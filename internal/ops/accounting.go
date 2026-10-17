package ops

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- the firm's own books: chart of accounts, vouchers, statements ----
//
// Client money flows through the operations modules. Everything else the
// firm pays and receives (rent, salaries, exchange and depository bills,
// vendor payments, capital) is entered as vouchers: payment, receipt,
// journal and contra, each made by one person and approved by another.
// Accounts can be restricted so only named users see them (salaries, for
// example). Profit and loss and the balance sheet are built from the same
// journal, for the whole firm or one branch.

const (
	cAccount = "account"
	cVoucher = "voucher"
	// Input GST paid on the firm's purchases (input tax credit).
	InputIGST = "RECEIVABLE:GST_INPUT:IGST"
	InputCGST = "RECEIVABLE:GST_INPUT:CGST"
	InputSGST = "RECEIVABLE:GST_INPUT:SGST"
)

// Account is an entry in the chart of accounts.
type Account struct {
	Code         string       `json:"code"`
	Name         string       `json:"name"`
	Group        string       `json:"group"`
	Type         string       `json:"type"`
	Restricted   bool         `json:"restricted"`
	AllowedUsers []string     `json:"allowed_users"`
	System       bool         `json:"system"`
	Balance      money.Amount `json:"balance"` // debit positive, as of the requested date
}

// Groups by account-code prefix, in balance-sheet order.
var accountGroups = map[string]string{
	"BANK": "Bank balances", "CASH": "Cash", "CC": "Clearing corporations", "RECEIVABLE": "Receivables and advances", "ASSET": "Fixed and other assets",
	"CLIENT": "Clients", "PAYABLE": "Duties, taxes and payables", "AP": "Authorised persons", "LOAN": "Loans and borrowings", "SUSPENSE": "Suspense",
	"EQUITY": "Capital and reserves", "INCOME": "Income", "EXPENSE": "Expenses",
}

var systemNames = map[string]string{
	ledger.BankClient: "Client bank account (segregated)", ledger.BankOwn: "Own bank account", ledger.IncomeBrokerage: "Brokerage",
	ledger.PayableSTT: "STT / CTT payable", ledger.PayableStamp: "Stamp duty payable", ledger.PayableSEBIFee: "SEBI turnover fee payable",
	ledger.PayableGST: "GST payable (output tax)", ledger.SuspenseBank: "Bank suspense (unidentified credits)", ledger.OpeningEquity: "Opening balance equity",
	ledger.CCCollateral: "Cash collateral with clearing corporation", ledger.PayablePenalty: "Margin penalty payable", IncomeInterest: "Interest and delayed payment charges",
	IncomeCharges: "Other charges recovered", ReceivableGateway: "Payment gateway receivable", InputIGST: "Input IGST", InputCGST: "Input CGST", InputSGST: "Input SGST",
	"EXPENSE:GATEWAY_FEES": "Payment gateway fees",
}

func accountType(code string) string {
	t, err := ledger.TypeOf(code)
	if err != nil {
		return ""
	}
	return string(t)
}

func accountGroup(code string) string {
	p, _, _ := strings.Cut(code, ":")
	return accountGroups[p]
}

// accountMaster returns the saved accounts by code.
func (o *Ops) accountMaster(ctx context.Context, tenant string) map[string]Account {
	as, _ := docs.List[Account](ctx, o.Docs, tenant, cAccount, "")
	m := map[string]Account{}
	for _, a := range as {
		m[a.Code] = a
	}
	return m
}

// AccountVisible returns a check for whether actor may see an account.
func (o *Ops) AccountVisible(ctx context.Context, tenant, actor string) func(string) bool {
	m := o.accountMaster(ctx, tenant)
	role, _ := o.RoleOf(ctx, tenant, actor)
	return func(code string) bool {
		a, ok := m[code]
		if !ok || !a.Restricted || role == RoleAdmin {
			return true
		}
		for _, u := range a.AllowedUsers {
			if u == actor {
				return true
			}
		}
		return false
	}
}

