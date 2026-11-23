package ops

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

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

// Account is an entry in the chart of accounts. People know it by its
// name (unique in the firm) and an optional alias; the code is made by
// Ledgerline (chart.go) and kept inside.
type Account struct {
	Code         string       `json:"code"`
	Name         string       `json:"name"`
	Alias        string       `json:"alias,omitempty"`
	Head         string       `json:"head,omitempty"` // head ID; empty: the default head for the code
	Group        string       `json:"group"`          // the level-2 head's name, as shown
	Type         string       `json:"type"`
	Restricted   bool         `json:"restricted"`
	AllowedUsers []string     `json:"allowed_users"`
	System       bool         `json:"system"`
	Status       string       `json:"status,omitempty"` // Active, Pending (made at voucher entry, waiting for a checker), Rejected
	CreatedBy    string       `json:"created_by,omitempty"`
	Created      string       `json:"created,omitempty"`
	Checker      string       `json:"checker,omitempty"`
	Reason       string       `json:"reason,omitempty"`
	Balance      money.Amount `json:"balance"` // debit positive, as of the requested date
	// Worked out when listed.
	HeadName string `json:"head_name,omitempty"`
	SubHead  string `json:"sub_head,omitempty"`
	Level1   string `json:"level1,omitempty"`
}

var systemNames = map[string]string{
	ledger.BankClient: "Client bank account (segregated)", ledger.BankOwn: "Own bank account", ledger.IncomeBrokerage: "Brokerage",
	ledger.PayableSTT: "STT / CTT payable", ledger.PayableStamp: "Stamp duty payable", ledger.PayableSEBIFee: "SEBI turnover fee payable",
	ledger.PayableGST: "GST payable (output tax)", ledger.SuspenseBank: "Bank suspense (unidentified credits)", ledger.OpeningEquity: "Opening balance equity",
	ledger.CCCollateral: "Cash collateral with clearing corporation", ledger.PayablePenalty: "Margin penalty payable", IncomeInterest: "Interest and delayed payment charges",
	IncomeCharges: "Other charges recovered", ReceivableGateway: "Payment gateway receivable", InputIGST: "Input IGST", InputCGST: "Input CGST", InputSGST: "Input SGST",
	"EXPENSE:GATEWAY_FEES": "Payment gateway fees", ExpenseBadDebts: "Bad debts written off", IncomeDP: "Depository (DP) charges", IncomeMTFInterest: "Interest on margin trading funding", PayableDepository: "Payable to depository (CDSL / NSDL)",
	PayableDPStamp: "Stamp duty on off-market transfers, payable to the depository",
	ReceivableFees: "Research and advisory fees receivable (fees received by UPI are credited here)",
}

func accountType(code string) string {
	t, err := ledger.TypeOf(code)
	if err != nil {
		return ""
	}
	return string(t)
}

