package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- more of what a contract note says, and trades the exchange cancelled ----
//
//   - Contract-wise summary at a weighted average price (NSE/INSP/64389,
//     Circular 72/2024, Annexure I, in force from trade date 27 Jun 2025 by
//     NSE/INSP/68618; MCX/INSP/354/2025 Annexure A for commodities): one
//     line a contract and side with the quantity, the weighted average
//     price, the brokerage per unit, the net rate per unit and the net total
//     before levies, followed by an annexure of every order and trade.
//     Equity trades in the same security on NSE and BSE are averaged
//     together (NSE's common contract note, exchange code 99); commodity
//     trades are averaged exchange by exchange, as MCX's format says. The
//     circular's own column layout was not at hand: the columns follow the
//     items the gap report quotes; to confirm against Annexure I.
//   - Positions closed because a margin call was not met (NSE/ISC/73908
//     Part A 9.7-9.8; MCX/ISD/243/2026 Part A 10.2.1): the note says so
//     against those trades. A trade is taken as such a close-out when it
//     is the fill of an RMS square-off instruction of the day (rms.go):
//     the same trade number, or the same client, contract and side.
//   - Rights entitlements (NSE/INSP/73976 item 4.22): a note with a
//     purchase of REs carries a prominent disclaimer that REs only give the
//     right to apply or renounce before the issue closes and lapse if not
//     used. REs are known from the firm's register of RE securities (the
//     exchange's notice gives the symbol and ISIN; Ledgerline's trade
//     records do not keep the series E1/E2) and from a symbol ending -RE.
//   - MCX last trading date (MCX/CTCL/256/2026 para 2.6): an MCX contract
//     is shown with its last trading date from the product master
//     (Product End Date Time, column 24), which MCX revises when a holiday
//     falls on the expiry.
//   - Trades the exchange cancelled (NSE/SURV/74008 item 29: the reversal
//     trade cancellation mechanism and SGTCM): Ledgerline's trade records
//     do not keep the trade file's status column (RptdTxSts), and NSE's
//     cancellation codes were not in the text at hand, so a cancellation is
//     recorded here by an Ops checker from the exchange's message, with its
//     reference. A cancelled trade drops out of obligations, bills, margins
//     and contract notes (it is removed with the client code modifications
//     in applyCCM); the client's bill is corrected and a revised note sent.

const (
	cTradeCxl  = "tradecxl"
	cRightsEnt = "rightsent"
)

// TradeCancelSources are where a cancellation comes from.
var TradeCancelSources = []string{"RTCM (reversal trade cancellation)", "SGTCM", "Exchange trade cancellation message", "Exchange circular or e-mail"}

// TradeCancel is an exchange trade the exchange cancelled.
type TradeCancel struct {
	Key      string    `json:"key"` // the trade's key (exchange|segment|date|trade no)
	Date     string    `json:"date"`
	Exchange string    `json:"exchange"`
	Segment  string    `json:"segment"`
	TradeNo  string    `json:"trade_no"`
	UCC      string    `json:"ucc"`
	Contract string    `json:"contract"`
	Side     string    `json:"side"`
	Qty      int64     `json:"qty"`
	Price    money.Dec `json:"price"`
	Source   string    `json:"source"`
	Ref      string    `json:"ref"` // the exchange's message or circular reference
	Note     string    `json:"note,omitempty"`
	// Rebilled: the client's bill was corrected and the note revised (or
	// why not, to do by hand).
	Rebilled string `json:"rebilled,omitempty"`
	By       string `json:"by"`
	At       string `json:"at"`
}