// Accounts is the chart of accounts with balances as of a date: every
// saved account and every account the journal has used, clients folded
// into one line.
func (o *Ops) Accounts(ctx context.Context, tenant, actor, asOf string) ([]Account, error) {
	bal, err := o.App.Ledger.Balances(ctx, tenant, asOf)
	if err != nil {
		return nil, err
	}
	m := o.accountMaster(ctx, tenant)
	seen := map[string]bool{}
	var out []Account
	vis := o.AccountVisible(ctx, tenant, actor)
	var clients money.Amount
	nClients := 0
	add := func(code string) {
		if seen[code] {
			return
		}
		seen[code] = true
		a, ok := m[code]
		if !ok {
			a = Account{Code: code, Name: systemNames[code], System: true}
			if a.Name == "" {
				_, q, _ := strings.Cut(code, ":")
				a.Name = strings.ReplaceAll(strings.Title(strings.ToLower(strings.ReplaceAll(q, "_", " "))), ":", " · ") //nolint:staticcheck
			}
		}
		a.Type, a.Group = accountType(code), accountGroup(code)
		if a.AllowedUsers == nil {
			a.AllowedUsers = []string{}
		}
		if !vis(code) {
			return
		}
		a.Balance = bal[code]
		out = append(out, a)
	}
	for _, code := range sortedKeys(m) {
		add(code)
	}
	for _, code := range sortedKeys(bal) {
		if strings.HasPrefix(code, "CLIENT:") {
			clients += bal[code]
			nClients++
			continue
		}
		add(code)
	}
	out = append(out, Account{Code: "CLIENT:*", Name: fmt.Sprintf("Client ledgers (%d)", nClients), Group: accountGroups["CLIENT"], Type: string(ledger.Liability), System: true, Balance: clients, AllowedUsers: []string{}})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

// SaveAccount adds or changes an account in the chart of accounts.
func (o *Ops) SaveAccount(ctx context.Context, tenant, actor string, a Account) (Account, error) {
	if err := o.require(ctx, tenant, actor, "Changing the chart of accounts", RoleChecker); err != nil {
		return a, err
	}
	a.Code = strings.ToUpper(strings.TrimSpace(a.Code))
	if _, err := ledger.TypeOf(a.Code); err != nil {
		return a, invalid("%v; use a prefix such as EXPENSE:, INCOME:, BANK:, CASH:, ASSET:, LOAN:, PAYABLE:, RECEIVABLE: or EQUITY:", err)
	}
	if strings.HasPrefix(a.Code, "CLIENT:") || strings.HasPrefix(a.Code, "CC:") {
		return a, invalid("client and clearing accounts are created by the system")
	}
	if strings.TrimSpace(a.Name) == "" {
		return a, invalid("give the account a name")
	}
	a.Type, a.Group, a.System = accountType(a.Code), accountGroup(a.Code), false
	if a.AllowedUsers == nil {
		a.AllowedUsers = []string{}
	}
	if a.Restricted && len(a.AllowedUsers) == 0 {
		a.AllowedUsers = []string{actor}
	}
	a.Balance = 0
	if err := docs.Upsert(ctx, o.Docs, tenant, cAccount, a.Code, a, actor); err != nil {
		return a, err
	}
	o.log(ctx, tenant, actor, "account.saved", a.Code, map[string]any{"name": a.Name, "restricted": a.Restricted})
	return a, nil
}

// VoucherLine is one line of a voucher.
type VoucherLine struct {
	Account string       `json:"account"`
	Debit   money.Amount `json:"debit"`
	Credit  money.Amount `json:"credit"`
	Memo    string       `json:"memo,omitempty"`
}

// Voucher is a payment, receipt, journal or contra entry of the firm.
type Voucher struct {
	No        string        `json:"no"`
	Type      string        `json:"type"` // Payment, Receipt, Journal, Contra
	Date      string        `json:"date"`
	Branch    string        `json:"branch,omitempty"`
	Party     string        `json:"party,omitempty"`
	Ref       string        `json:"ref,omitempty"` // cheque number or UTR
	Narration string        `json:"narration"`
	Lines     []VoucherLine `json:"lines"`
	Total     money.Amount  `json:"total"`
	Status    string        `json:"status"` // Pending, Posted, Rejected, Cancelled
	Maker     string        `json:"maker"`
	Checker   string        `json:"checker,omitempty"`
	Entry     string        `json:"entry,omitempty"`
	Reason    string        `json:"reason,omitempty"`
}

var voucherSeries = map[string]string{"Payment": "PV", "Receipt": "RV", "Journal": "JV", "Contra": "CV"}

func isMoneyAccount(code string) bool {
	return strings.HasPrefix(code, "BANK:") || strings.HasPrefix(code, "CASH:")
}

// Vouchers lists vouchers, newest first, hiding restricted ones.
func (o *Ops) Vouchers(ctx context.Context, tenant, actor string) ([]Voucher, error) {
	vs, err := docs.List[Voucher](ctx, o.Docs, tenant, cVoucher, "")
	if err != nil {
		return nil, err
	}
	vis := o.AccountVisible(ctx, tenant, actor)
	out := []Voucher{}
	for _, v := range vs {
		ok := true
		for _, l := range v.Lines {
			ok = ok && vis(l.Account)
		}
		if ok {
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].No > out[j].No
	})
	return out, nil
}

