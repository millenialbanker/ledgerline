package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- security incidents: the register, the CERT-In clock, alerts ----
//
// An incident is opened by a detection rule (secevents.go), recorded by the
// firm itself (a phishing mail, a lost laptop), or declared by Ledgerline
// for the platform (an outage or a breach affecting firms). Each has the
// CERT-In clock: the directions of 28 April 2022 ask for a report within 6
// hours of noticing the incident or being told of it, so the deadline is
// the time of detection (or of Ledgerline's notice) plus 6 hours, shown in
// IST. Whether an incident must be reported is the firm's judgement against
// the types in the directions' Annexure I; the register records the report
// to CERT-In (and to SEBI, the exchanges or the depositories, which ask for
// the same under CSCRF) with when, how and the reference number.
//
// One alert per incident: when a rule matches again for the same subject
// while its incident is not closed, the event joins that incident and no
// new alert is sent.

const (
	cIncident = "incident" // INC-nnnnnn (detected or recorded), PINC-nnnnnn (from Ledgerline)
	cSecOpen  = "secopen"  // rule|key -> the open incident it joins
	// CERTInHours is the reporting time limit (direction (ii)).
	CERTInHours = 6
)

// Incident statuses.
const (
	IncOpen          = "Open"
	IncInvestigating = "Investigating"
	IncReported      = "Reported"
	IncClosed        = "Closed"
)

// IncidentStatuses in their order.
var IncidentStatuses = []string{IncOpen, IncInvestigating, IncReported, IncClosed}

// ReportTo and ReportHow are the choices for the report step.
var (
	ReportTo  = []string{"CERT-In", "SEBI", "Exchange", "Depository", "Other"}
	ReportHow = []string{"Email", "Portal", "Phone", "Letter", "Other"}
)

// IncidentNote is one entry in an incident's history.
type IncidentNote struct {
	At     string `json:"at"`
	By     string `json:"by"`
	Text   string `json:"text"`
	Status string `json:"status,omitempty"` // the status it moved to
}

// IncidentReport is a report made to an authority.
type IncidentReport struct {
	To        string `json:"to"`  // CERT-In, SEBI, Exchange, Depository, Other
	At        string `json:"at"`  // when it was reported (RFC 3339)
	How       string `json:"how"` // Email, Portal, Phone, Letter, Other
	Reference string `json:"reference,omitempty"`
	Note      string `json:"note,omitempty"`
	By        string `json:"by"`
	Recorded  string `json:"recorded"`
}

// IncidentAlert is the alert sent when the incident opened.
type IncidentAlert struct {
	At      string   `json:"at"`
	To      []string `json:"to"`
	Results []string `json:"results"`
	Work    []string `json:"work"`
}

// Incident is one entry in the register.
type Incident struct {
	ID          string           `json:"id"`
	Source      string           `json:"source"` // detected, firm, provider
	Rule        string           `json:"rule,omitempty"`
	Key         string           `json:"key,omitempty"`
	Kind        string           `json:"kind"`
	Title       string           `json:"title"`
	Description string           `json:"description,omitempty"`
	Severity    string           `json:"severity"`
	DetectedAt  string           `json:"detected_at"` // when it was noticed: the CERT-In clock starts here
	FirstSeen   string           `json:"first_seen"`
	LastSeen    string           `json:"last_seen"`
	Events      []string         `json:"events"` // the first 200
	EventCount  int              `json:"event_count"`
	Status      string           `json:"status"`
	Owner       string           `json:"owner,omitempty"`
	Notes       []IncidentNote   `json:"notes"`
	Reports     []IncidentReport `json:"reports"`
	Alert       *IncidentAlert   `json:"alert,omitempty"`
	// From Ledgerline: the provider's incident, its status and the firms
	// it was sent to (on the platform's own record).
	Platform       string   `json:"platform,omitempty"`
	ProviderStatus string   `json:"provider_status,omitempty"`
	Affected       []string `json:"affected,omitempty"`
	Updated        string   `json:"updated"`
	UpdatedBy      string   `json:"updated_by"`
	ClosedAt       string   `json:"closed_at,omitempty"`
	ClosedBy       string   `json:"closed_by,omitempty"`
}

