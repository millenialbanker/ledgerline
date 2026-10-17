package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- purchase lots (cost basis) ----

// Lot is one purchase of a security still held: the basis for unrealised
// P&L and for capital gains when it is sold.
type Lot struct {
	ISIN   string    `json:"isin"`
	Symbol string    `json:"symbol"`
	Qty    int64     `json:"qty"`
	Date   string    `json:"date"` // acquisition date; "" if unknown
	Price  money.Dec `json:"price"`
}

// LotBook is a client's open purchase lots as at the end of AsOf.
type LotBook struct {
	UCC  string `json:"ucc"`
	AsOf string `json:"as_of"`
	Lots []Lot  `json:"lots"`
}

// Sale is a realised gain or loss: one sell matched to one purchase lot,
// or a same-day buy and sell (speculative).
type Sale struct {
	Date      string    `json:"date"`
	ISIN      string    `json:"isin"`
	Symbol    string    `json:"symbol"`
	Qty       int64     `json:"qty"`
	BuyDate   string    `json:"buy_date"`
	BuyPrice  money.Dec `json:"buy_price"`
	SellPrice money.Dec `json:"sell_price"`
	Cost      money.Dec `json:"cost"`
	Proceeds  money.Dec `json:"proceeds"`
	Gain      money.Dec `json:"gain"`
	Days      int       `json:"days"`
	Category  string    `json:"category"` // Speculative, STCG, LTCG, Cost unknown
}

// Gain categories for listed equity.
const (
	CatSpec    = "Speculative (intraday)"
	CatSTCG    = "Short-term capital gain"
	CatLTCG    = "Long-term capital gain"
	CatUnknown = "Cost not available"
)

// equityBook replays a client's cash-market trades over their purchase lots
// up to and including `to`. Within a day, matched buy and sell quantity in a
// scrip is speculative; the remainder adds a lot or sells lots first-in
// first-out. Prices exclude charges, which are reported separately.
func (o *Ops) equityBook(ctx context.Context, tenant, ucc, to string) (open []Lot, sales []Sale, asOf string, err error) {
	book, _, gerr := docs.Get[LotBook](ctx, o.Docs, tenant, cLot, ucc)
	if gerr == nil {
		open, asOf = append(open, book.Lots...), book.AsOf
	} else if errors.Is(gerr, docs.ErrNotFound) {
		// No lot file: fall back to demat balances at their average cost,
		// acquisition dates unknown.
		hs, herr := o.Holdings(ctx, tenant, ucc)
		if herr != nil {
			return nil, nil, "", herr
		}
		for _, h := range hs {
			if h.Qty > 0 {
				open = append(open, Lot{ISIN: h.ISIN, Symbol: h.Symbol, Qty: h.Qty, Price: h.AvgCost})
			}
		}
		asOf = o.firstTradeDate(ctx, tenant)
		if asOf != "" {
			asOf = addDays(asOf, -1)
		}
	} else {
		return nil, nil, "", gerr
	}
	if asOf == "" || asOf >= to {
		return open, nil, asOf, nil
	}
	cas, _ := docs.List[CorpAction](ctx, o.Docs, tenant, cCorpAction, "")
	for d := addDays(asOf, 1); d <= to; d = addDays(d, 1) {
		open = applyCorpLots(open, cas, d)
		ts, err := o.exchangeTrades(ctx, tenant, d)
		if err != nil {
			return nil, nil, "", err
		}
		type agg struct {
			sym    string
			bq, sq int64
			bv, sv money.Dec
		}
		day := map[string]*agg{}
		var order []string
		var look *Lookup
		for _, t := range ts {
			if t.UCC != ucc || t.Segment != "CM" {
				continue
			}
			if look == nil {
				_, l, _ := o.Market(ctx, tenant, d)
				look = &l
			}
			isin := t.Symbol
			if i, ok := look.ForTrade(t); ok && i.ISIN != "" {
				isin = i.ISIN
			} else {
				for _, l := range open {
					if l.Symbol == t.Symbol {
						isin = l.ISIN
						break
					}
				}
			}
			a := day[isin]
			if a == nil {
				a = &agg{sym: t.Symbol}
				day[isin] = a
				order = append(order, isin)
			}
			v := t.Price.MulInt(t.Qty)
			if t.Side == "B" {
				a.bq, a.bv = a.bq+t.Qty, a.bv.Add(v)
			} else {
				a.sq, a.sv = a.sq+t.Qty, a.sv.Add(v)
			}
		}
		sort.Strings(order)
		for _, isin := range order {
			a := day[isin]
			var bp, sp money.Dec
			if a.bq > 0 {
				bp, _ = a.bv.Quo(money.DecInt(a.bq))
			}
			if a.sq > 0 {
				sp, _ = a.sv.Quo(money.DecInt(a.sq))
			}
			if m := min(a.bq, a.sq); m > 0 {
				cost, proc := bp.MulInt(m), sp.MulInt(m)
				sales = append(sales, Sale{Date: d, ISIN: isin, Symbol: a.sym, Qty: m, BuyDate: d, BuyPrice: bp, SellPrice: sp, Cost: cost, Proceeds: proc, Gain: proc.Sub(cost), Category: CatSpec})
			}
			switch {
			case a.bq > a.sq:
				open = append(open, Lot{ISIN: isin, Symbol: a.sym, Qty: a.bq - a.sq, Date: d, Price: bp})
			case a.sq > a.bq:
				left := a.sq - a.bq
				for i := 0; i < len(open) && left > 0; i++ {
					l := &open[i]
					if l.ISIN != isin || l.Qty == 0 {
						continue
					}
					q := min(l.Qty, left)
					cost, proc := l.Price.MulInt(q), sp.MulInt(q)
					s := Sale{Date: d, ISIN: isin, Symbol: a.sym, Qty: q, BuyDate: l.Date, BuyPrice: l.Price, SellPrice: sp, Cost: cost, Proceeds: proc, Gain: proc.Sub(cost)}
					s.Category, s.Days = gainCategory(l.Date, d)
					sales = append(sales, s)
					l.Qty -= q
					left -= q
				}
				if left > 0 {
					proc := sp.MulInt(left)
					sales = append(sales, Sale{Date: d, ISIN: isin, Symbol: a.sym, Qty: left, SellPrice: sp, Proceeds: proc, Category: CatUnknown})
				}
			}
		}
		kept := open[:0]
		for _, l := range open {
			if l.Qty > 0 {
				kept = append(kept, l)
			}
		}
		open = kept
	}
	return open, sales, asOf, nil
}

