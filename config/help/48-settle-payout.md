Ledgy help articles: settlement under interoperability (BSE trades cleared at NSE Clearing netted with NSE trades), internal shortages and NSE Clearing's auction, the pay-out reconciliation and its deadlines, intraday short allocation, the allocation accepted by 22:00, transfer requests and account closures.

# Settlement and pay-out

## netted-obligations
title: Why a client's BSE sale and NSE purchase show as one obligation
screen: settlement
roles: Ops maker, Ops checker, Risk manager
ask: bse and nse netted; interoperability netting; client sold on bse bought on nse; obligation netted across exchanges; why is the exchange nse+bse; false short delivery; same isin both exchanges; netting group; cleared through ncl
related: settlement, internal-shortages, early-payin-lapse

When two exchanges' cash markets are cleared at the same clearing corporation (Settings → **Cleared through**: BSE trades cleared at NSE Clearing under interoperability), NSE Clearing nets each client's trades in a security across both exchanges. Ledgerline does the same on [[settlement|Settlement]].

- A client who sold 100 on BSE and bought 60 on NSE has one obligation: a sale of 40. The exchange column shows **BSE+NSE**.
- The obligation is short, sent for early pay-in or put in CUSPA only on the net. A sale fully netted by a purchase needs nothing.
- A short seller's internal shortage can be hidden by a buyer on either exchange.
- An exchange cleared by another clearing member nets on its own.

The **Cash market obligation** reconciliation on [[ccdata|Clearing data]] compares NSE Clearing's obligation with every trade NSE Clearing clears for the firm, BSE trades included.

## internal-shortages
title: Fund internal shortages by noon for NSE Clearing's auction
screen: settlement
roles: Ops checker, Ops maker, Risk manager
ask: internal shortage; cm_sa_s; cm_sa_dtls; valuation amount; settlement price plus 20; fund by 12 noon; internal shortage auction; buyer gets auctioned shares; close-out rate; auction rate; auction difference; short seller profit; no gain from short delivery
related: settlement, netted-obligations, clearing-data

An internal shortage is a client's short delivery that another client's purchase of the same security hides, so the firm's net obligation shows nothing short. NSE Clearing auctions it.

1. On the settlement day's morning Ledgerline works out the internal shortages from the day's obligations and raises a work item: **Keep ₹… in the settlement account by 12:00**. The amount is the settlement price plus 20% for each share short.
2. Load NSE Clearing's **CM_SA_S** report (and **CM_SA_DTLS**, client-wise) on [[xfiles|Exchange files]] when it is out. Its amount replaces Ledgerline's estimate. The panel **Internal shortages for NSE Clearing's auction** on [[settlement|Settlement]] shows both.
3. Fund the settlement account by noon. Without the funds NSE Clearing holds no auction.
4. When the auction result comes, click **Close out** on the short delivery. Enter the **shares received in the auction** and the **auction price**, and the **close-out rate** for the rest, from NSE Clearing's rate files.

The buyers get the shares received in the auction. Only what was not received is closed out and credited to them. The short seller is debited at least the settlement price, so there is no gain from a short delivery, and never more than NSE Clearing charges.

The two reports have no published column layout: Ledgerline reads them by their header names. If a column is not picked up, add its header text as an alias in the layout (config/formats).

## payout-recon
title: Reconcile the pay-out of securities with the depository transfers
screen: ccdata
roles: Ops checker, Ops maker, Compliance officer
ask: payout reconciliation; pay-out file vs depository; direct payout mismatch; payout credited to pool; depository rejected payout; securities not reached client; 24 hours payout; pay-out to client's own demat; explain mismatch to exchange
related: clearing-data, settlement, securities-transfer-request

On [[ccdata|Clearing data]], **Pay-out of securities against depository transfers** sets each client's net purchase against NSE Clearing's direct pay-out file and the pay-out credits CDSL shows for the client's accounts. Each difference has its reason:

- **Credited to the firm's pool** or **rejected by the depository**: transfer the shares to the client within one working day. A work item is raised the evening of the pay-out.
- **Paid to an account that is not the client's**: explain to the exchange and correct the client's demat details.
- **Due and paid differ**: check the obligation and the file.
- **Short: waiting for the auction**: nothing to do until the auction.

