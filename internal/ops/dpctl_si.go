package ops

import (
	"fmt"
	"strings"

	"ledgerline/internal/docs"
)

// ---- self-inspection tests of the DP controls (dpdis.go, dpreqtat.go,
// dpctl.go) and of clients' trading freeze requests (tradefreeze.go) ----

const (
	dpMC   = "SEBI Master Circular for Depositories SEBI/HO/MRD/MRD-PoD-1/P/CIR/2024/168"
	dpOI   = "CDSL Operating Instructions for DPs"
	siDPWD = 30 // days of slips and requests looked at
)

func init() {
	siTests = append(siTests,
		SITest{ID: "dis_books", Title: "DIS books issued only against the requisition slip, loose slips within 10 a year, dormant accounts' books authorised", Area: "Depository", Severity: "medium", Owner: RoleMaker,
			Source:    dpMC + " 1.12.3-1.12.5; " + dpOI + " 6.5.1-6.5.2; appendix rows DP-0153 (a book at opening or a written waiver)",
			ToConfirm: "Dormancy is judged from the demat transactions in Ledgerline: until the DP controls say from when the history is complete, an account with none is not judged.",
			Checks:    "Books issued in the last 12 months: a second or later book with neither the previous book's requisition slip nor a loss report; more than 10 loose slips for an account in the financial year; a book for a dormant account without the compliance officer's authorisation. Accounts opened in the last 12 months (a week ago or more) with neither a book nor the holder's written waiver.",
			Fix:       "Find the requisition slip or loss report and record it (or cancel the book's slips); have the compliance officer authorise the book; issue a book or record the waiver on Depository → Demat accounts & DIS.",
			Screen:    "dp", ScreenName: "Depository → Demat accounts & DIS", Real: true, run: siDISBooks},
		SITest{ID: "dis_checks", Title: "Slips needing a call to the holder, or above ₹5 lakh, went through their checks", Area: "Depository", Severity: "high", Owner: RoleChecker,
			Source:    dpMC + " 1.12.8; " + dpOI + " 6.5.4.5-6.5.4.9; appendix rows DP-0125/DP-0206 (cut-offs)",
			ToConfirm: "Ledgerline reads the ₹5 lakh rule as two people besides the maker, at least one of them a senior official.",
			Checks:    "Slips received in the last 30 days and verified or later: those needing a call (no transaction for 12 months; not operated for 6 months with all balances out; 5 or more ISINs in full) without a recorded call approved by the compliance officer; those above ₹5 lakh sent or executed without a countersignature. Slips taken after the cut-off are counted in the summary.",
			Fix:       "Stop the slip if it has not executed; record the call and its approval; countersign; review how it went through.",
			Screen:    "dp", ScreenName: "Depository → Demat accounts & DIS", Real: true, run: siDISChecks},
		SITest{ID: "dp_request_tat", Title: "Closures, demat, transmission and freeze requests within their timelines", Area: "Depository", Severity: "high", Owner: RoleChecker,
			Source:    dpOI + " 7.5.11, 10.5.3.6, 10.5.3.20-10.5.3.24, 10.7.3; CDSL DP2025-451 (closure-cum-shifting); " + dpMC + " Annexure 21 s4(1) (investor charter); CDSL DP2025-267 (freeze intimations); appendix DP-0194 (demat ageing)",
			ToConfirm: "When a freeze or unfreeze intimation must go is not in the text Ledgerline has: the next working day.",
			Checks:    "Requests of the last 90 days: closures within 2 working days (or the dues told within 2, paid within 30, closed or rejected within 2 more), the closure confirmed with the final statement, a rejection's reason told; demat and transmission within 7 days of the documents; the RTA followed up after 21 days; rejected documents back in 30 and returned in 7; freeze and unfreeze intimations sent.",
			Fix:       "Do what is late and record it on the request (Depository → Demat accounts & DIS, DP controls); find out why it was late.",
			Screen:    "dp", ScreenName: "Depository → Demat accounts & DIS", Real: true, run: siDPRequestTAT},
		SITest{ID: "dp_recon_daily", Title: "The DP's daily reconciliation signed off", Area: "Depository", Severity: "medium", Owner: RoleChecker,
			Source:    "SEBI (Depositories and Participants) Regulations, 2018, Reg 64 (records reconciled with the depository every day); appendix rows DP-0163, DP-0186, DP-0219, DP-0255, DP-0263, DP-0240",
			ToConfirm: "No format is prescribed: Ledgerline's lines are openings, modifications, slips, closures, freezes and pledges, received against executed and pending.",
			Checks:    "Each business day of the last two weeks before the date: the day's reconciliation signed off, and any difference from CDSL's figures explained.",
			Fix:       "Open the DP controls on Depository → Demat accounts & DIS, check the day against CDAS and sign it off.",
			Screen:    "dp", ScreenName: "Depository → Demat accounts & DIS", Real: true, run: siDPRecon},
		SITest{ID: "dis_stock", Title: "Blank DIS stock kept and counted", Area: "Depository", Severity: "low", Owner: RoleMaker,
			Source:    "Appendix row DP-0202 (an inventory of blank slips, reconciled periodically); " + dpOI + " 6.5.1 (pre-printed serial numbers, serial register)",
			ToConfirm: "Periodically is the firm's interval (30 days unless changed in the DP controls).",
			Checks:    "When the DP issues DIS books: a stock kept in Ledgerline for each series in use, and counted within the firm's interval.",
			Fix:       "Record the printer's deliveries and count the blank slips on the DP controls.",
			Screen:    "dp", ScreenName: "Depository → Demat accounts & DIS", Real: true, run: siDISStock},
		SITest{ID: "pool_payout", Title: "Pay-outs moved from the pool to clients within one working day", Area: "Depository", Severity: "high", Owner: RoleMaker,
			Source:    dpOI + " 6.6.5 and 12.2 (13-15) (0.06% a week on delays); CDSL Master Circular for DPs (CDSL/OPS/DP/POLCY/2026/257) 4.10.2.1; CDSL DP2025-829 (trading holidays are not working days)",
			ToConfirm: "A part week is counted as a week in the estimate of CDSL's charge.",
			Checks:    "Delivery obligations of the last 10 days (purchases not held for unpaid amounts): moved to the client's account by the working day after the pay-out.",
			Fix:       "Pay out the securities on Settlement; check CDSL's bill for the delay charge.",
			Screen:    "settlement", ScreenName: "Operations → Settlement", Real: true, run: siPoolPayout},
		SITest{ID: "trade_freeze", Title: "Clients' trading freeze requests frozen within 15 minutes, orders cancelled, positions sent", Area: "Risk", Severity: "high", Owner: RoleRisk,
			Source:    "NSE/INSP/73976 (Master Circular, Inspection Department), Exhibit 33 (framework for voluntary freezing of online access)",
			ToConfirm: "Trading hours are taken as 09:00 to 15:30 (23:55 with a commodity segment open).",
			Checks:    "The firm records at least two ways to ask and where the policy is published. Requests of the last 90 days: frozen within 15 minutes in trading hours (before the next session otherwise), acknowledged, Finspot's cancellation of pending orders confirmed, open positions sent within an hour, and trading re-enabled only with due diligence recorded.",
			Fix:       "Record the modes and the policy on the DP controls' settings; finish the open steps on RMS & limits → Trading freeze requests; review the late ones.",
			Screen:    "rms", ScreenName: "Risk → RMS & limits", Real: true, run: siTradeFreeze},
	)
}

