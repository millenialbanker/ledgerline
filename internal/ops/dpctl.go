package ops

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- DP controls: the blank slip stock, the daily reconciliation, pool
// pay-outs, and the evening's work items ----
//
//   - Blank DIS stock: what came from the printer, what was issued in
//     books, and a physical count every so often (appendix DP-0202; the
//     firm's interval, 30 days unless changed).
//   - The daily reconciliation of what the DP received against what it
//     executed and what is pending, for account openings, modifications,
//     slips, closures, freezes and pledges (SEBI (Depositories and
//     Participants) Regulations 2018, Reg 64; appendix DP-0163, DP-0186,
//     DP-0219, DP-0255, DP-0263, DP-0240). Ledgerline counts from its own
//     records; the checker enters CDSL's figures from CDAS where they
//     differ, and signs the day off.
//   - Pay-outs from the pool to the clients within one working day of the
//     pay-out (CDSL DPOI 6.6.5; CDSL Master Circular for DPs 4.10.2.1),
//     with an estimate of CDSL's charge for a delay, 0.06% of the value a
//     week (DPOI 12.2 (13-15)). Working days leave out trading holidays
//     (CDSL DP2025-829). A part week counts as a week: the estimate is the
//     most CDSL could charge.
//   - A minor's account when the holder turns 18 (appendix DP-0134).

// ---- blank slip stock ----

// DISStockEntry is one movement of the blank slip stock.
type DISStockEntry struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // Received, Issued, Count, Spoilt
	Series  string `json:"series,omitempty"`
	Slips   int64  `json:"slips"`
	Counted int64  `json:"counted,omitempty"` // a count: what was found
	Balance int64  `json:"balance"`           // the book balance after it
	Diff    int64  `json:"diff,omitempty"`    // a count: found less the book balance
	Ref     string `json:"ref,omitempty"`
	Note    string `json:"note,omitempty"`
	On      string `json:"on"`
	By      string `json:"by"`
}

// disStock is the running balance of a series.
type disStock struct {
	Series  string `json:"series"`
	Balance int64  `json:"balance"`
	Since   string `json:"since"`
	Counted string `json:"counted,omitempty"` // the last count
}

func stockKey(series string) string { return "stock|" + firstOf(series, "Regular") }

// disStockTake takes slips out of the stock for a book, when the firm
// keeps the stock in Ledgerline (from its first receipt).
func (o *Ops) disStockTake(ctx context.Context, tenant, series string, n int64) error {
	st, _, err := docs.Get[disStock](ctx, o.Docs, tenant, cDPCtl, stockKey(series))
	if err != nil {
		return nil // stock not kept in Ledgerline
	}
	if st.Balance < n {
		return fmt.Errorf("%w: only %d blank %s slips in stock; record the printer's delivery first", ErrState, st.Balance, firstOf(series, "regular"))
	}
	return o.disStockMove(ctx, tenant, SystemActor, DISStockEntry{Kind: "Issued", Series: series, Slips: n})
}

func (o *Ops) disStockMove(ctx context.Context, tenant, actor string, e DISStockEntry) error {
	today := o.now().In(ist).Format("2006-01-02")
	var after disStock
	_, err := upd(ctx, o.Docs, tenant, cDPCtl, stockKey(e.Series), actor, &disStock{Series: e.Series, Since: today}, func(s *disStock) error {
		switch e.Kind {
		case "Received":
			s.Balance += e.Slips
		case "Issued", "Spoilt":
			s.Balance -= e.Slips
		case "Count":
			e.Diff = e.Counted - s.Balance
			s.Balance, s.Counted = e.Counted, today
		}
		after = *s
		return nil
	})
	if err != nil {
		return err
	}
	n, err := o.next(ctx, tenant, "disstock", actor)
	if err != nil {
		return err
	}
	e.ID, e.Balance, e.On, e.By = fmt.Sprintf("DSK-%05d", n), after.Balance, today, actor
	return docs.Upsert(ctx, o.Docs, tenant, cDPCtl, fmt.Sprintf("move|%08d", n), e, actor)
}

