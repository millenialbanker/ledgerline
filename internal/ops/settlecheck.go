package ops

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- settlement and allocation checks through the day ----
//
//   - Intraday short allocation (SA01, SA02, SA03; NCL/CMPT/53307 of 16 Aug
//     2022, extended by NCL/CMPT/77292 of 4 Nov 2026): each client's
//     allocated collateral plus re-pledged securities must at all times be
//     at least the client's minimum margin (NCL/CMPT/51657, items I.2-I.3).
//     The reports of each peak margin snapshot are loaded as they come, and
//     each client short raises a work item the same day, not only at the
//     end of the day (SA04).
//   - Early pay-in of funds allocation (CEPF; NCL/CMPT/69520 of 6 Aug
//     2025): what NSE Clearing allocated against what was requested.
//   - The allocation accepted by 22:00 (NCL/CMPT/70442, circular 127/2025:
//     up- and down-streaming of cash and FDRs by 20:00, allocation of
//     collateral to segments and clients by 22:00).
//   - Early pay-in of securities at the end of the trade day (SEBI master
//     circular for stock brokers, paras 45.1-45.3: an early pay-in block
//     lapses at the end of T if unexecuted). CDSL's API (API Handbook
//     §5.9, early pay-in setup) moves the shares to the counter account
//     the DP names (epboid; the CM pool since CDSL/OPS/DP/SETTL/2026/122)
//     and describes no block or lapse, so Ledgerline does the safe part on
//     its side: an early pay-in not sent by the end of its trade day lapses
//     and is never sent later, and one accepted for more than the client's
//     net sale (netted as the clearing corporation nets it) is raised for a
//     person to get the excess back.

const (
	cSettleTick = "settletick" // check|date: when a once-a-day check ran

	allocAcceptBy = "22:00"
)

var reSABatch = regexp.MustCompile(`(?i)_i(\d+)\.`)

// loadIntradaySA keeps an intraday short allocation report, one per
// snapshot (the batch number in the name), and raises the clients short.
func (o *Ops) loadIntradaySA(ctx context.Context, tenant, actor string, s *xfile.Spec, f xfile.File, rows []xfile.Row, r *XFileResult) {
	kind := strings.TrimPrefix(s.ID, "ncl-") // sa01, sa02, sa03
	date := ymdFromName(f.Name)
	if date == "" && len(rows) > 0 {
		date = anyDate(rows[0].Get("Date"))
	}
	if date == "" {
		r.Loaded = "Not loaded: the date is not in the file name"
		return
	}
	seg := "CM"
	switch strings.ToUpper(f.Name[:1]) {
	case "F":
		seg = "FO"
	case "X":
		seg = "CD"
	}
	batch := "00"
	if m := reSABatch.FindStringSubmatch(f.Name); m != nil {
		batch = m[1]
	}
	n, err := o.putCCData(ctx, tenant, actor, kind, date, seg+"-"+batch, f.Name, rows, nil)
	if err != nil {
		r.Loaded = "Not loaded: " + err.Error()
		return
	}
	short := 0
	for _, x := range rows {
		amt := decOr0(x.Get("ShortAllocation"))
		who := strings.ToUpper(firstOf(x.Get("ClientCode"), x.Get("TMCode")))
		if amt.Sign() <= 0 || who == "" {
			continue
		}
		short++
		o.raiseWork(ctx, tenant, "short_allocation", "intraday|"+date+"|"+seg+"|"+who, fmt.Sprintf("Intraday short allocation · %s %s", seg, who),
			fmt.Sprintf("NSE Clearing's %s report (snapshot %s of %s) shows %s's collateral ₹%s short of the minimum margin ₹%s by ₹%s. Allocate more of the client's collateral now (Clearing allocation), or record the reason on Clearing data for the short allocation report; a short allocation is penalised.",
				strings.ToUpper(kind), batch, fmtDay(date), who, formatINR(decOr0(x.Get("TotalCollateral"))), formatINR(decOr0(x.Get("MinimumMarginRequired"))), formatINR(amt)), x.Get("ClientCode"), RoleRisk, "ccdata", date)
	}
	r.Loaded, r.Added = fmt.Sprintf("Loaded as NSE Clearing's intraday short allocation (%s, snapshot %s): %d short, each raised for the risk manager", strings.ToUpper(kind), batch, short), n
}

