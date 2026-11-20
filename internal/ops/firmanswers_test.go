package ops

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// A firm's answers entered as its profile at go-live (not built in): no
// MTF, no algo trading, internet and mobile trading, every client settled
// quarterly on the Friday of each date, no AI tools outside Ledgerline. The
// calendar follows each answer.
func TestFirmAnswersDriveCalendar(t *testing.T) {
	o, ctx, _ := aiFirm(t) // Ledgy AI switched on 20 October 2026
	// Every sample client on quarterly settlement to start with.
	cl, _ := o.ClientMap(ctx, T)
	for u, c := range cl {
		if c.RAS == RASKindMonthly {
			docs.Update(ctx, o.Docs, T, cClient, u, "t", func(x *Client) error { x.RAS = RASKindQuarterly; return nil })
		}
	}
	no := false
	if _, err := o.SaveFirmProfile(ctx, T, "vikram.shah", FirmProfile{CDSL: AnswerYes, NSDL: AnswerNo, Algo: AnswerNo, IBT: AnswerYes, OtherAI: AnswerNo, CSCRF: "small", MTF: &no}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveFirmProfile(ctx, T, "vikram.shah", FirmProfile{OtherAI: "maybe"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other AI tools: %v", err)
	}
	fv, err := o.FirmProfileFor(ctx, T, "vikram.shah")
	if err != nil {
		t.Fatal(err)
	}
	facts := map[string]FirmFact{}
	for _, f := range fv.Facts {
		facts[f.Key] = f
	}
	if f := facts["other_ai"]; f.Value != "No" || !f.Known || fv.Profile.OtherAI != AnswerNo {
		t.Fatalf("other AI fact %+v", f)
	}
	if f := facts["ibt"]; f.Value != "Yes" || !strings.Contains(f.Label, "mobile") {
		t.Fatalf("internet and mobile trading %+v", f)
	}

	// No MTF: the net worth certificate is due 30 May and 29 November.
	if r := resolveRule("NETWORTH", o.CompProfile(ctx, T)); r != "05-30,11-29" {
		t.Fatalf("net worth without MTF: %s", r)
	}
	if x := dutyOf(t, o, "OB-NETWORTH", "2026-11-02"); x.NextDue != "2026-11-29" {
		t.Fatalf("net worth duty %+v", x)
	}
	yes := true
	if _, err := o.SaveFirmProfile(ctx, T, "vikram.shah", FirmProfile{CDSL: AnswerYes, NSDL: AnswerNo, Algo: AnswerNo, IBT: AnswerYes, OtherAI: AnswerNo, CSCRF: "small", MTF: &yes}); err != nil {
		t.Fatal(err)
	}
	if x := dutyOf(t, o, "OB-NETWORTH", "2026-11-02"); x.NextDue != "2026-11-14" {
		t.Fatalf("net worth with MTF %+v", x)
	}
	if _, err := o.SaveFirmProfile(ctx, T, "vikram.shah", FirmProfile{CDSL: AnswerYes, NSDL: AnswerNo, Algo: AnswerNo, IBT: AnswerYes, OtherAI: AnswerNo, CSCRF: "small", MTF: &no}); err != nil {
		t.Fatal(err)
	}

	// Internet trading, no algo, small RE: the cyber audit is half-yearly,
	// not yearly. A qualified RE is half-yearly even without IBT or algo.
	verdict := func(id string) string {
		x := dutyOf(t, o, id, "2026-11-02")
		if x.Applicability == nil {
			return ""
		}
		return x.Applicability.Verdict
	}
	if verdict("CC-T-CYBER-AUDIT-HALF") != ApplyYes || verdict("OB-CYBER") != ApplyNo {
		t.Fatalf("cyber audit: half %s, yearly %s", verdict("CC-T-CYBER-AUDIT-HALF"), verdict("OB-CYBER"))
	}
	f := firmFacts{Member: map[string]bool{"NSE": true}, IBT: triNo, Algo: triNo, CSCRF: "qualified"}
	cat, _ := dutyCatalogue()
	for _, d := range cat.Duties {
		if d.ID == "CC-T-CYBER-AUDIT-HALF" && dutyApplies(d.Applies, f).Verdict != ApplyYes {
			t.Errorf("a qualified RE without IBT or algo: %+v", dutyApplies(d.Applies, f))
		}
		if d.ID == "OB-CYBER" && dutyApplies(d.Applies, f).Verdict != ApplyNo {
			t.Errorf("yearly audit for a qualified RE")
		}
	}

	// No algo: the AI & ML report is yearly (due 15 April), unless the
	// firm chooses a cycle.
	if p := o.discProfile(ctx, T); p.AIMLCycle != "yearly" || p.AIMLCycleChoice != "" || !strings.Contains(p.AIMLCycleWhy, "algo") {
		t.Fatalf("AI & ML cycle %+v", p)
	}
	if x := dutyOf(t, o, "OB-AIML-NSE", "2026-11-02"); x.Rule != "04-15" || x.Frequency != "Yearly" {
		t.Fatalf("AI & ML duty %+v", x)
	}
	p := o.discProfile(ctx, T)
	p.AIMLCycle = p.AIMLCycleChoice
	p.InfoSecName = "Information security officer"
	if _, err := o.SaveDisclosureProfile(ctx, T, "vikram.shah", p); err != nil {
		t.Fatal(err)
	}
	if p := o.discProfile(ctx, T); p.AIMLCycle != "yearly" || p.AIMLCycleChoice != "" {
		t.Fatalf("saving the details froze the cycle: %+v", p)
	}

	// No AI tools outside Ledgerline: nothing to report besides Ledgerline.
	o.Now = func() time.Time { return time.Date(2027, 4, 2, 10, 0, 0, 0, ist) }
	v, err := o.Disclosure(ctx, T, "vikram.shah", DiscAIML, "")
	notes := strings.Join(v.Notes, " ")
	if err != nil || !v.OnlyLedgerline || v.Answer != "Yes" || len(v.Systems) != 1 || !strings.Contains(notes, "nothing to report besides Ledgerline") {
		t.Fatalf("only Ledgerline: %v %s %v %v", v.OnlyLedgerline, v.Answer, v.Systems, v.Notes)
	}
	// A tool the firm adds contradicts the answer: the report says so.
	if _, err := o.SaveAITool(ctx, T, "vikram.shah", AISystem{Name: "Chat assistant", Purpose: "Drafting emails", OrderExecution: "N", Discretionary: "N", Advice: "N", CyberSecurity: "N", Active: true}); err != nil {
		t.Fatal(err)
	}
	v, _ = o.Disclosure(ctx, T, "vikram.shah", DiscAIML, "")
	if v.OnlyLedgerline || !strings.Contains(strings.Join(v.Notes, " "), "correct the profile or the inventory") {
		t.Fatalf("contradiction not shown: %v", v.Notes)
	}
	o.Now = func() time.Time { return time.Date(2026, 10, 20, 11, 0, 0, 0, ist) }

	// Every client quarterly, settled on the Friday: the monthly duty does
	// not apply; the profile says Friday for every date.
	cp := o.CompProfile(ctx, T)
	cp.RASDay, cp.RASClients = "Saturday", RASClientsQuarterly
	if cp, err = o.SaveCompProfile(ctx, T, "vikram.shah", cp); err != nil {
		t.Fatal(err)
	}
	for _, r := range cp.RAS {
		if wd, _ := time.Parse("2006-01-02", r.RunOn); wd.Weekday() != time.Saturday {
			t.Fatalf("%s %s settles on %s", r.Kind, r.Period, r.RunOn)
		}
	}
	cp.RASDay = RASDayFriday
	if cp, err = o.SaveCompProfile(ctx, T, "vikram.shah", cp); err != nil {
		t.Fatal(err)
	}
	for _, r := range cp.RAS {
		if wd, _ := time.Parse("2006-01-02", r.RunOn); wd.Weekday() != time.Friday {
			t.Fatalf("%s %s settles on %s", r.Kind, r.Period, r.RunOn)
		}
	}
	if m, q := o.RASDates(ctx, T, "2026-10-20"); m != "2026-11-06" || q != "2027-01-01" {
		t.Fatalf("next dates %s %s", m, q)
	}
	for _, bad := range []ComplianceProfile{{RAS: cp.RAS, RASDay: "sunday"}, {RAS: cp.RAS, RASClients: "weekly"}} {
		if _, err := o.SaveCompProfile(ctx, T, "vikram.shah", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v accepted: %v", bad, err)
		}
	}
	if x := dutyOf(t, o, "OB-RAS-M", "2026-10-20"); x.Applicability == nil || x.Applicability.Verdict != ApplyNo || x.Status != "Doesn't apply" {
		t.Fatalf("monthly settlement for a firm settling everyone quarterly: %+v", x.Applicability)
	}
	if x := dutyOf(t, o, "OB-RAS", "2026-10-20"); x.Status == "Doesn't apply" {
		t.Fatal("the quarterly settlement duty went too")
	}
	fv, _ = o.FirmProfileFor(ctx, T, "vikram.shah")
	for _, f := range fv.Facts {
		if f.Key == "ras" && (!strings.Contains(f.Value, "All clients quarterly") || !strings.Contains(f.Value, "Friday")) {
			t.Fatalf("RAS fact %+v", f)
		}
	}
	// A client record still on monthly: the duty applies, and the profile warns.
	var some string
	for u := range cl {
		some = u
		break
	}
	docs.Update(ctx, o.Docs, T, cClient, some, "t", func(x *Client) error { x.RAS = RASKindMonthly; return nil })
	if x := dutyOf(t, o, "OB-RAS-M", "2026-10-20"); x.Applicability == nil || x.Applicability.Verdict != ApplyYes {
		t.Fatalf("a monthly client: %+v", x.Applicability)
	}
	pv, _ := o.CompProfileFor(ctx, T, "vikram.shah", "2026-10-20")
	if !strings.Contains(strings.Join(pv.Warnings, " "), some) {
		t.Fatalf("no warning about %s: %v", some, pv.Warnings)
	}

	// The system audit cycle against the algo answer.
	cp.SysAudit = SysAuditHalfYearly
	if _, err := o.SaveCompProfile(ctx, T, "vikram.shah", cp); err != nil {
		t.Fatal(err)
	}
	pv, _ = o.CompProfileFor(ctx, T, "vikram.shah", "2026-10-20")
	if !strings.Contains(strings.Join(pv.Warnings, " "), "does not run or offer algo trading") {
		t.Fatalf("half-yearly system audit without algo: %v", pv.Warnings)
	}
}

// A duty the firm decides does not apply: marked with the reason and who
// decided, audited, not chased, kept through edits, and only for this firm;
// taken back, it is on the calendar again with the history kept.
func TestDutyNotApplicable(t *testing.T) {
	o, ctx := setup(t)
	const id = "OB-BANKBAL"
	x := dutyOf(t, o, id, D)
	// The members' daily upload was withdrawn (NSE/INSP/59044); the yearly
	// check of the banks' reporting stays, and says so.
	refs := ""
	for _, r := range x.Refs {
		refs += r.No + " "
	}
	if !strings.Contains(x.Title, "stopped from 30 October 2023") || !strings.Contains(refs, "NSE/INSP/59044") || x.Status == "Doesn't apply" {
		t.Fatalf("bank balance duty %+v", x)
	}
	if _, err := o.SetDutyApplies(ctx, T, "priya.nair", D, id, DutyAppliesReq{Reason: "Not for us"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker decided: %v", err)
	}
	if _, err := o.SetDutyApplies(ctx, T, "vikram.shah", D, id, DutyAppliesReq{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no reason: %v", err)
	}
	if _, err := o.SetDutyApplies(ctx, T, "vikram.shah", D, id, DutyAppliesReq{Applies: true}); !errors.Is(err, ErrState) {
		t.Fatalf("taken back before it was marked: %v", err)
	}
	why := "The exchange stopped the bank balance upload (NSE/INSP/59044)"
	x, err := o.SetDutyApplies(ctx, T, "vikram.shah", D, id, DutyAppliesReq{Reason: why})
	if err != nil {
		t.Fatal(err)
	}
	a := x.Applicability
	if x.Status != "Doesn't apply" || x.NextDue != "" || x.Overdue || a == nil || !a.Overridden || a.By != "vikram.shah" || !strings.Contains(a.Reason, why) || a.Rules != ApplyYes || x.NotApplicable == nil {
		t.Fatalf("marked %+v %+v", x, a)
	}
	evs, _ := o.App.Audit.List(ctx, T, 0)
	found := false
	for _, e := range evs {
		found = found || (e.Action == "obligation.not_applicable" && e.Object == id && e.Actor == "vikram.shah" && fmt.Sprint(e.Detail["reason"]) == why)
	}
	if !found {
		t.Fatal("not audited with the reason")
	}
	if _, err := o.DoneDuty(ctx, T, "vikram.shah", D, id, "done"); !errors.Is(err, ErrState) {
		t.Fatalf("done though not applicable: %v", err)
	}
	// Editing the duty keeps the decision.
	x.Evidence = "Bank confirmations"
	if _, err := o.SaveDuty(ctx, T, "vikram.shah", x); err != nil {
		t.Fatal(err)
	}
	if x = dutyOf(t, o, id, D); x.NotApplicable == nil || x.Status != "Doesn't apply" {
		t.Fatalf("edit lost the decision %+v", x)
	}
	// Another firm still has the duty.
	if y := dutyOf2(t, o, "other-firm", id, D); y.NotApplicable != nil || y.Status == "Doesn't apply" {
		t.Fatalf("another firm lost the duty: %+v", y)
	}
	// Taken back: on the calendar again, history kept.
	if x, err = o.SetDutyApplies(ctx, T, "vikram.shah", D, id, DutyAppliesReq{Applies: true}); err != nil {
		t.Fatal(err)
	}
	if x.NotApplicable != nil || x.Status == "Doesn't apply" || len(x.NAHistory) != 1 || x.NAHistory[0].Ended == "" || x.NAHistory[0].EndedBy != "vikram.shah" || x.NAHistory[0].Reason != why {
		t.Fatalf("taken back %+v", x)
	}
}

// dutyOf2 is dutyOf for another tenant.
func dutyOf2(t *testing.T, o *Ops, tenant, id, date string) Duty {
	t.Helper()
	ds, err := o.Duties(t.Context(), tenant, date)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("%s not on %s's calendar", id, tenant)
	return Duty{}
}
