// Package billing turns a day's exchange trades into balanced journal
// entries: one entry per client per exchange segment, carrying the trade
// value or MTM against the clearing account and each charge against its own
// income or payable account.
package billing

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ErrMissingSettlementPrice means futures cannot be marked to market.
var ErrMissingSettlementPrice = errors.New("billing: settlement price missing")

// Input is one billing run.
type Input struct {
	Tenant           string
	Date             string
	Actor            string
	Trades           []ingest.TradeEvent  // exchange trades; others are rejected
	SettlementPrices map[string]money.Dec // keyed by TradeEvent.Instrument()
	Schedule         charges.Schedule
	// Brokerage, when set, charges brokerage per executed order from the
	// client's card instead of per trade from the schedule. It returns the
	// rule and a card label; ok=false falls back to the schedule's rate.
	Brokerage BrokerageFunc
	// LotSize gives a derivative trade's lot size for per-lot brokerage.
	LotSize func(t ingest.TradeEvent) int64
	// Classify gives a trade's channel and cases (auction, index, stock),
	// most specific first. Billing adds rollover itself.
	Classify func(t ingest.TradeEvent) (channel string, cases []string)
}

// BrokerageFunc resolves a client's brokerage rule for an order.
type BrokerageFunc = func(ucc string, q charges.RateQuery, date string) (rule charges.BrokerageRule, plan string, ok bool)

// Rollovers finds futures trades that move a position to a later expiry:
// on the same day, the same client trades one expiry of an underlying on
// one side and a later expiry on the other. Both legs are the rollover.
func Rollovers(ts []ingest.TradeEvent) map[string]bool {
	type key struct{ ucc, exch, seg, sym string }
	sides := map[key]map[string]map[string]bool{} // expiry -> side
	for _, t := range ts {
		if t.InstrumentType != "FUT" || t.Expiry == "" {
			continue
		}
		k := key{t.UCC, t.Exchange, t.Segment, t.Symbol}
		if sides[k] == nil {
			sides[k] = map[string]map[string]bool{}
		}
		if sides[k][t.Expiry] == nil {
			sides[k][t.Expiry] = map[string]bool{}
		}
		sides[k][t.Expiry][t.Side] = true
	}
	roll := map[key]map[string]bool{} // expiries that are legs of a roll
	for k, ex := range sides {
		for e1, s1 := range ex {
			for e2, s2 := range ex {
				if e1 >= e2 {
					continue
				}
				if (s1["S"] && s2["B"]) || (s1["B"] && s2["S"]) {
					if roll[k] == nil {
						roll[k] = map[string]bool{}
					}
					roll[k][e1], roll[k][e2] = true, true
				}
			}
		}
	}
	out := map[string]bool{}
	for _, t := range ts {
		if roll[key{t.UCC, t.Exchange, t.Segment, t.Symbol}][t.Expiry] && t.InstrumentType == "FUT" {
			out[TradeKey(t)] = true
		}
	}
	return out
}

// Group summarises one client × exchange segment bill.
type Group struct {
	UCC      string            `json:"ucc"`
	Exchange string            `json:"exchange"`
	Segment  string            `json:"segment"`
	Trades   int               `json:"trades"`
	TradeNet money.Amount      `json:"trade_net"` // + means owed to the client
	Charges  charges.Breakdown `json:"charges"`
	Plan     string            `json:"plan,omitempty"`
	Net      money.Amount      `json:"net"` // TradeNet − charges
	EntryKey string            `json:"entry_key"`
}

// Output holds the entries to post and their summaries.
type Output struct {
	Entries []ledger.Entry `json:"entries"`
	Groups  []Group        `json:"groups"`
	// TradeBrokerage is each trade's share of its order's brokerage, keyed
	// exchange|segment|trade number, when brokerage is charged per order.
	TradeBrokerage map[string]money.Amount `json:"trade_brokerage,omitempty"`
}

// TradeKey identifies a trade in Output.TradeBrokerage.
func TradeKey(t ingest.TradeEvent) string { return t.Exchange + "|" + t.Segment + "|" + t.TradeNo }

// EntryKey is the idempotency key of a group's bill. One bill per client,
// segment and date: re-billing after a correction means reversing first.
func EntryKey(date, ucc, exchange, segment string) string {
	return fmt.Sprintf("BILL:%s:%s:%s-%s", date, ucc, exchange, segment)
}

