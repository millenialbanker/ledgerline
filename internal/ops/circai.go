package ops

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/help"
	"ledgerline/internal/ledger"
)

// ---- circulars read by AI: proposed obligations ----
//
// With the AI feature "circulars" switched on in Settings (off by default;
// never in India-only mode), the AI model reads a circular's text and
// proposes what the firm must do: each obligation with what to do, by when
// (a date, or so many days after the circular or its effective date), the
// owner role, the evidence to keep, the steps, and the part of Ledgerline
// it may change (CircAreas). The call goes through aiCompose: the gate,
// masking (the text is public, but is masked like any other), metering and
// the AI answer log.
//
// These are proposals only. Nothing becomes a task, a card in work or a
// work-queue item until the compliance officer accepts each one, changing
// it if needed, or rejects it with the reason: regulation 16C of the SEBI
// (Intermediaries) Regulations, 2008 (inserted by the SEBI (Intermediaries)
// (Amendment) Regulations, 2025, notification SEBI/LAD-NRO/GN/2025/226 of
// 6 February 2025) makes the firm solely responsible for the output of the
// AI tools it uses, so a person decides. With the feature off, refused or
// failing, a circular has its rule-based actions (triage in circulars.go),
// as before.
//
// New circulars of high or medium priority that may apply are read once,
// two a minute, from the boards' minute (circboard.go); the compliance
// officer can have any circular read again.

// Proposal statuses.
const (
	ObProposed = "Proposed"
	ObAccepted = "Accepted"
	ObRejected = "Rejected"
)

// CircObligation is one thing the AI model says a circular requires.
type CircObligation struct {
	ID       string   `json:"id"` // O1, O2 … (the task keeps it when accepted)
	What     string   `json:"what"`
	Due      string   `json:"due,omitempty"`      // a date the circular gives
	DueDays  int      `json:"due_days,omitempty"` // or so many days after …
	DueFrom  string   `json:"due_from,omitempty"` // … the circular or its effective date
	Role     string   `json:"role"`
	Evidence string   `json:"evidence,omitempty"`
	Area     string   `json:"area"`
	Steps    []string `json:"steps,omitempty"`
	Source   string   `json:"source"` // ai
	Status   string   `json:"status"` // Proposed, Accepted, Rejected
	Note     string   `json:"note,omitempty"`
	By       string   `json:"by,omitempty"`
	At       string   `json:"at,omitempty"`
	Task     string   `json:"task,omitempty"`
	// Worked out when shown: the due date and how it was set.
	DueOn   string `json:"due_on,omitempty"`
	DueRule string `json:"due_rule,omitempty"`
}

// CircularAIRead is the last time the AI model read a circular.
type CircularAIRead struct {
	At       string `json:"at"`
	By       string `json:"by"`              // who asked (system:circulars when read by itself)
	Model    string `json:"model,omitempty"` // the provider and model
	Proposed int    `json:"proposed"`
	Note     string `json:"note,omitempty"`    // why there are none, or why it was not read
	Refused  bool   `json:"refused,omitempty"` // the AI gate refused the call
}

// CircArea is a part of Ledgerline an obligation may change.
type CircArea struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Where string `json:"where"`
}

// CircAreas is the fixed list of Ledgerline areas an obligation can touch.
var CircAreas = []CircArea{
	{"cuspa", "CUSPA payment days", "Settings: payment period for unpaid securities (CUSPA)"},
	{"margins", "Margin rules", "Risk: margins, limits and square-off rules"},
	{"bsda", "BSDA", "Clients: the basic services demat account review"},
	{"nomination", "Nomination", "Clients & onboarding: nomination and opting out"},
	{"kyc", "KYC and onboarding", "Clients & onboarding, KYC & PMLA"},
	{"payouts", "Payouts and running account settlement", "Money: payouts and the RAS run"},
	{"charter", "Investor charter and disclosures", "The client portal and the website disclosures"},
	{"cscrf", "CSCRF (cyber security)", "Admin: hosting, backups, sign-in and access"},
	{"reporting", "Reporting formats", "Exchange files and regulatory reports"},
	{"charges", "Charges and contract notes", "The charge schedule and contract notes"},
	{"ucc", "Client records (UCC)", "The UCC upload"},
	{"grievances", "Complaints, SCORES and ODR", "Investor complaints"},
	{"calendar", "Compliance calendar", "Circulars & calendar: a new or changed duty"},
	{"none", "Nothing in Ledgerline", ""},
}

