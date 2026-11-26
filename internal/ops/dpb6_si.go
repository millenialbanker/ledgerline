package ops

import (
	"fmt"
	"strings"

	"ledgerline/internal/docs"
)

// ---- self-inspection tests of the round-6 DP controls (dpb6.go) ----

func init() {
	siTests = append(siTests,
		SITest{ID: "amc_refund", Title: "Unused AMC refunded on closure or transfer", Area: "Depository", Severity: "medium", Owner: RoleChecker,
			Source:    dpMC + " 1.10.4",
			ToConfirm: "The quarter in which the account closes is counted as used; the quarters after it are refunded.",
			Checks:    "Accounts closed on a yearly AMC plan with maintenance paid beyond the closure: the refund approved and posted (or its rejection reasoned) within 30 days of the closure.",
			Fix:       "Approve the refund on Depository → Demat accounts & DIS (DP controls).",
			Screen:    "dp", ScreenName: "Depository → Demat accounts & DIS", Real: true, run: siAMCRefund},
		SITest{ID: "death_cert_check", Title: "Death certificates checked and debits frozen on a death intimation", Area: "Depository", Severity: "high", Owner: RoleCompliance,
			Source: dpOI + " 7.5.7; CDSL communique DP2025-411",
			Checks: "Open deceased-client cases of clients with a demat account at the DP: the certificate's check recorded (site or registrar, signed and stamped) within 2 working days, and a debit freeze requested on each open account.",
			Fix:    "Record the certificate check on Compliance → Deceased clients; raise the debit freeze on DP requests.",
			Screen: "deceased", ScreenName: "Compliance → Deceased clients", Real: true, run: siDeathCert},
		SITest{ID: "kyc_deficiency", Title: "CDSL KYC deficiencies: BO told three ways, frozen under code 20 after 30 days", Area: "Depository", Severity: "high", Owner: RoleCompliance,
			Source: dpOI + " 13.5.3",
			Checks: "Cases CDSL reported: the BO told by letter, e-mail and phone within 7 days of receipt; not rectified within 30 days of the last notice: frozen under reason 20 within 7 days, CDSL informed and the acknowledgement sent.",
			Fix:    "Record the notices and the freeze on Depository → Demat accounts & DIS (DP controls).",
			Screen: "dp", ScreenName: "Depository → Demat accounts & DIS", Real: true, run: siKYCDef},
		SITest{ID: "otp_closure_2wd", Title: "OTP transfer-cum-closure requests decided within 2 working days", Area: "Depository", Severity: "medium", Owner: RoleChecker,
			Source:    "CDSL/OPS/DP/POLCY/2026/656 (as amended by 2026/665)",
			ToConfirm: "Only the requests entered in Ledgerline from CDAS are seen.",
			Checks:    "Requests received in the last 90 days: approved or rejected by the second working day after receipt.",
			Fix:       "Decide the request in CDAS and record it on DP controls; find out why it was late.",
			Screen:    "dp", ScreenName: "Depository → Demat accounts & DIS", Real: true, run: siOTPClosure},
		SITest{ID: "kyc_unfreeze_27", Title: "Reason-27 freezes lifted once the KRA validates the KYC", Area: "Depository", Severity: "low", Owner: RoleMaker,
			Source: "CDSL/OPS/DP/POLCY/2026/734; CDSL/OPS/DP/SYSTM/2023/525 (unfreeze process)",
			Checks: "CDSL accounts frozen under reason 27 with every holder's KYC now KRA-validated: an unfreeze request raised.",
			Fix:    "Raise the unfreeze on Compliance → KYC trading gate and follow CDSL's process in CDAS.",
			Screen: "kycgate", ScreenName: "Compliance → KYC trading gate", Real: true, run: siUnfreeze27},
		SITest{ID: "nomination_declared", Title: "Demat accounts opened only with a nomination or a signed opt-out", Area: "Depository", Severity: "high", Owner: RoleChecker,
			Source: dpMC + " 1.14.2-1.14.3",
			Checks: "Clients with a demat account at the DP opened in the last 12 months: nominees on record, or an opt-out declaration with its date and how it was signed.",
			Fix:    "Get the client's nomination or signed declaration and record it (DP controls → Opt-out declaration).",
			Screen: "dp", ScreenName: "Depository → Demat accounts & DIS", Real: true, run: siNominationDeclared},
		SITest{ID: "poa_terms", Title: "PoA and DDPI documents checked against the master circular's terms", Area: "Depository", Severity: "medium", Owner: RoleChecker,
			Source: "SEBI Master Circular for Stock Brokers 2025/90, 36.8.1.1-36.8.1.6",
			Checks: "Open accounts with a PoA or DDPI: a check of the document approved by a second person, with every term met.",
			Fix:    "Tick the terms on DP controls → PoA / DDPI terms and have a checker approve; get a fresh document where a term is missing.",
			Screen: "dp", ScreenName: "Depository → Demat accounts & DIS", Real: true, run: siPoATerms},
		SITest{ID: "ro_joint_freeze", Title: "Joint holders told that a statutory freeze on one freezes the account", Area: "Depository", Severity: "low", Owner: RoleCompliance,
			Source: "CDSL communique DP2025-688; " + dpOI + " Annexure 2.7",
			Checks: "When the DP has joint accounts: the firm practice records that the Rights and Obligations document has the clause.",
			Fix:    "Add the clause to the document and answer the practice in Settings → Firm practices.",
			Screen: "practices", ScreenName: "Settings → Firm practices", Real: true, run: siROJoint},
	)
}

