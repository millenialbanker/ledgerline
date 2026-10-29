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
ask: eipo api; nse ipo api; ipo callback; eipo callback url; send ipo bids by api; whitelist callback; login without otp
related: ipo-bid-file, connections

NSE gives API access to members on request: email msm@nse.co.in with the member code and user ID (API access, and login without OTP), and the callback address to whitelist (4 to 5 working days).

1. In [[connections|Connections]], **NSE eIPO API**: member code, user ID, password, and a **callback token** (a long random phrase).
2. Give NSE the callback address shown on [[ipo|IPO & public issues]] (Files & eIPO API), with your token in it. Optionally list the addresses NSE calls from.
3. NSE's API document is given to members, not published. Enter the API address and the bid and inquiry endpoint paths from it; until then Ledgerline does not send bids by API and they go by file.
4. Switch **Send bids through the API** on: waiting UPI bids then go every 5 minutes during bidding hours, and bids in flight are asked about every 15 minutes. Callbacks update bids and UPI mandates as they come.

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
ask: offer for sale; ofs bid; ofs retail; ofs margin; ofs allocation; ofs contract note
related: ipo-bid, run-billing, contract-notes

An offer for sale runs on two days: non-retail bids on T, retail bids (up to ₹2 lakh, cut-off allowed once the price is known) on T+1, each with **100% cash margin**.

1. Set the issue up as type OFS (T and T+1, floor price; enter the retail cut-off price as the cap once known).
2. **New bid**: the margin is taken from the client's ledger when the bid is taken, after checking the client's free funds. It moves with a revision and comes back on cancellation.
3. Place the bid in the OFS window of the trading system and **Record platform status** with the order number.
4. **Record allocation** with the shares and price: margins come back, and each allotment is billed as the day's trade. If the day's trade file does not carry it, tick the box to add it to the day's exchange trades; then run billing and contract notes for the day as usual.

## ipo-portal
title: Clients applying for IPOs in the portal
screen: ipo
roles: Ops maker, Ops checker
ask: client apply ipo online; portal ipo; client portal ipo application; client upi declaration
related: portal-access, ipo-bid

Clients see open and upcoming issues in the client portal (IPOs & public issues), with the lots each category allows, and apply from their own account. They confirm their UPI ID is on their own bank account, which records it. A family login sees family accounts but only the account holder applies. The client gets the same acknowledgement, and can withdraw where the rules allow.

## ipo-records
title: The IPO consent log and records
screen: ipo
roles: Ops maker, Ops checker, Compliance officer
ask: ipo consent log; proof of client instruction ipo; ipo records inspection; ipo audit; how long keep ipo records
related: ipo-bid, audit-seals

Every instruction (place, modify, cancel, re-send a mandate) is in the **Consent log** tab: when the client gave it and how, the reference, what was asked, who recorded it, and the acknowledgement number sent to the client. **Download** gives it as a file for inspection. Bid files, response files and eIPO callbacks are kept as received. Nothing is deleted: records are kept at least 3 years (NSE circular 65/2025).
