Ledgy help articles: clients and KYC.

# Clients and KYC

## new-client
title: Open a new client account
screen: clients
roles: Ops maker, Ops checker
ask: add client; new client; open account; onboard client; create client; account opening; register new client
related: online-opening, activate-client, ucc-upload

Two ways:

- **Individuals with Aadhaar and PAN:** send them the online account opening link from [[apps|Online applications]]. They do e-KYC, DigiLocker, bank check, video verification and e-Sign on their phone.
- **Walk-in, HUF, corporate, NRI and others:** on [[clients|Clients & onboarding]] click **New client** and fill in the five steps (identity, bank & demat, segments & risk, nominee & declarations, review).

Either way the client lands at **KYC verification** and moves through the stages to **Active**: see "activate a client".

## online-opening
title: Review an online account application
screen: apps
roles: Ops checker, Compliance officer
ask: online application; ekyc application; review application; digilocker application; applicant; online account opening link; approve online application; reject application
related: new-client, activate-client

1. Share the link shown at the top of [[apps|Online applications]] (on your website, by WhatsApp, or with an AP code so the AP is credited).
2. When an applicant submits, the application appears here and the client is created at **KYC verification**.
3. Click **Open** to see every check (PAN, KRA, Aadhaar, bank account, face match, e-Sign) and the documents: the e-Aadhaar and PAN record from DigiLocker, or the UIDAI-signed offline e-KYC file.
4. There is no in-person verification: Aadhaar comes from DigiLocker or from UIDAI's offline e-KYC file (not older than 3 days), which SEBI's KYC circular accepts without IPV or video.
5. **Aadhaar (offline e-KYC): check manually** usually means the mobile and email the applicant confirmed are not the ones linked to that Aadhaar, so the file may be someone else's. Call the applicant, or ask them to use DigiLocker or download a fresh file themselves.
6. On Autopilot set to activate (always in Full autopilot), an application whose every check passed is activated by itself. Otherwise, or when something is marked **Check manually** or **Failed** (a name that differs from Aadhaar, say), check it, then move the client through the stages on [[clients|Clients & onboarding]], or **Reject** with a reason, which is sent to the applicant.

## activate-client
title: Activate a client
screen: clients
roles: Ops maker, Ops checker
ask: activate client; client stuck in kyc verification; advance onboarding; move client to next stage; client not active; ucc upload stage; in person verification; ipv; approve new client
check: client
related: ucc-upload, new-client

Stages: **Application → KYC verification → UCC upload → Active**. There is no in-person verification stage: online applications verify Aadhaar and PAN through DigiLocker, and branch clients are seen in person when their documents are checked.

1. On [[clients|Clients & onboarding]], click **Advance** on the client's row at each stage once that stage's work is done.
2. At **UCC upload**, download the **UCC upload file**, upload it on the exchange's UCC portal, and wait for acceptance.
3. Then an **Ops checker** clicks **Activate**. It must be a different person from whoever created the application (maker-checker).

The client can trade only once Active and accepted by the exchange.

## ucc-upload
title: Upload a client's UCC to the exchange
screen: clients
roles: Ops maker, Ops checker
ask: ucc upload; upload ucc; ucc file; register client code with exchange; exchange ucc portal; ucc not uploaded
related: activate-client, ucc-rejected

1. On [[clients|Clients & onboarding]], download the **UCC upload file**. It contains every client at the UCC upload stage, in the exchange's format.
2. Upload it on each exchange's UCC portal (NSE, BSE, MCX) for the segments the client chose.
3. When the exchange accepts it, an Ops checker clicks **Activate** on the client.

## ucc-rejected
title: Fix a UCC the exchange rejected
screen: clients
roles: Ops maker, Ops checker
ask: ucc rejected; exchange rejected ucc; ucc error; pan not verified ucc; ucc name mismatch; ucc upload failed; client code rejected by exchange
check: client
related: change-contact, kra-status, ucc-upload

The exchange's rejection file gives the reason. The usual ones:

1. **PAN and name don't match the Income Tax records:** correct the name to exactly as on the PAN (change request), then upload again.
2. **PAN not verified, or not linked to Aadhaar:** the client must link PAN and Aadhaar on the Income Tax site; then upload again.
3. **KRA status not validated:** see "KRA status".
4. **Segment not allowed** (derivatives without income proof): add the income proof as a document, or drop the segment.
5. **Mobile or email used by another client:** each client needs their own, unless it's a family member and declared as such.

