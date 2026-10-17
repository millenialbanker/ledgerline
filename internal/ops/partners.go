package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- branches, authorised persons, revenue sharing, referrals ----
//
// Branches are the firm's own offices. Partners are authorised persons
// (APs, registered with the exchanges, who bring and service clients for a
// share of brokerage), relationship managers (employees, for hierarchy and
// performance), and referrers. Partners form a hierarchy: an RM reports to a
// branch head; an AP may sit under an RM.
//
// An AP's share is a percentage of the brokerage earned on its clients.
// The firm holds back an AP's payout to the extent of its clients' overdue
// debits (the AP answers for them). TDS under section 194H is deducted; if
// the AP is GST-registered, GST is added and the firm takes input credit.
//
// A client may be referred by another client: the referrer earns a share of
// the brokerage on the referred client's trades for a period, credited to
// the referrer's trading ledger.

const (
	cBranch   = "branch"
	cPartner  = "partner"
	cAPPayout = "ap_payout"
	cReferral = "referral_run"
	// PayableTDS194H is tax deducted at source on commission.
	PayableTDS194H = "PAYABLE:TDS_194H"
	// ExpenseAPShare is brokerage shared with authorised persons.
	ExpenseAPShare = "EXPENSE:AP_SHARE"
	// ExpenseReferral is referral rewards to clients.
	ExpenseReferral = "EXPENSE:REFERRAL"
)

// Partner roles.
const (
	RoleBranch = "Branch staff"
	RoleAP     = "Authorised person"
)

// Branch is an office of the firm.
type Branch struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	City    string `json:"city"`
	Manager string `json:"manager,omitempty"`
	Active  bool   `json:"active"`
}

// Partner is an authorised person, relationship manager or referrer.
type Partner struct {
	Code      string       `json:"code"`
	Name      string       `json:"name"`
	Kind      string       `json:"kind"` // Authorised person, Relationship manager
	Branch    string       `json:"branch"`
	Parent    string       `json:"parent,omitempty"`
	PAN       string       `json:"pan,omitempty"`
	GSTIN     string       `json:"gstin,omitempty"`
	RegNo     string       `json:"reg_no,omitempty"` // exchange AP registration
	SharePct  money.Dec    `json:"share_pct"`        // of brokerage on its clients
	Bank      string       `json:"bank,omitempty"`
	Account   string       `json:"account,omitempty"`
	IFSC      string       `json:"ifsc,omitempty"`
	Email     string       `json:"email,omitempty"`
	Active    bool         `json:"active"`
	Clients   int          `json:"clients"`
	Downline  []string     `json:"downline"`
	Brokerage money.Amount `json:"brokerage"`      // this financial year, own clients
	Team      money.Amount `json:"team_brokerage"` // own and downline
}

// PartnerKinds lists partner kinds.
var PartnerKinds = []string{"Authorised person", "Relationship manager"}

// Branches lists branches.
func (o *Ops) Branches(ctx context.Context, tenant string) ([]Branch, error) {
	return docs.List[Branch](ctx, o.Docs, tenant, cBranch, "")
}

// SaveBranch adds or changes a branch.
func (o *Ops) SaveBranch(ctx context.Context, tenant, actor string, b Branch) (Branch, error) {
	if err := o.require(ctx, tenant, actor, "Managing branches", RoleAdmin, RoleChecker); err != nil {
		return b, err
	}
	b.Code = strings.TrimSpace(b.Code)
	if b.Code == "" || strings.TrimSpace(b.Name) == "" {
		return b, invalid("branch code and name are required")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cBranch, b.Code, b, actor); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "branch.saved", b.Code, map[string]any{"name": b.Name})
	return b, nil
}

