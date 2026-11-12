package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// A client may have more than one registered bank account.
//
// The rules followed here:
//
//   - Money is taken only from the client's own (registered) bank accounts
//     and paid only to them: SEBI Master Circular for Stock Brokers
//     SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90 of 17 June 2025, para 28.2
//     (cheques drawn only by the clients, paid only in favour of the
//     clients; from SEBI/MRD/SE/Cir-33/2003/27/08 and
//     SEBI/HO/MIRSD/DOP/CIR/P/2018/113) and para 29.1 (third-party funds,
//     an audit trail that funds are received from the clients only;
//     CIR/MIRSD/03/2011). SEBI sets no number of accounts: any account of
//     the client that the client has registered with the broker counts. A
//     credit from an account not registered (or no longer active) is
//     third-party money (bank.go) and is returned.
//   - Payouts and running account settlement (para 24.3, para 15.10 "the
//     required bank details for initiating electronic fund transfers")
//     go to the primary account unless the client chooses another of the
//     registered accounts for a payout.
//   - Payments by UPI come in through the firm's validated .brk@valid IDs
//     (SEBI circular of 11 June 2025, upicollect.go) and are matched like
//     any credit: from a registered account of the client, or third-party.
//   - The number of accounts is limited by the exchanges' UCC records,
//     which carry the client's bank accounts with a primary/secondary flag:
//     BSE UCC 5 (BANKNAME1..5, PRIMARYORSECONDARYBANK1..5), NSE UCI 5
//     (ccdBankName..ccdBankName5, ccdPriSecBnk..5, NSE/ISC/73973 of 30
//     April 2026 for the file formats), MCX UCC 3 (BankName1..3 in the file
//     and bank_name_1..3 in the API). Settings → Maximum bank accounts per
//     client defaults to the fewest slots among the exchanges the firm
//     trades on (3 with MCX, otherwise 5) so every active account can be
//     reported to every exchange.
//   - Adding, removing or changing the primary account is a change of the
//     client's details: it needs the client's request (the portal login, or
//     a signed letter for staff), proof for a new account (cancelled cheque
//     or bank statement), a check that the account is the client's (penny
//     drop where a verification provider is connected), and a second
//     person's approval (the change request flow, portal.go). Once
//     approved the UCC records change and are sent to each exchange as a
//     modification (the UCC pages pick up the change), and a primary
//     change also goes to the depository (cdslbo.go). Para 36.8.1.2 ("the
//     list of clients' ... bank account and demat accounts may be updated
//     / amended by proper communication") is followed in the same way.

const (
	BankActive   = "Active"
	BankInactive = "Inactive"
)

// BankAccountTypes are the account types kept.
var BankAccountTypes = []string{"SB", "CA", "NRE", "NRO"}

// uccBankSlots is how many bank accounts each exchange's UCC record holds.
var uccBankSlots = map[string]int{"BSE": 5, "NSE": 5, "MCX": 3}

const maxUCCBankSlots = 5

// Bank account change actions (ChangeRequest.New["action"] of a "Bank
// account" change). Empty is replace (the only one before).
const (
	BankReplace    = "replace"
	BankAdd        = "add"
	BankDeactivate = "deactivate"
	BankPrimary    = "primary"
)

// uccBankSlotsMin is the fewest bank slots among the exchanges the firm
// trades on.
func (s Settings) uccBankSlotsMin() int {
	n := 0
	for _, x := range []string{"NSE", "BSE", "MCX"} {
		if s.tradesOn(x) && (n == 0 || uccBankSlots[x] < n) {
			n = uccBankSlots[x]
		}
	}
	if n == 0 {
		n = uccBankSlots["MCX"]
	}
	return n
}

// maxClientBanks is the most active bank accounts a client may have.
func (s Settings) maxClientBanks() int {
	if s.MaxClientBanks > 0 {
		return int(s.MaxClientBanks)
	}
	return s.uccBankSlotsMin()
}

