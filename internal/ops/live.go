package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/billing"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- live RMS and front-office files ----
//
// During the day the OMS trade book is loaded as often as wanted (rows
// already loaded are skipped) and last prices come from LTP-SNAPSHOT
// files. Live risk then values each client as at the time chosen:
// derivative and intraday positions marked to market, margins, losses,
// charges on trades not yet billed, and the auction risk of delivery sells
// not covered by holdings. Breaches give a liquidation order file.
//
// Files for the trading front end: the beginning-of-day limit file and
// holdings file (with yesterday's delivery buys, so BTST sells pass), and
// at the end of the day the segment-wise allocation of each client's funds
// and collateral to the clearing corporation.

const cLivePx = "livepx"

// LivePrice is one last-traded price at a time of day.
type LivePrice struct {
	Exchange   string    `json:"exchange"`
	Segment    string    `json:"segment"`
	Instrument string    `json:"instrument"` // symbol|type|expiry|strike
	LTP        money.Dec `json:"ltp"`
	Time       string    `json:"time"`
}

// AuctionPct is the reserve for short delivery: auctions settle up to 20%
// above the close.
var AuctionPct = money.DecInt(20)

// liveLookup is the day's market with the latest LTP snapshot at or before
// until applied.
func (o *Ops) liveLookup(ctx context.Context, tenant, date, until string) (Lookup, string, error) {
	m, _, err := o.Market(ctx, tenant, date)
	if err != nil && !strings.Contains(err.Error(), "market data not loaded") {
		return Lookup{}, "", err
	}
	px, _ := docs.List[LivePrice](ctx, o.Docs, tenant, cLivePx, date+"|")
	latest := map[string]LivePrice{}
	asAt := ""
	for _, p := range px {
		if until != "" && p.Time > until {
			continue
		}
		k := p.Exchange + "|" + p.Segment + "|" + p.Instrument
		if p.Time >= latest[k].Time {
			latest[k] = p
		}
		if p.Time > asAt {
			asAt = p.Time
		}
	}
	ins := make([]Instrument, len(m.Instruments))
	copy(ins, m.Instruments)
	for i := range ins {
		x := &ins[i]
		key := x.Symbol + "|" + x.Type + "|" + x.Expiry + "|" + x.Strike
		if p, ok := latest[x.Exchange+"|"+x.Segment+"|"+key]; ok {
			x.LTP = p.LTP
		}
	}
	return newLookup(Market{Date: date, Instruments: ins}), asAt, nil
}

// LiveRow is one client's live risk.
type LiveRow struct {
	UCC        string    `json:"ucc"`
	Name       string    `json:"name"`
	Funds      money.Dec `json:"funds"` // ledger before today's bills
	Collateral money.Dec `json:"collateral"`
	Margin     money.Dec `json:"margin"` // derivatives and open intraday
	// ExchangeMargin is NSE Clearing's own margin for the client's
	// derivatives (margins API), when fetched today; Margin uses it when it
	// is higher than Ledgerline's estimate.
	ExchangeMargin *money.Dec `json:"exchange_margin,omitempty"`
	// PayoutHold is sale money awaiting pay-out held back from the client's
	// allocation and limit (payouthold.go); it comes off Free.
	PayoutHold *money.Dec `json:"payout_hold,omitempty"`
	MTM        money.Dec  `json:"mtm"`      // derivatives and intraday, at live prices
	Unbilled   money.Dec  `json:"unbilled"` // charges on today's trades
	Auction    money.Dec  `json:"auction"`  // short delivery reserve
	Turnover   money.Dec  `json:"turnover"`
	Free       money.Dec  `json:"free"`
	Status     string     `json:"status"`
	Blocked    bool       `json:"blocked"`
	Short      []string   `json:"short_delivery"`
	Trades     int        `json:"trades"`
}

// LiveView is the live risk screen.
type LiveView struct {
	Date      string                    `json:"date"`
	Until     string                    `json:"until"`
	PricesAt  string                    `json:"prices_at"`
	Source    string                    `json:"source"` // Exchange, OMS
	Trades    int                       `json:"trades"`
	LastTrade string                    `json:"last_trade"`
	Rows      []LiveRow                 `json:"rows"`
	Breaches  int                       `json:"breaches"`
	Positions map[string][]PositionView `json:"positions"`
	// ExchangeMarginAt is when NSE Clearing's client margins were fetched.
	ExchangeMarginAt string `json:"exchange_margin_at,omitempty"`
}

