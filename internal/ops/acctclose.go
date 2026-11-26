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

// ---- closing a trading account on the client's request ----
//
// NSE's inspection master circular NSE/INSP/73976, item 1.15:
//
//   - (iv) acknowledge the closure request and stop taking trades from the
//     client from its receipt;
//   - (v), (viii) close only when there is no debit balance across the
//     exchanges, no open position and no pending arbitration or blocking
//     order; otherwise write to the client to clear them;
//   - (vi) on a valid request, return the client's funds, release all
//     collateral and pledged securities, and close the account within 3
//     working days.
//
// A closure request here starts the 3-working-day clock: the client is
// blocked from trading at once and gets an acknowledgement; each step is
// checked against the books (the credit balance paid out, every pledge,
// unpaid-securities line and clearing allocation released, then every
// segment closed) and dated when it is done. The closure letter goes only
// once the pay-out and the release are done, and says so from the books,
// not as a form of words.

const cClosure = "acctclosure" // request ID -> AccountClosure

// AccountClosure is a client's request to close the trading account.
type AccountClosure struct {
	ID       string `json:"id"`
	UCC      string `json:"ucc"`
	Name     string `json:"name"`
	Received string `json:"received"` // date the request was received
	Due      string `json:"due"`      // 3 working days later
	Source   string `json:"source"`   // Client, Branch, Online, Registered e-mail (ClosureSources)
	// FromEmail is the address an e-mailed request came from.
	FromEmail string `json:"from_email,omitempty"`
	Note      string `json:"note,omitempty"`
	// Status: Open (being worked), Waiting for client (a debit balance,
	// open positions or a blocking order to clear first), Closed,
	// Rejected.
	Status string `json:"status"`
	// Holds are what stands in the way at the last check.
	Holds []string `json:"holds"`
	// PaidOutOn, ReleasedOn and ClosedOn date the three steps.
	PaidOutOn  string `json:"paid_out_on,omitempty"`
	ReleasedOn string `json:"released_on,omitempty"`
	ClosedOn   string `json:"closed_on,omitempty"`
	LetterOn   string `json:"letter_on,omitempty"`
	AckOn      string `json:"ack_on,omitempty"`
	By         string `json:"by"`
	ClosedBy   string `json:"closed_by,omitempty"`
	Reason     string `json:"reason,omitempty"` // why rejected
	Checked    string `json:"checked,omitempty"`
}

// closureDue is three working days after the request.
func closureDue(set Settings, received string) string {
	d := received
	for range 3 {
		d = set.NextBusinessDay(d)
	}
	return d
}

// closureState is what the books say about a client being closed.
type closureState struct {
	credit, debit money.Dec
	payouts       int // payout requests not yet paid
	positions     int
	pledged       []string
	cuspa         int
	allocated     money.Dec
}

func (o *Ops) closureBooks(ctx context.Context, tenant, ucc, date string) closureState {
	var st closureState
	if f, err := o.clientFunds(ctx, tenant, date, ""); err == nil {
		switch b := f[ucc].Dec(); {
		case b.Sign() > 0:
			st.credit = b
		case b.Sign() < 0:
			st.debit = b.Neg()
		}
	}
	prs, _ := docs.List[PayoutRequest](ctx, o.Docs, tenant, cPayout, "")
	for _, p := range prs {
		if p.UCC == ucc && (p.Status == "Requested" || p.Status == "Approved") {
			st.payouts++
		}
	}
	set, _ := o.Settings(ctx, tenant)
	ps, _ := o.carried(ctx, tenant, set.NextBusinessDay(date))
	for _, p := range ps {
		if p.UCC == ucc && p.Qty != 0 {
			st.positions++
		}
	}
	hs, _ := o.Holdings(ctx, tenant, ucc)
	for _, h := range hs {
		if h.Pledged.Sign() > 0 {
			st.pledged = append(st.pledged, firstOf(h.Symbol, h.ISIN))
		}
	}
	cu, _ := docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
	for _, it := range cu {
		if it.UCC == ucc && it.Qty > 0 && it.Status == CUSPAHeld {
			st.cuspa++
		}
	}
	for k, l := range o.allocLevels(ctx, tenant) {
		if strings.HasSuffix(k, "|"+ucc) {
			st.allocated = st.allocated.Add(l.Amount)
		}
	}
	return st
}

