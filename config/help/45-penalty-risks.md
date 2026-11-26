Ledgy help articles: where stock brokers and DPs get penalised (the exchanges' common inspection findings and recent penalty orders), and how Ledgerline covers each: by a self-inspection test, a control on its own screen or a compliance calendar duty, or not yet.

# Penalty risks

## penalty-risks
title: Penalty risks and how Ledgerline covers them
screen: selfinspect
roles: Compliance officer, Admin, Ops checker, Ops maker, Risk manager
ask: penalty risks; where do brokers get penalised; inspection findings; common violations nse; nse inspection observations; exchange penalty orders; sebi adjudication order broker; what will the inspector check; are we covered; compliance gaps; penalty audit; which penalties does ledgerline prevent; what is not covered; inspection readiness checklist
related: self-inspection, self-inspection-tests, holding-statement-firm-accounts, parallel-run

This table lists what the exchanges and SEBI most often find against stock brokers and DPs, and how Ledgerline covers each. Most rows have a [[selfinspect|Self-inspection]] test that runs every night.

The findings come from:

- NSE/INSP/73503 (circular 9/2026) of 27 March 2026, Annexure A-B: common violations in inspections from October 2024 to September 2025.
- NSE/INSP/65693 of 19 December 2024: the same for April to September 2024.
- SEBI adjudication orders against Axis Securities (21 February 2025) and Prabhudas Lilladher (16 July 2025).
- NSE's Member Committee order against Simandhar Broking (23 May 2025).
- The Profitmart order: mobiles differed from the KYC in 142 cases, e-mails in 140.
- An NSE order of 8 May 2024: 37 such cases.

**Covered**: a test or a control stops or finds it. **Partial**: part of it is checked; the rest is said below. **Gap**: not covered; the reason is given.

| Area | What gets penalised | Source | How Ledgerline covers it | Status |
|---|---|---|---|---|
| Running account settlement | Client funds not settled on the first Friday or Saturday; idle balances not returned | SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197; SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/1; NSE/INSP/73503 item 23 | Tests **ras_settlement** and **ras_ucc_report** (10 trading days, NSE/INSP/55024) | Covered |
| Retention at settlement | Retention statements wrong, not sent or late; no intimation of the transfer | NSE/INSP/73503 items 26 to 28; NSE/INSP/65693 | New test **ras_statements**. The statement now also goes to clients settled by retention alone (it did not before). The amount retained is worked out by the settlement; no separate test checks it against the cap | Partial |
| Payouts | Funds not paid within one working day; paid beyond the free balance | SEBI/MIRSD/SE/Cir-19/2009; NSE/INSP/73503 items 13 and 25 | New test **payout_one_day**. A request above the free balance is refused when it is made | Covered |
| Payouts and receipts | Paid to unregistered accounts; third-party money used | SEBI/MRD/SE/Cir-33/2003/27/08 | Tests **payouts_registered** and **third_party_receipts** | Covered |
| Contract notes | Issued late, not delivered, not signed | Investor charter (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/22); NSE/INSP/73503 item 35; Simandhar order | Test **contract_notes**. Notes are signed and logged; a bounced note is a failure until resent | Covered |
| Daily margin statements | Not issued, late or incomplete | NSE/INSP/73503 item 24; NSE/INSP/65693 | New test **margin_statements** (by the next trading day; to confirm) | Covered |
| Statements of account | Weekly statement of funds and securities (Monday to Saturday, every active client); annual global statement; statements at settlement | NSE/INSP/73976 items 4.5 and 4.17; NSE/INSP/48431 | Tests **weekly_statement** and **annual_statement**. Monthly ledger statements are sent from Dispatch but not tested | Partial |
| Margin collection | Short collection; collection reported wrongly to the exchange | NSE/INSP/73503 item 22; Simandhar order | Margin reporting files are made from Ledgerline's data. The parallel run checks them against the old system's. There is no independent check against bank and collateral | Partial |
| Margin penalties | Short-collection penalties passed to clients | NSE/INSP/64315 (69/2024); NSE/INSP/73503 item 30; Axis Securities order | Test **margin_penalties** | Covered |
| Client funds | Shortfall; client money used for the firm or other clients | SEBI/HO/MIRSD/MIRSD2/CIR/P/2016/95 para 3.3.1; Prabhudas Lilladher order | Test **funds_segregation** | Covered |
| Bank transfers | Transfers between USCNBA, DSCNBA and own accounts not allowed or not reconciled daily | Master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90 para 15.4.4.2; NSE/INSP/55904; NSE/INSP/73503 items 1 and 2 | Test **client_money_recon** | Covered |
| Upstreaming | Credit balances not upstreamed to the clearing corporation; "retained" balances found elsewhere | NSE/INSP/73503 items 48 to 50 | Compliance calendar duty CC-T-UPSTREAMING (test **duties_overdue**). Balances are not tested | Partial |
| Inactive accounts | Not flagged in the UCC; traded without reactivation | NSE/INSP/64718 (80/2024); NSE/INSP/73503 item 45 | Test **inactive_accounts** | Covered |
| KYC before trading | Six attributes not validated by the KRA; PAN not linked with Aadhaar; documents missing | SEBI/HO/MIRSD/FATF/P/CIR/2023/0144; NSE/INSP/73503 item 5 | Test **kyc_complete** (active clients) and new test **kyc_before_trade** (clients who traded) | Covered |
| KYC uploads | KYC not uploaded to the KRA in time; CKYC not uploaded | NSE/INSP/19654 (Simandhar order); NSE/INSP/73503 item 11 | FATCA/CRS tests cover the KRA upload of self-certifications. CKYC upload is not tracked | Partial |
| UCC details | Mobile and e-mail not as in the KYC; changes not sent to the exchange | NSE/ISC/73973; NSE/INSP/73503 item 46; Profitmart order; NSE order of 8 May 2024 | New test **ucc_fields**: compares the exchanges' UCC downloads and NSE's last accepted record, field by field | Covered |
| UCC status | Trades for UCCs not permitted to trade; PAN not verified | NSE/INSP/65693 | Test **ucc_match** | Covered |
| Shared contacts | One mobile or e-mail on clients who are not one family | NSE/INSP/73503 item 47 (SEBI circulars of 2011 and 3 Dec 2024) | New test **shared_contacts** | Covered |
| AP contacts and mapping | AP's mobile or e-mail on clients; clients mapped to APs not reported | NSE/COMP/63628; NSE/COMP/49509; NSE/INSP/73503 Annexure A items 5 and 13 | Tests **ap_contacts** and **ap_mapping_weekly** | Covered |
| Unpaid securities (CUSPA) | Fully paid or credit-balance clients' securities in CUSPA; no notice; kept too long | SEBI HO/38/11/(9)2026-MIRSD-POD/I/15382/2026; NSE/INSP/75508; Axis Securities and Prabhudas Lilladher orders | Tests **cuspa_limit**, **cuspa_notice** and **cuspa_day6**. The SMS or e-mail when the pledge is created, and invocation above the debit balance, are not tested | Partial |
| Client securities | Pledged other than by margin pledge; moved to the pool for other clients | SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2022/137; master circular | Pool and CUSPA balances are now in the holding statement (see the next row). The DDPI purpose test waits for data: the depository records do not say which debits were under a DDPI. Pool balances after pay-out are not tested | Partial |
| Holding statement | Holdings not matching the firm's demat accounts; pool and CUSPA left out after a software change; DP account tagging not done | NSE/INSP/55039; NSE/INSP/55380; NSE/INSP/65693; NSE/INSP/73503 item 4; Axis Securities order | **Ledgerline's first version reported clients' holdings as OWN.** That reported clients' securities as the firm's own and left out pool and collateral accounts. Now the statement is the firm's tagged accounts from CDSL's statement of holding, checked before sending (it is not made while an account is untagged). Tests **holding_statement** and **holding_account_types** | Covered |
| Client collateral | SEGMON data wrong or late | SEBI/HO/MRD2_DCAP/CIR/2021/0598; NSE/INSP/73503 item 39 | Test **segmon** (files made, rejections) | Covered |
| Terminals and dealers | Terminals not used by approved users; details not uploaded; NISM lapsed | NSE/INSP/73503 items 42 to 44; NSE/INSP/65693 | New test **dealer_terminals**. Calendar duty CC-T-TERMINAL-NISM. Reminders before a certificate lapses | Covered |
| Minors | Trading allowed in a minor's account | NSE/INSP/73503 item 36 | New test **minor_trading** (the rule is to confirm) | Covered |
| Complaints | Not resolved in 21 days; register not kept | SEBI/HO/OIAE/IGRD/CIR/P/2023/156; Axis Securities order | Test **complaints_21** | Covered |
| Nomination | Nomination choice not recorded | SEBI nomination circulars (up to 10 nominees from 2025) | Nominees are kept on the client; the reminder is a calendar duty. No test checks that every account has a choice | Partial |
| Debarred clients | Trades by debarred PANs | SEBI orders (for example NSE/INVG/53493) | Test **debarred_trades** | Covered |
| Surveillance and AML | Alerts not decided in 45 days; STRs not filed | NSE/SURV/48818; Simandhar order | Test **alerts_45** | Covered |
| Algo and API orders | API orders without the exchange's algo ID; IP addresses of IBT orders not kept | SEBI's retail algo framework (2025); NSE/INSP/73503 item 33 | Gap: a front-end control. Ledgerline keeps the algo ID that trade files carry, but does not test it | Gap |
| Cyber and system audit | Audits and VAPT late; controls weak | SEBI CSCRF; NSE/INSP/73503 items 51 to 65 | Calendar duties (cyber audit, system audit, VAPT, their reports) and test **duties_overdue**. The controls themselves are outside a back office | Partial |
| Records | Order evidence; payout request evidence; registers in the prescribed format | NSE/INSP/73503 items 12 to 14; Simandhar order | Payout requests, dispatches and the audit trail are kept. Order evidence (call recordings) is the front end's | Partial |
| Bank and DP accounts | Opening or closing not reported to the exchange within a week | NSE/INSP/65693; NSE/INSP/73503 item 40 | Gap: Ledgerline does not keep a register of the firm's account openings and closings with their reporting dates | Gap |
| Net worth and RBS data | Net worth computed wrong; RBS data (PEP count) not matching the UCC | NSE/INSP/73503 items 31 and 38; Axis Securities order | Net worth is a calendar duty with the firm securities register. The RBS data is not made by Ledgerline | Partial |

**Before go-live**, the [[parallel|Parallel run]] compares the old system's files with Ledgerline's for 10 business days in a row. Wrong reports to the exchange are a penalty of their own.

**To confirm** marks on a test (on Self-inspection) show what could not be checked against the circular itself. Check them before relying on the test.
