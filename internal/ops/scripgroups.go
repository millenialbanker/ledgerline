package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- scrip groups: named lists of scrips a card can price separately ----
//
// A scrip group is a list the firm keeps (ETFs, a list of illiquid scrips,
// SharePro's SL… and SG… groups), by symbol or ISIN, each member from a date
// and optionally to a date. A rate line for "group:ETF" applies to orders in
// any member; a line for the scrip itself still wins over it. A scrip in two
// groups is priced by the group that comes first by name.

const cScripGroup = "scripgroup"

// GroupMember is a scrip in a group, from a date (and to a date, inclusive).
type GroupMember struct {
	Scrip string `json:"scrip"` // symbol or ISIN; for derivatives, the underlying
	From  string `json:"from"`
	To    string `json:"to,omitempty"`
}

// ScripGroup is a named list of scrips.
type ScripGroup struct {
	Name    string        `json:"name"`
	Note    string        `json:"note,omitempty"`
	Members []GroupMember `json:"members"`
	By      string        `json:"by,omitempty"`
	At      string        `json:"at,omitempty"`
}

// ScripGroups lists the firm's scrip groups.
func (o *Ops) ScripGroups(ctx context.Context, tenant string) ([]ScripGroup, error) {
	gs, err := docs.List[ScripGroup](ctx, o.Docs, tenant, cScripGroup, "")
	sort.Slice(gs, func(i, j int) bool { return gs[i].Name < gs[j].Name })
	return gs, err
}

// SaveScripGroup creates or replaces a scrip group. No members deletes it
// from use (a line for it then matches nothing).
func (o *Ops) SaveScripGroup(ctx context.Context, tenant, actor string, g ScripGroup) (ScripGroup, error) {
	if err := o.require(ctx, tenant, actor, "Changing scrip groups", RoleChecker); err != nil {
		return g, err
	}
	return o.saveScripGroup(ctx, tenant, actor, g)
}

func cleanScripGroup(g ScripGroup) (ScripGroup, error) {
	g.Name = strings.ToUpper(strings.TrimSpace(g.Name))
	if !charges.ValidGroupName(g.Name) {
		return g, invalid("scrip group %q: use up to 30 letters, digits, - or _", g.Name)
	}
	g.Note = strings.TrimSpace(g.Note)
	seen := map[string]bool{}
	ms := []GroupMember{}
	for _, m := range g.Members {
		m.Scrip, m.From, m.To = strings.ToUpper(strings.TrimSpace(m.Scrip)), strings.TrimSpace(m.From), strings.TrimSpace(m.To)
		if m.Scrip == "" {
			continue
		}
		if !charges.ValidScrip(m.Scrip) {
			return g, invalid("%s: %q is not a symbol or ISIN", g.Name, m.Scrip)
		}
		if m.From == "" {
			m.From = "2000-01-01"
		}
		if !validDate(m.From) || (m.To != "" && (!validDate(m.To) || m.To < m.From)) {
			return g, invalid("%s: %s needs a from date (YYYY-MM-DD) and a to date on or after it, or none", g.Name, m.Scrip)
		}
		k := m.Scrip + "|" + m.From
		if seen[k] {
			return g, invalid("%s: %s from %s is given twice", g.Name, m.Scrip, m.From)
		}
		seen[k] = true
		ms = append(ms, m)
	}
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].Scrip != ms[j].Scrip {
			return ms[i].Scrip < ms[j].Scrip
		}
		return ms[i].From < ms[j].From
	})
	g.Members = ms
	return g, nil
}

func (o *Ops) saveScripGroup(ctx context.Context, tenant, actor string, g ScripGroup) (ScripGroup, error) {
	g, err := cleanScripGroup(g)
	if err != nil {
		return g, err
	}
	g.By, g.At = actor, o.now().Format("2006-01-02 15:04")
	if err := docs.Upsert(ctx, o.Docs, tenant, cScripGroup, g.Name, g, actor); err != nil {
		return g, err
	}
	o.log(ctx, tenant, actor, "brokerage.scrip_group", g.Name, map[string]any{"members": len(g.Members)})
	return g, nil
}

// groupNames is the set of scrip group names.
func (o *Ops) groupNames(ctx context.Context, tenant string) (map[string]bool, error) {
	gs, err := o.ScripGroups(ctx, tenant)
	out := map[string]bool{}
	for _, g := range gs {
		out[g.Name] = true
	}
	return out, err
}

// lineGroup is the scrip group a rate line is for, if any.
func lineGroup(key string) string {
	k, err := charges.ParseRateKey(key)
	if err != nil || !strings.HasPrefix(k.Target, charges.TargetGroup) {
		return ""
	}
	return strings.TrimPrefix(k.Target, charges.TargetGroup)
}

