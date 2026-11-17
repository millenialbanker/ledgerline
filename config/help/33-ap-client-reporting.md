Ledgy help articles: reporting clients mapped to authorised persons. Format is described in internal/help/help.go.

# Authorised persons

## ap-client-reporting
title: Report clients mapped to APs to each exchange every week
screen: partners
roles: Ops maker, Ops checker, Compliance officer
ask: weekly ap client mapping report; clients mapped to authorised persons report; clients unmapped from an ap; ap client upload to the exchange; enit authorised person client upload; ap mapping file for nse bse mcx; nse comp 49509; ap report due second trading day; history of a client's ap
related: ap-client-counts, map-client, add-ap, periodic-work, ucc-rejected

Every week each exchange the firm is a member of gets its own file of the clients newly mapped, re-mapped or unmapped to an authorised person (AP), and the new clients of APs. It is due on the **2nd trading day of the following week** (NSE/COMP/49509 of 3 Sep 2021; MSE/MEM/15275/2024, item 15). It is not a common submission: upload it on every exchange. The details must match the client's UCC at that exchange.

On [[partners|Branches & partners]] → **AP client reporting**:

1. Choose the week (Monday to Sunday). The latest week that has ended is shown first.
2. Each exchange lists its clients, each with a check:
   - **In the file**: ready.
   - **In the file · check**: it goes in, but look at the note (for example, the client's record carries the AP's own mobile).
   - **Waits**: the client's UCC is not yet registered or accepted at that exchange. It goes in the week it is.
   - **To fix**: held back. Fix what the note says: a rejected UCC or a PAN the exchange could not verify ([[uccapi|Exchange UCC]]), or an AP with no registration number for that exchange (edit the AP on the Partners tab).
3. **Download** the exchange's file and upload it on the exchange's portal (NSE: ENIT, Compliance > Authorised Person).
4. **Record the upload** with the date and the acknowledgement the portal gave. The file as made is kept with it.

A re-mapping is two rows: D for the AP before and A for the new one. A client held back one week goes in a later week's file once it is right, so nothing is lost.

**How changes are recorded.** Every change of a client's AP is kept with the day it took effect, the AP before and after, and who made it: from the client's Branch and partner, at account opening and from the online application. Open **A client's AP history** to see them.

- Clients already mapped when the register started are recorded as an **existing mapping**, dated their account opening date, and taken as already reported to every exchange the firm is a member of. They are never in a weekly file.
- A client brought in by the client master import of the previous system comes in the same way. A changed AP in a later import is a change to report.
- An AP changed any other way is found on the next check and dated the day it was found.

**The file's layout.** The exchanges publish their layout as an annexure and a user manual that Ledgerline could not read, so each exchange's columns are in config/formats (for example nse-ap-client-mapping.json), marked unconfirmed. Check them against the exchange's template before the first upload; change the headers or the order there.

**An exchange added later.** If the firm reported its AP clients to an exchange before Ledgerline had its member code, the compliance officer can **Take as reported**: the present mappings are taken as known to that exchange. It is in the audit trail.

**The AP's own mobile and email.** NSE/COMP/63628 (Annexure A item 28) does not allow an AP's own mobile or email on any of its clients' UCC. Enter each AP's own mobile and email on the partner; Branches & partners warns when a client's record carries them. A client with the AP's own PAN (its own account) is left out.

The weekly task is on [[periodic|Periodic work]]; the nightly [[selfinspect|Self-inspection]] checks that the week was reported.

## ap-client-counts
title: Reconcile each AP's client count with the exchanges every month
screen: partners
roles: Ops maker, Ops checker, Compliance officer
ask: ap client count on the exchange; reconcile ap clients with nse; number of clients of an ap on enit; ap count reconciliation monthly; ap client count differs from exchange
related: ap-client-reporting, add-ap

NSE/COMP/63628 asks members to keep each AP's details current in the exchange's records, including the number of its clients. Once a month, compare them.

On [[partners|Branches & partners]] → **AP client reporting**, under **AP client counts with each exchange**:

1. Choose the month (the month just ended is shown).
2. For each exchange, note the number of clients the exchange's portal shows for each AP at the month's end.
3. Optionally paste the exchange's list of the AP's clients: Ledgerline then shows which clients differ.
4. **Record** the counts. If they differ, say what was done about it (for example, the missing clients were uploaded).

Ledgerline's count is the clients mapped to the AP at the month's end who trade on that exchange. The reconciliation is a task on [[periodic|Periodic work]], due by the 10th: that is Ledgerline's date, as no circular sets one.
