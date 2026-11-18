Ledgy help articles: public issues (IPOs, SME IPOs, NCDs, offers for sale).

# Public issues

## ipo-issue-setup
title: Set up a public issue
screen: ipo
roles: Ops maker, Ops checker
ask: new ipo; set up ipo; add issue; ipo price band; lot size; ipo categories; red herring prospectus; import issues; sme ipo setup; ncd issue setup
related: ipo-bid, ipo-rules

On [[ipo|IPO & public issues]], **New issue** (or **Import issues** from a CSV):

1. The **symbol** as the bidding platform shows it, the type (Mainboard, SME, NCD, OFS), the exchanges, the bidding dates and the price band (the floor price for an offer for sale, the face value for NCDs) and the lot size.
2. **Categories** come filled in for the type; adjust them from the red herring prospectus: the limits, cut-off, whether only individuals may bid, the discount, and the platform's category code if it differs.
3. Bidding hours default to 10:00 to 17:00, closing at 16:00 on the last day (T), with UPI mandates accepted until 17:00. Offers for sale default to 09:15 to 15:30.
4. After the basis of allotment, enter the **final issue price** and the **ISIN**. Set the **commission** the prospectus pays (per application or as a percentage of the value allotted).

The dates of allotment (T+1), release of blocked money and demat credit (T+2) and listing (T+3) are worked out from the closing date and the trading holidays.

## ipo-bid
title: Take an IPO bid for a client
screen: ipo
roles: Ops maker, Ops checker, Branch staff, Authorised person
ask: apply ipo for client; place ipo bid; ipo application; client wants to apply ipo; upi id for ipo; new bid; ipo acknowledgement; client instruction ipo
related: ipo-rules, ipo-bid-file, ipo-mandates

A bid needs the client's **prior instruction**. On [[ipo|IPO & public issues]], **New bid**:

1. Choose the issue, the client and the category; give one option (up to three in a book-built issue): lots, and a price or cut-off.
2. The client's **own UPI ID**. It must be on record for the client with how it was checked (name check, bank statement, signed form), or seen on a payment the client made to the firm. A UPI ID on record for another client is refused: the bank rejects third-party UPI IDs. Above ₹5 lakh, and for non-individuals, the money is blocked in the client's own bank account (ASBA) instead.
3. Record **how the client instructed** (recorded phone line, email from the registered address, signed instruction, SMS or WhatsApp from the registered number), its reference and when it was given.

Ledgerline checks the bid, keeps the instruction in the consent log and sends the client an **acknowledgement** with its number. The bid waits as a draft until it goes to the exchange.

## ipo-rules
title: IPO bid rules Ledgerline checks
screen: ipo
roles: Ops maker, Ops checker, Branch staff, Authorised person
ask: ipo limit; retail limit 2 lakh; nii limit; snii bnii; cut off price allowed; sme two lots; one application per pan; employee reservation; shareholder quota; why was the ipo bid refused; ncd category
related: ipo-bid, ipo-modify-cancel

- **Retail individual** (RII): up to ₹2 lakh, cut-off allowed. **Non-institutional**: above ₹2 lakh to ₹10 lakh (small) and above ₹10 lakh (big), no cut-off. **Employees**: up to ₹5 lakh, cut-off allowed. Shareholder and policyholder reservations: cut-off allowed. Amounts are at the cap price for a cut-off bid, less any discount.
- **SME issues** (from 1 July 2025): an individual bids exactly 2 lots; no cut-off price in any category.
- **One application per PAN** in an issue, across all of the firm's accounts.
- The client must be active, with a valid PAN and a demat account (not closed or frozen for credits where the firm is the DP), and not debarred.
- **UPI** for individuals up to ₹5 lakh; the UPI ID must be the client's own.
- **NCDs**: categories I to IV from the prospectus, at face value; series if the issue has them.
- Bids are taken from the opening date (or the early-application date) until the close at 16:00 on T.

