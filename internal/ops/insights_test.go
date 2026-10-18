package ops

import (
	"fmt"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

func TestCapTax(t *testing.T) {
	cases := []struct{ st, lt, want string }{
		{"100000", "200000", "30550"}, // 20,000 + 12.5% of 75,000 = 29,375, + 4% cess
		{"-50000", "200000", "3250"},  // short-term loss set off against long-term gain: 12.5% of 25,000 + cess
		{"0", "100000", "0"},          // within the ₹1.25 lakh exemption
		{"10000", "-80000", "2080"},   // long-term loss cannot reduce short-term gain
	}
	for _, c := range cases {
		if got := capTax(money.MustDec(c.st), money.MustDec(c.lt)); got.Cmp(money.MustDec(c.want)) != 0 {
			t.Errorf("capTax(%s, %s) = %s, want %s", c.st, c.lt, got, c.want)
		}
	}
	if l, f, e := fyOf("2027-02-10"); l != "2026-27" || f != "2026-04-01" || e != "2027-03-31" {
		t.Fatalf("fyOf: %s %s %s", l, f, e)
	}
	if l, _, _ := fyOf("2026-04-01"); l != "2026-27" {
		t.Fatalf("fyOf april: %s", l)
	}
	if got := lastTradeDayIn(DefaultSettings(), "2027-03-31"); got != "2027-03-30" {
		t.Fatalf("last trade day: %s", got)
	}
	if entitlement(250, "13/100") != 32 || entitlement(10, "1:1") != 10 || entitlement(10, "x") != 0 {
		t.Fatal("entitlement")
	}
	if !inWindow("2027-01-20", "01-15", "03-31") || inWindow("2026-12-20", "01-15", "03-31") || !inWindow("2026-12-20", "12-01", "01-31") {
		t.Fatal("window")
	}
}

func TestTaxHarvestAndOutcome(t *testing.T) {
	o, ctx := setup(t)
	m, _, err := o.Market(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	var eq []Instrument
	for _, i := range m.Instruments {
		if i.Exchange == "NSE" && i.Segment == "CM" && i.Type == "EQ" && i.ISIN != "" && i.LTP.Sign() > 0 {
			eq = append(eq, i)
		}
	}
	if len(eq) < 2 {
		t.Fatal("sample has no equities")
	}
	gain, loss := eq[0], eq[1]
	c, _ := o.Client(ctx, T, "BH1001")
	c.UCC, c.Name, c.Email = "BH9", "Test Harvester", "harvest@mail.example"
	if err := docs.Upsert(ctx, o.Docs, T, cClient, "BH9", c, "test"); err != nil {
		t.Fatal(err)
	}
	half, _ := gain.LTP.Quo(money.DecInt(2))
	lossCost := loss.LTP.Add(loss.LTP.Pct(money.DecInt(50)))
	book := LotBook{UCC: "BH9", AsOf: addDays(D, -1), Lots: []Lot{
		{ISIN: gain.ISIN, Symbol: gain.Symbol, Qty: 100, Date: "2026-03-02", Price: half},
		{ISIN: loss.ISIN, Symbol: loss.Symbol, Qty: 50, Date: "2026-02-02", Price: lossCost},
	}}
	if err := docs.Upsert(ctx, o.Docs, T, cLot, "BH9", book, "test"); err != nil {
		t.Fatal(err)
	}
	dmy := func(d string) string { // 2026-10-16 -> 16-Oct-2026
		tm := map[string]string{"10": "Oct", "11": "Nov"}
		return d[8:] + "-" + tm[d[5:7]] + "-" + d[:4]
	}
	trade := func(no int, sym, date string, qty int64, px money.Dec) {
		csv := "TradeNo,OrderNo,ClientCode,Symbol,Series,BuySell,Qty,Price,TradeDate,TradeTime,Product,AlgoId,TerminalId\n" +
			fmt.Sprintf("%d,%d,BH9,%s,EQ,2,%d,%s,%s,10:00:00,CNC,,T1\n", 990000+no, 880000+no, sym, qty, px.Fixed(2), dmy(date))
		if _, err := o.App.IngestFile(ctx, T, "priya.nair", "NSE-CM-TRADE", "", fmt.Sprintf("bh9-%d.csv", no), []byte(csv)); err != nil {
			t.Fatal(err)
		}
	}
	trade(1, gain.Symbol, D, 100, gain.LTP) // books short-term gain of 100 x (LTP - LTP/2)
	p, err := o.TaxPlan(ctx, T, "BH9", D)
	if err != nil {
		t.Fatal(err)
	}
	wantST := gain.LTP.Sub(half).MulInt(100)
	wantLoss := lossCost.Sub(loss.LTP).MulInt(50)
	if p.STCG.Cmp(wantST) != 0 || p.STLoss.Cmp(wantLoss) != 0 || len(p.Losses) != 1 || p.FY != "2026-27" {
		t.Fatalf("plan: stcg %s (want %s) stloss %s (want %s) losses %d", p.STCG, wantST, p.STLoss, wantLoss, len(p.Losses))
	}
	if want := capTax(wantST, money.Dec{}).Sub(capTax(wantST.Sub(wantLoss), money.Dec{})); p.Saving.Cmp(want) != 0 || p.Saving.Sign() <= 0 {
		t.Fatalf("saving %s want %s", p.Saving, want)
	}

	// Outside the window nothing is raised; widen the window to test.
	if _, err := o.RunStage(ctx, T, SystemActor, D, "", "ins_tax_harvest"); err != nil {
		t.Fatal(err)
	}
	if v, _ := o.Insights(ctx, T, D, nil); v.Counts["live:tax_harvest"] != 0 {
		t.Fatal("raised outside the window")
	}
	if _, err := o.SetAutoPolicy(ctx, T, "admin", AutoPolicy{ID: "ins_tax_harvest", Mode: ModeAuto, Params: map[string]string{"from": "10-01", "to": "10-31"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RunStage(ctx, T, SystemActor, D, "", "ins_tax_harvest"); err != nil {
		t.Fatal(err)
	}
	in, err := o.InsightByID(ctx, T, insightID("tax_harvest", "BH9", "FY2026-27"))
	if err != nil || in.Status != InsSent || !contains(in.Symbols, loss.Symbol) || in.Facts["saving"] == "" || !contains(in.Channels, ChEmail) {
		t.Fatalf("insight: %+v %v", in, err)
	}
	ms, _ := docs.List[Message](ctx, o.Docs, T, cMessage, "")
	found := false
	for _, x := range ms {
		found = found || (x.UCC == "BH9" && x.Event == "insight.tax_harvest" && strings.Contains(x.Body, "not tax or investment advice"))
	}
	if !found {
		t.Fatal("no tax message with the disclaimer")
	}
	// Running again does not message twice.
	runs, _ := o.RunStage(ctx, T, SystemActor, D, "", "ins_tax_harvest")
	if runs[0].Done != 0 {
		t.Fatalf("sent again: %+v", runs[0])
	}
	// A relationship manager logs a call; a risk manager cannot.
	if _, err := o.ActOnInsight(ctx, T, "anita.rao", in.ID, "call", "Interested", ""); err == nil {
		t.Fatal("risk manager worked an insight")
	}
	if in, err = o.ActOnInsight(ctx, T, "priya.nair", in.ID, "call", "Will act", "Selling next week"); err != nil || in.Status != InsCalled || len(in.Calls) != 1 {
		t.Fatalf("call: %+v %v", in.Status, err)
	}
	// Outcome: selling the loss shares the next day counts as acting on it.
	next := addDays(D, 3) // 19-Oct-2026, a Monday
	trade(2, loss.Symbol, next, 50, loss.LTP)
	if _, err := o.RunStage(ctx, T, SystemActor, next, "", "ins_outcomes"); err != nil {
		t.Fatal(err)
	}
	in, _ = o.InsightByID(ctx, T, in.ID)
	if in.Status != InsActed || in.ActedOn != next || in.Trades != 1 || in.Turnover.Cmp(loss.LTP.MulInt(50)) != 0 {
		t.Fatalf("outcome: %s %s %d %s", in.Status, in.ActedOn, in.Trades, in.Turnover)
	}
	v, _ := o.Insights(ctx, T, next, nil)
	if v.Acted != 1 || v.Turnover.Sign() <= 0 {
		t.Fatalf("view: acted %d turnover %s", v.Acted, v.Turnover)
	}
}

func TestBuyBackInsight(t *testing.T) {
	o, ctx := setup(t)
	hs, _ := o.Holdings(ctx, T, "")
	_, look, _ := o.Market(ctx, T, D)
	var h Holding
	var ltp money.Dec
	for _, x := range hs {
		if i, ok := look.ByISIN(x.ISIN); ok && x.Qty-x.Pledged >= 10 && i.LTP.Sign() > 0 {
			h, ltp = x, i.LTP
			break
		}
	}
	if h.UCC == "" {
		t.Fatal("no holding to test with")
	}
	price := ltp.Add(ltp.Pct(money.DecInt(20)))
	if _, err := o.SaveBuyBack(ctx, T, "priya.nair", BuyBack{ISIN: h.ISIN, Symbol: h.Symbol, Company: "Test Ltd", Price: price, Record: D, Open: D, Close: addDays(D, 10), SmallRatio: "bad"}); err == nil {
		t.Fatal("bad ratio accepted")
	}
	b, err := o.SaveBuyBack(ctx, T, "priya.nair", BuyBack{ISIN: h.ISIN, Symbol: h.Symbol, Company: "Test Ltd", Price: price, Record: D, Open: D, Close: addDays(D, 10), SmallRatio: "1/2", GeneralRatio: "1/10"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.RunStage(ctx, T, SystemActor, D, "", "ins_buyback"); err != nil {
		t.Fatal(err)
	}
	in, err := o.InsightByID(ctx, T, insightID("buyback", h.UCC, b.ID))
	if err != nil || in.Status != InsSent || in.Value.Sign() <= 0 || !strings.Contains(in.Title, "20.0%") {
		t.Fatalf("buyback insight: %+v %v", in, err)
	}
	// The client sees it in the portal and tenders there.
	user, _, err := o.PortalAccess(ctx, T, "farhan.khan", h.UCC, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	pv, err := o.ClientInsights(ctx, T, user, D, h.UCC)
	if err != nil || len(pv.BuyBacks) != 1 || pv.BuyBacks[0].Entitlement <= 0 || len(pv.Items) == 0 {
		t.Fatalf("portal: %+v %v", pv.BuyBacks, err)
	}
	if _, err := o.ClientTenderBuyBack(ctx, T, user, D, b.ID, "BH1999", 1); err == nil {
		t.Fatal("tendered for another account")
	}
	if _, err := o.ClientTenderBuyBack(ctx, T, user, D, b.ID, h.UCC, pv.BuyBacks[0].Entitlement); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RunStage(ctx, T, SystemActor, D, "", "ins_outcomes"); err != nil {
		t.Fatal(err)
	}
	if in, _ = o.InsightByID(ctx, T, in.ID); in.Status != InsActed {
		t.Fatalf("tender not counted: %s", in.Status)
	}
	// Insights off: nothing more is sent to the client.
	if _, err := o.SetInsightsOff(ctx, T, user, h.UCC, true); err != nil {
		t.Fatal(err)
	}
	b2, _ := o.SaveBuyBack(ctx, T, "priya.nair", BuyBack{ISIN: h.ISIN, Symbol: h.Symbol, Price: price, Record: D, Open: D, Close: addDays(D, 10)})
	_, _ = o.RunStage(ctx, T, SystemActor, D, "", "ins_buyback")
	in2, _ := o.InsightByID(ctx, T, insightID("buyback", h.UCC, b2.ID))
	if in2.Status != InsOpen || !strings.Contains(in2.Held, "turned insights off") {
		t.Fatalf("opted-out client: %s %q", in2.Status, in2.Held)
	}
	if _, err := o.ActOnInsight(ctx, T, "priya.nair", in2.ID, "send", "", ""); err == nil {
		t.Fatal("sent to an opted-out client")
	}
}

func TestInsightsSampleDay(t *testing.T) {
	o, ctx := setup(t)
	n, err := o.FindInsights(ctx, T, "priya.nair", D)
	if err != nil {
		t.Fatal(err)
	}
	v, err := o.Insights(ctx, T, D, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("found %d: %v", n, v.Counts)
	for _, x := range v.Items {
		if x.Status != InsOpen || x.SentAt != "" {
			t.Fatalf("Find sent a message: %+v", x)
		}
	}
	if _, err := o.FindInsights(ctx, T, "anita.rao", D); err == nil {
		t.Fatal("risk manager ran insights")
	}
}

func TestInsightsOnWhatsApp(t *testing.T) {
	o, ctx := setup(t)
	ts, _ := o.templates(ctx, T)
	wa := map[string]bool{}
	for _, x := range ts {
		if x.Channel == ChWhatsApp && strings.HasPrefix(x.Event, "insight.") {
			wa[x.Event] = x.WAName != ""
		}
	}
	for _, k := range InsightKinds {
		if !wa["insight."+k.ID] {
			t.Errorf("no approved WhatsApp template for %s", k.ID)
		}
	}
	c, _ := o.Client(ctx, T, "BH1001")
	o.DevCodes = true
	r0, _ := o.WhatsAppInbound(ctx, T, c.Mobile, "TAX", D)
	if !strings.Contains(r0, "the code is ") {
		t.Fatalf("no code asked: %s", r0)
	}
	_, _ = o.WhatsAppInbound(ctx, T, c.Mobile, r0[strings.Index(r0, "the code is ")+12:][:6], D)
	for _, cmd := range []string{"TAX", "OFFERS", "HELP"} {
		reply, err := o.WhatsAppInbound(ctx, T, c.Mobile, cmd, D)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s -> %s", cmd, reply)
		if cmd == "TAX" && !strings.Contains(reply, "Gains booked") {
			t.Fatalf("TAX: %s", reply)
		}
	}
}
