package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- running consecutive days ----
//
// A trading day D starts from what day D-1 left behind: open futures and
// options carried forward, holdings after settlement, the ledger. The chain is:
//
//	BOD   carried positions for D exist (rolled at D-1's close, or loaded
//	      from the clearing corporation's POSITIONS file on the first day)
//	day   trades, reconciliation
//	bill  trade bills, plus carried-futures MTM against D's settlement price
//	      and option exercise / assignment on expiry
//	EOD   close D, which rolls end-of-day positions into D+1: futures at
//	      D's settlement price (already settled through MTM), options at their
//	      average premium, expired contracts dropped
//
// Billing a day is refused while the previous trading day is still open,
// because its positions would not have carried forward yet.

// PrevBusinessDay is the trading day before date.
func (s Settings) PrevBusinessDay(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	hol := map[string]bool{}
	for _, h := range s.Holidays {
		hol[h] = true
	}
	for {
		t = t.AddDate(0, 0, -1)
		d := t.Format("2006-01-02")
		if t.Weekday() != time.Saturday && t.Weekday() != time.Sunday && !hol[d] {
			return d
		}
	}
}

// hadActivity reports whether a day has anything that must roll forward.
func (o *Ops) hadActivity(ctx context.Context, tenant, date string) (bool, error) {
	ts, err := o.App.Trades.List(ctx, tenant, ingest.FromExchange, date)
	if err != nil {
		return false, err
	}
	if len(ts) > 0 {
		return true, nil
	}
	cs, err := o.carried(ctx, tenant, date)
	return len(cs) > 0, err
}

// DayOpenError explains why a day cannot be worked yet.
func (o *Ops) DayOpenError(ctx context.Context, tenant, date string) error {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return err
	}
	prev := set.PrevBusinessDay(date)
	act, err := o.hadActivity(ctx, tenant, prev)
	if err != nil || !act {
		return err
	}
	st, _, err := docs.GetOr(ctx, o.Docs, tenant, cEOD, prev, EODState{})
	if err != nil {
		return err
	}
	if !st.Closed {
		return fmt.Errorf("%w: close %s first; its open positions carry into %s", ErrState, prev, date)
	}
	return nil
}

// DerivResult is what derivative settlement posted for a day.
type DerivResult struct {
	Date      string   `json:"date"`
	MTM       []string `json:"mtm"`    // carried-futures MTM entries posted
	Expiry    []string `json:"expiry"` // exercise / assignment entries posted
	Existing  int      `json:"existing"`
	Warnings  []string `json:"warnings"`
	Positions int      `json:"carried_futures"`
}

