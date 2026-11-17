package ops

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- self-inspection: the inspector's tests, every night ----
//
// The tests an exchange inspector or internal auditor runs, run against
// the firm's own data every night after the day's work (and whenever the
// compliance officer or an Admin presses Run now), so that a breach is
// found the next morning rather than at the half-yearly audit. The tests
// are in selfinspect_lib.go; each reuses the check Ledgerline already
// makes on its own screen (the segregation check, CUSPA ageing, running
// account settlement, the complaints register, the file timetable…).
//
// A run keeps every test's result as it was: what was checked, how much
// was found, a few examples (record IDs, client codes masked, never PAN,
// bank or demat numbers) and the dates of the data it read. A past run is
// shown exactly as it was.
//
// Each failing test opens one exception, which is a card on the
// Ledgerline board "Self-inspection" (source "inspection", boardsync.go):
// New → Being fixed → Fixed, waiting for the next run → Closed, or
// Accepted risk. The next run that finds the test passing closes it by
// itself; a test that still fails moves a "fixed" exception back to Being
// fixed. Accepting a risk needs a reason and a review date, and is the
// compliance officer's own decision (not an Admin's); so is muting a test.
// Both go to the audit trail.
//
// The score is the pass rate of the tests that ran, weighted by severity
// (high 3, medium 2, low 1). Tests waiting for data, skipped (nothing to
// check, not applicable) and muted are not counted; an accepted risk still
// counts as a failure, because the test still fails.
//
// SelfInspectionSummary gives every run of a period and how each failing
// test was handled, for the inspection pack.

const (
	cSIRun  = "selfinspect_run"  // a run, by ID (SIR-0000001)
	cSIExc  = "selfinspect_exc"  // an exception (a failing test), by ID (SIX-<test>-<n>)
	cSIMute = "selfinspect_mute" // a mute of a test, by <test>|<n>
	cSISet  = "selfinspect_set"  // the firm's schedule: "settings"

	siSource = "inspection" // the Ledgerline board's source and the card link's kind

	siDefaultAt  = "22:30" // the nightly run, IST, after the day's work
	siTrendDays  = 90
	siMaxExample = 5
)

// Result statuses.
const (
	SIPass    = "pass"
	SIFail    = "fail"
	SISkipped = "skipped" // waiting for data, nothing to check, or not applicable
	SIMuted   = "muted"
	SIError   = "error"
)

// Exception statuses (the board's lists).
const (
	SIExcNew      = "new"
	SIExcFixing   = "fixing"
	SIExcFixed    = "fixed"
	SIExcAccepted = "accepted"
	SIExcClosed   = "closed"
)

// SIExcNames are the exception statuses as people read them.
var SIExcNames = map[string]string{SIExcNew: "New", SIExcFixing: "Being fixed", SIExcFixed: "Fixed, waiting for the next run", SIExcAccepted: "Accepted risk", SIExcClosed: "Closed"}

// Severities, their weight in the score and how long an exception has.
var (
	siWeight  = map[string]int{"high": 3, "medium": 2, "low": 1}
	siDueDays = map[string]int{"high": 3, "medium": 10, "low": 30}
	siSevName = map[string]string{"high": "High", "medium": "Medium", "low": "Low"}
)

// SITest is one test of the library.
type SITest struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Area     string `json:"area"`
	Severity string `json:"severity"` // high, medium, low
	Owner    string `json:"owner"`    // the role that fixes it
	// Source is the rule: circular number and date. ToConfirm says what of
	// it could not be confirmed against a primary source.
	Source    string `json:"source"`
	ToConfirm string `json:"to_confirm,omitempty"`
	Checks    string `json:"checks"` // what the test looks at, in words
	Fix       string `json:"fix"`    // how to fix a failure
	Screen    string `json:"screen"` // the console screen where it is fixed
	// ScreenName is the screen as the menu names it.
	ScreenName string `json:"screen_name"`
	// Real is false for a test waiting for data Ledgerline does not have;
	// Waiting says what.
	Real    bool   `json:"real"`
	Waiting string `json:"waiting,omitempty"`
	run     func(e *siEnv) (siOut, error)
}

// SIInput is a piece of data a result was worked out from, with its date.
type SIInput struct {
	Name string `json:"name"`
	Date string `json:"date"`
}

// SIResult is one test's result in a run, kept as it was.
type SIResult struct {
	Test      string    `json:"test"`
	Title     string    `json:"title"`
	Area      string    `json:"area"`
	Severity  string    `json:"severity"`
	Owner     string    `json:"owner"`
	Source    string    `json:"source"`
	ToConfirm string    `json:"to_confirm,omitempty"`
	Status    string    `json:"status"` // pass, fail, skipped, muted, error
	Checked   int       `json:"checked"`
	Found     int       `json:"found"`
	Examples  []string  `json:"examples"`
	Summary   string    `json:"summary"`
	Inputs    []SIInput `json:"inputs"`
	// Muted: what the test found while it was muted (would have passed or
	// failed), and the mute.
	MutedAs   string `json:"muted_as,omitempty"`
	Mute      string `json:"mute,omitempty"`
	Exception string `json:"exception,omitempty"` // the exception it opened, updated or closed
}

// SIRun is one self-inspection.
type SIRun struct {
	ID      string     `json:"id"`
	Date    string     `json:"date"` // the business date inspected
	At      string     `json:"at"`
	By      string     `json:"by"`
	Trigger string     `json:"trigger"` // nightly, run now
	Results []SIResult `json:"results"`
	Score   float64    `json:"score"` // weighted pass rate, 0–100
	// Weights behind the score.
	PassWeight  int    `json:"pass_weight"`
	TotalWeight int    `json:"total_weight"`
	Passed      int    `json:"passed"`
	Failed      int    `json:"failed"`
	Skipped     int    `json:"skipped"`
	Muted       int    `json:"muted"`
	Errors      int    `json:"errors"`
	New         int    `json:"new_exceptions"`
	Closed      int    `json:"closed_exceptions"`
	Mailed      string `json:"mailed,omitempty"`
}

// SIEvent is one step in an exception's life.
type SIEvent struct {
	At   string `json:"at"`
	By   string `json:"by"`
	Role string `json:"role,omitempty"`
	What string `json:"what"`
	Note string `json:"note,omitempty"`
	Run  string `json:"run,omitempty"`
}

// SIAcceptance is a risk the compliance officer accepted.
type SIAcceptance struct {
	By     string `json:"by"`
	Role   string `json:"role"`
	Reason string `json:"reason"`
	Until  string `json:"until"` // reviewed again by then
	At     string `json:"at"`
}

