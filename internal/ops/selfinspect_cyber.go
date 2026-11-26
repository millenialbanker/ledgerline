package ops

import (
	"fmt"
	"strings"

	"ledgerline/internal/docs"
)

// ---- self-inspection tests: cyber security, data protection, the
// insider trading code and staff controls ----
//
// Each reads the records the screens keep (incfollow.go, resilience.go,
// authlock.go, dataprivacy.go, pit.go, staffctl.go) and changes nothing.

func init() {
	siTests = append(siTests,
		SITest{ID: "incident_followups", Title: "Cyber incidents taken on in time and their follow-up reports filed", Area: "Security", Severity: "high", Owner: RoleCompliance,
			Source:    "CSCRF SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2024/113, Part I DE.CM.S3 (alerts investigated within a set time) and Annexure-O B.3 (interim T+3, mitigation T+7, RCA T+30, VAPT and closure T+45, forensic T+75); CDSL/AUDIT/DP/POLCY/2026/151 Annexure A Table 2 (same-day mitigation report); SEBI HO/(449)2026-ITD-5_D via NSE/INSP/75970 (SEBI's incident portal within 24 hours); DPDP Rules 2025 rule 7 (from 13 May 2027)",
			ToConfirm: "CSCRF leaves the time to investigate to the entity: the hours are the firm's (Security → Incidents → Settings). \"Without delay\" in DPDP rule 7 is shown as 24 hours.",
			Checks:    "Incidents of the last year: each taken on (an owner, or Investigating) within the hours for its severity; for each reportable one, every follow-up report recorded by its date, or the forensic report marked not needed with the reason.",
			Fix:       "Take the incident on; record each follow-up report with its reference on Security → Incidents.",
			Screen:    "incidents", ScreenName: "Security → Incidents", Real: true, run: siIncidentFollowUps},
		SITest{ID: "restore_drill", Title: "Backups restored in a drill in the last six months, within RTO and RPO", Area: "Security", Severity: "medium", Owner: RoleAdmin,
			Source: "CSCRF Part I PR.IP.S7-S8 (periodic restoration drills), RC.RP.S1 (isolated backups, ransomware drills); SEBI cloud framework SEBI/HO/ITD/ITD_VAPT/P/CIR/2023/033 Annex 1 s.6.2.12 (backup and recovery tested at least twice a year); SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2025/119 para 6.10 (RTO 2 hours, RPO 15 minutes)",
			Checks: "The last restore or DR drill on record: within the last 183 days, the system back within 2 hours of the failure and the data restored no more than 15 minutes older than it.",
			Fix:    "Restore a backup (for Ledgerline, from an off-site or immutable copy) into a test environment, time it and record it on Security → Cyber controls.",
			Screen: "cyber", ScreenName: "Security → Cyber controls", Real: true, run: siRestoreDrill},
		SITest{ID: "signin_controls", Title: "Sign-in locks need an out-of-band unlock, passwords expire and logs are kept two years", Area: "Security", Severity: "medium", Owner: RoleAdmin,
			Source: "CSCRF Annexure-G 5-7 (out-of-band unlock, rate limiting), Part I PR.AA.S9 (user access logs kept at least 2 years); NSE/INSP/73850 (circular 20/2026) Annex D 5-6 (lock after 5 tries, 90-day expiry, history of 6)",
			Checks: "The firm's sign-in settings: out-of-band unlock (not the timed lock), passwords expiring within 90 days, at least 6 past passwords refused, security logs kept at least 730 days.",
			Fix:    "Change the settings on Security → Incidents → Settings.",
			Screen: "incidents", ScreenName: "Security → Incidents (Settings)", Real: true, run: siSignInControls},
		SITest{ID: "ntp_source", Title: "The server's clock follows NIC's or NPL's time servers", Area: "Security", Severity: "low", Owner: RoleAdmin,
			Source:    "CERT-In directions No. 20(3)/2022-CERT-In of 28 April 2022, direction (i)",
			ToConfirm: "Ledgerline reads the time service configuration of the server it runs on (or LEDGERLINE_NTP_SERVERS); the firm's own computers are its duty.",
			Checks:    "The time servers the Ledgerline server uses: at least one of NIC's (samay1.nic.in, samay2.nic.in) or NPL's (time.nplindia.org).",
			Fix:       "Ask the operator to point the server's time service at NIC's or NPL's servers and set LEDGERLINE_NTP_SERVERS.",
			Screen:    "cyber", ScreenName: "Security → Cyber controls", Real: true, run: siNTPSource},
		SITest{ID: "vendor_exit_plans", Title: "Each critical technology vendor has a reviewed exit plan", Area: "Security", Severity: "medium", Owner: RoleCompliance,
			Source: "SEBI cloud framework Annex 1 s.9 (lock-in and concentration risk; exit strategy with data portability); stock brokers' master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90 para 61.2-61.3 (a workable plan to switch the trading, RMS or back office vendor)",
			Checks: "Every vendor marked critical on Security → Cyber controls (Ledgerline itself included): an alternative named, a way to get the data out, exit steps, the lock-in and concentration assessed, and reviewed in the last year.",
			Fix:    "Complete and review the vendor's record on Security → Cyber controls.",
			Screen: "cyber", ScreenName: "Security → Cyber controls", Real: true, run: siVendorExitPlans},
		SITest{ID: "privileged_review", Title: "The Admins' activity reviewed every half year, each by someone else", Area: "Security", Severity: "medium", Owner: RoleCompliance,
			Source: "CSCRF Part I PR.AA.S11 (privileged users' activities reviewed periodically; privileged users do not review their own logs)",
			Checks: "From 30 days into a half year: the review of the previous half year built and every Admin signed off by another person.",
			Fix:    "Build the review on Security → Cyber controls and have someone other than each Admin sign them off.",
			Screen: "cyber", ScreenName: "Security → Cyber controls", Real: true, run: siPrivReview},
		SITest{ID: "data_requests", Title: "Clients' data requests answered within 90 days; closed accounts erased after retention", Area: "Clients", Severity: "medium", Owner: RoleCompliance,
			Source:    "Digital Personal Data Protection Act 2023 s.8(7), s.11-14; DPDP Rules 2025 rules 8 and 14(3) (from 13 May 2027)",
			ToConfirm: "The firm's record retention (the archive's years, 10 by default) is taken as the time the law needs the records kept.",
			Checks:    "Data requests from clients: answered within 90 days. Closed accounts whose retention ended: erased, or on a legal hold.",
			Fix:       "Answer the request on Security → Cyber controls (Data protection); erase the closed account or put it on a legal hold.",
			Screen:    "cyber", ScreenName: "Security → Cyber controls", Real: true, run: siDataRequests},
		SITest{ID: "branch_data_consent", Title: "Consent to sensitive personal data recorded for accounts opened at the branch", Area: "Clients", Severity: "low", Owner: RoleChecker,
			Source: "IT (Reasonable Security Practices and Procedures and Sensitive Personal Data or Information) Rules 2011, rule 5(1) (written consent before collecting sensitive personal data)",
			Checks: "Clients whose application was made in the last 60 days by staff (not online, not migrated): the signed form's consent recorded.",
			Fix:    "Record the consent with the form number on the client (Clients → the client → Data consent).",
			Screen: "clients", ScreenName: "Clients & onboarding → Clients", Real: true, run: siBranchConsent},
		SITest{ID: "pit_code", Title: "The insider trading code's records are kept", Area: "People", Severity: "medium", Owner: RoleCompliance,
			Source: "SEBI (Prohibition of Insider Trading) Regulations 2015, reg. 9(4) (designated persons on the board's criteria), Schedule C cl. 7-9 and 12 (trading window of the pre-clearance, contra trades, post-trade and no-trade reports, yearly disclosure of relatives)",
			Checks: "With HRMS on: the board's approval of the designation criteria recorded; each designated person's ground recorded; each designated person's disclosure of relatives for this financial year after the declaration month; every approved pre-clearance past its validity reported on; no contra trade approved without a written relaxation.",
			Fix:    "Record what is missing on People → Own trading.",
			Screen: "people", ScreenName: "People → Own trading", Real: true, run: siPITCode},
		SITest{ID: "staff_controls", Title: "AML-sensitive staff checked, DP compliance officer eligible, DP staff photo ID on file", Area: "People", Severity: "medium", Owner: RoleCompliance,
			Source: "SEBI AML/CFT master circular SEBI/HO/MIRSD/MIRSDSECFATF/P/CIR/2024/78 para 80; CDSL master circular for DPs CDSL/OPS/DP/POLCY/2026/257 para 10.1.1-10.1.2; CDSL operating instructions 17.7.2",
			Checks: "With HRMS on, everyone on the rolls: a suitability check for each person in an AML-sensitive position; the compliance officer's eligibility recorded and met, and NISM Series III-A held after a year; an ID proof on file for each person in DP operations.",
			Fix:    "Record the check, the eligibility or the ID proof on the person (People → Staff controls).",
			Screen: "people", ScreenName: "People → Staff controls", Real: true, run: siStaffControls},
	)
}

