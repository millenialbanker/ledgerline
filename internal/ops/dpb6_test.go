package ops

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// All data here is made up.

func TestAMCQuarters(t *testing.T) {
	for _, c := range []struct {
		from, closed string
		want         int
	}{
		{"2026-04-01", "2026-04-10", 3}, // first quarter used, three refunded
		{"2026-04-01", "2026-06-30", 3},
		{"2026-04-01", "2026-07-01", 2},
		{"2026-04-01", "2026-12-31", 1},
		{"2026-04-01", "2027-01-05", 0},
		{"2026-04-01", "2027-05-01", 0},
	} {
		if got := amcQuarters(c.from, c.closed); got != c.want {
			t.Errorf("%s closed %s: %d, want %d", c.from, c.closed, got, c.want)
		}
	}
}

func TestAMCRefundOnClosure(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "19:00"))
	c, _ := cdxTestClient(t, o, ctx)
	b := o.boFor(ctx, T, c, "")
	p := DPPlan{Code: "YR", Name: "Yearly", AMCBilling: AMCYearly, Versions: []DPPlanVersion{{From: "2020-01-01", AMCYear: money.MustAmount("800")}}}
	if err := docs.Upsert(ctx, o.Docs, T, cDPPlan, p.Code, p, "t"); err != nil {
		t.Fatal(err)
	}
	b.Plans, b.AMCPaidUpto, b.Status = []DPPlanAt{{Plan: "YR", From: "2020-01-01"}}, addDays(D, 300), "Closed"
	b.ClosedOn = D
	if err := docs.Upsert(ctx, o.Docs, T, cBO, boKey(b), b, "t"); err != nil {
		t.Fatal(err)
	}
	o.dpAMCRefundScan(ctx, T, D)
	r, _, err := docs.Get[DPAMCRefund](ctx, o.Docs, T, cB6DP, "amc|"+b.BOID)
	if err != nil || r.Status != AMCToApprove || r.Unused < 2 || r.Taxable != money.Amount(int64(money.MustAmount("800"))*int64(r.Unused)/4) {
		t.Fatalf("refund: %+v %v", r, err)
	}
	if _, err := o.DecideAMCRefund(ctx, T, "priya.nair", b.BOID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker approved: %v", err)
	}
	r, err = o.DecideAMCRefund(ctx, T, "farhan.khan", b.BOID, true, "")
	if err != nil || r.Status != AMCRefunded || r.Entry == "" || !strings.HasPrefix(r.CreditNote, "CRN/") {
		t.Fatalf("approve: %+v %v", r, err)
	}
	if _, err := o.DecideAMCRefund(ctx, T, "farhan.khan", b.BOID, true, ""); !errors.Is(err, ErrState) {
		t.Fatalf("approved twice: %v", err)
	}
}

func TestDeathIntimationDirect(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	c, _ := cdxTestClient(t, o, ctx)
	in := DeathIntimationReq{UCC: c.UCC, Notifier: "Test Nominee", Relation: "Son", Received: D}
	if _, err := o.RecordDeathIntimation(ctx, T, "priya.nair", in); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("no certificate: %v", err)
	}
	in.Certificate = "DC/TEST/0001"
	d, err := o.RecordDeathIntimation(ctx, T, "priya.nair", in)
	if err != nil || d.List != "Direct intimation" {
		t.Fatalf("intimation: %+v %v", d, err)
	}
	c2, _ := o.Client(ctx, T, c.UCC)
	if c2.Deceased == nil || c2.Deceased.Case != d.ID || !c2.Blocked {
		t.Fatalf("debits not blocked: %+v", c2.Deceased)
	}
	di, _, _ := docs.Get[DeathIntimation](ctx, o.Docs, T, cB6DP, "intim|"+d.ID)
	if len(di.Freezes) == 0 {
		t.Fatalf("no debit freeze raised: %+v", di)
	}
	rq, _, _ := docs.Get[DPRequest](ctx, o.Docs, T, cDPReq, di.Freezes[0])
	if rq.Kind != "Freeze" || rq.Detail["type"] != "Debit" {
		t.Fatalf("freeze request: %+v", rq)
	}
	if _, err := o.RecordDeathCertCheck(ctx, T, "vikram.shah", d.ID, DeathCertCheck{Via: "Government site", Where: "state CRS portal"}); err == nil {
		t.Fatal("check without signature and stamp accepted")
	}
	di, err = o.RecordDeathCertCheck(ctx, T, "vikram.shah", d.ID, DeathCertCheck{Via: "Government site", Where: "state CRS portal", SignedStamped: true})
	if err != nil || di.CheckedOn != D || !di.Direct {
		t.Fatalf("check: %+v %v", di, err)
	}
	if _, err := o.RecordDeathIntimation(ctx, T, "priya.nair", in); !errors.Is(err, ErrState) {
		t.Fatalf("second case: %v", err)
	}
}