// SIException is a failing test: a card on the Self-inspection board.
type SIException struct {
	ID         string        `json:"id"`
	Test       string        `json:"test"`
	Title      string        `json:"title"`
	Area       string        `json:"area"`
	Severity   string        `json:"severity"`
	Owner      string        `json:"owner"`
	Source     string        `json:"source"`
	Fix        string        `json:"fix"`
	Screen     string        `json:"screen"`
	Status     string        `json:"status"` // new, fixing, fixed, accepted, closed
	Found      int           `json:"found"`
	Checked    int           `json:"checked"`
	Examples   []string      `json:"examples"`
	Summary    string        `json:"summary"`
	Opened     string        `json:"opened"` // business date first found
	OpenedRun  string        `json:"opened_run"`
	LastRun    string        `json:"last_run"`
	LastDate   string        `json:"last_date"` // business date last found failing
	Due        string        `json:"due"`       // by severity from when it was opened
	Closed     string        `json:"closed,omitempty"`
	ClosedRun  string        `json:"closed_run,omitempty"`
	Accepted   *SIAcceptance `json:"accepted,omitempty"`
	History    []SIEvent     `json:"history"`
	Handled    string        `json:"handled,omitempty"` // how it was handled, in words (filled on reading)
	StatusName string        `json:"status_name,omitempty"`
}

// SIMute is a test muted by the compliance officer.
type SIMute struct {
	ID        string `json:"id"`
	Test      string `json:"test"`
	Reason    string `json:"reason"`
	From      string `json:"from"`
	Until     string `json:"until"`
	By        string `json:"by"`
	At        string `json:"at"`
	EndedAt   string `json:"ended_at,omitempty"`
	EndedBy   string `json:"ended_by,omitempty"`
	EndReason string `json:"end_reason,omitempty"`
}

func (m SIMute) activeOn(date string) bool {
	return m.EndedAt == "" && m.From <= date && date <= m.Until
}

// SISettings is the firm's nightly schedule.
type SISettings struct {
	Off         bool   `json:"off"` // the nightly run is switched off
	At          string `json:"at"`  // HH:MM, IST
	LastNightly string `json:"last_nightly,omitempty"`
	By          string `json:"by,omitempty"`
	Changed     string `json:"changed,omitempty"`
}

func (o *Ops) siSettings(ctx context.Context, tenant string) SISettings {
	s, _, _ := docs.GetOr(ctx, o.Docs, tenant, cSISet, "settings", SISettings{})
	if _, err := time.Parse("15:04", s.At); err != nil {
		s.At = siDefaultAt
	}
	return s
}

// ---- running ----

// siEnv is what one run knows.
type siEnv struct {
	o       *Ops
	ctx     context.Context
	tenant  string
	date    string
	now     time.Time
	set     Settings
	clients map[string]Client
	first   *string // the first trade date in Ledgerline, once read
}

// siOut is what a test found.
type siOut struct {
	skip     string // set: skipped, with why
	checked  int
	found    int
	examples []string
	summary  string
	inputs   []SIInput
}

func (s *siOut) bad(example string) {
	s.found++
	if len(s.examples) < siMaxExample && example != "" {
		s.examples = append(s.examples, bsScrub(example))
	}
}

func (s *siOut) input(name, date string) {
	s.inputs = append(s.inputs, SIInput{Name: name, Date: date})
}

func siSkip(why string, inputs ...SIInput) (siOut, error) {
	return siOut{skip: why, inputs: inputs}, nil
}

// siMask masks a client code for an example: its first two and last two
// characters.
func siMask(ucc string) string {
	u := strings.TrimSpace(ucc)
	switch n := len(u); {
	case n == 0:
		return ""
	case n <= 3:
		return u[:1] + strings.Repeat("*", n-1)
	case n <= 5:
		return u[:2] + strings.Repeat("*", n-2)
	default:
		return u[:2] + strings.Repeat("*", n-4) + u[n-2:]
	}
}

// siClient names a client in an example: the masked code only.
func siClient(ucc string) string {
	if ucc == "" {
		return ""
	}
	return "client " + siMask(ucc)
}

// runOne runs a test, turning a panic into an error result.
func (e *siEnv) runOne(t SITest) (out siOut, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the test stopped: %v", r)
			e.o.log(e.ctx, e.tenant, SystemActor, "selfinspect.test_failed", t.ID, map[string]any{"panic": fmt.Sprint(r), "stack": clip(string(debug.Stack()), 2000)})
		}
	}()
	return t.run(e)
}

// SelfInspectionTests is the library, in the order the screen lists it.
func SelfInspectionTests() []SITest { return append([]SITest(nil), siTests...) }

func siTest(id string) (SITest, bool) {
	for _, t := range siTests {
		if t.ID == id {
			return t, true
		}
	}
	return SITest{}, false
}

// RunSelfInspection runs every test now for date (default: the business
// date), as the compliance officer or an Admin.
func (o *Ops) RunSelfInspection(ctx context.Context, tenant, actor, date string) (SIRun, error) {
	if err := o.require(ctx, tenant, actor, "Running the self-inspection", RoleCompliance); err != nil {
		return SIRun{}, err
	}
	bd := o.businessDate(ctx, tenant)
	if date == "" {
		date = bd
	}
	if !validDate(date) || date > o.now().Format("2006-01-02") {
		return SIRun{}, invalid("give a date that has passed (YYYY-MM-DD)")
	}
	return o.siRun(ctx, tenant, actor, date, "run now")
}

// siEnvFor is what the tests of one run read, for date.
func (o *Ops) siEnvFor(ctx context.Context, tenant, date string) (*siEnv, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return nil, err
	}
	cl, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return &siEnv{o: o, ctx: ctx, tenant: tenant, date: date, now: o.now(), set: set, clients: cl}, nil
}

func (o *Ops) siRun(ctx context.Context, tenant, actor, date, trigger string) (SIRun, error) {
	e, err := o.siEnvFor(ctx, tenant, date)
	if err != nil {
		return SIRun{}, err
	}
	now, set := e.now, e.set
	mutes, _ := o.siMutes(ctx, tenant)
	muted := map[string]SIMute{}
	for _, m := range mutes {
		if m.activeOn(date) {
			muted[m.Test] = m
		}
	}
	run := SIRun{Date: date, At: now.Format(time.RFC3339), By: actor, Trigger: trigger, Results: []SIResult{}}
	for _, t := range siTests {
		r := SIResult{Test: t.ID, Title: t.Title, Area: t.Area, Severity: t.Severity, Owner: t.Owner, Source: t.Source, ToConfirm: t.ToConfirm, Examples: []string{}, Inputs: []SIInput{}}
		switch {
		case !t.Real:
			r.Status, r.Summary = SISkipped, "Waiting for data: "+t.Waiting
		default:
			out, err := e.runOne(t)
			switch {
			case err != nil:
				r.Status, r.Summary = SIError, "The test could not run: "+err.Error()
			case out.skip != "":
				r.Status, r.Summary = SISkipped, out.skip
			case out.found > 0:
				r.Status = SIFail
			default:
				r.Status = SIPass
			}
			if err == nil {
				r.Checked, r.Found, r.Inputs = out.checked, out.found, append(r.Inputs, out.inputs...)
				r.Examples = append(r.Examples, out.examples...)
				if out.skip == "" {
					r.Summary = out.summary
				}
			}
		}
		if m, ok := muted[t.ID]; ok && (r.Status == SIPass || r.Status == SIFail) {
			r.MutedAs, r.Status, r.Mute = r.Status, SIMuted, m.ID
		}
		run.Results = append(run.Results, r)
	}
	siScore(&run)
	n, err := o.next(ctx, tenant, "selfinspect_run", actor)
	if err != nil {
		return run, err
	}
	run.ID = fmt.Sprintf("SIR-%07d", n)
	if err := o.siApply(ctx, tenant, actor, &run); err != nil {
		return run, err
	}
	if trigger == "nightly" && run.Failed > 0 {
		run.Mailed = o.siMail(ctx, tenant, set, run)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSIRun, run.ID, run, actor); err != nil {
		return run, err
	}
	o.log(ctx, tenant, actor, "selfinspect.run", run.ID, map[string]any{"date": date, "trigger": trigger, "score": run.Score, "passed": run.Passed, "failed": run.Failed,
		"skipped": run.Skipped, "muted": run.Muted, "errors": run.Errors, "new": run.New, "closed": run.Closed})
	o.siSyncBoard(ctx, tenant)
	return run, nil
}

