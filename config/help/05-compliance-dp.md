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
ask: nominee; add nominee; change nominee; multiple nominees; nominee share; minor nominee; guardian; opt out of nomination; nomination declaration; nominee identity
related: regulatory-reports

Under SEBI's 2025 nomination rules a client may name up to 10 nominees, each with a share (equal if not given), a relationship and one identity detail: PAN, driving licence, the last 4 digits of Aadhaar, or passport. A nominee under 18 needs a date of birth and a guardian. A sole holder must either nominate or declare that they do not wish to.

1. Open the client and use **Change nomination** (or the client asks from the portal). A checker approves it like any other change.
2. Records from the previous back office load with the **NOMINEES** file on [[files|Files]].
3. The **Nomination status** report on [[reports|Regulatory reports]] lists clients with details missing or no declaration.

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
related: kyc-pmla, kill-switch

On [[surv|Surveillance & AML]]:

1. Exchange alerts and internal rules (turnover against income, third-party funds, frequent client code changes, dormant accounts) land in one queue. **Run internal rules** to scan again.
2. **Review**: look at the client's trades, funds and history; get the client's explanation if needed.
3. **Close** with a written rationale, or **File STR** with FIU-IND if it's suspicious.

Closing or filing needs the compliance officer. Never tell the client an STR was filed.

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
related: regulatory-reports

On [[circulars|Circulars & calendar]]:

1. **Load circulars file** (or they arrive once the exchange connection is set up). Each is sorted by area and priority with suggested actions, owners and due dates.
2. Work each action: **Done**, **Not needed**, **Not applicable**, or **Add action**.
3. The **calendar** lists recurring obligations (complaints disclosure, CTR, RAS, audits, net worth, cyber audit, AP inspection). **Mark done** with the evidence each time.

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
2. A maker **Enters a slip**: slip number, ISIN, quantity, target account.
3. A checker **Verifies** it against the slip register, the signature and the free balance (large values need a second check), or **Rejects** it.
4. Verified slips go in the upload batch; mark **Executed** or **Failed** from the depository's response.

"Slip already used" means that number was entered before. A slip can be used only once.

## dp-requests
title: Handle demat, freeze, transmission or closure requests
screen: dp
roles: Ops maker, Ops checker
ask: dematerialisation; demat request; freeze account; transmission; close demat account; account closure; rta
related: dis

On [[dp|Demat accounts & DIS]], **New request** and choose the type. Demat requests are **Sent to RTA**, then completed or rejected from the RTA's response. A closure needs the holdings transferred first.

## dp-billing
title: Run DP billing
screen: dp
roles: Ops checker
ask: dp charges; dp bill; amc charges; dp tariff; depository charges
related: dis

On [[dp|Demat accounts & DIS]]: set the tariff (**Save tariff**), then **Run DP billing** each month. Charges are debited to the trading ledger with GST.

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
