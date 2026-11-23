package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- NSE eIPO: tender offers (buy-back, open offer, delisting) ----
//
// NSE's tender master (GET tendermaster) gives each offer: series, bidding
// dates and times, lot size, minimum and maximum quantity, the price range
// and cut-off price, the registrar and the ISIN. A tender (POST tender/add,
// ER entry, MR modify, CR cancel) is the client's shares at a price, in a
// category, from a demat account (D) or as physical shares (P, folio and up
// to three certificates with their distinctive numbers). Status comes from
// tender/fetch (orderStatus, and the registrar's and custodian's status).
//
// Ledgerline checks the shares tendered are free in the demat account
// named, as it holds the client's holdings (the registrar checks the
// holding on the record date), less the client's other tenders in the
// offer. A buy-back or open offer is at the offer price; a delisting
// (reverse book building) takes a price in the offer's range. The protocol
// does not carry the early pay-in or block of the shares: once demat
// tenders are placed a work item asks the desk to confirm from the
// clearing corporation's or depository's report that the shares were
// blocked, and physical tenders to send the certificates to the registrar.
//
// The acceptance is not in the protocol either: an Ops checker records it
// from the registrar's basis of acceptance. As for a buy-back settled on
// the corporate actions screen (corporate.go), accepted demat shares leave
// the client's holding and the proceeds are credited to the client's
// ledger against the clearing corporation (CC:NSE-CM). Proceeds may be
// entered net of what the company deducted (a buy-back's tax).

// tenderCheck checks a tender and works out its price and value.
func (o *Ops) tenderCheck(ctx context.Context, tenant string, iss IPOIssue, c Client, x *EIPOOrder, in EIPOOrderInput) error {
	if len(iss.Categories) > 0 {
		code := strings.TrimSpace(in.Category)
		if code == "" {
			code = iss.Categories[0].Code
		}
		cat, ok := iss.category(code)
		if !ok {
			var cs []string
			for _, c := range iss.Categories {
				cs = append(cs, c.Code)
			}
			return invalid("choose a category of %s: %s", iss.Name, strings.Join(cs, ", "))
		}
		x.Category = cat.exch("NSE")
	}
	if err := qtyCheck(iss, x.Qty, "shares"); err != nil {
		return err
	}
	kind := productOr(iss).Kind
	price := iss.tenderPrice()
	if in.Price.Sign() > 0 {
		switch {
		case (kind == "Buy-back" || kind == "Open offer") && in.Price.Cmp(price) != 0:
			return invalid("a %s is at the offer price ₹%s", strings.ToLower(kind), price)
		case iss.PriceLow.Sign() > 0 && in.Price.Cmp(iss.PriceLow) < 0, iss.PriceHigh.Sign() > 0 && kind != "Delisting" && in.Price.Cmp(iss.PriceHigh) > 0:
			return invalid("the price must be within ₹%s to ₹%s", iss.PriceLow, firstDec(iss.PriceHigh, iss.PriceLow))
		}
		if iss.Tick.Sign() > 0 {
			if q, err := in.Price.Quo(iss.Tick); err != nil || q.Round(money.UnitRupee).Dec().Cmp(q) != 0 {
				return invalid("the price moves in steps of ₹%s", iss.Tick)
			}
		}
		price = in.Price
	}
	if price.Sign() <= 0 {
		return invalid("the offer has no price yet: set it on the offer")
	}
	x.Price = price
	x.Amount = price.MulInt(x.Qty).Round(money.UnitPaisa)
	x.Physical = in.Physical
	if x.Physical {
		x.Folio, x.Certs = strings.TrimSpace(in.Folio), nil
		for _, ct := range in.Certs {
			ct = TenderCert{No: strings.TrimSpace(ct.No), From: strings.TrimSpace(ct.From), To: strings.TrimSpace(ct.To)}
			if ct.No == "" && ct.From == "" && ct.To == "" {
				continue
			}
			if ct.No == "" || ct.From == "" || ct.To == "" || len(ct.No) > 16 || len(ct.From) > 16 || len(ct.To) > 16 {
				return invalid("each certificate needs its number and distinctive numbers from and to (at most 16 characters)")
			}
			x.Certs = append(x.Certs, ct)
		}
		switch {
		case x.Folio == "" || len(x.Folio) > 16:
			return invalid("give the folio number of the physical shares (at most 16 characters)")
		case len(x.Certs) == 0 || len(x.Certs) > 3:
			return invalid("give one to three share certificates (more go on another tender)")
		}
		x.Depository, x.DPID, x.BenID, x.Demat = "", "", "", ""
		return nil
	}
	if err := o.setDemat(ctx, tenant, x, c, in.Demat, false); err != nil {
		return err
	}
	if iss.ISIN == "" {
		return invalid("set the offer's ISIN first: the shares tendered are checked against the client's holding")
	}
	acct := holdingAcct(c, x.Demat)
	var free int64
	if hs, err := o.Holdings(ctx, tenant, c.UCC); err == nil {
		for _, h := range hs {
			if h.ISIN == iss.ISIN && normDematID(h.BOID) == acct {
				free += wholeUnits(h.Free())
			}
		}
	}
	all, _ := docs.List[EIPOOrder](ctx, o.Docs, tenant, cEIPOOrder, "")
	for _, y := range all {
		if y.IssueID == iss.ID && y.ID != x.ID && y.UCC == c.UCC && y.live() && !y.Physical && y.Demat == x.Demat && y.Status != OrderAccepted && y.Status != OrderPartly {
			free -= y.Qty
		}
	}
	if x.Qty > free {
		return invalid("%s has %d free %s share(s) in demat %s; %d cannot be tendered", c.UCC, max(free, 0), firstOf(iss.Symbol, iss.ISIN), x.Demat, x.Qty)
	}
	return nil
}

