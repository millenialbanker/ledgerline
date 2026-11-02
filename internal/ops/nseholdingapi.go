package ops

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
)

// ---- NSE holding statement by API ----
//
// Every trading member reports each client's demat holdings to NSE daily
// (each day of the week except Sunday, by the next trading day; a day can
// only be sent once the day before it has been) through NSE's inspection
// API. From NSE's public circulars NSE/INSP/55039 (28 December 2022) and
// NSE/INSP/55380 (25 January 2023, Annexure A "API Specifications, Member
// Holding Statement and Bank Account Balances Daily Submissions", and
// Annexure B, the registration and submission manual):
//
//   - The member registers on NSE's inspection member portal; NSE mails a
//     login ID, a password and a secret key (which changes with every
//     password reset).
//   - POST {base}/login {memberCode, loginId, password}: the password AES
//     encrypted (ECB, PKCS#5/7 padding) with the secret key (base64 decoded
//     to the raw key), base64. The answer {code: ["601"], token, status}:
//     a JWT for 30 minutes, sent as "Authorization: Bearer <token>".
//   - POST {base}/tradingHoldingUpload/{version} (version 1.0), multipart
//     form, key "File": the holding statement JSON (layout
//     holding-statement-api), gzip-compressed, named
//     <member PAN>_HS_<DDMMYYYY>_<seq>.gz for the data date.
//   - The answer {hdrCode: [..], hdrStatus, ackId, memberPan, date,
//     filePeriodDate, holdingData: [{recId, code: [..], status}]}: 601
//     success, 603 partial, 805 an earlier day is still to be sent, 9xx a
//     field of a record, 5xx the file (codes below).
//   - POST {base}/logout {memberCode, loginId}.
//   - Production base https://api.nseindia.com/inspectionapi; the circulars
//     give no UAT address for the API (it can be set on the connection).
//
// The same circulars named a daily bank account balance submission; NSE
// discontinued members' reporting of bank balances and statements from 30
// October 2023, the banks now report them directly (NSE/INSP/59044), so it
// is not to be built. The firm confirms its banks do (a yearly duty).
//
// BSE takes the holding statement daily too (BSE notice 20230113-13), by its
// own API or the BEFS portal; from 15 October 2025 a member of NSE and BSE
// reports it to NSE only and NSE shares it (NSE/INSP/70505). A BSE-only
// member uploads it on BEFS from Exchange files (bseholding.go) until BSE's
// API document is available.

const (
	connNSEHolding = "nse-holding-api"
	cNSEHSub       = "nsehsub"   // key: date|seq
	cNSEHState     = "nsehstate" // one document

	nseHoldingLive    = "https://api.nseindia.com/inspectionapi"
	nseHoldingVersion = "1.0"
)

