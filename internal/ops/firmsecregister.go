package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- the securities register ----
//
// Every movement of the firm's own securities is a register line
// (FSTxn): a day's purchases or sales of a scrip under one of the firm's
// trading codes (from the exchange trade files), a manual or off-market
// entry, an opening lot from the old system, a stock journal leg or a
// buy-back. The lots are never stored: they are worked out by replaying
// the lines in date order (fsReplay), first in first out and perpetual
// (questionnaire i1, t3), with the corporate actions engine's bonus,
// split, merger and rights events on their ex-date (i6). Lines are
// entered in date order for a scrip, so a later line never changes the
// cost an earlier sale was posted at.
//
// Each posted line has its own journal entry. When a line changes (its
// portfolio before the day's close, a rebill), the day's entries for
// the scrip are worked out again and an entry that differs is reversed
// and posted afresh.

const (
	cFSTxn = "fstxn"

	FSTypeTrade     = "Trade file"
	FSTypeManual    = "Manual"
	FSTypeOffMarket = "Off-market"
	FSTypeOpening   = "Opening"
	FSTypeJournal   = "Stock journal"
	FSTypeBuyBack   = "Buy-back"
	FSTypeCorp      = "Corporate action"

	FSPending   = "Pending"
	FSPosted    = "Posted"
	FSRejected  = "Rejected"
	FSCancelled = "Cancelled"
	// FSLoaded is an opening lot loaded but not yet posted to the books
	// (it counts in the holdings from the start).
	FSLoaded = "Loaded"

	TermLong    = "LT"
	TermCurrent = "CUR"
)

// FSCharges are the charges on a line, head by head.
type FSCharges struct {
	Brokerage money.Amount `json:"brokerage"`
	STT       money.Amount `json:"stt"`
	Exchange  money.Amount `json:"exchange"` // exchange transaction charges and SEBI turnover fee
	Stamp     money.Amount `json:"stamp"`
	GST       money.Amount `json:"gst"`
	Other     money.Amount `json:"other"`
}

// Total is every charge.
func (c FSCharges) Total() money.Amount {
	return c.Brokerage + c.STT + c.Exchange + c.Stamp + c.GST + c.Other
}

func (c FSCharges) get(h string) money.Amount {
	switch h {
	case "brokerage":
		return c.Brokerage
	case "stt":
		return c.STT
	case "exchange":
		return c.Exchange
	case "stamp":
		return c.Stamp
	case "gst":
		return c.GST
	}
	return c.Other
}

func (c *FSCharges) set(h string, a money.Amount) {
	switch h {
	case "brokerage":
		c.Brokerage = a
	case "stt":
		c.STT = a
	case "exchange":
		c.Exchange = a
	case "stamp":
		c.Stamp = a
	case "gst":
		c.GST = a
	default:
		c.Other = a
	}
}

func (c FSCharges) add(d FSCharges) FSCharges {
	for _, h := range fsChargeHeads {
		c.set(h.ID, c.get(h.ID)+d.get(h.ID))
	}
	return c
}

// share is the part of an amount for q of n units, to the paisa.
func share(a money.Amount, q, n money.Dec) money.Amount {
	if n.Sign() == 0 || q.Cmp(n) >= 0 {
		return a
	}
	x, err := a.Dec().Mul(q).Quo(n)
	if err != nil {
		return 0
	}
	return x.Round(money.UnitPaisa)
}

func (c FSCharges) part(q, n money.Dec) FSCharges {
	var o FSCharges
	for _, h := range fsChargeHeads {
		o.set(h.ID, share(c.get(h.ID), q, n))
	}
	return o
}

func (c FSCharges) sub(d FSCharges) FSCharges {
	for _, h := range fsChargeHeads {
		c.set(h.ID, c.get(h.ID)-d.get(h.ID))
	}
	return c
}

// included are the charge heads that go into the cost of a purchase in a
// portfolio (i2 for investments, t5 for stock-in-trade). STT is never
// part of cost: it is an expense of its own.
func (s FSSettings) included(port string) map[string]bool {
	all := map[string]bool{"brokerage": true, "exchange": true, "stamp": true, "gst": true, "other": true}
	if port == PortSIT {
		if s.SITCharges == "cost" {
			return all
		}
		return map[string]bool{}
	}
	switch s.InvCost {
	case "price":
		return map[string]bool{}
	case "full-itc":
		delete(all, "gst")
	}
	return all
}

// FSTxn is one line of the securities register.
type FSTxn struct {
	ID string `json:"id"`
	// Date is the trade (or entry) date; for an opening lot the as-at
	// date, with the acquisition date in AcqDate.
	Date    string `json:"date"`
	AcqDate string `json:"acq_date,omitempty"`
	// PayDate is when the securities reach the demat (a purchase) or
	// leave it (a sale): the pay-out or pay-in date.
	PayDate   string `json:"pay_date,omitempty"`
	Portfolio string `json:"portfolio"`
	// DefaultPortfolio is what the trading code gave.
	DefaultPortfolio string       `json:"default_portfolio,omitempty"`
	Kind             string       `json:"kind"`
	Side             string       `json:"side"` // B or S
	Type             string       `json:"type"`
	ISIN             string       `json:"isin"`
	Symbol           string       `json:"symbol"`
	Exchange         string       `json:"exchange,omitempty"`
	Code             string       `json:"code,omitempty"` // trading code
	Trades           int          `json:"trades,omitempty"`
	Qty              money.Dec    `json:"qty"`
	Value            money.Amount `json:"value"` // quantity × price
	Charges          FSCharges    `json:"charges"`
	TDS              money.Amount `json:"tds,omitempty"` // a buy-back: tax deducted on the deemed dividend
	// Counter is the account the money goes through: the trading code's
	// ledger for trade-file lines, a bank or payable for manual ones.
	Counter string `json:"counter,omitempty"`
	// Term marks an investment lot long-term (LT) or current (CUR) (i4).
	Term string `json:"term,omitempty"`
	// Tax facts of a stock journal's in-leg: the date and value the lot
	// is taken at for tax (sections 45(2) and 28(via)).
	TaxDate  string       `json:"tax_date,omitempty"`
	TaxValue money.Amount `json:"tax_value,omitempty"`
	TaxNote  string       `json:"tax_note,omitempty"`
	// TaxCost and TaxAcq carry an opening lot's tax cost and acquisition
	// when they differ from the books (blank: the same).
	Ref      string   `json:"ref,omitempty"`
	Note     string   `json:"note,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Status   string   `json:"status"`
	Maker    string   `json:"maker"`
	Checker  string   `json:"checker,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Entry    string   `json:"entry,omitempty"`
	Rev      int      `json:"rev,omitempty"`
	Hash     string   `json:"hash,omitempty"`
	Created  string   `json:"created"`
	// Filled in when listed.
	PortfolioName string `json:"portfolio_name,omitempty"`
	KindName      string `json:"kind_name,omitempty"`
	Changeable    bool   `json:"changeable,omitempty"`
	CostOut       string `json:"cost_out,omitempty"`
	Gain          string `json:"gain,omitempty"`
	SpecQty       string `json:"spec_qty,omitempty"`
}

func (t FSTxn) live() bool { return t.Status == FSPosted || t.Status == FSLoaded }

// acq is the lot's acquisition date.
func (t FSTxn) acq() string { return firstOf(t.AcqDate, t.Date) }

// FSCostHeads are a lot's cost head by head (the columns a report can show).
type FSCostHeads struct {
	Price     money.Amount `json:"price"`
	Brokerage money.Amount `json:"brokerage"`
	Exchange  money.Amount `json:"exchange"`
	Stamp     money.Amount `json:"stamp"`
	GST       money.Amount `json:"gst"`
	Other     money.Amount `json:"other"`
}

func (h FSCostHeads) total() money.Amount {
	return h.Price + h.Brokerage + h.Exchange + h.Stamp + h.GST + h.Other
}

func (h FSCostHeads) add(d FSCostHeads) FSCostHeads {
	return FSCostHeads{h.Price + d.Price, h.Brokerage + d.Brokerage, h.Exchange + d.Exchange, h.Stamp + d.Stamp, h.GST + d.GST, h.Other + d.Other}
}

