package ops

import (
	"context"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// Separate equity and commodity ledgers (segledger.go).

const (
	sgD0   = "2026-10-29" // the day the money comes in (Thursday)
	sgOpen = "2026-10-28" // opening balances
	sgAcct = "00321234560198"
	sgCom  = "00321234560277" // the firm's commodity collection account
)

func inr(s string) money.Amount { a, _ := money.ParseAmount(s); return a }

// sepSetup is Baljit's position before the incident: BJ01 has ₹10,000 in
// the equity ledger and ₹2,00,000 in the commodity ledger, loaded as
// opening balances with the Ledger column; BJ02 has ₹50,000 equity only.
// e-CMS pushes from HDFC are set up for two firm accounts.
func sepSetup(t *testing.T) (*Ops, context.Context) {
	t.Helper()
	o, ctx := newOps(t)
	for _, c := range []Client{
		{UCC: "BJ01", Name: "Asha Rao", Status: "Active", Email: "a@x.example", Segments: []string{"CM", "FO", "COM"}, Bank: BankAccount{Bank: "HDFC Bank", IFSC: "HDFC0001234", Account: "50100011112222"}},
		{UCC: "BJ02", Name: "Vikram Shah", Status: "Active", Email: "v@x.example", Segments: []string{"CM", "COM"}, Bank: BankAccount{Bank: "Axis Bank", IFSC: "UTIB0000123", Account: "91902033334444"}},
	} {
		if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "t"); err != nil {
			t.Fatal(err)
		}
	}
	set, _ := o.Settings(ctx, T)
	set.SeparateCommodityLedger = true
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	csv := "Account,Debit,Credit,Ledger\nCLIENT:BJ01,,10000.00,Equity\nCLIENT:BJ01,,200000.00,Commodity\nCLIENT:BJ02,,50000.00,\nEQUITY:OPENING,260000.00,,\n"
	if _, err := o.LoadFile(ctx, T, "admin", "OPENING-BALANCES", sgOpen, "opening.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connCreditsECMS, map[string]string{"accounts": sgAcct + "," + sgCom}, nil); err != nil {
		t.Fatal(err)
	}
	return o, ctx
}

func ecmsCredit(t *testing.T, o *Ops, ctx context.Context, seq, utr, toAcct, payer, amount, narration string) {
	t.Helper()
	body := []byte(`{"GenericCorporateAlertRequest":[{"Alert Sequence No":"` + seq + `","Account number":"` + toAcct + `","Debit Credit":"Credit","Amount":"` + amount + `",` +
		`"Remitter Name":"ASHA RAO","Remitter Account":"` + payer + `","Remitter IFSC":"HDFC0001234","User Reference Number":"` + utr + `","Mnemonic Code":"NEFT",` +
		`"Narration":"` + narration + `","Value Date":"` + sgD0 + `","Transaction Description":"NEFT","Transaction Date":"` + sgD0 + ` 10:15"}]}`)
	if res, err := o.BankCreditHookFrom(ctx, T, "ecms", "175.100.161.31", nil, body); err != nil || res.Added != 1 {
		t.Fatalf("e-CMS %s: %+v %v", utr, res, err)
	}
}

func ledgersOf(t *testing.T, o *Ops, ctx context.Context, ucc string) LedgerFunds {
	t.Helper()
	lf, err := o.clientLedgerFunds(ctx, T, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return lf[ucc]
}

func wantFunds(t *testing.T, got LedgerFunds, eq, com, wait string) {
	t.Helper()
	if got.Equity != inr(eq) || got.Commodity != inr(com) || got.Awaiting != inr(wait) {
		t.Fatalf("ledgers equity %s commodity %s awaiting %s, want %s %s %s", got.Equity, got.Commodity, got.Awaiting, eq, com, wait)
	}
}

// finspotCol5 is a client's column 5 (collateral / margin value) in the
// Finspot RMS limit file for date.
func finspotCol5(t *testing.T, o *Ops, ctx context.Context, date, ucc string) string {
	t.Helper()
	name, b, err := o.FinspotLimitFile(ctx, T, date)
	if err != nil {
		t.Fatal(err)
	}
	readFinspot(t, "finspot-rms-limit", name, b)
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\r\n")[1:] {
		f := strings.Split(l, "|")
		if f[0] == ucc {
			return f[4]
		}
	}
	t.Fatalf("no Finspot line for %s in %q", ucc, b)
	return ""
}

