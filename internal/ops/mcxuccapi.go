package ops

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
)

// ---- MCX UCC API ----
//
// Registers and updates clients' UCC at MCX by its UCC REST API, the same
// records as the UCC file (mcxuccmap.go builds them; this file only renames
// the fields to the API's JSON names). File and API share each client's
// registration ("MCX|<UCC>") and the day's batch numbers.
//
// From MCX's "UCC Registration - API" v0.6 beta (15 Oct 2025) and its
// Postman collection:
//   - POST /v1/ucc/token with headers tm-id and api-key gives a JWT
//     {token, token_type, issued_at, expires_at}; every other call sends
//     "Authorization: Bearer <token>".
//   - POST bodies are JSON encrypted with AES-256-GCM under the AES key MCX
//     e-mails the member (64 hex characters): base64(12-byte IV ‖
//     ciphertext ‖ 16-byte tag), sent as Content-Type application/base64.
//     2xx answers are encrypted the same way; 4xx and 5xx may be plain
//     (rate limit, firewall, bad JWT) and are told apart by the mime type.
//   - POST /v1/ucc/add {record_type "10", member_type "M", tmid, date,
//     batch_number, record_count, records: [...]} (at most 50 records);
//     each record's answer is {client_code, error_codes, status
//     (PASS_PENDING_VERIFICATION or FAIL), error_description}.
//   - POST /v1/ucc/update {tmid, records: [...]} (at most 50; client_code
//     and pan identify the client and cannot change); the answer is
//     {summary, results: [{index, client_code, status SUCCESS or FAIL,
//     error_code, error_description, updated_fields, validation_status}]}.
//   - POST /v1/ucc/search {client_code: code or [codes]} (or a date range;
//     param limits the fields) gives {items, total, page, size, pages},
//     items holding the record, verification_status (PAN verification)
//     and pan_response_status; ?page= for the next page.
//   - GET /v1/ucc/health gives {status: "ok"}.
//   - 10 requests a second per IP, 5 MB a request (429 and 413 beyond).
//   - Error codes are the UCC file's (mcxUCCErrors).
//   - Only the UAT address is given (https://ucc-uat.mcxindia.com); the
//     live address is assumed to be https://ucc.mcxindia.com and can be
//     changed on the connection.
//
// Choices where the document is not explicit (the safe reading):
//   - A client MCX holds (accepted before, by file, API or before
//     Ledgerline) goes by update, any other by add. Update sends the whole
//     record, as the file does, so MCX ends up with exactly what the file
//     would have given it.
//   - Add takes the next batch number of the day's series shared with the
//     file (error codes 007 and 009 suggest MCX checks them as for files);
//     date is DDMMYYYY as in the file's control record. tmid is a number in
//     add and text in update, as the document's samples show it.
//   - Answers are matched to clients by client_code (by index when MCX
//     gives no code). A client whose answer cannot be read stays Sent and is
//     looked up with search before anything is sent for it again, so an add
//     MCX took is never sent twice.
//   - Control-record and member-level errors (001 to 012, 301, 313) are not
//     the clients' fault: they go again, and the run stops.
//   - PASS_PENDING_VERIFICATION is Accepted with the PAN verification
//     pending; search later gives the verification. A PAN MCX could not
//     verify is shown as Rejected with a work item, as NSE's Incorrect PAN.
//   - The token is kept in memory only (never stored), until the lifetime
//     MCX gives (expires_at less issued_at) less a minute; on 401 it is
//     asked again once. After MCX refuses the tm-id or API key, or the
//     answer does not decrypt with the AES key, the automatic runs stop
//     (the connection shows Problem) until the Admin saves it again.
//   - Search is sent as the document describes (an encrypted JSON body);
//     the Postman collection's query parameters are not used.

const (
	connMCXUCC      = "mcx-ucc-api"
	cMCXUCCAPIState = "mcxuccapistate" // one document: timings
	mcxUCCAPIUAT    = "https://ucc-uat.mcxindia.com"
	mcxUCCAPILive   = "https://ucc.mcxindia.com"
	mcxUCCAPIBatch  = 50  // records a request (MCX's limit)
	mcxUCCAPIPerRun = 500 // clients sent or looked up in one run
	// mcxAtMCX is the Ref of a client an add found already at MCX: it goes
	// by update.
	mcxAtMCX = "at-mcx"
)

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connMCXUCC, Name: "MCX UCC API", Group: "Exchanges", Test: "https",
		Purpose: "Registers new clients with the commodity segment at MCX and sends their changes by MCX's UCC API (the same records as the MCX UCC file), and reads back MCX's PAN verification. Ask MCX in writing for the API key and the AES key (MCX e-mails them), and have MCX allow the server's address",
		Fields: []ConnField{{Key: "tmid", Label: "Trading member ID (leave empty for the MCX member code in Settings)", Optional: true},
			{Key: "api_key", Label: "API key (from MCX)", Secret: true},
			{Key: "aes_key", Label: "AES key (64 hexadecimal characters, from MCX)", Secret: true},
			{Key: "env", Label: "Environment", Options: []string{"UAT", "Live"}},
			{Key: "send", Label: "Send to MCX automatically (every 15 minutes, 08:00 to 22:00)", Options: []string{"Off", "On"}},
			{Key: "url", Label: "Address (leave empty for MCX's)", Optional: true, Placeholder: mcxUCCAPIUAT}, fNotes}})
}

