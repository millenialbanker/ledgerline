package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- reminders for missed steps ----
//
// A step (steps.go) not done by its due time gets a Ledgy notice and a work
// item for its owner. Still not done after its escalation delay (30
// minutes unless the firm set another), it goes to the Admin, or to the
// head of the desk the firm named. A step is reminded at most once a day
// and escalated at most once a day, and only on the days it runs (the
// exchange holiday calendar and the firm's own holidays; Saturdays only for
// the steps that run on Saturdays).
//
// What is not done carries over: it stays on the pending list ("Pending
// from earlier days" on the dashboard and in the Day runbook) until it is
// done, marked done by hand outside Ledgerline with a note, or skipped with
// a reason. Each of those is in the audit trail. A step that is done in
// Ledgerline (its automation ran, its file arrived, its periodic work is
// done) closes by itself on the next run, with its work items.
//
// Each reminder and escalation is also an internal event (Ops.StepEvents),
// so that a push to the staff mobile app can hook in later.

const (
	cStepItem = "stepitem" // step|key: a step's state for a day (or a period)
	wStep     = "step"     // work process of a step reminder
	wStepEsc  = "step-esc" // work process of an escalation
)

// Step states, as stored.
const (
	StepOpen    = "Open"
	StepDone    = "Done"
	StepByHand  = "Done by hand"
	StepSkipped = "Skipped"
)

