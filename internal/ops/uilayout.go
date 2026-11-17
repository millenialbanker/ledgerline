package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"time"

	"ledgerline/internal/docs"
)

// ---- screen layouts ----
//
// The console can be laid out in several ways: the classic sidebar of
// grouped screens, or one of the layout directions (a day runbook, an
// exceptions inbox, department workspaces, a dark control room, supervised
// Autopilot, a role home of tiles, a spreadsheet workbench, phone
// approvals). Every layout reaches every screen the user may open; only
// the way round the console changes.
//
// An Admin sets the firm's layout, a layout per role, whether staff may pick
// their own and from which, and the building blocks every layout shares:
// how a client opens, the search and command bar, the look, and the default
// table density and colour theme. Each user may set their own density and
// theme, and their own layout when the firm allows it. The console draws
// the layouts; the server keeps the choices and works out which applies.

const (
	cUILayout = "uilayout" // each user's choices, by user ID
	cUIPolicy = "uipolicy" // the firm's policy
)

// firmUIID is the firm policy's key; user IDs can't contain "*".
const firmUIID = "*firm"

// UILayoutDef is one layout the console can draw.
type UILayoutDef struct {
	ID    string `json:"id"`
	Code  string `json:"code"` // the letter in the layout directions; "" for the classic layout
	Name  string `json:"name"`
	About string `json:"about"`
	// Scoped is true for layouts branch staff and authorised persons can
	// use: those that work from their own clients and work items.
	Scoped bool `json:"scoped"`
}

// Layout IDs.
const (
	LayoutClassic    = "classic"
	LayoutRunbook    = "runbook"
	LayoutInbox      = "inbox"
	LayoutWorkspaces = "workspaces"
	LayoutControl    = "control"
	LayoutAutopilot  = "autopilot"
	LayoutRoleHome   = "rolehome"
	LayoutGrid       = "grid"
	LayoutPhone      = "phone"
)

// UILayouts is every layout, in the order the console offers them.
var UILayouts = []UILayoutDef{
	{LayoutClassic, "", "Classic", "Today's layout: a dark sidebar of grouped screens, the trade date and search across the top, and the dashboard you arrange yourself.", true},
	{LayoutRunbook, "A", "Day runbook", "The day as numbered steps from before the open to the commodity close. Each step shows whether it is done, needs someone or is waiting, and opens its screen.", false},
	{LayoutInbox, "B", "Exceptions inbox", "Only what needs a person: the work queue grouped by when it is due, with the selected item's detail, its prepared action and keyboard shortcuts. A narrow icon rail opens the rest.", true},
	{LayoutWorkspaces, "C", "Workspaces", "A hub per department (Operations, Risk, Money, Compliance and so on) across the top, each with its own menu and a page of its daily steps. Closest to the classic layout.", false},
	{LayoutControl, "D", "Control room", "One dense dark screen for the whole day: a timeline of the steps, the day's numbers, margin, Autopilot by stage, contract notes, what needs a person and the day's files.", false},
	{LayoutAutopilot, "E", "Supervised Autopilot", "Ledgerline does the work and people approve checkpoints: the close as a plan, the next approval waiting for you, how much each job does alone, and what Autopilot did.", false},
	{LayoutRoleHome, "F", "Role home", "A home of tiles chosen for the person's role (director, ops, risk, accounts, compliance, branch), which each person can change.", false},
	{LayoutGrid, "G", "Spreadsheet workbench", "Spreadsheet-style tables of clients, margin, breaks and the work queue, with saved views, filters, grouping, chosen columns, totals, a peek panel and export.", true},
	{LayoutPhone, "H", "Phone approvals", "Made for a phone: approvals waiting for you with the prepared action, today at a glance, clients and Ask Ledgy, with a bar of tabs at the bottom.", true},
}

// LayoutDef returns a layout by ID.
func LayoutDef(id string) (UILayoutDef, bool) {
	for _, l := range UILayouts {
		if l.ID == id {
			return l, true
		}
	}
	return UILayoutDef{}, false
}