// gainCategory classifies listed equity held from buy to sell: more than
// twelve months is long-term.
func gainCategory(buy, sell string) (string, int) {
	if buy == "" {
		return CatUnknown, 0
	}
	days := daysBetween(buy, sell)
	tb, err := time.Parse("2006-01-02", buy)
	if err != nil {
		return CatUnknown, 0
	}
	if sell > tb.AddDate(1, 0, 0).Format("2006-01-02") {
		return CatLTCG, days
	}
	return CatSTCG, days
}

func addDays(d string, n int) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

// firstTradeDate is the earliest date with market data, a proxy for the
// first operating day when no lot file was loaded.
func (o *Ops) firstTradeDate(ctx context.Context, tenant string) string {
	ms, err := o.Docs.List(ctx, tenant, cMarket, "")
	if err != nil || len(ms) == 0 {
		return ""
	}
	first := ms[0].ID
	for _, m := range ms {
		if m.ID < first {
			first = m.ID
		}
	}
	return first
}

// ---- report model ----

// Column types drive formatting: money and qty right-aligned, date as
// DD-Mon-YYYY, text as is.
const (
	ColText  = "text"
	ColMoney = "money"
	ColQty   = "qty"
	ColPct   = "pct"
	ColDate  = "date"
	ColDrCr  = "drcr" // signed: positive = debit (client owes)
)

type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

type Section struct {
	Title   string     `json:"title"`
	Columns []Column   `json:"columns"`
	Rows    [][]string `json:"rows"`
	Totals  []string   `json:"totals,omitempty"`
	Note    string     `json:"note,omitempty"`
	Empty   string     `json:"empty,omitempty"`
}

type SummaryItem struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Type  string `json:"type"`
	Tone  string `json:"tone,omitempty"` // good, bad
}

// ClientReport is a client statement or report ready for screen, print or CSV.
type ClientReport struct {
	Kind      string        `json:"kind"`
	Title     string        `json:"title"`
	UCC       string        `json:"ucc"`
	Name      string        `json:"name,omitempty"`
	PAN       string        `json:"pan,omitempty"`
	Address   string        `json:"address,omitempty"`
	From      string        `json:"from,omitempty"`
	To        string        `json:"to"`
	Period    string        `json:"period"`
	Summary   []SummaryItem `json:"summary"`
	Sections  []Section     `json:"sections"`
	Notes     []string      `json:"notes,omitempty"`
	Generated string        `json:"generated"`
}

// ClientReportDef describes one report kind for the console.
type ClientReportDef struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	About   string `json:"about"`
	Range   bool   `json:"range"`   // takes from and to; otherwise as-on date
	AllUCC  bool   `json:"all_ucc"` // can run for every client at once
	Default string `json:"default"` // default period: day, month, fy
}

var ClientReportDefs = []ClientReportDef{
	{"ledger", "Ledger statement", "Every debit and credit with running balance, opening and closing.", true, true, "fy"},
	{"holdings", "Holding statement", "Securities held with quantity, pledged quantity, average cost, market value and unrealised P&L.", false, true, ""},
	{"portfolio", "Portfolio report", "Net worth: ledger, equity holdings at market, open derivatives MTM, collateral, and realised P&L for the period.", true, false, "fy"},
	{"pnl", "Profit & loss / capital gains", "Realised equity gains lot by lot (speculative, short-term, long-term), derivatives P&L and charges, for tax filing.", true, false, "fy"},
	{"tradebook", "Trade book", "Every trade across exchanges and segments in the period.", true, true, "month"},
	{"positions", "Open positions", "Open futures and options with average price, last price, MTM and margin.", false, false, ""},
}

// ClientReportFor builds a report. ucc may be "ALL" for kinds that allow it.
func (o *Ops) ClientReportFor(ctx context.Context, tenant, kind, ucc, from, to string) (ClientReport, error) {
	var def *ClientReportDef
	for i := range ClientReportDefs {
		if ClientReportDefs[i].Kind == kind {
			def = &ClientReportDefs[i]
		}
	}
	if def == nil {
		return ClientReport{}, fmt.Errorf("%w: client report %q", ErrNotFound, kind)
	}
	if !validDate(to) {
		return ClientReport{}, invalid("to must be a date (YYYY-MM-DD)")
	}
	if def.Range {
		if !validDate(from) {
			return ClientReport{}, invalid("from must be a date (YYYY-MM-DD)")
		}
		if from > to {
			return ClientReport{}, invalid("from is after to")
		}
		if daysBetween(from, to) > 3*366 {
			return ClientReport{}, invalid("the period can be at most three years")
		}
	} else {
		from = ""
	}
	r := ClientReport{Kind: kind, Title: def.Name, UCC: strings.ToUpper(ucc), From: from, To: to, Generated: o.now().Format("02-Jan-2006 15:04 IST")}
	if def.Range {
		r.Period = fmtDay(from) + " to " + fmtDay(to)
	} else {
		r.Period = "As on " + fmtDay(to)
	}
	if r.multi() {
		if !def.AllUCC {
			return r, invalid("%s is produced one client at a time", def.Name)
		}
		r.Name = "All clients"
		if t, ok := strings.CutPrefix(r.UCC, "TAG:"); ok {
			r.Name = "Group " + t
		}
	} else {
		c, err := o.Client(ctx, tenant, r.UCC)
		if err != nil {
			return r, err
		}
		r.Name, r.PAN, r.Address = c.Name, MaskPAN(c.PAN), c.City
	}
	var err error
	switch kind {
	case "ledger":
		err = o.repLedger(ctx, tenant, &r)
	case "holdings":
		err = o.repHoldings(ctx, tenant, &r)
	case "portfolio":
		err = o.repPortfolio(ctx, tenant, &r)
	case "pnl":
		err = o.repPnL(ctx, tenant, &r)
	case "tradebook":
		err = o.repTradebook(ctx, tenant, &r)
	case "positions":
		err = o.repPositions(ctx, tenant, &r)
	}
	r.normalise()
	return r, err
}

