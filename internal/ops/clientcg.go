package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- the clients' capital gains statement ----
//
// Realised gains lot by lot, first in, first out, from the exchange
// trades (equity shares, ETFs, bonds) and the mutual fund units bought and
// redeemed through the firm (mf.go). Each sale is worked out with the
// rules in cgtax.go: the holding period and section by the kind of
// security, the 31 January 2018 grandfathering of listed equity and
// equity fund units, and the rates for the sale's date. The long-term
// gains under section 112A are also laid out scrip by scrip as the
// return's Schedule 112A asks (cgSchedule112A).

// CGSale is a client's sale worked out for tax.
type CGSale struct {
	Sale
	Kind   string `json:"kind"`
	Source string `json:"source"` // Exchange, Mutual fund
	CG     CGLine `json:"cg"`
}

// ClientCG is a client's capital gains for a period.
type ClientCG struct {
	Sales      []CGSale
	Spec       money.Dec
	STCG111A   money.Dec
	LTCG112A   money.Dec
	STOther    money.Dec
	LTOther    money.Dec
	Unknown    money.Dec
	Exempt112A money.Amount
	Taxable    money.Dec // 112A gains above the exemption
	NoFMV      int
	LotsAsOf   string
}

// mfSales are a client's mutual fund redemptions and switches out matched
// first in, first out to the units bought, to and including to.
func (o *Ops) mfSales(ctx context.Context, tenant, ucc, to string) ([]Sale, map[string]string) {
	ts, _ := docs.List[MFUnitTxn](ctx, o.Docs, tenant, cMFUnit, strings.ToUpper(ucc)+"|")
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].Date < ts[j].Date })
	names := map[string]string{}
	type lot struct {
		date       string
		units, nav money.Dec
	}
	lots := map[string][]*lot{}
	var out []Sale
	for _, t := range ts {
		if t.Date > to || t.Units.Sign() == 0 {
			continue
		}
		names[t.ISIN] = t.Scheme
		nav := t.NAV
		if nav.Sign() == 0 && t.Amount != 0 && t.Units.Sign() != 0 {
			nav, _ = t.Amount.Dec().Quo(t.Units.Abs())
		}
		if t.Units.Sign() > 0 {
			lots[t.ISIN] = append(lots[t.ISIN], &lot{t.Date, t.Units, nav})
			continue
		}
		left := t.Units.Abs()
		for _, l := range lots[t.ISIN] {
			if left.Sign() == 0 {
				break
			}
			if l.units.Sign() == 0 {
				continue
			}
			q := money.Min(l.units, left)
			cost, proc := l.nav.Mul(q), nav.Mul(q)
			s := Sale{Date: t.Date, ISIN: t.ISIN, Symbol: t.Scheme, Qty: q, BuyDate: l.date, BuyPrice: l.nav, SellPrice: nav, Cost: cost, Proceeds: proc, Gain: proc.Sub(cost)}
			s.Category, s.Days = gainCategory(l.date, t.Date)
			out = append(out, s)
			l.units = l.units.Sub(q)
			left = left.Sub(q)
		}
		if left.Sign() > 0 {
			proc := nav.Mul(left)
			out = append(out, Sale{Date: t.Date, ISIN: t.ISIN, Symbol: t.Scheme, Qty: left, SellPrice: nav, Proceeds: proc, Category: CatUnknown})
		}
	}
	return out, names
}

