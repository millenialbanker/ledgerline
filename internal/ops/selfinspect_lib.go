package ops

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- the self-inspection's tests ----
//
// Each test reuses the check Ledgerline already makes on its own screen and
// reads only (it changes nothing; the run keeps what it found). Its source
// is the rule as checked against the circular, with what could not be
// confirmed in ToConfirm. Examples name records by their IDs and clients by
// masked codes.
//
// Sources checked (November 2026):
//   - SEBI/HO/MIRSD/MIRSD2/CIR/P/2016/95 of 26 September 2016 (enhanced
//     supervision), para 3.3.1: cash and cash equivalents with the broker
//     and the clearing corporation at least equal to the clients' credit
//     balances.
//   - SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197 of 28 December 2023: running
//     accounts settled on the first Friday or Saturday of the month or
//     quarter the client chose, on the exchanges' joint calendar.
//   - NSE/INSP/55024 of 28 December 2022 (kept by NSE/INSP/61904 of 6 May
//     2024): the UCC-wise details of each settlement within 10 trading
//     days; NSE/INSP/56261 of 3 April 2023: ₹10,000 a day late.
//   - SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90 of 17 June
//     2025, para 15.4.4.2, and NSE/INSP/55904 of 9 March 2023: a daily
//     reconciliation statement of transfers between the client, settlement
//     and own accounts.
//   - SEBI HO/38/11/(9)2026-MIRSD-POD/I/15382/2026 of 3 July 2026 (paras
//     46.4, 46.8, 46.10: payment within 5 trading days of pay-out,
//     reasonable notice before invoking, release by the depositories at
//     the end of the 6th trading day) and NSE circular 41/2026
//     (NSE/INSP/75508) of 31 July 2026, guidelines 6 and 7.2: at most
//     2.25 × (C − B) kept in CUSPA, the excess released on or before the
//     next trading day; notice by a verifiable mode, logged.
//   - NSE/INSP/64315 (circular 69/2024) of 1 October 2024: a margin
//     penalty passes to the client only for a dishonoured cheque or a
//     hedge break disclosed in the risk management policy, for penalties
//     levied on or after 1 November 2024.
//   - The investor charter for stock brokers (SEBI/HO/MIRSD/MIRSD-PoD1/P/
//     CIR/2025/22 of 21 February 2025, Annexure A): contract notes within
//     24 hours of the trades; complaints resolved within 21 calendar days.
//   - SEBI/HO/OIAE/IGRD/CIR/P/2023/156 of 20 September 2023 (SCORES, in
//     force 4 December 2023): 21 calendar days for a complaint.
//   - SEBI/MRD/SE/Cir-33/2003/27/08 of 27 August 2003: cheques only from
//     the client and payments only in the client's favour.
//   - SEBI/HO/MIRSD/FATF/P/CIR/2023/0144 of 11 August 2023 (KRA
//     validation): a client whose KYC attributes are not verified does not
//     transact; NSE/INSP/64718 (80/2024) of 25 October 2024: inactive after
//     24 months, flagged in every exchange's UCC database, reactivated only
//     after IPV/VIPV and a validated or registered KRA status.
//   - NSE/SURV/48818 of 1 July 2021 (the same text: MSE/SURV/10627/2021),
//     kept in NSE/SURV/74008 of 30 April 2026: alerts processed within 45
//     days, with the reasons for any delay recorded.
//   - NSE/INSP/55039 of 28 December 2022 and NCL/INSP/60046 of 29 December
//     2023: the daily holding statement by T+1 (a one-time nil declaration
//     when there are no holdings); BSE notice 20230113-13; NSE/INSP/70505.
//   - SEBI/HO/MRD2_DCAP/CIR/2021/0598 of 20 July 2021: client-level
//     collateral reported to the clearing corporation (SEGMON).
//   - NSE/INSP/47227 of 3 February 2021, with its penalties in
//     NSE/INSP/48431 (29/2021) of 28 May 2021: the weekly statement of
//     funds, securities and commodities within the next four trading days.
//     The 2025 investor charter lists monthly statements: whether the
//     weekly one still applies is to confirm.
//   - SEBI orders debarring entities from the securities market,
//     circulated by the exchanges (for example NSE/INVG/53493 of 30 August
//     2022, with NSE's consolidated list).
//   - NSE/ISC/73973 of 30 April 2026 (the UCC master circular).
//   - SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2022/137 of 6 October 2022 (DDPI
//     purposes).
//   - NSE/ISC/45168 of 29 July 2020, NSE/ISC/47834 of 31 March 2021 and
//     NSE/ISC/72805 of 13 February 2026: clients' mobiles on DoT's
//     revocation list confirmed with the client and updated immediately.
//   - NSE/ISC/77375 of 9 November 2026 (and NSE/ISC/76893 of 14 October
//     2026): the UCCs of clients the KRAs report as deceased closed before
//     the month's cut-off; SEBI/HO/OIAE/OIAE_IAD-1/P/CIR/2023/0000000163 of
//     3 October 2023: their debits blocked at once.

const (
	siAlertDays = 45 // NSE/SURV/48818
	siParkDays  = 3  // working days a third-party credit may wait to be returned (firm's practice; to confirm)
	siWindow    = 30 // days of payouts, receipts and trades looked at
)