func siIncidentFollowUps(e *siEnv) (siOut, error) {
	from := addDays(e.date, -365)
	xs, err := e.o.incidentsSince(e.ctx, e.tenant, from)
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Security incidents", from)
	for _, x := range xs {
		if x.Source == "provider" {
			continue
		}
		s.checked++
		if x.Investigate.Overdue || x.Investigate.TakenAt != "" && !x.Investigate.Met {
			s.bad(fmt.Sprintf("%s · %s · not taken on within %d hours (by %s)", x.ID, x.Severity, x.Investigate.Hours, x.Investigate.DueIST))
		}
		for _, f := range x.FollowUps {
			if f.Overdue || f.Late {
				s.bad(fmt.Sprintf("%s · %s %s (due %s)", x.ID, f.Name, map[bool]string{true: "not filed", false: "filed late"}[f.Overdue], f.DueIST))
			}
		}
	}
	s.summary = fmt.Sprintf("%d incidents of the last year checked; %d late steps.", s.checked, s.found)
	return s, nil
}

func siRestoreDrill(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Restore drills", e.date)
	ds := e.o.restoreDrills(e.ctx, e.tenant)
	s.checked = len(ds)
	if len(ds) == 0 {
		s.bad("No restore or DR drill is recorded")
		s.summary = "No restore drill on record."
		return s, nil
	}
	d := ds[0]
	if n := daysBetween(d.Started[:10], e.date); n > 183 {
		s.bad(fmt.Sprintf("%s · the last drill was on %s, %d days ago", d.ID, fmtDay(d.Started[:10]), n))
	}
	if !d.Passed {
		s.bad(fmt.Sprintf("%s · %s back in %s with %d minutes of data lost (targets: 2 hours, 15 minutes)", d.ID, d.System, minutesText(d.RTOMin), d.RPOMin))
	}
	s.summary = fmt.Sprintf("Last drill %s on %s: RTO %s, RPO %d min.", d.ID, fmtDay(d.Started[:10]), minutesText(d.RTOMin), d.RPOMin)
	return s, nil
}

