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

// ---- corporate actions ----
//
// Dividends, bonus issues and splits announced by listed companies.
//
//   - Bonus N:M gives N new shares for every M held on the record date. The
//     new shares cost nil and are acquired on the allotment date (for
//     capital gains). Fractions are paid in cash by the company and ignored.
//   - A split from face value M to N multiplies every holding and lot by
//     M/N and divides its cost per share by the same factor; the
//     acquisition date does not change.
//   - A dividend is paid by the company straight to holders on the record
//     date. Shares a client bought that were still with the broker on the
//     record date (held in CUSPA, or not yet paid out) earn a dividend the
//     broker receives and must pass on.
//
//   - A merger (scheme of amalgamation or arrangement) replaces shares of
//     the old ISIN with N shares of the new company's ISIN for every M
//     held. The cost and the acquisition date carry over (the new shares
//     are held from when the old ones were bought).
//   - A symbol or ISIN change renames the security one for one.
//   - A rights issue credits rights entitlements (a separate ISIN, at nil
//     cost) of N for every M held; the shares allotted later arrive through
//     the depository's holding file.
//
// Purchase lots used for capital gains replay corporate actions on their
// ex-date, so P&L and portfolio reports stay right across them.

const (
	cCorpAction = "corpaction"
	cBuyBack    = "buyback"
)

// CorpAction is one announced corporate action.
type CorpAction struct {
	ID         string         `json:"id"`
	ISIN       string         `json:"isin"`
	Symbol     string         `json:"symbol"`
	Type       string         `json:"type"`                 // Dividend, Bonus, Split, Merger, Symbol change, Rights
	NewISIN    string         `json:"new_isin,omitempty"`   // merger, symbol change: the new ISIN; rights: the entitlement's ISIN
	NewSymbol  string         `json:"new_symbol,omitempty"` // merger, symbol change, rights
	ExDate     string         `json:"ex_date"`
	RecordDate string         `json:"record_date"`
	PayDate    string         `json:"pay_date,omitempty"` // dividend payment or bonus allotment
	DPS        money.Dec      `json:"dps"`                // dividend per share
	RatioNew   int64          `json:"ratio_new"`          // bonus: new shares; split: new face value
	RatioOld   int64          `json:"ratio_old"`          // bonus: held; split: old face value
	Status     string         `json:"status"`             // Announced, Applied, Dividend received
	By         string         `json:"by"`
	AppliedBy  string         `json:"applied_by,omitempty"`
	Lines      []CorpEntitle  `json:"lines"`
	Received   money.Amount   `json:"received,omitempty"`
	Credited   money.Amount   `json:"credited,omitempty"`
	ReceiptRef string         `json:"receipt_ref,omitempty"`
	Summary    map[string]any `json:"summary,omitempty"`
	Recon      *DividendRecon `json:"recon,omitempty"`
}

// CorpEntitle is one client's entitlement.
type CorpEntitle struct {
	UCC        string       `json:"ucc"`
	Name       string       `json:"name"`
	Held       money.Dec    `json:"held"`        // shares in demat on the record date (all the client's accounts; exact)
	BrokerHeld int64        `json:"broker_held"` // shares with the broker (CUSPA)
	NewShares  money.Dec    `json:"new_shares"`  // bonus or split difference
	Dividend   money.Amount `json:"dividend"`    // on demat shares: paid by the company
	ViaBroker  money.Amount `json:"via_broker"`  // on broker-held shares: passed on by the broker
}

// DividendRecon compares dividend the broker received with what it passed on.
type DividendRecon struct {
	Expected money.Amount `json:"expected"`
	Received money.Amount `json:"received"`
	Credited money.Amount `json:"credited"`
	Excess   money.Amount `json:"excess"` // received but not owed to any client
}

func (c CorpAction) factor() (money.Dec, error) {
	if c.RatioNew <= 0 || c.RatioOld <= 0 {
		return money.Dec{}, invalid("ratio must be two positive whole numbers")
	}
	return money.DecInt(c.RatioOld).Quo(money.DecInt(c.RatioNew))
}

// allotDate is when bonus shares are acquired.
func (c CorpAction) allotDate() string {
	if c.PayDate != "" {
		return c.PayDate
	}
	return c.RecordDate
}

