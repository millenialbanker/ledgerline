package ops

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// TestCUSPAToCDSL: the depository's automatic pledge in CDSL's transaction
// export puts its PSN on the CUSPA line; with the pledge API on, the
// line's invocation and release go to CDSL at once and CDSL's answer is on
// the line; one CDSL refuses goes to the work queue; without the API the
// depository list is given by hand as before.
func TestCUSPAToCDSL(t *testing.T) {
	var sent []cplSetup
	fail := ""
	o, _ := cplSetupTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/PledgeServices/UnpledgeSetup" && r.URL.Path != "/PledgeServices/ConfiscateSetup" {
			w.WriteHeader(404)
			return
		}
		r.ParseForm()
		var d cplSetup
		cplOpenT(t, r.PostForm.Get("pledgedtls"), &d)
		sent = append(sent, d)
		l := d.ISINs[0]
		st, code := "0", ""
		if fail != "" {
			st, code = "1", "PLG101"
		}
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		w.Write([]byte(url.Values{"reqid": {r.PostForm.Get("reqid")}, "pledgeresdtls": {cplSeal(t, map[string]any{"reqid": r.PostForm.Get("reqid"), "pledgeidentifier": d.PledgeIdentifier, "reqstatus": st,
			"isindtls": []map[string]string{{"isinreqid": l.ISINReqID, "psn": l.PSN, "psnstatus": "A", "reqstatus": st, "errorcode": code, "errormsg": fail}}})}}.Encode()))
	})
	ctx := t.Context()
	set, _ := o.Settings(ctx, T)
	set.CUSPADays = 3
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	set, _ = o.Settings(ctx, T)
	st, _ := o.Obligations(ctx, T, D)
	itc := obl(t, st, "BH1010", "NSE", "ITC")
	if _, err := o.SettleAction(ctx, T, "priya.nair", D, itc.Key, "cuspa"); err != nil {
		t.Fatal(err)
	}
	items, _ := docs.List[CUSPAItem](ctx, o.Docs, T, cCUSPA, "")
	it := items[0]
	payOut := set.NextSettlementDay(D)
	day := func(n int) string { return addTradingDays(set, payOut, n) }
	bo := o.bo(ctx, T, mustClient(t, o, "BH1010")).BOID

	// CDSL's transaction export: the automatic pledge to the firm's CUSPA.
	row := xfile.Row{Values: map[string]string{"ClntId": bo, "TxnCd": "CPLBL", "TxnTyp": "PLG", "ISIN": it.ISIN, "Qty": "10.000", "TxnID": "0004567890", "LineNb": "1",
		"ExecDt": payOut, "BizDt": payOut, "CuspaTxnFlg": "TM", "CuspaAcct": "1208160000000099"}}
	if _, _, err := o.loadCDSLTxns(ctx, T, "priya.nair", []xfile.Row{row}); err != nil {
		t.Fatal(err)
	}
	if it, _, _ = docs.Get[CUSPAItem](ctx, o.Docs, T, cCUSPA, it.ID); it.PSN != "4567890" {
		t.Fatalf("PSN not on the line: %+v", it)
	}
	// The same export again changes nothing.
	o.loadCDSLTxns(ctx, T, "priya.nair", []xfile.Row{row})
	if ps, _ := docs.List[CUSPAPledge](ctx, o.Docs, T, cCUSPAPSN, ""); len(ps) != 1 || ps[0].CUSPA != it.ID {
		t.Fatalf("pledges %+v", ps)
	}

	// Notice, then on day 4 the invocation goes to CDSL as a confiscation.
	if res, err := o.CUSPABulk(ctx, T, "priya.nair", CUSPABulkReq{Date: day(1), Action: "notice", IDs: []string{it.ID}}); err != nil || res.Done != 1 {
		t.Fatalf("notice %+v %v", res, err)
	}
	res, err := o.CUSPABulk(ctx, T, "anita.rao", CUSPABulkReq{Date: day(4), Action: "invoke", IDs: []string{it.ID}})
	if err != nil || res.Done != 1 || len(sent) != 1 {
		t.Fatalf("invoke %+v %v (%d sent)", res, err, len(sent))
	}
	q := res.Lines[0].Qty
	if d := sent[0]; d.PledgeIdentifier != "CF" || d.Type != "" || d.ISINs[0].PSN != "4567890" || d.ISINs[0].PartQuantity != cplQty(q)+".000" || d.ISINs[0].InvocationValue != res.Lines[0].Value.Fixed(3) {
		t.Fatalf("confiscation sent %+v", d)
	}
	it, _, _ = docs.Get[CUSPAItem](ctx, o.Docs, T, cCUSPA, it.ID)
	if inv := it.Events[len(it.Events)-1]; inv.What != "Invoked" || !strings.HasPrefix(inv.CDSL, "CF-") || !strings.HasSuffix(inv.CDSL, " Done") || it.CDSL != inv.CDSL {
		t.Fatalf("invocation on the line %+v", it)
	}
	var cf PledgeRelease
	for _, r := range o.cplReleases(ctx, T) {
		if r.Kind == "CF" {
			cf = r
		}
	}
	if cf.Of != cuspaOf || cf.Source != "CUSPA" || cf.CUSPA != it.ID || cf.Status != "Done" || cf.UCC != "BH1010" {
		t.Fatalf("invocation request %+v", cf)
	}

	// The rest is released: an unpledge by the pledgee.
	if res, err = o.CUSPABulk(ctx, T, "anita.rao", CUSPABulkReq{Date: day(4), Action: "release", IDs: []string{it.ID}}); err != nil || res.Done != 1 || len(sent) != 2 {
		t.Fatalf("release %+v %v (%d sent)", res, err, len(sent))
	}
	if d := sent[1]; d.PledgeIdentifier != "UN" || d.Type != "pledgee" || d.ISINs[0].PSN != "4567890" || d.ISINs[0].PartQuantity != cplQty(itc.NetQty-q)+".000" {
		t.Fatalf("unpledge sent %+v", d)
	}
	_, dep, _ := o.CUSPAFile(ctx, T, day(4), "depository")
	if !strings.Contains(string(dep), "Sent by CDSL API") || !strings.Contains(string(dep), "4567890,CF-") || !strings.Contains(string(dep), "4567890,UN-") {
		t.Fatalf("depository list %q", dep)
	}

	// CDSL refuses: the line says so and staff are asked to give it by hand.
	fail = "PSN not found"
	it2 := cuspaTestLine(t, o, "BH1010")
	if res, err = o.CUSPABulk(ctx, T, "anita.rao", CUSPABulkReq{Date: day(1), Action: "release", IDs: []string{it2.ID}}); err != nil || res.Done != 1 {
		t.Fatalf("release %+v %v", res, err)
	}
	it2, _, _ = docs.Get[CUSPAItem](ctx, o.Docs, T, cCUSPA, it2.ID)
	if !strings.Contains(it2.CDSL, "Failed") || !strings.Contains(it2.CDSL, "PSN not found") || !hasOpenWork(t, o, "cuspa", it2.ID+":cdsl") {
		t.Fatalf("refused release %+v", it2)
	}
}

