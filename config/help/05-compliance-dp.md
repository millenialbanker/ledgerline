Ledgy help articles: compliance, depository and corporate actions.

# Compliance

## regulatory-reports
title: Generate and upload regulatory reports
screen: reports
roles: Compliance officer, Ops checker
ask: regulatory report; exchange report upload; daily reports; holding report; funds and securities report; report not uploaded; report validation failed; mark uploaded
related: circulars

On [[reports|Regulatory reports]]:

1. Choose the report and date and **Download** it (generated from the ledger and records).
2. **Validate** checks it against the format rules; fix anything it lists (usually missing client data).
3. Upload it on the exchange or depository portal.
4. **Mark uploaded** with the acknowledgement number.

Every step is recorded. Autopilot generates and validates daily reports; uploads still need a person until the exchange connection is set up.

## dispatch-register
title: Signed contract notes, statements and the dispatch register
screen: dispatch
roles: Compliance officer, Ops checker, Ops maker
ask: dispatch register; proof of delivery; contract note bounced; email bounced; printed contract note; courier copy; signed pdf; digital signature certificate; documents held; statements not sent; margin statement; ras statement; dp statement; bounce report
related: regulatory-reports

Contract notes and statements go to clients as PDFs digitally signed with the firm's certificate. Every one is in the [[dispatch|Dispatch register]] with the file exactly as sent and its SHA-256 fingerprint: SEBI treats this log as proof of delivery.

1. **Digital signature** (Admin): load the firm's Class 3 document signer certificate (.pfx with its password), or connect a signing service or HSM. Without one, documents are **held**, never sent unsigned.
2. Contract notes are signed and sent from [[notes|Contract notes]]. Daily margin statements go out by a scheduled job; the weekly funds and securities, monthly account, DP and running account statements are [[periodic|Periodic work]], each with its due date. **Send statements** sends any of them by hand.
3. A **bounced** email (from the mail server, **Mark bounced**, or **Load bounce report**) opens a work item: print the signed PDF, send it by courier and record the courier reference with **Printed copy sent**. Clients who chose paper (ECN = N) get a printed copy every time.
4. **Check signature** re-verifies a stored PDF; **Download register** gives the log for inspection.

The mail server is set with LEDGERLINE_SMTP_URL (STARTTLS or TLS only).

## client-collateral
title: Client-level collateral file (SEGMON, CC01)
screen: reports
roles: Ops maker, Ops checker, Compliance officer, Risk manager
ask: segmon; cc01; cc02; client level collateral; collateral reporting to clearing corporation; ncl collateral file; client collateral upload
related: regulatory-reports

On [[reports|Regulatory reports]], **Client-level collateral (SEGMON: CC01)**:

1. **Review** downloads every segment with names and any shortage.
2. The **CM**, **FO** and **CD** buttons download each segment's upload file, gzip-compressed and named C_CC01_<member>_<DDMMYYYY>.csv.gz (the name starts C for cash, F for F&O, X for currency: C_CC01_<member>_<DDMMYYYY>.csv.gz; commodity collateral goes to MCXCCL). The firm's NSE member code must be set in Settings.
3. Cash allocated and securities re-pledged follow the end-of-day client allocation, so the two always agree.

Check the layout against NSE Clearing's current file specification before the first upload; it is revised from time to time.

## nomination
title: Nominees and opt-out declarations
screen: clients
roles: Ops maker, Ops checker, Compliance officer, Branch staff
ask: nominee; add nominee; change nominee; multiple nominees; how many nominees; nominee share; minor nominee; guardian; opt out of nomination; nomination declaration; nominee identity; joint account nomination; nominee on statement; more than 3 nominees
related: regulatory-reports

Under SEBI's nomination circular of 29 May 2026 (in force from 1 Sep 2026) a client may name up to **3** nominees. Only each nominee's **name and relationship** are required, and the date of birth of a nominee under 18. The share, identity detail (PAN, driving licence, last 4 digits of Aadhaar or passport), mobile, email and a minor's guardian are optional. Shares not given are equal, any odd amount going to the first nominee.

