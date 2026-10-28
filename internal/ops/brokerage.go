package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- brokerage plans ----

const (
	cBrokPlan     = "brokplan"
	cBrokAssign   = "brokassign"
	cBrokOverride = "brokoverride" // ucc -> ClientOverride
	cBrokSetup    = "broksetup"    // "setup" -> BrokerageSetup
)

// PlanVersion is a plan's rates from a date. Rules are rate lines keyed
// "PRODUCT" or "PRODUCT|EXCHANGE|CHANNEL|CASE" (see charges.RateKey).
// Products left out use the firm's charge schedule.
type PlanVersion struct {
	From  string                           `json:"from"`
	Rules map[string]charges.BrokerageRule `json:"rules"`
	By    string                           `json:"by"`
	At    string                           `json:"at"`
}

// Plan is a named brokerage plan with dated versions.
type Plan struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Note     string        `json:"note,omitempty"`
	Versions []PlanVersion `json:"versions"`
	Archived bool          `json:"archived,omitempty"`
}

// At returns the version in force on date.
func (p Plan) At(date string) (PlanVersion, bool) {
	var best PlanVersion
	ok := false
	for _, v := range p.Versions {
		if v.From <= date && (!ok || v.From > best.From) {
			best, ok = v, true
		}
	}
	return best, ok
}

// PlanAssignment puts a client, a group or the whole firm on a plan from a
// date. Target is "default", "tag:<GROUP>" or "ucc:<UCC>". Scope narrows it
// to a segment ("FO") or a segment on one exchange ("CM-BSE"); empty means
// every segment. An empty plan removes an earlier assignment of the same
// target and scope from that date. Exclusive (scheme changes such as a
// prepaid plan) sets the client's plan for every segment, setting aside
// their segment cards while it lasts.
type PlanAssignment struct {
	ID        string `json:"id"`
	Target    string `json:"target"`
	Scope     string `json:"scope,omitempty"`
	PlanID    string `json:"plan"`
	From      string `json:"from"`
	Exclusive bool   `json:"exclusive,omitempty"`
	By        string `json:"by"`
	At        string `json:"at"`
}

// Segments are the scopes a card can be assigned for.
var Segments = []string{"CM", "FO", "CD", "COM"}

// segmentOf is a product's segment: CM-DEL -> CM.
func segmentOf(b charges.Bucket) string { s, _, _ := strings.Cut(string(b), "-"); return s }

// normScope checks and normalises an assignment scope.
func normScope(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" || s == "ALL" || s == "*" {
		return "", nil
	}
	seg, exch, _ := strings.Cut(s, "-")
	ok := false
	for _, x := range Segments {
		ok = ok || x == seg
	}
	if !ok {
		return "", invalid("segment %q: use CM, FO, CD or COM, optionally with an exchange such as CM-BSE", s)
	}
	if exch != "" && !regexp.MustCompile(`^[A-Z]{2,8}$`).MatchString(exch) {
		return "", invalid("unknown exchange in %q", s)
	}
	return s, nil
}

