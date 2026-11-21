package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// stepClock gives a test its own clock and a record of the step events.
type stepClock struct {
	at     time.Time
	events []StepEvent
}

func newStepClock(o *Ops, at time.Time) *stepClock {
	c := &stepClock{at: at}
	o.Now = func() time.Time { return c.at }
	o.StepEvents = func(_ context.Context, e StepEvent) { c.events = append(c.events, e) }
	return c
}

func (c *stepClock) set(day, hm string) {
	t, err := time.ParseInLocation("2006-01-02 15:04", day+" "+hm, ist)
	if err != nil {
		panic(err)
	}
	c.at = t
}

// of counts the events of a kind for a step item.
func (c *stepClock) of(kind, item string) int {
	n := 0
	for _, e := range c.events {
		if e.Kind == kind && e.Item == item {
			n++
		}
	}
	return n
}

func stepRun(t *testing.T, o *Ops, ctx context.Context, c *stepClock) StepRun {
	t.Helper()
	r, err := o.RunStepReminders(ctx, T, c.at)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func stepItem(t *testing.T, o *Ops, ctx context.Context, id string) StepItem {
	t.Helper()
	it, _, err := docs.Get[StepItem](ctx, o.Docs, T, cStepItem, id)
	if err != nil {
		t.Fatalf("step item %s: %v", id, err)
	}
	return it
}

func findStep(v StepsView, id string) (StepItem, bool) {
	for _, it := range append(append([]StepItem{}, v.Steps...), v.Pending...) {
		if it.ID == id {
			return it, true
		}
	}
	return StepItem{}, false
}

func audited(t *testing.T, o *Ops, ctx context.Context, action, object string) map[string]any {
	t.Helper()
	evs, _ := o.App.Audit.List(ctx, T, 0)
	for _, e := range evs {
		if e.Action == action && e.Object == object {
			return e.Detail
		}
	}
	t.Fatalf("no %s for %s in the audit trail", action, object)
	return nil
}

const (
	stepFri = "2026-10-16"
	stepSat = "2026-10-17"
	stepMon = "2026-10-19"
)

// stepDays checks the calendar the tests rely on.
func stepDays(t *testing.T, o *Ops, ctx context.Context) {
	t.Helper()
	set, _ := o.Settings(ctx, T)
	if !set.IsBusinessDay(stepFri) || set.IsBusinessDay(stepSat) || !set.IsBusinessDay(stepMon) {
		t.Fatalf("calendar: %s %v, %s %v, %s %v", stepFri, set.IsBusinessDay(stepFri), stepSat, set.IsBusinessDay(stepSat), stepMon, set.IsBusinessDay(stepMon))
	}
}

// A firm's own step owned by a named person: due at 10:00, reminded once
// at the due time, escalated to the Admin 15 minutes later, once.
func TestStepReminderTiming(t *testing.T) {
	o, ctx := newOps(t)
	c := newStepClock(o, time.Time{})
	c.set(stepFri, "08:00")
	stepDays(t, o, ctx)
	if _, err := o.SaveStepSettings(ctx, T, "priya.nair", StepSettings{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker changed step settings: %v", err)
	}
	bad := []StepSettings{
		{Custom: []StepDef{{Name: "Cash count", Due: "25:00"}}},
		{Custom: []StepDef{{Name: "Cash count", Due: "10:00", Role: "nobody.here"}}},
		{Custom: []StepDef{{Name: "Cash count", Due: "10:00", Days: "fortnightly"}}},
		{Steps: map[string]StepSetting{"autopilot:billing": {Due: "7pm"}}},
		{Steps: map[string]StepSetting{"no:such": {Due: "19:00"}}},
		{EscalateTo: "Client"},
	}
	for i, b := range bad {
		if _, err := o.SaveStepSettings(ctx, T, "admin", b); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad settings %d accepted: %v", i, err)
		}
	}
	ss, err := o.SaveStepSettings(ctx, T, "admin", StepSettings{
		Custom: []StepDef{{Name: "Petty cash count", About: "Count the petty cash and sign the register.", Due: "10:00", Role: "farhan.khan"}},
		Steps:  map[string]StepSetting{"custom:petty-cash-count": {EscalateMin: 15}, "autopilot:billing": {Due: "19:45", Owner: RoleRisk}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ss.Custom) != 1 || ss.Custom[0].ID != "custom:petty-cash-count" || ss.Custom[0].Days != "business" {
		t.Fatalf("custom step %+v", ss.Custom)
	}
	sv, err := o.StepSettingsFor(ctx, T, "priya.nair")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, r := range sv.Rows {
		kinds[r.Kind] = true
		if r.ID == "autopilot:billing" && (r.EffDue != "19:45" || r.Owner != RoleRisk || !r.Changed) {
			t.Fatalf("billing setting %+v", r)
		}
		if r.Due == "" || r.Role == "" {
			t.Fatalf("step without a due time or owner: %+v", r)
		}
	}
	for _, k := range []string{StepAutopilot, StepPeriodic, StepFile, StepManual} {
		if !kinds[k] {
			t.Fatalf("no %s steps in %v", k, kinds)
		}
	}

	id := "custom:petty-cash-count|" + stepFri
	v, err := o.Steps(ctx, T, "priya.nair", stepFri)
	if err != nil {
		t.Fatal(err)
	}
	it, ok := findStep(v, id)
	if !ok || it.State != "upcoming" || it.Due != stepFri+" 10:00" || it.Owner != "farhan.khan" || it.Role != RoleChecker {
		t.Fatalf("before the due time: %+v %v", it, ok)
	}
	// Work Ledgerline can see is done needs no reminder: with no trades or
	// positions, the day's reconciliation and billing are done.
	for _, s := range []string{"autopilot:recon|" + stepFri, "autopilot:billing|" + stepFri} {
		if x, ok := findStep(v, s); !ok || x.State != "done" || x.DoneNote != "No trades or positions to work" {
			t.Fatalf("%s: %+v", s, x)
		}
	}
	c.set(stepFri, "09:59")
	stepRun(t, o, ctx, c)
	if c.of("reminder", id) != 0 {
		t.Fatal("reminded before the due time")
	}
	c.set(stepFri, "10:00")
	r := stepRun(t, o, ctx, c)
	if !contains(r.Reminded, id) || c.of("reminder", id) != 1 {
		t.Fatalf("not reminded at the due time: %+v", r)
	}
	ev := c.events[len(c.events)-1]
	for _, e := range c.events {
		if e.Item == id {
			ev = e
		}
	}
	if strings.Join(ev.Users, ",") != "farhan.khan" || !strings.Contains(ev.Text, "Petty cash count was due by 10:00") {
		t.Fatalf("reminder event %+v", ev)
	}
	audited(t, o, ctx, "step.reminder", id)
	w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, stepItem(t, o, ctx, id).Work)
	if err != nil || w.Status != "Open" || w.Role != RoleChecker || w.Meta["owner"] != "farhan.khan" || w.Due != stepFri+" 10:00" {
		t.Fatalf("work item %+v %v", w, err)
	}
	// Ledgy told the owner, in their own channel.
	if ch, err := o.chatChannel(ctx, T, "L-"+shortHash("farhan.khan")); err != nil || ch.Kind != ChatLedgy {
		t.Fatalf("no Ledgy channel for the owner: %v", err)
	}

	// No spam: later runs the same day remind nothing more.
	c.set(stepFri, "10:05")
	stepRun(t, o, ctx, c)
	c.set(stepFri, "10:14")
	stepRun(t, o, ctx, c)
	if c.of("reminder", id) != 1 || c.of("escalation", id) != 0 {
		t.Fatalf("reminders %d, escalations %d before the delay", c.of("reminder", id), c.of("escalation", id))
	}
	c.set(stepFri, "10:15")
	stepRun(t, o, ctx, c)
	if c.of("escalation", id) != 1 {
		t.Fatal("not escalated after 15 minutes")
	}
	it = stepItem(t, o, ctx, id)
	if it.EscalatedTo != RoleAdmin || it.EscWork == "" {
		t.Fatalf("escalation %+v", it)
	}
	for _, e := range c.events {
		if e.Kind == "escalation" && e.Item == id && strings.Join(e.Users, ",") != "admin" {
			t.Fatalf("escalated to %v", e.Users)
		}
	}
	c.set(stepFri, "16:00")
	stepRun(t, o, ctx, c)
	if c.of("reminder", id) != 1 || c.of("escalation", id) != 1 {
		t.Fatal("reminded or escalated twice")
	}
	if v, _ := o.Steps(ctx, T, "priya.nair", stepFri); func() string { x, _ := findStep(v, id); return x.State }() != "escalated" {
		t.Fatal("screen does not show the escalation")
	}

	// The head of the desk instead of the Admin.
	if _, err := o.SaveStepSettings(ctx, T, "admin", StepSettings{EscalateTo: "anita.rao", Custom: ss.Custom, Steps: map[string]StepSetting{"custom:petty-cash-count": {Due: "11:00", EscalateMin: 5}}}); err != nil {
		t.Fatal(err)
	}
	// Monday: Friday's item is pending from an earlier day, and Monday's own
	// occurrence is reminded and escalated to the head of the desk.
	c.set(stepMon, "11:05")
	stepRun(t, o, ctx, c)
	c.set(stepMon, "11:10")
	stepRun(t, o, ctx, c)
	mon := "custom:petty-cash-count|" + stepMon
	if c.of("reminder", mon) != 1 || c.of("escalation", mon) != 1 || c.of("reminder", id) != 1 {
		t.Fatalf("Monday: reminders %d, escalations %d, Friday's %d", c.of("reminder", mon), c.of("escalation", mon), c.of("reminder", id))
	}
	if it := stepItem(t, o, ctx, mon); it.EscalatedTo != "anita.rao" {
		t.Fatalf("escalated to %s", it.EscalatedTo)
	}
	v, _ = o.Steps(ctx, T, "priya.nair", stepMon)
	if p, ok := findStep(StepsView{Pending: v.Pending}, id); !ok || p.Date != stepFri {
		t.Fatalf("Friday's step not pending on Monday: %+v", v.Pending)
	}
	d, err := o.Dashboard(ctx, T, stepMon)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := findStep(StepsView{Pending: d.StepsPending}, id); !ok || p.State != "escalated" {
		t.Fatalf("dashboard pending %+v", d.StepsPending)
	}
	if _, ok := findStep(StepsView{Pending: d.StepsPending}, mon); ok {
		t.Fatal("today's step on the dashboard's earlier-days list")
	}
}

// Holidays and weekends: a business-day step has no occurrence and no
// reminder on Saturday or on the firm's holiday; a step that runs on
// Saturdays does.
func TestStepRemindersHolidays(t *testing.T) {
	o, ctx := newOps(t)
	c := newStepClock(o, time.Time{})
	c.set(stepFri, "09:00")
	stepDays(t, o, ctx)
	if _, err := o.SaveStepSettings(ctx, T, "admin", StepSettings{Custom: []StepDef{
		{Name: "Branch cash count", Due: "10:00", Role: RoleMaker},
		{Name: "Saturday backup tape", Due: "10:00", Role: RoleChecker, Days: "business+sat"},
		{Name: "Weekly office review", Due: "10:00", Role: RoleChecker, Days: "weekly:Sat"},
	}}); err != nil {
		t.Fatal(err)
	}
	fri := "custom:branch-cash-count|" + stepFri
	c.set(stepFri, "10:01")
	stepRun(t, o, ctx, c)
	if c.of("reminder", fri) != 1 || c.of("reminder", "custom:weekly-office-review|"+stepFri) != 0 {
		t.Fatal("Friday's reminders")
	}
	// Saturday: no occurrence of the business-day step, and Friday's is not
	// escalated (the step does not run on Saturdays); the Saturday steps are
	// reminded.
	c.set(stepSat, "12:00")
	stepRun(t, o, ctx, c)
	v, _ := o.Steps(ctx, T, "admin", stepSat)
	if _, ok := findStep(StepsView{Steps: v.Steps}, "custom:branch-cash-count|"+stepSat); ok {
		t.Fatal("a business-day step on a Saturday")
	}
	if p, ok := findStep(StepsView{Pending: v.Pending}, fri); !ok || p.State != "reminded" {
		t.Fatalf("Friday's step on Saturday: %+v", p)
	}
	if c.of("escalation", fri) != 0 {
		t.Fatal("escalated on a Saturday")
	}
	for _, id := range []string{"custom:saturday-backup-tape|" + stepSat, "custom:weekly-office-review|" + stepSat} {
		if c.of("reminder", id) != 1 {
			t.Fatalf("%s not reminded on Saturday", id)
		}
	}
	// The firm's own holiday on Monday: nothing for Monday, no escalation.
	set, _ := o.Settings(ctx, T)
	set.Holidays = append(set.Holidays, stepMon)
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	c.set(stepMon, "12:00")
	stepRun(t, o, ctx, c)
	if c.of("reminder", "custom:branch-cash-count|"+stepMon) != 0 || c.of("escalation", fri) != 0 {
		t.Fatal("reminded or escalated on the firm's holiday")
	}
	v, _ = o.Steps(ctx, T, "admin", stepMon)
	if _, ok := findStep(StepsView{Steps: v.Steps}, "custom:branch-cash-count|"+stepMon); v.Working || ok {
		t.Fatalf("steps on a holiday: working %v", v.Working)
	}
	// The next business day: Friday's step escalates, the day's own is
	// reminded, each once.
	set, _ = o.Settings(ctx, T)
	tue := set.NextBusinessDay(stepMon)
	c.set(tue, "12:00")
	stepRun(t, o, ctx, c)
	c.set(tue, "12:40")
	stepRun(t, o, ctx, c)
	if c.of("escalation", fri) != 1 || c.of("reminder", "custom:branch-cash-count|"+tue) != 1 || c.of("reminder", fri) != 1 {
		t.Fatalf("%s: escalations %d, reminders %d", tue, c.of("escalation", fri), c.of("reminder", "custom:branch-cash-count|"+tue))
	}
	// A step the firm removes leaves the pending list, with why.
	if _, err := o.SaveStepSettings(ctx, T, "admin", StepSettings{}); err != nil {
		t.Fatal(err)
	}
	c.set(tue, "13:00")
	stepRun(t, o, ctx, c)
	if it := stepItem(t, o, ctx, fri); it.Status != StepSkipped || it.Reason != "No longer one of the firm's steps" {
		t.Fatalf("removed step %+v", it)
	}
}

// Carry-over, done by hand and skipped with a reason.
func TestStepCarryOverSkipAndDoneByHand(t *testing.T) {
	o, ctx := newOps(t)
	c := newStepClock(o, time.Time{})
	c.set(stepFri, "18:00")
	stepDays(t, o, ctx)
	if _, err := o.SaveStepSettings(ctx, T, "admin", StepSettings{Custom: []StepDef{{Name: "Courier dispatch", Due: "17:00", Role: RoleMaker}}}); err != nil {
		t.Fatal(err)
	}
	fri, mon := "custom:courier-dispatch|"+stepFri, "custom:courier-dispatch|"+stepMon
	stepRun(t, o, ctx, c)
	c.set(stepMon, "09:00")
	stepRun(t, o, ctx, c)
	v, _ := o.Steps(ctx, T, "priya.nair", stepMon)
	if p, ok := findStep(StepsView{Pending: v.Pending}, fri); !ok || p.State != "escalated" {
		t.Fatalf("not carried over: %+v", v.Pending)
	}
	if it, ok := findStep(StepsView{Steps: v.Steps}, mon); !ok || it.State != "upcoming" {
		t.Fatalf("Monday's step %+v", it)
	}
	// Still pending on Tuesday-like later days until settled.
	if _, ok := findStep(StepsView{Pending: o.StepsPendingEarlier(ctx, T, "2026-10-22")}, fri); !ok {
		t.Fatal("pending list lost the step")
	}

	// Skip: a reason is needed and audited; only the owner's role, the
	// escalation target or the Admin may.
	if _, err := o.SkipStep(ctx, T, "priya.nair", fri, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("skipped without a reason: %v", err)
	}
	if _, err := o.SkipStep(ctx, T, "vikram.shah", fri, "Courier did not come"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance skipped an ops step: %v", err)
	}
	it, err := o.SkipStep(ctx, T, "priya.nair", fri, "Courier strike on Friday; sent by post on Monday")
	if err != nil || it.Status != StepSkipped || it.State != "skipped" {
		t.Fatalf("skip %+v %v", it, err)
	}
	if d := audited(t, o, ctx, "step.skipped", fri); fmt.Sprint(d["reason"]) != "Courier strike on Friday; sent by post on Monday" {
		t.Fatalf("audit %v", d)
	}
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, it.Work); w.Status != "Done" {
		t.Fatalf("work item still %s", w.Status)
	}
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, it.EscWork); w.Status != "Done" {
		t.Fatalf("escalation item still %s", w.Status)
	}
	if _, err := o.SkipStep(ctx, T, "priya.nair", fri, "again please"); !errors.Is(err, ErrState) {
		t.Fatalf("skipped twice: %v", err)
	}
	if _, ok := findStep(StepsView{Pending: o.StepsPendingEarlier(ctx, T, stepMon)}, fri); ok {
		t.Fatal("a skipped step is still pending")
	}

	// Done by hand, before its due time and before any reminder.
	if _, err := o.MarkStepDone(ctx, T, "priya.nair", mon, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("done without a note: %v", err)
	}
	it, err = o.MarkStepDone(ctx, T, "priya.nair", mon, "Handed to the courier at 08:45; docket 4471")
	if err != nil || it.Status != StepByHand {
		t.Fatalf("done by hand %+v %v", it, err)
	}
	audited(t, o, ctx, "step.done_by_hand", mon)
	c.set(stepMon, "18:00")
	stepRun(t, o, ctx, c)
	if c.of("reminder", mon) != 0 {
		t.Fatal("reminded after it was done by hand")
	}

	// A reminder closed from the work queue with a note is done by hand.
	tue := "2026-10-20"
	if set, _ := o.Settings(ctx, T); !set.IsBusinessDay(tue) {
		t.Skip("20 Oct 2026 is a holiday in this calendar")
	}
	c.set(tue, "18:00")
	stepRun(t, o, ctx, c)
	tid := "custom:courier-dispatch|" + tue
	if _, err := o.CloseWorkItem(ctx, T, "priya.nair", stepItem(t, o, ctx, tid).Work, false, "Sent with the evening courier"); err != nil {
		t.Fatal(err)
	}
	if it := stepItem(t, o, ctx, tid); it.Status != StepByHand || !strings.Contains(it.Note, "Sent with the evening courier") {
		t.Fatalf("closing the work item: %+v", it)
	}
}

