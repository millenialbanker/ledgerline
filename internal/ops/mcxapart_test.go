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

// MCX money kept apart in one ledger with one bank account for all
// exchanges (Settings.MCXMoneyApart, mcxearmark.go): no MCX-only account.

// apartOn turns the setting on for emSetup's firm (no MCX-only account),
// with the usual payout account set.
func apartOn(t *testing.T, o *Ops, ctx context.Context) {
	t.Helper()
	set, _ := o.Settings(ctx, T)
	set.MCXMoneyApart, set.PayoutDebitAccount = true, sgAcct
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if set, _ = o.Settings(ctx, T); !set.mcxEarmark() || !set.KeepsMCXMoneyApart() || len(set.MCXOnlyBankAccounts) != 0 {
		t.Fatalf("settings %+v", set)
	}
}

// moveToMCX brings a client's MCX money in, as RMS does at go-live: one
// person asks, another approves.
func moveToMCX(t *testing.T, o *Ops, ctx context.Context, ucc, amt string) {
	t.Helper()
	m, err := o.RequestMCXMove(ctx, T, "anita.rao", ucc, "MCX", sgD0, inr(amt), "MCX balance at go-live")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideMCXMove(ctx, T, "farhan.khan", m.ID, true, ""); err != nil {
		t.Fatal(err)
	}
}

func TestMCXApartOneBankAccount(t *testing.T) {
	o, ctx := emSetup(t, false) // AB01: ₹2,10,000, all paid into the one account
	apartOn(t, o, ctx)
	// Money in is equity money: there is no MCX-only account to mark it.
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "AB01"), "210000", "0")
	r, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{UCC: "AB02", Mode: "NEFT", Ref: "U77", Date: sgD0, Amount: inr("1000"), ToAccount: sgCom})
	if err != nil || r.Earmark != "" {
		t.Fatalf("receipt %+v %v", r, err)
	}

	// Go-live: RMS moves the client's MCX money, maker and a different checker.
	moveToMCX(t, o, ctx, "AB01", "150000")
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "AB01"), "210000", "150000")
	d, err := o.ClientDetail(ctx, T, "AB01", sgD0)
	if err != nil || d.Earmark == nil || d.Earmark.MCX != inr("150000") || len(d.Earmark.Accounts) != 0 {
		t.Fatalf("drawer %+v %v", d.Earmark, err)
	}
	// Equity limits see equity money only; BOW's commodity funds the MCX money.
	set, _ := o.Settings(ctx, T)
	next := set.nextBusinessDay(sgD0)
	if got := finspotCol5(t, o, ctx, next, "AB01"); got != "60000.00" {
		t.Fatalf("Finspot column 5 %s, want 60000.00", got)
	}
	if _, err := o.BOWFundsFile(ctx, T, next); err != nil {
		t.Fatal(err)
	}
	if bow, _ := o.bowFunds(ctx, T, next); bow["AB01"].Cmp(mustDec("150000")) != 0 {
		t.Fatalf("BOW commodity funds %s", bow["AB01"])
	}

	w, err := o.Withdrawable(ctx, T, sgD0, "AB01")
	if err != nil || w.Ledgers == nil || w.Ledgers.Equity != inr("60000") || w.Ledgers.Commodity != inr("150000") || w.Available != inr("210000") {
		t.Fatalf("%+v %+v %v", w, w.Ledgers, err)
	}
	// Each balance pays only what it has.
	if _, err := o.RequestPayoutFrom(ctx, T, "priya.nair", sgD0, "AB01", inr("70000"), "Head office", ""); err == nil || !strings.Contains(err.Error(), "MCX money") {
		t.Fatalf("equity payout using MCX money: %v", err)
	}
	if _, err := o.RequestPayoutFrom(ctx, T, "priya.nair", sgD0, "AB01", inr("160000"), "Head office", "COM"); err == nil || !strings.Contains(err.Error(), "1,50,000.00 of MCX money") || !strings.Contains(err.Error(), "10,000.00 to MCX") {
		t.Fatalf("MCX payout using equity money: %v", err)
	}
	// An MCX payout is not refused for want of an MCX-only account, and is
	// paid from the usual payout account.
	pm, err := o.RequestPayoutFrom(ctx, T, "priya.nair", sgD0, "AB01", inr("100000"), "Head office", "MCX")
	if err != nil || pm.Earmark != EarmarkMCX || pm.Ledger != ledger.LedgerCommodity || pm.DebitAccount != "" {
		t.Fatalf("MCX payout %+v %v", pm, err)
	}
	pe, err := o.RequestPayoutFrom(ctx, T, "priya.nair", sgD0, "AB01", inr("50000"), "Head office", "")
	if err != nil || pe.Earmark != "" {
		t.Fatalf("equity payout %+v %v", pe, err)
	}
	for _, id := range []string{pm.ID, pe.ID} {
		if x, _, _ := docs.Get[PayoutRequest](ctx, o.Docs, T, cPayout, id); x.Status == "Approved" {
			continue
		}
		if _, err := o.DecidePayout(ctx, T, "farhan.khan", sgD0, id, true, ""); err != nil {
			t.Fatal(id, err)
		}
	}
	// MCX money already promised: a further MCX payout is held to what is left.
	if w, _ = o.Withdrawable(ctx, T, sgD0, "AB01"); w.Ledgers.Commodity != inr("50000") || w.Ledgers.Equity != inr("10000") {
		t.Fatalf("after approval %+v", w.Ledgers)
	}
	// One batch, one file, one debit account for both.
	b, err := o.CreatePayoutBatch(ctx, T, "farhan.khan", sgD0)
	if err != nil || len(b.Items) != 2 {
		t.Fatalf("%+v %v", b, err)
	}
	f, err := o.PayoutFile(ctx, T, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(f)), "\n")[1:]
	if len(lines) != 2 {
		t.Fatalf("file %s", f)
	}
	for _, l := range lines {
		if !strings.Contains(l, ","+sgAcct+",") || strings.Contains(l, sgCom) {
			t.Fatalf("payout paid from the wrong account: %s", l)
		}
	}
	// The MCX payout debits the MCX money, the equity payout the equity money.
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "AB01"), "60000", "50000")
	// An MCX loss takes from MCX money.
	postMCXBill(t, o, ctx, sgD0, "AB01", "MCX", "COM", -inr("20000"))
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "AB01"), "40000", "30000")

	// Off is refused while clients have MCX money; it is for one ledger only.
	set, _ = o.Settings(ctx, T)
	set.MCXMoneyApart = false
	if err := o.SaveSettings(ctx, T, "admin", set); err == nil || !strings.Contains(err.Error(), "AB01") {
		t.Fatalf("turned off with MCX money kept apart: %v", err)
	}
	set, _ = o.Settings(ctx, T)
	set.SeparateCommodityLedger = true
	if err := o.SaveSettings(ctx, T, "admin", set); err == nil || !strings.Contains(err.Error(), "one ledger") {
		t.Fatalf("with the separate commodity ledger: %v", err)
	}
}

