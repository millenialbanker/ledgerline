package ops

import (
	"context"
	"fmt"
	"sort"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- short deliveries: internal shortage and exchange auction ----
//
// When a client sells shares they do not deliver, the shortfall is met in
// one of two ways:
//
//   - Internal shortage: another client of the firm bought the same scrip
//     for the same settlement, so the firm's net obligation with the
//     clearing corporation hides the shortfall. Obligations are netted as
//     the clearing corporation nets them, so a buyer on either exchange
//     cleared at NSE Clearing counts. NSE Clearing auctions it
//     (nclshortage.go): the buyer waits for the auctioned shares and is
//     credited the close-out price only for what the auction did not
//     deliver.
//   - Exchange auction: the clearing corporation buys the shares in its
//     auction (or closes out if the auction fails) and recovers the cost
//     from the firm, which debits the short seller.
//
// The suggested close-out price follows the exchanges' rule: the higher of
// the highest closing price from the trade date to today, and today's
// close plus 20%. It is only a suggestion: the rate NSE Clearing gives (its
// auction rate or close-out rate file) is what is entered.

const cShortage = "shortage"

// Shortage is one short delivery.
type Shortage struct {
	ID        string    `json:"id"`
	Date      string    `json:"date"` // trade date
	PayIn     string    `json:"pay_in"`
	UCC       string    `json:"ucc"`
	Name      string    `json:"name"`
	Exchange  string    `json:"exchange"`
	Clearing  string    `json:"clearing,omitempty"` // the netting group (settlement.go)
	ISIN      string    `json:"isin"`
	Symbol    string    `json:"symbol"`
	Qty       int64     `json:"qty"`
	SellPrice money.Dec `json:"sell_price"`
	// SettlePrice is the clearing corporation's settlement (closing)
	// price of the trade date: the short seller is never debited less
	// (no gain from a short delivery; nclshortage.go).
	SettlePrice money.Dec    `json:"settle_price"`
	Internal    int64        `json:"internal"`
	Auction     int64        `json:"auction"`
	Buyers      []ShortBuyer `json:"buyers"`
	Suggested   money.Dec    `json:"suggested"`
	Price       money.Dec    `json:"price"`
	Status      string       `json:"status"` // Open, Settled
	Debit       money.Amount `json:"debit,omitempty"`
	Loss        money.Dec    `json:"loss"` // (price − sell price) × qty, the seller's cost of the short
	By          string       `json:"by"`
	SettledBy   string       `json:"settled_by,omitempty"`
	// Received is how many shares the clearing corporation's auction
	// delivered (at AuctionPrice); the rest was closed out at Price.
	// Model is "auction" for a shortage finished that way
	// (SettleShortageWith); empty for one closed out the old way.
	Received     int64     `json:"received,omitempty"`
	AuctionPrice money.Dec `json:"auction_price"`
	Model        string    `json:"model,omitempty"`
}

// ShortBuyer is a client of the firm whose purchase the shortage hits.
type ShortBuyer struct {
	UCC    string       `json:"ucc"`
	Name   string       `json:"name"`
	Bought int64        `json:"bought"`
	Short  int64        `json:"short"`
	Key    string       `json:"key"`
	Credit money.Amount `json:"credit,omitempty"`
	// Received is the shares of the auction delivered to the buyer in
	// place of a cash close-out.
	Received int64 `json:"received,omitempty"`
}

// recordShortage is called when a short delivery is reported.
func (o *Ops) recordShortage(ctx context.Context, tenant, actor, date string, st Settlement, ob Obligation, short int64) (Shortage, error) {
	n, err := o.next(ctx, tenant, "shortage", actor)
	if err != nil {
		return Shortage{}, err
	}
	sh := Shortage{ID: fmt.Sprintf("SH-%05d", n), Date: date, PayIn: st.PayIn, UCC: ob.UCC, Name: ob.Name, Exchange: ob.Exchange, Clearing: ob.Clearing, ISIN: ob.ISIN, Symbol: ob.Symbol,
		Qty: short, Status: "Open", By: actor, Buyers: []ShortBuyer{}}
	if ob.SellQty > 0 && ob.NetQty < 0 {
		if p, err := ob.Funds.Quo(money.DecInt(-ob.NetQty)); err == nil {
			sh.SellPrice = p
		}
	}
	if _, look, err := o.Market(ctx, tenant, date); err == nil {
		if i, ok := look.ByISIN(ob.ISIN); ok {
			sh.SettlePrice = firstDec(i.Close, i.LTP)
		}
	}
	// A buyer's shares wait for the clearing corporation's auction when it
	// auctions internal shortages (a clearing corporation, not another
	// clearing member); otherwise the firm closes out.
	wait := "Awaiting close-out"
	if _, cc := ClearingCorpNames[ob.Clearing]; cc {
		wait = "Awaiting auction"
	}
	left := short
	for _, b := range st.Obligations {
		if left == 0 {
			break
		}
		if !sameNetting(b, ob) || b.ISIN != ob.ISIN || b.NetQty <= 0 || b.Action == "" {
			continue // only buyers whose shares have not been paid out yet
		}
		q := min(left, b.NetQty)
		sh.Buyers = append(sh.Buyers, ShortBuyer{UCC: b.UCC, Name: b.Name, Bought: b.NetQty, Short: q, Key: b.Key})
		sh.Internal += q
		left -= q
		if err := docs.Upsert(ctx, o.Docs, tenant, cSettle, date+"|"+b.Key, settleState{Status: wait, By: actor, At: o.now().Format("15:04")}, actor); err != nil {
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
