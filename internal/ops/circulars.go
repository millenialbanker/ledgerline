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
	if cs == nil {
		cs = []Circular{}
	}
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
	ID        string `json:"id"`
	Title     string `json:"title"`
	Authority string `json:"authority"`
	Frequency string `json:"frequency"` // Daily, Monthly, Quarterly, Half-yearly, Yearly
	// Rule: "D07" day 7 of the next month; "MM-DD,MM-DD" dates in the year;
	// "RASM" / "RASQ" the monthly / quarterly dates of the exchanges' RAS
	// calendar (compcal.go); "NETWORTH" the net worth certificate dates for
	// the firm (MTF or not); "SYSPLAN", "SYSPRELIM", "SYSATR" the system
	// audit milestones for the firm's cycle; "DAILY" each day but Sunday,
	// due the next day.
	Rule     string    `json:"rule"`
	Role     string    `json:"role"`
	Evidence string    `json:"evidence"` // what to keep
	Active   bool      `json:"active"`
	Since    string    `json:"since,omitempty"` // first date it applies from
	Done     []DutyRun `json:"done"`
	Task     string    `json:"task,omitempty"` // a Periodic work task that does it: its due date and records count
	// Shared: a common submission that a member of NSE and other exchanges
	// files with NSE only (NSE/INSP/69184, NSE/INSP/70505).
	Shared bool `json:"shared,omitempty"`
	// Rev is the version of the default duty it was last brought up to.
	Rev     int    `json:"rev,omitempty"`
	Filing  string `json:"filing,omitempty"`  // where it is filed, for this firm
	Warning string `json:"warning,omitempty"` // something to fix before it can be done
	NextDue string `json:"next_due,omitempty"`
	Period  string `json:"period,omitempty"`
	Overdue bool   `json:"overdue,omitempty"`
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

// dutyRev is the version of DefaultDuties. A saved default duty with an
// older Rev takes the new title, authority, frequency, rule and evidence
// once (keeping whether it is active and its history).
const dutyRev = 2

// DefaultDuties is the starting calendar for a stockbroker. Each date
// should be checked against the current circulars before relying on it.
func DefaultDuties() []Duty {
	ob := func(id, title, auth, freq, rule, role, ev string) Duty {
		return Duty{ID: id, Title: title, Authority: auth, Frequency: freq, Rule: rule, Role: role, Evidence: ev, Active: true, Done: []DutyRun{}, Rev: dutyRev}
	}
	shared := func(d Duty) Duty { d.Shared = true; return d }
	return []Duty{
		ob("OB-COMPLAINTS", "Publish the monthly investor complaints disclosure on the website", "SEBI (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/22)", "Monthly", "D07", RoleCompliance, "Screenshot or link of the published table (Periodic work builds the table)"),
		ob("OB-CTR", "Cash transaction report to FIU-IND (or nil confirmation per firm policy)", "PMLA rules", "Monthly", "D15", RoleCompliance, "FINnet acknowledgement"),
		withTask(ob("OB-RAS", "Running account settlement: clients who chose quarterly settlement", "SEBI / exchanges' joint RAS calendar (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197)", "Quarterly", "RASQ", RoleChecker, "Settlement run and statements sent"), "ras"),
		withTask(ob("OB-RAS-M", "Running account settlement: clients who chose monthly settlement", "SEBI / exchanges' joint RAS calendar (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197)", "Monthly", "RASM", RoleChecker, "Settlement run and statements sent"), "ras"),
		ob("OB-INTAUDIT", "Half-yearly internal audit report", "Exchanges", "Half-yearly", "05-15,11-15", RoleCompliance, "Audit report and exchange acknowledgement"),
		shared(ob("OB-NETWORTH", "Half-yearly net worth certificate", "Exchanges (NSE/INSP/70505)", "Half-yearly", "NETWORTH", RoleCompliance, "CA certificate and upload acknowledgement")),
		shared(ob("OB-ANNUALRET", "Annual returns", "Exchanges (NSE/INSP/70505)", "Yearly", "10-31", RoleCompliance, "Audited accounts, annual return and upload acknowledgement")),
		ob("OB-SYSAUDIT-PLAN", "System audit: audit plan on NSE ENIT (auditor, systems, office locations to visit)", "NSE (NSE/INSP/73850)", "Yearly", "SYSPLAN", RoleAdmin, "ENIT submission of the audit plan, agreed with the NSE-empanelled auditor"),
		ob("OB-SYSAUDIT", "System audit: preliminary audit report by an NSE-empanelled auditor", "NSE (NSE/INSP/73850)", "Yearly", "SYSPRELIM", RoleAdmin, "Preliminary report with management comments, after the algo MIS, version confirmation, assessment and visit details are in"),
		ob("OB-SYSAUDIT-ATR", "System audit: action taken report validated by the same auditor (if there were non-compliances)", "NSE (NSE/INSP/73850)", "Yearly", "SYSATR", RoleAdmin, "ATR marked Compliant / Non-Compliant by the auditor on the portal, or a note that there was nothing to act on"),
		shared(ob("OB-CYBER", "Cyber security audit and VAPT under CSCRF", "SEBI / exchanges (NSE/INSP/70505)", "Yearly", "06-30", RoleAdmin, "Audit and VAPT reports, closure of findings")),
		ob("OB-APINSP", "Annual inspection of authorised persons", "Exchanges", "Yearly", "03-31", RoleCompliance, "Inspection reports"),
		ob("OB-POLICIES", "Board review of risk, PMLA, surveillance and outsourcing policies", "SEBI / exchanges", "Yearly", "03-31", RoleCompliance, "Board minutes"),
		ob("OB-CHARTER", "Review the investor charter for stock brokers (2025 charter: SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/22 of 21 February 2025) on the website, in the account opening kit and at the offices, and the website disclosures", "SEBI", "Yearly", "04-30", RoleCompliance, "Updated pages and kit"),
		shared(ob("OB-BANKBAL", "Confirm your banks submit daily client bank balances to the exchange", "NSE (NSE/INSP/59044)", "Yearly", "04-30", RoleCompliance, "For every reported bank account: the bank's confirmation (NSE/INSP/70505 Annexure D) and the exchange's view that the bank's T+1 daily balances are coming in")),
		ob("OB-SURV", "Surveillance obligation quarterly report in Formats I to III (nil report when there is nothing to report)", "Exchanges (NSE/SURV/48818, NSE/SURV/49765, NSE/SURV/74008; BSE)", "Quarterly", "01-15,04-15,07-15,10-15", RoleCompliance, "Regulatory reports → Surveillance alert register, and each exchange's acknowledgement (NSE ENIT, BSE BEFS)"),
	}
}