func validDate(s string) bool { _, err := time.Parse("2006-01-02", s); return err == nil }

func fmtDay(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.Format("02-Jan-2006")
}

func d2s(d money.Dec) string    { return d.Fixed(2) }
func amt(a money.Amount) string { return a.Dec().Fixed(2) }
func qty(n int64) string        { return fmt.Sprint(n) }
func pctOf(a, b money.Dec) string {
	if b.Sign() == 0 {
		return ""
	}
	q, _ := a.MulInt(100).Quo(b)
	return q.Fixed(2)
}

func tone(d money.Dec) string {
	switch d.Sign() {
	case 1:
		return "good"
	case -1:
		return "bad"
	}
	return ""
}

// ---- ledger ----

func (o *Ops) repLedger(ctx context.Context, tenant string, r *ClientReport) error {
	if r.multi() {
		cs, err := o.Clients(ctx, tenant)
		if err != nil {
			return err
		}
		sec := Section{Title: "Client balances", Columns: []Column{{"ucc", "UCC", ColText}, {"name", "Client", ColText}, {"opening", "Opening", ColDrCr}, {"debit", "Debits", ColMoney}, {"credit", "Credits", ColMoney}, {"closing", "Closing", ColDrCr}}}
		var to, td, tc, tcl money.Amount
		for _, c := range cs {
			if c.Status != "Active" || !r.has(c) {
				continue
			}
			st, err := o.App.Ledger.Statement(ctx, tenant, ledger.ClientAccount(c.UCC), r.From, r.To)
			if err != nil {
				return err
			}
			var d, cr money.Amount
			for _, l := range st.Lines {
				d, cr = d+l.Debit, cr+l.Credit
			}
			to, td, tc, tcl = to+st.Opening, td+d, tc+cr, tcl+st.Closing
			sec.Rows = append(sec.Rows, []string{c.UCC, c.Name, amt(st.Opening), amt(d), amt(cr), amt(st.Closing)})
		}
		sec.Totals = []string{"Total", "", amt(to), amt(td), amt(tc), amt(tcl)}
		r.Sections = append(r.Sections, sec)
		r.Summary = []SummaryItem{{"Clients", fmt.Sprint(len(sec.Rows)), ColQty, ""}, {"Net opening", amt(to), ColDrCr, ""}, {"Net closing", amt(tcl), ColDrCr, ""}}
		r.Notes = append(r.Notes, "Dr = client owes the broker; Cr = the broker holds the client's funds.")
		return nil
	}
	st, err := o.App.Ledger.Statement(ctx, tenant, ledger.ClientAccount(r.UCC), r.From, r.To)
	if err != nil {
		return err
	}
	sec := Section{Title: "Statement of account", Columns: []Column{{"date", "Date", ColDate}, {"particulars", "Particulars", ColText}, {"voucher", "Voucher", ColText}, {"debit", "Debit", ColMoney}, {"credit", "Credit", ColMoney}, {"balance", "Balance", ColDrCr}},
		Empty: "No transactions in this period."}
	sec.Rows = append(sec.Rows, []string{r.From, "Opening balance", "", "", "", amt(st.Opening)})
	var d, c money.Amount
	for _, l := range st.Lines {
		p := l.Narration
		if l.Memo != "" && l.Memo != l.Narration {
			p = l.Memo + " · " + l.Narration
		}
		db, cr := "", ""
		if l.Debit != 0 {
			db = amt(l.Debit)
		}
		if l.Credit != 0 {
			cr = amt(l.Credit)
		}
		d, c = d+l.Debit, c+l.Credit
		sec.Rows = append(sec.Rows, []string{l.ValueDate, p, l.EntryID, db, cr, amt(l.Balance)})
	}
	sec.Totals = []string{"", "Totals for the period", "", amt(d), amt(c), amt(st.Closing)}
	r.Sections = append(r.Sections, sec)
	r.Summary = []SummaryItem{{"Opening balance", amt(st.Opening), ColDrCr, ""}, {"Debits", amt(d), ColMoney, ""}, {"Credits", amt(c), ColMoney, ""}, {"Closing balance", amt(st.Closing), ColDrCr, ""}}
	r.Notes = append(r.Notes, "Dr = amount payable by you; Cr = amount held by us on your behalf. Please report any discrepancy within 30 days.")
	return nil
}

// ---- holdings ----

type holdLine struct {
	isin, sym        string
	qty, pledged     int64
	cost, ltp, value money.Dec
	haircut          money.Dec
	priced           bool
}

func (o *Ops) holdLines(ctx context.Context, tenant, ucc, asOn string) ([]holdLine, string, error) {
	open, _, lotsAsOf, err := o.equityBook(ctx, tenant, ucc, asOn)
	if err != nil {
		return nil, "", err
	}
	_, look, _ := o.Market(ctx, tenant, asOn)
	dp, _ := o.Holdings(ctx, tenant, ucc)
	pledged := map[string]int64{}
	for _, h := range dp {
		pledged[h.ISIN] = h.Pledged
	}
	g := map[string]*holdLine{}
	var order []string
	for _, l := range open {
		h := g[l.ISIN]
		if h == nil {
			h = &holdLine{isin: l.ISIN, sym: l.Symbol}
			g[l.ISIN] = h
			order = append(order, l.ISIN)
		}
		h.qty += l.Qty
		h.cost = h.cost.Add(l.Price.MulInt(l.Qty))
	}
	sort.Slice(order, func(i, j int) bool { return g[order[i]].sym < g[order[j]].sym })
	out := make([]holdLine, 0, len(order))
	for _, k := range order {
		h := g[k]
		if h.qty <= 0 {
			continue
		}
		h.pledged = min(pledged[h.isin], h.qty)
		if i, ok := look.ByISIN(h.isin); ok {
			h.ltp, h.haircut, h.priced = i.LTP, i.HaircutPct, true
		} else if i, ok := look.Get("NSE", "CM", h.sym+"|EQ||"); ok {
			h.ltp, h.haircut, h.priced = i.LTP, i.HaircutPct, true
		}
		h.value = h.ltp.MulInt(h.qty)
		out = append(out, *h)
	}
	return out, lotsAsOf, nil
}