func (h FSCostHeads) sub(d FSCostHeads) FSCostHeads {
	return FSCostHeads{h.Price - d.Price, h.Brokerage - d.Brokerage, h.Exchange - d.Exchange, h.Stamp - d.Stamp, h.GST - d.GST, h.Other - d.Other}
}

func (h FSCostHeads) part(q, n money.Dec) FSCostHeads {
	return FSCostHeads{share(h.Price, q, n), share(h.Brokerage, q, n), share(h.Exchange, q, n), share(h.Stamp, q, n), share(h.GST, q, n), share(h.Other, q, n)}
}

// costHeads is a purchase line's cost: its value and the included charges.
func (s FSSettings) costHeads(t FSTxn) FSCostHeads {
	inc := s.included(t.Portfolio)
	h := FSCostHeads{Price: t.Value}
	if t.Type == FSTypeOpening || t.Type == FSTypeJournal {
		return h
	}
	if inc["brokerage"] {
		h.Brokerage = t.Charges.Brokerage
	}
	if inc["exchange"] {
		h.Exchange = t.Charges.Exchange
	}
	if inc["stamp"] {
		h.Stamp = t.Charges.Stamp
	}
	if inc["gst"] {
		h.GST = t.Charges.GST
	}
	if inc["other"] {
		h.Other = t.Charges.Other
	}
	return h
}

// FSLot is an open purchase lot.
type FSLot struct {
	ID        string       `json:"id"`
	Txn       string       `json:"txn"`
	Portfolio string       `json:"portfolio"`
	Kind      string       `json:"kind"`
	ISIN      string       `json:"isin"`
	Symbol    string       `json:"symbol"`
	Date      string       `json:"date"` // acquisition (for books)
	Qty       money.Dec    `json:"qty"`
	Cost      money.Amount `json:"cost"`
	Heads     FSCostHeads  `json:"heads"`
	Term      string       `json:"term,omitempty"`
	Source    string       `json:"source"`
	// TaxCost and TaxDate are the cost and acquisition date for tax when
	// they differ from the books (a stock journal's in-leg); Converted
	// marks a lot converted from investments into stock-in-trade
	// (section 45(2)): its capital gain to the conversion date is taxed
	// when it is sold.
	TaxCost   money.Amount `json:"tax_cost,omitempty"`
	TaxDate   string       `json:"tax_date,omitempty"`
	Converted *FSConverted `json:"converted,omitempty"`
}

// FSConverted is the capital gains side of an investment converted into
// stock-in-trade (section 45(2) of the Income-tax Act, 1961): the gain to
// the market value on the conversion date, taxed in the year the stock
// is sold.
type FSConverted struct {
	On       string       `json:"on"`
	FMV      money.Amount `json:"fmv"`
	Cost     money.Amount `json:"cost"`
	Acquired string       `json:"acquired"`
	Kind     string       `json:"kind"`
}

// take removes q units from the lot and returns them as a piece with its
// share of the cost (the lot keeps the rest, so totals stay exact).
func (l *FSLot) take(q money.Dec) FSLot {
	p := *l
	p.Qty = q
	p.Heads = l.Heads.part(q, l.Qty)
	p.Cost = p.Heads.total()
	if l.TaxCost != 0 {
		p.TaxCost = share(l.TaxCost, q, l.Qty)
		l.TaxCost -= p.TaxCost
	}
	if l.Converted != nil {
		c := *l.Converted
		c.FMV, c.Cost = share(l.Converted.FMV, q, l.Qty), share(l.Converted.Cost, q, l.Qty)
		nc := *l.Converted
		nc.FMV, nc.Cost = nc.FMV-c.FMV, nc.Cost-c.Cost
		l.Converted, p.Converted = &nc, &c
	}
	l.Heads = l.Heads.sub(p.Heads)
	l.Cost = l.Heads.total()
	l.Qty = l.Qty.Sub(q)
	return p
}

// FSMatch is a sale (or stock journal out-leg, or buy-back) matched to a
// lot, or a same-day purchase for an intraday sale.
type FSMatch struct {
	Txn       string       `json:"txn"`
	Type      string       `json:"type"` // Sale, Intraday, Stock journal, Buy-back
	Portfolio string       `json:"portfolio"`
	Kind      string       `json:"kind"`
	ISIN      string       `json:"isin"`
	Symbol    string       `json:"symbol"`
	Date      string       `json:"date"`
	Lot       string       `json:"lot,omitempty"`
	BuyDate   string       `json:"buy_date,omitempty"`
	Qty       money.Dec    `json:"qty"`
	Cost      money.Amount `json:"cost"`
	Heads     FSCostHeads  `json:"heads"`
	Proceeds  money.Amount `json:"proceeds"`
	Charges   FSCharges    `json:"charges"` // the sale's charges for this part
	Spec      bool         `json:"spec,omitempty"`
	Term      string       `json:"term,omitempty"`
	NoLot     bool         `json:"no_lot,omitempty"` // sold more than held
	TaxCost   money.Amount `json:"tax_cost,omitempty"`
	TaxDate   string       `json:"tax_date,omitempty"`
	Converted *FSConverted `json:"converted,omitempty"`
}

// Gain is the sale value less the cost (charges on the sale apart).
func (m FSMatch) Gain() money.Amount { return m.Proceeds - m.Cost }

// FSEvent is a corporate action applied to the firm's lots.
type FSEvent struct {
	Date      string    `json:"date"`
	Portfolio string    `json:"portfolio"`
	ISIN      string    `json:"isin"`
	Symbol    string    `json:"symbol"`
	Type      string    `json:"type"`
	Ref       string    `json:"ref"`
	Before    money.Dec `json:"before"`
	After     money.Dec `json:"after"`
	Detail    string    `json:"detail"`
}

// fsResult is what the replay worked out for one line.
type fsResult struct {
	CostIn    money.Amount
	HeadsIn   FSCostHeads
	SpecQty   money.Dec
	SpecValue money.Amount
	SpecCost  money.Amount // the purchase cost of the intraday part
	CostOut   money.Amount // cost of the lots a sale took
	TaxCost   money.Amount
	Matches   []int
}

// fsBook is the register replayed to a date.
type fsBook struct {
	Lots    map[string][]FSLot // portfolio|ISIN -> open lots, oldest first
	Matches []FSMatch
	Events  []FSEvent
	Res     map[string]*fsResult
	To      string
}

func fsKey(port, isin string) string { return port + "|" + isin }

