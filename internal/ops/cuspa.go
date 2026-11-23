package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- Client unpaid securities (CUSPA) ----
//
// SEBI's framework for unpaid securities (circular
// HO/38/11/(9)2026-MIRSD-POD/I/15382/2026 of 3 July 2026, in force from
// 31 October 2026; NSE circulars 35/2026 and 41/2026): securities a client
// has not paid for are paid out to the client's demat account and pledged
// automatically to the trading member's client unpaid securities pledgee
// account (CUSPA). Counting the pay-out day as day 0:
//
//   - The client pays within the firm's period (Settings.CUSPADays, at
//     most 5 trading days after pay-out; 5 when not set).
//   - Every day the value kept in CUSPA, valued as a margin pledge (price
//     less haircut), may be at most 2.25 × (C − B): C the client's
//     end-of-day margin in all segments, B the clear ledger balance across
//     the ledgers other than MTF (a debit is negative). The excess is
//     released by the next trading day, oldest pledge first; all of it once
//     B covers C, or when there is neither a debit nor a margin.
//   - Before invoking, the client is given notice by email or SMS, and it
//     is kept on record.
//   - Invocation is allowed up to the 5th trading day after pay-out. The
//     invoked shares are blocked for early pay-in and sold under the
//     client's code; the proceeds go to the client's ledger and the
//     contract note says so.
//   - What is neither released nor invoked is released by the depository
//     at the end of the 6th trading day after pay-out.
//
// In Ledgerline a CUSPA line (CUSPAItem) keeps the quantity still pledged
// apart from the client's free holding; a release moves it to the
// client's demat holding. With the pledge's PSN (from CDSL's transaction
// export) and CDSL's pledge API on, the release or invocation goes to
// CDSL by its unpledge and confiscation API at once (cuspacdsl.go);
// otherwise the instruction is given on the depository's system (the
// day's release and invocation list is downloadable).

// CUSPA line statuses.
const (
	CUSPAHeld         = "Held in CUSPA"
	CUSPAReleased     = "Released"
	CUSPAInvoked      = "Invoked"
	CUSPASold         = "Sold"
	CUSPAAutoReleased = "Auto-released"
)

const (
	cuspaInvokeDays = 5 // last trading day after pay-out to invoke
	cuspaAutoDays   = 6 // the depository releases at the end of this day
)

var cuspaMultiple = money.MustDec("2.25")

// CUSPAEvent is one step on a CUSPA line.
type CUSPAEvent struct {
	Date string `json:"date"` // business date
	At   string `json:"at"`   // when it was recorded
	What string `json:"what"` // Notice, Released, Invoked, Sold, Auto-released, PSN
	Qty  int64  `json:"qty,omitempty"`
	By   string `json:"by"`
	Note string `json:"note,omitempty"`
	// CDSL is how a release or invocation went to CDSL by the API: the
	// request (UN-n, CF-n) and its status. Empty: give it on CDSL's system.
	CDSL string `json:"cdsl,omitempty"`
}

// cuspaPayDays is the firm's payment period in trading days after pay-out.
func (s Settings) cuspaPayDays() int {
	if s.CUSPADays <= 0 || s.CUSPADays > cuspaInvokeDays {
		return cuspaInvokeDays
	}
	return s.CUSPADays
}

func addTradingDays(set Settings, d string, n int) string {
	for i := 0; i < n; i++ {
		d = set.NextBusinessDay(d)
	}
	return d
}

// total is the line's whole quantity: still held, released and invoked.
func (it CUSPAItem) total() int64 { return it.Qty + it.Released + it.Invoked }

// unsold is the invoked quantity not yet sold.
func (it CUSPAItem) unsold() int64 { return max(it.Invoked-it.Sold, 0) }

// costOf is the purchase cost of q shares of the line.
func (it CUSPAItem) costOf(q int64) money.Dec {
	t := it.total()
	if t <= 0 {
		return money.Dec{}
	}
	v, _ := it.Value.MulInt(q).Quo(money.DecInt(t))
	return v
}

// open reports whether the line still needs work: shares held or
// invoked shares not yet sold.
func (it CUSPAItem) open() bool { return it.Qty > 0 || it.unsold() > 0 }

// settle sets the status from the quantities; closed is the status when
// nothing is held or waiting for sale and nothing was invoked.
func (it *CUSPAItem) settle(closed string) {
	switch {
	case it.Qty > 0:
		it.Status = CUSPAHeld
	case it.unsold() > 0:
		it.Status = CUSPAInvoked
	case it.Invoked > 0:
		it.Status = CUSPASold
	default:
		it.Status = closed
	}
}

func (it CUSPAItem) noticed() bool { return it.NoticeOn != "" }

// cuspaDates are a line's days under the framework.
type cuspaDates struct {
	PayOut      string
	PayBy       string // last day for the client to pay
	InvokeFrom  string // first day the shares may be invoked
	LastInvoke  string // day 5
	AutoRelease string // day 6: released by the depository at day end
	Day         int    // trading days since pay-out on the date asked
}

func (it CUSPAItem) dates(set Settings, date string) cuspaDates {
	po := it.PayOut
	if po == "" {
		po = set.NextSettlementDay(it.Date)
	}
	pay := set.cuspaPayDays()
	d := cuspaDates{PayOut: po, PayBy: addTradingDays(set, po, pay), InvokeFrom: addTradingDays(set, po, min(pay+1, cuspaInvokeDays)),
		LastInvoke: addTradingDays(set, po, cuspaInvokeDays), AutoRelease: addTradingDays(set, po, cuspaAutoDays)}
	// An extension (cuspa_more.go) moves the last day to invoke to its last
	// trading day; the depository releases at the end of the next one.
	if n := len(it.Extensions); n > 0 && it.Extensions[n-1].Until > d.LastInvoke {
		d.LastInvoke = it.Extensions[n-1].Until
		d.AutoRelease = set.NextBusinessDay(d.LastInvoke)
	}
	if date > po {
		d.Day = businessDaysBetween(set, po, date)
	}
	return d
}