// siNotDP skips a test for a firm that is not a depository participant.
func siNotDP(e *siEnv) (siOut, bool) {
	if v, why := dutyNeed("dp", e.o.firmFacts(e.ctx, e.tenant)); v == triNo {
		s, _ := siSkip("Not a depository participant: " + why)
		return s, true
	}
	return siOut{}, false
}

func siAMCRefund(e *siEnv) (siOut, error) {
	if s, skip := siNotDP(e); skip {
		return s, nil
	}
	var s siOut
	s.input("DP accounts and refunds", e.date)
	rs, err := docs.List[DPAMCRefund](e.ctx, e.o.Docs, e.tenant, cB6DP, "amc|")
	if err != nil {
		return s, err
	}
	for _, r := range rs {
		if r.Status == AMCNotDue {
			continue
		}
		s.checked++
		if r.Status == AMCToApprove && addDays(r.ClosedOn, 30) < e.date {
			s.bad(fmt.Sprintf("%s: ₹%s unused AMC not refunded since %s", siMask(r.UCC), r.Total.String(), fmtDay(r.ClosedOn)))
		}
	}
	return s, nil
}

func siDeathCert(e *siEnv) (siOut, error) {
	if s, skip := siNotDP(e); skip {
		return s, nil
	}
	var s siOut
	s.input("Deceased-client cases", e.date)
	cases, err := docs.List[DemiseCase](e.ctx, e.o.Docs, e.tenant, cDemiseCase, "")
	if err != nil {
		return s, err
	}
	rs, _ := docs.List[DPRequest](e.ctx, e.o.Docs, e.tenant, cDPReq, "")
	asked := map[string]bool{}
	for _, r := range rs {
		if r.Kind == "Freeze" && r.Status != "Rejected" && (r.Detail["type"] == "Debit" || r.Detail["type"] == "Both") {
			asked[normDematID(r.BOID)] = true
		}
	}
	for _, d := range cases {
		if !demiseOpen(d) {
			continue
		}
		bs := e.o.dpAccountsOfUCC(e.ctx, e.tenant, d.UCC)
		if len(bs) == 0 {
			continue
		}
		s.checked++
		di, _, _ := docs.GetOr(e.ctx, e.o.Docs, e.tenant, cB6DP, "intim|"+d.ID, DeathIntimation{})
		if di.CheckedOn == "" && addWorkingDays(e.set, d.ReportedOn, 2) < e.date {
			s.bad(siMask(d.UCC) + ": the death certificate's check is not recorded (" + d.ID + ")")
			continue
		}
		for _, b := range bs {
			frozen := b.Status == "Frozen" && (b.Freeze == "Debit" || b.Freeze == "Both")
			if b.Status != "Closed" && b.Status != "Not opened" && !frozen && !asked[normDematID(b.BOID)] && !strings.Contains(b.CDSLFreeze, "Debit") && b.CDSLFreeze != "Both" {
				s.bad(siMask(d.UCC) + ": no debit freeze on " + cdslMaskBO(b.BOID) + " (" + d.ID + ")")
				break
			}
		}
	}
	return s, nil
}

