package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- Finspot: positions, dealer order types, margin baskets (finspot.go) ----

var finspotIndexSymbols = map[string]bool{"NIFTY": true, "BANKNIFTY": true, "FINNIFTY": true, "MIDCPNIFTY": true, "NIFTYNXT50": true, "SENSEX": true, "BANKEX": true}

// finspotContract is a position's contract as Finspot's F&O files give
// it: instrument type, symbol, expiry (YYYY-MM-DD), strike (0 for a
// future) and option type (XX for a future).
func finspotContract(p PositionView) (it, sym, exp, strike, opt string, ok bool) {
	f := strings.Split(p.Instrument, "|")
	if len(f) < 4 {
		return
	}
	sym, typ, exp, strike := f[0], f[1], f[2], f[3]
	fut := typ == "FUT"
	opt = typ
	if fut {
		opt, strike = "XX", "0"
	}
	switch p.Segment {
	case "CD":
		it = map[bool]string{true: "FUTCUR", false: "OPTCUR"}[fut]
	case "COM":
		it = map[bool]string{true: "FUTCOM", false: "OPTFUT"}[fut]
	default:
		idx := p.Index || finspotIndexSymbols[sym]
		it = map[[2]bool]string{{true, true}: "FUTIDX", {true, false}: "FUTSTK", {false, true}: "OPTIDX", {false, false}: "OPTSTK"}[[2]bool{fut, idx}]
	}
	return it, sym, exp, firstOf(strike, "0"), opt, true
}

// FinspotCostFile is NFO_COST_PRICE: each client's open NSE F&O positions
// at the previous business day's close, signed quantity and average price.
func (o *Ops) FinspotCostFile(ctx context.Context, tenant, date string) (string, []byte, error) {
	s, err := finspotSpec("finspot-nfo-cost-price")
	if err != nil {
		return "", nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	rv, err := o.Risk(ctx, tenant, set.PrevBusinessDay(date))
	if err != nil {
		return "", nil, err
	}
	var rows []xfile.Row
	for _, u := range sortedKeys(rv.Positions) {
		ps := append([]PositionView(nil), rv.Positions[u]...)
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Instrument < ps[j].Instrument })
		for _, p := range ps {
			it, sym, exp, strike, opt, ok := finspotContract(p)
			if p.Exchange != "NSE" || p.Segment != "FO" || p.Qty == 0 || !ok {
				continue
			}
			if t, err := time.Parse("2006-01-02", exp); err == nil {
				exp = t.Format("02-Jan-2006")
			}
			rows = append(rows, xfile.Row{Values: map[string]string{"CLIENT_ID": u, "INSTRUMENT_TYPE": it, "SCRIP_NAME": sym, "EXPIRY_DATE": exp,
				"STRIKE_PRICE": strike, "OPTION_TYPE": opt, "NETQTY": fmt.Sprint(p.Qty), "AVG_RATE": p.AvgPrice.Fixed(4)}})
		}
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(xfile.Vars{"date": date})
	return name, b, err
}

// FinspotDealerPositionFile is Dealer Client position: each client's open
// derivatives positions at the previous business day's close, under the
// firm's position dealer, product NRML; a long position as buy quantity
// and amount (quantity × average price), a short one as sell.
func (o *Ops) FinspotDealerPositionFile(ctx context.Context, tenant, date string) (string, []byte, error) {
	s, err := finspotSpec("finspot-dealer-position")
	if err != nil {
		return "", nil, err
	}
	cfg, err := o.finspotConfig(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	if cfg.PosDealer == "" {
		return "", nil, invalid("set the Finspot dealer IDs (or the position dealer) in Connections → Finspot uploads")
	}
	set, _ := o.Settings(ctx, tenant)
	rv, err := o.Risk(ctx, tenant, set.PrevBusinessDay(date))
	if err != nil {
		return "", nil, err
	}
	codes := map[string]string{}
	for _, fs := range finspotSegments {
		codes[fs.Key] = fs.Code
	}
	var rows []xfile.Row
	for _, u := range sortedKeys(rv.Positions) {
		ps := append([]PositionView(nil), rv.Positions[u]...)
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Exchange+ps[i].Instrument < ps[j].Exchange+ps[j].Instrument })
		for _, p := range ps {
			exch := codes[p.Exchange+"-"+p.Segment]
			it, sym, exp, strike, opt, ok := finspotContract(p)
			if p.Segment == "CM" || exch == "" || p.Qty == 0 || !ok {
				continue
			}
			if t, err := time.Parse("2006-01-02", exp); err == nil {
				exp = strings.ToUpper(t.Format("02-Jan-2006"))
			}
			v := map[string]string{"USER": cfg.PosDealer, "CLIENT": u, "EXCH": exch, "INSTRUMENT": it, "SYMBOL": sym, "EXP": exp, "STRIKE": strike, "OPTIONTYPE": opt,
				"BUYQTY": "0", "BUYAMT": "0", "SELLQTY": "0", "SELLAMT": "0", "PRODUCT": "NRML"}
			amt := p.AvgPrice.MulInt(abs(p.Qty)).Fixed(2)
			if p.Qty > 0 {
				v["BUYQTY"], v["BUYAMT"] = fmt.Sprint(p.Qty), amt
			} else {
				v["SELLQTY"], v["SELLAMT"] = fmt.Sprint(-p.Qty), amt
			}
			rows = append(rows, xfile.Row{Values: v})
		}
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(xfile.Vars{"date": date})
	return name, b, err
}