// lotsOf lists every open lot, by portfolio, symbol and date.
func (b fsBook) lotsOf(port string) []FSLot {
	var out []FSLot
	for _, k := range sortedKeys(b.Lots) {
		for _, l := range b.Lots[k] {
			if (port == "" || l.Portfolio == port) && l.Qty.Sign() > 0 {
				out = append(out, l)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Portfolio != out[j].Portfolio {
			return out[i].Portfolio < out[j].Portfolio
		}
		if out[i].Symbol != out[j].Symbol {
			return out[i].Symbol < out[j].Symbol
		}
		return out[i].Date < out[j].Date
	})
	return out
}

func (b fsBook) held(port, isin string) money.Dec {
	var q money.Dec
	for _, l := range b.Lots[fsKey(port, isin)] {
		q = q.Add(l.Qty)
	}
	return q
}

// fsReplay works out the lots and matches from the live lines, to and
// including date to (all when to is empty).
func fsReplay(s FSSettings, txns []FSTxn, cas []CorpAction, to string) fsBook {
	b := fsBook{Lots: map[string][]FSLot{}, Res: map[string]*fsResult{}, To: to}
	var live []FSTxn
	for _, t := range txns {
		if t.live() && (to == "" || t.Date <= to) {
			live = append(live, t)
		}
	}
	sort.SliceStable(live, func(i, j int) bool {
		if live[i].Date != live[j].Date {
			return live[i].Date < live[j].Date
		}
		return live[i].ID < live[j].ID
	})
	var corp []CorpAction
	for _, c := range cas {
		if c.Type != "Dividend" && c.ExDate != "" && (to == "" || c.ExDate <= to) {
			corp = append(corp, c)
		}
	}
	sort.SliceStable(corp, func(i, j int) bool { return corp[i].ExDate < corp[j].ExDate })
	ci := 0
	corpTo := func(d string, incl bool) {
		for ci < len(corp) && (corp[ci].ExDate < d || incl && corp[ci].ExDate == d) {
			b.applyCorp(corp[ci])
			ci++
		}
	}
	for i := 0; i < len(live); {
		d := live[i].Date
		j := i
		for j < len(live) && live[j].Date == d {
			j++
		}
		corpTo(d, true) // an action takes effect on its ex-date, before that day's trades
		day := live[i:j]
		groups := map[string][]FSTxn{}
		var order []string
		for _, t := range day {
			k := fsKey(t.Portfolio, t.ISIN)
			if _, ok := groups[k]; !ok {
				order = append(order, k)
			}
			groups[k] = append(groups[k], t)
		}
		for _, k := range order {
			b.replayGroup(s, d, groups[k])
		}
		// A stock journal's in-leg from investments carries the section
		// 45(2) facts of the lots its out-leg took.
		for _, t := range day {
			if t.Type == FSTypeJournal && t.Side == "B" && t.Portfolio == PortSIT {
				b.markConverted(t)
			}
		}
		i = j
	}
	if to != "" {
		corpTo(to, true)
	} else {
		corpTo("9999-12-31", true)
	}
	return b
}

// replayGroup applies one day's lines of a scrip in a portfolio: the
// intraday part first (bought and sold the same day: speculative, t2),
// then the rest of the purchases become lots and the rest of the sales
// take lots first in, first out.
func (b *fsBook) replayGroup(s FSSettings, d string, ts []FSTxn) {
	tradeLike := func(t FSTxn) bool {
		return t.Type == FSTypeTrade || t.Type == FSTypeManual || t.Type == FSTypeOffMarket
	}
	type piece struct {
		t    FSTxn
		left money.Dec
	}
	var buys, sells []*piece
	for _, t := range ts {
		r := &fsResult{}
		b.Res[t.ID] = r
		if t.Side == "B" {
			r.HeadsIn = s.costHeads(t)
			r.CostIn = r.HeadsIn.total()
			buys = append(buys, &piece{t, t.Qty})
		} else {
			sells = append(sells, &piece{t, t.Qty})
		}
	}
	key := fsKey(ts[0].Portfolio, ts[0].ISIN)
	// Intraday: same-day purchases and sales of trade-like lines, the
	// earliest lines first.
	if s.Spec {
		bi, si := 0, 0
		for bi < len(buys) && si < len(sells) {
			bp, sp := buys[bi], sells[si]
			if !tradeLike(bp.t) || bp.left.Sign() == 0 {
				bi++
				continue
			}
			if !tradeLike(sp.t) || sp.left.Sign() == 0 {
				si++
				continue
			}
			q := money.Min(bp.left, sp.left)
			heads := b.Res[bp.t.ID].HeadsIn.part(q, bp.t.Qty)
			if bp.left.Sub(q).Sign() == 0 {
				// the last piece of the purchase takes what is left of its cost
				heads = b.Res[bp.t.ID].HeadsIn.sub(b.specHeadsTaken(bp.t.ID))
			}
			m := FSMatch{Txn: sp.t.ID, Type: "Intraday", Portfolio: sp.t.Portfolio, Kind: sp.t.Kind, ISIN: sp.t.ISIN, Symbol: sp.t.Symbol, Date: d, Lot: bp.t.ID, BuyDate: d,
				Qty: q, Heads: heads, Cost: heads.total(), Proceeds: share(sp.t.Value, q, sp.t.Qty), Charges: sp.t.Charges.part(q, sp.t.Qty), Spec: true}
			b.Matches = append(b.Matches, m)
			rb, rs := b.Res[bp.t.ID], b.Res[sp.t.ID]
			rb.SpecQty, rb.SpecCost = rb.SpecQty.Add(q), rb.SpecCost+m.Cost
			rs.SpecQty, rs.SpecValue = rs.SpecQty.Add(q), rs.SpecValue+m.Proceeds
			rs.CostOut += m.Cost
			rs.Matches = append(rs.Matches, len(b.Matches)-1)
			bp.left, sp.left = bp.left.Sub(q), sp.left.Sub(q)
		}
		for _, bp := range buys {
			r := b.Res[bp.t.ID]
			if r.SpecQty.Sign() > 0 {
				r.SpecValue = share(bp.t.Value, r.SpecQty, bp.t.Qty)
			}
		}
	}
	// The rest of the purchases become lots.
	for _, bp := range buys {
		if bp.left.Sign() <= 0 {
			continue
		}
		r := b.Res[bp.t.ID]
		heads := r.HeadsIn.sub(b.specHeadsTaken(bp.t.ID))
		l := FSLot{ID: bp.t.ID, Txn: bp.t.ID, Portfolio: bp.t.Portfolio, Kind: bp.t.Kind, ISIN: bp.t.ISIN, Symbol: bp.t.Symbol, Date: bp.t.acq(), Qty: bp.left,
			Heads: heads, Cost: heads.total(), Term: bp.t.Term, Source: bp.t.Type}
		if bp.t.TaxDate != "" {
			l.TaxDate, l.TaxCost = bp.t.TaxDate, bp.t.TaxValue
		}
		b.Lots[key] = insertLot(b.Lots[key], l)
	}
	// The rest of the sales take lots first in, first out.
	for _, sp := range sells {
		if sp.left.Sign() <= 0 {
			continue
		}
		r := b.Res[sp.t.ID]
		typ := "Sale"
		switch sp.t.Type {
		case FSTypeJournal:
			typ = FSTypeJournal
		case FSTypeBuyBack:
			typ = FSTypeBuyBack
		}
		lots := b.Lots[key]
		for i := 0; i < len(lots) && sp.left.Sign() > 0; i++ {
			if lots[i].Qty.Sign() == 0 {
				continue
			}
			q := money.Min(lots[i].Qty, sp.left)
			p := lots[i].take(q)
			proc := share(sp.t.Value, q, sp.t.Qty)
			if sp.left.Sub(q).Sign() == 0 {
				proc = sp.t.Value - r.SpecValue - b.procTaken(sp.t.ID)
			}
			m := FSMatch{Txn: sp.t.ID, Type: typ, Portfolio: sp.t.Portfolio, Kind: p.Kind, ISIN: sp.t.ISIN, Symbol: sp.t.Symbol, Date: d, Lot: p.ID, BuyDate: p.Date,
				Qty: q, Heads: p.Heads, Cost: p.Cost, Proceeds: proc, Charges: sp.t.Charges.part(q, sp.t.Qty), Term: p.Term, TaxCost: p.TaxCost, TaxDate: p.TaxDate, Converted: p.Converted}
			b.Matches = append(b.Matches, m)
			r.CostOut += p.Cost
			r.TaxCost += firstAmt(p.TaxCost, p.Cost)
			r.Matches = append(r.Matches, len(b.Matches)-1)
			sp.left = sp.left.Sub(q)
		}
		if sp.left.Sign() > 0 {
			proc := sp.t.Value - r.SpecValue - b.procTaken(sp.t.ID)
			m := FSMatch{Txn: sp.t.ID, Type: typ, Portfolio: sp.t.Portfolio, Kind: sp.t.Kind, ISIN: sp.t.ISIN, Symbol: sp.t.Symbol, Date: d, Qty: sp.left, Proceeds: proc,
				Charges: sp.t.Charges.part(sp.left, sp.t.Qty), NoLot: true}
			b.Matches = append(b.Matches, m)
			r.Matches = append(r.Matches, len(b.Matches)-1)
		}
		b.Lots[key] = dropEmptyFS(lots)
	}
}

func firstAmt(a, b money.Amount) money.Amount {
	if a != 0 {
		return a
	}
	return b
}

// specHeadsTaken is the cost of a purchase's intraday part so far.
func (b *fsBook) specHeadsTaken(txn string) FSCostHeads {
	var h FSCostHeads
	for _, m := range b.Matches {
		if m.Spec && m.Lot == txn {
			h = h.add(m.Heads)
		}
	}
	return h
}

// procTaken is the sale value of a sale's lot matches so far.
func (b *fsBook) procTaken(txn string) money.Amount {
	var a money.Amount
	for _, m := range b.Matches {
		if m.Txn == txn && !m.Spec {
			a += m.Proceeds
		}
	}
	return a
}

// markConverted records the capital gains side of a journal's in-leg
// into stock-in-trade from the out-leg's lots (same journal, Ref).
func (b *fsBook) markConverted(in FSTxn) {
	var cost money.Amount
	acq := ""
	kind := ""
	for _, m := range b.Matches {
		if m.Type == FSTypeJournal && m.Portfolio == PortInv && m.Txn == strings.TrimSuffix(in.ID, "-IN")+"-OUT" {
			cost += m.Cost
			if acq == "" || m.BuyDate < acq {
				acq = m.BuyDate
			}
			kind = m.Kind
		}
	}
	if acq == "" {
		return
	}
	key := fsKey(in.Portfolio, in.ISIN)
	for i := range b.Lots[key] {
		if b.Lots[key][i].Txn == in.ID {
			b.Lots[key][i].Converted = &FSConverted{On: in.Date, FMV: in.Value, Cost: cost, Acquired: acq, Kind: kind}
		}
	}
}

// insertLot keeps lots oldest first (by acquisition date, then line).
func insertLot(ls []FSLot, l FSLot) []FSLot {
	i := sort.Search(len(ls), func(i int) bool {
		return ls[i].Date > l.Date || ls[i].Date == l.Date && ls[i].ID > l.ID
	})
	ls = append(ls, FSLot{})
	copy(ls[i+1:], ls[i:])
	ls[i] = l
	return ls
}

func dropEmptyFS(ls []FSLot) []FSLot {
	out := ls[:0]
	for _, l := range ls {
		if l.Qty.Sign() > 0 {
			out = append(out, l)
		}
	}
	return out
}

// applyCorp applies a corporate action to the firm's lots, as the engine
// does for clients' purchase lots (corporate.go): a bonus adds a lot at
// nil cost acquired on the allotment date; a split multiplies the
// quantity and keeps the cost; a merger or symbol change moves the lots
// to the new ISIN with their cost and dates; rights add entitlements at
// nil cost.
func (b *fsBook) applyCorp(c CorpAction) {
	for _, port := range []string{PortInv, PortSIT} {
		key := fsKey(port, c.ISIN)
		lots := b.Lots[key]
		var held money.Dec
		for _, l := range lots {
			held = held.Add(l.Qty)
		}
		if held.Sign() == 0 {
			continue
		}
		ev := FSEvent{Date: c.ExDate, Portfolio: port, ISIN: c.ISIN, Symbol: firstOf(lots[0].Symbol, c.Symbol), Type: c.Type, Ref: c.ID, Before: held}
		switch c.Type {
		case "Bonus":
			n := ratioQty(held, c.RatioNew, c.RatioOld)
			if n.Sign() > 0 {
				l := FSLot{ID: c.ID + "/" + port, Txn: c.ID, Portfolio: port, Kind: lots[0].Kind, ISIN: c.ISIN, Symbol: ev.Symbol, Date: c.allotDate(), Qty: n, Term: lots[0].Term, Source: FSTypeCorp}
				b.Lots[key] = insertLot(lots, l)
			}
			ev.After, ev.Detail = held.Add(n), fmt.Sprintf("Bonus %d:%d: %s new shares at nil cost, acquired %s", c.RatioNew, c.RatioOld, n, c.allotDate())
		case "Split":
			f, err := c.factor()
			if err != nil {
				continue
			}
			var after money.Dec
			for i := range lots {
				lots[i].Qty = splitQty(lots[i].Qty, f)
				after = after.Add(lots[i].Qty)
			}
			ev.After, ev.Detail = after, fmt.Sprintf("Split from face value %d to %d: cost and dates unchanged", c.RatioOld, c.RatioNew)
		case "Rights":
			n := ratioQty(held, c.RatioNew, c.RatioOld)
			if n.Sign() > 0 {
				rk := fsKey(port, c.NewISIN)
				b.Lots[rk] = insertLot(b.Lots[rk], FSLot{ID: c.ID + "/" + port, Txn: c.ID, Portfolio: port, Kind: lots[0].Kind, ISIN: c.NewISIN, Symbol: c.NewSymbol, Date: c.RecordDate, Qty: n, Term: lots[0].Term, Source: FSTypeCorp})
			}
			ev.After, ev.Detail = held, fmt.Sprintf("Rights entitlement %d:%d: %s of %s at nil cost", c.RatioNew, c.RatioOld, n, c.NewSymbol)
		case "Merger", "Symbol change":
			total := held
			if c.Type == "Merger" {
				total = ratioQty(held, c.RatioNew, c.RatioOld)
			}
			nk := fsKey(port, c.NewISIN)
			given := money.Dec{}
			for i := range lots {
				l := lots[i]
				q := l.Qty
				if c.Type == "Merger" {
					q = ratioQty(l.Qty, c.RatioNew, c.RatioOld)
				}
				if i == len(lots)-1 {
					q = total.Sub(given)
				}
				given = given.Add(q)
				l.ISIN, l.Symbol, l.Qty = c.NewISIN, c.NewSymbol, q
				b.Lots[nk] = insertLot(b.Lots[nk], l)
			}
			delete(b.Lots, key)
			ev.After, ev.Detail = total, fmt.Sprintf("%s into %s (%s): cost and dates carry over", c.Type, c.NewSymbol, c.NewISIN)
		default:
			continue
		}
		b.Events = append(b.Events, ev)
	}
}

// ---- loading, saving ----

func (o *Ops) fsTxns(ctx context.Context, tenant string) []FSTxn {
	ts, _ := docs.List[FSTxn](ctx, o.Docs, tenant, cFSTxn, "")
	return ts
}

func (o *Ops) fsCorpActions(ctx context.Context, tenant string) []CorpAction {
	cs, _ := docs.List[CorpAction](ctx, o.Docs, tenant, cCorpAction, "")
	return cs
}

// fsBookTo replays the register to a date.
func (o *Ops) fsBookTo(ctx context.Context, tenant, to string) (FSSettings, []FSTxn, fsBook) {
	s := o.FirmSecSettings(ctx, tenant)
	ts := o.fsTxns(ctx, tenant)
	return s, ts, fsReplay(s, ts, o.fsCorpActions(ctx, tenant), to)
}

func (o *Ops) fsNewID(ctx context.Context, tenant, actor, prefix string) (string, error) {
	n, err := o.next(ctx, tenant, "fstxn", actor)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%06d", prefix, n), nil
}

// fsDayClosed says whether the equity part of a day is closed (the
// portfolio of a trade-file line is changeable until then, s3).
func (o *Ops) fsDayClosed(ctx context.Context, tenant, date string) bool {
	st, _, err := docs.GetOr(ctx, o.Docs, tenant, cEOD, date, EODState{Date: date})
	if err != nil {
		return false
	}
	if st.Closed {
		return true
	}
	p := st.part(PartEquity)
	return p != nil && p.Closed
}

// ---- postings ----

// fsPostings is the journal entry of a line, from the replay ("" lines
// that post nothing of their own: stock journal legs, opening lots not
// yet posted).
func (s FSSettings) fsPostings(t FSTxn, r *fsResult, b fsBook) []ledger.Posting {
	if r == nil || t.Type == FSTypeJournal || t.Status == FSLoaded {
		return nil
	}
	var ps []ledger.Posting
	dr := func(acct string, a money.Amount, memo string) {
		if a > 0 {
			ps = append(ps, ledger.Posting{Account: acct, Debit: a, Memo: memo})
		} else if a < 0 {
			ps = append(ps, ledger.Posting{Account: acct, Credit: -a, Memo: memo})
		}
	}
	cr := func(acct string, a money.Amount, memo string) { dr(acct, -a, memo) }
	counter := firstOf(t.Counter, ledger.OpeningEquity)
	if t.Type == FSTypeOpening {
		acct := fsInvAcct(t.Kind)
		if t.Portfolio == PortSIT {
			acct = fsSITAcct(t.Kind)
		}
		dr(acct, r.CostIn, "Opening lot")
		cr(counter, r.CostIn, "Opening lot")
		return ps
	}
	inc := s.included(t.Portfolio)
	expCharges := func(c FSCharges, onlyExcluded bool) {
		for _, h := range fsChargeHeads {
			if onlyExcluded && inc[h.ID] {
				continue
			}
			dr(h.Acct, c.get(h.ID), h.Name)
		}
	}
	if t.Type == FSTypeBuyBack {
		// A buy-back on or after 1 October 2024: the whole amount is a
		// deemed dividend (section 2(22)(f)), taxed as dividend, with TDS
		// under section 194; the shares' cost becomes a capital loss
		// (section 46A: the consideration is deemed nil). Finance (No. 2)
		// Act, 2024. Stock-in-trade's cost leaves through the closing stock.
		dr(counter, t.Value-t.TDS-t.Charges.Total(), "Buy-back proceeds")
		dr(AcctFSTDS, t.TDS, "TDS on deemed dividend (buy-back)")
		expCharges(t.Charges, false)
		cr(AcctFSDividend, t.Value, "Deemed dividend on buy-back, section 2(22)(f)")
		if t.Portfolio == PortInv {
			dr(AcctFSBuyBackLoss, r.CostOut, "Cost of shares bought back (capital loss, section 46A)")
			cr(fsInvAcct(t.Kind), r.CostOut, "Shares bought back, at cost")
		}
		return mergePostings(ps)
	}
	if t.Side == "B" {
		gross := t.Value + t.Charges.Total()
		if t.Portfolio == PortInv {
			dr(fsInvAcct(t.Kind), r.CostIn, "Purchase at cost")
		} else {
			specCost := r.SpecCost
			if !s.Spec {
				specCost = 0
			}
			dr(fsPurAcct(t.Kind, false), r.CostIn-specCost, "Purchase")
			if specCost != 0 {
				dr(fsPurAcct(t.Kind, true), specCost, "Purchase squared off the same day")
			}
		}
		expCharges(t.Charges, true)
		cr(counter, gross, "Purchase")
		return mergePostings(ps)
	}
	net := t.Value - t.Charges.Total()
	dr(counter, net, "Sale")
	expCharges(t.Charges, false)
	if t.Portfolio == PortInv {
		cr(fsInvAcct(t.Kind), r.CostOut, "Lots sold, at cost (FIFO)")
		cr(AcctFSInvProfit, t.Value-r.CostOut, "Profit on sale")
	} else {
		spec := r.SpecValue
		if !s.Spec {
			spec = 0
		}
		cr(fsSalAcct(t.Kind, false), t.Value-spec, "Sale")
		if spec != 0 {
			cr(fsSalAcct(t.Kind, true), spec, "Sale squared off the same day")
		}
	}
	return mergePostings(ps)
}

// mergePostings adds up the lines of each account and side.
func mergePostings(ps []ledger.Posting) []ledger.Posting {
	type k struct {
		a  string
		dr bool
	}
	idx := map[k]int{}
	var out []ledger.Posting
	for _, p := range ps {
		kk := k{p.Account, p.Debit > 0}
		if i, ok := idx[kk]; ok {
			out[i].Debit += p.Debit
			out[i].Credit += p.Credit
			continue
		}
		idx[kk] = len(out)
		out = append(out, p)
	}
	return out
}

func postingsHash(ps []ledger.Posting, date, narr string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s", date, narr)
	for _, p := range ps {
		fmt.Fprintf(h, "|%s:%d:%d", p.Account, p.Debit, p.Credit)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func fsNarration(t FSTxn) string {
	what := map[string]string{"B": "Purchase", "S": "Sale"}[t.Side]
	switch t.Type {
	case FSTypeOpening:
		what = "Opening lot"
	case FSTypeBuyBack:
		what = "Buy-back"
	case FSTypeOffMarket:
		what = "Off-market " + strings.ToLower(what)
	}
	return fmt.Sprintf("Firm securities · %s · %s %s · %s · %s", what, t.Symbol, t.Qty, PortfolioName(t.Portfolio), t.ID)
}

// fsRepost works out the entries of the live lines dated from on and
// posts those that changed: the old entry is reversed and the new one
// posted under the next revision.
func (o *Ops) fsRepost(ctx context.Context, tenant, actor, from string) error {
	s, ts, b := o.fsBookTo(ctx, tenant, "")
	for _, t := range ts {
		if t.Date < from || (t.Status != FSPosted && !(t.Status == FSCancelled && t.Entry != "")) {
			continue
		}
		var ps []ledger.Posting
		if t.Status == FSPosted {
			ps = s.fsPostings(t, b.Res[t.ID], b)
		}
		narr := fsNarration(t)
		hash := ""
		if len(ps) >= 2 {
			hash = postingsHash(ps, t.Date, narr)
		}
		if hash == t.Hash {
			continue
		}
		if err := o.checkLock(ctx, tenant, t.Date); err != nil {
			return err
		}
		if t.Entry != "" {
			if _, _, err := o.App.Ledger.Reverse(ctx, tenant, t.Entry, actor, "firm securities: "+t.ID+" worked out again", ""); err != nil && !errors.Is(err, ledger.ErrNotFound) {
				return err
			}
		}
		t.Entry, t.Hash = "", ""
		if hash != "" {
			var codes []string
			for _, p := range ps {
				codes = append(codes, p.Account)
			}
			o.ensureFSAccounts(ctx, tenant, s, codes)
			t.Rev++
			e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: fmt.Sprintf("FS:%s#%d", t.ID, t.Rev), ValueDate: t.Date, Narration: narr,
				Source: ledger.SourceRef{Kind: "FIRMSEC", ID: t.ID}, Postings: ps, CreatedBy: actor})
			if err != nil {
				return fmt.Errorf("posting %s: %w", t.ID, err)
			}
			t.Entry, t.Hash = e.ID, hash
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cFSTxn, t.ID, t, actor); err != nil {
			return err
		}
	}
	return nil
}

// fsLaterLine finds a live line of the scrip in a portfolio dated after d
// (entries go in date order, so a sale already posted keeps its cost).
func fsLaterLine(ts []FSTxn, port, isin, d, except string) (FSTxn, bool) {
	for _, t := range ts {
		if t.live() && t.Portfolio == port && t.ISIN == isin && t.Date > d && t.ID != except {
			return t, true
		}
	}
	return FSTxn{}, false
}

// ---- the register for the screen ----

// FSRegisterView is the purchases and sales register for a period.
type FSRegisterView struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Lines    []FSTxn   `json:"lines"`
	Events   []FSEvent `json:"events"`
	Pending  int       `json:"pending"`
	CanEnter bool      `json:"can_enter"`
	CanCheck bool      `json:"can_check"`
}