// MaxClientBanksNow is the limit in force and why.
func (o *Ops) MaxClientBanksNow(ctx context.Context, tenant string) (int, string) {
	set, _ := o.Settings(ctx, tenant)
	n, slots := set.maxClientBanks(), set.uccBankSlotsMin()
	why := fmt.Sprintf("the fewest bank accounts any exchange the firm trades on takes in its UCC record is %d (BSE 5, NSE 5, MCX 3)", slots)
	if set.MaxClientBanks > 0 {
		why = "set in Settings"
		if n > slots {
			why += fmt.Sprintf("; more than the %d an exchange's UCC record takes, so the extra accounts cannot be reported there", slots)
		}
	}
	return n, why
}

func (b BankAccount) active() bool { return b.Status == "" || b.Status == BankActive }

func (b BankAccount) details() BankAccount {
	return BankAccount{Bank: b.Bank, IFSC: b.IFSC, Account: b.Account, Branch: b.Branch, MICR: b.MICR, Type: b.Type}
}

func normBank(b *BankAccount) {
	b.Bank = strings.Join(strings.Fields(b.Bank), " ")
	b.IFSC = strings.ToUpper(strings.TrimSpace(b.IFSC))
	b.Account = strings.TrimSpace(b.Account)
	b.Branch = strings.TrimSpace(b.Branch)
	b.MICR = strings.TrimSpace(b.MICR)
	b.Type = normBankType(b.Type)
	if b.Status == "" || strings.EqualFold(b.Status, BankActive) {
		b.Status = BankActive
	} else if strings.EqualFold(b.Status, BankInactive) {
		b.Status = BankInactive
	}
}

func normBankType(t string) string {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	case "SB", "SA", "SAVINGS", "SAVING", "10":
		return "SB"
	case "CA", "CB", "CURRENT", "11":
		return "CA"
	case "NRE":
		return "NRE"
	case "NRO":
		return "NRO"
	}
	return strings.ToUpper(strings.TrimSpace(t))
}

// registeredBanks are all the client's registered accounts, primary first:
// for a client with one account, Bank as the only (primary, active) one.
func registeredBanks(c Client) []BankAccount {
	if len(c.Banks) == 0 {
		if strings.TrimSpace(c.Bank.Account) == "" {
			return nil
		}
		b := c.Bank
		b.Primary, b.Status = true, BankActive
		return []BankAccount{b}
	}
	out := append([]BankAccount(nil), c.Banks...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Primary && !out[j].Primary })
	return out
}

// activeBanks are the active registered accounts, primary first.
func activeBanks(c Client) []BankAccount {
	var out []BankAccount
	for _, b := range registeredBanks(c) {
		if b.active() {
			out = append(out, b)
		}
	}
	return out
}

// primaryBank is the account payouts go to unless another is chosen.
func primaryBank(c Client) BankAccount {
	for _, b := range registeredBanks(c) {
		if b.Primary && b.active() {
			return b
		}
	}
	return c.Bank
}

// registeredBank finds an active registered account of the client by its
// number (and IFSC, when given).
func registeredBank(c Client, account, ifsc string) (BankAccount, bool) {
	account, ifsc = strings.TrimSpace(account), strings.ToUpper(strings.TrimSpace(ifsc))
	if account == "" {
		return BankAccount{}, false
	}
	for _, b := range registeredBanks(c) {
		if b.active() && b.Account == account && (ifsc == "" || strings.ToUpper(b.IFSC) == ifsc) {
			return b, true
		}
	}
	return BankAccount{}, false
}

func indexBank(bs []BankAccount, account, ifsc string) int {
	for i, b := range bs {
		if b.Account == account && (ifsc == "" || strings.EqualFold(b.IFSC, ifsc)) {
			return i
		}
	}
	return -1
}

func maskBanks(bs []BankAccount) []BankAccount {
	out := make([]BankAccount, len(bs))
	for i, b := range bs {
		b.Account = MaskAccount(b.Account)
		out[i] = b
	}
	return out
}

