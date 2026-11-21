package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/money"
	"ledgerline/internal/screens"
)

// rolesSetup is the sample firm with a second Admin (role changes need
// two) and the people the tests give roles to.
func rolesSetup(t *testing.T) (*Ops, context.Context) {
	t.Helper()
	o, ctx := setup(t)
	for _, u := range []User{{ID: "admin2", Name: "Second Admin", Role: RoleAdmin, Active: true},
		{ID: "neha.brok", Name: "Neha", Role: RoleMaker, Active: true}, {ID: "ravi.pay", Name: "Ravi", Role: RoleMaker, Active: true}} {
		if err := o.PutUser(ctx, T, "admin", u); err != nil {
			t.Fatal(err)
		}
	}
	return o, ctx
}

// approveRole proposes as admin and approves as admin2.
func approveRole(t *testing.T, o *Ops, ctx context.Context, c RoleChange, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideRoleChange(ctx, T, "admin2", c.ID, true, ""); err != nil {
		t.Fatal(err)
	}
}

func on(ctx context.Context, screens ...string) context.Context { return WithAreas(ctx, screens) }

func TestBuiltinTemplates(t *testing.T) {
	for _, n := range Roles {
		d, ok := BuiltinRole(n)
		if !ok || !d.BuiltIn || d.Template != n || len(d.Screens) == 0 {
			t.Fatalf("%s: %+v", n, d)
		}
		if err := checkRole(&d); err != nil && n != RoleAdmin {
			t.Errorf("%s does not pass its own checks: %v", n, err)
		}
		for k := range d.Screens {
			if _, ok := screens.Get(k); !ok {
				t.Errorf("%s: unknown screen %s", n, k)
			}
		}
	}
	for r, w := range builtinWork {
		for _, k := range strings.Fields(w.do + " " + w.approve) {
			if _, ok := screens.Get(k); !ok {
				t.Errorf("builtinWork[%s]: unknown screen %s", r, k)
			}
		}
	}
	b, _ := BuiltinRole(RoleBranch)
	if b.Limits.Scope != ScopeBranch || b.Screens["settlement"].View || !b.Screens["clients"].Do {
		t.Fatalf("branch template: %+v", b.Limits)
	}
	if mk, _ := BuiltinRole(RoleMaker); mk.Screens["brokerage"].Do || !mk.Screens["brokerage"].View || !mk.Screens["money"].Do || mk.Screens["money"].Approve {
		t.Fatalf("maker template: %+v %+v", mk.Screens["brokerage"], mk.Screens["money"])
	}
	for what, want := range map[string]string{"Viewing BOW limits": kindView, "Approving payouts": kindApprove, "Changing brokerage plans": kindDo,
		"Deciding whether a circular applies": kindDo, "Recording a receipt": kindDo} {
		if got := actionKind(what, []string{RoleChecker}); got != want {
			t.Errorf("%s: %s", what, got)
		}
	}
	if actionKind("Deciding a pledge request", []string{RoleMaker, RoleChecker}) != kindDo {
		t.Fatal("a maker's decision is doing")
	}
}

// An unedited built-in role is checked by name, exactly as before, even
// with screens in the context.
func TestBuiltinRolesUnchanged(t *testing.T) {
	o, ctx := rolesSetup(t)
	for _, c := range []struct {
		who, what string
		roles     []string
		ok        bool
	}{
		{"priya.nair", "Recording a receipt", []string{RoleMaker, RoleChecker, RoleBranch}, true},
		{"priya.nair", "Changing brokerage plans", []string{RoleChecker}, false},
		{"farhan.khan", "Changing brokerage plans", []string{RoleChecker}, true},
		{"anita.rao", "Approving payouts", []string{RoleChecker, RoleRisk}, true},
		{"vikram.shah", "Approving payouts", []string{RoleChecker, RoleRisk}, false},
		{"admin", "Changing settings", []string{RoleAdmin}, true},
		{"meera.iyer", "Changing settings", []string{RoleAdmin}, false},
	} {
		for _, cx := range []context.Context{ctx, on(ctx, "money"), on(ctx, "brokerage", "settings")} {
			err := o.require(cx, T, c.who, c.what, c.roles...)
			if (err == nil) != c.ok {
				t.Errorf("%s %s: %v", c.who, c.what, err)
			}
		}
	}
	if err := o.require(ctx, T, "priya.nair", "Changing brokerage plans", RoleChecker); err == nil || !strings.Contains(err.Error(), "(you are Ops maker)") {
		t.Fatalf("message: %v", err)
	}
	if r, _ := o.RoleOf(ctx, T, "meera.iyer"); r != RoleBranch {
		t.Fatal(r)
	}
}

