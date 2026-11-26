package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"sort"
	"strings"

	"ledgerline/internal/docs"
)

// ---- inspection packs: the standard data requests ----
//
// Each standard template is a starting list. The items and their sources
// were checked against these public documents (November 2026); where an
// item could not be matched to a published list it is marked "to confirm",
// with what to check. Exchanges, depositories and SEBI send their own data
// request with each inspection: compare it with the template and add,
// remove or change items before building.
//
//   - SEBI (Stock Brokers) Regulations, 2026 (SEBI/LAD-NRO/GN/2026/291, 7 January 2026):
//     books and records preserved for eight years; SEBI may inspect jointly
//     with the exchanges, clearing corporations and depositories.
//   - Securities Contracts (Regulation) Rules, 1957, rule 15, as listed in
//     BSE's Master Circular on Inspection (updated to 31 March 2024, item 6.1):
//     register of transactions, clients' ledger, general ledger, journal,
//     cash book, bank pass book, register of securities, contract notes,
//     margin deposit book, client account opening forms.
//   - SEBI/HO/MIRSD/MIRSD2/CIR/P/2016/95 (26 September 2016, enhanced
//     supervision): naming of client bank and demat accounts, records of
//     pledge authorisations, monitoring of client funds, half-yearly internal
//     audit with sample sizes, running account settlement.
//   - SEBI Master Circular for Stock Brokers SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90 (17 June 2025).
//   - NSE, internal audit of trading members for the half year ended 30
//     September 2026 (circular ref. 47/2026; an NSE-empanelled auditor; due
//     30 November 2026; sampling in Annexures III and IIIA; penalties under
//     NSE/INSP/73792 of 17 April 2026). Read from a summary of the circular:
//     the annexures themselves are to confirm.
//   - NSDL/POLICY/2025/0139 (10 October 2025): half-yearly internal and
//     concurrent audit of depository operations; the concurrent audit's
//     areas (account opening, DIS issue and execution, POA changes, closures,
//     grievances, KYC reconfirmation, NDU, client data changes, statutory
//     freezes, pledge and margin pledge, e-DIS).
//   - CDSL/AUDIT/DP/POLCY/2026/693 (6 October 2026): revised internal and
//     concurrent audit report format for the half year ended 30 September 2026
//     (read from a summary; the checklist is to confirm).
//   - SEBI CSCRF SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2024/113 (20 August 2024);
//     VAPT timelines in NSDL/POLICY/2025/0121 (15 September 2025).
//   - SEBI AML/CFT master circular SEBI/HO/MIRSD/MIRSDSECFATF/P/CIR/2024/78 (6 June 2024).
//   - SEBI/HO/MIRSD/DoP/P/CIR/2022/44 (4 April 2022): DDPI.

const (
	srcRegs2026 = "SEBI (Stock Brokers) Regulations, 2026 (SEBI/LAD-NRO/GN/2026/291, 7 January 2026)"
	srcRule15   = "SCRR 1957 rule 15; BSE Master Circular on Inspection (to 31 March 2024), item 6.1"
	srcES2016   = "SEBI/HO/MIRSD/MIRSD2/CIR/P/2016/95 (26 September 2016)"
	srcMC2025   = "SEBI Master Circular for Stock Brokers SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90 (17 June 2025)"
	srcNSEIA    = "NSE internal audit of trading members, half year ended 30 September 2026 (ref. 47/2026)"
	srcNSDLIA   = "NSDL/POLICY/2025/0139 (10 October 2025)"
	srcCDSLIA   = "CDSL/AUDIT/DP/POLCY/2026/693 (6 October 2026)"
	srcCSCRF    = "SEBI CSCRF SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2024/113 (20 August 2024)"
	srcAML      = "SEBI AML/CFT master circular SEBI/HO/MIRSD/MIRSDSECFATF/P/CIR/2024/78 (6 June 2024)"
	srcDDPI     = "SEBI/HO/MIRSD/DoP/P/CIR/2022/44 (4 April 2022)"
	srcRAS      = "SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197; SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/1"
	srcSEGMON   = "SEBI/HO/MRD2_DCAP/CIR/2021/0598 (20 July 2021); NSE/INSP/57639"
	srcHolding  = "NSE/INSP/55039, NSE/INSP/55380; BSE notice 20230113-13"
	srcVAPT     = "NSDL/POLICY/2025/0121 (15 September 2025)"
	srcJoint    = "SEBI: one joint inspection of each stock broker a year by the stock exchanges, clearing corporations and depositories with a lead institution, from 1 December 2025 (yearly for higher-risk brokers, every two years for professional clearing members, at least once in three years for others); SEBI press release 44/2026 (7 August 2026): joint inspection of stock brokers and DPs by the exchanges and depositories"
)

func pi(title, period, section string, sampled bool, basis, confirm string) PackItem {
	return PackItem{Title: title, Period: period, Section: section, Sampled: sampled, Basis: basis, ToConfirm: confirm}
}

func withParam(it PackItem, p string) PackItem { it.Param = p; return it }
func withNote(it PackItem, n string) PackItem  { it.Note = n; return it }

const confirmSEBIList = "SEBI's data request comes with its inspection letter and is not published: check this item against the letter"

