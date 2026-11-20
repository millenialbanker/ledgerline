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
