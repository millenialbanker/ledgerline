Ledgy help articles: the firm's chart of accounts kept as heads the way Tally keeps it, the account search box on every account field, and the keys for entering vouchers.

# Chart of accounts and vouchers

## chart-of-accounts
title: The chart of accounts: heads, sub-heads and accounts
screen: books
roles: Ops maker, Ops checker, Compliance officer, Admin
ask: chart of accounts; heads; groups; tally groups; account head; sub-head; sub group; primary group; level 1; level 2; major head; add a head; rename a head; reorder heads; remove a head; move an account to another head; where is my account; capital under liabilities; sundry debtors; sundry creditors; clients with debit balances; trial balance by heads; drill down; account code; ledger code
related: account-search, voucher-keys, vouchers-gst

The chart of accounts is on [[books|Firm accounts & GST]] → **Chart of accounts**. It is kept in heads, as in Tally:

- **Level 1** is fixed: Assets, Liabilities, Income and Expenditure. Capital and reserves sit under Liabilities.
- **Level 2** are the major heads every firm starts with, such as Own bank accounts, Duties and taxes, Brokerage, Staff costs. An Ops checker can rename them, add new ones (**+ Head**) and change their order (the arrows).
- **Sub-heads** are optional. Add one under a head with **+ Sub-head**, or even under another sub-head. You can also put accounts straight under a level-2 head.
- **Accounts** sit under any head. Click an account to open its ledger, then a line of the ledger to open its voucher.

Every account Ledgerline posts to by itself (bank, clearing, GST, STT, stamp duty, brokerage, interest, DP charges, salaries and the rest) starts under its usual head, so the books add up exactly as before.

**Clients.** Each client is an account under Clients. On the balance sheet and the trial balance a client in debit is shown under *Clients with debit balances* (Assets) and a client in credit under *Clients with credit balances* (Liabilities). **List clients** shows them, biggest first.

**New account.** Give a name, an optional short alias and the head. Ledgerline makes the account's code itself; you never type one. Names are unique in the firm (capitals don't matter). Codes the firm used before keep working.

**Moving an account to another head** needs two people. Click **Move**, choose the head and say why. Another Ops checker approves it under *Moves waiting for approval*. The account keeps its ledger and balance; only where it is shown changes. It can move only to a head under the same level 1 (an expense stays under Expenditure). Both steps are in the audit trail.

**Removing a head.** A head can go only when nothing is under it: no accounts, no sub-heads and no move waiting for it. Heads where Ledgerline's own accounts go can be renamed but not removed.

**Reports.** The **Trial balance**, **Profit & loss** and **Balance sheet** tabs open from level 1 to the level-2 heads, the sub-heads and the accounts. Click a head to open it, or use **Open all**. The trial balance shows each account in its debit or credit column, so its totals are the ledger's own.

## account-search
title: Find an account by typing its name
screen: books
roles: Ops maker, Ops checker, Compliance officer, Admin
ask: account search; find an account; search ledger; account dropdown; type to search; alias; account alias; short name; client code search; ucc search; select account; pick an account; create account from voucher; new account while entering voucher; account waits for checker; pending account; account not approved; voucher will not post new account
related: chart-of-accounts, voucher-keys

Every field where you pick an account (voucher lines, the party, a vendor's usual expense account, a bill's expense account, the bank a bill is paid from) is a search box.

1. Start typing. A list opens beside the field and narrows with each letter.
2. Names that **start** with what you typed come first, then names with a **word** that starts with it, then names that have it **inside** a word. The matched letters are highlighted.
3. An account's **alias** and a client's **code** match too. Typing a client's full code puts that client first.
4. Each line shows the account's name, its head, its sub-head (if any) and its balance today (Dr or Cr).
5. Use the arrow keys and **Enter** to choose, **Esc** to close the list, or click a line.

Client accounts can be picked on journal vouchers. A journal touching a client still needs an Ops checker's approval.

**Making an account while you enter a voucher.** The last line of the list is **+ Create new account**. It opens a small form: the name (what you typed), the head (type to find it) and an optional alias. Anyone who may enter vouchers can do this. The new account waits for a checker:

- A voucher that uses it cannot post until the checker approves it.
- When the checker posts the voucher, the new account is approved in the same step. The checker can't be the person who made the account.
- When rejecting the voucher, the checker can reject the new account too, with the reason. A rejected account can't be used again.

The search is quick even with thousands of clients: balances are refreshed every 30 seconds and whenever the chart or a voucher changes.

## voucher-keys
title: Voucher entry keys (as in Tally)
screen: books
roles: Ops maker, Ops checker
ask: voucher shortcut keys; tally keys; keyboard shortcuts; f4 contra; f5 payment; f6 receipt; f7 journal; ctrl a save; esc quit voucher; enter next field; dr cr; debit credit toggle; voucher entry fast; balance the voucher; last line amount
related: account-search, chart-of-accounts, vouchers-gst

**New voucher** on [[books|Firm accounts & GST]] works with the keyboard as Tally does. The keys are shown at the bottom of the screen.

| Key | What it does |
|---|---|
| **F4** | Contra |
| **F5** | Payment |
| **F6** | Receipt |
| **F7** | Journal |
| **Enter** | Next field |
| **D** or **C** | Make the line Dr or Cr (Space or the arrows switch it) |
| **↑ ↓** then **Enter** | Choose an account in the list |
| **Ctrl+A** | Save the voucher and send it for approval |
| **Esc** | Close the list; otherwise leave the voucher (you are asked first if you typed anything) |

How a voucher goes:

1. Date, branch, party and cheque or UTR, each followed by **Enter**.
2. Each line: Dr or Cr, the account, the amount. When the lines don't balance yet, **Enter** after an amount goes to the next line with the side it needs, and its amount is filled in with the difference.
3. When the lines balance, **Enter** goes to the narration. **Enter** on an empty account line also ends the lines.
4. **Ctrl+A** saves. An Ops checker who didn't make the voucher posts it.

A payment credits one bank or cash account, a receipt debits one, and a contra only moves money between the firm's bank and cash accounts.
