package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
)

// ---- investor complaints and service requests ----
//
// Complaints are resolved within 21 calendar days (SEBI SCORES 2.0,
// effective December 2023); service requests and queries within 7. The
// monthly disclosure of complaints that brokers publish on their website
// (investor charter, December 2021) is built from the same register.

// Ticket kinds.
const (
	KindComplaint = "Complaint"
	KindRequest   = "Service request"
	KindQuery     = "Query"
)

// TicketSources are where a complaint came from, as the disclosure groups them.
var TicketSources = []string{"Directly from investors", "SEBI (SCORES)", "Stock exchanges", "Other sources"}

// TicketCategories follow the SCORES categories for stock brokers.
var TicketCategories = []string{"Non-receipt of funds", "Non-receipt of securities", "Contract note / statement not received", "Excess brokerage or charges", "Unauthorised trades", "Margin or square-off", "Account opening / KYC / modification", "Account closure", "Demat / DP", "IPO / mutual funds", "Technical / trading platform", "Others"}

// TicketStatuses in order.
var TicketStatuses = []string{"Open", "In progress", "Awaiting client", "Resolved", "Closed"}

// TicketMsg is one message in a ticket.
type TicketMsg struct {
	At     string `json:"at"`
	By     string `json:"by"`
	Client bool   `json:"client"`
	Text   string `json:"text"`
}

// Ticket is a complaint, service request or query.
type Ticket struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind"`
	UCC        string      `json:"ucc"`
	Name       string      `json:"name"`
	Category   string      `json:"category"`
	Subject    string      `json:"subject"`
	Source     string      `json:"source"`
	Ref        string      `json:"ref,omitempty"` // SCORES or exchange reference
	Status     string      `json:"status"`
	Assignee   string      `json:"assignee,omitempty"`
	OpenedOn   string      `json:"opened_on"`
	Due        string      `json:"due"`
	ResolvedOn string      `json:"resolved_on,omitempty"`
	Messages   []TicketMsg `json:"messages"`
	Internal   []TicketMsg `json:"internal,omitempty"` // staff notes, never shown to the client
	By         string      `json:"by"`
	Overdue    bool        `json:"overdue"`
	AgeDays    int         `json:"age_days"`
}

func ticketTAT(kind string) int {
	if kind == KindComplaint {
		return 21
	}
	return 7
}

// OpenTicket raises a ticket. Clients raise their own from the portal;
// staff record those received by email, phone, SCORES or the exchanges.
func (o *Ops) OpenTicket(ctx context.Context, tenant, actor string, t Ticket, text string) (Ticket, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return t, err
	}
	t.UCC = strings.ToUpper(strings.TrimSpace(t.UCC))
	today := o.now().Format("2006-01-02")
	if role == RoleClient {
		accts, _ := o.PortalAccounts(ctx, tenant, actor)
		if !contains(accts, t.UCC) {
			return t, ErrNotFound
		}
		t.Source, t.Ref, t.OpenedOn = "Directly from investors", "", today
	} else if err := o.require(ctx, tenant, actor, "Recording complaints", RoleMaker, RoleChecker, RoleCompliance, RoleBranch, RoleAP); err != nil {
		return t, err
	}
	c, err := o.Client(ctx, tenant, t.UCC)
	if err != nil {
		return t, invalid("client %s not found", t.UCC)
	}
	if t.Kind == "" {
		t.Kind = KindComplaint
	}
	if t.Source == "" {
		t.Source = TicketSources[0]
	}
	if t.OpenedOn == "" {
		t.OpenedOn = today
	}
	switch {
	case !contains([]string{KindComplaint, KindRequest, KindQuery}, t.Kind):
		return t, invalid("kind must be Complaint, Service request or Query")
	case !contains(TicketCategories, t.Category):
		return t, invalid("choose a category")
	case !contains(TicketSources, t.Source):
		return t, invalid("source must be one of %s", strings.Join(TicketSources, ", "))
	case strings.TrimSpace(t.Subject) == "" || strings.TrimSpace(text) == "":
		return t, invalid("enter a subject and describe the issue")
	case !ledger.ValidDate(t.OpenedOn) || t.OpenedOn > today:
		return t, invalid("received date must be a date not in the future")
	case (t.Source == "SEBI (SCORES)" || t.Source == "Stock exchanges") && strings.TrimSpace(t.Ref) == "":
		return t, invalid("enter the %s reference number", t.Source)
	}
	n, err := o.next(ctx, tenant, "ticket", actor)
	if err != nil {
		return t, err
	}
	now := o.now().Format(time.RFC3339)
	t.ID = fmt.Sprintf("TKT-%06d", n)
	t.Name, t.Status, t.By = c.Name, "Open", actor
	t.Due = addDays(t.OpenedOn, ticketTAT(t.Kind))
	t.Messages = []TicketMsg{{At: now, By: actor, Client: role == RoleClient, Text: strings.TrimSpace(text)}}
	t.Internal = nil
	if _, err := docs.Put(ctx, o.Docs, tenant, cTicket, t.ID, t, 0, actor); err != nil {
		return t, err
	}
	o.log(ctx, tenant, actor, "ticket.opened", t.ID, map[string]any{"ucc": t.UCC, "kind": t.Kind, "category": t.Category, "source": t.Source})
	o.notify(ctx, tenant, actor, "ticket.opened", t.UCC, map[string]string{"id": t.ID, "subject": t.Subject, "due": t.Due})
	return t, nil
}