// FirmSecRegister lists the register lines of a period (newest first)
// with the corporate actions applied to the lots.
func (o *Ops) FirmSecRegister(ctx context.Context, tenant, actor, from, to string) (FSRegisterView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing firm securities", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return FSRegisterView{}, err
	}
	if to == "" {
		to = o.now().Format("2006-01-02")
	}
	if from == "" {
		from = addDays(to, -30)
	}
	s, ts, b := o.fsBookTo(ctx, tenant, "")
	v := FSRegisterView{From: from, To: to, Lines: []FSTxn{}, Events: []FSEvent{}}
	v.CanEnter = o.require(ctx, tenant, actor, "Entering firm securities", RoleMaker, RoleChecker) == nil
	v.CanCheck = o.require(ctx, tenant, actor, "Approving firm securities entries", RoleChecker) == nil
	closed := map[string]bool{}
	for _, t := range ts {
		if t.Status == FSPending {
			v.Pending++
		}
		if t.Date < from || t.Date > to {
			if t.Status != FSPending {
				continue
			}
		}
		t = o.decorateFS(s, t, b)
		if t.Type == FSTypeTrade && s.Default == "change" && t.Status == FSPosted {
			c, ok := closed[t.Date]
			if !ok {
				c = o.fsDayClosed(ctx, tenant, t.Date)
				closed[t.Date] = c
			}
			t.Changeable = !c
		}
		v.Lines = append(v.Lines, t)
	}
	for _, e := range b.Events {
		if e.Date >= from && e.Date <= to {
			v.Events = append(v.Events, e)
		}
	}
	sort.SliceStable(v.Lines, func(i, j int) bool {
		if v.Lines[i].Date != v.Lines[j].Date {
			return v.Lines[i].Date > v.Lines[j].Date
		}
		return v.Lines[i].ID > v.Lines[j].ID
	})
	return v, nil
}

