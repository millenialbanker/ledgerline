package ops

import (
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- self-inspection tests of the RMS limits beyond margin ----
//
// They read RiskLimits (rmslimits.go) for the date: position limits and
// index limits kept, no ban-period trade that increased open interest,
// surveillance measures loaded and applied, and the pay-in cut-offs met.

func init() {
	siTests = append(siTests,
		SITest{ID: "position_limits", Title: "Derivative positions within client, own-account and member position limits", Area: "Risk", Severity: "high", Owner: RoleRisk,
			Source:    "NSE Clearing NCL/CMPT/73997 (consolidated circular, F&O), items 11.1-11.3.2; NSE/COM/73930 items 1.10-1.11; SEBI/HO/MRD/MRD-PoD-1/P/CIR/2023/136 paras 3.4.1-3.4.2 (commodities: own positions at client-level limits, member level longs and shorts apart, no split across codes)",
			ToConfirm: "The shares of the MWPL (client 10%, own account 20%, member 30%) are as the firm's gap report reads the circular: check them against the clearing corporation's current circular. Ledgerline counts an open position as long plus short FutEq, the stricter reading.",
			Checks:    "The date's end-of-day derivative positions of each client (by PAN), the firm's own codes, the member and each group of connected clients against the limit file (or NSE Clearing's MWPL); NSE Clearing's member open interest against its limit. Skipped when there are derivative positions and no limit is known for any of them.",
			Fix:       "Stop fresh positions in the underlying on the trading front end and reduce the position; load the day's POSITION-LIMITS file on Exchange files so every underlying has a limit.",
			Screen:    "rms", ScreenName: "RMS & margin → RMS & limits (Position limits)", Real: true, run: siPositionLimits},
		SITest{ID: "index_limits", Title: "Index futures and index options within the entity limits, or backed and reported", Area: "Risk", Severity: "high", Owner: RoleRisk,
			Source:    "SEBI/HO/MRD/TPD-1/P/CIR/2025/79, paras 5.5.1, 5.5.2 and 5.5.4 (index futures: higher of 15% of open interest and ₹500 crore; index options per PAN: ₹1,500 crore net, ₹10,000 crore gross each side; excess backed by holdings and reported by the clearing member)",
			ToConfirm: "Ledgerline measures the index options net on options alone, per index; whether futures add to it is to be confirmed against the circular's Annexure-1. The clearing corporation's format for the backing report is not published to Ledgerline.",
			Checks:    "Each client's and the own account's gross index futures, and each PAN's index options FutEq, net and on each side, at the end of the date. A net beyond the limit passes only when the holdings on record back it and the cover report work item is done.",
			Fix:       "Have the position reduced; for a backed net, download the index options cover report on RMS → Position limits and submit it to the clearing corporation, then close the work item.",
			Screen:    "rms", ScreenName: "RMS & margin → RMS & limits (Position limits)", Real: true, run: siIndexLimits},
		SITest{ID: "fo_ban_period", Title: "No trade in the F&O ban period that increased open interest", Area: "Risk", Severity: "high", Owner: RoleRisk,
			Source: "SEBI/HO/MRD/TPD-1/P/CIR/2025/79 (in the ban period, orders only if they reduce the entity's FutEq open interest by the end of the day, from 1 Oct 2025)",
			Checks: "For each security in the ban period on the date (the FO-BAN file, or NSE Clearing's MWPL at the ban level the day before): each client who traded its derivatives, net FutEq at the start and the end of the day at the day's deltas.",
			Fix:    "Square off the increase, find how the order passed the front end, and record the action on the work item.",
			Screen: "rms", ScreenName: "RMS & margin → RMS & limits (Position limits)", Real: true, run: siFOBan},
		SITest{ID: "surveillance_measures", Title: "Exchange surveillance measures loaded and applied in RMS", Area: "Risk", Severity: "medium", Owner: RoleRisk,
			Source:    "NSE/SURV/74008 (circular 313/2026): GSM, ESM, LT-ASM, ST-ASM, IBC and ICA stage margins and trade-for-trade applied in RMS",
			ToConfirm: "Additional surveillance deposits and weekly trading limits are applied on the trading front end; Ledgerline does not check them.",
			Checks:    "A surveillance list loaded in the last 7 days when clients traded the cash market on the date; no intraday trade in a trade-for-trade security on the date.",
			Fix:       "Load the exchanges' current lists as SURV-MEASURES on Exchange files and give the surveillance margins file to the trading front end; block intraday trading in trade-for-trade securities.",
			Screen:    "rms", ScreenName: "RMS & margin → RMS & limits (Position limits)", Real: true, run: siSurvMeasures},
		SITest{ID: "payin_cutoffs", Title: "Pay-in reminders handled by the cut-off", Area: "Settlement", Severity: "medium", Owner: RoleChecker,
			Source:    "NSE Clearing NCL/CMPT/73996 (consolidated circular, cash market), items 3.2 and 5.1-5.3 (funds by 11:00 and securities by 10:30 on T+1; auction day+1 by 08:00)",
			ToConfirm: "The cut-off times are the firm's settings; the work item's closing time stands for when the pay-in was made.",
			Checks:    "The pay-in reminders of the last 30 days: each closed, and not after its due time.",
			Fix:       "Make the pay-in and close the work item with the reference; for a late one record the reason and any penalty.",
			Screen:    "settlement", ScreenName: "Operations → Settlement", Real: true, run: siPayInCutoffs},
	)
}

// workItemsOf lists a process's work items.
func (o *Ops) workItemsOf(e *siEnv, process string) ([]WorkItem, error) {
	all, err := docs.List[WorkItem](e.ctx, o.Docs, e.tenant, cWorkItem, "")
	if err != nil {
		return nil, err
	}
	var out []WorkItem
	for _, w := range all {
		if w.Process == process {
			out = append(out, w)
		}
	}
	return out, nil
}

// workDone says whether a process's work item was closed as done.
func (o *Ops) workDone(e *siEnv, process, key string) bool {
	ws, _ := o.workItemsOf(e, process)
	for _, w := range ws {
		if w.Key == process+":"+key && w.Status == "Done" {
			return true
		}
	}
	return false
}

func siPositionLimits(e *siEnv) (siOut, error) {
	v, err := e.o.RiskLimits(e.ctx, e.tenant, e.date)
	if err != nil {
		return siOut{}, err
	}
	if v.Config.PositionLimitsOff {
		return siSkip("Position limit checks are switched off (RMS → Position limits → Settings).")
	}
	if len(v.Limits) == 0 && len(v.MemberOI) == 0 && len(v.Missing) > 0 {
		return siSkip("Waiting for data: derivative positions but no position limit file or MWPL for the date (" + strings.Join(v.Missing[:min(3, len(v.Missing))], ", ") + ").")
	}
	var s siOut
	s.input("Position limits", e.date)
	for _, r := range v.Limits {
		s.checked++
		if r.Status == "Breach" {
			who := r.Entity
			if r.Level == "Client" && len(r.UCCs) > 0 {
				who = siClient(r.UCCs[0])
			}
			s.bad(fmt.Sprintf("%s %s · %s %s · open %s against %s (%.0f%%)", r.Level, who, r.Symbol, r.Market, r.Open.Fixed(0), r.Limit.Fixed(0), r.Pct))
		}
	}
	for _, r := range v.MemberOI {
		s.checked++
		if r.Status == "Breach" {
			s.bad(fmt.Sprintf("NSE Clearing member open interest · %s %s · %.0f%% of its limit", r.Symbol, r.Type, r.Pct))
		}
	}
	s.summary = fmt.Sprintf("%d positions against their limits; %d over.", s.checked, s.found)
	if len(v.Missing) > 0 {
		s.summary += fmt.Sprintf(" No limit known for %d underlyings.", len(v.Missing))
	}
	return s, nil
}

func siIndexLimits(e *siEnv) (siOut, error) {
	v, err := e.o.RiskLimits(e.ctx, e.tenant, e.date)
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Index limits", e.date)
	reported := e.o.workDone(e, wIndexReport, e.date+"|report")
	for _, r := range v.Index {
		s.checked++
		who := r.Entity
		if len(r.UCCs) > 0 && r.Level == "Client" {
			who = siClient(r.UCCs[0])
		}
		switch {
		case r.Status == "Breach":
			s.bad(fmt.Sprintf("%s · %s %s · ₹%s against ₹%s", r.Kind, who, r.Index, formatINR(r.Value), formatINR(r.Limit)))
		case r.Status == "Justified" && !reported:
			s.bad(fmt.Sprintf("%s · %s %s · backed but the cover report to the clearing corporation is not done", r.Kind, who, r.Index))
		}
	}
	s.summary = fmt.Sprintf("%d index limits checked; %d not kept.", s.checked, s.found)
	return s, nil
}

func siFOBan(e *siEnv) (siOut, error) {
	v, err := e.o.RiskLimits(e.ctx, e.tenant, e.date)
	if err != nil {
		return siOut{}, err
	}
	if len(v.Banned) == 0 {
		return siSkip("No security in the F&O ban period on the date (no FO-BAN file, or none at the ban level).")
	}
	var s siOut
	s.input("F&O ban list", e.date)
	for _, b := range v.Ban {
		s.checked++
		if b.Increased {
			s.bad(fmt.Sprintf("%s · %s · net FutEq %s to %s", siClient(b.UCC), b.Symbol, b.Start.Fixed(0), b.End.Fixed(0)))
		}
	}
	s.summary = fmt.Sprintf("%d securities in the ban period; %d clients traded them; %d increased open interest.", len(v.Banned), s.checked, s.found)
	return s, nil
}

func siSurvMeasures(e *siEnv) (siOut, error) {
	asOf, items := e.o.SurvMeasures(e.ctx, e.tenant)
	ts, err := e.o.exchangeTrades(e.ctx, e.tenant, e.date)
	if err != nil {
		return siOut{}, err
	}
	cm := 0
	for _, t := range ts {
		if t.Segment == "CM" {
			cm++
		}
	}
	if cm == 0 {
		return siSkip("No cash market trade on the date.")
	}
	var s siOut
	s.input("Surveillance list", asOf)
	s.checked++
	switch {
	case asOf == "":
		s.bad("No surveillance list (SURV-MEASURES) has been loaded")
	case daysBetween(asOf, e.date) > 7:
		s.bad(fmt.Sprintf("The surveillance list was last loaded on %s, %d days before", fmtDay(asOf), daysBetween(asOf, e.date)))
	}
	if asOf != "" {
		v, err := e.o.RiskLimits(e.ctx, e.tenant, e.date)
		if err != nil {
			return s, err
		}
		for _, h := range v.Surveillance {
			s.checked++
			s.bad(fmt.Sprintf("%s · %s %s (%s) · %s", siClient(h.UCC), h.Exchange, h.Symbol, h.Measure, h.Why))
		}
	}
	s.summary = fmt.Sprintf("%d securities on the surveillance list; %d findings.", len(items), s.found)
	return s, nil
}

func siPayInCutoffs(e *siEnv) (siOut, error) {
	ws, err := e.o.workItemsOf(e, wPayInCutoff)
	if err != nil {
		return siOut{}, err
	}
	from := addDays(e.date, -siWindow)
	var s siOut
	s.input("Pay-in reminders", e.date)
	for _, w := range ws {
		if w.Date < from || w.Date > e.date || (w.Date == e.date && w.Status == "Open") {
			continue
		}
		s.checked++
		switch {
		case w.Status == "Open":
			s.bad(fmt.Sprintf("%s · still open (due %s)", w.Title, w.Due))
		case w.Status == "Done" && len(w.Closed) >= 16 && closedIST(w.Closed) > w.Due:
			s.bad(fmt.Sprintf("%s · closed %s, due %s", w.Title, closedIST(w.Closed), w.Due))
		}
	}
	if s.checked == 0 {
		return siSkip("No pay-in reminder in the last 30 days.")
	}
	s.summary = fmt.Sprintf("%d pay-in reminders; %d open or late.", s.checked, s.found)
	return s, nil
}

// closedIST is a work item's closing time as YYYY-MM-DD HH:MM in IST.
func closedIST(at string) string {
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		return t.In(ist).Format("2006-01-02 15:04")
	}
	if len(at) >= 16 {
		return strings.Replace(at[:16], "T", " ", 1)
	}
	return at
}
