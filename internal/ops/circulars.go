package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
)

// ---- exchange circulars and the compliance calendar ----
//
// Circulars from SEBI, the exchanges, clearing corporations and depositories
// arrive every day. Each one loaded (from the CIRCULARS file, or the feed
// once a connection is set up) is triaged by rules on its subject and text:
// the area it touches, a priority, a short summary and the actions it
// usually needs, each with an owner role and a due date. The compliance
// officer reviews it, adds or removes actions, and closes it when every
// action is done with evidence.
//
// The calendar holds the firm's recurring obligations (disclosures,
// settlements, audits, certificates), works out when each is next due,
// queues it for its owner a week before, and keeps the evidence of each
// time it was done. The seeded list is a starting point: the compliance
// officer should check every due date against the current circulars.

const (
	cCircular   = "circular"
	cObligation = "obligation"
)

// CircularTask is one action a circular needs.
type CircularTask struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Role     string `json:"role"`
	Due      string `json:"due,omitempty"`
	Status   string `json:"status"` // Open, Done, Dropped
	Evidence string `json:"evidence,omitempty"`
	By       string `json:"by,omitempty"`
	At       string `json:"at,omitempty"`
}

// Circular is one regulatory circular.
type Circular struct {
	ID        string         `json:"id"`
	Source    string         `json:"source"`
	Ref       string         `json:"ref"`
	Date      string         `json:"date"`
	Subject   string         `json:"subject"`
	Dept      string         `json:"department,omitempty"`
	URL       string         `json:"url,omitempty"`
	Text      string         `json:"text,omitempty"`
	Effective string         `json:"effective,omitempty"`
	Summary   string         `json:"summary"`
	Areas     []string       `json:"areas"`
	Priority  string         `json:"priority"` // High, Medium, Low, Information
	Status    string         `json:"status"`   // New, Action needed, Done, Not applicable
	Tasks     []CircularTask `json:"tasks"`
	Note      string         `json:"note,omitempty"`
	By        string         `json:"by,omitempty"`
	Loaded    string         `json:"loaded"`
}

// triageRule maps words in a circular to an area, a priority and actions.
type triageRule struct {
	re       *regexp.Regexp
	area     string
	priority string
	actions  []ruleAction
}

type ruleAction struct {
	title string
	role  string
}

var triageRules = []triageRule{
	{regexp.MustCompile(`(?i)cyber|CSCRF|VAPT|system audit|information security|business continuity|disaster recovery`), "IT and cyber security", "High",
		[]ruleAction{{"Check the requirement against the firm's cyber security policy and the system audit plan", RoleCompliance}, {"Tell the technology vendor and the IT committee", RoleAdmin}}},
	{regexp.MustCompile(`(?i)margin|peak|SPAN|exposure|collateral|haircut|pledge|square.?off`), "Risk and margins", "High",
		[]ruleAction{{"Check limits, margin rules and the risk policy", RoleRisk}}},
	{regexp.MustCompile(`(?i)KYC|PMLA|anti.?money|AML|beneficial owner|PEP|CKYC|KRA|FATCA`), "KYC and PMLA", "High",
		[]ruleAction{{"Update the KYC / PMLA policy and onboarding checks", RoleCompliance}}},
	{regexp.MustCompile(`(?i)client funds|running account|settlement of (client )?(funds|account)|pay.?out|upstream|bank guarantee|clearing corporation`), "Client funds and settlement", "High",
		[]ruleAction{{"Check funds, settlement and running-account procedures", RoleChecker}}},
	{regexp.MustCompile(`(?i)UCC|unique client code|client code|PAN.{0,20}seed|nomination|inactive`), "Client records (UCC)", "Medium",
		[]ruleAction{{"Check the client records and the UCC upload", RoleChecker}}},
	{regexp.MustCompile(`(?i)contract note|STT|stamp duty|transaction charge|turnover fee|GST`), "Charges and contract notes", "Medium",
		[]ruleAction{{"Update the charge schedule or the contract note format", RoleChecker}}},
	{regexp.MustCompile(`(?i)report(ing)?|submission|disclos|return|certificate|net ?worth`), "Reporting", "Medium",
		[]ruleAction{{"Add or change the report and its due date in the compliance calendar", RoleCompliance}}},
	{regexp.MustCompile(`(?i)authori[sz]ed person|\bAP\b|branch|sub.?broker|franchise`), "Authorised persons and branches", "Medium",
		[]ruleAction{{"Tell authorised persons and branches; check AP agreements", RoleCompliance}}},
	{regexp.MustCompile(`(?i)algo|API|order (placement|type)|trading terminal|CTCL|kill switch`), "Trading systems", "Medium",
		[]ruleAction{{"Check with the trading platform vendor", RoleAdmin}}},
	{regexp.MustCompile(`(?i)investor (grievance|complaint)|SCORES|ODR|online dispute|investor charter`), "Investor services", "Medium",
		[]ruleAction{{"Update the grievance process and investor-facing pages", RoleCompliance}}},
	{regexp.MustCompile(`(?i)penalt|non.?compliance|disciplinary|show cause|inspection`), "Enforcement", "High",
		[]ruleAction{{"Check whether the firm is affected and record it", RoleCompliance}}},
	{regexp.MustCompile(`(?i)holiday|trading hours|market timing|mock trading|live trading session|contract (launch|introduction)|new contracts|expiry day`), "Market operations", "Information", nil},
}

