package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- the stock journal: moving holdings between the portfolios (s4) ----
//
// One entry with quantity and amount on both sides: the holding leaves
// one portfolio at its cost (the lots, first in, first out) and enters
// the other at that day's market value (the closing price; a price the
// maker gives where there is none, as for unlisted shares). The journal
// entry posts the difference as a gain or loss on transfer between the
// portfolios. A second person approves it.
//
// Tax, recorded on the journal and on the new lot:
//
//   - Investments into stock-in-trade is a conversion of a capital asset
//     into stock-in-trade, section 45(2) of the Income-tax Act, 1961: the
//     capital gain is the market value on the conversion date less the
//     cost (long- or short-term by the holding up to that date), and it
//     is taxed in the year the stock-in-trade is sold, when the business
//     income is the sale value less that market value.
//   - Stock-in-trade into investments is section 28(via) (from the
//     Finance Act, 2018): the market value on the conversion date less
//     the cost is business income of that year; that market value is the
//     cost for capital gains later (section 49(9)) and the holding runs
//     from the conversion date (section 2(42A)).
//
// From 1 April 2026 the Income-tax Act, 2025 replaces the 1961 Act and
// renumbers these sections (capital gains chargeable: section 67; profits
// and gains of business: section 26); the treatment is the same. Sources:
// incometaxindia.gov.in (Income-tax Act, 2025, sections 67 and 26) and
// the 1961 Act sections named, checked 22 November 2026.

const cFSJournal = "fsjournal"

// FSJournal is one stock journal.
type FSJournal struct {
	ID         string       `json:"id"`
	Date       string       `json:"date"`
	From       string       `json:"from"`
	To         string       `json:"to"`
	ISIN       string       `json:"isin"`
	Symbol     string       `json:"symbol"`
	Kind       string       `json:"kind"`
	Qty        money.Dec    `json:"qty"`
	Price      money.Dec    `json:"price"`
	PriceFrom  string       `json:"price_from"`
	OutCost    money.Amount `json:"out_cost"` // at cost, worked out when it is posted
	InValue    money.Amount `json:"in_value"` // at market value
	Difference money.Amount `json:"difference"`
	TaxDate    string       `json:"tax_date"`
	TaxValue   money.Amount `json:"tax_value"`
	TaxNote    string       `json:"tax_note"`
	Reason     string       `json:"reason"`
	Status     string       `json:"status"` // Pending, Posted, Rejected
	Maker      string       `json:"maker"`
	Checker    string       `json:"checker,omitempty"`
	Decision   string       `json:"decision,omitempty"`
	Entry      string       `json:"entry,omitempty"`
	Created    string       `json:"created"`
	Warnings   []string     `json:"warnings,omitempty"`
}

// FSJournalInput is a stock journal to enter.
type FSJournalInput struct {
	Date   string `json:"date"`
	From   string `json:"from"`
	To     string `json:"to"`
	ISIN   string `json:"isin"`
	Qty    string `json:"qty"`
	Price  string `json:"price,omitempty"` // only where there is no closing price
	Reason string `json:"reason"`
}

func fsTaxNote(from string) string {
	if from == PortInv {
		return "45(2): investment converted into stock-in-trade; the capital gain to this market value is taxed when the stock is sold"
	}
	return "28(via): stock-in-trade converted into an investment; this market value less cost is business income now, and the cost for capital gains later"
}

// fsPrice is a scrip's closing price on a date (the last price when the
// close is not given).
func (o *Ops) fsPrice(look Lookup, isin string) (money.Dec, bool) {
	if i, ok := look.ByISIN(isin); ok {
		if i.Close.Sign() > 0 {
			return i.Close, true
		}
		if i.LTP.Sign() > 0 {
			return i.LTP, true
		}
	}
	return money.Dec{}, false
}

