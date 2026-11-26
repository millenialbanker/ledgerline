package ops

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- referral rewards: registered authorised persons only ----
//
// A member may share brokerage, commission or any other consideration for
// bringing in clients only with an authorised person registered with the
// exchange (NSE/INSP/73976, the Inspection master circular, item 4.21;
// MCX/INSP/255/2026, para 9.9(a)). Paying a client for referring another
// client is such a consideration paid to an unregistered person.
//
// So:
//   - a referral reward is earned only when the referrer is an authorised
//     person (Partner kind "Authorised person") that is active and has an
//     exchange registration number;
//   - a client who refers another is still recorded (Client.ReferredBy,
//     ReferrerKind "Client") as the source of the referral, and earns
//     nothing from it;
//   - rewards to client referrers stop from Settings.ReferralAPOnlyFrom
//     (DefaultReferralAPOnlyFrom when empty): brokerage up to the day
//     before still counts, as it was earned under the earlier terms; a
//     work item lists the client referrers whose rewards stop, so the firm
//     can tell them.
//
// An AP's referral reward is commission: it is booked like the AP's
// brokerage share, with TDS on commission, to the AP's account.

// DefaultReferralAPOnlyFrom is the day Ledgerline applies the AP-only rule
// from when the firm has set no other: the day it was built in.
const DefaultReferralAPOnlyFrom = "2026-11-23"

// Referrer kinds.
const (
	ReferrerAP     = "AP"
	ReferrerClient = "Client"
)

func (s Settings) referralPct() money.Dec {
	if d, err := money.ParseDec(s.ReferralPct); err == nil && s.ReferralPct != "" {
		return d
	}
	return money.DecInt(10)
}

// referralCutoff is the first day client referrers earn nothing.
func (s Settings) referralCutoff() string {
	if validDate(s.ReferralAPOnlyFrom) {
		return s.ReferralAPOnlyFrom
	}
	return DefaultReferralAPOnlyFrom
}

