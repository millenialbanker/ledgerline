package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// secSetup is a sample firm with a clock that can move, a mail server
// that records what it is given, and the security contacts set.
func secSetup(t *testing.T) (*Ops, context.Context, *fakeMailer, *time.Time) {
	t.Helper()
	o, ctx := newOps(t)
	clock := time.Date(2026, 11, 20, 10, 0, 0, 0, ist)
	o.Now = func() time.Time { return clock }
	o.App.Now = func() time.Time { return clock }
	m := &fakeMailer{}
	o.Mailer = m
	// Today's purge and seal check are done, so a tick adds no events of its own.
	if err := docs.Upsert(ctx, o.Docs, T, cSecState, "main", secState{LastPurge: "2026-11-20", SealCheckedOn: "2026-11-20"}, SystemActor); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveSecuritySettings(ctx, T, "vikram.shah", SecuritySettings{Contacts: []string{"soc@bhairav.example", "co@bhairav.example"}}); err != nil {
		t.Fatal(err)
	}
	return o, ctx, m, &clock
}

func mailsTo(m *fakeMailer, to, subj string) int {
	n := 0
	for _, x := range m.got {
		if x.To == to && strings.Contains(x.Subject, subj) {
			n++
		}
	}
	return n
}

func incidents(t *testing.T, o *Ops, ctx context.Context, tenant string) []IncidentView {
	t.Helper()
	sc, err := o.Incidents(ctx, tenant, "vikram.shah")
	if err != nil {
		t.Fatal(err)
	}
	return sc.Incidents
}

// TestFailedSignInsOpenOneIncident: wrong passwords for one user open one
// incident at the fifth, alert each contact once and put it on the
// compliance officer's and the Admin's queues; later failures join it.
// Once it is closed, the next burst opens a new incident.
func TestFailedSignInsOpenOneIncident(t *testing.T) {
	o, ctx, m, clock := secSetup(t)
	for i := 0; i < 8; i++ {
		*clock = clock.Add(30 * time.Second)
		if _, _, err := o.Login(ctx, T, "priya.nair", "wrong-password-x", "", "198.51.100.7", "test"); err == nil {
			t.Fatal("wrong password accepted")
		}
	}
	xs := incidents(t, o, ctx, T)
	if len(xs) != 1 {
		t.Fatalf("incidents: %+v", xs)
	}
	in := xs[0]
	if in.Rule != "failed-user" || in.Key != "priya.nair" || in.Status != IncOpen || in.Severity != SevMedium || in.EventCount != 4 || !strings.Contains(in.Title, "priya.nair") {
		t.Fatalf("incident: %+v", in.Incident)
	}
	if mailsTo(m, "soc@bhairav.example", "[Security incident]") != 1 || mailsTo(m, "co@bhairav.example", "[Security incident]") != 1 || len(m.got) != 2 {
		t.Fatalf("alerts: %+v", m.got)
	}
	if !strings.Contains(m.got[0].Text, "6 hours") || !strings.Contains(m.got[0].Text, "IST") {
		t.Fatalf("alert text: %s", m.got[0].Text)
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	roles := map[string]bool{}
	for _, w := range ws {
		if w.Process == "security" && strings.Contains(w.Title, in.ID) {
			roles[w.Role] = true
		}
	}
	if !roles[RoleCompliance] || !roles[RoleAdmin] || len(roles) != 2 {
		t.Fatalf("work items: %v", roles)
	}
	evs, err := o.SecEvents(ctx, T, "vikram.shah", "2026-11-20", "2026-11-20", "signin", 0)
	if err != nil || len(evs) < 9 { // 8 failures and the lockout
		t.Fatalf("events: %d %v", len(evs), err)
	}
	if _, err := o.SecEvents(ctx, T, "priya.nair", "2026-11-20", "2026-11-20", "", 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker saw events: %v", err)
	}
	// The same user's failures from one address also count for the
	// address, but the user's incident is the one they join.
	d, err := o.IncidentByID(ctx, T, "vikram.shah", in.ID)
	if err != nil || len(d.EventList) != 4 || d.EventList[0].Kind != "signin.failed" || d.EventList[0].Incident != in.ID {
		t.Fatalf("detail: %+v %v", d.EventList, err)
	}
	// Close it; a new burst opens a new incident and alerts again.
	if _, err := o.UpdateIncident(ctx, T, "vikram.shah", in.ID, IncClosed, "", false); err == nil {
		t.Fatal("closed without a note")
	}
	if _, err := o.UpdateIncident(ctx, T, "vikram.shah", in.ID, IncClosed, "The user had forgotten the new password; reset it.", false); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Hour)
	for i := 0; i < 5; i++ {
		*clock = clock.Add(10 * time.Second)
		o.Login(ctx, T, "priya.nair", "wrong-password-y", "", "198.51.100.7", "test")
	}
	if xs := incidents(t, o, ctx, T); len(xs) != 2 || xs[0].ID == in.ID || xs[1].Status != IncClosed {
		t.Fatalf("after close: %+v", xs)
	}
	if mailsTo(m, "soc@bhairav.example", "[Security incident]") != 2 {
		t.Fatalf("second alert: %d", len(m.got))
	}
	ws, _ = o.WorkItems(ctx, T, "")
	for _, w := range ws {
		if w.Process == "security" && strings.Contains(w.Title, in.ID) && w.Status == "Open" {
			t.Fatalf("work item of the closed incident still open: %+v", w)
		}
	}
}

