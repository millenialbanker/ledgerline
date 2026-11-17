Ledgy help articles: lists checked against the clients. Mobile numbers on DoT's revocation list (MNRL) and clients the KRAs report as deceased.

# Lists checked against the clients

## revoked-mobiles
title: Revoked mobiles: clients' numbers on DoT's revocation list (MNRL)
screen: mnrl
roles: Ops maker, Ops checker, Compliance officer, Risk manager
ask: mnrl; mobile number revocation list; revoked mobile; disconnected mobile number; report for disconnected mobile numbers; dot dip; digital intelligence platform; mnrl match; client mobile disconnected; sms held; whatsapp held; reactivation data of dot; nsdl mnrl; cdsl mnrl; load the mnrl list; mobile on revocation list
related: revoked-mobiles-steps, deceased-clients, ucc-api

[[mnrl|Revoked mobiles]] (Clients & onboarding) checks clients' mobile numbers against the Mobile Number Revocation List of the Department of Telecommunications (DoT): numbers disconnected that an operator may give to someone else. A client's alerts and one-time codes must not reach a stranger, so each match is taken up with the client and the number corrected.

The lists, each with **Load** (or drop the file on [[xfiles|Exchange files]]):

- **NSE: report for disconnected mobile numbers**, daily on NSE's UCI Member Portal > Download (NSE/ISC/72805 of 13 February 2026): the firm's client codes whose numbers are on the MNRL.
- **DoT: Mobile Number Revocation List**, from DoT's Digital Intelligence Platform (DIP), where the MNRL is kept in near real time (NSE/COMP/74753 of 17 June 2026). CDSL asks DPs to download and check it daily (CDSL/OPS/DP/POLCY/2026/517 of 30 July 2026). It is checked against every client's mobile and the joint holders' mobiles Ledgerline keeps.
- **NSDL: MNRL matches** on e-PASS > iAssist > Download > View/Search - BSDA/MNRL/Email Bounce, and from July 2026 each month NSDL's **Reactivation Data of DoT** (NSDL/POLICY/2026/0090 of 16 June 2026).

None of these files has a published layout. Ledgerline finds the columns by their header names (mobile number, client code, DP ID and client ID, the date): a file whose header it cannot read is refused with what is missing, and an Admin can add a header text to the layout in config/formats.

What a match does:

1. The client is flagged (**Mobile revoked** on the client list).
2. SMS and WhatsApp to the number are **held**, the WhatsApp bot included; email to the registered address goes on.
3. An Ops maker gets a work item to confirm the client's number through another registered channel within 3 business days.

## revoked-mobiles-steps
title: Working a revoked mobile match
screen: mnrl
roles: Ops maker, Ops checker, Compliance officer
ask: mnrl match contacted; not the client's number on the mnrl; mnrl number updated; reactivated mnrl number; mnrl ucc modification; lift the mnrl hold; mnrl overdue; revocation list match status
related: revoked-mobiles, ucc-api

Each match has a status, with its history:

- **New**: found on a list. Take it up within 3 business days (the firm's practice: NSE asks for the number to be updated immediately).
- **Contacted**: you reached the client through another registered channel (the registered email, a letter, the branch). Say how, and what the client said.
- **Number updated**: set by Ledgerline when the client's mobile changes by an approved change request on [[changes|Change requests]]. For a joint holder, or an NSDL account whose number is changed in NSDL's system, mark it yourself with where it was changed.
- **Not the client's number**: the check found the listed number is not the client's number on record (for example, NSE still had an old number for the code). Say what was found.
- **Reactivated**: DoT reconnected the number to the same person, on NSDL's reactivation data or DoT's reply to an action taken report. Only the hold placed because of the MNRL is lifted; nothing else is touched.

When the number is updated, the change goes on by the usual paths: to each exchange as a UCC modification (see [[uccapi|Exchange UCC]]), to CDSL as a BO modify on CDSL requests, and for an NSDL account it is listed for the change in NSDL's system. The match shows what is still to happen.

On [[periodic|Periodic work]], **Revoked mobiles (MNRL): trading accounts** and **demat accounts** are due each business day until the day's list is loaded. The nightly self-inspection reports matches still New after 3 business days, or Contacted for more than 30 days.

## deceased-clients
title: Deceased clients reported by the KRAs
screen: deceased
roles: Compliance officer, Ops checker, Ops maker, Risk manager
ask: deceased client; demise list; demise clients by kra; tmcode demise clients by kra; kra reported death; investor death; death certificate; close ucc of deceased; transmission to nominee; legal heirs; blocked permanently; intimation of death false; enit demise file; not permitted to trade deceased; cut off date demise
related: deceased-clients-steps, revoked-mobiles, ucc-api

[[deceased|Deceased clients]] (Compliance) works NSE's monthly list of the firm's clients the KRAs report as deceased: the file **<TMcode>_Demise_Clients_by_KRA** on NSE's member portal (enit.nseindia.com > ENIT > Log > Downloads). Each month's NSE circular gives the cut-off: the UCCs of the PANs reported up to the end of the previous month are marked Closed, after due diligence, before it. From the cut-off those PANs are not permitted to trade and cannot even square off (NSE/ISC/77375 of 9 November 2026: PANs reported up to 31 October 2026, cut-off 21 November 2026; NSE/ISC/76893 of 14 October 2026 for the cycle before).

**Load the month's list** with the cut-off from the circular (the screen suggests it). Clients are matched by PAN across every client, and by client code when the file has one. The file has no published layout: its columns are found by their header names, and a plain list of PANs is read too.

For each client matched, **debits are blocked at once** until transmission (SEBI/HO/OIAE/OIAE_IAD-1/P/CIR/2023/0000000163 of 3 October 2023; NSE/ISC/75724 of 13 August 2026):

- payout requests are refused and approved payouts are held out of the bank file; running account settlement leaves the client out;
- nothing leaves the client's demat accounts with the firm's DP (slips, eDIS, pledges, early and settlement pay-in);
- trading limits to the front ends are zero, and mutual fund orders are refused;
- the risk manager's kill switch cannot lift the block.

## deceased-clients-steps
title: The steps for a deceased client
screen: deceased
roles: Compliance officer, Ops checker, Ops maker
ask: verify death of client; death verified; intimation false; close the ucc deceased; ucc closed on exchange; transmission information sent; uccs to close csv; change the demise cut-off; deceased overdue
related: deceased-clients, ucc-api

Each client shows its steps with their due dates, and the compliance officer gets a work item for each:

1. **Death verified** (compliance officer): the death certificate verified by the next working day, online, the original seen, or a validation report of an exchange's or depository's investor service centre; update the KRA the same day.
2. **Intimation false** (compliance officer): the investor is alive. Record the due diligence (for example a video call) and the KRA modification sent the same day; the block is lifted and the client is as before.
3. **Close the UCCs** (after the death is verified): every segment is marked Closed and the exchanges' UCC paths send it with the next upload: NSE's UCI API as client status C, BSE's UCC as Is Active C, and MCX's UCC as status 2 (inactive) with the remark, since MCX's layout has no closed status. No closure letter goes to the client. An exchange shows **Closed** once its UCC system accepts the record; a closure made on an exchange's portal is recorded with **record closed** and its reference. **UCCs to close (CSV)** lists them for the portals (NSE: UCI Online > Client Detail > Update Client Status). NSE's bulk "Upload Client Status" file is not written: its layout could not be confirmed.
4. **Transmission sent**: the transmission procedure, form and list of documents given to the nominee, legal heirs or surviving holders within 5 days of the KRA notice.

The cut-off and the days left are shown for each client; **Change the cut-off** sets it for the period's open clients from the month's circular. A client with open positions is flagged: square them off before the cut-off.

On [[periodic|Periodic work]], **Deceased clients reported by the KRAs: close the UCCs** is due each month until the list is loaded and every UCC on it is closed. The nightly self-inspection reports a listed client whose UCC is not closed by the cut-off, or whose debits are not blocked.
