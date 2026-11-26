package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- trading surveillance, round 6 of the compliance gap report ----
//
//   - Bulk deals (SEBI/MRD/SE/Cir-7/2004, appendix GAP-0105): a client's or
//     the firm's own trades in a scrip on one exchange in a day above 0.5%
//     of the company's equity shares listed there are disclosed to the
//     exchange: a single trade at once, the day's total within an hour of
//     the close. No exchange file Ledgerline reads carries the number of
//     listed shares (the NSE security master, BSE scrip and series file,
//     the UDiFF bhavcopy and CDSL's ISIN master do not), so the firm keeps
//     a table of shares outstanding by ISIN, uploaded from the exchanges'
//     or companies' shareholding data; a traded scrip without a number is
//     listed as not checked.
//   - Self and wash trades (SEBI PFUTP Regulations 4(2)(a)-(b), (g);
//     appendix GAP-0060): the trade files carry no counterparty, so
//     Ledgerline can find only what the firm's own trades show: the same
//     client buying and selling the same contract within a few minutes in
//     near-equal quantity at near the same price, and the same across two
//     client codes of one PAN. A match between two different firms'
//     clients needs the exchange's counterparty data (its alerts).
//   - Abnormal client transactions (NSE/SURV/74008, item 34, NSE-0223):
//     trades far from the day's closing price (a possible profit or loss
//     transfer), repeated same-day reversals in the same scrip (possible
//     artificial volume), large trades in the firm's list of illiquid
//     scrips (the scrip group ILLIQUID, loaded from the exchanges' list),
//     and large expiry-day option round trips. Each is an alert whose case
//     work records the examination in writing (survcase.go). The pre-order
//     checks the item also asks for are the front end's (Finspot's RMS).
//   - BSE's client code modification penalty file (BSE notices on penalty
//     for CCMs; SEBI stock broker master circular 2025/90, 38.3): BSE's
//     columns are not published to Ledgerline, so the layout
//     (config/formats/bse-ccm-penalty.json) is read by header names and is
//     unconfirmed. Each row is matched with Ledgerline's CCM register; a
//     modification BSE penalised that the register lacks is an alert, and
//     the month's penalty is a work item for compliance.

const (
	cSharesOut    = "sharesout" // ISIN -> SharesOutstanding
	cSurvB6       = "survb6"    // config; ccmpen|<month>
	illiquidGroup = "ILLIQUID"
)

// SharesOutstanding is a scrip's listed equity shares.
type SharesOutstanding struct {
	ISIN   string `json:"isin"`
	Symbol string `json:"symbol,omitempty"`
	Shares int64  `json:"shares"`
	AsOf   string `json:"as_of"`
	Source string `json:"source,omitempty"`
	By     string `json:"by"`
}

// SurvB6Config are the thresholds of the round-6 rules (the firm may
// change them; they are risk-based, not prescribed).
type SurvB6Config struct {
	WashMinutes    int    `json:"wash_minutes"`     // buy and sell within this many minutes
	WashQtyPct     int    `json:"wash_qty_pct"`     // quantities within this % of each other
	WashPricePct   string `json:"wash_price_pct"`   // prices within this %
	FarPctOptions  int    `json:"far_pct_options"`  // an option traded this % away from the close
	FarPctOther    int    `json:"far_pct_other"`    // other contracts
	ReversalDays   int    `json:"reversal_days"`    // same-day reversals on this many of the last 10 trading days
	IlliquidValue  int64  `json:"illiquid_value"`   // rupees a day in an illiquid scrip
	ExpiryPLRupees int64  `json:"expiry_pl_rupees"` // expiry-day option round trip profit or loss
	By             string `json:"by,omitempty"`
}

func (c SurvB6Config) withDefaults() SurvB6Config {
	if c.WashMinutes <= 0 {
		c.WashMinutes = 5
	}
	if c.WashQtyPct <= 0 {
		c.WashQtyPct = 10
	}
	if _, err := money.ParseDec(c.WashPricePct); err != nil || c.WashPricePct == "" {
		c.WashPricePct = "0.5"
	}
	if c.FarPctOptions <= 0 {
		c.FarPctOptions = 50
	}
	if c.FarPctOther <= 0 {
		c.FarPctOther = 10
	}
	if c.ReversalDays <= 0 {
		c.ReversalDays = 3
	}
	if c.IlliquidValue <= 0 {
		c.IlliquidValue = 100_000
	}
	if c.ExpiryPLRupees <= 0 {
		c.ExpiryPLRupees = 50_000
	}
	return c
}