- **Opting out** is by the signed declaration or online, where the declaration is shown and accepted. A video is not accepted.
- **How it is declared**: online with e-Sign, a digital signature or an OTP to the registered mobile and email; or on paper with a signature (no witness), or a thumb impression with two witnesses (names and addresses).
- **Joint accounts**: nomination is optional, but giving or changing one needs the consent of every holder. Confirm that all holders consented.
- **Statements** show the nominees' names or only Yes/No, as the client chooses.

1. Open the client and use **Change nomination** (or the client asks from the portal). Tick **Nominee is under 18** to give a minor's date of birth (and, optionally, a guardian). Choose how it was declared; a thumb impression needs both witnesses' names and addresses. Choose what statements show, and for a joint account tick **All holders consented**. Choosing *Opted out* shows the declaration the client makes. A checker approves it like any other change.
2. Records from the previous back office load with the **NOMINEES** file on [[files|Files]].
3. The **Nomination status** report on [[reports|Regulatory reports]] lists clients with details missing or no declaration.
4. Nominations recorded before 1 Sep 2026 with more than 3 nominees are kept as they are. They show as **More than 3 nominees (review)** and each has a work item: ask the client for a new nomination of up to 3.

## penalty-pass-through
title: Pass a margin penalty to a client
screen: margin
roles: Compliance officer, Risk manager, Ops checker
ask: pass penalty to client; charge penalty to client; margin penalty pass through; cheque bounce penalty; hedge break penalty; penalty evidence; penalty pass-through setting
related: peak-margin

A clearing corporation's penalty for short collection of upfront margin is the firm's. Under NSE/INSP/64315 (for penalties from 1 Nov 2024) it may be charged to a client only when the client caused the shortfall:

- a cheque the client gave was **dishonoured**; or
- a **hedge break**: the client changed a hedge or a leg expired, and the firm's risk management policy says this can raise margins and was notified to clients at least 7 days before.

Pass-through is **off** by default (Settings → *Allow passing penalties to clients case by case*). When it is on, nothing is charged by itself: Autopilot only puts each penalty in the work queue.

1. On [[margin|Margin reporting]], **Prepare penalties** lists them (nothing is debited); then a Compliance officer or Risk manager clicks **Pass to client…** on the row, chooses the reason and gives the dishonoured cheque (recorded as bounced on Receipts) or the policy clause and the date clients were notified of it.
2. Attach the supporting documents (PDF or image): the bank's return memo, or the policy extract and its notice.
3. A different person (Ops checker, Risk manager or Compliance officer) approves or rejects it, on Margin reporting (**Approve and post** or **Reject**, with a reason) or from the work queue. Only then is the client's ledger debited.
4. The client gets a signed letter with the documents, and the margin statement gives the reason and the evidence. Every step is in the audit trail.

Without an allowed reason, dismiss the item: the firm bears the penalty.

## segment-status
title: Client status in each segment
screen: clients
roles: Ops checker, Compliance officer
ask: segment status; suspend derivatives; suspend segment; dormant segment; reactivate segment; close segment; fo suspended; segment history
related: inactive

Each segment (cash, F&O, currency, commodity) has its own status: Active, Reactivated, Suspended, Dormant or Closed, with every change kept with its effective date and reason.

1. Open the client, find **Segments** and choose **Change**. Give the effective date and the reason.
2. The front-office limit file allows only the open segments; the day's changes go into the UCC upload batch.
3. History from the previous back office loads with the **SEGMENT-STATUS** file on [[files|Files]].

## surveillance
title: Work a surveillance or AML alert
screen: surv
roles: Compliance officer
ask: surveillance alert; aml alert; suspicious transaction; file str; close alert; exchange alert; turnover more than income; fiu
related: kyc-pmla, kill-switch, surveillance-return

