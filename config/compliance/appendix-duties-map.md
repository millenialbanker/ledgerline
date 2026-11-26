# Appendix section 8: firm duties and where they are in the compliance calendar

Section 8 of the compliance gap report's appendix (November 2026) lists 259 P1/P2 duties of the firm
that the compliance calendar (`config/compliance/duties.json`) did not have. Each row's outcome:

- **added**: a new duty in the calendar (the row's circular is one of its sources);
- **merged**: the row's circular added to an existing duty's sources (and its practice, where it adds a step);
- **covered**: already done by a calendar duty or a Ledgerline workflow or self-inspection test;
- **n/a**: not applicable to a stock broker or DP.

Each source in the calendar ends with the gap report rows it is the source for ("Gap report rows: ...").
Who a duty applies to is read from the firm profile (`applies`), so a firm without APs, MCX, IBT or a DP
registration does not get those duties.

Counts: 199 added, 26 merged, 34 covered, 0 not applicable (259 rows) into 45 new duties.

## New duties

| Duty | Title | Applies | Rule | Rows |
|---|---|---|---|---|
| CC-T-AP-ACTION-ATR | Act on each quarter's AP inspection findings and file the action taken report with each exchange | broker, ap\|branch | 03-31,06-30,09-30,12-31 | NSE-0074, NSE-0073, BSE1-N0005, BSE1-N0004, MCX-0042 |
| CC-T-AP-MISCONDUCT | Report an AP offering assured returns or unauthorised schemes to NSE within one working day, then act | broker, ap | EVENT 1wd | NSE-0064, NSE-0065 |
| CC-T-SURV-QUARTERLY | File the quarterly status of surveillance alerts with each exchange (NIL included) within 15 days | broker | 01-15,04-15,07-15,10-15 | MCX-N0006 |
| CC-T-CYBER-INCIDENT-QTR | File the quarterly cyber incident report with the exchanges (nil included) within 15 days | broker | 01-15,04-15,07-15,10-15 | CYBER-0281, CYBER-0284, NSE-0131, MCX-0033 |
| CC-T-CSCRF-CATEGORY | Work out the firm's CSCRF category for the year and have the board approve it | broker\|dp | 04-30 | CYBER-0164, CYBER-0165, CYBER-0004, CYBER-0234, MCX-0027, CDSL-N0013 |
| CC-T-CSCRF-ACCESS-REVIEW | Review user access rights, unused tokens and privileged activity every half year | broker\|dp, cscrf:mid,small,self | 03-31,09-30 | CYBER-0010, CYBER-0068, CYBER-0327 |
| CC-T-CSCRF-ACCESS-REVIEW-QTR | Review user access rights, unused tokens and privileged activity each quarter (qualified REs) | broker\|dp, cscrf:qualified | 03-31,06-30,09-30,12-31 | CYBER-0010, CYBER-0068, CYBER-0327 |
| CC-T-CSCRF-SOC | Keep every system under a security operations centre (M-SOC or own SOC) and get the yearly SOC efficacy report | broker\|dp, cscrf:qualified,mid,small,self | 06-30 | CYBER-0002, CYBER-0003, CYBER-0012, CYBER-0113, CYBER-0115, CYBER-0193, SEBI-NEW-0092, CYBER-0185, CYBER-0309, SEBI-NEW-0047 |
| CC-T-CSCRF-DRILL | Run the yearly live recovery drill of the cyber crisis, BCP and DR plans | broker\|dp, cscrf:qualified,mid,small,self | 03-31 | CYBER-0013, CYBER-0104, CYBER-0123, CYBER-0137, CYBER-0142, CYBER-0186, CYBER-0274 |
| CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs | broker\|dp, cscrf:qualified,mid,small,self | 03-31 | CYBER-0001, CYBER-0023, CYBER-0053, CYBER-0056, CYBER-0094, CYBER-0097, CYBER-0101, CYBER-0105, CYBER-0111, CYBER-0163, SEBI-NEW-0087, CYBER-0189, SEBI-NEW-0089, CYBER-0172, CYBER-0173, CYBER-0305, CYBER-0308, CYBER-0310, SEBI-NEW-0044, SEBI-NEW-0045, SEBI-NEW-0046, SEBI-NEW-0126, CYBER-0312, CYBER-0277, CYBER-0278, CYBER-0279, CYBER-0325, NSE-0141, CYBER-0324 |
| CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan | broker\|dp | 03-31 | CYBER-0197, CYBER-0201, CYBER-0205, CYBER-0208, CYBER-0211, CYBER-0212, CYBER-0213, CYBER-0223, CYBER-0225, CYBER-0228, CYBER-0048, CYBER-0052, CYBER-0106, CYBER-0158, CYBER-0169, CYBER-0183, SEBI-SB2-0150, SEBI-SB2-0153, SEBI-SB2-0156, SEBI-SB1-0063, NSE-0179, SEBI-NEW-0129 |
| CC-T-CYBER-OFFICERS | Designate the cyber Designated Officer (CISO for qualified REs) and the CERT-In point of contact; tell CERT-In of changes | broker\|dp | ASNEEDED | CYBER-0030, CYBER-0171, CYBER-0243, CYBER-0314, LAW-0160 |
| CC-T-IBT-CONTROLS | Yearly check of the internet and mobile trading controls: 2FA, IP capture, encryption, sessions and disclosures | broker, ibt | 03-31 | SEBI-SB2-0076, SEBI-SB2-0077, SEBI-SB2-0081, SEBI-SB2-0084, SEBI-SB2-0088, SEBI-SB2-0089, SEBI-SB2-0091, CYBER-0150, NSE-0190, CYBER-0275 |
| CC-T-PMLA-OFFICERS | Appoint the Principal Officer and Designated Director, register on FINnet 2.0 and tell FIU-IND of any change | broker\|dp | ASNEEDED | LAW-0023, LAW-0024, SEBI-KYCAML-0206, SEBI-KYCAML-0207, SEBI-KYCAML-0208, SEBI-KYCAML-0209 |
| CC-T-ML-RISK-ASSESSMENT | Carry out the yearly ML/TF risk assessment (and file BSE's internal risk assessment with RBS) | broker\|dp | 05-31 | LAW-0040, SEBI-KYCAML-0156, NCL-BSE-0112 |
| CC-T-AML-TRAINING | Train staff on AML/CFT each year, by role | broker\|dp | 03-31 | SEBI-KYCAML-0211 |
| CC-T-ORDER-EVIDENCE | Check each quarter that every dealer-placed order has the client's order evidence (call recording, e-mail, signed slip) | broker | 03-31,06-30,09-30,12-31 | SEBI-SB1-0142, SEBI-SB1-0143, SEBI-SB1-0144, SEBI-SB1-0145, NSE-0169, NSE-N0055, MCX-0076, MCX-0052 |
| CC-T-PROHIBITED-ACTIVITIES | Yearly attestation that the firm does none of the barred fund-based activities | broker | 03-31 | SEBI-SB2-0209, NSE-0187, NSE-N0045, MCX-N0026, SEBI-NEW-0175 |
| CC-T-NSE-QTR-DECLARATIONS | File NSE's quarterly declarations (brokerage display, advertisement code, AP and branch supervision, margin) within 30 days | broker, member:NSE | 01-30,04-30,07-30,10-30 | NSE-0033 |
| CC-T-INSURANCE | Keep the stock brokers' indemnity insurance in force and file the renewed policy with NSE | broker, member:NSE | 07-31 | NSE-0196, NSE-0042 |
| CC-T-BO-FACILITY | Use only exchange-empanelled back-office software and file its name and version with each exchange before use and on change | broker | EVENT 0 | NSE-0082, NCL-BSE-0001, NSE-N0053, NSE-0081, NSE-0083, MCX-0142, MCX-N0028 |
| CC-T-MCX-SYSAUDIT | MCX: have the yearly system audit done by an MCX-empanelled auditor and submit the report by 30 June | broker, member:MCX | 06-30 | MCX-0019, MCX-0011, MCX-0017, MCX-0023 |
| CC-T-MCX-SYSAUDIT-ATR | MCX: file the system audit action taken report, validated by the same auditor, by 30 September | broker, member:MCX | 09-30 | MCX-0020, MCX-0018 |
| CC-T-TRADING-SOFTWARE | Before a new trading software or version goes live: exchange mock testing and approval | broker | EVENT 0 | MCX-0112, MCX-0113, SEBI-SB2-0106 |
| CC-T-SMS-TEMPLATES | Pre-tag every SMS template's variable fields under TRAI's direction and use 1600 numbers for service calls | broker\|dp | ONCE | MCX-0135, CDSL-N0012 |
| CC-T-INVESTOR-AWARENESS | Show SEBI's investor awareness messages (Project Jagrook) and the F&O risk disclosures on the website and app | broker | MEND | SEBI-NEW-0001, SEBI-NEW-0002, SEBI-NEW-0003, NSE-0203, MCX-0069, SEBI-SB1-0205, SEBI-SB2-0058 |
| CC-T-CYBERFRAUD-LEA | Act on a cyber-fraud complaint or police request under the I4C SOP (NCRP and CFCFRMS) | broker\|dp | EVENT 0 | NSE-0202, NSE-0242 |
| CC-T-ADVERTISEMENT | Get NSE's approval for an advertisement at least 7 working days before it is issued | broker, member:NSE | EVENT 0 | NSE-N0011 |
| CC-T-SEBI-INSPECTION-CSR | After a SEBI inspection: correct the findings, file the compliance status report and have the internal auditor certify it | broker | EVENT 30d | NSE-0176, NSE-0177, NSE-0261, BSE1-N0018, MCX-N0047, MCX-0039 |
| CC-T-REGULATOR-LETTERS | Answer a letter of observation, show cause notice or information request by its date, with documents | broker\|dp | EVENT 15d | SEBI-N0001, BSE1-N0006, MCX-0037, NCL-BSE-0019, NSE-0258, SEBI-SB1-0048 |
| CC-T-NCL-PENALTY-REVIEW | Check NSE Clearing's portal for penalties and ask for any review within 7 working days | broker, clearing | EVENT 7wd | NCL-BSE-0031, NSE-N0050 |
| CC-T-BSE-CRITICAL-FINDING | Have the internal auditor e-mail BSE within 2 days of a critical non-compliance | broker, member:BSE | EVENT 2d | BSE1-N0001 |
| CC-T-SEBI-CM-FEE | Pay SEBI's yearly clearing member fee for each segment before 1 June | broker, clearing | 05-31 | SEBI-NEW-0161 |
| CC-T-TDS-DEPOSIT | Deposit the month's TDS by the 7th | broker\|dp | D07 | LAW-0072 |
| CC-T-TDS-RETURNS | File the quarterly TDS statements on time | broker\|dp | 01-31,05-31,07-31,10-31 | LAW-0075 |
| CC-T-DPDP-READINESS | Be ready for the Digital Personal Data Protection Act by 13 May 2027 | broker\|dp | ONCE:2027-05-13 | LAW-0125, LAW-0130, LAW-0148 |
| CC-T-PIT-CODE-REVIEW | Keep the insider trading code of conduct approved and have the audit committee review PIT compliance each year | broker\|dp | 03-31 | GAP-0001, GAP-0006 |
| CC-T-PIT-VIOLATION | Report a violation of the insider trading code by a designated person to the stock exchanges | broker\|dp | EVENT 7d | GAP-0021, GAP-0025 |
| CC-T-RMS-RULES | Put each new SEBI or exchange risk rule into the trading front end's RMS from its effective date | broker | EVENT 0 | NSE-0226, GAP-0096, SEBI-NEW-0061, SEBI-NEW-0071, SEBI-NEW-0073 |
| CC-T-EPI-MARGIN | Configure RMS so an EPI-backed sale is squared off or a new position taken only with the client's margin available | broker, seg:CM | ONCE:2027-02-22 | SEBI-NEW-0027, SEBI-SB1-0218 |
| CC-T-CLIENT-CODE-MODS | Keep one designated ERROR account and record a reason code for each client code modification | broker, member:NSE | EVENT 0 | NSE-0229 |
| CC-T-MATERIAL-CHANGE | Tell SEBI through the exchanges of a material change, and keep each exchange's member profile current | broker | EVENT 7d | SEBI-NEW-0112, MCX-N0022 |
| CC-D-COMPLIANCE-CERT | File the DP's half-yearly compliance certificate and NISM Series VI details on CDSL Audit Web | dp | 01-31,07-31 | SEBI-SB1-0046, DP-0326, DP-0325, CDSL-N0024 |
| CC-D-VAPT | Have the DP's yearly VAPT done by a CERT-In empanelled auditor and file the report with CDSL | dp, cscrf:qualified,mid,small,self | 07-31 | DP-0345 |
| CC-D-SEBI-FEES | Pay SEBI's DP fees: the yearly fee and the five-yearly block fee before each block expires | dp | ASNEEDED | GAP-0031 |

## All rows

| Id | Pri | Outcome | Where | Note |
|---|---|---|---|---|
| SEBI-KYCAML-0189 | P1 | covered | CC-T-SANCTIONS-MATCH, CC-T-SANCTIONS-UNFREEZE | WMD Act freeze (para 59) and the exemption and inadvertent-freezing route (unfreeze duty). |
| SEBI-KYCAML-0194 | P1 | covered | CC-T-SANCTIONS-MATCH | Resembling accounts reported at once to the UAPA Central Nodal Officer (paras 69-70). |
| BSE1-N0005 | P1 | added | CC-T-AP-ACTION-ATR | Act on each quarter's AP inspection findings and file the action taken report with each exchange |
| NSE-0074 | P1 | added | CC-T-AP-ACTION-ATR | Act on each quarter's AP inspection findings and file the action taken report with each exchange |
| NSE-0192 | P1 | covered | Self-inspection firm_bank_register; Client money reconciliation → Firm bank accounts | Every investor-facing account linked to an @valid UPI ID (NSE/COMP/75700 Annexure B is in the test's source); new accounts checked as they are added. |
| SEBI-SB2-0209 | P1 | added | CC-T-PROHIBITED-ACTIVITIES | Yearly attestation that the firm does none of the barred fund-based activities |
| SEBI-KYCAML-0074 | P1 | covered | Self-inspection proi_kyc; proi.go | PROI identification, verification and purpose at onboarding from 3 Dec 2026. |
| CYBER-0281 | P1 | added | CC-T-CYBER-INCIDENT-QTR | File the quarterly cyber incident report with the exchanges (nil included) within 15 days |
| CYBER-0284 | P1 | added | CC-T-CYBER-INCIDENT-QTR | File the quarterly cyber incident report with the exchanges (nil included) within 15 days |
| DP-0345 | P1 | added | CC-D-VAPT | Have the DP's yearly VAPT done by a CERT-In empanelled auditor and file the report with CDSL |
| NSE-0082 | P1 | added | CC-T-BO-FACILITY | Use only exchange-empanelled back-office software and file its name and version with each exchange before use and on change |
| CDSL-N0006 | P1 | merged | CC-D-SURV-QUARTERLY | Send CDSL the quarterly DP surveillance report (NIL included) within 15 days of the quarter |
| NSE-0081 | P1 | added | CC-T-BO-FACILITY | Use only exchange-empanelled back-office software and file its name and version with each exchange before use and on change |
| NSE-N0053 | P1 | added | CC-T-BO-FACILITY | Use only exchange-empanelled back-office software and file its name and version with each exchange before use and on change |
| NSE-0145 | P1 | covered | Self-inspection cuspa_policy; Settlement → CUSPA policy (gap-cuspa) | Written unpaid-securities policy, versioned, on the website, sent to every client before it applies. |
| MCX-N0006 | P1 | added | CC-T-SURV-QUARTERLY | File the quarterly status of surveillance alerts with each exchange (NIL included) within 15 days |
| LAW-0023 | P2 | added | CC-T-PMLA-OFFICERS | Appoint the Principal Officer and Designated Director, register on FINnet 2.0 and tell FIU-IND of any change |
| LAW-0024 | P2 | added | CC-T-PMLA-OFFICERS | Appoint the Principal Officer and Designated Director, register on FINnet 2.0 and tell FIU-IND of any change |
| LAW-0040 | P2 | added | CC-T-ML-RISK-ASSESSMENT | Carry out the yearly ML/TF risk assessment (and file BSE's internal risk assessment with RBS) |
| MCX-N0009 | P2 | merged | CC-T-INTERNAL-ALERTS | Keep the monthly file of internal and exchange surveillance alerts; close each within 45 days |
| NCL-BSE-0112 | P2 | added | CC-T-ML-RISK-ASSESSMENT | Carry out the yearly ML/TF risk assessment (and file BSE's internal risk assessment with RBS) |
| NSE-0249 | P2 | merged | CC-T-FATF | Re-screen clients on each FATF public statement SEBI or the exchanges circulate and record the EDD |
| SEBI-KYCAML-0182 | P2 | covered | CC-T-SANCTIONS-LISTS | MHA s.51A order of 2 Feb 2021 with corrigenda; nodal officer list checked with each update. |
| SEBI-KYCAML-0186 | P2 | covered | CC-T-SANCTIONS-MATCH | Copies to SEBI by post and e-mail (para 59(iv)). |
| SEBI-KYCAML-0187 | P2 | covered | CC-T-SANCTIONS-MATCH | Prevent transactions and inform the CNO (para 59(v)). |
| SEBI-KYCAML-0190 | P2 | covered | CC-T-SANCTIONS-LISTS | UAPA s.35(1) designations, ISIL/Al-Qaida and 1718 lists (paras 62-64). |
| SEBI-KYCAML-0195 | P2 | covered | CC-T-SANCTIONS-MATCH | Copies to the State nodal officer, SEBI and FIU-IND. |
| BSE1-N0004 | P2 | added | CC-T-AP-ACTION-ATR | Act on each quarter's AP inspection findings and file the action taken report with each exchange |
| MCX-0042 | P2 | added | CC-T-AP-ACTION-ATR | Act on each quarter's AP inspection findings and file the action taken report with each exchange |
| MCX-0052 | P2 | added | CC-T-ORDER-EVIDENCE | Check each quarter that every dealer-placed order has the client's order evidence (call recording, e-mail, signed slip) |
| NSE-0061 | P2 | merged | OB-APINSP | Inspect authorised persons and branches (at least 50% a year) and report each quarter |
| NSE-0064 | P2 | added | CC-T-AP-MISCONDUCT | Report an AP offering assured returns or unauthorised schemes to NSE within one working day, then act |
| NSE-0065 | P2 | added | CC-T-AP-MISCONDUCT | Report an AP offering assured returns or unauthorised schemes to NSE within one working day, then act |
| NSE-0073 | P2 | added | CC-T-AP-ACTION-ATR | Act on each quarter's AP inspection findings and file the action taken report with each exchange |
| MCX-0127 | P2 | covered | Self-inspection firm_bank_register | Same @valid UPI rule as NSE-0192, from MCX's circular. |
| MCX-0129 | P2 | covered | Self-inspection firm_bank_register | A new investor-facing account fails the test until its @valid UPI ID is recorded. |
| MCX-0132 | P2 | merged | OB-RAS-M | Running account settlement: clients who chose monthly settlement |
| MCX-N0026 | P2 | added | CC-T-PROHIBITED-ACTIVITIES | Yearly attestation that the firm does none of the barred fund-based activities |
| NCL-BSE-0120 | P2 | covered | Self-inspection firm_bank_register; Firm bank accounts register | Opening and closure reported to each exchange within 7 working days, with the bank undertaking. |
| NCL-BSE-0143 | P2 | covered | Self-inspection firm_bank_register | Bank undertaking (statements direct from the bank) within 7 working days of opening. |
| NCL-BSE-0144 | P2 | covered | Self-inspection firm_bank_register | List of accounts and the freeze undertaking within 7 days of opening. |
| NSE-0041 | P2 | covered | Self-inspection firm_bank_register | The undertaking recorded per account; the revised Annex B-D formats are the firm's paperwork for the same report. |
| NSE-0187 | P2 | added | CC-T-PROHIBITED-ACTIVITIES | Yearly attestation that the firm does none of the barred fund-based activities |
| SEBI-SB2-0176 | P2 | covered | Self-inspection firm_bank_register | Reporting within 7 working days of opening or change; the Annexure-39 bond is the document filed with it. |
| MCX-N0008 | P2 | merged | CC-T-INTERNAL-ALERTS | Keep the monthly file of internal and exchange surveillance alerts; close each within 45 days |
| SEBI-KYCAML-0042 | P2 | covered | Online applications (eKYC vendor's VIPV) and self-inspection kyc_complete | VIPV is done by the onboarding vendor and kept as the 'IPV / VIPV record' document; its conditions are in the vendor contract. |
| SEBI-KYCAML-0058 | P2 | covered | Self-inspection kyc_gate; KYC trading gate | Only KRA 'KYC Validated' records trade without the firm's own verification. |
| SEBI-KYCAML-0076 | P2 | covered | proi.go (KRA and CKYCRR looked up first) | Extra documents only where the record changed, is incomplete or lapsed. |
| SEBI-KYCAML-0079 | P2 | covered | Self-inspection proi_kyc | Passport (and OCI card) collected and verified. |
| SEBI-KYCAML-0080 | P2 | covered | Self-inspection proi_kyc | CERSAI template and wet signature in VIPV. |
| SEBI-KYCAML-0081 | P2 | covered | Self-inspection proi_kyc | OVD as PoI and PoA, overseas address verified. |
| SEBI-KYCAML-0082 | P2 | covered | Self-inspection proi_kyc | Original seen and verified, or the listed alternatives. |
| CDSL-N0012 | P2 | added | CC-T-SMS-TEMPLATES | Pre-tag every SMS template's variable fields under TRAI's direction and use 1600 numbers for service calls |
| CDSL-N0013 | P2 | added | CC-T-CSCRF-CATEGORY | Work out the firm's CSCRF category for the year and have the board approve it |
| CYBER-0001 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0002 | P2 | added | CC-T-CSCRF-SOC | Keep every system under a security operations centre (M-SOC or own SOC) and get the yearly SOC efficacy report |
| CYBER-0003 | P2 | added | CC-T-CSCRF-SOC | Keep every system under a security operations centre (M-SOC or own SOC) and get the yearly SOC efficacy report |
| CYBER-0004 | P2 | added | CC-T-CSCRF-CATEGORY | Work out the firm's CSCRF category for the year and have the board approve it |
| CYBER-0010 | P2 | added | CC-T-CSCRF-ACCESS-REVIEW, CC-T-CSCRF-ACCESS-REVIEW-QTR | Review user access rights, unused tokens and privileged activity every half year |
| CYBER-0012 | P2 | added | CC-T-CSCRF-SOC | Keep every system under a security operations centre (M-SOC or own SOC) and get the yearly SOC efficacy report |
| CYBER-0013 | P2 | added | CC-T-CSCRF-DRILL | Run the yearly live recovery drill of the cyber crisis, BCP and DR plans |
| CYBER-0023 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0030 | P2 | added | CC-T-CYBER-OFFICERS | Designate the cyber Designated Officer (CISO for qualified REs) and the CERT-In point of contact; tell CERT-In of changes |
| CYBER-0048 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0052 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0053 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0056 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0068 | P2 | added | CC-T-CSCRF-ACCESS-REVIEW, CC-T-CSCRF-ACCESS-REVIEW-QTR | Review user access rights, unused tokens and privileged activity every half year |
| CYBER-0094 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0097 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0101 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0104 | P2 | added | CC-T-CSCRF-DRILL | Run the yearly live recovery drill of the cyber crisis, BCP and DR plans |
| CYBER-0105 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0106 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0111 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0113 | P2 | added | CC-T-CSCRF-SOC | Keep every system under a security operations centre (M-SOC or own SOC) and get the yearly SOC efficacy report |
| CYBER-0115 | P2 | added | CC-T-CSCRF-SOC | Keep every system under a security operations centre (M-SOC or own SOC) and get the yearly SOC efficacy report |
| CYBER-0123 | P2 | added | CC-T-CSCRF-DRILL | Run the yearly live recovery drill of the cyber crisis, BCP and DR plans |
| CYBER-0126 | P2 | merged | CC-D-CYBER-INCIDENT | Report a cyber incident within 6 hours to CERT-In, SEBI and the depository, then follow up |
| CYBER-0128 | P2 | merged | CC-D-CYBER-INCIDENT | Report a cyber incident within 6 hours to CERT-In, SEBI and the depository, then follow up |
| CYBER-0129 | P2 | merged | CC-D-CYBER-INCIDENT | Report a cyber incident within 6 hours to CERT-In, SEBI and the depository, then follow up |
| CYBER-0133 | P2 | merged | CC-D-CYBER-INCIDENT | Report a cyber incident within 6 hours to CERT-In, SEBI and the depository, then follow up |
| CYBER-0137 | P2 | added | CC-T-CSCRF-DRILL | Run the yearly live recovery drill of the cyber crisis, BCP and DR plans |
| CYBER-0142 | P2 | added | CC-T-CSCRF-DRILL | Run the yearly live recovery drill of the cyber crisis, BCP and DR plans |
| CYBER-0150 | P2 | added | CC-T-IBT-CONTROLS | Yearly check of the internet and mobile trading controls: 2FA, IP capture, encryption, sessions and disclosures |
| CYBER-0154 | P2 | merged | CC-D-CYBER-INCIDENT | Report a cyber incident within 6 hours to CERT-In, SEBI and the depository, then follow up |
| CYBER-0157 | P2 | merged | CC-D-CYBER-INCIDENT | Report a cyber incident within 6 hours to CERT-In, SEBI and the depository, then follow up |
| CYBER-0158 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0163 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0164 | P2 | added | CC-T-CSCRF-CATEGORY | Work out the firm's CSCRF category for the year and have the board approve it |
| CYBER-0165 | P2 | added | CC-T-CSCRF-CATEGORY | Work out the firm's CSCRF category for the year and have the board approve it |
| CYBER-0169 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0171 | P2 | added | CC-T-CYBER-OFFICERS | Designate the cyber Designated Officer (CISO for qualified REs) and the CERT-In point of contact; tell CERT-In of changes |
| CYBER-0172 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0173 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0183 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0185 | P2 | added | CC-T-CSCRF-SOC | Keep every system under a security operations centre (M-SOC or own SOC) and get the yearly SOC efficacy report |
| CYBER-0186 | P2 | added | CC-T-CSCRF-DRILL | Run the yearly live recovery drill of the cyber crisis, BCP and DR plans |
| CYBER-0189 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0193 | P2 | added | CC-T-CSCRF-SOC | Keep every system under a security operations centre (M-SOC or own SOC) and get the yearly SOC efficacy report |
| CYBER-0197 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0201 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0205 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0208 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0211 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0212 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0213 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0223 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0225 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0228 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| CYBER-0234 | P2 | added | CC-T-CSCRF-CATEGORY | Work out the firm's CSCRF category for the year and have the board approve it |
| CYBER-0243 | P2 | added | CC-T-CYBER-OFFICERS | Designate the cyber Designated Officer (CISO for qualified REs) and the CERT-In point of contact; tell CERT-In of changes |
| CYBER-0274 | P2 | added | CC-T-CSCRF-DRILL | Run the yearly live recovery drill of the cyber crisis, BCP and DR plans |
| CYBER-0275 | P2 | added | CC-T-IBT-CONTROLS | Yearly check of the internet and mobile trading controls: 2FA, IP capture, encryption, sessions and disclosures |
| CYBER-0277 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0278 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0279 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0305 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0308 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0309 | P2 | added | CC-T-CSCRF-SOC | Keep every system under a security operations centre (M-SOC or own SOC) and get the yearly SOC efficacy report |
| CYBER-0310 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0312 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0314 | P2 | added | CC-T-CYBER-OFFICERS | Designate the cyber Designated Officer (CISO for qualified REs) and the CERT-In point of contact; tell CERT-In of changes |
| CYBER-0324 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0325 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| CYBER-0327 | P2 | added | CC-T-CSCRF-ACCESS-REVIEW, CC-T-CSCRF-ACCESS-REVIEW-QTR | Review user access rights, unused tokens and privileged activity every half year |
| LAW-0160 | P2 | added | CC-T-CYBER-OFFICERS | Designate the cyber Designated Officer (CISO for qualified REs) and the CERT-In point of contact; tell CERT-In of changes |
| MCX-0011 | P2 | added | CC-T-MCX-SYSAUDIT | MCX: have the yearly system audit done by an MCX-empanelled auditor and submit the report by 30 June |
| MCX-0017 | P2 | added | CC-T-MCX-SYSAUDIT | MCX: have the yearly system audit done by an MCX-empanelled auditor and submit the report by 30 June |
| MCX-0018 | P2 | added | CC-T-MCX-SYSAUDIT-ATR | MCX: file the system audit action taken report, validated by the same auditor, by 30 September |
| MCX-0019 | P2 | added | CC-T-MCX-SYSAUDIT | MCX: have the yearly system audit done by an MCX-empanelled auditor and submit the report by 30 June |
| MCX-0020 | P2 | added | CC-T-MCX-SYSAUDIT-ATR | MCX: file the system audit action taken report, validated by the same auditor, by 30 September |
| MCX-0027 | P2 | added | CC-T-CSCRF-CATEGORY | Work out the firm's CSCRF category for the year and have the board approve it |
| MCX-0033 | P2 | added | CC-T-CYBER-INCIDENT-QTR | File the quarterly cyber incident report with the exchanges (nil included) within 15 days |
| MCX-0112 | P2 | added | CC-T-TRADING-SOFTWARE | Before a new trading software or version goes live: exchange mock testing and approval |
| MCX-0113 | P2 | added | CC-T-TRADING-SOFTWARE | Before a new trading software or version goes live: exchange mock testing and approval |
| MCX-0135 | P2 | added | CC-T-SMS-TEMPLATES | Pre-tag every SMS template's variable fields under TRAI's direction and use 1600 numbers for service calls |
| MCX-0142 | P2 | added | CC-T-BO-FACILITY | Use only exchange-empanelled back-office software and file its name and version with each exchange before use and on change |
| MCX-N0028 | P2 | added | CC-T-BO-FACILITY | Use only exchange-empanelled back-office software and file its name and version with each exchange before use and on change |
| NCL-BSE-0001 | P2 | added | CC-T-BO-FACILITY | Use only exchange-empanelled back-office software and file its name and version with each exchange before use and on change |
| NCL-BSE-0102 | P2 | merged | OB-SYSAUDIT | System audit: preliminary audit report by an NSE-empanelled auditor |
| NSE-0083 | P2 | added | CC-T-BO-FACILITY | Use only exchange-empanelled back-office software and file its name and version with each exchange before use and on change |
| NSE-0116 | P2 | covered | OB-SYSAUDIT (system audit ToR, NSE/INSP/73850) | The controls auditable under the ToR are what the yearly system audit checks. |
| NSE-0131 | P2 | added | CC-T-CYBER-INCIDENT-QTR | File the quarterly cyber incident report with the exchanges (nil included) within 15 days |
| NSE-0141 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| NSE-0179 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| NSE-0190 | P2 | added | CC-T-IBT-CONTROLS | Yearly check of the internet and mobile trading controls: 2FA, IP capture, encryption, sessions and disclosures |
| SEBI-NEW-0044 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| SEBI-NEW-0045 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| SEBI-NEW-0046 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| SEBI-NEW-0047 | P2 | added | CC-T-CSCRF-SOC | Keep every system under a security operations centre (M-SOC or own SOC) and get the yearly SOC efficacy report |
| SEBI-NEW-0087 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| SEBI-NEW-0089 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| SEBI-NEW-0092 | P2 | added | CC-T-CSCRF-SOC | Keep every system under a security operations centre (M-SOC or own SOC) and get the yearly SOC efficacy report |
| SEBI-NEW-0126 | P2 | added | CC-T-CSCRF-CONTROLS | Yearly CSCRF controls review: critical systems, asset inventory, patching, hardening and APIs |
| SEBI-SB1-0063 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| SEBI-SB2-0076 | P2 | added | CC-T-IBT-CONTROLS | Yearly check of the internet and mobile trading controls: 2FA, IP capture, encryption, sessions and disclosures |
| SEBI-SB2-0081 | P2 | added | CC-T-IBT-CONTROLS | Yearly check of the internet and mobile trading controls: 2FA, IP capture, encryption, sessions and disclosures |
| SEBI-SB2-0084 | P2 | added | CC-T-IBT-CONTROLS | Yearly check of the internet and mobile trading controls: 2FA, IP capture, encryption, sessions and disclosures |
| SEBI-SB2-0089 | P2 | added | CC-T-IBT-CONTROLS | Yearly check of the internet and mobile trading controls: 2FA, IP capture, encryption, sessions and disclosures |
| SEBI-SB2-0091 | P2 | added | CC-T-IBT-CONTROLS | Yearly check of the internet and mobile trading controls: 2FA, IP capture, encryption, sessions and disclosures |
| SEBI-SB2-0106 | P2 | added | CC-T-TRADING-SOFTWARE | Before a new trading software or version goes live: exchange mock testing and approval |
| SEBI-SB2-0150 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| SEBI-SB2-0153 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| SEBI-SB2-0156 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| LAW-0125 | P2 | added | CC-T-DPDP-READINESS | Be ready for the Digital Personal Data Protection Act by 13 May 2027 |
| LAW-0130 | P2 | added | CC-T-DPDP-READINESS | Be ready for the Digital Personal Data Protection Act by 13 May 2027 |
| LAW-0148 | P2 | added | CC-T-DPDP-READINESS | Be ready for the Digital Personal Data Protection Act by 13 May 2027 |
| DP-0029 | P2 | merged | CC-D-CONCURRENT-AUDIT | Have the concurrent audit of risk-prone areas done; auditor reports to the DP by the 10th |
| DP-0310 | P2 | merged | CC-D-CONCURRENT-AUDIT | Have the concurrent audit of risk-prone areas done; auditor reports to the DP by the 10th |
| SEBI-SB1-0046 | P2 | added | CC-D-COMPLIANCE-CERT | File the DP's half-yearly compliance certificate and NISM Series VI details on CDSL Audit Web |
| SEBI-SB1-0048 | P2 | added | CC-T-REGULATOR-LETTERS | Answer a letter of observation, show cause notice or information request by its date, with documents |
| BSE1-N0001 | P2 | added | CC-T-BSE-CRITICAL-FINDING | Have the internal auditor e-mail BSE within 2 days of a critical non-compliance |
| BSE1-N0006 | P2 | added | CC-T-REGULATOR-LETTERS | Answer a letter of observation, show cause notice or information request by its date, with documents |
| BSE1-N0015 | P2 | merged | CC-T-RISK-BASED-SUPERVISION | Submit Risk Based Supervision data to each exchange |
| BSE1-N0018 | P2 | added | CC-T-SEBI-INSPECTION-CSR | After a SEBI inspection: correct the findings, file the compliance status report and have the internal auditor certify it |
| CDSL-N0004 | P2 | merged | CC-D-INTERNAL-ALERTS | Work DP surveillance alerts (own and CDSL) within 45 days and report adverse findings to CDSL within 7 days |
| CDSL-N0024 | P2 | added | CC-D-COMPLIANCE-CERT | File the DP's half-yearly compliance certificate and NISM Series VI details on CDSL Audit Web |
| CDSL-N0044 | P2 | covered | Self-inspections bsda_review, kyc_gate, cdsl_kyc_freeze, ddpi_purpose; CC-D-INTERNAL-AUDIT | CDSL's list of inspection findings: each item is a standing rule the tests or the DP internal audit check. |
| DP-0325 | P2 | added | CC-D-COMPLIANCE-CERT | File the DP's half-yearly compliance certificate and NISM Series VI details on CDSL Audit Web |
| DP-0326 | P2 | added | CC-D-COMPLIANCE-CERT | File the DP's half-yearly compliance certificate and NISM Series VI details on CDSL Audit Web |
| GAP-0001 | P2 | added | CC-T-PIT-CODE-REVIEW | Keep the insider trading code of conduct approved and have the audit committee review PIT compliance each year |
| GAP-0006 | P2 | added | CC-T-PIT-CODE-REVIEW | Keep the insider trading code of conduct approved and have the audit committee review PIT compliance each year |
| GAP-0031 | P2 | added | CC-D-SEBI-FEES | Pay SEBI's DP fees: the yearly fee and the five-yearly block fee before each block expires |
| MCX-0023 | P2 | added | CC-T-MCX-SYSAUDIT | MCX: have the yearly system audit done by an MCX-empanelled auditor and submit the report by 30 June |
| MCX-0037 | P2 | added | CC-T-REGULATOR-LETTERS | Answer a letter of observation, show cause notice or information request by its date, with documents |
| MCX-0039 | P2 | added | CC-T-SEBI-INSPECTION-CSR | After a SEBI inspection: correct the findings, file the compliance status report and have the internal auditor certify it |
| MCX-N0047 | P2 | added | CC-T-SEBI-INSPECTION-CSR | After a SEBI inspection: correct the findings, file the compliance status report and have the internal auditor certify it |
| NCL-BSE-0019 | P2 | added | CC-T-REGULATOR-LETTERS | Answer a letter of observation, show cause notice or information request by its date, with documents |
| NSE-0033 | P2 | added | CC-T-NSE-QTR-DECLARATIONS | File NSE's quarterly declarations (brokerage display, advertisement code, AP and branch supervision, margin) within 30 days |
| NSE-0042 | P2 | added | CC-T-INSURANCE | Keep the stock brokers' indemnity insurance in force and file the renewed policy with NSE |
| NSE-0176 | P2 | added | CC-T-SEBI-INSPECTION-CSR | After a SEBI inspection: correct the findings, file the compliance status report and have the internal auditor certify it |
| NSE-0177 | P2 | added | CC-T-SEBI-INSPECTION-CSR | After a SEBI inspection: correct the findings, file the compliance status report and have the internal auditor certify it |
| NSE-0196 | P2 | added | CC-T-INSURANCE | Keep the stock brokers' indemnity insurance in force and file the renewed policy with NSE |
| NSE-0258 | P2 | added | CC-T-REGULATOR-LETTERS | Answer a letter of observation, show cause notice or information request by its date, with documents |
| NSE-0261 | P2 | added | CC-T-SEBI-INSPECTION-CSR | After a SEBI inspection: correct the findings, file the compliance status report and have the internal auditor certify it |
| NSE-N0045 | P2 | added | CC-T-PROHIBITED-ACTIVITIES | Yearly attestation that the firm does none of the barred fund-based activities |
| SEBI-KYCAML-0156 | P2 | added | CC-T-ML-RISK-ASSESSMENT | Carry out the yearly ML/TF risk assessment (and file BSE's internal risk assessment with RBS) |
| SEBI-KYCAML-0206 | P2 | added | CC-T-PMLA-OFFICERS | Appoint the Principal Officer and Designated Director, register on FINnet 2.0 and tell FIU-IND of any change |
| SEBI-KYCAML-0208 | P2 | added | CC-T-PMLA-OFFICERS | Appoint the Principal Officer and Designated Director, register on FINnet 2.0 and tell FIU-IND of any change |
| SEBI-KYCAML-0211 | P2 | added | CC-T-AML-TRAINING | Train staff on AML/CFT each year, by role |
| SEBI-N0001 | P2 | added | CC-T-REGULATOR-LETTERS | Answer a letter of observation, show cause notice or information request by its date, with documents |
| SEBI-NEW-0112 | P2 | added | CC-T-MATERIAL-CHANGE | Tell SEBI through the exchanges of a material change, and keep each exchange's member profile current |
| SEBI-NEW-0161 | P2 | added | CC-T-SEBI-CM-FEE | Pay SEBI's yearly clearing member fee for each segment before 1 June |
| SEBI-SB1-0032 | P2 | merged | OB-INTAUDIT | File the half-yearly internal audit report by an empanelled auditor |
| SEBI-SB1-0033 | P2 | merged | OB-INTAUDIT | File the half-yearly internal audit report by an empanelled auditor |
| CDSL-N0025 | P2 | merged | CC-D-GRIEVANCE-REPORT | Submit the BO grievance report to CDSL and the IG report to NSDL by the 10th |
| CDSL-N0027 | P2 | merged | CC-D-COMPLAINTS-DISCLOSURE | Publish the DP's investor complaints data for last month on the website by the 7th |
| MCX-0069 | P2 | added | CC-T-INVESTOR-AWARENESS | Show SEBI's investor awareness messages (Project Jagrook) and the F&O risk disclosures on the website and app |
| MCX-0093 | P2 | covered | CC-T-MCX-COMPLAINTS | Monthly IGRS report within 7 days. |
| MCX-0094 | P2 | covered | CC-T-MCX-COMPLAINTS | NIL submission on IGRS (the duty's due text says NIL included). |
| NSE-0202 | P2 | added | CC-T-CYBERFRAUD-LEA | Act on a cyber-fraud complaint or police request under the I4C SOP (NCRP and CFCFRMS) |
| NSE-0203 | P2 | added | CC-T-INVESTOR-AWARENESS | Show SEBI's investor awareness messages (Project Jagrook) and the F&O risk disclosures on the website and app |
| NSE-0242 | P2 | added | CC-T-CYBERFRAUD-LEA | Act on a cyber-fraud complaint or police request under the I4C SOP (NCRP and CFCFRMS) |
| NSE-N0011 | P2 | added | CC-T-ADVERTISEMENT | Get NSE's approval for an advertisement at least 7 working days before it is issued |
| SEBI-NEW-0001 | P2 | added | CC-T-INVESTOR-AWARENESS | Show SEBI's investor awareness messages (Project Jagrook) and the F&O risk disclosures on the website and app |
| SEBI-NEW-0002 | P2 | added | CC-T-INVESTOR-AWARENESS | Show SEBI's investor awareness messages (Project Jagrook) and the F&O risk disclosures on the website and app |
| SEBI-NEW-0003 | P2 | added | CC-T-INVESTOR-AWARENESS | Show SEBI's investor awareness messages (Project Jagrook) and the F&O risk disclosures on the website and app |
| SEBI-NEW-0172 | P2 | merged | OB-COMPLAINTS | Publish last month's investor complaints data on the website by the 7th; file BSE's data |
| SEBI-SB1-0205 | P2 | added | CC-T-INVESTOR-AWARENESS | Show SEBI's investor awareness messages (Project Jagrook) and the F&O risk disclosures on the website and app |
| SEBI-SB2-0058 | P2 | added | CC-T-INVESTOR-AWARENESS | Show SEBI's investor awareness messages (Project Jagrook) and the F&O risk disclosures on the website and app |
| NSE-0226 | P2 | added | CC-T-RMS-RULES | Put each new SEBI or exchange risk rule into the trading front end's RMS from its effective date |
| SEBI-NEW-0027 | P2 | added | CC-T-EPI-MARGIN | Configure RMS so an EPI-backed sale is squared off or a new position taken only with the client's margin available |
| SEBI-NEW-0061 | P2 | added | CC-T-RMS-RULES | Put each new SEBI or exchange risk rule into the trading front end's RMS from its effective date |
| SEBI-NEW-0071 | P2 | added | CC-T-RMS-RULES | Put each new SEBI or exchange risk rule into the trading front end's RMS from its effective date |
| SEBI-NEW-0073 | P2 | added | CC-T-RMS-RULES | Put each new SEBI or exchange risk rule into the trading front end's RMS from its effective date |
| SEBI-SB1-0218 | P2 | added | CC-T-EPI-MARGIN | Configure RMS so an EPI-backed sale is squared off or a new position taken only with the client's margin available |
| SEBI-NEW-0129 | P2 | added | CC-T-THIRD-PARTY-REVIEW | Yearly review of cloud providers and IT vendors: due diligence, contracts, SOC 2 reports and exit plan |
| SEBI-NEW-0175 | P2 | added | CC-T-PROHIBITED-ACTIVITIES | Yearly attestation that the firm does none of the barred fund-based activities |
| SEBI-SB1-0144 | P2 | added | CC-T-ORDER-EVIDENCE | Check each quarter that every dealer-placed order has the client's order evidence (call recording, e-mail, signed slip) |
| SEBI-SB1-0145 | P2 | added | CC-T-ORDER-EVIDENCE | Check each quarter that every dealer-placed order has the client's order evidence (call recording, e-mail, signed slip) |
| GAP-0021 | P2 | added | CC-T-PIT-VIOLATION | Report a violation of the insider trading code by a designated person to the stock exchanges |
| GAP-0025 | P2 | added | CC-T-PIT-VIOLATION | Report a violation of the insider trading code by a designated person to the stock exchanges |
| GAP-0054 | P2 | merged | CC-D-GRIEVANCE-REPORT | Submit the BO grievance report to CDSL and the IG report to NSDL by the 10th |
| MCX-N0022 | P2 | added | CC-T-MATERIAL-CHANGE | Tell SEBI through the exchanges of a material change, and keep each exchange's member profile current |
| NCL-BSE-0031 | P2 | added | CC-T-NCL-PENALTY-REVIEW | Check NSE Clearing's portal for penalties and ask for any review within 7 working days |
| NSE-N0050 | P2 | added | CC-T-NCL-PENALTY-REVIEW | Check NSE Clearing's portal for penalties and ask for any review within 7 working days |
| SEBI-KYCAML-0207 | P2 | added | CC-T-PMLA-OFFICERS | Appoint the Principal Officer and Designated Director, register on FINnet 2.0 and tell FIU-IND of any change |
| SEBI-KYCAML-0209 | P2 | added | CC-T-PMLA-OFFICERS | Appoint the Principal Officer and Designated Director, register on FINnet 2.0 and tell FIU-IND of any change |
| SEBI-NEW-0104 | P2 | covered | Self-inspection firm_bank_register | New and closed bank accounts within 7 working days from 17 Apr 2026; demat accounts no longer reported by the broker. |
| SEBI-NEW-0006 | P2 | covered | Self-inspection cuspa_policy (gap-cuspa) | Same policy duty from SEBI's circular of 3 Jul 2026. |
| SEBI-SB2-0045 | P2 | covered | Self-inspection cuspa_no_lender (gap-cuspa) | CUSPA securities never re-pledged or transferred to banks or NBFCs. |
| DP-0360 | P2 | merged | CC-D-CONCURRENT-AUDIT | Have the concurrent audit of risk-prone areas done; auditor reports to the DP by the 10th |
| NCL-BSE-0053 | P2 | covered | Settlement screen (gap-settle): securities pay-in from the pool account | Gross sell obligation delivered by DO instructions each pay-in; to confirm Ledgerline's NSDL pool instructions if the clearing member's pool is at NSDL. |
| NSE2-N0011 | P2 | covered | Settlement screen (gap-settle): pay-in obligations, early pay-in and the day's NCL files | T+1 funds and securities pay-in cut-offs are the settlement day's own work; not a calendar item. |
| LAW-0048 | P2 | merged | CC-T-FATCA-FORM-166 | File the FATCA/CRS statement of reportable accounts (Form 166, formerly Form 61B) by 31 May |
| LAW-0072 | P2 | added | CC-T-TDS-DEPOSIT | Deposit the month's TDS by the 7th |
| LAW-0075 | P2 | added | CC-T-TDS-RETURNS | File the quarterly TDS statements on time |
| BSE1-N0020 | P2 | merged | CC-T-TERMINAL-NISM | Review that every terminal user holds a valid NISM certificate; update it before expiry |
| GAP-0096 | P2 | added | CC-T-RMS-RULES | Put each new SEBI or exchange risk rule into the trading front end's RMS from its effective date |
| MCX-0076 | P2 | added | CC-T-ORDER-EVIDENCE | Check each quarter that every dealer-placed order has the client's order evidence (call recording, e-mail, signed slip) |
| NSE-0169 | P2 | added | CC-T-ORDER-EVIDENCE | Check each quarter that every dealer-placed order has the client's order evidence (call recording, e-mail, signed slip) |
| NSE-0229 | P2 | added | CC-T-CLIENT-CODE-MODS | Keep one designated ERROR account and record a reason code for each client code modification |
| NSE-N0055 | P2 | added | CC-T-ORDER-EVIDENCE | Check each quarter that every dealer-placed order has the client's order evidence (call recording, e-mail, signed slip) |
| NSE2-N0022 | P2 | covered | CC-T-CLOSING-AUCTION (gap-cuspa) | One-time readiness for NSE's Closing Auction Session from 7 Dec 2026. |
| SEBI-SB1-0142 | P2 | added | CC-T-ORDER-EVIDENCE | Check each quarter that every dealer-placed order has the client's order evidence (call recording, e-mail, signed slip) |
| SEBI-SB1-0143 | P2 | added | CC-T-ORDER-EVIDENCE | Check each quarter that every dealer-placed order has the client's order evidence (call recording, e-mail, signed slip) |
| SEBI-SB2-0077 | P2 | added | CC-T-IBT-CONTROLS | Yearly check of the internet and mobile trading controls: 2FA, IP capture, encryption, sessions and disclosures |
| SEBI-SB2-0088 | P2 | added | CC-T-IBT-CONTROLS | Yearly check of the internet and mobile trading controls: 2FA, IP capture, encryption, sessions and disclosures |
