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
2. **Load the day's files** on [[xfiles|Files → Exchange files]]: exchange trade files, clearing and depository files, market data. The OMS trade book and bank statement go on [[files|Files → Other uploads]]. Drop them all in; Ledgerline recognises each one.
3. **Reconcile** on [[recon|Trade reconciliation]] and clear every break.
4. **Bill** on [[billing|Billing]].
5. **Settle** on [[settlement|Settlement]]: obligations, short deliveries, CUSPA.
6. **Send contract notes** on [[notes|Contract notes]] within 24 hours.
7. **Risk:** clear margin breaches on [[rms|RMS & limits]] and payouts on [[money|Receipts & payouts]].
8. **Close the day** with **Run EOD close** on the [[dashboard|Dashboard]].

Ask me "why can't I close the day?" at any point and I'll check what's still open.

## where-screens-are
title: Where each screen is
screen: dashboard
roles: Admin, Ops maker, Ops checker, Risk manager, Compliance officer
ask: where did the screen go; menu changed; cannot find screen; where is exchange files; where is scheduled jobs; where is periodic work; where is running account; where is dispatch register; where is charges calculator; new menu
related: daily-routine, periodic-work

Screens that do the same job sit together: the menu shows the group once and the screen opens with a tab for each part.

- **Automation**: Autopilot & work queue, Scheduled jobs, Browser automations.
- **Calendar & circulars**: [[periodic|Periodic work]] and Circulars & duties (the compliance calendar). Regulatory reports shows what falls due in the next month from both.
- **Files**: Exchange files, Clearing data, Other uploads & migration.
- **Clients & onboarding**: Clients, Online applications, Change requests.
- **Client documents**: Client reports, Dispatch register, Client messages, Year-end packs.
- **RMS & margin**: RMS & limits, Live (intraday), Margin reporting.
- **Collateral**: client collateral & pledges, and the firm's clearing deposits.
- **Receipts & payouts**: receipts and payouts, Bank reconciliation, Running account.
- **Brokerage & charges**: Brokerage plans, Charges calculator, Prepaid schemes.

Links and bookmarks to the old screens still open them, in their group.

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

## exchange-files
title: Load or download exchange, clearing and depository files
screen: xfiles
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: exchange file; trade file; ncl file; nse clearing file; cdsl file; soh file; holding file; position file; stt file; stamp duty file; var file; span file; allocation file; segmon file; holding statement; upload file format; file layout; finspot file; mcx file; bse file; bse extranet; provisional trade file; intraday trade file; mbrstd; bhavcopy; udiff; daily file checklist; missing file; expected files; cleared through; clearing corporation; iccl; interoperability; trading member only; not permitted to trade; bse permitted list; order charges; turnover trc; transaction charges; clearing house statement; debarred pan; debr; nome; bulk deals; order log; demat check response
related: load-files, file-rejected, clearing-data, mcx-files, check-file

On [[xfiles|Exchange files]]:

1. **Check a file** or **Load files**: drop the files as downloaded from the exchange, NSE Clearing, MCXCCL or CDSL (zip or gz is fine, several at once). Ledgerline recognises each by its name and header and checks every field against the layout: a changed column, a wrong date or a number that isn't a number is listed by line.
2. **Load files** also loads the ones Ledgerline uses: trade files (NSE Clearing, NSE, MCXCCL) as the day's exchange trades, CDSL statements of holding as client holdings (matched by demat account), the F&O position file as carried positions, and STT/CTT files for reconciliation. A file with any problem is not loaded.
   - **Provisional trade files** (NSE Clearing, NSE and BSE, `…_<date>_P_<n>.csv`, the same layout as the final `_F_` file) feed **live risk** only: never bills, contract notes or the trade reconciliation. Each one adds to the provisional trades of its exchange and segment. When the final `_F_` file of that exchange and segment is loaded, the provisional trades are dropped, and later provisional files for the day are not used, so no trade counts twice.
   - **BSE stamp duty** (`MBRSTD…_OTB_CM_<member>_<DDMMYY>.CSV`): the summary's record count and total must agree with the details, or the file is not loaded; client-wise stamp duty is reconciled on [[ccdata|Clearing data]] against the stamp duty billed on BSE trades (each clearing or exchange stamp duty file is set against its own exchange's bills).
   - **Bhavcopy** in SEBI's common format (`BhavCopy_NSE_CM_…`, `BhavCopy_BSE_CM_…`, `BhavCopy_BSE_FO_…`): closing, last and settlement prices go into the day's market data for that exchange and segment. A BSE row without an ISIN or symbol takes them from the BSE scrip and series mapping by scrip code.
   - **BSE member reports**: UCCs not permitted to trade (EQX cash, EQD derivatives; the latest run of the day replaces the earlier one; a work item per client says what to fix, closed when the client drops off the list); UCCs permitted to trade (compared with active clients: exceptions on Clearing data); order charges and turnover/transaction charges (the transaction charges are reconciled with those billed on BSE trades, and a difference over ₹1 opens a work item for a checker); the clearing house statement (`VA<DDMM>`, kept as BSE clearing ledger entries with a reminder to reconcile within 15 days); PANs debarred by SEBI (`DEBR`: added to the debarred list, active clients blocked with the reason "SEBI debarred (BSE DEBR report)"; a scrip-specific order only flags the client for the compliance officer); mobile and email exceptions (`NOME`, one work item); bulk deals (`BULK`, low-priority alerts) and the order log (`BO`, order-to-trade ratio by client and terminal); and BSE's demat check responses to the UCC upload (kept with the client). The bulk deal, order log and demat response layouts are provisional.
