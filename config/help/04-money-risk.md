Ledgy help articles: money and risk.

# Money

## record-receipt
title: Record money a client paid in
screen: money
roles: Ops maker, Ops checker, Branch staff
ask: record receipt; client paid money; add funds manually; cheque received; neft received; upi receipt; funds not credited; client says money sent
related: unknown-credit, cheque-return, third-party-funds

Credits on the client bank statement post themselves when you load the statement. For anything else:

1. On [[money|Receipts & payouts]], click **Record a receipt**: client, mode (Cheque, NEFT, RTGS, IMPS or UPI; **never cash**), date, reference (cheque number or UTR), amount and the account it came from.
2. An Ops checker approves it.
3. **Cheques** don't count towards limits or payouts until marked **Cleared**.

Money must come from the client's **registered** bank account. Anything else is refused.

## third-party-funds
title: Fix "not the client's registered bank account"
screen: money
roles: Ops checker
ask: third party funds; not the client's registered bank account; money from another account; payment from relative account; third-party receipt; funds from wrong account
related: unknown-credit, change-contact

SEBI forbids accepting a client's money from anyone else's account, including family.

1. Return the money to the account it came from (Autopilot does this for statement credits).
2. If the client has genuinely changed banks, add the new account through a **change request** with proof, approved by a checker. Then money from it is accepted.

## unknown-credit
title: Deal with an unidentified bank credit
screen: bank
roles: Ops checker
ask: unknown credit; unidentified receipt; money without client code; suspense; bank credit not matched; who sent this money; unmatched bank entry
related: third-party-funds, record-receipt

Credits that can't be matched to a client's registered account go to suspense on [[bank|Bank reconciliation]].

1. Autopilot suggests a client from the remitter's name and amount, in the work queue.
2. Get **written confirmation from the bank** that the remitting account belongs to that client.
3. With it: **Identify** and choose the client (Ops checker). Without it: **Return** the money to source.

Never post a credit to a client on a guess or on the client's word alone.

## live-bank-credits
title: Get client money from the bank as it lands
screen: connections
roles: Admin
ask: bank api; ecms; e-cms; virtual account; smart collect; auto collect; real time credit; instant credit; bank notification; credit intimation; hdfc ecms; axis ecoll; icici ecollection; kotak; yes bank
related: unknown-credit, third-party-funds

Instead of waiting for the bank statement, the bank (or Razorpay Smart Collect, or Cashfree virtual accounts) can tell Ledgerline about each credit the moment it lands. Each credit is checked exactly like a statement credit, so the statement later finds it already there.

1. On [[connections|Connections]] (Admin), open the one you use:
    - **HDFC Bank e-CMS credit alerts**: HDFC's own push format is built in. Enter the client money account number (credits to other accounts and all debits are ignored). Give the bank the address `/hooks/<firm>/bank/ecms` on your own domain. HDFC sends no password or signature, so Ledgerline accepts pushes only from the bank's published IP addresses (already filled in), and the address needs a CA-signed, domain-validated (not EV) certificate with its full chain, which the bank asks for in .txt form. Ledgerline answers each push with Success, Duplicate (same Alert Sequence No) or Technical Reject (the bank then pushes again).
    - **Bank credit notifications** for any other bank's collection service: Axis Power eColl, ICICI e-Collection, Kotak, Yes Bank and others. Banks give their message format only to their customers, so ask your bank's cash management team for the "real-time credit notification (push)" specification. Then fill in where the amount, UTR, virtual account and payer's account are in the bank's message, and how the bank signs it.
    - **Razorpay Smart Collect** or **Cashfree virtual bank accounts**: enter the webhook secret or client secret.
2. If clients pay into virtual accounts made of a prefix and their client code, enter the prefix.
3. Give the bank or provider the address shown (`/hooks/<firm>/bank/bank`, `…/razorpay` or `…/cashfree`) and the server's IP address.

Money from the client's registered account is a receipt at once, and is allocated to the clearing corporation. Money into a client's virtual account from any other account is held as third-party funds, and an alert is raised.

## cheque-return
title: Handle a returned (bounced) cheque
screen: money
roles: Ops maker, Ops checker
ask: cheque bounced; cheque returned; dishonoured cheque; cheque return charges; cheque not cleared
related: record-receipt, debit-interest

1. On [[money|Receipts & payouts]], find the cheque and click **Returned**, giving the bank's reason.
2. The receipt is reversed and the cheque return charge (Settings, plus GST) is debited.
3. If the client has traded against it, they're now in debit. Watch the margin and the debit block.

## payout
title: Pay out money to a client
screen: money
roles: Ops maker, Ops checker, Risk manager
ask: payout request; client wants withdrawal; approve payout; withdraw funds; payout rejected; only available to withdraw; payout amount more than available; release payout
related: payout-bank-file, ras

1. The client asks from their portal, or you click **Payout request** on [[money|Receipts & payouts]].
2. Ledgerline works out what they can withdraw across **all segments**: ledger balance less margin not covered by collateral, uncleared cheques, unposted interest and other approved payouts.
3. **Approve** (Ops checker or Risk manager) or **Reject** with a reason. Autopilot approves small, clean requests itself.

"Only ₹X is available" means the request is more than that. Ask for a smaller amount or reject. Money goes only to the client's registered bank account.

## payout-bank-file
title: Send approved payouts to the bank
screen: money
roles: Ops maker, Ops checker
ask: payout file; bank payout file; neft file; rtgs file; hdfc file; hdfc enet; enet upload; release payouts; payout utr; payout failed; bank result; payout file blocked; cancel payout file; file sequence number; instruction reference
related: payout