// syncBanks keeps Bank and Banks in step. Bank is the primary: when it
// names an account not in Banks (something that knows one account, such
// as a client master re-import, changed it) that account becomes the
// primary and the old primary is made inactive.
func syncBanks(c *Client) {
	if len(c.Banks) == 0 {
		return
	}
	for i := range c.Banks {
		normBank(&c.Banks[i])
	}
	if c.Bank.Account == "" {
		for _, b := range c.Banks {
			if b.Primary && b.active() {
				c.Bank = b.details()
			}
		}
		return
	}
	i := indexBank(c.Banks, c.Bank.Account, c.Bank.IFSC)
	if i < 0 {
		for j := range c.Banks {
			if c.Banks[j].Primary && c.Banks[j].active() {
				c.Banks[j].Status, c.Banks[j].InactiveWhy = BankInactive, "Replaced as the primary account"
			}
		}
		nb := c.Bank.details()
		nb.Status = BankActive
		c.Banks = append(c.Banks, nb)
		i = len(c.Banks) - 1
	}
	b := &c.Banks[i]
	b.Status, b.InactiveOn, b.InactiveWhy = BankActive, "", ""
	for _, f := range []struct {
		from string
		to   *string
	}{{c.Bank.Bank, &b.Bank}, {c.Bank.Branch, &b.Branch}, {c.Bank.MICR, &b.MICR}, {c.Bank.Type, &b.Type}} {
		if strings.TrimSpace(f.from) != "" {
			*f.to = strings.TrimSpace(f.from)
		}
	}
	normBank(b)
	for j := range c.Banks {
		c.Banks[j].Primary = j == i
	}
	c.Bank = b.details()
	sort.SliceStable(c.Banks, func(x, y int) bool { return c.Banks[x].Primary && !c.Banks[y].Primary })
}

// checkBanks validates the list of accounts.
func checkBanks(bs []BankAccount) []FieldError {
	var fe []FieldError
	seen := map[string]bool{}
	primaries := 0
	for i, b := range bs {
		f := fmt.Sprintf("banks.%d", i)
		if strings.TrimSpace(b.Bank) == "" {
			fe = append(fe, FieldError{f + ".bank", fmt.Sprintf("Bank account %d: enter the bank name", i+1)})
		}
		if !reIFSC.MatchString(b.IFSC) {
			fe = append(fe, FieldError{f + ".ifsc", fmt.Sprintf("Bank account %d: IFSC is 4 letters, 0, then 6 characters", i+1)})
		}
		if !reAcct.MatchString(b.Account) {
			fe = append(fe, FieldError{f + ".account", fmt.Sprintf("Bank account %d: account number is 9 to 18 digits", i+1)})
		}
		if b.Type != "" && !contains(BankAccountTypes, b.Type) {
			fe = append(fe, FieldError{f + ".type", fmt.Sprintf("Bank account %d: type must be SB, CA, NRE or NRO", i+1)})
		}
		if b.Status != BankActive && b.Status != BankInactive {
			fe = append(fe, FieldError{f + ".status", fmt.Sprintf("Bank account %d: status must be Active or Inactive", i+1)})
		}
		if b.Primary {
			primaries++
			if !b.active() {
				fe = append(fe, FieldError{f + ".primary", "The primary bank account must be active"})
			}
		}
		k := b.Account + "|" + strings.ToUpper(b.IFSC)
		if seen[k] {
			fe = append(fe, FieldError{f + ".account", fmt.Sprintf("Bank account %d is registered twice", i+1)})
		}
		seen[k] = true
	}
	if len(bs) > 0 && primaries != 1 {
		fe = append(fe, FieldError{"banks", "Exactly one bank account must be the primary"})
	}
	return fe
}

// bankChangeAction is a bank account change's action.
func bankChangeAction(r ChangeRequest) string {
	a := strings.ToLower(strings.TrimSpace(r.New["action"]))
	if a == "" {
		return BankReplace
	}
	return a
}