func TestKYCDeficiencyCase(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	c, _ := cdxTestClient(t, o, ctx)
	k, err := o.RecordKYCDeficiency(ctx, T, "priya.nair", KYCDefReq{UCC: c.UCC, CDSLRef: "CDSL/TEST/1", Deficiency: "Address proof missing"})
	if err != nil || k.Status != "Tell the BO" {
		t.Fatalf("record: %+v %v", k, err)
	}
	for _, s := range []string{"letter", "email", "phone"} {
		if k, err = o.KYCDefStep(ctx, T, "priya.nair", k.ID, s, D, "ref-"+s); err != nil {
			t.Fatal(err)
		}
	}
	if k.Due != addDays(D, 30) || k.Status != "Waiting 30 days" {
		t.Fatalf("due: %+v", k)
	}
	if _, err := o.KYCDefStep(ctx, T, "priya.nair", k.ID, "freeze", "", ""); !errors.Is(err, ErrState) {
		t.Fatalf("freeze before 30 days: %v", err)
	}
	o.Now = func() time.Time { return siAt(addDays(D, 31), "11:00") }
	k, err = o.KYCDefStep(ctx, T, "priya.nair", k.ID, "freeze", "", "")
	if err != nil || k.FreezeReq == "" {
		t.Fatalf("freeze: %+v %v", k, err)
	}
	rq, _, _ := docs.Get[DPRequest](ctx, o.Docs, T, cDPReq, k.FreezeReq)
	if rq.Detail["reason_code"] != "20" || rq.Detail["type"] != "Both" {
		t.Fatalf("freeze request: %+v", rq.Detail)
	}
	k, _ = o.KYCDefStep(ctx, T, "priya.nair", k.ID, "informed", "", "CDSL mail")
	k, _ = o.KYCDefStep(ctx, T, "priya.nair", k.ID, "ack", "", "ack sent")
	if k.Status != "Done" {
		t.Fatalf("stage: %s", k.Status)
	}
}

func TestOTPClosureDecision(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	c, _ := cdxTestClient(t, o, ctx)
	r, err := o.RecordOTPClosure(ctx, T, "priya.nair", c.UCC, "", "1208160099999999", "CDAS-1", D)
	if err != nil || r.Due <= D {
		t.Fatalf("record: %+v %v", r, err)
	}
	if _, err := o.DecideOTPClosure(ctx, T, "farhan.khan", r.ID, true, map[string]bool{"bo": true}, ""); err == nil {
		t.Fatal("approved without every check")
	}
	r, err = o.DecideOTPClosure(ctx, T, "farhan.khan", r.ID, false, nil, "Target account is closed")
	if err != nil || r.Status != "Rejected" || r.Checker != "farhan.khan" {
		t.Fatalf("reject: %+v %v", r, err)
	}
}

