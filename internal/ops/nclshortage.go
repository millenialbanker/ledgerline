package ops

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- internal shortages auctioned by the clearing corporation ----
//
// An internal shortage is a client's short delivery hidden by another
// client's purchase of the same security in the same settlement: the
// firm's net obligation with the clearing corporation shows nothing short.
// NSE Clearing now auctions internal shortages too (consolidated circular
// NCL/CMPT/73996, items 6.4 and 7.13-7.14; FAQs NCL/CMPT/71441 of 24 Nov
// 2025):
//
//   - the member identifies its internal shortages in advance and keeps
//     the valuation amount (settlement price plus 20%) in its settlement
//     account by 12:00 noon on the settlement day; without it NSE
//     Clearing holds no auction;
//   - NSE Clearing reports them (CM_SA_S, the internal shortage and the
//     valuation amount; CM_SA_DTLS, client-wise) and auctions them; the
//     shares bought in the auction reach the member and go to the buyer,
//     who is not cashed out;
//   - where the auction fails or is not held, the shortage is closed out
//     at the auction rate, else the close-out rate, and the buyer is
//     credited;
//   - the short-delivering client gains nothing: where the settlement
//     price exceeds the auction rate the difference is collected from the
//     member (FAQ "auction difference"), so the client is debited at least
//     the settlement price, never more than the clearing corporation
//     levies (SEBI master circular for stock brokers, para 51.3).
//
// Ledgerline estimates the morning's funding from the day's obligations,
// netted as the clearing corporation nets them (settlement.go), and raises
// a work item before noon; NSE Clearing's own figure replaces the estimate
// once CM_SA_S is loaded. No column layout of CM_SA_S or CM_SA_DTLS is
// published: both are read by their header names (config/formats).

const (
	cIntShortRun = "intshortrun" // payIn date: when the morning check last ran

	// intShortLoad is the valuation load NSE Clearing puts on the
	// settlement price for an internal shortage (item 7.13(iii)).
	intShortLoadPct = 120
	// intShortDue is the time the valuation amount must be in the
	// settlement account on the settlement day.
	intShortDue = "12:00"
)

// InternalShortRow is one security's internal shortage in a netting group.
type InternalShortRow struct {
	Clearing  string    `json:"clearing"`
	ISIN      string    `json:"isin"`
	Symbol    string    `json:"symbol"`
	Sellers   []string  `json:"sellers"` // clients short, with the quantity
	Buyers    []string  `json:"buyers"`  // clients whose purchase it hides
	Short     int64     `json:"short"`   // client sales not covered by holdings
	Bought    int64     `json:"bought"`  // other clients' net purchases
	Internal  int64     `json:"internal"`
	Price     money.Dec `json:"price"`     // settlement (closing) price of the trade date
	Valuation money.Dec `json:"valuation"` // internal × price × 120%
	// NSE Clearing's figure from CM_SA_S, when loaded.
	NCLQty    int64     `json:"ncl_qty,omitempty"`
	NCLAmount money.Dec `json:"ncl_amount"`
}

// InternalShortageView is the morning's internal shortage funding.
type InternalShortageView struct {
	TradeDate string             `json:"trade_date"`
	PayIn     string             `json:"pay_in"`
	Due       string             `json:"due"` // YYYY-MM-DD HH:MM
	Rows      []InternalShortRow `json:"rows"`
	Estimate  money.Dec          `json:"estimate"`
	NCL       money.Dec          `json:"ncl"`
	NCLLoaded bool               `json:"ncl_loaded"`
	NCLFile   string             `json:"ncl_file,omitempty"`
	Detail    int                `json:"detail_rows"` // CM_SA_DTLS rows loaded
	Fund      money.Dec          `json:"fund"`        // what to keep: NSE Clearing's figure, else the estimate
	Notes     []string           `json:"notes"`
}