// siScore counts a run's results and works out its weighted score.
func siScore(run *SIRun) {
	run.Passed, run.Failed, run.Skipped, run.Muted, run.Errors, run.PassWeight, run.TotalWeight = 0, 0, 0, 0, 0, 0, 0
	for _, r := range run.Results {
		w := siWeight[r.Severity]
		switch r.Status {
		case SIPass:
			run.Passed++
			run.PassWeight += w
			run.TotalWeight += w
		case SIFail:
			run.Failed++
			run.TotalWeight += w
		case SIMuted:
			run.Muted++
		case SIError:
			run.Errors++
		default:
			run.Skipped++
		}
	}
	run.Score = 100
	if run.TotalWeight > 0 {
		run.Score = math.Round(float64(run.PassWeight)*1000/float64(run.TotalWeight)) / 10
	}
}

// siApply opens, updates and closes exceptions from a run's results, and
// notes on each result the exception it touched.
func (o *Ops) siApply(ctx context.Context, tenant, actor string, run *SIRun) error {
	all, err := docs.List[SIException](ctx, o.Docs, tenant, cSIExc, "")
	if err != nil {
		return err
	}
	open := map[string]SIException{}
	for _, x := range all {
		if x.Status != SIExcClosed {
			if p, ok := open[x.Test]; !ok || x.Opened > p.Opened {
				open[x.Test] = x
			}
		}
	}
	at := o.now().Format(time.RFC3339)
	for i := range run.Results {
		r := &run.Results[i]
		x, has := open[r.Test]
		switch {
		case r.Status == SIFail && !has:
			t, _ := siTest(r.Test)
			n, err := o.next(ctx, tenant, "selfinspect_exc:"+r.Test, actor)
			if err != nil {
				return err
			}
			x = SIException{ID: fmt.Sprintf("SIX-%s-%d", r.Test, n), Test: r.Test, Title: r.Title, Area: r.Area, Severity: r.Severity, Owner: r.Owner, Source: r.Source,
				Fix: t.Fix, Screen: t.Screen, Status: SIExcNew, Found: r.Found, Checked: r.Checked, Examples: r.Examples, Summary: r.Summary,
				Opened: run.Date, OpenedRun: run.ID, LastRun: run.ID, LastDate: run.Date, Due: addDays(run.Date, siDueDays[r.Severity]),
				History: []SIEvent{{At: at, By: actor, What: "found", Note: fmt.Sprintf("%d found", r.Found), Run: run.ID}}}
			if err := docs.Upsert(ctx, o.Docs, tenant, cSIExc, x.ID, x, actor); err != nil {
				return err
			}
			o.log(ctx, tenant, actor, "selfinspect.exception.opened", x.ID, map[string]any{"test": r.Test, "found": r.Found, "run": run.ID, "due": x.Due})
			run.New++
			r.Exception = x.ID
		case r.Status == SIFail:
			nx, err := docs.Update(ctx, o.Docs, tenant, cSIExc, x.ID, actor, func(y *SIException) error {
				y.Found, y.Checked, y.Examples, y.Summary, y.LastRun, y.LastDate = r.Found, r.Checked, r.Examples, r.Summary, run.ID, run.Date
				switch {
				case y.Status == SIExcFixed:
					y.Status = SIExcFixing
					y.History = append(y.History, SIEvent{At: at, By: actor, What: "still failing", Note: fmt.Sprintf("marked fixed, but the run found %d", r.Found), Run: run.ID})
				case y.Status == SIExcAccepted && y.Accepted != nil && y.Accepted.Until < run.Date:
					y.Status, y.Due = SIExcNew, addDays(run.Date, siDueDays[y.Severity])
					y.History = append(y.History, SIEvent{At: at, By: actor, What: "acceptance ended", Note: "the risk was accepted until " + y.Accepted.Until + " and the test still fails", Run: run.ID})
				}
				return nil
			})
			if err != nil {
				return err
			}
			if nx.Status != x.Status {
				o.log(ctx, tenant, actor, "selfinspect.exception.reopened", x.ID, map[string]any{"from": x.Status, "to": nx.Status, "run": run.ID})
			}
			r.Exception = x.ID
		case r.Status == SIPass && has:
			if _, err := docs.Update(ctx, o.Docs, tenant, cSIExc, x.ID, actor, func(y *SIException) error {
				y.Status, y.Closed, y.ClosedRun, y.LastRun = SIExcClosed, at, run.ID, run.ID
				y.Found, y.Checked, y.Examples, y.Summary = 0, r.Checked, []string{}, r.Summary
				y.History = append(y.History, SIEvent{At: at, By: actor, What: "passed", Note: "closed by itself: the test passed", Run: run.ID})
				return nil
			}); err != nil {
				return err
			}
			o.log(ctx, tenant, actor, "selfinspect.exception.closed", x.ID, map[string]any{"test": r.Test, "run": run.ID, "was": x.Status})
			run.Closed++
			r.Exception = x.ID
		case has:
			r.Exception = x.ID // muted, skipped or not run: the exception stays as it is
		}
	}
	return nil
}

