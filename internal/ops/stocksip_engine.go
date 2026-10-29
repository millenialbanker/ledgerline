package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- stock SIPs: the instalment engine ----
//
// On each trading day, at the firm's placement time, every SIP with an
// instalment falling to the day (a holiday's instalment moves to the next
// trading day) becomes an order instruction: the price is the last traded
// price (else the last close); the quantity is the amount divided by it,
// rounded down (fractional shares cannot be bought in India), and an
// amount below one share fails; the scrip is checked again; and the
// client's free funds, less what earlier SIP orders of the day need, plus
// a top-up already on its way, must cover the order at its limit (or
// protected) price with charges, or the instalment fails. There are no
// partial orders.
//
// An instruction then goes to the OMS (a basket file or an order API),
// and the day's trades are matched back to it: Filled, Partly filled, or
// Lapsed at the end of the day. A SIP stops after the firm's number of
// failed instalments in a row.

// SIP order statuses.
const (
	SIPOrdSkipped = "Skipped"
	SIPOrdFailed  = "Failed"
	SIPOrdPending = "Pending" // worked out, waiting to go to the OMS
	SIPOrdPlaced  = "Placed"
	SIPOrdFilled  = "Filled"
	SIPOrdPartly  = "Partly filled"
	SIPOrdLapsed  = "Lapsed"      // placed, not executed by the end of the day
	SIPOrdNotSent = "Not sent"    // never handed to the OMS (the firm's side, not counted against the client)
	SIPOrdCheck   = "Check fills" // trades found that do not match the order: a person decides
)

// SIPOrder is one instalment: the instruction and what became of it.
type SIPOrder struct {
	ID        string       `json:"id"`  // date|SIP|scheduled date
	Ref       string       `json:"ref"` // the order's reference at the OMS
	SIP       string       `json:"sip"`
	UCC       string       `json:"ucc"`
	Name      string       `json:"name"`
	Exchange  string       `json:"exchange"`
	Symbol    string       `json:"symbol"`
	ISIN      string       `json:"isin"`
	Scheduled string       `json:"scheduled"` // the instalment's own date
	Date      string       `json:"date"`      // the trading day it is placed on
	Basis     string       `json:"basis"`
	Amount    money.Amount `json:"amount,omitempty"`
	Price     money.Dec    `json:"price"` // price the quantity was worked out at
	PriceSrc  string       `json:"price_source,omitempty"`
	Qty       int64        `json:"qty"`
	OrderType string       `json:"order_type"`
	Limit     money.Dec    `json:"limit,omitempty"`
	Protect   money.Dec    `json:"protection_pct"`
	Estimate  money.Amount `json:"estimate"` // funds the order needs with charges
	Free      money.Amount `json:"free"`     // free funds at the check, after earlier SIP orders of the day
	Topup     money.Amount `json:"topup,omitempty"`
	AlgoID    string       `json:"algo_id,omitempty"`
	Status    string       `json:"status"`
	Reason    string       `json:"reason,omitempty"`
	Router    string       `json:"router,omitempty"`
	Batch     string       `json:"batch,omitempty"`
	OMSOrder  string       `json:"oms_order,omitempty"` // the OMS's order ID (API order entry)
	PlacedAt  string       `json:"placed_at,omitempty"`
	FilledQty int64        `json:"filled_qty,omitempty"`
	AvgPrice  money.Dec    `json:"avg_price,omitempty"`
	Trades    []string     `json:"trades,omitempty"` // trade keys
	FillSrc   string       `json:"fill_source,omitempty"`
	Note      string       `json:"note,omitempty"`
	Counted   bool         `json:"counted,omitempty"` // the outcome has been counted on the SIP
	CreatedAt string       `json:"created_at"`
	UpdatedAt string       `json:"updated_at,omitempty"`
}

func (x SIPOrder) open() bool {
	return x.Status == SIPOrdPending || x.Status == SIPOrdPlaced || x.Status == SIPOrdPartly
}

// reserves says whether the order holds the client's funds for the day.
func (x SIPOrder) reserves() bool {
	return x.Status == SIPOrdPending || x.Status == SIPOrdPlaced || x.Status == SIPOrdFilled || x.Status == SIPOrdPartly || x.Status == SIPOrdCheck
}

