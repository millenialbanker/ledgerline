package ops

import (
	"fmt"
	"strings"

	"ledgerline/internal/docs"
)

// ---- self-inspection tests of the KYC trading gate (kycgate.go,
// kycgate_cdsl.go, proi.go) ----

func init() {
	siTests = append(siTests,
		SITest{ID: "kyc_gate", Title: "Clients the KRA has not verified, or on an exchange's KYC list, cannot trade", Area: "Clients", Severity: "high", Owner: RoleCompliance,
			Source:    "SEBI/HO/MIRSD/SECFATF/P/CIR/2024/41 and SEBI's FAQ of May 2024 (FAQ 42, 45); SEBI KYC master circular SEBI/HO/MIRSD/SECFATF/P/CIR/2023/169 paras 83 and 99; NSE/ISC/75724 (37/2026) items 3-4; NSE/ISC/77375 (50/2026); BSE notice 20261109-28; MCX/S&I/522/2026",
			ToConfirm: "The exchanges do not publish the columns of their KYC lists: Ledgerline reads them by header names. A trade on another exchange than the one whose list holds the client is counted too.",
			Checks:    "The KYC trading gate is on; no client on an exchange's KYC list is left unworked more than 30 days after the list; no trade, in the last 30 days, by a client on such a list after the exchange's Not Permitted to Trade date.",
			Fix:       "Keep the gate on (Firm practices); get each listed client's KYC validated at the KRA, or close the UCC; find out how the trade went through and block the client on the front end.",
			Screen:    "kycgate", ScreenName: "Compliance → KYC trading gate", Real: true, run: siKYCGate},
		SITest{ID: "kra_upload_3days", Title: "KYC uploaded to the KRA within 3 working days", Area: "Clients", Severity: "medium", Owner: RoleCompliance,
			Source:    "SEBI KYC master circular SEBI/HO/MIRSD/SECFATF/P/CIR/2023/169, para 84 (KYC records uploaded to the KRA within 3 working days); BSE notice 20250903-49 and extensions; NSE/ISC/75249 (33/2026) para 4; SEBI HO/38/30/12(3)2026 para 5.7 (PROI)",
			ToConfirm: "The day of upload is the day the KRA file holding the client was marked uploaded; a new client whose KRA status is already loaded counts as uploaded on time.",
			Checks:    "Clients whose KYC was completed, or whose KYC details changed, in the last 60 days: in a KRA upload file marked uploaded within 3 working days.",
			Fix:       "Make the KRA upload file on Compliance → KYC & PMLA, upload it at the KRA and mark it uploaded with the KRA's reference.",
			Screen:    "kyc", ScreenName: "Compliance → KYC & PMLA", Real: true, run: siKRAUpload},
		SITest{ID: "cdsl_kyc_freeze", Title: "Demat accounts with a holder's KYC not verified are frozen (CDSL reason 27)", Area: "Depository", Severity: "high", Owner: RoleCompliance,
			Source:    "CDSL/OPS/DP/POLCY/2026/234 (freeze for debit and credit under reason 27); CDSL/OPS/DP/POLCY/2026/770 (KYC of all holders at a KRA); CDSL/OPS/DP/POLCY/2026/325 (DP-wise KRA-deficient lists)",
			ToConfirm: "The reason code and the rule are taken from the compliance gap report's reading of the communiqués; a joint holder's KRA status is known only when the KRA status file has the holder's PAN.",
			Checks:    "Every CDSL account of the DP with a holder (joint holders included) whose KRA status is not Registered or Validated: CDSL's freeze master shows a freeze under reason 27 for debit and credit, or a freeze request is raised and not more than 5 days old.",
			Fix:       "Raise the freeze requests on Compliance → KYC trading gate (Demat accounts), approve them and set them in CDAS; follow up each holder to get the KYC validated, then unfreeze by the prescribed procedure.",
			Screen:    "kycgate", ScreenName: "Compliance → KYC trading gate", Real: true, run: siCDSLKYCFreeze},
		SITest{ID: "cdsl_mnrl_freeze", Title: "Revoked mobiles on demat accounts: 30-day notice, then debit freeze (CDSL reason 31)", Area: "Depository", Severity: "medium", Owner: RoleMaker,
			Source:    "CDSL/OPS/DP/POLCY/2025/618 (30-day notice; debit freeze under reason 31; freezes under code 21 for invalid mobiles moved to 31)",
			ToConfirm: "When the notice must be sent after a match is found is not set: 30 days (the firm's practice for a contacted client) is used.",
			Checks:    "Open revoked-mobile matches on the DP's CDSL accounts: a notice recorded within 30 days of the match; a debit freeze requested once the notice has run out; no account still frozen under code 21.",
			Fix:       "Send the notice and record it on Compliance → KYC trading gate (Demat accounts); raise the debit freeze when it runs out; move code-21 freezes to 31 in CDAS.",
			Screen:    "kycgate", ScreenName: "Compliance → KYC trading gate", Real: true, run: siCDSLMNRL},
		SITest{ID: "proi_kyc", Title: "Clients resident outside India onboarded by SEBI's PROI process", Area: "Clients", Severity: "high", Owner: RoleCompliance,
			Source: "SEBI HO/38/30/12(3)2026-MIRSD-POD2/I/25175/2026 paras 5.1-5.10, forwarded by NSE/INSP/77335 (54/2026) and CDSL DP2026-780; NSE/ISC/71043 (41/2025: NRO is category 37)",
			Checks: "NRI, OCI and foreign national clients onboarded from 3 Dec 2026: KRA and CKYCRR looked up before the documents, CKYC identifier sought, PAN verified, passport (and OCI card), overseas address with its document, e-mail verified, IPV or compliant video IPV with the official and time, translations; NRIs with an NRO account reported to NSE as category 37.",
			Fix:    "Complete the PROI record on Compliance → KYC trading gate (PROI onboarding); record the NRE or NRO account on NRI (PIS) so the UCC goes with the right category.",
			Screen: "kycgate", ScreenName: "Compliance → KYC trading gate", Real: true, run: siPROI},
	)
}