// CertInClock is the 6-hour reporting clock of an incident, in IST.
type CertInClock struct {
	Detected    string `json:"detected"` // RFC 3339, +05:30
	Deadline    string `json:"deadline"`
	DetectedIST string `json:"detected_ist"` // "20 Nov 2026 14:05 IST"
	DeadlineIST string `json:"deadline_ist"`
	ElapsedMin  int    `json:"elapsed_min"`
	LeftMin     int    `json:"left_min"` // negative once past
	Overdue     bool   `json:"overdue"`  // past the deadline and not reported to CERT-In
	Reported    bool   `json:"reported"`
	ReportedAt  string `json:"reported_at,omitempty"`
	Late        bool   `json:"late,omitempty"` // reported after the deadline
	Line        string `json:"line"`
}

// certInClock works out the clock from the detection time. A closed
// incident that was never reported stops counting (the firm judged it not
// reportable).
func certInClock(detected, now time.Time, reportedAt string, closed bool) CertInClock {
	d := detected.In(ist)
	dl := d.Add(CERTInHours * time.Hour)
	f := func(t time.Time) string { return t.In(ist).Format("02 Jan 2006 15:04") + " IST" }
	c := CertInClock{Detected: d.Format(time.RFC3339), Deadline: dl.Format(time.RFC3339), DetectedIST: f(d), DeadlineIST: f(dl)}
	c.ElapsedMin = int(now.Sub(d) / time.Minute)
	c.LeftMin = int(dl.Sub(now) / time.Minute)
	if reportedAt != "" {
		c.Reported, c.ReportedAt = true, reportedAt
		if t, err := time.Parse(time.RFC3339, reportedAt); err == nil && t.After(dl) {
			c.Late = true
		}
	}
	switch {
	case c.Reported && c.Late:
		c.Line = "Reported to CERT-In after the 6-hour limit (" + c.DeadlineIST + ")"
	case c.Reported:
		c.Line = "Reported to CERT-In within 6 hours"
	case closed:
		c.Line = "Closed without a report to CERT-In"
	case c.LeftMin < 0:
		c.Overdue = true
		c.Line = fmt.Sprintf("If reportable, CERT-In's 6 hours ran out at %s (%s ago)", c.DeadlineIST, minutesText(-c.LeftMin))
	default:
		c.Line = fmt.Sprintf("If reportable, report to CERT-In by %s (%s left)", c.DeadlineIST, minutesText(c.LeftMin))
	}
	return c
}

func minutesText(m int) string {
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	if m < 48*60 {
		return fmt.Sprintf("%d h %02d min", m/60, m%60)
	}
	return fmt.Sprintf("%d days", m/(24*60))
}

// IncidentView is an incident with its clock.
type IncidentView struct {
	Incident
	Clock CertInClock `json:"clock"`
}

func (o *Ops) incidentView(in Incident) IncidentView {
	t, err := time.Parse(time.RFC3339, in.DetectedAt)
	if err != nil {
		t = o.now()
	}
	rep := ""
	for _, r := range in.Reports {
		if r.To == "CERT-In" && (rep == "" || r.At < rep) {
			rep = r.At
		}
	}
	if in.Notes == nil {
		in.Notes = []IncidentNote{}
	}
	if in.Reports == nil {
		in.Reports = []IncidentReport{}
	}
	if in.Events == nil {
		in.Events = []string{}
	}
	return IncidentView{Incident: in, Clock: certInClock(t, o.now(), rep, in.Status == IncClosed)}
}

