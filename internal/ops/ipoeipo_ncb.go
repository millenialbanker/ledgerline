package ops

import (
	"context"

	"ledgerline/internal/money"
)

// ---- NSE eIPO: non-competitive bids in G-secs, T-bills and SDLs (NCB) ----
//
// A retail investor bids an amount of face value at the auction's weighted
// average price (RBI's non-competitive bidding scheme, through the
// exchange as aggregator). NSE's NCB master (GET ncbmaster) gives each
// security's symbol, series, bidding dates and times, lot size, minimum
// bid and maximum quantity; Ledgerline sets the issue up from it
// (ipoeipo_prod.go). The bid (POST ncb/add) carries investmentValue, the
// rupees of face value: at least minBidQuantity, in multiples of lotSize,
// at most maxQuantity (the samples' ₹10,000 multiples to ₹2 crore match
// the scheme).
//
// The member collects the money and pays it in to NSCCL (clearingStatus FP
// fund pending, FS sent to NSCCL), so Ledgerline checks the client's free
// funds and holds the money on the ledger when the bid is taken, as for an
// offer for sale: the face value plus the issue's funds buffer (a dated
// security can cost more than its face value; 0% by default). When NSE
// gives the amount payable (totalAmountPayable, after the auction) the
// money held follows it. Rejected (RJ) or cancelled bids get it back.
//
// The protocol lists a price for ncb/add and its samples suggest paise per
// ₹100 of face value; a non-competitive bid carries no price, so Ledgerline
// sends none. Physical (non-demat) allotment is not offered: the flag is D.

// ncbCheck checks an NCB bid and works out the money to hold.
func (o *Ops) ncbCheck(ctx context.Context, tenant string, iss IPOIssue, c Client, x *EIPOOrder, in EIPOOrderInput) error {
	if err := qtyCheck(iss, x.Qty, "rupees of face value"); err != nil {
		return err
	}
	if in.Physical {
		return invalid("non-competitive bids through the exchange are allotted in demat only")
	}
	x.Physical, x.Price, x.PriceKind = false, money.Dec{}, ""
	if err := o.setDemat(ctx, tenant, x, c, in.Demat, true); err != nil {
		return err
	}
	pct := money.DecInt(100).Add(productOr(iss).FundsBuffer)
	amt, _ := money.DecInt(x.Qty).Mul(pct).Quo(money.DecInt(100))
	x.Amount = amt.Round(money.UnitPaisa)
	if x.ExchAmount > 0 && x.Qty == x.ExchQty {
		x.Amount = x.ExchAmount // NSE has given the amount payable for this bid
	}
	return nil
}
