Ledgy help articles: brokerage, charges, branches and partners.

# Brokerage and charges

## change-brokerage
title: Change a client's brokerage
screen: brokerage
roles: Ops checker
ask: change brokerage; reduce brokerage; client wants lower brokerage; brokerage on delivery; brokerage concession; increase brokerage; brokerage rate; set brokerage for a client; special brokerage; change brokerage plan; brokerage for a group; new brokerage slab; flat brokerage; client override; client specific rate; different rate on BSE; card for one segment
related: brokerage-plans, client-rates, brokerage-import, correct-bill, client-groups, run-billing

1. Open [[brokerage|Brokerage plans]].
2. If no plan has the rates you need, click **New plan** (see "create a brokerage plan").
3. Click **Assign a plan** (or **Set for a client**) and choose:
   - **A client:** only that client.
   - **A group:** everyone tagged with that group (HNI, STAFF and so on).
   - **Firm default:** everyone without their own or a group plan.
4. Optionally choose a **Segment** (CM, FO, CD or COM) and an **Exchange**, to put the plan on that segment or exchange only. For example, a client can have one card for NSE cash and another for everything else.
5. Choose the **effective from** date and save. Assigning needs an **Ops checker**.

**Which plan applies:**
- **The plan:** the client's own plan, else their group's (the most recently assigned group if several), else the firm default, else the charge schedule. At each of these levels, a plan for the order's segment on that exchange is tried first, then one for the segment, then one for all segments. A plan with no line for the order passes it on to the next.
- **The client's own rate lines:** set with **Client rates**, these win over the plan for the orders they fit.

**Days already billed don't change by themselves.** To change them, use **Rebill changed trades** on [[billing|Billing]], or **Correct** for one client's past bill (it sends a revised contract note).

Statutory charges (STT, exchange, SEBI, stamp duty, GST) never come from a plan. Brokerage is capped by the exchange limit (2.5% in cash).

## brokerage-plans
title: Create a brokerage plan
screen: brokerage
roles: Ops checker
ask: new brokerage plan; create plan; rate card; brokerage per order; per lot brokerage; per share brokerage; percentage brokerage; minimum brokerage; minimum per share; minimum paise per share; minimum per lot; maximum brokerage; zero brokerage; free delivery; online brokerage; mobile brokerage; dealer brokerage; NSE and BSE rates; auction brokerage; rollover brokerage; expiry brokerage; exercise brokerage; assignment brokerage; index options brokerage
related: change-brokerage, client-rates, trading-channels

1. On [[brokerage|Brokerage plans]], click **New plan** and give it a name.
2. Add a **rate line** for each rate. A line is a product (delivery, intraday, futures or options in equity, currency or commodity), optionally narrowed to:
   - **Exchange:** for example a lower delivery rate on BSE.
   - **Channel:** dealer, online, mobile, or your own channels (see "trading channels").
   - **Case:**
     - **auction** (cash market)
     - **rollover** (both legs of a futures position moved to a later expiry the same day)
     - **futures expiry**
     - **option exercise** (long) or **option assignment** (short)
     - **index** or **stock** derivatives
3. Choose the charge:
   - **% of turnover**, **₹ per order**, **lower of % or ₹ per order**, **₹ per lot** (derivatives) or **₹ per share**, or **free**.
   - Optional **minimum per share** (for example 3 paise) or **minimum per lot**. These grow with the order.
   - Optional **minimum** and **maximum per order**.
4. Save, then assign it.

**Which line applies to an order:** the most specific one that fits. A matching case comes first, then a matching channel, then a matching exchange; the plain product line comes last. A product with no line uses the charge schedule.

