package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- client groups (tags) ----

var validTag = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,23}$`)

// NormaliseTag upper-cases a tag and turns spaces into dashes.
func NormaliseTag(t string) string {
	return strings.ToUpper(strings.Join(strings.Fields(t), "-"))
}

// TagCount is a group with its size.
type TagCount struct {
	Tag     string `json:"tag"`
	Clients int    `json:"clients"`
}

// Tags lists every group in use with its client count.
func (o *Ops) Tags(ctx context.Context, tenant string) ([]TagCount, error) {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, err
	}
	n := map[string]int{}
	for _, c := range cs {
		for _, t := range c.Tags {
			n[t]++
		}
	}
	// Groups that have a plan assigned exist even when empty.
	as, _ := docs.List[PlanAssignment](ctx, o.Docs, tenant, cBrokAssign, "")
	for _, a := range as {
		if t, ok := strings.CutPrefix(a.Target, "tag:"); ok {
			if _, seen := n[t]; !seen {
				n[t] = 0
			}
		}
	}
	out := []TagCount{}
	for _, t := range sortedKeys(n) {
		out = append(out, TagCount{t, n[t]})
	}
	return out, nil
}

// TagClients adds and removes groups on a set of clients.
func (o *Ops) TagClients(ctx context.Context, tenant, actor string, uccs, add, remove []string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Changing client groups", RoleMaker, RoleChecker); err != nil {
		return 0, err
	}
	norm := func(in []string) ([]string, error) {
		var out []string
		for _, t := range in {
			t = NormaliseTag(t)
			if t == "" {
				continue
			}
			if !validTag.MatchString(t) {
				return nil, invalid("group %q: use letters, digits, - or _, up to 24 characters", t)
			}
			out = append(out, t)
		}
		return out, nil
	}
	a, err := norm(add)
	if err != nil {
		return 0, err
	}
	r, err := norm(remove)
	if err != nil {
		return 0, err
	}
	if len(a)+len(r) == 0 {
		return 0, invalid("name at least one group to add or remove")
	}
	if len(uccs) == 0 {
		return 0, invalid("choose at least one client")
	}
	n := 0
	for _, u := range uccs {
		u = strings.ToUpper(strings.TrimSpace(u))
		var before []string
		c, err := docs.Update(ctx, o.Docs, tenant, cClient, u, actor, func(c *Client) error {
			before = append([]string(nil), c.Tags...)
			set := map[string]bool{}
			for _, t := range c.Tags {
				set[t] = true
			}
			for _, t := range a {
				set[t] = true
			}
			for _, t := range r {
				delete(set, t)
			}
			c.Tags = sortedKeys(set)
			return nil
		})
		if errors.Is(err, docs.ErrNotFound) {
			return n, fmt.Errorf("%w: client %s", ErrNotFound, u)
		}
		if err != nil {
			return n, err
		}
		if strings.Join(before, ",") != strings.Join(c.Tags, ",") {
			o.log(ctx, tenant, actor, "client.groups", u, map[string]any{"from": before, "to": c.Tags})
			n++
		}
	}
	return n, nil
}

// ---- brokerage plans ----

const (
	cBrokPlan   = "brokplan"
	cBrokAssign = "brokassign"
)

// PlanVersion is a plan's rates from a date. Products left out use the
// firm's charge schedule.
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
// date. Target is "default", "tag:<GROUP>" or "ucc:<UCC>". An empty plan
// removes an earlier assignment from that date.
type PlanAssignment struct {
	ID     string `json:"id"`
	Target string `json:"target"`
	PlanID string `json:"plan"`
	From   string `json:"from"`
	By     string `json:"by"`
	At     string `json:"at"`
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
	rules := map[string]charges.BrokerageRule{}
	for k, r := range in.Rules {
		b := charges.Bucket(k)
		if charges.BucketNames[b] == "" {
			return Plan{}, invalid("unknown product %q", k)
		}
		if r.Mode == "" || r.Mode == "inherit" {
			continue
		}
		if r.Mode == charges.PlanPerLot && (b == charges.CMDelivery || b == charges.CMIntraday) {
			return Plan{}, invalid("%s: per-lot brokerage applies to derivatives only", charges.BucketNames[b])
		}
		if err := r.Validate(); err != nil {
			return Plan{}, invalid("%s: %v", charges.BucketNames[b], err)
		}
		rules[k] = r
	}
	v := PlanVersion{From: in.From, Rules: rules, By: actor, At: o.now().Format("2006-01-02 15:04")}
	p, err := upd(ctx, o.Docs, tenant, cBrokPlan, in.ID, actor, &Plan{ID: in.ID}, func(p *Plan) error {
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
	if err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "brokerage.plan_saved", in.ID, map[string]any{"name": in.Name, "from": in.From, "rules": len(rules)})
	return p, nil
}

// Assign puts a target on a plan from a date ("" plan removes the earlier
// assignment for that target from the date).
func (o *Ops) Assign(ctx context.Context, tenant, actor, target, planID, from string) (PlanAssignment, error) {
	if err := o.require(ctx, tenant, actor, "Assigning brokerage plans", RoleChecker); err != nil {
		return PlanAssignment{}, err
	}
	if !validDate(from) {
		return PlanAssignment{}, invalid("effective from must be a date")
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
	} else if target == "default" {
		return PlanAssignment{}, invalid("choose the firm's default plan")
	}
	seq, err := o.next(ctx, tenant, "brokassign", actor)
	if err != nil {
		return PlanAssignment{}, err
	}
	a := PlanAssignment{ID: fmt.Sprintf("PA-%06d", seq), Target: target, PlanID: planID, From: from, By: actor, At: o.now().Format("2006-01-02 15:04")}
	if _, err := docs.Put(ctx, o.Docs, tenant, cBrokAssign, a.ID, a, 0, actor); err != nil {
		return a, err
	}
	o.log(ctx, tenant, actor, "brokerage.assigned", target, map[string]any{"plan": planID, "from": from})
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
}

// brokerageBook resolves plans for one date.
type brokerageBook struct {
	date    string
	plans   map[string]Plan
	latest  map[string]PlanAssignment // target -> assignment in force
	clients map[string]Client
	sched   string
}

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
	bb := &brokerageBook{date: date, plans: map[string]Plan{}, latest: map[string]PlanAssignment{}, clients: cm, sched: o.App.Schedule.Version}
	for _, p := range ps {
		bb.plans[p.ID] = p
	}
	for _, a := range as { // sorted by target, then from, then id
		if a.From <= date {
			bb.latest[a.Target] = a
		}
	}
	return bb, nil
}

// resolve finds the client's plan: their own assignment, else the most
// recently assigned of their groups, else the firm default.
func (bb *brokerageBook) resolve(ucc string) (EffectivePlan, Plan, bool) {
	c := bb.clients[ucc]
	ep := EffectivePlan{UCC: ucc, Name: c.Name, Tags: c.Tags, Source: "Charge schedule", Plan: "Charge schedule " + bb.sched}
	if ep.Tags == nil {
		ep.Tags = []string{}
	}
	pick := func(a PlanAssignment, src string) (EffectivePlan, Plan, bool) {
		p, ok := bb.plans[a.PlanID]
		if !ok {
			return ep, Plan{}, false
		}
		ep.PlanID, ep.Plan, ep.Source, ep.From = p.ID, p.Name, src, a.From
		return ep, p, true
	}
	if a, ok := bb.latest["ucc:"+ucc]; ok && a.PlanID != "" {
		return pick(a, "Client")
	}
	var best PlanAssignment
	bestTag := ""
	for _, t := range c.Tags {
		a, ok := bb.latest["tag:"+t]
		if !ok || a.PlanID == "" {
			continue
		}
		if bestTag == "" || a.From > best.From || (a.From == best.From && a.ID > best.ID) {
			best, bestTag = a, t
		}
	}
	if bestTag != "" {
		return pick(best, "Group "+bestTag)
	}
	if a, ok := bb.latest["default"]; ok && a.PlanID != "" {
		return pick(a, "Firm default")
	}
	return ep, Plan{}, false
}

// rule is the brokerage rule for a client's product on the book's date.
func (bb *brokerageBook) rule(ucc string, b charges.Bucket) (charges.BrokerageRule, string, bool) {
	_, p, ok := bb.resolve(ucc)
	if !ok {
		return charges.BrokerageRule{}, "", false
	}
	v, ok := p.At(bb.date)
	if !ok {
		return charges.BrokerageRule{}, "", false
	}
	r, ok := v.Rules[string(b)]
	if !ok {
		return charges.BrokerageRule{}, "", false
	}
	return r, p.Name, true
}

// billingHooks plugs plans and lot sizes into billing and contract notes.
func (o *Ops) billingHooks(ctx context.Context, tenant, date string) (app.BrokerageFunc, func(ingest.TradeEvent) int64, error) {
	bb, err := o.brokerageBook(ctx, tenant, date)
	if err != nil {
		return nil, nil, err
	}
	_, look, _ := o.Market(ctx, tenant, date)
	brok := func(ucc string, b charges.Bucket, exchange, d string) (charges.BrokerageRule, string, bool) {
		return bb.rule(ucc, b)
	}
	lot := func(t ingest.TradeEvent) int64 {
		if i, ok := look.ForTrade(t); ok && i.Lot > 0 {
			return i.Lot
		}
		return 1
	}
	return brok, lot, nil
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
	Current     map[string]PlanCurrent `json:"current"` // target -> plan in force
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

// ClientBrokerage is the plan a client is on, for quotes and the client page.
func (o *Ops) ClientBrokerage(ctx context.Context, tenant, ucc, date string) (EffectivePlan, map[string]string, error) {
	bb, err := o.brokerageBook(ctx, tenant, date)
	if err != nil {
		return EffectivePlan{}, nil, err
	}
	if _, ok := bb.clients[ucc]; !ok {
		return EffectivePlan{}, nil, fmt.Errorf("%w: client %s", ErrNotFound, ucc)
	}
	ep, p, ok := bb.resolve(ucc)
	desc := map[string]string{}
	var cur PlanVersion
	if ok {
		cur, _ = p.At(date)
	}
	for _, b := range charges.Buckets {
		if r, has := cur.Rules[string(b)]; ok && has {
			desc[string(b)] = r.Describe()
			continue
		}
		if r, err := o.App.Schedule.Rule(b, "NSE", date); err == nil {
			desc[string(b)] = charges.ScheduleBrokerage(r).Describe() + " (schedule)"
		}
	}
	return ep, desc, nil
}

// QuoteRule returns the brokerage rule a client pays for a product, if a
// plan sets one.
func (o *Ops) QuoteRule(ctx context.Context, tenant, ucc string, b charges.Bucket, date string) (charges.BrokerageRule, string, bool, error) {
	bb, err := o.brokerageBook(ctx, tenant, date)
	if err != nil {
		return charges.BrokerageRule{}, "", false, err
	}
	r, name, ok := bb.rule(strings.ToUpper(ucc), b)
	return r, name, ok, nil
}
