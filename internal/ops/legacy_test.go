package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

func TestLegacyDebts(t *testing.T) {
	o, ctx := notesReady(t)
	cs, _ := o.Clients(ctx, T)
	before, _ := o.clientMoves(ctx, T, D, nil)
	var free []string // clients the sample day leaves untouched
	for _, c := range cs {
		if len(before[c.UCC]) == 0 {
			if c.Status == "Active" {
				free = append([]string{c.UCC}, free...) // an active one first, to see the block
			} else {
				free = append(free, c.UCC)
			}
		}
	}
	if len(free) < 2 {
		t.Skip("the sample needs two untouched clients")
	}
	u, w := free[0], free[1]
	// An old debit that has not moved since January, as a migrated firm
	// brings in: client debit against opening equity.
	old := func(ucc, amt, date string) {
		t.Helper()
		if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "OLD:" + ucc + ":" + date, ValueDate: date, Narration: "Old debit", Source: ledger.SourceRef{Kind: "OPENING", ID: date}, CreatedBy: "admin",
			Postings: []ledger.Posting{{Account: ledger.ClientAccount(ucc), Debit: money.MustAmount(amt)}, {Account: ledger.OpeningEquity, Credit: money.MustAmount(amt)}}}); err != nil {
			t.Fatal(err)
		}
	}
	old(u, "4250.00", "2026-01-10")

	cand, err := o.FrozenDebits(ctx, T, D, 180)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cand {
		if c.UCC == u {
			found = c.Debit == money.MustAmount("4250") && c.Since == "2026-01-10" && c.Idle >= 180
		}
	}
	if !found {
		t.Fatalf("frozen debit of %s not found: %+v", u, cand)
	}
	if _, err := o.FrozenDebits(ctx, T, D, 10); err == nil {
		t.Fatal("10 idle days accepted")
	}

	// One person proposes, another approves.
	if _, err := o.ProposeLegacy(ctx, T, "priya.nair", D, []string{u}, ""); err == nil {
		t.Fatal("proposed without a reason")
	}
	if _, err := o.ProposeLegacy(ctx, T, "priya.nair", D, []string{w}, "old"); err == nil {
		t.Fatal("a client not in debit was proposed")
	}
	if n, err := o.ProposeLegacy(ctx, T, "priya.nair", D, []string{u}, "No movement since January"); err != nil || n != 1 {
		t.Fatalf("propose %d %v", n, err)
	}
	// Not in effect until approved: interest still charged.
	if r := interestRow(t, o, u); r.Status == "Legacy debt" {
		t.Fatal("proposal took effect before approval")
	}
	if _, err := o.DecideLegacy(ctx, T, "priya.nair", D, []string{u}, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker approved: %v", err)
	}
	if _, err := o.DecideLegacy(ctx, T, "farhan.khan", D, []string{u}, true, ""); err != nil {
		t.Fatal(err)
	}

	// In effect: no interest, out of the daily debit totals, still blocked.
	if r := interestRow(t, o, u); r.Status != "Legacy debt" {
		t.Fatalf("interest on a legacy debt: %+v", r)
	}
	dv, _ := o.Debits(ctx, T, D)
	for _, r := range dv.Rows {
		if r.UCC == u {
			// Still subject to the continuous-debit block (Block when the
			// account is active); only the totals and reminders leave it out.
			if !r.Legacy || (mustClient(t, o, u).Status == "Active" && r.Action != "Block") {
				t.Fatalf("debit row %+v", r)
			}
		}
	}
	if dv.LegacyCount != 1 || dv.LegacyDebit != money.MustAmount("4250") {
		t.Fatalf("legacy totals %d %s", dv.LegacyCount, dv.LegacyDebit)
	}
	if err := o.legacyBlocksReactivation(ctx, T, u, D); err == nil || !strings.Contains(err.Error(), "4,250") {
		t.Fatalf("reactivation not held: %v", err)
	}

	// Recovery: a balance confirmation, a part payment, a write-off of the rest.
	if _, err := o.NoteLegacy(ctx, T, "priya.nair", "2026-10-01", u, "Balance confirmed", "Client confirmed by email"); err != nil {
		t.Fatal(err)
	}
	pay := func(amt, date string) {
		t.Helper()
		if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "PAY:" + u + ":" + date, ValueDate: date, Narration: "Receipt", Source: ledger.SourceRef{Kind: "RECEIPT", ID: date}, CreatedBy: "admin",
			Postings: []ledger.Posting{{Account: ledger.BankClient, Debit: money.MustAmount(amt)}, {Account: ledger.ClientAccount(u), Credit: money.MustAmount(amt)}}}); err != nil {
			t.Fatal(err)
		}
	}
	pay("250.00", "2026-10-05")
	v, err := o.LegacyDebts(ctx, T, D)
	if err != nil || v.Open != 1 || v.Owed != money.MustAmount("4000") || v.Recovered != money.MustAmount("250") {
		t.Fatalf("after part payment %+v %v", v, err)
	}
	if v.Rows[0].LastAck != "2026-10-05" || v.Rows[0].ThreeYears != "2029-10-05" {
		t.Fatalf("acknowledgement %s %s", v.Rows[0].LastAck, v.Rows[0].ThreeYears)
	}
	if _, err := o.WriteOffLegacy(ctx, T, "priya.nair", D, u, money.MustAmount("100"), "small"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker wrote off: %v", err)
	}
	if _, err := o.WriteOffLegacy(ctx, T, "farhan.khan", D, u, money.MustAmount("5000"), "too much"); err == nil {
		t.Fatal("wrote off more than owed")
	}
	if _, err := o.WriteOffLegacy(ctx, T, "farhan.khan", D, u, money.MustAmount("4000"), "Not recoverable, approved by the directors"); err != nil {
		t.Fatal(err)
	}
	bals, _ := o.App.Ledger.Balances(ctx, T, D)
	if bals[ExpenseBadDebts] != money.MustAmount("4000") {
		t.Fatalf("bad debts %s", bals[ExpenseBadDebts])
	}
	v, _ = o.LegacyDebts(ctx, T, D)
	if v.Open != 0 || v.WrittenOff != money.MustAmount("4000") || v.Rows[0].State != "Cleared" {
		t.Fatalf("after write-off %+v", v)
	}
	if err := o.legacyBlocksReactivation(ctx, T, u, D); err != nil {
		t.Fatalf("cleared debt still holds reactivation: %v", err)
	}
}