var siTests = []SITest{
	{ID: "funds_segregation", Title: "Client money fully covered", Area: "Money", Severity: "high", Owner: RoleChecker,
		Source: "SEBI/HO/MIRSD/MIRSD2/CIR/P/2016/95 of 26 Sep 2016 (enhanced supervision), para 3.3.1",
		Checks: "The segregation check (bank.go Funds): the client bank account and money with the clearing corporation, less unmatched credits, against the clients' credit balances, netting each client's ledgers.",
		Fix:    "Reconcile the client bank account and the clearing corporation balance; bring in the shortfall from the firm's own funds; find any client money used for the firm or another client.",
		Screen: "ledger", ScreenName: "Accounts → Ledger", Real: true, run: siFunds},
	{ID: "ras_settlement", Title: "Running accounts settled on each client's chosen date", Area: "Money", Severity: "high", Owner: RoleChecker,
		Source: "SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197 of 28 Dec 2023 (the exchanges' joint RAS calendar); SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/1 of 6 Jan 2025 (30 days without a transaction: returned on the upcoming monthly settlement date)",
		Checks: "Running account settlement: credit balances not paid out after their monthly or quarterly date, idle balances not returned on their monthly settlement date, and calendar dates with no settlement run.",
		Fix:    "Settle the clients due on Running account (Receipts & payouts); add the exchanges' calendar for the year on Circulars & duties → RAS dates.",
		Screen: "ras", ScreenName: "Receipts & payouts → Running account", Real: true, run: siRAS},
	{ID: "ras_ucc_report", Title: "Each running account settlement reported to NSE within 10 trading days", Area: "Money", Severity: "high", Owner: RoleCompliance,
		Source: "NSE/INSP/55024 of 28 Dec 2022 (UCC-wise details within 10 trading days, kept by NSE/INSP/61904 of 6 May 2024); NSE/INSP/56261 of 3 Apr 2023 (₹10,000 a day late)",
		Checks: "Every settlement whose 10 trading days have passed (a settlement run, or a date of the exchanges' calendar): its UCC-wise report recorded as uploaded, or as not applicable, by the due date.",
		Fix:    "Make the file on Running account, upload it on NSE's member portal and record the portal's reference; where no client was settled, record the not-applicable declaration.",
		Screen: "ras", ScreenName: "Receipts & payouts → Running account", Real: true, run: siRASUCC},
	{ID: "client_money_recon", Title: "Daily client money reconciliation signed off", Area: "Money", Severity: "high", Owner: RoleChecker,
		Source:    "SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90 of 17 Jun 2025, para 15.4.4.2 (a daily reconciliation statement of transfers); NSE/INSP/55904 of 9 Mar 2023",
		ToConfirm: "No format is prescribed: Ledgerline's statement follows the money between clients, the USCNBA, the clearing corporation, the DSCNBA, the settlement and own accounts.",
		Checks:    "Each business day of the last two weeks before the date: the day's statement prepared and signed off by a checker who did not prepare it.",
		Fix:       "Prepare the day's statement on Client money reconciliation, explain what is flagged, and have a checker sign it off.",
		Screen:    "clientmoney", ScreenName: "Receipts & payouts → Client money reconciliation", Real: true, run: siMoneyRecon},
	{ID: "cuspa_limit", Title: "Unpaid securities kept within 2.25 × (margin − ledger)", Area: "Settlement", Severity: "medium", Owner: RoleChecker,
		Source: "NSE circular 41/2026 (NSE/INSP/75508) of 31 Jul 2026, guideline 6, under SEBI HO/38/11/(9)2026-MIRSD-POD/I/15382/2026 of 3 Jul 2026",
		Checks: "CUSPA ageing: lines over the client's limit at the end of the previous trading day and still not released.",
		Fix:    "Release the excess on Settlement → Unpaid securities (Release excess), oldest pledge first.",
		Screen: "settlement", ScreenName: "Operations → Settlement", Real: true, run: siCUSPALimit},
	{ID: "cuspa_notice", Title: "Notice given before unpaid securities were invoked", Area: "Settlement", Severity: "high", Owner: RoleChecker,
		Source: "SEBI HO/38/11/(9)2026-MIRSD-POD/I/15382/2026 of 3 Jul 2026, para 46.8; NSE circular 41/2026 (NSE/INSP/75508), guideline 7.2",
		Checks: "Every CUSPA line invoked: a notice to the client recorded on or before the day of invocation.",
		Fix:    "Find the notice sent (email or SMS log) and record it; tell the client and review why it was invoked without one.",
		Screen: "settlement", ScreenName: "Operations → Settlement", Real: true, run: siCUSPANotice},
	{ID: "cuspa_day6", Title: "Nothing kept in CUSPA past the 6th trading day", Area: "Settlement", Severity: "medium", Owner: RoleChecker,
		Source: "SEBI HO/38/11/(9)2026-MIRSD-POD/I/15382/2026 of 3 Jul 2026, para 46.10 (released by the depositories at the end of the 6th trading day after pay-out)",
		Checks: "CUSPA ageing: lines still held after the 6th trading day after pay-out.",
		Fix:    "Run the daily CUSPA review so the depository's release is recorded; check the depository's statement for the line.",
		Screen: "settlement", ScreenName: "Operations → Settlement", Real: true, run: siCUSPADay6},
	{ID: "margin_penalties", Title: "Margin penalties worked out, and passed to clients only where allowed", Area: "Risk", Severity: "high", Owner: RoleRisk,
		Source: "NSE/INSP/64315 (circular 69/2024) of 1 Oct 2024: only for a dishonoured cheque or a disclosed hedge break, for penalties levied from 1 Nov 2024",
		Checks: "Each trading day with a short collection has its penalties prepared; each penalty passed on has an allowed reason, evidence, and a checker other than its maker; no penalty is posted to a client without an approved pass.",
		Fix:    "Prepare the day's penalties on RMS → Margin reporting; reverse a penalty posted to a client without an allowed, approved pass and tell the client.",
		Screen: "margin", ScreenName: "RMS & margin → Margin reporting", Real: true, run: siPenalties},
	{ID: "contract_notes", Title: "Contract notes sent within 24 hours", Area: "Operations", Severity: "high", Owner: RoleMaker,
		Source: "SEBI investor charter for stock brokers (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/22 of 21 Feb 2025, Annexure A): within 24 hours of the trades",
		Checks: "Every client who traded in the last five trading days: a contract note, sent (or printed for courier) within 24 hours of the session's close.",
		Fix:    "Generate and send the missing notes on Contract notes; correct bounced email addresses; record the dispatch of physical notes.",
		Screen: "notes", ScreenName: "Operations → Contract notes", Real: true, run: siNotes},
	{ID: "complaints_21", Title: "Complaints resolved within 21 days (SCORES)", Area: "Compliance", Severity: "high", Owner: RoleCompliance,
		Source: "SEBI/HO/OIAE/IGRD/CIR/P/2023/156 of 20 Sep 2023 (SCORES, in force 4 Dec 2023): 21 calendar days",
		Checks: "Investor complaints open more than 21 calendar days.",
		Fix:    "Resolve each with a reply to the client on Investor complaints, and file the action taken report on SCORES.",
		Screen: "helpdesk", ScreenName: "Compliance → Investor complaints", Real: true, run: siComplaints},
	{ID: "payouts_registered", Title: "Payouts only to the client's registered bank accounts", Area: "Money", Severity: "high", Owner: RoleChecker,
		Source: "SEBI/MRD/SE/Cir-33/2003/27/08 of 27 Aug 2003 (payments only in the client's favour)",
		Checks: "Payout batches and running account settlements of the last 30 days: each paid to an account registered to the client on that day.",
		Fix:    "Recover or confirm the payment with the bank; register the account through a change request with proof, or pay to a registered account.",
		Screen: "money", ScreenName: "Receipts & payouts", Real: true, run: siPayouts},
	{ID: "third_party_receipts", Title: "Money from accounts the client didn't register is returned, not used", Area: "Money", Severity: "high", Owner: RoleChecker,
		Source:    "SEBI/MRD/SE/Cir-33/2003/27/08 of 27 Aug 2003 (cheques only from the client)",
		ToConfirm: "The three working days to return a parked credit are the firm's practice, not a rule found in a circular.",
		Checks:    "Bank credits from unregistered accounts (third-party or unidentified) parked more than three working days; receipts credited from an account not registered to the client.",
		Fix:       "Return the credit to its source on Bank reconciliation, or identify it with the bank's confirmation that the account is the client's.",
		Screen:    "bank", ScreenName: "Receipts & payouts → Bank reconciliation", Real: true, run: siThirdParty},
	{ID: "kyc_complete", Title: "KYC complete for active clients", Area: "Clients", Severity: "medium", Owner: RoleCompliance,
		Source:    "SEBI/HO/MIRSD/FATF/P/CIR/2023/0144 of 11 Aug 2023 (KRA validation); NSE/INSP/64718 of 25 Oct 2024 (validated or registered KRA status)",
		ToConfirm: "Income proof for derivatives is Ledgerline's account-opening check; confirm it against the exchanges' current KYC rules.",
		Checks:    "Every active client: KRA status validated or registered, PAN on record, no details missing from the previous system, income proof for derivatives segments.",
		Fix:       "Collect the documents, upload to the KRA and record the status on KYC & PMLA; complete the missing details on the client.",
		Screen:    "kyc", ScreenName: "Compliance → KYC & PMLA", Real: true, run: siKYC},
	{ID: "fatca_new", Title: "FATCA/CRS self-certifications recorded, checked and sent to the KRA", Area: "Clients", Severity: "medium", Owner: RoleCompliance,
		Source:    "SEBI/HO/MIRSD/SECFATF/P/CIR/2024/12 of 20 Feb 2024 (paras 3, 5 and 6); CVL/OPS/INTERM/GENRL/24-200 of 28 Jun 2024 (with the KYC upload from 1 Aug 2024); SEBI HO/38/14/(18)2026-MIRSD-POD2/I/25560/2026 of 11 Nov 2026 (paras 6 and 7)",
		ToConfirm: "No time is set for the FATCA/CRS upload: it goes with the KYC upload, so the KYC upload's three working days (KYC master circular, para 84) are used.",
		Checks:    "Active clients onboarded from 1 Aug 2024, and every active client with a self-certification: one on record, its reasonableness warnings resolved, and (for clients onboarded from 1 Aug 2024) at the KRA within three working days of being recorded, in a KRA file marked uploaded or accepted in the KRA's status file.",
		Fix:       "Record or correct the self-certification on KYC & PMLA → FATCA / CRS (a checker approves the change), resolve each warning with what was checked, upload the KRA file and mark it uploaded.",
		Screen:    "kyc", ScreenName: "Compliance → KYC & PMLA", Real: true, run: siFATCANew},
	{ID: "fatca_legacy", Title: "FATCA/CRS of clients onboarded before August 2024 at the KRA", Area: "Clients", Severity: "medium", Owner: RoleCompliance,
		Source:    "SEBI/HO/MIRSD/SECFATF/P/CIR/2024/12 of 20 Feb 2024, para 4 (within 90 days); KARVY/OPS/INTERM/GENRL/24-009 of 28 Sep 2024 (legacy individual clients by 31 Oct 2024)",
		ToConfirm: "The KRAs were to notify the process and format for non-individual legacy clients separately; not found.",
		Checks:    "Active clients onboarded before 1 Aug 2024, or migrated without an opening date: a self-certification on record, and at the KRA.",
		Fix:       "Collect the missing self-certifications (or load them with the FATCA-CRS file), make the legacy bulk file on KYC & PMLA → FATCA / CRS, upload it to the KRA and mark it uploaded.",
		Screen:    "kyc", ScreenName: "Compliance → KYC & PMLA", Real: true, run: siFATCALegacy},
	{ID: "inactive_accounts", Title: "Accounts inactive for 24 months flagged, and not traded without reactivation", Area: "Clients", Severity: "medium", Owner: RoleCompliance,
		Source: "NSE/INSP/64718 (circular 80/2024) of 25 Oct 2024, with the matching BSE and MCX circulars",
		Checks: "Accounts past the inactivity period not flagged; inactive accounts that traded without being reactivated.",
		Fix:    "Flag the accounts on Inactive accounts (they go in the UCC upload); reactivate only after IPV or VIPV and a validated KRA status.",
		Screen: "inactive", ScreenName: "Compliance → Inactive accounts", Real: true, run: siInactive},
	{ID: "ddpi_purpose", Title: "DDPI or POA used only for permitted purposes", Area: "Depository", Severity: "medium", Owner: RoleChecker,
		Source: "SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2022/137 of 6 Oct 2022 (deliveries and settlement, margin pledges, mutual funds and tenders on the exchange platforms)",
		Checks: "Debits from clients' demat accounts made under a DDPI or POA, against the permitted purposes.",
		Fix:    "—", Screen: "dp", ScreenName: "Depository → Demat accounts & DIS", Real: false,
		Waiting: "Ledgerline doesn't have the data for this yet: the depository records it loads do not say which debits were made under a DDPI or POA, or for what purpose."},
	{ID: "alerts_45", Title: "Surveillance and PMLA alerts decided within 45 days", Area: "Compliance", Severity: "high", Owner: RoleCompliance,
		Source: "NSE/SURV/48818 of 1 Jul 2021 (same text MSE/SURV/10627/2021), kept in NSE/SURV/74008 of 30 Apr 2026: alerts processed within 45 days",
		Checks: "Alerts open or under review more than 45 days after they were raised (an alert referred to the exchange counts as decided).",
		Fix:    "Decide each alert on Surveillance & AML with the evidence; record the reason for the delay; file an STR where the principal officer decides one is due.",
		Screen: "surv", ScreenName: "Compliance → Surveillance & AML", Real: true, run: siAlerts},
	{ID: "holding_statement", Title: "Holding statement sent to the exchange each day", Area: "Compliance", Severity: "high", Owner: RoleCompliance,
		Source: "NSE/INSP/55039 of 28 Dec 2022 (daily except Sunday); NSE/INSP/55380 (corrections: the whole day again); NSE/INSP/60046 of 29 Dec 2023 (T+1, one-time NIL Holding declaration, bulk nil report before resuming); BSE notice 20230113-13; NSE/INSP/70505; special session circulars (e.g. Muhurat on a Sunday)",
		Checks: "Each data day of the last week (not Sundays, except special sessions): accepted by NSE's API, the upload recorded with NSE's acknowledgement (BSE's BEFS for a BSE-only member), or, when nothing was held, covered by the NIL Holding declaration or the bulk nil report.",
		Fix:    "Submit the day on Exchange files (holding statement), oldest first, and record the acknowledgement on Regulatory reports; for a nil day, make the NIL Holding declaration on NSE's portal and record it on Exchange files.",
		Screen: "xfiles", ScreenName: "Files → Exchange files", Real: true, run: siHolding},
	{ID: "segmon", Title: "Client collateral (SEGMON) reported each day", Area: "Compliance", Severity: "high", Owner: RoleChecker,
		Source:    "SEBI/HO/MRD2_DCAP/CIR/2021/0598 of 20 Jul 2021 (segregation and monitoring of collateral at client level)",
		ToConfirm: "The times are NSE Clearing's as Ledgerline's file timetable has them (CC01 the same evening, SEGMON by noon the next day).",
		Checks:    "The last three trading days: the client collateral (CC01) and SEGMON files made, and no records rejected in NSE Clearing's response.",
		Fix:       "Make and upload the files on Exchange files; correct the rejected records and send them again.",
		Screen:    "xfiles", ScreenName: "Files → Exchange files", Real: true, run: siSEGMON},
	{ID: "weekly_statement", Title: "Weekly statement of funds and securities sent", Area: "Clients", Severity: "medium", Owner: RoleMaker,
		Source:    "NSE/INSP/47227 of 3 Feb 2021; NSE/INSP/48431 (29/2021) of 28 May 2021: within the next four trading days",
		ToConfirm: "The 2025 investor charter lists monthly statements of accounts: confirm the weekly statement still applies.",
		Checks:    "The last two weeks whose four trading days have passed: the statements sent.",
		Fix:       "Send the week's statements on Periodic work (or Dispatch register).",
		Screen:    "periodic", ScreenName: "Calendar & circulars → Periodic work", Real: true, run: siWeekly},
	{ID: "debarred_trades", Title: "No trades by debarred clients", Area: "Compliance", Severity: "high", Owner: RoleCompliance,
		Source: "SEBI orders debarring entities from the securities market, circulated by the exchanges (for example NSE/INVG/53493 of 30 Aug 2022)",
		Checks: "Trades of the last 30 days by a client whose PAN is on a debarment in force that day, and active clients on one who are not blocked.",
		Fix:    "Block the client on RMS, report the trades to the exchange and the compliance officer's file; load the latest lists on KYC & PMLA.",
		Screen: "kyc", ScreenName: "Compliance → KYC & PMLA", Real: true, run: siDebarred},
	{ID: "ucc_match", Title: "Client details match the exchanges' UCC records", Area: "Clients", Severity: "medium", Owner: RoleChecker,
		Source: "NSE/ISC/73973 of 30 Apr 2026 (UCC master circular), and the exchanges' UCC answers (PAN verification, permitted to trade)",
		Checks: "Active clients' UCC records the exchanges rejected, need fixing, hold a PAN the exchange could not verify, or are not permitted to trade.",
		Fix:    "Correct the client's details and send the UCC again on Exchange UCC.",
		Screen: "uccapi", ScreenName: "Clients & onboarding → Exchange UCC", Real: true, run: siUCC},
	{ID: "ucc_fields", Title: "Client details at the exchanges match Ledgerline", Area: "Clients", Severity: "medium", Owner: RoleChecker,
		Source:    "NSE/ISC/73973 of 30 Apr 2026 (UCC master circular: details changed at the exchange when they change); NSE/INSP/73503 (9/2026) of 27 Mar 2026, Annexure A-B items 46 and 47 (mobiles and e-mails not uploaded as in the registration documents); penalty orders: Profitmart (mobiles differed in 142 cases, e-mails in 140), NSE order of 8 May 2024 (37 cases)",
		ToConfirm: "The exchanges publish no layout for their UCC downloads: the columns are read by header names (config/formats/*-ucc-download.json); add a header text there when an exchange's file differs.",
		Checks:    "Every active client against the exchanges' UCC downloads of the last 45 days (PAN, mobile, e-mail, primary bank account with IFSC, address; kept only as fingerprints), and against what NSE's UCC API last accepted: which field differs, and clients changed since and not sent again.",
		Fix:       "Send the client's current details to the exchange (Exchange UCC, or the exchange's UCC portal), or correct Ledgerline's record with a change request if the exchange has the right one; load a fresh download to check.",
		Screen:    "uccapi", ScreenName: "Clients & onboarding → Exchange UCC", Real: true, run: siUCCFields},
	{ID: "mnrl_matches", Title: "Client mobiles on DoT's revocation list taken up in time", Area: "Clients", Severity: "medium", Owner: RoleMaker,
		Source:    "NSE/ISC/45168 of 29 Jul 2020 and NSE/ISC/47834 of 31 Mar 2021 (confirm the number with the client; update the back office and the UCC immediately); NSE/ISC/72805 of 13 Feb 2026 (daily report); CDSL/OPS/DP/POLCY/2026/517 of 30 Jul 2026; NSDL/POLICY/2026/0090 of 16 Jun 2026",
		ToConfirm: "The circulars say immediately: the 3 business days to take a match up and the 30 days for a contacted client to give a number are the firm's practice.",
		Checks:    "Matches from the revocation lists (NSE's report, DoT's MNRL, NSDL's e-PASS) still New after 3 business days, or Contacted for more than 30 days with no number confirmed.",
		Fix:       "Contact the client through another registered channel and raise the mobile change on Change requests; or mark the match on Revoked mobiles with what the check found.",
		Screen:    "mnrl", ScreenName: "Clients & onboarding → Revoked mobiles", Real: true, run: siMNRL},
	{ID: "deceased_ucc", Title: "Deceased clients' UCCs closed by the cut-off, with debits blocked", Area: "Clients", Severity: "high", Owner: RoleCompliance,
		Source: "NSE/ISC/77375 of 9 Nov 2026 and NSE/ISC/76893 of 14 Oct 2026 (UCCs closed before the month's cut-off); NSE/ISC/75724 of 13 Aug 2026; SEBI/HO/OIAE/OIAE_IAD-1/P/CIR/2023/0000000163 of 3 Oct 2023 (debits blocked at once)",
		Checks: "Clients on the KRAs' demise list: the UCC closed on every exchange before the cut-off in the month's NSE circular, and debits blocked from the day the list was loaded (unless the intimation was found false).",
		Fix:    "Verify the death and close the UCCs on Deceased clients (the UCC upload sends them), or record the closure made on each exchange's portal; tell the KRA the same day if the intimation is false.",
		Screen: "deceased", ScreenName: "Compliance → Deceased clients", Real: true, run: siDeceased},
	{ID: "bsda_review", Title: "BSDA review done each quarter", Area: "Depository", Severity: "low", Owner: RoleChecker,
		Source:    "SEBI circulars on Basic Services Demat Accounts (from CIR/MRD/DP/22/2012 of 27 Aug 2012)",
		ToConfirm: "The quarterly timing is Ledgerline's Periodic work; SEBI's 2024 revision has DPs reassess eligibility each billing cycle (number to confirm).",
		Checks:    "The BSDA review of the last quarter applied by its due date.",
		Fix:       "Run the review on Periodic work and have a checker apply it.",
		Screen:    "periodic", ScreenName: "Calendar & circulars → Periodic work", Real: true, run: siBSDA},
	{ID: "daily_files", Title: "The day's exchange and clearing files received and made", Area: "Operations", Severity: "low", Owner: RoleMaker,
		Source: "The firm's own control: Ledgerline's file timetable for its exchanges and clearing (no single circular)",
		Checks: "Expected files of the day and the day before, past their time and not received or made.",
		Fix:    "Load or download the missing files on Exchange files; the missing-files alert says which.",
		Screen: "xfiles", ScreenName: "Files → Exchange files", Real: true, run: siFiles},
	{ID: "ap_contacts", Title: "No AP's own mobile or email on a client's record", Area: "Clients", Severity: "medium", Owner: RoleChecker,
		Source: "NSE/COMP/63628 of 28 Aug 2024 (framework for supervision of APs and branches), Annexure A item 28: the AP's mobile number and email address are not mapped to any of its clients in the UCC",
		Checks: "Every authorised person's own mobile and email on Branches & partners, against the mobile and email of every client not closed (a client with the AP's own PAN, its own account, is left out).",
		Fix:    "Correct the client's mobile or email with a change request carrying the client's own details, and send the UCC to the exchanges again.",
		Screen: "partners", ScreenName: "Branches & partners", Real: true, run: siAPContacts},
	{ID: "ap_mapping_weekly", Title: "Clients mapped to APs reported to each exchange every week", Area: "Compliance", Severity: "medium", Owner: RoleMaker,
		Source:    "NSE/COMP/49509 of 3 Sep 2021; MSE circular 10854 of 4 Sep 2021 (item 15 of MSE/MEM/15275/2024 of 30 Apr 2024): weekly, by the 2nd trading day of the following week, matching the exchange's UCC",
		ToConfirm: "The exchanges' upload layouts could not be read; the files follow config/formats, marked unconfirmed.",
		Checks:    "The latest week whose due date has passed: clients mapped, re-mapped or unmapped to an AP that are not in an upload recorded for an exchange, and records held back that need fixing.",
		Fix:       "Make the week's file on Branches & partners → AP client reporting, upload it on each exchange's portal and record the acknowledgement; fix the records held back.",
		Screen:    "partners", ScreenName: "Branches & partners", Real: true, run: siAPMapping},
	{ID: "impersonation_actions", Title: "Every impersonation case acted on: FIR, takedown, notice, client alert", Area: "Compliance", Severity: "high", Owner: RoleCompliance,
		Source:    "BSE notices 20250804-58 (IA) and 20250804-59 (RA) of 4 Aug 2025, paras (a) to (d); NSDL/POLICY/2024/0048 of 18 Apr 2024 (police complaint, takedown, public notice, caution to clients)",
		ToConfirm: "The 7 days to record the steps are Ledgerline's own; the notices give no number of days.",
		Checks:    "Open cases on the impersonation register found more than 7 days ago without an FIR, a takedown request, the notice on the website or the alert to clients.",
		Fix:       "File the FIR, ask the platform or authority to take it down, put the notice on the website's home page and alert the clients; record each on Investor complaints → Impersonation.",
		Screen:    "helpdesk", ScreenName: "Compliance → Investor complaints", Real: true, run: siImpActions},
	{ID: "impersonation_report", Title: "Impersonation reported to BSE (RA, IA) and the depository (DP) each month", Area: "Compliance", Severity: "medium", Owner: RoleCompliance,
		Source:    "BSE notices 20250804-58 and 20250804-59 of 4 Aug 2025 (monthly, within 7 days of the month's end, on the IA/RA Membership Portal); NSDL/POLICY/2024/0048 of 18 Apr 2024 (by the 10th of the following month)",
		ToConfirm: "Whether BSE's module expects a nil entry, and how CDSL takes the report (communiqué DP2024-234 not read).",
		Checks:    "The latest month whose due date has passed: the report to BSE for each RA and IA registration (or its nil check), and to NSDL or CDSL when the DP was impersonated.",
		Fix:       "Report the month's cases on BSE's portal or to the depository, or record the nil check, on Investor complaints → Impersonation.",
		Screen:    "helpdesk", ScreenName: "Compliance → Investor complaints", Real: true, run: siImpReports},
	// From the penalty audit (selfinspect_more.go, firmdemats.go).
	{ID: "holding_account_types", Title: "Firm's demat accounts tagged and in the holding statement with their type", Area: "Compliance", Severity: "high", Owner: RoleCompliance,
		Source:    "NSE/INSP/55039 of 28 Dec 2022 and NSE/INSP/55380 (holding statement account types, as NSE's Inspection master circular NSE/INSP/73976 spells them: POOL, CM COLL, OWN, EPI, CLNTBENE-MTF, TMCOLL, TMCOLL-MTF, CLNT UNPAID, CLNT COLL, MRGN PLEDGE, MTF PLEDGE, CLNTBENE; security types EQ, MF, BOND, DEBT, PREF, IDR, WT, COMM); NSE FAQs on margin collection (Exhibit 17 of the master circular: margin-pledged securities not reported, invoked securities and the POOL, EPI, CUSPA and OWN accounts reported); NSE/INSP/65693 of 19 Dec 2024 (incorrect holding statements); SEBI adjudication order against Axis Securities of 21 Feb 2025 (holdings left out of the holding statement)",
		ToConfirm: "The API specification (NSE/INSP/55380 Annexure A) prints CLNTBENEMTF where the master circular prints CLNTBENE-MTF: Ledgerline sends the master circular's spelling (ask NSE if an MTF account is rejected with code 902). REIT and InvIT units have no type of their own in NSE's list and go as EQ.",
		Checks:    "The demat accounts in CDSL's last statement of holding that belong to no client: each tagged with its holding statement account type; each tagged account that holds securities reported under its type; no client's own account tagged as anything but OWN (its pledged balance would be margin collateral, which is not reported); and every ISIN's security type taken from CDSL's ISIN master.",
		Fix:       "Tag each of the firm's demat accounts in Settings → Firm demat accounts (or link a client's account to the client); report the holdings of tagged accounts in the holding statement.",
		Screen:    "settings", ScreenName: "Settings → Settings (Firm demat accounts)", Real: true, run: siHoldingTypes},
	{ID: "dealer_terminals", Title: "Trades only from registered terminals, by NISM-certified dealers", Area: "Operations", Severity: "high", Owner: RoleCompliance,
		Source: "NSE/INSP/73503 (9/2026) of 27 Mar 2026, Annexure A-B items 42 to 44 (terminals not operated by approved users; terminal details not uploaded or wrong); NSE/INSP/65693 of 19 Dec 2024 (terminals); SEBI's NISM Series VIII requirement for approved users in equity derivatives",
		Checks: "Trades of the last 30 days that carry a terminal ID: the terminal on a dealer's record on the trade date, and the dealer's NISM certificate valid that day.",
		Fix:    "Record the terminal and its user on Dealers & terminals (and upload it to the exchange); renew the dealer's certificate or close the terminal.",
		Screen: "dealers", ScreenName: "Branches & partners → Dealers & terminals", Real: true, run: siDealerTerminals},
	{ID: "minor_trading", Title: "No derivatives or intraday trading in a minor's account", Area: "Clients", Severity: "high", Owner: RoleCompliance,
		Source:    "NSE/INSP/73503 (9/2026) of 27 Mar 2026, Annexure A-B item 36 (trading allowed in a minor's account)",
		ToConfirm: "Which trades a minor's account may make is from the exchanges' practice (delivery-based cash trades through the guardian only); confirm against the KYC master circular.",
		Checks:    "Trades of the last 30 days by clients under 18 on the trade date (from the date of birth): derivatives, or the same scrip bought and sold the same day; and active minors with a segment other than cash.",
		Fix:       "Stop the segment on the client and on the trading front end; review the trades with the guardian and the compliance officer.",
		Screen:    "clients", ScreenName: "Clients & onboarding → Clients", Real: true, run: siMinorTrading},
	{ID: "kyc_before_trade", Title: "No trading before KYC is complete and validated", Area: "Clients", Severity: "high", Owner: RoleCompliance,
		Source:    "SEBI/HO/MIRSD/FATF/P/CIR/2023/0144 of 11 Aug 2023 (a client whose six KYC attributes are not validated by the KRA does not transact); NSE/INSP/73503 (9/2026), Annexure A-B item 5; the exchanges' PAN-Aadhaar seeding checks on the UCC",
		ToConfirm: "The PAN-Aadhaar link is read from NSE's PAN status answer (aadhaar seeding) and checked for individuals only.",
		Checks:    "Clients who traded in the last 30 days: KRA status validated or registered; name, PAN, address, mobile, e-mail and income range on record; PAN verified and, for individuals, linked with Aadhaar at NSE.",
		Fix:       "Block the client until the KYC is complete and the KRA validates it; complete the details with a change request and upload to the KRA.",
		Screen:    "kyc", ScreenName: "Compliance → KYC & PMLA", Real: true, run: siKYCBeforeTrade},
	{ID: "shared_contacts", Title: "No mobile or e-mail shared by clients who are not one family", Area: "Clients", Severity: "medium", Owner: RoleChecker,
		Source:    "NSE/INSP/73503 (9/2026) of 27 Mar 2026, Annexure A-B item 47 (the same e-mail or mobile for several clients not of one family, against SEBI's circulars of 2011 and 3 Dec 2024)",
		ToConfirm: "The family is taken as SEBI's KYC form has it (spouse, dependent children, dependent parents), declared on the client's UCC details.",
		Checks:    "Active clients sharing a mobile number or e-mail address: at most one holds it as their own; the others are declared as the holder's spouse, dependent child or dependent parent.",
		Fix:       "Get each client's own mobile and e-mail (change request), or record the family declaration on the client's UCC details.",
		Screen:    "clients", ScreenName: "Clients & onboarding → Clients", Real: true, run: siSharedContacts},
	{ID: "payout_one_day", Title: "Funds paid out within one working day of the client's request", Area: "Money", Severity: "high", Owner: RoleChecker,
		Source:    "SEBI/MIRSD/SE/Cir-19/2009 of 3 Dec 2009 (funds paid to the client within one working day); NSE/INSP/73503 (9/2026), Annexure A-B items 23 and 13 (client funds not settled; evidence of payout requests)",
		ToConfirm: "The circular sets one working day from the pay-out; Ledgerline applies it to each payout request from the day it is made.",
		Checks:    "Payout requests of the last 30 days (not rejected or cancelled): paid in a batch by the next working day.",
		Fix:       "Pay the request on Receipts & payouts, or reject it with the reason (insufficient free balance) and tell the client.",
		Screen:    "money", ScreenName: "Receipts & payouts", Real: true, run: siPayoutOneDay},
	{ID: "margin_statements", Title: "Daily margin statements sent by the next trading day", Area: "Clients", Severity: "medium", Owner: RoleMaker,
		Source:    "NSE/INSP/73503 (9/2026) of 27 Mar 2026, Annexure A-B item 24, and NSE/INSP/65693 of 19 Dec 2024 (daily margin statements not issued, issued late or incomplete)",
		ToConfirm: "The next trading day is the exchanges' inspection practice for when a day's statement is due; confirm against the circular.",
		Checks:    "The last five trading days with margins whose next trading day has passed: each client in the day's margin report sent the daily margin statement by then.",
		Fix:       "Send the day's statements on Client documents → Dispatch register (Send statements: margin); correct bounced e-mails.",
		Screen:    "dispatch", ScreenName: "Client documents → Dispatch register", Real: true, run: siMarginStatements},
	{ID: "ras_statements", Title: "Statement of each running account settlement sent, retention included", Area: "Money", Severity: "medium", Owner: RoleMaker,
		Source:    "NSE/INSP/73503 (9/2026) of 27 Mar 2026, Annexure A-B items 26 to 28 (retention statements and statements of account not sent, sent late or wrong at settlement); SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197 of 28 Dec 2023",
		ToConfirm: "Five working days after the settlement is the firm's practice for when the statement is due; confirm against the circular.",
		Checks:    "Running account settlements of the last 60 days whose five working days have passed: each client paid or settled by retention sent the settlement's statement.",
		Fix:       "Send the settlement's statements on Client documents → Dispatch register (Send statements: running account).",
		Screen:    "dispatch", ScreenName: "Client documents → Dispatch register", Real: true, run: siRASStatements},
	{ID: "duties_overdue", Title: "No compliance calendar duty overdue", Area: "Compliance", Severity: "medium", Owner: RoleCompliance,
		Source: "The compliance calendar's duties, each with its own circular (Circulars & duties)",
		Checks: "Active duties past their due date and not marked done (running account settlement is its own test).",
		Fix:    "Do the duty and mark it done with its evidence on Circulars & duties.",
		Screen: "circulars", ScreenName: "Calendar & circulars → Circulars & duties", Real: true, run: siDuties},
}