// SaveVoucher validates and records a voucher for approval.
func (o *Ops) SaveVoucher(ctx context.Context, tenant, actor string, v Voucher) (Voucher, error) {
	if err := o.require(ctx, tenant, actor, "Entering vouchers", RoleMaker, RoleChecker); err != nil {
		return v, err
	}
	series, ok := voucherSeries[v.Type]
	if !ok {
		return v, invalid("voucher type must be Payment, Receipt, Journal or Contra")
	}
	if !ledger.ValidDate(v.Date) {
		return v, invalid("date must be YYYY-MM-DD")
	}
	if strings.TrimSpace(v.Narration) == "" {
		return v, invalid("give a narration")
	}
	if err := o.checkLock(ctx, tenant, v.Date); err != nil {
		return v, err
	}
	vis := o.AccountVisible(ctx, tenant, actor)
	var dr, cr money.Amount
	var lines []VoucherLine
	moneyDr, moneyCr := 0, 0
	for i, l := range v.Lines {
		l.Account = strings.ToUpper(strings.TrimSpace(l.Account))
		if l.Account == "" && l.Debit == 0 && l.Credit == 0 {
			continue
		}
		if _, err := ledger.TypeOf(l.Account); err != nil {
			return v, invalid("line %d: %v", i+1, err)
		}
		if (l.Debit > 0) == (l.Credit > 0) || l.Debit < 0 || l.Credit < 0 {
			return v, invalid("line %d: enter either a debit or a credit above zero", i+1)
		}
		if !vis(l.Account) {
			return v, fmt.Errorf("%w: you cannot post to %s", ErrForbidden, l.Account)
		}
		if strings.HasPrefix(l.Account, "CLIENT:") && v.Type != "Journal" {
			return v, invalid("client money moves through Receipts & payouts; use a journal voucher only for adjustments")
		}
		if isMoneyAccount(l.Account) {
			if l.Debit > 0 {
				moneyDr++
			} else {
				moneyCr++
			}
		}
		dr += l.Debit
		cr += l.Credit
		lines = append(lines, l)
	}
	if len(lines) < 2 || dr != cr {
		return v, invalid("a voucher needs at least two lines and equal debits and credits (debits %s, credits %s)", dr, cr)
	}
	switch v.Type {
	case "Payment":
		if moneyCr != 1 || moneyDr != 0 {
			return v, invalid("a payment credits one bank or cash account")
		}
	case "Receipt":
		if moneyDr != 1 || moneyCr != 0 {
			return v, invalid("a receipt debits one bank or cash account")
		}
	case "Contra":
		if moneyDr+moneyCr != len(lines) {
			return v, invalid("a contra voucher moves money only between bank and cash accounts")
		}
	}
	for _, l := range lines {
		// Client money leaves the client bank account only as client
		// payouts or to the clearing corporation; own funds may be added.
		if l.Account == ledger.BankClient && l.Credit > 0 {
			return v, invalid("money cannot be taken out of the client bank account by a voucher; use Receipts & payouts or upstreaming")
		}
	}
	n, err := o.next(ctx, tenant, "voucher-"+series+"-"+finYear(v.Date), actor)
	if err != nil {
		return v, err
	}
	v.No = fmt.Sprintf("%s/%s/%05d", series, finYear(v.Date), n)
	v.Lines, v.Total, v.Status, v.Maker, v.Checker, v.Entry = lines, dr, "Pending", actor, "", ""
	if _, err := docs.Put(ctx, o.Docs, tenant, cVoucher, strings.ReplaceAll(v.No, "/", "-"), v, 0, actor); err != nil {
		return v, err
	}
	o.log(ctx, tenant, actor, "voucher.entered", v.No, map[string]any{"type": v.Type, "total": v.Total.String()})
	return v, nil
}

// DecideVoucher posts (approve) or rejects a pending voucher, or cancels a
// posted one by reversing it.
func (o *Ops) DecideVoucher(ctx context.Context, tenant, actor, no, action, reason string) (Voucher, error) {
	if err := o.require(ctx, tenant, actor, "Approving vouchers", RoleChecker); err != nil {
		return Voucher{}, err
	}
	id := strings.ReplaceAll(no, "/", "-")
	v, _, err := docs.Get[Voucher](ctx, o.Docs, tenant, cVoucher, id)
	if err != nil {
		return v, err
	}
	if !o.AccountVisible(ctx, tenant, actor)(v.Lines[0].Account) {
		return v, fmt.Errorf("%w: restricted voucher", ErrForbidden)
	}
	switch action {
	case "approve":
		if v.Status != "Pending" {
			return v, fmt.Errorf("%w: %s is %s", ErrState, no, v.Status)
		}
		if v.Maker == actor {
			return v, fmt.Errorf("%w: the maker of a voucher cannot approve it", ErrForbidden)
		}
		ps := make([]ledger.Posting, len(v.Lines))
		for i, l := range v.Lines {
			ps[i] = ledger.Posting{Account: l.Account, Debit: l.Debit, Credit: l.Credit, Memo: l.Memo}
		}
		narr := v.Type + " voucher " + v.No + " · " + v.Narration
		if v.Party != "" {
			narr += " · " + v.Party
		}
		e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "VCH:" + v.No, ValueDate: v.Date, Narration: narr,
			Source: ledger.SourceRef{Kind: "VOUCHER", ID: v.No}, Postings: ps, CreatedBy: actor})
		if err != nil {
			return v, err
		}
		v.Status, v.Checker, v.Entry = "Posted", actor, e.ID
	case "reject":
		if v.Status != "Pending" {
			return v, fmt.Errorf("%w: %s is %s", ErrState, no, v.Status)
		}
		if strings.TrimSpace(reason) == "" {
			return v, invalid("give the reason for rejecting")
		}
		v.Status, v.Checker, v.Reason = "Rejected", actor, reason
	case "cancel":
		if v.Status != "Posted" {
			return v, fmt.Errorf("%w: only a posted voucher can be cancelled", ErrState)
		}
		if strings.TrimSpace(reason) == "" {
			return v, invalid("give the reason for cancelling")
		}
		if _, _, err := o.App.Ledger.Reverse(ctx, tenant, v.Entry, actor, "voucher "+v.No+" cancelled: "+reason, ""); err != nil {
			return v, err
		}
		v.Status, v.Reason = "Cancelled", reason
	default:
		return v, invalid("action must be approve, reject or cancel")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cVoucher, id, v, actor); err != nil {
		return v, err
	}
	o.log(ctx, tenant, actor, "voucher."+action, v.No, map[string]any{"total": v.Total.String(), "reason": reason})
	return v, nil
}