// Build prices and groups the trades without posting anything.
//
// Scope note: futures are marked to market for today's trades against the
// settlement price. Carry-forward position MTM and option exercise /
// assignment are later phases.
func Build(in Input) (Output, error) {
	if !ledger.ValidDate(in.Date) {
		return Output{}, fmt.Errorf("billing: invalid date %q", in.Date)
	}
	if in.Tenant == "" || in.Actor == "" {
		return Output{}, errors.New("billing: tenant and actor are required")
	}
	type acc struct {
		ucc, exch, seg string
		net            money.Dec
		chg            charges.Breakdown
		n              int
		plans          map[string]bool
	}
	groups := map[string]*acc{}
	type order struct {
		group    string
		bucket   charges.Bucket
		exch     string
		channel  string
		cases    []string
		turnover money.Dec
		qty, lot int64
		trades   []ingest.TradeEvent
	}
	orders := map[string]*order{}
	var orderKeys []string
	missing := map[string]bool{}
	var rolls map[string]bool
	if in.Brokerage != nil {
		var today []ingest.TradeEvent
		for _, t := range in.Trades {
			if t.TradeDate == in.Date && t.Tenant == in.Tenant {
				today = append(today, t)
			}
		}
		rolls = Rollovers(today)
	}
	for _, t := range in.Trades {
		if t.Origin != ingest.FromExchange {
			return Output{}, fmt.Errorf("billing: trade %s is from %s; bills use exchange trades only", t.TradeNo, t.Origin)
		}
		if t.TradeDate != in.Date || t.Tenant != in.Tenant {
			continue
		}
		b, err := charges.BucketFor(t.Segment, t.InstrumentType, t.Product)
		if err != nil {
			return Output{}, fmt.Errorf("billing: trade %s: %w", t.TradeNo, err)
		}
		bd, err := in.Schedule.Compute(charges.Leg{Bucket: b, Exchange: t.Exchange, Side: t.Side, Qty: t.Qty, Price: t.Price, Date: t.TradeDate, NoBrokerage: in.Brokerage != nil})
		if err != nil {
			return Output{}, fmt.Errorf("billing: trade %s: %w", t.TradeNo, err)
		}
		k := t.UCC + "|" + t.Exchange + "|" + t.Segment
		g := groups[k]
		if g == nil {
			g = &acc{ucc: t.UCC, exch: t.Exchange, seg: t.Segment}
			groups[k] = g
		}
		g.n++
		g.chg = g.chg.Add(bd)
		if in.Brokerage != nil {
			ok := t.OrderNo
			if ok == "" {
				ok = "T" + t.TradeNo
			}
			channel, cases := "", []string(nil)
			if in.Classify != nil {
				channel, cases = in.Classify(t)
			}
			if rolls[TradeKey(t)] {
				cases = append([]string{charges.CaseRollover}, cases...)
			}
			ok = k + "|" + string(b) + "|" + channel + "|" + strings.Join(cases, ",") + "|" + ok
			o := orders[ok]
			if o == nil {
				lot := int64(1)
				if in.LotSize != nil {
					lot = in.LotSize(t)
				}
				o = &order{group: k, bucket: b, exch: t.Exchange, channel: channel, cases: cases, lot: lot}
				orders[ok] = o
				orderKeys = append(orderKeys, ok)
			}
			o.turnover = o.turnover.Add(t.Value())
			o.qty += t.Qty
			o.trades = append(o.trades, t)
		}
		if t.InstrumentType == "FUT" {
			sp, ok := in.SettlementPrices[t.Instrument()]
			if !ok {
				missing[t.Instrument()] = true
				continue
			}
			mtm := sp.Sub(t.Price).MulInt(t.Qty)
			if t.Side == "S" {
				mtm = mtm.Neg()
			}
			g.net = g.net.Add(mtm)
		} else if t.Side == "B" {
			g.net = g.net.Sub(t.Value())
		} else {
			g.net = g.net.Add(t.Value())
		}
	}
	if len(missing) > 0 {
		var m []string
		for k := range missing {
			m = append(m, k)
		}
		sort.Strings(m)
		return Output{}, fmt.Errorf("%w for %s", ErrMissingSettlementPrice, strings.Join(m, ", "))
	}
	var tradeBrok map[string]money.Amount
	if in.Brokerage != nil {
		tradeBrok = map[string]money.Amount{}
		sort.Strings(orderKeys)
		for _, ok := range orderKeys {
			o := orders[ok]
			g := groups[o.group]
			rule, plan, found := in.Brokerage(g.ucc, charges.RateQuery{Bucket: o.bucket, Exchange: o.exch, Channel: o.channel, Cases: o.cases}, in.Date)
			if !found {
				r, err := in.Schedule.Rule(o.bucket, o.exch, in.Date)
				if err != nil {
					return Output{}, fmt.Errorf("billing: %w", err)
				}
				rule, plan = charges.ScheduleBrokerage(r), "Schedule "+in.Schedule.Version
			}
			if g.plans == nil {
				g.plans = map[string]bool{}
			}
			g.plans[plan] = true
			brok := rule.Compute(o.bucket, o.turnover, o.qty, o.lot).Round(money.UnitPaisa)
			gst := brok.Dec().Pct(in.Schedule.GSTPct).Round(money.UnitPaisa)
			g.chg.Brokerage += brok
			g.chg.GST += gst
			g.chg.Total += brok + gst
			// Share the order's brokerage across its trades by value; the
			// last trade takes the rounding remainder.
			left := brok
			for i, t := range o.trades {
				share := left
				if i < len(o.trades)-1 && o.turnover.Sign() > 0 {
					q, _ := t.Value().Mul(brok.Dec()).Quo(o.turnover)
					share = q.Round(money.UnitPaisa)
				}
				tradeBrok[TradeKey(t)] = share
				left -= share
			}
		}
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := Output{Entries: []ledger.Entry{}, Groups: []Group{}, TradeBrokerage: tradeBrok}
	for _, k := range keys {
		g := groups[k]
		client := ledger.ClientAccount(g.ucc)
		cc := ledger.ClearingAccount(g.exch, g.seg)
		net := g.net.Round(money.UnitPaisa)
		var ps []ledger.Posting
		switch {
		case net > 0:
			ps = append(ps, ledger.Posting{Account: cc, Debit: net, Memo: "Trade value / MTM"}, ledger.Posting{Account: client, Credit: net, Memo: "Trade value / MTM"})
		case net < 0:
			ps = append(ps, ledger.Posting{Account: client, Debit: -net, Memo: "Trade value / MTM"}, ledger.Posting{Account: cc, Credit: -net, Memo: "Trade value / MTM"})
		}
		if g.chg.Total > 0 {
			ps = append(ps, ledger.Posting{Account: client, Debit: g.chg.Total, Memo: "Brokerage, taxes and charges"})
			for _, c := range []struct {
				acct string
				amt  money.Amount
				memo string
			}{
				{ledger.IncomeBrokerage, g.chg.Brokerage, "Brokerage"},
				{ledger.PayableSTT, g.chg.STT, "STT / CTT"},
				{ledger.ExchangeTxnPayable(g.exch), g.chg.ExchTxn, "Exchange transaction charges"},
				{ledger.PayableSEBIFee, g.chg.SEBIFee, "SEBI turnover fee"},
				{ledger.PayableStamp, g.chg.Stamp, "Stamp duty"},
				{ledger.PayableGST, g.chg.GST, "GST"},
			} {
				if c.amt > 0 {
					ps = append(ps, ledger.Posting{Account: c.acct, Credit: c.amt, Memo: c.memo})
				}
			}
		}
		key := EntryKey(in.Date, g.ucc, g.exch, g.seg)
		var plans []string
		for p := range g.plans {
			plans = append(plans, p)
		}
		sort.Strings(plans)
		out.Groups = append(out.Groups, Group{UCC: g.ucc, Exchange: g.exch, Segment: g.seg, Trades: g.n, TradeNet: net, Charges: g.chg, Net: net - g.chg.Total, EntryKey: key, Plan: strings.Join(plans, ", ")})
		if len(ps) < 2 {
			continue // zero-value group, nothing to post
		}
		out.Entries = append(out.Entries, ledger.Entry{
			Tenant: in.Tenant, Key: key, ValueDate: in.Date,
			Narration: fmt.Sprintf("Bill %s-%s %s · %d trade(s)", g.exch, g.seg, in.Date, g.n),
			Source:    ledger.SourceRef{Kind: "BILL", ID: in.Date + "/" + g.ucc + "/" + g.exch + "-" + g.seg},
			Postings:  ps, CreatedBy: in.Actor,
		})
	}
	return out, nil
}
