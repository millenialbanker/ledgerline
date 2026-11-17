package ops

import (
	"errors"
	"testing"
)

func sp(s string) *string { return &s }

func TestUILayoutPolicyAndChoices(t *testing.T) {
	o, ctx := setup(t)

	// Nothing saved: the classic layout, the client side panel, the
	// command bar on, comfortable tables and the system theme.
	v, err := o.UILayout(ctx, T, "priya.nair")
	if err != nil {
		t.Fatal(err)
	}
	e := v.Effective
	if e.Layout != LayoutClassic || e.Source != "firm" || e.ClientOpens != ClientOpensDrawer || !e.CommandBar || e.Density != DensityComfort || e.Theme != ThemeSystem || e.Look != LookClassic || e.CanChoose {
		t.Fatalf("defaults %+v", e)
	}
	if len(v.Layouts) != 9 || v.Layouts[0].ID != LayoutClassic {
		t.Fatalf("layouts %+v", v.Layouts)
	}

	// Staff can't pick a layout until the firm allows it, but can set
	// their own density and theme.
	if _, err := o.SaveUIPref(ctx, T, "priya.nair", UIPrefPatch{Layout: sp(LayoutInbox)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("picked a layout the firm sets: %v", err)
	}
	if v, err = o.SaveUIPref(ctx, T, "priya.nair", UIPrefPatch{Density: sp(DensityCompact), Theme: sp(ThemeDark)}); err != nil || v.Effective.Density != DensityCompact || v.Effective.Theme != ThemeDark {
		t.Fatalf("density and theme %+v %v", v.Effective, err)
	}

	// Only an Admin sets the firm's policy.
	pol := UIPolicy{Default: LayoutWorkspaces, Roles: map[string]string{RoleRisk: LayoutControl, RoleBranch: LayoutInbox}, StaffChoose: true, Allowed: []string{LayoutClassic, LayoutInbox, LayoutGrid, LayoutInbox}, ClientOpens: ClientOpensPage, CommandBar: true, Look: LookNew}
	if _, err := o.SaveUIPolicy(ctx, T, "priya.nair", pol); err == nil {
		t.Fatal("an Ops maker set the firm's layout")
	}
	if v, err = o.SaveUIPolicy(ctx, T, "admin", pol); err != nil {
		t.Fatal(err)
	}
	if len(v.Policy.Allowed) != 3 || v.Policy.By != "admin" || v.Policy.Density != DensityComfort || v.Policy.Theme != ThemeSystem {
		t.Fatalf("saved policy %+v", v.Policy)
	}

	// The firm's layout, a role's layout, and branch staff's layout.
	if e := o.EffectiveUI(ctx, T, "farhan.khan"); e.Layout != LayoutWorkspaces || e.Source != "firm" || e.ClientOpens != ClientOpensPage || e.Look != LookNew || !e.CanChoose || len(e.Choices) != 3 {
		t.Fatalf("checker %+v", e)
	}
	if e := o.EffectiveUI(ctx, T, "anita.rao"); e.Layout != LayoutControl || e.Source != "role" {
		t.Fatalf("risk %+v", e)
	}
	if e := o.EffectiveUI(ctx, T, "meera.iyer"); e.Layout != LayoutInbox || e.Source != "role" {
		t.Fatalf("branch %+v", e)
	}
	// An authorised person has no role layout, and the firm's workspaces
	// need the whole firm's day: classic, with a note saying why.
	if e := o.EffectiveUI(ctx, T, "sunrise.ap"); e.Layout != LayoutClassic || e.Source != "scoped" || e.Note == "" {
		t.Fatalf("authorised person %+v", e)
	}
	for _, c := range o.EffectiveUI(ctx, T, "sunrise.ap").Choices {
		if d, _ := LayoutDef(c); !d.Scoped {
			t.Fatalf("authorised person offered %s", c)
		}
	}

	// A user's own choice, from those the firm offers.
	if _, err := o.SaveUIPref(ctx, T, "farhan.khan", UIPrefPatch{Layout: sp(LayoutControl)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("picked a layout the firm doesn't offer: %v", err)
	}
	if v, err = o.SaveUIPref(ctx, T, "farhan.khan", UIPrefPatch{Layout: sp(LayoutGrid)}); err != nil || v.Effective.Layout != LayoutGrid || v.Effective.Source != "you" {
		t.Fatalf("own layout %+v %v", v.Effective, err)
	}
	// Branch staff can't pick a layout that needs the whole firm's day.
	if _, err := o.SaveUIPref(ctx, T, "meera.iyer", UIPrefPatch{Layout: sp(LayoutRunbook)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("branch staff picked the runbook: %v", err)
	}

	// The firm stops offering it: the choice is kept but not used.
	pol.Allowed = []string{LayoutClassic, LayoutInbox}
	if _, err := o.SaveUIPolicy(ctx, T, "admin", pol); err != nil {
		t.Fatal(err)
	}
	if e := o.EffectiveUI(ctx, T, "farhan.khan"); e.Layout != LayoutWorkspaces || e.Note == "" {
		t.Fatalf("withdrawn choice %+v", e)
	}
	// Other changes still save while the withdrawn choice is kept.
	if v, err = o.SaveUIPref(ctx, T, "farhan.khan", UIPrefPatch{Tiles: &[]string{"today", "needs"}}); err != nil || v.Mine.Layout != LayoutGrid || len(v.Mine.Tiles) != 2 {
		t.Fatalf("tiles with a withdrawn layout %+v %v", v.Mine, err)
	}
	// An empty layout follows the firm again.
	if v, err = o.SaveUIPref(ctx, T, "farhan.khan", UIPrefPatch{Layout: sp("")}); err != nil || v.Mine.Layout != "" || v.Effective.Note != "" {
		t.Fatalf("back to the firm's %+v %v", v, err)
	}

	// Bad policies and choices are refused.
	for name, p := range map[string]UIPolicy{
		"unknown layout":        {Default: "sideways"},
		"unknown role":          {Roles: map[string]string{"Chief": LayoutInbox}},
		"branch on the runbook": {Roles: map[string]string{RoleBranch: LayoutRunbook}},
		"client opens":          {ClientOpens: "window"},
		"look":                  {Look: "neon"},
		"density":               {Density: "tiny"},
		"theme":                 {Theme: "sepia"},
	} {
		if _, err := o.SaveUIPolicy(ctx, T, "admin", p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, ch := range map[string]UIPrefPatch{
		"density":    {Density: sp("tiny")},
		"tile":       {Tiles: &[]string{"<b>"}},
		"tile twice": {Tiles: &[]string{"today", "today"}},
		"view table": {Views: &[]GridView{{ID: "v1", Name: "Mine", Source: "trades"}}},
		"view name":  {Views: &[]GridView{{ID: "v1", Source: "clients"}}},
		"view twice": {Views: &[]GridView{{ID: "v1", Name: "A", Source: "clients"}, {ID: "v1", Name: "B", Source: "rms"}}},
		"view col":   {Views: &[]GridView{{ID: "v1", Name: "A", Source: "clients", Sort: "-x;drop"}}},
	} {
		if _, err := o.SaveUIPref(ctx, T, "priya.nair", ch); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A good saved view.
	views := []GridView{{ID: "hni", Name: "HNI in breach", Source: "clients", Filters: map[string]string{"tags": "HNI", "risk_status": "Breach"}, Sort: "-funds", Columns: []string{"ucc", "name", "funds"}}}
	if v, err = o.SaveUIPref(ctx, T, "priya.nair", UIPrefPatch{Views: &views}); err != nil || len(v.Mine.Views) != 1 || v.Mine.Density != DensityCompact {
		t.Fatalf("saved view %+v %v", v.Mine, err)
	}

	// An Admin may pick any layout, even one the firm doesn't offer staff.
	if v, err = o.SaveUIPref(ctx, T, "admin", UIPrefPatch{Layout: sp(LayoutAutopilot)}); err != nil || v.Effective.Layout != LayoutAutopilot {
		t.Fatalf("admin's own %+v %v", v.Effective, err)
	}

	// Turning staff choice off: a saved choice is no longer used.
	if _, err := o.SaveUIPref(ctx, T, "farhan.khan", UIPrefPatch{Layout: sp(LayoutInbox)}); err != nil {
		t.Fatal(err)
	}
	pol.StaffChoose = false
	if _, err := o.SaveUIPolicy(ctx, T, "admin", pol); err != nil {
		t.Fatal(err)
	}
	if e := o.EffectiveUI(ctx, T, "farhan.khan"); e.Layout != LayoutWorkspaces || e.CanChoose || e.Note == "" {
		t.Fatalf("staff choice off %+v", e)
	}
}