// nseHoldingCodes are NSE's response codes in plain words (NSE/INSP/55380,
// Annexure A).
var nseHoldingCodes = map[string]string{
	"601": "accepted", "602": "general error at NSE", "603": "only part of the file was accepted", "604": "none of the holding records was correct",
	"605": "something went wrong at NSE", "606": "unknown method", "608": "the previous request is still being processed: try again later",
	"701": "member code, login ID or password is wrong", "702": "the API user is disabled", "703": "member code or login ID is wrong", "704": "invalid declaration",
	"705": "a JSON key is wrong", "706": "error", "803": "the login token is wrong or has expired", "804": "too many records in one request",
	"805": "an earlier day's holding statement has not been submitted yet: submit the earlier days first", "806": "the member PAN is not right",
	"807": "the date is outside the holding period", "808": "the holding date format is wrong", "809": "the holding period is wrong", "810": "the file period date format is wrong",
	"901": "the demat account number is not right", "902": "the account type is not one NSE takes", "903": "the client code (UCC) is not right", "904": "the client name is not right",
	"905": "the client PAN is not right", "906": "the ISIN is not right", "907": "the security type is not one NSE takes", "908": "the pledged quantity is not right",
	"909": "the pledged quantity has more than 3 decimals", "910": "the free quantity is not right", "911": "the free quantity has more than 3 decimals",
	"912": "the total quantity is not right", "913": "the total quantity has more than 3 decimals", "914": "the total must equal pledged plus free",
	"916": "the action is not right", "917": "a field is blank", "918": "the record is repeated", "919": "the request ID is not right", "920": "the version is not right",
	"921": "the syntax is not right", "922": "the file format is not right", "923": "the address is not right", "924": "the content type is not right",
	"925": "NSE's login server is down", "926": "NSE's holding server is down", "927": "NSE's bank balance server is down", "928": "the account is inactive",
	"929": "API registration is not complete", "930": "the holding period has not started", "931": "uploads are no longer allowed for this period",
	"932": "the security name is not right", "933": "the unit type is not right", "934": "the commodity name is not right", "956": "NSE is busy: try later",
	"958": "NSE's server is down", "959": "this API call is not allowed",
	"510": "the file name is not right", "511": "the file sequence number is not right", "512": "the date is wrong", "513": "the file type is not right",
	"514": "the member PAN is not right", "515": "the file format is wrong", "516": "this file was already submitted", "517": "the file header is wrong",
	"518": "server error at NSE", "519": "no file was received", "520": "the file is over 200 MB", "521": "the member code is not known",
	"522": "NSE could not copy the file", "524": "NSE could not unzip the file", "526": "NSE could not read the file", "527": "the file must be sent as multipart",
	"530": "the PAN does not match the member", "531": "the period has been deleted", "533": "an earlier file is still being processed",
}

func nseHoldingPlain(codes []string) string {
	var out []string
	for _, c := range codes {
		if w, ok := nseHoldingCodes[c]; ok {
			out = append(out, w+" ("+c+")")
		} else if c != "" {
			out = append(out, "NSE code "+c)
		}
	}
	return strings.Join(out, "; ")
}

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connNSEHolding, Name: "NSE holding statement API", Group: "Exchanges", Test: "https",
		Purpose: "Submits the daily client holding statement to NSE by API (NSE circulars NSE/INSP/55039 and NSE/INSP/55380). Register on NSE's inspection member portal; NSE mails the login ID, password and secret key",
		Fields: []ConnField{{Key: "member", Label: "Member code (5 digits)"},
			{Key: "login", Label: "API login ID (leave empty for ApiUser<member code>)", Optional: true},
			{Key: "password", Label: "API password", Secret: true},
			{Key: "secret_key", Label: "Secret key (from NSE's registration email; changes with every password reset)", Secret: true},
			{Key: "send", Label: "Submit the previous day's statement automatically", Options: []string{"Off", "On"}},
			{Key: "start", Label: "First data date to submit automatically (YYYY-MM-DD; empty: from yesterday)", Optional: true},
			{Key: "version", Label: "API version (leave empty for 1.0)", Optional: true, Placeholder: nseHoldingVersion},
			{Key: "url", Label: "Address (leave empty for NSE's)", Optional: true, Placeholder: nseHoldingLive}, fNotes}})
}

// nseHoldingHTTP is replaced in tests.
var nseHoldingHTTP = &http.Client{Timeout: 3 * time.Minute}

// One submission at a time per server: NSE refuses overlapping requests (608).
var nseHoldingMu sync.Mutex

type nseHoldingConn struct {
	base, version, member, login, password string
	key                                    []byte
	auto                                   bool
	start                                  string
}

