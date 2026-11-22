package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- the chart of accounts as heads, the way Tally keeps it ----
//
// Every account sits under a head. Level 1 is fixed: Assets, Liabilities,
// Income and Expenditure (capital and reserves sit under Liabilities, as
// in Tally). Level 2 are the major heads every firm starts with, which a
// firm may rename, add to and reorder. Below them a firm may add sub-heads
// (and sub-heads of sub-heads), or put accounts straight under a level-2
// head. Inside, an account is still a ledger code whose prefix sets its
// type (ledger.TypeOf); a head only says where the account is shown. A
// new account gets its code from Ledgerline, with the prefix its head
// carries, so people pick heads and names and never type a code.

const (
	cHead        = "account_head"
	cAccountMove = "account_move"

	L1Assets      = "Assets"
	L1Liabilities = "Liabilities"
	L1Income      = "Income"
	L1Expenditure = "Expenditure"

	// The two client heads: each client's balance goes to one of them by
	// its sign on the balance sheet. They hold no accounts of their own.
	HeadClientsDr = "A-CLIENTDR"
	HeadClientsCr = "L-CLIENTCR"

	accountActive   = "Active"
	accountPending  = "Pending"
	accountRejected = "Rejected"
)

// Level1Heads are the fixed level-1 heads in report order.
var Level1Heads = []string{L1Assets, L1Liabilities, L1Income, L1Expenditure}

// AccountHead is a level-2 head or a sub-head.
type AccountHead struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Level1 string `json:"level1"`
	// Parent is empty for a level-2 head; for a sub-head it is the head
	// it sits under (a level-2 head or another sub-head).
	Parent string `json:"parent,omitempty"`
	// Prefix is the ledger prefix new accounts under the head get (a
	// sub-head uses its level-2 head's); empty where accounts cannot be
	// added by hand (the client heads, the segregated client bank).
	Prefix  string `json:"prefix,omitempty"`
	Order   int    `json:"order"`
	Default bool   `json:"default"`
	Clients bool   `json:"clients,omitempty"`
	Removed bool   `json:"removed,omitempty"`
	By      string `json:"by,omitempty"`
	// Worked out when listed.
	Level    int    `json:"level"`
	Path     string `json:"path"`     // level-2 head › sub-head › …
	Accounts int    `json:"accounts"` // accounts directly under it
	Fixed    bool   `json:"fixed"`    // a system account's default home: cannot be removed
}

type defHead struct{ id, level1, name, prefix string }

// defaultHeads are the level-2 heads every firm starts with.
var defaultHeads = []defHead{
	{"A-FIXED", L1Assets, "Fixed assets", "ASSET"},
	{"A-INVEST", L1Assets, "Investments (FDs, securities)", "ASSET"},
	{HeadStock, L1Assets, "Stock-in-trade (securities)", "ASSET"},
	{"A-DEPOSITS", L1Assets, "Deposits with exchanges and clearing corporations", "ASSET"},
	{"A-OWNBANK", L1Assets, "Own bank accounts", "BANK"},
	{"A-CLIENTBANK", L1Assets, "Client bank accounts (segregated)", ""},
	{"A-CASH", L1Assets, "Cash", "CASH"},
	{HeadClientsDr, L1Assets, "Clients with debit balances", ""},
	{"A-RECV", L1Assets, "Receivables and advances (GST input, TDS)", "RECEIVABLE"},
	{"A-OTHER", L1Assets, "Other current assets", "ASSET"},
	{"L-CAPITAL", L1Liabilities, "Capital and reserves", "EQUITY"},
	{"L-LOANS", L1Liabilities, "Loans", "LOAN"},
	{HeadClientsCr, L1Liabilities, "Clients with credit balances", ""},
	{"L-AP", L1Liabilities, "Authorised persons", "AP"},
	{"L-DUTIES", L1Liabilities, "Duties and taxes (STT, stamp duty, GST, TDS, SEBI fee)", "PAYABLE"},
	{"L-EXCH", L1Liabilities, "Payable to exchanges, clearing and depository", "PAYABLE"},
	{"L-OTHER", L1Liabilities, "Provisions and other liabilities", "PAYABLE"},
	{"L-SUSPENSE", L1Liabilities, "Suspense", "SUSPENSE"},
	{HeadSales, L1Income, "Sales of securities (stock-in-trade)", "INCOME"},
	{"I-BROK", L1Income, "Brokerage", "INCOME"},
	{"I-INT", L1Income, "Interest (delayed payment, MTF, FDs)", "INCOME"},
	{"I-DP", L1Income, "DP income", "INCOME"},
	{"I-OPER", L1Income, "Other operating income", "INCOME"},
	{"I-OTHER", L1Income, "Other income", "INCOME"},
	{HeadPurch, L1Expenditure, "Purchases of securities (stock-in-trade)", "EXPENSE"},
	{"E-EXCH", L1Expenditure, "Exchange and clearing charges", "EXPENSE"},
	{"E-STAFF", L1Expenditure, "Staff costs", "EXPENSE"},
	{"E-RENT", L1Expenditure, "Rent and office", "EXPENSE"},
	{"E-TECH", L1Expenditure, "Technology and software", "EXPENSE"},
	{"E-PROF", L1Expenditure, "Professional fees", "EXPENSE"},
	{"E-APCOMM", L1Expenditure, "AP commission", "EXPENSE"},
	{"E-BANK", L1Expenditure, "Bank and gateway charges", "EXPENSE"},
	{"E-DEPR", L1Expenditure, "Depreciation", "EXPENSE"},
	{"E-BAD", L1Expenditure, "Bad debts", "EXPENSE"},
	{"E-OTHER", L1Expenditure, "Other expenses", "EXPENSE"},
}

