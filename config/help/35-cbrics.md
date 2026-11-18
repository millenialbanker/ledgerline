Ledgy help articles: clients' over-the-counter corporate bond deals reported and settled through NSE's CBRICS.

# Corporate bonds (CBRICS)

## cbrics-setup
title: Connect NSE CBRICS (corporate bonds)
screen: connections
roles: Admin
ask: cbrics connection; set up cbrics; cbrics api; cbrics login; cbrics password expired; cbrics password policy; cbrics callback; cbrics notification; corporate bond reporting connection; bricsonline; cbrics uat; cbrics api user
related: cbrics-deal, cbrics-reading

Before the first deal, the firm needs an **API user** at CBRICS:

1. The firm's CBRICS administrator signs in to the CBRICS portal, opens **User Management** and adds a user of type **Dealer**, status **Active**.
2. The firm sends NSE the **participant code** and that user's **login ID**; NSE converts it to an API user.
3. For notifications, the firm gives NSE the **callback base address** shown on [[cbrics|Corporate bonds (CBRICS)]] → CBRICS link (NSE adds /v1/notification) and asks for the **callback key**.

Then on [[connections|Connections]], **NSE CBRICS (corporate bonds)**: the participant code (CBRICS calls it the domain), the login ID, the password, the environment (**Testing (UAT)** while trying it, then **Live**), and the callback key. Leave the address empty for NSE's. **Test** logs in and out.

