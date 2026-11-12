package ops

import (
	"context"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// One ledger with MCX money earmarked (mcxearmark.go).

// emSetup is Baljit on one ledger: BJ01 has ₹10,000 (opening balance) and
// pays ₹2,00,000 into the firm's MCX collection account by e-CMS; BJ02
// has ₹50,000. With mcx set, sgCom is the MCX-only bank account.
func emSetup(t *testing.T, mcx bool) (*Ops, context.Context) {
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
	if mcx {
		set, _ := o.Settings(ctx, T)
		set.MCXOnlyBankAccounts = []string{sgCom}
		if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
			t.Fatal(err)
		}
	}
	csv := "Account,Debit,Credit\nCLIENT:BJ01,,10000.00\nCLIENT:BJ02,,50000.00\nEQUITY:OPENING,60000.00,\n"
	if _, err := o.LoadFile(ctx, T, "admin", "OPENING-BALANCES", sgOpen, "opening.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connCreditsECMS, map[string]string{"accounts": sgAcct + "," + sgCom}, nil); err != nil {
		t.Fatal(err)
	}
	ecmsCredit(t, o, ctx, "S0", "UTR0", sgCom, "50100011112222", "200000", "MARGIN")
	return o, ctx
}

func earmarkOf(t *testing.T, o *Ops, ctx context.Context, date, ucc string) MCXEarmark {
	t.Helper()
	em, err := o.mcxEarmarks(ctx, T, date, "")
	if err != nil {
		t.Fatal(err)
	}
	return em[ucc]
}

func wantEarmark(t *testing.T, got MCXEarmark, ledgerBal, mcx string) {
	t.Helper()
	if got.Ledger != inr(ledgerBal) || got.MCX != inr(mcx) || got.Equity != inr(ledgerBal)-inr(mcx) {
		t.Fatalf("ledger %s MCX %s equity %s, want ledger %s MCX %s", got.Ledger, got.MCX, got.Equity, ledgerBal, mcx)
	}
}

// postMCXBill posts an MCX bill for ucc: positive is a profit.
func postMCXBill(t *testing.T, o *Ops, ctx context.Context, date, ucc, exch, seg string, net money.Amount) {
	t.Helper()
	cl, cc := ledger.ClientAccount(ucc), ledger.ClearingAccount(exch, seg)
	ps := []ledger.Posting{{Account: cc, Debit: net}, {Account: cl, Credit: net}}
	if net < 0 {
		ps = []ledger.Posting{{Account: cl, Debit: -net}, {Account: cc, Credit: -net}}
	}
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "BILLX:" + date + ":" + ucc + ":" + exch + seg + net.String(), ValueDate: date, CreatedBy: "t",
		Narration: "Bill", Source: ledger.SourceRef{Kind: "BILL", ID: date + "/" + ucc + "/" + exch + "-" + seg}, Postings: ps}); err != nil {
		t.Fatal(err)
	}
}