// ---- money ----

func siFunds(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Ledger balances", e.date)
	f, err := e.o.Funds(e.ctx, e.tenant, e.date)
	if err != nil {
		return s, err
	}
	held := f.ClientBank.Add(f.WithCC).Sub(f.Suspense)
	s.checked = 1
	s.summary = fmt.Sprintf("Held ₹%s (client bank ₹%s, with the clearing corporation ₹%s, less unmatched credits ₹%s) against client credit balances of ₹%s: cover %.2f×.",
		formatINR(held), formatINR(f.ClientBank), formatINR(f.WithCC), formatINR(f.Suspense), formatINR(f.ClientCredits), f.Cover)
	if !f.OK {
		s.bad(fmt.Sprintf("Short by ₹%s on %s", formatINR(f.ClientCredits.Sub(held)), fmtDay(e.date)))
	}
	return s, nil
}

func siRAS(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Running accounts", e.date)
	s.input("RAS calendar", e.date)
	duties, err := e.o.Duties(e.ctx, e.tenant, e.date)
	if err != nil {
		return s, err
	}
	missed := 0
	for _, d := range duties {
		if (d.ID != "OB-RAS" && d.ID != "OB-RAS-M") || !d.Active {
			continue
		}
		s.checked++
		if d.Overdue {
			missed++
			s.bad(fmt.Sprintf("%s: the settlement due %s has no run", d.ID, fmtDay(d.NextDue)))
		}
		if strings.HasPrefix(d.Warning, "No ") {
			s.bad(d.ID + ": " + d.Warning)
		}
	}
	v, err := e.o.RAS(e.ctx, e.tenant, e.date)
	if err != nil {
		return s, err
	}
	late := 0
	for _, r := range v.Rows {
		s.checked++
		if r.Settled || r.Payout.Round(money.UnitPaisa) <= 0 || r.SettlesOn == "" || r.SettlesOn >= e.date {
			continue
		}
		late++
		if r.Idle30 {
			s.bad(fmt.Sprintf("%s · no transaction for 30 days; ₹%s to return by %s", siClient(r.UCC), formatINR(r.Payout), fmtDay(r.IdleDue)))
		} else {
			s.bad(fmt.Sprintf("%s · ₹%s due on %s (%s) not settled", siClient(r.UCC), formatINR(r.Payout), fmtDay(r.SettlesOn), strings.ToLower(firstOf(r.Preference, "quarterly"))))
		}
	}
	s.summary = fmt.Sprintf("%d credit balances checked; %d not settled on their date; %d calendar dates missed.", len(v.Rows), late, missed)
	return s, nil
}