// bankChangeTouchesPrimary: the change alters the primary account, which
// the depository holds as the BO's bank (a secondary account is only the
// trading account's).
func bankChangeTouchesPrimary(r ChangeRequest) bool {
	switch bankChangeAction(r) {
	case BankReplace, BankPrimary:
		return true
	case BankAdd:
		return yes(r.New["primary"])
	}
	return false
}

// resolveBankTarget finds the account a deactivate or make-primary change
// is about: by its full number, or by its masked number and IFSC as the
// screens show it.
func resolveBankTarget(c Client, n map[string]string) (BankAccount, error) {
	bs := registeredBanks(c)
	acct, ifsc := strings.TrimSpace(n["account"]), strings.ToUpper(strings.TrimSpace(n["ifsc"]))
	if acct != "" && !strings.ContainsAny(acct, "Xx•*") {
		if i := indexBank(bs, acct, ifsc); i >= 0 {
			return bs[i], nil
		}
		return BankAccount{}, invalid("account %s is not registered for %s", MaskAccount(acct), c.UCC)
	}
	t := firstOf(strings.TrimSpace(n["target"]), acct)
	var hit []BankAccount
	for _, b := range bs {
		if t != "" && MaskAccount(b.Account) == MaskAccount(t) && (ifsc == "" || strings.EqualFold(b.IFSC, ifsc)) {
			hit = append(hit, b)
		}
	}
	switch len(hit) {
	case 0:
		return BankAccount{}, invalid("choose one of %s's registered bank accounts", c.UCC)
	case 1:
		return hit[0], nil
	}
	return BankAccount{}, invalid("more than one registered account ends %s: give the full account number", t)
}

// prepBankChange fills in the account a deactivate or make-primary change
// names, and checks the action.
func prepBankChange(c Client, n map[string]string) error {
	switch a := strings.ToLower(strings.TrimSpace(n["action"])); a {
	case "", BankReplace, BankAdd:
		return nil
	case BankDeactivate, BankPrimary:
		b, err := resolveBankTarget(c, n)
		if err != nil {
			return err
		}
		n["action"], n["account"], n["ifsc"], n["bank"] = a, b.Account, b.IFSC, b.Bank
		delete(n, "target")
		return nil
	default:
		return invalid("a bank account change is to add, replace or deactivate an account, or make one the primary")
	}
}