func TestFundsInstruction(t *testing.T) {
	s := Settings{SeparateCommodityLedger: true, BankAccountLedgers: map[string]string{"321234560277": "COM", "321234560198": "ASK"}}
	c := Client{UCC: "X1"}
	for _, x := range []struct{ to, narr, want string }{
		{sgCom, "", "COM"}, // the commodity collection account
		{sgAcct, "", ""},   // Ask: no instruction
		{sgAcct, "NEFT-HDFCN123-ASHA RAO-MCX MARGIN", "COM"},
		{sgAcct, "funds for f&o", "EQ"},
		{sgAcct, "COMMISSION REFUND", ""}, // COMM only as a whole word
		{sgAcct, "MCX 2000 NSE 3000", ""}, // both: a person decides
		{sgAcct, "equity", "EQ"},
		{sgCom, "NSE", "COM"}, // the account comes first
	} {
		if got, why := s.fundsInstruction(c, x.to, x.narr); got != x.want {
			t.Errorf("%s %q: %q (%s), want %q", x.to, x.narr, got, why, x.want)
		}
	}
	c.FundsLedger = "COM"
	if got, _ := s.fundsInstruction(c, sgAcct, ""); got != "COM" {
		t.Errorf("client default: %q", got)
	}
	s.EquityKeywords = []string{"SHARES"}
	if got, _ := s.fundsInstruction(Client{}, sgAcct, "for shares"); got != "EQ" {
		t.Errorf("firm's own keyword: %q", got)
	}
	if got, _ := s.fundsInstruction(Client{}, sgAcct, "NSE"); got != "" {
		t.Errorf("the firm's list replaces the defaults: %q", got)
	}
}