// LiveRisk values every client as at until (HH:MM:SS; "" for now).
func (o *Ops) LiveRisk(ctx context.Context, tenant, date, until string) (LiveView, error) {
	v := LiveView{Date: date, Until: until, Rows: []LiveRow{}}
	in, err := o.riskInput(ctx, tenant, date)
	if err != nil {
		return v, err
	}
	look, asAt, err := o.liveLookup(ctx, tenant, date, until)
	if err != nil {
		return v, err
	}
	in.look, v.PricesAt = look, asAt
	v.Source = "Exchange"
	// Provisional trade files fill the exchange segments whose final file
	// has not arrived (provtrades.go); a final file replaces them.
	if pt := o.provisionalTrades(ctx, tenant, date, in.trades); len(pt) > 0 {
		v.Source = "Exchange (provisional trade files)"
		if len(in.trades) > 0 {
			v.Source = "Exchange (final and provisional trade files)"
		}
		in.trades = append(in.trades, pt...)
	}
	if len(in.trades) == 0 {
		// During the day: trades from NSE's trade API, confirmed by the
		// exchange, before falling back to the trading front end's book.
		if nt, err := o.NotisTrades(ctx, tenant, date); err == nil && len(nt) > 0 {
			in.trades, v.Source = nt, "Exchange (NSE trade API, intraday)"
		}
	}
	if len(in.trades) == 0 {
		oms, err := o.App.Trades.List(ctx, tenant, ingest.FromOMS, date)
		if err != nil {
			return v, err
		}
		for _, t := range oms {
			t.Origin = ingest.FromExchange // estimate as if confirmed
			in.trades = append(in.trades, t)
		}
		v.Source = "OMS"
	}
	var trades []ingest.TradeEvent
	for _, t := range in.trades {
		if until == "" || t.TradeTime <= until {
			trades = append(trades, t)
			if t.TradeTime > v.LastTrade {
				v.LastTrade = t.TradeTime
			}
		}
	}
	in.trades = trades
	v.Trades = len(trades)
	cut := until
	if cut == "" {
		cut = "23:59:59"
	}
	pos, cm := in.positions(cut)
	v.Positions = pos
	coll := in.collateral()
	funds, err := o.clientFunds(ctx, tenant, date, date)
	if err != nil {
		return v, err
	}
	// Charges on trades not yet billed.
	unbilled := map[string]money.Dec{}
	if len(trades) > 0 {
		m, _, _ := o.Market(ctx, tenant, date)
		out, err := o.App.BuildBills(ctx, billing.Input{Tenant: tenant, Date: date, Actor: "live", Trades: trades, SettlementPrices: m.SettlementPrices(), Schedule: o.App.Schedule})
		if err == nil {
			for _, g := range out.Groups {
				unbilled[g.UCC] = unbilled[g.UCC].Add(g.Charges.Total.Dec())
			}
		}
	}
	// Intraday cash-market MTM and delivery sells beyond holdings.
	type leg struct {
		bq, sq  int64
		bv, sv  money.Dec
		ex, key string
		cncSell int64
	}
	cmLegs := map[string]map[string]*leg{}
	turnover := map[string]money.Dec{}
	count := map[string]int{}
	for _, t := range trades {
		count[t.UCC]++
		turnover[t.UCC] = turnover[t.UCC].Add(t.Value())
		if t.Segment != "CM" {
			continue
		}
		k := t.Exchange + "|" + t.Instrument()
		if cmLegs[t.UCC] == nil {
			cmLegs[t.UCC] = map[string]*leg{}
		}
		l := cmLegs[t.UCC][k]
		if l == nil {
			l = &leg{ex: t.Exchange, key: t.Instrument()}
			cmLegs[t.UCC][k] = l
		}
		if t.Side == "B" {
			l.bq, l.bv = l.bq+t.Qty, l.bv.Add(t.Value())
		} else {
			l.sq, l.sv = l.sq+t.Qty, l.sv.Add(t.Value())
			if t.Product == "CNC" {
				l.cncSell += t.Qty
			}
		}
	}
	held := map[string]int64{}
	for _, h := range in.holdings {
		held[h.UCC+"|"+h.Symbol] += h.Qty
	}
	// Shares CDSL accepted for early pay-in are delivered: a sale they
	// cover is not a short delivery. Holdings are the morning's, so they
	// may still include those shares; the larger of the two counts.
	for k, q := range o.epiCovered(ctx, tenant, date) {
		held[k] = max(held[k], q)
	}
	exMargin, exAt, _ := o.NCLMClientMargins(ctx, tenant, date)
	payoutHeld := o.payoutHeld(ctx, tenant, date)
	if len(exMargin) > 0 {
		v.ExchangeMarginAt = exAt
	}
	for _, u := range sortedKeys(in.clients) {
		c := in.clients[u]
		if c.Status != "Active" {
			continue
		}
		r := LiveRow{UCC: u, Name: c.Name, Funds: funds[u].Dec(), Collateral: coll[u], Unbilled: unbilled[u], Turnover: turnover[u], Trades: count[u], Blocked: c.Blocked || c.Inactive, Short: []string{}}
		deriv := money.Dec{}
		for _, p := range pos[u] {
			deriv = deriv.Add(p.Margin)
			r.MTM = r.MTM.Add(p.MTM)
		}
		// NSE Clearing's figure, when there is one, wins if it is higher.
		if ex, ok := exMargin[strings.ToUpper(u)]; ok {
			r.ExchangeMargin = &ex
			deriv = money.Max(deriv, ex)
		}
		r.Margin = cm[u].Add(deriv)
		for _, l := range cmLegs[u] {
			inst, ok := look.Get(l.ex, "CM", l.key)
			ltp := money.Dec{}
			if ok {
				ltp = inst.LTP
			}
			// Squared-off intraday quantity realises its P&L; open quantity is marked.
			m := min(l.bq, l.sq)
			if m > 0 {
				bp, _ := l.bv.Quo(money.DecInt(l.bq))
				sp, _ := l.sv.Quo(money.DecInt(l.sq))
				r.MTM = r.MTM.Add(sp.Sub(bp).MulInt(m))
			}
			sym := strings.Split(l.key, "|")[0]
			if net := l.sq - l.bq; net > 0 && l.cncSell > 0 {
				short := min(net, l.cncSell) - held[u+"|"+sym]
				if short > 0 && ltp.Sign() > 0 {
					r.Auction = r.Auction.Add(ltp.MulInt(short).Pct(AuctionPct))
					r.Short = append(r.Short, fmt.Sprintf("%s %d", sym, short))
				}
			}
		}
		cash := money.Max(r.Funds, money.Dec{}).Add(c.UPIBlock.Dec())
		usable := cash.Add(money.Min(r.Collateral, cash)).Add(money.Min(r.Funds, money.Dec{}))
		// Losses count at once; profits are not given intraday.
		r.Free = usable.Sub(r.Margin).Add(money.Min(r.MTM, money.Dec{})).Sub(r.Unbilled).Sub(r.Auction)
		if hs := payoutHeld[u]; len(hs) > 0 {
			var h money.Dec
			for _, v := range hs {
				h = h.Add(v)
			}
			r.PayoutHold, r.Free = &h, r.Free.Sub(h)
		}
		switch {
		case r.Free.Round(money.UnitPaisa) < 0 && (r.Margin.Sign() > 0 || r.MTM.Sign() < 0 || r.Auction.Sign() > 0):
			r.Status = "Breach"
			v.Breaches++
		case r.Free.Sign() < 0:
			r.Status = "Debit"
		case r.Trades > 0 || len(pos[u]) > 0:
			r.Status = "OK"
		default:
			r.Status = "Idle"
		}
		v.Rows = append(v.Rows, r)
	}
	sort.SliceStable(v.Rows, func(i, j int) bool { return v.Rows[i].Free.Cmp(v.Rows[j].Free) < 0 })
	return v, nil
}

