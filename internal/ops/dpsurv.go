package ops

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- the DP's own surveillance rules, mule accounts and staff trades ----
//
// CDSL/DP/SURV/2026/521 (DP2026-521) Part I B has each DP generate its own
// alerts, on thresholds it sets by risk, for CDSL's indicative themes. The
// themes Ledgerline has the data for are worked here, on the DP's
// transaction register (CDSL's transaction file and the DIS Ledgerline
// executes), the client master, approved change requests and the dispatch
// register:
//
//   - several accounts with the same PAN, mobile, e-mail, bank account or
//     address (raised half-yearly, 60 days to dispose of), and clusters of
//     more than the cluster size sharing a name, address or bank account;
//   - communications to the client bouncing, and debits after a bounce;
//   - frequent changes of the BO's details (more than the month's limit;
//     MCX's MRS alert on e-mail or mobile modified more than twice a month
//     is the same rule);
//   - off-market debits and pledges out of line with the declared income;
//   - off-market debits soon after a change of contact, address, bank or
//     demat details;
//   - debits from a dormant account;
//   - one account sending off-market to many, and many of the DP's
//     accounts sending to one;
//   - the reason code of an off-market instruction not fitting it (a gift
//     with consideration, a sale well under market value, a transfer "to
//     own account" to someone else's account).
//
// Mule accounts (SEBI PFUTP Regulations reg. 4(1) explanation (ii); SEBI
// Stock Brokers Regulations 2026 reg. 23(8); NSE's rule on one bank
// account across several PANs) are looked for in the same pass: bank
// accounts shared across PANs, and money passed through an account (paid in
// and paid back out with little trading). Staff trades ahead of a large
// client trade in the same contract and direction (front running, SEBI
// Master Circular for Stock Brokers 87.4) and synchronised opposite trades
// between connected clients (circular trading) are looked for on the day's
// trades.
//
// Each alert carries the transactions it is about (period, ISIN, quantity,
// value) so the case work and an STR can cite them. Thresholds are the
// firm's (survcase.go DPSurvSettings), scaled by the client's risk
// category. The rules look back two months, the window CDSL gives for
// identifying an adverse observation, so a file loaded late is still
// worked.

const dpLookbackDays = 61

// dpRuleEnv is what one run reads once.
type dpRuleEnv struct {
	o       *Ops
	ctx     context.Context
	tenant  string
	actor   string
	date    string
	set     DPSurvSettings
	cs      map[string]Client
	dets    map[string]UCCDetails
	byBO    map[string]string
	txns    []DPTxn
	changes []ChangeRequest
	n       int
	// raiseOff: only the groups are wanted (ConnectedGroups).
	raiseOff bool
}

func (e *dpRuleEnv) raise(a Alert, key string) {
	if e.raiseOff {
		return
	}
	if a.Date == "" {
		a.Date = e.date
	}
	if e.o.raiseAlert(e.ctx, e.tenant, e.actor, a, key) {
		e.n++
	}
}

// ownDP: the client has an active account with the firm's DP.
func ownDP(c Client) bool {
	for _, d := range activeDemats(c) {
		if d.OwnDP {
			return true
		}
	}
	return false
}

func (e *dpRuleEnv) area(uccs ...string) string {
	for _, u := range uccs {
		if ownDP(e.cs[u]) {
			return AreaDP
		}
	}
	return ""
}

// halfYear is the half-year a date is in: 2026-H2.
func halfYear(d string) string {
	if len(d) < 7 {
		return d
	}
	if d[5:7] <= "06" {
		return d[:4] + "-H1"
	}
	return d[:4] + "-H2"
}

