Ledgy help articles: the migration tool that moves an office from SharePro (or another old back office) to Ledgerline.

# Migration tool

## migration-tool
title: Make the migration package with the migration tool
screen: files
roles: Admin
ask: migration tool; download the migration tool; ledgerline-migrate; run the migration tool; make the migration package; read sharepro backups; convert sharepro backup; restore sharepro backup for migration; drop backups into the tool
related: migrate-firm, migration-backups, migration-passphrase, migrate-not-carried, migration-mac, migration-download

The **Ledgerline migration tool** runs at your office, on your own computer. It reads your SharePro backups and writes one locked file, the **migration package** (.llmig), which you then upload into Ledgerline. Nothing is sent anywhere while it works.

1. Download the tool for Windows or the Mac and start it. A page opens in your browser (only this computer can open it).
2. The tool finds SQL Server on the computer. The best place to run it is the **SharePro server** itself.
3. **Drop your backups**: the latest SharePro backup and the archives of up to 8 earlier years (.7z, .zip or .bak, or whole folders of them). The next article says where SharePro keeps them.
4. **What we found** lists every database: the company years (named like NC2627 for 2026-27), the mail archives (NCMAIL2627) and the admin database (shadmin). Years not in the package are shown as skipped, with the reason. If the newest backup is more than 2 days old, take a fresh backup in SharePro first.
5. **Choose** the firms and what the package is for. For go-live choose **Go-live**: it holds the masters, balances and holdings and the history of the **current financial year only**. You may tick earlier years to add, but the usual way is to go live with the current year and bring earlier years later in a history package (see *Bring earlier years later with a history package*). Then choose whether to include archived documents, what to do with dormant clients, whether clients' own interest rates come across or everyone moves to the firm's rate, where to save, and a passphrase.
6. When the package is ready, the tool shows the checks Ledgerline will verify and any issues with what to do. A printable **report** is saved beside the package.
7. In Ledgerline, open [[migrate|Files → Migration from old system]] and upload the package. Ledgerline asks for the passphrase and recalculates every check (trial balance, client balances, holdings) before anything goes live.

The tool restores each backup into a temporary copy on SQL Server, reads it, and deletes the copy and all its work files at the end, also when you cancel. It never writes to SharePro's own databases. A large office's archived documents may come out as several parts (4 GB each by default); upload every part.

## migration-backups
title: Where to find SharePro's backups for the migration
screen: files
roles: Admin
ask: where are sharepro backups; sharepro backup folder; sqlbackupandftp; sharepro 7z backup; find old year backup; earlier years database; archived years sharepro; detached database; nc2627 backup; ncmail backup; shadmin backup; backup password 7z
related: migration-tool, migration-passphrase

SharePro's backup job (**SQLBackupAndFTP**) usually writes one **.7z** file per database to a backup folder on the SharePro server (often on the D: drive, or the folder its settings show under *Store backups in*), and may copy them to a NAS or FTP folder too.

- **Company database** per financial year: NC2627 for 2026-27, NC2526 for 2025-26, and so on. Drop the newest one and up to 8 earlier years.
- **Mail archives**: NCMAIL2627 and so on, plus SSMAIL. These hold the contract notes and statements SharePro mailed; drop them to keep the archived documents.
- **Admin database**: shadmin (the firms and users).
- **Earlier years** are often archived or detached on the same server. If a year is only there as a detached database file (.mdf), attach it in SQL Server Management Studio and choose **Use databases already on this server** in the tool, or take a .bak of it.
- On the SharePro server itself you can skip the files: choose **Use databases already on this server** and tick the databases. They are read in place and not changed.
- A .7z with a password (set in SQLBackupAndFTP) is fine: the tool asks for it. RAR files must be unpacked with WinRAR or 7-Zip first.
- SQL Server Express holds at most 10 GB per database. A bigger backup must be read on the SharePro server, whose SQL Server can restore it.

## migration-history-package
title: Bring earlier years later with a history package
screen: files
roles: Admin, Ops checker
ask: history package; migrate earlier years later; bring old years later; load previous years after go live; current year only migration; only current financial year; add old financial years; earlier years history later; second migration package; load history into a live firm; years already loaded
related: migration-tool, migrate-firm, before-ledgerline, old-documents-archive