// raiseIncident opens an incident for a rule match, or adds the event to
// the open incident of the same rule and key. Only a new incident alerts.
func (o *Ops) raiseIncident(ctx context.Context, tenant, rule, key, title, sev string, e *SecEvent, now time.Time) (string, bool) {
	dk := rule + "|" + key
	ts := now.Format(time.RFC3339)
	if open, _, err := docs.Get[secOpen](ctx, o.Docs, tenant, cSecOpen, dk); err == nil && open.Incident != "" {
		joined := false
		_, err := upd(ctx, o.Docs, tenant, cIncident, open.Incident, SystemActor, nil, func(in *Incident) error {
			if in.Status == IncClosed {
				return errIncidentClosed
			}
			in.LastSeen, in.EventCount, joined = ts, in.EventCount+1, true
			if len(in.Events) < 200 {
				in.Events = append(in.Events, e.ID)
			}
			if incSevRank(e.Severity) < incSevRank(in.Severity) && e.Severity != SevInfo {
				in.Severity = e.Severity
			}
			return nil
		})
		if err == nil && joined {
			return open.Incident, false
		}
	}
	n, err := o.next(ctx, tenant, "incident", SystemActor)
	if err != nil {
		return "", false
	}
	in := Incident{ID: fmt.Sprintf("INC-%06d", n), Source: "detected", Rule: rule, Key: key, Kind: e.Kind, Title: trim(title, 200), Severity: sev,
		DetectedAt: ts, FirstSeen: ts, LastSeen: ts, Events: []string{e.ID}, EventCount: 1, Status: IncOpen,
		Notes: []IncidentNote{{At: ts, By: SystemActor, Text: "Opened by the rule \"" + ruleName(rule) + "\": " + e.Message, Status: IncOpen}}, Reports: []IncidentReport{}, Updated: ts, UpdatedBy: SystemActor}
	if err := docs.Upsert(ctx, o.Docs, tenant, cIncident, in.ID, in, SystemActor); err != nil {
		return "", false
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cSecOpen, dk, secOpen{Incident: in.ID}, SystemActor)
	o.log(ctx, tenant, SystemActor, "security.incident.opened", in.ID, map[string]any{"rule": rule, "severity": sev, "title": in.Title})
	o.alertIncident(ctx, tenant, &in, e.Message)
	return in.ID, true
}

var errIncidentClosed = errors.New("incident closed")

type secOpen struct {
	Incident string `json:"incident"` // empty once closed
}

func incSevRank(s string) int {
	for i, x := range Severities {
		if x == s {
			return i
		}
	}
	return len(Severities)
}

func ruleName(id string) string {
	for _, r := range SecRules(SecuritySettings{}) {
		if r.ID == id {
			return r.Name
		}
	}
	return id
}

// alertIncident e-mails the firm's security contacts and puts the
// incident on the compliance officer's and the Admin's work queues.
func (o *Ops) alertIncident(ctx context.Context, tenant string, in *Incident, what string) {
	set, _ := o.Settings(ctx, tenant)
	v := o.incidentView(*in)
	subject := fmt.Sprintf("[Security incident] %s %s: %s", in.ID, in.Severity, in.Title)
	body := fmt.Sprintf("%s\n\nIncident %s (%s), %s.\nDetected %s.\n%s\n\nWhat happened: %s\n\n"+
		"Open Ledgerline → Security → Incidents to see the events, record what you find and, if it is reportable, the report to CERT-In "+
		"(incident@cert-in.org.in or the CERT-In website) and to SEBI or the exchanges as CSCRF requires. CERT-In's directions ask for a report within 6 hours of noticing the incident.\n\n%s",
		in.Title, in.ID, in.Severity, sourceText(in.Source), v.Clock.DetectedIST, v.Clock.Line, firstOf(what, in.Description), firstOf(set.Firm, "Ledgerline"))
	if in.Source == "provider" {
		subject = fmt.Sprintf("[Ledgerline incident notice] %s: %s", in.Platform, in.Title)
	}
	al := &IncidentAlert{At: o.now().Format(time.RFC3339), To: o.alertContacts(ctx, tenant), Results: []string{}}
	for _, to := range al.To {
		al.Results = append(al.Results, to+": "+o.mailStaff(ctx, tenant, to, subject, body, in.ID, nil))
	}
	if len(al.To) == 0 {
		al.Results = append(al.Results, "No e-mail: set the security contacts under Security → Incidents → Settings")
	}
	today := o.now().Format("2006-01-02")
	detail := in.Title + ". " + v.Clock.Line + "."
	for _, r := range []string{RoleCompliance, RoleAdmin} {
		k := in.ID + "|" + map[string]string{RoleCompliance: "co", RoleAdmin: "admin"}[r]
		o.raiseWork(ctx, tenant, "security", k, "Security incident "+in.ID+": "+in.Title, detail, "", r, "incidents", today)
		al.Work = append(al.Work, "W-security-"+strings.ReplaceAll(k, "|", "-"))
	}
	in.Alert = al
	_ = docs.Upsert(ctx, o.Docs, tenant, cIncident, in.ID, *in, SystemActor)
}