func (o *Ops) decorateFS(s FSSettings, t FSTxn, b fsBook) FSTxn {
	t.PortfolioName, t.KindName = PortfolioName(t.Portfolio), s.kind(t.Kind).Name
	if r := b.Res[t.ID]; r != nil {
		if t.Side == "S" {
			t.CostOut = r.CostOut.String()
			if t.Portfolio == PortInv || t.Type == FSTypeJournal {
				t.Gain = (t.Value - r.CostOut).String()
			}
		}
		if r.SpecQty.Sign() > 0 {
			t.SpecQty = r.SpecQty.String()
		}
	}
	return t
}

// ---- trade files (t1) ----

// FSSyncResult is what bringing in a day's own trades did.
type FSSyncResult struct {
	Date      string   `json:"date"`
	Lines     int      `json:"lines"`
	New       int      `json:"new"`
	Changed   int      `json:"changed"`
	Cancelled int      `json:"cancelled"`
	Dividends int      `json:"dividends"`
	Warnings  []string `json:"warnings"`
}

// fsBillCharges reads the charges a day's bill put on a trading code for
// an exchange segment, head by head, from the posted bill.
func (o *Ops) fsBillCharges(ctx context.Context, tenant, date, code, exch string) (FSCharges, bool) {
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{From: date, To: date})
	if err != nil {
		return FSCharges{}, false
	}
	src := date + "/" + code + "/" + exch + "-CM"
	reversed := map[string]bool{}
	for _, e := range es {
		if e.ReversalOf != "" {
			reversed[e.ReversalOf] = true
		}
	}
	var bill *ledger.Entry
	for i := range es {
		e := es[i]
		if e.Source.Kind == "BILL" && e.Source.ID == src && !reversed[e.ID] && e.ReversalOf == "" && (bill == nil || e.Seq > bill.Seq) {
			bill = &es[i]
		}
	}
	if bill == nil {
		return FSCharges{}, false
	}
	var c FSCharges
	for _, p := range bill.Postings {
		switch {
		case p.Account == ledger.IncomeBrokerage:
			c.Brokerage += p.Credit
		case p.Account == ledger.PayableSTT:
			c.STT += p.Credit
		case strings.HasPrefix(p.Account, "PAYABLE:EXCH_TXN") || p.Account == ledger.PayableSEBIFee:
			c.Exchange += p.Credit
		case p.Account == ledger.PayableStamp:
			c.Stamp += p.Credit
		case p.Account == ledger.PayableGST:
			c.GST += p.Credit
		}
	}
	return c, true
}