// StepItem is one step on one day (or, for periodic work, one period).
type StepItem struct {
	ID        string `json:"id"` // step|key
	Step      string `json:"step"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Area      string `json:"area"`
	View      string `json:"view,omitempty"`
	Key       string `json:"key"`  // the day, the checklist date, or the period
	Date      string `json:"date"` // the day it is due
	Due       string `json:"due"`  // YYYY-MM-DD HH:MM IST
	Owner     string `json:"owner"`
	OwnerName string `json:"owner_name,omitempty"`
	Role      string `json:"role"`
	Status    string `json:"status"`          // Open, Done, Done by hand, Skipped
	State     string `json:"state,omitempty"` // shown: upcoming, overdue, reminded, escalated, done, by_hand, skipped
	DoneNote  string `json:"done_note,omitempty"`

	RemindedAt  string `json:"reminded_at,omitempty"`
	EscalatedAt string `json:"escalated_at,omitempty"`
	EscalatedTo string `json:"escalated_to,omitempty"`
	Work        string `json:"work,omitempty"`
	EscWork     string `json:"esc_work,omitempty"`

	Note   string `json:"note,omitempty"`   // done by hand: how
	Reason string `json:"reason,omitempty"` // skipped: why
	By     string `json:"by,omitempty"`
	At     string `json:"at,omitempty"`

	HasExceptions bool            `json:"has_exceptions,omitempty"`
	Exceptions    *StepExceptions `json:"exceptions,omitempty"`
	Confirmed     *StepConfirmed  `json:"confirmed,omitempty"`
}

// StepConfirmed records a run confirmed with its exemptions.
type StepConfirmed struct {
	By          string       `json:"by"`
	At          string       `json:"at"`
	Fingerprint string       `json:"fingerprint"`
	Summary     string       `json:"summary"`
	Clients     []string     `json:"clients"`
	Amount      money.Amount `json:"amount"`
	Result      string       `json:"result"`
	Note        string       `json:"note,omitempty"`
}

// StepEvent is a reminder or an escalation, for other channels (the staff
// mobile app's push, later).
type StepEvent struct {
	Kind   string   `json:"kind"` // reminder, escalation
	Tenant string   `json:"tenant"`
	Item   string   `json:"item"`
	Step   string   `json:"step"`
	Name   string   `json:"name"`
	Date   string   `json:"date"`
	Due    string   `json:"due"`
	Owner  string   `json:"owner"`
	Users  []string `json:"users"` // who was told
	Text   string   `json:"text"`
	View   string   `json:"view"`
	At     string   `json:"at"`
}

// stepEvent records the event in the audit trail and hands it to the hook.
func (o *Ops) stepEvent(ctx context.Context, e StepEvent) {
	o.log(ctx, e.Tenant, SystemActor, "step."+e.Kind, e.Item, map[string]any{"step": e.Step, "date": e.Date, "due": e.Due, "owner": e.Owner, "to": strings.Join(e.Users, ", ")})
	if o.StepEvents != nil {
		o.StepEvents(ctx, e)
	}
}

func stepItemID(step, key string) string { return step + "|" + key }

// stepInst is one occurrence of a step.
type stepInst struct {
	def  StepDef
	key  string // the day, the checklist date, or the period
	date string // the day it is due
	due  string // YYYY-MM-DD HH:MM
}

func (in stepInst) id() string { return stepItemID(in.def.ID, in.key) }

// stepEnv holds what one run needs, read once.
type stepEnv struct {
	o      *Ops
	ctx    context.Context
	tenant string
	set    Settings
	ss     StepSettings
	now    time.Time
	today  string
	nowAt  string // YYYY-MM-DD HH:MM IST
	defs   map[string]StepDef
	order  []StepDef
	users  map[string]User
	lists  map[string]FileChecklist
	pstat  map[string]PeriodicStatus
	eods   map[string]EODState
	runs   []AutoRun
	work   []WorkItem
	loaded bool
}

func (o *Ops) newStepEnv(ctx context.Context, tenant string, now time.Time) (*stepEnv, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return nil, err
	}
	now = now.In(ist)
	e := &stepEnv{o: o, ctx: ctx, tenant: tenant, set: set, ss: o.stepSettings(ctx, tenant), now: now, today: now.Format("2006-01-02"), nowAt: now.Format("2006-01-02 15:04"),
		defs: map[string]StepDef{}, users: map[string]User{}, lists: map[string]FileChecklist{}, pstat: map[string]PeriodicStatus{}, eods: map[string]EODState{}}
	// The file steps are those of today's checklist and the previous
	// business day's (whose next-day files are due this morning).
	for _, d := range o.stepDefsWith(ctx, tenant, e.checklist(e.today), e.ss) {
		e.add(d)
	}
	for _, d := range stepFileDefs(e.checklist(set.PrevBusinessDay(e.today)), o.FileAlertSettings(ctx, tenant).GraceMinutes) {
		e.add(d)
	}
	us, _ := o.Users(ctx, tenant)
	for _, u := range us {
		if u.Active {
			e.users[u.ID] = u
		}
	}
	return e, nil
}

func (e *stepEnv) add(d StepDef) {
	if _, ok := e.defs[d.ID]; !ok {
		e.defs[d.ID] = d
		e.order = append(e.order, d)
	}
}

func (e *stepEnv) checklist(date string) FileChecklist {
	if cl, ok := e.lists[date]; ok {
		return cl
	}
	cl, _ := e.o.DailyFileChecklist(e.ctx, e.tenant, date)
	e.lists[date] = cl
	return cl
}

func (e *stepEnv) periodic(id, date string) PeriodicStatus {
	k := id + "|" + date
	if s, ok := e.pstat[k]; ok {
		return s
	}
	t, _ := periodicTask(id)
	s := e.o.periodicStatus(e.ctx, e.tenant, t, date)
	e.pstat[k] = s
	return s
}

func (e *stepEnv) eod(date string) EODState {
	if s, ok := e.eods[date]; ok {
		return s
	}
	s, _ := e.o.EOD(e.ctx, e.tenant, date)
	e.eods[date] = s
	return s
}

func (e *stepEnv) load() {
	if !e.loaded {
		e.runs, _ = docs.List[AutoRun](e.ctx, e.o.Docs, e.tenant, cAutoRun, "AR-")
		e.work, _ = docs.List[WorkItem](e.ctx, e.o.Docs, e.tenant, cWorkItem, "")
		e.loaded = true
	}
}

// dueDay moves a due date that falls on a day the step does not run back
// to the last day it does, so a task due "by the 5th" on a Sunday is
// reminded on the Friday before.
func (e *stepEnv) dueDay(d StepDef, due string) string {
	for i := 0; i < 10 && !d.worksOn(e.set, due); i++ {
		due = addDays(due, -1)
	}
	return due
}

// autoApplies says whether an automation is a step on a day.
func (e *stepEnv) autoApplies(d StepDef, day string) bool {
	if d.ref == "interest_daily" {
		return e.set.dailyInterest() && day >= e.set.InterestDailyFrom
	}
	if stepAutoAlways[d.ref] {
		return true
	}
	_, mode, _ := e.o.policy(e.ctx, e.tenant, d.ref)
	return mode != ModeOff
}

// instancesOf are a step's occurrences due on a day.
func (e *stepEnv) instancesOf(d StepDef, day string) []stepInst {
	due, _, _, _ := e.ss.effective(d)
	switch d.Kind {
	case StepAutopilot:
		if d.worksOn(e.set, day) && e.autoApplies(d, day) {
			return []stepInst{{def: d, key: day, date: day, due: day + " " + due}}
		}
	case StepPeriodic:
		s := e.periodic(d.ref, day)
		if s.Period == "" || s.DueOn == "" || s.Status == "Nothing to do" {
			return nil
		}
		if dd := e.dueDay(d, s.DueOn); dd == day {
			return []stepInst{{def: d, key: s.Period, date: dd, due: dd + " " + due}}
		}
	case StepFile:
		var out []stepInst
		for _, cd := range []string{e.set.PrevBusinessDay(day), day} {
			if !e.set.IsBusinessDay(cd) {
				continue
			}
			for _, it := range e.checklist(cd).Items {
				if StepFile+":"+it.ID == d.ID && !it.Intraday && len(it.Due) >= 10 && it.Due[:10] == day {
					out = append(out, stepInst{def: d, key: cd, date: day, due: day + " " + due})
				}
			}
		}
		return out
	default:
		if d.worksOn(e.set, day) {
			return []stepInst{{def: d, key: day, date: day, due: day + " " + due}}
		}
	}
	return nil
}

func (e *stepEnv) instancesOn(day string) []stepInst {
	var out []stepInst
	for _, d := range e.order {
		out = append(out, e.instancesOf(d, day)...)
	}
	return out
}

// overduePeriodic is the periodic work overdue on a day whose due day is
// past (within the last month; older work is on Periodic work): it is
// pending even if no reminder was ever sent for it.
func (e *stepEnv) overduePeriodic(day string) []stepInst {
	var out []stepInst
	for _, d := range e.order {
		if d.Kind != StepPeriodic {
			continue
		}
		s := e.periodic(d.ref, day)
		if s.Status != "Overdue" && s.Status != "Failed" && s.Status != "Waiting for approval" && s.Status != "Waiting for a file" {
			continue
		}
		due, _, _, _ := e.ss.effective(d)
		if dd := e.dueDay(d, s.DueOn); s.DueOn != "" && dd < day && dd >= addDays(day, -31) {
			out = append(out, stepInst{def: d, key: s.Period, date: dd, due: dd + " " + due})
		}
	}
	return out
}

// isDone says whether a step's work is done in Ledgerline, and how.
func (e *stepEnv) isDone(in stepInst) (bool, string) {
	d := in.def
	switch d.Kind {
	case StepAutopilot:
		return e.autoDone(d.ref, in.key)
	case StepPeriodic:
		t, ok := periodicTask(d.ref)
		if !ok {
			return true, "No longer a periodic task"
		}
		if a := e.o.periodicApplicability(e.ctx, e.tenant, t.ID); a != nil && a.Verdict == ApplyNo {
			return true, "Doesn't apply: " + a.Reason
		}
		if done, note := e.o.periodicDone(e.ctx, e.tenant, t, in.key, e.today); done {
			return true, firstOf(note, "Done")
		}
	case StepFile:
		for _, it := range e.checklist(in.key).Items {
			if StepFile+":"+it.ID == d.ID && (it.Status == "Received" || it.Status == "Written") {
				return true, it.Status + ": " + strings.Join(it.Files, ", ")
			}
		}
	case StepManual:
		switch d.ID {
		case "manual:reports_upload":
			rs, err := e.o.Reports(e.ctx, e.tenant, in.key)
			if err != nil {
				return false, ""
			}
			for _, r := range rs {
				if r.Freq == "Daily" && r.Status != "Uploaded" {
					return false, ""
				}
			}
			return true, "Every daily report uploaded"
		case "manual:payout_upload":
			e.load()
			for _, w := range e.work {
				if w.Process == "payout_batch" && w.Status == "Open" && w.Date == in.key {
					return false, ""
				}
			}
			return true, "No payout batch waiting for its upload"
		}
	}
	return false, ""
}

// stepEODCheck is the equity close's check that shows an automation's
// work for the day is done, however it was done.
var stepEODCheck = map[string]string{"recon": "Every reconciliation break resolved", "billing": "Bills posted", "notes": "Contract notes sent"}

// autoDone: the automation's work for the day is done.
func (e *stepEnv) autoDone(id, day string) (bool, string) {
	switch id {
	case "interest_daily":
		if m := e.o.dailyInterestUpto(e.ctx, e.tenant); m.Upto >= day {
			return true, "Posted up to " + fmtDay(m.Upto) + " by " + m.By
		}
		return false, ""
	case "recon", "billing", "settlement", "notes", "day_close":
		// The day's work, whether Autopilot or a person did it: the equity
		// close's checks (closeparts.go).
		for _, p := range e.eod(day).Parts {
			if p.ID != PartEquity {
				continue
			}
			if p.Closed {
				return true, strings.TrimSpace("Equity day closed " + p.ClosedAt)
			}
			if id == "day_close" {
				break // a day with nothing to work is still closed
			}
			if active, err := e.o.partActive(e.ctx, e.tenant, day, PartEquity); err == nil && !active {
				return true, "No trades or positions to work"
			}
			for _, c := range p.Checks {
				if c.Name == stepEODCheck[id] && c.OK {
					return true, c.Name
				}
			}
		}
	}
	run := day
	if id == "commodity_close" {
		run = e.set.PrevTradingDay(PartCommodity, day)
	}
	e.load()
	var last *AutoRun
	for i := range e.runs {
		r := &e.runs[i]
		if r.Policy == id && r.Date == run && (last == nil || r.ID > last.ID) {
			last = r
		}
	}
	if last == nil || last.Error != "" {
		return false, ""
	}
	for _, w := range e.work {
		if w.Process == id && w.Status == "Open" && w.Date == run {
			return false, ""
		}
	}
	at := last.At
	if t, err := time.Parse(time.RFC3339, last.At); err == nil {
		at = t.In(ist).Format("15:04")
	}
	return true, "Autopilot ran " + at
}

// resolve turns an owner (a user ID or a role) into the role that acts,
// the users to tell and a name to show. A role nobody holds goes to the
// Admin.
func (e *stepEnv) resolve(who string) (role string, users []string, name string) {
	if u, ok := e.users[who]; ok {
		return u.Role, []string{u.ID}, u.Name
	}
	for id, u := range e.users {
		if u.Role == who {
			users = append(users, id)
		}
	}
	if len(users) == 0 {
		for id, u := range e.users {
			if u.Role == RoleAdmin {
				users = append(users, id)
			}
		}
	}
	sort.Strings(users)
	return who, users, who
}

func (e *stepEnv) newItem(in stepInst) StepItem {
	_, owner, _, _ := e.ss.effective(in.def)
	role, _, name := e.resolve(owner)
	return StepItem{ID: in.id(), Step: in.def.ID, Kind: in.def.Kind, Name: in.def.Name, Area: in.def.Area, View: in.def.View, Key: in.key, Date: in.date, Due: in.due,
		Owner: owner, OwnerName: name, Role: role, Status: StepOpen, HasExceptions: in.def.Exceptions}
}

func (e *stepEnv) exceptions(in stepInst) *StepExceptions {
	fn := stepExceptionFns[in.def.ID]
	if fn == nil {
		return nil
	}
	ex, err := fn(e.o, e.ctx, e.tenant, in.key)
	if err != nil {
		return nil
	}
	return ex
}

// inst rebuilds a stored item's occurrence.
func (e *stepEnv) inst(it StepItem) (stepInst, bool) {
	d, ok := e.defs[it.Step]
	if !ok {
		return stepInst{}, false
	}
	return stepInst{def: d, key: it.Key, date: it.Date, due: it.Due}, true
}

// findInst finds a step's occurrence by its key, within ten days either
// side of today.
func (e *stepEnv) findInst(d StepDef, key string) (stepInst, bool) {
	var days []string
	switch d.Kind {
	case StepAutopilot, StepManual:
		days = []string{key} // keyed by the day itself
	case StepFile:
		days = []string{key, e.set.NextBusinessDay(key)} // the checklist date, or the next morning
	default:
		for i := 0; i <= 10; i++ {
			days = append(days, addDays(e.today, -i), addDays(e.today, i))
		}
	}
	for _, day := range days {
		if !validDate(day) {
			continue
		}
		for _, in := range e.instancesOf(d, day) {
			if in.key == key {
				return in, true
			}
		}
	}
	return stepInst{}, false
}

func addMinutes(at string, n int) string {
	t, err := time.ParseInLocation("2006-01-02 15:04", at, ist)
	if err != nil {
		return at
	}
	return t.Add(time.Duration(n) * time.Minute).Format("2006-01-02 15:04")
}

func stepDueLabel(due, today string) string {
	if len(due) < 16 {
		return due
	}
	if due[:10] == today {
		return due[11:]
	}
	return due[11:] + " on " + fmtDay(due[:10])
}

// stepWork raises a step's work item, without Ledgy's general "new work"
// notice (the reminder says more). An item a person closed is not reopened.
// existed reports whether the item was there already.
func (o *Ops) stepWork(ctx context.Context, tenant, process, key, title, detail, role, view, date, due, owner string) (string, bool) {
	k := process + ":" + key
	id := "W-" + strings.NewReplacer(":", "-", "|", "-", " ", "", "/", "-").Replace(k)
	if len(id) > 120 {
		id = id[:120]
	}
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, tenant, cWorkItem, id); err == nil && w.Status != "Cleared" {
		return id, true
	}
	w := WorkItem{ID: id, Key: k, Process: process, Title: title, Detail: detail, Role: role, View: view, Date: date, Due: due, Status: "Open", Created: o.now().Format(time.RFC3339)}
	if owner != "" && owner != role {
		w.Meta = map[string]string{"owner": owner}
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cWorkItem, id, w, SystemActor)
	return id, false
}

// remind sends a step's reminder to its owner.
func (e *stepEnv) remind(it *StepItem, in stepInst) {
	role, users, name := e.resolve(it.Owner)
	it.Role, it.OwnerName = role, name
	due := stepDueLabel(it.Due, e.today)
	text := fmt.Sprintf("Reminder: %s was due by %s and is not done yet.", strings.TrimSuffix(it.Name, "."), due)
	detail := firstOf(in.def.About, it.Name)
	if in.def.Exceptions {
		if ex := e.exceptions(in); ex != nil {
			it.Exceptions = ex
			text += "\n" + ex.Summary + " Check the list and confirm “" + ex.Confirm + "” on Steps & reminders."
			detail += " " + ex.Summary
		}
	}
	text += "\nMark it done by hand (with a note) or skip it with a reason on Steps & reminders if it is not done in Ledgerline."
	process, key, title := wStep, it.ID, "Not done by "+due+": "+it.Name
	if in.def.Kind == StepFile {
		// One item with the missing-file alerts (filealerts.go), which
		// closes when the file is loaded.
		process, key, title = wFileMissing, in.key+"|"+in.def.ref, "Missing: "+it.Name+" for "+fmtDay(in.key)
	}
	wid, existed := e.o.stepWork(e.ctx, e.tenant, process, key, title, detail, role, firstOf(in.def.View, "steps"), it.Date, it.Due, it.Owner)
	it.Work, it.RemindedAt = wid, e.nowAt
	if existed && in.def.Kind == StepFile {
		users = nil // the missing-file alert has told them
	}
	if len(users) > 0 {
		e.o.ledgyTell(e.ctx, e.tenant, users, text, "steps")
	}
	e.o.stepEvent(e.ctx, StepEvent{Kind: "reminder", Tenant: e.tenant, Item: it.ID, Step: it.Step, Name: it.Name, Date: it.Date, Due: it.Due, Owner: it.Owner, Users: users, Text: text, View: firstOf(it.View, "steps"), At: e.nowAt})
}

// escalate tells the Admin, or the head of the desk, that a step is still
// not done.
func (e *stepEnv) escalate(it *StepItem, mins int) {
	to := firstOf(e.ss.EscalateTo, RoleAdmin)
	role, users, _ := e.resolve(to)
	text := fmt.Sprintf("Not done: %s was due by %s (owner %s, reminded at %s) and is still not done %d minutes later.", strings.TrimSuffix(it.Name, "."), stepDueLabel(it.Due, e.today), firstOf(it.OwnerName, it.Owner), stepDueLabel(it.RemindedAt, e.today), mins)
	if it.Exceptions != nil {
		text += "\n" + it.Exceptions.Summary
	}
	wid, _ := e.o.stepWork(e.ctx, e.tenant, wStepEsc, it.ID, "Escalated: "+it.Name+" not done", text, role, "steps", it.Date, it.Due, to)
	it.EscalatedAt, it.EscalatedTo, it.EscWork = e.nowAt, to, wid
	if len(users) > 0 {
		e.o.ledgyTell(e.ctx, e.tenant, users, text, "steps")
	}
	e.o.stepEvent(e.ctx, StepEvent{Kind: "escalation", Tenant: e.tenant, Item: it.ID, Step: it.Step, Name: it.Name, Date: it.Date, Due: it.Due, Owner: to, Users: users, Text: text, View: "steps", At: e.nowAt})
}

// closeStepWork closes a step's work items once the step is settled.
func (o *Ops) closeStepWork(ctx context.Context, tenant, actor string, it StepItem, note string) {
	o.closeWork(ctx, tenant, actor, wStep, it.ID, note)
	o.closeWork(ctx, tenant, actor, wStepEsc, it.ID, note)
	if it.Kind == StepFile && it.Status != StepDone {
		o.closeWork(ctx, tenant, actor, wFileMissing, it.Key+"|"+strings.TrimPrefix(it.Step, StepFile+":"), note)
	}
}

// StepRun is what one reminder run did.
type StepRun struct {
	At        string   `json:"at"`
	Reminded  []string `json:"reminded"`
	Escalated []string `json:"escalated"`
	Closed    []string `json:"closed"`
	Skipped   string   `json:"skipped,omitempty"`
}

// stepReminderTick runs the reminders at most every five minutes.
func (o *Ops) stepReminderTick(ctx context.Context, tenant string, now time.Time) {
	last, _, _ := docs.GetOr(ctx, o.Docs, tenant, cStepSet, "tick", map[string]string{})
	if t, err := time.Parse(time.RFC3339, last["at"]); err == nil && !now.Before(t) && now.Sub(t) < 5*time.Minute {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cStepSet, "tick", map[string]string{"at": now.Format(time.RFC3339)}, SystemActor)
	_, _ = o.RunStepReminders(ctx, tenant, now)
}

// RunStepReminders closes the steps done since the last run, and reminds
// and escalates those past their time.
func (o *Ops) RunStepReminders(ctx context.Context, tenant string, now time.Time) (StepRun, error) {
	e, err := o.newStepEnv(ctx, tenant, now)
	if err != nil {
		return StepRun{}, err
	}
	run := StepRun{At: e.nowAt, Reminded: []string{}, Escalated: []string{}, Closed: []string{}}
	if e.ss.Off {
		run.Skipped = "step reminders are switched off"
		return run, nil
	}
	stored, err := docs.List[StepItem](ctx, o.Docs, tenant, cStepItem, "")
	if err != nil {
		return run, err
	}
	byID := map[string]StepItem{}
	reminded, escalated := map[string]bool{}, map[string]bool{}
	for _, it := range stored {
		byID[it.ID] = it
		if strings.HasPrefix(it.RemindedAt, e.today) {
			reminded[it.Step] = true
		}
		if strings.HasPrefix(it.EscalatedAt, e.today) {
			escalated[it.Step] = true
		}
	}
	// Today's steps, periodic work overdue, and everything carried over.
	insts := e.instancesOn(e.today)
	insts = append(insts, e.overduePeriodic(e.today)...)
	for _, it := range stored {
		if it.Status != StepOpen || it.Date > e.today {
			continue
		}
		if in, ok := e.inst(it); ok {
			insts = append(insts, in)
			continue
		}
		// The step is gone (the firm removed its own step, or a file or a
		// periodic task no longer applies): off the pending list, with why.
		it.Status, it.Reason, it.By, it.At = StepSkipped, "No longer one of the firm's steps", SystemActor, now.Format(time.RFC3339)
		if _, err := o.saveStep(ctx, tenant, SystemActor, it, "step.skipped", map[string]any{"reason": it.Reason}); err != nil {
			return run, err
		}
		run.Closed = append(run.Closed, it.ID)
	}
	sort.SliceStable(insts, func(i, j int) bool { return insts[i].due < insts[j].due })
	seen := map[string]bool{}
	for _, in := range insts {
		id := in.id()
		if seen[id] {
			continue
		}
		seen[id] = true
		it, has := byID[id]
		if has && it.Status != StepOpen {
			continue
		}
		if done, note := e.isDone(in); done {
			if has {
				it.Status, it.DoneNote, it.By, it.At = StepDone, note, SystemActor, now.Format(time.RFC3339)
				if err := docs.Upsert(ctx, o.Docs, tenant, cStepItem, id, it, SystemActor); err != nil {
					return run, err
				}
				o.closeStepWork(ctx, tenant, SystemActor, it, "Done: "+note)
				run.Closed = append(run.Closed, id)
			}
			continue
		}
		_, _, mins, on := e.ss.effective(in.def)
		if e.nowAt < in.due || !on || !in.def.worksOn(e.set, e.today) {
			continue
		}
		if !has {
			it = e.newItem(in)
		}
		changed := !has
		switch {
		case it.RemindedAt == "" && reminded[in.def.ID]:
			// One reminder a step a day: the step's other occurrence was
			// reminded today already.
			it.RemindedAt, changed = e.nowAt, true
		case it.RemindedAt == "":
			e.remind(&it, in)
			reminded[in.def.ID], changed = true, true
			run.Reminded = append(run.Reminded, id)
		case it.EscalatedAt == "" && !escalated[in.def.ID] && e.nowAt >= addMinutes(max(it.Due, it.RemindedAt), mins):
			e.escalate(&it, mins)
			escalated[in.def.ID], changed = true, true
			run.Escalated = append(run.Escalated, id)
		}
		if changed {
			if err := docs.Upsert(ctx, o.Docs, tenant, cStepItem, id, it, SystemActor); err != nil {
				return run, err
			}
		}
	}
	return run, nil
}

// ---- the screen ----

// StepsView is the day's steps and the pending list.
type StepsView struct {
	Date    string         `json:"date"`
	Now     string         `json:"now"`
	Working bool           `json:"working"`
	Holiday string         `json:"holiday,omitempty"`
	Off     bool           `json:"off"`
	Steps   []StepItem     `json:"steps"`
	Pending []StepItem     `json:"pending"` // from earlier days
	Counts  map[string]int `json:"counts"`
}

// decorate sets what the screen shows of an item, from its stored state
// and whether its work is done now.
func (e *stepEnv) decorate(it *StepItem, in stepInst, live bool) {
	switch it.Status {
	case StepDone:
		it.State = "done"
		return
	case StepByHand:
		it.State = "by_hand"
		return
	case StepSkipped:
		it.State = "skipped"
		return
	}
	if live {
		if done, note := e.isDone(in); done {
			it.State, it.DoneNote = "done", note
			return
		}
	}
	switch {
	case it.EscalatedAt != "":
		it.State = "escalated"
	case it.RemindedAt != "":
		it.State = "reminded"
	case e.nowAt >= it.Due:
		it.State = "overdue"
	default:
		it.State = "upcoming"
	}
	if in.def.Exceptions && in.key <= e.today {
		it.Exceptions = e.exceptions(in)
	}
}

// Steps lists a day's steps with their state, and what is pending from
// earlier days.
func (o *Ops) Steps(ctx context.Context, tenant, actor, date string) (StepsView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing step reminders", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return StepsView{}, err
	}
	e, err := o.newStepEnv(ctx, tenant, o.now())
	if err != nil {
		return StepsView{}, err
	}
	if date == "" {
		date = e.today
	}
	if !validDate(date) {
		return StepsView{}, invalid("give a date (YYYY-MM-DD)")
	}
	v := StepsView{Date: date, Now: e.nowAt, Working: e.set.IsBusinessDay(date), Off: e.ss.Off, Steps: []StepItem{}, Pending: []StepItem{}, Counts: map[string]int{}}
	_, v.Holiday = e.set.IsHoliday(date)
	stored, _ := docs.List[StepItem](ctx, o.Docs, tenant, cStepItem, "")
	byID := map[string]StepItem{}
	for _, it := range stored {
		byID[it.ID] = it
	}
	seen := map[string]bool{}
	for _, in := range e.instancesOn(date) {
		if seen[in.id()] {
			continue
		}
		seen[in.id()] = true
		it, ok := byID[in.id()]
		if !ok {
			it = e.newItem(in)
		}
		e.decorate(&it, in, date <= e.today)
		v.Steps = append(v.Steps, it)
		v.Counts[it.State]++
	}
	pend := map[string]bool{}
	for _, it := range stored {
		if it.Status != StepOpen || it.Date >= date || seen[it.ID] {
			continue
		}
		in, ok := e.inst(it)
		if !ok {
			continue
		}
		e.decorate(&it, in, true)
		if it.State == "done" {
			continue // done since the last run: it closes on the next
		}
		pend[it.ID] = true
		v.Pending = append(v.Pending, it)
	}
	if date == e.today {
		for _, in := range e.overduePeriodic(date) {
			if pend[in.id()] || seen[in.id()] {
				continue
			}
			if it, ok := byID[in.id()]; ok && it.Status != StepOpen {
				continue
			}
			it := e.newItem(in)
			e.decorate(&it, in, false)
			pend[it.ID] = true
			v.Pending = append(v.Pending, it)
		}
	}
	v.Counts["pending"] = len(v.Pending)
	sort.SliceStable(v.Steps, func(i, j int) bool { return v.Steps[i].Due < v.Steps[j].Due })
	sort.SliceStable(v.Pending, func(i, j int) bool { return v.Pending[i].Due < v.Pending[j].Due })
	return v, nil
}

// StepsPendingEarlier is the pending list from before a day, as stored by
// the reminder runs (for the dashboard and the Day runbook).
func (o *Ops) StepsPendingEarlier(ctx context.Context, tenant, date string) []StepItem {
	stored, _ := docs.List[StepItem](ctx, o.Docs, tenant, cStepItem, "")
	out := []StepItem{}
	for _, it := range stored {
		if it.Status == StepOpen && it.Date < date {
			it.State = "reminded"
			if it.EscalatedAt != "" {
				it.State = "escalated"
			}
			it.Exceptions = nil
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Due < out[j].Due })
	return out
}

// ---- acting on a step ----

// stepFor finds a step's item to act on, stored or not yet, and checks
// that the actor may: its owner (the person, or anyone with the owner's
// role), whoever it escalates to, or the Admin.
func (o *Ops) stepFor(ctx context.Context, tenant, actor, id, what string) (*stepEnv, StepItem, stepInst, error) {
	step, key, ok := strings.Cut(id, "|")
	if !ok {
		return nil, StepItem{}, stepInst{}, invalid("unknown step %q", id)
	}
	e, err := o.newStepEnv(ctx, tenant, o.now())
	if err != nil {
		return nil, StepItem{}, stepInst{}, err
	}
	d, ok := e.defs[step]
	if !ok {
		return nil, StepItem{}, stepInst{}, fmt.Errorf("%w: step %s", ErrNotFound, step)
	}
	it, _, err := docs.Get[StepItem](ctx, o.Docs, tenant, cStepItem, id)
	var in stepInst
	if err == nil {
		in, _ = e.inst(it)
	} else {
		if in, ok = e.findInst(d, key); !ok {
			return nil, StepItem{}, stepInst{}, invalid("%s has no occurrence for %s", d.Name, key)
		}
		it = e.newItem(in)
	}
	if it.Status != StepOpen {
		return nil, it, in, fmt.Errorf("%w: %s for %s is %s", ErrState, it.Name, fmtDay(it.Date), strings.ToLower(it.Status))
	}
	if u, ok := e.users[it.Owner]; ok && u.ID == actor {
		return e, it, in, nil
	}
	if esc := e.ss.EscalateTo; esc != "" && esc == actor {
		return e, it, in, nil
	}
	roles := []string{it.Role}
	if esc := firstOf(e.ss.EscalateTo, RoleAdmin); contains(staffRoles, esc) {
		roles = append(roles, esc)
	}
	if err := o.require(ctx, tenant, actor, what, roles...); err != nil {
		return nil, it, in, err
	}
	return e, it, in, nil
}

func (o *Ops) saveStep(ctx context.Context, tenant, actor string, it StepItem, event string, detail map[string]any) (StepItem, error) {
	if err := docs.Upsert(ctx, o.Docs, tenant, cStepItem, it.ID, it, actor); err != nil {
		return it, err
	}
	o.closeStepWork(ctx, tenant, actor, it, it.Status+": "+firstOf(it.Note, it.Reason))
	detail["step"], detail["date"] = it.Step, it.Date
	o.log(ctx, tenant, actor, event, it.ID, detail)
	it.State = map[string]string{StepDone: "done", StepByHand: "by_hand", StepSkipped: "skipped"}[it.Status]
	return it, nil
}

// MarkStepDone records that a step was done by hand outside Ledgerline.
func (o *Ops) MarkStepDone(ctx context.Context, tenant, actor, id, note string) (StepItem, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return StepItem{}, invalid("say how it was done outside Ledgerline")
	}
	_, it, _, err := o.stepFor(ctx, tenant, actor, id, "Closing a step")
	if err != nil {
		return it, err
	}
	it.Status, it.Note, it.By, it.At = StepByHand, note, actor, o.now().Format(time.RFC3339)
	return o.saveStep(ctx, tenant, actor, it, "step.done_by_hand", map[string]any{"note": note})
}

// SkipStep takes a step off the pending list for a reason.
func (o *Ops) SkipStep(ctx context.Context, tenant, actor, id, reason string) (StepItem, error) {
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) < 5 {
		return StepItem{}, invalid("say why the step is skipped")
	}
	_, it, _, err := o.stepFor(ctx, tenant, actor, id, "Closing a step")
	if err != nil {
		return it, err
	}
	it.Status, it.Reason, it.By, it.At = StepSkipped, reason, actor, o.now().Format(time.RFC3339)
	return o.saveStep(ctx, tenant, actor, it, "step.skipped", map[string]any{"reason": reason})
}

// ConfirmStep runs a step with the exceptions the person saw: the
// fingerprint is that of the list shown, and a list that has changed since
// is refused, so what was confirmed is what was left out.
func (o *Ops) ConfirmStep(ctx context.Context, tenant, actor, id, fingerprint, note string) (StepItem, error) {
	e, it, in, err := o.stepFor(ctx, tenant, actor, id, "Closing a step")
	if err != nil {
		return it, err
	}
	run := stepConfirmFns[in.def.ID]
	if run == nil || !in.def.Exceptions {
		return it, invalid("%s has no exceptions to confirm: mark it done instead", it.Name)
	}
	ex := e.exceptions(in)
	if ex == nil {
		return it, fmt.Errorf("%w: the exceptions of %s cannot be worked out now", ErrState, it.Name)
	}
	if strings.TrimSpace(fingerprint) != ex.Fingerprint {
		return it, fmt.Errorf("%w: the exemptions have changed since they were shown; look at the list again", ErrState)
	}
	res, err := run(o, ctx, tenant, actor, in.key)
	if err != nil {
		return it, err
	}
	c := &StepConfirmed{By: actor, At: o.now().Format(time.RFC3339), Fingerprint: ex.Fingerprint, Summary: ex.Summary, Clients: []string{}, Amount: ex.Amount, Result: res, Note: strings.TrimSpace(note)}
	for _, r := range ex.Rows {
		c.Clients = append(c.Clients, r.UCC)
	}
	it.Status, it.Confirmed, it.Exceptions, it.DoneNote, it.By, it.At = StepDone, c, ex, res, actor, c.At
	return o.saveStep(ctx, tenant, actor, it, "step.exceptions_confirmed", map[string]any{"exempt": strings.Join(c.Clients, ", "), "not_charged": ex.Amount.String(), "from": ex.From, "to": ex.To, "result": res, "note": c.Note})
}

// stepWorkDismissed: a step's work item closed from the work queue with a
// note counts as the step done by hand.
func (o *Ops) stepWorkDismissed(ctx context.Context, tenant, actor, id, note string) {
	it, _, err := docs.Get[StepItem](ctx, o.Docs, tenant, cStepItem, id)
	if err != nil || it.Status != StepOpen {
		return
	}
	it.Status, it.Note, it.By, it.At = StepByHand, "Closed from the work queue: "+note, actor, o.now().Format(time.RFC3339)
	_, _ = o.saveStep(ctx, tenant, actor, it, "step.done_by_hand", map[string]any{"note": it.Note})
}

// CheckStepsNow runs the reminders at once (the screen's button).
func (o *Ops) CheckStepsNow(ctx context.Context, tenant, actor string) (StepRun, error) {
	if err := o.require(ctx, tenant, actor, "Checking steps for reminders", RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return StepRun{}, err
	}
	return o.RunStepReminders(ctx, tenant, o.now())
}