func sourceText(s string) string {
	switch s {
	case "provider":
		return "notified by Ledgerline (the software provider)"
	case "firm":
		return "recorded by the firm"
	}
	return "detected by Ledgerline's security rules"
}

// IncidentsScreen is the register with the settings and the rules.
type IncidentsScreen struct {
	Incidents []IncidentView     `json:"incidents"`
	Open      int                `json:"open"`
	Overdue   int                `json:"overdue"`
	Settings  SecuritySettings   `json:"settings"`
	Contacts  []string           `json:"contacts"` // where alerts go now
	Rules     []SecRule          `json:"rules"`
	Kinds     map[string]SecKind `json:"kinds"`
	Statuses  []string           `json:"statuses"`
	ReportTo  []string           `json:"report_to"`
	ReportHow []string           `json:"report_how"`
	Retention string             `json:"retention"`
	Now       string             `json:"now"`
}

// Incidents is the register, newest first. Compliance and Admin.
func (o *Ops) Incidents(ctx context.Context, tenant, actor string) (IncidentsScreen, error) {
	if err := o.require(ctx, tenant, actor, "Seeing security events and incidents", RoleCompliance); err != nil {
		return IncidentsScreen{}, err
	}
	xs, err := docs.List[Incident](ctx, o.Docs, tenant, cIncident, "")
	if err != nil {
		return IncidentsScreen{}, err
	}
	set := o.SecuritySettings(ctx, tenant)
	sc := IncidentsScreen{Incidents: []IncidentView{}, Settings: set, Contacts: o.alertContacts(ctx, tenant), Rules: SecRules(set), Kinds: SecKinds,
		Statuses: IncidentStatuses, ReportTo: ReportTo, ReportHow: ReportHow, Retention: retentionLine(set), Now: o.now().Format(time.RFC3339)}
	for _, x := range xs {
		v := o.incidentView(x)
		if x.Status != IncClosed {
			sc.Open++
		}
		if v.Clock.Overdue {
			sc.Overdue++
		}
		sc.Incidents = append(sc.Incidents, v)
	}
	sort.Slice(sc.Incidents, func(i, j int) bool {
		a, b := sc.Incidents[i], sc.Incidents[j]
		if a.DetectedAt != b.DetectedAt {
			return a.DetectedAt > b.DetectedAt
		}
		return a.Updated > b.Updated || a.Updated == b.Updated && a.ID > b.ID
	})
	return sc, nil
}

func retentionLine(s SecuritySettings) string {
	return fmt.Sprintf("Security events and sign-in records are kept for %d days (CERT-In requires at least %d), on this server; after that the person, address and details are cleared. Audit trail entries are never deleted.", s.RetentionDays, MinRetentionDays)
}

// IncidentDetail is one incident with its events.
type IncidentDetail struct {
	IncidentView
	EventList []SecEvent `json:"event_list"`
}

// IncidentByID returns one incident and its events. Compliance and Admin.
func (o *Ops) IncidentByID(ctx context.Context, tenant, actor, id string) (IncidentDetail, error) {
	if err := o.require(ctx, tenant, actor, "Seeing security events and incidents", RoleCompliance); err != nil {
		return IncidentDetail{}, err
	}
	in, _, err := docs.Get[Incident](ctx, o.Docs, tenant, cIncident, id)
	if err != nil {
		return IncidentDetail{}, err
	}
	d := IncidentDetail{IncidentView: o.incidentView(in), EventList: []SecEvent{}}
	for _, eid := range in.Events {
		if e, _, err := docs.Get[SecEvent](ctx, o.Docs, tenant, cSecEvent, eid); err == nil {
			d.EventList = append(d.EventList, e)
		}
	}
	return d, nil
}

// RecordIncidentInput is an incident the firm records itself.
type RecordIncidentInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
	Kind        string `json:"kind"`       // free text: phishing, lost device, …
	NoticedAt   string `json:"noticed_at"` // when it was noticed (RFC 3339 or YYYY-MM-DDTHH:MM in IST); empty: now
}

