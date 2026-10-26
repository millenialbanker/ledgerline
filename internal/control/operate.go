package control

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- notices to firms ----

// NoticeRec is a notice Control has sent to firms.
type NoticeRec struct {
	Notice
	Target    string   `json:"target"` // all, customer:<id>, firm:<id>, install:<id>
	Firms     []string `json:"firms"`
	Commands  []string `json:"commands"`
	By        string   `json:"by"`
	Created   string   `json:"created"`
	Withdrawn string   `json:"withdrawn,omitempty"`
}

// NoticeInput is a new notice.
type NoticeInput struct {
	Title    string `json:"title"`
	Text     string `json:"text"`
	Level    string `json:"level"`
	Audience string `json:"audience"`
	From     string `json:"from"`  // RFC3339 or YYYY-MM-DDTHH:MM (IST); empty = now
	Until    string `json:"until"` // same; empty = 7 days
	Target   string `json:"target"`
}

func parseWhen(s string, def time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	for _, f := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(f, s, IST); err == nil {
			return t, nil
		}
	}
	return def, invalid("dates must look like 2026-11-01T22:00")
}

func (c *Control) targetFirms(ctx context.Context, target string) ([]Firm, error) {
	fs, err := docs.List[Firm](ctx, c.Docs, T, cFirm, "")
	if err != nil {
		return nil, err
	}
	kind, id, _ := strings.Cut(target, ":")
	var out []Firm
	for _, f := range fs {
		if (kind == "all" || kind == "") ||
			(kind == "customer" && f.Customer == id) ||
			(kind == "firm" && f.ID == id) ||
			(kind == "install" && f.Installation == id) {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return nil, invalid("no firm matches that choice")
	}
	return out, nil
}

// SendNotice queues a notice for every firm in the target; each sees it in
// Ledgerline after its next report (within about 15 minutes).
func (c *Control) SendNotice(ctx context.Context, by string, in NoticeInput) (NoticeRec, error) {
	if err := c.need(ctx, by, RoleSupport, RoleBilling); err != nil {
		return NoticeRec{}, err
	}
	in.Title, in.Text = clean(in.Title, 120), clean(in.Text, 2000)
	if in.Title == "" {
		return NoticeRec{}, invalid("give the notice a title")
	}
	from, err := parseWhen(in.From, c.now())
	if err != nil {
		return NoticeRec{}, err
	}
	until, err := parseWhen(in.Until, from.AddDate(0, 0, 7))
	if err != nil {
		return NoticeRec{}, err
	}
	if !until.After(from) || until.Sub(from) > 120*24*time.Hour {
		return NoticeRec{}, invalid("show it for up to 120 days, ending after it starts")
	}
	if in.Level != "warn" && in.Level != "bad" {
		in.Level = "info"
	}
	if in.Audience != "admins" {
		in.Audience = "all"
	}
	fs, err := c.targetFirms(ctx, firstOf(in.Target, "all"))
	if err != nil {
		return NoticeRec{}, err
	}
	n, err := c.next(ctx, "notice")
	if err != nil {
		return NoticeRec{}, err
	}
	rec := NoticeRec{Notice: Notice{ID: fmt.Sprintf("NTC-%05d", n), Title: in.Title, Text: in.Text, Level: in.Level, Audience: in.Audience, From: from.Format(time.RFC3339), Until: until.Format(time.RFC3339)},
		Target: firstOf(in.Target, "all"), By: by, Created: c.stamp(), Firms: []string{}, Commands: []string{}}
	for _, f := range fs {
		nt := rec.Notice
		x, err := c.queueCommand(ctx, by, f, Command{Kind: "notice", Notice: &nt, Ref: rec.ID})
		if err != nil {
			return rec, err
		}
		rec.Firms, rec.Commands = append(rec.Firms, f.ID), append(rec.Commands, x.ID)
	}
	if err := docs.Upsert(ctx, c.Docs, T, cNotice, rec.ID, rec, by); err != nil {
		return rec, err
	}
	c.log(ctx, by, "notice.send", rec.ID, fmt.Sprintf("%s to %d firms", rec.Title, len(fs)))
	return rec, nil
}

func (c *Control) queueCommand(ctx context.Context, by string, f Firm, cmd Command) (CommandRec, error) {
	n, err := c.next(ctx, "command")
	if err != nil {
		return CommandRec{}, err
	}
	cmd.ID, cmd.Tenant = fmt.Sprintf("CMD-%05d", n), f.Tenant
	x := CommandRec{Command: cmd, Installation: f.Installation, Firm: f.ID, Customer: f.Customer, Status: "Queued", Created: c.stamp(), By: by}
	return x, docs.Upsert(ctx, c.Docs, T, cCommand, x.ID, x, by)
}

// WithdrawNotice takes a notice down everywhere.
func (c *Control) WithdrawNotice(ctx context.Context, by, id string) (NoticeRec, error) {
	if err := c.need(ctx, by, RoleSupport, RoleBilling); err != nil {
		return NoticeRec{}, err
	}
	rec, _, err := docs.Get[NoticeRec](ctx, c.Docs, T, cNotice, id)
	if err != nil {
		return rec, ErrNotFound
	}
	if rec.Withdrawn != "" {
		return rec, invalid("already withdrawn")
	}
	for _, fid := range rec.Firms {
		f, _, err := docs.Get[Firm](ctx, c.Docs, T, cFirm, fid)
		if err != nil {
			continue
		}
		// Not yet delivered: cancel it instead.
		cancelled := false
		for _, cid := range rec.Commands {
			if _, err := docs.Update(ctx, c.Docs, T, cCommand, cid, by, func(x *CommandRec) error {
				if x.Firm != fid || x.Status != "Queued" || x.Sent != "" {
					return ErrForbidden
				}
				x.Status, x.Done, x.Error = "Cancelled", c.stamp(), "notice withdrawn"
				return nil
			}); err == nil {
				cancelled = true
			}
		}
		if !cancelled {
			if _, err := c.queueCommand(ctx, by, f, Command{Kind: "notice_withdraw", Ref: rec.ID}); err != nil {
				return rec, err
			}
		}
	}
	out, err := docs.Update(ctx, c.Docs, T, cNotice, id, by, func(x *NoticeRec) error { x.Withdrawn = c.stamp(); return nil })
	c.log(ctx, by, "notice.withdraw", id, "")
	return out, err
}

// NoticeView is a notice with how far it has got.
type NoticeView struct {
	NoticeRec
	Delivered int  `json:"delivered"`
	Live      bool `json:"live"`
}

// Notices lists notices sent, newest first.
func (c *Control) Notices(ctx context.Context, by string) ([]NoticeView, error) {
	if err := c.need(ctx, by); err != nil {
		return nil, err
	}
	all, err := docs.List[NoticeRec](ctx, c.Docs, T, cNotice, "")
	cmds, _ := docs.List[CommandRec](ctx, c.Docs, T, cCommand, "")
	done := map[string]bool{}
	for _, x := range cmds {
		if x.Status == "Done" {
			done[x.ID] = true
		}
	}
	now := c.now().Format(time.RFC3339)
	out := []NoticeView{}
	for _, n := range all {
		v := NoticeView{NoticeRec: n, Live: n.Withdrawn == "" && n.From <= now && n.Until > now}
		for _, cid := range n.Commands {
			if done[cid] {
				v.Delivered++
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, err
}

// ---- edition changes ----

// SetFirmEdition changes a firm's edition (applied at its next report).
// The new edition is billed from the month it takes effect (the highest
// edition used in a month is billed).
func (c *Control) SetFirmEdition(ctx context.Context, by, firm, edition string) (CommandRec, error) {
	if err := c.need(ctx, by, RoleBilling); err != nil {
		return CommandRec{}, err
	}
	if _, ok := editionRank[edition]; !ok {
		return CommandRec{}, invalid("edition must be manual, autopilot or full")
	}
	f, _, err := docs.Get[Firm](ctx, c.Docs, T, cFirm, firm)
	if err != nil {
		return CommandRec{}, ErrNotFound
	}
	if f.Latest.Edition == edition {
		return CommandRec{}, invalid("the firm is already on %s", editionName[edition])
	}
	x, err := c.queueCommand(ctx, by, f, Command{Kind: "edition", Edition: edition})
	if err == nil {
		c.log(ctx, by, "firm.edition", firm, f.Latest.Edition+" → "+edition)
	}
	return x, err
}

// ---- alerts ----

// Alert is something that needs the provider's attention.
type Alert struct {
	ID       string `json:"id"` // stable, so it can be acknowledged
	Severity string `json:"severity"`
	Area     string `json:"area"` // Servers, Firms, Go-live, Billing, Support, Customers
	Subject  string `json:"subject"`
	Detail   string `json:"detail"`
	Link     string `json:"link"`
	Acked    string `json:"acked,omitempty"`
	AckedBy  string `json:"acked_by,omitempty"`
}

type alertAck struct {
	At, By, Alert string
}

// versionLess compares dotted versions (1.10 > 1.9).
func versionLess(a, b string) bool {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(strings.TrimFunc(pa[i], func(r rune) bool { return r < '0' || r > '9' }))
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(strings.TrimFunc(pb[i], func(r rune) bool { return r < '0' || r > '9' }))
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// Alerts lists what needs attention across servers, firms, billing and
// support, worst first. Acknowledged alerts stay listed (marked) for 7 days.
func (c *Control) Alerts(ctx context.Context, by string) ([]Alert, error) {
	if err := c.need(ctx, by); err != nil {
		return nil, err
	}
	now := c.now()
	set, _ := c.Settings(ctx)
	var out []Alert
	add := func(id, sev, area, subj, detail, link string) {
		out = append(out, Alert{ID: id, Severity: sev, Area: area, Subject: subj, Detail: detail, Link: link})
	}
	fs, im, err := c.firmViews(ctx)
	if err != nil {
		return nil, err
	}
	for _, in := range im {
		if in.Revoked {
			continue
		}
		if !c.Online(in) {
			add("down:"+in.ID, "bad", "Servers", in.Name, "not reporting; last report "+firstOf(in.LastReport, "never"), "installs")
		} else if set.Ops.LatestVersion != "" && versionLess(in.Info.Version, set.Ops.LatestVersion) {
			add("ver:"+in.ID+":"+set.Ops.LatestVersion, "warn", "Servers", in.Name, fmt.Sprintf("runs %s; latest is %s", firstOf(in.Info.Version, "?"), set.Ops.LatestVersion), "installs")
		}
	}
	today := now.Format("2006-01-02")
	for _, f := range fs {
		r, h, name := f.Latest, f.Latest.Health, firstOf(f.Latest.Firm, f.Tenant)
		link := "customer/" + f.Customer
		if !f.Online {
			continue // covered by the server alert
		}
		if h.BackupProblem != "" {
			add("backup:"+f.ID, "bad", "Firms", name, "backups: "+h.BackupProblem, link)
		} else if t, err := time.Parse(time.RFC3339, h.LastBackup); err != nil || now.Sub(t) > 36*time.Hour {
			add("backupold:"+f.ID, "warn", "Firms", name, "no good backup in the last 36 hours", link)
		}
		if h.EOD == "open" && r.BusinessDt != "" && r.BusinessDt < addDaysStr(today, -1) {
			add("eod:"+f.ID+":"+r.BusinessDt, "warn", "Firms", name, "day "+r.BusinessDt+" is still open", link)
		}
		if r.Bots.Failed > 0 {
			add("bots:"+f.ID+":"+r.Month, "info", "Firms", name, fmt.Sprintf("%d automation runs failed this month", r.Bots.Failed), link)
		}
		if r.AI.On && r.AI.Balance < int64(money.MustAmount("200")) {
			add("ai:"+f.ID, "warn", "Billing", name, "Ledgy AI credit is low: ₹"+inr(money.Amount(r.AI.Balance)), link)
		}
		for _, s := range r.Setup {
			if s.Key == "backups" {
				continue // covered by the backup alerts above
			}
			switch {
			case !s.OK:
				add("setup:"+f.ID+":"+s.Key, map[bool]string{true: "bad", false: "warn"}[s.Key == "signing" || s.Key == "mail"], "Go-live", name, s.Label+": "+firstOf(s.Detail, "not done"), link)
			case s.Days > 0 && s.Days <= 30:
				add("expiry:"+f.ID+":"+s.Key, map[bool]string{true: "bad", false: "warn"}[s.Days <= 7], "Go-live", name, fmt.Sprintf("%s expires in %d days", s.Label, s.Days), link)
			}
		}
	}
	cus, _ := docs.List[Customer](ctx, c.Docs, T, cCustomer, "")
	for _, cu := range cus {
		link := "customer/" + cu.ID
		if cu.Status == "Trial" && cu.TrialEnds != "" {
			switch d := daysBetween(today, cu.TrialEnds); {
			case d < 0:
				add("trial:"+cu.ID, "warn", "Customers", cu.Name, "trial ended "+cu.TrialEnds+": move to Active (billed) or Left", link)
			case d <= 7:
				add("trial:"+cu.ID, "info", "Customers", cu.Name, fmt.Sprintf("trial ends in %d days", d), link)
			}
		}
		if (cu.Status == "Active" || cu.Status == "Trial") && cu.ContractEnd != "" {
			if d := daysBetween(today, cu.ContractEnd); d <= 30 {
				add("renew:"+cu.ID+":"+cu.ContractEnd, map[bool]string{true: "bad", false: "warn"}[d < 0], "Customers", cu.Name, fmt.Sprintf("contract %s on %s", map[bool]string{true: "ended", false: "renews"}[d < 0], cu.ContractEnd), link)
			}
		}
		if cu.Status == "Active" && (strings.TrimSpace(cu.Address) == "" || firstOf(cu.BillingEmail, cu.Email) == "") {
			add("billdetails:"+cu.ID, "info", "Billing", cu.Name, "billing address or email missing", link)
		}
	}
	if c.need(ctx, by, RoleBilling) == nil {
		due, _ := c.RemindersDue(ctx, by)
		for _, d := range due {
			sev := "warn"
			if d.Suspend {
				sev = "bad"
			}
			msg := fmt.Sprintf("₹%s overdue %d days on %s", inr(d.Balance), d.Days, d.Number)
			if d.Suspend {
				msg += " · past the suspension limit"
			}
			add("overdue:"+d.Invoice, sev, "Billing", d.Name, msg, "invoice/"+d.Invoice)
		}
	}
	ts, _ := docs.List[Ticket](ctx, c.Docs, T, cTicket, "")
	for _, t := range ts {
		if c.Overdue(t) {
			add("sla:"+t.ID, "bad", "Support", t.FirmName, TicketNo(t)+" "+t.Subject+": no first reply yet ("+t.Priority+")", "ticket/"+t.ID)
		}
	}
	acks, _ := docs.List[alertAck](ctx, c.Docs, T, cAlertAck, "")
	ackOf := map[string]alertAck{}
	for _, a := range acks {
		ackOf[a.Alert] = a
	}
	for i := range out {
		if a, ok := ackOf[out[i].ID]; ok {
			if t, err := time.Parse(time.RFC3339, a.At); err == nil && now.Sub(t) < 7*24*time.Hour {
				out[i].Acked, out[i].AckedBy = a.At, a.By
			}
		}
	}
	rank := map[string]int{"bad": 0, "warn": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Acked == "") != (out[j].Acked == "") {
			return out[i].Acked == ""
		}
		return rank[out[i].Severity] < rank[out[j].Severity]
	})
	if out == nil {
		out = []Alert{}
	}
	return out, nil
}

// AckAlert marks an alert as looked at (it is set aside for 7 days).
func (c *Control) AckAlert(ctx context.Context, by, id string) error {
	if err := c.need(ctx, by); err != nil {
		return err
	}
	id = clean(id, 200)
	if id == "" {
		return invalid("which alert?")
	}
	return docs.Upsert(ctx, c.Docs, T, cAlertAck, sha(id)[:24], alertAck{At: c.stamp(), By: by, Alert: id}, by)
}

func addDaysStr(d string, n int) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

func daysBetween(a, b string) int {
	x, err1 := time.Parse("2006-01-02", a)
	y, err2 := time.Parse("2006-01-02", b)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(y.Sub(x).Hours() / 24)
}

// ---- support: saved replies and numbers ----

// Canned is a saved reply.
type Canned struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Text    string `json:"text"`
	By      string `json:"by"`
	Updated string `json:"updated"`
}

// CannedReplies lists saved replies.
func (c *Control) CannedReplies(ctx context.Context, by string) ([]Canned, error) {
	if err := c.need(ctx, by); err != nil {
		return nil, err
	}
	all, err := docs.List[Canned](ctx, c.Docs, T, cCanned, "")
	sort.Slice(all, func(i, j int) bool { return strings.ToLower(all[i].Title) < strings.ToLower(all[j].Title) })
	if all == nil {
		all = []Canned{}
	}
	return all, err
}

// SaveCanned adds, changes or (with an empty text) removes a saved reply.
func (c *Control) SaveCanned(ctx context.Context, by string, x Canned) (Canned, error) {
	if err := c.need(ctx, by, RoleSupport); err != nil {
		return x, err
	}
	x.Title, x.Text = clean(x.Title, 80), clean(x.Text, 4000)
	if x.ID != "" && x.Text == "" {
		_, err := docs.Update(ctx, c.Docs, T, cCanned, x.ID, by, func(y *Canned) error { y.Text, y.Title = "", "(removed)"; return nil })
		return x, err
	}
	if x.Title == "" || x.Text == "" {
		return x, invalid("give the reply a title and text")
	}
	if x.ID == "" {
		n, err := c.next(ctx, "canned")
		if err != nil {
			return x, err
		}
		x.ID = fmt.Sprintf("CAN-%03d", n)
	}
	x.By, x.Updated = by, c.stamp()
	return x, docs.Upsert(ctx, c.Docs, T, cCanned, x.ID, x, by)
}

// SupportStats is how support did in a month.
type SupportStats struct {
	Month        string         `json:"month"`
	Opened       int            `json:"opened"`
	Resolved     int            `json:"resolved"`
	Answered     int            `json:"answered"`
	OnTime       int            `json:"on_time"` // first reply within target
	MedianFirst  float64        `json:"median_first_hours"`
	MedianSolve  float64        `json:"median_resolve_hours"`
	ByCategory   map[string]int `json:"by_category"`
	ByPriority   map[string]int `json:"by_priority"`
	ByFirm       []LabelCount   `json:"by_firm"`
	ByStaff      []LabelCount   `json:"by_staff"`
	OpenNow      int            `json:"open_now"`
	OldestOpenHr float64        `json:"oldest_open_hours"`
}

// LabelCount is one bar.
type LabelCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Float64s(xs)
	m := xs[len(xs)/2]
	if len(xs)%2 == 0 {
		m = (xs[len(xs)/2-1] + m) / 2
	}
	return float64(int(m*10+0.5)) / 10
}

func topCounts(m map[string]int, n int) []LabelCount {
	out := []LabelCount{}
	for k, v := range m {
		out = append(out, LabelCount{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Count > out[j].Count || (out[i].Count == out[j].Count && out[i].Label < out[j].Label)
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// SupportNumbers is support's month: requests, first replies against the
// targets, time to resolve, and where requests come from.
func (c *Control) SupportNumbers(ctx context.Context, by, month string) (SupportStats, error) {
	if err := c.need(ctx, by); err != nil {
		return SupportStats{}, err
	}
	if _, err := time.Parse("2006-01", month); err != nil {
		month = c.month()
	}
	s := SupportStats{Month: month, ByCategory: map[string]int{}, ByPriority: map[string]int{}}
	ts, err := docs.List[Ticket](ctx, c.Docs, T, cTicket, "")
	firms, staff := map[string]int{}, map[string]int{}
	var first, solve []float64
	hours := func(a, b string) (float64, bool) {
		x, e1 := time.Parse(time.RFC3339, a)
		y, e2 := time.Parse(time.RFC3339, b)
		return y.Sub(x).Hours(), e1 == nil && e2 == nil
	}
	for _, t := range ts {
		if t.Status != "Resolved" && t.Status != "Closed" {
			s.OpenNow++
			if h, ok := hours(t.Created, c.stamp()); ok && h > s.OldestOpenHr {
				s.OldestOpenHr = float64(int(h*10)) / 10
			}
		}
		if !strings.HasPrefix(t.Created, month) {
			if strings.HasPrefix(t.ResolvedAt, month) {
				s.Resolved++
			}
			continue
		}
		s.Opened++
		s.ByCategory[firstOf(t.Category, "Other")]++
		s.ByPriority[t.Priority]++
		firms[t.FirmName]++
		if t.Assignee != "" {
			staff[t.Assignee]++
		}
		if t.FirstResponse != "" {
			s.Answered++
			if h, ok := hours(t.Created, t.FirstResponse); ok {
				first = append(first, h)
				if h <= SLA[t.Priority].Hours() {
					s.OnTime++
				}
			}
		}
		if t.ResolvedAt != "" {
			if strings.HasPrefix(t.ResolvedAt, month) {
				s.Resolved++
			}
			if h, ok := hours(t.Created, t.ResolvedAt); ok {
				solve = append(solve, h)
			}
		}
	}
	s.MedianFirst, s.MedianSolve = median(first), median(solve)
	s.ByFirm, s.ByStaff = topCounts(firms, 8), topCounts(staff, 8)
	return s, err
}

// ---- notes on a customer ----

// CustNote is a dated note on a customer: a call, a meeting, a promise.
type CustNote struct {
	ID       string `json:"id"`
	Customer string `json:"customer"`
	Kind     string `json:"kind"` // Note, Call, Meeting, Email, Follow-up
	Text     string `json:"text"`
	Due      string `json:"due,omitempty"` // for a follow-up
	Done     bool   `json:"done,omitempty"`
	By       string `json:"by"`
	At       string `json:"at"`
}

// AddCustNote records a note on a customer, or marks a follow-up done.
func (c *Control) AddCustNote(ctx context.Context, by string, n CustNote) (CustNote, error) {
	if err := c.need(ctx, by); err != nil {
		return n, err
	}
	if n.ID != "" {
		return docs.Update(ctx, c.Docs, T, cNote, n.ID, by, func(x *CustNote) error { x.Done = n.Done; return nil })
	}
	if _, _, err := docs.Get[Customer](ctx, c.Docs, T, cCustomer, n.Customer); err != nil {
		return n, ErrNotFound
	}
	n.Text = clean(n.Text, 4000)
	if n.Text == "" {
		return n, invalid("write the note")
	}
	if !has([]string{"Note", "Call", "Meeting", "Email", "Follow-up"}, n.Kind) {
		n.Kind = "Note"
	}
	if n.Due != "" {
		if _, err := time.Parse("2006-01-02", n.Due); err != nil {
			return n, invalid("follow-up date must be YYYY-MM-DD")
		}
	}
	k, err := c.next(ctx, "cnote")
	if err != nil {
		return n, err
	}
	n.ID, n.By, n.At = fmt.Sprintf("NOTE-%05d", k), by, c.stamp()
	return n, docs.Upsert(ctx, c.Docs, T, cNote, n.ID, n, by)
}

// CustNotes lists a customer's notes (or every open follow-up when
// customer is empty), newest first.
func (c *Control) CustNotes(ctx context.Context, by, customer string) ([]CustNote, error) {
	if err := c.need(ctx, by); err != nil {
		return nil, err
	}
	all, err := docs.List[CustNote](ctx, c.Docs, T, cNote, "")
	out := []CustNote{}
	for _, n := range all {
		if (customer != "" && n.Customer == customer) || (customer == "" && n.Kind == "Follow-up" && !n.Done) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out, err
}
