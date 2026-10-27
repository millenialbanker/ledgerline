package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// Instrument is one contract with its day's prices and risk parameters.
// Percentages are percent of value.
type Instrument struct {
	Exchange   string    `json:"exchange"`
	Segment    string    `json:"segment"`
	Symbol     string    `json:"symbol"`
	Type       string    `json:"type"` // EQ, FUT, CE, PE
	Expiry     string    `json:"expiry,omitempty"`
	Strike     string    `json:"strike,omitempty"`
	ISIN       string    `json:"isin,omitempty"`
	Company    string    `json:"company,omitempty"`
	Lot        int64     `json:"lot"`
	LTP        money.Dec `json:"ltp"`
	Close      money.Dec `json:"close"` // settlement / closing price
	VaRPct     money.Dec `json:"var_pct"`
	ELMPct     money.Dec `json:"elm_pct"`
	HaircutPct money.Dec `json:"haircut_pct"`
	SPANPct    money.Dec `json:"span_pct"`
	ExpoPct    money.Dec `json:"exposure_pct"`
	Delta      money.Dec `json:"delta"`
	Underlying money.Dec `json:"underlying"`
	Index      bool      `json:"index"`
}

// Key matches ingest.TradeEvent.Instrument().
func (i Instrument) Key() string { return i.Symbol + "|" + i.Type + "|" + i.Expiry + "|" + i.Strike }

// Name is a display name such as "NIFTY 27OCT26 25900 CE".
func (i Instrument) Name() string { return InstrumentName(i.Symbol, i.Type, i.Expiry, i.Strike) }

// InstrumentName formats a contract for display.
func InstrumentName(sym, typ, exp, strike string) string {
	if typ == "EQ" || typ == "" {
		return sym
	}
	e := exp
	if t, err := time.Parse("2006-01-02", exp); err == nil {
		e = strings.ToUpper(t.Format("02Jan06"))
	}
	if typ == "FUT" {
		return sym + " " + e + " FUT"
	}
	return sym + " " + e + " " + strike + " " + typ
}

// Market is one day's instrument master and prices.
type Market struct {
	Date        string       `json:"date"`
	Instruments []Instrument `json:"instruments"`
}

// Lookup indexes a market by exchange|segment|key, with an equity fallback
// by symbol across cash exchanges.
type Lookup struct {
	m      map[string]Instrument
	bySym  map[string]Instrument
	byISIN map[string]Instrument
}

func (l Lookup) Get(exch, seg, key string) (Instrument, bool) {
	if i, ok := l.m[exch+"|"+seg+"|"+key]; ok {
		return i, true
	}
	if seg == "CM" {
		sym := strings.SplitN(key, "|", 2)[0]
		i, ok := l.bySym[sym]
		return i, ok
	}
	return Instrument{}, false
}

// ForTrade finds the instrument of a trade.
func (l Lookup) ForTrade(t ingest.TradeEvent) (Instrument, bool) {
	return l.Get(t.Exchange, t.Segment, t.Instrument())
}

// ByISIN finds a cash-market instrument by ISIN.
func (l Lookup) ByISIN(isin string) (Instrument, bool) { i, ok := l.byISIN[isin]; return i, ok }

func newLookup(m Market) Lookup {
	l := Lookup{m: map[string]Instrument{}, bySym: map[string]Instrument{}, byISIN: map[string]Instrument{}}
	for _, i := range m.Instruments {
		l.m[i.Exchange+"|"+i.Segment+"|"+i.Key()] = i
		if i.Segment == "CM" {
			if _, ok := l.bySym[i.Symbol]; !ok || i.Exchange == "NSE" {
				l.bySym[i.Symbol] = i
			}
			if i.ISIN != "" {
				if _, ok := l.byISIN[i.ISIN]; !ok || i.Exchange == "NSE" {
					l.byISIN[i.ISIN] = i
				}
			}
		}
	}
	return l
}

// ErrNoMarketData means the day's market data file has not been loaded.
var ErrNoMarketData = errors.New("market data not loaded for this date; upload the MARKET-DATA file")

// Market returns a date's market data, falling back to the latest earlier day.
func (o *Ops) Market(ctx context.Context, tenant, date string) (Market, Lookup, error) {
	m, _, err := docs.Get[Market](ctx, o.Docs, tenant, cMarket, date)
	if errors.Is(err, docs.ErrNotFound) {
		all, lerr := o.Docs.List(ctx, tenant, cMarket, "")
		if lerr != nil {
			return Market{}, Lookup{}, lerr
		}
		best := ""
		for _, d := range all {
			if d.ID <= date && d.ID > best {
				best = d.ID
			}
		}
		if best == "" {
			return Market{Date: date}, newLookup(Market{}), fmt.Errorf("%w (%s)", ErrNoMarketData, date)
		}
		m, _, err = docs.Get[Market](ctx, o.Docs, tenant, cMarket, best)
	}
	if err != nil {
		return Market{}, Lookup{}, err
	}
	l := newLookup(m)
	o.addGSecPrices(ctx, tenant, date, &l)
	return m, l, nil
}

