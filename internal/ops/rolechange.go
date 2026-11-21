package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- changing roles: one Admin proposes, another approves ----
//
// A change to a role (edited, made, reset to its default or retired) and
// giving someone roles on the Roles screen are proposals. Nothing changes
// until a second Admin approves; the proposer cannot approve their own.
// Every step is in the audit trail, and the change itself is a security
// event (secevents.go).

// RoleChange is one proposed change.
type RoleChange struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // role, reset, retire, assign
	// Role is the role as proposed (kind role).
	Role *RoleDef `json:"role,omitempty"`
	// Name is the role changed (role, reset, retire).
	Name string `json:"name,omitempty"`
	// User, Roles (main role first), Branch and Partner: kind assign.
	User     string   `json:"user,omitempty"`
	Roles    []string `json:"roles,omitempty"`
	Branch   string   `json:"branch,omitempty"`
	Partner  string   `json:"partner,omitempty"`
	From     []string `json:"from,omitempty"` // the roles held before (assign)
	Summary  []string `json:"summary,omitempty"`
	Warnings []string `json:"warnings,omitempty"`

	Status    string `json:"status"` // Pending, Approved, Rejected, Withdrawn
	By        string `json:"by"`
	At        string `json:"at"`
	DecidedBy string `json:"decided_by,omitempty"`
	DecidedAt string `json:"decided_at,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Role change statuses.
const (
	RCPending   = "Pending"
	RCApproved  = "Approved"
	RCRejected  = "Rejected"
	RCWithdrawn = "Withdrawn"
)

// roleAdmin checks the actor is really an Admin (never a preview, never
// Ledgerline support): users and roles are changed only by Admins, whatever
// screens another role has.
func (o *Ops) roleAdmin(ctx context.Context, tenant, actor, what string) error {
	if err := notSupport(actor, what); err != nil {
		return err
	}
	if _, ok := Previewing(ctx); ok {
		return fmt.Errorf("%w: this is a preview; nothing can be changed", ErrForbidden)
	}
	a, err := o.Access(ctx, tenant, actor)
	if err != nil {
		return err
	}
	if !a.Admin || actor == SystemActor {
		return fmt.Errorf("%w: %s needs an Admin (you are %s)", ErrForbidden, what, a.Name)
	}
	return nil
}

// RoleChanges lists the proposals, newest first.
func (o *Ops) RoleChanges(ctx context.Context, tenant string) ([]RoleChange, error) {
	cs, err := docs.List[RoleChange](ctx, o.Docs, tenant, cRoleChange, "")
	for i, j := 0, len(cs)-1; i < j; i, j = i+1, j-1 {
		cs[i], cs[j] = cs[j], cs[i]
	}
	return cs, err
}

// CheckRole validates a role as the editor's Review step shows it: the
// role tidied, what it can and cannot do, and the warnings. Nothing is
// saved.
func (o *Ops) CheckRole(ctx context.Context, tenant, actor string, d RoleDef) (RoleDef, []string, []string, error) {
	if err := o.roleAdmin(ctx, tenant, actor, "Changing roles"); err != nil {
		return d, nil, nil, err
	}
	d, err := o.proposedRole(ctx, tenant, d)
	if err != nil {
		return d, nil, nil, err
	}
	return d, RoleSummary(d), RoleWarnings(d), nil
}

// proposedRole validates a role being edited or made and fills what the
// editor does not decide (built-in flags, the template of a firm's role).
func (o *Ops) proposedRole(ctx context.Context, tenant string, d RoleDef) (RoleDef, error) {
	if err := checkRole(&d); err != nil {
		return d, err
	}
	switch {
	case d.Name == RoleAdmin || d.Name == RoleClient:
		return d, invalid("the %s role cannot be edited", d.Name)
	case editableBuiltin(d.Name):
		b, _ := BuiltinRole(d.Name)
		d.BuiltIn, d.Edited, d.Template = true, true, d.Name
		if d.Limits.Scope != b.Limits.Scope {
			return d, invalid("%s always reaches %s; copy it into a new role for another scope", d.Name, map[string]string{ScopeFirm: "the whole firm", ScopeBranch: "its own branch", ScopeAP: "its own clients"}[b.Limits.Scope])
		}
	default:
		for _, n := range append(append([]string{}, Roles...), RoleClient) {
			if strings.EqualFold(n, d.Name) {
				return d, invalid("%s is the name of a built-in role", n)
			}
		}
		d.BuiltIn, d.Edited = false, false
		if old, _, err := docs.Get[RoleDef](ctx, o.Docs, tenant, cRole, d.Name); err == nil && !old.Retired {
			d.Template = old.Template // a role keeps what it started from
		} else {
			own, _ := docs.List[RoleDef](ctx, o.Docs, tenant, cRole, "")
			for _, x := range own {
				if !x.Retired && strings.EqualFold(x.Name, d.Name) {
					return d, invalid("there is already a role called %s", x.Name)
				}
			}
		}
	}
	d.Retired, d.ChangedBy, d.ApprovedBy, d.ApprovedAt = false, "", "", ""
	return d, nil
}

func (o *Ops) pendingFor(ctx context.Context, tenant, role, user string) error {
	cs, _ := docs.List[RoleChange](ctx, o.Docs, tenant, cRoleChange, "")
	for _, c := range cs {
		if c.Status != RCPending {
			continue
		}
		if (role != "" && c.Name == role) || (user != "" && c.User == user) {
			return fmt.Errorf("%w: %s is waiting for approval already; approve, reject or withdraw it first", ErrState, c.ID)
		}
	}
	return nil
}

func (o *Ops) proposeRoleChange(ctx context.Context, tenant, actor string, c RoleChange) (RoleChange, error) {
	n, err := o.next(ctx, tenant, "role_change", actor)
	if err != nil {
		return c, err
	}
	c.ID, c.Status, c.By, c.At = fmt.Sprintf("RC-%05d", n), RCPending, actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cRoleChange, c.ID, c, actor); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "role.proposed", c.ID, map[string]any{"kind": c.Kind, "role": c.Name, "user": c.User, "roles": strings.Join(c.Roles, ", ")})
	what := "role " + c.Name
	if c.Kind == "assign" {
		what = "roles of " + c.User
	}
	o.raiseWork(ctx, tenant, "roles", c.ID, "Approve the change to the "+what, actor+" proposed it; another Admin must approve it before it takes effect.", "", RoleAdmin, "roles", "")
	return c, nil
}

// ProposeRole proposes a role, new or changed (a built-in role edited).
func (o *Ops) ProposeRole(ctx context.Context, tenant, actor string, d RoleDef) (RoleChange, error) {
	if err := o.roleAdmin(ctx, tenant, actor, "Changing roles"); err != nil {
		return RoleChange{}, err
	}
	d, err := o.proposedRole(ctx, tenant, d)
	if err != nil {
		return RoleChange{}, err
	}
	if err := o.pendingFor(ctx, tenant, d.Name, ""); err != nil {
		return RoleChange{}, err
	}
	return o.proposeRoleChange(ctx, tenant, actor, RoleChange{Kind: "role", Role: &d, Name: d.Name, Summary: RoleSummary(d), Warnings: RoleWarnings(d)})
}

// ProposeRoleReset proposes putting an edited built-in role back as shipped.
func (o *Ops) ProposeRoleReset(ctx context.Context, tenant, actor, name string) (RoleChange, error) {
	if err := o.roleAdmin(ctx, tenant, actor, "Changing roles"); err != nil {
		return RoleChange{}, err
	}
	if d, ok := o.roleDef(ctx, tenant, name); !ok || !d.BuiltIn || !d.Edited {
		return RoleChange{}, invalid("only an edited built-in role can be reset to its default")
	}
	if err := o.pendingFor(ctx, tenant, name, ""); err != nil {
		return RoleChange{}, err
	}
	b, _ := BuiltinRole(name)
	return o.proposeRoleChange(ctx, tenant, actor, RoleChange{Kind: "reset", Name: name, Summary: RoleSummary(b)})
}

// ProposeRoleRetire proposes retiring a role the firm made; nobody may
// hold it.
func (o *Ops) ProposeRoleRetire(ctx context.Context, tenant, actor, name string) (RoleChange, error) {
	if err := o.roleAdmin(ctx, tenant, actor, "Changing roles"); err != nil {
		return RoleChange{}, err
	}
	d, ok := o.roleDef(ctx, tenant, name)
	if !ok || d.BuiltIn {
		return RoleChange{}, invalid("only a role the firm made can be retired")
	}
	if err := o.noHolders(ctx, tenant, name); err != nil {
		return RoleChange{}, err
	}
	if err := o.pendingFor(ctx, tenant, name, ""); err != nil {
		return RoleChange{}, err
	}
	return o.proposeRoleChange(ctx, tenant, actor, RoleChange{Kind: "retire", Name: name})
}

func (o *Ops) noHolders(ctx context.Context, tenant, name string) error {
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return err
	}
	for _, u := range us {
		if contains(u.roles(), name) {
			return fmt.Errorf("%w: %s still holds %s; give them other roles first", ErrState, u.ID, name)
		}
	}
	return nil
}

// roleScope is how far a role reaches.
func (o *Ops) roleScope(ctx context.Context, tenant, name string) (string, error) {
	d, ok := o.roleDef(ctx, tenant, name)
	if !ok || name == RoleClient {
		return "", invalid("unknown role %q", name)
	}
	return d.Limits.Scope, nil
}

// ProposeUserRoles proposes the roles a person holds (main role first),
// with their branch or partner code when the roles reach only a branch or
// an authorised person's clients.
func (o *Ops) ProposeUserRoles(ctx context.Context, tenant, actor, user string, roles []string, branch, partner string) (RoleChange, error) {
	if err := o.roleAdmin(ctx, tenant, actor, "Giving people roles"); err != nil {
		return RoleChange{}, err
	}
	u, err := o.user(ctx, tenant, user)
	if err != nil {
		return RoleChange{}, err
	}
	if u.Role == RoleClient {
		return RoleChange{}, invalid("a client's portal login has no staff roles")
	}
	var rs []string
	for _, r := range roles {
		if r = strings.TrimSpace(r); r != "" && !contains(rs, r) {
			rs = append(rs, r)
		}
	}
	if len(rs) == 0 {
		return RoleChange{}, invalid("give at least one role")
	}
	scope := ""
	for _, r := range rs {
		s, err := o.roleScope(ctx, tenant, r)
		if err != nil {
			return RoleChange{}, err
		}
		if scope != "" && s != scope {
			return RoleChange{}, invalid("a person's roles must all reach the same clients (the whole firm, one branch, or one authorised person)")
		}
		scope = s
	}
	branch, partner = strings.TrimSpace(branch), strings.ToUpper(strings.TrimSpace(partner))
	switch scope {
	case ScopeBranch:
		if branch = firstOf(branch, u.Branch); branch == "" {
			return RoleChange{}, invalid("these roles reach one branch's clients: give the branch")
		}
		partner = ""
	case ScopeAP:
		if partner = firstOf(partner, u.Partner); partner == "" {
			return RoleChange{}, invalid("these roles reach one authorised person's clients: give the partner code")
		}
		branch = ""
	default:
		branch, partner = "", ""
	}
	if err := o.pendingFor(ctx, tenant, "", user); err != nil {
		return RoleChange{}, err
	}
	return o.proposeRoleChange(ctx, tenant, actor, RoleChange{Kind: "assign", User: user, Roles: rs, Branch: branch, Partner: partner, From: u.roles(),
		Summary: []string{fmt.Sprintf("%s (%s) would hold %s, instead of %s.", u.Name, u.ID, strings.Join(rs, " + "), strings.Join(u.roles(), " + "))}})
}

// WithdrawRoleChange withdraws one's own proposal.
func (o *Ops) WithdrawRoleChange(ctx context.Context, tenant, actor, id string) (RoleChange, error) {
	if err := o.roleAdmin(ctx, tenant, actor, "Changing roles"); err != nil {
		return RoleChange{}, err
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cRoleChange, id, actor, func(c *RoleChange) error {
		if c.Status != RCPending {
			return fmt.Errorf("%w: %s is %s", ErrState, c.ID, c.Status)
		}
		if c.By != actor {
			return fmt.Errorf("%w: only %s, who proposed it, can withdraw it; reject it instead", ErrForbidden, c.By)
		}
		c.Status, c.DecidedBy, c.DecidedAt = RCWithdrawn, actor, o.now().Format(time.RFC3339)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "role.withdrawn", id, nil)
		o.closeWork(ctx, tenant, actor, "roles", id, "Withdrawn")
	}
	return c, err
}

// DecideRoleChange approves (and applies) or rejects a proposal. The
// Admin who proposed it cannot decide it.
func (o *Ops) DecideRoleChange(ctx context.Context, tenant, actor, id string, approve bool, note string) (RoleChange, error) {
	if err := o.roleAdmin(ctx, tenant, actor, "Approving role changes"); err != nil {
		return RoleChange{}, err
	}
	c, _, err := docs.Get[RoleChange](ctx, o.Docs, tenant, cRoleChange, id)
	if err != nil {
		return c, err
	}
	if c.Status != RCPending {
		return c, fmt.Errorf("%w: %s is %s", ErrState, c.ID, c.Status)
	}
	if c.By == actor {
		return c, fmt.Errorf("%w: the Admin who proposed a role change cannot approve it; another Admin must", ErrForbidden)
	}
	note = strings.TrimSpace(note)
	if !approve && note == "" {
		return c, invalid("give the reason for rejecting")
	}
	if approve {
		if err := o.applyRoleChange(ctx, tenant, actor, c); err != nil {
			return c, err
		}
	}
	c, err = docs.Update(ctx, o.Docs, tenant, cRoleChange, id, actor, func(x *RoleChange) error {
		x.Status, x.DecidedBy, x.DecidedAt, x.Note = map[bool]string{true: RCApproved, false: RCRejected}[approve], actor, o.now().Format(time.RFC3339), note
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "role.change_"+strings.ToLower(c.Status), id, map[string]any{"kind": c.Kind, "role": c.Name, "user": c.User, "proposed_by": c.By, "note": note})
		o.closeWork(ctx, tenant, actor, "roles", id, c.Status)
	}
	return c, err
}

func (o *Ops) applyRoleChange(ctx context.Context, tenant, actor string, c RoleChange) error {
	now := o.now().Format(time.RFC3339)
	switch c.Kind {
	case "role":
		d, err := o.proposedRole(ctx, tenant, *c.Role)
		if err != nil {
			return err
		}
		d.ChangedBy, d.ApprovedBy, d.ApprovedAt = c.By, actor, now
		if err := docs.Upsert(ctx, o.Docs, tenant, cRole, d.Name, d, actor); err != nil {
			return err
		}
		o.log(ctx, tenant, actor, "role.changed", d.Name, map[string]any{"change": c.ID, "proposed_by": c.By, "built_in": d.BuiltIn, "screens": len(d.Screens)})
	case "reset":
		b, _ := BuiltinRole(c.Name)
		b.ChangedBy, b.ApprovedBy, b.ApprovedAt = c.By, actor, now
		if err := docs.Upsert(ctx, o.Docs, tenant, cRole, c.Name, b, actor); err != nil {
			return err
		}
		o.log(ctx, tenant, actor, "role.changed", c.Name, map[string]any{"change": c.ID, "proposed_by": c.By, "reset": true})
	case "retire":
		if err := o.noHolders(ctx, tenant, c.Name); err != nil {
			return err
		}
		if _, err := docs.Update(ctx, o.Docs, tenant, cRole, c.Name, actor, func(d *RoleDef) error { d.Retired = true; return nil }); err != nil {
			return err
		}
		o.log(ctx, tenant, actor, "role.changed", c.Name, map[string]any{"change": c.ID, "proposed_by": c.By, "retired": true})
	case "assign":
		for _, r := range c.Roles {
			if _, err := o.roleScope(ctx, tenant, r); err != nil {
				return err
			}
		}
		prev, err := o.user(ctx, tenant, c.User)
		if err != nil {
			return err
		}
		if _, err := docs.Update(ctx, o.Docs, tenant, cUser, c.User, actor, func(u *User) error {
			u.Role, u.MoreRoles, u.Branch, u.Partner = c.Roles[0], append([]string(nil), c.Roles[1:]...), c.Branch, c.Partner
			return nil
		}); err != nil {
			return err
		}
		// A security event, as on the Users screen; granting Admin can
		// open an incident.
		to := strings.Join(c.Roles, " + ")
		if contains(c.Roles, RoleAdmin) && !contains(prev.roles(), RoleAdmin) {
			to = RoleAdmin
		}
		o.log(ctx, tenant, actor, "user.role_changed", c.User, map[string]any{"from": strings.Join(prev.roles(), " + "), "to": to, "roles": strings.Join(c.Roles, ", "), "change": c.ID, "proposed_by": c.By})
	default:
		return invalid("unknown change %q", c.Kind)
	}
	return nil
}

// ---- preview as a role ----

const cRolePreview = "role_preview" // an Admin's open preview, by user ID

// RolePreviewDoc is an Admin's open preview.
type RolePreviewDoc struct {
	Role    RoleDef `json:"role"`
	Started string  `json:"started"`
	Ended   string  `json:"ended,omitempty"`
}

// StartRolePreview opens a read-only preview of a role (one saved, or the
// role being edited) for the Admin: the console then shows only what the
// role would see, and nothing can be changed. Recorded in the audit trail.
func (o *Ops) StartRolePreview(ctx context.Context, tenant, actor string, d RoleDef) (RoleDef, error) {
	if err := o.roleAdmin(ctx, tenant, actor, "Previewing roles"); err != nil {
		return d, err
	}
	if d.Screens == nil {
		x, ok := o.roleDef(ctx, tenant, d.Name)
		if !ok {
			return d, invalid("unknown role %q", d.Name)
		}
		d = x
	} else {
		if err := checkRole(&d); err != nil {
			return d, err
		}
		if editableBuiltin(d.Name) {
			d.BuiltIn, d.Edited, d.Template = true, true, d.Name
		}
	}
	p := RolePreviewDoc{Role: d, Started: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cRolePreview, actor, p, actor); err != nil {
		return d, err
	}
	o.log(ctx, tenant, actor, "role.preview_started", d.Name, map[string]any{"screens": len(d.Screens)})
	return d, nil
}

// RolePreview returns the Admin's open preview.
func (o *Ops) RolePreview(ctx context.Context, tenant, actor string) (RoleDef, bool) {
	p, _, err := docs.Get[RolePreviewDoc](ctx, o.Docs, tenant, cRolePreview, actor)
	if err != nil || p.Ended != "" {
		return RoleDef{}, false
	}
	return p.Role, true
}

// EndRolePreview closes the Admin's preview.
func (o *Ops) EndRolePreview(ctx context.Context, tenant, actor string) error {
	p, _, err := docs.Get[RolePreviewDoc](ctx, o.Docs, tenant, cRolePreview, actor)
	if err != nil || p.Ended != "" {
		return nil
	}
	p.Ended = o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cRolePreview, actor, p, actor); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "role.preview_ended", p.Role.Name, nil)
	return nil
}