// CorpActions lists corporate actions with entitlements (computed live for
// announced ones).
func (o *Ops) CorpActions(ctx context.Context, tenant string) ([]CorpAction, error) {
	cs, err := docs.List[CorpAction](ctx, o.Docs, tenant, cCorpAction, "")
	if err != nil {
		return nil, err
	}
	for i := range cs {
		if cs[i].Status == "Announced" {
			cs[i].Lines, _ = o.entitlements(ctx, tenant, cs[i])
		}
		if cs[i].Lines == nil {
			cs[i].Lines = []CorpEntitle{}
		}
		if cs[i].Type == "Dividend" {
			var exp money.Amount
			for _, l := range cs[i].Lines {
				exp += l.ViaBroker
			}
			cs[i].Recon = &DividendRecon{Expected: exp, Received: cs[i].Received, Credited: cs[i].Credited, Excess: cs[i].Received - cs[i].Credited}
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].ExDate > cs[j].ExDate })
	return cs, nil
}

// SaveCorpAction announces a corporate action.
func (o *Ops) SaveCorpAction(ctx context.Context, tenant, actor string, c CorpAction) (CorpAction, error) {
	if err := o.require(ctx, tenant, actor, "Corporate actions", RoleMaker, RoleChecker); err != nil {
		return c, err
	}
	c.ISIN, c.Symbol = strings.ToUpper(strings.TrimSpace(c.ISIN)), strings.ToUpper(strings.TrimSpace(c.Symbol))
	switch {
	case c.ISIN == "":
		return c, invalid("ISIN is required")
	case !ledger.ValidDate(c.ExDate) || !ledger.ValidDate(c.RecordDate):
		return c, invalid("ex-date and record date are required (YYYY-MM-DD)")
	case c.PayDate != "" && !ledger.ValidDate(c.PayDate):
		return c, invalid("payment / allotment date must be YYYY-MM-DD")
	}
	switch c.Type {
	case "Dividend":
		if c.DPS.Sign() <= 0 {
			return c, invalid("dividend per share must be above zero")
		}
	case "Bonus", "Split":
		if _, err := c.factor(); err != nil {
			return c, err
		}
		if c.Type == "Split" && c.RatioNew >= c.RatioOld {
			return c, invalid("a split goes to a lower face value (e.g. 10 to 2)")
		}
	case "Merger", "Symbol change", "Rights":
		c.NewISIN, c.NewSymbol = strings.ToUpper(strings.TrimSpace(c.NewISIN)), strings.ToUpper(strings.TrimSpace(c.NewSymbol))
		if c.Type == "Symbol change" {
			c.RatioNew, c.RatioOld = 1, 1
			if c.NewISIN == "" {
				c.NewISIN = c.ISIN
			}
			if c.NewSymbol == "" && c.NewISIN == c.ISIN {
				return c, invalid("give the new symbol or the new ISIN")
			}
		}
		if _, err := c.factor(); err != nil {
			return c, err
		}
		if len(c.NewISIN) != 12 {
			return c, invalid("give the %s ISIN (12 characters)", map[string]string{"Rights": "rights entitlement's", "Merger": "new company's", "Symbol change": "new"}[c.Type])
		}
		if c.Type != "Symbol change" && c.NewISIN == c.ISIN {
			return c, invalid("the new ISIN must differ from the old one")
		}
		if c.NewSymbol == "" {
			c.NewSymbol = c.Symbol
			if c.Type == "Rights" {
				c.NewSymbol = c.Symbol + "-RE"
			}
		}
	default:
		return c, invalid("type must be Dividend, Bonus, Split, Merger, Symbol change or Rights")
	}
	all, _ := docs.List[CorpAction](ctx, o.Docs, tenant, cCorpAction, "")
	for _, x := range all {
		if x.ISIN == c.ISIN && x.Type == c.Type && x.ExDate == c.ExDate {
			return c, invalid("%s %s with ex-date %s is already recorded as %s", c.Symbol, c.Type, c.ExDate, x.ID)
		}
	}
	n, err := o.next(ctx, tenant, "corpaction", actor)
	if err != nil {
		return c, err
	}
	c.ID, c.Status, c.By, c.Lines = fmt.Sprintf("CA-%04d", n), "Announced", actor, nil
	if _, err := docs.Put(ctx, o.Docs, tenant, cCorpAction, c.ID, c, 0, actor); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "corpaction.announced", c.ID, map[string]any{"isin": c.ISIN, "type": c.Type, "ex_date": c.ExDate})
	return c, nil
}