// systemHeads places each system account the code posts to under its
// default head; codePrefixHeads (longest first) and prefixHeads catch
// the rest by code, so every account the journal has ever used has a
// head and the reports keep adding up to the same totals.
var systemHeads = map[string]string{
	ledger.BankClient: "A-CLIENTBANK",
	ledger.PayableSTT: "L-DUTIES", ledger.PayableStamp: "L-DUTIES", ledger.PayableSEBIFee: "L-DUTIES", ledger.PayableGST: "L-DUTIES", PayableRCM: "L-DUTIES",
	AcctTDSSalary: "L-DUTIES", AcctPFPayable: "L-DUTIES", AcctESIPayable: "L-DUTIES", AcctPTPayable: "L-DUTIES", PayableDPStamp: "L-DUTIES",
	ledger.PayablePenalty: "L-EXCH", PayableDepository: "L-EXCH", PayableOFSMargin: "L-EXCH", PayableEIPOFunds: "L-EXCH",
	ledger.IncomeBrokerage: "I-BROK", IncomeSubscription: "I-BROK",
	IncomeInterest: "I-INT", IncomeMTFInterest: "I-INT",
	IncomeDP:      "I-DP",
	IncomeCharges: "I-OPER", IncomeIPOComm: "I-OPER",
	AcctSalaries: "E-STAFF", AcctReimburse: "E-STAFF", AcctEmployerPF: "E-STAFF", AcctEmployerESI: "E-STAFF",
	"EXPENSE:RENT": "E-RENT", "EXPENSE:OFFICE": "E-RENT",
	"EXPENSE:LEGAL": "E-PROF",
	ExpenseAPShare:  "E-APCOMM", ExpenseReferral: "E-APCOMM",
	"EXPENSE:GATEWAY_FEES": "E-BANK",
	ExpenseBadDebts:        "E-BAD",
}

var codePrefixHeads = []struct{ prefix, head string }{
	{"PAYABLE:EXCH_TXN", "L-EXCH"},
	{"PAYABLE:TDS", "L-DUTIES"},
	// The firm's own securities (firmsec.go): one account per portfolio
	// and kind, under the heads the chart keeps for them.
	{"ASSET:FSINV_", headInvest},
	{"ASSET:FSSIT_", HeadStock},
	{"EXPENSE:FSPUR_", HeadPurch},
	{"INCOME:FSSAL_", HeadSales},
	{"EXPENSE:FSCHG_", "E-EXCH"},
}

var prefixHeads = map[string]string{
	"ASSET": "A-FIXED", "CC": "A-DEPOSITS", "BANK": "A-OWNBANK", "CASH": "A-CASH", "RECEIVABLE": "A-RECV",
	"EQUITY": "L-CAPITAL", "LOAN": "L-LOANS", "AP": "L-AP", "PAYABLE": "L-OTHER", "SUSPENSE": "L-SUSPENSE",
	"INCOME": "I-OTHER", "EXPENSE": "E-OTHER",
}

// prefixesFor are the ledger prefixes a new level-2 head may carry, by
// level-1 head, with what each means in plain words.
var prefixesFor = map[string][][2]string{
	L1Assets:      {{"ASSET", "Fixed or other asset"}, {"BANK", "Bank account (pays and receives)"}, {"CASH", "Cash (pays and receives)"}, {"RECEIVABLE", "Receivable or advance"}},
	L1Liabilities: {{"PAYABLE", "Payable or provision"}, {"LOAN", "Loan"}, {"EQUITY", "Capital or reserve"}, {"AP", "Authorised person"}, {"SUSPENSE", "Suspense"}},
	L1Income:      {{"INCOME", "Income"}},
	L1Expenditure: {{"EXPENSE", "Expense"}},
}

// HeadPrefixes lists the prefixes a new level-2 head may carry.
func HeadPrefixes() map[string][][2]string { return prefixesFor }

// level1Of is the level-1 head an account type belongs to.
func level1Of(t ledger.AccountType) string {
	switch t {
	case ledger.Asset:
		return L1Assets
	case ledger.Income:
		return L1Income
	case ledger.Expense:
		return L1Expenditure
	}
	return L1Liabilities // liabilities and capital
}

// defaultHeadOf is the head a code goes under when nobody has moved it.
func defaultHeadOf(code string) string {
	if h, ok := systemHeads[code]; ok {
		return h
	}
	for _, p := range codePrefixHeads {
		if strings.HasPrefix(code, p.prefix) {
			return p.head
		}
	}
	p, _, _ := strings.Cut(code, ":")
	return prefixHeads[p]
}

// headSet is a firm's heads, indexed.
type headSet struct {
	byID  map[string]*AccountHead
	order []string // depth first, in display order
}

func (hs headSet) get(id string) (AccountHead, bool) {
	h, ok := hs.byID[id]
	if !ok || h.Removed {
		return AccountHead{}, false
	}
	return *h, true
}

// root is the level-2 head above a head.
func (hs headSet) root(id string) string {
	for i := 0; i < 32; i++ {
		h, ok := hs.byID[id]
		if !ok || h.Parent == "" {
			return id
		}
		id = h.Parent
	}
	return id
}