// ---- unpaid securities ----

func siCUSPALimit(e *siEnv) (siOut, error) {
	var s siOut
	prev := e.set.PrevBusinessDay(e.date)
	s.input("CUSPA ageing", e.date)
	s.input("CUSPA ageing", prev)
	now, err := e.o.CUSPAAgeing(e.ctx, e.tenant, e.date)
	if err != nil {
		return s, err
	}
	before, err := e.o.CUSPAAgeing(e.ctx, e.tenant, prev)
	if err != nil {
		return s, err
	}
	over := map[string]bool{}
	for _, r := range before.Rows {
		if r.Qty > 0 && r.ReleaseQty > 0 {
			over[r.ID] = true
		}
	}
	today := 0
	for _, r := range now.Rows {
		if r.Qty <= 0 {
			continue
		}
		s.checked++
		switch {
		case r.ReleaseQty > 0 && over[r.ID]:
			s.bad(fmt.Sprintf("CUSPA line %s · %s · over the limit since %s; release %d", r.ID, siClient(r.UCC), fmtDay(prev), r.ReleaseQty))
		case r.ReleaseQty > 0:
			today++
		}
	}
	s.summary = fmt.Sprintf("%d lines held; %d over the limit since %s and not released; %d over it today, to release by %s.", s.checked, s.found, fmtDay(prev), today, fmtDay(e.set.NextBusinessDay(e.date)))
	return s, nil
}