// accountGroup is the name of the default head a code is shown under
// (for records that store it; listings work it out afresh).
func accountGroup(code string) string {
	id := defaultHeadOf(code)
	for _, d := range defaultHeads {
		if d.id == id {
			return d.name
		}
	}
	return ""
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
// saved account, every system account and every account the journal has
// used, each with its head, and clients folded into one line.
func (o *Ops) Accounts(ctx context.Context, tenant, actor, asOf string) ([]Account, error) {
	bal, err := o.App.Ledger.Balances(ctx, tenant, asOf)
	if err != nil {
		return nil, err
	}
	m := o.accountMaster(ctx, tenant)
	hs := o.heads(ctx, tenant)
	var out []Account
	vis := o.AccountVisible(ctx, tenant, actor)
	var clients money.Amount
	nClients := 0
	seenClient := map[string]bool{} // a client's equity, commodity and awaiting ledgers are one client
	for _, code := range chartCodes(m, bal) {
		if !vis(code) {
			continue
		}
		a, ok := m[code]
		if !ok {
			a = Account{Code: code, System: true}
		}
		a.Name = accountName(code, m)
		a = o.decorate(hs, a, m)
		a.Type = accountType(code)
		if a.AllowedUsers == nil {
			a.AllowedUsers = []string{}
		}
		a.Balance = bal[code]
		out = append(out, a)
	}
	for _, code := range sortedKeys(bal) {
		if u, _, ok := ledger.ClientCode(code); ok {
			clients += bal[code]
			if !seenClient[u] {
				seenClient[u] = true
				nClients++
			}
		}
	}
	out = append(out, Account{Code: "CLIENT:*", Name: fmt.Sprintf("Client ledgers (%d)", nClients), Group: "Clients", Type: string(ledger.Liability), System: true, Balance: clients, AllowedUsers: []string{}, Status: accountActive, Level1: L1Liabilities})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

// SaveAccount adds an account to the chart of accounts, or changes one's
// name, alias or who may see it. A new account needs only a name and the
// head it goes under: Ledgerline makes its code. An old-style call with a
// code still works, so codes the firm used before keep working. Moving
// an account to another head goes through RequestAccountMove.
func (o *Ops) SaveAccount(ctx context.Context, tenant, actor string, a Account) (Account, error) {
	if err := o.require(ctx, tenant, actor, "Changing the chart of accounts", RoleChecker); err != nil {
		return a, err
	}
	a.Code = strings.ToUpper(strings.TrimSpace(a.Code))
	a.Name, a.Alias = strings.Join(strings.Fields(a.Name), " "), strings.Join(strings.Fields(a.Alias), " ")
	if a.Name == "" {
		return a, invalid("give the account a name")
	}
	if utf8.RuneCountInString(a.Alias) > 24 {
		return a, invalid("keep the alias short (24 letters at most)")
	}
	hs := o.heads(ctx, tenant)
	m := o.accountMaster(ctx, tenant)
	old, exists := m[a.Code]
	if a.Code == "" {
		h, err := hs.headForNew(a.Head)
		if err != nil {
			return a, err
		}
		if err := checkAccountNames(m, "", a.Name, a.Alias); err != nil {
			return a, err
		}
		code, err := o.newCode(ctx, tenant, actor, h.Prefix, m)
		if err != nil {
			return a, err
		}
		a.Code, a.Head, a.Status, a.CreatedBy, a.Created = code, h.ID, accountActive, actor, o.now().Format(time.RFC3339)
	} else {
		t, err := ledger.TypeOf(a.Code)
		if err != nil {
			return a, invalid("%v", err)
		}
		if ledger.IsClientAccount(a.Code) || strings.HasPrefix(a.Code, "CC:") && !exists {
			return a, invalid("client and clearing accounts are created by the system")
		}
		if exists && old.Status == accountRejected {
			return a, invalid("%s was rejected", old.Name)
		}
		cur := hs.headOf(a.Code, m)
		switch {
		case a.Head == "" || a.Head == cur:
			a.Head = old.Head
		case !exists && systemNames[a.Code] == "":
			// A new account with an old-style code may name its head.
			h, ok := hs.get(a.Head)
			if !ok || h.Clients || h.Level1 != level1Of(t) {
				return a, invalid("choose a head under %s for this account", level1Of(t))
			}
		default:
			return a, invalid("to put an account under another head, ask for a move; a second person approves it")
		}
		if err := checkAccountNames(m, a.Code, a.Name, a.Alias); err != nil {
			return a, err
		}
		if exists {
			a.System, a.Status, a.CreatedBy, a.Created, a.Checker = old.System, old.Status, old.CreatedBy, old.Created, old.Checker
		} else {
			a.System, a.Status, a.CreatedBy, a.Created = systemNames[a.Code] != "", accountActive, actor, o.now().Format(time.RFC3339)
		}
	}
	a.Type = accountType(a.Code)
	if a.AllowedUsers == nil {
		a.AllowedUsers = []string{}
	}
	if a.Restricted && len(a.AllowedUsers) == 0 {
		a.AllowedUsers = []string{actor}
	}
	a.Balance, a.HeadName, a.SubHead, a.Level1 = 0, "", "", ""
	a.Group = accountGroup(a.Code)
	if h, ok := hs.get(hs.headOf(a.Code, map[string]Account{a.Code: a})); ok {
		a.Group = h.Name
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cAccount, a.Code, a, actor); err != nil {
		return a, err
	}
	o.indexChanged(tenant)
	o.log(ctx, tenant, actor, "account.saved", a.Code, map[string]any{"name": a.Name, "alias": a.Alias, "restricted": a.Restricted})
	m[a.Code] = a
	return o.decorate(hs, a, m), nil
}

// VoucherLine is one line of a voucher.
type VoucherLine struct {
	Account string       `json:"account"`
	Debit   money.Amount `json:"debit"`
	Credit  money.Amount `json:"credit"`
	Memo    string       `json:"memo,omitempty"`
	Name    string       `json:"name,omitempty"` // the account's name, filled in when listed
}

// Voucher is a payment, receipt, journal or contra entry of the firm.
type Voucher struct {
	No     string `json:"no"`
	Type   string `json:"type"` // Payment, Receipt, Journal, Contra
	Date   string `json:"date"`
	Branch string `json:"branch,omitempty"`
	Party  string `json:"party,omitempty"`
	// PartyAccount is the account picked as the party, when one was.
	PartyAccount string        `json:"party_account,omitempty"`
	Ref          string        `json:"ref,omitempty"` // cheque number or UTR
	Narration    string        `json:"narration"`
	Lines        []VoucherLine `json:"lines"`
	Total        money.Amount  `json:"total"`
	Status       string        `json:"status"` // Pending, Posted, Rejected, Cancelled
	Maker        string        `json:"maker"`
	Checker      string        `json:"checker,omitempty"`
	Entry        string        `json:"entry,omitempty"`
	Reason       string        `json:"reason,omitempty"`
	Bill         string        `json:"bill,omitempty"` // vendor bill booked or paid by it
	// NewAccounts are the accounts on a pending voucher that wait for a
	// checker; approving the voucher approves them. Filled in when listed.
	NewAccounts []Account `json:"new_accounts,omitempty"`
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
	m := o.accountMaster(ctx, tenant)
	var cs map[string]Client
	out := []Voucher{}
	for _, v := range vs {
		ok := true
		for i, l := range v.Lines {
			ok = ok && vis(l.Account)
			if u, kind, isClient := ledger.ClientCode(l.Account); isClient {
				if cs == nil {
					cs, _ = o.ClientMap(ctx, tenant)
				}
				n := cs[u].Name
				if n == "" {
					n = u
				}
				if kind == ledger.LedgerCommodity {
					n += " · Commodity ledger"
				}
				v.Lines[i].Name = n + " (" + u + ")"
			} else {
				v.Lines[i].Name = accountName(l.Account, m)
			}
			if a, has := m[l.Account]; has && a.Status == accountPending && v.Status == "Pending" {
				v.NewAccounts = append(v.NewAccounts, a)
			}
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
	return o.saveVoucher(ctx, tenant, actor, v)
}

// saveVoucher is SaveVoucher once the actor may enter it (a transfer
// between a client's ledgers is entered by Risk too, segledger.go).
func (o *Ops) saveVoucher(ctx context.Context, tenant, actor string, v Voucher) (Voucher, error) {
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
	master := o.accountMaster(ctx, tenant)
	v.PartyAccount = strings.ToUpper(strings.TrimSpace(v.PartyAccount))
	if v.PartyAccount != "" {
		if _, err := ledger.TypeOf(v.PartyAccount); err != nil {
			return v, invalid("party: %v", err)
		}
	}
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
		if a, ok := master[l.Account]; ok && a.Status == accountRejected {
			return v, invalid("line %d: the account %s was rejected by the checker (%s)", i+1, a.Name, a.Reason)
		}
		if ledger.IsClientAccount(l.Account) && v.Type != "Journal" {
			return v, invalid("client money moves through Receipts & payouts; use a journal voucher only for adjustments")
		}
		if _, kind, ok := ledger.ClientCode(l.Account); ok && kind != ledger.LedgerEquity && !o.sepCom(ctx, tenant) {
			return v, invalid("line %d: %s is a separate commodity ledger account, and the firm keeps one ledger per client", i+1, l.Account)
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
	if err := clientJournalRule(lines); err != nil {
		return v, err
	}
	n, err := o.next(ctx, tenant, "voucher-"+series+"-"+finYear(v.Date), actor)
	if err != nil {
		return v, err
	}
	v.No = fmt.Sprintf("%s/%s/%05d", series, finYear(v.Date), n)
	for i := range lines {
		lines[i].Name = ""
	}
	v.Lines, v.Total, v.Status, v.Maker, v.Checker, v.Entry, v.NewAccounts = lines, dr, "Pending", actor, "", "", nil
	if _, err := docs.Put(ctx, o.Docs, tenant, cVoucher, strings.ReplaceAll(v.No, "/", "-"), v, 0, actor); err != nil {
		return v, err
	}
	o.log(ctx, tenant, actor, "voucher.entered", v.No, map[string]any{"type": v.Type, "total": v.Total.String()})
	return v, nil
}

// DecideVoucher posts (approve) or rejects a pending voucher, or cancels a
// posted one by reversing it.
func (o *Ops) DecideVoucher(ctx context.Context, tenant, actor, no, action, reason string) (Voucher, error) {
	return o.DecideVoucherAccounts(ctx, tenant, actor, no, action, reason, nil)
}

// DecideVoucherAccounts is DecideVoucher where the checker also rejects
// some of the voucher's new accounts with the voucher (rejectAccounts,
// with the same reason). Approving a voucher approves the new accounts it
// uses, in the same step: the checker may not be the person who made
// them, and a voucher with a rejected account cannot post.
func (o *Ops) DecideVoucherAccounts(ctx context.Context, tenant, actor, no, action, reason string, rejectAccounts []string) (Voucher, error) {
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
		m := o.accountMaster(ctx, tenant)
		var pending []string
		for _, l := range v.Lines {
			a, ok := m[l.Account]
			switch {
			case !ok:
			case a.Status == accountRejected:
				return v, fmt.Errorf("%w: the account %s on this voucher was rejected (%s); reject the voucher and enter it again", ErrState, a.Name, a.Reason)
			case a.Status == accountPending:
				if a.CreatedBy == actor {
					return v, fmt.Errorf("%w: you made the new account %s; another checker approves the voucher", ErrForbidden, a.Name)
				}
				if !contains(pending, a.Code) {
					pending = append(pending, a.Code)
				}
			}
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
		for _, code := range pending {
			if _, err := o.decideAccount(ctx, tenant, actor, code, "approve", ""); err != nil {
				return v, err
			}
		}
		o.indexChanged(tenant)
	case "reject":
		if v.Status != "Pending" {
			return v, fmt.Errorf("%w: %s is %s", ErrState, no, v.Status)
		}
		if strings.TrimSpace(reason) == "" {
			return v, invalid("give the reason for rejecting")
		}
		m := o.accountMaster(ctx, tenant)
		for _, code := range rejectAccounts {
			on := false
			for _, l := range v.Lines {
				on = on || l.Account == code
			}
			if a, ok := m[code]; !on || !ok || a.Status != accountPending {
				return v, invalid("%s is not a new account on this voucher", code)
			}
			if _, err := o.decideAccount(ctx, tenant, actor, code, "reject", reason); err != nil {
				return v, err
			}
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
		o.indexChanged(tenant)
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

// FinLine is one account line of a financial statement.
type FinLine struct {
	Code   string       `json:"code"`
	Name   string       `json:"name"`
	Amount money.Amount `json:"amount"` // positive in the section's natural direction
}

// FinSection is a head on a statement: its accounts, its sub-heads and
// their total. The income and expenses sections are level 1 and list
// every account in Lines as well as the heads in Heads; on the balance
// sheet each section is a level-2 head whose Lines are the accounts
// directly under it.
type FinSection struct {
	ID    string       `json:"id,omitempty"`
	Title string       `json:"title"`
	Lines []FinLine    `json:"lines"`
	Heads []FinSection `json:"heads"`
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
// income by the client's branch, vouchers by their branch. Both are laid
// out by heads (chart.go): level 1, level 2, sub-heads, accounts.
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
	m := o.accountMaster(ctx, tenant)
	hs := o.heads(ctx, tenant)
	extra := map[string]string{"CLIENT:DR": "Clients with debit balances", "CLIENT:CR": "Clients with credit balances", "PROFIT": "Profit and loss account"}
	name := func(code string) string {
		if n := extra[code]; n != "" {
			return n
		}
		return accountName(code, m)
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
			if u, _, ok := ledger.ClientCode(p.Account); ok {
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
	// lines makes the lines of a set of accounts, restricted ones folded.
	lines := func(m map[string]money.Amount) ([]FinLine, money.Amount) {
		out := []FinLine{}
		var tot, hidden money.Amount
		for _, k := range sortedKeys(m) {
			if m[k] == 0 {
				continue
			}
			if !vis(k) {
				hidden += m[k]
			} else {
				out = append(out, FinLine{Code: k, Name: name(k), Amount: m[k]})
			}
			tot += m[k]
		}
		if hidden != 0 {
			out = append(out, FinLine{Name: "Restricted accounts", Amount: hidden})
		}
		return out, tot
	}
	// tree lays out amounts by head: byHead[head][code] = amount.
	var tree func(h *AccountHead, byHead map[string]map[string]money.Amount) FinSection
	tree = func(h *AccountHead, byHead map[string]map[string]money.Amount) FinSection {
		s := FinSection{ID: h.ID, Title: h.Name, Heads: []FinSection{}}
		s.Lines, s.Total = lines(byHead[h.ID])
		for _, c := range hs.children(h.ID, h.Level1) {
			cs := tree(c, byHead)
			if len(cs.Lines) == 0 && len(cs.Heads) == 0 {
				continue
			}
			s.Heads = append(s.Heads, cs)
			s.Total += cs.Total
		}
		return s
	}
	// level2 lays out one level-1 head; an account whose head cannot be
	// found goes in a last "Other" section so totals never drop anything.
	level2 := func(l1 string, byHead map[string]map[string]money.Amount) []FinSection {
		out := []FinSection{}
		placed := map[string]bool{}
		var mark func(id string)
		mark = func(id string) {
			placed[id] = true
			for _, c := range hs.children(id, l1) {
				mark(c.ID)
			}
		}
		for _, h := range hs.children("", l1) {
			mark(h.ID)
			s := tree(h, byHead)
			if len(s.Lines) == 0 && len(s.Heads) == 0 {
				continue
			}
			out = append(out, s)
		}
		rest := map[string]money.Amount{}
		for id, am := range byHead {
			if placed[id] {
				continue
			}
			for k, v := range am {
				rest[k] += v
			}
		}
		if ls, tot := lines(rest); len(ls) > 0 {
			out = append(out, FinSection{ID: l1 + "-OTHER", Title: "Other " + strings.ToLower(l1), Lines: ls, Heads: []FinSection{}, Total: tot})
		}
		return out
	}
	byHead := func(amts map[string]money.Amount) map[string]map[string]money.Amount {
		out := map[string]map[string]money.Amount{}
		for k, v := range amts {
			h := hs.headOf(k, m)
			if out[h] == nil {
				out[h] = map[string]money.Amount{}
			}
			out[h][k] += v
		}
		return out
	}
	level1 := func(title string, amts map[string]money.Amount) FinSection {
		s := FinSection{ID: title, Title: title}
		s.Lines, s.Total = lines(amts)
		s.Heads = level2(title, byHead(amts))
		return s
	}
	f.Income, f.Expenses = level1(L1Income, inc), level1(L1Expenditure, exp)
	f.Expenses.Title = "Expenses"
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
	owed := map[string]map[string]money.Amount{}
	put := func(m map[string]map[string]money.Amount, g, k string, v money.Amount) {
		if m[g] == nil {
			m[g] = map[string]money.Amount{}
		}
		m[g][k] += v
	}
	var clientDr, clientCr money.Amount
	// A client is a debtor or a creditor as a whole: with a separate
	// commodity ledger the client's ledgers are netted first.
	clientNet := map[string]money.Amount{}
	for k, v := range bal {
		t, _ := ledger.TypeOf(k)
		switch {
		case ledger.IsClientAccount(k):
			u, _, _ := ledger.ClientCode(k)
			clientNet[u] += v
		case t == ledger.Income || t == ledger.Expense:
			retained += -v
		case t == ledger.Asset && v < 0:
			// An asset account in credit (an overdrawn bank, money owed to a
			// clearing corporation) is a liability at the balance-sheet date.
			put(owed, hs.root(hs.headOf(k, m)), k, -v)
		case t == ledger.Asset:
			put(asset, hs.headOf(k, m), k, v)
		default:
			put(liab, hs.headOf(k, m), k, -v)
		}
	}
	for _, v := range clientNet {
		if v > 0 {
			clientDr += v
		} else {
			clientCr += -v
		}
	}
	put(asset, HeadClientsDr, "CLIENT:DR", clientDr)
	put(liab, HeadClientsCr, "CLIENT:CR", clientCr)
	put(liab, "L-CAPITAL", "PROFIT", retained)
	sum := func(ss []FinSection) (t money.Amount) {
		for _, s := range ss {
			t += s.Total
		}
		return t
	}
	f.Assets = level2(L1Assets, asset)
	f.Liabilities = level2(L1Liabilities, liab)
	for _, id := range hs.order {
		if am := owed[id]; len(am) > 0 {
			s := FinSection{ID: id + "-OWED", Title: hs.byID[id].Name + " (owed)", Heads: []FinSection{}}
			s.Lines, s.Total = lines(am)
			f.Liabilities = append(f.Liabilities, s)
			delete(owed, id)
		}
	}
	for id, am := range owed { // a head that is no longer there
		s := FinSection{ID: id + "-OWED", Title: "Assets in credit (owed)", Heads: []FinSection{}}
		s.Lines, s.Total = lines(am)
		f.Liabilities = append(f.Liabilities, s)
	}
	f.TotalAssets, f.TotalLiab = sum(f.Assets), sum(f.Liabilities)
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
		if src.Source.Kind == "PREPAID_ADJ" {
			// Brokerage met from prepaid credit: GST was paid on the
			// advance, so this day's note carries less taxable value.
			d, u, _ := strings.Cut(src.Source.ID, "/")
			k := d + "|" + u
			a := notes[k]
			if a == nil {
				a = &acc{}
				notes[k] = a
			}
			for _, x := range e.Postings {
				switch x.Account {
				case PayablePrepaid:
					a.taxable -= x.Debit - x.Credit
				case ledger.PayableGST:
					a.gst -= x.Debit - x.Credit
				}
			}
			continue
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

// clientJournalRule keeps journal vouchers on client ledgers to what they
// may do. A running account is settled only by an actual payment to the
// client's bank account, and journal entries are used only to levy or
// reverse charges (SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/
// 2025/90, para 48.3); no inter-client adjustment is made in settling
// accounts (para 23.1.1(j); NSE/INSP/53820, note d). So a journal that
// touches a client's ledger may touch only that client's ledgers (a move
// between its own equity and commodity ledgers, segledger.go) and income,
// expense and statutory or exchange payable accounts: never another
// client's ledger, a bank, cash, suspense, the clearing corporation or any
// other party's account.
func clientJournalRule(lines []VoucherLine) error {
	client := ""
	var others []string
	for _, l := range lines {
		if u, _, ok := ledger.ClientCode(l.Account); ok {
			if client != "" && u != client {
				return invalid("a journal voucher cannot move money from one client's ledger to another's (%s and %s): no inter-client adjustment is allowed (SEBI master circular 2025/90, para 23.1.1(j))", client, u)
			}
			client = u
			continue
		}
		others = append(others, l.Account)
	}
	if client == "" {
		return nil
	}
	for _, a := range others {
		t, _ := ledger.TypeOf(a)
		switch {
		case t == ledger.Income || t == ledger.Expense:
		case strings.HasPrefix(a, "PAYABLE:") && a != PayableOFSMargin && a != PayableEIPOFunds:
		default:
			return invalid("a journal voucher on a client's ledger may only levy or reverse charges (income, expense, statutory or exchange dues); %s is not one. A client is paid only by a payout to the registered bank account, never by a journal (SEBI master circular 2025/90, para 48.3)", a)
		}
	}
	return nil
}
