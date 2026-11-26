package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// Obligation is a client's net delivery obligation in one security.
type Obligation struct {
	Key      string    `json:"key"`
	UCC      string    `json:"ucc"`
	Name     string    `json:"name"`
	Exchange string    `json:"exchange"`
	ISIN     string    `json:"isin"`
	Symbol   string    `json:"symbol"`
	BuyQty   int64     `json:"buy_qty"`
	SellQty  int64     `json:"sell_qty"`
	NetQty   int64     `json:"net_qty"`
	Funds    money.Dec `json:"funds"` // + receivable by client
	Holding  int64     `json:"holding"`
	Free     int64     `json:"free"`
	Status   string    `json:"status"`
	Action   string    `json:"action,omitempty"` // what the user can do next
	// FromCUSPA is the part of a sale delivered from the client's invoked
	// CUSPA shares, blocked for early pay-in (cuspa.go).
	FromCUSPA int64 `json:"from_cuspa,omitempty"`
	// Clearing is the clearing corporation the obligation settles at
	// (Settings → Cleared through), or the exchange when another clearing
	// member clears it.
	Clearing string `json:"clearing,omitempty"`
	// Exchanges lists the exchanges netted into the obligation when there
	// is more than one: trades of exchanges cleared at the same clearing
	// corporation net per client and ISIN, as the clearing corporation
	// nets them (BSE trades cleared at NSE Clearing under
	// interoperability). Exchange is then the exchange of the larger side
	// in the direction of the net (where most of a net sale was sold).
	Exchanges []string `json:"exchanges,omitempty"`
}

type settleState struct {
	Status string `json:"status"`
	By     string `json:"by"`
	At     string `json:"at"`
	// On is the day it was done (for the pool pay-out check, dpctl.go).
	On string `json:"on,omitempty"`
}

// CUSPAItem is a client's unpaid purchase pledged to the client unpaid
// securities pledgee account (cuspa.go has the rules).
type CUSPAItem struct {
	ID       string    `json:"id"`
	Date     string    `json:"date"` // trade date
	UCC      string    `json:"ucc"`
	Exchange string    `json:"exchange,omitempty"`
	ISIN     string    `json:"isin"`
	Symbol   string    `json:"symbol"`
	Qty      int64     `json:"qty"`   // still held in CUSPA
	Value    money.Dec `json:"value"` // unpaid purchase value of the whole line
	Status   string    `json:"status"`
	By       string    `json:"by"`
	PayOut   string    `json:"pay_out,omitempty"` // the pay-out day (day 0)
	Released int64     `json:"released,omitempty"`
	Invoked  int64     `json:"invoked,omitempty"`
	Sold     int64     `json:"sold,omitempty"`
	// NoticeOn is the day the client was given notice before invocation.
	NoticeOn  string       `json:"notice_on,omitempty"`
	NoticeBy  string       `json:"notice_by,omitempty"`
	InvokedOn string       `json:"invoked_on,omitempty"`
	SoldOn    string       `json:"sold_on,omitempty"`
	Events    []CUSPAEvent `json:"events,omitempty"`
	// PSN is CDSL's pledge sequence number of the depository's automatic
	// pledge to the CUSPA, from CDSL's transaction export. With it and the
	// CDSL pledge API on, releases and invocations go to CDSL by the API
	// (cuspacdsl.go); CDSL is how the last one went there.
	PSN  string `json:"psn,omitempty"`
	CDSL string `json:"cdsl,omitempty"`
	// IntimatedAt is when the client was told of the pledge (debit,
	// obligation, shares, the right to sell; cuspa_more.go); Intimation how
	// the last attempt went.
	IntimatedAt string `json:"intimated_at,omitempty"`
	Intimation  string `json:"intimation,omitempty"`
	// Extensions are the requests to the depository to extend the pledge
	// (from 3 January 2027), the latest last.
	Extensions []CUSPAExtension `json:"extensions,omitempty"`
}

// Settlement is the settlement page for a trade date.
type Settlement struct {
	Date        string            `json:"date"`
	PayIn       string            `json:"pay_in"`
	Obligations []Obligation      `json:"obligations"`
	MTM         []SegmentMTM      `json:"mtm"`
	CUSPA       []CUSPAItem       `json:"cuspa"`
	Totals      map[string]string `json:"totals"`
}