// tenderSentWork asks the desk to confirm the shares of placed tenders were
// blocked (demat) or sent to the registrar (physical).
func (o *Ops) tenderSentWork(ctx context.Context, tenant string, iss IPOIssue, now time.Time) {
	var demat, phys []string
	for _, x := range o.issueOrders(ctx, tenant, iss.ID) {
		if x.Status != OrderPlaced {
			continue
		}
		line := fmt.Sprintf("%s · %s %s · %d share(s)", x.ID, x.UCC, x.Name, x.Qty)
		if x.Physical {
			phys = append(phys, line+" · folio "+x.Folio)
		} else {
			demat = append(demat, line+" · "+x.Depository+" "+x.DPID+x.BenID)
		}
	}
	today := now.In(ist).Format("2006-01-02")
	if len(demat) > 0 {
		o.raiseWork(ctx, tenant, "ipo", "tender-block-"+iss.ID+"-"+today, fmt.Sprintf("%s: confirm the shares of %d demat tender(s) were blocked", iss.Name, len(demat)),
			"The exchange's process blocks (or takes as early pay-in) the shares tendered from each client's demat account. Check the clearing corporation's or depository's report: a tender whose shares were not blocked is not considered. An early pay-in for the offer through CDSL (Early pay-in, with the offer chosen) carries the trading member code (TMID) and must reach CDSL more than 10 minutes before the cut-off, when CDSL earmarks the balance (CDSL communiqué DP2026-815).\n"+strings.Join(demat, "\n"), "", RoleMaker, "ipo", today)
	}
	if len(phys) > 0 {
		o.raiseWork(ctx, tenant, "ipo", "tender-physical-"+iss.ID+"-"+today, fmt.Sprintf("%s: send %d physical tender(s) to the registrar", iss.Name, len(phys)),
			"Send the original share certificates and the documents the letter of offer asks for to the registrar ("+firstOf(iss.Registrar, "see the offer")+") within the offer's time.\n"+strings.Join(phys, "\n"), "", RoleMaker, "ipo", today)
	}
}

// TenderAcceptance is one tender's acceptance.
type TenderAcceptance struct {
	Order    string       `json:"order"`
	Qty      int64        `json:"qty"`                // shares accepted
	Proceeds money.Amount `json:"proceeds,omitempty"` // credited to the client; empty: shares × price
}

// TenderAcceptResult says what recording an acceptance did.
type TenderAcceptResult struct {
	Accepted    int          `json:"accepted"`
	Partly      int          `json:"partly"`
	NotAccepted int          `json:"not_accepted"`
	Posted      int          `json:"posted"`
	Proceeds    money.Amount `json:"proceeds"`
	Notes       []string     `json:"notes,omitempty"`
}