Go live with the **current financial year only**, then bring the earlier years across when you are ready. That keeps the go-live package small and the go-live day short.

1. In the migration tool, drop the backups of the earlier years you want (for example NC2425 and NC2324, with their mail archives) and also the newest company backup: the tool reads it only to know which client each account belongs to.
2. On **Choose**, pick **History package** and tick the years. The current financial year cannot be ticked: it came with the go-live package.
3. Make the package with a passphrase, and upload it on [[migrate|Files → Migration from old system]]. Check it as usual.
4. **Propose load**. If a year is already in Ledgerline (from the go-live package or an earlier history package), Ledgerline refuses until you tick **Load these years again**; each year then shows the newer copy, and its documents are added beside the ones there.
5. A second person approves it with the passphrase.

A history package loads only read-only history and archived documents. It never posts to the live ledger, never changes balances, clients or holdings, and never touches the current year. It can be undone (by two people) at any time, even after the firm has posted new entries.

A year that falls in an archived period (Admin → Data & exit → Archive) is refused: restore that archive first, load the history, then archive the period again.

## migration-passphrase
title: The migration package's passphrase
screen: files
roles: Admin
ask: migration passphrase; package passphrase; llmig password; forgot migration passphrase; package will not open; passphrase is wrong; how strong passphrase; who needs the passphrase
related: migration-tool

The tool locks the package with a **passphrase** you choose (at least 12 characters; a few unrelated words make a strong one). Everything inside is encrypted: client details, balances, documents.

- Ledgerline asks for the passphrase when you upload the package. **Without it the package cannot be opened**, not by you, not by Ledgerline, not by anyone. The tool does not keep it.
- Give the passphrase to the person loading the package separately from the file: say it on the phone or in person, not in the same e-mail or chat as the file.
- If it is lost, run the tool again on the same backups with a new passphrase.

## migration-mac
title: Running the migration tool on a Mac
screen: files
roles: Admin
ask: migration tool mac; sharepro data on a mac; docker sql server; docker desktop migration; rosetta docker; apple silicon sql server; sql server express docker; migration laptop
related: migration-tool, migration-download

There is no SQL Server for the Mac, so on a Mac the tool runs Microsoft's free **SQL Server Express** inside **Docker** and removes it at the end.

1. Install **Docker Desktop** (or OrbStack or Colima) and open it. Wait until it says it is running.
2. In the tool, press **Check Docker**, then **Start SQL Server**. The first time it downloads SQL Server from Microsoft, about 1.5 GB; the download is kept for next time.
3. Give Docker at least 4 GB of memory (Docker Desktop → Settings → Resources).
4. On **Apple silicon** (M1 and later) SQL Server runs under emulation: turn on **Use Rosetta for x86/amd64 emulation on Apple Silicon** in Docker Desktop → Settings → General.

SQL Server Express holds at most 10 GB per database. For a bigger backup, run the tool on the Windows server where SharePro runs, or leave that backup out. Under Advanced the tool also offers SQL Server **Developer** edition, which has no such limit; Microsoft licenses it for development and testing only, so check with your IT before using it.

## migration-download
title: Opening the migration tool the first time (Windows SmartScreen, macOS)
screen: files
roles: Admin
ask: windows protected your pc; smartscreen migration tool; run anyway; cannot be opened because the developer cannot be verified; macos unidentified developer; open migration app mac; gatekeeper; migration tool blocked
related: migration-tool, migration-mac

The migration tool is not code-signed yet, so the first time Windows or macOS warns about it.

- **Windows**: if *Windows protected your PC* appears, click **More info**, then **Run anyway**. You can also drag the backups onto the program's icon to start with them.
- **macOS**: unzip it, then **right-click** "Ledgerline Migration" → **Open** → **Open**. Or open it once, then go to System Settings → Privacy & Security and click **Open Anyway**. The app has no window of its own: it opens the page in your browser, and keeps its log in ~/Library/Logs/Ledgerline Migration/.

Leave the tool running until you press **Finish** on the page; it then removes its temporary copies and work files.