// SettlementPrices returns closing prices for futures, keyed like trades.
func (m Market) SettlementPrices() map[string]money.Dec {
	out := map[string]money.Dec{}
	for _, i := range m.Instruments {
		if i.Type == "FUT" && !i.Close.IsZero() {
			out[i.Key()] = i.Close
		}
	}
	return out
}

// ---- holdings, pledges, carried positions ----

// Holding is a client's demat balance in one ISIN.
type Holding struct {
	UCC     string    `json:"ucc"`
	ISIN    string    `json:"isin"`
	Symbol  string    `json:"symbol"`
	Qty     int64     `json:"qty"`
	Pledged int64     `json:"pledged"`
	AvgCost money.Dec `json:"avg_cost"`
}

func holdingID(ucc, isin string) string { return ucc + "|" + isin }

// Holdings lists holdings, optionally for one client.
func (o *Ops) Holdings(ctx context.Context, tenant, ucc string) ([]Holding, error) {
	p := ""
	if ucc != "" {
		p = ucc + "|"
	}
	return docs.List[Holding](ctx, o.Docs, tenant, cHolding, p)
}

func (o *Ops) adjustHolding(ctx context.Context, tenant, actor, ucc, isin, sym string, dQty, dPledged int64) (Holding, error) {
	id := holdingID(ucc, isin)
	if _, _, err := docs.Get[Holding](ctx, o.Docs, tenant, cHolding, id); errors.Is(err, docs.ErrNotFound) {
		if dQty < 0 || dPledged < 0 {
			return Holding{}, fmt.Errorf("%w: %s holds no %s", ErrState, ucc, sym)
		}
		h := Holding{UCC: ucc, ISIN: isin, Symbol: sym}
		if _, err := docs.Put(ctx, o.Docs, tenant, cHolding, id, h, 0, actor); err != nil && !errors.Is(err, docs.ErrStale) {
			return Holding{}, err
		}
	}
	return docs.Update(ctx, o.Docs, tenant, cHolding, id, actor, func(h *Holding) error {
		h.Qty += dQty
		h.Pledged += dPledged
		if h.Pledged < 0 || h.Qty < 0 || h.Pledged > h.Qty {
			return fmt.Errorf("%w: %s %s would hold %d with %d pledged", ErrState, ucc, sym, h.Qty, h.Pledged)
		}
		return nil
	})
}

