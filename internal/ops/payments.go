package ops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- money in: receipts, cheques, payment gateway ----
//
// Branch and head-office staff record receipts that do not arrive through
// the bank statement match (cheques deposited, NEFT/RTGS/IMPS/UPI advised by
// the client). A maker records, a different checker approves; approval
// credits the client. Cheques stay "Uncleared" until the bank clears them:
// uncleared amounts do not count towards limits or payouts. A bounced cheque
// reverses the credit and can carry a bounce charge. Only the client's own
// registered bank account may pay in: SEBI does not allow third-party funds.
//
// Payment-gateway receipts arrive on a signed webhook and are credited at
// once against a gateway receivable, which the gateway's settlement into the
// client bank account clears.

const (
	cReceipt = "receipt"
	cPayout  = "payout"
	cPOBatch = "payout_batch"
	cGWSettl = "gw_settlement"
	// ReceivableGateway is money collected by the payment gateway and not
	// yet settled into the client bank account.
	ReceivableGateway = "RECEIVABLE:GATEWAY"
	// IncomeCharges is other charges recovered from clients (cheque bounce).
	IncomeCharges = "INCOME:CHARGES"
)

// ReceiptModes are the ways money may come in. Cash is not accepted.
var ReceiptModes = []string{"Cheque", "NEFT", "RTGS", "IMPS", "UPI", "Gateway"}

// Receipt is money received from a client outside the bank statement match.
type Receipt struct {
	ID          string       `json:"id"`
	Date        string       `json:"date"`
	UCC         string       `json:"ucc"`
	Name        string       `json:"name"`
	Mode        string       `json:"mode"`
	Ref         string       `json:"ref"` // cheque number, UTR or gateway payment id
	DraweeBank  string       `json:"drawee_bank,omitempty"`
	FromAccount string       `json:"from_account,omitempty"` // client's account or VPA
	Amount      money.Amount `json:"amount"`
	Branch      string       `json:"branch,omitempty"`
	Status      string       `json:"status"` // Pending, Rejected, Uncleared, Credited, Bounced
	Maker       string       `json:"maker"`
	Checker     string       `json:"checker,omitempty"`
	Entry       string       `json:"entry,omitempty"`
	ClearedOn   string       `json:"cleared_on,omitempty"`
	Note        string       `json:"note,omitempty"`
	Settled     string       `json:"settled,omitempty"` // gateway settlement reference
}

// Public masks the account.
func (r Receipt) Public() Receipt {
	if !strings.Contains(r.FromAccount, "@") {
		r.FromAccount = MaskAccount(r.FromAccount)
	}
	return r
}

func (s Settings) bounceCharge() money.Amount {
	if a, err := money.ParseAmount(s.ChequeBounceCharge); err == nil && s.ChequeBounceCharge != "" {
		return a
	}
	return 35000 // ₹350
}

// Receipts lists receipts, newest first.
func (o *Ops) Receipts(ctx context.Context, tenant string) ([]Receipt, error) {
	rs, err := docs.List[Receipt](ctx, o.Docs, tenant, cReceipt, "")
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].ID > rs[j].ID })
	for i := range rs {
		rs[i] = rs[i].Public()
	}
	return rs, err
}