// DefaultPackTemplates are the standard data requests.
func DefaultPackTemplates() []PackTemplate {
	number := func(t PackTemplate) PackTemplate {
		t.Standard = true
		for i := range t.Items {
			t.Items[i].ID = fmt.Sprintf("I%d", i+1)
			if t.Items[i].Period == "" {
				t.Items[i].Period = "period"
			}
		}
		return t
	}
	exchange := PackTemplate{ID: "std-exchange", Name: "Exchange inspection of the stock broker (NSE, BSE, MCX)", Kind: "exchange", Authority: "NSE, BSE or MCX: inspection of the trading member", SampleN: 25,
		About:   "The records an exchange inspection usually asks for: the books a member keeps under rule 15 of the SCRR, client funds and securities, margins and the regulatory submissions. The exchange's letter names the period and often the clients to sample.",
		Sources: []string{srcRule15, srcES2016, srcMC2025, srcRegs2026},
		Items: []PackItem{
			pi("Client master (client register)", "end", "client_master", false, srcRule15+" (client account opening forms)", ""),
			pi("KYC and account opening documents of the sampled clients", "end", "kyc_sample", true, srcRule15+" (client account opening forms)", ""),
			pi("UCC uploads and segment status changes", "period", "ucc_uploads", false, "The exchanges' UCC circulars", "Check the UCC records the exchange's letter asks for"),
			pi("Register of transactions (trade register)", "period", "trades", false, srcRule15+" (register of transactions)", ""),
			pi("Contract notes issued (copies for the sampled clients)", "period", "contract_notes", true, srcRule15+" (counterfoils or duplicates of contract notes)", ""),
			pi("Dispatch register: contract notes and statements sent", "period", "dispatch", false, "Proof of sending contract notes and statements", "Check whether the exchange asks for proof of delivery"),
			pi("Client ledgers of the sampled clients, and balances at the period end", "period", "client_ledgers", true, srcRule15+" (clients' ledger)", ""),
			pi("Margin collection and short-collection report", "period", "margins", false, srcRule15+" (margin deposit book); BSE Master Circular on Inspection item 3.5 (daily margin statement)", "Confirm the clearing corporation's current margin reporting circular for the period"),
			pi("Running account settlement register", "period", "ras_register", false, srcES2016+" (running account settlement); "+srcRAS, ""),
			pi("Investor complaints register", "period", "complaints", false, srcES2016+" (investor grievance reports)", "Check the register's columns against the exchange's letter"),
			pi("Pledges, DDPI and BSDA", "period", "pledges", false, srcES2016+" (records of pledge authorisations); "+srcDDPI, ""),
			pi("Payout register", "period", "payouts", false, srcES2016+" (transfers from client accounts)", "Check the payout details the exchange asks for"),
			pi("Receipts register", "period", "receipts", false, srcRule15+" (cash book, bank pass book)", ""),
			pi("Client bank account segregation check, day by day", "period", "segregation", false, srcES2016+" (naming and monitoring of client accounts)", ""),
			pi("Holding statements submitted", "period", "holdings", false, srcHolding, ""),
			pi("SEGMON submissions (client funds and collateral)", "period", "segmon", false, srcSEGMON, ""),
			pi("Register of securities", "end", "register_securities", false, srcRule15+" (register of securities)", ""),
			pi("Client code modifications", "period", "ccm", false, "The exchanges' client code modification rules", "Check whether the letter asks for the modification log"),
			pi("Terminals and users (dealers and NISM certificates)", "end", "dealers", false, "The exchanges' rules on terminals and approved users", "Check whether the letter asks for the terminal list"),
			pi("PMLA alerts and STR handling (counts only)", "period", "pmla_counts", false, srcAML, ""),
			withParam(pi("Cyber security: CSCRF, VAPT and cyber audit reports", "current", "documents", false, srcCSCRF+"; "+srcVAPT, ""), "CSCRF,VAPT report,Cyber audit report,System audit report,Action taken report"),
			withParam(pi("Policies (risk, PMLA, surveillance, outsourcing) and the board's approval", "current", "documents", false, "The yearly board review of policies", "Check which policies the letter asks for"), "Policy,Board minutes"),
			withParam(pi("Net worth certificate and audited accounts", "current", "documents", false, srcES2016+" (monitoring of financial strength)", ""), "Net worth certificate,Audited accounts"),
			pi("Compliance calendar: duties done in the period, with evidence", "period", "compliance_calendar", false, "Ledgerline's compliance calendar", ""),
			pi("Circulars and the actions taken, with evidence", "period", "circular_tasks", false, "Ledgerline's circular register", ""),
			pi("Self-inspection results for the period", "period", "self_inspection", false, "Ledgerline's nightly self-inspection", ""),
			withNote(pi("Order logs and voice recordings of client orders", "period", "not_held", false, "Evidence of client orders", "Check whether the letter asks for order evidence"), "Kept by the trading platform and the voice logger, not by Ledgerline: export them from those systems."),
		}}
	depository := PackTemplate{ID: "std-depository", Name: "Depository inspection of the DP (CDSL or NSDL)", Kind: "depository", Authority: "CDSL or NSDL: inspection of the depository participant", SampleN: 25,
		About:   "The DP's records a depository inspection or audit looks at: account opening, delivery instruction slips, pledges, DDPI and POA, closures and freezes, statements and grievances. The areas follow the depositories' internal and concurrent audit circulars.",
		Sources: []string{srcNSDLIA, srcCDSLIA, srcDDPI, srcCSCRF},
		Items: []PackItem{
			pi("BO accounts (the DP's client master)", "end", "dp_accounts", false, srcNSDLIA+" (account opening)", ""),
			pi("Account opening and KYC of the sampled BOs", "end", "kyc_sample", true, srcNSDLIA+" (account opening, KYC reconfirmation)", ""),
			pi("DIS register: books issued, slips received, verified and executed", "period", "dis_register", false, srcNSDLIA+" (DIS book issuance and DIS execution)", ""),
			pi("Transaction statements of the sampled BOs", "period", "dp_transactions", true, srcNSDLIA, "Check the sample the depository's letter asks for"),
			pi("Pledges, margin pledges, DDPI and BSDA", "period", "pledges", false, srcNSDLIA+" (pledge and hypothecation; margin pledge and re-pledge; POA modifications); "+srcDDPI, ""),
			pi("Closures, freezes, transmission and other account requests", "period", "dp_requests", false, srcNSDLIA+" (account closures; statutory freezes; client data modifications)", ""),
			pi("Statements sent to BOs (dispatch register)", "period", "dispatch", false, "Depository rules on statements to beneficial owners", "Check whether the letter asks for proof of delivery"),
			withParam(pi("Investor grievances about the DP", "period", "complaints", false, srcNSDLIA+" (investor grievances)", ""), "dp"),
			pi("DP bills (charges to BOs)", "period", "dp_bills", false, "The DP's tariff", "Check whether the letter asks for billing records"),
			pi("PMLA alerts and STR handling (counts only)", "period", "pmla_counts", false, srcAML, ""),
			withParam(pi("Internal and concurrent audit reports, with action taken", "current", "documents", false, srcNSDLIA+"; "+srcCDSLIA, ""), "Internal audit report,Concurrent audit report,Action taken report"),
			withParam(pi("Cyber security: CSCRF and VAPT reports", "current", "documents", false, srcCSCRF+"; "+srcVAPT, ""), "CSCRF,VAPT report,Cyber audit report,Action taken report"),
			pi("Compliance calendar: duties done in the period, with evidence", "period", "compliance_calendar", false, "Ledgerline's compliance calendar", ""),
			pi("Circulars and the actions taken, with evidence", "period", "circular_tasks", false, "Ledgerline's circular register", ""),
			pi("Self-inspection results for the period", "period", "self_inspection", false, "Ledgerline's nightly self-inspection", ""),
			withNote(pi("Non-disposal undertakings and investor accreditation", "period", "not_held", false, srcNSDLIA+" (NDU; investor accreditation)", ""), "Ledgerline does not record NDUs or investor accreditation: keep them from the depository's system."),
		}}
	sebi := PackTemplate{ID: "std-sebi", Name: "SEBI inspection (stock broker and DP)", Kind: "sebi", Authority: "SEBI, alone or jointly with the exchanges, clearing corporations and depositories", SampleN: 30,
		About:   "SEBI may inspect a stock broker's books, records and systems, alone or jointly with the exchanges, clearing corporations and depositories, and the records are kept for eight years. SEBI's data request comes with its letter and is not published, so every item here is to confirm against the letter.",
		Sources: []string{srcRegs2026, srcMC2025, srcES2016, srcAML, srcCSCRF},
		Items: []PackItem{
			pi("Client master (client register)", "end", "client_master", false, srcRegs2026, confirmSEBIList),
			pi("KYC and account opening documents of the sampled clients", "end", "kyc_sample", true, srcRegs2026, confirmSEBIList),
			pi("Register of transactions (trade register)", "period", "trades", false, srcRule15, confirmSEBIList),
			pi("Contract notes issued (copies for the sampled clients)", "period", "contract_notes", true, srcRule15, confirmSEBIList),
			pi("Client ledgers of the sampled clients, and balances at the period end", "period", "client_ledgers", true, srcRule15, confirmSEBIList),
			pi("Margin collection and short-collection report", "period", "margins", false, srcMC2025, confirmSEBIList),
			pi("Running account settlement register", "period", "ras_register", false, srcRAS, confirmSEBIList),
			pi("Client bank account segregation check, day by day", "period", "segregation", false, srcES2016, confirmSEBIList),
			pi("SEGMON submissions (client funds and collateral)", "period", "segmon", false, srcSEGMON, confirmSEBIList),
			pi("Holding statements submitted", "period", "holdings", false, srcHolding, confirmSEBIList),
			pi("Pledges, DDPI and BSDA", "period", "pledges", false, srcDDPI, confirmSEBIList),
			pi("Payout register", "period", "payouts", false, srcES2016, confirmSEBIList),
			pi("Receipts register", "period", "receipts", false, srcRule15, confirmSEBIList),
			pi("Investor complaints register", "period", "complaints", false, srcMC2025, confirmSEBIList),
			pi("PMLA alerts and STR handling (counts only)", "period", "pmla_counts", false, srcAML, ""),
			withParam(pi("Cyber security: CSCRF, VAPT and cyber audit reports", "current", "documents", false, srcCSCRF, ""), "CSCRF,VAPT report,Cyber audit report,System audit report,Action taken report"),
			withParam(pi("Policies and the board's approval", "current", "documents", false, srcMC2025, confirmSEBIList), "Policy,Board minutes"),
			withParam(pi("Internal audit reports and action taken", "current", "documents", false, srcES2016+" (internal audit)", ""), "Internal audit report,Action taken report"),
			withParam(pi("Net worth certificate and audited accounts", "current", "documents", false, srcES2016+" (monitoring of financial strength)", ""), "Net worth certificate,Audited accounts"),
			pi("Compliance calendar: duties done in the period, with evidence", "period", "compliance_calendar", false, "Ledgerline's compliance calendar", ""),
			pi("Circulars and the actions taken, with evidence", "period", "circular_tasks", false, "Ledgerline's circular register", ""),
			pi("Self-inspection results for the period", "period", "self_inspection", false, "Ledgerline's nightly self-inspection", ""),
			withNote(pi("Order logs and voice recordings of client orders", "period", "not_held", false, "Evidence of client orders", confirmSEBIList), "Kept by the trading platform and the voice logger, not by Ledgerline: export them from those systems."),
		}}
	audit := PackTemplate{ID: "std-internal-audit", Name: "Half-yearly internal audit (stock broker)", Kind: "internal_audit", Authority: "The firm's internal auditor (an NSE-empanelled CA, CS or CMA firm for NSE members)", SampleN: 25,
		About:   "What the internal auditor checks each half year (April to September, October to March), with the sample of clients recorded. The report is due by 30 November and 31 May. The exchange's sampling rules (NSE's Annexures III and IIIA) set how many instances to check per area: the sample here is a starting point.",
		Sources: []string{srcES2016, srcNSEIA, srcMC2025, srcAML, srcCSCRF},
		Items: []PackItem{
			pi("KYC and account opening of the sampled clients", "end", "kyc_sample", true, srcES2016+" (internal audit)", "Match the sample size to NSE's Annexure III for the half year"),
			pi("Client master (client register)", "end", "client_master", false, srcES2016, ""),
			pi("UCC uploads and segment status changes", "period", "ucc_uploads", false, srcNSEIA, "Check the half year's checklist"),
			pi("Contract notes of the sampled clients", "period", "contract_notes", true, srcNSEIA, "Match the sample size to NSE's Annexure III for the half year"),
			pi("Dispatch register: contract notes and statements sent", "period", "dispatch", false, srcNSEIA, "Check the half year's checklist"),
			pi("Client ledgers of the sampled clients, and balances at the period end", "period", "client_ledgers", true, srcES2016, ""),
			pi("Margin collection, short collection and penalties", "period", "margins", false, srcNSEIA, "Check the half year's checklist"),
			pi("Running account settlement register", "period", "ras_register", false, srcES2016+" (running account settlement)", ""),
			pi("Client bank account segregation check, day by day", "period", "segregation", false, srcES2016+" (monitoring of client funds)", ""),
			pi("SEGMON submissions (client funds and collateral)", "period", "segmon", false, srcSEGMON, ""),
			pi("Holding statements submitted", "period", "holdings", false, srcHolding, ""),
			pi("Pledges, DDPI and BSDA", "period", "pledges", false, srcES2016+" (records of pledge authorisations); "+srcDDPI, ""),
			pi("Payout register", "period", "payouts", false, srcES2016, "Check the half year's checklist"),
			pi("Receipts register", "period", "receipts", false, srcES2016, "Check the half year's checklist"),
			pi("Investor complaints register", "period", "complaints", false, srcES2016, ""),
			pi("Nomination status", "end", "nomination", false, srcNSEIA, "Check the half year's checklist"),
			pi("Client code modifications", "period", "ccm", false, srcNSEIA, "Check the half year's checklist"),
			pi("Terminals and users (dealers and NISM certificates)", "end", "dealers", false, srcNSEIA, "Check the half year's checklist"),
			pi("PMLA alerts and STR handling (counts only)", "period", "pmla_counts", false, srcAML, ""),
			withParam(pi("The previous internal audit report and action taken", "current", "documents", false, srcNSEIA+" (action taken on non-compliances)", ""), "Internal audit report,Action taken report"),
			withParam(pi("Cyber security: CSCRF and VAPT reports", "current", "documents", false, srcCSCRF, ""), "CSCRF,VAPT report,Cyber audit report,Action taken report"),
			pi("Compliance calendar: duties done in the period, with evidence", "period", "compliance_calendar", false, "Ledgerline's compliance calendar", ""),
			pi("Circulars and the actions taken, with evidence", "period", "circular_tasks", false, "Ledgerline's circular register", ""),
			pi("Self-inspection results for the period", "period", "self_inspection", false, "Ledgerline's nightly self-inspection", ""),
		}}
	// The joint inspection: the exchange list and the depository list
	// together, each item once (by section, option and the period it
	// covers), the broker's items first.
	joint := PackTemplate{ID: "std-joint", Name: "Joint inspection by the exchanges and depositories (stock broker and DP)", Kind: "joint",
		Authority: "The stock exchanges, clearing corporations and depositories together, led by one of them", SampleN: exchange.SampleN,
		About:   "One inspection of the stock broker and its DP together. The items are the exchange list and the depository list with each record asked for once. The lead institution's letter decides the final list: compare it before building.",
		Sources: append(append([]string{srcJoint}, exchange.Sources...), depository.Sources...)}
	seen := map[string]bool{}
	for _, it := range append(append([]PackItem{}, exchange.Items...), depository.Items...) {
		k := it.Section + "|" + it.Param + "|" + it.Period
		if it.Section == "not_held" {
			k += "|" + it.Title
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		if it.ToConfirm == "" {
			it.ToConfirm = "Check this item against the lead institution's inspection letter"
		}
		joint.Items = append(joint.Items, it)
	}
	return []PackTemplate{number(exchange), number(depository), number(sebi), number(audit), number(joint), number(sysAuditTemplate())}
}

// ---- sections ----

// inSpan reports whether a date (or a time starting with one) is from
// from to to; an empty from has no lower bound.
func inSpan(d, from, to string) bool {
	if len(d) >= 10 {
		d = d[:10]
	}
	if d == "" {
		return false
	}
	return (from == "" || d >= from) && d <= to
}

// rowsOfCSV reads CSV bytes (a leading byte order mark is dropped).
func rowsOfCSV(b []byte) [][]string {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord = -1
	rows, _ := r.ReadAll()
	return rows
}

// daily joins a regulatory report over the business days of a span, with
// a Date column first when the report has none.
func daily(ctx context.Context, b *PackBuild, from, to, code string) (rows [][]string, days, skipped int, err error) {
	for _, d := range b.Days(from, to) {
		rs, err := b.O.ReportRows(ctx, b.Tenant, d, code)
		if err != nil {
			skipped++
			continue
		}
		if len(rs) == 0 {
			continue
		}
		addDate := rs[0][0] != "Date" && rs[0][0] != "TradeDate"
		if rows == nil {
			h := rs[0]
			if addDate {
				h = append([]string{"Date"}, h...)
			}
			rows = [][]string{h}
		}
		if len(rs) > 1 {
			days++
		}
		for _, r := range rs[1:] {
			if addDate {
				r = append([]string{d}, r...)
			}
			rows = append(rows, r)
		}
	}
	return rows, days, skipped, nil
}

func dayNote(days, skipped int) string {
	s := fmt.Sprintf("%d business day(s) with records", days)
	if skipped > 0 {
		s += fmt.Sprintf("; %d day(s) without the day's data in Ledgerline (no market or trade files loaded) are left out", skipped)
	}
	return s
}

// packSelfInspection is the nightly self-inspection's summary for a period
// (F2, built alongside this). LEAD: wire it at merge, in F2's file:
//
//	func init() {
//		packSelfInspection = func(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
//			from, to := b.Span(it)
//			r, err := b.O.SelfInspectionSummary(ctx, b.Tenant, from, to)
//			if err != nil {
//				return PackPart{}, err
//			}
//			rows := [][]string{{ /* the report's columns */ }}
//			// ... one row per test and night ...
//			return PackPart{Files: []PackFile{b.CSV("self_inspection.csv", rows)}, Records: len(rows) - 1}, nil
//		}
//	}
//
// Until then the item shows as not available.
var packSelfInspection func(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error)

func init() {
	for _, s := range []PackSection{
		{ID: "client_master", Name: "Regulatory reports → Client register in exchange format", About: "Every client with category, PAN, contact, bank (last four digits, as the report shows), demat, segments, KYC date, nomination, status, branch and AP.", Build: secClientMaster},
		{ID: "kyc_sample", Name: "KYC & PMLA → KYC registry and client documents", About: "For the sampled clients: KRA, CKYC and periodic KYC status, the list of documents on file, and copies of the documents.", Build: secKYCSample},
		{ID: "ucc_uploads", Name: "Regulatory reports → UCC upload batch and segment status", About: "Each business day's UCC upload rows, and each client's status in each segment.", Build: secUCC},
		{ID: "trades", Name: "Regulatory reports → Trade register", About: "Every exchange trade in the period, day by day.", Build: secTrades},
		{ID: "contract_notes", Name: "Contract notes", About: "The register of contract notes issued in the period, and the notes of the sampled clients as PDFs.", Build: secNotes},
		{ID: "dispatch", Name: "Client documents → Dispatch register", About: "Every document sent to clients in the period, with channel, status and file checksum.", Build: secDispatch},
		{ID: "client_ledgers", Name: "Client reports → Ledger statement; Regulatory reports → Client ledger balances", About: "Ledger statements of the sampled clients (CSV and PDF) and every client's balance at the period end with days in debit.", Build: secLedgers},
		{ID: "margins", Name: "Regulatory reports → Client-wise margin collection", About: "Peak and end-of-day margin required, collected, short collection and penalty, client by client, each business day.", Build: secMargins},
		{ID: "ras_register", Name: "Running account → settlement batches", About: "Each running account settlement in the period, client by client, and the clients without a running account authorisation.", Build: secRAS},
		{ID: "complaints", Name: "Investor complaints → register", About: "Complaints open at any time in the period, with source, SCORES or exchange reference, category, due and resolution dates. With the option dp: those about the demat account only.", Build: secComplaints},
		{ID: "pledges", Name: "Collateral & pledges; Depository → DDPI and BSDA", About: "Margin pledge requests in the period, the DDPI and POA register, and the BSDA reviews and accounts.", Build: secPledges},
		{ID: "payouts", Name: "Receipts & payouts → payout requests and batches", About: "Payout requests in the period with maker, checker, batch and UTR, and the payout batches.", Build: secPayouts},
		{ID: "receipts", Name: "Receipts & payouts → receipts", About: "Receipts in the period with mode, reference, account paid from, maker and checker.", Build: secReceipts},
		{ID: "segregation", Name: "Bank reconciliation → client money segregation check", About: "Each business day: client bank balance, money with the clearing corporation, suspense, client credits and debits, cover and whether client money was covered. Firm totals only.", Build: secSegregation},
		{ID: "holdings", Name: "Exchange files → holding statement submissions", About: "The holding statements submitted to NSE (API) and BSE (BEFS) in the period, and the current holding statement.", Build: secHoldings},
		{ID: "segmon", Name: "Regulatory reports and Exchange files → SEGMON", About: "The client funds and collateral reports in the period with their status and acknowledgement, and NSE Clearing's responses.", Build: secSEGMON},
		{ID: "register_securities", Name: "Regulatory reports → Register of securities", About: "Client-wise securities in demat, pledged to the firm and held in CUSPA.", Build: secRegisterSecurities},
		{ID: "ccm", Name: "Regulatory reports → Client code modification log", About: "Every client code modification in the period with reason, by whom and when.", Build: secCCM},
		{ID: "dealers", Name: "Dealers & terminals → Terminals and users", About: "Every terminal and segment with location, dealer, NISM certificate and status.", Build: secDealers},
		{ID: "nomination", Name: "Regulatory reports → Nomination status", About: "Each client's nomination or opt-out declaration.", Build: secNomination},
		{ID: "pmla_counts", Name: "Surveillance & AML; KYC & PMLA → counts", About: "Counts only: surveillance and AML alerts in the period by source, type and status (STRs filed counted), and clients by risk category. No names, alert details or STR contents.", Build: secPMLA},
		{ID: "documents", Name: "Inspection packs → Documents", About: "Copies of the policy, audit and evidence documents uploaded here, of the kinds the option names (comma-separated).", Build: secDocuments, Ready: readyDocuments},
		{ID: "compliance_calendar", Name: "Circulars & duties → compliance calendar", About: "Each duty done in the period with its due date, when and by whom, late or not, and the evidence recorded; and the duties overdue at the period end.", Build: secCalendar},
		{ID: "circular_tasks", Name: "Circulars & duties → circulars and their actions", About: "Circulars dated in the period or with actions done in it: each action with owner, due date, status and evidence.", Build: secCirculars},
		{ID: "self_inspection", Name: "Self-inspection", About: "The nightly self-inspection's results for the period.", Build: secSelfInspection, Ready: readySelfInspection},
		{ID: "dp_accounts", Name: "Depository → BO accounts", About: "Every BO account at the firm's DP with status, freeze, BSDA, DDPI and value.", Build: secDPAccounts, Ready: readyDP},
		{ID: "dis_register", Name: "Depository → DIS books and slips", About: "DIS books issued and slips received, verified and executed in the period.", Build: secDIS, Ready: readyDP},
		{ID: "dp_transactions", Name: "Depository → transactions", About: "The sampled clients' demat transactions in the period.", Build: secDPTxns, Ready: readyDP},
		{ID: "dp_requests", Name: "Depository → account requests", About: "Demat, freeze, unfreeze, transmission and closure requests made in the period.", Build: secDPRequests, Ready: readyDP},
		{ID: "dp_bills", Name: "Depository → DP bills", About: "DP bills for the months in the period.", Build: secDPBills, Ready: readyDP},
		{ID: "not_held", Name: "Not held in Ledgerline", About: "A record the inspection asks for that Ledgerline does not keep; the item's note says where it is.", Build: secNotHeld, Ready: readyNotHeld},
	} {
		RegisterPackSection(s)
	}
}

func secClientMaster(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	_, to := b.Span(it)
	rows, err := b.O.ReportRows(ctx, b.Tenant, to, "client_register")
	if err != nil {
		return PackPart{}, err
	}
	f := b.CSV("client_register.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records, Note: "The client's details as Ledgerline holds them now; bank account numbers show the last four digits, as the client register does."}, nil
}

func secKYCSample(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	if len(b.Clients) == 0 {
		return PackPart{NotAvailable: "No clients were sampled for this pack."}, nil
	}
	_, to := b.Span(it)
	reg, err := b.O.ReportRows(ctx, b.Tenant, to, "kyc_registry")
	if err != nil {
		return PackPart{}, err
	}
	status := [][]string{reg[0]}
	for _, r := range reg[1:] {
		if b.Sampled(r[0]) {
			status = append(status, r)
		}
	}
	cm, err := b.O.ReportRows(ctx, b.Tenant, to, "client_register")
	if err != nil {
		return PackPart{}, err
	}
	master := [][]string{cm[0]}
	for _, r := range cm[1:] {
		if b.Sampled(r[0]) {
			master = append(master, r)
		}
	}
	list := [][]string{{"UCC", "Document", "Kind", "File name", "Type", "Bytes", "Uploaded", "By", "SHA-256", "In this pack"}}
	files := []PackFile{b.CSV("kyc_status.csv", status), b.CSV("client_details.csv", master)}
	copies := 0
	for _, u := range b.Clients {
		ds, err := b.O.ClientDocs(ctx, b.Tenant, u)
		if err != nil {
			return PackPart{}, err
		}
		for _, d := range ds {
			in := "Yes"
			if b.Mask {
				in = "No (masked copy)"
			} else if rf, err := b.O.App.Ingest.Raw.Get(ctx, b.Tenant, d.FileID); err == nil {
				ext := docTypes[d.ContentType]
				files = append(files, PackFile{Name: "documents/" + u + "/" + d.ID + " " + d.Kind + ext, Data: rf.Content, Records: 1})
				copies++
			} else {
				in = "No: " + err.Error()
			}
			list = append(list, []string{u, d.ID, d.Kind, d.Name, d.ContentType, fmt.Sprint(d.Size), d.At, d.By, d.SHA256, in})
		}
	}
	files = append(files, b.CSV("documents_on_file.csv", list))
	note := fmt.Sprintf("%d sampled clients, %d documents on file", len(b.Clients), len(list)-1)
	if b.Mask {
		note += "; document images are left out of a masked copy"
	} else {
		note += fmt.Sprintf(", %d copied as uploaded", copies)
	}
	return PackPart{Files: files, Records: len(b.Clients), Note: note}, nil
}

func secUCC(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	rows, days, skipped, err := daily(ctx, b, from, to, "ucc")
	if err != nil {
		return PackPart{}, err
	}
	if rows == nil {
		rows = [][]string{{"Date", "UCC", "Name", "PAN", "Category", "Segments", "Mobile", "Email", "Status"}}
	}
	// The day's batch lists every client still waiting for its upload: a
	// client waiting through the period is listed once, on the first day.
	seen, dates := map[string]bool{}, map[string]bool{}
	kept := rows[:1]
	for _, r := range rows[1:] {
		k := strings.Join(r[1:], "\x00")
		if !seen[k] {
			seen[k] = true
			dates[r[0]] = true
			kept = append(kept, r)
		}
	}
	rows, days = kept, len(dates)
	seg, err := b.O.ReportRows(ctx, b.Tenant, to, "segment_status")
	if err != nil {
		return PackPart{}, err
	}
	up := b.CSV("ucc_uploads.csv", rows)
	return PackPart{Files: []PackFile{up, b.CSV("segment_status.csv", seg)}, Records: up.Records, Note: dayNote(days, skipped) + "; segment status is as Ledgerline holds it now"}, nil
}

func secTrades(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	rows, days, skipped, err := daily(ctx, b, from, to, "trades")
	if err != nil {
		return PackPart{}, err
	}
	if rows == nil {
		rows = [][]string{{"Date", "Exchange", "Segment", "TradeNo", "OrderNo", "Time", "UCC", "Contract", "Side", "Qty", "Price", "Product", "AlgoID", "File", "Line"}}
	}
	f := b.CSV("trade_register.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records, Note: dayNote(days, skipped)}, nil
}

func secNotes(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	all, err := docs.List[ContractNote](ctx, b.O.Docs, b.Tenant, cNote, "")
	if err != nil {
		return PackPart{}, err
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Date != all[j].Date {
			return all[i].Date < all[j].Date
		}
		return all[i].UCC < all[j].UCC
	})
	rows := [][]string{{"Date", "Note no.", "UCC", "Name", "Part", "Trades", "Net", "Status", "Sent at", "Attempts", "Revision", "Replaces", "Reason", "Dispatch"}}
	var files []PackFile
	pdfs := 0
	for _, n := range all {
		if !inSpan(n.Date, from, to) {
			continue
		}
		rows = append(rows, []string{n.Date, n.No, n.UCC, n.Name, n.part(), fmt.Sprint(n.Trades), n.Net.Fixed(2), n.Status, n.SentAt, fmt.Sprint(n.Attempts), fmt.Sprint(n.Revision), n.Replaces, n.Reason, n.Dispatch})
		if !it.Sampled || !b.Sampled(n.UCC) {
			continue
		}
		r, _, err := b.O.notePDF(ctx, b.Tenant, n.Date, n.UCC, n.Part)
		if err != nil {
			continue
		}
		f, err := b.PDF("notes/"+n.UCC+"/"+n.Date+" "+packFileName(n.No)+".pdf", r, 1)
		if err != nil {
			return PackPart{}, err
		}
		files = append(files, f)
		pdfs++
	}
	reg := b.CSV("contract_note_register.csv", rows)
	note := fmt.Sprintf("%d notes in the register", reg.Records)
	if it.Sampled {
		note += fmt.Sprintf("; %d notes of the sampled clients as PDF, as issued", pdfs)
	}
	return PackPart{Files: append([]PackFile{reg}, files...), Records: reg.Records, Note: note}, nil
}

func secDispatch(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	data, err := b.O.DispatchCSV(ctx, b.Tenant, b.Actor, DispatchFilter{From: from, To: to})
	if err != nil {
		return PackPart{}, err
	}
	f := b.CSV("dispatch_register.csv", rowsOfCSV(data))
	p := PackPart{Files: []PackFile{f}, Records: f.Records}
	if f.Records >= 2000 {
		p.Note = "The dispatch register lists at most 2,000 documents: make packs for shorter periods to have them all."
	}
	return p, nil
}

func secLedgers(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	bal, err := b.O.ReportRows(ctx, b.Tenant, to, "ledger_balances")
	if err != nil {
		return PackPart{}, err
	}
	files := []PackFile{b.CSV("ledger_balances_"+to+".csv", bal)}
	records := len(bal) - 1
	n := 0
	if it.Sampled {
		for _, u := range b.Clients {
			r, err := b.O.ClientReportFor(ctx, b.Tenant, "ledger", u, from, to)
			if err != nil {
				continue
			}
			lines := 0
			for _, s := range r.Sections {
				lines += len(s.Rows)
			}
			files = append(files, b.CSVn("statements/"+r.FileName("csv"), rowsOfCSV(r.CSV()), lines))
			f, err := b.PDF("statements/"+r.FileName("pdf"), reportPDF(r, b.Set), lines)
			if err != nil {
				return PackPart{}, err
			}
			files = append(files, f)
			n++
		}
	}
	note := fmt.Sprintf("Balances of %d clients at %s", records, fmtDay(to))
	if it.Sampled {
		note += fmt.Sprintf("; ledger statements of %d sampled clients (CSV and PDF)", n)
	}
	return PackPart{Files: files, Records: records, Note: note}, nil
}

func secMargins(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	rows, days, skipped, err := daily(ctx, b, from, to, "margin")
	if err != nil {
		return PackPart{}, err
	}
	if rows == nil {
		rows = [][]string{{"TradeDate", "UCC", "PAN", "PeakMarginRequired", "PeakAt", "EODMarginRequired", "MarginCollected", "ShortCollection", "Penalty"}}
	}
	f := b.CSV("margin_collection.csv", rows)
	short := 0
	for _, r := range rows[1:] {
		if len(r) > 7 && strings.Trim(r[7], "0.") != "" {
			short++
		}
	}
	return PackPart{Files: []PackFile{f}, Records: f.Records, Note: dayNote(days, skipped) + fmt.Sprintf("; %d client-days with short collection", short)}, nil
}

func secRAS(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	bs, err := b.O.RASBatches(ctx, b.Tenant, "")
	if err != nil {
		return PackPart{}, err
	}
	rows := [][]string{{"SettlementDate", "AsOf", "UCC", "Name", "CreditBalance", "Retained", "Paid", "Bank", "IFSC", "Account", "By", "At"}}
	batches := 0
	for _, x := range bs {
		if !inSpan(x.Date, from, to) {
			continue
		}
		batches++
		for _, i := range x.Items {
			rows = append(rows, []string{x.Date, x.AsOf, i.UCC, i.Name, i.Credit.Fixed(2), i.Retained.Fixed(2), amt(i.Amount), i.Bank, i.IFSC, i.Account, x.By, x.At})
		}
		for _, h := range x.Held {
			rows = append(rows, []string{x.Date, x.AsOf, h, "", "", "", "Held: bank account incomplete", "", "", "", x.By, x.At})
		}
	}
	noAuth, err := b.O.ReportRows(ctx, b.Tenant, to, "no_ras_auth")
	if err != nil {
		return PackPart{}, err
	}
	f := b.CSV("ras_settlements.csv", rows)
	return PackPart{Files: []PackFile{f, b.CSV("clients_without_ras_authorisation.csv", noAuth)}, Records: f.Records, Note: fmt.Sprintf("%d settlement run(s) in the period", batches)}, nil
}

func secComplaints(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	ts, err := b.O.Tickets(ctx, b.Tenant, to)
	if err != nil {
		return PackPart{}, err
	}
	rows := [][]string{{"Ticket", "Kind", "Opened", "Source", "Reference", "UCC", "Name", "Category", "Subject", "Status", "Due", "Resolved", "AgeDays", "Overdue", "Assignee"}}
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
	for _, t := range ts {
		if t.Kind != KindComplaint || (it.Param == "dp" && t.Category != "Demat / DP") {
			continue
		}
		// Open at any time in the period.
		if t.OpenedOn > to || (t.ResolvedOn != "" && from != "" && t.ResolvedOn < from) {
			continue
		}
		rows = append(rows, []string{t.ID, t.Kind, t.OpenedOn, t.Source, t.Ref, t.UCC, t.Name, t.Category, t.Subject, t.Status, t.Due, t.ResolvedOn, fmt.Sprint(t.AgeDays), yn(t.Overdue), t.Assignee})
	}
	f := b.CSV("complaints_register.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records, Note: "The register; the correspondence stays on Investor complaints"}, nil
}

func secPledges(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	ps, err := b.O.PledgeRequests(ctx, b.Tenant)
	if err != nil {
		return PackPart{}, err
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID })
	pr := [][]string{{"Request", "UCC", "ISIN", "Symbol", "Qty", "BOID", "Time", "Status", "PSN", "Kind", "Via", "Requested by", "Decided by", "Reason", "Expiry"}}
	for _, p := range ps {
		if !inSpan(firstOf(p.Time, p.SentAt), from, to) {
			continue
		}
		pr = append(pr, []string{p.ID, p.UCC, p.ISIN, p.Symbol, fmt.Sprint(p.Qty), p.BOID, p.Time, p.Status, p.PSN, p.Kind, p.Via, p.RequestedBy, p.DecidedBy, p.Reason, p.Expiry})
	}
	files := []PackFile{b.CSV("pledge_requests.csv", pr)}
	records := len(pr) - 1
	dp, err := b.O.DP(ctx, b.Tenant, to)
	if err != nil {
		return PackPart{}, err
	}
	dd := [][]string{{"BOID", "UCC", "Name", "Status", "DDPI", "DDPI from", "POA type", "Opened"}}
	bs := [][]string{{"BOID", "UCC", "Name", "BSDA", "Opted out", "Other demat", "Eligible", "Value"}}
	for _, r := range dp.Rows {
		var poa, by string
		if r.CDSL != nil {
			poa, by = r.CDSL.POAType, r.CDSL.DDPIBy
		}
		dd = append(dd, []string{r.BOID, r.UCC, r.Name, r.Status, yn(r.DDPI), by, poa, r.Opened})
		bs = append(bs, []string{r.BOID, r.UCC, r.Name, yn(r.BSDA), yn(r.BSDAOptOut), yn(r.OtherDemat), yn(r.BSDAOK), r.Value.Fixed(2)})
	}
	note := fmt.Sprintf("%d pledge request(s) in the period", records)
	if len(dp.Rows) > 0 {
		files = append(files, b.CSV("ddpi_poa_register.csv", dd), b.CSV("bsda_accounts.csv", bs))
		records += len(dp.Rows)
		note += fmt.Sprintf("; DDPI and BSDA of %d BO accounts as Ledgerline holds them now", len(dp.Rows))
	} else {
		note += "; no BO accounts in Ledgerline, so no DDPI or BSDA register (the firm's DP accounts are not loaded, or it is not a DP)"
	}
	rv, err := b.O.BSDAReviews(ctx, b.Tenant)
	if err != nil {
		return PackPart{}, err
	}
	rr := [][]string{{"Review", "AsOf", "Status", "BOID", "UCC", "Name", "From", "To", "Reason", "Value", "Applied by", "Applied at"}}
	for _, x := range rv {
		if !inSpan(x.AsOf, from, to) {
			continue
		}
		for _, l := range x.Lines {
			rr = append(rr, []string{x.ID, x.AsOf, x.Status, l.BOID, l.UCC, l.Name, l.From, l.To, l.Reason, l.Value.Fixed(2), x.AppliedBy, x.AppliedAt})
		}
	}
	if len(rr) > 1 || len(dp.Rows) > 0 {
		files = append(files, b.CSV("bsda_reviews.csv", rr))
	}
	return PackPart{Files: files, Records: records, Note: note}, nil
}

func secPayouts(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	ps, err := b.O.PayoutRequests(ctx, b.Tenant, to)
	if err != nil {
		return PackPart{}, err
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID })
	rows := [][]string{{"Request", "Date", "UCC", "Name", "Amount", "Source", "Status", "Ledger", "Maker", "Checker", "Reason", "Batch", "UTR", "To account", "IFSC"}}
	for _, p := range ps {
		if !inSpan(p.Date, from, to) {
			continue
		}
		rows = append(rows, []string{p.ID, p.Date, p.UCC, p.Name, amt(p.Amount), p.Source, p.Status, p.Ledger, p.Maker, p.Checker, p.Reason, p.Batch, p.UTR, p.ToAccount, p.ToIFSC})
	}
	bs, err := b.O.PayoutBatches(ctx, b.Tenant)
	if err != nil {
		return PackPart{}, err
	}
	br := [][]string{{"Batch", "Date", "Payouts", "Total", "Status", "By", "Held"}}
	for _, x := range bs {
		if inSpan(x.Date, from, to) {
			br = append(br, []string{x.ID, x.Date, fmt.Sprint(len(x.Items)), amt(x.Total), x.Status, x.By, strings.Join(x.Held, " ")})
		}
	}
	f := b.CSV("payout_register.csv", rows)
	return PackPart{Files: []PackFile{f, b.CSV("payout_batches.csv", br)}, Records: f.Records}, nil
}

func secReceipts(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	rs, err := b.O.Receipts(ctx, b.Tenant)
	if err != nil {
		return PackPart{}, err
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
	rows := [][]string{{"Receipt", "Date", "UCC", "Name", "Mode", "Reference", "Drawee bank", "From account", "Amount", "Status", "Maker", "Checker", "Cleared on", "Ledger", "Paid into", "Note"}}
	for _, r := range rs {
		if !inSpan(r.Date, from, to) {
			continue
		}
		rows = append(rows, []string{r.ID, r.Date, r.UCC, r.Name, r.Mode, r.Ref, r.DraweeBank, r.FromAccount, amt(r.Amount), r.Status, r.Maker, r.Checker, r.ClearedOn, r.Ledger, r.ToAccount, r.Note})
	}
	f := b.CSV("receipts_register.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records}, nil
}

func secSegregation(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	rows := [][]string{{"Date", "ClientBank", "WithClearingCorporation", "Suspense", "ClientCredits", "ClientDebits", "Cover", "Covered"}}
	short := 0
	for _, d := range b.Days(from, to) {
		f, err := b.O.Funds(ctx, b.Tenant, d)
		if err != nil {
			return PackPart{}, err
		}
		if !f.OK {
			short++
		}
		rows = append(rows, []string{d, f.ClientBank.Fixed(2), f.WithCC.Fixed(2), f.Suspense.Fixed(2), f.ClientCredits.Fixed(2), f.ClientDebits.Fixed(2), fmt.Sprintf("%.4f", f.Cover), yn(f.OK)})
	}
	f := b.CSV("segregation_check.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records, Note: fmt.Sprintf("Ledgerline's segregation check as at each business day; client money was not covered on %d day(s). Firm totals only.", short)}, nil
}

func secHoldings(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	rows := [][]string{{"Exchange", "Data date", "Channel", "Status", "Records", "Acknowledgement", "Reason", "SHA-256", "By", "At"}}
	ns, err := docs.List[NSEHoldingSubmission](ctx, b.O.Docs, b.Tenant, cNSEHSub, "")
	if err != nil {
		return PackPart{}, err
	}
	for _, s := range ns {
		if inSpan(s.Date, from, to) {
			rows = append(rows, []string{"NSE", s.Date, "NSE holding API", s.Status, fmt.Sprint(s.Records), s.AckID, s.Reason, s.SHA256, s.By, s.At})
		}
	}
	fs, err := docs.List[BSEHoldingFiling](ctx, b.O.Docs, b.Tenant, cBSEHFiling, "")
	if err != nil {
		return PackPart{}, err
	}
	for _, x := range fs {
		if inSpan(x.Date, from, to) {
			rows = append(rows, []string{"BSE", x.Date, x.Channel, "Uploaded", fmt.Sprint(x.Records), x.Ack, "", x.SHA256, x.By, x.At})
		}
	}
	rs, err := docs.List[ReportStatus](ctx, b.O.Docs, b.Tenant, cReport, "")
	if err != nil {
		return PackPart{}, err
	}
	for _, r := range rs {
		if r.Code == "holdings" && inSpan(r.Date, from, to) {
			rows = append(rows, []string{"Register", r.Date, "Regulatory reports", r.Status, fmt.Sprint(r.Rows), r.Ack, "", r.SHA256, r.By, r.At})
		}
	}
	sort.SliceStable(rows[1:], func(i, j int) bool { return rows[1+i][1] < rows[1+j][1] })
	cur, err := b.O.ReportRows(ctx, b.Tenant, b.Today, "holdings")
	if err != nil {
		return PackPart{}, err
	}
	f := b.CSV("holding_submissions.csv", rows)
	return PackPart{Files: []PackFile{f, b.CSV("holding_statement_now.csv", cur)}, Records: f.Records,
		Note: "Submissions in the period; Ledgerline keeps the latest holdings only, so the holding statement is as at " + fmtDay(b.Today) + ", not each day of the period"}, nil
}

func secSEGMON(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	rs, err := docs.List[ReportStatus](ctx, b.O.Docs, b.Tenant, cReport, "")
	if err != nil {
		return PackPart{}, err
	}
	rows := [][]string{{"Date", "Report", "Status", "Rows", "SHA-256", "By", "At", "Acknowledgement"}}
	for _, r := range rs {
		if (r.Code == "funds_balance" || r.Code == "cc01" || r.Code == "margin") && inSpan(r.Date, from, to) {
			rows = append(rows, []string{r.Date, r.Name, r.Status, fmt.Sprint(r.Rows), r.SHA256, r.By, r.At, r.Ack})
		}
	}
	resp, err := docs.List[SegmonResponse](ctx, b.O.Docs, b.Tenant, cSegmonResp, "")
	if err != nil {
		return PackPart{}, err
	}
	rr := [][]string{{"Date", "Response file", "Lines", "Accepted", "Rejected", "Layout unconfirmed", "By", "At"}}
	for _, x := range resp {
		if inSpan(x.Date, from, to) {
			rr = append(rr, []string{x.Date, x.Name, fmt.Sprint(x.Lines), fmt.Sprint(x.Accepted), fmt.Sprint(x.Rejected), yn(x.Unconfirmed), x.By, x.At})
		}
	}
	f := b.CSV("segmon_submissions.csv", rows)
	g := b.CSV("segmon_responses.csv", rr)
	return PackPart{Files: []PackFile{f, g}, Records: f.Records + g.Records}, nil
}

func secRegisterSecurities(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	rows, err := b.O.ReportRows(ctx, b.Tenant, b.Today, "register_securities")
	if err != nil {
		return PackPart{}, err
	}
	f := b.CSV("register_of_securities.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records, Note: "Ledgerline keeps the latest holdings only: the register is as at " + fmtDay(b.Today)}, nil
}

func secCCM(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	rows, days, skipped, err := daily(ctx, b, from, to, "ccm")
	if err != nil {
		return PackPart{}, err
	}
	if rows == nil {
		rows = [][]string{{"TradeDate", "Exchange", "Segment", "TradeNo", "OriginalUCC", "ModifiedUCC", "Reason", "ModifiedBy", "Time"}}
	}
	f := b.CSV("client_code_modifications.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records, Note: dayNote(days, skipped)}, nil
}

func secDealers(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	data, err := b.O.InspectionCSV(ctx, b.Tenant, b.Actor)
	if err != nil {
		return PackPart{}, err
	}
	f := b.CSV("terminals_and_users.csv", rowsOfCSV(data))
	return PackPart{Files: []PackFile{f}, Records: f.Records, Note: "As Ledgerline holds them now"}, nil
}

func secNomination(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	rows, err := b.O.ReportRows(ctx, b.Tenant, b.To, "nomination")
	if err != nil {
		return PackPart{}, err
	}
	f := b.CSV("nomination_status.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records, Note: "As Ledgerline holds them now"}, nil
}

// secPMLA is counts only: a suspicious transaction report is confidential
// and the client must not be told of it (PMLA; SEBI's AML/CFT master
// circular SEBI/HO/MIRSD/MIRSDSECFATF/P/CIR/2024/78), so a pack never
// carries an STR, an alert's detail or the clients alerted.
func secPMLA(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	as, err := b.O.Alerts(ctx, b.Tenant)
	if err != nil {
		return PackPart{}, err
	}
	type key struct{ src, typ, sev, st string }
	counts := map[key]int{}
	total, strs := 0, 0
	for _, a := range as {
		if !inSpan(a.Date, from, to) {
			continue
		}
		counts[key{a.Source, a.Type, a.Severity, a.Status}]++
		total++
		if a.Status == "STR filed" {
			strs++
		}
	}
	var ks []key
	for k := range counts {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool {
		a, c := ks[i], ks[j]
		return a.src+a.typ+a.sev+a.st < c.src+c.typ+c.sev+c.st
	})
	rows := [][]string{{"Source", "Alert type", "Severity", "Status", "Alerts"}}
	for _, k := range ks {
		rows = append(rows, []string{k.src, k.typ, k.sev, k.st, fmt.Sprint(counts[k])})
	}
	rows = append(rows, []string{"All", "All", "All", "All", fmt.Sprint(total)}, []string{"All", "All", "All", "STR filed", fmt.Sprint(strs)})
	pv, err := b.O.PMLA(ctx, b.Tenant, to)
	if err != nil {
		return PackPart{}, err
	}
	risk := map[string]int{}
	csc := map[string]int{}
	for _, r := range pv.Rows {
		risk[firstOf(r.Risk, "Not set")]++
		for _, c := range r.CSC {
			csc[c]++
		}
	}
	cr := [][]string{{"What", "Category", "Clients"}}
	for _, k := range sortedKeys(risk) {
		cr = append(cr, []string{"Risk category", k, fmt.Sprint(risk[k])})
	}
	for _, k := range sortedKeys(csc) {
		cr = append(cr, []string{"Special category", k, fmt.Sprint(csc[k])})
	}
	return PackPart{Files: []PackFile{b.CSV("alert_counts.csv", rows), b.CSV("client_risk_counts.csv", cr)}, Records: total,
		Note: fmt.Sprintf("Counts only: %d alerts in the period, %d with an STR filed. No client names, alert details or STR contents: an STR is confidential and the client must not be told of it.", total, strs)}, nil
}

func packDocKindsOf(param string) []string {
	var ks []string
	for _, k := range strings.Split(param, ",") {
		if k = strings.TrimSpace(k); k != "" {
			ks = append(ks, k)
		}
	}
	return ks
}

func readyDocuments(ctx context.Context, o *Ops, tenant string, it PackItem) string {
	ds, _ := o.PackDocs(ctx, tenant)
	ks := packDocKindsOf(it.Param)
	for _, d := range ds {
		if !d.Retired && (len(ks) == 0 || contains(ks, d.Kind)) {
			return ""
		}
	}
	if len(ks) == 0 {
		return "No documents uploaded: add them under Documents on this screen."
	}
	return "Nothing uploaded of the kind " + strings.Join(ks, ", ") + ": add the documents under Documents on this screen."
}

func secDocuments(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	ds, err := b.O.PackDocs(ctx, b.Tenant)
	if err != nil {
		return PackPart{}, err
	}
	ks := packDocKindsOf(it.Param)
	list := [][]string{{"Document", "Kind", "Title", "Version", "File", "Bytes", "Uploaded", "By", "SHA-256"}}
	var files []PackFile
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].ID < ds[j].ID })
	for _, d := range ds {
		if d.Retired || (len(ks) > 0 && !contains(ks, d.Kind)) {
			continue
		}
		rf, err := b.O.App.Ingest.Raw.Get(ctx, b.Tenant, d.FileID)
		if err != nil {
			return PackPart{}, err
		}
		files = append(files, PackFile{Name: d.ID + " " + d.Kind + " - " + d.Name, Data: rf.Content, Records: 1})
		list = append(list, []string{d.ID, d.Kind, d.Title, d.Version, d.Name, fmt.Sprint(d.Size), d.At, d.By, d.SHA256})
	}
	if len(files) == 0 {
		return PackPart{NotAvailable: readyDocuments(ctx, b.O, b.Tenant, it)}, nil
	}
	return PackPart{Files: append([]PackFile{b.CSV("documents.csv", list)}, files...), Records: len(files), Note: "Copies as uploaded (they carry no watermark of their own); retired versions are left out"}, nil
}

func secCalendar(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	ds, err := docs.List[Duty](ctx, b.O.Docs, b.Tenant, cObligation, "")
	if err != nil {
		return PackPart{}, err
	}
	rows := [][]string{{"Duty", "Title", "Authority", "Frequency", "Owner", "Period", "Due", "Done at", "By", "Late", "Evidence"}}
	for _, d := range ds {
		for _, r := range d.Done {
			if inSpan(r.Due, from, to) || inSpan(r.At, from, to) {
				rows = append(rows, []string{d.ID, d.Title, d.Authority, d.Frequency, d.Role, r.Period, r.Due, r.At, r.By, yn(r.Late), r.Evidence})
			}
		}
	}
	sort.SliceStable(rows[1:], func(i, j int) bool { return rows[1+i][6] < rows[1+j][6] })
	open := [][]string{{"Duty", "Title", "Authority", "Owner", "Period", "Due", "Evidence to keep"}}
	if obs, err := b.O.Duties(ctx, b.Tenant, to); err == nil {
		for _, d := range obs {
			if d.Active && d.Overdue {
				open = append(open, []string{d.ID, d.Title, d.Authority, d.Role, d.Period, d.NextDue, d.Evidence})
			}
		}
	}
	f := b.CSV("duties_done.csv", rows)
	return PackPart{Files: []PackFile{f, b.CSV("duties_overdue_at_period_end.csv", open)}, Records: f.Records, Note: fmt.Sprintf("%d duties done; %d overdue at %s", f.Records, len(open)-1, fmtDay(to))}, nil
}

// secCirculars reads the circular register's actions and evidence
// (circulars.go). LEAD: F1 turns circulars into cards with their own
// evidence; when it lands, add the cards' evidence here (or register a
// section of its own and use it in the templates).
func secCirculars(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	from, to := b.Span(it)
	cs, err := b.O.Circulars(ctx, b.Tenant)
	if err != nil {
		return PackPart{}, err
	}
	rows := [][]string{{"Source", "Circular", "Date", "Subject", "Priority", "Circular status", "Action", "Owner", "Due", "Action status", "Evidence or reason", "By", "At", "Note"}}
	n := 0
	for i := len(cs) - 1; i >= 0; i-- {
		c := cs[i]
		in := inSpan(c.Date, from, to)
		for _, t := range c.Tasks {
			in = in || inSpan(t.At, from, to)
		}
		if !in {
			continue
		}
		n++
		if len(c.Tasks) == 0 {
			rows = append(rows, []string{c.Source, c.Ref, c.Date, c.Subject, c.Priority, c.Status, "", "", "", "", "", c.By, "", c.Note})
		}
		for _, t := range c.Tasks {
			rows = append(rows, []string{c.Source, c.Ref, c.Date, c.Subject, c.Priority, c.Status, t.Title, t.Role, t.Due, t.Status, t.Evidence, t.By, t.At, c.Note})
		}
	}
	f := b.CSV("circular_actions.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records, Note: fmt.Sprintf("%d circulars", n)}, nil
}

func readySelfInspection(ctx context.Context, o *Ops, tenant string, it PackItem) string {
	if packSelfInspection == nil {
		return "The nightly self-inspection is not in this version of Ledgerline yet."
	}
	return ""
}

func secSelfInspection(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	if packSelfInspection == nil {
		return PackPart{NotAvailable: readySelfInspection(ctx, b.O, b.Tenant, it)}, nil
	}
	return packSelfInspection(ctx, b, it)
}

// readyDP: the Depository screen lists the BO accounts on record and, for
// an active client with none, the demat account from the client master;
// with neither there is nothing to put in a DP item.
func readyDP(ctx context.Context, o *Ops, tenant string, it PackItem) string {
	if len(o.boAccounts(ctx, tenant)) > 0 {
		return ""
	}
	if cs, _ := o.Clients(ctx, tenant); len(cs) > 0 {
		for _, c := range cs {
			if c.Status == "Active" && c.Demat.ID != "" {
				return ""
			}
		}
	}
	return "No BO accounts in Ledgerline: the firm's DP accounts are not loaded, or it is not a DP."
}

func secDPAccounts(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	if why := readyDP(ctx, b.O, b.Tenant, it); why != "" {
		return PackPart{NotAvailable: why}, nil
	}
	_, to := b.Span(it)
	dp, err := b.O.DP(ctx, b.Tenant, to)
	if err != nil {
		return PackPart{}, err
	}
	rows := [][]string{{"BOID", "UCC", "Name", "Holders", "Primary", "Status", "Freeze", "Freeze reason", "BSDA", "DDPI", "Opened", "Closed on", "ISINs", "Value"}}
	for _, r := range dp.Rows {
		rows = append(rows, []string{r.BOID, r.UCC, r.Name, r.Holders, yn(r.Primary), r.Status, r.Freeze, r.FreezeWhy, yn(r.BSDA), yn(r.DDPI), r.Opened, r.ClosedOn, fmt.Sprint(r.ISINs), r.Value.Fixed(2)})
	}
	f := b.CSV("bo_accounts.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records, Note: "As Ledgerline holds them now"}, nil
}

func secDIS(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	if why := readyDP(ctx, b.O, b.Tenant, it); why != "" {
		return PackPart{NotAvailable: why}, nil
	}
	from, to := b.Span(it)
	dp, err := b.O.DP(ctx, b.Tenant, to)
	if err != nil {
		return PackPart{}, err
	}
	slips := [][]string{{"DIS", "UCC", "Name", "BOID", "Slip", "Kind", "To BO", "Received", "Execution date", "Lines", "Value", "Client confirmation", "Status", "Maker", "Checker", "Batch", "Note"}}
	for _, d := range dp.DIS {
		if inSpan(firstOf(d.Received, d.ExecDate), from, to) {
			slips = append(slips, []string{d.ID, d.UCC, d.Name, d.BOID, fmt.Sprint(d.Slip), d.Kind, d.ToBO, d.Received, d.ExecDate, fmt.Sprint(len(d.Lines)), d.Value.Fixed(2), d.Confirmed, d.Status, d.Maker, d.Checker, d.Batch, d.Note})
		}
	}
	books := [][]string{{"Book", "UCC", "BOID", "From slip", "To slip", "Issued", "Used", "Lost"}}
	for _, k := range dp.Books {
		if inSpan(k.Issued, from, to) {
			books = append(books, []string{k.ID, k.UCC, k.BOID, fmt.Sprint(k.From), fmt.Sprint(k.To), k.Issued, fmt.Sprint(len(k.Used)), fmt.Sprint(len(k.Lost))})
		}
	}
	f := b.CSV("dis_slips.csv", slips)
	return PackPart{Files: []PackFile{f, b.CSV("dis_books_issued.csv", books)}, Records: f.Records}, nil
}

func secDPTxns(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	if why := readyDP(ctx, b.O, b.Tenant, it); why != "" {
		return PackPart{NotAvailable: why}, nil
	}
	if len(b.Clients) == 0 {
		return PackPart{NotAvailable: "No clients were sampled for this pack."}, nil
	}
	from, to := b.Span(it)
	rows := [][]string{{"Date", "UCC", "BOID", "ISIN", "Symbol", "Qty", "Kind", "Reference", "Value", "Clearing member", "Pledge"}}
	for _, u := range b.Clients {
		ts, err := b.O.DPTransactions(ctx, b.Tenant, u)
		if err != nil {
			continue
		}
		for _, t := range ts {
			if inSpan(t.Date, from, to) {
				rows = append(rows, []string{t.Date, t.UCC, t.BOID, t.ISIN, t.Symbol, t.Qty.String(), t.Kind, t.Ref, t.Value.Fixed(2), t.ClearingMember, t.Pledge})
			}
		}
	}
	f := b.CSV("dp_transactions.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records, Note: fmt.Sprintf("%d sampled clients", len(b.Clients))}, nil
}

func secDPRequests(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	if why := readyDP(ctx, b.O, b.Tenant, it); why != "" {
		return PackPart{NotAvailable: why}, nil
	}
	from, to := b.Span(it)
	dp, err := b.O.DP(ctx, b.Tenant, to)
	if err != nil {
		return PackPart{}, err
	}
	rows := [][]string{{"Request", "Kind", "UCC", "Name", "BOID", "Status", "Maker", "Checker", "At", "Detail", "Note"}}
	for _, r := range dp.Reqs {
		if !inSpan(r.At, from, to) {
			continue
		}
		var det []string
		for _, k := range sortedKeys(r.Detail) {
			det = append(det, k+": "+r.Detail[k])
		}
		rows = append(rows, []string{r.ID, r.Kind, r.UCC, r.Name, r.BOID, r.Status, r.Maker, r.Checker, r.At, strings.Join(det, "; "), r.Note})
	}
	f := b.CSV("account_requests.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records}, nil
}

func secDPBills(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	if why := readyDP(ctx, b.O, b.Tenant, it); why != "" {
		return PackPart{NotAvailable: why}, nil
	}
	from, to := b.Span(it)
	dp, err := b.O.DP(ctx, b.Tenant, to)
	if err != nil {
		return PackPart{}, err
	}
	rows := [][]string{{"Bill", "Month", "UCC", "Name", "Lines", "Taxable", "GST", "Stamp duty", "Total", "Invoice", "Entry"}}
	for _, x := range dp.Bills {
		if x.Month >= from[:min(7, len(from))] && x.Month <= to[:7] {
			rows = append(rows, []string{x.ID, x.Month, x.UCC, x.Name, fmt.Sprint(len(x.Lines)), amt(x.Taxable), amt(x.GST), amt(x.Stamp), amt(x.Total), x.Invoice, x.Entry})
		}
	}
	f := b.CSV("dp_bills.csv", rows)
	return PackPart{Files: []PackFile{f}, Records: f.Records}, nil
}

func readyNotHeld(ctx context.Context, o *Ops, tenant string, it PackItem) string {
	return firstOf(it.Note, "Ledgerline does not keep this record.")
}

func secNotHeld(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
	return PackPart{NotAvailable: readyNotHeld(ctx, b.O, b.Tenant, it)}, nil
}