// ---- financial statements ----

// StatementLine is one account line of a financial statement.
type FinLine struct {
	Code   string       `json:"code"`
	Name   string       `json:"name"`
	Amount money.Amount `json:"amount"` // positive in the section's natural direction
}

// FinSection is a group of lines.
type FinSection struct {
	Title string       `json:"title"`
	Lines []FinLine    `json:"lines"`
	Total money.Amount `json:"total"`
}

// Financials are the profit and loss account for a period and the balance
// sheet at its end.
type Financials struct {
	From        string       `json:"from"`
	To          string       `json:"to"`
	Branch      string       `json:"branch,omitempty"`
	Branches    []string     `json:"branches"`
	Income      FinSection   `json:"income"`
	Expenses    FinSection   `json:"expenses"`
	Profit      money.Amount `json:"profit"`
	Assets      []FinSection `json:"assets"`
	Liabilities []FinSection `json:"liabilities"`
	TotalAssets money.Amount `json:"total_assets"`
	TotalLiab   money.Amount `json:"total_liabilities"`
	Balanced    bool         `json:"balanced"`
}

// Financials builds P&L for from..to and the balance sheet as at to. With
// a branch, the P&L counts only that branch's income and expenses: client
// income by the client's branch, vouchers by their branch.
func (o *Ops) Financials(ctx context.Context, tenant, actor, from, to, branch string) (Financials, error) {
	if !ledger.ValidDate(from) || !ledger.ValidDate(to) || from > to {
		return Financials{}, invalid("give a period: from on or before to")
	}
	f := Financials{From: from, To: to, Branch: branch, Branches: []string{}}
	cs, _ := o.ClientMap(ctx, tenant)
	vs, _ := docs.List[Voucher](ctx, o.Docs, tenant, cVoucher, "")
	vBranch := map[string]string{}
	bset := map[string]bool{"Head office": true}
	for _, v := range vs {
		vBranch[v.No] = v.Branch
		if v.Branch != "" {
			bset[v.Branch] = true
		}
	}
	for _, c := range cs {
		if c.Branch != "" {
			bset[c.Branch] = true
		}
	}
	f.Branches = sortedKeys(bset)
	names := map[string]string{}
	for _, a := range o.accountMaster(ctx, tenant) {
		names[a.Code] = a.Name
	}
	name := func(code string) string {
		if n := names[code]; n != "" {
			return n
		}
		if n := systemNames[code]; n != "" {
			return n
		}
		return code
	}
	vis := o.AccountVisible(ctx, tenant, actor)
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{From: from, To: to})
	if err != nil {
		return f, err
	}
	all, _ := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{})
	byID := map[string]ledger.Entry{}
	for _, e := range all {
		byID[e.ID] = e
	}
	entryBranch := func(e ledger.Entry) string {
		if e.ReversalOf != "" {
			if orig, ok := byID[e.ReversalOf]; ok {
				e = orig
			}
		}
		if e.Source.Kind == "VOUCHER" {
			if b := vBranch[e.Source.ID]; b != "" {
				return b
			}
			return "Head office"
		}
		for _, p := range e.Postings {
			if u, ok := strings.CutPrefix(p.Account, "CLIENT:"); ok {
				if b := cs[u].Branch; b != "" {
					return b
				}
			}
		}
		return "Head office"
	}
	inc, exp := map[string]money.Amount{}, map[string]money.Amount{}
	for _, e := range es {
		if branch != "" && entryBranch(e) != branch {
			continue
		}
		for _, p := range e.Postings {
			switch {
			case strings.HasPrefix(p.Account, "INCOME:"):
				inc[p.Account] += p.Credit - p.Debit
			case strings.HasPrefix(p.Account, "EXPENSE:"):
				exp[p.Account] += p.Debit - p.Credit
			}
		}
	}
	section := func(title string, m map[string]money.Amount) FinSection {
		s := FinSection{Title: title, Lines: []FinLine{}}
		for _, k := range sortedKeys(m) {
			if m[k] == 0 {
				continue
			}
			if !vis(k) {
				k2 := "Restricted accounts"
				s.Lines = append(s.Lines, FinLine{Code: "", Name: k2, Amount: m[k]})
			} else {
				s.Lines = append(s.Lines, FinLine{Code: k, Name: name(k), Amount: m[k]})
			}
			s.Total += m[k]
		}
		return s
	}
	f.Income, f.Expenses = section("Income", inc), section("Expenses", exp)
	f.Profit = f.Income.Total - f.Expenses.Total
	if branch != "" {
		return f, nil // a branch has no balance sheet of its own
	}
	bal, err := o.App.Ledger.Balances(ctx, tenant, to)
	if err != nil {
		return f, err
	}
	// Retained profit: all income less expenses up to `to`.
	var retained money.Amount
	asset, liab := map[string]map[string]money.Amount{}, map[string]map[string]money.Amount{}
	put := func(m map[string]map[string]money.Amount, g, k string, v money.Amount) {
		if m[g] == nil {
			m[g] = map[string]money.Amount{}
		}
		m[g][k] += v
	}
	var clientDr, clientCr money.Amount
	for k, v := range bal {
		t, _ := ledger.TypeOf(k)
		switch {
		case strings.HasPrefix(k, "CLIENT:"):
			if v > 0 {
				clientDr += v
			} else {
				clientCr += -v
			}
		case t == ledger.Income || t == ledger.Expense:
			retained += -v
		case t == ledger.Asset && v < 0:
			// An asset account in credit (an overdrawn bank, money owed to a
			// clearing corporation) is a liability at the balance-sheet date.
			put(liab, accountGroup(k)+" (owed)", k, -v)
		case t == ledger.Asset:
			put(asset, accountGroup(k), k, v)
		default:
			put(liab, accountGroup(k), k, -v)
		}
	}
	put(asset, "Clients (sundry debtors)", "CLIENT:DR", clientDr)
	put(liab, "Clients (credit balances)", "CLIENT:CR", clientCr)
	put(liab, "Capital and reserves", "PROFIT", retained)
	names["CLIENT:DR"], names["CLIENT:CR"], names["PROFIT"] = "Clients with debit balances", "Clients with credit balances", "Profit and loss account"
	build := func(m map[string]map[string]money.Amount) ([]FinSection, money.Amount) {
		var out []FinSection
		var tot money.Amount
		for _, g := range sortedKeys(m) {
			s := section(g, m[g])
			if len(s.Lines) == 0 {
				continue
			}
			out = append(out, s)
			tot += s.Total
		}
		return out, tot
	}
	f.Assets, f.TotalAssets = build(asset)
	f.Liabilities, f.TotalLiab = build(liab)
	f.Balanced = f.TotalAssets == f.TotalLiab
	return f, nil
}