// RecordReceipt records a receipt for a checker to approve.
func (o *Ops) RecordReceipt(ctx context.Context, tenant, actor string, r Receipt) (Receipt, error) {
	if err := o.require(ctx, tenant, actor, "Recording a receipt", RoleMaker, RoleChecker, RoleBranch); err != nil {
		return r, err
	}
	r.UCC = strings.ToUpper(strings.TrimSpace(r.UCC))
	c, err := o.Client(ctx, tenant, r.UCC)
	if err != nil {
		return r, invalid("client %s not found", r.UCC)
	}
	ok := false
	for _, m := range ReceiptModes {
		ok = ok || (m == r.Mode && m != "Gateway")
	}
	switch {
	case !ok:
		return r, invalid("mode must be Cheque, NEFT, RTGS, IMPS or UPI (cash is not accepted)")
	case r.Amount <= 0:
		return r, invalid("amount must be above zero")
	case strings.TrimSpace(r.Ref) == "":
		return r, invalid("give the cheque number or transfer reference (UTR)")
	case !ledger.ValidDate(r.Date):
		return r, invalid("date must be YYYY-MM-DD")
	}
	r.FromAccount = strings.TrimSpace(r.FromAccount)
	if r.Mode != "UPI" {
		if r.FromAccount == "" {
			r.FromAccount = c.Bank.Account
		}
		if r.FromAccount != c.Bank.Account {
			return r, invalid("account %s is not the client's registered bank account; third-party funds cannot be accepted", MaskAccount(r.FromAccount))
		}
	} else if !strings.Contains(r.FromAccount, "@") {
		return r, invalid("give the client's UPI ID (VPA) for a UPI receipt")
	}
	all, _ := docs.List[Receipt](ctx, o.Docs, tenant, cReceipt, "")
	for _, x := range all {
		if x.Ref == r.Ref && x.Mode == r.Mode && x.Status != "Rejected" {
			return r, invalid("%s %s is already recorded as %s", r.Mode, r.Ref, x.ID)
		}
	}
	n, err := o.next(ctx, tenant, "receipt", actor)
	if err != nil {
		return r, err
	}
	r.ID = fmt.Sprintf("RC-%06d", n)
	r.Name, r.Status, r.Maker, r.Checker, r.Entry = c.Name, "Pending", actor, "", ""
	if _, err := docs.Put(ctx, o.Docs, tenant, cReceipt, r.ID, r, 0, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "receipt.recorded", r.ID, map[string]any{"ucc": r.UCC, "mode": r.Mode, "amount": r.Amount.String()})
	return r.Public(), nil
}

func (o *Ops) postReceipt(ctx context.Context, tenant, actor string, r Receipt, debit string) (ledger.Entry, error) {
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "RCV:" + r.ID, ValueDate: r.Date, CreatedBy: actor,
		Narration: fmt.Sprintf("Funds received · %s %s", r.Mode, r.Ref), Source: ledger.SourceRef{Kind: "RECEIPT", ID: r.ID},
		Postings: []ledger.Posting{{Account: debit, Debit: r.Amount}, {Account: ledger.ClientAccount(r.UCC), Credit: r.Amount, Memo: r.Mode + " " + r.Ref}}})
	return e, err
}

// DecideReceipt approves or rejects a pending receipt. Approval credits the
// client; a cheque stays uncleared until cleared.
func (o *Ops) DecideReceipt(ctx context.Context, tenant, actor, id string, approve bool, note string) (Receipt, error) {
	if err := o.require(ctx, tenant, actor, "Approving a receipt", RoleChecker); err != nil {
		return Receipt{}, err
	}
	r, _, err := docs.Get[Receipt](ctx, o.Docs, tenant, cReceipt, id)
	if err != nil {
		return r, err
	}
	if r.Status != "Pending" {
		return r, fmt.Errorf("%w: receipt %s is %s", ErrState, id, r.Status)
	}
	if r.Maker == actor {
		return r, fmt.Errorf("%w: the maker of a receipt cannot approve it", ErrForbidden)
	}
	if !approve && strings.TrimSpace(note) == "" {
		return r, invalid("give the reason for rejecting")
	}
	status := "Rejected"
	var entry string
	if approve {
		e, err := o.postReceipt(ctx, tenant, actor, r, ledger.BankClient)
		if err != nil {
			return r, err
		}
		entry, status = e.ID, "Credited"
		if r.Mode == "Cheque" {
			status = "Uncleared"
		}
	}
	r, err = docs.Update(ctx, o.Docs, tenant, cReceipt, id, actor, func(x *Receipt) error {
		x.Status, x.Checker, x.Entry, x.Note = status, actor, entry, note
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "receipt."+strings.ToLower(status), id, map[string]any{"ucc": r.UCC, "amount": r.Amount.String()})
		if approve {
			o.notify(ctx, tenant, actor, "receipt.credited", r.UCC, map[string]string{"amount": formatINR(r.Amount.Dec()), "mode": r.Mode, "date": r.Date, "status": map[string]string{"Credited": "credited", "Uncleared": "credited subject to clearing"}[status]})
		}
	}
	return r.Public(), err
}