// LiveLiquidationFile lists closing orders for breached clients, largest
// margin first, until the shortfall would be covered.
func (o *Ops) LiveLiquidationFile(ctx context.Context, tenant, date, until string) ([]byte, int, error) {
	v, err := o.LiveRisk(ctx, tenant, date, until)
	if err != nil {
		return nil, 0, err
	}
	rows := [][]string{{"ClientCode", "Exchange", "Segment", "Instrument", "Side", "Quantity", "OrderType", "Reason"}}
	n := 0
	for _, r := range v.Rows {
		if r.Status != "Breach" {
			continue
		}
		ps := append([]PositionView{}, v.Positions[r.UCC]...)
		sort.Slice(ps, func(i, j int) bool { return ps[i].Margin.Cmp(ps[j].Margin) > 0 })
		need := r.Free.Neg()
		for _, p := range ps {
			if need.Sign() <= 0 {
				break
			}
			side := "S"
			if p.Qty < 0 {
				side = "B"
			}
			rows = append(rows, []string{r.UCC, p.Exchange, p.Segment, p.Instrument, side, fmt.Sprint(abs(p.Qty)), "MARKET", "Live RMS shortfall ₹" + formatINR(r.Free.Neg())})
			need = need.Sub(p.Margin).Add(money.Min(p.MTM, money.Dec{}))
			n++
		}
	}
	return csvBytesOf(rows), n, nil
}