// mcxUCCAPIFields are the API's JSON names of the UCC record, in the
// document's order, against the UCC file layout's keys (mcx-ucc.json).
// The file's contact persons 6 to 20 have no API field.
var mcxUCCAPIFields = func() [][2]string {
	f := [][2]string{{mxCode, "client_code"}, {mxName, "client_name"}, {mxCategory, "category"}, {mxPAN, "pan"}, {mxGender, "gender"},
		{mxGuardian, "fathers_husbands_guardians_name"}, {mxMarital, "marital_status"}, {mxNationality, "nationality"}, {mxNationalityOther, "nationality_other"},
		{mxEmail, "client_email_id"}, {mxAddr1, "address_1_corresponding"}, {mxAddr2, "address_2_corresponding"}, {mxAddr3, "address_3_corresponding"},
		{mxCountry, "country_corresponding"}, {mxStateCity, "state_city_code_corresponding"}, {mxPin, "pin_code_corresponding"},
		{mxPermSame, "permanent_address_same_as_corresponding_address"}, {mxPAddr1, "address_1_permanent"}, {mxPAddr2, "address_2_permanent"},
		{mxPAddr3, "address_3_permanent"}, {mxPCountry, "country_permanent"}, {mxPStateCity, "state_city_code_permanent"}, {mxPPin, "pin_code_permanent"},
		{"TelephoneResidence", "telephone_number_r"}, {mxMobile, "mobile_number"}, {"TelephoneOffice", "telephone_number_o"},
		{mxDOB, "date_of_birth_incorporation"}, {mxUID, "uid_number"}}
	for i := 1; i <= 3; i++ {
		n := strconv.Itoa(i)
		f = append(f, [2]string{"BankName" + n, "bank_name_" + n}, [2]string{"BankBranch" + n, "bank_branch_address_" + n},
			[2]string{"BankAccountNumber" + n, "bank_account_number_" + n}, [2]string{"BankAccountType" + n, "bank_account_type_" + n})
	}
	for i := 1; i <= 3; i++ {
		n := strconv.Itoa(i)
		f = append(f, [2]string{"Depository" + n, "depository_name_" + n}, [2]string{"DepositoryId" + n, "depository_id_" + n},
			[2]string{"BeneficiaryAccountId" + n, "beneficiary_account_id_" + n})
	}
	f = append(f, [][2]string{{"CinLei", "cin_lei"}, {"PlaceOfIncorporation", "place_of_incorporation"}, {mxOccupation, "occupation"},
		{mxOccDetail, "occupation_details_for_others"}, {"DateOfCommencementOfBusiness", "date_of_commencement_of_business"},
		{mxIncome, "income_details_per_annum"}, {mxIncomeDate, "income_as_on_date"}, {"NetWorth", "networth_in_rs_usd"}, {"NetWorthAsOnDate", "networth_as_on_date"},
		{mxPEP, "politically_exposed_person"}, {"CpCode", "cp_code"}, {"RegistrationNumber", "registration_number_of_client"},
		{"RegisteringAuthority", "registering_authority"}, {"PlaceOfRegistration", "place_of_registration"}, {"DateOfRegistration", "date_of_registration"},
		{mxIPV, "in_person_verification"}, {mxAgreement, "client_agreement_date"}, {"Reserved", "reserved_1"}, {"Relationship", "relationship_code"},
		{"ProofType", "proof_type"}, {"ProofNumber", "proof_number"}, {"IssuePlaceOfProof", "issue_place_of_proof"}, {"IssueDateOfProof", "issue_date_of_proof"},
		{mxStatus, "client_status"}, {mxRemarks, "remarks"}, {mxCorporate, "client_type_corporate"}}...)
	for i := 1; i <= 5; i++ {
		n, p := strconv.Itoa(i), "ContactPerson"+strconv.Itoa(i)
		f = append(f, [2]string{p + "Name", "name_of_contact_person_" + n}, [2]string{p + "Designation", "designation_of_contact_person_" + n},
			[2]string{p + "Pan", "pan_of_contact_person_" + n}, [2]string{p + "Address", "address_of_contact_person_" + n},
			[2]string{p + "ContactDetails", "contact_details_of_contact_person_" + n}, [2]string{p + "Din", "din_of_contact_person_" + n},
			[2]string{p + "Uid", "uid_of_contact_person_" + n}, [2]string{p + "Email", "email_id_of_contact_person_" + n})
	}
	return append(f, [][2]string{{mxRelMobile, "relationship_with_client_for_mobile_no"}, {mxNameMobile, "name_of_the_authorized_contact_person_for_mobile_no"},
		{mxRelEmail, "relationship_with_client_for_email_id"}, {mxNameEmail, "name_of_the_authorized_contact_person_for_email_id"},
		// Fields the file does not have: sent empty (nomination is optional).
		{"", "nomination"}, {"", "reserved_2"}, {"", "reserved_3"}, {"", "reserved_4"}, {"", "reserved_5"}, {"", "reserved_6"},
		{mxFlag, "flag"}}...)
}()

// mcxUCCAPIRecord is a client's record under the API's names.
func mcxUCCAPIRecord(r mcxUCCRecord) map[string]string {
	m := make(map[string]string, len(mcxUCCAPIFields))
	for _, f := range mcxUCCAPIFields {
		m[f[1]] = ""
		if f[0] != "" {
			m[f[1]] = r.Values[f[0]]
		}
	}
	return m
}

// mcxUCCAPIProblems is what the API cannot carry: a value in a file field
// that has no API field.
func mcxUCCAPIProblems(r mcxUCCRecord) []string {
	have := map[string]bool{}
	for _, f := range mcxUCCAPIFields {
		have[f[0]] = true
	}
	var out []string
	for _, k := range sortedKeys(r.Values) {
		if r.Values[k] != "" && !have[k] {
			out = append(out, mcxLabel(k)+" has no field in MCX's UCC API: register this client by the UCC file")
		}
	}
	return out
}

// mcxMaskJSON masks PANs, bank and demat account numbers and Aadhaar
// numbers in a request or answer, for the audit.
func mcxMaskJSON(b []byte) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return trimTo(string(b), 2000)
	}
	var walk func(x any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			for k, e := range t {
				s, ok := e.(string)
				switch {
				case !ok || s == "":
					t[k] = walk(e)
				case k == "pan" || strings.HasPrefix(k, "pan_of_"):
					t[k] = MaskPAN(s)
				case strings.HasPrefix(k, "bank_account_number") || strings.HasPrefix(k, "beneficiary_account_id"):
					t[k] = MaskAccount(s)
				case strings.HasPrefix(k, "uid"):
					t[k] = maskUID(s)
				}
			}
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
		}
		return x
	}
	out, _ := json.Marshal(walk(v))
	return trimTo(string(out), 16<<10)
}

// ---- the connection ----

type mcxUCCConn struct {
	base, tmid, apiKey string
	key                []byte
	live, auto         bool
}

// fp identifies the connection's settings, so a token is not reused after
// they change.
func (x mcxUCCConn) fp() string {
	s := sha256.Sum256([]byte(x.base + "\x00" + x.tmid + "\x00" + x.apiKey + "\x00" + hex.EncodeToString(x.key)))
	return hex.EncodeToString(s[:])
}

func parseMCXUCCConn(c Connection, member string) (mcxUCCConn, error) {
	x := mcxUCCConn{tmid: firstOf(strings.TrimSpace(c.Values["tmid"]), strings.TrimSpace(member)), apiKey: strings.TrimSpace(c.Secrets["api_key"]), auto: c.Values["send"] == "On"}
	if x.tmid == "" || x.apiKey == "" || c.Secrets["aes_key"] == "" {
		return x, invalid("the MCX UCC API connection needs the trading member ID (or the MCX member code in Settings), the API key and the AES key")
	}
	k, err := parseMCXAESKey(c.Secrets["aes_key"])
	if err != nil {
		return x, err
	}
	x.key = k
	x.base = mcxUCCAPIUAT
	if c.Values["env"] == "Live" {
		x.base = mcxUCCAPILive
	}
	if u := strings.TrimRight(strings.TrimSpace(c.Values["url"]), "/"); u != "" {
		x.base = u
	}
	x.live = x.base == mcxUCCAPILive
	return x, nil
}

