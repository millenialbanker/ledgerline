package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- paying a client by cheque when the electronic transfer failed ----
//
// Clients are paid electronically to their registered bank account; a
// physical instrument is issued only when the electronic transfer fails,
// and then the date the money is credited to the client counts as the
// settlement date (SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/
// 2025/90, paras 15.10.1.4-15.10.1.6). Funds kept in the DSCNBA for a
// cheque issued and not yet cleared are reported under RC05, and a cheque
// not cleared within five settlement days of issue has its funds
// upstreamed to the clearing corporation on the sixth settlement day
// (NSE Clearing NCL/CMPL/61186, Annexure 1, instruction 7; NSE/INSP/73976
// Exhibit 29). Ledgerline does that by stopping the cheque and cancelling
// it here: the payout is reversed, so the money is back in the client's
// credit and goes up with the day's allocation.
//
// The cheque is drawn on the DSCNBA (clients are paid only from it, para
// 15.3.2.2), in the client's name, for a payout request whose electronic
// transfer failed.

const cPayCheque = "paycheque"

// PayoutCheque is a cheque issued to a client for a payout.
type PayoutCheque struct {
	ID        string       `json:"id"` // PC-000001
	Request   string       `json:"request"`
	UCC       string       `json:"ucc"`
	Name      string       `json:"name"`
	Amount    money.Amount `json:"amount"`
	ChequeNo  string       `json:"cheque_no"`
	DrawnOn   string       `json:"drawn_on,omitempty"` // the DSCNBA (FB-01)
	IssuedOn  string       `json:"issued_on"`
	Why       string       `json:"why"` // why the electronic transfer failed
	Status    string       `json:"status"`
	ClearedOn string       `json:"cleared_on,omitempty"` // the date of credit: the settlement date
	Cancelled string       `json:"cancelled_on,omitempty"`
	CancelWhy string       `json:"cancel_why,omitempty"`
	Entry     string       `json:"entry,omitempty"`
	Reversal  string       `json:"reversal,omitempty"`
	By        string       `json:"by"`
	At        string       `json:"at"`
}

// Payout cheque statuses.
const (
	ChqIssued    = "Issued"
	ChqCleared   = "Cleared"
	ChqCancelled = "Cancelled"
)

// outstandingOn says whether the cheque's money was still in the DSCNBA at
// the end of date.
func (q PayoutCheque) outstandingOn(date string) bool {
	if q.IssuedOn == "" || q.IssuedOn > date {
		return false
	}
	if q.ClearedOn != "" && q.ClearedOn <= date {
		return false
	}
	return q.Cancelled == "" || q.Cancelled > date
}

func (o *Ops) payoutCheques(ctx context.Context, tenant string) []PayoutCheque {
	qs, _ := docs.List[PayoutCheque](ctx, o.Docs, tenant, cPayCheque, "")
	sort.SliceStable(qs, func(i, j int) bool { return qs[i].ID > qs[j].ID })
	return qs
}

// PayoutCheques lists the cheques issued, newest first.
func (o *Ops) PayoutCheques(ctx context.Context, tenant, actor string) ([]PayoutCheque, error) {
	if err := o.require(ctx, tenant, actor, "Viewing payouts", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return nil, err
	}
	qs := o.payoutCheques(ctx, tenant)
	if qs == nil {
		qs = []PayoutCheque{}
	}
	return qs, nil
}

// IssuePayoutCheque pays a payout request whose electronic transfer failed
// by a cheque drawn on the DSCNBA: the client's ledger is debited on the
// day it is issued; the settlement date is the day it clears.
func (o *Ops) IssuePayoutCheque(ctx context.Context, tenant, actor, request, chequeNo, date string) (PayoutCheque, error) {
	if err := o.require(ctx, tenant, actor, "Releasing payouts", RoleChecker); err != nil {
		return PayoutCheque{}, err
	}
	chequeNo = strings.TrimSpace(chequeNo)
	if chequeNo == "" || !validDate(date) {
		return PayoutCheque{}, invalid("give the cheque number and the day it is issued")
	}
	p, _, err := docs.Get[PayoutRequest](ctx, o.Docs, tenant, cPayout, request)
	if err != nil {
		return PayoutCheque{}, err
	}
	if p.Status != "Failed" {
		return PayoutCheque{}, fmt.Errorf("%w: a cheque is issued only when the electronic transfer failed (SEBI master circular 2025/90, para 15.10.1.5); request %s is %s", ErrState, p.ID, strings.ToLower(p.Status))
	}
	for _, q := range o.payoutCheques(ctx, tenant) {
		if q.Request == p.ID && q.Status != ChqCancelled {
			return q, fmt.Errorf("%w: cheque %s is already issued for %s", ErrState, q.ChequeNo, p.ID)
		}
	}
	c, err := o.Client(ctx, tenant, p.UCC)
	if err != nil {
		return PayoutCheque{}, err
	}
	if c.Deceased != nil {
		return PayoutCheque{}, fmt.Errorf("%w: %s", ErrState, deceasedWhy(c))
	}
	w, err := o.Withdrawable(ctx, tenant, date, p.UCC)
	if err != nil {
		return PayoutCheque{}, err
	}
	if p.Amount > w.Available {
		return PayoutCheque{}, invalid("only ₹%s is available to pay %s now; reject the request or ask for a smaller amount", formatINR(w.Available.Dec()), p.UCC)
	}
	if err := o.checkLock(ctx, tenant, date); err != nil {
		return PayoutCheque{}, err
	}
	drawn := ""
	if b, ok := defaultFirmBank(o.firmBanks(ctx, tenant), BankDSCNBA, "NSE"); ok {
		drawn = b.ID
	}
	n, err := o.next(ctx, tenant, "paycheque", actor)
	if err != nil {
		return PayoutCheque{}, err
	}
	q := PayoutCheque{ID: fmt.Sprintf("PC-%06d", n), Request: p.ID, UCC: p.UCC, Name: c.Name, Amount: p.Amount, ChequeNo: chequeNo, DrawnOn: drawn, IssuedOn: date,
		Why: firstOf(p.Reason, "electronic transfer failed"), Status: ChqIssued, By: actor, At: o.now().Format(time.RFC3339)}
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "PCHQ:" + q.ID, ValueDate: date, CreatedBy: actor,
		Narration: fmt.Sprintf("Funds payout %s by cheque %s (electronic transfer failed)", p.ID, chequeNo), Source: ledger.SourceRef{Kind: "PAYOUT", ID: p.ID},
		Postings: []ledger.Posting{{Account: ledger.ClientAccount(p.UCC), Debit: p.Amount, Memo: "Payout by cheque " + chequeNo}, {Account: ledger.BankClient, Credit: p.Amount}}})
	if err != nil {
		return q, err
	}
	q.Entry = e.ID
	if _, err := docs.Put(ctx, o.Docs, tenant, cPayCheque, q.ID, q, 0, actor); err != nil {
		return q, err
	}
	if _, err := docs.Update(ctx, o.Docs, tenant, cPayout, p.ID, actor, func(x *PayoutRequest) error {
		x.Status, x.Entry = "Cheque issued", e.ID
		return nil
	}); err != nil {
		return q, err
	}
	o.log(ctx, tenant, actor, "payout.cheque.issued", q.ID, map[string]any{"request": p.ID, "ucc": p.UCC, "amount": p.Amount.String(), "cheque": chequeNo})
	return q, nil
}

