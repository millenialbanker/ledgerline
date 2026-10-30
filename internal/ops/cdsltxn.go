package ops

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// ---- CDSL APIs: transactions (Common upload), DESTAT ----
//
// The DP's CDSL connection ("cdsl-api", set up in cdslapi.go) carries the
// DP ID, the environment (Mock or Live), the API user and its secrets, and
// one on/off switch per API family. This file reads it by ID and keeps its
// own calls:
//
//   - Common upload (CDSL API Handbook §5.7): verified delivery instruction
//     slips (off-market and inter-depository) go to CDSL as a transaction
//     upload file through the API instead of the DIS batch file. Ledgerline
//     signs in (AuthWeb APIAuth, a token for 15 minutes), uploads the zipped
//     file (Upload v2.0), asks for its status and, when CDAS has processed
//     it, downloads the response file and marks each instruction executed
//     or failed (ExecuteDIS). With the API off, the batch file stays.
//   - DESTAT (§5.8): conversion of mutual fund units held in a statement of
//     account into the demat account. A pending demat request with a folio
//     is sent to CDSL's DESTAT page by a redirect POST; the investor
//     confirms with the OTP CDSL sends, and CDSL posts the result back with
//     a DESTAT request number (DRN) per ISIN. The request then waits for the
//     RTA like any demat request (Sent to RTA, then the checker credits it).
//
// Encryption: AES-256-CBC, PKCS#7, a null (all-zero) IV, base64, under the
// key CDSL issues for the API family (§5.8.7, §5.11.6, §5.21.6). The common
// upload's AuthSign is the SHA-256 of the exact body, encrypted the same
// way under the user's secret key (§5.7.5–5.7.6).

const (
	connCDSLAPI = "cdsl-api" // defined in cdslapi.go; read here by ID

	cCDSLUpload  = "cdslupload"  // transaction uploads through the API
	cCDSLDestat  = "cdsldestat"  // DESTAT requests, by reqid
	cCDSLTxState = "cdsltxstate" // "state": sign-in token and last runs

	// Families of the cdsl-api connection's on/off switches.
	cdxFamCommon = "common_upload"
	cdxFamDestat = "destat"
	cdxFamEDIS   = "edis"
	cdxFamReport = "report"

	// cdxUploadID is CDAS's upload ID for the transaction upload module
	// (the handbook's sample). The connection's "upload_id" value, when set,
	// replaces it.
	cdxUploadID = "18"
)

// cdxHosts are CDSL's API gateways (§5.7.3, §5.8.5, §5.21.5); tests replace them.
var cdxHosts = map[bool]string{false: "https://mockapigt.cdsl.co.in", true: "https://apigt.cdsl.co.in"}

// cdxHTTP is replaced in tests.
var cdxHTTP = &http.Client{Timeout: 2 * time.Minute}

// cdxMu keeps one sign-in per tenant at a time.
var cdxMu sync.Mutex

// cdxConn is what this file needs from the cdsl-api connection.
type cdxConn struct {
	dpID     string // 5 or 6 digits, as CDSL wants it ("10900", not "12010900")
	live     bool
	base     string
	user     string
	secret   string // the API user's secret key (common upload)
	password string
	values   map[string]string
	secrets  map[string]string
	status   string
}

// cdxSet reports whether the cdsl-api connection is there at all.
func (c cdxConn) set() bool { return c.status != "" && c.status != "Not set" }

// on reports whether an API family is switched on in the connection.
func (c cdxConn) on(fam string) bool {
	if !c.set() {
		return false
	}
	for _, k := range []string{fam, "api_" + fam, fam + "_api", "use_" + fam} {
		switch strings.ToLower(strings.TrimSpace(c.values[k])) {
		case "on", "yes", "y", "true", "1", "enabled":
			return true
		case "off", "no", "n", "false", "0", "disabled":
			return false
		}
	}
	return false
}

// key is the AES key of an API family: its own key when the connection has
// one (CDSL issues separate keys for eDIS, DESTAT and reports), else the
// shared secret.
func (c cdxConn) key(fam string) string {
	ks := []string{fam + "_key", fam + "_api_key", "api_key", "aes_key", "secret"}
	if fam == cdxFamCommon {
		ks = []string{"secret"} // the common upload signs with the user's secret key
	}
	for _, k := range ks {
		if v := strings.TrimSpace(c.secrets[k]); v != "" {
			return v
		}
	}
	return ""
}

// cdxDPID normalises a CDSL DP ID to the 5 or 6 digits the APIs take.
func cdxDPID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 8 && strings.HasPrefix(s, "12") {
		s = s[2:]
	}
	return s
}

// cdxBODP is the DP ID part of a CDSL BO ID.
func cdxBODP(bo string) string {
	bo = strings.TrimSpace(bo)
	if len(bo) != 16 {
		return ""
	}
	return strings.TrimLeft(bo[2:8], "0")
}

func (o *Ops) cdxConn(ctx context.Context, tenant string) cdxConn {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connCDSLAPI)
	if err != nil {
		return cdxConn{status: "Not set"}
	}
	x := cdxConn{dpID: cdxDPID(c.Values["dp_id"]), user: strings.TrimSpace(c.Values["user"]), secret: c.Secrets["secret"], password: c.Secrets["password"],
		values: c.Values, secrets: c.Secrets, status: c.Status}
	if x.values == nil {
		x.values = map[string]string{}
	}
	if x.secrets == nil {
		x.secrets = map[string]string{}
	}
	switch strings.ToLower(strings.TrimSpace(c.Values["env"])) {
	case "live", "production", "prod":
		x.live = true
	}
	x.base = cdxHosts[x.live]
	for _, k := range []string{"base_url", "url"} {
		if u, err := url.Parse(strings.TrimRight(c.Values[k], "/")); err == nil && u.Scheme == "https" && u.Host != "" {
			x.base = u.Scheme + "://" + u.Host
			break
		}
	}
	return x
}

// ---- encryption (AES-256-CBC, PKCS#7, null IV, base64) ----

