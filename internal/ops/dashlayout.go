package ops

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"ledgerline/internal/docs"
)

// ---- dashboard layouts ----
//
// Each user arranges their own dashboard: which widgets, in what order and
// at what size. An Admin can save a firm default that users start from.
// The widget catalogue lives in the console; the server only checks that a
// layout is well formed.

const cDashLayout = "dashlayout"

// firmLayoutID is the firm default's key; user IDs can't contain "*".
const firmLayoutID = "*firm"

// DashWidget is one widget's place on a dashboard.
type DashWidget struct {
	ID string `json:"id"`
	W  int    `json:"w"` // columns of 12
	H  int    `json:"h"` // rows
}

// DashLayout is a saved arrangement.
type DashLayout struct {
	Widgets []DashWidget `json:"widgets"`
	By      string       `json:"by,omitempty"`
	Saved   string       `json:"saved,omitempty"`
}

// DashLayouts is what a user's console needs: their own layout and the
// firm default. A nil Widgets means none saved.
type DashLayouts struct {
	Mine DashLayout `json:"mine"`
	Firm DashLayout `json:"firm"`
}

const (
	DashMaxWidgets = 40
	DashMaxW       = 12
	DashMaxH       = 12
)

var reWidgetID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// CheckDashWidgets validates a layout.
func CheckDashWidgets(ws []DashWidget) error {
	if len(ws) > DashMaxWidgets {
		return fmt.Errorf("%w: a dashboard can hold at most %d widgets", ErrInvalid, DashMaxWidgets)
	}
	seen := map[string]bool{}
	for _, w := range ws {
		if !reWidgetID.MatchString(w.ID) {
			return fmt.Errorf("%w: unknown widget %q", ErrInvalid, w.ID)
		}
		if seen[w.ID] {
			return fmt.Errorf("%w: widget %q appears twice", ErrInvalid, w.ID)
		}
		seen[w.ID] = true
		if w.W < 1 || w.W > DashMaxW || w.H < 1 || w.H > DashMaxH {
			return fmt.Errorf("%w: widget %q must be 1–%d columns wide and 1–%d rows tall", ErrInvalid, w.ID, DashMaxW, DashMaxH)
		}
	}
	return nil
}

// DashboardLayouts returns the user's layout and the firm default.
func (o *Ops) DashboardLayouts(ctx context.Context, tenant, actor string) (DashLayouts, error) {
	if _, err := o.RoleOf(ctx, tenant, actor); err != nil {
		return DashLayouts{}, err
	}
	var v DashLayouts
	v.Mine, _, _ = docs.GetOr(ctx, o.Docs, tenant, cDashLayout, actor, DashLayout{})
	v.Firm, _, _ = docs.GetOr(ctx, o.Docs, tenant, cDashLayout, firmLayoutID, DashLayout{})
	return v, nil
}

// SaveDashboardLayout stores the user's own layout. Nil widgets clears it,
// so the user sees the firm default again.
func (o *Ops) SaveDashboardLayout(ctx context.Context, tenant, actor string, ws []DashWidget) (DashLayout, error) {
	if _, err := o.RoleOf(ctx, tenant, actor); err != nil {
		return DashLayout{}, err
	}
	return o.saveLayout(ctx, tenant, actor, actor, ws)
}

// SaveFirmDashboard stores the layout new users start from.
func (o *Ops) SaveFirmDashboard(ctx context.Context, tenant, actor string, ws []DashWidget) (DashLayout, error) {
	if err := o.require(ctx, tenant, actor, "Setting the firm's default dashboard", RoleAdmin); err != nil {
		return DashLayout{}, err
	}
	l, err := o.saveLayout(ctx, tenant, firmLayoutID, actor, ws)
	if err == nil {
		o.log(ctx, tenant, actor, "dashboard.firm_default", "", map[string]any{"widgets": len(ws)})
	}
	return l, err
}

func (o *Ops) saveLayout(ctx context.Context, tenant, id, actor string, ws []DashWidget) (DashLayout, error) {
	if err := CheckDashWidgets(ws); err != nil {
		return DashLayout{}, err
	}
	l := DashLayout{Widgets: ws, By: actor, Saved: time.Now().Format(time.RFC3339)}
	if ws == nil {
		l = DashLayout{}
	}
	return l, docs.Upsert(ctx, o.Docs, tenant, cDashLayout, id, l, actor)
}
