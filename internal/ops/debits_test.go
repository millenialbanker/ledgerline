package ops

import (
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// TestDebitsAndInterest works a hand-checked debit history:
//
//	01-Sep  client debited ₹1,000      balance −1,000 (10 days)
//	11-Sep  receipt ₹400               balance   −600 (10 days)
//	20-Sep  buy bill ₹600, due 21-Sep  balance −1,200 (10 days to 30-Sep)
//
// Interest at 18% p.a. = (10×1000 + 10×600 + 10×1200) × 18 / 36500
// = ₹13.81, GST 18% = ₹2.49.
func TestDebitsAndInterest(t *testing.T) {
	o, ctx := newOps(t)
	put := func(c Client) {
		if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "test"); err != nil {
			t.Fatal(err)
		}
	}
	put(Client{UCC: "DB001", Name: "Debit One", Status: "Active", Tags: []string{}})
	put(Client{UCC: "DB002", Name: "Staff Debit", Status: "Active", Tags: []string{"STAFF"}})
	post := func(key, date, kind, ucc string, amt int64) {
		t.Helper()
		ps := []ledger.Posting{{Account: ledger.ClientAccount(ucc), Debit: money.Amount(amt)}, {Account: ledger.BankClient, Credit: money.Amount(amt)}}
		if amt < 0 {
			ps = []ledger.Posting{{Account: ledger.ClientAccount(ucc), Credit: money.Amount(-amt)}, {Account: ledger.BankClient, Debit: money.Amount(-amt)}}
		}
		if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: key, ValueDate: date, Narration: key, Source: ledger.SourceRef{Kind: kind, ID: key}, CreatedBy: "test", Postings: ps}); err != nil {
			t.Fatal(err)
		}
	}
	for _, u := range []string{"DB001", "DB002"} {
		post("d1"+u, "2026-09-01", "MANUAL", u, 100000)
		post("r1"+u, "2026-09-11", "BANK", u, -40000)
		post("b1"+u, "2026-09-20", "BILL", u, 60000)
	}
	set, _ := o.Settings(ctx, T)
	set.InterestExemptTags = []string{"STAFF"}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	v, err := o.Interest(ctx, T, "2026-09-01", "2026-09-30")
	if err != nil {
		t.Fatal(err)
	}
	row := func(v InterestView, u string) InterestRow {
		for _, r := range v.Rows {
			if r.UCC == u {
				return r
			}
		}
		t.Fatalf("no interest row for %s", u)
		return InterestRow{}
	}
	r := row(v, "DB001")
	if r.DebitDays != 30 || r.Product.String() != "28000.00" || r.Interest.String() != "13.81" || r.GST.String() != "2.49" || r.Status != "To post" {
		t.Fatalf("interest row = %+v", r)
	}
	if s := row(v, "DB002").Status; s != "Exempt" {
		t.Fatalf("staff status = %s", s)
	}
	if _, _, err := o.PostInterest(ctx, T, "priya.nair", "2026-09-01", "2026-09-30"); err == nil {
		t.Fatal("a maker posted interest")
	}
	v, n, err := o.PostInterest(ctx, T, "farhan.khan", "2026-09-01", "2026-09-30")
	if err != nil || n != 1 {
		t.Fatalf("post: n=%d err=%v", n, err)
	}
	if r := row(v, "DB001"); r.Status != "Posted" || !strings.HasPrefix(r.Invoice, "INT/2026-27/") {
		t.Fatalf("after posting: %+v", r)
	}
	if _, n, _ := o.PostInterest(ctx, T, "farhan.khan", "2026-09-01", "2026-09-30"); n != 0 {
		t.Fatal("interest posted twice")
	}
	// An overlapping period is not charged again.
	v, _ = o.Interest(ctx, T, "2026-09-15", "2026-10-05")
	if r := row(v, "DB001"); !strings.HasPrefix(r.Status, "Posted for") {
		t.Fatalf("overlap status = %s", r.Status)
	}
	invs, _ := o.Invoices(ctx, T, "DB001")
	if len(invs) != 1 || invs[0].Total.String() != "16.30" {
		t.Fatalf("invoices = %+v", invs)
	}

	// Debtors on 5-Oct: 35 days in debit; ₹1,216.30 owed in three lots.
	d, err := o.Debits(ctx, T, "2026-10-05")
	if err != nil {
		t.Fatal(err)
	}
	var dr DebitRow
	for _, x := range d.Rows {
		if x.UCC == "DB001" {
			dr = x
		}
	}
	if dr.Debit.String() != "1216.30" || dr.Since != "2026-09-01" || dr.Days != 35 || dr.Action != "Block" {
		t.Fatalf("debit row = %+v", dr)
	}
	// 600 left of the 1-Sep debit (34 days), 600 from the bill due 21-Sep
	// (14 days), 16.30 interest of 30-Sep (5 days).
	if dr.Ageing[0].String() != "16.30" || dr.Ageing[1].String() != "600.00" || dr.Ageing[2].String() != "600.00" {
		t.Fatalf("ageing = %v", dr.Ageing)
	}
	if _, _, err := o.ApplyDebitBlocks(ctx, T, "priya.nair", "2026-10-05"); err == nil {
		t.Fatal("a maker blocked clients")
	}
	b, u, err := o.ApplyDebitBlocks(ctx, T, "anita.rao", "2026-10-05")
	if err != nil || b != 2 || u != 0 {
		t.Fatalf("blocks: %d %d %v", b, u, err)
	}
	f, _ := o.DebitBlockFile(ctx, T, "2026-10-05")
	if !strings.Contains(string(f), "DB001,BLOCK_BUY,ALL,35,1216.30") {
		t.Fatalf("block file:\n%s", f)
	}
	// Paying up unblocks.
	post("r2DB001", "2026-10-06", "BANK", "DB001", -121630)
	d, _ = o.Debits(ctx, T, "2026-10-06")
	if d.ToUnblock != 1 {
		t.Fatalf("to unblock = %d", d.ToUnblock)
	}
	if _, u, _ := o.ApplyDebitBlocks(ctx, T, "anita.rao", "2026-10-06"); u != 1 {
		t.Fatalf("unblocked %d", u)
	}
	c, _ := o.Client(ctx, T, "DB001")
	if c.Blocked {
		t.Fatal("still blocked after paying")
	}
	f, _ = o.DebitBlockFile(ctx, T, "2026-10-06")
	if strings.Contains(string(f), "DB001") {
		t.Fatalf("cleared client still in file:\n%s", f)
	}

	// A locked period refuses postings, including interest.
	set, _ = o.Settings(ctx, T)
	set.LockedUpTo = "2026-10-31"
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.PostInterest(ctx, T, "farhan.khan", "2026-10-01", "2026-10-31"); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("posted into a locked period: %v", err)
	}
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "late", ValueDate: "2026-10-15", Narration: "x", Source: ledger.SourceRef{Kind: "MANUAL", ID: "x"}, CreatedBy: "test",
		Postings: []ledger.Posting{{Account: ledger.BankClient, Debit: 100}, {Account: ledger.SuspenseBank, Credit: 100}}}); err == nil {
		t.Fatal("ledger accepted an entry in a locked period")
	}
}
