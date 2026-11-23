Ledgy help articles: the daily holding statement to the exchanges is the firm's own demat accounts with their NSE account types and security types; tagging the firm's accounts in Settings; nil days, special sessions and corrections.

# Holding statement

## holding-statement-firm-accounts
title: The holding statement: the firm's own demat accounts and their types
screen: settings
roles: Admin, Compliance officer, Ops checker, Ops maker
ask: holding statement; holding statement account type; firm demat accounts; tag demat account; pool account holding statement; cuspa account in holding statement; client unpaid securities account; clnt coll; clntbene-mtf; clntbenemtf; mrgn pledge; mtf pledge; margin pledge in holding statement; invoked pledge holding statement; own account type; security type; securityType; mutual fund units holding statement; bonds holding statement; commodity in holding statement; why is the holding statement not made; holding statement blocked; untagged demat account; daily holding statement nse; member own client code
related: holding-statement-nil-days, holding-api, self-inspection-tests, penalty-risks

The daily holding statement to the exchanges (NSE/INSP/55039; layout and account types in NSE/INSP/55380) lists the **firm's own demat accounts**: each account and ISIN, with the account's NSE type, the security type and the firm's own client code, PAN and name. It is **not** the clients' holdings in their own accounts.

Each of the firm's accounts has one of NSE's types: **POOL**, **CM COLL**, **OWN**, **EPI**, **CLNTBENEMTF**, **TMCOLL**, **TMCOLL-MTF**, **CLNT UNPAID**, **CLNT COLL**, **MRGN PLEDGE**, **MTF PLEDGE** or **CLNTBENE**. Settings shows what each type is for.

1. Load CDSL's statement of holding on [[xfiles|Exchange files]] each day. Ledgerline keeps each day's balances of the accounts in it that belong to no client, and of every tagged account. Clients' own accounts are not kept for this.
2. On [[settings|Settings]], under **Firm demat accounts**, an Admin gives each account its type and sets the firm's own client code. **Take the types from the old system's holding statement** reads the old file's accounts and types only.
3. The statement is made from the day's balances of the tagged accounts. Total, pledged and free quantities have 3 decimals; free is total less pledged.

**Margin pledges are not reported.** NSE's FAQ on margin collection says securities clients pledge as margin are left out of the holding statement. They stay in the clients' own accounts, so Ledgerline never reports them. What the firm's own accounts hold is reported: the pool, early pay-in, CUSPA and own accounts, and securities from an invoked pledge. An invoked pledge moves the securities into the firm's margin pledge account, so that account (MRGN PLEDGE, or CLNT COLL as some systems tag it) is reported for what it holds.

**Security type** comes from CDSL's ISIN master: EQ, MF, BOND, DEBT, PREF, IDR, WT or COMM. Load the ISIN master on Exchange files. An ISIN missing from it is sent as MF (mutual fund ISINs), BOND (government securities) or EQ, and a work item asks you to check it. REIT and InvIT units go as EQ: NSE's list has no type for them. A commodity row also carries the commodity's name and unit.

**CLNTBENEMTF** is spelt as in NSE's API specification, which NSE's API checks against. NSE's Inspection master circular prints CLNTBENE-MTF. Ledgerline reads either when tagging. If NSE rejects an MTF account with code 902, the work item says so: ask NSE which spelling its API takes.

Before anything is made or sent, Ledgerline checks:

- The CDSL statement of holding for the day is loaded.
- Every account in it that belongs to no client and holds securities is tagged.
- No client's own account is tagged with a type other than OWN.
- No tagged NSDL account is left out. Ledgerline does not load NSDL balances.
- The firm's client code, PAN and name are set.
- For each account and ISIN, the statement equals the statement of holding.

If anything fails, the statement is not made or sent. A work item goes to the compliance officer saying why. Nothing is left out without warning.

Tag the pool and client unpaid (CUSPA) accounts whenever the firm has them. A member was penalised when its pool and unpaid securities accounts dropped out of the statement after it changed software.

The self-inspection test **Firm's demat accounts tagged and in the holding statement with their type** runs the same check every night, including the security types.

## holding-statement-nil-days
title: Holding statement: nil days, Sunday sessions and correcting a past day
screen: xfiles
roles: Compliance officer, Ops checker, Ops maker, Admin
ask: nil holding; nil holding declaration; one time nil declaration; bulk nil holding; bulk nil reporting; no holdings holding statement; holding statement sunday; muhurat trading holding statement; special session holding statement; holding statement saturday; holding statement holiday; correct holding statement; resubmit holding statement; holding statement rejected past day; delete holding statement file; old cdsl holding file
related: holding-api, holding-statement-firm-accounts

**Which days.** A statement is due for every day except Sunday, by the next day. A Saturday or a holiday uses the balances of the last trading day, unless CDSL sent a statement of holding for that day. A Sunday with a special session (for example Muhurat trading) is due too. List these Sundays in the holding statement panel on [[xfiles|Exchange files]] (**Change**, Compliance officer or Admin). A Sunday with exchange trades in Ledgerline counts by itself.

**Nil days.** When the firm's tagged accounts hold nothing on a day, nothing is sent by API (NSE/INSP/60046):

1. By the next day, make the one-time **NIL Holding declaration** on NSE's member portal: Statement upload → Holding Statement Daily → Submit Data → Submission in the capacity of Trading member → Not Applicable Declaration → NIL Holding Declaration.
2. In the holding statement panel, click **Record the NIL declaration** with the portal's acknowledgement. It covers the following nil days until the next statement.
3. When holdings come back, first report every nil day on the portal (Statement upload → Holding Statement Daily → **Bulk Nil Holding Reporting**). Then click **Record the bulk nil report** with the acknowledgement.
4. Only then does Ledgerline send the next day by API. Until the bulk report is recorded, that day shows *Not sent* with the reason.

A work item reminds the compliance officer at each step. Not declaring in time counts as not submitting the holding statement.

**Correcting a past day.** NSE takes a corrected day only as a whole. Ledgerline keeps each day's balances of the firm's accounts, so a past day can be made and sent again at any time: choose the date and **Submit to NSE now**. Each submission gets the next file number. If NSE refuses the new file as a repeat (code 516), delete the earlier file on NSE's member portal and submit again.

If a day's CDSL statement of holding was never loaded, load it on Exchange files, even after later ones. An older file only adds that day's balances of the firm's accounts. Clients' holdings stay as they are.