// AddFirmSecJournal enters a stock journal; it waits for a second person.
func (o *Ops) AddFirmSecJournal(ctx context.Context, tenant, actor string, in FSJournalInput) (FSJournal, error) {
	if err := o.require(ctx, tenant, actor, "Entering firm securities", RoleMaker, RoleChecker); err != nil {
		return FSJournal{}, err
	}
	s := o.FirmSecSettings(ctx, tenant)
	if s.Moves != "journal" {
		return FSJournal{}, fmt.Errorf("%w: the firm's practice is that holdings do not move between the portfolios (Settings → Firm practices → Firm securities)", ErrState)
	}
	in.ISIN = strings.ToUpper(strings.TrimSpace(in.ISIN))
	switch {
	case !ledger.ValidDate(in.Date):
		return FSJournal{}, invalid("date must be YYYY-MM-DD")
	case in.From == in.To || !s.has(in.From) || !s.has(in.To):
		return FSJournal{}, invalid("choose the portfolio the holding leaves and the one it goes to")
	case strings.TrimSpace(in.Reason) == "":
		return FSJournal{}, invalid("say why the holding moves")
	}
	qty, err := parseQty(in.Qty)
	if err != nil || qty.Sign() <= 0 {
		return FSJournal{}, invalid("give the quantity, above zero")
	}
	if err := o.checkLock(ctx, tenant, in.Date); err != nil {
		return FSJournal{}, err
	}
	_, ts, b := o.fsBookTo(ctx, tenant, in.Date)
	if held := b.held(in.From, in.ISIN); held.Cmp(qty) < 0 {
		return FSJournal{}, invalid("%s holds %s of %s on %s", PortfolioName(in.From), held, in.ISIN, in.Date)
	}
	for _, p := range []string{in.From, in.To} {
		if x, ok := fsLaterLine(ts, p, in.ISIN, in.Date, ""); ok {
			return FSJournal{}, fmt.Errorf("%w: %s has a later line in %s (%s on %s); enter the journal in date order", ErrState, in.ISIN, PortfolioName(p), x.ID, x.Date)
		}
	}
	lots := b.Lots[fsKey(in.From, in.ISIN)]
	j := FSJournal{Date: in.Date, From: in.From, To: in.To, ISIN: in.ISIN, Symbol: lots[0].Symbol, Kind: lots[0].Kind, Qty: qty, Reason: strings.TrimSpace(in.Reason),
		Status: FSPending, Maker: actor, Created: o.now().Format(time.RFC3339)}
	_, look, _ := o.Market(ctx, tenant, in.Date)
	if p, ok := o.fsPrice(look, in.ISIN); ok {
		j.Price, j.PriceFrom = p, "closing price on "+in.Date
		if strings.TrimSpace(in.Price) != "" {
			j.Warnings = append(j.Warnings, "the closing price is used, not the price given")
		}
	} else {
		p, err := money.ParseDec(strings.TrimSpace(in.Price))
		if err != nil || p.Sign() <= 0 {
			return FSJournal{}, invalid("there is no closing price for %s on %s: give the market value per unit", in.ISIN, in.Date)
		}
		j.Price, j.PriceFrom = p, "given by "+actor+" (no closing price)"
	}
	j.InValue = j.Price.Mul(qty).Round(money.UnitPaisa)
	j.TaxDate, j.TaxValue, j.TaxNote = in.Date, j.InValue, fsTaxNote(in.From)
	// The cost it would leave at now (worked out again when posted).
	tmp := b.Lots[fsKey(in.From, in.ISIN)]
	cp := append([]FSLot(nil), tmp...)
	left := qty
	for i := 0; i < len(cp) && left.Sign() > 0; i++ {
		q := money.Min(cp[i].Qty, left)
		j.OutCost += cp[i].take(q).Cost
		left = left.Sub(q)
	}
	j.Difference = j.InValue - j.OutCost
	n, err := o.next(ctx, tenant, "fsjournal", actor)
	if err != nil {
		return j, err
	}
	j.ID = fmt.Sprintf("SJ-%04d", n)
	if err := docs.Upsert(ctx, o.Docs, tenant, cFSJournal, j.ID, j, actor); err != nil {
		return j, err
	}
	o.raiseWork(ctx, tenant, "firmsec", "journal|"+j.ID, "Stock journal to approve: "+j.Symbol,
		fmt.Sprintf("%s moves %s %s from %s to %s on %s (at cost ₹%s, in at market value ₹%s). A second person approves it on Firm securities → Stock journal.", actor, j.Qty, j.Symbol, PortfolioName(j.From), PortfolioName(j.To), j.Date, formatINR(j.OutCost.Dec()), formatINR(j.InValue.Dec())),
		"", RoleChecker, "firmsec", j.Date)
	o.log(ctx, tenant, actor, "firm_securities.journal_entered", j.ID, map[string]any{"from": j.From, "to": j.To, "isin": j.ISIN, "qty": j.Qty.String()})
	return j, nil
}

