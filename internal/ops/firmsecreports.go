package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- holdings: account → scrip → lot ----

// FSScripHold is a scrip held in a portfolio, with its lots.
type FSScripHold struct {
	ISIN    string       `json:"isin"`
	Symbol  string       `json:"symbol"`
	Qty     money.Dec    `json:"qty"`
	Cost    money.Amount `json:"cost"`
	Price   money.Dec    `json:"price"`
	Market  money.Amount `json:"market"`
	Pledged money.Dec    `json:"pledged"`
	Lots    []FSLotView  `json:"lots"`
}

// FSLotView is an open lot for the screen.
type FSLotView struct {
	FSLot
	TermNow string `json:"term_now,omitempty"` // investments: long-term or current on the date
	Days    int    `json:"days"`
}

// FSAcctHold is one securities account (a portfolio and kind) with its scrips.
type FSAcctHold struct {
	Account  string        `json:"account"`
	Name     string        `json:"name"`
	Kind     string        `json:"kind"`
	KindName string        `json:"kind_name"`
	Cost     money.Amount  `json:"cost"`
	Market   money.Amount  `json:"market"`
	Book     money.Amount  `json:"book"` // the account's balance in the books
	Scrips   []FSScripHold `json:"scrips"`
}

// FSPortHold is a portfolio's holdings.
type FSPortHold struct {
	Portfolio string       `json:"portfolio"`
	Name      string       `json:"name"`
	Cost      money.Amount `json:"cost"`
	Market    money.Amount `json:"market"`
	Accounts  []FSAcctHold `json:"accounts"`
	Demat     string       `json:"demat,omitempty"`
}

// FSHoldingsView is the Holdings tab.
type FSHoldingsView struct {
	Date      string        `json:"date"`
	Settings  FSSettings    `json:"settings"`
	Ports     []FSPortHold  `json:"portfolios"`
	Kinds     []FSKind      `json:"kinds"`
	Warnings  []string      `json:"warnings"`
	Events    []FSEvent     `json:"events"`
	Loaded    int           `json:"loaded"` // opening lots loaded, not yet posted
	Income    []FSIncome    `json:"income"`
	Codes     []string      `json:"codes"`
	CanEnter  bool          `json:"can_enter"`
	CanCheck  bool          `json:"can_check"`
	CanAdmin  bool          `json:"can_admin"`
	Pending   int           `json:"pending"`
	Journals  int           `json:"journals_pending"`
	TaxRates  []SummaryItem `json:"tax_rates"`
	Practices string        `json:"practices"`
}

// FirmSecHoldings lists the holdings on a date: portfolio, then account
// (one per portfolio and kind, c6), then scrip, then lot.
func (o *Ops) FirmSecHoldings(ctx context.Context, tenant, actor, date string) (FSHoldingsView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing firm securities", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return FSHoldingsView{}, err
	}
	if !ledger.ValidDate(date) {
		return FSHoldingsView{}, invalid("date must be YYYY-MM-DD")
	}
	s, ts, b := o.fsBookTo(ctx, tenant, date)
	v := FSHoldingsView{Date: date, Settings: s, Ports: []FSPortHold{}, Kinds: s.kinds(), Warnings: s.fsWarnings(), Events: []FSEvent{}, Income: []FSIncome{}, Codes: s.ownCodes(),
		Practices: "Settings → Firm practices → Firm securities"}
	v.CanEnter = o.require(ctx, tenant, actor, "Entering firm securities", RoleMaker, RoleChecker) == nil
	v.CanCheck = o.require(ctx, tenant, actor, "Approving firm securities entries", RoleChecker) == nil
	v.CanAdmin = o.require(ctx, tenant, actor, "Changing firm securities settings", RoleAdmin) == nil
	for _, t := range ts {
		if t.Status == FSLoaded {
			v.Loaded++
		}
		if t.Status == FSPending {
			v.Pending++
		}
	}
	if js, err := o.FirmSecJournals(ctx, tenant, actor); err == nil {
		for _, j := range js {
			if j.Status == FSPending {
				v.Journals++
			}
		}
	}
	_, look, _ := o.Market(ctx, tenant, date)
	bal, _ := o.App.Ledger.Balances(ctx, tenant, date)
	pledged := o.fsPledgedOn(ctx, tenant, date)
	demats := s.fsDemats()
	for _, p := range s.Ports() {
		ph := FSPortHold{Portfolio: p, Name: PortfolioName(p), Accounts: []FSAcctHold{}}
		for d, ps := range demats {
			if contains(ps, p) {
				ph.Demat = d
			}
		}
		accts := map[string]*FSAcctHold{}
		scrips := map[string]*FSScripHold{}
		var order []string
		for _, l := range b.lotsOf(p) {
			k := s.kind(l.Kind)
			code := fsInvAcct(k.ID)
			if p == PortSIT {
				code = fsSITAcct(k.ID)
			}
			a := accts[code]
			if a == nil {
				name, _ := s.fsAccountName(code)
				a = &FSAcctHold{Account: code, Name: name, Kind: k.ID, KindName: k.Name, Book: bal[code]}
				accts[code] = a
				order = append(order, code)
			}
			sk := code + "|" + l.ISIN
			sh := scrips[sk]
			if sh == nil {
				sh = &FSScripHold{ISIN: l.ISIN, Symbol: l.Symbol, Pledged: pledged[fsKey(p, l.ISIN)]}
				if pr, ok := o.fsPrice(look, l.ISIN); ok {
					sh.Price = pr
				}
				scrips[sk] = sh
			}
			lv := FSLotView{FSLot: l, Days: daysBetween(l.Date, date)}
			if p == PortInv {
				lv.TermNow = fsTermOn(s, l, date)
			}
			sh.Lots = append(sh.Lots, lv)
			sh.Qty = sh.Qty.Add(l.Qty)
			sh.Cost += l.Cost
		}
		for _, code := range order {
			a := accts[code]
			var keys []string
			for k := range scrips {
				if strings.HasPrefix(k, code+"|") {
					keys = append(keys, k)
				}
			}
			sort.Slice(keys, func(i, j int) bool { return scrips[keys[i]].Symbol < scrips[keys[j]].Symbol })
			for _, k := range keys {
				sh := scrips[k]
				sh.Market = sh.Cost
				if sh.Price.Sign() > 0 {
					sh.Market = sh.Price.Mul(sh.Qty).Round(money.UnitPaisa)
				}
				a.Cost += sh.Cost
				a.Market += sh.Market
				a.Scrips = append(a.Scrips, *sh)
			}
			ph.Cost += a.Cost
			ph.Market += a.Market
			ph.Accounts = append(ph.Accounts, *a)
		}
		v.Ports = append(v.Ports, ph)
	}
	for _, e := range b.Events {
		if e.Date > addDays(date, -90) {
			v.Events = append(v.Events, e)
		}
	}
	for _, x := range o.fsIncomes(ctx, tenant) {
		if len(v.Income) < 50 {
			v.Income = append(v.Income, x)
		}
	}
	v.TaxRates = []SummaryItem{{"Short-term gains, listed equity (" + cgSectionName("111A", date) + ")", cgRate("111A", date), ColText, ""},
		{"Long-term gains, listed equity (" + cgSectionName("112A", date) + ")", cgRate("112A", date), ColText, ""},
		{"Other long-term gains (" + cgSectionName("112", date) + ")", cgRate("112", date), ColText, ""}}
	return v, nil
}