// TicketUpdate adds a reply or internal note, and may change the status or
// assignee. A client may only reply to their own ticket, which reopens it.
func (o *Ops) TicketUpdate(ctx context.Context, tenant, actor, id, text string, internal bool, status, assignee string) (Ticket, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return Ticket{}, err
	}
	var accts []string
	if role == RoleClient {
		accts, _ = o.PortalAccounts(ctx, tenant, actor)
		if internal || assignee != "" || (status != "" && status != "Closed") {
			return Ticket{}, fmt.Errorf("%w: clients can only reply or close", ErrForbidden)
		}
	} else if err := o.require(ctx, tenant, actor, "Working complaints", RoleMaker, RoleChecker, RoleCompliance, RoleBranch, RoleAP); err != nil {
		return Ticket{}, err
	}
	text = strings.TrimSpace(text)
	if status != "" && !contains(TicketStatuses, status) {
		return Ticket{}, invalid("status must be one of %s", strings.Join(TicketStatuses, ", "))
	}
	if status == "Resolved" && text == "" {
		return Ticket{}, invalid("write the resolution to the client")
	}
	now := o.now()
	t, err := docs.Update(ctx, o.Docs, tenant, cTicket, id, actor, func(t *Ticket) error {
		if role == RoleClient && !contains(accts, t.UCC) {
			return ErrNotFound
		}
		if t.Status == "Closed" {
			return fmt.Errorf("%w: %s is closed; raise a new ticket", ErrState, id)
		}
		if text != "" {
			m := TicketMsg{At: now.Format(time.RFC3339), By: actor, Client: role == RoleClient, Text: text}
			if internal {
				t.Internal = append(t.Internal, m)
			} else {
				t.Messages = append(t.Messages, m)
			}
		}
		if role == RoleClient && status == "" && text != "" && t.Status != "Open" {
			t.Status, t.ResolvedOn = "Open", ""
		}
		if status != "" {
			t.Status = status
			if status == "Resolved" || status == "Closed" {
				if t.ResolvedOn == "" {
					t.ResolvedOn = now.Format("2006-01-02")
				}
			} else {
				t.ResolvedOn = ""
			}
		} else if t.Status == "Open" && role != RoleClient && text != "" && !internal {
			t.Status = "In progress"
		}
		if assignee != "" {
			t.Assignee = assignee
		}
		return nil
	})
	if err != nil {
		return t, err
	}
	o.log(ctx, tenant, actor, "ticket.updated", id, map[string]any{"status": t.Status, "assignee": t.Assignee, "internal": internal})
	if !internal && text != "" && role != RoleClient {
		o.notify(ctx, tenant, actor, "ticket.reply", t.UCC, map[string]string{"id": t.ID, "subject": t.Subject, "status": t.Status})
	}
	return t, nil
}

// Tickets lists tickets, newest first, with age and overdue flags as at date.
func (o *Ops) Tickets(ctx context.Context, tenant, date string) ([]Ticket, error) {
	ts, err := docs.List[Ticket](ctx, o.Docs, tenant, cTicket, "")
	for i := range ts {
		t := &ts[i]
		end := date
		if t.ResolvedOn != "" {
			end = t.ResolvedOn
		}
		t.AgeDays = daysBetween(t.OpenedOn, end)
		t.Overdue = end > t.Due
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID > ts[j].ID })
	if ts == nil {
		ts = []Ticket{}
	}
	return ts, err
}

