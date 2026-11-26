package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- a client reports a fraudulent transaction ----
//
// CSCRF Part II PR.AT.S3 3: customers get a way in the app to flag a
// fraudulent transaction, with immediate notification. From the portal
// (and the client app, which uses the same call) the client says what
// they did not do: a trade, a payout, a demat debit, a pledge, a sign-in
// or a change of details. At once:
//
//   - the firm's security contacts and the compliance officer get an
//     e-mail, and the compliance officer and the Admin a work item;
//   - it is a security event, and a security incident (High) so the
//     CERT-In clock and the follow-ups run from the report;
//   - the client gets an acknowledgement with the reference by e-mail;
//   - with the client's say-so, the account is blocked from trading and
//     payouts until the firm has looked (the kill switch, which the
//     risk manager lifts).
//
// For a cyber fraud the client is also told to call 1930 or report on
// cybercrime.gov.in (the I4C's national helpline), which the firm's duty
// CC-T-CYBERFRAUD-LEA covers on its side.

const cFraudRpt = "fraudreport"

// FraudKinds are what a client can report.
var FraudKinds = []string{"Trade I did not place", "Payout I did not ask for", "Shares moved out of my demat", "Pledge I did not make", "Sign-in I did not make", "Change of my details I did not ask for", "Other"}

// FraudReport is a client's report.
type FraudReport struct {
	ID       string       `json:"id"`
	UCC      string       `json:"ucc"`
	Kind     string       `json:"kind"`
	When     string       `json:"when,omitempty"` // when it happened, as the client says
	Amount   money.Amount `json:"amount,omitempty"`
	Ref      string       `json:"ref,omitempty"` // contract note, order or payout number
	Detail   string       `json:"detail"`
	Block    bool         `json:"block"` // the client asked to block the account
	By       string       `json:"by"`
	At       string       `json:"at"`
	IP       string       `json:"ip,omitempty"`
	Incident string       `json:"incident,omitempty"`
	Notified []string     `json:"notified"`
	Status   string       `json:"status"` // Open, Closed
	Outcome  string       `json:"outcome,omitempty"`
	ClosedBy string       `json:"closed_by,omitempty"`
	ClosedAt string       `json:"closed_at,omitempty"`
}

// ReportFraud records the client's report and tells the firm at once.
func (o *Ops) ReportFraud(ctx context.Context, tenant, actor string, r FraudReport, ip string) (FraudReport, error) {
	c, err := o.portalClient(ctx, tenant, actor, r.UCC)
	if err != nil {
		return FraudReport{}, err
	}
	if !contains(FraudKinds, r.Kind) {
		return FraudReport{}, invalid("choose what happened")
	}
	r.Detail, r.Ref, r.When = strings.TrimSpace(r.Detail), strings.TrimSpace(r.Ref), strings.TrimSpace(r.When)
	if len(r.Detail) < 10 {
		return FraudReport{}, invalid("tell us in a few words what you saw")
	}
	if r.Amount < 0 {
		return FraudReport{}, invalid("the amount cannot be negative")
	}
	n, err := o.next(ctx, tenant, "fraudreport", actor)
	if err != nil {
		return FraudReport{}, err
	}
	now := o.now()
	r.ID, r.UCC, r.By, r.At, r.IP, r.Status, r.Notified = fmt.Sprintf("FR-%06d", n), c.UCC, actor, now.Format(time.RFC3339), trim(ip, 60), "Open", []string{}
	r.Detail, r.Ref, r.When = trim(r.Detail, 2000), trim(r.Ref, 80), trim(r.When, 40)
	if _, err := docs.Put(ctx, o.Docs, tenant, cFraudRpt, r.ID, r, 0, actor); err != nil {
		return FraudReport{}, err
	}
	o.log(ctx, tenant, actor, "client.fraud_reported", c.UCC, map[string]any{"id": r.ID, "kind": r.Kind, "amount": r.Amount.String(), "block": r.Block})
	// A security event, and an incident with its clocks.
	e := o.SecurityEvent(ctx, tenant, SecEvent{Kind: "client.fraud_reported", Subject: c.UCC, Actor: actor, IP: ip, Detail: map[string]any{"report": r.ID},
		Message: "Client " + c.UCC + " reported a fraudulent transaction: " + r.Kind})
	r.Incident = e.Incident
	set, _ := o.Settings(ctx, tenant)
	subject := fmt.Sprintf("[Fraud report] %s from %s: %s", r.ID, c.UCC, r.Kind)
	body := fmt.Sprintf("Client %s (%s) reported a fraudulent transaction from the portal at %s.\n\nWhat: %s\nWhen: %s\nAmount: %s\nReference: %s\nDetails: %s\nBlock the account: %v\n\nIncident %s is open on Security → Incidents with its CERT-In clock. Look at the account now: Compliance → Fraud reports.",
		c.Name, c.UCC, now.In(ist).Format("02 Jan 2006 15:04 IST"), r.Kind, firstOf(r.When, "not given"), map[bool]string{true: "₹" + r.Amount.String(), false: "not given"}[r.Amount > 0], firstOf(r.Ref, "not given"), r.Detail, r.Block, firstOf(r.Incident, "(not opened)"))
	to := o.alertContacts(ctx, tenant)
	if set.ComplianceEmail != "" && !contains(to, set.ComplianceEmail) {
		to = append(to, set.ComplianceEmail)
	}
	for _, x := range to {
		r.Notified = append(r.Notified, x+": "+o.mailStaff(ctx, tenant, x, subject, body, r.ID, nil))
	}
	today := now.In(ist).Format("2006-01-02")
	for _, role := range []string{RoleCompliance, RoleAdmin} {
		o.raiseWork(ctx, tenant, "fraud", r.ID+"|"+role, "Fraud report "+r.ID+" from "+c.UCC+": "+r.Kind, trim(r.Detail, 300), c.UCC, role, "cyber", today)
	}
	if r.Block && !c.Blocked {
		if _, err := docs.Update(ctx, o.Docs, tenant, cClient, c.UCC, SystemActor, func(x *Client) error {
			x.Blocked, x.BlockedReason = true, "Client reported a fraudulent transaction ("+r.ID+")"
			return nil
		}); err == nil {
			o.log(ctx, tenant, SystemActor, "client.blocked", c.UCC, map[string]any{"reason": "fraud report " + r.ID, "at_request_of": actor})
		}
	}
	if reEmail.MatchString(c.Email) {
		o.sendDirect(ctx, tenant, actor, ChEmail, c.Email, c.UCC, "fraud.ack", "We have your report "+r.ID,
			fmt.Sprintf("Dear %s,\n\nWe received your report %s at %s: %s.%s\n\nWe are looking into it and will write to you. If money was taken from your bank account by a cyber fraud, also call the national cyber crime helpline 1930 or report it on cybercrime.gov.in at once.\n\n%s",
				c.Name, r.ID, now.In(ist).Format("02 Jan 2006 15:04"), r.Kind, map[bool]string{true: " As you asked, your account is blocked from trading and payouts until we have checked it.", false: ""}[r.Block], set.Firm))
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cFraudRpt, r.ID, r, actor)
	return r, nil
}

