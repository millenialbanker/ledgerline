package ops

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- offers for sale (first version) ----
//
// An offer for sale (OFS) runs in the exchange's OFS window: non-retail
// bids on T, retail bids on T+1, each with 100% cash margin collected
// through the clearing corporation; allotted shares settle as a normal
// trade with a contract note. Unlike an IPO the broker is in the funds
// path, so:
//
//   - the margin is taken from the client's ledger when the bid is taken
//     (debit the client, credit PAYABLE:OFS_MARGIN, client money held as
//     margin) after checking the client's withdrawable balance; it moves
//     with a revision and comes back on cancellation or allocation;
//   - the bid is placed by a dealer in the OFS window of the trading
//     system and recorded as placed with its order number;
//   - on allocation the margin is released and the allotted shares are
//     billed as the day's exchange trade: from the clearing corporation's
//     trade file when it carries them, or, for an allocation the file does
//     not carry, added to the day's exchange trades (Ledgerline's own trade
//     number OFS-<bid>), so the day's billing posts the obligation and the
//     day's contract note includes it.
//
// Not covered yet: carrying unallotted non-retail bids to T+1, and
// reporting the margin held in the segment-wise client funds file (the
// client's ledger balance there is after the block).

// ofsHasFunds checks the client's free funds cover the margin (counting
// margin already held for this bid).
func (o *Ops) ofsHasFunds(ctx context.Context, tenant, ucc, date string, need, held money.Amount) error {
	w, err := o.Withdrawable(ctx, tenant, date, ucc)
	if err != nil {
		return err
	}
	if w.Blocked {
		return invalid("the client's free funds cannot be worked out (%s); an offer for sale needs 100%% cash margin", w.BlockedWhy)
	}
	if w.Available+held < need {
		return invalid("an offer for sale needs 100%% cash margin: %s has ₹%s free and the bid needs ₹%s", ucc, formatINR((w.Available + held).Dec()), formatINR(need.Dec()))
	}
	return nil
}

// ofsBlock takes the bid's margin from the client's ledger, replacing any
// margin held for an earlier version of the bid.
func (o *Ops) ofsBlock(ctx context.Context, tenant, actor string, a *IPOApp, iss IPOIssue, date string) error {
	if a.MarginEntry != "" {
		if _, _, err := o.App.Ledger.Reverse(ctx, tenant, a.MarginEntry, actor, "OFS bid "+a.ID+" revised", date); err != nil {
			return err
		}
		a.MarginEntry = ""
	}
	a.MarginRev++
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: fmt.Sprintf("OFSM:%s:%d", a.ID, a.MarginRev), ValueDate: date, CreatedBy: actor,
		Narration: fmt.Sprintf("OFS margin: %s bid %s, %d shares", iss.Name, a.ID, a.Lots*iss.LotSize), Source: ledger.SourceRef{Kind: "OFS-MARGIN", ID: a.ID},
		Postings: []ledger.Posting{{Account: ledger.ClientAccount(a.UCC), Debit: a.Margin, Memo: "OFS margin held: " + iss.Symbol}, {Account: PayableOFSMargin, Credit: a.Margin, Memo: a.UCC + " " + a.ID}}})
	if err != nil {
		return err
	}
	a.MarginEntry = e.ID
	return nil
}

// ofsRelease gives the bid's margin back to the client's ledger.
func (o *Ops) ofsRelease(ctx context.Context, tenant, actor string, a *IPOApp, date, why string) error {
	if a.MarginEntry == "" {
		return nil
	}
	if _, _, err := o.App.Ledger.Reverse(ctx, tenant, a.MarginEntry, actor, "OFS margin released: "+why, date); err != nil {
		return err
	}
	a.MarginEntry, a.UnblockedOn, a.Unblocked = "", date, a.Margin
	return nil
}

// OFSAllocation is one bid's allocation.
type OFSAllocation struct {
	Bid   string    `json:"bid"`
	Qty   int64     `json:"qty"` // shares allotted
	Price money.Dec `json:"price"`
}

// OFSAllocationResult says what an allocation did.
type OFSAllocationResult struct {
	Allotted    int      `json:"allotted"`
	NotAllotted int      `json:"not_allotted"`
	InFile      int      `json:"in_trade_file"`
	Added       int      `json:"added_to_trades"`
	Waiting     int      `json:"not_in_trade_file"`
	Released    int      `json:"margins_released"`
	Notes       []string `json:"notes,omitempty"`
}

