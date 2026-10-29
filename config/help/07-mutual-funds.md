Ledgy help articles: mutual fund distribution (orders, SIPs, mandates, units, commission and the platforms).

# Mutual funds

## mf-routes
title: Offer mutual funds: Regular plans, Direct plans or both
screen: ipo
roles: Compliance officer, Admin
ask: offer mutual funds; mutual fund distribution; regular plan; direct plan; regular or direct; arn; amfi registration; euin; nism v-a; execution only platform; eop; eop exemption; mf route; switch on mutual funds; mf settings
related: mf-platforms, mf-register-client, mf-orders

A firm can offer mutual funds in two ways, and may do both:

- **Regular plans**, as a distributor: the firm's AMFI registration (ARN), an **EUIN** for each employee who deals with investors (NISM Series V-A), and empanelment with each fund house. The firm earns trail commission only.
- **Direct plans**, execution only: SEBI's execution-only platform framework (circular SEBI/HO/IMD/IMD-PoD-1/P/CIR/2023/86). A stockbroker's platform for its own broking clients is exempt from registering (para 1.2); the compliance officer confirms this applies to the firm and records it.

1. In **MF settings** (Compliance officer), choose the plans offered, enter the ARN and each employee's EUIN, and for Direct plans record the basis.
2. Enable the platforms (BSE StAR MF, NSE MF Invest) and set the trail rate the firm expects.

**One route per family.** The first order or SIP of an investor fixes the route (Regular or Direct) for the whole family by PAN: the accounts linked under one client portal login and investors who name the same family head. A Direct plan is then refused for a Regular family, and the other way round. Only compliance can change a family's route (**Investor profile → Change**), with the reason, once the existing units are dealt with.

## mf-platforms
title: Connect BSE StAR MF and NSE MF Invest
screen: connections
roles: Admin
ask: bse star mf; star mf api; nse mf invest; nse invest api; nmf ii; mf platform login; mutual fund api; mf api key; member license key; api secret; pass key; mfapi; bse star mf demo; whitelist ip for nse invest
related: mf-routes, mf-register-client, connections

Each platform has its own connection in [[connections|Connections]] (Admin). The logins stay there, encrypted; nothing else holds them.

- **BSE StAR MF**: the member ID, user ID and password BSE gives after membership, and a pass key of the firm's choice. BSE gives the live order entry address only after the demo and activation: enter it on the connection.
- **NSE MF Invest**: email NSE with the member code, the Member Desk login and up to 4 static IP addresses of this server; NSE replies with the **API secret** and **API member license key**. Choose UAT until NSE switches the firm live.

What goes through the API and what is done on the platform's own site:

- BSE: purchases, redemptions, XSIP registration and cancellation go through the API. Client registration, switches and mandates are done on BSE's site and recorded here.
- NSE: client registration, purchases, redemptions, switches, order cancellation and XSIP registration go through the API. XSIP cancellation and pauses, and mandates, are done on NSE's site and recorded here.
- Order status, allotment and mandate status come from the platforms' report files (see "Load the platforms' and RTAs' files").

Switch on **Check orders during the day** on a connection for Ledgerline to ask the platform every 30 minutes for what its API offers.

## mf-register-client
title: Register a client for mutual funds
screen: ipo
roles: Ops maker, Ops checker
ask: register client for mutual fund; mf ucc; mf client registration; client not registered on nse invest; fatca; fatca declaration; mf nomination; nominee for folio; opt out of nomination; kyc on hold mutual fund; mf profile; 183 column
related: mf-orders, nomination, kra-status

Before any order, an investor needs:

1. **KYC validated at the KRA.** A client whose KRA status is **On hold** or **Rejected** cannot transact at all.
2. **FATCA self-certification** recorded in the MF profile (or declared by the client in the portal).
3. **Nomination for new folios.** From 1 September 2026 a new single-holder folio needs up to 3 nominees, with shares adding to 100%, or the investor's opt-out.
4. The rest of the **MF profile**: units in demat or a folio, the platform's tax status and occupation codes, gender, address, state code and PIN code.
5. **Registration on each platform.** On the MF orders screen's **Investor profile**, **Send registration** sends NSE MF Invest's 183-column client registration; NSE then sends the investor a link to authorise it. Once authorised, **Record status → Registered**. For BSE StAR MF, register the client on BSE's site and record it here.

## mf-orders
title: Place a mutual fund order
screen: ipo
roles: Ops maker, Ops checker
ask: place mf order; buy mutual fund; mutual fund purchase; redeem mutual fund; mf redemption; switch mutual fund; lumpsum; mf cut-off; nav cut-off; mf payment link; pay for mutual fund; euin on order; cancel mf order; mf allotment; units allotted; mf-orders file
related: mf-register-client, mf-sips, mf-files, ipo

On **MF orders**, **Place order**: the investor, the platform, purchase, redemption or switch, the scheme and the amount (or units). Regular plan orders carry your EUIN; Direct plan and portal orders carry none.

