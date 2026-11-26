package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/screens"
)

// ---- editable roles ----
//
// A role says, for every staff screen (internal/screens), whether its
// holders may look at it (View), create and change things on it (Do, the
// maker's power) and approve what others entered (Approve, the checker's
// power), plus a few limits: how far their client list reaches, whether
// they see brokerage totals, may reveal full identifiers, may download,
// and up to what amount they may approve payouts.
//
// The built-in roles (Ops maker, Ops checker, Risk manager, Compliance
// officer, Branch staff, Authorised person, Admin) are templates. While a
// firm leaves a built-in role as it is, its holders are checked exactly as
// before: by the role's name in each permission check (require). Only an
// edited built-in role or a role the firm made is checked by its screens.
//
// How a screen permission decides a permission check (permits):
//   - The check's kind comes from what it guards: looking (Viewing...),
//     approving (Approving..., Verifying..., when the Ops maker is not one
//     of the roles named) or doing (everything else).
//   - The screen is the one the request came from (the API maps every
//     route to its screens, WithAreas).
//   - A tick the role's starting template also has keeps the template's
//     limits: an edited Ops maker still does on that screen only what an
//     Ops maker does there. A tick the template does not have (or any tick
//     of a role started blank) gives every action of that kind on the
//     screen, except those only an Admin may take, which stay with Admin
//     on every screen but the Admin section's.
//
// Changes to roles, and giving people roles they cannot be given on the
// Users screen (a role the firm made, an edited role, or more than one
// role), take effect only when a second Admin approves (RoleChange).

const (
	cRole       = "role"        // role definitions the firm changed or made, by name
	cRoleChange = "role_change" // proposed changes, by ID
)

// Power is what a role may do on one screen.
type Power struct {
	View    bool `json:"view,omitempty"`
	Do      bool `json:"do,omitempty"`
	Approve bool `json:"approve,omitempty"`
}

func (p Power) has(kind string) bool {
	switch kind {
	case kindView:
		return p.View
	case kindApprove:
		return p.Approve
	}
	return p.Do
}

func (p Power) or(q Power) Power {
	return Power{p.View || q.View, p.Do || q.Do, p.Approve || q.Approve}
}

// Scope is how far a role's client list reaches.
const (
	ScopeFirm   = "firm"   // every client
	ScopeBranch = "branch" // the person's branch (like Branch staff)
	ScopeAP     = "ap"     // the person's authorised person and downline (like an AP login)
)

// RoleLimits are a role's limits beyond its screens.
type RoleLimits struct {
	Scope     string `json:"scope"`
	Brokerage bool   `json:"brokerage"`  // may see brokerage totals
	RevealIDs bool   `json:"reveal_ids"` // may reveal full PAN, Aadhaar and bank numbers
	Export    bool   `json:"export"`     // may download and export
	// PayoutLimit is the largest payout (₹) the role may approve; blank
	// for no limit.
	PayoutLimit string `json:"payout_limit,omitempty"`
}

// RoleDef is one role.
type RoleDef struct {
	// Name is the role's name and its key (users hold roles by name, as
	// they always have). A role the firm made cannot be renamed; copy it.
	Name  string `json:"name"`
	About string `json:"about,omitempty"`
	// Template is the built-in role it started from ("" when blank); for a
	// built-in role, itself.
	Template string           `json:"template,omitempty"`
	BuiltIn  bool             `json:"built_in,omitempty"`
	Edited   bool             `json:"edited,omitempty"`  // a built-in role changed from its default
	Retired  bool             `json:"retired,omitempty"` // a firm's role no longer offered
	Screens  map[string]Power `json:"screens"`
	Limits   RoleLimits       `json:"limits"`
	// Who proposed and approved the version in force.
	ChangedBy  string `json:"changed_by,omitempty"`
	ApprovedBy string `json:"approved_by,omitempty"`
	ApprovedAt string `json:"approved_at,omitempty"`
}

// The kinds of permission check.
const (
	kindView    = "view"
	kindDo      = "do"
	kindApprove = "approve"
)

// whatRevealIDs is the check for seeing a client's full identifiers; a
// role's "may reveal full numbers" limit decides it.
const whatRevealIDs = "See full client identifiers"