func parseNSEHoldingConn(c Connection) (nseHoldingConn, error) {
	x := nseHoldingConn{member: strings.TrimSpace(c.Values["member"]), password: c.Secrets["password"], auto: c.Values["send"] == "On", start: strings.TrimSpace(c.Values["start"])}
	x.login = firstOf(strings.TrimSpace(c.Values["login"]), "ApiUser"+x.member)
	if len(x.member) != 5 || x.password == "" {
		return x, invalid("the NSE holding statement API connection needs the 5-digit member code and the API password")
	}
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.Secrets["secret_key"]))
	if err != nil || (len(k) != 16 && len(k) != 24 && len(k) != 32) {
		return x, invalid("the secret key must be the base64 key from NSE's registration email (it decodes to 16, 24 or 32 bytes)")
	}
	x.key = k
	x.base = firstOf(strings.TrimRight(strings.TrimSpace(c.Values["url"]), "/"), nseHoldingLive)
	x.version = firstOf(strings.TrimSpace(c.Values["version"]), nseHoldingVersion)
	if x.start != "" && !validDate(x.start) {
		return x, invalid("the first data date must be YYYY-MM-DD")
	}
	return x, nil
}

// nseHoldingPassword encrypts the password as NSE asks: AES-ECB, PKCS#7, base64.
func nseHoldingPassword(password string, key []byte) (string, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	n := aes.BlockSize - len(password)%aes.BlockSize
	src := append([]byte(password), bytes.Repeat([]byte{byte(n)}, n)...)
	out := make([]byte, len(src))
	for i := 0; i < len(src); i += aes.BlockSize {
		b.Encrypt(out[i:i+aes.BlockSize], src[i:i+aes.BlockSize])
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

// NSEHoldingError is one record NSE did not accept.
type NSEHoldingError struct {
	RecID string   `json:"rec_id,omitempty"`
	UCC   string   `json:"ucc,omitempty"`
	ISIN  string   `json:"isin,omitempty"`
	Codes []string `json:"codes"`
	Plain string   `json:"plain"`
}

// NSEHoldingSubmission is one submission and NSE's answer.
type NSEHoldingSubmission struct {
	ID        string            `json:"id"` // date|seq
	Date      string            `json:"date"`
	Seq       int               `json:"seq"`
	File      string            `json:"file"`
	Records   int               `json:"records"`
	SHA256    string            `json:"sha256,omitempty"`
	At        string            `json:"at"`
	By        string            `json:"by"`
	HTTP      int               `json:"http,omitempty"`
	HdrCode   []string          `json:"hdr_code,omitempty"`
	HdrStatus string            `json:"hdr_status,omitempty"`
	AckID     string            `json:"ack_id,omitempty"`
	Status    string            `json:"status"` // Accepted, Partly accepted, Rejected, Failed, Not sent
	Reason    string            `json:"reason,omitempty"`
	Errors    []NSEHoldingError `json:"errors"`
	Response  string            `json:"response,omitempty"` // PANs masked, cut at 32 KB
}

// NSEHoldingState is the automatic submission's progress.
type NSEHoldingState struct {
	LastTick  string `json:"last_tick,omitempty"`
	LastOK    string `json:"last_ok,omitempty"` // last data date accepted
	LastError string `json:"last_error,omitempty"`
}

// NSEHoldingView is the holding statement panel on Exchange files.
type NSEHoldingView struct {
	Date        string                 `json:"date"`
	Connection  string                 `json:"connection"` // Not set, Saved, Working, Problem
	Auto        bool                   `json:"auto"`
	Problem     string                 `json:"problem,omitempty"`
	Submissions []NSEHoldingSubmission `json:"submissions"`
	State       NSEHoldingState        `json:"state"`
}

func (o *Ops) nseHoldingConn(ctx context.Context, tenant string) (Connection, nseHoldingConn, error) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNSEHolding)
	if err != nil || cn.Status == "Not set" {
		return cn, nseHoldingConn{}, fmt.Errorf("%w: set up the NSE holding statement API in Settings → Connections; until then download the file and upload it on NSE's portal", ErrState)
	}
	x, err := parseNSEHoldingConn(cn)
	return cn, x, err
}

