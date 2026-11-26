package ops

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

func i64(n int64) *int64 { return &n }

// TestModuleConditions: every condition of the Firm modules vocabulary
// reads yes, no or not sure from the firm facts, with a reason, and an
// unknown answer asks the firm to tell Ledgerline on Settings → Firm
// modules.
func TestModuleConditions(t *testing.T) {
	known := firmFacts{
		Member: map[string]bool{"NSE": true, "MCX": true}, Seg: map[string]bool{"CM": true, "FO": true, "COM": true, "SLB": true},
		XSeg:   map[string]bool{"NSE-CM": true, "NSE-FO": true, "NSE-SLB": true, "MCX-COM": true},
		SelfAt: map[string]bool{"NCL": true}, ViaCM: map[string]string{"MCXCCL": "Some CM"},
		Role: map[string]string{"NSE-CM": SegPCM, "NSE-FO": SegSelf, "MCX-COM": SegTM}, PCM: triYes,
		CMChange: triYes, CMChangeOn: "2026-10-10", IBT: triYes, CDSL: triYes, NSDL: triNo,
		Active: clientNumber{Value: 2500, Known: true, Exact: true}, Registered: clientNumber{Value: 30000, Known: true, Exact: true},
		IBTClients: clientNumber{Value: 12000, Known: true, Exact: true}, Specified: triYes, QSB: triNo, QSBEx: triYes, QSBExUntil: "2029-03-31",
		Const: "llp", Sw: map[string]tri{}, Regs: map[string]bool{},
	}
	none := firmFacts{Member: map[string]bool{"NSE": true}, Seg: map[string]bool{"CM": true}, XSeg: map[string]bool{"NSE-CM": true}, SelfAt: map[string]bool{},
		Role: map[string]string{"NSE-CM": SegTM}, PCM: triNo, CMChange: triNo, IBT: triNo, Active: clientNumber{Value: 150, Known: true, Exact: true},
		Registered: clientNumber{Value: 400, Known: true, Exact: true}, Specified: triNo, QSB: triNo, QSBEx: triNo, Const: "company", Sw: map[string]tri{}, Regs: map[string]bool{}}
	unsure := firmFacts{Member: map[string]bool{}, Seg: map[string]bool{}, XSeg: map[string]bool{}, SelfAt: map[string]bool{}, Role: map[string]string{},
		Sw: map[string]tri{}, Regs: map[string]bool{}, IBT: triUnknown, Active: clientNumber{Value: 900, Known: true}} // a lower bound
	for _, s := range FirmSwitches {
		known.Sw[s.ID], none.Sw[s.ID] = triYes, triNo
	}
	for _, c := range []struct {
		cond          string
		yes, no, unk  string // verdicts for known, none, unsure
		whyYes, whyNo string // in the reason
	}{
		{"xseg:NSE-SLB", ApplyYes, ApplyNo, ApplyNo, "you trade NSE SLB", "don't trade NSE SLB"},
		{"xseg:MCX-COM", ApplyYes, ApplyNo, ApplyNo, "MCX COM", "MCX COM"},
		{"selfat:NCL", ApplyYes, ApplyNo, ApplyNo, "NSE Clearing", "NSE Clearing"},
		{"selfat:MCXCCL", ApplyNo, ApplyNo, ApplyNo, "", "MCX Clearing"},
		{"pcm", ApplyYes, ApplyNo, ApplyUnsure, "other trading members", "only your own"},
		{"cmchange", ApplyYes, ApplyNo, ApplyUnsure, "changed clearing member", "no change"},
		{"slb", ApplyYes, ApplyNo, ApplyNo, "lending and borrowing", "SLB"},
		{"clients:active>=2000", ApplyYes, ApplyNo, ApplyUnsure, "2,500 active", "fewer than 2,000"},
		{"ibt:10k", ApplyYes, ApplyNo, ApplyUnsure, "12,000", "website or app"},
		{"specified", ApplyYes, ApplyNo, ApplyUnsure, "specified stockbrokers", "not a specified"},
		{"qsb", ApplyNo, ApplyNo, ApplyUnsure, "", "not a qualified"},
		{"qsb:ex", ApplyYes, ApplyNo, ApplyUnsure, "31-Mar-2029", "ex-QSB"},
		{"const:llp", ApplyYes, ApplyNo, ApplyUnsure, "limited liability partnership", "company"},
		{"const:partnership|const:llp", ApplyYes, ApplyNo, ApplyUnsure, "", ""},
		{"seg:SLB", ApplyYes, ApplyNo, ApplyNo, "lending", "lending"},
	} {
		for i, f := range []firmFacts{known, none, unsure} {
			want := []string{c.yes, c.no, c.unk}[i]
			a := dutyApplies([]string{c.cond}, f)
			if a.Verdict != want || a.Reason == "" {
				t.Errorf("%s (%d): %+v, want %s", c.cond, i, a, want)
			}
			if i == 0 && c.whyYes != "" && !strings.Contains(a.Reason, c.whyYes) {
				t.Errorf("%s: reason %q lacks %q", c.cond, a.Reason, c.whyYes)
			}
			if i == 1 && c.whyNo != "" && !strings.Contains(a.Reason, c.whyNo) {
				t.Errorf("%s: reason %q lacks %q", c.cond, a.Reason, c.whyNo)
			}
			if want == ApplyUnsure && i == 2 && !strings.Contains(a.Reason, "Settings → Firm modules") {
				t.Errorf("%s: an unknown answer does not say where to tell Ledgerline: %q", c.cond, a.Reason)
			}
		}
		if !validDutyNeed(c.cond) {
			t.Errorf("%s refused", c.cond)
		}
	}
	// Every switch: yes, no, and not answered → tell Ledgerline.
	for _, s := range FirmSwitches {
		for i, f := range []firmFacts{known, none, unsure} {
			want := []string{ApplyYes, ApplyNo, ApplyUnsure}[i]
			if a := dutyApplies([]string{s.ID}, f); a.Verdict != want || (i == 2 && !strings.Contains(a.Reason, "tell Ledgerline")) {
				t.Errorf("switch %s (%d): %+v", s.ID, i, a)
			}
		}
		if !validDutyNeed(s.ID) || !validDutyNeed("no:"+s.ID) {
			t.Errorf("switch %s refused as a condition", s.ID)
		}
	}
	// The whole vocabulary is accepted in duties.json; nonsense is not.
	for _, c := range ModuleConditions() {
		if !strings.Contains(c.Cond, "<") && !validDutyNeed(c.Cond) {
			t.Errorf("%s refused", c.Cond)
		}
	}
	for _, bad := range []string{"xseg:NSE-XYZ", "selfat:NSCCL", "const:trust", "clients:active>=", "qsb:yes", "dp:nothing", "seg:SLX"} {
		if validDutyNeed(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	// The CSCRF category from the numbers: the higher of clients and
	// volume; a lower bound is not sure unless it already reaches the top.
	for _, c := range []struct {
		reg, vol clientNumber
		cat      string
		sure     bool
	}{
		{clientNumber{Value: 900, Known: true, Exact: true}, clientNumber{Value: 800, Known: true, Exact: true}, "exempt", true},
		{clientNumber{Value: 1000, Known: true, Exact: true}, clientNumber{Value: 10, Known: true, Exact: true}, "self", true},
		{clientNumber{Value: 5000, Known: true, Exact: true}, clientNumber{Value: 20000, Known: true, Exact: true}, "small", true},
		{clientNumber{Value: 200000, Known: true, Exact: true}, clientNumber{}, "mid", false},
		{clientNumber{Value: 2000000, Known: true, Exact: true}, clientNumber{}, "qualified", true},
		{clientNumber{Value: 500, Known: true, Exact: true}, clientNumber{Value: 5, Known: true}, "exempt", false},
	} {
		if cat, sure := cscrfFromNumbers(c.reg, c.vol); cat != c.cat || sure != c.sure {
			t.Errorf("%+v %+v: %s %v, want %s %v", c.reg, c.vol, cat, sure, c.cat, c.sure)
		}
	}
	if fmtCount(1234567) != "12,34,567" || fmtCount(999) != "999" {
		t.Errorf("Indian grouping: %s %s", fmtCount(1234567), fmtCount(999))
	}
}

// TestFirmModulesFlow: existing settings load unchanged; the screen writes
// the segment roles to Settings (one source of truth with Cleared through),
// clearing for others switches the clearing member's internal audit on,
// client numbers carry who gave them, the CSCRF category is worked out
// and an exempt firm gets the Exempted declaration, a change is previewed
// and audited, screens and self-inspection tests follow, and only the
// compliance officer and an Admin change it.
func TestFirmModulesFlow(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 17, 10, 0, 0, 0, ist) }

	// Existing tenant: nothing answered, SLB not traded, settings as they were.
	set, _ := o.Settings(ctx, T)
	before := fmt.Sprint(set.ClearingFor, set.MemberCodes)
	if set.ClearedThrough("NSE-SLB") != ClearNone || set.ClearedThrough("BSE-SLB") != ClearNone {
		t.Fatalf("SLB traded by default: %s %s", set.ClearedThrough("NSE-SLB"), set.ClearedThrough("BSE-SLB"))
	}
	v, err := o.FirmModulesFor(ctx, T, "priya.nair") // everyone reads
	if err != nil || v.CanEdit || len(v.Segments) != len(ClearingSegments) || len(v.Counts) != 4 {
		t.Fatalf("view for ops maker: %v %+v", err, v.CanEdit)
	}
	if s, _ := o.Settings(ctx, T); fmt.Sprint(s.ClearingFor, s.MemberCodes) != before {
		t.Fatal("opening the screen changed settings")
	}
	if c := v.Counts[0]; c.Key != CountRegistered || c.Counted == nil || *c.Counted == 0 {
		t.Fatalf("registered clients not counted: %+v", c)
	}
	f := o.firmFacts(ctx, T)
	if v, _ := dutyNeed("pcm", f); v != triUnknown && len(f.SelfAt) > 0 {
		t.Fatalf("pcm for a self-clearing firm that has not answered: %v", v)
	}
	if _, err := o.SaveFirmModules(ctx, T, "priya.nair", FirmModulesReq{QSB: sp("no")}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ops maker saved: %v", err)
	}
	if _, err := o.PreviewFirmModules(ctx, T, "anita.rao", FirmModulesReq{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk previewed: %v", err)
	}
	for _, bad := range []FirmModulesReq{
		{Segments: map[string]SegChoice{"NSE-CM": {Role: "boss"}}},
		{Segments: map[string]SegChoice{"MCX-COM": {Role: SegTM}}},                // no clearing member
		{Segments: map[string]SegChoice{"NSE-CM": {Role: SegSelf, CC: "MCXCCL"}}}, // not allowed there
		{Switches: map[string]string{"dma": "maybe"}},
		{Switches: map[string]string{"nonsense": "yes"}},
		{On: map[string][]string{"hedge": {"NSE"}}},
		{Constitution: sp("trust")},
		{QSB: sp("ex")}, // without the date
		{Counts: map[string]*CountInput{CountActive: {Value: i64(-1)}}},
		{CSCRF: sp("huge")},
		{CMChangedOn: sp("2027-01-01")},
	} {
		if _, err := o.PreviewFirmModules(ctx, T, "vikram.shah", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v accepted: %v", bad, err)
		}
	}

	// The preview says what switches on before anything is saved.
	req := FirmModulesReq{
		MemberCodes: map[string]string{"NSE": "90001", "BSE": "3001", "MCX": "56001"},
		Segments: map[string]SegChoice{"NSE-CM": {Role: SegPCM, CC: "NCL"}, "NSE-FO": {Role: SegSelf, CC: "NCL"}, "BSE-CM": {Role: SegSelf, CC: "NCL"}, "BSE-FO": {Role: SegNone},
			"NSE-CD": {Role: SegNone}, "BSE-CD": {Role: SegNone}, "NSE-COM": {Role: SegNone}, "MCX-COM": {Role: SegTM, CMCode: "M9", CMName: "Made-up Clearing Ltd"}, "NSE-SLB": {Role: SegSelf}},
		CDSL: sp(AnswerNo), NSDL: sp(AnswerNo), Switches: map[string]string{"dma": AnswerYes, "dp:saral": AnswerNo},
		On: map[string][]string{"dma": {"NSE"}}, Constitution: sp("llp"),
		Counts: map[string]*CountInput{CountRegistered: {Value: i64(800), Note: "UCC count from NSE"}, CountVolume: {Value: i64(500)}, CountActive: {Value: i64(2100)}},
	}
	diff, err := o.PreviewFirmModules(ctx, T, "vikram.shah", req)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]DiffItem{}
	for _, x := range append(append(diff.Duties, diff.Tests...), diff.Screens...) {
		ids[x.ID] = x
	}
	if x, ok := ids["CC-T-CM-AUDIT"]; !ok || x.To != ApplyYes {
		t.Errorf("clearing for others does not switch on the CM internal audit: %+v (%v)", x, ok)
	}
	if x, ok := ids["CC-T-MOD-CSCRF-EXEMPTED"]; !ok || x.To != ApplyYes {
		t.Errorf("an exempt firm is not given the Exempted declaration: %+v", x)
	}
	if x, ok := ids["dp"]; !ok || x.To != ApplyNo {
		t.Errorf("the depository screen is not hidden for a firm that is no DP: %+v", x)
	}
	if x, ok := ids["bsda_review"]; !ok || x.To != ApplyNo {
		t.Errorf("the BSDA test still applies to a firm that is no DP: %+v", x)
	}
	if len(diff.Changes) < 5 {
		t.Errorf("changes in words: %v", diff.Changes)
	}
	if s, _ := o.Settings(ctx, T); fmt.Sprint(s.ClearingFor, s.MemberCodes) != before {
		t.Fatal("the preview saved")
	}

	v, err = o.SaveFirmModules(ctx, T, "vikram.shah", req)
	if err != nil {
		t.Fatal(err)
	}
	set, _ = o.Settings(ctx, T)
	if set.ClearedThrough("MCX-COM") != "CM:M9" || set.ClearingMemberNames["MCX-COM"] != "Made-up Clearing Ltd" || set.ClearedThrough("NSE-SLB") != "NCL" || set.ClearedThrough("BSE-FO") != ClearNone || set.MemberCodes["NSE"] != "90001" {
		t.Fatalf("settings: %v %v %v", set.ClearingFor, set.ClearingMemberNames, set.MemberCodes)
	}
	f = o.firmFacts(ctx, T)
	for cond, want := range map[string]tri{"pcm": triYes, "selfat:NCL": triYes, "selfat:MCXCCL": triNo, "xseg:MCX-COM": triYes, "slb": triYes, "dp": triNo,
		"dma": triYes, "dp:saral": triNo, "sor": triUnknown, "const:llp": triYes, "clients:active>=2000": triYes, "cscrf:exempt": triYes, "cmchange": triYes, "bo:inhouse": triNo} {
		if got, why := dutyNeed(cond, f); got != want {
			t.Errorf("%s: %v (%s), want %v", cond, got, why, want)
		}
	}
	if f.CSCRFFrom != "counted" {
		t.Errorf("the CSCRF category was not worked out from the numbers: %+v", f.CSCRF)
	}
	if c := v.Counts[0]; c.Override == nil || c.Override.By != "vikram.shah" || c.Override.At == "" || c.Override.Note != "UCC count from NSE" || *c.Value != 800 {
		t.Fatalf("override without who and when: %+v", c)
	}
	// The firm's answer wins; a mismatch with the numbers is shown.
	v, err = o.SaveFirmModules(ctx, T, "admin", FirmModulesReq{CSCRF: sp("small"), Counts: map[string]*CountInput{CountVolume: {Value: i64(200000)}}})
	if err != nil {
		t.Fatal(err)
	}
	var cat DerivedView
	for _, d := range v.Derived {
		if d.Key == "cscrf" {
			cat = d
		}
	}
	if cat.Value != "Small-size RE" || !strings.Contains(cat.Warn, "Mid-size") {
		t.Errorf("CSCRF answer and mismatch: %+v", cat)
	}
	if got, _ := dutyNeed("cscrf:small", o.firmFacts(ctx, T)); got != triYes {
		t.Error("the firm's answer does not win")
	}

	// The calendar follows: no DP duties, the CM audit applies.
	ds, err := o.Duties(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		switch {
		case d.ID == "CC-T-CM-AUDIT" && (d.Applicability == nil || d.Applicability.Verdict != ApplyYes):
			t.Errorf("CM audit: %+v", d.Applicability)
		case d.ID == "CC-D-INTERNAL-AUDIT" && d.Status != "Doesn't apply":
			t.Errorf("DP duty for a firm that is no DP: %s", d.Status)
		}
	}

	// Screens: hidden for the menu.
	h := o.HiddenScreens(ctx, T)
	if h["dp"] == "" || h["cdsltxn"] == "" || h["recon.mcx"] != "" {
		t.Errorf("hidden screens: %v", h)
	}

	// Self-inspection: the BSDA test is not applicable.
	run, err := o.RunSelfInspection(ctx, T, "vikram.shah", D)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range run.Results {
		if r.Test == "bsda_review" && (r.Status != SISkipped || !strings.Contains(r.Summary, "Not applicable")) {
			t.Errorf("bsda_review: %s %s", r.Status, r.Summary)
		}
	}

	// Audited with what it switched.
	evs, _ := o.App.Audit.List(ctx, T, 0)
	found := false
	for _, e := range evs {
		found = found || (e.Action == "firm.modules" && e.Actor == "vikram.shah" && strings.Contains(fmt.Sprint(e.Detail["duties"]), "CC-T-CM-AUDIT"))
	}
	if !found {
		t.Error("the change is not in the audit trail with the duties it switched")
	}

	// An ex-QSB keeps the obligations for three financial years.
	if _, err := o.SaveFirmModules(ctx, T, "vikram.shah", FirmModulesReq{QSB: sp("ex"), QSBEndedOn: sp("2025-06-30")}); err != nil {
		t.Fatal(err)
	}
	f = o.firmFacts(ctx, T)
	if v, why := dutyNeed("qsb:ex", f); v != triYes || f.QSBExUntil != "2029-03-31" {
		t.Errorf("ex-QSB: %v %s until %s", v, why, f.QSBExUntil)
	}
	if v, _ := dutyNeed("qsb", f); v != triNo {
		t.Error("an ex-QSB is a QSB")
	}
	// Back to Ledgerline's count.
	if _, err := o.SaveFirmModules(ctx, T, "vikram.shah", FirmModulesReq{Counts: map[string]*CountInput{CountActive: nil}}); err != nil {
		t.Fatal(err)
	}
	if m := o.firmModules(ctx, T); m.Counts[CountActive].By != "" {
		t.Error("the figure was not taken back")
	}
}

