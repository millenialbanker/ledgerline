package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// PositionView is an open position with its margin.
type PositionView struct {
	UCC        string    `json:"ucc"`
	Exchange   string    `json:"exchange"`
	Segment    string    `json:"segment"`
	Instrument string    `json:"instrument"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	Qty        int64     `json:"qty"`
	AvgPrice   money.Dec `json:"avg_price"`
	LTP        money.Dec `json:"ltp"`
	MTM        money.Dec `json:"mtm"`
	Margin     money.Dec `json:"margin"`
	FutEq      money.Dec `json:"futeq"`
	Index      bool      `json:"index"`
	Priced     bool      `json:"priced"`
}

type posAcc struct {
	PositionView
	bq, sq int64
	bv, sv money.Dec
}

// RiskRow is one client's limit and margin position.
type RiskRow struct {
	UCC         string    `json:"ucc"`
	Name        string    `json:"name"`
	Funds       money.Dec `json:"funds"` // ledger credit; negative = client owes
	UPI         money.Dec `json:"upi"`
	Collateral  money.Dec `json:"collateral"`        // pledged value after haircut
	CollUsable  money.Dec `json:"collateral_usable"` // capped at the cash component
	Cash        money.Dec `json:"cash"`
	Required    money.Dec `json:"required"`
	Free        money.Dec `json:"free"`
	Util        float64   `json:"utilisation"`
	Status      string    `json:"status"` // OK, Watch, Breach, Debit
	CashShort   bool      `json:"cash_short"`
	FutEq       money.Dec `json:"futeq"`
	IndexFutEq  money.Dec `json:"index_futeq"`
	IndexBreach bool      `json:"index_breach"`
	Overrides   money.Dec `json:"overrides"`
	Limit       money.Dec `json:"limit"`
	Blocked     bool      `json:"blocked"`
	Positions   int       `json:"positions"`
	SqOffSent   int       `json:"square_off_sent"`
}

// riskInput gathers everything a risk computation needs for one date.
type riskInput struct {
	date     string
	set      Settings
	clients  map[string]Client
	look     Lookup
	carried  []Position
	trades   []ingest.TradeEvent
	holdings []Holding
}

func (o *Ops) riskInput(ctx context.Context, tenant, date string) (riskInput, error) {
	in := riskInput{date: date}
	var err error
	if in.set, err = o.Settings(ctx, tenant); err != nil {
		return in, err
	}
	if in.clients, err = o.ClientMap(ctx, tenant); err != nil {
		return in, err
	}
	if _, in.look, err = o.Market(ctx, tenant, date); err != nil && !strings.Contains(err.Error(), "market data not loaded") {
		return in, err
	}
	if in.carried, err = o.carried(ctx, tenant, date); err != nil {
		return in, err
	}
	if in.trades, err = o.exchangeTrades(ctx, tenant, date); err != nil {
		return in, err
	}
	in.holdings, err = o.Holdings(ctx, tenant, "")
	return in, err
}

// positions nets carried positions and the day's derivative trades up to
// `until` (HH:MM:SS, "" for end of day). It also returns the cash-market
// margin on open buys at that time (VaR + ELM), which applies intraday only.
func (in riskInput) positions(until string) (map[string][]PositionView, map[string]money.Dec) {
	acc := map[string]*posAcc{}
	get := func(ucc, exch, seg, key string) *posAcc {
		k := ucc + "|" + exch + "|" + seg + "|" + key
		if acc[k] == nil {
			p := strings.Split(key, "|")
			acc[k] = &posAcc{PositionView: PositionView{UCC: ucc, Exchange: exch, Segment: seg, Instrument: key, Type: p[1]}}
		}
		return acc[k]
	}
	for _, c := range in.carried {
		a := get(c.UCC, c.Exchange, c.Segment, c.Instrument)
		if c.Qty > 0 {
			a.bq += c.Qty
			a.bv = a.bv.Add(c.AvgPrice.MulInt(c.Qty))
		} else {
			a.sq += -c.Qty
			a.sv = a.sv.Add(c.AvgPrice.MulInt(-c.Qty))
		}
	}
	cmOpen := map[string]map[string]int64{} // ucc -> instrument key -> net qty (intraday)
	cmPrice := map[string]money.Dec{}
	for _, t := range in.trades {
		if until != "" && t.TradeTime > until {
			continue
		}
		if t.Segment == "CM" {
			if until == "" {
				continue
			}
			k := t.Exchange + "|" + t.Instrument()
			if cmOpen[t.UCC] == nil {
				cmOpen[t.UCC] = map[string]int64{}
			}
			q := t.Qty
			if t.Side == "S" {
				q = -q
			}
			// CNC sells are delivered from holdings and need no margin.
			if t.Product == "CNC" && t.Side == "S" {
				continue
			}
			cmOpen[t.UCC][k] += q
			cmPrice[k] = t.Price
			continue
		}
		a := get(t.UCC, t.Exchange, t.Segment, t.Instrument())
		if t.Side == "B" {
			a.bq += t.Qty
			a.bv = a.bv.Add(t.Value())
		} else {
			a.sq += t.Qty
			a.sv = a.sv.Add(t.Value())
		}
	}
	out := map[string][]PositionView{}
	for _, a := range acc {
		q := a.bq - a.sq
		if q == 0 {
			continue
		}
		p := a.PositionView
		p.Qty = q
		if q > 0 {
			p.AvgPrice, _ = a.bv.Quo(money.DecInt(a.bq))
		} else {
			p.AvgPrice, _ = a.sv.Quo(money.DecInt(a.sq))
		}
		inst, ok := in.look.Get(p.Exchange, p.Segment, p.Instrument)
		parts := strings.Split(p.Instrument, "|")
		p.Name = InstrumentName(parts[0], parts[1], parts[2], parts[3])
		p.LTP = p.AvgPrice
		if ok {
			p.Priced, p.LTP, p.Index = true, inst.LTP, inst.Index
			n := money.DecInt(abs(q))
			rate := inst.SPANPct.Add(inst.ExpoPct)
			switch {
			case inst.Type == "FUT":
				p.Margin = n.Mul(inst.LTP).Pct(rate)
				p.FutEq = money.DecInt(q).Mul(inst.LTP)
			case q < 0: // short option
				p.Margin = n.Mul(inst.Underlying).Pct(rate)
				p.FutEq = money.DecInt(q).Mul(inst.Delta).Mul(inst.Underlying)
			default: // long option: premium already paid
				p.FutEq = money.DecInt(q).Mul(inst.Delta).Mul(inst.Underlying)
			}
		}
		p.MTM = p.LTP.Sub(p.AvgPrice).MulInt(q)
		out[p.UCC] = append(out[p.UCC], p)
	}
	for u := range out {
		sort.Slice(out[u], func(i, j int) bool { return out[u][i].Name < out[u][j].Name })
	}
	cm := map[string]money.Dec{}
	for u, m := range cmOpen {
		for k, q := range m {
			if q <= 0 {
				continue
			}
			ex, key, _ := strings.Cut(k, "|")
			px := cmPrice[k]
			rate := money.MustDec("20")
			if inst, ok := in.look.Get(ex, "CM", key); ok {
				px, rate = inst.LTP, inst.VaRPct.Add(inst.ELMPct)
			}
			cm[u] = cm[u].Add(px.MulInt(q).Pct(rate))
		}
	}
	return out, cm
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// collateral returns each client's pledged value after haircut.
func (in riskInput) collateral() map[string]money.Dec {
	out := map[string]money.Dec{}
	for _, h := range in.holdings {
		if h.Pledged <= 0 {
			continue
		}
		if i, ok := in.look.ByISIN(h.ISIN); ok {
			out[h.UCC] = out[h.UCC].Add(i.LTP.MulInt(h.Pledged).Pct(money.DecInt(100).Sub(i.HaircutPct)))
		}
	}
	return out
}

// Override is a temporary extra limit approved by a checker.
type Override struct {
	ID        string    `json:"id"`
	Date      string    `json:"date"`
	UCC       string    `json:"ucc"`
	Amount    money.Dec `json:"amount"`
	Reason    string    `json:"reason"`
	Maker     string    `json:"maker"`
	Checker   string    `json:"checker,omitempty"`
	Status    string    `json:"status"` // Pending, Approved, Rejected
	CreatedAt string    `json:"created_at"`
}

// Overrides lists a date's overrides.
func (o *Ops) Overrides(ctx context.Context, tenant, date string) ([]Override, error) {
	return docs.List[Override](ctx, o.Docs, tenant, cOverride, date+"|")
}

// RiskView is the RMS page: one row per active client plus positions.
type RiskView struct {
	Date      string                    `json:"date"`
	Rows      []RiskRow                 `json:"rows"`
	Positions map[string][]PositionView `json:"positions"`
	Overrides []Override                `json:"overrides"`
	WarnPct   int64                     `json:"warn_pct"`
	Warning   string                    `json:"warning,omitempty"`
}

// Risk computes end-of-day limits for every active client.
func (o *Ops) Risk(ctx context.Context, tenant, date string) (RiskView, error) {
	in, err := o.riskInput(ctx, tenant, date)
	if err != nil {
		return RiskView{}, err
	}
	funds, err := o.clientFunds(ctx, tenant, date, "")
	if err != nil {
		return RiskView{}, err
	}
	ovs, err := o.Overrides(ctx, tenant, date)
	if err != nil {
		return RiskView{}, err
	}
	sq, err := docs.List[SquareOff](ctx, o.Docs, tenant, cSquareOff, date+"|")
	if err != nil {
		return RiskView{}, err
	}
	pos, _ := in.positions("")
	coll := in.collateral()
	v := RiskView{Date: date, Positions: pos, Overrides: ovs, WarnPct: in.set.WarnUtilPct}
	if len(in.look.m) == 0 {
		v.Warning = "Market data is not loaded for this date, so positions are valued at trade price and carry no margin."
	}
	for _, u := range sortedKeys(in.clients) {
		c := in.clients[u]
		if c.Status != "Active" {
			continue
		}
		r := RiskRow{UCC: u, Name: c.Name, Funds: funds[u].Dec(), UPI: c.UPIBlock.Dec(), Collateral: coll[u], Blocked: c.Blocked, Positions: len(pos[u])}
		for _, p := range pos[u] {
			r.Required = r.Required.Add(p.Margin)
			r.FutEq = r.FutEq.Add(p.FutEq)
			if p.Index {
				r.IndexFutEq = r.IndexFutEq.Add(p.FutEq)
			}
		}
		for _, s := range sq {
			if s.UCC == u && s.Status == "Sent to OMS" {
				r.SqOffSent++
			}
		}
		for _, ov := range ovs {
			if ov.UCC == u && ov.Status == "Approved" {
				r.Overrides = r.Overrides.Add(ov.Amount)
			}
		}
		fillRisk(&r, in.set)
		v.Rows = append(v.Rows, r)
	}
	return v, nil
}

func fillRisk(r *RiskRow, set Settings) {
	r.Cash = money.Max(r.Funds, money.Dec{}).Add(r.UPI)
	r.CollUsable = money.Min(r.Collateral, r.Cash)
	usable := r.Cash.Add(r.CollUsable).Add(money.Min(r.Funds, money.Dec{}))
	r.Free = usable.Sub(r.Required)
	base := r.Cash.Add(r.CollUsable)
	if r.Required.Sign() > 0 {
		if base.Sign() > 0 {
			r.Util = r.Required.Float64() / base.Float64()
		} else {
			r.Util = 9.99
		}
	}
	half, _ := r.Required.Quo(money.DecInt(2))
	r.CashShort = r.Required.Sign() > 0 && r.Cash.Cmp(half) < 0
	switch {
	case r.Required.Sign() > 0 && r.Free.Round(money.UnitPaisa) < 0:
		r.Status = "Breach"
	case r.Funds.Sign() < 0:
		r.Status = "Debit"
	case r.Required.Sign() > 0 && r.Util*100 >= float64(set.WarnUtilPct):
		r.Status = "Watch"
	default:
		r.Status = "OK"
	}
	limitCr := money.DecInt(set.IndexNetLimitCr).MulInt(10_000_000)
	r.IndexBreach = set.IndexNetLimitCr > 0 && r.IndexFutEq.Abs().Cmp(limitCr) > 0
	if r.Blocked {
		r.Limit = money.Dec{}
	} else {
		r.Limit = money.Max(r.Free, money.Dec{}).Add(r.Overrides)
	}
}

// CreateOverride asks for a temporary limit increase (maker).
func (o *Ops) CreateOverride(ctx context.Context, tenant, actor, date, ucc string, amount money.Dec, reason string) (Override, error) {
	if err := o.require(ctx, tenant, actor, "Creating a limit override", RoleMaker, RoleRisk); err != nil {
		return Override{}, err
	}
	if amount.Sign() <= 0 {
		return Override{}, invalid("override amount must be above zero")
	}
	if strings.TrimSpace(reason) == "" {
		return Override{}, invalid("a reason is required")
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return Override{}, err
	}
	n, err := o.next(ctx, tenant, "override", actor)
	if err != nil {
		return Override{}, err
	}
	ov := Override{ID: fmt.Sprintf("OV-%04d", n), Date: date, UCC: c.UCC, Amount: amount, Reason: reason, Maker: actor, Status: "Pending", CreatedAt: o.now().Format("15:04")}
	if _, err := docs.Put(ctx, o.Docs, tenant, cOverride, date+"|"+ov.ID, ov, 0, actor); err != nil {
		return Override{}, err
	}
	o.log(ctx, tenant, actor, "rms.override.created", ov.ID, map[string]any{"ucc": ov.UCC, "amount": amount.String(), "reason": reason})
	return ov, nil
}

// DecideOverride approves or rejects an override; the checker must differ
// from the maker.
func (o *Ops) DecideOverride(ctx context.Context, tenant, actor, date, id string, approve bool) (Override, error) {
	if err := o.require(ctx, tenant, actor, "Approving an override", RoleChecker, RoleRisk); err != nil {
		return Override{}, err
	}
	ov, err := docs.Update(ctx, o.Docs, tenant, cOverride, date+"|"+id, actor, func(x *Override) error {
		if x.Status != "Pending" {
			return fmt.Errorf("%w: override %s is %s", ErrState, id, x.Status)
		}
		if x.Maker == actor {
			return fmt.Errorf("%w: the maker cannot approve or reject their own override", ErrForbidden)
		}
		x.Checker = actor
		if approve {
			x.Status = "Approved"
		} else {
			x.Status = "Rejected"
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "rms.override."+strings.ToLower(ov.Status), id, map[string]any{"ucc": ov.UCC, "amount": ov.Amount.String()})
	}
	return ov, err
}

// SquareOff is an instruction to the OMS to close a position.
type SquareOff struct {
	ID         string    `json:"id"`
	Date       string    `json:"date"`
	UCC        string    `json:"ucc"`
	Exchange   string    `json:"exchange"`
	Segment    string    `json:"segment"`
	Instrument string    `json:"instrument"`
	Name       string    `json:"name"`
	Side       string    `json:"side"`
	Qty        int64     `json:"qty"`
	RefPrice   money.Dec `json:"ref_price"`
	Status     string    `json:"status"` // Sent to OMS, Filled
	By         string    `json:"by"`
	At         string    `json:"at"`
	TradeNo    string    `json:"trade_no,omitempty"`
}

// SquareOffResult reports what was sent.
type SquareOffResult struct {
	Instructions []SquareOff `json:"instructions"`
	Filled       int         `json:"filled"`
	RebillNeeded bool        `json:"rebill_needed"`
	Note         string      `json:"note"`
}

// SquareOffClients sends square-off instructions for every open derivatives
// position of the given clients (all breached clients when ucc is empty).
func (o *Ops) SquareOffClients(ctx context.Context, tenant, actor, date string, uccs []string) (SquareOffResult, error) {
	if err := o.require(ctx, tenant, actor, "Square-off", RoleRisk); err != nil {
		return SquareOffResult{}, err
	}
	rv, err := o.Risk(ctx, tenant, date)
	if err != nil {
		return SquareOffResult{}, err
	}
	if len(uccs) == 0 {
		for _, r := range rv.Rows {
			if r.Status == "Breach" && r.Positions > 0 {
				uccs = append(uccs, r.UCC)
			}
		}
	}
	res := SquareOffResult{Instructions: []SquareOff{}}
	existing, _ := docs.List[SquareOff](ctx, o.Docs, tenant, cSquareOff, date+"|")
	pending := map[string]bool{}
	for _, s := range existing {
		if s.Status == "Sent to OMS" {
			pending[s.UCC+"|"+s.Exchange+"|"+s.Segment+"|"+s.Instrument] = true
		}
	}
	var fills []ingest.TradeEvent
	at := o.now().Format("15:04:05")
	for _, u := range uccs {
		for _, p := range rv.Positions[u] {
			if pending[u+"|"+p.Exchange+"|"+p.Segment+"|"+p.Instrument] {
				continue
			}
			n, err := o.next(ctx, tenant, "sqoff", actor)
			if err != nil {
				return res, err
			}
			side := "S"
			if p.Qty < 0 {
				side = "B"
			}
			s := SquareOff{ID: fmt.Sprintf("SQ-%05d", n), Date: date, UCC: u, Exchange: p.Exchange, Segment: p.Segment, Instrument: p.Instrument, Name: p.Name, Side: side, Qty: abs(p.Qty), RefPrice: p.LTP, Status: "Sent to OMS", By: actor, At: at}
			if o.SimulateOMS {
				s.Status, s.TradeNo = "Filled", fmt.Sprintf("9%09d", n)
				parts := strings.Split(p.Instrument, "|")
				ev := ingest.TradeEvent{Tenant: tenant, Origin: ingest.FromExchange, Exchange: p.Exchange, Segment: p.Segment, TradeNo: s.TradeNo,
					OrderNo: fmt.Sprintf("SQO%013d", n), UCC: u, Symbol: parts[0], InstrumentType: parts[1], Expiry: parts[2], Strike: parts[3],
					Side: side, Qty: s.Qty, Price: p.LTP, TradeDate: date, TradeTime: at, Product: "NRML", Terminal: "RMS-SQOFF"}
				om := ev
				om.Origin = ingest.FromOMS
				fills = append(fills, ev, om)
				res.Filled++
			}
			if _, err := docs.Put(ctx, o.Docs, tenant, cSquareOff, date+"|"+s.ID, s, 0, actor); err != nil {
				return res, err
			}
			res.Instructions = append(res.Instructions, s)
		}
	}
	if len(fills) > 0 {
		var ex, om []ingest.TradeEvent
		for _, f := range fills {
			if f.Origin == ingest.FromExchange {
				ex = append(ex, f)
			} else {
				om = append(om, f)
			}
		}
		if _, _, err := o.App.Trades.Add(ctx, ex); err != nil {
			return res, err
		}
		if _, _, err := o.App.Trades.Add(ctx, om); err != nil {
			return res, err
		}
		res.RebillNeeded = true
		res.Note = "Development mode: the simulated OMS filled these orders at the last price. Run billing with rebill to bill the closing trades."
	} else {
		res.Note = "Instructions sent to the OMS. Positions close when the fills arrive in the exchange trade file."
	}
	o.log(ctx, tenant, actor, "rms.squareoff", date, map[string]any{"clients": uccs, "instructions": len(res.Instructions), "simulated_fills": res.Filled})
	return res, nil
}

// MarginRow is one client's margin collection report line.
type MarginRow struct {
	UCC       string    `json:"ucc"`
	Name      string    `json:"name"`
	PAN       string    `json:"pan"`
	Peak      money.Dec `json:"peak"`
	PeakAt    string    `json:"peak_at"`
	EOD       money.Dec `json:"eod"`
	Collected money.Dec `json:"collected"`
	Short     money.Dec `json:"short"`
	ShortAt   string    `json:"short_at,omitempty"`
	ShortPct  float64   `json:"short_pct"`
	SlabPct   money.Dec `json:"slab_pct"`
	Penalty   money.Dec `json:"penalty"`
}

// MarginReport is the client-wise margin collection report.
type MarginReport struct {
	Date      string      `json:"date"`
	Snapshots []string    `json:"snapshots"`
	Rows      []MarginRow `json:"rows"`
	Posted    bool        `json:"penalties_posted"`
}

// Margins computes peak margin from the intraday snapshot times: positions
// at each time are rebuilt from trade timestamps, and collection is the
// client's funds before the day's bills plus UPI blocks and usable collateral.
func (o *Ops) Margins(ctx context.Context, tenant, date string) (MarginReport, error) {
	in, err := o.riskInput(ctx, tenant, date)
	if err != nil {
		return MarginReport{}, err
	}
	pre, err := o.clientFunds(ctx, tenant, date, date)
	if err != nil {
		return MarginReport{}, err
	}
	coll := in.collateral()
	eod, _ := in.positions("")
	rep := MarginReport{Date: date, Snapshots: in.set.SnapshotTimes, Rows: []MarginRow{}}
	type snap struct {
		at  string
		pos map[string][]PositionView
		cm  map[string]money.Dec
	}
	var snaps []snap
	for _, t := range in.set.SnapshotTimes {
		p, cm := in.positions(t)
		snaps = append(snaps, snap{t, p, cm})
	}
	posted, _, _ := docs.GetOr(ctx, o.Docs, tenant, cSnapshotPx, "penalty|"+date, struct{ Posted bool }{})
	rep.Posted = posted.Posted
	for _, u := range sortedKeys(in.clients) {
		c := in.clients[u]
		if c.Status != "Active" {
			continue
		}
		cash := money.Max(pre[u].Dec(), money.Dec{}).Add(c.UPIBlock.Dec())
		collected := cash.Add(money.Min(coll[u], cash))
		row := MarginRow{UCC: u, Name: c.Name, PAN: MaskPAN(c.PAN), Collected: collected}
		for _, p := range eod[u] {
			row.EOD = row.EOD.Add(p.Margin)
		}
		for _, s := range snaps {
			req := s.cm[u]
			for _, p := range s.pos[u] {
				req = req.Add(p.Margin)
			}
			if req.Cmp(row.Peak) > 0 {
				row.Peak, row.PeakAt = req, s.at
			}
			if short := req.Sub(collected); short.Cmp(row.Short) > 0 {
				row.Short, row.ShortAt = short, s.at
				row.ShortPct = short.Float64() / req.Float64()
				lakh := money.DecInt(100000)
				tenPct := req.Pct(money.DecInt(10))
				if short.Cmp(lakh) < 0 && short.Cmp(tenPct) < 0 {
					row.SlabPct = money.MustDec("0.5")
				} else {
					row.SlabPct = money.DecInt(1)
				}
			}
		}
		if row.Peak.Sign() == 0 && row.EOD.Sign() == 0 {
			continue
		}
		row.Penalty = row.Short.Pct(row.SlabPct)
		rep.Rows = append(rep.Rows, row)
	}
	sort.SliceStable(rep.Rows, func(i, j int) bool { return rep.Rows[i].Short.Cmp(rep.Rows[j].Short) > 0 })
	return rep, nil
}

// PostPenalties debits short-collection penalties to clients when the
// tenant passes them through. Idempotent per client and date.
func (o *Ops) PostPenalties(ctx context.Context, tenant, actor, date string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Posting margin penalties", RoleRisk, RoleChecker); err != nil {
		return 0, err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return 0, err
	}
	if !set.PenaltyPassThrough {
		return 0, fmt.Errorf("%w: penalty pass-through is off in Settings", ErrState)
	}
	rep, err := o.Margins(ctx, tenant, date)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rep.Rows {
		amt := r.Penalty.Round(money.UnitPaisa)
		if amt <= 0 {
			continue
		}
		_, created, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "PEN:" + date + ":" + r.UCC, ValueDate: date,
			Narration: fmt.Sprintf("Short margin collection penalty for %s (%s%% on %s)", date, r.SlabPct, r.Short.Fixed(2)),
			Source:    ledger.SourceRef{Kind: "PENALTY", ID: date + "/" + r.UCC}, CreatedBy: actor,
			Postings: []ledger.Posting{{Account: ledger.ClientAccount(r.UCC), Debit: amt}, {Account: ledger.PayablePenalty, Credit: amt}}})
		if err != nil {
			return n, err
		}
		if created {
			n++
		}
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cSnapshotPx, "penalty|"+date, struct{ Posted bool }{true}, actor)
	o.log(ctx, tenant, actor, "margin.penalties.posted", date, map[string]any{"clients": n})
	return n, nil
}
