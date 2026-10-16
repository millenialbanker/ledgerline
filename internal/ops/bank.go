package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// BankTxn is one credit on the client bank account statement.
type BankTxn struct {
	ID       string       `json:"id"`
	Date     string       `json:"date"`
	Time     string       `json:"time"`
	Mode     string       `json:"mode"`
	Ref      string       `json:"ref"`
	Remitter string       `json:"remitter"`
	Account  string       `json:"account"` // remitter account, stored in full
	Amount   money.Amount `json:"amount"`
	Status   string       `json:"status"` // Matched, Third-party, Unidentified, Returned, Identified
	UCC      string       `json:"ucc,omitempty"`
	EntryID  string       `json:"entry_id,omitempty"`
	Note     string       `json:"note,omitempty"`
	By       string       `json:"by,omitempty"`
}

// Public masks the remitter account.
func (b BankTxn) Public() BankTxn {
	if b.Account != "VPA" {
		b.Account = MaskAccount(b.Account)
	}
	return b
}

// BankTxns lists statement credits, newest first.
func (o *Ops) BankTxns(ctx context.Context, tenant string) ([]BankTxn, error) {
	ts, err := docs.List[BankTxn](ctx, o.Docs, tenant, cBankTxn, "")
	sort.SliceStable(ts, func(i, j int) bool {
		if ts[i].Date != ts[j].Date {
			return ts[i].Date > ts[j].Date
		}
		return ts[i].Time > ts[j].Time
	})
	return ts, err
}

// matchCredit classifies and posts one statement credit. A credit from a
// client's registered account is a receipt. Anything else is parked in the
// bank suspense account until it is returned or identified: third-party if
// the remitter name resembles a client, unidentified otherwise.
func (o *Ops) matchCredit(ctx context.Context, tenant, actor string, t BankTxn, clients []Client) (BankTxn, error) {
	var byAcct *Client
	for i := range clients {
		if clients[i].Bank.Account != "" && clients[i].Bank.Account == t.Account {
			byAcct = &clients[i]
		}
	}
	entry := ledger.Entry{Tenant: tenant, ValueDate: t.Date, CreatedBy: actor, Source: ledger.SourceRef{Kind: "BANK", ID: t.Ref}}
	if byAcct != nil {
		t.Status, t.UCC = "Matched", byAcct.UCC
		entry.Key = "RCPT:" + t.Ref
		entry.Narration = fmt.Sprintf("Funds received · %s %s", t.Mode, t.Ref)
		entry.Postings = []ledger.Posting{{Account: ledger.BankClient, Debit: t.Amount}, {Account: ledger.ClientAccount(byAcct.UCC), Credit: t.Amount, Memo: "Receipt from registered account"}}
	} else {
		t.Status = "Unidentified"
		low := strings.ToLower(t.Remitter)
		for _, c := range clients {
			last := strings.ToLower(lastWord(c.Name))
			if len(last) > 2 && strings.Contains(low, last) && t.Account != "VPA" {
				t.Status, t.UCC = "Third-party", c.UCC
				break
			}
		}
		entry.Key = "SUSP:" + t.Ref
		entry.Narration = fmt.Sprintf("Unmatched credit parked in suspense · %s %s", t.Mode, t.Ref)
		entry.Postings = []ledger.Posting{{Account: ledger.BankClient, Debit: t.Amount}, {Account: ledger.SuspenseBank, Credit: t.Amount, Memo: t.Remitter}}
	}
	e, _, err := o.App.Ledger.Post(ctx, entry)
	if err != nil {
		return t, err
	}
	t.EntryID = e.ID
	if t.Status == "Third-party" {
		o.raiseAlert(ctx, tenant, actor, Alert{Source: "Internal", Type: "Third-party funds attempt", UCC: t.UCC, Severity: "Medium",
			Detail: fmt.Sprintf("%s credit of ₹%s from an unregistered account (%s, %s)", t.Mode, formatINR(t.Amount.Dec()), t.Remitter, MaskAccount(t.Account))}, "TPF:"+t.Ref)
	}
	return t, nil
}

func lastWord(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	w := f[len(f)-1]
	if strings.EqualFold(w, "HUF") && len(f) > 1 {
		return f[0]
	}
	return w
}

// ReturnCredit sends an unmatched credit back to the remitter.
func (o *Ops) ReturnCredit(ctx context.Context, tenant, actor, id, note string) (BankTxn, error) {
	if err := o.require(ctx, tenant, actor, "Returning a bank credit", RoleMaker, RoleChecker); err != nil {
		return BankTxn{}, err
	}
	t, _, err := docs.Get[BankTxn](ctx, o.Docs, tenant, cBankTxn, id)
	if err != nil {
		return t, err
	}
	if t.Status != "Third-party" && t.Status != "Unidentified" {
		return t, fmt.Errorf("%w: credit %s is %s", ErrState, t.Ref, t.Status)
	}
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "RET:" + t.Ref, ValueDate: o.now().Format("2006-01-02"),
		Narration: "Returned to remitter · " + t.Ref, Source: ledger.SourceRef{Kind: "BANK", ID: t.Ref}, CreatedBy: actor,
		Postings: []ledger.Posting{{Account: ledger.SuspenseBank, Debit: t.Amount}, {Account: ledger.BankClient, Credit: t.Amount, Memo: "Refund to source account"}}})
	if err != nil {
		return t, err
	}
	t, err = docs.Update(ctx, o.Docs, tenant, cBankTxn, id, actor, func(x *BankTxn) error {
		x.Status, x.Note, x.By, x.EntryID = "Returned", note, actor, e.ID
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "bank.credit.returned", t.Ref, map[string]any{"amount": t.Amount.String()})
	}
	return t, err
}