The self-inspection test **Purchased securities reach the client's own demat account within a working day of pay-out** checks the last 10 settlement days. **Clients without a running account authorisation paid within 24 hours** checks that their free credit is paid out by the next working day.

## securities-transfer-request
title: Transfer a client's securities on request
screen: settlement
roles: Ops maker, Ops checker
ask: transfer securities on request; client wants shares transferred; release shares to client demat; securities held by broker; 1 working day 3 working days; transfer request deadline
related: payout-recon, settlement

A client may ask for the securities the firm holds for them to be sent to their demat account. On [[settlement|Settlement]] → **Transfer requests**, click **Transfer request** and give the client, ISIN, quantity, where they lie and how the client asked.

- In the firm's own accounts: due in **one working day**.
- With the clearing member or clearing corporation: due in **three working days**.

A work item goes to the Ops checker. When the transfer is done, click **Done** and give the depository reference. If it cannot be met, **Reject** with the reason and tell the client.

## account-closure
title: Close a trading account within 3 working days
screen: settlement
roles: Ops maker, Ops checker, Compliance officer
ask: close account; account closure request; client wants to close account; closure within 3 working days; closure letter; closure letter held; pay out before closing; release pledges on closure; stop trading after closure request
related: payout-recon, settlement

When a client asks to close the account, record it on [[settlement|Settlement]] → **Account closures** (**Closure request**).

1. The client is blocked from trading at once and gets an acknowledgement.
2. Ledgerline checks the books. A debit balance or an open position puts the request on **Waiting for client**: ask the client to clear it.
3. Pay out the credit balance and release every pledge, unpaid-securities line and clearing allocation. Each step is dated when the books show it done. Click **Check again** after each.
4. Click **Close the account**. Every segment is closed and the closure letter goes, saying when the pay-out and the release were done.

It must all happen within **3 working days** of the request (NSE/INSP/73976 item 1.15). An overdue closure is raised as a work item, and the self-inspection test **Accounts closed within 3 working days of the request, after the pay-out** checks the last 60 days.

A segment closed another way still gets a letter, but only once the books show nothing left to pay out. Until then the letter is held and a work item says so.

## intraday-short-allocation
title: Act on intraday short allocation (SA01 to SA03)
screen: ccdata
roles: Risk manager, Ops checker
ask: intraday short allocation; sa01; sa02; sa03; peak margin snapshot short allocation; short allocation during the day; collateral short at snapshot; f_sa01
related: clearing-data, allocation-22

NSE Clearing sends a short allocation report at each peak margin snapshot (SA01 for the trading member, SA02 and SA03 for a clearing member), and from 9 November 2026 more after market hours. Load them on [[xfiles|Exchange files]] as they come.

Each client short raises a work item for the risk manager the same day. Allocate more of the client's collateral on Clearing allocation, or record the reason on [[ccdata|Clearing data]] for the end-of-day short allocation report. The section **Intraday short allocation** lists every snapshot.

## allocation-22
title: Make sure NSE Clearing accepted the allocation by 22:00
screen: allocation
roles: Risk manager, Ops checker
ask: allocation by 10 pm; allocation not accepted; 22:00 allocation; collateral allocation cut-off; ncl allocation deadline; end of day allocation not accepted; dormant client allocation; inactive client credit not allocated
related: intraday-short-allocation, clearing-data

NSE Clearing asks for the allocation of collateral to segments and clients by 22:00 (NCL/CMPT/70442). After 22:00 Ledgerline checks the day's requests. If any is unanswered, failed or not accepted, or no end-of-day batch went while allocations differ, a work item goes to the risk manager.

Without the allocation API the check is that the day's allocation file was made in Ledgerline.

Every client with funds or collateral is allocated, whatever the trading status: a dormant, suspended or closed client's credit balance is the client's money at NSE Clearing.

## early-payin-lapse
title: Early pay-ins are good for their trade day only
screen: cdslepi
roles: Ops maker, Ops checker
ask: early pay-in lapse; early pay-in not sent; epi block end of day; early pay-in more than sale; excess early pay-in; epi next day
related: netted-obligations

An early pay-in counts for its trade day only. After 18:00 an early pay-in still waiting to be sent lapses and cannot be sent later. An accepted early pay-in larger than the client's net sale (netted across the exchanges cleared together) raises a work item: get the excess back from the pool to the client.

CDSL's API moves the shares to the CM pool account; it has no block that lapses on its own, so Ledgerline does the check.
