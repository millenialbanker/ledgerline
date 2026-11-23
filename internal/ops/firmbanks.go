package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- the firm's bank accounts: which is which ----
//
// SEBI names the bank accounts a stock broker keeps for client money
// (master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90 of 17 June 2025,
// para 15.3.2): clients' funds are received only in the Up Streaming Client
// Nodal Bank Account (USCNBA, 15.3.2.1), clients are paid only from the Down
// Streaming Client Nodal Bank Account (DSCNBA, 15.3.2.2), and accounts held
// for settlement are the Settlement Account (15.3.2.3). Any other account of
// the broker is deemed its proprietary (own) account (15.3.3). The same
// split came with the upstreaming of client funds to the clearing
// corporations (SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2023/84 of 8 June 2023,
// para 3.C.II).
//
// Ledgerline's ledger keeps one client bank account (BANK:CLIENT) and one
// own account (BANK:OWN). This register says which real accounts the firm
// has, of which kind and for which exchanges or segments, so the daily
// reconciliation (clientmoney.go) can say from which account money came and
// went, and the UCC-wise settlement report (rasucc.go) can name the account
// clients were paid from. Settings already name some of them (the payout
// account, MCX-only accounts, the accounts mapped to a ledger); they are
// offered here to be added, not copied silently.

const cFirmBank = "firmbank"

// Kinds of the firm's bank accounts.
const (
	BankUSCNBA     = "USCNBA"
	BankDSCNBA     = "DSCNBA"
	BankSettlement = "Settlement"
	BankOwn        = "Own"
	BankCC         = "Clearing corporation" // the counterparty for upstreaming and funds coming back
)

// FirmBankKinds are the kinds, with what each is for.
var FirmBankKinds = []struct{ ID, Name, About string }{
	{BankUSCNBA, "Up Streaming Client Nodal Bank Account (USCNBA)", "Clients' money is received only here, and upstreamed to the clearing corporation (SEBI master circular 2025/90, para 15.3.2.1)."},
	{BankDSCNBA, "Down Streaming Client Nodal Bank Account (DSCNBA)", "Clients are paid only from here, from funds that came back from the clearing corporation (para 15.3.2.2). Report it to the exchanges."},
	{BankSettlement, "Settlement account", "Held for settlement with the clearing corporation (para 15.3.2.3)."},
	{BankOwn, "Own (proprietary) account", "The firm's own money. Every account that is not a USCNBA, DSCNBA or settlement account counts as one (para 15.3.3)."},
	{BankCC, "Clearing corporation (counterparty)", "The clearing corporation's account the firm upstreams to and receives funds from."},
}

func firmBankKind(k string) bool {
	for _, x := range FirmBankKinds {
		if x.ID == k {
			return true
		}
	}
	return false
}

// clientMoneyKind says whether an account of this kind holds clients' money.
func clientMoneyKind(k string) bool {
	return k == BankUSCNBA || k == BankDSCNBA || k == BankSettlement
}

