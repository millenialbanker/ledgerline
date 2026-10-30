package ops

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// fakeEDIS is CDSL's eDIS server side: the inquiry and TPIN generation.
type fakeEDIS struct {
	mu       sync.Mutex
	pending  bool   // the inquiry says the TPIN is not entered yet
	txnReq   string // the TxnReqId the inquiry answers for
	pins     int
	lastPin  map[string]any
	inquired map[string]any
	t        *testing.T
}

func (f *fakeEDIS) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.t
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		t.Errorf("not form data: %v", err)
		w.WriteHeader(400)
		return
	}
	switch r.URL.Path {
	case "/" + edisPathInquiry:
		if r.Header.Get("DPID") != "081600" || r.Header.Get("Version") != "1.0" {
			w.WriteHeader(400)
			return
		}
		f.inquired = cdxTestOpen(t, testEDISKey, r.FormValue("EncrypTransDtl"))
		reqID := r.Header.Get("ReqId")
		body := map[string]any{"ReqId": reqID, "ReqType": "D", "ResId": "82E255", "ResStatus": "0", "ResError": "ERR058"}
		if !f.pending {
			body["ResError"] = ""
			body["ResStatus"] = "1"
			body["recordresDtls"] = []any{map[string]any{"TxnReqId": f.txnReq, "TxnId": "11111113", "Status": "1", "Errorcode": "ERR027"}}
		}
		w.Write([]byte(cdxTestSeal(t, testEDISKey, body)))
	case "/" + edisPathPin:
		if r.Header.Get("DPId") != "081600" || len(r.Header.Get("ReqId")) != 16 {
			w.WriteHeader(400)
			return
		}
		f.pins++
		f.lastPin = cdxTestOpen(t, testEDISKey, r.FormValue("EncryptDtls"))
		w.Write([]byte(`{"Body":"` + cdxTestSeal(t, testEDISKey, map[string]any{"ReqId": r.Header.Get("ReqId"), "ResId": "49E353", "Status": "00", "ErrorCode": ""}) + `"}`))
	default:
		w.WriteHeader(404)
	}
}