func siSignInControls(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Security settings", e.date)
	set := e.o.SecuritySettings(e.ctx, e.tenant)
	s.checked = 4
	if set.TimedUnlock {
		s.bad("A locked sign-in opens again by itself after 15 minutes (no out-of-band unlock)")
	}
	if d := set.passwordDays(); d == 0 || d > 90 {
		s.bad(fmt.Sprintf("Passwords %s", map[bool]string{true: "never expire", false: fmt.Sprintf("last %d days", d)}[d == 0]))
	}
	if set.passwordHistory() < 6 {
		s.bad(fmt.Sprintf("Only the last %d passwords are refused", set.passwordHistory()))
	}
	if set.RetentionDays < 730 {
		s.bad(fmt.Sprintf("Security logs are kept %d days", set.RetentionDays))
	}
	s.summary = fmt.Sprintf("%d of 4 sign-in settings short of the rules.", s.found)
	return s, nil
}

func siNTPSource(e *siEnv) (siOut, error) {
	var s siOut
	n := e.o.ntp()
	s.input("The server's time service", e.date)
	s.checked = 1
	if n.State != "NIC/NPL" {
		s.bad(n.Line)
	}
	s.summary = n.Line
	return s, nil
}

func siVendorExitPlans(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Technology vendors", e.date)
	for _, v := range e.o.techVendors(e.ctx, e.tenant) {
		if !v.Critical {
			continue
		}
		s.checked++
		if len(v.Gaps) > 0 {
			s.bad(v.Name + " · " + strings.Join(v.Gaps, "; "))
		}
	}
	s.summary = fmt.Sprintf("%d critical vendors; %d plans incomplete.", s.checked, s.found)
	return s, nil
}

func siPrivReview(e *siEnv) (siOut, error) {
	var s siOut
	_, from, _ := privPeriod(e.date)
	pid, _, _ := privPeriod(addDays(from, -1))
	s.input("Privileged activity reviews", e.date)
	s.checked = 1
	if daysBetween(from, e.date) < 30 {
		s.summary = "The review of " + pid + " is due 30 days into the half year."
		return s, nil
	}
	r, _, err := docs.Get[PrivReview](e.ctx, e.o.Docs, e.tenant, cPrivReview, pid)
	switch {
	case err != nil:
		s.bad("The review of " + pid + " was not built")
	case r.Status != "Done":
		for _, a := range r.Admins {
			if a.ReviewedBy == "" {
				s.bad(pid + " · " + a.User + " not signed off")
			}
		}
	}
	s.summary = fmt.Sprintf("Review of %s: %s.", pid, map[bool]string{true: "done", false: "not done"}[s.found == 0])
	return s, nil
}