// SegmentMTM is the derivatives money settlement per client and segment.
type SegmentMTM struct {
	UCC     string    `json:"ucc"`
	Name    string    `json:"name"`
	Segment string    `json:"segment"`
	Amount  money.Dec `json:"amount"`
}

// Obligations computes cash-market delivery obligations (CNC trades) and
// their settlement status.
func (o *Ops) Obligations(ctx context.Context, tenant, date string) (Settlement, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return Settlement{}, err
	}
	trades, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return Settlement{}, err
	}
	_, look, _ := o.Market(ctx, tenant, date)
	clients, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return Settlement{}, err
	}
	hs, err := o.Holdings(ctx, tenant, "")
	if err != nil {
		return Settlement{}, err
	}
	// Every one of the client's accounts together, in whole units (a
	// delivery is in whole shares; a fraction stays where it is).
	hold := holdingsByClient(hs)
	cuspaItems, err := docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
	if err != nil {
		return Settlement{}, err
	}
	early := cuspaEarlyPayIn(cuspaItems, date)
	free := map[string]int64{}
	for _, h := range hs {
		free[holdingID(h.UCC, h.ISIN)] += wholeUnits(h.Free())
	}
	states, err := docs.List[settleState](ctx, o.Docs, tenant, cSettle, date+"|")
	if err != nil {
		return Settlement{}, err
	}
	stDocs, _ := o.Docs.List(ctx, tenant, cSettle, date+"|")
	state := map[string]settleState{}
	for i, d := range stDocs {
		state[strings.TrimPrefix(d.ID, date+"|")] = states[i]
	}
	g, err := o.nettedObligations(ctx, tenant, set, trades, look, clients)
	if err != nil {
		return Settlement{}, err
	}
	// Equity obligations are funded from the equity ledger (a separate
	// commodity ledger's money is not used without a transfer).
	funds, err := o.equityFunds(ctx, tenant, date, "")
	if err != nil {
		return Settlement{}, err
	}
	out := Settlement{Date: date, PayIn: set.NextSettlementDay(date), Obligations: []Obligation{}, Totals: map[string]string{}}
	var payIn, payOut money.Dec
	for _, k := range sortedKeys(g) {
		ob := g[k]
		h := hold[holdingID(ob.UCC, ob.ISIN)]
		ob.Holding, ob.Free = wholeUnits(h.Qty), free[holdingID(ob.UCC, ob.ISIN)]
		switch {
		case ob.NetQty < 0:
			need := -ob.NetQty
			id := holdingID(ob.UCC, ob.ISIN)
			if c := min(early[id], need); c > 0 {
				early[id] -= c
				ob.FromCUSPA, need = c, need-c
			}
			switch {
			case need == 0:
				ob.Status = "Early pay-in (CUSPA)"
			case need > ob.Holding:
				ob.Status, ob.Action = "Short delivery", "report_short"
			case need > ob.Free:
				ob.Status, ob.Action = "Unpledge & pay-in", "unpledge_payin"
			default:
				ob.Status, ob.Action = "Ready for pay-in", "payin"
			}
			payOut = payOut.Add(ob.Funds)
		case ob.NetQty > 0:
			if funds[ob.UCC] < 0 {
				ob.Status, ob.Action = "Funds shortfall", "cuspa"
			} else {
				ob.Status, ob.Action = "Funded", "payout"
			}
			payIn = payIn.Add(ob.Funds.Neg())
		default:
			ob.Status = "Netted"
		}
		if s, ok := state[k]; ok {
			ob.Status, ob.Action = s.Status, ""
			if s.Status == "Held in CUSPA" {
				ob.Action = ""
			}
		}
		out.Obligations = append(out.Obligations, *ob)
	}
	out.Totals["funds_pay_in"] = payIn.Fixed(2)
	out.Totals["funds_pay_out"] = payOut.Fixed(2)
	out.Totals["net_to_cc"] = payIn.Sub(payOut).Fixed(2)
	// Derivatives MTM and premium from the posted bills.
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledgerFilterDate(date))
	if err != nil {
		return out, err
	}
	reversed := map[string]bool{}
	for _, e := range es {
		if e.ReversalOf != "" {
			reversed[e.ReversalOf] = true
		}
	}
	mtm := map[string]money.Amount{}
	for _, e := range es {
		if !derivKinds[e.Source.Kind] || reversed[e.ID] {
			continue
		}
		parts := strings.Split(e.Source.ID, "/")
		if len(parts) != 3 || strings.HasSuffix(parts[2], "-CM") {
			continue
		}
		for _, p := range e.Postings {
			if p.Memo == "Trade value / MTM" && ledger.IsClientAccount(p.Account) {
				mtm[parts[1]+"|"+parts[2]] += p.Credit - p.Debit
			}
		}
	}
	for _, k := range sortedKeys(mtm) {
		u, seg, _ := strings.Cut(k, "|")
		out.MTM = append(out.MTM, SegmentMTM{UCC: u, Name: clients[u].Name, Segment: seg, Amount: mtm[k].Dec()})
	}
	sort.SliceStable(out.MTM, func(i, j int) bool { return out.MTM[i].Amount.Cmp(out.MTM[j].Amount) < 0 })
	out.CUSPA = cuspaItems
	return out, nil
}