func TestCDSLEDIS(t *testing.T) {
	o, ctx := setup(t)
	c, h := cdxTestClient(t, o, ctx)
	if err := o.PutUser(ctx, T, "admin", User{ID: "client.edis", Name: c.Name, Role: RoleClient, Active: true, Clients: []string{c.UCC}}); err != nil {
		t.Fatal(err)
	}
	fk := &fakeEDIS{t: t}
	srv := useCDSLServer(t, fk.handler)
	cdxTestConn(t, o, srv.URL)
	req := EDISRequest{UCC: c.UCC, Lines: []EDISRequestLine{{ISIN: h.ISIN, Qty: 10}}}
	if _, _, err := o.StartEDIS(ctx, T, "client.edis", req, ""); !errors.Is(err, ErrState) {
		t.Fatalf("eDIS off: %v", err)
	}
	if v, err := o.PortalEDIS(ctx, T, "client.edis", ""); err != nil || v.On || v.Problem == "" {
		t.Fatalf("portal off: %+v %v", v, err)
	}
	cdxTestConn(t, o, srv.URL, cdslFamEDIS)

	// The portal page.
	v, err := o.PortalEDIS(ctx, T, "client.edis", "")
	if err != nil || !v.On || v.UCC != c.UCC || len(v.Holdings) == 0 || len(v.Exchanges) != 1 || strings.Contains(v.BOID, c.Demat.ID[:8]) {
		t.Fatalf("portal %+v %v", v, err)
	}
	// Someone else's account, too many shares, a client without the
	// account on the portal.
	if _, _, err := o.StartEDIS(ctx, T, "client.edis", EDISRequest{UCC: "BH9999", Lines: req.Lines}, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other account: %v", err)
	}
	big := req
	big.Lines = []EDISRequestLine{{ISIN: h.ISIN, Qty: h.Qty + 1}}
	if _, _, err := o.StartEDIS(ctx, T, "client.edis", big, ""); err == nil || !strings.Contains(err.Error(), "free") {
		t.Fatalf("over holding: %v", err)
	}
	bse := req
	bse.Exchange = "BSE"
	if _, _, err := o.StartEDIS(ctx, T, "client.edis", bse, ""); err == nil || !strings.Contains(err.Error(), "clearing member") {
		t.Fatalf("no BSE CM: %v", err)
	}

	f, a, err := o.StartEDIS(ctx, T, "client.edis", req, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Action != srv.URL+"/EDIS/VerifyDIS" || f.Fields["DPId"] != "081600" || f.Fields["Version"] != "1.2" || len(f.Fields["ReqId"]) != 16 || a.Via != "Portal" {
		t.Fatalf("form %+v", f)
	}
	m := cdxTestOpen(t, testEDISKey, f.Fields["TransDtls"])
	set, _ := o.Settings(ctx, T)
	settle := set.NextBusinessDay(o.now().Format("2006-01-02"))
	recs := m["RecordDtls"].([]any)
	r0 := recs[0].(map[string]any)
	if m["ReqType"] != "D" || m["ReqIdentifier"] != "S" || m["BOID"] != c.Demat.ID || m["CMID"] != "M1234" || m["TMID"] != "T1234" || m["ExID"] != "12" || m["NumOfDays"] != "3" ||
		m["SettleId"] != "" || m["ExecDate"] != "" || m["ReturnURL"] != "https://bo.example.test/hooks/"+T+"/cdsl/edis/return" ||
		r0["Quantity"] != "10" || r0["ISIN"] != h.ISIN || r0["ExecDate"] != edisDDMMYYYY(settle) || len(r0["TxnReqId"].(string)) != 16 {
		t.Fatalf("TransDtls %+v", m)
	}

	// CDSL's redirect back: forged, for another request, then genuine.
	ret := func(key, reqID string, recs []any) url.Values {
		return url.Values{"ReqId": {a.ReqID}, "ReqType": {"D"}, "transDtls": {cdxTestSeal(t, key, map[string]any{"TransResDtls": map[string]any{"ReqId": reqID, "ReqType": "D", "ResId": "R01", "ResStatus": "0", "ResError": "", "RecordResDtls": recs}})}}
	}
	okRec := []any{map[string]any{"TxnReqId": a.Lines[0].TxnReqID, "TxnId": "0406202000112D01", "Status": "0", "Errorcode": ""}}
	if _, err := o.EDISReturn(ctx, T, ret(testCDSLKey, a.ReqID, okRec)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("forged: %v", err)
	}
	if _, err := o.EDISReturn(ctx, T, ret(testEDISKey, "0000000000000001", okRec)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other request: %v", err)
	}
	out, err := o.EDISReturn(ctx, T, ret(testEDISKey, a.ReqID, okRec))
	if err != nil || !out.OK || out.Back != "/#pedis" {
		t.Fatalf("return %+v %v", out, err)
	}
	a, _, _ = docs.Get[EDISAuth](ctx, o.Docs, T, cEDIS, a.ReqID)
	if a.Status != "Authorised" || a.Lines[0].TxnID != "0406202000112D01" || a.AnsweredBy != "Redirect" || a.ValidTill != addDays(o.now().Format("2006-01-02"), 2) {
		t.Fatalf("auth %+v", a)
	}
	msgs, _ := o.Messages(ctx, T, c.UCC, 20)
	sent := false
	for _, x := range msgs {
		sent = sent || (x.Event == "edis.authorised" && strings.Contains(x.Body, a.ReqID))
	}
	if !sent {
		t.Fatal("no message to the client")
	}
	// Live risk counts the authorised quantity.
	need, qty := o.edisDeliverable(ctx, T, o.now().Format("2006-01-02"))
	if !need[c.UCC] || qty[c.UCC+"|"+h.ISIN] != 10 || (h.Symbol != "" && qty[c.UCC+"|"+h.Symbol] != 10) {
		t.Fatalf("deliverable %v %v", need[c.UCC], qty)
	}

	// A request left without an answer: the inquiry.
	f2, a2, err := o.StartEDIS(ctx, T, "priya.nair", EDISRequest{UCC: c.UCC, Lines: []EDISRequestLine{{ISIN: h.ISIN, Qty: 2}}}, "https://ignored.example.test")
	if err != nil || a2.Via != "Staff" || !strings.HasPrefix(cdxTestOpen(t, testEDISKey, f2.Fields["TransDtls"])["ReturnURL"].(string), "https://bo.example.test/") {
		t.Fatalf("staff start %+v %v", a2, err)
	}
	fk.mu.Lock()
	fk.pending = true
	fk.mu.Unlock()
	if a2, err = o.EDISInquire(ctx, T, "priya.nair", a2.ReqID); err != nil || a2.Status != "Awaiting TPIN" || a2.Inquiries != 1 || fk.inquired["ReqType"] != "D" {
		t.Fatalf("pending inquiry %+v %v %v", a2, err, fk.inquired)
	}
	fk.mu.Lock()
	fk.pending, fk.txnReq = false, a2.Lines[0].TxnReqID
	fk.mu.Unlock()
	if a2, err = o.EDISInquire(ctx, T, "priya.nair", a2.ReqID); err != nil || a2.Status != "Failed" || !strings.Contains(a2.Lines[0].Error, "three times") || a2.AnsweredBy != "Inquiry" {
		t.Fatalf("answered inquiry %+v %v", a2, err)
	}
	// The tick closes an earlier day's unanswered request.
	_, a3, err := o.StartEDIS(ctx, T, "client.edis", EDISRequest{UCC: c.UCC, Lines: []EDISRequestLine{{ISIN: h.ISIN, Qty: 1}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	o.edisTick(ctx, T, o.now().AddDate(0, 0, 1))
	if a3, _, _ = docs.Get[EDISAuth](ctx, o.Docs, T, cEDIS, a3.ReqID); a3.Status != "No answer" {
		t.Fatalf("lapsed %s", a3.Status)
	}

	// Cancel: not within 20 minutes; then through CDSL's page.
	if _, err := o.StartEDISCancel(ctx, T, "client.edis", a.ReqID, ""); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "20 minutes") {
		t.Fatalf("early cancel: %v", err)
	}
	a.DoneAt = o.now().Add(-30 * time.Minute).Format(time.RFC3339)
	docs.Upsert(ctx, o.Docs, T, cEDIS, a.ReqID, a, "admin")
	cf, err := o.StartEDISCancel(ctx, T, "client.edis", a.ReqID, "")
	if err != nil {
		t.Fatal(err)
	}
	rv := cdxTestOpen(t, testEDISKey, cf.Fields["RevokDtls"])
	rr := rv["RecordDtls"].([]any)[0].(map[string]any)
	if cf.Action != srv.URL+"/EDIS/CancelTransaction" || len(cf.Fields["RevocReqId"]) != 24 || rv["ReqType"] != "D" || rv["BOID"] != c.Demat.ID || rr["TxnId"] != "0406202000112D01" || rr["ResId"] != "R01" || len(rr["RevocReqTxnId"].(string)) != 24 {
		t.Fatalf("cancel form %+v %+v", cf, rv)
	}
	cret := url.Values{"RevocReqId": {cf.Fields["RevocReqId"]}, "ReqType": {"D"}, "TransResDtls": {cdxTestSeal(t, testEDISKey, map[string]any{"TransResDtls": map[string]any{
		"RevocReqId": cf.Fields["RevocReqId"], "ReqType": "D", "RevocResStatus": "0",
		"RecordResDtls": []any{map[string]any{" RevocReqTxnId ": rr["RevocReqTxnId"], " TxnId ": "0406202000112D01", "ISIN": h.ISIN, "EdisQuantity": "10", "CancQuantity": "10", "Status": "0", "Errorcode": ""}}}})}}
	if out, err = o.EDISCancelReturn(ctx, T, cret); err != nil || !out.OK {
		t.Fatalf("cancel return %+v %v", out, err)
	}
	if a, _, _ = docs.Get[EDISAuth](ctx, o.Docs, T, cEDIS, a.ReqID); a.Status != "Cancelled" || a.Lines[0].Cancelled != 10 {
		t.Fatalf("after cancel %+v", a)
	}
	if _, qty = o.edisDeliverable(ctx, T, o.now().Format("2006-01-02")); qty[c.UCC+"|"+h.ISIN] != 0 {
		t.Fatalf("still deliverable %v", qty)
	}

	// TPIN generation: staff need a note; three a day.
	if _, err := o.GenerateTPIN(ctx, T, "priya.nair", c.UCC, ""); err == nil {
		t.Fatal("staff TPIN without a note")
	}
	p, err := o.GenerateTPIN(ctx, T, "client.edis", c.UCC, "")
	if err != nil || p.Status != "Sent to the client" || fk.lastPin["BOID"] != c.Demat.ID || fk.lastPin["PAN"] != c.PAN || fk.lastPin["ReqFlag"] != "N" {
		t.Fatalf("tpin %+v %v %v", p, err, fk.lastPin)
	}
	o.GenerateTPIN(ctx, T, "priya.nair", c.UCC, "Call ref 55")
	o.GenerateTPIN(ctx, T, "client.edis", c.UCC, "")
	if _, err := o.GenerateTPIN(ctx, T, "client.edis", c.UCC, ""); err == nil || fk.pins != 3 {
		t.Fatalf("fourth TPIN today: %v (%d)", err, fk.pins)
	}
	// TPIN change: BO ID and PAN for CDSL's page.
	cp, err := o.ChangeTPINForm(ctx, T, "client.edis", c.UCC)
	if err != nil || cp.Action != srv.URL+"/Home/ChangeBOPin" || cp.Fields["dpId"] != "081600" {
		t.Fatalf("change %+v %v", cp, err)
	}
	if bd := cdxTestOpen(t, testEDISKey, cp.Fields["boDtls"]); bd["BOID"] != c.Demat.ID || bd["PAN"] != c.PAN {
		t.Fatalf("boDtls %+v", bd)
	}
	sv, err := o.EDISScreen(ctx, T, "anita.rao", o.now().Format("2006-01-02"))
	if err != nil || len(sv.Auths) != 3 || len(sv.Pins) != 3 || len(sv.Revocs) != 1 || sv.Version != "1.2" {
		t.Fatalf("screen %d %d %d %v", len(sv.Auths), len(sv.Pins), len(sv.Revocs), err)
	}
	// Logs never carry the full BO ID or the PAN.
	evs, _ := o.App.Audit.List(ctx, T, 0)
	for _, e := range evs {
		if s := fmt.Sprint(e.Object, e.Detail); strings.Contains(s, c.Demat.ID) || strings.Contains(s, c.PAN) {
			t.Fatalf("log carries the BO ID or PAN: %s %s", e.Action, s)
		}
	}
}

func TestCDSLEDISVersion11AndOffMarket(t *testing.T) {
	o, ctx := setup(t)
	c, h := cdxTestClient(t, o, ctx)
	cdxTestConn(t, o, "https://cdsl.example.test", cdslFamEDIS)
	x, _ := o.cdxConn(ctx, T)
	x.c.Values["edis_version"] = "1.1"
	docs.Upsert(ctx, o.Docs, T, cConn, connCDSL, x.c, "admin")
	f, _, err := o.StartEDIS(ctx, T, "priya.nair", EDISRequest{UCC: c.UCC, Lines: []EDISRequestLine{{ISIN: h.ISIN, Qty: 3, SettleID: "1211002020123"}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	m := cdxTestOpen(t, testEDISKey, f.Fields["TransDtls"])
	r0 := m["RecordDtls"].([]any)[0].(map[string]any)
	if f.Fields["Version"] != "1.1" || m["SettleId"] != "1211002020123" || m["ExecDate"] == "" || r0["SettleId"] != nil {
		t.Fatalf("1.1 %+v", m)
	}
	if _, _, err := o.StartEDIS(ctx, T, "priya.nair", EDISRequest{UCC: c.UCC, ReqType: "F", CtrBOID: "12", Reason: "Gift", Lines: []EDISRequestLine{{ISIN: h.ISIN, Qty: 1}}}, ""); err == nil {
		t.Fatal("off-market without a counter account")
	}
	f, a, err := o.StartEDIS(ctx, T, "priya.nair", EDISRequest{UCC: c.UCC, ReqType: "F", CtrBOID: "1208160099999999", Reason: "Gift", ExecDate: o.now().Format("2006-01-02"), Lines: []EDISRequestLine{{ISIN: h.ISIN, Qty: 1}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	m = cdxTestOpen(t, testEDISKey, f.Fields["TransDtls"])
	if m["ReqType"] != "F" || m["CtrBOID"] != "1208160099999999" || m["TrRsnCode"] != "2" || m["NumOfDays"] != "" || m["ExecDate"] != edisDDMMYYYY(o.now().Format("2006-01-02")) || a.ValidTill == "" {
		t.Fatalf("F %+v", m)
	}
}

func TestCDSLEDISInLiveRisk(t *testing.T) {
	o, ctx := setup(t)
	short := func() map[string]string {
		lv, err := o.LiveRisk(ctx, T, D, "")
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, r := range lv.Rows {
			for _, s := range r.Short {
				m[r.UCC+"|"+strings.Fields(s)[0]] = s
			}
		}
		return m
	}
	before := short()
	cdxTestConn(t, o, "https://cdsl.example.test", cdslFamEDIS)
	after := short()
	key := ""
	for k, s := range after {
		if strings.HasSuffix(s, "(not authorised by eDIS)") {
			key = k
			break
		}
	}
	if key == "" {
		t.Skip("the sample day has no delivery sell by a CDSL client of this DP")
	}
	ucc, sym := strings.Split(key, "|")[0], strings.Split(key, "|")[1]
	a := EDISAuth{ReqID: "2610160000000099", UCC: ucc, ReqType: "D", Status: "Authorised", At: D + "T10:00:00+05:30", ValidTill: D,
		Lines: []EDISLine{{TxnReqID: "1", ISIN: "INE000000000", Symbol: sym, Qty: 1 << 20, Status: "Authorised", TxnID: "T"}}}
	if err := docs.Upsert(ctx, o.Docs, T, cEDIS, a.ReqID, a, "admin"); err != nil {
		t.Fatal(err)
	}
	now := short()
	if s := now[key]; strings.Contains(s, "eDIS") {
		t.Fatalf("authorised but still short: %s", s)
	}
	if now[key] != before[key] {
		t.Fatalf("with the authorisation: %q, without eDIS: %q", now[key], before[key])
	}
}