// entitlements works out each holder's entitlement from current demat
// holdings and broker-held (CUSPA) shares bought on or before the record
// date's trade date.
func (o *Ops) entitlements(ctx context.Context, tenant string, c CorpAction) ([]CorpEntitle, error) {
	hs, err := o.Holdings(ctx, tenant, "")
	if err != nil {
		return nil, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	by := map[string]*CorpEntitle{}
	get := func(u string) *CorpEntitle {
		if by[u] == nil {
			by[u] = &CorpEntitle{UCC: u, Name: cs[u].Name}
		}
		return by[u]
	}
	// Per client: every one of the client's demat accounts together.
	for _, h := range hs {
		if h.ISIN == c.ISIN && h.Qty.Sign() > 0 {
			e := get(h.UCC)
			e.Held = e.Held.Add(h.Qty)
		}
	}
	cu, _ := docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
	for _, it := range cu {
		if it.ISIN == c.ISIN && it.Status == "Held in CUSPA" && it.Date < c.RecordDate {
			get(it.UCC).BrokerHeld += it.Qty
		}
	}
	f, _ := c.factor()
	var out []CorpEntitle
	for _, u := range sortedKeys(by) {
		e := by[u]
		tot := e.Held.Add(money.DecInt(e.BrokerHeld))
		switch c.Type {
		case "Dividend":
			e.Dividend = c.DPS.Mul(e.Held).Round(money.UnitPaisa)
			e.ViaBroker = c.DPS.MulInt(e.BrokerHeld).Round(money.UnitPaisa)
		case "Bonus":
			e.NewShares = ratioQty(tot, c.RatioNew, c.RatioOld)
		case "Split":
			e.NewShares = splitQty(tot, f).Sub(tot)
		case "Merger", "Symbol change", "Rights":
			e.NewShares = ratioQty(tot, c.RatioNew, c.RatioOld) // shares of the new ISIN (rights: entitlements)
		}
		out = append(out, *e)
	}
	return out, nil
}

// ApplyCorpAction applies a bonus or split to demat holdings (and CUSPA
// items), or freezes the dividend entitlements. A checker applies it.
func (o *Ops) ApplyCorpAction(ctx context.Context, tenant, actor, id string) (CorpAction, error) {
	if err := o.require(ctx, tenant, actor, "Applying corporate actions", RoleChecker); err != nil {
		return CorpAction{}, err
	}
	c, _, err := docs.Get[CorpAction](ctx, o.Docs, tenant, cCorpAction, id)
	if err != nil {
		return c, err
	}
	if c.Status != "Announced" {
		return c, fmt.Errorf("%w: %s is %s", ErrState, id, c.Status)
	}
	lines, err := o.entitlements(ctx, tenant, c)
	if err != nil {
		return c, err
	}
	f, _ := c.factor()
	if c.Type == "Merger" || c.Type == "Symbol change" || c.Type == "Rights" {
		if err := o.applyISINChange(ctx, tenant, actor, c); err != nil {
			return c, err
		}
	} else if c.Type != "Dividend" {
		// Each account's holding in its own account.
		hs, _ := o.Holdings(ctx, tenant, "")
		for _, h := range hs {
			if h.ISIN != c.ISIN || h.Qty.Sign() <= 0 {
				continue
			}
			var add, addPl money.Dec
			avg := h.AvgCost
			if c.Type == "Bonus" {
				add = ratioQty(h.Qty, c.RatioNew, c.RatioOld)
				// Average cost spreads over the larger holding.
				if q, err := h.AvgCost.Mul(h.Qty).Quo(h.Qty.Add(add)); err == nil {
					avg = q
				}
			} else {
				add = splitQty(h.Qty, f).Sub(h.Qty)
				addPl = splitQty(h.Pledged, f).Sub(h.Pledged)
				if q, err := h.AvgCost.Quo(f); err == nil {
					avg = q
				}
			}
			if _, err := docs.Update(ctx, o.Docs, tenant, cHolding, holdingIDAt(h.UCC, h.ISIN, h.BOID), actor, func(x *Holding) error {
				x.Qty = x.Qty.Add(add)
				x.Pledged = x.Pledged.Add(addPl)
				x.AvgCost = avg
				return nil
			}); err != nil {
				return c, err
			}
		}
		cu, _ := docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
		for _, it := range cu {
			if it.ISIN != c.ISIN || it.Status != "Held in CUSPA" || it.Date >= c.RecordDate {
				continue
			}
			add := it.Qty * c.RatioNew / c.RatioOld
			if c.Type == "Split" {
				add = money.DecInt(it.Qty).Mul(f).Round(money.UnitRupee).Paise()/100 - it.Qty
			}
			_, _ = docs.Update(ctx, o.Docs, tenant, cCUSPA, it.ID, actor, func(x *CUSPAItem) error { x.Qty += add; return nil })
		}
	}
	c, err = docs.Update(ctx, o.Docs, tenant, cCorpAction, id, actor, func(x *CorpAction) error {
		x.Status, x.AppliedBy, x.Lines = "Applied", actor, lines
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "corpaction.applied", id, map[string]any{"type": c.Type, "isin": c.ISIN, "clients": len(lines)})
	}
	return c, err
}

// ReceiveDividend records the dividend the broker received on broker-held
// shares and credits each entitled client. Anything received above what
// clients are owed is parked in bank suspense for investigation.
func (o *Ops) ReceiveDividend(ctx context.Context, tenant, actor, id, date, ref string, received money.Amount) (CorpAction, error) {
	if err := o.require(ctx, tenant, actor, "Crediting dividends", RoleChecker); err != nil {
		return CorpAction{}, err
	}
	c, _, err := docs.Get[CorpAction](ctx, o.Docs, tenant, cCorpAction, id)
	if err != nil {
		return c, err
	}
	if c.Type != "Dividend" || c.Status != "Applied" {
		return c, fmt.Errorf("%w: apply the dividend first (it is %s)", ErrState, c.Status)
	}
	var owed money.Amount
	for _, l := range c.Lines {
		owed += l.ViaBroker
	}
	if received < owed {
		return c, invalid("₹%s received is less than the ₹%s owed to clients on broker-held shares; check with the registrar before crediting", formatINR(received.Dec()), formatINR(owed.Dec()))
	}
	if strings.TrimSpace(ref) == "" {
		return c, invalid("give the bank credit reference")
	}
	ps := []ledger.Posting{{Account: ledger.BankClient, Debit: received, Memo: "Dividend " + c.Symbol + " " + ref}}
	for _, l := range c.Lines {
		if l.ViaBroker > 0 {
			ps = append(ps, ledger.Posting{Account: ledger.ClientAccount(l.UCC), Credit: l.ViaBroker, Memo: fmt.Sprintf("Dividend %s ₹%s × %d", c.Symbol, c.DPS, l.BrokerHeld)})
		}
	}
	if received > owed {
		ps = append(ps, ledger.Posting{Account: ledger.SuspenseBank, Credit: received - owed, Memo: "Dividend received in excess"})
	}
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "DIV:" + c.ID, ValueDate: date, CreatedBy: actor,
		Narration: fmt.Sprintf("Dividend %s ₹%s per share (record date %s) on broker-held shares", c.Symbol, c.DPS, c.RecordDate),
		Source:    ledger.SourceRef{Kind: "DIVIDEND", ID: c.ID}, Postings: ps}); err != nil {
		return c, err
	}
	c, err = docs.Update(ctx, o.Docs, tenant, cCorpAction, id, actor, func(x *CorpAction) error {
		x.Status, x.Received, x.Credited, x.ReceiptRef = "Dividend received", received, owed, ref
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "dividend.credited", id, map[string]any{"received": received.String(), "credited": owed.String()})
	}
	return c, err
}