// FirmBank is one of the firm's bank accounts.
type FirmBank struct {
	ID      string `json:"id"` // FB-01
	Kind    string `json:"kind"`
	Bank    string `json:"bank"`
	Branch  string `json:"branch,omitempty"`
	Account string `json:"account"` // stored in full, shown masked
	IFSC    string `json:"ifsc"`
	// Name is the account's name as the bank has it, e.g. "<firm> – USCNB
	// account" (the nomenclature of para 15.3.2).
	Name string `json:"name,omitempty"`
	// CC is the clearing corporation, for a counterparty account (NCL,
	// ICCL, MCCIL, MCXCCL, NCCL).
	CC string `json:"cc,omitempty"`
	// Serves are the exchanges or exchange segments it is for: NSE, NSE-CM,
	// MCX-COM …; empty is all of them.
	Serves []string `json:"serves"`
	// Reported: reported to the exchanges as a client bank account under
	// enhanced supervision (a DSCNBA must be, to pay clients from it). It
	// is set by itself once ReportedOn has every exchange the firm trades
	// on; an account ticked before the dates were kept stays ticked.
	Reported bool `json:"reported,omitempty"`
	// OpenedOn is the day the bank opened the account. A new account is
	// reported to each exchange within 7 working days of opening, and a
	// closed one within 7 working days of closing (SEBI master circular
	// 2025/90 para 15.4.3 as amended by SEBI HO/38/11/(1)2026-MIRSD-POD/I/
	// 7656/2026 of 23 March 2026, NSE/INSP/73436; NSE inspection master
	// circular NSE/INSP/73976 item 8.2). ReportedOn and ClosureReportedOn
	// are the days it was reported, by exchange (NSE, BSE, MCX …).
	OpenedOn          string            `json:"opened_on,omitempty"`
	ReportedOn        map[string]string `json:"reported_on,omitempty"`
	ClosureReportedOn map[string]string `json:"closure_reported_on,omitempty"`
	// UndertakingOn is the day the bank's undertaking addendum for the
	// account (the exchange's authority to obtain its statements) was
	// submitted: within 7 working days of opening a client bank account
	// (NSE/INSP/77149, circular 52/2026, Annexure A).
	UndertakingOn string `json:"undertaking_on,omitempty"`
	// UPIID is the firm's validated @valid UPI ID linked to the account:
	// every account whose number is given to investors for payment is
	// linked to one, so it can be verified through SEBI Check
	// (NSE/COMP/75700, circular 73/2026). A USCNBA always is such an
	// account; InvestorFacing marks any other.
	UPIID          string `json:"upi_id,omitempty"`
	InvestorFacing bool   `json:"investor_facing,omitempty"`
	// Overdraft: an overdraft, cash credit or loan-against-securities
	// account. Its balance is never client money and is left out of the
	// client funds check (NSE/INSP/73976 Exhibit 34); a USCNBA, DSCNBA or
	// settlement account cannot be one.
	Overdraft bool `json:"overdraft,omitempty"`
	// Opening is the balance on OpeningOn, the day before Ledgerline's
	// records of the account start; the reconciliation's book balances run
	// from it.
	Opening   money.Amount `json:"opening"`
	OpeningOn string       `json:"opening_on,omitempty"`
	Active    bool         `json:"active"`
	ClosedOn  string       `json:"closed_on,omitempty"`
	Note      string       `json:"note,omitempty"`
	By        string       `json:"by,omitempty"`
	At        string       `json:"at,omitempty"`
}

// Public masks the account number.
func (b FirmBank) Public() FirmBank {
	b.Account = MaskAccount(b.Account)
	return b
}

// Label names the account for people: kind, bank and the last four digits.
func (b FirmBank) Label() string {
	if b.Kind == BankCC {
		return firstOf(ClearingCorpNames[b.CC], firstOf(b.Bank, "Clearing corporation")) + " " + MaskAccount(b.Account)
	}
	return strings.TrimSpace(b.Kind + " " + b.Bank + " " + MaskAccount(b.Account))
}

// serves says whether the account is for an exchange or segment (an empty
// list serves all).
func (b FirmBank) serves(exchange string) bool {
	if len(b.Serves) == 0 || exchange == "" {
		return true
	}
	for _, s := range b.Serves {
		if s == exchange || strings.HasPrefix(s, exchange+"-") || strings.HasPrefix(exchange, s+"-") {
			return true
		}
	}
	return false
}

// firmBanks lists the register, oldest first.
func (o *Ops) firmBanks(ctx context.Context, tenant string) []FirmBank {
	bs, _ := docs.List[FirmBank](ctx, o.Docs, tenant, cFirmBank, "")
	sort.SliceStable(bs, func(i, j int) bool { return bs[i].ID < bs[j].ID })
	for i := range bs {
		if bs[i].Serves == nil {
			bs[i].Serves = []string{}
		}
	}
	return bs
}

// firmBankByAccount finds an account by its number.
func firmBankByAccount(bs []FirmBank, acct string) (FirmBank, bool) {
	n := normAcctNo(acct)
	if n == "" {
		return FirmBank{}, false
	}
	for _, b := range bs {
		if normAcctNo(b.Account) == n {
			return b, true
		}
	}
	return FirmBank{}, false
}

func firmBankByID(bs []FirmBank, id string) (FirmBank, bool) {
	for _, b := range bs {
		if b.ID == id {
			return b, true
		}
	}
	return FirmBank{}, false
}