// cdxKeyBytes reads a CDSL API key: the key text itself (32 characters for
// AES-256), or base64 of 16, 24 or 32 bytes.
func cdxKeyBytes(k string) ([]byte, error) {
	k = strings.TrimSpace(k)
	switch len(k) {
	case 16, 24, 32:
		return []byte(k), nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(k); err == nil && (len(b) == 16 || len(b) == 24 || len(b) == 32) {
			return b, nil
		}
	}
	if k == "" {
		return nil, invalid("the CDSL API key is not set: enter it in Settings → Connections → CDSL APIs (DP)")
	}
	return nil, invalid("the CDSL API key must be the 32-character key CDSL issued")
}

// cdxEncrypt encrypts with AES-CBC, PKCS#7 padding and an all-zero IV, and
// gives base64.
func cdxEncrypt(key string, plain []byte) (string, error) {
	kb, err := cdxKeyBytes(key)
	if err != nil {
		return "", err
	}
	blk, err := aes.NewCipher(kb)
	if err != nil {
		return "", err
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	p := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, len(p))
	cipher.NewCBCEncrypter(blk, make([]byte, aes.BlockSize)).CryptBlocks(out, p)
	return base64.StdEncoding.EncodeToString(out), nil
}

// cdxDecrypt reverses cdxEncrypt. It tolerates what a form post does to
// base64 (a + read as a space), line breaks and quotes.
func cdxDecrypt(key, text string) ([]byte, error) {
	kb, err := cdxKeyBytes(key)
	if err != nil {
		return nil, err
	}
	text = strings.Trim(strings.TrimSpace(text), `"`)
	text = strings.NewReplacer("\r", "", "\n", "", " ", "+", `\/`, "/").Replace(text)
	var ct []byte
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(text); err == nil {
			ct = b
			break
		}
	}
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return nil, errors.New("not CDSL's encrypted data")
	}
	blk, err := aes.NewCipher(kb)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(blk, make([]byte, aes.BlockSize)).CryptBlocks(out, ct)
	n := int(out[len(out)-1])
	if n == 0 || n > aes.BlockSize || n > len(out) || !bytes.Equal(out[len(out)-n:], bytes.Repeat([]byte{byte(n)}, n)) {
		return nil, errors.New("decryption failed: the key does not match")
	}
	return out[:len(out)-n], nil
}

// cdxSHA256 is the hex SHA-256 of b.
func cdxSHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// cdxAuthSign is the common upload's AuthSign: the body's SHA-256,
// encrypted under the user's secret key.
func cdxAuthSign(key string, body []byte) (string, error) {
	return cdxEncrypt(key, []byte(cdxSHA256(body)))
}

// ---- reading CDSL's JSON, whatever its key spelling ----

// cdxObj is a JSON object with its keys trimmed and lower-cased: the
// handbook's samples spell the same key several ways (" RevocReqTxnId ",
// "Errorcode", "ErrorCode").
type cdxObj map[string]any

func cdxNormal(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := cdxObj{}
		for k, v := range x {
			m[strings.ToLower(strings.TrimSpace(k))] = cdxNormal(v)
		}
		return m
	case []any:
		for i := range x {
			x[i] = cdxNormal(x[i])
		}
		return x
	}
	return v
}

func cdxParse(b []byte) (cdxObj, error) {
	var v any
	d := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(bytes.TrimSpace(b), []byte("\xef\xbb\xbf"))))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	m, ok := cdxNormal(v).(cdxObj)
	if !ok {
		return nil, errors.New("not a JSON object")
	}
	return m, nil
}

// in returns the object under one of names, or m itself.
func (m cdxObj) in(names ...string) cdxObj {
	for _, n := range names {
		if x, ok := m[n].(cdxObj); ok {
			return x
		}
	}
	return m
}

func (m cdxObj) str(k string) string {
	switch x := m[strings.ToLower(k)].(type) {
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	case bool:
		return fmt.Sprint(x)
	}
	return ""
}

func (m cdxObj) truth(k string) bool {
	switch x := m[strings.ToLower(k)].(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true")
	}
	return false
}

func (m cdxObj) list(k string) []cdxObj {
	var out []cdxObj
	if xs, ok := m[strings.ToLower(k)].([]any); ok {
		for _, x := range xs {
			if o, ok := x.(cdxObj); ok {
				out = append(out, o)
			}
		}
	}
	return out
}

// cdxError finds CDSL's error code and message anywhere in a reply.
func cdxError(m cdxObj) string {
	var walk func(x cdxObj) string
	walk = func(x cdxObj) string {
		if id := firstOf(x.str("errorid"), x.str("errorcode")); id != "" {
			return strings.TrimSpace(id + " " + firstOf(x.str("errormessage"), x.str("errormsg")))
		}
		for _, v := range x {
			if o, ok := v.(cdxObj); ok {
				if s := walk(o); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return walk(m)
}

// cdxNumericID is a numeric ID of n digits: the time, then random digits.
func cdxNumericID(now time.Time, n int, layout string) string {
	s := now.In(ist).Format(layout)
	for len(s) < n {
		d, _ := rand.Int(rand.Reader, big.NewInt(10))
		s += d.String()
	}
	return s[:n]
}

// cdxTime is CDSL's ddmmyyyyhhmmss, IST.
func cdxTime(t time.Time) string { return t.In(ist).Format("02012006150405") }

// cdxPost sends a form (multipart, as CDSL's eDIS gateway takes) with extra
// headers and returns the status and body.
func cdxPostForm(ctx context.Context, address string, headers, fields map[string]string) (int, []byte, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_ = mw.WriteField(k, fields[k])
	}
	_ = mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, &buf)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return cdxDo(req)
}

func cdxDo(req *http.Request) (int, []byte, error) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := cdxHTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("could not reach CDSL: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxXFileBytes))
	return resp.StatusCode, b, err
}

// cdxDecryptReply reads an encrypted reply body: the base64 text itself, a
// JSON string, or a JSON object holding it.
func cdxDecryptReply(key string, b []byte) (cdxObj, error) {
	t := strings.TrimSpace(string(b))
	if m, err := cdxParse(b); err == nil {
		for _, v := range m.in("body") {
			if s, ok := v.(string); ok && len(s) >= 24 {
				if p, err := cdxDecrypt(key, s); err == nil {
					return cdxParse(p)
				}
			}
		}
		return m, nil // already plain
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		t = s
	}
	p, err := cdxDecrypt(key, t)
	if err != nil {
		return nil, fmt.Errorf("CDSL's reply could not be read (%v): %s", err, trimBody(b))
	}
	return cdxParse(p)
}

// CDSLForm is a redirect POST to CDSL: the browser submits these fields to
// the address (the client's TPIN or OTP page).
type CDSLForm struct {
	Action string            `json:"action"`
	Fields map[string]string `json:"fields"`
	Ref    string            `json:"ref"`
	Note   string            `json:"note,omitempty"`
}

// ---- Common upload: delivery instructions by API ----

// CDSLUpload is one transaction upload sent through the API.
type CDSLUpload struct {
	ID           string            `json:"id"`
	Date         string            `json:"date"`
	File         string            `json:"file"`
	FileID       string            `json:"file_id,omitempty"`
	DIS          []string          `json:"dis"`
	Lines        int               `json:"lines"`
	Ack          string            `json:"ack,omitempty"`
	Status       string            `json:"status"` // Sent, Processing, Done, Failed
	CDSLStatus   string            `json:"cdsl_status,omitempty"`
	SentAt       string            `json:"sent_at"`
	SentBy       string            `json:"sent_by"`
	CheckedAt    string            `json:"checked_at,omitempty"`
	Checks       int               `json:"checks"`
	Response     string            `json:"response_file,omitempty"`
	ResponseID   string            `json:"response_file_id,omitempty"`
	Results      map[string]string `json:"results,omitempty"` // DIS ID -> Executed / Failed: reason
	Problem      string            `json:"problem,omitempty"`
	Environment  string            `json:"environment"`
	UnreadResult bool              `json:"unread_result,omitempty"`
}

// cdxState is the sign-in and the last runs.
type cdxState struct {
	Token     string            `json:"token,omitempty"`
	TokenExp  string            `json:"token_exp,omitempty"`
	LastSend  string            `json:"last_send,omitempty"`
	LastPoll  string            `json:"last_poll,omitempty"`
	LastError string            `json:"last_error,omitempty"`
	Done      map[string]string `json:"done,omitempty"` // once-a-day keys
}

func (o *Ops) cdxState(ctx context.Context, tenant string) cdxState {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCDSLTxState, "state", cdxState{})
	if st.Done == nil {
		st.Done = map[string]string{}
	}
	return st
}