// SIPRunResult is what a run of the day's instalments did.
type SIPRunResult struct {
	Date    string         `json:"date"`
	Counts  map[string]int `json:"counts"`
	Orders  []SIPOrder     `json:"orders"`
	Funds   string         `json:"funds_note,omitempty"`
	Sent    *SIPSendResult `json:"sent,omitempty"`
	Message string         `json:"message"`
}

// RunStockSIPs works out the day's instalments. It is safe to run again:
// an instalment already worked out is left as it is.
func (o *Ops) RunStockSIPs(ctx context.Context, tenant, actor, date string) (SIPRunResult, error) {
	res := SIPRunResult{Date: date, Counts: map[string]int{}, Orders: []SIPOrder{}}
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Running stock SIP instalments", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return res, err
		}
	}
	cfg := o.sipSettings(ctx, tenant)
	if !cfg.Enabled {
		return res, fmt.Errorf("%w: stock SIPs are switched off for this firm", ErrState)
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return res, err
	}
	now := o.now()
	if date != now.Format("2006-01-02") {
		return res, invalid("instalments are worked out on the day they are placed (%s)", now.Format("2006-01-02"))
	}
	if !set.IsBusinessDay(date) {
		return res, invalid("%s is not a trading day", date)
	}
	sips, err := o.StockSIPs(ctx, tenant, "")
	if err != nil {
		return res, err
	}
	clients, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return res, err
	}
	look, asAt, err := o.liveLookup(ctx, tenant, date, now.Format("15:04:05"))
	if err != nil {
		return res, err
	}
	free, note := o.sipFreeFunds(ctx, tenant, date)
	res.Funds = note
	have := map[string]bool{}
	reserved := map[string]money.Dec{}
	existing, _ := docs.List[SIPOrder](ctx, o.Docs, tenant, cSIPOrder, date+"|")
	for _, x := range existing {
		have[x.ID] = true
		if x.reserves() {
			reserved[x.UCC] = reserved[x.UCC].Add(x.Estimate.Dec())
		}
	}
	topups := o.sipPendingTopups(ctx, tenant, date)
	mkt, _, _ := o.Market(ctx, tenant, date)
	srcClose := "last close (market data of " + mkt.Date + ")"
	sort.Slice(sips, func(i, j int) bool { return sips[i].ID < sips[j].ID })
	for _, s := range sips {
		if s.Status != SIPActive && s.Status != SIPPaused {
			continue
		}
		for _, sched := range s.scheduledFor(set, date) {
			id := date + "|" + s.ID + "|" + sched
			if have[id] {
				continue
			}
			x := o.sipEvaluate(ctx, tenant, cfg, s, clients[s.UCC], look, asAt, srcClose, date, sched, free[s.UCC].Sub(reserved[s.UCC]), topups[s.UCC])
			x.ID, x.CreatedAt = id, now.Format(time.RFC3339)
			if _, err := docs.Put(ctx, o.Docs, tenant, cSIPOrder, x.ID, x, 0, actor); err != nil {
				if errors.Is(err, docs.ErrStale) {
					continue // worked out by another run just now
				}
				return res, err
			}
			have[id] = true
			if x.reserves() {
				reserved[s.UCC] = reserved[s.UCC].Add(x.Estimate.Dec())
			}
			o.log(ctx, tenant, actor, "stocksip.instalment", x.ID, map[string]any{"ucc": x.UCC, "symbol": x.Symbol, "qty": x.Qty, "status": x.Status, "reason": x.Reason, "estimate": x.Estimate.String()})
			switch x.Status {
			case SIPOrdFailed:
				o.sipCount(ctx, tenant, actor, &x, false)
				o.sipNotify(ctx, tenant, actor, "stocksip.failed", x.UCC, map[string]string{"scrip": x.Symbol, "date": fmtDay(x.Date), "reason": x.Reason})
			case SIPOrdPending:
				o.sipOpenDate(ctx, tenant, date, true)
			}
			res.Counts[x.Status]++
			res.Orders = append(res.Orders, x)
		}
	}
	// SIPs whose pause ended start again; SIPs past their end date are
	// complete.
	for _, s := range sips {
		ended := s.End != "" && s.End < date && (s.Status == SIPActive || s.Status == SIPPaused)
		resumed := s.Status == SIPPaused && s.PausedUntil != "" && s.PausedUntil < date
		if !ended && !resumed {
			continue
		}
		_, _ = docs.Update(ctx, o.Docs, tenant, cStockSIP, s.ID, actor, func(x *StockSIP) error {
			switch {
			case x.End != "" && x.End < date && (x.Status == SIPActive || x.Status == SIPPaused):
				x.Status, x.PausedUntil = SIPCompleted, ""
				x.event(now.Format(time.RFC3339), actor, "Completed: the end date has passed")
			case x.Status == SIPPaused && x.PausedUntil != "" && x.PausedUntil < date:
				x.Status, x.PausedUntil = SIPActive, ""
				x.event(now.Format(time.RFC3339), actor, "Pause ended; active again")
			}
			return nil
		})
	}
	res.Message = fmt.Sprintf("%d instalments: %d ready for the OMS, %d failed, %d skipped", len(res.Orders), res.Counts[SIPOrdPending], res.Counts[SIPOrdFailed], res.Counts[SIPOrdSkipped])
	// An order API takes the orders straight away; a basket file waits for
	// the dealer.
	if cfg.Router == "xts" && res.Counts[SIPOrdPending] > 0 {
		sr, err := o.SendStockSIPOrders(ctx, tenant, actor, date)
		if err != nil {
			res.Message += "; not sent to the OMS: " + strings.TrimPrefix(err.Error(), "invalid: ")
		} else {
			res.Sent = &sr
			res.Message += "; " + sr.Message
		}
	}
	o.log(ctx, tenant, actor, "stocksip.run", date, map[string]any{"counts": res.Counts})
	return res, nil
}

