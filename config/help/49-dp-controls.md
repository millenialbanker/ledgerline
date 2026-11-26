Ledgy help articles: the DP controls on DIS books and slips, request timelines, the daily reconciliation, the blank slip stock, pool pay-outs, and clients' requests to freeze online trading.

# DP controls

## dis-books
title: Issue a DIS book: requisition slip, loss, loose slips, dormant accounts
screen: dp
roles: Ops maker, Ops checker, Compliance officer
ask: issue a new dis book; second dis book; requisition slip; dis requisition slip; dis book lost; dis booklet stolen; cancel unused slips; loose slips; loose dis; how many loose slips; dis book for dormant account; dormant account dis booklet; registered address dis book; compliance officer authorise dis book; poa dis series; ddpi slip series; separate series for poa accounts; dis book at account opening; waiver of dis book; old dis book not accepted; slips of the old book
related: dis, dis-slip-checks, dis-stock

On [[dp|Demat accounts & DIS]], **Issue DIS book**:

- **The first book** of an account is issued as asked. Issue it at opening, or record the holder's written **waiver** (DP controls → DIS book waived).
- **A later book** needs the **requisition slip** of the previous book, which the holder hands in. Give its number; it is marked used. If the book was lost or stolen, give the reference of the holder's written report instead. Every unused slip of the earlier books is then cancelled. **Book lost** on the DP controls cancels a book's unused slips without issuing a new one.
- **Loose slips** are given to the holder in person, after the signature is verified. The holder signs the register; give its entry. An account gets at most 10 loose slips in a financial year.
- **A dormant account** (no transaction for 12 months) gets a book only at its **registered address**. The compliance officer authorises it on the DP controls before any slip of it is accepted.
- Accounts with a **POA or DDPI** get slips of their own series, numbered from 5000001.
- A month after a new book is issued, slips of the old book are no longer accepted.

When the firm keeps its blank slip stock in Ledgerline, each book takes its slips from the stock (see the blank DIS stock).

## dis-slip-checks
title: Checks on a slip: call to the holder, above ₹5 lakh, joint holders, cut-offs
screen: dp
roles: Ops maker, Ops checker, Compliance officer
ask: call the holder before executing dis; recorded call dis; dormant account dis verification; no transaction for 12 months; not operated for 6 months all balances transferred; five isins transferred in full; 5 isins dis; compliance officer approve call; dis above 5 lakh; senior official verify dis; countersign dis; second signature high value slip; all holders signed dis; joint account mode of operation; jointly or anyone or survivor; dis cut off time; dis received after 4 pm; best effort dis; early pay-in cut off 11 am; inter-depository future dated; inter depository 10 days
related: dis, dis-books, dp-requests

