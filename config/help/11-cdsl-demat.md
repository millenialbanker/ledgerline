Ledgy help articles: CDSL's APIs for demat accounts (opening, modification, active demat count, transfer cum closure, off-market beneficiaries).

# CDSL APIs

## cdsl-demat-api
title: CDSL APIs: demat accounts
screen: cdslreq
roles: Ops maker, Ops checker, Compliance officer, Admin
ask: cdsl api; open demat account through api; bo online; bo id not received; bo setup upload; bo modify upload; send change to cdsl; cdsl request failed; retry cdsl request; account transfer cum closure; close demat account through cdsl; client did not get otp from cdsl; add beneficiary; off market beneficiary; buyer seller link; active demat account count; how many demat accounts for this pan; bo defaults; do in cdas
related: dp-requests, dis, change-contact

Every call the DP makes to CDSL about a demat account is a **CDSL request**, on [[cdslreq|CDSL requests]] (Depository). Each request shows its status, CDSL's reference, what was sent and CDSL's answers. A checker who did not raise a request approves it before anything goes to CDSL.

**Setting up (Admin, once).** In [[connections|Connections]], set up **CDSL APIs (DP)**:
- the DP ID, Mock or Live, and the separate API user CDSL creates for the DP, with its password and secret key;
- the API key CDSL issues for the redirect APIs (closure, beneficiaries);
- Ledgerline's public address, which CDSL sends answers to.

Then switch each API family on once it has passed CDSL's mock testing. CDSL accepts calls only from a server address it has listed. On CDSL requests, a checker sets the **BO defaults**: CDSL's product number and the code values used for every new account (BO category, sub-status, statement cycle, bank account type and so on).

**Opening an account (BO online).**
1. A client a checker has activated, with CDSL as depository and no demat account, gets an **Open account** request automatically.
2. The details come from the client master and from the online application, if there is one. Anything CDSL needs that is missing is listed under **Needs details** in plain words, such as the address, PIN code, the bank's MICR code or a minor nominee's guardian. Add it with **Details**.
3. The client's activation counts as the checker's approval. A request staff raise by hand goes to a checker.
4. CDSL answers with a CRN, then calls back with the **BO ID**. The BO ID goes on the client and the DP's account record, and CDSL's acknowledgement is filed with the client's documents.
5. Without a signature image (JPG or PNG) on the client, CDSL opens the account **pending activation**. Upload the signature in CDAS, then mark the request **Activated**.
6. If CDSL has not called back within a day, a work item asks you to check in CDAS.

**Changes (BO modify).**
- An approved change of email, mobile, address, bank account or nomination on a CDSL account raises a **Modify account** request. It lists the fields CDSL holds that change.
- CDSL's API takes these changes as a BO modify file in the CDAS upload layout. Attach that file with **Attach file**, and a checker approves it.
- The file is uploaded, and Ledgerline then asks CDSL for its status until it is processed. CDSL's response file is kept with the request; open it for each account's result.
- **BO setup upload** sends a BO setup file (several accounts) the same way.

**Active demat count.** Whenever a CDSL account is opened, or its PAN, mobile, email or BO ID changes, Ledgerline asks CDSL how many active demat accounts it holds for that PAN, mobile and email. The result shows on the client.
- More than one for the PAN means the client has another demat account, so the account cannot be a BSDA. A BSDA in that position raises a work item for the compliance officer.
- An email or mobile on several accounts is allowed only within a family, with the client's declaration.
- CDSL counts only its own accounts, not NSDL's.

**Closure (account transfer cum closure).**
- With this API on, a checker's approval of a **Closure** request on [[dp|Demat accounts & DIS]] sends it to CDSL, and the DP request reads **Sent to CDSL**.
- The client gets a link by email and SMS. Staff also see it once, for a client at the branch. On CDSL's page the client sees the accounts and confirms with the OTP CDSL sends to the registered mobile and email.
- The account is closed here only when CDSL confirms the transfer. The holdings leave it then.
- The link works for 72 hours, and **New link** sends another. Rejecting the DP request cancels the CDSL request.

**Add beneficiary (off-market).** **Add beneficiary** lists up to 10 buyer accounts:
- for each, a CDSL BO ID (16 digits) or an NSDL account (IN followed by 14 characters);
- the buyer's PAN, unless the buyer is PAN-exempt.

A checker approves, and the client confirms on CDSL's page as for a closure. Each buyer's result (added, or CDSL's reason) is kept.

**When something fails.**
- **Failed** means the call did not get through, or something on the DP's side was missing (for example the public address). Fix it and **Retry**.
- **Rejected by CDSL** shows CDSL's reason and code.
- An account opening whose details changed since approval goes back to the checker on retry.
- Calls that failed only because CDSL could not be reached are retried automatically, up to three times.

**With an API switched off,** a request approved by the checker says **Do in CDAS**. Do it in CDSL's own system, then record it with **Done in CDAS**; for an account opening, enter the BO ID CDAS gave.