// RecordDISStock records blank slips received from the printer, spoilt
// slips, or a physical count (with the reason for any difference).
func (o *Ops) RecordDISStock(ctx context.Context, tenant, actor string, e DISStockEntry) (DISStockEntry, error) {
	if err := o.require(ctx, tenant, actor, "Keeping the blank DIS stock", RoleMaker, RoleChecker); err != nil {
		return e, err
	}
	if e.Series != "" && e.Series != disSeriesDDPI {
		return e, invalid("the series is Regular (blank) or %s", disSeriesDDPI)
	}
	switch e.Kind {
	case "Received", "Spoilt":
		if e.Slips <= 0 {
			return e, invalid("give the number of slips")
		}
		if e.Kind == "Received" && strings.TrimSpace(e.Ref) == "" {
			return e, invalid("give the printer's delivery reference")
		}
	case "Count":
		if e.Counted < 0 {
			return e, invalid("give the number of blank slips counted")
		}
		if st, _, err := docs.Get[disStock](ctx, o.Docs, tenant, cDPCtl, stockKey(e.Series)); err == nil && st.Balance != e.Counted && strings.TrimSpace(e.Note) == "" {
			return e, invalid("the count differs from the book balance (%d): give the reason", st.Balance)
		}
	default:
		return e, invalid("record Received, Spoilt or Count")
	}
	if err := o.disStockMove(ctx, tenant, actor, e); err != nil {
		return e, err
	}
	o.log(ctx, tenant, actor, "dis.stock_"+strings.ToLower(e.Kind), firstOf(e.Series, "Regular"), map[string]any{"slips": e.Slips, "counted": e.Counted, "ref": e.Ref, "note": e.Note})
	ms, _ := docs.List[DISStockEntry](ctx, o.Docs, tenant, cDPCtl, "move|")
	if len(ms) > 0 {
		return ms[len(ms)-1], nil
	}
	return e, nil
}

// ---- the daily reconciliation ----

// DPReconLine is one kind of work on a day.
type DPReconLine struct {
	Kind     string `json:"kind"`
	Carried  int    `json:"carried"`  // pending at the start of the day
	Received int    `json:"received"` // received on the day
	Executed int    `json:"executed"` // executed (completed) on the day
	Rejected int    `json:"rejected"` // rejected, failed or cancelled on the day
	Pending  int    `json:"pending"`  // pending at the end of the day
	// CDSL is CDSL's count of executed for the day, as the checker
	// entered it (-1: not entered); Diff Executed less CDSL.
	CDSL int `json:"cdsl"`
	Diff int `json:"diff,omitempty"`
}

// DPRecon is a day's reconciliation.
type DPRecon struct {
	Date     string        `json:"date"`
	Lines    []DPReconLine `json:"lines"`
	Balanced bool          `json:"balanced"` // carried + received = executed + rejected + pending, and no CDSL difference
	Signed   string        `json:"signed_by,omitempty"`
	SignedAt string        `json:"signed_at,omitempty"`
	Note     string        `json:"note,omitempty"`
	Stock    []disStock    `json:"stock,omitempty"`
}

type dpReconSign struct {
	Date  string         `json:"date"`
	CDSL  map[string]int `json:"cdsl,omitempty"`
	Note  string         `json:"note,omitempty"`
	By    string         `json:"by"`
	At    string         `json:"at"`
	Lines []DPReconLine  `json:"lines"`
}

// DPReconKinds are the lines of the reconciliation.
var DPReconKinds = []string{"Account openings", "Modifications", "Delivery instructions", "Closures", "Freezes and unfreezes", "Pledges"}

// dpReconItem is one record: when it came in and when it finished.
type dpReconItem struct{ in, done, how string }