// normWhat is a check's text with a capital letter, as the permission list
// (config/help/permissions.md) writes it; a few checks start lower-case.
func normWhat(what string) string {
	if what == "" {
		return what
	}
	return strings.ToUpper(what[:1]) + what[1:]
}

// actionKind sorts a permission check by what it guards.
func actionKind(what string, roles []string) string {
	what = normWhat(what)
	for _, p := range []string{"Viewing", "View ", "Seeing", "Looking at", "Searching", "Downloading"} {
		if strings.HasPrefix(what, p) {
			return kindView
		}
	}
	if contains(roles, RoleMaker) {
		return kindDo
	}
	for _, p := range []string{"Approving", "Activating", "Verifying", "Signing off", "Rejecting", "Confirming", "Releasing payouts"} {
		if strings.HasPrefix(what, p) {
			return kindApprove
		}
	}
	if strings.HasPrefix(what, "Deciding") && !strings.HasPrefix(what, "Deciding whether") {
		return kindApprove
	}
	return kindDo
}

// ---- the built-in roles as templates ----

// builtinWork is, for each built-in role, the screens it creates and
// changes things on and the screens it approves on, as the permission
// checks in this package allow today (config/help/permissions.md). Every
// staff role sees every screen, as the console has always shown them;
// branch staff and authorised persons see the client-level screens.
// Used only for roles a firm edits or copies: an unedited built-in role
// is checked by name.
var builtinWork = map[string]struct{ do, approve string }{
	RoleMaker: {do: "me dashboard boards chat autopilot steps rpa periodic circulars xfiles ccdata files recon billing settlement notes clients apps changes uccapi mnrl corp " +
		"statements dispatch messages yearend archive rms live payouthold collateral cdslpledge cdslepi allocation debits legacy mtf ledger money bank clientmoney " +
		"books firmsec prepaid reports kyc kycgate helpdesk inactive deceased selfinspect ipo rfq cbrics mforders mfsips mfmandates mfunits stocksip dp cdslreq edis cdsltxn " +
		"insights crm partners dealers"},
	RoleChecker: {do: "me dashboard boards chat autopilot steps jobs rpa periodic circulars xfiles ccdata files migrate recon billing settlement notes clients apps changes uccapi mnrl corp " +
		"statements dispatch messages yearend archive rms live margin payouthold collateral cdslpledge cdslepi allocation deposits debits legacy mtf ledger money bank ras clientmoney " +
		"books firmsec brokerage charges prepaid reports kyc kycgate helpdesk nri surv inactive deceased selfinspect ipo rfq cbrics mforders mfsips mfmandates mfunits mfcomm stocksip dp cdslreq edis cdsltxn " +
		"insights crm partners dealers backups",
		approve: "migrate billing settlement clients apps changes corp rms margin payouthold collateral cdslpledge cdslepi allocation deposits legacy mtf money bank ras clientmoney " +
			"books firmsec circulars inactive kyc ipo rfq cbrics dp cdslreq cdsltxn"},
	RoleRisk: {do: "me dashboard boards chat autopilot steps rpa periodic xfiles ccdata recon settlement clients rms live margin payouthold collateral cdslpledge cdslepi allocation " +
		"debits legacy mtf ledger money reports stocksip ipo rfq cbrics dp edis cdsltxn insights selfinspect firmsec kycgate glitches",
		approve: "rms margin payouthold collateral cdslpledge cdslepi allocation mtf settlement money"},
	RoleCompliance: {do: "dashboard boards chat autopilot steps jobs rpa periodic circulars xfiles files clients apps changes uccapi mnrl statements dispatch messages yearend archive " +
		"margin legacy clientmoney firmsec reports exdisc aictl kyc kycgate helpdesk nri surv inactive deceased selfinspect inspack audit incidents glitches logexport " +
		"ipo mfsettings mfcomm mforders stocksip dp cdslreq edis insights partners dealers backups settings supportaccess dataexit me people hrcomp hrpolicy firmmodules",
		approve: "apps changes margin legacy inactive kyc circulars cdslpledge reports dataexit deceased practices hrcomp"},
	// HRMS and Payroll (hrms.go, payroll.go, payrun.go).
	RoleHR:      {do: "me people hrlife hrcomp attendance leave hrpolicy payroll", approve: "hrlife attendance payroll"},
	RolePayroll: {do: "me payroll hrlife", approve: "payroll"},
	RoleBranch:  {do: "boards chat rpa clients money rms prepaid changes helpdesk crm ipo insights"},
	RoleAP:      {do: "boards chat clients money rms changes helpdesk crm ipo insights"},
}