// ceilQty is the fewest whole shares at per each that make up amount.
func ceilQty(amount, per money.Dec) int64 {
	if amount.Sign() <= 0 {
		return 0
	}
	if per.Sign() <= 0 {
		return 1 << 62
	}
	q, _ := amount.Quo(per)
	n := int64(q.Float64())
	for n > 0 && per.MulInt(n-1).Cmp(amount) >= 0 {
		n--
	}
	for per.MulInt(n).Cmp(amount) < 0 {
		n++
	}
	return n
}

// cuspaBook is what the CUSPA rules need on a date.
type cuspaBook struct {
	set       Settings
	date      string
	look      Lookup
	items     []CUSPAItem
	clients   map[string]Client
	funds     map[string]money.Amount // B: ledger balance, credit positive
	margin    map[string]money.Dec    // C: end-of-day margin, all segments
	marginErr string
}

func (o *Ops) cuspaBook(ctx context.Context, tenant, date string) (cuspaBook, error) {
	b := cuspaBook{date: date, margin: map[string]money.Dec{}}
	var err error
	if b.set, err = o.Settings(ctx, tenant); err != nil {
		return b, err
	}
	if b.items, err = docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, ""); err != nil {
		return b, err
	}
	kept := b.items[:0]
	for _, it := range b.items {
		if it.Date <= date {
			kept = append(kept, it)
		}
	}
	b.items = kept
	sort.SliceStable(b.items, func(i, j int) bool {
		pi, pj := b.items[i].dates(b.set, date).PayOut, b.items[j].dates(b.set, date).PayOut
		if pi != pj {
			return pi < pj
		}
		return b.items[i].ID < b.items[j].ID
	})
	_, b.look, _ = o.Market(ctx, tenant, date)
	if b.clients, err = o.ClientMap(ctx, tenant); err != nil {
		return b, err
	}
	if b.funds, err = o.clientFunds(ctx, tenant, date, ""); err != nil {
		return b, err
	}
	in, err := o.riskInput(ctx, tenant, date)
	if err != nil {
		b.marginErr = err.Error()
		return b, nil
	}
	pos, cm := in.positions("")
	for u, ps := range pos {
		for _, p := range ps {
			b.margin[u] = b.margin[u].Add(p.Margin)
		}
	}
	for u, v := range cm {
		b.margin[u] = b.margin[u].Add(v)
	}
	return b, nil
}

// price is a share's market price and haircut; without a price for the
// date the line is valued at its purchase cost.
func (b cuspaBook) price(it CUSPAItem) (px, haircut money.Dec, atCost bool) {
	if i, ok := b.look.ByISIN(it.ISIN); ok && i.LTP.Sign() > 0 {
		return i.LTP, pledgeHaircut(i), false // the margin-pledge haircut, with its floors (collhaircut.go)
	}
	return it.costOf(1), money.Dec{}, true
}

// pledgeValue is the value of one share as a margin pledge.
func (b cuspaBook) pledgeValue(it CUSPAItem) money.Dec {
	px, hc, _ := b.price(it)
	return px.Pct(money.DecInt(100).Sub(hc))
}

func (b cuspaBook) owes(ucc string) bool { return b.funds[ucc] < 0 }

// CUSPAClient is one client's CUSPA limit on a date.
type CUSPAClient struct {
	UCC        string    `json:"ucc"`
	Name       string    `json:"name"`
	Ledger     money.Dec `json:"ledger"`  // B
	Margin     money.Dec `json:"margin"`  // C
	Pledged    money.Dec `json:"pledged"` // A: held lines at price less haircut
	Max        money.Dec `json:"max"`     // 2.25 × (C − B), nil when B covers C
	Excess     money.Dec `json:"excess"`  // A beyond the maximum
	ReleaseAll bool      `json:"release_all"`
	Lines      int       `json:"lines"`
}

// limits works out each client's maximum and the quantity of each line to
// release to bring the client within it, oldest pledge first.
func (b cuspaBook) limits() ([]CUSPAClient, map[string]int64) {
	by := map[string]*CUSPAClient{}
	var order []string
	for _, it := range b.items {
		if it.Qty <= 0 {
			continue
		}
		c := by[it.UCC]
		if c == nil {
			c = &CUSPAClient{UCC: it.UCC, Name: b.clients[it.UCC].Name, Ledger: b.funds[it.UCC].Dec(), Margin: b.margin[it.UCC]}
			by[it.UCC] = c
			order = append(order, it.UCC)
		}
		c.Pledged = c.Pledged.Add(b.pledgeValue(it).MulInt(it.Qty))
		c.Lines++
	}
	release := map[string]int64{}
	var out []CUSPAClient
	for _, u := range order {
		c := by[u]
		gap := c.Margin.Sub(c.Ledger)
		if gap.Sign() <= 0 {
			c.ReleaseAll, c.Excess = true, c.Pledged
		} else {
			c.Max = gap.Mul(cuspaMultiple)
			c.Excess = money.Max(c.Pledged.Sub(c.Max), money.Dec{})
		}
		left := c.Excess
		for _, it := range b.items {
			if it.UCC != u || it.Qty <= 0 || (!c.ReleaseAll && left.Sign() <= 0) {
				continue
			}
			q := it.Qty
			if !c.ReleaseAll {
				q = min(it.Qty, ceilQty(left, b.pledgeValue(it)))
				left = left.Sub(b.pledgeValue(it).MulInt(q))
			}
			if q > 0 {
				release[it.ID] = q
			}
		}
		out = append(out, *c)
	}
	return out, release
}

// CUSPAPolicy is the firm's CUSPA rule as the screen shows it.
type CUSPAPolicy struct {
	PayDays    int    `json:"pay_days"`
	InvokeDays int    `json:"invoke_days"`
	AutoDays   int    `json:"auto_days"`
	Multiple   string `json:"multiple"`
}

