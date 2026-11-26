package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/money"
)

// ---- ICCL risk: pre-delivery margin and the trading limit ----
//
// BSE2-N0011, ICCL notice 20250724-4 (pre-delivery margins for physically
// settled stock derivatives): a long in-the-money stock option position
// on BSE carries a staggered pre-delivery margin from four trading days
// before expiry: 10% (E-4), 25% (E-3), 45% (E-2), 70% (E-1) and 100% on
// expiry day. The notice's text is not in Ledgerline; the safe reading
// taken here: the percentage is of the margin on the deliverable value
// (underlying price × quantity × the security's VaR plus extreme loss
// rate as last loaded), the position is ITM on the underlying's latest
// price, and the margin is added to the client's required margin in the
// ICCL margin reporting review and raised as a work item to collect.
//
// NCL-BSE-0132, ICCL notice 20251024-2 (consolidated penalties, tables
// 1.1-1.4): staying within BSECL's trading limit (RRM) and position limits.
// Position limits (client, own account, member) are checked for BSE
// positions too on RMS → Position limits, from the position limit file
// loaded for BSE (rmslimits.go). The trading limit is BSECL's: Ledgerline
// cannot read BSECL's RRM figure (no layout), so it estimates it as the
// firm's free deposits with ICCL (Clearing deposits: value above base
// capital) plus clients' collateral allocated there, against the margin
// of every position cleared at ICCL, and raises a work item at the watch
// level and on a breach. A breach costs 0.07% a day plus slab amounts.

// preDeliveryPct is the share of the margin by trading days to expiry.
var preDeliveryPct = map[int]int64{4: 10, 3: 25, 2: 45, 1: 70, 0: 100}

// PreDeliveryRow is one client's pre-delivery margin on one contract.
type PreDeliveryRow struct {
	UCC        string    `json:"ucc"`
	Name       string    `json:"name"`
	Contract   string    `json:"contract"`
	Expiry     string    `json:"expiry"`
	DaysLeft   int       `json:"days_left"` // trading days to expiry
	Qty        int64     `json:"qty"`
	Underlying money.Dec `json:"underlying"`
	Strike     money.Dec `json:"strike"`
	Rate       money.Dec `json:"rate"` // VaR + ELM, per cent
	Pct        int64     `json:"pct"`
	Margin     money.Dec `json:"margin"`
	Note       string    `json:"note,omitempty"`
}

