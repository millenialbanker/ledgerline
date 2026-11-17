package ops

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

// TestDutyRuleDates: the calendar's due rules date each occurrence as the
// circulars set them (trading days, the week's last trading day, the
// fortnight, the month end, trading days after each RAS date, one-time
// deadlines that stay due, event deadlines in days, working days or hours).
func TestDutyRuleDates(t *testing.T) {
	set := Settings{Holidays: []string{"2026-10-20"}} // a Tuesday off
	prof := DefaultComplianceProfile()
	first := func(rule, from, to string) dutyOcc {
		t.Helper()
		oc := dutyOccurrences(set, prof, rule, from, to, 400)
		if len(oc) == 0 {
			t.Fatalf("%s: nothing between %s and %s", rule, from, to)
		}
		return oc[0]
	}
	for _, c := range []struct{ rule, from, to, period, due string }{
		{"TDAY", "2026-10-16", "2026-10-31", "2026-10-16", "2026-10-16"},
		{"TDAY+1", "2026-10-19", "2026-10-31", "2026-10-16", "2026-10-19"}, // Friday's, due Monday
		{"TDAY+5", "2026-10-26", "2026-10-31", "2026-10-16", "2026-10-26"}, // past the holiday
		{"WEEK+3", "2026-10-19", "2026-10-31", "2026-W42", "2026-10-22"},   // Fri 16th + 3 trading days, skipping the 20th
		{"WEEKEND", "2026-10-16", "2026-10-31", "2026-W42", "2026-10-16"},
		{"FORTNIGHT", "2026-10-02", "2026-10-31", "2026-10 (1–15)", "2026-10-15"},
		{"FORTNIGHT", "2026-10-16", "2026-10-31", "2026-10 (16–end)", "2026-10-31"},
		{"MEND", "2026-11-01", "2026-11-30", "2026-11", "2026-11-30"},
		{"D10", "2026-11-01", "2026-11-30", "2026-10", "2026-11-10"},
		{"D31", "2027-02-01", "2027-02-28", "2027-01", "2027-02-28"}, // the month's last day
		{"02-28,05-31,08-31,11-30", "2026-10-17", "2027-12-31", "2026-11-30", "2026-11-30"},
		{"02-29", "2027-01-01", "2027-12-31", "2027-02-28", "2027-02-28"},
		{"RAS+10", "2026-11-07", "2026-12-31", "RAS of 2026-11-06", "2026-11-20"}, // settled Fri 6 Nov
		{"ONCE:2025-06-30", "2026-10-01", "2027-01-01", "once", "2025-06-30"},     // still due
		{"NETWORTH", "2026-10-17", "2027-12-31", "2026-11-29", "2026-11-29"},
	} {
		if oc := first(c.rule, c.from, c.to); oc.Period != c.period || oc.Due != c.due {
			t.Errorf("%s from %s: %+v, want %s due %s", c.rule, c.from, oc, c.period, c.due)
		}
	}
	if oc := dutyOccurrences(set, prof, "ONCE", "2026-10-01", "2026-10-31", 9); len(oc) != 1 || oc[0].Due != "" || oc[0].Period != "once" {
		t.Errorf("one-time without a date: %+v", oc)
	}
	if oc := dutyOccurrences(set, prof, "TDAY", "2026-10-19", "2026-10-23", 99); len(oc) != 4 {
		t.Errorf("trading days in the week with a holiday: %+v", oc)
	}
	if d := extendedDue([]DutyExtension{{Period: "2026-10-31", Due: "2026-12-31", By: "NSE circular 97/2026"}}, dutyOcc{Period: "2026-10-31", Due: "2026-10-31"}); d != "2026-12-31" {
		t.Errorf("extension: %s", d)
	}
	if oc := dutyOccurrences(set, prof, "ASNEEDED", "2026-10-01", "2026-12-31", 9); len(oc) != 0 {
		t.Errorf("as needed has no dates: %+v", oc)
	}
	for _, c := range []struct{ within, date, hhmm, due, at string }{
		{"0", "2026-10-16", "", "2026-10-16", ""},
		{"1wd", "2026-10-16", "", "2026-10-19", ""},
		{"7d", "2026-10-16", "", "2026-10-23", ""},
		{"6h", "2026-10-16", "20:30", "2026-10-17", "02:30"},
	} {
		if d, a := eventDue(set, c.within, c.date, c.hhmm); d != c.due || a != c.at {
			t.Errorf("event %s on %s %s: due %s %s, want %s %s", c.within, c.date, c.hhmm, d, a, c.due, c.at)
		}
	}
	for _, ok := range []string{"TDAY", "TDAY+5", "WEEK+3", "WEEKEND", "FORTNIGHT", "MEND", "RAS+10", "EVENT", "ONCE", "ONCE:2027-01-31", "ASNEEDED", "D07", "05-15,11-15", "RASM", "NETWORTH"} {
		if !validDutyRule(ok) {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"", "TDAY+0", "WEEK+11", "ONCE:2027-02-30", "RAS-1", "D32", "WEEKLY"} {
		if validDutyRule(bad) {
			t.Errorf("%s accepted", bad)
		}
	}
}

// TestDutyApplicability: a duty applies when every condition holds, not
// when one fails, and is "not sure" when the profile doesn't say.
func TestDutyApplicability(t *testing.T) {
	f := firmFacts{Member: map[string]bool{"NSE": true, "BSE": true}, Seg: map[string]bool{"CM": true, "FO": true}, SelfAt: map[string]bool{"NSCCL": true},
		ViaCM: map[string]string{}, Regs: map[string]bool{"ra": true}, CDSL: triYes, NSDL: triNo, MTF: triNo, Algo: triUnknown, IBT: triNo, CSCRF: "self"}
	for _, c := range []struct {
		needs   []string
		verdict string
	}{
		{[]string{"broker"}, ApplyYes},
		{[]string{"member:MCX"}, ApplyNo},
		{[]string{"cdsl"}, ApplyYes},
		{[]string{"nsdl"}, ApplyNo},
		{[]string{"dp", "nsdl"}, ApplyNo},
		{[]string{"broker", "mtf"}, ApplyNo},
		{[]string{"ibt|algo"}, ApplyUnsure}, // no IBT, algo not known
		{[]string{"ra|ia"}, ApplyYes},
		{[]string{"ia"}, ApplyNo},
		{[]string{"cscrf:mid,small"}, ApplyNo},
		{[]string{"cscrf:self"}, ApplyYes},
		{[]string{"derivatives", "clearing"}, ApplyYes},
		{[]string{"seg:COM"}, ApplyNo},
		{[]string{"no:mtf"}, ApplyYes},
		{[]string{"no:ibt", "no:algo"}, ApplyUnsure},
		{[]string{"no:cdsl"}, ApplyNo},
	} {
		if a := dutyApplies(c.needs, f); a.Verdict != c.verdict || a.Reason == "" {
			t.Errorf("%v: %+v, want %s", c.needs, a, c.verdict)
		}
	}
	f.CSCRF = ""
	if a := dutyApplies([]string{"cscrf:small"}, f); a.Verdict != ApplyUnsure || !strings.Contains(a.Reason, "CSCRF") {
		t.Errorf("unknown category: %+v", a)
	}
	for _, bad := range []string{"", "dp|", "member:XYZ", "cscrf:big", "broker,dp"} {
		if validDutyNeed(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestDutyCatalogueValid: the shipped catalogue reads, every entry names
// real Ledgerline tests and tasks, and every item of the source calendar
// (Trading, DP and RA & IA sheets; PMS left out) is covered.
func TestDutyCatalogueValid(t *testing.T) {
	cat, err := dutyCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Duties) == 0 || cat.Version < 1 {
		t.Fatalf("empty catalogue %+v", cat)
	}
	// Every item of the source calendar is covered (PMS left out).
	covered := map[string]bool{}
	for _, d := range cat.Duties {
		for _, it := range d.Items {
			covered[it] = true
		}
	}
	var want []string
	for i := 1; i <= 47; i++ {
		want = append(want, fmt.Sprintf("T%02d", i))
	}
	for i := 1; i <= 39; i++ {
		want = append(want, fmt.Sprintf("D%02d", i))
	}
	for i := 1; i <= 22; i++ {
		want = append(want, fmt.Sprintf("R%02d", i))
	}
	want = append(want, "TU1", "TU2", "TU3", "DU1")
	for _, it := range want {
		if !covered[it] {
			t.Errorf("calendar item %s is not covered", it)
		}
	}
	for _, d := range cat.Duties {
		if d.Link != nil && d.Link.Check != "" {
			if _, ok := siTest(d.Link.Check); !ok {
				t.Errorf("%s: unknown self-inspection test %q", d.ID, d.Link.Check)
			}
		}
		if d.API.Status == APIBuilt && d.How != HowAPI && d.How != HowLedgerline {
			t.Errorf("%s: an API Ledgerline has, but done %q", d.ID, d.How)
		}
	}
}

// dutyOf is one duty as the calendar lists it on a date.
func dutyOf(t *testing.T, o *Ops, id, date string) Duty {
	t.Helper()
	ds, err := o.Duties(t.Context(), T, date)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("%s not on the calendar", id)
	return Duty{}
}

// TestDutyCalendarFlow: the catalogue's duties are on the calendar with
// their circulars; what doesn't apply to the firm is not chased; an event
// duty is dated from the event and done once; a daily duty's open days are
// done together and go in one work item; Ledgerline's own check marks a
// duty done; the month's sheet lists them.
func TestDutyCalendarFlow(t *testing.T) {
	o, ctx := setup(t)
	cat, err := dutyCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveFirmProfile(ctx, T, "vikram.shah", FirmProfile{CDSL: AnswerYes, NSDL: AnswerNo, IBT: AnswerNo, Algo: AnswerNo, CSCRF: "self"}); err != nil {
		t.Fatal(err)
	}
	set, _ := o.Settings(ctx, T)
	facts := o.firmFacts(ctx, T)
	ds, err := o.Duties(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Duty{}
	for _, d := range ds {
		byID[d.ID] = d
	}
	for _, d := range cat.Duties {
		x, ok := byID[d.ID]
		if !ok {
			if strings.HasPrefix(d.ID, "CC-") {
				t.Fatalf("%s is not on the calendar", d.ID)
			}
			continue
		}
		if len(x.Refs) == 0 || x.How == "" || x.API == nil || x.Status == "" || x.Group == "" {
			t.Fatalf("%s without its catalogue text: %+v", d.ID, x)
		}
	}
	find := func(ok func(DutyDef) bool) (DutyDef, bool) {
		for _, d := range cat.Duties {
			if strings.HasPrefix(d.ID, "CC-") && ok(d) {
				return d, true
			}
		}
		return DutyDef{}, false
	}
	applies := func(d DutyDef) bool { return dutyApplies(d.Applies, facts).Verdict == ApplyYes }
	if d, ok := find(func(d DutyDef) bool { return contains(d.Applies, "nsdl") }); ok {
		if x := byID[d.ID]; x.Status != "Doesn't apply" || x.Overdue || x.NextDue != "" || x.Applicability == nil || x.Applicability.Verdict != ApplyNo {
			t.Fatalf("an NSDL duty for a firm that is only a CDSL participant: %+v", x)
		}
		if _, err := o.DoneDuty(ctx, T, "vikram.shah", D, d.ID, "done"); !errors.Is(err, ErrState) {
			t.Fatalf("marked done though it doesn't apply: %v", err)
		}
	}

	// An event: due from when it happened, on the owner's work queue, done once.
	ev, ok := find(func(d DutyDef) bool {
		return d.Rule == "EVENT" && !strings.HasSuffix(d.EventWithin, "h") && applies(d) && (d.Role == RoleCompliance || d.Role == RoleAdmin)
	})
	if !ok {
		t.Fatal("no event-driven duty in the catalogue")
	}
	if _, err := o.DoneDutyFor(ctx, T, "vikram.shah", D, ev.ID, DutyDoneReq{Evidence: "x"}); !errors.Is(err, ErrState) {
		t.Fatalf("done with nothing recorded: %v", err)
	}
	if _, err := o.RecordDutyEvent(ctx, T, "vikram.shah", D, ev.ID, DutyEventReq{Date: addDays(D, 1), What: "x"}); err == nil {
		t.Fatal("an event in the future was recorded")
	}
	x, err := o.RecordDutyEvent(ctx, T, "vikram.shah", D, ev.ID, DutyEventReq{Date: D, What: "The board appointed a director"})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := eventDue(set, ev.EventWithin, D, "")
	if x.NextDue != want || x.Period != "event E1" || !hasOpenWork(t, o, "compliance", ev.ID+"|event E1") {
		t.Fatalf("event duty %+v, want due %s and a work item", x, want)
	}
	if _, err := o.DoneDutyFor(ctx, T, "vikram.shah", D, ev.ID, DutyDoneReq{Evidence: "Intimated on the depository's portal, acknowledgement 123"}); err != nil {
		t.Fatal(err)
	}
	if x = dutyOf(t, o, ev.ID, D); x.Status != "When it happens" || len(x.Events) != 1 || x.Events[0].Done == nil || hasOpenWork(t, o, "compliance", ev.ID+"|event E1") {
		t.Fatalf("after done %+v", x)
	}

	// A daily check: the open days are done together.
	dd, ok := find(func(d DutyDef) bool {
		return d.Rule == "TDAY" && applies(d) && (d.Link == nil || d.Link.Check == "" && d.Link.Task == "")
	})
	if !ok {
		t.Fatal("no daily duty in the catalogue")
	}
	later := addTradingDays(set, D, 2)
	if x = dutyOf(t, o, dd.ID, later); !x.Overdue || x.Status != "Overdue" {
		t.Fatalf("earlier days not done: %+v", x)
	}
	r := &runCtx{o: o, ctx: ctx, tenant: T, date: later, process: "compliance", keys: map[string]bool{}}
	if err := autoCompliance(r); err != nil {
		t.Fatal(err)
	}
	if !hasOpenWork(t, o, "compliance", "daily|"+later) || hasOpenWork(t, o, "compliance", dd.ID+"|"+later) {
		t.Fatal("the day's checks are not in one work item")
	}
	if _, err := o.DoneDutyFor(ctx, T, "vikram.shah", later, dd.ID, DutyDoneReq{Evidence: "Checked", All: true}); err != nil {
		t.Fatal(err)
	}
	if x = dutyOf(t, o, dd.ID, later); x.Overdue || x.NextDue <= later || len(x.Done) < 2 {
		t.Fatalf("after all days done %+v", x)
	}

	// Ledgerline's nightly check: a pass leaves nothing to chase, a failure
	// says so. (A duty that Periodic work also does takes its dates from
	// the task instead.)
	if ck, ok := find(func(d DutyDef) bool { return d.Link != nil && d.Link.Check != "" && d.Link.Task == "" && applies(d) }); ok {
		run := SIRun{ID: "SIR-9000001", Date: D, At: D + "T22:30:00+05:30", By: SystemActor, Trigger: "nightly", Results: []SIResult{{Test: ck.Link.Check, Status: SIPass, Summary: "All good"}}}
		if err := docs.Upsert(ctx, o.Docs, T, cSIRun, run.ID, run, "test"); err != nil {
			t.Fatal(err)
		}
		if x = dutyOf(t, o, ck.ID, D); x.Status != "Done by Ledgerline" || x.Overdue || x.Check == nil {
			t.Fatalf("checked by Ledgerline: %+v", x)
		}
		run.ID, run.Results[0].Status = "SIR-9000002", SIFail
		if err := docs.Upsert(ctx, o.Docs, T, cSIRun, run.ID, run, "test"); err != nil {
			t.Fatal(err)
		}
		if x = dutyOf(t, o, ck.ID, D); x.Status != "Ledgerline check failing" {
			t.Fatalf("failing check: %+v", x)
		}
	}

	// The month's sheet.
	sh, err := o.DutySheetFor(ctx, T, "vikram.shah", D[:7], D)
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.Rows) != len(ds) || sh.Label != "October 2026" || sh.Counts["applies"] == 0 {
		t.Fatalf("sheet %d rows for %d duties %+v", len(sh.Rows), len(ds), sh.Counts)
	}
	daily := false
	for _, row := range sh.Rows {
		daily = daily || (row.ID == dd.ID && strings.HasPrefix(row.Every, "Each trading day"))
	}
	if !daily || !strings.HasPrefix(string(sh.CSV()), "Group,Sl,Compliance") {
		t.Fatalf("sheet rows %+v", sh.Rows)
	}
	if _, err := o.DutySheetFor(ctx, T, "meera.iyer", D[:7], D); err == nil {
		t.Fatal("the sheet was shown to a branch login")
	}

	// A change by the compliance officer stays.
	x = dutyOf(t, o, dd.ID, D)
	x.Title, x.Role = "Our own wording", RoleChecker
	if _, err := o.SaveDuty(ctx, T, "vikram.shah", x); err != nil {
		t.Fatal(err)
	}
	if x = dutyOf(t, o, dd.ID, D); x.Title != "Our own wording" || x.Role != RoleChecker || !x.Custom || len(x.Refs) == 0 {
		t.Fatalf("changed duty %+v", x)
	}
}