func circAreaOK(id string) bool {
	_, ok := circArea(id)
	return ok
}

func circArea(id string) (CircArea, bool) {
	for _, a := range CircAreas {
		if a.ID == id {
			return a, true
		}
	}
	return CircArea{}, false
}

// circSoftware says whether an area is something in Ledgerline that may
// need changing.
func circSoftware(id string) bool { return id != "" && id != "none" && circAreaOK(id) }

var (
	reAreaCUSPA   = regexp.MustCompile(`(?i)\bCUSPA\b|unpaid securities`)
	reAreaBSDA    = regexp.MustCompile(`(?i)\bBSDA\b|basic services? demat`)
	reAreaNominee = regexp.MustCompile(`(?i)\bnominat`)
	reAreaCharter = regexp.MustCompile(`(?i)investor charter`)
)

// ruleArea refines a triage rule's area from the circular's words.
func ruleArea(area, text string) string {
	switch {
	case area == "margins" && reAreaCUSPA.MatchString(text), area == "payouts" && reAreaCUSPA.MatchString(text):
		return "cuspa"
	case area == "ucc" && reAreaNominee.MatchString(text):
		return "nomination"
	case area == "grievances" && reAreaCharter.MatchString(text):
		return "charter"
	case area == "kyc" && reAreaBSDA.MatchString(text):
		return "bsda"
	}
	return area
}

// dueOn is the obligation's due date for a circular, and how it was set.
func (p CircObligation) dueOn(c Circular) string { d, _ := p.due(c); return d }

func (p CircObligation) due(c Circular) (string, string) {
	if p.Due != "" {
		return p.Due, "the date in the circular"
	}
	if p.DueDays > 0 {
		base, what := c.Date, "the circular"
		if p.DueFrom == "effective" {
			if c.Effective != "" {
				base, what = c.Effective, "the effective date"
			} else {
				what = "the circular (it gives no effective date)"
			}
		}
		return addDays(base, p.DueDays), fmt.Sprintf("%d days after %s", p.DueDays, what)
	}
	if c.Effective != "" && c.Effective >= c.Date {
		return c.Effective, "the effective date"
	}
	return addDays(c.Date, 14), "14 days after the circular (it gives no date)"
}

// task is the action an accepted proposal becomes, with the compliance
// officer's changes.
func (p CircObligation) task(c Circular, a CircularAction, actor, now string) (CircularTask, error) {
	due, rule := p.due(c)
	t := CircularTask{ID: p.ID, Title: p.What, Role: p.Role, Due: due, DueRule: rule, Status: "Open", Source: "ai", Accepted: actor, AcceptedAt: now,
		Steps: p.Steps, Needs: p.Evidence, Area: p.Area}
	if s := strings.TrimSpace(a.Title); s != "" {
		t.Title = clip(s, 300)
	}
	if a.Role != "" {
		t.Role = a.Role
	}
	if a.Due != "" && a.Due != due {
		t.Due, t.DueRule = a.Due, "set by "+actor
	}
	if a.Needs != "" {
		t.Needs = clip(strings.TrimSpace(a.Needs), 300)
	}
	if a.Area != "" {
		t.Area = a.Area
	}
	if a.Steps != nil {
		t.Steps = cleanSteps(a.Steps)
	}
	t.NoEvidence = a.NoEvidence
	switch {
	case t.Title == "":
		return t, invalid("say what has to be done")
	case !contains(circRoles, t.Role):
		return t, invalid("choose the owner role")
	case !ledger.ValidDate(t.Due):
		return t, invalid("the due date must be YYYY-MM-DD")
	case !circAreaOK(t.Area):
		return t, invalid("unknown Ledgerline area %q", t.Area)
	case !t.NoEvidence && strings.TrimSpace(t.Needs) == "":
		return t, invalid("say what evidence to keep, or tick that none is needed")
	}
	return t, nil
}

const circAIInstr = `You read one regulatory circular issued to market intermediaries in India (by SEBI, a stock exchange, a clearing corporation or a depository) and list what a stockbroking firm must do because of it.
Use ONLY the circular text provided. Do not invent dates, numbers or requirements.
Reply with one obligation per line, at most 8 lines, and nothing else (no heading, no numbering, no blank lines), in this form:
what to do | when | owner | evidence to keep | area | step; step; step
- what to do: one short imperative sentence, under 25 words.
- when: a date as YYYY-MM-DD if the circular gives one; or +N circular, or +N effective, for N days after the circular's date or its effective date; or - if it gives no deadline.
- owner: one of Compliance officer, Ops checker, Ops maker, Risk manager, Admin.
- evidence to keep: what shows it was done, under 15 words.
- area: one of cuspa, margins, bsda, nomination, kyc, payouts, charter, cscrf, reporting, charges, ucc, grievances, calendar, none.
- steps: up to 5 short steps separated by semicolons.
Keep tokens such as [CLIENT-1] or [PAN-1] exactly as they are. Give no investment advice. If the circular asks nothing of a stock broker or depository participant, reply with the single word NONE.`

