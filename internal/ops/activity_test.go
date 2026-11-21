package ops

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"ledgerline"
	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/screens"
	"ledgerline/internal/vault"
)

// activityFixture is a firm with the development users and two clients,
// one at the Bengaluru branch under AP001 and one at Delhi, and a clock
// the test moves (both the audit trail's and Ops's).
type activityFixture struct {
	o   *Ops
	ctx context.Context
	now time.Time
}

func newActivityFixture(t *testing.T, at time.Time) *activityFixture {
	t.Helper()
	reg, err := ingest.LoadFS(ledgerline.FS, "config/adapters")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := ledgerline.FS.Open("config/charges/sample-schedule.json")
	sched, err := charges.LoadSchedule(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	x := &activityFixture{o: New(a), ctx: context.Background(), now: at}
	x.o.Now = func() time.Time { return x.now }
	a.Now = func() time.Time { return x.now }
	if err := x.o.SeedDev(x.ctx, T); err != nil {
		t.Fatal(err)
	}
	for _, c := range []Client{{UCC: "BLR01", Name: "Test Client One", Branch: "Bengaluru", Partner: "AP001", Status: "Active"}, {UCC: "DEL01", Name: "Test Client Two", Branch: "Delhi", Status: "Active"}} {
		if err := docs.Upsert(x.ctx, x.o.Docs, T, cClient, c.UCC, c, "test"); err != nil {
			t.Fatal(err)
		}
	}
	return x
}

func (x *activityFixture) at(s string) {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, ist)
	if err != nil {
		panic(err)
	}
	x.now = t
}

func (x *activityFixture) log(actor, action, object string, d map[string]any) {
	x.o.log(x.ctx, T, actor, action, object, d)
}

func (x *activityFixture) feed(t *testing.T, actor string, q ActivityQuery) ActivityFeed {
	t.Helper()
	f, err := x.o.Activity(x.ctx, T, actor, q)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func actTexts(f ActivityFeed) []string {
	var out []string
	for _, e := range f.Entries {
		out = append(out, e.Text)
	}
	return out
}

func actHas(f ActivityFeed, sub string) bool {
	for _, e := range f.Entries {
		if strings.Contains(e.Text, sub) {
			return true
		}
	}
	return false
}

func TestActivityWeekWindow(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		now, from string
		ended     bool
	}{
		{"2026-06-01T00:00:00+05:30", "2026-06-01T00:00:00+05:30", false}, // Monday 00:00 starts the week
		{"2026-06-03T13:00:00+05:30", "2026-06-01T00:00:00+05:30", false},
		{"2026-06-06T23:59:00+05:30", "2026-06-01T00:00:00+05:30", false}, // Saturday 23:59 is still the week
		{"2026-06-07T00:00:00+05:30", "2026-06-01T00:00:00+05:30", true},  // Sunday shows the week just ended
		{"2026-06-07T23:59:59+05:30", "2026-06-01T00:00:00+05:30", true},
		{"2026-06-08T00:00:00+05:30", "2026-06-08T00:00:00+05:30", false}, // and Monday starts fresh
		// IST, not UTC: Sunday 20:00 UTC is Monday 01:30 IST.
		{"2026-06-07T20:00:00Z", "2026-06-08T00:00:00+05:30", false},
		{"2026-06-07T18:29:59Z", "2026-06-01T00:00:00+05:30", true},
		{"2026-06-07T18:30:00Z", "2026-06-08T00:00:00+05:30", false},
		// Monday 02:00 IST is still Sunday in UTC.
		{"2026-06-07T20:30:00Z", "2026-06-08T00:00:00+05:30", false},
		// Across a year end.
		{"2027-01-02T10:00:00+05:30", "2026-12-28T00:00:00+05:30", false},
	}
	for _, c := range cases {
		w := activityWeek(at(c.now))
		if w.From != c.from || w.Ended != c.ended {
			t.Errorf("%s: from %s ended %v, want %s %v", c.now, w.From, w.Ended, c.from, c.ended)
		}
		if !w.to.Equal(w.from.AddDate(0, 0, 7)) || !strings.HasSuffix(w.Ends, "T23:59:00+05:30") {
			t.Errorf("%s: to %s ends %s", c.now, w.To, w.Ends)
		}
	}
	if w := activityWeek(at("2026-06-03T13:00:00+05:30")); w.Label != "Mon 1 Jun – Sat 6 Jun 2026" {
		t.Errorf("label %q", w.Label)
	}
	if w := activityWeek(at("2027-01-02T10:00:00+05:30")); w.Label != "Mon 28 Dec 2026 – Sat 2 Jan 2027" {
		t.Errorf("label across the year %q", w.Label)
	}
}