// siMail tells the compliance officer about a nightly run with failures:
// counts and a link only, never a test's title, a card's title or a
// client.
func (o *Ops) siMail(ctx context.Context, tenant string, set Settings, run SIRun) string {
	to := strings.TrimSpace(set.ComplianceEmail)
	if !reEmail.MatchString(to) {
		return ""
	}
	sev := map[string]int{}
	for _, r := range run.Results {
		if r.Status == SIFail {
			sev[r.Severity]++
		}
	}
	link := "#selfinspect"
	if base := o.bnFirm(ctx, tenant).Console; base != "" {
		link = base + "/#selfinspect"
	}
	subject := fmt.Sprintf("[%s] Self-inspection for %s: %d of %d tests failed", set.Firm, fmtDay(run.Date), run.Failed, run.Passed+run.Failed)
	body := fmt.Sprintf("The nightly self-inspection for %s found %d failing tests: %d high, %d medium and %d low. %d are new; %d closed because their test passed. Score: %.1f%%.\n\nSee them in Ledgerline, Compliance → Self-inspection: %s\n",
		fmtDay(run.Date), run.Failed, sev["high"], sev["medium"], sev["low"], run.New, run.Closed, run.Score, link)
	return o.mailStaff(ctx, tenant, to, subject, body, "selfinspect:"+run.ID, nil)
}

// ---- the nightly run ----

// selfInspectTick runs the self-inspection once a business day, at the
// firm's time (after the day's work), in every edition: it changes nothing
// in the firm's work, it only finds what needs fixing.
func (o *Ops) selfInspectTick(ctx context.Context, tenant string, now time.Time) {
	s := o.siSettings(ctx, tenant)
	now = now.In(ist)
	today, hm := now.Format("2006-01-02"), now.Format("15:04")
	if s.Off || hm < s.At || s.LastNightly == today {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	if !set.IsBusinessDay(today) {
		return
	}
	// Claim the night, so two servers do not both run it.
	if _, _, err := docs.Get[SISettings](ctx, o.Docs, tenant, cSISet, "settings"); errors.Is(err, docs.ErrNotFound) {
		if _, err := docs.Put(ctx, o.Docs, tenant, cSISet, "settings", SISettings{At: s.At}, 0, SystemActor); err != nil && !errors.Is(err, docs.ErrStale) {
			return
		}
	}
	claimed := false
	if _, err := docs.Update(ctx, o.Docs, tenant, cSISet, "settings", SystemActor, func(x *SISettings) error {
		if x.LastNightly == today || x.Off {
			return errBSSame
		}
		x.LastNightly, claimed = today, true
		return nil
	}); err != nil || !claimed {
		return
	}
	if _, err := o.siRun(ctx, tenant, SystemActor, today, "nightly"); err != nil {
		o.log(ctx, tenant, SystemActor, "selfinspect.run_failed", today, map[string]any{"error": err.Error()})
	}
}

// SaveSelfInspectionSettings switches the nightly run on or off and sets
// its time.
func (o *Ops) SaveSelfInspectionSettings(ctx context.Context, tenant, actor string, s SISettings) (SISettings, error) {
	if err := o.require(ctx, tenant, actor, "Changing the self-inspection schedule", RoleCompliance); err != nil {
		return SISettings{}, err
	}
	if _, err := time.Parse("15:04", s.At); err != nil {
		return SISettings{}, invalid("give the time as HH:MM (IST)")
	}
	if _, _, err := docs.Get[SISettings](ctx, o.Docs, tenant, cSISet, "settings"); errors.Is(err, docs.ErrNotFound) {
		if _, err := docs.Put(ctx, o.Docs, tenant, cSISet, "settings", SISettings{At: siDefaultAt}, 0, actor); err != nil && !errors.Is(err, docs.ErrStale) {
			return SISettings{}, err
		}
	}
	out, err := docs.Update(ctx, o.Docs, tenant, cSISet, "settings", actor, func(x *SISettings) error {
		x.Off, x.At, x.By, x.Changed = s.Off, s.At, actor, o.now().Format(time.RFC3339)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "selfinspect.schedule", "settings", map[string]any{"nightly": !s.Off, "at": s.At})
	}
	return out, err
}

// ---- working an exception ----

// siOwnerOrCompliance checks that the person works this exception: its
// owner role, the compliance officer or an Admin.
func (o *Ops) siOwnerOrCompliance(ctx context.Context, tenant, actor string, x SIException) (string, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return "", err
	}
	if role != x.Owner && role != RoleCompliance && role != RoleAdmin {
		return role, fmt.Errorf("%w: %s is for the %s or the compliance officer to work (you are %s)", ErrForbidden, x.Title, strings.ToLower(x.Owner), role)
	}
	return role, nil
}

// SetSelfInspectionException moves an exception to Being fixed or Fixed
// (waiting for the next run), with a note.
func (o *Ops) SetSelfInspectionException(ctx context.Context, tenant, actor, id, status, note string) (SIException, error) {
	x, err := o.siSetStatus(ctx, tenant, actor, id, status, note)
	if err == nil {
		o.siSyncBoard(ctx, tenant)
	}
	return x, err
}

func (o *Ops) siSetStatus(ctx context.Context, tenant, actor, id, status, note string) (SIException, error) {
	if err := o.require(ctx, tenant, actor, "Working a self-inspection exception", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return SIException{}, err
	}
	if status != SIExcFixing && status != SIExcFixed {
		return SIException{}, invalid("an exception can be moved to Being fixed or Fixed; it closes by itself when the test passes, and accepting a risk needs the compliance officer's reason")
	}
	x, _, err := docs.Get[SIException](ctx, o.Docs, tenant, cSIExc, id)
	if err != nil {
		return x, err
	}
	role, err := o.siOwnerOrCompliance(ctx, tenant, actor, x)
	if err != nil {
		return x, err
	}
	if x.Status == SIExcClosed {
		return x, fmt.Errorf("%w: %s is closed: the test passed", ErrState, x.ID)
	}
	if x.Status == status {
		return x, nil
	}
	note = strings.TrimSpace(note)
	if status == SIExcFixed && note == "" {
		return x, invalid("say what was fixed; the next run checks it")
	}
	at := o.now().Format(time.RFC3339)
	from := x.Status
	x, err = docs.Update(ctx, o.Docs, tenant, cSIExc, id, actor, func(y *SIException) error {
		if y.Status == SIExcClosed {
			return fmt.Errorf("%w: %s is closed: the test passed", ErrState, y.ID)
		}
		y.Status = status
		what := map[string]string{SIExcFixing: "being fixed", SIExcFixed: "fixed, waiting for the next run"}[status]
		if from == SIExcAccepted {
			what += " (the accepted risk is withdrawn)"
			y.Accepted = nil
		}
		y.History = append(y.History, SIEvent{At: at, By: actor, Role: role, What: what, Note: clip(note, 500)})
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "selfinspect.exception."+status, id, map[string]any{"test": x.Test, "from": from, "note": note})
	}
	return x, err
}

