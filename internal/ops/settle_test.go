package ops

import (
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// A client's BSE sale and NSE purchase of one ISIN, both cleared at NSE
// Clearing, net into one obligation; with BSE cleared elsewhere they do not.
func TestObligationsNetAcrossNCL(t *testing.T) {
	o := &Ops{}
	isin := "INE000X01011"
	look := newLookup(Market{Instruments: []Instrument{
		{Exchange: "NSE", Segment: "CM", Symbol: "FICT", Type: "EQ", ISIN: isin, Close: money.MustDec("100")},
		{Exchange: "BSE", Segment: "CM", Symbol: "FICT", Type: "EQ", ISIN: isin, Close: money.MustDec("100")},
	}})
	tr := func(ex, side string, q int64) ingest.TradeEvent {
		return ingest.TradeEvent{Exchange: ex, Segment: "CM", UCC: "ZZ01", Symbol: "FICT", InstrumentType: "EQ", Side: side, Qty: q, Price: money.MustDec("100"), Product: "CNC"}
	}
	trades := []ingest.TradeEvent{tr("BSE", "S", 100), tr("NSE", "B", 60), tr("NSE", "B", 40)}
	set := Settings{}
	g, err := o.nettedObligations(t.Context(), T, set, trades, look, map[string]Client{})
	if err != nil {
		t.Fatal(err)
	}
	if len(g) != 1 {
		t.Fatalf("obligations = %+v", g)
	}
	for k, ob := range g {
		if k != "ZZ01|NCL|"+isin || ob.NetQty != 0 || ob.Clearing != "NCL" || strings.Join(ob.Exchanges, "+") != "BSE+NSE" {
			t.Fatalf("netted %s %+v", k, ob)
		}
	}
	trades = trades[:2]
	g, _ = o.nettedObligations(t.Context(), T, set, trades, look, map[string]Client{})
	ob := g["ZZ01|NCL|"+isin]
	if ob == nil || ob.NetQty != -40 || ob.Exchange != "BSE" {
		t.Fatalf("net sale %+v", g)
	}
	// BSE cleared by another clearing member: no netting with NSE.
	set.ClearingFor = map[string]string{"BSE-CM": "CM:9999"}
	g, _ = o.nettedObligations(t.Context(), T, set, trades, look, map[string]Client{})
	if len(g) != 2 || g["ZZ01|BSE|"+isin] == nil || g["ZZ01|NSE|"+isin].NetQty != 60 {
		t.Fatalf("not netted %+v", g)
	}
}

// Every client with funds is allocated, whatever the trading status; risk
// itself still shows active clients only.
func TestAllocationCoversInactiveClients(t *testing.T) {
	o, ctx := setup(t)
	funds, _ := o.clientFunds(ctx, T, D, "")
	ucc := ""
	for _, u := range sortedKeys(funds) {
		if funds[u] > 0 {
			ucc = u
			break
		}
	}
	if ucc == "" {
		t.Fatal("no client in credit in the sample")
	}
	if _, err := docs.Update(ctx, o.Docs, T, cClient, ucc, "admin", func(c *Client) error { c.Status = "Dormant"; return nil }); err != nil {
		t.Fatal(err)
	}
	rv, _ := o.Risk(ctx, T, D)
	for _, r := range rv.Rows {
		if r.UCC == ucc {
			t.Fatal("a dormant client in risk")
		}
	}
	al, err := o.Allocation(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	var cash money.Dec
	for _, a := range al {
		if a.UCC == ucc {
			cash = cash.Add(a.Cash)
		}
	}
	if cash.Sign() <= 0 {
		t.Fatalf("dormant client %s with ₹%s credit not allocated", ucc, funds[ucc])
	}
}

// The morning's internal shortage and its noon funding, then the auction:
// the buyer gets the auctioned shares instead of cash, and the seller
// gains nothing.
func TestInternalShortageAuction(t *testing.T) {
	o, ctx := setup(t)
	st, _ := o.Obligations(ctx, T, D)
	seller, buyer := obl(t, st, "BH1013", "NSE", "BAJFINANCE"), obl(t, st, "BH1011", "NSE", "BAJFINANCE")
	if seller.Clearing != "NCL" || seller.Status != "Short delivery" {
		t.Fatalf("seller %+v", seller)
	}
	v, err := o.InternalShortages(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	var row InternalShortRow
	for _, r := range v.Rows {
		if r.ISIN == seller.ISIN {
			row = r
		}
	}
	if row.Internal != 47 || row.Valuation.Sign() <= 0 || v.Fund.Cmp(v.Estimate) != 0 || v.PayIn != st.PayIn {
		t.Fatalf("internal shortage %+v %+v", row, v)
	}
	want := row.Price.MulInt(47).Pct(money.DecInt(120)).Round(money.UnitPaisa).Dec()
	if row.Valuation.Cmp(want) != 0 {
		t.Fatalf("valuation %s, want %s", row.Valuation, want)
	}
	// The morning work item, before noon on the settlement day.
	o.Now = func() time.Time { return time.Date(2026, 10, 19, 9, 0, 0, 0, ist) }
	if set, _ := o.Settings(ctx, T); set.NextSettlementDay(D) != "2026-10-19" {
		t.Skip("sample settlement day moved")
	}
	o.internalShortageTick(ctx, T, o.now())
	w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-internal_shortage-fund-2026-10-19")
	if err != nil || w.Status != "Open" || !strings.Contains(w.Title, "12:00") {
		t.Fatalf("funding work item %+v %v", w, err)
	}
	// NSE Clearing's CM_SA_S replaces the estimate.
	loadX(t, o, "CM_SA_S_90001_19102026.csv", []byte("Settlement No,Symbol,Series,ISIN,Internal Shortage Qty,Valuation Price,Valuation Amount\n2026201,BAJFINANCE,EQ,"+seller.ISIN+",47,1300.00,61100.00\n"))
	v, _ = o.InternalShortages(ctx, T, D)
	if !v.NCLLoaded || v.Fund.String() != "61100" {
		t.Fatalf("NSE Clearing figure %+v", v)
	}

	if _, err := o.SettleAction(ctx, T, "priya.nair", D, seller.Key, "report_short"); err != nil {
		t.Fatal(err)
	}
	st, _ = o.Obligations(ctx, T, D)
	if b := obl(t, st, "BH1011", "NSE", "BAJFINANCE"); b.Status != "Awaiting auction" {
		t.Fatalf("buyer %+v", b)
	}
	ss, _ := o.Shortages(ctx, T)
	sh := ss[0]
	before, _ := o.clientFunds(ctx, T, "", "")
	h0, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID("BH1011", buyer.ISIN))
	// An auction price below the settlement price: the seller still pays
	// the settlement price.
	low := sh.SettlePrice.Sub(money.MustDec("10"))
	if _, err := o.SettleShortageWith(ctx, T, "farhan.khan", sh.ID, ShortageClose{Date: D, Received: 47}); err == nil {
		t.Fatal("received shares without an auction price")
	}
	got, err := o.SettleShortageWith(ctx, T, "farhan.khan", sh.ID, ShortageClose{Date: D, Received: 47, AuctionPrice: low})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := o.clientFunds(ctx, T, "", "")
	if d := (before["BH1013"] - after["BH1013"]).Dec(); d.Cmp(sh.SettlePrice.MulInt(47).Round(money.UnitPaisa).Dec()) != 0 {
		t.Fatalf("seller debited %s, want the settlement price × 47 (%s)", d, sh.SettlePrice)
	}
	if after["BH1011"] != before["BH1011"] {
		t.Fatal("buyer cashed out though the shares came in the auction")
	}
	h1, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID("BH1011", buyer.ISIN))
	if wq(h1.Qty)-wq(h0.Qty) != buyer.NetQty {
		t.Fatalf("buyer received %d, want %d", wq(h1.Qty)-wq(h0.Qty), buyer.NetQty)
	}
	if b, c, d := shortageQtys(got); b != 47 || c != 0 || d != 0 || got.Buyers[0].Received != 47 {
		t.Fatalf("report quantities %d %d %d %+v", b, c, d, got)
	}
	tb, _ := o.App.Ledger.TrialBalance(ctx, T, "")
	if !tb.Balanced {
		t.Fatal("trial balance")
	}
}

// Intraday short allocation is loaded per snapshot and raised at once;
// the allocation's acceptance is checked after 22:00.
func TestIntradayShortAllocationAndAllocationBy22(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	u := cs[0].UCC
	loadX(t, o, "F_SA01_P_90001_16102026_i02.lis.gz", xwrite(t, "ncl-sa01", xr("", "Date", "16-Oct-2026", "ClientCode", u, "MinimumMarginRequired", "50000.00", "CashCollateral", "20000.00", "NonCashCollateral", "10000.00", "TotalCollateral", "30000.00", "ShortAllocation", "20000.00")))
	sec := ccSection(t, o, "short_allocation_intraday")
	if !sec.Loaded || len(sec.Rows) != 1 || sec.Rows[0][1] != "FO" || sec.Rows[0][2] != "02" || sec.Diffs != 1 {
		t.Fatalf("section %+v", sec)
	}
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-short_allocation-intraday-"+D+"-FO-"+u); err != nil || w.Role != RoleRisk {
		t.Fatalf("work item %+v %v", w, err)
	}

	// Without the API and with no allocation file made: raised after 22:00.
	if why := o.AllocationNotAccepted(ctx, T, D); len(why) == 0 {
		t.Fatal("allocation file not made, yet accepted")
	}
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 21, 59, 0, 0, ist) }
	o.allocAcceptTick(ctx, T, o.now())
	if _, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-allocation-accept-"+D); err == nil {
		t.Fatal("raised before 22:00")
	}
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 22, 5, 0, 0, ist) }
	o.allocAcceptTick(ctx, T, o.now())
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-allocation-accept-"+D); err != nil || w.Status != "Open" {
		t.Fatalf("22:00 work item %+v %v", w, err)
	}
}

