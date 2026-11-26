package ops

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- self-inspection tests for tax, partners, the website, grievances,
// deposits, receipts and the DP's smaller duties (round 5 of the gap
// report: compregs.go, taxgst.go, gstnotes.go, apweb.go, dpmisc.go) ----

const siGapScreen = "compregs"

func init() {
	reg := "Compliance → Registers"
	siTests = append(siTests,
		SITest{ID: "tds_vendor_rates", Title: "TDS on vendor bills: thresholds, PAN status, certificates", Area: "Money", Severity: "medium", Owner: RoleChecker,
			Source:    "Income-tax Act, 2025 (from 1 April 2026): s.393(1) Table Sl. 8(ii) (was 194Q: 0.1% on purchases of goods beyond ₹50 lakh from a seller, by a buyer with turnover above ₹10 crore); s.397 (was 206AA: higher rate without an operative PAN); lower or nil deduction certificates and no-deduction declarations (were s.197, 197A)",
			ToConfirm: "Section numbers of the 2025 Act for certificates and declarations, and the 5% no-PAN rate for purchases of goods, are carried over from the 1961 Act's provisos.",
			Checks:    "Vendor bills of the last 90 days: TDS on purchases of goods only above ₹50 lakh and only when the firm's turnover was above ₹10 crore; the higher rate for a vendor with no PAN or an inoperative one; the rate of a lower deduction certificate within its amount and dates; no TDS under a declaration.",
			Fix:       "Correct the TDS on the bill (or in the next payment), record the vendor's PAN check, certificate or declaration in Compliance → Registers, and answer the turnover question in Firm practices.",
			Screen:    "books", ScreenName: "Accounts → Firm accounts & GST", Real: true, run: siTDSVendorRates},
		SITest{ID: "tds_certificates", Title: "TDS certificates given within 15 days of the quarterly statement's due date", Area: "Money", Severity: "medium", Owner: RoleChecker,
			Source:    "Income-tax Rules, 2026 (successors of rule 31 of the 1962 Rules): non-salary certificates downloaded from TRACES, given within 15 days of the due date of the quarterly statement",
			ToConfirm: "Salary certificates (Part A from TRACES, Part B from payroll) are not counted here.",
			Checks:    "Vendors and authorised persons with TDS in a quarter whose certificate due date (15 August, 15 November, 15 February, 15 June) has passed in the last year: a certificate recorded for the payee and quarter.",
			Fix:       "Download the certificates from TRACES, give them to the payees and record each in " + reg + " → TDS certificates issued.",
			Screen:    siGapScreen, ScreenName: reg, Real: true, run: siTDSCertificates},
		SITest{ID: "stamp_duty_rates", Title: "Stamp duty in the charge schedule at the Schedule I rates", Area: "Money", Severity: "high", Owner: RoleChecker,
			Source:    "Indian Stamp Act, 1899 as amended by the Finance Act, 2019 (ss. 9A, 21; Schedule I) and the Indian Stamp (Collection of Stamp-Duty through Stock Exchanges, Clearing Corporations and Depositories) Rules, 2019: on the buyer, delivery 0.015%, non-delivery 0.003%, futures 0.002%, options 0.003% of the premium, currency and interest rate derivatives 0.0001%",
			ToConfirm: "Commodity derivatives are taken at the equity futures and options rates.",
			Checks:    "The charge schedule's rule in force on the day for each product: the stamp duty rate on purchases equals the Schedule I rate.",
			Fix:       "Correct the stamp duty rate in the charge schedule and rebill the days charged at the wrong rate.",
			Screen:    "charges", ScreenName: "Accounts → Brokerage & charges → Charges calculator", Real: true, run: siStampRates},
		SITest{ID: "einvoice_irn", Title: "E-invoices: an IRN for each invoice to a client with a GSTIN", Area: "Money", Severity: "medium", Owner: RoleChecker,
			Source:    "CGST Rules, rule 48(4) and notification 10/2023-Central Tax (e-invoicing above ₹5 crore AATO); GSTN advisory on the 30-day reporting limit for AATO of ₹10 crore or more (from 1 April 2025)",
			ToConfirm: "Whether a contract note serving as the tax invoice is reported to the IRP as it is, or through a separate tax invoice, is for the firm's GST adviser.",
			Checks:    "When the firm's AATO answer in Firm practices brings it under e-invoicing: every contract note and tax invoice of the last 60 days to a client with a GSTIN has an IRN recorded; with AATO of ₹10 crore or more, acknowledged within 30 days of the invoice date.",
			Fix:       "Report the invoice on the IRP and record the IRN in " + reg + " → E-invoice IRNs.",
			Screen:    siGapScreen, ScreenName: reg, Real: true, run: siEInvoiceIRN},
		SITest{ID: "itc_gstr2b", Title: "Input tax credit only for invoices in GSTR-2B", Area: "Money", Severity: "medium", Owner: RoleChecker,
			Source: "CGST Act s.16(2)(aa) and rule 36(4): credit only for invoices the supplier reported, shown in GSTR-2B",
			Checks: "Vendor bills with credit taken, against the GSTR-2Bs loaded: each found in a 2B with the same tax and credit available.",
			Fix:    "Hold the credit until the supplier reports the invoice (ask the vendor to file), or reverse it in GSTR-3B; correct the bill if the tax differs.",
			Screen: siGapScreen, ScreenName: reg + " → Tax and GST", Real: true, run: siITCGSTR2B},
		SITest{ID: "itc_180_days", Title: "Credit reversed on bills unpaid 180 days after their date", Area: "Money", Severity: "medium", Owner: RoleChecker,
			Source: "CGST Act s.16(2), second proviso, and rule 37: credit on a bill not paid within 180 days of its date is reversed (with interest) and taken again on payment",
			Checks: "Vendor bills with credit taken and some value unpaid 180 days after the bill date.",
			Fix:    "Pay the vendor, or add the credit on the unpaid part to output tax in GSTR-3B (table 4(B)(2)) and take it again once paid.",
			Screen: siGapScreen, ScreenName: reg + " → Tax and GST", Real: true, run: siITC180},
		SITest{ID: "gst_credit_notes", Title: "A credit or debit note for each bill corrected after its month's return", Area: "Money", Severity: "medium", Owner: RoleChecker,
			Source:    "CGST Act s.34 and rule 53: a change in a tax invoice after it is reported is made by a credit or debit note, declared in the return of the month it is issued",
			ToConfirm: "A correction is taken as after the return when it is made after the 10th of the next month (monthly GSTR-1).",
			Checks:    "Bills corrected in the last three months after their month's GSTR-1 was due: a numbered credit or debit note issued.",
			Fix:       "Issue the notes on Accounts → Firm accounts & GST (GST), and declare them in the GSTR-1 of the month (table 9B and table 7).",
			Screen:    "books", ScreenName: "Accounts → Firm accounts & GST", Real: true, run: siGSTNotes},
		SITest{ID: "transmission_tlh", Title: "Nominee to legal heir transfers sent with reason TLH", Area: "Depository", Severity: "medium", Owner: RoleChecker,
			Source: "SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/130, paras 4 and 6: a transmission of securities from the nominee to the legal heirs is reported to CBDT with reason code TLH, not as a transfer",
			Checks: "Off-market slips of the last 90 days from accounts that received securities by transmission: the reason is Transmission (sent to CDSL as TLH) or another reason the staff chose knowingly.",
			Fix:    "Check each slip with the client; if it passes the shares on to the legal heirs, correct the reason with CDSL.",
			Screen: "dp", ScreenName: "Depository → Demat accounts & DIS", Real: true, run: siTransmissionTLH},
		SITest{ID: "ap_change_notice", Title: "Branch clients told before their AP changes", Area: "Clients", Severity: "medium", Owner: RoleCompliance,
			Source: "SEBI master circular for stock brokers 2025/90, para 33.7.4",
			Checks: "Clients of one branch moved together from one authorised person to another on a day in the last 60 days: a notice to the branch's clients sent before that day.",
			Fix:    "Send the notice with Notify branch clients on " + reg + " → Partners and website (before the next change), and record any sent another way.",
			Screen: siGapScreen, ScreenName: reg, Real: true, run: siAPChangeNotice},
		SITest{ID: "ap_payout_hold", Title: "AP payouts held while its clients' complaints are unresolved", Area: "Clients", Severity: "medium", Owner: RoleCompliance,
			Source:    "SEBI master circular for stock brokers 2025/90, para 33.7.7: on irregularities by an AP, amounts due to it are withheld until the investors' issues are resolved",
			ToConfirm: "Ledgerline cannot tell whether a complaint is about the AP's conduct: it lists APs whose clients have complaints past their due date, for the compliance officer to judge.",
			Checks:    "Authorised persons with a client complaint open past its due date and no payout hold.",
			Fix:       "Look into the complaints; where the AP is at fault, hold its payouts on " + reg + " → Partners and website until they are resolved.",
			Screen:    siGapScreen, ScreenName: reg, Real: true, run: siAPPayoutHold},
		SITest{ID: "terminal_turnover", Title: "Low and declining terminals reviewed", Area: "Operations", Severity: "low", Owner: RoleCompliance,
			Source:    "MCX/INSP/255/2026 (inspection master circular), para 5.1: turnover from every terminal at branches and AP locations monitored, trends analysed and low or declining locations reviewed closely for business done outside the terminal",
			ToConfirm: "MCX sets no figure for low volume; Ledgerline flags a terminal below a tenth of the median, or lower three months running.",
			Checks:    "Terminals with trades in the last six months: last full month below a tenth of the median terminal, or lower in each of the last three full months.",
			Fix:       "Review each flagged terminal and location (visit, call records, the dealer's clients), record the outcome, and mute the test for it with the reason.",
			Screen:    "dealers", ScreenName: "Branches & partners → Dealers & terminals", Real: true, run: siTerminalTurnover},
		SITest{ID: "nse_user_limits", Title: "NSE user IDs typed and within the limits NSE allowed", Area: "Operations", Severity: "medium", Owner: RoleCompliance,
			Source: "NSE/MSD/73956 (user ID consolidated circular): permitted user types and the number of user IDs the member may hold",
			Checks: "Open NSE terminals: each with its user type (corporate manager, branch manager, dealer); the count of each type within the number entered in Firm practices.",
			Fix:    "Set each terminal's user type on " + reg + " → Partners and website, enter NSE's limits in Firm practices, and close user IDs beyond them.",
			Screen: siGapScreen, ScreenName: reg, Real: true, run: siNSEUserLimits},
		SITest{ID: "website_lists", Title: "Website shows the current USCNBA accounts and AP lists", Area: "Compliance", Severity: "medium", Owner: RoleCompliance,
			Source:    "NSE/INSP/73976 item 4.12 (all active USCNBA accounts with the prescribed note); NSE/COMP/73981 Exhibits 42-44 (active APs and those cancelled for disciplinary reasons, exchange-wise)",
			ToConfirm: "The exact note of item 4.12 and the column format of Exhibits 42-44.",
			Checks:    "Each list as Ledgerline has it now against the version last marked as put on the website.",
			Fix:       "Copy the list from " + reg + " → Partners and website to the website, then mark the website updated.",
			Screen:    siGapScreen, ScreenName: reg, Real: true, run: siWebsiteLists},
		SITest{ID: "prop_trading_notice", Title: "Clients told before the firm trades on its own account", Area: "Compliance", Severity: "medium", Owner: RoleCompliance,
			Source: "SEBI master circular for stock brokers 2025/90, paras 30.1-30.3",
			Checks: "Trades in the firm's own trading codes (Firm securities settings): a notice to all clients recorded on or before the first of them in Ledgerline.",
			Fix:    "Send the notice with Tell clients about proprietary trading on " + reg + " (or record one sent earlier).",
			Screen: siGapScreen, ScreenName: reg, Real: true, run: siPropNotice},
		SITest{ID: "bse_complaints", Title: "BSE complaints answered by BSE's date, from a dedicated e-mail", Area: "Compliance", Severity: "medium", Owner: RoleCompliance,
			Source: "BSE master circular on investor grievance redressal (2025), paras 2.4-2.7",
			Checks: "The dedicated e-mail for BSE's investor services recorded in Firm practices (a firm with a BSE member code); complaints BSE forwarded with BSE's due date, and none open past it.",
			Fix:    "Record the e-mail given to BSE; enter BSE's due date on each complaint and upload the redressal on BSE's portal.",
			Screen: siGapScreen, ScreenName: reg + " → Grievances and deposits", Real: true, run: siBSEComplaints},
		SITest{ID: "complaints_networth", Title: "Complaints pending over 30 days below half the net worth", Area: "Compliance", Severity: "high", Owner: RoleCompliance,
			Source:    "SEBI master circular for stock brokers 2025/90, para 15.8.1.1(h): an event-based trigger when complaints pending over 30 days amount to more than 50% of net worth",
			ToConfirm: "A complaint without an amount claimed counts as nil; enter the amount on each complaint that claims money.",
			Checks:    "Amounts claimed in complaints open for more than 30 days, against 50% of the net worth worked out on Firm securities.",
			Fix:       "Resolve the complaints; enter the amount claimed on each; check the net worth inputs.",
			Screen:    "helpdesk", ScreenName: "Compliance → Investor complaints", Real: true, run: siComplaintsNetWorth},
		SITest{ID: "bmc_cash", Title: "Half of the base minimum capital in cash and cash equivalents", Area: "Money", Severity: "high", Owner: RoleChecker,
			Source:    "SEBI CIR/MRD/DRMNP/36/2012, para 3(g)-(h): at least 50% of the base minimum capital in cash and cash equivalents; no exposure against it",
			ToConfirm: "Cash equivalents are taken as cash, fixed deposits and bank guarantees (the register's rule for collateral); the circular's own list may add government securities.",
			Checks:    "Active deposits with each clearing corporation marked Base minimum capital: cash, fixed deposits and bank guarantees at least half of their value.",
			Fix:       "Replace securities in the base minimum capital with cash, a fixed deposit or a bank guarantee.",
			Screen:    "deposits", ScreenName: "Risk → Collateral → Clearing deposits", Real: true, run: siBMCCash},
		SITest{ID: "segment_deposit", Title: "Segment deposit kept as a self-clearing member in equity derivatives", Area: "Money", Severity: "high", Owner: RoleChecker,
			Source: "SEBI (Stock Brokers) Regulations, 2026 (SEBI/LAD-NRO/GN/2026/291), regulation 48: ₹50 lakh in equity derivatives as a self-clearing member; nil as a trading member clearing through another clearing member",
			Checks: "When Firm practices says the firm self-clears equity derivatives: active deposits with NSE Clearing or ICCL for the FO segment (or all segments) of at least ₹50 lakh.",
			Fix:    "Deposit the shortfall with the clearing corporation and record it on Clearing deposits.",
			Screen: "deposits", ScreenName: "Risk → Collateral → Clearing deposits", Real: true, run: siSegmentDeposit},
		SITest{ID: "dd_bank_certificate", Title: "Demand drafts of ₹50,000 a day only with the bank's certificate", Area: "Money", Severity: "high", Owner: RoleChecker,
			Source: "NSE/INSP/73976 item 4.3: pre-funded instruments of ₹50,000 or more per client per day accepted only with the issuing bank's certificate of the account holder's name and the account debited",
			Checks: "Demand draft and pay order receipts of the last 30 days: on each client-day totalling ₹50,000 or more, every one carries the certified name and account, and the account is one of the client's registered accounts.",
			Fix:    "Get the bank's certificate for the instrument and record it on the receipt, or return the money.",
			Screen: "money", ScreenName: "Accounts → Receipts & payouts", Real: true, run: siDDCertificate},
		SITest{ID: "dp_pass_through", Title: "Depository charges recovered only as CDSL levied them", Area: "Depository", Severity: "medium", Owner: RoleChecker,
			Source:    "SEBI master circular for depositories 2024/168, para 4.86.6 (true to label)",
			ToConfirm: "Only the transaction charges' depository share and stamp duty are compared; AMC and pledge charges billed as the DP's own are not.",
			Checks:    "Each month billed in the last six: the depository share and stamp duty on clients' DP bills against CDSL's bill entered in " + reg + ".",
			Fix:       "Enter CDSL's bill; where clients paid more than CDSL billed, correct the tariff's depository share and refund the excess.",
			Screen:    siGapScreen, ScreenName: reg + " → Depository", Real: true, run: siPassThrough},
		SITest{ID: "dis_setup_delay", Title: "Off-market slips set up the day received or the next", Area: "Depository", Severity: "medium", Owner: RoleChecker,
			Source: "CDSL communiqués DP2025-543 (points 1-6) and DP2025-641; CDSL bye-law 7.2.1",
			Checks: "Off-market and inter-depository slips entered in the last 30 days: set up within a day of receipt, and when more than 2 days later, with the reason recorded.",
			Fix:    "Record the reason on the slip, and set up slips the day they are received.",
			Screen: "dp", ScreenName: "Depository → Demat accounts & DIS", Real: true, run: siDISSetupDelay},
		SITest{ID: "aop_accounts", Title: "AoP demat accounts with both PANs and no equity shares", Area: "Depository", Severity: "medium", Owner: RoleCompliance,
			Source: "SEBI/HO/MRD/PoD1/CIR/P/2025/24, para 3",
			Checks: "Demat accounts recorded as an association of persons' own: no equity shares held (ISIN security type 01, or type EQ in the security master).",
			Fix:    "Ask the AoP to move the equity shares out; review the account with the compliance officer.",
			Screen: siGapScreen, ScreenName: reg + " → Depository", Real: true, run: siAoPAccounts},
		SITest{ID: "pool_voting", Title: "No voting on securities in pool accounts", Area: "Depository", Severity: "low", Owner: RoleCompliance,
			Source: "SEBI master circular for depositories 2024/168, para 4.36",
			Checks: "When the firm has demat accounts tagged POOL: Firm practices records that it never votes on securities held in them.",
			Fix:    "Answer the question in Settings → Firm practices, and tell the staff who handle e-voting.",
			Screen: "practices", ScreenName: "Settings → Firm practices", Real: true, run: siPoolVoting},
		SITest{ID: "ro_kit_delivery", Title: "Rights and Obligations given to each new demat account holder", Area: "Depository", Severity: "medium", Owner: RoleChecker,
			Source: "SEBI master circular for depositories 2024/168, para 1.3.4",
			Checks: "Demat accounts opened in the last 90 days: the Rights and Obligations document given, electronically (logged) or physically.",
			Fix:    "Send it with Send R&O on " + reg + " → Depository, or record the printed copy given.",
			Screen: siGapScreen, ScreenName: reg + " → Depository", Real: true, run: siROKit},
		SITest{ID: "closure_channel", Title: "Closure requests only through the permitted channels", Area: "Clients", Severity: "low", Owner: RoleCompliance,
			Source: "NSE/INSP/73976 item 1.15(iii): online closure requests only through the broker's portal or app with two-factor authentication, or from the client's registered e-mail; not by SMS or messaging apps",
			Checks: "Closure requests of the last 90 days: received signed, online with two-factor login, or from the registered e-mail.",
			Fix:    "Ask the client to confirm through a permitted channel before closing.",
			Screen: "settlement", ScreenName: "Operations → Settlement", Real: true, run: siClosureChannel},
		SITest{ID: "dlt_templates", Title: "SMS only through DLT-registered templates", Area: "Compliance", Severity: "medium", Owner: RoleCompliance,
			Source: "TRAI Telecom Commercial Communications Customer Preference Regulations, 2018: the principal entity, its headers and its templates registered on the operator's DLT platform; messages only through registered templates",
			Checks: "Active SMS templates: a 19-digit DLT template ID that is not one of Ledgerline's sample IDs; the SMS connection with the DLT entity ID and header.",
			Fix:    "Register the templates on the DLT platform and enter their IDs in Client messages → templates, and the entity ID in Connections.",
			Screen: "messages", ScreenName: "Client documents → Client messages", Real: true, run: siDLTTemplates},
	)
}

