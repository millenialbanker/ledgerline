package ops

import (
	"fmt"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
)

// ---- self-inspection tests of client statements and notices ----
//
// From the compliance gap report of November 2026 (workstream E): the
// annual global statement, referral rewards only to registered APs, and
// the SMART ODR notice on complaints left unresolved. The weekly
// statement's coverage is in weekly_statement (selfinspect_lib.go).

func init() {
	siTests = append(siTests, siStmtTests...)
}

var siStmtTests = []SITest{
	{ID: "annual_statement", Title: "Annual global statement sent to every client within 30 days of the year's end", Area: "Clients", Severity: "medium", Owner: RoleMaker,
		Source:    "NSE/INSP/73976 (Inspection master circular), item 4.17 and Exhibit 30; SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, para 23.9 and the investor charter",
		ToConfirm: "Exhibit 30's own layout was not at hand: the statement has the columns item 4.17 lists. One statement per UCC.",
		Checks:    "After 30 April: every client who traded in the financial year just ended was sent the signed annual global statement (dispatch register), and by 30 April.",
		Fix:       "Send the year's statements on Reports → Year-end packs (Email all clients) or Periodic work → Annual global statement.",
		Screen:    "yearend", ScreenName: "Reports → Year-end packs", Real: true, run: siAnnualStatement},
	{ID: "referral_rewards_ap", Title: "Referral rewards paid only to registered authorised persons", Area: "Compliance", Severity: "high", Owner: RoleCompliance,
		Source: "NSE/INSP/73976 (Inspection master circular), item 4.21; MCX/INSP/255/2026, para 9.9(a): brokerage or other consideration for bringing clients shared only with registered APs",
		Checks: "Referral rewards booked in the last 180 days: none credited to a client's ledger for a period ending on or after the day the AP-only rule applies from (Settings → Partners and referrals).",
		Fix:    "Reverse the reward (a journal back from the client to referral expense) and record the client as the source only; pay registered APs through the referral or AP payout run.",
		Screen: "partners", ScreenName: "Branches & partners → Referrals", Real: true, run: siReferralRewards},
	{ID: "complaints_odr", Title: "Clients told of SCORES and SMART ODR when a complaint stays unresolved", Area: "Compliance", Severity: "medium", Owner: RoleCompliance,
		Source: "SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, Annex 37 (investor charter: grievance redressal and the ODR route); NSE/ISC/73908, Part A 7.2",
		Checks: "Complaints from clients still open after their 21 days: each one's client was sent the SCORES and SMART ODR notice.",
		Fix:    "Ledgerline sends the notice each morning; check the client's email and mobile and the message templates (ticket.odr) on Client messages.",
		Screen: "helpdesk", ScreenName: "Compliance → Investor complaints", Real: true, run: siComplaintsODR},
}

// siReferralRewards: no reward to a client referrer after the cut-off.
func siReferralRewards(e *siEnv) (siOut, error) {
	cut := e.set.referralCutoff()
	es, err := e.o.App.Ledger.Store.List(e.ctx, e.tenant, ledger.Filter{From: addDays(e.date, -180), To: e.date})
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Referral rewards booked", e.date)
	for _, en := range es {
		if en.Source.Kind != "REFERRAL" {
			continue
		}
		s.checked++
		if en.ValueDate < cut {
			continue
		}
		for _, p := range en.Postings {
			if strings.HasPrefix(p.Account, "CLIENT:") && p.Credit > 0 {
				s.bad(fmt.Sprintf("%s · %s · ₹%s credited to client %s after %s", fmtDay(en.ValueDate), en.ID, formatINR(p.Credit.Dec()), siMask(strings.TrimPrefix(p.Account, "CLIENT:")), fmtDay(cut)))
			}
		}
	}
	if s.checked == 0 {
		return siSkip("Nothing to check: no referral reward booked in the last 180 days.", s.inputs...)
	}
	s.summary = fmt.Sprintf("%d referral postings checked; %d to clients after the AP-only rule.", s.checked, s.found)
	return s, nil
}

// siComplaintsODR: complaints past 21 days carry the ODR notice.
func siComplaintsODR(e *siEnv) (siOut, error) {
	ts, err := docs.List[Ticket](e.ctx, e.o.Docs, e.tenant, cTicket, "")
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Investor complaints", e.date)
	for _, t := range ts {
		if t.Kind != KindComplaint || t.UCC == "" || t.Due == "" || t.Due >= e.date || t.Status == "Resolved" || t.Status == "Closed" {
			continue
		}
		s.checked++
		if t.ODRNoticeOn == "" {
			s.bad(fmt.Sprintf("%s · received %s, due %s · client %s not told of SCORES and SMART ODR", t.ID, fmtDay(t.OpenedOn), fmtDay(t.Due), siMask(t.UCC)))
		}
	}
	if s.checked == 0 {
		return siSkip("Nothing to check: no client complaint is open past its 21 days.", s.inputs...)
	}
	s.summary = fmt.Sprintf("%d complaints open past 21 days; %d without the notice.", s.checked, s.found)
	return s, nil
}