// parseMCXAESKey reads the AES-256 key: 64 hexadecimal characters (as MCX's
// Postman collection takes it), or the base64 of 32 bytes.
func parseMCXAESKey(s string) ([]byte, error) {
	s = strings.Join(strings.Fields(s), "")
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, invalid("MCX's AES key must be 64 hexadecimal characters (a 256-bit key), as MCX gives it")
}

// mcxSeal encrypts a body: base64(IV ‖ ciphertext ‖ tag), AES-256-GCM.
func mcxSeal(key, plain []byte) ([]byte, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, g.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	return []byte(base64.StdEncoding.EncodeToString(g.Seal(iv, iv, plain, nil))), nil
}

// mcxOpen decrypts an answer sealed as mcxSeal does.
func mcxOpen(key, body []byte) ([]byte, error) {
	s := strings.TrimSpace(string(body))
	if len(s) >= 2 && s[0] == '"' {
		_ = json.Unmarshal([]byte(s), &s) // an answer written as a JSON string
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "=")); err != nil {
			return nil, errors.New("the answer is not base64")
		}
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	if len(raw) < g.NonceSize()+g.Overhead() {
		return nil, errors.New("the answer is too short to be encrypted")
	}
	pt, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], nil)
	if err != nil {
		return nil, errors.New("the answer does not decrypt with the AES key")
	}
	return pt, nil
}

// mcxUCCHTTP talks to MCX; tests replace it.
var mcxUCCHTTP = &http.Client{Timeout: 60 * time.Second}

// mcxUCCAPIGap spaces requests under MCX's 10 a second; tests set it to 0.
var mcxUCCAPIGap = 150 * time.Millisecond

// mcxUCCTokens holds each firm's token in memory only.
var mcxUCCTokens sync.Map // tenant → mcxUCCToken

type mcxUCCToken struct {
	token, fp string
	exp       time.Time
}

// mcxStop is a refusal that stops a run; auth says it is the credentials'.
type mcxStop struct {
	msg  string
	auth bool
}

func (e *mcxStop) Error() string { return e.msg }

// mcxUCCCall is one run's conversation with MCX.
type mcxUCCCall struct {
	ctx    context.Context
	tenant string
	x      mcxUCCConn
	now    func() time.Time
	calls  []BSEUCCCall
	sent   int // requests, for the gap
}

func (c *mcxUCCCall) do(method, path, token string, body []byte) (int, string, []byte, error) {
	if c.sent > 0 && mcxUCCAPIGap > 0 {
		select {
		case <-c.ctx.Done():
			return 0, "", nil, c.ctx.Err()
		case <-time.After(mcxUCCAPIGap):
		}
	}
	c.sent++
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(c.ctx, method, c.x.base+path, rd)
	if err != nil {
		return 0, "", nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/base64")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if path == "/v1/ucc/token" {
		req.Header["tm-id"] = []string{c.x.tmid} // as MCX writes them
		req.Header["api-key"] = []string{c.x.apiKey}
	}
	resp, err := mcxUCCHTTP.Do(req)
	if err != nil {
		return 0, "", nil, fmt.Errorf("could not reach MCX's UCC API: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, resp.Header.Get("Content-Type"), b, err
}

// answer is the readable answer: decrypted when MCX encrypted it (its mime
// type, or any 2xx answer that is not plain JSON).
func (c *mcxUCCCall) answer(code int, ctype string, b []byte) ([]byte, error) {
	t := bytes.TrimSpace(b)
	plain := len(t) > 0 && (t[0] == '{' || t[0] == '[')
	if strings.Contains(strings.ToLower(ctype), "base64") || (code/100 == 2 && !plain && len(t) > 0) {
		pt, err := mcxOpen(c.x.key, t)
		if err != nil {
			if plain {
				return t, nil
			}
			return nil, err
		}
		return pt, nil
	}
	return t, nil
}

// mcxMessage is MCX's message in a plain or decrypted error answer.
func mcxMessage(b []byte) string {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return trimBody(b)
	}
	for _, k := range []string{"detail", "message", "error_description", "error", "msg", "status"} {
		if v, ok := m[k]; ok && v != nil {
			if s, ok := v.(string); ok {
				return s
			}
			j, _ := json.Marshal(v)
			return trimTo(string(j), 300)
		}
	}
	return trimBody(b)
}

// mcxTokenLife is how long a token is used: the lifetime MCX gives
// (expires_at less issued_at, so the time zone does not matter), else the
// JWT's own expiry, else ten minutes; a minute is kept in hand.
func mcxTokenLife(tok, issued, expires string, now time.Time) time.Time {
	parse := func(s string) (time.Time, bool) {
		for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05Z07:00"} {
			if t, err := time.Parse(l, strings.TrimSpace(s)); err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	}
	exp := now.Add(10 * time.Minute)
	if i, ok1 := parse(issued); ok1 {
		if e, ok2 := parse(expires); ok2 && e.After(i) {
			exp = now.Add(e.Sub(i))
		}
	}
	if j := nseUCITokenExp(tok, now.Add(24*time.Hour)); j.Before(exp.Add(-time.Minute)) {
		return j // the JWT's exp, less a minute
	}
	return exp.Add(-time.Minute)
}

// token gives the firm's token, asking MCX when there is none or it has run
// out.
func (c *mcxUCCCall) token(force bool) (string, error) {
	fp := c.x.fp()
	if v, ok := mcxUCCTokens.Load(c.tenant); ok && !force {
		if t := v.(mcxUCCToken); t.fp == fp && c.now().Before(t.exp) {
			return t.token, nil
		}
	}
	code, ctype, b, err := c.do(http.MethodPost, "/v1/ucc/token", "", nil)
	call := BSEUCCCall{Function: "token", Request: "tm-id " + c.x.tmid, Code: code}
	defer func() { c.calls = append(c.calls, call) }()
	if err != nil {
		call.Error = err.Error()
		return "", &mcxStop{msg: err.Error()}
	}
	ans, derr := c.answer(code, ctype, b)
	if code/100 != 2 {
		msg := mcxMessage(firstBytes(ans, b))
		call.Response, call.Error = trimTo(msg, 500), msg
		if code == 401 || code == 403 {
			return "", &mcxStop{msg: fmt.Sprintf("MCX did not accept the trading member ID or API key (%d: %s): check them in Connections, and that MCX allows this server's address", code, msg), auth: true}
		}
		return "", &mcxStop{msg: fmt.Sprintf("MCX's token request failed (%d: %s)", code, msg)}
	}
	if derr != nil {
		call.Error = derr.Error()
		return "", &mcxStop{msg: "MCX's token answer " + strings.TrimPrefix(derr.Error(), "the answer ") + ": check the AES key in Connections", auth: true}
	}
	var t struct {
		Token     string `json:"token"`
		IssuedAt  string `json:"issued_at"`
		ExpiresAt string `json:"expires_at"`
	}
	if json.Unmarshal(ans, &t) != nil || t.Token == "" {
		call.Error = "no token in MCX's answer"
		return "", &mcxStop{msg: "MCX's token answer has no token", auth: true}
	}
	exp := mcxTokenLife(t.Token, t.IssuedAt, t.ExpiresAt, c.now())
	call.Response = "token issued, used until " + exp.In(ist).Format("02/01/2006 15:04") // never the token
	mcxUCCTokens.Store(c.tenant, mcxUCCToken{token: t.Token, fp: fp, exp: exp})
	return t.Token, nil
}

func firstBytes(a, b []byte) []byte {
	if len(a) > 0 {
		return a
	}
	return b
}

// post sends an encrypted request and reads the answer, asking for a new
// token once if MCX refuses the one held. ucc names the client for the
// audit of a one-client request.
func (c *mcxUCCCall) post(path, ucc string, payload any) (int, []byte, error) {
	plain, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	body, err := mcxSeal(c.x.key, plain)
	if err != nil {
		return 0, nil, err
	}
	call := BSEUCCCall{Function: strings.TrimPrefix(path, "/v1/ucc/"), UCC: ucc, Request: mcxMaskJSON(plain)}
	tok, err := c.token(false)
	if err != nil {
		return 0, nil, err
	}
	code, ctype, b, err := c.do(http.MethodPost, path, tok, body)
	if err == nil && code == 401 {
		mcxUCCTokens.Delete(c.tenant)
		if tok, err = c.token(true); err != nil {
			return code, nil, err
		}
		code, ctype, b, err = c.do(http.MethodPost, path, tok, body)
	}
	call.Code = code
	if err != nil {
		call.Error = err.Error()
		c.calls = append(c.calls, call)
		return 0, nil, &mcxStop{msg: err.Error()}
	}
	ans, derr := c.answer(code, ctype, b)
	if derr != nil {
		call.Error = derr.Error()
		c.calls = append(c.calls, call)
		if code/100 == 2 {
			return code, nil, &mcxStop{msg: "MCX's answer " + strings.TrimPrefix(derr.Error(), "the answer ") + ": check the AES key in Connections", auth: true}
		}
		ans = b
	}
	call.Response = mcxMaskJSON(ans)
	c.calls = append(c.calls, call)
	switch {
	case code == 401 || code == 403:
		return code, ans, &mcxStop{msg: fmt.Sprintf("MCX refused the request (%d: %s): check the API key in Connections, and that MCX allows this server's address", code, mcxMessage(ans)), auth: true}
	case code == 429:
		return code, ans, &mcxStop{msg: "MCX's limit of 10 requests a second was reached; the rest go on the next run"}
	case code == 413:
		return code, ans, &mcxStop{msg: "MCX refused the request as larger than 5 MB"}
	case code >= 500:
		return code, ans, &mcxStop{msg: fmt.Sprintf("MCX's server answered with an error (%d: %s)", code, mcxMessage(ans))}
	}
	return code, ans, nil
}

func (o *Ops) mcxUCCAPIConnection(ctx context.Context, tenant string) (Connection, mcxUCCConn, error) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connMCXUCC)
	if err != nil || cn.Status == "Not set" {
		return cn, mcxUCCConn{}, fmt.Errorf("%w: set up the MCX UCC API in Settings → Connections", ErrState)
	}
	set, _ := o.Settings(ctx, tenant)
	x, err := parseMCXUCCConn(cn, set.MemberCodes["MCX"])
	return cn, x, err
}