// CUSPARow is a CUSPA line on the ageing view.
type CUSPARow struct {
	CUSPAItem
	Name        string    `json:"name"`
	Groups      []string  `json:"groups,omitempty"`
	PayOutDay   string    `json:"pay_out_day"`
	PayBy       string    `json:"pay_by"`
	InvokeFrom  string    `json:"invoke_from"`
	LastInvoke  string    `json:"last_invoke"`
	AutoRelease string    `json:"auto_release"`
	Day         int       `json:"day"`    // trading days since pay-out
	Bucket      string    `json:"bucket"` // period, due, overdue, last, auto, invoked, closed
	Price       money.Dec `json:"price"`
	HaircutPct  money.Dec `json:"haircut_pct"`
	AtCost      bool      `json:"at_cost,omitempty"`
	MarketValue money.Dec `json:"market_value"`
	PledgeValue money.Dec `json:"pledge_value"`
	Ledger      money.Dec `json:"ledger"`
	ReleaseQty  int64     `json:"release_qty,omitempty"` // to release under the 2.25 limit
	Next        string    `json:"next"`
	Can         []string  `json:"can"`
}

// CUSPAView is the CUSPA ageing on a date.
type CUSPAView struct {
	Date    string            `json:"date"`
	Policy  CUSPAPolicy       `json:"policy"`
	Rows    []CUSPARow        `json:"rows"`
	Clients []CUSPAClient     `json:"clients"`
	Totals  map[string]string `json:"totals"`
	Notes   []string          `json:"notes,omitempty"`
}

func (b cuspaBook) row(it CUSPAItem, releaseQty int64) CUSPARow {
	d := it.dates(b.set, b.date)
	c := b.clients[it.UCC]
	r := CUSPARow{CUSPAItem: it, Name: c.Name, Groups: c.Tags, PayOutDay: d.PayOut, PayBy: d.PayBy, InvokeFrom: d.InvokeFrom, LastInvoke: d.LastInvoke,
		AutoRelease: d.AutoRelease, Day: d.Day, Ledger: b.funds[it.UCC].Dec(), ReleaseQty: releaseQty, Can: []string{}}
	r.Price, r.HaircutPct, r.AtCost = b.price(it)
	q := it.Qty + it.unsold()
	r.MarketValue = r.Price.MulInt(q)
	r.PledgeValue = b.pledgeValue(it).MulInt(it.Qty)
	owes := b.owes(it.UCC)
	switch {
	case it.Qty > 0 && b.date > d.AutoRelease:
		r.Bucket, r.Next = "auto", "Released by the depository at the end of "+fmtDay(d.AutoRelease)+"; the daily review records it."
	case it.Qty > 0 && b.date == d.AutoRelease:
		r.Bucket, r.Next = "auto", "The depository releases it at the end of today."
	case it.Qty > 0 && b.date == d.LastInvoke:
		r.Bucket = "last"
	case it.Qty > 0 && b.date > d.PayBy:
		r.Bucket = "overdue"
	case it.Qty > 0 && b.date == d.PayBy:
		r.Bucket = "due"
	case it.Qty > 0:
		r.Bucket = "period"
	case it.unsold() > 0:
		r.Bucket, r.Next = "invoked", fmt.Sprintf("Sell %d under the client's code; the proceeds go to the client's ledger.", it.unsold())
	default:
		r.Bucket, r.Next = "closed", it.Status
	}
	if it.Qty > 0 {
		r.Can = append(r.Can, "release")
		if releaseQty > 0 {
			r.Can = append(r.Can, "release_excess")
		}
		inWindow := b.date <= d.LastInvoke
		if owes && inWindow {
			r.Can = append(r.Can, "notice")
		}
		if owes && b.date == d.LastInvoke && b.date >= cuspaExtFrom {
			r.Can = append(r.Can, "extend")
		}
		if owes && inWindow && it.noticed() && b.date >= d.InvokeFrom {
			r.Can = append(r.Can, "invoke")
		}
		if r.Next == "" {
			switch {
			case !owes:
				r.Next = "Paid: release to the client."
			case b.date == d.LastInvoke && b.date >= cuspaExtFrom && it.noticed():
				r.Next = "Last day: invoke and sell today, or, if the shares cannot be sold (lower circuit, suspension), ask the depository for an extension by 6 PM."
			case b.date < d.PayBy:
				r.Next = "Client to pay by " + fmtDay(d.PayBy) + "."
			case !it.noticed():
				r.Next = "Give notice; invoke by " + fmtDay(d.LastInvoke) + " at the latest."
			case b.date < d.InvokeFrom:
				r.Next = "Notice given; invoke from " + fmtDay(d.InvokeFrom) + " if still unpaid."
			default:
				r.Next = "Invoke and sell by " + fmtDay(d.LastInvoke) + "."
			}
			if releaseQty > 0 {
				r.Next = fmt.Sprintf("Over the limit: release %d by %s. ", releaseQty, fmtDay(b.set.NextBusinessDay(b.date))) + r.Next
			}
		}
	}
	if it.unsold() > 0 {
		r.Can = append(r.Can, "sold")
	}
	return r
}

// CUSPAAgeing is every CUSPA line with its age under the framework, each
// client's 2.25× limit and what to do next. Closed lines are kept for 90
// days.
func (o *Ops) CUSPAAgeing(ctx context.Context, tenant, date string) (CUSPAView, error) {
	if !validDate(date) {
		return CUSPAView{}, invalid("give a date (YYYY-MM-DD)")
	}
	b, err := o.cuspaBook(ctx, tenant, date)
	if err != nil {
		return CUSPAView{}, err
	}
	clients, release := b.limits()
	v := CUSPAView{Date: date, Policy: CUSPAPolicy{PayDays: b.set.cuspaPayDays(), InvokeDays: cuspaInvokeDays, AutoDays: cuspaAutoDays, Multiple: cuspaMultiple.String()},
		Rows: []CUSPARow{}, Clients: clients, Totals: map[string]string{}}
	if v.Clients == nil {
		v.Clients = []CUSPAClient{}
	}
	keepFrom := addDays(date, -90)
	var held, pledged, market money.Dec
	n := map[string]int{}
	atCost := 0
	for _, it := range b.items {
		if !it.open() && cuspaLastDate(it) < keepFrom {
			continue
		}
		r := b.row(it, release[it.ID])
		v.Rows = append(v.Rows, r)
		n[r.Bucket]++
		if it.Qty > 0 {
			held = held.Add(it.costOf(it.Qty))
			pledged = pledged.Add(r.PledgeValue)
			market = market.Add(r.MarketValue)
			if r.AtCost {
				atCost++
			}
		}
		if it.Qty > 0 && contains(r.Can, "release_excess") {
			n["excess"]++
		}
	}
	for _, k := range []string{"period", "due", "overdue", "last", "auto", "invoked", "closed", "excess"} {
		v.Totals[k] = fmt.Sprint(n[k])
	}
	v.Totals["cost"], v.Totals["pledge_value"], v.Totals["market_value"] = held.Fixed(2), pledged.Fixed(2), market.Fixed(2)
	if atCost > 0 {
		v.Notes = append(v.Notes, fmt.Sprintf("%d line(s) have no price for %s and are valued at their purchase cost.", atCost, fmtDay(date)))
	}
	if b.marginErr != "" {
		v.Notes = append(v.Notes, "The end-of-day margin could not be worked out ("+b.marginErr+"); the limit uses a margin of nil.")
	}
	return v, nil
}

