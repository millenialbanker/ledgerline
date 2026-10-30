package ops

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// Synthetic module keys, different from the API key, so the tests show
// each module uses its own.
const (
	testEDISKey   = "edisedisedisedisedisedisedisedis"
	testDestatKey = "destatdestatdestatdestatdestat12"
	testReportKey = "reportreportreportreportreport12"
)

// cdxTestConn saves a cdsl-api connection to srvURL for DP 12081600 (the
// seed's CDSL accounts), with module keys and the given families on.
func cdxTestConn(t *testing.T, o *Ops, srvURL string, on ...string) {
	t.Helper()
	vals := map[string]string{"dp_id": "12081600", "env": cdslEnvMock, "user": "APIUSER1", "url": srvURL, "callback_url": "https://bo.example.test",
		"edis_cm_nse": "M1234", "edis_tm": "T1234", "edis_days": "3"}
	for _, f := range on {
		vals[f] = "On"
	}
	c := Connection{Provider: connCDSL, Status: "Saved", Values: vals, Secrets: map[string]string{"password": "Synthetic@1", "secret": testCDSLKey, "api_key": testCDSLKey,
		cdxKeyEDIS: testEDISKey, cdxKeyDestat: testDestatKey, cdxKeyReport: testReportKey}}
	if err := docs.Upsert(t.Context(), o.Docs, T, cConn, connCDSL, c, "admin"); err != nil {
		t.Fatal(err)
	}
}