// SavePartner adds or changes a partner.
func (o *Ops) SavePartner(ctx context.Context, tenant, actor string, p Partner) (Partner, error) {
	if err := o.require(ctx, tenant, actor, "Managing partners", RoleAdmin, RoleChecker); err != nil {
		return p, err
	}
	p.Code = strings.ToUpper(strings.TrimSpace(p.Code))
	if !reUCC.MatchString(p.Code) {
		return p, invalid("partner code must be 3–10 letters or digits")
	}
	ok := false
	for _, k := range PartnerKinds {
		ok = ok || k == p.Kind
	}
	if !ok || strings.TrimSpace(p.Name) == "" {
		return p, invalid("name and kind (Authorised person or Relationship manager) are required")
	}
	if p.SharePct.Sign() < 0 || p.SharePct.Cmp(money.DecInt(100)) > 0 {
		return p, invalid("share must be between 0 and 100%")
	}
	if p.PAN != "" && !rePAN.MatchString(strings.ToUpper(p.PAN)) {
		return p, invalid("PAN is not valid")
	}
	if p.GSTIN != "" && !reGSTIN.MatchString(strings.ToUpper(p.GSTIN)) {
		return p, invalid("GSTIN is not valid")
	}
	p.PAN, p.GSTIN = strings.ToUpper(p.PAN), strings.ToUpper(p.GSTIN)
	if p.Parent == p.Code {
		return p, invalid("a partner cannot report to itself")
	}
	// No cycles in the hierarchy.
	all, _ := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
	parent := map[string]string{}
	for _, x := range all {
		parent[x.Code] = x.Parent
	}
	parent[p.Code] = p.Parent
	for cur, n := p.Parent, 0; cur != ""; cur, n = parent[cur], n+1 {
		if cur == p.Code || n > 50 {
			return p, invalid("that reporting line would make a loop")
		}
	}
	p.Clients, p.Downline, p.Brokerage, p.Team = 0, nil, 0, 0
	if err := docs.Upsert(ctx, o.Docs, tenant, cPartner, p.Code, p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "partner.saved", p.Code, map[string]any{"kind": p.Kind, "share_pct": p.SharePct.String()})
	return p, nil
}

// clientBrokerage sums brokerage earned per client in from..to from bills
// (net of reversals).
func (o *Ops) clientBrokerage(ctx context.Context, tenant, from, to string) (map[string]money.Amount, error) {
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{From: from, To: to})
	if err != nil {
		return nil, err
	}
	all, _ := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{})
	byID := map[string]ledger.Entry{}
	for _, e := range all {
		byID[e.ID] = e
	}
	out := map[string]money.Amount{}
	for _, e := range es {
		src := e
		if e.ReversalOf != "" {
			src = byID[e.ReversalOf]
		}
		if src.Source.Kind != "BILL" {
			continue
		}
		p := strings.Split(src.Source.ID, "/")
		if len(p) < 2 {
			continue
		}
		for _, x := range e.Postings {
			if x.Account == ledger.IncomeBrokerage {
				out[p[1]] += x.Credit - x.Debit
			}
		}
	}
	return out, nil
}

// Partners lists partners with client counts, hierarchy and brokerage for
// the financial year to date.
func (o *Ops) Partners(ctx context.Context, tenant, date string) ([]Partner, error) {
	ps, err := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
	if err != nil {
		return nil, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	brok, _ := o.clientBrokerage(ctx, tenant, fyStartOf(date), date)
	idx := map[string]*Partner{}
	for i := range ps {
		ps[i].Downline = []string{}
		idx[ps[i].Code] = &ps[i]
	}
	for _, c := range cs {
		for _, code := range []string{c.Partner, c.RM} {
			if p := idx[code]; p != nil {
				p.Clients++
				p.Brokerage += brok[c.UCC]
			}
		}
	}
	for i := range ps {
		if p := idx[ps[i].Parent]; p != nil {
			p.Downline = append(p.Downline, ps[i].Code)
		}
	}
	var team func(code string, seen map[string]bool) money.Amount
	team = func(code string, seen map[string]bool) money.Amount {
		if seen[code] {
			return 0
		}
		seen[code] = true
		p := idx[code]
		t := p.Brokerage
		for _, d := range p.Downline {
			t += team(d, seen)
		}
		return t
	}
	for i := range ps {
		ps[i].Team = team(ps[i].Code, map[string]bool{})
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Code < ps[j].Code })
	return ps, nil
}

func fyStartOf(d string) string {
	fy := finYear(d)
	if fy == "" {
		return d
	}
	return fy[:4] + "-04-01"
}