func TestOptOutDeclarationBlocksOpening(t *testing.T) {
	c := Client{UCC: "X1", Nominee: "Opted out"}
	if noNominationChoice(c) == "" {
		t.Fatal("silent opt-out not flagged")
	}
	c.NominationOn = D
	if noNominationChoice(c) != "" {
		t.Fatal("declared opt-out flagged")
	}
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	cs, _ := o.Clients(ctx, T)
	var u string
	for _, x := range cs {
		if len(x.Nominees) == 0 && x.Status == "Active" {
			u = x.UCC
			break
		}
	}
	if u == "" {
		t.Skip("no client without nominees in the seed")
	}
	if _, err := docs.Update(ctx, o.Docs, T, cClient, u, "t", func(x *Client) error { x.NominationOn, x.NominationMode, x.Nominee = "", "", "Opted out"; return nil }); err != nil {
		t.Fatal(err)
	}
	r := CDSLRequest{UCC: u, Kind: CDSLOpen}
	if _, _, err := o.cdslOpenCheck(ctx, T, &r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.Problems, ";"), "opt-out declaration") {
		t.Fatalf("problems: %v", r.Problems)
	}
	if _, err := o.RecordOptOutDeclaration(ctx, T, "priya.nair", u, NominationDeclReq{Mode: "Signed form"}); err == nil {
		t.Fatal("declaration without its reference accepted")
	}
	x, err := o.RecordOptOutDeclaration(ctx, T, "priya.nair", u, NominationDeclReq{Mode: "Signed form", Ref: "SCAN-1"})
	if err != nil || x.NominationState() != "Opted out (declared)" {
		t.Fatalf("declaration: %v %v", x.NominationState(), err)
	}
	r = CDSLRequest{UCC: u, Kind: CDSLOpen}
	_, _, _ = o.cdslOpenCheck(ctx, T, &r)
	if strings.Contains(strings.Join(r.Problems, ";"), "opt-out declaration") {
		t.Fatalf("still blocked: %v", r.Problems)
	}
}

func TestPoATermsMakerChecker(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	c, _ := cdxTestClient(t, o, ctx)
	all := map[string]bool{}
	for _, x := range PoATerms {
		all[x.Key] = true
	}
	if _, err := o.RecordPoACheck(ctx, T, "priya.nair", c.UCC, "", "POA-1", "", map[string]bool{"accounts": true}); err == nil {
		t.Fatal("missing terms without a note accepted")
	}
	p, err := o.RecordPoACheck(ctx, T, "priya.nair", c.UCC, "", "POA-1", "", all)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApprovePoACheck(ctx, T, "priya.nair", p.BOID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker approved: %v", err)
	}
	p, err = o.ApprovePoACheck(ctx, T, "farhan.khan", p.BOID)
	if err != nil || p.Status != "Approved" {
		t.Fatalf("approve: %+v %v", p, err)
	}
}