func siKYCGate(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Clients' KRA status and the exchanges' KYC lists", e.date)
	if e.set.KYCBlockOff {
		n := 0
		for _, c := range e.clients {
			if c.Status == "Active" && !c.DPOnly && !kraVerified(c.KRAStatus) {
				n++
			}
		}
		s.checked++
		s.bad(fmt.Sprintf("the KYC trading gate is off: %d active client%s not verified by a KRA can trade", n, plural(n)))
	}
	held := map[string][]KYCHold{}
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if c.Status != "Active" || c.DPOnly {
			continue
		}
		for _, h := range c.KYCHolds {
			if h.demat() {
				continue
			}
			s.checked++
			held[u] = append(held[u], h)
			if addDays(h.Since, 30) < e.date {
				s.bad(fmt.Sprintf("%s · on the %s list since %s, still not validated", siClient(u), h.Exchange, fmtDay(h.Since)))
			}
		}
	}
	if len(held) > 0 {
		from := addDays(e.date, -siWindow)
		ts, err := siTradesSince(e, from)
		if err != nil {
			return s, err
		}
		s.input("Trades", from)
		seen := map[string]bool{}
		for _, t := range ts {
			u := strings.ToUpper(t.UCC)
			for _, h := range held[u] {
				if h.NPTTFrom != "" && t.TradeDate >= h.NPTTFrom && !seen[u+t.TradeDate] {
					seen[u+t.TradeDate] = true
					s.bad(fmt.Sprintf("%s · traded on %s on %s, after the %s list's not-permitted date %s", siClient(u), t.Exchange, fmtDay(t.TradeDate), h.Exchange, fmtDay(h.NPTTFrom)))
				}
			}
		}
	}
	s.summary = fmt.Sprintf("The gate is %s; %d client%s on exchanges' KYC lists checked; %d problems.", map[bool]string{true: "off", false: "on"}[e.set.KYCBlockOff], len(held), plural(len(held)), s.found)
	return s, nil
}

func siKRAUpload(e *siEnv) (siOut, error) {
	var s siOut
	s.input("KYC dates and KRA upload files", e.date)
	for _, it := range e.o.kraUploads(e.ctx, e.tenant, e.set, addDays(e.date, -60), e.date) {
		if it.Uploaded == "" && it.Due >= e.date {
			continue // not due yet
		}
		s.checked++
		if it.Late {
			s.bad(fmt.Sprintf("%s · KYC %s %s, due at the KRA by %s, %s", siClient(it.UCC), map[string]string{"New": "completed", "Modify": "changed"}[it.Kind], fmtDay(it.From), fmtDay(it.Due),
				map[bool]string{true: "not uploaded", false: "uploaded " + fmtDay(it.Uploaded)}[it.Uploaded == ""]))
		}
	}
	s.summary = fmt.Sprintf("%d KYC uploads due in the last 60 days checked; %d late.", s.checked, s.found)
	return s, nil
}

