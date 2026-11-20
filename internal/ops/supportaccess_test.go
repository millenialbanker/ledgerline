package ops

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// saOps is a firm with users and a Ledgerline support operator, on a clock
// the test moves.
func saOps(t *testing.T) (*Ops, func(time.Duration), func(SupportAccessInput) SupportAccess) {
	t.Helper()
	o, ctx := newOps(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	if _, _, err := o.SaveSupportOperator(ctx, "provider", "ravi.k", "Ravi K", true, false); err != nil {
		t.Fatal(err)
	}
	ask := func(in SupportAccessInput) SupportAccess {
		t.Helper()
		if in.Tenant == "" {
			in.Tenant = T
		}
		if in.Reason == "" {
			in.Reason = "Trade file for today does not load"
		}
		if in.Ticket == "" {
			in.Ticket = "SR-0042"
		}
		if in.Areas == nil {
			in.Areas = []string{"exchange_files"}
		}
		r, err := o.RequestSupportAccess(ctx, "ravi.k", in)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	return o, func(d time.Duration) { now = now.Add(d) }, ask
}

func TestSupportMasking(t *testing.T) {
	got := SupportMaskText("PAN ABCDE1234F, aadhaar 2345 6789 0123, a/c 50200012345678, ref 2026-11-20, amount 12345.67")
	for _, bad := range []string{"ABCDE1234F", "2345 6789 0123", "50200012345678"} {
		if strings.Contains(got, bad) {
			t.Fatalf("%q left in %q", bad, got)
		}
	}
	for _, keep := range []string{"ABCDEXXXXF", "XXXX XXXX 0123", "XXXXXXXXXX5678", "2026-11-20", "12345.67"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("%q missing from %q", keep, got)
		}
	}
	// JSON: strings and keys are masked, numbers (amounts) are not.
	b, ok := SupportMaskJSON([]byte(`{"pan":"abcde1234f","banks":[{"account":"000123456789012"}],"map":{"50200012345678":"EQ"},"paise":123456789012,"aadhaar":"234567890123"}`))
	if !ok {
		t.Fatal("not JSON")
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, bad := range []string{"abcde1234f", "ABCDE1234F", "000123456789012", "50200012345678", "234567890123"} {
		if strings.Contains(s, bad) {
			t.Fatalf("%q left in %s", bad, s)
		}
	}
	if !strings.Contains(s, "123456789012") || v["pan"] != "ABCDEXXXXF" {
		t.Fatalf("masked wrongly: %s", s)
	}
	if _, ok := SupportMaskJSON([]byte("not json")); ok {
		t.Fatal("text taken as JSON")
	}
}

func TestSupportGate(t *testing.T) {
	ro := &SupportAuth{Areas: []string{"clients"}}
	rw := &SupportAuth{Areas: []string{"clients", "settings"}, Write: true}
	full := &SupportAuth{Areas: []string{"full"}, Write: true}
	cases := []struct {
		a       *SupportAuth
		pattern string
		ok      bool
	}{
		{ro, "GET /v1/clients/{ucc}", true},
		{ro, "GET /v1/clients", true},
		{ro, "POST /v1/changes", false},         // read-only
		{ro, "GET /v1/ledger/statement", false}, // outside the areas
		{ro, "GET /v1/dashboard", false},        // in no area: full only
		{ro, "GET /v1/me", true},                // always
		{ro, "POST /v1/support-access/session/end", true},
		{ro, "POST /v1/clients/{ucc}/reveal", false},
		{rw, "PUT /v1/settings", true},
		{rw, "POST /v1/users", false},     // never changes users
		{rw, "PUT /v1/users/{id}", false}, // never changes users
		{rw, "GET /v1/users", true},       // settings area: may look
		{rw, "POST /v1/connections/{id}", false},
		{full, "GET /v1/dashboard", true},
		{full, "POST /v1/support-access/{id}/approve", false}, // never approves itself
		{full, "PUT /v1/support-access/policy", false},
		{full, "POST /v1/auth/password", false},
		{full, "POST /v1/rpa/agents", false},
		{full, "POST /v1/clients/{ucc}/reveal", false},
	}
	for _, c := range cases {
		err := SupportGate(c.a, c.pattern)
		if (err == nil) != c.ok {
			t.Errorf("%v %s: %v", c.a.Areas, c.pattern, err)
		}
		if err != nil && !errors.Is(err, ErrForbidden) {
			t.Errorf("%s: %v is not forbidden", c.pattern, err)
		}
	}
	if SupportGate(nil, "POST /v1/users") != nil {
		t.Fatal("the firm's own users are gated")
	}
}

func TestSupportAccessFlow(t *testing.T) {
	o, step, ask := saOps(t)
	ctx := t.Context()

	// Nothing before approval: no session, no role.
	r := ask(SupportAccessInput{Areas: []string{"exchange_files", "clients"}, Hours: 3})
	if r.Status != SAPending || r.Actor != "support:ravi.k@"+T || r.Hours != 3 {
		t.Fatalf("request %+v", r)
	}
	if _, err := o.StartSupportSession(ctx, T, "ravi.k", r.ID, "10.0.0.1", "test"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("session before approval: %v", err)
	}
	if _, err := o.RoleOf(ctx, T, r.Actor); !errors.Is(err, ErrForbidden) {
		t.Fatalf("support has a role before approval: %v", err)
	}
	// The firm was told: a work item for the Admin and an e-mail.
	ws, _ := o.WorkItems(ctx, T, "Open")
	found := false
	for _, w := range ws {
		found = found || w.ID == "W-support_access-"+r.ID && w.Role == RoleAdmin && w.View == "supportaccess"
	}
	if !found {
		t.Fatal("no work item for the request")
	}
	mails, _ := docs.List[OutboxItem](ctx, o.Docs, T, cOutbox, "")
	if len(mails) == 0 || mails[len(mails)-1].Ref != "support-access:"+r.ID {
		t.Fatalf("no e-mail: %+v", mails)
	}
	// A second request while one waits is refused.
	if _, err := o.RequestSupportAccess(ctx, "ravi.k", SupportAccessInput{Tenant: T, Reason: "another look please", Ticket: "SR-1", Areas: []string{"ledger"}}); !errors.Is(err, ErrState) {
		t.Fatalf("second request: %v", err)
	}
	// Bad requests.
	for _, in := range []SupportAccessInput{{Tenant: T, Reason: "short", Ticket: "x", Areas: []string{"ledger"}}, {Tenant: T, Reason: "long enough reason", Areas: []string{"ledger"}},
		{Tenant: T, Reason: "long enough reason", Ticket: "x", Areas: []string{"ledger"}, Hours: 9}, {Tenant: T, Reason: "long enough reason", Ticket: "x", Areas: []string{"everything"}}} {
		if _, err := o.RequestSupportAccess(ctx, "ravi.k", in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", in, err)
		}
	}

	// Approval by a non-approver is refused; so is support approving itself.
	if _, err := o.DecideSupportAccess(ctx, T, "priya.nair", r.ID, true, "", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Ops maker approved: %v", err)
	}
	if _, err := o.DecideSupportAccess(ctx, T, "vikram.shah", r.ID, true, "", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Compliance approved without being the firm's approver: %v", err)
	}
	if _, err := o.DecideSupportAccess(ctx, T, r.Actor, r.ID, true, "", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("support approved itself: %v", err)
	}
	// The Admin approves, narrowing to exchange files and 1 hour; widening is refused.
	no := false
	if _, err := o.DecideSupportAccess(ctx, T, "admin", r.ID, true, "", &SupportNarrow{Areas: []string{"ledger"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("widened: %v", err)
	}
	r, err := o.DecideSupportAccess(ctx, T, "admin", r.ID, true, "only the files", &SupportNarrow{Areas: []string{"exchange_files"}, Hours: 1, Write: &no})
	if err != nil || r.Status != SAApproved || len(r.Areas) != 1 || r.Hours != 1 || r.AskedHours != 3 {
		t.Fatalf("approve: %+v %v", r, err)
	}
	if ws, _ := o.WorkItems(ctx, T, "Open"); len(ws) > 0 {
		for _, w := range ws {
			if w.ID == "W-support_access-"+r.ID {
				t.Fatal("work item still open after the decision")
			}
		}
	}

	// The session: marked as support, scoped, Admin role while it lasts.
	tok, err := o.StartSupportSession(ctx, T, "ravi.k", r.ID, "10.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	st, err := o.SessionUser(ctx, T, tok)
	if err != nil || st.Support == nil || st.User != r.Actor || st.Support.Write || st.Support.Areas[0] != "exchange_files" || st.Support.Name != "Ravi K" {
		t.Fatalf("session %+v %v", st, err)
	}
	if role, err := o.RoleOf(ctx, T, r.Actor); err != nil || role != RoleAdmin {
		t.Fatalf("role %q %v", role, err)
	}
	// Never, whatever was approved: users, passwords, reveal, support access.
	if _, err := o.CreateUser(ctx, T, r.Actor, User{ID: "evil", Name: "Evil", Role: RoleAdmin}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("support added a user: %v", err)
	}
	if _, err := o.ResetPassword(ctx, T, r.Actor, "admin"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("support reset a password: %v", err)
	}
	if _, err := o.RevealClient(ctx, T, r.Actor, "X1", "pan", "looking at it"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("support revealed a PAN: %v", err)
	}
	if _, err := o.SetSupportPolicy(ctx, T, r.Actor, SupportPolicy{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("support changed the policy: %v", err)
	}
	// Starting again ends the earlier session.
	tok2, err := o.StartSupportSession(ctx, T, "ravi.k", r.ID, "10.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SessionUser(ctx, T, tok); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("old session still open: %v", err)
	}

	// Expiry: after the approved hour the session and the role end.
	for _, d := range []time.Duration{25 * time.Minute, 25 * time.Minute, 9 * time.Minute} { // used now and then, so never idle
		step(d)
		if _, err := o.SessionUser(ctx, T, tok2); err != nil {
			t.Fatalf("ended early: %v", err)
		}
	}
	step(2 * time.Minute)
	if _, err := o.SessionUser(ctx, T, tok2); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("still open after expiry: %v", err)
	}
	if _, err := o.RoleOf(ctx, T, r.Actor); !errors.Is(err, ErrForbidden) {
		t.Fatalf("role after expiry: %v", err)
	}
	if x, _, _ := docs.Get[SupportAccess](ctx, o.Docs, T, cSupportAccess, r.ID); x.Status != SAExpired {
		t.Fatalf("status after expiry %q", x.Status)
	}

	// End now: effective on the very next request.
	r2 := ask(SupportAccessInput{Areas: []string{"clients"}})
	if _, err := o.DecideSupportAccess(ctx, T, "admin", r2.ID, true, "", nil); err != nil {
		t.Fatal(err)
	}
	tok3, err := o.StartSupportSession(ctx, T, "ravi.k", r2.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.EndSupportAccess(ctx, T, "priya.nair", r2.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Ops maker ended access: %v", err)
	}
	if _, err := o.EndSupportAccess(ctx, T, "admin", r2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SessionUser(ctx, T, tok3); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("session after End now: %v", err)
	}
	if _, err := o.StartSupportSession(ctx, T, "ravi.k", r2.ID, "", ""); err == nil {
		t.Fatal("started after End now")
	}

	// The firm may let the Compliance officer approve.
	if _, err := o.SetSupportPolicy(ctx, T, "vikram.shah", SupportPolicy{ApproverRole: RoleCompliance}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Compliance changed the policy: %v", err)
	}
	if _, err := o.SetSupportPolicy(ctx, T, "admin", SupportPolicy{ApproverRole: RoleCompliance}); err != nil {
		t.Fatal(err)
	}
	r3 := ask(SupportAccessInput{Areas: []string{"settings"}, Write: true})
	if _, err := o.DecideSupportAccess(ctx, T, "vikram.shah", r3.ID, true, "", nil); err != nil {
		t.Fatalf("chosen approver: %v", err)
	}
	tok4, err := o.StartSupportSession(ctx, T, "ravi.k", r3.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Even with write, support does not weaken sign-in.
	set, _ := o.Settings(ctx, T)
	set.TwoFactor = "required"
	if err := o.SaveSettings(ctx, T, r3.Actor, set); !errors.Is(err, ErrForbidden) {
		t.Fatalf("support changed two-factor: %v", err)
	}

	// Block: open access ends, waiting requests are refused, new ones too.
	if _, err := o.SetSupportPolicy(ctx, T, "admin", SupportPolicy{Blocked: true, ApproverRole: RoleCompliance}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SessionUser(ctx, T, tok4); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("session after block: %v", err)
	}
	if x, _, _ := docs.Get[SupportAccess](ctx, o.Docs, T, cSupportAccess, r3.ID); x.Status != SAEnded {
		t.Fatalf("status after block %q", x.Status)
	}
	if _, err := o.RequestSupportAccess(ctx, "ravi.k", SupportAccessInput{Tenant: T, Reason: "need to look again", Ticket: "SR-9", Areas: []string{"clients"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("request while blocked: %v", err)
	}
	if _, err := o.SetSupportPolicy(ctx, T, "admin", SupportPolicy{}); err != nil {
		t.Fatal(err)
	}

	// The log has every step, the support actor's own lines included.
	o.SupportAction(ctx, T, &SupportAuth{Request: r.ID, Operator: "ravi.k"}, "GET", "/v1/exchange-files", 200, "")
	ls, err := o.SupportAccessLog(ctx, T, "vikram.shah", 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range ls {
		seen[l.Action] = true
		if l.Action == "support_access.action" && l.Actor != "support:ravi.k@"+T {
			t.Fatalf("action under %q", l.Actor)
		}
	}
	for _, a := range []string{"support_access.requested", "support_access.approved", "support_access.session_started", "support_access.expired", "support_access.ended",
		"support_access.blocked", "support_access.refused_blocked", "support_access.unblocked", "support_access.action"} {
		if !seen[a] {
			t.Errorf("log has no %s", a)
		}
	}
	csv, err := o.SupportAccessLogCSV(ctx, T, "admin")
	if err != nil || !strings.HasPrefix(string(csv), "Time (IST),By,Event,Request,Details") || !strings.Contains(string(csv), r.ID) {
		t.Fatalf("csv %v %.200s", err, csv)
	}
	if _, err := o.SupportAccessLog(ctx, T, "priya.nair", 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Ops maker read the log: %v", err)
	}
	for _, e := range ls {
		if auditArea(e.Action) == "Other" {
			t.Errorf("no area for %q", e.Action)
		}
	}
}

func TestSupportOutageRuleAndLapse(t *testing.T) {
	o, step, ask := saOps(t)
	ctx := t.Context()
	// Off by default: an outage request waits like any other.
	r := ask(SupportAccessInput{Outage: true})
	if r.Status != SAPending {
		t.Fatalf("let in without the rule: %+v", r)
	}
	// Unanswered for 24 hours, it lapses (the tick closes it).
	step(25 * time.Hour)
	o.supportAccessTick(ctx, T, o.Now())
	if x, _, _ := docs.Get[SupportAccess](ctx, o.Docs, T, cSupportAccess, r.ID); x.Status != SAExpired {
		t.Fatalf("not lapsed: %q", x.Status)
	}
	if _, err := o.DecideSupportAccess(ctx, T, "admin", r.ID, true, "", nil); !errors.Is(err, ErrState) {
		t.Fatalf("approved a lapsed request: %v", err)
	}
	if _, err := o.SetSupportPolicy(ctx, T, "admin", SupportPolicy{OutageReadOnly: true, OutageHours: 1}); err != nil {
		t.Fatal(err)
	}
	// Write or full is never let in by the rule.
	w := ask(SupportAccessInput{Outage: true, Write: true})
	if w.Status != SAPending {
		t.Fatalf("write let in by the rule: %+v", w)
	}
	if _, err := o.EndOwnSupportAccess(ctx, T, "ravi.k", w.ID); err != nil {
		t.Fatal(err)
	}
	a := ask(SupportAccessInput{Outage: true, Hours: 4})
	if a.Status != SAApproved || a.Hours != 1 || a.DecidedBy != supportOutageActor {
		t.Fatalf("outage rule: %+v", a)
	}
	if _, err := o.StartSupportSession(ctx, T, "ravi.k", a.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	// A deactivated operator's session ends.
	tok, _ := o.StartSupportSession(ctx, T, "ravi.k", a.ID, "", "")
	if _, _, err := o.SaveSupportOperator(ctx, "provider", "ravi.k", "Ravi K", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SessionUser(ctx, T, tok); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("deactivated operator still in: %v", err)
	}
	// Firm users are untouched by all of this.
	if role, err := o.RoleOf(ctx, T, "priya.nair"); err != nil || role != RoleMaker {
		t.Fatalf("firm user %q %v", role, err)
	}
}