func siCUSPANotice(e *siEnv) (siOut, error) {
	var s siOut
	s.input("CUSPA lines", e.date)
	items, err := docs.List[CUSPAItem](e.ctx, e.o.Docs, e.tenant, cCUSPA, "")
	if err != nil {
		return s, err
	}
	for _, it := range items {
		if it.Invoked <= 0 || it.Date > e.date {
			continue
		}
		s.checked++
		inv, notice := it.InvokedOn, it.NoticeOn
		for _, ev := range it.Events {
			switch ev.What {
			case "Invoked":
				if inv == "" || ev.Date < inv {
					inv = ev.Date
				}
			case "Notice":
				if notice == "" || ev.Date < notice {
					notice = ev.Date
				}
			}
		}
		switch {
		case notice == "":
			s.bad(fmt.Sprintf("CUSPA line %s · %s · invoked on %s with no notice on record", it.ID, siClient(it.UCC), fmtDay(inv)))
		case inv != "" && notice > inv:
			s.bad(fmt.Sprintf("CUSPA line %s · %s · notice on %s, after the invocation on %s", it.ID, siClient(it.UCC), fmtDay(notice), fmtDay(inv)))
		}
	}
	s.summary = fmt.Sprintf("%d invoked lines checked; %d without a notice before invocation.", s.checked, s.found)
	return s, nil
}