When a slip is entered, Ledgerline checks whether the holder must be **called** on the registered number before it is verified:
- the account had **no transaction for 12 months**, and any security is transferred;
- the account was **not operated for 6 months**, and the slip (with the account's other slips of the day) transfers **every balance**;
- **5 or more ISINs** are transferred in full.

Then a maker or checker calls the holder, **records the call** (the recording's reference; only the registered number), and the **compliance officer approves** it on the DP controls. Only then can the slip be verified. Ledgerline judges dormancy from the demat transactions it holds. Until the DP controls' settings say from when that history is complete, an account with no transaction on record is not judged. The slip says so, and the checker checks in CDAS.

**Above ₹5 lakh**: the checker verifies with the client's confirmation. Another employee then **countersigns** it. One of the two must be a senior official (the firm's list in the settings, or a compliance officer). The slip goes in the upload batch only once countersigned.

**Joint accounts** are operated **Jointly** unless every holder asked for "Anyone or survivor"; any one holder can ask to go back to Jointly. For a jointly operated account, the checker confirms every holder signed the slip and the signatures match.

**Cut-offs** for a delivery to the clearing member: 4 pm (physical) or 6 pm (electronic) on the day before the pay-in, and 11 am for an early pay-in the same day. A slip received later is taken on a best-effort basis and marked so. An inter-depository slip may be dated at most 10 days ahead.

## dp-request-timelines
title: Closure, demat, transmission and freeze timelines
screen: dp
roles: Ops maker, Ops checker, Compliance officer
ask: closure within 2 working days; account closure timeline; closure with dues; dues outstanding before closure; 30 days to pay dues; closure confirmation letter; final statement on closure; closure rejected reason to client; closure blocked pending demat; closure while frozen; closure with pledge; target account same pan; transfer to own account on closure free; closure dis and otp; dp initiated closure; close account by dp; 30 days notice closure; closure cum shifting; demat request ageing; follow up rta 21 days; rejected demat documents; return documents to holder 7 days; transmission 7 days; transmission category; quick transmission processing; transmission up to 30 lakh; survivors fresh nomination; freeze signed acknowledgement; lien notice; freeze intimation to bo; investor charter timelines
related: dp-requests, dis, dp-daily-recon

Each request on [[dp|Demat accounts & DIS]] has its dates, listed under **Requests with something due** on the DP controls. A work item is raised the day before each one falls due. Record what was done with **Record** (a letter, e-mail or courier reference).

**Closures**
- No dues: transfer the balances and close within **2 working days** of the request.
- Dues on the ledger: tell the holder within 2 working days and allow up to 30 days. Record **Dues paid**, then close within 2 working days. If they are not paid, reject within 2 working days of the 30 days, and the rejection carries the dues.
- After closing, send the **closure confirmation with the final statement** within 2 working days. After a rejection, tell the holder why.
- A closure is refused while a **demat request is pending**, or the account has a **freeze** or a **pledge**.
- Holdings going to an account whose holders have the **same PANs** move free of charge. Give the target holders' PANs from its client master. Otherwise the holder signs a **DIS and confirms by OTP**, unless the closure goes through CDSL's transfer cum closure API.
- A closure the **DP** starts needs the date of its **30 days' written notice**. The compliance officer approves it after the notice runs out.

**Demat**: 7 days after the documents are complete. The RTA is followed up 21 days after the request went to it. The documents of a rejected request come back from the RTA within 30 days and go back to the holder within 7.

**Transmission**: 7 days after the documents are complete. Ledgerline sets the category by the account's value: quick processing up to ₹30,000, simplified documents up to ₹30 lakh, full documents above. Record deficiencies told to the claimant. When the surviving joint holders keep the account, record their **fresh nomination** (or opt-out) before approving.

**Freezes**: a freeze the holder asks for needs the signed request; one by an authority needs the order's reference, and a lien notice goes to the holder. The holder is told of every freeze and unfreeze.

## dp-daily-recon
title: The DP's daily reconciliation
screen: dp
roles: Ops checker, Ops maker
ask: dp daily reconciliation; reconcile with cdsl every day; received executed pending; daily dp reconciliation sign off; requests received executed pending; dis received executed; closures received executed; freezes reconciliation; pledges reconciliation; account openings reconciliation; modifications reconciliation; regulation 64 reconciliation
related: dp-request-timelines, dis-stock

The DP controls on [[dp|Demat accounts & DIS]] show the previous business day: for account openings, modifications, slips, closures, freezes and pledges, what was pending at the start, received, executed, rejected and pending at the end. The figures come from Ledgerline's records.

A checker compares them with CDAS and **signs off**. Enter CDSL's count of what was executed where it differs. Any difference needs a note. A work item reminds the checker each evening, and the self-inspection checks every business day was signed off.

## dis-stock
title: Blank DIS stock and its count
screen: dp
roles: Ops maker, Ops checker
ask: blank dis stock; dis inventory; blank slips received from printer; count blank dis; dis stock reconciliation; spoilt dis slips; not enough blank slips
related: dis-books, dp-daily-recon

Record each **delivery from the printer** (the series and number of slips, with the printer's reference) on the DP controls. From then on each book issued takes its slips from the stock, and a book larger than the stock is refused. Record **spoilt** slips. **Count** the blank slips every so often (30 days unless the settings say otherwise). A count that differs from the book balance needs the reason, and the count becomes the new balance.

## pool-payouts
title: Pay-outs from the pool within one working day
screen: dp
roles: Ops maker, Ops checker
ask: pool payout delay; securities not moved from pool; transfer from cm pool to client; pay-out within one working day; cdsl charge for pool delay; 0.06 percent per week; late pay-out of shares
related: dp-daily-recon

Securities received in the pay-out are moved from the pool to the client's account by the **next working day** (trading holidays are not working days). The DP controls list the late ones with an estimate of CDSL's charge: 0.06% of the value for each week or part of a week late. Purchases held for unpaid amounts (CUSPA) are not counted. Pay out on [[settlement|Settlement]]. A work item is raised for each pay-out not moved.

## trade-freeze
title: A client asks to freeze online trading
screen: rms
roles: Risk manager, Ops maker, Ops checker
ask: client wants to freeze trading; voluntary freeze; client asks to block online trading; stop trading request from client; freeze my account; block my trading account; stoptrade email; freeze within 15 minutes; cancel pending orders after freeze; send open positions after freeze; re-enable trading after freeze; unfreeze trading account; voluntary freeze policy; ways to request freeze
related: kill-switch, finspot-api

On [[rms|RMS & limits]], under **Trading freeze requests**, **Record a request**: the client, how it came (e-mail, SMS, app…), from where, and when.

- From the client's **registered e-mail or mobile**, trading is **frozen at once**. Otherwise check it is the client, then **freeze** (or mark it not the client's).
- The freeze is due **within 15 minutes** in trading hours (09:00 to 15:30, to 23:55 for a client with a commodity segment). Outside them, it is due before the next session.
- The freeze blocks fresh orders on Finspot, like the kill switch. The client can still close positions.
- **Cancel the pending orders on Finspot** and record its confirmation, or that there were none.
- Send the **acknowledgement** and the **open positions within an hour**, and record them.
- **Re-enable** only on the client's request, with the due diligence written down (Risk manager or Ops checker).

Record the ways the firm offers (at least two) and where the policy is published in the DP controls' settings. The self-inspection checks both, and each request's times.