func (o *Ops) cdxSaveState(ctx context.Context, tenant string, st cdxState) {
	_ = docs.Upsert(ctx, o.Docs, tenant, cCDSLTxState, "state", st, SystemActor)
}

// cdxAuthBody is the sign-in body, in CDSL's key order.
type cdxAuthBody struct {
	DPID     string `json:"DPID"`
	UserID   string `json:"UserID"`
	Password string `json:"Password"`
}

// cdxLogin signs in to CDAS (AuthWeb APIAuth) and keeps the token for 14
// minutes (CDSL's lasts 15).
func (o *Ops) cdxLogin(ctx context.Context, tenant string, c cdxConn, force bool) (string, error) {
	st := o.cdxState(ctx, tenant)
	now := o.now()
	if exp, err := time.Parse(time.RFC3339, st.TokenExp); err == nil && now.Before(exp) && st.Token != "" && !force {
		return st.Token, nil
	}
	if c.dpID == "" || c.user == "" || c.secret == "" || c.password == "" {
		return "", invalid("the CDSL APIs connection needs the DP ID, user ID, secret key and password for the common upload")
	}
	pw, err := cdxEncrypt(c.secret, []byte(c.password))
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(cdxAuthBody{DPID: c.dpID, UserID: c.user, Password: pw})
	sign, err := cdxAuthSign(c.secret, body)
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/AuthWeb/api/CDASAuthorization/APIAuth", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthSign", sign)
	code, b, err := cdxDo(req)
	if err != nil {
		return "", err
	}
	m, perr := cdxParse(b)
	tok := ""
	if perr == nil {
		tok = m.in("body").str("authtoken")
	}
	if code != 200 || tok == "" {
		msg := fmt.Sprintf("HTTP %d %s", code, trimBody(b))
		if perr == nil && cdxError(m) != "" {
			msg = cdxError(m)
		}
		st.LastError = "CDSL sign-in refused: " + msg
		o.cdxSaveState(ctx, tenant, st)
		return "", fmt.Errorf("%w: CDSL refused the API sign-in: %s", ErrState, msg)
	}
	st.Token, st.TokenExp = tok, now.Add(14*time.Minute).Format(time.RFC3339)
	o.cdxSaveState(ctx, tenant, st)
	return tok, nil
}

// cdxCall posts a signed JSON body to a CommonWeb endpoint, signing in
// again once if the token was refused.
func (o *Ops) cdxCall(ctx context.Context, tenant string, c cdxConn, path string, body []byte, multipartFile []byte, fileName string) (cdxObj, error) {
	try := func(force bool) (int, []byte, error) {
		tok, err := o.cdxLogin(ctx, tenant, c, force)
		if err != nil {
			return 0, nil, err
		}
		sign, err := cdxAuthSign(c.secret, body)
		if err != nil {
			return 0, nil, err
		}
		var req *http.Request
		if multipartFile != nil {
			var fields map[string]string
			_ = json.Unmarshal(body, &fields)
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			for _, k := range []string{"UploadId", "Signature"} {
				_ = mw.WriteField(k, fields[k])
			}
			fw, _ := mw.CreateFormFile("file", fileName)
			_, _ = fw.Write(multipartFile)
			_ = mw.Close()
			req, _ = http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, &buf)
			req.Header.Set("Content-Type", mw.FormDataContentType())
			req.Header.Set("FileComputeHash", cdxSHA256(multipartFile))
		} else {
			req, _ = http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("AuthToken", tok)
		req.Header.Set("AuthSign", sign)
		return cdxDo(req)
	}
	code, b, err := try(false)
	if err == nil && (code == 401 || strings.Contains(strings.ToLower(string(b)), "invalid auth token")) {
		code, b, err = try(true)
	}
	if err != nil {
		return nil, err
	}
	m, perr := cdxParse(b)
	if perr != nil || code != 200 {
		msg := fmt.Sprintf("HTTP %d %s", code, trimBody(b))
		if perr == nil && cdxError(m) != "" {
			msg = cdxError(m)
		}
		return nil, fmt.Errorf("CDSL answered %s", msg)
	}
	m = m.in("body")
	if !m.truth("success") {
		return m, fmt.Errorf("CDSL answered %s", firstOf(cdxError(m), "not successful"))
	}
	return m, nil
}