// InternalShortages works out a trade date's internal shortages and the
// funds to keep for their auction by noon on the settlement day.
func (o *Ops) InternalShortages(ctx context.Context, tenant, date string) (InternalShortageView, error) {
	if !validDate(date) {
		return InternalShortageView{}, invalid("give the trade date (YYYY-MM-DD)")
	}
	st, err := o.Obligations(ctx, tenant, date)
	if err != nil {
		return InternalShortageView{}, err
	}
	_, look, _ := o.Market(ctx, tenant, date)
	v := InternalShortageView{TradeDate: date, PayIn: st.PayIn, Due: st.PayIn + " " + intShortDue, Rows: []InternalShortRow{}, Notes: []string{}}
	reported := map[string]int64{}
	ss, _ := o.Shortages(ctx, tenant)
	for _, s := range ss {
		if s.Date == date {
			reported[s.UCC+"|"+s.ISIN] += s.Qty
		}
	}
	type acc struct {
		row  *InternalShortRow
		sell map[string]int64
		buy  map[string]int64
	}
	g := map[string]*acc{}
	at := func(ob Obligation) *acc {
		k := ob.Clearing + "|" + ob.ISIN
		if g[k] == nil {
			g[k] = &acc{row: &InternalShortRow{Clearing: ob.Clearing, ISIN: ob.ISIN, Symbol: ob.Symbol}, sell: map[string]int64{}, buy: map[string]int64{}}
		}
		return g[k]
	}
	for _, ob := range st.Obligations {
		if _, cc := ClearingCorpNames[ob.Clearing]; !cc {
			continue // cleared by another member, or on its own exchange
		}
		switch {
		case ob.NetQty < 0:
			short := int64(0)
			if q, ok := reported[ob.UCC+"|"+ob.ISIN]; ok {
				short = q
			} else if ob.Status == "Short delivery" {
				short = max(-ob.NetQty-ob.FromCUSPA-ob.Holding, 0)
			}
			if short > 0 {
				at(ob).sell[ob.UCC] += short
			}
		case ob.NetQty > 0:
			if !strings.HasPrefix(ob.Status, "Paid out") {
				at(ob).buy[ob.UCC] += ob.NetQty
			}
		}
	}
	for _, k := range sortedKeys(g) {
		a := g[k]
		r := a.row
		for _, u := range sortedKeys(a.sell) {
			r.Short += a.sell[u]
			r.Sellers = append(r.Sellers, fmt.Sprintf("%s ×%d", u, a.sell[u]))
		}
		for _, u := range sortedKeys(a.buy) {
			r.Bought += a.buy[u]
			r.Buyers = append(r.Buyers, fmt.Sprintf("%s ×%d", u, a.buy[u]))
		}
		r.Internal = min(r.Short, r.Bought)
		if r.Internal <= 0 {
			continue
		}
		if i, ok := look.ByISIN(r.ISIN); ok {
			r.Price = i.Close
			if r.Price.IsZero() {
				r.Price = i.LTP
			}
		}
		r.Valuation = r.Price.MulInt(r.Internal).Pct(money.DecInt(intShortLoadPct)).Round(money.UnitPaisa).Dec()
		v.Estimate = v.Estimate.Add(r.Valuation)
		if r.Price.IsZero() {
			v.Notes = append(v.Notes, fmt.Sprintf("%s: no closing price for %s, so its valuation is not in the estimate.", firstOf(r.Symbol, r.ISIN), fmtDay(date)))
		}
		v.Rows = append(v.Rows, *r)
	}
	// NSE Clearing's figures, when its reports are loaded for the pay-in
	// day (or the trade date, if named so).
	for _, day := range []string{st.PayIn, date} {
		for _, d := range o.ccData(ctx, tenant, "intshort", day, "") {
			v.NCLLoaded, v.NCLFile = true, d.File
			for _, x := range d.Rows {
				amt := decOr0(x["amount"])
				v.NCL = v.NCL.Add(amt)
				isin := strings.ToUpper(x["isin"])
				q, _ := strconv.ParseInt(decOr0(x["qty"]).Fixed(0), 10, 64)
				found := false
				for i := range v.Rows {
					if isin != "" && v.Rows[i].ISIN == isin {
						v.Rows[i].NCLQty += q
						v.Rows[i].NCLAmount = v.Rows[i].NCLAmount.Add(amt)
						found = true
					}
				}
				if !found && (isin != "" || q > 0) {
					v.Rows = append(v.Rows, InternalShortRow{Clearing: "NCL", ISIN: isin, Symbol: x["symbol"], NCLQty: q, NCLAmount: amt, Sellers: []string{}, Buyers: []string{}})
					v.Notes = append(v.Notes, fmt.Sprintf("NSE Clearing reports an internal shortage in %s that Ledgerline did not find: check the clients' deliveries.", firstOf(x["symbol"], isin)))
				}
			}
		}
		for _, d := range o.ccData(ctx, tenant, "intshortdtl", day, "") {
			v.Detail += len(d.Rows)
		}
		if v.NCLLoaded {
			break
		}
	}
	v.Fund = v.Estimate
	if v.NCLLoaded {
		v.Fund = v.NCL
		if v.NCL.Sub(v.Estimate).Abs().Cmp(money.MustDec("1")) > 0 {
			v.Notes = append(v.Notes, fmt.Sprintf("NSE Clearing's valuation amount ₹%s differs from Ledgerline's estimate ₹%s: keep NSE Clearing's figure.", formatINR(v.NCL), formatINR(v.Estimate)))
		}
	} else if len(v.Rows) > 0 {
		v.Notes = append(v.Notes, "Ledgerline's estimate: load NSE Clearing's CM_SA_S report when it is out to confirm the amount.")
	}
	return v, nil
}

