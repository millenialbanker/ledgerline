Ledgy help articles: the UCC-wise report of each running account settlement, the firm's register of bank accounts (USCNBA, DSCNBA, settlement, own and the clearing corporation), and the daily reconciliation of client money transfers.

# Client money

## ras-ucc-report
title: Report each running account settlement client by client (UCC-wise details)
screen: ras
roles: Compliance officer, Ops checker, Ops maker
ask: ucc wise settlement details; ucc-wise running account settlement report; submit ucc wise details; settlement statement upload on the member portal; report the running account settlement to nse; ras report to the exchange; pan_qs file; qs file sequence number; ucc wise report within 10 trading days; not applicable declaration for a settlement date; penalty for late ucc wise report; retained only clients in the ucc wise report
related: ras, ras-calendar, firm-bank-accounts

After each running account settlement the firm reports, client by client, what it retained and what it paid, within **10 trading days** of the settlement date (NSE/INSP/55024 of 28 December 2022; the 2-day summary report was dropped by NSE/INSP/61904 of 6 May 2024). A firm that is a member of NSE and other exchanges files it with NSE only, and NSE shares it (NSE/INSP/70505 of 29 September 2025).

On [[ras|Running account]], the **UCC-wise settlement reports** panel lists each settlement date of the exchanges' calendar with its due date, counted on Ledgerline's trading calendar:

1. Run the settlement as usual. The panel then shows the clients **paid** and the clients whose funds were **only retained**. Clients with nothing retained and nothing paid are left out (NSE/INSP/56148 of 27 March 2023).
2. **Make the file.** It is one CSV of 17 fields inside a ZIP named *PAN_QS_DDMMYYYY_SeqNo*, as the portal asks (NSE/INSP/55135 of 5 January 2023). Bank account numbers are in double quotes so they are not rounded. Clients only retained have NA for the four bank fields. **Clients** shows each client's row; anything missing (a client's bank account, the DSCNBA the firm paid from) is listed against it.
3. Upload the ZIP on NSE's member portal: Inspection › Statement Upload › Settlement Statement › Submit Data › Submit UCC wise Details. Check the result under *UCC wise view submission*.
4. **Record upload** with the portal's reference and the day it was uploaded. An upload after the due date is recorded as late.

If no client had funds to settle on a date, make the not-applicable declaration on the portal and record it with **Not applicable**.

**Sequence numbers.** A file name the portal accepted once cannot be used again. **Make again** gives the same file if nothing changed; a changed settlement, or ticking *make a new file*, takes the next number for that date. Ledgerline never gives a number twice.

**Reminder.** Each settlement raises a work item for the compliance officer, due on the due date; it stays open until the upload or the not-applicable declaration is recorded. Periodic work shows the task *UCC-wise running account settlement report*, and the self-inspection check of the same name looks for any settlement not reported within 10 trading days.

**Late reports** cost ₹10,000 a day. After a week no new client may be registered, and after two weeks trading is disabled in all segments until the report is filed (NSE/INSP/56261 of 3 April 2023).

The 17 fields are the format the exchanges applied from the settlement of 6 October 2023, as NCDEX's member inspection master circular for 2025-26 lists them (Annexure A). Check the first file against the template on NSE's portal: if a header differs, change it in config/formats/nse-ras-ucc.json.

## firm-bank-accounts
title: Name the firm's bank accounts (USCNBA, DSCNBA, settlement, own)
screen: clientmoney
roles: Compliance officer
ask: firm bank accounts register; uscnba; dscnba; up streaming client nodal bank account; down streaming client nodal bank account; nodal bank account of the broker; settlement bank account with the clearing corporation; proprietary bank account of the firm; which of our bank accounts is the uscnba; clearing corporation account we upstream to; account reported under enhanced supervision
related: client-money-recon, ras-ucc-report, upstream

Since upstreaming began, clients' money is received only in an **Up Streaming Client Nodal Bank Account (USCNBA)**, and clients are paid only from a **Down Streaming Client Nodal Bank Account (DSCNBA)**. A **settlement account** is held for settlement with the clearing corporation, and every other account counts as the firm's **own** account (SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90 of 17 June 2025, para 15.3).