func siTDSVendorRates(e *siEnv) (siOut, error) {
	all, err := e.o.vendorBills(e.ctx, e.tenant)
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Vendor bills", addDays(e.date, -90))
	from := addDays(e.date, -90)
	vs := map[string]Vendor{}
	for _, b := range all {
		if b.BillDate < from || b.BillDate > e.date || b.Status == BillRejected || b.Status == BillCancelled {
			continue
		}
		v, ok := vs[b.Vendor]
		if !ok {
			v, _ = e.o.Vendor(e.ctx, e.tenant, b.Vendor)
			vs[b.Vendor] = v
		}
		others := make([]VendorBill, 0, len(all))
		for _, x := range all {
			if x.ID != b.ID && (x.BillDate < b.BillDate || (x.BillDate == b.BillDate && x.ID < b.ID)) {
				others = append(others, x)
			}
		}
		s.checked++
		if w := e.o.vendorTDSWarnings(e.ctx, e.tenant, v, b, others); len(w) > 0 {
			s.bad(fmt.Sprintf("Bill %s of %s (%s): %s", b.ID, b.VendorName, fmtDay(b.BillDate), w[0]))
		}
	}
	s.summary = fmt.Sprintf("%d vendor bills of the last 90 days checked; %d with TDS to look at.", s.checked, s.found)
	return s, nil
}

