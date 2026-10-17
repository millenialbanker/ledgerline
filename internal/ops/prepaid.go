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

// ---- prepaid brokerage schemes ----
//
// Two kinds:
//
//   - Credit: the client pays an amount up front (GST included) and
//     brokerage on their trades is adjusted against it, day by day, until
//     the credit is used or the scheme expires; unused credit then lapses.
//     GST was paid on the advance, so the GST on adjusted brokerage is
//     refunded to the client with the brokerage.
//   - Plan: the client pays a fee (GST included) for a period during which
//     a cheaper brokerage plan applies; the plan is assigned from the start
//     and the client's earlier plan restored after expiry.
//
// Schemes may renew automatically when the client has the funds.

const (
	cPrepaidScheme = "prepaid_scheme"
	cPrepaidSub    = "prepaid_sub"
	// PayablePrepaid is brokerage paid in advance and not yet used.
	PayablePrepaid = "PAYABLE:PREPAID_BROKERAGE"
	// IncomeSubscription is fees for plan schemes and lapsed prepaid credit.
	IncomeSubscription = "INCOME:SUBSCRIPTION"
)

// PrepaidScheme is a prepaid brokerage offer.
type PrepaidScheme struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Kind   string       `json:"kind"`  // Credit or Plan
	Price  money.Amount `json:"price"` // including GST
	Days   int          `json:"days"`  // validity
	Plan   string       `json:"plan,omitempty"`
	Active bool         `json:"active"`
}

// PrepaidSub is a client's purchase of a scheme.
type PrepaidSub struct {
	ID        string       `json:"id"`
	UCC       string       `json:"ucc"`
	Name      string       `json:"name"`
	Scheme    string       `json:"scheme"`
	Kind      string       `json:"kind"`
	Start     string       `json:"start"`
	Expiry    string       `json:"expiry"`
	Price     money.Amount `json:"price"`
	GST       money.Amount `json:"gst"`
	Credit    money.Amount `json:"credit"` // brokerage value available (credit kind)
	Used      money.Amount `json:"used"`
	Status    string       `json:"status"` // Active, Exhausted, Expired, Renewed, Cancelled
	AutoRenew bool         `json:"auto_renew"`
	PrevPlan  string       `json:"prev_plan,omitempty"`
	Invoice   string       `json:"invoice,omitempty"`
	LastRun   string       `json:"last_run,omitempty"`
}

// PrepaidSchemes lists schemes.
func (o *Ops) PrepaidSchemes(ctx context.Context, tenant string) ([]PrepaidScheme, error) {
	return docs.List[PrepaidScheme](ctx, o.Docs, tenant, cPrepaidScheme, "")
}

// SavePrepaidScheme adds or changes a scheme.
func (o *Ops) SavePrepaidScheme(ctx context.Context, tenant, actor string, s PrepaidScheme) (PrepaidScheme, error) {
	if err := o.require(ctx, tenant, actor, "Prepaid schemes", RoleChecker, RoleAdmin); err != nil {
		return s, err
	}
	s.ID = strings.ToLower(strings.TrimSpace(s.ID))
	if !validPlanID.MatchString(s.ID) || strings.TrimSpace(s.Name) == "" {
		return s, invalid("give an id (lower-case letters, digits, dashes) and a name")
	}
	if s.Kind != "Credit" && s.Kind != "Plan" {
		return s, invalid("kind must be Credit or Plan")
	}
	if s.Price <= 0 || s.Days <= 0 || s.Days > 3660 {
		return s, invalid("price above zero and validity of 1 to 3660 days are required")
	}
	if s.Kind == "Plan" {
		if _, _, err := docs.Get[Plan](ctx, o.Docs, tenant, cBrokPlan, s.Plan); err != nil {
			return s, invalid("brokerage plan %q not found", s.Plan)
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cPrepaidScheme, s.ID, s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "prepaid.scheme.saved", s.ID, map[string]any{"kind": s.Kind, "price": s.Price.String()})
	return s, nil
}

// PrepaidSubs lists subscriptions, newest first.
func (o *Ops) PrepaidSubs(ctx context.Context, tenant string) ([]PrepaidSub, error) {
	ss, err := docs.List[PrepaidSub](ctx, o.Docs, tenant, cPrepaidSub, "")
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].ID > ss[j].ID })
	return ss, err
}