var validPlanID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,40}$`)

func (o *Ops) Plans(ctx context.Context, tenant string) ([]Plan, error) {
	return docs.List[Plan](ctx, o.Docs, tenant, cBrokPlan, "")
}

func (o *Ops) Assignments(ctx context.Context, tenant string) ([]PlanAssignment, error) {
	as, err := docs.List[PlanAssignment](ctx, o.Docs, tenant, cBrokAssign, "")
	sort.SliceStable(as, func(i, j int) bool {
		if as[i].Target != as[j].Target {
			return as[i].Target < as[j].Target
		}
		return as[i].From < as[j].From || (as[i].From == as[j].From && as[i].ID < as[j].ID)
	})
	return as, err
}

// PlanInput is a create or edit request: the rates apply from From.
type PlanInput struct {
	ID    string                           `json:"id"`
	Name  string                           `json:"name"`
	Note  string                           `json:"note"`
	From  string                           `json:"from"`
	Rules map[string]charges.BrokerageRule `json:"rules"`
}

// SavePlan creates a plan or adds a version from a date (replacing a
// version that starts on the same date). Bills already posted keep their
// brokerage unless the day is rebilled.
func (o *Ops) SavePlan(ctx context.Context, tenant, actor string, in PlanInput) (Plan, error) {
	if err := o.require(ctx, tenant, actor, "Changing brokerage plans", RoleChecker); err != nil {
		return Plan{}, err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.ID == "" {
		in.ID = strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(in.Name), "-"), "-")
	}
	if !validPlanID.MatchString(in.ID) {
		return Plan{}, invalid("plan ID must be 2 to 41 lower-case letters, digits or dashes")
	}
	if in.Name == "" {
		return Plan{}, invalid("give the plan a name")
	}
	if !validDate(in.From) {
		return Plan{}, invalid("effective from must be a date")
	}
	rules, err := cleanRules(in.Rules)
	if err != nil {
		return Plan{}, err
	}
	p, _, err := o.savePlanVersion(ctx, tenant, actor, in, rules)
	return p, err
}

// savePlanVersion stores a checked version; changed is false when the plan
// already had exactly this version (a file loaded twice).
func (o *Ops) savePlanVersion(ctx context.Context, tenant, actor string, in PlanInput, rules map[string]charges.BrokerageRule) (Plan, bool, error) {
	v := PlanVersion{From: in.From, Rules: rules, By: actor, At: o.now().Format("2006-01-02 15:04")}
	changed := true
	p, err := upd(ctx, o.Docs, tenant, cBrokPlan, in.ID, actor, &Plan{ID: in.ID}, func(p *Plan) error {
		for _, x := range p.Versions {
			if x.From == in.From && sameRules(x.Rules, rules) && p.Name == in.Name && !p.Archived {
				changed = false
				return nil
			}
		}
		p.Name, p.Note, p.Archived = in.Name, strings.TrimSpace(in.Note), false
		kept := p.Versions[:0]
		for _, x := range p.Versions {
			if x.From != in.From {
				kept = append(kept, x)
			}
		}
		p.Versions = append(kept, v)
		sort.Slice(p.Versions, func(i, j int) bool { return p.Versions[i].From < p.Versions[j].From })
		return nil
	})
	if err != nil || !changed {
		return p, changed, err
	}
	o.log(ctx, tenant, actor, "brokerage.plan_saved", in.ID, map[string]any{"name": in.Name, "from": in.From, "rules": len(rules)})
	return p, true, nil
}

// sameRules compares rate lines by value.
func sameRules(a, b map[string]charges.BrokerageRule) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// cleanRules checks rate lines and returns them under canonical keys.
// Lines with no mode (or "inherit") are dropped.
func cleanRules(in map[string]charges.BrokerageRule) (map[string]charges.BrokerageRule, error) {
	rules := map[string]charges.BrokerageRule{}
	for k, r := range in {
		rk, err := charges.ParseRateKey(k)
		if err != nil {
			return nil, invalid("%v", err)
		}
		if r.Mode == "" || r.Mode == "inherit" {
			continue
		}
		cash := segmentOf(rk.Bucket) == "CM"
		if cash && (r.Mode == charges.PlanPerLot || r.MinPerLot.Sign() > 0) {
			return nil, invalid("%s: per-lot rates apply to derivatives only", rk.Describe())
		}
		if err := r.Validate(); err != nil {
			return nil, invalid("%s: %v", rk.Describe(), err)
		}
		if _, dup := rules[rk.String()]; dup {
			return nil, invalid("%s is given twice", rk.Describe())
		}
		rules[rk.String()] = r
	}
	return rules, nil
}

// Assign puts a target on a plan from a date for every segment ("" plan
// removes the earlier assignment for that target from the date).
func (o *Ops) Assign(ctx context.Context, tenant, actor, target, planID, from string) (PlanAssignment, error) {
	return o.AssignScoped(ctx, tenant, actor, target, "", planID, from)
}

// AssignScoped puts a target on a plan from a date for one segment, or one
// segment on one exchange (scope "FO", "CM-BSE"; "" is every segment).
func (o *Ops) AssignScoped(ctx context.Context, tenant, actor, target, scope, planID, from string) (PlanAssignment, error) {
	if err := o.require(ctx, tenant, actor, "Assigning brokerage plans", RoleChecker); err != nil {
		return PlanAssignment{}, err
	}
	return o.assignAs(ctx, tenant, actor, PlanAssignment{Target: target, Scope: scope, PlanID: planID, From: from})
}

// assign records a scheme change without a role check (a prepaid scheme
// starting or ending): the plan applies to every segment, setting aside the
// client's segment cards while it lasts.
func (o *Ops) assign(ctx context.Context, tenant, actor, target, planID, from string) (PlanAssignment, error) {
	return o.assignAs(ctx, tenant, actor, PlanAssignment{Target: target, PlanID: planID, From: from, Exclusive: strings.HasPrefix(target, "ucc:") && planID != ""})
}

func (o *Ops) assignAs(ctx context.Context, tenant, actor string, in PlanAssignment) (PlanAssignment, error) {
	target, planID, from := in.Target, in.PlanID, in.From
	if !validDate(from) {
		return PlanAssignment{}, invalid("effective from must be a date")
	}
	scope, err := normScope(in.Scope)
	if err != nil {
		return PlanAssignment{}, err
	}
	switch {
	case target == "default":
	case strings.HasPrefix(target, "tag:"):
		t := NormaliseTag(strings.TrimPrefix(target, "tag:"))
		if !validTag.MatchString(t) {
			return PlanAssignment{}, invalid("unknown group")
		}
		target = "tag:" + t
	case strings.HasPrefix(target, "ucc:"):
		u := strings.ToUpper(strings.TrimPrefix(target, "ucc:"))
		if _, err := o.Client(ctx, tenant, u); err != nil {
			return PlanAssignment{}, err
		}
		target = "ucc:" + u
	default:
		return PlanAssignment{}, invalid("assign to the firm default, a group or a client")
	}
	if planID != "" {
		if _, _, err := docs.Get[Plan](ctx, o.Docs, tenant, cBrokPlan, planID); err != nil {
			return PlanAssignment{}, fmt.Errorf("%w: plan %s", ErrNotFound, planID)
		}
	} else if target == "default" && scope == "" {
		return PlanAssignment{}, invalid("choose the firm's default plan")
	}
	seq, err := o.next(ctx, tenant, "brokassign", actor)
	if err != nil {
		return PlanAssignment{}, err
	}
	a := PlanAssignment{ID: fmt.Sprintf("PA-%06d", seq), Target: target, Scope: scope, PlanID: planID, From: from, Exclusive: in.Exclusive && scope == "", By: actor, At: o.now().Format("2006-01-02 15:04")}
	if _, err := docs.Put(ctx, o.Docs, tenant, cBrokAssign, a.ID, a, 0, actor); err != nil {
		return a, err
	}
	o.log(ctx, tenant, actor, "brokerage.assigned", target, map[string]any{"plan": planID, "from": from, "scope": scope})
	return a, nil
}

// EffectivePlan says which plan applies to a client and why.
type EffectivePlan struct {
	UCC    string   `json:"ucc"`
	Name   string   `json:"name"`
	Tags   []string `json:"tags"`
	PlanID string   `json:"plan"`
	Plan   string   `json:"plan_name"`
	Source string   `json:"source"` // Client, Group <TAG>, Firm default, Charge schedule
	From   string   `json:"from,omitempty"`
	// Segments are cards that apply to one segment or exchange only.
	Segments []ScopedPlan `json:"segments,omitempty"`
	Override bool         `json:"override,omitempty"` // the client has their own rate lines
}

// ScopedPlan is a card in force for one segment or exchange.
type ScopedPlan struct {
	Scope  string `json:"scope"`
	PlanID string `json:"plan"`
	Plan   string `json:"plan_name"`
	Source string `json:"source"`
	From   string `json:"from"`
}

// brokerageBook resolves plans for one date.
type brokerageBook struct {
	date      string
	plans     map[string]Plan
	latest    map[string]PlanAssignment // target#scope -> assignment in force
	clients   map[string]Client
	overrides map[string]OverrideVersion // ucc -> lines in force
	setup     BrokerageSetup
	sched     string
}

func latestKey(target, scope string) string { return target + "#" + scope }

func (o *Ops) brokerageBook(ctx context.Context, tenant, date string) (*brokerageBook, error) {
	ps, err := o.Plans(ctx, tenant)
	if err != nil {
		return nil, err
	}
	as, err := o.Assignments(ctx, tenant)
	if err != nil {
		return nil, err
	}
	cm, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, err
	}
	ovs, err := docs.List[ClientOverride](ctx, o.Docs, tenant, cBrokOverride, "")
	if err != nil {
		return nil, err
	}
	setup, err := o.BrokerageSetup(ctx, tenant)
	if err != nil {
		return nil, err
	}
	bb := &brokerageBook{date: date, plans: map[string]Plan{}, latest: map[string]PlanAssignment{}, clients: cm, overrides: map[string]OverrideVersion{}, setup: setup, sched: o.App.Schedule.Version}
	for _, p := range ps {
		bb.plans[p.ID] = p
	}
	for _, a := range as { // sorted by target, then from, then id
		if a.From <= date {
			bb.latest[latestKey(a.Target, a.Scope)] = a
		}
	}
	for _, ov := range ovs {
		if v, ok := ov.At(date); ok && len(v.Rules) > 0 {
			bb.overrides[ov.UCC] = v
		}
	}
	return bb, nil
}

// candidate is a plan that may price a client's order, with why it applies.
type candidate struct {
	a      PlanAssignment
	plan   Plan
	source string
}

// candidates lists the plans that apply to a client's product on an
// exchange, in the order they are tried: the client's own cards (exchange,
// segment, all), then their groups' (the most recently assigned group at
// each level), then the firm's. A plan with no line for an order passes it
// to the next. A client's exclusive (scheme) plan stands alone.
func (bb *brokerageBook) candidates(ucc string, b charges.Bucket, exchange string) []candidate {
	c := bb.clients[ucc]
	var scopes []string
	if b != "" {
		seg := segmentOf(b)
		if exchange != "" {
			scopes = append(scopes, seg+"-"+strings.ToUpper(exchange))
		}
		scopes = append(scopes, seg)
	}
	scopes = append(scopes, "")
	var out []candidate
	add := func(a PlanAssignment, src string) {
		if p, ok := bb.plans[a.PlanID]; ok && a.PlanID != "" {
			if a.Scope != "" {
				src += " · " + a.Scope
			}
			out = append(out, candidate{a, p, src})
		}
	}
	if a, ok := bb.latest[latestKey("ucc:"+ucc, "")]; ok && a.Exclusive && a.PlanID != "" {
		add(a, "Client")
		return out
	}
	for _, sc := range scopes {
		if a, ok := bb.latest[latestKey("ucc:"+ucc, sc)]; ok {
			add(a, "Client")
		}
	}
	for _, sc := range scopes {
		var best PlanAssignment
		bestTag := ""
		for _, t := range c.Tags {
			a, ok := bb.latest[latestKey("tag:"+t, sc)]
			if !ok || a.PlanID == "" {
				continue
			}
			if bestTag == "" || a.From > best.From || (a.From == best.From && a.ID > best.ID) {
				best, bestTag = a, t
			}
		}
		if bestTag != "" {
			add(best, "Group "+bestTag)
		}
	}
	for _, sc := range scopes {
		if a, ok := bb.latest[latestKey("default", sc)]; ok {
			add(a, "Firm default")
		}
	}
	return out
}

// resolve finds the client's main plan (every segment): their own
// assignment, else the most recently assigned of their groups, else the
// firm default. Segment cards are listed in Segments.
func (bb *brokerageBook) resolve(ucc string) (EffectivePlan, Plan, bool) {
	c := bb.clients[ucc]
	ep := EffectivePlan{UCC: ucc, Name: c.Name, Tags: c.Tags, Source: "Charge schedule", Plan: "Charge schedule " + bb.sched}
	if ep.Tags == nil {
		ep.Tags = []string{}
	}
	_, ep.Override = bb.overrides[ucc]
	cs := bb.candidates(ucc, "", "")
	// Segment cards in force for the client, most specific source only.
	if len(cs) == 0 || !cs[0].a.Exclusive {
		seen := map[string]bool{}
		for _, seg := range Segments {
			for _, x := range bb.scopesFor(seg) {
				for _, cd := range bb.candidates(ucc, charges.Bucket(seg+"-X"), x) {
					if cd.a.Scope == "" || seen[cd.a.Scope] {
						continue
					}
					seen[cd.a.Scope] = true
					ep.Segments = append(ep.Segments, ScopedPlan{cd.a.Scope, cd.plan.ID, cd.plan.Name, cd.source, cd.a.From})
				}
			}
		}
		sort.Slice(ep.Segments, func(i, j int) bool { return ep.Segments[i].Scope < ep.Segments[j].Scope })
	}
	if len(cs) == 0 {
		return ep, Plan{}, false
	}
	ep.PlanID, ep.Plan, ep.Source, ep.From = cs[0].plan.ID, cs[0].plan.Name, cs[0].source, cs[0].a.From
	return ep, cs[0].plan, true
}

// scopesFor lists the exchanges that have a card assigned for a segment.
func (bb *brokerageBook) scopesFor(seg string) []string {
	ex := map[string]bool{"": true}
	for k := range bb.latest {
		_, sc, _ := strings.Cut(k, "#")
		if s, x, ok := strings.Cut(sc, "-"); ok && s == seg {
			ex[x] = true
		}
	}
	return sortedKeys(ex)
}

// rule is the brokerage rule for a client's order on the book's date.
//
// The card that prices it is the first candidate with a line for the
// order. The client's own lines (overrides) are then checked alongside the
// card's, most specific line first; at equal specificity the client's own
// line wins.
func (bb *brokerageBook) rule(ucc string, q charges.RateQuery) (charges.BrokerageRule, string, bool) {
	ov, hasOv := bb.overrides[ucc]
	var card *candidate
	var lines map[string]charges.BrokerageRule
	for _, cd := range bb.candidates(ucc, q.Bucket, q.Exchange) {
		v, ok := cd.plan.At(bb.date)
		if !ok {
			continue
		}
		if _, _, ok := charges.Lookup(v.Rules, q); ok {
			c := cd
			card, lines = &c, v.Rules
			break
		}
	}
	for _, k := range q.Keys() {
		if hasOv {
			if r, ok := ov.Rules[k]; ok {
				label := "Client rate"
				if card != nil {
					label = card.plan.Name + " + client rate"
				}
				return r, label, true
			}
		}
		if card != nil {
			if r, ok := lines[k]; ok {
				return r, card.plan.Name, true
			}
		}
	}
	return charges.BrokerageRule{}, "", false
}

// ---- client overrides: a client's own rate lines ----

// OverrideVersion is a client's own rate lines from a date. No lines
// removes the client's own rates from that date.
type OverrideVersion struct {
	From  string                           `json:"from"`
	Rules map[string]charges.BrokerageRule `json:"rules"`
	Note  string                           `json:"note,omitempty"`
	By    string                           `json:"by"`
	At    string                           `json:"at"`
}

// ClientOverride is the history of a client's own rate lines. They win over
// the client's card for the orders they fit.
type ClientOverride struct {
	UCC      string            `json:"ucc"`
	Versions []OverrideVersion `json:"versions"`
}

// At returns the version in force on date.
func (c ClientOverride) At(date string) (OverrideVersion, bool) {
	var best OverrideVersion
	ok := false
	for _, v := range c.Versions {
		if v.From <= date && (!ok || v.From > best.From) {
			best, ok = v, true
		}
	}
	return best, ok
}

// OverrideInput sets a client's own rate lines from a date.
type OverrideInput struct {
	UCC   string                           `json:"ucc"`
	From  string                           `json:"from"`
	Rules map[string]charges.BrokerageRule `json:"rules"`
	Note  string                           `json:"note"`
}

// SaveOverride records a client's own rate lines from a date (replacing a
// version that starts on the same date). Empty lines end them from the date.
func (o *Ops) SaveOverride(ctx context.Context, tenant, actor string, in OverrideInput) (ClientOverride, error) {
	if err := o.require(ctx, tenant, actor, "Changing client brokerage rates", RoleChecker); err != nil {
		return ClientOverride{}, err
	}
	return o.saveOverride(ctx, tenant, actor, in)
}

func (o *Ops) saveOverride(ctx context.Context, tenant, actor string, in OverrideInput) (ClientOverride, error) {
	c, _, err := o.saveOverrideChanged(ctx, tenant, actor, in)
	return c, err
}

func (o *Ops) saveOverrideChanged(ctx context.Context, tenant, actor string, in OverrideInput) (ClientOverride, bool, error) {
	in.UCC = strings.ToUpper(strings.TrimSpace(in.UCC))
	if _, err := o.Client(ctx, tenant, in.UCC); err != nil {
		return ClientOverride{}, false, err
	}
	if !validDate(in.From) {
		return ClientOverride{}, false, invalid("effective from must be a date")
	}
	rules, err := cleanRules(in.Rules)
	if err != nil {
		return ClientOverride{}, false, err
	}
	v := OverrideVersion{From: in.From, Rules: rules, Note: strings.TrimSpace(in.Note), By: actor, At: o.now().Format("2006-01-02 15:04")}
	changed := true
	c, err := upd(ctx, o.Docs, tenant, cBrokOverride, in.UCC, actor, &ClientOverride{UCC: in.UCC}, func(c *ClientOverride) error {
		for _, x := range c.Versions {
			if x.From == in.From && sameRules(x.Rules, rules) {
				changed = false
				return nil
			}
		}
		kept := c.Versions[:0]
		for _, x := range c.Versions {
			if x.From != in.From {
				kept = append(kept, x)
			}
		}
		c.Versions = append(kept, v)
		sort.Slice(c.Versions, func(i, j int) bool { return c.Versions[i].From < c.Versions[j].From })
		return nil
	})
	if err != nil || !changed {
		return c, changed, err
	}
	o.log(ctx, tenant, actor, "brokerage.client_rates", in.UCC, map[string]any{"from": in.From, "lines": len(rules)})
	return c, true, nil
}

// ClientOverrides lists every client with their own rate lines.
func (o *Ops) ClientOverrides(ctx context.Context, tenant string) ([]ClientOverride, error) {
	return docs.List[ClientOverride](ctx, o.Docs, tenant, cBrokOverride, "")
}

// ---- channels and how trades are classified ----

// TerminalChannel says which channel trades from matching terminals came
// through. Terminal is a pattern: * matches any run of characters.
type TerminalChannel struct {
	Terminal string `json:"terminal"`
	Channel  string `json:"channel"`
}

// BrokerageSetup is how the firm tells its channels and special cases apart.
type BrokerageSetup struct {
	// Channels the firm prices separately, e.g. dealer, online, mobile.
	Channels []string `json:"channels"`
	// DefaultChannel is the channel of trades no terminal rule matches.
	DefaultChannel string            `json:"default_channel"`
	Terminals      []TerminalChannel `json:"terminals"`
	// AuctionSettlements are the settlement types (as in the exchange's
	// trade file) that mark auction trades.
	AuctionSettlements []string `json:"auction_settlements"`
	// IndexUnderlyings are added to the built-in list of index underlyings.
	IndexUnderlyings []string `json:"index_underlyings"`
	By               string   `json:"by,omitempty"`
	At               string   `json:"at,omitempty"`
}

// DefaultBrokerageSetup is used until the firm saves its own.
func DefaultBrokerageSetup() BrokerageSetup {
	return BrokerageSetup{Channels: []string{"dealer", "online", "mobile"}, DefaultChannel: "dealer", Terminals: []TerminalChannel{},
		AuctionSettlements: []string{"A", "R", "AUC", "AUCTION"}, IndexUnderlyings: []string{}}
}

// BrokerageSetup returns the firm's channel and case setup.
func (o *Ops) BrokerageSetup(ctx context.Context, tenant string) (BrokerageSetup, error) {
	s, _, err := docs.Get[BrokerageSetup](ctx, o.Docs, tenant, cBrokSetup, "setup")
	if errors.Is(err, docs.ErrNotFound) {
		return DefaultBrokerageSetup(), nil
	}
	if s.Terminals == nil {
		s.Terminals = []TerminalChannel{}
	}
	if s.IndexUnderlyings == nil {
		s.IndexUnderlyings = []string{}
	}
	return s, err
}

// SaveBrokerageSetup checks and saves the channel and case setup.
func (o *Ops) SaveBrokerageSetup(ctx context.Context, tenant, actor string, s BrokerageSetup) (BrokerageSetup, error) {
	if err := o.require(ctx, tenant, actor, "Changing brokerage channels", RoleChecker); err != nil {
		return s, err
	}
	return o.saveBrokerageSetup(ctx, tenant, actor, s)
}

func (o *Ops) saveBrokerageSetup(ctx context.Context, tenant, actor string, s BrokerageSetup) (BrokerageSetup, error) {
	known := map[string]bool{}
	var chans []string
	for _, c := range s.Channels {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" || known[c] {
			continue
		}
		if !charges.ValidChannel(c) {
			return s, invalid("channel %q: use 2 to 20 lower-case letters, digits or dashes", c)
		}
		known[c] = true
		chans = append(chans, c)
	}
	if len(chans) == 0 {
		return s, invalid("name at least one channel")
	}
	s.Channels = chans
	s.DefaultChannel = strings.ToLower(strings.TrimSpace(s.DefaultChannel))
	if !known[s.DefaultChannel] {
		return s, invalid("the default channel must be one of the channels")
	}
	var ts []TerminalChannel
	for _, t := range s.Terminals {
		t.Terminal, t.Channel = strings.TrimSpace(t.Terminal), strings.ToLower(strings.TrimSpace(t.Channel))
		if t.Terminal == "" {
			continue
		}
		if _, err := path.Match(t.Terminal, ""); err != nil {
			return s, invalid("terminal pattern %q is not valid", t.Terminal)
		}
		if !known[t.Channel] {
			return s, invalid("terminal %s: channel %q is not one of the channels", t.Terminal, t.Channel)
		}
		ts = append(ts, t)
	}
	s.Terminals = ts
	if s.Terminals == nil {
		s.Terminals = []TerminalChannel{}
	}
	up := func(xs []string) []string {
		out := []string{}
		for _, x := range xs {
			if x = strings.ToUpper(strings.TrimSpace(x)); x != "" {
				out = append(out, x)
			}
		}
		return out
	}
	s.AuctionSettlements, s.IndexUnderlyings = up(s.AuctionSettlements), up(s.IndexUnderlyings)
	s.By, s.At = actor, o.now().Format("2006-01-02 15:04")
	if err := docs.Upsert(ctx, o.Docs, tenant, cBrokSetup, "setup", s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "brokerage.setup", "setup", map[string]any{"channels": len(s.Channels), "terminals": len(s.Terminals)})
	return s, nil
}

// channelOf is the channel a terminal's trades came through: the first
// matching terminal rule, else the default.
func (s BrokerageSetup) channelOf(terminal string) string {
	terminal = strings.TrimSpace(terminal)
	for _, t := range s.Terminals {
		if ok, _ := path.Match(t.Terminal, terminal); ok && terminal != "" {
			return t.Channel
		}
	}
	if s.DefaultChannel == "" {
		return "dealer"
	}
	return s.DefaultChannel
}

// classify gives a trade's channel and cases (auction; index or stock).
func (s BrokerageSetup) classify(t ingest.TradeEvent) (string, []string) {
	var cases []string
	switch t.Segment {
	case "CM":
		for _, a := range s.AuctionSettlements {
			if t.Settlement != "" && t.Settlement == a {
				cases = append(cases, charges.CaseAuction)
			}
		}
	case "FO":
		kind := charges.CaseStock
		for _, l := range [][]string{charges.IndexUnderlyings, s.IndexUnderlyings} {
			for _, u := range l {
				if t.Symbol == u {
					kind = charges.CaseIndex
				}
			}
		}
		cases = append(cases, kind)
	}
	return s.channelOf(t.Terminal), cases
}

// billingHooks plugs plans, lot sizes and trade classes into billing and
// contract notes.
func (o *Ops) billingHooks(ctx context.Context, tenant, date string) (app.Pricing, error) {
	bb, err := o.brokerageBook(ctx, tenant, date)
	if err != nil {
		return app.Pricing{}, err
	}
	_, look, _ := o.Market(ctx, tenant, date)
	brok := func(ucc string, q charges.RateQuery, d string) (charges.BrokerageRule, string, bool) {
		return bb.rule(ucc, q)
	}
	lot := func(t ingest.TradeEvent) int64 {
		if i, ok := look.ForTrade(t); ok && i.Lot > 0 {
			return i.Lot
		}
		return 1
	}
	return app.Pricing{Brokerage: brok, LotSize: lot, Classify: bb.setup.classify}, nil
}

// BrokerageView is the brokerage screen.
type BrokerageView struct {
	Date        string                 `json:"date"`
	Plans       []PlanView             `json:"plans"`
	Assignments []PlanAssignment       `json:"assignments"`
	Effective   []EffectivePlan        `json:"effective"`
	Tags        []TagCount             `json:"tags"`
	Schedule    []ScheduleBrokerage    `json:"schedule"`
	Products    []ProductName          `json:"products"`
	Current     map[string]PlanCurrent `json:"current"` // target#scope -> plan in force
	// Client rate lines in force, the channel setup and the cases a line
	// can be for.
	Overrides []OverrideView `json:"overrides"`
	Setup     BrokerageSetup `json:"setup"`
	Cases     []CaseName     `json:"cases"`
	Segments  []string       `json:"segments"`
}

// OverrideView is a client's own rate lines with the version in force.
type OverrideView struct {
	ClientOverride
	Name     string            `json:"name"`
	Status   string            `json:"status"`
	Current  *OverrideVersion  `json:"current,omitempty"`
	Describe map[string]string `json:"describe"`
}

// CaseName is a case with its display name and the products it fits.
type CaseName struct {
	Case     string   `json:"case"`
	Name     string   `json:"name"`
	Products []string `json:"products"`
}

// PlanView is a plan with its version in force and who is on it.
type PlanView struct {
	Plan
	Current  *PlanVersion      `json:"current,omitempty"`
	Upcoming *PlanVersion      `json:"upcoming,omitempty"`
	Describe map[string]string `json:"describe"`
	Clients  int               `json:"clients"`
}

// PlanCurrent is the assignment in force for a target.
type PlanCurrent struct {
	PlanID string `json:"plan"`
	From   string `json:"from"`
}

// ScheduleBrokerage is the charge schedule's own brokerage for a product.
type ScheduleBrokerage struct {
	Bucket   string `json:"bucket"`
	Exchange string `json:"exchange"`
	Describe string `json:"describe"`
}

// ProductName is a bucket with its display name.
type ProductName struct {
	Bucket string `json:"bucket"`
	Name   string `json:"name"`
}

// Brokerage builds the brokerage screen for a date.
func (o *Ops) Brokerage(ctx context.Context, tenant, date string) (BrokerageView, error) {
	bb, err := o.brokerageBook(ctx, tenant, date)
	if err != nil {
		return BrokerageView{}, err
	}
	v := BrokerageView{Date: date, Plans: []PlanView{}, Effective: []EffectivePlan{}, Current: map[string]PlanCurrent{}}
	if v.Assignments, err = o.Assignments(ctx, tenant); err != nil {
		return v, err
	}
	if v.Tags, err = o.Tags(ctx, tenant); err != nil {
		return v, err
	}
	for t, a := range bb.latest {
		v.Current[t] = PlanCurrent{a.PlanID, a.From}
	}
	v.Setup, v.Segments, v.Overrides = bb.setup, Segments, []OverrideView{}
	for _, c := range charges.CaseNames {
		cn := CaseName{Case: c.Case, Name: c.Name, Products: []string{}}
		for _, b := range charges.Buckets {
			if charges.CaseFits(c.Case, b) {
				cn.Products = append(cn.Products, string(b))
			}
		}
		v.Cases = append(v.Cases, cn)
	}
	ovs, _ := o.ClientOverrides(ctx, tenant)
	for _, ov := range ovs {
		x := OverrideView{ClientOverride: ov, Name: bb.clients[ov.UCC].Name, Status: bb.clients[ov.UCC].Status, Describe: map[string]string{}}
		if cur, ok := ov.At(date); ok {
			c := cur
			x.Current = &c
			for k, r := range cur.Rules {
				x.Describe[k] = r.Describe()
			}
		}
		v.Overrides = append(v.Overrides, x)
	}
	count := map[string]int{}
	for _, u := range sortedKeys(bb.clients) {
		c := bb.clients[u]
		if c.Status != "Active" {
			continue
		}
		ep, _, _ := bb.resolve(u)
		v.Effective = append(v.Effective, ep)
		count[ep.PlanID]++
	}
	ps, _ := o.Plans(ctx, tenant)
	for _, p := range ps {
		pv := PlanView{Plan: p, Describe: map[string]string{}, Clients: count[p.ID]}
		if cur, ok := p.At(date); ok {
			c := cur
			pv.Current = &c
			for k, r := range cur.Rules {
				pv.Describe[k] = r.Describe()
			}
		}
		for i := range p.Versions {
			if p.Versions[i].From > date {
				u := p.Versions[i]
				pv.Upcoming = &u
				break
			}
		}
		v.Plans = append(v.Plans, pv)
	}
	for _, r := range o.App.Schedule.Rules {
		if r.EffectiveFrom <= date && (r.EffectiveTo == "" || r.EffectiveTo >= date) {
			v.Schedule = append(v.Schedule, ScheduleBrokerage{string(r.Bucket), r.Exchange, charges.ScheduleBrokerage(r).Describe()})
		}
	}
	for _, b := range charges.Buckets {
		v.Products = append(v.Products, ProductName{string(b), charges.BucketNames[b]})
	}
	return v, nil
}

// ClientBrokerage is the plan a client is on, for quotes and the client
// page: what each product costs on each exchange through the default
// channel, with the client's own lines and segment cards applied.
func (o *Ops) ClientBrokerage(ctx context.Context, tenant, ucc, date string) (EffectivePlan, map[string]string, error) {
	bb, err := o.brokerageBook(ctx, tenant, date)
	if err != nil {
		return EffectivePlan{}, nil, err
	}
	if _, ok := bb.clients[ucc]; !ok {
		return EffectivePlan{}, nil, fmt.Errorf("%w: client %s", ErrNotFound, ucc)
	}
	ep, _, _ := bb.resolve(ucc)
	desc := map[string]string{}
	exchanges := map[string][]string{"CM": {"NSE", "BSE"}, "FO": {"NSE", "BSE"}, "CD": {"NSE", "BSE"}, "COM": {"MCX", "NCDEX"}}
	for _, b := range charges.Buckets {
		seen := map[string]string{}
		var order []string
		for _, x := range exchanges[segmentOf(b)] {
			d := ""
			if r, name, ok := bb.rule(ucc, charges.RateQuery{Bucket: b, Exchange: x, Channel: bb.setup.DefaultChannel}); ok {
				d = r.Describe() + " (" + name + ")"
			} else if r, err := o.App.Schedule.Rule(b, x, date); err == nil {
				d = charges.ScheduleBrokerage(r).Describe() + " (schedule)"
			} else {
				continue
			}
			if _, ok := seen[d]; !ok {
				order = append(order, d)
			}
			seen[d] += x + " "
		}
		switch len(order) {
		case 0:
		case 1:
			desc[string(b)] = order[0]
		default:
			var parts []string
			for _, d := range order {
				parts = append(parts, strings.TrimSpace(seen[d])+": "+d)
			}
			desc[string(b)] = strings.Join(parts, "; ")
		}
	}
	return ep, desc, nil
}

// QuoteRule returns the brokerage rule a client pays for an order, if a
// card or the client's own lines set one.
func (o *Ops) QuoteRule(ctx context.Context, tenant, ucc string, q charges.RateQuery, date string) (charges.BrokerageRule, string, bool, error) {
	bb, err := o.brokerageBook(ctx, tenant, date)
	if err != nil {
		return charges.BrokerageRule{}, "", false, err
	}
	if q.Channel == "" {
		q.Channel = bb.setup.DefaultChannel
	}
	r, name, ok := bb.rule(strings.ToUpper(ucc), q)
	return r, name, ok, nil
}
