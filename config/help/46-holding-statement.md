Ledgy help articles: the daily holding statement to the exchanges is the firm's own demat accounts with their NSE account types; tagging the firm's accounts in Settings.

# Holding statement

## holding-statement-firm-accounts
title: The holding statement: the firm's own demat accounts and their types
screen: settings
roles: Admin, Compliance officer, Ops checker, Ops maker
ask: holding statement; holding statement account type; firm demat accounts; tag demat account; pool account holding statement; cuspa account in holding statement; client unpaid securities account; clnt coll; own account type; why is the holding statement not made; holding statement blocked; untagged demat account; daily holding statement nse; member own client code
related: self-inspection-tests, penalty-risks

The daily holding statement to the exchanges (NSE/INSP/55039; layout and account types in NSE/INSP/55380) lists the **firm's own demat accounts**: each account and ISIN, with the account's NSE type and the firm's own client code, PAN and name. It is **not** the clients' holdings in their own accounts.

Each of the firm's accounts has one of NSE's types: **POOL**, **CM COLL**, **OWN**, **EPI**, **CLNTBENEMTF**, **TMCOLL**, **TMCOLL-MTF**, **CLNT UNPAID**, **CLNT COLL**, **MRGN PLEDGE**, **MTF PLEDGE** or **CLNTBENE**.

1. Load CDSL's statement of holding on [[xfiles|Exchange files]] each day. Ledgerline keeps the balances of the accounts in it that belong to no client, and of every tagged account. Clients' own accounts are not kept for this.
2. On [[settings|Settings]], under **Firm demat accounts**, an Admin gives each account its type and sets the firm's own client code. **Take the types from the old system's holding statement** reads the old file's accounts and types only.
3. The statement is made from the day's balances of the tagged accounts. Total, pledged and free quantities have 3 decimals; free is total less pledged.

Before anything is made or sent, Ledgerline checks:

- The day's CDSL statement of holding is loaded.
- Every account in it that belongs to no client and holds securities is tagged.
- No tagged NSDL account is left out. Ledgerline does not load NSDL balances.
- The firm's client code, PAN and name are set.
- For each account and ISIN, the statement equals the statement of holding.

If anything fails, the statement is not made or sent. A work item goes to the compliance officer saying why. Nothing is left out without warning.

Tag the pool and client unpaid (CUSPA) accounts whenever the firm has them. A member was penalised when its pool and unpaid securities accounts dropped out of the statement after it changed software. Ledgerline's first version sent clients' holdings marked OWN. That reported clients' securities as the broker's own and left out the pool and collateral accounts. It no longer does.

The self-inspection test **Firm's demat accounts tagged and in the holding statement with their type** runs the same check every night.
