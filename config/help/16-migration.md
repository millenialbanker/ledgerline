Ledgy help articles: moving a firm from its previous back office (migration). Format is described in internal/help/help.go.

# Migration from the old back office

## migrate-firm
title: Move a firm from its old back office to Ledgerline
screen: migrate
roles: Admin, Ops checker
ask: migrate from sharepro; migration from old back office; move from old software; how to migrate; go live from another back office; load old back office data; migration package; llmig file; upload migration package; switch from sharepro to ledgerline; bring clients and balances across
related: migration-tool, migration-backups, migrate-check, migrate-reconcile, migrate-undo, migrate-not-carried, before-ledgerline, old-documents-archive

Moving a firm is done once, at go-live, in five steps.

1. **Run the migration tool at the office** (see *Make the migration package with the migration tool*). It reads the old system's backups (SharePro first) and writes one encrypted file, the package (.llmig), with a passphrase you choose. A large office may get several parts, for example the documents of earlier years in parts of their own. Keep the passphrase apart from the file.
2. **Upload** on [[migrate|Files → Migration from old system]] (Admin). Click **Upload package** and choose the file. It goes up in pieces; if the connection drops, choose the same file again and the upload carries on from where it stopped. Upload each part the same way; the screen shows which parts are here. The largest package the firm accepts is in [[settings|Settings]].
3. **Check** it with the passphrase. Nothing changes: every file's checksum is checked, and every go-live file (client master, bank accounts, nominees, brokerage, opening balances, holdings, positions) goes through the same checks as a normal import, in the order it would load. **Every** problem is listed at once, with the file, row, field, what is wrong and what to do. Put them right in the old system, make the package again, and check again until it is clean. A package can hold several old companies: pick the one this firm on Ledgerline is; the others can be loaded into their own firms with the same package.
4. **Propose** the load (Admin): tick the files, and give the date the balances are as of (the old system's last closed day). Then a **different person** (another Admin or an Ops checker) approves it with the passphrase and the load starts: go-live files first, then the earlier years, then the documents. The screen shows the progress; if the server restarts, click **Resume** and type the passphrase.
5. **Reconcile.** When the load ends Ledgerline compares its own figures with the old system's (trial balance, clients, client balances, demat holdings, bank accounts, DP tariff plans, positions, documents). See [[migrate|the reconciliation]].

- Ledgerline refuses to load into a firm that already has its own ledger entries (it looks live), unless you confirm **This firm is not live yet**.
- A package is loaded once. Loading it again is refused unless the earlier load was undone.
- The passphrase is typed each time the package is opened. It is never stored or written to any log.

## migrate-check
title: Put right what the migration check found
screen: migrate
roles: Admin
ask: migration check errors; dry run errors; list of clients to fix; clients with details missing list; two active codes same pan; package has errors; why can't I propose the migration; client master rejected in migration; opening balances do not balance in migration; migration errors csv; fix migration errors; recommended action migration; details missing from the previous system; inactive client incomplete; dormant clients missing mobile or pan; masked bank account in migration
related: migrate-firm, file-rejected, migrate-reconcile

The check is a dry run: it loads every go-live file into a sandbox exactly as a real load would, so a bank account is checked against the clients the same package brings and a brokerage card against its scrip groups. Nothing is saved.

1. Open the package on [[migrate|Migration from old system]]. Files with problems show their count; click it to see only that file's.
2. Each line gives the **file, row, client, field, the problem and a recommended action**; "(missing)" beside the field means the old system has nothing there, rather than something not valid. **Download every problem (CSV)** to work through them in a spreadsheet; the screen shows the first 2,000. **Clients to fix (CSV)** lists the same problems one client a row (code, name, whether the client is active in the old system, and each detail to put right in plain words), with a Yes column for each kind of detail (mobile, email, PAN, bank account, IFSC, nominee, address…) so the office can filter, say, every client without a mobile. **With warnings** adds the problems that do not stop the load.
3. Correct the records **in the old system** (or the tool's settings), make the package again with the tool, upload it and check again.

- A go-live file with errors cannot be proposed. You can untick it and load it later on [[files|Other uploads]] once it is right.
- Warnings (for example a document for a client not in the client master) do not stop the load.
- **One PAN, two active codes.** The exchanges allow one active code per individual client with a trading member (NSE circular NSE/ISC/48573, June 2021; NRI, PMS and institutional clients are exempt). Where the client master has two or more active codes of an individual client with the same PAN, each one gets a warning naming the others: keep one active and mark the rest inactive, in the old system or in Ledgerline after the load.
- "The package is damaged" means a file does not match its checksum: copy the package again from the office.
- The tool's own findings and the fields it left out on purpose are listed on the same screen.

**Inactive clients with details missing.** Old records of clients who stopped trading years ago often lack details a new client must give: a blank mobile or PAN, a bank account number masked as the KRA shows it (xxxx1234), no segment, a nominee without a relationship, an address without its PIN. Those clients are still migrated, because their records, balances, holdings and documents must be kept. For a client marked **Inactive Y** in the client master, such a detail in the client master, bank accounts, nominees, UCC details or contact persons is a **warning**, not an error: the record loads as the old system had it, and the client is marked **Details missing**, listing what to complete (the clients list can show only those). The client cannot be reactivated until every one is completed: mobile, email and city with **Update contact details** on the client, bank accounts and nomination by a change request, the address, PIN and contact persons in the client's UCC details on [[uccapi|Exchange UCC]], and PAN, segments or other client-master details by loading a corrected client master row on [[files|Other uploads]]. Each detail can be completed on its own; the others stay listed until they are. A masked or missing bank account is never paid: running account settlement and payouts hold that client until the account is completed. Active clients are checked in full: the same problems are errors for them.

## migrate-reconcile
title: Read the migration reconciliation
screen: migrate
roles: Admin, Ops checker, Compliance officer
ask: migration reconciliation; trial balance does not match after migration; migration check failed; expected vs ledgerline; reconcile migration; migration totals differ; download migration reconciliation pdf
related: migrate-firm, migrate-undo, migrate-check

After the load, each total the old system showed is set against Ledgerline's: **Expected (old system)**, **Ledgerline**, **Difference** and **Pass** or **Fail**.

1. A failed line opens to show the rows behind it: the accounts, clients or holdings whose figure in the loaded file differs from Ledgerline's. Click a client to open them.
2. **Reconcile again** after a correction. Download the result as **CSV** or **PDF** for the file.
3. A total Ledgerline does not know yet is shown as **Not checked**.

- Trial-balance figures are as at the as-of date of the load.
- If the difference is in the old system's own figure (a ledger left out of the export), make the package again; if the load is wrong, undo it while nothing else has been posted.

## migrate-undo
title: Undo a migration
screen: migrate
roles: Admin, Ops checker
ask: undo migration; reverse the migration; migration loaded with the wrong date; remove migrated data; start the migration again; delete migrated history
related: migrate-firm, migrate-reconcile

1. On [[migrate|Migration from old system]], an Admin clicks **Undo migration** and says why.
2. A **different person** (another Admin or an Ops checker) approves it.
3. The opening entries are reversed and the earlier years and archived documents of that migration are taken out. Clients and other go-live records stay; a new load replaces them.

- Undo is allowed only while **nothing else has been posted** to the ledger since the load. After that, correct balances with journal entries.
- It is refused while any of the migration's documents is on legal hold.
- After an undo the same package can be proposed and loaded again.

## migrate-not-carried
title: What is never migrated
screen: migrate
roles: Admin, Compliance officer
ask: are passwords migrated; is aadhaar migrated; passwords migrated; client login passwords; full aadhaar number; fields left out migration; what is not migrated; what does the migration leave out; client web passwords after migration; full aadhaar number migration; dropped fields migration
related: migrate-firm, reset-password

Some things are never brought across, whatever the old system holds:

- **Passwords** of every kind: staff logins, client web and trading passwords, PINs. Users get new logins in Ledgerline ([[settings|Settings → Users]]); clients are given portal access afresh.
- **Full Aadhaar numbers.** Only the last four digits are kept, as everywhere in Ledgerline.

The package lists every field left out on purpose, why, and how many records had it, under **Left out on purpose** on [[migrate|the package]].

## before-ledgerline
title: See a client's years before Ledgerline
screen: clients
roles: Ops maker, Ops checker, Compliance officer, Branch staff
ask: old ledger before ledgerline; previous year ledger from sharepro; earlier years history; old bills; old contract notes register; old dp transactions; year end holdings old system; migrated history; client statement for 2024-25; export old year history csv
related: migrate-firm, old-documents-archive

A migration brings the earlier years of the old system as **read-only history**, labelled "Migrated from <system> — read only".

1. Open the client and scroll to **Before Ledgerline**. Choose the financial year.
2. **Ledger** shows the year's statement with the opening balance and a running balance; **Bills**, **Trades**, **Contract notes register**, **Receipts and payments**, **DP transactions**, **DP bills** and **Year-end holdings** show what the old system had. **Archive** lists the client's old documents.
3. A whole year of one kind, for every client, is exported as CSV from [[migrate|Migration from old system]] (Ops checker or Compliance).

- Nothing in the history can be changed. New entries are made in Ledgerline as usual.
- Clients see their own earlier ledger, contract notes and old documents in the portal under **Earlier years**.

## old-documents-archive
title: Find an old contract note or statement in the archive
screen: archive
roles: Ops maker, Ops checker, Risk manager, Compliance officer
ask: old contract note; contract note from old back office; archived documents; document archive search; legal hold document; delete old documents; how long are documents kept; retention of contract notes; who downloaded a document; old statements from sharepro
related: before-ledgerline, migrate-firm, dispatch-register

The archive holds the documents the old system sent (contract notes, statements, DP bills, margin statements), up to eight years of them, as they were sent.

1. Open [[archive|Client documents → Old documents archive]]. Search by client, type, dates and whether the document was signed.
2. **Download** opens the document. Every download is recorded in the [[audit|audit trail]]: who, when, which document.
3. A client's documents are also on the client screen (Before Ledgerline → Archive), and the client sees their own in the portal.

- Each document is kept **until its retention date**: the date in the package, or eight years from the document's date. It cannot be deleted before then.
- A Compliance officer can put a document on **legal hold** (give the matter or order); while on hold it cannot be deleted at all.
- After the retention date a Compliance officer may delete it, with a reason; it is then not shown or served to anyone.

## migrate-masters
title: Bring branches, partners, UCC details and demat accounts across
screen: files
roles: Admin, Ops checker
ask: import branches; import branch master; import partners; import authorised persons; import sub brokers; import ap master; import ucc details; import client address for ucc; import contact persons directors; import dp accounts; import bo accounts; migrate demat accounts; client groups in client master import; inactive clients in migration
related: migrate-firm, migrate-check, add-branch, add-ap

Besides clients, balances and holdings, the old back office keeps data Ledgerline also holds. The migration tool writes each as a go-live file; they can also be loaded by hand on [[files|Files → Other uploads]]. They load in this order:

1. **BRANCHES**: the offices and their tree (Code, Name; optional City, Manager, Kind, Parent, Active). A parent may come further down the file.
2. **PARTNERS**: authorised persons and relationship managers (Code, Name, Kind; optional Branch, Parent, PAN, GSTIN, RegNo, SharePct, bank details, Email, Active).
3. **CLIENT-MASTER**, which may also carry **Inactive** and **InactiveSince**, **AP** and **RM** (partner codes, checked against the partners once there are any), **Groups** (separated by ;, added to the client's groups) and **DPOnly** (Y for a client of the DP only, with no segments). Its demat account becomes the client's primary; a demat account already on another client is refused (a warning for an inactive client).
   **CLIENT-DEMATS** (after CLIENT-BANKS) lists every demat account a client registered, with the firm's DP or another (UCC, Depository, DematID; optional Primary Y on at most one row, Status Active or Inactive). A client's rows replace their demat accounts; with no Primary the client master's account stays the primary.
4. **UCC-DETAILS**: the exchanges' UCC details (address in lines with PIN, permanent address, gender, father's or spouse's name, occupation, PEP, IPV, POA and the rest). A blank cell leaves what is stored.
5. **UCC-CONTACTS**: contact persons of firms and companies (UCC, Name, Designation; optional PAN, Address, Phone, Email, DIN). A client's rows replace their contacts, at most 5.
6. **DP-ACCOUNTS**: the DP's record of each demat account (UCC, BOID, Depository, Status; optional Opened, ClosedOn, BSDA, Freeze and FreezeReason, DDPI, Primary, and the joint holders Holder2Name, Holder2PAN, Holder3Name, Holder3PAN). A client may have several rows; Primary Y on one makes it the primary demat account.

- Each file is checked with the same rules as its screen, and every problem is listed at once.
- A demat account whose UCC is not a client here is an error: load the client master first (a client of the DP only comes in with DPOnly Y). A BO ID on two clients is an error too.
- The reconciliation counts every BO account and compares holdings quantity by quantity, decimals included.
- Loading a file again changes nothing.