On [[surv|Surveillance & AML]]:

1. Exchange alerts and internal rules (turnover against income, third-party funds, frequent client code changes, dormant accounts) land in one queue. **Run internal rules** to scan again.
2. **Review**: look at the client's trades, funds and history; get the client's explanation if needed.
3. **Close** with a written rationale, or **File STR** with FIU-IND if it's suspicious.

Closing or filing needs the compliance officer. Never tell the client an STR was filed.

Each quarter the alerts' status goes to the exchanges in the surveillance obligation report (Formats I to III, within 15 days of the quarter's end): record **Referred to exchange** and other actions here as they happen (see *Quarterly surveillance obligation report*).

## complaints
title: Handle an investor complaint
screen: helpdesk
roles: Ops maker, Ops checker, Compliance officer
ask: complaint; investor grievance; scores complaint; client complaint; service request; resolve complaint; complaint overdue; monthly complaint disclosure
related: portal-access

On [[helpdesk|Investor complaints]]:

1. Complaints from the portal arrive by themselves; **Record a complaint** for email, phone, branch, SCORES or exchange ones.
2. **Reply**, add an **Internal note**, or **Reply & resolve**.
3. Deadlines: complaints within **21 calendar days**, service requests within **7**. Autopilot assigns and escalates.
4. The **monthly disclosure** table (choose the month, **Show**) is what you publish on your website by the 7th.

## circulars
title: Track circulars and compliance deadlines
screen: circulars
roles: Compliance officer
ask: circular; new sebi circular; exchange circular; compliance calendar; due dates; compliance deadline; recurring compliance; what is due this month
related: regulatory-reports, ras-calendar, compliance-profile, shared-filing, surveillance-return

On [[circulars|Circulars & calendar]]:

1. **Load circulars file** (or they arrive once the exchange connection is set up). Each is sorted by area and priority with suggested actions, owners and due dates.
2. Work each action: **Done**, **Not needed**, **Not applicable**, or **Add action**.
3. The **calendar** lists recurring obligations (complaints disclosure, CTR, RAS, audits, net worth, cyber audit, AP inspection). **Mark done** with the evidence each time.
4. **RAS dates & profile** holds the exchanges' joint RAS calendar (see *Keep the exchanges' RAS dates*) and the system audit cycle, auditor and MTF approval that date the audit and net worth duties (see *System audit cycle, auditor and MTF*).
5. Common submissions that a member of NSE and other exchanges files with NSE only show once, marked **File with NSE (shared with the other exchanges)** (see *Submissions filed with NSE only*).

Items are queued a week ahead. Check the seeded due dates against current circulars.

## whatsapp-consent
title: Get clients' WhatsApp consent
screen: messages
roles: Ops checker, Compliance officer
ask: whatsapp consent; whatsapp opt in; client not getting whatsapp; whatsapp not sent; opt out; stop whatsapp
related: message-templates

WhatsApp messages go only to clients who opted in, and every choice is recorded.

1. On [[messages|Client messages]] → WhatsApp consent, see who has opted in.
2. **Ask by email** or **Ask by SMS** sends a one-tap consent link to clients not yet asked.
3. To record consent given another way (signed form, recorded call), open the client → WhatsApp → **Record consent** with a reference.
4. Clients can reply STOP at any time; that's recorded too.

## audit-seals
title: Check the audit trail hasn't been tampered with
screen: audit
roles: Compliance officer, Admin
ask: audit trail; check seals; tampering; who changed this; audit log; data protection; encryption status; was the log edited
related: reveal-pan

On [[audit|Audit trail]]:

- Filter by user, action or record to see who did what and when.
- **Check seals** verifies every entry against the one before it. A changed, removed or inserted entry is listed as a problem; treat that as a security incident.
- Record the **latest seal** shown in your IT committee minutes, so removal of the newest entries can be detected too.
- The panel also shows how client data is encrypted and which key version is in use.

