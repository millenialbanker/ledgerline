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
	Qty        int64     `json:"qty"`
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
	for _, h := range hs {
		if h.Pledged <= 0 {
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
		l.Qty += h.Pledged
	}
	out := []CollateralLine{}
	for _, l := range g {
		l.Value = l.LTP.MulInt(l.Qty)
		l.Collateral = l.Value.Pct(money.DecInt(100).Sub(l.HaircutPct))
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Collateral.Cmp(out[j].Collateral) > 0 })
	return out, nil
}

// HoldingView is a holding with its value.
type HoldingView struct {
	Holding
	LTP   money.Dec `json:"ltp"`
	Value money.Dec `json:"value"`
}

// ClientDetail is everything about one client for the client drawer.
type ClientDetail struct {
	Client    Client              `json:"client"`
	Risk      *RiskRow            `json:"risk,omitempty"`
	Positions []PositionView      `json:"positions"`
	Holdings  []HoldingView       `json:"holdings"`
	Trades    []ingest.TradeEvent `json:"trades"`
	Statement ledger.Statement    `json:"statement"`
	Notes     []ContractNote      `json:"notes"`
	Alerts    []Alert             `json:"alerts"`
	Segments  map[string]string   `json:"segment_names"`
}

// ClientDetail gathers a client's position for a date.
func (o *Ops) ClientDetail(ctx context.Context, tenant, ucc, date string) (ClientDetail, error) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return ClientDetail{}, err
	}
	d := ClientDetail{Client: c.Public(), Positions: []PositionView{}, Holdings: []HoldingView{}, Trades: []ingest.TradeEvent{}, Notes: []ContractNote{}, Alerts: []Alert{}, Segments: SegmentNames}
	if c.Status == "Active" {
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
	for _, h := range hs {
		v := HoldingView{Holding: h}
		if i, ok := look.ByISIN(h.ISIN); ok {
			v.LTP = i.LTP
			v.Value = i.LTP.MulInt(h.Qty)
		}
		d.Holdings = append(d.Holdings, v)
	}
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
	d.Statement, err = o.App.Ledger.Statement(ctx, tenant, ledger.ClientAccount(c.UCC), "", date)
	if err != nil {
		return d, err
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