// spread divides a bill's charges over the day's lines by value, head by
// head; the largest line takes what rounding leaves.
func spread(c FSCharges, values []money.Amount) []FSCharges {
	out := make([]FSCharges, len(values))
	var tot money.Amount
	big := 0
	for i, v := range values {
		tot += v
		if v > values[big] {
			big = i
		}
	}
	if tot == 0 || len(values) == 0 {
		return out
	}
	for _, h := range fsChargeHeads {
		left := c.get(h.ID)
		for i, v := range values {
			x := share(c.get(h.ID), money.DecInt(int64(v)), money.DecInt(int64(tot)))
			out[i].set(h.ID, x)
			left -= x
		}
		out[big].set(h.ID, out[big].get(h.ID)+left)
	}
	return out
}

// fsPayDate is when a cash-market trade of a date settles: the clearing
// corporation's securities pay-out, else the next trading day (T+1).
func (o *Ops) fsPayDate(ctx context.Context, tenant string, set Settings, date string) string {
	if d, ok := o.SettlementFor(ctx, tenant, "CM", "NORMAL1", date); ok && len(d.SecOut) >= 10 {
		return d.SecOut[:10]
	}
	return set.NextTradingDay(PartEquity, date)
}

// FirmSecSync brings in a day's trades under the firm's own trading codes
// from the exchange trade files: one register line per code, exchange,
// scrip and side, in the code's default portfolio, with the bill's
// charges spread by value. Safe to run again: a line keeps the portfolio
// someone chose, and changes when the trades change.
func (o *Ops) FirmSecSync(ctx context.Context, tenant, actor, date string) (FSSyncResult, error) {
	if err := o.require(ctx, tenant, actor, "Entering firm securities", RoleMaker, RoleChecker); err != nil {
		return FSSyncResult{}, err
	}
	return o.fsSync(ctx, tenant, actor, date)
}

// fsAutoSync is the sync run after billing and before the day's close.
func (o *Ops) fsAutoSync(ctx context.Context, tenant, date string) (FSSyncResult, error) {
	s := o.FirmSecSettings(ctx, tenant)
	if s.Portfolios == FSNone || s.SITSource == "manual" || len(s.ownCodes()) == 0 {
		return FSSyncResult{Date: date}, nil
	}
	return o.fsSync(ctx, tenant, SystemActor, date)
}

func (o *Ops) fsSync(ctx context.Context, tenant, actor, date string) (FSSyncResult, error) {
	res := FSSyncResult{Date: date, Warnings: []string{}}
	if !ledger.ValidDate(date) {
		return res, invalid("date must be YYYY-MM-DD")
	}
	s := o.FirmSecSettings(ctx, tenant)
	if s.Portfolios == FSNone {
		return res, fmt.Errorf("%w: the firm holds no securities of its own (Settings → Firm practices → Firm securities)", ErrState)
	}
	if s.SITSource == "manual" {
		return res, fmt.Errorf("%w: the firm enters its own trades by hand (Settings → Firm practices → Firm securities)", ErrState)
	}
	codes := s.ownCodes()
	trades, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return res, err
	}
	set, _ := o.Settings(ctx, tenant)
	_, look, _ := o.Market(ctx, tenant, date)
	type agg struct {
		code, exch, isin, sym, side, series, company string
		qty                                        money.Dec
		val                                        money.Dec
		n                                          int
	}
	groups := map[string]*agg{}
	for _, t := range trades {
		if t.Segment != "CM" || !contains(codes, strings.ToUpper(t.UCC)) {
			continue
		}
		isin, company := t.Symbol, ""
		if i, ok := look.ForTrade(t); ok && i.ISIN != "" {
			isin, company = i.ISIN, i.Company
		}
		k := strings.ToUpper(t.UCC) + "|" + t.Exchange + "|" + isin + "|" + t.Side
		g := groups[k]
		if g == nil {
			g = &agg{code: strings.ToUpper(t.UCC), exch: t.Exchange, isin: isin, sym: t.Symbol, side: t.Side, series: t.InstrumentType, company: company}
			groups[k] = g
		}
		g.qty = g.qty.Add(money.DecInt(t.Qty))
		g.val = g.val.Add(t.Price.MulInt(t.Qty))
		g.n++
	}
	// Charges per code and exchange, from the posted bills.
	byBill := map[string][]string{}
	for k, g := range groups {
		byBill[g.code+"|"+g.exch] = append(byBill[g.code+"|"+g.exch], k)
	}
	charges := map[string]FSCharges{}
	for bk, ks := range byBill {
		sort.Strings(ks)
		code, exch, _ := strings.Cut(bk, "|")
		c, ok := o.fsBillCharges(ctx, tenant, date, code, exch)
		if !ok {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s on %s is not billed yet: its lines have no charges; bring the trades in again after billing", code, exch))
			continue
		}
		vals := make([]money.Amount, len(ks))
		for i, k := range ks {
			vals[i] = groups[k].val.Round(money.UnitPaisa)
		}
		for i, part := range spread(c, vals) {
			charges[ks[i]] = part
		}
	}
	existing := map[string]FSTxn{}
	for _, t := range o.fsTxns(ctx, tenant) {
		if t.Type == FSTypeTrade && t.Date == date {
			existing[t.ID] = t
		}
	}
	pay := o.fsPayDate(ctx, tenant, set, date)
	seen := map[string]bool{}
	now := o.now().Format(time.RFC3339)
	for _, k := range sortedKeys(groups) {
		g := groups[k]
		id := "FT-" + strings.ReplaceAll(date, "-", "") + "-" + g.code + "-" + g.exch + "-" + g.isin + "-" + g.side
		seen[id] = true
		res.Lines++
		val := g.val.Round(money.UnitPaisa)
		t, ok := existing[id]
		if !ok {
			port := s.codePortfolio(g.code)
			t = FSTxn{ID: id, Date: date, PayDate: pay, Portfolio: port, DefaultPortfolio: port, Kind: kindOfTrade(g.series, g.isin, g.sym, g.company), Side: g.side, Type: FSTypeTrade,
				ISIN: g.isin, Symbol: g.sym, Exchange: g.exch, Code: g.code, Counter: ledger.ClientAccount(g.code), Status: FSPosted, Maker: actor, Created: now}
			if port == PortInv {
				t.Term = TermLong
			}
			if s.needsChecker(FSTypeTrade) {
				t.Status = FSPending
			}
			res.New++
		} else if t.Qty.Cmp(g.qty) == 0 && t.Value == val && t.Charges == charges[k] && t.Trades == g.n && t.Status != FSCancelled {
			continue
		} else {
			res.Changed++
			if t.Status == FSCancelled || t.Status == FSRejected {
				t.Status = FSPosted
				if s.needsChecker(FSTypeTrade) {
					t.Status = FSPending
				}
			}
		}
		t.Qty, t.Value, t.Charges, t.Trades, t.PayDate = g.qty, val, charges[k], g.n, pay
		if err := docs.Upsert(ctx, o.Docs, tenant, cFSTxn, t.ID, t, actor); err != nil {
			return res, err
		}
	}
	for id, t := range existing {
		if !seen[id] && t.Status != FSCancelled {
			t.Status, t.Reason = FSCancelled, "no trades remain under the code for this scrip (moved by a client code modification, or cancelled)"
			if err := docs.Upsert(ctx, o.Docs, tenant, cFSTxn, t.ID, t, actor); err != nil {
				return res, err
			}
			res.Cancelled++
		}
	}
	if err := o.fsRepost(ctx, tenant, actor, date); err != nil {
		return res, err
	}
	res.Warnings = append(res.Warnings, o.fsPledgeWarnings(ctx, tenant, actor, date)...)
	if n, err := o.fsBookDividends(ctx, tenant, actor, date); err == nil {
		res.Dividends = n
	}
	if res.New+res.Changed+res.Cancelled > 0 {
		o.log(ctx, tenant, actor, "firm_securities.synced", date, map[string]any{"lines": res.Lines, "new": res.New, "changed": res.Changed, "cancelled": res.Cancelled})
	}
	return res, nil
}