// defaultFirmBank is the account of a kind money of an exchange goes
// through when nothing recorded says which: the first active one of the
// kind named for the exchange, else one for every exchange, else the first
// active one of the kind.
func defaultFirmBank(bs []FirmBank, kind, exchange string) (FirmBank, bool) {
	var all, other *FirmBank
	for i := range bs {
		b := &bs[i]
		if b.Kind != kind || !b.Active {
			continue
		}
		if exchange != "" && len(b.Serves) > 0 && b.serves(exchange) {
			return *b, true
		}
		if len(b.Serves) == 0 && all == nil {
			all = b
		}
		if other == nil {
			other = b
		}
	}
	if all != nil {
		return *all, true
	}
	if other != nil {
		return *other, true
	}
	return FirmBank{}, false
}

// FirmBankSuggestion is an account Settings already names that is not in
// the register.
type FirmBankSuggestion struct {
	Account string   `json:"account"` // masked
	Kind    string   `json:"kind"`
	Serves  []string `json:"serves"`
	From    string   `json:"from"`
	Key     string   `json:"key"` // to add it: the full number is filled in on the server
}

// FirmBankView is the register for the screen.
type FirmBankView struct {
	Accounts []FirmBank           `json:"accounts"`
	Suggest  []FirmBankSuggestion `json:"suggest"`
	Kinds    any                  `json:"kinds"`
	Segments []string             `json:"segments"`
	CCs      map[string]string    `json:"ccs"`
	Warnings []string             `json:"warnings"`
	CanEdit  bool                 `json:"can_edit"`
	// Exchanges are those each account is reported to; Reports what is
	// still to report or record, with its due date (firmbankrules.go).
	Exchanges []string         `json:"exchanges"`
	Reports   []BankReportItem `json:"reports"`
}

// FirmBanks is the register with what Settings name and what is missing.
func (o *Ops) FirmBanks(ctx context.Context, tenant, actor string) (FirmBankView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the firm's bank accounts", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return FirmBankView{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	bs := o.firmBanks(ctx, tenant)
	v := FirmBankView{Accounts: []FirmBank{}, Suggest: []FirmBankSuggestion{}, Kinds: FirmBankKinds, CCs: ClearingCorpNames, Warnings: []string{}}
	for _, b := range bs {
		v.Accounts = append(v.Accounts, b.Public())
	}
	for _, s := range set.activeSegments() {
		v.Segments = append(v.Segments, s.Key)
	}
	v.Suggest = o.firmBankSuggestions(ctx, tenant, set, bs)
	v.CanEdit = o.require(ctx, tenant, actor, "Changing the firm's bank accounts", RoleCompliance) == nil
	v.Exchanges, v.Reports = o.reportingExchanges(ctx, tenant), []BankReportItem{}
	for _, b := range bs {
		for _, it := range bankReportItems(set, v.Exchanges, b) {
			if it.Done == "" {
				v.Reports = append(v.Reports, it)
			}
		}
	}
	has := map[string]int{}
	for _, b := range bs {
		if b.Active {
			has[b.Kind]++
		}
	}
	if len(bs) > 0 {
		for _, k := range []string{BankUSCNBA, BankDSCNBA} {
			if has[k] == 0 {
				v.Warnings = append(v.Warnings, fmt.Sprintf("No active %s: client money is received only in a USCNBA and paid only from a DSCNBA (SEBI master circular 2025/90, para 15.3.2).", k))
			}
		}
		for _, b := range bs {
			if b.Active && b.Kind == BankDSCNBA && !b.Reported {
				v.Warnings = append(v.Warnings, fmt.Sprintf("%s is not marked as reported to the exchanges: clients may be paid only from an account reported under enhanced supervision (NSE/INSP/56148).", b.Label()))
			}
		}
		v.Warnings = append(v.Warnings, o.firmBankProblems(ctx, tenant, set, bs, o.now().In(ist).Format("2006-01-02"))...)
	}
	if has[BankUSCNBA]+has[BankDSCNBA] > 30 {
		v.Warnings = append(v.Warnings, "More than 30 USCNBA and DSCNBA accounts: NSE allows at most 30 together (NSE/INSP/57959, FAQ 23).")
	}
	return v, nil
}