// BuyPrepaid subscribes a client to a scheme from date, debiting the price
// (which must be within the funds the client can withdraw).
func (o *Ops) BuyPrepaid(ctx context.Context, tenant, actor, date, ucc, scheme string, autoRenew bool) (PrepaidSub, error) {
	if actor != "system" {
		if err := o.require(ctx, tenant, actor, "Selling prepaid schemes", RoleMaker, RoleChecker, RoleBranch); err != nil {
			return PrepaidSub{}, err
		}
	}
	ucc = strings.ToUpper(ucc)
	sc, _, err := docs.Get[PrepaidScheme](ctx, o.Docs, tenant, cPrepaidScheme, scheme)
	if err != nil || !sc.Active {
		return PrepaidSub{}, invalid("scheme %q is not available", scheme)
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return PrepaidSub{}, invalid("client %s not found", ucc)
	}
	ss, _ := docs.List[PrepaidSub](ctx, o.Docs, tenant, cPrepaidSub, "")
	for _, x := range ss {
		if x.UCC == ucc && x.Status == "Active" && x.Expiry >= date {
			return PrepaidSub{}, invalid("%s already has an active scheme (%s) until %s", ucc, x.ID, x.Expiry)
		}
	}
	w, err := o.Withdrawable(ctx, tenant, date, ucc)
	if err != nil {
		return PrepaidSub{}, err
	}
	if w.Available < sc.Price {
		return PrepaidSub{}, invalid("%s has ₹%s free; the scheme costs ₹%s", ucc, formatINR(w.Available.Dec()), formatINR(sc.Price.Dec()))
	}
	set, _ := o.Settings(ctx, tenant)
	// Price includes GST: taxable = price × 100 / (100 + rate).
	net, _ := sc.Price.Dec().MulInt(100).Quo(money.DecInt(100).Add(set.gstRate()))
	taxable := net.Round(money.UnitPaisa)
	gst := sc.Price - taxable
	n, err := o.next(ctx, tenant, "prepaid", actor)
	if err != nil {
		return PrepaidSub{}, err
	}
	sub := PrepaidSub{ID: fmt.Sprintf("PP-%06d", n), UCC: ucc, Name: c.Name, Scheme: sc.ID, Kind: sc.Kind, Start: date, Expiry: addDays(date, sc.Days-1),
		Price: sc.Price, GST: gst, Status: "Active", AutoRenew: autoRenew}
	credit := PayablePrepaid
	if sc.Kind == "Credit" {
		sub.Credit = taxable
	} else {
		credit = IncomeSubscription
	}
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "PPB:" + sub.ID, ValueDate: date, CreatedBy: actor,
		Narration: fmt.Sprintf("Prepaid scheme %s (%s) %s to %s", sc.Name, sub.ID, date, sub.Expiry), Source: ledger.SourceRef{Kind: "PREPAID", ID: sub.ID},
		Postings: []ledger.Posting{{Account: ledger.ClientAccount(ucc), Debit: sc.Price}, {Account: credit, Credit: taxable}, {Account: ledger.PayableGST, Credit: gst}}})
	if err != nil {
		return sub, err
	}
	inv, err := o.issueInvoice(ctx, tenant, actor, Invoice{Date: date, UCC: ucc, Name: c.Name, Kind: "Prepaid", Description: "Prepaid brokerage scheme " + sc.Name + " · " + sub.ID,
		SAC: SACBroking, Taxable: taxable, GSTPct: set.gstRate(), GST: gst, Total: sc.Price, Entry: e.ID})
	if err != nil {
		return sub, err
	}
	sub.Invoice = inv.No
	if sc.Kind == "Plan" {
		if as, err := o.Assignments(ctx, tenant); err == nil {
			for _, a := range as {
				if a.Target == "ucc:"+ucc && a.From <= date {
					sub.PrevPlan = a.PlanID
				}
			}
		}
		if _, err := o.assign(ctx, tenant, actor, "ucc:"+ucc, sc.Plan, date); err != nil {
			return sub, err
		}
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cPrepaidSub, sub.ID, sub, 0, actor); err != nil {
		return sub, err
	}
	o.log(ctx, tenant, actor, "prepaid.bought", sub.ID, map[string]any{"ucc": ucc, "scheme": sc.ID, "price": sc.Price.String()})
	return sub, nil
}