// ClearCheque marks an uncleared cheque cleared, or bounced. A bounce
// reverses the credit and, if charge is set, debits the bounce charge with
// GST and issues an invoice.
func (o *Ops) ClearCheque(ctx context.Context, tenant, actor, id, date string, bounced, charge bool, note string) (Receipt, error) {
	if err := o.require(ctx, tenant, actor, "Clearing cheques", RoleMaker, RoleChecker); err != nil {
		return Receipt{}, err
	}
	r, _, err := docs.Get[Receipt](ctx, o.Docs, tenant, cReceipt, id)
	if err != nil {
		return r, err
	}
	if r.Status != "Uncleared" {
		return r, fmt.Errorf("%w: receipt %s is %s", ErrState, id, r.Status)
	}
	if !ledger.ValidDate(date) || date < r.Date {
		return r, invalid("clearing date must be on or after the receipt date")
	}
	status := "Credited"
	if bounced {
		status = "Bounced"
		if strings.TrimSpace(note) == "" {
			return r, invalid("give the bank's return reason")
		}
		if _, _, err := o.App.Ledger.Reverse(ctx, tenant, r.Entry, actor, "cheque "+r.Ref+" returned: "+note, date); err != nil {
			return r, err
		}
		if charge {
			set, _ := o.Settings(ctx, tenant)
			amt := set.bounceCharge()
			gst := amt.Dec().Pct(set.gstRate()).Round(money.UnitPaisa)
			desc := "Cheque return charges · cheque " + r.Ref
			e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "BNC:" + r.ID, ValueDate: date, CreatedBy: actor, Narration: desc,
				Source:   ledger.SourceRef{Kind: "CHARGE", ID: r.ID},
				Postings: []ledger.Posting{{Account: ledger.ClientAccount(r.UCC), Debit: amt + gst}, {Account: IncomeCharges, Credit: amt}, {Account: ledger.PayableGST, Credit: gst}}})
			if err != nil {
				return r, err
			}
			if _, err := o.issueInvoice(ctx, tenant, actor, Invoice{Date: date, UCC: r.UCC, Name: r.Name, Kind: "Charges", Description: desc, SAC: "997158",
				Taxable: amt, GSTPct: set.gstRate(), GST: gst, Total: amt + gst, Entry: e.ID}); err != nil {
				return r, err
			}
		}
		o.raiseAlert(ctx, tenant, actor, Alert{Source: "Internal", Type: "Cheque returned", UCC: r.UCC, Severity: "Medium",
			Detail: fmt.Sprintf("Cheque %s for ₹%s returned: %s", r.Ref, formatINR(r.Amount.Dec()), note)}, "BNC:"+r.ID)
	}
	r, err = docs.Update(ctx, o.Docs, tenant, cReceipt, id, actor, func(x *Receipt) error {
		x.Status, x.ClearedOn = status, date
		if note != "" {
			x.Note = note
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "cheque."+strings.ToLower(status), id, map[string]any{"ucc": r.UCC, "amount": r.Amount.String()})
	}
	return r.Public(), err
}

// uncleared sums uncleared cheques per client.
func (o *Ops) uncleared(ctx context.Context, tenant string) map[string]money.Amount {
	out := map[string]money.Amount{}
	rs, _ := docs.List[Receipt](ctx, o.Docs, tenant, cReceipt, "")
	for _, r := range rs {
		if r.Status == "Uncleared" {
			out[r.UCC] += r.Amount
		}
	}
	return out
}

// GatewayPayment is the part of a gateway "payment.captured" event used here
// (Razorpay-style: amount in paise, client code in notes).
type GatewayPayment struct {
	Event   string `json:"event"`
	Payload struct {
		Payment struct {
			Entity struct {
				ID        string            `json:"id"`
				Amount    int64             `json:"amount"`
				Method    string            `json:"method"`
				VPA       string            `json:"vpa"`
				Notes     map[string]string `json:"notes"`
				CreatedAt int64             `json:"created_at"`
				Status    string            `json:"status"`
			} `json:"entity"`
		} `json:"payment"`
	} `json:"payload"`
}

