Ledgy help articles: client money kept back from the clearing corporation and its reason codes in SEGMON, the rules on the firm's bank accounts (names, reporting to the exchanges, the bank undertaking, @valid UPI IDs), paying a client by cheque when the transfer failed, client-fund FDRs, and journals on client ledgers.

# Client money rules

## retained-funds
title: Client money kept back: reason codes RC01 to RC09
screen: clientmoney
roles: Ops maker, Ops checker, Compliance officer
ask: retention reason code; rc01; rc09; reason code column 52; justification code for retained funds; funds received after cut off; segmon retained by tm; why was client money not upstreamed; record retained funds; fdr in transit; mfos in transit; ofs refund retained; bank failure retained; set the upstreaming cut off time; suspe1234n; suspense ucc in segmon; unidentified receipts in segmon; early pay-in of funds column 46
related: segmon-report, client-money-recon, firm-bank-accounts, payout-cheque

Client money goes to the clearing corporation every day. Any client money kept back is reported in the daily client collateral file (SEGMON) **by reason**: one line per reason code, with the code in column 52, the amount in column 22 and the client fields blank (NSE Clearing NCL/CMPL/61186 of 18 March 2024).

On [[clientmoney|Client money reconciliation]] → **Retained funds**, choose the day. Ledgerline lists each reason with its amount and where it comes from:

- **RC01** money a client paid after the firm's cut-off. It first meets the client's debit; only what is left over is retained. If it does not cover the debit, nothing is retained (NCL/CMPL/76318, FAQ 5). Ledgerline reads the time from the bank statement credit.
- **RC02** client-fund FDRs lien-marked to the clearing corporation and not yet deposited with it (at most five settlement days).
- **RC04** payouts sent to the bank and not yet confirmed, and funds the clearing corporation released for a client's payout (payout holds).
- **RC05** cheques issued to clients and not yet cleared (at most five settlement days).
- **RC08** funds held for payouts on a running account settlement day.
- **RC09** anything else: client credit not allocated to the clearing corporation with no reason, and anything kept past its time limit. RC09 is a warning: upstream the money, or record the reason.