var reDateish = regexp.MustCompile(`(?i)(?:with effect from|w\.e\.f\.?|effective (?:from|date)?|from)\s+(\d{1,2})(?:st|nd|rd|th)?[ -](jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*[ ,-]+(\d{4})`)

var monIdx = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

// triage fills a circular's areas, priority, summary, effective date and
// suggested actions.
func triage(c *Circular) {
	text := c.Subject + ". " + c.Text
	rank := map[string]int{"Information": 0, "Low": 1, "Medium": 2, "High": 3}
	c.Priority, c.Areas = "Low", []string{}
	var acts []ruleAction
	for _, r := range triageRules {
		if !r.re.MatchString(text) {
			continue
		}
		c.Areas = append(c.Areas, r.area)
		if rank[r.priority] > rank[c.Priority] || (len(c.Areas) == 1 && r.priority == "Information") {
			c.Priority = r.priority
		}
		acts = append(acts, r.actions...)
	}
	if len(c.Areas) == 0 {
		c.Areas = []string{"General"}
	}
	if m := reDateish.FindStringSubmatch(text); m != nil {
		d, _ := strconv.Atoi(m[1])
		y, _ := strconv.Atoi(m[3])
		c.Effective = fmt.Sprintf("%04d-%02d-%02d", y, monIdx[strings.ToLower(m[2])], d)
		if !ledger.ValidDate(c.Effective) {
			c.Effective = ""
		}
	}
	c.Summary = summarise(c.Subject, c.Text)
	due := c.Effective
	if due == "" || due < c.Date {
		due = addDays(c.Date, 14)
	}
	seen := map[string]bool{}
	c.Tasks = []CircularTask{}
	for _, a := range acts {
		if seen[a.title] {
			continue
		}
		seen[a.title] = true
		c.Tasks = append(c.Tasks, CircularTask{ID: fmt.Sprintf("T%d", len(c.Tasks)+1), Title: a.title, Role: a.role, Due: due, Status: "Open"})
	}
	c.Status = "New"
	if len(c.Tasks) == 0 {
		c.Status = "Not applicable"
		if c.Priority == "Information" {
			c.Status = "Done"
		}
	}
}

var reSentence = regexp.MustCompile(`[^.!?]+[.!?]`)

// summarise is the subject plus the first two sentences of the text.
func summarise(subject, text string) string {
	text = strings.Join(strings.Fields(text), " ")
	ss := reSentence.FindAllString(text, 3)
	out := strings.TrimSpace(subject)
	n := 0
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if len(s) < 25 || strings.EqualFold(s, subject) {
			continue
		}
		out += " — " + s
		n++
		if n == 2 {
			break
		}
	}
	if len(out) > 600 {
		out = out[:597] + "…"
	}
	return out
}

func circularID(source, ref string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "/", "-", "|", "-").Replace(source + "-" + ref))
}