// scopedScreens are the client-level screens branch staff and authorised
// persons reach (each filters to their own clients).
var scopedScreens = strings.Fields("chat boards rpa yearend insights clients statements notes money debits rms partners prepaid changes helpdesk crm ipo me people attendance leave")

// viewOnlyFor are the screens that, besides the Admin, only the roles named
// open (Firm practices: the Admin answers and Compliance approves; Support
// access: Compliance approves a session; Staff app and Client app: the
// Admin changes them, Compliance may look), so the other templates do not
// have them.
var viewOnlyFor = map[string][]string{
	"practices": {RoleCompliance}, "supportaccess": {RoleCompliance}, "staffapp": {RoleCompliance}, "clientapp": {RoleCompliance},
	// Settings → Modules: the Admin only. The People screens the console
	// shows by role (hrViewOK, web/hrms.js); My HR, Staff, Attendance and
	// Leave are every staff role's (each sees their own and their team's).
	"modules": {},
	"hrcomp":  {RoleHR, RoleCompliance}, "hrpolicy": {RoleHR, RoleCompliance},
	"hrlife":  {RoleHR, RoleCompliance, RolePayroll},
	"payroll": {RolePayroll, RoleHR},
}

// editableBuiltin reports a built-in role a firm may edit: not Admin
// (which always has everything) and not the client portal's.
func editableBuiltin(name string) bool { return contains(Roles, name) && name != RoleAdmin }

// BuiltinRole is a built-in role as shipped.
func BuiltinRole(name string) (RoleDef, bool) {
	if !contains(Roles, name) {
		return RoleDef{}, false
	}
	d := RoleDef{Name: name, Template: name, BuiltIn: true, Screens: map[string]Power{},
		Limits: RoleLimits{Scope: ScopeFirm, Brokerage: true, Export: true}}
	switch name {
	case RoleAdmin:
		d.About = "Everything, including users, roles, connections, security and the firm's data. Cannot be edited."
	case RoleMaker:
		d.About = "Enters the day's work: receipts, payout requests, client records, vouchers, DP requests, bids and orders."
	case RoleChecker:
		d.About = "Approves what the makers enter, and runs the steps that need a second person: billing corrections, payouts, the day's close."
	case RoleRisk:
		d.About = "Limits, margin, pledges, collateral, pay-out holds, square-off and debit blocking."
	case RoleCompliance:
		d.About = "KYC and PMLA, complaints, surveillance, regulatory reports and disclosures, inspection, security incidents."
	case RoleBranch:
		d.About = "Branch staff: the branch's own clients on the client-level screens."
	case RoleAP:
		d.About = "An authorised person's login: their own and their downline's clients on the client-level screens."
	case RoleHR:
		d.About = "The employee master, joiners and leavers, certificates, attendance and leave (with the HRMS module on)."
	case RolePayroll:
		d.About = "Runs payroll: salary structures, inputs, the month's run, payslips, loans and tax proofs (with the Payroll module on)."
	}
	view := screens.All()
	if name == RoleBranch || name == RoleAP {
		view = nil
		for _, k := range scopedScreens {
			if name == RoleAP && k == "rpa" {
				continue // automations are for the firm's own staff
			}
			if x, ok := screens.Get(k); ok {
				view = append(view, x)
			}
		}
	}
	for _, x := range view {
		if rs, ok := viewOnlyFor[x.Key]; ok && !contains(rs, name) && name != RoleAdmin {
			continue // the console never showed these to the other roles
		}
		d.Screens[x.Key] = Power{View: true}
	}
	if name == RoleAdmin {
		for _, x := range screens.All() {
			d.Screens[x.Key] = Power{true, true, true}
		}
		d.Limits.RevealIDs = true
		return d, true
	}
	w := builtinWork[name]
	for _, k := range strings.Fields(w.do) {
		p := d.Screens[k]
		p.View, p.Do = true, true
		d.Screens[k] = p
	}
	for _, k := range strings.Fields(w.approve) {
		p := d.Screens[k]
		p.View, p.Approve = true, true
		d.Screens[k] = p
	}
	switch name {
	case RoleChecker, RoleCompliance:
		d.Limits.RevealIDs = true
	case RoleBranch:
		d.Limits.Scope = ScopeBranch
	case RoleAP:
		d.Limits.Scope = ScopeAP
	}
	return d, true
}