// TestClientCount: Ledgerline counts registered and active clients from
// its own data and says when the trades cover only part of the period.
func TestClientCount(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 17, 10, 0, 0, 0, ist) }
	c, err := o.countClients(ctx, T, "2026-10-17")
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := o.Clients(ctx, T)
	if c.Counts[CountRegistered] == 0 || c.Counts[CountRegistered] > int64(len(cs)) || c.Counts[CountActive] == 0 || c.Counts[CountActive] > c.Counts[CountRegistered] {
		t.Fatalf("counts %+v of %d clients", c.Counts, len(cs))
	}
	if c.FY != "2025-26" || c.Partial[CountActive] == "" || c.Partial[CountVolume] == "" {
		t.Fatalf("partial: %+v", c)
	}
	n := numberFor(CountActive, FirmModules{}, c)
	if !n.Known || n.Exact || !strings.Contains(n.From, "at least") {
		t.Fatalf("a count from part of the year: %+v", n)
	}
	// A lower bound under 2,000 is not a No.
	f := o.firmFacts(ctx, T)
	if _, err := o.recountClients(ctx, T); err != nil {
		t.Fatal(err)
	}
	f = o.firmFacts(ctx, T)
	if v, why := dutyNeed("clients:active>=2000", f); v != triUnknown || !strings.Contains(why, "Firm modules") {
		t.Fatalf("active clients from part of the year: %v %s", v, why)
	}
	// The once-a-day count runs from the boards' tick.
	_ = docs.Upsert(ctx, o.Docs, T, cFirmModules, fmCounted, fmCount{Date: "2026-01-01"}, SystemActor)
	for _, h := range boardTickHooks {
		h(ctx, o, T, o.now())
	}
	if c := o.firmCount(ctx, T); c.Date != "2026-10-17" {
		t.Fatalf("not counted by the tick: %+v", c)
	}
}

// TestCMChange: a change of clearing member made in Settings is recorded
// for the cmchange condition; starting to trade a segment is not one.
func TestCMChange(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2026, 10, 17, 10, 0, 0, 0, ist) }
	set, _ := o.Settings(ctx, T)
	set.ClearingFor = map[string]string{"NSE-SLB": "NCL"}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if v, _ := dutyNeed("cmchange", o.firmFacts(ctx, T)); v != triNo {
		t.Fatal("starting SLB counted as a change of clearing member")
	}
	set, _ = o.Settings(ctx, T)
	set.ClearingFor["NSE-FO"] = "CM:C77"
	set.ClearingMemberNames = map[string]string{"NSE-FO": "Made-up CM"}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if v, why := dutyNeed("cmchange", o.firmFacts(ctx, T)); v != triYes || !strings.Contains(why, "17-Oct-2026") {
		t.Fatalf("change of clearing member: %v %s", v, why)
	}
	o.Now = func() time.Time { return time.Date(2026, 11, 20, 10, 0, 0, 0, ist) }
	if v, _ := dutyNeed("cmchange", o.firmFacts(ctx, T)); v != triNo {
		t.Fatal("a change 34 days ago still counts")
	}
}