// sipEvaluate works out one instalment.
func (o *Ops) sipEvaluate(ctx context.Context, tenant string, cfg SIPSettings, s StockSIP, c Client, look Lookup, asAt, srcClose, date, sched string, free, topup money.Dec) SIPOrder {
	x := SIPOrder{Ref: sipRef(date, s.ID, sched), SIP: s.ID, UCC: s.UCC, Name: s.Name, Exchange: s.Exchange, Symbol: s.Symbol, ISIN: s.ISIN, Scheduled: sched, Date: date,
		Basis: s.Basis, Amount: s.Amount, OrderType: cfg.OrderType, Protect: dec(cfg.ProtectionPct), Status: SIPOrdFailed}
	if cfg.AlgoTag {
		x.AlgoID = cfg.AlgoID
	}
	fail := func(f string, a ...any) SIPOrder { x.Status, x.Reason = SIPOrdFailed, fmt.Sprintf(f, a...); return x }
	switch {
	case contains(s.Skips, sched):
		x.Status, x.Reason = SIPOrdSkipped, "skipped at the client's request"
		return x
	case s.Status == SIPPaused && (s.PausedUntil == "" || sched <= s.PausedUntil):
		x.Status, x.Reason = SIPOrdSkipped, "SIP paused"
		return x
	}
	if c.UCC == "" {
		return fail("the client is no longer on record")
	}
	if why := sipClientBar(c); why != "" {
		return fail("%s", why)
	}
	inst, why := o.sipScrip(ctx, tenant, cfg, look, s.Exchange, s.Symbol, s.ISIN)
	if why != "" {
		return fail("%s", why)
	}
	px := sipPrice(inst)
	if px.Sign() <= 0 {
		return fail("no price for %s today", s.Symbol)
	}
	x.Price, x.PriceSrc = px, srcClose
	if asAt != "" && inst.LTP.Sign() > 0 {
		x.PriceSrc = "last traded price at " + asAt
	}
	switch s.Basis {
	case SIPQty:
		x.Qty = s.Qty
	default:
		x.Qty = floorDiv(s.Amount.Dec(), px)
		if x.Qty < 1 {
			return fail("₹%s does not buy one share of %s at ₹%s", formatINR(s.Amount.Dec()), s.Symbol, px.Fixed(2))
		}
	}
	if px.MulInt(x.Qty).Cmp(cfg.MinAmount.Dec()) < 0 {
		return fail("the order (%d × ₹%s) is below the firm's minimum of ₹%s", x.Qty, px.Fixed(2), formatINR(cfg.MinAmount.Dec()))
	}
	worst := px.Add(px.Pct(x.Protect))
	if cfg.OrderType == "LIMIT" {
		tick := dec(cfg.TickSize)
		if tick.Sign() <= 0 {
			tick = money.MustDec("0.05")
		}
		x.Limit = tick.MulInt(floorDiv(worst, tick))
		if x.Limit.Sign() <= 0 {
			return fail("no limit price could be set for %s", s.Symbol)
		}
		worst = x.Limit
	}
	value := worst.MulInt(x.Qty)
	need := value.Add(o.sipCharges(ctx, tenant, cfg, s.UCC, s.Exchange, date, x.Qty, worst))
	x.Estimate = need.CeilRupee().Round(money.UnitPaisa)
	x.Free, x.Topup = free.Round(money.UnitPaisa), topup.Round(money.UnitPaisa)
	if free.Add(topup).Cmp(x.Estimate.Dec()) < 0 {
		with := ""
		if topup.Sign() > 0 {
			with = fmt.Sprintf(", with a top-up of ₹%s on its way", formatINR(topup))
		}
		return fail("not enough free funds: the order needs about ₹%s with charges; free funds ₹%s%s", formatINR(x.Estimate.Dec()), formatINR(money.Max(free, money.Dec{})), with)
	}
	x.Status, x.Reason = SIPOrdPending, ""
	return x
}

