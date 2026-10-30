package ops

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// ---- CDSL APIs: transactions (common upload) and DESTAT ----
//
// The DP's one CDSL connection ("cdsl-api", cdslapi.go) carries the DP ID,
// the environment, the API user and its secrets, and a switch per API
// family. This file and cdsledis.go / cdslrptsetup.go add their own options
// to it (init below) and keep their own calls:
//
//   - Common upload (CDSL API Handbook §5.7): delivery instructions a
//     checker has verified (VerifyDIS) go to CDAS as a transaction upload
//     file through the API instead of being uploaded by hand. Ledgerline
//     uploads the zipped file (Upload v2.0, signed with the shared
//     Commonweb sign-in), asks for its status and, once CDAS has processed
//     it, downloads the response file and records each instruction as
//     executed or failed (ExecuteDIS). With the API off, the same file is
//     made for upload in CDAS (DISUploadFile), and the plain DIS batch
//     (DISBatch) stays.
//   - DESTAT (§5.8): mutual fund units held in a statement of account,
//     converted into the demat account. A pending demat request with a
//     folio is sent to CDSL's DESTAT page by a redirect POST from the
//     browser; the investor confirms with the OTP CDSL sends, and CDSL posts
//     the result back with a DESTAT request number (DRN) per ISIN. The
//     request then waits for the RTA like any demat request.
//
// Encryption for the redirect APIs: AES-256-CBC, PKCS#7, a null IV, base64
// (§5.8.7, §5.11.6, §5.21.6), under the key CDSL issued for the module
// (CDSL keeps one key per module; the connection's API key is used when no
// module key is entered).

const (
	cCDSLUpload  = "cdslupload"  // transaction uploads through the API
	cCDSLDestat  = "cdsldestat"  // DESTAT requests, by reqid
	cCDSLTxState = "cdsltxstate" // "state": last runs, once-a-day keys

	// Module keys (secrets of the cdsl-api connection, added below).
	cdxKeyEDIS   = "edis_key"
	cdxKeyDestat = "destat_key"
	cdxKeyReport = "report_key"

	// cdxUploadID is CDAS's upload ID for the transaction module (the
	// handbook's sample); the connection's upload_id replaces it.
	cdxUploadID = "18"

	cdxPathUpload = "Commonweb/api/CDASAPI/Upload/v2.0" // §5.7.3
	cdxPathDestat = "DestatWebapi/v1.0"                 // §5.8.5
)

// cdxFields are this file's, cdsledis.go's and cdslrptsetup.go's options on
// the cdsl-api connection.
var cdxFields = []ConnField{
	{Key: "upload_id", Label: "Common upload: CDAS upload ID for transaction files (CDSL's sample uses 18)", Optional: true, Placeholder: cdxUploadID},
	{Key: "txn_types", Label: "Common upload: transaction type codes for off-market, inter-depository and on-market (comma separated)", Optional: true, Placeholder: "WTR,IDD,IDO"},
	{Key: cdxKeyDestat, Label: "DESTAT API key (if CDSL gave DESTAT its own key)", Secret: true, Optional: true},
	{Key: cdxKeyEDIS, Label: "eDIS API key (if CDSL gave eDIS its own key)", Secret: true, Optional: true},
	{Key: "edis_version", Label: "eDIS version agreed with CDSL", Options: []string{"1.2", "1.1"}, Optional: true},
	{Key: "edis_cm_nse", Label: "eDIS: clearing member ID for NSE (as in CDSL's CM master)", Optional: true},
	{Key: "edis_cm_bse", Label: "eDIS: clearing member ID for BSE (as in CDSL's CM master)", Optional: true},
	{Key: "edis_tm", Label: "eDIS: trading member ID (optional)", Optional: true},
	{Key: "edis_days", Label: "eDIS: days a pre-trade authorisation stays valid", Optional: true, Placeholder: "1"},
	{Key: cdxKeyReport, Label: "Report setup API key (if CDSL gave reports their own key)", Secret: true, Optional: true},
	{Key: "sftp_host", Label: "Report SFTP server (host:port), if CDSL gives one", Optional: true},
	{Key: "sftp_user", Label: "Report SFTP user", Optional: true},
	{Key: "sftp_password", Label: "Report SFTP password", Secret: true, Optional: true},
	{Key: "sftp_dir", Label: "Report SFTP folder", Optional: true, Placeholder: "/outgoing"},
	{Key: "sftp_hostkey", Label: "Report SFTP server's host key fingerprint (SHA256:…)", Optional: true},
}