func isoDay(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.In(ist).Format("2006-01-02")
	}
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func (o *Ops) dpReconItems(ctx context.Context, tenant string) map[string][]dpReconItem {
	out := map[string][]dpReconItem{}
	crs, _ := docs.List[CDSLRequest](ctx, o.Docs, tenant, cCDSLReq, "")
	for _, r := range crs {
		kind := ""
		switch r.Kind {
		case CDSLOpen, CDSLSetupUpload:
			kind = "Account openings"
		case CDSLModify:
			kind = "Modifications"
		default:
			continue
		}
		it := dpReconItem{in: isoDay(r.At)}
		switch r.Status {
		case crCompleted, crManualDone:
			it.done, it.how = isoDay(firstOf(r.DoneAt, r.Updated)), "x"
		case crRejected, crFailed, crCancelled:
			it.done, it.how = isoDay(r.Updated), "r"
		}
		out[kind] = append(out[kind], it)
	}
	ds, _ := docs.List[DIS](ctx, o.Docs, tenant, cDIS, "")
	for _, d := range ds {
		it := dpReconItem{in: d.Received}
		switch d.Status {
		case "Executed":
			it.done, it.how = firstOf(d.DoneOn, d.ExecDate), "x"
		case "Rejected", "Failed":
			it.done, it.how = firstOf(d.DoneOn, d.ExecDate), "r"
		}
		out["Delivery instructions"] = append(out["Delivery instructions"], it)
	}
	rs, _ := docs.List[DPRequest](ctx, o.Docs, tenant, cDPReq, "")
	for _, r := range rs {
		kind := map[string]string{"Closure": "Closures", "Freeze": "Freezes and unfreezes", "Unfreeze": "Freezes and unfreezes"}[r.Kind]
		if kind == "" {
			continue
		}
		it := dpReconItem{in: r.day()}
		switch r.Status {
		case "Completed":
			it.done, it.how = firstOf(r.DoneOn, r.day()), "x"
		case "Rejected":
			it.done, it.how = firstOf(r.DoneOn, r.day()), "r"
		}
		out[kind] = append(out[kind], it)
	}
	ps, _ := docs.List[PledgeRequest](ctx, o.Docs, tenant, cPledge, "")
	for _, p := range ps {
		it := dpReconItem{in: isoDay(p.Time)}
		switch p.Status {
		case "Accepted":
			it.done, it.how = isoDay(firstOf(p.DoneAt, p.Time)), "x"
		case "Rejected", "Failed":
			it.done, it.how = isoDay(firstOf(p.DoneAt, p.Time)), "r"
		}
		out["Pledges"] = append(out["Pledges"], it)
	}
	return out
}

// DPReconcile is the day's reconciliation, with the sign-off if any.
func (o *Ops) DPReconcile(ctx context.Context, tenant, actor, date string) (DPRecon, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the DP controls", RoleMaker, RoleChecker, RoleCompliance, RoleBranch); err != nil {
		return DPRecon{}, err
	}
	if !validDate(date) {
		return DPRecon{}, invalid("give a date (YYYY-MM-DD)")
	}
	return o.dpRecon(ctx, tenant, date), nil
}

func (o *Ops) dpRecon(ctx context.Context, tenant, date string) DPRecon {
	sign, _, signErr := docs.Get[dpReconSign](ctx, o.Docs, tenant, cDPCtl, "recon|"+date)
	items := o.dpReconItems(ctx, tenant)
	out := DPRecon{Date: date, Balanced: true}
	for _, k := range DPReconKinds {
		l := DPReconLine{Kind: k, CDSL: -1}
		for _, it := range items[k] {
			if it.in > date || (it.done != "" && it.done < date) {
				continue
			}
			if it.in < date {
				l.Carried++
			} else {
				l.Received++
			}
			switch {
			case it.done == date && it.how == "x":
				l.Executed++
			case it.done == date:
				l.Rejected++
			default:
				l.Pending++
			}
		}
		if signErr == nil {
			if v, ok := sign.CDSL[k]; ok {
				l.CDSL, l.Diff = v, l.Executed-v
			}
		}
		if l.Carried+l.Received != l.Executed+l.Rejected+l.Pending || l.Diff != 0 {
			out.Balanced = false
		}
		out.Lines = append(out.Lines, l)
	}
	if signErr == nil {
		out.Signed, out.SignedAt, out.Note = sign.By, sign.At, sign.Note
	}
	st, _ := docs.List[disStock](ctx, o.Docs, tenant, cDPCtl, "stock|")
	out.Stock = st
	return out
}