// ---- GST ----

// GSTLine is one outward supply in the GST register.
type GSTLine struct {
	Date    string       `json:"date"`
	Invoice string       `json:"invoice"`
	Kind    string       `json:"kind"`
	UCC     string       `json:"ucc"`
	Name    string       `json:"name"`
	GSTIN   string       `json:"gstin,omitempty"`
	POS     string       `json:"pos"` // place of supply (state)
	SAC     string       `json:"sac"`
	Taxable money.Amount `json:"taxable"`
	Rate    money.Dec    `json:"rate"`
	IGST    money.Amount `json:"igst"`
	CGST    money.Amount `json:"cgst"`
	SGST    money.Amount `json:"sgst"`
	Total   money.Amount `json:"total"` // taxable + tax
}

// GSTReturn is the GST register for a period with GSTR-1 and GSTR-3B
// summaries.
type GSTReturn struct {
	From         string        `json:"from"`
	To           string        `json:"to"`
	FirmGSTIN    string        `json:"firm_gstin"`
	FirmState    string        `json:"firm_state"`
	Lines        []GSTLine     `json:"lines"`
	B2B          []GSTLine     `json:"b2b"`
	B2CS         []GSTLine     `json:"b2cs"` // by place of supply and rate
	HSN          []GSTLine     `json:"hsn"`  // by SAC
	Docs         []GSTDocRange `json:"docs"`
	Out          GSTLine       `json:"outward"`       // 3.1(a)
	ITC          GSTLine       `json:"itc"`           // 4(A)(5)
	Payable      GSTLine       `json:"payable"`       // tax to pay in cash after set-off
	CarryForward GSTLine       `json:"carry_forward"` // credit left over
	Warnings     []string      `json:"warnings"`
}