// bseHoldingDuty is added for a BSE member that is not an NSE member.
func bseHoldingDuty() Duty {
	return Duty{ID: "OB-BSEHOLD", Title: "Daily holding statement to BSE (BEFS portal)", Authority: "BSE (notice 20230113-13)", Frequency: "Daily", Rule: "DAILY", Role: RoleCompliance,
		Evidence: "BEFS acknowledgement, recorded on Exchange files", Active: true, Done: []DutyRun{}, Task: "bse_holding", Rev: dutyRev}
}

func withTask(d Duty, task string) Duty { d.Task = task; return d }

// dutyTasks links duties saved before the link existed to the Periodic
// work task that does them, so a duty is not dated and chased twice.
var dutyTasks = map[string]string{"OB-RAS": "ras", "OB-RAS-M": "ras"}

// retiredDuty: duties for returns that were discontinued are switched off
// once, with a note (the weekly client funds monitoring return, NSE/INSP/61121).
func retiredDuty(d Duty) (string, bool) {
	t := strings.ToLower(d.Title)
	if d.Task == "es_return" || strings.Contains(t, "client funds monitoring") || strings.Contains(t, "enhanced supervision") {
		return "Discontinued from 18 March 2024 (NSE/INSP/61121)", true
	}
	return "", false
}