// ---- reading roles ----

// roleDef returns a role by name: a built-in role as the firm has it
// (edited or as shipped), or one the firm made. Retired roles are not
// found.
func (o *Ops) roleDef(ctx context.Context, tenant, name string) (RoleDef, bool) {
	if b, ok := BuiltinRole(name); ok {
		if name == RoleAdmin {
			return b, true
		}
		if d, _, err := docs.Get[RoleDef](ctx, o.Docs, tenant, cRole, name); err == nil && d.Edited {
			d.BuiltIn, d.Template = true, name
			return d, true
		}
		return b, true
	}
	d, _, err := docs.Get[RoleDef](ctx, o.Docs, tenant, cRole, name)
	if err != nil || d.Retired {
		return RoleDef{}, false
	}
	return d, true
}

// RoleView is a role as the Roles screen shows it.
type RoleView struct {
	RoleDef
	Holders []string `json:"holders"` // user IDs holding it
	Summary []string `json:"summary"`
	Default *RoleDef `json:"default,omitempty"` // a built-in role's shipped version, when edited
}

// RoleDefs is every role the firm can give: the built-in roles, then the
// firm's own, by name.
func (o *Ops) RoleDefs(ctx context.Context, tenant string) ([]RoleView, error) {
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return nil, err
	}
	holders := map[string][]string{}
	for _, u := range us {
		for _, r := range u.roles() {
			holders[r] = append(holders[r], u.ID)
		}
	}
	var out []RoleView
	for _, n := range Roles {
		d, _ := o.roleDef(ctx, tenant, n)
		v := RoleView{RoleDef: d, Holders: holders[n], Summary: RoleSummary(d)}
		if d.Edited {
			b, _ := BuiltinRole(n)
			v.Default = &b
		}
		out = append(out, v)
	}
	own, err := docs.List[RoleDef](ctx, o.Docs, tenant, cRole, "")
	if err != nil {
		return nil, err
	}
	sort.Slice(own, func(i, j int) bool { return own[i].Name < own[j].Name })
	for _, d := range own {
		if d.Retired || contains(Roles, d.Name) {
			continue
		}
		out = append(out, RoleView{RoleDef: d, Holders: holders[d.Name], Summary: RoleSummary(d)})
	}
	return out, nil
}

// ---- a person's effective access ----

