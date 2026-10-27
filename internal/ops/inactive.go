package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- inactive trading accounts ----
//
// Exchange circulars of October 2024 (NSE NSE/INSP/64718 of 25-Oct-2024,
// with matching BSE and MCX circulars) define an inactive trading account as
// one with no activity through the member for 24 months. Activity is:
//
//   - a trade on any exchange or segment, or a bid in an OFS, buy-back or
//     open offer;
//   - an IPO bid that was not cancelled, an SGB application, or a mutual
//     fund lumpsum or SIP purchase on an exchange platform;
//   - a change of email, mobile or address uploaded to the KRA.
//
// An inactive account is flagged "Inactive" in every exchange's UCC
// database. It may not trade until reactivated: in-person or video
// verification, confirmation (or update) of address, mobile, email, bank,
// demat and income details, and a validated or registered KRA status. The
// 24 months restart from reactivation. Members may tell clients before
// flagging but must not ask them to trade to avoid it. Funds and securities
// of inactive clients are still returned through running account
// settlement; inactive clients with nil balances drop out of the holding and
// collateral reporting.

const cReactivation = "reactivation"

// DefaultInactiveMonths is the inactivity period in the 2024 circulars.
const DefaultInactiveMonths = 24

// ActivityRow is a client's activity and inactivity status.
type ActivityRow struct {
	UCC           string    `json:"ucc"`
	Name          string    `json:"name"`
	Tags          []string  `json:"tags"`
	LastActivity  string    `json:"last_activity"`
	ActivityKind  string    `json:"activity_kind"`
	InactiveFrom  string    `json:"inactive_from"` // date the account becomes due to be flagged
	DaysLeft      int       `json:"days_left"`
	State         string    `json:"state"` // Active, Approaching, Due, Inactive, Reactivation pending
	InactiveSince string    `json:"inactive_since,omitempty"`
	Funds         money.Dec `json:"funds"`
	Securities    money.Dec `json:"securities"`
	Holdings      int       `json:"holdings"`
}