// "Brokerage settings for only one": a role started blank with Do on
// Brokerage plans changes plans there, and nowhere else.
func TestCustomRoleScreens(t *testing.T) {
	o, ctx := rolesSetup(t)
	c, err := o.ProposeRole(ctx, T, "admin", RoleDef{Name: "Brokerage desk", About: "Brokerage plans", Screens: map[string]Power{"brokerage": {Do: true}, "charges": {View: true}}})
	approveRole(t, o, ctx, c, err)
	c, err = o.ProposeUserRoles(ctx, T, "admin", "neha.brok", []string{"Brokerage desk"}, "", "")
	approveRole(t, o, ctx, c, err)
	if err := o.require(on(ctx, "brokerage"), T, "neha.brok", "Changing brokerage plans", RoleChecker); err != nil {
		t.Fatalf("brokerage desk changes plans: %v", err)
	}
	if err := o.require(on(ctx, "money"), T, "neha.brok", "Recording a receipt", RoleMaker, RoleChecker, RoleBranch); !errors.Is(err, ErrForbidden) {
		t.Fatalf("brokerage desk records a receipt: %v", err)
	}
	if err := o.require(ctx, T, "neha.brok", "Changing brokerage plans", RoleChecker); !errors.Is(err, ErrForbidden) {
		t.Fatalf("no screen, no power for a blank role: %v", err)
	}
	// Admin-only actions stay with Admin on ordinary screens.
	if err := o.require(on(ctx, "brokerage"), T, "neha.brok", "Setting the firm's default dashboard", RoleAdmin); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin-only action: %v", err)
	}
	// Do without Approve cannot approve.
	if err := o.require(on(ctx, "brokerage"), T, "neha.brok", "Approving vouchers", RoleChecker); !errors.Is(err, ErrForbidden) {
		t.Fatalf("approves without Approve: %v", err)
	}
	a, _ := o.Access(ctx, T, "neha.brok")
	if !a.Gated() || !a.Allows([]string{"brokerage"}, true) || a.Allows([]string{"money"}, false) || !a.Allows([]string{"charges"}, false) || a.Allows([]string{"charges"}, true) {
		t.Fatalf("access: %+v", a)
	}
	if r, _ := o.RoleOf(ctx, T, "neha.brok"); r != "Brokerage desk" {
		t.Fatal(r)
	}
	// The Users screen keeps the roles; changing them there is refused.
	u, _ := o.user(ctx, T, "neha.brok")
	u.Name = "Neha S"
	if err := o.PutUser(ctx, T, "admin", u); err != nil {
		t.Fatal(err)
	}
	u.Role = RoleChecker
	if err := o.PutUser(ctx, T, "admin", u); !errors.Is(err, ErrInvalid) {
		t.Fatalf("role change on the Users screen: %v", err)
	}
	if err := o.PutUser(ctx, T, "admin", User{ID: "new.one", Name: "New", Role: "Brokerage desk", Active: true}); err == nil || !strings.Contains(err.Error(), "second Admin") {
		t.Fatalf("custom role on the Users screen: %v", err)
	}
}