// RecordIncident adds an incident the firm found itself to the register.
// Compliance and Admin.
func (o *Ops) RecordIncident(ctx context.Context, tenant, actor string, in RecordIncidentInput) (IncidentView, error) {
	if err := o.require(ctx, tenant, actor, "Recording and reporting security incidents", RoleCompliance); err != nil {
		return IncidentView{}, err
	}
	in.Title, in.Description = strings.TrimSpace(in.Title), strings.TrimSpace(in.Description)
	if len(in.Title) < 5 {
		return IncidentView{}, invalid("say in a few words what happened")
	}
	if !contains(Severities, in.Severity) {
		return IncidentView{}, invalid("choose the severity")
	}
	now := o.now()
	noticed := now
	if in.NoticedAt != "" {
		t, err := parseISTTime(in.NoticedAt)
		if err != nil {
			return IncidentView{}, invalid("give when it was noticed as a date and time")
		}
		if t.After(now.Add(time.Minute)) {
			return IncidentView{}, invalid("the time it was noticed is in the future")
		}
		noticed = t.In(ist)
	}
	n, err := o.next(ctx, tenant, "incident", actor)
	if err != nil {
		return IncidentView{}, err
	}
	ts := now.Format(time.RFC3339)
	x := Incident{ID: fmt.Sprintf("INC-%06d", n), Source: "firm", Kind: firstOf(trim(strings.TrimSpace(in.Kind), 80), "recorded"), Title: trim(in.Title, 200), Description: trim(in.Description, 4000),
		Severity: in.Severity, DetectedAt: noticed.Format(time.RFC3339), FirstSeen: noticed.Format(time.RFC3339), LastSeen: noticed.Format(time.RFC3339), Events: []string{}, Status: IncOpen, Owner: actor,
		Notes: []IncidentNote{{At: ts, By: actor, Text: "Recorded by " + actor, Status: IncOpen}}, Reports: []IncidentReport{}, Updated: ts, UpdatedBy: actor}
	if err := docs.Upsert(ctx, o.Docs, tenant, cIncident, x.ID, x, actor); err != nil {
		return IncidentView{}, err
	}
	o.log(ctx, tenant, actor, "security.incident.recorded", x.ID, map[string]any{"severity": x.Severity, "title": x.Title})
	o.alertIncident(ctx, tenant, &x, x.Description)
	return o.incidentView(x), nil
}

func parseISTTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	for _, f := range []string{"2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(f, s, ist); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad time %q", s)
}