// FinspotOrderTypeFile is Dealer – Order Type Enable: the firm's order
// types for each of its Finspot dealer IDs.
func (o *Ops) FinspotOrderTypeFile(ctx context.Context, tenant, date string) (string, []byte, error) {
	s, err := finspotSpec("finspot-order-types")
	if err != nil {
		return "", nil, err
	}
	cfg, err := o.finspotConfig(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	if len(cfg.Dealers) == 0 {
		return "", nil, invalid("set the Finspot dealer IDs in Connections → Finspot uploads")
	}
	var rows []xfile.Row
	for _, d := range cfg.Dealers {
		rows = append(rows, xfile.Row{Values: map[string]string{"UserId": finspotText(d), "OrderType": finspotText(cfg.OrderTypes)}})
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(xfile.Vars{"date": date})
	return name, b, err
}

// FinspotMarginBasketFile is the NSE or BSE margin basket: the margin
// percentage per equity scrip (VaR + extreme loss margin from the previous
// business day's data, never below the firm's minimum, at most 100) for
// the firm's basket product and name. The BSE basket has the scrips with a
// BSE scrip code in the security master (from BSE's scrip and series file).
func (o *Ops) FinspotMarginBasketFile(ctx context.Context, tenant, exch, date string) (string, []byte, error) {
	s, err := finspotSpec("finspot-margin-basket-" + strings.ToLower(exch))
	if err != nil {
		return "", nil, err
	}
	cfg, err := o.finspotConfig(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	m, _, err := o.Market(ctx, tenant, set.PrevBusinessDay(date))
	if err != nil {
		return "", nil, err
	}
	code, basket := "NSE Margin Basket", cfg.NSEBasket
	if exch == "BSE" {
		code, basket = "BSE Margin Basket", cfg.BSEBasket
	}
	rows := []xfile.Row{{Record: code, Values: map[string]string{"Header": code, "Product": cfg.BasketProduct, "Basket": finspotText(basket)}}}
	seen := map[string]bool{}
	is := append([]Instrument(nil), m.Instruments...)
	sort.SliceStable(is, func(i, j int) bool { return is[i].Symbol < is[j].Symbol })
	for _, i := range is {
		if i.Segment != "CM" || i.Type != "EQ" || i.ISIN == "" || seen[i.ISIN] || exch == "NSE" && i.Exchange != "NSE" {
			continue
		}
		pct := money.Min(money.Max(i.VaRPct.Add(i.ELMPct), cfg.BasketMin), money.DecInt(100))
		ref, _, _ := docs.Get[SecRef](ctx, o.Docs, tenant, cSecRef, i.ISIN)
		if exch == "NSE" {
			seen[i.ISIN] = true
			rows = append(rows, xfile.Row{Values: map[string]string{"Symbol": finspotText(i.Symbol), "Series": finspotText(firstOf(ref.Series, "EQ")), "MarginPct": trimDec(pct.Fixed(2))}})
			continue
		}
		if ref.BSECode == "" {
			continue // the BSE basket is by scrip code
		}
		seen[i.ISIN] = true
		rows = append(rows, xfile.Row{Values: map[string]string{"ScripCode": finspotText(ref.BSECode), "MarginPct": trimDec(pct.Fixed(2))}})
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(xfile.Vars{"date": date})
	return name, b, err
}