// AssignClient sets a client's branch, authorised person, relationship
// manager and referrer.
func (o *Ops) AssignClient(ctx context.Context, tenant, actor, ucc, branch, partner, rm, referrer, from string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Mapping clients", RoleChecker, RoleAdmin); err != nil {
		return Client{}, err
	}
	ucc, referrer = strings.ToUpper(ucc), strings.ToUpper(strings.TrimSpace(referrer))
	if referrer == ucc && referrer != "" {
		return Client{}, invalid("a client cannot refer themselves")
	}
	if referrer != "" {
		if _, err := o.Client(ctx, tenant, referrer); err != nil {
			return Client{}, invalid("referring client %s not found", referrer)
		}
	}
	for _, code := range []string{partner, rm} {
		if code == "" {
			continue
		}
		if _, _, err := docs.Get[Partner](ctx, o.Docs, tenant, cPartner, code); err != nil {
			return Client{}, invalid("partner %s not found", code)
		}
	}
	if from != "" && !ledger.ValidDate(from) {
		return Client{}, invalid("referral start must be YYYY-MM-DD")
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, ucc, actor, func(x *Client) error {
		x.Branch, x.Partner, x.RM = branch, strings.ToUpper(partner), strings.ToUpper(rm)
		if x.ReferredBy != referrer {
			x.ReferredBy, x.ReferredFrom = referrer, from
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "client.mapped", ucc, map[string]any{"branch": branch, "partner": partner, "rm": rm, "referrer": referrer})
	}
	return c, err
}

// APPayout is an authorised person's share for a period.
type APPayout struct {
	ID        string          `json:"id"`
	Partner   string          `json:"partner"`
	Name      string          `json:"name"`
	From      string          `json:"from"`
	To        string          `json:"to"`
	Brokerage money.Amount    `json:"brokerage"`
	SharePct  money.Dec       `json:"share_pct"`
	Share     money.Amount    `json:"share"`
	GST       money.Amount    `json:"gst"`
	TDS       money.Amount    `json:"tds"`
	Withheld  money.Amount    `json:"withheld"` // held back against clients' overdue debits
	Payable   money.Amount    `json:"payable"`  // share + GST − TDS − withheld
	Status    string          `json:"status"`   // Computed, Posted
	Entry     string          `json:"entry,omitempty"`
	Clients   []APClientShare `json:"clients"`
}

// APClientShare is one client's contribution to an AP payout.
type APClientShare struct {
	UCC       string       `json:"ucc"`
	Name      string       `json:"name"`
	Brokerage money.Amount `json:"brokerage"`
	Debit     money.Amount `json:"debit"`
	DebitDays int          `json:"debit_days"`
}

func (s Settings) tdsRate() money.Dec {
	if d, err := money.ParseDec(s.TDSCommissionPct); err == nil && s.TDSCommissionPct != "" {
		return d
	}
	return money.DecInt(2) // section 194H from 1 October 2024
}