func siTDSCertificates(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Vendor bills and AP payouts with TDS", e.date)
	s.input("TDS certificates issued", e.date)
	for _, c := range e.o.tdsCertsDue(e.ctx, e.tenant) {
		if c.Due == "" || c.Due > e.date || c.Due < addDays(e.date, -365) {
			continue
		}
		s.checked++
		if c.Issued == "" {
			s.bad(fmt.Sprintf("%s %s (%s) · %s %s · TDS ₹%s · certificate due %s, none recorded", c.PayeeKind, c.Payee, c.Name, c.FY, c.Quarter, formatINR(c.TDS.Dec()), fmtDay(c.Due)))
		}
	}
	s.summary = fmt.Sprintf("%d certificates due in the last year; %d not recorded as given.", s.checked, s.found)
	return s, nil
}

func siStampRates(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Charge schedule", e.date)
	sched := e.o.App.Schedule
	for _, b := range sortedKeys(stampScheduleI) {
		want := money.MustDec(stampScheduleI[b])
		for _, r := range sched.Rules {
			if string(r.Bucket) != b || r.EffectiveFrom > e.date || (r.EffectiveTo != "" && r.EffectiveTo < e.date) {
				continue
			}
			s.checked++
			if r.StampBuyPct.Cmp(want) != 0 {
				s.bad(fmt.Sprintf("%s on %s: stamp duty %s%% in the schedule, Schedule I has %s%%", b, firstOf(r.Exchange, "*"), r.StampBuyPct, want))
			}
		}
	}
	if s.checked == 0 {
		return siSkip("Waiting for data: the charge schedule has no rule in force today.")
	}
	s.summary = fmt.Sprintf("%d charge rules in force checked; %d with a stamp duty rate other than Schedule I's.", s.checked, s.found)
	return s, nil
}