// mcxUCCConnMark records how the connection answered.
func (o *Ops) mcxUCCConnMark(ctx context.Context, tenant, status, problem string) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cConn, connMCXUCC, SystemActor, func(c *Connection) error {
		if c.Status == "Not set" {
			return docs.ErrStale
		}
		c.Status, c.Problem, c.TestedAt, c.TestedBy = status, problem, o.now().Format(time.RFC3339), SystemActor
		return nil
	})
}

// mcxUCCAPITest is the connection's test: a token (the tm-id and API key,
// and that the AES key decrypts the answer), then the health check.
func (o *Ops) mcxUCCAPITest(ctx context.Context, tenant string, cn Connection) (string, string) {
	set, _ := o.Settings(ctx, tenant)
	x, err := parseMCXUCCConn(cn, set.MemberCodes["MCX"])
	if err != nil {
		return "Problem", strings.TrimPrefix(err.Error(), "invalid: ")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := &mcxUCCCall{ctx: cctx, tenant: tenant, x: x, now: o.now}
	tok, err := c.token(true)
	if err != nil {
		return "Problem", err.Error()
	}
	code, ctype, b, err := c.do(http.MethodGet, "/v1/ucc/health", tok, nil)
	if err != nil {
		return "Problem", err.Error()
	}
	ans, _ := c.answer(code, ctype, b)
	var h struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(ans, &h)
	if code != 200 || !strings.EqualFold(h.Status, "ok") {
		return "Problem", fmt.Sprintf("MCX issued a token, but its health check answered %d: %s", code, mcxMessage(ans))
	}
	env := "UAT"
	if x.live {
		env = "live"
	}
	return "Working", "MCX (" + env + ") issued a token and its health check is ok"
}

// ---- MCX's answers ----

// mcxRecAnswer is MCX's answer for one record.
type mcxRecAnswer struct {
	index                           int // -1: none given
	code, status, errs, desc, valid string
}

func mcxStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		var p []string
		for _, e := range t {
			if s := mcxStr(e); s != "" {
				p = append(p, s)
			}
		}
		return strings.Join(p, ",")
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// mcxRecAnswers reads add's and update's answers: a list of records, one
// record, or an object holding the list (update's "results").
func mcxRecAnswers(b []byte) []mcxRecAnswer {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var objs []map[string]any
	isRec := func(m map[string]any) bool {
		_, a := m["client_code"]
		_, s := m["status"]
		return a || s && m["results"] == nil
	}
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				objs = append(objs, m)
			}
		}
	case map[string]any:
		if isRec(t) {
			objs = []map[string]any{t}
			break
		}
		for _, k := range append([]string{"results", "records", "data", "items", "response"}, sortedKeys(t)...) {
			if l, ok := t[k].([]any); ok && len(l) > 0 {
				for _, e := range l {
					if m, ok := e.(map[string]any); ok {
						objs = append(objs, m)
					}
				}
				break
			}
		}
	}
	var out []mcxRecAnswer
	for _, m := range objs {
		a := mcxRecAnswer{index: -1, code: strings.ToUpper(mcxStr(m["client_code"])), status: strings.ToUpper(mcxStr(m["status"])), desc: mcxStr(m["error_description"]), valid: mcxStr(m["validation_status"])}
		a.errs = firstOf(mcxStr(m["error_codes"]), mcxStr(m["error_code"]))
		if f, ok := m["index"].(float64); ok {
			a.index = int(f)
		}
		out = append(out, a)
	}
	return out
}