// The pay-out reconciliation explains a pay-out to the pool, and the
// 24-hour test fails it.
func TestPayoutReconciliation(t *testing.T) {
	o, ctx := setup(t)
	st, _ := o.Obligations(ctx, T, D)
	var buy Obligation
	for _, ob := range st.Obligations {
		if ob.NetQty > 0 && !strings.HasPrefix(ob.ISIN, "UNKNOWN") {
			buy = ob
			break
		}
	}
	if buy.UCC == "" {
		t.Fatal("no purchase in the sample")
	}
	set, _ := o.Settings(ctx, T)
	pool := "1208160000000099"
	set.FirmDemats = map[string]string{pool: "POOL"}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	row := map[string]string{"DueDt": st.PayIn, "ClntId": buy.UCC, "ISIN": buy.ISIN, "TckrSymb": buy.Symbol, "QtyORShrtQty": "1", "TrgtDpstryAcct": pool}
	if err := docs.Upsert(ctx, o.Docs, T, cCCData, "directpayout|"+st.PayIn+"|CM", CCData{ID: "directpayout|" + st.PayIn + "|CM", Kind: "directpayout", Date: st.PayIn, Segment: "CM", File: "12345.txt", Rows: []map[string]string{row}}, "admin"); err != nil {
		t.Fatal(err)
	}
	rc, err := o.ReconcilePayout(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	var got PayoutReconRow
	for _, r := range rc.Rows {
		if r.UCC == buy.UCC && r.ISIN == buy.ISIN {
			got = r
		}
	}
	if got.OK || got.Pool != 1 || !strings.Contains(got.Reason, "POOL") || !strings.Contains(got.Action, "pool") {
		t.Fatalf("recon row %+v", got)
	}
	if n := o.RaisePayoutMismatches(ctx, T, D); n == 0 {
		t.Fatal("no work item for the pool")
	}
	e := &siEnv{o: o, ctx: ctx, tenant: T, date: "2026-10-22", set: set, clients: map[string]Client{}}
	s, err := siPayoutSecurities(e)
	if err != nil || s.found == 0 {
		t.Fatalf("payout_securities_24h %+v %v", s, err)
	}
}

// Transfer requests are timed at one or three working days.
func TestSecuritiesTransferRequest(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	in := SecTransferInput{Date: D, UCC: cs[0].UCC, ISIN: "INE000X01011", Qty: 5, Where: "Firm", Evidence: "email of 16 Oct"}
	if _, err := o.RequestSecTransfer(ctx, T, "anita.rao", in); err == nil {
		t.Fatal("risk manager recorded a transfer request")
	}
	x, err := o.RequestSecTransfer(ctx, T, "priya.nair", in)
	if err != nil || x.Due != "2026-10-19" {
		t.Fatalf("request %+v %v", x, err)
	}
	in.Where = "Clearing"
	y, _ := o.RequestSecTransfer(ctx, T, "priya.nair", in)
	if y.Due != "2026-10-21" {
		t.Fatalf("3 working days: %s", y.Due)
	}
	if _, err := o.FinishSecTransfer(ctx, T, "farhan.khan", x.ID, true, "", ""); err == nil {
		t.Fatal("done without a reference")
	}
	o.Now = func() time.Time { return time.Date(2026, 10, 20, 11, 0, 0, 0, ist) }
	if x, err = o.FinishSecTransfer(ctx, T, "farhan.khan", x.ID, true, "CDSL-TXN-1", ""); err != nil || x.Status != "Done" {
		t.Fatalf("finish %+v %v", x, err)
	}
	set, _ := o.Settings(ctx, T)
	s, _ := siSecTransfers(&siEnv{o: o, ctx: ctx, tenant: T, date: "2026-10-22", set: set})
	if s.checked != 2 || s.found != 2 { // one done late, one still open past its due date
		t.Fatalf("test %+v", s)
	}
}

// A closure request blocks the client, waits for the pay-out, and the
// letter goes only once the books show it.
func TestAccountClosure(t *testing.T) {
	o, ctx := setup(t)
	funds, _ := o.clientFunds(ctx, T, "", "")
	cs, _ := o.Clients(ctx, T)
	var inCredit, clear string
	for _, c := range cs {
		if c.Status != "Active" {
			continue
		}
		b := o.closureBooks(ctx, T, c.UCC, D)
		switch {
		case inCredit == "" && funds[c.UCC] > 0 && b.positions == 0 && b.debit.IsZero():
			inCredit = c.UCC
		case clear == "" && funds[c.UCC] == 0 && b.positions == 0 && len(b.pledged) == 0 && b.cuspa == 0 && b.payouts == 0 && b.allocated.IsZero():
			clear = c.UCC
		}
	}
	if inCredit == "" || clear == "" {
		t.Skip("sample has no client to close")
	}
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 15, 0, 0, 0, ist) }
	x, err := o.RequestClosure(ctx, T, "priya.nair", inCredit, D, "Client", "")
	if err != nil {
		t.Fatal(err)
	}
	if x.Due != "2026-10-21" || x.PaidOutOn != "" || len(x.Holds) == 0 {
		t.Fatalf("closure %+v", x)
	}
	if c, _ := o.Client(ctx, T, inCredit); !c.Blocked {
		t.Fatal("client not blocked on the request")
	}
	if _, err := o.CompleteClosure(ctx, T, "farhan.khan", x.ID); err == nil {
		t.Fatal("closed with a credit balance")
	}
	if _, err := o.RequestClosure(ctx, T, "priya.nair", inCredit, D, "Client", ""); err == nil {
		t.Fatal("second open request")
	}
	y, err := o.RequestClosure(ctx, T, "priya.nair", clear, D, "Online", "")
	if err != nil || y.PaidOutOn == "" || y.ReleasedOn == "" {
		t.Fatalf("clear client %+v %v", y, err)
	}
	if y, err = o.CompleteClosure(ctx, T, "farhan.khan", y.ID); err != nil || y.Status != "Closed" {
		t.Fatalf("complete %+v %v", y, err)
	}
	if c, _ := o.Client(ctx, T, clear); len(c.OpenSegments()) != 0 {
		t.Fatalf("segments still open: %v", c.OpenSegments())
	}
	set, _ := o.Settings(ctx, T)
	s, _ := siClosure3WD(&siEnv{o: o, ctx: ctx, tenant: T, date: "2026-10-23", set: set})
	if s.checked != 2 || s.found != 1 {
		t.Fatalf("closure test %+v", s)
	}
	// A segment closed with credit left: the letter is held.
	if _, err := o.SetSegmentStatus(ctx, T, "farhan.khan", inCredit, "CM", SegClosed, D, "test closure"); err != nil {
		t.Fatal(err)
	}
}

