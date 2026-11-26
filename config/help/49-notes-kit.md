Ledgy help articles: the contract-wise summary at weighted average prices and the annexure of trades, remarks for margin-call close-outs and rights entitlements, the MCX last trading date, trades the exchange cancelled, electronic contract note authorisation, the account opening kit, the client master of a new demat account, the registered-post letter when the mobile and e-mail both change, the nomination pop-up, closed demat accounts that receive securities, and transmission deficiency letters.

# Contract notes, the kit and client notices

## contract-note-format
title: The contract note format: weighted average price and the annexure
screen: notes
roles: Ops maker, Ops checker, Compliance officer
ask: weighted average price on contract note; wap contract note; common contract note; single weighted average price across exchanges; exchange code 99; cmn contract note; revised contract note format; nse revised format 27 june 2025; mcx contract note format; contract wise summary; annexure of orders and trades; brokerage per unit; net rate per unit; mcx last trading date on contract note; last trading day mcx
related: contract-notes, contract-note-clauses, contract-note-remarks

Every contract note starts with a **contract-wise summary**: one line for each contract and side, with the quantity, the **weighted average price**, the brokerage per unit, the net rate per unit and the net total before levies. The trades follow as the **annexure: orders and trades**, each with its order number, trade number, time, quantity and price.

- **Equity:** trades in the same security on NSE and BSE are averaged together, as NSE's common contract note asks (NSE/INSP/64389, Circular 72/2024, in force from trade date 27 June 2025 by NSE/INSP/68618).
- **Commodities:** trades are averaged exchange by exchange (MCX/INSP/354/2025).
- **MCX contracts** show their **last trading date** from the MCX product master (MCX/CTCL/256/2026 para 2.6). MCX moves it when a holiday falls on the expiry. Load the product master each day on [[xfiles|Exchange files]].

The levies (exchange charges, STT, SEBI fee, stamp duty, GST) stay in the table by segment.

To confirm with your compliance team: the circulars' own column layouts were not at hand. The columns follow what the circulars require. The STP (IFN515) fields are your STP provider's.

## contract-note-remarks
title: Remarks on the contract note: margin calls and rights entitlements
screen: notes
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: remark on contract note margin call not met; squared off for margin shortfall remark; rms square off remark contract note; position closed margin call; rights entitlement disclaimer; re disclaimer contract note; rights entitlements lapse; buy rights entitlement; re series e1; register of rights entitlements
related: contract-note-format, kill-switch, contract-note-clauses

**Margin call not met.** When the firm closes a position because the client did not meet a margin call, the contract note says so against those trades (NSE/ISC/73908 Part A 9.7-9.8; MCX/ISD/243/2026 Part A 10.2.1). Ledgerline marks a trade when it fills a square-off sent from [[rms|RMS & limits]] that day. It matches the same trade number, or the same client, contract and side. The margin call itself is the margin shortfall message on Client messages.

**Rights entitlements.** A note with a purchase of rights entitlements (REs) starts with a disclaimer (NSE/INSP/73976 item 4.22). It says an RE only gives the right to apply for the shares, or to sell the right, before the issue closes, and that an unused RE lapses. Ledgerline knows an RE from the **Rights entitlements** register on [[notes|Contract notes]]. Add each one from the exchange's notice: the symbol as the trade file has it, the ISIN and the date the issue closes. A symbol ending in -RE counts as an RE too.

## trade-cancelled-by-exchange
title: A trade the exchange cancelled (RTCM, SGTCM)
screen: notes
roles: Ops checker
ask: exchange cancelled trade; trade cancellation rtcm; reversal trade cancellation mechanism; sgtcm cancelled trade; remove trade from contract note; cancelled trade obligation; trade cxl; exchange reversed a trade
related: contract-notes, correct-bill

When an exchange cancels a trade (the reversal trade cancellation mechanism, SGTCM, or an exchange message), an Ops checker records it on [[notes|Contract notes]] → **Trades cancelled by the exchange**. Give the trade date, exchange, trade number, where it came from and the exchange's reference.

Ledgerline then:

1. leaves the trade out of obligations, bills, margins and contract notes;
2. corrects the client's bill and sends a revised contract note that gives the reason. If that cannot be done, you get a work item to do it on Billing.

Ledgerline does not read the cancellation from the trade file's status column. The exchange's cancellation codes were not in the text at hand, so the record is made by hand from the exchange's message.

## ecn-authorisation
title: Electronic contract notes: the client's authorisation and e-mail
screen: notes
roles: Ops maker, Ops checker, Compliance officer
ask: electronic contract note authorisation; ecn consent; specific authorisation for ecn; client opted for physical contract note; printed contract notes; ecn signed by client; change of email for contract notes; email change letter; client email on firm domain; email created by broker
related: dispatch-register, account-kit, change-contact

Contract notes go by e-mail only to a client who authorised it specifically and in writing (SEBI master circular for stock brokers paras 23.2.1 and 52.3).