// cdxReasonCodes are CDSL's off-market trade reason codes for the reasons
// Ledgerline offers. Confirm them against CDSL's current list before going
// live; a wrong code makes CDSL reject the line, never move the shares.
var cdxReasonCodes = map[string]string{
	"Sale (off-market)": "1", "Gift": "2", "Transfer to own account": "3", "Transfer to family member": "4", "Donation": "5",
	"Transmission": "6", "Implementation of government order": "7", "Margin to clearing member": "8", "Others": "9",
}

// cdxDISRows are the transaction upload's lines for verified slips.
func (o *Ops) cdxDISRows(ctx context.Context, tenant string, c cdxConn, ds []DIS, batch string) ([]xfile.Row, error) {
	set, _ := o.Settings(ctx, tenant)
	sender := firstOf(strings.TrimSpace(set.MemberCodes["CDSL-SENDER"]), c.user)
	if sender == "" {
		return nil, invalid("set the CDSL sender ID (member code CDSL-SENDER) in Settings")
	}
	var rows []xfile.Row
	for _, d := range ds {
		bo := strings.TrimSpace(d.BOID)
		if len(bo) != 16 {
			return nil, invalid("%s: the client's BO ID is not a 16-digit CDSL BO ID", d.ID)
		}
		for i, l := range d.Lines {
			v := map[string]string{"BtchId": batch, "SndrId": sender, "BrnchId": "1", "CntrlSctiesDpstryPtcptRole": "DP",
				"LineNb": fmt.Sprint(len(rows) + 1), "SeqNm": fmt.Sprintf("%04d", len(rows)+1), "SrcCntrlSctiesDpstryPtcpt": bo[2:8], "SrcClntIdOrBnfclOwnrId": bo,
				"ISIN": l.ISIN, "Qty": fmt.Sprintf("%d.000", l.Qty), "BuySellInd": "S", "ExecDt": d.ExecDate, "DISTpInd": "NRM", "DISSrlNb": fmt.Sprintf("%012d", d.Slip),
				"ReqRcvdDtFrBO": d.Received, "MkrOprId": d.Maker, "ChckrOprtrId": d.Checker, "IntlRefNb": d.ID, "SndrRefNb1": fmt.Sprintf("%s-%d", d.ID, i+1), "ChanlInd": "API"}
			switch d.Kind {
			case "Inter-depository":
				v["TxnTyp"] = "IDD"
				to := strings.ToUpper(d.ToBO)
				if strings.HasPrefix(to, "IN") && len(to) == 16 {
					v["OthrDpstryPtcpt"], v["OthrDpstryClntCd"], v["OthrDpstryId"] = to[:8], to[8:], "NSDL"
				} else {
					v["CntrBnfclOwnrId"] = to
				}
			default:
				v["TxnTyp"] = "WTR"
				v["CntrBnfclOwnrId"] = d.ToBO
				v["TrdRsnCd"] = cdxReasonCodes[d.Reason]
				if d.Consideration > 0 {
					v["Cnsdrtn"] = d.Consideration.String()
				}
			}
			rows = append(rows, xfile.Row{Values: v})
		}
	}
	return rows, nil
}

func cdxZip(name string, data []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(name)
	_, _ = w.Write(data)
	_ = zw.Close()
	return buf.Bytes()
}

// SendDISByAPI sends the verified slips due by date to CDSL through the
// common upload API and marks them uploaded. Slips are sent only after a
// checker has verified them (VerifyDIS).
func (o *Ops) SendDISByAPI(ctx context.Context, tenant, actor, date string) (CDSLUpload, error) {
	if err := o.require(ctx, tenant, actor, "Uploading delivery instructions", RoleMaker, RoleChecker); err != nil {
		return CDSLUpload{}, err
	}
	c := o.cdxConn(ctx, tenant)
	if !c.on(cdxFamCommon) {
		return CDSLUpload{}, fmt.Errorf("%w: the CDSL common upload API is off: use the upload batch file, or switch the API on in Settings → Connections → CDSL APIs (DP)", ErrState)
	}
	if date == "" {
		date = o.now().In(ist).Format("2006-01-02")
	}
	cdxMu.Lock()
	defer cdxMu.Unlock()
	all, err := docs.List[DIS](ctx, o.Docs, tenant, cDIS, "")
	if err != nil {
		return CDSLUpload{}, err
	}
	var due []DIS
	for _, d := range all {
		if d.Status == "Verified" && d.ExecDate <= date && d.Checker != "" {
			due = append(due, d)
		}
	}
	if len(due) == 0 {
		return CDSLUpload{}, invalid("no verified instructions to send for %s", date)
	}
	lib, err := XFiles()
	if err != nil {
		return CDSLUpload{}, err
	}
	s, ok := lib.Get("cdsl-txn-upload")
	if !ok {
		return CDSLUpload{}, errors.New("the CDSL transaction upload layout is missing")
	}
	n, err := o.next(ctx, tenant, "cdslupload", actor)
	if err != nil {
		return CDSLUpload{}, err
	}
	now := o.now()
	u := CDSLUpload{ID: fmt.Sprintf("CU-%06d", n), Date: date, Status: "Sent", SentAt: now.Format(time.RFC3339), SentBy: actor, Environment: map[bool]string{true: "Live", false: "Mock"}[c.live]}
	rows, err := o.cdxDISRows(ctx, tenant, c, due, strings.ReplaceAll(date, "-", "")+fmt.Sprintf("%04d", n%10000))
	if err != nil {
		return u, err
	}
	content, err := s.Write(rows)
	if err != nil {
		return u, err
	}
	u.File, err = s.FileName(xfile.Vars{"dp_id": strings.TrimSpace(due[0].BOID)[2:8], "date": date, "seq": fmt.Sprint(n)})
	if err != nil {
		return u, err
	}
	zipped := cdxZip(u.File, content)
	if f, err := o.keepFile(ctx, tenant, u.File, content); err == nil {
		u.FileID = f.ID
	}
	for _, d := range due {
		u.DIS = append(u.DIS, d.ID)
	}
	u.Lines = len(rows)
	upID := firstOf(strings.TrimSpace(c.values["upload_id"]), cdxUploadID)
	body, _ := json.Marshal(struct {
		UploadID  string `json:"UploadId"`
		Signature string `json:"Signature"`
	}{upID, ""})
	m, err := o.cdxCall(ctx, tenant, c, "/Commonweb/api/CDASAPI/Upload/v2.0", body, zipped, strings.TrimSuffix(u.File, ".csv")+".zip")
	if err != nil {
		o.log(ctx, tenant, actor, "cdsl.upload_failed", u.ID, map[string]any{"instructions": len(due), "error": err.Error()})
		return u, err
	}
	u.Ack = m.str("acknowledgmentid")
	if u.Ack == "" {
		return u, errors.New("CDSL took the file but gave no acknowledgement number")
	}
	for _, d := range due {
		if _, err := docs.Update(ctx, o.Docs, tenant, cDIS, d.ID, actor, func(x *DIS) error {
			if x.Status != "Verified" {
				return fmt.Errorf("%w: %s changed while sending", ErrState, x.ID)
			}
			x.Status, x.Batch = "Uploaded", "CDSL API "+u.ID+" ack "+u.Ack
			return nil
		}); err != nil {
			return u, err
		}
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cCDSLUpload, u.ID, u, 0, actor); err != nil {
		return u, err
	}
	st := o.cdxState(ctx, tenant)
	st.LastSend, st.LastError = now.Format(time.RFC3339), ""
	o.cdxSaveState(ctx, tenant, st)
	o.log(ctx, tenant, actor, "cdsl.uploaded", u.ID, map[string]any{"ack": u.Ack, "instructions": len(due), "lines": u.Lines, "file": u.File})
	return u, nil
}