func normWords(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// dpOffMarket: a transfer that is not an on-market settlement or a pledge.
func dpOffMarket(t DPTxn) bool {
	if t.Pledge != "" || t.ClearingMember != "" {
		return false
	}
	k := strings.ToLower(t.Kind)
	return strings.Contains(k, "off-market") || strings.Contains(k, "inter-depository") || k == "account transfer out"
}

// dpPledgeOut: a pledge created on the client's securities.
func dpPledgeOut(t DPTxn) bool { return t.Pledge == PledgeCreated || t.Pledge == MarginPledgeCreated }

// txnValue is a DP transaction's value: as recorded, else at the latest
// closing price on or before the day.
func (e *dpRuleEnv) txnValue(t DPTxn, px ISINPrices) money.Dec {
	if t.Value.Sign() != 0 {
		return t.Value.Abs()
	}
	if p, ok := px.Prices[t.ISIN]; ok {
		return t.Qty.Abs().Mul(p)
	}
	return money.Dec{}
}

// RunDPRules runs the DP's own rules, the mule-account rules and the staff
// and connected-client trade rules for a date, and returns how many new
// alerts were raised.
func (o *Ops) RunDPRules(ctx context.Context, tenant, actor, date string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Running surveillance rules", RoleCompliance, RoleChecker); err != nil {
		return 0, err
	}
	if !validDate(date) {
		return 0, invalid("give a date (YYYY-MM-DD)")
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return 0, err
	}
	e := &dpRuleEnv{o: o, ctx: ctx, tenant: tenant, actor: actor, date: date, set: o.DPSurvConfig(ctx, tenant), cs: cs, dets: map[string]UCCDetails{}, byBO: o.boClients(ctx, tenant)}
	if ds, err := docs.List[UCCDetails](ctx, o.Docs, tenant, cUCCDetails, ""); err == nil {
		for _, d := range ds {
			e.dets[d.UCC] = d
		}
	}
	all, _ := docs.List[DPTxn](ctx, o.Docs, tenant, cDPTxn, "")
	for _, t := range all {
		if t.Date != "" && t.Date <= date {
			e.txns = append(e.txns, t)
		}
	}
	sort.SliceStable(e.txns, func(i, j int) bool { return e.txns[i].Date < e.txns[j].Date })
	chs, _ := docs.List[ChangeRequest](ctx, o.Docs, tenant, cChange, "")
	for _, c := range chs {
		if c.Status == "Approved" {
			e.changes = append(e.changes, c)
		}
	}
	groups := e.sharedDetails()
	e.bounces()
	e.frequentChanges()
	e.transfers()
	e.reasonCodes()
	e.passThrough()
	trades, _ := o.exchangeTrades(ctx, tenant, date)
	e.frontRunning(trades)
	e.circular(trades, groups)
	o.log(ctx, tenant, actor, "surveillance.dprules.run", date, map[string]any{"new_alerts": e.n})
	return e.n, nil
}

// ---- several accounts with the same details ----

type sharedGroup struct {
	Kind    string   `json:"kind"`
	Shown   string   `json:"shown"` // the shared value, masked
	Members []string `json:"members"`
	PANs    int      `json:"pans"`
	key     string
}

// sharedDetails finds the details several clients share and raises the
// half-yearly alerts; it returns the groups (for connected clients).
func (e *dpRuleEnv) sharedDetails() []sharedGroup {
	type acc struct {
		shown string
		uccs  map[string]bool
		pans  map[string]bool
	}
	by := map[string]map[string]*acc{}
	add := func(kind, val, shown, ucc, pan string) {
		if val == "" {
			return
		}
		if by[kind] == nil {
			by[kind] = map[string]*acc{}
		}
		a := by[kind][val]
		if a == nil {
			a = &acc{shown: shown, uccs: map[string]bool{}, pans: map[string]bool{}}
			by[kind][val] = a
		}
		a.uccs[ucc] = true
		a.pans[firstOf(pan, ucc)] = true
	}
	for _, u := range sortedKeys(e.cs) {
		c := e.cs[u]
		if c.Status == "Closed" {
			continue
		}
		pan := strings.ToUpper(strings.TrimSpace(c.PAN))
		add("PAN", pan, MaskPAN(pan), u, u) // each client counts once
		if m := normMobile(c.Mobile); m != "" {
			add("mobile", m, maskMobile(m), u, pan)
		}
		if em := strings.ToLower(strings.TrimSpace(c.Email)); strings.Contains(em, "@") {
			add("e-mail", em, maskEmail(em), u, pan)
		}
		for _, b := range activeBanks(c) {
			if acct := strings.TrimLeft(strings.TrimSpace(b.Account), "0"); acct != "" {
				add("bank account", strings.ToUpper(strings.TrimSpace(b.IFSC))+":"+acct, MaskAccount(acct)+" at "+strings.ToUpper(b.IFSC), u, pan)
			}
		}
		d := e.dets[u]
		if a := normWords(d.Address1 + " " + d.Address2 + " " + d.Address3); a != "" && strings.TrimSpace(d.PIN) != "" {
			add("address", a+"|"+strings.TrimSpace(d.PIN), "an address at PIN "+strings.TrimSpace(d.PIN), u, pan)
		}
		if n := normName(c.Name); n != "" {
			add("name", n, "a similar name", u, pan)
		}
	}
	period := halfYear(e.date)
	var groups []sharedGroup
	for _, kind := range []string{"PAN", "mobile", "e-mail", "bank account", "address", "name"} {
		for _, val := range sortedKeys(by[kind]) {
			a := by[kind][val]
			n := len(a.pans)
			if kind == "PAN" {
				n = len(a.uccs)
			}
			if n < 2 {
				continue
			}
			members := sortedKeys(a.uccs)
			g := sharedGroup{Kind: kind, Shown: a.shown, Members: members, PANs: len(a.pans), key: kind + ":" + shortHash(val)}
			cluster := n > e.set.ClusterSize
			if kind == "name" && !cluster {
				continue // a shared name alone is not a theme; a cluster is
			}
			if kind != "name" {
				groups = append(groups, g)
			}
			sev := map[string]string{"PAN": "High", "bank account": "High", "mobile": "Medium", "e-mail": "Medium", "address": "Low"}[kind]
			typ := "Several accounts with the same " + kind
			if kind == "bank account" {
				typ = "One bank account registered for several PANs"
			}
			if cluster {
				sev, typ = "High", fmt.Sprintf("Cluster of more than %d accounts with %s", e.set.ClusterSize, map[string]string{"name": "similar names"}[kind])
				if kind != "name" {
					typ = fmt.Sprintf("Cluster of more than %d accounts with the same %s", e.set.ClusterSize, kind)
				}
			}
			note := ""
			if (kind == "mobile" || kind == "e-mail") && !cluster && e.familyDeclared(kind, members) {
				sev, note = "Low", " All but one holder declared the "+kind+" as a family member's (spouse, dependent child or parent): check the declarations."
			}
			shown := members
			if len(shown) > 30 {
				shown = shown[:30]
			}
			e.raise(Alert{Source: "Internal", Area: e.area(members...), Type: typ, UCC: members[0], Severity: sev, HalfYearly: true,
				Detail: fmt.Sprintf("%d clients (%d PANs) share %s: %s%s. Check each account belongs to the person named and is not used by another; a bank account must be the client's own.%s",
					len(members), len(a.pans), a.shown, strings.Join(shown, ", "), map[bool]string{true: " and more", false: ""}[len(members) > 30], note)},
				"DPS:"+g.key+":"+period)
		}
	}
	return groups
}