# Depository

## dis
title: Process a delivery instruction slip (DIS)
screen: dp
roles: Ops maker, Ops checker
ask: dis; delivery instruction slip; enter slip; verify dis; dis book; transfer shares; off market transfer; dis rejected; slip already used
related: dp-requests

On [[dp|Demat accounts & DIS]]:

1. **Issue DIS book** to the client (slip numbers are registered).
2. A maker **Enters a slip**: slip number, the client's demat account it debits (the primary unless another is chosen), ISIN, quantity (up to 4 decimals for units held in demat), target account. Only that account's free balance counts, and a slip from a book issued for another account is refused.
3. A checker **Verifies** it against the slip register, the signature and the free balance (large values need a second check), or **Rejects** it.
4. Verified slips go in the upload batch; mark **Executed** or **Failed** from the depository's response.

"Slip already used" means that number was entered before. A slip can be used only once.

A slip from an account CDSL has frozen for debits (the whole account, or the ISIN in full) is refused, and a slip already verified is not executed if a freeze arrives before execution (see [[dp|CDSL freezes]]). A part of an ISIN frozen at CDSL is taken out of the free balance.

**CDSL's DIS master (DIS_MSTR).** Loading it on [[xfiles|Exchange files]] checks CDSL's slips against the books issued here:
- a slip CDSL has cancelled, blocked or made inactive is marked **lost** on its book, so it cannot be accepted;
- a slip CDSL shows as used that has no instruction here, a slip lost here but active at CDSL, and slips Ledgerline did not issue go to the work queue for the DP desk.

## cdsl-client-master
title: CDSL client master file (CLN_MSTR): what it changes
screen: dp
roles: Ops maker, Ops checker, Compliance officer
ask: cdsl client master; cln_mstr; client master export; client master file from cdsl; ddpi flag from cdsl; poa flag; does the client have ddpi; why does the client need edis; account status at cdsl; pending activation at cdsl; account closed at cdsl; bsda flag from cdsl; cdsl says different pan; cdsl details differ from client master; accounts not in cdsl full file; harmonised client master; udiff client master
related: cdsl-freeze, cdsl-edis, dis, cdsl-demat-api

CDSL's client master export (CLN_MSTR, the harmonised layout of SEBI's UDiFF catalogue) has every demat account of the DP as CDSL holds it, one line per purpose: the first holder with each address, joint holders, nominees, POA and DDPI links, BO-UCC links. CDSL sends an incremental file (I, the accounts that changed) daily and a full file (F) on request. Load it on [[xfiles|Exchange files]]; it is on the daily file checklist for a firm that is a CDSL DP.

For each account linked to a client (by its BO ID), Ledgerline records on [[dp|Demat accounts & DIS]]:
- **Account status**: closed at CDSL closes the account here (with the reason and date); pending activation is shown so. A freeze the DP put on stays until it is lifted here.
- **BSDA**: BSDA, non-BSDA or opted out; the DP tariff follows it.
- **POA / DDPI**: the links with their nature (settlement, margin pledge, mutual fund, tender offer), holders and validity. The account **has DDPI** when an active link for settlement (or a general POA) covers the first or all holders, and, once the firm's DDPI POA ID is set in the CDSL pledge settings, is to that master. With DDPI, sales are delivered under it; **without it, every sale needs eDIS (TPIN) or a slip**, and live risk counts a sale not authorised by eDIS as short.
- the nomination flag and number of nominees, PAN verification, whether the mobile and email are verified, the clearing members of the BO-UCC links and CM mappings, and CDSL's freeze status (an account CDSL shows as frozen is blocked like a freeze from the freeze master).

**Nothing in the client's KYC is overwritten.** Where CDSL's name, PAN, date of birth, mobile, email, bank or nomination differ from the client master, a work item lists the differences (masked) for the DP desk: correct the client with the client's request, or the account at CDSL with a BO modify. Accounts no client is linked to, a DDPI that appears or ends, a closure and a BSDA change also go to the work queue. Aadhaar is never kept: it is masked in the stored copy of the file.