// registeredAP reports whether a partner may earn a referral reward: an
// active authorised person with an exchange registration.
func registeredAP(p Partner) bool {
	if p.Kind != RoleAP || !p.Active {
		return false
	}
	if strings.TrimSpace(p.RegNo) != "" {
		return true
	}
	for _, v := range p.ExchangeRegs {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// referrerKind says what a referrer is: a registered AP, or a client
// (recorded as the source only). A partner that is not a registered AP
// cannot be a referrer: staff and unregistered intermediaries may not be
// paid for clients, and a relationship manager is mapped as the RM.
func (o *Ops) referrerKind(ctx context.Context, tenant, code string) (string, error) {
	if code == "" {
		return "", nil
	}
	if p, _, err := docs.Get[Partner](ctx, o.Docs, tenant, cPartner, code); err == nil {
		if !registeredAP(p) {
			return "", invalid("%s is not an active authorised person with an exchange registration: only a registered AP may be a paid referrer (NSE inspection master circular item 4.21; MCX/INSP/255/2026 para 9.9(a)). Map a relationship manager as the RM instead", code)
		}
		return ReferrerAP, nil
	}
	if _, err := o.Client(ctx, tenant, code); err != nil {
		return "", invalid("referrer %s is neither a client nor an authorised person", code)
	}
	return ReferrerClient, nil
}

// clientReferrer: the client's referrer is a client (or, on an older
// record, not a registered AP).
func clientReferrer(c Client, ps map[string]Partner) bool {
	if c.ReferrerKind == ReferrerAP {
		p, ok := ps[c.ReferredBy]
		return !ok || !registeredAP(p)
	}
	return true
}

// ReferralRow is a referrer's reward for a period.
type ReferralRow struct {
	Referrer  string       `json:"referrer"`
	Name      string       `json:"name"`
	Kind      string       `json:"kind"` // AP or Client
	Referred  []string     `json:"referred"`
	Brokerage money.Amount `json:"brokerage"`
	Reward    money.Amount `json:"reward"`
	TDS       money.Amount `json:"tds,omitempty"` // an AP's reward is commission
	Status    string       `json:"status"`        // To post, Posted, No reward (client referrer)
	Note      string       `json:"note,omitempty"`
}

// Referrals computes referral rewards for from..to: the firm's referral
// percentage of brokerage on referred clients, within the referral period,
// for registered APs; client referrers only for brokerage before the
// AP-only rule's cut-off.
func (o *Ops) Referrals(ctx context.Context, tenant, from, to string) ([]ReferralRow, error) {
	if !ledger.ValidDate(from) || !ledger.ValidDate(to) || from > to {
		return nil, invalid("give a period: from on or before to")
	}
	set, _ := o.Settings(ctx, tenant)
	cs, _ := o.ClientMap(ctx, tenant)
	ps := o.partnerMap(ctx, tenant)
	brok, err := o.clientBrokerage(ctx, tenant, from, to)
	if err != nil {
		return nil, err
	}
	cut := set.referralCutoff()
	// Client referrers earn on brokerage before the cut-off only.
	var early map[string]money.Amount
	if from < cut {
		if early, err = o.clientBrokerage(ctx, tenant, from, min(to, addDays(cut, -1))); err != nil {
			return nil, err
		}
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
			r = &ReferralRow{Referrer: c.ReferredBy, Kind: ReferrerAP, Referred: []string{}, Status: "To post"}
			if clientReferrer(c, ps) {
				r.Kind, r.Name = ReferrerClient, cs[c.ReferredBy].Name
			} else {
				r.Name = ps[c.ReferredBy].Name
			}
			by[c.ReferredBy] = r
		}
		r.Referred = append(r.Referred, u)
		if r.Kind == ReferrerClient {
			r.Brokerage += early[u]
		} else {
			r.Brokerage += brok[u]
		}
	}
	var out []ReferralRow
	for _, k := range sortedKeys(by) {
		r := by[k]
		r.Reward = r.Brokerage.Dec().Pct(set.referralPct()).Round(money.UnitPaisa)
		if r.Kind == ReferrerAP {
			r.TDS = r.Reward.Dec().Pct(set.tdsRate()).Round(money.UnitRupee)
		}
		if r.Kind == ReferrerClient {
			switch {
			case to >= cut && r.Reward == 0:
				r.Status = "No reward (client referrer)"
				r.Note = "From " + fmtDay(cut) + " only a registered authorised person earns a referral reward; the client is recorded as the source."
			case to >= cut:
				r.Note = "Only brokerage before " + fmtDay(cut) + " counts: from then only a registered authorised person earns a referral reward."
			}
		}
		if _, _, err := docs.Get[map[string]string](ctx, o.Docs, tenant, cReferral, k+"|"+from+"|"+to); err == nil {
			r.Status = "Posted"
		}
		out = append(out, *r)
	}
	return out, nil
}

// partnerMap is every partner by code.
func (o *Ops) partnerMap(ctx context.Context, tenant string) map[string]Partner {
	ps, _ := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
	m := map[string]Partner{}
	for _, p := range ps {
		m[p.Code] = p
	}
	return m
}

// PostReferrals books the period's referral rewards: to a client
// referrer's trading ledger (brokerage before the cut-off only), and to a
// registered AP's account as commission with TDS.
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
		narr := fmt.Sprintf("Referral reward %s to %s on clients %s", from, to, strings.Join(r.Referred, ", "))
		post := []ledger.Posting{{Account: ExpenseReferral, Debit: r.Reward}, {Account: ledger.ClientAccount(r.Referrer), Credit: r.Reward, Memo: "Referral reward"}}
		if r.Kind == ReferrerAP {
			post = []ledger.Posting{{Account: ExpenseReferral, Debit: r.Reward, Memo: "Referral commission"}}
			if r.TDS > 0 {
				post = append(post, ledger.Posting{Account: PayableTDS194H, Credit: r.TDS, Memo: "TDS u/s " + strings.TrimPrefix(tdsRef("194H", to), "section ")})
			}
			post = append(post, ledger.Posting{Account: "AP:" + r.Referrer, Credit: r.Reward - r.TDS, Memo: "Referral commission"})
		}
		if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "REF:" + r.Referrer + ":" + from + ":" + to, ValueDate: to, CreatedBy: actor,
			Narration: narr, Source: ledger.SourceRef{Kind: "REFERRAL", ID: r.Referrer + "/" + from + "/" + to}, Postings: post}); err != nil {
			return n, err
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cReferral, r.Referrer+"|"+from+"|"+to, map[string]string{"by": actor}, actor)
		n++
	}
	o.log(ctx, tenant, actor, "referrals.posted", from+"/"+to, map[string]any{"referrers": n})
	return n, nil
}

// flagClientReferrers raises, once from the cut-off, a work item listing
// the client referrers whose rewards stop, for the firm to tell them.
func (o *Ops) flagClientReferrers(ctx context.Context, tenant, date string) int {
	set, _ := o.Settings(ctx, tenant)
	cut := set.referralCutoff()
	if date < cut {
		return 0
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return 0
	}
	ps := o.partnerMap(ctx, tenant)
	months := set.ReferralMonths
	if months <= 0 {
		months = 12
	}
	by := map[string][]string{}
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.ReferredBy == "" || !clientReferrer(c, ps) {
			continue
		}
		// Still within the referral period at the cut-off: a reward that
		// would have run on.
		if c.ReferredFrom != "" && addMonths(c.ReferredFrom, int(months)) < cut {
			continue
		}
		by[c.ReferredBy] = append(by[c.ReferredBy], u)
	}
	if len(by) == 0 {
		return 0
	}
	var lines []string
	for _, r := range sortedKeys(by) {
		lines = append(lines, fmt.Sprintf("%s %s: referred %s", r, cs[r].Name, strings.Join(by[r], ", ")))
	}
	o.raiseWork(ctx, tenant, "referrals", "client-referrers:"+cut, fmt.Sprintf("Referral rewards to %d client referrers stop from %s", len(by), fmtDay(cut)),
		"Only a registered authorised person may be paid for bringing in clients (NSE inspection master circular item 4.21; MCX/INSP/255/2026 para 9.9(a)). From "+fmtDay(cut)+" these clients stay recorded as the source of their referrals and earn no reward; brokerage before then is still paid on the next posting. Tell each of them, then close this item. "+strings.Join(lines, "; ")+".",
		"", RoleCompliance, "partners", date)
	return len(by)
}