func siKYCDef(e *siEnv) (siOut, error) {
	if s, skip := siNotDP(e); skip {
		return s, nil
	}
	var s siOut
	s.input("CDSL KYC deficiency cases", e.date)
	ks, err := docs.List[KYCDefCase](e.ctx, e.o.Docs, e.tenant, cB6DP, "kycdef|")
	if err != nil {
		return s, err
	}
	for _, k := range ks {
		s.checked++
		switch st := k.stage(e.date); {
		case st == "Tell the BO" && addDays(k.Received, 7) < e.date:
			s.bad(siMask(k.UCC) + ": not told of the deficiency by letter, e-mail and phone (" + k.ID + ")")
		case st == "Freeze (reason 20)" && addDays(k.Due, 7) < e.date:
			s.bad(siMask(k.UCC) + ": not frozen under reason 20 after the 30 days (" + k.ID + ")")
		case st == "Inform CDSL" && addDays(k.Due, 14) < e.date:
			s.bad(siMask(k.UCC) + ": CDSL not informed or acknowledgement not sent (" + k.ID + ")")
		}
	}
	return s, nil
}

func siOTPClosure(e *siEnv) (siOut, error) {
	if s, skip := siNotDP(e); skip {
		return s, nil
	}
	var s siOut
	s.input("OTP transfer-cum-closure requests", e.date)
	rs, err := docs.List[OTPClosure](e.ctx, e.o.Docs, e.tenant, cB6DP, "otpclose|")
	if err != nil {
		return s, err
	}
	for _, r := range rs {
		if r.Received < addDays(e.date, -90) {
			continue
		}
		s.checked++
		decided := isoDay(r.At)
		if (r.Status == "Pending" && r.Due < e.date) || (r.Status != "Pending" && decided > r.Due) {
			s.bad(fmt.Sprintf("%s: received %s, due %s, %s", siMask(r.UCC), fmtDay(r.Received), fmtDay(r.Due), map[bool]string{true: "still pending", false: "decided " + fmtDay(decided)}[r.Status == "Pending"]))
		}
	}
	return s, nil
}

func siUnfreeze27(e *siEnv) (siOut, error) {
	if v, why := dutyNeed("cdsl", e.o.firmFacts(e.ctx, e.tenant)); v == triNo {
		return siSkip("Not a CDSL participant: " + why)
	}
	var s siOut
	s.input("CDSL freeze master", e.date)
	for _, a := range e.o.kycUnfreezeReady(e.ctx, e.tenant, e.date) {
		s.checked++
		if a.Request == "" {
			s.bad(siMask(a.UCC) + ": KYC validated, still frozen under 27 (" + cdslMaskBO(a.BOID) + ")")
		}
	}
	return s, nil
}

func siNominationDeclared(e *siEnv) (siOut, error) {
	if s, skip := siNotDP(e); skip {
		return s, nil
	}
	var s siOut
	s.input("Clients and DP accounts", e.date)
	from := addMonths(e.date, -12)
	for _, b := range e.o.boAccounts(e.ctx, e.tenant) {
		if b.BOID == "" || b.Status == "Not opened" || b.Opened < from || b.Opened > e.date {
			continue
		}
		c, ok := e.clients[b.UCC]
		if !ok {
			continue
		}
		s.checked++
		if noNominationChoice(c) != "" {
			s.bad(siMask(c.UCC) + ": " + strings.ToLower(c.NominationState()) + " (opened " + fmtDay(b.Opened) + ")")
		}
	}
	return s, nil
}

func siPoATerms(e *siEnv) (siOut, error) {
	if s, skip := siNotDP(e); skip {
		return s, nil
	}
	var s siOut
	s.input("DP accounts with a PoA or DDPI", e.date)
	for _, b := range e.o.poaAccounts(e.ctx, e.tenant) {
		s.checked++
		p, _, err := docs.Get[PoACheck](e.ctx, e.o.Docs, e.tenant, cB6DP, "poa|"+b.BOID)
		switch {
		case err != nil:
			s.bad(siMask(b.UCC) + ": the PoA/DDPI document's terms not checked")
		case p.Status != "Approved":
			s.bad(siMask(b.UCC) + ": PoA/DDPI check " + strings.ToLower(p.Status))
		}
	}
	return s, nil
}

func siROJoint(e *siEnv) (siOut, error) {
	if s, skip := siNotDP(e); skip {
		return s, nil
	}
	var s siOut
	s.input("DP accounts", e.date)
	joint := 0
	for _, b := range e.o.boAccounts(e.ctx, e.tenant) {
		if len(b.JointHolders) > 0 && b.Status != "Closed" {
			joint++
		}
	}
	if joint == 0 {
		return siSkip("No joint accounts at the DP")
	}
	s.checked = 1
	if applied, _ := e.o.tcPractice(e.ctx, e.tenant, "ro-joint-freeze"); applied != "included" {
		s.bad(fmt.Sprintf("%d joint account%s; the Rights and Obligations is not recorded as having the joint-holder freeze clause", joint, plural(joint)))
	}
	return s, nil
}