// SignDPRecon signs the day off (a checker), with CDSL's counts of what
// it executed where entered; a difference needs a note.
func (o *Ops) SignDPRecon(ctx context.Context, tenant, actor, date string, cdsl map[string]int, note string) (DPRecon, error) {
	if err := o.require(ctx, tenant, actor, "Signing off the DP reconciliation", RoleChecker); err != nil {
		return DPRecon{}, err
	}
	if !validDate(date) || date > o.now().In(ist).Format("2006-01-02") {
		return DPRecon{}, invalid("give a past date (YYYY-MM-DD)")
	}
	for k, v := range cdsl {
		if !contains(DPReconKinds, k) || v < 0 {
			return DPRecon{}, invalid("unknown line %q or a negative count", k)
		}
	}
	s := dpReconSign{Date: date, CDSL: cdsl, Note: strings.TrimSpace(note), By: actor, At: o.now().Format(time.RFC3339)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cDPCtl, "recon|"+date, s, actor); err != nil {
		return DPRecon{}, err
	}
	r := o.dpRecon(ctx, tenant, date)
	if !r.Balanced && s.Note == "" {
		_ = docs.Upsert(ctx, o.Docs, tenant, cDPCtl, "recon|"+date, dpReconSign{Date: date}, actor)
		return r, invalid("Ledgerline's figures differ from CDSL's: explain the difference in the note")
	}
	s.Lines = r.Lines
	if err := docs.Upsert(ctx, o.Docs, tenant, cDPCtl, "recon|"+date, s, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "dp.recon_signed", date, map[string]any{"cdsl": cdsl, "note": note})
	o.closeWork(ctx, tenant, actor, wDPCtl, "recon:"+date, "signed")
	return o.dpRecon(ctx, tenant, date), nil
}

// ---- pool pay-outs ----

// PoolPayout is a client's pay-out from the pool account.
type PoolPayout struct {
	TradeDate string    `json:"trade_date"`
	PayOut    string    `json:"pay_out"`
	Due       string    `json:"due"` // one working day after the pay-out
	UCC       string    `json:"ucc"`
	Symbol    string    `json:"symbol"`
	ISIN      string    `json:"isin"`
	Qty       int64     `json:"qty"`
	Value     money.Dec `json:"value"`
	Status    string    `json:"status"`
	Paid      string    `json:"paid,omitempty"` // the day it was moved to the client
	Late      int       `json:"late_days,omitempty"`
	// Charge is the estimate of CDSL's charge for the delay: 0.06% of
	// the value for each week or part of a week late.
	Charge money.Dec `json:"charge,omitempty"`
}

// cdslPoolRate is CDSL's charge for a late transfer from the pool, % of
// the value a week (DPOI 12.2 (13-15)).
var cdslPoolRate = money.MustDec("0.06")

