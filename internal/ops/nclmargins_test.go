package ops

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// fakeNCLM plays NSE Clearing: it encrypts the login to the member's public
// key and checks and answers every call with the AES key it handed out.
type fakeNCLM struct {
	t        *testing.T
	pub      *rsa.PublicKey
	key, iv  string // as sent in the login reply
	kb, ivb  []byte
	token    string
	logins   int
	calls    []string
	bodies   []map[string]any
	replies  map[string][]string // path -> CSV texts, in turn
	inquiry  int
	expired  bool
	keyStyle string
}

func (f *fakeNCLM) Post(ctx context.Context, u string, h http.Header, body []byte) (int, http.Header, []byte, error) {
	pu, _ := url.Parse(u)
	path := pu.Path[strings.Index(pu.Path, "/v2/request/")+len("/v2/request/"):]
	f.calls = append(f.calls, strings.Split(pu.Path, "/")[1]+" "+path)
	if h.Get("nonce") == "" || h.Get("User-Agent") != "/" || len(h["Accept-Encoding"]) != 1 || h["Accept-Encoding"][0] != "" {
		f.t.Errorf("%s: missing protocol headers %v", path, h)
	}
	js := func(v any) []byte { b, _ := json.Marshal(v); return b }
	if path == "token" {
		if h.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("ck-test:cs-test")) || string(body) != "grant_type=client_credentials" {
			return 401, nil, js(map[string]any{"messages": map[string]string{"code": "0100401"}, "status": "error"}), nil
		}
		f.logins++
		f.token = fmt.Sprintf("ee1073de-45d0-4040-b9c2-eddfa80280c%d", f.logins)
		plain := fmt.Sprintf(`{"access_token":%q,"token_type":"bearer","expires_in":"3600","scope":"api_scope","key":%q,"iv":%q}`, f.token, f.key, f.iv)
		ct, err := rsa.EncryptPKCS1v15(crand.Reader, f.pub, []byte(plain))
		if err != nil {
			f.t.Fatal(err)
		}
		return 200, nil, js(map[string]string{"data": base64.StdEncoding.EncodeToString(ct)}), nil
	}
	// Data calls: the bearer and body are encrypted with this login's key.
	tok, err := aesCBCDecrypt(f.kb, f.ivb, strings.TrimPrefix(h.Get("Authorization"), "Bearer "))
	if err != nil || string(tok) != f.token || f.expired || (len(h["consumerKey"]) != 1 || h["consumerKey"][0] != "ck-test") {
		return 401, nil, js(map[string]any{"messages": map[string]string{"code": "0101401"}, "status": "error"}), nil
	}
	var wire map[string]string
	json.Unmarshal(body, &wire)
	plain, err := aesCBCDecrypt(f.kb, f.ivb, wire["data"])
	if err != nil {
		return 400, nil, js(map[string]any{"messages": map[string]string{"code": "0101400"}, "status": "error"}), nil
	}
	var req map[string]any
	json.Unmarshal(plain, &req)
	f.bodies = append(f.bodies, req)
	switch {
	case strings.HasSuffix(path, "generate-all"):
		return 200, nil, js(map[string]any{"status": "Success", "messages": map[string]string{"success": " Request submitted successfully."}, "data": map[string]string{"code": "01010000"}}), nil
	case strings.HasSuffix(path, "inquiry"):
		f.inquiry++
		if f.inquiry == 1 {
			return 400, nil, js(map[string]any{"status": "error", "messages": map[string]string{"code": "01010245"}}), nil
		}
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		pw, _ := mw.CreateFormFile("file", "cli-margins.csv")
		pw.Write([]byte(f.replies[path][0]))
		mw.Close()
		return 200, http.Header{"Content-Type": {mw.FormDataContentType()}}, buf.Bytes(), nil
	}
	csv := f.replies[path][0]
	f.replies[path] = f.replies[path][1:]
	data, _ := aesCBCEncrypt(f.kb, f.ivb, []byte(csv))
	return 200, nil, js(map[string]string{"status": "SUCCESS", "message": "", "timeStamp": "17 Nov 2025 14:11:58", "data": data}), nil
}