// SettleAction applies one settlement step to an obligation:
// payin, unpledge_payin, report_short, cuspa, payout.
func (o *Ops) SettleAction(ctx context.Context, tenant, actor, date, key, action string) (Obligation, error) {
	if err := o.require(ctx, tenant, actor, "Settlement actions", RoleMaker, RoleChecker); err != nil {
		return Obligation{}, err
	}
	st, err := o.Obligations(ctx, tenant, date)
	if err != nil {
		return Obligation{}, err
	}
	var ob *Obligation
	for i := range st.Obligations {
		if st.Obligations[i].Key == key {
			ob = &st.Obligations[i]
		}
	}
	if ob == nil {
		return Obligation{}, fmt.Errorf("%w: obligation %s", ErrNotFound, key)
	}
	if ob.Action == "" || ob.Action != action {
		return *ob, fmt.Errorf("%w: %s %s is %s; %q is not available", ErrState, ob.UCC, ob.Symbol, ob.Status, action)
	}
	if why := o.settleFrozen(ctx, tenant, *ob, action); why != "" {
		return *ob, fmt.Errorf("%w: %s", ErrState, why)
	}
	status := ""
	// Pay-ins come out of the client's accounts, the primary first; pay-outs
	// go to the account the client chose for pay-outs, else the primary.
	cl, _ := o.Client(ctx, tenant, ob.UCC)
	if cl.UCC == "" {
		cl.UCC = ob.UCC
	}
	switch action {
	case "payin":
		if err := o.debitAcross(ctx, tenant, actor, cl, ob.ISIN, ob.Symbol, -ob.NetQty-ob.FromCUSPA); err != nil {
			return *ob, err
		}
		status = "Paid in"
	case "unpledge_payin":
		need := -ob.NetQty - ob.FromCUSPA
		unpledge := need - ob.Free
		if err := o.unpledgeAcross(ctx, tenant, actor, cl, ob.ISIN, ob.Symbol, unpledge); err != nil {
			return *ob, err
		}
		// At the depository this is a pledge release for early pay-in
		// (PR-EP), never more than the clearing corporation's sale
		// obligation; the shares stay blocked for that pay-in in the
		// client's account (NSE/INSP/68373 para 4; CDSL Operating
		// Instructions 8.6.7-8.6.10). CDSL's API handbook here has no PR-EP
		// call, so it is given on CDSL's system.
		o.raiseWork(ctx, tenant, "settlement", "prep:"+date+":"+key, fmt.Sprintf("Release %d %s of %s for early pay-in (PR-EP)", unpledge, ob.Symbol, ob.UCC),
			fmt.Sprintf("%s sold %d %s on %s; %d of them are pledged to the firm as margin. Give a pledge release for early pay-in (PR-EP) on CDSL's system for %d, no more than the sale obligation, so they are blocked for the pay-in in the client's account. A client who may not trade (frozen code): invoke for early pay-in (IV-EP) through the clearing member instead.", ob.UCC, -ob.NetQty, ob.Symbol, fmtDay(date), unpledge, unpledge),
			ob.UCC, RoleChecker, "settlement", date)
		if err := o.debitAcross(ctx, tenant, actor, cl, ob.ISIN, ob.Symbol, need); err != nil {
			return *ob, err
		}
		status = "Paid in"
	case "report_short":
		// Deliver what is free; the remainder goes to auction.
		if ob.Free > 0 {
			if err := o.debitAcross(ctx, tenant, actor, cl, ob.ISIN, ob.Symbol, ob.Free); err != nil {
				return *ob, err
			}
		}
		if _, err := o.recordShortage(ctx, tenant, actor, date, st, *ob, -ob.NetQty-ob.FromCUSPA-max(ob.Free, 0)); err != nil {
			return *ob, err
		}
		status = "Short reported"
	case "cuspa":
		n, err := o.next(ctx, tenant, "cuspa", actor)
		if err != nil {
			return *ob, err
		}
		it := CUSPAItem{ID: fmt.Sprintf("CU-%05d", n), Date: date, UCC: ob.UCC, Exchange: ob.Exchange, ISIN: ob.ISIN, Symbol: ob.Symbol, Qty: ob.NetQty, Value: ob.Funds.Neg(),
			Status: CUSPAHeld, By: actor, PayOut: st.PayIn}
		it.Events = []CUSPAEvent{o.cuspaEvent(date, "Pledged", ob.NetQty, actor, "unpaid purchase, pay-out "+st.PayIn)}
		if _, err := docs.Put(ctx, o.Docs, tenant, cCUSPA, it.ID, it, 0, actor); err != nil {
			return *ob, err
		}
		// The client is told at once (debit, obligation, shares, the right
		// to sell); the daily review retries what did not go.
		if _, failed := o.cuspaIntimate(ctx, tenant, actor, date, []string{it.ID}); failed[it.UCC] != "" {
			o.raiseWork(ctx, tenant, "cuspa", it.UCC+":intimate", "Tell "+it.UCC+" that unpaid shares are pledged to the CUSPA",
				"The intimation after the pledge "+failed[it.UCC]+". Tell the client by email or SMS by the start of the next trading day and keep the record.", it.UCC, RoleChecker, "settlement", date)
		}
		status = "Held in CUSPA"
	case "payout":
		if _, err := o.adjustHoldingAt(ctx, tenant, actor, ob.UCC, holdingAcct(cl, payoutDemat(cl).ID), ob.ISIN, ob.Symbol, money.DecInt(ob.NetQty), money.Dec{}); err != nil {
			return *ob, err
		}
		status = "Paid out"
	default:
		return *ob, invalid("unknown settlement action %q", action)
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cSettle, date+"|"+key, settleState{Status: status, By: actor, At: o.now().Format("15:04"), On: o.now().In(ist).Format("2006-01-02")}, 0, actor); err != nil {
		return *ob, err
	}
	o.log(ctx, tenant, actor, "settlement."+action, key, map[string]any{"date": date, "qty": ob.NetQty, "status": status})
	ob.Status, ob.Action = status, ""
	return *ob, nil
}