// circAIText is what the model is sent: the circular, and nothing about
// the firm.
func circAIText(c Circular) string {
	var s strings.Builder
	fmt.Fprintf(&s, "Issued by: %s\nCircular number: %s\nDate: %s\n", c.Source, c.Ref, c.Date)
	if c.Dept != "" {
		fmt.Fprintf(&s, "Department: %s\n", c.Dept)
	}
	if c.Effective != "" {
		fmt.Fprintf(&s, "Effective date: %s\n", c.Effective)
	}
	fmt.Fprintf(&s, "Subject: %s\n\nText:\n%s\n", c.Subject, clip(strings.TrimSpace(c.Text), 20000))
	return s.String()
}

var (
	reCircAIDays = regexp.MustCompile(`(?i)^\+?\s*(\d{1,3})\s*(?:days?\s*(?:after|from)?\s*(?:the)?\s*)?(circular|effective)?`)
	reCircAIDate = regexp.MustCompile(`\b(20\d\d-\d\d-\d\d)\b`)
)

// circRole reads an owner the model named.
func circRole(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "compliance"):
		return RoleCompliance
	case strings.Contains(l, "checker"):
		return RoleChecker
	case strings.Contains(l, "maker"), strings.Contains(l, "operations"), strings.Contains(l, "ops"):
		return RoleMaker
	case strings.Contains(l, "risk"):
		return RoleRisk
	case strings.Contains(l, "admin"), strings.Contains(l, "it "), l == "it", strings.Contains(l, "technology"):
		return RoleAdmin
	}
	return RoleCompliance
}