On [[clientmoney|Client money reconciliation]] → **Firm bank accounts**, the compliance officer names each account once:

1. **Add an account**: its kind, bank, branch, account number, IFSC and the name the bank has for it.
2. Tick the exchanges or segments it serves. None ticked means all of them; one USCNBA and one DSCNBA may serve every segment.
3. Tick **Reported** for the accounts reported to the exchanges under enhanced supervision. Clients may be paid only from such an account.
4. Add the **clearing corporation** too, as the counterparty you upstream to and receive funds from.
5. An **opening balance** with its date lets the daily statement show each account's balance.

Accounts already named in Settings (the payout account, MCX-only accounts, the bank connection) are offered under *Named in Settings*; **Add** takes the full number from there. Numbers are shown masked. When an account is closed, give the date it closed instead of removing it.

The register is used by the daily [[clientmoney|client money reconciliation]], which checks that receipts arrive only in a USCNBA, payments leave only from a DSCNBA, and nothing is left in the DSCNBA at the end of the day; and by the UCC-wise settlement report, which needs the DSCNBA each client was paid from. NSE allows at most 30 USCNBA and DSCNBA accounts together (NSE/INSP/57959, FAQ 23).

## client-money-recon
title: Prepare and sign off the daily client money reconciliation
screen: clientmoney
roles: Ops maker, Ops checker, Compliance officer
ask: client money reconciliation; daily reconciliation statement of client funds; transfers between client and own account; client account to own account transfer; uscnba dscnba reconciliation; daily statement of funds transferred; brokerage transferred to own account; dscnba balance at end of day; money moved from uscnba to dscnba; record a transfer between the firm's accounts; funds back from the clearing corporation; sign off the money reconciliation
related: firm-bank-accounts, upstream, unknown-credit, ras-ucc-report

SEBI lets money move from the client account to the firm's own account only for brokerage, statutory dues and other charges debited to clients, or a client's debit the firm met, and asks for a **daily reconciliation statement** of the funds transferred (master circular 2025/90, para 15.4.4.2). The exchanges ask for it in inspections (NSE/INSP/55904 of 9 March 2023). No format is prescribed: this is Ledgerline's.

On [[clientmoney|Client money reconciliation]], the **Daily statement** of the day chosen at the top lists every movement from Ledgerline's records, each with its date, from, to, amount, purpose and ledger entry:

- receipts from clients, payouts to clients and running account settlements;
- upstreaming to the clearing corporation and funds back from it;
- transfers to and from the own account;
- the bank statement lines loaded for the day, matched by reference and amount. What does not match is listed under *Reconciling items*.

It gives totals by kind, the charges debited to clients that back the transfers to the own account, and each account's balance (book and bank).

**Flags** to explain:

- money taken to the own account beyond the brokerage, charges and statutory dues debited to clients (and own funds put in);
- money received outside a USCNBA, or paid to a client from outside a DSCNBA;
- money moved from a USCNBA straight to a DSCNBA (NSE/INSP/57959, FAQ 21);
- a balance left in the DSCNBA at the end of a day that is not a running account settlement day (NSE/INSP/58146, FAQ 1).

**Transfers** between the firm's accounts that Ledgerline would not otherwise know of are recorded on the **Transfers** tab: why the money moved, from, to, amount and the bank's reference. An Ops checker approves each one, and approving posts it in the ledger where the accounts differ there. Upstreaming can still be recorded with **Upstream funds to CC** on [[ledger|Ledger]].

**Maker and checker.** An Ops maker **Prepares** the statement, with a note on anything flagged; it is kept with its CSV. An Ops checker who did not prepare it **Signs off**, or **Sends back** with a reason. If the day's records change after it was prepared, it must be prepared again before it can be signed off; earlier versions are kept. **CSV** and **Print** give the statement as evidence.

Periodic work shows the daily task *Daily client money reconciliation* for each business day, due the next business day, and the self-inspection check of the same name lists any business day whose statement was not signed off. The nightly *client funds segregation* check, (A+B)−C ≥ 0, stays as it is: it checks the total, this statement checks each day's movements.