func siDISBooks(e *siEnv) (siOut, error) {
	var s siOut
	s.input("DIS books", e.date)
	books, err := docs.List[DISBook](e.ctx, e.o.Docs, e.tenant, cDISBook, "")
	if err != nil {
		return s, err
	}
	from := addMonths(e.date, -12)
	type acct struct{ ucc, bo string }
	seen := map[acct]bool{}
	loose := map[acct]int64{}
	for _, b := range books { // oldest first by ID
		k := acct{b.UCC, b.BOID}
		if b.Issued > e.date {
			continue
		}
		if b.Loose {
			if b.Issued >= fyStart(e.date) {
				loose[k] += b.To - b.From + 1
			}
			continue
		}
		first := !seen[k]
		seen[k] = true
		if b.Issued < from {
			continue
		}
		s.checked++
		switch {
		case !first && b.Requisition == 0 && b.LossRef == "":
			s.bad(fmt.Sprintf("Book %s · %s · issued %s without the previous book's requisition slip or a loss report", b.ID, siClient(b.UCC), fmtDay(b.Issued)))
		case b.AwaitingCO != "" && b.AuthorisedBy == "":
			s.bad(fmt.Sprintf("Book %s · %s · dormant account (%s), not authorised by the compliance officer", b.ID, siClient(b.UCC), b.AwaitingCO))
		}
	}
	for k, n := range loose {
		if n > disLooseLimit {
			s.bad(fmt.Sprintf("%s · %d loose slips this financial year (at most %d)", siClient(k.ucc), n, disLooseLimit))
		}
	}
	// A book at opening, or the holder's written waiver (DP-0153).
	bos, _ := docs.List[BOAccount](e.ctx, e.o.Docs, e.tenant, cBO, "")
	for _, b := range bos {
		if b.MovedTo != "" || b.Status == "Closed" || b.Opened == "" || b.Opened < from || addDays(b.Opened, 7) > e.date {
			continue
		}
		s.checked++
		has := false
		for k := range seen {
			if k.ucc == b.UCC && (k.bo == "" || k.bo == normDematID(b.BOID)) {
				has = true
			}
		}
		if !has && b.DISWaiver == "" {
			s.bad(fmt.Sprintf("%s · account opened %s with no DIS book and no written waiver", siClient(b.UCC), fmtDay(b.Opened)))
		}
	}
	s.summary = fmt.Sprintf("%d books and new accounts checked; %d found.", s.checked, s.found)
	return s, nil
}