func siEInvoiceIRN(e *siEnv) (siOut, error) {
	opt, _ := e.o.tcPractice(e.ctx, e.tenant, "gst-aato")
	switch opt {
	case "":
		return siSkip("Waiting for data: answer the GST turnover question in Settings → Firm practices.")
	case "none":
		return siSkip("Not applicable: the firm's AATO is ₹5 crore or less (Firm practices).")
	}
	from := addDays(e.date, -60)
	r, err := e.o.GST(e.ctx, e.tenant, from, e.date)
	if err != nil {
		return siOut{}, err
	}
	irn := map[string]CompRecord{}
	for _, x := range e.o.compRecs(e.ctx, e.tenant, "irn") {
		irn[strings.ToUpper(x.V("invoice"))] = x
	}
	var s siOut
	s.input("GST register (B2B)", from)
	s.input("E-invoice IRNs", e.date)
	for _, l := range r.B2B {
		if strings.HasPrefix(l.Invoice, "BILL ") {
			continue
		}
		s.checked++
		x, ok := irn[strings.ToUpper(l.Invoice)]
		switch {
		case !ok && opt == "10cr" && addDays(l.Date, 30) <= e.date:
			s.bad(fmt.Sprintf("%s of %s to %s · no IRN, and the 30 days to report it have passed", l.Invoice, fmtDay(l.Date), siClient(l.UCC)))
		case !ok && l.Date < e.date:
			s.bad(fmt.Sprintf("%s of %s to %s · no IRN recorded", l.Invoice, fmtDay(l.Date), siClient(l.UCC)))
		case ok && opt == "10cr" && x.V("ack_date") > addDays(l.Date, 30):
			s.bad(fmt.Sprintf("%s of %s · reported on %s, after 30 days", l.Invoice, fmtDay(l.Date), fmtDay(x.V("ack_date"))))
		}
	}
	s.summary = fmt.Sprintf("%d invoices to clients with a GSTIN in the last 60 days; %d without an IRN in time.", s.checked, s.found)
	return s, nil
}

