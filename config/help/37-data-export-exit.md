Ledgy help articles: the full export of all the firm's data (data portability), checking an export, and closing the Ledgerline account with the deletion of the firm's data and a destruction certificate (SEBI's cloud framework).

# Data & exit

## full-data-export
title: Export all our data from Ledgerline
screen: dataexit
roles: Admin, Compliance officer
ask: export all data; export all our data; full data export; download all our data; data portability; copy of all our data; take our data out; backup for ourselves; get our data; export database; all data in one file; move to another back office; data export zip; download export; export link expired
related: verify-data-export, close-account, backups

Open [[dataexit|Data & exit]] and click **Export all our data**. Give the reason; it is recorded.

1. A second person approves: the compliance officer or another Admin. The person who asked cannot approve it.
2. Ledgerline builds the export in the background. You can leave the screen. A large firm's export can take a while.
3. When it shows **Ready**, an Admin clicks **Download**. Each click makes a link that works once, for 15 minutes, for that Admin only. Every download is in the audit trail.
4. The export is kept on the server, encrypted, for 7 days. Then it is deleted. Ask for a new one if you need it again.

The export is one ZIP with:

- **collections/**: every document of every collection as JSON Lines (one record per line), decrypted. Clients, users, settings and every workflow record.
- **files/**: every stored file as it was received: KYC documents, exchange, depository and bank files, contract notes, reports. **files/index.jsonl** lists each with its SHA-256.
- **csv/**: spreadsheet copies of clients, ledger entries, trades, holdings, contract notes and bills, receipts, payments, DP transactions and DP bills.
- **tables/** and **history/**: the ledger, trades and reconciliation as stored, and every earlier version of every document.
- **audit/audit-trail.jsonl**: the audit trail with every seal.
- **manifest.json** (every file with its size and SHA-256, the counts, the time and the Ledgerline version), its signature, and **README.txt**, which explains the formats.

## verify-data-export
title: Check a data export is complete and unchanged
screen: dataexit
roles: Admin, Compliance officer
ask: verify export; check export signature; manifest signature; export manifest hash; is the export complete; check the zip; sha256 of export files; ed25519 public key; export signed with; check audit seals in export; seal keys
related: full-data-export, audit-seals

Each export's line on [[dataexit|Data & exit]] shows its **manifest SHA-256** and how it is signed. Keep a note of both.

1. **The signature.** With a digital signature set up (Settings → Digital signature), **manifest.json.p7s** is signed with the firm's certificate; README.txt gives the openssl command to check it. Without one, **manifest.json.sig** is an Ed25519 signature; its public key is in manifest.json and on the screen. The two must match.
2. **Every file.** Each file's SHA-256 (sha256sum) and size must match its line in manifest.json. No file may be missing or extra.
3. **The audit trail.** Each entry links to the one before. Ledgerline checks every seal when it makes the export and writes the result in the manifest. The final export made when the account is closed also has **audit/seal-keys.json**, so the seals can be checked without Ledgerline. README.txt explains how.

## close-account
title: Close our Ledgerline account and delete our data
screen: dataexit
roles: Admin, Compliance officer
ask: close account; close our ledgerline account; delete our data; leave ledgerline; stop using ledgerline; exit ledgerline; offboarding; terminate ledgerline; expunge data; data deletion; destruction certificate; certificate of data destruction; cooling off period; cancel closing; data returned or destroyed; sebi cloud exit
related: full-data-export, verify-data-export, backups

Closing the account deletes all of the firm's data from Ledgerline. It is slow on purpose. On [[dataexit|Data & exit]]:

1. An Admin clicks **Close our Ledgerline account**, gives the reason and types the firm's code.
2. A second person approves: the compliance officer or another Admin. The person who asked cannot.
3. The **cooling-off** starts: 7 days unless an Admin changed it beforehand (1 to 90 days). The screen counts down. The firm works as normal. Either person can **Cancel closing the account** until the data is deleted.
4. When the cooling-off ends, Ledgerline stops the firm's scheduled work and builds the **final export**. Do not change anything from now on: changes are not in it.
5. An Admin downloads the final export and checks it opens and its signature verifies.
6. An Admin clicks **Delete our data now** and types the firm's code again.

Ledgerline then deletes every record of the firm in one go: documents, their history, files, ledger, trades and the audit trail, and the firm's backups and exports on the server. No one can sign in again.

You get the **destruction certificate** at once: what was deleted (counts by kind), when, who asked and approved, the final export's manifest hash, and the date by which copies of backups held elsewhere age out. It is signed like your other documents and emailed to the compliance email in Settings. Keep the link it gives: the certificate can be downloaded again from it without signing in. Ledgerline's provider keeps only a record of the exit and the certificate.

If the firm signs with a USB token signer, the certificate cannot be signed with it: switch to a certificate file or signing service before deleting, or keep the certificate's SHA-256.

Archived periods are deleted too: their packages go into the final export first (archives/<id>.zip), then they are deleted from archive storage with the rest of the firm's data.

## archive-period
title: Archive closed years to lower storage costs
screen: dataexit
roles: Admin, Compliance officer
ask: archive old years; archive a financial year; move old data to archive; reduce storage cost; database too big; free up space; archive period; archive custom period; cold storage; archive trades and ledger; archive old files; what can be archived; archive preview; storage saving
related: archive-restore, archived-period-notice, full-data-export

Open [[dataexit|Data & exit]] and the **Archive** tab. An Admin chooses what to move out of the live data:

1. **The period**: whole financial years (one after another), or a first and last date. The current financial year can never be archived.
2. **What to archive**: stored exchange and clearing files, contract notes and other documents, trade events, ledger entries, document history (earlier versions of records) and migrated history. Leave any of them unticked to keep them live.
3. Click **Preview**. It shows, for each kind, the rows and bytes it would free, and the estimated saving a month at the storage prices under **Archive rules**. Nothing changes yet.
4. Click **Ask to archive**. A second person, the compliance officer or another Admin, approves it.

Ledgerline then writes the period to an **archive package** on archive storage (encrypted with the firm's keys and signed like the full export), reads it back and checks every file and every row, and only then removes the rows from the live data. The period's ledger entries are replaced by **opening balances carried forward** on the period's last day, so every balance, statement and report after it stays exactly the same; Ledgerline checks this before it finishes.

What stays live:

- The audit trail, always.
- Nothing is archived while a reconciliation break, a payout or a pledge request of the period is open, or while closing the account is in progress.
- The rows of clients with an unpaid debit, open pledges or an open complaint stay live; so do migrated documents on legal hold, KYC documents, vendor bills and evidence files.

Archive packages are kept at least as long as **Keep archives** says (8 years by default, the record-keeping rules for brokers) and cannot be deleted before then.

## archive-restore
title: Restore an archived period
screen: dataexit
roles: Admin, Compliance officer
ask: restore archive; unarchive; bring back archived year; see archived data; restore old period; archived on restore it; inspection needs archived year; archive again; re-archive
related: archive-period, archived-period-notice

On [[dataexit|Data & exit]] → **Archive**, find the archive and click **Restore…** (Admin). Give the reason; a second person approves it.

Ledgerline reads the package back from archive storage and checks its signature and every file again. It puts every row back exactly as it was (the same entry numbers, files with the same bytes), removes the opening balances that stood in for the period, and checks that every balance is unchanged. If the package is missing or damaged, nothing changes and the period stays archived.

The whole archive comes back. When you are done, click **Archive again** to move the same period out again (a second person approves, as before).

## archived-period-notice
title: "Archived on … restore it to see it"
screen: dataexit
roles: Ops maker, Ops checker, Compliance officer, Admin
ask: archived on; restore it to see it; statement says archived; why can't I see old ledger; old file archived; cannot post entry archived period; trial balance archived; old trades missing; client statement starts later; opening balances carried forward
related: archive-period, archive-restore

A screen or report that reaches into an **archived period** says when it was archived and that it must be restored to be seen, instead of showing figures that would be incomplete.

- A client statement from the beginning starts the day after the archived period. Its opening balance includes the archived years, so the balance is right.
- A statement, trade list or trial balance for dates inside the archived period says it is archived. The trial balance on the period's last day and later is exact.
- A file or migrated year that was archived says which archive holds it.
- Nothing can be posted with a date inside an archived period.

To see the period again, ask an Admin to restore it on [[dataexit|Data & exit]] → **Archive**.