func cuspaLastDate(it CUSPAItem) string {
	d := it.Date
	for _, e := range it.Events {
		if e.Date > d {
			d = e.Date
		}
	}
	return d
}

// CUSPABulkReq is one action on several CUSPA lines.
type CUSPABulkReq struct {
	Date    string   `json:"date"`
	Action  string   `json:"action"` // release, release_excess, notice, invoke, sold
	IDs     []string `json:"ids"`
	Preview bool     `json:"preview,omitempty"`
	Note    string   `json:"note,omitempty"`
	// Code is the depository's reason code of an extension (CUSPA01 to
	// CUSPA06); Note then says why in words.
	Code string `json:"code,omitempty"`
}

// CUSPALine is what an action does, or would do, to one line.
type CUSPALine struct {
	ID     string    `json:"id"`
	UCC    string    `json:"ucc"`
	Symbol string    `json:"symbol"`
	Qty    int64     `json:"qty"`
	Value  money.Dec `json:"value"`
	OK     bool      `json:"ok"`
	Why    string    `json:"why,omitempty"`
}

// CUSPABulkResult reports a bulk action.
type CUSPABulkResult struct {
	Action  string      `json:"action"`
	Preview bool        `json:"preview"`
	Lines   []CUSPALine `json:"lines"`
	Done    int         `json:"done"`
	Skipped int         `json:"skipped"`
}