**At expiry:** brokerage on futures expiry, exercise and assignment is charged only where the plan has a line for that case. It is posted when derivatives are settled. A % rate applies to the settlement value (settlement price, or the underlying's price for options, times the quantity).

To change rates later, **Edit rates** with an effective date. Earlier days keep the old rates, so history stays correct.

## client-rates
title: Give a client their own rates
screen: brokerage
roles: Ops checker
ask: client override; client specific brokerage; override brokerage for one client; client's own rate; special rate for one client on options; negotiated rate
related: brokerage-plans, change-brokerage

1. On [[brokerage|Brokerage plans]], click **Client rates** and choose the client.
2. Add only the lines that differ from the client's plan. For example, options at ₹15 per lot online.
3. Choose the effective date and save.

The client's own lines win over their plan for the orders they fit. At equal detail, the client's line wins; a more detailed plan line (for a case, a channel or an exchange) still applies where the client has no line that detailed. To end them, open **Client rates**, remove every line and save with the date they end.

## trading-channels
title: Set up trading channels
screen: brokerage
roles: Ops checker
ask: trading channel; online rate; mobile rate; dealer rate; terminal; CTCL ID; which trades are online; channel brokerage
related: brokerage-plans, brokerage-import

1. On [[brokerage|Brokerage plans]], click **Channels**.
2. Name the channels you price separately (for example dealer, online, mobile, algo) and choose the default.
3. Add terminal rules, one per line, as `terminal = channel`, using `*` for any run of characters (for example `1234567*=online`).

Each trade gets its channel from the terminal (CTCL) ID in the exchange trade file: the first rule that matches, else the default. Auction trades are recognised by the settlement type in the trade file. Index derivatives are recognised by the underlying; NIFTY, BANKNIFTY, SENSEX and the other main indices are built in, and you can add more.

## brokerage-import
title: Bring brokerage rates across from another back office
screen: files
roles: Ops checker
ask: import brokerage; import rate cards; migrate brokerage; load brokerage rates; import client brokerage; brokerage for inactive clients; brokerage for suspended clients
related: brokerage-plans, client-rates, trading-channels, load-files

Load these files on [[files|Files]], in this order. A converter for the old system writes them; loading a file twice changes nothing. Loading needs an **Ops checker**.

1. **BROKERAGE-CARDS:** every rate card with its history, one row per rate line. Columns:
   - Card, From, Product and Mode.
   - Optional Name, Exchange, Channel, Case, Pct, Flat, PerLot, PerShare, MinPerShare, MinPerLot, Min and Max.
   - All the lines of a card with the same From make that version.
2. **BROKERAGE-CLIENTS:** which card each client is on, with history. It covers every client whatever their status: active, inactive, suspended or closed. Columns:
   - UCC, Card and From.
   - Optional Segment and Exchange, for a card on one segment or exchange.
   - UCC can also be DEFAULT for the firm default, or GROUP:<name>.
   - A blank Card ends the earlier card from the date.
3. **BROKERAGE-CLIENT-RATES:** clients' own rate lines, in the same columns as the cards, keyed by UCC.
4. **TERMINAL-CHANNELS:** Terminal and Channel.

Load the client master first: rows for unknown clients are refused, and so is the whole file.

## charges-check
title: Check a charge a client disputes
screen: charges
roles: Ops maker, Ops checker
ask: client disputes charges; check charges; why was client charged; stt calculation; stamp duty; transaction charges; charges breakdown; brokerage calculator
related: change-brokerage, correct-bill

1. Open [[charges|Charges calculator]].
2. Enter the order (product, exchange, buy or sell, quantity, price) and choose the client. It uses the client's plan, exactly as billing does.
3. Compare with the contract note. The note lists every charge per trade.

If the brokerage is wrong, fix the plan and **Correct** the bill. Statutory charges follow the charge schedule; if those look wrong, the schedule needs updating.

## prepaid
title: Sell a prepaid brokerage scheme
screen: prepaid
roles: Ops checker, Admin, Branch staff
ask: prepaid brokerage; prepaid plan; subscription plan; brokerage credit; annual plan; sell scheme
related: change-brokerage

On [[prepaid|Prepaid schemes]]:

1. **New scheme** (once): a **credit** scheme (pay an amount, brokerage is adjusted against it until used or expired) or a **plan** scheme (pay a fee for a period with a cheaper plan).
2. **Sell a scheme** to a client. The price is debited to their ledger.
3. Credit schemes adjust each day's brokerage automatically; unused credit lapses at expiry.

# Branches and partners

## add-ap
title: Add an authorised person (AP)
screen: partners
roles: Admin, Ops checker
ask: add ap; new authorised person; add sub broker; add franchise; register ap; create partner; new partner; ap registration; add remisier
related: map-client, ap-login, ap-payout, add-branch

First register the AP with the exchanges (AP registration on each exchange's member portal). Then:

1. Open [[partners|Branches & partners]] and click **New partner**.
2. Enter:
   - **Code:** 3–10 letters or digits, for example AP001
   - **Name** and **Kind:** Authorised person (or Relationship manager)
   - **Branch** and **Reports to**, which place it in the office tree
   - **Share %:** their share of brokerage on their clients
   - **PAN**, **GSTIN** (if GST registered), **exchange registration number**, **email**
3. Save. Managing partners needs an Admin or Ops checker.

Next: map their clients to them ("map a client to an AP") and give them a login ("give an AP or branch a login"). New clients from the AP's online link (with `?ap=CODE`) are mapped automatically.

## add-branch
title: Add a branch or office
screen: partners
roles: Admin, Ops checker
ask: add branch; new branch; new office; zone; region; franchise office; office hierarchy; branch tree
related: add-ap, ap-login, map-client

1. On [[partners|Branches & partners]], click **New branch**.
2. Give a code and name, the **kind** (Zone, Region, Branch or Franchise) and the **parent** office it reports to.
3. Save.

Staff of an office see the clients of that office **and every office under it**. An office can't report to one beneath it (that would make a loop).

## map-client
title: Map a client to an AP, branch or RM
screen: clients
roles: Ops checker, Admin
ask: map client to ap; change client branch; assign client to ap; transfer client to another ap; change relationship manager; client under wrong ap; move client branch
related: add-ap, ap-payout

1. Open the client on [[clients|Clients & onboarding]].
2. Under **Branch and partner**, click **Change**.
3. Choose the branch, AP, relationship manager and referrer, and save.

The AP earns their share on the client's brokerage from then on; earlier months stay with whoever had the client then.

## ap-login
title: Give an AP or branch staff a login
screen: settings
roles: Admin
ask: ap login; branch login; give ap access; sub broker login; branch user; partner login; ap cannot see clients
related: add-user, add-ap

1. Open [[settings|Settings]] and click **Add user**.
2. Role **Authorised person** with their partner code, or **Branch staff** with their branch.
3. Give them the one-time password through a separate channel. They must set a password and two-factor sign-in.

They see only their own clients (an AP) or their office and those under it (branch staff), and only client-level screens. Firm-wide screens are closed to them.

## ap-payout
title: Pay APs their brokerage share
screen: partners
roles: Ops checker
ask: ap payout; ap commission; sub broker payment; ap share; pay partner; ap brokerage sharing; tds on ap commission; 194h
related: add-ap, map-client

1. On [[partners|Branches & partners]], choose the month and click **Show** to see each AP's share.
2. Ledgerline works out the share of brokerage on their clients, deducts **TDS under section 194H** (rate in Settings), adds **GST** if the AP is registered, and **holds back** the amount of their clients' overdue debits.
3. AP payouts are posted with the month-end postings ([[autopilot|Autopilot]] → Month-end postings → **Post all**), approved by an Ops checker.

## limit-request
title: Request extra limit for a client (branch or AP)
screen: rms
roles: Branch staff, Authorised person, Risk manager
ask: extra limit; increase limit; limit request; more exposure; client needs more limit; limit override; enhance limit
related: margin-shortfall, client-cant-trade

- **Branch staff or AP:** on [[rms|RMS & limits]], click **Request limit** for your client, give the amount and reason. It goes to head-office risk.
- **Risk manager or Ops checker:** the request appears in the work queue and on RMS & limits; **Approve** or **Reject**.

An approved override adds to the client's trading limit for the day only.

## referral
title: Reward client referrals
screen: partners
roles: Ops checker
ask: referral; refer a friend; referral reward; client referred another client; referral brokerage
related: add-ap

1. Record the referrer on the new client (**Branch and partner → Change → Referred by**).
2. The referrer earns the share of the referred client's brokerage set in [[settings|Settings]], for the set number of months.
3. On [[partners|Branches & partners]], **Post rewards** for the month credits referrers' ledgers.