func siCUSPADay6(e *siEnv) (siOut, error) {
	var s siOut
	s.input("CUSPA ageing", e.date)
	v, err := e.o.CUSPAAgeing(e.ctx, e.tenant, e.date)
	if err != nil {
		return s, err
	}
	for _, r := range v.Rows {
		if r.Qty <= 0 {
			continue
		}
		s.checked++
		if r.Day > cuspaAutoDays {
			s.bad(fmt.Sprintf("CUSPA line %s · %s · day %d after pay-out, %d still held; the depository released it at the end of %s", r.ID, siClient(r.UCC), r.Day, r.Qty, fmtDay(r.AutoRelease)))
		}
	}
	s.summary = fmt.Sprintf("%d lines held; %d past the 6th trading day after pay-out.", s.checked, s.found)
	return s, nil
}

// ---- risk ----

func siPenalties(e *siEnv) (siOut, error) {
	var s siOut
	// The penalties of the three trading days before the date (a day's
	// margin files arrive that evening and the next morning).
	d := e.date
	for i := 0; i < 3; i++ {
		d = e.set.PrevBusinessDay(d)
		rep, err := e.o.Margins(e.ctx, e.tenant, d)
		if err != nil {
			continue // no margin data for the day
		}
		s.input("Margin reporting", d)
		short := 0
		for _, m := range rep.Rows {
			if m.Penalty.Round(money.UnitPaisa) > 0 {
				short++
			}
		}
		if short == 0 {
			continue
		}
		s.checked++
		if _, _, err := docs.Get[struct{ Posted bool }](e.ctx, e.o.Docs, e.tenant, cSnapshotPx, "penalty|"+d); err != nil {
			s.bad(fmt.Sprintf("Penalties of %s (%d clients short) not worked out", fmtDay(d), short))
		}
	}
	passes, err := e.o.PenaltyPasses(e.ctx, e.tenant, "")
	if err != nil {
		return s, err
	}
	from := addDays(e.date, -90)
	posted := map[string]bool{}
	for _, p := range passes {
		if p.Status != "Posted" || p.Date < from || p.Date > e.date {
			continue
		}
		posted[p.Entry] = true
		s.checked++
		var why []string
		if PenaltyReasons[p.Reason] == "" {
			why = append(why, "the reason is not one the rule allows")
		}
		if len(p.Evidence) == 0 {
			why = append(why, "no evidence")
		}
		if p.Checker == "" || p.Checker == p.Maker {
			why = append(why, "not approved by a second person")
		}
		if p.Date < PenaltyPassFrom {
			why = append(why, "before the rule allowed any")
		}
		if len(why) > 0 {
			s.bad(fmt.Sprintf("Pass %s · %s · %s", p.ID, siClient(p.UCC), strings.Join(why, "; ")))
		}
	}
	s.input("Penalties passed on", from)
	es, err := e.o.App.Ledger.Store.List(e.ctx, e.tenant, ledger.Filter{From: from, To: e.date})
	if err != nil {
		return s, err
	}
	for _, en := range es {
		if en.Source.Kind != "PENALTY" || en.ReversalOf != "" {
			continue
		}
		s.checked++
		if !posted[en.ID] {
			ucc := ""
			for _, p := range en.Postings {
				if u, _, ok := ledger.ClientCode(p.Account); ok && p.Debit > 0 {
					ucc = u
				}
			}
			s.bad(fmt.Sprintf("Entry %s · %s · a penalty debited to the client with no approved pass", en.ID, siClient(ucc)))
		}
	}
	s.summary = fmt.Sprintf("%d days with penalties, passes and penalty entries checked; %d problems.", s.checked, s.found)
	return s, nil
}

// ---- operations ----

// siSessionClose is when a part's session closes, the start of a note's 24
// hours at the latest.
var siSessionClose = map[string]string{PartEquity: "15:30", PartCommodity: "23:55"}

func siNotes(e *siEnv) (siOut, error) {
	var s siOut
	d := e.date
	days := 0
	for i := 0; i < 8 && days < 5; i++ {
		if i > 0 || !e.set.IsBusinessDay(d) {
			d = e.set.PrevBusinessDay(d)
		}
		deadline, err := time.ParseInLocation("2006-01-02 15:04", addDays(d, 1)+" "+siSessionClose[PartEquity], ist)
		if err != nil || deadline.After(e.now) {
			continue // the day's 24 hours are not over
		}
		trades, err := e.o.exchangeTrades(e.ctx, e.tenant, d)
		if err != nil {
			return s, err
		}
		s.input("Trades and contract notes", d)
		if len(trades) == 0 {
			continue
		}
		days++
		notes, err := e.o.Notes(e.ctx, e.tenant, d)
		if err != nil {
			return s, err
		}
		have := map[string]ContractNote{}
		for _, n := range notes {
			if n.Replaces != "" && have[n.UCC+"|"+n.part()].No != "" {
				continue
			}
			have[n.UCC+"|"+n.part()] = n
		}
		seen := map[string]bool{}
		keys := []string{}
		for _, t := range trades {
			k := t.UCC + "|" + PartOf(t.Exchange, t.Segment)
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			ucc, part, _ := strings.Cut(k, "|")
			s.checked++
			n, ok := have[k]
			due, _ := time.ParseInLocation("2006-01-02 15:04", addDays(d, 1)+" "+firstOf(siSessionClose[part], "15:30"), ist)
			switch {
			case !ok:
				s.bad(fmt.Sprintf("Trades of %s · %s · no contract note", fmtDay(d), siClient(ucc)))
			case n.Status == "Paper":
			case n.Status != "Sent":
				s.bad(fmt.Sprintf("Note %s · %s · %s", n.No, siClient(ucc), strings.ToLower(firstOf(n.Status, "not sent"))))
			default:
				if at := bsTime(n.SentAt); !at.IsZero() && at.After(due) {
					s.bad(fmt.Sprintf("Note %s · %s · sent %s, after %s", n.No, siClient(ucc), at.In(ist).Format("02 Jan 15:04"), due.Format("02 Jan 15:04")))
				}
			}
		}
	}
	s.summary = fmt.Sprintf("%d notes due on %d trading days; %d not sent within 24 hours.", s.checked, days, s.found)
	return s, nil
}

func siFiles(e *siEnv) (siOut, error) {
	var s siOut
	since := siFirstTrade(e)
	if since == "" {
		return siSkip("Nothing to check: no trades in Ledgerline yet.")
	}
	for _, d := range []string{e.set.PrevBusinessDay(e.date), e.date} {
		if d < since {
			continue
		}
		cl, err := e.o.DailyFileChecklist(e.ctx, e.tenant, d)
		if err != nil {
			return s, err
		}
		s.input("File checklist", d)
		for _, it := range cl.Items {
			if it.Status == "Intraday" || it.Status == "Check by hand" {
				continue
			}
			s.checked++
			if it.Status == "Missing" {
				s.bad(fmt.Sprintf("%s · for %s · due %s", it.Title, fmtDay(d), it.Due))
			}
		}
	}
	s.summary = fmt.Sprintf("%d expected files checked; %d missing past their time.", s.checked, s.found)
	return s, nil
}

// ---- complaints, alerts and duties ----

func siComplaints(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Investor complaints", e.date)
	ts, err := e.o.Tickets(e.ctx, e.tenant, e.date)
	if err != nil {
		return s, err
	}
	for _, t := range ts {
		if t.Kind != KindComplaint || t.Status == "Resolved" || t.Status == "Closed" || t.OpenedOn > e.date {
			continue
		}
		s.checked++
		if age := daysBetween(t.OpenedOn, e.date); age > ticketTAT(KindComplaint) {
			s.bad(fmt.Sprintf("%s · %s · open %d days%s", t.ID, siClient(t.UCC), age, map[bool]string{true: " (SCORES " + t.Ref + ")", false: ""}[t.Ref != "" && strings.Contains(t.Source, "SCORES")]))
		}
	}
	s.summary = fmt.Sprintf("%d complaints open; %d older than 21 days.", s.checked, s.found)
	return s, nil
}

func siAlerts(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Alerts", e.date)
	as, err := e.o.Alerts(e.ctx, e.tenant)
	if err != nil {
		return s, err
	}
	for _, a := range as {
		if a.Status == "Closed" || a.Status == "STR filed" || a.Date > e.date {
			continue
		}
		if l, _, err := docs.Get[AlertLife](e.ctx, e.o.Docs, e.tenant, cAlertLife, a.ID); err == nil && l.ReferredOn != "" {
			continue // referred to the exchange: decided
		}
		s.checked++
		if age := daysBetween(a.Date, e.date); age > siAlertDays {
			s.bad(fmt.Sprintf("%s · %s · %s · open %d days", a.ID, a.Type, siClient(a.UCC), age))
		}
	}
	s.summary = fmt.Sprintf("%d alerts open; %d older than %d days.", s.checked, s.found, siAlertDays)
	return s, nil
}

func siDuties(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Compliance calendar", e.date)
	ds, err := e.o.Duties(e.ctx, e.tenant, e.date)
	if err != nil {
		return s, err
	}
	for _, d := range ds {
		if !d.Active || strings.HasPrefix(d.ID, "OB-RAS") {
			continue
		}
		s.checked++
		if d.Overdue {
			s.bad(fmt.Sprintf("%s · %s · due %s", d.ID, clip(d.Title, 90), fmtDay(d.NextDue)))
		}
	}
	s.summary = fmt.Sprintf("%d duties on the calendar; %d overdue.", s.checked, s.found)
	return s, nil
}