// internalShortageTick raises, on the settlement day's morning (08:00 to
// noon, every 30 minutes until it is funded), the work item to keep the
// valuation amount of internal shortages in the settlement account by
// 12:00 noon. It refreshes the item as figures change (NSE Clearing's
// CM_SA_S loaded, shortages reported).
func (o *Ops) internalShortageTick(ctx context.Context, tenant string, now time.Time) {
	now = now.In(ist)
	hm, today := now.Format("15:04"), now.Format("2006-01-02")
	if hm < "08:00" || hm >= intShortDue {
		return
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil || !set.IsBusinessDay(today) {
		return
	}
	last, _, _ := docs.Get[struct{ At string }](ctx, o.Docs, tenant, cIntShortRun, today)
	if t, err := time.Parse(time.RFC3339, last.At); err == nil && now.Sub(t) < 30*time.Minute {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cIntShortRun, today, struct{ At string }{now.Format(time.RFC3339)}, SystemActor)
	td := set.PrevBusinessDay(today)
	if set.NextSettlementDay(td) != today {
		return
	}
	o.raiseInternalShortage(ctx, tenant, td)
}

// raiseInternalShortage opens or refreshes the funding work item for a
// trade date's internal shortages.
func (o *Ops) raiseInternalShortage(ctx context.Context, tenant, date string) {
	v, err := o.InternalShortages(ctx, tenant, date)
	if err != nil || len(v.Rows) == 0 || v.Fund.Sign() <= 0 {
		return
	}
	// ICCL's internal shortages have their own item (icclsettle.go); the
	// NSE Clearing item covers the rest.
	if icclEst := o.raiseICCLInternalShortage(ctx, tenant, v); icclEst.Sign() > 0 {
		var rest []InternalShortRow
		for _, r := range v.Rows {
			if r.Clearing != "ICCL" {
				rest = append(rest, r)
			}
		}
		if !v.NCLLoaded {
			v.Fund = v.Fund.Sub(icclEst)
		}
		if v.Rows = rest; len(rest) == 0 || v.Fund.Sign() <= 0 {
			return
		}
	}
	var what []string
	for _, r := range v.Rows {
		q := r.Internal
		if r.NCLQty > 0 {
			q = r.NCLQty
		}
		what = append(what, fmt.Sprintf("%s ×%d", firstOf(r.Symbol, r.ISIN), q))
	}
	src := "Ledgerline's estimate (settlement price plus 20%)"
	if v.NCLLoaded {
		src = "NSE Clearing's CM_SA_S report " + v.NCLFile
	}
	o.raiseWork(ctx, tenant, "internal_shortage", "fund|"+v.PayIn, fmt.Sprintf("Keep ₹%s in the settlement account by 12:00 for internal shortages", formatINR(v.Fund)),
		fmt.Sprintf("Internal shortages of trades of %s (%s), %s. NSE Clearing debits the valuation amount at 12:00 noon on %s and auctions the shares for the buyers; without the funds it holds no auction. Fund the settlement account, then let the buyers wait for the auctioned shares on Settlement → Short deliveries.",
			fmtDay(date), strings.Join(what, ", "), src, fmtDay(v.PayIn)), "", RoleChecker, "settlement", v.PayIn)
}

// loadInternalShortage keeps NSE Clearing's CM_SA_S or CM_SA_DTLS report
// for its date and refreshes the morning's funding item.
func (o *Ops) loadInternalShortage(ctx context.Context, tenant, actor string, s *xfile.Spec, f xfile.File, rows []xfile.Row, r *XFileResult) {
	kind := map[string]string{"ncl-internal-shortage": "intshort", "ncl-internal-shortage-detail": "intshortdtl"}[s.ID]
	date := ymdFromName(f.Name)
	if date == "" {
		date = o.now().In(ist).Format("2006-01-02")
	}
	n, err := o.putCCData(ctx, tenant, actor, kind, date, "CM", f.Name, rows, nil)
	if err != nil {
		r.Loaded = "Not loaded: " + err.Error()
		return
	}
	r.Loaded, r.Added = "Loaded as NSE Clearing's internal shortages for the auction of "+fmtDay(date)+" (Settlement → Short deliveries)", n
	set, _ := o.Settings(ctx, tenant)
	// The report is for the settlement (pay-in) day: its trade date is the
	// business day before.
	if td := set.PrevBusinessDay(date); set.NextSettlementDay(td) == date {
		o.raiseInternalShortage(ctx, tenant, td)
	}
}

// ---- closing out a shortage: auction or close-out ----

// ShortageClose is how a short delivery was finished by the clearing
// corporation: the shares bought in its auction (Received, at
// AuctionPrice) and the rest closed out at Price (the close-out rate, or
// the firm's internal close-out price where the clearing corporation held
// no auction).
type ShortageClose struct {
	Date         string    `json:"date"`
	Price        money.Dec `json:"price"`
	Received     int64     `json:"received"`
	AuctionPrice money.Dec `json:"auction_price"`
}

// SettleShortage closes out a shortage at price (nothing received in an
// auction).
func (o *Ops) SettleShortage(ctx context.Context, tenant, actor, id, date string, price money.Dec) (Shortage, error) {
	return o.SettleShortageWith(ctx, tenant, actor, id, ShortageClose{Date: date, Price: price})
}

// SettleShortageWith finishes a shortage. The firm's buyers whose purchase
// the shortage hit receive the shares bought in the auction first (they
// are not cashed out for those); what was not received is closed out and
// credited to them at the close-out price. The short-delivering client is
// debited per share the higher of the rate and the settlement price
// (never a gain from the shortage, never more than the clearing
// corporation levies); the clearing corporation is credited the rest.
func (o *Ops) SettleShortageWith(ctx context.Context, tenant, actor, id string, x ShortageClose) (Shortage, error) {
	if err := o.require(ctx, tenant, actor, "Closing out a short delivery", RoleChecker); err != nil {
		return Shortage{}, err
	}
	sh, _, err := docs.Get[Shortage](ctx, o.Docs, tenant, cShortage, id)
	if err != nil {
		return sh, err
	}
	if sh.Status != "Open" {
		return sh, fmt.Errorf("%w: %s is %s", ErrState, id, sh.Status)
	}
	date := x.Date
	if !ledger.ValidDate(date) {
		return sh, invalid("give the auction or close-out price and the date")
	}
	if x.Received < 0 || x.Received > sh.Qty {
		return sh, invalid("the shares received in the auction are 0 to %d", sh.Qty)
	}
	if x.Received > 0 && x.AuctionPrice.Sign() <= 0 {
		return sh, invalid("give the auction price of the %d shares received", x.Received)
	}
	closeQty := sh.Qty - x.Received
	if closeQty > 0 && x.Price.Sign() <= 0 {
		return sh, invalid("give the auction or close-out price and the date")
	}
	floor := sh.SettlePrice
	if floor.IsZero() {
		floor = sh.SellPrice
	}
	rate := func(p money.Dec) money.Dec { return money.Max(p, floor) }
	cost := x.AuctionPrice.MulInt(x.Received).Add(x.Price.MulInt(closeQty)).Round(money.UnitPaisa)
	debit := rate(x.AuctionPrice).MulInt(x.Received).Add(rate(x.Price).MulInt(closeQty)).Round(money.UnitPaisa)
	memo := fmt.Sprintf("Short delivery %s × %d", sh.Symbol, sh.Qty)
	switch {
	case x.Received == 0:
		memo += fmt.Sprintf(" closed out at ₹%s", rate(x.Price))
	case closeQty == 0:
		memo += fmt.Sprintf(" bought in auction at ₹%s", rate(x.AuctionPrice))
	default:
		memo += fmt.Sprintf(": %d in auction at ₹%s, %d closed out at ₹%s", x.Received, rate(x.AuctionPrice), closeQty, rate(x.Price))
	}
	ps := []ledger.Posting{{Account: ledger.ClientAccount(sh.UCC), Debit: debit, Memo: memo}}
	// The auctioned shares go to the firm's buyers first.
	recLeft := min(x.Received, sh.Internal)
	delivered := map[int]int64{}
	var credited money.Amount
	for i, b := range sh.Buyers {
		got := min(recLeft, b.Short)
		recLeft -= got
		delivered[i] = got
		if out := b.Short - got; out > 0 {
			amt := x.Price.MulInt(out).Round(money.UnitPaisa)
			sh.Buyers[i].Credit = amt
			credited += amt
			ps = append(ps, ledger.Posting{Account: ledger.ClientAccount(b.UCC), Credit: amt, Memo: fmt.Sprintf("Close-out of %d %s not received", out, sh.Symbol)})
		}
		sh.Buyers[i].Received = got
	}
	if rest := debit - credited; rest > 0 {
		m := "Auction / close-out by the clearing corporation"
		if diff := debit - cost; diff > 0 {
			m += fmt.Sprintf(" (incl. ₹%s auction difference: no gain to the short seller)", diff.Dec().Fixed(2))
		}
		ps = append(ps, ledger.Posting{Account: ledger.ClearingAccount(sh.Exchange, "CM"), Credit: rest, Memo: m})
	}
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "SHORT:" + sh.ID, ValueDate: date, CreatedBy: actor,
		Narration: fmt.Sprintf("Short delivery %s %s: %d internal, %d exchange; %d received in auction, %d closed out", sh.ID, sh.Symbol, sh.Internal, sh.Auction, x.Received, closeQty),
		Source:    ledger.SourceRef{Kind: "SHORTAGE", ID: sh.ID}, Postings: ps}); err != nil {
		return sh, err
	}
	for i, b := range sh.Buyers {
		deliver := b.Bought - b.Short + delivered[i]
		status := "Closed out"
		if deliver > 0 {
			if _, err := o.adjustHolding(ctx, tenant, actor, b.UCC, sh.ISIN, sh.Symbol, deliver, 0); err != nil {
				return sh, err
			}
			status = fmt.Sprintf("Paid out %d", deliver)
			if out := b.Short - delivered[i]; out > 0 {
				status += fmt.Sprintf("; %d closed out", out)
			}
			if delivered[i] > 0 {
				status += fmt.Sprintf(" (%d from the auction)", delivered[i])
			}
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cSettle, sh.Date+"|"+b.Key, settleState{Status: status, By: actor, At: o.now().Format("15:04")}, actor)
	}
	sh.Status, sh.Debit, sh.SettledBy = "Settled", debit, actor
	sh.Price, sh.Received, sh.AuctionPrice = x.Price, x.Received, x.AuctionPrice
	if closeQty == 0 {
		sh.Price = x.AuctionPrice
	}
	sh.Loss = debit.Dec().Sub(sh.SellPrice.MulInt(sh.Qty))
	sh.Model = "auction"
	if err := docs.Upsert(ctx, o.Docs, tenant, cShortage, sh.ID, sh, actor); err != nil {
		return sh, err
	}
	o.log(ctx, tenant, actor, "shortage.settled", sh.ID, map[string]any{"price": x.Price.String(), "received": x.Received, "auction_price": x.AuctionPrice.String(), "debit": debit.String()})
	return sh, nil
}

// shortageQtys are the quantities of a settled shortage for NSE Clearing's
// client-wise shortage report (Part C (10)): B cleared in auction, C
// cleared by close-out, D settled internally.
func shortageQtys(x Shortage) (b, c, d int64) {
	if x.Model == "auction" {
		return x.Received, x.Qty - x.Received, 0
	}
	return x.Auction, x.Internal, x.Qty - x.Auction - x.Internal
}
