package ops

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/migrate/pkg"
)

// TestDutyCatalogueGapFixes: the calendar corrections of the November 2026
// gap report are in the shipped catalogue.
func TestDutyCatalogueGapFixes(t *testing.T) {
	cat, err := dutyCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]DutyDef{}
	for _, d := range cat.Duties {
		by[d.ID] = d
	}
	get := func(id string) DutyDef {
		d, ok := by[id]
		if !ok {
			t.Fatalf("%s not in the catalogue", id)
		}
		return d
	}
	if d := get("CC-D-INTERNAL-ALERTS"); !strings.Contains(d.DueText, "45 days") || !strings.Contains(d.DueText, "60 days") || !strings.Contains(d.DueText, "7 days") || strings.Contains(d.DueText, "30 days") {
		t.Errorf("DP alert timelines: %s", d.DueText)
	}
	if d := get("CC-D-SURV-QUARTERLY"); d.Rule != "01-15,04-15,07-15,10-15" || !strings.Contains(d.DueText, "NIL") {
		t.Errorf("quarterly DP surveillance report %+v", d)
	}
	if d := get("CC-D-STR-INTIMATION"); d.Rule != "D07" || !strings.Contains(d.Title, "7th") {
		t.Errorf("STR intimation to CDSL %s %s", d.Rule, d.Title)
	}
	if d := get("CC-T-ASSOCIATE-ENTITIES"); d.Frequency != "Half-yearly" || strings.HasPrefix(d.Rule, "ONCE") {
		t.Errorf("associate entities scheduled once: %s", d.Rule)
	}
	if d := get("CC-T-ASSOCIATE-CHANGES"); d.Rule != "EVENT" || d.EventWithin != "30d" {
		t.Errorf("associate changes %+v", d)
	}
	if d := get("CC-T-MCX-COMPLAINTS"); d.Rule != "D07" || !contains(d.Applies, "member:MCX") || !strings.Contains(d.DueText, "NIL") {
		t.Errorf("MCX complaints %+v", d)
	}
	if strings.Contains(get("OB-COMPLAINTS").Changed, "No NSE, MCX") {
		t.Error("OB-COMPLAINTS still says there is no MCX monthly complaint filing")
	}
	ctr := get("OB-CTR")
	if strings.Contains(strings.ToLower(ctr.Title), "nil") || !strings.Contains(ctr.DueText, "No NIL report") {
		t.Errorf("CTR wording offers a nil report: %s / %s", ctr.Title, ctr.DueText)
	}
	for _, x := range DefaultDuties() {
		if x.ID == "OB-CTR" && strings.Contains(strings.ToLower(x.Title), "nil") {
			t.Errorf("default CTR duty offers a nil report: %s", x.Title)
		}
	}
	for _, id := range []string{"CC-D-AIML-REPORT", "CC-D-RISK-ASSESSMENT", "CC-T-MCX-INTAUDIT", "CC-T-BO-SOFTWARE-CRITERIA"} {
		if d := get(id); d.Rule == "" || len(d.Refs) == 0 {
			t.Errorf("%s incomplete %+v", id, d)
		}
	}
	if d := get("CC-D-AIML-REPORT"); d.Rule != "01-15,04-15,07-15,10-15" || !contains(d.Applies, "dp") {
		t.Errorf("DP AI & ML report %+v", d)
	}
	if d := get("CC-T-MCX-INTAUDIT"); !contains(d.Applies, "member:MCX") {
		t.Errorf("MCX internal audit applies to %v", d.Applies)
	}
}

