Ledgy help articles: the compliance calendar's duties (Trading, DP, RA & IA) with their circulars, steps and how each is done, the month sheet, the exchanges' RAS calendar, the system audit cycle, common submissions filed with NSE only, and the other dated duties.

# Compliance calendar

## compliance-duties
title: The compliance calendar: every duty, its circulars, steps and how it is done
screen: circulars
roles: Compliance officer, Admin, Ops maker, Ops checker, Risk manager
ask: compliance calendar; compliance sheet; monthly compliance; what is due this month; daily compliance checks; which compliances can be done by api; api compliance; circular for this compliance; steps to file; how to file on fingate; scores login; smart odr; mnrl; demise cases; dp monthly reports; nil report; change in directors intimation; cyber incident 6 hours; record an event; one time compliance; compliance doesn't apply; mark duty not applicable; duty withdrawn; revised cut-off; special session deadline; muhurat margin reporting cut-off; deadline moved for holiday; remove a duty from the calendar; ra ia compliance; month sheet csv; download compliance sheet
related: compliance-month-sheet, compliance-profile, shared-filing, ras-calendar

[[circulars|Circulars & calendar]] → **Compliance calendar** lists the firm's recurring compliance work in three groups: **Trading** (the stock broker), **DP** (the depository participant) and **RA & IA** (a firm also registered as a research analyst or investment adviser). Each duty is one line of a broker's compliance sheet, and carries:

- **The circulars** that set it: issuer, number, date, what each requires, and a link. The deadline is given as the circular states it ("within T+1", "by the 10th of the next month, nil report included"), and Ledgerline turns it into dates.
- **Steps in the circular.** When a circular, its annexure or the portal's user manual lists the steps (the portal path, the format, who approves), they are shown in order with where they come from. When no circular lists steps, the usual practice is shown, marked as practice.
- **How it is done:**
  - **By API**: Ledgerline sends it through an API it has, for example the holding statement or the clearing allocation;
  - **By Ledgerline**: statements, settlements, registers;
  - **File for the portal**: Ledgerline has the data; a person uploads it;
  - **On the portal**: FINGate, SCORES, SMART ODR, CDSL's reporting;
  - **Independent auditor**;
  - **The firm**: board, website, policies.
- **API.** Whether an official API exists that Ledgerline does not use yet. Filter with *How it is done → API exists, not connected* to see what could move to an API.
- **In Ledgerline.** The screen that does it, and the self-inspection test that checks it each night. When that test passed on a recent run, the duty shows **Done by Ledgerline** and is not chased. When it fails, the duty says so, and the exception is on the Self-inspection board.
- **Whether it applies.** Read from the firm profile: CDSL or NSDL participant, exchanges and segments, clearing, MTF, algo, internet-based trading, authorised persons, branches, CSCRF category, RA/IA registration. A duty that doesn't apply is shown with the reason and is never chased. When the profile doesn't say, the duty shows **Not sure** with the question to answer (Settings → Firm profile).

**Kinds of deadline**
- **Daily duties** (each trading day, or T+1 to T+5) go to their owner's work queue as one item, *Daily compliance checks*. **Mark done** has *Every day still open up to today*.
- **Weekly duties** fall due a set number of trading days after the week's last trading day.
- **Fortnightly duties** fall due on the 15th and on the month's last day.
- **Settlement duties** fall due a number of trading days after each running account settlement.
- **Event-driven duties**: a change of directors or address, a cyber incident within 6 hours, the compliance report on an audit within 30 days. Choose **Record an event**: give when it happened (and the time, for deadlines in hours) and what happened. It falls due from then and goes straight to the owner's work queue.
- **One-time duties** stay due until marked done, however old the deadline. When a regulator makes a one-time duty recurring, the filing you marked done still counts for the dates up to when you did it.
- **Revised cut-off.** When a circular moves the deadline for one occurrence (NSE Clearing's cut-off for a Muhurat or other special session's margin reporting, a deadline moved for a holiday), open the duty and choose **Revised cut-off**. Give the occurrence (the trade date for a daily duty, the month or date for others), the new date and the circular. That occurrence is then chased by the new date. List special session days in the holding statement panel on [[xfiles|Exchange files]] (**Change**): duties due each trading day then expect those days too.
- **As required** duties have no date. **Done by Ledgerline** duties need nothing marked.