## ipo-modify-cancel
title: Modify or cancel an IPO bid
screen: ipo
roles: Ops maker, Ops checker, Branch staff, Authorised person
ask: modify ipo bid; cancel ipo bid; withdraw ipo application; revise ipo bid; increase ipo bid; lower ipo bid; why can't i cancel the bid
related: ipo-rules, ipo-bid-file

Open the bid on [[ipo|IPO & public issues]] and use **Modify** or **Cancel bid**, recording the client's instruction as for a new bid.

- Retail, employee, shareholder and policyholder bids (up to ₹2 lakh on mainboard) can be raised, lowered or cancelled until the close.
- Non-institutional bids can only be **revised upward**: no fewer lots, no lower price, no cancellation.
- **SME** bids cannot be cancelled or lowered in any category.
- A bid not yet sent to the exchange is changed or cancelled at once; otherwise the change goes with the next bid file (or API call) and takes effect when the exchange accepts it.

## ipo-bid-file
title: Send the day's IPO bids and load the responses
screen: ipo
roles: Ops maker, Ops checker
ask: ipo bid file; upload bids to eipo; ibbs upload; bulk upload ipo; ipo response file; ipo error file; eipo success file; bid rejected by exchange; dp id pan mismatch
related: ipo-eipo-api, exchange-files

1. During bidding hours, on the Issues tab, click **NSE bid file** or **BSE bid file**. Ledgerline writes one file per issue and exchange with every bid waiting to go (new, modified, cancelled), keeps it, and marks the bids sent.
2. Upload it on NSE's eIPO (bulk upload) or BSE's iBBS.
3. Load the **success and error files** the platform returns (Files & eIPO API tab, or [[xfiles|Exchange files]]). Accepted bids wait for the client's UPI mandate; rejected ones show the reason. A depository mismatch (DP ID, client ID, PAN) is shown on the bid: correct it and modify before the close.

**The layouts are provisional.** NSE (circular NSE/IPO/58442, Annexure 2; eIPO Misc > File Format) and BSE (notice 20230110-44, Annexure A) give the standard layouts to members; until they are in hand, Ledgerline's layouts carry the field names NSE's eIPO uses and are marked provisional on Exchange files. Correct them before the first live upload.

## ipo-eipo-api
title: Connect NSE's eIPO API and callback
screen: connections
roles: Admin
ask: eipo api; nse ipo api; ipo callback; eipo callback url; send ipo bids by api; whitelist callback; login without otp; ipo master; new ipo set up automatically; ipo allotment api; appdpstatus; apppaystatus; asba by api; asba bank code; asba location code; eipo v3; asba forms; eipo v2; eipo advanced security; eipo rsa key; nse public key for eipo; eipo encryption
related: ipo-bid-file, connections

NSE gives API access to members on request: email msm@nse.co.in with the member code and user ID (API access, and login without OTP), and the callback address to whitelist (4 to 5 working days).

Ledgerline follows NSE's Protocol for WEB API, Initial Public Offer System (2.0.0): the API version is **v3**, signed in with a Bearer token. NSE also offers **v2**, advanced security (see below). If NSE still has the firm on 1.20, choose **v1** under API version.