// sipRef is the order's reference at the OMS: S, the trade date, the
// SIP's number and the scheduled day (15 characters).
func sipRef(date, sipID, sched string) string {
	d := strings.ReplaceAll(date, "-", "")
	return "S" + d[2:] + strings.TrimPrefix(sipID, "SIP-") + sched[8:10]
}

// floorDiv is a ÷ b rounded down to a whole number (0 for b ≤ 0).
func floorDiv(a, b money.Dec) int64 {
	q, err := a.Quo(b)
	if err != nil || b.Sign() <= 0 || q.Sign() <= 0 {
		return 0
	}
	n, _ := strconv.ParseInt(strings.SplitN(q.Fixed(0), ".", 2)[0], 10, 64)
	for n > 0 && money.DecInt(n).Cmp(q) > 0 {
		n--
	}
	for money.DecInt(n+1).Cmp(q) <= 0 {
		n++
	}
	return n
}

// sipCharges estimates the charges on a delivery buy: brokerage from the
// client's plan (the firm's SIP channel) or the firm's per-instalment
// brokerage, and the statutory charges from the rate schedule.
func (o *Ops) sipCharges(ctx context.Context, tenant string, cfg SIPSettings, ucc, exchange, date string, qty int64, price money.Dec) money.Dec {
	leg := charges.Leg{Bucket: charges.CMDelivery, Exchange: exchange, Side: "B", Qty: qty, Price: price, Date: date}
	rule, _, planned, err := o.QuoteRule(ctx, tenant, ucc, charges.RateQuery{Bucket: leg.Bucket, Exchange: exchange, Channel: cfg.BrokerageChannel}, date)
	planned = planned && err == nil
	perInst := !planned && cfg.BrokeragePerInstalment > 0
	leg.NoBrokerage = planned || perInst
	b, err := o.App.Schedule.Compute(leg)
	if err != nil {
		// No rate for the day: a cautious 0.5% of the order.
		return price.MulInt(qty).Pct(money.MustDec("0.5"))
	}
	total := b.Total.Dec()
	var brok money.Dec
	switch {
	case planned:
		brok = rule.Compute(leg.Bucket, price.MulInt(qty), qty, 1)
	case perInst:
		brok = cfg.BrokeragePerInstalment.Dec()
	}
	if brok.Sign() > 0 {
		total = total.Add(brok).Add(brok.Pct(o.App.Schedule.GSTPct))
	}
	return total
}

// sipFreeFunds is each client's free funds for buying: the ledger before
// the day's bills, less margin not covered by collateral, uncleared
// cheques and payouts approved but not yet paid. The note says when the
// margin could not be worked out.
func (o *Ops) sipFreeFunds(ctx context.Context, tenant, date string) (map[string]money.Dec, string) {
	out := map[string]money.Dec{}
	funds, err := o.clientFunds(ctx, tenant, date, date)
	if err != nil {
		return out, "funds could not be read: " + err.Error()
	}
	for u, a := range funds {
		out[u] = a.Dec()
	}
	for u, a := range o.uncleared(ctx, tenant) {
		out[u] = out[u].Sub(a.Dec())
	}
	prs, _ := docs.List[PayoutRequest](ctx, o.Docs, tenant, cPayout, "")
	for _, p := range prs {
		if p.Status == "Approved" && p.Batch == "" {
			out[p.UCC] = out[p.UCC].Sub(p.Amount.Dec())
		}
	}
	note := ""
	if rv, err := o.Risk(ctx, tenant, date); err == nil {
		for _, r := range rv.Rows {
			if m := money.Max(r.Required.Sub(r.CollUsable), money.Dec{}); m.Sign() > 0 {
				out[r.UCC] = out[r.UCC].Sub(m)
			}
		}
	} else {
		note = "margin could not be worked out (" + err.Error() + "), so it was not deducted from free funds"
	}
	return out, note
}