func (o *Ops) repHoldings(ctx context.Context, tenant string, r *ClientReport) error {
	if r.multi() {
		cs, err := o.Clients(ctx, tenant)
		if err != nil {
			return err
		}
		sec := Section{Title: "Holdings of all clients", Columns: []Column{{"ucc", "UCC", ColText}, {"name", "Client", ColText}, {"isin", "ISIN", ColText}, {"symbol", "Security", ColText}, {"qty", "Quantity", ColQty}, {"pledged", "Pledged", ColQty}, {"avg", "Avg cost", ColMoney}, {"ltp", "Price", ColMoney}, {"value", "Market value", ColMoney}}}
		var tv money.Dec
		n := 0
		for _, c := range cs {
			if c.Status != "Active" || !r.has(c) {
				continue
			}
			hs, _, err := o.holdLines(ctx, tenant, c.UCC, r.To)
			if err != nil {
				return err
			}
			if len(hs) > 0 {
				n++
			}
			for _, h := range hs {
				avg, _ := h.cost.Quo(money.DecInt(h.qty))
				sec.Rows = append(sec.Rows, []string{c.UCC, c.Name, h.isin, h.sym, qty(h.qty), qty(h.pledged), d2s(avg), d2s(h.ltp), d2s(h.value)})
				tv = tv.Add(h.value)
			}
		}
		sec.Totals = []string{"Total", "", "", "", "", "", "", "", d2s(tv)}
		r.Sections = append(r.Sections, sec)
		r.Summary = []SummaryItem{{"Clients with holdings", fmt.Sprint(n), ColQty, ""}, {"Lines", fmt.Sprint(len(sec.Rows)), ColQty, ""}, {"Market value", d2s(tv), ColMoney, ""}}
		return nil
	}
	hs, lotsAsOf, err := o.holdLines(ctx, tenant, r.UCC, r.To)
	if err != nil {
		return err
	}
	sec, tc, tv := holdingSection(hs)
	r.Sections = append(r.Sections, sec)
	pl := tv.Sub(tc)
	r.Summary = []SummaryItem{{"Securities", fmt.Sprint(len(hs)), ColQty, ""}, {"Cost", d2s(tc), ColMoney, ""}, {"Market value", d2s(tv), ColMoney, ""}, {"Unrealised P&L", d2s(pl), ColMoney, tone(pl)}}
	r.Notes = append(r.Notes, holdingNotes(lotsAsOf)...)
	return nil
}

func holdingSection(hs []holdLine) (Section, money.Dec, money.Dec) {
	sec := Section{Title: "Equity holdings", Columns: []Column{{"isin", "ISIN", ColText}, {"symbol", "Security", ColText}, {"qty", "Quantity", ColQty}, {"pledged", "Pledged", ColQty}, {"avg", "Avg cost", ColMoney}, {"cost", "Cost", ColMoney}, {"ltp", "Price", ColMoney}, {"value", "Market value", ColMoney}, {"pl", "Unrealised P&L", ColMoney}, {"plpct", "P&L %", ColPct}, {"weight", "Weight %", ColPct}},
		Empty: "No securities held."}
	var tc, tv money.Dec
	for _, h := range hs {
		tc, tv = tc.Add(h.cost), tv.Add(h.value)
	}
	for _, h := range hs {
		avg, _ := h.cost.Quo(money.DecInt(h.qty))
		pl := h.value.Sub(h.cost)
		ltp, val := d2s(h.ltp), d2s(h.value)
		if !h.priced {
			ltp, val = "", ""
		}
		sec.Rows = append(sec.Rows, []string{h.isin, h.sym, qty(h.qty), qty(h.pledged), d2s(avg), d2s(h.cost), ltp, val, d2s(pl), pctOf(pl, h.cost), pctOf(h.value, tv)})
	}
	if len(hs) > 0 {
		sec.Totals = []string{"Total", "", "", "", "", d2s(tc), "", d2s(tv), d2s(tv.Sub(tc)), pctOf(tv.Sub(tc), tc), "100.00"}
	}
	return sec, tc, tv
}

func holdingNotes(lotsAsOf string) []string {
	n := []string{"Cost is the purchase price excluding brokerage and statutory charges, matched first-in first-out. Prices are the latest closing prices on or before the report date."}
	if lotsAsOf != "" {
		n = append(n, "Cost history is carried from the purchase lots as at "+fmtDay(lotsAsOf)+" plus trades since.")
	}
	return n
}

// ---- derivatives and charges from bills ----

type billTotals struct {
	derivBySeg map[string]money.Dec // EXCH-SEG -> MTM and premium settled
	charges    map[string]money.Dec // head -> amount
	turnover   map[string]int       // EXCH-SEG -> bills
}

var chargeHeads = []struct{ acct, label string }{
	{ledger.IncomeBrokerage, "Brokerage"}, {ledger.PayableSTT, "STT / CTT"}, {"PAYABLE:EXCH_TXN", "Exchange transaction charges"},
	{ledger.PayableSEBIFee, "SEBI turnover fee"}, {ledger.PayableStamp, "Stamp duty"}, {ledger.PayableGST, "GST"},
}