// familyDeclared: every sharer but one says the contact is a family
// member's.
func (e *dpRuleEnv) familyDeclared(kind string, uccs []string) bool {
	self := 0
	for _, u := range uccs {
		d := e.dets[u]
		rel := d.MobileRelation
		if kind == "e-mail" {
			rel = d.EmailRelation
		}
		switch strings.ToLower(strings.TrimSpace(rel)) {
		case "spouse", "dependent child", "dependent parent", "dependent children", "dependent parents":
		default:
			self++
		}
	}
	return self <= 1
}

// ConnectedGroup is a set of clients joined by a shared mobile, e-mail,
// bank account or address (MCX's group of connected clients).
type ConnectedGroup struct {
	ID      string   `json:"id"`
	Members []string `json:"members"`
	Links   []string `json:"links"` // what they share, masked
}

// connect joins the groups that share a member.
func connect(gs []sharedGroup) []ConnectedGroup {
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if p, ok := parent[x]; ok && p != x {
			r := find(p)
			parent[x] = r
			return r
		}
		parent[x] = x
		return x
	}
	for _, g := range gs {
		if g.Kind == "PAN" {
			continue
		}
		for _, m := range g.Members[1:] {
			a, b := find(g.Members[0]), find(m)
			if a != b {
				if a < b {
					parent[b] = a
				} else {
					parent[a] = b
				}
			}
		}
	}
	out := map[string]*ConnectedGroup{}
	for _, g := range gs {
		if g.Kind == "PAN" {
			continue
		}
		r := find(g.Members[0])
		cg := out[r]
		if cg == nil {
			cg = &ConnectedGroup{ID: "G-" + r}
			out[r] = cg
		}
		cg.Links = append(cg.Links, fmt.Sprintf("%s %s (%d)", g.Kind, g.Shown, len(g.Members)))
	}
	for x := range parent {
		if cg := out[find(x)]; cg != nil {
			cg.Members = append(cg.Members, x)
		}
	}
	var res []ConnectedGroup
	for _, k := range sortedKeys(out) {
		cg := out[k]
		sort.Strings(cg.Members)
		res = append(res, *cg)
	}
	return res
}