// sipCount records an instalment's outcome on its SIP once: a bought
// instalment clears the run of failures; a failed one adds to it, and the
// SIP stops at the firm's limit.
func (o *Ops) sipCount(ctx context.Context, tenant, actor string, x *SIPOrder, bought bool) {
	if x.Counted {
		return
	}
	x.Counted = true
	_ = docs.Upsert(ctx, o.Docs, tenant, cSIPOrder, x.ID, *x, actor)
	cfg := o.sipSettings(ctx, tenant)
	stopped := ""
	s, err := docs.Update(ctx, o.Docs, tenant, cStockSIP, x.SIP, actor, func(s *StockSIP) error {
		stopped = ""
		if bought {
			s.Failures, s.Done = 0, s.Done+1
		} else {
			s.Failures, s.Failed = s.Failures+1, s.Failed+1
		}
		if x.Date > s.LastDate {
			s.LastDate = x.Date
		}
		if !bought && s.Failures >= cfg.MaxFailures && (s.Status == SIPActive || s.Status == SIPPaused) {
			s.Status = SIPCancelled
			s.StopReason = fmt.Sprintf("%d instalments in a row were not bought", s.Failures)
			s.Consent.RevokedAt, s.Consent.RevokedBy = o.now().Format(time.RFC3339), SystemActor
			s.event(o.now().Format(time.RFC3339), SystemActor, "Stopped: "+s.StopReason)
			stopped = s.StopReason
		}
		if bought && s.End != "" && x.Scheduled >= s.End && s.Status == SIPActive {
			s.Status = SIPCompleted
			s.event(o.now().Format(time.RFC3339), SystemActor, "Completed: last instalment bought")
		}
		return nil
	})
	if err == nil && stopped != "" {
		o.log(ctx, tenant, actor, "stocksip.stopped", s.ID, map[string]any{"ucc": s.UCC, "reason": stopped})
		o.sipNotify(ctx, tenant, actor, "stocksip.cancelled", s.UCC, map[string]string{"scrip": s.Symbol, "reason": stopped})
	}
}

// ---- open dates (orders not yet final) ----

type sipOpenDates struct {
	Dates []string `json:"dates"`
}

func (o *Ops) sipOpenDate(ctx context.Context, tenant, date string, open bool) {
	if _, _, err := docs.Get[sipOpenDates](ctx, o.Docs, tenant, cSIPRun, "_open"); errors.Is(err, docs.ErrNotFound) {
		_, _ = docs.Put(ctx, o.Docs, tenant, cSIPRun, "_open", sipOpenDates{Dates: []string{}}, 0, SystemActor)
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cSIPRun, "_open", SystemActor, func(x *sipOpenDates) error {
		has := contains(x.Dates, date)
		switch {
		case open && !has:
			x.Dates = append(x.Dates, date)
			sort.Strings(x.Dates)
		case !open && has:
			var keep []string
			for _, d := range x.Dates {
				if d != date {
					keep = append(keep, d)
				}
			}
			x.Dates = keep
		}
		return nil
	})
}

// ---- matching the day's trades ----

// SIPMatchResult is what a matching run found.
type SIPMatchResult struct {
	Date    string         `json:"date"`
	Source  string         `json:"source"`
	Counts  map[string]int `json:"counts"`
	Changed []SIPOrder     `json:"changed"`
}