// SurvB6Settings is the firm's thresholds.
func (o *Ops) SurvB6Settings(ctx context.Context, tenant string) SurvB6Config {
	c, _, _ := docs.GetOr(ctx, o.Docs, tenant, cSurvB6, "config", SurvB6Config{})
	return c.withDefaults()
}

// SaveSurvB6Settings changes the thresholds (compliance officer).
func (o *Ops) SaveSurvB6Settings(ctx context.Context, tenant, actor string, c SurvB6Config) (SurvB6Config, error) {
	if err := o.require(ctx, tenant, actor, "Changing surveillance thresholds", RoleCompliance); err != nil {
		return c, err
	}
	c = c.withDefaults()
	c.By = actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cSurvB6, "config", c, actor); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "surveillance.thresholds", "round6", map[string]any{"config": c})
	return c, nil
}

// ---- shares outstanding ----

// LoadSharesOutstanding reads a CSV (or pipe-, tab-separated) file with
// the headings ISIN, Shares (or Listed shares, Issued shares, No of
// shares) and optionally Symbol and As of; each row replaces the ISIN's
// number.
func (o *Ops) LoadSharesOutstanding(ctx context.Context, tenant, actor, source string, data []byte) (int, error) {
	if err := o.require(ctx, tenant, actor, "Loading shares outstanding", RoleCompliance, RoleChecker); err != nil {
		return 0, err
	}
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	r.Comma, r.FieldsPerRecord, r.LazyQuotes = survDelim(data), -1, true
	recs, err := r.ReadAll()
	if err != nil || len(recs) < 2 {
		return 0, invalid("the file needs a row of headings (ISIN, Shares) and at least one row")
	}
	col := map[string]int{}
	for j, h := range recs[0] {
		switch k := survHead(h); k {
		case "isin", "isincode":
			col["isin"] = j
		case "symbol", "scrip", "scripcode", "securityname":
			col["symbol"] = j
		case "shares", "listedshares", "issuedshares", "noofshares", "numberofshares", "sharesoutstanding", "totalshares", "issuedcapitalshares":
			col["shares"] = j
		case "asof", "date", "asondate", "ason":
			col["asof"] = j
		}
	}
	if _, ok := col["isin"]; !ok {
		return 0, invalid("no ISIN column in the headings")
	}
	if _, ok := col["shares"]; !ok {
		return 0, invalid("no Shares column in the headings (Shares, Listed shares, Issued shares, No of shares)")
	}
	today := o.now().In(ist).Format("2006-01-02")
	get := func(rec []string, k string) string {
		if j, ok := col[k]; ok && j < len(rec) {
			return strings.TrimSpace(rec[j])
		}
		return ""
	}
	n := 0
	var bad []string
	for i, rec := range recs[1:] {
		isin := strings.ToUpper(get(rec, "isin"))
		sh, err := strconv.ParseInt(strings.ReplaceAll(get(rec, "shares"), ",", ""), 10, 64)
		if !reISIN.MatchString(isin) || err != nil || sh <= 0 {
			if len(bad) < 10 {
				bad = append(bad, fmt.Sprintf("line %d", i+2))
			}
			continue
		}
		asof := today
		if d, ok := parseListDate(get(rec, "asof")); ok {
			asof = d
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cSharesOut, isin, SharesOutstanding{ISIN: isin, Symbol: strings.ToUpper(get(rec, "symbol")), Shares: sh, AsOf: asof, Source: strings.TrimSpace(source), By: actor}, actor); err != nil {
			return n, err
		}
		n++
	}
	o.log(ctx, tenant, actor, "surveillance.shares_outstanding", "", map[string]any{"rows": n, "bad": bad, "source": source})
	if n == 0 {
		return 0, invalid("no row had an ISIN and a whole number of shares (%s)", strings.Join(bad, ", "))
	}
	return n, nil
}

// ---- the checks on a day's trades ----

// TradeCheckResult is what the round-6 rules found on a day.
type TradeCheckResult struct {
	Date       string   `json:"date"`
	Raised     int      `json:"raised"`
	BulkDeals  []string `json:"bulk_deals"`
	NotChecked []string `json:"bulk_not_checked"` // scrips traded with no shares-outstanding number
	Needs      string   `json:"needs"`
}