**Record a reason** for what Ledgerline cannot see: MFOS in transit (RC03), an OFS refund credited back (RC06), a bank's failure (RC07, with the bank's evidence), or a payout that failed in the banking system (RC04, with the bank's evidence). Give the amount, the account it lies in and the reference. **Withdraw** an entry made in error, with the reason.

**Cut-off.** Set the firm's upstreaming cut-off once (Compliance officer), for example 18:00. Without it, no money can be told apart as received after the cut-off.

**In the SEGMON file**, Ledgerline also:

- reports unidentified receipts in the bank suspense account on a line of their own under the back-office code **SUSPE1234N**, with the firm's PAN as the client PAN (NCL/CMPL/64088). The code is never a client: Ledgerline refuses it as a client code, so it never reaches the exchanges' UCC database or the front end;
- reports a client's offer-for-sale margin, which is with NSE Clearing at the end of the day, as cash placed with NSE Clearing (column 46), not as retained;
- warns when the cash allocated to a client includes money paid after the cut-off.

The self-inspection test *Client money upstreamed every day* fails on any day with money under RC09.

## firm-bank-reporting
title: Report the firm's bank accounts: names, 7 working days, undertaking, @valid UPI ID
screen: clientmoney
roles: Compliance officer
ask: report new bank account to exchange; bank account opened report within 7 working days; report closure of bank account; bank account nomenclature; uscnb account name; dscnb account name; settlement account name; bank undertaking addendum; sebi check; valid upi id for bank account; neft into account without valid upi; overdraft account client money; las account; bank account reported on
related: firm-bank-accounts, client-money-recon, retained-funds

Each of the firm's bank accounts on [[clientmoney|Client money reconciliation]] → **Firm bank accounts** carries what the rules ask of it:

1. **Name.** The USCNBA, DSCNBA and settlement account carry SEBI's names: *<firm's name> - USCNB account*, *<firm's name> - DSCNB account* and *<firm's name> - Settlement Account* (SEBI master circular 2025/90, para 15.3). Enter the name as the bank has it; a name that does not follow the rule is refused. No name is prescribed for the own account.
2. **Opened on.** The day the bank opened the account. A new account is reported to **each exchange** within **7 working days**, and a closed one within 7 working days of closing (para 15.4.3 as amended in March 2026; NSE/INSP/73976 item 8.2). Record the day you reported it to each exchange under **Reported to**.
3. **Bank undertaking.** For a USCNBA, DSCNBA or settlement account, the day the bank's undertaking addendum was submitted, within 7 working days of opening (NSE/INSP/77149).
4. **@valid UPI ID.** Every account clients pay into (a USCNBA, or any account ticked *given to clients*) is linked to a validated @valid UPI ID, so clients can verify it through SEBI Check (NSE/COMP/75700). Record the ID on the account, or give the account number on the ID in Settings → UPI collection.
5. **Overdraft or loan account.** Tick it for an OD, cash credit or loan-against-securities account. Such an account is never client money and is left out of the client funds check; a USCNBA, DSCNBA or settlement account cannot be one.

Each morning the automation *Client money rules* opens a work item for every report or undertaking still to record, due on its seventh working day, and for every account clients pay into without an @valid UPI ID. A client's bank transfer into such an account opens a work item too. The items close by themselves once the dates are recorded. The self-inspection test *Firm bank accounts named, reported to each exchange and verifiable by clients* lists anything missing or late.

**Taking funds back from the clearing corporation.** A transfer *Collateral released by the clearing corporation* says why, from SEBI's list (SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2023/84, para 3.C.IX): the client's unpaid securities or MTF obligation, losses on selling them, penalties, statutory levies, brokerage, other charges (these come to the settlement account), or running account settlement and clients' withdrawal requests (these come only to the DSCNBA).

## payout-cheque
title: Pay a client by cheque when the bank transfer failed
screen: money
roles: Ops checker, Ops maker
ask: pay by cheque; pay the client by cheque; neft failed pay by cheque; payout failed issue cheque; physical instrument payout; cheque to client not cleared; uncleared cheque issued to client; cheque not cleared in 5 days; upstream on sixth settlement day; settlement date for cheque payment; date of credit to client; stop cheque; cancel payout cheque
related: payout, retained-funds

Clients are paid electronically to their registered bank account. Only when that transfer **fails** may the firm pay by a physical instrument, and then the day the money reaches the client counts as the settlement date (SEBI master circular 2025/90, paras 15.10.1.4 to 15.10.1.6).

1. On [[money|Receipts & payouts]], a payout the bank returned shows as *Failed*. An Ops checker clicks **Pay by cheque**, enters the cheque number and the day it was issued. The client's ledger is debited that day; the cheque is drawn on the DSCNBA.
2. When the client's bank credits it, click **Cleared** with the day of credit. That day is the payout's settlement date, and the self-inspection's one-working-day test counts from it.
3. A cheque not cleared within **five settlement days** of issue has its funds upstreamed to the clearing corporation on the **sixth** (NCL/CMPL/61186, instruction 7). On the fifth day a work item asks for it: stop the cheque with the bank and click **Cancel**. The payout is reversed, the money is back in the client's credit, and it goes up with the day's allocation.

Until it clears, the money is reported in SEGMON as retained under RC05; after five settlement days it is RC09.

## client-fdr
title: Fixed deposits made from client money
screen: deposits
roles: Ops checker, Compliance officer
ask: client fdr; fdr from client funds; upstream client funds as fdr; fdr tenor one year one day; fdr lien marked to clearing corporation; fdr used for bank overdraft; fdr from uscnba; fdr in transit; rc02
related: clearing-deposits, retained-funds

Client money may be upstreamed as a fixed deposit, under SEBI's conditions (SEBI master circular 2025/90, paras 97.5 and 97.6): made only from the **USCNBA**, with a bank the clearing corporation accepts, for at most **one year and one day**, principal protected and pre-terminable on demand, **lien-marked** to the clearing corporation, and never used for any funded or non-funded bank facility.

On [[deposits|Clearing deposits]], record it as a fixed deposit and tick **Made from client money**. Choose the USCNBA it was funded from, give the issue and maturity dates, and confirm it is principal protected and pre-terminable. Ledgerline refuses one that breaks a condition. Give the day the clearing corporation took it; until then it is in transit, reported in SEGMON under RC02, and must reach the clearing corporation within five settlement days.

A client-fund FDR is clients' money, not the firm's collateral: it does not count towards base minimum capital. The self-inspection test *Client-fund FDRs made only from the USCNBA, within SEBI's conditions* checks every one.

## client-journals
title: Journals on a client's ledger: charges only
screen: books
roles: Ops maker, Ops checker
ask: journal between two clients; inter client adjustment; transfer money from one client to another; journal voucher client ledger; settle client by journal; pay client by journal entry; why is my journal voucher refused; jv to client ledger
related: payout, client-money-recon

A client's running account is settled only by paying the client's registered bank account; journal entries are used only to levy or reverse charges, and no money moves between clients (SEBI master circular 2025/90, paras 23.1.1(j) and 48.3).

So a journal voucher on a client's ledger may only be set against income, expense and statutory or exchange dues (brokerage, interest, DP charges, GST, stamp duty and the like), or move money between the same client's own ledgers. Ledgerline refuses a journal that:

- moves money from one client's ledger to another's;
- sets a client's ledger against a bank, cash, suspense, the clearing corporation or any other party's account.

Pay a client through [[money|Receipts & payouts]]. The self-inspection test *No journal used to pay a client or move money between clients* looks at the last 90 days of posted journals.