A **full** file's last part (_P0) also lists the DP's accounts Ledgerline has as open that CDSL's file does not: check them in CDAS. Load every part of a file that came in parts, the _P0 part last.

## cdsl-freeze
title: CDSL freezes (freeze master DPE6): what they block
screen: dp
roles: Ops maker, Ops checker, Compliance officer, Risk manager
ask: cdsl freeze; dpe6; online freeze master report; 18dpe6u; account frozen by cdsl; frozen for debit; frozen for credit; isin frozen; partial freeze; freeze by sebi order; freeze by court order; unfreeze at cdsl; freeze lifted; why can't the client sell frozen; payout held frozen account; regulatory authority freeze
related: cdsl-client-master, dis, dp-requests, cdsl-edis

CDSL's **online freeze master report (DPE6)** lists every freeze on the DP's accounts: set up, unfrozen, cancelled, released or failed, for the whole account or one ISIN, in full or for a quantity, with the reason, who initiated it and, since CDSL's communiqué CDSL/SURV/DP/POLCY/2026/621, the regulatory authority, its details, the type of order and the reason for delay. Load each file on [[xfiles|Exchange files]] as it arrives (CDSL names it like 18DPE6U…).

While a freeze is in force (from its activation date to its expiry, unless lifted):
- **frozen for debits, whole account**: no DIS (entry or execution), no eDIS, no pledge, no early pay-in, no settlement pay-in from the account; funds payouts are not approved automatically but wait for a checker;
- **frozen for debits, one ISIN**: the same for that ISIN; a **partial** freeze takes its quantity out of the free balance;
- **frozen for credits**: no pay-out of securities to the account (or of that ISIN) and no IPO bid that would credit it.

A new freeze and a lifted one each go to the work queue. Unfreeze, cancel and release records lift the block; a failed freeze never takes effect. These freezes are CDSL's and are kept apart from the DP's own freeze requests, so an unfreeze request here does not lift them. Expiry and future activation are applied each morning.


## dp-requests
title: Handle demat, freeze, transmission or closure requests
screen: dp
roles: Ops maker, Ops checker
ask: dematerialisation; demat request; freeze account; transmission; close demat account; account closure; rta
related: dis, cdsl-demat-api

On [[dp|Demat accounts & DIS]], **New request** and choose the type. Demat requests are **Sent to RTA**, then completed or rejected from the RTA's response. A closure needs the holdings transferred first.

With CDSL's account transfer cum closure API switched on, an approved closure of a CDSL account goes to CDSL instead: the client confirms with CDSL's OTP and the account closes when CDSL confirms (see [[cdslreq|CDSL requests]]).

## dp-billing
title: Run DP billing
screen: dp
roles: Ops checker
ask: dp charges; dp bill; amc charges; dp tariff; depository charges; pool account; free dp charges for trades through us; no dp charge on pay-in; credit charges; debit charges
related: dis, dp-tariff-plans

On [[dp|Demat accounts & DIS]]: set the tariff (**Save tariff**), then **Run DP billing** each month. Charges are debited to the trading ledger with GST.

**Charge-free accounts.** List the firm's own pool (CM) accounts, one per line, as CDSL or NSDL, then the account ID, then a label:
- **CDSL:** the 16-digit BO ID.
- **NSDL:** the DP ID and client ID together (IN followed by 14 digits).

You can list as many as you need. Transfers to or from these accounts carry no debit or credit charge, so deliveries for trades done through the firm are free.

**The firm's clearing member codes.** On-market pay-ins go straight from the client's account to the clearing corporation, not through the pool. So also enter the firm's clearing member code(s) as the CDSL transaction file shows them. The screen lists the codes found in loaded transactions to choose from. Pay-ins and pay-outs settled under these codes are not charged. Every other debit is charged the debit charge per ISIN, and every other credit the credit charge per ISIN (0 for none). The bill shows how many pool transfers were not charged. The counterparty is read from the CDSL transaction file.