// applyCorpLots adjusts open purchase lots for corporate actions with
// ex-date d. Used while replaying a client's trades.
func applyCorpLots(open []Lot, cas []CorpAction, d string) []Lot {
	for _, c := range cas {
		if c.ExDate != d || c.Type == "Dividend" {
			continue
		}
		if c.Type == "Merger" || c.Type == "Symbol change" || c.Type == "Rights" {
			open = isinChangeLots(open, c)
			continue
		}
		f, err := c.factor()
		if err != nil {
			continue
		}
		var held money.Dec
		sym := c.Symbol
		for i := range open {
			if open[i].ISIN != c.ISIN {
				continue
			}
			held = held.Add(open[i].Qty)
			sym = open[i].Symbol
			if c.Type == "Split" {
				open[i].Qty = splitQty(open[i].Qty, f)
				if q, err := open[i].Price.Quo(f); err == nil {
					open[i].Price = q
				}
			}
		}
		if c.Type == "Bonus" && held.Sign() > 0 {
			if n := ratioQty(held, c.RatioNew, c.RatioOld); n.Sign() > 0 {
				open = append(open, Lot{ISIN: c.ISIN, Symbol: sym, Qty: n, Date: c.allotDate(), Price: money.Dec{}})
			}
		}
	}
	return open
}