// nextDue is the first due date on or after `from` for a rule, and the
// period it belongs to. The rules that depend on the firm (RASM, RASQ,
// NETWORTH, SYS*, DAILY) are resolved by Duties.
func nextDue(rule, from string) (string, string) {
	t, err := time.Parse("2006-01-02", from)
	if err != nil {
		return "", ""
	}
	switch {
	case reDutyMonthly.MatchString(rule):
		day, _ := strconv.Atoi(rule[1:])
		for i := 0; i < 3; i++ {
			m := time.Date(t.Year(), t.Month()+time.Month(i), day, 0, 0, 0, 0, time.UTC)
			if d := m.Format("2006-01-02"); d >= from {
				return d, m.AddDate(0, -1, 0).Format("2006-01")
			}
		}
	case reDutyDates.MatchString(rule):
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

var (
	reDutyMonthly = regexp.MustCompile(`^D(0[1-9]|[12][0-9]|3[01])$`)
	reDutyDates   = regexp.MustCompile(`^\d{2}-\d{2}( *, *\d{2}-\d{2})*$`)
)

// dutyRules are the rules resolved from the firm's profile.
var dutyRules = []string{"RASM", "RASQ", "NETWORTH", "SYSPLAN", "SYSPRELIM", "SYSATR", "DAILY"}

// resolveRule turns a firm-dependent rule into dates.
func resolveRule(rule string, p ComplianceProfile) string {
	switch rule {
	case "NETWORTH":
		// Within 60 days of 31 March; within 45 days of 30 September for a
		// member with MTF, 60 days otherwise (NSE/INSP/70505, item 29).
		if p.MTF {
			return "05-30,11-14"
		}
		return "05-30,11-29"
	case "SYSPLAN":
		return sysAuditRule("plan", p.SysAudit)
	case "SYSPRELIM":
		return sysAuditRule("prelim", p.SysAudit)
	case "SYSATR":
		return sysAuditRule("atr", p.SysAudit)
	}
	return rule
}

// Obligations lists the calendar with each next due date, seeding the
// default list for a new firm.
func (o *Ops) Duties(ctx context.Context, tenant, date string) ([]Duty, error) {
	obs, err := docs.List[Duty](ctx, o.Docs, tenant, cObligation, "")
	if err != nil {
		return nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	prof := o.CompProfile(ctx, tenant)
	have := map[string]int{}
	for i, d := range obs {
		have[d.ID] = i
	}
	defs := DefaultDuties()
	if ok, _ := bseHoldingApplies(set, date); ok {
		defs = append(defs, bseHoldingDuty())
	}
	for _, x := range defs {
		i, ok := have[x.ID]
		if !ok {
			// A new firm, or a duty added to the defaults since.
			x.Since = date
			if err := docs.Upsert(ctx, o.Docs, tenant, cObligation, x.ID, x, SystemActor); err != nil {
				return nil, err
			}
			have[x.ID] = len(obs)
			obs = append(obs, x)
			continue
		}
		if y := &obs[i]; y.Rev < x.Rev {
			y.Title, y.Authority, y.Frequency, y.Rule, y.Evidence, y.Shared, y.Rev = x.Title, x.Authority, x.Frequency, x.Rule, x.Evidence, x.Shared, x.Rev
			if y.Task == "" {
				y.Task = x.Task
			}
			if err := docs.Upsert(ctx, o.Docs, tenant, cObligation, y.ID, *y, SystemActor); err != nil {
				return nil, err
			}
		}
	}
	for i := range obs {
		if why, ok := retiredDuty(obs[i]); ok && obs[i].Active {
			obs[i].Active, obs[i].Warning = false, why
			_ = docs.Upsert(ctx, o.Docs, tenant, cObligation, obs[i].ID, obs[i], SystemActor)
		}
	}
	// The AI & ML and SaaS reports, for each exchange the firm files with.
	obs = o.ensureDisclosureDuties(ctx, tenant, date, obs)
	var batches []RASBatch
	if len(prof.RAS) > 0 {
		batches, _ = docs.List[RASBatch](ctx, o.Docs, tenant, cRASBatch, "")
	}
	settled := func(r RASDate) bool {
		for _, b := range batches {
			if contains(r.Dates, b.Date) || b.Date == r.RunOn {
				return true
			}
		}
		return false
	}
	for i := range obs {
		x := &obs[i]
		if x.Done == nil {
			x.Done = []DutyRun{}
		}
		if x.Shared {
			x.Filing = sharedFilingNote(set)
		} else if x.ID == "OB-INTAUDIT" || x.ID == "OB-SURV" {
			x.Filing = eachExchangeNote(set)
		}
		if strings.HasPrefix(x.Rule, "SYS") {
			x.Frequency = sysAuditFrequency(prof.SysAudit)
			if prof.SysAudit == SysAuditNone {
				x.Warning = "Not applicable: the firm uses only the exchange's NEAT terminals (system audit cycle set to none on the compliance profile)."
				x.NextDue, x.Period, x.Overdue = "", "", false
				continue
			}
			if !prof.AuditorEmpanelled {
				x.Warning = "Record the auditor as NSE-empanelled on the compliance profile: NSE accepts system audit reports only from an empanelled auditor (NSE/INSP/69631)."
			}
			if prof.SysAudit == SysAuditTwoYearly {
				x.Warning = strings.TrimSpace(x.Warning + " Audited once in 2 years: in the year between, mark the duty done with \"not due this year\".")
			}
		}
		if x.Rule == "DAILY" && x.ID == "OB-BSEHOLD" {
			o.bseHoldingDue(ctx, tenant, set, date, x)
			continue
		}
		if x.Rule == "RASM" || x.Rule == "RASQ" {
			kind := map[string]string{"RASM": RASKindMonthly, "RASQ": RASKindQuarterly}[x.Rule]
			from := addDays(date, -120)
			if x.Since > from {
				from = x.Since
			}
			x.NextDue, x.Period = "", ""
			for k := 0; k < 30; k++ {
				r, ok := prof.rasWindow(kind, from)
				if !ok {
					x.Warning = "No " + strings.ToLower(kind) + " settlement date left in the RAS calendar: add the exchanges' calendar for the next financial year (Circulars & calendar → RAS dates)."
					break
				}
				done := settled(r)
				for _, d := range x.Done {
					done = done || d.Period == r.Period
				}
				if !done {
					x.NextDue, x.Period = r.Last(), r.Period
					break
				}
				from = addDays(r.Last(), 1)
			}
			x.Overdue = x.Active && x.NextDue != "" && x.NextDue < date
			continue
		}
		rule := resolveRule(x.Rule, prof)
		// The next due is the earliest one not yet done, which may be past.
		from := addDays(date, -120)
		if x.Since > from {
			from = x.Since
		}
		for k := 0; k < 30; k++ {
			d, p := nextDue(rule, from)
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
		if x.Task == "" {
			x.Task = dutyTasks[x.ID]
		}
		if t, ok := periodicTask(x.Task); ok {
			// Periodic work does it: its period, due date and records decide.
			st := o.periodicStatus(ctx, tenant, t, date)
			x.Period, x.NextDue, x.Overdue = st.Period, st.DueOn, x.Active && st.Status == "Overdue"
			if st.Status == "Done" || st.Status == "Nothing to do" {
				x.NextDue, x.Overdue = "", false
			}
		}
	}
	sort.SliceStable(obs, func(i, j int) bool { return obs[i].NextDue < obs[j].NextDue })
	return obs, nil
}

// bseHoldingDue dates the BSE holding statement duty: the earliest data
// day not recorded as uploaded, due the day after it.
func (o *Ops) bseHoldingDue(ctx context.Context, tenant string, set Settings, date string, x *Duty) {
	x.NextDue, x.Period, x.Overdue = "", "", false
	if ok, why := bseHoldingApplies(set, date); !ok {
		x.Filing = why
		return
	}
	x.Filing = "Upload on BEFS; BSE's API is waiting for BSE's document"
	if ps := o.bseHoldingPending(ctx, tenant, set, date); len(ps) > 0 {
		x.Period, x.NextDue = ps[0], addDays(ps[0], 1)
		x.Overdue = x.Active && x.NextDue < date
		return
	}
	if t, _ := time.Parse("2006-01-02", date); t.Weekday() != time.Sunday {
		x.Period, x.NextDue = date, addDays(date, 1)
	}
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
	if x.ID == "OB-BSEHOLD" {
		// Each day is recorded with BEFS's acknowledgement on Exchange files.
		if x.Period == "" {
			return *x, fmt.Errorf("%w: nothing is waiting to be uploaded to BSE", ErrState)
		}
		if _, err := o.BSEHoldingUploaded(ctx, tenant, actor, x.Period, evidence); err != nil {
			return *x, err
		}
		return *x, nil
	}
	if x.NextDue == "" {
		return *x, fmt.Errorf("%w: nothing is due on this duty now", ErrState)
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
	if d, _ := nextDue(x.Rule, "2026-01-01"); d == "" && !contains(dutyRules, x.Rule) {
		return x, invalid("due rule must be D07 (day of next month), MM-DD dates such as 05-15,11-15, RASM or RASQ (the exchanges' monthly or quarterly RAS dates), NETWORTH, SYSPLAN, SYSPRELIM, SYSATR or DAILY")
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
		x.Done, x.Shared = old.Done, old.Shared
	}
	// A saved duty keeps the person's version: defaults are not applied to it again.
	x.Rev, x.Filing, x.Warning, x.NextDue, x.Period, x.Overdue = dutyRev, "", "", "", "", false
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
	// The next RAS dates in Settings follow the exchanges' calendar; this
	// runs at the start of the day, before the RAS job at its end.
	r.o.syncRASSettings(r.ctx, r.tenant, SystemActor, r.date)
	prof := r.o.CompProfile(r.ctx, r.tenant)
	if end := prof.rasCalendarEnds(); end == "" || daysBetween(r.date, end) < 60 {
		r.queue("ras-calendar", "Add the exchanges' RAS calendar for the next financial year", "The running account settlement calendar in Ledgerline ends "+firstOf(fmtDay(end), "now")+". The exchanges publish the next year's Friday/Saturday dates jointly, usually in January (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197). Add them on Circulars & calendar → RAS dates.", "", RoleCompliance, "circulars", "", nil)
	}
	if n := r.o.bseHoldingChase(r.ctx, r.tenant, r.date); n > 0 {
		r.note("%d day(s) of holding statement to upload to BSE", n)
	}
	obs, err := r.o.Duties(r.ctx, r.tenant, r.date)
	if err != nil {
		return err
	}
	for _, x := range obs {
		if !x.Active || x.NextDue == "" || daysBetween(r.date, x.NextDue) > 7 || x.Task != "" { // Periodic work chases its own
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