// ok says whether MCX took the record (SKIPPED: MCX already holds it).
func (a mcxRecAnswer) ok() bool {
	return strings.HasPrefix(a.status, "PASS") || strings.HasPrefix(a.status, "SUCCESS") || a.status == "SKIPPED"
}

func (a mcxRecAnswer) failed() bool {
	return strings.HasPrefix(a.status, "FAIL") || strings.HasPrefix(a.status, "REJECT") || strings.HasPrefix(a.status, "ERROR")
}

var reMCXCodes = regexp.MustCompile(`\d+`)

// text is the rejection in words: each error code, and MCX's description.
func (a mcxRecAnswer) text() string {
	var p []string
	for _, c := range reMCXCodes.FindAllString(a.errs, -1) {
		p = append(p, MCXUCCError(c))
	}
	switch {
	case len(p) == 0 && a.desc == "":
		return "MCX rejected it without a reason"
	case len(p) == 0:
		return "MCX rejected it: " + a.desc
	case a.desc != "":
		return strings.Join(p, "; ") + " (MCX: " + a.desc + ")"
	}
	return strings.Join(p, "; ")
}

// notClients says whether the errors are the request's or the member's, not
// the client's: control-record errors, a member banned or disabled, size.
func (a mcxRecAnswer) notClients() bool {
	cs := reMCXCodes.FindAllString(a.errs, -1)
	for _, c := range cs {
		n, _ := strconv.Atoi(c)
		if !(n >= 1 && n <= 12 || n == 301 || n == 313) {
			return false
		}
	}
	return len(cs) > 0
}

// ---- sending ----

// mcxUCCAPIItems are the firm's MCX clients, by code.
func mcxItemMap(items []mcxUCCItem) map[string]mcxUCCItem {
	m := make(map[string]mcxUCCItem, len(items))
	for _, it := range items {
		m[it.c.UCC] = it
	}
	return m
}

// MCXUCCAPISend sends pending clients (or the ones named) to MCX by its UCC
// API: an add for a client MCX does not hold, an update for one it does.
func (o *Ops) MCXUCCAPISend(ctx context.Context, tenant, actor string, uccs []string) (BSEUCCResult, error) {
	res := BSEUCCResult{Problems: []string{}}
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Sending UCC to MCX", RoleMaker, RoleChecker); err != nil {
			return res, err
		}
	}
	_, x, err := o.mcxUCCAPIConnection(ctx, tenant)
	if err != nil {
		return res, err
	}
	defer mcxUCCLock(tenant)()
	now := o.now()
	date := now.In(ist).Format("2006-01-02")
	items, baseline, err := o.mcxUCCItems(ctx, tenant, date)
	if err != nil {
		return res, err
	}
	if len(baseline) > 0 {
		o.mcxUCCBaseline(ctx, tenant, date, baseline)
	}
	want := map[string]bool{}
	for _, u := range uccs {
		if u = strings.ToUpper(strings.TrimSpace(u)); u != "" {
			want[u] = true
		}
	}
	c := &mcxUCCCall{ctx: ctx, tenant: tenant, x: x, now: o.now}
	at := now.Format(time.RFC3339)
	var stopped *mcxStop

	// Clients sent by API whose answer was not read: ask MCX first, so a
	// record it took is not sent again.
	var unanswered []string
	for _, it := range items {
		if (len(want) == 0 || want[it.c.UCC]) && it.reg.Status == UCCSent && it.reg.Via == "API" {
			unanswered = append(unanswered, it.c.UCC)
		}
	}
	if len(unanswered) > 0 {
		var sr BSEUCCResult
		if err := o.mcxUCCAPILookup(ctx, tenant, actor, c, mcxItemMap(items), unanswered, date, &sr); err != nil {
			errors.As(err, &stopped)
			if stopped == nil {
				stopped = &mcxStop{msg: err.Error()}
			}
		}
		res.Accepted += sr.Accepted
		if items, _, err = o.mcxUCCItems(ctx, tenant, date); err != nil {
			return res, err
		}
	}

	found := map[string]bool{}
	var adds, updates []mcxUCCItem
	for _, it := range items {
		if len(want) > 0 && !want[it.c.UCC] {
			continue
		}
		found[it.c.UCC] = true
		if !it.pending {
			if len(want) > 0 {
				msg := "nothing to send (MCX has these details)"
				if it.reg.Status == UCCSent {
					msg = "sent, waiting for MCX's answer"
				}
				res.Problems = append(res.Problems, it.c.UCC+": "+msg)
			}
			continue
		}
		probs := append(append([]string{}, it.rec.Problems...), mcxUCCAPIProblems(it.rec)...)
		if len(probs) > 0 {
			r := it.reg
			r.Problems = probs
			if r.Status != UCCNeedsFixing || r.Reason != probs[0] {
				r.event(at, actor, UCCNeedsFixing, probs[0], "")
			}
			o.mcxUCCSaveReg(ctx, tenant, r)
			res.Problems = append(res.Problems, it.c.UCC+": "+strings.Join(probs, "; "))
			continue
		}
		if len(adds)+len(updates) >= mcxUCCAPIPerRun {
			continue
		}
		if it.reg.Action == "Modify" {
			updates = append(updates, it)
		} else {
			adds = append(adds, it)
		}
	}
	for u := range want {
		if !found[u] {
			res.Problems = append(res.Problems, u+": not a client to register at MCX (only clients with the commodity segment at UCC upload, or registered before)")
		}
	}

	send := func(kind string, group []mcxUCCItem) {
		for len(group) > 0 && stopped == nil {
			n := min(len(group), mcxUCCAPIBatch)
			chunk := group[:n]
			group = group[n:]
			if s := o.mcxUCCAPIRequest(ctx, tenant, actor, c, kind, chunk, now, &res); s != nil {
				stopped = s
			}
		}
	}
	if stopped == nil {
		send("update", updates)
		send("add", adds)
	}

	if stopped != nil {
		res.Problems = append(res.Problems, "Stopped: "+stopped.msg)
		if stopped.auth {
			mcxUCCTokens.Delete(tenant)
			o.mcxUCCConnMark(ctx, tenant, "Problem", stopped.msg)
		}
	} else if res.Sent > 0 {
		o.mcxUCCConnMark(ctx, tenant, "Working", "")
	}
	switch {
	case res.Sent == 0 && stopped == nil:
		res.Message = "Nothing to send to MCX"
		if n := len(res.Problems); n > 0 {
			res.Message = fmt.Sprintf("Nothing sent to MCX: %d client(s) need fixing first", n)
		}
		if res.Accepted > 0 {
			res.Message = fmt.Sprintf("%d client(s) found accepted at MCX; nothing else to send", res.Accepted)
		}
	default:
		res.Message = fmt.Sprintf("Sent %d to MCX by API: %d accepted, %d rejected", res.Sent, res.Accepted, res.Rejected)
		if stopped != nil {
			res.Message += "; stopped: " + stopped.msg
		}
	}
	o.log(ctx, tenant, actor, "mcxucc.api_send", "", map[string]any{"sent": res.Sent, "accepted": res.Accepted, "rejected": res.Rejected, "stopped": stopped != nil})
	return res, nil
}