// MCXCCL's margin shortage file gives the levied penalty when no NCMR or
// UDiFF penalty file is loaded.
func TestMCXMarginShortage(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	u := cs[0].UCC
	loadX(t, o, "MCX_MARGINSHORTAGE_20261016_.E01", xwrite(t, "mcx-margin-shortage", xr("", "Date", "16OCT2026", "TM/CP ID", "90001", "Client ID", u, "Initial Margin", "10000.00", "Other Margin", "0.00", "MTM", "0.00",
		"Amount of Shortfall of Margins", "2000.00", "Penalty Levied", "100.00", "MTM Collected", "0", "Initial Margin Collected", "8000.00", "Other Margin Collected", "0", "Peak Margin Threshold %", "100", "Peak Margin", "10000", "Peak Margin Collected", "8000", "Peak Margin Shortfall", "2000",
		"Intra-day Short Allocation", "0", "EOD Short Allocation", "0", "Intra-day Short Allocation Reported", "0", "Reserved", "", "Intra-day Allocation Shortfall", "0", "EOD Short Allocation Reported", "0", "EOD Allocation Shortfall", "0")))
	l := o.mcxLevied(ctx, T, D)[u]
	if l.Penalty.String() != "100" || l.Short.String() != "2000" {
		t.Fatalf("levied %+v", l)
	}
	if s := ccSection(t, o, "mcx_margin_shortage"); !s.Loaded || s.Diffs != 1 {
		t.Fatalf("section %+v", s)
	}
}

// Early pay-ins not sent by the end of the trade day lapse.
func TestEPILapsesAtEndOfDay(t *testing.T) {
	o, ctx := setup(t)
	r := EPIRecord{ID: D + "|BH1001|INE000X01011|1", Date: D, UCC: "BH1001", ISIN: "INE000X01011", Qty: 1, Status: "Pending", Maker: "priya.nair"}
	if err := docs.Upsert(ctx, o.Docs, T, cEPI, r.ID, r, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	o.Now = func() time.Time { return time.Date(2026, 10, 19, 10, 0, 0, 0, ist) }
	if _, err := o.DecideEPI(ctx, T, "farhan.khan", []string{r.ID}, true, ""); err == nil || !strings.Contains(err.Error(), "trade day only") {
		t.Fatalf("sent a day late: %v", err)
	}
	if lapsed, _ := o.EPIEndOfDay(ctx, T, SystemActor, D); lapsed != 1 {
		t.Fatalf("lapsed %d", lapsed)
	}
	x, _, _ := docs.Get[EPIRecord](ctx, o.Docs, T, cEPI, r.ID)
	if x.Status != "Lapsed" {
		t.Fatal(x.Status)
	}
}