// ConnectedGroups lists the groups of connected clients today.
func (o *Ops) ConnectedGroups(ctx context.Context, tenant, actor string) ([]ConnectedGroup, error) {
	if err := o.require(ctx, tenant, actor, "Seeing connected clients", RoleCompliance, RoleChecker); err != nil {
		return nil, err
	}
	return o.connectedGroupsAs(ctx, tenant, actor)
}

// connectedGroupsSys is ConnectedGroups for Ledgerline's own checks (the
// split of positions across connected clients' codes, rmslimits.go).
func (o *Ops) connectedGroupsSys(ctx context.Context, tenant string) []ConnectedGroup {
	gs, _ := o.connectedGroupsAs(ctx, tenant, SystemActor)
	return gs
}

func (o *Ops) connectedGroupsAs(ctx context.Context, tenant, actor string) ([]ConnectedGroup, error) {
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, err
	}
	e := &dpRuleEnv{o: o, ctx: ctx, tenant: tenant, actor: actor, date: o.businessDate(ctx, tenant), set: o.DPSurvConfig(ctx, tenant), cs: cs, dets: map[string]UCCDetails{}}
	if ds, err := docs.List[UCCDetails](ctx, o.Docs, tenant, cUCCDetails, ""); err == nil {
		for _, d := range ds {
			e.dets[d.UCC] = d
		}
	}
	// The groups only: no alert is raised from this view.
	gs := e.groupsOnly()
	out := connect(gs)
	if out == nil {
		out = []ConnectedGroup{}
	}
	return out, nil
}

// groupsOnly is sharedDetails without raising anything.
func (e *dpRuleEnv) groupsOnly() []sharedGroup {
	saved := e.raiseOff
	e.raiseOff = true
	defer func() { e.raiseOff = saved }()
	return e.sharedDetails()
}

// ---- bounced communications ----

func (e *dpRuleEnv) bounces() {
	ds, _ := docs.List[Dispatch](e.ctx, e.o.Docs, e.tenant, cDispatch, "")
	from := addDays(e.date, -90)
	n := map[string]int{}
	first := map[string]string{}
	for _, d := range ds {
		if d.Status != "Bounced" || d.UCC == "" {
			continue
		}
		day := firstOf(dayOf(d.BouncedAt), dayOf(d.Created))
		if day < from || day > e.date {
			continue
		}
		n[d.UCC]++
		if first[d.UCC] == "" || day < first[d.UCC] {
			first[d.UCC] = day
		}
	}
	for _, u := range sortedKeys(n) {
		// A debit after the bounce, while the client may not know of it.
		for _, t := range e.txns {
			if t.UCC == u && t.Qty.Sign() < 0 && (dpOffMarket(t) || dpPledgeOut(t)) && t.Date >= first[u] && t.Date >= addDays(e.date, -dpLookbackDays) {
				e.raise(Alert{Source: "Internal", Area: e.area(u), Type: "Debit after communications to the client bounced", UCC: u, Severity: "High", BOID: t.BOID, TxnFrom: t.Date, TxnTo: t.Date, ISIN: t.ISIN, Qty: t.Qty.Abs().String(),
					Detail: fmt.Sprintf("%d statements or notices to the client bounced since %s, then %s of %s (%s) on %s. Confirm the instruction with the client by another channel and update the contact details.", n[u], first[u], t.Qty.Abs().String(), firstOf(t.Symbol, t.ISIN), t.Kind, t.Date)},
					"BNC-DBT:"+t.ID)
				break
			}
		}
		if n[u] >= 2 {
			e.raise(Alert{Source: "Internal", Area: e.area(u), Type: "Communications to the client bounce", UCC: u, Severity: "Low",
				Detail: fmt.Sprintf("%d statements or notices bounced in 90 days (first on %s). Get correct contact details; watch the account for debits.", n[u], first[u])},
				"BNC:"+u+":"+e.date[:7])
		}
	}
}

// ---- frequent changes of details ----

// modKinds are the changes the themes look at.
var modKinds = map[string]bool{"Email": true, "Mobile": true, "Address": true, "Bank account": true, "Demat account": true}

func changeDay(c ChangeRequest) string { return firstOf(dayOf(c.Decided), dayOf(c.At)) }