// CUSPABulk applies one action to the chosen lines (Preview: only says
// what it would do):
//
//   - release: the whole quantity held. While the client's ledger is in
//     debit only a checker or risk manager may release more than the
//     2.25× limit requires.
//   - release_excess: the quantity over the 2.25× limit, oldest first.
//   - notice: emails and texts each client the notice before sale and
//     records it on the lines.
//   - invoke: from the end of the payment period to the 5th trading day
//     after pay-out, after notice, and only as much as covers the debit.
//   - sold: records the sale of the invoked shares.
//   - extend: records the request to the depository for a one-week
//     extension of the pledge (from 3 January 2027; cuspa_more.go) and
//     tells the client.
func (o *Ops) CUSPABulk(ctx context.Context, tenant, actor string, req CUSPABulkReq) (CUSPABulkResult, error) {
	if !validDate(req.Date) {
		return CUSPABulkResult{}, invalid("give a date (YYYY-MM-DD)")
	}
	if len(req.IDs) == 0 {
		return CUSPABulkResult{}, invalid("choose the CUSPA lines")
	}
	switch req.Action {
	case "release", "release_excess":
		if err := o.require(ctx, tenant, actor, "Releasing CUSPA securities", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return CUSPABulkResult{}, err
		}
	case "notice":
		if err := o.require(ctx, tenant, actor, "Giving notice before invoking CUSPA securities", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return CUSPABulkResult{}, err
		}
	case "invoke":
		if err := o.require(ctx, tenant, actor, "Invoking unpaid CUSPA securities for sale", RoleChecker, RoleRisk); err != nil {
			return CUSPABulkResult{}, err
		}
	case "sold":
		if err := o.require(ctx, tenant, actor, "Recording the sale of invoked CUSPA securities", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return CUSPABulkResult{}, err
		}
	case "extend":
		if err := o.require(ctx, tenant, actor, "Asking for an extension of CUSPA pledges", RoleChecker, RoleRisk); err != nil {
			return CUSPABulkResult{}, err
		}
		if err := cuspaExtCheckReq(req); err != nil {
			return CUSPABulkResult{}, err
		}
	default:
		return CUSPABulkResult{}, invalid("unknown CUSPA action %q: release, release_excess, notice, invoke, sold or extend", req.Action)
	}
	b, err := o.cuspaBook(ctx, tenant, req.Date)
	if err != nil {
		return CUSPABulkResult{}, err
	}
	limits, excess := b.limits()
	releaseAll := map[string]bool{}
	for _, c := range limits {
		releaseAll[c.UCC] = c.ReleaseAll
	}
	want := map[string]bool{}
	for _, id := range req.IDs {
		want[strings.TrimSpace(id)] = true
	}
	res := CUSPABulkResult{Action: req.Action, Preview: req.Preview, Lines: []CUSPALine{}}
	var chosen []CUSPAItem
	for _, it := range b.items { // oldest pledge first
		if want[it.ID] {
			chosen = append(chosen, it)
			delete(want, it.ID)
		}
	}
	for _, id := range sortedKeys(want) {
		res.Lines = append(res.Lines, CUSPALine{ID: id, Why: "no such CUSPA line"})
	}
	skip := func(it CUSPAItem, why string) {
		res.Lines = append(res.Lines, CUSPALine{ID: it.ID, UCC: it.UCC, Symbol: it.Symbol, Why: why})
	}
	plan := map[string]int64{}
	switch req.Action {
	case "release", "release_excess":
		early := ""
		for _, it := range chosen {
			if it.Qty <= 0 {
				skip(it, "nothing held ("+it.Status+")")
				continue
			}
			q := it.Qty
			if req.Action == "release_excess" {
				if q = excess[it.ID]; q <= 0 {
					skip(it, "within the client's 2.25× limit")
					continue
				}
			} else if b.owes(it.UCC) && !releaseAll[it.UCC] && q > excess[it.ID] {
				if early == "" {
					early = "ok"
					if err := o.require(ctx, tenant, actor, "Releasing CUSPA securities before the client has paid", RoleChecker, RoleRisk); err != nil {
						early = "the client has not paid; a checker or risk manager releases it before payment"
					}
				}
				if early != "ok" {
					skip(it, early)
					continue
				}
			}
			plan[it.ID] = q
		}
	case "notice":
		for _, it := range chosen {
			d := it.dates(b.set, req.Date)
			switch {
			case it.Qty <= 0:
				skip(it, "nothing held ("+it.Status+")")
			case !b.owes(it.UCC):
				skip(it, "the client's ledger is not in debit")
			case req.Date > d.LastInvoke:
				skip(it, "past the last day to invoke ("+fmtDay(d.LastInvoke)+")")
			default:
				plan[it.ID] = it.Qty
			}
		}
	case "invoke":
		left := map[string]money.Dec{}
		for u, f := range b.funds {
			if f < 0 {
				left[u] = f.Dec().Neg()
			}
		}
		for _, it := range chosen {
			d := it.dates(b.set, req.Date)
			px, _, _ := b.price(it)
			switch {
			case it.Qty <= 0:
				skip(it, "nothing held ("+it.Status+")")
			case !b.owes(it.UCC):
				skip(it, "the client's ledger is not in debit")
			case !it.noticed():
				skip(it, "give the client notice first")
			case req.Date < d.InvokeFrom:
				skip(it, "the client has until "+fmtDay(d.PayBy)+" to pay")
			case req.Date > d.LastInvoke:
				skip(it, "the last day to invoke was "+fmtDay(d.LastInvoke))
			case left[it.UCC].Sign() <= 0:
				skip(it, "the debit is covered by the client's earlier lines")
			default:
				q := min(it.Qty, ceilQty(left[it.UCC], px))
				left[it.UCC] = left[it.UCC].Sub(px.MulInt(q))
				plan[it.ID] = q
			}
		}
	case "sold":
		for _, it := range chosen {
			if it.unsold() <= 0 {
				skip(it, "no invoked shares waiting for sale")
				continue
			}
			plan[it.ID] = it.unsold()
		}
	case "extend":
		for _, it := range chosen {
			if why := o.cuspaExtWhyNot(b, it, req.Date); why != "" {
				skip(it, why)
				continue
			}
			plan[it.ID] = it.Qty
		}
	}
	var noticed map[string]cuspaNotice
	if req.Action == "notice" && !req.Preview {
		noticed = o.cuspaNotify(ctx, tenant, actor, b, chosen, plan)
	}
	if req.Action == "extend" && !req.Preview {
		return o.cuspaExtend(ctx, tenant, actor, b, req, chosen, plan, res)
	}
	cl := map[string]Client{}
	for _, it := range chosen {
		q, ok := plan[it.ID]
		if !ok {
			continue
		}
		px, _, _ := b.price(it)
		line := CUSPALine{ID: it.ID, UCC: it.UCC, Symbol: it.Symbol, Qty: q, Value: px.MulInt(q), OK: true}
		if req.Preview {
			res.Lines = append(res.Lines, line)
			continue
		}
		var err error
		switch req.Action {
		case "release", "release_excess":
			c, seen := cl[it.UCC]
			if !seen {
				c, _ = o.Client(ctx, tenant, it.UCC)
				if c.UCC == "" {
					c.UCC = it.UCC
				}
				cl[it.UCC] = c
			}
			why := "released by the firm"
			if req.Action == "release_excess" {
				why = "over the 2.25× limit"
			}
			why = firstOf(req.Note, why)
			err = o.cuspaRelease(ctx, tenant, actor, req.Date, c, it, q, CUSPAReleased, why)
		case "notice":
			if n := noticed[it.UCC]; !n.sent {
				line.OK, line.Why = false, n.note
			} else {
				_, err = docs.Update(ctx, o.Docs, tenant, cCUSPA, it.ID, actor, func(x *CUSPAItem) error {
					x.NoticeOn, x.NoticeBy = req.Date, actor
					x.Events = append(x.Events, o.cuspaEvent(req.Date, "Notice", 0, actor, n.note))
					return nil
				})
			}
		case "invoke":
			_, err = docs.Update(ctx, o.Docs, tenant, cCUSPA, it.ID, actor, func(x *CUSPAItem) error {
				if x.Qty < q {
					return fmt.Errorf("%w: %s holds %d", ErrState, x.ID, x.Qty)
				}
				x.Qty -= q
				x.Invoked += q
				x.InvokedOn = req.Date
				x.Events = append(x.Events, o.cuspaEvent(req.Date, "Invoked", q, actor, firstOf(req.Note, "unpaid after notice")))
				x.settle(CUSPAReleased)
				return nil
			})
			if err == nil {
				o.log(ctx, tenant, actor, "cuspa.invoked", it.ID, map[string]any{"ucc": it.UCC, "symbol": it.Symbol, "qty": q, "date": req.Date})
				o.cuspaToCDSL(ctx, tenant, actor, req.Date, it.ID, "CF", q, px.MulInt(q), firstOf(req.Note, "unpaid after notice"))
			}
		case "sold":
			err = o.cuspaSold(ctx, tenant, actor, req.Date, it.ID, q, firstOf(req.Note, "recorded by hand"))
		}
		if err != nil {
			line.OK, line.Why = false, errText(err)
		}
		res.Lines = append(res.Lines, line)
	}
	for _, l := range res.Lines {
		if l.OK {
			res.Done++
		} else {
			res.Skipped++
		}
	}
	return res, nil
}

func (o *Ops) cuspaEvent(date, what string, qty int64, by, note string) CUSPAEvent {
	return CUSPAEvent{Date: date, At: o.now().Format(time.RFC3339), What: what, Qty: qty, By: by, Note: note}
}