func TestActivityFeedWeek(t *testing.T) {
	x := newActivityFixture(t, time.Time{})
	x.at("2026-05-30 18:00") // Saturday of the week before
	x.log("priya.nair", "recon.run", "R-1", map[string]any{"date": "2026-05-29", "matched": 10, "breaks": 0})
	x.at("2026-05-31 23:50") // Sunday night, IST
	x.log(SystemActor, "backup.created", "B-sun", nil)
	x.at("2026-06-01 00:00") // Monday 00:00 IST
	x.log(SystemActor, "file.ingested", "F-1", map[string]any{"source": "NSE-CM-TRADE", "name": "trades.csv", "parsed": 1204, "added": 1204, "duplicates": 0})
	x.at("2026-06-03 10:15")
	x.log("farhan.khan", "payout.batch", "PB-12", map[string]any{"items": 14, "total": "320000.00"})
	x.at("2026-06-06 23:59") // Saturday 23:59
	x.log("anita.rao", "rms.override.created", "OV-1", map[string]any{"ucc": "BLR01", "amount": "50000", "reason": "cheque deposited"})

	// Saturday night: the whole week since Monday 00:00, nothing before.
	f := x.feed(t, "admin", ActivityQuery{})
	got := actTexts(f)
	want := []string{
		"Anita Rao asked for an RMS limit override of ₹50,000 for BLR01",
		"Farhan Khan released payout batch PB-12 (14 payouts, ₹3.2 lakh)",
		"Ledgerline loaded NSE CM trade file (1,204 trades)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("week feed:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if e := f.Entries[2]; e.Kind != "ledgerline" || e.Who != "Ledgerline" || e.Via != "autopilot" || e.View != "files" || e.Area != AreaOperations {
		t.Fatalf("system line %+v", e)
	}
	if e := f.Entries[0]; e.UCC != "BLR01" || e.View != "rms" || e.Area != AreaRisk || e.Day != "2026-06-06" || e.At != "2026-06-06T23:59:00+05:30" {
		t.Fatalf("override line %+v", e)
	}
	if f.Week.From != "2026-06-01T00:00:00+05:30" || f.Week.Ended {
		t.Fatalf("week %+v", f.Week)
	}
	// Sunday: the week just ended is still shown, with Sunday's own entries.
	x.at("2026-06-07 09:00")
	x.log(SystemActor, "backup.created", "B-1", nil)
	f = x.feed(t, "admin", ActivityQuery{})
	if !f.Week.Ended || len(f.Entries) != 4 || f.Entries[0].Text != "Ledgerline made backup B-1" {
		t.Fatalf("Sunday: %+v %v", f.Week, actTexts(f))
	}
	// Monday 00:00 IST: a fresh week.
	x.at("2026-06-08 00:00")
	f = x.feed(t, "admin", ActivityQuery{})
	if len(f.Entries) != 0 || f.Week.From != "2026-06-08T00:00:00+05:30" {
		t.Fatalf("new week: %v", actTexts(f))
	}
	if !f.AuditLink {
		t.Fatal("head office should get the Audit trail link")
	}
}

func TestActivityVisibility(t *testing.T) {
	x := newActivityFixture(t, time.Time{})
	x.at("2026-06-02 07:00")
	x.log("priya.nair", "auth.login", "priya.nair", map[string]any{"ip": "10.0.0.5"})
	x.log("admin", "connection.secret_rotated", "bse-ucc", map[string]any{"key": "password"})
	x.log("system:support-outage-rule", "support_access.approved", "SA-1", map[string]any{"hours": 2})
	x.log("admin", "firmkey.rotated", "aws", nil)
	x.log("priya.nair", "receipt.recorded", "RC-1", map[string]any{"ucc": "BLR01", "mode": "NEFT", "amount": "25000"})
	x.log("priya.nair", "receipt.recorded", "RC-2", map[string]any{"ucc": "DEL01", "mode": "NEFT", "amount": "40000"})
	x.log("meera.iyer", "change.requested", "CR-1", map[string]any{"ucc": "BLR01", "kind": "bank_account"})
	x.log(SystemActor, "alert.raised", "A-1", map[string]any{"type": "TURNOVER", "ucc": "BLR01", "severity": "High"})
	x.log(SystemActor, "billing.run", "2026-06-01", map[string]any{"posted": 1, "client": "BLR01"})
	x.log("admin", "settings.saved", "settings", nil)
	x.log("priya.nair", "dp.bo_opened", "BLR01", map[string]any{"bo": "1208160000001234", "request": "Q-1"})
	x.log("vikram.shah", "client.reveal", "BLR01", map[string]any{"field": "PAN", "reason": "KRA mismatch on ABCDE1234F"})
	x.log("priya.nair", "chat.message", "ops", map[string]any{"text": "private words"})
	x.log("priya.nair", "connection.used", "nse", nil)

	// Security-sensitive lines: the Admin and Compliance only.
	for _, who := range []string{"admin", "vikram.shah"} {
		f := x.feed(t, who, ActivityQuery{})
		for _, s := range []string{"Priya Nair signed in", "changed the password of connection bse-ucc", "approved support access SA-1", "rotated the firm's encryption key", "looked at BLR01's full pan"} {
			if !actHas(f, s) {
				t.Errorf("%s does not see %q: %v", who, s, actTexts(f))
			}
		}
	}
	for _, who := range []string{"priya.nair", "farhan.khan", "anita.rao"} {
		f := x.feed(t, who, ActivityQuery{})
		if actHas(f, "signed in") || actHas(f, "password") || actHas(f, "support access") || actHas(f, "encryption key") || actHas(f, "full pan") {
			t.Errorf("%s sees a security line: %v", who, actTexts(f))
		}
		if !actHas(f, "RC-1") || !actHas(f, "RC-2") || !actHas(f, "changed the firm's settings") {
			t.Errorf("%s misses ordinary lines: %v", who, actTexts(f))
		}
	}
	// Left out for everyone: team messages and every connection call.
	if f := x.feed(t, "admin", ActivityQuery{}); actHas(f, "private words") || actHas(f, "ops") && actHas(f, "chat") || actHas(f, "connection nse") {
		t.Errorf("quiet actions shown: %v", actTexts(f))
	}
	// Branch staff: their branch's clients and their own office's people;
	// nothing firm-wide, nothing from Admin or Depository.
	f := x.feed(t, "meera.iyer", ActivityQuery{})
	if !actHas(f, "RC-1") || !actHas(f, "Meera Iyer raised change request CR-1") || actHas(f, "RC-2") || actHas(f, "settings") || actHas(f, "demat") || actHas(f, "signed in") ||
		actHas(f, "surveillance") || actHas(f, "billed") {
		t.Errorf("branch feed: %v", actTexts(f))
	}
	if f.AuditLink || f.Counts != nil {
		t.Errorf("branch staff get the audit link or counts: %+v", f)
	}
	for _, a := range f.Areas {
		if a == AreaAdmin || a == AreaDepository {
			t.Errorf("branch area %s", a)
		}
	}
	// An authorised person: their own clients only.
	f = x.feed(t, "sunrise.ap", ActivityQuery{})
	if !actHas(f, "RC-1") || actHas(f, "RC-2") || actHas(f, "settings") {
		t.Errorf("AP feed: %v", actTexts(f))
	}
	// A client portal login has no feed.
	_ = docs.Upsert(x.ctx, x.o.Docs, T, cUser, "client.one", User{ID: "client.one", Name: "Test Client One", Role: RoleClient, Active: true, Clients: []string{"BLR01"}}, "test")
	if _, err := x.o.Activity(x.ctx, T, "client.one", ActivityQuery{}); err == nil {
		t.Error("client portal login read the feed")
	}
	// Masking: demat numbers and PANs never appear whole.
	for _, e := range x.feed(t, "admin", ActivityQuery{}).Entries {
		if strings.Contains(e.Text+e.Object, "1208160000001234") || strings.Contains(e.Text+e.Object, "ABCDE1234F") || strings.Contains(e.Text, "KRA mismatch") {
			t.Errorf("identifier or free text in %q", e.Text)
		}
	}
}

func TestActivityPhrases(t *testing.T) {
	users := map[string]User{"farhan.khan": {ID: "farhan.khan", Name: "Farhan Khan", Role: RoleChecker}}
	line := func(actor, action, object string, d map[string]any) ActivityEntry {
		t.Helper()
		e, ok := activityLine(app.AuditEvent{Actor: actor, Action: action, Object: object, Detail: d, At: time.Date(2026, 6, 2, 3, 0, 0, 0, time.UTC)}, users)
		if !ok {
			t.Fatalf("%s left out", action)
		}
		return e
	}
	cases := []struct {
		actor, action, object string
		d                     map[string]any
		want                  string
	}{
		{"farhan.khan", "payout.approved", "PO-7", map[string]any{"ucc": "BLR01", "amount": "125000000"}, "Farhan Khan approved payout PO-7 (₹12.5 crore) for BLR01"},
		{"farhan.khan", "payout.approved", "PO-8", nil, "Farhan Khan approved payout PO-8"}, // optional parts drop out
		{"system:scheduler", "notes.sent", "2026-06-01", map[string]any{"sent": 120400, "bounced": 2}, "Ledgerline sent contract notes for 1 Jun (1,20,400 notes sent, 2 bounces)"},
		{"bank", "bank.credit.live", "UTR1", map[string]any{"amount": "999.40", "ucc": "DEL01"}, "Ledgerline received a bank credit of ₹999 for DEL01"},
		{"client:BLR01", "change.client_confirmed", "CR-9", nil, "Client BLR01 confirmed change request CR-9"},
		{"applicant:A-1", "eapply.submitted", "A-1", nil, "An applicant submitted online application A-1"},
		{"support:asha@demo", "support_access.session_started", "SA-2", nil, "Ledgerline support (asha) started support session SA-2"},
		{"farhan.khan", "client.privacy", "BLR01", map[string]any{"private": true}, "Farhan Khan made BLR01 private"},
		{"farhan.khan", "autopilot.schedule", "", map[string]any{"on": false}, "Farhan Khan switched off the autopilot schedule"},
		{"farhan.khan", "brokerage.assigned", "ucc:BLR01", map[string]any{"plan": "hni-flat"}, "Farhan Khan moved BLR01 to brokerage plan hni-flat"},
		{"system:scheduler", "statements.sent", "ras_statement 2026-06-01", map[string]any{"sent": 42, "bounced": 0}, "Ledgerline sent running-account statements for 1 Jun (42 sent)"},
		{"system:scheduler", "billing.run", "2026-06-01", map[string]any{"posted": 3, "client": "BLR01"}, "Ledgerline billed BLR01 again for 1 Jun"},
		{"system:scheduler", "billing.run", "2026-06-01", map[string]any{"posted": 1}, "Ledgerline ran billing for 1 Jun: 1 bill posted"},
		// Fallbacks: the module's name, a verb from the last part when it reads as one, the object.
		{"farhan.khan", "mcxucc.api_sync", "B-12", nil, "Farhan Khan recorded MCX UCC api sync (B-12)"},
		{"farhan.khan", "dealer.imported", "dealers.csv", nil, "Farhan Khan imported dealer (dealers.csv)"},
		{"farhan.khan", "widget.frobbed", "", nil, "Farhan Khan recorded widget frobbed"},
		{"farhan.khan", "cdsl.pledge.result", "PL-1", nil, "Farhan Khan recorded CDSL pledge result (PL-1)"},
		{"unknown.user", "mf.sip.paused", "S-1", nil, "unknown.user recorded MF SIP paused (S-1)"},
		// Identifiers in an object are masked.
		{"farhan.khan", "verify.pan", "ABCDE1234F", nil, "Farhan Khan recorded verification pan (ABCDEXXXXF)"},
		{"farhan.khan", "payout.account", "PO-1", map[string]any{"account": "50200012345678"}, "Farhan Khan recorded payout account (PO-1)"},
	}
	for _, c := range cases {
		if got := line(c.actor, c.action, c.object, c.d).Text; got != c.want {
			t.Errorf("%s: %q\n want %q", c.action, got, c.want)
		}
	}
	if e := line("farhan.khan", "widget.frobbed", "", nil); e.Area != AreaOperations || e.View != "audit" {
		t.Errorf("unknown module: area %s view %s", e.Area, e.View)
	}
	if e := line("farhan.khan", "cdsl.pledge.result", "PL-1", nil); e.Area != AreaRisk || e.View != "cdslpledge" {
		t.Errorf("two-part module: %s %s", e.Area, e.View)
	}
	if e := line("farhan.khan", "cdsl.request", "Q-1", map[string]any{"kind": "closure", "ucc": "BLR01"}); e.Area != AreaDepository || e.UCC != "BLR01" {
		t.Errorf("cdsl request: %+v", e)
	}
	if e := line("farhan.khan", "firmkey.move_failed", "", nil); !e.Secure || e.Area != AreaAdmin {
		t.Errorf("key changes are security-sensitive: %+v", e)
	}
	for _, a := range []string{"chat.dm", "board.created", "connection.used", "support_access.action", "client.document.viewed"} {
		if _, ok := activityLine(app.AuditEvent{Action: a}, users); ok {
			t.Errorf("%s should be left out", a)
		}
	}
	if s := activityINR(320000); s != "₹3.2 lakh" {
		t.Error(s)
	}
	if s := activityINR(100000); s != "₹1 lakh" {
		t.Error(s)
	}
	if s := activityCount(12345678); s != "1,23,45,678" {
		t.Error(s)
	}
}

// Every screen the feed links to is a console screen.
func TestActivityViewsExist(t *testing.T) {
	b, err := os.ReadFile("../../web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	// The console's screens are the catalogue (internal/screens).
	views := map[string]bool{}
	for _, x := range screens.All() {
		views[x.Key] = true
	}
	sv := regexp.MustCompile(`const SCOPED_VIEWS = \[([^\]]*)\]`).FindStringSubmatch(string(b))
	if sv == nil {
		t.Fatal("SCOPED_VIEWS not found in app.js")
	}
	for v := range activityScopedViews {
		if !strings.Contains(sv[1], "'"+v+"'") {
			t.Errorf("%s is not a screen branch staff can open", v)
		}
	}
	for k, m := range activityModules {
		if !views[m.view] {
			t.Errorf("module %s: screen %q", k, m.view)
		}
		if !contains(ActivityAreas, m.area) {
			t.Errorf("module %s: area %q", k, m.area)
		}
	}
	for k, p := range activityPhrases {
		if p.view != "" && !views[p.view] {
			t.Errorf("phrase %s: screen %q", k, p.view)
		}
		if p.area != "" && !contains(ActivityAreas, p.area) {
			t.Errorf("phrase %s: area %q", k, p.area)
		}
		if !p.quiet && p.text == "" && p.fn == nil {
			t.Errorf("phrase %s has no words", k)
		}
	}
}

func TestActivityCursorPagingAndState(t *testing.T) {
	x := newActivityFixture(t, time.Time{})
	x.at("2026-06-01 09:00")
	for i := 0; i < 5; i++ {
		x.log("priya.nair", "receipt.recorded", "RC-M"+string(rune('0'+i)), map[string]any{"ucc": "BLR01", "amount": "100"})
	}
	x.at("2026-06-02 08:00")
	x.log(SystemActor, "backup.created", "B-2", nil)
	x.log("farhan.khan", "payout.approved", "PO-1", map[string]any{"ucc": "DEL01", "amount": "100"})

	// Pages of two, newest first, then older with before=.
	f := x.feed(t, "admin", ActivityQuery{Limit: 2})
	if len(f.Entries) != 2 || !f.More || f.Before != f.Entries[1].Seq || f.Entries[0].Text != "Farhan Khan approved payout PO-1 (₹100) for DEL01" {
		t.Fatalf("first page %+v", f)
	}
	seen := map[int64]bool{}
	pages := 0
	for q := (ActivityQuery{Limit: 2}); ; {
		p := x.feed(t, "admin", q)
		for _, e := range p.Entries {
			if seen[e.Seq] {
				t.Fatalf("seq %d twice", e.Seq)
			}
			seen[e.Seq] = true
		}
		pages++
		if !p.More {
			break
		}
		q.Before = p.Before
	}
	if len(seen) != 7 || pages != 4 {
		t.Fatalf("paged %d entries in %d pages", len(seen), pages)
	}
	// Per-person counts for today (Tuesday), for head office.
	if len(f.Counts) != 2 || f.Counts[0].N != 1 {
		t.Fatalf("counts %+v", f.Counts)
	}
	// Polling: nothing new, then only the new line.
	cur := f.Cursor
	if p := x.feed(t, "admin", ActivityQuery{After: cur}); len(p.Entries) != 0 || p.Cursor != cur {
		t.Fatalf("empty poll %+v", p)
	}
	x.at("2026-06-02 08:05")
	x.log("anita.rao", "debit.blocked", "DEL01", map[string]any{"days": 5, "debit": "12000"})
	x.log("priya.nair", "auth.login", "priya.nair", nil) // not for a maker's eyes
	p := x.feed(t, "priya.nair", ActivityQuery{After: cur})
	if len(p.Entries) != 1 || p.Entries[0].Text != "Anita Rao blocked DEL01 for an old debit of ₹12,000" || p.Cursor <= cur {
		t.Fatalf("poll %+v", p)
	}
	// Filters.
	if p := x.feed(t, "admin", ActivityQuery{Only: "ledgerline"}); len(p.Entries) != 1 || p.Entries[0].Kind != "ledgerline" {
		t.Fatalf("only ledgerline %v", actTexts(p))
	}
	if p := x.feed(t, "priya.nair", ActivityQuery{Only: "mine"}); len(p.Entries) != 5 {
		t.Fatalf("only mine %v", actTexts(p))
	}
	if p := x.feed(t, "admin", ActivityQuery{Only: "people", Area: AreaRisk}); len(p.Entries) != 1 || p.Entries[0].Actor != "anita.rao" {
		t.Fatalf("people in risk %v", actTexts(p))
	}
	if p := x.feed(t, "admin", ActivityQuery{Role: RoleChecker}); len(p.Entries) != 1 {
		t.Fatalf("role filter %v", actTexts(p))
	}
	if p := x.feed(t, "admin", ActivityQuery{Text: "payout del01"}); len(p.Entries) != 1 {
		t.Fatalf("search %v", actTexts(p))
	}
	if _, err := x.o.Activity(x.ctx, T, "admin", ActivityQuery{Only: "everyone"}); err == nil {
		t.Fatal("bad filter accepted")
	}
	// The marker: seen moves forward only; unseen counts what is newer.
	open := true
	if _, err := x.o.SaveActivityState(x.ctx, T, "admin", ActivityStatePatch{Open: &open, Seen: &cur}); err != nil {
		t.Fatal(err)
	}
	old := int64(1)
	st, _ := x.o.SaveActivityState(x.ctx, T, "admin", ActivityStatePatch{Seen: &old})
	if st.Seen != cur || !st.Open {
		t.Fatalf("state %+v", st)
	}
	f = x.feed(t, "admin", ActivityQuery{})
	if f.Seen != cur || f.Unseen != 2 || !f.Open { // the block and the sign-in
		t.Fatalf("unseen %d seen %d open %v", f.Unseen, f.Seen, f.Open)
	}
	if f2 := x.feed(t, "farhan.khan", ActivityQuery{}); f2.Open || f2.Seen != 0 || f2.Unseen != 0 {
		t.Fatalf("state is per user: %+v", f2)
	}
}

// Someone holding a role the firm made sees only lines about the screens
// the role includes; security lines and the per-person counts need more.
func TestActivityFollowsRoles(t *testing.T) {
	x := newActivityFixture(t, time.Time{})
	x.at("2026-06-02 07:00")
	for _, u := range []User{{ID: "admin2", Name: "Second Admin", Role: RoleAdmin, Active: true}, {ID: "ria.desk", Name: "Ria", Role: RoleMaker, Active: true}} {
		if err := x.o.PutUser(x.ctx, T, "admin", u); err != nil {
			t.Fatal(err)
		}
	}
	c, err := x.o.ProposeRole(x.ctx, T, "admin", RoleDef{Name: "Receipts desk", Screens: map[string]Power{"money": {Do: true}, "steps": {View: true}}})
	if err == nil {
		_, err = x.o.DecideRoleChange(x.ctx, T, "admin2", c.ID, true, "")
	}
	if err == nil {
		c, err = x.o.ProposeUserRoles(x.ctx, T, "admin", "ria.desk", []string{"Receipts desk"}, "", "")
	}
	if err == nil {
		_, err = x.o.DecideRoleChange(x.ctx, T, "admin2", c.ID, true, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	x.log("priya.nair", "auth.login", "priya.nair", map[string]any{"ip": "10.0.0.5"})
	x.log("priya.nair", "receipt.recorded", "RC-1", map[string]any{"ucc": "BLR01", "mode": "NEFT", "amount": "25000"})
	x.log(SystemActor, "billing.run", "2026-06-01", map[string]any{"posted": 1, "client": "BLR01"})
	x.log("admin", "settings.saved", "settings", nil)
	f := x.feed(t, "ria.desk", ActivityQuery{})
	if !actHas(f, "RC-1") || actHas(f, "billed") || actHas(f, "settings") || actHas(f, "signed in") {
		t.Errorf("receipts desk feed: %v", actTexts(f))
	}
	if f.AuditLink || f.Counts != nil {
		t.Errorf("receipts desk gets the audit link or counts: %+v", f)
	}
	// The Ops maker still sees the whole firm's ordinary lines.
	if f := x.feed(t, "priya.nair", ActivityQuery{}); !actHas(f, "RC-1") || !actHas(f, "changed the firm's settings") {
		t.Errorf("ops maker feed: %v", actTexts(f))
	}
	// Step reminders: View on the screen opens the list.
	if err := x.o.require(WithAreas(x.ctx, []string{"steps"}), T, "ria.desk", "Viewing step reminders", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		t.Fatalf("steps with View: %v", err)
	}
	if err := x.o.require(WithAreas(x.ctx, []string{"steps"}), T, "ria.desk", "Changing step due times and owners", RoleAdmin); err == nil {
		t.Fatal("step settings stay with Admin")
	}
}
