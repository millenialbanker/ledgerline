Ledgy help articles: the rest of SEBI's framework for unpaid securities (policy, extensions, margin, NMASS flags) and the checks on how clients' pledged securities are used.

# Unpaid securities and collateral

## cuspa-policy
title: The policy on unpaid securities
screen: settlement
roles: Compliance officer, Admin, Ops checker, Risk manager
ask: unpaid securities policy; cuspa policy; written policy on unpaid securities; send cuspa policy to clients; policy on website; communicate policy to clients; rms policy unpaid
related: cuspa, cuspa-extension

SEBI's framework (revised master circular paras 46.3 and 46.4) asks the firm to write down its policy on unpaid securities and send it to every client before it applies. The policy says when and how the pledge is invoked or released, how and when the shares are sold, and the most time a client has to pay (at most 5 trading days after the pay-out).

On [[settlement|Settlement]] → **Unpaid securities** → **Policy**:

1. **Write it.** The draft is made from the firm's settings. Edit it to the firm's practice.
2. **Record it** (compliance officer). Give the date it applies from and its address on the firm's website. Each save is a new version. The payment period it states is taken from Settings; if Settings change later, record a new version.
3. **Publish it** on the firm's website at that address.
4. **Send it to clients** before the date it applies. Each active client is emailed and texted (template **cuspa.policy**) with the address. Ledgerline records who was sent which version. The daily CUSPA review sends it to each new client.

The self-inspection test **cuspa_policy** fails when no policy is recorded, the website address is missing, it was sent after the date it applies, its payment period differs from Settings, or an active client has not been sent the current version.

## cuspa-extension
title: Extending a CUSPA pledge when the shares cannot be sold
screen: settlement
roles: Ops checker, Risk manager
ask: cuspa extension; extend cuspa pledge; lower circuit unpaid shares; suspended shares cuspa; cuspa01; cuspa reason code; extension by 6 pm; unpaid shares cannot be sold
related: cuspa, cuspa-policy

From 3 January 2027 (SEBI revised master circular paras 46.12 to 46.14; NSE circular 41/2026, guideline 10), when unpaid shares cannot be sold within the 5 trading days (for example a lower circuit after the sell order was placed, or trading suspended or halted), the firm may ask the depository to extend the pledge by up to one calendar week.

1. On the line's **last day to invoke**, before **6 PM**, select the line on [[settlement|Settlement]] → **Unpaid securities** and click **Extend** (Ops checker or Risk manager).
2. Choose the depository's **reason code** (CUSPA01 to CUSPA06, as listed in NSE circular 41/2026) and say why in words.
3. Ledgerline records it, moves the last day to invoke to the last trading day of the week asked for, and emails and texts the client (template **cuspa.extension**). The depository then releases at the end of the next trading day.
4. Give the request on the depository's system the same day: it is on the day's **Depository list** ("Extend (by 6 PM)"), and a work item reminds you.
5. If the reason lasts, extend again on the last day of the extension.

Ledgerline refuses an extension before 3 January 2027, on any day other than the last day to invoke, after 6 PM, or when the client is not in debit. The daily review puts the last-day lines in the work queue with the 6 PM deadline. The self-inspection test **cuspa_extension** checks each extension was asked in time, with a code, and told to the client.

## cuspa-margin
title: Unpaid securities and margin
screen: settlement
roles: Risk manager, Admin
ask: cuspa margin; count cuspa as margin; unpaid securities margin reporting; exposure on cuspa shares; cuspa collateral
related: cuspa, peak-margin

Unpaid securities in the CUSPA may be counted towards the client's margin reported to the clearing corporation, but the client may never get further exposure on them (SEBI revised master circular para 46.5).

- **Exposure:** Ledgerline never gives any. CUSPA shares are not collateral in RMS, and the front-end holding file lists them as holdings (so they can be sold) but never as collateral.
- **Margin reporting (optional):** in Settings → Risk and settlement, tick **Count unpaid securities in the CUSPA as margin collected**. Margin reporting then counts them, valued at the previous close less the haircut, as non-cash collateral within the client's collateral. Leave it off to report without them.

## nmass-unpaid-flags
title: Reporting unpaid and MTF purchases to NSE Clearing (NMASS)
screen: periodic
roles: Ops maker, Ops checker
ask: unpaid mtf file; unpaidmtf; nmass unpaid flag; cuspa pay-out reporting; ctm flag; report unpaid securities to nse clearing; 10 am cut-off cuspa
related: cuspa, periodic-work

So that NSE Clearing pays out unpaid purchases to the client's demat account with the pledge to the CUSPA (and MTF purchases with the MTF pledge), the member flags them per settlement on NMASS (NCL/CMPT/73996 items 6.2.1 and 7.7).

The periodic task **CUSPA and MTF pay-out reporting** writes one file per settlement: the normal rolling settlement and trade-for-trade (shares in the BE or BZ series). It leaves out clients trading with UPI blocks and the firm's own code. Nothing carries forward: each settlement is flagged on its own.

Upload the files on NMASS in the window NSE Clearing gives (the trade day's afternoon and evening, or the settlement morning), and by **10:00 AM on the settlement day** at the latest. The work item says so, and warns when the cut-off has already passed.

## collateral-checks
title: Checks on clients' collateral, re-pledge and pool accounts
screen: selfinspect
roles: Compliance officer, Risk manager, Ops checker
ask: repledge used for another client; client securities used for proprietary obligations; allocation covers margin; collateral shortfall per client; pool account securities; client securities in pool account; cuspa pledged to bank; cuspa nbfc
related: self-inspection-tests, cuspa, pledge

The self-inspection runs these checks every night:

- **client_margin_cover:** from NSE Clearing's client margins (margins API), each client's allocated cash plus re-pledged non-cash collateral covers the client's margin in each segment (SEBI master circular paras 43.13 and 43.14). A short client makes the clearing corporation block the firm's own collateral.
- **repledge_own_client:** NSE Clearing's re-pledge status file shows no client with more of a security than the client pledged to the firm, and nothing under the firm's own code or a code that is no client (para 42.12.5).
- **client_secs_pool:** the firm's pool accounts hold nothing two statements of holding in a row, and the client unpaid securities account holds nothing (para 42.10). Tag the accounts in Settings → Firm demat accounts and load CDSL's statement of holding each day.
- **cuspa_no_lender:** CUSPA securities are never pledged to banks or NBFCs: the client unpaid securities account has nothing pledged out, and no pledge to a lender or re-pledge carries a CUSPA pledge (para 46.11).
- **pledge_invoke_notice** and **pledge_undertaking:** see **Invoke a client's pledge**.