// UpdateIncident moves an incident on (Investigating, Closed …), takes it
// on as its owner or adds a note. Compliance and Admin. Closing needs a
// note saying what was found.
func (o *Ops) UpdateIncident(ctx context.Context, tenant, actor, id, status, note string, own bool) (IncidentView, error) {
	if err := o.require(ctx, tenant, actor, "Recording and reporting security incidents", RoleCompliance); err != nil {
		return IncidentView{}, err
	}
	note = strings.TrimSpace(note)
	if status != "" && !contains(IncidentStatuses, status) {
		return IncidentView{}, invalid("unknown status %q", status)
	}
	if status == IncReported {
		return IncidentView{}, invalid("record the report itself (to whom, when, how and the reference); the status follows")
	}
	ts := o.now().Format(time.RFC3339)
	in, err := upd(ctx, o.Docs, tenant, cIncident, id, actor, nil, func(in *Incident) error {
		if in.ID == "" {
			return ErrNotFound
		}
		if in.Status == IncClosed && status != IncOpen && status != IncInvestigating {
			if note == "" {
				return fmt.Errorf("%w: the incident is closed; reopen it to change it", ErrState)
			}
		}
		if status == IncClosed && len(note) < 10 {
			return invalid("say what was found and done before closing (it stays in the register)")
		}
		if status == "" && note == "" && !own {
			return invalid("nothing to change")
		}
		if own {
			in.Owner = actor
		}
		n := IncidentNote{At: ts, By: actor, Text: trim(note, 4000)}
		if status != "" && status != in.Status {
			n.Status, in.Status = status, status
			if status == IncClosed {
				in.ClosedAt, in.ClosedBy = ts, actor
			} else {
				in.ClosedAt, in.ClosedBy = "", ""
			}
		}
		if own && n.Text == "" {
			n.Text = actor + " took this on"
		}
		if n.Text != "" || n.Status != "" {
			in.Notes = append(in.Notes, n)
		}
		in.Updated, in.UpdatedBy = ts, actor
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return IncidentView{}, ErrNotFound
	}
	if err != nil {
		return IncidentView{}, err
	}
	if in.Status == IncClosed {
		o.incidentDone(ctx, tenant, actor, in)
	} else if in.Rule != "" {
		_ = docs.Upsert(ctx, o.Docs, tenant, cSecOpen, in.Rule+"|"+in.Key, secOpen{Incident: in.ID}, actor) // reopened: joins again
	}
	o.log(ctx, tenant, actor, "security.incident.updated", in.ID, map[string]any{"status": in.Status, "note": trim(note, 200)})
	return o.incidentView(in), nil
}

// incidentDone ends the dedupe link and closes the work items.
func (o *Ops) incidentDone(ctx context.Context, tenant, actor string, in Incident) {
	if in.Rule != "" {
		dk := in.Rule + "|" + in.Key
		if cur, _, err := docs.Get[secOpen](ctx, o.Docs, tenant, cSecOpen, dk); err == nil && cur.Incident == in.ID {
			_ = docs.Upsert(ctx, o.Docs, tenant, cSecOpen, dk, secOpen{}, actor)
		}
	}
	for _, k := range []string{"co", "admin"} {
		o.closeWork(ctx, tenant, actor, "security", in.ID+"|"+k, "incident "+strings.ToLower(in.Status))
	}
}

// ReportIncident records a report made to CERT-In (or SEBI, an exchange,
// a depository): when, how and the reference number. A report to CERT-In
// moves the incident to Reported. Compliance and Admin.
func (o *Ops) ReportIncident(ctx context.Context, tenant, actor, id string, r IncidentReport) (IncidentView, error) {
	if err := o.require(ctx, tenant, actor, "Recording and reporting security incidents", RoleCompliance); err != nil {
		return IncidentView{}, err
	}
	if !contains(ReportTo, r.To) {
		return IncidentView{}, invalid("choose whom it was reported to")
	}
	if !contains(ReportHow, r.How) {
		return IncidentView{}, invalid("choose how it was reported")
	}
	now := o.now()
	at := now
	if r.At != "" {
		t, err := parseISTTime(r.At)
		if err != nil {
			return IncidentView{}, invalid("give when it was reported as a date and time")
		}
		if t.After(now.Add(time.Minute)) {
			return IncidentView{}, invalid("the report time is in the future")
		}
		at = t.In(ist)
	}
	r.Reference, r.Note = trim(strings.TrimSpace(r.Reference), 120), trim(strings.TrimSpace(r.Note), 2000)
	if r.Reference == "" && r.How != "Phone" {
		return IncidentView{}, invalid("give the reference number or acknowledgement (for a phone report, the note says whom you spoke to)")
	}
	if r.How == "Phone" && r.Reference == "" && len(r.Note) < 5 {
		return IncidentView{}, invalid("for a phone report, say whom you spoke to")
	}
	r.At, r.By, r.Recorded = at.Format(time.RFC3339), actor, now.Format(time.RFC3339)
	in, err := upd(ctx, o.Docs, tenant, cIncident, id, actor, nil, func(in *Incident) error {
		if in.ID == "" {
			return ErrNotFound
		}
		if at.Before(mustTime(in.DetectedAt).Add(-time.Minute)) {
			return invalid("the report time is before the incident was detected")
		}
		in.Reports = append(in.Reports, r)
		n := IncidentNote{At: r.Recorded, By: actor, Text: fmt.Sprintf("Reported to %s by %s at %s%s", r.To, strings.ToLower(r.How), at.Format("02 Jan 2006 15:04 IST"), map[bool]string{true: ", reference " + r.Reference}[r.Reference != ""])}
		if r.To == "CERT-In" && in.Status != IncClosed && in.Status != IncReported {
			n.Status, in.Status = IncReported, IncReported
		}
		in.Notes = append(in.Notes, n)
		in.Updated, in.UpdatedBy = r.Recorded, actor
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return IncidentView{}, ErrNotFound
	}
	if err != nil {
		return IncidentView{}, err
	}
	o.log(ctx, tenant, actor, "security.incident.reported", in.ID, map[string]any{"to": r.To, "how": r.How, "at": r.At, "reference": r.Reference})
	return o.incidentView(in), nil
}

func mustTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// ---- platform incidents: Ledgerline's notice to the firms ----

// PlatformIncidentInput is what the operator declares, or updates.
type PlatformIncidentInput struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Severity    string   `json:"severity"`
	Kind        string   `json:"kind"`    // Outage, Breach, Degraded service, Other
	Started     string   `json:"started"` // when it began, if known
	Tenants     []string `json:"tenants"` // the firms affected; empty: every firm on this server
	Status      string   `json:"status"`  // on an update: Open, Investigating, Closed
	Note        string   `json:"note"`    // on an update: what changed
}

// PlatformKinds are the kinds of platform incident.
var PlatformKinds = []string{"Outage", "Breach", "Degraded service", "Other"}

// DeclarePlatformIncident records an incident of the platform in the
// operator's stream and, at once, in the register of every affected firm,
// with an e-mail to each firm's security contacts and work items for its
// compliance officer and Admin: the provider's prompt notice. all is every
// firm on this server.
func (o *Ops) DeclarePlatformIncident(ctx context.Context, all []string, by string, in PlatformIncidentInput) (Incident, error) {
	in.Title, in.Description = strings.TrimSpace(in.Title), strings.TrimSpace(in.Description)
	if len(in.Title) < 5 || len(in.Description) < 10 {
		return Incident{}, invalid("give a title and a description of what happened and what firms should do")
	}
	if !contains(Severities, in.Severity) {
		return Incident{}, invalid("choose the severity")
	}
	if !contains(PlatformKinds, in.Kind) {
		return Incident{}, invalid("choose the kind: %s", strings.Join(PlatformKinds, ", "))
	}
	firms := in.Tenants
	if len(firms) == 0 {
		firms = all
	}
	var aff []string
	for _, t := range firms {
		if t = strings.TrimSpace(t); t == "" || t == PlatformTenant {
			continue
		}
		if !contains(all, t) {
			return Incident{}, invalid("%q is not a firm on this server", t)
		}
		if !contains(aff, t) {
			aff = append(aff, t)
		}
	}
	if len(aff) == 0 {
		return Incident{}, invalid("no firm to notify")
	}
	now := o.now()
	ts := now.Format(time.RFC3339)
	started := ts
	if in.Started != "" {
		t, err := parseISTTime(in.Started)
		if err != nil {
			return Incident{}, invalid("give when it started as a date and time")
		}
		started = t.In(ist).Format(time.RFC3339)
	}
	n, err := o.next(ctx, PlatformTenant, "incident", by)
	if err != nil {
		return Incident{}, err
	}
	p := Incident{ID: fmt.Sprintf("PINC-%06d", n), Source: "provider", Kind: "platform." + strings.ToLower(strings.ReplaceAll(in.Kind, " ", "_")), Title: trim(in.Title, 200), Description: trim(in.Description, 4000),
		Severity: in.Severity, DetectedAt: ts, FirstSeen: started, LastSeen: ts, Events: []string{}, Status: IncOpen, Affected: aff,
		Notes: []IncidentNote{{At: ts, By: by, Text: "Declared and sent to " + fmt.Sprint(len(aff)) + " firm(s)", Status: IncOpen}}, Reports: []IncidentReport{}, Updated: ts, UpdatedBy: by}
	p.Platform, p.ProviderStatus = p.ID, IncOpen
	ev := o.PlatformEvent(ctx, SecEvent{Kind: "platform.incident", Severity: in.Severity, Actor: by, Subject: p.ID, Message: p.Title, Detail: map[string]any{"kind": in.Kind, "firms": len(aff)}})
	p.Events = append(p.Events, ev.ID)
	if err := docs.Upsert(ctx, o.Docs, PlatformTenant, cIncident, p.ID, p, by); err != nil {
		return Incident{}, err
	}
	for _, t := range aff {
		e := o.SecurityEvent(ctx, t, SecEvent{Kind: "incident.provider_notice", Severity: in.Severity, Actor: "ledgerline:" + by, Subject: p.ID, Message: "Ledgerline incident notice: " + p.Title, Detail: map[string]any{"kind": in.Kind, "started": started}})
		x := p
		x.Affected, x.Events, x.EventCount = nil, []string{e.ID}, 1
		x.Notes = []IncidentNote{{At: ts, By: "Ledgerline", Text: "Notice from Ledgerline (the software provider). " + x.Description, Status: IncOpen}}
		x.Description = trim(in.Description, 4000) + "\n\nStarted: " + mustTime(started).In(ist).Format("02 Jan 2006 15:04 IST") + "."
		x.UpdatedBy = "Ledgerline"
		if err := docs.Upsert(ctx, o.Docs, t, cIncident, x.ID, x, SystemActor); err != nil {
			continue
		}
		o.log(ctx, t, SystemActor, "security.incident.provider_notice", x.ID, map[string]any{"severity": x.Severity, "title": x.Title, "kind": in.Kind})
		o.alertIncident(ctx, t, &x, x.Description)
	}
	return p, nil
}

// UpdatePlatformIncident adds an update to a platform incident and sends
// it to every firm it went to; the firm's own copy keeps the firm's own
// status (Ledgerline's status is shown beside it).
func (o *Ops) UpdatePlatformIncident(ctx context.Context, by, id string, in PlatformIncidentInput) (Incident, error) {
	note := strings.TrimSpace(in.Note)
	if len(note) < 5 {
		return Incident{}, invalid("say what changed")
	}
	if in.Status != "" && in.Status != IncOpen && in.Status != IncInvestigating && in.Status != IncClosed {
		return Incident{}, invalid("the status is Open, Investigating or Closed")
	}
	ts := o.now().Format(time.RFC3339)
	p, err := upd(ctx, o.Docs, PlatformTenant, cIncident, id, by, nil, func(p *Incident) error {
		if p.ID == "" {
			return ErrNotFound
		}
		n := IncidentNote{At: ts, By: by, Text: trim(note, 4000)}
		if in.Status != "" && in.Status != p.Status {
			p.Status, p.ProviderStatus, n.Status = in.Status, in.Status, in.Status
			if in.Status == IncClosed {
				p.ClosedAt, p.ClosedBy = ts, by
			}
		}
		p.Notes = append(p.Notes, n)
		p.Updated, p.UpdatedBy, p.LastSeen = ts, by, ts
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return Incident{}, ErrNotFound
	}
	if err != nil {
		return Incident{}, err
	}
	o.PlatformEvent(ctx, SecEvent{Kind: "platform.incident_update", Actor: by, Subject: p.ID, Message: p.Title + ": " + trim(note, 200)})
	for _, t := range p.Affected {
		x, err := upd(ctx, o.Docs, t, cIncident, p.ID, SystemActor, nil, func(x *Incident) error {
			if x.ID == "" {
				return ErrNotFound
			}
			x.ProviderStatus, x.LastSeen = p.ProviderStatus, ts
			x.Notes = append(x.Notes, IncidentNote{At: ts, By: "Ledgerline", Text: "Update from Ledgerline (status " + p.ProviderStatus + "): " + trim(note, 4000)})
			x.Updated, x.UpdatedBy = ts, "Ledgerline"
			return nil
		})
		if err != nil {
			continue
		}
		e := o.SecurityEvent(ctx, t, SecEvent{Kind: "platform.incident_update", Actor: "ledgerline:" + by, Subject: p.ID, Message: "Ledgerline update on " + p.ID + ": " + trim(note, 200)})
		_, _ = upd(ctx, o.Docs, t, cIncident, p.ID, SystemActor, nil, func(x *Incident) error {
			if len(x.Events) < 200 {
				x.Events = append(x.Events, e.ID)
			}
			x.EventCount++
			return nil
		})
		set, _ := o.Settings(ctx, t)
		for _, to := range o.alertContacts(ctx, t) {
			o.mailStaff(ctx, t, to, fmt.Sprintf("[Ledgerline incident update] %s: %s (%s)", p.ID, p.Title, p.ProviderStatus),
				fmt.Sprintf("Update on %s, %s:\n\n%s\n\nLedgerline's status: %s. Your own record is in Ledgerline → Security → Incidents (your status there: %s).\n\n%s", p.ID, p.Title, note, p.ProviderStatus, x.Status, firstOf(set.Firm, "Ledgerline")), p.ID, nil)
		}
	}
	return p, nil
}

// PlatformIncidents lists the operator's incidents, newest first.
func (o *Ops) PlatformIncidents(ctx context.Context) ([]IncidentView, error) {
	xs, err := docs.List[Incident](ctx, o.Docs, PlatformTenant, cIncident, "")
	out := []IncidentView{}
	for i := len(xs) - 1; i >= 0; i-- {
		out = append(out, o.incidentView(xs[i]))
	}
	return out, err
}