// The incident on one ledger: ₹5,000 by e-CMS into the usual account
// counts for Finspot; into the MCX-only account it does not.
func TestEarmarkIncident(t *testing.T) {
	t.Run("usual account", func(t *testing.T) {
		o, ctx := emSetup(t, true)
		ecmsCredit(t, o, ctx, "S1", "UTR1", sgAcct, "50100011112222", "5000", "MCX MARGIN")
		if c := credit(t, o, ctx, "UTR1"); c.Status != "Matched" || c.Earmark != "" || c.ToAccount != "" || c.Ledger != "" {
			t.Fatalf("credit %+v", c)
		}
		set, _ := o.Settings(ctx, T)
		next := set.nextBusinessDay(sgD0)
		if got := finspotCol5(t, o, ctx, next, "BJ01"); got != "15000.00" {
			t.Fatalf("Finspot column 5 %s, want 15000.00", got)
		}
		wantEarmark(t, earmarkOf(t, o, ctx, "", "BJ01"), "215000", "200000")
	})
	t.Run("MCX-only account", func(t *testing.T) {
		o, ctx := emSetup(t, true)
		if c := credit(t, o, ctx, "UTR0"); c.Earmark != EarmarkMCX || c.ToAccount != sgCom {
			t.Fatalf("credit %+v", c)
		}
		ecmsCredit(t, o, ctx, "S1", "UTR1", sgCom, "50100011112222", "5000", "")
		set, _ := o.Settings(ctx, T)
		next := set.nextBusinessDay(sgD0)
		if got := finspotCol5(t, o, ctx, next, "BJ01"); got != "10000.00" {
			t.Fatalf("Finspot column 5 %s, want 10000.00", got)
		}
		wantEarmark(t, earmarkOf(t, o, ctx, "", "BJ01"), "215000", "205000")
		// The accounts are one ledger: CLIENT:BJ01 only.
		if lf := ledgersOf(t, o, ctx, "BJ01"); lf.Equity != inr("215000") || lf.Commodity != 0 {
			t.Fatalf("ledger %+v", lf)
		}
		bow, _ := o.bowFunds(ctx, T, next)
		if bow["BJ01"].Cmp(mustDec("205000")) != 0 {
			t.Fatalf("BOW commodity funds %s", bow["BJ01"])
		}
	})
	t.Run("option unused", func(t *testing.T) {
		o, ctx := emSetup(t, false)
		ecmsCredit(t, o, ctx, "S1", "UTR1", sgCom, "50100011112222", "5000", "")
		if c := credit(t, o, ctx, "UTR1"); c.Earmark != "" || c.ToAccount != "" || c.Narration != "" {
			t.Fatalf("credit %+v", c)
		}
		set, _ := o.Settings(ctx, T)
		next := set.nextBusinessDay(sgD0)
		if got := finspotCol5(t, o, ctx, next, "BJ01"); got != "215000.00" {
			t.Fatalf("Finspot column 5 %s, want the whole ledger 215000.00", got)
		}
		rv, _ := o.Risk(ctx, T, sgD0)
		for _, r := range rv.Rows {
			if r.Ledgers != nil {
				t.Fatalf("risk row split without the option: %+v", r)
			}
		}
		w, _ := o.Withdrawable(ctx, T, sgD0, "BJ01")
		if w.Ledgers != nil || w.Available != inr("215000") {
			t.Fatalf("withdrawable %+v", w)
		}
		if _, err := o.RequestPayoutFrom(ctx, T, "priya.nair", sgD0, "BJ01", 100, "Head office", "COM"); err == nil {
			t.Fatal("MCX payout without the option")
		}
		if _, err := o.RequestMCXMove(ctx, T, "anita.rao", "BJ01", "EQ", sgD0, 100, ""); err == nil {
			t.Fatal("move without the option")
		}
		if _, err := o.BOWFundsFile(ctx, T, next); err == nil {
			t.Fatal("BOW funds without the option")
		}
		d, err := o.ClientDetail(ctx, T, "BJ01", sgD0)
		if err != nil || d.Earmark != nil || d.Ledgers != nil {
			t.Fatalf("drawer %+v %v", d.Earmark, err)
		}
	})
}

// MCX losses and charges take from the earmark first; profits add to it;
// equity bills do not touch it.
func TestEarmarkBills(t *testing.T) {
	o, ctx := emSetup(t, true)
	set, _ := o.Settings(ctx, T)
	d1 := set.nextBusinessDay(sgD0)
	postMCXBill(t, o, ctx, d1, "BJ01", "MCX", "COM", -inr("50000"))
	wantEarmark(t, earmarkOf(t, o, ctx, d1, "BJ01"), "160000", "150000")
	postMCXBill(t, o, ctx, d1, "BJ01", "MCX", "COM", inr("12000"))
	wantEarmark(t, earmarkOf(t, o, ctx, d1, "BJ01"), "172000", "162000")
	postMCXBill(t, o, ctx, d1, "BJ01", "NSE", "FO", inr("3000")) // equity profit: equity money
	wantEarmark(t, earmarkOf(t, o, ctx, d1, "BJ01"), "175000", "162000")
	// Before the day's bills (as live risk and margin collection read it).
	em, _ := o.mcxEarmarks(ctx, T, d1, d1)
	if e := em["BJ01"]; e.MCX != inr("200000") || e.Ledger != inr("210000") {
		t.Fatalf("before the day's bills %+v", e)
	}
	// The day before is untouched.
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "BJ01"), "210000", "200000")
	// Finspot on the next day: the ledger less the earmark.
	if got := finspotCol5(t, o, ctx, set.nextBusinessDay(d1), "BJ01"); got != "13000.00" {
		t.Fatalf("Finspot column 5 %s, want 13000.00", got)
	}
}