func TestBlockedClientOrderCancel(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	cs, _ := o.Clients(ctx, T)
	var u string
	for _, c := range cs {
		if blockWhy(c) == "" && c.Status == "Active" && !c.DPOnly {
			u = c.UCC
			break
		}
	}
	if _, err := o.SetBlocked(ctx, T, "anita.rao", u, true, "test: margin shortfall"); err != nil {
		t.Fatal(err)
	}
	all, err := o.OrderCancels(ctx, T, "anita.rao")
	var oc OrderCancel
	for _, x := range all {
		if x.UCC == u {
			oc = x
		}
	}
	if err != nil || oc.Status != OCToCancel {
		t.Fatalf("no item: %+v %v", all, err)
	}
	if oc, err = o.ConfirmOrderCancel(ctx, T, "anita.rao", oc.ID, "No pending orders"); err != nil || oc.Status != OCConfirmed {
		t.Fatalf("confirm: %+v %v", oc, err)
	}
	if _, err := o.CheckOrderCancel(ctx, T, "anita.rao", oc.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("same person checked: %v", err)
	}
	if oc, err = o.CheckOrderCancel(ctx, T, "farhan.khan", oc.ID); err != nil || oc.Status != OCDone {
		t.Fatalf("check: %+v %v", oc, err)
	}
	// The same block does not open a second item.
	o.orderCancelScan(ctx, T, o.now())
	all, _ = o.OrderCancels(ctx, T, "anita.rao")
	n := 0
	for _, x := range all {
		if x.UCC == u {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d items for one block", n)
	}
}

func b6Trade(u, side, sym, tm string, qty int64, px string) ingest.TradeEvent {
	return ingest.TradeEvent{Exchange: "NSE", Segment: "CM", UCC: u, Symbol: sym, InstrumentType: "EQ", Side: side, Qty: qty, Price: money.MustDec(px), TradeDate: D, TradeTime: tm, TradeNo: u + side + tm}
}

func TestTradePatternRules(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "19:00"))
	cs, _ := o.ClientMap(ctx, T)
	res := TradeCheckResult{}
	e := &b6Env{o: o, ctx: ctx, tenant: T, actor: "vikram.shah", date: D, cfg: SurvB6Config{}.withDefaults(), cs: cs, res: &res}
	// Wash: the same client both sides within minutes.
	e.wash([]ingest.TradeEvent{b6Trade("WSH1", "B", "TESTCO", "10:00:00", 100, "50"), b6Trade("WSH1", "S", "TESTCO", "10:02:00", 100, "50.1"), b6Trade("WSH2", "B", "TESTCO", "10:00:00", 100, "50")})
	if res.Raised != 1 {
		t.Fatalf("wash alerts: %d", res.Raised)
	}
	// Bulk: 0.6% of the listed shares.
	if _, err := o.LoadSharesOutstanding(ctx, T, "vikram.shah", "test", []byte("ISIN,Symbol,Shares\nINE000T01019,TESTCO,100000\n")); err != nil {
		t.Fatal(err)
	}
	look := newLookup(Market{Instruments: []Instrument{{Exchange: "NSE", Segment: "CM", Symbol: "TESTCO", Type: "EQ", ISIN: "INE000T01019", Close: money.MustDec("50")}}})
	e.bulk([]ingest.TradeEvent{b6Trade("BLK1", "B", "TESTCO", "11:00:00", 400, "50"), b6Trade("BLK1", "B", "TESTCO", "11:30:00", 200, "50"), b6Trade("BLK2", "B", "OTHER", "11:00:00", 1, "5")}, look)
	if len(res.BulkDeals) != 1 || !strings.Contains(res.BulkDeals[0], "BLK1") || len(res.NotChecked) != 1 {
		t.Fatalf("bulk: %+v", res)
	}
	// Far from the close.
	before := res.Raised
	e.farFromClose([]ingest.TradeEvent{b6Trade("FAR1", "B", "TESTCO", "12:00:00", 10, "60")}, look)
	if res.Raised != before+1 {
		t.Fatal("far-from-close trade not flagged")
	}
	// Expiry-day round trip.
	op := func(side, px string) ingest.TradeEvent {
		x := b6Trade("EXP1", side, "NIFTY", "14:00:00", 1000, px)
		x.Segment, x.InstrumentType, x.Expiry, x.Strike = "FO", "CE", D, "25000"
		return x
	}
	before = res.Raised
	e.expiry([]ingest.TradeEvent{op("B", "10"), op("S", "70")})
	if res.Raised != before+1 {
		t.Fatal("expiry round trip not flagged")
	}
}

func TestBSECCMPenaltyFile(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	file := "Trade Date,Trade No,Scrip Code,Original Client Code,Modified Client Code,Value,Penalty\n" + D + ",9990001,500000,CCMA1,CCMB1,100000,1000\n"
	res, err := o.LoadExchangeFile(ctx, T, "vikram.shah", "9999_CCM_Penalty_"+strings.ReplaceAll(D, "-", "")+".csv", []byte(file), true)
	if err != nil || len(res) != 1 || !strings.Contains(res[0].Loaded, "BSE CCM penalties") {
		t.Fatalf("load: %+v %v", res, err)
	}
	p, _, err := docs.Get[CCMPenalty](ctx, o.Docs, T, cSurvB6, "ccmpen|"+D[:7])
	if err != nil || p.Missing != 1 || p.Total.Cmp(money.MustDec("1000")) != 0 {
		t.Fatalf("penalty: %+v %v", p, err)
	}
}