// loadEPIF keeps NSE Clearing's early pay-in of funds allocation and
// raises each allocation short of what was requested.
func (o *Ops) loadEPIF(ctx context.Context, tenant, actor string, f xfile.File, rows []xfile.Row, r *XFileResult) {
	date := ymdFromName(f.Name)
	for _, x := range rows {
		if strings.TrimSpace(x.Get("RecordType")) == "10" {
			if d := anyDate(x.Get("Date")); d != "" {
				date = d
			}
		}
	}
	if date == "" {
		r.Loaded = "Not loaded: the date is not in the file name"
		return
	}
	var det []xfile.Row
	for _, x := range rows {
		if strings.TrimSpace(x.Get("RecordType")) == "20" {
			det = append(det, x)
		}
	}
	n, err := o.putCCData(ctx, tenant, actor, "epif", date, "CM", f.Name, det, nil)
	if err != nil {
		r.Loaded = "Not loaded: " + err.Error()
		return
	}
	for _, x := range det {
		req, got := decOr0(x.Get("AllocationRequested")), decOr0(x.Get("AmountAllocated"))
		if got.Cmp(req) < 0 {
			u := strings.ToUpper(x.Get("ClientCode"))
			o.raiseWork(ctx, tenant, "epif", date+"|"+u+"|"+x.Get("Symbol"), "Early pay-in of funds not fully allocated · "+u,
				fmt.Sprintf("NSE Clearing allocated ₹%s of the ₹%s early pay-in of funds asked for %s %s (settlement %s). Check the funds reached NSE Clearing through the clearing bank; the sale needs margin for the part not allocated.", formatINR(got), formatINR(req), u, x.Get("Symbol"), x.Get("SettlementNo")), u, RoleRisk, "ccdata", date)
		}
	}
	r.Loaded, r.Added = "Loaded as NSE Clearing's early pay-in of funds allocation (Clearing data)", n
}

// settleSections are the Clearing data sections of this file: intraday
// short allocation and early pay-in of funds.
func (o *Ops) settleSections(ctx context.Context, tenant, date string, cs map[string]Client) []CCSection {
	sa := CCSection{ID: "short_allocation_intraday", Title: "Intraday short allocation (SA01, SA02, SA03)", About: "NSE Clearing's report at each peak margin snapshot: clients (and, for a clearing member, trading members) whose collateral was below the minimum margin at the snapshot. Each short is raised for the risk manager the same day.",
		Columns: []string{"Report", "Segment", "Snapshot", "TM / CP", "Client", "Name", "Minimum margin", "Collateral", "Short"}}
	for _, kind := range []string{"sa01", "sa02", "sa03"} {
		for _, d := range o.ccData(ctx, tenant, kind, date, "") {
			sa.Loaded, sa.File = true, strings.TrimPrefix(sa.File+", "+d.File, ", ")
			seg, batch, _ := strings.Cut(d.Segment, "-")
			for _, x := range d.Rows {
				u := strings.ToUpper(x["ClientCode"])
				short := decOr0(x["ShortAllocation"])
				if short.Sign() > 0 {
					sa.Diffs++
				}
				sa.Rows = append(sa.Rows, []string{strings.ToUpper(kind), seg, batch, x["TMCode"], u, cs[u].Name, decOr0(x["MinimumMarginRequired"]).Fixed(2), decOr0(x["TotalCollateral"]).Fixed(2), short.Fixed(2)})
			}
		}
	}
	ep := CCSection{ID: "epif", Title: "Early pay-in of funds allocated (CEPF)", About: "Early pay-in of funds NSE Clearing allocated to each client and security against the allocation requested (NCL/CMPT/69520).",
		Columns: []string{"Settlement", "Client", "Name", "Security", "Requested", "Allocated", "Short"}}
	for _, d := range o.ccData(ctx, tenant, "epif", date, "") {
		ep.Loaded, ep.File = true, d.File
		for _, x := range d.Rows {
			u := strings.ToUpper(x["ClientCode"])
			req, got := decOr0(x["AllocationRequested"]), decOr0(x["AmountAllocated"])
			if got.Cmp(req) < 0 {
				ep.Diffs++
			}
			ep.Rows = append(ep.Rows, []string{x["SettlementType"] + " " + x["SettlementNo"], u, cs[u].Name, x["Symbol"] + " " + x["Series"], req.Fixed(2), got.Fixed(2), money.Max(req.Sub(got), money.Dec{}).Fixed(2)})
		}
	}
	return []CCSection{sa, ep}
}