func siITCGSTR2B(e *siEnv) (siOut, error) {
	m := e.o.itcMatch(e.ctx, e.tenant)
	if len(m.Periods) == 0 {
		return siSkip("Waiting for data: no GSTR-2B loaded (Compliance → Registers → Tax and GST).")
	}
	var s siOut
	s.input("GSTR-2B", m.Periods[len(m.Periods)-1])
	s.input("Vendor bills", e.date)
	first := m.Periods[0] + "-01"
	last := addDays(addMonths(m.Periods[len(m.Periods)-1]+"-01", 1), -1)
	for _, r := range m.Rows {
		if r.BillDate < first || r.BillDate > last {
			continue
		}
		s.checked++
		if r.Status != "In GSTR-2B" {
			s.bad(fmt.Sprintf("Bill %s %s of %s (%s) · %s", r.Bill, r.BillNo, r.Vendor, fmtDay(r.BillDate), r.Status))
		}
	}
	s.summary = fmt.Sprintf("%d bills with credit taken in the months of the GSTR-2Bs loaded; %d not matched.", s.checked, s.found)
	return s, nil
}

func siITC180(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Vendor bills and payments", e.date)
	bills, _ := e.o.vendorBills(e.ctx, e.tenant)
	for _, b := range bills {
		if b.ITC && b.gst() > 0 && addDays(b.BillDate, 180) <= e.date {
			s.checked++
		}
	}
	for _, r := range e.o.itcUnpaid180(e.ctx, e.tenant, e.date) {
		s.bad(fmt.Sprintf("Bill %s of %s (%s) · ₹%s unpaid past %s · reverse credit of ₹%s", r.Bill, r.Vendor, fmtDay(r.Date), formatINR(r.Unpaid.Dec()), fmtDay(r.Limit), formatINR(r.Reverse.Dec())))
	}
	s.summary = fmt.Sprintf("%d bills with credit past 180 days; %d with some value unpaid.", s.checked, s.found)
	return s, nil
}

func siGSTNotes(e *siEnv) (siOut, error) {
	from := addMonths(e.date[:7]+"-01", -2)
	r, err := e.o.GST(e.ctx, e.tenant, from, e.date)
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Bill corrections", from)
	for _, n := range r.Notes {
		s.checked++
		if n.Invoice == "" {
			s.bad(fmt.Sprintf("%s of %s corrected on %s · %s of ₹%s not issued", n.Orig, fmtDay(n.OrigDate), fmtDay(n.Date), strings.ToLower(n.Kind), formatINR(n.Taxable.Dec())))
		}
	}
	s.summary = fmt.Sprintf("%d late bill corrections in the last three months; %d without a note.", s.checked, s.found)
	return s, nil
}

func siTransmissionTLH(e *siEnv) (siOut, error) {
	ds, _ := docs.List[DIS](e.ctx, e.o.Docs, e.tenant, cDIS, "")
	from := addDays(e.date, -90)
	var s siOut
	s.input("Delivery instructions", from)
	s.input("Transmission requests", e.date)
	got := map[string]bool{}
	for _, d := range ds {
		if d.Kind == "On-market" || firstOf(d.SetupOn, d.Received) < from || firstOf(d.SetupOn, d.Received) > e.date || d.Status == "Rejected" {
			continue
		}
		if _, ok := got[d.UCC]; !ok {
			got[d.UCC] = e.o.receivedByTransmission(e.ctx, e.tenant, d.UCC)
		}
		if !got[d.UCC] {
			continue
		}
		s.checked++
		if d.Reason != "Transmission" {
			s.bad(fmt.Sprintf("Slip %d (%s) · %s, which received a transmission · reason %q: if it passes shares to the legal heirs, use Transmission (TLH)", d.Slip, d.ID, siClient(d.UCC), d.Reason))
		}
	}
	s.summary = fmt.Sprintf("%d slips from accounts that received a transmission; %d with another reason.", s.checked, s.found)
	return s, nil
}

func siAPChangeNotice(e *siEnv) (siOut, error) {
	from := addDays(e.date, -60)
	var s siOut
	s.input("Client-AP mapping changes", from)
	s.input("Notices to branch clients", e.date)
	type grp struct {
		branch, from, to, date string
		n                      int
	}
	gs := map[string]*grp{}
	for u, h := range e.o.apHistory(e.ctx, e.tenant) {
		c := e.clients[u]
		for _, x := range h {
			if x.From == "" || x.To == "" || x.NewClient || x.Date < from || x.Date > e.date || c.Branch == "" {
				continue
			}
			k := c.Branch + "|" + x.From + "|" + x.To + "|" + x.Date
			if gs[k] == nil {
				gs[k] = &grp{c.Branch, x.From, x.To, x.Date, 0}
			}
			gs[k].n++
		}
	}
	notes := e.o.compRecs(e.ctx, e.tenant, "ap-notice")
	for _, k := range sortedKeys(gs) {
		g := gs[k]
		if g.n < 2 {
			continue // one client's own move is the client's request
		}
		s.checked++
		ok := false
		for _, r := range notes {
			if r.V("branch") == g.branch && r.V("sent_on") < g.date && r.V("effective_on") <= g.date && (r.V("new_ap") == "" || r.V("new_ap") == g.to) {
				ok = true
			}
		}
		if !ok {
			s.bad(fmt.Sprintf("Branch %s · %d clients moved from AP %s to %s on %s with no notice sent before", g.branch, g.n, g.from, g.to, fmtDay(g.date)))
		}
	}
	s.summary = fmt.Sprintf("%d branch AP changes in the last 60 days; %d without a notice before.", s.checked, s.found)
	return s, nil
}

