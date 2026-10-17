package ops

import (
	"context"
	"fmt"
	"sort"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- short deliveries: internal shortage and exchange auction ----
//
// When a client sells shares they do not deliver, the shortfall is met in
// one of two ways:
//
//   - Internal shortage: another client of the firm bought the same scrip
//     for the same settlement, so the firm's net obligation with the
//     clearing corporation hides the shortfall. The firm closes it out
//     itself: the short seller is debited, and the buyer is credited, at
//     the close-out price instead of receiving the shares.
//   - Exchange auction: the clearing corporation buys the shares in its
//     auction (or closes out if the auction fails) and recovers the cost
//     from the firm, which debits the short seller.
//
// The suggested close-out price follows the exchanges' rule: the higher of
// the highest closing price from the trade date to today, and today's
// close plus 20%.

const cShortage = "shortage"

// Shortage is one short delivery.
type Shortage struct {
	ID        string       `json:"id"`
	Date      string       `json:"date"` // trade date
	PayIn     string       `json:"pay_in"`
	UCC       string       `json:"ucc"`
	Name      string       `json:"name"`
	Exchange  string       `json:"exchange"`
	ISIN      string       `json:"isin"`
	Symbol    string       `json:"symbol"`
	Qty       int64        `json:"qty"`
	SellPrice money.Dec    `json:"sell_price"`
	Internal  int64        `json:"internal"`
	Auction   int64        `json:"auction"`
	Buyers    []ShortBuyer `json:"buyers"`
	Suggested money.Dec    `json:"suggested"`
	Price     money.Dec    `json:"price"`
	Status    string       `json:"status"` // Open, Settled
	Debit     money.Amount `json:"debit,omitempty"`
	Loss      money.Dec    `json:"loss"` // (price − sell price) × qty, the seller's cost of the short
	By        string       `json:"by"`
	SettledBy string       `json:"settled_by,omitempty"`
}

// ShortBuyer is a client of the firm whose purchase the shortage hits.
type ShortBuyer struct {
	UCC    string       `json:"ucc"`
	Name   string       `json:"name"`
	Bought int64        `json:"bought"`
	Short  int64        `json:"short"`
	Key    string       `json:"key"`
	Credit money.Amount `json:"credit,omitempty"`
}

// recordShortage is called when a short delivery is reported.
func (o *Ops) recordShortage(ctx context.Context, tenant, actor, date string, st Settlement, ob Obligation, short int64) (Shortage, error) {
	n, err := o.next(ctx, tenant, "shortage", actor)
	if err != nil {
		return Shortage{}, err
	}
	sh := Shortage{ID: fmt.Sprintf("SH-%05d", n), Date: date, PayIn: st.PayIn, UCC: ob.UCC, Name: ob.Name, Exchange: ob.Exchange, ISIN: ob.ISIN, Symbol: ob.Symbol,
		Qty: short, Status: "Open", By: actor, Buyers: []ShortBuyer{}}
	if ob.SellQty > 0 && ob.NetQty < 0 {
		if p, err := ob.Funds.Quo(money.DecInt(-ob.NetQty)); err == nil {
			sh.SellPrice = p
		}
	}
	left := short
	for _, b := range st.Obligations {
		if left == 0 {
			break
		}
		if b.Exchange != ob.Exchange || b.ISIN != ob.ISIN || b.NetQty <= 0 || b.Action == "" {
			continue // only buyers whose shares have not been paid out yet
		}
		q := min(left, b.NetQty)
		sh.Buyers = append(sh.Buyers, ShortBuyer{UCC: b.UCC, Name: b.Name, Bought: b.NetQty, Short: q, Key: b.Key})
		sh.Internal += q
		left -= q
		if err := docs.Upsert(ctx, o.Docs, tenant, cSettle, date+"|"+b.Key, settleState{Status: "Awaiting close-out", By: actor, At: o.now().Format("15:04")}, actor); err != nil {
			return sh, err
		}
	}
	sh.Auction = left
	sh.Suggested = o.closeOutPrice(ctx, tenant, sh)
	if _, err := docs.Put(ctx, o.Docs, tenant, cShortage, sh.ID, sh, 0, actor); err != nil {
		return sh, err
	}
	o.log(ctx, tenant, actor, "shortage.recorded", sh.ID, map[string]any{"ucc": sh.UCC, "isin": sh.ISIN, "qty": short, "internal": sh.Internal, "auction": sh.Auction})
	return sh, nil
}

// closeOutPrice suggests the close-out price from the market data loaded
// from the trade date onwards.
func (o *Ops) closeOutPrice(ctx context.Context, tenant string, sh Shortage) money.Dec {
	ds, _ := o.Docs.List(ctx, tenant, cMarket, "")
	var dates []string
	for _, d := range ds {
		if d.ID >= sh.Date {
			dates = append(dates, d.ID)
		}
	}
	sort.Strings(dates)
	var hi, last money.Dec
	for _, d := range dates {
		m, _, err := docs.Get[Market](ctx, o.Docs, tenant, cMarket, d)
		if err != nil {
			continue
		}
		if i, ok := newLookup(m).ByISIN(sh.ISIN); ok {
			c := i.Close
			if c.IsZero() {
				c = i.LTP
			}
			hi = money.Max(hi, c)
			last = c
		}
	}
	return money.Max(hi, last.Pct(money.DecInt(120))).Round(money.UnitPaisa).Dec()
}

// Shortages lists short deliveries, newest first.
func (o *Ops) Shortages(ctx context.Context, tenant string) ([]Shortage, error) {
	ss, err := docs.List[Shortage](ctx, o.Docs, tenant, cShortage, "")
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].ID > ss[j].ID })
	return ss, err
}