// ---- the reports (questionnaire t6, i7, c2) ----

// FSReportDefs are the firm securities reports.
var FSReportDefs = []ClientReportDef{
	{"register", "Daily purchase and sale register", "Every purchase and sale of the firm's own securities, day by day, with the charges head by head.", true, false, "month"},
	{"summary", "Scrip-wise stock summary", "Opening, bought, sold and closing, quantity and amount apart, with the profit or loss on what was sold.", true, false, "fy"},
	{"pnl", "Speculative and non-speculative profit and loss", "Intraday (speculative) and delivery (non-speculative) results scrip by scrip, with the charges split between them.", true, false, "fy"},
	{"turnover", "Turnover for the tax audit", "Speculative turnover (the sum of the profits and losses, each taken as positive) and delivery turnover (sale value), as the tax audit report asks.", true, false, "fy"},
	{"closing", "Closing stock statement", "The closing stock scrip by scrip at cost, at the lower of cost and market value scrip by scrip, and category by category (ICDS VIII), with the value the books take.", false, false, ""},
	{"cg", "Capital gains register (investments)", "Sales of investments lot by lot, short- or long-term by section, with grandfathering; and the capital gain of investments converted into stock-in-trade, when that stock is sold.", true, false, "fy"},
	{"pledges", "Pledges", "The firm's own securities pledged on a date: to whom, how many, at cost and at market value.", false, false, ""},
}

// FirmSecReport builds a report. port is the portfolio (blank: the
// report's own, stock-in-trade where the firm keeps it); heads shows each
// cost head as its own column (i2).
func (o *Ops) FirmSecReport(ctx context.Context, tenant, actor, kind, port, from, to string, heads bool) (ClientReport, error) {
	if err := o.require(ctx, tenant, actor, "Viewing firm securities", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return ClientReport{}, err
	}
	var def *ClientReportDef
	for i := range FSReportDefs {
		if FSReportDefs[i].Kind == kind {
			def = &FSReportDefs[i]
		}
	}
	if def == nil {
		return ClientReport{}, fmt.Errorf("%w: firm securities report %q", ErrNotFound, kind)
	}
	if !ledger.ValidDate(to) || def.Range && (!ledger.ValidDate(from) || from > to) {
		return ClientReport{}, invalid("give the period (YYYY-MM-DD), from before to")
	}
	s := o.FirmSecSettings(ctx, tenant)
	if port == "" {
		port = PortSIT
		if !s.has(PortSIT) || kind == "cg" {
			port = PortInv
		}
	}
	if kind != "pledges" && kind != "register" && !s.has(port) {
		return ClientReport{}, invalid("the firm does not keep a %s portfolio", PortfolioName(port))
	}
	set, _ := o.Settings(ctx, tenant)
	r := ClientReport{Kind: "firmsec-" + kind, Title: def.Name, UCC: "FIRM", Name: set.Firm, From: from, To: to, Generated: o.now().In(ist).Format("02-Jan-2006 15:04 IST")}
	if def.Range {
		r.Period = fmtDay(from) + " to " + fmtDay(to)
	} else {
		r.From, r.Period = "", "As on "+fmtDay(to)
	}
	if kind != "pledges" && kind != "register" {
		r.Title += " · " + PortfolioName(port)
	}
	var err error
	switch kind {
	case "register":
		err = o.fsRepRegister(ctx, tenant, s, &r, port, heads)
	case "summary":
		err = o.fsRepSummary(ctx, tenant, s, &r, port, heads)
	case "pnl":
		err = o.fsRepPnL(ctx, tenant, s, &r, port)
	case "turnover":
		err = o.fsRepTurnover(ctx, tenant, s, &r, port)
	case "closing":
		err = o.fsRepClosing(ctx, tenant, s, &r, port)
	case "cg":
		err = o.fsRepCG(ctx, tenant, s, &r)
	case "pledges":
		err = o.fsRepPledges(ctx, tenant, s, &r)
	}
	r.normalise()
	return r, err
}