## dp-tariff-plans
title: DP tariff plans and annual maintenance
screen: dp
roles: Ops checker, Admin
ask: dp tariff plan; tariff basket; basket from sharepro; change dp tariff of an account; different dp charges for some clients; amc paid up to; amc paid in advance; yearly amc; lifetime amc; corporate amc; dp plan history; which tariff is a client on; staff dp tariff; zero dp charges for some accounts
related: dp-billing, migrate-reconcile

Most DPs charge some accounts differently: staff, a group of clients, pool accounts. These are **tariff plans** (SharePro calls them baskets). An account on a plan pays the plan's rates; every other account pays the firm tariff.

1. **The plans.** On [[dp|Demat accounts & DIS]] → **Billing & tariff**, **Tariff plans** lists each plan with its accounts, maintenance and charges. A plan has dated versions; the bill uses the version in force in the month billed. Plans come with the migration (DP-TARIFFS) or on **Files → Other uploads**.
2. **Charges.** Each line is a depository transaction type (off-market delivery, pay-in, demat and so on): a flat amount, a % of value or an amount per certificate, with a minimum and maximum. A demat request adds the courier charge once. **Own trades** is the rate for transfers to or from the firm's pool accounts or clearing member; blank means free.
3. **Maintenance.** On a yearly plan the account pays a year in advance when its **AMC paid up to** date falls due, from the day after it; the date then moves on a year. **For life** means never billed again. A body corporate also pays the plan's corporate maintenance. On a monthly plan it pays a twelfth each month.
4. **Move an account to another plan.** In the accounts table, **Tariff → change** (Ops checker or Admin): pick the plan, or the firm tariff, and the date it applies from. Earlier plans stay in the account's history, so a past month bills as it was.

- After a migration, the reconciliation checks the plan versions, the accounts on a plan and those with maintenance paid ahead against the old system.
- Reloading the DP's accounts (DP-ACCOUNTS) keeps each account's plans and paid-up date.
- The old system's other rate set (SharePro's SP rates) is shown on the plan for the record and is not billed. Ask the office what it was for.

# Corporate actions and IPO

## corporate-actions
title: Record a dividend, bonus or split
screen: corp
roles: Ops maker, Ops checker
ask: dividend; bonus issue; stock split; corporate action; holdings wrong after bonus; capital gains after split; record date
related: buyback

On [[corp|Corporate actions]]:

1. **Record a corporate action**: type, scrip, ratio, record and ex dates.
2. An Ops checker clicks **Apply**. Every holding and purchase lot changes (bonus shares cost nil from allotment; a split keeps the acquisition date), so capital gains stay right.
3. **Dividends:** when received for shares the firm held for clients on the record date (CUSPA), click **Dividend received** to pass them on.

## buyback
title: Tender shares in a buy-back
screen: corp
roles: Ops maker, Ops checker
ask: buyback; buy back; tender offer; tender shares for client; buyback acceptance; open offer
related: corporate-actions, client-insights

1. **Record a buy-back offer** on [[corp|Corporate actions]] with price, dates and the small-shareholder and general ratios.
2. Clients who hold the share are told automatically (Client insights). **Tender for a client** on their request.
3. Download the **Bid file** for the exchange window.
4. After the offer, **Enter acceptance**: accepted shares are paid, the rest returned.

## ipo
title: Handle IPO bids and mutual fund orders
screen: ipo
roles: Ops maker, Ops checker
ask: ipo; ipo bid; asba; upi mandate; ipo allotment; mutual fund order; mf order; allotment file
related: client-reports

On [[ipo|IPO & mutual funds]], bids come from the exchange bidding platform with the client's UPI mandate. **Upload allotment file** from the registrar to record allotments. Mutual fund orders through exchange platforms are listed here too.