// sipTrades are the day's trades: the exchange trade files when loaded,
// else NSE's trade API, else the OMS trade book.
func (o *Ops) sipTrades(ctx context.Context, tenant, date string) ([]ingest.TradeEvent, string, bool) {
	if ex, _ := o.exchangeTrades(ctx, tenant, date); len(ex) > 0 {
		return ex, "exchange trade files", true
	}
	if nt, _ := o.NotisTrades(ctx, tenant, date); len(nt) > 0 {
		return nt, "NSE trade API (intraday)", false
	}
	oms, _ := o.App.Trades.List(ctx, tenant, ingest.FromOMS, date)
	return oms, "OMS trade book", false
}

// matchSIPTrades picks the trades that are a SIP order's fill: the
// client's delivery buys of the scrip on the order's exchange not already
// taken by another SIP order, by the OMS order ID when the trades carry
// it, then by the algo ID when SIP orders are tagged, then one exchange
// order whose quantity is the order's. A single smaller order is a part
// fill; anything else is left for a person to check.
func matchSIPTrades(x SIPOrder, ts []ingest.TradeEvent, taken map[string]bool) ([]ingest.TradeEvent, string) {
	var cands []ingest.TradeEvent
	for _, t := range ts {
		if t.UCC != x.UCC || t.Segment != "CM" || t.Side != "B" || t.Symbol != x.Symbol || t.Exchange != x.Exchange || taken[t.Key()] {
			continue
		}
		if t.Product != "" && t.Product != "CNC" {
			continue // intraday trades are not the SIP's
		}
		cands = append(cands, t)
	}
	if len(cands) == 0 {
		return nil, ""
	}
	if x.OMSOrder != "" {
		var by []ingest.TradeEvent
		for _, t := range cands {
			if t.OrderNo == x.OMSOrder {
				by = append(by, t)
			}
		}
		if len(by) > 0 {
			return by, "by the OMS order ID"
		}
	}
	if x.AlgoID != "" {
		var by []ingest.TradeEvent
		for _, t := range cands {
			if t.AlgoID == x.AlgoID {
				by = append(by, t)
			}
		}
		if len(by) > 0 {
			cands = by
		}
	}
	type group struct {
		key   string
		qty   int64
		first string
		ts    []ingest.TradeEvent
	}
	var gs []*group
	idx := map[string]*group{}
	for _, t := range cands {
		k := t.OrderNo
		if k == "" {
			k = "trade " + t.Key()
		}
		g := idx[k]
		if g == nil {
			g = &group{key: k, first: t.TradeTime}
			idx[k] = g
			gs = append(gs, g)
		}
		g.qty += t.Qty
		g.ts = append(g.ts, t)
		if t.TradeTime < g.first {
			g.first = t.TradeTime
		}
	}
	sort.SliceStable(gs, func(i, j int) bool { return gs[i].first < gs[j].first })
	for _, g := range gs {
		if g.qty == x.Qty {
			return g.ts, "client, scrip, date and quantity"
		}
	}
	if len(gs) == 1 && gs[0].qty < x.Qty {
		return gs[0].ts, "client, scrip and date (part of the quantity)"
	}
	return nil, fmt.Sprintf("%d buy order(s) of %s found for the client, none for %d shares: check and record the fill", len(gs), x.Symbol, x.Qty)
}