// AcceptSelfInspectionRisk records the compliance officer's decision to
// accept a failing test's risk until a review date, with the reason. An
// Admin cannot: it is the compliance officer's own decision.
func (o *Ops) AcceptSelfInspectionRisk(ctx context.Context, tenant, actor, id, reason, until string) (SIException, error) {
	if err := o.require(ctx, tenant, actor, "Accepting a self-inspection risk", RoleCompliance); err != nil {
		return SIException{}, err
	}
	if err := o.siComplianceOnly(ctx, tenant, actor, "Accepting a risk"); err != nil {
		return SIException{}, err
	}
	reason = strings.TrimSpace(reason)
	if len(reason) < 10 {
		return SIException{}, invalid("give the reason the risk is accepted, so an inspector can read it")
	}
	today := o.businessDate(ctx, tenant)
	if !validDate(until) || until <= today || until > addDays(today, 365) {
		return SIException{}, invalid("give a review date after today and within a year")
	}
	at := o.now().Format(time.RFC3339)
	x, err := docs.Update(ctx, o.Docs, tenant, cSIExc, id, actor, func(y *SIException) error {
		if y.Status == SIExcClosed {
			return fmt.Errorf("%w: %s is closed: the test passed", ErrState, y.ID)
		}
		y.Status = SIExcAccepted
		y.Accepted = &SIAcceptance{By: actor, Role: RoleCompliance, Reason: clip(reason, 1000), Until: until, At: at}
		y.History = append(y.History, SIEvent{At: at, By: actor, Role: RoleCompliance, What: "risk accepted until " + until, Note: clip(reason, 1000)})
		return nil
	})
	if err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "selfinspect.risk_accepted", id, map[string]any{"test": x.Test, "reason": reason, "until": until, "role": RoleCompliance, "found": x.Found})
	o.siSyncBoard(ctx, tenant)
	return x, nil
}

// siComplianceOnly refuses anyone but a compliance officer (an Admin
// included).
func (o *Ops) siComplianceOnly(ctx context.Context, tenant, actor, what string) error {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return err
	}
	if role != RoleCompliance {
		return fmt.Errorf("%w: %s is the compliance officer's own decision (you are %s)", ErrForbidden, what, role)
	}
	return nil
}

// ---- muting a test ----

func (o *Ops) siMutes(ctx context.Context, tenant string) ([]SIMute, error) {
	ms, err := docs.List[SIMute](ctx, o.Docs, tenant, cSIMute, "")
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].At > ms[j].At })
	return ms, err
}

// MuteSelfInspectionTest stops a test counting until an end date, with the
// reason (it still runs, and its result is kept as muted).
func (o *Ops) MuteSelfInspectionTest(ctx context.Context, tenant, actor, test, reason, until string) (SIMute, error) {
	if err := o.require(ctx, tenant, actor, "Muting a self-inspection test", RoleCompliance); err != nil {
		return SIMute{}, err
	}
	if err := o.siComplianceOnly(ctx, tenant, actor, "Muting a test"); err != nil {
		return SIMute{}, err
	}
	t, ok := siTest(test)
	if !ok {
		return SIMute{}, fmt.Errorf("%w: no test %q", ErrNotFound, test)
	}
	reason = strings.TrimSpace(reason)
	if len(reason) < 10 {
		return SIMute{}, invalid("give the reason the test is muted, so an inspector can read it")
	}
	today := o.businessDate(ctx, tenant)
	if !validDate(until) || until < today || until > addDays(today, 180) {
		return SIMute{}, invalid("give an end date from today and within 180 days")
	}
	ms, err := o.siMutes(ctx, tenant)
	if err != nil {
		return SIMute{}, err
	}
	for _, m := range ms {
		if m.Test == test && m.activeOn(today) {
			return m, fmt.Errorf("%w: %s is already muted until %s; end that mute first", ErrState, t.Title, fmtDay(m.Until))
		}
	}
	n, err := o.next(ctx, tenant, "selfinspect_mute:"+test, actor)
	if err != nil {
		return SIMute{}, err
	}
	m := SIMute{ID: fmt.Sprintf("%s|%d", test, n), Test: test, Reason: clip(reason, 1000), From: today, Until: until, By: actor, At: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSIMute, m.ID, m, actor); err != nil {
		return m, err
	}
	o.log(ctx, tenant, actor, "selfinspect.muted", test, map[string]any{"reason": m.Reason, "until": until, "mute": m.ID})
	return m, nil
}

// UnmuteSelfInspectionTest ends a test's mute before its end date.
func (o *Ops) UnmuteSelfInspectionTest(ctx context.Context, tenant, actor, test, reason string) (SIMute, error) {
	if err := o.require(ctx, tenant, actor, "Muting a self-inspection test", RoleCompliance); err != nil {
		return SIMute{}, err
	}
	if err := o.siComplianceOnly(ctx, tenant, actor, "Ending a mute"); err != nil {
		return SIMute{}, err
	}
	today := o.businessDate(ctx, tenant)
	ms, err := o.siMutes(ctx, tenant)
	if err != nil {
		return SIMute{}, err
	}
	for _, m := range ms {
		if m.Test != test || !m.activeOn(today) {
			continue
		}
		m, err = docs.Update(ctx, o.Docs, tenant, cSIMute, m.ID, actor, func(x *SIMute) error {
			x.EndedAt, x.EndedBy, x.EndReason = o.now().Format(time.RFC3339), actor, clip(strings.TrimSpace(reason), 500)
			return nil
		})
		if err == nil {
			o.log(ctx, tenant, actor, "selfinspect.unmuted", test, map[string]any{"mute": m.ID, "reason": reason})
		}
		return m, err
	}
	return SIMute{}, fmt.Errorf("%w: the test is not muted", ErrState)
}

// ---- the screen ----

// SITrendPoint is one day's score.
type SITrendPoint struct {
	Date   string  `json:"date"`
	Score  float64 `json:"score"`
	Passed int     `json:"passed"`
	Failed int     `json:"failed"`
	Run    string  `json:"run"`
}

// SIHist is a test's result in one run.
type SIHist struct {
	Run     string `json:"run"`
	Date    string `json:"date"`
	Status  string `json:"status"`
	Found   int    `json:"found"`
	Summary string `json:"summary,omitempty"`
}

// SITestView is a test with its latest result.
type SITestView struct {
	SITest
	Last      *SIResult `json:"last,omitempty"`
	LastRun   string    `json:"last_run,omitempty"`
	History   []SIHist  `json:"history"` // the latest runs, newest last
	Mute      *SIMute   `json:"mute,omitempty"`
	Exception string    `json:"exception,omitempty"` // the open exception
	ExcStatus string    `json:"exception_status,omitempty"`
}

// SIRunInfo is a run without its results.
type SIRunInfo struct {
	ID      string  `json:"id"`
	Date    string  `json:"date"`
	At      string  `json:"at"`
	By      string  `json:"by"`
	Trigger string  `json:"trigger"`
	Score   float64 `json:"score"`
	Passed  int     `json:"passed"`
	Failed  int     `json:"failed"`
	Skipped int     `json:"skipped"`
	Muted   int     `json:"muted"`
	Errors  int     `json:"errors"`
}

