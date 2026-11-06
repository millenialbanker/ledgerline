package ledger

import (
	"context"
	"testing"

	"ledgerline/internal/money"
)

func TestClientCode(t *testing.T) {
	for acct, want := range map[string][2]string{
		"CLIENT:BJ01":        {"BJ01", LedgerEquity},
		"CLIENTCOM:BJ01":     {"BJ01", LedgerCommodity},
		"CLIENTUNALLOC:BJ01": {"BJ01", LedgerAwaiting},
	} {
		u, k, ok := ClientCode(acct)
		if !ok || u != want[0] || k != want[1] {
			t.Errorf("%s: %s %s %v", acct, u, k, ok)
		}
		if ClientLedgerAccount(want[0], want[1]) != acct {
			t.Errorf("%s: account %s", acct, ClientLedgerAccount(want[0], want[1]))
		}
		if typ, err := TypeOf(acct); err != nil || typ != Liability {
			t.Errorf("%s: %s %v", acct, typ, err)
		}
	}
	if _, _, ok := ClientCode("CC:NSE-CM"); ok {
		t.Error("clearing account read as a client's")
	}
}

// The comprehensive statement merges a client's ledgers with one running
// balance; the client's own view leaves out what only moves money between
// them; with one ledger it is exactly the account's statement.
func TestClientStatement(t *testing.T) {
	ctx := context.Background()
	l := New(NewMemStore())
	post := func(key, date string, ps ...Posting) {
		t.Helper()
		if _, _, err := l.Post(ctx, Entry{Tenant: "t1", Key: key, ValueDate: date, Narration: key, CreatedBy: "ops", Postings: ps}); err != nil {
			t.Fatal(err)
		}
	}
	a := money.MustAmount
	post("open", "2026-10-28", Posting{Account: ClientAccount("C1"), Credit: a("10000")}, Posting{Account: ClientCommodityAccount("C1"), Credit: a("200000")}, Posting{Account: OpeningEquity, Debit: a("210000")})
	post("rcpt", "2026-10-29", Posting{Account: BankClient, Debit: a("5000")}, Posting{Account: ClientAwaitingAccount("C1"), Credit: a("5000")})
	post("alloc", "2026-10-29", Posting{Account: ClientAwaitingAccount("C1"), Debit: a("5000")}, Posting{Account: ClientAccount("C1"), Credit: a("5000")})
	post("bill", "2026-10-29", Posting{Account: ClientCommodityAccount("C1"), Debit: a("700")}, Posting{Account: IncomeBrokerage, Credit: a("700")})
	post("xfer", "2026-10-30", Posting{Account: ClientCommodityAccount("C1"), Debit: a("1000")}, Posting{Account: ClientAccount("C1"), Credit: a("1000")})
	post("other", "2026-10-30", Posting{Account: ClientAccount("C2"), Credit: a("1")}, Posting{Account: BankClient, Debit: a("1")})

	st, err := l.ClientStatement(ctx, "t1", "C1", "2026-10-29", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Account != "CLIENT:C1" || st.Opening != -a("210000") || st.Closing != -a("214300") || len(st.Lines) != 2 {
		t.Fatalf("%+v", st)
	}
	if st.Lines[0].Credit != a("5000") || st.Lines[0].Balance != -a("215000") || st.Lines[1].Debit != a("700") || st.Lines[1].Balance != -a("214300") || st.Lines[1].Ledger != "" {
		t.Fatalf("%+v", st.Lines)
	}
	staff, _ := l.ClientStatement(ctx, "t1", "C1", "", "", true)
	if len(staff.Lines) != 8 || staff.Closing != st.Closing || staff.Lines[1].Ledger != LedgerCommodity {
		t.Fatalf("%+v", staff.Lines)
	}
	one, _ := l.ClientStatement(ctx, "t1", "C2", "", "", false)
	raw, _ := l.Statement(ctx, "t1", ClientAccount("C2"), "", "")
	if len(one.Lines) != 1 || one.Lines[0] != raw.Lines[0] || one.Closing != raw.Closing {
		t.Fatalf("%+v vs %+v", one, raw)
	}
}