// MatchStockSIPFills matches a day's trades to its SIP orders. With final,
// orders still without trades lapse (unexecuted orders lapse at the end
// of the day) once the exchange's trade file for their exchange is in.
func (o *Ops) MatchStockSIPFills(ctx context.Context, tenant, actor, date string, final bool) (SIPMatchResult, error) {
	res := SIPMatchResult{Date: date, Counts: map[string]int{}, Changed: []SIPOrder{}}
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Matching stock SIP orders to trades", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return res, err
		}
	}
	if !validDate(date) {
		return res, invalid("give the trade date (YYYY-MM-DD)")
	}
	orders, err := docs.List[SIPOrder](ctx, o.Docs, tenant, cSIPOrder, date+"|")
	if err != nil {
		return res, err
	}
	ts, src, exch := o.sipTrades(ctx, tenant, date)
	res.Source = src
	loaded := map[string]bool{} // exchanges whose trade file is in
	if exch {
		for _, t := range ts {
			if t.Segment == "CM" {
				loaded[t.Exchange] = true
			}
		}
	}
	taken := map[string]bool{}
	for _, x := range orders {
		if !x.open() {
			for _, k := range x.Trades {
				taken[k] = true
			}
		}
	}
	sort.Slice(orders, func(i, j int) bool { return orders[i].Ref < orders[j].Ref })
	now := o.now().Format(time.RFC3339)
	anyOpen := false
	stillOpen := func(x SIPOrder) bool { return x.open() && !(final && x.Status == SIPOrdPartly) }
	for _, x := range orders {
		if !x.open() {
			continue
		}
		orig := x
		before := x.Status
		if x.Status != SIPOrdPending {
			picked, how := matchSIPTrades(x, ts, taken)
			if len(picked) > 0 {
				var q int64
				var v money.Dec
				x.Trades = nil
				for _, t := range picked {
					q += t.Qty
					v = v.Add(t.Value())
					x.Trades = append(x.Trades, t.Key())
					taken[t.Key()] = true
				}
				x.FilledQty, x.FillSrc = q, src+", matched by "+how
				x.AvgPrice, _ = v.Quo(money.DecInt(q))
				x.Status, x.Note = SIPOrdFilled, ""
				if q < x.Qty {
					x.Status = SIPOrdPartly
				}
			} else if how != "" {
				x.Note = how
			}
		}
		if final && x.Status == SIPOrdPlaced && (loaded[x.Exchange] || daysBetween(date, o.now().Format("2006-01-02")) > 5) {
			if x.Note != "" && strings.HasSuffix(x.Note, "record the fill") {
				x.Status = SIPOrdCheck
			} else {
				x.Status, x.Reason = SIPOrdLapsed, "the order was not executed by the end of the day"
			}
		}
		if final && x.Status == SIPOrdPending {
			x.Status, x.Reason = SIPOrdNotSent, "the order was not sent to the OMS on the day"
		}
		if x.Status == orig.Status && x.FilledQty == orig.FilledQty && x.Note == orig.Note && x.Reason == orig.Reason {
			if stillOpen(x) {
				anyOpen = true
			}
			continue
		}
		x.UpdatedAt = now
		if err := docs.Upsert(ctx, o.Docs, tenant, cSIPOrder, x.ID, x, actor); err != nil {
			return res, err
		}
		if x.Status != before {
			o.log(ctx, tenant, actor, "stocksip.fill", x.ID, map[string]any{"ucc": x.UCC, "symbol": x.Symbol, "status": x.Status, "filled": x.FilledQty, "source": src})
			switch x.Status {
			case SIPOrdFilled, SIPOrdPartly:
				if before == SIPOrdPlaced {
					o.sipCount(ctx, tenant, actor, &x, true)
					o.sipNotify(ctx, tenant, actor, "stocksip.filled", x.UCC, map[string]string{"scrip": x.Symbol, "qty": strconv.FormatInt(x.FilledQty, 10), "price": x.AvgPrice.Fixed(2),
						"value": formatINR(x.AvgPrice.MulInt(x.FilledQty)), "date": fmtDay(x.Date)})
				}
			case SIPOrdLapsed:
				o.sipCount(ctx, tenant, actor, &x, false)
				o.sipNotify(ctx, tenant, actor, "stocksip.failed", x.UCC, map[string]string{"scrip": x.Symbol, "date": fmtDay(x.Date), "reason": x.Reason})
			case SIPOrdNotSent:
				o.sipNotify(ctx, tenant, actor, "stocksip.failed", x.UCC, map[string]string{"scrip": x.Symbol, "date": fmtDay(x.Date), "reason": "the order could not be sent to the exchange on the day; it does not count against your SIP"})
			}
			res.Changed = append(res.Changed, x)
		}
		if stillOpen(x) {
			anyOpen = true
		}
		res.Counts[x.Status]++
	}
	if final && !anyOpen {
		o.sipOpenDate(ctx, tenant, date, false)
	}
	return res, nil
}

