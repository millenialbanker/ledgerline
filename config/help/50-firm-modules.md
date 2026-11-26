## firm-modules
title: Firm modules: choose what the firm does
screen: firmmodules
roles: Compliance officer, Admin, Ops maker, Ops checker, Risk manager
ask: firm modules; choose our modules; which exchanges do we trade; self clearing member; trading member only; professional clearing member; tm-cm; clears for other brokers; are we a depository participant; which products do we offer; dma; smart order routing; client api access; slb segment; early pay-in of funds; mfos; tpp distributor; saral accounts; fax instructions; partnership firm; llp; multiple gstin; qualified stock broker; qsb; ex qsb; specified stockbroker; number of active clients; registered clients count; clients trading volume; compliance changes with modules; duties switched on; hide screens we do not use; show all modules; what changes if I save
related: firm-profile, circular-applies, compliance-profile, firm-modules-numbers, firm-modules-conditions

Settings → **Firm modules** is the one place the firm says what it does. The compliance calendar, self-inspection and the menu follow it. Everyone on the staff can read it; the compliance officer and an Admin change it.

**What you choose:**
- **Exchanges and clearing.** The member code of each exchange, and for each exchange segment (NSE and BSE cash, F&O, currency and SLB, NSE commodity, MSE, MCX, NCDEX) one role: **Not traded**, **Trading member** (another clearing member clears: give its code and name), **Self-clearing member** (own trades only) or **Clears for other members too** (a TM-CM or professional clearing member). For a clearing member, choose the clearing corporation. This writes Settings → member codes and **Cleared through**, so the two screens always agree.
- **Depository.** CDSL and NSDL participant.
- **Products.** MTF, algo trading, internet or mobile trading, client API access, DMA, SOR, early pay-in of funds, MFOS, third-party products, institutional (DVP) clients, client BGs for hedging, MCX hedge limits, SGTCM, buybacks, own corporate bond trading, underwriting. Where it matters, tick the exchanges.
- **Depository options.** SARAL, escrow accounts, fax and e-instructions, bank BOs, MF redemption, accredited investors, IPO allotment pledge, nil-balance statement exemption, drop boxes, arbitration.
- **The firm.** Constitution, GSTINs in more than one state, a year end other than 31 March, a financial group, overseas units, another regulator, UPSI, a GIFT IFSC unit, sub-KUA, cloud services, rule 237 reporting, significant data fiduciary, shared price data, public advice, and in-house back office (normally **No**: Ledgerline is a vendor's back office).
- **QSB and specified stockbroker.** The exchanges publish these lists. Choose **Was a QSB** with the date it stopped: the QSB obligations continue for three financial years after that year.

Each answer is **Yes**, **No** or **Not answered**. Not answered means not sure: the duties that depend on it show as **Not sure** with what to answer, and are not chased. Where Ledgerline can tell (a CDSL connection, clients with API access, a mutual fund distributor registration), it says so next to the answer.

**What changes.** As you change an answer, the **What changes if you save** panel lists the calendar duties, self-inspection tests and screens that switch on or off. **Save** shows it again before saving. Every save goes to the audit trail with what changed and what it switched.

**Screens.** A screen of a module the firm does not have is hidden from the menu (for example the depository screens when the firm is no DP, MTF without MTF, eDIS and CDSL transactions without CDSL). Nothing is deleted. An unknown answer shows the screen. An Admin can tick **Show all modules in my menu** to see them all.

## firm-modules-numbers
title: Client numbers, CSCRF category, surveillance and glitch rules
screen: firmmodules
roles: Compliance officer, Admin
ask: how many active clients; active client meaning; registered clients; clients trading volume for cscrf; cscrf category worked out; cscrf mismatch; exempt from cscrf; exempted declaration nse; automated surveillance 2000 active ucc; technical glitch framework 10000 clients; ibt clients count; count clients again; override client count
related: firm-modules, firm-profile

Ledgerline counts the firm's clients from its own data once a day (and when you press **Count again**):
- **Registered clients:** trading accounts past onboarding. Closed accounts, applications still in KYC or UCC upload and DP-only accounts are left out.
- **Active clients:** registered clients who traded in any exchange segment in the last 12 months (NSE's meaning), from the exchange trade files and the last trade date migrated with each client.
- **Clients' trading volume:** the value of clients' exchange trades in the last full financial year (quantity × price; options at their premium). The firm's own codes are left out.
- **Internet or mobile trading clients:** Ledgerline cannot count these, because the trading platform holds the log-ins. Enter the number.

When Ledgerline's trades start inside the period, the count is marked **At least**: it cannot say No from it. Type your own figure (from the exchange or the trading platform) with a note; it is used instead, with your name and the time, until you clear it.

**What the numbers decide:**
- **CSCRF category** (SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2025/60): the higher of the category by registered clients and by clients' trading volume. Your answer wins; if the numbers say otherwise, the screen warns you. A firm below 1,000 clients and ₹1,000 crore is exempt and gets the duty to file the **Exempted** declaration on the NSE member portal by 31 July.
- **Automated surveillance:** needed with 2,000 or more active clients (NSE/INVG/71818).
- **Technical glitch framework:** for internet or mobile trading with more than 10,000 registered clients.

## firm-modules-conditions
title: The conditions duties, tests and screens read
screen: firmmodules
roles: Compliance officer, Admin
ask: applies condition; duty condition list; what does pcm mean; xseg condition; selfat condition; why does this duty not apply; condition vocabulary; screen gating table; which self inspection tests depend on modules
related: firm-modules, circular-applies

Each calendar duty, self-inspection test and gated screen names the conditions it needs. All must hold; `a|b` means either; `no:x` means x does not hold. The list at the foot of Settings → Firm modules shows every condition with how it stands for the firm and why.

- Exchanges and clearing: `broker`, `member:NSE`, `seg:CM` (also FO, CD, COM, SLB), `xseg:NSE-CM` (any exchange segment), `derivatives`, `clearing`, `selfat:NCL` (also ICCL, MCXCCL, NCCL, MCCIL), `pcm`, `cmchange` (changed clearing member in the last 30 days).
- Size: `clients:active>=2000`, `ibt:10k`, `specified`, `qsb`, `qsb:ex`, `cscrf:<categories>`.
- Products: `mtf`, `algo`, `ibt`, `slb`, `dma`, `sor`, `api`, `epi:funds`, `mfos`, `tpp`, `inst`, `prop:only`, `clientbg`, `hedge`, `sgtcm`, `buyback`, `corpbond:prop`, `underwrite`.
- Depository: `dp`, `cdsl`, `nsdl`, `dp:saral`, `dp:escrow`, `dp:fax`, `dp:einstr`, `dp:bankbo`, `dp:mfredeem`, `dp:accredited`, `dp:ipopledge`, `dp:nilbal`, `dp:dropbox`, `dp:arbitration`.
- Firm: `const:proprietor`, `const:partnership`, `const:llp`, `const:company`, `gstin:multi`, `fy:nonmarch`, `fingroup`, `overseas`, `otherreg`, `upsi`, `gift`, `subkua`, `cloud`, `rule237`, `sdf`, `pricedata`, `ra:public`, `bo:inhouse`, and `nri`, `ap`, `branch`, `ras:monthly`, `ra`, `ia`, `pms`.

A self-inspection test whose condition is **No** is skipped as not applicable; when the answer is unknown it runs as before.