// An edited built-in role keeps its powers where it was not changed, and
// loses them where a tick was taken away.
func TestEditedBuiltinRole(t *testing.T) {
	o, ctx := rolesSetup(t)
	mk, _ := BuiltinRole(RoleMaker)
	p := mk.Screens["money"]
	p.Do = false
	mk.Screens["money"] = p
	k := mk.Screens["exdisc"]
	k.Do = true
	mk.Screens["exdisc"] = k
	c, err := o.ProposeRole(ctx, T, "admin", mk)
	approveRole(t, o, ctx, c, err)
	if err := o.require(on(ctx, "money"), T, "priya.nair", "Recording a receipt", RoleMaker, RoleChecker, RoleBranch); !errors.Is(err, ErrForbidden) {
		t.Fatalf("receipt after taking Do away: %v", err)
	}
	if err := o.require(on(ctx, "clients"), T, "priya.nair", "Creating a client", RoleMaker, RoleChecker); err != nil {
		t.Fatalf("an unchanged screen: %v", err)
	}
	if err := o.require(on(ctx, "clients"), T, "priya.nair", "Activating a client", RoleChecker); !errors.Is(err, ErrForbidden) {
		t.Fatalf("still not the checker's power: %v", err)
	}
	// A tick the Ops maker never had gives every action of its kind there.
	if err := o.require(on(ctx, "exdisc"), T, "priya.nair", "Editing exchange disclosures", RoleCompliance); err != nil {
		t.Fatalf("Do added on Exchange disclosures: %v", err)
	}
	if err := o.require(on(ctx, "kyc"), T, "priya.nair", "Setting client risk", RoleCompliance); !errors.Is(err, ErrForbidden) {
		t.Fatalf("KYC kept the Ops maker's limits: %v", err)
	}
	// Reset to default: back to checks by name.
	c, err = o.ProposeRoleReset(ctx, T, "admin2", RoleMaker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideRoleChange(ctx, T, "admin", c.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := o.require(on(ctx, "money"), T, "priya.nair", "Recording a receipt", RoleMaker, RoleChecker, RoleBranch); err != nil {
		t.Fatalf("after reset: %v", err)
	}
	if a, _ := o.Access(ctx, T, "priya.nair"); len(a.Defs) != 0 || a.Gated() {
		t.Fatalf("reset is legacy: %+v", a)
	}
}

func TestRoleChangesNeedTwoAdmins(t *testing.T) {
	o, ctx := rolesSetup(t)
	c, err := o.ProposeRole(ctx, T, "admin", RoleDef{Name: "CRM desk", Screens: map[string]Power{"crm": {Do: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideRoleChange(ctx, T, "admin", c.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("own approval: %v", err)
	}
	if _, err := o.DecideRoleChange(ctx, T, "farhan.khan", c.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a checker approves: %v", err)
	}
	if _, err := o.ProposeRole(ctx, T, "admin2", RoleDef{Name: "CRM desk", Screens: map[string]Power{"crm": {View: true}}}); !errors.Is(err, ErrState) {
		t.Fatalf("second proposal while one waits: %v", err)
	}
	if _, ok := o.roleDef(ctx, T, "CRM desk"); ok {
		t.Fatal("in force before approval")
	}
	if _, err := o.ProposeUserRoles(ctx, T, "admin", "neha.brok", []string{"CRM desk"}, "", ""); err == nil {
		t.Fatal("a role not yet approved given")
	}
	if _, err := o.DecideRoleChange(ctx, T, "admin2", c.ID, false, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reject without a reason: %v", err)
	}
	if x, err := o.DecideRoleChange(ctx, T, "admin2", c.ID, true, ""); err != nil || x.Status != RCApproved || x.DecidedBy != "admin2" {
		t.Fatalf("approve: %+v %v", x, err)
	}
	if d, ok := o.roleDef(ctx, T, "CRM desk"); !ok || d.ApprovedBy != "admin2" || d.ChangedBy != "admin" {
		t.Fatalf("in force: %+v", d)
	}
	// Names are unique and not the built-in ones; Admin cannot be edited.
	for _, d := range []RoleDef{{Name: "crm DESK"}, {Name: "ops MAKER"}, {Name: RoleAdmin}, {Name: "x"}, {Name: "Desk", Screens: map[string]Power{"nowhere": {View: true}}}} {
		if _, err := o.ProposeRole(ctx, T, "admin", d); err == nil {
			t.Errorf("%q accepted", d.Name)
		}
	}
	// Branch staff stay limited to their branch.
	br, _ := BuiltinRole(RoleBranch)
	br.Limits.Scope = ScopeFirm
	if _, err := o.ProposeRole(ctx, T, "admin", br); err == nil {
		t.Fatal("branch staff widened")
	}
	// A role limited to a branch reaches only client-level screens.
	if _, err := o.ProposeRole(ctx, T, "admin", RoleDef{Name: "Branch payouts", Screens: map[string]Power{"settlement": {View: true}}, Limits: RoleLimits{Scope: ScopeBranch}}); err == nil {
		t.Fatal("branch role on a firm-wide screen")
	}
	// Retiring needs nobody holding it; a withdrawn proposal does nothing.
	a, err := o.ProposeUserRoles(ctx, T, "admin", "neha.brok", []string{"CRM desk", RoleMaker}, "", "")
	approveRole(t, o, ctx, a, err)
	if _, err := o.ProposeRoleRetire(ctx, T, "admin", "CRM desk"); !errors.Is(err, ErrState) {
		t.Fatalf("retire while held: %v", err)
	}
	w, err := o.ProposeUserRoles(ctx, T, "admin", "neha.brok", []string{RoleMaker}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.WithdrawRoleChange(ctx, T, "admin2", w.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("someone else withdraws: %v", err)
	}
	if _, err := o.WithdrawRoleChange(ctx, T, "admin", w.ID); err != nil {
		t.Fatal(err)
	}
	if u, _ := o.user(ctx, T, "neha.brok"); u.Role != "CRM desk" || len(u.MoreRoles) != 1 {
		t.Fatalf("withdrawn change applied: %+v", u)
	}
	// Audited, and a security event.
	ev, _ := o.App.Audit.List(ctx, T, 200)
	seen := map[string]bool{}
	for _, e := range ev {
		seen[e.Action] = true
	}
	for _, a := range []string{"role.proposed", "role.change_approved", "role.changed", "user.role_changed", "role.withdrawn"} {
		if !seen[a] {
			t.Errorf("no %s in the audit trail", a)
		}
	}
}

// More than one role: the union. Neha holds the Ops maker and a CRM role.
func TestRoleUnion(t *testing.T) {
	o, ctx := rolesSetup(t)
	c, err := o.ProposeRole(ctx, T, "admin", RoleDef{Name: "Plans", Screens: map[string]Power{"brokerage": {Do: true}}})
	approveRole(t, o, ctx, c, err)
	c, err = o.ProposeUserRoles(ctx, T, "admin", "neha.brok", []string{RoleMaker, "Plans"}, "", "")
	approveRole(t, o, ctx, c, err)
	if err := o.require(on(ctx, "money"), T, "neha.brok", "Recording a receipt", RoleMaker, RoleChecker, RoleBranch); err != nil {
		t.Fatalf("as Ops maker: %v", err)
	}
	if err := o.require(on(ctx, "brokerage"), T, "neha.brok", "Changing brokerage plans", RoleChecker); err != nil {
		t.Fatalf("as Plans: %v", err)
	}
	if a, _ := o.Access(ctx, T, "neha.brok"); a.Gated() {
		t.Fatal("an unedited Ops maker among the roles sees every screen")
	}
	if r, _ := o.RoleOf(ctx, T, "neha.brok"); r != RoleMaker {
		t.Fatal(r)
	}
	// Roles of different reach cannot be combined.
	if _, err := o.ProposeUserRoles(ctx, T, "admin", "ravi.pay", []string{RoleMaker, RoleBranch}, "Bengaluru", ""); err == nil {
		t.Fatal("firm-wide and branch roles combined")
	}
}

// A role limited to a branch: Branch staff to the rest of the system.
func TestCustomBranchRole(t *testing.T) {
	o, ctx := rolesSetup(t)
	c, err := o.ProposeRole(ctx, T, "admin", RoleDef{Name: "Branch CRM", Template: RoleBranch, Screens: map[string]Power{"crm": {Do: true}, "clients": {View: true}}, Limits: RoleLimits{Scope: ScopeBranch}})
	approveRole(t, o, ctx, c, err)
	if _, err := o.ProposeUserRoles(ctx, T, "admin", "ravi.pay", []string{"Branch CRM"}, "", ""); err == nil {
		t.Fatal("no branch given")
	}
	c, err = o.ProposeUserRoles(ctx, T, "admin", "ravi.pay", []string{"Branch CRM"}, "Bengaluru", "")
	approveRole(t, o, ctx, c, err)
	if r, _ := o.RoleOf(ctx, T, "ravi.pay"); r != RoleBranch {
		t.Fatalf("role of: %s", r)
	}
	u, _ := o.user(ctx, T, "ravi.pay")
	if u.Scope() != RoleBranch || u.Branch != "Bengaluru" {
		t.Fatalf("user: %+v", u)
	}
	f, g := o.ClientScope(ctx, T, "ravi.pay"), o.ClientScope(ctx, T, "meera.iyer")
	if f == nil || g == nil {
		t.Fatal("not filtered")
	}
	cs, _ := o.ClientMap(ctx, T)
	n := 0
	for ucc := range cs {
		if f(ucc) != g(ucc) {
			t.Fatalf("%s: custom branch role sees differently from branch staff", ucc)
		}
		if f(ucc) {
			n++
		}
	}
	if n == 0 || n == len(cs) {
		t.Fatalf("%d of %d clients", n, len(cs))
	}
}

func TestPayoutApprovalLimit(t *testing.T) {
	o, ctx := rolesSetup(t)
	c, err := o.ProposeRole(ctx, T, "admin", RoleDef{Name: "Payout approver", Screens: map[string]Power{"money": {Approve: true}}, Limits: RoleLimits{PayoutLimit: "3,00,000"}})
	approveRole(t, o, ctx, c, err)
	if d, _ := o.roleDef(ctx, T, "Payout approver"); d.Limits.PayoutLimit != "300000.00" {
		t.Fatalf("limit stored as %q", d.Limits.PayoutLimit)
	}
	c, err = o.ProposeUserRoles(ctx, T, "admin", "ravi.pay", []string{"Payout approver"}, "", "")
	approveRole(t, o, ctx, c, err)
	m, _, _ := o.Market(ctx, T, D)
	o.App.RunBilling(ctx, T, "priya.nair", D, m.SettlementPrices())
	var ucc string
	cs, _ := o.ClientMap(ctx, T)
	for _, u := range sortedKeys(cs) {
		if w, _ := o.Withdrawable(ctx, T, D, u); w.Available > 50000000 && cs[u].Risk != "High" && !cs[u].Blocked {
			ucc = u
			break
		}
	}
	if ucc == "" {
		t.Fatal("no client with free funds")
	}
	// Above the Autopilot's automatic limit (₹2,00,000), so people decide.
	small, err := o.RequestPayout(ctx, T, "priya.nair", D, ucc, money.Amount(25000000), "Head office")
	if err != nil || small.Status != "Requested" {
		t.Fatalf("small: %+v %v", small, err)
	}
	mctx := on(ctx, "money")
	// Approve without Do: cannot enter one.
	if _, err := o.RequestPayout(mctx, T, "ravi.pay", D, ucc, money.Amount(100000), "Head office"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("approver enters a payout: %v", err)
	}
	if x, err := o.DecidePayout(mctx, T, "ravi.pay", D, small.ID, true, ""); err != nil || x.Status != "Approved" {
		t.Fatalf("within the limit: %+v %v", x, err)
	}
	big, err := o.RequestPayout(ctx, T, "priya.nair", D, ucc, money.Amount(30500000), "Head office")
	if err != nil || big.Status != "Requested" {
		t.Fatalf("big: %+v %v", big, err)
	}
	if _, err := o.DecidePayout(mctx, T, "ravi.pay", D, big.ID, true, ""); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("above the limit: %v", err)
	}
	// The built-in checker has no limit, as before.
	if _, err := o.DecidePayout(mctx, T, "farhan.khan", D, big.ID, true, ""); err != nil {
		t.Fatal(err)
	}
}

func TestPreviewIsReadOnly(t *testing.T) {
	o, ctx := rolesSetup(t)
	d, err := o.StartRolePreview(ctx, T, "admin", RoleDef{Name: "Draft", Screens: map[string]Power{"money": {Do: true, Approve: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.StartRolePreview(ctx, T, "priya.nair", RoleDef{Name: RoleChecker}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker previews: %v", err)
	}
	p, ok := o.RolePreview(ctx, T, "admin")
	if !ok || p.Name != "Draft" {
		t.Fatal("no preview")
	}
	pctx := WithPreview(on(ctx, "money"), "admin", d)
	if err := o.require(pctx, T, "admin", "Recording a receipt", RoleMaker); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "preview") {
		t.Fatalf("a write in preview: %v", err)
	}
	if err := o.require(pctx, T, "admin", "Viewing the client money reconciliation", RoleMaker); err != nil {
		t.Fatalf("a view the role has: %v", err)
	}
	if err := o.require(WithPreview(on(ctx, "clientmoney"), "admin", d), T, "admin", "Viewing the client money reconciliation", RoleMaker); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a view the role lacks: %v", err)
	}
	if r, _ := o.RoleOf(pctx, T, "admin"); r != "Draft" {
		t.Fatalf("role of in preview: %s", r)
	}
	if _, err := o.ProposeRole(pctx, T, "admin", RoleDef{Name: "Other"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("propose in preview: %v", err)
	}
	if r, _ := o.RoleOf(ctx, T, "admin"); r != RoleAdmin {
		t.Fatal("the preview leaked out of its requests")
	}
	if err := o.EndRolePreview(ctx, T, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, ok := o.RolePreview(ctx, T, "admin"); ok {
		t.Fatal("preview still open")
	}
}

func TestRoleSummary(t *testing.T) {
	d := RoleDef{Name: "Desk", Template: RoleMaker, Screens: map[string]Power{"money": {Do: true, Approve: true}, "connections": {Do: true}, "crm": {View: true}},
		Limits: RoleLimits{Scope: ScopeFirm, PayoutLimit: "50000.00"}}
	s := strings.Join(RoleSummary(d), "\n")
	for _, want := range []string{"Receipts & payouts (as Ops maker does)", "payouts up to ₹50,000.00", "only looks at Leads & CRM", "Cannot see anything under", "Does not see brokerage totals", "masked"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary lacks %q:\n%s", want, s)
		}
	}
	w := strings.Join(RoleWarnings(d), "\n")
	for _, want := range []string{"Maker-checker still applies", "Admin-level screens: Connections"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %q:\n%s", want, w)
		}
	}
}