func a2s(a money.Amount) string { return a.Dec().Fixed(2) }

// fsLinesIn are the live lines of a period in a portfolio ("" all).
func fsLinesIn(ts []FSTxn, port, from, to string) []FSTxn {
	var out []FSTxn
	for _, t := range ts {
		if t.live() && t.Date >= from && t.Date <= to && (port == "" || t.Portfolio == port) {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		if out[i].Symbol != out[j].Symbol {
			return out[i].Symbol < out[j].Symbol
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (o *Ops) fsRepRegister(ctx context.Context, tenant string, s FSSettings, r *ClientReport, port string, heads bool) error {
	if port == PortSIT && !s.has(PortSIT) || port == PortInv && !s.has(PortInv) {
		port = ""
	}
	_, ts, b := o.fsBookTo(ctx, tenant, "")
	cols := []Column{{"date", "Date", ColDate}, {"port", "Portfolio", ColText}, {"scrip", "Scrip", ColText}, {"isin", "ISIN", ColText}, {"type", "Type", ColText}, {"side", "B/S", ColText},
		{"qty", "Qty", ColQty}, {"intraday", "Of which intraday", ColQty}, {"rate", "Rate", ColMoney}, {"value", "Value", ColMoney}}
	if heads {
		for _, h := range fsChargeHeads {
			cols = append(cols, Column{h.ID, h.Name, ColMoney})
		}
	} else {
		cols = append(cols, Column{"charges", "Charges", ColMoney})
	}
	cols = append(cols, Column{"net", "Net amount", ColMoney}, Column{"ref", "Line", ColText})
	sec := Section{Title: "Purchases and sales", Columns: cols, Empty: "No purchases or sales in this period."}
	var buy, sell, chg money.Amount
	tc := FSCharges{}
	for _, t := range fsLinesIn(ts, port, r.From, r.To) {
		if t.Type == FSTypeOpening {
			continue
		}
		rate := ""
		if t.Qty.Sign() > 0 {
			q, _ := t.Value.Dec().Quo(t.Qty)
			rate = q.Fixed(2)
		}
		spec := ""
		if res := b.Res[t.ID]; res != nil && res.SpecQty.Sign() > 0 {
			spec = fmtQty(res.SpecQty)
		}
		net := t.Value + t.Charges.Total()
		if t.Side == "S" {
			net = t.Value - t.Charges.Total()
			sell += t.Value
		} else {
			buy += t.Value
		}
		row := []string{t.Date, PortfolioName(t.Portfolio), t.Symbol, t.ISIN, t.Type, map[string]string{"B": "Buy", "S": "Sell"}[t.Side], fmtQty(t.Qty), spec, rate, a2s(t.Value)}
		if heads {
			for _, h := range fsChargeHeads {
				row = append(row, a2s(t.Charges.get(h.ID)))
			}
		} else {
			row = append(row, a2s(t.Charges.Total()))
		}
		sec.Rows = append(sec.Rows, append(row, a2s(net), t.ID))
		chg += t.Charges.Total()
		tc = tc.add(t.Charges)
	}
	if len(sec.Rows) > 0 {
		tot := []string{"Total", "", "", "", "", "", "", "", "", a2s(buy + sell)}
		if heads {
			for _, h := range fsChargeHeads {
				tot = append(tot, a2s(tc.get(h.ID)))
			}
		} else {
			tot = append(tot, a2s(chg))
		}
		sec.Totals = append(tot, "", "")
	}
	r.Sections = append(r.Sections, sec)
	r.Summary = []SummaryItem{{"Bought", a2s(buy), ColMoney, ""}, {"Sold", a2s(sell), ColMoney, ""}, {"Charges", a2s(chg), ColMoney, ""}}
	r.Notes = append(r.Notes, "Lines from the exchange trade files are one per trading code, exchange, scrip and side for the day, with the day's bill charges spread by value. Manual and off-market entries are shown once approved.")
	return nil
}

// fsScripAgg is a scrip's movement in a period.
type fsScripAgg struct {
	isin, sym                        string
	oq, bq, sq, cq, aq               money.Dec
	oa, ba, sa, sc, ca               money.Amount
	bh, ch                           FSCostHeads
	specPL, specSale, specBuy, specQ money.Amount
	nonPL, nonSale, nonCost          money.Amount
	nonQ, specQty                    money.Dec
	spec, non                        FSCharges
}

// fsScrips works out each scrip's opening, purchases, sales and closing in
// a portfolio over a period, quantity and amount apart (t6), with the
// profit or loss on what was sold.
func (o *Ops) fsScrips(ctx context.Context, tenant string, s FSSettings, port, from, to string) ([]*fsScripAgg, error) {
	ts := o.fsTxns(ctx, tenant)
	cas := o.fsCorpActions(ctx, tenant)
	open := fsReplay(s, ts, cas, addDays(from, -1))
	all := fsReplay(s, ts, cas, to)
	by := map[string]*fsScripAgg{}
	get := func(isin, sym string) *fsScripAgg {
		a := by[isin]
		if a == nil {
			a = &fsScripAgg{isin: isin, sym: sym}
			by[isin] = a
		}
		return a
	}
	for _, l := range open.lotsOf(port) {
		a := get(l.ISIN, l.Symbol)
		a.oq, a.oa = a.oq.Add(l.Qty), a.oa+l.Cost
	}
	for _, t := range fsLinesIn(ts, port, from, to) {
		if t.Side != "B" {
			continue
		}
		res := all.Res[t.ID]
		if res == nil {
			continue
		}
		a := get(t.ISIN, t.Symbol)
		a.bq, a.ba, a.bh = a.bq.Add(t.Qty), a.ba+res.CostIn, a.bh.add(res.HeadsIn)
	}
	for _, t := range fsLinesIn(ts, port, from, to) {
		res := all.Res[t.ID]
		if res == nil || t.Type == FSTypeOpening {
			continue
		}
		// Charges apart between the intraday part and the rest, by quantity.
		a := get(t.ISIN, t.Symbol)
		sp := t.Charges.part(res.SpecQty, t.Qty)
		if res.SpecQty.Sign() == 0 {
			sp = FSCharges{}
		}
		a.spec = a.spec.add(sp)
		a.non = a.non.add(t.Charges.sub(sp))
	}
	for _, m := range all.Matches {
		if m.Portfolio != port || m.Date < from || m.Date > to {
			continue
		}
		a := get(m.ISIN, m.Symbol)
		proc := m.Proceeds
		if m.Type == FSTypeBuyBack {
			proc = 0 // the amount is a deemed dividend
		}
		a.sq, a.sa, a.sc = a.sq.Add(m.Qty), a.sa+proc, a.sc+m.Cost
		if m.Spec {
			a.specPL += proc - m.Cost
			a.specSale += proc
			a.specBuy += m.Cost
			a.specQty = a.specQty.Add(m.Qty)
		} else {
			a.nonPL += proc - m.Cost
			a.nonSale += proc
			a.nonCost += m.Cost
			a.nonQ = a.nonQ.Add(m.Qty)
		}
	}
	for _, l := range all.lotsOf(port) {
		a := get(l.ISIN, l.Symbol)
		a.cq, a.ca, a.ch = a.cq.Add(l.Qty), a.ca+l.Cost, a.ch.add(l.Heads)
	}
	var out []*fsScripAgg
	for _, a := range by {
		// Corporate actions change quantity without a purchase or sale.
		a.aq = a.cq.Sub(a.oq).Sub(a.bq).Add(a.sq)
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sym < out[j].sym })
	return out, nil
}

func (o *Ops) fsRepSummary(ctx context.Context, tenant string, s FSSettings, r *ClientReport, port string, heads bool) error {
	xs, err := o.fsScrips(ctx, tenant, s, port, r.From, r.To)
	if err != nil {
		return err
	}
	corp := false
	for _, a := range xs {
		corp = corp || a.aq.Sign() != 0
	}
	cols := []Column{{"scrip", "Scrip", ColText}, {"isin", "ISIN", ColText}, {"oq", "Opening qty", ColQty}, {"oa", "Opening amount", ColMoney}, {"bq", "Bought qty", ColQty}, {"ba", "Bought amount", ColMoney}}
	if corp {
		cols = append(cols, Column{"aq", "Corporate actions qty", ColQty})
	}
	cols = append(cols, Column{"sq", "Sold qty", ColQty}, Column{"sa", "Sold amount (sale value)", ColMoney}, Column{"sc", "Cost of sold", ColMoney},
		Column{"cq", "Closing qty", ColQty}, Column{"ca", "Closing amount (cost)", ColMoney})
	headCols := []struct{ id, name string }{{"price", "price"}, {"brokerage", "brokerage"}, {"exchange", "exchange and SEBI"}, {"stamp", "stamp duty"}, {"gst", "GST"}, {"other", "other"}}
	hv := func(h FSCostHeads, id string) money.Amount {
		return map[string]money.Amount{"price": h.Price, "brokerage": h.Brokerage, "exchange": h.Exchange, "stamp": h.Stamp, "gst": h.GST, "other": h.Other}[id]
	}
	if heads {
		for _, h := range headCols {
			cols = append(cols, Column{"c" + h.id, "Closing cost: " + h.name, ColMoney})
		}
	}
	cols = append(cols, Column{"pl", "Profit / loss", ColMoney})
	sec := Section{Title: "Scrip-wise stock summary", Columns: cols, Empty: "Nothing held or traded in this period."}
	var t struct {
		oa, ba, sa, sc, ca, pl money.Amount
		ch                     FSCostHeads
	}
	for _, a := range xs {
		row := []string{a.sym, a.isin, fmtQty(a.oq), a2s(a.oa), fmtQty(a.bq), a2s(a.ba)}
		if corp {
			row = append(row, fmtQty(a.aq))
		}
		row = append(row, fmtQty(a.sq), a2s(a.sa), a2s(a.sc), fmtQty(a.cq), a2s(a.ca))
		if heads {
			for _, h := range headCols {
				row = append(row, a2s(hv(a.ch, h.id)))
			}
		}
		pl := a.sa - a.sc
		sec.Rows = append(sec.Rows, append(row, a2s(pl)))
		t.oa, t.ba, t.sa, t.sc, t.ca, t.pl, t.ch = t.oa+a.oa, t.ba+a.ba, t.sa+a.sa, t.sc+a.sc, t.ca+a.ca, t.pl+pl, t.ch.add(a.ch)
	}
	if len(sec.Rows) > 0 {
		tot := []string{"Total", "", "", a2s(t.oa), "", a2s(t.ba)}
		if corp {
			tot = append(tot, "")
		}
		tot = append(tot, "", a2s(t.sa), a2s(t.sc), "", a2s(t.ca))
		if heads {
			for _, h := range headCols {
				tot = append(tot, a2s(hv(t.ch, h.id)))
			}
		}
		sec.Totals = append(tot, a2s(t.pl))
	}
	r.Sections = append(r.Sections, sec)
	r.Summary = []SummaryItem{{"Opening (cost)", a2s(t.oa), ColMoney, ""}, {"Bought", a2s(t.ba), ColMoney, ""}, {"Sold (sale value)", a2s(t.sa), ColMoney, ""},
		{"Closing (cost)", a2s(t.ca), ColMoney, ""}, {"Profit / loss on sales", a2s(t.pl), ColMoney, tone(t.pl.Dec())}}
	note := "Amounts are at cost, first in, first out; the profit or loss is the sale value less the cost of what was sold, before charges."
	if port == PortSIT && s.SITCharges != "cost" || port == PortInv && s.InvCost == "price" {
		note += " Charges are expenses here, not part of the cost (Settings → Firm practices → Firm securities)."
	}
	r.Notes = append(r.Notes, note, "Opening + bought − cost of sold = closing, scrip by scrip (corporate actions change the quantity only; a bonus comes at nil cost).")
	return nil
}

func (o *Ops) fsRepPnL(ctx context.Context, tenant string, s FSSettings, r *ClientReport, port string) error {
	xs, err := o.fsScrips(ctx, tenant, s, port, r.From, r.To)
	if err != nil {
		return err
	}
	spec := Section{Title: "Speculative (bought and sold the same day)", Columns: []Column{{"scrip", "Scrip", ColText}, {"qty", "Qty", ColQty}, {"buy", "Purchase value", ColMoney}, {"sale", "Sale value", ColMoney}, {"pl", "Profit / loss", ColMoney}, {"chg", "Charges", ColMoney}, {"net", "Net", ColMoney}}, Empty: "No intraday trades in this period."}
	non := Section{Title: "Non-speculative (delivery)", Columns: []Column{{"scrip", "Scrip", ColText}, {"qty", "Qty sold", ColQty}, {"cost", "Cost of sold", ColMoney}, {"sale", "Sale value", ColMoney}, {"pl", "Profit / loss", ColMoney}, {"chg", "Charges", ColMoney}, {"net", "Net", ColMoney}}, Empty: "No delivery sales in this period."}
	var sp, np, sc, nc money.Amount
	var specC, nonC FSCharges
	for _, a := range xs {
		if a.specQty.Sign() > 0 || a.spec.Total() != 0 {
			spec.Rows = append(spec.Rows, []string{a.sym, fmtQty(a.specQty), a2s(a.specBuy), a2s(a.specSale), a2s(a.specPL), a2s(a.spec.Total()), a2s(a.specPL - a.spec.Total())})
			sp, sc = sp+a.specPL, sc+a.spec.Total()
		}
		if a.nonQ.Sign() > 0 || a.non.Total() != 0 {
			non.Rows = append(non.Rows, []string{a.sym, fmtQty(a.nonQ), a2s(a.nonCost), a2s(a.nonSale), a2s(a.nonPL), a2s(a.non.Total()), a2s(a.nonPL - a.non.Total())})
			np, nc = np+a.nonPL, nc+a.non.Total()
		}
		specC, nonC = specC.add(a.spec), nonC.add(a.non)
	}
	if len(spec.Rows) > 0 {
		spec.Totals = []string{"Total", "", "", "", a2s(sp), a2s(sc), a2s(sp - sc)}
	}
	if len(non.Rows) > 0 {
		non.Totals = []string{"Total", "", "", "", a2s(np), a2s(nc), a2s(np - nc)}
	}
	ch := Section{Title: "Charges head by head", Columns: []Column{{"head", "Charge", ColText}, {"spec", "Speculative", ColMoney}, {"non", "Non-speculative", ColMoney}, {"total", "Total", ColMoney}}}
	for _, h := range fsChargeHeads {
		ch.Rows = append(ch.Rows, []string{h.Name, a2s(specC.get(h.ID)), a2s(nonC.get(h.ID)), a2s(specC.get(h.ID) + nonC.get(h.ID))})
	}
	ch.Totals = []string{"Total", a2s(specC.Total()), a2s(nonC.Total()), a2s(specC.Total() + nonC.Total())}
	r.Sections = append(r.Sections, spec, non, ch)
	r.Summary = []SummaryItem{{"Speculative result", a2s(sp - sc), ColMoney, tone((sp - sc).Dec())}, {"Non-speculative result", a2s(np - nc), ColMoney, tone((np - nc).Dec())}, {"Charges", a2s(sc + nc), ColMoney, ""}}
	r.Notes = append(r.Notes, "Speculative: a scrip bought and sold the same day under the firm's own codes and not delivered (section 43(5) of the Income-tax Act, 1961). Its loss is set off only against speculative income. A day's charges are split between the intraday part and the rest by quantity.",
		"Before charges, the results are at cost first in, first out. With charges treated as expenses the charges, STT included (section 36(1)(xv)), are deducted here.")
	if port == PortInv {
		r.Notes = append(r.Notes, "For investments the delivery result is a capital gain: see the capital gains register.")
	}
	return nil
}

// fsRepTurnover is the turnover the tax audit report (Form 3CD) asks for,
// worked out as the ICAI's Guidance Note on Tax Audit under section 44AB
// says: for speculative transactions the sum of the favourable and
// unfavourable differences, each taken as positive; for delivery-based
// transactions the sale value.
func (o *Ops) fsRepTurnover(ctx context.Context, tenant string, s FSSettings, r *ClientReport, port string) error {
	ts := o.fsTxns(ctx, tenant)
	b := fsReplay(s, ts, o.fsCorpActions(ctx, tenant), r.To)
	type key struct{ date, isin string }
	diff := map[key]money.Amount{}
	sym := map[key]string{}
	var sale money.Amount
	var nSale int
	for _, m := range b.Matches {
		if m.Portfolio != port || m.Date < r.From || m.Date > r.To {
			continue
		}
		if m.Spec {
			k := key{m.Date, m.ISIN}
			diff[k] += m.Proceeds - m.Cost
			sym[k] = m.Symbol
		}
	}
	for _, t := range fsLinesIn(ts, port, r.From, r.To) {
		if t.Side != "S" || t.Type == FSTypeJournal || t.Type == FSTypeBuyBack {
			continue
		}
		v := t.Value
		if res := b.Res[t.ID]; res != nil {
			v -= res.SpecValue
		}
		if v != 0 {
			sale += v
			nSale++
		}
	}
	sec := Section{Title: "Speculative transactions (each scrip and day)", Columns: []Column{{"date", "Date", ColDate}, {"scrip", "Scrip", ColText}, {"diff", "Difference", ColMoney}, {"abs", "Taken as positive", ColMoney}}, Empty: "No intraday trades in this period."}
	var spec, net money.Amount
	keys := make([]key, 0, len(diff))
	for k := range diff {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].date != keys[j].date {
			return keys[i].date < keys[j].date
		}
		return sym[keys[i]] < sym[keys[j]]
	})
	for _, k := range keys {
		d := diff[k]
		ab := d
		if ab < 0 {
			ab = -ab
		}
		spec += ab
		net += d
		sec.Rows = append(sec.Rows, []string{k.date, sym[k], a2s(d), a2s(ab)})
	}
	if len(sec.Rows) > 0 {
		sec.Totals = []string{"Total", "", a2s(net), a2s(spec)}
	}
	sum := Section{Title: "Turnover", Columns: []Column{{"what", "", ColText}, {"amount", "Turnover", ColMoney}}, Rows: [][]string{
		{"Speculative (intraday): sum of differences taken as positive", a2s(spec)},
		{fmt.Sprintf("Non-speculative (delivery): sale value of %d sales", nSale), a2s(sale)},
	}, Totals: []string{"Total", a2s(spec + sale)}}
	r.Sections = append(r.Sections, sum, sec)
	r.Summary = []SummaryItem{{"Speculative turnover", a2s(spec), ColMoney, ""}, {"Delivery turnover", a2s(sale), ColMoney, ""}, {"Total", a2s(spec + sale), ColMoney, ""}}
	r.Notes = append(r.Notes, "As the ICAI's Guidance Note on Tax Audit under section 44AB describes turnover for these transactions. The firm's own futures and options are not in the securities register: add their turnover (the sum of the differences, and the premium on options sold) from the derivatives books.")
	return nil
}

func (o *Ops) fsRepClosing(ctx context.Context, tenant string, s FSSettings, r *ClientReport, port string) error {
	_, _, b := o.fsBookTo(ctx, tenant, r.To)
	_, look, _ := o.Market(ctx, tenant, r.To)
	v := o.fsValue(ctx, tenant, s, b, look, r.To, port)
	cols := []Column{{"scrip", "Scrip", ColText}, {"isin", "ISIN", ColText}, {"kind", "Kind", ColText}, {"cat", "ICDS VIII category", ColText}}
	if port == PortInv {
		cols = append(cols, Column{"term", "Term", ColText})
	}
	cols = append(cols, Column{"qty", "Qty", ColQty}, Column{"cost", "Cost", ColMoney}, Column{"price", "Closing price", ColMoney}, Column{"market", "Market value", ColMoney}, Column{"lcm", "Lower of cost and market", ColMoney}, Column{"value", "Value taken", ColMoney})
	sec := Section{Title: "Scrip by scrip", Columns: cols, Empty: "Nothing held on this date."}
	catName := map[string]string{}
	for _, c := range FSCategories {
		catName[c.ID] = c.Name
	}
	for _, x := range v.Rows {
		price := "unlisted: at cost"
		if x.HasPrice {
			price = x.Price.Fixed(2)
		} else if x.Listed {
			price = "no price: at cost"
		}
		row := []string{x.Symbol, x.ISIN, x.KindName, catName[x.Category]}
		if port == PortInv {
			row = append(row, map[string]string{TermLong: "Long-term", TermCurrent: "Current"}[x.Term])
		}
		sec.Rows = append(sec.Rows, append(row, fmtQty(x.Qty), a2s(x.Cost), price, a2s(x.Market), a2s(x.LCM), a2s(x.Value)))
	}
	if len(sec.Rows) > 0 {
		t := []string{"Total", "", "", ""}
		if port == PortInv {
			t = append(t, "")
		}
		sec.Totals = append(t, "", a2s(v.AtCost), "", a2s(v.Market), a2s(v.LCMScrip), a2s(v.Value))
	}
	cat := Section{Title: "Category by category (ICDS VIII)", Columns: []Column{{"cat", "Category", ColText}, {"cost", "Cost (quoted)", ColMoney}, {"market", "Market value (quoted)", ColMoney}, {"atcost", "Not quoted: at cost", ColMoney}, {"value", "Value", ColMoney}}}
	for _, c := range v.Categories {
		cat.Rows = append(cat.Rows, []string{c.Name, a2s(c.Cost), a2s(c.Market), a2s(c.AtCost), a2s(c.Value)})
	}
	cat.Totals = []string{"Total", "", "", "", a2s(v.LCMCat)}
	three := Section{Title: "The three values", Columns: []Column{{"basis", "Basis", ColText}, {"value", "Value", ColMoney}, {"used", "Taken by the books", ColText}}, Rows: [][]string{
		{"At cost", a2s(v.AtCost), map[bool]string{true: "Yes", false: ""}[v.Basis == "cost"]},
		{"Lower of cost and market value, scrip by scrip", a2s(v.LCMScrip), map[bool]string{true: "Yes", false: ""}[v.Basis == "lcm-scrip" || v.Basis == "lcm-all"]},
		{"Lower of cost and market value, category by category (ICDS VIII, for tax)", a2s(v.LCMCat), map[bool]string{true: "Yes", false: ""}[v.Basis == "lcm-category"]},
	}}
	r.Sections = append(r.Sections, three, sec)
	if port == PortSIT {
		r.Sections = append(r.Sections, cat)
	}
	r.Summary = []SummaryItem{{"At cost", a2s(v.AtCost), ColMoney, ""}, {"Market value", a2s(v.Market), ColMoney, ""}, {"Lower, scrip by scrip", a2s(v.LCMScrip), ColMoney, ""}, {"Lower, category by category", a2s(v.LCMCat), ColMoney, ""}, {"Value taken (" + strings.ToLower(v.BasisName) + ")", a2s(v.Value), ColMoney, ""}}
	r.Notes = append(r.Notes, "ICDS VIII (Securities, Part A): securities held as stock-in-trade are valued at the lower of actual cost and net realisable value, compared category by category (shares, debt securities, convertible securities, any other); unlisted securities, or listed ones not quoted regularly, at actual cost.")
	r.Notes = append(r.Notes, v.Warnings...)
	r.Notes = append(r.Notes, s.fsWarnings()...)
	return nil
}

func (o *Ops) fsRepCG(ctx context.Context, tenant string, s FSSettings, r *ClientReport) error {
	ts := o.fsTxns(ctx, tenant)
	b := fsReplay(s, ts, o.fsCorpActions(ctx, tenant), r.To)
	fmv := o.fmv2018(ctx, tenant)
	fmvLots := map[string]money.Dec{}
	for _, t := range ts {
		if t.Type == FSTypeOpening && t.FMV2018.Sign() > 0 {
			fmvLots[t.ID] = t.FMV2018
		}
	}
	cols := []Column{{"date", "Sale date", ColDate}, {"scrip", "Scrip", ColText}, {"isin", "ISIN", ColText}, {"kind", "Kind", ColText}, {"qty", "Qty", ColQty}, {"acq", "Acquired", ColDate}, {"cost", "Cost", ColMoney},
		{"fmv", "FMV 31 Jan 2018", ColMoney}, {"tcost", "Cost for tax", ColMoney}, {"sale", "Sale value", ColMoney}, {"gain", "Gain / loss", ColMoney}, {"days", "Days", ColQty}, {"term", "Term", ColText}, {"sec", "Section", ColText}, {"how", "How", ColText}}
	sec := Section{Title: "Sales of investments", Columns: cols, Empty: "No investments sold in this period."}
	conv := Section{Title: "Investments converted into stock-in-trade, sold in this period (section 45(2))", Columns: cols, Empty: "None sold in this period."}
	tot := map[string]money.Dec{}
	noFMV := 0
	for _, m := range b.Matches {
		if m.Date < r.From || m.Date > r.To || m.NoLot || m.Spec {
			continue
		}
		k := s.kind(m.Kind)
		class := cgClassOf(k.ID, m.Symbol)
		if k.Own {
			class = k.Tax
		}
		f := fmv[m.ISIN]
		if x, ok := fmvLots[m.Lot]; ok {
			f = x
		}
		switch {
		case m.Portfolio == PortInv && (m.Type == "Sale" || m.Type == FSTypeBuyBack):
			acq, cost := firstOf(m.TaxDate, m.BuyDate), firstAmt(m.TaxCost, m.Cost)
			proc := m.Proceeds
			how := "Sold"
			if m.Type == FSTypeBuyBack {
				proc, how = 0, "Buy-back: consideration nil (section 46A, from 1 Oct 2024); the amount is a deemed dividend"
			}
			l := cgWork(class, acq, m.Date, m.Qty, cost.Dec(), proc.Dec(), f)
			if l.Grand && !l.HasFMV {
				noFMV++
			}
			fs := ""
			if l.HasFMV {
				fs = d2s(l.FMV)
			}
			sec.Rows = append(sec.Rows, []string{m.Date, m.Symbol, m.ISIN, k.Name, fmtQty(m.Qty), acq, a2s(cost), fs, d2s(l.TaxCost), a2s(proc), d2s(l.Gain), fmt.Sprint(l.Days), l.Term, cgSectionName(l.Section, m.Date), how})
			tot[l.Section] = tot[l.Section].Add(l.Gain)
		case m.Portfolio == PortSIT && m.Converted != nil && (m.Type == "Sale" || m.Type == FSTypeBuyBack):
			c := m.Converted
			l := cgWork(class, c.Acquired, c.On, m.Qty, c.Cost.Dec(), c.FMV.Dec(), f)
			fs := ""
			if l.HasFMV {
				fs = d2s(l.FMV)
			}
			conv.Rows = append(conv.Rows, []string{m.Date, m.Symbol, m.ISIN, k.Name, fmtQty(m.Qty), c.Acquired, a2s(c.Cost), fs, d2s(l.TaxCost), a2s(c.FMV), d2s(l.Gain), fmt.Sprint(l.Days), l.Term, cgSectionName(l.Section, c.On),
				"Converted on " + fmtDay(c.On) + "; the market value then is the sale value for capital gains, and the cost of the stock-in-trade"})
			tot[l.Section] = tot[l.Section].Add(l.Gain)
		}
	}
	r.Sections = append(r.Sections, sec, conv)
	exempt := CG112AExemption(r.To).Dec()
	lt112A := tot["112A"]
	taxable := lt112A.Sub(exempt)
	if taxable.Sign() < 0 {
		taxable = money.Dec{}
	}
	r.Summary = []SummaryItem{{"Short-term, " + cgSectionName("111A", r.To) + " (" + cgRate("111A", r.To) + ")", d2s(tot["111A"]), ColMoney, tone(tot["111A"])},
		{"Long-term, " + cgSectionName("112A", r.To), d2s(lt112A), ColMoney, tone(lt112A)},
		{"of which taxed (above ₹" + formatINR(exempt) + ")", d2s(taxable), ColMoney, ""},
		{"Long-term, " + cgSectionName("112", r.To) + " (" + cgRate("112", r.To) + ")", d2s(tot["112"]), ColMoney, tone(tot["112"])},
		{"Short-term at normal rates", d2s(tot["slab"].Add(tot["50AA"])), ColMoney, tone(tot["slab"].Add(tot["50AA"]))}}
	if s.Term == "lot" {
		r.Notes = append(r.Notes, "Long-term or current in the books is as each lot was marked when bought; for tax the holding period decides, as here.")
	}
	r.Notes = append(r.Notes, fmt.Sprintf("Rates for transfers on or after 23 July 2024 (Finance (No. 2) Act, 2024): %s %s, %s %s on gains above ₹1.25 lakh, other long-term gains 12.5%% without indexation. Bought before 1 February 2018 and long-term: grandfathered at the 31 January 2018 value (section 55(2)(ac)).",
		cgSectionName("111A", r.To), cgRate("111A", r.To), cgSectionName("112A", r.To), strings.TrimSuffix(cgRate("112A", r.To), " above ₹1.25 lakh")))
	if n := fmvNote(noFMV); n != "" {
		r.Notes = append(r.Notes, n)
	}
	if !s.CGRegister {
		r.Notes = append(r.Notes, "The firm's practice is not to keep a capital gains register (Settings → Firm practices → Firm securities); this is worked out on request.")
	}
	if newTaxAct(r.To) {
		r.Notes = append(r.Notes, "From 1 April 2026 the Income-tax Act, 2025 applies: sections 111A, 112 and 112A of the 1961 Act are sections 196, 197 and 198.")
	}
	return nil
}

func (o *Ops) fsRepPledges(ctx context.Context, tenant string, s FSSettings, r *ClientReport) error {
	_, _, b := o.fsBookTo(ctx, tenant, r.To)
	_, look, _ := o.Market(ctx, tenant, r.To)
	sec := Section{Title: "Pledged on " + fmtDay(r.To), Columns: []Column{{"id", "Pledge", ColText}, {"date", "Pledged on", ColDate}, {"port", "Portfolio", ColText}, {"scrip", "Scrip", ColText}, {"isin", "ISIN", ColText},
		{"qty", "Qty", ColQty}, {"held", "Held", ColQty}, {"pledgee", "Pledgee", ColText}, {"type", "Pledgee type", ColText}, {"purpose", "Purpose", ColText}, {"ref", "Pledge number", ColText}, {"cost", "Cost", ColMoney}, {"market", "Market value", ColMoney}}, Empty: "Nothing pledged on this date."}
	var tc, tm money.Amount
	byType := map[string]money.Amount{}
	for _, p := range o.fsPledges(ctx, tenant) {
		if !p.openOn(r.To) {
			continue
		}
		held := b.held(p.Portfolio, p.ISIN)
		var cost money.Amount
		left := p.Qty
		for _, l := range b.Lots[fsKey(p.Portfolio, p.ISIN)] {
			if left.Sign() == 0 {
				break
			}
			q := money.Min(l.Qty, left)
			cost += share(l.Cost, q, l.Qty)
			left = left.Sub(q)
		}
		mkt := cost
		if pr, ok := o.fsPrice(look, p.ISIN); ok {
			mkt = pr.Mul(p.Qty).Round(money.UnitPaisa)
		}
		sec.Rows = append(sec.Rows, []string{p.ID, p.Date, PortfolioName(p.Portfolio), p.Symbol, p.ISIN, fmtQty(p.Qty), fmtQty(held), p.Pledgee, p.PledgeeType, p.Purpose, p.Ref, a2s(cost), a2s(mkt)})
		tc, tm = tc+cost, tm+mkt
		byType[p.PledgeeType] += mkt
	}
	if len(sec.Rows) > 0 {
		sec.Totals = []string{"Total", "", "", "", "", "", "", "", "", "", "", a2s(tc), a2s(tm)}
	}
	r.Sections = append(r.Sections, sec)
	r.Summary = []SummaryItem{{"Pledged at cost", a2s(tc), ColMoney, ""}, {"Pledged at market value", a2s(tm), ColMoney, ""}}
	for _, t := range FSPledgeeTypes {
		if byType[t] != 0 {
			r.Summary = append(r.Summary, SummaryItem{t, a2s(byType[t]), ColMoney, ""})
		}
	}
	r.Notes = append(r.Notes, "Pledges are tracked, not blocked: a sale into pledged units goes through with a warning. Pledged securities are deducted in the net worth.")
	return nil
}
