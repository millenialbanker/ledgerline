package ops

import (
	"context"
	"strings"
)

// ---- clients BSE does not permit to trade, for the screens ----
//
// BSE's "UCCs not permitted to trade" reports (bseextranet.go) give, per
// segment, the clients BSE will not take orders for and why. The Exchange
// UCC screen and the client drawer show them from the latest report of
// each segment, with the reason and the fix in plain words.

// BSEBlockedClient is one client on BSE's latest not-permitted lists.
type BSEBlockedClient struct {
	UCC      string   `json:"ucc"`
	Name     string   `json:"name"`
	Blocked  bool     `json:"blocked"`
	Segments []string `json:"segments"` // CM, FO
	Reasons  []string `json:"reasons"`  // plain words, each with the fix
	BSE      []string `json:"bse"`      // BSE's own text
	AsOf     string   `json:"as_of"`    // date of the latest list it is on
	Files    []string `json:"files"`
}

// BSEBlockedView is GET /v1/bse-ucc-blocked.
type BSEBlockedView struct {
	Lists   map[string]string  `json:"lists"` // segment -> date of the latest list
	Clients []BSEBlockedClient `json:"clients"`
}

func (o *Ops) canSeeBSEBlocked(ctx context.Context, tenant, actor string) error {
	return o.require(ctx, tenant, actor, "Viewing clients BSE does not permit to trade", RoleMaker, RoleChecker, RoleRisk, RoleCompliance)
}

func (o *Ops) bseBlocked(ctx context.Context, tenant string) (map[string]*BSEBlockedClient, map[string]string) {
	latest := o.bseLatestNotPermitted(ctx, tenant)
	out, lists := map[string]*BSEBlockedClient{}, map[string]string{}
	for _, seg := range []string{"CM", "FO"} {
		l, ok := latest[seg]
		if !ok {
			continue
		}
		lists[seg] = l.Date
		for _, u := range sortedKeys(l.Clients) {
			x := out[u]
			if x == nil {
				x = &BSEBlockedClient{UCC: u, Blocked: true, Segments: []string{}, Reasons: []string{}, BSE: []string{}, Files: []string{}}
				out[u] = x
			}
			x.Segments = append(x.Segments, seg)
			x.Reasons = append(x.Reasons, l.Clients[u]...)
			x.BSE = append(x.BSE, l.BSE[u]...)
			x.Files = append(x.Files, l.File)
			if l.Date > x.AsOf {
				x.AsOf = l.Date
			}
		}
	}
	return out, lists
}

// BSEBlockedList lists every client on BSE's latest not-permitted lists.
func (o *Ops) BSEBlockedList(ctx context.Context, tenant, actor string) (BSEBlockedView, error) {
	v := BSEBlockedView{Lists: map[string]string{}, Clients: []BSEBlockedClient{}}
	if err := o.canSeeBSEBlocked(ctx, tenant, actor); err != nil {
		return v, err
	}
	bl, lists := o.bseBlocked(ctx, tenant)
	cs, _ := o.ClientMap(ctx, tenant)
	v.Lists = lists
	for _, u := range sortedKeys(bl) {
		x := *bl[u]
		x.Name = cs[u].Name
		v.Clients = append(v.Clients, x)
	}
	return v, nil
}

// BSEBlockedFor is one client's entry (Blocked false when BSE permits it).
func (o *Ops) BSEBlockedFor(ctx context.Context, tenant, actor, ucc string) (BSEBlockedClient, error) {
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	x := BSEBlockedClient{UCC: ucc, Segments: []string{}, Reasons: []string{}, BSE: []string{}, Files: []string{}}
	if err := o.canSeeBSEBlocked(ctx, tenant, actor); err != nil {
		return x, err
	}
	bl, _ := o.bseBlocked(ctx, tenant)
	if b, ok := bl[ucc]; ok {
		x = *b
	}
	if c, err := o.Client(ctx, tenant, ucc); err == nil {
		x.Name = c.Name
	}
	return x, nil
}
