Ledgy help articles: the weekly statement of funds and securities, the annual global statement, DP statements returned undelivered and the closing statement, nomination acknowledgements and the register, complaint acknowledgements and the ODR notice, what contract notes say besides the trades.

# Client statements and notices

## weekly-statement
title: The weekly statement of funds and securities
screen: periodic
roles: Ops maker, Ops checker, Compliance officer
ask: weekly statement; statement of funds and securities; weekly statement monday to saturday; statement as on saturday; zero balance client weekly statement; who gets the weekly statement; pending obligations in the statement; uncleared cheques in the statement; margin in the weekly statement; running account authorisation clause; daily margin statement clause; weekly statement due date; four trading days; voluntary freeze in statement
related: periodic-work, annual-statement, dispatch-register

The weekly statement follows NSE's Inspection master circular (NSE/INSP/73976, item 4.5).

- **The week** runs Monday to Saturday. Balances are as on the Saturday.
- **It is due** within four trading days after the Saturday. [[periodic|Periodic work]] sends it by itself on the next business day.
- **Who gets it:** every client who traded in the last 12 months, even with a zero balance, and any other client with a balance, a movement in the week or securities with the broker. NSE's RAS FAQ 9 lets you leave out only a client with no balance and no trade in 12 months.

Each statement shows:

1. The funds: opening and closing balance, and the debits and credits in the week.
2. **Balance as on Saturday**: the ledger balance after the open bills, less cheques not yet cleared, the margin posted at the end of the week's last trading day and how much of it pledged securities cover, and the funds free of all of these.
3. **Pending obligations**: the pay-ins and pay-outs of the week's trades that settle after the Saturday, security by security, with derivatives mark-to-market.
4. **Uncleared cheques**: each cheque received by the Saturday and not cleared by it.
5. Securities with the broker (margin pledges, unpaid purchases in CUSPA), demat holdings and the funds movement.
6. The clauses: the daily margin statements sent, the running account authorisation and how to revoke it, and how to freeze the account yourself.

The **weekly_statement** self-inspection test checks the last two weeks: the statements went out, to every active client who traded in the last 12 months. To send a week by hand, use **Run now** on Periodic work. Give the Saturday that ends the week.

## annual-statement
title: The annual global statement (funds, securities and transactions)
screen: yearend
roles: Ops maker, Ops checker, Compliance officer
ask: annual global statement; annual global transaction statement; agts; annual statement of funds and securities; 31 march statement; statement as on 31 march; signed annual statement; within 30 days of year end; 30 april statement; exhibit 30; year end statement to clients; transactions in the year exchange wise
related: year-end-packs, weekly-statement, dispatch-register

Within 30 days of the financial year's end (by 30 April), every client gets an **annual global statement**. This follows NSE/INSP/73976 item 4.17 with Exhibit 30, SEBI's master circular for stock brokers para 23.9, and the investor charter.

It holds:

- the funds and securities as on 31 March: balances, open items, securities with the broker and demat holdings;
- the **Annual Global Transaction Statement**: each security or contract on each exchange and segment, with the quantity and value bought and sold, close-outs and auctions, and the STT, stamp duty, transaction charges and SEBI fees.

It is signed and goes on the [[dispatch|Dispatch register]]: by email, or printed for clients who chose paper. **Email all clients** on [[yearend|Year-end packs]] sends it. So does [[periodic|Periodic work]] → *Annual global statement to every client*, by itself after 31 March. The client is also told that the year-end tax pack is in the portal. The pack itself has the same table as its section 4.

The **annual_statement** self-inspection test checks after 30 April that every client who traded in the year was sent it, and on time. Ledgerline makes one statement per client code. A client with several codes under one PAN gets one for each.

## dp-statement-returned
title: DP statements returned undelivered, and the closing statement
screen: dispatch
roles: Ops maker, Ops checker
ask: dp statement returned; courier returned statement; statement undelivered; address changed statement returned; stop sending dp statements; three statements returned; withheld dp statements; resend withheld statements; dp account closed statement; final statement on closure; account closed statement; zero balance final statement
related: dispatch-register, periodic-work

**A printed statement comes back.** Open it on the [[dispatch|Dispatch register]] and click **Mark returned**. Say why it came back and keep the returned cover as proof. Ledgerline puts a work item in the queue: ask the client to update the address.

**After three in a row.** SEBI's master circular for depositories (para 1.22) lets the DP stop only after three DP statements in a row come back undelivered. From then the monthly run records the statement as **Withheld** instead of sending it.

