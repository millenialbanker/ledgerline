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

// ---- investor complaints and service requests ----
//
// Complaints are resolved within 21 calendar days (SEBI SCORES 2.0,
// effective December 2023); service requests and queries within 7. The
// monthly disclosure of complaints published on the website is built from
// the same register, one table for each business a complaint can concern:
//
//   - the stock broker's: Annexure B of SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/
//     2025/22 (21 Feb 2025), by the 7th of the next month;
//   - the depository participant's: latest by the 7th (SEBI Master Circular
//     for Depositories, SEBI/HO/MRD/MRD-PoD-1/P/CIR/2024/168, Annexure 22,
//     which could not be read: the rows follow the depositories' own
//     format, Annexure 20, with depositories where it has members);
//   - the research analyst's: para 7.3 and Annexure E of the RA master
//     circular HO/38/12/11(1)2026-MIRSD-POD/I/4360/2026 (6 Feb 2026);
//   - the investment adviser's: paras 2.4 and 7.5 and Annexure C of the IA
//     master circular HO/38/12/11(2)2026-MIRSD-POD/I/4300/2026 (6 Feb
//     2026), shown on the home page without scrolling, in font size 12 or
//     above.
//
// The RA and IA tables have no stock exchanges row and add a line for the
// complaints received because some other entity impersonated the RA or IA.

// Ticket kinds.
const (
	KindComplaint = "Complaint"
	KindRequest   = "Service request"
	KindQuery     = "Query"
)

// The businesses a complaint can concern.
const (
	BizBroking = "Broking"
	BizDP      = "DP"
	BizRA      = "RA"
	BizIA      = "IA"
)

// TicketBusinesses names them.
var TicketBusinesses = []struct{ ID, Name string }{{BizBroking, "Stock broking"}, {BizDP, "Depository participant"}, {BizRA, "Research analyst"}, {BizIA, "Investment adviser"}}

// TicketSources are where a complaint came from, as the disclosure groups
// them. Depositories forward complaints about the DP.
var TicketSources = []string{"Directly from investors", "SEBI (SCORES)", "Stock exchanges", "Other sources", "Depositories"}

// sourcesOf are the sources a business's table has rows for.
var sourcesOf = map[string][]string{
	BizBroking: {"Directly from investors", "SEBI (SCORES)", "Stock exchanges", "Other sources"},
	BizDP:      {"Directly from investors", "SEBI (SCORES)", "Depositories", "Other sources"},
	BizRA:      {"Directly from investors", "SEBI (SCORES)", "Other sources"},
	BizIA:      {"Directly from investors", "SEBI (SCORES)", "Other sources"},
}

// normBiz reads a business ("" stays "").
func normBiz(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", true
	}
	for _, b := range TicketBusinesses {
		if strings.EqualFold(s, b.ID) || strings.EqualFold(s, b.Name) {
			return b.ID, true
		}
	}
	return "", false
}

// biz is the business a ticket concerns: broking unless marked.
func (t Ticket) biz() string { return firstOf(t.Business, BizBroking) }

// ticketBusinesses are the businesses the firm's complaints can concern:
// broking always; the DP unless the firm profile says it is none; the RA
// and IA with those registrations.
func (o *Ops) ticketBusinesses(ctx context.Context, tenant string) []string {
	f, _ := o.firmProfileFactsOpt(ctx, tenant, false)
	out := []string{BizBroking}
	if f.dp() != triNo {
		out = append(out, BizDP)
	}
	if f.Regs["ra"] {
		out = append(out, BizRA)
	}
	if f.Regs["ia"] {
		out = append(out, BizIA)
	}
	return out
}

// TicketBusinessesFor are the businesses to choose from on a complaint.
func (o *Ops) TicketBusinessesFor(ctx context.Context, tenant string) []map[string]string {
	have := o.ticketBusinesses(ctx, tenant)
	var out []map[string]string
	for _, b := range TicketBusinesses {
		if contains(have, b.ID) {
			out = append(out, map[string]string{"id": b.ID, "name": b.Name})
		}
	}
	return out
}

