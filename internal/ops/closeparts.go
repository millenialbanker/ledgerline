package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- closing the day in parts: equity and commodity ----
//
// NSE and BSE (cash, F&O, currency) stop trading in the afternoon; MCX
// trades until 23:30, or 23:55 while the US is on standard time, and its
// files arrive after that. So the day closes in two parts:
//
//	equity     NSE and BSE: closed in the evening, once their files are in
//	commodity  MCX (and any exchange's commodity segment): closed after the
//	           session, usually the next morning
//
// Each part has its own checks, counting only its own trades, breaks,
// bills, contract notes and margin, and carries its own positions forward
// to its own next trading day. Firm-wide checks (trial balance, client
// funds) are in both. A part with nothing to close (no trades, nothing
// carried) is not needed that day. The day counts as closed when every
// needed part is closed; each close is followed by a backup.

// Close parts.
const (
	PartEquity    = "equity"
	PartCommodity = "commodity"
)

// ClosePartInfo describes a part.
type ClosePartInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	About string `json:"about"`
}

// CloseParts are the parts of a day's close, in the order they close.
var CloseParts = []ClosePartInfo{
	{PartEquity, "Equity", "NSE and BSE: cash, F&O and currency"},
	{PartCommodity, "Commodity", "MCX, and commodity segments of other exchanges"},
}

func partName(id string) string {
	for _, p := range CloseParts {
		if p.ID == id {
			return p.Name
		}
	}
	return id
}

// PartOf returns the part an exchange segment closes with.
func PartOf(exchange, segment string) string {
	if segment == "COM" || exchange == "MCX" || exchange == "NCDEX" {
		return PartCommodity
	}
	return PartEquity
}

// partOfKey reads the part from a ledger source ID date/ucc/EXCH-SEG.
func partOfKey(srcID string) string {
	p := strings.Split(srcID, "/")
	if len(p) < 3 {
		return PartEquity
	}
	ex, seg, _ := strings.Cut(p[2], "-")
	return PartOf(ex, seg)
}

// ---- trading days per part ----

// IsTradingDay reports whether date is a trading day for a part. Equity
// follows the business days. Commodity follows MCX's approved holiday list
// for the years it covers (a day with the evening session open counts),
// and the business days otherwise.
func (s Settings) IsTradingDay(part, date string) bool {
	if part != PartCommodity || len(date) < 4 || !s.exMCXYears[date[:4]] {
		return s.IsBusinessDay(date)
	}
	t, err := time.Parse("2006-01-02", date)
	if err != nil || t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return false
	}
	if _, ok := s.exMCX[date]; ok {
		return false
	}
	return !contains(s.Holidays, date)
}

// NextTradingDay and PrevTradingDay step over a part's non-trading days.
func (s Settings) NextTradingDay(part, date string) string { return s.stepTrading(part, date, 1) }
func (s Settings) PrevTradingDay(part, date string) string { return s.stepTrading(part, date, -1) }

func (s Settings) stepTrading(part, date string, by int) string {
	if part != PartCommodity {
		return s.stepDay(date, by, false)
	}
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	for i := 0; i < 400; i++ {
		t = t.AddDate(0, 0, by)
		if d := t.Format("2006-01-02"); s.IsTradingDay(part, d) {
			return d
		}
	}
	return ""
}

// ---- what each part has ----

func (o *Ops) partTrades(ctx context.Context, tenant, date, part string) (ex, oms []ingest.TradeEvent, err error) {
	all, err := o.exchangeTrades(ctx, tenant, date)
	if err != nil {
		return nil, nil, err
	}
	for _, t := range all {
		if PartOf(t.Exchange, t.Segment) == part {
			ex = append(ex, t)
		}
	}
	om, _ := o.App.Trades.List(ctx, tenant, ingest.FromOMS, date)
	for _, t := range om {
		if PartOf(t.Exchange, t.Segment) == part {
			oms = append(oms, t)
		}
	}
	return ex, oms, nil
}

func (o *Ops) partCarried(ctx context.Context, tenant, date, part string) ([]Position, error) {
	cs, err := o.carried(ctx, tenant, date)
	out := cs[:0]
	for _, c := range cs {
		if PartOf(c.Exchange, c.Segment) == part {
			out = append(out, c)
		}
	}
	return out, err
}

