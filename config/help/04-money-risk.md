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
    - **Bank credit notifications** for your own bank's collection service: HDFC e-CMS, Axis Power eColl, ICICI e-Collection, Kotak, Yes Bank and others. Banks give their message format only to their customers, so ask your bank's cash management team for the "real-time credit notification (push)" specification. Then fill in where the amount, UTR, virtual account and payer's account are in the bank's message, and how the bank signs it.
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
roles: Ops checker
ask: payout file; bank payout file; neft file; release payouts; payout utr; payout failed; bank result
related: payout

1. On [[money|Receipts & payouts]], download the **Bank file** of approved payouts (Autopilot prepares it at the cut-off).
2. Upload it on the bank's corporate banking.
3. When the bank responds, click **Enter bank result**: the UTR for each paid item, or mark it failed. Failed payouts go back to the client's ledger.

## ras
title: Run the running account settlement (RAS)
screen: ras
roles: Ops checker
ask: running account settlement; ras; quarterly settlement; monthly settlement; settle client funds; return idle funds
related: payout

SEBI requires unused client funds to be returned monthly or quarterly, as each client chose.

1. Open [[ras|Running account]] on the settlement date. It lists each client's amount to return, keeping the allowed buffer.
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
3. A new vendor is added from the bill in one click (**Add vendor**). Set the **TDS section**, payment terms, and the **Udyam number** for a micro or small enterprise: their bills must be paid within the agreed days, at most 45 (15 without an agreement), or the expense is disallowed until paid (section 43B(h)).
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
roles: Risk manager, Ops checker
ask: peak margin; margin penalty; short collection penalty; penalty slab; why penalty; margin reporting; snapshot
related: margin-shortfall

On [[margin|Margin reporting]]:

- Peak margin is rebuilt from trade timestamps at each clearing snapshot time.
- Collection = the client's funds before the day's bills + UPI blocks + usable collateral.
- **Penalty:** 0.5% if the shortfall is under ₹1 lakh **and** under 10% of the requirement, otherwise 1%.
- Penalties are posted monthly (Risk manager or Ops checker); passing them to clients follows your Settings.

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
ask: bod file; limit file; oms limits; trading limits not updated; holdings file to oms; start of day file; t1 holdings
related: client-cant-trade, debit-interest

On [[rms|RMS & limits]], download:

- **BOD limit file:** each client's opening limit, for the trading system.
- **BOD holdings file (with T1):** sellable holdings including unsettled T1 shares.

Load both into the trading system before the market opens. Autopilot prepares them in the start-of-day stage.

## kill-switch
title: Stop a client's trading at once
screen: rms
roles: Risk manager
ask: kill switch; stop client trading; freeze client; block trading immediately; suspicious trading stop
related: margin-shortfall, surveillance

On [[rms|RMS & limits]], use the kill switch on the client's row. Their limit drops to nil immediately. Only a Risk manager can do this, and it's recorded with the reason.