// children are a head's live children (level-2 heads for a level-1 name).
func (hs headSet) children(parent, level1 string) []*AccountHead {
	var out []*AccountHead
	for _, h := range hs.byID {
		if h.Removed {
			continue
		}
		if parent == "" && h.Parent == "" && h.Level1 == level1 || parent != "" && h.Parent == parent {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// names are the level-2 head and the sub-head path of a head.
func (hs headSet) names(id string) (head, sub string) {
	var chain []string
	for i := 0; i < 32 && id != ""; i++ {
		h, ok := hs.byID[id]
		if !ok {
			break
		}
		chain = append([]string{h.Name}, chain...)
		id = h.Parent
	}
	if len(chain) == 0 {
		return "", ""
	}
	return chain[0], strings.Join(chain[1:], " › ")
}

// heads loads the firm's heads: the defaults with the firm's renames and
// order, and the heads the firm added.
func (o *Ops) heads(ctx context.Context, tenant string) headSet {
	hs := headSet{byID: map[string]*AccountHead{}}
	for i, d := range defaultHeads {
		hs.byID[d.id] = &AccountHead{ID: d.id, Name: d.name, Level1: d.level1, Prefix: d.prefix, Order: (i + 1) * 10, Default: true, Clients: d.id == HeadClientsDr || d.id == HeadClientsCr}
	}
	saved, _ := docs.List[AccountHead](ctx, o.Docs, tenant, cHead, "")
	for _, s := range saved {
		if d, ok := hs.byID[s.ID]; ok && d.Default {
			d.Name, d.Order, d.Removed = s.Name, s.Order, s.Removed
			continue
		}
		s := s
		s.Default, s.Clients = false, false
		hs.byID[s.ID] = &s
	}
	// Sub-heads take their level-1 head and prefix from their level-2 head.
	for _, h := range hs.byID {
		if h.Parent != "" {
			if r, ok := hs.byID[hs.root(h.ID)]; ok {
				h.Level1, h.Prefix = r.Level1, r.Prefix
			}
		}
	}
	var walk func(parent, l1 string, level int)
	walk = func(parent, l1 string, level int) {
		for _, h := range hs.children(parent, l1) {
			h.Level = level
			head, sub := hs.names(h.ID)
			h.Path = head
			if sub != "" {
				h.Path += " › " + sub
			}
			hs.order = append(hs.order, h.ID)
			walk(h.ID, l1, level+1)
		}
	}
	for _, l1 := range Level1Heads {
		walk("", l1, 2)
	}
	fixed := map[string]bool{HeadClientsDr: true, HeadClientsCr: true}
	for _, h := range systemHeads {
		fixed[h] = true
	}
	for _, h := range prefixHeads {
		fixed[h] = true
	}
	for _, p := range codePrefixHeads {
		fixed[p.head] = true
	}
	for id := range fixed {
		if h, ok := hs.byID[id]; ok {
			h.Fixed = true
		}
	}
	return hs
}

// headOf is the head an account is shown under: where it was put or
// moved, or its default.
func (hs headSet) headOf(code string, m map[string]Account) string {
	if a, ok := m[code]; ok && a.Head != "" {
		if _, ok := hs.get(a.Head); ok {
			return a.Head
		}
	}
	return defaultHeadOf(code)
}

// accountName is the name an account is shown by.
func accountName(code string, m map[string]Account) string {
	if a, ok := m[code]; ok && a.Name != "" {
		return a.Name
	}
	if n := systemNames[code]; n != "" {
		return n
	}
	// Accounts the code opens one of per exchange, section, partner or
	// client get names that say what they are.
	for _, p := range []struct{ prefix, name string }{{"PAYABLE:EXCH_TXN:", "Exchange transaction charges payable · "}, {"PAYABLE:TDS:", "TDS payable · "},
		{"RECEIVABLE:MTF:", "Margin trading funding · "}, {"AP:", "Authorised person · "}, {"CC:", "Clearing settlement · "}} {
		if rest, ok := strings.CutPrefix(code, p.prefix); ok && rest != "" {
			return p.name + strings.ReplaceAll(rest, "-", " ")
		}
	}
	_, q, _ := strings.Cut(code, ":")
	return strings.ReplaceAll(strings.Title(strings.ToLower(strings.ReplaceAll(q, "_", " "))), ":", " · ") //nolint:staticcheck
}

// Heads lists the firm's heads in display order, with how many accounts
// sit directly under each.
func (o *Ops) Heads(ctx context.Context, tenant, asOf string) ([]AccountHead, error) {
	hs := o.heads(ctx, tenant)
	m := o.accountMaster(ctx, tenant)
	bal, err := o.App.Ledger.Balances(ctx, tenant, asOf)
	if err != nil {
		return nil, err
	}
	count := map[string]int{}
	for _, code := range chartCodes(m, bal) {
		count[hs.headOf(code, m)]++
	}
	out := make([]AccountHead, 0, len(hs.order))
	for _, id := range hs.order {
		h := *hs.byID[id]
		h.Accounts = count[id]
		out = append(out, h)
	}
	return out, nil
}

// chartCodes are the firm's account codes other than clients': saved
// accounts (not rejected ones), system accounts and every code the
// journal has used.
func chartCodes(m map[string]Account, bal map[string]money.Amount) []string {
	set := map[string]bool{}
	for code, a := range m {
		if a.Status != accountRejected {
			set[code] = true
		}
	}
	for code := range systemNames {
		set[code] = true
	}
	for code := range bal {
		if !ledger.IsClientAccount(code) {
			set[code] = true
		}
	}
	return sortedKeys(set)
}

func acctNorm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// SaveHead adds a head or sub-head, or renames one.
func (o *Ops) SaveHead(ctx context.Context, tenant, actor string, h AccountHead) (AccountHead, error) {
	if err := o.require(ctx, tenant, actor, "Changing the chart of accounts", RoleChecker); err != nil {
		return h, err
	}
	h.Name = strings.Join(strings.Fields(h.Name), " ")
	if h.Name == "" {
		return h, invalid("give the head a name")
	}
	hs := o.heads(ctx, tenant)
	for _, x := range hs.byID {
		if !x.Removed && x.ID != h.ID && acctNorm(x.Name) == acctNorm(h.Name) {
			return h, invalid("there is already a head called %s", x.Name)
		}
	}
	if h.ID != "" {
		old, ok := hs.get(h.ID)
		if !ok {
			return h, fmt.Errorf("%w: head %s", ErrNotFound, h.ID)
		}
		old.Name, old.By = h.Name, actor
		if err := o.putHead(ctx, tenant, actor, old); err != nil {
			return old, err
		}
		o.log(ctx, tenant, actor, "account_head.renamed", old.ID, map[string]any{"name": old.Name})
		return old, nil
	}
	if h.Parent != "" {
		p, ok := hs.get(h.Parent)
		if !ok {
			return h, invalid("the head %s to put it under was not found", h.Parent)
		}
		if p.Clients {
			return h, invalid("clients are kept by Ledgerline: %s has no sub-heads", p.Name)
		}
		h.Level1, h.Prefix = p.Level1, p.Prefix
	} else {
		if !contains(Level1Heads, h.Level1) {
			return h, invalid("a head sits under Assets, Liabilities, Income or Expenditure")
		}
		okp := false
		for _, p := range prefixesFor[h.Level1] {
			okp = okp || p[0] == h.Prefix
		}
		if !okp {
			return h, invalid("choose what kind of accounts go under %s", h.Name)
		}
	}
	top := 0
	for _, c := range hs.children(h.Parent, h.Level1) {
		top = max(top, c.Order)
	}
	n, err := o.next(ctx, tenant, "account-head", actor)
	if err != nil {
		return h, err
	}
	h.ID, h.Order, h.Default, h.Clients, h.Removed, h.By = fmt.Sprintf("H%04d", n), top+10, false, false, false, actor
	if err := o.putHead(ctx, tenant, actor, h); err != nil {
		return h, err
	}
	o.indexChanged(tenant)
	o.log(ctx, tenant, actor, "account_head.added", h.ID, map[string]any{"name": h.Name, "parent": h.Parent, "level1": h.Level1})
	return h, nil
}

func (o *Ops) putHead(ctx context.Context, tenant, actor string, h AccountHead) error {
	h.Level, h.Path, h.Accounts, h.Fixed = 0, "", 0, false
	o.indexChanged(tenant)
	return docs.Upsert(ctx, o.Docs, tenant, cHead, h.ID, h, actor)
}

// RemoveHead takes away a head nothing uses: no accounts, no sub-heads,
// no move waiting to put an account there, and not the default home of
// a system account.
func (o *Ops) RemoveHead(ctx context.Context, tenant, actor, id string) error {
	if err := o.require(ctx, tenant, actor, "Changing the chart of accounts", RoleChecker); err != nil {
		return err
	}
	hs := o.heads(ctx, tenant)
	h, ok := hs.get(id)
	if !ok {
		return fmt.Errorf("%w: head %s", ErrNotFound, id)
	}
	if h.Fixed {
		return invalid("%s is where Ledgerline's own accounts go; rename it instead", h.Name)
	}
	if len(hs.children(id, h.Level1)) > 0 {
		return invalid("%s has sub-heads; remove or move them first", h.Name)
	}
	m := o.accountMaster(ctx, tenant)
	for code, a := range m {
		if a.Head == id && a.Status != accountRejected {
			return invalid("%s has accounts (%s); move them to another head first", h.Name, accountName(code, m))
		}
	}
	mv, _ := docs.List[AccountMove](ctx, o.Docs, tenant, cAccountMove, "")
	for _, x := range mv {
		if x.Status == "Pending" && x.To == id {
			return invalid("a move of %s to %s is waiting for approval", x.AccountName, h.Name)
		}
	}
	h.Removed, h.By = true, actor
	if err := o.putHead(ctx, tenant, actor, h); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "account_head.removed", id, map[string]any{"name": h.Name})
	return nil
}

// ReorderHead moves a head up or down among the heads beside it.
func (o *Ops) ReorderHead(ctx context.Context, tenant, actor, id, dir string) error {
	if err := o.require(ctx, tenant, actor, "Changing the chart of accounts", RoleChecker); err != nil {
		return err
	}
	hs := o.heads(ctx, tenant)
	h, ok := hs.get(id)
	if !ok {
		return fmt.Errorf("%w: head %s", ErrNotFound, id)
	}
	sib := hs.children(h.Parent, h.Level1)
	i := 0
	for j, s := range sib {
		if s.ID == id {
			i = j
		}
	}
	j := i - 1
	if dir == "down" {
		j = i + 1
	} else if dir != "up" {
		return invalid("dir must be up or down")
	}
	if j < 0 || j >= len(sib) {
		return nil
	}
	// Renumber the siblings so each has its own place, then swap.
	for k, s := range sib {
		s.Order = (k + 1) * 10
	}
	sib[i].Order, sib[j].Order = sib[j].Order, sib[i].Order
	for _, s := range sib {
		if err := o.putHead(ctx, tenant, actor, *s); err != nil {
			return err
		}
	}
	o.log(ctx, tenant, actor, "account_head.reordered", id, map[string]any{"dir": dir})
	return nil
}

// ---- accounts without codes ----

// checkAccountNames refuses a name or alias another account already has
// (names are unique in the firm, ignoring case, as in Tally; an alias may
// not be another account's name or alias either).
func checkAccountNames(m map[string]Account, code, name, alias string) error {
	n, al := acctNorm(name), acctNorm(alias)
	if al != "" && al == n {
		al = ""
	}
	for c, a := range m {
		if c == code || a.Status == accountRejected {
			continue
		}
		on, oa := acctNorm(accountName(c, m)), acctNorm(a.Alias)
		if on == n || oa != "" && oa == n {
			return invalid("there is already an account called %s", accountName(c, m))
		}
		if al != "" && (on == al || oa == al) {
			return invalid("%s is already used by the account %s", alias, accountName(c, m))
		}
	}
	for c, sn := range systemNames {
		if _, saved := m[c]; saved || c == code {
			continue
		}
		if acctNorm(sn) == n {
			return invalid("there is already an account called %s", sn)
		}
		if al != "" && acctNorm(sn) == al {
			return invalid("%s is already the name of the account %s", alias, sn)
		}
	}
	return nil
}

// newCode gives a new account its code: the head's prefix and the next
// account number, e.g. EXPENSE:A0042.
func (o *Ops) newCode(ctx context.Context, tenant, actor, prefix string, m map[string]Account) (string, error) {
	for range 1000 {
		n, err := o.next(ctx, tenant, "account-code", actor)
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("%s:A%04d", prefix, n)
		if _, used := m[code]; !used && systemNames[code] == "" {
			return code, nil
		}
	}
	return "", fmt.Errorf("%w: no free account code", ErrState)
}

// headForNew checks the head a new account goes under.
func (hs headSet) headForNew(id string) (AccountHead, error) {
	h, ok := hs.get(id)
	if !ok {
		return h, invalid("choose the head the account goes under")
	}
	if h.Prefix == "" {
		if h.Clients {
			return h, invalid("client accounts are opened with the client; %s has no other accounts", h.Name)
		}
		return h, invalid("accounts under %s are kept by Ledgerline", h.Name)
	}
	return h, nil
}

// NewAccount is an account made while entering a voucher, by anyone who
// may enter vouchers. It waits for a checker: a voucher that uses it
// cannot post until the checker approves the account, which they do when
// they approve the voucher.
func (o *Ops) NewAccount(ctx context.Context, tenant, actor string, a Account) (Account, error) {
	if err := o.require(ctx, tenant, actor, "Entering vouchers", RoleMaker, RoleChecker); err != nil {
		return a, err
	}
	a.Name, a.Alias = strings.Join(strings.Fields(a.Name), " "), strings.Join(strings.Fields(a.Alias), " ")
	if a.Name == "" {
		return a, invalid("give the account a name")
	}
	if utf8.RuneCountInString(a.Alias) > 24 {
		return a, invalid("keep the alias short (24 letters at most)")
	}
	hs := o.heads(ctx, tenant)
	h, err := hs.headForNew(a.Head)
	if err != nil {
		return a, err
	}
	m := o.accountMaster(ctx, tenant)
	if err := checkAccountNames(m, "", a.Name, a.Alias); err != nil {
		return a, err
	}
	code, err := o.newCode(ctx, tenant, actor, h.Prefix, m)
	if err != nil {
		return a, err
	}
	n := Account{Code: code, Name: a.Name, Alias: a.Alias, Head: h.ID, Status: accountPending, CreatedBy: actor, Created: o.now().Format(time.RFC3339), AllowedUsers: []string{}}
	n.Type, n.Group = accountType(code), h.Name
	if _, err := docs.Put(ctx, o.Docs, tenant, cAccount, code, n, 0, actor); err != nil {
		return n, err
	}
	o.indexChanged(tenant)
	o.log(ctx, tenant, actor, "account.requested", code, map[string]any{"name": n.Name, "head": h.Name, "alias": n.Alias})
	return o.decorate(hs, n, m), nil
}

// DecideAccount approves or rejects a new account waiting for a checker.
func (o *Ops) DecideAccount(ctx context.Context, tenant, actor, code, action, reason string) (Account, error) {
	if action == "reject" {
		if err := o.require(ctx, tenant, actor, "Rejecting new accounts", RoleChecker); err != nil {
			return Account{}, err
		}
	} else if err := o.require(ctx, tenant, actor, "Approving new accounts", RoleChecker); err != nil {
		return Account{}, err
	}
	return o.decideAccount(ctx, tenant, actor, code, action, reason)
}

func (o *Ops) decideAccount(ctx context.Context, tenant, actor, code, action, reason string) (Account, error) {
	a, _, err := docs.Get[Account](ctx, o.Docs, tenant, cAccount, code)
	if err != nil {
		return a, err
	}
	if a.Status != accountPending {
		return a, fmt.Errorf("%w: %s is not waiting for approval", ErrState, a.Name)
	}
	if a.CreatedBy == actor {
		return a, fmt.Errorf("%w: you made the account %s; another checker approves it", ErrForbidden, a.Name)
	}
	switch action {
	case "approve":
		a.Status, a.Reason = accountActive, ""
	case "reject":
		if strings.TrimSpace(reason) == "" {
			return a, invalid("give the reason for rejecting the account %s", a.Name)
		}
		a.Status, a.Reason = accountRejected, strings.TrimSpace(reason)
	default:
		return a, invalid("action must be approve or reject")
	}
	a.Checker = actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cAccount, code, a, actor); err != nil {
		return a, err
	}
	o.indexChanged(tenant)
	o.log(ctx, tenant, actor, "account."+action+"d", code, map[string]any{"name": a.Name, "reason": a.Reason})
	return a, nil
}

// decorate fills in the head names an account is shown with.
func (o *Ops) decorate(hs headSet, a Account, m map[string]Account) Account {
	if ledger.IsClientAccount(a.Code) {
		return a
	}
	a.Head = hs.headOf(a.Code, m)
	a.HeadName, a.SubHead = hs.names(a.Head)
	if h, ok := hs.byID[a.Head]; ok {
		a.Level1 = h.Level1
	} else if t, err := ledger.TypeOf(a.Code); err == nil {
		a.Level1 = level1Of(t)
	}
	a.Group = a.HeadName
	if a.Status == "" {
		a.Status = accountActive
	}
	return a
}

// ---- moving an account to another head (two people) ----

// AccountMove is a request to show an account under another head. One
// person asks, another approves; both are in the audit trail. A later
// screen mapping an old system's chart can raise these in bulk.
type AccountMove struct {
	ID          string `json:"id"`
	Account     string `json:"account"`
	AccountName string `json:"account_name"`
	From        string `json:"from"`
	FromName    string `json:"from_name"`
	To          string `json:"to"`
	ToName      string `json:"to_name"`
	Reason      string `json:"reason"`
	Status      string `json:"status"` // Pending, Approved, Rejected
	By          string `json:"by"`
	At          string `json:"at"`
	Checker     string `json:"checker,omitempty"`
	DecidedAt   string `json:"decided_at,omitempty"`
	Note        string `json:"note,omitempty"`
}

// AccountMoves lists move requests, newest first.
func (o *Ops) AccountMoves(ctx context.Context, tenant string) ([]AccountMove, error) {
	ms, err := docs.List[AccountMove](ctx, o.Docs, tenant, cAccountMove, "")
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].ID > ms[j].ID })
	if ms == nil {
		ms = []AccountMove{}
	}
	return ms, err
}