// CheckCDSLUpload asks CDSL for an upload's status and, once processed,
// reads the response file and records each instruction's result.
func (o *Ops) CheckCDSLUpload(ctx context.Context, tenant, actor, id string) (CDSLUpload, error) {
	if err := o.require(ctx, tenant, actor, "Recording DP responses", RoleMaker, RoleChecker); err != nil {
		return CDSLUpload{}, err
	}
	u, _, err := docs.Get[CDSLUpload](ctx, o.Docs, tenant, cCDSLUpload, id)
	if err != nil {
		return u, err
	}
	if u.Status == "Done" || u.Status == "Failed" {
		return u, nil
	}
	c := o.cdxConn(ctx, tenant)
	if !c.set() {
		return u, fmt.Errorf("%w: the CDSL APIs connection is not set", ErrState)
	}
	cdxMu.Lock()
	defer cdxMu.Unlock()
	now := o.now()
	u.CheckedAt, u.Checks = now.Format(time.RFC3339), u.Checks+1
	body, _ := json.Marshal(struct {
		Ack       string `json:"AcknowledgmentID"`
		Signature string `json:"Signature"`
	}{u.Ack, ""})
	m, err := o.cdxCall(ctx, tenant, c, "/Commonweb/api/CDASAPI/Status", body, nil, "")
	save := func() { _ = docs.Upsert(ctx, o.Docs, tenant, cCDSLUpload, u.ID, u, actor) }
	if err != nil {
		u.Problem = err.Error()
		save()
		return u, err
	}
	u.CDSLStatus, u.Problem = m.str("status"), ""
	low := strings.ToLower(u.CDSLStatus)
	switch {
	case strings.Contains(low, "fail") || strings.Contains(low, "reject") || strings.Contains(low, "error"):
		u.Status, u.Problem = "Failed", "CDSL did not process the file: "+u.CDSLStatus
		o.cdxRequeue(ctx, tenant, actor, u)
		save()
		o.raiseWork(ctx, tenant, "cdsl", "upload:"+u.ID, "CDSL did not process upload "+u.ID, u.Problem+". The instructions are back to Verified: send them again or use the batch file.", "", RoleMaker, "cdsltxn", "")
		return u, nil
	case !strings.Contains(low, "complete") && !strings.Contains(low, "processed") && !strings.Contains(low, "success"):
		u.Status = "Processing"
		save()
		return u, nil
	}
	body, _ = json.Marshal(struct {
		UploadID  string `json:"UploadID"`
		Name      string `json:"UploadFileName"`
		Signature string `json:"Signature"`
	}{firstOf(strings.TrimSpace(c.values["upload_id"]), cdxUploadID), strings.TrimSuffix(u.File, ".csv"), ""})
	m, err = o.cdxCall(ctx, tenant, c, "/Commonweb/api/CDASAPI/Download", body, nil, "")
	if err != nil {
		u.Problem = err.Error()
		save()
		return u, err
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(m.str("responsefilebuffer")))
	if err != nil || len(data) == 0 {
		u.Problem = "CDSL's response file could not be read"
		save()
		return u, errors.New(u.Problem)
	}
	u.Response = firstOf(m.str("responsefilename"), u.ID+"_response.zip")
	if f, err := o.keepFile(ctx, tenant, u.Response, data); err == nil {
		u.ResponseID = f.ID
	}
	res, unread := cdxReadResponse(u.Response, data, u.DIS)
	u.Results = map[string]string{}
	for _, dis := range u.DIS {
		r, ok := res[dis]
		if !ok {
			unread = true
			continue
		}
		var err error
		if r == "" {
			_, err = o.ExecuteDIS(ctx, tenant, actor, dis, true, "", "")
			u.Results[dis] = "Executed"
		} else {
			_, err = o.ExecuteDIS(ctx, tenant, actor, dis, false, "CDSL rejected: "+r, "")
			u.Results[dis] = "Failed: " + r
		}
		if err != nil {
			u.Results[dis] = "Not recorded: " + err.Error()
			unread = true
		}
	}
	u.Status, u.UnreadResult = "Done", unread
	save()
	if unread {
		o.raiseWork(ctx, tenant, "cdsl", "response:"+u.ID, "Read CDSL's response file for upload "+u.ID, "Some instructions in "+u.Response+" could not be matched. Open it from Depository → Transactions (API) and mark them executed or failed on the DIS tab.", "", RoleMaker, "cdsltxn", "")
	}
	o.log(ctx, tenant, actor, "cdsl.upload_response", u.ID, map[string]any{"results": u.Results, "unread": unread})
	return u, nil
}

// cdxRequeue puts the slips of an upload CDSL did not process back to
// Verified, so they can go again (by API or by file).
func (o *Ops) cdxRequeue(ctx context.Context, tenant, actor string, u CDSLUpload) {
	for _, id := range u.DIS {
		_, _ = docs.Update(ctx, o.Docs, tenant, cDIS, id, actor, func(x *DIS) error {
			if x.Status == "Uploaded" && strings.Contains(x.Batch, u.ID) {
				x.Status, x.Batch = "Verified", ""
			}
			return nil
		})
	}
}

