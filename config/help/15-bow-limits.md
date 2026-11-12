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
ask: bow fund file; bow_fund csv; bow funds upload; bow deposit upload; deposit_upload bow; bow holding file; bow dp holding upload; bow_dp_holding_combined_upload; user stock upload bow; bow holdings not showing; bow pool quantity; bow t1 holdings; bow changed clients file; bow file instead of api; bow commodity deposit file; bow securities and commodities lines
related: bow-limits, bow-setup, bow-uploads

Ledgerline writes the two files the firm uploads each morning in the **BOW Admin terminal** (or at bow1uploads.bseindia.com), in the layouts of BOW's format workbook. Download them on [[live|Live (intraday)]] → **Front-office files** → **BOW uploads**, or from the BOW limits panel. BOW takes these two (user deposits and user stocks) during market hours too; every other BOW upload only from 17:30 to 08:00.

1. **BOW funds file** (`BOW_FUND_<date> <time>.csv`, DEPOSIT_UPLOAD, around 08:13): a deposit line has a **category**, so the file keeps equity and commodity money apart. By default each active client gets **two lines**: category **securities** with the Securities figure (equity money) and category **commodities** with the Commodities figure (MCX money), exchange and market blank (every exchange of the category), as the BOW limits panel computes them. Commodity money never raises the equity limit. In [[connections|Connections]] → **BOW upload files**, **Deposit file: lines per client** can add a **user-level line** (category blank, their sum) or write the **single combined line** of the old back office (category blank, Securities + Commodities). Figures are whole rupees rounded down; a debit is kept as a negative figure; a blocked, inactive or not active client gets 0 (and a client who has stopped being active gets 0 once). The lines have the broker's 20 fields (factor 1, adhoc 0, flags 1, MTM factor 1, effective amount 0, add entry Y); set **Product** in the connection to add BOW's 21st field. **BOW funds, changed clients** has only the clients whose figures changed since the last funds file, for an upload later in the morning.
2. **BOW holdings file** (`BOW_DP_Holding_Combined_Upload_<date>.TXT`, COMBINED DP UPLOAD WITH T1 POOL, around 08:17; BOW Admin → User Stock upload): one line per client per security. **DPQuantity** is the sellable demat quantity (as Finspot's holding file: margin-pledged shares included, less shares pledged to lenders or frozen; unpaid purchases held in CUSPA included); **PoolQuantity** is yesterday's delivery buys not yet received, so they can be sold (BTST). A client with several demat accounts gets one line per security, the accounts added up; the file takes whole units, so a fraction (units held in demat) is left out of each account's quantity. **DPAvePrice** is the client's average cost of the holding (from the DP holdings file and corporate actions; 0.00 when Ledgerline has none), **PoolAvePrice** the average of yesterday's delivery buys. Symbol and series come from the **NSE security master**, the BSE code from the **BSE scrip file**: load both on [[xfiles|Exchange files]] first; a security in neither is left out.
3. **DPCFSHaircut** and **PoolCFSHaircut** are the credit-for-sale haircuts % (10 means 90% of the sell value is available). They are the firm's values in **BOW upload files**, 0 unless the firm uses them; BOW's API document says a pool haircut of **100** gives **no credit for sale** on T1 shares.

The BOW limits API also sets Securities and Commodities apart and needs no upload. Holdings go only by the file (BOW's API cannot write them). Saving **BOW upload files**, or the BOW limits API, puts both files on the daily file checklist.

## bow-uploads
title: Every other BOW upload (users, rights, limits, brokerage, positions)
screen: live
roles: Risk manager, Ops maker, Ops checker
ask: bow upload files; bow ibt user addition; bow user addition file; bow dealer addition; bow offline client ctcl; bow client dealer mapping; bow nnf location; bow trading rights upload; bow risk preferences; bow contract margin; bow right limits; bow profile mapping; bow slb holding; bow update pan; bow only dp holding; bow t1 pool holding file; bow bank upload; bow kra status; bow brokerage master; bow brokerage plan mapping; bow cp code mapping; bow manual position file; bow upload timing; bow file uploader 5.30
related: bow-files, bow-limits

Every upload in BOW's format workbook is on [[live|Live (intraday)]] → **Front-office files** → **BOW uploads**, in BOW's layout (comma-separated, no header; SLB pipe-separated). **BOW's file uploader takes them from 17:30 to 08:00** (only deposits and stocks also during market hours). Upload each in its own option at bow1uploads.bseindia.com.

- **Users**: **IBT users** (internet clients; upload menu INTERNET RETAIL CLIENTS), **Offline (CTCL) users** (clients in the offline client groups, default OFFLINE), **Dealers** (the dealer register), **Client–dealer mapping** (the dealers of the client's authorised person and the firm's dealers for every client), **NNF location IDs** (dealers' terminals with a location ID; clients' trader and location IDs from the connection). **Passwords are never written**: BOW wants an initial password, so set it in BOW (or in the file just before uploading).
- **Rights and limits**: **Trading rights** from each client's segment status (open: allowed; not registered, suspended, dormant or closed: blocked with the reason; a client who closes is blocked once); **Risk preferences**, **Contract margin**, **Right limits** and **Profile mapping** from the firm's values in the connection.
- **Holdings**: DP holding only, T1 pool only and the older DP stocks file, as the combined holdings file. **SLB holding** is empty: Ledgerline keeps no SLB positions.
- **Client details**: **PAN update**, **Banks** (every registered account, primary first, active or inactive; the IFSC when the branch name is not known; account numbers under 10 digits left out), **KRA status** (A validated or registered, S under process, R rejected, H on hold), **CP codes** (NRI clients trading derivatives).
- **Brokerage and positions**: **Brokerage master** from the plans in force (percent, flat, lower of, percent with a minimum, per lot, zero; the BOW channel's lines, default online) with the charge schedule's STT, transaction charges, GST, SEBI fee and stamp duty; lines BOW cannot express (per share, a maximum per order, minimums per share, lot or contract note, expiry and other cases, scrip groups) are left out and listed. **Plan mapping** puts each client on their plan. **Manual positions**: yesterday's carried derivatives positions, net (commodities in lots).

The client and user files carry only the clients (or dealers) new or changed since the last file, and those already written that day; tick **Every client** to send all. A file with nothing to send downloads empty, and the note says why or what was left out.

In [[connections|Connections]] → **BOW upload files**, fill what BOW needs and Ledgerline does not hold, named as in BOW's sheets: **Owner Id** (the admin ID users are created under), offline client groups, BOW dealer IDs, the dealers every client is mapped to, client trader and location IDs, the nine **risk preference** values, **contract margin** lines (Exchange,Market,Segment=Intraday:Delivery, 100 / leverage), **right limit** lines, profile names, the brokerage channel, DP charge per delivery sell and disclaimers, and the manual position product type. A file whose required field has no value is refused with the field and the setting to fill.