// checkTicketTag checks a complaint's business, source and impersonation
// case against the firm.
func (o *Ops) checkTicketTag(ctx context.Context, tenant string, t *Ticket) error {
	b, ok := normBiz(t.Business)
	if !ok {
		return invalid("the business is Broking, DP, RA or IA")
	}
	t.Business = b
	if b == BizBroking {
		t.Business = "" // the default
	}
	if !contains(o.ticketBusinesses(ctx, tenant), t.biz()) {
		return invalid("the firm has no %s registration (Settings → Firm profile)", strings.ToLower(bizName(t.biz())))
	}
	if !contains(sourcesOf[t.biz()], t.Source) {
		return invalid("a %s complaint comes from one of: %s", strings.ToLower(bizName(t.biz())), strings.Join(sourcesOf[t.biz()], ", "))
	}
	t.ImpCase = strings.ToUpper(strings.TrimSpace(t.ImpCase))
	if t.ImpCase != "" {
		if _, _, err := docs.Get[ImpCase](ctx, o.Docs, tenant, cImpCase, t.ImpCase); err != nil {
			return invalid("impersonation case %s is not on the register", t.ImpCase)
		}
		t.Impersonation = true
	}
	return nil
}

func bizName(b string) string {
	for _, x := range TicketBusinesses {
		if x.ID == b {
			return x.Name
		}
	}
	return b
}

// TicketCategories follow the SCORES categories for stock brokers.
var TicketCategories = []string{"Non-receipt of funds", "Non-receipt of securities", "Contract note / statement not received", "Excess brokerage or charges", "Unauthorised trades", "Margin or square-off", "Account opening / KYC / modification", "Account closure", "Demat / DP", "IPO / mutual funds", "Technical / trading platform", "Others", "Research or advice (RA / IA)", "Impersonation of the firm"}

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
	// Business is what the complaint concerns: Broking (when empty), DP, RA
	// or IA; each has its own table in the monthly disclosure.
	Business string `json:"business,omitempty"`
	// Impersonation: received because some other entity impersonated the
	// firm (counted on the RA and IA tables' impersonation line), with the
	// case on the impersonation register.
	Impersonation bool   `json:"impersonation,omitempty"`
	ImpCase       string `json:"impersonation_case,omitempty"`
	// Contact is how to reach a complainant who is not a client (a research
	// or advisory subscriber, or someone the impersonator deceived).
	Contact string `json:"contact,omitempty"`
	// Exchange is the exchange that forwarded a complaint (source "Stock
	// exchanges"), and ExchangeDue the day the exchange wants the
	// redressal uploaded on its portal (BSE's master circular on investor
	// grievances, paras 2.4-2.7); Claim is the amount the complainant
	// claims, for the check of complaints against net worth (grievnw.go).
	Exchange    string       `json:"exchange,omitempty"`
	ExchangeDue string       `json:"exchange_due,omitempty"`
	Claim       money.Amount `json:"claim,omitempty"`
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
		t.Impersonation, t.ImpCase, t.Contact = false, "", ""
		t.Exchange, t.ExchangeDue, t.Claim = "", "", 0
	} else if err := o.require(ctx, tenant, actor, "Recording complaints", RoleMaker, RoleChecker, RoleCompliance, RoleBranch, RoleAP); err != nil {
		return t, err
	}
	// A complaint about the DP is the DP's unless marked otherwise.
	if t.Business == "" && t.Category == "Demat / DP" && contains(o.ticketBusinesses(ctx, tenant), BizDP) {
		t.Business = BizDP
	}
	// A research analyst's or investment adviser's complaint, or one caused
	// by impersonation, may come from someone who is not a client.
	var c Client
	var err2 error
	if t.UCC == "" && role != RoleClient {
		if b, _ := normBiz(t.Business); b != BizRA && b != BizIA && !t.Impersonation && t.ImpCase == "" {
			return t, invalid("choose the client: only a research analyst's or investment adviser's complaint, or one caused by impersonation, may come from someone who is not a client")
		}
		if c.Name = strings.Join(strings.Fields(t.Name), " "); c.Name == "" {
			return t, invalid("give the complainant's name")
		}
	} else if c, err2 = o.Client(ctx, tenant, t.UCC); err2 != nil {
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
	case (t.Source == "SEBI (SCORES)" || t.Source == "Stock exchanges" || t.Source == "Depositories") && strings.TrimSpace(t.Ref) == "":
		return t, invalid("enter the %s reference number", t.Source)
	}
	if err := o.checkTicketTag(ctx, tenant, &t); err != nil {
		return t, err
	}
	n, err := o.next(ctx, tenant, "ticket", actor)
	if err != nil {
		return t, err
	}
	now := o.now().Format(time.RFC3339)
	t.ID = fmt.Sprintf("TKT-%06d", n)
	t.Name, t.Status, t.By = c.Name, "Open", actor
	t.Contact = clip(strings.TrimSpace(t.Contact), 120)
	t.Due = addDays(t.OpenedOn, ticketTAT(t.Kind))
	t.Messages = []TicketMsg{{At: now, By: actor, Client: role == RoleClient, Text: strings.TrimSpace(text)}}
	t.Internal = nil
	if _, err := docs.Put(ctx, o.Docs, tenant, cTicket, t.ID, t, 0, actor); err != nil {
		return t, err
	}
	o.log(ctx, tenant, actor, "ticket.opened", t.ID, map[string]any{"ucc": t.UCC, "kind": t.Kind, "category": t.Category, "source": t.Source, "business": t.biz(), "impersonation": t.Impersonation})
	if t.UCC != "" {
		o.notify(ctx, tenant, actor, "ticket.opened", t.UCC, map[string]string{"id": t.ID, "subject": t.Subject, "due": t.Due})
	}
	if t.ImpCase != "" {
		o.linkImpComplaint(ctx, tenant, actor, t.ImpCase, t.ID)
	}
	if _, mode, _ := o.policy(ctx, tenant, "service"); mode == ModeAuto {
		if _, err := o.RunStage(ctx, tenant, SystemActor, today, "", "service"); err == nil {
			t, _, _ = docs.Get[Ticket](ctx, o.Docs, tenant, cTicket, t.ID)
			if role == RoleClient {
				t.Internal = nil
			}
		}
	}
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
	if !internal && text != "" && role != RoleClient && t.UCC != "" {
		o.notify(ctx, tenant, actor, "ticket.reply", t.UCC, map[string]string{"id": t.ID, "subject": t.Subject, "status": t.Status})
	}
	return t, nil
}