func init() {
	for i := range ConnProviders {
		p := &ConnProviders[i]
		if p.ID != connCDSL {
			continue
		}
		// Before the address and notes, which close the form.
		at := len(p.Fields)
		for at > 0 && (p.Fields[at-1].Key == "url" || p.Fields[at-1].Key == "notes") {
			at--
		}
		fs := append([]ConnField{}, p.Fields[:at]...)
		fs = append(fs, cdxFields...)
		p.Fields = append(fs, p.Fields[at:]...)
	}
}

// cdxConn reads the cdsl-api connection; ok is false when it is not set.
func (o *Ops) cdxConn(ctx context.Context, tenant string) (cdslConn, bool) {
	x, ok := o.cdslConnFor(ctx, tenant)
	if ok {
		x.DPID = cdxDPID(x.DPID)
	}
	return x, ok
}

// cdxDPID is a CDSL DP ID as the APIs take it: 5 or 6 digits ("10900", not
// "12010900"; handbook FAQ).
func cdxDPID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 8 && strings.HasPrefix(s, "12") {
		s = s[2:]
	}
	return s
}

// cdxBODP is the DP ID part of a CDSL BO ID, without leading zeros.
func cdxBODP(bo string) string {
	bo = strings.TrimSpace(bo)
	if len(bo) != 16 {
		return ""
	}
	return strings.TrimLeft(bo[2:8], "0")
}

// cdxOwnBO checks a BO ID is a 16-digit CDSL account of this DP.
func cdxOwnBO(x cdslConn, bo string) error {
	if len(bo) != 16 || strings.Trim(bo, "0123456789") != "" {
		return invalid("the client has no 16-digit CDSL BO ID")
	}
	if cdxBODP(bo) != strings.TrimLeft(x.DPID, "0") {
		return invalid("BO %s is not an account of DP %s", cdslMaskBO(bo), x.DPID)
	}
	return nil
}

// ---- module keys and sealing ----

// cdxKey is a module's AES key: its own when entered, else the API key.
func cdxKey(x cdslConn, module string) ([]byte, error) {
	if s := strings.TrimSpace(x.c.Secrets[module]); s != "" {
		return cdslAESKey(s, strings.ReplaceAll(module, "_", " "))
	}
	return x.apiKey()
}

// cdxSeal encrypts plain under a module's key.
func cdxSeal(x cdslConn, module string, plain []byte) (string, error) {
	key, err := cdxKey(x, module)
	if err != nil {
		return "", err
	}
	iv, err := x.iv()
	if err != nil {
		return "", err
	}
	return cdslEncrypt(key, iv, plain)
}

// cdxOpen decrypts text from CDSL under a module's key and reads the JSON.
// A form post turns a + into a space; that is undone first.
func cdxOpen(x cdslConn, module, text string) (cdxObj, error) {
	key, err := cdxKey(x, module)
	if err != nil {
		return nil, err
	}
	iv, err := x.iv()
	if err != nil {
		return nil, err
	}
	text = strings.Trim(strings.TrimSpace(text), `"`)
	text = strings.NewReplacer(" ", "+", `\/`, "/").Replace(text)
	p, err := cdslDecrypt(key, iv, text)
	if err != nil {
		return nil, err
	}
	return cdxParse(p)
}

