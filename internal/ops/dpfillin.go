package ops

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/migrate/pkg"
)

// What the old system's backup did not say about the DP's charges, which
// the firm fills in after the migration (pkg.FillIn): Ledgerline asks for
// it (a to-do for an Ops checker, a note on the DP desk) and holds DP
// billing until it is done.

const cDPFillIn = "dpfillin" // key: the item (pkg.FillDPFirmTariff, pkg.FillDPPledgeCharges)

// DPFillIn is one thing to fill in.
type DPFillIn struct {
	Item  string `json:"item"`
	Title string `json:"title"`
	Why   string `json:"why"`
	// Plans are the tariff plans whose pledge charges are still to be
	// filled in (pkg.FillDPPledgeCharges).
	Plans     []string `json:"plans,omitempty"`
	Migration string   `json:"migration"`
	Since     string   `json:"since"`
	Done      string   `json:"done,omitempty"` // the date it was filled in
	DoneBy    string   `json:"done_by,omitempty"`
}

var dpFillInTitles = map[string]string{
	pkg.FillDPFirmTariff:    "Fill in the firm's DP tariff",
	pkg.FillDPPledgeCharges: "Fill in the DP's pledge charges",
}

// dpAskFillIns turns a loaded migration's fill-ins for its firm into
// to-dos, if the load brought the DP's accounts.
func (o *Ops) dpAskFillIns(ctx context.Context, tenant, actor string, m *Migration) {
	dp := false
	for _, st := range m.Steps {
		dp = dp || (st.Source == "DP-ACCOUNTS" && st.Status == "Done")
	}
	if !dp {
		return
	}
	plans, _ := o.DPPlans(ctx, tenant)
	for _, id := range m.Packages {
		p, _, err := docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id)
		if err != nil || p.Manifest == nil {
			continue
		}
		for _, f := range p.Manifest.FillIn {
			title, ok := dpFillInTitles[f.Item]
			if !ok || (f.Firm != "" && f.Firm != m.Firm) {
				continue
			}
			if old, _, err := docs.Get[DPFillIn](ctx, o.Docs, tenant, cDPFillIn, f.Item); err == nil && old.Migration == m.ID {
				continue // asked already (a resumed load)
			}
			x := DPFillIn{Item: f.Item, Title: title, Why: f.Why, Migration: m.ID, Since: o.now().Format("2006-01-02")}
			if f.Item == pkg.FillDPPledgeCharges {
				x.Plans = f.Plans
				if len(x.Plans) == 0 {
					for _, pl := range plans {
						x.Plans = append(x.Plans, pl.Code)
					}
				}
			}
			if err := docs.Upsert(ctx, o.Docs, tenant, cDPFillIn, f.Item, x, actor); err != nil {
				continue
			}
			detail := f.Why + " Fill it in on the DP desk (Billing & tariff); DP billing waits until you do."
			if len(x.Plans) > 0 {
				detail += " Plans: " + strings.Join(x.Plans, ", ") + "."
			}
			o.raiseWork(ctx, tenant, "dpfillin", f.Item, title+" (not in the old system's backup)", detail, "", RoleChecker, "dp", "")
		}
	}
}

// DPFillIns lists what is still to be filled in.
func (o *Ops) DPFillIns(ctx context.Context, tenant string) []DPFillIn {
	all, _ := docs.List[DPFillIn](ctx, o.Docs, tenant, cDPFillIn, "")
	xs := []DPFillIn{}
	for _, x := range all {
		if x.Done == "" {
			xs = append(xs, x)
		}
	}
	return xs
}

// dpFilledIn marks an item done (plan: one plan's pledge charges; the item
// is done when every plan's are).
func (o *Ops) dpFilledIn(ctx context.Context, tenant, actor, item, plan string) {
	x, _, err := docs.Get[DPFillIn](ctx, o.Docs, tenant, cDPFillIn, item)
	if err != nil || x.Done != "" {
		return
	}
	if plan != "" {
		var left []string
		for _, p := range x.Plans {
			if p != plan {
				left = append(left, p)
			}
		}
		if len(left) > 0 {
			x.Plans = left
			docs.Upsert(ctx, o.Docs, tenant, cDPFillIn, item, x, actor)
			return
		}
	}
	x.Plans, x.Done, x.DoneBy = nil, o.now().Format("2006-01-02"), actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cDPFillIn, item, x, actor); err != nil {
		return
	}
	o.closeWork(ctx, tenant, actor, "dpfillin", item, "filled in")
	o.log(ctx, tenant, actor, "dp.filled_in", item, nil)
}

// dpBillingHeld says why DP billing must wait ("": it need not).
func (o *Ops) dpBillingHeld(ctx context.Context, tenant string) error {
	var what []string
	for _, x := range o.DPFillIns(ctx, tenant) {
		w := strings.ToLower(x.Title[:1]) + x.Title[1:]
		if len(x.Plans) > 0 {
			w += " (plans " + strings.Join(x.Plans, ", ") + ")"
		}
		what = append(what, w)
	}
	if len(what) == 0 {
		return nil
	}
	return fmt.Errorf("%w: the old system's backup did not say all the DP's charges; first %s on the DP desk (Billing & tariff)", ErrState, strings.Join(what, "; "))
}