// checkGroups refuses lines for scrip groups the firm has not set up.
func (o *Ops) checkGroups(ctx context.Context, tenant string, rules map[string]charges.BrokerageRule) error {
	known, err := o.groupNames(ctx, tenant)
	if err != nil {
		return err
	}
	for k := range rules {
		if g := lineGroup(k); g != "" && !known[g] {
			return invalid("no scrip group %s: set it up under Scrip groups first", g)
		}
	}
	return nil
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// groupsOn indexes the groups by member scrip for a date: scrip -> group
// names in order (by name).
func groupsOn(gs []ScripGroup, date string) map[string][]string {
	out := map[string][]string{}
	for _, g := range gs {
		in := map[string]bool{}
		for _, m := range g.Members {
			if m.From <= date && (m.To == "" || date <= m.To) && !in[m.Scrip] {
				in[m.Scrip] = true
				out[m.Scrip] = append(out[m.Scrip], g.Name)
			}
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// targetsFor is what a trade's scrip looks like to rate lines: for the cash
// market its symbol and ISIN and the groups either is in; for derivatives
// its underlying and the groups the underlying is in.
func (bb *brokerageBook) targetsFor(segment, symbol string) charges.Targets {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	var t charges.Targets
	ids := []string{symbol}
	if segment == "CM" {
		t.Scrips = []string{symbol}
		if i, ok := bb.look.Get("NSE", "CM", symbol+"|EQ||"); ok && i.ISIN != "" && i.ISIN != symbol {
			t.Scrips = append(t.Scrips, i.ISIN)
			ids = append(ids, i.ISIN)
		}
	} else {
		t.Underlying = symbol
	}
	seen := map[string]bool{}
	for _, id := range ids {
		for _, g := range bb.groups[id] {
			if !seen[g] {
				seen[g] = true
				t.Groups = append(t.Groups, g)
			}
		}
	}
	sort.Strings(t.Groups)
	return t
}

func (bb *brokerageBook) tradeTargets(t ingest.TradeEvent) charges.Targets {
	return bb.targetsFor(t.Segment, t.Symbol)
}

// prepareScripGroups loads SCRIP-GROUPS: Group, Scrip, optional From, To and
// Note. The groups in the file are replaced by the file's rows.
func (o *Ops) prepareScripGroups(ctx context.Context, tenant, actor string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	bad := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	if err := o.require(ctx, tenant, actor, "Changing scrip groups", RoleChecker); err != nil {
		bad(1, "", "%v", err)
		return nil, errs
	}
	gs := map[string]*ScripGroup{}
	for _, r := range rows {
		name := strings.ToUpper(strings.TrimSpace(r.get("Group")))
		if !charges.ValidGroupName(name) {
			bad(r.line, "Group", "%q: use up to 30 letters, digits, - or _", r.get("Group"))
			continue
		}
		m := GroupMember{Scrip: strings.ToUpper(strings.TrimSpace(r.get("Scrip"))), From: strings.TrimSpace(r.get("From")), To: strings.TrimSpace(r.get("To"))}
		if !charges.ValidScrip(m.Scrip) {
			bad(r.line, "Scrip", "%q is not a symbol or ISIN", r.get("Scrip"))
			continue
		}
		if m.From != "" && !validDate(m.From) {
			bad(r.line, "From", "must be YYYY-MM-DD, got %q", m.From)
			continue
		}
		if m.To != "" && (!validDate(m.To) || (m.From != "" && m.To < m.From)) {
			bad(r.line, "To", "must be YYYY-MM-DD on or after From, got %q", m.To)
			continue
		}
		g := gs[name]
		if g == nil {
			g = &ScripGroup{Name: name}
			gs[name] = g
		}
		if n := strings.TrimSpace(r.get("Note")); n != "" {
			g.Note = n
		}
		g.Members = append(g.Members, m)
	}
	for _, name := range sortedKeys(gs) {
		if _, err := cleanScripGroup(*gs[name]); err != nil {
			bad(1, "Group", "%v", err)
		}
	}
	return func() (int, int, error) {
		n, same := 0, 0
		for _, name := range sortedKeys(gs) {
			g, _ := cleanScripGroup(*gs[name])
			if old, _, err := docs.Get[ScripGroup](ctx, o.Docs, tenant, cScripGroup, name); err == nil && old.Note == g.Note && sameJSON(old.Members, g.Members) {
				same++
				continue
			}
			if _, err := o.saveScripGroup(ctx, tenant, actor, g); err != nil {
				return n, same, err
			}
			n++
		}
		return n, same, nil
	}, errs
}