// RecordTradeCancel records an exchange trade the exchange cancelled; an Ops
// checker does it, from the exchange's message.
func (o *Ops) RecordTradeCancel(ctx context.Context, tenant, actor string, in TradeCancel) (TradeCancel, error) {
	if err := o.require(ctx, tenant, actor, "Recording a trade cancelled by the exchange", RoleChecker); err != nil {
		return TradeCancel{}, err
	}
	in.Exchange, in.Segment = strings.ToUpper(strings.TrimSpace(in.Exchange)), strings.ToUpper(strings.TrimSpace(in.Segment))
	in.TradeNo, in.Ref, in.Note = strings.TrimSpace(in.TradeNo), strings.TrimSpace(in.Ref), strings.TrimSpace(in.Note)
	if !validDate(in.Date) || in.Exchange == "" || in.TradeNo == "" {
		return TradeCancel{}, invalid("give the trade date, exchange and trade number")
	}
	if !contains(TradeCancelSources, in.Source) {
		return TradeCancel{}, invalid("say where the cancellation comes from: %s", strings.Join(TradeCancelSources, ", "))
	}
	if in.Ref == "" {
		return TradeCancel{}, invalid("give the exchange's reference for the cancellation (message, circular or e-mail)")
	}
	raw, err := o.App.Trades.List(ctx, tenant, ingest.FromExchange, in.Date)
	if err != nil {
		return TradeCancel{}, err
	}
	var t *ingest.TradeEvent
	for i := range raw {
		r := raw[i]
		if r.Exchange == in.Exchange && r.TradeNo == in.TradeNo && (in.Segment == "" || r.Segment == in.Segment) {
			t = &raw[i]
			break
		}
	}
	if t == nil {
		return TradeCancel{}, invalid("no %s trade %s on %s in the exchange trade files", in.Exchange, in.TradeNo, fmtDay(in.Date))
	}
	if _, _, err := docs.Get[TradeCancel](ctx, o.Docs, tenant, cTradeCxl, t.Key()); err == nil {
		return TradeCancel{}, fmt.Errorf("%w: trade %s is already recorded as cancelled", ErrState, t.TradeNo)
	}
	// The client is the one the trade belongs to now (after any client
	// code modification).
	ucc := t.UCC
	if adj, err := o.applyCCM(ctx, tenant, []ingest.TradeEvent{*t}); err == nil && len(adj) == 1 {
		ucc = adj[0].UCC
	}
	x := TradeCancel{Key: t.Key(), Date: in.Date, Exchange: t.Exchange, Segment: t.Segment, TradeNo: t.TradeNo, UCC: ucc,
		Contract: InstrumentName(t.Symbol, t.InstrumentType, t.Expiry, t.Strike), Side: t.Side, Qty: t.Qty, Price: t.Price,
		Source: in.Source, Ref: in.Ref, Note: in.Note, By: actor, At: o.now().Format(time.RFC3339)}
	if _, err := docs.Put(ctx, o.Docs, tenant, cTradeCxl, x.Key, x, 0, actor); err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "trade.cancelled_by_exchange", x.Key, map[string]any{"ucc": ucc, "source": x.Source, "ref": x.Ref})
	// Correct the client's bill and revise the note when they were made.
	x.Rebilled = "No bill or contract note yet: billing leaves the trade out."
	if ns, _ := o.Notes(ctx, tenant, in.Date); len(ns) > 0 {
		has := false
		for _, n := range ns {
			has = has || n.UCC == ucc
		}
		if has {
			reason := fmt.Sprintf("trade %s cancelled by %s (%s)", x.TradeNo, x.Exchange, x.Ref)
			if _, err := o.CorrectBill(ctx, tenant, actor, in.Date, ucc, reason, "", "", true); err != nil {
				x.Rebilled = "Correct the bill by hand: " + err.Error()
				o.raiseWork(ctx, tenant, "tradecxl", x.Key, "Rebill "+ucc+" for the cancelled trade "+x.TradeNo,
					fmt.Sprintf("%s cancelled trade %s of %s (%s). Ledgerline could not correct the bill (%v): correct it on Billing → Correct a bill and send the revised contract note.", x.Exchange, x.TradeNo, fmtDay(x.Date), x.Ref, err), ucc, RoleChecker, "notes", in.Date)
			} else {
				x.Rebilled = "Bill corrected and revised contract note sent."
			}
		}
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cTradeCxl, x.Key, actor, func(y *TradeCancel) error { y.Rebilled = x.Rebilled; return nil })
	return x, nil
}

// TradeCancels lists the trades the exchanges cancelled, newest first.
func (o *Ops) TradeCancels(ctx context.Context, tenant, actor string) ([]TradeCancel, error) {
	if err := o.require(ctx, tenant, actor, "Viewing trades cancelled by the exchange", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return nil, err
	}
	xs, err := docs.List[TradeCancel](ctx, o.Docs, tenant, cTradeCxl, "")
	sort.Slice(xs, func(i, j int) bool { return xs[i].At > xs[j].At })
	return xs, err
}

