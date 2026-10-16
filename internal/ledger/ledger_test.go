package ledger

import (
	"context"
	"errors"
	"testing"

	"ledgerline/internal/money"
)

func receipt(key string, amt string) Entry {
	a := money.MustAmount(amt)
	return Entry{
		Tenant: "t1", Key: key, ValueDate: "2026-10-16", Narration: "Funds received · NEFT", CreatedBy: "ops",
		Source:   SourceRef{Kind: "BANK", ID: "UTR123"},
		Postings: []Posting{{Account: BankClient, Debit: a}, {Account: ClientAccount("BH1001"), Credit: a}},
	}
}

func TestValidate(t *testing.T) {
	ok := receipt("k", "100.00")
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
	bad := []func(e *Entry){
		func(e *Entry) { e.Postings[1].Credit = 99 },                              // unbalanced
		func(e *Entry) { e.Postings = e.Postings[:1] },                            // one leg
		func(e *Entry) { e.Postings[0].Credit = e.Postings[0].Debit },             // both sides
		func(e *Entry) { e.Postings[0].Account = "WALLET:x" },                     // unknown prefix
		func(e *Entry) { e.ValueDate = "16-10-2026" },                             // bad date
		func(e *Entry) { e.Key = "" },                                             // no key
		func(e *Entry) { e.Postings[0].Debit, e.Postings[1].Credit = -100, -100 }, // negative
	}
	for i, mut := range bad {
		e := receipt("k", "100.00")
		mut(&e)
		if err := e.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d: want ErrInvalid, got %v", i, err)
		}
	}
}

func TestIdempotency(t *testing.T) {
	ctx := context.Background()
	l := New(NewMemStore())
	e1, created, err := l.Post(ctx, receipt("RCPT:UTR123", "5000.00"))
	if err != nil || !created {
		t.Fatalf("first post: %v created=%v", err, created)
	}
	retry := receipt("RCPT:UTR123", "5000.00")
	retry.CreatedBy = "worker-2"
	e2, created, err := l.Post(ctx, retry)
	if err != nil || created || e2.ID != e1.ID {
		t.Fatalf("retry should return existing: %v created=%v id=%s", err, created, e2.ID)
	}
	if _, _, err := l.Post(ctx, receipt("RCPT:UTR123", "6000.00")); !errors.Is(err, ErrConflict) {
		t.Fatalf("different content under same key: want ErrConflict, got %v", err)
	}
}

func TestReverseAndProjections(t *testing.T) {
	ctx := context.Background()
	l := New(NewMemStore())
	if _, _, err := l.Post(ctx, receipt("a", "5000.00")); err != nil {
		t.Fatal(err)
	}
	e, _, err := l.Post(ctx, receipt("b", "1200.00"))
	if err != nil {
		t.Fatal(err)
	}
	rev, _, err := l.Reverse(ctx, "t1", e.ID, "checker", "credited to wrong client", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Reverse(ctx, "t1", rev.ID, "checker", "again", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reversing a reversal should fail, got %v", err)
	}
	if _, _, err := l.Reverse(ctx, "t1", e.ID, "checker", "different reason", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("second reversal should conflict, got %v", err)
	}
	bal, _ := l.Balances(ctx, "t1", "")
	if got := bal[ClientAccount("BH1001")]; got != -money.MustAmount("5000.00") {
		t.Fatalf("client balance = %s, want -5000.00 (credit)", got)
	}
	tb, _ := l.TrialBalance(ctx, "t1", "")
	if !tb.Balanced || tb.TotalDebit != money.MustAmount("5000.00") {
		t.Fatalf("trial balance %+v", tb)
	}
	st, err := l.Statement(ctx, "t1", ClientAccount("BH1001"), "2026-10-16", "2026-10-16")
	if err != nil || len(st.Lines) != 3 || st.Closing != -money.MustAmount("5000.00") {
		t.Fatalf("statement %+v err %v", st, err)
	}
	// Tenant isolation.
	other, _ := l.Balances(ctx, "t2", "")
	if len(other) != 0 {
		t.Fatalf("tenant t2 sees %v", other)
	}
}

func TestStatementOpening(t *testing.T) {
	ctx := context.Background()
	l := New(NewMemStore())
	early := receipt("old", "700.00")
	early.ValueDate = "2026-10-01"
	l.Post(ctx, early)
	l.Post(ctx, receipt("new", "300.00"))
	st, _ := l.Statement(ctx, "t1", ClientAccount("BH1001"), "2026-10-10", "2026-10-31")
	if st.Opening != -70000 || len(st.Lines) != 1 || st.Closing != -100000 {
		t.Fatalf("statement %+v", st)
	}
}