// IdentifyCredit credits an unidentified receipt to a client after the bank
// confirms the remitting account belongs to that client. The account is then
// added to the client's record as verified.
func (o *Ops) IdentifyCredit(ctx context.Context, tenant, actor, id, ucc string, bankConfirmed bool) (BankTxn, error) {
	if err := o.require(ctx, tenant, actor, "Identifying a bank credit", RoleChecker); err != nil {
		return BankTxn{}, err
	}
	if !bankConfirmed {
		return BankTxn{}, invalid("the bank must confirm the remitting account belongs to the client; otherwise return the funds")
	}
	t, _, err := docs.Get[BankTxn](ctx, o.Docs, tenant, cBankTxn, id)
	if err != nil {
		return t, err
	}
	if t.Status != "Unidentified" {
		return t, fmt.Errorf("%w: only unidentified credits can be identified; third-party credits must be returned", ErrState)
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return t, err
	}
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "IDN:" + t.Ref, ValueDate: o.now().Format("2006-01-02"),
		Narration: "Identified receipt · " + t.Ref, Source: ledger.SourceRef{Kind: "BANK", ID: t.Ref}, CreatedBy: actor,
		Postings: []ledger.Posting{{Account: ledger.SuspenseBank, Debit: t.Amount}, {Account: ledger.ClientAccount(c.UCC), Credit: t.Amount, Memo: "Receipt identified with bank confirmation"}}})
	if err != nil {
		return t, err
	}
	t, err = docs.Update(ctx, o.Docs, tenant, cBankTxn, id, actor, func(x *BankTxn) error {
		x.Status, x.UCC, x.By, x.EntryID = "Identified", c.UCC, actor, e.ID
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "bank.credit.identified", t.Ref, map[string]any{"ucc": c.UCC, "amount": t.Amount.String()})
	}
	return t, err
}

// Upstream moves client money from the bank to the clearing corporation.
func (o *Ops) Upstream(ctx context.Context, tenant, actor, date string, amount money.Amount, ref string) (ledger.Entry, error) {
	if err := o.require(ctx, tenant, actor, "Upstreaming funds", RoleChecker); err != nil {
		return ledger.Entry{}, err
	}
	if amount <= 0 || strings.TrimSpace(ref) == "" {
		return ledger.Entry{}, invalid("give an amount above zero and the transfer reference")
	}
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "UPS:" + ref, ValueDate: date, Narration: "Client funds upstreamed to clearing corporation · " + ref,
		Source: ledger.SourceRef{Kind: "BANK", ID: ref}, CreatedBy: actor,
		Postings: []ledger.Posting{{Account: ledger.CCCollateral, Debit: amount}, {Account: ledger.BankClient, Credit: amount}}})
	if err == nil {
		o.log(ctx, tenant, actor, "funds.upstreamed", ref, map[string]any{"amount": amount.String()})
	}
	return e, err
}

// FundsPosition is the client-money segregation check.
type FundsPosition struct {
	ClientBank    money.Dec `json:"client_bank"`
	WithCC        money.Dec `json:"with_cc"`
	Suspense      money.Dec `json:"suspense"`
	ClientCredits money.Dec `json:"client_credits"`
	ClientDebits  money.Dec `json:"client_debits"`
	Cover         float64   `json:"cover"`
	OK            bool      `json:"ok"`
}

// Funds computes the segregation check as of a date.
func (o *Ops) Funds(ctx context.Context, tenant, asOf string) (FundsPosition, error) {
	bal, err := o.App.Ledger.Balances(ctx, tenant, asOf)
	if err != nil {
		return FundsPosition{}, err
	}
	f := FundsPosition{ClientBank: bal[ledger.BankClient].Dec(), WithCC: bal[ledger.CCCollateral].Dec(), Suspense: (-bal[ledger.SuspenseBank]).Dec()}
	for k, v := range bal {
		if strings.HasPrefix(k, "CLIENT:") {
			if v < 0 {
				f.ClientCredits = f.ClientCredits.Add((-v).Dec())
			} else {
				f.ClientDebits = f.ClientDebits.Add(v.Dec())
			}
		}
	}
	held := f.ClientBank.Add(f.WithCC).Sub(f.Suspense)
	if f.ClientCredits.Sign() > 0 {
		f.Cover = held.Float64() / f.ClientCredits.Float64()
	} else {
		f.Cover = 1
	}
	f.OK = held.Cmp(f.ClientCredits) >= 0
	return f, nil
}