// applyBankChange changes the client's accounts as an approved (or, to
// check it, a pending) bank account change asks.
func applyBankChange(c *Client, r ChangeRequest) error {
	n := r.New
	date := firstOf(dateOnly(r.Decided), dateOnly(r.At))
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	c.Banks = registeredBanks(*c)
	nb := BankAccount{Bank: n["bank"], IFSC: n["ifsc"], Account: n["account"], Branch: n["branch"], MICR: n["micr"], Type: n["type"]}
	normBank(&nb)
	verify := func(b *BankAccount) {
		b.ProofDoc, b.Change = firstOf(r.Doc, b.ProofDoc), firstOf(r.ID, b.Change)
		if r.Checker == "" {
			return
		}
		b.VerifiedOn = date
		if r.Check != nil && r.Check.Status == "Passed" {
			b.VerifyMethod = "Penny drop (" + firstOf(r.Check.Provider, "provider") + ")"
		} else {
			b.VerifyMethod = "Proof checked by " + r.Checker
		}
	}
	setPrimary := func(i int) {
		for j := range c.Banks {
			c.Banks[j].Primary = j == i
		}
	}
	upsert := func() (int, error) {
		if nb.Account == "" {
			return -1, invalid("enter the bank account number")
		}
		i := indexBank(c.Banks, nb.Account, nb.IFSC)
		if i < 0 {
			nb.Status, nb.AddedOn = BankActive, date
			c.Banks = append(c.Banks, nb)
			i = len(c.Banks) - 1
		} else {
			b := &c.Banks[i]
			b.Bank, b.Branch, b.MICR, b.Type = firstOf(nb.Bank, b.Bank), firstOf(nb.Branch, b.Branch), firstOf(nb.MICR, b.MICR), firstOf(nb.Type, b.Type)
			b.Status, b.InactiveOn, b.InactiveWhy = BankActive, "", ""
		}
		verify(&c.Banks[i])
		return i, nil
	}
	switch bankChangeAction(r) {
	case BankReplace:
		old := -1
		for j, b := range c.Banks {
			if b.Primary {
				old = j
			}
		}
		i, err := upsert()
		if err != nil {
			return err
		}
		if old >= 0 && old != i {
			c.Banks[old].Status, c.Banks[old].InactiveOn, c.Banks[old].InactiveWhy = BankInactive, date, "Replaced by "+firstOf(r.ID, "a new account")
		}
		setPrimary(i)
	case BankAdd:
		if b, ok := registeredBank(*c, nb.Account, nb.IFSC); ok {
			return invalid("account %s (%s) is already registered for %s", MaskAccount(b.Account), b.IFSC, c.UCC)
		}
		i, err := upsert()
		if err != nil {
			return err
		}
		if yes(n["primary"]) {
			setPrimary(i)
		}
	case BankDeactivate:
		i := indexBank(c.Banks, nb.Account, nb.IFSC)
		switch {
		case i < 0:
			return invalid("account %s is not registered for %s", MaskAccount(nb.Account), c.UCC)
		case !c.Banks[i].active():
			return invalid("account %s is already inactive", MaskAccount(nb.Account))
		case c.Banks[i].Primary:
			return invalid("account %s is the primary account: make another account the primary first", MaskAccount(nb.Account))
		}
		c.Banks[i].Status, c.Banks[i].InactiveOn, c.Banks[i].InactiveWhy = BankInactive, date, firstOf(strings.TrimSpace(n["reason"]), "Removed at the client's request")
		c.Banks[i].Change = firstOf(r.ID, c.Banks[i].Change)
	case BankPrimary:
		i := indexBank(c.Banks, nb.Account, nb.IFSC)
		switch {
		case i < 0:
			return invalid("account %s is not registered for %s", MaskAccount(nb.Account), c.UCC)
		case !c.Banks[i].active():
			return invalid("account %s is inactive: add it again to use it", MaskAccount(nb.Account))
		case c.Banks[i].Primary:
			return invalid("account %s is already the primary account", MaskAccount(nb.Account))
		}
		setPrimary(i)
		c.Banks[i].Change = firstOf(r.ID, c.Banks[i].Change)
	default:
		return invalid("a bank account change is to add, replace or deactivate an account, or make one the primary")
	}
	for _, b := range c.Banks {
		if b.Primary {
			c.Bank = b.details()
		}
	}
	return nil
}

func dateOnly(at string) string {
	if len(at) >= 10 {
		return at[:10]
	}
	return ""
}

// bankLimitErr refuses a change that takes the client's active accounts
// above the firm's limit (a change that does not add one is allowed, so a
// client migrated with more can still remove accounts).
func bankLimitErr(before, after Client, limit int) error {
	nb, na := len(activeBanks(before)), len(activeBanks(after))
	if na > nb && na > limit {
		return invalid("%s may have at most %d active bank accounts (Settings → Maximum bank accounts per client); deactivate one first", after.UCC, limit)
	}
	return nil
}

