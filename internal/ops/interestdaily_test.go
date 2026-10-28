package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// Baljit's way: 24% a year, no grace days, from the pay-in date, posted
// every day so that the next day's interest is on principal plus interest.
func TestDailyInterestCompounds(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	before, _ := o.clientMoves(ctx, T, D, nil)
	var u string
	for _, c := range cs {
		if len(before[c.UCC]) == 0 {
			u = c.UCC
			break
		}
	}
	if u == "" {
		t.Skip("no untouched client in the sample")
	}
	set, _ := o.Settings(ctx, T)
	set.InterestPctPA, set.InterestGraceDays, set.InterestMinimum = "24", 0, "0"
	set.InterestPosting, set.InterestDailyFrom = InterestDaily, "2026-10-01"
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	bad := set
	bad.InterestDailyFrom = ""
	if err := o.SaveSettings(ctx, T, "admin", bad); err == nil {
		t.Fatal("daily posting without a start date accepted")
	}
	// A bill of ₹1,00,000 dated 30 Sep: due (overdue) from 1 Oct, its pay-in day.
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "B:" + u, ValueDate: "2026-09-30", Narration: "Bill", Source: ledger.SourceRef{Kind: "BILL", ID: "x"}, CreatedBy: "admin",
		Postings: []ledger.Posting{{Account: ledger.ClientAccount(u), Debit: money.MustAmount("100000")}, {Account: ledger.OpeningEquity, Credit: money.MustAmount("100000")}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.PostDailyInterest(ctx, T, "priya.nair", "2026-10-03"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker posted interest: %v", err)
	}
	run, err := o.PostDailyInterest(ctx, T, "farhan.khan", "2026-10-03")
	if err != nil {
		t.Fatal(err)
	}
	if run.Entries < 3 {
		t.Fatalf("run %+v", run) // other sample clients may be in debit too
	}
	days := func() int {
		t.Helper()
		es, _ := o.App.Ledger.Store.List(ctx, T, ledger.Filter{})
		n := 0
		for _, e := range es {
			if e.Source.Kind == "INTEREST" && strings.HasSuffix(e.Source.ID, "/"+u) {
				n++
			}
		}
		return n
	}
	// Day 1: 1,00,000 × 24% ÷ 365 = 65.75, GST 11.84.
	// Day 2: on 1,00,077.59 = 65.80, GST 11.84.
	// Day 3: on 1,00,155.23 = 65.86, GST 11.85.
	want := []string{"65.75", "65.80", "65.86"}
	es, _ := o.App.Ledger.Store.List(ctx, T, ledger.Filter{From: "2026-10-01", To: "2026-10-03"})
	got := map[string]string{}
	for _, e := range es {
		if e.Source.Kind == "INTEREST" && strings.HasSuffix(e.Source.ID, "/"+u) {
			for _, p := range e.Postings {
				if p.Account == IncomeInterest {
					got[e.ValueDate] = p.Credit.String()
				}
			}
		}
	}
	for i, d := range []string{"2026-10-01", "2026-10-02", "2026-10-03"} {
		if got[d] != want[i] {
			t.Fatalf("interest on %s = %s, want %s (all %v)", d, got[d], want[i], got)
		}
	}
	// Again: nothing new. Next day: one more, on the grown balance.
	if run, _ := o.PostDailyInterest(ctx, T, "farhan.khan", "2026-10-03"); run.Entries != 0 {
		t.Fatalf("posted twice: %+v", run)
	}
	o.PostDailyInterest(ctx, T, "farhan.khan", "2026-10-04")
	if days() != 4 {
		t.Fatalf("next day: %d entries", days())
	}
	// Paid on 5 Oct (the receipt clears the whole balance): no interest from then.
	bal, _ := o.App.Ledger.Balances(ctx, T, "2026-10-05")
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "R:" + u, ValueDate: "2026-10-05", Narration: "Receipt", Source: ledger.SourceRef{Kind: "RECEIPT", ID: "r"}, CreatedBy: "admin",
		Postings: []ledger.Posting{{Account: ledger.BankClient, Debit: bal[ledger.ClientAccount(u)]}, {Account: ledger.ClientAccount(u), Credit: bal[ledger.ClientAccount(u)]}}}); err != nil {
		t.Fatal(err)
	}
	o.PostDailyInterest(ctx, T, "farhan.khan", "2026-10-10")
	if days() != 4 {
		t.Fatalf("charged after payment: %d entries", days())
	}
	// The month's view shows what was posted; the monthly posting refuses;
	// month end issues one invoice for the client.
	iv, err := o.Interest(ctx, T, "2026-10-01", "2026-10-31")
	var row InterestRow
	for _, r := range iv.Rows {
		if r.UCC == u {
			row = r
		}
	}
	if err != nil || iv.Posting != InterestDaily || row.DebitDays != 4 || row.Status != "Posted daily" {
		t.Fatalf("view %+v %v", row, err)
	}
	if _, _, err := o.PostInterest(ctx, T, "farhan.khan", "2026-10-01", "2026-10-31"); err == nil {
		t.Fatal("monthly posting ran with daily posting set")
	}
	if n, err := o.IssueDailyInterestInvoices(ctx, T, "farhan.khan", "2026-10"); err != nil || n != len(iv.Rows) {
		t.Fatalf("invoices %d %v", n, err)
	}
	if n, _ := o.IssueDailyInterestInvoices(ctx, T, "farhan.khan", "2026-10"); n != 0 {
		t.Fatal("invoiced twice")
	}
	invs, _ := o.Invoices(ctx, T, u)
	if len(invs) != 1 || invs[0].Taxable != row.Interest || !strings.Contains(invs[0].Description, "4 days") {
		t.Fatalf("invoice %+v", invs)
	}
}