// partActive reports whether a part has anything to close on date:
// exchange trades, or positions carried into the day.
func (o *Ops) partActive(ctx context.Context, tenant, date, part string) (bool, error) {
	ex, _, err := o.partTrades(ctx, tenant, date, part)
	if err != nil || len(ex) > 0 {
		return len(ex) > 0, err
	}
	cs, err := o.partCarried(ctx, tenant, date, part)
	return len(cs) > 0, err
}

// neededParts returns the parts to close on date. A day with nothing at
// all still needs its equity part closed, as before parts existed.
func (o *Ops) neededParts(ctx context.Context, tenant, date string) (map[string]bool, error) {
	need := map[string]bool{}
	for _, p := range CloseParts {
		a, err := o.partActive(ctx, tenant, date, p.ID)
		if err != nil {
			return nil, err
		}
		need[p.ID] = a
	}
	if !need[PartEquity] && !need[PartCommodity] {
		need[PartEquity] = true
	}
	return need, nil
}

// ---- the checks ----

func (o *Ops) partChecks(ctx context.Context, tenant, date, part string) ([]EODCheck, error) {
	var cs []EODCheck
	ex, om, err := o.partTrades(ctx, tenant, date, part)
	if err != nil {
		return nil, err
	}
	cs = append(cs, EODCheck{"Exchange and OMS files loaded", len(ex) > 0 && len(om) > 0, fmt.Sprintf("%d exchange trades, %d OMS fills", len(ex), len(om)), "files"})
	allB, _ := o.App.Breaks.List(ctx, tenant, date, "")
	open, all := 0, 0
	for _, b := range allB {
		if PartOf(b.Exchange, b.Segment) != part {
			continue
		}
		all++
		if b.Status == "OPEN" {
			open++
		}
	}
	cs = append(cs, EODCheck{"Every reconciliation break resolved", len(ex) > 0 && open == 0 && (all > 0 || o.reconRan(ctx, tenant, date)), fmt.Sprintf("%d open of %d break(s)", open, all), "recon"})
	es, _ := o.App.Ledger.Store.List(ctx, tenant, ledgerFilterDate(date))
	bills, mtm := 0, 0
	for _, e := range es {
		if partOfKey(e.Source.ID) != part {
			continue
		}
		switch e.Source.Kind {
		case "BILL":
			bills++
		case "MTM":
			mtm++
		}
	}
	cs = append(cs, EODCheck{"Bills posted", bills > 0, fmt.Sprintf("%d bill entries", bills), "billing"})
	cf, _ := o.partCarried(ctx, tenant, date, part)
	if len(cf) > 0 {
		fut := map[string]bool{}
		for _, c := range cf {
			if strings.Contains(c.Instrument, "|FUT|") {
				fut[c.UCC+"|"+c.Exchange+"-"+c.Segment] = true
			}
		}
		cs = append(cs, EODCheck{"Carried futures marked to market", len(fut) == 0 || mtm > 0, fmt.Sprintf("%d MTM entries for %d client-segments with carried futures", mtm, len(fut)), "billing"})
	}
	notes, _ := o.Notes(ctx, tenant, date)
	mine, unsent := 0, 0
	for _, n := range notes {
		if n.part() != part {
			continue
		}
		mine++
		if n.Status != "Sent" {
			unsent++
		}
	}
	cs = append(cs, EODCheck{"Contract notes sent", mine > 0 && unsent == 0, fmt.Sprintf("%d of %d not delivered", unsent, mine), "notes"})
	rv, err := o.Risk(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	// Margin is a client's; count breaches of clients active in this part.
	active := map[string]bool{}
	for _, t := range ex {
		active[t.UCC] = true
	}
	for _, c := range cf {
		active[c.UCC] = true
	}
	breach := 0
	for _, r := range rv.Rows {
		if r.Status == "Breach" && active[r.UCC] {
			breach++
		}
	}
	cs = append(cs, EODCheck{"No unaddressed margin breaches", breach == 0, fmt.Sprintf("%d client(s) trading here in breach", breach), "rms"})
	tb, _ := o.App.Ledger.TrialBalance(ctx, tenant, date)
	cs = append(cs, EODCheck{"Trial balance agrees", tb.Balanced, fmt.Sprintf("₹%s each side", formatINR(tb.TotalDebit.Dec())), "ledger"})
	f, _ := o.Funds(ctx, tenant, date)
	cs = append(cs, EODCheck{"Client funds segregation", f.OK, fmt.Sprintf("cover %.2f×", f.Cover), "ledger"})
	return cs, nil
}

// reconRan reports whether reconciliation was run for date (a part with no
// breaks at all is then clean, not unreconciled).
func (o *Ops) reconRan(ctx context.Context, tenant, date string) bool {
	all, _ := o.App.Breaks.List(ctx, tenant, date, "")
	return len(all) > 0
}

// EODPart is one part of a day's close.
type EODPart struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	About      string     `json:"about"`
	Needed     bool       `json:"needed"` // has trades or carried positions
	Checks     []EODCheck `json:"checks"`
	Closed     bool       `json:"closed"`
	ClosedAt   string     `json:"closed_at,omitempty"`
	ClosedBy   string     `json:"closed_by,omitempty"`
	Exceptions []string   `json:"exceptions,omitempty"`
	NextDay    string     `json:"next_day,omitempty"`
	Rolled     int        `json:"rolled_positions"`
	Previous   string     `json:"previous_open,omitempty"`
}