// mcxUCCAPIRequest sends one add or update request of up to 50 clients and
// records each client's answer. It returns what stopped the run, if any.
func (o *Ops) mcxUCCAPIRequest(ctx context.Context, tenant, actor string, c *mcxUCCCall, kind string, chunk []mcxUCCItem, now time.Time, res *BSEUCCResult) *mcxStop {
	at := now.Format(time.RFC3339)
	date := now.In(ist).Format("2006-01-02")
	// The token first: a refused login leaves the clients as they were.
	if _, err := c.token(false); err != nil {
		var stop *mcxStop
		if !errors.As(err, &stop) {
			stop = &mcxStop{msg: err.Error()}
		}
		return stop
	}
	recs := make([]map[string]string, len(chunk))
	for i, it := range chunk {
		recs[i] = mcxUCCAPIRecord(it.rec)
	}
	doc := MCXUCCBatchDoc{Date: date, Via: "API", Kind: kind, Hashes: map[string]string{}, Actions: map[string]string{}, Summaries: map[string]map[string]string{},
		WrittenAt: at, WrittenBy: actor, UploadedAt: at, UploadedBy: actor}
	var payload any
	path := "/v1/ucc/" + kind
	if kind == "add" {
		seq, _ := o.mcxUCCNextBatch(ctx, tenant, date)
		if seq > 99 {
			return &mcxStop{msg: fmt.Sprintf("MCX takes batches 01 to 99 a day; %s has no batch left", fmtDay(date))}
		}
		doc.Batch, doc.ID = seq, mcxBatchKey(date, seq)
		doc.File = fmt.Sprintf("API add %02d of %s", seq, fmtDay(date))
		var tm any = c.x.tmid
		if n, err := strconv.Atoi(c.x.tmid); err == nil {
			tm = n // a number in MCX's add sample
		}
		payload = map[string]any{"record_type": "10", "member_type": "M", "tmid": tm, "date": mcxDate(date), "batch_number": fmt.Sprintf("%02d", seq),
			"record_count": strconv.Itoa(len(recs)), "records": recs}
	} else {
		n, err := o.next(ctx, tenant, "mcxuccapiupdate", actor)
		if err != nil {
			return &mcxStop{msg: err.Error()}
		}
		doc.ID = fmt.Sprintf("%s|U%06d", date, n)
		doc.File = fmt.Sprintf("API update %06d", n)
		payload = map[string]any{"tmid": c.x.tmid, "records": recs}
	}
	// Sent before the call: a request with no readable answer is looked up
	// before the client goes again.
	regs := make([]BSEUCCReg, len(chunk))
	for i, it := range chunk {
		r := it.reg
		r.Problems = nil
		r.Hash, r.SentAt, r.Via, r.Ref = it.hash, at, "API", doc.File
		r.event(at, actor, UCCSent, "Sent by MCX's UCC API ("+kind+")", doc.File)
		o.mcxUCCSaveReg(ctx, tenant, r)
		regs[i] = r
		doc.UCCs = append(doc.UCCs, it.c.UCC)
		doc.Hashes[it.c.UCC], doc.Actions[it.c.UCC], doc.Summaries[it.c.UCC] = it.hash, r.Action, it.rec.Summary
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cMCXUCCBatch, doc.ID, doc, actor)
	ucc := ""
	if len(chunk) == 1 {
		ucc = chunk[0].c.UCC
	}
	from := len(c.calls)
	code, ans, err := c.post(path, ucc, payload)
	doc.Calls = append(doc.Calls, c.calls[from:]...)
	res.Sent += len(chunk)
	save := func() { _ = docs.Upsert(ctx, o.Docs, tenant, cMCXUCCBatch, doc.ID, doc, actor) }

	var stop *mcxStop
	if err != nil {
		if !errors.As(err, &stop) {
			stop = &mcxStop{msg: err.Error()}
		}
		if stop.auth && code/100 != 2 || code == 429 || code == 413 {
			// Not taken by MCX (refused before reading it): the clients go
			// again. A 2xx answer that does not decrypt leaves them Sent: MCX
			// may have taken them, so they are looked up first.
			for i := range regs {
				regs[i].Hash = ""
				regs[i].event(at, actor, UCCNotSent, "Not sent: "+stop.msg, "")
				o.mcxUCCSaveReg(ctx, tenant, regs[i])
			}
			res.Sent -= len(chunk)
		}
		doc.FileError = stop.msg
		save()
		return stop
	}
	answers := mcxRecAnswers(ans)
	if code/100 != 2 && len(answers) == 0 {
		// The request was refused whole (e.g. invalid JSON): not the
		// clients' fault; they go again once it is put right.
		msg := fmt.Sprintf("MCX refused the %s request (%d: %s)", kind, code, mcxMessage(ans))
		for i := range regs {
			regs[i].Hash = ""
			regs[i].event(at, actor, UCCNotSent, msg, "")
			o.mcxUCCSaveReg(ctx, tenant, regs[i])
		}
		res.Sent -= len(chunk)
		doc.FileError, doc.ResponseAt = msg, at
		save()
		o.raiseWork(ctx, tenant, "mcxucc", "api:"+date, "MCX refused a UCC API request",
			msg+". The clients were not registered and go again with the next send; if it repeats, check the MCX UCC API connection or contact MCX.", "", RoleMaker, "uccapi", date)
		return &mcxStop{msg: msg}
	}
	byCode := map[string]mcxRecAnswer{}
	for _, a := range answers {
		if a.code != "" {
			byCode[a.code] = a
		}
	}
	ref := doc.File
	for i := range regs {
		r := &regs[i]
		a, ok := byCode[chunk[i].c.UCC]
		if !ok {
			for _, x := range answers {
				if x.code == "" && (x.index == i || x.index < 0 && len(answers) == len(chunk) && answers[i].code == "") {
					a, ok = x, true
					break
				}
			}
		}
		switch {
		case !ok || !a.ok() && !a.failed():
			r.event(at, actor, UCCSent, fmt.Sprintf("MCX's answer (%d) could not be read for this client: looked up with MCX's search before it goes again", code), ref)
		case a.ok():
			note := ""
			if strings.Contains(a.status, "PENDING") || strings.EqualFold(a.valid, "PENDING") {
				note, r.PANStatus = "PAN verification pending at MCX", "Pending"
			}
			o.mcxUCCAnswered(ctx, tenant, actor, r, at, ref, date, "", note, chunk[i].rec.Summary, "")
			res.Accepted++
			doc.Accepted++
		case a.notClients():
			r.Hash = ""
			r.event(at, actor, UCCNotSent, a.text()+": not the client's record; it goes again", "")
			if stop == nil {
				stop = &mcxStop{msg: a.text()}
			}
			res.Sent--
		default:
			o.mcxUCCAnswered(ctx, tenant, actor, r, at, ref, date, a.text(), "", nil, "the corrected record goes to MCX again by the API (or in the next UCC file).")
			if r.AcceptedHash == "" && chunk[i].reg.Action == "Modify" {
				r.Ref = mcxAtMCX // still held at MCX: the correction goes as an update
			}
			res.Rejected++
			doc.Rejected++
			res.Problems = append(res.Problems, r.UCC+": "+a.text())
		}
		o.mcxUCCSaveReg(ctx, tenant, *r)
	}
	doc.ResponseAt = at
	if stop != nil {
		doc.FileError = stop.msg
	}
	save()
	return stop
}