// TestSingleEventRules: an audit seal failure, malware and an Admin role
// granted each open an incident at once; a second malware hit with the
// same signature joins the first.
func TestSingleEventRules(t *testing.T) {
	o, ctx, m, _ := secSetup(t)
	o.log(ctx, T, SystemActor, "security.audit.verified", "", map[string]any{"ok": false, "sealed": 10, "problems": 2})
	o.log(ctx, T, "priya.nair", "file.malware_found", "F1", map[string]any{"signature": "Eicar-Signature", "source": "CLIENT-DOC"})
	o.log(ctx, T, "farhan.khan", "file.malware_found", "F2", map[string]any{"signature": "Eicar-Signature", "source": "BOARD"})
	if _, err := o.CreateUser(ctx, T, "admin", User{ID: "new.admin", Name: "New Admin", Role: RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.CreateUser(ctx, T, "admin", User{ID: "new.maker", Name: "New Maker", Role: RoleMaker}); err != nil {
		t.Fatal(err)
	}
	by := map[string]IncidentView{}
	for _, x := range incidents(t, o, ctx, T) {
		by[x.Rule] = x
	}
	if len(by) != 3 || by["seal"].Severity != SevCritical || by["malware"].EventCount != 2 || by["admin"].Key != "new.admin" {
		t.Fatalf("incidents: %+v", by)
	}
	if n := mailsTo(m, "soc@bhairav.example", "[Security incident]"); n != 3 {
		t.Fatalf("alerts: %d", n)
	}
	evs, _ := o.SecEvents(ctx, T, "admin", "2026-11-20", "2026-11-20", "account", 0)
	kinds := map[string]int{}
	for _, e := range evs {
		kinds[e.Kind]++
	}
	if kinds["account.admin_granted"] != 1 || kinds["account.created"] != 1 {
		t.Fatalf("account events: %v", kinds)
	}
	// Admin alerts can be switched off; the event stays.
	if _, err := o.SaveSecuritySettings(ctx, T, "admin", SecuritySettings{NoAdminAlert: true}); err != nil {
		t.Fatal(err)
	}
	if err := o.PutUser(ctx, T, "admin", User{ID: "new.maker", Name: "New Maker", Role: RoleAdmin, Active: true}); err != nil {
		t.Fatal(err)
	}
	if xs := incidents(t, o, ctx, T); len(xs) != 3 {
		t.Fatalf("admin alert off still opened: %d", len(xs))
	}
	// Changing a connection secret and the sign-in settings are events.
	if _, err := o.SaveConnection(ctx, T, "admin", "kra", map[string]string{"provider": "CVL KRA", "code": "X1", "user": "u"}, map[string]string{"password": "s3cret-pass"}); err != nil {
		t.Fatal(err)
	}
	evs, _ = o.SecEvents(ctx, T, "admin", "2026-11-20", "2026-11-20", "connection.secret_changed", 0)
	if len(evs) != 1 || strings.Contains(evs[0].Message, "s3cret") {
		t.Fatalf("connection events: %+v", evs)
	}
}

// TestCertInClockIST: the 6-hour deadline is worked out and shown in IST,
// across midnight; a report to CERT-In stops the clock and moves the
// incident to Reported.
func TestCertInClockIST(t *testing.T) {
	det := time.Date(2026, 11, 20, 20, 30, 0, 0, time.UTC) // 02:00 IST on the 21st
	c := certInClock(det, det.Add(90*time.Minute), "", false)
	if c.DetectedIST != "21 Nov 2026 02:00 IST" || c.DeadlineIST != "21 Nov 2026 08:00 IST" || c.Deadline != "2026-11-21T08:00:00+05:30" || c.LeftMin != 270 || c.ElapsedMin != 90 || c.Overdue {
		t.Fatalf("clock: %+v", c)
	}
	if c := certInClock(det, det.Add(7*time.Hour), "", false); !c.Overdue || c.LeftMin != -60 || !strings.Contains(c.Line, "ran out") {
		t.Fatalf("overdue: %+v", c)
	}
	if c := certInClock(det, det.Add(7*time.Hour), "2026-11-21T08:30:00+05:30", false); !c.Reported || !c.Late || c.Overdue {
		t.Fatalf("late report: %+v", c)
	}
	if c := certInClock(det, det.Add(7*time.Hour), "", true); c.Overdue {
		t.Fatalf("closed unreported still counting: %+v", c)
	}

	o, ctx, _, clock := secSetup(t)
	*clock = det.In(ist)
	v, err := o.RecordIncident(ctx, T, "vikram.shah", RecordIncidentInput{Title: "Phishing mail clicked by a dealer", Description: "A dealer opened a link in a mail claiming to be from the exchange.", Severity: SevHigh, Kind: "phishing"})
	if err != nil || v.Clock.DeadlineIST != "21 Nov 2026 08:00 IST" {
		t.Fatalf("recorded: %+v %v", v.Clock, err)
	}
	if _, err := o.RecordIncident(ctx, T, "priya.nair", RecordIncidentInput{Title: "Something odd", Severity: SevLow}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker recorded: %v", err)
	}
	*clock = clock.Add(2 * time.Hour)
	if _, err := o.ReportIncident(ctx, T, "vikram.shah", v.ID, IncidentReport{To: "CERT-In", How: "Email"}); err == nil {
		t.Fatal("report without a reference")
	}
	if _, err := o.ReportIncident(ctx, T, "vikram.shah", v.ID, IncidentReport{To: "CERT-In", How: "Email", At: "2026-11-21T03:30", Reference: "CERTIn-TEST-0001"}); err != nil {
		t.Fatal(err)
	}
	r, err := o.ReportIncident(ctx, T, "vikram.shah", v.ID, IncidentReport{To: "SEBI", How: "Portal", Reference: "SEBI-TEST-77"})
	if err != nil || r.Status != IncReported || !r.Clock.Reported || r.Clock.Late || r.Clock.ReportedAt != "2026-11-21T03:30:00+05:30" || len(r.Reports) != 2 {
		t.Fatalf("reported: %+v %+v %v", r.Clock, r.Reports, err)
	}
}

// TestPlatformIncidentNotifiesAllFirms: an incident the provider declares
// reaches every firm on the server at once: its register, an e-mail to its
// contacts and its work queues; an update reaches them too.
func TestPlatformIncidentNotifiesAllFirms(t *testing.T) {
	o, ctx, m, _ := secSetup(t)
	const B = "beta"
	if err := o.SeedDev(ctx, B); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveSecuritySettings(ctx, B, "admin", SecuritySettings{Contacts: []string{"it@beta.example"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.DeclarePlatformIncident(ctx, []string{T, B}, "ops-lead", PlatformIncidentInput{Title: "x", Severity: SevHigh, Kind: "Outage"}); err == nil {
		t.Fatal("declared without a description")
	}
	if _, err := o.DeclarePlatformIncident(ctx, []string{T, B}, "ops-lead", PlatformIncidentInput{Title: "Database outage", Description: "The primary database stopped.", Severity: SevHigh, Kind: "Outage", Tenants: []string{"gamma"}}); err == nil {
		t.Fatal("unknown firm accepted")
	}
	p, err := o.DeclarePlatformIncident(ctx, []string{T, B}, "ops-lead", PlatformIncidentInput{Title: "Unauthorised access to a backup store", Description: "A backup bucket was readable from outside for two hours; firms' encrypted backups were exposed.", Severity: SevCritical, Kind: "Breach", Started: "2026-11-20T07:30"})
	if err != nil || p.ID != "PINC-000001" || len(p.Affected) != 2 {
		t.Fatalf("declared: %+v %v", p, err)
	}
	for _, tn := range []string{T, B} {
		sc, err := o.Incidents(ctx, tn, "admin")
		if err != nil || len(sc.Incidents) != 1 || sc.Incidents[0].ID != p.ID || sc.Incidents[0].Source != "provider" || sc.Incidents[0].Clock.DeadlineIST != "20 Nov 2026 16:00 IST" {
			t.Fatalf("%s register: %+v %v", tn, sc.Incidents, err)
		}
		ws, _ := o.WorkItems(ctx, tn, "Open")
		n := 0
		for _, w := range ws {
			if w.Process == "security" {
				n++
			}
		}
		if n != 2 {
			t.Fatalf("%s work items: %d", tn, n)
		}
	}
	if mailsTo(m, "soc@bhairav.example", "[Ledgerline incident notice] PINC-000001") != 1 || mailsTo(m, "it@beta.example", "[Ledgerline incident notice] PINC-000001") != 1 {
		t.Fatalf("notices: %+v", m.got)
	}
	if _, err := o.UpdatePlatformIncident(ctx, "ops-lead", p.ID, PlatformIncidentInput{Status: IncClosed, Note: "Access closed and keys rotated."}); err != nil {
		t.Fatal(err)
	}
	d, err := o.IncidentByID(ctx, B, "admin", p.ID)
	if err != nil || d.ProviderStatus != IncClosed || d.Status != IncOpen || !strings.Contains(d.Notes[len(d.Notes)-1].Text, "keys rotated") || len(d.EventList) != 2 {
		t.Fatalf("firm copy after update: %+v %v", d.Incident, err)
	}
	if mailsTo(m, "it@beta.example", "[Ledgerline incident update]") != 1 {
		t.Fatal("no update mail")
	}
	ps, _ := o.PlatformIncidents(ctx)
	if len(ps) != 1 || ps[0].Status != IncClosed {
		t.Fatalf("platform record: %+v", ps)
	}
	evs, _ := docs.List[SecEvent](ctx, o.Docs, PlatformTenant, cSecEvent, "")
	if len(evs) != 2 || evs[0].Kind != "platform.incident" {
		t.Fatalf("operator stream: %+v", evs)
	}
}

// TestRetentionMinimum: security logs are kept at least 180 days; the
// default is 365; events past the period lose their personal details.
func TestRetentionMinimum(t *testing.T) {
	o, ctx, _, clock := secSetup(t)
	if _, err := o.SaveSecuritySettings(ctx, T, "vikram.shah", SecuritySettings{RetentionDays: 90}); err == nil || !strings.Contains(err.Error(), "180") {
		t.Fatalf("90 days accepted: %v", err)
	}
	if s, err := o.SaveSecuritySettings(ctx, T, "vikram.shah", SecuritySettings{}); err != nil || s.RetentionDays != 365 {
		t.Fatalf("default: %+v %v", s, err)
	}
	if s, err := o.SaveSecuritySettings(ctx, T, "vikram.shah", SecuritySettings{RetentionDays: 180}); err != nil || s.RetentionDays != 180 {
		t.Fatalf("180: %+v %v", s, err)
	}
	if _, err := o.SaveSecuritySettings(ctx, T, "priya.nair", SecuritySettings{RetentionDays: 365}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker changed settings: %v", err)
	}
	if !strings.Contains(retentionLine(o.SecuritySettings(ctx, T)), "180 days") {
		t.Fatal("retention not stated")
	}
	old := o.SecurityEvent(ctx, T, SecEvent{Kind: "signin.ok", Actor: "priya.nair", Subject: "priya.nair", IP: "198.51.100.9"})
	*clock = clock.Add(179 * 24 * time.Hour)
	o.securityTick(ctx, T, *clock)
	if e, _, _ := docs.Get[SecEvent](ctx, o.Docs, T, cSecEvent, old.ID); e.Purged || e.IP == "" {
		t.Fatalf("purged before 180 days: %+v", e)
	}
	*clock = clock.Add(3 * 24 * time.Hour)
	o.securityTick(ctx, T, *clock)
	if e, _, _ := docs.Get[SecEvent](ctx, o.Docs, T, cSecEvent, old.ID); !e.Purged || e.IP != "" || e.Actor != "" || e.Kind != "signin.ok" {
		t.Fatalf("not purged after the period: %+v", e)
	}
}

// TestSignInChecks: a sign-in from a new address is an event; with the
// firm's networks set, one from outside opens an incident.
func TestSignInChecks(t *testing.T) {
	o, ctx, _, clock := secSetup(t)
	if _, _, err := o.Login(ctx, T, "farhan.khan", DevPassword, "", "10.1.2.3", "test"); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Minute)
	o.Login(ctx, T, "farhan.khan", DevPassword, "", "10.1.2.4", "test")
	evs, _ := o.SecEvents(ctx, T, "admin", "2026-11-20", "2026-11-20", "signin.new_ip", 0)
	if len(evs) != 1 || evs[0].IP != "10.1.2.4" {
		t.Fatalf("new address: %+v", evs)
	}
	if _, err := o.SaveSecuritySettings(ctx, T, "admin", SecuritySettings{AllowedNetworks: []string{"10.1.0.0/16", "not-a-net"}}); err == nil {
		t.Fatal("bad network accepted")
	}
	if _, err := o.SaveSecuritySettings(ctx, T, "admin", SecuritySettings{AllowedNetworks: []string{"10.1.0.0/16"}}); err != nil {
		t.Fatal(err)
	}
	o.Login(ctx, T, "farhan.khan", DevPassword, "", "10.1.9.9", "test")
	if n := len(incidents(t, o, ctx, T)); n != 0 {
		t.Fatalf("inside the network opened %d", n)
	}
	o.Login(ctx, T, "farhan.khan", DevPassword, "", "203.0.113.50", "test")
	xs := incidents(t, o, ctx, T)
	if len(xs) != 1 || xs[0].Rule != "outside-ip" || xs[0].Severity != SevHigh {
		t.Fatalf("outside: %+v", xs)
	}
	*clock = clock.Add(time.Minute)
	o.NoteDownload(ctx, T, "farhan.khan", "/v1/clients/export", 80<<20, "10.1.2.3")
	if xs := incidents(t, o, ctx, T); len(xs) != 2 || xs[0].Rule != "download" {
		t.Fatalf("large download: %+v", xs)
	}
}