// GSTDocRange summarises a document series for GSTR-1 table 13.
type GSTDocRange struct {
	Series string `json:"series"`
	From   string `json:"from"`
	To     string `json:"to"`
	Count  int    `json:"count"`
}

// SACBroking is the services accounting code used on contract notes.
const SACBroking = "997152"

func splitGST(gst money.Amount, inter bool) (i, c, s money.Amount) {
	if inter {
		return gst, 0, 0
	}
	c = gst / 2
	return 0, c, gst - c
}

// GST builds the outward-supply register from contract notes (brokerage
// and exchange and SEBI charges, as billed) and tax invoices, splits tax
// into IGST or CGST and SGST by place of supply, and summarises it for
// GSTR-1 and GSTR-3B. Input tax credit comes from the input GST accounts.
func (o *Ops) GST(ctx context.Context, tenant, from, to string) (GSTReturn, error) {
	if !ledger.ValidDate(from) || !ledger.ValidDate(to) || from > to {
		return GSTReturn{}, invalid("give a period: from on or before to")
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return GSTReturn{}, err
	}
	r := GSTReturn{From: from, To: to, FirmGSTIN: set.FirmGSTIN, FirmState: set.FirmState, Lines: []GSTLine{}, Warnings: []string{}}
	if set.FirmState == "" {
		r.Warnings = append(r.Warnings, "Set the firm's state in Settings: without it every supply is treated as within the state (CGST + SGST).")
	}
	cs, _ := o.ClientMap(ctx, tenant)
	rate := set.gstRate()
	pos := func(c Client) (string, bool) {
		st := strings.TrimSpace(c.State)
		if st == "" {
			st = set.FirmState
		}
		return st, set.FirmState != "" && !strings.EqualFold(st, set.FirmState)
	}
	all, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{})
	if err != nil {
		return r, err
	}
	byID := map[string]ledger.Entry{}
	for _, e := range all {
		byID[e.ID] = e
	}
	type acc struct{ taxable, gst money.Amount }
	notes := map[string]*acc{} // date|ucc
	for _, e := range all {
		if e.ValueDate < from || e.ValueDate > to {
			continue
		}
		src := e
		if e.Source.Kind == "REVERSAL" {
			if orig, ok := byID[e.ReversalOf]; ok {
				src = orig
			}
		}
		if src.Source.Kind != "BILL" && src.Source.Kind != "MTM" && src.Source.Kind != "EXPIRY" {
			continue
		}
		p := strings.Split(src.Source.ID, "/")
		if len(p) < 2 {
			continue
		}
		k := p[0] + "|" + p[1]
		a := notes[k]
		if a == nil {
			a = &acc{}
			notes[k] = a
		}
		for _, x := range e.Postings {
			switch {
			case x.Account == ledger.IncomeBrokerage, strings.HasPrefix(x.Account, "PAYABLE:EXCH_TXN"), x.Account == ledger.PayableSEBIFee:
				a.taxable += x.Credit - x.Debit
			case x.Account == ledger.PayableGST:
				a.gst += x.Credit - x.Debit
			}
		}
	}
	for _, k := range sortedKeys(notes) {
		a := notes[k]
		if a.taxable == 0 && a.gst == 0 {
			continue
		}
		d, u, _ := strings.Cut(k, "|")
		c := cs[u]
		no := "BILL " + d + " " + u
		if n, _, err := docs.Get[ContractNote](ctx, o.Docs, tenant, cNote, d+"|"+u); err == nil {
			no = n.No
		}
		st, inter := pos(c)
		l := GSTLine{Date: d, Invoice: no, Kind: "Contract note", UCC: u, Name: c.Name, GSTIN: c.GSTIN, POS: st, SAC: SACBroking, Taxable: a.taxable, Rate: rate, Total: a.taxable + a.gst}
		l.IGST, l.CGST, l.SGST = splitGST(a.gst, inter)
		r.Lines = append(r.Lines, l)
	}
	invs, _ := docs.List[Invoice](ctx, o.Docs, tenant, cInvoice, "")
	for _, iv := range invs {
		if iv.Date < from || iv.Date > to || iv.Status != "Issued" {
			continue
		}
		c := cs[iv.UCC]
		st, inter := pos(c)
		l := GSTLine{Date: iv.Date, Invoice: iv.No, Kind: iv.Kind, UCC: iv.UCC, Name: iv.Name, GSTIN: c.GSTIN, POS: st, SAC: iv.SAC, Taxable: iv.Taxable, Rate: iv.GSTPct, Total: iv.Total}
		l.IGST, l.CGST, l.SGST = splitGST(iv.GST, inter)
		r.Lines = append(r.Lines, l)
	}
	sort.SliceStable(r.Lines, func(i, j int) bool {
		if r.Lines[i].Date != r.Lines[j].Date {
			return r.Lines[i].Date < r.Lines[j].Date
		}
		return r.Lines[i].Invoice < r.Lines[j].Invoice
	})
	b2cs, hsn := map[string]*GSTLine{}, map[string]*GSTLine{}
	series := map[string]*GSTDocRange{}
	addTo := func(t *GSTLine, l GSTLine) {
		t.Taxable += l.Taxable
		t.IGST += l.IGST
		t.CGST += l.CGST
		t.SGST += l.SGST
		t.Total += l.Total
	}
	for _, l := range r.Lines {
		addTo(&r.Out, l)
		if l.GSTIN != "" {
			r.B2B = append(r.B2B, l)
		} else {
			k := l.POS + "|" + l.Rate.String()
			if b2cs[k] == nil {
				b2cs[k] = &GSTLine{POS: l.POS, Rate: l.Rate, Kind: "B2CS"}
			}
			addTo(b2cs[k], l)
		}
		if hsn[l.SAC] == nil {
			hsn[l.SAC] = &GSTLine{SAC: l.SAC, Rate: l.Rate}
		}
		addTo(hsn[l.SAC], l)
		if strings.HasPrefix(l.Invoice, "BILL ") {
			continue
		}
		i := strings.LastIndex(l.Invoice, "/")
		if i < 0 {
			continue
		}
		s := l.Invoice[:i]
		if series[s] == nil {
			series[s] = &GSTDocRange{Series: s, From: l.Invoice, To: l.Invoice}
		}
		x := series[s]
		x.Count++
		if l.Invoice < x.From {
			x.From = l.Invoice
		}
		if l.Invoice > x.To {
			x.To = l.Invoice
		}
	}
	if r.B2B == nil {
		r.B2B = []GSTLine{}
	}
	for _, k := range sortedKeys(b2cs) {
		r.B2CS = append(r.B2CS, *b2cs[k])
	}
	for _, k := range sortedKeys(hsn) {
		r.HSN = append(r.HSN, *hsn[k])
	}
	for _, k := range sortedKeys(series) {
		r.Docs = append(r.Docs, *series[k])
	}
	if r.B2CS == nil {
		r.B2CS = []GSTLine{}
	}
	if r.HSN == nil {
		r.HSN = []GSTLine{}
	}
	if r.Docs == nil {
		r.Docs = []GSTDocRange{}
	}
	for _, e := range all {
		if e.ValueDate < from || e.ValueDate > to {
			continue
		}
		for _, p := range e.Postings {
			switch p.Account {
			case InputIGST:
				r.ITC.IGST += p.Debit - p.Credit
			case InputCGST:
				r.ITC.CGST += p.Debit - p.Credit
			case InputSGST:
				r.ITC.SGST += p.Debit - p.Credit
			}
		}
	}
	r.Payable, r.CarryForward = setOff(r.Out, r.ITC)
	return r, nil
}