// dropCancelled leaves out the trades the exchanges cancelled.
func (o *Ops) dropCancelled(ctx context.Context, tenant string, evs []ingest.TradeEvent) []ingest.TradeEvent {
	xs, err := docs.List[TradeCancel](ctx, o.Docs, tenant, cTradeCxl, "")
	if err != nil || len(xs) == 0 {
		return evs
	}
	gone := map[string]bool{}
	for _, x := range xs {
		gone[x.Key] = true
	}
	out := make([]ingest.TradeEvent, 0, len(evs))
	for _, e := range evs {
		if !gone[e.Key()] {
			out = append(out, e)
		}
	}
	return out
}

// ---- rights entitlements ----

// RightsEntitlement is a security traded as rights entitlements.
type RightsEntitlement struct {
	ID       string `json:"id"` // exchange|symbol
	Exchange string `json:"exchange"`
	Symbol   string `json:"symbol"` // as in the trade file
	ISIN     string `json:"isin,omitempty"`
	Company  string `json:"company,omitempty"`
	Closes   string `json:"issue_closes"` // the rights issue closes on
	Ref      string `json:"ref,omitempty"`
	By       string `json:"by"`
	At       string `json:"at"`
}

// SaveRightsEntitlement adds or updates a security on the register of RE
// securities, from the exchange's notice.
func (o *Ops) SaveRightsEntitlement(ctx context.Context, tenant, actor string, r RightsEntitlement) (RightsEntitlement, error) {
	if err := o.require(ctx, tenant, actor, "Adding rights entitlements", RoleMaker, RoleChecker); err != nil {
		return r, err
	}
	r.Exchange, r.Symbol = strings.ToUpper(strings.TrimSpace(r.Exchange)), strings.ToUpper(strings.TrimSpace(r.Symbol))
	r.ISIN, r.Company, r.Ref = strings.ToUpper(strings.TrimSpace(r.ISIN)), strings.TrimSpace(r.Company), strings.TrimSpace(r.Ref)
	if r.Exchange != "NSE" && r.Exchange != "BSE" {
		return r, invalid("rights entitlements trade on NSE or BSE")
	}
	if r.Symbol == "" || !validDate(r.Closes) {
		return r, invalid("give the RE's symbol as the trade file has it and the date the rights issue closes")
	}
	r.ID, r.By, r.At = r.Exchange+"|"+r.Symbol, actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cRightsEnt, r.ID, r, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "rights_entitlement.saved", r.ID, map[string]any{"closes": r.Closes})
	return r, nil
}

// RightsEntitlementList lists the register for the screen.
func (o *Ops) RightsEntitlementList(ctx context.Context, tenant, actor string) ([]RightsEntitlement, error) {
	if err := o.require(ctx, tenant, actor, "The register of rights entitlements", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return nil, err
	}
	return o.RightsEntitlements(ctx, tenant)
}

// RightsEntitlements lists the register.
func (o *Ops) RightsEntitlements(ctx context.Context, tenant string) ([]RightsEntitlement, error) {
	return docs.List[RightsEntitlement](ctx, o.Docs, tenant, cRightsEnt, "")
}

// reRemarks is the disclaimer for REs bought on the note.
func (o *Ops) reRemarks(ctx context.Context, tenant string, trades []ingest.TradeEvent) []string {
	reg := map[string]RightsEntitlement{}
	if rs, err := o.RightsEntitlements(ctx, tenant); err == nil {
		for _, r := range rs {
			reg[r.ID] = r
		}
	}
	var bought []string
	closes := map[string]bool{}
	seen := map[string]bool{}
	for _, t := range trades {
		if t.Side != "B" || t.Segment != "CM" {
			continue
		}
		r, ok := reg[t.Exchange+"|"+t.Symbol]
		if !ok && !strings.HasSuffix(t.Symbol, "-RE") {
			continue
		}
		if !seen[t.Symbol] {
			seen[t.Symbol] = true
			bought = append(bought, t.Symbol)
		}
		if r.Closes != "" {
			closes[t.Symbol+" on "+fmtDay(r.Closes)] = true
		}
	}
	if len(bought) == 0 {
		return nil
	}
	when := "before the rights issue closes"
	if len(closes) > 0 {
		when = "before the rights issue closes (" + strings.Join(sortedKeys(closes), "; ") + ")"
	}
	return []string{fmt.Sprintf("IMPORTANT: you bought rights entitlements (REs): %s. An RE only gives you the right to apply for shares in the rights issue, or to sell (renounce) the right, %s. An RE you do not use by then lapses and becomes worthless.", strings.Join(bought, ", "), when)}
}

