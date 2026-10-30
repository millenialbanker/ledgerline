Ledgy help articles: exchange UCC by API.

# Exchange UCC by API

## ucc-api
title: Exchange UCC by API
screen: uccapi
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: ucc api; uci api; nse uci api; nse ucc api; send ucc by api; register clients with nse by api; exchange ucc screen; ucc sent to nse; ucc not sent; ucc needs fixing; bse ucc api; send clients to exchange automatically
related: ucc-api-setup, ucc-api-fix, ucc-api-pan, ucc-upload, ucc-rejected

The **Exchange UCC** screen (Clients & onboarding) registers clients with each exchange through its UCC API instead of the batch file. It has a tab per exchange: **NSE** (NSE's UCI API), **BSE** (BSE's UCC REST API) and MCX, which takes the UCC file only.

What goes to the exchange:

1. **New clients** at the UCC upload stage (and active clients the exchange has not yet taken).
2. **Approved changes**: a change request (address, bank, e-mail, mobile, nomination) once a checker approves it.
3. **Segment status changes** (active, suspended, dormant, closed) and **inactive flags and reactivations**.

Each client's record is built from the client record, the Exchange UCC details and the online application, and checked against the exchange's field rules before it goes. A record that has not changed since the exchange took it is **never sent again**. NSE takes one record per segment, so a change to one segment's status sends only that segment.

Each client shows a status:

- **Not sent**: new or changed and ready.
- **Needs fixing**: something is missing or not in the exchange's format; the problems are listed in words. Fix them and the client becomes Not sent.
- **Sent**: sent, and the exchange has not yet confirmed it.
- **Accepted**: the exchange saved it. **Add** is a client new to the exchange, **Modify** a change.
- **Rejected**: the exchange refused it, with its reason. An Ops maker gets a work item; fixing the record sends it again.

Click **Send pending** to send every client that is Not sent, or tick clients and click **Send selected**. **Preview** shows exactly what would go (PAN and account numbers masked), the problems, the history, and the Exchange UCC details. Every call's request and answer are kept with the run, with Aadhaar masked and never the password.

## ucc-api-setup
title: Set up the NSE UCC API
screen: connections
roles: Admin
ask: set up nse ucc api; nse uci api user; register nse api user; uci api password; connect2nsccl; nse ucc api login failed; uci api registration; switch ucc api to production
related: ucc-api, connections

1. Register the API user once on NSE's API registration page (connect2nsccl.com, **api-reg**): the member code, a password and the e-mail address; NSE e-mails a one-time code to finish. Then write to NSE's UCI desk (uci@nse.co.in) to **activate** the user. The same page resets the password.
2. In [[connections|Connections]], open **NSE UCC API (UCI)** and enter the member code, the password, and the environment: **UAT** while testing with NSE, **Production** once NSE has approved the firm. The address is filled in; change it only if NSE gives another.
3. Switch **Send new and changed clients automatically** On when the firm is ready: Ledgerline then sends every 15 minutes from 08:00 to 22:00 and refreshes PAN status once a day. With it Off, staff send from the Exchange UCC screen.

If NSE allows calls only from listed addresses, give NSE the server address shown on the Connections page.

BSE works the same way through BSE's **UCC REST API**: set up its connection on the same page and use the BSE tab of [[uccapi|Exchange UCC]].

## ucc-api-fix
title: Fix a client that needs fixing before it goes to the exchange
screen: uccapi
roles: Ops maker, Ops checker
ask: ucc needs fixing; address line 1 missing; father's or spouse's name missing; occupation salaried public or private; income declared older than previous calendar year; exchange ucc details; contact persons for company ucc; cin for ucc; why is the client not sent to nse
related: ucc-api, change-contact

The problems are listed against the client on [[uccapi|Exchange UCC]]. The usual ones:

1. **Address line 1, PIN, gender, father's or spouse's name, marital status, occupation**: these come from the online application. For other clients, open **Preview**, fill in **Exchange UCC details** and save.
2. **Occupation "Salaried"**: the exchange asks whether it is private sector, public sector or government service. Ask the client and enter it in the details.
3. **Income declared on**: the exchange wants the income declaration no older than the previous calendar year. Take a fresh declaration and update the client's income (a KYC update).
4. **Demat account** for the cash segment, **IFSC** or **bank account** in the wrong form: correct them with a change request.
5. **Partnerships, HUFs, companies and trusts** need their contact persons (name, PAN, address, phone; for a company the director's DIN) and a company its CIN: enter them in the details, one a line.
6. **Client category** not sent by API (foreign investors, institutions): register those on the exchange's own portal.

When nothing is left to fix, the client shows Not sent and goes with the next send.

## ucc-api-pan
title: PAN status at the exchange
screen: uccapi
roles: Ops maker, Ops checker, Compliance officer
ask: pan status at nse; check pan status ucc; incorrect pan nse; pan verification pending nse; not permitted to trade nse; aadhaar seeding status; nse pan status approved; client not permitted to trade
related: ucc-api, ucc-rejected, kra-status

The exchange checks each client's PAN, name and date of birth with the Income Tax Department. **Check PAN status** asks it for the clients that are not yet approved (or for those ticked); with automatic sending on, Ledgerline also asks once a day.

- **Approved**: nothing to do.
- **Pending**: the exchange has not finished checking.
- **Incorrect PAN**: the PAN, name or date of birth does not match the PAN record. An Ops maker gets a work item: check them against the PAN card, correct the client record, and the corrected record is sent again.
- **Not permitted to trade** in a segment: the compliance officer gets a work item to find out why (PAN verification, PAN-Aadhaar link) before the client trades.
