package ops

import (
	"context"

	"errors"
	"ledgerline/internal/docs"
	"strings"
	"testing"
)

func TestBSDAReview(t *testing.T) {
	o, ctx := setup(t)
	asOf := D
	if _, err := o.ReviewBSDA(ctx, T, "priya.nair", "2026-06-30"); err == nil {
		t.Fatal("reviewed without prices")
	}
	// One eligible client chose a regular account; another BSDA is above the limit.
	v, _ := o.DP(ctx, T, D)
	var stay, over string
	for _, r := range v.Rows {
		if r.BSDAOK && !r.BSDA && stay == "" {
			stay = r.UCC
		}
		if !r.BSDAOK && over == "" {
			over = r.UCC
		}
	}
	if stay == "" {
		t.Skip("no BSDA-eligible sample account")
	}
	if _, err := o.SetBSDAChoice(ctx, T, "priya.nair", stay, true, false); err != nil {
		t.Fatal(err)
	}
	if over != "" {
		b := o.bo(ctx, T, mustClient(t, o, over))
		b.BSDA = true
		_ = upsertBO(o, ctx, b)
	}
	r, err := o.ReviewBSDA(ctx, T, "priya.nair", asOf)
	if err != nil || r.ID != "2026-Q4" || r.Status != "Proposed" {
		t.Fatalf("review %+v %v", r, err)
	}
	for _, l := range r.Lines {
		if l.UCC == stay {
			t.Fatal("opted-out client proposed for BSDA")
		}
	}
	kept := false
	for _, l := range r.Kept {
		kept = kept || (l.UCC == stay && strings.Contains(l.Reason, "chose"))
	}
	if !kept {
		t.Fatalf("opt-out not shown: %+v", r.Kept)
	}
	if over != "" {
		found := false
		for _, l := range r.Lines {
			found = found || (l.UCC == over && l.To == "Regular")
		}
		if !found {
			t.Fatalf("BSDA over the limit not reverted: %+v", r.Lines)
		}
	}
	if _, err := o.ApplyBSDAReview(ctx, T, "priya.nair", r.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker applied: %v", err)
	}
	a, err := o.ApplyBSDAReview(ctx, T, "farhan.khan", r.ID)
	if err != nil || a.Status != "Applied" || a.FileID == "" {
		t.Fatalf("apply %+v %v", a, err)
	}
	f, err := o.PeriodicFileData(ctx, T, "priya.nair", a.FileID)
	if err != nil || !strings.HasPrefix(string(f.Content), "BOID,ClientCode") || strings.Count(string(f.Content), "\n") != len(a.Lines)+1 {
		t.Fatalf("file %q %v", f.Content, err)
	}
	for _, l := range a.Lines {
		b := o.bo(ctx, T, mustClient(t, o, l.UCC))
		if b.BSDA != (l.To == "BSDA") {
			t.Fatalf("%s not changed", l.UCC)
		}
	}
	if _, err := o.ReviewBSDA(ctx, T, "priya.nair", asOf); !errors.Is(err, ErrState) {
		t.Fatalf("applied review redone: %v", err)
	}
}

func TestPeriodicCalendar(t *testing.T) {
	o, ctx := notesReady(t)
	date := "2026-10-19" // a Monday
	ps, err := o.Periodic(ctx, T, date)
	if err != nil || len(ps) != len(PeriodicTasks) {
		t.Fatalf("%d %v", len(ps), err)
	}
	by := map[string]PeriodicStatus{}
	for _, p := range ps {
		by[p.ID] = p
	}
	if p := by["funds_securities"]; p.Period != "2026-10-16" || p.Status != "Overdue" || p.DueOn != "2026-10-17" {
		t.Fatalf("weekly %+v", p)
	}
	if p := by["month_end"]; p.Period != "2026-09" || p.Status != "Overdue" {
		t.Fatalf("month end %+v", p)
	}
	if p := by["bsda_review"]; p.Period != "2026-Q3" || p.DueOn != "2026-10-10" {
		t.Fatalf("bsda %+v", p)
	}
	r, err := o.RunPeriodic(ctx, T, "priya.nair", "funds_securities", "", date)
	if err != nil || r.Status != "Done" || !strings.Contains(r.Summary, "sent") {
		t.Fatalf("run %+v %v", r, err)
	}
	r, err = o.RunPeriodic(ctx, T, "priya.nair", "complaints_disclosure", "", date)
	if err != nil || len(r.Files) != 1 {
		t.Fatalf("disclosure %+v %v", r, err)
	}
	if _, err := o.RunPeriodic(ctx, T, "meera.iyer", "funds_securities", "", date); err == nil {
		t.Fatal("branch user ran periodic work")
	}
	// Month-end postings follow Autopilot's policy whoever starts them: a
	// maker cannot post them, and the unattended run asks a checker.
	if _, err := o.RunPeriodic(ctx, T, "priya.nair", "month_end", "", date); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker posted month end: %v", err)
	}
	sum, err := o.RunDuePeriodic(ctx, T, date)
	t.Logf("due: %s %v", sum, err)
	ps, _ = o.Periodic(ctx, T, date)
	for _, p := range ps {
		t.Logf("%-22s %-10s %-20s %s", p.ID, p.Period, p.Status, p.Note)
		if p.ID == "funds_securities" && p.Status != "Done" {
			t.Fatalf("weekly not done: %+v", p)
		}
		if p.ID == "month_end" && p.Status != "Waiting for approval" {
			t.Fatalf("month end did not wait for a checker: %+v", p)
		}
		if p.ID == "bsda_review" && p.Status != "Waiting for approval" && !strings.Contains(p.Note, "market data") {
			t.Fatalf("bsda not proposed: %+v", p)
		}
	}
}

func TestMonthEndApprovalQueued(t *testing.T) {
	o, ctx := notesReady(t)
	if _, err := o.RunPeriodic(ctx, T, SystemActor, "month_end", "", "2026-10-19"); err != nil {
		t.Fatal(err)
	}
	ws, _ := o.WorkItems(ctx, T, "")
	found := false
	for _, w := range ws {
		found = found || (w.Proposed != nil && w.Proposed.Kind == "monthly.post" && w.Proposed.Args["month"] == "2026-09")
	}
	if !found {
		t.Fatal("no approval in the work queue")
	}
	// The RAS duty in the compliance calendar is dated by Periodic work.
	ds, _ := o.Duties(ctx, T, "2026-10-19")
	for _, d := range ds {
		if d.ID == "OB-RAS" && d.Task != "ras" {
			t.Fatalf("RAS duty not linked: %+v", d)
		}
	}
}

func mustClient(t *testing.T, o *Ops, ucc string) Client {
	t.Helper()
	c, err := o.Client(t.Context(), T, ucc)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func upsertBO(o *Ops, ctx context.Context, b BOAccount) error {
	return docs.Upsert(ctx, o.Docs, T, cBO, b.UCC, b, "test")
}