// Building-block choices.
const (
	ClientOpensDrawer = "drawer" // the side panel (today's behaviour)
	ClientOpensPage   = "page"   // a full client page with sections
	LookClassic       = "classic"
	LookNew           = "new"
	DensityComfort    = "comfortable"
	DensityCompact    = "compact"
	ThemeSystem       = "system"
	ThemeLight        = "light"
	ThemeDark         = "dark"
)

// UIPolicy is the firm's choice, set by an Admin.
type UIPolicy struct {
	Default string            `json:"default"`         // layout for everyone without a role layout
	Roles   map[string]string `json:"roles,omitempty"` // role → layout
	// StaffChoose lets each user pick their own layout from Allowed (every
	// layout when Allowed is empty).
	StaffChoose bool     `json:"staff_choose"`
	Allowed     []string `json:"allowed,omitempty"`
	// PhoneAuto switches to phone approvals on a phone-sized screen, for
	// users whose layout isn't already the phone layout.
	PhoneAuto bool `json:"phone_auto"`

	ClientOpens string `json:"client_opens"` // drawer, page
	CommandBar  bool   `json:"command_bar"`  // ⌘K search and command bar
	Look        string `json:"look"`         // classic, new
	Density     string `json:"density"`      // default for users who haven't chosen
	Theme       string `json:"theme"`        // default for users who haven't chosen

	By    string `json:"by,omitempty"`
	Saved string `json:"saved,omitempty"`
}

// DefaultUIPolicy keeps the console as it was: the classic layout for
// everyone and the client side panel; the command bar is on.
func DefaultUIPolicy() UIPolicy {
	return UIPolicy{Default: LayoutClassic, ClientOpens: ClientOpensDrawer, CommandBar: true, Look: LookClassic, Density: DensityComfort, Theme: ThemeSystem}
}

// GridView is a saved view of the spreadsheet workbench.
type GridView struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Source  string            `json:"source"` // clients, rms, breaks, work
	Query   string            `json:"q,omitempty"`
	Filters map[string]string `json:"filters,omitempty"` // column → value
	Sort    string            `json:"sort,omitempty"`    // column, "-" prefix for descending
	Group   string            `json:"group,omitempty"`
	Columns []string          `json:"columns,omitempty"`
}

// UIPref is one user's own choices. Empty fields follow the firm.
type UIPref struct {
	Layout  string     `json:"layout,omitempty"`
	Density string     `json:"density,omitempty"`
	Theme   string     `json:"theme,omitempty"`
	Tiles   []string   `json:"tiles,omitempty"` // role home tiles, in order; nil = the role's tiles
	Views   []GridView `json:"views,omitempty"` // workbench saved views
	Saved   string     `json:"saved,omitempty"`
}

// UIEffective is what applies to the user now.
type UIEffective struct {
	Layout      string `json:"layout"`
	Source      string `json:"source"` // you, role, firm, scoped
	PhoneAuto   bool   `json:"phone_auto"`
	ClientOpens string `json:"client_opens"`
	CommandBar  bool   `json:"command_bar"`
	Look        string `json:"look"`
	Density     string `json:"density"`
	Theme       string `json:"theme"`
	CanChoose   bool   `json:"can_choose"`
	// Choices are the layouts this user may pick when CanChoose.
	Choices []string `json:"choices"`
	Note    string   `json:"note,omitempty"`
}

// UIView is what the layout screens need.
type UIView struct {
	Layouts   []UILayoutDef `json:"layouts"`
	Policy    UIPolicy      `json:"policy"`
	Mine      UIPref        `json:"mine"`
	Effective UIEffective   `json:"effective"`
	Roles     []string      `json:"roles"`
}

const (
	maxGridViews = 30
	maxTiles     = 40
)

var (
	reTileID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	reViewID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	reColumn = regexp.MustCompile(`^[a-z0-9_.]{1,40}$`)
)

var gridSources = map[string]bool{"clients": true, "rms": true, "breaks": true, "work": true}

func oneOf(v string, ok ...string) bool {
	for _, x := range ok {
		if v == x {
			return true
		}
	}
	return false
}

// scopedRole is a role that sees only its own clients.
func scopedRole(r string) bool { return r == RoleBranch || r == RoleAP }

