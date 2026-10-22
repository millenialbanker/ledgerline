# Ledgerline back office

The production core of a multi-tenant back office for Indian stockbrokers: file ingestion, trade reconciliation, charges, EOD billing and an append-only double-entry ledger, on PostgreSQL. It covers equity cash, F&O, currency and commodity segments.

On top of that core sit the daily operations modules: client onboarding, RMS and limits, peak-margin reporting, collateral and pledge, settlement obligations and unpaid securities, contract notes, bank reconciliation, running account settlement, regulatory reports, surveillance, IPO and mutual fund orders, and EOD close. A browser console served by the same binary drives all of it.

## Try it in two minutes

```bash
./bin/ledgerline -dev        # then open http://localhost:8080
```

Sign in with one of the development users shown on the sign-in page (password `ledgerline-demo`). On the dashboard, either **Load sample day** and work it yourself, or **Run a sample week**: five consecutive trading days (16 to 22 October 2026, including the 20-Oct weekly options expiry) loaded, reconciled, billed, settled, closed and rolled forward. Each generated day is real files through the normal loaders: about 60 clients, 300+ trades across NSE, BSE and MCX, market data, OMS trade book with breaks, and the bank statement. Switch user from the menu at top right to see maker-checker and role rules.

## Consecutive trading days

A day starts from what the previous one left behind:

| Step | What happens |
| --- | --- |
| Start of day | Open futures and options carried in from the previous close (the clearing corporation's POSITIONS file on the first day). |
| Billing | Trade bills; carried futures marked to market at the day's settlement price; options expiring that day exercised or assigned at intrinsic value. |
| End of day | Closing rolls end-of-day positions to the next trading day: futures at the settlement price, options at average premium, expired contracts dropped. |
| Guard | A day cannot be billed, closed or loaded while the previous trading day with activity is still open. |

## Client groups and brokerage plans

Clients can be put in groups (tags such as HNI, NRI, STAFF), one by one or in bulk from the client list. Groups filter the client list and can be reported on as a whole (ledger, holding and trade-book reports).

Brokerage plans are named sets of rates per product (equity delivery and intraday, futures and options in each segment): percent of turnover, rupees per order, the lower of the two, rupees per lot, or free, each with an optional minimum and maximum per order. Cash-market brokerage is capped at 2.5% of the order value. Plans have dated versions, and are assigned from a date to the firm (default), a group or a single client. A client pays: their own plan, else their group's (the most recently assigned group if several), else the firm default, else the charge schedule. Brokerage is charged per executed order and split across the order's trades on the contract note. Statutory charges always come from the schedule. The charges calculator quotes for a chosen client.

## Correcting one client's bill

Billing → **Correct** on the client's row (Ops checker). Choose what is being corrected: a brokerage rate (charge this trade date, or this date onwards, under a chosen plan; a one-day change restores the client's previous plan from the next business day) or trades already corrected by a client code modification or a corrected file. A reason is required. Ledgerline reverses only that client's bills for the date, posts the corrected ones, and issues a revised contract note with a new number that states which note it replaces and why; it can be emailed at once or later from Contract notes, where any note can also be resent. API: `POST /v1/billing/correct {date, ucc, reason, plan, scope: day|onward, send}`.

## Inactive accounts