// clientCG works out a client's capital gains for a period.
func (o *Ops) clientCG(ctx context.Context, tenant, ucc, from, to string) (ClientCG, error) {
	var res ClientCG
	_, sales, asOf, err := o.equityBook(ctx, tenant, ucc, to)
	if err != nil {
		return res, err
	}
	res.LotsAsOf = asOf
	mf, mfNames := o.mfSales(ctx, tenant, ucc, to)
	schemes := map[string]string{}
	if len(mf) > 0 {
		ss, _ := docs.List[MFScheme](ctx, o.Docs, tenant, cMFScheme, "")
		for _, s := range ss {
			schemes[s.ISIN] = s.Category + " " + s.Name
		}
	}
	fmv := o.fmv2018(ctx, tenant)
	add := func(s Sale, kind, src, name string) {
		if s.Date < from || s.Date > to {
			return
		}
		x := CGSale{Sale: s, Kind: kind, Source: src}
		switch s.Category {
		case CatSpec:
			res.Spec = res.Spec.Add(s.Gain)
		case CatUnknown:
			res.Unknown = res.Unknown.Add(s.Proceeds)
		default:
			x.CG = cgWork(cgClassOf(kind, name), s.BuyDate, s.Date, s.Qty, s.Cost, s.Proceeds, fmv[s.ISIN])
			if x.CG.Grand && !x.CG.HasFMV {
				res.NoFMV++
			}
			switch x.CG.Section {
			case "111A":
				res.STCG111A = res.STCG111A.Add(x.CG.Gain)
			case "112A":
				res.LTCG112A = res.LTCG112A.Add(x.CG.Gain)
			case "112":
				res.LTOther = res.LTOther.Add(x.CG.Gain)
			default:
				res.STOther = res.STOther.Add(x.CG.Gain)
			}
		}
		res.Sales = append(res.Sales, x)
	}
	for _, s := range sales {
		k := kindOfTrade("", s.ISIN, s.Symbol, "")
		add(s, k, "Exchange", s.Symbol)
	}
	for _, s := range mf {
		add(s, "MFO", "Mutual fund", schemes[s.ISIN]+" "+mfNames[s.ISIN])
	}
	sort.SliceStable(res.Sales, func(i, j int) bool { return res.Sales[i].Date < res.Sales[j].Date })
	res.Exempt112A = CG112AExemption(to)
	res.Taxable = res.LTCG112A.Sub(res.Exempt112A.Dec())
	if res.Taxable.Sign() < 0 {
		res.Taxable = money.Dec{}
	}
	return res, nil
}

// cgSchedule112A lays the section 112A sales out scrip by scrip as
// Schedule 112A of the return (ITR-2 and ITR-3) asks: per ISIN, whether
// bought on or before 31 January 2018, the units, the sale price per
// unit, the full value of the consideration, the cost of acquisition
// without indexation (the higher of the cost and, for units bought
// before 1 February 2018, the lower of the fair market value and the
// sale value), the fair market value on 31 January 2018, the expenditure
// on transfer and the balance. Rows are kept apart by whether the
// transfer was before 23 July 2024 (the return splits the year there).
func cgSchedule112A(sales []CGSale) Section {
	sec := Section{Title: "Schedule 112A: long-term gains on equity shares and equity fund units (STT paid), scrip-wise",
		Columns: []Column{{"acq", "1 Acquired", ColText}, {"isin", "2 ISIN", ColText}, {"name", "3 Name of the share or unit", ColText}, {"qty", "4 Number", ColQty}, {"price", "5 Sale price per unit", ColMoney},
			{"fvc", "6 Full value of consideration (4 × 5)", ColMoney}, {"coa", "7 Cost of acquisition without indexation (higher of 8 and 9)", ColMoney}, {"cost", "8 Cost of acquisition", ColMoney},
			{"low", "9 If bought before 1 Feb 2018: lower of 6 and 11", ColMoney}, {"fmvu", "10 FMV per unit on 31 Jan 2018", ColMoney}, {"fmv", "11 Total FMV (4 × 10)", ColMoney},
			{"exp", "12 Expenditure on transfer", ColMoney}, {"ded", "13 Total deductions (7 + 12)", ColMoney}, {"bal", "14 Balance (6 − 13)", ColMoney}, {"when", "Transferred", ColText}},
		Empty: "No long-term gains under section 112A in this period."}
	type key struct{ acq, isin, when string }
	type agg struct {
		name                             string
		qty, fvc, coa, cost, low, fmv, b money.Dec
		hasFMV                           bool
	}
	rows := map[key]*agg{}
	var order []key
	for _, s := range sales {
		if s.CG.Section != "112A" {
			continue
		}
		k := key{"After 31 Jan 2018", s.ISIN, "On or after 23 Jul 2024"}
		if s.BuyDate < cgGrandfatherBuy {
			k.acq = "On or before 31 Jan 2018"
		}
		if s.Date < cgNewRegime {
			k.when = "Before 23 Jul 2024"
		}
		a := rows[k]
		if a == nil {
			a = &agg{name: s.Symbol}
			rows[k] = a
			order = append(order, k)
		}
		a.qty, a.fvc, a.coa, a.cost = a.qty.Add(s.Qty), a.fvc.Add(s.Proceeds), a.coa.Add(s.CG.TaxCost), a.cost.Add(s.Cost)
		if s.CG.HasFMV {
			a.hasFMV = true
			a.fmv = a.fmv.Add(s.CG.FMV)
			a.low = a.low.Add(money.Min(s.CG.FMV, s.Proceeds))
		}
		a.b = a.b.Add(s.CG.Gain)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].when != order[j].when {
			return order[i].when > order[j].when
		}
		if rows[order[i]].name != rows[order[j]].name {
			return rows[order[i]].name < rows[order[j]].name
		}
		return order[i].acq > order[j].acq
	})
	var t6, t7, t8, t14 money.Dec
	for _, k := range order {
		a := rows[k]
		price, _ := a.fvc.Quo(a.qty)
		low, fmvu, fmv := "", "", ""
		if a.hasFMV {
			u, _ := a.fmv.Quo(a.qty)
			low, fmvu, fmv = d2s(a.low), d2s(u), d2s(a.fmv)
		}
		sec.Rows = append(sec.Rows, []string{k.acq, k.isin, a.name, fmtQty(a.qty), d2s(price), d2s(a.fvc), d2s(a.coa), d2s(a.cost), low, fmvu, fmv, "0.00", d2s(a.coa), d2s(a.b), k.when})
		t6, t7, t8, t14 = t6.Add(a.fvc), t7.Add(a.coa), t8.Add(a.cost), t14.Add(a.b)
	}
	if len(sec.Rows) > 0 {
		sec.Totals = []string{"Total", "", "", "", "", d2s(t6), d2s(t7), d2s(t8), "", "", "", "0.00", d2s(t7), d2s(t14), ""}
	}
	sec.Note = "Column 12 is left at nil: brokerage and charges are in the charges section; add the expenditure on transfer (not STT) when you file. The return asks for the rows transferred before 23 July 2024 apart from the later ones."
	return sec
}

