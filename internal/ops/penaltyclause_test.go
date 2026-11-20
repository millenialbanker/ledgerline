package ops

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// A hedge-break penalty is passed to a client only once the firm has
// recorded the date the risk policy clause was sent to clients, and only
// from that date + 7 days (NSE/INSP/64315). Until then proposing and
// approving both refuse and the penalty stays the firm's.
func TestPenaltyHedgeBreakClauseDate(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 17, 10, 0, 0, 0, ist) }
	if DefaultSettings().RiskPolicyClauseSentOn != "" {
		t.Fatal("the clause date is set by default")
	}
	rep, err := o.Margins(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.HedgeBreakBar, "not set") {
		t.Fatalf("margin report bar %q", rep.HedgeBreakBar)
	}
	var u string
	for _, r := range rep.Rows {
		if r.Penalty.Round(money.UnitPaisa) > 0 {
			u = r.UCC
			break
		}
	}
	if u == "" {
		t.Fatal("no client with a penalty in the sample day")
	}
	set, _ := o.Settings(ctx, T)
	set.PenaltyPassThrough = true
	if err := docs.Upsert(ctx, o.Docs, T, cSettings, "main", set, "test"); err != nil {
		t.Fatal(err)
	}
	clause := func(d string) error {
		t.Helper()
		s, _ := o.Settings(ctx, T)
		s.RiskPolicyClauseSentOn = d
		return o.SaveSettings(ctx, T, "admin", s)
	}
	// The setting is a real date, not after today.
	if err := clause("17-10-2026"); err == nil {
		t.Fatal("a malformed date accepted")
	}
	if err := clause("2026-10-18"); err == nil || !strings.Contains(err.Error(), "after today") {
		t.Fatalf("a date after today: %v", err)
	}

	// The decision item says a hedge break is not possible yet.
	if _, err := o.PreparePenalties(ctx, T, "anita.rao", D); err != nil {
		t.Fatal(err)
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	found := false
	for _, w := range ws {
		if w.Key == wPenaltyPass+":"+penaltyPassID(D, u) {
			found = strings.Contains(w.Detail, "a hedge break cannot be")
		}
	}
	if !found {
		t.Fatal("decision item without the hedge-break note")
	}

	ev, err := o.UploadPenaltyEvidence(ctx, T, "vikram.shah", "policy-extract.pdf", []byte("%PDF-1.4\n% policy\n"))
	if err != nil {
		t.Fatal(err)
	}
	hedge := func(notified string) PenaltyPassRequest {
		return PenaltyPassRequest{Date: D, UCC: u, Reason: PenaltyHedgeBreak, PolicyClause: "RMS 7.3", PolicyNotifiedOn: notified, Evidence: []string{ev.ID}}
	}
	// Not set: refused, even with a date given for this penalty.
	if _, err := o.ProposePenaltyPass(ctx, T, "vikram.shah", hedge(addDays(D, -30))); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "not set in Settings") || !strings.Contains(err.Error(), "firm's") {
		t.Fatalf("hedge break with no clause date: %v", err)
	}
	// Sent less than 7 days before the penalty: refused.
	if err := clause(addDays(D, -6)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ProposePenaltyPass(ctx, T, "vikram.shah", hedge("")); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "only from "+fmtDay(addDays(D, 1))) {
		t.Fatalf("hedge break 6 days after the clause: %v", err)
	}
	// The existing, stricter rule stays: the date given for the penalty must
	// also be 7 days before it.
	if err := clause(addDays(D, -10)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ProposePenaltyPass(ctx, T, "vikram.shah", hedge(addDays(D, -3))); err == nil || !strings.Contains(err.Error(), "at least 7 days") {
		t.Fatalf("notified 3 days before: %v", err)
	}
	// Exactly 7 days: allowed; the firm's date is used when none is given.
	if err := clause(addDays(D, -7)); err != nil {
		t.Fatal(err)
	}
	if rep, _ = o.Margins(ctx, T, D); rep.HedgeBreakBar != "" || rep.ClauseSentOn != addDays(D, -7) {
		t.Fatalf("margin report %q %q", rep.HedgeBreakBar, rep.ClauseSentOn)
	}
	p, err := o.ProposePenaltyPass(ctx, T, "vikram.shah", hedge(""))
	if err != nil || p.PolicyNotifiedOn != addDays(D, -7) || p.Status != "Waiting for approval" {
		t.Fatalf("propose %+v %v", p, err)
	}

	// The date is cleared before approval: approving refuses, by hand or
	// through the approval work item; the penalty stays the firm's.
	if err := clause(""); err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecidePenaltyPass(ctx, T, "farhan.khan", p.ID, true, ""); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "not set in Settings") {
		t.Fatalf("approved with no clause date: %v", err)
	}
	ws, _ = o.WorkItems(ctx, T, "Open")
	for _, w := range ws {
		if w.Key == wPenaltyPass+":approve:"+p.ID {
			if _, err := o.CloseWorkItem(ctx, T, "farhan.khan", w.ID, true, ""); err == nil {
				t.Fatal("approval work item posted a hedge-break penalty with no clause date")
			}
		}
	}
	st, _ := o.App.Ledger.Statement(ctx, T, ledger.ClientAccount(u), "", D)
	for _, l := range st.Lines {
		if strings.Contains(l.Narration, "penalty") {
			t.Fatalf("penalty posted: %+v", l)
		}
	}
	if p, err = o.DecidePenaltyPass(ctx, T, "farhan.khan", p.ID, false, "clause not sent"); err != nil || p.Status != "Rejected" {
		t.Fatalf("reject %+v %v", p, err)
	}

	// Set again: proposed and approved by a different person, it is posted.
	if err := clause(addDays(D, -8)); err != nil {
		t.Fatal(err)
	}
	if p, err = o.ProposePenaltyPass(ctx, T, "vikram.shah", hedge("")); err != nil {
		t.Fatal(err)
	}
	if p, err = o.DecidePenaltyPass(ctx, T, "farhan.khan", p.ID, true, ""); err != nil || p.Status != "Posted" || p.Entry == "" {
		t.Fatalf("approve %+v %v", p, err)
	}
}
