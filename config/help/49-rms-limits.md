Ledgy help articles: RMS beyond margin. Position limits of derivatives (client, the firm's own account, the member, connected clients), index futures and index options limits and the report to the clearing corporation, the F&O ban period, surveillance measures, the expiry-day spread rule, the early pay-in margin rule, margin netted across NSE and BSE, pay-in cut-offs, and one clearing balance per clearing corporation.

# RMS limits beyond margin

## position-limits
title: Watch position limits in derivatives (client, own account, member)
screen: rms
roles: Risk manager, Compliance officer
ask: position limit; mwpl; market wide position limit; client position limit; member position limit; tm level limit; open position limit; exceeded position limit; position limit breach; proprietary position limit; commodity position limit; mcx position limit; limit file; position-limits file
related: index-limits, fo-ban-period, connected-split

Each evening after 18:00 Ledgerline checks every derivative position against its limit. **RMS → Position limits** shows the result for the date. Click **Run checks now** to check again after loading a file.

1. Load the day's limits on [[xfiles|Exchange files]] as **POSITION-LIMITS** (Exchange, Segment, Symbol and any of MWPL, ClientLimit, MemberLimit, OIValue). Quantities are in units of the underlying. With NSE Clearing's margins API on, its MWPL is used for any symbol the file leaves out.
2. A client's open position in an underlying is its long FutEq plus its short FutEq. Codes with the same PAN count as one client.
3. The limit is the file's own number where it gives one. Otherwise it is the firm's share of the MWPL: client 10%, own account 20%, member 30% by default. Change them in **Settings** on the panel.
4. The firm's own account counts at the client-level limit. List the firm's own codes in Settings. A code with the firm's PAN counts as own account too.
5. The member level adds every client and the own account. In commodities the longs and the shorts are added apart and the higher counts.

At 90% a row goes on the watch list. Over 100% Ledgerline raises a work item for the Risk manager. Stop fresh positions in that underlying on the trading front end (square-off only) and have the position reduced. NSE Clearing's own member open interest (margins API, **moi**) shows on the same panel.

Positions on NSE and BSE cleared at one clearing corporation are taken together.

## connected-split
title: Positions split across connected clients' codes
screen: rms
roles: Risk manager, Compliance officer
ask: split positions; positions across codes; acting in concert; group position limit; connected clients position; clubbing of positions; circumvent position limit
related: position-limits, dp-surveillance-rules

Clients who share a mobile, e-mail, bank account or address are connected (see [[surv|Surveillance & AML]]). On **RMS → Position limits**, a connected group is shown when its codes together are at or over the client limit in an underlying.

- **Each code is within the client limit but together they are not** means the position may have been split across codes to get round the limit. The exchange clubs such positions. Check the group, stop fresh positions and reduce them.
- A group where one member is over on its own is shown too. That member also has its own breach row.

## index-limits
title: Index futures and index options limits, and the report to the clearing corporation
screen: rms
roles: Risk manager, Compliance officer
ask: index futures limit; 500 crore; 15% of open interest; index options limit; 1500 crore net; 10000 crore gross; futeq per pan; index option futeq; entity level limit; holdings justification; cash justification; report to clearing corporation; index cover report
related: position-limits

**RMS → Position limits** has an **Index limits** table.

- **Index futures gross**: each client code and the firm's own account, per index. The limit is the higher of 15% of the index futures open interest and ₹500 crore. Give the open interest as OIValue in the POSITION-LIMITS file. Without it the ₹500 crore floor is used, the lower of the two.
- **Index options net, long side, short side**: each PAN's end-of-day FutEq, per index. The limits are ₹1,500 crore net and ₹10,000 crore gross on each side.

A net beyond the limit is allowed only when it is backed: a net short by stock holdings, a net long by cash and cash equivalents. Ledgerline shows what is on record. A fully backed row shows **Justified**.

Whenever a PAN is beyond the net limit, a work item asks you to send the backing to the clearing corporation. Click **Index options cover report** to download it, and upload it in the clearing corporation's format within its timeline. The limits are settings.

## fo-ban-period
title: Trades in a security in the F&O ban period
screen: rms
roles: Risk manager
ask: ban period; f&o ban; security in ban; fo_secban; ban list; trade in banned stock; increase open interest in ban; 95% mwpl
related: position-limits

In the ban period a client may trade a security's derivatives only to reduce the open position (net FutEq) by the end of the day.

1. Load the exchange's ban list for the date as **FO-BAN** (one Symbol column) on [[xfiles|Exchange files]]. With NSE Clearing's margins API on, a security at or over 95% of its MWPL the day before is treated as banned too.
2. Each evening Ledgerline compares each client's net FutEq in a banned security at the start and at the end of the day, both at the day's deltas.
3. An increase raises a work item. Square off the increase, find how the order passed the front end, and record what you did. The exchange may levy a penalty.

The front end must block such orders during the day. Ledgerline finds the ones that got through.

## surveillance-measures
title: Surveillance measures (GSM, ESM, ASM) in the limits
screen: rms
roles: Risk manager, Ops maker
ask: gsm; esm; asm; lt-asm; st-asm; graded surveillance; enhanced surveillance; additional surveillance measure; 100% margin; trade for trade; surveillance margin; stage margin; ibc; ica
related: margin-shortfall, bod-files

Load the exchanges' surveillance lists as **SURV-MEASURES** on [[xfiles|Exchange files]]: Exchange, Symbol, Measure, and Stage, ISIN, MarginPct, TradeForTrade (Y or N), From, To. The list replaces the one before.

- A security under a measure is margined at the measure's rate when it is higher than VaR plus ELM. This applies to intraday cash positions in RMS, Live RMS and margin reporting.
- An intraday trade in a trade-for-trade security raises a work item. So does a buy and a sell of it on the same day.
- **Surveillance margins file** on **RMS → Position limits** gives the list in force for the trading front end.

Additional surveillance deposits and weekly trading limits are set on the front end; Ledgerline does not work them out.

## expiry-spread
title: No calendar spread benefit on expiry day
screen: rms
roles: Risk manager
ask: calendar spread; spread benefit; expiry day spread; spread margin; expiring contract; single stock spread
related: margin-shortfall

On expiry day a single-stock calendar spread that includes the contract expiring that day gets no margin offset.

Ledgerline never gives a spread offset: each leg is margined in full. On an expiry day it lists each client holding such a spread, and raises a work item to tell the client the full margin applies. Check that the trading front end gives no offset either. The rule's start date is a setting.

## epi-margin-rule
title: Square-off of an early pay-in sale needs margin
screen: live
roles: Risk manager
ask: early pay-in square off; epi sale square off; buy back epi; new position without margin; epi margin; margin available epi
related: margin-shortfall, cdsl-early-payin

From the date in **RMS → Position limits → Settings** (22 February 2027 by default), a client may square off a sale backed by early pay-in, or take a new cash position, only when the required margin is actually with the firm.

On [[live|Live RMS]] such a client shows the rule's note when their free margin is below zero. Each evening's check raises a work item for each. Block further buys until the margin comes in. The clearing corporation blocks the resulting position.

## cross-exchange-margin
title: Margin on positions netted across NSE and BSE
screen: rms
roles: Risk manager, Ops checker
ask: margin netted across exchanges; nse and bse same contract; interoperability margin; gross margin both exchanges; netted margin; cross exchange netting; margin too high both exchanges
related: netted-obligations, margin-shortfall

When two exchanges are cleared at one clearing corporation (Settings → **Cleared through**), the clearing corporation margins a client's net position: the same security in the cash market, or the same underlying, expiry, strike and option type in derivatives. Ledgerline does the same for limits and for the allocation of collateral.

- A position on each exchange shows **netted at NCL**. The margin of the net is shared among the legs on the side of the net.
- A segment cleared by another clearing member is margined on its own.
- To margin each exchange apart, tick **Margin each exchange apart** in Settings on **RMS → Position limits**.

## payin-cutoffs
title: Pay-in cut-off reminders
screen: settlement
roles: Ops checker, Ops maker
ask: pay-in cut-off; funds pay-in time; securities pay-in time; 11 am; 10:30; auction pay-in; 8 am; pay-in reminder; late pay-in
related: settlement, internal-shortages

On each settlement day Ledgerline raises work items from 06:00:

- **Funds pay-in** to each clearing corporation, with the net amount, due by 11:00 (Ops checker).
- **Securities pay-in**, with the number of deliveries, due by 10:30 (Ops maker).
- **Auction pay-in** by 08:00 on the business day after an auction (raised from 16:00 on the auction day).

The times are settings on **RMS → Position limits**, because the clearing corporation's circulars change them. A segment cleared by another clearing member gets no reminder.

## cc-split
title: One pay-in for both exchanges cleared at NSE Clearing
screen: clientmoney
roles: Ops checker, Accountant
ask: single pay-in; one ncl debit; split pay-in; cc:nse-cm cc:bse-cm; clearing account per clearing corporation; pay-in reconcile; bse pay-in at ncl
related: payin-cutoffs, netted-obligations

NSE Clearing takes one settlement pay-in for the trades of every exchange it clears. Ledgerline keeps a clearing account per exchange (CC:NSE-CM, CC:BSE-CM).

When a pay-in or pay-out to the clearing corporation is approved, Ledgerline splits it across those accounts. Each gets what it owes on the transfer's date, the chosen segment first. Anything left stays on the chosen segment. The ledger entry shows the split. One debit then clears both accounts.

To post each transfer to one segment only, tick **Post each pay-in to one segment** in Settings on **RMS → Position limits**.