func (e *dpRuleEnv) frequentChanges() {
	month := e.date[:7]
	per := map[string][]string{}
	contact := map[string]int{}
	for _, c := range e.changes {
		if !modKinds[c.Kind] || !strings.HasPrefix(changeDay(c), month) || changeDay(c) > e.date {
			continue
		}
		per[c.UCC] = append(per[c.UCC], c.Kind)
		if c.Kind == "Email" || c.Kind == "Mobile" {
			contact[c.UCC]++
		}
	}
	for _, u := range sortedKeys(per) {
		lim := e.set.scaleCount(e.set.ModsPerMonth, e.cs[u].Risk)
		if len(per[u]) <= lim {
			continue
		}
		e.raise(Alert{Source: "Internal", Area: e.area(u), Type: "Frequent changes of the client's details", UCC: u, Severity: "Medium",
			Detail: fmt.Sprintf("%d changes approved in %s (%s; %d of e-mail or mobile), more than %d. Confirm each with the client; watch for transfers after a change.", len(per[u]), month, strings.Join(per[u], ", "), contact[u], lim)},
			"MOD:"+u+":"+month)
	}
}

// ---- transfers: income, after changes, dormant, one to many, many to one ----

func (e *dpRuleEnv) transfers() {
	from := addDays(e.date, -dpLookbackDays)
	win := addDays(e.date, -30)
	px, _ := latestDoc[ISINPrices](e.ctx, e.o, e.tenant, cISINPx, e.date)
	// The last transaction on each account before each debit.
	lastOn := map[string]string{}
	type fan struct{ to, from map[string]bool }
	out := map[string]*fan{}
	in := map[string]*fan{}
	month := map[string]money.Dec{}
	monthTx := map[string][]DPTxn{}
	for _, t := range e.txns {
		acct := normDematID(firstOf(t.BOID, primaryDemat(e.cs[t.UCC]).ID))
		prev := lastOn[acct]
		lastOn[acct] = t.Date
		if t.Date < from || t.UCC == "" {
			continue
		}
		debit := t.Qty.Sign() < 0 && dpOffMarket(t)
		if debit {
			// Dormant: no transaction for the dormancy period before it.
			if prev != "" && daysBetween(prev, t.Date) > e.set.DormantDays {
				e.raise(Alert{Source: "Internal", Area: e.area(t.UCC), Type: "Debit from a dormant demat account", UCC: t.UCC, Severity: "High", BOID: acct, TxnFrom: t.Date, TxnTo: t.Date, ISIN: t.ISIN, Qty: t.Qty.Abs().String(), Value: e.txnValue(t, px).Fixed(2),
					Detail: fmt.Sprintf("No transaction on BO %s from %s to %s (%d days), then %s of %s (%s). Confirm the client gave the instruction.", MaskAccount(acct), prev, t.Date, daysBetween(prev, t.Date), t.Qty.Abs().String(), firstOf(t.Symbol, t.ISIN), t.Kind)},
					"DMT:"+t.ID)
			}
			// Soon after a change of details.
			for _, c := range e.changes {
				if c.UCC != t.UCC || !modKinds[c.Kind] {
					continue
				}
				d := changeDay(c)
				if d != "" && d <= t.Date && daysBetween(d, t.Date) <= e.set.AfterModDays {
					e.raise(Alert{Source: "Internal", Area: e.area(t.UCC), Type: "Transfer soon after a change of the client's details", UCC: t.UCC, Severity: "High", BOID: acct, TxnFrom: t.Date, TxnTo: t.Date, ISIN: t.ISIN, Qty: t.Qty.Abs().String(), Value: e.txnValue(t, px).Fixed(2),
						Detail: fmt.Sprintf("%s change approved on %s, then %s of %s (%s) on %s, %d days later. Confirm the change and the instruction with the client on the old and new contacts.", c.Kind, d, t.Qty.Abs().String(), firstOf(t.Symbol, t.ISIN), t.Kind, t.Date, daysBetween(d, t.Date))},
						"TAM:"+t.ID)
					break
				}
			}
		}
		if t.Date < win {
			continue
		}
		if debit || (dpPledgeOut(t) && t.Qty.Sign() != 0) {
			month[t.UCC] = month[t.UCC].Add(e.txnValue(t, px))
			monthTx[t.UCC] = append(monthTx[t.UCC], t)
		}
		if debit && len(t.Counterparty) > 0 {
			ctr := normDematID(t.Counterparty[0])
			if out[acct] == nil {
				out[acct] = &fan{to: map[string]bool{}, from: map[string]bool{}}
			}
			out[acct].to[ctr] = true
			if in[ctr] == nil {
				in[ctr] = &fan{to: map[string]bool{}, from: map[string]bool{}}
			}
			in[ctr].from[acct] = true
		}
	}
	ym := e.date[:7]
	// Off-market debits and pledges against the declared income.
	for _, u := range sortedKeys(month) {
		c := e.cs[u]
		mid := incomeMid[c.Income]
		if mid <= 0 {
			continue
		}
		lim := money.DecInt(mid).MulInt(int64(e.set.IncomeTimes)).Pct(money.DecInt(int64(e.set.riskScale(c.Risk))))
		if month[u].Cmp(lim) <= 0 {
			continue
		}
		ts := monthTx[u]
		isins := map[string]bool{}
		for _, t := range ts {
			isins[t.ISIN] = true
		}
		a := Alert{Source: "Internal", Area: e.area(u), Type: "Off-market transfers or pledges out of line with income", UCC: u, Severity: "High", TxnFrom: ts[0].Date, TxnTo: ts[len(ts)-1].Date, Value: month[u].Fixed(2),
			Detail: fmt.Sprintf("Off-market debits and pledges of ₹%s from %s to %s (%d transactions, %d ISINs) against declared income %s (risk %s). Ask for the source of the securities and the purpose; update the income if it has changed.", formatINR(month[u]), ts[0].Date, ts[len(ts)-1].Date, len(ts), len(isins), c.Income, firstOf(c.Risk, "not set"))}
		if len(isins) == 1 {
			a.ISIN = ts[0].ISIN
		}
		e.raise(a, "OMI:"+u+":"+ym)
	}
	// One account to many, many of the DP's accounts to one.
	for _, acct := range sortedKeys(out) {
		u := e.byBO[acct]
		if lim := e.set.scaleCount(e.set.FanCount, e.cs[u].Risk); len(out[acct].to) >= lim && u != "" {
			e.raise(Alert{Source: "Internal", Area: AreaDP, Type: "Off-market transfers from one account to many", UCC: u, Severity: "Medium", BOID: acct, TxnFrom: win, TxnTo: e.date,
				Detail: fmt.Sprintf("BO %s sent off-market transfers to %d different accounts from %s to %s. Find out who the receivers are and why.", MaskAccount(acct), len(out[acct].to), win, e.date)},
				"OTM:"+acct+":"+ym)
		}
	}
	for _, ctr := range sortedKeys(in) {
		if len(in[ctr].from) < e.set.FanCount {
			continue
		}
		srcs := sortedKeys(in[ctr].from)
		var uccs []string
		for _, s := range srcs {
			if u := e.byBO[s]; u != "" {
				uccs = append(uccs, u)
			}
		}
		if len(uccs) == 0 {
			continue
		}
		e.raise(Alert{Source: "Internal", Area: AreaDP, Type: "Off-market transfers from many accounts to one", UCC: uccs[0], Severity: "Medium", BOID: ctr, TxnFrom: win, TxnTo: e.date,
			Detail: fmt.Sprintf("%d of the DP's accounts (clients %s) sent off-market transfers to BO %s from %s to %s. Find out who holds the receiving account and why.", len(srcs), strings.Join(uccs, ", "), MaskAccount(ctr), win, e.date)},
			"MTO:"+shortHash(ctr)+":"+ym)
	}
}