// ---- the allocation accepted by 22:00 ----

// allocAcceptTick checks, once after 22:00 on a business day, that NSE
// Clearing accepted the day's client allocation, and raises a work item
// for the risk manager when it did not.
func (o *Ops) allocAcceptTick(ctx context.Context, tenant string, now time.Time) {
	now = now.In(ist)
	today := now.Format("2006-01-02")
	if now.Format("15:04") < allocAcceptBy {
		return
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil || !set.IsBusinessDay(today) {
		return
	}
	ncl := false
	for _, c := range set.activeSegments() {
		if set.ClearedThrough(c.Key) == "NCL" {
			ncl = true
		}
	}
	if !ncl {
		return
	}
	id := "allocaccept|" + today
	if _, _, err := docs.Get[struct{ At string }](ctx, o.Docs, tenant, cSettleTick, id); err == nil {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cSettleTick, id, struct{ At string }{now.Format(time.RFC3339)}, SystemActor)
	if why := o.AllocationNotAccepted(ctx, tenant, today); len(why) > 0 {
		o.raiseWork(ctx, tenant, "allocation", "accept|"+today, "Client allocation not accepted by NSE Clearing by 22:00",
			fmt.Sprintf("NSE Clearing asks for the allocation of collateral to segments and clients by 22:00 (NCL/CMPT/70442): %s. A client left short is short allocation and is penalised. Act on Clearing allocation and, if NSE Clearing's system is at fault, email collaterals_ops@nsccl.co.in.", strings.Join(why, "; ")),
			"", RoleRisk, "allocation", today)
	}
}

// AllocationNotAccepted lists why the day's client allocation is not (or
// not known to be) accepted by NSE Clearing; empty when it is.
func (o *Ops) AllocationNotAccepted(ctx context.Context, tenant, date string) []string {
	set, _ := o.Settings(ctx, tenant)
	var why []string
	if !set.AllocAPILive {
		// Without the API the allocation goes by file upload: the day's file
		// must at least have been made (written) for the date.
		recv, _ := docs.List[DailyFileRecv](ctx, o.Docs, tenant, cDailyRecv, date+"|ncl-allocation|")
		if len(recv) == 0 {
			why = append(why, "no allocation file was made in Ledgerline for "+fmtDay(date)+", so its upload and acceptance cannot be confirmed; record the upload on Exchange files")
		}
		return why
	}
	ms, _ := docs.List[AllocMessage](ctx, o.Docs, tenant, cAllocMsg, "")
	eod, open, notOK := false, 0, 0
	for _, m := range ms {
		if m.Date != date {
			continue
		}
		if m.Kind == AllocEOD && m.Status != "Rejected" && m.Status != "Superseded" {
			eod = true
		}
		switch m.Status {
		case "Sent", "Ready", "Awaiting approval", "Failed":
			open++
		case "Done":
			for _, r := range m.Records {
				if r.Code != "0100" {
					notOK++
				}
			}
		}
	}
	if open > 0 {
		why = append(why, fmt.Sprintf("%d request(s) not answered or not sent", open))
	}
	if notOK > 0 {
		why = append(why, fmt.Sprintf("%d client record(s) not accepted", notOK))
	}
	if !eod {
		// No end-of-day batch: fine only if every client's level already
		// matches Ledgerline's allocation.
		if al, err := o.Allocation(ctx, tenant, date); err == nil {
			levels := o.allocLevels(ctx, tenant)
			diff := 0
			for _, a := range al {
				if a.Segment == "COM" {
					continue
				}
				if l := levels[allocKey(a.Segment, a.UCC)]; l.Amount.Cmp(a.Cash) != 0 {
					diff++
				}
			}
			if diff > 0 {
				why = append(why, fmt.Sprintf("no end-of-day batch was sent and %d client allocation(s) differ from Ledgerline's", diff))
			}
		}
	}
	return why
}

// ---- early pay-in of securities at the end of the trade day ----

// epiEndOfDay runs once after 18:00 on a business day, for the day's early
// pay-ins: those still waiting to be sent lapse (never sent for a later
// day), and an accepted early pay-in larger than the client's net sale is
// raised so the excess comes back to the client.
func (o *Ops) epiEndOfDay(ctx context.Context, tenant string, now time.Time) {
	now = now.In(ist)
	today := now.Format("2006-01-02")
	if now.Format("15:04") < "18:00" {
		return
	}
	id := "epieod|" + today
	if _, _, err := docs.Get[struct{ At string }](ctx, o.Docs, tenant, cSettleTick, id); err == nil {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cSettleTick, id, struct{ At string }{now.Format(time.RFC3339)}, SystemActor)
	o.EPIEndOfDay(ctx, tenant, SystemActor, today)
}

// EPIEndOfDay applies the end-of-T rules to early pay-ins of trade dates
// up to date; it returns how many lapsed and how many were raised as
// excess.
func (o *Ops) EPIEndOfDay(ctx context.Context, tenant, actor, date string) (lapsed, excess int) {
	for _, r := range o.epiAll(ctx, tenant, "") {
		if r.Status == "Pending" && r.Date <= date {
			if _, err := docs.Update(ctx, o.Docs, tenant, cEPI, r.ID, actor, func(x *EPIRecord) error {
				x.Status, x.Reason = "Lapsed", "not sent by the end of the trade day; an early pay-in is good for its trade day only"
				return nil
			}); err == nil {
				lapsed++
				o.log(ctx, tenant, actor, "cdsl.epi.lapsed", r.ID, map[string]any{"date": r.Date})
			}
		}
	}
	acc := o.EPIAccepted(ctx, tenant, date)
	if len(acc) == 0 {
		return lapsed, 0
	}
	st, err := o.Obligations(ctx, tenant, date)
	if err != nil {
		return lapsed, 0
	}
	sold := map[string]int64{}
	for _, ob := range st.Obligations {
		if ob.NetQty < 0 {
			sold[ob.UCC+"|"+ob.ISIN] += -ob.NetQty
		}
	}
	paid := map[string]int64{}
	for _, r := range acc {
		if r.ISIN != "" {
			paid[r.UCC+"|"+r.ISIN] += r.Qty
		}
	}
	for _, k := range sortedKeys(paid) {
		if over := paid[k] - sold[k]; over > 0 {
			u, isin, _ := strings.Cut(k, "|")
			excess++
			o.raiseWork(ctx, tenant, "cdslepi", "excess|"+date+"|"+k, "Early pay-in more than the sale · "+u,
				fmt.Sprintf("%s paid in %d of %s early on %s but the net sale (across the exchanges cleared together) is %d: %d too many. NSE Clearing returns excess pay-in to the pool: move the %d back to the client's demat account and check why the sale was smaller (cancelled or modified trade).",
					u, paid[k], isin, fmtDay(date), sold[k], over, over), u, RoleChecker, "cdslepi", date)
		}
	}
	return lapsed, excess
}
