Ledgy help articles: AMC refunds on closure, death intimations received directly, CDSL KYC deficiencies, OTP transfer-cum-closure requests, reason-27 unfreezes, the nomination opt-out declaration, PoA and DDPI terms, the joint-holder freeze clause, blocked clients' pending orders, bulk deals, self and wash trades, abnormal trading patterns and BSE's CCM penalty file.

# DP and trading controls (round 6)

## amc-refund
title: Refund of unused AMC when a demat account closes
screen: dp
roles: Ops checker
ask: refund amc on closure; pro rata amc refund; annual maintenance refund account closed; amc collected in advance refund; amc refund on transfer of account; unused quarters amc; credit note for amc refund
related: dp-request-timelines, dp-tariff-plans

When an account on a plan that bills maintenance **a year in advance** closes (or is transferred and closed) before the year ends, the unused part is refunded. Ledgerline counts whole quarters of the account's maintenance year. The quarter in which the account closes is used; the quarters after it are refunded.

Each evening the DP controls on [[dp|Demat accounts & DIS]] list the refunds due, worked out from what the account was billed for the year (or the plan's charge when the bill is not in Ledgerline). An **Ops checker** approves it: the refund and its GST go back to the client's ledger, and a credit note is issued. Or the checker rejects it with the reason. A self-inspection test flags refunds not decided within 30 days.

## death-intimation-direct
title: A death intimation received directly (not from the KRA)
screen: deceased
roles: Ops maker, Ops checker, Compliance officer
ask: nominee informed death of holder; legal heir sent death certificate; death intimation received at branch; freeze demat on death; holder deceased freeze debit; record death intimation; verify death certificate online; death certificate signed and stamped; demise intimation not on kra list; deceased reason code freeze
related: deceased-clients, dp-request-timelines

On [[deceased|Deceased clients]], **Death intimation received** records who sent it, their relation, the date and the death certificate's number. The same case opens as for the KRAs' list: the client's debits are blocked at once, and the UCC closure and transmission steps follow.

For each of the client's demat accounts with the DP, a **debit freeze request** is raised. A checker approves it on DP requests, and the DP desk sets the freeze in CDAS with CDSL's holder-deceased reason code.

Then record the **certificate check**: where it was verified (the issuing government site, the registrar of births and deaths, or the original seen), and that the copy kept is **signed and stamped** by the person who checked it. A work item asks for it, and a self-inspection test flags a case without it after 2 working days.

## cdsl-kyc-deficiency
title: CDSL reports a BO's KYC deficiency
screen: dp
roles: Ops maker, Ops checker, Compliance officer
ask: cdsl kyc deficiency; kyc deficient bo cdsl instruction; freeze reason code 20; inform bo kyc deficiency letter email phone; 30 days to rectify kyc; freeze after 30 days kyc; acknowledgement to cdsl kyc freeze
related: kyc-trading-gate, dp-requests

On the DP controls, **KYC deficiency** records CDSL's instruction: its reference, the account, the deficiency and the freeze CDSL asks for. Then:
1. Tell the BO by **letter, e-mail and phone**, and record each with its reference (dispatch proof, e-mail log, call note).
2. The BO has **30 days** from the last of them to rectify. Record **Rectified** when it is done.
3. If not, **Freeze** raises the freeze request under **reason code 20**. A checker approves it and the DP desk sets it in CDAS.
4. Record that CDSL was informed and the acknowledgement sent.

## otp-closure
title: Transfer-cum-closure requests clients start with an OTP
screen: dp
roles: Ops maker, Ops checker
ask: client closed account online otp cdsl; otp transfer cum closure; approve closure request in cdas; closure request pending in cdas; 2 working days closure approval; reject online closure
related: dp-request-timelines

A client can ask CDSL for a **transfer-cum-closure** with an OTP. The request then waits in CDAS for the DP, which approves or rejects it within **2 working days** of receipt. On the DP controls, a maker enters each request seen in CDAS (account, target BO ID, date received). A checker who did not enter it checks the BO ID, the target account and the holdings, makes the decision in CDAS and records it here. A closure the account cannot take (a pending demat, a freeze, a pledge) is rejected. A work item carries the due date, and a self-inspection test flags late decisions.

## kyc-unfreeze-27
title: Unfreeze reason-27 accounts once the KRA validates the KYC
screen: kycgate
roles: Ops maker, Ops checker, Compliance officer
ask: unfreeze reason 27; kyc validated unfreeze demat; kra validated still frozen; lift kyc freeze cdsl; unfreeze after kra validation
related: kyc-trading-gate, cdsl-kyc-freeze

CDSL freezes accounts whose KYC is not KRA-validated under reason 27. When every holder's KYC is validated, the account is unfrozen by CDSL's prescribed process. The DP controls list such accounts. **Raise unfreezes** raises an unfreeze request for each; a checker approves it, and the DP desk follows CDSL's process in CDAS. A work item is raised each evening for an account still waiting.

## nomination-optout
title: Opt-out declaration before a demat account opens
screen: dp
roles: Ops maker, Ops checker
ask: opt out of nomination declaration; client does not want nominee; no nominee demat account opening; nomination or declaration required; demat account cannot be opened nomination; signed opt out form; record declaration of non nomination
related: nomination

A demat account opens only with a **nomination** or a **signed opt-out declaration**. A client added offline with no nominee is not treated as opted out: the CDSL account-opening request shows a problem until one of them is on record. Record the signed declaration on the DP controls (**Opt-out declaration**: the date, how it was signed and the reference of the copy kept). It goes in the register of nominations and the client gets the acknowledgement.

## poa-terms
title: Check a PoA or DDPI document's terms
screen: dp
roles: Ops maker, Ops checker, Compliance officer
ask: poa terms checklist; ddpi document check; poa not assignable; poa stamped; poa in name of broker not employee; poa destination accounts; return of securities received in error poa
related: dis

Each account with a PoA or DDPI has its document checked against the stock broker master circular (36.8.1). A maker ticks what it meets:
- it names the demat accounts it operates;
- its destination accounts are only the client's own or the broker's settlement accounts;
- it is in the name of the registered stock broker, not of an employee;
- it cannot be assigned;
- it is duly stamped;
- it requires securities or money received in error to be returned.

A term not met needs a note (for example, a fresh document asked for). A checker who did not tick it approves. The self-inspection lists accounts with no approved check.

## ro-joint-freeze
title: Joint holders: a statutory freeze on one freezes the account
screen: practices
roles: Compliance officer, Admin
ask: joint account statutory freeze clause; freeze of one joint holder; rights and obligations joint holders freeze; unfreeze share of other joint holders
related: dp-request-timelines

CDSL asks DPs to say, in the Rights and Obligations document, that if an authority freezes the account of one joint holder, the whole account is frozen, and the other holders need a specific order to release their share. Answer **Joint holders: statutory freeze clause** in Settings → Firm practices once the firm's document has it. The Rights and Obligations e-mail to a client with a joint account carries the clause.

## order-cancel
title: Cancel a blocked client's pending orders
screen: rms
roles: Risk manager, Ops checker
ask: cancel pending orders when client blocked; kill switch pending orders; blocked client open orders finspot; cancel orders finspot api; orders still pending after block
related: kill-switch, trade-freeze

A block (the kill switch, a deceased client, a sanctions case) stops fresh orders at the front end, but orders already pending stay until cancelled. Finspot's admin API has no call to list or cancel a client's orders, so the cancellation is done on Finspot.

Each block opens an item on [[rms|RMS & limits]] with a due time: 15 minutes in trading hours, before the next session otherwise. **Orders cancelled** records Finspot's confirmation (or "No pending orders") in one click. A second person checks the client's order book and confirms. A client's own freeze request keeps its own steps.

## bulk-deal-check
title: Bulk deals: trades above 0.5% of listed shares
screen: surv
roles: Compliance officer, Ops checker
ask: bulk deal 0.5 percent; trade more than half percent of shares; bulk deal disclosure to exchange; shares outstanding upload; listed shares of company; bulk deal within one hour of close
related: dp-surveillance-rules

A client's (or the firm's own) trades in a scrip on one exchange in a day above **0.5%** of the company's listed equity shares are disclosed to the exchange: a single trade at once, the day's total within an hour of the close. No exchange file Ledgerline reads carries the number of listed shares, so load a **shares outstanding** file (headings ISIN, Shares; optional Symbol, As of) on Surveillance & AML. The check runs with **Run internal rules** and the evening autopilot. It raises an alert and a work item due at 16:30, and lists the traded scrips with no number on record.