// repPnL is the clients' profit and loss and capital gains statement.
func (o *Ops) repPnL(ctx context.Context, tenant string, r *ClientReport) error {
	cg, err := o.clientCG(ctx, tenant, r.UCC, r.From, r.To)
	if err != nil {
		return err
	}
	bt, err := o.bills(ctx, tenant, r.UCC, r.From, r.To)
	if err != nil {
		return err
	}
	cols := func(grand bool) []Column {
		c := []Column{{"sell", "Sell date", ColDate}, {"symbol", "Security", ColText}, {"isin", "ISIN", ColText}, {"qty", "Qty", ColQty}, {"buy", "Buy date", ColDate}, {"bp", "Buy price", ColMoney}, {"sp", "Sell price", ColMoney}, {"cost", "Cost", ColMoney}}
		if grand {
			c = append(c, Column{"fmv", "FMV 31 Jan 2018", ColMoney}, Column{"tcost", "Cost for tax", ColMoney})
		}
		return append(c, Column{"proc", "Sale value", ColMoney}, Column{"gain", "Gain / loss", ColMoney}, Column{"days", "Days held", ColQty})
	}
	sec := func(title string, grand bool, keep func(CGSale) bool) Section {
		s := Section{Title: title, Columns: cols(grand), Empty: "None in this period."}
		var tc, tt, tp, tg money.Dec
		for _, x := range cg.Sales {
			if !keep(x) {
				continue
			}
			days, cost, bp := "", d2s(x.Cost), d2s(x.BuyPrice)
			if x.BuyDate != "" {
				days = fmt.Sprint(daysBetween(x.BuyDate, x.Date))
			}
			if x.Category == CatUnknown {
				cost, bp = "", ""
			}
			gain := x.Gain
			tcost := x.Cost
			if x.Category != CatSpec && x.Category != CatUnknown {
				gain, tcost = x.CG.Gain, x.CG.TaxCost
			}
			row := []string{x.Date, x.Symbol, x.ISIN, fmtQty(x.Qty), x.BuyDate, bp, d2s(x.SellPrice), cost}
			if grand {
				f := ""
				if x.CG.HasFMV {
					f = d2s(x.CG.FMV)
				}
				row = append(row, f, d2s(tcost))
			}
			s.Rows = append(s.Rows, append(row, d2s(x.Proceeds), d2s(gain), days))
			tc, tt, tp, tg = tc.Add(x.Cost), tt.Add(tcost), tp.Add(x.Proceeds), tg.Add(gain)
		}
		if len(s.Rows) > 0 {
			t := []string{"Total", "", "", "", "", "", "", d2s(tc)}
			if grand {
				t = append(t, "", d2s(tt))
			}
			s.Totals = append(t, d2s(tp), d2s(tg), "")
		}
		return s
	}
	of := func(cat string) func(CGSale) bool { return func(x CGSale) bool { return x.Category == cat } }
	inSec := func(ss ...string) func(CGSale) bool {
		return func(x CGSale) bool {
			return x.Category != CatSpec && x.Category != CatUnknown && contains(ss, x.CG.Section)
		}
	}
	spec := sec("Speculative business income (intraday equity)", false, of(CatSpec))
	spec.Columns[4].Label, spec.Columns[5].Label, spec.Columns[6].Label = "Trade date", "Avg buy", "Avg sell"
	st := sec(fmt.Sprintf("Short-term capital gains: listed equity and equity fund units, STT paid (%s, %s)", cgSectionName("111A", r.To), cgRate("111A", r.To)), false, inSec("111A"))
	lt := sec(fmt.Sprintf("Long-term capital gains: listed equity and equity fund units, STT paid (%s, %s)", cgSectionName("112A", r.To), cgRate("112A", r.To)), true, inSec("112A"))
	lt.Note = "Shares and equity fund units bought before 1 February 2018 take as cost the higher of their cost and the lower of their fair market value on 31 January 2018 and the sale value (section 55(2)(ac))."
	r.Sections = append(r.Sections, spec, st, lt)
	so := sec("Other short-term capital gains (bonds, debt and other fund units; taxed at your normal rates)", false, inSec("slab", "50AA"))
	lo := sec(fmt.Sprintf("Other long-term capital gains (%s, %s)", cgSectionName("112", r.To), cgRate("112", r.To)), false, inSec("112"))
	if len(so.Rows) > 0 {
		r.Sections = append(r.Sections, so)
	}
	if len(lo.Rows) > 0 {
		r.Sections = append(r.Sections, lo)
	}
	r.Sections = append(r.Sections, cgSchedule112A(cg.Sales), bt.derivSection(), bt.chargeSection())
	if cg.Unknown.Sign() != 0 {
		u := sec("Sales without purchase history", false, of(CatUnknown))
		u.Note = "These sales have no recorded purchase. Load the client's purchase lots (HOLDING-LOTS) to compute their gains."
		r.Sections = append(r.Sections, u)
	}
	r.Summary = []SummaryItem{
		{"Speculative", d2s(cg.Spec), ColMoney, tone(cg.Spec)},
		{"Short-term capital gain", d2s(cg.STCG111A), ColMoney, tone(cg.STCG111A)},
		{"Long-term capital gain", d2s(cg.LTCG112A), ColMoney, tone(cg.LTCG112A)},
		{"Derivatives (non-speculative business)", d2s(bt.derivTotal()), ColMoney, tone(bt.derivTotal())},
		{"Charges", d2s(bt.chargeTotal()), ColMoney, ""},
		{"Long-term gain not taxed (" + cgSectionName("112A", r.To) + ")", d2s(money.Min(money.Max(cg.LTCG112A, money.Dec{}), cg.Exempt112A.Dec())), ColMoney, ""},
		{"Long-term gain taxed at " + strings.TrimSuffix(cgRate("112A", r.To), " above ₹1.25 lakh"), d2s(cg.Taxable), ColMoney, ""},
	}
	if cg.STOther.Sign() != 0 || cg.LTOther.Sign() != 0 {
		r.Summary = append(r.Summary, SummaryItem{"Other short-term gain", d2s(cg.STOther), ColMoney, tone(cg.STOther)}, SummaryItem{"Other long-term gain", d2s(cg.LTOther), ColMoney, tone(cg.LTOther)})
	}
	r.Notes = append(r.Notes,
		"Gains are computed first-in first-out on purchase price; brokerage and statutory charges are shown separately (STT is not deductible against capital gains). Same-day buy and sell of a scrip is treated as speculative.",
		fmt.Sprintf("Rates for sales on or after 23 July 2024: short-term gains on listed equity and equity fund units 20%% (%s); long-term gains on them 12.5%% on the year's gains above ₹1.25 lakh (%s); other long-term gains 12.5%% without indexation. Before 23 July 2024: 15%%, and 10%% above ₹1 lakh. Long-term means held more than 12 months for listed securities and equity fund units, 24 months for the rest; debt fund units bought from 1 April 2023 are always short-term.", cgSectionName("111A", r.To), cgSectionName("112A", r.To)),
		"This statement supports tax filing and is not tax advice; please confirm with your tax adviser.")
	if n := fmvNote(cg.NoFMV); n != "" {
		r.Notes = append(r.Notes, n)
	}
	if newTaxAct(r.To) {
		r.Notes = append(r.Notes, "From 1 April 2026 the Income-tax Act, 2025 applies: sections 111A, 112 and 112A of the 1961 Act are sections 196, 197 and 198.")
	}
	if cg.LotsAsOf != "" {
		r.Notes = append(r.Notes, "Purchase history is carried from the lots as at "+fmtDay(cg.LotsAsOf)+".")
	}
	return nil
}