type b6Env struct {
	o      *Ops
	ctx    context.Context
	tenant string
	actor  string
	date   string
	cfg    SurvB6Config
	cs     map[string]Client
	res    *TradeCheckResult
}

func (e *b6Env) raise(a Alert, key string) {
	a.Date = firstOf(a.Date, e.date)
	if a.Source == "" {
		a.Source = "Internal"
	}
	if e.o.raiseAlert(e.ctx, e.tenant, e.actor, a, key) {
		e.res.Raised++
	}
}

// RunTradePatterns runs the round-6 rules on a day's trades.
func (o *Ops) RunTradePatterns(ctx context.Context, tenant, actor, date string) (TradeCheckResult, error) {
	res := TradeCheckResult{Date: date, BulkDeals: []string{}, NotChecked: []string{},
		Needs: "Self and wash trades between the firm's clients and other members' clients need the exchange's counterparty data: Ledgerline sees only the firm's side."}
	if err := o.require(ctx, tenant, actor, "Running surveillance rules", RoleCompliance, RoleChecker); err != nil {
		return res, err
	}
	return o.runTradePatterns(ctx, tenant, actor, date)
}

func (o *Ops) runTradePatterns(ctx context.Context, tenant, actor, date string) (TradeCheckResult, error) {
	res := TradeCheckResult{Date: date, BulkDeals: []string{}, NotChecked: []string{},
		Needs: "Self and wash trades between the firm's clients and other members' clients need the exchange's counterparty data: Ledgerline sees only the firm's side."}
	if !validDate(date) {
		return res, invalid("give a date (YYYY-MM-DD)")
	}
	if !o.b6Need(ctx, tenant, "broker") {
		return res, nil
	}
	trades, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return res, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	e := &b6Env{o: o, ctx: ctx, tenant: tenant, actor: actor, date: date, cfg: o.SurvB6Settings(ctx, tenant), cs: cs, res: &res}
	_, look, _ := o.Market(ctx, tenant, date)
	e.bulk(trades, look)
	e.wash(trades)
	e.farFromClose(trades, look)
	e.reversals(trades)
	e.illiquid(trades)
	e.expiry(trades)
	o.log(ctx, tenant, actor, "surveillance.trade_patterns", date, map[string]any{"raised": res.Raised, "bulk": len(res.BulkDeals), "not_checked": len(res.NotChecked)})
	return res, nil
}

// bulk: a client's (or the firm's) day total in a scrip on an exchange
// above 0.5% of the listed shares.
func (e *b6Env) bulk(ts []ingest.TradeEvent, look Lookup) {
	type agg struct {
		qty, big int64
		isin     string
		sym      string
	}
	by := map[string]*agg{}
	missing := map[string]bool{}
	shares := map[string]int64{}
	for _, t := range ts {
		if t.Segment != "CM" {
			continue
		}
		isin := ""
		if i, ok := look.ForTrade(t); ok {
			isin = i.ISIN
		}
		if isin == "" {
			missing[t.Exchange+" "+t.Symbol] = true
			continue
		}
		if _, ok := shares[isin]; !ok {
			so, _, err := docs.Get[SharesOutstanding](e.ctx, e.o.Docs, e.tenant, cSharesOut, isin)
			shares[isin] = 0
			if err == nil {
				shares[isin] = so.Shares
			}
		}
		if shares[isin] == 0 {
			missing[t.Exchange+" "+t.Symbol] = true
			continue
		}
		k := t.UCC + "|" + t.Exchange + "|" + isin + "|" + t.Side
		a := by[k]
		if a == nil {
			a = &agg{isin: isin, sym: t.Symbol}
			by[k] = a
		}
		a.qty += t.Qty
		if t.Qty*200 > shares[isin] { // a single trade above 0.5%
			a.big += t.Qty
		}
	}
	for _, k := range sortedKeys(by) {
		a := by[k]
		sh := shares[a.isin]
		if a.qty*200 <= sh {
			continue
		}
		p := strings.Split(k, "|")
		u, ex, side := p[0], p[1], map[string]string{"B": "bought", "S": "sold"}[p[3]]
		pct := money.DecInt(a.qty * 100)
		pc, _ := pct.Quo(money.DecInt(sh))
		when := "the day's total is reported to " + ex + " within an hour of the close"
		if a.big > 0 {
			when = "a single trade was above 0.5%: it is reported to " + ex + " at once, and the day's total within an hour of the close"
		}
		e.res.BulkDeals = append(e.res.BulkDeals, fmt.Sprintf("%s %s %s %d (%s%%)", u, side, a.sym, a.qty, pc.Fixed(2)))
		e.raise(Alert{Type: "Bulk deal: above 0.5% of listed shares", UCC: u, Severity: "Medium", ISIN: a.isin, Qty: strconv.FormatInt(a.qty, 10), TxnFrom: e.date, TxnTo: e.date,
			Detail: fmt.Sprintf("%s %s %d shares of %s on %s on %s: %s%% of the %d shares listed (SEBI/MRD/SE/Cir-7/2004). Disclose it with the client's name, quantity and price: %s.", u, side, a.qty, a.sym, ex, e.date, pc.Fixed(2), sh, when)},
			"BULKCHK:"+e.date+":"+shortHash(k))
		e.o.workDue(e.ctx, e.tenant, "bulkdeal", e.date+":"+shortHash(k), "Report the bulk deal of "+u+" in "+a.sym+" to "+ex,
			fmt.Sprintf("%s %s %d shares (%s%% of the listed shares) on %s: %s.", u, side, a.qty, pc.Fixed(2), e.date, when), u, RoleCompliance, "surv", e.date, e.date+" 16:30")
	}
	e.res.NotChecked = append(e.res.NotChecked, sortedKeys(missing)...)
}