// ---- reason codes ----

func (e *dpRuleEnv) reasonCodes() {
	ds, _ := docs.List[DIS](e.ctx, e.o.Docs, e.tenant, cDIS, "")
	from := addDays(e.date, -dpLookbackDays)
	for _, d := range ds {
		day := firstOf(d.ExecDate, dayOf(d.Received))
		if d.Kind == "On-market" || d.Status == "Rejected" || day < from || day > e.date {
			continue
		}
		why := ""
		sev := "Medium"
		cons := d.Consideration.Dec()
		switch d.Reason {
		case "Gift", "Donation":
			if d.Consideration > 0 {
				why = fmt.Sprintf("the reason is %s but a consideration of ₹%s is given", strings.ToLower(d.Reason), formatINR(cons))
			}
		case "Sale (off-market)":
			if d.Value.Sign() > 0 && cons.MulInt(2).Cmp(d.Value) < 0 {
				why = fmt.Sprintf("sold off-market for ₹%s, under half the market value ₹%s", formatINR(cons), formatINR(d.Value))
			}
		case "Transfer to own account":
			if to := e.byBO[normDematID(d.ToBO)]; to != "" && to != d.UCC && !strings.EqualFold(e.cs[to].PAN, e.cs[d.UCC].PAN) {
				why, sev = fmt.Sprintf("marked as a transfer to the client's own account, but BO %s is client %s's, with another PAN", MaskAccount(normDematID(d.ToBO)), to), "High"
			}
		}
		if why == "" {
			continue
		}
		a := Alert{Source: "Internal", Area: AreaDP, Type: "Reason code does not fit the off-market instruction", UCC: d.UCC, Severity: sev, BOID: normDematID(d.BOID), TxnFrom: day, TxnTo: day, Value: d.Value.Fixed(2),
			Detail: fmt.Sprintf("DIS %s (slip %d) on %s: %s. Ask the client for the purpose and the documents (gift deed, sale agreement).", d.ID, d.Slip, day, why)}
		if len(d.Lines) == 1 {
			a.ISIN, a.Qty = d.Lines[0].ISIN, d.Lines[0].Qty.String()
		}
		e.raise(a, "RCM:"+d.ID)
	}
}

