package ops

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"ledgerline/internal/money"
)

// All data here is made up: the sample firm and records added by the tests.

// moneyAccounts names the sample firm's bank accounts: a USCNBA, the DSCNBA
// it pays from, a settlement and an own account.
func moneyAccounts(t *testing.T, o *Ops) (usc, dsc, sett, own FirmBank) {
	t.Helper()
	ctx := t.Context()
	save := func(kind, acct, ifsc string) FirmBank {
		b, err := o.SaveFirmBank(ctx, T, siCO, FirmBankReq{FirmBank: FirmBank{Kind: kind, Bank: "Made-up Bank", Account: acct, IFSC: ifsc, Reported: true}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	usc = save(BankUSCNBA, "000111222333", "MADE0000002")
	dsc = rasDSCNBA(t, o)
	sett = save(BankSettlement, "000444555666", "MADE0000003")
	own = save(BankOwn, "000777888999", "MADE0000004")
	return
}

func moneyHas(r MoneyRecon, kind string) int {
	n := 0
	for _, f := range r.Flags {
		if f.Kind == kind {
			n++
		}
	}
	return n
}

func moneyGroup(r MoneyRecon, group string) (int, money.Amount) {
	for _, t := range r.Totals {
		if t.Group == group {
			return t.Count, t.Amount
		}
	}
	return 0, 0
}

// moneyXfer records a transfer as the maker and approves it as the checker.
func moneyXfer(t *testing.T, o *Ops, x FirmTransfer) FirmTransfer {
	t.Helper()
	ctx := t.Context()
	r, err := o.RecordFirmTransfer(ctx, T, "priya.nair", x)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideFirmTransfer(ctx, T, "priya.nair", r.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the maker approved %s: %v", r.ID, err)
	}
	a, err := o.DecideFirmTransfer(ctx, T, "farhan.khan", r.ID, true, "")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestFirmBankRegister(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	v, err := o.FirmBanks(ctx, T, "priya.nair")
	if err != nil || len(v.Accounts) != 0 || len(v.Suggest) != 1 || v.Suggest[0].Kind != BankDSCNBA || strings.Contains(v.Suggest[0].Account, set.PayoutDebitAccount) || v.CanEdit {
		t.Fatalf("empty register %+v %v", v, err)
	}
	if _, err := o.SaveFirmBank(ctx, T, "farhan.khan", FirmBankReq{FirmBank: FirmBank{Kind: BankOwn, Bank: "Made-up Bank", Account: "000777888999", IFSC: "MADE0000004"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a checker changed the register: %v", err)
	}
	for _, bad := range []FirmBank{
		{Kind: "Client", Bank: "Made-up Bank", Account: "000777888999", IFSC: "MADE0000004"},
		{Kind: BankOwn, Bank: "Made-up Bank", Account: "12", IFSC: "MADE0000004"},
		{Kind: BankOwn, Bank: "Made-up Bank", Account: "000777888999", IFSC: "MADE1000004"},
		{Kind: BankCC, Bank: "Made-up Bank", Account: "000777888999", IFSC: "MADE0000004"},
		{Kind: BankUSCNBA, Bank: "Made-up Bank", Account: "000777888999", IFSC: "MADE0000004", Serves: []string{"LSE"}},
	} {
		if _, err := o.SaveFirmBank(ctx, T, siCO, FirmBankReq{FirmBank: bad}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %+v: %v", bad, err)
		}
	}
	// The payout account Settings name, added from its suggestion.
	d, err := o.SaveFirmBank(ctx, T, siCO, FirmBankReq{FirmBank: FirmBank{Kind: BankDSCNBA, Bank: "Made-up Bank", IFSC: "MADE0000001", Serves: []string{"nse", "BSE-CM"}}, Suggestion: v.Suggest[0].Key})
	if err != nil || d.ID != "FB-01" || !d.Active || d.Account != MaskAccount(set.PayoutDebitAccount) || strings.Join(d.Serves, ",") != "NSE,BSE-CM" {
		t.Fatalf("from the suggestion %+v %v", d, err)
	}
	if _, err := o.SaveFirmBank(ctx, T, siCO, FirmBankReq{FirmBank: FirmBank{Kind: BankOwn, Bank: "Made-up Bank", Account: set.PayoutDebitAccount, IFSC: "MADE0000001"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an account twice: %v", err)
	}
	v, _ = o.FirmBanks(ctx, T, siCO)
	if len(v.Accounts) != 1 || len(v.Suggest) != 0 || !v.CanEdit || len(v.Warnings) < 2 {
		t.Fatalf("register %+v", v)
	}
	// Changing it keeps the full number when the masked one comes back.
	d.Reported, d.Note = true, "Reported to NSE and BSE"
	d2, err := o.SaveFirmBank(ctx, T, siCO, FirmBankReq{FirmBank: d})
	if err != nil || d2.ID != d.ID || !d2.Reported {
		t.Fatalf("change %+v %v", d2, err)
	}
	if a, _ := o.rasPaidFrom(ctx, T); a != set.PayoutDebitAccount {
		t.Fatalf("paid from %q", a)
	}
	if b, ok := defaultFirmBank(o.firmBanks(ctx, T), BankDSCNBA, "MCX"); !ok || b.ID != d.ID {
		t.Fatalf("default DSCNBA %+v", b)
	}
}

// The day's statement follows the money and flags what the rules forbid.
func TestMoneyReconStatement(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "20:00"))
	m, _, err := o.Market(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.App.RunBilling(ctx, T, "priya.nair", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	// With no register, nothing is checked against the accounts.
	r, err := o.MoneyReconFor(ctx, T, "priya.nair", D)
	if err != nil || r.Register != 0 || len(r.Notes) == 0 || r.Charges.Total <= 0 || !r.Settlement {
		t.Fatalf("no register %+v %v", r, err)
	}
	if n, _ := moneyGroup(r, mvReceipt); n == 0 {
		t.Fatalf("no receipts %+v", r.Totals)
	}
	if _, err := o.MoneyReconFor(ctx, T, "meera.iyer", D); !errors.Is(err, ErrForbidden) {
		t.Fatalf("branch staff saw it: %v", err)
	}
	usc, dsc, sett, own := moneyAccounts(t, o)
	if _, err := o.Upstream(ctx, T, "farhan.khan", D, 5_00_000_00, "UTRUPMADEUP1"); err != nil {
		t.Fatal(err)
	}
	b, err := o.RunRAS(ctx, T, "farhan.khan", D, D)
	if err != nil {
		t.Fatal(err)
	}
	r, _ = o.MoneyReconFor(ctx, T, "priya.nair", D)
	np, paid := moneyGroup(r, mvPayout)
	nu, up := moneyGroup(r, mvUpstream)
	if np != len(b.Items) || paid != b.Total || nu != 1 || up != 5_00_000_00 || moneyHas(r, "payment_outside") != 0 || moneyHas(r, "receipt_outside") != 0 {
		t.Fatalf("payouts %d %s, upstream %d %s, flags %+v", np, paid, nu, up, r.Flags)
	}
	// The DSCNBA paid out more than came in: record the funds that came back.
	if moneyHas(r, "dscnba_balance") != 1 {
		t.Fatalf("no DSCNBA flag %+v", r.Flags)
	}
	for _, mv := range r.Moves {
		if mv.Group == mvPayout && (mv.FromID != dsc.ID || mv.Assumed) {
			t.Fatalf("payout not from the DSCNBA %+v", mv)
		}
		if mv.Group == mvReceipt && mv.Assumed && mv.ToID != usc.ID {
			t.Fatalf("receipt not in the USCNBA %+v", mv)
		}
	}
	if _, err := o.RecordFirmTransfer(ctx, T, "priya.nair", FirmTransfer{Date: D, Purpose: "release", From: dsc.ID, To: "CC", Amount: 100, Ref: "X"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a release from the DSCNBA: %v", err)
	}
	if _, err := o.RecordFirmTransfer(ctx, T, "priya.nair", FirmTransfer{Date: D, Purpose: "payout", From: "CC", To: sett.ID, Amount: 100, Ref: "X"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a pay-out with no segment: %v", err)
	}
	rel := moneyXfer(t, o, FirmTransfer{Date: D, Purpose: "release", From: "CC", To: dsc.ID, Withdraw: "withdrawal", Amount: b.Total, Ref: "UTRRELMADEUP1"})
	if rel.Status != "Posted" || rel.Entry == "" {
		t.Fatalf("release %+v", rel)
	}
	r, _ = o.MoneyReconFor(ctx, T, "priya.nair", D)
	if moneyHas(r, "dscnba_balance") != 0 {
		t.Fatalf("DSCNBA still flagged %+v", r.Flags)
	}
	// Brokerage to the own account within the charges debited: fine.
	moneyXfer(t, o, FirmTransfer{Date: D, Purpose: "brokerage", From: usc.ID, To: own.ID, Amount: 100_00, Ref: "UTROWNMADEUP1"})
	r, _ = o.MoneyReconFor(ctx, T, "priya.nair", D)
	if n, a := moneyGroup(r, mvToOwn); n != 1 || a != 100_00 || moneyHas(r, "unbacked_own") != 0 || r.Pool.Available <= 0 {
		t.Fatalf("to own %d %s %+v", n, a, r.Pool)
	}
	// Beyond the charges debited to clients: flagged.
	moneyXfer(t, o, FirmTransfer{Date: D, Purpose: "statutory", From: usc.ID, To: own.ID, Amount: 2_00_00_000_00, Ref: "UTROWNMADEUP2"})
	r, _ = o.MoneyReconFor(ctx, T, "priya.nair", D)
	if moneyHas(r, "unbacked_own") != 1 || r.Pool.Available >= 0 {
		t.Fatalf("not flagged %+v %+v", r.Flags, r.Pool)
	}
	// From a USCNBA straight to a DSCNBA: recorded, flagged.
	x, err := o.RecordFirmTransfer(ctx, T, "priya.nair", FirmTransfer{Date: D, Purpose: "internal", From: usc.ID, To: dsc.ID, Amount: 1000_00, Ref: "UTRINTMADEUP1"})
	if err != nil || x.Warning == "" {
		t.Fatalf("internal %+v %v", x, err)
	}
	x, _ = o.DecideFirmTransfer(ctx, T, "farhan.khan", x.ID, true, "")
	if x.Status != "Recorded" || x.Entry != "" {
		t.Fatalf("internal decided %+v", x)
	}
	r, _ = o.MoneyReconFor(ctx, T, "priya.nair", D)
	if moneyHas(r, "uscnba_to_dscnba") != 1 || moneyHas(r, "dscnba_balance") != 0 {
		t.Fatalf("internal flags %+v (a settlement day keeps the DSCNBA's balance)", r.Flags)
	}
	// A client's money paid into the own account: flagged.
	cs, _ := o.Clients(ctx, T)
	var c Client
	for _, x := range cs {
		if x.Status == "Active" && payable(primaryBank(x)) {
			c = x
			break
		}
	}
	set, _ := o.Settings(ctx, T)
	stmt := fmt.Sprintf("Date,Time,Mode,Reference,Remitter,Account,Amount,DrCr,FirmAccount,Balance\n%s,15:00,NEFT,UTRWRONGMADEUP1,%s,%s,1000.00,CR,000777888999,123456.00\n%s,15:05,NEFT,UTRDSCMADEUP1,SOMEONE,000999888777,10.00,DR,%s,0.00\n", D, strings.ToUpper(c.Name), primaryBank(c).Account, D, set.PayoutDebitAccount)
	if _, err := o.LoadFile(ctx, T, "priya.nair", "BANK-STMT", "", "stmt_made_up.csv", []byte(stmt)); err != nil {
		t.Fatal(err)
	}
	r, _ = o.MoneyReconFor(ctx, T, "priya.nair", D)
	if moneyHas(r, "receipt_outside") != 1 {
		t.Fatalf("receipt into the own account not flagged %+v", r.Flags)
	}
	var ownAcct *MoneyAccount
	for i := range r.Accounts {
		if r.Accounts[i].ID == own.ID {
			ownAcct = &r.Accounts[i]
		}
	}
	if ownAcct == nil || ownAcct.Statement != "123456.00" || ownAcct.Lines != 1 || ownAcct.Difference == "" {
		t.Fatalf("own account %+v", ownAcct)
	}
	statementOnly := 0
	for _, u := range r.Unmatched {
		if u.Side == "In the bank statement only" {
			statementOnly++
		}
	}
	if statementOnly < 2 || len(r.Lines) < 3 {
		t.Fatalf("reconciling items %+v (%d lines)", r.Unmatched, len(r.Lines))
	}
	for _, l := range r.Lines {
		if strings.Contains(l.Account+l.PartyAccount, "000777888999") || strings.Contains(l.PartyAccount, primaryBank(c).Account) {
			t.Fatalf("account numbers not masked %+v", l)
		}
	}
	csv := string(MoneyReconCSV(r))
	if !strings.Contains(csv, "Daily reconciliation statement of client money transfers") || !strings.Contains(csv, "UTRRELMADEUP1") || !strings.Contains(csv, "not a USCNBA") {
		t.Fatalf("csv %s", csv)
	}
}

// A maker prepares, a checker signs off; Periodic work prepares it when no
// one has; the self-inspection checks each business day was signed off.
func TestMoneyReconSignOff(t *testing.T) {
	o, ctx := setup(t)
	move := siClock(o, siAt(D, "20:00"))
	_, dsc, _, _ := moneyAccounts(t, o)
	if _, err := o.SignMoneyRecon(ctx, T, "farhan.khan", D, ""); !errors.Is(err, ErrState) {
		t.Fatalf("signed with nothing prepared: %v", err)
	}
	r, err := o.PrepareMoneyRecon(ctx, T, "priya.nair", D, "Checked against the bank statements")
	if err != nil || r.Status != "Prepared" || r.FileID == "" || r.Prepared.By != "priya.nair" || r.CanSign {
		t.Fatalf("prepare %+v %v", r, err)
	}
	if w := rasOpenWork(t, o, wMoneyRecon+":"+moneyWorkKey(D, 0)); w == nil || w.Role != RoleChecker {
		t.Fatalf("no sign-off item %+v", w)
	}
	if _, err := o.SignMoneyRecon(ctx, T, "priya.nair", D, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker signed: %v", err)
	}
	// The records change: it is prepared again before it is signed.
	moneyXfer(t, o, FirmTransfer{Date: D, Purpose: "release", From: "CC", To: dsc.ID, Withdraw: "withdrawal", Amount: 1000_00, Ref: "UTRRELMADEUP2"})
	if v, _ := o.MoneyReconFor(ctx, T, "farhan.khan", D); !v.Changed || v.CanSign {
		t.Fatalf("change not seen %+v", v)
	}
	if _, err := o.SignMoneyRecon(ctx, T, "farhan.khan", D, ""); !errors.Is(err, ErrState) {
		t.Fatalf("signed a changed statement: %v", err)
	}
	r, err = o.PrepareMoneyRecon(ctx, T, "farhan.khan", D, "")
	if err != nil || len(r.History) != 1 || r.CanSign {
		t.Fatalf("prepared again %+v %v", r, err)
	}
	if _, err := o.SignMoneyRecon(ctx, T, "farhan.khan", D, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("signed what they prepared: %v", err)
	}
	r, err = o.ReturnMoneyRecon(ctx, T, "farhan.khan", D, "")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("sent back with no reason: %v", err)
	}
	if err := o.PutUser(ctx, T, "admin", User{ID: "ravi.k", Name: "Ravi K (made up)", Role: RoleChecker, Active: true}); err != nil {
		t.Fatal(err)
	}
	r, err = o.ReturnMoneyRecon(ctx, T, "ravi.k", D, "Explain the receipt parked in suspense")
	if err != nil || r.Status != "Sent back" || r.Returned.By != "ravi.k" {
		t.Fatalf("sent back %+v %v", r, err)
	}
	if w := rasOpenWork(t, o, wMoneyRecon+":"+moneyPrepKey(D, 2)); w == nil || w.Role != RoleMaker {
		t.Fatalf("no item for the maker %+v", w)
	}
	// Periodic work leaves a statement sent back to the maker.
	if run, err := o.RunPeriodic(ctx, T, SystemActor, "client_money_recon", D, D); err != nil || run.Status != "Sent back" || !strings.Contains(run.Summary, "ravi.k") {
		t.Fatalf("periodic after sending back %+v %v", run, err)
	}
	if v, _ := o.MoneyReconFor(ctx, T, "priya.nair", D); v.Status != "Sent back" {
		t.Fatalf("prepared again by Periodic work: %s", v.Status)
	}
	if _, err := o.PrepareMoneyRecon(ctx, T, "priya.nair", D, "Explained"); err != nil {
		t.Fatal(err)
	}
	if rasOpenWork(t, o, wMoneyRecon+":"+moneyPrepKey(D, 2)) != nil {
		t.Fatal("the maker's item is still open")
	}
	r, err = o.SignMoneyRecon(ctx, T, "farhan.khan", D, "Seen")
	if err != nil || r.Status != "Signed off" || r.Signed.By != "farhan.khan" || r.CanPrepare {
		t.Fatalf("signed %+v %v", r, err)
	}
	if _, err := o.PrepareMoneyRecon(ctx, T, "priya.nair", D, ""); !errors.Is(err, ErrState) {
		t.Fatalf("prepared a signed statement that did not change: %v", err)
	}
	name, csv, err := o.MoneyReconFile(ctx, T, "priya.nair", D)
	if err != nil || name != "ClientMoneyReconciliation_"+D+".csv" || !strings.Contains(string(csv), "Signed off by,farhan.khan") {
		t.Fatalf("file %s %v %s", name, err, csv)
	}
	page, err := o.RenderMoneyRecon(ctx, T, "priya.nair", D)
	if err != nil || !strings.Contains(string(page), "Daily reconciliation statement of client money transfers") || !strings.Contains(string(page), "farhan.khan") {
		t.Fatalf("page %v", err)
	}
	// The self-inspection, and Periodic work for the next business day.
	move(siAt("2026-10-20", "09:00"))
	siWant(t, siOne(t, o, ctx, "client_money_recon", "2026-10-19"), SIPass, "the 16th signed off")
	out := siOne(t, o, ctx, "client_money_recon", "2026-10-20")
	siWant(t, out, SIFail, "the 19th not prepared")
	if out.found != 1 || !strings.Contains(out.examples[0], "19-Oct-2026") {
		t.Fatalf("self-inspection %+v", out)
	}
	run, err := o.RunPeriodic(ctx, T, SystemActor, "client_money_recon", "", "2026-10-20")
	if err != nil || run.Period != "2026-10-19" || run.Status != "Waiting for sign-off" || len(run.Files) != 1 {
		t.Fatalf("periodic %+v %v", run, err)
	}
	if _, err := o.SignMoneyRecon(ctx, T, "farhan.khan", "2026-10-19", ""); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "client_money_recon", "2026-10-20"), SIPass, "both days signed off")
	ps, _ := o.Periodic(ctx, T, "2026-10-20")
	for _, p := range ps {
		if p.ID == "client_money_recon" && p.Status != "Done" {
			t.Fatalf("periodic %+v", p)
		}
	}
	// Money left in the DSCNBA on a day that is not a settlement day.
	moneyXfer(t, o, FirmTransfer{Date: "2026-10-20", Purpose: "release", From: "CC", To: dsc.ID, Withdraw: "regulatory", Amount: 500_00, Ref: "UTRRELMADEUP3"})
	v, _ := o.MoneyReconFor(ctx, T, "priya.nair", "2026-10-20")
	if v.Settlement || moneyHas(v, "dscnba_balance") != 1 {
		t.Fatalf("DSCNBA balance on the 20th %+v", v.Flags)
	}
}
