package ops

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The 5-minute test and its exclusions, as a pure function.
func TestGlitchClassification(t *testing.T) {
	now := time.Date(2026, 11, 2, 11, 0, 0, 0, ist)
	g := Glitch{Started: now.Add(-3 * time.Minute).Format(time.RFC3339), Functions: []string{"Login"}, Kind: "Stoppage"}
	if _, rep, why := glitchClass(g, now); rep || !strings.Contains(why, "reaches 5") {
		t.Fatalf("3 minutes and going on: %v %s", rep, why)
	}
	g.Started = now.Add(-6 * time.Minute).Format(time.RFC3339)
	if m, rep, _ := glitchClass(g, now); !rep || m != 6 {
		t.Fatalf("6 minutes: %v %d", rep, m)
	}
	g.Ended = now.Add(-2 * time.Minute).Format(time.RFC3339)
	if _, rep, why := glitchClass(g, now); rep || !strings.Contains(why, "under 5") {
		t.Fatalf("ended after 4 minutes: %v %s", rep, why)
	}
	g.Ended, g.Exclusion = "", "mii"
	if _, rep, why := glitchClass(g, now); rep || !strings.Contains(why, "exchange, clearing corporation") {
		t.Fatalf("exchange's own problem: %v %s", rep, why)
	}
	g.Exclusion, g.OutsideHours = "", true
	if _, rep, _ := glitchClass(g, now); rep {
		t.Fatal("outside trading hours counted")
	}
	g.OutsideHours, g.Functions = false, nil
	if _, rep, _ := glitchClass(g, now); rep {
		t.Fatal("nothing affected counted")
	}
	// MCX's exemption limit: the lower of 2% and 2,000.
	if exemptionLimit(50000) != 1000 || exemptionLimit(500000) != 2000 || exemptionLimit(149) != 3 {
		t.Fatalf("limits %d %d %d", exemptionLimit(50000), exemptionLimit(500000), exemptionLimit(149))
	}
	// The fallback when the firm modules don't know ibt:10k.
	if v, _ := glitchFallback(triNo, 20000, "2026-03-31"); v != triNo {
		t.Fatal("no online trading")
	}
	if v, _ := glitchFallback(triYes, 9000, "2026-03-31"); v != triNo {
		t.Fatal("under 10,000")
	}
	if v, _ := glitchFallback(triYes, 10001, "2026-03-31"); v != triYes {
		t.Fatal("over 10,000")
	}
	if v, _ := glitchFallback(triUnknown, 20000, "2026-03-31"); v != triUnknown {
		t.Fatal("not answered")
	}
}