// bankChangeApproved flags the exchanges' UCC records for a modification:
// they are re-made from the client and sent as a change (each UCC page
// shows the client as pending), and a work item says so.
func (o *Ops) bankChangeApproved(ctx context.Context, tenant string, r ChangeRequest, c Client) []string {
	if c.DPOnly {
		return nil // no UCC at the exchanges (dponly.go)
	}
	set, _ := o.Settings(ctx, tenant)
	var ex []string
	for _, x := range []string{"NSE", "BSE", "MCX"} {
		if set.tradesOn(x) {
			ex = append(ex, x)
		}
	}
	what := map[string]string{BankAdd: "added a bank account", BankReplace: "replaced the primary bank account", BankDeactivate: "deactivated a bank account", BankPrimary: "changed the primary bank account"}[bankChangeAction(r)]
	o.raiseWork(ctx, tenant, "uccmod", r.ID, fmt.Sprintf("Send %s's bank account change to the exchanges", c.UCC),
		fmt.Sprintf("%s %s (%s, %s). Upload the UCC modification to %s: the UCC pages list %s as changed; the records carry the %d active account(s), primary first.",
			r.ID, what, MaskAccount(r.New["account"]), strings.ToUpper(r.New["ifsc"]), strings.Join(ex, ", "), c.UCC, len(activeBanks(c))), c.UCC, RoleMaker, "uccapi", "")
	return ex
}

// uccBanks are the accounts an exchange's UCC record carries: the active
// ones, primary first, up to the record's slots; more is a problem.
func uccBanks(c Client, exch string, bad func(string, ...any)) []BankAccount {
	bs := activeBanks(c)
	if slots := uccBankSlots[exch]; len(bs) > slots {
		bad("The client has %d active bank accounts; the %s UCC record takes %d. Deactivate the extra accounts (Settings → Maximum bank accounts per client)", len(bs), exch, slots)
		bs = bs[:slots]
	}
	return bs
}

// ---- payouts to a chosen account ----

// payoutBank is the account a payout goes to: the one chosen on the
// request, which must still be an active registered account, or else the
// primary.
func payoutBank(c Client, p PayoutRequest) (BankAccount, error) {
	var b BankAccount
	if strings.TrimSpace(p.ToAccount) != "" {
		var ok bool
		if b, ok = registeredBank(c, p.ToAccount, p.ToIFSC); !ok {
			return b, invalid("%s: account %s is not an active registered bank account of %s", p.ID, MaskAccount(p.ToAccount), c.UCC)
		}
	} else if b = primaryBank(c); strings.TrimSpace(b.Account) == "" {
		return b, invalid("%s: %s has no registered bank account", p.ID, c.UCC)
	}
	if len(c.Incomplete) > 0 && !payable(b) {
		// Migrated as the previous system had it (incomplete.go): held here
		// rather than stopping the whole bank file (hdfcenet.go).
		return b, invalid("%s: %s's bank account %s (%s) is incomplete; complete the client's bank details first", p.ID, c.UCC, MaskAccount(b.Account), firstOf(b.IFSC, "no IFSC"))
	}
	return b, nil
}

// checkPayoutTo checks a chosen payout account: an active registered
// account of the client, with a reason when it is not the primary.
func checkPayoutTo(c Client, account, ifsc, reason string) (BankAccount, error) {
	if strings.ContainsAny(account, "Xx") {
		// As the portal and the screens show it: masked, with the IFSC.
		t, err := resolveBankTarget(c, map[string]string{"target": account, "ifsc": ifsc})
		if err != nil {
			return t, err
		}
		account, ifsc = t.Account, t.IFSC
	}
	b, ok := registeredBank(c, account, ifsc)
	if !ok {
		return b, invalid("account %s is not an active registered bank account of %s; payouts go only to the client's registered accounts", MaskAccount(account), c.UCC)
	}
	if !b.Primary && len(strings.TrimSpace(reason)) < 3 {
		return b, invalid("give the reason for paying to an account other than the primary")
	}
	return b, nil
}