func (o *Ops) bills(ctx context.Context, tenant, ucc, from, to string) (billTotals, error) {
	acct := ledger.ClientAccount(ucc)
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{Account: acct, To: to})
	if err != nil {
		return billTotals{}, err
	}
	byID := map[string]ledger.Entry{}
	for _, e := range es {
		byID[e.ID] = e
	}
	bt := billTotals{derivBySeg: map[string]money.Dec{}, charges: map[string]money.Dec{}, turnover: map[string]int{}}
	for _, e := range es {
		if e.ValueDate < from || e.ValueDate > to {
			continue
		}
		src := e
		if e.ReversalOf != "" {
			src = byID[e.ReversalOf]
		}
		if !derivKinds[src.Source.Kind] {
			continue
		}
		parts := strings.Split(src.Source.ID, "/")
		seg := parts[len(parts)-1]
		if e.ReversalOf == "" && src.Source.Kind == "BILL" {
			bt.turnover[seg]++
		}
		for _, p := range e.Postings {
			net := p.Credit.Dec().Sub(p.Debit.Dec())
			if p.Account == acct && p.Memo == "Trade value / MTM" && !strings.HasSuffix(seg, "-CM") {
				bt.derivBySeg[seg] = bt.derivBySeg[seg].Add(net)
			}
			for _, h := range chargeHeads {
				if p.Account == h.acct || strings.HasPrefix(p.Account, h.acct+":") {
					bt.charges[h.label] = bt.charges[h.label].Add(net)
				}
			}
		}
	}
	return bt, nil
}

func (bt billTotals) chargeTotal() money.Dec {
	var t money.Dec
	for _, v := range bt.charges {
		t = t.Add(v)
	}
	return t
}

func (bt billTotals) derivTotal() money.Dec {
	var t money.Dec
	for _, v := range bt.derivBySeg {
		t = t.Add(v)
	}
	return t
}

func (bt billTotals) chargeSection() Section {
	sec := Section{Title: "Charges paid", Columns: []Column{{"head", "Charge", ColText}, {"amount", "Amount", ColMoney}}}
	for _, h := range chargeHeads {
		sec.Rows = append(sec.Rows, []string{h.label, d2s(bt.charges[h.label])})
	}
	sec.Totals = []string{"Total", d2s(bt.chargeTotal())}
	return sec
}

func (bt billTotals) derivSection() Section {
	sec := Section{Title: "Derivatives: realised P&L", Columns: []Column{{"segment", "Exchange segment", ColText}, {"bills", "Trading days", ColQty}, {"pl", "MTM and premium settled", ColMoney}},
		Empty: "No futures or options settled in this period.",
		Note:  "Futures are marked to market daily; options show premium received less premium paid. Open positions are in the positions section at their current MTM."}
	for _, k := range sortedKeys(bt.derivBySeg) {
		sec.Rows = append(sec.Rows, []string{k, fmt.Sprint(bt.turnover[k]), d2s(bt.derivBySeg[k])})
	}
	if len(sec.Rows) > 0 {
		sec.Totals = []string{"Total", "", d2s(bt.derivTotal())}
	}
	return sec
}

func gainsBy(sales []Sale, from, to string) map[string]money.Dec {
	m := map[string]money.Dec{}
	for _, s := range sales {
		if s.Date >= from && s.Date <= to {
			m[s.Category] = m[s.Category].Add(s.Gain)
		}
	}
	return m
}

// ---- portfolio ----

func (o *Ops) positionsOn(ctx context.Context, tenant, ucc, date string) ([]PositionView, *RiskRow, error) {
	rv, err := o.Risk(ctx, tenant, date)
	if err != nil {
		return nil, nil, err
	}
	var row *RiskRow
	for i := range rv.Rows {
		if rv.Rows[i].UCC == ucc {
			row = &rv.Rows[i]
		}
	}
	return rv.Positions[ucc], row, nil
}

func positionSection(ps []PositionView) (Section, money.Dec) {
	sec := Section{Title: "Open derivatives positions", Columns: []Column{{"contract", "Contract", ColText}, {"seg", "Exchange", ColText}, {"qty", "Net qty", ColQty}, {"avg", "Avg price", ColMoney}, {"ltp", "Last price", ColMoney}, {"mtm", "MTM", ColMoney}, {"margin", "Margin", ColMoney}},
		Empty: "No open futures or options."}
	var mtm, mg money.Dec
	for _, p := range ps {
		sec.Rows = append(sec.Rows, []string{p.Name, p.Exchange + "-" + p.Segment, qty(p.Qty), d2s(p.AvgPrice), d2s(p.LTP), d2s(p.MTM), d2s(p.Margin)})
		mtm, mg = mtm.Add(p.MTM), mg.Add(p.Margin)
	}
	if len(ps) > 0 {
		sec.Totals = []string{"Total", "", "", "", "", d2s(mtm), d2s(mg)}
	}
	return sec, mtm
}