// RunPrepaid adjusts brokerage charged on date against active credit
// schemes, then expires schemes that ended before date (lapsing unused
// credit, restoring earlier plans) and renews those set to renew.
func (o *Ops) RunPrepaid(ctx context.Context, tenant, actor, date string) (adjusted, expired, renewed int, err error) {
	if err := o.require(ctx, tenant, actor, "Running prepaid adjustments", RoleChecker); err != nil {
		return 0, 0, 0, err
	}
	set, _ := o.Settings(ctx, tenant)
	ss, err := docs.List[PrepaidSub](ctx, o.Docs, tenant, cPrepaidSub, "")
	if err != nil {
		return 0, 0, 0, err
	}
	brok, err := o.clientBrokerage(ctx, tenant, date, date)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, sub := range ss {
		if sub.Status != "Active" {
			continue
		}
		if sub.Kind == "Credit" && sub.Start <= date && date <= sub.Expiry && brok[sub.UCC] > 0 {
			use := min(brok[sub.UCC], sub.Credit-sub.Used)
			if use > 0 {
				gst := use.Dec().Pct(set.gstRate()).Round(money.UnitPaisa)
				_, created, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "PPA:" + sub.ID + ":" + date, ValueDate: date, CreatedBy: actor,
					Narration: fmt.Sprintf("Brokerage of %s adjusted against prepaid %s", date, sub.ID), Source: ledger.SourceRef{Kind: "PREPAID_ADJ", ID: date + "/" + sub.UCC},
					Postings: []ledger.Posting{{Account: PayablePrepaid, Debit: use}, {Account: ledger.PayableGST, Debit: gst, Memo: "GST paid on the advance"}, {Account: ledger.ClientAccount(sub.UCC), Credit: use + gst, Memo: "Brokerage met from prepaid credit"}}})
				if err != nil {
					return adjusted, expired, renewed, err
				}
				if created {
					adjusted++
					sub.Used += use
					if sub.Used >= sub.Credit {
						sub.Status = "Exhausted"
					}
					sub.LastRun = date
					if err := docs.Upsert(ctx, o.Docs, tenant, cPrepaidSub, sub.ID, sub, actor); err != nil {
						return adjusted, expired, renewed, err
					}
				}
			}
		}
		if sub.Status != "Active" || sub.Expiry >= date {
			continue
		}
		// Expired.
		if sub.Kind == "Credit" && sub.Credit > sub.Used {
			left := sub.Credit - sub.Used
			if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "PPL:" + sub.ID, ValueDate: sub.Expiry, CreatedBy: actor,
				Narration: fmt.Sprintf("Unused prepaid credit of %s lapsed on expiry", sub.ID), Source: ledger.SourceRef{Kind: "PREPAID", ID: sub.ID},
				Postings: []ledger.Posting{{Account: PayablePrepaid, Debit: left}, {Account: IncomeSubscription, Credit: left}}}); err != nil {
				return adjusted, expired, renewed, err
			}
		}
		if sub.Kind == "Plan" {
			if _, err := o.assign(ctx, tenant, actor, "ucc:"+sub.UCC, sub.PrevPlan, addDays(sub.Expiry, 1)); err != nil {
				return adjusted, expired, renewed, err
			}
		}
		sub.Status = "Expired"
		expired++
		if sub.AutoRenew {
			if _, err := o.BuyPrepaid(ctx, tenant, "system", addDays(sub.Expiry, 1), sub.UCC, sub.Scheme, true); err == nil {
				sub.Status = "Renewed"
				renewed++
			}
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cPrepaidSub, sub.ID, sub, actor); err != nil {
			return adjusted, expired, renewed, err
		}
	}
	o.log(ctx, tenant, actor, "prepaid.run", date, map[string]any{"adjusted": adjusted, "expired": expired, "renewed": renewed})
	return adjusted, expired, renewed, nil
}
