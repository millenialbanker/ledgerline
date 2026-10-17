package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/money"
	"ledgerline/internal/sample"
)

func TestDetectSource(t *testing.T) {
	o, _ := setup(t)
	for _, f := range sample.Generate(D).Files {
		if got := o.DetectSource(f.Name, f.Data); got != f.Source {
			t.Errorf("%s: detected %q, want %q", f.Name, got, f.Source)
		}
	}
}

func TestAutopilotDay(t *testing.T) {
	o, ctx := setup(t)
	// Caps.
	if _, err := o.SetAutoPolicy(ctx, T, "admin", AutoPolicy{ID: "unidentified", Mode: ModeAuto}); err == nil || !strings.Contains(err.Error(), "at most Suggest") {
		t.Fatalf("cap not enforced: %v", err)
	}
	if _, err := o.SetAutoPolicy(ctx, T, "priya.nair", AutoPolicy{ID: "payouts", Mode: ModeOff}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker changed policy: %v", err)
	}
	if _, err := o.SetAutoPolicy(ctx, T, "admin", AutoPolicy{ID: "square_off", Mode: ModeAuto, Params: map[string]string{"at": "15:00"}}); err != nil {
		t.Fatal(err)
	}
	var done, queued int
	for _, st := range []string{"bod", "intraday", "preclose", "eod"} {
		runs, err := o.RunStage(ctx, T, SystemActor, D, st, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range runs {
			if r.Error != "" {
				t.Errorf("%s: %s", r.Policy, r.Error)
			}
			done += r.Done
			queued += r.Queued
			t.Logf("%-14s %-8s done %3d queued %2d  %s", r.Policy, r.Mode, r.Done, r.Queued, r.Note)
		}
	}
	if done == 0 {
		t.Fatal("nothing done automatically")
	}
	st, _ := o.EOD(ctx, T, D)
	for _, c := range st.Checks {
		if c.Name == "Bills posted" && !c.OK {
			t.Fatal("billing did not run")
		}
	}
	ns, _ := o.Notes(ctx, T, D)
	if len(ns) == 0 {
		t.Fatal("no contract notes")
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	roles := map[string]int{}
	for _, w := range ws {
		roles[w.Role]++
	}
	t.Logf("done %d, queued %d, open items by role %v", done, queued, roles)
	// Clear every break the system prepared, then close the day again.
	for _, w := range ws {
		if w.Process == "recon" && w.Proposed != nil {
			if _, err := o.CloseWorkItem(ctx, T, "farhan.khan", w.ID, true, ""); err != nil {
				t.Fatalf("%s: %v", w.ID, err)
			}
		}
		if w.Process == "recon" && w.Proposed == nil {
			if _, err := o.CloseWorkItem(ctx, T, "farhan.khan", w.ID, false, ""); err == nil {
				t.Fatal("dismissed without a note")
			}
		}
	}
	v, err := o.Autopilot(ctx, T, D)
	if err != nil || v.Done == 0 || v.STP <= 0 || len(v.HumanOnly) == 0 {
		t.Fatalf("view: %+v %v", v.STP, err)
	}
}

func TestAutoPayouts(t *testing.T) {
	o, ctx := setup(t)
	m, _, _ := o.Market(ctx, T, D)
	o.App.RunBilling(ctx, T, "priya.nair", D, m.SettlementPrices())
	var ucc string
	cs, _ := o.ClientMap(ctx, T)
	for _, u := range sortedKeys(cs) {
		if w, _ := o.Withdrawable(ctx, T, D, u); w.Available > 50000000 && cs[u].Risk != "High" && !cs[u].Blocked {
			ucc = u
			break
		}
	}
	if ucc == "" {
		t.Fatal("no client with free funds")
	}
	small, err := o.RequestPayout(ctx, T, "priya.nair", D, ucc, 1000000, "Head office")
	if err != nil || small.Status != "Approved" {
		t.Fatalf("small payout: %+v %v", small, err)
	}
	big, err := o.RequestPayout(ctx, T, "priya.nair", D, ucc, money.Amount(40000000), "Head office")
	if err != nil || big.Status != "Requested" {
		t.Fatalf("big payout: %+v %v", big, err)
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	var item *WorkItem
	for i := range ws {
		if ws[i].Process == "payouts" && strings.Contains(ws[i].Title, big.ID) {
			item = &ws[i]
		}
	}
	if item == nil || item.Proposed == nil {
		t.Fatal("big payout not queued with a proposal")
	}
	if _, err := o.CloseWorkItem(ctx, T, "priya.nair", item.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker approved the queued payout: %v", err)
	}
	if _, err := o.CloseWorkItem(ctx, T, "farhan.khan", item.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	p, _ := o.PayoutRequests(ctx, T, D)
	for _, x := range p {
		if x.ID == big.ID && x.Status != "Approved" {
			t.Fatalf("not approved: %s", x.Status)
		}
	}
}
