package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
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
}

type settleState struct {
	Status string `json:"status"`
	By     string `json:"by"`
	At     string `json:"at"`
}

// CUSPAItem is a client's unpaid purchase held in the client unpaid
// securities pledgee account.
type CUSPAItem struct {
	ID     string    `json:"id"`
	Date   string    `json:"date"`
	UCC    string    `json:"ucc"`
	ISIN   string    `json:"isin"`
	Symbol string    `json:"symbol"`
	Qty    int64     `json:"qty"`
	Value  money.Dec `json:"value"`
	Status string    `json:"status"` // Held in CUSPA, Released
	By     string    `json:"by"`
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
	hold := map[string]Holding{}
	for _, h := range hs {
		hold[holdingID(h.UCC, h.ISIN)] = h
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
	g := map[string]*Obligation{}
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
		k := t.UCC + "|" + t.Exchange + "|" + isin
		ob := g[k]
		if ob == nil {
			ob = &Obligation{Key: k, UCC: t.UCC, Name: clients[t.UCC].Name, Exchange: t.Exchange, ISIN: isin, Symbol: t.Symbol}
			g[k] = ob
		}
		if t.Side == "B" {
			ob.BuyQty += t.Qty
			ob.Funds = ob.Funds.Sub(t.Value())
		} else {
			ob.SellQty += t.Qty
			ob.Funds = ob.Funds.Add(t.Value())
		}
	}
	funds, err := o.clientFunds(ctx, tenant, date, "")
	if err != nil {
		return Settlement{}, err
	}
	out := Settlement{Date: date, PayIn: set.NextBusinessDay(date), Obligations: []Obligation{}, Totals: map[string]string{}}
	var payIn, payOut money.Dec
	for _, k := range sortedKeys(g) {
		ob := g[k]
		ob.NetQty = ob.BuyQty - ob.SellQty
		h := hold[holdingID(ob.UCC, ob.ISIN)]
		ob.Holding, ob.Free = h.Qty, h.Qty-h.Pledged
		switch {
		case ob.NetQty < 0:
			need := -ob.NetQty
			switch {
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
			if p.Memo == "Trade value / MTM" && strings.HasPrefix(p.Account, "CLIENT:") {
				mtm[parts[1]+"|"+parts[2]] += p.Credit - p.Debit
			}
		}
	}
	for _, k := range sortedKeys(mtm) {
		u, seg, _ := strings.Cut(k, "|")
		out.MTM = append(out.MTM, SegmentMTM{UCC: u, Name: clients[u].Name, Segment: seg, Amount: mtm[k].Dec()})
	}
	sort.SliceStable(out.MTM, func(i, j int) bool { return out.MTM[i].Amount.Cmp(out.MTM[j].Amount) < 0 })
	out.CUSPA, err = docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
	return out, err
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
	status := ""
	switch action {
	case "payin":
		if _, err := o.adjustHolding(ctx, tenant, actor, ob.UCC, ob.ISIN, ob.Symbol, ob.NetQty, 0); err != nil {
			return *ob, err
		}
		status = "Paid in"
	case "unpledge_payin":
		need := -ob.NetQty
		unpledge := need - ob.Free
		if _, err := o.adjustHolding(ctx, tenant, actor, ob.UCC, ob.ISIN, ob.Symbol, 0, -unpledge); err != nil {
			return *ob, err
		}
		if _, err := o.adjustHolding(ctx, tenant, actor, ob.UCC, ob.ISIN, ob.Symbol, ob.NetQty, 0); err != nil {
			return *ob, err
		}
		status = "Paid in"
	case "report_short":
		// Deliver what is free; the remainder goes to auction.
		if ob.Free > 0 {
			if _, err := o.adjustHolding(ctx, tenant, actor, ob.UCC, ob.ISIN, ob.Symbol, -ob.Free, 0); err != nil {
				return *ob, err
			}
		}
		if _, err := o.recordShortage(ctx, tenant, actor, date, st, *ob, -ob.NetQty-max(ob.Free, 0)); err != nil {
			return *ob, err
		}
		status = "Short reported"
	case "cuspa":
		n, err := o.next(ctx, tenant, "cuspa", actor)
		if err != nil {
			return *ob, err
		}
		it := CUSPAItem{ID: fmt.Sprintf("CU-%05d", n), Date: date, UCC: ob.UCC, ISIN: ob.ISIN, Symbol: ob.Symbol, Qty: ob.NetQty, Value: ob.Funds.Neg(), Status: "Held in CUSPA", By: actor}
		if _, err := docs.Put(ctx, o.Docs, tenant, cCUSPA, it.ID, it, 0, actor); err != nil {
			return *ob, err
		}
		status = "Held in CUSPA"
	case "payout":
		if _, err := o.adjustHolding(ctx, tenant, actor, ob.UCC, ob.ISIN, ob.Symbol, ob.NetQty, 0); err != nil {
			return *ob, err
		}
		status = "Paid out"
	default:
		return *ob, invalid("unknown settlement action %q", action)
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cSettle, date+"|"+key, settleState{Status: status, By: actor, At: o.now().Format("15:04")}, 0, actor); err != nil {
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
			if _, err := o.SettleAction(ctx, tenant, actor, date, ob.Key, ob.Action); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// ReleaseCUSPA releases unpaid securities to the client after funds arrive.
func (o *Ops) ReleaseCUSPA(ctx context.Context, tenant, actor, id string) (CUSPAItem, error) {
	if err := o.require(ctx, tenant, actor, "Releasing CUSPA securities", RoleMaker, RoleChecker); err != nil {
		return CUSPAItem{}, err
	}
	it, _, err := docs.Get[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, id)
	if err != nil {
		return it, err
	}
	if it.Status != "Held in CUSPA" {
		return it, fmt.Errorf("%w: %s is %s", ErrState, id, it.Status)
	}
	funds, err := o.clientFunds(ctx, tenant, "", "")
	if err != nil {
		return it, err
	}
	if funds[it.UCC] < 0 {
		return it, fmt.Errorf("%w: %s still owes %s; record the receipt first", ErrState, it.UCC, (-funds[it.UCC]).String())
	}
	if _, err := o.adjustHolding(ctx, tenant, actor, it.UCC, it.ISIN, it.Symbol, it.Qty, 0); err != nil {
		return it, err
	}
	it, err = docs.Update(ctx, o.Docs, tenant, cCUSPA, id, actor, func(x *CUSPAItem) error { x.Status = "Released"; return nil })
	if err == nil {
		o.log(ctx, tenant, actor, "cuspa.released", id, map[string]any{"ucc": it.UCC, "qty": it.Qty, "symbol": it.Symbol})
	}
	return it, err
}

func ledgerFilterDate(date string) ledger.Filter { return ledger.Filter{From: date, To: date} }