// The incident: ₹5,000 comes in by e-CMS for a client with ₹10,000 equity
// and ₹2,00,000 commodity. Finspot must never see the commodity money.
func TestSepLedgerIncident(t *testing.T) {
	t.Run("narration says MCX", func(t *testing.T) {
		o, ctx := sepSetup(t)
		wantFunds(t, ledgersOf(t, o, ctx, "BJ01"), "10000", "200000", "0")
		ecmsCredit(t, o, ctx, "S1", "UTR1", sgAcct, "50100011112222", "5000", "MCX MARGIN")
		if c := credit(t, o, ctx, "UTR1"); c.Status != "Matched" || c.Ledger != "COM" || !strings.Contains(c.LedgerWhy, "MCX") || c.Narration != "MCX MARGIN NEFT" || c.ToAccount != sgAcct {
			t.Fatalf("credit %+v", c)
		}
		wantFunds(t, ledgersOf(t, o, ctx, "BJ01"), "10000", "205000", "0")
		set, _ := o.Settings(ctx, T)
		next := set.nextBusinessDay(sgD0)
		if got := finspotCol5(t, o, ctx, next, "BJ01"); got != "10000.00" {
			t.Fatalf("Finspot column 5 %s, want the equity ledger 10000.00", got)
		}
		bow, _ := o.bowFunds(ctx, T, next)
		if bow["BJ01"].Cmp(mustDec("205000")) != 0 {
			t.Fatalf("BOW funds %s", bow["BJ01"])
		}
		if as, _ := o.FundsAwaitingList(ctx, T, true); len(as) != 0 {
			t.Fatalf("waiting %+v", as)
		}
	})
	t.Run("no instruction, Risk allocates to equity", func(t *testing.T) {
		o, ctx := sepSetup(t)
		ecmsCredit(t, o, ctx, "S2", "UTR2", sgAcct, "50100011112222", "5000", "")
		c := credit(t, o, ctx, "UTR2")
		if c.Ledger != ledger.LedgerAwaiting {
			t.Fatalf("credit %+v", c)
		}
		wantFunds(t, ledgersOf(t, o, ctx, "BJ01"), "10000", "200000", "5000")
		set, _ := o.Settings(ctx, T)
		next := set.nextBusinessDay(sgD0)
		// Waiting: in neither front end's limit, but in the client's ledger.
		if got := finspotCol5(t, o, ctx, next, "BJ01"); got != "10000.00" {
			t.Fatalf("Finspot column 5 while waiting %s", got)
		}
		if bow, _ := o.bowFunds(ctx, T, next); bow["BJ01"].Cmp(mustDec("200000")) != 0 {
			t.Fatalf("BOW funds while waiting %s", bow["BJ01"])
		}
		st, _ := o.clientStatement(ctx, T, "BJ01", "", next)
		if st.Closing != -inr("215000") {
			t.Fatalf("the client's ledger has the money: closing %s", st.Closing)
		}
		as, _ := o.FundsAwaitingList(ctx, T, true)
		if len(as) != 1 || as[0].UCC != "BJ01" || as[0].Amount != inr("5000") || as[0].Status != "Waiting" {
			t.Fatalf("waiting %+v", as)
		}
		ws, _ := o.WorkItems(ctx, T, "Open")
		found := false
		for _, w := range ws {
			if w.Process == wFundsAlloc && w.UCC == "BJ01" && w.Role == RoleRisk && strings.Contains(w.Title, "choose Equity or Commodity") && strings.Contains(w.Title, "5,000") {
				found = true
			}
		}
		if !found {
			t.Fatalf("no urgent work item for Risk: %+v", ws)
		}
		if _, err := o.AllocateFunds(ctx, T, "vikram.shah", as[0].ID, "EQ"); err == nil {
			t.Fatal("compliance allocated")
		}
		a, err := o.AllocateFunds(ctx, T, "anita.rao", as[0].ID, "EQ")
		if err != nil || a.Status != "Allocated" || a.Ledger != "EQ" || a.By != "anita.rao" {
			t.Fatalf("%+v %v", a, err)
		}
		if _, err := o.AllocateFunds(ctx, T, "anita.rao", as[0].ID, "COM"); err == nil {
			t.Fatal("allocated twice")
		}
		wantFunds(t, ledgersOf(t, o, ctx, "BJ01"), "15000", "200000", "0")
		if got := finspotCol5(t, o, ctx, next, "BJ01"); got != "15000.00" {
			t.Fatalf("Finspot column 5 %s, want 15000.00", got)
		}
		if bow, _ := o.bowFunds(ctx, T, next); bow["BJ01"].Cmp(mustDec("200000")) != 0 {
			t.Fatalf("BOW funds %s, want 200000 unchanged", bow["BJ01"])
		}
		if ws, _ := o.WorkItems(ctx, T, "Open"); len(ws) != 0 {
			for _, w := range ws {
				if w.Process == wFundsAlloc {
					t.Fatalf("work item still open: %+v", w)
				}
			}
		}
		// The client's statement: one ledger, one running balance, no
		// allocation line.
		st, _ = o.clientStatement(ctx, T, "BJ01", "", next)
		if st.Account != "CLIENT:BJ01" || st.Closing != -inr("215000") || len(st.Lines) != 3 {
			t.Fatalf("client statement %+v", st)
		}
		run := money.Amount(0)
		for _, l := range st.Lines {
			run += l.Debit - l.Credit
			if l.Balance != run || l.Ledger != "" || strings.Contains(l.Narration, "Allocation") {
				t.Fatalf("line %+v (running %s)", l, run)
			}
		}
		if st.Lines[2].Credit != inr("5000") || !strings.Contains(st.Lines[2].Narration, "Funds received") {
			t.Fatalf("receipt line %+v", st.Lines[2])
		}
		// Staff see every line, marked with its ledger.
		ss, _ := o.staffStatement(ctx, T, "BJ01", "", next)
		if len(ss.Lines) != 5 || ss.Closing != st.Closing || ss.Lines[1].Ledger != "COM" {
			t.Fatalf("staff statement %+v", ss.Lines)
		}
		d, err := o.ClientDetail(ctx, T, "BJ01", next)
		if err != nil || d.Ledgers == nil || d.Ledgers.Equity.Cmp(mustDec("15000")) != 0 || d.Ledgers.Commodity.Cmp(mustDec("200000")) != 0 || d.Ledgers.Combined.Cmp(mustDec("215000")) != 0 {
			t.Fatalf("drawer %+v %v", d.Ledgers, err)
		}
	})
	t.Run("paid into the commodity account", func(t *testing.T) {
		o, ctx := sepSetup(t)
		set, _ := o.Settings(ctx, T)
		set.BankAccountLedgers = map[string]string{sgCom: "Commodity", sgAcct: "ask"}
		if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
			t.Fatal(err)
		}
		if set, _ = o.Settings(ctx, T); set.BankAccountLedgers["321234560277"] != "COM" || set.BankAccountLedgers["321234560198"] != "ASK" {
			t.Fatalf("mapping %v", set.BankAccountLedgers)
		}
		ecmsCredit(t, o, ctx, "S3", "UTR3", sgCom, "50100011112222", "5000", "")
		wantFunds(t, ledgersOf(t, o, ctx, "BJ01"), "10000", "205000", "0")
	})
	t.Run("left waiting past the firm's limit", func(t *testing.T) {
		o, ctx := sepSetup(t)
		set, _ := o.Settings(ctx, T)
		set.AllocateAfterMinutes, set.AllocateAfterTo = 30, "Commodity"
		if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
			t.Fatal(err)
		}
		ecmsCredit(t, o, ctx, "S4", "UTR4", sgAcct, "50100011112222", "5000", "")
		o.fundsAllocTick(ctx, T, o.now().Add(10*60e9))
		wantFunds(t, ledgersOf(t, o, ctx, "BJ01"), "10000", "200000", "5000")
		o.fundsAllocTick(ctx, T, o.now().Add(31*60e9))
		wantFunds(t, ledgersOf(t, o, ctx, "BJ01"), "10000", "205000", "0")
		as, _ := o.FundsAwaitingList(ctx, T, false)
		if len(as) != 1 || !as[0].Auto || as[0].Ledger != "COM" {
			t.Fatalf("%+v", as)
		}
	})
}