// RequestAccountMove asks to show an account under another head of the
// same kind (an expense under another expenditure head, and so on: the
// account's ledger type does not change).
func (o *Ops) RequestAccountMove(ctx context.Context, tenant, actor, code, to, reason string) (AccountMove, error) {
	if err := o.require(ctx, tenant, actor, "Moving an account to another head", RoleMaker, RoleChecker); err != nil {
		return AccountMove{}, err
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	t, err := ledger.TypeOf(code)
	if err != nil {
		return AccountMove{}, invalid("%v", err)
	}
	if ledger.IsClientAccount(code) {
		return AccountMove{}, invalid("client accounts stay under Clients")
	}
	if !o.AccountVisible(ctx, tenant, actor)(code) {
		return AccountMove{}, fmt.Errorf("%w: restricted account", ErrForbidden)
	}
	hs := o.heads(ctx, tenant)
	m := o.accountMaster(ctx, tenant)
	if a, ok := m[code]; ok && a.Status == accountRejected {
		return AccountMove{}, invalid("%s was rejected", a.Name)
	}
	h, ok := hs.get(to)
	if !ok || h.Clients {
		return AccountMove{}, invalid("choose the head to move it to")
	}
	if h.Level1 != level1Of(t) {
		return AccountMove{}, invalid("%s is under %s; it can move only to another head under %s", accountName(code, m), level1Of(t), level1Of(t))
	}
	from := hs.headOf(code, m)
	if from == to {
		return AccountMove{}, invalid("%s is already under %s", accountName(code, m), h.Name)
	}
	ms, _ := docs.List[AccountMove](ctx, o.Docs, tenant, cAccountMove, "")
	for _, x := range ms {
		if x.Account == code && x.Status == "Pending" {
			return AccountMove{}, fmt.Errorf("%w: a move of %s is already waiting for approval (%s)", ErrState, x.AccountName, x.ID)
		}
	}
	if strings.TrimSpace(reason) == "" {
		return AccountMove{}, invalid("say why the account moves")
	}
	n, err := o.next(ctx, tenant, "account-move", actor)
	if err != nil {
		return AccountMove{}, err
	}
	fh, fs := hs.names(from)
	th, ts := hs.names(to)
	mv := AccountMove{ID: fmt.Sprintf("AM-%05d", n), Account: code, AccountName: accountName(code, m), From: from, FromName: joinHead(fh, fs), To: to, ToName: joinHead(th, ts),
		Reason: strings.TrimSpace(reason), Status: "Pending", By: actor, At: o.now().Format(time.RFC3339)}
	if _, err := docs.Put(ctx, o.Docs, tenant, cAccountMove, mv.ID, mv, 0, actor); err != nil {
		return mv, err
	}
	o.log(ctx, tenant, actor, "account.move_requested", code, map[string]any{"id": mv.ID, "from": mv.FromName, "to": mv.ToName, "reason": mv.Reason})
	return mv, nil
}

func joinHead(h, s string) string {
	if s == "" {
		return h
	}
	return h + " › " + s
}

// DecideAccountMove approves (the account moves) or rejects a move.
func (o *Ops) DecideAccountMove(ctx context.Context, tenant, actor, id, action, note string) (AccountMove, error) {
	if err := o.require(ctx, tenant, actor, "Approving an account move", RoleChecker); err != nil {
		return AccountMove{}, err
	}
	mv, _, err := docs.Get[AccountMove](ctx, o.Docs, tenant, cAccountMove, id)
	if err != nil {
		return mv, err
	}
	if mv.Status != "Pending" {
		return mv, fmt.Errorf("%w: %s is %s", ErrState, id, mv.Status)
	}
	if mv.By == actor {
		return mv, fmt.Errorf("%w: the person who asked for a move cannot approve it", ErrForbidden)
	}
	switch action {
	case "approve":
		hs := o.heads(ctx, tenant)
		if _, ok := hs.get(mv.To); !ok {
			return mv, invalid("the head %s is no longer there", mv.ToName)
		}
		m := o.accountMaster(ctx, tenant)
		a, ok := m[mv.Account]
		if !ok {
			// A system account gets a record of its own to carry the head.
			a = Account{Code: mv.Account, Name: accountName(mv.Account, m), System: true, AllowedUsers: []string{}, Type: accountType(mv.Account)}
		}
		a.Head = mv.To
		if err := docs.Upsert(ctx, o.Docs, tenant, cAccount, mv.Account, a, actor); err != nil {
			return mv, err
		}
		mv.Status = "Approved"
	case "reject":
		if strings.TrimSpace(note) == "" {
			return mv, invalid("give the reason for rejecting")
		}
		mv.Status = "Rejected"
	default:
		return mv, invalid("action must be approve or reject")
	}
	mv.Checker, mv.DecidedAt, mv.Note = actor, o.now().Format(time.RFC3339), strings.TrimSpace(note)
	if err := docs.Upsert(ctx, o.Docs, tenant, cAccountMove, id, mv, actor); err != nil {
		return mv, err
	}
	o.indexChanged(tenant)
	ev := "account.moved"
	if action == "reject" {
		ev = "account.move_rejected"
	}
	o.log(ctx, tenant, actor, ev, mv.Account, map[string]any{"id": id, "from": mv.FromName, "to": mv.ToName, "note": mv.Note})
	return mv, nil
}

// ---- the account search box ----
//
// One search serves every account field: firm accounts and client
// ledgers, matched by name, alias and (clients) UCC. The index is built
// once per firm and date and kept for a short while, so typing in a firm
// with thousands of clients stays quick; any change to the chart or a
// posted voucher drops it.

// AccountHit is one line of the search dropdown.
type AccountHit struct {
	Code    string       `json:"code"`
	Name    string       `json:"name"`
	Alias   string       `json:"alias,omitempty"`
	UCC     string       `json:"ucc,omitempty"`
	Head    string       `json:"head"`
	Sub     string       `json:"sub,omitempty"`
	HeadID  string       `json:"head_id,omitempty"`
	Level1  string       `json:"level1"`
	Balance money.Amount `json:"balance"`
	Client  bool         `json:"client,omitempty"`
	Status  string       `json:"status,omitempty"` // Pending for an account waiting for a checker
	// Where the typed text matched (name, alias or ucc) and at which
	// letter, for highlighting.
	Match string `json:"match,omitempty"`
	At    int    `json:"at"`
	Len   int    `json:"len"`
	rank  int
}

// AccountSearch is what the box asks for.
type AccountSearch struct {
	Q        string
	Date     string
	Limit    int
	Prefixes []string // only these ledger prefixes, e.g. BANK, CASH
	Clients  string   // "" both, "no" firm accounts only, "only" clients only
	Side     string   // with clients: "dr" debit balances, "cr" credit balances
}

// AccountSearchResult is a page of hits.
type AccountSearchResult struct {
	Hits  []AccountHit `json:"hits"`
	Total int          `json:"total"`
	More  bool         `json:"more"`
}

type idxRow struct {
	AccountHit
	name, alias, ucc string
	restricted       bool
}

type acctIndex struct {
	at   time.Time
	rows []idxRow
}

const acctIndexTTL = 30 * time.Second

// indexChanged drops a firm's cached search index.
func (o *Ops) indexChanged(tenant string) {
	o.acctIdx.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), tenant+"|") {
			o.acctIdx.Delete(k)
		}
		return true
	})
}

