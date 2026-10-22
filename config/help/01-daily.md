Ledgy help articles: the trading day. Format is described in internal/help/help.go.

# The trading day

## daily-routine
title: Run the trading day, start to finish
screen: dashboard
roles: Ops maker, Ops checker
ask: daily routine; what do i do every day; order of daily tasks; daily checklist; day process; steps for the day; start of day to end of day
related: load-files, run-recon, run-billing, contract-notes, eod-close, autopilot

The usual order. With [[autopilot|Autopilot]] on, most of this runs by itself and you only work the queue.

1. **Before the market opens:** Autopilot's start-of-day stage blocks clients in debit and builds the BOD limit and holdings files for the trading system ([[rms|RMS & limits]]).
2. **Load the day's files** on [[files|Files]]: exchange trade files, OMS trade book, clearing obligations, bank statement, market data. Drop them all in the inbox; Ledgerline recognises each one.
3. **Reconcile** on [[recon|Trade reconciliation]] and clear every break.
4. **Bill** on [[billing|Billing]].
5. **Settle** on [[settlement|Settlement]]: obligations, short deliveries, CUSPA.
6. **Send contract notes** on [[notes|Contract notes]] within 24 hours.
7. **Risk:** clear margin breaches on [[rms|RMS & limits]] and payouts on [[money|Receipts & payouts]].
8. **Close the day** with **Run EOD close** on the [[dashboard|Dashboard]].

Ask me "why can't I close the day?" at any point and I'll check what's still open.

## load-files
title: Load the day's files
screen: files
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: upload file; load trade file; import exchange file; where do i upload files; upload bank statement; load bhavcopy; add files; load oms file
related: file-rejected, run-recon

1. Open [[files|Files]].
2. Drop every file for the day into the inbox, or choose them, then click **Load all**.
3. Ledgerline recognises each file by its columns and loads masters first (clients, market data) and trades after.
4. Each file shows its result: lines applied, or why it was rejected.

- A file loaded twice is recognised by its SHA-256 and not applied again.
- Every file is kept exactly as received, encrypted, for audit.
- If a layout isn't recognised, it's a format Ledgerline doesn't know yet. Send a sample to whoever maintains your file layouts; layouts are configuration, not code.

## file-rejected
title: Fix a rejected file
screen: files
roles: Ops maker, Ops checker
ask: file rejected; file failed to load; upload error; bad line in file; file not accepted; file not recognised; unknown file type; columns not recognised
related: load-files

A file with **any** bad line is rejected whole, so nothing is half-applied.

1. On [[files|Files]], open the rejected file. Each bad line is listed with its line number and the reason.
2. Common reasons:
   - **Unknown client code:** the client isn't in the master yet. Load the client master first, or add the client, then load the file again.
   - **Bad date or number:** the file was edited in Excel, which changes dates and drops leading zeros. Use the original file from the exchange or bank.
   - **Wrong file type:** a file for a different segment or date. Check the name and date.
   - **Not recognised:** the columns match no known layout. It may be a new exchange format.
3. Fix the cause and load the file again. A corrected file has a different SHA-256, so it is accepted as new.

Don't edit exchange files by hand to make them load. Fix the master data instead, so the record matches what the exchange sent.

## run-recon
title: Run trade reconciliation
screen: recon
roles: Ops maker, Ops checker
ask: reconcile trades; run recon; match exchange and oms; trade matching; reconciliation not run
check: breaks
related: recon-breaks, load-files

1. Make sure **every** exchange and segment file and the OMS trade book for the day are loaded on [[files|Files]]. A missing file makes every trade in it look like a break.
2. Open [[recon|Trade reconciliation]] and click **Run reconciliation**.
3. Matched trades need nothing. Each difference becomes a break with steps to check and fix it.

The exchange record is the source of truth: billing always uses the exchange's trades.

## recon-breaks
title: Clear reconciliation breaks
screen: recon
roles: Ops maker, Ops checker
ask: clear breaks; resolve break; break queue; open breaks; how to resolve mismatch; recon exceptions; break not clearing
check: breaks
related: break-ucc-mismatch, break-qty-mismatch, break-missing-in-oms, break-algo-id-missing, ccm

1. Open [[recon|Trade reconciliation]] and click a break.
2. The break shows **what to check** and, for each possible finding, **how to put it right**, with the trade's details filled in.
3. Do the checks, pick the finding that's true, follow its steps.
4. Click **Resolve**, use the suggested note (fill in the blanks), and **Mark resolved**.

Breaks with no money effect (trade-number differences, fills the exchange rejected) are cleared by Autopilot. The day can't close cleanly while a break is open.

## ccm
title: Do a client code modification (CCM)
screen: recon
roles: Ops maker, Ops checker
ask: client code modification; ccm; trade booked under another client; trade booked to wrong client; trade in wrong client account; change client code of a trade; move trade to another client; trade punched in wrong client; shift trade to correct client
related: break-ucc-mismatch, run-billing, contract-notes

A CCM moves an executed trade from one client to another. It's allowed only for genuine punching errors, before the exchange's cut-off, and every one is reported, so keep them rare.

1. Get the evidence first: order log, call recording, or the client's written instruction.
2. Make the change on the **exchange's** system before its cut-off.
3. Here, open the break on [[recon|Trade reconciliation]] and click **Client code modification → (the right client)**. It moves the trade and closes the break.
4. [[billing|Billing]] → **Rebill changed trades**.
5. [[notes|Contract notes]] → **Generate notes**, then **Send pending**.

The count of client code changes today is shown at the top of Trade reconciliation. Surveillance flags clients with frequent changes.

## run-billing
title: Run billing and rebill changed trades
screen: billing
roles: Ops maker, Ops checker
ask: run billing; generate bills; bill not generated; rebill; bills wrong; charges not posted; billing after correction
related: correct-bill, change-brokerage, contract-notes