// wash: the same client (or two codes of one PAN) on both sides of a
// contract within minutes, near-equal quantity and price.
func (e *b6Env) wash(ts []ingest.TradeEvent) {
	pan := map[string]string{}
	for u, c := range e.cs {
		pan[u] = firstOf(strings.ToUpper(c.PAN), u)
	}
	type leg struct {
		t   ingest.TradeEvent
		sec int
	}
	by := map[string][]leg{}
	for _, t := range ts {
		s := tradeSecond(t.TradeTime)
		if s < 0 {
			continue
		}
		p := firstOf(pan[t.UCC], t.UCC)
		by[p+"|"+t.Exchange+"|"+t.Segment+"|"+t.Instrument()] = append(by[p+"|"+t.Exchange+"|"+t.Segment+"|"+t.Instrument()], leg{t, s})
	}
	win := e.cfg.WashMinutes * 60
	pp, _ := money.ParseDec(e.cfg.WashPricePct)
	for _, k := range sortedKeys(by) {
		ls := by[k]
		sort.SliceStable(ls, func(i, j int) bool { return ls[i].sec < ls[j].sec })
		seen := false
		for i := 0; i < len(ls) && !seen; i++ {
			for j := i + 1; j < len(ls) && ls[j].sec-ls[i].sec <= win; j++ {
				a, b := ls[i].t, ls[j].t
				if a.Side == b.Side {
					continue
				}
				lo, hi := min(a.Qty, b.Qty), max(a.Qty, b.Qty)
				if hi == 0 || (hi-lo)*100 > hi*int64(e.cfg.WashQtyPct) {
					continue
				}
				diff := a.Price.Sub(b.Price).Abs().MulInt(100)
				if a.Price.Sign() == 0 || diff.Cmp(a.Price.Mul(pp)) > 0 {
					continue
				}
				what, typ := "the same client "+a.UCC, "Possible self or wash trade (same client)"
				if a.UCC != b.UCC {
					what, typ = "client codes "+a.UCC+" and "+b.UCC+" of one PAN", "Possible self trade across client codes of one PAN"
				}
				e.raise(Alert{Type: typ, UCC: a.UCC, Severity: "Medium", TxnFrom: e.date, TxnTo: e.date,
					Detail: fmt.Sprintf("%s bought and sold %s on %s within %d minutes (%d at %s, %d at %s; trades %s and %s). Ledgerline cannot see the counterparty: check with the exchange's alerts whether the trades matched each other (SEBI PFUTP Regulations 4(2)(a)-(b), (g)).",
						what, InstrumentName(a.Symbol, a.InstrumentType, a.Expiry, a.Strike), a.Exchange, e.cfg.WashMinutes, a.Qty, a.Price.Fixed(2), b.Qty, b.Price.Fixed(2), a.TradeNo, b.TradeNo)},
					"WASH:"+e.date+":"+shortHash(k))
				seen = true
				break
			}
		}
	}
}