// Moves between equity and MCX money need a person to ask and a different
// person (Ops checker or Risk manager) to approve; nothing moves before.
func TestMCXApartMovesMakerChecker(t *testing.T) {
	o, ctx := emSetup(t, false)
	if _, err := o.RequestMCXMove(ctx, T, "anita.rao", "AB01", "MCX", sgD0, inr("1000"), ""); err == nil {
		t.Fatal("move with MCX money not kept apart")
	}
	apartOn(t, o, ctx)
	if _, err := o.RequestMCXMove(ctx, T, SystemActor, "AB01", "MCX", sgD0, inr("1000"), ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("automatic move: %v", err)
	}
	if _, err := o.RequestMCXMove(ctx, T, "vikram.shah", "AB01", "MCX", sgD0, inr("1000"), ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance asked for a move: %v", err)
	}
	m, err := o.RequestMCXMove(ctx, T, "anita.rao", "AB01", "MCX", sgD0, inr("100000"), "")
	if err != nil || m.Status != "Pending" {
		t.Fatalf("%+v %v", m, err)
	}
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "AB01"), "210000", "0")
	for _, who := range []string{"anita.rao", "vikram.shah", "priya.nair", SystemActor} {
		if _, err := o.DecideMCXMove(ctx, T, who, m.ID, true, ""); err == nil {
			t.Fatalf("%s approved the move", who)
		}
	}
	// The admin who asked cannot approve either.
	ma, err := o.RequestMCXMove(ctx, T, "admin", "AB01", "MCX", sgD0, inr("1000"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideMCXMove(ctx, T, "admin", ma.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin approved own move: %v", err)
	}
	if m, err = o.DecideMCXMove(ctx, T, "farhan.khan", m.ID, true, ""); err != nil || m.Checker != "farhan.khan" {
		t.Fatalf("%+v %v", m, err)
	}
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "AB01"), "210000", "100000")
	// And back to equity, the same way.
	b, _ := o.RequestMCXMove(ctx, T, "priya.nair", "AB01", "EQ", sgD0, inr("40000"), "")
	if _, err := o.DecideMCXMove(ctx, T, "priya.nair", b.ID, true, ""); err == nil {
		t.Fatal("maker approved own move")
	}
	if _, err := o.DecideMCXMove(ctx, T, "anita.rao", b.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "AB01"), "210000", "60000")
}