// TicketTag is what TagTicket changes on a complaint.
type TicketTag struct {
	Business      string `json:"business"`
	Impersonation bool   `json:"impersonation"`
	ImpCase       string `json:"impersonation_case,omitempty"`
}

// TagTicket marks the business a complaint concerns and whether it arose
// from impersonation (staff only); the disclosure tables follow it.
func (o *Ops) TagTicket(ctx context.Context, tenant, actor, id string, tag TicketTag) (Ticket, error) {
	if err := o.require(ctx, tenant, actor, "Marking a complaint's business", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return Ticket{}, err
	}
	var before Ticket
	t, err := docs.Update(ctx, o.Docs, tenant, cTicket, id, actor, func(t *Ticket) error {
		before = *t
		t.Business, t.Impersonation, t.ImpCase = tag.Business, tag.Impersonation, tag.ImpCase
		if !tag.Impersonation && tag.ImpCase == "" {
			t.ImpCase = ""
		}
		if t.UCC == "" {
			if b, _ := normBiz(t.Business); b != BizRA && b != BizIA && !t.Impersonation && t.ImpCase == "" {
				return invalid("this complainant is not a client: the complaint stays a research analyst's or investment adviser's, or one caused by impersonation")
			}
		}
		return o.checkTicketTag(ctx, tenant, t)
	})
	if err != nil {
		return t, err
	}
	o.log(ctx, tenant, actor, "ticket.tagged", id, map[string]any{"business": t.biz(), "was": before.biz(), "impersonation": t.Impersonation, "case": t.ImpCase})
	if t.ImpCase != "" && t.ImpCase != before.ImpCase {
		o.linkImpComplaint(ctx, tenant, actor, t.ImpCase, t.ID)
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
	Source        string `json:"source"` // the row as the annexure names it
	PendingAtOpen int    `json:"pending_at_start"`
	Received      int    `json:"received"`
	TotalPending  int    `json:"total_pending"` // carried forward and received (Annexure B)
	Resolved      int    `json:"resolved"`
	Pending       int    `json:"pending"`
	Under3Months  int    `json:"pending_under_3_months"`
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

// Disclosure is the monthly complaints data published on the website, for
// one business.
type Disclosure struct {
	Month       string          `json:"month"`
	Business    string          `json:"business"`
	Title       string          `json:"title"`
	Basis       string          `json:"basis"`
	Rows        []DisclosureRow `json:"rows"`
	Total       DisclosureRow   `json:"total"`
	Trend       []TrendRow      `json:"trend"`
	TrendTotal  TrendRow        `json:"trend_total"`
	Annual      []TrendRow      `json:"annual"`
	AnnualTotal TrendRow        `json:"annual_total"`
	// Impersonation is the RA's or IA's line: complaints received in the
	// month because some other entity impersonated it.
	Impersonation *int `json:"impersonation,omitempty"`
	// Adjusted: those complaints are left out of the tables, as the
	// annexures' note allows.
	Adjusted    bool     `json:"adjusted,omitempty"`
	Unconfirmed string   `json:"unconfirmed,omitempty"`
	Notes       []string `json:"notes"`
}

func monthEnd(m string) string { return addDays(addMonths(m+"-01", 1), -1) }

// discRowDef is one row of a business's table and the sources it counts.
type discRowDef struct {
	Label   string
	Sources []string
}

// discRows are each business's rows, as its annexure names them. A source
// a business has no row for (from before complaints were marked by
// business) counts under other sources.
var discRows = map[string][]discRowDef{
	BizBroking: {{"Directly from Investors", []string{"Directly from investors"}}, {"SEBI (SCORES 2.0)", []string{"SEBI (SCORES)"}}, {"Stock Exchanges", []string{"Stock exchanges"}}, {"Other Sources (if any)", []string{"Other sources", "Depositories"}}},
	BizDP:      {{"Directly from Investors", []string{"Directly from investors"}}, {"SEBI (SCORES)", []string{"SEBI (SCORES)"}}, {"Depositories", []string{"Depositories"}}, {"Other Sources (if any)", []string{"Other sources", "Stock exchanges"}}},
	BizRA:      {{"Directly from Investors", []string{"Directly from investors"}}, {"SEBI (SCORES)", []string{"SEBI (SCORES)"}}, {"Other Sources (if any)", []string{"Other sources", "Stock exchanges", "Depositories"}}},
	BizIA:      {{"Directly from Investors", []string{"Directly from investors"}}, {"SEBI (SCORES)", []string{"SEBI (SCORES)"}}, {"Other Sources (if any)", []string{"Other sources", "Stock exchanges", "Depositories"}}},
}

var discTitles = map[string][2]string{
	BizBroking: {"Investor complaints data of the stock broker", "SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/22 of 21 Feb 2025, Annexure B: on the website by the 7th of the following month"},
	BizDP:      {"Investor complaints data of the depository participant", "SEBI Master Circular for Depositories (SEBI/HO/MRD/MRD-PoD-1/P/CIR/2024/168 of 3 Dec 2024), Annexure 22: on the website latest by the 7th of the following month"},
	BizRA:      {"Complaint data to be displayed by RAs", "RA master circular HO/38/12/11(1)2026-MIRSD-POD/I/4360/2026 of 6 Feb 2026, para 7.3 and Annexure E: on the website and mobile app latest by the 7th of the following month"},
	BizIA:      {"Complaint data to be displayed by IAs", "IA master circular HO/38/12/11(2)2026-MIRSD-POD/I/4300/2026 of 6 Feb 2026, paras 2.4 and 7.5 and Annexure C: on the website and mobile app latest by the 7th of the following month, on the home page without scrolling, in font size 12 or above"},
}

// ComplaintsDisclosure builds the broker's disclosure for month (YYYY-MM),
// with the trend of the financial year and the annual trend.
func (o *Ops) ComplaintsDisclosure(ctx context.Context, tenant, month string) (Disclosure, error) {
	return o.ComplaintsDisclosureFor(ctx, tenant, BizBroking, month)
}

// ComplaintsDisclosureFor builds a business's disclosure for month.
func (o *Ops) ComplaintsDisclosureFor(ctx context.Context, tenant, business, month string) (Disclosure, error) {
	if !ledger.ValidDate(month + "-01") {
		return Disclosure{}, invalid("month must be YYYY-MM")
	}
	biz, ok := normBiz(business)
	if !ok {
		return Disclosure{}, invalid("the business is Broking, DP, RA or IA")
	}
	biz = firstOf(biz, BizBroking)
	all, err := docs.List[Ticket](ctx, o.Docs, tenant, cTicket, "")
	if err != nil {
		return Disclosure{}, err
	}
	adjust := false
	if biz == BizRA || biz == BizIA {
		adjust = o.raiaSettings(ctx, tenant).AdjustImpersonation
	}
	start, end := month+"-01", monthEnd(month)
	var cs []Ticket
	imp := 0
	for _, t := range all {
		if t.Kind != KindComplaint || t.biz() != biz {
			continue
		}
		if t.Impersonation && t.OpenedOn >= start && t.OpenedOn <= end {
			imp++
		}
		if adjust && t.Impersonation {
			continue
		}
		cs = append(cs, t)
	}
	ds := complaintsTable(cs, biz, month)
	ds.Title, ds.Basis, ds.Adjusted = discTitles[biz][0], discTitles[biz][1], adjust
	if biz == BizRA || biz == BizIA {
		ds.Impersonation = &imp
		if adjust {
			ds.Notes = append(ds.Notes, fmt.Sprintf("The %d complaint%s received because of impersonation %s left out of the tables, as the annexure's note allows; they are closed after the due process.", imp, plural(imp), map[bool]string{true: "is", false: "are"}[imp == 1]))
		}
	}
	if biz == BizDP {
		ds.Unconfirmed = "The DPs' format (Annexure 22 of SEBI's Master Circular for Depositories) could not be read: the rows follow the depositories' own format (Annexure 20), with depositories where it has members. Confirm the rows before publishing."
	}
	return ds, nil
}

// complaintsTable works a business's complaints into its tables.
func complaintsTable(cs []Ticket, biz, month string) Disclosure {
	start, end := month+"-01", monthEnd(month)
	open := func(t Ticket, d string) bool { return t.OpenedOn <= d && (t.ResolvedOn == "" || t.ResolvedOn > d) }
	ds := Disclosure{Month: month, Business: biz, Notes: []string{}}
	tot := DisclosureRow{Source: "Grand Total"}
	var totDays, totN int
	for _, def := range discRows[biz] {
		r := DisclosureRow{Source: def.Label}
		var days, n int
		for _, t := range cs {
			if !contains(def.Sources, t.Source) {
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
		r.TotalPending, r.Under3Months = r.PendingAtOpen+r.Received, r.Pending-r.Over3Months
		if n > 0 {
			r.AvgDays = (days + n/2) / n
		}
		totDays, totN = totDays+days, totN+n
		tot.PendingAtOpen += r.PendingAtOpen
		tot.Received += r.Received
		tot.TotalPending += r.TotalPending
		tot.Resolved += r.Resolved
		tot.Pending += r.Pending
		tot.Under3Months += r.Under3Months
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
	total := func(rs []TrendRow) TrendRow {
		t := TrendRow{Month: "Grand Total"}
		for i, r := range rs {
			if i == 0 {
				t.Carried = r.Carried
			}
			t.Received += r.Received
			t.Resolved += r.Resolved
			t.Pending = r.Pending
		}
		return t
	}
	fy := fyStartOf(start)
	for m := fy[:7]; m <= month; m = addMonths(m+"-01", 1)[:7] {
		r := trend(m+"-01", monthEnd(m))
		r.Month = m
		ds.Trend = append(ds.Trend, r)
	}
	ds.TrendTotal = total(ds.Trend)
	// The broker's and the DP's annual trend is five years rolling; the RA
	// and IA annexures list every year from 2021-22.
	first := addMonths(fy, -48)
	if biz == BizRA || biz == BizIA {
		first = "2021-04-01"
	}
	for f := first; f <= fy; f = addMonths(f, 12) {
		r := trend(f, addDays(addMonths(f, 12), -1))
		r.Month = fyLabel(f)
		ds.Annual = append(ds.Annual, r)
	}
	ds.AnnualTotal = total(ds.Annual)
	return ds
}

// disclosureBusinesses are the businesses the firm publishes a table for:
// broking always, and the DP, RA and IA when the firm has them or has
// complaints marked for them.
func (o *Ops) disclosureBusinesses(ctx context.Context, tenant string) []string {
	f, _ := o.firmProfileFactsOpt(ctx, tenant, false)
	has := map[string]bool{BizBroking: true, BizDP: f.dp() == triYes, BizRA: f.Regs["ra"], BizIA: f.Regs["ia"]}
	ts, _ := docs.List[Ticket](ctx, o.Docs, tenant, cTicket, "")
	for _, t := range ts {
		if t.Kind == KindComplaint {
			has[t.biz()] = true
		}
	}
	var out []string
	for _, b := range TicketBusinesses {
		if has[b.ID] {
			out = append(out, b.ID)
		}
	}
	return out
}

// DisclosureBusinesses are the tables the firm publishes.
func (o *Ops) DisclosureBusinesses(ctx context.Context, tenant string) []map[string]string {
	var out []map[string]string
	for _, b := range o.disclosureBusinesses(ctx, tenant) {
		out = append(out, map[string]string{"id": b, "name": bizName(b)})
	}
	return out
}

// ---- the tables as files ----

// discHeadings are the main table's columns as each annexure has them.
func discHeadings(biz string) []string {
	if biz == BizRA || biz == BizIA {
		return []string{"Sr. No.", "Received from", "Pending at the end of last month", "Received", "Resolved*", "Total Pending#", "Pending complaints > 3 months", "Average Resolution time^ (in days)"}
	}
	return []string{"Sr. No.", "Received from", "Carried forward from previous month", "Received during the month", "Total Pending", "Resolved*", "Pending at the end of the month**: pending for less than 3 months", "Pending at the end of the month**: pending for more than 3 months", "Average Resolution time^ (in days)"}
}

func discCells(biz, sn string, r DisclosureRow) []string {
	if biz == BizRA || biz == BizIA {
		return []string{sn, r.Source, fmt.Sprint(r.PendingAtOpen), fmt.Sprint(r.Received), fmt.Sprint(r.Resolved), fmt.Sprint(r.Pending), fmt.Sprint(r.Over3Months), fmt.Sprint(r.AvgDays)}
	}
	return []string{sn, r.Source, fmt.Sprint(r.PendingAtOpen), fmt.Sprint(r.Received), fmt.Sprint(r.TotalPending), fmt.Sprint(r.Resolved), fmt.Sprint(r.Under3Months), fmt.Sprint(r.Over3Months), fmt.Sprint(r.AvgDays)}
}

func trendLabel(m string) string {
	if t, err := time.Parse("2006-01", m); err == nil {
		return t.Format("January, 2006")
	}
	return m
}

// discFootnotes are the annexures' notes.
func discFootnotes(biz string) []string {
	if biz == BizRA || biz == BizIA {
		return []string{"* Inclusive of complaints of previous months resolved in the current month.", "# Inclusive of complaints pending as on the last day of the month.",
			"^ Average Resolution time is the sum total of time taken to resolve each complaint, in days, in the current month divided by total number of complaints resolved in the current month."}
	}
	return []string{"* Should include complaints of previous months resolved in the current month, if any.", "** Should include total complaints pending as on the last day of the month, if any.",
		"^ Average resolution time is the sum total of time taken to resolve each complaint in the current month divided by total number of complaints resolved in the current month."}
}

// CSV is the disclosure as a file: the month's table, the impersonation
// line (RA, IA), the trend of the year and the annual trend.
func (d Disclosure) CSV() []byte {
	rows := [][]string{{d.Title}, {"Data for the month ending " + fmtDay(monthEnd(d.Month))}, discHeadings(d.Business)}
	for i, r := range d.Rows {
		rows = append(rows, discCells(d.Business, fmt.Sprint(i+1), r))
	}
	rows = append(rows, discCells(d.Business, "", d.Total))
	if d.Impersonation != nil {
		rows = append(rows, []string{}, []string{"Number of complaints received during month against the " + d.Business + " due to impersonation by some other entity:", fmt.Sprint(*d.Impersonation)})
	}
	rows = append(rows, []string{}, []string{"Trend of monthly disposal of complaints"}, []string{"Sr. No.", "Month", "Carried forward from previous month", "Received", "Resolved*", "Pending#"})
	for i, r := range d.Trend {
		rows = append(rows, []string{fmt.Sprint(i + 1), trendLabel(r.Month), fmt.Sprint(r.Carried), fmt.Sprint(r.Received), fmt.Sprint(r.Resolved), fmt.Sprint(r.Pending)})
	}
	rows = append(rows, []string{"", "Grand Total", fmt.Sprint(d.TrendTotal.Carried), fmt.Sprint(d.TrendTotal.Received), fmt.Sprint(d.TrendTotal.Resolved), fmt.Sprint(d.TrendTotal.Pending)})
	rows = append(rows, []string{}, []string{"Trend of annual disposal of complaints"}, []string{"Sr. No.", "Year", "Carried forward from previous year", "Received", "Resolved*", "Pending#"})
	for i, r := range d.Annual {
		rows = append(rows, []string{fmt.Sprint(i + 1), r.Month, fmt.Sprint(r.Carried), fmt.Sprint(r.Received), fmt.Sprint(r.Resolved), fmt.Sprint(r.Pending)})
	}
	rows = append(rows, []string{"", "Grand Total", fmt.Sprint(d.AnnualTotal.Carried), fmt.Sprint(d.AnnualTotal.Received), fmt.Sprint(d.AnnualTotal.Resolved), fmt.Sprint(d.AnnualTotal.Pending)}, []string{})
	for _, f := range discFootnotes(d.Business) {
		rows = append(rows, []string{f})
	}
	return csvBytesOf(rows)
}

// HTML is the disclosure as a web page section to publish: plain tables
// in 12-point type, as an IA's home page needs it (IA master circular para
// 2.4); the firm's web team places it.
func (d Disclosure) HTML(firm string) []byte {
	var b strings.Builder
	e := htmlEsc
	td := func(cells []string, tag string) {
		b.WriteString("<tr>")
		for _, c := range cells {
			b.WriteString("<" + tag + ">" + e(c) + "</" + tag + ">")
		}
		b.WriteString("</tr>\n")
	}
	b.WriteString(`<section class="complaints-data" style="font-size:12pt;font-family:Arial,Helvetica,sans-serif">` + "\n")
	b.WriteString("<h2 style=\"font-size:14pt\">" + e(d.Title) + "</h2>\n<p>" + e(firm) + " · Data for the month ending " + e(fmtDay(monthEnd(d.Month))) + "</p>\n")
	tbl := `<table style="border-collapse:collapse;font-size:12pt" border="1" cellpadding="4">` + "\n"
	b.WriteString(tbl)
	td(discHeadings(d.Business), "th")
	for i, r := range d.Rows {
		td(discCells(d.Business, fmt.Sprint(i+1), r), "td")
	}
	td(discCells(d.Business, "", d.Total), "td")
	b.WriteString("</table>\n")
	if d.Impersonation != nil {
		b.WriteString("<p>Number of complaints received during month against the " + e(d.Business) + " due to impersonation by some other entity: <b>" + fmt.Sprint(*d.Impersonation) + "</b></p>\n")
	}
	b.WriteString("<h3 style=\"font-size:13pt\">Trend of monthly disposal of complaints</h3>\n" + tbl)
	td([]string{"Sr. No.", "Month", "Carried forward from previous month", "Received", "Resolved*", "Pending#"}, "th")
	for i, r := range d.Trend {
		td([]string{fmt.Sprint(i + 1), trendLabel(r.Month), fmt.Sprint(r.Carried), fmt.Sprint(r.Received), fmt.Sprint(r.Resolved), fmt.Sprint(r.Pending)}, "td")
	}
	td([]string{"", "Grand Total", fmt.Sprint(d.TrendTotal.Carried), fmt.Sprint(d.TrendTotal.Received), fmt.Sprint(d.TrendTotal.Resolved), fmt.Sprint(d.TrendTotal.Pending)}, "td")
	b.WriteString("</table>\n<h3 style=\"font-size:13pt\">Trend of annual disposal of complaints</h3>\n" + tbl)
	td([]string{"Sr. No.", "Year", "Carried forward from previous year", "Received", "Resolved*", "Pending#"}, "th")
	for i, r := range d.Annual {
		td([]string{fmt.Sprint(i + 1), r.Month, fmt.Sprint(r.Carried), fmt.Sprint(r.Received), fmt.Sprint(r.Resolved), fmt.Sprint(r.Pending)}, "td")
	}
	td([]string{"", "Grand Total", fmt.Sprint(d.AnnualTotal.Carried), fmt.Sprint(d.AnnualTotal.Received), fmt.Sprint(d.AnnualTotal.Resolved), fmt.Sprint(d.AnnualTotal.Pending)}, "td")
	b.WriteString("</table>\n")
	for _, f := range discFootnotes(d.Business) {
		b.WriteString("<p style=\"font-size:12pt\">" + e(f) + "</p>\n")
	}
	b.WriteString("</section>\n")
	return []byte(b.String())
}

func htmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace(s)
}