// cdxOpenReply reads an encrypted reply body: the base64 text itself, a
// JSON string holding it, or a JSON object with it in one field. A plain
// JSON object is returned as it is.
func cdxOpenReply(x cdslConn, module string, b []byte) (cdxObj, error) {
	if m, err := cdxParse(b); err == nil {
		for _, v := range m.in("body") {
			if s, ok := v.(string); ok && len(s) >= 24 {
				if p, err := cdxOpen(x, module, s); err == nil {
					return p, nil
				}
			}
		}
		return m, nil
	}
	t := strings.TrimSpace(string(b))
	var s string
	if json.Unmarshal(b, &s) == nil {
		t = s
	}
	m, err := cdxOpen(x, module, t)
	if err != nil {
		return nil, fmt.Errorf("CDSL's reply could not be read (%v): %s", err, trimBody(b))
	}
	return m, nil
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
	d := json.NewDecoder(bytes.NewReader(cdslCleanJSON(b)))
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

// cdxNumericID is an ID of n digits: the time as laid out, then random
// digits.
func cdxNumericID(now time.Time, n int, layout string) string {
	s := now.In(ist).Format(layout)
	for len(s) < n {
		d, _ := rand.Int(rand.Reader, big.NewInt(10))
		s += d.String()
	}
	return s[:n]
}

// cdxSeqID is an ID of n digits unique for the firm: the date (yymmdd, or
// yyyymmddhhmmss for 24 digits) then a sequence.
func (o *Ops) cdxSeqID(ctx context.Context, tenant, seq string, n int) (string, error) {
	k, err := o.next(ctx, tenant, seq, SystemActor)
	if err != nil {
		return "", err
	}
	head := o.now().Format("060102")
	if n >= 24 {
		head = o.now().Format("20060102150405")
	}
	return head + fmt.Sprintf("%0*d", n-len(head), k%pow10(n-len(head))), nil
}

// cdxTime is CDSL's ddmmyyyyhhmmss, IST.
func cdxTime(t time.Time) string { return t.In(ist).Format("02012006150405") }

// cdxQty is a quantity as CDSL's (13,3) fields take it.
func cdxQty(q int64) string { return fmt.Sprintf("%d", q) }

// CDSLForm is a redirect POST to CDSL: the browser submits these fields to
// the address (the client's TPIN, OTP or change-PIN page).
type CDSLForm struct {
	Action string            `json:"action"`
	Fields map[string]string `json:"fields"`
	Ref    string            `json:"ref"`
	Note   string            `json:"note,omitempty"`
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

// cdxState is the transaction module's last runs.
type cdxState struct {
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

// cdxOnce reports whether key is new today, and records it.
func (o *Ops) cdxOnce(ctx context.Context, tenant, key string, now time.Time) bool {
	st := o.cdxState(ctx, tenant)
	if st.Done[key] != "" {
		return false
	}
	day := now.In(ist).Format("2006-01-02")
	st.Done[key] = now.Format(time.RFC3339)
	for k, v := range st.Done { // keep a week
		if v < addDays(day, -7) {
			delete(st.Done, k)
		}
	}
	o.cdxSaveState(ctx, tenant, st)
	return true
}

// ---- Common upload: delivery instructions by API ----

// cdxMu keeps one upload or status check at a time.
var cdxMu sync.Mutex

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

// cdxReasonCodes are CDSL's off-market trade reason codes for the reasons
// Ledgerline offers. Confirm them against CDSL's current list before going
// live: a wrong code makes CDSL reject the line, never move the shares.
var cdxReasonCodes = map[string]string{
	"Sale (off-market)": "1", "Gift": "2", "Transfer to own account": "3", "Transfer to family member": "4", "Donation": "5",
	"Transmission": "6", "Implementation of government order": "7", "Margin to clearing member": "8", "Others": "9",
}

// cdxExchangeIDs are CDSL's exchange IDs (§5.11.7: BSE 11, NSE 12).
var cdxExchangeIDs = map[string]string{"BSE": "11", "NSE": "12"}

// cdxTxnTypes are the upload's transaction type codes by DIS kind, as
// CDSL's transaction files show them; the connection's txn_types replaces
// them.
func cdxTxnTypes(x cdslConn) map[string]string {
	t := map[string]string{"Off-market": "WTR", "Inter-depository": "IDD", "On-market": "IDO"}
	if v := strings.Split(x.c.Values["txn_types"], ","); len(v) == 3 {
		for i, k := range []string{"Off-market", "Inter-depository", "On-market"} {
			if s := strings.ToUpper(strings.TrimSpace(v[i])); s != "" {
				t[k] = s
			}
		}
	}
	return t
}

// cdxDISRows are the transaction upload's lines for verified slips.
func (o *Ops) cdxDISRows(ctx context.Context, tenant string, x cdslConn, ds []DIS, batch string) ([]xfile.Row, error) {
	set, _ := o.Settings(ctx, tenant)
	sender := firstOf(strings.TrimSpace(set.MemberCodes["CDSL-SENDER"]), x.User)
	if sender == "" {
		return nil, invalid("set the CDSL sender ID (member code CDSL-SENDER) in Settings")
	}
	types := cdxTxnTypes(x)
	var rows []xfile.Row
	for _, d := range ds {
		bo := strings.TrimSpace(d.BOID)
		if len(bo) != 16 {
			return nil, invalid("%s: the client's BO ID is not a 16-digit CDSL BO ID", d.ID)
		}
		for i, l := range d.Lines {
			v := map[string]string{"BtchId": batch, "SndrId": sender, "BrnchId": "1", "CntrlSctiesDpstryPtcptRole": "DP", "TxnTyp": types[d.Kind],
				"LineNb": fmt.Sprint(len(rows) + 1), "SeqNm": fmt.Sprintf("%04d", len(rows)+1), "SrcCntrlSctiesDpstryPtcpt": bo[2:8], "SrcClntIdOrBnfclOwnrId": bo,
				"ISIN": l.ISIN, "Qty": fmt.Sprintf("%d.000", l.Qty), "BuySellInd": "S", "ExecDt": d.ExecDate, "DISTpInd": "NRM", "DISSrlNb": fmt.Sprintf("%012d", d.Slip),
				"ReqRcvdDtFrBO": d.Received, "MkrOprId": d.Maker, "ChckrOprtrId": d.Checker, "IntlRefNb": d.ID, "SndrRefNb1": fmt.Sprintf("%s-%d", d.ID, i+1), "ChanlInd": "API"}
			switch d.Kind {
			case "Inter-depository":
				to := strings.ToUpper(d.ToBO)
				if strings.HasPrefix(to, "IN") && len(to) == 16 {
					v["OthrDpstryPtcpt"], v["OthrDpstryClntCd"], v["OthrDpstryId"] = to[:8], to[8:], "NSDL"
				} else {
					v["CntrBnfclOwnrId"] = to
				}
			case "On-market":
				v["CntrCMBPId"], v["SctiesSttlmTxId"], v["Xchg"] = d.ToBO, d.Settlement, cdxExchangeIDs[d.Exchange]
			default:
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

// cdxDue are the verified slips due by date.
func (o *Ops) cdxDue(ctx context.Context, tenant, date string) ([]DIS, error) {
	all, err := docs.List[DIS](ctx, o.Docs, tenant, cDIS, "")
	if err != nil {
		return nil, err
	}
	var due []DIS
	for _, d := range all {
		if d.Status == "Verified" && d.ExecDate <= date && d.Checker != "" {
			due = append(due, d)
		}
	}
	if len(due) == 0 {
		return nil, invalid("no verified instructions to send for %s", date)
	}
	return due, nil
}

// cdxUploadFile writes the transaction upload file for slips.
func (o *Ops) cdxUploadFile(ctx context.Context, tenant string, x cdslConn, due []DIS, date string, n int64) (string, []byte, error) {
	lib, err := XFiles()
	if err != nil {
		return "", nil, err
	}
	s, ok := lib.Get("cdsl-txn-upload")
	if !ok {
		return "", nil, errors.New("the CDSL transaction upload layout is missing")
	}
	rows, err := o.cdxDISRows(ctx, tenant, x, due, strings.ReplaceAll(date, "-", "")+fmt.Sprintf("%04d", n%10000))
	if err != nil {
		return "", nil, err
	}
	content, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(xfile.Vars{"dp_id": strings.TrimSpace(due[0].BOID)[2:8], "date": date, "seq": fmt.Sprint(n)})
	return name, content, err
}

// cdxMarkUploaded moves slips to Uploaded with a batch reference.
func (o *Ops) cdxMarkUploaded(ctx context.Context, tenant, actor string, due []DIS, batch string) error {
	for _, d := range due {
		if _, err := docs.Update(ctx, o.Docs, tenant, cDIS, d.ID, actor, func(x *DIS) error {
			if x.Status != "Verified" {
				return fmt.Errorf("%w: %s changed while sending", ErrState, x.ID)
			}
			x.Status, x.Batch = "Uploaded", batch
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// cdxCommon makes a Commonweb call and checks CDSL's "success".
func cdxCommon(ctx context.Context, tenant string, x cdslConn, path string, body []byte, form url.Values, file *cdslFile) (map[string]any, error) {
	m, err := x.Common(ctx, tenant, path, body, form, file, nil)
	if err != nil {
		return nil, err
	}
	if cdslStr(m, "success") != "true" {
		id, msg := cdslError(m)
		return m, fmt.Errorf("%w: CDSL answered: %s", ErrState, cdslExplain(id, firstOf(msg, "not successful")))
	}
	return m, nil
}

// SendDISByAPI sends the verified slips due by date to CDSL through the
// common upload API (§5.7.6) and marks them uploaded. Only slips a checker
// has verified (VerifyDIS) are sent.
func (o *Ops) SendDISByAPI(ctx context.Context, tenant, actor, date string) (CDSLUpload, error) {
	if err := o.require(ctx, tenant, actor, "Uploading delivery instructions", RoleMaker, RoleChecker); err != nil {
		return CDSLUpload{}, err
	}
	x, ok := o.cdxConn(ctx, tenant)
	if !ok || !x.On(cdslFamUpload) {
		return CDSLUpload{}, fmt.Errorf("%w: the CDSL common upload API is off: use the upload file, or switch the API on in Settings → Connections → CDSL APIs (DP)", ErrState)
	}
	if date == "" {
		date = o.now().Format("2006-01-02")
	}
	cdxMu.Lock()
	defer cdxMu.Unlock()
	due, err := o.cdxDue(ctx, tenant, date)
	if err != nil {
		return CDSLUpload{}, err
	}
	n, err := o.next(ctx, tenant, "cdslupload", actor)
	if err != nil {
		return CDSLUpload{}, err
	}
	now := o.now()
	u := CDSLUpload{ID: fmt.Sprintf("CU-%06d", n), Date: date, Status: "Sent", SentAt: now.Format(time.RFC3339), SentBy: actor, Environment: x.Env}
	name, content, err := o.cdxUploadFile(ctx, tenant, x, due, date, n)
	if err != nil {
		return u, err
	}
	u.File = name
	if f, err := o.keepFile(ctx, tenant, u.File, content); err == nil {
		u.FileID = f.ID
	}
	for _, d := range due {
		u.DIS = append(u.DIS, d.ID)
		u.Lines += len(d.Lines)
	}
	upID := firstOf(strings.TrimSpace(x.c.Values["upload_id"]), cdxUploadID)
	zipName := strings.TrimSuffix(u.File, ".csv") + ".zip"
	// The AuthSign is over the body as JSON; the same fields go in the
	// multipart request with the zipped file (§5.7.6). No digital
	// signature: "Signature" is blank.
	body := cdslJSON("UploadId", upID, "Signature", "")
	m, err := cdxCommon(ctx, tenant, x, cdxPathUpload, body, url.Values{"UploadId": {upID}, "Signature": {""}}, &cdslFile{Field: "file", Name: zipName, Data: cdxZip(u.File, content)})
	if err != nil {
		o.log(ctx, tenant, actor, "cdsl.upload_failed", u.ID, map[string]any{"instructions": len(due), "error": err.Error()})
		st := o.cdxState(ctx, tenant)
		st.LastError = err.Error()
		o.cdxSaveState(ctx, tenant, st)
		return u, err
	}
	u.Ack = cdslStr(m, "AcknowledgmentID", "AcknowledgementID")
	if u.Ack == "" {
		return u, errors.New("CDSL took the file but gave no acknowledgement number")
	}
	if err := o.cdxMarkUploaded(ctx, tenant, actor, due, "CDSL API "+u.ID+" ack "+u.Ack); err != nil {
		return u, err
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

// DISUploadFile is the fallback when the API is off: the same transaction
// upload file, for upload in CDAS by hand. The slips are marked uploaded.
func (o *Ops) DISUploadFile(ctx context.Context, tenant, actor, date string) (string, []byte, int, error) {
	if err := o.require(ctx, tenant, actor, "Uploading delivery instructions", RoleMaker, RoleChecker); err != nil {
		return "", nil, 0, err
	}
	if date == "" {
		date = o.now().Format("2006-01-02")
	}
	x, _ := o.cdxConn(ctx, tenant) // the sender and type codes, when set
	cdxMu.Lock()
	defer cdxMu.Unlock()
	due, err := o.cdxDue(ctx, tenant, date)
	if err != nil {
		return "", nil, 0, err
	}
	n, err := o.next(ctx, tenant, "cdslupload", actor)
	if err != nil {
		return "", nil, 0, err
	}
	name, content, err := o.cdxUploadFile(ctx, tenant, x, due, date, n)
	if err != nil {
		return "", nil, 0, err
	}
	if err := o.cdxMarkUploaded(ctx, tenant, actor, due, "CDAS file "+name); err != nil {
		return "", nil, 0, err
	}
	o.log(ctx, tenant, actor, "dis.uploaded", name, map[string]any{"instructions": len(due), "via": "CDAS upload file"})
	return name, content, len(due), nil
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
	x, ok := o.cdxConn(ctx, tenant)
	if !ok {
		return u, fmt.Errorf("%w: the CDSL APIs connection is not set", ErrState)
	}
	cdxMu.Lock()
	defer cdxMu.Unlock()
	now := o.now()
	u.CheckedAt, u.Checks = now.Format(time.RFC3339), u.Checks+1
	save := func() { _ = docs.Upsert(ctx, o.Docs, tenant, cCDSLUpload, u.ID, u, actor) }
	m, err := cdxCommon(ctx, tenant, x, cdslPathStatus, cdslJSON("AcknowledgmentID", u.Ack, "Signature", ""), nil, nil)
	if err != nil {
		u.Problem = err.Error()
		save()
		return u, err
	}
	u.CDSLStatus, u.Problem = cdslStr(m, "Status"), ""
	low := strings.ToLower(u.CDSLStatus)
	switch {
	case strings.Contains(low, "fail") || strings.Contains(low, "reject") || strings.Contains(low, "error"):
		u.Status, u.Problem = "Failed", "CDSL did not process the file: "+u.CDSLStatus
		o.cdxRequeue(ctx, tenant, actor, u)
		save()
		o.raiseWork(ctx, tenant, "cdsl", "upload:"+u.ID, "CDSL did not process upload "+u.ID, u.Problem+". The instructions are back to Verified: send them again or use the upload file.", "", RoleMaker, "cdsltxn", "")
		return u, nil
	case !strings.Contains(low, "complete") && !strings.Contains(low, "processed") && !strings.Contains(low, "success"):
		u.Status = "Processing"
		save()
		return u, nil
	}
	// The response is asked for by the uploaded file's name without .zip
	// (handbook FAQ).
	upID := firstOf(strings.TrimSpace(x.c.Values["upload_id"]), cdxUploadID)
	m, err = cdxCommon(ctx, tenant, x, cdslPathDownload, cdslJSON("UploadID", upID, "UploadFileName", strings.TrimSuffix(u.File, ".csv"), "Signature", ""), nil, nil)
	if err != nil {
		u.Problem = err.Error()
		save()
		return u, err
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cdslStr(m, "ResponseFilebuffer")))
	if err != nil || len(data) == 0 {
		u.Problem = "CDSL's response file could not be read"
		save()
		return u, errors.New(u.Problem)
	}
	u.Response = firstOf(cdslStr(m, "ResponseFileName"), u.ID+"_response.zip")
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
// status. Instructions it cannot place are left out. CDSL's response
// layout is not in the handbook, so the columns are found by name.
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
	BOReqID   string `json:"boreqid"`
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

// destatErrors are §5.8.8's codes most often seen, in words.
var destatErrors = map[string]string{"ERR002": "the DP is not registered for the DESTAT API", "ERR032": "CDSL has declared its end of day", "ERR034": "CDSL could not decrypt the request (check the DESTAT key)",
	"ERR048": "CDSL has no mobile number or e-mail for the account", "ERR051": "the OTP was wrong", "ERR052": "the OTP expired", "ERR054": "too many OTP attempts", "ERR050": "the session timed out"}

// StartDestat builds the redirect POST that takes a pending demat request
// for mutual fund units (one with a folio) to CDSL's DESTAT page, where the
// investor confirms with the OTP CDSL sends (§5.8.7 A). The request must
// have been checked: DESTAT replaces the physical DRF for units, so a
// checker sends it.
func (o *Ops) StartDestat(ctx context.Context, tenant, actor, dpReqID, base string) (CDSLForm, error) {
	if err := o.require(ctx, tenant, actor, "Sending demat requests to CDSL DESTAT", RoleChecker); err != nil {
		return CDSLForm{}, err
	}
	x, ok := o.cdxConn(ctx, tenant)
	if !ok || !x.On(cdslFamDestat) {
		return CDSLForm{}, fmt.Errorf("%w: the CDSL DESTAT API is off: switch it on in Settings → Connections → CDSL APIs (DP)", ErrState)
	}
	r, _, err := docs.Get[DPRequest](ctx, o.Docs, tenant, cDPReq, dpReqID)
	if err != nil {
		return CDSLForm{}, err
	}
	if r.Kind != "Demat" || r.Status != "Pending" {
		return CDSLForm{}, fmt.Errorf("%w: only a pending demat request goes to DESTAT", ErrState)
	}
	if r.Maker == actor {
		return CDSLForm{}, fmt.Errorf("%w: the person who raised the request cannot send it", ErrForbidden)
	}
	folio := strings.TrimSpace(r.Detail["folio"])
	if folio == "" || len(folio) > 16 {
		return CDSLForm{}, invalid("DESTAT is for units held in a statement of account: give the folio number (up to 16 characters) on the request")
	}
	drf := strings.TrimSpace(r.Detail["drf"])
	if drf == "" || len(drf) > 16 {
		return CDSLForm{}, invalid("give the DRF number (up to 16 characters)")
	}
	cl, err := o.Client(ctx, tenant, r.UCC)
	if err != nil {
		return CDSLForm{}, err
	}
	bo := strings.TrimSpace(o.bo(ctx, tenant, cl).BOID)
	if err := cdxOwnBO(x, bo); err != nil {
		return CDSLForm{}, err
	}
	pub := strings.TrimRight(firstOf(x.Callback, base), "/")
	if pub == "" {
		return CDSLForm{}, invalid("set Ledgerline's public address in the CDSL APIs connection so CDSL can send the result back")
	}
	now := o.now()
	qty, qtype := strings.TrimSpace(r.Detail["qty"]), ""
	if strings.EqualFold(qty, "all") || qty == "" {
		qty, qtype = "", "A" // all units: quantity type A, no quantity
	}
	pages := firstOf(strings.TrimSpace(r.Detail["pages"]), "0")
	d := CDSLDestat{ReqID: cdxNumericID(now, 24, "20060102150405"), BOReqID: cdxNumericID(now, 24, "20060102150405"), DPReq: r.ID, UCC: r.UCC, BOID: cdslMaskBO(bo),
		ISIN: strings.ToUpper(strings.TrimSpace(r.Detail["isin"])), Folio: folio, Qty: firstOf(qty, "ALL"), Status: "Awaiting OTP", At: now.Format(time.RFC3339), By: actor}
	d.ISINReqID = cdxNumericID(now, 24, "20060102150405")
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
	}{cdxTime(now), d.BOReqID, bo, drf, pages, pub + "/hooks/" + tenant + "/cdsl/destat/return",
		[]isinDtl{{d.ISINReqID, folio, d.ISIN, qtype, qty, r.Detail["lockin_reason"], r.Detail["lockin_expiry"], r.Detail["arn"]}}}
	plain, _ := json.Marshal(dtl)
	enc, err := cdxSeal(x, cdxKeyDestat, plain)
	if err != nil {
		return CDSLForm{}, err
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cCDSLDestat, d.ReqID, d, 0, actor); err != nil {
		return CDSLForm{}, err
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cDPReq, r.ID, actor, func(y *DPRequest) error { y.Checker = actor; y.Detail["destat_reqid"] = d.ReqID; return nil })
	o.log(ctx, tenant, actor, "cdsl.destat_started", r.ID, map[string]any{"reqid": d.ReqID, "bo": d.BOID, "isin": d.ISIN})
	return CDSLForm{Action: x.URL(cdxPathDestat), Fields: map[string]string{"dpid": x.DPID, "reqid": d.ReqID, "destatdtls": enc}, Ref: d.ReqID,
		Note: "CDSL sends the investor an OTP on the registered mobile and e-mail; the investor confirms on CDSL's page."}, nil
}

// DestatReturn takes CDSL's post-back of a DESTAT request (§5.8.7 C; not
// signed in: it must decrypt under the DP's DESTAT key and match a request
// sent from here).
func (o *Ops) DestatReturn(ctx context.Context, tenant string, form url.Values) (CDSLReturn, error) {
	set, _ := o.Settings(ctx, tenant)
	out := CDSLReturn{Firm: set.Firm, Title: "DESTAT request", Back: "/#dp", BackFor: "Back to Depository"}
	x, ok := o.cdxConn(ctx, tenant)
	reqid := strings.TrimSpace(form.Get("reqid"))
	d, _, err := docs.Get[CDSLDestat](ctx, o.Docs, tenant, cCDSLDestat, reqid)
	if err != nil || reqid == "" || !ok {
		return out, fmt.Errorf("%w: no DESTAT request %q was sent from here", ErrNotFound, reqid)
	}
	m, err := cdxOpen(x, cdxKeyDestat, form.Get("destatresdtls"))
	if err != nil {
		return out, fmt.Errorf("%w: CDSL's reply could not be verified", ErrInvalid)
	}
	m = m.in("destatresdtls")
	if got := m.str("reqid"); got != reqid {
		return out, fmt.Errorf("%w: CDSL's reply is for another request", ErrInvalid)
	}
	if d.Status != "Awaiting OTP" {
		out.OK, out.Lines = d.Status == "Accepted", []string{"This request was already recorded: " + d.Status + "."}
		return out, nil
	}
	d.ResID, d.DoneAt = m.str("resid"), o.now().Format(time.RFC3339)
	code := m.str("reserror")
	d.Status, d.Error = "Failed", strings.TrimSpace(firstOf(destatErrors[code], m.str("reserrmsg"))+" "+code)
	if m.str("resstatus") == "0" {
		for _, y := range m.list("isinresdtls") {
			if y.str("isinreqid") != d.ISINReqID && y.str("isin") != d.ISIN {
				continue
			}
			if y.str("isinreqstatus") == "0" && y.str("drn") != "" {
				d.Status, d.DRN, d.Error = "Accepted", y.str("drn"), ""
				if q := y.str("quantity"); q != "" {
					d.Qty = q
				}
			} else {
				d.Error = firstOf(strings.TrimSpace(y.str("errorcode")+" "+y.str("errormsg")), "CDSL did not accept the ISIN")
			}
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cCDSLDestat, d.ReqID, d, SystemActor); err != nil {
		return out, err
	}
	if d.Status == "Accepted" {
		_, _ = docs.Update(ctx, o.Docs, tenant, cDPReq, d.DPReq, SystemActor, func(y *DPRequest) error {
			if y.Status == "Pending" {
				y.Status = "Sent to RTA"
				y.Detail["drn"], y.Detail["via"] = d.DRN, "CDSL DESTAT"
				if q := strings.TrimSpace(d.Qty); q != "" && q != "ALL" {
					if i := strings.Index(q, "."); i > 0 && strings.Trim(q[i+1:], "0") == "" {
						q = q[:i]
					}
					y.Detail["qty"] = q
				}
				y.Note = "DESTAT request " + d.DRN + " accepted by CDSL; waiting for the RTA"
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
	Connection string            `json:"connection"` // Not set, Set
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
	Received   []CDSLReportIn    `json:"report_received"`
	SFTP       string            `json:"sftp"`
}

// CDSLTxnScreen shows the transaction uploads, DESTAT and report setups.
func (o *Ops) CDSLTxnScreen(ctx context.Context, tenant, actor string) (CDSLTxnView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing CDSL API transactions", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return CDSLTxnView{}, err
	}
	v := CDSLTxnView{Connection: "Not set", Families: map[string]bool{}, ToSend: []DIS{}, Uploads: []CDSLUpload{}, Destat: []CDSLDestat{}, DestatOpen: []DPRequest{}}
	if x, ok := o.cdxConn(ctx, tenant); ok {
		v.Connection, v.Env, v.DPID = "Set", x.Env, x.DPID
		for _, f := range []string{cdslFamUpload, cdslFamDestat, cdslFamEDIS, cdslFamReport} {
			v.Families[f] = x.On(f)
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
	v.Reports, v.Subs, v.Received = o.cdslReportLists(ctx, tenant)
	v.SFTP = o.cdslSFTPState(ctx, tenant)
	return v, nil
}

// ---- the scheduler ----

// cdslTxnTick, every few minutes:
//   - on business days 08:00–18:30, sends newly verified slips through the
//     common upload and reads back CDSL's processing of sent uploads;
//   - asks CDSL about eDIS authorisations whose redirect never came back,
//     and closes lapsed ones (cdsledis.go);
//   - on business days, sends the day's standing report setups and fetches
//     reports from CDSL's SFTP server (cdslrptsetup.go).
func (o *Ops) cdslTxnTick(ctx context.Context, tenant string, now time.Time) {
	x, ok := o.cdxConn(ctx, tenant)
	if !ok {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	day, hm := now.In(ist).Format("2006-01-02"), now.In(ist).Format("15:04")
	business := set.IsBusinessDay(day)
	if x.On(cdslFamUpload) && business && hm >= "08:00" && hm <= "18:30" {
		st := o.cdxState(ctx, tenant)
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
				if (u.Status == "Sent" || u.Status == "Processing") && u.Checks <= 200 {
					_, _ = o.CheckCDSLUpload(ctx, tenant, SystemActor, u.ID)
				}
			}
		}
	}
	if x.On(cdslFamEDIS) {
		o.edisTick(ctx, tenant, now)
	}
	if x.On(cdslFamReport) && business {
		o.cdslReportTick(ctx, tenant, now)
	}
}