1. On [[money|Receipts & payouts]], an Ops checker clicks **Release payouts** (Autopilot does it at the cut-off). Each client is debited and a payout batch is made. Release as often as you send files: two or three batches a day is normal.
2. On the batch, click **HDFC ENet file**. You see each payment's type and any problems. Click **Produce and download** and upload the plain file (for example `ABCD12340411.817`) on HDFC ENet.
3. When the bank responds, click **Enter bank result**: the UTR for each paid item, or mark it failed. Failed payouts go back to the client's ledger.

**The file.** HDFC ENet's bulk upload format: one line per payment, 28 comma-separated fields, no header or trailer, CRLF line endings. Each line carries:
- the client code as beneficiary code;
- the registered account, the amount (2 decimals), the name, the IFSC, the bank and branch and the client's registered email;
- a 14-digit instruction reference (your prefix and a running number, given once per payment);
- the customer reference `UCC-PAY`;
- the value date as DD/MM/YYYY.

**Transaction type.**
- **I** (HDFC to HDFC) when the IFSC starts with HDFC.
- **R** (RTGS) from the RTGS threshold (₹2,00,000 unless changed), or when the payout was marked *Send by RTGS*.
- **N** (NEFT) otherwise.

Settings can turn I off or force one type.

**Checks.** Any of these blocks the file, and every problem is listed:
- the account is not the client's registered bank account;
- the IFSC is not 11 characters with 0 as the 5th;
- the account number is not 9–18 digits;
- an amount is not above zero;
- a client appears twice (unless allowed);
- the file total differs from the batch's approved total;
- there is no registered email;
- a forced type doesn't fit (I needs an HDFC IFSC; R needs ₹2 lakh);
- a field is too long or has characters the file cannot carry.

Commas and line breaks are taken out of names and bank names. HDFC doesn't publish field lengths, so Ledgerline uses safe limits:
- name, bank name and branch: 35 characters;
- client code and customer reference: 20 characters;
- email: 100 characters.

**One file per batch.** A file can be downloaded again; each download is recorded with who and when. To make a new file for the same payouts (not uploaded, or rejected by the bank), an Ops checker first clicks **Cancel file** and gives the reason. The new file takes the next number and keeps the same instruction references.

**Settings → Receipts and payouts.**
- **HDFC ENet client ID.**
- **File name template:** default `{hdfc_client_id}{date:0201}.{seq:3}`, the ID then DDMM then the number. You can put a fixed number in place of the date.
- **File sequence:** *running* across days, the default, wrapping from 999 to 001; or *restart at 001 each day*.
- **Next file number:** set it to carry on from the previous system's last file. There is an optional **second series** with its own number. Tick *Number from the second series* when producing a file.
- **Instruction reference prefix**, **customer reference suffix** (PAY), **RTGS from**, **transaction type**, **HDFC-to-HDFC transfers**, **allow a client twice**.

## ras
title: Run the running account settlement (RAS)
screen: ras
roles: Ops checker
ask: running account settlement; ras; quarterly settlement; monthly settlement; settle client funds; return idle funds; 30 days no trade; idle client funds; 3 working days
related: payout, ras-calendar

SEBI requires unused client funds to be returned monthly or quarterly, as each client chose, on the dates the exchanges set jointly for each financial year: a Friday and/or the Saturday after it (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2023/197). For FY 2026-27, for example, the quarterly dates are 17/18 April, 3/4 July, 16/17 October 2026 and 1/2 January 2027. The dates come from the exchanges' joint calendar, kept on [[circulars|Circulars & calendar]] → *RAS dates & profile* by the compliance officer or an admin (see *Keep the exchanges' RAS dates*); Settings shows the next monthly and quarterly date from that list, and the compliance calendar dates the two settlements from it.

**Idle clients: 3 working days.** A client with a credit balance and no transaction for 30 calendar days (no trade and no funds received; open positions or margin count as activity) is settled within 3 working days after the 30 days, whatever they chose, not on the next monthly or quarterly date (NSE/INSP/53820). Fresh funds start the 30 days again.

1. Open [[ras|Running account]] on the settlement date. It lists each client's amount to return, keeping the allowed buffer. Idle clients are marked with the date their money is due back, and are included in any settlement run from that day.
2. Download the **Bank payout file**, pay through the bank, and record the result.
3. Clients get a statement of the settlement.

## debit-interest
title: Handle clients in debit and interest
screen: debits
roles: Risk manager, Ops checker
ask: client in debit; debit balance; interest on debit; dpc; delayed payment charges; daily interest; interest on interest; compound interest; debit block; debit ageing; recover dues; client owes money
related: client-cant-trade, margin-shortfall

On [[debits|Debtors & interest]]:

- **Ageing:** how long each client has been in debit.
- **Debit block:** after the days set in Settings, buying is blocked until they pay. Autopilot does this before the market opens; download the **Block / unblock file** for the trading system if it isn't connected.
- **Interest (delayed payment charges):** charged on each day's debit from the pay-in date, at the Settings rate after the grace days. Settings → Debit balances and interest → **Interest posting** decides how:
  - **Monthly** (the default): simple interest, posted with the month-end postings (a checker approves), with a GST invoice.
  - **Daily**: posted every day from the start date you set, on that day's debit including the interest and GST already posted, so interest earns interest from the second day. Autopilot posts it at the end of each day (weekends with the next run), or use **Post daily interest** on the Interest tab. Month end issues each client one tax invoice for the month.
