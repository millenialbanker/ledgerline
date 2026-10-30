Ledgy help articles: registering clients at BSE by the UCC API and the batch file.

# BSE UCC

## bse-ucc-api
title: BSE UCC by API and batch file
screen: xfiles
roles: Ops maker, Ops checker, Admin
ask: bse ucc api; send ucc to bse; bse ucc rest api; register client at bse automatically; bse ucc batch file; bse ucc not sent; bse ucc needs fixing; bse pan status; bse pan invalid; bse aadhaar seeding
related: ucc-upload, ucc-rejected, bse-ucc-password

**BSE** takes new clients and every change to a client's UCC record (contact, bank, address, segments and their statuses, inactivity and reactivation) through its UCC REST API.

1. The Admin sets up **BSE UCC API** in [[connections|Connections]]: the member code, a separate API user created on BSE's UCC portal, its password, and the RSA public key BSE gives the member (PEM, base64 or BSE's XML). Choose Live or UAT, and switch **Send to BSE automatically** on.
2. Clients at the **UCC upload** stage, and changes to clients BSE already holds, are sent every 15 minutes from 08:00 to 22:00, one client per request. Clients already active before the connection was set up are taken as registered at BSE; only their later changes are sent, as modifications. Ops makers and checkers can also send at once.
3. Each client shows **Not sent**, **Sent**, **Accepted**, **Rejected** (with BSE's reason in plain English) or **Needs fixing**: Ledgerline found a problem (no address, a date of birth missing, income declared more than a year ago, a character BSE does not take) and did not send it. Fix the detail and it goes on the next run. A record that has not changed is never sent again.
4. Body corporates and HUFs need directors, contact persons and a CIN that Ledgerline does not keep: register them on BSE's UCC portal.
5. After BSE accepts a client, Ledgerline asks BSE's **PAN status** (BSE allows one request every two minutes). An invalid PAN, a name that differs from the PAN, or a PAN not linked with Aadhaar raises a work item.

Without the API, download the **BSE UCC upload** file from [[xfiles|Exchange files]] (the clients waiting, in BSE's 136-field layout), upload it on BSE's UCC portal and mark those clients as uploaded; BSE's PAN status then confirms them. An Ops checker activates the client once BSE (and the other exchanges) accept the UCC.

## bse-ucc-password
title: Reset the BSE UCC API password
screen: connections
roles: Admin
ask: bse ucc password expired; reset bse ucc api password; bse invalid user id or password; bse ucc login refused; bse checksum error
related: bse-ucc-api

BSE's API passwords expire every **30 days**; Ledgerline raises a work item for the Admin from day 25.

1. On the BSE UCC screen, the Admin chooses **Reset password** and gives a new one: 8 to 20 characters with a capital letter, a small letter, a digit and a special character.
2. Ledgerline changes it at BSE and keeps the new password, encrypted, as the connection's secret. Passwords are never shown or written to the audit trail.

If BSE refuses the login or the checksum, automatic sending stops (the connection shows **Problem**) so that repeated attempts do not lock the user. Check the user ID, password and BSE's public key in [[connections|Connections]] and save it again; sending resumes.
