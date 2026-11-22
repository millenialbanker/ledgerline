package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// Made-up securities for the firm's own books (not real ISINs).
const (
	fsX  = "INE000X01011"
	fsY  = "INE000Y01012"
	fsBD = "INE000Z07013" // a bond
	fsUN = "INE000U01014" // unlisted
)

func amtOf(s string) money.Amount { return money.MustAmount(s) }

// fsFirm is the sample firm with both portfolios, PRO trades going to
// stock-in-trade, and the practice given.
func fsFirm(t *testing.T, fn func(*FSSettings)) (*Ops, context.Context) {
	t.Helper()
	o, ctx := setup(t)
	if err := o.saveFS(ctx, T, "admin", func(s *FSSettings) error {
		s.Portfolios, s.SITCodes = FSBoth, []string{"PRO"}
		if fn != nil {
			fn(s)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return o, ctx
}

// fsMarket stores a day's closing prices for made-up securities.
func fsMarket(t *testing.T, o *Ops, ctx context.Context, date string, prices map[string]string) {
	t.Helper()
	m, _, _ := docs.GetOr(ctx, o.Docs, T, cMarket, date, Market{Date: date})
	for isin, p := range prices {
		m.Instruments = append(m.Instruments, Instrument{Exchange: "NSE", Segment: "CM", Symbol: "S" + isin[7:10], Type: "EQ", ISIN: isin, Close: money.MustDec(p), LTP: money.MustDec(p), Lot: 1})
	}
	if err := docs.Upsert(ctx, o.Docs, T, cMarket, date, m, "t"); err != nil {
		t.Fatal(err)
	}
}

func fsBalanced(t *testing.T, o *Ops, ctx context.Context) {
	t.Helper()
	tb, err := o.App.Ledger.TrialBalance(ctx, T, "9999-12-31")
	if err != nil || !tb.Balanced || tb.TotalDebit != tb.TotalCredit {
		t.Fatalf("books not balanced: %v %s %s", err, tb.TotalDebit, tb.TotalCredit)
	}
}

func fsBal(t *testing.T, o *Ops, ctx context.Context, acct string) money.Amount {
	t.Helper()
	b, err := o.App.Ledger.Balances(ctx, T, "9999-12-31")
	if err != nil {
		t.Fatal(err)
	}
	return b[acct]
}

func fsEntry(t *testing.T, o *Ops, ctx context.Context, actor string, in FSEntryInput) FSTxn {
	t.Helper()
	x, err := o.AddFirmSecEntry(ctx, T, actor, in)
	if err != nil {
		t.Fatalf("entry %+v: %v", in, err)
	}
	return x
}

func TestFSPracticesSection(t *testing.T) {
	o, ctx := setup(t)
	v, err := o.FirmPractices(ctx, T, "admin")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, q := range v.Questions {
		if q.Section == SectionFirmSecurities {
			n++
			if q.ID != "fs-portfolios" && q.Applies {
				t.Fatalf("%s applies before the firm keeps a portfolio", q.ID)
			}
		}
	}
	if n != len(fsPracticeDefs) || v.Total != len(practiceDefs) {
		t.Fatalf("questions %d of %d, total %d", n, len(fsPracticeDefs), v.Total)
	}
	// Default: no securities, nothing to answer but the first.
	for _, it := range o.practiceChecklist(ctx, T) {
		if strings.HasPrefix(it.Key, "practice_fs-") && it.Key != "practice_fs-portfolios" {
			t.Fatalf("checklist asks %s before the firm keeps securities", it.Key)
		}
	}
	// Where the securities are kept affects money: a second approver.
	q, err := o.SetFirmPractice(ctx, T, "admin", "fs-portfolios", PracticeAnswer{Option: "sit"})
	if err != nil || q.Pending == nil || o.FirmSecSettings(ctx, T).Portfolios != FSNone {
		t.Fatalf("proposed: %+v %v", q.Pending, err)
	}
	if _, err := o.DecideFirmPractice(ctx, T, "admin", "fs-portfolios", true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("own approval: %v", err)
	}
	if _, err := o.DecideFirmPractice(ctx, T, "vikram.shah", "fs-portfolios", true, ""); err != nil {
		t.Fatal(err)
	}
	if s := o.FirmSecSettings(ctx, T); s.Portfolios != "sit" || !s.has(PortSIT) || s.has(PortInv) {
		t.Fatalf("settings %+v", s)
	}
	// Trading codes are a field of the default-portfolio question.
	if _, err := o.SetFirmPractice(ctx, T, "admin", "fs-default", PracticeAnswer{Option: "change", Values: map[string]string{"sit_codes": "pro, own1"}}); err != nil {
		t.Fatal(err)
	}
	if q, _ := o.practiceView(ctx, T, "admin", "fs-default"); q.Pending != nil {
		if _, err := o.DecideFirmPractice(ctx, T, "vikram.shah", "fs-default", true, ""); err != nil {
			t.Fatal(err)
		}
	}
	if s := o.FirmSecSettings(ctx, T); strings.Join(s.SITCodes, ",") != "PRO,OWN1" || s.codePortfolio("own1") != PortSIT {
		t.Fatalf("codes %v", s.SITCodes)
	}
	// Recorded-at-once questions apply without a second person.
	if _, err := o.SetFirmPractice(ctx, T, "admin", "fs-demat-check", PracticeAnswer{Option: "monthly"}); err != nil || o.FirmSecSettings(ctx, T).DematCheck != "monthly" {
		t.Fatalf("demat check: %v", err)
	}
	// An instrument type of the firm's own is asked how it is treated.
	if _, err := o.SetFirmPractice(ctx, T, "admin", "fs-instruments", PracticeAnswer{Option: "all", Values: map[string]string{"extra": "Commercial paper"}}); err != nil {
		t.Fatal(err)
	}
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-firmsec-kind-X_COMMERCIAL_PAPER"); w.Status == "Done" || w.Title == "" {
		t.Fatalf("no work item for the firm's own type: %+v", w)
	}
	if _, err := o.SetFSKind(ctx, T, "admin", FSKind{ID: "X_COMMERCIAL_PAPER", Category: "debt", Tax: "listed", Listed: false}); err != nil {
		t.Fatal(err)
	}
	if k := o.FirmSecSettings(ctx, T).kind("X_COMMERCIAL_PAPER"); k.Category != "debt" || !k.Asked {
		t.Fatalf("kind %+v", k)
	}
	if _, err := o.SetFSKind(ctx, T, "priya.nair", FSKind{ID: "X_COMMERCIAL_PAPER", Category: "debt", Tax: "listed"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker set a kind: %v", err)
	}
}

func TestFSIndASWarning(t *testing.T) {
	s := DefaultFSSettings()
	s.Portfolios, s.Standard, s.Closing = FSBoth, "indas", "cost"
	w := s.fsWarnings()
	if len(w) != 2 || !strings.Contains(w[0], "fair value through profit or loss") {
		t.Fatalf("warnings %v", w)
	}
	s.Closing, s.InvValue = "fair", "fair"
	if w := s.fsWarnings(); len(w) != 0 {
		t.Fatalf("fair value under Ind AS: %v", w)
	}
	s.Standard = "as"
	if w := s.fsWarnings(); len(w) != 2 {
		t.Fatalf("fair value under AS: %v", w)
	}
}

// TestFSReplayFIFOAndCost: first in, first out, perpetual; the cost of a
// lot by the firm's practice, head by head; postings balance.
func TestFSReplayFIFOAndCost(t *testing.T) {
	s := DefaultFSSettings()
	s.Portfolios = FSBoth
	ch := FSCharges{Brokerage: amtOf("10"), STT: amtOf("1"), Exchange: amtOf("2"), Stamp: amtOf("3"), GST: amtOf("1.80")}
	ts := []FSTxn{
		{ID: "A1", Date: "2026-04-01", Portfolio: PortInv, Kind: "EQ", Side: "B", Type: FSTypeManual, ISIN: fsX, Symbol: "XCO", Qty: dq(100), Value: amtOf("1000"), Charges: ch, Status: FSPosted, Counter: ledger.BankOwn},
		{ID: "A2", Date: "2026-05-01", Portfolio: PortInv, Kind: "EQ", Side: "B", Type: FSTypeManual, ISIN: fsX, Symbol: "XCO", Qty: dq(50), Value: amtOf("600"), Charges: FSCharges{Brokerage: amtOf("5")}, Status: FSPosted, Counter: ledger.BankOwn},
		{ID: "A3", Date: "2026-06-01", Portfolio: PortInv, Kind: "EQ", Side: "S", Type: FSTypeManual, ISIN: fsX, Symbol: "XCO", Qty: dq(120), Value: amtOf("1500"), Charges: FSCharges{Brokerage: amtOf("12"), STT: amtOf("1.50")}, Status: FSPosted, Counter: ledger.BankOwn},
		{ID: "A4", Date: "2026-06-02", Portfolio: PortInv, Kind: "EQ", Side: "B", Type: FSTypeManual, ISIN: fsX, Symbol: "XCO", Qty: dq(10), Value: amtOf("100"), Status: FSPending},
	}
	for _, c := range []struct {
		cost      string
		lot1, out string
		left      string
	}{{"full", "1016.80", "1258.80", "363.00"}, {"full-itc", "1015.00", "1257.00", "363.00"}, {"price", "1000.00", "1240.00", "360.00"}} {
		s.InvCost = c.cost
		b := fsReplay(s, ts, nil, "")
		if r := b.Res["A1"]; r.CostIn.String() != c.lot1 || r.HeadsIn.Price != amtOf("1000") {
			t.Fatalf("%s: lot 1 cost %s %+v", c.cost, r.CostIn, r.HeadsIn)
		}
		r := b.Res["A3"]
		if r.CostOut.String() != c.out || len(r.Matches) != 2 {
			t.Fatalf("%s: cost out %s matches %d", c.cost, r.CostOut, len(r.Matches))
		}
		m0, m1 := b.Matches[r.Matches[0]], b.Matches[r.Matches[1]]
		if m0.Lot != "A1" || m0.Qty.String() != "100" || m1.Lot != "A2" || m1.Qty.String() != "20" {
			t.Fatalf("not FIFO: %+v %+v", m0, m1)
		}
		lots := b.lotsOf(PortInv)
		if len(lots) != 1 || lots[0].Qty.String() != "30" || lots[0].Cost.String() != c.left {
			t.Fatalf("%s: left %+v (the pending purchase must not count)", c.cost, lots)
		}
		// The sale's entry balances: net to the bank, charges as expenses,
		// the lots out at cost, the rest profit.
		ps := s.fsPostings(ts[2], r, b)
		var dr, cr money.Amount
		for _, p := range ps {
			dr, cr = dr+p.Debit, cr+p.Credit
		}
		if dr != cr || dr != amtOf("1500") {
			t.Fatalf("sale entry %s / %s: %+v", dr, cr, ps)
		}
	}
	// Up to a date: the sale is not there yet.
	if b := fsReplay(s, ts, nil, "2026-05-31"); b.held(PortInv, fsX).String() != "150" {
		t.Fatalf("held before the sale %s", b.held(PortInv, fsX))
	}
}

// TestFSSpeculativeSplit: bought and sold the same day is intraday
// (speculative); purchases and sales post to their own accounts.
func TestFSSpeculativeSplit(t *testing.T) {
	s := DefaultFSSettings()
	s.Portfolios, s.SITCharges = FSBoth, "expense"
	ch := FSCharges{STT: amtOf("10"), Stamp: amtOf("2")}
	ts := []FSTxn{
		{ID: "B1", Date: "2026-10-16", Portfolio: PortSIT, Kind: "EQ", Side: "B", Type: FSTypeTrade, ISIN: fsX, Symbol: "XCO", Qty: dq(100), Value: amtOf("10000"), Charges: ch, Status: FSPosted, Counter: "CLIENT:PRO"},
		{ID: "B2", Date: "2026-10-16", Portfolio: PortSIT, Kind: "EQ", Side: "S", Type: FSTypeTrade, ISIN: fsX, Symbol: "XCO", Qty: dq(40), Value: amtOf("4200"), Charges: ch, Status: FSPosted, Counter: "CLIENT:PRO"},
	}
	b := fsReplay(s, ts, nil, "")
	if b.Res["B1"].SpecQty.String() != "40" || b.Res["B2"].SpecQty.String() != "40" || b.held(PortSIT, fsX).String() != "60" {
		t.Fatalf("intraday %s %s held %s", b.Res["B1"].SpecQty, b.Res["B2"].SpecQty, b.held(PortSIT, fsX))
	}
	if m := b.Matches[0]; !m.Spec || m.Gain() != amtOf("200") {
		t.Fatalf("intraday match %+v", m)
	}
	acct := map[string]money.Amount{}
	for _, p := range append(s.fsPostings(ts[0], b.Res["B1"], b), s.fsPostings(ts[1], b.Res["B2"], b)...) {
		acct[p.Account] += p.Debit - p.Credit
	}
	if acct[fsPurAcct("EQ", true)] != amtOf("4000") || acct[fsPurAcct("EQ", false)] != amtOf("6000") || acct[fsSalAcct("EQ", true)] != -amtOf("4200") || acct["EXPENSE:FSCHG_STT"] != amtOf("20") {
		t.Fatalf("accounts %v", acct)
	}
	// Not kept apart: one purchases and one sales account.
	s.Spec = false
	b = fsReplay(s, ts, nil, "")
	if b.Res["B1"].SpecQty.Sign() != 0 || b.held(PortSIT, fsX).String() != "60" {
		t.Fatal("spec off still split")
	}
}

// TestFSTradeFiles: the firm's PRO trades come in from the exchange trade
// files after billing, one line per scrip and side with the bill's charges;
// the portfolio can be changed until the day is closed; trade-file lines
// need no second person.
func TestFSTradeFiles(t *testing.T) {
	o, ctx := fsFirm(t, nil)
	csv := "TradeNo,OrderNo,ClientCode,Symbol,Series,BuySell,Qty,Price,TradeDate,TradeTime,Product,AlgoId,TerminalId\n" +
		"990001,880001,PRO,RELIANCE,EQ,1,100,1400.00,16-Oct-2026,10:01:00,CNC,,T1\n" +
		"990002,880002,PRO,RELIANCE,EQ,2,40,1410.00,16-Oct-2026,14:01:00,CNC,,T1\n" +
		"990003,880003,PRO,INFY,EQ,1,10,1500.00,16-Oct-2026,11:00:00,CNC,,T1\n"
	if _, err := o.App.IngestFile(ctx, T, "priya.nair", "NSE-CM-TRADE", "", "pro_trades.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	// Before billing: lines without charges, and a warning.
	res, err := o.FirmSecSync(ctx, T, "priya.nair", D)
	if err != nil || res.New != 3 || len(res.Warnings) == 0 {
		t.Fatalf("sync before billing %+v %v", res, err)
	}
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBillingOpts(ctx, T, SystemActor, D, m.SettlementPrices(), false); err != nil {
		t.Fatal(err)
	}
	res, err = o.FirmSecSync(ctx, T, "priya.nair", D)
	if err != nil || res.Changed != 3 || res.New != 0 {
		t.Fatalf("sync after billing %+v %v", res, err)
	}
	if res, _ := o.FirmSecSync(ctx, T, "priya.nair", D); res.Changed+res.New != 0 {
		t.Fatalf("second sync changed things: %+v", res)
	}
	v, err := o.FirmSecRegister(ctx, T, "priya.nair", D, D)
	if err != nil || len(v.Lines) != 3 {
		t.Fatalf("register %d %v", len(v.Lines), err)
	}
	var buy FSTxn
	var charges money.Amount
	for _, l := range v.Lines {
		if l.Status != FSPosted || l.Portfolio != PortSIT || !l.Changeable || l.Entry == "" {
			t.Fatalf("line %+v", l)
		}
		if l.Symbol == "RELIANCE" && l.Side == "B" {
			buy = l
		}
		charges += l.Charges.Total()
	}
	bill, _ := o.fsBillCharges(ctx, T, D, "PRO", "NSE")
	if buy.SpecQty != "40" || buy.Value != amtOf("140000") || charges != bill.Total() || charges == 0 {
		t.Fatalf("buy %+v charges %s bill %s", buy, charges, bill.Total())
	}
	// The PRO code's ledger is cleared by the register's entries.
	if b := fsBal(t, o, ctx, "CLIENT:PRO"); b != 0 {
		t.Fatalf("CLIENT:PRO left at %s", b)
	}
	fsBalanced(t, o, ctx)
	// Changeable until the close: move the INFY purchase to investments.
	var infy FSTxn
	for _, l := range v.Lines {
		if l.Symbol == "INFY" {
			infy = l
		}
	}
	x, err := o.FirmSecChange(ctx, T, "priya.nair", infy.ID, PortInv, "", "")
	if err != nil || x.Portfolio != PortInv || x.Term != TermLong || x.DefaultPortfolio != PortSIT || x.Rev < 2 {
		t.Fatalf("change %+v %v", x, err)
	}
	if b := fsBal(t, o, ctx, fsInvAcct("EQ")); b != amtOf("15000")+x.Charges.Total()-x.Charges.STT {
		t.Fatalf("investment account %s", b)
	}
	fsBalanced(t, o, ctx)
	docs.Upsert(ctx, o.Docs, T, cEOD, D, EODState{Date: D, Closed: true}, "t")
	if _, err := o.FirmSecChange(ctx, T, "priya.nair", infy.ID, PortSIT, "", ""); !errors.Is(err, ErrState) {
		t.Fatalf("changed after the close: %v", err)
	}
	if _, err := o.FirmSecChange(ctx, T, "anita.rao", infy.ID, PortSIT, "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk changed a line: %v", err)
	}
	// With "every entry" approved by a second person, new lines wait.
	o.saveFS(ctx, T, "admin", func(s *FSSettings) error { s.Checker = "all"; return nil })
	if !o.FirmSecSettings(ctx, T).needsChecker(FSTypeTrade) || DefaultFSSettings().needsChecker(FSTypeTrade) || !DefaultFSSettings().needsChecker(FSTypeOffMarket) {
		t.Fatal("checker practice")
	}
}

// TestFSMakerChecker: manual and off-market entries wait for a second
// person; the maker cannot approve; with no checker they post at once.
func TestFSMakerChecker(t *testing.T) {
	o, ctx := fsFirm(t, nil)
	x := fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-09-01", Portfolio: PortInv, Side: "B", Type: FSTypeOffMarket, ISIN: fsX, Symbol: "XCO", Qty: "100", Price: "50", Counter: ledger.BankOwn})
	if x.Status != FSPending || x.Entry != "" {
		t.Fatalf("off-market posted at once: %+v", x)
	}
	if _, err := o.DecideFirmSecEntry(ctx, T, "priya.nair", x.ID, "approve", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker approved: %v", err)
	}
	if _, err := o.DecideFirmSecEntry(ctx, T, "vikram.shah", x.ID, "approve", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance approved: %v", err)
	}
	if _, err := o.DecideFirmSecEntry(ctx, T, "farhan.khan", x.ID, "reject", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rejected without a reason: %v", err)
	}
	x, err := o.DecideFirmSecEntry(ctx, T, "farhan.khan", x.ID, "approve", "")
	if err != nil || x.Status != FSPosted || x.Entry == "" || x.Checker != "farhan.khan" {
		t.Fatalf("approved %+v %v", x, err)
	}
	if b := fsBal(t, o, ctx, fsInvAcct("EQ")); b != amtOf("5000") {
		t.Fatalf("investments %s", b)
	}
	if _, err := o.AddFirmSecEntry(ctx, T, "anita.rao", FSEntryInput{Date: "2026-09-02", Portfolio: PortInv, Side: "B", ISIN: fsX, Symbol: "XCO", Qty: "1", Price: "50"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk entered: %v", err)
	}
	// Entries go in date order for a scrip.
	if _, err := o.AddFirmSecEntry(ctx, T, "priya.nair", FSEntryInput{Date: "2026-08-01", Portfolio: PortInv, Side: "B", ISIN: fsX, Symbol: "XCO", Qty: "1", Price: "50"}); !errors.Is(err, ErrState) {
		t.Fatalf("entered out of order: %v", err)
	}
	o.saveFS(ctx, T, "admin", func(s *FSSettings) error { s.Checker = "none"; return nil })
	y := fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-09-02", Portfolio: PortInv, Side: "B", Type: FSTypeManual, ISIN: fsX, Symbol: "XCO", Qty: "10", Price: "50"})
	if y.Status != FSPosted || y.Entry == "" {
		t.Fatalf("no checker: %+v", y)
	}
	// A posted manual entry is cancelled by a checker: its entry reversed.
	if _, err := o.DecideFirmSecEntry(ctx, T, "farhan.khan", y.ID, "cancel", "entered twice"); err != nil {
		t.Fatal(err)
	}
	if b := fsBal(t, o, ctx, fsInvAcct("EQ")); b != amtOf("5000") {
		t.Fatalf("after cancel %s", b)
	}
	fsBalanced(t, o, ctx)
}

// TestFSStockJournal: holdings move between the portfolios by a stock
// journal: out at cost, in at the day's market value, the difference
// posted, the tax date and value kept; a second person approves.
func TestFSStockJournal(t *testing.T) {
	o, ctx := fsFirm(t, func(s *FSSettings) { s.Checker = "none" })
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-09-01", Portfolio: PortInv, Side: "B", ISIN: fsX, Symbol: "XCO", Qty: "100", Price: "50"})
	fsMarket(t, o, ctx, "2026-09-15", map[string]string{fsX: "60"})
	if _, err := o.AddFirmSecJournal(ctx, T, "priya.nair", FSJournalInput{Date: "2026-09-15", From: PortInv, To: PortSIT, ISIN: fsX, Qty: "40"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no reason: %v", err)
	}
	if _, err := o.AddFirmSecJournal(ctx, T, "priya.nair", FSJournalInput{Date: "2026-09-15", From: PortInv, To: PortSIT, ISIN: fsX, Qty: "400", Reason: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("more than held: %v", err)
	}
	j, err := o.AddFirmSecJournal(ctx, T, "priya.nair", FSJournalInput{Date: "2026-09-15", From: PortInv, To: PortSIT, ISIN: fsX, Qty: "40", Reason: "Held for trading from today"})
	if err != nil || j.Status != FSPending || j.InValue != amtOf("2400") || j.OutCost != amtOf("2000") || !strings.Contains(j.TaxNote, "45(2)") {
		t.Fatalf("journal %+v %v", j, err)
	}
	if _, err := o.DecideFirmSecJournal(ctx, T, "priya.nair", j.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker approved: %v", err)
	}
	j, err = o.DecideFirmSecJournal(ctx, T, "farhan.khan", j.ID, true, "")
	if err != nil || j.Status != FSPosted || j.Entry == "" || j.Difference != amtOf("400") {
		t.Fatalf("posted %+v %v", j, err)
	}
	e, err := o.App.Ledger.Store.Get(ctx, T, j.Entry)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]money.Amount{}
	for _, p := range e.Postings {
		got[p.Account] += p.Debit - p.Credit
	}
	if got[fsPurAcct("EQ", false)] != amtOf("2400") || got[fsInvAcct("EQ")] != -amtOf("2000") || got[AcctFSTransfer] != -amtOf("400") {
		t.Fatalf("journal entry %v", got)
	}
	_, _, b := o.fsBookTo(ctx, T, "2026-09-15")
	sit := b.Lots[fsKey(PortSIT, fsX)]
	if b.held(PortInv, fsX).String() != "60" || len(sit) != 1 || sit[0].Cost != amtOf("2400") || sit[0].Converted == nil || sit[0].Converted.Cost != amtOf("2000") || sit[0].TaxDate != "2026-09-15" {
		t.Fatalf("lots after the journal %+v", sit)
	}
	// Selling the converted stock brings its capital gain (to the market
	// value on conversion) into the capital gains register.
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-10-20", Portfolio: PortSIT, Side: "S", ISIN: fsX, Symbol: "XCO", Qty: "40", Price: "70"})
	r, err := o.FirmSecReport(ctx, T, "farhan.khan", "cg", "", "2026-04-01", "2027-03-31", false)
	if err != nil || len(r.Sections) < 2 || len(r.Sections[1].Rows) != 1 || r.Sections[1].Rows[0][10] != "400.00" || r.Sections[1].Rows[0][13] != "section 196 (was 111A)" {
		t.Fatalf("cg register %+v %v", r.Sections, err)
	}
	// The other way: section 28(via).
	j2, err := o.AddFirmSecJournal(ctx, T, "priya.nair", FSJournalInput{Date: "2026-10-21", From: PortSIT, To: PortInv, ISIN: fsX, Qty: "1", Reason: "x", Price: "65"})
	if err == nil && !strings.Contains(j2.TaxNote, "28(via)") {
		t.Fatalf("tax note %q", j2.TaxNote)
	}
	fsBalanced(t, o, ctx)
	o.saveFS(ctx, T, "admin", func(s *FSSettings) error { s.Moves = "no"; return nil })
	if _, err := o.AddFirmSecJournal(ctx, T, "priya.nair", FSJournalInput{Date: "2026-10-22", From: PortInv, To: PortSIT, ISIN: fsX, Qty: "1", Reason: "x"}); !errors.Is(err, ErrState) {
		t.Fatalf("moves not allowed: %v", err)
	}
}

// TestFSCorporateActions: a bonus at nil cost, a split keeping cost and
// dates, and a buy-back by the rule from 1 October 2024.
func TestFSCorporateActions(t *testing.T) {
	o, ctx := fsFirm(t, func(s *FSSettings) { s.Checker = "none" })
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-08-03", Portfolio: PortInv, Side: "B", ISIN: fsX, Symbol: "XCO", Qty: "100", Price: "50"})
	for _, c := range []CorpAction{
		{ID: "CA-T1", ISIN: fsX, Symbol: "XCO", Type: "Bonus", ExDate: "2026-08-10", RecordDate: "2026-08-10", PayDate: "2026-08-12", RatioNew: 1, RatioOld: 1, Status: "Applied"},
		{ID: "CA-T2", ISIN: fsX, Symbol: "XCO", Type: "Split", ExDate: "2026-08-20", RecordDate: "2026-08-20", RatioNew: 2, RatioOld: 10, Status: "Applied"},
	} {
		docs.Upsert(ctx, o.Docs, T, cCorpAction, c.ID, c, "t")
	}
	_, _, b := o.fsBookTo(ctx, T, "2026-08-15")
	lots := b.Lots[fsKey(PortInv, fsX)]
	if len(lots) != 2 || lots[1].Cost != 0 || lots[1].Qty.String() != "100" || lots[1].Date != "2026-08-12" || lots[0].Cost != amtOf("5000") {
		t.Fatalf("after the bonus %+v", lots)
	}
	_, _, b = o.fsBookTo(ctx, T, "2026-08-31")
	lots = b.Lots[fsKey(PortInv, fsX)]
	if b.held(PortInv, fsX).String() != "1000" || lots[0].Cost != amtOf("5000") || lots[0].Date != "2026-08-03" || len(b.Events) != 2 {
		t.Fatalf("after the split %+v events %d", lots, len(b.Events))
	}
	// Buy-back of 100 at 20: deemed dividend, TDS receivable, the cost of
	// the oldest lots (100 of the first 500 at ₹10) a capital loss.
	bb := fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-09-01", Portfolio: PortInv, Side: "S", Type: FSTypeBuyBack, ISIN: fsX, Symbol: "XCO", Qty: "100", Price: "20", TDS: "200"})
	e, _ := o.App.Ledger.Store.Get(ctx, T, bb.Entry)
	got := map[string]money.Amount{}
	for _, p := range e.Postings {
		got[p.Account] += p.Debit - p.Credit
	}
	if got[ledger.BankOwn] != amtOf("1800") || got[AcctFSTDS] != amtOf("200") || got[AcctFSDividend] != -amtOf("2000") || got[AcctFSBuyBackLoss] != amtOf("1000") || got[fsInvAcct("EQ")] != -amtOf("1000") {
		t.Fatalf("buy-back entry %v", got)
	}
	r, _ := o.FirmSecReport(ctx, T, "farhan.khan", "cg", "", "2026-04-01", "2027-03-31", false)
	if len(r.Sections[0].Rows) != 1 || r.Sections[0].Rows[0][9] != "0.00" || r.Sections[0].Rows[0][10] != "-1000.00" {
		t.Fatalf("buy-back in the register %v", r.Sections[0].Rows)
	}
	fsBalanced(t, o, ctx)
}

// TestFSDividendsAndBank: dividends from the corporate actions, with TDS
// as receivable once the year's dividends pass the threshold, matched to
// the firm's bank credits.
func TestFSDividendsAndBank(t *testing.T) {
	o, ctx := fsFirm(t, func(s *FSSettings) { s.Checker = "none" })
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-08-03", Portfolio: PortInv, Side: "B", ISIN: fsX, Symbol: "XCO", Qty: "1000", Price: "50"})
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-09-21", Portfolio: PortInv, Side: "B", ISIN: fsX, Symbol: "XCO", Qty: "500", Price: "50"})
	docs.Upsert(ctx, o.Docs, T, cCorpAction, "CA-D1", CorpAction{ID: "CA-D1", ISIN: fsX, Symbol: "XCO", Type: "Dividend", ExDate: "2026-09-21", RecordDate: "2026-09-21", PayDate: "2026-10-05", DPS: money.MustDec("12")}, "t")
	docs.Upsert(ctx, o.Docs, T, cCorpAction, "CA-D2", CorpAction{ID: "CA-D2", ISIN: fsX, Symbol: "XCO", Type: "Dividend", ExDate: "2027-01-10", RecordDate: "2027-01-10", PayDate: "2027-01-20", DPS: money.MustDec("2")}, "t")
	n, err := o.fsBookDividends(ctx, T, SystemActor, "2026-10-01")
	if err != nil || n != 1 {
		t.Fatalf("booked %d %v", n, err)
	}
	if n, _ := o.fsBookDividends(ctx, T, SystemActor, "2026-10-01"); n != 0 {
		t.Fatal("booked twice")
	}
	xs := o.fsIncomes(ctx, T)
	// Bought on the ex-date: no dividend on those 500.
	if len(xs) != 1 || xs[0].Qty.String() != "1000" || xs[0].Gross != amtOf("12000") || xs[0].TDS != amtOf("1200") || xs[0].Net != amtOf("10800") {
		t.Fatalf("dividend %+v", xs)
	}
	if fsBal(t, o, ctx, AcctFSTDS) != amtOf("1200") || fsBal(t, o, ctx, AcctFSIncomeRecv) != amtOf("10800") {
		t.Fatal("receivables")
	}
	res, err := o.FirmSecMatchIncome(ctx, T, "priya.nair", "", []FSBankCredit{{Date: "2026-10-06", Amount: "10,800.00", Ref: "NEFT1", Text: "XCO LTD FINAL DIV"}, {Date: "2026-10-06", Amount: "99", Ref: "N2", Text: "other"}})
	if err != nil || len(res.Matched) != 1 || len(res.Unmatched) != 1 || res.Matched[0].Status != FSIncomeReceived {
		t.Fatalf("match %+v %v", res, err)
	}
	if fsBal(t, o, ctx, AcctFSIncomeRecv) != 0 || fsBal(t, o, ctx, ledger.BankOwn) < amtOf("10800")-amtOf("75000") {
		t.Fatal("receivable not cleared")
	}
	if _, err := o.FirmSecMatchIncome(ctx, T, "priya.nair", ledger.BankClient, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("client bank: %v", err)
	}
	// The next dividend of the year from the company: TDS at once.
	if n, _ := o.fsBookDividends(ctx, T, SystemActor, "2027-01-10"); n != 1 {
		t.Fatal("second dividend")
	}
	for _, x := range o.fsIncomes(ctx, T) {
		if x.Corp == "CA-D2" && (x.Gross != amtOf("3000") || x.TDS != amtOf("300")) {
			t.Fatalf("second %+v", x)
		}
	}
	// Under the threshold, no TDS.
	s := DefaultFSSettings()
	if fsTDS(s, amtOf("9000"), nil) != 0 || fsTDS(s, amtOf("4000"), []FSIncome{{Gross: amtOf("7000")}}) != amtOf("1100") {
		t.Fatal("threshold")
	}
	fsBalanced(t, o, ctx)
}

// TestFSDematRecon: the register against the firm's demat; purchases
// before their pay-out are in transit, not breaks.
func TestFSDematRecon(t *testing.T) {
	o, ctx := fsFirm(t, func(s *FSSettings) { s.Checker, s.Demats, s.SITDemat = "none", "one", "1208160000000001" })
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-10-14", PayDate: "2026-10-15", Portfolio: PortSIT, Side: "B", ISIN: fsY, Symbol: "YCO", Qty: "100", Price: "10"})
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-10-16", PayDate: "2026-10-19", Portfolio: PortSIT, Side: "B", ISIN: fsY, Symbol: "YCO", Qty: "20", Price: "10"})
	if _, err := o.SaveFirmSecDemat(ctx, T, "priya.nair", FSDematStatement{Demat: "9999", Date: D}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not the firm's demat: %v", err)
	}
	if _, err := o.SaveFirmSecDemat(ctx, T, "priya.nair", FSDematStatement{Demat: "1208160000000001", Date: D, Lines: []FSDematLine{{ISIN: fsY, Qty: dq(100)}}}); err != nil {
		t.Fatal(err)
	}
	v, err := o.FirmSecRecon(ctx, T, "anita.rao", D)
	if err != nil || len(v.Rows) != 1 || v.Rows[0].Status != "In transit" || v.Breaks != 0 || v.Rows[0].TransitIn.String() != "20" {
		t.Fatalf("recon %+v %v", v, err)
	}
	o.SaveFirmSecDemat(ctx, T, "priya.nair", FSDematStatement{Demat: "1208160000000001", Date: D, Lines: []FSDematLine{{ISIN: fsY, Qty: dq(90)}}})
	v, _ = o.FirmSecRecon(ctx, T, "anita.rao", D)
	if v.Breaks != 1 || v.Rows[0].Difference.String() != "-10" {
		t.Fatalf("break %+v", v.Rows)
	}
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-firmsec-demat-"+D); w.Title == "" {
		t.Fatal("no work item for the break")
	}
	// After the pay-out the purchase is expected in the demat.
	if v := o.fsRecon(ctx, T, "2026-10-19"); v.Rows[0].TransitIn.Sign() != 0 || v.Rows[0].Expected.String() != "120" {
		t.Fatalf("after pay-out %+v", v.Rows)
	}
}

// TestFSPledges: pledges are tracked, not blocked: a sale into pledged
// units goes through with a warning.
func TestFSPledges(t *testing.T) {
	o, ctx := fsFirm(t, nil)
	o.saveFS(ctx, T, "admin", func(s *FSSettings) error { s.Checker = "none"; return nil })
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-09-01", Portfolio: PortInv, Side: "B", ISIN: fsX, Symbol: "XCO", Qty: "100", Price: "50"})
	o.saveFS(ctx, T, "admin", func(s *FSSettings) error { s.Checker = "manual"; return nil })
	if _, _, err := o.AddFirmSecPledge(ctx, T, "anita.rao", FSPledgeInput{Date: "2026-09-05", Portfolio: PortInv, ISIN: fsX, Qty: "80", Pledgee: "A bank", PledgeeType: "Lender"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pledgee type: %v", err)
	}
	p, warn, err := o.AddFirmSecPledge(ctx, T, "anita.rao", FSPledgeInput{Date: "2026-09-05", Portfolio: PortInv, ISIN: fsX, Qty: "80", Pledgee: "A bank", PledgeeType: "Bank", Purpose: "Overdraft"})
	if err != nil || len(warn) != 0 || p.Status != FSPledgeOpen {
		t.Fatalf("pledge %+v %v %v", p, warn, err)
	}
	x := fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-09-10", Portfolio: PortInv, Side: "S", Type: FSTypeOffMarket, ISIN: fsX, Symbol: "XCO", Qty: "30", Price: "55"})
	if len(x.Warnings) != 1 || !strings.Contains(x.Warnings[0], "pledged") || x.Status != FSPending {
		t.Fatalf("sale into pledged units %+v", x)
	}
	if _, err := o.DecideFirmSecEntry(ctx, T, "farhan.khan", x.ID, "approve", ""); err != nil {
		t.Fatalf("pledged sale blocked: %v", err)
	}
	r, err := o.FirmSecReport(ctx, T, "anita.rao", "pledges", "", "", "2026-09-10", false)
	if err != nil || len(r.Sections[0].Rows) != 1 {
		t.Fatalf("report %+v %v", r, err)
	}
	if row := r.Sections[0].Rows[0]; row[5] != "80" || row[6] != "70" || row[8] != "Bank" {
		t.Fatalf("pledge row %v", row)
	}
	if _, err := o.CloseFirmSecPledge(ctx, T, "anita.rao", p.ID, "release", "2026-09-20", ""); err != nil {
		t.Fatal(err)
	}
	if q := o.fsPledgedOn(ctx, T, "2026-09-20")[fsKey(PortInv, fsX)]; q.Sign() != 0 {
		t.Fatalf("still pledged %s", q)
	}
	if q := o.fsPledgedOn(ctx, T, "2026-09-19")[fsKey(PortInv, fsX)]; q.String() != "80" {
		t.Fatalf("pledged the day before the release %s", q)
	}
}

// TestFSValuation: closing stock at cost, the lower of cost and market
// scrip by scrip, and category by category (ICDS VIII, unlisted at cost);
// investments at cost or the lower of cost and closing price; the
// proposed entry and its approval.
func TestFSValuation(t *testing.T) {
	o, ctx := fsFirm(t, func(s *FSSettings) { s.Checker = "none" })
	for _, e := range []FSEntryInput{
		{Portfolio: PortSIT, ISIN: fsX, Symbol: "XCO", Qty: "100", Price: "10"},                 // cost 1000, market 800
		{Portfolio: PortSIT, ISIN: fsY, Symbol: "YCO", Qty: "100", Price: "10"},                 // cost 1000, market 1300
		{Portfolio: PortSIT, ISIN: fsBD, Symbol: "ZBOND", Kind: "BOND", Qty: "5", Price: "100"}, // cost 500, market 450
		{Portfolio: PortSIT, ISIN: fsUN, Symbol: "UCO", Kind: "UNL", Qty: "30", Price: "10"},    // cost 300, unlisted
		{Portfolio: PortInv, ISIN: fsX, Symbol: "XCO", Qty: "50", Price: "10", Term: TermCurrent},
		{Portfolio: PortInv, ISIN: fsY, Symbol: "YCO", Qty: "50", Price: "10"},
	} {
		e.Date, e.Side = "2026-10-01", "B"
		fsEntry(t, o, ctx, "priya.nair", e)
	}
	fsMarket(t, o, ctx, "2026-10-31", map[string]string{fsX: "8", fsY: "13", fsBD: "90", fsUN: "50"})
	v, err := o.FirmSecValuation(ctx, T, "priya.nair", "2026-10-31", PortSIT)
	if err != nil {
		t.Fatal(err)
	}
	if v.AtCost != amtOf("2800") || v.LCMScrip != amtOf("2550") || v.LCMCat != amtOf("2750") || v.Value != amtOf("2800") {
		t.Fatalf("closing stock cost %s scrip %s category %s value %s", v.AtCost, v.LCMScrip, v.LCMCat, v.Value)
	}
	for _, c := range []struct{ basis, want string }{{"lcm-scrip", "2550.00"}, {"lcm-category", "2750.00"}, {"cost", "2800.00"}} {
		o.saveFS(ctx, T, "admin", func(s *FSSettings) error { s.Closing = c.basis; return nil })
		v, _ = o.FirmSecValuation(ctx, T, "priya.nair", "2026-10-31", PortSIT)
		var tgt money.Amount
		for _, p := range v.Proposed {
			if strings.HasPrefix(p.Account, "ASSET:FSSIT_") {
				tgt += p.Target
			}
		}
		if v.Value.String() != c.want || tgt.String() != c.want {
			t.Fatalf("%s: value %s targets %s", c.basis, v.Value, tgt)
		}
	}
	// Propose and post the closing stock entry.
	x, err := o.ProposeFirmSecValuation(ctx, T, "priya.nair", "2026-10-31", PortSIT)
	if err != nil || x.Status != FSPending {
		t.Fatalf("propose %+v %v", x, err)
	}
	if _, err := o.DecideFirmSecValuation(ctx, T, "priya.nair", x.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker posted: %v", err)
	}
	if x, err = o.DecideFirmSecValuation(ctx, T, "farhan.khan", x.ID, true, ""); err != nil || x.Status != FSPosted {
		t.Fatalf("post %+v %v", x, err)
	}
	if b := fsBal(t, o, ctx, AcctFSChangeInv); b != -amtOf("2800") {
		t.Fatalf("change in inventories %s", b)
	}
	if v, _ := o.FirmSecValuation(ctx, T, "priya.nair", "2026-10-31", PortSIT); len(v.Proposed) != 0 {
		t.Fatalf("still proposes %+v", v.Proposed)
	}
	// Investments: current at the lower of cost and price, long-term at cost.
	for _, c := range []struct{ basis, want string }{{"cost", "1000.00"}, {"lcm", "900.00"}, {"lcm-all", "900.00"}, {"fair", "1050.00"}} {
		o.saveFS(ctx, T, "admin", func(s *FSSettings) error { s.InvValue = c.basis; return nil })
		v, _ := o.FirmSecValuation(ctx, T, "priya.nair", "2026-10-31", PortInv)
		if v.Value.String() != c.want {
			t.Fatalf("investments %s: %s", c.basis, v.Value)
		}
	}
	o.saveFS(ctx, T, "admin", func(s *FSSettings) error { s.InvValue = "lcm"; return nil })
	x, _ = o.ProposeFirmSecValuation(ctx, T, "priya.nair", "2026-10-31", PortInv)
	if _, err := o.DecideFirmSecValuation(ctx, T, "farhan.khan", x.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if fsBal(t, o, ctx, AcctFSProvision) != -amtOf("100") || fsBal(t, o, ctx, AcctFSProvExp) != amtOf("100") {
		t.Fatal("provision for diminution")
	}
	// The closing stock statement shows all three values.
	r, err := o.FirmSecReport(ctx, T, "vikram.shah", "closing", PortSIT, "", "2026-10-31", false)
	if err != nil || len(r.Sections) != 3 || r.Sections[0].Rows[0][1] != "2800.00" || r.Sections[0].Rows[1][1] != "2550.00" || r.Sections[0].Rows[2][1] != "2750.00" {
		t.Fatalf("closing statement %+v %v", r.Sections, err)
	}
	fsBalanced(t, o, ctx)
}

// TestFSReports: the scrip-wise summary adds up (opening + bought − cost
// of sold = closing, quantity and amount apart, with a P&L column); cost
// heads as columns; the speculative split and the tax audit turnover.
func TestFSReports(t *testing.T) {
	o, ctx := fsFirm(t, func(s *FSSettings) { s.Checker, s.SITCharges = "none", "cost" })
	ch := FSCharges{Brokerage: amtOf("10"), STT: amtOf("5"), Stamp: amtOf("1")}
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-03-20", Portfolio: PortSIT, Side: "B", ISIN: fsX, Symbol: "XCO", Qty: "100", Price: "10", Charges: ch})
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-04-10", Portfolio: PortSIT, Side: "B", ISIN: fsX, Symbol: "XCO", Qty: "50", Price: "12"})
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-04-20", Portfolio: PortSIT, Side: "S", ISIN: fsX, Symbol: "XCO", Qty: "120", Price: "15", Charges: ch})
	r, err := o.FirmSecReport(ctx, T, "priya.nair", "summary", PortSIT, "2026-04-01", "2027-03-31", true)
	if err != nil {
		t.Fatal(err)
	}
	row := r.Sections[0].Rows[0]
	// opening 100 at 1011; bought 50 at 600; sold 120 for 1800 at cost
	// 1011 + 20 × 12 = 1251; closing 30 at 360; P&L 549.
	want := []string{"XCO", fsX, "100", "1011.00", "50", "600.00", "120", "1800.00", "1251.00", "30", "360.00"}
	for i, w := range want {
		if row[i] != w {
			t.Fatalf("summary col %d (%s) = %q, want %q: %v", i, r.Sections[0].Columns[i].Label, row[i], w, row)
		}
	}
	if row[len(row)-1] != "549.00" || row[11] != "360.00" || len(r.Sections[0].Columns) != 18 {
		t.Fatalf("P&L or heads %v", row)
	}
	if r, _ := o.FirmSecReport(ctx, T, "priya.nair", "summary", PortSIT, "2026-04-01", "2027-03-31", false); len(r.Sections[0].Columns) != 12 {
		t.Fatalf("without heads: %d columns", len(r.Sections[0].Columns))
	}
	// Intraday on one day: speculative; turnover is the difference taken
	// as positive.
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-05-04", Portfolio: PortSIT, Side: "B", ISIN: fsY, Symbol: "YCO", Qty: "10", Price: "100"})
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-05-04", Portfolio: PortSIT, Side: "S", ISIN: fsY, Symbol: "YCO", Qty: "10", Price: "95"})
	r, _ = o.FirmSecReport(ctx, T, "priya.nair", "pnl", PortSIT, "2026-04-01", "2027-03-31", false)
	if r.Summary[0].Value != "-50.00" || r.Summary[1].Value != "533.00" {
		t.Fatalf("pnl %+v", r.Summary)
	}
	r, _ = o.FirmSecReport(ctx, T, "priya.nair", "turnover", PortSIT, "2026-04-01", "2027-03-31", false)
	if r.Summary[0].Value != "50.00" || r.Summary[1].Value != "1800.00" {
		t.Fatalf("turnover %+v", r.Summary)
	}
	r, _ = o.FirmSecReport(ctx, T, "priya.nair", "register", "", "2026-04-01", "2027-03-31", true)
	if len(r.Sections[0].Rows) != 4 || r.Summary[1].Value != "2750.00" {
		t.Fatalf("register %d %+v", len(r.Sections[0].Rows), r.Summary)
	}
	if _, err := o.FirmSecReport(ctx, T, "meera.iyer", "summary", PortSIT, "2026-04-01", "2027-03-31", false); err == nil {
		t.Fatal("a branch login saw the firm's securities")
	}
	if len(r.CSV()) == 0 {
		t.Fatal("csv")
	}
}

// TestFSNetWorth: the net worth with the firm's securities at cost:
// pledged, unlisted and 30% of the rest deducted.
func TestFSNetWorth(t *testing.T) {
	o, ctx := fsFirm(t, func(s *FSSettings) { s.Checker = "none" })
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-09-01", Portfolio: PortInv, Side: "B", ISIN: fsX, Symbol: "XCO", Qty: "100", Price: "100"})
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-09-01", Portfolio: PortInv, Side: "B", ISIN: fsUN, Symbol: "UCO", Kind: "UNL", Qty: "10", Price: "100"})
	o.AddFirmSecPledge(ctx, T, "anita.rao", FSPledgeInput{Date: "2026-09-02", Portfolio: PortInv, ISIN: fsX, Qty: "40", Pledgee: "Clearing", PledgeeType: FSPledgeeTypes[0]})
	if _, err := o.SaveFirmSecNetWorthInputs(ctx, T, "priya.nair", FSNetWorthInputs{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker saved inputs: %v", err)
	}
	if _, err := o.SaveFirmSecNetWorthInputs(ctx, T, "vikram.shah", FSNetWorthInputs{Card: amtOf("100000"), BaseRequired: amtOf("1000000")}); err != nil {
		t.Fatal(err)
	}
	nw, err := o.FirmSecNetWorth(ctx, T, "vikram.shah", "2026-09-30")
	if err != nil || !nw.Provisional {
		t.Fatal(err)
	}
	line := map[string]money.Amount{}
	var run money.Amount
	for _, l := range nw.Lines {
		line[l.Key] = l.Amount
		run += money.Amount(l.Sign) * l.Amount
	}
	if line["pledged"] != amtOf("4000") || line["unlisted"] != amtOf("1000") || line["haircut"] != amtOf("1800") || line["card"] != amtOf("100000") {
		t.Fatalf("deductions %v", line)
	}
	if nw.NetWorth != line["networth"] || nw.Required != amtOf("1000000") || nw.Surplus != nw.NetWorth-nw.Required {
		t.Fatalf("net worth %s (%s) required %s", nw.NetWorth, line["networth"], nw.Required)
	}
	if len(fsNetWorthLayout) == 0 || fsNetWorthLayout[len(fsNetWorthLayout)-1].key != "networth" {
		t.Fatal("layout")
	}
}

// TestFSOpeningLots: FIRM-LOTS loads lots that count at once; a second
// person posts them to the books.
func TestFSOpeningLots(t *testing.T) {
	o, ctx := fsFirm(t, nil)
	data := "Portfolio,ISIN,Symbol,Qty,AcqDate,Cost,Kind,Term,FMV2018\n" +
		"INV," + fsX + ",XCO,100,2017-06-01,10000,EQ,LT,150\n" +
		"SIT," + fsY + ",YCO,50,2026-02-01,5000,,,\n"
	if _, err := o.LoadFile(ctx, T, "priya.nair", "FIRM-LOTS", "2026-03-31", "lots.csv", []byte(data)); err != nil {
		t.Fatal(err)
	}
	h, err := o.FirmSecHoldings(ctx, T, "priya.nair", "2026-04-01")
	if err != nil || h.Loaded != 2 || len(h.Ports) != 2 || h.Ports[0].Cost != amtOf("10000") {
		t.Fatalf("holdings %+v %v", h, err)
	}
	if lot := h.Ports[0].Accounts[0].Scrips[0].Lots[0]; lot.Date != "2017-06-01" || lot.TermNow != TermLong {
		t.Fatalf("lot %+v", lot)
	}
	if _, err := o.PostFirmSecOpening(ctx, T, "priya.nair", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("loader posted: %v", err)
	}
	eq0 := fsBal(t, o, ctx, ledger.OpeningEquity)
	if n, err := o.PostFirmSecOpening(ctx, T, "farhan.khan", ""); err != nil || n != 2 {
		t.Fatalf("posted %d %v", n, err)
	}
	if fsBal(t, o, ctx, fsInvAcct("EQ")) != amtOf("10000") || fsBal(t, o, ctx, fsSITAcct("EQ")) != amtOf("5000") || fsBal(t, o, ctx, ledger.OpeningEquity)-eq0 != -amtOf("15000") {
		t.Fatal("opening entries")
	}
	if _, err := o.LoadFile(ctx, T, "priya.nair", "FIRM-LOTS", "2026-03-31", "lots2.csv", []byte(data)); err == nil {
		t.Fatal("loaded again after posting")
	}
	// The lot's 31 January 2018 value grandfathers its long-term gain.
	o.saveFS(ctx, T, "admin", func(s *FSSettings) error { s.Checker = "none"; return nil })
	fsEntry(t, o, ctx, "priya.nair", FSEntryInput{Date: "2026-06-01", Portfolio: PortInv, Side: "S", ISIN: fsX, Symbol: "XCO", Qty: "100", Price: "200"})
	r, _ := o.FirmSecReport(ctx, T, "priya.nair", "cg", "", "2026-04-01", "2027-03-31", false)
	if row := r.Sections[0].Rows[0]; row[7] != "15000.00" || row[8] != "15000.00" || row[10] != "5000.00" || row[12] != "LT" || row[13] != "section 198 (was 112A)" {
		t.Fatalf("grandfathered row %v", row)
	}
	fsBalanced(t, o, ctx)
}

// TestCGRules: the capital gains rules both registers use.
func TestCGRules(t *testing.T) {
	d := money.MustDec
	for _, c := range []struct {
		class, acq, sale, cost, proc, fmv string
		term, sec, gain, tcost            string
	}{
		{"equity", "2017-06-01", "2026-10-16", "100", "200", "1.5", "LT", "112A", "50", "150"},  // FMV between cost and sale
		{"equity", "2017-06-01", "2026-10-16", "100", "120", "1.5", "LT", "112A", "0", "120"},   // sale below FMV
		{"equity", "2017-06-01", "2026-10-16", "100", "200", "0.8", "LT", "112A", "100", "100"}, // FMV below cost
		{"equity", "2018-02-01", "2026-10-16", "100", "200", "1.5", "LT", "112A", "100", "100"}, // bought after: no grandfathering
		{"equity", "2026-01-01", "2026-10-16", "100", "200", "", "ST", "111A", "100", "100"},
		{"mf-debt", "2024-01-01", "2026-10-16", "100", "130", "", "ST", "50AA", "30", "100"},
		{"mf-other", "2021-01-01", "2026-10-16", "100", "130", "", "LT", "112", "30", "100"},
		{"unlisted", "2025-01-01", "2026-10-16", "100", "130", "", "ST", "slab", "30", "100"},
		{"unlisted", "2024-01-01", "2026-10-16", "100", "130", "", "LT", "112", "30", "100"},
		{"listed", "2025-09-01", "2026-10-16", "100", "130", "", "LT", "112", "30", "100"},
	} {
		f := money.Dec{}
		if c.fmv != "" {
			f = d(c.fmv)
		}
		l := cgWork(c.class, c.acq, c.sale, d("100"), d(c.cost), d(c.proc), f)
		if l.Term != c.term || l.Section != c.sec || l.Gain.Cmp(d(c.gain)) != 0 || l.TaxCost.Cmp(d(c.tcost)) != 0 {
			t.Fatalf("%+v: got %s %s gain %s cost %s", c, l.Term, l.Section, l.Gain, l.TaxCost)
		}
	}
	if cgRate("111A", "2024-07-22") != "15%" || cgRate("111A", "2024-07-23") != "20%" || CG112AExemption("2026-10-16") != amtOf("125000") || CG112AExemption("2024-01-01") != amtOf("100000") {
		t.Fatal("rates")
	}
	if cgSectionName("112A", "2026-04-01") != "section 198 (was 112A)" || cgSectionName("112A", "2026-03-31") != "section 112A" {
		t.Fatal("2025 Act names")
	}
	if cgClassOf("ETF", "NIPPON GOLD BEES") != "mf-other" || cgClassOf("ETF", "NIFTYBEES") != "equity" || cgClassOf("MFO", "Debt Scheme - Liquid Fund") != "mf-debt" {
		t.Fatal("classes")
	}
	// Schedule 112A: scrip-wise, grandfathered rows apart, split at 23 July 2024.
	sales := []CGSale{
		{Sale: Sale{Date: "2026-10-16", ISIN: fsX, Symbol: "XCO", Qty: d("10"), BuyDate: "2017-06-01", Cost: d("100"), Proceeds: d("200")}, CG: cgWork("equity", "2017-06-01", "2026-10-16", d("10"), d("100"), d("200"), d("15"))},
		{Sale: Sale{Date: "2026-10-17", ISIN: fsX, Symbol: "XCO", Qty: d("5"), BuyDate: "2017-06-01", Cost: d("50"), Proceeds: d("100")}, CG: cgWork("equity", "2017-06-01", "2026-10-17", d("5"), d("50"), d("100"), d("15"))},
		{Sale: Sale{Date: "2026-10-16", ISIN: fsY, Symbol: "YCO", Qty: d("1"), BuyDate: "2020-01-01", Cost: d("10"), Proceeds: d("30")}, CG: cgWork("equity", "2020-01-01", "2026-10-16", d("1"), d("10"), d("30"), money.Dec{})},
		{Sale: Sale{Date: "2024-07-01", ISIN: fsY, Symbol: "YCO", Qty: d("1"), BuyDate: "2020-01-01", Cost: d("10"), Proceeds: d("30")}, CG: cgWork("equity", "2020-01-01", "2024-07-01", d("1"), d("10"), d("30"), money.Dec{})},
	}
	s := cgSchedule112A(sales)
	if len(s.Rows) != 3 || len(s.Columns) != 15 {
		t.Fatalf("rows %d cols %d", len(s.Rows), len(s.Columns))
	}
	if r := s.Rows[0]; r[0] != "On or before 31 Jan 2018" || r[3] != "15" || r[5] != "300.00" || r[6] != "225.00" || r[9] != "15.00" || r[10] != "225.00" || r[13] != "75.00" {
		t.Fatalf("grandfathered row %v", r)
	}
	if s.Rows[2][14] != "Before 23 Jul 2024" || s.Totals[13] != "115.00" {
		t.Fatalf("split rows %v totals %v", s.Rows, s.Totals)
	}
}

// TestClientCGStatement: the clients' statement has the sections by
// tax treatment, Schedule 112A and the rates, also in the year-end pack.
func TestClientCGStatement(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	found := false
	for _, c := range cs {
		if c.Status != "Active" {
			continue
		}
		r, err := o.ClientReportFor(ctx, T, "pnl", c.UCC, "2026-04-01", D)
		if err != nil {
			t.Fatal(err)
		}
		has := false
		for _, s := range r.Sections {
			has = has || strings.HasPrefix(s.Title, "Schedule 112A")
		}
		if !has || r.Summary[1].Label != "Short-term capital gain" || r.Summary[2].Label != "Long-term capital gain" || len(r.Summary) < 7 {
			t.Fatalf("%s: %+v", c.UCC, r.Summary)
		}
		if r.Summary[1].Value != "0.00" || r.Summary[2].Value != "0.00" {
			found = true
			p, err := o.YearEnd(ctx, T, c.UCC, "2026-27", D)
			if err != nil {
				t.Fatal(err)
			}
			b, err := RenderYearEnd(p)
			if err != nil || !strings.Contains(string(b), "Schedule 112A") || p.RateST != "20%" {
				t.Fatalf("year-end pack %v %s", err, p.RateST)
			}
			break
		}
	}
	if !found {
		t.Fatal("no client with gains")
	}
}

// TestFSDisclosure: with the practice on, online account opening asks the
// client to accept the proprietary trading disclosure.
func TestFSDisclosure(t *testing.T) {
	o, ctx := setup(t)
	if _, ok := o.ApplyConfig(ctx, T).Declare["prop"]; ok {
		t.Fatal("disclosure before the firm keeps securities")
	}
	o.saveFS(ctx, T, "admin", func(s *FSSettings) error {
		s.Portfolios, s.Disclosure, s.DisclosureText = "sit", true, "We trade on our own account."
		return nil
	})
	if d := o.ApplyConfig(ctx, T).Declare["prop"]; d != "We trade on our own account." {
		t.Fatalf("declaration %q", d)
	}
}