// computeEarmarks: floored at zero (a larger MCX loss is not owed back by
// later MCX money), capped at the ledger credit, reversals follow their
// entry, moves, and the bills left out.
func TestComputeEarmarks(t *testing.T) {
	seq := int64(0)
	ent := func(id, date, kind, src string, amt money.Amount, rev string) ledger.Entry {
		seq++
		p := []ledger.Posting{{Account: "CLIENT:C1", Credit: amt}}
		if amt < 0 {
			p = []ledger.Posting{{Account: "CLIENT:C1", Debit: -amt}}
		}
		return ledger.Entry{ID: id, Seq: seq, ValueDate: date, Source: ledger.SourceRef{Kind: kind, ID: src}, Postings: p, ReversalOf: rev}
	}
	in := map[string]bool{"R1": true, "R2": true, "P1": true}
	es := []ledger.Entry{
		ent("O", "2026-11-02", "OPENING", "", inr("10000"), ""),
		ent("R1", "2026-11-02", "BANK", "U1", inr("100000"), ""), // MCX receipt
		ent("B1", "2026-11-03", "BILL", "2026-11-03/C1/MCX-COM", -inr("150000"), ""),
		ent("R2", "2026-11-04", "BANK", "U2", inr("30000"), ""), // MCX receipt after the loss
		ent("E1", "2026-11-05", "BILL", "2026-11-05/C1/NSE-FO", -inr("20000"), ""),
		ent("X1", "2026-11-06", "REVERSAL", "R2", -inr("30000"), "R2"), // the MCX receipt returned
	}
	got := func(asOf, excl string, moves ...MCXMove) MCXEarmark {
		var xs []ledger.Entry
		for _, e := range es {
			if asOf == "" || e.ValueDate <= asOf {
				xs = append(xs, e)
			}
		}
		return computeEarmarks(xs, in, moves, excl)["C1"]
	}
	wantEarmark(t, got("2026-11-02", ""), "110000", "100000")
	// The loss of 1,50,000 takes the 1,00,000 and 50,000 of other money.
	wantEarmark(t, got("2026-11-03", ""), "-40000", "0")
	wantEarmark(t, got("2026-11-03", "2026-11-03"), "110000", "100000")
	// New MCX money after the loss is MCX money, all of it.
	wantEarmark(t, got("2026-11-04", ""), "-10000", "0") // but never above the ledger credit
	if e := got("2026-11-04", ""); e.Raw != inr("30000") {
		t.Fatalf("raw %s", e.Raw)
	}
	// A move to MCX and a cap: ledger 50,000 after an equity receipt.
	es = append(es[:4], ent("R3", "2026-11-04", "BANK", "U3", inr("60000"), ""))
	wantEarmark(t, got("2026-11-04", ""), "50000", "30000")
	es = append(es, ent("E1", "2026-11-05", "BILL", "2026-11-05/C1/NSE-FO", -inr("40000"), ""))
	wantEarmark(t, got("2026-11-05", ""), "10000", "10000") // the equity loss leaves 10,000: the cap
	es = append(es, ent("R4", "2026-11-06", "BANK", "U4", inr("50000"), ""))
	wantEarmark(t, got("2026-11-06", ""), "60000", "30000") // equity money in makes good the equity debit first
	// Moves: to equity frees what it says even when capped; pending moves
	// do not count.
	mv := func(id, date, to, amt, status string) MCXMove {
		return MCXMove{ID: id, Date: date, UCC: "C1", To: to, Amount: inr(amt), Status: status}
	}
	wantEarmark(t, got("2026-11-06", "", mv("M1", "2026-11-06", "EQ", "20000", "Approved")), "60000", "10000")
	wantEarmark(t, got("2026-11-06", "", mv("M1", "2026-11-06", "EQ", "20000", "Pending")), "60000", "30000")
	wantEarmark(t, got("2026-11-06", "", mv("M1", "2026-11-06", "MCX", "25000", "Approved")), "60000", "55000")
	wantEarmark(t, got("2026-11-05", "", mv("M1", "2026-11-05", "EQ", "50000", "Approved")), "10000", "0")
	// A reversal of an MCX receipt takes it back out.
	es = append(es, ent("X3", "2026-11-07", "REVERSAL", "R3", -inr("60000"), "R3"), ent("X1", "2026-11-07", "REVERSAL", "R1", -inr("100000"), "R1"))
	wantEarmark(t, got("2026-11-07", ""), "-100000", "0")
	for _, x := range []struct {
		src  string
		want bool
	}{{"2026-11-03/C1/MCX-COM", true}, {"2026-11-03/C1/NSE-FO", false}, {"2026-11-03/C1/BSE-CM", false}, {"2026-11-03/C1/NSE-COM", true}} {
		if got := mcxSegmentEntry(ledger.Entry{Source: ledger.SourceRef{Kind: "MTM", ID: x.src}}); got != x.want {
			t.Errorf("%s: %v", x.src, got)
		}
	}
	if mcxSegmentEntry(ledger.Entry{Source: ledger.SourceRef{Kind: "CHARGE", ID: "MCX-COM"}}) {
		t.Error("a charge with no segment is MCX")
	}
}