// A step Ledgerline can tell is done closes by itself: a checklist file
// shares its work item with the missing-file alerts and closes when the
// file is loaded.
func TestStepFileClosesItself(t *testing.T) {
	o, ctx := newOps(t)
	c := newStepClock(o, time.Time{})
	c.set(stepFri, "07:00")
	stepDays(t, o, ctx)
	cl, err := o.DailyFileChecklist(ctx, T, stepFri)
	if err != nil {
		t.Fatal(err)
	}
	var f ChecklistItem
	for _, it := range cl.Items {
		if it.Name == "" && it.Format != "" && !it.Intraday && !it.NextDay && it.At >= "08:00" && it.At <= "09:00" {
			f = it
			break
		}
	}
	if f.ID == "" {
		t.Skip("no plain morning file on the checklist")
	}
	id := StepFile + ":" + f.ID + "|" + stepFri
	c.set(stepFri, "21:00")
	stepRun(t, o, ctx, c)
	it := stepItem(t, o, ctx, id)
	if it.RemindedAt == "" || it.Work != "W-"+wFileMissing+"-"+stepFri+"-"+f.ID {
		t.Fatalf("file step %+v", it)
	}
	o.noteDailyFile(ctx, T, "priya.nair", f.Format, "made-up-"+f.ID+".csv", stepFri)
	c.set(stepFri, "21:10")
	r := stepRun(t, o, ctx, c)
	if it := stepItem(t, o, ctx, id); !contains(r.Closed, id) || it.Status != StepDone || !strings.Contains(it.DoneNote, "made-up-") {
		t.Fatalf("not closed when the file came: %+v", it)
	}
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, it.Work); w.Status != "Done" {
		t.Fatalf("missing-file item %s", w.Status)
	}
}