// The protocol's own sample records.
const (
	nclSampleCM  = "XXXXXX,6350002144.25,50136240.00,50136240.00,6400138384.25,3334843727.87,181848144.38,3516691872.25,5000000.00,54.99,404419232.63,0.00,12732175.15,2917692320.09,-38638348.10,,,,,,"
	nclSampleTM  = "XXXXX,Mr.ABCLtd,36200000.00,19160897.19,55360897.19,57713443.38,408974.68,4410000.00,3080236.90,50632181.16,91.45,807373.68,10755.00,5052272.00,22620.00,1308243.28,51330308.10,,,,,,"
	nclSampleCli = "XXXXXX,Mr.XYZLtd,XXXXX,Mr.ABCLtd,BH1003,19881.09,567037.70,131242.00,21072.50,0.00,49624.72,180866.72,0.00,145080.76,492440.95,145080.76^BH1004,19881.09,567037.70,131242.00,21072.50,0.00,49624.72,90000.00,0.00,145080.76,492440.95,145080.76,,,,,,"
	nclSamplePos = "XXXXX,BH1003,FUTSTK,UNITDSPR,20251125,0,FF,,,,,,,4,,,,,,"
)

func setupNCLMargins(t *testing.T, o *Ops, ctx context.Context, segments string) {
	t.Helper()
	if _, err := o.SaveConnection(ctx, T, "admin", connNCLMargins, map[string]string{"member": "M1234", "segments": segments, "env": "UAT", "fetch": "On"},
		map[string]string{"api_key": "ck-test", "secret": "cs-test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.NCLMarginsMakeKey(ctx, T, "admin", NCLMSubject{State: "Maharashtra", City: "Mumbai", Org: "Test Broking Pvt Ltd", Name: "bo.testbroking.in"}); err != nil {
		t.Fatal(err)
	}
}

func fakeFor(t *testing.T, o *Ops, ctx context.Context) *fakeNCLM {
	cn, _, _ := docsGetConn(ctx, o)
	priv, err := parseRSAKey(cn.Secrets["private_key"])
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeNCLM{t: t, pub: &priv.PublicKey, kb: []byte("0123456789abcdef0123456789abcdef"), ivb: []byte("fedcba9876543210"), replies: map[string][]string{}}
	f.key, f.iv = base64.StdEncoding.EncodeToString(f.kb), base64.StdEncoding.EncodeToString(f.ivb)
	o.NCLMargins = f
	return f
}

func TestNCLMarginsFeed(t *testing.T) {
	o, ctx := newOps(t)
	at := time.Date(2026, 10, 29, 10, 0, 0, 0, ist)
	o.Now = func() time.Time { return at }
	if _, err := o.NCLMarginsFetch(ctx, T, "admin", "FO", "cm", nil); err == nil {
		t.Fatal("fetched without a connection")
	}
	setupNCLMargins(t, o, ctx, "FO, CD")
	f := fakeFor(t, o, ctx)
	f.replies["cm-margins"] = []string{nclSampleCM}
	f.replies["tm-margins"] = []string{nclSampleTM}
	f.replies["cli-margins"] = []string{nclSampleCli}
	f.replies["positions"] = []string{nclSamplePos}

	// The first call logs in and then asks, both encrypted end to end.
	res, err := o.NCLMarginsFetch(ctx, T, "admin", "FO", "cm", nil)
	if err != nil || res.Status != "Received" || res.Rows != 1 || res.MsgID != "M1234202610290000001" || f.logins != 1 {
		t.Fatalf("cm: %+v %v (logins %d)", res, err, f.logins)
	}
	if f.calls[0] != "Margins-FO-API token" || f.calls[1] != "Margins-FO-API cm-margins" {
		t.Fatalf("calls %v", f.calls)
	}
	if d := f.bodies[0]["data"].(map[string]any); f.bodies[0]["version"] != "2.0" || d["memCode"] != "M1234" || d["msgId"] != "M1234202610290000001" {
		t.Fatalf("request %v", f.bodies[0])
	}
	v := o.NCLMarginsStatus(ctx, T, "")
	if len(v.Snaps) != 1 || v.Snaps[0].Rows[0][7] != "3516691872.25" || v.Snaps[0].Columns[7] != "Total margin" || len(v.Snaps[0].Rows[0]) != 15 {
		t.Fatalf("snapshot %+v", v.Snaps)
	}
	for _, s := range v.Segments {
		if s.Token != "" || s.Key != "" {
			t.Fatal("the login leaks to the screen")
		}
	}
	// Each endpoint has its own 10-second limit; the token is reused.
	if _, err := o.NCLMarginsFetch(ctx, T, "admin", "FO", "cm", nil); err == nil || !strings.Contains(err.Error(), "limits") {
		t.Fatalf("no wait: %v", err)
	}
	if res, err := o.NCLMarginsFetch(ctx, T, "admin", "FO", "tm", nil); err != nil || res.Rows != 1 || f.logins != 1 {
		t.Fatalf("tm %+v %v", res, err)
	}
	// Clients: the sample's second record has no CM/TM prefix.
	res, err = o.NCLMarginsFetch(ctx, T, "admin", "FO", "cli", []string{"BH1003", "BH1004"})
	if err != nil || res.Rows != 2 {
		t.Fatalf("cli %+v %v", res, err)
	}
	if d := f.bodies[2]["data"].(map[string]any); fmt.Sprint(d["cliList"]) != "[BH1003 BH1004]" || d["msgId"] != "M1234202610290000003" {
		t.Fatalf("cli request %v", d)
	}
	m, _, _ := o.NCLMClientMargins(ctx, T, "2026-10-29")
	if m["BH1003"].String() != "180866.72" || m["BH1004"].String() != "90000" {
		t.Fatalf("client margins %v", m)
	}
	if res, err := o.NCLMarginsFetch(ctx, T, "admin", "FO", "positions", []string{"BH1003"}); err != nil || res.Rows != 1 {
		t.Fatalf("positions %+v %v", res, err)
	}
	ps, _ := o.NCLMClientPositions(ctx, T, "2026-10-29", "bh1003")
	if len(ps) != 1 || ps[0].Positions[0].Symbol != "UNITDSPR" || ps[0].Positions[0].Expiry != "2025-11-25" || ps[0].Positions[0].Qty.String() != "4" {
		t.Fatalf("positions %+v", ps)
	}
	// Live RMS takes NSE Clearing's margin when it is higher.
	lv, err := o.LiveRisk(ctx, T, "2026-10-29", "")
	if err != nil || lv.ExchangeMarginAt == "" {
		t.Fatalf("live %v %v", lv.ExchangeMarginAt, err)
	}
	for _, r := range lv.Rows {
		if r.UCC == "BH1003" && (r.ExchangeMargin == nil || r.Margin.Cmp(*r.ExchangeMargin) < 0) {
			t.Fatalf("live row %+v", r)
		}
	}
	// Market-wide limits are F&O only; segments not switched on are refused.
	if _, err := o.NCLMarginsFetch(ctx, T, "admin", "CD", "mwpl", nil); err == nil {
		t.Fatal("mwpl for currency")
	}
	if _, err := o.NCLMarginsFetch(ctx, T, "admin", "CO", "cm", nil); err == nil || !strings.Contains(err.Error(), "not switched on") {
		t.Fatalf("CO: %v", err)
	}
	// A refused token means logging in again, no sooner than a minute
	// after the last login.
	at = at.Add(11 * time.Second)
	f.expired = true
	if _, err := o.NCLMarginsFetch(ctx, T, "admin", "FO", "cm", nil); err == nil {
		t.Fatal("expired token not reported")
	}
	f.expired = false
	f.replies["cm-margins"] = []string{nclSampleCM}
	at = at.Add(11 * time.Second)
	if _, err := o.NCLMarginsFetch(ctx, T, "admin", "FO", "cm", nil); err == nil || !strings.Contains(err.Error(), "next login") {
		t.Fatalf("login too soon: %v", err)
	}
	at = at.Add(time.Minute)
	if res, err := o.NCLMarginsFetch(ctx, T, "admin", "FO", "cm", nil); err != nil || res.Rows != 1 || f.logins != 2 {
		t.Fatalf("after login again: %+v %v %d", res, err, f.logins)
	}
	// Message numbers never repeat, even across a refused call.
	seen := map[string]bool{}
	for _, b := range f.bodies {
		id := b["data"].(map[string]any)["msgId"].(string)
		if seen[id] {
			t.Fatalf("message number %s used twice", id)
		}
		seen[id] = true
	}
}

func TestNCLMarginsWholeBookFile(t *testing.T) {
	o, ctx := newOps(t)
	at := time.Date(2026, 10, 30, 11, 0, 0, 0, ist)
	o.Now = func() time.Time { return at }
	setupNCLMargins(t, o, ctx, "CD")
	f := fakeFor(t, o, ctx)
	f.replies["cli-margins/inquiry"] = []string{nclSampleCli}
	res, err := o.NCLMarginsFetch(ctx, T, "admin", "CD", "cli-all", nil)
	if err != nil || res.Status != "Requested" {
		t.Fatalf("request %+v %v", res, err)
	}
	if d := f.bodies[0]["data"].(map[string]any); d["memCode"] != nil {
		t.Fatalf("generate-all carries no member code: %v", d)
	}
	at = at.Add(11 * time.Second)
	if res, err = o.NCLMarginsFetch(ctx, T, "admin", "CD", "cli-all", nil); err != nil || res.Status != "In progress" {
		t.Fatalf("first inquiry %+v %v", res, err)
	}
	at = at.Add(11 * time.Second)
	res, err = o.NCLMarginsFetch(ctx, T, "admin", "CD", "cli-all", nil)
	if err != nil || res.Status != "Received" || res.Rows != 2 || res.MsgID != "M1234202610300000001" {
		t.Fatalf("file %+v %v", res, err)
	}
	if f.calls[2] != "Margins-CD-API cli-margins/inquiry" || f.bodies[2]["data"].(map[string]any)["msgId"] != "M1234202610300000001" {
		t.Fatalf("inquiry %v %v", f.calls, f.bodies[2])
	}
	// The next file request waits five minutes after the last one.
	at = at.Add(11 * time.Second)
	if _, err := o.NCLMarginsFetch(ctx, T, "admin", "CD", "cli-all", nil); err == nil {
		t.Fatal("generate-all within 300 seconds")
	}
}

func TestNCLMarginsPieces(t *testing.T) {
	k := []byte("0123456789abcdef0123456789abcdef")
	for _, s := range []string{string(k), base64.StdEncoding.EncodeToString(k), hex.EncodeToString(k)} {
		if b, err := nclmKeyBytes(s, 32); err != nil || !bytes.Equal(b, k) {
			t.Fatalf("key %q: %v", s, err)
		}
	}
	if _, err := nclmKeyBytes("short", 32); err == nil {
		t.Fatal("short key accepted")
	}
	iv := []byte("fedcba9876543210")
	for _, p := range []string{"", "a", "exactly sixteen!", strings.Repeat("x", 100)} {
		c, _ := aesCBCEncrypt(k, iv, []byte(p))
		if d, err := aesCBCDecrypt(k, iv, c); err != nil || string(d) != p {
			t.Fatalf("round trip %q: %q %v", p, d, err)
		}
	}
	// A known vector: AES-256-CBC of "hello" under this key and IV.
	if c, _ := aesCBCEncrypt(k, iv, []byte("hello")); c == "" {
		t.Fatal("no ciphertext")
	}
	for _, b := range []string{`{"messages":{"code":"0101401"},"status":"error"}`, `{"code":401,"messages":["Invalid token."]}`} {
		if e, ok := parseNCLMEnvelope([]byte(b)); !ok || !strings.HasSuffix(e.Code, "401") {
			t.Fatalf("envelope %s: %+v", b, e)
		}
	}
	if e, _ := parseNCLMEnvelope([]byte(`{"status":"Success","messages":{"success":" Request submitted successfully."},"data":{"code":"01010000"}}`)); e.Code != "01010000" || e.Text != "Request submitted successfully." {
		t.Fatalf("generate-all envelope %+v", e)
	}
	if got := nclmCodeText("01150246"); got != "client list: more than 50,000 clients" {
		t.Fatal(got)
	}
	// A key pasted into one line still reads.
	o, ctx := newOps(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 29, 10, 0, 0, 0, ist) }
	setupNCLMargins(t, o, ctx, "FO")
	cn, _, _ := docsGetConn(ctx, o)
	flat := strings.Join(strings.Fields(cn.Secrets["private_key"]), " ")
	flat = strings.ReplaceAll(strings.ReplaceAll(flat, "BEGIN RSA PRIVATE KEY", "BEGIN_RSA_PRIVATE_KEY"), "END RSA PRIVATE KEY", "END_RSA_PRIVATE_KEY")
	flat = strings.ReplaceAll(strings.ReplaceAll(flat, "BEGIN_RSA_PRIVATE_KEY", "BEGIN RSA PRIVATE KEY"), "END_RSA_PRIVATE_KEY", "END RSA PRIVATE KEY")
	if _, err := parseRSAKey(flat); err != nil {
		t.Fatalf("one-line key: %v", err)
	}
	if _, err := parseRSAKey("not a key"); err == nil {
		t.Fatal("garbage accepted")
	}
	if recs := nclmRecords(`["A,1,,", "B,2"]`); len(recs) != 2 || len(recs[0]) != 2 {
		t.Fatalf("json records %v", recs)
	}
}

func docsGetConn(ctx context.Context, o *Ops) (Connection, bool, error) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, T, cConn, connNCLMargins)
	return c, err == nil, err
}