3. Under **Sent**, choose the date and **Download**: the client allocation file and API message (NSE Clearing, MCXCCL), the client holding statement for NSE/BSE, the 65-column client funds report (SEGMON) and the client collateral (CC01) files, named as the receiver expects.

**Daily file checklist**: the top of Exchange files lists the day's expected files with their usual times (for example BSE's final trade file about 17:00, its cash bhavcopy about 16:30 and F&O bhavcopy about 17:20, the MBRSTD stamp duty file about 15:00 the next day) and marks each Received, Written, Due or Missing. Which files appear follows **Cleared through** in [[settings|Settings]] (Admin): choose for each exchange segment the clearing corporation it is cleared at under SEBI interoperability (NSE and BSE: NSE Clearing or ICCL; MSE: MCCIL, NSE Clearing or ICCL; MCX: MCXCCL; NCDEX: NCCL), **Another clearing member** (the firm is only a trading member there; give the member's code and name) or **Not traded**. The exchange's own files appear whenever the firm trades on that exchange; a clearing corporation's files appear only when some segment is cleared there; a segment cleared by another clearing member has no clearing-corporation files, since they come from that member, except the files a clearing corporation sends the trading member itself (MCXCCL's obligation, margin, peak margin, penalty, collateral and allocation files: see [[xfiles|MCX and MCXCCL files]]). Every change is in the audit trail. The writers of outgoing files are not affected.

The firm's PAN is taken from the GSTIN unless set in Settings; set the clearing member's PAN there if another member clears for the firm. **Download** on the SEGMON file and on the holding statement opens their own panel first: the SEGMON check and NSE Clearing's response, and the holding statement's submission to NSE by API.

## mcx-files
title: MCX and MCXCCL files
screen: xfiles
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: mcx file; mcxccl file; mcx nobl; mcx pnobl; mcx obligation file; mcx_mrg; mcx margin file; mcx peak margin; peakmargin; mcx ncmr; margin shortfall penalty mcx; mcx penalty file; mcx_cd; mcx collateral file; mcx_cd ucc; web allocation deallocation; mcx allocation report; mcx product master; productmaster; contract master commodity; mcx lot size; mcx bhavcopy; mcx cp master; mcx ord file; mcx online orders; daily index file; eodsar; intrasar; mcx debit credit note; ctt mcxccl; stamp mcxccl; position mcxccl; commodity files
related: exchange-files, clearing-data, check-file, otr-alerts

Ledgerline reads MCX's and MCXCCL's files as MCXCCL's **File Formats** (Version 1) and MCX circular **MCX/TECH/394/2023** (File Formats 1.2) lay them out, and MCXCCL's UDiFF (SEBI common format) files. Drop them on [[xfiles|Exchange files]] as downloaded (zip or gz is fine). A trading member cleared through another clearing member still receives the trading-member files; a clearing member's own NOBL, MCX_MRG and peak margin files share the names and are told apart by their lines.