Fix the detail, download the UCC upload file again from [[clients|Clients & onboarding]], and re-upload. A client whose UCC isn't accepted must not be activated.

## client-cant-trade
title: Why can't a client trade or pay out?
screen: clients
roles: Ops maker, Ops checker, Risk manager
ask: client cannot trade; client blocked; why is client blocked; client not allowed to buy; zero limit; client limit is nil; client account frozen; unblock client
check: client
related: debit-interest, inactive, margin-shortfall, kyc-pmla

Tell me the client code (for example "why is BH1003 blocked?") and I'll check the account. The usual causes:

- **Not Active yet:** still in onboarding ([[clients|Clients & onboarding]]).
- **Debit block:** in debit longer than the firm allows; buying is blocked until they pay ([[debits|Debtors & interest]]).
- **Inactive:** no activity for the set period; needs reactivation ([[inactive|Inactive accounts]]).
- **Blocked by compliance:** a PAN match with a debarment order, or a manual block with a reason ([[kyc|KYC & PMLA]]).
- **Margin breach:** limit used up; see [[rms|RMS & limits]].

## change-contact
title: Change a client's mobile, email, address, bank or nominee
screen: changes
roles: Ops maker, Ops checker
ask: change mobile number; change email; update address; change bank account; add bank account; change nominee; update client details; modify client details; client changed phone
related: kra-status, contract-notes

1. Open the client and click **Update contact details**, or let the client raise it from their portal.
2. A **new mobile or email** is confirmed with a code sent to the new number or address before it can be approved.
3. A **bank account** change needs proof (cancelled cheque or statement) and should be verified by a penny drop.
4. An **Ops checker** who didn't raise the request approves it on [[changes|Change requests]].
5. Approved changes apply at once, go into the audit trail, and are in the next KRA upload file.

Never change a bank account on a phone call alone. That's the commonest fraud route.

## client-groups
title: Put clients in groups
screen: clients
roles: Ops maker, Ops checker
ask: client group; tag clients; hni group; staff group; bulk tag; group clients; add clients to group; remove from group
related: change-brokerage, client-reports

Groups (tags) such as HNI, STAFF or ACTIVE-TRADER drive brokerage plans and group reports.

- **One client:** open the client and click **Edit groups**.
- **Many clients:** on [[clients|Clients & onboarding]], tick the clients, then **Add to group** or **Remove from group**.

A client in several groups pays the plan of the group assigned most recently, unless they have their own plan.

## portal-access
title: Give a client portal access or reset their password
screen: clients
roles: Ops checker, Admin
ask: client portal login; client forgot password; give client login; client cannot log in; reset client password; portal access; client portal password
related: change-contact

1. Open the client on [[clients|Clients & onboarding]] and click **Portal access** at the bottom.
2. A one-time password is shown once. Give it to the client through a separate channel (not the same email as the user ID).
3. The client signs in with their client code as the user ID and must set their own password.

Doing it again issues a new one-time password and ends their sessions, which is also how you reset a forgotten password.

## client-documents
title: Add client documents
screen: clients
roles: Ops maker, Ops checker, Compliance officer, Branch staff
ask: upload client document; add pan card; add address proof; attach kyc document; scan documents; client documents missing; income proof
related: kyc-pmla

1. Open the client, choose the document type (PAN card, address proof, photograph, signature, bank proof, income proof and so on).
2. Choose the file (PDF, JPEG or PNG, up to 5 MB) and click **Add document**.

Documents are encrypted and kept with their hash. Online applications attach every document automatically.

## inactive
title: Reactivate an inactive account
screen: inactive
roles: Ops maker, Ops checker, Compliance officer
ask: reactivate client; inactive account; dormant account; client inactive; reactivation; flag inactive; account turned inactive; client wants to trade again
check: client
related: kra-status, client-cant-trade

An account with no activity (trades, IPO bids, mutual fund orders, KYC updates) for the set period (24 months by default) is flagged inactive and gets no trading limit.

**To reactivate:**
1. Open the client and click **Request reactivation**. Record the in-person or video verification (IPV/VIPV) and the KRA status, which must be **Validated** or **Registered**.
2. An Ops checker or compliance officer who didn't make the request approves it on [[inactive|Inactive accounts]].
3. The inactivity clock restarts.

**Flagging:** [[inactive|Inactive accounts]] lists accounts due; select them and **Flag selected inactive**, or let Autopilot do it.