// firmBankSuggestions are accounts Settings and the bank connection name
// that the register does not have yet.
func (o *Ops) firmBankSuggestions(ctx context.Context, tenant string, set Settings, bs []FirmBank) []FirmBankSuggestion {
	out := []FirmBankSuggestion{}
	seen := map[string]bool{}
	add := func(acct, kind, from string, serves ...string) {
		n := normAcctNo(acct)
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		if _, ok := firmBankByAccount(bs, acct); ok {
			return
		}
		if serves == nil {
			serves = []string{}
		}
		out = append(out, FirmBankSuggestion{Account: MaskAccount(acct), Kind: kind, Serves: serves, From: from, Key: from + "|" + n})
	}
	add(set.PayoutDebitAccount, BankDSCNBA, "Settings → payout account")
	for _, a := range set.MCXOnlyBankAccounts {
		add(a, BankUSCNBA, "Settings → MCX-only bank accounts", "MCX")
	}
	for _, a := range sortedKeys(set.BankAccountLedgers) {
		serves := []string(nil)
		if set.BankAccountLedgers[a] == "COM" {
			serves = []string{"MCX"}
		}
		add(a, BankUSCNBA, "Settings → firm bank accounts and their ledger", serves...)
	}
	if c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connCreditsECMS); err == nil {
		for _, a := range splitList(c.Values["accounts"]) {
			add(a, BankUSCNBA, "Connections → HDFC Bank e-CMS")
		}
	}
	return out
}

// suggestedAccount is the full number behind a suggestion's key.
func (o *Ops) suggestedAccount(ctx context.Context, tenant, key string) string {
	set, _ := o.Settings(ctx, tenant)
	_, n, _ := strings.Cut(key, "|")
	cands := append([]string{set.PayoutDebitAccount}, set.MCXOnlyBankAccounts...)
	cands = append(cands, sortedKeys(set.BankAccountLedgers)...)
	if c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connCreditsECMS); err == nil {
		cands = append(cands, splitList(c.Values["accounts"])...)
	}
	for _, a := range cands {
		if n != "" && normAcctNo(a) == n {
			return strings.ReplaceAll(strings.TrimSpace(a), " ", "")
		}
	}
	return ""
}

// FirmBankReq adds or changes an account. Account is the full number; when
// changing an account, an empty or masked number keeps the one on record.
// Suggestion takes the number from a suggestion's key.
type FirmBankReq struct {
	FirmBank
	Suggestion string `json:"suggestion,omitempty"`
}