// SIView is the Self-inspection screen.
type SIView struct {
	Date       string          `json:"date"`
	Last       *SIRun          `json:"last,omitempty"`
	Tests      []SITestView    `json:"tests"`
	Areas      []string        `json:"areas"`
	Trend      []SITrendPoint  `json:"trend"`
	Runs       []SIRunInfo     `json:"runs"`
	Exceptions []SIException   `json:"exceptions"` // open, and closed in the last 30 days
	Settings   SISettings      `json:"settings"`
	Board      string          `json:"board,omitempty"`
	Real       int             `json:"real"`
	Waiting    int             `json:"waiting"`
	Can        map[string]bool `json:"can"`
}

func (o *Ops) siRuns(ctx context.Context, tenant string) ([]SIRun, error) {
	rs, err := docs.List[SIRun](ctx, o.Docs, tenant, cSIRun, "")
	// By the business date inspected, then in the order they ran: a run
	// now for an earlier date sits with that date.
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Date != rs[j].Date {
			return rs[i].Date < rs[j].Date
		}
		return rs[i].ID < rs[j].ID
	})
	return rs, err
}

func siInfo(r SIRun) SIRunInfo {
	return SIRunInfo{ID: r.ID, Date: r.Date, At: r.At, By: r.By, Trigger: r.Trigger, Score: r.Score, Passed: r.Passed, Failed: r.Failed, Skipped: r.Skipped, Muted: r.Muted, Errors: r.Errors}
}

// siTrend is the score of each business date's latest run within days of
// date.
func siTrend(rs []SIRun, date string, days int) []SITrendPoint {
	from := addDays(date, -days)
	by := map[string]SIRun{}
	for _, r := range rs { // oldest first: a later run of the date replaces an earlier
		if r.Date >= from && r.Date <= date {
			by[r.Date] = r
		}
	}
	out := []SITrendPoint{}
	for _, d := range sortedKeys(by) {
		r := by[d]
		out = append(out, SITrendPoint{Date: d, Score: r.Score, Passed: r.Passed, Failed: r.Failed, Run: r.ID})
	}
	return out
}

// siHeadOffice refuses branch staff and authorised persons.
func (o *Ops) siHeadOffice(ctx context.Context, tenant, actor string) (string, error) {
	if err := o.require(ctx, tenant, actor, "Seeing the self-inspection", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return "", err
	}
	return o.RoleOf(ctx, tenant, actor)
}

// SelfInspection is the screen: the score, the trend, every test with its
// latest result and history, the open exceptions and the schedule.
func (o *Ops) SelfInspection(ctx context.Context, tenant, actor string) (SIView, error) {
	role, err := o.siHeadOffice(ctx, tenant, actor)
	if err != nil {
		return SIView{}, err
	}
	date := o.businessDate(ctx, tenant)
	v := SIView{Date: date, Tests: []SITestView{}, Areas: []string{}, Trend: []SITrendPoint{}, Runs: []SIRunInfo{}, Exceptions: []SIException{}, Settings: o.siSettings(ctx, tenant),
		Can: map[string]bool{"run": role == RoleCompliance || role == RoleAdmin, "schedule": role == RoleCompliance || role == RoleAdmin, "accept": role == RoleCompliance, "mute": role == RoleCompliance, "set_up_board": role == RoleAdmin}}
	rs, err := o.siRuns(ctx, tenant)
	if err != nil {
		return v, err
	}
	if len(rs) > 0 {
		last := rs[len(rs)-1]
		v.Last = &last
		v.Trend = siTrend(rs, last.Date, siTrendDays)
	}
	for i := len(rs) - 1; i >= 0 && len(v.Runs) < 60; i-- {
		v.Runs = append(v.Runs, siInfo(rs[i]))
	}
	ms, _ := o.siMutes(ctx, tenant)
	xs, _ := docs.List[SIException](ctx, o.Docs, tenant, cSIExc, "")
	openBy := map[string]SIException{}
	for _, x := range xs {
		if x.Status != SIExcClosed {
			openBy[x.Test] = x
		}
		if x.Status != SIExcClosed || bsTime(x.Closed).After(o.now().Add(-30*24*time.Hour)) {
			v.Exceptions = append(v.Exceptions, siDescribe(x))
		}
	}
	sort.SliceStable(v.Exceptions, func(i, j int) bool {
		a, b := v.Exceptions[i], v.Exceptions[j]
		if (a.Status == SIExcClosed) != (b.Status == SIExcClosed) {
			return b.Status == SIExcClosed
		}
		if siWeight[a.Severity] != siWeight[b.Severity] {
			return siWeight[a.Severity] > siWeight[b.Severity]
		}
		return a.Opened < b.Opened
	})
	seen := map[string]bool{}
	for _, t := range siTests {
		tv := SITestView{SITest: t, History: []SIHist{}}
		if t.Real {
			v.Real++
		} else {
			v.Waiting++
		}
		if !seen[t.Area] {
			seen[t.Area] = true
			v.Areas = append(v.Areas, t.Area)
		}
		for i := len(rs) - 1; i >= 0 && len(tv.History) < 30; i-- {
			for _, r := range rs[i].Results {
				if r.Test != t.ID {
					continue
				}
				if tv.Last == nil {
					rr := r
					tv.Last, tv.LastRun = &rr, rs[i].ID
				}
				tv.History = append(tv.History, SIHist{Run: rs[i].ID, Date: rs[i].Date, Status: r.Status, Found: r.Found})
			}
		}
		for l, r := 0, len(tv.History)-1; l < r; l, r = l+1, r-1 {
			tv.History[l], tv.History[r] = tv.History[r], tv.History[l]
		}
		for _, m := range ms {
			if m.Test == t.ID && m.activeOn(date) {
				mm := m
				tv.Mute = &mm
				break
			}
		}
		if x, ok := openBy[t.ID]; ok {
			tv.Exception, tv.ExcStatus = x.ID, x.Status
		}
		v.Tests = append(v.Tests, tv)
	}
	if bs, err := docs.List[Board](ctx, o.Docs, tenant, cBoard, ""); err == nil {
		if b, ok := bsBoardFor(bs, siSource); ok {
			v.Board = b.ID
		}
	}
	return v, nil
}

// SITestDetail is one test's history.
type SITestDetail struct {
	Test       SITestView    `json:"test"`
	Results    []SIHist      `json:"results"` // the last 90 days, newest first
	Exceptions []SIException `json:"exceptions"`
	Mutes      []SIMute      `json:"mutes"`
}