// isinChangeLots replays a merger, symbol change or rights issue on lots.
func isinChangeLots(open []Lot, c CorpAction) []Lot {
	var held, given money.Dec
	var idx []int
	for i := range open {
		if open[i].ISIN == c.ISIN && open[i].Qty.Sign() > 0 {
			held = held.Add(open[i].Qty)
			idx = append(idx, i)
		}
	}
	if c.Type == "Rights" {
		if n := ratioQty(held, c.RatioNew, c.RatioOld); n.Sign() > 0 {
			open = append(open, Lot{ISIN: c.NewISIN, Symbol: c.NewSymbol, Qty: n, Date: c.RecordDate})
		}
		return open
	}
	// New shares on the whole holding (as the company allots them), spread
	// over the lots; each lot's cost and date carry over.
	total := ratioQty(held, c.RatioNew, c.RatioOld)
	newQ := make([]money.Dec, len(idx))
	for k, i := range idx {
		newQ[k] = ratioQty(open[i].Qty, c.RatioNew, c.RatioOld)
		given = given.Add(newQ[k])
	}
	one := money.DecInt(1)
	for k := 0; given.Add(one).Cmp(total) <= 0 && k < len(idx); k++ {
		newQ[k] = newQ[k].Add(one)
		given = given.Add(one)
	}
	for k, i := range idx {
		l := &open[i]
		cost := l.Price.Mul(l.Qty)
		l.ISIN, l.Symbol, l.Qty = c.NewISIN, c.NewSymbol, newQ[k]
		if newQ[k].Sign() > 0 {
			l.Price, _ = cost.Quo(newQ[k])
		}
	}
	return open
}

// applyISINChange moves demat holdings and CUSPA items to the new ISIN
// (merger, symbol change) or credits rights entitlements.
func (o *Ops) applyISINChange(ctx context.Context, tenant, actor string, c CorpAction) error {
	hs, err := o.Holdings(ctx, tenant, "")
	if err != nil {
		return err
	}
	// Each account's holding moves within its own account.
	for _, h := range hs {
		if h.ISIN != c.ISIN || h.Qty.Sign() <= 0 {
			continue
		}
		q := ratioQty(h.Qty, c.RatioNew, c.RatioOld)
		pl := ratioQty(h.Pledged, c.RatioNew, c.RatioOld)
		if c.Type == "Rights" {
			if q.Sign() > 0 {
				if _, err := o.adjustHoldingAt(ctx, tenant, actor, h.UCC, h.BOID, c.NewISIN, c.NewSymbol, q, money.Dec{}); err != nil {
					return err
				}
			}
			continue
		}
		cost := h.AvgCost.Mul(h.Qty)
		if _, err := docs.Update(ctx, o.Docs, tenant, cHolding, holdingIDAt(h.UCC, h.ISIN, h.BOID), actor, func(x *Holding) error {
			x.Qty, x.Pledged = money.Dec{}, money.Dec{} // the old ISIN is extinguished
			return nil
		}); err != nil {
			return err
		}
		if q.Sign() <= 0 {
			continue
		}
		nk := holdingIDAt(h.UCC, c.NewISIN, h.BOID)
		nh, _, gerr := docs.Get[Holding](ctx, o.Docs, tenant, cHolding, nk)
		if gerr != nil {
			nh = Holding{UCC: h.UCC, ISIN: c.NewISIN, Symbol: c.NewSymbol, BOID: h.BOID}
		}
		tc := nh.AvgCost.Mul(nh.Qty).Add(cost)
		nh.Qty = nh.Qty.Add(q)
		nh.Pledged = nh.Pledged.Add(pl)
		nh.Symbol = c.NewSymbol
		nh.AvgCost, _ = tc.Quo(nh.Qty)
		if err := docs.Upsert(ctx, o.Docs, tenant, cHolding, nk, nh, actor); err != nil {
			return err
		}
	}
	if c.Type == "Rights" {
		return nil
	}
	cu, _ := docs.List[CUSPAItem](ctx, o.Docs, tenant, cCUSPA, "")
	for _, it := range cu {
		if it.ISIN != c.ISIN || it.Status != "Held in CUSPA" {
			continue
		}
		_, _ = docs.Update(ctx, o.Docs, tenant, cCUSPA, it.ID, actor, func(x *CUSPAItem) error {
			x.ISIN, x.Symbol, x.Qty = c.NewISIN, c.NewSymbol, x.Qty*c.RatioNew/c.RatioOld
			return nil
		})
	}
	return nil
}