- Clients in debit get reminders (Scheduled jobs → Debit reminders).
- Old debits that no longer move can be kept apart as [[legacy|Legacy debts]]: no interest, no daily reminders, still blocked.

## legacy-debts
title: Keep old client debits apart (legacy debts)
screen: legacy
roles: Ops checker, Risk manager, Compliance officer, Ops maker
ask: legacy debt; old debit; old dues; stale debit balance; dormant account in debit; write off debit; bad debt; frozen debit; sundry debtors; recover old dues; balance confirmation; limitation
related: debit-interest, client-cant-trade

On [[legacy|Debtors → Legacy debts]]:

1. **Find frozen debits:** client debits that have not moved for 90, 180, 365 or 730 days, largest first.
2. **Propose** the ones that are old dues rather than today's trading, with the reason. Debits marked **Legacy = Y** in the opening balances at go-live arrive here as proposals.
3. **A second person approves** (Ops checker or Compliance officer). The person who proposed cannot approve.

Once approved, a legacy debt:

- is not charged interest;
- is left out of debit reminders, the debit totals and the ageing on [[debits|Debtors & interest]];
- still blocks the client from buying and from payouts, as any debit does;
- blocks reactivating an inactive account until it is paid or written off.

To recover it:

- **Send reminder** emails the client the amount (message "legacy_debt.reminder").
- **Note** records calls, notices, settlements and a **Balance confirmed** from the client.
- **3 years from that** counts from the last payment or confirmation. Have a lawyer look at the large debts before that date: recovery claims are generally time-barred three years after the money fell due unless the client has paid or acknowledged the debt since. This is a reminder, not legal advice.
- **Write off** (Ops checker) credits the client and debits Bad debts written off, with the reason. Part write-offs are allowed. Ask your CA about the tax treatment.

A legacy debt closes by itself when the client is no longer in debit. **Unmark** returns it to the daily debit work.

## vouchers-gst
title: Post a voucher or file GST
screen: books
roles: Ops maker, Ops checker
ask: voucher; journal entry; expense entry; payment voucher; firm accounts; gst return; gstr-1; gstr-3b; profit and loss; balance sheet; chart of accounts
related: period-locked, vendor-bills

On [[books|Firm accounts & GST]]:

1. **New voucher:** payment, receipt, journal or contra, with lines that balance. An Ops checker who didn't make it **Posts** it.
2. **New account** adds to the chart of accounts. Accounts can be restricted to named users (salaries, for example).
3. **GST:** choose the period for GSTR-1 tables and the GSTR-3B summary from everything billed to clients.
4. **P&L and balance sheet** for the firm or a branch.

## vendor-bills
title: Add a vendor or book and pay a vendor bill
screen: books
roles: Ops maker, Ops checker
ask: vendor bill; purchase bill; book a bill; scan a bill; upload bill; read bill; vendor master; add vendor; supplier; pay vendor; tds on vendor; msme payment; 43b(h); bill register; input tax credit; reverse charge
related: vouchers-gst

On [[books|Firm accounts & GST]] → **Vendor bills**:

1. **Scan a bill:** upload the PDF, JPEG or PNG. A PDF made by the vendor's software is read on the server: vendor, GSTIN (check digit verified), PAN, address, bank account and IFSC, bill number and date, taxable value, CGST, SGST, IGST and total fill the form. A scanned or photographed bill has no text: type the details, or use **Read with AI** if an admin has switched on *Read bills with AI* in Settings (the bill is then sent to the AI provider and charged from the AI credit; not available in India-only mode).
2. **Check every figure against the bill.** Warnings show what doesn't add up: a wrong GSTIN check digit, totals that don't match, IGST from a vendor in your own state, a bill not made out to the firm's GSTIN.
3. A new vendor is added from the bill in one click (**Add vendor**). Set the **TDS section**, payment terms, and the **Udyam number** for a micro or small enterprise: their bills must be paid within the agreed days, at most 45 (15 without an agreement), or the expense is disallowed until paid (section 37(2)(g) of the Income-tax Act 2025; section 43B(h) before 1 April 2026). The TDS sections are shown by their 2025-Act provision (all TDS other than salary is in section 393, at the same rates) with the 1961 section they replaced; bills dated before 1 April 2026 keep the 1961 section in the voucher.
4. **Book bill** makes a journal voucher: expense and input GST debited, TDS and the vendor credited (reverse charge credits GST payable instead). An Ops checker who didn't make it posts it.
5. **Pay** makes a payment voucher from the firm's own bank account; a checker posts it. Part payments are allowed.

If the bank account on a bill differs from the vendor master, Ledgerline warns you: confirm with the vendor on a number you already know before paying. The same file uploaded twice, or the same bill number from the same vendor, is caught. **Download CSV** gives the register with PAN and TDS columns for the TDS return.

## clearing-deposits
title: Record clearing deposits and base capital
screen: deposits
roles: Ops checker
ask: clearing deposit; fd lien; bank guarantee; base minimum capital; bmc; collateral with clearing corporation; deposit expiring; 50 percent cash
related: upstream

On [[deposits|Clearing deposits]], **Record a deposit** for each item with the clearing corporation: cash, FD lien-marked, bank guarantee or securities, with value, haircut and maturity.

- Ledgerline checks base minimum capital (**Base capital required**) and that at least half is cash or cash equivalents.
- Maturities are queued 30 days ahead; when renewed or released, click **Renewed / released**.

## clearing-allocation
title: Allocate client collateral to NSE Clearing
screen: allocation
roles: Ops checker, Risk manager
ask: allocation; client allocation; collateral allocation; NSE Clearing API; CIM allocation; allocate funds to client; short allocation; push allocation; money came in allocate; N-MASS allocation; allocation overwritten; consumer key; allocation API key
related: clearing-deposits, margin-shortfall, record-receipt