**A duty that doesn't apply to your firm** (for example one withdrawn by a circular): open it and choose **Doesn't apply to us** (compliance officer). Give the reason, such as the circular that withdrew it. The duty stays in Ledgerline for other firms. For yours it shows *Doesn't apply* with your name, the date and the reason, and it is not chased. The decision is in the audit trail and in the duty's history. **It applies after all** takes it back.

Each **Mark done** keeps who did it, when, and the evidence named; *late* is shown when it was done after the due date. Duties are queued a week before they fall due. The compliance officer can change a duty's title, due rule, owner or evidence. The change is kept, and Ledgerline's catalogue no longer replaces it, but the circulars and steps still update with each Ledgerline release.

## compliance-month-sheet
title: The month's compliance sheet
screen: circulars
roles: Compliance officer, Admin, Ops maker, Ops checker, Risk manager
ask: compliance sheet for the month; november compliance; monthly compliance calendar; print compliance calendar; export compliance calendar; csv of compliance; which duties fall due this month
related: compliance-duties

**Circulars & calendar → Month sheet** sets out the month the way a compliance consultant's monthly sheet does: Trading, DP and RA & IA, each duty with:
- its frequency;
- the deadline in the circular's words;
- the dates it falls due that month (daily duties show the number of trading days);
- where it is filed and how it is done (API, Ledgerline, file for the portal, portal, auditor or the firm);
- the owner and its status, and what was done in the month.

Choose the month at the top. **Show what doesn't apply** adds the duties the firm profile rules out, greyed. **Download CSV** gives the same sheet as a file, with the circulars of each duty.

## ras-calendar
title: Keep the exchanges' RAS dates
screen: circulars
roles: Compliance officer, Admin
ask: ras dates; ras calendar; running account settlement dates; first friday; friday or saturday settlement; settle on friday; quarterly settlement date; monthly settlement date; all clients quarterly; annual calendar for settlement of running account; next ras date
related: ras, compliance-profile

Running accounts are settled on the dates the exchanges set jointly for each financial year, not on the first Friday (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197 of 28 December 2023; NSE/INSP/60014). Each date is a Friday and/or the Saturday after it, separately for clients who chose monthly and quarterly settlement.

1. On [[circulars|Circulars & calendar]] → **RAS dates & profile**, the list shows each settlement, the exchanges' dates and the day the firm settles. FY 2026-27 is already there (for example 17/18 April, 3/4 July, 16/17 October 2026 and 1/2 January 2027 for quarterly clients; 15/16 May, 6/7 November 2026 for monthly clients).
2. **Edit RAS dates** (compliance officer or admin): one line per settlement — *Monthly* or *Quarterly*, the period (2026-11, or 2026-27 Q3; Q1 is April–June), the Friday and/or Saturday, and the day the firm settles (the Friday unless you choose the Saturday). Only Fridays and Saturdays are accepted.
   - **The firm settles on:** *the Friday of every date* or *the Saturday of every date* sets the day for every line at once; *the day each line says* lets each line choose.
   - **Clients are settled:** *each client's own choice*, or *every client quarterly*. With every client quarterly, the monthly settlement duty doesn't apply. If a client record still says monthly, the duty stays and a warning names the client. Clients idle for 30 days are still paid out on the monthly date.
3. The next monthly and quarterly date in Settings, the [[ras|Running account]] screen, Periodic work and Autopilot all follow the list; it is applied each morning and whenever it is saved. The compliance calendar has one duty for quarterly clients and one for monthly clients, due on the last day of each window and done once the settlement has run.
4. When the list ends within two months a work item asks for the next year's calendar, which the exchanges publish in January.

## compliance-profile
title: System audit cycle, auditor and MTF
screen: circulars
roles: Compliance officer, Admin
ask: system audit; system audit due date; type iii system audit; half yearly system audit; empanelled auditor; enit audit plan; action taken report; atr; net worth certificate date; mtf net worth
related: ras-calendar, shared-filing

