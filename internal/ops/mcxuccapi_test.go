package ops

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// All made up.
const (
	mcxTestAESKey = "8f1c2b7a4e9d03f6a5b8c1d2e3f40516273849aabbccddeeff00112233445566"
	mcxTestAPIKey = "mcx-api-key-made-up-7731"
)

func TestMCXUCCAPICrypto(t *testing.T) {
	key, err := parseMCXAESKey(mcxTestAESKey)
	if err != nil || len(key) != 32 {
		t.Fatalf("key %v", err)
	}
	if k2, err := parseMCXAESKey(base64.StdEncoding.EncodeToString(key)); err != nil || string(k2) != string(key) {
		t.Fatalf("base64 key %v", err)
	}
	for _, bad := range []string{"", "abc", mcxTestAESKey[:32]} {
		if _, err := parseMCXAESKey(bad); err == nil {
			t.Errorf("key %q taken", bad)
		}
	}
	plain := []byte(`{"client_code":"A123"}`)
	sealed, err := mcxSeal(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	// The layout MCX's Postman script uses: base64(12-byte IV, ciphertext, 16-byte tag).
	raw, err := base64.StdEncoding.DecodeString(string(sealed))
	if err != nil || len(raw) != 12+len(plain)+16 {
		t.Fatalf("sealed %d bytes, %v", len(raw), err)
	}
	blk, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCMWithTagSize(blk, 16)
	if pt, err := g.Open(nil, raw[:12], raw[12:], nil); err != nil || string(pt) != string(plain) {
		t.Fatalf("independent open %q %v", pt, err)
	}
	if pt, err := mcxOpen(key, sealed); err != nil || string(pt) != string(plain) {
		t.Fatalf("open %q %v", pt, err)
	}
	other, _ := hex.DecodeString(strings.Repeat("11", 32))
	if _, err := mcxOpen(other, sealed); err == nil {
		t.Fatal("opened with another key")
	}
	// The token's lifetime from MCX's times, whatever their zone.
	now := time.Date(2026, 10, 30, 11, 0, 0, 0, ist)
	if e := mcxTokenLife("x", "2026-10-30T05:30:00", "2026-10-30T06:00:00", now); !e.Equal(now.Add(29 * time.Minute)) {
		t.Errorf("token life %v", e)
	}
	if e := mcxTokenLife("x", "", "", now); !e.Equal(now.Add(9 * time.Minute)) {
		t.Errorf("default token life %v", e)
	}
}

func TestMCXUCCAPIFields(t *testing.T) {
	// The document's 126 record fields, each once, and every file key a
	// field of the UCC file layout.
	if len(mcxUCCAPIFields) != 126 {
		t.Fatalf("%d fields", len(mcxUCCAPIFields))
	}
	lib, _ := XFiles()
	s, _ := lib.Get("mcx-ucc")
	keys := map[string]bool{}
	for _, col := range s.Records[1].Columns {
		keys[col.Key] = true
	}
	seen := map[string]bool{}
	for _, f := range mcxUCCAPIFields {
		if seen[f[1]] {
			t.Errorf("%s twice", f[1])
		}
		seen[f[1]] = true
		if f[0] != "" && !keys[f[0]] {
			t.Errorf("%s: %s is not a field of the MCX UCC layout", f[1], f[0])
		}
	}
	now := time.Date(2026, 10, 30, 11, 0, 0, 0, ist)
	r := mcxUCCMap(mcxUCCClient("MCX01", "ABCPK1234F", "9811100001", now), mcxTestSources(map[string]UCCDetails{"MCX01": mcxDetails("MCX01")}, true), nil, "2026-10-30")
	m := mcxUCCAPIRecord(r)
	if m["client_code"] != "MCX01" || m["state_city_code_corresponding"] != "1442" || m["bank_account_number_1"] != "50100012345678" || m["flag"] != "E" || m["nomination"] != "" || len(m) != 126 {
		t.Fatalf("record %v", m)
	}
	if p := mcxUCCAPIProblems(r); len(p) != 0 {
		t.Fatalf("problems %v", p)
	}
	r.Values["ContactPerson7Name"] = "Someone"
	if p := mcxUCCAPIProblems(r); len(p) != 1 || !strings.Contains(p[0], "no field in MCX's UCC API") {
		t.Fatalf("contact person 7 %v", p)
	}
	masked := mcxMaskJSON([]byte(`{"records":[{"pan":"ABCPK1234F","bank_account_number_1":"50100012345678","beneficiary_account_id_1":"1208160012345678","uid_number":"123412341234","client_name":"Asha"}]}`))
	for _, s := range []string{"ABCPK1234F", "50100012345678", "1208160012345678", "123412341234"} {
		if strings.Contains(masked, s) {
			t.Errorf("mask kept %s: %s", s, masked)
		}
	}
	if !strings.Contains(masked, "Asha") {
		t.Errorf("mask lost the name: %s", masked)
	}
}

// fakeMCX is MCX's UCC API as the document describes it.
type fakeMCX struct {
	t       *testing.T
	mu      sync.Mutex
	key     []byte
	tokens  map[string]bool
	issued  int
	adds    []map[string]any
	updates []map[string]any
	paths   []string
	held    map[string]map[string]any
	verify  map[string]string
	answer  func(rec map[string]any) (status, code, desc string)
	dropAdd bool // store the add, then answer 500 (an answer lost)
}

func (f *fakeMCX) seal(w http.ResponseWriter, code int, v any) {
	b, _ := json.Marshal(v)
	s, err := mcxSeal(f.key, b)
	if err != nil {
		f.t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/base64")
	w.WriteHeader(code)
	w.Write(s)
}

func (f *fakeMCX) plain(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (f *fakeMCX) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	if r.URL.Path == "/v1/ucc/token" {
		if r.Method != http.MethodPost || r.Header.Get("tm-id") != "56845" || r.Header.Get("api-key") != mcxTestAPIKey {
			f.plain(w, 401, map[string]string{"detail": "Invalid credentials"})
			return
		}
		f.issued++
		tok := fmt.Sprintf("tok-%d", f.issued)
		f.tokens[tok] = true
		f.seal(w, 200, map[string]string{"token": tok, "token_type": "Bearer", "issued_at": "2026-10-30T05:30:00", "expires_at": "2026-10-30T06:30:00"})
		return
	}
	if !f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] {
		f.plain(w, 401, map[string]string{"detail": "Invalid or expired token"})
		return
	}
	if r.URL.Path == "/v1/ucc/health" {
		f.plain(w, 200, map[string]string{"status": "ok"})
		return
	}
	body, _ := io.ReadAll(r.Body)
	if r.Header.Get("Content-Type") != "application/base64" {
		f.plain(w, 415, map[string]string{"detail": "Unsupported media type"})
		return
	}
	pt, err := mcxOpen(f.key, body)
	if err != nil {
		f.plain(w, 400, map[string]string{"detail": "Decryption failed"})
		return
	}
	var req map[string]any
	if err := json.Unmarshal(pt, &req); err != nil {
		f.seal(w, 422, map[string]string{"detail": "Invalid JSON"})
		return
	}
	switch r.URL.Path {
	case "/v1/ucc/add":
		f.adds = append(f.adds, req)
		var out []map[string]any
		for _, x := range req["records"].([]any) {
			rec := x.(map[string]any)
			st, code, desc := f.answer(rec)
			if st != "FAIL" {
				f.held[rec["client_code"].(string)] = rec
			}
			out = append(out, map[string]any{"client_code": rec["client_code"], "error_codes": code, "status": st, "error_description": desc})
		}
		if f.dropAdd {
			f.plain(w, 502, map[string]string{"detail": "Bad gateway"})
			return
		}
		f.seal(w, 200, out)
	case "/v1/ucc/update":
		f.updates = append(f.updates, req)
		var res []map[string]any
		for i, x := range req["records"].([]any) {
			rec := x.(map[string]any)
			u := rec["client_code"].(string)
			old, ok := f.held[u]
			if !ok {
				res = append(res, map[string]any{"index": i, "client_code": u, "status": "FAIL", "error_code": "305", "error_description": "Invalid Client ID"})
				continue
			}
			var changed []string
			for k, v := range rec {
				if old[k] != v {
					changed = append(changed, k)
				}
				old[k] = v
			}
			res = append(res, map[string]any{"index": i, "client_code": u, "status": "SUCCESS", "error_code": nil, "error_description": nil, "updated_fields": changed, "validation_status": "PASSED"})
		}
		f.seal(w, 200, map[string]any{"summary": map[string]any{"total_records": len(res)}, "results": res})
	case "/v1/ucc/search":
		var items []map[string]any
		for _, x := range req["client_code"].([]any) {
			u := x.(string)
			if rec, ok := f.held[u]; ok {
				it := map[string]any{}
				for k, v := range rec {
					it[k] = v
				}
				it["verification_status"] = f.verify[u]
				if f.verify[u] == "rejected" {
					it["pan_response_status"] = "Record (PAN) Not Found in ITD Database/Invalid PAN"
				}
				items = append(items, it)
			}
		}
		f.seal(w, 200, map[string]any{"items": items, "total": len(items), "page": 1, "size": 50, "pages": 1})
	default:
		f.plain(w, 404, map[string]string{"detail": "Not Found"})
	}
}

func mcxAPISetup(t *testing.T) (*Ops, context.Context, *fakeMCX) {
	t.Helper()
	o, ctx := mcxSetup(t)
	if _, err := o.LoadExchangeFile(ctx, T, "priya.nair", "MCX_State_City_Master.csv", []byte("Sr No,State,City,State-City Code\n1,Maharashtra,Pune,1442\n"), true); err != nil {
		t.Fatal(err)
	}
	key, _ := parseMCXAESKey(mcxTestAESKey)
	f := &fakeMCX{t: t, key: key, tokens: map[string]bool{}, held: map[string]map[string]any{}, verify: map[string]string{},
		answer: func(map[string]any) (string, string, string) { return "PASS_PENDING_VERIFICATION", "", "" }}
	srv := httptest.NewTLSServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	old, gap := mcxUCCHTTP, mcxUCCAPIGap
	mcxUCCHTTP, mcxUCCAPIGap = srv.Client(), 0
	t.Cleanup(func() { mcxUCCHTTP, mcxUCCAPIGap = old, gap; mcxUCCTokens.Delete(T) })
	if _, err := o.SaveConnection(ctx, T, "admin", connMCXUCC, map[string]string{"env": "UAT", "send": "On", "url": srv.URL},
		map[string]string{"api_key": mcxTestAPIKey, "aes_key": mcxTestAESKey}); err != nil {
		t.Fatal(err)
	}
	return o, ctx, f
}

func TestMCXUCCAPISend(t *testing.T) {
	o, ctx, f := mcxAPISetup(t)
	f.answer = func(rec map[string]any) (string, string, string) {
		if rec["client_code"] == "MCXB2" {
			return "FAIL", "015", "Client Name is Invalid"
		}
		return "PASS_PENDING_VERIFICATION", "", ""
	}
	// The connection's test: a token and the health check.
	cv, err := o.TestConnection(ctx, T, "admin", connMCXUCC)
	if err != nil || cv.Status != "Working" || !strings.Contains(cv.Problem, "health check is ok") {
		t.Fatalf("test %+v %v", cv, err)
	}
	if _, err := o.MCXUCCAPISend(ctx, T, "anita.rao", nil); err == nil {
		t.Fatal("a risk manager sent UCC")
	}

	// Token, then one add with the two individuals; the company is not sent.
	res, err := o.MCXUCCAPISend(ctx, T, "priya.nair", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 2 || res.Accepted != 1 || res.Rejected != 1 || len(f.adds) != 1 {
		t.Fatalf("send %+v, %d adds", res, len(f.adds))
	}
	add := f.adds[0]
	if add["record_type"] != "10" || add["member_type"] != "M" || add["tmid"] != float64(56845) || add["date"] != "30102026" || add["batch_number"] != "01" || add["record_count"] != "2" {
		t.Fatalf("add header %v", add)
	}
	for _, x := range add["records"].([]any) {
		rec := x.(map[string]any)
		if rec["client_code"] == "MCXC3" || rec["state_city_code_corresponding"] != "1442" || rec["flag"] != "E" || len(rec) != 126 {
			t.Fatalf("add record %v", rec)
		}
	}
	all := strings.Join(res.Problems, " | ")
	if !strings.Contains(all, "MCXC3: ") || !strings.Contains(all, "MCXB2: MCX error 015") {
		t.Fatalf("problems %v", res.Problems)
	}
	a := mcxReg(t, o, ctx, "MCXA1")
	if a.Status != UCCAccepted || a.Via != "API" || a.Action != "Add" || a.AcceptedHash == "" || a.PANStatus != "Pending" || a.Reason != "PAN verification pending at MCX" {
		t.Fatalf("A %+v", a)
	}
	b := mcxReg(t, o, ctx, "MCXB2")
	if b.Status != UCCRejected || !strings.Contains(b.Reason, "015") || !strings.Contains(b.Reason, "client name") || !strings.Contains(b.Reason, "MCX: Client Name is Invalid") {
		t.Fatalf("B %+v", b)
	}
	if c := mcxReg(t, o, ctx, "MCXC3"); c.Status != UCCNeedsFixing {
		t.Fatalf("C %+v", c)
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	found := false
	for _, w := range ws {
		found = found || w.Key == "mcxucc:rej:MCXB2" && w.UCC == "MCXB2" && w.Role == RoleMaker
	}
	if !found {
		t.Fatal("no work item for the rejection")
	}
	// The add took batch 01 of the day's series the file shares.
	if n, _ := o.mcxUCCNextBatch(ctx, T, "2026-10-30"); n != 2 {
		t.Fatalf("next batch %d", n)
	}
	v, err := o.MCXUCCView(ctx, T, "priya.nair")
	if err != nil || v.API != "Working" || v.APILive || !v.APIAuto || len(v.Runs) != 1 || !strings.HasPrefix(v.Runs[0].Note, "sent by API (add)") || v.Runs[0].Accepted != 1 {
		t.Fatalf("view %+v %v", v, err)
	}
	// Nothing again while nothing changed; the named client says so.
	if res, _ := o.MCXUCCAPISend(ctx, T, "priya.nair", []string{"MCXA1"}); res.Sent != 0 || len(f.adds) != 1 || !strings.Contains(res.Problems[0], "nothing to send") {
		t.Fatalf("again %+v", res)
	}

	// A change to the accepted client goes as an update, with the token held.
	if _, err := docs.Update(ctx, o.Docs, T, cClient, "MCXA1", "farhan.khan", func(c *Client) error { c.Email = "asha.new@example.in"; return nil }); err != nil {
		t.Fatal(err)
	}
	res, err = o.MCXUCCAPISend(ctx, T, "priya.nair", []string{"MCXA1"})
	if err != nil || res.Sent != 1 || res.Accepted != 1 || len(f.updates) != 1 || f.issued != 1 {
		t.Fatalf("update %+v %v (%d updates, %d tokens)", res, err, len(f.updates), f.issued)
	}
	up := f.updates[0]
	rec := up["records"].([]any)[0].(map[string]any)
	if up["tmid"] != "56845" || rec["client_code"] != "MCXA1" || rec["pan"] != "ABCPA1111A" || rec["client_email_id"] != "asha.new@example.in" {
		t.Fatalf("update body %v", up)
	}
	if a := mcxReg(t, o, ctx, "MCXA1"); a.Action != "Modify" || a.Status != UCCAccepted || a.AcceptedHash != a.Hash {
		t.Fatalf("A after update %+v", a)
	}

	// The token runs out at MCX: asked again once, and the fix goes.
	f.mu.Lock()
	f.tokens = map[string]bool{}
	f.mu.Unlock()
	if _, err := docs.Update(ctx, o.Docs, T, cClient, "MCXB2", "farhan.khan", func(c *Client) error { c.Name = "Asha R Kulkarni"; return nil }); err != nil {
		t.Fatal(err)
	}
	f.answer = func(map[string]any) (string, string, string) { return "PASS_PENDING_VERIFICATION", "", "" }
	res, err = o.MCXUCCAPISend(ctx, T, SystemActor, nil)
	if err != nil || res.Sent != 1 || res.Accepted != 1 || f.issued != 2 || len(f.adds) != 2 || f.adds[1]["batch_number"] != "02" {
		t.Fatalf("after expiry %+v %v (%d tokens)", res, err, f.issued)
	}
	if ws, _ := o.WorkItems(ctx, T, "Open"); func() bool {
		for _, w := range ws {
			if w.Key == "mcxucc:rej:MCXB2" {
				return true
			}
		}
		return false
	}() {
		t.Fatal("the rejection's work item is still open")
	}

	// An add whose answer is lost stays Sent and is looked up, not sent twice.
	d := mcxDetails("MCXD4")
	nc := mcxUCCClient("MCXD4", "ABCPD4444D", "9811100044", o.now())
	if err := docs.Upsert(ctx, o.Docs, T, cClient, nc.UCC, nc, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	if err := docs.Upsert(ctx, o.Docs, T, cUCCDetails, nc.UCC, d, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	f.dropAdd = true
	res, _ = o.MCXUCCAPISend(ctx, T, "priya.nair", nil)
	if dd := mcxReg(t, o, ctx, "MCXD4"); dd.Status != UCCSent || dd.Via != "API" || len(f.adds) != 3 || !strings.Contains(strings.Join(res.Problems, " "), "Stopped: MCX's server answered with an error (502") {
		t.Fatalf("lost answer %+v %v", dd, res)
	}
	f.dropAdd = false
	res, err = o.MCXUCCAPISend(ctx, T, "priya.nair", nil)
	if err != nil || len(f.adds) != 3 || res.Accepted != 1 {
		t.Fatalf("after a lost answer %+v %v (%d adds)", res, err, len(f.adds))
	}
	if dd := mcxReg(t, o, ctx, "MCXD4"); dd.Status != UCCAccepted || dd.AcceptedHash == "" {
		t.Fatalf("D %+v", dd)
	}

	// Search: MCX verified A's PAN; then reports B's PAN not verified.
	f.verify["MCXA1"], f.verify["MCXB2"] = "success", "rejected"
	res, err = o.MCXUCCAPISync(ctx, T, "vikram.shah", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := mcxReg(t, o, ctx, "MCXA1"); a.PANStatus != "Valid" || a.Status != UCCAccepted || a.Reason != "" {
		t.Fatalf("A after search %+v (%+v)", a, res)
	}
	bb := mcxReg(t, o, ctx, "MCXB2")
	if bb.Status != UCCRejected || !strings.HasPrefix(bb.PANStatus, "Invalid") || !strings.Contains(bb.Reason, "could not verify the PAN") {
		t.Fatalf("B after search %+v", bb)
	}
	found = false
	ws, _ = o.WorkItems(ctx, T, "Open")
	for _, w := range ws {
		found = found || w.Key == "mcxucc:pan:MCXB2"
	}
	if !found {
		t.Fatal("no work item for the PAN MCX could not verify")
	}
	f.verify["MCXB2"] = "success"
	if _, err := o.MCXUCCAPISync(ctx, T, "priya.nair", []string{"MCXB2"}); err != nil {
		t.Fatal(err)
	}
	if bb := mcxReg(t, o, ctx, "MCXB2"); bb.Status != UCCAccepted || bb.PANStatus != "Valid" {
		t.Fatalf("B verified %+v", bb)
	}

	// The company with problems never went in any request.
	for _, req := range append(append([]map[string]any{}, f.adds...), f.updates...) {
		for _, x := range req["records"].([]any) {
			if x.(map[string]any)["client_code"] == "MCXC3" {
				t.Fatal("the client with problems was sent")
			}
		}
	}

	// Secrets and full PANs or account numbers are in no log, audit or batch.
	evs, _ := o.App.Audit.List(ctx, T, 0)
	bs, _ := docs.List[MCXUCCBatchDoc](ctx, o.Docs, T, cMCXUCCBatch, "")
	regs, _ := docs.List[BSEUCCReg](ctx, o.Docs, T, cBSEUCCReg, "MCX|")
	view, _ := o.connView(ctx, T, "admin", connMCXUCC)
	raw, _ := json.Marshal([]any{evs, bs, regs, view, res})
	for _, s := range []string{mcxTestAPIKey, mcxTestAESKey, "tok-1", "tok-2", "ABCPA1111A", "50100012345678"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("%q is in the audit, batches or registrations", s)
		}
	}
	if !strings.Contains(string(raw), `\"client_code\":\"MCXA1\"`) || !strings.Contains(string(raw), "XXXXXXXXXX5678") {
		t.Fatal("the batches lack the masked request")
	}
}

func TestMCXUCCAPIRefused(t *testing.T) {
	o, ctx, f := mcxAPISetup(t)
	// A wrong API key: nothing is sent, the clients stay Not sent, the
	// connection shows the problem and the scheduler leaves it alone.
	if _, err := o.SaveConnection(ctx, T, "admin", connMCXUCC, map[string]string{"env": "UAT", "send": "On", "url": strings.TrimSuffix(o.mcxTestConnURL(t, ctx), "/")},
		map[string]string{"api_key": "wrong-key", "aes_key": mcxTestAESKey}); err != nil {
		t.Fatal(err)
	}
	res, err := o.MCXUCCAPISend(ctx, T, "priya.nair", nil)
	if err != nil || res.Sent != 0 || len(f.adds) != 0 || !strings.Contains(strings.Join(res.Problems, " "), "did not accept the trading member ID or API key") {
		t.Fatalf("refused %+v %v", res, err)
	}
	if r, _, err := docs.Get[BSEUCCReg](ctx, o.Docs, T, cBSEUCCReg, mcxRegKey("MCXA1")); err == nil && r.Status == UCCSent {
		t.Fatalf("A marked sent %+v", r)
	}
	cn, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connMCXUCC)
	if cn.Status != "Problem" {
		t.Fatalf("connection %s", cn.Status)
	}
	o.mcxUCCAPITick(ctx, T, o.now())
	if f.issued != 0 || len(f.paths) != 1 {
		t.Fatalf("the scheduler called MCX: %v", f.paths)
	}
	// Saved again with the right key: the scheduler sends.
	if _, err := o.SaveConnection(ctx, T, "admin", connMCXUCC, map[string]string{"env": "UAT", "send": "On", "url": cn.Values["url"]},
		map[string]string{"api_key": mcxTestAPIKey, "aes_key": mcxTestAESKey}); err != nil {
		t.Fatal(err)
	}
	o.mcxUCCAPITick(ctx, T, o.now())
	if len(f.adds) != 1 {
		t.Fatalf("tick %v", f.paths)
	}
	if r := mcxReg(t, o, ctx, "MCXA1"); r.Status != UCCAccepted {
		t.Fatalf("A after the tick %+v", r)
	}
	// A wrong AES key: MCX's answer does not decrypt.
	mcxUCCTokens.Delete(T)
	if _, err := o.SaveConnection(ctx, T, "admin", connMCXUCC, map[string]string{"env": "UAT", "send": "Off", "url": cn.Values["url"]},
		map[string]string{"api_key": mcxTestAPIKey, "aes_key": strings.Repeat("ab", 32)}); err != nil {
		t.Fatal(err)
	}
	if cv, _ := o.TestConnection(ctx, T, "admin", connMCXUCC); cv.Status != "Problem" || !strings.Contains(cv.Problem, "AES key") {
		t.Fatalf("test with a wrong AES key %+v", cv)
	}
}

func (o *Ops) mcxTestConnURL(t *testing.T, ctx context.Context) string {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, T, cConn, connMCXUCC)
	if err != nil {
		t.Fatal(err)
	}
	return cn.Values["url"]
}