// Turning the setting on routes MCX bills, carried MCX futures and
// receipts to the commodity ledger and changes nothing else: per client,
// the combined ledger and the client's statement are what one ledger
// gives, and Finspot sees only the equity ledger.
func TestSepLedgerSampleDay(t *testing.T) {
	bill := func(sep bool) (*Ops, context.Context) {
		o, ctx := setup(t)
		if sep {
			set, _ := o.Settings(ctx, T)
			set.SeparateCommodityLedger = true
			if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
				t.Fatal(err)
			}
		}
		m, _, err := o.Market(ctx, T, D)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := o.App.RunBilling(ctx, T, "priya.nair", D, m.SettlementPrices()); err != nil {
			t.Fatal(err)
		}
		return o, ctx
	}
	off, ctx := bill(false)
	on, ctx2 := bill(true)
	es, _ := on.App.Ledger.Store.List(ctx2, T, ledger.Filter{})
	mcx := 0
	for _, e := range es {
		if e.Source.Kind != "BILL" {
			continue
		}
		com := strings.HasSuffix(e.Source.ID, "/MCX-COM")
		for _, p := range e.Postings {
			_, kind, ok := ledger.ClientCode(p.Account)
			if !ok {
				continue
			}
			if com != (kind == ledger.LedgerCommodity) {
				t.Fatalf("bill %s posts to %s", e.Source.ID, p.Account)
			}
			if com {
				mcx++
			}
		}
	}
	if mcx == 0 {
		t.Fatal("the sample day has no MCX bills")
	}
	fOff, _ := off.clientFunds(ctx, T, D, "")
	fOn, _ := on.clientFunds(ctx2, T, D, "")
	lfOn, _ := on.clientLedgerFunds(ctx2, T, D, "")
	withCom := 0
	for u, a := range fOff {
		if fOn[u] != a {
			t.Fatalf("%s: combined %s, one ledger %s", u, fOn[u], a)
		}
		if lfOn[u].Commodity != 0 {
			withCom++
		}
		so, _ := off.clientStatement(ctx, T, u, "", D)
		sn, _ := on.clientStatement(ctx2, T, u, "", D)
		if len(so.Lines) != len(sn.Lines) || so.Closing != sn.Closing {
			t.Fatalf("%s: statement %d lines %s, one ledger %d lines %s", u, len(sn.Lines), sn.Closing, len(so.Lines), so.Closing)
		}
		for i := range so.Lines {
			a, b := so.Lines[i], sn.Lines[i]
			if a.Debit != b.Debit || a.Credit != b.Credit || a.Balance != b.Balance || a.Narration != b.Narration || a.Memo != b.Memo || a.ValueDate != b.ValueDate {
				t.Fatalf("%s line %d: %+v vs %+v", u, i, b, a)
			}
		}
		// The merge gives exactly the one ledger's statement.
		raw, _ := off.App.Ledger.Statement(ctx, T, ledger.ClientAccount(u), "", D)
		merged, _ := off.App.Ledger.ClientStatement(ctx, T, u, "", D, false)
		if len(raw.Lines) != len(merged.Lines) || raw.Closing != merged.Closing {
			t.Fatalf("%s: ClientStatement differs from Statement with one ledger", u)
		}
		for i := range raw.Lines {
			if raw.Lines[i] != merged.Lines[i] {
				t.Fatalf("%s line %d: %+v vs %+v", u, i, merged.Lines[i], raw.Lines[i])
			}
		}
	}
	if withCom == 0 {
		t.Fatal("no client has a commodity balance")
	}
	// Risk: the row is the equity ledger against NSE and BSE margin;
	// commodity margin moves to Ledgers, and nothing is lost.
	rOff, _ := off.Risk(ctx, T, D)
	rOn, _ := on.Risk(ctx2, T, D)
	byU := map[string]RiskRow{}
	for _, r := range rOn.Rows {
		byU[r.UCC] = r
	}
	for _, r := range rOff.Rows {
		n := byU[r.UCC]
		if n.Ledgers == nil || n.Ledgers.Combined.Cmp(r.Funds) != 0 || n.allRequired().Cmp(r.Required) != 0 || n.Funds.Cmp(n.Ledgers.Equity) != 0 {
			t.Fatalf("%s: risk %+v %+v vs one ledger funds %s required %s", r.UCC, n, n.Ledgers, r.Funds, r.Required)
		}
	}
	// Finspot: the equity ledger only.
	set, _ := on.Settings(ctx2, T)
	next := set.nextBusinessDay(D)
	fs, _ := on.finspotFunds(ctx2, T, next)
	for u, f := range lfOn {
		if fs[u].Cmp(f.Equity.Dec()) != 0 && on.uncleared(ctx2, T)[u] == 0 {
			t.Fatalf("%s: Finspot %s, equity %s, commodity %s", u, fs[u], f.Equity, f.Commodity)
		}
	}
	// Allocation: NSE Clearing's segments from the equity ledger, MCX
	// from the commodity ledger; the MCX margin reporting reads COM.
	al, _ := on.allocationBase(ctx2, T, D)
	for _, a := range al {
		r := byU[a.UCC]
		if a.Segment == "COM" {
			if a.Cash.Cmp(money.Max(r.Ledgers.Commodity, money.Dec{})) != 0 || a.NonCash.Sign() != 0 {
				t.Fatalf("COM allocation %+v, commodity %s", a, r.Ledgers.Commodity)
			}
		}
	}
	var eqCash money.Dec
	for _, a := range al {
		if a.Segment != "COM" && a.UCC == rOn.Rows[0].UCC {
			eqCash = eqCash.Add(a.Cash)
		}
	}
	if r := rOn.Rows[0]; eqCash.Cmp(money.Max(r.Funds, money.Dec{})) != 0 {
		t.Fatalf("%s: equity allocation %s, equity ledger %s", r.UCC, eqCash, r.Funds)
	}
}