// LimitFile is the beginning-of-day limit upload for the trading front end,
// from the previous day's closing risk.
func (o *Ops) LimitFile(ctx context.Context, tenant, date string) ([]byte, error) {
	set, _ := o.Settings(ctx, tenant)
	prev := set.PrevBusinessDay(date)
	rv, err := o.Risk(ctx, tenant, prev)
	if err != nil {
		return nil, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	rows := [][]string{{"TradeDate", "ClientCode", "Cash", "CollateralAfterHaircut", "MarginBlocked", "Override", "NetLimit", "BuyAllowed", "Reason", "SegmentsAllowed"}}
	for _, r := range rv.Rows {
		buy, why := "Y", ""
		open := cs[r.UCC].OpenSegments()
		switch {
		case r.Blocked && r.Inactive:
			buy, why = "N", "inactive account"
		case r.Blocked:
			buy, why = "N", "blocked"
		case len(open) == 0:
			buy, why = "N", "no segment active ("+segStatusSummary(cs[r.UCC])+")"
		}
		rows = append(rows, []string{date, r.UCC, r.Cash.Fixed(2), r.CollUsable.Fixed(2), r.Required.Fixed(2), r.Overrides.Fixed(2), r.Limit.Fixed(2), buy, why, strings.Join(open, "/")})
	}
	// Ticked off for the day: pay-out holds wait for the BOD files (payouthold.go).
	o.noteDailyFile(ctx, tenant, SystemActor, payoutBODLimitFile, "BOD limit file", date)
	return csvBytesOf(rows), nil
}

// HoldingsFile is the beginning-of-day holdings upload for the trading
// front end: demat balances, pledged and CUSPA quantities, and the previous
// day's delivery buys not yet received (so they can be sold BTST).
func (o *Ops) HoldingsFile(ctx context.Context, tenant, date string) ([]byte, error) {
	set, _ := o.Settings(ctx, tenant)
	prev := set.PrevBusinessDay(date)
	hs, err := o.Holdings(ctx, tenant, "")
	if err != nil {
		return nil, err
	}
	type row struct {
		ucc, isin, sym     string
		demat, pledged, t1 int64
	}
	all := map[string]*row{}
	get := func(u, isin, sym string) *row {
		k := u + "|" + isin
		if all[k] == nil {
			all[k] = &row{ucc: u, isin: isin, sym: sym}
		}
		return all[k]
	}
	for _, h := range hs {
		r := get(h.UCC, h.ISIN, h.Symbol)
		r.demat += h.Qty
		r.pledged += h.Pledged
	}
	ts, _ := o.exchangeTrades(ctx, tenant, prev)
	_, look, _ := o.Market(ctx, tenant, prev)
	for _, t := range ts {
		if t.Segment != "CM" || t.Product != "CNC" {
			continue
		}
		isin := t.Symbol
		if i, ok := look.ForTrade(t); ok && i.ISIN != "" {
			isin = i.ISIN
		}
		r := get(t.UCC, isin, t.Symbol)
		if t.Side == "B" {
			r.t1 += t.Qty
		} else {
			r.t1 -= t.Qty
		}
	}
	rows := [][]string{{"TradeDate", "ClientCode", "ISIN", "Symbol", "DematQty", "PledgedQty", "T1Qty", "SellableQty"}}
	for _, k := range sortedKeys(all) {
		r := all[k]
		t1 := max(r.t1, 0)
		if r.demat == 0 && t1 == 0 {
			continue
		}
		rows = append(rows, []string{date, r.ucc, r.isin, r.sym, fmt.Sprint(r.demat), fmt.Sprint(r.pledged), fmt.Sprint(t1), fmt.Sprint(r.demat + t1)})
	}
	return csvBytesOf(rows), nil
}

// AllocationRow is one client's segment allocation to the clearing corporation.
type AllocationRow struct {
	UCC      string    `json:"ucc"`
	Segment  string    `json:"segment"`
	Cash     money.Dec `json:"cash"`
	NonCash  money.Dec `json:"non_cash"`
	Required money.Dec `json:"required"`
}

// Allocation splits each client's funds and collateral across segments for
// the clearing corporation's client-level allocation (end of day): first to
// cover each segment's margin, the remainder to the client's most active
// segment (equity cash when none). A pay-out hold active on date
// (payouthold.go) lowers the held client's cash allocation.
func (o *Ops) Allocation(ctx context.Context, tenant, date string) ([]AllocationRow, error) {
	rows, err := o.allocationBase(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	return o.withPayoutHolds(ctx, tenant, date, rows), nil
}

// allocationBase is Allocation without pay-out holds.
func (o *Ops) allocationBase(ctx context.Context, tenant, date string) ([]AllocationRow, error) {
	rv, err := o.Risk(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	var out []AllocationRow
	for _, r := range rv.Rows {
		need := map[string]money.Dec{}
		for _, p := range rv.Positions[r.UCC] {
			need[p.Segment] = need[p.Segment].Add(p.Margin)
		}
		cash, noncash := money.Max(r.Funds, money.Dec{}), r.CollUsable
		if cash.Sign() == 0 && noncash.Sign() == 0 {
			continue
		}
		segs := sortedKeys(need)
		sort.SliceStable(segs, func(i, j int) bool { return need[segs[i]].Cmp(need[segs[j]]) > 0 })
		alloc := map[string]*AllocationRow{}
		at := func(s string) *AllocationRow {
			if alloc[s] == nil {
				alloc[s] = &AllocationRow{UCC: r.UCC, Segment: s, Required: need[s]}
			}
			return alloc[s]
		}
		for _, s := range segs {
			req := need[s]
			// Half of margin must be cash; collateral covers the rest.
			half, _ := req.Quo(money.DecInt(2))
			c := money.Min(cash, money.Max(req.Sub(money.Min(noncash, half)), money.Dec{}))
			nc := money.Min(noncash, req.Sub(c))
			a := at(s)
			a.Cash, a.NonCash = c, nc
			cash, noncash = cash.Sub(c), noncash.Sub(nc)
		}
		rest := "CM"
		if len(segs) > 0 {
			rest = segs[0]
		}
		if cash.Sign() > 0 || noncash.Sign() > 0 {
			a := at(rest)
			a.Cash, a.NonCash = a.Cash.Add(cash), a.NonCash.Add(noncash)
		}
		for _, s := range sortedKeys(alloc) {
			out = append(out, *alloc[s])
		}
	}
	return out, nil
}

// AllocationFile is the allocation in upload form.
func (o *Ops) AllocationFile(ctx context.Context, tenant, date string) ([]byte, error) {
	rows, err := o.Allocation(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	out := [][]string{{"Date", "MemberCode", "ClientCode", "Segment", "CashAllocated", "NonCashAllocated", "MarginRequired"}}
	for _, r := range rows {
		out = append(out, []string{date, set.MemberCodes["NSE"], r.UCC, r.Segment, r.Cash.Fixed(2), r.NonCash.Fixed(2), r.Required.Fixed(2)})
	}
	return csvBytesOf(out), nil
}

// loadLivePrices stores an LTP-SNAPSHOT file.
func (o *Ops) loadLivePrices(ctx context.Context, tenant, actor, date string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	var ps []LivePrice
	for _, r := range rows {
		ltp, err := money.ParseDec(r.get("LTP"))
		t := r.get("Time")
		if err != nil || ltp.Sign() <= 0 {
			errs = append(errs, ingest.ParseError{Line: r.line, Field: "LTP", Msg: "must be a positive price"})
			continue
		}
		if len(t) != 8 || t[2] != ':' || t[5] != ':' {
			errs = append(errs, ingest.ParseError{Line: r.line, Field: "Time", Msg: "must be HH:MM:SS"})
			continue
		}
		seg := strings.ToUpper(r.get("Segment"))
		typ := strings.ToUpper(r.get("InstrumentType"))
		if typ == "" {
			typ = "EQ"
		}
		key := strings.ToUpper(r.get("Symbol")) + "|" + typ + "|" + r.get("Expiry") + "|" + r.get("Strike")
		ps = append(ps, LivePrice{Exchange: strings.ToUpper(r.get("Exchange")), Segment: seg, Instrument: key, LTP: ltp, Time: t})
	}
	return func() (int, int, error) {
		for _, p := range ps {
			if err := docs.Upsert(ctx, o.Docs, tenant, cLivePx, date+"|"+p.Exchange+"|"+p.Segment+"|"+p.Instrument+"|"+p.Time, p, actor); err != nil {
				return 0, 0, err
			}
		}
		return len(ps), 0, nil
	}, errs
}