// TestDutyOnceThenRecurring: a one-time duty made recurring keeps its
// one-time filing for the dates up to when it was done.
func TestDutyOnceThenRecurring(t *testing.T) {
	x := Duty{ID: "CC-T-X", Rule: "05-15,11-15", Active: true, Since: "2026-10-20",
		Done: []DutyRun{{Period: "once", Due: "2026-11-15", At: "2026-11-12T10:00:00+05:30", By: "vikram.shah"}}}
	dueDuty(Settings{}, ComplianceProfile{}, &x, "2026-11-23")
	if x.NextDue != "2027-05-15" || x.Overdue {
		t.Fatalf("after a one-time filing: next %s overdue %v", x.NextDue, x.Overdue)
	}
	y := Duty{ID: "CC-T-Y", Rule: "05-15,11-15", Active: true, Since: "2026-10-20"}
	dueDuty(Settings{}, ComplianceProfile{}, &y, "2026-11-23")
	if y.NextDue != "2026-11-15" || !y.Overdue {
		t.Fatalf("not filed: next %s overdue %v", y.NextDue, y.Overdue)
	}
}

// TestDutyRevisedCutoff: a revised cut-off dates its occurrence (earlier or
// later); a special session day is a trading day for daily duties; the
// firm records one and takes it back.
func TestDutyRevisedCutoff(t *testing.T) {
	x := Duty{ID: "CC-T-X", Rule: "D10", Active: true, Since: "2026-11-15", Revised: []DutyExtension{{Period: "2026-11", Due: "2026-12-05", By: "made-up notice"}}}
	dueDuty(Settings{}, ComplianceProfile{}, &x, "2026-12-01")
	if x.NextDue != "2026-12-05" || x.Period != "2026-11" {
		t.Fatalf("revised cut-off: %s %s", x.NextDue, x.Period)
	}
	set := Settings{SpecialSessions: []string{"2026-11-08"}} // a Sunday
	if !isTradingDay(set, "2026-11-08") || isTradingDay(Settings{}, "2026-11-08") {
		t.Fatal("a special session day is a trading day for the calendar")
	}
	found := false
	for _, oc := range dutyOccurrences(set, ComplianceProfile{}, "TDAY+5", "2026-11-08", "2026-11-30", 50) {
		found = found || oc.Period == "2026-11-08"
	}
	if !found {
		t.Fatal("the special session's trade date is not expected")
	}

	o, ctx := setup(t)
	const id = "CC-T-MARGIN-REPORTING"
	d, err := o.SetDutyCutoff(ctx, T, "vikram.shah", "2026-11-23", id, DutyCutoffReq{Period: "2026-11-20", Due: "2026-11-30", By: "made-up NSE Clearing notice"})
	if err != nil || len(d.Revised) != 1 || d.Revised[0].Due != "2026-11-30" {
		t.Fatalf("record: %v %+v", err, d.Revised)
	}
	if _, err := o.SetDutyCutoff(ctx, T, "vikram.shah", "2026-11-23", id, DutyCutoffReq{Period: "2026-11-20", Due: "2026-11-30"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no circular named: %v", err)
	}
	if _, err := o.SetDutyCutoff(ctx, T, "vikram.shah", "2026-11-23", "CC-T-FIU-FILINGS", DutyCutoffReq{Period: "x", Due: "2026-11-30", By: "n"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an event duty: %v", err)
	}
	if _, err := o.SetDutyCutoff(ctx, T, "priya.nair", "2026-11-23", id, DutyCutoffReq{Period: "2026-11-20", Due: "2026-11-30", By: "n"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an Ops maker: %v", err)
	}
	if d, err = o.SetDutyCutoff(ctx, T, "vikram.shah", "2026-11-23", id, DutyCutoffReq{Period: "2026-11-20"}); err != nil || len(d.Revised) != 0 {
		t.Fatalf("take back: %v %+v", err, d.Revised)
	}
}

// TestArchiveRetention: ten years by default; an MCX member cannot go below
// ten, another firm not below SEBI's eight; a migrated document keeps the
// later of ten years and the package's date; a demat account's KYC
// documents stay for its life and eight years after it closes.
func TestArchiveRetention(t *testing.T) {
	nse := Settings{MemberCodes: map[string]string{"NSE": "90001"}}
	mcx := Settings{MemberCodes: map[string]string{"NSE": "90001", "MCX": "50001"}}
	if archiveRetainMin(nse) != 8 || archiveRetainMin(mcx) != 10 || archiveRetainMin(Settings{}) != 10 {
		t.Fatalf("floors %d %d %d", archiveRetainMin(nse), archiveRetainMin(mcx), archiveRetainMin(Settings{}))
	}
	if s := (ArchiveSettings{}).norm(8); s.RetainYears != 10 {
		t.Fatalf("default %d", s.RetainYears)
	}
	if s := (ArchiveSettings{RetainYears: 8}).norm(8); s.RetainYears != 8 {
		t.Fatalf("eight for a non-MCX firm: %d", s.RetainYears)
	}
	if s := (ArchiveSettings{RetainYears: 8}).norm(10); s.RetainYears != 10 {
		t.Fatalf("eight for an MCX member: %d", s.RetainYears)
	}
	day := func(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }
	now := day(2026, 11, 23)
	if got := retainUntil(pkg.DocMeta{Date: day(2020, 1, 1), RetainUntil: day(2028, 1, 1)}, "2019-20", now); got != "2030-01-01" {
		t.Fatalf("package's eight years shortened the rule: %s", got)
	}
	if got := retainUntil(pkg.DocMeta{Date: day(2020, 1, 1), RetainUntil: day(2035, 1, 1)}, "2019-20", now); got != "2035-01-01" {
		t.Fatalf("package's longer date: %s", got)
	}
	if got := retainUntil(pkg.DocMeta{}, "2019-20", now); got != "2030-03-31" {
		t.Fatalf("by financial year: %s", got)
	}

	o, ctx := setup(t)
	o.Now = func() time.Time { return now }
	const ucc, bo = "BH1001", "1208160000000099"
	doc := ArchiveDoc{Type: pkg.DocKYC, UCC: ucc}
	if why := o.dpAccountHold(ctx, T, ArchiveDoc{Type: pkg.DocKYC, UCC: "NOSUCH1"}, "2026-11-23"); why != "" {
		t.Fatalf("no demat account: %s", why)
	}
	cdslLinkBO(t, o, ucc, bo)
	if why := o.dpAccountHold(ctx, T, doc, "2026-11-23"); !strings.Contains(why, "is open") {
		t.Fatalf("open account: %q", why)
	}
	if why := o.dpAccountHold(ctx, T, ArchiveDoc{Type: pkg.DocContractNote, UCC: ucc}, "2026-11-23"); why != "" {
		t.Fatalf("a contract note is not an account record: %q", why)
	}
	if err := o.putBO(ctx, T, "admin", BOAccount{UCC: ucc, BOID: bo, Depository: "CDSL", Status: "Closed", Opened: "2010-01-01", ClosedOn: "2020-03-01"}); err != nil {
		t.Fatal(err)
	}
	if why := o.dpAccountHold(ctx, T, doc, "2026-11-23"); !strings.Contains(why, "2028") {
		t.Fatalf("closed in 2020: %q", why)
	}
	if why := o.dpAccountHold(ctx, T, doc, "2028-03-01"); why != "" {
		t.Fatalf("eight years after closing: %q", why)
	}
}

// TestBackdatedLockCheck: the self-inspection test passes only with an
// automatic lock of at most 3 business days.
func TestBackdatedLockCheck(t *testing.T) {
	if _, ok := siTest("backdated_lock"); !ok {
		t.Fatal("backdated_lock not registered")
	}
	run := func(set Settings) siOut {
		out, err := siBackdatedLock(&siEnv{date: "2026-11-23", set: set})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if s := run(Settings{LockedUpTo: "2026-11-20"}); s.found != 1 {
		t.Fatalf("manual lock only: %+v", s)
	}
	if s := run(Settings{AutoLockDays: 5}); s.found != 1 {
		t.Fatalf("five days: %+v", s)
	}
	if s := run(Settings{AutoLockDays: 3}); s.found != 0 || !strings.Contains(s.summary, "older than T-2") {
		t.Fatalf("three days: %+v", s)
	}
}