// SignGateway is the webhook signature: hex HMAC-SHA256 of the raw body.
func SignGateway(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// GatewayWebhook verifies and records a captured gateway payment, crediting
// the client at once. Repeated deliveries of the same payment are ignored.
func (o *Ops) GatewayWebhook(ctx context.Context, tenant string, body []byte, signature string) (Receipt, bool, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return Receipt{}, false, err
	}
	if set.GatewaySecret == "" {
		return Receipt{}, false, fmt.Errorf("%w: payment gateway is not configured", ErrForbidden)
	}
	if !hmac.Equal([]byte(SignGateway(set.GatewaySecret, body)), []byte(strings.ToLower(signature))) {
		return Receipt{}, false, fmt.Errorf("%w: webhook signature does not match", ErrForbidden)
	}
	var ev GatewayPayment
	if err := json.Unmarshal(body, &ev); err != nil {
		return Receipt{}, false, invalid("webhook body is not valid JSON")
	}
	p := ev.Payload.Payment.Entity
	if ev.Event != "payment.captured" {
		return Receipt{}, false, nil // other events are acknowledged and ignored
	}
	if p.ID == "" || p.Amount <= 0 {
		return Receipt{}, false, invalid("payment id and amount are required")
	}
	id := "GW-" + p.ID
	if r, _, err := docs.Get[Receipt](ctx, o.Docs, tenant, cReceipt, id); err == nil {
		return r.Public(), false, nil
	}
	ucc := strings.ToUpper(p.Notes["ucc"])
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return Receipt{}, false, invalid("payment %s names unknown client %q", p.ID, ucc)
	}
	date := o.now().Format("2006-01-02")
	if p.CreatedAt > 0 {
		date = time.Unix(p.CreatedAt, 0).In(ist).Format("2006-01-02")
	}
	if d := p.Notes["value_date"]; ledger.ValidDate(d) {
		date = d
	}
	r := Receipt{ID: id, Date: date, UCC: c.UCC, Name: c.Name, Mode: "Gateway", Ref: p.ID, FromAccount: p.VPA, Amount: money.Amount(p.Amount),
		Status: "Credited", Maker: "gateway", Checker: "gateway", Note: "Paid online by " + p.Method}
	e, err := o.postReceipt(ctx, tenant, "gateway", r, ReceivableGateway)
	if err != nil {
		return r, false, err
	}
	r.Entry = e.ID
	if _, err := docs.Put(ctx, o.Docs, tenant, cReceipt, r.ID, r, 0, "gateway"); err != nil {
		return r, false, err
	}
	o.log(ctx, tenant, "gateway", "receipt.gateway", r.ID, map[string]any{"ucc": r.UCC, "amount": r.Amount.String()})
	o.notify(ctx, tenant, "gateway", "receipt.credited", r.UCC, map[string]string{"amount": formatINR(r.Amount.Dec()), "mode": "online payment", "date": r.Date, "status": "credited"})
	return r.Public(), true, nil
}