// RecordTenderAcceptance records the registrar's basis of acceptance for
// an offer's placed tenders (those left out are not accepted): accepted
// demat shares leave the client's holding and the proceeds are credited to
// the client's ledger against the clearing corporation, as a buy-back is
// settled. Physical tenders are recorded; their shares are not in a demat
// account Ledgerline holds.
func (o *Ops) RecordTenderAcceptance(ctx context.Context, tenant, actor, issueID, date string, price money.Dec, rows []TenderAcceptance) (TenderAcceptResult, error) {
	var res TenderAcceptResult
	if err := o.require(ctx, tenant, actor, "Settling a buy-back", RoleChecker); err != nil {
		return res, err
	}
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return res, err
	}
	if iss.Type != IssueTender {
		return res, invalid("%s is not a tender offer", iss.Name)
	}
	if !validDate(date) || date < iss.Close {
		return res, invalid("give the acceptance date (on or after the close, %s)", iss.Close)
	}
	by := map[string]TenderAcceptance{}
	for _, r := range rows {
		by[r.Order] = r
	}
	orders := o.issueOrders(ctx, tenant, iss.ID)
	for _, x := range orders {
		r, ok := by[x.ID]
		switch {
		case ok && x.Status != OrderPlaced:
			return res, invalid("%s is %s; only placed tenders are accepted", x.ID, x.Status)
		case x.Status != OrderPlaced:
			continue
		case r.Qty < 0 || r.Qty > x.Qty:
			return res, invalid("%s: accepted shares must be between 0 and the %d tendered", x.ID, x.Qty)
		case r.Proceeds < 0:
			return res, invalid("%s: proceeds cannot be negative", x.ID)
		}
	}
	now := o.now()
	var lines []string
	for _, x := range orders {
		if x.Status != OrderPlaced {
			continue
		}
		r := by[x.ID]
		px := firstDec(price, x.Price)
		gross := px.MulInt(r.Qty).Round(money.UnitPaisa)
		proceeds := gross
		if r.Proceeds > 0 {
			proceeds = r.Proceeds
		}
		if r.Proceeds > gross {
			return res, invalid("%s: proceeds ₹%s are more than %d share(s) at ₹%s", x.ID, formatINR(r.Proceeds.Dec()), r.Qty, px)
		}
		c, err := o.Client(ctx, tenant, x.UCC)
		if err != nil {
			return res, err
		}
		var entry string
		if r.Qty > 0 && !x.Physical {
			if _, err := o.adjustHoldingAt(ctx, tenant, actor, x.UCC, holdingAcct(c, x.Demat), iss.ISIN, iss.Symbol, money.DecInt(-r.Qty), money.Dec{}); err != nil {
				return res, err
			}
			e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "TND:" + x.ID, ValueDate: date, CreatedBy: actor,
				Narration: fmt.Sprintf("%s: %d of %d shares accepted at ₹%s", iss.Name, r.Qty, x.Qty, px), Source: ledger.SourceRef{Kind: "TENDER", ID: x.ID},
				Postings: []ledger.Posting{{Account: ledger.ClearingAccount("NSE", "CM"), Debit: proceeds}, {Account: ledger.ClientAccount(x.UCC), Credit: proceeds, Memo: "Tender proceeds: " + iss.Symbol}}})
			if err != nil {
				return res, err
			}
			entry = e.ID
			res.Posted++
			res.Proceeds += proceeds
		}
		var after EIPOOrder
		_, err = docs.Update(ctx, o.Docs, tenant, cEIPOOrder, x.ID, actor, func(y *EIPOOrder) error {
			y.AcceptedQty, y.AcceptedPrice, y.AcceptedOn, y.ProceedsEntry = r.Qty, px, date, entry
			if r.Qty > 0 {
				y.Proceeds = proceeds
			}
			switch {
			case r.Qty == 0:
				y.Status = OrderNotAccepted
			case r.Qty < y.Qty:
				y.Status = OrderPartly
			default:
				y.Status = OrderAccepted
			}
			note := ""
			if y.Physical && r.Qty > 0 {
				note = " (physical: no demat holding moved and nothing posted; follow the registrar's payment)"
			}
			y.event(now, actor, "acceptance: %d of %d share(s) at ₹%s%s", r.Qty, y.Qty, px, note)
			after = *y
			return nil
		})
		if err != nil {
			return res, err
		}
		switch after.Status {
		case OrderAccepted:
			res.Accepted++
		case OrderPartly:
			res.Partly++
		default:
			res.NotAccepted++
		}
		if after.Qty > after.AcceptedQty {
			lines = append(lines, fmt.Sprintf("%s · %s · %d share(s) not accepted: to come back to %s", after.ID, after.UCC, after.Qty-after.AcceptedQty, map[bool]string{true: "the client (registrar)", false: "demat " + after.Demat}[after.Physical]))
		}
		if after.Physical && after.AcceptedQty > 0 {
			lines = append(lines, fmt.Sprintf("%s · %s · physical: %d share(s) accepted; check the registrar's payment", after.ID, after.UCC, after.AcceptedQty))
		}
	}
	o.closeWork(ctx, tenant, actor, "ipo", "tender-accept-"+iss.ID, "acceptance recorded")
	if res.Posted > 0 || len(lines) > 0 {
		o.raiseWork(ctx, tenant, "ipo", "tender-settle-"+iss.ID, fmt.Sprintf("%s: check the payout of ₹%s and the return of shares not accepted", iss.Name, formatINR(res.Proceeds.Dec())),
			"Proceeds are credited to the clients' ledgers against the clearing corporation (CC:NSE-CM): match them with the clearing corporation's payout. Shares not accepted come back to the clients' demat accounts; check they did.\n"+strings.Join(lines, "\n"), "", RoleMaker, "ipo", date)
	}
	o.log(ctx, tenant, actor, "ipo.tender_accepted", iss.ID, map[string]any{"date": date, "accepted": res.Accepted, "partly": res.Partly, "not_accepted": res.NotAccepted, "proceeds": res.Proceeds.String()})
	return res, nil
}