// RequestClosure records a closure request: the client is blocked from
// trading at once, acknowledged, and the 3-working-day clock starts.
func (o *Ops) RequestClosure(ctx context.Context, tenant, actor, ucc, received, source, note string) (AccountClosure, error) {
	return o.RequestClosureFrom(ctx, tenant, actor, ucc, received, source, note, "")
}

// ClosureSources are the ways a closure request may come (NSE/INSP/73976
// item 1.15(iii)): signed and given in person or by post ("Client",
// "Branch", "Head office"), online through the firm's portal or app with two-factor
// authentication ("Online"), or from the client's registered e-mail.
// A request by SMS or a messaging app is not accepted.
var ClosureSources = []string{"Client", "Branch", "Head office", "Online", ClosureByEmail}

// ClosureByEmail is a request from the client's registered e-mail.
const ClosureByEmail = "Registered e-mail"

// RequestClosureFrom is RequestClosure with the address an e-mailed
// request came from, which must be the client's registered e-mail.
func (o *Ops) RequestClosureFrom(ctx context.Context, tenant, actor, ucc, received, source, note, fromEmail string) (AccountClosure, error) {
	if err := o.require(ctx, tenant, actor, "Recording an account closure request", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return AccountClosure{}, err
	}
	source = firstOf(strings.TrimSpace(source), "Client")
	if l := strings.ToLower(source); strings.Contains(l, "sms") || strings.Contains(l, "whatsapp") || strings.Contains(l, "telegram") || strings.Contains(l, "messag") {
		return AccountClosure{}, invalid("a closure request by SMS or a messaging app is not accepted: ask the client to use the portal or app, write from the registered e-mail, or sign a request (NSE/INSP/73976 item 1.15(iii))")
	}
	if !contains(ClosureSources, source) {
		return AccountClosure{}, invalid("the request comes signed (Client or Branch), Online (portal or app with two-factor login) or from the %s", strings.ToLower(ClosureByEmail))
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if received == "" {
		received = o.now().In(ist).Format("2006-01-02")
	}
	if !validDate(received) {
		return AccountClosure{}, invalid("give the date the request was received (YYYY-MM-DD)")
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return AccountClosure{}, err
	}
	if source == ClosureByEmail && (strings.TrimSpace(fromEmail) == "" || !strings.EqualFold(strings.TrimSpace(fromEmail), strings.TrimSpace(c.Email))) {
		return AccountClosure{}, invalid("an e-mailed closure request is accepted only from the client's registered e-mail address")
	}
	xs, _ := docs.List[AccountClosure](ctx, o.Docs, tenant, cClosure, "")
	for _, x := range xs {
		if x.UCC == ucc && (x.Status == "Open" || x.Status == "Waiting for client") {
			return x, fmt.Errorf("%w: closure request %s for %s is already open", ErrState, x.ID, ucc)
		}
	}
	set, _ := o.Settings(ctx, tenant)
	n, err := o.next(ctx, tenant, "closure", actor)
	if err != nil {
		return AccountClosure{}, err
	}
	x := AccountClosure{ID: fmt.Sprintf("CL-%05d", n), UCC: ucc, Name: c.Name, Received: received, Due: closureDue(set, received), Source: source, FromEmail: strings.ToLower(strings.TrimSpace(fromEmail)),
		Note: strings.TrimSpace(note), Status: "Open", Holds: []string{}, By: actor}
	if _, err := docs.Put(ctx, o.Docs, tenant, cClosure, x.ID, x, 0, actor); err != nil {
		return x, err
	}
	// No further trades from the receipt of the request (item 1.15(iv)).
	if c.Status == "Active" && !c.Blocked {
		_, _ = docs.Update(ctx, o.Docs, tenant, cClient, ucc, actor, func(cl *Client) error {
			cl.Blocked, cl.BlockedReason = true, "Account closure requested ("+x.ID+")"
			return nil
		})
		o.log(ctx, tenant, actor, "rms.kill.on", ucc, map[string]any{"reason": "account closure " + x.ID})
	}
	o.log(ctx, tenant, actor, "closure.requested", x.ID, map[string]any{"ucc": ucc, "received": received, "due": x.Due})
	if _, err := o.sendLetter(ctx, tenant, actor, received, ucc, x.ID, "Account closure request received", []string{
		fmt.Sprintf("We have received your request %s of %s to close your trading account %s. No further orders will be taken on the account.", x.ID, fmtDay(received), ucc),
		fmt.Sprintf("We will pay out any funds due to you, release your pledged securities and close the account by %s, and write to you when it is done.", fmtDay(x.Due)),
	}); err == nil {
		_, _ = docs.Update(ctx, o.Docs, tenant, cClosure, x.ID, actor, func(a *AccountClosure) error { a.AckOn = received; return nil })
	}
	x, err = o.CheckClosure(ctx, tenant, actor, x.ID)
	o.raiseWork(ctx, tenant, "closure", x.ID, fmt.Sprintf("Close %s's account by %s", ucc, fmtDay(x.Due)),
		fmt.Sprintf("Closure request %s received %s. Pay out the credit balance, release every pledge and allocation, then close the account on Clients → closures; all within 3 working days (NSE/INSP/73976 item 1.15(vi)).", x.ID, fmtDay(received)), ucc, RoleChecker, "settlement", received)
	return x, err
}

// CheckClosure re-checks a closure against the books, dating each step
// as it is done.
func (o *Ops) CheckClosure(ctx context.Context, tenant, actor, id string) (AccountClosure, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Checking an account closure", RoleMaker, RoleChecker, RoleCompliance); err != nil {
			return AccountClosure{}, err
		}
	}
	today := o.now().In(ist).Format("2006-01-02")
	return docs.Update(ctx, o.Docs, tenant, cClosure, id, actor, func(x *AccountClosure) error {
		if x.Status == "Closed" || x.Status == "Rejected" {
			return nil
		}
		b := o.closureBooks(ctx, tenant, x.UCC, today)
		x.Holds = []string{}
		wait := false
		if b.debit.Sign() > 0 {
			x.Holds = append(x.Holds, fmt.Sprintf("debit balance of ₹%s across the exchanges: the client pays it first", formatINR(b.debit)))
			wait = true
		}
		if b.positions > 0 {
			x.Holds = append(x.Holds, fmt.Sprintf("%d open position(s): squared off or settled first", b.positions))
			wait = true
		}
		if b.credit.Sign() > 0 || b.payouts > 0 {
			x.Holds = append(x.Holds, fmt.Sprintf("₹%s credit to pay out (%d payout request(s) not yet paid)", formatINR(b.credit), b.payouts))
			x.PaidOutOn = ""
		} else if x.PaidOutOn == "" && !wait {
			x.PaidOutOn = today
		}
		if len(b.pledged) > 0 || b.cuspa > 0 || b.allocated.Sign() > 0 {
			var r []string
			if len(b.pledged) > 0 {
				r = append(r, "pledges of "+strings.Join(b.pledged, ", "))
			}
			if b.cuspa > 0 {
				r = append(r, fmt.Sprintf("%d unpaid-securities line(s)", b.cuspa))
			}
			if b.allocated.Sign() > 0 {
				r = append(r, fmt.Sprintf("₹%s still allocated at NSE Clearing", formatINR(b.allocated)))
			}
			x.Holds = append(x.Holds, "to release: "+strings.Join(r, "; "))
			x.ReleasedOn = ""
		} else if x.ReleasedOn == "" && !wait {
			x.ReleasedOn = today
		}
		x.Status = "Open"
		if wait {
			x.Status = "Waiting for client"
		}
		x.Checked = o.now().Format(time.RFC3339)
		return nil
	})
}