func siAPPayoutHold(e *siEnv) (siOut, error) {
	ts, err := e.o.Tickets(e.ctx, e.tenant, e.date)
	if err != nil {
		return siOut{}, err
	}
	st := e.o.apStatuses(e.ctx, e.tenant)
	var s siOut
	s.input("Investor complaints", e.date)
	s.input("AP payout holds", e.date)
	late := map[string][]string{}
	for _, t := range ts {
		if t.Kind != KindComplaint || t.Status == "Resolved" || t.Status == "Closed" || t.Due >= e.date {
			continue
		}
		if p := e.clients[t.UCC].Partner; p != "" {
			late[p] = append(late[p], t.ID)
		}
	}
	for _, p := range sortedKeys(late) {
		s.checked++
		if !st[p].Hold {
			s.bad(fmt.Sprintf("AP %s · complaints past due from its clients: %s · payouts not held", p, strings.Join(late[p], ", ")))
		}
	}
	s.summary = fmt.Sprintf("%d APs whose clients have complaints past due; %d with payouts not held.", s.checked, s.found)
	return s, nil
}

func siTerminalTurnover(e *siEnv) (siOut, error) {
	rep := e.o.terminalTurnover(e.ctx, e.tenant, e.date)
	if len(rep.Rows) == 0 {
		return siSkip("Not checkable: no trades of the last six months carry a terminal ID.")
	}
	var s siOut
	s.input("Trades with their terminals", rep.Months[0]+"-01")
	for _, r := range rep.Rows {
		s.checked++
		if r.Review != "" {
			s.bad(fmt.Sprintf("%s terminal %s (%s) · %s", r.Exchange, r.Terminal, firstOf(r.Location, "location not recorded"), r.Review))
		}
	}
	s.summary = fmt.Sprintf("%d terminals' turnover by month; %d to review. %s", s.checked, s.found, rep.Rule)
	return s, nil
}

func siNSEUserLimits(e *siEnv) (siOut, error) {
	counts, untyped := e.o.nseUserCounts(e.ctx, e.tenant, e.date)
	total := len(untyped)
	for _, c := range counts {
		total += c.Open
	}
	if total == 0 {
		return siSkip("Not applicable: no open NSE terminal on Dealers & terminals.")
	}
	var s siOut
	s.input("Dealers & terminals", e.date)
	s.input("NSE user ID limits (Firm practices)", e.date)
	s.checked = total
	for _, t := range untyped {
		s.bad("NSE terminal " + t + " · user type not set")
	}
	for _, c := range counts {
		switch {
		case c.Open > 0 && c.Limit == "":
			s.bad(fmt.Sprintf("%s · %d open, NSE's limit not entered in Firm practices", c.Type, c.Open))
		case c.Over:
			s.bad(fmt.Sprintf("%s · %d open, NSE allows %s", c.Type, c.Open, c.Limit))
		}
	}
	s.summary = fmt.Sprintf("%d open NSE terminals; %d problems with their user types or limits.", total, s.found)
	return s, nil
}

func siWebsiteLists(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Firm bank accounts and authorised persons", e.date)
	s.input("Website updates", e.date)
	for _, l := range []WebsiteList{e.o.websiteUSCNBA(e.ctx, e.tenant), e.o.websiteAPs(e.ctx, e.tenant)} {
		if len(l.Accounts) == 0 && len(l.Active) == 0 && len(l.Cancelled) == 0 {
			continue
		}
		s.checked++
		if l.Changed {
			why := "never marked as put on the website"
			if l.Published != "" {
				why = "changed since it was put on the website on " + fmtDay(l.Published)
			}
			s.bad(l.Title + " · " + why)
		}
	}
	if s.checked == 0 {
		return siSkip("Not applicable: no USCNBA account and no authorised person recorded.")
	}
	s.summary = fmt.Sprintf("%d website lists checked; %d out of date.", s.checked, s.found)
	return s, nil
}

func siPropNotice(e *siEnv) (siOut, error) {
	own := e.o.FirmSecSettings(e.ctx, e.tenant).ownCodes()
	if len(own) == 0 {
		return siSkip("Not applicable: no own trading code in the Firm securities settings.")
	}
	ts, _ := e.o.App.Trades.List(e.ctx, e.tenant, "", "")
	first := ""
	for _, t := range ts {
		if contains(own, strings.ToUpper(t.UCC)) && (first == "" || t.TradeDate < first) {
			first = t.TradeDate
		}
	}
	var s siOut
	s.input("Trades in the firm's own codes", e.date)
	s.input("Notice to clients", e.date)
	if first == "" {
		s.summary = "No trade in the firm's own codes yet."
		return s, nil
	}
	s.checked = 1
	ok := false
	for _, r := range e.o.compRecs(e.ctx, e.tenant, "prop-notice") {
		if r.V("sent_on") <= first {
			ok = true
		}
	}
	if !ok {
		s.bad("The firm's own codes traded from " + fmtDay(first) + " with no notice to clients recorded before")
	}
	s.summary = "Proprietary trading in Ledgerline from " + fmtDay(first) + "."
	return s, nil
}

func siBSEComplaints(e *siEnv) (siOut, error) {
	ts, err := e.o.Tickets(e.ctx, e.tenant, e.date)
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Investor complaints", e.date)
	if _, bse := e.set.MemberCodes["BSE"]; bse {
		s.checked++
		if opt, vals := e.o.tcPractice(e.ctx, e.tenant, "bse-grievance-email"); opt != "given" || vals["email"] == "" {
			s.bad("No dedicated e-mail for BSE's investor services recorded in Firm practices")
		}
	}
	for _, t := range ts {
		if t.Source != "Stock exchanges" || t.Exchange != "BSE" || t.Status == "Resolved" || t.Status == "Closed" {
			continue
		}
		s.checked++
		switch {
		case t.ExchangeDue == "":
			s.bad(fmt.Sprintf("%s (BSE %s) · BSE's due date not entered", t.ID, t.Ref))
		case t.ExchangeDue < e.date:
			s.bad(fmt.Sprintf("%s (BSE %s) · open past BSE's date %s", t.ID, t.Ref, fmtDay(t.ExchangeDue)))
		}
	}
	s.summary = fmt.Sprintf("%d checks of BSE complaint handling; %d to fix.", s.checked, s.found)
	return s, nil
}