- **MCX_ProductMaster.csv**: the commodity contract master (tradable lot, quotation quantity and unit, tick, expiry, delivery unit and mode). Its live contracts go into the day's MCX contracts in the market data, whose lots commodity billing uses. About 17,000 lines load in a second or two.
- **Bhavcopy_MCX_CO_…** (UDiFF): closing and settlement prices of the MCX contracts (market data: valuation, MTM, margins). Its market lot of 1 does not replace the product master's lot.
- **MCX_NOBL_…** (and the provisional **MCX_PNOBL_…**): the day's commodity obligation. On [[ccdata|Clearing data]] each client's MTM, premium and exercise or assignment is set against Ledgerline's own MCX obligation from the bills; a difference over ₹1 is counted as a break.
- **MCX_MRG_…**, **Margin_MCXCCL_CO_…** (UDiFF) and **MCX_PeakMargin…_nn**: the margin MCXCCL charged and its peak snapshots. Live RMS uses MCXCCL's figure (the latest snapshot, or the end-of-day margin) when it is higher than Ledgerline's; Margin reporting shows MCXCCL's margin and highest peak beside Ledgerline's; Clearing data compares them.
- **MCX_NCMR_…** and **Penalty_MCXCCL_CO_…**: the shortfall and penalty MCXCCL levied. On Margin reporting the client's penalty is then MCXCCL's (marked as levied by MCXCCL). It is the firm's: it reaches the client only by the pass-through rules (a dishonoured cheque or a disclosed hedge break, with evidence, approved by a second person). Ledgerline never debits it by itself.
- **MCX_WebAllocationDeallocation…** (MCX's own spelling, *Deallcoation*, is accepted too): each client's commodity allocation at end of day becomes the client's COM level on Clearing allocation, and Clearing data sets it, with the client collateral file **MCX_CD_…_UCC**, against Ledgerline's allocation. The member collateral file **MCX_CD_…** is kept. Commodity levels are never sent to NSE Clearing.
- **CTT_MCXCCL_…** and **Stamp_MCXCCL_…** (UDiFF): reconciled with the CTT and stamp duty billed on MCX, as NSE Clearing's STT and stamp files are (only the per client and contract lines count, so totals are not added twice).
- **Position_MCXCCL_…** (UDiFF): commodity positions carried into the next day.
- **MCX_<ddmmyyyy>ORD** (`.txt` from the terminal, or `_n.csv`): the MCX order log; order-to-trade ratios per firm, client and terminal (one line per order, so changes are not counted separately).
- **EODSAR, INTRASAR, debit/credit notes, the CP master and DailyIndexFile**: kept; short allocations and debit/credit notes are listed on Clearing data.

The daily file checklist lists these files at their usual times for a firm with an MCX member code. MCX's NCMR and daily index files have a title row whose text the circulars do not give, so only its number of columns is checked.

## check-file
title: Check real exchange files on your own computer
screen: xfiles
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: check file without uploading; check real files locally; ledgerctl check-file; masked summary; file does not load; why is my file not recognised; share file problem with support; field count error; trailing comma
related: exchange-files, mcx-files, file-rejected

To see how Ledgerline reads a file without loading it or sending it anywhere, run on the computer that holds the files:

    ledgerctl check-file ~/Downloads/MCX/*

(a folder is checked file by file; zip and gz files are opened in memory). For each file it prints the layout it is recognised as, the rows by record type, the number of fields on the lines against the layout, the header row against the layout's column names, the problems by column and kind with the first line numbers, and whether the file writes back byte for byte. **No field value is printed**: quoted values are masked (letters A, digits 9), every other digit is shown as 9 and file names keep only their letters, so the summary can be pasted to support. Add `-json` for the same summary as JSON. It ends with exit status 3 when a file is not recognised or has problems.

## segmon-report
title: Client funds and collateral report (SEGMON): check, download, response
screen: xfiles
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: segmon; segmon check; client funds report; 65 column file; peak ledger balance; peak clear balance; credit in lieu of epi; cash placed with ncl; mtf indicator; non mtf; uncleared receipts; settlement amount column; segmon error file; segmon response; segmon rejected; segregation and monitoring of collateral
related: exchange-files, client-collateral, holding-api

On [[xfiles|Exchange files]], under **Sent**, choose the date and **Download** on *Client funds and collateral report*. Ledgerline first runs the **SEGMON check** and shows its errors and warnings; the file is made only when there are no errors.

What Ledgerline reports for each client (one line per segment with an allocation; the ledger, received and retained amounts on the client's first line):

- **Ledger balance (A)** and **clear balance (B)**: the client ledger at the end of the day; B leaves out cheques credited but not yet cleared, which are in **Uncleared receipts**.
- **Peak clear balance (C)**: the highest clear balance during the day, worked out from the start-of-day balance and the day's postings in the order they were made. A cheque counts from the day it clears. The day's settlement bills do not lower the peak.
- **Credit in lieu of early pay-in**: the full value of shares the client paid in early through CDSL that day (their sale price, else the closing price).
- **Cash and securities placed with NSE Clearing**: the client's end-of-day allocation to the segment, the same as the allocation file and CC01. Securities pledged and not allocated are shown as retained by the trading member. G-secs and T-bills are reported in their own columns.
- **MTF**: a client with margin trading has a second cash-segment line marked **MTF** with the margin trading ledger; the client's cash margin on open MTF positions is in **Cash collateral for MTF positions** and in the **NON MTF** line's balances, not in the MTF line.
- **Settlement amount**: funds paid out in the running account settlement, in the file of the working day after the settlement date.
- Bank guarantees, fixed deposits, commodities and other collateral are zero: clients do not give them to the firm in Ledgerline.

The check stops the file for: a missing or malformed PAN (firm, clearing member or client; shown masked), a client twice for the same segment and indicator, collateral received not equal to retained plus placed (or placed with the clearing member not equal to retained by it plus placed with NSE Clearing), a peak below the clear balance, totals that do not agree with the client ledgers or the day's allocation, and a client with a balance left out. It warns when cash allocated is more than the client's clear credit.

**Load NSE Clearing's response**: drop the response or error file NSE Clearing gives back (zip or gz is fine). Each line it did not accept opens a work item for the compliance officer saying why in plain words. NSE Clearing's response layout is not in the published circulars, so Ledgerline reads it leniently and says so; send a sample response file to have its layout confirmed.

## holding-api
title: Holding statement to NSE by API (and BSE)
screen: xfiles
roles: Ops maker, Ops checker, Compliance officer
ask: holding statement api; submit holding statement; nse holding statement; bse holding statement; befs holding statement; bank balance submission; holding statement rejected; inspection api; daily holding statement; hs file; holding statement error code; 805 error; submit earlier day first
related: exchange-files, connections

NSE takes each client's demat holdings every day except Sunday, by API (NSE circulars NSE/INSP/55039 and NSE/INSP/55380). A day is accepted only after the day before it.

1. **Set up** (Admin): [[connections|Settings → Connections]] → *NSE holding statement API*: the 5-digit member code, the API password and the secret key NSE mailed at registration on its inspection portal (the login ID is ApiUser followed by the member code unless NSE gave another). The secret key changes with every password reset.
2. **Submit by hand**: on [[xfiles|Exchange files]], under **Sent**, choose the date and **Download** on *Client holding statement*, then **Submit to NSE now**. Ledgerline checks every record first (demat account, name, PAN, ISIN, pledged plus free equals total) and sends nothing while a record would fail; the problems are listed.
3. **Automatic**: with *Submit the previous day's statement automatically* on, the scheduler sends each day up to yesterday that NSE has not accepted, oldest first, once the CDSL statement of holding for it is loaded. Ledgerline keeps the latest holdings only, so a day whose holdings were already replaced by a later CDSL file is not sent: a work item asks for it to go through NSE's portal.
4. Every submission is kept with NSE's acknowledgement and codes. A rejected file or record opens a work item with NSE's reason in plain words (for example *the client PAN is not right (905)* or *an earlier day's holding statement has not been submitted yet (805)*). Correct and submit again; each submission gets the next file number.

**Download the file** stays available for NSE's portal.

**BSE**: BSE also takes the holding statement daily except Sunday, by the next day (BSE notice 20230113-13). From 15 October 2025 a member of both NSE and BSE reports it to NSE only, and NSE shares it with BSE (NSE/INSP/70505), so nothing more is sent. A member of BSE that is not an NSE member downloads the day's file in the same panel, uploads it on BSE's BEFS portal and clicks **Recorded on BEFS** with BEFS's acknowledgement; a day not recorded opens a work item the next morning, and the compliance calendar has the daily duty. BSE's API is not set up: its document (Annexure I of the notice) is given to members and is not public, so ask BSE for it to have the API added.

The daily bank balance submission named in the same circulars is not sent: NSE stopped it from 30 October 2023 because the banks report balances directly (NSE/INSP/59044). The compliance calendar has a yearly duty to confirm the banks do.

## periodic-work
title: Weekly, monthly and quarterly work
screen: periodic
roles: Ops maker, Ops checker, Compliance officer
ask: periodic work; weekly statement; statement of funds and securities; monthly statement; month end; quarterly; bsda review; running account settlement; complaints disclosure; settlement master; ccil prices; g-sec prices; cuspa; mtf payout reporting; unpaidmtf
related: exchange-files

On [[periodic|Periodic work]] every task that is not daily is listed with the period it covers (the week to Friday, the month just ended, the quarter), when it is due and whether it is done. Work done on its own screen or by Autopilot counts.

1. **Weekly**: the statement of funds and securities to clients (Saturday); unpaid securities in CUSPA released to clients who have paid, and those still unpaid after the allowed days (Settings, 7 by default) queued for sale.
2. **Each settlement day**: the CUSPA and MTF pay-out reporting file for NSE Clearing (UNPAIDMTF), which needs the month's settlement master loaded on [[xfiles|Exchange files]].
3. **Monthly**: month-end postings (interest, MTF interest, DP bills, AP commission), statements of account, DP statements and the complaints disclosure; checks that the coming month's settlement master and CCIL's month-end G-sec and T-bill prices (MTMPRICE, .xls as published) are loaded.
4. **Running account settlement** on the exchanges' RAS dates (the Friday and/or Saturday of their joint annual calendar, separately for monthly and quarterly clients; kept on [[circulars|Circulars & calendar]] → *RAS dates & profile*), with the statements. A client with a credit balance and no transaction for 30 days (no trade and no funds received, and no open position or margin) is settled within 3 working days, whatever they chose, not on the monthly or quarterly date (NSE/INSP/53820); [[ras|Running account]] marks such clients with the date their money is due back.
5. **Quarterly BSDA review**: proposes conversions on the [[dp|Depository]] screen (BSDA review tab); a different checker applies it, the depository modification file is kept and clients are emailed. Record a client's choice to stay regular, or a demat account with another DP, so the review leaves them out.

**Run now** runs a task by hand. The **Periodic work** scheduled job (on [[jobs|Scheduled jobs]], every morning) runs the ones that can run by themselves and puts the rest in the work queue: a review to approve, a file to load, a return to upload.

## clearing-data
title: Use the clearing corporation's and CDSL's daily files
screen: ccdata
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: clearing data; mg13; client margin file; margin reporting file; short allocation; sa04; reason code; n-mass; nmass; stamp duty reconciliation; obligation file; direct payout; re-pledge; repledge; var file; haircut; cdsl closing price; isin master; security master; cdsl transaction file; dis master; txn_upld
related: exchange-files, periodic-work

Load the day's files on [[xfiles|Exchange files]]. What each one does:

- **Security masters** (NSE, BSE scrip series, NSE Clearing applicable symbols, CDSL ISIN master): name every ISIN clients hold.
- **CDSL closing prices, mutual fund VaR (NAV), NSE Clearing VaR file**: value holdings and collateral that have no exchange price, and give VaR, extreme loss and haircut where the day's market data has none.
- **Client margin files** (NSE Clearing MG13, MCXCCL): download the **margin reporting** files from Exchange files: the clearing corporation's row unaltered, with the margin collected from Ledgerline's allocation for the segment.
- **Short allocation (SA04)**: on [[ccdata|Clearing data]] record a reason (01–07) and the collateral available for each client, then download the **short allocation reporting** file.
- **N-MASS collateral, stamp duty, cash market obligation, re-pledge report, direct pay-out**: reconciled on Clearing data against Ledgerline's allocation, bills, trades and pledges; differences are counted.
- **BSE**: stamp duty (MBRSTD) against the stamp duty billed on BSE trades; clients BSE does not permit to trade and the permitted list against Ledgerline's active clients; transaction charges against those billed; order charges month to date; the clearing house statement; order-to-trade ratios from the order log.
- **CDSL transaction file**: credits and debits go to the DP transaction register (DP statements and billing; debits are billable).
- **Shortage reporting** and the **DIS master upload** (CDSL, weekly on Periodic work) are written from Ledgerline's settled short deliveries and the DIS books issued. Set the CDSL sender ID as member code **CDSL-SENDER** in Settings.