// A transfer between a client's ledgers is a journal a checker approves;
// the client never sees it.
func TestSepLedgerTransfer(t *testing.T) {
	o, ctx := sepSetup(t)
	if _, err := o.LedgerTransfer(ctx, T, "anita.rao", "BJ01", "COM", "COM", sgD0, inr("1000"), ""); err == nil {
		t.Fatal("transfer to the same ledger")
	}
	v, err := o.LedgerTransfer(ctx, T, "anita.rao", "BJ01", "COM", "EQ", sgD0, inr("40000"), "client asked")
	if err != nil || v.Type != "Journal" || v.Status != "Pending" || len(v.Lines) != 2 || v.Lines[0].Account != "CLIENTCOM:BJ01" || v.Lines[1].Account != "CLIENT:BJ01" {
		t.Fatalf("%+v %v", v, err)
	}
	wantFunds(t, ledgersOf(t, o, ctx, "BJ01"), "10000", "200000", "0") // not until approved
	if _, err := o.DecideVoucher(ctx, T, "farhan.khan", v.No, "approve", ""); err != nil {
		t.Fatal(err)
	}
	wantFunds(t, ledgersOf(t, o, ctx, "BJ01"), "50000", "160000", "0")
	st, _ := o.clientStatement(ctx, T, "BJ01", "", sgD0)
	for _, l := range st.Lines {
		if strings.Contains(l.Narration, "Transfer") {
			t.Fatalf("the client sees the transfer: %+v", l)
		}
	}
	if st.Closing != -inr("210000") || len(st.Lines) != 2 {
		t.Fatalf("%+v", st)
	}
	// A journal may name the commodity ledger only when it is kept.
	o2, ctx2 := newOps(t)
	_, err = o2.SaveVoucher(ctx2, T, "priya.nair", Voucher{Type: "Journal", Date: sgD0, Narration: "x", Lines: []VoucherLine{{Account: "CLIENTCOM:BJ01", Debit: 100}, {Account: "CLIENT:BJ01", Credit: 100}}})
	if err == nil || !strings.Contains(err.Error(), "one ledger per client") {
		t.Fatalf("commodity line with one ledger: %v", err)
	}
	if _, err := o2.LedgerTransfer(ctx2, T, "anita.rao", "BJ01", "COM", "EQ", sgD0, 100, ""); err == nil {
		t.Fatal("transfer with one ledger")
	}
}