// CompleteClosure closes every open segment of the client once the
// pay-out and the release are done, and sends the closure letter. It
// refuses while anything stands in the way.
func (o *Ops) CompleteClosure(ctx context.Context, tenant, actor, id string) (AccountClosure, error) {
	if err := o.require(ctx, tenant, actor, "Closing an account", RoleChecker, RoleCompliance); err != nil {
		return AccountClosure{}, err
	}
	x, err := o.CheckClosure(ctx, tenant, actor, id)
	if err != nil {
		return x, err
	}
	if x.Status == "Closed" {
		return x, fmt.Errorf("%w: %s is already closed", ErrState, id)
	}
	if len(x.Holds) > 0 || x.PaidOutOn == "" || x.ReleasedOn == "" {
		return x, fmt.Errorf("%w: %s cannot be closed yet: %s", ErrState, id, strings.Join(x.Holds, "; "))
	}
	today := o.now().In(ist).Format("2006-01-02")
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, x.UCC, actor, func(c *Client) error {
		for _, s := range c.Segments {
			if c.SegmentStatus(s) == SegClosed {
				continue
			}
			if err := applySegmentStatus(c, s, SegClosed, today, "Closed on the client's request "+x.ID, actor, "Ledgerline", o.now()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return x, err
	}
	x, err = docs.Update(ctx, o.Docs, tenant, cClosure, id, actor, func(a *AccountClosure) error {
		a.Status, a.ClosedOn, a.ClosedBy = "Closed", today, actor
		return nil
	})
	if err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "closure.closed", x.ID, map[string]any{"ucc": x.UCC, "paid_out_on": x.PaidOutOn, "released_on": x.ReleasedOn, "late": today > x.Due})
	paras := []string{fmt.Sprintf("Your trading account %s has been closed on %s, as you asked in your request %s of %s.", c.UCC, fmtDay(today), x.ID, fmtDay(x.Received)),
		fmt.Sprintf("All funds due to you were paid to your registered bank account (completed %s) and all your pledged securities and collateral were released (completed %s). Your statement of account to the date of closure is available on request.", fmtDay(x.PaidOutOn), fmtDay(x.ReleasedOn))}
	if _, err := o.sendLetter(ctx, tenant, actor, today, c.UCC, x.ID, "Account closure", paras); err != nil {
		o.log(ctx, tenant, actor, "letter.error", c.UCC, map[string]any{"closure": x.ID, "error": err.Error()})
	} else {
		x, _ = docs.Update(ctx, o.Docs, tenant, cClosure, id, actor, func(a *AccountClosure) error { a.LetterOn = today; return nil })
	}
	o.closeWork(ctx, tenant, actor, "closure", x.ID, "closed")
	return x, nil
}

// RejectClosure refuses a closure request, with the reason written to the
// client (item 1.15(viii)); the trading block is lifted.
func (o *Ops) RejectClosure(ctx context.Context, tenant, actor, id, reason string) (AccountClosure, error) {
	if err := o.require(ctx, tenant, actor, "Rejecting an account closure", RoleChecker, RoleCompliance); err != nil {
		return AccountClosure{}, err
	}
	if len(strings.TrimSpace(reason)) < 4 {
		return AccountClosure{}, invalid("give the reason to tell the client")
	}
	x, err := docs.Update(ctx, o.Docs, tenant, cClosure, id, actor, func(a *AccountClosure) error {
		if a.Status == "Closed" || a.Status == "Rejected" {
			return fmt.Errorf("%w: %s is %s", ErrState, a.ID, strings.ToLower(a.Status))
		}
		a.Status, a.Reason = "Rejected", strings.TrimSpace(reason)
		return nil
	})
	if err != nil {
		return x, err
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cClient, x.UCC, actor, func(c *Client) error {
		if c.Blocked && c.BlockedReason == "Account closure requested ("+x.ID+")" {
			c.Blocked, c.BlockedReason = false, ""
		}
		return nil
	})
	o.log(ctx, tenant, actor, "closure.rejected", x.ID, map[string]any{"reason": x.Reason})
	_, _ = o.sendLetter(ctx, tenant, actor, o.now().In(ist).Format("2006-01-02"), x.UCC, x.ID, "Account closure not done", []string{
		fmt.Sprintf("We could not close your trading account %s on your request %s, for this reason: %s.", x.UCC, x.ID, x.Reason),
		"Please clear it and write to us again; you can also write to us if you think this is a mistake."})
	o.closeWork(ctx, tenant, actor, "closure", x.ID, "rejected")
	return x, nil
}

// Closures lists the closure requests, newest first.
func (o *Ops) Closures(ctx context.Context, tenant string) ([]AccountClosure, error) {
	xs, err := docs.List[AccountClosure](ctx, o.Docs, tenant, cClosure, "")
	sort.SliceStable(xs, func(i, j int) bool { return xs[i].ID > xs[j].ID })
	if xs == nil {
		xs = []AccountClosure{}
	}
	return xs, err
}

// raiseOverdueClosures re-checks open closures and raises each past its
// 3 working days.
func (o *Ops) raiseOverdueClosures(ctx context.Context, tenant, today string) {
	xs, _ := o.Closures(ctx, tenant)
	for _, x := range xs {
		if x.Status != "Open" && x.Status != "Waiting for client" {
			continue
		}
		x, _ = o.CheckClosure(ctx, tenant, SystemActor, x.ID)
		if x.Due < today && x.Status == "Open" {
			o.raiseWork(ctx, tenant, "closure", "late|"+x.ID, "Account closure overdue · "+x.UCC,
				fmt.Sprintf("Closure request %s was due by %s (3 working days). Still to do: %s.", x.ID, fmtDay(x.Due), strings.Join(x.Holds, "; ")), x.UCC, RoleChecker, "settlement", today)
		}
	}
}

// closurePaidOut says whether a client's books show nothing left to pay
// out or release, for a closure letter that says so.
func (o *Ops) closurePaidOut(ctx context.Context, tenant, ucc, date string) (bool, string) {
	b := o.closureBooks(ctx, tenant, ucc, date)
	var why []string
	if b.credit.Sign() > 0 || b.payouts > 0 {
		why = append(why, fmt.Sprintf("₹%s credit not yet paid out", formatINR(b.credit)))
	}
	if len(b.pledged) > 0 || b.cuspa > 0 {
		why = append(why, "pledged or unpaid securities not yet released")
	}
	return len(why) == 0, strings.Join(why, "; ")
}

// siClosure3WD: each closure request of the last 60 days closed within 3
// working days (or waiting for the client to clear a debit or position),
// and no account closed before its pay-out was done.
func siClosure3WD(e *siEnv) (siOut, error) {
	var s siOut
	xs, err := e.o.Closures(e.ctx, e.tenant)
	if err != nil {
		return s, err
	}
	from := addDays(e.date, -60)
	s.input("Account closure requests", from)
	for _, x := range xs {
		if x.Received < from || x.Received > e.date || x.Status == "Rejected" {
			continue
		}
		s.checked++
		switch {
		case x.Status == "Closed" && x.ClosedOn > x.Due:
			s.bad(fmt.Sprintf("%s · %s · asked %s, closed %s (due by %s)", x.ID, siClient(x.UCC), fmtDay(x.Received), fmtDay(x.ClosedOn), fmtDay(x.Due)))
		case x.Status == "Closed" && (x.PaidOutOn == "" || x.PaidOutOn > x.ClosedOn):
			s.bad(fmt.Sprintf("%s · %s · closed %s before the pay-out was done", x.ID, siClient(x.UCC), fmtDay(x.ClosedOn)))
		case x.Status == "Open" && x.Due < e.date:
			s.bad(fmt.Sprintf("%s · %s · asked %s, not closed by %s: %s", x.ID, siClient(x.UCC), fmtDay(x.Received), fmtDay(x.Due), strings.Join(x.Holds, "; ")))
		}
	}
	s.summary = fmt.Sprintf("%d closure requests of the last 60 days checked; %d not closed within 3 working days or closed before the pay-out.", s.checked, s.found)
	return s, nil
}