// APPayouts computes each authorised person's share for from..to, merged
// with payouts already posted for that period.
func (o *Ops) APPayouts(ctx context.Context, tenant, from, to string) ([]APPayout, error) {
	if !ledger.ValidDate(from) || !ledger.ValidDate(to) || from > to {
		return nil, invalid("give a period: from on or before to")
	}
	set, _ := o.Settings(ctx, tenant)
	ps, err := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
	if err != nil {
		return nil, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	brok, err := o.clientBrokerage(ctx, tenant, from, to)
	if err != nil {
		return nil, err
	}
	deb, _ := o.Debits(ctx, tenant, to)
	dmap := map[string]DebitRow{}
	for _, r := range deb.Rows {
		dmap[r.UCC] = r
	}
	var out []APPayout
	for _, p := range ps {
		if p.Kind != "Authorised person" {
			continue
		}
		id := p.Code + "|" + from + "|" + to
		if done, _, err := docs.Get[APPayout](ctx, o.Docs, tenant, cAPPayout, id); err == nil {
			out = append(out, done)
			continue
		}
		a := APPayout{ID: id, Partner: p.Code, Name: p.Name, From: from, To: to, SharePct: p.SharePct, Status: "Computed", Clients: []APClientShare{}}
		var owed money.Amount
		for _, u := range sortedKeys(cs) {
			c := cs[u]
			if c.Partner != p.Code {
				continue
			}
			d := dmap[u]
			if brok[u] == 0 && d.Debit == 0 {
				continue
			}
			a.Clients = append(a.Clients, APClientShare{UCC: u, Name: c.Name, Brokerage: brok[u], Debit: d.Debit, DebitDays: d.Days})
			a.Brokerage += brok[u]
			if d.Debit > 0 && d.Days >= deb.BlockDays {
				owed += d.Debit
			}
		}
		a.Share = a.Brokerage.Dec().Pct(p.SharePct).Round(money.UnitPaisa)
		if p.GSTIN != "" {
			a.GST = a.Share.Dec().Pct(set.gstRate()).Round(money.UnitPaisa)
		}
		a.TDS = a.Share.Dec().Pct(set.tdsRate()).Round(money.UnitRupee)
		net := a.Share + a.GST - a.TDS
		a.Withheld = min(max(net, 0), owed)
		a.Payable = net - a.Withheld
		out = append(out, a)
	}
	return out, nil
}

// PostAPPayouts books the period's AP shares: expense, input GST, TDS
// payable, and the net credited to each AP's account. The withheld part
// stays in the AP's account, marked as held.
func (o *Ops) PostAPPayouts(ctx context.Context, tenant, actor, from, to string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Posting AP payouts", RoleChecker); err != nil {
		return 0, err
	}
	ps, err := o.APPayouts(ctx, tenant, from, to)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, a := range ps {
		if a.Status != "Computed" || a.Share <= 0 {
			continue
		}
		post := []ledger.Posting{{Account: ExpenseAPShare, Debit: a.Share, Memo: fmt.Sprintf("%s%% of ₹%s brokerage", a.SharePct, a.Brokerage)}}
		if a.GST > 0 {
			post = append(post, ledger.Posting{Account: InputIGST, Debit: a.GST, Memo: "GST on AP commission"})
		}
		if a.TDS > 0 {
			post = append(post, ledger.Posting{Account: PayableTDS194H, Credit: a.TDS, Memo: "TDS u/s 194H"})
		}
		post = append(post, ledger.Posting{Account: "AP:" + a.Partner, Credit: a.Share + a.GST - a.TDS, Memo: fmt.Sprintf("Payable now ₹%s, held ₹%s", a.Payable, a.Withheld)})
		e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "APS:" + a.ID, ValueDate: to, CreatedBy: actor,
			Narration: fmt.Sprintf("Brokerage share %s %s to %s", a.Partner, from, to), Source: ledger.SourceRef{Kind: "AP_SHARE", ID: a.ID}, Postings: post})
		if err != nil {
			return n, err
		}
		a.Status, a.Entry = "Posted", e.ID
		if err := docs.Upsert(ctx, o.Docs, tenant, cAPPayout, a.ID, a, actor); err != nil {
			return n, err
		}
		n++
	}
	o.log(ctx, tenant, actor, "ap.payouts.posted", from+"/"+to, map[string]any{"partners": n})
	return n, nil
}

// ReferralRow is a referrer's reward for a period.
type ReferralRow struct {
	Referrer  string       `json:"referrer"`
	Name      string       `json:"name"`
	Referred  []string     `json:"referred"`
	Brokerage money.Amount `json:"brokerage"`
	Reward    money.Amount `json:"reward"`
	Status    string       `json:"status"` // To post, Posted
}

func (s Settings) referralPct() money.Dec {
	if d, err := money.ParseDec(s.ReferralPct); err == nil && s.ReferralPct != "" {
		return d
	}
	return money.DecInt(10)
}

// Referrals computes referral rewards for from..to: the firm's referral
// percentage of brokerage on referred clients, within the referral period.
func (o *Ops) Referrals(ctx context.Context, tenant, from, to string) ([]ReferralRow, error) {
	set, _ := o.Settings(ctx, tenant)
	cs, _ := o.ClientMap(ctx, tenant)
	brok, err := o.clientBrokerage(ctx, tenant, from, to)
	if err != nil {
		return nil, err
	}
	months := set.ReferralMonths
	if months <= 0 {
		months = 12
	}
	by := map[string]*ReferralRow{}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.ReferredBy == "" || brok[u] == 0 {
			continue
		}
		if c.ReferredFrom != "" && (to < c.ReferredFrom || from > addMonths(c.ReferredFrom, int(months))) {
			continue
		}
		r := by[c.ReferredBy]
		if r == nil {
			r = &ReferralRow{Referrer: c.ReferredBy, Name: cs[c.ReferredBy].Name, Referred: []string{}, Status: "To post"}
			by[c.ReferredBy] = r
		}
		r.Referred = append(r.Referred, u)
		r.Brokerage += brok[u]
	}
	var out []ReferralRow
	for _, k := range sortedKeys(by) {
		r := by[k]
		r.Reward = r.Brokerage.Dec().Pct(set.referralPct()).Round(money.UnitPaisa)
		if _, _, err := docs.Get[map[string]string](ctx, o.Docs, tenant, cReferral, k+"|"+from+"|"+to); err == nil {
			r.Status = "Posted"
		}
		out = append(out, *r)
	}
	return out, nil
}