// SettleGateway records the gateway paying collected money into the
// client bank account: every unsettled gateway receipt up to date, less the
// gateway's fee (an expense of the firm).
func (o *Ops) SettleGateway(ctx context.Context, tenant, actor, date, ref string, fee money.Amount) (int, money.Amount, error) {
	if err := o.require(ctx, tenant, actor, "Gateway settlement", RoleChecker); err != nil {
		return 0, 0, err
	}
	if strings.TrimSpace(ref) == "" || fee < 0 {
		return 0, 0, invalid("give the settlement reference (and a fee of zero or more)")
	}
	rs, _ := docs.List[Receipt](ctx, o.Docs, tenant, cReceipt, "GW-")
	var gross money.Amount
	var ids []string
	for _, r := range rs {
		if r.Status == "Credited" && r.Settled == "" && r.Date <= date {
			gross += r.Amount
			ids = append(ids, r.ID)
		}
	}
	if gross == 0 {
		return 0, 0, fmt.Errorf("%w: no unsettled gateway receipts up to %s", ErrState, date)
	}
	ps := []ledger.Posting{{Account: ledger.BankClient, Debit: gross - fee, Memo: "Gateway settlement " + ref}, {Account: ReceivableGateway, Credit: gross}}
	if fee > 0 {
		// The fee is the firm's cost: the firm's own bank makes good the
		// shortfall in the client account.
		ps = append(ps, ledger.Posting{Account: "EXPENSE:GATEWAY_FEES", Debit: fee}, ledger.Posting{Account: ledger.BankClient, Debit: fee, Memo: "Fee made good from own funds"},
			ledger.Posting{Account: ledger.BankOwn, Credit: fee})
	}
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "GWS:" + ref, ValueDate: date, CreatedBy: actor, Narration: fmt.Sprintf("Payment gateway settlement %s · %d receipts", ref, len(ids)),
		Source: ledger.SourceRef{Kind: "GATEWAY", ID: ref}, Postings: ps}); err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		_, _ = docs.Update(ctx, o.Docs, tenant, cReceipt, id, actor, func(x *Receipt) error { x.Settled = ref; return nil })
	}
	o.log(ctx, tenant, actor, "gateway.settled", ref, map[string]any{"receipts": len(ids), "gross": gross.String(), "fee": fee.String()})
	return len(ids), gross, nil
}

// ---- money out: payout requests and bank payout batches ----

// PayoutRequest is a client's request to withdraw funds.
type PayoutRequest struct {
	ID        string       `json:"id"`
	Date      string       `json:"date"`
	UCC       string       `json:"ucc"`
	Name      string       `json:"name"`
	Amount    money.Amount `json:"amount"`
	Source    string       `json:"source"` // Client, Branch, Head office
	Status    string       `json:"status"` // Requested, Approved, Rejected, Paid, Failed, Expired, Cancelled
	Available money.Amount `json:"available"`
	Maker     string       `json:"maker"`
	Checker   string       `json:"checker,omitempty"`
	Reason    string       `json:"reason,omitempty"`
	Batch     string       `json:"batch,omitempty"`
	Entry     string       `json:"entry,omitempty"`
	UTR       string       `json:"utr,omitempty"`
	ExpiresOn string       `json:"expires_on"`
}

// Withdrawable explains how much a client can take out today.
type Withdrawable struct {
	UCC        string       `json:"ucc"`
	Ledger     money.Amount `json:"ledger"`    // credit positive, incl. today's bills
	Margin     money.Amount `json:"margin"`    // margin to be kept in cash after collateral
	Uncleared  money.Amount `json:"uncleared"` // cheques not yet cleared
	Accrued    money.Amount `json:"accrued"`   // interest accrued and not posted
	Pending    money.Amount `json:"pending"`   // approved payouts not yet paid
	Available  money.Amount `json:"available"` // what may be paid out
	Note       string       `json:"note,omitempty"`
	Blocked    bool         `json:"blocked"`
	BlockedWhy string       `json:"blocked_why,omitempty"`
}

// Withdrawable computes, across all segments, the funds a client may
// withdraw: ledger credit less margin not covered by collateral, uncleared
// cheques, unposted interest and payouts already approved.
func (o *Ops) Withdrawable(ctx context.Context, tenant, date, ucc string) (Withdrawable, error) {
	w := Withdrawable{UCC: ucc}
	funds, err := o.clientFunds(ctx, tenant, date, "")
	if err != nil {
		return w, err
	}
	w.Ledger = funds[ucc]
	if rv, err := o.Risk(ctx, tenant, date); err == nil {
		for _, r := range rv.Rows {
			if r.UCC == ucc {
				m := money.Max(r.Required.Sub(r.CollUsable), money.Dec{})
				w.Margin = m.Round(money.UnitPaisa)
			}
		}
	} else {
		w.Note = "Margin could not be computed (" + err.Error() + "); payouts held"
		w.Blocked, w.BlockedWhy = true, "risk not available"
	}
	w.Uncleared = o.uncleared(ctx, tenant)[ucc]
	if iv, err := o.Interest(ctx, tenant, date[:8]+"01", date); err == nil {
		for _, r := range iv.Rows {
			if r.UCC == ucc && (r.Status == "To post" || r.Status == "Below minimum") {
				w.Accrued = r.Total
			}
		}
	}
	prs, _ := docs.List[PayoutRequest](ctx, o.Docs, tenant, cPayout, "")
	for _, p := range prs {
		if p.UCC == ucc && p.Status == "Approved" && p.Batch == "" {
			w.Pending += p.Amount
		}
	}
	w.Available = w.Ledger - w.Margin - w.Uncleared - w.Accrued - w.Pending
	if w.Blocked || w.Available < 0 {
		w.Available = 0
	}
	return w, nil
}