// SelfInspectionTest is one test with its results of the last 90 days, its
// exceptions and its mutes.
func (o *Ops) SelfInspectionTest(ctx context.Context, tenant, actor, id string) (SITestDetail, error) {
	if _, err := o.siHeadOffice(ctx, tenant, actor); err != nil {
		return SITestDetail{}, err
	}
	t, ok := siTest(id)
	if !ok {
		return SITestDetail{}, fmt.Errorf("%w: no test %q", ErrNotFound, id)
	}
	d := SITestDetail{Test: SITestView{SITest: t, History: []SIHist{}}, Results: []SIHist{}, Exceptions: []SIException{}, Mutes: []SIMute{}}
	rs, err := o.siRuns(ctx, tenant)
	if err != nil {
		return d, err
	}
	from := addDays(o.businessDate(ctx, tenant), -siTrendDays)
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].Date < from {
			continue
		}
		for _, r := range rs[i].Results {
			if r.Test == id {
				if d.Test.Last == nil {
					rr := r
					d.Test.Last, d.Test.LastRun = &rr, rs[i].ID
				}
				d.Results = append(d.Results, SIHist{Run: rs[i].ID, Date: rs[i].Date, Status: r.Status, Found: r.Found, Summary: r.Summary})
			}
		}
	}
	xs, _ := docs.List[SIException](ctx, o.Docs, tenant, cSIExc, "")
	for _, x := range xs {
		if x.Test == id {
			d.Exceptions = append(d.Exceptions, siDescribe(x))
		}
	}
	sort.SliceStable(d.Exceptions, func(i, j int) bool { return d.Exceptions[i].Opened > d.Exceptions[j].Opened })
	ms, _ := o.siMutes(ctx, tenant)
	for _, m := range ms {
		if m.Test == id {
			d.Mutes = append(d.Mutes, m)
		}
	}
	return d, nil
}

// SelfInspectionRun is a past run as it was.
func (o *Ops) SelfInspectionRun(ctx context.Context, tenant, actor, id string) (SIRun, error) {
	if _, err := o.siHeadOffice(ctx, tenant, actor); err != nil {
		return SIRun{}, err
	}
	r, _, err := docs.Get[SIRun](ctx, o.Docs, tenant, cSIRun, id)
	return r, err
}

// siDescribe fills in an exception's status name and how it was handled.
func siDescribe(x SIException) SIException {
	x.StatusName = SIExcNames[x.Status]
	last := func(what string) SIEvent {
		for i := len(x.History) - 1; i >= 0; i-- {
			if strings.HasPrefix(x.History[i].What, what) {
				return x.History[i]
			}
		}
		return SIEvent{}
	}
	switch x.Status {
	case SIExcClosed:
		x.Handled = fmt.Sprintf("Closed by itself on %s: the test passed on run %s", fmtDay(dayOf(x.Closed)), x.ClosedRun)
		if f := last("fixed"); f.At != "" {
			x.Handled += fmt.Sprintf(", after %s (%s) marked it fixed on %s", f.By, f.Role, fmtDay(dayOf(f.At)))
		}
		if a := last("risk accepted"); a.At != "" && last("fixed").At == "" {
			x.Handled += ", while its risk had been accepted by " + a.By
		}
	case SIExcAccepted:
		if x.Accepted != nil {
			x.Handled = fmt.Sprintf("Risk accepted by %s (%s) on %s until %s: %s", x.Accepted.By, x.Accepted.Role, fmtDay(dayOf(x.Accepted.At)), fmtDay(x.Accepted.Until), x.Accepted.Reason)
		}
	case SIExcFixed:
		f := last("fixed")
		x.Handled = fmt.Sprintf("Marked fixed by %s (%s) on %s, waiting for the next run: %s", f.By, f.Role, fmtDay(dayOf(f.At)), f.Note)
	case SIExcFixing:
		f := last("being fixed")
		if f.At == "" {
			f = last("still failing")
		}
		x.Handled = fmt.Sprintf("Being fixed since %s (due %s)", fmtDay(dayOf(f.At)), fmtDay(x.Due))
	default:
		x.Handled = fmt.Sprintf("Open since %s, not yet picked up (due %s)", fmtDay(x.Opened), fmtDay(x.Due))
	}
	return x
}

// ---- for the inspection pack ----

// SelfInspectionReport is the self-inspection of a period, for the
// inspection pack: every run in the period with every test's result as it
// was, and how each failing test was handled. It carries client codes
// masked and no PAN, bank or demat numbers; the pack that includes it is
// for Compliance and Admin, and is logged by the pack.
//
// Stable for the inspection pack (F3): fields are only ever added.
type SelfInspectionReport struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Generated string `json:"generated"`
	// Tests is the library as it is now (each run's results carry the
	// title, severity, owner and source the test had then).
	Tests []SITest `json:"tests"`
	// Runs are every run whose business date is in the period, oldest
	// first.
	Runs []SIRun `json:"runs"`
	// Exceptions are every failing test open at any time in the period,
	// with its history and Handled, how it was handled, in words.
	Exceptions []SIException `json:"exceptions"`
	// Mutes are the mutes in force at any time in the period.
	Mutes  []SIMute       `json:"mutes"`
	Totals SIReportTotals `json:"totals"`
}

// SIReportTotals sums a period up.
type SIReportTotals struct {
	Runs         int     `json:"runs"`
	AverageScore float64 `json:"average_score"`
	FirstScore   float64 `json:"first_score"`
	LastScore    float64 `json:"last_score"`
	Exceptions   int     `json:"exceptions"`
	Open         int     `json:"open"`     // at the end of the period: new or being fixed
	Fixed        int     `json:"fixed"`    // marked fixed, waiting for a run
	Accepted     int     `json:"accepted"` // accepted risk
	Closed       int     `json:"closed"`
	Waiting      int     `json:"tests_waiting_for_data"`
}

// SelfInspectionSummary is the self-inspection of the business dates from
// from to to (YYYY-MM-DD, both included), for the inspection pack. It
// checks no role: the pack that calls it is for Compliance and Admin and
// logs itself.
func (o *Ops) SelfInspectionSummary(ctx context.Context, tenant, from, to string) (SelfInspectionReport, error) {
	if !validDate(from) || !validDate(to) || from > to {
		return SelfInspectionReport{}, invalid("give the period as two dates, the first on or before the second (YYYY-MM-DD)")
	}
	rep := SelfInspectionReport{From: from, To: to, Generated: o.now().Format(time.RFC3339), Tests: SelfInspectionTests(), Runs: []SIRun{}, Exceptions: []SIException{}, Mutes: []SIMute{}}
	for _, t := range rep.Tests {
		if !t.Real {
			rep.Totals.Waiting++
		}
	}
	rs, err := o.siRuns(ctx, tenant)
	if err != nil {
		return rep, err
	}
	sum := 0.0
	for _, r := range rs {
		if r.Date >= from && r.Date <= to {
			rep.Runs = append(rep.Runs, r)
			sum += r.Score
		}
	}
	if n := len(rep.Runs); n > 0 {
		rep.Totals.Runs = n
		rep.Totals.AverageScore = math.Round(sum*10/float64(n)) / 10
		rep.Totals.FirstScore, rep.Totals.LastScore = rep.Runs[0].Score, rep.Runs[n-1].Score
	}
	xs, err := docs.List[SIException](ctx, o.Docs, tenant, cSIExc, "")
	if err != nil {
		return rep, err
	}
	for _, x := range xs {
		if x.Opened > to || (x.Status == SIExcClosed && dayOf(x.Closed) < from) {
			continue
		}
		rep.Exceptions = append(rep.Exceptions, siDescribe(x))
		rep.Totals.Exceptions++
		switch x.Status {
		case SIExcClosed:
			rep.Totals.Closed++
		case SIExcAccepted:
			rep.Totals.Accepted++
		case SIExcFixed:
			rep.Totals.Fixed++
		default:
			rep.Totals.Open++
		}
	}
	sort.SliceStable(rep.Exceptions, func(i, j int) bool {
		if rep.Exceptions[i].Opened != rep.Exceptions[j].Opened {
			return rep.Exceptions[i].Opened < rep.Exceptions[j].Opened
		}
		return rep.Exceptions[i].ID < rep.Exceptions[j].ID
	})
	ms, err := o.siMutes(ctx, tenant)
	if err != nil {
		return rep, err
	}
	for _, m := range ms {
		end := m.Until
		if m.EndedAt != "" && dayOf(m.EndedAt) < end {
			end = dayOf(m.EndedAt)
		}
		if m.From <= to && end >= from {
			rep.Mutes = append(rep.Mutes, m)
		}
	}
	return rep, nil
}