## wash-trades
title: Self, wash and abnormal trades
screen: surv
roles: Compliance officer, Ops checker
ask: self trade; wash trade; same client buy and sell same time; same pan different client codes trades; artificial volume; profit loss transfer; illiquid scrip concentration; expiry day options pattern; synchronised trades; reversal trades
related: dp-surveillance-rules, surveillance-thresholds

With **Run internal rules**, Ledgerline looks at the firm's own trades for:
- the same client buying and selling a contract within a few minutes, near-equal in quantity and price, and the same across client codes of **one PAN**;
- trades far from the day's close (a possible profit or loss transfer);
- the same quantity bought and sold in a scrip on several of the last 10 trading days (possible artificial volume);
- large trades in scrips on the firm's illiquid list (the scrip group **ILLIQUID**);
- large expiry-day option round trips.

Each is an alert; examine it and record the finding in the case. The trade files carry no counterparty, so trades matched against **another member's client** are seen only in the exchange's alerts. The compliance officer can change the thresholds.

## bse-ccm-penalty
title: BSE's client code modification penalty file
screen: surv
roles: Compliance officer, Ops checker
ask: bse ccm penalty; client code modification penalty file; penalty on modified trades bse; ccm penalty 1 percent 2 percent
related: ccm

Load BSE's CCM penalty file on Exchange files. BSE has not published its columns, so Ledgerline reads them by their headings (trade date, trade number, original and modified client codes, value, penalty); the layout is unconfirmed. Each modification is matched with the CCM register. One BSE penalised that the register lacks is a high alert. The month's total is a work item for the compliance officer to book and review.