// CheckUIPolicy validates a firm policy and fills its blanks.
func CheckUIPolicy(p UIPolicy) (UIPolicy, error) {
	d := DefaultUIPolicy()
	if p.Default == "" {
		p.Default = d.Default
	}
	if _, ok := LayoutDef(p.Default); !ok {
		return p, invalid("unknown layout %q", p.Default)
	}
	for r, l := range p.Roles {
		if !contains(Roles, r) {
			return p, invalid("unknown role %q", r)
		}
		if l == "" {
			delete(p.Roles, r)
			continue
		}
		def, ok := LayoutDef(l)
		if !ok {
			return p, invalid("unknown layout %q for %s", l, r)
		}
		if scopedRole(r) && !def.Scoped {
			return p, invalid("%s can't use the %s layout: it works from the whole firm's day; choose Classic, Exceptions inbox, Spreadsheet workbench or Phone approvals", r, def.Name)
		}
	}
	if len(p.Roles) == 0 {
		p.Roles = nil
	}
	seen := map[string]bool{}
	allowed := p.Allowed[:0:0]
	for _, l := range p.Allowed {
		if _, ok := LayoutDef(l); !ok {
			return p, invalid("unknown layout %q", l)
		}
		if !seen[l] {
			seen[l] = true
			allowed = append(allowed, l)
		}
	}
	p.Allowed = allowed
	if len(p.Allowed) == 0 || len(p.Allowed) == len(UILayouts) {
		p.Allowed = nil // every layout
	}
	if p.ClientOpens == "" {
		p.ClientOpens = d.ClientOpens
	}
	if !oneOf(p.ClientOpens, ClientOpensDrawer, ClientOpensPage) {
		return p, invalid("a client opens in the side panel (drawer) or as a page")
	}
	if p.Look == "" {
		p.Look = d.Look
	}
	if !oneOf(p.Look, LookClassic, LookNew) {
		return p, invalid("the look is classic or new")
	}
	if p.Density == "" {
		p.Density = d.Density
	}
	if !oneOf(p.Density, DensityComfort, DensityCompact) {
		return p, invalid("tables are comfortable or compact")
	}
	if p.Theme == "" {
		p.Theme = d.Theme
	}
	if !oneOf(p.Theme, ThemeSystem, ThemeLight, ThemeDark) {
		return p, invalid("the colour theme is system, light or dark")
	}
	return p, nil
}

// checkUIPref validates a user's choices against the firm policy.
func checkUIPref(p UIPref, pol UIPolicy, role string) (UIPref, error) {
	if p.Layout != "" {
		def, ok := LayoutDef(p.Layout)
		if !ok {
			return p, invalid("unknown layout %q", p.Layout)
		}
		if !pol.StaffChoose && role != RoleAdmin {
			return p, invalid("your firm sets the layout; ask an Admin to change it")
		}
		if role != RoleAdmin && len(pol.Allowed) > 0 && !contains(pol.Allowed, p.Layout) {
			return p, invalid("your firm doesn't offer the %s layout", def.Name)
		}
		if scopedRole(role) && !def.Scoped {
			return p, invalid("the %s layout works from the whole firm's day and isn't open to %s", def.Name, role)
		}
	}
	if p.Density != "" && !oneOf(p.Density, DensityComfort, DensityCompact) {
		return p, invalid("tables are comfortable or compact")
	}
	if p.Theme != "" && !oneOf(p.Theme, ThemeSystem, ThemeLight, ThemeDark) {
		return p, invalid("the colour theme is system, light or dark")
	}
	if len(p.Tiles) > maxTiles {
		return p, invalid("a home holds at most %d tiles", maxTiles)
	}
	seen := map[string]bool{}
	for _, t := range p.Tiles {
		if !reTileID.MatchString(t) || seen[t] {
			return p, invalid("tile %q is not valid or appears twice", t)
		}
		seen[t] = true
	}
	if len(p.Views) > maxGridViews {
		return p, invalid("keep at most %d saved views", maxGridViews)
	}
	ids := map[string]bool{}
	for i, v := range p.Views {
		if !reViewID.MatchString(v.ID) || ids[v.ID] {
			return p, invalid("saved view %d has no valid ID, or its ID appears twice", i+1)
		}
		ids[v.ID] = true
		if v.Name == "" || len([]rune(v.Name)) > 60 {
			return p, invalid("a saved view needs a name of up to 60 characters")
		}
		if !gridSources[v.Source] {
			return p, invalid("saved view %q: unknown table %q", v.Name, v.Source)
		}
		if len(v.Query) > 200 || len(v.Filters) > 20 || len(v.Columns) > 60 {
			return p, invalid("saved view %q is too large", v.Name)
		}
		for c, f := range v.Filters {
			if !reColumn.MatchString(c) || len(f) > 100 {
				return p, invalid("saved view %q: bad filter %q", v.Name, c)
			}
		}
		for _, c := range append(append([]string{}, v.Columns...), trimDash(v.Sort), v.Group) {
			if c != "" && !reColumn.MatchString(c) {
				return p, invalid("saved view %q: bad column %q", v.Name, c)
			}
		}
	}
	return p, nil
}