// SettleDerivatives posts, for one trade date, the MTM on futures carried in
// from the previous day (quantity × (today's settlement − carried price)) and
// the exercise and assignment value of options expiring today (net position
// × intrinsic value at the underlying's closing price). Each client and
// exchange segment gets one entry per kind; re-running is idempotent.
func (o *Ops) SettleDerivatives(ctx context.Context, tenant, actor, date string) (DerivResult, error) {
	res := DerivResult{Date: date, MTM: []string{}, Expiry: []string{}, Warnings: []string{}}
	in, err := o.riskInput(ctx, tenant, date)
	if err != nil {
		return res, err
	}
	type grp struct{ amt money.Dec }
	mtm := map[string]*grp{}
	var missing []string
	for _, c := range in.carried {
		parts := strings.Split(c.Instrument, "|")
		if len(parts) < 2 || parts[1] != "FUT" {
			continue
		}
		res.Positions++
		inst, ok := in.look.Get(c.Exchange, c.Segment, c.Instrument)
		if !ok || inst.Close.IsZero() {
			missing = append(missing, c.Exchange+"-"+c.Segment+" "+c.Instrument)
			continue
		}
		k := c.UCC + "|" + c.Exchange + "-" + c.Segment
		if mtm[k] == nil {
			mtm[k] = &grp{}
		}
		mtm[k].amt = mtm[k].amt.Add(inst.Close.Sub(c.AvgPrice).MulInt(c.Qty))
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return res, fmt.Errorf("%w: no settlement price for carried futures %s; load the day's market data", ErrInvalid, strings.Join(missing, ", "))
	}
	exp := map[string]*grp{}
	pos, _ := in.positions("")
	for _, ps := range pos {
		for _, p := range ps {
			parts := strings.Split(p.Instrument, "|")
			if len(parts) < 4 || (parts[1] != "CE" && parts[1] != "PE") || parts[2] != date {
				continue
			}
			inst, ok := in.look.Get(p.Exchange, p.Segment, p.Instrument)
			if !ok || inst.Underlying.IsZero() {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s %s expires today but the underlying price is missing; exercise not posted", p.UCC, p.Name))
				continue
			}
			strike, err := money.ParseDec(parts[3])
			if err != nil {
				continue
			}
			intrinsic := inst.Underlying.Sub(strike)
			if parts[1] == "PE" {
				intrinsic = intrinsic.Neg()
			}
			if intrinsic.Sign() <= 0 {
				continue // out of the money: expires worthless
			}
			k := p.UCC + "|" + p.Exchange + "-" + p.Segment
			if exp[k] == nil {
				exp[k] = &grp{}
			}
			exp[k].amt = exp[k].amt.Add(intrinsic.MulInt(p.Qty))
		}
	}
	post := func(kind, label string, m map[string]*grp, out *[]string) error {
		for _, k := range sortedKeys(m) {
			ucc, seg, _ := strings.Cut(k, "|")
			exch, sg, _ := strings.Cut(seg, "-")
			net := m[k].amt.Round(money.UnitPaisa)
			if net == 0 {
				continue
			}
			client, cc := ledger.ClientAccount(ucc), ledger.ClearingAccount(exch, sg)
			var ps []ledger.Posting
			if net > 0 {
				ps = []ledger.Posting{{Account: cc, Debit: net, Memo: label}, {Account: client, Credit: net, Memo: "Trade value / MTM"}}
			} else {
				ps = []ledger.Posting{{Account: client, Debit: -net, Memo: "Trade value / MTM"}, {Account: cc, Credit: -net, Memo: label}}
			}
			e, created, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: kind + ":" + date + ":" + ucc + ":" + seg, ValueDate: date,
				Narration: fmt.Sprintf("%s %s %s", label, seg, date), Source: ledger.SourceRef{Kind: kind, ID: date + "/" + ucc + "/" + seg}, Postings: ps, CreatedBy: actor})
			if err != nil {
				return fmt.Errorf("%s %s: %w", ucc, seg, err)
			}
			if created {
				*out = append(*out, e.ID)
			} else {
				res.Existing++
			}
		}
		return nil
	}
	if err := post("MTM", "Carried futures MTM", mtm, &res.MTM); err != nil {
		return res, err
	}
	if err := post("EXPIRY", "Option exercise / assignment", exp, &res.Expiry); err != nil {
		return res, err
	}
	if len(res.MTM)+len(res.Expiry) > 0 {
		o.log(ctx, tenant, actor, "derivatives.settled", date, map[string]any{"mtm": len(res.MTM), "expiry": len(res.Expiry)})
	}
	return res, nil
}

// derivKinds are the ledger entry kinds that settle derivatives money.
var derivKinds = map[string]bool{"BILL": true, "MTM": true, "EXPIRY": true}

// RollPositions carries a closed day's end-of-day derivatives positions into
// the next trading day. Futures carry at the day's settlement price (their
// MTM is settled); options at their average premium. Contracts expiring on
// or before the day are dropped.
func (o *Ops) RollPositions(ctx context.Context, tenant, actor, date string) (string, int, error) {
	in, err := o.riskInput(ctx, tenant, date)
	if err != nil {
		return "", 0, err
	}
	next := in.set.NextBusinessDay(date)
	pos, _ := in.positions("")
	n := 0
	for _, u := range sortedKeys(pos) {
		for _, p := range pos[u] {
			parts := strings.Split(p.Instrument, "|")
			if len(parts) >= 3 && parts[2] != "" && parts[2] <= date {
				continue // expired
			}
			avg := p.AvgPrice
			if parts[1] == "FUT" {
				if inst, ok := in.look.Get(p.Exchange, p.Segment, p.Instrument); ok && !inst.Close.IsZero() {
					avg = inst.Close
				}
			}
			cp := Position{Date: next, UCC: p.UCC, Exchange: p.Exchange, Segment: p.Segment, Instrument: p.Instrument, Qty: p.Qty, AvgPrice: avg}
			if err := docs.Upsert(ctx, o.Docs, tenant, cPosition, next+"|"+p.UCC+"|"+p.Exchange+"|"+p.Segment+"|"+p.Instrument, cp, actor); err != nil {
				return next, n, err
			}
			n++
		}
	}
	o.log(ctx, tenant, actor, "positions.rolled", date, map[string]any{"to": next, "positions": n})
	return next, n, nil
}