// cuspaRelease moves q shares of a line to the client's demat holding.
// The firm's own release goes to CDSL by the pledge API when it can
// (cuspacdsl.go); the depository's own day-6 release needs nothing.
func (o *Ops) cuspaRelease(ctx context.Context, tenant, actor, date string, c Client, it CUSPAItem, q int64, status, why string) error {
	if _, err := o.adjustHoldingAt(ctx, tenant, actor, it.UCC, holdingAcct(c, payoutDemat(c).ID), it.ISIN, it.Symbol, money.DecInt(q), money.Dec{}); err != nil {
		return err
	}
	what := "Released"
	if status == CUSPAAutoReleased {
		what = "Auto-released"
	}
	_, err := docs.Update(ctx, o.Docs, tenant, cCUSPA, it.ID, actor, func(x *CUSPAItem) error {
		q = min(q, x.Qty)
		x.Qty -= q
		x.Released += q
		x.Events = append(x.Events, o.cuspaEvent(date, what, q, actor, why))
		x.settle(status)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "cuspa.released", it.ID, map[string]any{"ucc": it.UCC, "qty": q, "symbol": it.Symbol, "date": date, "why": why})
		if status == CUSPAReleased {
			o.cuspaToCDSL(ctx, tenant, actor, date, it.ID, "UN", q, money.Dec{}, why)
		}
	}
	return err
}

// cuspaSold records q invoked shares of a line as sold on date.
func (o *Ops) cuspaSold(ctx context.Context, tenant, actor, date, id string, q int64, note string) error {
	_, err := docs.Update(ctx, o.Docs, tenant, cCUSPA, id, actor, func(x *CUSPAItem) error {
		q = min(q, x.unsold())
		if q <= 0 {
			return fmt.Errorf("%w: %s has no invoked shares waiting for sale", ErrState, id)
		}
		x.Sold += q
		x.SoldOn = date
		x.Events = append(x.Events, o.cuspaEvent(date, "Sold", q, actor, note))
		x.settle(CUSPAReleased)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "cuspa.sold", id, map[string]any{"qty": q, "date": date})
	}
	return err
}

// cuspaNotify sends each client one notice for the planned lines. It
// returns, for each client, whether a channel took it and what to record
// (the channels, or why it did not go).
func (o *Ops) cuspaNotify(ctx context.Context, tenant, actor string, b cuspaBook, items []CUSPAItem, plan map[string]int64) map[string]cuspaNotice {
	by := map[string][]CUSPAItem{}
	var order []string
	for _, it := range items {
		if _, ok := plan[it.ID]; !ok {
			continue
		}
		if by[it.UCC] == nil {
			order = append(order, it.UCC)
		}
		by[it.UCC] = append(by[it.UCC], it)
	}
	out := map[string]cuspaNotice{}
	for _, u := range order {
		var lines []string
		first := cuspaDates{}
		for i, it := range by[u] {
			lines = append(lines, fmt.Sprintf("%d %s", it.Qty, it.Symbol))
			if d := it.dates(b.set, b.date); i == 0 || d.PayBy < first.PayBy {
				first = d
			}
		}
		payBy := first.PayBy
		if payBy < b.date {
			payBy = b.date
		}
		invokeFrom := first.InvokeFrom
		if invokeFrom <= b.date {
			invokeFrom = min(b.set.NextBusinessDay(b.date), first.LastInvoke)
		}
		ms, err := o.sendEvent(ctx, tenant, actor, "cuspa.notice", u, map[string]string{
			"lines": strings.Join(lines, ", "), "amount": b.funds[u].Dec().Neg().Fixed(2), "pay_by": fmtDay(payBy),
			"invoke_from": fmtDay(invokeFrom), "last_day": fmtDay(first.LastInvoke), "date": fmtDay(b.date),
		}, "")
		var sent, other []string
		for _, m := range ms {
			if m.Status == "Sent" {
				sent = append(sent, m.Channel)
			} else {
				other = append(other, m.Channel+" "+strings.ToLower(m.Status))
			}
		}
		switch {
		case len(sent) > 0:
			out[u] = cuspaNotice{true, "notice by " + strings.Join(sent, " and ")}
			o.log(ctx, tenant, actor, "cuspa.notice", u, map[string]any{"lines": len(lines), "channels": strings.Join(sent, ", "), "date": b.date})
		case err != nil:
			out[u] = cuspaNotice{false, "the notice could not be sent: " + errText(err)}
		case len(other) > 0:
			out[u] = cuspaNotice{false, "the notice was not delivered: " + strings.Join(other, ", ")}
		default:
			out[u] = cuspaNotice{false, "no active cuspa.notice template (Client messages)"}
		}
		if !out[u].sent {
			o.log(ctx, tenant, actor, "cuspa.notice.failed", u, map[string]any{"why": out[u].note})
		}
	}
	return out
}

type cuspaNotice struct {
	sent bool
	note string
}

// ---- the daily review ----