// ---- IPO allotments ----

// IPOAllotmentResult reports posted allotments.
type IPOAllotmentResult struct {
	Issue   string `json:"issue"`
	Posted  int    `json:"posted"`
	Shares  int64  `json:"shares"`
	Skipped int    `json:"skipped"` // already posted
}

// PostIPOAllotments records the shares allotted in an IPO as purchases at
// the issue price on the allotment date, so the clients' holdings have a
// cost and an acquisition date for capital gains. The shares themselves
// arrive in the demat account through the depository's holding file.
func (o *Ops) PostIPOAllotments(ctx context.Context, tenant, actor, issue, isin, symbol, date string, price money.Dec, lotSize int64) (IPOAllotmentResult, error) {
	res := IPOAllotmentResult{Issue: issue}
	if err := o.require(ctx, tenant, actor, "Posting IPO allotments", RoleMaker, RoleChecker); err != nil {
		return res, err
	}
	isin, symbol = strings.ToUpper(strings.TrimSpace(isin)), strings.ToUpper(strings.TrimSpace(symbol))
	switch {
	case strings.TrimSpace(issue) == "":
		return res, invalid("choose the issue")
	case len(isin) != 12:
		return res, invalid("give the ISIN of the shares allotted")
	case !ledger.ValidDate(date):
		return res, invalid("give the allotment date (YYYY-MM-DD)")
	case price.Sign() <= 0:
		return res, invalid("give the issue price")
	case lotSize <= 0:
		return res, invalid("give the number of shares in a lot")
	}
	apps, err := o.IPOApps(ctx, tenant)
	if err != nil {
		return res, err
	}
	for _, a := range apps {
		if a.Issue != issue || a.Allotted <= 0 {
			continue
		}
		key := strings.ToUpper(a.UCC) + "|IPO-" + a.ID
		if _, err := o.Docs.Get(ctx, tenant, cExtTrd, key); err == nil {
			res.Skipped++
			continue
		}
		t := ExternalTrade{ID: "IPO-" + a.ID, UCC: strings.ToUpper(a.UCC), Date: date, ISIN: isin, Symbol: symbol, Side: "B", Qty: a.Allotted * lotSize, Price: price, Broker: "IPO allotment: " + issue, By: actor}
		if _, err := docs.Put(ctx, o.Docs, tenant, cExtTrd, key, t, 0, actor); err != nil {
			return res, err
		}
		res.Posted++
		res.Shares += t.Qty
	}
	if res.Posted == 0 && res.Skipped == 0 {
		return res, invalid("no allotment is recorded for %s: load the allotment file first", issue)
	}
	o.log(ctx, tenant, actor, "ipo.allotment_posted", issue, map[string]any{"posted": res.Posted, "shares": res.Shares, "isin": isin})
	return res, nil
}

// ---- buy-back tender offers ----

// BuyBack is a company's tender offer to buy back shares through the
// exchange's acquisition window.
type BuyBack struct {
	ID      string    `json:"id"`
	ISIN    string    `json:"isin"`
	Symbol  string    `json:"symbol"`
	Company string    `json:"company"`
	Price   money.Dec `json:"price"`
	Record  string    `json:"record_date"`
	Open    string    `json:"open"`
	Close   string    `json:"close"`
	Status  string    `json:"status"` // Open, Settled
	// Entitlement ratios from the letter of offer, "a/b": a shares for every b held.
	SmallRatio   string       `json:"small_ratio,omitempty"`   // reserved category (holding up to ₹2 lakh on the record date)
	GeneralRatio string       `json:"general_ratio,omitempty"` // general category
	Bids         []BuyBackBid `json:"bids"`
	By           string       `json:"by"`
}

// BuyBackBid is a client's tender.
type BuyBackBid struct {
	UCC      string       `json:"ucc"`
	Name     string       `json:"name"`
	Qty      int64        `json:"qty"`
	Date     string       `json:"date"`
	By       string       `json:"by"`
	Status   string       `json:"status"` // Tendered, Cancelled, Accepted, Partly accepted, Not accepted
	Accepted int64        `json:"accepted"`
	Amount   money.Amount `json:"amount,omitempty"`
}

