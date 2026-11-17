Ledgy help articles: sending Finspot its limits, holdings, blocks, client logins and margin baskets by the Noren admin API.

# Finspot by API

## finspot-api
title: Limits and holdings to Finspot by API
screen: live
roles: Risk manager, Ops maker, Ops checker, Compliance officer
ask: finspot api; push to finspot; finspot limits by api; noren api; norenwcadmin; kambala; finspot limit not updated; finspot holdings by api; finspot t1 by api; finspot push failed; finspot refused; set limits finspot; check finspot limit; finspot limit differs
related: finspot-api-setup, bod-files, bow-limits

Finspot runs on **Noren**, Kambala Solutions' OMS, and its admin API takes what the Finspot upload files carry, one client at a time. With **Finspot by API** set up in [[connections|Connections]], Ledgerline sends it, and the **Finspot by API** panel at the foot of [[live|Live (intraday)]] shows each client's limit now, what Finspot was last given and the latest pushes with Finspot's answers.

What goes, and when (with automatic pushes on):

- **Start of day** (08:40 by default, before 09:00): blocks (when they go by API), every client's **limit**, the **ISIN holdings**, the **T1 holdings** and, when switched on, the **margin baskets**. Each part is tried again every few minutes until it goes, and ticks off the day's file checklist as its file would.
- **During the day** (09:00 to 20:00): a client whose money changed (money in, payouts, pay-out holds and releases, allocations) is sent within a few minutes; every few minutes, any limit, block or client login that differs from the last push. Only what changed is sent.

The figures are the files' (see [[bod-files|Send limits and holdings to the trading system]]):

1. **Limit** (SetLimits): **cash** is the equity money as it stands: the ledger at the end of the previous business day less cheques not yet cleared, plus money in and out since, less pay-out holds; a debit is sent as is, as in the RMS Limits line. **Notional cash** (cash-equivalent collateral after haircut) goes in the field chosen in the connection (prevalued collateral unless Finspot says otherwise). A blocked client gets no positive cash and no notional cash.
2. **ISIN holdings** (UpdateAbsHoldingsISIN): every line of the ISIN Holding file, with margin-pledged non-cash collateral, collateral mode 1 (LTP). An ISIN Ledgerline sent before that the client no longer holds is sent at 0, so Finspot keeps no stale holding.
3. **T1 holdings** (UpdateBTSTAbsHoldings): Finspot names these by the exchange **token**, which comes from the day's NSE and BSE common bhavcopy. A line whose ISIN has no token is not sent and the risk manager gets a work item: load the bhavcopy, or upload the T1 file.

Click **Push to Finspot now** for every part, a part's button for one part, or **Push** on one client. **Check Finspot** reads a client's limit and holdings back from Finspot and shows where they differ from the last push (nothing read is kept). A client Finspot refuses shows **Failed** with Finspot's words and a work item goes to the risk manager.

**NFO cost price**, **dealer-client positions** and **dealer order types** have no Finspot API: they stay as files on [[rms|RMS & limits]] → Finspot uploads. Any file can still be uploaded by hand.

## finspot-api-setup
title: Set up Finspot by API
screen: connections
roles: Admin
ask: set up finspot api; finspot vendor code; finspot vendor key; noren vendor code; finspot admin api login; finspot ip whitelist; finspot api password expired; finspot login locked; notional cash field finspot; finspot blocks by api; finspot client creation by api; finspot margin basket api; npoa holdings finspot
related: finspot-api, bod-files

1. Ask Finspot (the Noren team at Kambala) for API access for the firm's **admin login**, the **vendor code** and **vendor key**, the API address for **UAT** and **live** (it ends `/NorenWCAdmin`), and to allow this server's IP address. Give the server's MAC address if they ask for one.
2. In [[connections|Connections]], open **Finspot by API** and enter the environment, address, admin login ID and password, vendor code and vendor key. The firm's Finspot values (account ID suffix, exchange user IDs, order types, basket names) stay in **Finspot uploads**.
3. Choose:
   - **Notional cash goes in**: Finspot's API has no field named notional cash; prevalued collateral (brkcollamt) is the default. Confirm with Finspot.
   - **Holdings of clients without a POA or DDPI**: the same call as other holdings, or Finspot's NPOA ISIN Holdings call (with the DP account), as Finspot advises.
   - **Blocks**: by the block file (the default), or by API. The API blocks the whole account or all fresh orders, where the file blocks new MIS and NRML positions and CNC buys only, so a blocked client can still sell holdings to clear a debit. Keep the file unless Finspot offers a block that allows selling.
   - **Client logins** and **margin baskets** by API: off by default. New clients are added without a password (Finspot sets it, as with the file); changed ones are modified without a password. Use either the API or the client files, not both.
4. Click **Test**: it signs in. Push one part by hand on Live RMS in UAT, check a few clients with **Check Finspot**, then switch **Push to Finspot automatically** on.

The password, the vendor key and the session are never kept in a push's record. If Finspot refuses the sign-in (wrong or expired password, IP not allowed, vendor code or key wrong), automatic pushes stop and the connection shows **Problem**, so a locked login is never tried again by itself: fix it and save the connection again. During Finspot's EOD and BOD the API answers "unknown request"; the push stops and the next check tries again.