// FirmSecChange changes a trade-file line's portfolio, kind or term
// before the day is closed (s3); the day's entries for the scrip are
// worked out again.
func (o *Ops) FirmSecChange(ctx context.Context, tenant, actor, id, port, kind, term string) (FSTxn, error) {
	if err := o.require(ctx, tenant, actor, "Entering firm securities", RoleMaker, RoleChecker); err != nil {
		return FSTxn{}, err
	}
	s := o.FirmSecSettings(ctx, tenant)
	t, _, err := docs.Get[FSTxn](ctx, o.Docs, tenant, cFSTxn, id)
	if err != nil {
		return t, err
	}
	if t.Type != FSTypeTrade {
		return t, invalid("only lines from the trade files change here; a manual entry is cancelled and entered again")
	}
	if port != "" && port != t.Portfolio {
		if s.Default != "change" {
			return t, fmt.Errorf("%w: the firm's practice is that the trading code decides the portfolio (Settings → Firm practices → Firm securities)", ErrState)
		}
		if !s.has(port) {
			return t, invalid("the firm does not keep a %s portfolio", PortfolioName(port))
		}
	}
	if o.fsDayClosed(ctx, tenant, t.Date) {
		return t, fmt.Errorf("%w: %s is closed: a trade's portfolio can be changed only until the day's close; move it with a stock journal instead", ErrState, t.Date)
	}
	ts := o.fsTxns(ctx, tenant)
	for _, p := range []string{t.Portfolio, firstOf(port, t.Portfolio)} {
		if x, ok := fsLaterLine(ts, p, t.ISIN, t.Date, t.ID); ok {
			return t, fmt.Errorf("%w: %s has a later line in %s (%s on %s)", ErrState, t.Symbol, PortfolioName(p), x.ID, x.Date)
		}
		for _, x := range ts {
			if x.Type == FSTypeJournal && x.live() && x.Date == t.Date && x.ISIN == t.ISIN && x.Portfolio == p {
				return t, fmt.Errorf("%w: a stock journal of %s is posted on %s", ErrState, t.Symbol, t.Date)
			}
		}
	}
	was := t.Portfolio
	if port != "" {
		t.Portfolio = port
	}
	if kind != "" {
		t.Kind = s.kind(kind).ID
	}
	if term == TermLong || term == TermCurrent {
		t.Term = term
	}
	if t.Portfolio == PortInv && t.Term == "" {
		t.Term = TermLong
	}
	if t.Portfolio == PortSIT {
		t.Term = ""
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cFSTxn, t.ID, t, actor); err != nil {
		return t, err
	}
	if err := o.fsRepost(ctx, tenant, actor, t.Date); err != nil {
		return t, err
	}
	o.log(ctx, tenant, actor, "firm_securities.changed", t.ID, map[string]any{"portfolio": t.Portfolio, "was": was, "kind": t.Kind, "term": t.Term})
	t, _, err = docs.Get[FSTxn](ctx, o.Docs, tenant, cFSTxn, id)
	return t, err
}

// ---- manual and off-market entries ----

// FSEntryInput is a manual entry.
type FSEntryInput struct {
	Date      string    `json:"date"`
	Portfolio string    `json:"portfolio"`
	Side      string    `json:"side"`
	Type      string    `json:"type"` // Manual, Off-market, Buy-back
	ISIN      string    `json:"isin"`
	Symbol    string    `json:"symbol"`
	Kind      string    `json:"kind"`
	Qty       string    `json:"qty"`
	Price     string    `json:"price"`
	Charges   FSCharges `json:"charges"`
	TDS       string    `json:"tds,omitempty"`
	Counter   string    `json:"counter"`
	Term      string    `json:"term,omitempty"`
	PayDate   string    `json:"pay_date,omitempty"`
	Note      string    `json:"note"`
}