// cuspaTestLine is another CUSPA line of the client with its own PSN.
func cuspaTestLine(t *testing.T, o *Ops, ucc string) CUSPAItem {
	t.Helper()
	it := CUSPAItem{ID: "CU-90001", Date: D, UCC: ucc, Exchange: "NSE", ISIN: "INE002A01018", Symbol: "RELIANCE", Qty: 5, Status: CUSPAHeld, By: "test", PayOut: D, PSN: "7770001"}
	if err := docs.Upsert(t.Context(), o.Docs, T, cCUSPA, it.ID, it, "test"); err != nil {
		t.Fatal(err)
	}
	return it
}

// TestCUSPAReviewByAPI: the daily review's release of a paid-up client's
// line goes to CDSL by the API, so no one is asked to give it by hand.
func TestCUSPAReviewByAPI(t *testing.T) {
	n := 0
	o, _ := cplSetupTest(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		var d cplSetup
		cplOpenT(t, r.PostForm.Get("pledgedtls"), &d)
		n++
		l := d.ISINs[0]
		w.Write([]byte(url.Values{"reqid": {r.PostForm.Get("reqid")}, "pledgeresdtls": {cplSeal(t, map[string]any{"reqid": r.PostForm.Get("reqid"), "reqstatus": "0",
			"isindtls": []map[string]string{{"isinreqid": l.ISINReqID, "psn": l.PSN, "psnstatus": "A", "reqstatus": "0"}}})}}.Encode()))
	})
	ctx := t.Context()
	set, _ := o.Settings(ctx, T)
	st, _ := o.Obligations(ctx, T, D)
	itc := obl(t, st, "BH1010", "NSE", "ITC")
	if _, err := o.SettleAction(ctx, T, "priya.nair", D, itc.Key, "cuspa"); err != nil {
		t.Fatal(err)
	}
	payOut := set.NextSettlementDay(D)
	// The pledge was seen before the line was made: the review links it.
	items, _ := docs.List[CUSPAItem](ctx, o.Docs, T, cCUSPA, "")
	if err := docs.Upsert(ctx, o.Docs, T, cCUSPAPSN, "5550001", CUSPAPledge{PSN: "5550001", UCC: "BH1010", ISIN: items[0].ISIN, Date: payOut}, "test"); err != nil {
		t.Fatal(err)
	}
	b, _ := o.cuspaBook(ctx, T, payOut)
	if debit := b.funds["BH1010"]; debit < 0 {
		rc, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: payOut, UCC: "BH1010", Mode: "NEFT", Ref: "UTRCUSPA2", Amount: -debit})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := o.DecideReceipt(ctx, T, "farhan.khan", rc.ID, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	date := set.NextBusinessDay(payOut)
	sum, _, err := o.cuspaReview(ctx, T, SystemActor, date)
	if err != nil || !strings.Contains(sum, "1 by CDSL's pledge API") || n != 1 {
		t.Fatalf("review %q %v (%d sent)", sum, err, n)
	}
	it, _, _ := docs.Get[CUSPAItem](ctx, o.Docs, T, cCUSPA, items[0].ID)
	if it.PSN != "5550001" || it.Qty != 0 || !strings.HasSuffix(it.CDSL, " Done") {
		t.Fatalf("line %+v", it)
	}
	if hasOpenWork(t, o, "cuspa", "release:"+date) {
		t.Fatal("asked to give a release CDSL already did")
	}
}