// MCX margin is met from MCX money only: pledged shares cover equity
// margin, so MCX money withdrawable is MCX money less MCX margin.
func TestMCXApartSampleDayMargin(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	set.MCXMoneyApart = true
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	rv, err := o.Risk(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rv.Rows {
		if r.Ledgers == nil || !r.Ledgers.Earmark {
			t.Fatalf("%s: risk row not split: %+v", r.UCC, r.Ledgers)
		}
		if r.Ledgers.CommodityRequired.Sign() <= 0 {
			continue
		}
		n++
		w, err := o.Withdrawable(ctx, T, D, r.UCC)
		if err != nil || w.Ledgers == nil {
			t.Fatal(err)
		}
		// Collateral is set against equity margin only: the margin kept is
		// the equity margin after collateral plus the whole MCX margin, and
		// MCX money pays out only above the MCX margin.
		eq := money.Max(r.Required.Sub(r.CollUsable), money.Dec{}).Round(money.UnitPaisa)
		if com := r.Ledgers.CommodityRequired.Round(money.UnitPaisa); w.Margin != eq+com {
			t.Fatalf("%s: margin kept %s, want equity %s after collateral %s plus MCX %s", r.UCC, w.Margin, eq, r.CollUsable, com)
		}
		if want := r.Ledgers.Commodity.Sub(r.Ledgers.CommodityRequired).Round(money.UnitPaisa); w.Ledgers.Commodity > want {
			t.Fatalf("%s: MCX money withdrawable %s above MCX money less MCX margin %s", r.UCC, w.Ledgers.Commodity, want)
		}
	}
	if n == 0 {
		t.Fatal("the sample day has no MCX margin")
	}
}

// The client portal: the client chooses equity money or MCX money, sees
// both amounts, and is offered MCX only when the firm keeps it apart and
// the client has some.
func TestMCXApartPortalWithdrawal(t *testing.T) {
	o, ctx := emSetup(t, false)
	// Payouts wait for a person here (no Autopilot approval).
	if err := docs.Upsert(ctx, o.Docs, T, cAutoPol, "payouts", AutoPolicy{ID: "payouts", Mode: ModeOff}, "test"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"AB01", "AB02"} {
		if err := o.PutUser(ctx, T, "admin", User{ID: "client." + strings.ToLower(u), Name: u, Role: RoleClient, Active: true, Clients: []string{u}}); err != nil {
			t.Fatal(err)
		}
	}
	// Not kept apart: no choice, and a client's choice is ignored.
	v, err := o.Portal(ctx, T, "client.ab01", sgD0, "AB01")
	if err != nil || v.Balances != nil || v.Withdrawable.Ledgers != nil {
		t.Fatalf("portal without the option %+v %v", v.Balances, err)
	}
	p0, err := o.RequestPayoutFrom(ctx, T, "client.ab01", sgD0, "AB01", inr("1000"), "Client", "MCX")
	if err != nil || p0.Earmark != "" || p0.Ledger != "" {
		t.Fatalf("client chose a balance without the option: %+v %v", p0, err)
	}
	apartOn(t, o, ctx)
	// Kept apart, but no MCX money yet: nothing to choose.
	if v, _ = o.Portal(ctx, T, "client.ab01", sgD0, "AB01"); v.Balances != nil {
		t.Fatalf("offered MCX with no MCX money: %+v", v.Balances)
	}
	moveToMCX(t, o, ctx, "AB01", "150000")
	v, err = o.Portal(ctx, T, "client.ab01", sgD0, "AB01")
	if err != nil || v.Balances == nil || v.Balances.MCX != inr("150000") || v.Balances.MCXBalance != inr("150000") || v.Balances.Equity != inr("60000") || v.Withdrawable.Ledgers != nil {
		t.Fatalf("portal balances %+v %v", v.Balances, err)
	}
	if v2, _ := o.Portal(ctx, T, "client.ab02", sgD0, "AB02"); v2.Balances != nil {
		t.Fatalf("AB02 has no MCX money: %+v", v2.Balances)
	}
	// The client withdraws from MCX money: within MCX money only.
	if _, err := o.RequestPayoutFrom(ctx, T, "client.ab01", sgD0, "AB01", inr("150001"), "Client", "MCX"); err == nil {
		t.Fatal("client withdrew more MCX money than there is")
	}
	pm, err := o.RequestPayoutFrom(ctx, T, "client.ab01", sgD0, "AB01", inr("20000"), "Client", "MCX")
	if err != nil || pm.Earmark != EarmarkMCX || pm.Source != "Client" || pm.DebitAccount != "" {
		t.Fatalf("client MCX payout %+v %v", pm, err)
	}
	// From equity money: within equity money only.
	if _, err := o.RequestPayoutFrom(ctx, T, "client.ab01", sgD0, "AB01", inr("60001"), "Client", ""); err == nil || !strings.Contains(err.Error(), "MCX money") {
		t.Fatalf("client equity payout using MCX money: %v", err)
	}
	if _, err := o.DecidePayout(ctx, T, "farhan.khan", sgD0, pm.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if v, _ = o.Portal(ctx, T, "client.ab01", sgD0, "AB01"); v.Balances == nil || v.Balances.MCX != inr("130000") {
		t.Fatalf("after approval %+v", v.Balances)
	}
	b, err := o.CreatePayoutBatch(ctx, T, "farhan.khan", sgD0)
	if err != nil || len(b.Items) != 1 {
		t.Fatalf("%+v %v", b, err)
	}
	wantEarmark(t, earmarkOf(t, o, ctx, sgD0, "AB01"), "190000", "130000")
	// A blocked client has nothing to choose.
	c, _ := o.Client(ctx, T, "AB01")
	c.Deceased = &DeceasedFlag{}
	if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "t"); err != nil {
		t.Fatal(err)
	}
	if v, _ = o.Portal(ctx, T, "client.ab01", sgD0, "AB01"); v.Balances != nil {
		t.Fatalf("blocked client offered a balance: %+v", v.Balances)
	}
}

// HDFC: MCX payouts from the one account go by ENet file or API with the
// equity payouts; only a payout from another firm account is held back.
func TestMCXApartHDFC(t *testing.T) {
	r := hdfcAPISetup(t)
	o, ctx := r.o, r.ctx
	set, _ := o.Settings(ctx, T)
	set.MCXMoneyApart, set.PayoutDebitAccount = true, sgAcct
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	cs := hdfcAPIMix[:2]
	hdfcClients(t, o, ctx, cs)
	hdfcApproved(t, o, ctx, "2026-11-19", cs)
	ps, _ := docs.List[PayoutRequest](ctx, o.Docs, T, cPayout, "")
	if _, err := docs.Update(ctx, o.Docs, T, cPayout, ps[0].ID, "t", func(x *PayoutRequest) error {
		x.Earmark, x.Ledger = EarmarkMCX, ledger.LedgerCommodity
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, err := o.CreatePayoutBatch(ctx, T, "farhan.khan", "2026-11-19")
	if err != nil || len(b.Items) != 2 {
		t.Fatalf("%+v %v", b, err)
	}
	v, err := o.HDFCPayView(ctx, T, b.ID)
	if err != nil || !v.CanSend || len(v.Blocked) > 0 {
		t.Fatalf("API view %+v %v", v, err)
	}
	setDebit := func(acct string) {
		t.Helper()
		bb, _, _ := docsGetBatch(o, ctx, b.ID)
		bb.Items[0].DebitAccount = acct
		if err := docs.Upsert(ctx, o.Docs, T, cPOBatch, bb.ID, bb, "t"); err != nil {
			t.Fatal(err)
		}
	}
	// The payout account named on the item is the same account: still one batch.
	setDebit("00" + sgAcct)
	if v, _ = o.HDFCPayView(ctx, T, b.ID); !v.CanSend || len(v.Blocked) > 0 {
		t.Fatalf("API view with the payout account named %+v", v)
	}
	// Another firm account (an MCX-only account) cannot go under the CBX code.
	setDebit(sgCom)
	if v, _ = o.HDFCPayView(ctx, T, b.ID); v.CanSend || !hasProblem(v.Blocked, "paid from "+sgCom) {
		t.Fatalf("API batch with another debit account %+v", v)
	}
	// The ENet file takes the MCX payout with the equity payout.
	setDebit("")
	if ps := hdfcProblems(t, o, ctx, b.ID); len(ps) != 0 {
		t.Fatalf("ENet problems %v", ps)
	}
}