**When the address is updated.** Once an Address change is approved, the next run sends the withheld statements again, free of charge. Each one is sent as it was made.

**A closed account.** The statement for the month the account closed is marked **ACCOUNT CLOSED** with the closing date. It shows the holdings as on that day, nil, as the final statement (CDSL operating instructions 10.5.3.9 to 10.5.3.16). It goes on the dispatch register like every statement.

## nomination-acknowledgement
title: Nomination acknowledgements, the register and fortnightly reminders
screen: kyc
roles: Ops maker, Ops checker, Compliance officer
ask: nomination acknowledgement; acknowledge nomination; acknowledgement of nomination form; nomination register; serial number of nomination; register of nominations; nomination reminder; fortnightly nomination reminder; clients without nomination; no nomination choice; remind client to nominate; opt out acknowledgement
related: nomination, periodic-work, regulatory-reports

**Every nomination action is acknowledged** to the client by email and SMS (SEBI's nomination circular of 29 May 2026, para 9; CDSL operating instructions 3.4.2; BSE inspection master circular item 1.10):

- when a nomination, a change or an opt-out is received, quoting the change request number;
- when it is recorded, quoting its number in the register.

**The register of nominations** gives every nomination, change and opt-out recorded a serial number (NOM-000001 and on), with the date, the client, the nominees, the mode and the request. This covers approved change requests and the online application. Download it from [[reports|Regulatory reports]] → *Register of nominations*.

**Fortnightly reminders.** SEBI's master circular for stock brokers (paras 22.6 and 22.7) asks for an email and SMS every fortnight to each client with no nomination choice. [[periodic|Periodic work]] → *Nomination reminders* sends them by itself in each half of the month. They go to every active client who has neither nominated nor opted out by the declaration. That includes clients added by staff with no declaration on record, who show as *No declaration*. The reminders stop once the client chooses.

## complaint-acknowledgement
title: Complaint acknowledgements, the escalation matrix and the ODR notice
screen: helpdesk
roles: Ops maker, Ops checker, Compliance officer
ask: complaint acknowledgement; ticket number in acknowledgement; escalation matrix; escalation matrix in email; customer care head of customer care compliance officer ceo; smart odr; odr notice; complaint not resolved in 21 days; tell client about scores; scores and odr in reply; complaint response template
related: complaints, message-templates

NSE's grievance master circular (NSE/ISC/73908, Part A 7.2) asks that every complaint is acknowledged with a ticket number, and that complaint messages carry the escalation matrix.

1. Set the **escalation matrix** in [[settings|Settings]]: one level a line, as *Level | Name | Email | Phone | Working hours*. The levels are customer care, head of customer care, compliance officer and CEO. Also set the compliance officer's name and phone, and the firm's address and phone.
2. When a complaint is registered, the client's acknowledgement quotes the **ticket number**, the date we will respond by, the escalation matrix, and SCORES and the SMART ODR portal.
3. Every reply says SCORES and SMART ODR are there if the client is not satisfied (MCX's grievance rules ask the same).
4. A complaint still open after its 21 days gets an **ODR notice** to the client each morning it is found, once per complaint. The **complaints_odr** self-inspection test checks it went.

The texts are the *ticket.opened*, *ticket.reply* and *ticket.odr* templates on [[messages|Client messages]].

## contract-note-clauses
title: What a contract note says besides the trades
screen: notes
roles: Ops maker, Ops checker, Compliance officer
ask: contract note tax invoice; gst on contract note; sac on contract note; place of supply on contract note; rule 46 particulars; mcx jurisdiction clause; courts at mumbai; scrip wise summary; average rate summary; ddpi client daily summary; poa client daily summary; firm address on contract note; compliance officer on contract note
related: contract-notes, vouchers-gst

- **Tax invoice.** The contract note is also the tax invoice for the brokerage and charges (CGST Act section 31, rule 46). It shows the invoice number (the note number), the firm's GSTIN, SAC 997152, the client's GSTIN or *unregistered*, the place of supply (the client's state), and the GST as IGST, or CGST and SGST. Set the firm's state and GSTIN in [[settings|Settings]], and the client's state on the client.
- **MCX trades.** The note says that only the courts at Mumbai have jurisdiction over disputes arising from it (MCX bye-laws).
- **Clients with a DDPI or POA.** The note adds a **scrip-wise summary** of the day's buys and sells at average rates (SEBI master circular for stock brokers, para 36.8.1.9). It is emailed with the note the same day.
- **The firm's details.** Every note and statement prints the firm's name, SEBI registration, member codes, address and phone, and the compliance officer's name, email and phone, as set in Settings.