// cdxReadResponse reads CDAS's response file: for each instruction (by the
// internal reference sent), "" for accepted or the rejection reasons. A
// line is rejected when it carries rejection reason codes or a rejected
// status; accepted when it carries CDSL's transaction ID or an accepted
// status. Instructions it cannot place are left out.
func cdxReadResponse(name string, data []byte, ids []string) (map[string]string, bool) {
	out := map[string]string{}
	fail := map[string][]string{}
	okSeen := map[string]bool{}
	files, err := xfile.Unpack(name, data)
	if err != nil {
		files = []xfile.File{{Name: name, Data: data}}
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	unread := false
	for _, f := range files {
		text := strings.ReplaceAll(string(f.Data), "\r\n", "\n")
		delim := ','
		if strings.Count(text, "|") > strings.Count(text, ",") {
			delim = '|'
		} else if strings.Count(text, "~") > strings.Count(text, ",") {
			delim = '~'
		}
		r := csv.NewReader(strings.NewReader(text))
		r.Comma, r.FieldsPerRecord, r.LazyQuotes = delim, -1, true
		recs, _ := r.ReadAll()
		var head []string
		for _, rec := range recs {
			if head == nil {
				for _, x := range rec {
					if strings.EqualFold(strings.TrimSpace(x), "IntlRefNb") || strings.EqualFold(strings.TrimSpace(x), "SndrRefNb1") {
						head = rec
						break
					}
				}
				if head != nil {
					continue
				}
			}
			get := func(k string) string {
				for i, h := range head {
					if strings.EqualFold(strings.TrimSpace(h), k) && i < len(rec) {
						return strings.TrimSpace(rec[i])
					}
				}
				return ""
			}
			id := ""
			if head != nil {
				id = get("IntlRefNb")
				if !want[id] {
					if ref := get("SndrRefNb1"); ref != "" {
						if i := strings.LastIndex(ref, "-"); i > 0 {
							id = ref[:i]
						}
					}
				}
			} else {
				for _, x := range rec {
					if want[strings.TrimSpace(x)] {
						id = strings.TrimSpace(x)
					}
				}
			}
			if !want[id] {
				continue
			}
			var codes []string
			status := ""
			if head != nil {
				for _, k := range []string{"RjctnRsnCd1", "RjctnRsnCd2", "RjctnRsnCd3", "RjctnRsnCd4"} {
					if v := get(k); v != "" {
						codes = append(codes, v)
					}
				}
				status = strings.ToUpper(firstOf(get("TxSts"), firstOf(get("Sts"), firstOf(get("Status"), get("StsChngRsn")))))
			} else {
				for _, x := range rec {
					u := strings.ToUpper(strings.TrimSpace(x))
					if strings.HasPrefix(u, "REJECT") || strings.HasPrefix(u, "ACCEPT") || strings.HasPrefix(u, "SUCCESS") || strings.HasPrefix(u, "FAIL") {
						status = u
					}
				}
			}
			switch {
			case len(codes) > 0 || strings.HasPrefix(status, "REJ") || strings.HasPrefix(status, "FAIL") || status == "R" || status == "F":
				fail[id] = append(fail[id], firstOf(strings.Join(codes, ", "), status))
			case (head != nil && get("TxId") != "") || strings.HasPrefix(status, "ACC") || strings.HasPrefix(status, "SUCC") || strings.HasPrefix(status, "EXEC") || status == "A" || status == "S":
				okSeen[id] = true
			default:
				unread = true
			}
		}
	}
	for id := range want {
		switch {
		case len(fail[id]) > 0:
			out[id] = strings.Join(fail[id], "; ")
		case okSeen[id]:
			out[id] = ""
		}
	}
	return out, unread
}

// ---- DESTAT: mutual fund units from a statement of account into demat ----

// CDSLDestat is one DESTAT request sent to CDSL.
type CDSLDestat struct {
	ReqID     string `json:"reqid"`
	DPReq     string `json:"dp_request"`
	UCC       string `json:"ucc"`
	BOID      string `json:"bo_id"` // masked
	ISIN      string `json:"isin"`
	Folio     string `json:"folio"`
	Qty       string `json:"qty"`
	ISINReqID string `json:"isin_reqid"`
	Status    string `json:"status"` // Awaiting OTP, Accepted, Failed
	DRN       string `json:"drn,omitempty"`
	ResID     string `json:"resid,omitempty"`
	Error     string `json:"error,omitempty"`
	At        string `json:"at"`
	By        string `json:"by"`
	DoneAt    string `json:"done_at,omitempty"`
}

// StartDestat builds the redirect POST that takes a pending demat request
// for mutual fund units (one with a folio) to CDSL's DESTAT page, where the
// investor confirms with the OTP CDSL sends (§5.8).
func (o *Ops) StartDestat(ctx context.Context, tenant, actor, dpReqID, base string) (CDSLForm, error) {
	if err := o.require(ctx, tenant, actor, "DP requests", RoleMaker, RoleChecker, RoleCompliance, RoleBranch); err != nil {
		return CDSLForm{}, err
	}
	c := o.cdxConn(ctx, tenant)
	if !c.on(cdxFamDestat) {
		return CDSLForm{}, fmt.Errorf("%w: the CDSL DESTAT API is off: switch it on in Settings → Connections → CDSL APIs (DP)", ErrState)
	}
	key := c.key(cdxFamDestat)
	if c.dpID == "" || key == "" {
		return CDSLForm{}, invalid("the CDSL APIs connection needs the DP ID and the DESTAT API key")
	}
	r, _, err := docs.Get[DPRequest](ctx, o.Docs, tenant, cDPReq, dpReqID)
	if err != nil {
		return CDSLForm{}, err
	}
	if r.Kind != "Demat" || r.Status != "Pending" {
		return CDSLForm{}, fmt.Errorf("%w: only a pending demat request goes to DESTAT", ErrState)
	}
	folio := strings.TrimSpace(r.Detail["folio"])
	if folio == "" {
		return CDSLForm{}, invalid("DESTAT is for units held in a statement of account: give the folio number on the request")
	}
	cl, err := o.Client(ctx, tenant, r.UCC)
	if err != nil {
		return CDSLForm{}, err
	}
	bo := strings.TrimSpace(o.bo(ctx, tenant, cl).BOID)
	if len(bo) != 16 || strings.Trim(bo, "0123456789") != "" {
		return CDSLForm{}, invalid("%s has no 16-digit CDSL BO ID", r.UCC)
	}
	now := o.now()
	qty, qtype := strings.TrimSpace(r.Detail["qty"]), ""
	if strings.EqualFold(qty, "all") || qty == "" {
		qty, qtype = "", "A"
	}
	pages := firstOf(strings.TrimSpace(r.Detail["pages"]), "0")
	d := CDSLDestat{ReqID: cdxNumericID(now, 24, "20060102150405"), DPReq: r.ID, UCC: r.UCC, BOID: MaskAccount(bo), ISIN: strings.ToUpper(r.Detail["isin"]), Folio: folio, Qty: firstOf(qty, "ALL"),
		Status: "Awaiting OTP", At: now.Format(time.RFC3339), By: actor}
	d.ISINReqID = cdxNumericID(now.Add(time.Second), 24, "20060102150405")
	type isinDtl struct {
		ISINReqID    string `json:"isinreqid"`
		Folio        string `json:"foliono"`
		ISIN         string `json:"isin"`
		QtyType      string `json:"quantitytype"`
		Qty          string `json:"quantity"`
		LockinReason string `json:"lockinreason"`
		LockinExpiry string `json:"lockinexpirydate"`
		ARN          string `json:"arncode"`
	}
	dtl := struct {
		ReqTime   string    `json:"reqtime"`
		BOReqID   string    `json:"boreqid"`
		BOID      string    `json:"boid"`
		DRF       string    `json:"drfno"`
		Pages     string    `json:"noofpages"`
		ReturnURL string    `json:"returnurl"`
		ISINs     []isinDtl `json:"isindtls"`
	}{cdxTime(now), d.ReqID, bo, r.Detail["drf"], pages, strings.TrimRight(base, "/") + "/hooks/" + tenant + "/cdsl/destat/return",
		[]isinDtl{{d.ISINReqID, folio, d.ISIN, qtype, qty, r.Detail["lockin_reason"], r.Detail["lockin_expiry"], r.Detail["arn"]}}}
	plain, _ := json.Marshal(dtl)
	enc, err := cdxEncrypt(key, plain)
	if err != nil {
		return CDSLForm{}, err
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cCDSLDestat, d.ReqID, d, 0, actor); err != nil {
		return CDSLForm{}, err
	}
	o.log(ctx, tenant, actor, "cdsl.destat_started", r.ID, map[string]any{"reqid": d.ReqID, "bo": d.BOID, "isin": d.ISIN})
	return CDSLForm{Action: c.base + "/DestatWebapi/v1.0", Fields: map[string]string{"dpid": c.dpID, "reqid": d.ReqID, "destatdtls": enc}, Ref: d.ReqID,
		Note: "CDSL sends the investor an OTP on the registered mobile and e-mail; the investor confirms there."}, nil
}

// CDSLReturn is what a CDSL redirect back to Ledgerline did, for the page
// the browser lands on.
type CDSLReturn struct {
	Firm    string
	Title   string
	Lines   []string
	OK      bool
	Back    string
	BackFor string
}

// DestatReturn takes CDSL's post-back of a DESTAT request (not signed in:
// it must decrypt under the DP's DESTAT key and match a request sent).
func (o *Ops) DestatReturn(ctx context.Context, tenant string, form url.Values) (CDSLReturn, error) {
	set, _ := o.Settings(ctx, tenant)
	out := CDSLReturn{Firm: set.Firm, Title: "DESTAT request", Back: "/#dp", BackFor: "Back to Depository"}
	c := o.cdxConn(ctx, tenant)
	reqid := strings.TrimSpace(form.Get("reqid"))
	d, _, err := docs.Get[CDSLDestat](ctx, o.Docs, tenant, cCDSLDestat, reqid)
	if err != nil || reqid == "" {
		return out, fmt.Errorf("%w: no DESTAT request %q was sent from here", ErrNotFound, reqid)
	}
	p, err := cdxDecrypt(c.key(cdxFamDestat), form.Get("destatresdtls"))
	if err != nil {
		return out, fmt.Errorf("%w: CDSL's reply could not be verified: %v", ErrInvalid, err)
	}
	m, err := cdxParse(p)
	if err != nil {
		return out, fmt.Errorf("%w: CDSL's reply is not readable", ErrInvalid)
	}
	m = m.in("destatresdtls")
	if got := m.str("reqid"); got != "" && got != reqid {
		return out, fmt.Errorf("%w: CDSL's reply is for another request", ErrInvalid)
	}
	if d.Status != "Awaiting OTP" {
		out.OK, out.Lines = d.Status == "Accepted", []string{"This request was already recorded: " + d.Status + "."}
		return out, nil
	}
	d.ResID, d.DoneAt = m.str("resid"), o.now().Format(time.RFC3339)
	d.Status, d.Error = "Failed", strings.TrimSpace(m.str("reserror")+" "+m.str("reserrmsg"))
	if m.str("resstatus") == "0" {
		for _, x := range m.list("isinresdtls") {
			if x.str("isinreqid") != d.ISINReqID && x.str("isin") != d.ISIN {
				continue
			}
			if x.str("isinreqstatus") == "0" && x.str("drn") != "" {
				d.Status, d.DRN, d.Error = "Accepted", x.str("drn"), ""
				if q := x.str("quantity"); q != "" {
					d.Qty = q
				}
			} else {
				d.Error = firstOf(strings.TrimSpace(x.str("errorcode")+" "+x.str("errormsg")), "CDSL did not accept the ISIN")
			}
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cCDSLDestat, d.ReqID, d, SystemActor); err != nil {
		return out, err
	}
	if d.Status == "Accepted" {
		_, _ = docs.Update(ctx, o.Docs, tenant, cDPReq, d.DPReq, SystemActor, func(x *DPRequest) error {
			if x.Status == "Pending" {
				x.Status = "Sent to RTA"
				x.Detail["drn"], x.Detail["via"] = d.DRN, "CDSL DESTAT"
				if q := strings.TrimSpace(d.Qty); q != "" && q != "ALL" {
					if i := strings.Index(q, "."); i > 0 && strings.Trim(q[i+1:], "0") == "" {
						q = q[:i]
					}
					x.Detail["qty"] = q
				}
				x.Note = "DESTAT request " + d.DRN + " accepted by CDSL; waiting for the RTA"
			}
			return nil
		})
		out.OK, out.Lines = true, []string{fmt.Sprintf("CDSL accepted the request for %s (folio %s). DESTAT request number: %s.", d.ISIN, d.Folio, d.DRN), "The units are credited when the RTA confirms."}
	} else {
		out.Lines = []string{"CDSL did not accept the request: " + firstOf(d.Error, "no reason given") + "."}
		o.raiseWork(ctx, tenant, "cdsl", "destat:"+d.ReqID, "DESTAT not accepted for "+d.UCC, "Request "+d.DPReq+" ("+d.ISIN+"): "+firstOf(d.Error, "no reason given"), d.UCC, RoleMaker, "dp", "")
	}
	o.log(ctx, tenant, SystemActor, "cdsl.destat_result", d.DPReq, map[string]any{"reqid": d.ReqID, "status": d.Status, "drn": d.DRN, "error": d.Error})
	return out, nil
}

// ---- the Transactions (API) screen ----

// CDSLTxnView is the Transactions (API) tab.
type CDSLTxnView struct {
	Connection string            `json:"connection"` // Not set, Off, On
	Families   map[string]bool   `json:"families"`
	Env        string            `json:"environment"`
	DPID       string            `json:"dp_id"`
	LastError  string            `json:"last_error,omitempty"`
	LastSend   string            `json:"last_send,omitempty"`
	ToSend     []DIS             `json:"to_send"`
	Uploads    []CDSLUpload      `json:"uploads"`
	Destat     []CDSLDestat      `json:"destat"`
	DestatOpen []DPRequest       `json:"destat_open"` // pending demat requests with a folio
	Reports    []CDSLReportSetup `json:"reports"`
	Subs       []CDSLReportSub   `json:"report_subscriptions"`
	ReportURL  string            `json:"report_callback,omitempty"`
}

// CDSLTxnScreen shows the transaction uploads, DESTAT and report setups.
func (o *Ops) CDSLTxnScreen(ctx context.Context, tenant, actor string) (CDSLTxnView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing CDSL API transactions", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return CDSLTxnView{}, err
	}
	c := o.cdxConn(ctx, tenant)
	v := CDSLTxnView{Connection: "Not set", Families: map[string]bool{}, ToSend: []DIS{}, Uploads: []CDSLUpload{}, Destat: []CDSLDestat{}, DestatOpen: []DPRequest{}, Reports: []CDSLReportSetup{}, Subs: []CDSLReportSub{}}
	if c.set() {
		v.Connection, v.Env, v.DPID = "On", map[bool]string{true: "Live", false: "Mock"}[c.live], c.dpID
		for _, f := range []string{cdxFamCommon, cdxFamDestat, cdxFamEDIS, cdxFamReport} {
			v.Families[f] = c.on(f)
		}
	}
	st := o.cdxState(ctx, tenant)
	v.LastError, v.LastSend = st.LastError, st.LastSend
	ds, _ := docs.List[DIS](ctx, o.Docs, tenant, cDIS, "")
	for _, d := range ds {
		if d.Status == "Verified" {
			v.ToSend = append(v.ToSend, d)
		}
	}
	us, _ := docs.List[CDSLUpload](ctx, o.Docs, tenant, cCDSLUpload, "")
	sort.Slice(us, func(i, j int) bool { return us[i].ID > us[j].ID })
	if len(us) > 100 {
		us = us[:100]
	}
	v.Uploads = append(v.Uploads, us...)
	dd, _ := docs.List[CDSLDestat](ctx, o.Docs, tenant, cCDSLDestat, "")
	sort.Slice(dd, func(i, j int) bool { return dd[i].At > dd[j].At })
	v.Destat = append(v.Destat, dd...)
	rs, _ := docs.List[DPRequest](ctx, o.Docs, tenant, cDPReq, "")
	for _, r := range rs {
		if r.Kind == "Demat" && r.Status == "Pending" && strings.TrimSpace(r.Detail["folio"]) != "" {
			v.DestatOpen = append(v.DestatOpen, r)
		}
	}
	v.Reports, v.Subs = o.cdslReportLists(ctx, tenant)
	return v, nil
}

// ---- the scheduler ----

// cdslTxnTick, every few minutes on business days:
//   - sends newly verified slips through the common upload (08:00–18:30)
//     and reads back CDSL's processing of sent uploads;
//   - asks CDSL for eDIS authorisations whose redirect never came back,
//     and closes the day's unfinished ones after the day;
//   - sends the day's report setups.
func (o *Ops) cdslTxnTick(ctx context.Context, tenant string, now time.Time) {
	c := o.cdxConn(ctx, tenant)
	if !c.set() {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	day, hm := now.In(ist).Format("2006-01-02"), now.In(ist).Format("15:04")
	business := set.IsBusinessDay(day)
	st := o.cdxState(ctx, tenant)
	if c.on(cdxFamCommon) && business && hm >= "08:00" && hm <= "18:30" {
		if t, err := time.Parse(time.RFC3339, st.LastPoll); err != nil || now.Sub(t) >= 5*time.Minute {
			st.LastPoll = now.Format(time.RFC3339)
			o.cdxSaveState(ctx, tenant, st)
			if _, err := o.SendDISByAPI(ctx, tenant, SystemActor, day); err != nil && !errors.Is(err, ErrInvalid) {
				st = o.cdxState(ctx, tenant)
				st.LastError = err.Error()
				o.cdxSaveState(ctx, tenant, st)
			}
			us, _ := docs.List[CDSLUpload](ctx, o.Docs, tenant, cCDSLUpload, "")
			for _, u := range us {
				if u.Status == "Sent" || u.Status == "Processing" {
					if u.Checks > 200 {
						continue
					}
					_, _ = o.CheckCDSLUpload(ctx, tenant, SystemActor, u.ID)
				}
			}
		}
	}
	if c.on(cdxFamEDIS) {
		o.edisTick(ctx, tenant, now)
	}
	if c.on(cdxFamReport) && business {
		o.cdslReportTick(ctx, tenant, now)
	}
}