// ---- margin call close-outs ----

// marginCallRemarks marks the trades that closed a position because a
// margin call was not met.
func (o *Ops) marginCallRemarks(ctx context.Context, tenant, date, ucc string, trades []ingest.TradeEvent) []string {
	sq, err := docs.List[SquareOff](ctx, o.Docs, tenant, cSquareOff, date+"|")
	if err != nil || len(sq) == 0 {
		return nil
	}
	byNo := map[string]SquareOff{}
	byPos := map[string]SquareOff{}
	for _, s := range sq {
		if s.UCC != ucc {
			continue
		}
		if s.TradeNo != "" {
			byNo[s.Exchange+"|"+s.TradeNo] = s
		}
		byPos[s.Exchange+"|"+s.Segment+"|"+s.Instrument+"|"+s.Side] = s
	}
	got := map[string][]string{} // square-off ID -> trade numbers
	names := map[string]string{}
	for _, t := range trades {
		s, ok := byNo[t.Exchange+"|"+t.TradeNo]
		if !ok {
			s, ok = byPos[t.Exchange+"|"+t.Segment+"|"+t.Instrument()+"|"+t.Side]
		}
		if !ok {
			continue
		}
		got[s.ID] = append(got[s.ID], t.TradeNo)
		names[s.ID] = InstrumentName(t.Symbol, t.InstrumentType, t.Expiry, t.Strike) + " (" + t.Exchange + ")"
	}
	var out []string
	for _, id := range sortedKeys(got) {
		out = append(out, fmt.Sprintf("Trade %s in %s closed your position because the margin call was not met (square-off %s by the firm's risk team).", strings.Join(got[id], ", "), names[id], id))
	}
	return out
}

// ---- contract-wise summary at weighted average prices ----

// wapRow is one contract and side of the day at its weighted average price.
type wapRow struct {
	Security, Exchanges, Side string
	Qty                       int64
	WAP, BrokPU, NetRate      string
	Net                       money.Dec
}

// wapSummary averages the note's trades contract by contract and side:
// across NSE and BSE for equity, exchange by exchange for commodities.
func wapSummary(trades []ingest.TradeEvent, lines []noteLine) []wapRow {
	type acc struct {
		sec, side string
		exch      map[string]bool
		qty       int64
		val, brok money.Dec
	}
	by := map[string]*acc{}
	for i, t := range trades {
		name := InstrumentName(t.Symbol, t.InstrumentType, t.Expiry, t.Strike)
		k := name + "|" + t.Side
		if PartOf(t.Exchange, t.Segment) == PartCommodity {
			k += "|" + t.Exchange
		}
		a := by[k]
		if a == nil {
			a = &acc{sec: name, side: t.Side, exch: map[string]bool{}}
			by[k] = a
		}
		a.exch[t.Exchange] = true
		a.qty += t.Qty
		a.val = a.val.Add(t.Value())
		if i < len(lines) {
			a.brok = a.brok.Add(lines[i].Brok.Dec())
		}
	}
	var out []wapRow
	for _, k := range sortedKeys(by) {
		a := by[k]
		if a.qty == 0 {
			continue
		}
		q := money.DecInt(a.qty)
		wap, _ := a.val.Quo(q)
		bpu, _ := a.brok.Quo(q)
		r := wapRow{Security: a.sec, Exchanges: strings.Join(sortedKeys(a.exch), ", "), Side: "Buy", Qty: a.qty, WAP: wap.Fixed(4), BrokPU: bpu.Fixed(4)}
		if a.side == "B" {
			r.NetRate, r.Net = wap.Add(bpu).Fixed(4), a.val.Add(a.brok).Neg()
		} else {
			r.Side, r.NetRate, r.Net = "Sell", wap.Sub(bpu).Fixed(4), a.val.Sub(a.brok)
		}
		out = append(out, r)
	}
	return out
}

// mcxLastTrading is, by contract key, the MCX last trading date from the
// product master, where it is loaded.
func (o *Ops) mcxLastTrading(ctx context.Context, tenant string) map[string]string {
	m, _, err := docs.Get[MCXContractMaster](ctx, o.Docs, tenant, cMCXContracts, "master")
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, c := range m.Contracts {
		if c.LastTrade != "" {
			out[c.Key()] = c.LastTrade
		}
	}
	return out
}