// PayoutRequests lists requests, newest first; expired requests are
// marked as such.
func (o *Ops) PayoutRequests(ctx context.Context, tenant, date string) ([]PayoutRequest, error) {
	ps, err := docs.List[PayoutRequest](ctx, o.Docs, tenant, cPayout, "")
	for i := range ps {
		if ps[i].Status == "Requested" && date > ps[i].ExpiresOn {
			ps[i].Status = "Expired"
		}
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].ID > ps[j].ID })
	return ps, err
}

// RequestPayout records a withdrawal request. It is valid for two days.
func (o *Ops) RequestPayout(ctx context.Context, tenant, actor, date, ucc string, amount money.Amount, source string) (PayoutRequest, error) {
	ucc = strings.ToUpper(ucc)
	if source != "Client" {
		if err := o.require(ctx, tenant, actor, "Entering a payout request", RoleMaker, RoleChecker, RoleBranch, RoleAP); err != nil {
			return PayoutRequest{}, err
		}
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return PayoutRequest{}, invalid("client %s not found", ucc)
	}
	if amount <= 0 {
		return PayoutRequest{}, invalid("amount must be above zero")
	}
	w, err := o.Withdrawable(ctx, tenant, date, ucc)
	if err != nil {
		return PayoutRequest{}, err
	}
	if amount > w.Available {
		return PayoutRequest{}, invalid("₹%s is more than the ₹%s available to withdraw", formatINR(amount.Dec()), formatINR(w.Available.Dec()))
	}
	n, err := o.next(ctx, tenant, "payout", actor)
	if err != nil {
		return PayoutRequest{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	p := PayoutRequest{ID: fmt.Sprintf("PO-%06d", n), Date: date, UCC: ucc, Name: c.Name, Amount: amount, Source: source, Status: "Requested",
		Available: w.Available, Maker: actor, ExpiresOn: set.NextBusinessDay(set.NextBusinessDay(date))}
	if _, err := docs.Put(ctx, o.Docs, tenant, cPayout, p.ID, p, 0, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "payout.requested", p.ID, map[string]any{"ucc": ucc, "amount": amount.String(), "source": source})
	if _, mode, _ := o.policy(ctx, tenant, "payouts"); mode != ModeOff {
		if _, err := o.RunStage(ctx, tenant, SystemActor, date, "", "payouts"); err == nil {
			p, _, _ = docs.Get[PayoutRequest](ctx, o.Docs, tenant, cPayout, p.ID)
		}
	}
	return p, nil
}

// DecidePayout approves (after re-checking the funds) or rejects a request.
func (o *Ops) DecidePayout(ctx context.Context, tenant, actor, date, id string, approve bool, reason string) (PayoutRequest, error) {
	if err := o.require(ctx, tenant, actor, "Approving payouts", RoleChecker, RoleRisk); err != nil {
		return PayoutRequest{}, err
	}
	p, _, err := docs.Get[PayoutRequest](ctx, o.Docs, tenant, cPayout, id)
	if err != nil {
		return p, err
	}
	if p.Status != "Requested" {
		return p, fmt.Errorf("%w: request %s is %s", ErrState, id, p.Status)
	}
	if date > p.ExpiresOn {
		return p, fmt.Errorf("%w: request %s expired on %s; the client must request again", ErrState, id, p.ExpiresOn)
	}
	if p.Maker == actor {
		return p, fmt.Errorf("%w: the person who entered a payout cannot approve it", ErrForbidden)
	}
	status := "Rejected"
	if approve {
		w, err := o.Withdrawable(ctx, tenant, date, p.UCC)
		if err != nil {
			return p, err
		}
		if p.Amount > w.Available {
			return p, invalid("only ₹%s is available now (ledger %s, margin %s, uncleared %s, interest %s, other approved payouts %s); reject or ask for a smaller amount",
				formatINR(w.Available.Dec()), w.Ledger, w.Margin, w.Uncleared, w.Accrued, w.Pending)
		}
		status = "Approved"
	} else if strings.TrimSpace(reason) == "" {
		return p, invalid("give the reason for rejecting")
	}
	p, err = docs.Update(ctx, o.Docs, tenant, cPayout, id, actor, func(x *PayoutRequest) error {
		x.Status, x.Checker, x.Reason = status, actor, reason
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "payout."+strings.ToLower(status), id, map[string]any{"ucc": p.UCC, "amount": p.Amount.String()})
		if !approve {
			o.notify(ctx, tenant, actor, "payout.rejected", p.UCC, map[string]string{"amount": formatINR(p.Amount.Dec()), "reason": reason})
		}
	}
	return p, err
}

// PayoutBatch is one bank upload of payouts.
type PayoutBatch struct {
	ID      string         `json:"id"`
	Date    string         `json:"date"`
	Items   []PayoutItem   `json:"items"`
	Total   money.Amount   `json:"total"`
	Status  string         `json:"status"` // Sent to bank, Confirmed
	By      string         `json:"by"`
	Results map[string]int `json:"results,omitempty"`
}

// PayoutItem is one transfer in a batch.
type PayoutItem struct {
	Request string       `json:"request"`
	UCC     string       `json:"ucc"`
	Name    string       `json:"name"`
	Bank    string       `json:"bank"`
	IFSC    string       `json:"ifsc"`
	Account string       `json:"account"`
	Amount  money.Amount `json:"amount"`
	Mode    string       `json:"mode"` // NEFT or RTGS (₹2 lakh and above)
	Entry   string       `json:"entry"`
	Status  string       `json:"status"` // Sent, Paid, Failed
	UTR     string       `json:"utr,omitempty"`
}

// CreatePayoutBatch pays every approved request: debits each client,
// credits the client bank account and produces the bank upload.
func (o *Ops) CreatePayoutBatch(ctx context.Context, tenant, actor, date string) (PayoutBatch, error) {
	if err := o.require(ctx, tenant, actor, "Releasing payouts", RoleChecker); err != nil {
		return PayoutBatch{}, err
	}
	prs, err := docs.List[PayoutRequest](ctx, o.Docs, tenant, cPayout, "")
	if err != nil {
		return PayoutBatch{}, err
	}
	n, err := o.next(ctx, tenant, "payout-batch", actor)
	if err != nil {
		return PayoutBatch{}, err
	}
	b := PayoutBatch{ID: fmt.Sprintf("PB-%06d", n), Date: date, Status: "Sent to bank", By: actor, Items: []PayoutItem{}}
	for _, p := range prs {
		if p.Status != "Approved" || p.Batch != "" {
			continue
		}
		c, err := o.Client(ctx, tenant, p.UCC)
		if err != nil {
			continue
		}
		mode := "NEFT"
		if p.Amount >= 20000000 {
			mode = "RTGS"
		}
		e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "PAY:" + p.ID, ValueDate: date, CreatedBy: actor,
			Narration: fmt.Sprintf("Funds payout %s to %s %s", p.ID, c.Bank.Bank, MaskAccount(c.Bank.Account)), Source: ledger.SourceRef{Kind: "PAYOUT", ID: p.ID},
			Postings: []ledger.Posting{{Account: ledger.ClientAccount(p.UCC), Debit: p.Amount, Memo: "Payout on request"}, {Account: ledger.BankClient, Credit: p.Amount}}})
		if err != nil {
			return b, err
		}
		b.Items = append(b.Items, PayoutItem{Request: p.ID, UCC: p.UCC, Name: c.Name, Bank: c.Bank.Bank, IFSC: c.Bank.IFSC, Account: c.Bank.Account, Amount: p.Amount, Mode: mode, Entry: e.ID, Status: "Sent"})
		b.Total += p.Amount
		if _, err := docs.Update(ctx, o.Docs, tenant, cPayout, p.ID, actor, func(x *PayoutRequest) error {
			x.Batch, x.Entry = b.ID, e.ID
			return nil
		}); err != nil {
			return b, err
		}
	}
	if len(b.Items) == 0 {
		return b, fmt.Errorf("%w: no approved payouts to release", ErrState)
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cPOBatch, b.ID, b, 0, actor); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "payout.batch", b.ID, map[string]any{"items": len(b.Items), "total": b.Total.String()})
	return b, nil
}