// ---- payouts and receipts ----

// siWasRegistered reports whether account (and IFSC) was a registered bank
// account of the client on day.
func siWasRegistered(c Client, account, ifsc, day string) bool {
	account, ifsc = strings.TrimSpace(account), strings.ToUpper(strings.TrimSpace(ifsc))
	for _, b := range registeredBanks(c) {
		if b.Account != account || (ifsc != "" && b.IFSC != "" && strings.ToUpper(b.IFSC) != ifsc) {
			continue
		}
		if b.AddedOn != "" && dayOf(b.AddedOn) > day {
			continue
		}
		if b.active() || (b.InactiveOn != "" && dayOf(b.InactiveOn) >= day) {
			return true
		}
	}
	return false
}

func siPayouts(e *siEnv) (siOut, error) {
	var s siOut
	from := addDays(e.date, -siWindow)
	s.input("Payout batches", from)
	bs, err := e.o.PayoutBatches(e.ctx, e.tenant)
	if err != nil {
		return s, err
	}
	for _, b := range bs {
		if b.Date < from || b.Date > e.date {
			continue
		}
		for _, it := range b.Items {
			s.checked++
			if !siWasRegistered(e.clients[strings.ToUpper(it.UCC)], it.Account, it.IFSC, b.Date) {
				s.bad(fmt.Sprintf("Payout %s · %s · paid to %s, not a registered account", it.Request, siClient(it.UCC), MaskAccount(it.Account)))
			}
		}
	}
	s.input("Running account settlements", from)
	rbs, err := docs.List[RASBatch](e.ctx, e.o.Docs, e.tenant, cRASBatch, "")
	if err != nil {
		return s, err
	}
	for _, b := range rbs {
		if b.Date < from || b.Date > e.date {
			continue
		}
		for _, it := range b.Items {
			s.checked++
			if !siWasRegistered(e.clients[strings.ToUpper(it.UCC)], it.Account, it.IFSC, b.Date) {
				s.bad(fmt.Sprintf("Settlement of %s · %s · paid to %s, not a registered account", fmtDay(b.Date), siClient(it.UCC), MaskAccount(it.Account)))
			}
		}
	}
	s.summary = fmt.Sprintf("%d payouts of the last %d days checked; %d to an account the client had not registered.", s.checked, siWindow, s.found)
	return s, nil
}

func siThirdParty(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Bank statement credits", e.date)
	ts, err := e.o.BankTxns(e.ctx, e.tenant)
	if err != nil {
		return s, err
	}
	parked := 0
	for _, t := range ts {
		if (t.Status != "Third-party" && t.Status != "Unidentified") || t.Date > e.date {
			continue
		}
		s.checked++
		parked++
		if n := businessDaysBetween(e.set, t.Date, e.date); n > siParkDays {
			s.bad(fmt.Sprintf("Credit %s · ₹%s from %s (%s) · parked %d working days", t.ID, formatINR(t.Amount.Dec()), MaskAccount(t.Account), strings.ToLower(t.Status), n))
		}
	}
	from := addDays(e.date, -siWindow)
	s.input("Receipts", from)
	rs, err := e.o.Receipts(e.ctx, e.tenant)
	if err != nil {
		return s, err
	}
	for _, r := range rs {
		if (r.Status != "Credited" && r.Status != "Uncleared") || r.Date < from || r.Date > e.date || r.Mode == "UPI" || r.Mode == "Gateway" || r.FromAccount == "" {
			continue
		}
		s.checked++
		if !siWasRegistered(e.clients[strings.ToUpper(r.UCC)], r.FromAccount, "", r.Date) {
			s.bad(fmt.Sprintf("Receipt %s · %s · from %s, not a registered account", r.ID, siClient(r.UCC), MaskAccount(r.FromAccount)))
		}
	}
	s.summary = fmt.Sprintf("%d credits from unregistered accounts parked; %d receipts and parked credits checked; %d problems.", parked, s.checked, s.found)
	return s, nil
}

// ---- clients ----

func siKYC(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Clients", e.date)
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if c.Status != "Active" {
			continue
		}
		s.checked++
		var why []string
		if c.KRAStatus != "Validated" && c.KRAStatus != "Registered" {
			why = append(why, "KRA "+strings.ToLower(firstOf(c.KRAStatus, "status not loaded")))
		}
		if strings.TrimSpace(c.PAN) == "" {
			why = append(why, "no PAN")
		}
		if len(c.Incomplete) > 0 {
			why = append(why, "missing "+strings.ToLower(strings.Join(c.Incomplete, ", ")))
		}
		deriv := false
		for _, g := range c.Segments {
			deriv = deriv || g == "FO" || g == "CD" || g == "COM"
		}
		if deriv && !c.IncomeProof {
			why = append(why, "no income proof for derivatives")
		}
		if len(why) > 0 {
			s.bad(siClient(u) + " · " + strings.Join(why, "; "))
		}
	}
	s.summary = fmt.Sprintf("%d active clients checked; %d with KYC incomplete.", s.checked, s.found)
	return s, nil
}

func siInactive(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Account activity", e.date)
	v, err := e.o.Dormancy(e.ctx, e.tenant, e.date)
	if err != nil {
		return s, err
	}
	last, err := e.o.lastTrades(e.ctx, e.tenant)
	if err != nil {
		return s, err
	}
	inactive, assets := 0, 0
	for _, r := range v.Rows {
		s.checked++
		c := e.clients[r.UCC]
		switch {
		case r.State == "Due":
			s.bad(fmt.Sprintf("%s · last activity %s; inactive from %s, not flagged", siClient(r.UCC), firstOf(fmtDay(r.LastActivity), "none on record"), firstOf(fmtDay(r.InactiveFrom), "—")))
		case c.Inactive:
			inactive++
			if r.Funds.Sign() != 0 || r.Holdings > 0 {
				assets++
			}
			if lt := last[r.UCC]; lt != "" && c.InactiveSince != "" && lt > c.InactiveSince && (c.ReactivatedOn == "" || c.ReactivatedOn > lt) {
				s.bad(fmt.Sprintf("%s · traded on %s while inactive since %s", siClient(r.UCC), fmtDay(lt), fmtDay(c.InactiveSince)))
			}
		}
	}
	s.summary = fmt.Sprintf("%d accounts checked against %d months; %d inactive (%d with funds or securities, settled through running account settlement); %d problems.", s.checked, v.Months, inactive, assets, s.found)
	return s, nil
}

// siUCCReg is the part of an exchange UCC registration the test reads
// (NSE, BSE and MCX keep theirs in one collection).
type siUCCReg struct {
	Exchange    string            `json:"exchange"`
	UCC         string            `json:"ucc"`
	Status      string            `json:"status"`
	Reason      string            `json:"reason"`
	PANStatus   string            `json:"pan_status"`
	TradeStatus map[string]string `json:"trade_status"`
}

func siUCC(e *siEnv) (siOut, error) {
	regs, err := docs.List[siUCCReg](e.ctx, e.o.Docs, e.tenant, cUCCReg, "")
	if err != nil {
		return siOut{}, err
	}
	if len(regs) == 0 {
		return siSkip("No answers from the exchanges' UCC systems yet: send the clients by the UCC APIs or load the response files on Exchange UCC.")
	}
	var s siOut
	s.input("Exchange UCC answers", e.date)
	sort.SliceStable(regs, func(i, j int) bool { return regs[i].Exchange+regs[i].UCC < regs[j].Exchange+regs[j].UCC })
	for _, r := range regs {
		c, ok := e.clients[strings.ToUpper(r.UCC)]
		if !ok || c.Status != "Active" || c.DPOnly {
			continue
		}
		s.checked++
		var why []string
		switch r.Status {
		case "Rejected", "Needs fixing":
			why = append(why, strings.ToLower(r.Status)+map[bool]string{true: ": " + clip(r.Reason, 80), false: ""}[r.Reason != ""])
		}
		switch r.PANStatus {
		case "I", "Invalid":
			why = append(why, "PAN not verified by the exchange")
		}
		for _, seg := range sortedKeys(r.TradeStatus) {
			if strings.HasPrefix(strings.ToLower(r.TradeStatus[seg]), "not permitted") {
				why = append(why, seg+" not permitted to trade")
			}
		}
		if len(why) > 0 {
			s.bad(fmt.Sprintf("%s · %s · %s", r.Exchange, siClient(r.UCC), strings.Join(why, "; ")))
		}
	}
	s.summary = fmt.Sprintf("%d exchange UCC records of active clients checked; %d do not match.", s.checked, s.found)
	return s, nil
}