// farFromClose: trades far from the contract's closing price.
func (e *b6Env) farFromClose(ts []ingest.TradeEvent, look Lookup) {
	worst := map[string]ingest.TradeEvent{}
	dev := map[string]money.Dec{}
	for _, t := range ts {
		i, ok := look.ForTrade(t)
		if !ok || i.Close.Sign() <= 0 || t.Price.Sign() <= 0 {
			continue
		}
		lim := int64(e.cfg.FarPctOther)
		if t.InstrumentType == "CE" || t.InstrumentType == "PE" {
			lim = int64(e.cfg.FarPctOptions)
		}
		d := t.Price.Sub(i.Close).Abs().MulInt(100)
		if d.Cmp(i.Close.MulInt(lim)) <= 0 {
			continue
		}
		pct, _ := d.Quo(i.Close)
		k := t.UCC + "|" + t.Exchange + "|" + t.Instrument()
		if pct.Cmp(dev[k]) > 0 {
			dev[k], worst[k] = pct, t
		}
	}
	for _, k := range sortedKeys(worst) {
		t := worst[k]
		e.raise(Alert{Type: "Trade far from the closing price (possible profit or loss transfer)", UCC: t.UCC, Severity: "Medium", TxnFrom: e.date, TxnTo: e.date,
			Detail: fmt.Sprintf("%s traded %s on %s at %s, %s%% from the day's close (NSE/SURV/74008 item 34). Examine who was on the other side and why the price; record the finding.", t.UCC, InstrumentName(t.Symbol, t.InstrumentType, t.Expiry, t.Strike), t.Exchange, t.Price.Fixed(2), dev[k].Fixed(0))},
			"FAR:"+e.date+":"+shortHash(k))
	}
}