// poolPayouts are the pay-outs of the trade dates from..to.
func (o *Ops) poolPayouts(ctx context.Context, tenant, from, to string) []PoolPayout {
	set, _ := o.Settings(ctx, tenant)
	today := o.now().In(ist).Format("2006-01-02")
	var out []PoolPayout
	for d := from; d <= to; d = addDays(d, 1) {
		if !set.IsBusinessDay(d) {
			continue
		}
		states, _ := docs.List[settleState](ctx, o.Docs, tenant, cSettle, d+"|")
		stDocs, _ := o.Docs.List(ctx, tenant, cSettle, d+"|")
		paid := map[string]settleState{}
		for i, x := range stDocs {
			paid[strings.TrimPrefix(x.ID, d+"|")] = states[i]
		}
		st, err := o.Obligations(ctx, tenant, d)
		if err != nil {
			continue
		}
		for _, ob := range st.Obligations {
			if ob.NetQty <= 0 || strings.HasPrefix(ob.ISIN, "UNKNOWN:") || ob.Status == "Held in CUSPA" || ob.Status == "Funds shortfall" {
				continue
			}
			p := PoolPayout{TradeDate: d, PayOut: st.PayIn, Due: set.NextBusinessDay(st.PayIn), UCC: ob.UCC, Symbol: ob.Symbol, ISIN: ob.ISIN, Qty: ob.NetQty, Value: ob.Funds.Abs(), Status: ob.Status}
			if s, ok := paid[ob.Key]; ok && s.Status == "Paid out" {
				p.Paid = s.On
				if p.Paid == "" {
					continue // paid before Ledgerline kept the day: not judged
				}
			}
			end := firstOf(p.Paid, today)
			if end > p.Due {
				p.Late = daysBetween(p.Due, end)
				weeks := int64(math.Ceil(float64(p.Late) / 7))
				p.Charge = p.Value.Pct(cdslPoolRate).MulInt(weeks).Round(money.UnitPaisa).Dec()
			}
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Late > out[j].Late })
	return out
}

// PoolPayouts lists the pay-outs of the last days, late ones first.
func (o *Ops) PoolPayouts(ctx context.Context, tenant, actor, from, to string) ([]PoolPayout, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the DP controls", RoleMaker, RoleChecker, RoleCompliance, RoleBranch); err != nil {
		return nil, err
	}
	if !validDate(from) || !validDate(to) || from > to || daysBetween(from, to) > 62 {
		return nil, invalid("give a period of at most two months")
	}
	ps := o.poolPayouts(ctx, tenant, from, to)
	if ps == nil {
		ps = []PoolPayout{}
	}
	return ps, nil
}

// ---- the DP controls view ----

// DPControlsView is the panel on the Depository screen.
type DPControlsView struct {
	Config    DPControls                   `json:"config"`
	Books     []DISBook                    `json:"books_awaiting_co"`
	Calls     []DIS                        `json:"calls"`       // slips waiting for the call or its approval
	Counter   []DIS                        `json:"countersign"` // verified slips above ₹5 lakh not countersigned
	Requests  []DPRequestTimeline          `json:"requests"`    // with a duty not done
	Recon     DPRecon                      `json:"recon"`       // the previous business day
	Stock     []DISStockEntry              `json:"stock"`       // the latest stock movements
	Pool      []PoolPayout                 `json:"pool_late"`   // late pay-outs of the last 10 days
	StepNames map[string]map[string]string `json:"step_names"`
}