1. Clear the reconciliation breaks first. Billing uses the exchange's trades.
2. Open [[billing|Billing]] and click **Run billing** for the date.
3. If trades, client codes or brokerage plans changed after billing, click **Rebill changed trades**. Only the affected clients are reversed and billed again.

Statutory charges (STT, exchange, SEBI, stamp duty, GST) come from the charge schedule; brokerage comes from each client's plan.

## correct-bill
title: Correct one client's bill
screen: billing
roles: Ops checker
ask: correct a bill; wrong brokerage charged; client charged wrong brokerage; revise contract note; bill correction; refund excess brokerage; rebill one client
related: change-brokerage, run-billing, contract-notes

Use this when one client's bill for a past day is wrong.

1. Open [[billing|Billing]], find the client's row and click **Correct** (or **Correct a client's bill**).
2. Choose what was wrong:
   - **Brokerage rate:** enter the right rate, for that date only or from that date onwards.
   - **Trades already corrected:** after a CCM or trade fix, rebill from the corrected trades.
3. Give the reason. It's printed on the revised contract note.
4. Only that client's bill is reversed and posted again, and a **revised contract note** with a new number goes out, marked as replacing the old one.

If you get "the bill did not change", the cause hasn't been fixed yet: change the plan, the trades or the client code first, then correct.

## contract-notes
title: Generate and send contract notes
screen: notes
roles: Ops maker, Ops checker
ask: send contract notes; contract note not sent; generate contract notes; ecn; contract note bounced; resend contract note; client did not get contract note
related: run-billing, change-contact

Contract notes must reach clients within 24 hours of the trade.

1. Bill the day first.
2. On [[notes|Contract notes]], click **Generate notes**, then **Send pending**. Autopilot does both at end of day.
3. **Bounced:** the email address doesn't accept mail. Get the right email through a change request (verified by OTP), then open the note and resend. Autopilot queues bounced notes for the maker with a 24-hour deadline.
4. **Resend one:** open the note and click send again.

Notes for clients with an open client code change are held until the change is settled, so a wrong note never goes out.

## settlement
title: Handle settlement, short deliveries and CUSPA
screen: settlement
roles: Ops maker, Ops checker
ask: settlement obligation; pay in pay out; short delivery; auction; client did not deliver shares; cuspa; unpaid securities; release securities; close out short delivery
related: run-billing, debit-interest

On [[settlement|Settlement]]:

- **Obligations:** what the firm pays in to and receives from the clearing corporation for each settlement, client by client.
- **Short delivery:** a client sold shares they didn't deliver. The exchange auctions them; when the auction result arrives, click **Close out** to post the auction or close-out price difference to the client.
- **CUSPA (client unpaid securities):** shares bought by clients who haven't paid are held in the CUSPA account. When the client pays, click **Release**. Shares unpaid beyond the allowed days are sold as per the rules.

## eod-close
title: Close the day (equity and commodity)
screen: dashboard
roles: Ops checker
ask: close the day; end of day; eod close; run eod; why can't i close the day; day close failed; eod not completing; close day with exceptions; close commodity; mcx close; commodity close; close equity; mcx trades late; mcx closes at night
check: eod
related: previous-day-open, recon-breaks, contract-notes, margin-shortfall, backups

The day closes in two parts, because MCX trades until 23:30 (23:55 from November to March) while NSE and BSE stop in the afternoon:

- **Equity** (NSE and BSE: cash, F&O, currency) in the evening, once their files are in.
- **Commodity** (MCX) after its session, usually the next morning when MCX's files arrive. Commodity trades get their own contract note.

On the [[dashboard|Dashboard]], click **Run EOD close**. Each part shows its own checks, counting only its own trades:

1. Exchange and OMS files loaded ([[files|Files]])
2. Every reconciliation break resolved ([[recon|Trade reconciliation]])
3. Bills posted, and carried futures marked to market ([[billing|Billing]])
4. Contract notes delivered ([[notes|Contract notes]])
5. No unaddressed margin breaches for clients trading there ([[rms|RMS & limits]])
6. Trial balance agrees and client funds are segregated ([[ledger|Ledger]])

Click **Close equity** or **Close commodity**. Each close carries that part's positions to its next trading day and is followed by a backup ([[backups|Backups]]). The day counts as closed once every part that traded is closed; the next day's work waits for both.

If a check fails for a genuine reason, close with a note explaining why; it is kept in the audit trail. Closing needs an **Ops checker**. On Autopilot, equity closes with the end-of-day stage and commodity at 07:00 the next morning.

## previous-day-open
title: Fix "close the previous day first"
screen: dashboard
roles: Ops checker
ask: close first; previous day not closed; open positions carry into; cannot start new day; positions carry
check: eod
related: eod-close

Each day builds on the one before: open positions, holdings and balances carry forward. So a day can't be processed until the previous business day is closed.

1. Set the trade date at the top to the earlier day.
2. Finish it and **Run EOD close** on the [[dashboard|Dashboard]] (with a note if a check can't pass).
3. Move the date forward and carry on.

## period-locked
title: Fix "the books are locked"
screen: settings
roles: Admin
ask: books are locked; period locked; locked up to; cannot post to old date; unlock period; back dated entry refused
related: correct-bill

The books are locked up to a date so closed months can't change. An entry dated in a locked period is refused.

1. Post the correction in the current period instead, if the rules allow (most reversals can be dated today).
2. If it must be back-dated, an **Admin** changes **Books locked up to** (Settings → Period locking) on [[settings|Settings]], posts it, and locks again.

Every lock and unlock is in the audit trail. **Also lock automatically** in Settings (Period locking) keeps the lock a set number of business days behind today, so you don't have to remember.