// PostReferrals credits referral rewards to the referrers' ledgers.
func (o *Ops) PostReferrals(ctx context.Context, tenant, actor, from, to string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Posting referral rewards", RoleChecker); err != nil {
		return 0, err
	}
	rs, err := o.Referrals(ctx, tenant, from, to)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rs {
		if r.Status != "To post" || r.Reward <= 0 {
			continue
		}
		if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "REF:" + r.Referrer + ":" + from + ":" + to, ValueDate: to, CreatedBy: actor,
			Narration: fmt.Sprintf("Referral reward %s to %s on clients %s", from, to, strings.Join(r.Referred, ", ")), Source: ledger.SourceRef{Kind: "REFERRAL", ID: r.Referrer + "/" + from + "/" + to},
			Postings: []ledger.Posting{{Account: ExpenseReferral, Debit: r.Reward}, {Account: ledger.ClientAccount(r.Referrer), Credit: r.Reward, Memo: "Referral reward"}}}); err != nil {
			return n, err
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cReferral, r.Referrer+"|"+from+"|"+to, map[string]string{"by": actor}, actor)
		n++
	}
	o.log(ctx, tenant, actor, "referrals.posted", from+"/"+to, map[string]any{"referrers": n})
	return n, nil
}

// ---- data scope: who may see which clients ----

// ClientScope returns a check for whether actor may see a client, or nil
// when the actor sees every client. Branch staff see their branch's
// clients; authorised persons see their own clients (and their downline's);
// private clients are visible only to admins, compliance and named users.
func (o *Ops) ClientScope(ctx context.Context, tenant, actor string) func(ucc string) bool {
	u, err := o.user(ctx, tenant, actor)
	cs, _ := o.ClientMap(ctx, tenant)
	private := false
	for _, c := range cs {
		private = private || c.Private
	}
	if err != nil {
		if !private {
			return nil // API keys and single-user setups
		}
		u = User{ID: actor, Role: RoleMaker}
	}
	if u.Role == RoleClient {
		own := map[string]bool{}
		for _, c := range u.Clients {
			own[strings.ToUpper(c)] = true
		}
		return func(ucc string) bool { return own[strings.ToUpper(ucc)] }
	}
	scoped := u.Role == RoleBranch || u.Role == RoleAP
	if !scoped && (!private || u.Role == RoleAdmin || u.Role == RoleCompliance) {
		return nil
	}
	partners := map[string]bool{}
	if u.Role == RoleAP {
		ps, _ := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
		kids := map[string][]string{}
		for _, p := range ps {
			kids[p.Parent] = append(kids[p.Parent], p.Code)
		}
		var walk func(string)
		walk = func(c string) {
			if partners[c] {
				return
			}
			partners[c] = true
			for _, k := range kids[c] {
				walk(k)
			}
		}
		if u.Partner != "" {
			walk(u.Partner)
		}
	}
	return func(ucc string) bool {
		c, ok := cs[strings.ToUpper(ucc)]
		if !ok {
			return !scoped
		}
		if c.Private && u.Role != RoleAdmin && u.Role != RoleCompliance {
			allowed := false
			for _, x := range c.VisibleTo {
				allowed = allowed || x == u.ID
			}
			if !allowed {
				return false
			}
		}
		switch u.Role {
		case RoleBranch:
			return u.Branch != "" && c.Branch == u.Branch
		case RoleAP:
			return partners[c.Partner] || partners[c.RM]
		}
		return true
	}
}

// SetClientPrivacy marks a client private (visible only to admins,
// compliance and the named users) or public again.
func (o *Ops) SetClientPrivacy(ctx context.Context, tenant, actor, ucc string, private bool, visibleTo []string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Restricting client data", RoleAdmin, RoleCompliance); err != nil {
		return Client{}, err
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(x *Client) error {
		x.Private, x.VisibleTo = private, visibleTo
		if !private {
			x.VisibleTo = nil
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "client.privacy", c.UCC, map[string]any{"private": private, "visible_to": visibleTo})
	}
	return c, err
}