// roles is every role the user holds, the first being their main role.
func (u User) roles() []string {
	out := []string{u.Role}
	for _, r := range u.MoreRoles {
		if r != "" && !contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// Access is what a person may do: the built-in roles they hold unedited
// (checked by name, as always) and the role definitions of the others.
type Access struct {
	Actor string
	Name  string // the main role, for messages
	Admin bool
	// Legacy are the unedited built-in roles held (and Client).
	Legacy []string
	// Defs are the edited built-in roles and the firm's own roles held.
	Defs []RoleDef
	// Preview is set when an Admin is previewing a role (read-only).
	Preview bool
}

// Gated reports access decided by role definitions: someone holding a
// role the firm made or edited and no unedited firm-wide built-in role
// (which sees every screen anyway), or an Admin's preview.
func (a Access) Gated() bool {
	if a.Admin || len(a.Defs) == 0 {
		return false
	}
	for _, r := range a.Legacy {
		if r != RoleBranch && r != RoleAP && r != RoleClient {
			return false
		}
	}
	return true
}

// Screens is the union of every held role's screens (unedited built-in
// roles count as their template).
func (a Access) Screens() map[string]Power {
	out := map[string]Power{}
	add := func(d RoleDef) {
		for k, p := range d.Screens {
			out[k] = out[k].or(p)
		}
	}
	for _, r := range a.Legacy {
		if b, ok := BuiltinRole(r); ok {
			add(b)
		}
	}
	for _, d := range a.Defs {
		add(d)
	}
	return out
}

// Allows reports whether a request reading (write=false) or changing one
// of the screens is open to a gated person. The permission checks behind
// the request still decide each action.
func (a Access) Allows(areas []string, write bool) bool {
	ss := a.Screens()
	for _, k := range areas {
		p := ss[k]
		if (!write && p.View) || (write && (p.Do || p.Approve)) {
			return true
		}
	}
	return false
}

// Limits is the union of the held roles' limits: the widest scope, any
// role's permission, the highest payout limit (blank beats any amount).
func (a Access) Limits() RoleLimits {
	l := RoleLimits{Scope: ScopeAP}
	if a.Admin {
		return RoleLimits{Scope: ScopeFirm, Brokerage: true, RevealIDs: true, Export: true}
	}
	first := true
	widen := func(s string) {
		rank := map[string]int{ScopeAP: 0, ScopeBranch: 1, ScopeFirm: 2}
		if rank[s] > rank[l.Scope] {
			l.Scope = s
		}
	}
	add := func(x RoleLimits) {
		widen(x.Scope)
		l.Brokerage = l.Brokerage || x.Brokerage
		l.RevealIDs = l.RevealIDs || x.RevealIDs
		l.Export = l.Export || x.Export
		switch {
		case first:
			l.PayoutLimit = x.PayoutLimit
		case l.PayoutLimit == "" || x.PayoutLimit == "":
			l.PayoutLimit = ""
		default:
			if mustAmt(x.PayoutLimit) > mustAmt(l.PayoutLimit) {
				l.PayoutLimit = x.PayoutLimit
			}
		}
		first = false
	}
	for _, r := range a.Legacy {
		if b, ok := BuiltinRole(r); ok {
			add(b.Limits)
		}
	}
	for _, d := range a.Defs {
		add(d.Limits)
	}
	return l
}

func mustAmt(s string) money.Amount { a, _ := money.ParseAmount(s); return a }

// Beyond is the screens where a held role creates, changes or approves
// beyond what its template does there (or was started blank): there it may
// take every action of that kind, not only its template's.
func (a Access) Beyond() map[string]bool {
	out := map[string]bool{}
	for _, d := range a.Defs {
		base, _ := BuiltinRole(d.Template)
		for k, p := range d.Screens {
			b := base.Screens[k]
			if d.Template != RoleAdmin && (p.Do && !b.Do || p.Approve && !b.Approve) {
				out[k] = true
			}
		}
	}
	return out
}

// Bases are the templates of the held roles (for the console's buttons).
func (a Access) Bases() []string {
	var out []string
	for _, d := range a.Defs {
		if d.Template != "" && !contains(out, d.Template) {
			out = append(out, d.Template)
		}
	}
	return out
}

type ctxRoleKey int

const (
	keyAreas ctxRoleKey = iota
	keyPreview
)

// WithAreas records the screens a request belongs to, for the permission
// checks of custom roles (the API sets it from the route).
func WithAreas(ctx context.Context, areas []string) context.Context {
	return context.WithValue(ctx, keyAreas, areas)
}

func areasFrom(ctx context.Context) []string {
	a, _ := ctx.Value(keyAreas).([]string)
	return a
}

type preview struct {
	actor string
	role  RoleDef
}

// WithPreview makes the actor's requests run as the role, read-only (the
// API sets it for an Admin's "Preview as this role").
func WithPreview(ctx context.Context, actor string, role RoleDef) context.Context {
	return context.WithValue(ctx, keyPreview, preview{actor, role})
}

// Previewing returns the role an Admin is previewing in this request.
func Previewing(ctx context.Context) (RoleDef, bool) {
	p, ok := ctx.Value(keyPreview).(preview)
	return p.role, ok
}

// Access works out what the actor may do. Its errors are RoleOf's: an
// unknown or deactivated user.
func (o *Ops) Access(ctx context.Context, tenant, actor string) (Access, error) {
	if p, ok := ctx.Value(keyPreview).(preview); ok && p.actor == actor {
		a := Access{Actor: actor, Name: p.role.Name, Preview: true, Admin: p.role.BuiltIn && p.role.Name == RoleAdmin}
		if p.role.BuiltIn && !p.role.Edited {
			a.Legacy = []string{p.role.Name}
		} else {
			a.Defs = []RoleDef{p.role}
		}
		return a, nil
	}
	if actor == SystemActor {
		return Access{Actor: actor, Name: RoleAdmin, Admin: true}, nil
	}
	if IsSupportActor(actor) {
		r, err := o.supportRole(ctx, tenant, actor)
		if err != nil {
			return Access{}, err
		}
		return Access{Actor: actor, Name: r, Admin: r == RoleAdmin, Legacy: []string{r}}, nil
	}
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return Access{}, err
	}
	if len(us) == 0 {
		return Access{Actor: actor, Name: RoleAdmin, Admin: true}, nil
	}
	for _, u := range us {
		if u.ID != actor {
			continue
		}
		if !u.Active {
			return Access{}, fmt.Errorf("%w: user %s is deactivated", ErrForbidden, actor)
		}
		a := Access{Actor: actor, Name: u.Role}
		for _, r := range u.roles() {
			if r == RoleAdmin {
				a.Admin = true
			}
			if r == RoleClient {
				a.Legacy = append(a.Legacy, r)
				continue
			}
			d, ok := o.roleDef(ctx, tenant, r)
			switch {
			case !ok:
				// A role that was retired (or never existed) gives nothing,
				// and the person sees no screens for it.
				if r != "" {
					a.Defs = append(a.Defs, RoleDef{Name: r, Screens: map[string]Power{}})
				}
			case d.BuiltIn && !d.Edited:
				a.Legacy = append(a.Legacy, r)
			default:
				a.Defs = append(a.Defs, d)
			}
		}
		return a, nil
	}
	return Access{}, fmt.Errorf("%w: %q is not a user of this firm", ErrForbidden, actor)
}

// adminAlways are the checks no role but Admin passes, whatever screens it
// has: who may sign in and as what, and the firm's data and keys as a
// whole. Without this a role with Do on the Users screen could make
// itself Admin.
var adminAlways = map[string]bool{
	"Adding users": true, "Managing users": true, "Resetting passwords": true, "Resetting two-factor sign-in": true,
	"Deleting the firm's data": true, "Asking to close the firm's account": true, "Starting a full data export": true, "Downloading a full data export": true,
	"Moving the firm onto its own encryption key": true, "Rotating the firm's encryption key": true, "Changing how long the firm's keys stay in memory": true,
	"Changing Ledgerline support access settings": true,
	"Switching modules on or off":                 true, // priced modules (Settings → Modules)
}

// adminScreens are the screens on which a role may only look: changing
// users and roles always needs an Admin, with a second Admin's approval.
var adminScreens = map[string]bool{"users": true, "roles": true}

// permits decides a permission check for the roles the firm edited or made
// (see the package comment above for the rules).
func (a Access) permits(what string, roles []string, areas []string) bool {
	if what = normWhat(what); adminAlways[what] {
		return false
	}
	kind := actionKind(what, roles)
	adminOnly := len(roles) == 0 || (len(roles) == 1 && roles[0] == RoleAdmin)
	for _, d := range a.Defs {
		legacy := d.Template == RoleAdmin || contains(roles, d.Template)
		base, _ := BuiltinRole(d.Template)
		if what == whatRevealIDs {
			if d.Limits.RevealIDs && (legacy || !base.Limits.RevealIDs) {
				return true
			}
			continue
		}
		if len(areas) == 0 {
			// Not from a screen (an automatic run, an internal step): the
			// role does what its template does.
			if legacy {
				return true
			}
			continue
		}
		for _, k := range areas {
			if !d.Screens[k].has(kind) || adminScreens[k] && kind != kindView {
				continue
			}
			if legacy {
				return true
			}
			if adminOnly && !screens.IsAdmin(k) {
				continue
			}
			if !base.Screens[k].has(kind) {
				return true // a power added beyond the template
			}
		}
	}
	return false
}

// ---- checks used by the rest of the package ----

// hrRole is the role the HRMS and Payroll code (hrms.go and the files
// beside it, which decide whom a person sees by role) goes by: Admin, then
// the HR manager, Payroll and the compliance officer when the person holds
// one of them, unedited or as the template of a role the firm edited or
// made that keeps the screen it works on (People for HR and Compliance,
// Payroll for Payroll), else their role.
func (o *Ops) hrRole(ctx context.Context, tenant, actor string) (string, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil || role == RoleAdmin || role == RoleClient {
		return role, err
	}
	a, err := o.Access(ctx, tenant, actor)
	if err != nil {
		return "", err
	}
	for _, r := range []struct{ role, screen string }{{RoleHR, "people"}, {RolePayroll, "payroll"}, {RoleCompliance, "people"}} {
		if contains(a.Legacy, r.role) {
			return r.role, nil
		}
		for _, d := range a.Defs {
			if d.Template == r.role && d.Screens[r.screen].View {
				return r.role, nil
			}
		}
	}
	return role, nil
}

// payoutLimitCheck refuses approving a payout above the approver's limit.
// Unedited built-in roles have no limit, as before.
func (o *Ops) payoutLimitCheck(ctx context.Context, tenant, actor string, amount money.Amount) error {
	a, err := o.Access(ctx, tenant, actor)
	if err != nil {
		return err
	}
	if a.Admin || contains(a.Legacy, RoleChecker) || contains(a.Legacy, RoleRisk) {
		return nil
	}
	// The highest limit among the held roles that approve payouts; one
	// without a limit means none.
	lim := money.Amount(-1)
	for _, d := range a.Defs {
		if !d.Screens["money"].Approve && d.Template != RoleAdmin {
			continue
		}
		if d.Limits.PayoutLimit == "" {
			return nil
		}
		if x := mustAmt(d.Limits.PayoutLimit); x > lim {
			lim = x
		}
	}
	if lim < 0 || amount <= lim {
		return nil
	}
	return fmt.Errorf("%w: ₹%s is above your approval limit of ₹%s; someone with a higher limit must approve it", ErrForbidden, formatINR(amount.Dec()), formatINR(lim.Dec()))
}

// ---- validation and the plain-English summary ----

var reRoleName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 &().,'-]{1,39}$`)

// checkRole validates a role and tidies it: View with Do or Approve, known
// screens only, limits in range.
func checkRole(d *RoleDef) error {
	d.Name, d.About = strings.Join(strings.Fields(d.Name), " "), strings.TrimSpace(d.About)
	if !reRoleName.MatchString(d.Name) {
		return invalid("a role's name is 2 to 40 letters, digits and spaces, starting with a letter")
	}
	if len(d.About) > 300 {
		return invalid("keep the description under 300 characters")
	}
	if d.Template != "" && !contains(Roles, d.Template) {
		return invalid("start from a built-in role or from blank")
	}
	out := map[string]Power{}
	for k, p := range d.Screens {
		if _, ok := screens.Get(k); !ok {
			return invalid("unknown screen %q", k)
		}
		if p.Do || p.Approve {
			p.View = true
		}
		if adminScreens[k] {
			p.Do, p.Approve = false, false // users and roles: only an Admin changes them
		}
		if p.View {
			out[k] = p
		}
	}
	d.Screens = out
	switch d.Limits.Scope {
	case "":
		d.Limits.Scope = ScopeFirm
	case ScopeFirm, ScopeBranch, ScopeAP:
	default:
		return invalid("branch scope is the whole firm, own branch or own authorised person")
	}
	if d.Limits.PayoutLimit = strings.ReplaceAll(strings.TrimSpace(d.Limits.PayoutLimit), ",", ""); d.Limits.PayoutLimit != "" {
		a, err := money.ParseAmount(d.Limits.PayoutLimit)
		if err != nil || a <= 0 {
			return invalid("the payout approval limit is an amount in rupees (blank for no limit)")
		}
		d.Limits.PayoutLimit = a.String()
	}
	if d.Limits.Scope != ScopeFirm {
		for k := range d.Screens {
			if !contains(scopedScreens, k) {
				return invalid("a role limited to its own branch or authorised person reaches only the client-level screens; %s shows the whole firm's data", screens.Name(k))
			}
		}
	}
	return nil
}

// RoleSummary says in plain English what a role can and cannot do.
func RoleSummary(d RoleDef) []string {
	if d.Name == RoleAdmin && d.BuiltIn {
		return []string{"Everything, on every screen. Admin cannot be edited."}
	}
	base, _ := BuiltinRole(d.Template)
	var out []string
	var none []string
	for _, sec := range screens.Nav {
		var view, do, appr []string
		for _, x := range screens.All() {
			if x.Section != sec.Name {
				continue
			}
			p := d.Screens[x.Key]
			name := screens.Name(x.Key)
			switch {
			case !p.View && !p.Do && !p.Approve:
				continue
			case !p.Do && !p.Approve:
				view = append(view, name)
			}
			if p.Do {
				do = append(do, name+howFar(d, base, x.Key, kindDo))
			}
			if p.Approve {
				a := name + howFar(d, base, x.Key, kindApprove)
				if x.Key == "money" && d.Limits.PayoutLimit != "" {
					a += " (payouts up to ₹" + formatINR(mustAmt(d.Limits.PayoutLimit).Dec()) + ")"
				}
				appr = append(appr, a)
			}
		}
		if len(view)+len(do)+len(appr) == 0 {
			none = append(none, sec.Name)
			continue
		}
		var parts []string
		if len(do) > 0 {
			parts = append(parts, "creates and changes on "+strings.Join(do, ", "))
		}
		if len(appr) > 0 {
			parts = append(parts, "approves on "+strings.Join(appr, ", "))
		}
		if len(view) > 0 {
			parts = append(parts, "only looks at "+strings.Join(view, ", "))
		}
		out = append(out, sec.Name+": "+strings.Join(parts, "; ")+".")
	}
	if len(none) > 0 {
		out = append(out, "Cannot see anything under "+strings.Join(none, ", ")+".")
	}
	switch d.Limits.Scope {
	case ScopeBranch:
		out = append(out, "Sees only the clients of the person's own branch.")
	case ScopeAP:
		out = append(out, "Sees only the person's own clients as an authorised person, and their downline's.")
	default:
		out = append(out, "Sees every client of the firm.")
	}
	yes := func(b bool, y, n string) string {
		if b {
			return y
		}
		return n
	}
	out = append(out, yes(d.Limits.Brokerage, "Sees brokerage totals.", "Does not see brokerage totals."),
		yes(d.Limits.RevealIDs, "May reveal a client's full PAN, Aadhaar and bank numbers (each reveal is in the audit trail).", "Sees PAN, Aadhaar and bank numbers masked."),
		yes(d.Limits.Export, "May download files and exports.", "Cannot download files or exports."))
	return out
}

// howFar notes when a tick keeps the template's limits.
func howFar(d, base RoleDef, k, kind string) string {
	if d.Template != "" && d.Template != RoleAdmin && base.Screens[k].has(kind) {
		return " (as " + d.Template + " does)"
	}
	return ""
}

// RoleWarnings are what the Review step points out before a role is sent
// for approval.
func RoleWarnings(d RoleDef) []string {
	var out []string
	var both, admin []string
	for _, x := range screens.All() {
		p := d.Screens[x.Key]
		if p.Do && p.Approve {
			both = append(both, screens.Name(x.Key))
		}
		if x.Admin && (p.Do || p.Approve) {
			admin = append(admin, screens.Name(x.Key))
		}
	}
	if len(both) > 0 {
		out = append(out, "Both Do and Approve on "+strings.Join(both, ", ")+". Maker-checker still applies: nobody can approve an item they entered themselves, so someone else must approve their work.")
	}
	if len(admin) > 0 {
		out = append(out, "Admin-level screens: "+strings.Join(admin, ", ")+". Changes there affect the whole firm's system (connections, users, security, data and exit). Some things stay with Admin whatever the role: users and roles, sign-in resets, full data exports, closing the account and deleting the firm's data, the firm's encryption keys and Ledgerline support access settings.")
	}
	if len(d.Screens) == 0 {
		out = append(out, "The role has no screens: its holders would see nothing.")
	}
	if d.Limits.RevealIDs {
		out = append(out, "Holders may reveal full PAN, Aadhaar and bank numbers.")
	}
	if d.Screens["money"].Approve && d.Limits.PayoutLimit == "" {
		out = append(out, "Approves payouts with no amount limit.")
	}
	return out
}
