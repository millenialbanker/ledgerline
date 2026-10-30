Ledgy help articles: CDSL's pledge and early pay-in APIs.

# CDSL APIs: pledges and early pay-in

## cdsl-pledge-apis
title: CDSL APIs: pledges and early pay-in
screen: cdslpledge
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: cdsl pledge api; margin pledge api; pledge through cdsl; client pledge otp; pledge confirmation cdsl; margin pledge setup; repledge api; re-pledge to clearing corporation; normal pledge; loan against shares pledge; unpledge api; release pledge; confiscate; invoke pledge; early pay-in api; epi; psn
related: cdsl-pledge-invocation, cdsl-early-payin, cdsl-pledge-go-live

As the firm's CDSL DP, Ledgerline sends pledges and early pay-ins to CDSL through its APIs instead of CDSL's own screens. Nothing goes to CDSL until a second person approves it.

**Margin pledge** ([[cdslpledge|Pledges (CDSL)]], Margin pledges):

1. The client asks in the portal (Pledge shares), or the desk enters it with **New margin pledge**. The MTF pledge of a funded position works the same way.
2. A checker who did not enter it approves it. The client gets a message to confirm.
3. In the portal the client chooses **Confirm with CDSL**. Ledgerline hands them to CDSL's page, which shows the pledge and sends a one-time password to the mobile and email registered with the demat account. The link works once, for 15 minutes.
4. CDSL sends the client back with the result. When the pledge is set up, its **PSN** is recorded and the shares count as pledged collateral in RMS and on Client collateral & pledge. If the result does not come back, Ledgerline asks CDSL every few minutes; a hand-over the client never finished goes back to waiting for them, and approved requests lapse after 7 days.

**Re-pledge** (Re-pledge tab): risk ticks the clients' margin pledges to re-pledge to the clearing member or clearing corporation, a checker sends them. Once NSE Clearing's re-pledge status file is loaded (Files), the tab compares what CDSL accepted with what NSE Clearing shows.

**Pledges to lenders** (loan against shares): a DP maker enters the client's signed pledge form with the lender's BO ID; a DP checker sends it. These shares are not margin with the firm and are not free to pledge again.

**Unpledge**: the client asks in the portal (or the desk enters it); a checker sends it. Released margin pledges come off the client's pledged quantity. A pledge re-pledged to the clearing corporation, or needed by an open MTF position, cannot be released.

If CDSL does not answer a request, it shows **No reply**: look it up in CDSL and use **Record result**; never send it again blindly.

## cdsl-pledge-invocation
title: Invoke a client's pledge for an unpaid debit or MTF liquidation
screen: cdslpledge
roles: Risk manager, Compliance officer
ask: invoke pledge; invocation of pledge; confiscate pledged shares; sell pledged shares for debit; recover debit from pledged shares; mtf liquidation pledge; pledge invocation notice; debit not paid pledged shares
related: cdsl-pledge-apis, debit-interest

The Unpledge & invocation tab lists pledges that **may be invoked**: clients whose debit is overdue past the blocking period (Debtors), and MTF positions to be liquidated (Margin trading). The scheduler puts each in the risk team's work queue.

1. **Risk** raises the invocation with the quantity (Ledgerline suggests enough to cover the debit at the last price).
2. **Compliance** (not the person who raised it) approves it. The client is sent notice by email and SMS with the amount, the shares and the date by which to pay. The notice period is in Settings (1 day unless changed).
3. When the notice runs out, Ledgerline sends the invocation to CDSL, unless the debit has been paid or the MTF position closed meanwhile, in which case it is cancelled. Risk may also use **Invoke now** once the notice is over.
4. The shares move to the firm's account; holdings drop. A work item asks risk to sell them and credit the proceeds (Debtors), or to close the MTF position.

## cdsl-early-payin
title: Early pay-in through CDSL's API
screen: cdslepi
roles: Ops maker, Ops checker
ask: early pay-in; early payin; epi; early pay in of securities; sell without margin; avoid short delivery; credit against sale; segmon early pay-in; ep transaction id
related: cdsl-pledge-apis, cdsl-pledge-go-live

The [[cdslepi|Early pay-in]] screen lists the day's delivery sells (net of buys) per client and scrip.

1. A DP maker clicks **Enter** on a sell (or **Enter early pay-in**). Clients with DDPI need nothing more; otherwise give the DIS or eDIS number. The CDSL settlement ID (13 digits) comes from CDSL's settlement calendar when it is loaded (Files); otherwise type it.
2. A DP checker who did not enter them ticks them and clicks **Send selected**. CDSL answers at once with an early pay-in transaction ID for each ISIN, or the reason it refused.
3. Requests CDSL did not answer are asked about every few minutes, and left for a person after a day.

Accepted early pay-ins are kept by date, client, ISIN, quantity and settlement. Live (intraday) risk treats those sales as delivered: no short delivery and no auction reserve. They also feed credit against sale and SEGMON.

## cdsl-pledge-go-live
title: Set up CDSL's pledge and early pay-in APIs
screen: connections
roles: Admin, Compliance officer, Risk manager
ask: set up cdsl pledge api; cdsl api key; cdsl margin pledge registration; whitelist ip cdsl; cdsl mock testing; pledgee bo id; early pay-in bo id; go live cdsl pledge
related: cdsl-pledge-apis, cdsl-early-payin

1. Ask CDSL (through the DP's relationship manager) for the API documents and register for margin pledge: CDSL needs a registration form with the DP ID, SEBI registration number, contact details and the authorised signatory. A DP already registered for eDIS uses the same API key.
2. Ask CDSL's helpdesk for the mock details, and give the server's public static IP address for whitelisting (re-pledge, normal pledge and early pay-in calls come from it).
3. On [[connections|Connections]], fill **CDSL APIs (DP)**: DP ID, environment (Mock), the API key, and Ledgerline's public address (CDSL sends clients back to it after the OTP).
4. On Pledges (CDSL) → Settings, fill the firm's pledgee BO IDs, TM, CM, exchange and segment IDs, reason codes, the re-pledge pledgee, and the early pay-in BO, CM ID and DDPI POA ID.
5. Switch on each API family on the connection, test in Mock, send CDSL the successful logs, then ask CDSL to enable the DP ID in Live and change the environment to Live.