func siDataRequests(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Data requests", e.date)
	rs, _ := docs.List[DataRequest](e.ctx, e.o.Docs, e.tenant, cDataReq, "")
	for _, r := range rs {
		s.checked++
		switch {
		case r.Status == "Open" && r.Due < e.date:
			s.bad(fmt.Sprintf("%s · %s · %s, open past %s", r.ID, siClient(r.UCC), strings.ToLower(r.Kind), fmtDay(r.Due)))
		case r.Answered != "" && r.Answered[:10] > r.Due:
			s.bad(fmt.Sprintf("%s · %s · answered %s, after %s", r.ID, siClient(r.UCC), fmtDay(r.Answered[:10]), fmtDay(r.Due)))
		}
	}
	for _, c := range e.o.erasureList(e.ctx, e.tenant) {
		s.checked++
		if c.Due {
			s.bad(fmt.Sprintf("%s · closed %s, kept until %s, not erased", siClient(c.UCC), fmtDay(c.ClosedOn), fmtDay(c.KeepUntil)))
		}
	}
	s.summary = fmt.Sprintf("%d requests and closed accounts checked; %d late.", s.checked, s.found)
	return s, nil
}

func siBranchConsent(e *siEnv) (siOut, error) {
	var s siOut
	from := addDays(e.date, -60)
	s.input("Clients' applications", from)
	for _, c := range e.clients {
		if c.KYCDate < from || c.KYCDate > e.date || strings.HasPrefix(c.CreatedBy, "applicant:") || c.CreatedBy == "" || strings.HasPrefix(c.CreatedBy, "migrat") || contains(c.Tags, "MIGRATED") {
			continue
		}
		s.checked++
		if c.DataConsent == nil {
			s.bad(fmt.Sprintf("%s · application of %s by %s, no consent recorded", siClient(c.UCC), fmtDay(c.KYCDate), c.CreatedBy))
		}
	}
	s.summary = fmt.Sprintf("%d branch applications of the last 60 days; %d without the consent record.", s.checked, s.found)
	return s, nil
}

func siPITCode(e *siEnv) (siOut, error) {
	if !e.o.ModuleOn(e.ctx, e.tenant, ModHRMS) {
		return siSkip("Not checkable: the HRMS module is off, so Ledgerline keeps no own-trading register.")
	}
	var s siOut
	s.input("Own-trading register", e.date)
	p := e.o.hrPolicy(e.ctx, e.tenant)
	es, _ := e.o.employees(e.ctx, e.tenant)
	des := 0
	for _, x := range es {
		if x.Status != EmpLeft && x.Designated {
			des++
		}
	}
	s.checked++
	if des > 0 && p.OwnTrading.BoardApproval == nil {
		s.bad("The board's approval of the designated-person criteria is not recorded")
	}
	fy := finYear(e.date)
	have := map[string]bool{}
	ds, _ := docs.List[PITDisclosure](e.ctx, e.o.Docs, e.tenant, cPITDiscl, "")
	for _, d := range ds {
		if d.Period == fy {
			have[d.Employee] = true
		}
	}
	// After the declaration month, to the end of the financial year.
	m := atoiSafe(e.date[5:7])
	afterMonth := m > p.OwnTrading.AnnualDueMonth || m < 4
	for _, x := range es {
		if x.Status == EmpLeft || !x.Designated {
			continue
		}
		s.checked++
		if x.DesignatedFor == "" {
			s.bad(x.Code + " · designated with no ground recorded")
		}
		if afterMonth && !have[x.Code] {
			s.bad(x.Code + " · no disclosure of relatives for " + fy)
		}
	}
	ps, _ := docs.List[PreClearance](e.ctx, e.o.Docs, e.tenant, cPreClear, "")
	for _, x := range ps {
		if x.Status != "Approved" {
			continue
		}
		s.checked++
		if x.Report == nil && x.ValidTill != "" && x.ValidTill < e.date {
			s.bad(x.ID + " · no post-trade or no-trade report")
		}
		if x.Contra && x.ContraRelaxed == "" {
			s.bad(x.ID + " · contra trade approved without a written relaxation")
		}
	}
	s.summary = fmt.Sprintf("%d records of the code checked; %d gaps.", s.checked, s.found)
	return s, nil
}

func siStaffControls(e *siEnv) (siOut, error) {
	if !e.o.ModuleOn(e.ctx, e.tenant, ModHRMS) {
		return siSkip("Not checkable: the HRMS module is off, so Ledgerline keeps no staff records.")
	}
	var s siOut
	s.input("Staff master", e.date)
	v := e.o.staffControls(e.ctx, e.tenant, e.date)
	s.checked = v.AMLPeople + v.DPStaff + v.COs
	for _, g := range v.Gaps {
		s.bad(g.Code + " · " + g.What)
	}
	s.summary = fmt.Sprintf("%d people in AML-sensitive positions, %d in DP operations, %d compliance officers; %d gaps.", v.AMLPeople, v.DPStaff, v.COs, s.found)
	return s, nil
}
