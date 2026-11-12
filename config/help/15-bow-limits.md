Ledgy help articles: client limits to BOW (BOLTPlusOnWeb) by its API.

# BOW limits

## bow-limits
title: Client limits in BOW (BOLTPlusOnWeb)
screen: live
roles: Risk manager, Ops maker, Ops checker, Compliance officer
ask: bow limits; boltplus limits; boltplusonweb; push limits to bow; bow user deposit; bow deposit not updated; client limit in bow; mcx limit in bow; bow commodities limit; bow securities limit; push to bow now; bow push failed
related: bow-setup, bow-signin, bow-files

Ledgerline sets each client's limit in **BOW** (BSE's front end from MarketPlace Tech, used for internet trading and MCX) through BOW's API, as two **user deposits**: **Securities** (equity) and **Commodities** (MCX). The **BOW limits** panel at the foot of [[live|Live (intraday)]] shows, for each client, the limit now, what BOW was last given, and how the last push went.

What each client gets:

1. **Securities**: the equity money: the ledger at the end of the previous business day less cheques not yet cleared (the same figure as Finspot's limit), plus money in and out since, less any pay-out hold.
2. **Commodities**: the MCX money: with an **MCX-only bank account** (one ledger), the money earmarked for MCX; with a **separate commodity ledger**, the commodity ledger. With neither, Commodities is 0 and BOW gives no MCX limit.
3. A **blocked**, **inactive** or not active client gets 0 on both; a client with a debit balance gets 0. A **closed** client's deposits are removed from BOW.

When it happens (with automatic pushes on in Connections):

- **Start of day** (08:30 by default): every client.
- **During the day** (09:00 to 23:30): a client whose limit changed (money in, payouts, pay-out holds and releases, allocations, moves between MCX and equity money) is pushed within a few minutes. Only changed clients are sent.

Click **Push to BOW now** to send every client, or **Push** on one client. Each push is kept with BOW's answers (never the password or session key). A client BOW refuses shows **Failed** with BOW's reason and the risk manager gets a work item. **Not in BOW** means BOW's client list has no login for that UCC: create the client in BOW, or click **BOW user** and enter the client's BOW internal user ID and login ID.

## bow-signin
title: Sign in to BOW with the OTP
screen: live
roles: Risk manager
ask: bow otp; sign in to bow; bow two factor; bow 2fa; bow session expired; bow limits waiting for sign in
related: bow-limits, bow-setup

When the BOW admin login has two-factor sign-in, BOW needs an OTP once a day before limits can go.

1. On [[live|Live (intraday)]], in the BOW limits panel, click **Sign in to BOW**.
2. BOW sends an OTP to the admin login's registered mobile and e-mail. Enter it and click **Sign in**.
3. Limits waiting for the sign-in (the start-of-day push too) go on the next check, within a few minutes.

The sign-in lasts for the day and is kept only in the server's memory. Until someone signs in, a work item **Sign in to BOW with today's OTP** stays open for the risk manager.

## bow-setup
title: Set up the BOW limits API
screen: connections
roles: Admin
ask: set up bow api; bow api connection; bow vendor code; bow admin login api; marketplace tech api access; bow password expired; bow login locked
related: bow-limits, bow-signin

1. Ask MarketPlace Tech (the BOW team) for API access for the firm's **BOW admin login**, the **vendor code** and front-end version they allot, the API address for **UAT** and **live**, and to allow this server's address (shown on [[connections|Connections]]).
2. In [[connections|Connections]], open **BOW (BOLTPlusOnWeb) limits API** and enter the environment, the admin login ID (e.g. ADMIN-T9065) and password, the vendor code, whether the login has two-factor sign-in, and the multipliers for Securities and Commodities (1 unless the firm gives leverage). MTM is not checked unless an MTM multiplier is entered.
3. Click **Test**: it signs in and asks BOW for the exchange status. Switch **Push to BOW automatically** on when the UAT results are right.

BOW locks the login after three wrong passwords and asks for a new password every 14 days. If BOW refuses the password, automatic pushes stop and the connection shows **Problem**: change the password in the BOW Admin Terminal and save it here again.

## bow-files
title: BOW upload files (funds and holdings)
screen: live
roles: Risk manager, Ops maker, Ops checker
ask: bow fund file; bow_fund csv; bow funds upload; bow holding file; bow dp holding upload; bow_dp_holding_combined_upload; user stock upload bow; bow holdings not showing; bow pool quantity; bow t1 holdings; bow changed clients file; bow file instead of api
related: bow-limits, bow-setup

Ledgerline writes the two files the firm uploads each morning in the **BOW Admin terminal**, in the same layout as the firm's earlier back office. Download them on [[live|Live (intraday)]] → **Front-office files** → **BOW uploads**, or from the BOW limits panel.

1. **BOW funds file** (`BOW_FUND_<date> <time>.csv`, around 08:13): one line per active client with the funds in whole rupees. It is a user-level deposit, so it carries **one figure**: Securities + Commodities, as the BOW limits panel computes them, rounded down to the rupee. A debit is kept as a negative figure; a blocked, inactive or not active client gets 0 (and a client who has stopped being active gets 0 once). **BOW funds, changed clients** has only the clients whose figure changed since the last funds file, for an upload later in the morning.
2. **BOW holdings file** (`BOW_DP_Holding_Combined_Upload_<date>.TXT`, around 08:17; BOW Admin → User Stock upload): one line per client per security. **DP quantity** is the sellable demat quantity (as Finspot's holding file: margin-pledged shares included, less shares pledged to lenders or frozen; unpaid purchases held in CUSPA included); **pool quantity** is yesterday's delivery buys not yet received, so they can be sold (BTST). The DP average price is the client's average cost of the holding (from the DP holdings file and corporate actions; 0.00 when Ledgerline has none), the pool average price the average of yesterday's delivery buys. Symbol and series come from the **NSE security master**, the BSE code from the **BSE scrip file**: load both on [[xfiles|Exchange files]] first; a security in neither is left out.

The funds are better sent by the **BOW limits API**, which sets Securities and Commodities apart; the file cannot. Holdings go only by the file (BOW's API cannot write them). In [[connections|Connections]], **BOW upload files** holds the credit-for-sale haircuts on DP and pool holdings (0 unless the firm uses them; 100 on pool means no credit for sale); saving it, or the BOW limits API, puts both files on the daily file checklist.