## kra-status
title: Fix KRA status and KRA uploads
screen: kyc
roles: Ops maker, Ops checker, Compliance officer
ask: kra status; kra not validated; kra rejected; kra upload; kra on hold; kyc registered; kra file; ckyc number
related: change-contact, ucc-rejected

1. New clients and every KYC change go to the KRA in the **KRA upload file** from [[kyc|KYC & PMLA]]. Autopilot builds it each evening.
2. Load the KRA's status file back on [[files|Files]]; each client then shows their KRA status.
3. **On hold or rejected:** the KRA's response says why (usually a missing or unclear document, or a name mismatch with PAN). Collect the document, update the details, and the next upload file resends it.
4. **CKYC:** download the **CKYC bulk request** for clients without a CKYC number and load CERSAI's response on Files.

A client can't be reactivated, and shouldn't trade new segments, unless the KRA status is Validated or Registered.

## kyc-pmla
title: Set client risk, re-KYC and screening
screen: kyc
roles: Compliance officer
ask: pmla risk; high risk client; set risk category; re-kyc due; periodic kyc; debarred client; sanctions list; sebi debarred; client matched debarred list
check: client
related: kra-status, surveillance

On [[kyc|KYC & PMLA]]:

- **Risk category:** Ledgerline suggests one from the rules (turnover against income, special categories, debarment); a compliance officer confirms with **Set risk**.
- **Periodic KYC:** due every 2 years for High risk, 8 for Medium, 10 for Low. When done, click **Record re-KYC**. Clients due within 30 days are reminded automatically.
- **Screening:** load SEBI and exchange debarment orders and UN sanctions lists on [[files|Files]]. A **PAN match blocks the client at once**; a name-only match is raised for review.

## reveal-pan
title: See a client's full PAN or bank account number
screen: clients
roles: Ops checker, Compliance officer
ask: see full pan; pan masked; show pan; full bank account number; unmask pan; account number hidden
related: audit-seals

PAN and bank account numbers are masked on screen to protect clients.

1. Open the client and click **Show** next to the PAN or bank account.
2. Give the reason (for example "KRA rejection, checking against the PAN card").
3. The full value is shown, and your name, the time and the reason go into the audit trail.

Only Ops checkers, compliance officers and admins can do this. Regulatory files (UCC, KRA) always carry the full values.

## nri-client
title: Register an NRI client
screen: nri
roles: Ops checker, Compliance officer
ask: nri client; register nri; pis account; nre nro; nri trading; nri tds
related: new-client

1. Open the client account as usual, type **NRI**.
2. On [[nri|NRI (PIS)]], click **Register NRI**: PIS bank, account type (NRE repatriable or NRO), PIS permission, and the custodial participant code if they'll trade derivatives.
3. Ledgerline then applies the NRI rules: no intraday or short selling in cash, derivatives only with a CP code and NRO funds, RBI ban and caution lists.
4. Each day, download the **PIS bank file** for the banks. Tax is deducted on capital gains automatically.

## client-reports
title: Give a client a statement or report
screen: statements
roles: Ops maker, Ops checker, Branch staff
ask: client statement; send ledger to client; email statement to client; ledger statement; holding statement; capital gains report; pnl report; tax report; download client report; statement for group
related: year-end-packs, portal-access

1. Open [[statements|Client reports]], choose the report (ledger, holdings, portfolio, P&L and capital gains, trade book, positions), the client, a group or all clients, and the period (up to 3 years).
2. **View report**, then **Print / PDF** or **Download CSV**.

Clients get the same reports themselves in their portal. Every print and download is recorded.

## add-segment
title: Add a segment (F&O, currency, commodity) for an existing client
screen: clients
roles: Ops maker, Ops checker
ask: add segment; activate f&o; enable derivatives; add fno segment; activate commodity; add currency segment; client wants to trade futures and options; segment activation
related: client-documents, ucc-upload

**Ledgerline doesn't have a segment change request for existing clients yet.** Segments are set when the account is opened. Until it's added, the regulatory steps are:

1. Collect **income proof** (salary slip, ITR, 6-month bank statement or net-worth certificate) and the signed **risk disclosure** for the segment. Add them as client documents.
2. Activate the segment for the client code on each exchange's UCC portal.
3. Ask your Ledgerline administrator to update the client's segments. It's on the list to become a proper maker-checker request.

Never enable derivatives without income proof on file: it's checked in exchange inspections.