// PledgeRequest is a client's margin pledge request from the depository.
type PledgeRequest struct {
	ID        string `json:"id"`
	UCC       string `json:"ucc"`
	ISIN      string `json:"isin"`
	Symbol    string `json:"symbol"`
	Qty       int64  `json:"qty"`
	Time      string `json:"time"`
	Status    string `json:"status"` // Pending, Accepted, Rejected
	PSN       string `json:"psn,omitempty"`
	DecidedBy string `json:"decided_by,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// PledgeRequests lists pledge requests.
func (o *Ops) PledgeRequests(ctx context.Context, tenant string) ([]PledgeRequest, error) {
	ps, err := docs.List[PledgeRequest](ctx, o.Docs, tenant, cPledge, "")
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Status == "Pending" && ps[j].Status != "Pending" })
	return ps, err
}

// DecidePledge accepts or rejects a pledge request. Accepting marks the
// securities as pledged collateral.
func (o *Ops) DecidePledge(ctx context.Context, tenant, actor, id string, accept bool, reason string) (PledgeRequest, error) {
	if err := o.require(ctx, tenant, actor, "Deciding a pledge request", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return PledgeRequest{}, err
	}
	p, _, err := docs.Get[PledgeRequest](ctx, o.Docs, tenant, cPledge, id)
	if err != nil {
		return p, err
	}
	if p.Status != "Pending" {
		return p, fmt.Errorf("%w: pledge %s is %s", ErrState, id, p.Status)
	}
	if !accept && strings.TrimSpace(reason) == "" {
		return p, invalid("give a reason for rejecting the pledge")
	}
	if accept {
		hs, err := o.Holdings(ctx, tenant, p.UCC)
		if err != nil {
			return p, err
		}
		free := int64(0)
		for _, h := range hs {
			if h.ISIN == p.ISIN {
				free = h.Qty - h.Pledged
			}
		}
		if p.Qty > free {
			return p, fmt.Errorf("%w: only %d %s are free to pledge", ErrState, free, p.Symbol)
		}
		if _, err := o.adjustHolding(ctx, tenant, actor, p.UCC, p.ISIN, p.Symbol, 0, p.Qty); err != nil {
			return p, err
		}
	}
	psn := ""
	if accept {
		n, err := o.next(ctx, tenant, "psn", actor)
		if err != nil {
			return p, err
		}
		psn = fmt.Sprintf("%08d", 7000000+n)
	}
	p, err = docs.Update(ctx, o.Docs, tenant, cPledge, id, actor, func(x *PledgeRequest) error {
		if accept {
			x.Status, x.PSN = "Accepted", psn
		} else {
			x.Status, x.Reason = "Rejected", reason
		}
		x.DecidedBy = actor
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "pledge."+strings.ToLower(p.Status), id, map[string]any{"ucc": p.UCC, "isin": p.ISIN, "qty": p.Qty})
	}
	return p, err
}

// Position is a derivatives position carried from the previous day.
type Position struct {
	Date       string    `json:"date"` // the trade date it applies to
	UCC        string    `json:"ucc"`
	Exchange   string    `json:"exchange"`
	Segment    string    `json:"segment"`
	Instrument string    `json:"instrument"`
	Qty        int64     `json:"qty"`
	AvgPrice   money.Dec `json:"avg_price"`
}

func (o *Ops) carried(ctx context.Context, tenant, date string) ([]Position, error) {
	return docs.List[Position](ctx, o.Docs, tenant, cPosition, date+"|")
}

// ---- client code modification ----

// CCM is a client code modification on an exchange trade.
type CCM struct {
	TradeKey string `json:"trade_key"`
	Date     string `json:"date"`
	Exchange string `json:"exchange"`
	Segment  string `json:"segment"`
	TradeNo  string `json:"trade_no"`
	From     string `json:"from"`
	To       string `json:"to"`
	Reason   string `json:"reason"`
	By       string `json:"by"`
	At       string `json:"at"`
}

// CCMs lists modifications for a date ("" for all).
func (o *Ops) CCMs(ctx context.Context, tenant, date string) ([]CCM, error) {
	all, err := docs.List[CCM](ctx, o.Docs, tenant, cCCM, "")
	if err != nil || date == "" {
		return all, err
	}
	out := []CCM{}
	for _, c := range all {
		if c.Date == date {
			out = append(out, c)
		}
	}
	return out, nil
}

func (o *Ops) applyCCM(ctx context.Context, tenant string, evs []ingest.TradeEvent) ([]ingest.TradeEvent, error) {
	cs, err := docs.List[CCM](ctx, o.Docs, tenant, cCCM, "")
	if err != nil || len(cs) == 0 {
		return evs, err
	}
	m := map[string]string{}
	for _, c := range cs {
		m[c.TradeKey] = c.To
	}
	out := make([]ingest.TradeEvent, len(evs))
	for i, e := range evs {
		if to, ok := m[e.Key()]; ok {
			e.UCC = to
		}
		out[i] = e
	}
	return out, nil
}

// RaiseCCM moves an exchange trade to another client code and resolves the
// matching UCC break. A rebill is then needed for the date.
func (o *Ops) RaiseCCM(ctx context.Context, tenant, actor, breakID, reason string) (CCM, error) {
	if err := o.require(ctx, tenant, actor, "Client code modification", RoleMaker, RoleChecker); err != nil {
		return CCM{}, err
	}
	if strings.TrimSpace(reason) == "" {
		reason = "Punching error"
	}
	bs, err := o.App.Breaks.List(ctx, tenant, "", "OPEN")
	if err != nil {
		return CCM{}, err
	}
	var target *struct{ date, exch, seg, no, from, to string }
	for _, b := range bs {
		if b.ID == breakID {
			for _, d := range b.Diffs {
				if d.Field == "ucc" {
					target = &struct{ date, exch, seg, no, from, to string }{b.TradeDate, b.Exchange, b.Segment, b.TradeNo, d.Exchange, d.OMS}
				}
			}
		}
	}
	if target == nil {
		return CCM{}, fmt.Errorf("%w: %s is not an open UCC break", ErrState, breakID)
	}
	key := target.exch + "|" + target.seg + "|" + target.date + "|" + target.no
	c := CCM{TradeKey: key, Date: target.date, Exchange: target.exch, Segment: target.seg, TradeNo: target.no, From: target.from, To: target.to, Reason: reason, By: actor, At: o.now().Format("15:04:05")}
	if _, err := o.Client(ctx, tenant, c.To); err != nil {
		return CCM{}, fmt.Errorf("%w: client %s does not exist", ErrInvalid, c.To)
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cCCM, key, c, 0, actor); err != nil {
		return CCM{}, err
	}
	if _, err := o.App.ResolveBreak(ctx, tenant, actor, breakID, fmt.Sprintf("Client code modified %s → %s (%s)", c.From, c.To, reason)); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "ccm.raised", key, map[string]any{"from": c.From, "to": c.To, "reason": reason})
	return c, nil
}
