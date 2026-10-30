Ledgy help articles: CDSL APIs for transactions, DESTAT, eDIS and reports.

# CDSL APIs: transactions and eDIS

## cdsl-txn-edis
title: CDSL APIs: transactions and eDIS
screen: cdsltxn
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Admin
ask: cdsl api; cdsl transaction upload api; common upload api; send dis to cdsl by api; dis upload api; cdas upload; edis; e-dis; cdsl tpin; authorise sale; destat; cdsl report setup api; cdsl sftp reports; dp api
related: cdsl-edis, cdsl-edis-tpin, cdsl-destat, cdsl-report-setup

A firm that is a CDSL DP can send its work to CDSL through CDSL's APIs instead of uploading files in CDAS by hand. All of them use one connection, **Connections → CDSL APIs (DP)**, which the Admin fills in: the DP ID, Mock or Live, the API user, its password and secret key, the API keys CDSL issued (eDIS, DESTAT and reports may each have their own) and one switch per API family. Switch a family on only after it has passed CDSL's mock testing.

**Delivery instructions (common upload).** On **Depository → Transactions (API)**:

1. Slips are entered and verified on Demat accounts & DIS as before: nothing goes to CDSL until a **checker** has verified it.
2. **Send to CDSL by API** makes the transaction upload file of the verified slips (off-market, inter-depository and on-market), zips it and uploads it. CDSL gives an acknowledgement number. The scheduler also does this every five minutes on business days, 08:00–18:30.
3. Ledgerline asks CDSL for the status and, when CDAS has processed the file, downloads the response file and marks each slip **executed** (the holding moves out and the transfer is billed) or **failed** with CDSL's rejection code.
4. If CDSL did not process the file, the slips go back to Verified and a work item is raised. If a line in the response cannot be matched, a work item asks you to read the file (it is kept on the screen) and record the result on the DIS tab.

With the API off, **Upload file for CDAS** gives the same file to upload in CDAS by hand; record the results on the DIS tab as before.

**What the DP must get from CDSL first:** the API documents from its relationship manager; mock details from CDSL's helpdesk; a separate CDAS user for the APIs with its secret key; the server's public static IP address listed with CDSL (needed for the common upload, TPIN generation and pledges); success logs from mock testing sent from the DP's registered e-mail to enable the DP in Live; and, for eDIS, CDSL's registration form.

## cdsl-edis
title: eDIS: clients authorise sales with the CDSL TPIN
screen: edis
roles: Ops maker, Ops checker, Risk manager, Branch staff
ask: edis authorisation; client cannot sell shares; sell without ddpi; edis not authorised; short delivery edis; tpin authorisation failed; edis cancel; revoke edis; edis inquiry; edis status not received
related: cdsl-edis-tpin, cdsl-txn-edis

A client without a DDPI must authorise a sale from the demat account on CDSL's page with the CDSL **TPIN** and an OTP.

1. In the client portal, **Authorise sale (CDSL TPIN)** lists the shares that are free (holdings less pledges and pending slips). The client enters the quantities (up to 10 shares at a time) and goes to CDSL's page. Staff can do the same on **Depository → eDIS authorisations → Authorise for a client**, with the client present.
2. CDSL sends the client back with the result. Ledgerline takes it only if it decrypts with the firm's eDIS key and matches a request sent from here, and records each share as authorised (with CDSL's transaction ID) or failed with the reason. The client gets an e-mail and SMS.
3. If the client closed CDSL's page, the scheduler asks CDSL for the status after 10 minutes (up to three times, the same day only). **Ask CDSL** does it at once. Requests left unanswered by the next day show as **No answer**.
4. A pre-trade authorisation lasts the number of days set in the connection. **Cancel** revokes the unused quantity; CDSL allows it 20 minutes after the authorisation and asks for an OTP on its page.

**Live risk:** with eDIS on, a delivery sell by a client who has no DDPI counts as covered only up to the eDIS-authorised quantity, not the whole holding. The rest shows as short delivery "(not authorised by eDIS)" and carries the auction reserve.

The connection needs the firm's clearing member ID for each exchange (as in CDSL's CM master) and the eDIS version agreed with CDSL (1.2 sends the settlement date with each share).

## cdsl-edis-tpin
title: Generate or change a client's CDSL TPIN
screen: edis
roles: Ops maker, Ops checker, Branch staff
ask: generate tpin; client forgot tpin; tpin not received; change tpin; reset cdsl tpin; new tpin for edis
related: cdsl-edis

CDSL sends a new TPIN to the mobile number and e-mail it has for the demat account.

- Clients ask for one in the portal (**Get a new TPIN**). Staff ask on **eDIS authorisations → Generate TPIN**, recording how the client asked (call reference, letter or visit): CDSL allows TPIN generation only on the client's genuine request. At most three a day per client.
- **Change TPIN** opens CDSL's change-PIN page with the BO ID and PAN filled in; the client completes it there. If they are not filled in, the client enters them.
- If the TPIN does not arrive, check the mobile and e-mail on the demat account with CDSL. CDSL does not generate a TPIN for a POA master account.

## cdsl-destat
title: Demat mutual fund units by DESTAT
screen: cdsltxn
roles: Ops maker, Ops checker
ask: destat; demat mutual fund units; convert soa units to demat; mf units statement of account to demat; destat request number; drn
related: cdsl-txn-edis

DESTAT converts mutual fund units held in a statement of account into the demat account.

1. Raise a **Demat** request on Demat accounts & DIS → Requests with the ISIN, quantity (or "all"), DRF number and the **folio** number (no certificates).
2. A **checker** (not the person who raised it) opens **Transactions (API) → DESTAT** and clicks **Send to CDSL**. CDSL shows the request to the investor, who confirms with the OTP CDSL sends.
3. CDSL returns a DESTAT request number (DRN). The request moves to **Sent to RTA** and is completed when the RTA confirms, like any demat request. If CDSL does not accept it, a work item gives the reason.

## cdsl-report-setup
title: Ask CDSL for DP reports through the report setup API
screen: cdsltxn
roles: Ops maker, Ops checker, Compliance officer
ask: cdsl report setup; dp97 report; dpz6 report; cdsl sftp report; daily cdsl report setup; report not received from cdsl
related: cdsl-txn-edis

On **Transactions (API) → Reports**, give the module ID, the report ID and its parameters, separated by ~ (DP97: from_to~F or ~I; DPZ6: a BO ID with the other values blank, or the account status, date type M or S, and the date range). **Set up daily** sends it each business day at the time given, with {prev} and {today} replaced by dates.

CDSL sends its answer to Ledgerline's call-back address. A report file whose layout Ledgerline knows is loaded as if uploaded on Files; everything that arrived is listed.

If CDSL gives the DP an SFTP folder for its reports, enter it in the connection. This build of Ledgerline has no SFTP client, so the screen says so and reports come in through the call-back or on Files.