// NSEHolding lists the submissions (all dates, newest first, or one date).
func (o *Ops) NSEHolding(ctx context.Context, tenant, actor, date string) (NSEHoldingView, error) {
	if err := o.require(ctx, tenant, actor, "Downloading exchange files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return NSEHoldingView{}, err
	}
	v := NSEHoldingView{Date: date, Connection: "Not set", Submissions: []NSEHoldingSubmission{}}
	if cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNSEHolding); err == nil {
		v.Connection, v.Auto = firstOf(cn.Status, "Saved"), cn.Values["send"] == "On"
		if _, err := parseNSEHoldingConn(cn); err != nil {
			v.Problem = strings.TrimPrefix(err.Error(), "invalid: ")
		}
	}
	v.State, _, _ = docs.GetOr(ctx, o.Docs, tenant, cNSEHState, "state", NSEHoldingState{})
	p := ""
	if date != "" {
		p = date + "|"
	}
	ss, err := docs.List[NSEHoldingSubmission](ctx, o.Docs, tenant, cNSEHSub, p)
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].At > ss[j].At })
	if len(ss) > 60 {
		ss = ss[:60]
	}
	if ss != nil {
		v.Submissions = ss
	}
	return v, err
}

// holdingItem is the part of a holding record the checks need.
type holdingItem struct {
	RecID, Dmat, UCC, ClientName, PAN, ISIN, TotalPldgQty, FreeBalQty, TotalQty string
}

// nseHoldingProblems checks the records against NSE's field rules before
// sending (NSE/INSP/55380 codes 901, 904-906, 914, 917, 918).
func nseHoldingProblems(items []holdingItem) []string {
	var out []string
	seen := map[string]bool{}
	for _, it := range items {
		who := it.UCC + " " + it.ISIN
		switch {
		case it.Dmat == "" || len(it.Dmat) > 16:
			out = append(out, who+": the demat account is missing on the client's record")
		case it.ClientName == "":
			out = append(out, who+": the client's name is missing")
		case !rePAN.MatchString(it.PAN):
			out = append(out, who+": the client's PAN is missing or not valid ("+MaskPAN(it.PAN)+")")
		case len(it.ISIN) != 12:
			out = append(out, who+": the ISIN is not 12 characters")
		}
		k := it.Dmat + "|" + it.UCC + "|" + it.ISIN
		if seen[k] {
			out = append(out, who+": the holding is in the file twice")
		}
		seen[k] = true
		p, f, t := decOr0(it.TotalPldgQty), decOr0(it.FreeBalQty), decOr0(it.TotalQty)
		if p.Add(f).Cmp(t) != 0 {
			out = append(out, who+": total is not pledged plus free")
		}
	}
	return out
}

type nseHoldingCall struct {
	ctx context.Context
	x   nseHoldingConn
}

func (c nseHoldingCall) do(req *http.Request) (int, []byte, error) {
	resp, err := nseHoldingHTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("could not reach NSE's holding statement API: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return resp.StatusCode, b, err
}

func (c nseHoldingCall) postJSON(path, token string, body any) (int, []byte, error) {
	j, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.x.base+path, bytes.NewReader(j))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.do(req)
}

// codes reads a code field given as a list or a single value.
func nseHoldingCodeList(v any) []string {
	switch x := v.(type) {
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, strings.TrimSpace(fmt.Sprint(e)))
		}
		return out
	case nil:
		return nil
	default:
		if s := strings.TrimSpace(fmt.Sprint(x)); s != "" {
			return []string{s}
		}
	}
	return nil
}

func (c nseHoldingCall) loginToken() (string, error) {
	pw, err := nseHoldingPassword(c.x.password, c.x.key)
	if err != nil {
		return "", err
	}
	code, b, err := c.postJSON("/login", "", map[string]string{"memberCode": c.x.member, "loginId": c.x.login, "password": pw})
	if err != nil {
		return "", err
	}
	var r map[string]any
	_ = json.Unmarshal(b, &r)
	tok, _ := r["token"].(string)
	codes := nseHoldingCodeList(r["code"])
	if code != 200 || tok == "" || (len(codes) > 0 && !contains(codes, "601")) {
		why := nseHoldingPlain(codes)
		if why == "" {
			why = fmt.Sprintf("HTTP %d", code)
		}
		return "", fmt.Errorf("NSE did not accept the holding API login: %s; check the member code, login ID, password and secret key (five wrong passwords lock the account)", why)
	}
	return tok, nil
}