// FraudReports lists the reports, newest first: the compliance officer
// and Admin see all; a client sees their own accounts'.
func (o *Ops) FraudReports(ctx context.Context, tenant, actor string) ([]FraudReport, error) {
	var only []string
	if role, _ := o.RoleOf(ctx, tenant, actor); role == RoleClient {
		accts, err := o.PortalAccounts(ctx, tenant, actor)
		if err != nil {
			return nil, err
		}
		only = accts
	} else if err := o.require(ctx, tenant, actor, "Seeing clients' fraud reports", RoleCompliance, RoleRisk); err != nil {
		return nil, err
	}
	xs, err := docs.List[FraudReport](ctx, o.Docs, tenant, cFraudRpt, "")
	out := []FraudReport{}
	for _, x := range xs {
		if only == nil || contains(only, x.UCC) {
			if only != nil {
				x.Notified, x.IP = nil, ""
			}
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, err
}

// CloseFraudReport records what was found and tells the client.
// Compliance and Admin.
func (o *Ops) CloseFraudReport(ctx context.Context, tenant, actor, id, outcome string) (FraudReport, error) {
	if err := o.require(ctx, tenant, actor, "Closing clients' fraud reports", RoleCompliance); err != nil {
		return FraudReport{}, err
	}
	outcome = strings.TrimSpace(outcome)
	if len(outcome) < 10 {
		return FraudReport{}, invalid("say what was found and done; the client gets it by e-mail")
	}
	r, err := docs.Update(ctx, o.Docs, tenant, cFraudRpt, id, actor, func(x *FraudReport) error {
		if x.Status != "Open" {
			return fmt.Errorf("%w: %s is closed", ErrState, x.ID)
		}
		x.Status, x.Outcome, x.ClosedBy, x.ClosedAt = "Closed", trim(outcome, 2000), actor, o.now().Format(time.RFC3339)
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	for _, role := range []string{RoleCompliance, RoleAdmin} {
		o.closeWork(ctx, tenant, actor, "fraud", r.ID+"|"+role, "closed")
	}
	if c, err := o.Client(ctx, tenant, r.UCC); err == nil && reEmail.MatchString(c.Email) {
		set, _ := o.Settings(ctx, tenant)
		o.sendDirect(ctx, tenant, actor, ChEmail, c.Email, c.UCC, "fraud.closed", "Your report "+r.ID,
			fmt.Sprintf("Dear %s,\n\nAbout your report %s of %s: %s\n\nIf you are not satisfied, write to %s, or approach SEBI through SCORES.\n\n%s", c.Name, r.ID, fmtDay(r.At[:10]), r.Outcome, firstOf(set.ComplianceEmail, "our compliance officer"), set.Firm))
	}
	o.log(ctx, tenant, actor, "client.fraud_closed", r.UCC, map[string]any{"id": r.ID})
	return r, nil
}