func (o *Ops) accountIndex(ctx context.Context, tenant, date string) (*acctIndex, error) {
	key := tenant + "|" + date
	if v, ok := o.acctIdx.Load(key); ok {
		ix := v.(*acctIndex)
		if d := o.now().Sub(ix.at); d >= 0 && d < acctIndexTTL {
			return ix, nil
		}
	}
	bal, err := o.App.Ledger.Balances(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	m := o.accountMaster(ctx, tenant)
	hs := o.heads(ctx, tenant)
	ix := &acctIndex{at: o.now()}
	add := func(h AccountHit, restricted bool) {
		ix.rows = append(ix.rows, idxRow{AccountHit: h, name: strings.ToLower(h.Name), alias: strings.ToLower(h.Alias), ucc: strings.ToLower(h.UCC), restricted: restricted})
	}
	for _, code := range chartCodes(m, bal) {
		a := o.decorate(hs, Account{Code: code, Name: accountName(code, m), Alias: m[code].Alias, Status: m[code].Status}, m)
		st := ""
		if a.Status == accountPending {
			st = accountPending
		}
		add(AccountHit{Code: code, Name: a.Name, Alias: a.Alias, Head: a.HeadName, Sub: a.SubHead, HeadID: a.Head, Level1: a.Level1, Balance: bal[code], Status: st}, m[code].Restricted)
	}
	cs, _ := o.ClientMap(ctx, tenant)
	sep := o.sepCom(ctx, tenant)
	seen := map[string]bool{}
	client := func(code, ucc, name string) {
		if seen[code] {
			return
		}
		seen[code] = true
		add(AccountHit{Code: code, Name: name, UCC: ucc, Head: "Clients", Level1: L1Liabilities, Balance: bal[code], Client: true}, false)
	}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		name := c.Name
		if name == "" {
			name = u
		}
		client(ledger.ClientAccount(u), u, name)
		if sep {
			client(ledger.ClientCommodityAccount(u), u, name+" · Commodity ledger")
		}
	}
	for code := range bal {
		// Ledgers of clients no longer on the client list (closed,
		// migrated) still have balances and can be adjusted.
		if u, kind, ok := ledger.ClientCode(code); ok && (kind == ledger.LedgerEquity || kind == ledger.LedgerCommodity && sep) {
			client(code, u, u)
		}
	}
	o.acctIdx.Store(key, ix)
	return ix, nil
}