// The daily interest run: its reminder lists the exempt clients and the
// interest not charged, and the checker confirms "run with these
// exemptions".
func TestStepInterestExemptionsConfirmed(t *testing.T) {
	o, ctx := newOps(t)
	c := newStepClock(o, time.Time{})
	c.set(stepFri, "12:00")
	stepDays(t, o, ctx)
	put := func(cl Client) {
		if err := docs.Upsert(ctx, o.Docs, T, cClient, cl.UCC, cl, "test"); err != nil {
			t.Fatal(err)
		}
	}
	put(Client{UCC: "SX001", Name: "Valued Client", Status: "Active", Tags: []string{"VIP"}})
	put(Client{UCC: "SX002", Name: "Promoter Family", Status: "Active", Tags: []string{}})
	put(Client{UCC: "SX003", Name: "Ordinary Client", Status: "Active", Tags: []string{}})
	for _, u := range []string{"SX001", "SX002", "SX003"} {
		if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "B:" + u, ValueDate: "2026-10-14", Narration: "Bill", Source: ledger.SourceRef{Kind: "BILL", ID: u}, CreatedBy: "test",
			Postings: []ledger.Posting{{Account: ledger.ClientAccount(u), Debit: money.MustAmount("100000")}, {Account: ledger.OpeningEquity, Credit: money.MustAmount("100000")}}}); err != nil {
			t.Fatal(err)
		}
	}
	set, _ := o.Settings(ctx, T)
	set.InterestPctPA, set.InterestGraceDays, set.InterestMinimum = "24", 0, "0"
	set.InterestPosting, set.InterestDailyFrom, set.InterestExemptTags = InterestDaily, "2026-10-14", []string{"VIP"}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveClientInterest(ctx, T, "farhan.khan", ClientInterestInput{UCC: "SX002", InterestTerms: InterestTerms{From: "2026-10-01", Free: true, Note: "Promoter family"}}); err != nil {
		t.Fatal(err)
	}
	id := "autopilot:interest_daily|" + stepFri
	v, err := o.Steps(ctx, T, "farhan.khan", stepFri)
	if err != nil {
		t.Fatal(err)
	}
	it, ok := findStep(v, id)
	if !ok || !it.HasExceptions || it.Exceptions == nil || it.Due != stepFri+" 20:30" || it.Owner != RoleChecker {
		t.Fatalf("interest step %+v", it)
	}
	ex := it.Exceptions
	rows := map[string]StepExceptionRow{}
	for _, r := range ex.Rows {
		rows[r.UCC] = r
	}
	// Overdue from 15 Oct (the pay-in day): two days at 24% on ₹1,00,000,
	// 65.75 a day.
	if r := rows["SX001"]; r.Days != 2 || r.Amount.String() != "131.50" || !strings.Contains(r.Why, "VIP") {
		t.Fatalf("VIP row %+v (all %+v)", r, ex.Rows)
	}
	if r := rows["SX002"]; r.Days != 2 || r.Amount.String() != "131.50" || !strings.Contains(r.Why, "Promoter family") {
		t.Fatalf("own terms row %+v", r)
	}
	if _, ok := rows["SX003"]; ok {
		t.Fatal("an ordinary client listed as exempt")
	}
	if ex.From != "2026-10-14" || ex.To != stepFri || ex.Confirm != "Run with these exemptions" || !strings.Contains(ex.Summary, "2 clients exempt") {
		t.Fatalf("exceptions %+v", ex)
	}

	// The reminder carries the list.
	c.set(stepFri, "20:30")
	stepRun(t, o, ctx, c)
	stored := stepItem(t, o, ctx, id)
	if c.of("reminder", id) != 1 || stored.Exceptions == nil || stored.Exceptions.Fingerprint != ex.Fingerprint {
		t.Fatalf("reminder %+v", stored)
	}
	for _, e := range c.events {
		if e.Item == id && !strings.Contains(e.Text, "2 clients exempt") {
			t.Fatalf("reminder text %q", e.Text)
		}
	}

	// Confirming: the owner's role (or the Admin), and only the list shown.
	if _, err := o.ConfirmStep(ctx, T, "priya.nair", id, ex.Fingerprint, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker confirmed: %v", err)
	}
	if _, err := o.ConfirmStep(ctx, T, "farhan.khan", id, "stale", ""); !errors.Is(err, ErrState) {
		t.Fatalf("a stale list confirmed: %v", err)
	}
	done, err := o.ConfirmStep(ctx, T, "farhan.khan", id, ex.Fingerprint, "As agreed with the MD")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != StepDone || done.Confirmed == nil || strings.Join(done.Confirmed.Clients, ",") != "SX001,SX002" || done.Confirmed.Amount.String() != "263.00" {
		t.Fatalf("confirmed %+v %+v", done, done.Confirmed)
	}
	d := audited(t, o, ctx, "step.exceptions_confirmed", id)
	if fmt.Sprint(d["exempt"]) != "SX001, SX002" || fmt.Sprint(d["not_charged"]) != "263.00" {
		t.Fatalf("audit %v", d)
	}
	charged := map[string]int{}
	es, _ := o.App.Ledger.Store.List(ctx, T, ledger.Filter{})
	for _, e := range es {
		if e.Source.Kind == "INTEREST" {
			charged[e.Source.ID[strings.LastIndex(e.Source.ID, "/")+1:]]++
		}
	}
	if charged["SX003"] != 2 || charged["SX001"] != 0 || charged["SX002"] != 0 {
		t.Fatalf("interest posted %v", charged)
	}
	// Done: no escalation, and Monday's run covers the weekend.
	c.set(stepFri, "21:30")
	stepRun(t, o, ctx, c)
	if c.of("escalation", id) != 0 {
		t.Fatal("escalated after it was confirmed")
	}
	mex, err := o.InterestExemptions(ctx, T, stepMon)
	if err != nil {
		t.Fatal(err)
	}
	if mex.From != stepSat || mex.To != stepMon || len(mex.Rows) != 2 || mex.Rows[0].Days != 3 {
		t.Fatalf("Monday's exemptions %+v", mex)
	}
	// Posted by Autopilot (or from the work queue) also counts as done.
	if _, err := o.PostDailyInterest(ctx, T, SystemActor, stepMon); err != nil {
		t.Fatal(err)
	}
	c.set(stepMon, "21:00")
	stepRun(t, o, ctx, c)
	if c.of("reminder", "autopilot:interest_daily|"+stepMon) != 0 {
		t.Fatal("reminded although the interest was posted")
	}
}