Following the exchange circulars of October 2024 (NSE NSE/INSP/64718 of 25-Oct-2024 and the matching BSE and MCX circulars), a trading account with no activity for 24 months (configurable) is due to be flagged Inactive in the UCC database. Activity counted: exchange trades in any segment, IPO bids not cancelled, mutual fund SIP or lumpsum purchases on exchange platforms, KYC contact changes uploaded to the KRA, and reactivation. An Ops checker or compliance officer flags due accounts (they go into the day's UCC batch); inactive accounts get a nil OMS limit, a High alert if they trade, and drop out of the funds-and-securities report when their balances are nil. Reactivation is a maker request recording IPV/VIPV, confirmation or update of details and a Validated/Registered KRA status, approved by a different checker; the 24 months restart from reactivation.

## Modules

The console has 38 screens for staff and a separate client portal. Beyond trades, reconciliation, billing, settlement and contract notes:

**Risk.** Live RMS from intraday OMS trades and LTP snapshots (margins, losses, unbilled charges, a 20% auction reserve on uncovered delivery sells) with a liquidation order file; end-of-day RMS with overrides, kill switch and square-off; peak margin and penalties; collateral and pledges; debtors (continuous debit with front-end block file, ageing, debtors' stock, interest with GST invoices); margin trading (MTF: policy and net-worth limit, enrolment, eligible list at VaR + 3/5 × ELM, funding from own funds, MTF pledge by T+1, daily interest, margin calls, liquidation, the exchange MTF report). Front-office files: beginning-of-day limits and holdings (with T1 for BTST), end-of-day client allocation of funds and collateral to the clearing corporation.

**Money.** Receipts entered by a maker and approved by a checker from the registered account only; cheque clearing and returns with charges; signed payment-gateway webhook; payout requests checked across segments; NEFT/RTGS payout batches with UTRs; running account settlement.

**Securities.** Corporate actions (dividend, bonus, split) in holdings and purchase lots, dividend reconciliation, buy-back tenders, internal close-out and auction of short deliveries.

**Firm accounts.** Chart of accounts, vouchers with maker-checker, branch books, P&L and balance sheet, GST invoice register with GSTR-1 tables and GSTR-3B, period locking (fixed or automatic T-n) and private account heads.

**Depository.** BO accounts with BSDA eligibility; DIS books and maker-checker slips verified against the slip register and free balance (client confirmation above ₹5 lakh); upload batch and execution; demat, freeze, transmission and closure; DP tariff and monthly DP bills posted to the trading ledger with GST.

**Compliance.** PMLA risk with special categories and periodic KYC (2/8/10 years); KRA status and upload file, CKYC numbers and bulk request; SEBI/exchange debarment and UN sanctions screening (PAN match blocks, name match for review); scanned client documents; inspection reports; STT and turnover reconciliation; inactive accounts; surveillance; investor complaints with 21/7-day due dates, SCORES and exchange references and the monthly website disclosure; NRI under PIS (rule checks, PIS bank file, TDS on capital gains).

**Clients and business.** Client portal (family logins, holdings, ledger, contract notes, reports, payouts, profile changes approved by head office, help desk, trades through other brokers for capital gains, WhatsApp alerts); leads and CRM with masked mobiles, calls through a telephony bridge, appointments and conversion incentives; branches, authorised persons and brokerage sharing with scoped logins; referrals; prepaid brokerage schemes; brokerage plans and client groups.

**Messaging and automation.** Templates per event and channel (email, SMS with DLT template IDs, WhatsApp with opt-in), event messages, bulk sends, a WhatsApp bot for balance, holdings, ledger, contract notes and margin, and scheduled jobs (debit reminders, monthly statements, KRA file, emailed reports, and the Autopilot stages) with run history and management emails. Contract notes, margin calls, KYC reminders, inactive flagging and interest are Autopilot steps, not separate jobs, so nothing runs twice.

Connections that need the broker's own accounts (exchange extranet, depositories, KRA/CKYC, banks, SMS/WhatsApp providers, digital signature) are built up to a pluggable point with a simulated stand-in, the way contract-note email goes to an outbox.

## Autopilot

Routine work runs by itself; people get only the exceptions. Each process has a policy: **Auto** (done and recorded), **Suggest** (prepared, one click to approve) or **Off**, capped where the rules need a person's judgement or signature. Stages run on business days from the scheduler (start of day, intraday, pre-close, end of day, month end), or by hand; some run the moment something happens.

| Runs by itself | Prepared for one click | Stays with people |
| --- | --- | --- |
| File intake (type recognised from the columns, loaded in order), debit blocks, margin pledges, return of third-party credits, payouts up to a limit for clients with no risk flags, payout bank file, reconciliation breaks with no economic difference, billing (clients in a client-code dispute wait), settlement and derivatives MTM, contract notes, margin calls, square-off at the cut-off, penalties, daily reports generated and validated, NRI checks, KYC reminders, inactive flags, email/mobile changes verified by OTP, service requests for missing notes or statements, complaint assignment, day close, running account settlement, month-end interest, MTF interest, DP bills, AP payouts | Unknown credits (name match, bank confirms), large or flagged payouts, money-affecting breaks, PMLA risk the rules suggest, MTF liquidation, report uploads until a portal connection exists | Client activation and reactivation, surveillance decisions and STRs, physical DIS verification, client code modifications, bill corrections and overrides, bank/address/nomination changes, MTF enrolment, firm vouchers |

Everything that cannot finish lands in one **work queue** with the role who should act, a due time, and the action ready where possible; items close themselves when the condition clears. On the sample day, 288 actions ran without anyone and 74 items went to people (80% straight-through).

## Client insights

Every evening Ledgerline looks through each client's own account for things the client would want to know, and either messages the client (Auto) or puts them on the relationship managers' call list (Suggest), set per kind on Autopilot:

| Insight | When | Worth to the client |
| --- | --- | --- |
| Tax-loss harvesting | 15 Jan to the last trading day that settles by 31 March | Tax saved by booking open losses against gains already booked (STCL against STCG then LTCG, LTCL against LTCG), or long-term gains still within the ₹1.25 lakh exemption. A reminder goes a week before the last day. |
| Buy-back eligibility | While an offer priced above the market is open | Category (small shareholder up to ₹2 lakh, or general), entitlement from the letter-of-offer ratios, premium on it. Clients tender from the portal. |
| Physical settlement at expiry | Three business days before expiry | Stock futures and in-the-money stock options that would settle by delivery, with the delivery value. |
| Advance tax | Ten days before each instalment | Capital gains booked this year and the estimated tax on them. |
| Dividends and bonuses | Record date within ten days | Holders' entitlement. |
| Idle funds | Credit balance, no positions, no trade for 30 days | The funds, and that they can be withdrawn. |
| Margin from holdings | Derivatives clients with unpledged holdings | Margin available after haircuts. |
| Account turning inactive | 60 days before the exchanges' inactivity date | What keeps it active. |

Messages are information about the client's own account, never a recommendation to buy or sell: each template carries a not-advice line, the compliance officer approves the wording (Messages), clients can turn insights off in the portal, promotional SMS follows the DND register, and a client gets at most one non-urgent insight every three days. Outcomes are tracked: a client who trades in the scrips concerned, tenders or rolls over within 15 days counts as acted on, with the turnover. Clients see their full capital-gains and harvesting working any time under **Tax & insights** in the portal; staff see the same working from the insight. Tax figures are estimates at the listed-equity rates (20% short-term, 12.5% long-term above ₹1.25 lakh, 4% cess), without surcharge or other income.

## Added for brokers switching from other back offices

- **WhatsApp consent.** Every opt-in and opt-out is kept with date, source, person and reference: the account opening form, the client portal, YES/START or STOP on WhatsApp, a recorded call or signed form, or migrated from the previous system (client master columns WhatsApp, WAConsentDate, WAConsentRef). Messages → WhatsApp consent shows the register and sends a one-tap consent link by email or SMS to clients not yet asked; the link opens a public page at `/optin/<firm>/<token>` and records the answer (POST only, so link scanners cannot answer).
- **WhatsApp bot with a one-time code.** Balances, holdings, ledger, contract notes, margin, tax and offers need a code emailed to the registered address; the request is answered once the code is given, and the session lasts 24 hours (Settings).
- **Year-end packs.** Per client and financial year: capital gains, F&O result, charges, closing balance and holdings, and the Form No. 10DB STT particulars per exchange (rule 20AB), recomputed from the trades and matching what billing charged. Emailed in bulk after 31 March; clients open them in the portal.
- **Branch hierarchy and limit requests.** Offices form a tree (zone, region, branch, franchise); staff see the clients of every office under theirs. Branch staff and authorised persons request extra limit for their own clients; head-office risk decides, and the request is queued for risk.
- **Circulars and compliance calendar.** A CIRCULARS file (Source, Ref, Date, Subject; optional Department, URL, Text) is triaged by rules into areas, priority, a summary, the effective date and actions with owners and due dates. Recurring obligations (complaints disclosure, CTR, RAS, internal audit, net worth, system and cyber audit, AP inspection, policy reviews) carry due-date rules and the evidence of each time they were met, and are queued a week ahead. The seeded dates are a starting point to check against current circulars.
- **Clearing deposits.** Cash, FDs, bank guarantees and securities with each clearing corporation, valued after haircut, checked against base minimum capital and the 50% cash-equivalent rule, with maturities queued 30 days ahead.
- **Day close in two parts.** Equity (NSE, BSE: cash, F&O, currency) closes in the evening; commodity (MCX) after its session, usually the next morning. Each part has its own checks (only its trades, breaks, bills, contract notes and margin), rolls its own positions to its own next trading day (MCX follows its approved holiday list), and is followed by a backup. Commodity trades get a separate contract note. The day is closed when every part that traded is; the next day waits for both. Autopilot closes equity in its end-of-day stage and commodity in a 07:00 stage for the last MCX session.
- **Scheduled backups.** After each close (equity, then commodity) the whole firm is backed up into `LEDGERLINE_BACKUP_DIR`: every table, read in one consistent snapshot, compressed and encrypted with the firm's data keys in 1 MiB chunks bound to the file and their position, then read back and checked against its sealed manifest (row counts and SHA-256). The backup of the last trading day of a week, quarter or year (financial year by default) is also weekly, quarterly or yearly; each tier keeps its own count (30/13/12/10 by default). A part not closed by its fallback (equity 23:30, commodity 00:30 the next morning) is backed up anyway; failures are retried four times and emailed to management. `ledgerline backup verify FILE` checks a file; `ledgerline backup restore FILE` (as the database owner, `LEDGERLINE_DB` set) restores a firm into an empty database in one transaction, keeping every ID and seal, and then checks the audit chain. Copy the folder off the server daily, and keep the data keys somewhere other than the backups.
- **Exchange holiday calendar.** NSE's trading and clearing holidays for every segment are fetched from NSE's holiday API (daily in December until next year's list is found, weekly for mid-year changes); BSE's equity, F&O and currency lists follow NSE's; MCX's are uploaded from its circular. A checker or the compliance officer approves each list. Approved equity holidays decide business days, and settlement holidays move pay-in.
- **Customizable dashboard.** Each user arranges their own dashboard from 23 widgets (day progress, KPIs, needs attention, margin utilisation, turnover by segment and hour, open breaks by type, clients closest to their margin, top clients and contracts, buy vs sell, settlement, contract note delivery, approvals waiting, client money segregation, bank receipts, work queue): drag to move, drag the corner to resize, keyboard and menu alternatives, saved on the server per user. An Admin can save a firm default.
- **Metered AI answers.** Each AI answer is priced from the tokens the provider reports, at the provider's list price (`config/ai-prices.json`), converted at your rupee rate, multiplied by your markup (never below 2×) and rounded up to the paisa, then taken from the firm's prepaid AI credit. A firm without enough credit for the largest possible answer gets Ledgy's standard answer instead. Firms see their credit, answers and charges (plus GST) in Settings, but not your cost or markup; you get cost, charge and margin per firm from the provider endpoint. Check the price table against the providers' pages when their prices change.
- **Editions.** The same software in three editions, set per firm by the provider: **Manual** (every step is a person's click; no Autopilot, work queue or timed runs; jobs run by hand), **Autopilot** (routine work runs by itself where safe, people are asked when judgement or approval is needed) and **Full autopilot** (every automation at the most the rules allow; people act only where the regulations require). Set `LEDGERLINE_EDITION` for all firms and `LEDGERLINE_EDITIONS="firm:manual,firm2:full"` per firm. Changes are recorded in the audit trail; a development server can switch edition from Settings. Maker-checker, the caps on each automation and the steps reserved for people are the same in every edition.
- **Data stays in India.** Ledgerline keeps everything in its own database and serves its own pages and fonts, so browsers contact no other site. The Data protection panel (Audit trail → Check seals) lists where data is kept and sent, line by line. `LEDGERLINE_INDIA_ONLY=1` refuses to start with a hosting region outside India (`LEDGERLINE_REGION`) or with Ledgy's AI phrasing, which uses an API outside India.
- **Ledgy, the in-app helper.** An **Ask Ledgy** button on every staff screen (or press `?`) opens a chat panel that answers how to do anything in Ledgerline and what to do when stuck: changing brokerage, adding an AP, a UCC mismatch, a rejected file, a payout that says only part is available, a permission error. Answers come from about 100 short help articles in `config/help` (one per task, with the exact screens and buttons, plus the reconciliation break playbooks), with a button that opens the right screen. Ledgy also checks live data: what still blocks the day close, why a named client can't trade, which breaks are open, what's in your queue, and who may do something (a table generated from the code's own permission checks, and a test fails if they drift). Every error message has an **Ask Ledgy about this** link. Questions it couldn't answer, and answers marked unhelpful, are kept (identifiers masked) for admins and compliance under **Unanswered**, so the articles can be improved. Optionally, with `LEDGERLINE_LEDGY_KEY` set and the firm's switch on in Settings, Claude phrases the answer from the same articles; only the masked question and the articles are sent, never client data.
- **Encryption and a sealed audit trail.** Every client record, workflow record, user, setting and stored file (KYC documents, exchange and bank files, reports, contract notes), and the change history of each, is encrypted with AES-256-GCM before it reaches the database, under a separate key for each firm derived from the server's master key. A copied database or backup reveals no client data; a ciphertext moved to another record or altered is refused, and so is any record or file found unencrypted (it cannot have been written by Ledgerline). Audit entries are only ever written sealed. Identifiers, ledger amounts and trade records stay in clear so the database can index them and enforce balancing and duplicate rules. Each audit entry carries a keyed seal (HMAC-SHA256) linking it to the previous one; Audit trail → Check seals verifies the whole chain and shows any entry that was changed, removed or inserted. PAN and bank account numbers are masked on screen; ops checkers, compliance and administrators can see one in full with a reason, which is recorded. The server sends HSTS over HTTPS, `nosniff`, `no-referrer` and `no-store` on API responses, and accepts TLS 1.2 and 1.3 only. Still to do when hosting: keys in AWS KMS (Mumbai), disk and backup encryption, TLS to the database, and an external VAPT.
- **Online account opening (e-KYC).** A public page at `/open/<firm>` (link it from the website; `?ap=<code>` or `?lead=<id>` credits a partner or lead) takes an applicant through nine steps on a phone: mobile and email codes, PAN check and KRA search, address from DigiLocker or an uploaded proof, a ₹1 bank check, segments with income proof for derivatives, nominee or opt-out, photograph, signature and PAN copy, an in-person verification video with a spoken code, then review, declarations and Aadhaar e-Sign. Progress is saved, so applicants can resume. The signed form and every document are attached to the new client at KYC verification, a WhatsApp choice is kept as consent from the account opening form, and Online applications shows each check's result for staff to verify or reject with a reason. The checks run through one provider interface (PAN, KRA, DigiLocker, penny drop, face match, e-Sign): `-dev` simulates them; without a provider they are marked for staff to check by hand. Connect a KYC vendor and an e-Sign ASP before going live.

## Sign-in

Each person has a user ID, a password (12+ characters, PBKDF2-SHA256, 600,000 iterations) and, when the firm requires it (the default), a time-based two-factor code from an authenticator app. Sessions are HttpOnly, SameSite=Strict cookies that end after 30 minutes idle (configurable), 12 hours, sign-out, or an admin reset. Five wrong attempts lock a user for 15 minutes; sign-in attempts are also limited per address. New users get a one-time password to change at first sign-in. Every sign-in, failure and change is in the audit trail. API keys remain for integrations, with `X-Actor`.

First run against PostgreSQL: start once with `LEDGERLINE_BOOTSTRAP_ADMIN=<tenant>:<user-id>:<password>` to create the first administrator, sign in, change the password, then remove the variable.

## What it does

```
exchange / CC / OMS files ──► ingest ──► trade events ──► recon ──► breaks (OPEN → RESOLVED / CLEARED)
                                              │
                                              └──► billing ──► ledger (journal entries) ──► balances, trial balance, statements
                                                     ▲
                                        charge schedule (effective-dated rates)
```

| Package | Responsibility |
| --- | --- |
| `internal/money` | Exact decimals (`big.Rat`) for prices and rates; ledger amounts in integer paise; one explicit rounding step, half away from zero. No floating point anywhere. |
| `internal/ledger` | Balanced, append-only journal entries with idempotency keys, one-time reversals, balances, trial balance and account statements. |
| `internal/charges` | Brokerage, STT/CTT, exchange transaction charge, SEBI fee, stamp duty and GST from a versioned, effective-dated rate schedule, with per-exchange overrides and configurable rounding. |
| `internal/ingest` | Keeps every uploaded file byte-for-byte with its SHA-256, then parses it with a versioned, config-driven layout into canonical trade events that record their file and line. Strict layouts reject the whole file on any bad row. |
| `internal/recon` | Three-way match of exchange trades against the OMS: missing either side, trade-number mismatch (fuzzy match within 2 s), UCC, side, instrument, quantity, price and missing algo ID. Break IDs are deterministic across reruns. |
| `internal/billing` | One balanced bill per client per exchange segment per day: trade value or futures MTM against the clearing account, and each charge to its own income or payable account. |
| `internal/app` | Daily operations (ingest, recon, resolve, bill, journal, reverse) with an audit event for every action. |
| `internal/pgstore` | PostgreSQL implementation of every store via `database/sql`, with tenant set per transaction for row-level security. |
| `internal/api` | HTTP/JSON API: bearer key per tenant, `X-Actor` on every change, consistent error bodies. |
| `internal/docs` | Versioned document store for master and workflow data (clients, holdings, settings, workflow status), with optimistic concurrency and full change history. Money never lives here. |
| `internal/ops` | Daily operations: onboarding with validation and maker-checker activation, market data and holdings, RMS (VaR+ELM, SPAN-style F&O margin, 50:50 collateral rule, overrides, kill switch, square-off), peak margin and penalties, settlement and CUSPA, contract notes, bank matching and third-party funds, running account settlement, reports, surveillance rules, EOD checks. |
| `internal/ops/clientreports.go` | Client statements and reports for any period: ledger statement, holding statement, portfolio and net worth, P&L and capital gains (first-in first-out over purchase lots, speculative / short-term / long-term, derivatives, charges), trade book, open positions. Purchase history comes from the HOLDING-LOTS file at go-live plus every trade since. |
| `internal/sample` | Deterministic generator for a realistic sample trading day, with injected reconciliation breaks and exceptions. |
| `web/`, `internal/web` | Browser console served at `/`: 19 screens, every number from the API. |
| `cmd/ledgerline` | API server. `cmd/ledgerctl`: demo, file check and charge quote CLI. |
| `db/migrations` | Schema with guarantees enforced by the database itself (below). |

### Guarantees enforced by PostgreSQL

These hold even if application code has a bug:

1. **Every journal entry balances.** A deferred constraint trigger checks debits equal credits, with at least two postings, at commit, including postings added later to an old entry.
2. **History cannot be edited.** UPDATE, DELETE and TRUNCATE on journal entries, postings, raw files, trade events and the audit log raise an error, even for the table owner. The application role is never granted those privileges in the first place.
3. **Tenants are isolated.** Row-level security on every table, forced for the owner, keyed on `app.tenant_id`, which the store sets in each transaction. A connection with no tenant set sees nothing.
4. **Posting is idempotent.** `(tenant_id, idem_key)` is unique, and each entry can be reversed only once.

## Verified in this build

- `go test ./...`: 12 packages, including a full generated sample day through every operations module (`internal/ops`), and the original 4-file day through the app layer and HTTP API.
- The console driven end to end in headless Chromium: all 38 screens and their tabs, the client portal, plus onboarding, overrides with a second user, square-off, penalties, rebill, settlement, bank returns, RAS, reports, alerts and EOD.
- Charges checked against hand-worked figures. For example, 10 RELIANCE @ ₹1,412 delivery buy costs ₹16.75: STT 14.12, exchange 0.42, SEBI 0.01, stamp 2.12, GST 0.08.
- Client BH1001's closing balance on the sample day is ₹17,863.72 Dr, computed by hand and asserted in tests.
- `db/tests/run.sh` on PostgreSQL 16: nine schema guarantee tests, then every store SQL statement (including the document store) prepared and executed in order against the live schema.
- The API server, run and exercised with curl.

**PostgreSQL end to end (CI):** every push runs the full generated sample day through every operations module twice, on in-memory stores and on PostgreSQL through the `pgx` driver, and requires all 357 compared figures to match (`internal/pgstore/ops_integration_test.go`). CI fails if the integration tests are skipped.

## Run it

Requires Go 1.24. PostgreSQL 16 is optional for local runs.

```bash
./bin/ledgerline -dev   # then open http://localhost:8080 for the web console
make test        # unit + end-to-end tests, no database needed
make demo        # sample trading day printed step by step
make run         # API on :8080 with in-memory stores; prints a dev API key
```

With PostgreSQL:

```bash
docker compose up --build                   # Postgres with schema + API on :8080
# or, against your own Postgres:
PGHOST=localhost PGUSER=postgres make sqltest   # schema and store SQL tests
make build-pgx                                  # server with the pgx driver
LEDGERLINE_DB='postgres://ledgerline_svc:...@localhost/ledgerline' \
LEDGERLINE_API_KEYS='<32+ random chars>:<tenant id>' \
LEDGERLINE_DATA_KEYS="$(bin/ledgerctl keygen)" bin/ledgerline
```

The service must connect as a **non-superuser** granted `ledgerline_app`. Superusers bypass row-level security.

### Security settings

| Variable | Purpose |
|---|---|
| `LEDGERLINE_DATA_KEYS` or `LEDGERLINE_DATA_KEYS_FILE` | Master keys that encrypt client data, newest first: `2:<base64>,1:<base64>`. **Required with a database**; the server refuses to start without them. A key file must be `chmod 600`. Create a key with `ledgerctl keygen`. Keep copies in a secrets manager: losing every copy makes the data unreadable. |
| `LEDGERLINE_TLS_CERT`, `LEDGERLINE_TLS_KEY` | Serve HTTPS directly (TLS 1.2 minimum). Leave unset when a load balancer terminates TLS. |
| `LEDGERLINE_EDITION`, `LEDGERLINE_EDITIONS` | Edition for every firm (`manual`, `autopilot` or `full`; default `autopilot`) and per firm (`firm:edition,…`). |
| `LEDGERLINE_REGION`, `LEDGERLINE_INDIA_ONLY` | The hosting region, shown on the Data protection panel; with India-only mode (`1`) the server refuses a region outside India and any connection that sends data abroad. |
| `LEDGERLINE_AI_PROVIDER` and `LEDGERLINE_ANTHROPIC_KEY` / `LEDGERLINE_OPENAI_KEY` / `LEDGERLINE_GEMINI_KEY` | Optional. Lets Ledgy phrase answers with Anthropic (default `claude-haiku-5-5`), OpenAI (`gpt-5.6-luna`) or Google Gemini (`gemini-3.1-flash-lite`); `LEDGERLINE_AI_MODEL` picks another priced model, `LEDGERLINE_AI_BASE_URL` a gateway. `LEDGERLINE_LEDGY_KEY` alone still means Anthropic. Each firm must also switch it on in Settings. Only the masked question and help articles are sent. |
| `LEDGERLINE_AI_USDINR`, `LEDGERLINE_AI_MARKUP`, `LEDGERLINE_AI_MIN_CHARGE`, `LEDGERLINE_AI_PRICES` | AI metering: rupees per dollar (required), markup on the provider's cost (default and minimum `2`; lower is refused), an optional minimum charge per answer in rupees, and your own copy of the price table (default `config/ai-prices.json`). The server refuses to start with an AI model that has no price. |
| `LEDGERLINE_BACKUP_DIR` | Folder for firm backups, on a separate disk (`<dir>/<firm>/*.llbak` plus a `.json` listing each). Unset: backups are off (a development server uses a temporary folder). |
| `LEDGERLINE_HOLIDAY_FETCH`, `LEDGERLINE_NSE_URL` | `off` stops fetching exchange holidays (upload the circulars instead); `LEDGERLINE_NSE_URL` points the fetch at a gateway or mirror of NSE's site. |
| `LEDGERLINE_PROVIDER_KEY` | Your key (24+ characters) for the provider endpoints: `POST /provider/ai/credit` `{"tenant","amount","ref"}` adds prepaid credit; `GET /provider/ai/usage?month=YYYY-MM&format=csv` gives each firm's answers, provider cost, charges, margin and balance for invoicing. |

**Rotating keys:** run `ledgerctl keygen -version 2`, set `LEDGERLINE_DATA_KEYS="2:<new>,1:<old>"` and restart. New writes use version 2; everything written under version 1 stays readable. Keep version 1 loaded: stored files and change history are never rewritten, so they stay under the key they were written with.

### API

All endpoints need `Authorization: Bearer <key>`. Changes also need `X-Actor: <user>`.

| Method and path | Purpose |
| --- | --- |
| `GET /v1/adapters` | File layouts available, with versions |
| `POST /v1/files?source=NSE-CM-TRADE&name=…` | Upload a file (raw body). 422 with row-level errors if rejected |
| `GET /v1/trades?date=&origin=EXCHANGE\|OMS` | Parsed trades |
| `POST /v1/recon/runs` `{"date"}` | Reconcile a trade date |
| `GET /v1/recon/breaks?date=&status=` | Breaks with workflow state |
| `POST /v1/recon/breaks/{id}/resolve` `{"resolution"}` | Close a break with a note |
| `POST /v1/billing/runs` `{"date","settlement_prices"}` | Bill a date (idempotent; 207 if some groups conflict) |
| `POST /v1/charges/quote` `{bucket, exchange, side, qty, price, date}` | Charge breakdown for an order |
| `POST /v1/journal` `{key, value_date, narration, postings}` | Manual journal entry |
| `POST /v1/journal/{id}/reverse` `{"reason"}` | Reverse an entry |
| `GET /v1/journal/{id}` | One entry |
| `GET /v1/ledger/trial-balance?as_of=` | Trial balance |
| `GET /v1/ledger/statement?ucc=&from=&to=` | Account statement with opening balance |
| `GET /v1/audit?limit=` | Audit trail, newest first |

Operations endpoints (`internal/api/ops.go`): `/v1/me`, `/v1/users`, `/v1/settings`, `/v1/dashboard`, `/v1/clients`, `/v1/market`, `/v1/collateral`, `/v1/pledges`, `/v1/rms`, `/v1/margin`, `/v1/settlement`, `/v1/cuspa`, `/v1/notes`, `/v1/bank`, `/v1/funds`, `/v1/ras`, `/v1/reports`, `/v1/alerts`, `/v1/surveillance/run`, `/v1/ipo`, `/v1/mf`, `/v1/ccm`, `/v1/eod`, and client reports at `/v1/client-reports/{ledger|holdings|portfolio|pnl|tradebook|positions}?ucc=&from=&to=&format=json|csv|html`. Mutating calls check the `X-Actor` user's role: Ops maker, Ops checker, Risk manager, Compliance officer or Admin.

Try it with the dev server running:

```bash
K='Authorization: Bearer dev-key-not-for-production'; A='X-Actor: you'
curl -H "$K" -H "$A" --data-binary @testdata/sample/nse_cm_trades.csv 'localhost:8080/v1/files?source=NSE-CM-TRADE'
curl -H "$K" -X POST -d '{"bucket":"FO-OPT","exchange":"NSE","side":"S","qty":130,"price":"112.50","date":"2026-10-16"}' localhost:8080/v1/charges/quote
```

## Placeholders to replace during pilot discovery

| Item | Where | Why |
| --- | --- | --- |
| Exchange and OMS file layouts | `config/adapters/*.json` (`0.1.0-sample`) | Column lists are illustrative. Map each real file from the pilot broker's exchange, clearing and OMS specifications, then add it as a new version. Old versions stay so historical files still parse. |
| Charge rates | `config/charges/sample-schedule.json` (`"sample": true`) | Rates are illustrative. Load current exchange, clearing and statutory circulars and the broker's brokerage plans. Billing warns while a sample schedule is active. |
| Settlement prices | `testdata/sample/settlement_prices.json` | In production these come from the clearing corporation's daily files via an adapter. |

## Design decisions

- **Exchange records are the source of truth.** Billing uses exchange trades only. The OMS is reconciled against the exchange, never the reverse.
- **One bill per client, segment and date.** The idempotency key is `BILL:<date>:<ucc>:<exchange>-<segment>`. Re-running billing is safe. If trades change after billing (for example a client code modification), the run reports a conflict: reverse the old bill, then re-bill. Automating that rebill is part of step 2.
- **Rounding** happens once per charge component per trade, half away from zero, with STT and stamp duty configurable to the rupee. GST is levied on rounded brokerage, exchange charge and SEBI fee. Confirm the rounding convention with the pilot's auditor; it is configuration.
- **Uploads are facts.** Every upload is stored, even a duplicate (linked via `prior_id`). Trade rows are idempotent: identical repeats are ignored, and conflicting repeats reject the whole batch.

## Next steps

Before a pilot:

1. **Real file layouts and rates** from the pilot broker (see placeholders above), including the clearing corporations' SPAN and obligation files, the depository's DIS batch and the front end's limit format.
2. **Connectors:** exchange extranet and depository APIs, KRA/CKYC, bank statement feed and payout API, SMS/WhatsApp providers, mail relay with digital signing.
3. **Exact regulatory formats** for each report.
4. **Hosting in India**, backups, and the SEBI cyber security framework (CSCRF) evidence.