// DPControlsPanel is what the DP controls panel shows.
func (o *Ops) DPControlsPanel(ctx context.Context, tenant, actor string) (DPControlsView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the DP controls", RoleMaker, RoleChecker, RoleCompliance, RoleBranch); err != nil {
		return DPControlsView{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	today := o.now().In(ist).Format("2006-01-02")
	v := DPControlsView{Config: o.DPControlsConfig(ctx, tenant), Books: []DISBook{}, Calls: []DIS{}, Counter: []DIS{}, Requests: []DPRequestTimeline{}, Stock: []DISStockEntry{}, Pool: []PoolPayout{}, StepNames: DPStepNames}
	books, _ := docs.List[DISBook](ctx, o.Docs, tenant, cDISBook, "")
	for _, b := range books {
		if b.AwaitingCO != "" && b.AuthorisedBy == "" {
			v.Books = append(v.Books, b)
		}
	}
	ds, _ := docs.List[DIS](ctx, o.Docs, tenant, cDIS, "")
	for _, d := range ds {
		switch {
		case d.Status == "Entered" && len(d.CallNeeded) > 0 && (d.Call == nil || d.Call.Approved == ""):
			v.Calls = append(v.Calls, d)
		case d.Status == "Verified" && d.disReady() != "":
			v.Counter = append(v.Counter, d)
		}
	}
	ts, _ := o.DPRequestTimelines(ctx, tenant, actor)
	for _, t := range ts {
		for _, d := range t.Duties {
			if d.Done == "" {
				v.Requests = append(v.Requests, t)
				break
			}
		}
	}
	v.Recon = o.dpRecon(ctx, tenant, set.PrevBusinessDay(today))
	ms, _ := docs.List[DISStockEntry](ctx, o.Docs, tenant, cDPCtl, "move|")
	for i := len(ms) - 1; i >= 0 && len(v.Stock) < 10; i-- {
		v.Stock = append(v.Stock, ms[i])
	}
	for _, p := range o.poolPayouts(ctx, tenant, addDays(today, -14), today) {
		if p.Late > 0 {
			v.Pool = append(v.Pool, p)
		}
	}
	return v, nil
}

// ---- the evening's work items ----

type dpCtlTick struct {
	Day string `json:"day"`
}

// dpControlsTick runs once a day after 18:00 IST: request duties falling
// due, books and calls waiting for the compliance officer, the previous
// business day's reconciliation not signed off, the blank stock count
// due, late pool pay-outs and minors turning 18. The trading freeze
// requests have their own tick, every run (tradefreeze.go).
func (o *Ops) dpControlsTick(ctx context.Context, tenant string, now time.Time) {
	o.tradeFreezeTick(ctx, tenant, now)
	t := now.In(ist)
	day := t.Format("2006-01-02")
	if t.Hour() < 18 {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cDPCtl, "tick", dpCtlTick{})
	if st.Day == day {
		return
	}
	st.Day = day
	_ = docs.Upsert(ctx, o.Docs, tenant, cDPCtl, "tick", st, SystemActor)
	o.dpControlsDaily(ctx, tenant, day)
}

func (o *Ops) dpControlsDaily(ctx context.Context, tenant, day string) {
	set, _ := o.Settings(ctx, tenant)
	rs, _ := docs.List[DPRequest](ctx, o.Docs, tenant, cDPReq, "")
	for _, r := range rs {
		if r.DoneOn != "" && r.DoneOn < addDays(day, -60) {
			continue
		}
		o.dpReqWork(ctx, tenant, r, day)
	}
	books, _ := docs.List[DISBook](ctx, o.Docs, tenant, cDISBook, "")
	for _, b := range books {
		if b.AwaitingCO == "" {
			continue
		}
		if b.AuthorisedBy != "" {
			o.closeWork(ctx, tenant, SystemActor, wDPCtl, "book:"+b.ID, "authorised")
			continue
		}
		o.raiseWork(ctx, tenant, wDPCtl, "book:"+b.ID, "Authorise DIS book "+b.ID+" for "+b.UCC, "The account had "+b.AwaitingCO+": the book goes to the registered address only with the compliance officer's authorisation (SEBI Master Circular for Depositories 1.12.4).", b.UCC, RoleCompliance, "dp", b.Issued)
	}
	ds, _ := docs.List[DIS](ctx, o.Docs, tenant, cDIS, "")
	for _, d := range ds {
		key := "call:" + d.ID
		if d.Status != "Entered" || len(d.CallNeeded) == 0 || (d.Call != nil && d.Call.Approved != "") {
			o.closeWork(ctx, tenant, SystemActor, wDPCtl, key, "done")
			continue
		}
		role, what := RoleMaker, "Call the holder on the registered number and record the call"
		if d.Call != nil {
			role, what = RoleCompliance, "Approve the recorded call"
		}
		o.raiseWork(ctx, tenant, wDPCtl, key, what+" for slip "+d.ID+" ("+d.UCC+")", "Before the slip is verified: "+strings.Join(d.CallNeeded, "; ")+" (SEBI Master Circular for Depositories 1.12.8; CDSL DPOI 6.5.4.9).", d.UCC, role, "dp", d.Received)
	}
	// Yesterday's reconciliation.
	if prev := set.PrevBusinessDay(day); prev != "" {
		if r := o.dpRecon(ctx, tenant, prev); r.Signed == "" {
			o.raiseWork(ctx, tenant, wDPCtl, "recon:"+prev, "Sign off the DP reconciliation of "+fmtDay(prev), "What the DP received, executed and has pending for "+fmtDay(prev)+": openings, modifications, slips, closures, freezes and pledges. Check against CDAS and sign it off on Depository → Demat accounts & DIS (DP controls).", "", RoleChecker, "dp", prev)
		}
	}
	// The blank stock count.
	cfg := o.DPControlsConfig(ctx, tenant)
	stocks, _ := docs.List[disStock](ctx, o.Docs, tenant, cDPCtl, "stock|")
	for _, s := range stocks {
		last := firstOf(s.Counted, s.Since)
		if due := addDays(last, cfg.stockDays()); due <= day {
			o.raiseWork(ctx, tenant, wDPCtl, "stock:"+firstOf(s.Series, "Regular")+":"+due, "Count the blank "+firstOf(s.Series, "regular")+" DIS stock", fmt.Sprintf("The last count was on %s; the stock is counted every %d days. Record the count on Depository → DP controls.", fmtDay(last), cfg.stockDays()), "", RoleMaker, "dp", day)
		}
	}
	// Pool pay-outs not moved within a working day.
	for _, p := range o.poolPayouts(ctx, tenant, addDays(day, -10), day) {
		if p.Late > 0 && p.Paid == "" {
			o.raiseWork(ctx, tenant, wDPCtl, "pool:"+p.TradeDate+":"+p.UCC+":"+p.ISIN, "Move "+p.UCC+"'s "+p.Symbol+" from the pool", fmt.Sprintf("Pay-out of %s; due in the client's account by %s (one working day). CDSL may charge 0.06%% of the value a week of delay: about ₹%s so far.", fmtDay(p.PayOut), fmtDay(p.Due), p.Charge.Fixed(2)), p.UCC, RoleMaker, "settlement", p.PayOut)
		}
	}
	o.minorsTurning18(ctx, tenant, day)
}

// minorsTurning18 raises a work item for each of the DP's accounts opened
// for a minor whose holder has turned 18 (appendix DP-0134): the account
// is changed from minor to major with the holder's own KYC and signature.
func (o *Ops) minorsTurning18(ctx context.Context, tenant, day string) {
	cs, _ := o.ClientMap(ctx, tenant)
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Status != "Active" || len(c.DOB) < 10 || (c.Type != "" && c.Type != "Individual") {
			continue
		}
		major := addMonths(c.DOB, 18*12)
		if major > day || addDays(major, 365) < day {
			continue
		}
		for _, b := range o.boAccountsOf(ctx, tenant, u) {
			if b.Status == "Closed" || b.Opened == "" || b.Opened >= major {
				continue
			}
			o.raiseWork(ctx, tenant, wDPCtl, "major:"+b.BOID, u+" has turned 18: change the demat account from minor to major", fmt.Sprintf("%s turned 18 on %s. The account %s was opened while the holder was a minor: take the holder's own KYC, signature and bank details, remove the guardian, and modify the account at CDSL.", c.Name, fmtDay(major), b.BOID), u, RoleMaker, "dp", major)
		}
	}
}