// ---- status from MCX's search ----

// mcxSame says whether MCX's record has what was sent, on the fields that
// show a change took (those MCX returns).
func mcxSame(item map[string]any, rec map[string]string) bool {
	norm := func(s string) string { return strings.ToUpper(strings.Join(strings.Fields(s), " ")) }
	for _, k := range []string{"client_name", "client_email_id", "mobile_number", "address_1_corresponding", "pin_code_corresponding", "state_city_code_corresponding",
		"bank_account_number_1", "client_status", "income_details_per_annum"} {
		v, ok := item[k]
		if !ok {
			continue
		}
		if norm(mcxStr(v)) != norm(rec[k]) {
			return false
		}
	}
	return true
}

// mcxSearch looks codes up with MCX's search, every page.
func (c *mcxUCCCall) search(codes []string) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for page := 1; page <= 20; page++ {
		path := "/v1/ucc/search"
		if page > 1 {
			path += "?page=" + strconv.Itoa(page)
		}
		code, ans, err := c.post(path, "", map[string]any{"client_code": codes})
		if err != nil {
			return out, err
		}
		if code == 404 {
			return out, nil // none of them
		}
		if code/100 != 2 {
			return out, &mcxStop{msg: fmt.Sprintf("MCX's search answered %d: %s", code, mcxMessage(ans))}
		}
		var r struct {
			Items []map[string]any `json:"items"`
			Pages int              `json:"pages"`
		}
		if json.Unmarshal(ans, &r) != nil {
			return out, &mcxStop{msg: "MCX's search answer could not be read"}
		}
		for _, it := range r.Items {
			if u := strings.ToUpper(mcxStr(it["client_code"])); u != "" {
				out[u] = it
			}
		}
		if page >= r.Pages {
			break
		}
	}
	return out, nil
}

// mcxUCCAPILookup asks MCX's search for clients and records what it holds:
// a client sent with no answer read becomes Accepted when MCX has it (an
// add, or an update MCX's record shows), Not sent when MCX does not; MCX's
// PAN verification is kept. The caller holds the firm's lock.
func (o *Ops) mcxUCCAPILookup(ctx context.Context, tenant, actor string, c *mcxUCCCall, items map[string]mcxUCCItem, codes []string, date string, res *BSEUCCResult) error {
	at := o.now().Format(time.RFC3339)
	ref := "MCX search " + o.now().In(ist).Format("02/01/2006 15:04")
	for len(codes) > 0 {
		n := min(len(codes), mcxUCCAPIBatch)
		chunk := codes[:n]
		codes = codes[n:]
		held, err := c.search(chunk)
		if err != nil {
			return err
		}
		for _, u := range chunk {
			r, _, gerr := docs.Get[BSEUCCReg](ctx, o.Docs, tenant, cBSEUCCReg, mcxRegKey(u))
			if gerr != nil {
				continue
			}
			it, hasItem := items[u]
			item, isHeld := held[u]
			res.Sent++
			switch {
			case !isHeld && r.Status == UCCSent && r.Via == "API":
				r.Hash = ""
				r.event(at, actor, UCCNotSent, "MCX has no record of the client: it goes again", ref)
				o.mcxUCCSaveReg(ctx, tenant, r)
				continue
			case !isHeld:
				r.PANCheckedOn = date // asked today
				o.mcxUCCSaveReg(ctx, tenant, r)
				continue
			case r.Status == UCCSent && (r.Action == "Add" || hasItem && it.hash == r.Hash && mcxSame(item, mcxUCCAPIRecord(it.rec))):
				var sum map[string]string
				if hasItem && it.hash == r.Hash {
					sum = it.rec.Summary
				}
				if s := mcxStr(item["client_status"]); s != "" {
					sum = map[string]string{"status": s}
				}
				o.mcxUCCAnswered(ctx, tenant, actor, &r, at, ref, date, "", "Found at MCX by its search", sum, "")
				res.Accepted++
			case r.Status == UCCSent && r.Via == "API":
				r.Hash = ""
				r.event(at, actor, UCCNotSent, "MCX's record differs from the one sent: it goes again as an update", ref)
			case r.Status == UCCRejected && r.AcceptedHash == "" && r.Action == "Add":
				r.Hash, r.Ref = "", mcxAtMCX
				r.event(at, actor, UCCNotSent, "MCX already holds this client code: the record goes as an update", mcxAtMCX)
				o.closeWork(ctx, tenant, actor, "mcxucc", "rej:"+u, "held at MCX; sent as an update")
			}
			o.mcxUCCPAN(ctx, tenant, actor, &r, item, at, date)
			o.mcxUCCSaveReg(ctx, tenant, r)
		}
	}
	return nil
}