1. In [[connections|Connections]], **NSE eIPO API**: member code, user ID, password, Live or Mock (UAT), and a **callback token** (a long random phrase). If NSE registered a separate callback password, enter it too.
2. Give NSE the callback address shown on [[ipo|IPO & public issues]] (Files & eIPO API), with your token in it; NSE adds the version and `appdpstatus`, `apppaystatus` or `notification` (for example `/v3/apppaystatus`). Optionally list the addresses NSE calls from.
3. Switch **Send bids through the API** on: waiting bids then go every 5 minutes during bidding hours, and bids in flight are asked about every 15 minutes. Ledgerline gives each application its number (member code and a running number) and keeps NSE's bid numbers, so a change or cancellation goes against the bids NSE holds. Callbacks update the depository check and UPI mandate as they come.
4. **Set up new issues from NSE's IPO master** (on unless switched off): each morning at 08:30, and when NSE says an issue changed, Ledgerline sets up the coming mainboard, SME and debt issues it does not have (price band, lot, dates, the UPI categories and any discount) and keeps their dates and price band in step. Set the commission on the issue if the issuer pays one. Issues set up by staff are left alone.
5. **Read allotment from NSE** (on unless switched off): from the allotment date, every half hour for three days, Ledgerline reads the issue's allotment and records it (and the final price, if not set): allotted lots for each application named, none for the other valid bids.
6. **ASBA bids** (blocked in the client's own bank account: above ₹5 lakh, and non-individuals) go by API too, once NSE's codes for the client's bank are in. NSE gives each bank a **bank code** and a **location code** (6 characters each; find them on eIPO). Enter the firm's location code on the connection, and each bank's code on IPO → Files & eIPO API → **ASBA bank codes**, by IFSC (one at a time, or a CSV of IFSC, bank code, location code, bank name). A bid whose bank has no codes yet stays for the bid file, and a work item asks for them; once entered, the next send takes it.
7. The API replaces the upload, not the form: each day ASBA bids go, a work item lists how many signed ASBA forms go to which bank's branch for syndicate ASBA, so the money is blocked before the issue closes.

**Advanced security (v2).** In v2 every request and reply is encrypted (AES-256, with the AES key sent RSA-encrypted) and every request is signed. Each side has a 2048-bit RSA key pair: NSE makes a key pair for the firm and sends the firm its public key; the firm makes its own key pair and sends NSE its public key. Neither private key ever leaves its owner.

1. Email msm@nse.co.in with the member code and user ID: ask for v2, and for NSE's public key for the firm.
2. Have the firm's key pair made on a secure computer (2048-bit RSA, no passphrase), for example with OpenSSL: `openssl genrsa -out eipo-key.pem 2048`, then `openssl rsa -in eipo-key.pem -pubout -out eipo-public.pem`. Send NSE only the public key file.
3. On the connection choose **v2 (advanced security: RSA and AES)**. Paste the firm's private key (the whole PEM file, PKCS#1 or PKCS#8) and NSE's public key (a PEM public key or certificate). Ledgerline checks on saving that both are 2048-bit RSA keys and that NSE's key is not the firm's own.
4. Keep the password entered: the calls the protocol does not offer in v2 (login, heartbeat, notifications, mismatches, the allotment report, NCB, tender, SGB, the holiday master) still go by v3.

On v2, a reply from NSE that fails the check (changed on the way, or sealed for another key) is an error, and nothing in it is used. NSE's callbacks must come sealed for the firm and signed with NSE's key; any that fail are refused. Ledgerline's answers to callbacks go back sealed for NSE.

Before going live on v2, confirm with NSE the points the protocol leaves open: the AES-GCM IV (Ledgerline puts a random 12-byte IV in front of the ciphertext), how the key3 signature is made (Ledgerline uses the protocol's RSA-OAEP transformation with the private key), that v2 calls need no login token, and that NSE's callbacks are sealed and signed on v2.

## ipo-mandates
title: Follow up UPI mandates before the cut-off
screen: ipo
roles: Ops maker, Ops checker, Branch staff, Authorised person
ask: upi mandate pending; mandate not approved; ipo mandate cut off; mandate declined; resend upi mandate; failed mandate; 5 pm cut off ipo; ipo reminder
related: ipo-bid, work-queue

The **UPI mandates** tab lists, for each issue bidding or just closed, the mandates still pending, those declined or failed, and bids not yet sent.

- At **16:00 on the closing day** clients with a pending mandate are reminded (SMS, WhatsApp) and the desk gets a work item with the list.
- At **17:00** pending mandates lapse: those bids are marked failed (not considered for allotment), clients are told, and the desk gets the list.
- A failed or declined mandate can be **sent again once** in the issue, after the day's bidding closes and before the last day, on the client's request; then resend it on the platform (bidding inquiry, resend UPI).

## ipo-allotment
title: Record IPO allotment, release of money and demat credit
screen: ipo
roles: Ops maker, Ops checker
ask: ipo allotment status; enter allotment; registrar allotment file; basis of allotment; refund not received; money still blocked; ipo shares not credited; demat credit ipo; post ipo allotment
related: ipo-compensation, ipo-commission, ipo

On the **Allotment & refunds** tab:

1. On T+1, **Enter allotment** (or load the registrar's file: ApplicationNo and AllottedLots). Registrars publish no API. Clients are told their allotment.
2. Ledgerline shows, for each bid, the money to release and the shares to credit, due by T+2. Releases come from the sponsor bank's status in the response files or callback, or click **Released**.
3. Shares are confirmed from the depository where the firm is the DP (the CDSL transaction file's IPO credits or the holding file); **Check demat credits** marks them credited. Late credits and releases go to the work queue each evening from T+2.
4. **Post as purchases** records allotted shares at the issue price for holdings cost and capital gains.

## ipo-compensation
title: Compensation for late release of IPO money
screen: ipo
roles: Ops checker, Compliance officer
ask: ipo compensation; late unblock; refund delayed compensation; 100 per day 15 percent; blocked money not released compensation
related: ipo-allotment, complaints

Money not needed (bid not allotted, partly allotted, or cancelled) must be released by T+2. If it is late, the client is owed **₹100 a day or 15% a year of the bid amount, whichever is higher**, by whoever caused the delay (usually the client's bank as SCSB; for a cancelled bid, counted from the day the cancellation was placed). Ledgerline works it out on the Allotment & refunds tab.

Click **Compensation** to record it **Claimed** (with the bank), **Paid** or **Waived** (with why). If the firm pays, the amount is credited to the client's ledger. A client's complaint about it is opened from the bid (21-day timeline).

## ipo-commission
title: Claim the IPO commission from the issuer
screen: ipo
roles: Ops checker
ask: ipo commission; brokerage from issuer; ipo brokerage receivable; selling commission rhp; commission per application
related: ipo-issue-setup, vouchers-gst

The prospectus sets the commission paid to brokers: per valid or allotted application, or a percentage of the value allotted. Set it on the issue.

After allotment, on the Allotment & refunds tab, **Claim commission** posts it as a receivable with its income and GST. When it is paid into the firm's own bank account, **Commission received** records the amount and any TDS the payer deducted.

## ipo-ofs
title: Bid in an offer for sale
screen: ipo
roles: Ops maker, Ops checker
ask: offer for sale; ofs bid; ofs retail; ofs margin; ofs allocation; ofs contract note; ofs api; place ofs bid by api; eofs
related: ipo-bid, run-billing, contract-notes

An offer for sale runs on two days: non-retail bids on T, retail bids (up to ₹2 lakh, cut-off allowed once the price is known) on T+1, each with **100% cash margin**.

1. Set the issue up as type OFS (T and T+1, floor price; enter the retail cut-off price as the cap once known).
2. **New bid**: the margin is taken from the client's ledger when the bid is taken, after checking the client's free funds. It moves with a revision and comes back on cancellation.
3. Place the bid in the OFS window of the trading system and **Record platform status** with the order number. Or let Ledgerline place it: with **NSE OFS API** set up in [[connections|Connections]] (member ID, OFS user ID and password, from NSE) and **Place bids through the API** on, waiting bids go every two minutes while the window is open (non-retail on T, retail on T+1; series IS, RS or ES, 100% margin, cut-off as a market order), changes and cancellations against NSE's order number, and NSE's order book confirms them. A refused bid shows NSE's reason.
4. **Record allocation** with the shares and price: margins come back, and each allotment is billed as the day's trade. If the day's trade file does not carry it, tick the box to add it to the day's exchange trades; then run billing and contract notes for the day as usual.

## ipo-portal
title: Clients applying for IPOs in the portal
screen: ipo
roles: Ops maker, Ops checker
ask: client apply ipo online; portal ipo; client portal ipo application; client upi declaration
related: portal-access, ipo-bid

Clients see open and upcoming IPOs, SME IPOs, NCDs and offers for sale in the client portal (IPOs & public issues), with the lots each category allows, and apply from their own account. They confirm their UPI ID is on their own bank account, which records it. A family login sees family accounts but only the account holder applies. The client gets the same acknowledgement, and can withdraw where the rules allow. G-sec and T-bill bids, gold bonds and tender offers are taken by staff only.

## ipo-records
title: The IPO consent log and records
screen: ipo
roles: Ops maker, Ops checker, Compliance officer
ask: ipo consent log; proof of client instruction ipo; ipo records inspection; ipo audit; how long keep ipo records
related: ipo-bid, audit-seals

Every instruction (place, modify, cancel, re-send a mandate) is in the **Consent log** tab: when the client gave it and how, the reference, what was asked, who recorded it, and the acknowledgement number sent to the client. **Download** gives it as a file for inspection. Bid files, response files and eIPO callbacks are kept as received. Nothing is deleted: records are kept at least 3 years (NSE circular 65/2025).

## ipo-products-setup
title: Offer G-secs, gold bonds and tender offers through NSE's eIPO
screen: ipo
roles: Ops checker, Ops maker, Admin
ask: g-sec ncb; non competitive bidding; t-bill through broker; sovereign gold bond through broker; sgb eipo; buyback through eipo; tender offer eipo; ncb master; sgb master; tender master; bonds sgb tenders tab
related: ipo-ncb, ipo-sgb, ipo-tender, ipo-eipo-api

The same NSE eIPO login that takes IPO bids also takes **non-competitive bids in G-secs, T-bills and SDLs** (NCB), **sovereign gold bonds** (SGB) and **tender offers** (buy-back, open offer, delisting). Ask msm@nse.co.in to enable the products the firm offers on its eIPO user ID.

1. Set up the **NSE eIPO API** in [[connections|Connections]] as for IPOs.
2. On [[ipo|IPO & public issues]] → **Bonds, SGB & tenders**, an Ops checker ticks the products the firm offers and saves.
3. Each morning at 08:30 Ledgerline reads NSE's NCB, SGB and tender masters and sets up the coming and open issues (dates, times, lot, minimum and maximum, prices, T1 window, allotment date). **Read NSE's masters** does it at once. Set up or correct one by hand with **New** or **Edit**: for a tender offer, the kind, the record date and the category codes from the exchange's notice; for NCB, the percentage above face value to hold on the ledger.
4. With **Send bids through the API** on, waiting orders go every 5 minutes while bidding is open, and their status is asked every 15 minutes. **Send by API** and **Ask status** do it at once.

Orders are taken by staff on the client's instruction; the client portal does not offer these products.

## ipo-ncb
title: Take a non-competitive bid in a G-sec, T-bill or SDL
screen: ipo
roles: Ops maker, Ops checker, Branch staff, Authorised person
ask: ncb bid; non competitive bid for client; buy t-bill for client; buy government security; g-sec auction retail; ncb funds; ncb mismatch; funds pending nsccl; ncb allotment
related: ipo-products-setup, ipo-sgb

On **Bonds, SGB & tenders**, **New order** and choose the security:

1. The amount is in **rupees of face value**: at least the minimum, in multiples of the lot, at most the maximum NSE's master gives (usually ₹10,000 multiples up to ₹2 crore an auction). The price is set in the auction; a non-competitive bid carries none.
2. The money comes from the **client's ledger**: Ledgerline checks the client's free funds and holds the face value (plus the issue's buffer, if set). Collect the money first if the client has not enough. One bid per PAN in an auction.
3. Record how the client instructed. The client gets an acknowledgement.

Once NSE takes the bid it shows **funds pending**: a work item tells the checker how much to pay in to NSCCL. When NSE gives the **amount payable** after the auction, the money held follows it. A **mismatch** (PAN and demat account do not match) goes to the work queue: correct the client master and **Modify** the order during bidding or in the T1 window. A rejected or cancelled bid gets its money back on the ledger; an order NSE never took is released after the close.

## ipo-sgb
title: Apply for sovereign gold bonds for a client
screen: ipo
roles: Ops maker, Ops checker, Branch staff, Authorised person
ask: sgb application; sovereign gold bond for client; sgb online price; sgb certificate; sgb incomplete; sgb missing details; sgb date of birth; sgb bank account; sgb 4 kg limit
related: ipo-products-setup, ipo-ncb

On **Bonds, SGB & tenders**, **New order** and choose the tranche:

1. The quantity in **grams**, within NSE's minimum and maximum. Individuals and HUFs may subscribe up to 4 kg a financial year and trusts 20 kg; Ledgerline counts the client's applications through the firm. NRIs and companies cannot subscribe.
2. **Offline price** by default; tick **online price** only if the client applied online and pays digitally (₹50 a gram less). An application cannot be taken until RBI's price is on the issue.
3. **In demat** (the client's usual account, or another one named) or **as a certificate**.
4. The money comes from the client's ledger and is held until it is paid in to NSCCL, as for NCB.

RBI needs details a demat application does not carry. When NSE lists an application as **incomplete**, a work item asks an Ops maker to open the order and **Send missing details** (date of birth, state, bank account, joint holders, guardian) before NSE's last day for them. Ledgerline fills in what the client master has; check each detail. Nomination is not sent through NSE's API.

## ipo-tender
title: Tender offers through NSE's eIPO (buy-back, open offer, delisting)
screen: ipo
roles: Ops maker, Ops checker, Branch staff, Authorised person
ask: tender through eipo; tender offer api; delisting bid; delisting reverse book building; open offer tender eipo; tendermaster; physical shares tender; early pay in tender; tender block demat; record tender acceptance
related: ipo-products-setup, buyback

This is the route through NSE's eIPO API. A buy-back can also be recorded on [[corp|Corporate actions]] and tendered with a bid file (see the buy-back article). On **Bonds, SGB & tenders**, **New order** and choose the offer:

1. The **shares** to tender. From demat, Ledgerline checks they are free in the client's demat account as it holds it (the registrar checks the holding on the record date), less the client's other tenders in the offer. Physical shares need the folio and up to three certificates with their distinctive numbers.
2. The **price**: a buy-back or open offer is at the offer price; a delisting takes a price in the offer's range. Choose the category from the offer.
3. Record how the client instructed. No money is taken.

Once NSE takes the tenders, a work item asks the desk to confirm the shares were **blocked** in each client's demat account (from the clearing corporation's or depository's report), and to send physical certificates to the registrar.

After the close, an Ops checker records the registrar's **basis of acceptance** (**Record acceptance**): accepted demat shares leave the client's holding and the proceeds are credited to the client's ledger against the clearing corporation. Enter a buy-back's proceeds net of the tax the company deducted, as paid. A work item asks to match the payout and check shares not accepted came back.

## ipo-demand
title: See an issue's demand on NSE
screen: ipo
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: ipo subscription status; ipo demand; how many bids in ipo; category wise demand; demand at price; eipo query server; ipo subscribed how many times
related: ipo-eipo-api, ipo-issue-setup

For an issue bidding on NSE, **Demand** (on the Issues tab, or Bonds, SGB & tenders) shows NSE's figures from the **eIPO Query Server**: quantity and bids by category and sub-category, and at each price (with cut-off and cumulative figures). **Read again** takes a fresh reading, at most every 3 minutes.

Set up **NSE eIPO Query Server** in [[connections|Connections]] with the login NSE gives for it; fields left empty use the NSE eIPO API connection's.
