Ledgy help articles: inspection packs (a joint, exchange, depository or SEBI inspection's data request, or the half-yearly internal audit, put together in one ZIP), their templates and sample, who may make and download them, and the policy and audit documents kept for them.

# Inspection packs

## inspection-packs
title: Make an inspection pack for an inspection or internal audit
screen: inspack
roles: Compliance officer, Admin
ask: inspection pack; make an inspection pack; exchange inspection data request; nse inspection documents; bse inspection; mcx inspection; sebi inspection; depository inspection; cdsl inspection; nsdl inspection; internal audit data; documents for the internal auditor; data for inspection; prepare for inspection; inspection in one click; pack is building; download inspection pack
related: inspection-pack-templates, inspection-pack-sample, inspection-pack-privacy, inspection-documents, regulatory-reports

When the data request arrives, the compliance officer makes the pack on [[inspack|Inspection packs]]:

1. **New pack**, then choose the inspection: one of the standard templates (joint inspection of the broker and DP, exchange, depository, SEBI, half-yearly internal audit) or the firm's own.
2. **Period**: name the inspection as it should appear on every file, the request's reference, and the dates. The buttons fill in the half-year, financial year, quarter or month just ended.
3. **Sample**: N clients at random (the seed is recorded so the same clients can be drawn again), the client codes the inspector named, or no sample.
4. **Review items**: change a title, its period (the inspection period, as at its end, or the current version), the Ledgerline report that produces it, or take it off; **Add an item** for anything else. Each item says whether Ledgerline can produce it now, or why not, and which ones are **to confirm** against the request.
5. **Build the pack**. It is built in the background with its progress on the list; you can leave the screen.

The pack is one ZIP: **00-cover.html** (firm, inspection, period, sample seed, who prepared it and when, and the SHA-256 of every file), **00-index.html** and **00-index.csv** (every item with its files, record counts and period, or "not available" and why), **SHA256SUMS.txt** (for checking with sha256sum), and a folder per item. Each item comes from Ledgerline's own reports and exports: the client register, KYC registry and client documents, UCC batches, the trade register, contract notes, the dispatch register, ledger statements and balances, margin collection, running account settlements, the complaints register, pledges with DDPI and BSDA, payouts and receipts, the daily client money segregation check, holding statement and SEGMON submissions, the register of securities, client code modifications, terminals and users, nominations, the compliance calendar's evidence, circular actions with their evidence, and the documents uploaded under **Documents**.

Some records are not in Ledgerline (order logs and voice recordings are kept by the trading platform and the voice logger; NDUs by the depository's system). The index lists them as not available, with where they are kept. Ledgerline keeps the latest holdings only, so the holding statement and register of securities are as at the day the pack is made, and the index says so.

## inspection-pack-templates
title: Inspection pack templates and where their items come from
screen: inspack
roles: Compliance officer, Admin
ask: inspection template; joint inspection; joint inspection by exchanges and depositories; lead exchange inspection; data request list; what does the exchange ask for in inspection; internal audit checklist; change the inspection template; add item to inspection pack; save our own template; to confirm in inspection pack; which items are to confirm
related: inspection-packs, inspection-pack-sample

Five standard templates are a starting list, checked against public documents in November 2026. Compare each with the actual request and change the items before building.

- **Joint inspection by the exchanges and depositories (stock broker and DP)**: since 1 December 2025 SEBI has the stock exchanges, clearing corporations and depositories inspect each stock broker together once, with one of them leading (yearly for higher-risk brokers, every two years for professional clearing members, at least once in three years for others), and SEBI's press release 44/2026 (7 August 2026) makes the inspection of a broker and its DP joint. This template is the exchange list and the depository list with each record asked for once; every item is marked to check against the lead institution's letter.
- **Exchange inspection of the stock broker (NSE, BSE, MCX)**: the books a member keeps under rule 15 of the Securities Contracts (Regulation) Rules, 1957, as BSE's Master Circular on Inspection (updated to 31 March 2024, item 6.1) lists them, and the client funds and account rules of SEBI/HO/MIRSD/MIRSD2/CIR/P/2016/95 (26 September 2016).
- **Depository inspection of the DP (CDSL or NSDL)**: the areas of NSDL/POLICY/2025/0139 (10 October 2025) and CDSL/AUDIT/DP/POLCY/2026/693 (6 October 2026) on internal and concurrent audit: account opening, DIS, POA and DDPI, pledges, closures and freezes, grievances.
- **SEBI inspection**: SEBI (Stock Brokers) Regulations, 2026 (SEBI/LAD-NRO/GN/2026/291, 7 January 2026) let SEBI inspect alone or jointly with the exchanges, clearing corporations and depositories, and records are kept for eight years. SEBI's data request comes with its letter and is not published, so most items are **to confirm**.
- **Half-yearly internal audit (stock broker)**: SEBI/HO/MIRSD/MIRSD2/CIR/P/2016/95 and NSE's half-yearly internal audit circular (ref. 47/2026, half year ended 30 September 2026, by an NSE-empanelled auditor, due 30 November 2026). NSE's sampling annexures (III and IIIA) were not read in full: the sample size is **to confirm**.

Cyber security items follow SEBI's CSCRF (SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2024/113, 20 August 2024); DDPI SEBI/HO/MIRSD/DoP/P/CIR/2022/44 (4 April 2022). Each item shows its source on the **Templates** tab.

The standard templates cannot be changed. **Copy as the firm's template**, or **Save as a template** while reviewing a pack's items, keeps the firm's own list (compliance officer or Admin). Deleting a firm's template takes it off the list; packs made from it keep their items.

## inspection-pack-sample
title: The sample of clients in an inspection pack, and drawing it again
screen: inspack
roles: Compliance officer, Admin
ask: sample clients for inspection; random sample of clients; sample seed; draw the same sample again; inspector gave a list of clients; how is the sample chosen; reproduce the sample; population for the sample
related: inspection-packs, inspection-pack-templates

A random sample is drawn from the clients whose account was opened by the period end (KYC date on or before it) and not rejected, in client code order, with a seed: a whole number of up to 15 digits. Leave the seed blank for a new one; it is shown before the build and printed on the cover sheet with the number of clients drawn from and the SHA-256 of their client codes.

The same seed, period end and clients draw the same sample again: Ledgerline shuffles the client codes with the SplitMix64 generator started from the seed (Fisher–Yates) and keeps the first N, and the method is fixed so it does not change between versions. If the codes' checksum on the cover differs, the client list itself has changed since (clients added or rejected).

With **The inspector's list**, paste the client codes the request names; codes that are not the firm's clients are refused with the list of them. Items for sampled clients only (KYC documents, contract note copies, ledger statements, DP transactions) cover the sample; without a sample they are taken off.

## inspection-pack-privacy
title: Who may make and download inspection packs, the watermark, masked copies and expiry
screen: inspack
roles: Compliance officer, Admin
ask: who can download inspection pack; inspection pack audit trail; who opened the pack; watermark on inspection files; prepared for watermark; mask personal details; masked copy for advisers; share pack with consultant; pack expired; inspection pack deleted; how long are packs kept; remove pack from download list; pack interrupted
related: inspection-packs, audit-seals

A pack holds full client data, because inspectors need it. Only the compliance officer and Admin see [[inspack|Inspection packs]], make packs and download them; other roles do not see the screen. Every pack made, downloaded or its index opened is written to the sealed audit trail (inspack.made, inspack.downloaded, inspack.index_viewed) and to the pack's own list: **Who opened it**.

Every file Ledgerline writes carries "Prepared for <inspection>, <date>, by <person>": the first row of each CSV, a band across the top of each HTML page and each PDF page. Copies of uploaded documents (KYC scans, policies, audit reports) go in as they were uploaded.

**Masked copy** (or **Mask personal details** in the wizard) builds the pack again from the same template, period, items and sample for outside advisers: client names, client codes, PANs, Aadhaar-like numbers, phone numbers, email addresses and account numbers become tokens such as [CLIENT-3], the same token for the same value throughout the pack; dates of birth keep the year only; KYC images are left out. Staff names stay. The watermark says it is a masked copy.

A pack leaves the download list after 30 days (**Keep packs … days** changes it for new packs). Ledgerline's file store is append-only: the pack is marked expired and its file is no longer served; the record of the pack, its contents and who opened it stay. **Remove** takes a pack off the list sooner, with a reason. Packs are never emailed: download them here. A pack the server stopped building shows as interrupted: make it again.

STRs are confidential and the client must not be told of one (PMLA; SEBI's AML/CFT master circular SEBI/HO/MIRSD/MIRSDSECFATF/P/CIR/2024/78 of 6 June 2024; the paragraph is to confirm), so the PMLA item has counts only: alerts by source, type and status, STRs filed, and clients by risk category, with no names, alert details or STR contents.

## inspection-documents
title: Keep policies, audit reports and cyber security evidence for inspection packs
screen: inspack
roles: Compliance officer, Admin
ask: upload policy for inspection; vapt report upload; cscrf evidence; cyber audit report; internal audit report upload; net worth certificate upload; board minutes for inspection; retire old policy; documents tab inspection packs
related: inspection-packs

On [[inspack|Inspection packs]] → **Documents**, the compliance officer adds the documents an inspection asks for and Ledgerline does not make itself: policies (risk, PMLA, surveillance, outsourcing), board minutes approving them, CSCRF evidence, VAPT, cyber and system audit reports, internal and concurrent audit reports and action taken reports, the net worth certificate and audited accounts. Choose the kind, a title, the version or date, and the file (PDF, picture, Word or Excel, up to 20 MB). It is kept encrypted with its SHA-256.

A pack's document items take the current documents of the kinds they name (for example "CSCRF, VAPT report, Cyber audit report"). **Retire** a document when a newer version replaces it: it stays on record and can still be downloaded, but goes into no new pack. Until a document of a kind is uploaded, the items asking for it show as not available.