// matchAt finds q in s: rank 0 when s starts with it, 1 when a word of s
// does, 2 when it is inside a word; -1 when it is not there. at is the
// letter (not byte) where it matched.
func matchAt(s, q string) (rank, at int) {
	i := strings.Index(s, q)
	if i < 0 {
		return -1, 0
	}
	if i == 0 {
		return 0, 0
	}
	first := i
	for j := i; j >= 0; {
		r, _ := utf8.DecodeLastRuneInString(s[:j])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return 1, utf8.RuneCountInString(s[:j])
		}
		k := strings.Index(s[j+1:], q)
		if k < 0 {
			break
		}
		j += 1 + k
	}
	return 2, utf8.RuneCountInString(s[:first])
}

// SearchAccounts finds accounts for the search box: names that start
// with the text first, then names with a word that starts with it, then
// the rest; aliases and client codes count as well. Restricted accounts
// the person may not see are left out.
func (o *Ops) SearchAccounts(ctx context.Context, tenant, actor string, s AccountSearch) (AccountSearchResult, error) {
	if err := o.require(ctx, tenant, actor, "Searching accounts", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return AccountSearchResult{}, err
	}
	if s.Date == "" {
		s.Date = o.now().Format("2006-01-02")
	}
	if s.Limit <= 0 || s.Limit > 200 {
		s.Limit = 15
	}
	ix, err := o.accountIndex(ctx, tenant, s.Date)
	if err != nil {
		return AccountSearchResult{}, err
	}
	q := strings.ToLower(strings.Join(strings.Fields(s.Q), " "))
	var vis func(string) bool
	pre := map[string]bool{}
	for _, p := range s.Prefixes {
		if p = strings.ToUpper(strings.TrimSpace(p)); p != "" {
			pre[p] = true
		}
	}
	var hits []AccountHit
	for _, r := range ix.rows {
		if s.Clients == "no" && r.Client || s.Clients == "only" && !r.Client {
			continue
		}
		if len(pre) > 0 && !r.Client {
			p, _, _ := strings.Cut(r.Code, ":")
			if !pre[p] {
				continue
			}
		} else if len(pre) > 0 && r.Client && s.Clients != "only" {
			continue
		}
		if s.Side == "dr" && r.Balance <= 0 || s.Side == "cr" && r.Balance >= 0 {
			continue
		}
		h := r.AccountHit
		if q != "" {
			best, at, field := -1, 0, ""
			try := func(f, v string) {
				if v == "" {
					return
				}
				rk, a := matchAt(v, q)
				if rk < 0 {
					return
				}
				if f != "name" && rk == 0 && v != q {
					rk = 1 // an alias or UCC that starts with the text ranks with word starts; an exact one with names
				}
				if best < 0 || rk < best {
					best, at, field = rk, a, f
				}
			}
			try("name", r.name)
			try("alias", r.alias)
			try("ucc", r.ucc)
			if best < 0 {
				continue
			}
			h.rank, h.At, h.Len, h.Match = best, at, utf8.RuneCountInString(q), field
		}
		if r.restricted {
			if vis == nil {
				vis = o.AccountVisible(ctx, tenant, actor)
			}
			if !vis(r.Code) {
				continue
			}
		}
		hits = append(hits, h)
	}
	abs := func(a money.Amount) money.Amount {
		if a < 0 {
			return -a
		}
		return a
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if q == "" && s.Side != "" && abs(a.Balance) != abs(b.Balance) {
			return abs(a.Balance) > abs(b.Balance)
		}
		if a.Client != b.Client {
			return !a.Client // the firm's own accounts before clients
		}
		return a.Name < b.Name
	})
	res := AccountSearchResult{Total: len(hits), Hits: hits}
	if len(hits) > s.Limit {
		res.Hits, res.More = hits[:s.Limit], true
	}
	if res.Hits == nil {
		res.Hits = []AccountHit{}
	}
	return res, nil
}