// prepareCirculars loads a CIRCULARS file.
func (o *Ops) prepareCirculars(ctx context.Context, tenant, actor string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var cs []Circular
	var errs []ingest.ParseError
	for _, r := range rows {
		c := Circular{Source: strings.ToUpper(r.get("Source")), Ref: r.get("Ref"), Date: r.get("Date"), Subject: r.get("Subject"), Dept: r.get("Department"), URL: r.get("URL"), Text: r.get("Text")}
		if t, err := time.Parse("02-Jan-2006", c.Date); err == nil {
			c.Date = t.Format("2006-01-02")
		}
		switch {
		case c.Source == "" || c.Ref == "":
			errs = append(errs, ingest.ParseError{Line: r.line, Field: "Ref", Msg: "source and circular number are required"})
		case !ledger.ValidDate(c.Date):
			errs = append(errs, ingest.ParseError{Line: r.line, Field: "Date", Msg: "date must be YYYY-MM-DD or DD-Mon-YYYY"})
		case strings.TrimSpace(c.Subject) == "":
			errs = append(errs, ingest.ParseError{Line: r.line, Field: "Subject", Msg: "subject is required"})
		case c.URL != "" && !strings.HasPrefix(c.URL, "https://") && !strings.HasPrefix(c.URL, "http://"):
			errs = append(errs, ingest.ParseError{Line: r.line, Field: "URL", Msg: "link must start with https://"})
		default:
			cs = append(cs, c)
		}
	}
	return func() (int, int, error) {
		added, dup := 0, 0
		for _, c := range cs {
			c.ID = circularID(c.Source, c.Ref)
			if _, _, err := docs.Get[Circular](ctx, o.Docs, tenant, cCircular, c.ID); err == nil {
				dup++
				continue
			}
			triage(&c)
			c.Loaded = o.now().Format(time.RFC3339)
			if err := docs.Upsert(ctx, o.Docs, tenant, cCircular, c.ID, c, actor); err != nil {
				return added, dup, err
			}
			added++
		}
		return added, dup, nil
	}, errs
}

// Circulars lists circulars, newest first.
func (o *Ops) Circulars(ctx context.Context, tenant string) ([]Circular, error) {
	cs, err := docs.List[Circular](ctx, o.Docs, tenant, cCircular, "")
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Date != cs[j].Date {
			return cs[i].Date > cs[j].Date
		}
		return cs[i].ID > cs[j].ID
	})
	return cs, err
}