// ---- the Ledgerline board ----

var (
	siSevLabels = []bsLabel{{"sev:high", "High", "red"}, {"sev:medium", "Medium", "orange"}, {"sev:low", "Low", "yellow"}}
)

func init() {
	bsSources = append(bsSources, &bsSource{ID: siSource, Name: "Self-inspection", Background: "black", Screen: "Compliance → Self-inspection", Kinds: []string{siSource},
		About:  "Each self-inspection test that failed, with what it found, how to fix it, who owns it and when it is due. A card closes by itself when the test passes on a later run; accepting a risk is the compliance officer's decision, with a reason.",
		Lists:  []bsList{{SIExcNew, "New", ""}, {SIExcFixing, "Being fixed", "yellow"}, {SIExcFixed, "Fixed, waiting for the next run", "sky"}, {SIExcAccepted, "Accepted risk", "purple"}, {SIExcClosed, "Closed", "green"}},
		Closed: []string{SIExcClosed}, Labels: siSevLabels,
		since: func(env *bsEnv) time.Time { return env.now.Add(-bsKeep) }, list: siBoardRecs, move: siBoardMove})
	bsRecordKinds[siSource] = siLinkRecord
}

func siCardTitle(x SIException) string {
	if x.Status == SIExcClosed {
		return x.Title + " · passed"
	}
	return fmt.Sprintf("%s · %d found", x.Title, x.Found)
}

func siBoardRecs(ctx context.Context, o *Ops, tenant string, env *bsEnv) ([]bsRec, error) {
	xs, err := docs.List[SIException](ctx, o.Docs, tenant, cSIExc, "")
	if err != nil {
		return nil, err
	}
	out := make([]bsRec, 0, len(xs))
	for _, x := range xs {
		r := bsRec{Key: siSource + "|" + x.ID, Status: x.Status, Closed: x.Status == SIExcClosed, Title: siCardTitle(x), Due: bsDue(x.Due),
			Order: fmt.Sprintf("%d|%s|%s", 9-siWeight[x.Severity], x.Opened, x.ID)}
		if r.Closed {
			r.ClosedAt = bsTime(x.Closed)
		}
		if x.Status == SIExcAccepted && x.Accepted != nil {
			r.Due = bsDue(x.Accepted.Until) // reviewed again then
		}
		for _, l := range siSevLabels {
			if l.Key == "sev:"+x.Severity {
				r.Labels = append(r.Labels, l)
			}
		}
		r.Labels = append(r.Labels, bsAreaLabel(x.Area), bsRoleLabel(x.Owner))
		r.Link = CardLink{Kind: siSource, ID: x.ID, Label: clip(r.Title, 120), View: "selfinspect", Status: SIExcNames[x.Status]}
		out = append(out, r)
	}
	return out, nil
}

// siBoardMove: moving a card to Being fixed or Fixed works the exception
// as the person; the rest is refused with where it is done.
func siBoardMove(ctx context.Context, o *Ops, tenant, actor string, c Card, to BoardList) (string, error) {
	switch to.Source {
	case SIExcFixing, SIExcFixed:
		x, err := o.siSetStatus(ctx, tenant, actor, c.Link.ID, to.Source, "Moved to "+to.Name+" on the board")
		return SIExcNames[x.Status], err
	case SIExcAccepted:
		return "", invalid("accepting a risk needs the compliance officer's reason and a review date: use Accept risk on the card, or Compliance → Self-inspection")
	case SIExcClosed:
		return "", invalid("a self-inspection card closes by itself when its test passes on a later run")
	}
	return "", invalid("a card is New only when a run finds its test failing; move it to Being fixed or Fixed")
}

// siLinkRecord is an exception as the card window shows it.
func siLinkRecord(ctx context.Context, o *Ops, tenant, actor, id string) (LinkHit, error) {
	if _, err := o.siHeadOffice(ctx, tenant, actor); err != nil {
		return LinkHit{}, errBSHidden
	}
	x, _, err := docs.Get[SIException](ctx, o.Docs, tenant, cSIExc, id)
	if err != nil {
		return LinkHit{}, errBSHidden
	}
	x = siDescribe(x)
	due := "due " + fmtDay(x.Due)
	if x.Status == SIExcAccepted && x.Accepted != nil {
		due = "review by " + fmtDay(x.Accepted.Until)
	}
	if x.Status == SIExcClosed {
		due = ""
	}
	h := LinkHit{CardLink: CardLink{Kind: siSource, ID: x.ID, Label: clip(siCardTitle(x), 120), View: "selfinspect", Status: x.StatusName},
		Sub: bsJoin(siSevName[x.Severity]+" severity", x.Area, "owner "+x.Owner, due)}
	h.Lines = []string{x.Summary, fmt.Sprintf("Found %d of %d checked (run %s, %s)", x.Found, x.Checked, x.LastRun, fmtDay(x.LastDate))}
	for _, e := range x.Examples {
		h.Lines = append(h.Lines, "Example: "+e)
	}
	h.Lines = append(h.Lines, "How to fix: "+x.Fix, "Rule: "+x.Source, x.Handled)
	return h, nil
}

// siSyncBoard brings the Self-inspection board up to date now, when it is
// set up.
func (o *Ops) siSyncBoard(ctx context.Context, tenant string) {
	bs, err := docs.List[Board](ctx, o.Docs, tenant, cBoard, "")
	if err != nil {
		return
	}
	if b, ok := bsBoardFor(bs, siSource); ok && !b.Closed {
		_, _ = o.bsSync(ctx, tenant, b, o.now(), 0)
	}
}