// FirmSecJournals lists the stock journals, newest first.
func (o *Ops) FirmSecJournals(ctx context.Context, tenant, actor string) ([]FSJournal, error) {
	if err := o.require(ctx, tenant, actor, "Viewing firm securities", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return nil, err
	}
	js, err := docs.List[FSJournal](ctx, o.Docs, tenant, cFSJournal, "")
	sort.SliceStable(js, func(i, k int) bool { return js[i].ID > js[k].ID })
	if js == nil {
		js = []FSJournal{}
	}
	return js, err
}

// DecideFirmSecJournal approves (posts) or rejects a stock journal. The
// maker cannot approve it.
func (o *Ops) DecideFirmSecJournal(ctx context.Context, tenant, actor, id string, approve bool, note string) (FSJournal, error) {
	if err := o.require(ctx, tenant, actor, "Approving firm securities entries", RoleChecker); err != nil {
		return FSJournal{}, err
	}
	j, _, err := docs.Get[FSJournal](ctx, o.Docs, tenant, cFSJournal, id)
	if err != nil {
		return j, err
	}
	if j.Status != FSPending {
		return j, fmt.Errorf("%w: %s is %s", ErrState, id, j.Status)
	}
	note = strings.TrimSpace(note)
	if !approve {
		if note == "" {
			return j, invalid("say why it is rejected")
		}
		j.Status, j.Checker, j.Decision = FSRejected, actor, note
		if err := docs.Upsert(ctx, o.Docs, tenant, cFSJournal, j.ID, j, actor); err != nil {
			return j, err
		}
		o.closeWork(ctx, tenant, actor, "firmsec", "journal|"+j.ID, "rejected")
		o.log(ctx, tenant, actor, "firm_securities.journal_rejected", j.ID, map[string]any{"note": note})
		return j, nil
	}
	if j.Maker == actor {
		return j, fmt.Errorf("%w: you entered %s; another person approves it", ErrForbidden, id)
	}
	if err := o.checkLock(ctx, tenant, j.Date); err != nil {
		return j, err
	}
	s, ts, b := o.fsBookTo(ctx, tenant, j.Date)
	if held := b.held(j.From, j.ISIN); held.Cmp(j.Qty) < 0 {
		return j, fmt.Errorf("%w: %s now holds %s of %s on %s; reject the journal", ErrState, PortfolioName(j.From), held, j.Symbol, j.Date)
	}
	for _, p := range []string{j.From, j.To} {
		if x, ok := fsLaterLine(ts, p, j.ISIN, j.Date, ""); ok {
			return j, fmt.Errorf("%w: %s on %s came after this journal was entered; reject it and enter it again", ErrState, x.ID, x.Date)
		}
	}
	now := o.now().Format(time.RFC3339)
	out := FSTxn{ID: j.ID + "-OUT", Date: j.Date, PayDate: j.Date, Portfolio: j.From, Kind: j.Kind, Side: "S", Type: FSTypeJournal, ISIN: j.ISIN, Symbol: j.Symbol,
		Qty: j.Qty, Value: j.InValue, Ref: j.ID, Status: FSPosted, Maker: j.Maker, Checker: actor, Created: now}
	in := FSTxn{ID: j.ID + "-IN", Date: j.Date, PayDate: j.Date, Portfolio: j.To, Kind: j.Kind, Side: "B", Type: FSTypeJournal, ISIN: j.ISIN, Symbol: j.Symbol,
		Qty: j.Qty, Value: j.InValue, Ref: j.ID, TaxDate: j.TaxDate, TaxValue: j.TaxValue, TaxNote: j.TaxNote, Status: FSPosted, Maker: j.Maker, Checker: actor, Created: now}
	if j.To == PortInv {
		in.Term = TermLong
	}
	b = fsReplay(s, append(append([]FSTxn{}, ts...), out, in), o.fsCorpActions(ctx, tenant), j.Date)
	j.OutCost = b.Res[out.ID].CostOut
	j.Difference = j.InValue - j.OutCost
	// The entry: out at cost, in at market value, the difference as gain
	// or loss on transfer.
	var ps []ledger.Posting
	add := func(acct string, dr, cr money.Amount, memo string) {
		if dr > 0 {
			ps = append(ps, ledger.Posting{Account: acct, Debit: dr, Memo: memo})
		}
		if cr > 0 {
			ps = append(ps, ledger.Posting{Account: acct, Credit: cr, Memo: memo})
		}
	}
	outAcct, inAcct := fsInvAcct(j.Kind), fsInvAcct(j.Kind)
	if j.From == PortSIT {
		outAcct = fsPurAcct(j.Kind, false)
	}
	if j.To == PortSIT {
		inAcct = fsPurAcct(j.Kind, false)
	}
	add(inAcct, j.InValue, 0, "Transferred in from "+PortfolioName(j.From)+" at market value")
	add(outAcct, 0, j.OutCost, "Transferred out to "+PortfolioName(j.To)+" at cost")
	if j.Difference > 0 {
		add(AcctFSTransfer, 0, j.Difference, "Gain on transfer between portfolios")
	} else {
		add(AcctFSTransfer, -j.Difference, 0, "Loss on transfer between portfolios")
	}
	o.ensureFSAccounts(ctx, tenant, s, []string{inAcct, outAcct, AcctFSTransfer})
	if len(ps) >= 2 {
		e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "FSJ:" + j.ID, ValueDate: j.Date, CreatedBy: actor, Source: ledger.SourceRef{Kind: "FIRMSEC", ID: j.ID},
			Narration: fmt.Sprintf("Stock journal %s · %s %s from %s (at cost) to %s (at market value) · %s", j.ID, j.Qty, j.Symbol, PortfolioName(j.From), PortfolioName(j.To), j.Reason),
			Postings:  ps})
		if err != nil {
			return j, err
		}
		j.Entry = e.ID
	}
	out.Entry, in.Entry = j.Entry, j.Entry
	for _, t := range []FSTxn{out, in} {
		if err := docs.Upsert(ctx, o.Docs, tenant, cFSTxn, t.ID, t, actor); err != nil {
			return j, err
		}
	}
	j.Status, j.Checker, j.Decision = FSPosted, actor, note
	if err := docs.Upsert(ctx, o.Docs, tenant, cFSJournal, j.ID, j, actor); err != nil {
		return j, err
	}
	o.closeWork(ctx, tenant, actor, "firmsec", "journal|"+j.ID, "posted")
	o.log(ctx, tenant, actor, "firm_securities.journal_posted", j.ID, map[string]any{"out_cost": j.OutCost.String(), "in_value": j.InValue.String(), "difference": j.Difference.String()})
	return j, nil
}