// reversals: the same client bought and sold the same quantity of a cash
// scrip on the same day, on several of the last 10 trading days.
func (e *b6Env) reversals(today []ingest.TradeEvent) {
	set, _ := e.o.Settings(e.ctx, e.tenant)
	days := []string{e.date}
	d := e.date
	for len(days) < 10 {
		d = set.PrevBusinessDay(d)
		if d == "" {
			break
		}
		days = append(days, d)
	}
	count := map[string]int{}
	for i, day := range days {
		ts := today
		if i > 0 {
			ts, _ = e.o.exchangeTrades(e.ctx, e.tenant, day)
		}
		net := map[string][2]int64{}
		for _, t := range ts {
			if t.Segment != "CM" {
				continue
			}
			k := t.UCC + "|" + t.Exchange + "|" + t.Symbol
			v := net[k]
			if t.Side == "B" {
				v[0] += t.Qty
			} else {
				v[1] += t.Qty
			}
			net[k] = v
		}
		for k, v := range net {
			if v[0] > 0 && v[0] == v[1] {
				count[k]++
			}
		}
	}
	for _, k := range sortedKeys(count) {
		if count[k] < e.cfg.ReversalDays {
			continue
		}
		p := strings.Split(k, "|")
		// Raised only when today is one of the days.
		found := false
		for _, t := range today {
			if t.UCC == p[0] && t.Exchange == p[1] && t.Symbol == p[2] {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		e.raise(Alert{Type: "Repeated same-day reversals (possible artificial volume)", UCC: p[0], Severity: "Medium", TxnFrom: days[len(days)-1], TxnTo: e.date,
			Detail: fmt.Sprintf("%s bought and sold the same quantity of %s on %s on %d of the last %d trading days (NSE/SURV/74008 item 34). Examine whether the trades add volume without a change of ownership; record the finding.", p[0], p[2], p[1], count[k], len(days))},
			"REV:"+e.date+":"+shortHash(k))
	}
}

// illiquid: a client's day value in a scrip of the firm's ILLIQUID group.
func (e *b6Env) illiquid(ts []ingest.TradeEvent) {
	gs, _ := e.o.ScripGroups(e.ctx, e.tenant)
	on := groupsOn(gs, e.date)
	val := map[string]money.Dec{}
	for _, t := range ts {
		ill := false
		for _, g := range on[t.Symbol] {
			ill = ill || g == illiquidGroup
		}
		if !ill {
			continue
		}
		k := t.UCC + "|" + t.Exchange + "|" + t.Symbol
		val[k] = val[k].Add(t.Value())
	}
	for _, k := range sortedKeys(val) {
		if val[k].Cmp(money.DecInt(e.cfg.IlliquidValue)) < 0 {
			continue
		}
		p := strings.Split(k, "|")
		e.raise(Alert{Type: "Large trades in an illiquid scrip", UCC: p[0], Severity: "Medium", TxnFrom: e.date, TxnTo: e.date, Value: val[k].Fixed(2),
			Detail: fmt.Sprintf("%s traded ₹%s of %s on %s, a scrip on the firm's illiquid list (NSE/SURV/74008 item 34). Examine the concentration and the client's profile; record the finding.", p[0], formatINR(val[k]), p[2], p[1])},
			"ILQ:"+e.date+":"+shortHash(k))
	}
}

// expiry: an option bought and sold on its expiry day with a large profit
// or loss.
func (e *b6Env) expiry(ts []ingest.TradeEvent) {
	type pos struct {
		bq, sq int64
		bv, sv money.Dec
	}
	by := map[string]*pos{}
	for _, t := range ts {
		if (t.InstrumentType != "CE" && t.InstrumentType != "PE") || t.Expiry != e.date {
			continue
		}
		k := t.UCC + "|" + t.Exchange + "|" + t.Instrument()
		p := by[k]
		if p == nil {
			p = &pos{}
			by[k] = p
		}
		if t.Side == "B" {
			p.bq += t.Qty
			p.bv = p.bv.Add(t.Value())
		} else {
			p.sq += t.Qty
			p.sv = p.sv.Add(t.Value())
		}
	}
	for _, k := range sortedKeys(by) {
		p := by[k]
		q := min(p.bq, p.sq)
		if q == 0 {
			continue
		}
		ab, _ := p.bv.Quo(money.DecInt(p.bq))
		as, _ := p.sv.Quo(money.DecInt(p.sq))
		pl := as.Sub(ab).MulInt(q)
		if pl.Abs().Cmp(money.DecInt(e.cfg.ExpiryPLRupees)) < 0 {
			continue
		}
		x := strings.SplitN(k, "|", 3)
		e.raise(Alert{Type: "Large expiry-day option round trip (near-expiry pattern)", UCC: x[0], Severity: "Medium", TxnFrom: e.date, TxnTo: e.date, Value: pl.Fixed(2),
			Detail: fmt.Sprintf("%s bought and sold %d of %s on %s on its expiry day, a %s of ₹%s (NSE/SURV/74008 item 34). Examine whether the prices moved money between accounts; record the finding.", x[0], q, strings.ReplaceAll(x[2], "|", " "), x[1], map[bool]string{true: "profit", false: "loss"}[pl.Sign() > 0], formatINR(pl.Abs()))},
			"EXP:"+e.date+":"+shortHash(k))
	}
}

// ---- BSE's client code modification penalty file ----

// CCMPenaltyRow is one modification BSE penalised.
type CCMPenaltyRow struct {
	TradeDate string    `json:"trade_date"`
	TradeNo   string    `json:"trade_no"`
	Scrip     string    `json:"scrip,omitempty"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Value     money.Dec `json:"value"`
	Penalty   money.Dec `json:"penalty"`
	InLedger  bool      `json:"in_register"` // the CCM is on Ledgerline's register
}

// CCMPenalty is one BSE penalty file as loaded.
type CCMPenalty struct {
	ID      string          `json:"id"` // month
	File    string          `json:"file"`
	Rows    []CCMPenaltyRow `json:"rows"`
	Total   money.Dec       `json:"total"`
	Missing int             `json:"missing"` // rows not on the register
	By      string          `json:"by"`
	At      string          `json:"at"`
}

func (o *Ops) loadBSECCMPenalty(ctx context.Context, tenant, actor, name string, rows []xfile.Row, r *XFileResult) error {
	ccms, _ := o.CCMs(ctx, tenant, "")
	reg := map[string]bool{}
	for _, c := range ccms {
		if c.Exchange == "BSE" {
			reg[strings.TrimLeft(c.TradeNo, "0")] = true
		}
	}
	p := CCMPenalty{File: name, By: actor, At: o.now().Format(time.RFC3339)}
	for _, row := range rows {
		x := CCMPenaltyRow{TradeNo: strings.TrimSpace(row.Get("trade_no")), Scrip: strings.TrimSpace(row.Get("scrip")),
			From: strings.ToUpper(strings.TrimSpace(row.Get("from"))), To: strings.ToUpper(strings.TrimSpace(row.Get("to"))),
			Value: decOr0(strings.ReplaceAll(row.Get("value"), ",", "")), Penalty: decOr0(strings.ReplaceAll(row.Get("penalty"), ",", ""))}
		if d, ok := parseListDate(row.Get("date")); ok {
			x.TradeDate = d
		}
		if x.TradeNo == "" && x.From == "" {
			continue
		}
		x.InLedger = reg[strings.TrimLeft(x.TradeNo, "0")]
		if x.TradeDate > p.ID {
			p.ID = x.TradeDate
		}
		p.Total = p.Total.Add(x.Penalty)
		if !x.InLedger {
			p.Missing++
			o.raiseAlert(ctx, tenant, actor, Alert{Source: "BSE", Type: "Client code modification penalised by BSE, not on the CCM register", UCC: x.From, Severity: "High", Date: x.TradeDate,
				Detail: fmt.Sprintf("BSE's CCM penalty file %s has trade %s of %s (%s to %s, value ₹%s, penalty ₹%s); Ledgerline's register has no such modification. Find who modified it and why, and record it.", name, x.TradeNo, fmtDay(x.TradeDate), x.From, x.To, x.Value.Fixed(2), x.Penalty.Fixed(2))},
				"CCMPEN:"+x.TradeDate+":"+x.TradeNo)
		}
		p.Rows = append(p.Rows, x)
	}
	if len(p.Rows) == 0 {
		return fmt.Errorf("no modification rows found")
	}
	if p.ID == "" {
		p.ID = o.now().In(ist).Format("2006-01-02")
	}
	p.ID = p.ID[:7]
	if err := docs.Upsert(ctx, o.Docs, tenant, cSurvB6, "ccmpen|"+p.ID, p, actor); err != nil {
		return err
	}
	o.raiseWork(ctx, tenant, "ccm", "bsepenalty:"+p.ID, fmt.Sprintf("BSE CCM penalty for %s: ₹%s", p.ID, p.Total.Fixed(2)),
		fmt.Sprintf("%d modification%s penalised by BSE (%d not on the CCM register). Check them against the register, book the penalty (it is the firm's, not the client's) and review why the codes were modified (SEBI stock broker master circular 38.3).", len(p.Rows), plural(len(p.Rows)), p.Missing),
		"", RoleCompliance, "surv", o.now().In(ist).Format("2006-01-02"))
	o.log(ctx, tenant, actor, "ccm.bse_penalty_loaded", p.ID, map[string]any{"rows": len(p.Rows), "total": p.Total.Fixed(2), "missing": p.Missing})
	r.Loaded, r.Added = fmt.Sprintf("BSE CCM penalties for %s: ₹%s on %d modifications, %d not on the CCM register (unconfirmed layout)", p.ID, p.Total.Fixed(2), len(p.Rows), p.Missing), len(p.Rows)
	return nil
}

// TradeSurvB6View is the round-6 surveillance data for the screen.
type TradeSurvB6View struct {
	Config     SurvB6Config        `json:"config"`
	Shares     int                 `json:"shares_outstanding"`
	SharesAsOf string              `json:"shares_as_of,omitempty"`
	Penalties  []CCMPenalty        `json:"ccm_penalties"`
	Sample     []SharesOutstanding `json:"shares_sample"`
}

// TradeSurvB6Panel is the round-6 surveillance data.
func (o *Ops) TradeSurvB6Panel(ctx context.Context, tenant, actor string) (TradeSurvB6View, error) {
	v := TradeSurvB6View{Penalties: []CCMPenalty{}, Sample: []SharesOutstanding{}}
	if err := o.require(ctx, tenant, actor, "Viewing surveillance alerts", RoleCompliance, RoleChecker, RoleRisk); err != nil {
		return v, err
	}
	v.Config = o.SurvB6Settings(ctx, tenant)
	ss, _ := docs.List[SharesOutstanding](ctx, o.Docs, tenant, cSharesOut, "")
	v.Shares = len(ss)
	for i, s := range ss {
		if s.AsOf > v.SharesAsOf {
			v.SharesAsOf = s.AsOf
		}
		if i < 20 {
			v.Sample = append(v.Sample, s)
		}
	}
	ps, _ := docs.List[CCMPenalty](ctx, o.Docs, tenant, cSurvB6, "ccmpen|")
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].ID > ps[j].ID })
	v.Penalties = append(v.Penalties, ps...)
	return v, nil
}