// GSTR1CSV returns one GSTR-1 table in the GST offline tool's column layout.
func (o *Ops) GSTR1CSV(ctx context.Context, tenant, from, to, table string) ([]byte, error) {
	r, err := o.GST(ctx, tenant, from, to)
	if err != nil {
		return nil, err
	}
	var rows [][]string
	switch table {
	case "b2b":
		rows = [][]string{{"GSTIN/UIN of Recipient", "Receiver Name", "Invoice Number", "Invoice date", "Invoice Value", "Place Of Supply", "Reverse Charge", "Invoice Type", "Rate", "Taxable Value", "Integrated Tax", "Central Tax", "State/UT Tax"}}
		for _, l := range r.B2B {
			rows = append(rows, []string{l.GSTIN, l.Name, l.Invoice, l.Date, l.Total.String(), l.POS, "N", "Regular", l.Rate.String(), l.Taxable.String(), l.IGST.String(), l.CGST.String(), l.SGST.String()})
		}
	case "b2cs":
		rows = [][]string{{"Type", "Place Of Supply", "Rate", "Taxable Value", "Integrated Tax", "Central Tax", "State/UT Tax"}}
		for _, l := range r.B2CS {
			rows = append(rows, []string{"OE", l.POS, l.Rate.String(), l.Taxable.String(), l.IGST.String(), l.CGST.String(), l.SGST.String()})
		}
	case "hsn":
		rows = [][]string{{"HSN", "Description", "UQC", "Total Quantity", "Total Value", "Taxable Value", "Integrated Tax Amount", "Central Tax Amount", "State/UT Tax Amount", "Rate"}}
		for _, l := range r.HSN {
			rows = append(rows, []string{l.SAC, "Stock broking services", "NA", "0", l.Total.String(), l.Taxable.String(), l.IGST.String(), l.CGST.String(), l.SGST.String(), l.Rate.String()})
		}
	case "docs":
		rows = [][]string{{"Nature of Document", "Sr. No. From", "Sr. No. To", "Total Number", "Cancelled"}}
		for _, d := range r.Docs {
			rows = append(rows, []string{"Invoices for outward supply", d.From, d.To, fmt.Sprint(d.Count), "0"})
		}
	default:
		return nil, invalid("table must be b2b, b2cs, hsn or docs")
	}
	return CSV(rows), nil
}