func siDISChecks(e *siEnv) (siOut, error) {
	var s siOut
	from := addDays(e.date, -siDPWD)
	s.input("Delivery instructions", from)
	ds, err := docs.List[DIS](e.ctx, e.o.Docs, e.tenant, cDIS, "")
	if err != nil {
		return s, err
	}
	late := 0
	for _, d := range ds {
		if d.Received < from || d.Received > e.date || d.Status == "Entered" || d.Status == "Rejected" {
			continue
		}
		s.checked++
		if d.BestEffort != "" {
			late++
		}
		if len(d.CallNeeded) > 0 && (d.Call == nil || d.Call.Approved == "") {
			s.bad(fmt.Sprintf("%s · %s · %s without an approved call to the holder (%s)", d.ID, siClient(d.UCC), strings.ToLower(d.Status), strings.Join(d.CallNeeded, "; ")))
			continue
		}
		if d.Value.Cmp(HighValueDIS) > 0 && d.Status != "Verified" && d.Countersigned == "" {
			s.bad(fmt.Sprintf("%s · %s · above ₹5 lakh, %s without a countersignature", d.ID, siClient(d.UCC), strings.ToLower(d.Status)))
		}
	}
	s.summary = fmt.Sprintf("%d slips of the last %d days checked; %d without their checks; %d taken after the cut-off on a best-effort basis.", s.checked, siDPWD, s.found, late)
	return s, nil
}

func siDPRequestTAT(e *siEnv) (siOut, error) {
	var s siOut
	from := addDays(e.date, -90)
	s.input("DP requests", from)
	rs, err := docs.List[DPRequest](e.ctx, e.o.Docs, e.tenant, cDPReq, "")
	if err != nil {
		return s, err
	}
	for _, r := range rs {
		if r.day() > e.date || (r.DoneOn != "" && r.DoneOn < from) || (r.DoneOn == "" && r.day() < from && r.Status != "Pending" && r.Status != "Sent to RTA") {
			continue
		}
		s.checked++
		for _, d := range dpDuties(e.set, r, e.date) {
			if d.Late {
				s.bad(fmt.Sprintf("%s · %s · %s · %s, due %s%s", r.ID, r.Kind, siClient(r.UCC), d.What, fmtDay(d.Due), map[bool]string{true: ", done " + fmtDay(d.Done), false: ", not done"}[d.Done != ""]))
				break
			}
		}
	}
	s.summary = fmt.Sprintf("%d requests of the last 90 days checked; %d with a duty late.", s.checked, s.found)
	return s, nil
}