// ---- money passed through an account ----

func (e *dpRuleEnv) passThrough() {
	from := addDays(e.date, -e.set.PassDays)
	in := map[string]money.Dec{}
	outp := map[string]money.Dec{}
	if rs, err := docs.List[Receipt](e.ctx, e.o.Docs, e.tenant, cReceipt, ""); err == nil {
		for _, r := range rs {
			if r.Status == "Credited" && r.Date > from && r.Date <= e.date && r.UCC != "" {
				in[r.UCC] = in[r.UCC].Add(r.Amount.Dec())
			}
		}
	}
	if bs, err := e.o.BankTxns(e.ctx, e.tenant); err == nil {
		for _, b := range bs {
			if (b.Status == "Matched" || b.Status == "Identified") && b.Date > from && b.Date <= e.date && b.UCC != "" {
				in[b.UCC] = in[b.UCC].Add(b.Amount.Dec())
			}
		}
	}
	if ps, err := docs.List[PayoutRequest](e.ctx, e.o.Docs, e.tenant, cPayout, ""); err == nil {
		for _, p := range ps {
			if p.Status == "Paid" && p.Date > from && p.Date <= e.date && p.UCC != "" {
				outp[p.UCC] = outp[p.UCC].Add(p.Amount.Dec())
			}
		}
	}
	var cand []string
	for _, u := range sortedKeys(in) {
		c := e.cs[u]
		min := money.DecInt(e.set.PassMin).Pct(money.DecInt(int64(e.set.riskScale(c.Risk))))
		if in[u].Cmp(min) < 0 || outp[u].MulInt(100).Cmp(in[u].MulInt(int64(e.set.PassPct))) < 0 {
			continue
		}
		cand = append(cand, u)
	}
	if len(cand) == 0 {
		return
	}
	want := map[string]bool{}
	for _, u := range cand {
		want[u] = true
	}
	turn := map[string]money.Dec{}
	for d := addDays(from, 1); d <= e.date; d = addDays(d, 1) {
		ts, _ := e.o.exchangeTrades(e.ctx, e.tenant, d)
		for _, t := range ts {
			if want[t.UCC] {
				turn[t.UCC] = turn[t.UCC].Add(t.Value())
			}
		}
	}
	for _, u := range cand {
		if turn[u].MulInt(100).Cmp(in[u].MulInt(int64(e.set.TradePct))) >= 0 {
			continue
		}
		e.raise(Alert{Source: "Internal", Area: "", Type: "Money passed through the account (possible mule account)", UCC: u, Severity: "High", TxnFrom: addDays(from, 1), TxnTo: e.date, Value: in[u].Fixed(2),
			Detail: fmt.Sprintf("₹%s paid in and ₹%s paid out from %s to %s, with trades of only ₹%s. Find out whose money it is and who operates the account; an account used by another person to deal is a mule account (SEBI PFUTP reg. 4(1)).", formatINR(in[u]), formatINR(outp[u]), addDays(from, 1), e.date, formatINR(turn[u]))},
			"PTF:"+u+":"+e.date[:7])
	}
}

// ---- staff trades ahead of clients ----

// tradeSecond is a trade's time of day in seconds (-1 unknown).
func tradeSecond(s string) int {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexAny(s, " T"); i >= 0 {
		s = s[i+1:]
	}
	p := strings.Split(s, ":")
	if len(p) < 2 {
		return -1
	}
	h, e1 := strconv.Atoi(p[0])
	m, e2 := strconv.Atoi(p[1])
	sec := 0
	if len(p) > 2 {
		sec, _ = strconv.Atoi(strings.SplitN(p[2], ".", 2)[0])
	}
	if e1 != nil || e2 != nil {
		return -1
	}
	return h*3600 + m*60 + sec
}

