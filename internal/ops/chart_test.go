package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// billedDay is the sample day with billing run, so the books carry
// brokerage, taxes, clearing and client balances.
func billedDay(t *testing.T) (*Ops, context.Context) {
	t.Helper()
	o, ctx := setup(t)
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBilling(ctx, T, "farhan.khan", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	return o, ctx
}

// TestDefaultHeadsAndSystemAccounts: every firm starts with the level-2
// heads, and every account the code posts to (and every code the sample
// day's journal used) has a default head under the right level-1 head.
func TestDefaultHeadsAndSystemAccounts(t *testing.T) {
	o, ctx := billedDay(t)
	hs, err := o.Heads(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]AccountHead{}
	per := map[string]int{}
	for _, h := range hs {
		byName[h.Name] = h
		per[h.Level1]++
		if h.Level != 2 || !h.Default {
			t.Fatalf("head %+v", h)
		}
	}
	if per[L1Assets] != 9 || per[L1Liabilities] != 8 || per[L1Income] != 5 || per[L1Expenditure] != 10 {
		t.Fatalf("heads per level 1: %v", per)
	}
	if byName["Capital and reserves"].Level1 != L1Liabilities {
		t.Fatal("capital sits under liabilities")
	}
	want := map[string]string{
		ledger.BankClient: "A-CLIENTBANK", ledger.BankOwn: "A-OWNBANK", "CASH:PETTY": "A-CASH", "CC:NSE-CM": "A-DEPOSITS", ledger.CCCollateral: "A-DEPOSITS",
		InputIGST: "A-RECV", ReceivableTDS: "A-RECV", "ASSET:COMPUTERS": "A-FIXED", ledger.OpeningEquity: "L-CAPITAL", "EQUITY:CAPITAL": "L-CAPITAL",
		"LOAN:BANK_OD": "L-LOANS", "AP:AP001": "L-AP", ledger.PayableSTT: "L-DUTIES", ledger.PayableStamp: "L-DUTIES", ledger.PayableGST: "L-DUTIES",
		ledger.PayableSEBIFee: "L-DUTIES", "PAYABLE:TDS:194C": "L-DUTIES", AcctTDSSalary: "L-DUTIES", AcctPFPayable: "L-DUTIES", ledger.ExchangeTxnPayable("NSE"): "L-EXCH",
		PayableDepository: "L-EXCH", ledger.PayablePenalty: "L-EXCH", "PAYABLE:VENDOR:V0001": "L-OTHER", AcctSalaryPayable: "L-OTHER", PayablePrepaid: "L-OTHER",
		ledger.SuspenseBank: "L-SUSPENSE", ledger.IncomeBrokerage: "I-BROK", IncomeInterest: "I-INT", IncomeMTFInterest: "I-INT", IncomeDP: "I-DP", IncomeCharges: "I-OPER",
		AcctStaffRecovered: "I-OTHER", AcctSalaries: "E-STAFF", AcctEmployerPF: "E-STAFF", "EXPENSE:RENT": "E-RENT", "EXPENSE:GATEWAY_FEES": "E-BANK", ExpenseBadDebts: "E-BAD",
		ExpenseAPShare: "E-APCOMM", "EXPENSE:LEGAL": "E-PROF", ExpenseIPOCompensated: "E-OTHER",
	}
	for code, h := range want {
		if got := defaultHeadOf(code); got != h {
			t.Errorf("%s: head %s, want %s", code, got, h)
		}
	}
	bal, _ := o.App.Ledger.Balances(ctx, T, D)
	codes := sortedKeys(systemNames)
	for c := range bal {
		if !ledger.IsClientAccount(c) {
			codes = append(codes, c)
		}
	}
	set := o.heads(ctx, T)
	for _, c := range codes {
		id := defaultHeadOf(c)
		h, ok := set.get(id)
		typ, _ := ledger.TypeOf(c)
		if !ok || h.Level1 != level1Of(typ) || h.Clients {
			t.Errorf("%s: default head %q (%+v)", c, id, h)
		}
	}
	// The chart lists every account with its head; clients are one line.
	as, _ := o.Accounts(ctx, T, "farhan.khan", D)
	for _, a := range as {
		if a.Code == "CLIENT:*" {
			continue
		}
		if a.HeadName == "" || a.Level1 == "" {
			t.Errorf("%s has no head", a.Code)
		}
	}
}

// oldTotals is the balance sheet and P&L arithmetic as it was before
// heads: by account type alone.
func oldTotals(bal map[string]money.Amount) (assets, liab money.Amount) {
	var retained money.Amount
	net := map[string]money.Amount{}
	for k, v := range bal {
		t, _ := ledger.TypeOf(k)
		switch {
		case ledger.IsClientAccount(k):
			u, _, _ := ledger.ClientCode(k)
			net[u] += v
		case t == ledger.Income || t == ledger.Expense:
			retained -= v
		case t == ledger.Asset && v < 0:
			liab -= v
		case t == ledger.Asset:
			assets += v
		default:
			liab -= v
		}
	}
	for _, v := range net {
		if v > 0 {
			assets += v
		} else {
			liab -= v
		}
	}
	return assets, liab + retained
}

func flatLines(ss []FinSection) map[string]money.Amount {
	out := map[string]money.Amount{}
	var walk func(s FinSection)
	walk = func(s FinSection) {
		for _, l := range s.Lines {
			out[l.Code] += l.Amount
		}
		for _, c := range s.Heads {
			walk(c)
		}
	}
	for _, s := range ss {
		walk(s)
	}
	return out
}

// TestReportsSameByHeads: on the sample day the trial balance by heads
// totals the ledger's own trial balance, and the balance sheet and P&L
// carry the same totals and the same amount on every account as before.
func TestReportsSameByHeads(t *testing.T) {
	o, ctx := billedDay(t)
	tb, _ := o.App.Ledger.TrialBalance(ctx, T, D)
	ht, err := o.TrialBalanceByHeads(ctx, T, "farhan.khan", D)
	if err != nil {
		t.Fatal(err)
	}
	if ht.TotalDebit != tb.TotalDebit || ht.TotalCredit != tb.TotalCredit || !ht.Balanced || tb.TotalDebit == 0 {
		t.Fatalf("trial balance by heads %s / %s, ledger %s / %s", ht.TotalDebit, ht.TotalCredit, tb.TotalDebit, tb.TotalCredit)
	}
	if len(ht.Heads) != 4 || ht.Heads[0].Name != L1Assets || ht.Heads[3].Name != L1Expenditure {
		t.Fatalf("level 1: %+v", ht.Heads)
	}
	var clientsDr, clientsCr int
	for _, n := range ht.Heads {
		for _, h := range n.Heads {
			if h.ID == HeadClientsDr {
				clientsDr = h.Clients
			}
			if h.ID == HeadClientsCr {
				clientsCr = h.Clients
			}
		}
	}
	if clientsDr+clientsCr == 0 {
		t.Fatal("client ledgers not on the trial balance")
	}
	bal, _ := o.App.Ledger.Balances(ctx, T, D)
	wa, wl := oldTotals(bal)
	f, err := o.Financials(ctx, T, "farhan.khan", "2026-04-01", D, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.TotalAssets != wa || f.TotalLiab != wl || !f.Balanced {
		t.Fatalf("balance sheet %s / %s, before heads %s / %s", f.TotalAssets, f.TotalLiab, wa, wl)
	}
	lines := flatLines(f.Assets)
	for k, v := range flatLines(f.Liabilities) {
		lines[k] += v
	}
	for k, v := range bal {
		t2, _ := ledger.TypeOf(k)
		if v == 0 || ledger.IsClientAccount(k) || t2 == ledger.Income || t2 == ledger.Expense {
			continue
		}
		want := v
		if t2 != ledger.Asset || v < 0 {
			want = -v
		}
		if lines[k] != want {
			t.Errorf("%s on the balance sheet %s, want %s", k, lines[k], want)
		}
	}
	// P&L: the flat lines are the old lines, the heads add up to them.
	var inc money.Amount
	es, _ := o.App.Ledger.Store.List(ctx, T, ledger.Filter{From: "2026-04-01", To: D})
	for _, e := range es {
		for _, p := range e.Postings {
			if strings.HasPrefix(p.Account, "INCOME:") {
				inc += p.Credit - p.Debit
			}
		}
	}
	var heads money.Amount
	for _, h := range f.Income.Heads {
		heads += h.Total
	}
	if f.Income.Total != inc || heads != inc || inc == 0 {
		t.Fatalf("income %s, by heads %s, journal %s", f.Income.Total, heads, inc)
	}
	if f.Income.Heads[0].ID != "I-BROK" {
		t.Fatalf("first income head %+v", f.Income.Heads[0])
	}
}

// TestSubHeadsOptionalAndCodes: accounts go straight under a level-2
// head or under sub-heads (nested too); Ledgerline makes codes with the
// head's prefix; names are unique.
func TestSubHeadsOptionalAndCodes(t *testing.T) {
	o, ctx := setup(t)
	ck := "farhan.khan"
	if _, err := o.SaveHead(ctx, T, "priya.nair", AccountHead{Name: "Branch rent", Parent: "E-RENT"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker changed the chart: %v", err)
	}
	sub, err := o.SaveHead(ctx, T, ck, AccountHead{Name: "Branch rent", Parent: "E-RENT"})
	if err != nil || sub.Level1 != L1Expenditure || sub.Prefix != "EXPENSE" {
		t.Fatalf("sub-head %+v %v", sub, err)
	}
	subsub, err := o.SaveHead(ctx, T, ck, AccountHead{Name: "Bengaluru branch rent", Parent: sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveHead(ctx, T, ck, AccountHead{Name: "branch RENT", Parent: "E-RENT"}); err == nil {
		t.Fatal("duplicate head name")
	}
	if _, err := o.SaveHead(ctx, T, ck, AccountHead{Name: "Debtors", Parent: HeadClientsDr}); err == nil {
		t.Fatal("sub-head under clients")
	}
	reCode := regexp.MustCompile(`^EXPENSE:A\d{4}$`)
	direct, err := o.SaveAccount(ctx, T, ck, Account{Name: "Head office rent", Head: "E-RENT"})
	if err != nil || !reCode.MatchString(direct.Code) || direct.HeadName != "Rent and office" || direct.SubHead != "" {
		t.Fatalf("direct %+v %v", direct, err)
	}
	deep, err := o.SaveAccount(ctx, T, ck, Account{Name: "Koramangala rent", Alias: "KRENT", Head: subsub.ID})
	if err != nil || !reCode.MatchString(deep.Code) || deep.Code == direct.Code || deep.SubHead != "Branch rent › Bengaluru branch rent" {
		t.Fatalf("deep %+v %v", deep, err)
	}
	bank, err := o.SaveAccount(ctx, T, ck, Account{Name: "Second current account", Head: "A-OWNBANK"})
	if err != nil || !strings.HasPrefix(bank.Code, "BANK:A") || !isMoneyAccount(bank.Code) {
		t.Fatalf("bank %+v %v", bank, err)
	}
	// Each head's prefix gives accounts of the head's level-1 type.
	for _, d := range defaultHeads {
		if d.prefix == "" {
			continue
		}
		ty, err := ledger.TypeOf(d.prefix + ":A0001")
		if err != nil || level1Of(ty) != d.level1 {
			t.Errorf("%s: prefix %s is %s", d.id, d.prefix, ty)
		}
	}
	for _, h := range []string{HeadClientsDr, "A-CLIENTBANK", "NOPE"} {
		if _, err := o.SaveAccount(ctx, T, ck, Account{Name: "Anything " + h, Head: h}); err == nil {
			t.Errorf("account made under %s", h)
		}
	}
	for _, n := range []string{"head office RENT", "  Head   office rent ", "Brokerage", "krent"} {
		if _, err := o.SaveAccount(ctx, T, ck, Account{Name: n, Head: "E-OTHER"}); err == nil {
			t.Errorf("name %q accepted twice", n)
		}
	}
	if _, err := o.SaveAccount(ctx, T, ck, Account{Name: "Something new", Alias: "Head office rent", Head: "E-OTHER"}); err == nil {
		t.Error("alias that is another account's name")
	}
	// Old codes keep working, and renaming keeps the head.
	if _, err := o.SaveAccount(ctx, T, ck, Account{Code: "EXPENSE:RENT", Name: "Office rent (old code)"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveAccount(ctx, T, ck, Account{Code: "EXPENSE:RENT", Name: "Office rent (old code)", Head: "E-TECH"}); err == nil {
		t.Fatal("head changed without a second person")
	}
	// Vouchers on the new accounts show by head in the P&L.
	v, err := o.SaveVoucher(ctx, T, "priya.nair", Voucher{Type: "Payment", Date: D, Narration: "Rent", Lines: []VoucherLine{
		{Account: direct.Code, Debit: 300000}, {Account: deep.Code, Debit: 200000}, {Account: bank.Code, Credit: 500000}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideVoucher(ctx, T, ck, v.No, "approve", ""); err != nil {
		t.Fatal(err)
	}
	f, _ := o.Financials(ctx, T, ck, "2026-04-01", D, "")
	var rent FinSection
	for _, s := range f.Expenses.Heads {
		if s.ID == "E-RENT" {
			rent = s
		}
	}
	if rent.Total != 500000 || len(rent.Lines) != 1 || rent.Lines[0].Code != direct.Code || len(rent.Heads) != 1 || rent.Heads[0].Heads[0].Lines[0].Code != deep.Code {
		t.Fatalf("rent section %+v", rent)
	}
	// Removing: fixed heads stay; a used sub-head stays; an empty one goes.
	if err := o.RemoveHead(ctx, T, ck, "E-RENT"); err == nil {
		t.Fatal("removed a system head")
	}
	if err := o.RemoveHead(ctx, T, ck, subsub.ID); err == nil {
		t.Fatal("removed a head with an account")
	}
	if err := o.RemoveHead(ctx, T, ck, sub.ID); err == nil {
		t.Fatal("removed a head with sub-heads")
	}
	if err := o.RemoveHead(ctx, T, ck, "E-DEPR"); err != nil {
		t.Fatalf("unused default head: %v", err)
	}
	// Reorder and rename a default head.
	if err := o.ReorderHead(ctx, T, ck, "E-TECH", "up"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveHead(ctx, T, ck, AccountHead{ID: "E-TECH", Name: "IT and software"}); err != nil {
		t.Fatal(err)
	}
	hs, _ := o.Heads(ctx, T, D)
	var order []string
	for _, h := range hs {
		if h.Level1 == L1Expenditure && h.Level == 2 {
			order = append(order, h.Name)
		}
		if h.ID == "E-DEPR" {
			t.Fatal("removed head listed")
		}
	}
	if strings.Join(order[:3], "|") != "Exchange and clearing charges|Staff costs|IT and software" {
		t.Fatalf("order %v", order)
	}
}

// TestAccountSearch: prefix, word-start and in-word matches in that
// order, aliases and client codes, highlighting, restricted accounts and
// filters.
func TestAccountSearch(t *testing.T) {
	o, ctx := billedDay(t)
	ck := "farhan.khan"
	for _, a := range []Account{{Name: "Rent paid in advance", Head: "A-RECV"}, {Name: "Office maintenance", Alias: "OFM", Head: "E-RENT"}, {Name: "Recurrent subscriptions", Head: "E-TECH"},
		{Name: "Directors remuneration", Head: "E-STAFF", Restricted: true, AllowedUsers: []string{ck}}} {
		if _, err := o.SaveAccount(ctx, T, ck, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.SaveAccount(ctx, T, ck, Account{Code: "EXPENSE:RENT", Name: "Office rent"}); err != nil {
		t.Fatal(err)
	}
	r, err := o.SearchAccounts(ctx, T, "priya.nair", AccountSearch{Q: "rent", Date: D, Limit: 50, Clients: "no"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range r.Hits {
		got = append(got, fmt.Sprintf("%s:%d", h.Name, h.rank))
	}
	idx := func(n string) int {
		for i, h := range r.Hits {
			if h.Name == n {
				return i
			}
		}
		return -1
	}
	ra, of, rc := idx("Rent paid in advance"), idx("Office rent"), idx("Recurrent subscriptions")
	if ra < 0 || of < 0 || rc < 0 || !(ra < of && of < rc) {
		t.Fatalf("ranking: %v", got)
	}
	h := r.Hits[of]
	if h.Match != "name" || h.At != 7 || h.Len != 4 || h.Head != "Rent and office" || h.HeadID != "E-RENT" {
		t.Fatalf("office rent hit %+v", h)
	}
	if r.Hits[rc].At != 5 || r.Hits[rc].rank != 2 {
		t.Fatalf("in-word %+v", r.Hits[rc])
	}
	// Alias.
	r, _ = o.SearchAccounts(ctx, T, "priya.nair", AccountSearch{Q: "ofm", Date: D})
	if len(r.Hits) == 0 || r.Hits[0].Name != "Office maintenance" || r.Hits[0].Match != "alias" {
		t.Fatalf("alias: %+v", r.Hits)
	}
	// Restricted: hidden from the maker, shown to the allowed checker.
	r, _ = o.SearchAccounts(ctx, T, "priya.nair", AccountSearch{Q: "directors", Date: D})
	if r.Total != 0 {
		t.Fatal("restricted account found by the maker")
	}
	r, _ = o.SearchAccounts(ctx, T, ck, AccountSearch{Q: "directors", Date: D})
	if r.Total != 1 {
		t.Fatal("restricted account hidden from its user")
	}
	// Clients: by UCC (exact first) and name, with balances.
	cs, _ := o.Clients(ctx, T)
	c := cs[0]
	r, _ = o.SearchAccounts(ctx, T, "priya.nair", AccountSearch{Q: strings.ToLower(c.UCC), Date: D})
	if len(r.Hits) == 0 || r.Hits[0].Code != ledger.ClientAccount(c.UCC) || !r.Hits[0].Client || r.Hits[0].Match != "ucc" || r.Hits[0].rank != 0 {
		t.Fatalf("ucc: %+v", r.Hits)
	}
	bal, _ := o.App.Ledger.Balances(ctx, T, D)
	if r.Hits[0].Balance != bal[ledger.ClientAccount(c.UCC)] {
		t.Fatal("client balance")
	}
	word := strings.Fields(c.Name)[0][1:3]
	r, _ = o.SearchAccounts(ctx, T, "priya.nair", AccountSearch{Q: word, Date: D, Limit: 200})
	found := false
	for _, h := range r.Hits {
		found = found || h.Code == ledger.ClientAccount(c.UCC)
	}
	if !found {
		t.Fatalf("client not found by %q in the middle of its name", word)
	}
	// Firm accounts before clients in the same rank; limit and more.
	r, _ = o.SearchAccounts(ctx, T, "priya.nair", AccountSearch{Q: "", Date: D, Limit: 5})
	if len(r.Hits) != 5 || !r.More || r.Hits[0].Client {
		t.Fatalf("empty query: %+v", r)
	}
	// Only bank and cash.
	r, _ = o.SearchAccounts(ctx, T, "priya.nair", AccountSearch{Q: "", Date: D, Limit: 50, Prefixes: []string{"BANK", "CASH"}})
	for _, h := range r.Hits {
		if !isMoneyAccount(h.Code) {
			t.Fatalf("money filter let %s through", h.Code)
		}
	}
	// Client debit balances, biggest first.
	r, _ = o.SearchAccounts(ctx, T, "priya.nair", AccountSearch{Date: D, Limit: 200, Clients: "only", Side: "dr"})
	for i, h := range r.Hits {
		if !h.Client || h.Balance <= 0 || i > 0 && h.Balance > r.Hits[i-1].Balance {
			t.Fatalf("debtors list %+v", r.Hits)
		}
	}
	// Who may search.
	if _, err := o.SearchAccounts(ctx, T, "nobody", AccountSearch{Q: "x"}); err == nil {
		t.Fatal("unknown user searched")
	}
}

func TestMatchAt(t *testing.T) {
	for _, c := range []struct {
		s, q     string
		rank, at int
	}{{"office rent", "off", 0, 0}, {"office rent", "rent", 1, 7}, {"recurrent", "rent", 2, 5}, {"rent-a-rent", "rent", 0, 0},
		{"a-rent current", "rent", 1, 2}, {"current rent", "rent", 1, 8}, {"ऑफिस किराया", "किराया", 1, 5}, {"abc", "x", -1, 0}} {
		r, a := matchAt(c.s, c.q)
		if r != c.rank || r >= 0 && a != c.at {
			t.Errorf("matchAt(%q, %q) = %d, %d; want %d, %d", c.s, c.q, r, a, c.rank, c.at)
		}
	}
}

// TestPendingAccountFromVoucherEntry: a maker makes an account while
// entering a voucher; the voucher cannot post until a checker approves,
// and approving the voucher approves the account. A rejected account
// stops vouchers.
func TestPendingAccountFromVoucherEntry(t *testing.T) {
	o, ctx := setup(t)
	mk, ck := "priya.nair", "farhan.khan"
	if _, err := o.NewAccount(ctx, T, "anita.rao", Account{Name: "Courier charges", Head: "E-OTHER"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk made an account: %v", err)
	}
	a, err := o.NewAccount(ctx, T, mk, Account{Name: "Courier charges", Head: "E-OTHER", Alias: "cour"})
	if err != nil || a.Status != accountPending || !strings.HasPrefix(a.Code, "EXPENSE:A") || a.CreatedBy != mk {
		t.Fatalf("new account %+v %v", a, err)
	}
	if _, err := o.NewAccount(ctx, T, mk, Account{Name: "courier CHARGES", Head: "E-OTHER"}); err == nil {
		t.Fatal("pending name taken twice")
	}
	r, _ := o.SearchAccounts(ctx, T, mk, AccountSearch{Q: "cour", Date: D})
	if len(r.Hits) == 0 || r.Hits[0].Code != a.Code || r.Hits[0].Status != accountPending {
		t.Fatalf("pending account not found: %+v", r.Hits)
	}
	v, err := o.SaveVoucher(ctx, T, mk, Voucher{Type: "Payment", Date: D, Narration: "Courier", Party: "Speedy Couriers", Lines: []VoucherLine{{Account: a.Code, Debit: 50000}, {Account: ledger.BankOwn, Credit: 50000}}})
	if err != nil {
		t.Fatal(err)
	}
	vs, _ := o.Vouchers(ctx, T, ck)
	if len(vs[0].NewAccounts) != 1 || vs[0].NewAccounts[0].Code != a.Code || vs[0].Lines[0].Name != "Courier charges" {
		t.Fatalf("voucher list %+v", vs[0])
	}
	// The checker approves the voucher and the account in one step.
	if v, err = o.DecideVoucher(ctx, T, ck, v.No, "approve", ""); err != nil || v.Status != "Posted" {
		t.Fatalf("approve: %v %+v", err, v)
	}
	m := o.accountMaster(ctx, T)
	if m[a.Code].Status != accountActive || m[a.Code].Checker != ck {
		t.Fatalf("account after approval %+v", m[a.Code])
	}
	// A checker cannot approve a voucher carrying an account they made.
	b, _ := o.NewAccount(ctx, T, ck, Account{Name: "Printing and stationery", Head: "E-RENT"})
	v2, _ := o.SaveVoucher(ctx, T, mk, Voucher{Type: "Payment", Date: D, Narration: "Paper", Lines: []VoucherLine{{Account: b.Code, Debit: 1000}, {Account: ledger.BankOwn, Credit: 1000}}})
	if _, err := o.DecideVoucher(ctx, T, ck, v2.No, "approve", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("checker approved own account: %v", err)
	}
	if _, err := o.DecideAccount(ctx, T, ck, b.Code, "approve", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("checker approved own account directly: %v", err)
	}
	// Rejecting the voucher and its new account together.
	c, _ := o.NewAccount(ctx, T, mk, Account{Name: "Misc gifts", Head: "E-OTHER"})
	v3, _ := o.SaveVoucher(ctx, T, mk, Voucher{Type: "Journal", Date: D, Narration: "Gifts", Lines: []VoucherLine{{Account: c.Code, Debit: 1000}, {Account: ledger.BankOwn, Credit: 1000}}})
	if _, err := o.DecideVoucherAccounts(ctx, T, ck, v3.No, "reject", "use Staff welfare", []string{c.Code}); err != nil {
		t.Fatal(err)
	}
	m = o.accountMaster(ctx, T)
	if m[c.Code].Status != accountRejected || m[c.Code].Reason != "use Staff welfare" {
		t.Fatalf("rejected account %+v", m[c.Code])
	}
	if _, err := o.SaveVoucher(ctx, T, mk, Voucher{Type: "Journal", Date: D, Narration: "Gifts again", Lines: []VoucherLine{{Account: c.Code, Debit: 1000}, {Account: ledger.BankOwn, Credit: 1000}}}); err == nil {
		t.Fatal("voucher on a rejected account saved")
	}
	// A voucher saved before its account was rejected cannot post.
	d, _ := o.NewAccount(ctx, T, mk, Account{Name: "Donations", Head: "E-OTHER"})
	v4, _ := o.SaveVoucher(ctx, T, mk, Voucher{Type: "Payment", Date: D, Narration: "Donation", Lines: []VoucherLine{{Account: d.Code, Debit: 1000}, {Account: ledger.BankOwn, Credit: 1000}}})
	if _, err := o.DecideAccount(ctx, T, ck, d.Code, "reject", "not allowed"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideVoucher(ctx, T, ck, v4.No, "approve", ""); !errors.Is(err, ErrState) {
		t.Fatalf("voucher with a rejected account posted: %v", err)
	}
	if r, _ := o.SearchAccounts(ctx, T, mk, AccountSearch{Q: "donations", Date: D}); r.Total != 0 {
		t.Fatal("rejected account still offered")
	}
}

// TestAccountMoveTwoPeople: moving an account between heads needs a
// second person and is audited; the account keeps its code and type.
func TestAccountMoveTwoPeople(t *testing.T) {
	o, ctx := setup(t)
	mk, ck := "priya.nair", "farhan.khan"
	if _, err := o.RequestAccountMove(ctx, T, mk, "EXPENSE:RENT", "I-OTHER", "wrong side"); err == nil {
		t.Fatal("expense moved under income")
	}
	if _, err := o.RequestAccountMove(ctx, T, mk, "CLIENT:BH1001", "L-OTHER", "x"); err == nil {
		t.Fatal("client moved")
	}
	mv, err := o.RequestAccountMove(ctx, T, mk, "EXPENSE:GATEWAY_FEES", "E-TECH", "the gateway is a software subscription")
	if err != nil || mv.FromName != "Bank and gateway charges" || mv.ToName != "Technology and software" {
		t.Fatalf("move %+v %v", mv, err)
	}
	if _, err := o.RequestAccountMove(ctx, T, ck, "EXPENSE:GATEWAY_FEES", "E-OTHER", "again"); !errors.Is(err, ErrState) {
		t.Fatalf("second pending move: %v", err)
	}
	if _, err := o.DecideAccountMove(ctx, T, mk, mv.ID, "approve", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker approved: %v", err)
	}
	if err := o.RemoveHead(ctx, T, ck, "E-TECH"); err == nil {
		t.Fatal("removed a head a move is waiting for")
	}
	if mv, err = o.DecideAccountMove(ctx, T, ck, mv.ID, "approve", ""); err != nil || mv.Status != "Approved" {
		t.Fatalf("approve %v %+v", err, mv)
	}
	hs := o.heads(ctx, T)
	if hs.headOf("EXPENSE:GATEWAY_FEES", o.accountMaster(ctx, T)) != "E-TECH" {
		t.Fatal("not moved")
	}
	as, _ := o.Accounts(ctx, T, ck, D)
	for _, a := range as {
		if a.Code == "EXPENSE:GATEWAY_FEES" && (a.HeadName != "Technology and software" || a.Name != "Payment gateway fees") {
			t.Fatalf("moved account %+v", a)
		}
	}
	evs, _ := o.App.Audit.List(ctx, T, 0)
	var seen []string
	for _, e := range evs {
		if strings.HasPrefix(e.Action, "account.move") {
			seen = append(seen, e.Action+" "+e.Actor)
		}
	}
	if strings.Join(seen, ",") != "account.move_requested priya.nair,account.moved farhan.khan" && strings.Join(seen, ",") != "account.moved farhan.khan,account.move_requested priya.nair" {
		t.Fatalf("audit %v", seen)
	}
	m2, _ := o.RequestAccountMove(ctx, T, ck, "EXPENSE:GATEWAY_FEES", "E-BANK", "back")
	if _, err := o.DecideAccountMove(ctx, T, mk, m2.ID, "reject", "no"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker decided a move: %v", err)
	}
}

// TestAccountSearchManyClients: a firm with 5,000+ clients searches
// quickly once the index is built.
func TestAccountSearchManyClients(t *testing.T) {
	if testing.Short() {
		t.Skip("large firm")
	}
	o, ctx := setup(t)
	first := []string{"Aarav", "Vivaan", "Aditya", "Vihaan", "Arjun", "Sai", "Reyansh", "Ayaan", "Krishna", "Ishaan", "Ananya", "Diya", "Saanvi", "Aadhya", "Pari"}
	last := []string{"Sharma", "Verma", "Iyer", "Nair", "Reddy", "Patel", "Shah", "Mehta", "Rao", "Kulkarni", "Menon", "Gupta"}
	for i := range 5200 {
		u := fmt.Sprintf("ZZ%05d", i)
		c := Client{UCC: u, Name: first[i%len(first)] + " " + last[(i/len(first))%len(last)] + fmt.Sprintf(" %d", i), Status: "Active"}
		if _, err := docs.Put(ctx, o.Docs, T, cClient, u, c, 0, "test"); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	r, err := o.SearchAccounts(ctx, T, "priya.nair", AccountSearch{Q: "a", Date: D})
	if err != nil || r.Total < 5200 {
		t.Fatalf("first search %v, %d hits", err, r.Total)
	}
	build := time.Since(start)
	start = time.Now()
	n := 0
	for _, q := range []string{"aa", "aar", "aara", "aarav", "aarav s", "sha", "shar", "zz0", "zz04", "zz042", "zz0421", "menon 1", "ish", "ishaan", "rao", "rent", "gst", "brok"} {
		r, err := o.SearchAccounts(ctx, T, "priya.nair", AccountSearch{Q: q, Date: D})
		if err != nil {
			t.Fatal(err)
		}
		n += len(r.Hits)
		if len(r.Hits) > 15 {
			t.Fatal("limit")
		}
	}
	each := time.Since(start) / 18
	t.Logf("index build %v, each search %v (%d hits)", build, each, n)
	if each > 500*time.Millisecond {
		t.Fatalf("searching is slow: %v a search", each)
	}
	r, _ = o.SearchAccounts(ctx, T, "priya.nair", AccountSearch{Q: "zz04211", Date: D})
	if len(r.Hits) != 1 || r.Hits[0].UCC != "ZZ04211" || r.Hits[0].Head != "Clients" {
		t.Fatalf("exact ucc %+v", r.Hits)
	}
}