func (o *Ops) repPortfolio(ctx context.Context, tenant string, r *ClientReport) error {
	hs, lotsAsOf, err := o.holdLines(ctx, tenant, r.UCC, r.To)
	if err != nil {
		return err
	}
	_, sales, _, err := o.equityBook(ctx, tenant, r.UCC, r.To)
	if err != nil {
		return err
	}
	bt, err := o.bills(ctx, tenant, r.UCC, r.From, r.To)
	if err != nil {
		return err
	}
	ps, risk, err := o.positionsOn(ctx, tenant, r.UCC, r.To)
	if err != nil {
		return err
	}
	st, err := o.App.Ledger.Statement(ctx, tenant, ledger.ClientAccount(r.UCC), "", r.To)
	if err != nil {
		return err
	}
	hsec, cost, value := holdingSection(hs)
	psec, mtm := positionSection(ps)
	funds := st.Closing.Dec().Neg() // credit-positive
	net := funds.Add(value).Add(mtm)
	g := gainsBy(sales, r.From, r.To)
	realised := g[CatSpec].Add(g[CatSTCG]).Add(g[CatLTCG]).Add(bt.derivTotal())
	r.Summary = []SummaryItem{
		{"Net worth with broker", d2s(net), ColMoney, ""},
		{"Ledger balance (credit +)", d2s(funds), ColMoney, tone(funds)},
		{"Equity at market", d2s(value), ColMoney, ""},
		{"Unrealised equity P&L", d2s(value.Sub(cost)), ColMoney, tone(value.Sub(cost))},
		{"Open derivatives MTM", d2s(mtm), ColMoney, tone(mtm)},
		{"Realised P&L in period", d2s(realised), ColMoney, tone(realised)},
		{"Charges in period", d2s(bt.chargeTotal()), ColMoney, ""},
	}
	if risk != nil {
		r.Summary = append(r.Summary, SummaryItem{"Margin used", d2s(risk.Required), ColMoney, ""}, SummaryItem{"Collateral value (after haircut)", d2s(risk.Collateral), ColMoney, ""})
	}
	alloc := Section{Title: "Allocation", Columns: []Column{{"asset", "Asset", ColText}, {"value", "Value", ColMoney}, {"share", "Share %", ColPct}}}
	gross := money.Max(funds, money.Dec{}).Add(value)
	for _, a := range []struct {
		l string
		v money.Dec
	}{{"Equity holdings", value}, {"Cash with broker", money.Max(funds, money.Dec{})}} {
		alloc.Rows = append(alloc.Rows, []string{a.l, d2s(a.v), pctOf(a.v, gross)})
	}
	rp := Section{Title: "Realised P&L for the period", Columns: []Column{{"cat", "Category", ColText}, {"pl", "P&L", ColMoney}}}
	for _, c := range []string{CatSpec, CatSTCG, CatLTCG} {
		rp.Rows = append(rp.Rows, []string{c, d2s(g[c])})
	}
	rp.Rows = append(rp.Rows, []string{"Derivatives (futures and options)", d2s(bt.derivTotal())})
	if !g[CatUnknown].IsZero() {
		rp.Rows = append(rp.Rows, []string{"Sales without cost history (proceeds)", d2s(g[CatUnknown])})
	}
	rp.Totals = []string{"Total before charges", d2s(realised)}
	r.Sections = append(r.Sections, alloc, hsec, psec, rp, bt.chargeSection())
	r.Notes = append(r.Notes, holdingNotes(lotsAsOf)...)
	r.Notes = append(r.Notes, "Net worth = ledger balance + equity at market + open derivatives MTM. Mutual funds and holdings with other brokers are not included.")
	return nil
}

// ---- P&L / capital gains ----