func siCDSLKYCFreeze(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Demat accounts and holders' KRA status", e.date)
	s.input("CDSL freeze master (DPE6)", e.date)
	rs, _ := docs.List[DPRequest](e.ctx, e.o.Docs, e.tenant, cDPReq, "")
	at := map[string]string{}
	for _, r := range rs {
		at[r.ID] = r.At
	}
	for _, a := range e.o.dematKYCAccounts(e.ctx, e.tenant, e.clients, e.date) {
		s.checked++
		switch {
		case a.Frozen27:
		case a.Request == "":
			s.bad(fmt.Sprintf("%s · demat %s · a holder's KYC not verified, not frozen under reason 27", siClient(a.UCC), cdslMaskBO(a.BOID)))
		case len(at[a.Request]) >= 10 && addDays(at[a.Request][:10], 5) < e.date:
			s.bad(fmt.Sprintf("%s · demat %s · freeze %s requested on %s, not yet shown by CDSL", siClient(a.UCC), cdslMaskBO(a.BOID), a.Request, fmtDay(at[a.Request][:10])))
		}
	}
	s.summary = fmt.Sprintf("%d demat accounts with a holder not verified by a KRA checked; %d not frozen.", s.checked, s.found)
	return s, nil
}

func siCDSLMNRL(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Revoked-mobile matches on CDSL accounts", e.date)
	ms, _ := docs.List[MNRLMatch](e.ctx, e.o.Docs, e.tenant, cMNRLMatch, "")
	opened := map[string]string{}
	for _, m := range ms {
		opened[m.ID] = m.Opened
	}
	for _, n := range e.o.mnrlNotices(e.ctx, e.tenant, e.clients, e.date) {
		if !mnrlOpen(n.Status) {
			continue
		}
		s.checked++
		switch {
		case n.NoticeOn == "" && addDays(opened[n.Match], mnrlContactDays) < e.date:
			s.bad(fmt.Sprintf("%s · match %s since %s · no 30-day notice recorded", siClient(n.UCC), n.Match, fmtDay(opened[n.Match])))
		case n.Expired && n.Request == "":
			s.bad(fmt.Sprintf("%s · match %s · notice ran out on %s, no debit freeze (31) requested", siClient(n.UCC), n.Match, fmtDay(n.Expiry)))
		}
	}
	for _, f := range e.o.code21Freezes(e.ctx, e.tenant, e.date) {
		s.checked++
		s.bad(fmt.Sprintf("%s · demat %s · freeze %s still under code 21", siClient(f.UCC), cdslMaskBO(f.BOID), f.FreezeID))
	}
	s.summary = fmt.Sprintf("%d open matches on CDSL accounts and code-21 freezes checked; %d problems.", s.checked, s.found)
	return s, nil
}

func siPROI(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Clients resident outside India", e.date)
	found := false
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if !isPROI(c) || c.Status == "Closed" || c.Status == "Rejected" {
			continue
		}
		found = true
		if c.Type == "NRI" && c.NRIAccount == "" {
			if d, _, err := docs.Get[NRIDetails](e.ctx, e.o.Docs, e.tenant, cNRI, u); err == nil {
				c.NRIAccount = d.Account
			}
		}
		var why []string
		if proiApplies(c, e.date) {
			why = append(why, proiProblems(c, e.date)...)
		}
		if c.Status == "Active" && c.Type == "NRI" && c.NRIAccount == "" {
			why = append(why, "NRE or NRO account not recorded (the NSE UCC category depends on it)")
		}
		s.checked++
		if len(why) > 0 {
			s.bad(siClient(u) + " · " + strings.Join(why, "; "))
		}
	}
	if !found {
		return siSkip("Not applicable: no client resident outside India.")
	}
	s.summary = fmt.Sprintf("%d clients resident outside India checked; %d with something to complete.", s.checked, s.found)
	return s, nil
}