func cdxTestSeal(t *testing.T, key string, v any) string {
	t.Helper()
	k, _ := cdslAESKey(key, "k")
	iv, _ := cdslIV("")
	b, _ := json.Marshal(v)
	s, err := cdslEncrypt(k, iv, b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func cdxTestOpen(t *testing.T, key, s string) map[string]any {
	t.Helper()
	k, _ := cdslAESKey(key, "k")
	iv, _ := cdslIV("")
	p, err := cdslDecrypt(k, iv, s)
	if err != nil {
		t.Fatalf("decrypt under %s: %v", key[:4], err)
	}
	var m map[string]any
	if err := json.Unmarshal(p, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// cdxTestClient is a seed client with an active CDSL account of DP
// 12081600 and a free holding of at least 20.
func cdxTestClient(t *testing.T, o *Ops, ctx context.Context) (Client, Holding) {
	t.Helper()
	cs, _ := o.ClientMap(ctx, T)
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		b := o.bo(ctx, T, c)
		if b.Status != "Active" || !strings.HasPrefix(b.BOID, "12081600") || !rePAN.MatchString(c.PAN) {
			continue
		}
		hs, _ := o.Holdings(ctx, T, c.UCC)
		for _, h := range hs {
			if o.freeBalance(ctx, T, c.UCC, h.ISIN) >= 20 {
				return c, h
			}
		}
	}
	t.Fatal("no CDSL client with a free holding in the seed")
	return Client{}, Holding{}
}

func TestCDSLConnectionHasModuleFields(t *testing.T) {
	p, ok := connProvider(connCDSL)
	if !ok {
		t.Fatal("no cdsl-api provider")
	}
	keys := map[string]int{}
	for i, f := range p.Fields {
		keys[f.Key] = i
	}
	for _, k := range []string{cdxKeyEDIS, cdxKeyDestat, cdxKeyReport, "upload_id", "edis_cm_nse", "edis_version", "sftp_host"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("field %s missing", k)
		}
	}
	if keys["url"] < keys["sftp_host"] {
		t.Error("the address should stay after the module options")
	}
	o, ctx := newOps(t)
	if _, err := o.SaveConnection(ctx, T, "admin", connCDSL, map[string]string{"dp_id": "12081600", "env": cdslEnvMock, "edis_cm_nse": "M1234", cdslFamEDIS: "On"}, map[string]string{cdxKeyEDIS: testEDISKey}); err != nil {
		t.Fatal(err)
	}
	x, ok := o.cdxConn(ctx, T)
	if !ok || x.DPID != "081600" || x.c.Values["edis_cm_nse"] != "M1234" || x.c.Secrets[cdxKeyEDIS] != testEDISKey || !x.On(cdslFamEDIS) {
		t.Fatalf("saved: %+v", x)
	}
}

// cdxTestDIS enters and verifies an off-market slip.
func cdxTestDIS(t *testing.T, o *Ops, ctx context.Context, c Client, h Holding, qty int64) DIS {
	t.Helper()
	book, err := o.IssueDISBook(ctx, T, "priya.nair", c.UCC, 5)
	if err != nil {
		t.Fatal(err)
	}
	d, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: c.UCC, Slip: book.From, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Gift", ExecDate: D, Received: D, Lines: []DISLine{{ISIN: h.ISIN, Qty: qty}}})
	if err != nil {
		t.Fatal(err)
	}
	if d, err = o.VerifyDIS(ctx, T, "farhan.khan", d.ID, true, "", "Call ref C-1"); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCDSLTransactionUploadByAPI(t *testing.T) {
	o, ctx := setup(t)
	c, h := cdxTestClient(t, o, ctx)
	key, _ := cdslAESKey(testCDSLKey, "k")
	iv, _ := cdslIV("")
	var mu sync.Mutex
	var uploaded []byte
	var upName string
	statusCalls := 0
	accept, reject := "", ""
	srv := useCDSLServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		signed := func(b []byte) bool {
			s, err := cdslDecrypt(key, iv, r.Header.Get("AuthSign"))
			return err == nil && string(s) == string(cdslHash(b, cdslHashHexLower)) && (r.URL.Path == "/"+cdslPathAuth || r.Header.Get("AuthToken") == "TOK1")
		}
		switch r.URL.Path {
		case "/" + cdslPathAuth:
			b, _ := io.ReadAll(r.Body)
			if !signed(b) {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"Body":{"success":true,"AuthToken":"TOK1"}}`))
		case "/" + cdxPathUpload:
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("upload is not multipart: %v", err)
				return
			}
			f, fh, err := r.FormFile("file")
			if err != nil {
				t.Errorf("no file: %v", err)
				return
			}
			uploaded, _ = io.ReadAll(f)
			upName = fh.Filename
			if !signed([]byte(`{"UploadId":"18","Signature":""}`)) || r.FormValue("UploadId") != "18" || r.Header.Get("FileComputeHash") != string(cdslHash(uploaded, cdslHashHexLower)) {
				w.Write([]byte(`{"Body":{"success":false,"UploadResponse":{"ErrorId":"ERR003","ErrorMessage":"Invalid Compute Hash"}}}`))
				return
			}
			w.Write([]byte(`{"Body":{"success":true,"AcknowledgmentID":"ACK77"}}`))
		case "/" + cdslPathStatus:
			b, _ := io.ReadAll(r.Body)
			if !signed(b) || !strings.Contains(string(b), `"AcknowledgmentID":"ACK77"`) {
				w.WriteHeader(401)
				return
			}
			statusCalls++
			if statusCalls == 1 {
				w.Write([]byte(`{"Body":{"success":true,"Status":"In Progress"}}`))
				return
			}
			w.Write([]byte(`{"Body":{"success":true,"Status":"Completed"}}`))
		case "/" + cdslPathDownload:
			b, _ := io.ReadAll(r.Body)
			var q map[string]string
			json.Unmarshal(b, &q)
			if !signed(b) || q["UploadFileName"]+".zip" != upName {
				w.Write([]byte(`{"Body":{"success":false,"DownloadResponseFile":{"ErrorId":"ERR002","ErrorMessage":"Invalid Upload File Name"}}}`))
				return
			}
			resp := "IntlRefNb,SndrRefNb1,TxId,RjctnRsnCd1\n" + accept + "," + accept + "-1,9001,\n" + reject + "," + reject + "-1,,R123\n"
			var zb bytes.Buffer
			zw := zip.NewWriter(&zb)
			fw, _ := zw.Create("resp.csv")
			fw.Write([]byte(resp))
			zw.Close()
			out, _ := json.Marshal(map[string]any{"Body": map[string]any{"success": true, "ResponseFileName": "18RESP.zip", "ResponseFilebuffer": base64.StdEncoding.EncodeToString(zb.Bytes())}})
			w.Write(out)
		default:
			w.WriteHeader(404)
		}
	})
	set, _ := o.Settings(ctx, T)
	set.MemberCodes["CDSL-SENDER"] = "SNDR01"
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	d1 := cdxTestDIS(t, o, ctx, c, h, 5)
	d2 := cdxTestDIS(t, o, ctx, c, h, 4)
	accept, reject = d1.ID, d2.ID
	// Entered but not verified: never sent.
	book, _ := o.IssueDISBook(ctx, T, "priya.nair", c.UCC, 1)
	d3, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: c.UCC, Slip: book.From, Kind: "Off-market", ToBO: "1208160099999998", Reason: "Gift", ExecDate: D, Received: D, Lines: []DISLine{{ISIN: h.ISIN, Qty: 1}}})
	if err != nil {
		t.Fatal(err)
	}

	cdxTestConn(t, o, srv.URL) // the API off
	if _, err := o.SendDISByAPI(ctx, T, "priya.nair", D); !errors.Is(err, ErrState) {
		t.Fatalf("API off: %v", err)
	}
	cdxTestConn(t, o, srv.URL, cdslFamUpload)
	if _, err := o.SendDISByAPI(ctx, T, "vikram.shah", D); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance sent: %v", err)
	}
	u, err := o.SendDISByAPI(ctx, T, "priya.nair", D)
	if err != nil {
		t.Fatal(err)
	}
	if u.Ack != "ACK77" || len(u.DIS) != 2 || u.Status != "Sent" || !strings.HasSuffix(upName, ".zip") {
		t.Fatalf("upload: %+v %s", u, upName)
	}
	zr, err := zip.NewReader(bytes.NewReader(uploaded), int64(len(uploaded)))
	if err != nil || len(zr.File) != 1 {
		t.Fatalf("zip: %v", err)
	}
	rc, _ := zr.File[0].Open()
	csvb, _ := io.ReadAll(rc)
	if !strings.Contains(string(csvb), d1.ID) || strings.Contains(string(csvb), d3.ID) || !strings.Contains(string(csvb), "SNDR01") || !strings.Contains(string(csvb), "WTR") {
		t.Fatalf("file: %s", csvb)
	}
	for _, id := range []string{d1.ID, d2.ID} {
		if x, _, _ := docs.Get[DIS](ctx, o.Docs, T, cDIS, id); x.Status != "Uploaded" || !strings.Contains(x.Batch, "ACK77") {
			t.Fatalf("%s: %+v", id, x)
		}
	}
	if u, err = o.CheckCDSLUpload(ctx, T, "priya.nair", u.ID); err != nil || u.Status != "Processing" {
		t.Fatalf("first check: %+v %v", u, err)
	}
	if u, err = o.CheckCDSLUpload(ctx, T, "priya.nair", u.ID); err != nil || u.Status != "Done" {
		t.Fatalf("second check: %+v %v", u, err)
	}
	if u.Results[d1.ID] != "Executed" || !strings.Contains(u.Results[d2.ID], "R123") || u.UnreadResult {
		t.Fatalf("results: %+v", u.Results)
	}
	if x, _, _ := docs.Get[DIS](ctx, o.Docs, T, cDIS, d1.ID); x.Status != "Executed" {
		t.Fatalf("d1 %s", x.Status)
	}
	if x, _, _ := docs.Get[DIS](ctx, o.Docs, T, cDIS, d2.ID); x.Status != "Failed" || !strings.Contains(x.Note, "R123") {
		t.Fatalf("d2 %+v", x)
	}
	if h2, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID(c.UCC, h.ISIN)); h2.Qty != h.Qty-5 {
		t.Fatalf("holding %d -> %d", h.Qty, h2.Qty)
	}
	v, err := o.CDSLTxnScreen(ctx, T, "anita.rao")
	if err != nil || len(v.Uploads) != 1 || !v.Families[cdslFamUpload] || v.DPID != "081600" {
		t.Fatalf("screen: %+v %v", v, err)
	}
}

func TestCDSLUploadFileFallback(t *testing.T) {
	o, ctx := setup(t)
	c, h := cdxTestClient(t, o, ctx)
	set, _ := o.Settings(ctx, T)
	set.MemberCodes["CDSL-SENDER"] = "SNDR01"
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	d := cdxTestDIS(t, o, ctx, c, h, 3)
	name, b, n, err := o.DISUploadFile(ctx, T, "priya.nair", D)
	if err != nil || n != 1 || !strings.HasPrefix(name, "TXN_UPLD_081600_") || !strings.Contains(string(b), d.ID) {
		t.Fatalf("file %s %d %v", name, n, err)
	}
	if x, _, _ := docs.Get[DIS](ctx, o.Docs, T, cDIS, d.ID); x.Status != "Uploaded" {
		t.Fatal(x.Status)
	}
	// The response is then recorded by hand, as before.
	if _, err := o.ExecuteDIS(ctx, T, "priya.nair", d.ID, true, "", D); err != nil {
		t.Fatal(err)
	}
}

func TestCDSLOnMarketSlip(t *testing.T) {
	o, ctx := setup(t)
	c, h := cdxTestClient(t, o, ctx)
	book, _ := o.IssueDISBook(ctx, T, "priya.nair", c.UCC, 2)
	in := DIS{UCC: c.UCC, Slip: book.From, Kind: "On-market", ToBO: "1100001100099999", Exchange: "NSE", ExecDate: D, Received: D, Lines: []DISLine{{ISIN: h.ISIN, Qty: 2}}}
	bad := in
	bad.Settlement = ""
	if _, err := o.EnterDIS(ctx, T, "priya.nair", bad); err == nil {
		t.Fatal("on-market without settlement")
	}
	in.Settlement = "2026198"
	d, err := o.EnterDIS(ctx, T, "priya.nair", in)
	if err != nil {
		t.Fatal(err)
	}
	x := cdslConn{c: Connection{Values: map[string]string{}}}
	rows, err := o.cdxDISRows(ctx, T, x, []DIS{d}, "B1")
	if err == nil {
		t.Fatal("no sender")
	}
	x.User = "APIUSER1"
	if rows, err = o.cdxDISRows(ctx, T, x, []DIS{d}, "B1"); err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	v := rows[0].Values
	if v["TxnTyp"] != "IDO" || v["SctiesSttlmTxId"] != "2026198" || v["Xchg"] != "12" || v["CntrCMBPId"] != "1100001100099999" || v["TrdRsnCd"] != "" {
		t.Fatalf("row %+v", v)
	}
}

func TestCDSLDestat(t *testing.T) {
	o, ctx := setup(t)
	c, _ := cdxTestClient(t, o, ctx)
	srv := useCDSLServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	cdxTestConn(t, o, srv.URL, cdslFamDestat)
	r, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Demat", UCC: c.UCC, Detail: map[string]string{"isin": "INF209K01YN0", "qty": "125.5", "drf": "DRF778", "folio": "FOLIO12345"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.StartDestat(ctx, T, "priya.nair", r.ID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker sent: %v", err)
	}
	f, err := o.StartDestat(ctx, T, "farhan.khan", r.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Action != srv.URL+"/DestatWebapi/v1.0" || f.Fields["dpid"] != "081600" || len(f.Fields["reqid"]) != 24 {
		t.Fatalf("form %+v", f)
	}
	m := cdxTestOpen(t, testDestatKey, f.Fields["destatdtls"])
	isins, _ := m["isindtls"].([]any)
	if m["boid"] != c.Demat.ID || m["drfno"] != "DRF778" || m["returnurl"] != "https://bo.example.test/hooks/"+T+"/cdsl/destat/return" || len(isins) != 1 || len(m["boreqid"].(string)) != 24 {
		t.Fatalf("destatdtls %+v", m)
	}
	isin := isins[0].(map[string]any)
	if isin["foliono"] != "FOLIO12345" || isin["quantity"] != "125.5" || isin["quantitytype"] != "" {
		t.Fatalf("isin %+v", isin)
	}
	reply := func(key string, reqid string) url.Values {
		return url.Values{"reqid": {f.Fields["reqid"]}, "destatresdtls": {cdxTestSeal(t, key, map[string]any{"reqid": reqid, "resid": "DESREF1", " resstatus": "0", "reserror": "",
			"isinresdtls": []any{map[string]any{"isinreqid": isin["isinreqid"], "Isinreqstatus": "0", "drn": "7894568", "isin": "INF209K01YN0", "quantity": "125.500"}}})}}
	}
	if _, err := o.DestatReturn(ctx, T, reply(testCDSLKey, f.Fields["reqid"])); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong key taken: %v", err)
	}
	if _, err := o.DestatReturn(ctx, T, reply(testDestatKey, "999")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other request taken: %v", err)
	}
	out, err := o.DestatReturn(ctx, T, reply(testDestatKey, f.Fields["reqid"]))
	if err != nil || !out.OK {
		t.Fatalf("return %+v %v", out, err)
	}
	r2, _, _ := docs.Get[DPRequest](ctx, o.Docs, T, cDPReq, r.ID)
	if r2.Status != "Sent to RTA" || r2.Detail["drn"] != "7894568" || r2.Detail["qty"] != "125.500" && r2.Detail["qty"] != "125.5" {
		t.Fatalf("dp request %+v", r2)
	}
	// A second post changes nothing.
	if out, err = o.DestatReturn(ctx, T, reply(testDestatKey, f.Fields["reqid"])); err != nil || !strings.Contains(out.Lines[0], "already") {
		t.Fatalf("again %+v %v", out, err)
	}
}

func TestCDSLReportSetupAndCallback(t *testing.T) {
	o, ctx := newOps(t)
	srv := useCDSLServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]string
		json.Unmarshal(b, &body)
		if r.URL.Path != "/"+cdxPathReport || r.Header.Get("dpid") != "081600" || r.Header.Get("version") != "1.0" || len(r.Header.Get("requestseqno")) != 24 {
			w.WriteHeader(404)
			return
		}
		m := cdxTestOpen(t, testReportKey, body["encryptreqdtl"])
		if m["reportid"] != "DP97" || m["moduleid"] != "8" || !strings.HasPrefix(m["dpcallbackurl"].(string), "https://bo.example.test/hooks/"+T+"/cdsl/report/") {
			t.Errorf("setup %+v", m)
		}
		w.Write([]byte(`"` + cdxTestSeal(t, testReportKey, map[string]any{"setupresdtls": map[string]any{" requestseqno ": r.Header.Get("requestseqno"), "resid": "EPRES1", "resstatus": "0"}}) + `"`))
	})
	if err := checkRptFields("8", "DP97", "01112024_14112024~X"); err == nil {
		t.Error("DP97 type")
	}
	if err := checkRptFields("8", "DPZ6", "1201090000000019~~~~~~"); err != nil {
		t.Error(err)
	}
	if err := checkRptFields("8", "DPZ6", "~A~~~Q~01112024~02112024"); err == nil {
		t.Error("DPZ6 date type")
	}
	cdxTestConn(t, o, srv.URL)
	if _, err := o.SetupCDSLReport(ctx, T, "priya.nair", "8", "DP97", "01112024_14112024~F", ""); !errors.Is(err, ErrState) {
		t.Fatalf("off: %v", err)
	}
	cdxTestConn(t, o, srv.URL, cdslFamReport)
	r, err := o.SetupCDSLReport(ctx, T, "priya.nair", "8", "dp97", "01112024_14112024~F", "")
	if err != nil || r.Status != "Taken" || r.ResID != "EPRES1" {
		t.Fatalf("setup %+v %v", r, err)
	}
	tok := o.cdslRptState(ctx, T).Token
	if _, err := o.CDSLReportCallback(ctx, T, "nope", nil, nil, []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad token: %v", err)
	}
	res, err := o.CDSLReportCallback(ctx, T, tok, url.Values{"requestseqno": {r.SeqNo}}, []CDSLReportFile{{Name: "../DP97_report.txt", Data: []byte("H|x\n")}}, nil)
	if err != nil || !strings.HasPrefix(res, "DP97_report.txt") {
		t.Fatalf("callback %s %v", res, err)
	}
	if got, _, _ := docs.Get[CDSLReportSetup](ctx, o.Docs, T, cCDSLRptSetup, r.SeqNo); got.Status != "Received" {
		t.Fatalf("setup after: %+v", got)
	}
	// Standing setups go once a day at their time.
	if _, err := o.SaveCDSLReportSub(ctx, T, "priya.nair", CDSLReportSub{Module: "8", Report: "DP97", Param: "{prev}_{today}~F", At: "07:30", Active: true}, ""); err != nil {
		t.Fatal(err)
	}
	now := o.now()
	n := func() int { rs, _ := docs.List[CDSLReportSetup](ctx, o.Docs, T, cCDSLRptSetup, ""); return len(rs) }
	before := n()
	at := func(hm string) { tt, _ := timeAt(now, hm); o.cdslReportTick(ctx, T, tt) }
	at("07:00")
	at("08:00")
	at("09:00")
	if n() != before+1 {
		t.Fatalf("subscription ran %d times", n()-before)
	}
}

func TestCDSLReportSFTPFetch(t *testing.T) {
	o, ctx := newOps(t)
	cdxTestConn(t, o, "https://cdsl.example.test", cdslFamReport)
	x, _ := o.cdxConn(ctx, T)
	x.c.Values["sftp_host"], x.c.Values["sftp_user"] = "sftp.example.test:22", "DP081600"
	if err := docs.Upsert(ctx, o.Docs, T, cConn, connCDSL, x.c, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.FetchCDSLSFTP(ctx, T, "priya.nair"); !errors.Is(err, ErrState) {
		t.Fatalf("no client: %v", err)
	}
	if !strings.Contains(o.cdslSFTPState(ctx, T), "no SFTP client") {
		t.Fatal(o.cdslSFTPState(ctx, T))
	}
	calls := 0
	cdslSFTPFetch = func(_ context.Context, cfg CDSLSFTPConfig) ([]CDSLReportFile, error) {
		calls++
		if cfg.Host != "sftp.example.test:22" || cfg.Dir != "/" {
			t.Errorf("cfg %+v", cfg)
		}
		return []CDSLReportFile{{Name: "/out/DPG1_20261016.txt", Data: []byte("01|16102026\n")}}, nil
	}
	t.Cleanup(func() { cdslSFTPFetch = nil })
	got, err := o.FetchCDSLSFTP(ctx, T, "priya.nair")
	if err != nil || len(got) != 1 {
		t.Fatalf("fetch %v %v", got, err)
	}
	if got, _ = o.FetchCDSLSFTP(ctx, T, "priya.nair"); len(got) != 0 || calls != 2 {
		t.Fatalf("fetched again: %v", got)
	}
	_, _, in := o.cdslReportLists(ctx, T)
	if len(in) != 1 || in[0].Via != "SFTP" || in[0].Name != "DPG1_20261016.txt" {
		t.Fatalf("received %+v", in)
	}
}

// timeAt is now's day in IST at hm.
func timeAt(now time.Time, hm string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02 15:04", now.In(ist).Format("2006-01-02")+" "+hm, ist)
}
