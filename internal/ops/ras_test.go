package ops

import (
	"testing"

	"ledgerline/internal/money"
)

// A credit balance with no transaction for 30 calendar days is returned
// within three working days, not on the next settlement date
// (NSE/INSP/53820), under the RAS policy and its checker approval.
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
	if x.IdleFrom != addDays(x.LastActivity, 30) || x.IdleDue != addWorkingDays(set, x.IdleFrom, 3) || x.SettlesOn != x.IdleDue || x.SettlesOn == set.RASMonthly {
		t.Fatalf("dates: %+v", x)
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
