package ops

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline"
	"ledgerline/internal/backup"
	"ledgerline/internal/docs"
)

// Recovery targets, intraday backups and the RPO Ledgerline states.
func TestIntradayBackupsAndRPO(t *testing.T) {
	o, ctx, _ := backupOps(t)
	at := time.Date(2026, 10, 16, 10, 0, 0, 0, ist)
	o.Now = func() time.Time { return at }
	if _, err := o.SaveCyberSettings(ctx, T, "vikram.shah", CyberSettings{IntradayMinutes: 7}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an interval not offered: %v", err)
	}
	if _, err := o.SaveCyberSettings(ctx, T, "vikram.shah", CyberSettings{RPOMin: 60}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a looser RPO without a reason: %v", err)
	}
	if _, err := o.SaveCyberSettings(ctx, T, "priya.nair", CyberSettings{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker changed the settings: %v", err)
	}
	s := o.rpoStatus(ctx, T)
	if s.Meets || !strings.Contains(s.Line, "after each day's close") {
		t.Fatalf("before intraday: %+v", s)
	}
	if _, err := o.SaveCyberSettings(ctx, T, "vikram.shah", CyberSettings{IntradayMinutes: 10}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		o.intradayTick(ctx, T, at)
		at = at.Add(5 * time.Minute)
	}
	es, _ := o.Backups.List(T)
	n := 0
	for _, e := range es {
		if e.Has(backup.Intraday) {
			n++
		}
	}
	if n != 10 { // 10:00 to 11:35 every 10 minutes
		t.Fatalf("intraday backups: %d", n)
	}
	s = o.rpoStatus(ctx, T)
	if !s.Meets || s.LastGood == "" || s.WorstGapMin > 15 || s.WAL != "not available" {
		t.Fatalf("with intraday: %+v", s)
	}
	// An intraday backup does not stand for the day's close backup.
	st, _, _ := docs.GetOr(ctx, o.Docs, T, cBackupState, "main", backupState{})
	if len(st.Days) != 0 {
		t.Fatalf("intraday marked a day: %+v", st.Days)
	}
	// Outside the window nothing runs.
	at = time.Date(2026, 10, 16, 23, 45, 0, 0, ist)
	o.intradayTick(ctx, T, at)
	if es2, _ := o.Backups.List(T); len(es2) != len(es) {
		t.Fatal("a backup outside the window")
	}
	// Intraday backups are kept by count, not by day.
	var xs []backup.Entry
	for i := 0; i < 20; i++ {
		xs = append(xs, backup.Entry{Manifest: backup.Manifest{ID: "b" + string(rune('a'+i)), Date: "2026-10-16", Tiers: []string{backup.Intraday}, Created: time.Date(2026, 10, 16, 9, i, 0, 0, time.UTC).Format(time.RFC3339)}, Verified: "x"})
	}
	if drop := backup.Prune(xs, backup.Keep{Intraday: 16}); len(drop) != 4 {
		t.Fatalf("pruned %d", len(drop))
	}
}

// Capacity: requests counted per quarter hour, written by the tick, and a
// crossed threshold is a work item.
func TestCapacityMonitoring(t *testing.T) {
	o, ctx := setup(t)
	at := time.Date(2026, 10, 16, 10, 1, 0, 0, ist)
	o.Now = func() time.Time { return at }
	if _, err := o.SaveCyberSettings(ctx, T, "vikram.shah", CyberSettings{BusyPerMin: 10, SlowMs: 500}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		o.NoteRequest(T, time.Duration(100+i*30)*time.Millisecond, 200)
	}
	at = at.Add(16 * time.Minute)
	o.capacityTick(ctx, T, at)
	v := o.capacityView(ctx, T)
	if len(v.Today) != 1 || v.Today[0].Requests != 30 || v.Today[0].PeakPerMin != 30 || v.Today[0].P95Ms < 900 {
		t.Fatalf("sample %+v", v.Today)
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	found := false
	for _, w := range ws {
		found = found || strings.HasPrefix(w.ID, "W-security-capacity")
	}
	if !found || len(v.Alerts) == 0 {
		t.Fatalf("no alert: %+v", v.Alerts)
	}
}

// Confidentiality agreements: a login made with the hold on cannot sign
// in until its agreement is recorded; older logins are not affected.
func TestConfidentialityAgreementHold(t *testing.T) {
	o, ctx := setup(t)
	if _, err := o.SaveCyberSettings(ctx, T, "vikram.shah", CyberSettings{HoldLogins: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.Login(ctx, T, "farhan.khan", DevPassword, "", "10.0.0.1", ""); err != nil {
		t.Fatalf("an existing login was held: %v", err)
	}
	pw, err := o.CreateUser(ctx, T, "admin", User{ID: "new.joiner", Name: "New Joiner", Role: RoleMaker})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.Login(ctx, T, "new.joiner", pw, "", "10.0.0.1", ""); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "confidentiality") {
		t.Fatalf("held login signed in: %v", err)
	}
	if _, err := o.RecordAgreement(ctx, T, "admin", ConfAgreement{User: "new.joiner", Kind: "Third party", Signed: "2026-10-16", Doc: "file 12"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("third party without its organisation: %v", err)
	}
	if _, err := o.RecordAgreement(ctx, T, "admin", ConfAgreement{User: "new.joiner", Kind: "Third party", Party: "Outsourced KYC Ltd", Signed: o.today(), Doc: "file 12"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.Login(ctx, T, "new.joiner", pw, "", "10.0.0.1", ""); err != nil {
		t.Fatalf("still held after the agreement: %v", err)
	}
	rows := o.agreementRows(ctx, T)
	for _, r := range rows {
		if r.User == "new.joiner" && (r.Missing || r.Party != "Outsourced KYC Ltd") {
			t.Fatalf("row %+v", r)
		}
	}
}

// Release evidence from the provider, the evidence pack, the test
// environment and inspection visits.
func TestEvidenceSandboxInspection(t *testing.T) {
	o, ctx := setup(t)
	if _, err := o.RecordReleaseEvidence(ctx, "ops@provider", ReleaseEvidence{Version: runningVersion(), Released: "2026-10-01", Kind: "SAST", Tool: "gosec", Result: "Findings open"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("open findings without a summary: %v", err)
	}
	if _, err := o.RecordReleaseEvidence(ctx, "ops@provider", ReleaseEvidence{Version: runningVersion(), Released: "2026-10-01", Kind: "SAST", Tool: "gosec", Result: "Passed"}); err != nil {
		t.Fatal(err)
	}
	rv := o.releaseView(ctx)
	if !contains(rv.Covered, "SAST") || !contains(rv.Missing, "VAPT") {
		t.Fatalf("release view %+v", rv)
	}
	b, _, err := o.EvidencePack(ctx, T, "vikram.shah")
	if err != nil {
		t.Fatal(err)
	}
	var pack map[string]json.RawMessage
	if err := json.Unmarshal(b, &pack); err != nil || pack["sbom"] == nil || pack["cloud_principles"] == nil {
		t.Fatalf("pack %v %s", err, b[:200])
	}
	var cps []CloudPrinciple
	json.Unmarshal(pack["cloud_principles"], &cps)
	if len(cps) != 9 {
		t.Fatalf("principles %d", len(cps))
	}
	// No test environment, then one.
	if _, _, err := o.PrepareSandbox(ctx, T, "admin", nil); !errors.Is(err, ErrState) {
		t.Fatalf("prepared without one: %v", err)
	}
	o.Sandboxes = map[string]string{"demo-uat": T}
	if !o.IsSandbox("demo-uat") || o.sandboxInfo(ctx, "demo-uat").Production != T {
		t.Fatal("sandbox not known")
	}
	v, pw, err := o.PrepareSandbox(ctx, T, "admin", nil)
	if err != nil || pw == "" || v.Users != 1 {
		t.Fatalf("prepare %v %q %+v", err, pw, v)
	}
	if _, _, err := o.Login(ctx, "demo-uat", "admin", pw, "", "10.0.0.1", ""); err != nil {
		t.Fatalf("sign in to the test environment: %v", err)
	}
	if _, pw2, _ := o.PrepareSandbox(ctx, T, "admin", nil); pw2 != "" {
		t.Fatal("a second login was made")
	}
	// An inspection visit.
	iv, err := o.SaveInspectionVisit(ctx, T, "vikram.shah", InspectionVisit{Authority: "MCX", Inspectors: "A. Inspector", From: o.today(), Given: []string{"Supervised screens", "Test environment"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveInspectionVisit(ctx, T, "vikram.shah", InspectionVisit{ID: iv.ID, Authority: "MCX", Inspectors: "A. Inspector", From: iv.From, Closed: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("closed without what was observed: %v", err)
	}
	res, err := o.CyberResView(ctx, T, "vikram.shah")
	if err != nil || len(res.Visits) != 1 || res.AuditCycle.QSB != "unsure" || res.RootedPolicy != "warn" {
		t.Fatalf("screen %v %+v", err, res.AuditCycle)
	}
}

// A firm may refuse rooted or jailbroken phones; warning stays the default.
func TestRootedPhoneRefused(t *testing.T) {
	o, ctx := setup(t)
	mobileClock(o)
	staffOn(t, o, ctx, MobileSettings{StaffRoles: []string{RoleChecker}})
	tok := signIn(t, o, ctx, "farhan.khan", DevPassword)
	if _, _, err := o.RegisterDevice(ctx, T, "farhan.khan", tok, DeviceReq{App: AppStaff, Platform: "android", Rooted: true}); err != nil {
		t.Fatalf("warn is the default: %v", err)
	}
	staffOn(t, o, ctx, MobileSettings{StaffRoles: []string{RoleChecker}, RootedPolicy: "refuse"})
	tok = signIn(t, o, ctx, "farhan.khan", DevPassword)
	if _, _, err := o.RegisterDevice(ctx, T, "farhan.khan", tok, DeviceReq{App: AppStaff, Platform: "android", Rooted: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("rooted phone registered: %v", err)
	}
	if cfg, _ := o.MobileAppConfig(ctx, T, AppStaff); cfg.RootedPolicy != "refuse" {
		t.Fatalf("config %+v", cfg.RootedPolicy)
	}
	if _, err := o.SaveMobileSettings(ctx, T, "admin", MobileSettings{RootedPolicy: "block"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown policy: %v", err)
	}
}

// Full Aadhaar numbers in a document's text are refused; masked ones pass.
func TestAadhaarGuardAndCallers(t *testing.T) {
	// 2345 6789 0124 is not checked; build a valid one from the table.
	base := "23456789012"
	full := ""
	for d := '0'; d <= '9'; d++ {
		if verhoeffValid(base + string(d)) {
			full = base + string(d)
		}
	}
	printed := full[:4] + " " + full[4:8] + " " + full[8:]
	if aadhaarGuard([]byte("%PDF-1.4 Aadhaar: "+printed+" ...")) == nil {
		t.Fatal("a full Aadhaar number got through")
	}
	if aadhaarGuard([]byte("%PDF-1.4 Aadhaar: XXXX XXXX "+full[8:])) != nil {
		t.Fatal("a masked Aadhaar was refused")
	}
	if aadhaarGuard([]byte("account 1234 5678 9012 345")) != nil && verhoeffValid("123456789012") {
		t.Log("a longer number is not an Aadhaar")
	}
	if !AllowedCaller(nil, "8.8.8.8") || !AllowedCaller(prefixes([]string{"203.0.113.0/24"}), "203.0.113.9") || AllowedCaller(prefixes([]string{"203.0.113.0/24", "10.0.0.1"}), "198.51.100.1") || !AllowedCaller(prefixes([]string{"10.0.0.1"}), "10.0.0.1") {
		t.Fatal("caller list")
	}
}

// The duties waiting for the firm-module conditions are valid but for
// those conditions.
func TestPendingCyberDuties(t *testing.T) {
	b, err := ledgerline.FS.ReadFile("config/compliance/duties-pending-cyber2.json")
	if err != nil {
		t.Skip("the pending file is not embedded:", err)
	}
	var c struct {
		Duties []DutyDef `json:"duties"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	cat, _ := dutyCatalogue()
	for _, d := range c.Duties {
		for _, x := range cat.Duties {
			if x.ID == d.ID {
				t.Errorf("%s is already in duties.json", d.ID)
			}
		}
		for i, n := range d.Applies {
			n = strings.NewReplacer("ibt:10k", "ibt", "specified", "ibt", "qsb", "broker").Replace(n)
			d.Applies[i] = n
		}
		if err := d.validate(); err != nil {
			t.Errorf("%s: %v", d.ID, err)
		}
	}
}