// RecordOFSAllocation records the exchange's allocation for an offer for
// sale: margins are released, and each allotment is billed as the day's
// trade. With addTrades, an allotment the day's trade file does not carry
// is added to the day's exchange trades.
func (o *Ops) RecordOFSAllocation(ctx context.Context, tenant, actor, issueID, date string, allocs []OFSAllocation, addTrades bool) (OFSAllocationResult, error) {
	var res OFSAllocationResult
	if err := o.require(ctx, tenant, actor, "Recording allotments", RoleMaker, RoleChecker); err != nil {
		return res, err
	}
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return res, err
	}
	if iss.Type != IssueOFS {
		return res, invalid("%s is not an offer for sale", iss.Name)
	}
	if !validDate(date) || date < iss.Open {
		return res, invalid("give the allocation date (on or after %s)", iss.Open)
	}
	bids, err := o.issueBids(ctx, tenant, issueID)
	if err != nil {
		return res, err
	}
	by := map[string]OFSAllocation{}
	for _, al := range allocs {
		by[al.Bid] = al
	}
	trades, _ := o.exchangeTrades(ctx, tenant, date)
	inFile := map[string]int64{}
	for _, t := range trades {
		if t.Segment == "CM" && t.Side == "B" && strings.EqualFold(t.Symbol, iss.Symbol) && !strings.HasPrefix(t.TradeNo, "OFS-") {
			inFile[t.UCC] += t.Qty
		}
	}
	now := o.now()
	var add []ingest.TradeEvent
	for _, b := range bids {
		if b.Status != BidAccepted && b.Status != BidDraft && b.Status != BidUploaded {
			if _, ok := by[b.ID]; ok {
				res.Notes = append(res.Notes, fmt.Sprintf("%s is %s; allocation left out", b.ID, b.Status))
			}
			continue
		}
		al := by[b.ID]
		placed := b.Status == BidAccepted
		bidQty := b.Lots * iss.LotSize
		switch {
		case al.Qty < 0 || al.Qty > bidQty:
			return res, invalid("%s: allotted shares must be between 0 and the %d bid", b.ID, bidQty)
		case al.Qty > 0 && !placed:
			return res, invalid("%s was not recorded as placed in the OFS window; record it as placed first", b.ID)
		case al.Qty > 0 && al.Price.Cmp(iss.PriceLow) < 0:
			return res, invalid("%s: the allocation price must be at least the floor price ₹%s", b.ID, iss.PriceLow)
		}
		var a IPOApp
		_, err := docs.Update(ctx, o.Docs, tenant, cIPO, b.ID, actor, func(x *IPOApp) error {
			x.normalise()
			if err := o.ofsRelease(ctx, tenant, actor, x, date, "allocation of "+iss.Name); err != nil {
				return err
			}
			x.AllotDate, x.Shares, x.AllotPrice = date, al.Qty, al.Price
			x.Allotted = al.Qty / max(iss.LotSize, 1)
			switch {
			case !placed:
				x.Status, x.Allotment, x.Pending = BidRejected, "Not placed in the OFS window", ""
			case al.Qty > 0:
				x.Status, x.Allotment = BidAllotted, fmt.Sprintf("Allotted %d share(s) at ₹%s", al.Qty, al.Price)
			default:
				x.Status, x.Allotment = BidNotAllotted, "Not allotted · margin released"
			}
			if al.Qty > 0 {
				switch {
				case inFile[x.UCC] >= al.Qty:
					x.Obligation = "In the exchange trade file of " + date + ": billed with the day's trades"
				case addTrades:
					x.Obligation = "Added to the exchange trades of " + date + ": billed with the day's trades"
					ev := ingest.TradeEvent{Tenant: tenant, Origin: ingest.FromExchange, Exchange: firstOf(x.Exchange, iss.Exchanges[0]), Segment: "CM", TradeNo: "OFS-" + x.ID,
						OrderNo: firstOf(x.AppNo, x.ID), UCC: x.UCC, Symbol: iss.Symbol, InstrumentType: "EQ", Side: "B", Qty: al.Qty, Price: al.Price, TradeDate: date,
						TradeTime: "15:30:00", Product: "CNC", Terminal: "OFS-ALLOCATION", Settlement: "OFS"}
					om := ev
					om.Origin = ingest.FromOMS
					add = append(add, ev, om)
				default:
					x.Obligation = "Not in the trade file of " + date + " yet: load the day's trade file, or add the allocation to the day's trades"
				}
			}
			x.event(now, actor, "allocation: %s", x.Allotment)
			a = *x
			return nil
		})
		if err != nil {
			return res, err
		}
		if a.Unblocked > 0 {
			res.Released++
		}
		switch {
		case a.Status == BidAllotted:
			res.Allotted++
			switch {
			case strings.HasPrefix(a.Obligation, "In the"):
				res.InFile++
			case strings.HasPrefix(a.Obligation, "Added"):
				res.Added++
			default:
				res.Waiting++
			}
		default:
			res.NotAllotted++
		}
	}
	if len(add) > 0 {
		if _, _, err := o.App.Trades.Add(ctx, add); err != nil {
			return res, err
		}
		res.Notes = append(res.Notes, fmt.Sprintf("%d allotment(s) added to the exchange trades of %s: run (or re-run) billing for that day, then contract notes", len(add)/2, date))
	}
	o.log(ctx, tenant, actor, "ipo.ofs_allocated", iss.ID, map[string]any{"date": date, "allotted": res.Allotted, "added_to_trades": res.Added, "released": res.Released})
	return res, nil
}