- **Online applicants** see a separate, optional tick for electronic contract notes on the review step. It is printed in the e-Signed form. Without it, notes go by post.
- **Others:** first add the client's signed authorisation as a client document. Then record it on [[notes|Contract notes]] → **Electronic contract note authorisation**, or switch the client to printed notes there.
- **E-mail changes** by staff need the client's signed letter attached. Otherwise the client changes the e-mail through the portal's secured login.

The **ecn_authorisation** self-inspection test lists clients on e-mailed notes with no authorisation recorded. It also lists any registered e-mail on the firm's own domain. Clients migrated from an earlier system need their paper authorisation attached and recorded.

## account-kit
title: The account opening kit
screen: dispatch
roles: Ops maker, Ops checker, Compliance officer
ask: account opening kit; index of documents; mitc; most important terms and conditions; do's and don'ts guidance note; tariff sheet to client; senior officials details to client; investor grievance cell details; welcome kit; kit delivery log; rights and obligations of bo and dp
related: ecn-authorisation, dispatch-register, client-master-letter

The kit follows SEBI's master circular for stock brokers, paras 21.1-21.12.

- **Online**, the applicant accepts the MITC and the guidance note (do's and don'ts) along with the other declarations. A firm that is a DP also asks for the rights and obligations of the beneficial owner and the DP.
- **After the application**, the client is sent the **account opening kit** as a signed PDF through the [[dispatch|Dispatch register]]. It has the index of documents, the client's choices, the tariff, and the escalation matrix with SCORES and SMART ODR. A bounced e-mail goes to paper.
- **A client opened on paper:** send the kit from [[notes|Contract notes]] → **Send account opening kit**.

Publish the documents themselves (MITC, guidance note, rights and obligations, risk disclosure) on your website. The **account_kit** self-inspection test lists accounts opened in the last 180 days with no kit delivered.

## client-master-letter
title: The client master sent when a demat account opens
screen: dp
roles: Ops maker, Ops checker
ask: client master to bo on account opening; account opening confirmation demat; cmr to client; bo opened letter; demat account opening letter; proof of dispatch client master
related: account-kit, dispatch-register

When the firm's DP opens a demat account, the holder gets the **client master** as a signed PDF through the [[dispatch|Dispatch register]] (CDSL DPOI 2.4.21). It shows the BO ID, the holders, contacts (masked), bank, nomination, DDPI and BSDA. A bounced e-mail goes to paper with a courier reference, which is the proof of dispatch.

## contact-change-letter
title: Mobile and e-mail both changed: the registered-post letter
screen: changes
roles: Ops maker, Ops checker
ask: mobile and email changed together; registered post letter permanent address; email and mobile change kyc not compliant; acknowledgement letter by courier; dp 3.4.17
related: change-contact, ecn-authorisation

Sometimes both the mobile and the e-mail of a demat client change within 30 days, and the client's KYC is not KRA-validated. Ledgerline then raises a work item: send an acknowledgement letter by **registered post or courier to the permanent address**, not to the new contacts (CDSL DPOI 3.4.17). Close the work item with the post or courier number.

## nomination-popup
title: The nomination pop-up in the client portal and app
screen: messages
roles: Ops maker, Ops checker, Compliance officer
ask: nomination pop up; nominate pop up at login; nomination nudge; benefits of nomination message; first login of the day nomination
related: nomination-acknowledgement

A client whose demat account has no nominee (opt-outs included) sees a short message on the benefits of nominating at the first login of the day, on the web portal and the app (SEBI nomination circular of 2026, para 10.2). Accounts with a nomination do not. The twice-yearly e-mail and SMS reminders are on Client messages.

## closed-account-balances
title: Securities that reach a closed demat account
screen: dp
roles: Ops maker, Ops checker
ask: closed demat account received shares; unfrozen balance after closure; credit to closed account; one year after closure transfer; corporate action credit closed account
related: dp-statement-returned, dp-requests

Sometimes securities reach a demat account after it was closed, such as a corporate benefit or a freeze lifted. They are moved, on the holder's instruction, to the account the holder names within **one year** of the closure (CDSL DPOI 10.5.3). Ledgerline raises a work item due a year from the closure. The **closed_account_balances** self-inspection test fails for any still there after the year.

## transmission-deficiency
title: Transmission claims: telling the claimant what is missing
screen: dp
roles: Ops maker, Ops checker
ask: transmission deficiency letter; missing documents transmission; transmission claim acknowledgement; documents complete transmission; further documents transmission reason; deviation in writing transmission
related: dp-request-timelines, dp-requests

On a transmission request, record **Deficiency told to the claimant** with every missing or deficient document in the note: the claimant is told of all of them at once. A further request is accepted only with its reason in writing (start the note with "Reason:"). Record **Documents complete** with the reference of the message that told the claimant. This follows the SEBI transmission circular of 2026 (Annexure 6.1.3-6.1.4).