// AddFirmSecEntry records a manual or off-market purchase or sale, or a
// buy-back accepted. It waits for a second person unless the firm's
// practice (c5) says otherwise. Warnings (a pledged lot sold) do not
// stop it: pledges are tracked, not blocked (c2).
func (o *Ops) AddFirmSecEntry(ctx context.Context, tenant, actor string, in FSEntryInput) (FSTxn, error) {
	if err := o.require(ctx, tenant, actor, "Entering firm securities", RoleMaker, RoleChecker); err != nil {
		return FSTxn{}, err
	}
	s := o.FirmSecSettings(ctx, tenant)
	in.ISIN, in.Symbol = strings.ToUpper(strings.TrimSpace(in.ISIN)), strings.ToUpper(strings.TrimSpace(in.Symbol))
	switch {
	case !ledger.ValidDate(in.Date):
		return FSTxn{}, invalid("date must be YYYY-MM-DD")
	case !s.has(in.Portfolio):
		return FSTxn{}, invalid("choose one of the firm's portfolios")
	case in.Side != "B" && in.Side != "S":
		return FSTxn{}, invalid("side must be B (purchase) or S (sale)")
	case in.ISIN == "" || in.Symbol == "":
		return FSTxn{}, invalid("give the ISIN and the scrip")
	}
	typ := map[string]string{"": FSTypeManual, FSTypeManual: FSTypeManual, FSTypeOffMarket: FSTypeOffMarket, FSTypeBuyBack: FSTypeBuyBack}[in.Type]
	if typ == "" {
		return FSTxn{}, invalid("type must be Manual, Off-market or Buy-back")
	}
	if typ == FSTypeBuyBack && in.Side != "S" {
		return FSTxn{}, invalid("a buy-back takes shares out: side S")
	}
	qty, err := parseQty(in.Qty)
	if err != nil || qty.Sign() <= 0 {
		return FSTxn{}, invalid("give the quantity, above zero")
	}
	price, err := money.ParseDec(strings.TrimSpace(in.Price))
	if err != nil || price.Sign() < 0 {
		return FSTxn{}, invalid("give the price per unit (0 for a transfer without consideration)")
	}
	var tds money.Amount
	if strings.TrimSpace(in.TDS) != "" {
		if tds, err = money.ParseAmount(strings.TrimSpace(in.TDS)); err != nil || tds < 0 {
			return FSTxn{}, invalid("TDS is an amount in rupees")
		}
	}
	for _, h := range fsChargeHeads {
		if in.Charges.get(h.ID) < 0 {
			return FSTxn{}, invalid("charges cannot be negative")
		}
	}
	counter := strings.ToUpper(strings.TrimSpace(firstOf(in.Counter, ledger.BankOwn)))
	if _, err := ledger.TypeOf(counter); err != nil {
		return FSTxn{}, invalid("the account the money goes through: %v", err)
	}
	if err := o.checkLock(ctx, tenant, in.Date); err != nil {
		return FSTxn{}, err
	}
	ts := o.fsTxns(ctx, tenant)
	if x, ok := fsLaterLine(ts, in.Portfolio, in.ISIN, in.Date, ""); ok {
		return FSTxn{}, fmt.Errorf("%w: enter %s in date order: %s on %s is already in the register", ErrState, in.Symbol, x.ID, x.Date)
	}
	kind := in.Kind
	if kind == "" {
		kind = kindOfTrade("", in.ISIN, in.Symbol, "")
	}
	t := FSTxn{Date: in.Date, PayDate: firstOf(in.PayDate, in.Date), Portfolio: in.Portfolio, Kind: s.kind(kind).ID, Side: in.Side, Type: typ, ISIN: in.ISIN, Symbol: in.Symbol,
		Qty: qty, Value: price.Mul(qty).Round(money.UnitPaisa), Charges: in.Charges, TDS: tds, Counter: counter, Note: strings.TrimSpace(in.Note), Maker: actor, Created: o.now().Format(time.RFC3339)}
	if t.Portfolio == PortInv {
		t.Term = map[bool]string{true: in.Term, false: TermLong}[in.Term == TermLong || in.Term == TermCurrent]
	}
	if t.Side == "S" {
		_, _, b := o.fsBookTo(ctx, tenant, in.Date)
		held := b.held(t.Portfolio, t.ISIN)
		if held.Cmp(qty) < 0 {
			t.Warnings = append(t.Warnings, fmt.Sprintf("only %s held in %s on %s", held, PortfolioName(t.Portfolio), in.Date))
		}
		if w := o.fsPledgeWarning(ctx, tenant, t, held.Sub(qty)); w != "" {
			t.Warnings = append(t.Warnings, w)
		}
	}
	if t.ID, err = o.fsNewID(ctx, tenant, actor, "FS"); err != nil {
		return t, err
	}
	t.Status = FSPosted
	if s.needsChecker(typ) {
		t.Status = FSPending
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cFSTxn, t.ID, t, actor); err != nil {
		return t, err
	}
	if t.Status == FSPosted {
		if err := o.fsRepost(ctx, tenant, actor, t.Date); err != nil {
			return t, err
		}
	} else {
		o.raiseWork(ctx, tenant, "firmsec", "entry|"+t.ID, "Firm securities entry to approve: "+t.Symbol,
			fmt.Sprintf("%s entered a %s %s of %s %s in %s on %s. A second person approves it on Firm securities → Purchases & sales.", actor, strings.ToLower(typ), map[string]string{"B": "purchase", "S": "sale"}[t.Side], t.Qty, t.Symbol, PortfolioName(t.Portfolio), t.Date),
			"", RoleChecker, "firmsec", t.Date)
	}
	o.log(ctx, tenant, actor, "firm_securities.entered", t.ID, map[string]any{"type": typ, "side": t.Side, "isin": t.ISIN, "qty": t.Qty.String(), "value": t.Value.String(), "status": t.Status})
	t, _, err = docs.Get[FSTxn](ctx, o.Docs, tenant, cFSTxn, t.ID)
	return t, err
}

// DecideFirmSecEntry approves (posts) or rejects a waiting entry, or
// cancels a posted manual one (its entry is reversed). The person who
// entered it cannot approve it.
func (o *Ops) DecideFirmSecEntry(ctx context.Context, tenant, actor, id, action, reason string) (FSTxn, error) {
	if err := o.require(ctx, tenant, actor, "Approving firm securities entries", RoleChecker); err != nil {
		return FSTxn{}, err
	}
	t, _, err := docs.Get[FSTxn](ctx, o.Docs, tenant, cFSTxn, id)
	if err != nil {
		return t, err
	}
	reason = strings.TrimSpace(reason)
	switch action {
	case "approve":
		if t.Status != FSPending {
			return t, fmt.Errorf("%w: %s is %s", ErrState, id, t.Status)
		}
		if t.Maker == actor {
			return t, fmt.Errorf("%w: you entered %s; another person approves it", ErrForbidden, id)
		}
		if x, ok := fsLaterLine(o.fsTxns(ctx, tenant), t.Portfolio, t.ISIN, t.Date, t.ID); ok && t.Type != FSTypeTrade {
			return t, fmt.Errorf("%w: %s on %s was posted after this entry was made; reject it and enter it again in date order", ErrState, x.ID, x.Date)
		}
		t.Status, t.Checker = FSPosted, actor
	case "reject":
		if t.Status != FSPending {
			return t, fmt.Errorf("%w: %s is %s", ErrState, id, t.Status)
		}
		if reason == "" {
			return t, invalid("say why it is rejected")
		}
		t.Status, t.Checker, t.Reason = FSRejected, actor, reason
	case "cancel":
		if t.Status != FSPosted || t.Type == FSTypeTrade || t.Type == FSTypeJournal {
			return t, fmt.Errorf("%w: only a posted manual entry is cancelled here", ErrState)
		}
		if reason == "" {
			return t, invalid("say why it is cancelled")
		}
		if x, ok := fsLaterLine(o.fsTxns(ctx, tenant), t.Portfolio, t.ISIN, t.Date, t.ID); ok {
			return t, fmt.Errorf("%w: %s on %s comes after it; cancel the later lines first", ErrState, x.ID, x.Date)
		}
		t.Status, t.Checker, t.Reason = FSCancelled, actor, reason
	default:
		return t, invalid("action must be approve, reject or cancel")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cFSTxn, t.ID, t, actor); err != nil {
		return t, err
	}
	if action != "reject" {
		if err := o.fsRepost(ctx, tenant, actor, t.Date); err != nil {
			return t, err
		}
	}
	o.closeWork(ctx, tenant, actor, "firmsec", "entry|"+t.ID, action)
	o.log(ctx, tenant, actor, "firm_securities."+action, t.ID, map[string]any{"reason": reason})
	t, _, err = docs.Get[FSTxn](ctx, o.Docs, tenant, cFSTxn, id)
	return t, err
}

// ---- opening lots (c7) ----

// PostFirmSecOpening posts the opening lots loaded from the old system to
// the books (Dr the securities accounts, Cr the account the opening
// balances were carried in), once a second person agrees: lots already
// in an opening trial balance under another account are moved from it.
func (o *Ops) PostFirmSecOpening(ctx context.Context, tenant, actor, counter string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Approving firm securities entries", RoleChecker); err != nil {
		return 0, err
	}
	counter = strings.ToUpper(strings.TrimSpace(firstOf(counter, ledger.OpeningEquity)))
	if _, err := ledger.TypeOf(counter); err != nil {
		return 0, invalid("%v", err)
	}
	n := 0
	from := ""
	for _, t := range o.fsTxns(ctx, tenant) {
		if t.Type != FSTypeOpening || t.Status != FSLoaded {
			continue
		}
		if t.Maker == actor {
			return n, fmt.Errorf("%w: you loaded the opening lots; another person posts them", ErrForbidden)
		}
		t.Status, t.Checker, t.Counter = FSPosted, actor, counter
		if err := docs.Upsert(ctx, o.Docs, tenant, cFSTxn, t.ID, t, actor); err != nil {
			return n, err
		}
		if from == "" || t.Date < from {
			from = t.Date
		}
		n++
	}
	if n == 0 {
		return 0, fmt.Errorf("%w: no opening lots are waiting to be posted", ErrState)
	}
	if err := o.fsRepost(ctx, tenant, actor, from); err != nil {
		return n, err
	}
	o.log(ctx, tenant, actor, "firm_securities.opening_posted", counter, map[string]any{"lots": n})
	return n, nil
}