// Payouts: equity money may not use MCX money; MCX money is paid from
// the MCX-only account and uses the earmark first; an RMS move (maker-
// checker) frees MCX money for equity.
func TestEarmarkPayoutsAndMoves(t *testing.T) {
	o, ctx := emSetup(t, true)
	w, err := o.Withdrawable(ctx, T, sgD0, "BJ01")
	if err != nil || w.Ledgers == nil || w.Ledgers.Equity != inr("10000") || w.Ledgers.Commodity != inr("200000") || w.Available != inr("210000") {
		t.Fatalf("%+v %+v %v", w, w.Ledgers, err)
	}
	_, err = o.RequestPayoutFrom(ctx, T, "priya.nair", sgD0, "BJ01", inr("50000"), "Head office", "")
	if err == nil || !strings.Contains(err.Error(), "MCX money") || !strings.Contains(err.Error(), "40,000") || !strings.Contains(err.Error(), "2,00,000") {
		t.Fatalf("equity payout using MCX money: %v", err)
	}
	// RMS moves ₹40,000 to equity: not until a second person approves.
	m, err := o.RequestMCXMove(ctx, T, "anita.rao", "BJ01", "EQ", sgD0, inr("40000"), "client asked")
	if err != nil || m.Status != "Pending" || m.ID == "" {
		t.Fatalf("%+v %v", m, err)
	}
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "BJ01"), "210000", "200000")
	if _, err := o.DecideMCXMove(ctx, T, "anita.rao", m.ID, true, ""); err == nil {
		t.Fatal("the maker approved the move")
	}
	if _, err := o.DecideMCXMove(ctx, T, "vikram.shah", m.ID, true, ""); err == nil {
		t.Fatal("compliance approved the move")
	}
	if _, err := o.RequestMCXMove(ctx, T, "anita.rao", "BJ01", "EQ", sgD0, inr("300000"), ""); err == nil {
		t.Fatal("moved more than the MCX money")
	}
	if _, err := o.RequestMCXMove(ctx, T, "anita.rao", "BJ01", "MCX", sgD0, inr("20000"), ""); err == nil {
		t.Fatal("moved more than the equity money to MCX")
	}
	if m, err = o.DecideMCXMove(ctx, T, "farhan.khan", m.ID, true, ""); err != nil || m.Status != "Approved" || m.Checker != "farhan.khan" {
		t.Fatalf("%+v %v", m, err)
	}
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "BJ01"), "210000", "160000")
	p1, err := o.RequestPayoutFrom(ctx, T, "priya.nair", sgD0, "BJ01", inr("50000"), "Head office", "")
	if err != nil || p1.Earmark != "" || p1.DebitAccount != "" {
		t.Fatalf("%+v %v", p1, err)
	}
	// MCX money from the MCX-only account: 1,70,000 is the 1,60,000
	// earmark and then 10,000 of equity money... less the 50,000 already
	// approved, only within what may be withdrawn.
	p2, err := o.RequestPayoutFrom(ctx, T, "priya.nair", sgD0, "BJ01", inr("100000"), "Head office", "COM")
	if err != nil || p2.Earmark != EarmarkMCX || p2.Ledger != "COM" || p2.DebitAccount != sgCom {
		t.Fatalf("%+v %v", p2, err)
	}
	for _, id := range []string{p1.ID, p2.ID} {
		x, _, _ := docs.Get[PayoutRequest](ctx, o.Docs, T, cPayout, id)
		if x.Status == "Approved" {
			continue
		}
		if _, err := o.DecidePayout(ctx, T, "farhan.khan", sgD0, id, true, ""); err != nil {
			t.Fatal(id, err)
		}
	}
	b, err := o.CreatePayoutBatch(ctx, T, "farhan.khan", sgD0)
	if err != nil || len(b.Items) != 2 {
		t.Fatalf("%+v %v", b, err)
	}
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "BJ01"), "60000", "60000")
	f, err := o.PayoutFile(ctx, T, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	set, _ := o.Settings(ctx, T)
	for _, l := range strings.Split(strings.TrimSpace(string(f)), "\n")[1:] {
		mcx := strings.Contains(l, p2.ID)
		if mcx != strings.Contains(l, ","+sgCom+",") || !mcx && !strings.Contains(l, ","+set.PayoutDebitAccount+",") {
			t.Fatalf("payout paid from the wrong account: %s", l)
		}
	}
	// One ledger: two payouts on the client's statement, nothing else.
	st, _ := o.clientStatement(ctx, T, "BJ01", "", sgD0)
	if st.Closing != -inr("60000") || len(st.Lines) != 4 {
		t.Fatalf("%+v", st)
	}
	// A payout the bank returns gives the MCX money back.
	if _, err := o.ConfirmPayouts(ctx, T, "farhan.khan", b.ID, map[string]string{p1.ID: "UTR-P1"}, map[string]string{p2.ID: "account closed"}); err != nil {
		t.Fatal(err)
	}
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "BJ01"), "160000", "160000")
	// Rejecting needs a reason; the moves are listed for the client.
	m2, _ := o.RequestMCXMove(ctx, T, "anita.rao", "BJ01", "EQ", sgD0, inr("1000"), "")
	if _, err := o.DecideMCXMove(ctx, T, "farhan.khan", m2.ID, false, ""); err == nil {
		t.Fatal("rejected without a reason")
	}
	if ms, _ := o.MCXMoves(ctx, T, "BJ01"); len(ms) != 2 || ms[0].ID != m2.ID {
		t.Fatalf("%+v", ms)
	}
	d, err := o.ClientDetail(ctx, T, "BJ01", sgD0)
	if err != nil || d.Earmark == nil || d.Earmark.MCX != inr("160000") || len(d.Earmark.Moves) != 2 || d.Earmark.Accounts[0] != sgCom {
		t.Fatalf("drawer %+v %v", d.Earmark, err)
	}
}