func interestRow(t *testing.T, o *Ops, ucc string) InterestRow {
	t.Helper()
	iv, err := o.Interest(t.Context(), T, "2026-10-01", D)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range iv.Rows {
		if r.UCC == ucc {
			return r
		}
	}
	t.Fatalf("no interest row for %s", ucc)
	return InterestRow{}
}

func TestOpeningBalancesMarkLegacy(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	var u string
	ms, _ := o.clientMoves(ctx, T, D, nil)
	for _, c := range cs {
		if len(ms[c.UCC]) == 0 {
			u = c.UCC
			break
		}
	}
	if u == "" {
		t.Skip("no untouched client in the sample")
	}
	csv := "Account,Debit,Credit,Since,Legacy\nCLIENT:" + u + ",900.00,,2026-02-01,Y\nEQUITY:OPENING,,900.00,,\n"
	if _, err := o.LoadFile(ctx, T, "admin", "OPENING-BALANCES", D, "opening.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	v, _ := o.LegacyDebts(ctx, T, D)
	if v.Proposed != 1 || v.Rows[0].UCC != u || v.Rows[0].DebitSince != "2026-02-01" || v.Rows[0].ProposedBy != "admin" {
		t.Fatalf("%+v", v)
	}
	bad := "Account,Debit,Credit,Legacy\nEQUITY:OPENING,500.00,,Y\nEQUITY:OPENING,,500.00,\n"
	if _, err := o.LoadFile(ctx, T, "admin", "OPENING-BALANCES", D, "bad.csv", []byte(bad)); err == nil {
		t.Fatal("legacy flag on a non-client row accepted")
	}
}