// mcxUCCPAN keeps MCX's PAN verification of a client MCX holds: one it
// could not verify is Rejected with a work item; verified later, Accepted.
func (o *Ops) mcxUCCPAN(ctx context.Context, tenant, actor string, r *BSEUCCReg, item map[string]any, at, date string) {
	vs := strings.ToLower(mcxStr(item["verification_status"]))
	if vs == "" || r.AcceptedHash == "" {
		return
	}
	why := mcxStr(item["pan_response_status"])
	was := r.PANStatus
	r.PANCheckedOn = date
	switch {
	case vs == "success" || vs == "verified" || vs == "approved":
		r.PANStatus = "Valid"
		if r.Status == UCCRejected && strings.HasPrefix(was, "Invalid") {
			r.event(at, actor, UCCAccepted, "MCX verified the PAN", "")
			o.closeWork(ctx, tenant, actor, "mcxucc", "pan:"+r.UCC, "PAN verified by MCX")
		} else if r.Status == UCCAccepted && r.Reason == "PAN verification pending at MCX" {
			r.Reason = ""
		}
	case vs == "rejected" || vs == "failed" || vs == "invalid":
		r.PANStatus = "Invalid"
		if why != "" {
			r.PANStatus += " · " + why
		}
		if r.Status == UCCAccepted {
			text := "MCX could not verify the PAN with the Income Tax Department" + map[bool]string{true: ": " + why, false: ""}[why != ""]
			r.event(at, actor, UCCRejected, text, "")
			o.raiseWork(ctx, tenant, "mcxucc", "pan:"+r.UCC, "MCX could not verify the PAN of "+firstOf(r.Name, r.UCC),
				r.UCC+": "+text+". Check the PAN, name and date of birth against the PAN card and correct the client record; the corrected record goes to MCX as an update.", r.UCC, RoleMaker, "uccapi", date)
		}
	default:
		r.PANStatus = "Pending"
	}
	if strings.HasPrefix(was, "Invalid") && strings.HasPrefix(r.PANStatus, "Invalid") {
		r.PANStatus = was
	}
}

// MCXUCCAPISync asks MCX's search for the clients sent and not confirmed
// and for those whose PAN verification is not done (or the ones named).
func (o *Ops) MCXUCCAPISync(ctx context.Context, tenant, actor string, uccs []string) (BSEUCCResult, error) {
	res := BSEUCCResult{Problems: []string{}}
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Checking UCC status at MCX", RoleMaker, RoleChecker, RoleCompliance); err != nil {
			return res, err
		}
	}
	_, x, err := o.mcxUCCAPIConnection(ctx, tenant)
	if err != nil {
		return res, err
	}
	defer mcxUCCLock(tenant)()
	date := o.now().In(ist).Format("2006-01-02")
	items, _, err := o.mcxUCCItems(ctx, tenant, date)
	if err != nil {
		return res, err
	}
	regs, err := docs.List[BSEUCCReg](ctx, o.Docs, tenant, cBSEUCCReg, "MCX|")
	if err != nil {
		return res, err
	}
	want := map[string]bool{}
	for _, u := range uccs {
		if u = strings.ToUpper(strings.TrimSpace(u)); u != "" {
			want[u] = true
		}
	}
	var codes []string
	for _, r := range regs {
		switch {
		case len(want) > 0:
			if !want[r.UCC] {
				continue
			}
			delete(want, r.UCC)
		case r.Status == UCCSent:
		case r.AcceptedHash != "" && r.Via != "Before Ledgerline" && !strings.HasPrefix(r.PANStatus, "Valid") && r.PANCheckedOn != date:
		case r.Status == UCCRejected && r.AcceptedHash == "" && r.Action == "Add" && r.Via == "API" && r.PANCheckedOn != date:
		default:
			continue
		}
		codes = append(codes, r.UCC)
	}
	for u := range want {
		res.Problems = append(res.Problems, u+": not a client registered or sent to MCX")
	}
	sort.Strings(codes)
	if len(codes) > mcxUCCAPIPerRun {
		codes = codes[:mcxUCCAPIPerRun]
	}
	if len(codes) == 0 {
		res.Message = "Nothing to ask MCX"
		return res, nil
	}
	c := &mcxUCCCall{ctx: ctx, tenant: tenant, x: x, now: o.now}
	err = o.mcxUCCAPILookup(ctx, tenant, actor, c, mcxItemMap(items), codes, date, &res)
	var stop *mcxStop
	if err != nil {
		if !errors.As(err, &stop) {
			stop = &mcxStop{msg: err.Error()}
		}
		res.Problems = append(res.Problems, "Stopped: "+stop.msg)
		if stop.auth {
			mcxUCCTokens.Delete(tenant)
			o.mcxUCCConnMark(ctx, tenant, "Problem", stop.msg)
		}
	}
	res.Message = fmt.Sprintf("Asked MCX about %d client(s): %d found accepted", res.Sent, res.Accepted)
	if stop != nil {
		res.Message += "; stopped: " + stop.msg
	}
	o.log(ctx, tenant, actor, "mcxucc.api_sync", "", map[string]any{"asked": res.Sent, "accepted": res.Accepted, "stopped": stop != nil})
	return res, nil
}

// ---- scheduler ----

type mcxUCCAPIStateDoc struct {
	LastSend string `json:"last_send,omitempty"`
	LastSync string `json:"last_sync,omitempty"`
}

// mcxUCCAPITick sends pending clients every 15 minutes and asks MCX's
// search every hour, from 08:00 to 22:00, when sending is switched on and
// the connection has no problem.
func (o *Ops) mcxUCCAPITick(ctx context.Context, tenant string, now time.Time) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connMCXUCC)
	if err != nil || cn.Status == "Not set" || cn.Status == "" || cn.Status == "Problem" || cn.Values["send"] != "On" {
		return
	}
	if hm := now.In(ist).Format("15:04"); hm < "08:00" || hm >= "22:00" {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cMCXUCCAPIState, "state", mcxUCCAPIStateDoc{})
	if t, err := time.Parse(time.RFC3339, st.LastSend); err != nil || now.Sub(t) >= 15*time.Minute {
		st.LastSend = now.Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cMCXUCCAPIState, "state", st, SystemActor)
		_, _ = o.MCXUCCAPISend(ctx, tenant, SystemActor, nil)
	}
	if t, err := time.Parse(time.RFC3339, st.LastSync); err != nil || now.Sub(t) >= time.Hour {
		st.LastSync = now.Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cMCXUCCAPIState, "state", st, SystemActor)
		_, _ = o.MCXUCCAPISync(ctx, tenant, SystemActor, nil)
	}
}