// BuyBacks lists offers, newest first.
func (o *Ops) BuyBacks(ctx context.Context, tenant string) ([]BuyBack, error) {
	bs, err := docs.List[BuyBack](ctx, o.Docs, tenant, cBuyBack, "")
	for i := range bs {
		if bs[i].Bids == nil {
			bs[i].Bids = []BuyBackBid{}
		}
	}
	sort.SliceStable(bs, func(i, j int) bool { return bs[i].Open > bs[j].Open })
	return bs, err
}

// SaveBuyBack records a buy-back offer.
func (o *Ops) SaveBuyBack(ctx context.Context, tenant, actor string, b BuyBack) (BuyBack, error) {
	if err := o.require(ctx, tenant, actor, "Buy-back offers", RoleMaker, RoleChecker); err != nil {
		return b, err
	}
	b.ISIN, b.Symbol = strings.ToUpper(strings.TrimSpace(b.ISIN)), strings.ToUpper(strings.TrimSpace(b.Symbol))
	if b.ISIN == "" || b.Price.Sign() <= 0 || !ledger.ValidDate(b.Open) || !ledger.ValidDate(b.Close) || b.Close < b.Open {
		return b, invalid("ISIN, a price above zero and the tender period (open on or before close) are required")
	}
	for _, r := range []string{b.SmallRatio, b.GeneralRatio} {
		if strings.TrimSpace(r) != "" && entitlement(1000000, r) == 0 {
			return b, invalid("entitlement ratios are written a/b, e.g. 13/100 (13 shares for every 100 held)")
		}
	}
	n, err := o.next(ctx, tenant, "buyback", actor)
	if err != nil {
		return b, err
	}
	b.ID, b.Status, b.By, b.Bids = fmt.Sprintf("BB-%04d", n), "Open", actor, []BuyBackBid{}
	if _, err := docs.Put(ctx, o.Docs, tenant, cBuyBack, b.ID, b, 0, actor); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "buyback.saved", b.ID, map[string]any{"isin": b.ISIN, "price": b.Price.String()})
	return b, nil
}