// parseCircAI reads the model's lines as proposals, numbered from next.
func parseCircAI(reply string, c Circular, next int) ([]CircObligation, string) {
	var out []CircObligation
	skipped := 0
	seen := map[string]bool{}
	for _, l := range strings.Split(strings.ReplaceAll(reply, "\r", ""), "\n") {
		l = strings.TrimSpace(checkLeadRe.ReplaceAllString(l, ""))
		l = strings.Trim(l, "`*")
		if l == "" || strings.EqualFold(strings.Trim(l, "."), "none") {
			continue
		}
		if !strings.Contains(l, "|") {
			skipped++
			continue
		}
		f := strings.Split(l, "|")
		for len(f) < 6 {
			f = append(f, "")
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		what := clip(strings.Trim(f[0], `"`), 300)
		if len([]rune(what)) < 5 || strings.EqualFold(what, "what to do") || seen[strings.ToLower(what)] {
			skipped++
			continue
		}
		seen[strings.ToLower(what)] = true
		p := CircObligation{ID: fmt.Sprintf("O%d", next+len(out)), What: what, Role: circRole(f[2]), Evidence: clip(f[3], 300), Area: strings.ToLower(f[4]), Source: "ai", Status: ObProposed}
		if !circAreaOK(p.Area) {
			p.Area = "none"
		}
		switch w := f[1]; {
		case reCircAIDate.MatchString(w):
			d := reCircAIDate.FindString(w)
			if ledger.ValidDate(d) && d >= c.Date {
				p.Due = d
			}
		case reCircAIDays.MatchString(w):
			m := reCircAIDays.FindStringSubmatch(w)
			if n, _ := strconv.Atoi(m[1]); n > 0 && n <= 730 {
				p.DueDays, p.DueFrom = n, "circular"
				if strings.EqualFold(m[2], "effective") {
					p.DueFrom = "effective"
				}
			}
		}
		for _, s := range strings.Split(f[5], ";") {
			if s = strings.TrimSpace(s); s != "" && len(p.Steps) < 6 {
				p.Steps = append(p.Steps, clip(s, 200))
			}
		}
		out = append(out, p)
		if len(out) == 8 {
			break
		}
	}
	note := ""
	switch {
	case len(out) == 0 && strings.Contains(strings.ToUpper(reply), "NONE"):
		note = "The AI model found nothing this circular asks of a stock broker or depository participant."
	case len(out) == 0:
		note = "The AI model's reply could not be read as obligations; the rule-based actions stand."
	case skipped > 0:
		note = fmt.Sprintf("%d line(s) of the reply could not be read and were left out.", skipped)
	}
	return out, note
}

// circAIActor reads new circulars by itself.
const circAIActor = "system:circulars"

// ReadCircularAI has the AI model read a circular and propose obligations.
// Earlier proposals not yet accepted or rejected are replaced; accepted
// and rejected ones stay.
func (o *Ops) ReadCircularAI(ctx context.Context, tenant, actor, id string) (Circular, error) {
	if actor != circAIActor {
		if err := o.require(ctx, tenant, actor, "Reading circulars with AI", RoleCompliance); err != nil {
			return Circular{}, err
		}
	}
	c, _, err := docs.Get[Circular](ctx, o.Docs, tenant, cCircular, id)
	if err != nil {
		return Circular{}, fmt.Errorf("%w: no such circular", ErrNotFound)
	}
	if len(strings.TrimSpace(c.Text)) < 40 {
		return c, invalid("Ledgerline has no text of this circular to read: load it again with its text (the Text column of the CIRCULARS file)")
	}
	now := o.now().Format(time.RFC3339)
	reply, by, err := o.aiCompose(ctx, tenant, actor, AIFeatureCirculars, circAIInstr, circAIText(c), "Obligations from circular "+c.Source+" "+c.Ref, 600)
	if err != nil {
		note, refused := aiRefusal(err)
		if !refused {
			note = strings.TrimPrefix(err.Error(), "invalid state: ")
		}
		_, _ = docs.Update(ctx, o.Docs, tenant, cCircular, id, actor, func(x *Circular) error {
			x.AI = &CircularAIRead{At: now, By: actor, Note: note + " The rule-based actions stand.", Refused: refused}
			return nil
		})
		if refused {
			return c, invalid("%s", note)
		}
		return c, err
	}
	var obs []CircObligation
	saved, err := docs.Update(ctx, o.Docs, tenant, cCircular, id, actor, func(x *Circular) error {
		next := 1
		var keep []CircObligation
		for _, p := range x.Proposals {
			if n, _ := strconv.Atoi(strings.TrimPrefix(p.ID, "O")); n >= next {
				next = n + 1
			}
			if p.Status != ObProposed {
				keep = append(keep, p)
			}
		}
		var note string
		obs, note = parseCircAI(reply, *x, next)
		x.Proposals = append(keep, obs...)
		x.AI = &CircularAIRead{At: now, By: actor, Model: by, Proposed: len(obs), Note: note}
		return nil
	})
	if err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "circular.ai_read", id, map[string]any{"proposed": len(obs), "model": by})
	saved.annotate(o.firmFacts(ctx, tenant))
	return saved, nil
}

// CircularsState is what the Calendar & circulars screen needs besides
// the circulars: AI reading, the Ledgerline areas and the Circulars board.
type CircularsState struct {
	AIOn      bool       `json:"ai_on"`
	AIWhy     string     `json:"ai_why,omitempty"`
	Areas     []CircArea `json:"areas"`
	Board     string     `json:"board,omitempty"` // the Circulars board, when set up
	Questions []string   `json:"questions"`       // the firm profile's open questions
	CanReview bool       `json:"can_review"`
}

// CircularsStateFor is the screen's state for a person.
func (o *Ops) CircularsStateFor(ctx context.Context, tenant, actor string) (CircularsState, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return CircularsState{}, err
	}
	v := CircularsState{Areas: CircAreas, Questions: []string{}, CanReview: role == RoleCompliance || role == RoleAdmin}
	v.AIOn, v.AIWhy = o.aiFeatureState(ctx, tenant, AIFeatureCirculars)
	if b, ok := o.circBoard(ctx, tenant); ok {
		v.Board = b.ID
	}
	if v.CanReview {
		if p, err := o.FirmProfileFor(ctx, tenant, actor); err == nil {
			v.Questions = p.Questions
		}
	}
	return v, nil
}

// CircularAIAvailable says whether the server can read circulars with AI
// at all (a model that composes is connected, and not India-only mode).
func (o *Ops) CircularAIAvailable() bool {
	_, ok := o.LedgyAI.(help.Composer)
	return ok && o.LedgyAI != nil && o.Meter != nil && !o.Residency.IndiaOnly && !o.AIOff
}