// NSEHoldingSubmit submits a data date's holding statement to NSE.
func (o *Ops) NSEHoldingSubmit(ctx context.Context, tenant, actor, date string) (NSEHoldingSubmission, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Submitting the holding statement to NSE", RoleMaker, RoleChecker, RoleCompliance); err != nil {
			return NSEHoldingSubmission{}, err
		}
	}
	if !validDate(date) {
		return NSEHoldingSubmission{}, invalid("give the date the holdings are for")
	}
	_, x, err := o.nseHoldingConn(ctx, tenant)
	if err != nil {
		return NSEHoldingSubmission{}, err
	}
	nseHoldingMu.Lock()
	defer nseHoldingMu.Unlock()
	prev, _ := docs.List[NSEHoldingSubmission](ctx, o.Docs, tenant, cNSEHSub, date+"|")
	seq := 1
	for _, p := range prev {
		if p.Status != "Not sent" && p.Seq >= seq {
			seq = p.Seq + 1
		}
	}
	lib, err := XFiles()
	if err != nil {
		return NSEHoldingSubmission{}, err
	}
	spec, _ := lib.Get("holding-statement-api")
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return NSEHoldingSubmission{}, err
	}
	name, j, err := o.holdingStatementFile(ctx, tenant, spec, set, date, map[string]string{"date": date, "seq": fmt.Sprint(seq)})
	if err != nil {
		return NSEHoldingSubmission{}, err
	}
	var msg struct {
		HoldingData []holdingItem `json:"holdingData"`
	}
	if err := json.Unmarshal(j, &msg); err != nil {
		return NSEHoldingSubmission{}, err
	}
	byRec := map[string]holdingItem{}
	for _, it := range msg.HoldingData {
		byRec[it.RecID] = it
	}
	gzName := strings.TrimSuffix(name, ".json") + ".gz"
	sub := NSEHoldingSubmission{ID: fmt.Sprintf("%s|%02d", date, seq), Date: date, Seq: seq, File: maskPANs(gzName), Records: len(msg.HoldingData), SHA256: sha256Hex(j),
		At: o.now().Format(time.RFC3339), By: actor, Errors: []NSEHoldingError{}}
	save := func() (NSEHoldingSubmission, error) {
		if err := docs.Upsert(ctx, o.Docs, tenant, cNSEHSub, sub.ID, sub, actor); err != nil {
			return sub, err
		}
		o.log(ctx, tenant, actor, "holding.submitted", sub.ID, map[string]any{"status": sub.Status, "records": sub.Records, "hdr_code": strings.Join(sub.HdrCode, ","), "ack": sub.AckID})
		if sub.Status == "Accepted" {
			o.closeWork(ctx, tenant, actor, "holding-api", date, "accepted by NSE, acknowledgement "+sub.AckID)
			st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNSEHState, "state", NSEHoldingState{})
			if date > st.LastOK {
				st.LastOK = date
			}
			st.LastError = ""
			_ = docs.Upsert(ctx, o.Docs, tenant, cNSEHState, "state", st, SystemActor)
			return sub, nil
		}
		detail := fmt.Sprintf("The holding statement for %s was not fully accepted by NSE: %s.", fmtDay(date), firstOf(sub.Reason, sub.Status))
		for i, e := range sub.Errors {
			if i == 10 {
				detail += fmt.Sprintf(" …and %d more records.", len(sub.Errors)-10)
				break
			}
			detail += " " + strings.TrimSpace(e.UCC+" "+e.ISIN) + ": " + e.Plain + "."
		}
		detail += " Correct the records and submit again from Exchange files (each submission gets the next file number); the JSON file can also be uploaded on NSE's portal."
		o.raiseWork(ctx, tenant, "holding-api", date, "Holding statement for "+fmtDay(date)+": "+sub.Status, detail, "", RoleCompliance, "xfiles", date)
		st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNSEHState, "state", NSEHoldingState{})
		st.LastError = date + ": " + firstOf(sub.Reason, sub.Status)
		_ = docs.Upsert(ctx, o.Docs, tenant, cNSEHState, "state", st, SystemActor)
		return sub, nil
	}
	if ps := nseHoldingProblems(msg.HoldingData); len(ps) > 0 {
		sub.Status, sub.Reason = "Not sent", fmt.Sprintf("%d record(s) would fail NSE's checks", len(ps))
		for i, p := range ps {
			if i == 50 {
				break
			}
			sub.Errors = append(sub.Errors, NSEHoldingError{Plain: p})
		}
		return save()
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Name = name
	zw.ModTime = o.now()
	if _, err := zw.Write(j); err != nil {
		return sub, err
	}
	if err := zw.Close(); err != nil {
		return sub, err
	}
	c := nseHoldingCall{ctx: ctx, x: x}
	tok, err := c.loginToken()
	if err != nil {
		sub.Status, sub.Reason = "Failed", err.Error()
		return save()
	}
	defer func() {
		_, _, _ = c.postJSON("/logout", tok, map[string]string{"memberCode": x.member, "loginId": x.login})
	}()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="File"; filename="%s"`, gzName))
	h.Set("Content-Type", "application/gzip")
	pw, _ := mw.CreatePart(h)
	_, _ = pw.Write(gz.Bytes())
	_ = mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.base+"/tradingHoldingUpload/"+x.version, &body)
	if err != nil {
		return sub, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+tok)
	code, b, err := c.do(req)
	sub.HTTP = code
	if r := maskPANs(string(b)); len(r) > 32<<10 {
		sub.Response = r[:32<<10] + "…"
	} else {
		sub.Response = r
	}
	if err != nil {
		sub.Status, sub.Reason = "Failed", err.Error()
		return save()
	}
	var r map[string]any
	if json.Unmarshal(b, &r) != nil {
		sub.Status, sub.Reason = "Failed", fmt.Sprintf("NSE answered HTTP %d without a readable reply", code)
		return save()
	}
	sub.HdrCode = nseHoldingCodeList(r["hdrCode"])
	if sub.HdrCode == nil {
		sub.HdrCode = nseHoldingCodeList(r["code"])
	}
	sub.HdrStatus, _ = r["hdrStatus"].(string)
	sub.AckID = strings.TrimSpace(fmt.Sprint(firstNonNil(r["ackId"], "")))
	if rows, ok := r["holdingData"].([]any); ok {
		for _, x := range rows {
			m, _ := x.(map[string]any)
			codes := nseHoldingCodeList(m["code"])
			if len(codes) == 0 || (len(codes) == 1 && codes[0] == "601") {
				continue
			}
			e := NSEHoldingError{RecID: strings.TrimSpace(fmt.Sprint(firstNonNil(m["recId"], ""))), Codes: codes, Plain: nseHoldingPlain(codes)}
			if it, ok := byRec[e.RecID]; ok && e.RecID != "" {
				e.UCC, e.ISIN = it.UCC, it.ISIN
			}
			sub.Errors = append(sub.Errors, e)
		}
	}
	switch {
	case code == 200 && contains(sub.HdrCode, "601") && len(sub.Errors) == 0:
		sub.Status = "Accepted"
	case code == 200 && (contains(sub.HdrCode, "603") || contains(sub.HdrCode, "601")):
		sub.Status, sub.Reason = "Partly accepted", firstOf(nseHoldingPlain(sub.HdrCode), "some records were not accepted")
	default:
		sub.Status = "Rejected"
		sub.Reason = firstOf(nseHoldingPlain(sub.HdrCode), fmt.Sprintf("HTTP %d", code))
		if sub.HdrStatus != "" && !strings.EqualFold(sub.HdrStatus, "success") {
			sub.Reason += " — NSE: " + maskPANs(sub.HdrStatus)
		}
	}
	return save()
}

func firstNonNil(v, def any) any {
	if v == nil {
		return def
	}
	return v
}

// latestHoldingFile is the latest data date of a CDSL statement of holding
// loaded on Exchange files ("" when none).
func (o *Ops) latestHoldingFile(ctx context.Context, tenant string) string {
	rs, _ := docs.List[DailyFileRecv](ctx, o.Docs, tenant, cDailyRecv, "")
	last := ""
	for _, r := range rs {
		if r.Format == "cdsl-holding" && r.Date > last {
			last = r.Date
		}
	}
	return last
}

// nseHoldingTick, when automatic submission is on, submits each data day
// (every day but Sunday) up to yesterday that NSE has not accepted, oldest
// first, once the CDSL statement of holding for it is loaded, every 30
// minutes from 07:00 to 22:00. Ledgerline keeps today's holdings only, so a
// day whose holdings were already replaced by a later file is not sent: a
// work item asks for it to be submitted on NSE's portal. A day still waiting
// for its holding file at 20:00 also opens a work item.
func (o *Ops) nseHoldingTick(ctx context.Context, tenant string, now time.Time) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNSEHolding)
	if err != nil || cn.Status == "Not set" || cn.Values["send"] != "On" {
		return
	}
	x, err := parseNSEHoldingConn(cn)
	if err != nil {
		return
	}
	now = now.In(ist)
	hm := now.Format("15:04")
	if hm < "07:00" || hm >= "22:00" {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNSEHState, "state", NSEHoldingState{})
	if t, err := time.Parse(time.RFC3339, st.LastTick); err == nil && now.Sub(t) < 30*time.Minute {
		return
	}
	st.LastTick = now.Format(time.RFC3339)
	_ = docs.Upsert(ctx, o.Docs, tenant, cNSEHState, "state", st, SystemActor)
	set, _ := o.Settings(ctx, tenant)
	yesterday := addDays(now.Format("2006-01-02"), -1)
	from := firstOf(x.start, yesterday)
	if lim := addDays(yesterday, -7); from < lim {
		from = lim
	}
	latest := o.latestHoldingFile(ctx, tenant)
	for d := from; d <= yesterday; d = addDays(d, 1) {
		if t, _ := time.Parse("2006-01-02", d); t.Weekday() == time.Sunday {
			continue
		}
		subs, _ := docs.List[NSEHoldingSubmission](ctx, o.Docs, tenant, cNSEHSub, d+"|")
		done, tried := false, 0
		for _, s := range subs {
			done = done || s.Status == "Accepted" || s.Status == "Partly accepted"
			if s.By == SystemActor {
				tried++
			}
		}
		if done {
			continue
		}
		if tried >= 3 {
			return // the work item from the last try is open; a person takes over
		}
		base := d // the last trading day on or before d holds its holdings
		for i := 0; i < 10 && !set.IsBusinessDay(base); i++ {
			base = addDays(base, -1)
		}
		switch {
		case latest > d:
			o.raiseWork(ctx, tenant, "holding-api", d, "Holding statement for "+fmtDay(d)+": submit on NSE's portal",
				"The holding statement for "+fmtDay(d)+" was not submitted before a later CDSL holding file was loaded, and Ledgerline keeps the latest holdings only. Submit that day's statement on NSE's inspection portal; NSE takes a day only after the day before it.", "", RoleCompliance, "xfiles", d)
			return
		case latest < base:
			if hm >= "20:00" {
				o.raiseWork(ctx, tenant, "holding-api", d, "Holding statement for "+fmtDay(d)+": CDSL holding file not loaded",
					"The holding statement for "+fmtDay(d)+" is sent to NSE once the CDSL statement of holding for "+fmtDay(base)+" is loaded on Exchange files. Load it, or submit the statement by hand.", "", RoleCompliance, "xfiles", d)
			}
			return
		}
		sub, err := o.NSEHoldingSubmit(ctx, tenant, SystemActor, d)
		if err != nil || sub.Status != "Accepted" {
			return // recorded, with a work item; later days wait (NSE code 805)
		}
	}
}