NSE Clearing holds each client's collateral as the firm allocates it. The amount sent is the client's **final** allocation, not an addition: a request built on an old figure cuts the client's allocation and can cause short allocation. Ledgerline therefore builds every request on the level NSE Clearing last confirmed.

**Set up (Admin), once:**
1. The clearing member registers for the API: email collaterals_ops@nsccl.co.in with the member code, name, email, phone and this server's IP addresses (circular NCL/CMPT/52129, Annexure 1).
2. Enter the consumer key and secret NSE Clearing emails back in [[connections|Connections]] → NSE Clearing allocation API.
3. In [[settings|Settings]] → NSE Clearing allocation, switch on sending, and choose whether money that comes in is allocated at once and to which segment. There is no test system: the first request is live.

**During the day:**
- When money is credited (receipt approved, cheque cleared, bank credit matched, online payment), it is added to the client's confirmed level and sent as an upward request.
- Only one request per client is out at a time; more money waits for the result.
- A money-in request never lowers a level.
- Results are fetched every minute. A partly accepted request leaves the level **unconfirmed**: nothing more is sent for that client until a checker uses **Confirm a level** with the figure from N-MASS.
- Requests that fail because NSE Clearing is unavailable are resent under a new message ID; no result after 5 minutes raises a work item.

**Start and end of day:** on [[allocation|Clearing allocation]], **Prepare start-of-day batch** or **Prepare end-of-day batch** brings every client to Ledgerline's figure (cash allocated), only where it differs, reductions first. A second person approves it before it is sent. The end-of-day batch goes as type E, applied at NSE Clearing's end-of-day release. Allocation closes at 20:00.

Confirmed levels come from NSE Clearing's results and from the N-MASS file loaded on Exchange files. Every request and reply is kept by message ID, as evidence when reporting short allocation (reason 05, allocation accepted later).

## upstream
title: Upstream client funds to the clearing corporation
screen: ledger
roles: Ops checker
ask: upstreaming; upstream funds; client funds to cc; end of day upstream; segregation; client funds segregation
related: clearing-deposits, eod-close

Client money left in bank accounts at end of day must be upstreamed to the clearing corporation.

1. On [[ledger|Ledger]], click **Upstream funds to CC** with the amount sent.
2. **EOD client allocation to CC** on [[rms|RMS & limits]] gives the file of how much collateral belongs to each client.

The day-close check "Client funds segregation" shows whether client money is covered.

# Risk

## nse-trade-api
title: See today's NSE trades during the day
screen: live
roles: Admin, Risk manager
ask: notis; nse trade api; trade inquiry; intraday trades; online trade file; trades during the day; connect2nse; trade api key; actions inquiry; trade modification; client code modification
related: margin-shortfall, peak-margin

NSE's trade API (NOTIS) gives each trade a few seconds after it happens, including client code changes and cancellations. Ledgerline uses it for [[live|Live RMS]] until the end-of-day trade file arrives.

1. Ask NSE (through the member portal) for trade API access for the cash and F&O segments. NSE gives a consumer key and secret for each, and allows only your registered server IPs.
2. On [[connections|Connections]] (Admin), open **NSE trade API (NOTIS), cash market** and **NSE trade API (NOTIS), F&O**: enter the member code, key and secret, choose UAT or Live, and turn **Fetch during the day** on.
3. Ledgerline asks for new trades every 15 seconds or more, as NSE requires, between 09:00 and 16:30, and never between 05:00 and 06:30 when NSE refuses calls.

A trade whose client code was changed shows under the new code. Cancelled trades are left out. Once the end-of-day trade file is loaded, it replaces these trades.

## ncl-margins-api
title: Get NSE Clearing's own margin figures during the day
screen: live
roles: Admin, Risk manager
ask: ncl margin api; nsccl margin api; nmass api; margins api; exchange margin; clearing margin during the day; client margin from nse clearing; cli margins; tm margins; cm margins; mwpl api; open interest api; rsa certificate; api access management
related: nse-trade-api, margin-shortfall, peak-margin

NSE Clearing can give the margins it is actually charging, during market hours, for F&O, currency and commodity: for the clearing member, for each trading member, and for each client, with client positions, member open interest and market-wide position limits. [[live|Live RMS]] then uses NSE Clearing's margin for a client whenever it is higher than Ledgerline's own estimate, so a shortfall shows before the end-of-day file.

1. Email risk_ops@nsccl.co.in, subject "API Facility in NMASS-Margins", with your NMASS super admin's details.
2. On [[connections|Connections]] (Admin), open **NSE Clearing margins API**, enter the clearing member code and save. Then press **Make key**: Ledgerline makes the key pair, keeps the private key in its vault and gives you the certificate file `RSA_<member code>.pem`. (Or paste the private key of a certificate you already have.)
3. On NMASS, under **API Access Management**, choose the margins service, enter your server's IP addresses (up to 25) and upload the `.pem` file. After NSE Clearing approves, the super admin gets the consumer key and secret by email.
4. Enter the key and secret, the segments, UAT or Live, and turn **Fetch during the day** on.

Ledgerline then asks for client margins every 5 minutes, and for the clearing and trading member figures every 15, from 09:15 to 15:45 (commodity until 23:30), keeping to NSE Clearing's limits. With more than 50,000 clients it asks for the whole-book file instead. The certificate lasts a year: make a new key and upload it again before it runs out.