func siDPRecon(e *siEnv) (siOut, error) {
	var s siOut
	if e.o.firmFacts(e.ctx, e.tenant).CDSL == triNo {
		return siSkip("the firm is not a CDSL participant")
	}
	from := addDays(e.date, -14)
	s.input("DP reconciliation sign-offs", from)
	for d := from; d < e.date; d = addDays(d, 1) {
		if !e.set.IsBusinessDay(d) {
			continue
		}
		s.checked++
		r := e.o.dpRecon(e.ctx, e.tenant, d)
		switch {
		case r.Signed == "":
			s.bad(fmt.Sprintf("%s · not signed off", fmtDay(d)))
		case !r.Balanced && r.Note == "":
			s.bad(fmt.Sprintf("%s · differs from CDSL's figures with no explanation", fmtDay(d)))
		}
	}
	s.summary = fmt.Sprintf("%d business days checked; %d not signed off.", s.checked, s.found)
	return s, nil
}

func siDISStock(e *siEnv) (siOut, error) {
	var s siOut
	books, _ := docs.List[DISBook](e.ctx, e.o.Docs, e.tenant, cDISBook, "")
	series := map[string]bool{}
	for _, b := range books {
		if b.Issued >= addMonths(e.date, -12) {
			series[firstOf(b.Series, "Regular")] = true
		}
	}
	if len(series) == 0 {
		return siSkip("no DIS book issued in the last 12 months")
	}
	s.input("Blank DIS stock", e.date)
	cfg := e.o.DPControlsConfig(e.ctx, e.tenant)
	for _, k := range sortedKeys(series) {
		s.checked++
		st, _, err := docs.Get[disStock](e.ctx, e.o.Docs, e.tenant, cDPCtl, "stock|"+k)
		switch {
		case err != nil:
			s.bad(fmt.Sprintf("%s slips · no blank stock kept in Ledgerline", k))
		case addDays(firstOf(st.Counted, st.Since), cfg.stockDays()) < e.date:
			s.bad(fmt.Sprintf("%s slips · last counted %s (every %d days)", k, fmtDay(firstOf(st.Counted, st.Since)), cfg.stockDays()))
		}
	}
	s.summary = fmt.Sprintf("%d series checked; %d not kept or not counted.", s.checked, s.found)
	return s, nil
}

func siPoolPayout(e *siEnv) (siOut, error) {
	var s siOut
	from := addDays(e.date, -10)
	s.input("Settlement obligations", from)
	for _, p := range e.o.poolPayouts(e.ctx, e.tenant, from, e.date) {
		if p.Due > e.date && p.Paid == "" {
			continue
		}
		s.checked++
		if p.Late > 0 {
			s.bad(fmt.Sprintf("%s · %s · pay-out %s, due %s, %s · CDSL charge about ₹%s", siClient(p.UCC), p.Symbol, fmtDay(p.PayOut), fmtDay(p.Due), map[bool]string{true: "moved " + fmtDay(p.Paid), false: "not moved"}[p.Paid != ""], p.Charge.Fixed(2)))
		}
	}
	s.summary = fmt.Sprintf("%d pay-outs of the last 10 days checked; %d late.", s.checked, s.found)
	return s, nil
}

func siTradeFreeze(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Trading freeze requests", addDays(e.date, -90))
	cfg := e.o.DPControlsConfig(e.ctx, e.tenant)
	s.checked++
	if len(cfg.FreezeModes) < freezeMinModes || cfg.FreezePolicy == "" {
		s.bad(fmt.Sprintf("%d way%s to ask for a freeze recorded (at least %d), policy %s", len(cfg.FreezeModes), plural(len(cfg.FreezeModes)), freezeMinModes, map[bool]string{true: "published at " + cfg.FreezePolicy, false: "not recorded as published"}[cfg.FreezePolicy != ""]))
	}
	fs, err := docs.List[TradeFreeze](e.ctx, e.o.Docs, e.tenant, cTradeFreeze, "")
	if err != nil {
		return s, err
	}
	for _, f := range fs {
		if isoDay(f.Received) < addDays(e.date, -90) || isoDay(f.Received) > e.date || f.Status == "Rejected" {
			continue
		}
		s.checked++
		if why := freezeLate(f, e.now); len(why) > 0 {
			s.bad(fmt.Sprintf("%s · %s · %s", f.ID, siClient(f.UCC), strings.Join(why, "; ")))
		}
	}
	s.summary = fmt.Sprintf("The firm's modes and %d requests of the last 90 days checked; %d found.", s.checked-1, s.found)
	return s, nil
}