// cuspaReview is the daily CUSPA work (Periodic work, every trading day):
// records the depository's auto-releases, releases what is over each
// client's 2.25× limit, spots the sale of invoked shares in the trades,
// and puts the notices, invocations and sales due in the work queue.
func (o *Ops) cuspaReview(ctx context.Context, tenant, actor, date string) (string, []PeriodicFile, error) {
	b, err := o.cuspaBook(ctx, tenant, date)
	if err != nil {
		return "", nil, err
	}
	if !b.set.IsBusinessDay(date) {
		return "Not a trading day: nothing to review", nil, nil
	}
	o.cuspaLinkPSNs(ctx, tenant, SystemActor) // lines made after CDSL's export was read
	sold := o.cuspaSpotSales(ctx, tenant, b)
	auto, released, owing := 0, 0, 0
	cl := map[string]Client{}
	client := func(u string) Client {
		c, ok := cl[u]
		if !ok {
			c, _ = o.Client(ctx, tenant, u)
			if c.UCC == "" {
				c.UCC = u
			}
			cl[u] = c
		}
		return c
	}
	rc := &runCtx{o: o, ctx: ctx, tenant: tenant, date: date, process: "cuspa", keys: map[string]bool{}}
	for _, it := range b.items {
		if d := it.dates(b.set, date); it.Qty > 0 && date > d.AutoRelease {
			if o.cuspaRelease(ctx, tenant, SystemActor, date, client(it.UCC), it, it.Qty, CUSPAAutoReleased, "released by the depository at the end of "+fmtDay(d.AutoRelease)) == nil {
				auto++
				if b.owes(it.UCC) {
					owing++
					rc.queue(it.ID+":auto", fmt.Sprintf("Unpaid %s of %s released by the depository", it.Symbol, it.UCC),
						fmt.Sprintf("%d %s bought on %s were neither released nor invoked by the last day to invoke, so the depository released them to the client at the end of %s. The ledger is still in debit by ₹%s: recover it as for any debit.", it.Qty, it.Symbol, fmtDay(it.Date), fmtDay(d.AutoRelease), b.funds[it.UCC].Dec().Neg().Fixed(2)),
						it.UCC, RoleRisk, "settlement", "", nil)
				}
			}
		}
	}
	if auto > 0 {
		if b, err = o.cuspaBook(ctx, tenant, date); err != nil {
			return "", nil, err
		}
	}
	_, excess := b.limits()
	var relIDs []string // released here, to give on the depository's system
	byAPI := 0
	for _, it := range b.items {
		if q := excess[it.ID]; q > 0 {
			if o.cuspaRelease(ctx, tenant, SystemActor, date, client(it.UCC), it, q, CUSPAReleased, "over the 2.25× limit (daily review)") == nil {
				released++
				if x, _, err := docs.Get[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, it.ID); err == nil && cuspaLastCDSL(x, date) {
					byAPI++
				} else {
					relIDs = append(relIDs, it.ID)
				}
			}
		}
	}
	if len(relIDs) > 0 {
		rc.queue("release:"+date, fmt.Sprintf("Release %d CUSPA line(s) at the depository", len(relIDs)),
			"The daily review released shares that were over the clients' 2.25× limit, or whose clients have paid: "+strings.Join(relIDs, ", ")+". Give the release instructions on the depository's system today (Settlement → Unpaid securities → Depository list).",
			"", RoleChecker, "settlement", "", nil)
	}
	if auto > 0 || released > 0 {
		if b, err = o.cuspaBook(ctx, tenant, date); err != nil {
			return "", nil, err
		}
	}
	notice, invoke, sell := map[string][]string{}, map[string][]string{}, map[string][]string{}
	var uccs []string
	for _, it := range b.items {
		// The weekly review's "Sell unpaid ..." items (before the framework).
		o.closeWork(ctx, tenant, SystemActor, "periodic", "cuspa:"+it.ID, "replaced by the daily CUSPA review")
		d := it.dates(b.set, date)
		switch {
		case it.unsold() > 0:
			sell[it.UCC] = append(sell[it.UCC], it.ID)
		case it.Qty <= 0 || !b.owes(it.UCC) || date > d.LastInvoke || date < d.PayBy:
			continue
		case !it.noticed():
			notice[it.UCC] = append(notice[it.UCC], it.ID)
		case date >= d.InvokeFrom:
			invoke[it.UCC] = append(invoke[it.UCC], it.ID)
		default:
			continue
		}
		if !contains(uccs, it.UCC) {
			uccs = append(uccs, it.UCC)
		}
	}
	for _, u := range uccs {
		owed := b.funds[u].Dec().Neg().Fixed(2)
		if ids := notice[u]; len(ids) > 0 {
			rc.queue(u+":notice", fmt.Sprintf("Give %s notice before invoking unpaid shares", u),
				fmt.Sprintf("%s owes ₹%s and the payment period is over for CUSPA line(s) %s. Send the notice before invoking them.", u, owed, strings.Join(ids, ", ")),
				u, RoleChecker, "settlement", "", &Proposal{Kind: "cuspa.notice", Label: "Send notice", Args: map[string]string{"ids": strings.Join(ids, ",")}})
		}
		if ids := invoke[u]; len(ids) > 0 {
			last := ""
			for _, it := range b.items {
				if contains(ids, it.ID) {
					if l := it.dates(b.set, date).LastInvoke; last == "" || l < last {
						last = l
					}
				}
			}
			rc.queue(u+":invoke", fmt.Sprintf("Invoke and sell unpaid shares of %s by %s", u, fmtDay(last)),
				fmt.Sprintf("%s still owes ₹%s after notice. Invoke CUSPA line(s) %s, only as much as covers the debit, and sell them under the client's code. After %s they can no longer be invoked.", u, owed, strings.Join(ids, ", "), fmtDay(last)),
				u, RoleRisk, "settlement", last+" 23:59", &Proposal{Kind: "cuspa.invoke", Label: "Invoke", Args: map[string]string{"ids": strings.Join(ids, ",")}})
		}
		if ids := sell[u]; len(ids) > 0 {
			rc.queue(u+":sell", fmt.Sprintf("Sell invoked shares of %s", u),
				fmt.Sprintf("Invoked CUSPA line(s) %s are blocked for early pay-in. Place the sale under the client's code (the firm's own code if the account is frozen); the proceeds go to the client's ledger. Sell list: Settlement → Unpaid securities.", strings.Join(ids, ", ")),
				u, RoleRisk, "settlement", "", nil)
		}
	}
	more := o.cuspaReviewMore(ctx, tenant, date, b, rc)
	rc.clearStale()
	held := 0
	for _, it := range b.items {
		if it.Qty > 0 {
			held++
		}
	}
	api := ""
	if byAPI > 0 {
		api = fmt.Sprintf(" (%d by CDSL's pledge API)", byAPI)
	}
	if more != "" {
		more = "; " + more
	}
	return fmt.Sprintf("%d held; %d released over the limit%s; %d released by the depository (%d still owing); %d sale(s) found; %d to notice, %d to invoke, %d to sell%s",
		held, released, api, auto, owing, sold, len(notice), len(invoke), len(sell), more), nil, nil
}

// cuspaLastCDSL says the line's last release or invocation on date went
// to CDSL by the API and CDSL did it.
func cuspaLastCDSL(it CUSPAItem, date string) bool {
	for i := len(it.Events) - 1; i >= 0; i-- {
		e := it.Events[i]
		if e.What == "Released" || e.What == "Invoked" {
			return e.Date == date && strings.HasSuffix(e.CDSL, " Done")
		}
	}
	return false
}