// SettleShortage closes out a shortage at price: the internal part between
// the seller and the firm's buyers, the auction part against the clearing
// corporation. Buyers receive any shares of their purchase that were not
// short.
func (o *Ops) SettleShortage(ctx context.Context, tenant, actor, id, date string, price money.Dec) (Shortage, error) {
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
	if price.Sign() <= 0 || !ledger.ValidDate(date) {
		return sh, invalid("give the auction or close-out price and the date")
	}
	debit := price.MulInt(sh.Qty).Round(money.UnitPaisa)
	ps := []ledger.Posting{{Account: ledger.ClientAccount(sh.UCC), Debit: debit, Memo: fmt.Sprintf("Short delivery %s × %d at ₹%s", sh.Symbol, sh.Qty, price)}}
	var credited money.Amount
	for i, b := range sh.Buyers {
		amt := price.MulInt(b.Short).Round(money.UnitPaisa)
		sh.Buyers[i].Credit = amt
		credited += amt
		ps = append(ps, ledger.Posting{Account: ledger.ClientAccount(b.UCC), Credit: amt, Memo: fmt.Sprintf("Close-out of %d %s not received", b.Short, sh.Symbol)})
	}
	if rest := debit - credited; rest > 0 {
		ps = append(ps, ledger.Posting{Account: ledger.ClearingAccount(sh.Exchange, "CM"), Credit: rest, Memo: "Auction / close-out by the clearing corporation"})
	}
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "SHORT:" + sh.ID, ValueDate: date, CreatedBy: actor,
		Narration: fmt.Sprintf("Short delivery %s %s: %d internal close-out, %d exchange auction, at ₹%s", sh.ID, sh.Symbol, sh.Internal, sh.Auction, price),
		Source:    ledger.SourceRef{Kind: "SHORTAGE", ID: sh.ID}, Postings: ps}); err != nil {
		return sh, err
	}
	for _, b := range sh.Buyers {
		status := "Closed out"
		if deliver := b.Bought - b.Short; deliver > 0 {
			if _, err := o.adjustHolding(ctx, tenant, actor, b.UCC, sh.ISIN, sh.Symbol, deliver, 0); err != nil {
				return sh, err
			}
			status = fmt.Sprintf("Paid out %d; %d closed out", deliver, b.Short)
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cSettle, sh.Date+"|"+b.Key, settleState{Status: status, By: actor, At: o.now().Format("15:04")}, actor)
	}
	sh.Status, sh.Price, sh.Debit, sh.SettledBy = "Settled", price, debit, actor
	sh.Loss = price.Sub(sh.SellPrice).MulInt(sh.Qty)
	if err := docs.Upsert(ctx, o.Docs, tenant, cShortage, sh.ID, sh, actor); err != nil {
		return sh, err
	}
	o.log(ctx, tenant, actor, "shortage.settled", sh.ID, map[string]any{"price": price.String(), "debit": debit.String()})
	return sh, nil
}