func siComplaintsNetWorth(e *siEnv) (siOut, error) {
	nw, err := e.o.FirmSecNetWorth(e.ctx, e.tenant, SystemActor, e.date)
	if err != nil || nw.NetWorth <= 0 {
		return siSkip("Waiting for data: no net worth worked out (Firm securities → net worth).")
	}
	ts, err := e.o.Tickets(e.ctx, e.tenant, e.date)
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Investor complaints", e.date)
	s.input("Net worth", e.date)
	var sum money.Amount
	for _, t := range ts {
		if t.Kind != KindComplaint || t.Status == "Resolved" || t.Status == "Closed" || addDays(t.OpenedOn, 30) >= e.date {
			continue
		}
		s.checked++
		sum += t.Claim
	}
	half := nw.NetWorth / 2
	if sum > half {
		s.bad(fmt.Sprintf("Complaints open over 30 days claim ₹%s, more than half the net worth (₹%s)", formatINR(sum.Dec()), formatINR(half.Dec())))
	}
	s.summary = fmt.Sprintf("%d complaints open over 30 days claim ₹%s; half the net worth is ₹%s.", s.checked, formatINR(sum.Dec()), formatINR(half.Dec()))
	return s, nil
}

func siBMCCash(e *siEnv) (siOut, error) {
	v, err := e.o.Deposits(e.ctx, e.tenant, e.date)
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Clearing deposits", e.date)
	for _, c := range v.ByCC {
		if c.BMCHeld.Sign() <= 0 {
			continue
		}
		s.checked++
		if c.BMCCashPct < 50 {
			s.bad(fmt.Sprintf("%s · base minimum capital ₹%s, of it %.0f%% cash or cash equivalents", c.CC, formatINR(c.BMCHeld), c.BMCCashPct))
		}
	}
	if s.checked == 0 {
		return siSkip("Not applicable: no deposit marked Base minimum capital on Clearing deposits.")
	}
	s.summary = fmt.Sprintf("%d clearing corporations' base minimum capital checked; %d below half in cash.", s.checked, s.found)
	return s, nil
}

func siSegmentDeposit(e *siEnv) (siOut, error) {
	opt, _ := e.o.tcPractice(e.ctx, e.tenant, "fo-clearing")
	switch opt {
	case "":
		return siSkip("Waiting for data: answer how the firm clears equity derivatives in Settings → Firm practices.")
	case "tm", "none":
		return siSkip("Not applicable: the firm does not self-clear equity derivatives (Firm practices).")
	}
	ds, _ := docs.List[Deposit](e.ctx, e.o.Docs, e.tenant, cDeposit, "")
	var s siOut
	s.input("Clearing deposits", e.date)
	s.checked = 1
	var held money.Dec
	for _, d := range ds {
		if d.Status != "Active" || d.ClientFunds || (d.Expiry != "" && d.Expiry < e.date) || (d.CC != "NSE Clearing" && d.CC != "ICCL") {
			continue
		}
		if d.Segment == "FO" || d.Segment == "ALL" || d.Segment == "" {
			held = held.Add(d.Value)
		}
	}
	if need := money.DecInt(5000000); held.Cmp(need) < 0 {
		s.bad(fmt.Sprintf("Equity derivatives deposits ₹%s, below ₹50 lakh", formatINR(held)))
	}
	s.summary = fmt.Sprintf("Deposits for equity derivatives ₹%s (₹50 lakh needed as a self-clearing member).", formatINR(held))
	return s, nil
}

func siDDCertificate(e *siEnv) (siOut, error) {
	rs, _ := docs.List[Receipt](e.ctx, e.o.Docs, e.tenant, cReceipt, "")
	from := addDays(e.date, -siWindow)
	day := map[string]money.Amount{}
	for _, r := range rs {
		if r.Mode == ModeDD && r.Status != "Rejected" && r.Date >= from && r.Date <= e.date {
			day[r.UCC+"|"+r.Date] += r.Amount
		}
	}
	var s siOut
	s.input("Receipts", from)
	for _, r := range rs {
		if r.Mode != ModeDD || r.Status == "Rejected" || r.Date < from || r.Date > e.date {
			continue
		}
		s.checked++
		if day[r.UCC+"|"+r.Date] < ddCertFrom {
			continue
		}
		c := e.clients[r.UCC]
		_, own := registeredBank(c, r.BankCertAccount, "")
		switch {
		case r.BankCertName == "" || r.BankCertAccount == "":
			s.bad(fmt.Sprintf("%s · %s on %s · no bank certificate", r.ID, siClient(r.UCC), fmtDay(r.Date)))
		case !own:
			s.bad(fmt.Sprintf("%s · %s on %s · the certified account is not the client's registered account", r.ID, siClient(r.UCC), fmtDay(r.Date)))
		}
	}
	s.summary = fmt.Sprintf("%d demand drafts and pay orders in the last 30 days; %d without a valid bank certificate.", s.checked, s.found)
	return s, nil
}

func siPassThrough(e *siEnv) (siOut, error) {
	from := addMonths(e.date[:7]+"-01", -6)[:7]
	var s siOut
	s.input("DP bills", from+"-01")
	s.input("CDSL's bills", e.date)
	rows := e.o.dpPassThrough(e.ctx, e.tenant)
	months := map[string]bool{}
	for _, r := range rows {
		if r.Month < from || r.Month >= e.date[:7] {
			continue
		}
		months[r.Month] = true
		s.checked++
		switch {
		case !r.Entered && r.Recovered > 0:
			s.bad(fmt.Sprintf("%s %s · ₹%s recovered from clients; CDSL's bill not entered", r.Month, strings.ToLower(r.Head), formatINR(r.Recovered.Dec())))
		case r.OverByMoney > 100:
			s.bad(fmt.Sprintf("%s %s · ₹%s recovered from clients, CDSL billed ₹%s", r.Month, strings.ToLower(r.Head), formatINR(r.Recovered.Dec()), formatINR(r.Billed.Dec())))
		}
	}
	if len(months) == 0 {
		return siSkip("Not applicable: no DP bill in the last six months.")
	}
	s.summary = fmt.Sprintf("%d month-heads of the last six months compared; %d to look at.", s.checked, s.found)
	return s, nil
}

