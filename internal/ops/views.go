package ops

import (
	"context"
	"sort"

	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// CollateralLine is the pledged collateral in one ISIN.
type CollateralLine struct {
	ISIN       string    `json:"isin"`
	Symbol     string    `json:"symbol"`
	Clients    int       `json:"clients"`
	Qty        money.Dec `json:"qty"`
	LTP        money.Dec `json:"ltp"`
	Value      money.Dec `json:"value"`
	HaircutPct money.Dec `json:"haircut_pct"`
	Collateral money.Dec `json:"collateral"`
	Priced     bool      `json:"priced"`
}

// Collateral summarises pledged securities by ISIN.
func (o *Ops) Collateral(ctx context.Context, tenant, date string) ([]CollateralLine, error) {
	hs, err := o.Holdings(ctx, tenant, "")
	if err != nil {
		return nil, err
	}
	_, look, _ := o.Market(ctx, tenant, date)
	g := map[string]*CollateralLine{}
	for _, h := range sumHoldings(hs) {
		if h.Pledged.Sign() <= 0 {
			continue
		}
		l := g[h.ISIN]
		if l == nil {
			l = &CollateralLine{ISIN: h.ISIN, Symbol: h.Symbol}
			if i, ok := look.ByISIN(h.ISIN); ok {
				l.LTP, l.HaircutPct, l.Priced = i.LTP, i.HaircutPct, true
			}
			g[h.ISIN] = l
		}
		l.Clients++
		l.Qty = l.Qty.Add(h.Pledged)
	}
	out := []CollateralLine{}
	for _, l := range g {
		l.Value = l.LTP.Mul(l.Qty)
		l.Collateral = l.Value.Pct(money.DecInt(100).Sub(l.HaircutPct))
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Collateral.Cmp(out[j].Collateral) > 0 })
	return out, nil
}

// HoldingView is a holding with its value. In a client's list each row is
// one demat account's (Account is its BO ID, Primary whether it is the
// primary); Totals add the accounts up.
type HoldingView struct {
	Holding
	LTP     money.Dec `json:"ltp"`
	Value   money.Dec `json:"value"`
	Account string    `json:"account,omitempty"`
	Primary bool      `json:"primary,omitempty"`
}

// holdingViews values holdings at the day's prices (exact, rounded to paise
// only when shown), with each one's account.
func holdingViews(c Client, hs []Holding, look Lookup) []HoldingView {
	out := []HoldingView{}
	for _, h := range hs {
		v := HoldingView{Holding: h, Account: holdingBO(c, h), Primary: h.BOID == ""}
		if i, ok := look.ByISIN(h.ISIN); ok {
			v.LTP = i.LTP
			v.Value = h.Value(i.LTP)
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Primary != out[j].Primary {
			return out[i].Primary
		}
		if out[i].Account != out[j].Account {
			return out[i].Account < out[j].Account
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}

// ClientDetail is everything about one client for the client drawer.
type ClientDetail struct {
	Client    Client         `json:"client"`
	Risk      *RiskRow       `json:"risk,omitempty"`
	Positions []PositionView `json:"positions"`
	Holdings  []HoldingView  `json:"holdings"`
	// HoldingTotals are the holdings of all the client's demat accounts
	// added up, one per ISIN; Demats the client's demat accounts.
	HoldingTotals []HoldingView       `json:"holding_totals"`
	Demats        []DematRow          `json:"demats"`
	Trades        []ingest.TradeEvent `json:"trades"`
	Statement     ledger.Statement    `json:"statement"`
	Notes         []ContractNote      `json:"notes"`
	Alerts        []Alert             `json:"alerts"`
	Segments      map[string]string   `json:"segment_names"`
	// Ledgers: the client's equity, commodity and awaiting balances, with
	// a separate commodity ledger (segledger.go).
	Ledgers *ClientLedgers `json:"ledgers,omitempty"`
	// Earmark: the client's MCX money and equity money in one ledger,
	// with an MCX-only bank account (mcxearmark.go).
	Earmark *ClientEarmark `json:"earmark,omitempty"`
}

// ClientDetail gathers a client's position for a date.
func (o *Ops) ClientDetail(ctx context.Context, tenant, ucc, date string) (ClientDetail, error) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return ClientDetail{}, err
	}
	d := ClientDetail{Client: o.withDemats(ctx, tenant, c).Public(), Positions: []PositionView{}, Holdings: []HoldingView{}, HoldingTotals: []HoldingView{}, Trades: []ingest.TradeEvent{}, Notes: []ContractNote{}, Alerts: []Alert{}, Segments: SegmentNames}
	if c.Status == "Active" && !c.DPOnly {
		rv, err := o.Risk(ctx, tenant, date)
		if err != nil {
			return d, err
		}
		for i := range rv.Rows {
			if rv.Rows[i].UCC == c.UCC {
				d.Risk = &rv.Rows[i]
			}
		}
		if p := rv.Positions[c.UCC]; p != nil {
			d.Positions = p
		}
	}
	hs, err := o.Holdings(ctx, tenant, c.UCC)
	if err != nil {
		return d, err
	}
	_, look, _ := o.Market(ctx, tenant, date)
	d.Holdings = holdingViews(c, hs, look)
	for _, v := range holdingViews(c, sumHoldings(hs), look) {
		v.Account, v.Primary = "", false
		d.HoldingTotals = append(d.HoldingTotals, v)
	}
	d.Demats = o.dematRows(ctx, tenant, c, d.Holdings)
	ts, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return d, err
	}
	for _, t := range ts {
		if t.UCC == c.UCC {
			d.Trades = append(d.Trades, t)
		}
	}
	sort.Slice(d.Trades, func(i, j int) bool { return d.Trades[i].TradeTime < d.Trades[j].TradeTime })
	d.Statement, err = o.staffStatement(ctx, tenant, c.UCC, "", date)
	if err != nil {
		return d, err
	}
	if o.sepCom(ctx, tenant) {
		if d.Ledgers, err = o.clientLedgersView(ctx, tenant, c.UCC, date); err != nil {
			return d, err
		}
	}
	if set, _ := o.Settings(ctx, tenant); set.mcxEarmark() {
		if d.Earmark, err = o.clientEarmarkView(ctx, tenant, set, c.UCC, date); err != nil {
			return d, err
		}
	}
	ns, _ := o.Notes(ctx, tenant, date)
	for _, n := range ns {
		if n.UCC == c.UCC {
			d.Notes = append(d.Notes, n)
		}
	}
	as, _ := o.Alerts(ctx, tenant)
	for _, a := range as {
		if a.UCC == c.UCC {
			d.Alerts = append(d.Alerts, a)
		}
	}
	return d, nil
}

// ClientRow is a client in the client list with balance and risk status.
type ClientRow struct {
	Client
	Funds      money.Dec `json:"funds"`
	RiskStatus string    `json:"risk_status,omitempty"`
}

// ClientRows lists clients with their funds and risk status.
func (o *Ops) ClientRows(ctx context.Context, tenant, date string) ([]ClientRow, error) {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, err
	}
	funds, err := o.clientFunds(ctx, tenant, date, "")
	if err != nil {
		return nil, err
	}
	rv, err := o.Risk(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	st := map[string]string{}
	for _, r := range rv.Rows {
		st[r.UCC] = r.Status
	}
	out := make([]ClientRow, 0, len(cs))
	for _, c := range cs {
		out = append(out, ClientRow{Client: c.Public(), Funds: funds[c.UCC].Dec(), RiskStatus: st[c.UCC]})
	}
	return out, nil
}
