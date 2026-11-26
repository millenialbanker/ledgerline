package ops

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// The sign-in lock needs an out-of-band unlock; the code by e-mail works
// once; passwords expire and the last ones cannot be reused.
func TestSignInLockAndPasswordRules(t *testing.T) {
	o, ctx := setup(t)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	o.DevCodes = true
	for i := 0; i < maxFailures; i++ {
		o.Login(ctx, T, "farhan.khan", "wrong-password", "", "1.1.1.1", "")
	}
	now = now.Add(2 * time.Hour)
	if _, _, err := o.Login(ctx, T, "farhan.khan", DevPassword, "", "1.1.1.1", ""); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "unlock") {
		t.Fatalf("still locked after 2 hours: %v", err)
	}
	if err := o.UnlockUser(ctx, T, "admin", "farhan.khan", "ok"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unlock without saying how: %v", err)
	}
	if err := o.UnlockUser(ctx, T, "priya.nair", "farhan.khan", "called the registered mobile"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker unlocked staff: %v", err)
	}
	// No e-mail on record for this staff login: the answer is the same
	// and nothing is sent; a wrong code does nothing.
	if msg := o.RequestUnlockCode(ctx, T, "farhan.khan", "1.1.1.1"); strings.Contains(msg, "Development server") {
		t.Fatalf("a code was sent with no e-mail on record: %s", msg)
	}
	if err := o.UnlockWithCode(ctx, T, "farhan.khan", "123456", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong code: %v", err)
	}
	// A client login: the code goes to the client's e-mail.
	cs, _ := o.Clients(ctx, T)
	var ucc string
	for _, c := range cs {
		if c.Status == "Active" && reEmail.MatchString(c.Email) {
			ucc = c.UCC
			break
		}
	}
	id, pw, err := o.PortalAccess(ctx, T, "farhan.khan", ucc, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.ChangePassword(ctx, T, id, pw, "client password one"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFailures; i++ {
		o.Login(ctx, T, id, "nope-nope-nope", "", "2.2.2.2", "")
	}
	if _, _, err := o.Login(ctx, T, id, "client password one", "", "2.2.2.2", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("client not locked: %v", err)
	}
	msg := o.RequestUnlockCode(ctx, T, id, "2.2.2.2")
	i := strings.Index(msg, "the code is ")
	if i < 0 {
		t.Fatalf("no code: %s", msg)
	}
	code := msg[i+12 : i+18]
	if err := o.UnlockWithCode(ctx, T, id, code, "2.2.2.2"); err != nil {
		t.Fatal(err)
	}
	if err := o.UnlockWithCode(ctx, T, id, code, "2.2.2.2"); err == nil {
		t.Fatal("a code worked twice")
	}
	if _, _, err := o.Login(ctx, T, id, "client password one", "", "2.2.2.2", ""); err != nil {
		t.Fatalf("after unlock: %v", err)
	}
	// History: the last passwords cannot be used again.
	if err := o.ChangePassword(ctx, T, id, "client password one", "client password two"); err != nil {
		t.Fatal(err)
	}
	if err := o.ChangePassword(ctx, T, id, "client password two", "client password one"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an old password reused: %v", err)
	}
	// Expiry: after 90 days the password must be changed.
	now = now.AddDate(0, 0, 91)
	_, st, err := o.Login(ctx, T, id, "client password two", "", "2.2.2.2", "")
	if err != nil || !st.MustChange || !st.PasswordExpired || !st.Restricted() {
		t.Fatalf("expired password: %+v %v", st, err)
	}
	// The firm may choose the timed lock; then it ends by itself.
	if _, err := o.SaveSecuritySettings(ctx, T, "vikram.shah", SecuritySettings{TimedUnlock: true}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFailures; i++ {
		o.Login(ctx, T, "anita.rao", "wrong-password", "", "", "")
	}
	now = now.Add(16 * time.Minute)
	if _, _, err := o.Login(ctx, T, "anita.rao", DevPassword, "", "", ""); err != nil {
		t.Fatalf("timed lock: %v", err)
	}
}

// Follow-up reports start once the incident is reported; the forensic
// report may be marked not needed; a personal data breach brings the
// Board's reports and the notice to clients; the time to take it on.
func TestIncidentFollowUps(t *testing.T) {
	o, ctx := setup(t)
	now := time.Date(2027, 6, 1, 10, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	v, err := o.RecordIncident(ctx, T, "vikram.shah", RecordIncidentInput{Title: "Phishing mail opened", Severity: SevHigh})
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := o.IncidentByID(ctx, T, "vikram.shah", v.ID); len(d.FollowUps) != 0 || !d.Investigate.Met {
		t.Fatalf("not reported yet: %+v %+v", d.FollowUps, d.Investigate)
	}
	if _, err := o.ReportIncident(ctx, T, "vikram.shah", v.ID, IncidentReport{To: "CERT-In", How: "Email", Reference: "CERT-1"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	d, _ := o.IncidentByID(ctx, T, "vikram.shah", v.ID)
	st := map[string]FollowUp{}
	for _, f := range d.FollowUps {
		st[f.ID] = f
	}
	if !st["sebi-portal"].Overdue || st["interim"].Overdue || st["forensic"].ID == "" || st["dpb-report"].ID != "" {
		t.Fatalf("stages %+v", d.FollowUps)
	}
	if _, err := o.ReportIncident(ctx, T, "vikram.shah", v.ID, IncidentReport{To: "SEBI", How: "Portal", Reference: "SEBI-9", Stage: "sebi-portal"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.MarkFollowUpNotNeeded(ctx, T, "vikram.shah", v.ID, "rca", "no reason at all"); err == nil {
		t.Fatal("the RCA marked not needed")
	}
	if _, err := o.MarkFollowUpNotNeeded(ctx, T, "vikram.shah", v.ID, "forensic", "contained on one laptop, wiped and reimaged"); err != nil {
		t.Fatal(err)
	}
	// A personal data breach.
	x, err := o.MarkPersonalData(ctx, T, "vikram.shah", v.ID, true, "one client's statement mailed to a wrong address")
	if err != nil {
		t.Fatal(err)
	}
	st = map[string]FollowUp{}
	for _, f := range x.FollowUps {
		st[f.ID] = f
	}
	if !st["sebi-portal"].Done || st["forensic"].NotNeeded == "" || !st["dpb-intimation"].Overdue || st["dpb-report"].ID == "" {
		t.Fatalf("after: %+v", x.FollowUps)
	}
	cs, _ := o.Clients(ctx, T)
	if _, err := o.NotifyBreachClients(ctx, T, "vikram.shah", v.ID, []string{cs[0].UCC}, "a statement went to a wrong address", "your holdings were seen by one person", "we asked the person to delete it", "nothing; watch for unusual mails"); err != nil {
		t.Fatal(err)
	}
	// The tick puts late reports on the queue.
	o.incidentFollowTick(ctx, T, now)
	if len(workOpen(o, ctx, v.ID+"|dpb-intimation")) != 1 || len(workOpen(o, ctx, v.ID+"|clients")) != 0 {
		t.Fatal("late Board intimation not raised, or the clients' notice not seen")
	}
	// A detected incident not taken on in time.
	e := o.SecurityEvent(ctx, T, SecEvent{Kind: "malware.found", Detail: map[string]any{"signature": "Eicar"}})
	now = now.Add(5 * time.Hour)
	d, _ = o.IncidentByID(ctx, T, "vikram.shah", e.Incident)
	if !d.Investigate.Overdue || d.Investigate.Hours != 4 {
		t.Fatalf("investigate %+v", d.Investigate)
	}
	if _, err := o.DeclareDisaster(ctx, T, "vikram.shah", e.Incident, now.Add(-40*time.Minute).Format(time.RFC3339), "", "back office down"); err != nil {
		t.Fatal(err)
	}
	if y, _ := o.IncidentByID(ctx, T, "vikram.shah", e.Incident); y.Disaster == nil || !y.Disaster.Late {
		t.Fatalf("disaster %+v", y.Disaster)
	}
	sc, _ := o.Incidents(ctx, T, "vikram.shah")
	if sc.RetentionWarn != "" || sc.Late == 0 {
		t.Fatalf("screen: warn %q late %d", sc.RetentionWarn, sc.Late)
	}
}

// A client must accept the terms and notice; reports a fraud; asks for
// the data summary, names a nominee; withdraws WhatsApp consent at once.
func TestPortalPrivacyAndFraud(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return time.Date(2027, 6, 1, 11, 0, 0, 0, ist) }
	cs, _ := o.Clients(ctx, T)
	var c Client
	for _, x := range cs {
		if x.Status == "Active" && reEmail.MatchString(x.Email) {
			c = x
			break
		}
	}
	id, _, err := o.PortalAccess(ctx, T, "farhan.khan", c.UCC, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !o.TermsDue(ctx, T, id) || o.TermsDue(ctx, T, "priya.nair") {
		t.Fatal("terms due wrong")
	}
	pt, _ := o.PortalTermsFor(ctx, T, id)
	if _, err := o.AcceptTerms(ctx, T, id, pt.TermsVersion+1, pt.NoticeVersion, "3.3.3.3"); !errors.Is(err, ErrState) {
		t.Fatalf("a version not shown accepted: %v", err)
	}
	if _, err := o.AcceptTerms(ctx, T, id, pt.TermsVersion, pt.NoticeVersion, "3.3.3.3"); err != nil || o.TermsDue(ctx, T, id) {
		t.Fatalf("accept: %v", err)
	}
	// A new notice: due again.
	p := o.privacySettings(ctx, T)
	p.Notice += "\n\nWe also keep call recordings for a year."
	if _, err := o.SavePrivacySettings(ctx, T, "vikram.shah", p); err != nil {
		t.Fatal(err)
	}
	if !o.TermsDue(ctx, T, id) {
		t.Fatal("a changed notice is not due")
	}
	// Fraud report: incident, work items, the account blocked as asked.
	r, err := o.ReportFraud(ctx, T, id, FraudReport{UCC: c.UCC, Kind: "Payout I did not ask for", Detail: "a payout of 5000 I never asked for", Block: true}, "3.3.3.3")
	if err != nil || r.Incident == "" {
		t.Fatalf("fraud %+v %v", r, err)
	}
	if x, _ := o.Client(ctx, T, c.UCC); !x.Blocked {
		t.Fatal("not blocked")
	}
	if len(workOpen(o, ctx, r.ID+"|")) != 2 {
		t.Fatal("work items")
	}
	if _, err := o.CloseFraudReport(ctx, T, "vikram.shah", r.ID, "the payout was the client's own standing instruction"); err != nil {
		t.Fatal(err)
	}
	// Data summary and requests.
	sum, err := o.PortalDataSummary(ctx, T, id, c.UCC)
	if err != nil || len(sum.Items) < 4 || len(sum.Recipients) < 4 || strings.Contains(sum.Items[1].Value, c.PAN) {
		t.Fatalf("summary %+v %v", sum, err)
	}
	if _, err := o.SetDataNominee(ctx, T, id, c.UCC, DataNominee{Name: "Asha Test", Relation: "Spouse"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a nominee with no contact")
	}
	if _, err := o.SetDataNominee(ctx, T, id, c.UCC, DataNominee{Name: "Asha Test", Relation: "Spouse", Mobile: "9000000001"}); err != nil {
		t.Fatal(err)
	}
	dr, err := o.RequestData(ctx, T, id, c.UCC, "Correction", "my city is spelt wrong")
	if err != nil || dr.Status != "Open" || dr.Due != "2027-08-30" {
		t.Fatalf("request %+v %v", dr, err)
	}
	if _, err := o.AnswerDataRequest(ctx, T, "vikram.shah", dr.ID, true, "corrected through a change request"); err != nil {
		t.Fatal(err)
	}
	if sum, _ = o.PortalDataSummary(ctx, T, id, c.UCC); sum.Nominee == nil || len(sum.Requests) != 1 {
		t.Fatalf("summary after: %+v", sum)
	}
}

// A closed client can be erased only after the retention period.
func TestErasureAfterRetention(t *testing.T) {
	o, ctx := setup(t)
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	cs, _ := o.Clients(ctx, T)
	u := cs[0].UCC
	if err := docs.Upsert(ctx, o.Docs, T, cClosure, "CL-1", AccountClosure{ID: "CL-1", UCC: u, Status: "Closed", ClosedOn: "2015-06-30"}, "t"); err != nil {
		t.Fatal(err)
	}
	if err := docs.Upsert(ctx, o.Docs, T, cClosure, "CL-2", AccountClosure{ID: "CL-2", UCC: cs[1].UCC, Status: "Closed", ClosedOn: "2024-06-30"}, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.EraseClient(ctx, T, "vikram.shah", cs[1].UCC, "retention over"); !errors.Is(err, ErrState) {
		t.Fatalf("erased inside retention: %v", err)
	}
	c, err := o.EraseClient(ctx, T, "vikram.shah", u, "retention over")
	if err != nil || c.PAN != "" || c.Email != "" || !strings.HasPrefix(c.Name, "Erased") || c.ErasedOn == "" {
		t.Fatalf("erase %+v %v", c, err)
	}
	if _, err := o.EraseClient(ctx, T, "vikram.shah", u, "again"); !errors.Is(err, ErrState) {
		t.Fatal("erased twice")
	}
}

// Pre-clearance needs the no-UPSI declaration; a contra trade needs a
// written relaxation; the post-trade and no-trade reports; the yearly
// disclosure; the board's approval and the UPSI record.
func TestInsiderTradingCode(t *testing.T) {
	r := hrSetup(t)
	o, ctx := r.o, r.ctx
	e := r.add(t, Employee{Name: "Omkar Test", JoinedOn: "2025-01-01", User: "priya.nair", Designated: true})
	a, err := o.DeclareAccount(ctx, T, "priya.nair", "", StaffAccount{Holder: "Omkar Test", Broker: "This firm", Account: "ZZ1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.RequestPreClearance(ctx, T, "priya.nair", PreClearance{Account: a.ID, Security: "TESTCO", Side: "Buy", Qty: 10}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no declaration: %v", err)
	}
	pc, _ := o.RequestPreClearance(ctx, T, "priya.nair", PreClearance{Account: a.ID, Security: "TESTCO", Side: "Buy", Qty: 10, NoUPSI: true})
	pc, _ = o.DecidePreClearance(ctx, T, "vikram.shah", pc.ID, true, "")
	if pc.NoUPSIText == "" {
		t.Fatal("declaration words not kept")
	}
	if _, err := o.ReportPreClearance(ctx, T, "priya.nair", pc.ID, PreClearReport{Traded: true, TradeDate: "2026-05-04", Qty: 11, Price: 100_00}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("more than pre-cleared: %v", err)
	}
	if pc, err = o.ReportPreClearance(ctx, T, "priya.nair", pc.ID, PreClearReport{Traded: true, TradeDate: "2026-05-04", Qty: 10, Price: 100_00}); err != nil || pc.Report == nil || pc.Report.Late {
		t.Fatalf("report %+v %v", pc.Report, err)
	}
	// A sale within six months: contra, approved only with a relaxation.
	r.at("2026-06-10 10:00")
	pc2, _ := o.RequestPreClearance(ctx, T, "priya.nair", PreClearance{Account: a.ID, Security: "TESTCO", Side: "Sell", Qty: 10, NoUPSI: true})
	if !pc2.Contra {
		t.Fatal("contra not seen")
	}
	if _, err := o.DecidePreClearance(ctx, T, "vikram.shah", pc2.ID, true, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("contra approved without relaxation: %v", err)
	}
	if pc2, err = o.DecidePreClearance(ctx, T, "vikram.shah", pc2.ID, true, "medical emergency, relaxed under clause 8"); err != nil || pc2.ContraRelaxed == "" {
		t.Fatal(err)
	}
	// Not reported by its end: the tick asks for the report.
	r.at("2026-06-25 10:00")
	o.pitTick(ctx, T, r.clock)
	if len(workOpen(o, ctx, "preclear-report:"+pc2.ID)) != 1 {
		t.Fatal("missing report not raised")
	}
	if _, err := o.ReportPreClearance(ctx, T, "priya.nair", pc2.ID, PreClearReport{Reason: "price moved away"}); err != nil {
		t.Fatal(err)
	}
	// The yearly disclosure.
	if _, err := o.DeclarePITDetails(ctx, T, "priya.nair", "", PITDisclosure{Relatives: []PITPerson{{Name: "Spouse Test", Relation: "Spouse"}}, NoFinancial: true, Phones: []string{"9000000002"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative without PAN or other ID: %v", err)
	}
	if _, err := o.DeclarePITDetails(ctx, T, "priya.nair", "", PITDisclosure{Relatives: []PITPerson{{Name: "Spouse Test", Relation: "Spouse", PAN: "ABCDE1234F"}}, NoFinancial: true, Phones: []string{"9000000002"}, Education: []string{"Test College"}}); err != nil {
		t.Fatal(err)
	}
	// Board approval and grounds.
	if _, err := o.RecordPITBoardApproval(ctx, T, "vikram.shah", "Dealers, research, compliance and everyone with access to client order flow", "2026-04-15", "BR-2026-04/3"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetDesignated(ctx, T, "vikram.shah", e.Code, true, "Access to UPSI"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordUPSIShare(ctx, T, "vikram.shah", UPSIShare{Subject: "TESTCO", Nature: "a block deal enquiry", SharedWith: "Legal adviser", OtherID: "Bar council 123", Purpose: "legal advice", SharedOn: "2026-06-25"}); err != nil {
		t.Fatal(err)
	}
	v, _ := o.OwnTrading(ctx, T, "vikram.shah", false)
	if len(v.MissingDiscl) != 0 || len(v.NoBasis) != 0 || len(v.NoReport) != 0 || len(v.UPSI) != 1 || v.Policy.BoardApproval == nil {
		t.Fatalf("register %+v", v)
	}
	// Changing the criteria needs a fresh approval.
	p := o.hrPolicy(ctx, T)
	p.OwnTrading.DesignationCriteria = "Everyone"
	if p, _ = o.SaveHRPolicy(ctx, T, "vikram.shah", p); p.OwnTrading.BoardApproval != nil {
		t.Fatal("approval kept for new criteria")
	}
}

// Valid till: seven trading days, the day of approval the first.
func TestPreClearValidTill(t *testing.T) {
	set := Settings{}
	if got := preClearValidTill(set, "2026-05-04", 7); got != "2026-05-12" {
		t.Fatalf("Monday: %s", got)
	}
	if got := preClearValidTill(set, "2026-05-09", 7); got != "2026-05-19" {
		t.Fatalf("Saturday: %s", got)
	}
}

func TestStaffControls(t *testing.T) {
	r := hrSetup(t)
	o, ctx := r.o, r.ctx
	co := r.add(t, Employee{Name: "Chitra Test", JoinedOn: "2024-01-01", Duties: []string{"Compliance officer"}})
	dp := r.add(t, Employee{Name: "Dev Test", JoinedOn: "2025-01-01", Duties: []string{"DP operations"}})
	v, err := o.StaffControlsView(ctx, T, "vikram.shah")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, g := range v.Gaps {
		want[g.Code+"|"+g.What] = true
	}
	for _, k := range []string{co.Code + "|AML-sensitive position not checked", co.Code + "|Compliance officer's eligibility not recorded", co.Code + "|Compliance officer without NISM Series III-A after a year", dp.Code + "|No photo ID on file (DP staff)"} {
		if !want[k] {
			t.Fatalf("missing gap %s in %+v", k, v.Gaps)
		}
	}
	if _, err := o.RecordCOEligibility(ctx, T, "vikram.shah", co.Code, COEligibility{Employee: true, Qualification: "Graduate", ExperienceYrs: 1}); !errors.Is(err, ErrState) {
		t.Fatalf("one year's experience accepted: %v", err)
	}
	if _, err := o.RecordCOEligibility(ctx, T, "vikram.shah", co.Code, COEligibility{Employee: true, Qualification: "Graduate", ExperienceYrs: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordAMLCheck(ctx, T, "hema.hr", co.Code, AMLCheck{Note: "references and background checked; AML training done"}); err != nil {
		t.Fatal(err)
	}
	v, _ = o.StaffControlsView(ctx, T, "vikram.shah")
	for _, g := range v.Gaps {
		if g.Code == co.Code && g.What != "Compliance officer without NISM Series III-A after a year" {
			t.Fatalf("gap left %+v", g)
		}
	}
}

func TestResilienceRecords(t *testing.T) {
	o, ctx := newOps(t)
	o.Now = func() time.Time { return time.Date(2026, 11, 2, 10, 0, 0, 0, ist) }
	d, err := o.RecordRestoreDrill(ctx, T, "admin", RestoreDrill{Kind: "Restore test", System: "Ledgerline", From: "Off-site backup", Isolated: true,
		Started: "2026-10-20T10:00", Restored: "2026-10-20T11:30", DataUpTo: "2026-10-20T09:50"})
	if err != nil || d.RTOMin != 90 || d.RPOMin != 10 || !d.Passed {
		t.Fatalf("drill %+v %v", d, err)
	}
	if _, err := o.SaveTechVendor(ctx, T, "vikram.shah", TechVendor{Name: "Finspot", Service: "Trading front end", Critical: true}); err != nil {
		t.Fatal(err)
	}
	v, err := o.CyberControlsView(ctx, T, "vikram.shah")
	if err != nil || len(v.Vendors) != 2 || v.Vendors[0].ID != "ledgerline" || len(v.Vendors[1].Gaps) == 0 || v.DrillDue || v.Glitch.AsOf != "2026-03-31" || v.Glitch.Over {
		t.Fatalf("view %+v %v", v.Vendors, err)
	}
	b, err := o.SBOM(ctx, T, "vikram.shah")
	var bom map[string]any
	if err != nil || json.Unmarshal(b, &bom) != nil || bom["bomFormat"] != "CycloneDX" || len(bom["components"].([]any)) < 1 {
		t.Fatalf("sbom %s %v", b, err)
	}
	// Privileged review: built, and nobody signs their own line.
	rv, err := o.BuildPrivReview(ctx, T, "vikram.shah", "2026-11-02")
	if err != nil || rv.ID != "2026-H1" || len(rv.Admins) == 0 {
		t.Fatalf("review %+v %v", rv, err)
	}
	if _, err := o.SignPrivReview(ctx, T, rv.Admins[0].User, rv.ID, rv.Admins[0].User, "looked at it"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("own line signed: %v", err)
	}
}

func TestNTPStatus(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "chrony.conf")
	os.WriteFile(f, []byte("# test\nserver samay1.nic.in iburst\npool 2.pool.ntp.org\n"), 0o600)
	if s := ntpStatus("", []string{filepath.Join(dir, "none"), f}); s.State != "NIC/NPL" || len(s.Servers) != 2 || s.From != f {
		t.Fatalf("chrony %+v", s)
	}
	if s := ntpStatus("time.google.com", nil); s.State != "Other" {
		t.Fatalf("env %+v", s)
	}
	if s := ntpStatus("", []string{filepath.Join(dir, "none")}); s.State != "Not found" {
		t.Fatalf("none %+v", s)
	}
}