// TenderBuyBack places, changes or (qty 0) cancels a client's tender while
// the offer is open. Shares tendered must be free in the client's demat.
func (o *Ops) TenderBuyBack(ctx context.Context, tenant, actor, id, date, ucc string, qty int64, byClient bool) (BuyBack, error) {
	if !byClient {
		if err := o.require(ctx, tenant, actor, "Tendering for a client", RoleMaker, RoleChecker); err != nil {
			return BuyBack{}, err
		}
	}
	ucc = strings.ToUpper(ucc)
	b, _, err := docs.Get[BuyBack](ctx, o.Docs, tenant, cBuyBack, id)
	if err != nil {
		return b, err
	}
	if b.Status != "Open" || date < b.Open || date > b.Close {
		return b, fmt.Errorf("%w: the offer is open for tenders from %s to %s", ErrState, b.Open, b.Close)
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return b, invalid("client %s not found", ucc)
	}
	if qty < 0 {
		return b, invalid("quantity cannot be negative")
	}
	// Tendered through the exchange in whole shares, from any of the
	// client's accounts.
	var free int64
	if hs, err := o.Holdings(ctx, tenant, ucc); err == nil {
		for _, h := range hs {
			if h.ISIN == b.ISIN {
				free += wholeUnits(h.Free())
			}
		}
	}
	if qty > free {
		return b, invalid("%s has %d free %s shares; %d cannot be tendered", ucc, free, b.Symbol, qty)
	}
	b, err = docs.Update(ctx, o.Docs, tenant, cBuyBack, id, actor, func(x *BuyBack) error {
		for i := range x.Bids {
			if x.Bids[i].UCC == ucc && x.Bids[i].Status == "Tendered" {
				x.Bids[i].Status = "Cancelled"
			}
		}
		if qty > 0 {
			x.Bids = append(x.Bids, BuyBackBid{UCC: ucc, Name: c.Name, Qty: qty, Date: date, By: actor, Status: "Tendered"})
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "buyback.tender", id, map[string]any{"ucc": ucc, "qty": qty})
	}
	return b, err
}

// BuyBackFile is the bid file for the exchange's acquisition window.
func (o *Ops) BuyBackFile(ctx context.Context, tenant, id string) ([]byte, error) {
	b, _, err := docs.Get[BuyBack](ctx, o.Docs, tenant, cBuyBack, id)
	if err != nil {
		return nil, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	rows := [][]string{{"Symbol", "ISIN", "UCC", "PAN", "DPId", "Qty", "Price", "Category"}}
	for _, x := range b.Bids {
		if x.Status != "Tendered" {
			continue
		}
		c := cs[x.UCC]
		cat := "GENERAL"
		if b.Price.MulInt(x.Qty).Cmp(money.DecInt(200000)) <= 0 {
			cat = "SMALL SHAREHOLDER" // holding up to ₹2 lakh on the record date
		}
		rows = append(rows, []string{b.Symbol, b.ISIN, x.UCC, c.PAN, c.Demat.ID, fmt.Sprint(x.Qty), b.Price.Fixed(2), cat})
	}
	return CSV(rows), nil
}

// SettleBuyBack records the registrar's acceptance: accepted shares leave
// the demat and the client is credited at the offer price through the
// clearing corporation; the rest stays with the client.
func (o *Ops) SettleBuyBack(ctx context.Context, tenant, actor, id, date, exchange string, accepted map[string]int64) (BuyBack, error) {
	if err := o.require(ctx, tenant, actor, "Settling a buy-back", RoleChecker); err != nil {
		return BuyBack{}, err
	}
	b, _, err := docs.Get[BuyBack](ctx, o.Docs, tenant, cBuyBack, id)
	if err != nil {
		return b, err
	}
	if b.Status != "Open" {
		return b, fmt.Errorf("%w: %s is %s", ErrState, id, b.Status)
	}
	if exchange == "" {
		exchange = "NSE"
	}
	for i, x := range b.Bids {
		if x.Status != "Tendered" {
			continue
		}
		a := accepted[x.UCC]
		if a < 0 || a > x.Qty {
			return b, invalid("accepted quantity for %s must be between 0 and %d", x.UCC, x.Qty)
		}
		b.Bids[i].Accepted = a
		switch {
		case a == 0:
			b.Bids[i].Status = "Not accepted"
			continue
		case a < x.Qty:
			b.Bids[i].Status = "Partly accepted"
		default:
			b.Bids[i].Status = "Accepted"
		}
		amt := b.Price.MulInt(a).Round(money.UnitPaisa)
		b.Bids[i].Amount = amt
		xc, _ := o.Client(ctx, tenant, x.UCC)
		if err := o.debitAcross(ctx, tenant, actor, xc, b.ISIN, b.Symbol, a); err != nil {
			return b, err
		}
		if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "BB:" + b.ID + ":" + x.UCC, ValueDate: date, CreatedBy: actor,
			Narration: fmt.Sprintf("Buy-back %s: %d of %d shares accepted at ₹%s", b.Symbol, a, x.Qty, b.Price), Source: ledger.SourceRef{Kind: "BUYBACK", ID: b.ID + "/" + x.UCC},
			Postings: []ledger.Posting{{Account: ledger.ClearingAccount(exchange, "CM"), Debit: amt}, {Account: ledger.ClientAccount(x.UCC), Credit: amt, Memo: "Buy-back proceeds"}}}); err != nil {
			return b, err
		}
	}
	b.Status = "Settled"
	if err := docs.Upsert(ctx, o.Docs, tenant, cBuyBack, b.ID, b, actor); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "buyback.settled", b.ID, map[string]any{"bids": len(b.Bids)})
	return b, nil
}

// ratioQty is a corporate action's entitlement on a quantity at new:old.
// On whole shares it is whole shares (the fraction is the company's to
// settle in cash, as before); on fractional units it is exact to 4 decimal
// places, rounded down.
func ratioQty(q money.Dec, num, den int64) money.Dec {
	if den == 0 {
		return money.Dec{}
	}
	x, _ := q.MulInt(num).Quo(money.DecInt(den))
	if isWhole(q) {
		return money.DecInt(wholeUnits(x))
	}
	return truncQty(x)
}

// splitQty is a quantity after a split or consolidation by factor f: whole
// shares round to whole shares (as before); fractional units are exact to
// 4 decimal places, rounded down.
func splitQty(q, f money.Dec) money.Dec {
	x := q.Mul(f)
	if isWhole(q) {
		return money.DecInt(x.Round(money.UnitRupee).Paise() / 100)
	}
	return truncQty(x)
}