func (o *Ops) repPnL(ctx context.Context, tenant string, r *ClientReport) error {
	_, sales, lotsAsOf, err := o.equityBook(ctx, tenant, r.UCC, r.To)
	if err != nil {
		return err
	}
	bt, err := o.bills(ctx, tenant, r.UCC, r.From, r.To)
	if err != nil {
		return err
	}
	g := gainsBy(sales, r.From, r.To)
	lotSec := func(title string, cats ...string) Section {
		s := Section{Title: title, Columns: []Column{{"sell", "Sell date", ColDate}, {"symbol", "Security", ColText}, {"isin", "ISIN", ColText}, {"qty", "Qty", ColQty}, {"buy", "Buy date", ColDate}, {"bp", "Buy price", ColMoney}, {"sp", "Sell price", ColMoney}, {"cost", "Cost", ColMoney}, {"proc", "Sale value", ColMoney}, {"gain", "Gain / loss", ColMoney}, {"days", "Days held", ColQty}},
			Empty: "None in this period."}
		var tc, tp, tg money.Dec
		for _, x := range sales {
			if x.Date < r.From || x.Date > r.To || !contains(cats, x.Category) {
				continue
			}
			days := ""
			if x.BuyDate != "" {
				days = fmt.Sprint(x.Days)
			}
			cost, bp := d2s(x.Cost), d2s(x.BuyPrice)
			if x.Category == CatUnknown {
				cost, bp = "", ""
			}
			s.Rows = append(s.Rows, []string{x.Date, x.Symbol, x.ISIN, qty(x.Qty), x.BuyDate, bp, d2s(x.SellPrice), cost, d2s(x.Proceeds), d2s(x.Gain), days})
			tc, tp, tg = tc.Add(x.Cost), tp.Add(x.Proceeds), tg.Add(x.Gain)
		}
		if len(s.Rows) > 0 {
			s.Totals = []string{"Total", "", "", "", "", "", "", d2s(tc), d2s(tp), d2s(tg), ""}
		}
		return s
	}
	spec := lotSec("Speculative business income (intraday equity)", CatSpec)
	spec.Columns[4].Label, spec.Columns[5].Label, spec.Columns[6].Label = "Trade date", "Avg buy", "Avg sell"
	st := lotSec("Short-term capital gains (listed equity held 12 months or less)", CatSTCG)
	lt := lotSec("Long-term capital gains (listed equity held more than 12 months)", CatLTCG)
	r.Sections = append(r.Sections, spec, st, lt, bt.derivSection(), bt.chargeSection())
	if !g[CatUnknown].IsZero() {
		u := lotSec("Sales without purchase history", CatUnknown)
		u.Note = "These sales have no recorded purchase. Load the client's purchase lots (HOLDING-LOTS) to compute their gains."
		r.Sections = append(r.Sections, u)
	}
	r.Summary = []SummaryItem{
		{"Speculative", d2s(g[CatSpec]), ColMoney, tone(g[CatSpec])},
		{"Short-term capital gain", d2s(g[CatSTCG]), ColMoney, tone(g[CatSTCG])},
		{"Long-term capital gain", d2s(g[CatLTCG]), ColMoney, tone(g[CatLTCG])},
		{"Derivatives (non-speculative business)", d2s(bt.derivTotal()), ColMoney, tone(bt.derivTotal())},
		{"Charges", d2s(bt.chargeTotal()), ColMoney, ""},
	}
	r.Notes = append(r.Notes,
		"Gains are computed first-in first-out on purchase price; brokerage and statutory charges are shown separately (STT is not deductible against capital gains). Same-day buy and sell of a scrip is treated as speculative.",
		"Long-term = listed equity held more than 12 months. Grandfathering of gains to 31-Jan-2018 is not applied. This statement supports tax filing and is not tax advice; please confirm with your tax adviser.")
	if lotsAsOf != "" {
		r.Notes = append(r.Notes, "Purchase history is carried from the lots as at "+fmtDay(lotsAsOf)+".")
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// ---- trade book ----

func (o *Ops) repTradebook(ctx context.Context, tenant string, r *ClientReport) error {
	cols := []Column{{"date", "Date", ColDate}, {"time", "Time", ColText}, {"exch", "Exchange", ColText}, {"contract", "Contract", ColText}, {"side", "B/S", ColText}, {"qty", "Qty", ColQty}, {"price", "Price", ColMoney}, {"value", "Value", ColMoney}, {"product", "Product", ColText}, {"order", "Order no.", ColText}, {"trade", "Trade no.", ColText}}
	if r.multi() {
		cols = append([]Column{{"ucc", "UCC", ColText}}, cols...)
	}
	sec := Section{Title: "Trades", Columns: cols, Empty: "No trades in this period."}
	scope := map[string]bool{}
	if r.multi() {
		cs, err := o.Clients(ctx, tenant)
		if err != nil {
			return err
		}
		for _, c := range cs {
			if r.has(c) {
				scope[c.UCC] = true
			}
		}
	}
	var buy, sell money.Dec
	n := 0
	for d := r.From; d <= r.To; d = addDays(d, 1) {
		ts, err := o.exchangeTrades(ctx, tenant, d)
		if err != nil {
			return err
		}
		sort.Slice(ts, func(i, j int) bool {
			if ts[i].UCC != ts[j].UCC {
				return ts[i].UCC < ts[j].UCC
			}
			return ts[i].TradeTime < ts[j].TradeTime
		})
		for _, t := range ts {
			if r.multi() && !scope[t.UCC] || !r.multi() && t.UCC != r.UCC {
				continue
			}
			v := tradeValue(t)
			side := "Buy"
			if t.Side == "B" {
				buy = buy.Add(v)
			} else {
				side, sell = "Sell", sell.Add(v)
			}
			row := []string{t.TradeDate, t.TradeTime, t.Exchange + "-" + t.Segment, InstrumentName(t.Symbol, t.InstrumentType, t.Expiry, t.Strike), side, qty(t.Qty), t.Price.String(), d2s(v), t.Product, t.OrderNo, t.TradeNo}
			if r.multi() {
				row = append([]string{t.UCC}, row...)
			}
			sec.Rows = append(sec.Rows, row)
			n++
		}
	}
	r.Sections = append(r.Sections, sec)
	r.Summary = []SummaryItem{{"Trades", fmt.Sprint(n), ColQty, ""}, {"Bought", d2s(buy), ColMoney, ""}, {"Sold", d2s(sell), ColMoney, ""}}
	r.Notes = append(r.Notes, "Values are price × quantity; for options this is the premium. Exchange trade records, after any approved client code modifications.")
	return nil
}

func tradeValue(t ingest.TradeEvent) money.Dec { return t.Price.MulInt(t.Qty) }

// ---- positions ----

func (o *Ops) repPositions(ctx context.Context, tenant string, r *ClientReport) error {
	ps, risk, err := o.positionsOn(ctx, tenant, r.UCC, r.To)
	if err != nil {
		return err
	}
	sec, mtm := positionSection(ps)
	r.Sections = append(r.Sections, sec)
	r.Summary = []SummaryItem{{"Open contracts", fmt.Sprint(len(ps)), ColQty, ""}, {"MTM", d2s(mtm), ColMoney, tone(mtm)}}
	if risk != nil {
		r.Summary = append(r.Summary, SummaryItem{"Margin required", d2s(risk.Required), ColMoney, ""}, SummaryItem{"Free margin", d2s(risk.Free), ColMoney, tone(risk.Free)})
	}
	return nil
}

// ---- output ----

// CSV renders every section one after another, with raw numbers.
// normalise makes empty lists encode as [] rather than null.
func (r *ClientReport) normalise() {
	if r.Summary == nil {
		r.Summary = []SummaryItem{}
	}
	if r.Sections == nil {
		r.Sections = []Section{}
	}
	for i := range r.Sections {
		if r.Sections[i].Rows == nil {
			r.Sections[i].Rows = [][]string{}
		}
	}
}

func (r ClientReport) CSV() []byte {
	var b bytes.Buffer
	b.WriteString("\ufeff")
	w := csv.NewWriter(&b)
	w.Write([]string{r.Title})
	w.Write([]string{"Client", r.UCC, r.Name})
	if r.PAN != "" {
		w.Write([]string{"PAN", r.PAN})
	}
	w.Write([]string{"Period", r.Period})
	for _, s := range r.Summary {
		w.Write([]string{s.Label, s.Value})
	}
	for _, s := range r.Sections {
		w.Write(nil)
		w.Write([]string{s.Title})
		h := make([]string, len(s.Columns))
		for i, c := range s.Columns {
			h[i] = c.Label
		}
		w.Write(h)
		for _, row := range s.Rows {
			w.Write(row)
		}
		if len(s.Totals) > 0 {
			w.Write(s.Totals)
		}
	}
	w.Flush()
	return b.Bytes()
}

// FileName is a download name such as ledger_BH1001_2026-04-01_2026-10-16.csv.
func (r ClientReport) FileName(ext string) string {
	p := r.To
	if r.From != "" {
		p = r.From + "_" + r.To
	}
	return r.Kind + "_" + strings.ReplaceAll(r.UCC, ":", "-") + "_" + p + "." + ext
}

func cellFmt(typ, v string) string {
	if v == "" {
		return ""
	}
	switch typ {
	case ColMoney:
		d, err := money.ParseDec(v)
		if err != nil {
			return v
		}
		return formatINR(d)
	case ColDrCr:
		d, err := money.ParseDec(v)
		if err != nil {
			return v
		}
		switch d.Sign() {
		case 1:
			return formatINR(d) + " Dr"
		case -1:
			return formatINR(d.Neg()) + " Cr"
		}
		return "0.00"
	case ColDate:
		return fmtDay(v)
	case ColPct:
		return v + "%"
	}
	return v
}

func alignR(typ string) bool {
	return typ == ColMoney || typ == ColQty || typ == ColDrCr || typ == ColPct
}

var clientReportTmpl = template.Must(template.New("cr").Funcs(template.FuncMap{
	"cell": cellFmt, "r": alignR,
	"col": func(cs []Column, i int) Column {
		if i < len(cs) {
			return cs[i]
		}
		return Column{}
	},
	"neg": func(typ, v string) bool { return (typ == ColMoney) && strings.HasPrefix(v, "-") },
}).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>{{.R.Title}} · {{.R.UCC}}</title>
<style>
body{font:12.5px/1.45 -apple-system,system-ui,"Segoe UI",sans-serif;color:#16201B;margin:32px auto;max-width:1060px;padding:0 24px}
h1{font-size:18px;margin:0}h2{font-size:14px;margin:24px 0 6px}
.meta{display:flex;justify-content:space-between;gap:24px;border-bottom:2px solid #16201B;padding-bottom:12px}
.meta div{display:flex;flex-direction:column;gap:2px}.meta .rt{text-align:right}
.who{display:flex;justify-content:space-between;gap:24px;padding:12px 0;border-bottom:1px solid #C9CDC3}
.who div{display:flex;flex-direction:column;gap:2px}.who b{font-size:14px}
.sum{display:grid;grid-template-columns:repeat(auto-fit,minmax(170px,1fr));gap:8px;margin:16px 0 4px}
.sum div{border:1px solid #C9CDC3;border-radius:6px;padding:8px 10px}.sum small{display:block;color:#525D57;font-size:11px}.sum b{font:600 14px ui-monospace,Menlo,monospace}
.good{color:#1D6B43}.bad{color:#A3271D}
table{width:100%;border-collapse:collapse;margin-top:6px}th,td{padding:5px 6px;border-bottom:1px solid #DADDD5;text-align:left;vertical-align:top}
th{font-size:10.5px;text-transform:uppercase;letter-spacing:.04em;background:#EAECE6;white-space:nowrap}
td.r,th.r{text-align:right;white-space:nowrap;font-family:ui-monospace,Menlo,monospace}
tfoot td{font-weight:700;border-top:2px solid #16201B}
.sec-note{font-size:11px;color:#525D57;margin:4px 0 0}.empty{color:#525D57;padding:10px 6px}
.notes{margin-top:24px;font-size:11px;color:#525D57}.notes p{margin:4px 0}
.bar{position:sticky;top:0;background:#16201B;color:#fff;margin:-32px -24px 20px;padding:10px 24px;display:flex;justify-content:space-between;align-items:center;font-size:13px}
@media print{body{margin:10mm;padding:0;max-width:none}.bar{display:none}thead{display:table-header-group}tr{break-inside:avoid}}
</style></head><body>
<div class="bar"><span>{{.R.Title}} · {{.R.UCC}} · {{.R.Period}}</span><span>Press ⌘P (Mac) or Ctrl+P to print or save as PDF</span></div>
<div class="meta"><div><h1>{{.Firm}}</h1><span>SEBI registration {{.SEBIReg}}</span><span>{{.Members}}</span></div>
<div class="rt"><b>{{.R.Title}}</b><span>{{.R.Period}}</span><span>Generated {{.R.Generated}}</span></div></div>
<div class="who"><div><b>{{.R.Name}}</b><span>UCC {{.R.UCC}}{{if .PAN}} · PAN {{.PAN}}{{end}}</span>{{if .R.Address}}<span>{{.R.Address}}</span>{{end}}</div></div>
{{if .R.Summary}}<div class="sum">{{range .R.Summary}}<div><small>{{.Label}}</small><b class="{{.Tone}}">{{cell .Type .Value}}</b></div>{{end}}</div>{{end}}
{{range .R.Sections}}{{$cols := .Columns}}<h2>{{.Title}}</h2>
{{if .Rows}}<table><thead><tr>{{range .Columns}}<th class="{{if r .Type}}r{{end}}">{{.Label}}</th>{{end}}</tr></thead><tbody>
{{range .Rows}}<tr>{{range $i, $v := .}}{{$c := col $cols $i}}<td class="{{if r $c.Type}}r{{end}}{{if neg $c.Type $v}} bad{{end}}">{{cell $c.Type $v}}</td>{{end}}</tr>
{{end}}</tbody>{{if .Totals}}<tfoot><tr>{{range $i, $v := .Totals}}{{$c := col $cols $i}}<td class="{{if r $c.Type}}r{{end}}">{{if eq $i 0}}{{$v}}{{else}}{{cell $c.Type $v}}{{end}}</td>{{end}}</tr></tfoot>{{end}}</table>
{{else}}<div class="empty">{{if .Empty}}{{.Empty}}{{else}}Nothing to show.{{end}}</div>{{end}}
{{if .Note}}<p class="sec-note">{{.Note}}</p>{{end}}{{end}}
<div class="notes">{{range .R.Notes}}<p>{{.}}</p>{{end}}{{if .Sample}}<p>Charges use the sample rate schedule. Not valid for real clients.</p>{{end}}<p>This is a computer-generated statement.</p></div>
</body></html>`))

// RenderClientReport returns a printable HTML page.
func (o *Ops) RenderClientReport(ctx context.Context, tenant string, r ClientReport) ([]byte, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return nil, err
	}
	var members []string
	for _, k := range sortedKeys(set.MemberCodes) {
		members = append(members, k+" "+set.MemberCodes[k])
	}
	var b bytes.Buffer
	err = clientReportTmpl.Execute(&b, map[string]any{"R": r, "Firm": set.Firm, "SEBIReg": set.SEBIReg, "Members": "Member codes " + strings.Join(members, " · "),
		"PAN": r.PAN, "Sample": o.App.Schedule.Sample})
	return b.Bytes(), err
}

// multi reports whether the report covers many clients: all, or a group.
func (r ClientReport) multi() bool { return r.UCC == "ALL" || strings.HasPrefix(r.UCC, "TAG:") }

// has reports whether a client is in the report's scope.
func (r ClientReport) has(c Client) bool {
	t, ok := strings.CutPrefix(r.UCC, "TAG:")
	if !ok {
		return r.UCC == "ALL" || c.UCC == r.UCC
	}
	for _, x := range c.Tags {
		if x == t {
			return true
		}
	}
	return false
}