// PayoutBatches lists batches, newest first.
func (o *Ops) PayoutBatches(ctx context.Context, tenant string) ([]PayoutBatch, error) {
	bs, err := docs.List[PayoutBatch](ctx, o.Docs, tenant, cPOBatch, "")
	sort.SliceStable(bs, func(i, j int) bool { return bs[i].ID > bs[j].ID })
	return bs, err
}

// PayoutFile is the bank's bulk transfer upload for a batch.
func (o *Ops) PayoutFile(ctx context.Context, tenant, id string) ([]byte, error) {
	b, _, err := docs.Get[PayoutBatch](ctx, o.Docs, tenant, cPOBatch, id)
	if err != nil {
		return nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	rows := [][]string{{"TxnType", "DebitAccount", "BeneficiaryName", "BeneficiaryAccount", "IFSC", "Amount", "ValueDate", "Reference", "Narration"}}
	for _, it := range b.Items {
		rows = append(rows, []string{it.Mode, set.PayoutDebitAccount, it.Name, it.Account, it.IFSC, it.Amount.String(), b.Date, it.Request, "Payout " + it.UCC})
	}
	return CSV(rows), nil
}

// ConfirmPayouts records the bank's result for a batch: UTRs for paid
// transfers; failed transfers are reversed back to the client.
func (o *Ops) ConfirmPayouts(ctx context.Context, tenant, actor, id string, utrs map[string]string, failed map[string]string) (PayoutBatch, error) {
	if err := o.require(ctx, tenant, actor, "Confirming payouts", RoleMaker, RoleChecker); err != nil {
		return PayoutBatch{}, err
	}
	b, _, err := docs.Get[PayoutBatch](ctx, o.Docs, tenant, cPOBatch, id)
	if err != nil {
		return b, err
	}
	if b.Status == "Confirmed" {
		return b, fmt.Errorf("%w: batch %s is already confirmed", ErrState, id)
	}
	res := map[string]int{"Paid": 0, "Failed": 0}
	for i, it := range b.Items {
		status, utr := "Paid", utrs[it.Request]
		if why, bad := failed[it.Request]; bad {
			status = "Failed"
			if _, _, err := o.App.Ledger.Reverse(ctx, tenant, it.Entry, actor, "payout returned by bank: "+why, b.Date); err != nil {
				return b, err
			}
		} else if utr == "" {
			return b, invalid("give the UTR for %s or mark it failed", it.Request)
		}
		b.Items[i].Status, b.Items[i].UTR = status, utr
		res[status]++
		pr, _ := docs.Update(ctx, o.Docs, tenant, cPayout, it.Request, actor, func(x *PayoutRequest) error {
			x.Status, x.UTR = status, utr
			if status == "Failed" {
				x.Reason = failed[it.Request]
			}
			return nil
		})
		if status == "Paid" {
			o.notify(ctx, tenant, actor, "payout.paid", pr.UCC, map[string]string{"amount": formatINR(pr.Amount.Dec()), "utr": utr})
		} else {
			o.notify(ctx, tenant, actor, "payout.failed", pr.UCC, map[string]string{"amount": formatINR(pr.Amount.Dec()), "reason": failed[it.Request]})
		}
	}
	b.Status, b.Results = "Confirmed", res
	if err := docs.Upsert(ctx, o.Docs, tenant, cPOBatch, b.ID, b, actor); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "payout.confirmed", b.ID, map[string]any{"paid": res["Paid"], "failed": res["Failed"]})
	return b, nil
}