// CircularAction changes a circular: review, add or finish an action,
// drop an action, mark not applicable or done.
type CircularAction struct {
	Kind     string `json:"kind"` // review, add_task, done_task, drop_task, not_applicable, reopen
	Task     string `json:"task,omitempty"`
	Title    string `json:"title,omitempty"`
	Role     string `json:"role,omitempty"`
	Due      string `json:"due,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Note     string `json:"note,omitempty"`
}

// ActOnCircular applies one change. Reviews and closing are for the
// compliance officer; a task's owner role may finish it with evidence.
func (o *Ops) ActOnCircular(ctx context.Context, tenant, actor, id string, a CircularAction) (Circular, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return Circular{}, err
	}
	staff := role == RoleCompliance || role == RoleAdmin
	if !staff && a.Kind != "done_task" {
		return Circular{}, fmt.Errorf("%w: only the compliance officer reviews circulars", ErrForbidden)
	}
	now := o.now().Format(time.RFC3339)
	c, err := docs.Update(ctx, o.Docs, tenant, cCircular, id, actor, func(c *Circular) error {
		find := func() (*CircularTask, error) {
			for i := range c.Tasks {
				if c.Tasks[i].ID == a.Task {
					return &c.Tasks[i], nil
				}
			}
			return nil, invalid("no action %s on this circular", a.Task)
		}
		switch a.Kind {
		case "review":
			if c.Status == "New" {
				c.Status = "Action needed"
			}
			c.Note, c.By = strings.TrimSpace(a.Note), actor
		case "add_task":
			if strings.TrimSpace(a.Title) == "" || !contains([]string{RoleMaker, RoleChecker, RoleRisk, RoleCompliance, RoleAdmin}, a.Role) || !ledger.ValidDate(a.Due) {
				return invalid("an action needs a description, an owner role and a due date")
			}
			c.Tasks = append(c.Tasks, CircularTask{ID: fmt.Sprintf("T%d", len(c.Tasks)+1), Title: strings.TrimSpace(a.Title), Role: a.Role, Due: a.Due, Status: "Open"})
			c.Status = "Action needed"
		case "done_task":
			t, err := find()
			if err != nil {
				return err
			}
			if !staff && role != t.Role {
				return fmt.Errorf("%w: this action is for the %s", ErrForbidden, t.Role)
			}
			if strings.TrimSpace(a.Evidence) == "" {
				return invalid("say what was done, with a reference to the evidence")
			}
			t.Status, t.Evidence, t.By, t.At = "Done", strings.TrimSpace(a.Evidence), actor, now
		case "drop_task":
			t, err := find()
			if err != nil {
				return err
			}
			if strings.TrimSpace(a.Note) == "" {
				return invalid("say why the action is not needed")
			}
			t.Status, t.Evidence, t.By, t.At = "Dropped", strings.TrimSpace(a.Note), actor, now
		case "not_applicable":
			if strings.TrimSpace(a.Note) == "" {
				return invalid("say why the circular does not apply to the firm")
			}
			c.Status, c.Note, c.By = "Not applicable", strings.TrimSpace(a.Note), actor
			return nil
		case "reopen":
			c.Status = "Action needed"
			return nil
		default:
			return invalid("unknown action %q", a.Kind)
		}
		open := 0
		for _, t := range c.Tasks {
			if t.Status == "Open" {
				open++
			}
		}
		if open == 0 && len(c.Tasks) > 0 && c.Status != "New" {
			c.Status = "Done"
		} else if open > 0 && c.Status == "Done" {
			c.Status = "Action needed"
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "circular."+a.Kind, id, map[string]any{"task": a.Task})
	}
	return c, err
}

// ---- compliance calendar ----

// Duty is a recurring duty with its due-date rule.
type Duty struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Authority string    `json:"authority"`
	Frequency string    `json:"frequency"` // Monthly, Quarterly, Half-yearly, Yearly
	Rule      string    `json:"rule"`      // "D07": day 7 of the next month; "MM-DD,MM-DD": dates in the year; "FRI1Q": first Friday of each quarter
	Role      string    `json:"role"`
	Evidence  string    `json:"evidence"` // what to keep
	Active    bool      `json:"active"`
	Since     string    `json:"since,omitempty"` // first date it applies from
	Done      []DutyRun `json:"done"`
	NextDue   string    `json:"next_due,omitempty"`
	Period    string    `json:"period,omitempty"`
	Overdue   bool      `json:"overdue,omitempty"`
}

// DutyRun is one time it was done.
type DutyRun struct {
	Period   string `json:"period"`
	Due      string `json:"due"`
	At       string `json:"at"`
	By       string `json:"by"`
	Evidence string `json:"evidence"`
	Late     bool   `json:"late,omitempty"`
}

// DefaultDuties is the starting calendar for a stockbroker. Each date
// should be checked against the current circulars before relying on it.
func DefaultDuties() []Duty {
	ob := func(id, title, auth, freq, rule, role, ev string) Duty {
		return Duty{ID: id, Title: title, Authority: auth, Frequency: freq, Rule: rule, Role: role, Evidence: ev, Active: true, Done: []DutyRun{}}
	}
	return []Duty{
		ob("OB-COMPLAINTS", "Publish the monthly investor complaints disclosure on the website", "SEBI", "Monthly", "D07", RoleCompliance, "Screenshot or link of the published table"),
		ob("OB-CTR", "Cash transaction report to FIU-IND (or nil confirmation per firm policy)", "PMLA rules", "Monthly", "D15", RoleCompliance, "FINnet acknowledgement"),
		ob("OB-RAS", "Quarterly running account settlement of client funds and securities", "SEBI / exchanges", "Quarterly", "FRI1Q", RoleChecker, "Settlement run and statements sent"),
		ob("OB-INTAUDIT", "Half-yearly internal audit report submitted to the exchanges", "Exchanges", "Half-yearly", "05-15,11-15", RoleCompliance, "Audit report and exchange acknowledgement"),
		ob("OB-NETWORTH", "Half-yearly net worth certificate to the exchanges", "Exchanges", "Half-yearly", "04-30,10-31", RoleCompliance, "CA certificate and upload acknowledgement"),
		ob("OB-SYSAUDIT", "Annual system audit by an empanelled auditor", "Exchanges", "Yearly", "06-30", RoleAdmin, "System audit report and submission"),
		ob("OB-CYBER", "Annual cyber security audit and VAPT under CSCRF", "SEBI", "Yearly", "06-30", RoleAdmin, "Audit and VAPT reports, closure of findings"),
		ob("OB-APINSP", "Annual inspection of authorised persons", "Exchanges", "Yearly", "03-31", RoleCompliance, "Inspection reports"),
		ob("OB-POLICIES", "Board review of risk, PMLA, surveillance and outsourcing policies", "SEBI / exchanges", "Yearly", "03-31", RoleCompliance, "Board minutes"),
		ob("OB-CHARTER", "Review the investor charter and the website disclosures", "SEBI", "Yearly", "04-30", RoleCompliance, "Updated pages"),
	}
}

// nextDue is the first due date on or after `from` for a rule, and the
// period it belongs to.
func nextDue(rule, from string) (string, string) {
	t, err := time.Parse("2006-01-02", from)
	if err != nil {
		return "", ""
	}
	switch {
	case strings.HasPrefix(rule, "D"):
		day, _ := strconv.Atoi(rule[1:])
		for i := 0; i < 3; i++ {
			m := time.Date(t.Year(), t.Month()+time.Month(i), day, 0, 0, 0, 0, time.UTC)
			if d := m.Format("2006-01-02"); d >= from {
				return d, m.AddDate(0, -1, 0).Format("2006-01")
			}
		}
	case rule == "FRI1Q":
		for i := 0; i < 15; i++ {
			m := time.Date(t.Year(), t.Month()+time.Month(i), 1, 0, 0, 0, 0, time.UTC)
			if (int(m.Month())-1)%3 != 0 {
				continue
			}
			for m.Weekday() != time.Friday {
				m = m.AddDate(0, 0, 1)
			}
			if d := m.Format("2006-01-02"); d >= from {
				return d, fmt.Sprintf("%d-Q%d", m.Year(), (int(m.Month())-1)/3+1)
			}
		}
	default:
		var best string
		for _, md := range strings.Split(rule, ",") {
			md = strings.TrimSpace(md)
			for _, y := range []int{t.Year(), t.Year() + 1} {
				d := fmt.Sprintf("%d-%s", y, md)
				if ledger.ValidDate(d) && d >= from && (best == "" || d < best) {
					best = d
				}
			}
		}
		return best, best
	}
	return "", ""
}

// Obligations lists the calendar with each next due date, seeding the
// default list for a new firm.
func (o *Ops) Duties(ctx context.Context, tenant, date string) ([]Duty, error) {
	obs, err := docs.List[Duty](ctx, o.Docs, tenant, cObligation, "")
	if err != nil {
		return nil, err
	}
	if len(obs) == 0 {
		for _, x := range DefaultDuties() {
			x.Since = date
			if err := docs.Upsert(ctx, o.Docs, tenant, cObligation, x.ID, x, "system"); err != nil {
				return nil, err
			}
			obs = append(obs, x)
		}
	}
	for i := range obs {
		x := &obs[i]
		if x.Done == nil {
			x.Done = []DutyRun{}
		}
		// The next due is the earliest one not yet done, which may be past.
		from := addDays(date, -120)
		if x.Since > from {
			from = x.Since
		}
		for k := 0; k < 30; k++ {
			d, p := nextDue(x.Rule, from)
			if d == "" {
				break
			}
			done := false
			for _, r := range x.Done {
				done = done || r.Period == p
			}
			if !done {
				x.NextDue, x.Period = d, p
				break
			}
			from = addDays(d, 1)
		}
		x.Overdue = x.Active && x.NextDue != "" && x.NextDue < date
	}
	sort.SliceStable(obs, func(i, j int) bool { return obs[i].NextDue < obs[j].NextDue })
	return obs, nil
}

// DoneDuty records an obligation met for its current period.
func (o *Ops) DoneDuty(ctx context.Context, tenant, actor, date, id, evidence string) (Duty, error) {
	obs, err := o.Duties(ctx, tenant, date)
	if err != nil {
		return Duty{}, err
	}
	var x *Duty
	for i := range obs {
		if obs[i].ID == id {
			x = &obs[i]
		}
	}
	if x == nil {
		return Duty{}, ErrNotFound
	}
	if err := o.require(ctx, tenant, actor, x.Title, x.Role, RoleCompliance); err != nil {
		return Duty{}, err
	}
	if strings.TrimSpace(evidence) == "" {
		return Duty{}, invalid("say what was done and where the evidence is: %s", x.Evidence)
	}
	run := DutyRun{Period: x.Period, Due: x.NextDue, At: o.now().Format(time.RFC3339), By: actor, Evidence: strings.TrimSpace(evidence), Late: date > x.NextDue}
	saved, err := docs.Update(ctx, o.Docs, tenant, cObligation, id, actor, func(y *Duty) error {
		y.Done = append(y.Done, run)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "obligation.done", id, map[string]any{"period": run.Period, "late": run.Late})
		o.closeWork(ctx, tenant, actor, "compliance", id+"|"+run.Period, "done")
	}
	return saved, err
}

// SaveDuty adds or changes an obligation.
func (o *Ops) SaveDuty(ctx context.Context, tenant, actor string, x Duty) (Duty, error) {
	if err := o.require(ctx, tenant, actor, "Changing the compliance calendar", RoleCompliance); err != nil {
		return x, err
	}
	if strings.TrimSpace(x.Title) == "" || !contains([]string{RoleMaker, RoleChecker, RoleRisk, RoleCompliance, RoleAdmin}, x.Role) {
		return x, invalid("an obligation needs a title and an owner role")
	}
	if d, _ := nextDue(x.Rule, "2026-01-01"); d == "" {
		return x, invalid("due rule must be D07 (day of next month), FRI1Q (first Friday of each quarter) or MM-DD dates such as 05-15,11-15")
	}
	if x.ID == "" {
		n, err := o.next(ctx, tenant, "obligation", actor)
		if err != nil {
			return x, err
		}
		x.ID = fmt.Sprintf("OB-%04d", n)
	}
	old, _, err := docs.Get[Duty](ctx, o.Docs, tenant, cObligation, x.ID)
	if err == nil {
		x.Done = old.Done
	}
	if x.Done == nil {
		x.Done = []DutyRun{}
	}
	if x.Since == "" {
		x.Since = firstOf(old.Since, o.businessDate(ctx, tenant))
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cObligation, x.ID, x, actor); err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "obligation.saved", x.ID, map[string]any{"title": x.Title, "rule": x.Rule, "active": x.Active})
	return x, nil
}

// autoCompliance queues circular actions and obligations falling due in
// the next seven days (and overdue ones) for their owners.
func autoCompliance(r *runCtx) error {
	obs, err := r.o.Duties(r.ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	for _, x := range obs {
		if !x.Active || x.NextDue == "" || daysBetween(r.date, x.NextDue) > 7 {
			continue
		}
		r.queue(x.ID+"|"+x.Period, x.Title+" · due "+fmtDay(x.NextDue), "Keep: "+x.Evidence+". Mark it done on Circulars & calendar.", "", x.Role, "circulars", x.NextDue+" 18:00", nil)
	}
	cs, err := r.o.Circulars(r.ctx, r.tenant)
	if err != nil {
		return err
	}
	news := 0
	for _, c := range cs {
		if c.Status == "New" && (c.Priority == "High" || c.Priority == "Medium") {
			news++
		}
		if c.Status != "Action needed" && c.Status != "New" {
			continue
		}
		for _, t := range c.Tasks {
			if t.Status == "Open" && t.Due != "" && daysBetween(r.date, t.Due) <= 7 {
				r.queue(c.ID+"|"+t.ID, fmt.Sprintf("%s %s: %s", c.Source, c.Ref, t.Title), c.Subject, "", t.Role, "circulars", t.Due+" 18:00", nil)
			}
		}
	}
	if news > 0 {
		r.queue("review", fmt.Sprintf("%d new circular(s) to review", news), "High and medium priority circulars loaded and triaged; review the suggested actions.", "", RoleCompliance, "circulars", r.date+" 18:00", nil)
	}
	r.note("%d obligations, %d circulars", len(obs), len(cs))
	return nil
}