// SaveFirmBank adds or changes an account in the register (compliance
// officer or Admin).
func (o *Ops) SaveFirmBank(ctx context.Context, tenant, actor string, req FirmBankReq) (FirmBank, error) {
	if err := o.require(ctx, tenant, actor, "Changing the firm's bank accounts", RoleCompliance); err != nil {
		return FirmBank{}, err
	}
	b := req.FirmBank
	bs := o.firmBanks(ctx, tenant)
	var prev *FirmBank
	if b.ID != "" {
		p, ok := firmBankByID(bs, b.ID)
		if !ok {
			return FirmBank{}, fmt.Errorf("%w: bank account %s", ErrNotFound, b.ID)
		}
		prev = &p
	}
	b.Account = strings.ReplaceAll(strings.TrimSpace(b.Account), " ", "")
	if req.Suggestion != "" {
		b.Account = o.suggestedAccount(ctx, tenant, req.Suggestion)
	}
	if prev != nil && (b.Account == "" || strings.Contains(b.Account, "X")) {
		b.Account = prev.Account
	}
	b.Kind, b.Bank, b.Branch, b.Name = strings.TrimSpace(b.Kind), clip(strings.TrimSpace(b.Bank), 80), clip(strings.TrimSpace(b.Branch), 80), clip(strings.TrimSpace(b.Name), 120)
	b.IFSC, b.CC = strings.ToUpper(strings.TrimSpace(b.IFSC)), strings.ToUpper(strings.TrimSpace(b.CC))
	switch {
	case !firmBankKind(b.Kind):
		return b, invalid("say which kind of account it is: USCNBA, DSCNBA, Settlement, Own or Clearing corporation")
	case b.Bank == "":
		return b, invalid("give the bank's name")
	case !reAcct.MatchString(b.Account):
		return b, invalid("the account number is 9 to 18 digits")
	case !reIFSC.MatchString(b.IFSC):
		return b, invalid("the IFSC is 4 letters, 0, then 6 letters or digits")
	case b.Kind == BankCC && ClearingCorpNames[b.CC] == "":
		return b, invalid("say which clearing corporation the account is of (NCL, ICCL, MCCIL, MCXCCL or NCCL)")
	case b.OpeningOn != "" && !validDate(b.OpeningOn):
		return b, invalid("the opening balance date is YYYY-MM-DD")
	case b.ClosedOn != "" && !validDate(b.ClosedOn):
		return b, invalid("the closing date is YYYY-MM-DD")
	case b.OpenedOn != "" && !validDate(b.OpenedOn):
		return b, invalid("the date the account was opened is YYYY-MM-DD")
	case b.UndertakingOn != "" && !validDate(b.UndertakingOn):
		return b, invalid("the date the bank undertaking was submitted is YYYY-MM-DD")
	case b.Overdraft && clientMoneyKind(b.Kind):
		return b, invalid("a %s holds client money: it cannot be an overdraft, cash credit or loan-against-securities account", b.Kind)
	}
	if b.Kind == BankCC {
		b.OpenedOn, b.ReportedOn, b.ClosureReportedOn, b.UndertakingOn, b.UPIID, b.InvestorFacing = "", nil, nil, "", "", false
	}
	if why, hard := bankNameCheck(o.firmSettings(ctx, tenant), b); hard {
		return b, invalid("%s", why)
	}
	b.UPIID = normVPA(b.UPIID)
	if b.UPIID != "" && !reValidUPI.MatchString(b.UPIID) {
		return b, invalid("%s is not a validated UPI ID: the username, the category (.brk for broking money) and @valid with the bank's name, like abc.brk@validhdfc", b.UPIID)
	}
	for _, m := range []map[string]string{b.ReportedOn, b.ClosureReportedOn} {
		for x, d := range m {
			if d != "" && !validDate(d) {
				return b, invalid("the date the account was reported to %s is YYYY-MM-DD", x)
			}
		}
	}
	b.ReportedOn, b.ClosureReportedOn = cleanDateMap(b.ReportedOn), cleanDateMap(b.ClosureReportedOn)
	if exs := o.reportingExchanges(ctx, tenant); len(exs) > 0 && b.Kind != BankCC {
		all := true
		for _, x := range exs {
			all = all && b.ReportedOn[x] != ""
		}
		b.Reported = b.Reported || all
	}
	if b.Kind != BankCC {
		b.CC = ""
	}
	var serves []string
	for _, s := range b.Serves {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "" || contains(serves, s) {
			continue
		}
		known := false
		for _, c := range ClearingSegments {
			known = known || c.Key == s || c.Exchange == s
		}
		if !known {
			return b, invalid("%s is not an exchange (NSE, BSE, MSE, MCX, NCDEX) or an exchange segment such as NSE-CM", s)
		}
		serves = append(serves, s)
	}
	if serves == nil {
		serves = []string{}
	}
	b.Serves = serves
	for _, x := range bs {
		if x.ID != b.ID && normAcctNo(x.Account) == normAcctNo(b.Account) {
			return b, invalid("account %s is already in the register as %s", MaskAccount(b.Account), x.ID)
		}
	}
	if prev == nil {
		n, err := o.next(ctx, tenant, "firmbank", actor)
		if err != nil {
			return b, err
		}
		b.ID = fmt.Sprintf("FB-%02d", n)
		if !b.Active && b.ClosedOn == "" {
			b.Active = true
		}
	}
	if b.ClosedOn != "" {
		b.Active = false
	}
	b.Note, b.By, b.At = clip(strings.TrimSpace(b.Note), 300), actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cFirmBank, b.ID, b, actor); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "firmbank.saved", b.ID, map[string]any{"kind": b.Kind, "bank": b.Bank, "account": MaskAccount(b.Account), "serves": b.Serves, "active": b.Active, "reported": b.Reported,
		"opened_on": b.OpenedOn, "reported_on": b.ReportedOn, "closure_reported_on": b.ClosureReportedOn, "undertaking_on": b.UndertakingOn, "upi_id": b.UPIID, "overdraft": b.Overdraft})
	return b.Public(), nil
}

// rasPaidFrom is the firm account running account settlements are paid
// from: the DSCNBA of the register (for NSE first), else the payout account
// in Settings.
func (o *Ops) rasPaidFrom(ctx context.Context, tenant string) (string, string) {
	bs := o.firmBanks(ctx, tenant)
	if b, ok := defaultFirmBank(bs, BankDSCNBA, "NSE"); ok {
		return b.Account, b.IFSC
	}
	set, _ := o.Settings(ctx, tenant)
	if a := strings.TrimSpace(set.PayoutDebitAccount); a != "" {
		if b, ok := firmBankByAccount(bs, a); ok {
			return b.Account, b.IFSC
		}
		return a, ""
	}
	return "", ""
}