func siDISSetupDelay(e *siEnv) (siOut, error) {
	ds, _ := docs.List[DIS](e.ctx, e.o.Docs, e.tenant, cDIS, "")
	from := addDays(e.date, -siWindow)
	var s siOut
	s.input("Delivery instructions", from)
	for _, d := range ds {
		if d.Kind == "On-market" || d.SetupOn == "" || d.SetupOn < from || d.SetupOn > e.date {
			continue
		}
		s.checked++
		switch {
		case d.DelayDays > 2 && d.DelayReason == "":
			s.bad(fmt.Sprintf("Slip %d (%s) · received %s, set up %s · no reason recorded", d.Slip, d.ID, fmtDay(d.Received), fmtDay(d.SetupOn)))
		case d.DelayDays == 2:
			s.bad(fmt.Sprintf("Slip %d (%s) · received %s, set up %s · later than the next day", d.Slip, d.ID, fmtDay(d.Received), fmtDay(d.SetupOn)))
		}
	}
	s.summary = fmt.Sprintf("%d slips set up in the last 30 days; %d late.", s.checked, s.found)
	return s, nil
}

var reEquityISIN = regexp.MustCompile(`^INE[0-9A-Z]{4}01[0-9A-Z]{3}$`)

func siAoPAccounts(e *siEnv) (siOut, error) {
	recs := e.o.compRecs(e.ctx, e.tenant, "aop")
	if len(recs) == 0 {
		return siSkip("Not applicable: no demat account recorded as an association of persons'.")
	}
	var s siOut
	s.input("Holdings", e.date)
	for _, r := range recs {
		s.checked++
		c := e.clients[r.V("ucc")]
		hs, _ := e.o.Holdings(e.ctx, e.tenant, c.UCC)
		for _, h := range hs {
			if h.Qty.Sign() <= 0 || (h.BOID != "" && normDematID(h.BOID) != r.V("bo_id") && holdingAcct(c, r.V("bo_id")) != h.BOID) {
				continue
			}
			ref, _, _ := docs.Get[SecRef](e.ctx, e.o.Docs, e.tenant, cSecRef, h.ISIN)
			if ref.Type == "EQ" || reEquityISIN.MatchString(h.ISIN) {
				s.bad(fmt.Sprintf("%s · AoP account %s holds equity %s", siClient(c.UCC), cdslMaskBO(r.V("bo_id")), firstOf(h.Symbol, h.ISIN)))
				break
			}
		}
	}
	s.summary = fmt.Sprintf("%d AoP demat accounts checked; %d hold equity shares.", s.checked, s.found)
	return s, nil
}

func siPoolVoting(e *siEnv) (siOut, error) {
	pools := 0
	for _, t := range e.set.FirmDemats {
		if normHSType(t) == "POOL" {
			pools++
		}
	}
	if pools == 0 {
		return siSkip("Not applicable: no demat account tagged POOL in Settings → Firm demat accounts.")
	}
	var s siOut
	s.input("Firm practices", e.date)
	s.checked = 1
	if opt, _ := e.o.tcPractice(e.ctx, e.tenant, "pool-voting"); opt != "never" {
		s.bad("The firm has not recorded that it never votes on securities in its pool accounts")
	}
	s.summary = fmt.Sprintf("%d pool accounts.", pools)
	return s, nil
}

func siROKit(e *siEnv) (siOut, error) {
	from := addDays(e.date, -90)
	given := map[string]bool{}
	for _, r := range e.o.compRecs(e.ctx, e.tenant, "ro-kit") {
		given[r.V("ucc")] = true
	}
	var s siOut
	s.input("Demat accounts", from)
	s.input("Rights and Obligations given", e.date)
	seen := map[string]bool{}
	bs := e.o.boAccounts(e.ctx, e.tenant)
	sort.SliceStable(bs, func(i, j int) bool { return bs[i].Opened < bs[j].Opened })
	for _, b := range bs {
		if b.Opened < from || b.Opened > e.date || b.Status == "Not opened" || seen[b.UCC] {
			continue
		}
		seen[b.UCC] = true
		s.checked++
		if !given[b.UCC] {
			s.bad(fmt.Sprintf("%s · demat account opened %s · Rights and Obligations not recorded as given", siClient(b.UCC), fmtDay(b.Opened)))
		}
	}
	s.summary = fmt.Sprintf("%d clients with a demat account opened in the last 90 days; %d without the document recorded.", s.checked, s.found)
	return s, nil
}

func siClosureChannel(e *siEnv) (siOut, error) {
	xs, err := e.o.Closures(e.ctx, e.tenant)
	if err != nil {
		return siOut{}, err
	}
	from := addDays(e.date, -90)
	var s siOut
	s.input("Account closure requests", from)
	for _, x := range xs {
		if x.Received < from || x.Received > e.date {
			continue
		}
		s.checked++
		if !contains(ClosureSources, x.Source) {
			s.bad(fmt.Sprintf("%s · %s · received as %q", x.ID, siClient(x.UCC), x.Source))
		}
	}
	s.summary = fmt.Sprintf("%d closure requests in the last 90 days; %d through another channel.", s.checked, s.found)
	return s, nil
}

// reSampleDLT matches the DLT template IDs of Ledgerline's sample templates
// (messaging.go DefaultTemplates): placeholders, not registered IDs.
var reSampleDLT = regexp.MustCompile(`^110716600000000000[0-9]$`)

func siDLTTemplates(e *siEnv) (siOut, error) {
	ts, err := e.o.templates(e.ctx, e.tenant)
	if err != nil {
		return siOut{}, err
	}
	var s siOut
	s.input("Message templates", e.date)
	sms := 0
	for _, t := range ts {
		if !t.Active || t.Channel != ChSMS {
			continue
		}
		sms++
		s.checked++
		switch {
		case !regexp.MustCompile(`^[0-9]{19}$`).MatchString(t.DLT):
			s.bad(fmt.Sprintf("SMS template %s (%s) · no 19-digit DLT template ID", t.ID, t.Event))
		case reSampleDLT.MatchString(t.DLT):
			s.bad(fmt.Sprintf("SMS template %s (%s) · still Ledgerline's sample DLT ID %s", t.ID, t.Event, t.DLT))
		}
	}
	if sms == 0 {
		return siSkip("Not applicable: no active SMS template.")
	}
	if c, ok := e.o.msgConn(e.ctx, e.tenant, "sms"); ok {
		s.checked++
		if c.Values["dlt_entity"] == "" || c.Values["sender"] == "" {
			s.bad("The SMS connection has no DLT entity ID or header")
		}
	}
	s.summary = fmt.Sprintf("%d active SMS templates; %d not on a registered DLT template.", sms, s.found)
	return s, nil
}
