# Ledgerline back office

The production core of a multi-tenant back office for Indian stockbrokers: file ingestion, trade reconciliation, charges, EOD billing and an append-only double-entry ledger, on PostgreSQL. It covers equity cash, F&O, currency and commodity segments.

On top of that core sit the daily operations modules: client onboarding, RMS and limits, peak-margin reporting, collateral and pledge, settlement obligations and unpaid securities, contract notes, bank reconciliation, running account settlement, regulatory reports, surveillance, IPO and mutual fund orders, and EOD close. A browser console served by the same binary drives all of it.

## Try it in two minutes

```bash
./bin/ledgerline -dev        # then open http://localhost:8080
```

On the dashboard, click **Load sample trading day**. It generates about 60 clients and 355 trades across NSE, BSE and MCX (cash, F&O, currency, commodity), holdings, carried positions, pledge requests, a bank statement, exchange alerts and IPO/MF orders, and pushes each one through the normal file loaders. Then work the day: run reconciliation, resolve breaks, bill, settle, send contract notes, square off breaches, and close the day. Switch the **Signed in as** user at top right to see maker-checker and role rules in action.

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
- The console driven end to end in headless Chromium: every screen, plus onboarding, overrides with a second user, square-off, penalties, rebill, settlement, bank returns, RAS, reports, alerts and EOD.
- Charges checked against hand-worked figures. For example, 10 RELIANCE @ ₹1,412 delivery buy costs ₹16.75: STT 14.12, exchange 0.42, SEBI 0.01, stamp 2.12, GST 0.08.
- Client BH1001's closing balance on the sample day is ₹17,863.72 Dr, computed by hand and asserted in tests.
- `db/tests/run.sh` on PostgreSQL 16: nine schema guarantee tests, then every store SQL statement (including the document store) prepared and executed in order against the live schema.
- The API server, run and exercised with curl.

**Not run in this build:** the Go-to-PostgreSQL path through the `pgx` driver. The build environment could not download Go modules, so `internal/pgstore/integration_test.go` (full sample day against PostgreSQL) runs in CI, and on your machine after `go get github.com/jackc/pgx/v5`. All of its SQL was already verified directly on PostgreSQL as described above.

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
LEDGERLINE_API_KEYS='<32+ random chars>:<tenant id>' bin/ledgerline
```

The service must connect as a **non-superuser** granted `ledgerline_app`. Superusers bypass row-level security.

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

Operations endpoints (`internal/api/ops.go`): `/v1/me`, `/v1/users`, `/v1/settings`, `/v1/dashboard`, `/v1/clients`, `/v1/market`, `/v1/collateral`, `/v1/pledges`, `/v1/rms`, `/v1/margin`, `/v1/settlement`, `/v1/cuspa`, `/v1/notes`, `/v1/bank`, `/v1/funds`, `/v1/ras`, `/v1/reports`, `/v1/alerts`, `/v1/surveillance/run`, `/v1/ipo`, `/v1/mf`, `/v1/ccm`, `/v1/eod`. Mutating calls check the `X-Actor` user's role: Ops maker, Ops checker, Risk manager, Compliance officer or Admin.

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

1. **Real file layouts and rates** from the pilot broker (see placeholders above), including the clearing corporations' SPAN and obligation files.
2. **Connectors:** exchange extranet and depository APIs for BOD/EOD file pulls, OMS limit push and square-off, bank statement feed, mail relay with digital signing for contract notes.
3. **Sign-in:** single sign-on with per-user credentials, replacing the shared tenant key plus `X-Actor`.
4. **Exact regulatory formats** for each report, and auction handling for short deliveries.
5. **Option exercise and assignment** at expiry.