func trimDash(s string) string {
	if len(s) > 0 && s[0] == '-' {
		return s[1:]
	}
	return s
}

// effectiveUI works out what applies to a user.
func effectiveUI(pol UIPolicy, mine UIPref, role string) UIEffective {
	e := UIEffective{PhoneAuto: pol.PhoneAuto, ClientOpens: pol.ClientOpens, CommandBar: pol.CommandBar, Look: pol.Look, Density: pol.Density, Theme: pol.Theme, Choices: []string{}}
	if mine.Density != "" {
		e.Density = mine.Density
	}
	if mine.Theme != "" {
		e.Theme = mine.Theme
	}
	usable := func(id string) bool {
		def, ok := LayoutDef(id)
		return ok && (!scopedRole(role) || def.Scoped)
	}
	e.CanChoose = pol.StaffChoose || role == RoleAdmin
	for _, l := range UILayouts {
		if !usable(l.ID) {
			continue
		}
		if role == RoleAdmin || len(pol.Allowed) == 0 || contains(pol.Allowed, l.ID) {
			e.Choices = append(e.Choices, l.ID)
		}
	}
	switch {
	case e.CanChoose && mine.Layout != "" && usable(mine.Layout) && contains(e.Choices, mine.Layout):
		e.Layout, e.Source = mine.Layout, "you"
	case pol.Roles[role] != "" && usable(pol.Roles[role]):
		e.Layout, e.Source = pol.Roles[role], "role"
	case usable(pol.Default):
		e.Layout, e.Source = pol.Default, "firm"
	default:
		// A firm-wide layout that needs the whole firm's day: branch staff
		// and authorised persons keep the classic layout.
		e.Layout, e.Source = LayoutClassic, "scoped"
		def, _ := LayoutDef(pol.Default)
		e.Note = fmt.Sprintf("The firm's %s layout works from the whole firm's day, so %s logins use the classic layout.", def.Name, role)
	}
	if mine.Layout != "" && e.Source != "you" && e.Note == "" {
		def, _ := LayoutDef(mine.Layout)
		if e.CanChoose {
			e.Note = fmt.Sprintf("Your choice of %s is no longer offered by the firm.", def.Name)
		} else {
			e.Note = fmt.Sprintf("Your firm sets the layout now, so your choice of %s is not used.", def.Name)
		}
	}
	if role == RoleClient {
		// The client portal keeps its own layout.
		e = UIEffective{Layout: LayoutClassic, Source: "firm", ClientOpens: ClientOpensDrawer, Look: pol.Look, Density: e.Density, Theme: e.Theme, Choices: []string{}}
	}
	return e
}

func (o *Ops) uiPolicy(ctx context.Context, tenant string) UIPolicy {
	p, _, _ := docs.GetOr(ctx, o.Docs, tenant, cUIPolicy, firmUIID, DefaultUIPolicy())
	if q, err := CheckUIPolicy(p); err == nil {
		return q
	}
	return DefaultUIPolicy()
}

func (o *Ops) uiPref(ctx context.Context, tenant, actor string) UIPref {
	p, _, _ := docs.GetOr(ctx, o.Docs, tenant, cUILayout, actor, UIPref{})
	return p
}