// SetPayoutAccount chooses which registered account a payout not yet
// released goes to (empty account: back to the primary).
func (o *Ops) SetPayoutAccount(ctx context.Context, tenant, actor, id, account, ifsc, reason string) (PayoutRequest, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return PayoutRequest{}, err
	}
	p, _, err := docs.Get[PayoutRequest](ctx, o.Docs, tenant, cPayout, id)
	if err != nil {
		return p, err
	}
	if role == RoleClient {
		accts, _ := o.PortalAccounts(ctx, tenant, actor)
		if !contains(accts, p.UCC) {
			return PayoutRequest{}, ErrNotFound
		}
	} else if err := o.require(ctx, tenant, actor, "Entering a payout request", RoleMaker, RoleChecker, RoleBranch, RoleAP); err != nil {
		return PayoutRequest{}, err
	}
	c, err := o.Client(ctx, tenant, p.UCC)
	if err != nil {
		return p, err
	}
	var b BankAccount
	if strings.TrimSpace(account) != "" {
		if b, err = checkPayoutTo(c, account, ifsc, reason); err != nil {
			return p, err
		}
	}
	p, err = docs.Update(ctx, o.Docs, tenant, cPayout, id, actor, func(x *PayoutRequest) error {
		if x.Batch != "" || (x.Status != "Requested" && x.Status != "Approved") {
			return fmt.Errorf("%w: payout %s is %s and released already or closed", ErrState, id, x.Status)
		}
		x.ToAccount, x.ToIFSC, x.ToReason = b.Account, b.IFSC, strings.TrimSpace(reason)
		if b.Account == "" {
			x.ToReason = ""
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "payout.account", id, map[string]any{"ucc": p.UCC, "account": MaskAccount(b.Account), "ifsc": b.IFSC, "reason": trim(reason, 200)})
	}
	return p, err
}

// ---- migration: CLIENT-BANKS ----