- **Payment never passes through the firm.** The platform sends the investor a link to authorise the order and pay the clearing corporation (NSE Clearing, or Indian Clearing Corporation for BSE), or the order is paid by the investor's mandate. Trading account balances are never used.
- **Cut-offs.** NSE MF Invest takes liquid funds until 1 pm and others until 2:30 pm (AMFI's are 1:30 pm and 3 pm). An order after the cut-off gets the next business day's NAV and is marked so.
- **Demat redemptions and switches go by units.** Redemption proceeds are paid by the fund house to the investor's registered bank account.
- **Cancel** works on NSE MF Invest while the order awaits payment. BSE no longer cancels lumpsum orders.
- When the platform's or RTA's statement shows the units, record the **allotment** (or load the statement file): the units ledger moves.

Orders in the platform's own order file still load through MF-ORDERS on [[files|Other uploads & migration]]; a row for an order placed here updates it.

## mf-sips
title: Start, pause or cancel an SIP
screen: ipo
roles: Ops maker, Ops checker
ask: start sip; xsip; register sip; sip mandate; pause sip; sip pause; cancel sip; stop sip; sip failed; sip bounce; sip instalment; sip reminder; sip debit date; three failed sip
related: mf-mandates, mf-orders

SIPs are XSIPs: each instalment is debited from the investor's own bank account by a mandate in the clearing corporation's favour.

1. **Start SIP**: the investor, platform, scheme, amount, monthly, quarterly or weekly, the first instalment (the 1st to the 28th) and the mandate. The first instalment can be 2 working days after the mandate is approved; with the mandate still being processed, 5 days later still for an e-mandate and 20 for a physical NACH.
2. The bank is sent the debit the working day before each instalment. Clients are reminded a few days ahead (MF settings).
3. **Pause** up to twice in an SIP's life: pause it on the platform's site, then record it.
4. **Cancel** until two working days before an instalment. BSE XSIPs are cancelled through the API; NSE XSIPs on NSE's site, then recorded here.
5. Record each **instalment** from the platform's or bank's report. Three failures in a row cancel the SIP, and the client is told.

## mf-mandates
title: Record a mutual fund mandate
screen: ipo
roles: Ops maker, Ops checker
ask: mf mandate; nach mandate; e-mandate; emandate; physical nach; umrn; mandate limit; mandate pending; mandate rejected; mandate status report; one time mandate; otm
related: mf-sips

A mandate is always on the investor's own registered bank account and in favour of the platform's clearing corporation, never the firm. It allows at most ₹1 crore a debit.

1. Register the mandate on the platform (the investor authorises an e-mandate online).
2. On **MF mandates**, **Record mandate** with the platform's mandate number, its limit and type.
3. An e-mandate takes up to 5 working days to register, a physical NACH up to 20; a mandate pending longer is flagged **Overdue**.
4. **Update** the status (and UMRN) as the platform or bank reports it, or load the platform's mandate status report. A mandate with an active SIP cannot be cancelled until the SIP is.

## mf-nav
title: Load NAVs and the platforms' scheme codes
screen: ipo
roles: Ops maker, Ops checker
ask: nav; nav file; amfi nav; navall; scheme master; mf scheme not found; scheme code; load nav; nav not updated; mutual fund prices
related: mf-files, mf-orders

- **NAVs**: with **Fetch AMFI's NAV file every evening** on in MF settings, Ledgerline fetches AMFI's daily file after 9:30 pm (and the next morning if that failed). If the fetch fails, the reason shows in MF settings: **Upload NAV file** with a copy saved from amfiindia.com.
- **Scheme codes**: each platform has its own code for each scheme. Load the platform's scheme file as **Platform scheme master** (MF settings → **Load a file**), mapping the code and ISIN columns, and NSE's AMC code column for SIPs. A scheme without a code on a platform can't be ordered there.

## mf-files
title: Load the platforms' and RTAs' files (KFintech, CAMS)
screen: ipo
roles: Ops maker, Ops checker
ask: kfintech file; karvy mailback; mfsd; mfsd201; cams mailback; cams wbr; rta feed; rta file; allotment statement; redemption statement; order status report; dbf file; column mapping; load mf file
related: mf-nav, mf-commission, mf-orders

The platforms' reports and the RTAs' mailback files come in layouts given to members and distributors, so they load through a **column mapping** the firm sets once:

1. MF settings → **Load a file**: choose the kind (order status, allotment or redemption statement, mandate status, RTA transaction feed, RTA brokerage statement, platform scheme master) and the file.
2. Type each field's column heading as it appears in the file, and **save the mapping** under a name (for example "KFintech MFSD201").
3. Next time choose the saved mapping.

Files sent as DBF are saved as CSV first. Rows that can't be applied (an unknown order or folio) are listed after loading. RTA rows already loaded are skipped.

## mf-commission
title: Reconcile trail commission
screen: ipo
roles: Ops checker, Compliance officer
ask: trail commission; mf commission; mf brokerage; amc brokerage; rta brokerage statement; commission not received; trail short; accrue commission; distributor commission
related: mf-files, mf-routes

On **MF commission**:

1. **Work out accruals** for the month: Regular plan units held at the month's end × the month-end NAV × the trail rate in MF settings ÷ 12.
2. **Load brokerage statement** from KFintech and CAMS with a saved mapping.
3. Each scheme shows **Matched**, **Short**, **Excess**, **Not received** or **Not expected**. Differences over 2% are flagged. Rows without a recognisable scheme are shown as not matched.

## mf-portal
title: How clients invest in mutual funds from the portal
screen: ipo
roles: Ops maker, Ops checker
ask: client buy mutual fund online; portal mutual fund; client start sip online; client redeem online; execution only order; client fatca online
related: mf-orders, mf-register-client

Once the firm has registered a client on a platform, the client portal's **Mutual funds** page lets them buy, redeem and start SIPs on their own mandate, cancel an SIP (where the platform's API allows), and record their FATCA declaration and folio nomination. Portal orders are execution only: no EUIN. The investor's family route still applies, and payment is by the platform's link to the clearing corporation.