// ---- trial balance by heads ----

// TBLine is an account on the trial balance.
type TBLine struct {
	Code   string       `json:"code"`
	Name   string       `json:"name"`
	Debit  money.Amount `json:"debit"`
	Credit money.Amount `json:"credit"`
}

// TBNode is a head on the trial balance with its sub-heads and accounts.
type TBNode struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Level    int          `json:"level"`
	Debit    money.Amount `json:"debit"`
	Credit   money.Amount `json:"credit"`
	Heads    []TBNode     `json:"heads"`
	Accounts []TBLine     `json:"accounts"`
	// Clients is the number of client ledgers on a client head; they are
	// listed through the account search (side dr or cr).
	Clients int `json:"clients,omitempty"`
}

// HeadTrialBalance is the trial balance arranged by heads.
type HeadTrialBalance struct {
	AsOf        string       `json:"as_of"`
	Heads       []TBNode     `json:"heads"`
	TotalDebit  money.Amount `json:"total_debit"`
	TotalCredit money.Amount `json:"total_credit"`
	Balanced    bool         `json:"balanced"`
}

// TrialBalanceByHeads is the trial balance from level 1 down to accounts.
// Each account's balance is in the debit or the credit column, as on the
// ledger's own trial balance, so the totals are the same; each client
// ledger goes under Clients with debit or with credit balances by its
// sign.
func (o *Ops) TrialBalanceByHeads(ctx context.Context, tenant, actor, asOf string) (HeadTrialBalance, error) {
	tb, err := o.App.Ledger.TrialBalance(ctx, tenant, asOf)
	if err != nil {
		return HeadTrialBalance{}, err
	}
	m := o.accountMaster(ctx, tenant)
	hs := o.heads(ctx, tenant)
	vis := o.AccountVisible(ctx, tenant, actor)
	lines := map[string][]TBLine{}
	hidden := map[string]*TBLine{}
	nClients := map[string]int{}
	cl := map[string]*TBLine{}
	for _, r := range tb.Rows {
		if ledger.IsClientAccount(r.Account) {
			h := HeadClientsCr
			if r.Debit > 0 {
				h = HeadClientsDr
			}
			if cl[h] == nil {
				cl[h] = &TBLine{Code: "CLIENT:" + map[string]string{HeadClientsDr: "DR", HeadClientsCr: "CR"}[h], Name: "Client ledgers"}
			}
			cl[h].Debit += r.Debit
			cl[h].Credit += r.Credit
			nClients[h]++
			continue
		}
		h := hs.headOf(r.Account, m)
		if !vis(r.Account) {
			if hidden[h] == nil {
				hidden[h] = &TBLine{Name: "Restricted accounts"}
			}
			hidden[h].Debit += r.Debit
			hidden[h].Credit += r.Credit
			continue
		}
		lines[h] = append(lines[h], TBLine{Code: r.Account, Name: accountName(r.Account, m), Debit: r.Debit, Credit: r.Credit})
	}
	for h, l := range hidden {
		lines[h] = append(lines[h], *l)
	}
	for h, l := range cl {
		lines[h] = append(lines[h], *l)
	}
	var build func(h *AccountHead) TBNode
	build = func(h *AccountHead) TBNode {
		n := TBNode{ID: h.ID, Name: h.Name, Level: h.Level, Heads: []TBNode{}, Accounts: lines[h.ID], Clients: nClients[h.ID]}
		if n.Accounts == nil {
			n.Accounts = []TBLine{}
		}
		sort.SliceStable(n.Accounts, func(i, j int) bool { return n.Accounts[i].Name < n.Accounts[j].Name })
		for _, a := range n.Accounts {
			n.Debit += a.Debit
			n.Credit += a.Credit
		}
		for _, c := range hs.children(h.ID, h.Level1) {
			cn := build(c)
			if cn.Debit == 0 && cn.Credit == 0 {
				continue
			}
			n.Heads = append(n.Heads, cn)
			n.Debit += cn.Debit
			n.Credit += cn.Credit
		}
		return n
	}
	out := HeadTrialBalance{AsOf: asOf, Heads: []TBNode{}}
	placed := map[string]bool{}
	for _, l1 := range Level1Heads {
		top := TBNode{ID: l1, Name: l1, Level: 1, Heads: []TBNode{}, Accounts: []TBLine{}}
		for _, h := range hs.children("", l1) {
			n := build(h)
			markPlaced(n, placed)
			if n.Debit == 0 && n.Credit == 0 {
				continue
			}
			top.Heads = append(top.Heads, n)
			top.Debit += n.Debit
			top.Credit += n.Credit
		}
		out.Heads = append(out.Heads, top)
	}
	// An account whose head was removed (it cannot be, while used, but a
	// stored head may be missing) is shown under its level-1 head, so the
	// totals always agree with the ledger.
	for h, ls := range lines {
		if placed[h] {
			continue
		}
		for _, l := range ls {
			l1 := L1Liabilities
			if t, err := ledger.TypeOf(l.Code); err == nil {
				l1 = level1Of(t)
			}
			for i := range out.Heads {
				if out.Heads[i].ID == l1 {
					out.Heads[i].Accounts = append(out.Heads[i].Accounts, l)
					out.Heads[i].Debit += l.Debit
					out.Heads[i].Credit += l.Credit
				}
			}
		}
	}
	for _, t := range out.Heads {
		out.TotalDebit += t.Debit
		out.TotalCredit += t.Credit
	}
	out.Balanced = out.TotalDebit == out.TotalCredit
	return out, nil
}

func markPlaced(n TBNode, placed map[string]bool) {
	placed[n.ID] = true
	for _, c := range n.Heads {
		markPlaced(c, placed)
	}
}