func siDebarred(e *siEnv) (siOut, error) {
	list, err := docs.List[Debarred](e.ctx, e.o.Docs, e.tenant, cDebarred, "")
	if err != nil {
		return siOut{}, err
	}
	withPAN := list[:0]
	for _, d := range list {
		if d.PAN != "" {
			withPAN = append(withPAN, d)
		}
	}
	if len(withPAN) == 0 {
		return siSkip("Waiting for data: no debarred list with PANs is loaded (KYC & PMLA, or BSE's DEBR file on Exchange files).")
	}
	var s siOut
	from := addDays(e.date, -siWindow)
	s.input("Debarred list", e.date)
	s.input("Trades", from)
	on := func(pan, day string) (Debarred, bool) {
		for _, d := range withPAN {
			if strings.EqualFold(d.PAN, pan) && (d.OrderDate == "" || d.OrderDate <= day) && (d.Till == "" || d.Till >= day) {
				return d, true
			}
		}
		return Debarred{}, false
	}
	trades, err := e.o.App.Trades.List(e.ctx, e.tenant, ingest.FromExchange, "")
	if err != nil {
		return s, err
	}
	seen := map[string]bool{}
	for _, t := range trades {
		if t.TradeDate < from || t.TradeDate > e.date {
			continue
		}
		s.checked++
		c := e.clients[strings.ToUpper(t.UCC)]
		if c.PAN == "" || seen[t.UCC+"|"+t.TradeDate] {
			continue
		}
		if d, ok := on(c.PAN, t.TradeDate); ok {
			seen[t.UCC+"|"+t.TradeDate] = true
			s.bad(fmt.Sprintf("%s · traded on %s while debarred (%s %s of %s)", siClient(t.UCC), fmtDay(t.TradeDate), d.Regulator, d.OrderNo, fmtDay(d.OrderDate)))
		}
	}
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if c.Status != "Active" || c.PAN == "" {
			continue
		}
		s.checked++
		if d, ok := on(c.PAN, e.date); ok && !c.Blocked {
			s.bad(fmt.Sprintf("%s · on %s %s, not blocked", siClient(u), d.Regulator, d.OrderNo))
		}
	}
	s.summary = fmt.Sprintf("%d trades and active clients checked against %d debarments; %d problems.", s.checked, len(withPAN), s.found)
	return s, nil
}

// ---- reports to the exchanges ----

// siFirstTrade is the first trade date in Ledgerline: days before it are
// before the firm's data, and are not checked.
func siFirstTrade(e *siEnv) string {
	if e.first != nil {
		return *e.first
	}
	ts, _ := e.o.App.Trades.List(e.ctx, e.tenant, ingest.FromExchange, "")
	first := ""
	for _, t := range ts {
		if first == "" || t.TradeDate < first {
			first = t.TradeDate
		}
	}
	e.first = &first
	return first
}

func siHolding(e *siEnv) (siOut, error) {
	exs := memberExchanges(e.set)
	nse := contains(exs, "NSE")
	bse, why := bseHoldingApplies(e.set, e.date)
	if !nse && !bse {
		return siSkip("Not applicable: " + firstOf(why, "the firm has no NSE or BSE member code in Settings."))
	}
	since := siFirstTrade(e)
	if since == "" {
		return siSkip("Nothing to check: no trades in Ledgerline yet.")
	}
	var s siOut
	if bse && !nse {
		s.input("BSE holding statements", e.date)
		for _, d := range e.o.bseHoldingPending(e.ctx, e.tenant, e.set, e.date) {
			if d >= since && d >= addDays(e.date, -7) && d < e.date {
				s.checked++
				s.bad(fmt.Sprintf("Data day %s · not recorded as uploaded on BEFS", fmtDay(d)))
			}
		}
		s.summary = fmt.Sprintf("%d days of the last week not uploaded to BSE.", s.found)
		return s, nil
	}
	for d := addDays(e.date, -7); d < e.date; d = addDays(d, 1) {
		if d < since || !e.o.isHoldingDay(e.ctx, e.tenant, e.set, d) {
			continue
		}
		s.checked++
		s.input("Holding statement", d)
		subs, _ := docs.List[NSEHoldingSubmission](e.ctx, e.o.Docs, e.tenant, cNSEHSub, d+"|")
		state := ""
		for _, x := range subs {
			switch {
			case x.Status == "Accepted" || x.Status == hsNilDecl || x.Status == hsNilBulk:
				state = "Accepted"
			case (x.Status == "Partly accepted" || x.Status == hsNil) && state == "":
				state = x.Status
			}
		}
		if state == "" {
			if r, _, err := docs.Get[ReportStatus](e.ctx, e.o.Docs, e.tenant, cReport, d+"|holdings"); err == nil && r.Status == "Uploaded" {
				state = "Uploaded"
			}
		}
		switch state {
		case "":
			s.bad(fmt.Sprintf("Data day %s · not sent to NSE (no accepted submission or recorded upload)", fmtDay(d)))
		case "Partly accepted":
			s.bad(fmt.Sprintf("Data day %s · NSE accepted it only in part", fmtDay(d)))
		case hsNil:
			s.bad(fmt.Sprintf("Data day %s · nothing held, and NSE's one-time NIL Holding declaration is not recorded", fmtDay(d)))
		}
	}
	if s.checked == 0 {
		return siSkip("Nothing to check yet: the first data day in Ledgerline is not yet due (T+1).")
	}
	s.summary = fmt.Sprintf("%d data days of the last week checked; %d not sent in full.", s.checked, s.found)
	return s, nil
}

func siSEGMON(e *siEnv) (siOut, error) {
	var s siOut
	since := siFirstTrade(e)
	if since == "" {
		return siSkip("Nothing to check: no trades in Ledgerline yet.")
	}
	d, any := e.date, false
	for i := 0; i < 3; i++ {
		d = e.set.PrevBusinessDay(d)
		if d < since {
			break
		}
		cl, err := e.o.DailyFileChecklist(e.ctx, e.tenant, d)
		if err != nil {
			return s, err
		}
		for _, it := range cl.Items {
			if it.ID != "ncl-segmon" && it.ID != "ncl-cc01" {
				continue
			}
			any = true
			s.checked++
			if it.Status == "Missing" {
				s.bad(fmt.Sprintf("%s · for %s · not made (due %s)", it.Title, fmtDay(d), it.Due))
			}
		}
		if rs, err := e.o.SEGMONResponses(e.ctx, e.tenant, SystemActor, d); err == nil {
			for _, r := range rs {
				if r.Rejected > 0 {
					s.bad(fmt.Sprintf("SEGMON response %s · for %s · %d records rejected", r.Name, fmtDay(d), r.Rejected))
				}
			}
		}
		s.input("SEGMON and CC01 files", d)
	}
	if len(s.inputs) == 0 {
		return siSkip("Nothing to check yet: the first trading day in Ledgerline has not had its files due.")
	}
	if !any {
		return siSkip("Not applicable: none of the firm's segments is cleared through NSE Clearing.", s.inputs...)
	}
	s.summary = fmt.Sprintf("%d files of the last three trading days checked; %d problems.", s.checked, s.found)
	return s, nil
}

func siWeekly(e *siEnv) (siOut, error) {
	since := siFirstTrade(e)
	if since == "" {
		return siSkip("Nothing to check: no trades in Ledgerline yet.")
	}
	t, _ := periodicTask("funds_securities")
	var s siOut
	ds, _ := docs.List[Dispatch](e.ctx, e.o.Docs, e.tenant, cDispatch, "")
	sent := map[string]bool{}
	for _, d := range ds {
		if d.Kind == DocFundsSec {
			sent[d.Date] = true
		}
	}
	fri := lastFriday(e.date)
	for k := 0; k < 6 && s.checked < 2; k++ {
		due := addTradingDays(e.set, fri, 4)
		if due < e.date && fri >= since {
			s.checked++
			s.input("Weekly statements", fri)
			if done, _ := e.o.periodicDone(e.ctx, e.tenant, t, fri, e.date); !done && !sent[fri] {
				s.bad(fmt.Sprintf("Week %s to %s · not sent (due by %s)", fmtDay(addDays(fri, -6)), fmtDay(fri), fmtDay(due)))
			}
		}
		fri = addDays(fri, -7)
	}
	if s.checked == 0 {
		return siSkip("Nothing to check yet: no week since the first trade has passed its four trading days.", s.inputs...)
	}
	s.summary = fmt.Sprintf("%d weeks checked; %d not sent.", s.checked, s.found)
	return s, nil
}

// ---- depository ----

func siBSDA(e *siEnv) (siOut, error) {
	bos, _ := docs.List[BOAccount](e.ctx, e.o.Docs, e.tenant, cBO, "")
	if len(bos) == 0 && !e.o.cdslDPFirm(e.ctx, e.tenant, e.set) {
		return siSkip("Not applicable: the firm is not a depository participant in Ledgerline.")
	}
	t, _ := periodicTask("bsda_review")
	st := e.o.periodicStatus(e.ctx, e.tenant, t, e.date)
	var s siOut
	s.checked = 1
	s.input("BSDA review", st.Period)
	if st.Status == "Overdue" {
		s.bad(fmt.Sprintf("Review for %s · not applied (due %s)%s", st.Label, fmtDay(st.DueOn), map[bool]string{true: " · " + st.Note, false: ""}[st.Note != ""]))
	}
	s.summary = fmt.Sprintf("The BSDA review for %s: %s.", st.Label, strings.ToLower(st.Status))
	return s, nil
}

// siTestIDs lists the library's IDs, for the tests.
func siTestIDs() []string {
	var ids []string
	for _, t := range siTests {
		ids = append(ids, t.ID)
	}
	return ids
}