func (e *dpRuleEnv) frontRunning(trades []ingest.TradeEvent) {
	accts, _ := docs.List[StaffAccount](e.ctx, e.o.Docs, e.tenant, cTradeAcct, "")
	staff := map[string]string{}
	for _, a := range accts {
		if !a.Outside && a.ClosedOn == "" {
			if _, ok := e.cs[a.Account]; ok {
				staff[a.Account] = a.Employee
			}
		}
	}
	if len(staff) == 0 {
		return
	}
	type ord struct {
		ucc   string
		first int
		value money.Dec
	}
	// Each client's trades in a contract and direction, as one order.
	clients := map[string]map[string]*ord{}
	var st []ingest.TradeEvent
	for _, t := range trades {
		if _, ok := staff[t.UCC]; ok {
			st = append(st, t)
			continue
		}
		k := t.Exchange + "|" + t.Instrument() + "|" + t.Side
		if clients[k] == nil {
			clients[k] = map[string]*ord{}
		}
		o := clients[k][t.UCC]
		sec := tradeSecond(t.TradeTime)
		if o == nil {
			o = &ord{ucc: t.UCC, first: sec}
			clients[k][t.UCC] = o
		}
		if sec >= 0 && (o.first < 0 || sec < o.first) {
			o.first = sec
		}
		o.value = o.value.Add(t.Value())
	}
	win := e.set.FrontMinutes * 60
	big := money.DecInt(e.set.FrontValue)
	for _, s := range st {
		ss := tradeSecond(s.TradeTime)
		if ss < 0 {
			continue
		}
		k := s.Exchange + "|" + s.Instrument() + "|" + s.Side
		for _, u := range sortedKeys(clients[k]) {
			c := clients[k][u]
			if c.first > ss && c.first-ss <= win && c.value.Cmp(big) >= 0 {
				e.raise(Alert{Source: "Internal", Type: "Staff trade ahead of a client order (possible front running)", UCC: s.UCC, Severity: "High", TxnFrom: e.date, TxnTo: e.date, Value: s.Value().Fixed(2),
					Detail: fmt.Sprintf("Staff account %s (employee %s) %s %d %s at %s, %d minutes before client %s's order of ₹%s in the same contract and direction. Check whether the staff member knew of the client's order.", s.UCC, staff[s.UCC], map[string]string{"B": "bought", "S": "sold"}[s.Side], s.Qty, InstrumentName(s.Symbol, s.InstrumentType, s.Expiry, s.Strike), s.TradeTime, (c.first-ss+59)/60, u, formatINR(c.value))},
					"FRN:"+e.date+":"+s.UCC+":"+shortHash(k))
				break
			}
		}
	}
}

// ---- connected clients trading against each other ----

func (e *dpRuleEnv) circular(trades []ingest.TradeEvent, gs []sharedGroup) {
	groups := connect(gs)
	of := map[string]string{}
	for _, g := range groups {
		for _, m := range g.Members {
			of[m] = g.ID
		}
	}
	if len(of) == 0 {
		return
	}
	type side struct{ buy, sell map[string]int64 }
	by := map[string]*side{}
	for _, t := range trades {
		g := of[t.UCC]
		if g == "" {
			continue
		}
		k := g + "|" + t.Exchange + "|" + t.Instrument()
		s := by[k]
		if s == nil {
			s = &side{buy: map[string]int64{}, sell: map[string]int64{}}
			by[k] = s
		}
		if t.Side == "B" {
			s.buy[t.UCC] += t.Qty
		} else {
			s.sell[t.UCC] += t.Qty
		}
	}
	for _, k := range sortedKeys(by) {
		s := by[k]
		for _, b := range sortedKeys(s.buy) {
			for _, sl := range sortedKeys(s.sell) {
				if b == sl {
					continue
				}
				bq, sq := s.buy[b], s.sell[sl]
				lo, hi := bq, sq
				if lo > hi {
					lo, hi = hi, lo
				}
				if lo*2 < hi {
					continue
				}
				parts := strings.SplitN(k, "|", 3)
				e.raise(Alert{Source: "Internal", Type: "Connected clients trading against each other (possible circular trading)", UCC: b, Severity: "Medium", TxnFrom: e.date, TxnTo: e.date,
					Detail: fmt.Sprintf("Clients %s (bought %d) and %s (sold %d) share contact or bank details (group %s) and traded %s on %s on %s. Check whether the trades were matched between them.", b, bq, sl, sq, parts[0], strings.ReplaceAll(parts[2], "|", " "), parts[1], e.date)},
					"CIR:"+e.date+":"+shortHash(k+b+sl))
			}
		}
	}
}
