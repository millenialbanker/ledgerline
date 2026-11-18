Ledgy help articles: corporate bond, CP and CD deals on NSE's RFQ platform (Bonds (RFQ deals)).

# Bonds on NSE's RFQ platform

## rfq-deals
title: Confirm a client's RFQ bond deal
screen: rfq
roles: Ops maker, Ops checker
ask: rfq deal; nse rfq; confirm rfq deal; bond deal client; corporate bond deal; cp deal; cd deal; propose consideration; deal propose; consideration confirmation; name the client rfq; rfq client code; inst*; rfq trade split; split bond deal across clients; accept counterparty consideration; reject consideration; change client rfq; cbrics rfq deal; debt deal confirmation
related: rfq-amendments, rfq-settle, rfq-setup

A deal on NSE's RFQ platform is negotiated by the dealer on NSE's screen. Once a quote is accepted the deal is **Traded**; before NSE's consideration cut-off the firm names the client and the consideration is agreed with the counterparty. Ledgerline reads the firm's deals from NSE every few minutes while the market is open (and at once after NSE's notification), and lists them on [[rfq|Bonds (RFQ deals)]] with what each needs.

**The firm proposes** (status *Confirm the client and propose the consideration*): click **Confirm…**.

1. Name the client by UCC. To split the deal across clients, click **Split: add a client** and give each client's bonds: they must add up to the deal.
2. The **RFQ client code** is the client's code on NSE's RFQ platform. Leave it empty to use the code sent for the client before, or the UCC. Ledgerline keeps the code once sent, and matches later deals to the client by it.
3. Give the **price** (per ₹100 of face value) and the **accrued interest** for the whole deal. **Price from NSE's calculator** fills the price and the put or call date at the deal's yield; check the accrued interest basis before entering it.
4. The **consideration** is worked out as face value × price ÷ 100 + accrued interest; type it only to override.
5. Click **Propose for a checker**.

**The counterparty proposed** (status *The counterparty proposed the consideration*): click **Accept…** and name the client (or split it), or **Reject…**. Ledgerline accepts the price, accrued interest and consideration exactly as proposed.

**A different checker sends it**: **Send to NSE**. Ledgerline first reads the deal from NSE again, and checks each client is active, not flagged inactive, and on neither the firm's debarred list nor NSE's. For an acceptance it tells NSE the client first (change client) or the splits (trade split). If NSE refuses, its words show on the deal; send again or **Discard** it.

The firm's own (direct) deals need no client. When the firm is the broker on both sides, say which side the proposal is for.

## rfq-settle
title: Settle and bill a confirmed RFQ deal
screen: rfq
roles: Ops maker, Ops checker
ask: settle rfq deal; rfq settlement; bond deal settlement; rfq pay-in; debt deal billing; rfq deal not in ledger; accrued interest bond deal; mark rfq deal settled; rfq deals csv; bond deal obligation
related: rfq-deals, rfq-amendments

Once both sides agree the consideration, NSE shows the deal **Consideration confirmed** and Ledgerline raises a work item to settle it, with the client, bonds, consideration, accrued interest and settlement date (T+0 or T+1).

Ledgerline does **not yet post debt deals to the ledger**, bill them or put them on a contract note: settlement is through NSE Clearing's delivery against payment, and the brokerage and stamp duty on debt are the firm's own choices. So, for each deal:

1. Download **Deals settling (CSV)** on [[rfq|Bonds (RFQ deals)]] for the settlement date.
2. Arrange the client's funds (a purchase) or securities (a sale) for the pay-in, record the obligation and the brokerage in the ledger by hand, and issue the contract note.
3. Click **Mark settled** and give the pay-in reference or bill number. The work item closes.

A deal cancelled by an approved amendment shows **Cancelled** and needs nothing.

## rfq-amendments
title: Amend or cancel a confirmed RFQ deal
screen: rfq
roles: Ops maker, Ops checker
ask: rfq amendment; modify rfq deal; cancel rfq deal; dealamend; deal amendment; approve amendment; reject amendment; counterparty cancellation; modification reason; cancellation reason; fat finger bond; settlement type change; accrued interest change rfq
related: rfq-deals, rfq-settle

A confirmed deal can be modified (yield type, accrued interest and consideration, settlement type) or cancelled; the other side approves or rejects.

**The counterparty's amendment**: it appears under **Amendments** on [[rfq|Bonds (RFQ deals)]] with its reason, and a checker gets a work item. A checker clicks **Approve**, or **Reject…** with remarks the counterparty sees. An approved cancellation leaves nothing to settle; an approved modification changes the deal's accrued interest, consideration and settlement date.

**The firm's amendment**: on the deal, **Amend…**, choose cancel or modify and the reason from NSE's list:

- Modification: SETTYP (settlement type), ACCRINT (accrued interest and consideration), YLDTYP (yield type).
- Cancellation: FATFNGR (fat finger error), CSHFLO (cash flow mismatch), SHRT (short sell), ILLIQ (liquidity constraints), SHTPRD (shut period).

A different checker sends it. A deal that was split across clients is modified on NSE's screen. For a brokered deal of an institutional client with its own RFQ access, NSE takes the amendment only from the client.

## rfq-setup
title: Set up NSE RFQ (debt)
screen: connections
roles: Admin
ask: set up nse rfq; rfq api; rfq connection; cbrics api user; rfq login key; rfq password expired; rfq callback; rfq notification; rfq signing key; rfq market timings; rfq broker registration; bricsonline
related: rfq-deals

1. Get from NSE: the firm registered on the RFQ platform as a **broker**, an **API-enabled CBRICS user** for the firm's participant code, and, for notifications, NSE's **signing key** for the callback. Use a user of its own: a second sign-in with the same login ends the first session.
2. In [[connections|Connections]], open **NSE RFQ (debt)**: participant code, API user login ID, password, environment (UAT for NSE's test platform). Leave the address empty for NSE's.
3. For notifications, enter a long random **callback token** and NSE's **signing key**, then give NSE the callback address shown on [[rfq|Bonds (RFQ deals)]] → Connection & timings (NSE adds /v1/notification). A notification is checked against the token in the address and its signature, and only makes Ledgerline read the deals from NSE again.
4. Click **Test**: it signs in and reads the day's market timings. It also says when NSE does not show the firm as a broker.

Deals are read every 5 minutes (or the minutes set) on business days from the first market opening to half an hour after the last consideration cut-off. When the password expires, change it on CBRICS and save the new one here.

Posting RFQs, quoting and negotiating, participant groups and limits stay on NSE's screen.