func (st *EODState) part(id string) *EODPart {
	for i := range st.Parts {
		if st.Parts[i].ID == id {
			return &st.Parts[i]
		}
	}
	return nil
}

// EOD runs the end-of-day checks, part by part.
func (o *Ops) EOD(ctx context.Context, tenant, date string) (EODState, error) {
	st, _, err := docs.GetOr(ctx, o.Docs, tenant, cEOD, date, EODState{Date: date})
	if err != nil {
		return st, err
	}
	legacyClosed := st.Closed && len(st.Parts) == 0 // closed before parts existed
	need, err := o.neededParts(ctx, tenant, date)
	if err != nil {
		return st, err
	}
	saved := map[string]EODPart{}
	for _, p := range st.Parts {
		saved[p.ID] = p
	}
	st.Parts, st.Checks = nil, nil
	nNeeded := 0
	for _, p := range CloseParts {
		if need[p.ID] {
			nNeeded++
		}
	}
	for _, info := range CloseParts {
		p := saved[info.ID]
		p.ID, p.Name, p.About, p.Needed = info.ID, info.Name, info.About, need[info.ID]
		if legacyClosed {
			p.Closed, p.ClosedAt, p.ClosedBy = true, st.ClosedAt, st.ClosedBy
		}
		cs, err := o.partChecks(ctx, tenant, date, info.ID)
		if err != nil {
			return st, err
		}
		p.Checks = cs
		if err := o.partOpenError(ctx, tenant, date, info.ID); err != nil {
			p.Previous = strings.TrimPrefix(err.Error(), ErrState.Error()+": ")
		} else {
			p.Previous = ""
		}
		if p.Needed {
			for _, c := range cs {
				if nNeeded > 1 {
					c.Name = info.Name + ": " + c.Name
				}
				st.Checks = append(st.Checks, c)
			}
		}
		st.Parts = append(st.Parts, p)
	}
	st.Date = date
	st.Closed = true
	for _, p := range st.Parts {
		if p.Needed && !p.Closed {
			st.Closed = false
		}
	}
	if !st.Closed {
		st.ClosedAt, st.ClosedBy = "", ""
	}
	st.Previous = ""
	if err := o.DayOpenError(ctx, tenant, date); err != nil {
		st.Previous = strings.TrimPrefix(err.Error(), ErrState.Error()+": ")
	}
	return st, nil
}