// ClearPayoutCheque records that the cheque was credited to the client on
// date: the settlement date of the payout.
func (o *Ops) ClearPayoutCheque(ctx context.Context, tenant, actor, id, date string) (PayoutCheque, error) {
	if err := o.require(ctx, tenant, actor, "Confirming payouts", RoleMaker, RoleChecker); err != nil {
		return PayoutCheque{}, err
	}
	if !validDate(date) {
		return PayoutCheque{}, invalid("give the day the cheque was credited to the client")
	}
	q, err := docs.Update(ctx, o.Docs, tenant, cPayCheque, id, actor, func(x *PayoutCheque) error {
		if x.Status != ChqIssued {
			return fmt.Errorf("%w: cheque %s is %s", ErrState, x.ChequeNo, strings.ToLower(x.Status))
		}
		if date < x.IssuedOn {
			return invalid("it cannot clear before it was issued (%s)", fmtDay(x.IssuedOn))
		}
		x.Status, x.ClearedOn = ChqCleared, date
		return nil
	})
	if err != nil {
		return q, err
	}
	docs.Update(ctx, o.Docs, tenant, cPayout, q.Request, actor, func(x *PayoutRequest) error {
		x.Status, x.UTR, x.SettledOn = "Paid", "Cheque "+q.ChequeNo, date
		return nil
	})
	o.closeWork(ctx, tenant, actor, "client_money", "cheque6|"+q.ID, "cleared on "+date)
	o.log(ctx, tenant, actor, "payout.cheque.cleared", q.ID, map[string]any{"request": q.Request, "cleared_on": date})
	o.notify(ctx, tenant, actor, "payout.paid", q.UCC, map[string]string{"amount": formatINR(q.Amount.Dec()), "utr": "cheque " + q.ChequeNo})
	return q, nil
}

// CancelPayoutCheque records a cheque stopped or returned: the payout is
// reversed to the client's ledger, so the money is upstreamed with the
// day's allocation (a cheque not cleared in five settlement days).
func (o *Ops) CancelPayoutCheque(ctx context.Context, tenant, actor, id, date, why string) (PayoutCheque, error) {
	if err := o.require(ctx, tenant, actor, "Releasing payouts", RoleChecker); err != nil {
		return PayoutCheque{}, err
	}
	if !validDate(date) || strings.TrimSpace(why) == "" {
		return PayoutCheque{}, invalid("give the day and why it is cancelled (stopped with the bank, returned)")
	}
	q, _, err := docs.Get[PayoutCheque](ctx, o.Docs, tenant, cPayCheque, id)
	if err != nil {
		return q, err
	}
	if q.Status != ChqIssued {
		return q, fmt.Errorf("%w: cheque %s is %s", ErrState, q.ChequeNo, strings.ToLower(q.Status))
	}
	rev, _, err := o.App.Ledger.Reverse(ctx, tenant, q.Entry, actor, "cheque "+q.ChequeNo+" cancelled: "+why, date)
	if err != nil {
		return q, err
	}
	q, err = docs.Update(ctx, o.Docs, tenant, cPayCheque, id, actor, func(x *PayoutCheque) error {
		x.Status, x.Cancelled, x.CancelWhy, x.Reversal = ChqCancelled, date, strings.TrimSpace(why), rev.ID
		return nil
	})
	if err != nil {
		return q, err
	}
	docs.Update(ctx, o.Docs, tenant, cPayout, q.Request, actor, func(x *PayoutRequest) error {
		x.Status, x.Reason = "Failed", "cheque "+q.ChequeNo+" cancelled: "+strings.TrimSpace(why)
		return nil
	})
	o.closeWork(ctx, tenant, actor, "client_money", "cheque6|"+q.ID, "cancelled")
	o.log(ctx, tenant, actor, "payout.cheque.cancelled", q.ID, map[string]any{"request": q.Request, "why": why})
	return q, nil
}
