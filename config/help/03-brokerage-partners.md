Ledgy help articles: brokerage, charges, branches and partners.

# Brokerage and charges

## change-brokerage
title: Change a client's brokerage
screen: brokerage
roles: Ops checker
ask: change brokerage; reduce brokerage; client wants lower brokerage; brokerage on delivery; brokerage concession; increase brokerage; brokerage rate; set brokerage for a client; special brokerage; change brokerage plan; brokerage for a group; new brokerage slab; flat brokerage
related: brokerage-plans, correct-bill, client-groups, run-billing

1. Open [[brokerage|Brokerage plans]].
2. If no plan has the rates you need, click **New plan** (see "create a brokerage plan").
3. Click **Assign a plan** (or **Set for a client**) and choose:
   - **A client:** only that client.
   - **A group:** everyone tagged with that group (HNI, STAFF and so on).
   - **Firm default:** everyone without their own or a group plan.
4. Choose the **effective from** date and save. Assigning needs an **Ops checker**.

**Which plan applies:** the client's own plan, else their group's (the most recently assigned group if several), else the firm default, else the charge schedule.

**Days already billed don't change by themselves.** To change them, use **Rebill changed trades** on [[billing|Billing]], or **Correct** for one client's past bill (it sends a revised contract note).

Statutory charges (STT, exchange, SEBI, stamp duty, GST) never come from a plan. Brokerage is capped by the exchange limit (2.5% in cash).

## brokerage-plans
title: Create a brokerage plan
screen: brokerage
roles: Ops checker
ask: new brokerage plan; create plan; brokerage per order; per lot brokerage; percentage brokerage; minimum brokerage; maximum brokerage; zero brokerage; free delivery
related: change-brokerage

1. On [[brokerage|Brokerage plans]], click **New plan**, give it an ID and name.
2. For each product (delivery, intraday, futures, options; per exchange if needed) choose the rule:
   - **% of turnover**, **₹ per order**, **lower of % or ₹ per order**, **₹ per lot** (derivatives only) or **free**
   - optional minimum and maximum per order
3. Save, then assign it.

To change rates later, **Edit rates** with an effective date. Earlier days keep the old rates, so history stays correct.

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