var invoiceTmpl = template.Must(template.New("inv").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Tax invoice {{.I.No}}</title>
<style>body{font:14px/1.5 system-ui,sans-serif;color:#1d1d1b;max-width:760px;margin:32px auto;padding:0 16px}h1{font-size:20px;margin:0}table{width:100%;border-collapse:collapse;margin:16px 0}td,th{border-bottom:1px solid #ddd;padding:8px;text-align:left}td.r,th.r{text-align:right}.muted{color:#666;font-size:12.5px}.top{display:flex;justify-content:space-between;gap:16px;border-bottom:2px solid #1d1d1b;padding-bottom:12px}</style></head><body>
<div class="top"><div><h1>{{.S.Firm}}</h1><div class="muted">SEBI {{.S.SEBIReg}}{{if .S.FirmGSTIN}} · GSTIN {{.S.FirmGSTIN}}{{end}}{{if .S.FirmState}} · {{.S.FirmState}}{{end}}</div></div><div style="text-align:right"><b>TAX INVOICE</b><div>{{.I.No}}</div><div class="muted">{{.I.Date}}</div></div></div>
<p><b>{{.C.Name}}</b><br><span class="muted">UCC {{.C.UCC}}{{if .C.GSTIN}} · GSTIN {{.C.GSTIN}}{{end}} · Place of supply {{.POS}}</span></p>
<table><tr><th>Description</th><th>SAC</th><th class="r">Taxable value (₹)</th></tr><tr><td>{{.I.Description}}</td><td>{{.I.SAC}}</td><td class="r">{{.I.Taxable}}</td></tr>
{{if .IGST}}<tr><td colspan="2">IGST @ {{.I.GSTPct}}%</td><td class="r">{{.IGST}}</td></tr>{{else}}<tr><td colspan="2">CGST @ {{.Half}}%</td><td class="r">{{.CGST}}</td></tr><tr><td colspan="2">SGST @ {{.Half}}%</td><td class="r">{{.SGST}}</td></tr>{{end}}
<tr><th colspan="2">Total</th><th class="r">{{.I.Total}}</th></tr></table>
<p class="muted">Debited to your trading account (journal {{.I.Entry}}). This is a computer-generated invoice.{{if .S.ComplianceEmail}} Queries: {{.S.ComplianceEmail}}.{{end}}</p></body></html>`))

// RenderInvoice returns a printable tax invoice.
func (o *Ops) RenderInvoice(ctx context.Context, tenant, no string) ([]byte, error) {
	iv, _, err := docs.Get[Invoice](ctx, o.Docs, tenant, cInvoice, strings.ReplaceAll(no, "/", "-"))
	if err != nil {
		return nil, err
	}
	c, _ := o.Client(ctx, tenant, iv.UCC)
	set, _ := o.Settings(ctx, tenant)
	st := c.State
	if st == "" {
		st = set.FirmState
	}
	inter := set.FirmState != "" && !strings.EqualFold(st, set.FirmState)
	i, cg, sg := splitGST(iv.GST, inter)
	half, _ := iv.GSTPct.Quo(money.DecInt(2))
	var buf bytes.Buffer
	err = invoiceTmpl.Execute(&buf, map[string]any{"I": iv, "C": c, "S": set, "POS": st, "IGST": func() string {
		if i == 0 {
			return ""
		}
		return i.String()
	}(), "CGST": cg, "SGST": sg, "Half": half})
	return buf.Bytes(), err
}

// setOff applies input tax credit against output tax in the order the GST
// law requires: IGST credit against IGST, then CGST, then SGST; CGST credit
// against CGST, then IGST; SGST credit against SGST, then IGST. CGST and
// SGST credit can never be used against each other.
func setOff(out, itc GSTLine) (pay, left GSTLine) {
	pay = GSTLine{IGST: out.IGST, CGST: out.CGST, SGST: out.SGST}
	left = GSTLine{IGST: itc.IGST, CGST: itc.CGST, SGST: itc.SGST}
	use := func(credit, tax *money.Amount) {
		if *credit <= 0 || *tax <= 0 {
			return
		}
		u := min(*credit, *tax)
		*credit -= u
		*tax -= u
	}
	use(&left.IGST, &pay.IGST)
	use(&left.IGST, &pay.CGST)
	use(&left.IGST, &pay.SGST)
	use(&left.CGST, &pay.CGST)
	use(&left.CGST, &pay.IGST)
	use(&left.SGST, &pay.SGST)
	use(&left.SGST, &pay.IGST)
	return pay, left
}