// RecordStockSIPFill records a fill a person checked (or that the trade
// files could not show), or marks an order that went nowhere as lapsed.
func (o *Ops) RecordStockSIPFill(ctx context.Context, tenant, actor, id string, qty int64, price money.Dec, note string) (SIPOrder, error) {
	if err := o.require(ctx, tenant, actor, "Recording a stock SIP fill", RoleMaker, RoleChecker); err != nil {
		return SIPOrder{}, err
	}
	x, _, err := docs.Get[SIPOrder](ctx, o.Docs, tenant, cSIPOrder, id)
	if err != nil {
		return x, err
	}
	if x.Status != SIPOrdPlaced && x.Status != SIPOrdPartly && x.Status != SIPOrdCheck && x.Status != SIPOrdLapsed {
		return x, fmt.Errorf("%w: order %s is %s", ErrState, x.Ref, x.Status)
	}
	if strings.TrimSpace(note) == "" {
		return x, invalid("say what you checked (trade numbers, the OMS order book)")
	}
	if qty < 0 || qty > x.Qty || (qty > 0 && price.Sign() <= 0) {
		return x, invalid("the quantity is 0 (not executed) to %d, with the average price when above 0", x.Qty)
	}
	before := x.Status
	switch {
	case qty == 0:
		x.Status, x.Reason = SIPOrdLapsed, "not executed (checked by "+actor+")"
	case qty < x.Qty:
		x.Status = SIPOrdPartly
	default:
		x.Status = SIPOrdFilled
	}
	x.FilledQty, x.AvgPrice, x.FillSrc, x.Note, x.UpdatedAt = qty, price, "recorded by "+actor, note, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cSIPOrder, x.ID, x, actor); err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "stocksip.fill.recorded", x.ID, map[string]any{"ucc": x.UCC, "qty": qty, "price": price.String(), "from": before, "note": note})
	if !x.Counted {
		o.sipCount(ctx, tenant, actor, &x, qty > 0)
		if qty > 0 {
			o.sipNotify(ctx, tenant, actor, "stocksip.filled", x.UCC, map[string]string{"scrip": x.Symbol, "qty": strconv.FormatInt(qty, 10), "price": price.Fixed(2), "value": formatINR(price.MulInt(qty)), "date": fmtDay(x.Date)})
		}
	}
	return x, nil
}

// ---- the scheduler ----

// SIPRunState is what the scheduler did on a day.
type SIPRunState struct {
	Date      string `json:"date"`
	TopupsAt  string `json:"topups_at,omitempty"`
	RanAt     string `json:"ran_at,omitempty"`
	Result    string `json:"result,omitempty"`
	MatchedAt string `json:"matched_at,omitempty"`
}

// stockSIPTick runs the stock SIP work that is due: top-up debits for the
// trading day after tomorrow (so the client has a day's notice of a debit
// tomorrow), the day's instalments at the placement time, and from 15:45
// matching trades back to orders every 15 minutes, lapsing what was not
// executed once the exchange's trade file is in.
func (o *Ops) stockSIPTick(ctx context.Context, tenant string, now time.Time) {
	cfg := o.sipSettings(ctx, tenant)
	if !cfg.Enabled {
		return
	}
	now = now.In(ist)
	today, hm := now.Format("2006-01-02"), now.Format("15:04")
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cSIPRun, today, SIPRunState{Date: today})
	changed := false
	if cfg.Topups && hm >= cfg.TopupAt && st.TopupsAt == "" {
		if td := now.AddDate(0, 0, 2).Format("2006-01-02"); set.IsBusinessDay(td) {
			if _, err := o.RaiseStockSIPTopups(ctx, tenant, SystemActor, td); err != nil {
				o.log(ctx, tenant, SystemActor, "stocksip.topups.error", td, map[string]any{"error": err.Error()})
			}
		}
		st.TopupsAt, changed = now.Format(time.RFC3339), true
	}
	if set.IsBusinessDay(today) && hm >= cfg.PlaceAt && hm < "15:30" && st.RanAt == "" {
		r, err := o.RunStockSIPs(ctx, tenant, SystemActor, today)
		st.RanAt, st.Result, changed = now.Format(time.RFC3339), r.Message, true
		if err != nil {
			st.Result = err.Error()
		}
	}
	due := hm >= "15:45"
	if t, err := time.Parse(time.RFC3339, st.MatchedAt); err == nil && now.Sub(t) < 15*time.Minute {
		due = false
	}
	if due {
		od, _, _ := docs.GetOr(ctx, o.Docs, tenant, cSIPRun, "_open", sipOpenDates{})
		for _, d := range od.Dates {
			if d > today {
				continue
			}
			_, _ = o.MatchStockSIPFills(ctx, tenant, SystemActor, d, d < today || hm >= "17:00")
		}
		st.MatchedAt, changed = now.Format(time.RFC3339), true
	}
	if changed {
		_ = docs.Upsert(ctx, o.Docs, tenant, cSIPRun, today, st, SystemActor)
	}
}