// A glitch is recorded, becomes reportable, runs its clocks, is reported,
// clients are told through the client messages, the drafts carry the
// facts, and it links to a security incident. A firm the framework does
// not cover cannot record one.
func TestGlitchRegisterFlow(t *testing.T) {
	o, ctx := setup(t)
	now := time.Date(2026, 11, 2, 10, 30, 0, 0, ist) // a Monday
	o.Now = func() time.Time { return now }
	sc, err := o.Glitches(ctx, T, "vikram.shah")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Applies == "no" {
		t.Fatalf("the sample firm's answer on online trading is not given, so not sure: %+v", sc)
	}
	if _, err := o.Glitches(ctx, T, "priya.nair"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker saw the register: %v", err)
	}
	in := GlitchInput{Title: "Trading app login failing", System: "Trading app", Vendor: "OMS vendor", Functions: []string{"Login", "Order placement"}, Kind: "Stoppage",
		Started: "2026-11-02T10:00", Exchanges: []string{"NSE", "MCX"}}
	v, err := o.RecordGlitch(ctx, T, "anita.rao", in)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Reportable || v.Minutes != 30 || len(v.Clocks) != 5 {
		t.Fatalf("view %+v", v)
	}
	due := map[string]GlitchClock{}
	for _, c := range v.Clocks {
		due[c.Stage] = c
	}
	if !strings.HasPrefix(due["nse"].Due, "2026-11-02T11:00") || !strings.HasPrefix(due["exchanges"].Due, "2026-11-02T12:00") || !strings.HasPrefix(due["clients"].Due, "2026-11-02T12:00") {
		t.Fatalf("hour clocks %+v", due)
	}
	if !strings.HasPrefix(due["prelim"].Due, "2026-11-03") {
		t.Fatalf("T+1: %s", due["prelim"].Due)
	}
	if d := due["rca"].Due[:10]; d < "2026-11-19" || d > "2026-11-24" {
		t.Fatalf("14 working days: %s", d)
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	n := 0
	for _, w := range ws {
		if strings.HasPrefix(w.ID, "W-glitch-"+v.ID) {
			n++
		}
	}
	if n != 5 {
		t.Fatalf("work items for the clocks: %d", n)
	}
	// NSE told late; MCX told in time.
	now = time.Date(2026, 11, 2, 11, 20, 0, 0, ist)
	if _, err := o.ReportGlitch(ctx, T, "vikram.shah", v.ID, GlitchReport{Stage: "nse", How: "Portal"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no reference: %v", err)
	}
	if _, err := o.ReportGlitch(ctx, T, "vikram.shah", v.ID, GlitchReport{Stage: "exchanges", To: []string{"BSE"}, How: "Email", Reference: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an exchange not affected: %v", err)
	}
	v, err = o.ReportGlitch(ctx, T, "vikram.shah", v.ID, GlitchReport{Stage: "nse", How: "Portal", Reference: "NSE-GL-1"})
	if err != nil {
		t.Fatal(err)
	}
	v, err = o.ReportGlitch(ctx, T, "vikram.shah", v.ID, GlitchReport{Stage: "exchanges", To: []string{"MCX"}, How: "Email", Reference: "mail of 11:20"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Clocks {
		if c.Stage == "nse" && (!c.Done || !c.Late) || c.Stage == "exchanges" && (!c.Done || c.Late) {
			t.Fatalf("clock %+v", c)
		}
	}
	// Clients told through the client messages.
	cs, _ := o.Clients(ctx, T)
	v, err = o.NoticeGlitch(ctx, T, "vikram.shah", v.ID, "Client messages", "", []string{cs[0].UCC}, "Orders cannot be placed in the app. Call the dealing desk.", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Notices) != 1 || len(v.Notices[0].Results) == 0 {
		t.Fatalf("notice %+v", v.Notices)
	}
	ms, _ := o.Messages(ctx, T, cs[0].UCC, 20)
	found := false
	for _, m := range ms {
		found = found || m.Event == evGlitchNotice && strings.Contains(m.Body, "dealing desk")
	}
	if !found {
		t.Fatal("no glitch notice in the client's messages")
	}
	// The drafts.
	b, name, err := o.GlitchDraft(ctx, T, "vikram.shah", v.ID, "B")
	if err != nil || !strings.Contains(string(b), "Annexure B") || !strings.Contains(string(b), "OMS vendor") || !strings.HasSuffix(name, "annexure-b.txt") {
		t.Fatalf("draft B %v %s", err, b)
	}
	if _, err := o.UpdateGlitch(ctx, T, "anita.rao", v.ID, GlitchInput{Title: in.Title, System: in.System, Vendor: in.Vendor, Functions: in.Functions, Kind: in.Kind, Started: in.Started, Ended: "2026-11-02T10:50", Exchanges: in.Exchanges, RootCause: "Expired TLS certificate on the login gateway"}, ""); err != nil {
		t.Fatal(err)
	}
	b, _, _ = o.GlitchDraft(ctx, T, "vikram.shah", v.ID, "C")
	if !strings.Contains(string(b), "Expired TLS certificate") || !strings.Contains(string(b), "50 min") {
		t.Fatalf("draft C %s", b)
	}
	// Closing with reports still due needs a reason.
	if _, err := o.UpdateGlitch(ctx, T, "vikram.shah", v.ID, GlitchInput{}, GlitchClosed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("closed with the RCA due: %v", err)
	}
	// MCX's exemption evidence.
	v, err = o.GlitchExempt(ctx, T, "vikram.shah", v.ID, GlitchExemption{QuarterAvg: 50000, Affected: 400, Evidence: "Login gateway logs 10:00-10:50, checked by the internal auditor"})
	if err != nil || v.Exemption.Limit != 1000 || !v.Exemption.Below {
		t.Fatalf("exemption %v %+v", err, v.Exemption)
	}
	// Also a security incident: linked both ways.
	v, err = o.LinkGlitchIncident(ctx, T, "vikram.shah", v.ID, "", "")
	if err != nil || !strings.HasPrefix(v.Incident, "INC-") {
		t.Fatalf("link %v %+v", err, v.Incident)
	}
	inc, err := o.IncidentByID(ctx, T, "vikram.shah", v.Incident)
	if err != nil || !strings.Contains(inc.Notes[len(inc.Notes)-1].Text, v.ID) {
		t.Fatalf("incident %v %+v", err, inc.Notes)
	}
	// The calendar has the glitch's events.
	d := dutyOf(t, o, "CC-T-GLITCH-RCA", "2026-11-02")
	if len(d.Events) != 1 {
		t.Fatalf("calendar events %+v", d.Events)
	}
	// A firm without online trading: no glitches.
	if _, err := o.SaveFirmProfile(ctx, T, "vikram.shah", FirmProfile{IBT: AnswerNo, CSCRF: "self"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordGlitch(ctx, T, "anita.rao", in); !errors.Is(err, ErrState) {
		t.Fatalf("recorded where the framework does not apply: %v", err)
	}
	sc, _ = o.Glitches(ctx, T, "vikram.shah")
	if sc.Applies != "no" || len(sc.Glitches) != 1 {
		t.Fatalf("screen %+v", sc.Applies)
	}
}