// EffectiveUI is the layout and building blocks for a user (for /v1/me).
func (o *Ops) EffectiveUI(ctx context.Context, tenant, actor string) UIEffective {
	role, _ := o.RoleOf(ctx, tenant, actor)
	return effectiveUI(o.uiPolicy(ctx, tenant), o.uiPref(ctx, tenant, actor), role)
}

// UILayout returns the layout screens' data for a user.
func (o *Ops) UILayout(ctx context.Context, tenant, actor string) (UIView, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return UIView{}, err
	}
	pol, mine := o.uiPolicy(ctx, tenant), o.uiPref(ctx, tenant, actor)
	return UIView{Layouts: UILayouts, Policy: pol, Mine: mine, Effective: effectiveUI(pol, mine, role), Roles: Roles}, nil
}

// SaveUIPolicy stores the firm's layout policy (Admin).
func (o *Ops) SaveUIPolicy(ctx context.Context, tenant, actor string, p UIPolicy) (UIView, error) {
	if err := o.require(ctx, tenant, actor, "Setting the firm's screen layout", RoleAdmin); err != nil {
		return UIView{}, err
	}
	p, err := CheckUIPolicy(p)
	if err != nil {
		return UIView{}, err
	}
	old := o.uiPolicy(ctx, tenant)
	p.By, p.Saved = actor, time.Now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cUIPolicy, firmUIID, p, actor); err != nil {
		return UIView{}, err
	}
	o.log(ctx, tenant, actor, "ui.layout_policy", "", map[string]any{"default": p.Default, "roles": rolesLine(p.Roles), "staff_choose": p.StaffChoose, "allowed": p.Allowed, "phone_auto": p.PhoneAuto, "client_opens": p.ClientOpens, "command_bar": p.CommandBar, "look": p.Look, "density": p.Density, "theme": p.Theme, "was": old.Default})
	return o.UILayout(ctx, tenant, actor)
}

func rolesLine(m map[string]string) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	s := ""
	for _, k := range ks {
		if s != "" {
			s += ", "
		}
		s += k + ": " + m[k]
	}
	return s
}

// UIPrefPatch is a change to a user's choices: only the fields sent change.
type UIPrefPatch struct {
	Layout  *string     `json:"layout"`
	Density *string     `json:"density"`
	Theme   *string     `json:"theme"`
	Tiles   *[]string   `json:"tiles"`
	Views   *[]GridView `json:"views"`
}

// SaveUIPref stores the user's own choices. An empty string returns a
// field to the firm's choice.
func (o *Ops) SaveUIPref(ctx context.Context, tenant, actor string, ch UIPrefPatch) (UIView, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return UIView{}, err
	}
	if role == RoleClient {
		return UIView{}, fmt.Errorf("%w: the client portal has one layout", ErrForbidden)
	}
	p := o.uiPref(ctx, tenant, actor)
	layoutChanged := ch.Layout != nil && *ch.Layout != p.Layout
	if ch.Layout != nil {
		p.Layout = *ch.Layout
	}
	if ch.Density != nil {
		p.Density = *ch.Density
	}
	if ch.Theme != nil {
		p.Theme = *ch.Theme
	}
	if ch.Tiles != nil {
		p.Tiles = *ch.Tiles
	}
	if ch.Views != nil {
		p.Views = *ch.Views
	}
	pol := o.uiPolicy(ctx, tenant)
	if ch.Layout == nil && p.Layout != "" {
		// A choice the firm no longer offers stays saved (it may return)
		// but isn't checked again when something else changes.
		q := p
		q.Layout = ""
		if _, err := checkUIPref(q, pol, role); err != nil {
			return UIView{}, err
		}
	} else if p, err = checkUIPref(p, pol, role); err != nil {
		return UIView{}, err
	}
	p.Saved = time.Now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cUILayout, actor, p, actor); err != nil {
		return UIView{}, err
	}
	if layoutChanged {
		o.log(ctx, tenant, actor, "ui.layout_mine", actor, map[string]any{"layout": p.Layout})
	}
	return o.UILayout(ctx, tenant, actor)
}
