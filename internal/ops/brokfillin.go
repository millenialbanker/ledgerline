package ops

import (
	"context"

	"ledgerline/internal/docs"
	"ledgerline/internal/migrate/pkg"
)

// What the old system's backup did not say about brokerage, which the firm
// sets after the migration (pkg.FillIn): the firm default card, for clients
// the old system had on no card and with no rates of their own; and which
// terminals (CTCL IDs) are which trading channel, when the cards price
// channels apart. Each becomes a to-do for an Ops checker, closed when the
// firm default is assigned or the terminal list is saved. Billing does not
// wait: until then those clients pay the charge schedule and every trade
// takes the default channel, which the to-do says.

var brokFillInTitles = map[string]string{
	pkg.FillBrokerageDefault: "Set the firm default brokerage card",
	pkg.FillTerminalChannels: "Fill in which terminals are online, mobile and dealer",
}

var brokFillInNext = map[string]string{
	pkg.FillBrokerageDefault: " Set it on Brokerage plans → Firm default and groups (Change); until then those clients pay the charge schedule.",
	pkg.FillTerminalChannels: " Add the terminal (CTCL) IDs on Brokerage plans → Trading channels (Change), or load TERMINAL-CHANNELS on Files; until then every trade is billed at the default channel's rates.",
}

// brokAskFillIns turns a loaded migration's brokerage fill-ins for its
// firm into to-dos, unless the firm has already done them.
func (o *Ops) brokAskFillIns(ctx context.Context, tenant string, m *Migration) {
	for _, id := range m.Packages {
		p, _, err := docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id)
		if err != nil || p.Manifest == nil {
			continue
		}
		for _, f := range p.Manifest.FillIn {
			title, ok := brokFillInTitles[f.Item]
			if !ok || (f.Firm != "" && f.Firm != m.Firm) || o.brokFilledIn(ctx, tenant, f.Item) {
				continue
			}
			o.raiseWork(ctx, tenant, "brokfillin", f.Item, title+" (not in the old system's backup)", f.Why+brokFillInNext[f.Item], "", RoleChecker, "brokerage", "")
		}
	}
}

// brokFilledIn says whether the firm has set what the item asks for.
func (o *Ops) brokFilledIn(ctx context.Context, tenant, item string) bool {
	switch item {
	case pkg.FillBrokerageDefault:
		as, _ := o.Assignments(ctx, tenant)
		for _, a := range as {
			if a.Target == "default" && a.Scope == "" && a.PlanID != "" {
				return true
			}
		}
	case pkg.FillTerminalChannels:
		s, _ := o.BrokerageSetup(ctx, tenant)
		return len(s.Terminals) > 0
	}
	return false
}

// brokFillInDone closes the to-do once the firm has done it.
func (o *Ops) brokFillInDone(ctx context.Context, tenant, actor, item string) {
	if o.brokFilledIn(ctx, tenant, item) {
		o.closeWork(ctx, tenant, actor, "brokfillin", item, "Done")
	}
}
