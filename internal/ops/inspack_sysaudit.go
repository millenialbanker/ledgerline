package ops

import (
	"context"
	"fmt"
)

// ---- inspection packs: the system auditor's back-office criteria ----
//
// NSE's Master Circular for Membership Compliance (NSE/COMP/73981),
// Exhibit 33 Annexure VII, lists the testing criteria the back-office
// software must pass in the system audit. As the compliance gap report of
// November 2026 (row NSE-N0015) summarises them: user rights and privilege
// validation, user and journal (JV) audit logs, no parallel client ledgers,
// a system lock of back-dated entries beyond T-2, and the client ledger,
// trial balance, register of securities, bank book and UCC master in the
// standard formats. The criteria pack maps each one to how Ledgerline
// meets it, where the auditor sees it and where it stands for the firm
// today. The criteria's exact words are to confirm against Annexure VII.

const srcAnnexVII = "NSE/COMP/73981 (Master Circular for Membership Compliance Department), Exhibit 33 Annexure VII (testing criteria for back-office software)"

func init() {
	RegisterPackSection(PackSection{ID: "bo_criteria", Name: "Back-office software criteria (NSE Annexure VII)",
		About: "Each criterion of NSE's testing criteria for back-office software, how Ledgerline meets it, where the auditor sees it, and where it stands for the firm (the period lock, the nightly self-inspection).", Build: secBOCriteria})
}

// sysAuditTemplate is the system auditor's data request for the
// back-office software.
func sysAuditTemplate() PackTemplate {
	return PackTemplate{ID: "std-system-audit", Name: "System audit: back-office software criteria (stock broker)", Kind: "system_audit",
		Authority: "The firm's system auditor (an NSE-empanelled auditor for NSE members)", SampleN: 10,
		About:   "What the system auditor checks of the back-office software against NSE's testing criteria (Annexure VII): the criteria map, the standard-format reports and the self-inspection results. Build it before the preliminary audit report.",
		Sources: []string{srcAnnexVII},
		Items: []PackItem{
			pi("Back-office criteria map: each criterion, how it is met and where it stands", "end", "bo_criteria", false, srcAnnexVII, "Check the criteria's words against Annexure VII of the current master circular"),
			pi("Client ledgers of the sampled clients, and balances at the period end", "period", "client_ledgers", true, srcAnnexVII+" (standard-format client ledger)", ""),
			pi("Register of securities", "end", "register_securities", false, srcAnnexVII+" (standard-format register of securities)", ""),
			pi("Client master (UCC master)", "end", "client_master", false, srcAnnexVII+" (standard-format UCC master)", ""),
			pi("Self-inspection results for the period (period lock, client journals)", "period", "self_inspection", false, "Ledgerline's nightly self-inspection", ""),
			withParam(pi("The previous system audit report and action taken", "current", "documents", false, "NSE system audit (NSE/INSP/73850)", ""), "System audit report,Action taken report"),
		}}
}

func secBOCriteria(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	_, to := b.Span(it)
	set, _ := b.O.Settings(ctx, b.Tenant)
	checks := b.O.siLatest(ctx, b.Tenant, to)
	check := func(id string) string {
		c, ok := checks[id]
		if !ok {
			return "Not run yet (Self-inspection → Run now)"
		}
		return fmt.Sprintf("Self-inspection %s on %s: %s", c.Status, fmtDay(c.Date), c.Summary)
	}
	lock := "Automatic lock off; locked by hand up to " + firstOf(fmtDay(set.LockedUpTo), "no date")
	if set.AutoLockDays > 0 {
		lock = fmt.Sprintf("Locked automatically %d business days behind today (up to %s on %s)", set.AutoLockDays, fmtDay(set.lockedUpTo(to)), fmtDay(to))
	}
	rows := [][]string{{"Criterion", "How Ledgerline meets it", "Where the auditor sees it", "Where it stands"},
		{"User rights and privilege validation", "Every action checks the user's role (Ops maker, Ops checker, Risk manager, Compliance officer, Admin); a maker cannot check their own entry.", "Settings → Users; Audit trail", "Built in"},
		{"User audit log", "Sign-ins, failed sign-ins and security events are recorded per user; the audit trail is sealed and cannot be edited.", "Audit trail; Security events", "Built in"},
		{"Journal (JV) audit log", "Every voucher keeps its maker and checker; a posted entry is never edited, only reversed, and both are in the audit trail.", "Accounts → Firm accounts & GST (vouchers); Audit trail", "Built in"},
		{"No parallel client ledgers", "One ledger per client in one book; no journal moves money between clients.", "Client reports → Ledger statement; self-inspection client_journals", check("client_journals")},
		{"Back-dated entries beyond T-2 locked by the system", "Settings → Period locking: 'Also lock automatically' keeps the books locked a set number of business days behind today (3 refuses everything before T-2).", "Settings → Period locking; self-inspection backdated_lock", lock + ". " + check("backdated_lock")},
		{"Client ledger in the standard format", "Ledger statement per client and ledger balances with days in debit.", "Client reports → Ledger statement; Regulatory reports → Client ledger balances", "Built in (format to compare with NSE's prescribed layout)"},
		{"Trial balance", "Trial balance on any date from the same book.", "Accounts → Firm accounts & GST → Trial balance", "Built in (format to compare with NSE's prescribed layout)"},
		{"Register of securities in the standard format", "Client-wise securities in demat, pledged to the firm and held in CUSPA.", "Regulatory reports → Register of securities", "Built in (format to compare with NSE's prescribed layout)"},
		{"Bank book in the standard format", "Bank vouchers and the bank reconciliation are kept; a bank book export in NSE's prescribed layout is not built yet.", "Accounts → Firm accounts & GST; Bank reconciliation", "Gap: no standard-format bank book export yet"},
		{"UCC master in the standard format", "Client register in the exchange format, and the UCC upload batches.", "Regulatory reports → Client register in exchange format; UCC upload batch", "Built in"},
	}
	f := b.CSV("bo_software_criteria.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records, Note: "Criteria as summarised from NSE's Annexure VII; compare with the circular's own words."}, nil
}