// SettleAllReady applies every available pay-in and payout for a date.
func (o *Ops) SettleAllReady(ctx context.Context, tenant, actor, date string) (int, error) {
	st, err := o.Obligations(ctx, tenant, date)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ob := range st.Obligations {
		if ob.Action == "payin" || ob.Action == "payout" {
			if o.settleFrozen(ctx, tenant, ob, ob.Action) != "" {
				continue // frozen at CDSL: left for a person (cdslfreeze.go)
			}
			if _, err := o.SettleAction(ctx, tenant, actor, date, ob.Key, ob.Action); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// ReleaseCUSPA releases a CUSPA line's whole quantity to the client
// (CUSPABulk "release" for one line, on today's date).
func (o *Ops) ReleaseCUSPA(ctx context.Context, tenant, actor, id string) (CUSPAItem, error) {
	r, err := o.CUSPABulk(ctx, tenant, actor, CUSPABulkReq{Date: o.now().Format("2006-01-02"), Action: "release", IDs: []string{id}})
	if err != nil {
		return CUSPAItem{}, err
	}
	if len(r.Lines) == 1 && !r.Lines[0].OK {
		if r.Lines[0].Why == "no such CUSPA line" {
			return CUSPAItem{}, fmt.Errorf("%w: CUSPA line %s", ErrNotFound, id)
		}
		return CUSPAItem{}, fmt.Errorf("%w: %s: %s", ErrState, id, r.Lines[0].Why)
	}
	it, _, err := docs.Get[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, id)
	return it, err
}

// nettingGroup is what an exchange's cash market trades net with: the
// clearing corporation that clears the exchange's cash segment (Settings →
// Cleared through), so trades of two exchanges cleared at one clearing
// corporation net together (SEBI's interoperability framework; NSE
// Clearing's interoperability FAQ, netting Q1-Q2: obligations of trades on
// either exchange are netted per client and security). A segment cleared
// by another clearing member, or not traded, nets on its own exchange: the
// safe reading, since Ledgerline cannot know how the other member nets.
func (s Settings) nettingGroup(exch string) string {
	th := s.ClearedThrough(exch + "-CM")
	if th == "" || th == ClearNone || strings.HasPrefix(th, "CM:") {
		return exch
	}
	return th
}

// nettedObligations groups the day's delivery trades (cash market, not
// intraday) into one obligation per client, netting group and ISIN. A
// group with one exchange keeps the key UCC|EXCH|ISIN (as before netting
// across exchanges); a group netted across exchanges is keyed
// UCC|<clearing corporation>|ISIN.
func (o *Ops) nettedObligations(ctx context.Context, tenant string, set Settings, trades []ingest.TradeEvent, look Lookup, clients map[string]Client) (map[string]*Obligation, error) {
	type leg struct{ buy, sell int64 }
	type grp struct {
		ob   *Obligation
		legs map[string]*leg
	}
	gs := map[string]*grp{}
	for _, t := range trades {
		if t.Segment != "CM" || t.Product == "MIS" {
			continue
		}
		isin := ""
		if i, ok := look.ForTrade(t); ok {
			isin = i.ISIN
		}
		if isin == "" {
			isin = "UNKNOWN:" + t.Symbol
		}
		ng := set.nettingGroup(t.Exchange)
		k := t.UCC + "|" + ng + "|" + isin
		x := gs[k]
		if x == nil {
			x = &grp{ob: &Obligation{UCC: t.UCC, Name: clients[t.UCC].Name, Exchange: t.Exchange, ISIN: isin, Symbol: t.Symbol, Clearing: ng}, legs: map[string]*leg{}}
			gs[k] = x
		}
		l := x.legs[t.Exchange]
		if l == nil {
			l = &leg{}
			x.legs[t.Exchange] = l
		}
		ob := x.ob
		if t.Side == "B" {
			ob.BuyQty += t.Qty
			l.buy += t.Qty
			ob.Funds = ob.Funds.Sub(t.Value())
		} else {
			ob.SellQty += t.Qty
			l.sell += t.Qty
			ob.Funds = ob.Funds.Add(t.Value())
		}
		if t.Exchange == "NSE" {
			ob.Symbol = t.Symbol // NSE's symbol, when NSE trades are netted in
		}
	}
	out := map[string]*Obligation{}
	for _, x := range gs {
		ob := x.ob
		ob.NetQty = ob.BuyQty - ob.SellQty
		exs := sortedKeys(x.legs)
		if len(exs) == 1 {
			ob.Exchange = exs[0]
			ob.Key = ob.UCC + "|" + ob.Exchange + "|" + ob.ISIN
			out[ob.Key] = ob
			continue
		}
		// The exchange of the larger side in the direction of the net.
		best, most := exs[0], int64(-1)
		for _, e := range exs {
			q := x.legs[e].buy
			if ob.NetQty < 0 {
				q = x.legs[e].sell
			}
			if q > most {
				best, most = e, q
			}
		}
		ob.Exchange, ob.Exchanges = best, exs
		ob.Key = ob.UCC + "|" + ob.Clearing + "|" + ob.ISIN
		out[ob.Key] = ob
	}
	return out, nil
}

// sameNetting says whether two obligations settle together at one
// clearing corporation (or on one exchange).
func sameNetting(a, b Obligation) bool {
	if a.Clearing != "" && b.Clearing != "" {
		return a.Clearing == b.Clearing
	}
	return a.Exchange == b.Exchange
}

func ledgerFilterDate(date string) ledger.Filter { return ledger.Filter{From: date, To: date} }
