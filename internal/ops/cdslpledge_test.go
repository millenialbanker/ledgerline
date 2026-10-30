package ops

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// Synthetic data only: sample clients, a fake CDSL gateway (httptest TLS)
// and the synthetic API key of cdslapi_test.go.

func cplTestKey(t *testing.T) ([]byte, []byte) {
	t.Helper()
	k, err := cdslAESKey(testCDSLKey, "k")
	if err != nil {
		t.Fatal(err)
	}
	iv, _ := cdslIV("")
	return k, iv
}

func cplSeal(t *testing.T, v any) string {
	t.Helper()
	k, iv := cplTestKey(t)
	b, _ := json.Marshal(v)
	s, err := cdslEncrypt(k, iv, b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func cplOpenT(t *testing.T, s string, v any) {
	t.Helper()
	k, iv := cplTestKey(t)
	b, err := cdslDecrypt(k, iv, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

// cplSetupTest is the sample firm at 11:00 on the sample day, with the
// CDSL APIs connection pointing at fake and the pledge settings filled.
func cplSetupTest(t *testing.T, fake http.HandlerFunc) (*Ops, func(time.Duration)) {
	t.Helper()
	o, ctx := setup(t)
	now, _ := time.ParseInLocation("2006-01-02 15:04", D+" 11:00", ist)
	o.Now = func() time.Time { return now }
	srv := useCDSLServer(t, fake)
	fakeCDSLConn(t, o, srv.URL, cdslFamMargin, cdslFamPledge, cdslFamEarlyPayin)
	_, err := o.SaveCDSLPledgeSettings(ctx, T, "vikram.shah", CDSLPledgeConfig{PledgeeBOID: "1208160000000011", MTFPledgeeBOID: "1208160000000012", TMID: "90311", CMID: "M90311",
		ExID: "11", SegmentID: "CM", RepledgeeBOID: "1100001100015432", RepledgeCMID: "M90311", EPIBOID: "1100001100099999", EPICMID: "M90311", EPIExID: "11", EPISegment: "CM", DDPIPOAID: "2208160000000001"})
	if err != nil {
		t.Fatal(err)
	}
	return o, func(d time.Duration) { now = now.Add(d) }
}

func cplFreeHolding(t *testing.T, o *Ops, ucc string, min int64) Holding {
	t.Helper()
	hs, _ := o.Holdings(t.Context(), T, ucc)
	for _, h := range hs {
		if _, free := o.cplFree(t.Context(), T, ucc, h.ISIN, ""); free >= min {
			return h
		}
	}
	t.Fatalf("%s has no holding with %d free", ucc, min)
	return Holding{}
}

// cplPledged takes a margin pledge through to CDSL's postback and returns it.
func cplPledged(t *testing.T, o *Ops, ucc string, qty int64) PledgeRequest {
	t.Helper()
	ctx := t.Context()
	user, _, err := o.PortalAccess(ctx, T, "farhan.khan", ucc, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	h := cplFreeHolding(t, o, ucc, qty)
	ps, err := o.RequestMarginPledge(ctx, T, user, ucc, "MP", "", []PledgeLine{{ISIN: h.ISIN, Qty: qty}})
	if err != nil || len(ps) != 1 || ps[0].Status != "Pending" {
		t.Fatalf("request: %+v %v", ps, err)
	}
	if _, err := o.DecidePledge(ctx, T, "farhan.khan", ps[0].ID, true, ""); err != nil {
		t.Fatal(err)
	}
	tok, err := o.CDSLPledgeLink(ctx, T, user, ucc)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := o.CDSLPledgeFormPage(ctx, T, tok, "https://portal.example.test")
	if err != nil {
		t.Fatalf("form page: %v %s", err, pg.Error)
	}
	f := map[string]string{}
	for _, x := range pg.Fields {
		f[x.Name] = x.Value
	}
	var d cplMPDetails
	cplOpenT(t, f["pledgedtls"], &d)
	if len(d.ISINs) != 1 {
		t.Fatalf("isindtls: %+v", d)
	}
	l := d.ISINs[0]
	res := map[string]any{"reqid": f["reqid"], "pledgeidentifier": "MP", "reqstatus": "0", "reserror": "", "reserrmsg": "",
		" pledgeresdtls": []map[string]string{{"isinreqid": l.ISINReqID, "isin": l.ISIN, "quantity": l.Quantity, "psn": "2889" + f["reqid"][len(f["reqid"])-5:], "psnstatus": "S", "reqstatus": "0"}}}
	if _, err := o.CDSLPledgeReturn(ctx, T, "https://portal.example.test", url.Values{"reqid": {f["reqid"]}, "pledgeidentifier": {"MP"}, "pledgeresdtls": {cplSeal(t, res)}}); err != nil {
		t.Fatal(err)
	}
	p, _, _ := docs.Get[PledgeRequest](ctx, o.Docs, T, cPledge, ps[0].ID)
	if p.Status != "Accepted" || p.PSN == "" {
		t.Fatalf("pledge: %+v", p)
	}
	return p
}

func TestCDSLMarginPledge(t *testing.T) {
	var inq struct {
		hdr  http.Header
		body map[string]string
	}
	var answer func(reqid string) any
	o, later := cplSetupTest(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/PledgeServices/MPInquiry" {
			w.WriteHeader(404)
			return
		}
		inq.hdr = r.Header.Clone()
		var env map[string]string
		json.Unmarshal(b, &env)
		inq.body = map[string]string{}
		cplOpenT(t, env["encryptreqdtl"], &inq.body)
		json.NewEncoder(w).Encode(map[string]string{"encryptresdtl": cplSeal(t, answer(inq.body["reqid"]))})
	})
	ctx := t.Context()
	const ucc = "BH1002"
	before := cplFreeHolding(t, o, ucc, 10)

	// The client asks, a checker approves, the client confirms at CDSL.
	p := cplPledged(t, o, ucc, 10)
	h, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID(ucc, p.ISIN))
	if p.ISIN == before.ISIN && h.Pledged != before.Pledged+10 {
		t.Fatalf("pledged %d -> %d", before.Pledged, h.Pledged)
	}
	if p.Expiry != addDays(D, 365) {
		t.Errorf("expiry %s", p.Expiry)
	}

	// The hand-over: maker cannot approve their own; a used link fails.
	user, _, _ := o.PortalAccess(ctx, T, "farhan.khan", ucc, nil, false)
	h2 := cplFreeHolding(t, o, ucc, 5)
	ps, err := o.RequestMarginPledge(ctx, T, "priya.nair", ucc, "MP", "", []PledgeLine{{ISIN: h2.ISIN, Qty: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecidePledge(ctx, T, "priya.nair", ps[0].ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker approved: %v", err)
	}
	if _, err := o.DecidePledge(ctx, T, "farhan.khan", ps[0].ID, true, ""); err != nil {
		t.Fatal(err)
	}
	tok, err := o.CDSLPledgeLink(ctx, T, user, ucc)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := o.CDSLPledgeFormPage(ctx, T, tok, "https://portal.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(pg.Action, "/PledgeServices/MPSetup") || len(pg.Fields) != 4 || pg.Fields[0].Value != "10900" || pg.Fields[2].Value != "2.0" {
		t.Fatalf("form: %+v", pg)
	}
	reqid := pg.Fields[1].Value
	var d cplMPDetails
	cplOpenT(t, pg.Fields[3].Value, &d)
	if d.PledgeIdentifier != "MP" || d.PledgorBOID != "1208160082825519" || d.PledgeeBOID != "1208160000000011" || d.ReturnURL != "https://bo.example.test/hooks/demo/cdsl/pledge-return" ||
		d.ReqTime != "16102026110000" || d.ExecutionDate != "16102026" || d.ISINs[0].Quantity != "5" || d.ISINs[0].SegmentID != "CM" || len(reqid) != 24 {
		t.Fatalf("pledgedtls: %+v", d)
	}
	if _, err := o.CDSLPledgeFormPage(ctx, T, tok, "https://portal.example.test"); err == nil {
		t.Fatal("a link works once")
	}

	// A reply under another key, or for another request, is refused.
	other, _ := cdslAESKey("ffffffffffffffffffffffffffffffff", "k")
	iv, _ := cdslIV("")
	forged, _ := cdslEncrypt(other, iv, []byte(`{"reqid":"`+reqid+`","reqstatus":"0"}`))
	if _, err := o.CDSLPledgeReturn(ctx, T, "", url.Values{"reqid": {reqid}, "pledgeresdtls": {forged}}); err == nil {
		t.Fatal("forged reply accepted")
	}
	if _, err := o.CDSLPledgeReturn(ctx, T, "", url.Values{"reqid": {"LL999"}, "pledgeresdtls": {cplSeal(t, map[string]string{"reqstatus": "0"})}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown reqid: %v", err)
	}

	// No postback came: the tick asks the inquiry API. First CDSL has
	// nothing yet (ERR221): the request keeps waiting.
	answer = func(string) any {
		return map[string]any{"resstatus": "1", "reqerrorcode": "ERR221", "reqerrormsg": "No Data Found For Request Sequence Number"}
	}
	later(4 * time.Minute)
	o.cdslPledgeTick(ctx, T, o.now())
	if inq.hdr.Get("dpid") != "10900" || inq.hdr.Get("version") != "2.0" || len(inq.hdr.Get("requestseqno")) != 24 || inq.body["reqid"] != reqid || len(inq.body["reqdatetime"]) != 14 {
		t.Fatalf("inquiry: %v %v", inq.hdr, inq.body)
	}
	x, _, _ := docs.Get[PledgeRequest](ctx, o.Docs, T, cPledge, ps[0].ID)
	if x.Status != "Awaiting client" || x.ReqID != reqid {
		t.Fatalf("after ERR221: %+v", x)
	}
	// Then the client's OTP went through.
	answer = func(r string) any {
		return map[string]any{"reqid": r, "resstatus": "0", "isinresdtls": []map[string]string{{"isinreqid": d.ISINs[0].ISINReqID, "psn": "288900077", "psnstatus": "S", "reqstatus": "0"}}}
	}
	later(6 * time.Minute)
	o.cdslPledgeTick(ctx, T, o.now())
	x, _, _ = docs.Get[PledgeRequest](ctx, o.Docs, T, cPledge, ps[0].ID)
	if x.Status != "Accepted" || x.PSN != "288900077" {
		t.Fatalf("after inquiry: %+v", x)
	}
	v, err := o.CDSLPledges(ctx, T, D)
	if err != nil || !v.Connected || len(v.Pledges) != 2 || len(v.Calls) != 2 {
		t.Fatalf("screen: %+v %v", v, err)
	}
	pv, err := o.PortalPledges(ctx, T, user, ucc)
	if err != nil || len(pv.Requests) != 2 || len(pv.PSNs) != 2 || pv.Requests[0].RequestedBy != "" {
		t.Fatalf("portal: %+v %v", pv, err)
	}
}

func TestCDSLPledgeSwitchedOff(t *testing.T) {
	o, _ := cplSetupTest(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	ctx := t.Context()
	fakeCDSLConn(t, o, "https://127.0.0.1:1", cdslFamEDIS)
	if _, err := o.cplConnection(ctx, T, "MP"); !errors.Is(err, errCPLNotSet) || !strings.Contains(err.Error(), "Margin pledge") {
		t.Fatalf("off: %v", err)
	}
	docs.Upsert(ctx, o.Docs, T, cConn, connCDSL, Connection{Provider: connCDSL, Status: "Not set"}, "admin")
	if _, err := o.cplConnection(ctx, T, "EPI"); !errors.Is(err, errCPLNotSet) {
		t.Fatalf("missing: %v", err)
	}
}

func TestCDSLUnpledgeAndInvocation(t *testing.T) {
	var sent []cplSetup
	o, later := cplSetupTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/PledgeServices/UnpledgeSetup" && r.URL.Path != "/PledgeServices/ConfiscateSetup" {
			w.WriteHeader(404)
			return
		}
		r.ParseForm()
		var d cplSetup
		cplOpenT(t, r.PostForm.Get("pledgedtls"), &d)
		sent = append(sent, d)
		if len(r.PostForm.Get("reqid")) > 16 || r.PostForm.Get("dpid") != "10900" {
			t.Errorf("form: %v", r.PostForm)
		}
		l := d.ISINs[0]
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		w.Write([]byte(url.Values{"reqid": {r.PostForm.Get("reqid")}, "pledgeresdtls": {cplSeal(t, map[string]any{"reqid": r.PostForm.Get("reqid"), "pledgeidentifier": d.PledgeIdentifier, "reqstatus": "0",
			"isindtls": []map[string]string{{"isinreqid": l.ISINReqID, "psn": l.PSN, "psnstatus": "A", "partQuantity": l.PartQuantity, "reqstatus": "0"}}})}}.Encode()))
	})
	ctx := t.Context()
	const ucc = "BH1002"
	p := cplPledged(t, o, ucc, 20)
	user, _, _ := o.PortalAccess(ctx, T, "farhan.khan", ucc, nil, false)
	h0, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID(ucc, p.ISIN))

	// The client asks to release 8; a checker sends it.
	if _, err := o.RequestUnpledge(ctx, T, user, ucc, p.PSN, 21, ""); err == nil {
		t.Fatal("more than pledged")
	}
	u, err := o.RequestUnpledge(ctx, T, user, ucc, p.PSN, 8, "")
	if err != nil || u.Source != "Client" {
		t.Fatalf("unpledge: %+v %v", u, err)
	}
	if _, err := o.DecideUnpledge(ctx, T, user, u.ID, true, ""); err == nil {
		t.Fatal("the client cannot approve")
	}
	u, err = o.DecideUnpledge(ctx, T, "farhan.khan", u.ID, true, "")
	if err != nil || u.Status != "Done" {
		t.Fatalf("decide: %+v %v", u, err)
	}
	if len(sent) != 1 || sent[0].PledgeIdentifier != "UN" || sent[0].Type != "pledgee" || sent[0].ISINs[0].PSN != p.PSN || sent[0].ISINs[0].PartQuantity != "8.000" {
		t.Fatalf("sent: %+v", sent)
	}
	h1, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID(ucc, p.ISIN))
	if h1.Pledged != h0.Pledged-8 || h1.Qty != h0.Qty {
		t.Fatalf("holding after unpledge %+v -> %+v", h0, h1)
	}
	a, _ := o.cplPSN(ctx, T, p.PSN)
	if a.Free != 12 {
		t.Fatalf("psn: %+v", a)
	}

	// Invocation: risk raises, compliance approves (notice), CDSL after the notice.
	_ = o.InvocationCandidates(ctx, T, D)
	put := func(id, src string) PledgeRelease {
		r := PledgeRelease{ID: id, Kind: "CF", UCC: ucc, Of: "MP", PSN: p.PSN, ISIN: p.ISIN, Symbol: p.Symbol, Qty: 5, Value: "500.000", Reason: "test", Debit: "1000.00", Source: src, Status: "Awaiting compliance", RequestedBy: "anita.rao", At: o.now().Format(time.RFC3339)}
		if _, err := docs.Put(ctx, o.Docs, T, cCPLRelease, id, r, 0, "anita.rao"); err != nil {
			t.Fatal(err)
		}
		return r
	}
	put("CF-901", "MTF")
	if _, err := o.DecideInvocation(ctx, T, "anita.rao", "CF-901", true, ""); err == nil {
		t.Fatal("risk cannot approve its own invocation")
	}
	r, err := o.DecideInvocation(ctx, T, "vikram.shah", "CF-901", true, "")
	if err != nil || r.Status != "Notice given" || r.DueAt == "" {
		t.Fatalf("approve: %+v %v", r, err)
	}
	if _, err := o.InvokeNow(ctx, T, "anita.rao", "CF-901"); err == nil {
		t.Fatal("invoked during the notice")
	}
	later(25 * time.Hour)
	o.cdslPledgeTick(ctx, T, o.now())
	r, _, _ = docs.Get[PledgeRelease](ctx, o.Docs, T, cCPLRelease, "CF-901")
	if r.Status != "Done" || len(sent) != 2 || sent[1].PledgeIdentifier != "CF" || sent[1].ISINs[0].InvocationValue != "500.000" || sent[1].Type != "" {
		t.Fatalf("invoked: %+v %+v", r, sent)
	}
	h2, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID(ucc, p.ISIN))
	if h2.Qty != h1.Qty-5 || h2.Pledged != h1.Pledged-5 {
		t.Fatalf("holding after invocation %+v -> %+v", h1, h2)
	}
	// A debit paid during the notice cancels the invocation.
	put("CF-902", "Debit")
	o.DecideInvocation(ctx, T, "vikram.shah", "CF-902", true, "")
	later(25 * time.Hour)
	funds, _ := o.clientFunds(ctx, T, o.now().Format("2006-01-02"), "")
	r, err = o.InvokeNow(ctx, T, "anita.rao", "CF-902")
	if funds[ucc] >= 0 && (err != nil || r.Status != "Cancelled") {
		t.Fatalf("paid debit: %+v %v", r, err)
	}
}

func TestCDSLRepledgeAndNormalPledge(t *testing.T) {
	var mr cplMRSetup
	var ep cplSetup
	o, _ := cplSetupTest(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		reqid := r.PostForm.Get("reqid")
		switch r.URL.Path {
		case "/PledgeServices/MRSetup":
			cplOpenT(t, r.PostForm.Get("pledgedtls"), &mr)
			var lines []map[string]string
			for _, l := range mr.Lines {
				lines = append(lines, map[string]string{"pledgeeboreqid": l.PledgeeBOReqID, "pledgeseqno": l.PledgeSeqNo, "boreqstatus": "0"})
			}
			json.NewEncoder(w).Encode(map[string]string{"pledgeresdtls": cplSeal(t, map[string]any{"reqid": reqid, "pledgeidentifier": "MR", "resstatus": "0", "mrgnrepldgdtls": lines})})
		case "/PledgeServices/Setup":
			cplOpenT(t, r.PostForm.Get("pledgedtls"), &ep)
			var lines []map[string]string
			for i, l := range ep.ISINs {
				st := map[bool]string{true: "0", false: "1"}[i == 0]
				lines = append(lines, map[string]string{"isinreqid": l.ISINReqID, "psn": map[bool]string{true: "3001", false: ""}[i == 0], "psnstatus": "W", "reqstatus": st, "errorcode": map[bool]string{true: "", false: "ERR074"}[i == 0]})
			}
			w.Write([]byte(cplSeal(t, map[string]any{"reqid": reqid, "pledgeidentifier": "EP", "reqstatus": "0", "isinresdtls": lines})))
		default:
			w.WriteHeader(404)
		}
	})
	ctx := t.Context()
	p := cplPledged(t, o, "BH1001", 30)
	if _, err := o.RequestRepledge(ctx, T, "priya.nair", []RepledgeAsk{{PSN: p.PSN, Qty: 31}}); err == nil {
		t.Fatal("more than pledged")
	}
	b, err := o.RequestRepledge(ctx, T, "anita.rao", []RepledgeAsk{{PSN: p.PSN, Qty: 25}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideRepledge(ctx, T, "anita.rao", b.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker sent: %v", err)
	}
	b, err = o.DecideRepledge(ctx, T, "farhan.khan", b.ID, true, "")
	if err != nil || b.Status != "Done" || mr.PledgeIdentifier != "MR" || mr.Lines[0].PledgeSeqNo != p.PSN || mr.Lines[0].PledgeeBOID != "1100001100015432" || mr.Lines[0].PledgeQuantity != "25" || mr.Lines[0].PledgeType != "MP" {
		t.Fatalf("repledge: %+v %v %+v", b, err, mr)
	}
	rows, loaded := o.RepledgeRecon(ctx, T, D)
	if loaded || len(rows) != 1 || rows[0].Repledged != 25 || rows[0].Accepted != -1 {
		t.Fatalf("recon without file: %+v", rows)
	}
	o.putCCData(ctx, T, "priya.nair", "repledge", D, "CM", "F_90311_SEC_PLEDGE_16102026_1.csv", nil, nil)
	cd, _, _ := docs.Get[CCData](ctx, o.Docs, T, cCCData, "repledge|"+D+"|CM")
	cd.Rows = []map[string]string{{"ClientCpCode": "BH1001", "Isin": p.ISIN, "Qty": "20", "Symbol": p.Symbol}}
	docs.Upsert(ctx, o.Docs, T, cCCData, cd.ID, cd, "priya.nair")
	rows, loaded = o.RepledgeRecon(ctx, T, D)
	if !loaded || rows[0].Accepted != 20 || rows[0].Diff != 5 || rows[0].Note == "" {
		t.Fatalf("recon: %+v", rows)
	}
	if len(o.cplRepledgable(ctx, T)) != 1 {
		t.Error("5 left to re-pledge")
	}

	// A normal pledge to a lender: maker, checker, CDSL; one ISIN fails.
	hs, _ := o.Holdings(ctx, T, "BH1002")
	var lines []NormalLine
	for _, h := range hs {
		if _, free := o.cplFree(ctx, T, "BH1002", h.ISIN, ""); free >= 2 && len(lines) < 2 {
			lines = append(lines, NormalLine{ISIN: h.ISIN, Qty: 2})
		}
	}
	n, err := o.RequestNormalPledge(ctx, T, "priya.nair", NormalPledge{UCC: "BH1002", Lender: "Sample NBFC Ltd", PledgeeBOID: "1208160099990001", Agreement: "LAS-77", Lines: lines})
	if err != nil {
		t.Fatal(err)
	}
	_, free0 := o.cplFree(ctx, T, "BH1002", lines[0].ISIN, "")
	n, err = o.DecideNormalPledge(ctx, T, "farhan.khan", n.ID, true, "")
	if err != nil || n.Status != "Partly done" || n.Lines[0].PSN != "3001" || n.Lines[1].Status != "Failed" || n.Lines[1].ErrCode != "ERR074" {
		t.Fatalf("normal: %+v %v", n, err)
	}
	if ep.PledgeIdentifier != "EP" || ep.PledgorBOID != "1208160082825519" || ep.PledgeeBOID != "1208160099990001" || ep.ISINs[0].PRFNumber != "LAS-77" || ep.ReturnURL != "" {
		t.Fatalf("pledgedtls: %+v", ep)
	}
	_, free1 := o.cplFree(ctx, T, "BH1002", lines[0].ISIN, "")
	if free1 != free0 {
		t.Errorf("free %d -> %d: pending and done lender pledges are both held", free0, free1)
	}
	if hh, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID("BH1002", lines[0].ISIN)); hh.Pledged != hs[0].Pledged && lines[0].ISIN == hs[0].ISIN {
		t.Error("a lender pledge is not margin collateral")
	}
}

func TestCDSLEarlyPayin(t *testing.T) {
	var got struct {
		hdr  http.Header
		body cplEPISetup
	}
	fail := false
	o, later := cplSetupTest(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var env map[string]string
		json.Unmarshal(b, &env)
		switch r.URL.Path {
		case "/EarlyPayin/v2.0":
			got.hdr = r.Header.Clone()
			cplOpenT(t, env["encryptreqdtl"], &got.body)
			if fail {
				w.WriteHeader(502)
				return
			}
			var bos []map[string]any
			for _, s := range got.body.Setup {
				var ls []map[string]string
				for _, l := range s.ISINs {
					ls = append(ls, map[string]string{"isinreqid": l.ISINReqID, "isin": l.ISIN, "isinreqstatus": "0", "eptxnid": "4456789"})
				}
				bos = append(bos, map[string]any{"boreqid": "B1", "boid": s.BOID, "boreqstatus": "0", "isinresdtls": ls})
			}
			json.NewEncoder(w).Encode(map[string]string{"encryptresdtl": cplSeal(t, map[string]any{"epsetupresdtls": map[string]any{"reqseqno": r.Header.Get("requestseqno"), "resstatus": "0", "resdtls": bos}})})
		case "/EarlyPayin/Inquiry":
			var q map[string]string
			cplOpenT(t, env["encryptreqdtl"], &q)
			var ls []map[string]string
			for _, s := range got.body.Setup {
				if s.ReqSeqNo == q["dpreqseqno"] {
					for _, l := range s.ISINs {
						ls = append(ls, map[string]string{"isinreqid": l.ISINReqID, "isin": l.ISIN, "isinreqstatus": "0", "quantity": l.Qty, "eptxnid": "5550001", "status": "S"})
					}
				}
			}
			json.NewEncoder(w).Encode(map[string]string{"encryptresdtl": cplSeal(t, map[string]any{"dpreqseqno": q["dpreqseqno"], "rresstatus": "0", "resdtls": []map[string]any{{"boreqid": "B2", "boreqstatus": "0", "isinresdtls": ls}}})})
		default:
			w.WriteHeader(404)
		}
	})
	ctx := t.Context()
	h := cplFreeHolding(t, o, "BH1001", 10)
	in := []EPIInput{{UCC: "BH1001", ISIN: h.ISIN, Qty: 10, SettID: "1110002026201"}}
	if _, err := o.EnterEPI(ctx, T, "priya.nair", in); err == nil || !strings.Contains(err.Error(), "DIS") {
		t.Fatalf("no DDPI and no DIS: %v", err)
	}
	in[0].DISNo = "1234567"
	rs, err := o.EnterEPI(ctx, T, "priya.nair", in)
	if err != nil || len(rs) != 1 || rs[0].Flag != "N" {
		t.Fatalf("enter: %+v %v", rs, err)
	}
	if _, err := o.DecideEPI(ctx, T, "priya.nair", []string{rs[0].ID}, true, ""); err == nil {
		t.Fatal("maker sent")
	}
	rs, err = o.DecideEPI(ctx, T, "farhan.khan", []string{rs[0].ID}, true, "")
	if err != nil || rs[0].Status != "Accepted" || rs[0].EPTxnID != "4456789" {
		t.Fatalf("send: %+v %v", rs, err)
	}
	s := got.body.Setup[0]
	if got.hdr.Get("dpid") != "10900" || got.body.DPID != "10900" || s.BOID != "1208160041484254" || s.SettID != "1110002026201" || s.EPBOID != "1100001100099999" || s.ElecTxn != "N" || s.DISNo != "1234567" ||
		s.CheckerID != "farhan.khan" || s.ISINs[0].Qty != "10" || len(s.ReqSeqNo) != 24 {
		t.Fatalf("setup: %+v %+v", got.hdr, got.body)
	}
	acc := o.EPIAccepted(ctx, T, D)
	if len(acc) != 1 || acc[0].Qty != 10 || acc[0].SettID != "1110002026201" || acc[0].ISIN != h.ISIN {
		t.Fatalf("accepted: %+v", acc)
	}
	if o.epiCovered(ctx, T, D)["BH1001|"+acc[0].Symbol] != 10 {
		t.Error("live risk cover")
	}

	// No answer: the tick asks the inquiry API.
	fail = true
	in[0].Qty, in[0].DISNo = 3, "1234568"
	rs, _ = o.EnterEPI(ctx, T, "priya.nair", in)
	rs, err = o.DecideEPI(ctx, T, "farhan.khan", []string{rs[0].ID}, true, "")
	if err != nil || rs[0].Status != "Sent" {
		t.Fatalf("no answer: %+v %v", rs, err)
	}
	later(3 * time.Minute)
	o.cdslPledgeTick(ctx, T, o.now())
	x, _, _ := docs.Get[EPIRecord](ctx, o.Docs, T, cEPI, rs[0].ID)
	if x.Status != "Accepted" || x.EPTxnID != "5550001" || x.TxnStatus != "S" {
		t.Fatalf("after inquiry: %+v", x)
	}
	if len(o.EPIAccepted(ctx, T, D)) != 2 {
		t.Error("two accepted")
	}

	// A postback under another key is refused.
	other, _ := cdslAESKey("ffffffffffffffffffffffffffffffff", "k")
	iv, _ := cdslIV("")
	forged, _ := cdslEncrypt(other, iv, []byte(`{"resdtls":[]}`))
	if _, err := o.CDSLEPIHook(ctx, T, []byte(`{"encryptresdtl":"`+forged+`"}`)); err == nil {
		t.Fatal("forged postback")
	}
	v, err := o.EarlyPayins(ctx, T, D)
	if err != nil || len(v.Records) != 2 || strings.Contains(v.Records[0].BOID, "1208160041") {
		t.Fatalf("screen: %+v %v", v, err)
	}
}