// DisclosureRow is one line of the monthly complaints disclosure.
type DisclosureRow struct {
	Source        string `json:"source"`
	PendingAtOpen int    `json:"pending_at_start"`
	Received      int    `json:"received"`
	Resolved      int    `json:"resolved"`
	Pending       int    `json:"pending"`
	Over3Months   int    `json:"pending_over_3_months"`
	AvgDays       int    `json:"avg_resolution_days"`
}

// TrendRow is one month of the disposal trend.
type TrendRow struct {
	Month    string `json:"month"`
	Carried  int    `json:"carried_forward"`
	Received int    `json:"received"`
	Resolved int    `json:"resolved"`
	Pending  int    `json:"pending"`
}

// Disclosure is the monthly complaints data brokers publish.
type Disclosure struct {
	Month  string          `json:"month"`
	Rows   []DisclosureRow `json:"rows"`
	Total  DisclosureRow   `json:"total"`
	Trend  []TrendRow      `json:"trend"`
	Annual []TrendRow      `json:"annual"`
}

func monthEnd(m string) string { return addDays(addMonths(m+"-01", 1), -1) }

// ComplaintsDisclosure builds the disclosure for month (YYYY-MM), with the
// twelve-month trend and financial-year figures.
func (o *Ops) ComplaintsDisclosure(ctx context.Context, tenant, month string) (Disclosure, error) {
	if !ledger.ValidDate(month + "-01") {
		return Disclosure{}, invalid("month must be YYYY-MM")
	}
	all, err := docs.List[Ticket](ctx, o.Docs, tenant, cTicket, "")
	if err != nil {
		return Disclosure{}, err
	}
	var cs []Ticket
	for _, t := range all {
		if t.Kind == KindComplaint {
			cs = append(cs, t)
		}
	}
	start, end := month+"-01", monthEnd(month)
	open := func(t Ticket, d string) bool { return t.OpenedOn <= d && (t.ResolvedOn == "" || t.ResolvedOn > d) }
	ds := Disclosure{Month: month}
	tot := DisclosureRow{Source: "Grand total"}
	var totDays, totN int
	for _, src := range TicketSources {
		r := DisclosureRow{Source: src}
		var days, n int
		for _, t := range cs {
			if t.Source != src {
				continue
			}
			if open(t, addDays(start, -1)) {
				r.PendingAtOpen++
			}
			if t.OpenedOn >= start && t.OpenedOn <= end {
				r.Received++
			}
			if t.ResolvedOn >= start && t.ResolvedOn <= end {
				r.Resolved++
				days += daysBetween(t.OpenedOn, t.ResolvedOn)
				n++
			}
			if open(t, end) {
				r.Pending++
				if t.OpenedOn < addMonths(addDays(end, 1), -3) {
					r.Over3Months++
				}
			}
		}
		if n > 0 {
			r.AvgDays = (days + n/2) / n
		}
		totDays, totN = totDays+days, totN+n
		tot.PendingAtOpen += r.PendingAtOpen
		tot.Received += r.Received
		tot.Resolved += r.Resolved
		tot.Pending += r.Pending
		tot.Over3Months += r.Over3Months
		ds.Rows = append(ds.Rows, r)
	}
	if totN > 0 {
		tot.AvgDays = (totDays + totN/2) / totN
	}
	ds.Total = tot
	trend := func(from, to string) TrendRow {
		r := TrendRow{}
		for _, t := range cs {
			if open(t, addDays(from, -1)) {
				r.Carried++
			}
			if t.OpenedOn >= from && t.OpenedOn <= to {
				r.Received++
			}
			if t.ResolvedOn >= from && t.ResolvedOn <= to {
				r.Resolved++
			}
			if open(t, to) {
				r.Pending++
			}
		}
		return r
	}
	fy := fyStartOf(start)
	for m := fy[:7]; m <= month; m = addMonths(m+"-01", 1)[:7] {
		r := trend(m+"-01", monthEnd(m))
		r.Month = m
		ds.Trend = append(ds.Trend, r)
	}
	for i := 4; i >= 0; i-- {
		f := addMonths(fy, -12*i)
		r := trend(f, addDays(addMonths(f, 12), -1))
		r.Month = fyLabel(f)
		ds.Annual = append(ds.Annual, r)
	}
	return ds, nil
}