// prepareClientBanks loads every client's bank accounts, one row each
// (several rows per client; the previous back office's bank master). The
// list replaces the client's accounts; Primary Y marks the primary (the
// first row when none is marked); Status Inactive keeps a closed one for
// the record.
func (o *Ops) prepareClientBanks(ctx context.Context, tenant, actor string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	warn := func(l int, f, m string) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: m, Warning: true})
	}
	by := map[string][]BankAccount{}
	lines := map[string]int{}
	rowLines := map[string][]int{}
	for _, r := range rows {
		u := strings.ToUpper(r.get("UCC"))
		if u == "" {
			errs = append(errs, ingest.ParseError{Line: r.line, Field: "UCC", Msg: "is required"})
			continue
		}
		b := BankAccount{Bank: r.get("Bank"), IFSC: r.get("IFSC"), Account: r.get("Account"), Branch: r.get("Branch"), MICR: r.get("MICR"), Type: r.get("Type"),
			Primary: yes(r.get("Primary")), Status: r.get("Status"), VerifiedOn: r.get("VerifiedOn"), VerifyMethod: r.get("VerifyMethod"), AddedOn: r.get("AddedOn")}
		normBank(&b)
		if b.VerifiedOn != "" && b.VerifyMethod == "" {
			b.VerifyMethod = "Migrated from the previous back office"
		}
		by[u] = append(by[u], b)
		rowLines[u] = append(rowLines[u], r.line)
		if lines[u] == 0 {
			lines[u] = r.line
		}
	}
	cs, _ := o.ClientMap(ctx, tenant)
	set, _ := o.Settings(ctx, tenant)
	limit := set.maxClientBanks()
	warned := map[string]bool{} // inactive clients whose accounts load incomplete
	for _, u := range sortedKeys(by) {
		bs := by[u]
		c, ok := cs[u]
		if !ok {
			errs = append(errs, ingest.ParseError{Line: lines[u], Field: "UCC", Msg: u + " is not a client"})
			continue
		}
		if c.Inactive {
			// An inactive client's accounts load as the previous system had
			// them (incomplete.go): only a row with no bank details at all, or
			// the same account a second time, is left out, and a status other
			// than Active or Inactive loads as Inactive.
			var keep []BankAccount
			seen := map[string]bool{}
			for i, b := range bs {
				l := rowLines[u][i]
				k := b.Account + "|" + strings.ToUpper(b.IFSC)
				switch {
				case b.Bank == "" && b.IFSC == "" && b.Account == "":
					warn(l, "Account", u+": the row has no bank details; it is left out")
					continue
				case seen[k]:
					warn(l, "Account", fmt.Sprintf("%s: account %s (%s) is on an earlier row too; this row is left out", u, firstOf(MaskAccount(b.Account), "with no number"), b.IFSC))
					continue
				case b.Status != BankActive && b.Status != BankInactive:
					warn(l, "Status", fmt.Sprintf("%s: status %q is neither Active nor Inactive; the account loads as Inactive", u, b.Status))
					b.Status = BankInactive
				}
				seen[k] = true
				keep = append(keep, b)
			}
			bs = keep
			if len(bs) == 0 {
				delete(by, u)
				continue
			}
		}
		p := -1
		for i, b := range bs {
			if b.Primary && p < 0 && b.active() {
				p = i
			}
		}
		if p < 0 {
			for i, b := range bs {
				if b.active() && p < 0 {
					p = i
				}
			}
		}
		for i := range bs {
			bs[i].Primary = i == p
		}
		if !c.Inactive {
			// An active client's other accounts that the previous system
			// holds with an invalid number or IFSC (often masked numbers
			// copied from the KRA) cannot receive payouts, which go only to
			// the primary: they load as inactive accounts.
			for i := range bs {
				b := &bs[i]
				if b.Primary || !b.active() || (reAcct.MatchString(b.Account) && reIFSC.MatchString(b.IFSC)) {
					continue
				}
				b.Status, b.InactiveWhy = BankInactive, bankInvalidWhy
				warn(rowLines[u][i], "Account", fmt.Sprintf("%s: account %s (%s) is not a valid account number and IFSC; it loads as an inactive account (payouts go only to the primary)", u, firstOf(MaskAccount(b.Account), "with no number"), firstOf(b.IFSC, "no IFSC")))
			}
		}
		n := len(activeBanks(Client{Banks: bs}))
		if n > limit {
			// The primary and the first others up to the limit stay active;
			// the rest are kept as inactive accounts.
			k := 1
			for i := range bs {
				if !bs[i].active() || bs[i].Primary {
					continue
				}
				if k < limit {
					k++
					continue
				}
				bs[i].Status, bs[i].InactiveWhy = BankInactive, bankOverLimitWhy
			}
			warn(lines[u], "Account", fmt.Sprintf("%s: %d active bank accounts; at most %d are allowed (Settings → Maximum bank accounts per client), so %d load as inactive accounts", u, n, limit, n-limit))
		}
		for _, f := range checkBanks(bs) {
			if i := bankIndex(f.Field); !c.Inactive && i >= 0 && !bs[i].active() {
				// an active client's inactive account: kept as it was
				warn(lines[u], f.Field, u+": "+f.Msg+inactiveAcctNote)
				continue
			}
			if c.Inactive {
				note := incompleteNote
				if i := bankIndex(f.Field); i >= 0 && !bs[i].active() {
					note = inactiveAcctNote
				} else {
					warned[u] = true
				}
				warn(lines[u], f.Field, u+": "+f.Msg+note)
				continue
			}
			errs = append(errs, ingest.ParseError{Line: lines[u], Field: f.Field, Msg: u + ": " + f.Msg})
		}
		by[u] = bs
	}
	return func() (int, int, error) {
		n := 0
		for _, u := range sortedKeys(by) {
			if _, ok := cs[u]; !ok {
				continue
			}
			bs := by[u]
			if _, err := docs.Update(ctx, o.Docs, tenant, cClient, u, actor, func(c *Client) error {
				c.Banks = append([]BankAccount(nil), bs...)
				c.Bank = BankAccount{}
				for _, b := range c.Banks {
					if b.Primary {
						c.Bank = b.details()
					}
				}
				syncBanks(c)
				if warned[u] {
					addIncomplete(c, "Bank accounts")
				}
				refreshIncomplete(c, nil)
				return nil
			}); err != nil {
				return n, 0, err
			}
			n += len(bs)
		}
		o.log(ctx, tenant, actor, "client_banks.loaded", "", map[string]any{"clients": len(by), "accounts": n})
		return n, 0, nil
	}, errs
}
