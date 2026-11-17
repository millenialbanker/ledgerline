package ops

import (
	"testing"

	"ledgerline/internal/money"
)

// A credit balance with no transaction for 30 calendar days is returned on
// the upcoming monthly settlement date of the exchanges' calendar, whatever
// the client chose (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/1), under the RAS
// policy and its checker approval.
func TestRASIdle30(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	v, err := o.RAS(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	var idle []RASRow
	for _, r := range v.Rows {
		if r.Idle30 && r.Payout.Round(money.UnitPaisa) > 0 {
			idle = append(idle, r)
		}
		if r.Idle30 && r.Retention.Sign() > 0 {
			t.Fatalf("%s has open positions but is idle", r.UCC)
		}
	}
	if len(idle) < 2 {
		t.Fatalf("sample day has %d idle credit balances", len(idle))
	}
	x := idle[0]
	prof := o.CompProfile(ctx, T)
	win, ok := prof.rasWindow(RASKindMonthly, x.IdleFrom)
	if !ok || x.IdleFrom != addDays(x.LastActivity, 30) || x.IdleDue != win.Last() || x.SettlesOn != win.RunOn {
		t.Fatalf("dates: %+v (monthly window %+v)", x, win)
	}
	// The upcoming monthly settlement of the FY 2026-27 calendar: the
	// Friday the firm settles, due by the end of the Friday/Saturday window.
	for _, c := range []struct{ from, settle, due string }{
		{"2026-10-20", "2026-11-06", "2026-11-07"},
		{"2026-11-07", "2026-11-07", "2026-11-07"}, // the 30 days end on the Saturday
		{"2026-11-08", "2026-12-04", "2026-12-05"},
	} {
		if s, d := idleRASDates(set, prof, c.from); s != c.settle || d != c.due {
			t.Errorf("idle from %s: settle %s due %s, want %s %s", c.from, s, d, c.settle, c.due)
		}
	}
	// Fresh funds restart the 30 days.
	y := idle[1]
	rc, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: D, UCC: y.UCC, Mode: "NEFT", Ref: "UTR0000123", Amount: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideReceipt(ctx, T, "farhan.khan", rc.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	v, _ = o.RAS(ctx, T, D)
	for _, r := range v.Rows {
		if r.UCC == y.UCC && r.Idle30 {
			t.Fatalf("idle after fresh funds: %+v", r)
		}
	}
	// Suggest: the morning check asks a checker; Autopilot's RAS run does
	// not clear the request.
	if _, err := o.SetAutoPolicy(ctx, T, "admin", AutoPolicy{ID: "ras", Mode: ModeSuggest}); err != nil {
		t.Fatal(err)
	}
	run, err := o.RunPeriodic(ctx, T, SystemActor, "ras_idle", D, D)
	if err != nil || run.Status != "Waiting for approval" {
		t.Fatalf("suggest: %+v %v", run, err)
	}
	if bs, _ := o.RASBatches(ctx, T, ""); len(bs) != 0 {
		t.Fatal("settled without approval")
	}
	if _, err := o.RunStage(ctx, T, "admin", D, "eod", "ras"); err != nil {
		t.Fatal(err)
	}
	var item *WorkItem
	ws, _ := o.WorkItems(ctx, T, "Open")
	for i := range ws {
		if ws[i].Key == wRASIdle+":ras30:"+D {
			item = &ws[i]
		}
	}
	if item == nil || item.Proposed == nil || item.Proposed.Kind != "ras.settle" {
		t.Fatalf("no approval: %+v", item)
	}
	if _, err := o.CloseWorkItem(ctx, T, "priya.nair", item.ID, true, ""); err == nil {
		t.Fatal("maker approved the settlement")
	}
	if _, err := o.CloseWorkItem(ctx, T, "farhan.khan", item.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	bs, _ := o.RASBatches(ctx, T, D)
	paid := map[string]bool{}
	for _, b := range bs {
		for _, it := range b.Items {
			paid[it.UCC] = true
		}
	}
	if !paid[x.UCC] || paid[y.UCC] {
		t.Fatalf("paid %v", paid)
	}
	// Quarterly clients who are not idle wait for their date.
	for _, r := range v.Rows {
		if !r.Idle30 && !r.Inactive && r.Preference == "Quarterly" && paid[r.UCC] {
			t.Fatalf("%s settled before the quarterly date", r.UCC)
		}
	}
	due, _, _ := o.IdleRASDue(ctx, T, D)
	if len(due) != 0 {
		t.Fatalf("%d still due", len(due))
	}
	if run, err := o.RunPeriodic(ctx, T, SystemActor, "ras_idle", D, D); err != nil || run.Status != "Done" {
		t.Fatalf("after: %+v %v", run, err)
	}
}