## nse-extranet-api
title: Fetch NSE's files automatically from the extranet
screen: xfiles
roles: Admin, Ops maker
ask: nse extranet api; extranet download; download nse files automatically; onlinebackup; trade file from extranet; udiff trade file download; extranet secret key; extranet registration; member portal extranet; common folder; extranet error 701; extranet error 803
related: nse-trade-api

Instead of someone downloading the day's files from the NSE extranet and uploading them, Ledgerline can fetch them itself through NSE's extranet API and load each one it reads, just as an uploaded file is loaded.

1. On NSE's Member Portal, register for the Extranet API (the help file is in NSE circular MSD55065). Set a password; NSE emails the login ID and a secret key. The admin user must give that login access to the segments (and to common files, if wanted).
2. On [[connections|Connections]] (Admin), open **NSE extranet API**: enter the member code, login ID, password and secret key, Internet or lease line, the segments, and turn **Fetch new files automatically** on.
3. **Folders to look in** defaults to `member:/Onlinebackup, member:/Reports` (the day's trade files and reports). Add `common:/…` folders for common files.

Every 15 minutes (not from 05:00 to 06:30, NSE's maintenance hour) Ledgerline logs in, looks through those folders and two levels below them, and downloads each file of the day that it reads and has not already fetched. Files it does not read are left on the extranet. NSE keeps one login valid at a time, so do not use the same API login in another program.

## margin-shortfall
title: Handle a client's margin shortfall
screen: rms
roles: Risk manager
ask: margin shortfall; margin breach; client in breach; margin call; square off client; insufficient margin; client exceeded limit; utilisation high
related: peak-margin, limit-request, pledge

On [[rms|RMS & limits]] (or [[live|Live RMS]] during market hours):

1. **Watch / Breach** status shows clients whose margin use is high or over.
2. Autopilot sends **margin calls** by message. Call the client too.
3. If they don't bring funds or pledge shares by the cut-off: **Square off** (one client) or **Square off all breaches** (Risk manager only).
4. A shortfall at the clearing snapshots attracts a penalty (see "peak margin penalty").

Breaches left open block a clean day close.

## peak-margin
title: Understand a peak margin penalty
screen: margin
roles: Risk manager, Ops checker, Compliance officer
ask: peak margin; margin penalty; short collection penalty; penalty slab; why penalty; margin reporting; snapshot; prepare penalties
related: margin-shortfall, penalty-pass-through

On [[margin|Margin reporting]]:

- Peak margin is rebuilt from trade timestamps at each clearing snapshot time.
- Collection = the client's funds before the day's bills + UPI blocks + usable collateral.
- **Penalty:** 0.5% if the shortfall is under ₹1 lakh **and** under 10% of the requirement, otherwise 1%.
- **Prepare penalties** (Risk manager, Ops checker or Compliance officer) lists the day's penalties for a decision. Penalties are prepared, never posted to clients automatically: each is the firm's. Passing one on needs an allowed reason (a dishonoured cheque or a disclosed hedge break), the evidence, and a second person's approval; see *Pass a margin penalty to a client*.

## pledge
title: Accept a margin pledge
screen: collateral
roles: Ops maker, Ops checker, Risk manager
ask: pledge request; accept pledge; margin pledge; repledge; collateral; pledge shares for margin; unpledge
related: margin-shortfall, mtf

On [[collateral|Collateral & pledge]], pledge requests from the depository appear for decision.

1. **Accept** or **Reject**. Autopilot accepts requests from active, unblocked clients in securities with a haircut.
2. The value after haircut (from the day's market data) counts as collateral.

Securities stay in the client's demat; they're only pledged.

## mtf
title: Run margin trading funding (MTF)
screen: mtf
roles: Ops checker, Risk manager
ask: mtf; margin trading funding; mtf enrolment; mtf interest; mtf liquidation; mtf pledge; fund client under mtf; mtf limit
related: pledge, margin-shortfall

On [[mtf|Margin trading (MTF)]]:

1. **Save policy** once (limits, margin, interest rate, eligible securities).
2. **Enrol client:** needs the client's MTF agreement.
3. **Fund** a client's purchase within their limit. The shares must be **Pledged** by T+1.
4. **Raise margin calls** when cover falls; unmet calls go to liquidation (a person decides).
5. **Post interest** monthly; **Repay** to close a position.
6. Download the **Exchange MTF report** daily.

## bod-files
title: Send limits and holdings to the trading system
screen: rms
roles: Risk manager, Ops checker
ask: bod file; limit file; oms limits; notional cash; cash equivalent collateral; account id suffix; finspot collateral; trading limits not updated; holdings file to oms; start of day file; t1 holdings; finspot; finspot margin basket; finspot dealer; finspot limit file; finspot holding file; finspot block; finspot client creation; isin holding file; rms limits file
related: client-cant-trade, debit-interest

On [[rms|RMS & limits]], download:

- **BOD limit file:** each client's opening limit, for the trading system.
- **BOD holdings file (with T1):** sellable holdings including unsettled T1 shares.

Load both into the trading system before the market opens. Autopilot prepares them in the start-of-day stage.

**Finspot.** Under **Finspot uploads** the same files come in Finspot's own layouts, named as Finspot expects:
- **BOD limits** (RMS Limits, by 08:50): Finspot works out the limit itself, so each client's line across exchanges has the **ledger balance** as it stood at the end of the previous business day (column 5, negative when the client owes; uncleared cheques left out), the **group wise validation indicator** (column 17, `no` unless set otherwise in the connection) and **notional cash** (column 25): pledged **cash-equivalent** collateral after the day's haircut. For example `D224-BJT||||-20000.00||||||||||||no||||||||150000.00`. A blocked client (or one with no open segment) keeps a debit but gets no positive balance and no notional cash. Approved limit overrides and UPI blocks are not in the Finspot line. Finspot carries the equity segments only: the balance is meant to be the equity balance, and is the whole client ledger until segment-wise client ledgers exist.
- **Cash equivalent** collateral: government securities, T-bills and SDLs (by ISIN, or NSE series GS, TB, SG), and the ISINs the firm lists under **Cash-equivalent collateral ISINs** in Connections → **Finspot uploads** (units of liquid and overnight funds, liquid ETFs; empty until the firm fills it). Every other pledged security is non-cash collateral.
- **BOD holdings** (ISIN Holding, by 08:54): sellable demat holdings (margin-pledged shares included), less shares pledged to lenders or frozen at CDSL, plus unpaid purchases in CUSPA. A margin-pledged **non-cash** security is collateral on its line (IS Collateral 1, Collateral Qty the pledged quantity, with the haircut), and Finspot adds its value to the limit. Cash-equivalent pledges are already notional cash in the limit file, so they are not collateral here: nothing is counted twice. A blocked client's holdings carry no collateral.
- **Account ID suffix**: set it in the connection (e.g. `-BJT`) and every Finspot file writes the client's account and user IDs as the UCC with the suffix (`D224-BJT`). Finspot's RMS limit account ID and segment user ID hold 10 characters: those files are refused, naming the clients, when the UCC with the suffix is longer.
- **T1 holdings** (T1 - ISIN Holdings): yesterday's delivery buys not yet received, so they can be sold.
- **Block / unblock**: every client blocked in Ledgerline (kill switch, debit block, debarred, inactive, no open segment) blocked for new MIS and NRML positions and CNC buys, square-off allowed; and an unblock line for each client Ledgerline blocked earlier who is no longer blocked. Blocks set by hand on Finspot are never lifted by the file.
- **New clients**, **Client segments**, **Client products**: logins for active clients new or changed since the last file (add `&all=1` to the address for every client, at go-live). Passwords are never in the file; Finspot sets them. The segment file needs the firm's exchange user IDs in Connections → **Finspot uploads**.
- **F&O cost price** (NFO_COST_PRICE): open NSE F&O positions with their average cost.
- **Dealer client positions**: open derivatives positions (buy or sell quantity and amount, NRML) under the firm's position dealer.
- **NSE / BSE margin basket**: margin % per scrip (VaR + extreme loss margin, never below the minimum, 20% by default) for the basket product and name set in the connection. The BSE basket needs BSE's scrip and series file loaded (for the scrip codes).
- **Dealer order types**: the order types (LMT, MKT, SL-LMT, SL-MKT, DS by default) for each dealer ID set in the connection.
- **Client information (addition)**: Finspot's shorter client addition file, for the same clients as **New clients**.

Saving the **Finspot uploads** connection puts the BOD limit and holding files on the daily file checklist.

## kill-switch
title: Stop a client's trading at once
screen: rms
roles: Risk manager
ask: kill switch; stop client trading; freeze client; block trading immediately; suspicious trading stop
related: margin-shortfall, surveillance

On [[rms|RMS & limits]], use the kill switch on the client's row. Their limit drops to nil immediately. Only a Risk manager can do this, and it's recorded with the reason.

## payout-hold
title: Pay-out holds for big sellers
screen: payouthold
roles: Risk manager, Ops checker
ask: pay-out hold; payout hold; big seller; big sellers; sale proceeds awaiting payout; sale money awaiting pay-out; hold sale money; broker funds tied up; de-allocate sale money; reduce allocation until payout; payout not received; nse clearing payout credit; release pay-out hold
related: clearing-allocation, bod-files, live-bank-credits

**Why.** A client who sold on T has the sale money as a ledger credit on T+1, and the start-of-day allocation gives it to the client at NSE Clearing. The clearing corporation pays the sale money out only around 13:00 on T+1. Until then that allocation is backed by the **firm's own funds** at NSE Clearing, and one or two big sellers can tie up most of them. Pay-out holds take most of that money back from the allocation and the OMS limit until the pay-out lands, and then give it back.

**The rule** (set under **Rules** on [[payouthold|Pay-out holds]] by the risk manager or Admin):
1. **Starting broker funds**: the free broker funds of the segments counted (CM and F&O by default), from NSE Clearing's margins API (trading member figures), else the N-MASS file, else the figure the risk team types in each morning (with who and when). The screen shows which source was used. With no figure nothing is held, and a work item asks for it.
2. **Net sale money awaiting pay-out**, for each settlement whose funds pay-out is today: sale value − purchase value − charges, if positive (or, on the gross basis, sale value − charges).
3. A client above the **threshold** (20% of starting broker funds by default) is held at the **hold %** (80% by default) of it, never more than the money awaiting pay-out and never below zero allocation. Holds below the **minimum hold** are not made. About 20% stays allocated, enough margin for the client to buy about the full amount in non-trade-for-trade stocks before pay-out.
4. **When**: once the start-of-day files are done (the start-of-day allocation sent or accepted, or the allocation file made, and the BOD limit file made), or at the **apply time** (08:45 by default) when they are not detected. **Apply holds now** does it at once.
5. **Mode**: automatic (holds are made at once) or propose (the risk team approves or edits each hold, then it is made).

**What is changed.**
- With NSE Clearing's allocation API on, each hold is sent as the client's **final** allocation (confirmed level less the hold, action D); the release sends the confirmed level plus the hold (action U). One request per client is out at a time; results come back as for any allocation request. A rejected hold is not put back into money in.
- With the API off, download the **N-MASS hold file** (or release file), upload it in N-MASS, then press **Done in N-MASS**.
- The trading front end has no API: download the **OMS hold file** and later the **OMS release file** and upload them. They are Finspot RMS Limits files, the same line as the BOD limit file: the ledger balance less the amount held (on release, less only what is still held), with the client's notional cash again so the upload does not wipe it.
- [[live|Live RMS]] shows the hold and takes it off the client's free funds. Later allocation batches that day keep the hold; after release they use the normal figure.

**Release.** The holds are given back when the pay-out is seen:
- a live credit from the clearing corporation on the settlement account (the remitter pattern, e.g. "NSE CLEARING", or the settlement account number), or the same credit in the day's bank statement;
- **Pay-out received** (risk manager or ops checker), for all settlements or only those ticked;
- optionally, at the **assume paid out at** time (off by default; 13:30 suggested), with a work item saying no credit was seen.
A credit that names a settlement number, or ICCL rather than NSE Clearing, releases only the matching settlements' share.

**Good to know.** Every N-MASS change is made at NSE Clearing: it is recorded there, visible to the clearing corporation and appears in the client collateral reporting. Holding back unpaid sale money from allocation is the conservative position: the client's own cleared money is never reduced, only money the clearing corporation has not yet paid. Every hold, approval, release and file is in the audit trail; failures, proposals, a pay-out not seen and holds larger than broker funds raise work items for the risk team.


## commodity-ledger
title: Keep separate equity and commodity ledgers
screen: settings
roles: Admin, Risk manager, Ops checker
ask: separate commodity ledger; equity and commodity ledger; two ledgers; commodity balance in finspot limit; mcx money in equity limit; trading member only on mcx; commodity cleared by another member; bow limit; bow funds; ledger per segment; segment wise ledger; limit jumped after receipt
related: funds-awaiting-allocation, ledger-transfer, bod-files

**Who needs it.** A firm that is only a **trading member** on a commodity exchange: its MCX trades are cleared by another clearing member and placed on another front end (for example BOW), while equity trades on Finspot. Then the client's commodity money is not the equity front end's: if both sit in one ledger, a ₹5,000 receipt can lift the Finspot limit from ₹10,000 to ₹2,15,000 because the ₹2,00,000 commodity balance is in it.

**Switching it on.** On [[settings|Settings]] (Admin), under **Equity and commodity ledgers**, tick **Separate commodity ledger**. Off (the default), there is one ledger per client, exactly as before. On, each client has:
- the **Equity ledger** (`CLIENT:<code>`, NSE and BSE): the Finspot limit and pay-out hold lines (column 5), the NSE Clearing allocation, margin reporting and N-MASS, SEGMON, equity settlement, CUSPA, MTF, stock SIPs, DP charges, interest;
- the **Commodity ledger** (`CLIENTCOM:<code>`, MCX): bills, carried futures MTM and expiry charges of MCX trades, the BOW commodity funds (Front office files → **BOW commodity funds**), the MCXCCL margin reporting;
- **Awaiting allocation** (`CLIENTUNALLOC:<code>`): money received with no instruction where it goes. It is in neither front end's limit.

The client always sees **one ledger**: statements, the portal, contract-note ledgers, SMS and email balances and annual statements merge the ledgers with one running balance, and leave out transfers between the client's own ledgers. Running account settlement, debit ageing and interest, dormant funds and the client-money check look at the client as a whole.

**Opening balances.** In the OPENING-BALANCES file add a **Ledger** column (Equity or Commodity) for client rows; an empty Ledger is Equity.

**Turning it off** is refused while any client still has money in a commodity or awaiting ledger: transfer it to the equity ledger first.

Good to know: bills already posted keep the ledger they were posted to. Switch on at the start of a day, before billing, and load the commodity opening balances the same day.

## funds-awaiting-allocation
title: Allocate a credit to the equity or commodity ledger
screen: rms
roles: Risk manager, Ops checker, Ops maker
ask: awaiting allocation; credit awaiting allocation; choose equity or commodity; which ledger; unallocated receipt; ecms credit not in limit; credit not in finspot; credit not in bow; allocate receipt; receipt went to suspense ledger
related: commodity-ledger, ledger-transfer, live-bank-credits

With separate equity and commodity ledgers, money from a client goes to a ledger by the **first instruction** that applies:
1. the firm's **bank account** it was paid into (Settings: one line per account, `account number = Equity`, `Commodity` or `Ask`);
2. a **word in the narration** or remarks: Commodity for MCX, COMM, COMMODITY, COMMODITIES; Equity for NSE, BSE, EQ, EQUITY, F&O, FNO (whole words; your own lists in Settings replace these). A narration naming both is left for a person;
3. the **client's default** (client drawer → *Money in with no instruction goes to*).

Otherwise the money **waits**. It is credited to the client's awaiting-allocation account at once, so it is in the client's ledger and statements, but it is in **neither** front end's limit. An urgent work item goes to Risk: "₹X from client Y: choose Equity or Commodity".

To allocate, open [[rms|RMS & limits]] → **Money awaiting allocation** (or the client drawer) and click **Equity** or **Commodity**. The allocation is a journal from the awaiting account to the ledger, dated the day the money came in, so the next Finspot BOD limit file (or BOW commodity funds) carries it; money allocated to Equity is also sent to NSE Clearing when automatic allocation is on. Optionally, Settings can put money still waiting after so many minutes in Equity or Commodity.

Money awaiting allocation is not paid out until it is allocated, and SEGMON warns if any is still waiting at the end of the day.

## ledger-transfer
title: Move money between a client's equity and commodity ledgers
screen: books
roles: Risk manager, Ops maker, Ops checker
ask: transfer between equity and commodity; move money from commodity to equity; inter ledger transfer; inter-segment transfer; commodity profit to equity; payout from commodity ledger; ledger cannot pay; payout refused ledger
related: commodity-ledger, funds-awaiting-allocation, payout

1. In the client drawer, click **Transfer between equity and commodity**, choose From, the amount and a note.
2. It becomes a **journal voucher**; an Ops checker approves it under [[books|Firm accounts & GST]] → Vouchers, like every journal. Only then do the balances move.
3. The client does not see it: it moves money between the client's own ledgers and nets to nothing in the client's statement.

**Payouts.** Receipts & payouts lets you pay from Equity, Commodity or either ledger. *Either* pays from each ledger only up to its own credit. A payout from one ledger that the ledger cannot pay on its own is **refused** with the shortfall to transfer, unless Settings lets payouts **transfer the shortfall** from the other ledger (recorded as a transfer entry on the payout). Running account settlement settles the client as a whole: a ledger in debit is first made good from the other, and no ledger is left below zero.

## mcx-only-bank-account
title: Keep MCX money out of equity limits with one ledger (MCX-only bank account)
screen: settings
roles: Admin, Risk manager, Ops checker
ask: mcx only bank account; mcx bank account; commodity bank account; funds received in mcx account; mcx money in finspot; earmark mcx money; mcx earmark; mcx money earmarked; one ledger mcx and equity; commodity money with one ledger; finspot limit includes commodity money; mcx collection account
related: mcx-earmark-move, commodity-ledger, bod-files, payout

**Who needs it.** A firm that keeps **one ledger per client** (Settings → *Separate commodity ledger* off) but collects money for MCX in a bank account of its own. Money a client pays into that account is for MCX only: it must never lift the Finspot limit or go to NSE Clearing.

**Switching it on.** On [[settings|Settings]] (Admin), under **MCX-only bank accounts**, enter the account numbers, one per line. With no account entered nothing changes. With the separate commodity ledger on the setting is not used: map the account to Commodity under *Money paid into these firm bank accounts* instead, which already does this.

**What it does.** The client keeps one ledger (`CLIENT:<code>`) and sees one ledger: statements, the portal, SMS and WhatsApp balances, running account settlement, debit ageing and interest are all unchanged. Ledgerline works out each client's **MCX money (earmarked)** from that ledger:
- plus money paid into an MCX-only account (e-CMS, bank statement credits with the ToAccount column, gateway payments whose notes give `to_account`, receipts entered by hand with *Paid into* the MCX-only account);
- plus MCX profits, less MCX losses and charges (bills, carried futures MTM and expiry charges of MCX trades): losses take from the MCX money first;
- less payouts of MCX money (from the MCX-only account), plus a returned one;
- plus or minus moves approved on RMS.

It never goes below zero (an MCX loss larger than the MCX money is borne by the client's other money) and is never more than the client's credit. **Equity money** is the ledger less the MCX money.

**Where each is used.** Equity money only: the Finspot RMS line (column 5) and pay-out hold lines, equity risk and limits, NSE Clearing allocation, cash and F&O margin reporting, N-MASS, SEGMON and stock SIP orders. MCX money: the MCX allocation and MCXCCL margin reporting, MCX margin on RMS and live risk, and the BOW commodity funds file. Money paid into the MCX-only account is never sent to NSE Clearing as money in.

**Example.** A client has ₹10,000 of equity money and ₹2,00,000 of MCX money. ₹5,000 by e-CMS into the usual account: Finspot column 5 becomes ₹15,000. The same ₹5,000 into the MCX-only account: Finspot stays ₹10,000 and the MCX money is ₹2,05,000. An MCX loss of ₹50,000 brings the MCX money to ₹1,55,000; Finspot is unchanged.

**Payouts.** In Receipts & payouts choose *Pay from*: **Equity money** (the usual payout account) may not use MCX money; a larger request is refused with the amount to move first. **MCX money** is paid from the first MCX-only account and uses the MCX money first, then equity money, within what the client may withdraw. A payout the client asks for on the portal is equity money.

**Turning it off** is refused while any client has MCX money earmarked: move it to equity on RMS first.

Good to know: the earmark is a figure, not a posting. Money already in the ledger when you switch it on is equity money; earmark a client's existing MCX money with a move to MCX (see *Move money between MCX money and equity money*).

## mcx-earmark-move
title: Move money between MCX money and equity money (one ledger)
screen: rms
roles: Risk manager, Ops maker, Ops checker
ask: move mcx money to equity; move equity money to mcx; release mcx earmark; use mcx money for equity; mcx profit to equity with one ledger; payout refused mcx money; equity payout refused earmarked; earmark opening mcx balance
related: mcx-only-bank-account, payout

1. In the client drawer, under the balance, click **Move MCX money to equity** or **Move equity money to MCX** (or **Move** on the client's line on [[rms|RMS & limits]]), give the amount and a note.
2. A second person, an Ops checker or Risk manager, approves it under RMS → **Moves between MCX money and equity money** (or in the drawer). The person who asked cannot.
3. It counts from its date: the next Finspot BOD limit file and NSE Clearing allocation carry it.

Nothing is posted: the client's ledger, statement and balance do not change; only which side the money counts for. A move cannot take more than the side it comes from has. Every request and decision is in the audit trail.