// Receipts entered by hand and gateway payments for the MCX-only account
// are MCX money; an uncleared MCX cheque is kept out of the MCX side.
func TestEarmarkReceipts(t *testing.T) {
	o, ctx := emSetup(t, true)
	r, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{UCC: "BJ02", Mode: "Cheque", Ref: "000111", Date: sgD0, Amount: inr("7000"), ToAccount: sgCom, Earmark: ""})
	if err != nil || r.Earmark != EarmarkMCX {
		t.Fatalf("%+v %v", r, err)
	}
	r2, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{UCC: "BJ02", Mode: "NEFT", Ref: "U9", Date: sgD0, Amount: inr("3000"), ToAccount: sgAcct, Earmark: EarmarkMCX})
	if err != nil || r2.Earmark != "" {
		t.Fatalf("the earmark comes from the account only: %+v %v", r2, err)
	}
	for _, id := range []string{r.ID, r2.ID} {
		if _, err := o.DecideReceipt(ctx, T, "farhan.khan", id, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "BJ02"), "60000", "7000")
	rv, _ := o.Risk(ctx, T, sgD0)
	for _, x := range rv.Rows {
		if x.UCC == "BJ02" && (x.Funds.Cmp(mustDec("53000")) != 0 || x.Ledgers == nil || x.Ledgers.Commodity.Sign() != 0 || !x.Ledgers.Earmark) {
			t.Fatalf("risk with an uncleared MCX cheque %+v %+v", x, x.Ledgers)
		}
	}
	// The cheque bounces: the earmark goes with it.
	if _, err := o.ClearCheque(ctx, T, "priya.nair", r.ID, sgD0, true, false, "funds insufficient"); err != nil {
		t.Fatal(err)
	}
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "BJ02"), "53000", "0")
	// A gateway payment whose notes name the MCX-only account.
	set, _ := o.Settings(ctx, T)
	set.GatewaySecret = "s3cret"
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"event":"payment.captured","payload":{"payment":{"entity":{"id":"pay_1","amount":250000,"method":"upi","vpa":"v@upi","notes":{"ucc":"BJ02","to_account":"` + sgCom + `","value_date":"` + sgD0 + `"},"status":"captured"}}}}`)
	g, created, err := o.GatewayWebhook(ctx, T, body, SignGateway("s3cret", body))
	if err != nil || !created || g.Earmark != EarmarkMCX {
		t.Fatalf("%+v %v %v", g, created, err)
	}
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "BJ02"), "55500", "2500")
}

// NSE Clearing allocation and margin reporting, SEGMON: equity money;
// MCX allocation and MCXCCL margin reporting: MCX money.
func TestEarmarkAllocationAndReporting(t *testing.T) {
	o, ctx := emSetup(t, true)
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
	if bs, _ := segmonBalancesOf(t, o, ctx); bs != "60000.00" {
		t.Fatalf("SEGMON reads %s", bs)
	}
	mrg := strings.Join([]string{"29OCT2026", "56845", "BJ01", "1000.00", "200.00", "50.00", "", "", "", "", "", "100.00", "1000.00", "", "", "0.00", "0.00", "", ""}, ",") + "\n"
	loadX(t, o, "MCX_MARGIN_56845_20261029.csv", []byte(mrg))
	name, b, err := o.ExchangeFile(ctx, T, "priya.nair", "mcx-margin-reporting", sgD0, XFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := XFiles()
	out, _ := lib.Get("mcx-margin-reporting")
	tb, err := out.Read(name, b)
	if err != nil || len(tb.Rows) != 1 || tb.Rows[0].Values["PeakMarginCollected"] != "200000.00" {
		t.Fatalf("%v %+v", err, tb.Rows)
	}
	// Option off: everything is the one ledger, as before.
	o2, ctx2 := emSetup(t, false)
	al2, _ := o2.Allocation(ctx2, T, sgD0)
	got = map[string]string{}
	for _, a := range al2 {
		got[a.UCC+"|"+a.Segment] = a.Cash.Fixed(2)
	}
	if got["BJ01|CM"] != "210000.00" || got["BJ01|COM"] != "" {
		t.Fatalf("allocation without the option %v", got)
	}
	if bs, _ := segmonBalancesOf(t, o2, ctx2); bs != "260000.00" {
		t.Fatalf("SEGMON without the option %s", bs)
	}
}

// The sample day with the option on: per client the one ledger, the
// client's statement and the total margin are what they are without it;
// MCX margin moves to the MCX side.
func TestEarmarkSampleDay(t *testing.T) {
	bill := func(on bool) (*Ops, context.Context) {
		o, ctx := setup(t)
		if on {
			set, _ := o.Settings(ctx, T)
			set.MCXOnlyBankAccounts = []string{sgCom}
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
	rOff, _ := off.Risk(ctx, T, D)
	rOn, _ := on.Risk(ctx2, T, D)
	byU := map[string]RiskRow{}
	for _, r := range rOn.Rows {
		byU[r.UCC] = r
	}
	em, _ := on.mcxEarmarks(ctx2, T, D, "")
	profit, comMargin := 0, 0
	for _, r := range rOn.Rows {
		if em[r.UCC].MCX > 0 {
			profit++
		}
		if r.Ledgers != nil && r.Ledgers.CommodityRequired.Sign() > 0 {
			comMargin++
		}
	}
	if profit == 0 || comMargin == 0 {
		t.Fatalf("the sample day has no MCX profit (%d) or MCX margin (%d)", profit, comMargin)
	}
	for _, r := range rOff.Rows {
		n := byU[r.UCC]
		if n.Ledgers == nil || n.Ledgers.Combined.Cmp(r.Funds) != 0 || n.allRequired().Cmp(r.Required) != 0 || n.Ledgers.Commodity.Cmp(em[r.UCC].MCX.Dec()) != 0 {
			t.Fatalf("%s: %+v %+v vs one ledger funds %s required %s", r.UCC, n, n.Ledgers, r.Funds, r.Required)
		}
		so, _ := off.clientStatement(ctx, T, r.UCC, "", D)
		sn, _ := on.clientStatement(ctx2, T, r.UCC, "", D)
		if len(so.Lines) != len(sn.Lines) || so.Closing != sn.Closing {
			t.Fatalf("%s: statement changed", r.UCC)
		}
		for i := range so.Lines {
			if so.Lines[i] != sn.Lines[i] {
				t.Fatalf("%s line %d: %+v vs %+v", r.UCC, i, sn.Lines[i], so.Lines[i])
			}
		}
	}
	if _, err := on.LiveRisk(ctx2, T, D, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := on.Margins(ctx2, T, D); err != nil {
		t.Fatal(err)
	}
}

// Settings: MCX-only accounts are for one ledger, and the last cannot be
// removed while MCX money is earmarked.
func TestEarmarkSettings(t *testing.T) {
	o, ctx := emSetup(t, true)
	set, _ := o.Settings(ctx, T)
	set.SeparateCommodityLedger = true
	if err := o.SaveSettings(ctx, T, "admin", set); err == nil || !strings.Contains(err.Error(), "one ledger") {
		t.Fatalf("with the separate commodity ledger: %v", err)
	}
	set, _ = o.Settings(ctx, T)
	set.MCXOnlyBankAccounts = []string{" " + sgCom + " ", "0" + sgCom, sgAcct}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if set, _ = o.Settings(ctx, T); len(set.MCXOnlyBankAccounts) != 2 || !set.mcxOnly("00"+sgAcct) || set.mcxOnly("123") {
		t.Fatalf("%v", set.MCXOnlyBankAccounts)
	}
	set.MCXOnlyBankAccounts = nil
	if err := o.SaveSettings(ctx, T, "admin", set); err == nil || !strings.Contains(err.Error(), "BJ01") {
		t.Fatalf("removed with MCX money earmarked: %v", err)
	}
}