- **Password policy** (CBRICS's Annexure 1): 12 to 15 characters, with at least one small letter, one capital letter, one digit and one of ~!@#$*()_+-:; ; not the current password nor any of the last 5; it expires after 365 days. When CBRICS says it has expired, Ledgerline raises a work item for the Admin. Enter a **next password** ahead of time and Ledgerline changes it at the next login and keeps it as the password.
- **One server per API user**: a second login with the same user ends the first session. Ledgerline logs in again once when its session has gone.
- **Deal type** (empty: Brokered) and **buyer confirmation** of a sale reported (empty: Mandatory) apply to every deal unless the deal says otherwise.
- **Reporting time limit**: the protocol sets none; Ledgerline uses 15 minutes after the deal (SEBI's rule for reporting OTC corporate bond deals) unless the firm enters another.
- **Read deals and settlements every 10 minutes**: On by default, 08:00 to 20:00.

## cbrics-participants
title: Keep clients and counterparties as CBRICS participants
screen: cbrics
roles: Ops maker, Ops checker
ask: cbrics participant; unregistered participant; add client to cbrics; client not on cbrics; cbrics participant code; find cbrics participant; registered participant; counterparty code cbrics; cbrics participant returned; cbrics participant rejected; cbrics add bank account; cbrics add demat account; cbrics default bank; delete cbrics participant; cbrics contact change; cbrics state code
related: cbrics-deal, cbrics-settlement

Every CBRICS deal names a participant code for each side. On [[cbrics|Corporate bonds (CBRICS)]] → **Participants**:

- **Add a client**: a client with no CBRICS account of its own becomes an **unregistered participant**. Ledgerline fills it in from the client's record: name, PAN, contact, address, the state code, the date of birth or incorporation, and the client's **active registered bank and demat accounts**, the primary bank account and the pay-out demat account as default. Fill in what is missing (often the state and the registered address) and save. Only the client's own accounts go: money and bonds are paid out only to the client.
- **Add a counterparty**: an unregistered participant who is not a client (its own details and accounts).
- **Find a registered participant**: CBRICS's list, by code or PAN. **Keep** the one a deal is with; give the UCC when a client has its own CBRICS account, so its deals are matched to it.
- **Take in from CBRICS**: the approved unregistered participants already made on the CBRICS portal, linked to the client with the same PAN.

A checker sends each new participant, correction or change; NSE approves it. Ledgerline reads its status back. A participant **returned** by NSE (or the firm's CBRICS checker) raises a work item with NSE's remarks: correct it and save, and it goes again as a correction. Once approved, **Change contact details**, **Add a bank account**, **Add a demat account**, **Change** an account (make it the default, or set it active, suspended or deleted) and **Delete at CBRICS** each go the same way, one person entering and another sending.

## cbrics-deal
title: Report a client's corporate bond deal to CBRICS
screen: cbrics
roles: Ops maker, Ops checker
ask: report corporate bond deal; cbrics deal; otc bond deal; report bond trade; bond trade reporting; ncd otc trade; sell bonds otc; buy bonds otc; cbrics reporting time; 15 minutes reporting; deal not reported in time; late cbrics reporting; cbrics refused; change reported deal; cancel cbrics deal; yield band violation
related: cbrics-participants, cbrics-confirm, cbrics-settlement

On [[cbrics|Corporate bonds (CBRICS)]], **New deal**:

1. The client, whether the client **sells** or **buys**, the ISIN (the client's holdings are offered; **Today's price and yield on CBRICS** fills in the day's average price, yield and maturity when the bond traded today) and the counterparty.
2. The number of bonds and a bond's face value, the price per ₹100 of face value, the accrued interest, the yield and its type (to maturity, call or put, with the put or call date), the settlement (same day, T+1 or T+2), and the deal's date and time. If the yield is outside CBRICS's band, say why (credit or liquidity).
3. **Save for a checker**. Ledgerline shows the consideration (face value × price ÷ 100 + accrued interest) and warns when the client's demat accounts with the firm do not hold the bonds.

A **checker** (not the person who entered it) opens the deal and **Report to CBRICS**: a client's sale goes as the seller's reporting, a purchase as the buyer's standing instruction, with the deal number as the reference. CBRICS's transaction number comes back. A refusal shows CBRICS's words and goes back to the maker to correct.

**Time limit**: a deal not reported within the firm's limit after the deal (15 minutes unless set otherwise) raises a work item for the checkers; a late report is marked with the minutes it took.

A reported deal can be **changed** until the buyer confirms it: CBRICS cancels the old order and takes the change as a new one. CBRICS has no cancellation by the API: a deal not yet reported is cancelled here; a reported one lapses if the buyer does not accept it, or ask NSE.

## cbrics-confirm
title: A corporate bond sale reported to a client as buyer
screen: cbrics
roles: Ops maker, Ops checker
ask: accept cbrics deal; reject cbrics deal; buyer confirmation cbrics; seller reported to client; awaiting confirmation cbrics; client to confirm bond purchase; standing instruction cbrics
related: cbrics-deal

When the client buys, Ledgerline gives CBRICS the client's **standing instruction**, which confirms the seller's report at once when it matches. When a seller reports a sale to the client that does not match (or there was no instruction), the deal shows **Awaiting confirmation** and the makers get a work item. Get the client's answer, open the deal and record **Client accepts** or **Client rejects** (with the yield band reason if CBRICS asks); a checker sends it.

## cbrics-settlement
title: Settle a CBRICS deal: accounts and money
screen: cbrics
roles: Ops maker, Ops checker
ask: cbrics settlement; bond settlement; settlement accounts cbrics; payout bank account cbrics; demat account for bonds; cbrics pay in; funds pay in bond; cbrics payment page; stamp duty bond deal; bond deal ledger entry; why no ledger entry for bond deal; settlement failed cbrics; cbrics payout
related: cbrics-deal, cbrics-reading

Once the buyer confirms, CBRICS makes the **order for settlement**: the settlement number and date, the consideration, the stamp duty and who bears it, and what the buyer pays in and the seller is paid. It shows on [[cbrics|Corporate bonds (CBRICS)]] → **Settlement**.

- **Accounts**: the seller is paid to its payout bank account and the buyer's bonds go to its demat account. For each side the firm keeps as an unregistered participant, Ledgerline compares CBRICS's account with the client's records (the primary bank account and the pay-out demat account, or the one chosen with **Settlement accounts**) and, when CBRICS has another, proposes the change; a checker sends it before the settlement starts. A registered participant gives its own accounts.
- **Money**: each settlement raises a work item. A buying client pays NSE Clearing from their own bank account by the settlement date (the work item has the link to CBRICS's payment page); a selling client delivers the bonds for NSE Clearing's securities pay-in and is paid by NSE Clearing. The item closes when CBRICS reports the pay-in complete; a failed settlement raises its own item.
- **Books**: the money moves between each side's own account and NSE Clearing, and the stamp duty is collected in the settlement, so nothing passes through the firm and Ledgerline posts no ledger entry. The settled deal is kept with its consideration and settlement date. Brokerage on the deal, if the firm charges any, is billed separately.

## cbrics-reading
title: How Ledgerline reads CBRICS
screen: cbrics
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: read cbrics; cbrics status not updated; cbrics sync; deal found at cbrics; cbrics notification not received; cbrics unmatched transactions; when does ledgerline check cbrics; cbrics deal entered on portal
related: cbrics-setup, cbrics-deal, cbrics-settlement

Ledgerline reads CBRICS every 10 minutes from 08:00 to 20:00 (when switched on), within a minute of a notification from NSE, and when anyone clicks **Read CBRICS now**: the day's sell reportings (and earlier days' of open deals), the buy instructions, the orders for settlement and the participants waiting for approval.

Each record is matched to its deal by CBRICS's transaction number, then by Ledgerline's deal number in the reference, then (a sale reported against a client's standing instruction) by the deal's terms. A deal of a client found at CBRICS and not entered here (made on the CBRICS portal, or settled from another platform) is added, marked **found at CBRICS**. Transactions of no client kept here are listed under **CBRICS link**.

NSE's notifications are checked against the callback key (the token header) and kept as received; one with the wrong token is refused.