// preDeliveryRows are the date's pre-delivery margins on BSE stock options
// cleared at ICCL.
func (o *Ops) preDeliveryRows(ctx context.Context, tenant string, set Settings, date string) ([]PreDeliveryRow, error) {
	if set.ClearedThrough("BSE-FO") != "ICCL" {
		return nil, nil
	}
	rv, err := o.Risk(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	_, look, _ := o.Market(ctx, tenant, date)
	rates, _ := latestDoc[SecVaRs](ctx, o, tenant, cSecVaR, date)
	cs, _ := o.ClientMap(ctx, tenant)
	// Trading days to each expiry, up to four.
	left := func(exp string) int {
		d := date
		for n := 0; n <= 4; n++ {
			if d == exp {
				return n
			}
			d = set.nextBusinessDay(d)
		}
		return -1
	}
	var out []PreDeliveryRow
	for _, u := range sortedKeys(rv.Positions) {
		for _, p := range rv.Positions[u] {
			parts := strings.Split(p.Instrument, "|")
			if p.Exchange != "BSE" || p.Segment != "FO" || p.Index || p.Qty <= 0 || len(parts) < 4 {
				continue
			}
			sym, typ, exp, strike := parts[0], parts[1], parts[2], parts[3]
			if typ != "CE" && typ != "PE" {
				continue
			}
			n := left(exp)
			if n < 0 {
				continue
			}
			ins, _ := look.Get(p.Exchange, p.Segment, p.Instrument)
			und := ins.Underlying
			eq, eqOK := look.Get("NSE", "CM", sym+"|EQ||")
			if und.Sign() <= 0 && eqOK {
				und = eq.LTP
			}
			k, _ := money.ParseDec(strike)
			if und.Sign() <= 0 || !((typ == "CE" && und.Cmp(k) > 0) || (typ == "PE" && und.Cmp(k) < 0)) {
				continue
			}
			r := PreDeliveryRow{UCC: u, Name: cs[u].Name, Contract: firstOf(p.Name, p.Instrument), Expiry: exp, DaysLeft: n, Qty: p.Qty, Underlying: und, Strike: k, Pct: preDeliveryPct[n]}
			if eqOK && eq.ISIN != "" {
				if v, ok := rates.Rates[strings.ToUpper(eq.ISIN)]; ok {
					r.Rate = v.VaR.Add(v.ELM)
				}
			}
			if r.Rate.Sign() <= 0 {
				r.Note = "no VaR and extreme loss rate loaded for " + sym + ": load the day's VaR file; the margin is not counted"
			} else {
				r.Margin = und.MulInt(p.Qty).Pct(r.Rate).Pct(money.DecInt(r.Pct)).Round(money.UnitPaisa).Dec()
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// raisePreDelivery raises one work item per client and expiry to collect
// the pre-delivery margin before the day's end.
func (o *Ops) raisePreDelivery(ctx context.Context, tenant string, set Settings, date string) int {
	rows, _ := o.preDeliveryRows(ctx, tenant, set, date)
	n := 0
	for _, r := range rows {
		if r.Margin.Sign() <= 0 {
			continue
		}
		o.raiseWork(ctx, tenant, wICCL, "predelivery|"+date+"|"+r.UCC+"|"+r.Contract, fmt.Sprintf("Collect pre-delivery margin ₹%s from %s (%s)", formatINR(r.Margin), r.UCC, r.Contract),
			fmt.Sprintf("%s holds %d of %s, in the money with %d trading day(s) to expiry on %s. ICCL charges %d%% of the delivery margin before expiry (notice 20250724-4): about ₹%s at a %s%% rate on the underlying at ₹%s. Collect it today and include it in the margin reported to ICCL; or close the position.",
				r.UCC, r.Qty, r.Contract, r.DaysLeft, fmtDay(r.Expiry), r.Pct, formatINR(r.Margin), r.Rate.Fixed(2), r.Underlying.Fixed(2)), r.UCC, RoleRisk, "rms", date)
		n++
	}
	return n
}

// ICCLTradingLimit is Ledgerline's estimate of the firm's use of its
// trading limit at ICCL.
type ICCLTradingLimit struct {
	Deposits money.Dec `json:"deposits"` // the firm's deposits with ICCL above base capital
	Clients  money.Dec `json:"clients"`  // clients' collateral allocated at ICCL
	Limit    money.Dec `json:"limit"`
	Used     money.Dec `json:"used"` // margin of positions cleared at ICCL
	Pct      float64   `json:"pct"`
	Status   string    `json:"status"`
	Basis    string    `json:"basis"`
}

func (o *Ops) icclTradingLimit(ctx context.Context, tenant string, ms []ICCLMarginRow, date string) ICCLTradingLimit {
	t := ICCLTradingLimit{Basis: "Ledgerline's estimate: BSECL's own trading limit (RRM) figure is on its extranet. Limit = the firm's deposits with ICCL above base capital (Clearing deposits) plus clients' collateral allocated there; used = the margin of every position cleared at ICCL."}
	if dv, err := o.Deposits(ctx, tenant, date); err == nil {
		for _, c := range dv.ByCC {
			if c.CC == "ICCL" {
				t.Deposits = money.Max(money.Dec{}, c.Value.Sub(c.BMC))
			}
		}
	}
	for _, m := range ms {
		t.Used = t.Used.Add(m.Required)
		if !m.Own {
			t.Clients = t.Clients.Add(m.EOD)
		}
	}
	t.Limit = t.Deposits.Add(t.Clients)
	switch {
	case t.Limit.Sign() > 0:
		t.Pct = limPct(t.Used, t.Limit)
		t.Status = limitStatus(t.Pct, o.RiskLimitConfig(ctx, tenant).WarnPct)
	case t.Used.Sign() > 0:
		t.Pct, t.Status = 100, "Breach"
	default:
		t.Status = "OK"
	}
	return t
}

func (o *Ops) raiseTradingLimit(ctx context.Context, tenant string, t ICCLTradingLimit, date string) {
	if t.Status == "OK" {
		return
	}
	o.raiseWork(ctx, tenant, wICCL, "tradinglimit|"+date, fmt.Sprintf("ICCL trading limit %s: %.0f%% used", strings.ToLower(t.Status), t.Pct),
		fmt.Sprintf("The margin of positions cleared at ICCL (₹%s) is %.0f%% of the estimated trading limit (₹%s: deposits above base capital ₹%s, clients' collateral ₹%s). Check BSECL's RRM figure on its extranet; add deposits or reduce positions. A trading-limit breach costs 0.07%% a day plus slab amounts (ICCL notice 20251024-2).",
			formatINR(t.Used), t.Pct, formatINR(t.Limit), formatINR(t.Deposits), formatINR(t.Clients)), "", RoleRisk, "deposits", date)
}

func sortPreDelivery(rs []PreDeliveryRow) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Margin.Cmp(rs[j].Margin) > 0 })
}