// A payout from one ledger never takes it below zero to pay from the
// other: refused, unless the firm lets the payout transfer the shortfall.
func TestSepLedgerPayout(t *testing.T) {
	o, ctx := sepSetup(t)
	w, err := o.Withdrawable(ctx, T, sgD0, "BJ01")
	if err != nil || w.Ledgers == nil || w.Ledgers.Equity != inr("10000") || w.Ledgers.Commodity != inr("200000") || w.Available != inr("210000") {
		t.Fatalf("%+v %+v %v", w, w.Ledgers, err)
	}
	_, err = o.RequestPayoutFrom(ctx, T, "priya.nair", sgD0, "BJ01", inr("50000"), "Head office", "EQ")
	if err == nil || !strings.Contains(err.Error(), "Equity ledger can pay") || !strings.Contains(err.Error(), "40,000") {
		t.Fatalf("equity payout beyond the equity ledger: %v", err)
	}
	p, err := o.RequestPayoutFrom(ctx, T, "priya.nair", sgD0, "BJ01", inr("8000"), "Head office", "EQ")
	if err != nil || p.Ledger != "EQ" {
		t.Fatalf("%+v %v", p, err)
	}
	set, _ := o.Settings(ctx, T)
	set.PayoutAutoTransfer = true
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	p2, err := o.RequestPayoutFrom(ctx, T, "priya.nair", sgD0, "BJ01", inr("50000"), "Head office", "EQ")
	if err != nil {
		t.Fatal(err)
	}
	p3, err := o.RequestPayoutFrom(ctx, T, "priya.nair", sgD0, "BJ02", inr("20000"), "Head office", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{p.ID, p2.ID, p3.ID} {
		x, _, _ := docs.Get[PayoutRequest](ctx, o.Docs, T, cPayout, id)
		if x.Status == "Approved" {
			continue // approved by Autopilot when requested (with the same checks)
		}
		if _, err := o.DecidePayout(ctx, T, "farhan.khan", sgD0, id, true, ""); err != nil {
			t.Fatal(id, err)
		}
	}
	b, err := o.CreatePayoutBatch(ctx, T, "farhan.khan", sgD0)
	if err != nil || len(b.Items) != 3 {
		t.Fatalf("%+v %v", b, err)
	}
	// 8,000 from equity; then 50,000 from equity, 48,000 of it transferred
	// in from commodity first (only 2,000 was left).
	wantFunds(t, ledgersOf(t, o, ctx, "BJ01"), "0", "152000", "0")
	wantFunds(t, ledgersOf(t, o, ctx, "BJ02"), "30000", "0", "0")
	x, _, _ := docs.Get[PayoutRequest](ctx, o.Docs, T, cPayout, p2.ID)
	if x.Transfer == "" {
		t.Fatalf("no transfer recorded %+v", x)
	}
	e, _ := o.App.Ledger.Store.Get(ctx, T, x.Transfer)
	if e.Source.Kind != srcLedgerXfer || e.Postings[0].Account != "CLIENTCOM:BJ01" || e.Postings[0].Debit != inr("48000") {
		t.Fatalf("transfer %+v", e)
	}
	st, _ := o.clientStatement(ctx, T, "BJ01", "", sgD0)
	if st.Closing != -inr("152000") {
		t.Fatalf("closing %s", st.Closing)
	}
	for _, l := range st.Lines {
		if strings.Contains(l.Narration, "Transfer") {
			t.Fatalf("the client sees %+v", l)
		}
	}
	// Either ledger: each pays only up to its own credit, the larger first.
	legs, _, _, err := payoutLegs(LedgerFunds{Equity: inr("1000"), Commodity: inr("5000")}, inr("5500"), "", false)
	if err != nil || legs["COM"] != inr("5000") || legs["EQ"] != inr("500") {
		t.Fatalf("%v %v", legs, err)
	}
	if _, _, _, err := payoutLegs(LedgerFunds{Equity: inr("1000"), Commodity: inr("5000"), Awaiting: inr("900")}, inr("6500"), "", false); err == nil {
		t.Fatal("paid money awaiting allocation")
	}
}

// Running account settlement looks at the client as a whole and never
// leaves a ledger below zero.
func TestSepLedgerRAS(t *testing.T) {
	o, ctx := sepSetup(t)
	// BJ02 owes ₹5,000 in equity and has ₹20,000 in commodity.
	csv := "Account,Debit,Credit,Ledger\nCLIENT:BJ02,55000.00,,Equity\nCLIENT:BJ02,,20000.00,Commodity\nEQUITY:OPENING,,35000.00,\n"
	if _, err := o.LoadFile(ctx, T, "admin", "OPENING-BALANCES", sgOpen, "ob2.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	wantFunds(t, ledgersOf(t, o, ctx, "BJ02"), "-5000", "20000", "0")
	set, _ := o.Settings(ctx, T)
	set.RASMonthly = "2026-10-30"
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	v, err := o.RAS(ctx, T, sgD0)
	if err != nil {
		t.Fatal(err)
	}
	pay := map[string]money.Dec{}
	for _, r := range v.Rows {
		pay[r.UCC] = r.Payout
	}
	if pay["BJ01"].Cmp(mustDec("210000")) != 0 || pay["BJ02"].Cmp(mustDec("15000")) != 0 {
		t.Fatalf("payouts %v", pay)
	}
	if _, err := o.RunRAS(ctx, T, "farhan.khan", sgD0, "2026-10-30"); err != nil {
		t.Fatal(err)
	}
	wantFunds(t, ledgersOf(t, o, ctx, "BJ01"), "0", "0", "0")
	wantFunds(t, ledgersOf(t, o, ctx, "BJ02"), "0", "0", "0")
}

// The NSE Clearing allocation (and so NCL margin reporting, SEGMON) is
// the equity ledger; MCX's is the commodity ledger, cash only.
func TestSepLedgerAllocation(t *testing.T) {
	o, ctx := sepSetup(t)
	al, err := o.Allocation(ctx, T, sgD0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range al {
		got[a.UCC+"|"+a.Segment] = a.Cash.Fixed(2)
	}
	if got["BJ01|CM"] != "10000.00" || got["BJ01|COM"] != "200000.00" || got["BJ02|CM"] != "50000.00" || got["BJ02|COM"] != "" {
		t.Fatalf("allocation %v", got)
	}
	bs, b := segmonBalancesOf(t, o, ctx)
	if bs != "60000.00" || b {
		t.Fatalf("SEGMON reads %s (commodity in it: %v)", bs, b)
	}
}

// MCXCCL's margin reporting collects from the commodity ledger: ₹2,00,000,
// not the equity ledger's ₹10,000.
func TestSepLedgerMCXMarginReporting(t *testing.T) {
	o, ctx := sepSetup(t)
	mrg := strings.Join([]string{"29OCT2026", "56845", "BJ01", "1000.00", "200.00", "50.00", "", "", "", "", "", "100.00", "1000.00", "", "", "0.00", "0.00", "", ""}, ",") + "\n"
	loadX(t, o, "MCX_MARGIN_56845_20261029.csv", []byte(mrg))
	name, b, err := o.ExchangeFile(ctx, T, "priya.nair", "mcx-margin-reporting", sgD0, XFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := XFiles()
	out, _ := lib.Get("mcx-margin-reporting")
	tb, err := out.Read(name, b)
	if err != nil || len(tb.Issues) != 0 || len(tb.Rows) != 1 {
		t.Fatalf("%v %+v %q", err, tb.Issues, b)
	}
	v := tb.Rows[0].Values
	if v["MTMCollected"] != "50.00" || v["IMCollected"] != "1000.00" || v["OtherMarginCollected"] != "198950.00" || v["PeakMarginCollected"] != "200000.00" {
		t.Fatalf("collected from the wrong ledger: %v", v)
	}
}

func segmonBalancesOf(t *testing.T, o *Ops, ctx context.Context) (string, bool) {
	t.Helper()
	bals, err := o.segmonBalances(ctx, T, sgD0)
	if err != nil {
		t.Fatal(err)
	}
	var tot money.Dec
	com := false
	for acct, b := range bals {
		if strings.HasPrefix(acct, "CLIENTCOM:") {
			com = true
		}
		tot = tot.Add(b.A)
	}
	return tot.Fixed(2), com
}

// A receipt is for the ledger the maker chose; its uncleared cheque stays
// out of that ledger's front end only.
func TestSepLedgerReceipt(t *testing.T) {
	o, ctx := sepSetup(t)
	r, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{UCC: "BJ01", Mode: "Cheque", Ref: "000111", Date: sgD0, Amount: inr("7000"), Ledger: "Commodity"})
	if err != nil || r.Ledger != "COM" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := o.DecideReceipt(ctx, T, "farhan.khan", r.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	wantFunds(t, ledgersOf(t, o, ctx, "BJ01"), "10000", "207000", "0")
	set, _ := o.Settings(ctx, T)
	next := set.nextBusinessDay(sgD0)
	if bow, _ := o.bowFunds(ctx, T, next); bow["BJ01"].Cmp(mustDec("200000")) != 0 {
		t.Fatalf("uncleared commodity cheque in BOW: %s", bow["BJ01"])
	}
	if got := finspotCol5(t, o, ctx, next, "BJ01"); got != "10000.00" {
		t.Fatalf("Finspot %s", got)
	}
	b, err := o.BOWFundsFile(ctx, T, next)
	if err != nil || !strings.Contains(string(b), next+",BJ01,200000.00") {
		t.Fatalf("%s %v", b, err)
	}
	// With one ledger a receipt cannot be for the commodity ledger.
	o2, ctx2 := creditSetup(t)
	if _, err := o2.RecordReceipt(ctx2, T, "priya.nair", Receipt{UCC: "BC01", Mode: "NEFT", Ref: "U1", Date: sgD0, Amount: 100, Ledger: "COM"}); err == nil {
		t.Fatal("commodity receipt with one ledger")
	}
	if r, err := o2.RecordReceipt(ctx2, T, "priya.nair", Receipt{UCC: "BC01", Mode: "NEFT", Ref: "U2", Date: sgD0, Amount: 100, Ledger: "Equity"}); err != nil || r.Ledger != "" {
		t.Fatalf("%+v %v", r, err)
	}
}

// Opening balances: the Ledger column, refused without the setting; and
// the setting cannot be turned off while commodity money is held.
func TestSepLedgerOpeningAndSwitch(t *testing.T) {
	o, ctx := newOps(t)
	for _, csv := range []string{
		"Account,Debit,Credit,Ledger\nCLIENT:BJ01,,100.00,Commodity\nEQUITY:OPENING,100.00,,\n",
		"Account,Debit,Credit\nCLIENTCOM:BJ01,,100.00\nEQUITY:OPENING,100.00,\n",
	} {
		if _, err := o.LoadFile(ctx, T, "admin", "OPENING-BALANCES", sgOpen, "ob.csv", []byte(csv)); err == nil {
			t.Fatalf("commodity balance with one ledger: %q", csv)
		}
	}
	o, ctx = sepSetup(t)
	if _, err := o.LoadFile(ctx, T, "admin", "OPENING-BALANCES", sgOpen, "x.csv", []byte("Account,Debit,Credit,Ledger\nCLIENTCOM:BJ02,,1.00,Equity\nEQUITY:OPENING,1.00,,\n")); err == nil {
		t.Fatal("CLIENTCOM given as the equity ledger")
	}
	if _, err := o.LoadFile(ctx, T, "admin", "OPENING-BALANCES", sgOpen, "y.csv", []byte("Account,Debit,Credit,Ledger\nCLIENT:BJ02,,1.00,Gold\nEQUITY:OPENING,1.00,,\n")); err == nil {
		t.Fatal("unknown ledger")
	}
	set, _ := o.Settings(ctx, T)
	set.SeparateCommodityLedger = false
	if err := o.SaveSettings(ctx, T, "admin", set); err == nil || !strings.Contains(err.Error(), "BJ01") {
		t.Fatalf("turned off with commodity money held: %v", err)
	}
}
