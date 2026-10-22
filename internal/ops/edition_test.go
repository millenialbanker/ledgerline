package ops

import (
	"errors"
	"testing"
	"time"

	"ledgerline/internal/app"
)

func modeOf(t *testing.T, o *Ops, id string) string {
	t.Helper()
	_, m, _ := o.policy(t.Context(), T, id)
	return m
}

func TestEditions(t *testing.T) {
	o, ctx := setup(t)

	// Autopilot (the default): each automation at its usual setting.
	if ed, _ := o.Edition(ctx, T); ed != EditionAutopilot {
		t.Fatalf("default edition %s", ed)
	}
	if modeOf(t, o, "inactive") != ModeSuggest || modeOf(t, o, "billing") != ModeAuto || modeOf(t, o, "unidentified") != ModeSuggest {
		t.Fatalf("autopilot modes: %s %s %s", modeOf(t, o, "inactive"), modeOf(t, o, "billing"), modeOf(t, o, "unidentified"))
	}
	// The firm turns billing down to Suggest while on Autopilot.
	if _, err := o.SetAutoPolicy(ctx, T, "admin", AutoPolicy{ID: "billing", Mode: ModeSuggest}); err != nil {
		t.Fatal(err)
	}

	// Full autopilot: everything at its cap. Settings saved under the
	// earlier edition no longer hold back; caps set by the rules still do.
	time.Sleep(2 * time.Millisecond)
	o.SetEdition(T, EditionFull)
	if modeOf(t, o, "inactive") != ModeAuto || modeOf(t, o, "billing") != ModeAuto {
		t.Fatalf("full: inactive %s billing %s", modeOf(t, o, "inactive"), modeOf(t, o, "billing"))
	}
	for _, d := range AutoDefs {
		if modeRank[modeOf(t, o, d.ID)] > modeRank[d.Cap] {
			t.Fatalf("%s above its cap", d.ID)
		}
		if d.Cap == ModeSuggest && modeOf(t, o, d.ID) != ModeSuggest {
			t.Fatalf("%s should stay Suggest (rules need a person): %s", d.ID, modeOf(t, o, d.ID))
		}
	}
	if !o.AutopilotScheduled(ctx, T) {
		t.Fatal("full autopilot should switch the schedule on")
	}
	// A firm on Full may still turn something down.
	time.Sleep(2 * time.Millisecond)
	if _, err := o.SetAutoPolicy(ctx, T, "admin", AutoPolicy{ID: "payouts", Mode: ModeSuggest}); err != nil {
		t.Fatal(err)
	}
	if modeOf(t, o, "payouts") != ModeSuggest {
		t.Fatal("full: firm's own lower setting ignored")
	}

	// Manual: nothing automatic.
	o.SetEdition(T, EditionManual)
	for _, d := range AutoDefs {
		if modeOf(t, o, d.ID) != ModeOff {
			t.Fatalf("manual: %s is %s", d.ID, modeOf(t, o, d.ID))
		}
	}
	if _, err := o.SetAutoPolicy(ctx, T, "admin", AutoPolicy{ID: "billing", Mode: ModeAuto}); err == nil {
		t.Fatal("manual: automation changed")
	}
	if _, err := o.RunStage(ctx, T, "admin", D, "eod", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("manual: stage ran by hand: %v", err)
	}
	if runs, err := o.RunStage(ctx, T, SystemActor, D, "eod", ""); err != nil || len(runs) != 0 {
		t.Fatalf("manual: scheduler ran %d: %v", len(runs), err)
	}
	if o.AutopilotScheduled(ctx, T) {
		t.Fatal("manual: schedule shown as on")
	}
	if _, err := o.SetAutopilotSchedule(ctx, T, "admin", true); err == nil {
		t.Fatal("manual: schedule switched on")
	}
	js, _, _ := o.Jobs(ctx, T)
	for _, j := range js {
		if j.Kind == "autopilot" || j.Active {
			t.Fatalf("manual: job %s %s active=%v", j.ID, j.Kind, j.Active)
		}
	}
	if n, _ := o.Tick(ctx, T, time.Date(2026, 10, 19, 23, 0, 0, 0, ist)); n != 0 {
		t.Fatalf("manual: %d jobs ran on a timer", n)
	}
	// Jobs can still be run by hand, and insights found on request.
	if run, err := o.RunJob(ctx, T, "admin", js[0].ID, D); err != nil || run.Status != "Succeeded" {
		t.Fatalf("manual: run by hand: %+v %v", run, err)
	}
	if _, err := o.FindInsights(ctx, T, "admin", D); err != nil {
		t.Fatalf("manual: find insights: %v", err)
	}
	// Ledgy says so.
	a, _ := o.Ledgy(ctx, T, "admin", "how do I set up autopilot", "", D)
	if len(a.Facts) == 0 || a.Facts[0].Title != "Your edition: Manual" {
		t.Fatalf("ledgy: %+v", a.Facts)
	}

	// Every change is in the audit trail.
	ev, _ := o.App.Audit.List(ctx, T, 0)
	n := 0
	for _, e := range ev {
		if e.Action == "edition.changed" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("edition changes logged: %d", n)
	}
	_ = app.AuditEvent{}
}

func TestParseEditions(t *testing.T) {
	m, err := ParseEditions("alpha:manual, beta:full")
	if err != nil || m["alpha"] != EditionManual || m["beta"] != EditionFull {
		t.Fatal(m, err)
	}
	for _, bad := range []string{"alpha", "alpha:turbo", ":full"} {
		if _, err := ParseEditions(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