NSE's system audit circular NSE/INSP/73850 (22 April 2026) sets the cycle by member type: half-yearly for members trading through NEAT, NNF and algo (Type III); yearly for Type I and Type II members with more than 10 locations or 50 terminals, and for depository participants; once in 2 years for the smaller Type II members; none for NEAT-only members who are not DPs.

On **RAS dates & profile** → **System audit, auditor and MTF** set the cycle, the auditor, and whether NSE has empanelled them (NSE/INSP/69631: only an empanelled auditor's report is accepted). A warning shows when the cycle does not match the firm profile's answer on algo trading (half-yearly is the cycle for algo members). The calendar then has three duties:

- **Audit plan** on NSE's ENIT portal (auditor, systems, office locations to visit): by 5 June; also by 5 November for the April–September half year.
- **Preliminary audit report**: by 30 June; also by 30 November for the half year (NSE/INSP/76769). The algo MIS, version confirmation, assessment details and the auditor's geo-tagged visit details go in first.
- **Action taken report**, validated by the same auditor, if there were non-compliances: by 30 September; by 28 February for the half year.

**MTF**: a member approved for margin trading files the September half-year net worth certificate within 45 days (by 14 November); others within 60 days (29 November). The March certificate is due within 60 days (30 May) for everyone (NSE/INSP/70505).

## shared-filing
title: Submissions filed with NSE only
screen: circulars
roles: Compliance officer
ask: file with nse only; common submissions; single filing exchanges; net worth certificate to which exchange; annual return to bse; technology based sharing mechanism; nse shares with bse
related: compliance-profile

A member of NSE that is also a member of BSE, MSE, MCX or NCDEX files the common submissions with NSE only, and NSE shares them (NSE/INSP/69184 of 18 July 2025, 40 items from 1 August 2025; NSE/INSP/70505 of 29 September 2025: 28 items from 15 October 2025, and the half-yearly net worth certificate and annual returns from 15 December 2025). The list includes the holding statement, reporting of bank and demat accounts, the cyber security and VAPT reports, the SaaS and AI/ML reports, and the net worth certificate.

In the compliance calendar these duties appear once and say **File with NSE (shared with the other exchanges)** when the firm has an NSE member code and another exchange's in Settings. A firm that is not an NSE member sees **File with each exchange**. Bank account reporting to NSE uses the revised undertaking, authorisation and bank confirmation formats from 15 October 2025 (Annexures B to D of NSE/INSP/70505); Ledgerline does not print these letters.

**Daily client bank balances:** members stopped uploading them from 30 October 2023; the banks send them to the exchange on T+1 (NSE/INSP/59044). The daily upload is not on the calendar. A yearly duty remains to confirm that your banks send them for every reported account. If your firm decides that this doesn't apply to it either, open the duty and choose **Doesn't apply to us** with the reason.

## surveillance-return
title: Quarterly surveillance obligation report (Formats I to III)
screen: surv
roles: Compliance officer
ask: surveillance report; quarterly surveillance reporting; surveillance obligation; alert register; format i; format ii; format iii; nil report surveillance; alerts referred to exchange; surveillance return due
related: shared-filing

Each quarter, within 15 calendar days of its end, the firm reports the status of its surveillance alerts to every exchange it trades on (NSE/SURV/48818 and NSE/SURV/49765, in the consolidated surveillance circular NSE/SURV/74008 of 30 April 2026; BSE's guidelines, on BEFS). It is not a monthly register.

- **Format I**, for each alert name: alerts under process at the start of the quarter, new in the quarter, verified and closed, referred to the exchange, and pending at the end.
- **Format II**: each alert referred to the exchange, with the date of the alert, its type, the brief observation and action taken, and the date referred.
- **Format III**: any major surveillance action other than referring alerts.

On [[surv|Surveillance & AML]], **Referred to exchange** records the date and observation for Format II; **Add an action** records a Format III entry. An alert counts as closed in the quarter Ledgerline first sees it Closed (or STR filed). [[reports|Regulatory reports]] → *Surveillance obligation report* downloads the three formats for the quarter ended before the chosen date, with **NIL** where there is nothing to report; the compliance calendar has the quarterly duty.

## upi-collection
title: Collect money by UPI: validated UPI IDs and the SEBI QR
screen: settings
roles: Compliance officer, Admin
ask: upi id; validated upi; brk@valid; dp@valid; upi id for dp charges; upi for maintenance charges; make upi qr; upload upi qr; upi id in connections; ra@valid; ia@valid; @valid upi handle; sebi check; upi qr code; add funds by upi; old upi id; collect money by upi; research analyst fee by upi; investment adviser fee by upi; upi id for ra or ia fees
related: live-bank-credits, unknown-credit, upi-fee-credit

SEBI's circular SEBI/HO/DEPA-II/DEPA-II_SRG/P/CIR/2025/86 (11 June 2025) has SEBI-registered intermediaries collect investors' money by UPI only through a standardised, validated UPI ID: a readable username, the **category** of the registration, and an **@valid** handle with the bank's name, for example *abc.brk@validhdfc*, available from 1 October 2025. Old IDs were to be stopped within 180 days. The categories are in its Annexure B: **.brk** stock broker, **.dp** depository participant, **.ra** research analyst, **.ia** investment adviser, and others (bti, invit, mf, pms, sreit, reit). A firm with several registrations applies for a handle for each one. The RA and IA master circulars of 6 February 2026 (paras 26 and 28) refer research analysts and investment advisers to it. The QR code comes from SEBI's SI portal and is shown on the website, the client portal and payment pages. NSE asks every account given to investors for payment to be linked to an @valid ID so it can be checked in SEBI Check (NSE/COMP/75700).

1. Get the validated UPI IDs from your bank, one for each registration (for a broker that is also a DP, a **.brk** ID for broking money and a **.dp** ID for the DP's charges), and, if you can, each ID's QR code from the SI portal.
2. In [[connections|Connections]] → *UPI collection IDs (@valid)* → **Set up** (or [[settings|Settings]] → *UPI collection* → **Change**), enter each ID with the bank and the account it credits. Each ID has its own QR code: Ledgerline makes a UPI payment QR from the ID, or upload the image from the SI portal (PNG or JPEG), which also carries the SEBI Check mark; the uploaded one is used when there is one. Ledgerline takes the categories of the firm's registrations: **.brk** always (broking money); **.dp** when the firm is a depository participant (Settings → *Firm profile*); **.ra** and **.ia** when the firm profile lists those registrations. The window shows which are taken and why not.
3. The client portal's **Add funds** panel then shows the broking ID with its QR (the .dp ID for a client of the DP only), with the bank account for NEFT, RTGS or IMPS as the alternative. A trading client also sees the DP's .dp ID with its QR, for DP charges such as account maintenance. A research or advisory fee ID is never shown there. Ledgerline does not start payments.
4. Statement credits say which UPI ID they were paid to when the bank gives it (the optional *PayeeVPA* column of the bank statement file, or *UPI ID paid to* on a live bank connection). A credit to a client-money ID is matched as usual (it must still come from the client's registered account); a credit to the firm's .ra or .ia ID is the firm's fee (see [[bank|Bank reconciliation]]); a credit to any other UPI ID opens a work item to stop the old ID.

Ledgerline has no research or advisory fee module: link the .ra and .ia IDs to the firm's own account, where the fees belong.

## upi-fee-credit
title: Research or advisory fee paid to the firm's UPI ID
screen: bank
roles: Ops checker, Ops maker
ask: fee paid to ra upi id; advisory fee on bank statement; research fee on bank statement; firm's fee bank credit; move fee to own account; ra fee not client money; ia fee upi credit
related: upi-collection, unknown-credit

A fee paid to the firm's research analyst (.ra) or investment adviser (.ia) UPI ID is the firm's money, never client money. When one shows on the client bank statement, Ledgerline does not credit any client's ledger: it is held in the bank suspense account (left out of the client-money check), shown as **Firm's fee** on [[bank|Bank reconciliation]], and an Ops checker gets a work item.

1. Transfer it from the client bank account to the firm's own account.
2. Click **Move to firm's account** and give the transfer reference. Ledgerline posts it out of the client bank account into the firm's own bank, against *Research and advisory fees receivable*, where the firm's accountant matches it to the fee invoice.
3. If it was paid by mistake, **Return** it to the payer instead.
4. Ask the bank to link the .ra or .ia ID to the firm's own account, so fees no longer come into the client bank account.