// ClosePart closes one part of the day: its failing checks are recorded as
// exceptions and need a note, its positions carry forward, and a backup
// follows. The day is closed once every needed part is.
func (o *Ops) ClosePart(ctx context.Context, tenant, actor, date, part, note string) (EODState, error) {
	if err := o.require(ctx, tenant, actor, "Closing the day", RoleChecker); err != nil {
		return EODState{}, err
	}
	st, err := o.EOD(ctx, tenant, date)
	if err != nil {
		return st, err
	}
	p := st.part(part)
	if p == nil {
		return st, invalid("unknown part %q: choose equity or commodity", part)
	}
	if p.Closed {
		return st, fmt.Errorf("%w: the %s part of %s is already closed", ErrState, strings.ToLower(p.Name), date)
	}
	if !p.Needed {
		return st, fmt.Errorf("%w: there is nothing to close in %s for %s (no trades and nothing carried)", ErrState, strings.ToLower(p.Name), date)
	}
	var ex []string
	for _, c := range p.Checks {
		if !c.OK {
			ex = append(ex, c.Name+": "+c.Detail)
		}
	}
	if len(ex) > 0 && strings.TrimSpace(note) == "" {
		return st, invalid("%d %s check(s) failed; add a note explaining why it is closed with exceptions", len(ex), strings.ToLower(p.Name))
	}
	if note != "" {
		ex = append(ex, "Note: "+note)
	}
	if err := o.partOpenError(ctx, tenant, date, part); err != nil {
		return st, err
	}
	next, n, err := o.RollPositions(ctx, tenant, actor, date, part)
	if err != nil {
		return st, fmt.Errorf("rolling %s positions forward: %w", strings.ToLower(p.Name), err)
	}
	at := o.now().Format("15:04")
	p.Closed, p.ClosedAt, p.ClosedBy, p.Exceptions, p.NextDay, p.Rolled = true, at, actor, ex, next, n
	st.Closed = true
	for _, x := range st.Parts {
		if x.Needed && !x.Closed {
			st.Closed = false
		}
	}
	st.Rolled += n
	if p.ID == PartEquity || st.NextDay == "" {
		st.NextDay = next
	}
	st.Exceptions = append(st.Exceptions, prefixAll(p.Name+": ", ex)...)
	if st.Closed {
		st.ClosedAt, st.ClosedBy = at, actor
	}
	// Store the close, not the checks (they are worked out when read).
	keep := st
	keep.Checks, keep.Previous = nil, ""
	keep.Parts = append([]EODPart(nil), st.Parts...)
	for i := range keep.Parts {
		keep.Parts[i].Checks, keep.Parts[i].Previous = nil, ""
	}
	if _, err := docs.Update(ctx, o.Docs, tenant, cEOD, date, actor, func(x *EODState) error { *x = keep; return nil }); err != nil {
		if _, err := docs.Put(ctx, o.Docs, tenant, cEOD, date, keep, 0, actor); err != nil {
			return st, err
		}
	}
	o.log(ctx, tenant, actor, "eod.closed", date, map[string]any{"part": part, "exceptions": ex, "day_closed": st.Closed})
	o.backupAfterClose(ctx, tenant, date, part)
	return st, nil
}

func prefixAll(p string, ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = p + s
	}
	return out
}

// CloseDay closes every part of the day still open, equity first; the
// note covers any failing checks in either.
func (o *Ops) CloseDay(ctx context.Context, tenant, actor, date, note string) (EODState, error) {
	if err := o.require(ctx, tenant, actor, "Closing the day", RoleChecker); err != nil {
		return EODState{}, err
	}
	st, err := o.EOD(ctx, tenant, date)
	if err != nil {
		return st, err
	}
	if st.Closed {
		return st, fmt.Errorf("%w: %s is already closed", ErrState, date)
	}
	for _, p := range CloseParts {
		x := st.part(p.ID)
		if x == nil || !x.Needed || x.Closed {
			continue
		}
		if st, err = o.ClosePart(ctx, tenant, actor, date, p.ID, note); err != nil {
			return st, err
		}
	}
	return st, nil
}

// partOpenError explains why a part of date cannot close yet: the same
// part of its previous trading day is still open.
func (o *Ops) partOpenError(ctx context.Context, tenant, date, part string) error {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return err
	}
	prev := set.PrevTradingDay(part, date)
	act, err := o.partActive(ctx, tenant, prev, part)
	if err != nil || !act {
		return err
	}
	st, _, err := docs.GetOr(ctx, o.Docs, tenant, cEOD, prev, EODState{})
	if err != nil {
		return err
	}
	if st.Closed && len(st.Parts) == 0 {
		return nil // closed before parts existed
	}
	if p := st.part(part); p != nil && p.Closed {
		return nil
	}
	return fmt.Errorf("%w: close the %s part of %s first; its open positions carry into %s", ErrState, strings.ToLower(partName(part)), prev, date)
}

// DayOpenError explains why a day cannot be worked yet: a part of the
// previous trading day is still open.
func (o *Ops) DayOpenError(ctx context.Context, tenant, date string) error {
	for _, p := range CloseParts {
		if err := o.partOpenError(ctx, tenant, date, p.ID); err != nil {
			return err
		}
	}
	return nil
}