// cuspaSpotSales finds the sale of invoked shares in the client's trades
// from the day of invocation, and records it.
func (o *Ops) cuspaSpotSales(ctx context.Context, tenant string, b cuspaBook) int {
	n := 0
	sales := map[string]map[string]int64{} // day -> ucc|isin -> qty sold
	for _, it := range b.items {
		if it.unsold() <= 0 || it.InvokedOn == "" {
			continue
		}
		for d := it.InvokedOn; d <= b.date; d = b.set.NextBusinessDay(d) {
			if sales[d] == nil {
				sales[d] = o.cuspaSalesOn(ctx, tenant, d)
			}
			k := holdingID(it.UCC, it.ISIN)
			if q := min(sales[d][k], it.unsold()); q > 0 {
				if o.cuspaSold(ctx, tenant, SystemActor, d, it.ID, q, "found in the trades of "+fmtDay(d)) == nil {
					sales[d][k] -= q
					it.Sold += q
					n++
				}
			}
			if it.unsold() <= 0 {
				break
			}
		}
	}
	return n
}

// cuspaSalesOn is each client's delivery sales of a day by ISIN.
func (o *Ops) cuspaSalesOn(ctx context.Context, tenant, date string) map[string]int64 {
	out := map[string]int64{}
	ts, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil || len(ts) == 0 {
		return out
	}
	_, look, _ := o.Market(ctx, tenant, date)
	for _, t := range ts {
		if t.Segment != "CM" || t.Product == "MIS" || t.Side != "S" {
			continue
		}
		if i, ok := look.ForTrade(t); ok {
			out[holdingID(t.UCC, i.ISIN)] += t.Qty
		}
	}
	return out
}

// cuspaEarlyPayIn is the invoked CUSPA shares available on date for the
// pay-in of the client's sale, by holding (UCC and ISIN).
func cuspaEarlyPayIn(items []CUSPAItem, date string) map[string]int64 {
	out := map[string]int64{}
	for _, it := range items {
		if it.Invoked <= 0 || it.InvokedOn == "" || it.InvokedOn > date {
			continue
		}
		q := it.Invoked
		if it.SoldOn != "" && it.SoldOn < date {
			q -= it.Sold
		}
		if q > 0 {
			out[holdingID(it.UCC, it.ISIN)] += q
		}
	}
	return out
}

// cuspaNoteRemarks are the contract note's remarks on the sale of invoked
// CUSPA shares among the client's trades of date.
func (o *Ops) cuspaNoteRemarks(ctx context.Context, tenant, date, ucc string, trades []ingest.TradeEvent) []string {
	items, _ := docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
	var cand []CUSPAItem
	for _, it := range items {
		if it.UCC == ucc && it.Invoked > 0 && it.InvokedOn != "" && it.InvokedOn <= date && (it.SoldOn == "" || it.SoldOn >= date) {
			cand = append(cand, it)
		}
	}
	if len(cand) == 0 {
		return nil
	}
	sold := map[string]int64{} // delivery sales by ISIN
	_, look, _ := o.Market(ctx, tenant, date)
	for _, t := range trades {
		if t.Segment == "CM" && t.Product != "MIS" && t.Side == "S" {
			if i, ok := look.ForTrade(t); ok {
				sold[i.ISIN] += t.Qty
			}
		}
	}
	var out []string
	for _, it := range cand {
		q := min(sold[it.ISIN], it.Invoked)
		if q <= 0 {
			continue
		}
		sold[it.ISIN] -= q
		out = append(out, fmt.Sprintf("Sale of %d %s: unpaid shares pledged to the client unpaid securities pledgee account (CUSPA line %s), invoked on %s after notice and sold under SEBI's framework for unpaid securities. The proceeds are credited to your account.", q, it.Symbol, it.ID, fmtDay(it.InvokedOn)))
	}
	return out
}

// CUSPAFile is a CUSPA list for download: "sell", the invoked shares to
// sell; "depository", the day's releases and invocations to instruct at
// the depository.
func (o *Ops) CUSPAFile(ctx context.Context, tenant, date, kind string) (string, []byte, error) {
	if !validDate(date) {
		return "", nil, invalid("give a date (YYYY-MM-DD)")
	}
	b, err := o.cuspaBook(ctx, tenant, date)
	if err != nil {
		return "", nil, err
	}
	switch kind {
	case "sell":
		rows := [][]string{{"UCC", "Name", "Exchange", "Symbol", "ISIN", "Qty", "CUSPA line", "Invoked on", "Remark"}}
		for _, it := range b.items {
			if q := it.unsold(); q > 0 {
				rows = append(rows, []string{it.UCC, csvSafe(b.clients[it.UCC].Name), firstOf(it.Exchange, "NSE"), it.Symbol, it.ISIN, fmt.Sprint(q), it.ID, it.InvokedOn,
					"Sale of unpaid securities invoked from CUSPA (" + it.ID + "); sell under the client's code, the firm's own if the account is frozen"})
			}
		}
		return "cuspa_sell_" + date + ".csv", csvBytesOf(rows), nil
	case "depository":
		// "Sent by CDSL API" lines are done at CDSL already; the rest are
		// given on CDSL's system.
		rows := [][]string{{"Action", "CUSPA line", "UCC", "Name", "BO ID", "ISIN", "Symbol", "Qty", "Reason", "By", "PSN", "Sent by CDSL API"}}
		for _, it := range b.items {
			c := b.clients[it.UCC]
			for _, e := range it.Events {
				if e.Date != date || (e.What != "Released" && e.What != "Invoked" && e.What != "Extended") {
					continue
				}
				action := "Release (unpledge)"
				switch e.What {
				case "Invoked":
					action = "Invoke"
				case "Extended":
					action = "Extend (by 6 PM)"
				}
				rows = append(rows, []string{action, it.ID, it.UCC, csvSafe(c.Name), payoutDemat(c).ID, it.ISIN, it.Symbol, fmt.Sprint(e.Qty), csvSafe(e.Note), e.By, it.PSN, csvSafe(e.CDSL)})
			}
		}
		return "cuspa_depository_" + date + ".csv", csvBytesOf(rows), nil
	}
	return "", nil, invalid("unknown CUSPA list %q: sell or depository", kind)
}