// Reactivation is a request to reactivate an inactive account.
type Reactivation struct {
	ID             string `json:"id"`
	UCC            string `json:"ucc"`
	Name           string `json:"name"`
	Date           string `json:"date"`
	IPVMode        string `json:"ipv_mode"` // IPV or VIPV
	IPVDate        string `json:"ipv_date"`
	DetailsChanged bool   `json:"details_changed"`
	Email          string `json:"email,omitempty"`
	Mobile         string `json:"mobile,omitempty"`
	City           string `json:"city,omitempty"`
	KRAStatus      string `json:"kra_status"`
	Note           string `json:"note,omitempty"`
	Maker          string `json:"maker"`
	Checker        string `json:"checker,omitempty"`
	Status         string `json:"status"` // Pending, Approved, Rejected
	Decided        string `json:"decided,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// DormancyView is the inactive-accounts screen.
type DormancyView struct {
	Date     string         `json:"date"`
	Months   int64          `json:"months"`
	Cutoff   string         `json:"cutoff"` // last activity on or before this date is due
	Rows     []ActivityRow  `json:"rows"`
	Requests []Reactivation `json:"requests"`
	Counts   map[string]int `json:"counts"`
}

func (o *Ops) inactiveMonths(set Settings) int64 {
	if set.InactiveMonths > 0 {
		return set.InactiveMonths
	}
	return DefaultInactiveMonths
}

func addMonths(d string, n int) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.AddDate(0, n, 0).Format("2006-01-02")
}

func (o *Ops) fileDate(date string) string {
	if date != "" {
		return date
	}
	return o.now().Format("2006-01-02")
}

// lastActivity finds each client's latest qualifying activity on or before
// date, and what it was.
func (o *Ops) lastActivity(ctx context.Context, tenant, date string, cs map[string]Client) (map[string][2]string, error) {
	out := map[string][2]string{}
	see := func(u, d, kind string) {
		if d == "" || d > date {
			return
		}
		if d > out[u][0] {
			out[u] = [2]string{d, kind}
		}
	}
	for u, c := range cs {
		see(u, c.LastTrade, "Trade")
		see(u, c.KYCUpdatedOn, "KYC update")
		see(u, c.ReactivatedOn, "Reactivation")
		see(u, c.KYCDate, "Account opened")
	}
	ts, err := o.App.Trades.List(ctx, tenant, "EXCHANGE", "")
	if err != nil {
		return nil, err
	}
	if o.App.Adjust != nil {
		if adj, err := o.App.Adjust(ctx, tenant, ts); err == nil {
			ts = adj
		}
	}
	for _, t := range ts {
		see(t.UCC, t.TradeDate, "Trade")
	}
	ipos, _ := o.IPOApps(ctx, tenant)
	for _, a := range ipos {
		if a.Mandate == "Accepted" && !strings.HasPrefix(a.Allotment, "Rejected") && !strings.HasPrefix(a.Allotment, "Cancelled") {
			see(a.UCC, a.Date, "IPO bid")
		}
	}
	mfs, _ := o.MFOrders(ctx, tenant)
	for _, m := range mfs {
		if (m.Type == "SIP" || m.Type == "Lumpsum") && (m.Status == "Placed" || m.Status == "Allotted") {
			see(m.UCC, m.Date, "Mutual fund "+strings.ToLower(m.Type))
		}
	}
	bbs, _ := o.BuyBacks(ctx, tenant)
	for _, b := range bbs {
		for _, x := range b.Bids {
			if x.Status != "Cancelled" {
				see(x.UCC, x.Date, "Buy-back tender")
			}
		}
	}
	return out, nil
}

// Dormancy lists every active-stage client's activity and inactivity state
// as of date.
func (o *Ops) Dormancy(ctx context.Context, tenant, date string) (DormancyView, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return DormancyView{}, err
	}
	cm, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return DormancyView{}, err
	}
	months := o.inactiveMonths(set)
	v := DormancyView{Date: date, Months: months, Cutoff: addMonths(date, -int(months)), Rows: []ActivityRow{}, Counts: map[string]int{}}
	last, err := o.lastActivity(ctx, tenant, date, cm)
	if err != nil {
		return v, err
	}
	funds, err := o.clientFunds(ctx, tenant, date, "")
	if err != nil {
		return v, err
	}
	hs, _ := o.Holdings(ctx, tenant, "")
	_, look, _ := o.Market(ctx, tenant, date)
	sec := map[string]money.Dec{}
	nh := map[string]int{}
	for _, h := range hs {
		if h.Qty <= 0 {
			continue
		}
		nh[h.UCC]++
		if i, ok := look.ByISIN(h.ISIN); ok {
			sec[h.UCC] = sec[h.UCC].Add(i.LTP.MulInt(h.Qty))
		}
	}
	if v.Requests, err = docs.List[Reactivation](ctx, o.Docs, tenant, cReactivation, ""); err != nil {
		return v, err
	}
	pending := map[string]bool{}
	for _, r := range v.Requests {
		if r.Status == "Pending" {
			pending[r.UCC] = true
		}
	}
	sort.Slice(v.Requests, func(i, j int) bool { return v.Requests[i].ID > v.Requests[j].ID })
	for _, u := range sortedKeys(cm) {
		c := cm[u]
		if c.Status != "Active" {
			continue
		}
		la := last[u]
		r := ActivityRow{UCC: u, Name: c.Name, Tags: c.Tags, LastActivity: la[0], ActivityKind: la[1], InactiveSince: c.InactiveSince,
			Funds: funds[u].Dec(), Securities: sec[u], Holdings: nh[u]}
		if r.Tags == nil {
			r.Tags = []string{}
		}
		if la[0] != "" {
			r.InactiveFrom = addMonths(la[0], int(months))
			r.DaysLeft = daysBetween(date, r.InactiveFrom)
		}
		switch {
		case c.Inactive && pending[u]:
			r.State = "Reactivation pending"
		case c.Inactive:
			r.State = "Inactive"
		case la[0] == "" || la[0] <= v.Cutoff:
			r.State = "Due"
		case r.DaysLeft <= 60:
			r.State = "Approaching"
		default:
			r.State = "Active"
		}
		v.Counts[r.State]++
		if c.Inactive && (r.Funds.Sign() != 0 || r.Holdings > 0) {
			v.Counts["Inactive with assets"]++
		}
		v.Rows = append(v.Rows, r)
	}
	v.Counts["Reactivation pending"] = len(pending)
	return v, nil
}

// FlagInactive marks accounts that are due as Inactive. Each is re-checked
// against the date; the change goes into the day's UCC upload batch.
func (o *Ops) FlagInactive(ctx context.Context, tenant, actor, date string, uccs []string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Flagging accounts inactive", RoleChecker, RoleCompliance); err != nil {
		return 0, err
	}
	v, err := o.Dormancy(ctx, tenant, date)
	if err != nil {
		return 0, err
	}
	due := map[string]ActivityRow{}
	for _, r := range v.Rows {
		if r.State == "Due" {
			due[r.UCC] = r
		}
	}
	n := 0
	for _, u := range uccs {
		u = strings.ToUpper(u)
		r, ok := due[u]
		if !ok {
			return n, invalid("%s is not due to be flagged on %s (last activity %s)", u, date, fmtDay(lastOr(v.Rows, u)))
		}
		if _, err := docs.Update(ctx, o.Docs, tenant, cClient, u, actor, func(c *Client) error {
			c.Inactive, c.InactiveSince = true, date
			return nil
		}); err != nil {
			return n, err
		}
		o.log(ctx, tenant, actor, "client.inactive", u, map[string]any{"last_activity": r.LastActivity, "kind": r.ActivityKind, "months": v.Months})
		n++
	}
	return n, nil
}

func lastOr(rows []ActivityRow, u string) string {
	for _, r := range rows {
		if r.UCC == u {
			return r.LastActivity
		}
	}
	return ""
}

// KRA statuses that allow trading.
var kraOK = map[string]bool{"Validated": true, "Registered": true}

// RequestReactivation records the checks for reactivating an inactive
// account (maker). A checker approves it.
func (o *Ops) RequestReactivation(ctx context.Context, tenant, actor, date string, r Reactivation) (Reactivation, error) {
	if err := o.require(ctx, tenant, actor, "Requesting reactivation", RoleMaker, RoleChecker); err != nil {
		return r, err
	}
	c, err := o.Client(ctx, tenant, strings.ToUpper(r.UCC))
	if err != nil {
		return r, err
	}
	if !c.Inactive {
		return r, invalid("%s is not flagged inactive", c.UCC)
	}
	all, _ := docs.List[Reactivation](ctx, o.Docs, tenant, cReactivation, "")
	for _, x := range all {
		if x.UCC == c.UCC && x.Status == "Pending" {
			return r, invalid("%s already has a reactivation request waiting (%s)", c.UCC, x.ID)
		}
	}
	if r.IPVMode != "IPV" && r.IPVMode != "VIPV" {
		return r, invalid("record whether in-person (IPV) or video (VIPV) verification was done")
	}
	if !validDate(r.IPVDate) || r.IPVDate > date {
		return r, invalid("the verification date must be on or before %s", fmtDay(date))
	}
	if !kraOK[r.KRAStatus] {
		return r, invalid("the KRA status is %q; collect the documents and get it validated or registered before reactivating", r.KRAStatus)
	}
	if r.DetailsChanged {
		upd := Client{Email: r.Email, Mobile: r.Mobile, City: r.City}
		upd.Normalise()
		if upd.Email == "" && upd.Mobile == "" && upd.City == "" {
			return r, invalid("enter the changed details, or record that nothing changed")
		}
		r.Email, r.Mobile, r.City = upd.Email, upd.Mobile, upd.City
	} else {
		r.Email, r.Mobile, r.City = "", "", ""
	}
	seq, err := o.next(ctx, tenant, "reactivation", actor)
	if err != nil {
		return r, err
	}
	r.ID, r.UCC, r.Name, r.Date, r.Maker, r.Status = fmt.Sprintf("RA-%05d", seq), c.UCC, c.Name, date, actor, "Pending"
	r.Note = strings.TrimSpace(r.Note)
	if _, err := docs.Put(ctx, o.Docs, tenant, cReactivation, r.ID, r, 0, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "reactivation.requested", c.UCC, map[string]any{"id": r.ID, "ipv": r.IPVMode, "kra": r.KRAStatus, "details_changed": r.DetailsChanged})
	return r, nil
}

// DecideReactivation approves or rejects a request. The checker must not be
// the maker. Approval clears the Inactive flag (for the next UCC batch),
// applies any changed details, and restarts the 24 months.
func (o *Ops) DecideReactivation(ctx context.Context, tenant, actor, date, id string, approve bool, reason string) (Reactivation, error) {
	if err := o.require(ctx, tenant, actor, "Approving reactivation", RoleChecker, RoleCompliance); err != nil {
		return Reactivation{}, err
	}
	r, err := docs.Update(ctx, o.Docs, tenant, cReactivation, id, actor, func(r *Reactivation) error {
		if r.Status != "Pending" {
			return fmt.Errorf("%w: %s is already %s", ErrState, r.ID, strings.ToLower(r.Status))
		}
		if r.Maker == actor {
			return fmt.Errorf("%w: the person who requested a reactivation cannot approve it", ErrForbidden)
		}
		if !approve && strings.TrimSpace(reason) == "" {
			return invalid("give a reason for rejecting")
		}
		r.Checker, r.Decided, r.Reason = actor, date, strings.TrimSpace(reason)
		r.Status = "Rejected"
		if approve {
			r.Status = "Approved"
		}
		return nil
	})
	if err != nil || !approve {
		if err == nil {
			o.log(ctx, tenant, actor, "reactivation.rejected", r.UCC, map[string]any{"id": id, "reason": reason})
			o.reactivationLetter(ctx, tenant, actor, r)
		}
		return r, err
	}
	if _, err := docs.Update(ctx, o.Docs, tenant, cClient, r.UCC, actor, func(c *Client) error {
		c.Inactive, c.ReactivatedOn = false, date
		if r.DetailsChanged {
			if r.Email != "" {
				c.Email = r.Email
			}
			if r.Mobile != "" {
				c.Mobile = r.Mobile
			}
			if r.City != "" {
				c.City = r.City
			}
			c.KYCUpdatedOn = date
		}
		return nil
	}); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "reactivation.approved", r.UCC, map[string]any{"id": id})
	o.reactivationLetter(ctx, tenant, actor, r)
	return r, nil
}

// UpdateContact records a change of email, mobile or address uploaded to
// the KRA. It counts as activity for the inactivity rule once the KRA shows
// the record as validated or registered.
func (o *Ops) UpdateContact(ctx context.Context, tenant, actor, date, ucc, email, mobile, city, kraStatus string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Updating KYC details", RoleMaker, RoleChecker); err != nil {
		return Client{}, err
	}
	var before Client
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(c *Client) error {
		before = *c
		if email != "" {
			c.Email = email
		}
		if mobile != "" {
			c.Mobile = mobile
		}
		if city != "" {
			c.City = city
		}
		c.Normalise()
		if err := c.Validate(); err != nil {
			return err
		}
		if c.Email == before.Email && c.Mobile == before.Mobile && c.City == before.City {
			return invalid("nothing changed")
		}
		if kraOK[kraStatus] {
			c.KYCUpdatedOn = date
		}
		return nil
	})
	if err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "client.kyc_updated", c.UCC, map[string]any{"kra": kraStatus, "email_changed": c.Email != before.Email, "mobile_changed": c.Mobile != before.Mobile, "address_changed": c.City != before.City})
	return c.Public(), nil
}
