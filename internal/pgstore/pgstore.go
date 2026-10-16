// Package pgstore implements every store interface on PostgreSQL using only
// database/sql, so it compiles without a driver. Register a driver in the
// binary (cmd/ledgerline uses github.com/jackc/pgx/v5/stdlib behind the
// "pgx" build tag) and pass its name to Open.
//
// Each operation runs in a transaction that first sets app.tenant_id, which
// the schema's row-level security policies use to isolate tenants.
package pgstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/recon"
)

// Store implements ledger.Store directly; RawStore, TradeStore, BreakStore
// and AuditStore return views for the other interfaces, whose method names
// overlap with the ledger's.
type Store struct{ DB *sql.DB }

var (
	_ ledger.Store      = (*Store)(nil)
	_ ingest.RawStore   = rawStoreAdapter{}
	_ ingest.TradeStore = tradeStoreAdapter{}
	_ app.BreakStore    = breakStoreAdapter{}
	_ app.AuditStore    = auditStoreAdapter{}
)

// Open connects and pings.
func Open(ctx context.Context, driver, dsn string) (*Store, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("pgstore: ping: %w", err)
	}
	return &Store{DB: db}, nil
}

// Stores returns the bundle for app.New.
func (s *Store) Stores() app.Stores {
	return app.Stores{Ledger: s, Raw: s.RawStore(), Trades: s.TradeStore(), Breaks: s.BreakStore(), Audit: s.AuditStore()}
}

func (s *Store) tx(ctx context.Context, tenant string, fn func(*sql.Tx) error) error {
	if tenant == "" {
		return errors.New("pgstore: tenant is required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, Q["set_tenant"], tenant); err != nil {
		tx.Rollback()
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Q holds every statement, so they can be checked against a live schema
// (see TestDumpPrepared and db/tests/check_prepared.sh).
var Q = map[string]string{
	"set_tenant": `SELECT set_config('app.tenant_id', $1::text, true)`,

	"entry_by_key":   `SELECT id, fingerprint FROM ll.journal_entry WHERE tenant_id = $1::text AND idem_key = $2::text`,
	"entry_exists":   `SELECT 1 FROM ll.journal_entry WHERE tenant_id = $1::text AND id = $2::text`,
	"upsert_account": `INSERT INTO ll.account (tenant_id, code, type) VALUES ($1::text, $2::text, $3::text) ON CONFLICT DO NOTHING`,
	"insert_entry": `WITH s AS (SELECT nextval('ll.journal_seq') AS n)
INSERT INTO ll.journal_entry (tenant_id, id, seq, idem_key, value_date, narration, source_kind, source_id, source_line, reversal_of, fingerprint, created_by)
SELECT $1::text, 'JE-' || lpad(s.n::text, 10, '0'), s.n, $2::text, $3::date, $4::text, $5::text, $6::text, nullif($7::int, 0), nullif($8::text, ''), $9::text, $10::text FROM s
RETURNING id, seq, created_at`,
	"insert_posting": `INSERT INTO ll.posting (tenant_id, entry_id, line, account_code, debit_paise, credit_paise, memo)
VALUES ($1::text, $2::text, $3::smallint, $4::text, $5::bigint, $6::bigint, nullif($7::text, ''))`,
	"list_entries": `SELECT e.id, e.seq, e.idem_key, to_char(e.value_date, 'YYYY-MM-DD'), e.narration, e.source_kind, e.source_id,
       coalesce(e.source_line, 0), coalesce(e.reversal_of, ''), e.created_by, e.created_at,
       p.account_code, p.debit_paise, p.credit_paise, coalesce(p.memo, '')
FROM ll.journal_entry e
JOIN ll.posting p ON p.tenant_id = e.tenant_id AND p.entry_id = e.id
WHERE e.tenant_id = $1::text
  AND ($2::text = '' OR e.id = $2::text)
  AND e.value_date >= coalesce(nullif($3::text, '')::date, '-infinity'::date)
  AND e.value_date <= coalesce(nullif($4::text, '')::date, 'infinity'::date)
  AND ($5::text = '' OR EXISTS (SELECT 1 FROM ll.posting q WHERE q.tenant_id = e.tenant_id AND q.entry_id = e.id AND q.account_code = $5::text))
ORDER BY e.value_date, e.seq, p.line`,

	"insert_raw": `WITH s AS (SELECT nextval('ll.raw_file_seq') AS n),
     prior AS (SELECT min(id) AS id FROM ll.raw_file WHERE tenant_id = $1::text AND source = $2::text AND sha256 = $4::text)
INSERT INTO ll.raw_file (tenant_id, id, source, name, sha256, size_bytes, prior_id, content)
SELECT $1::text, 'RF-' || lpad(s.n::text, 8, '0'), $2::text, $3::text, $4::text, $5::bigint, prior.id, $6::bytea FROM s, prior
RETURNING id, coalesce(prior_id, ''), received_at`,
	"get_raw": `SELECT id, source, name, sha256, size_bytes, coalesce(prior_id, ''), received_at, content FROM ll.raw_file WHERE tenant_id = $1::text AND id = $2::text`,

	"insert_trade": `INSERT INTO ll.trade_event (tenant_id, origin, exchange, segment, trade_date, trade_no, order_no, ucc, symbol, instrument_type,
  expiry, strike, side, qty, price, trade_time, product, algo_id, terminal, file_id, line)
VALUES ($1::text, $2::text, $3::text, $4::text, $5::date, $6::text, nullif($7::text, ''), $8::text, $9::text, $10::text,
  nullif($11::text, '')::date, nullif($12::text, '')::numeric, $13::text, $14::bigint, $15::numeric, nullif($16::text, '')::time,
  nullif($17::text, ''), nullif($18::text, ''), nullif($19::text, ''), nullif($20::text, ''), nullif($21::int, 0))
ON CONFLICT DO NOTHING`,
	"list_trades": `SELECT origin, exchange, segment, to_char(trade_date, 'YYYY-MM-DD'), trade_no, coalesce(order_no, ''), ucc, symbol, instrument_type,
  coalesce(to_char(expiry, 'YYYY-MM-DD'), ''), coalesce(trim_scale(strike)::text, ''), side, qty, price::text,
  coalesce(to_char(trade_time, 'HH24:MI:SS'), ''), coalesce(product, ''), coalesce(algo_id, ''), coalesce(terminal, ''), coalesce(file_id, ''), coalesce(line, 0)
FROM ll.trade_event
WHERE tenant_id = $1::text AND ($2::text = '' OR origin = $2::text) AND ($3::text = '' OR trade_date = nullif($3::text, '')::date)
  AND ($4::text = '' OR (exchange || '|' || segment || '|' || to_char(trade_date, 'YYYY-MM-DD') || '|' || trade_no) = $4::text)
ORDER BY trade_date, exchange, segment, trade_no`,

	"insert_run": `INSERT INTO ll.recon_run (tenant_id, id, trade_date, exchange_count, oms_count, matched, breaks, run_at)
VALUES ($1::text, $2::text, $3::date, $4::int, $5::int, $6::int, $7::int, $8::timestamptz)`,
	"upsert_break": `INSERT INTO ll.recon_break AS b (tenant_id, id, trade_date, type, exchange, segment, trade_no, oms_trade_no, ucc, instrument, value, diffs, suggestion, first_seen, last_run)
VALUES ($1::text, $2::text, $3::date, $4::text, $5::text, $6::text, $7::text, nullif($8::text, ''), $9::text, $10::text, $11::numeric, $12::jsonb, $13::text, $14::timestamptz, $15::text)
ON CONFLICT (tenant_id, id) DO UPDATE SET value = EXCLUDED.value, diffs = EXCLUDED.diffs, last_run = EXCLUDED.last_run,
  status = CASE WHEN b.status = 'CLEARED' THEN 'OPEN' ELSE b.status END`,
	"clear_breaks": `UPDATE ll.recon_break SET status = 'CLEARED', last_run = $3::text
WHERE tenant_id = $1::text AND trade_date = $2::date AND status = 'OPEN' AND last_run <> $3::text`,
	"list_breaks": `SELECT id, to_char(trade_date, 'YYYY-MM-DD'), type, exchange, segment, trade_no, coalesce(oms_trade_no, ''), ucc, instrument, value::text,
  diffs::text, suggestion, status, coalesce(resolution, ''), coalesce(resolved_by, ''), resolved_at, first_seen, last_run
FROM ll.recon_break
WHERE tenant_id = $1::text AND ($2::text = '' OR trade_date = nullif($2::text, '')::date) AND ($3::text = '' OR status = $3::text) AND ($4::text = '' OR id = $4::text)
ORDER BY trade_date, type, trade_no`,
	"resolve_break": `UPDATE ll.recon_break SET status = 'RESOLVED', resolution = $3::text, resolved_by = $4::text, resolved_at = $5::timestamptz
WHERE tenant_id = $1::text AND id = $2::text AND status = 'OPEN'`,

	"insert_audit": `INSERT INTO ll.audit_log (tenant_id, at, actor, action, object, detail) VALUES ($1::text, $2::timestamptz, $3::text, $4::text, $5::text, $6::jsonb)`,
	"list_audit":   `SELECT at, actor, action, object, coalesce(detail::text, '') FROM ll.audit_log WHERE tenant_id = $1::text ORDER BY id DESC LIMIT $2::int`,
}

// ---- ledger.Store ----

func (s *Store) Append(ctx context.Context, e ledger.Entry) (ledger.Entry, bool, error) {
	if err := e.Validate(); err != nil {
		return ledger.Entry{}, false, err
	}
	fp := e.Fingerprint()
	var out ledger.Entry
	created := false
	err := s.tx(ctx, e.Tenant, func(tx *sql.Tx) error {
		var id, efp string
		switch err := tx.QueryRowContext(ctx, Q["entry_by_key"], e.Tenant, e.Key).Scan(&id, &efp); {
		case err == nil:
			if strings.TrimSpace(efp) != fp {
				return fmt.Errorf("%w (key %q, existing %s)", ledger.ErrConflict, e.Key, id)
			}
			es, err := s.listTx(ctx, tx, e.Tenant, id, ledger.Filter{})
			if err != nil || len(es) != 1 {
				return fmt.Errorf("pgstore: reload %s: %v", id, err)
			}
			out = es[0]
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if e.ReversalOf != "" {
			var one int
			if err := tx.QueryRowContext(ctx, Q["entry_exists"], e.Tenant, e.ReversalOf).Scan(&one); err != nil {
				return fmt.Errorf("%w: reversal target %s", ledger.ErrNotFound, e.ReversalOf)
			}
		}
		for _, p := range e.Postings {
			t, _ := ledger.TypeOf(p.Account)
			if _, err := tx.ExecContext(ctx, Q["upsert_account"], e.Tenant, p.Account, string(t)); err != nil {
				return err
			}
		}
		out = e
		out.Postings = append([]ledger.Posting(nil), e.Postings...)
		if err := tx.QueryRowContext(ctx, Q["insert_entry"], e.Tenant, e.Key, e.ValueDate, e.Narration, e.Source.Kind, e.Source.ID, e.Source.Line, e.ReversalOf, fp, e.CreatedBy).
			Scan(&out.ID, &out.Seq, &out.CreatedAt); err != nil {
			return err
		}
		for i, p := range e.Postings {
			if _, err := tx.ExecContext(ctx, Q["insert_posting"], e.Tenant, out.ID, i+1, p.Account, p.Debit.Paise(), p.Credit.Paise(), p.Memo); err != nil {
				return err
			}
		}
		created = true
		return nil
	})
	if err != nil {
		// A concurrent writer may have inserted the same key between our check and insert.
		if !created && !errors.Is(err, ledger.ErrConflict) && !errors.Is(err, ledger.ErrNotFound) && strings.Contains(err.Error(), "idem_key") {
			return s.Append(ctx, e)
		}
		return ledger.Entry{}, false, err
	}
	return out, created, nil
}

func (s *Store) Get(ctx context.Context, tenant, id string) (ledger.Entry, error) {
	var out []ledger.Entry
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		var err error
		out, err = s.listTx(ctx, tx, tenant, id, ledger.Filter{})
		return err
	})
	if err != nil {
		return ledger.Entry{}, err
	}
	if len(out) == 0 {
		return ledger.Entry{}, fmt.Errorf("%w: %s", ledger.ErrNotFound, id)
	}
	return out[0], nil
}

func (s *Store) List(ctx context.Context, tenant string, f ledger.Filter) ([]ledger.Entry, error) {
	var out []ledger.Entry
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		var err error
		out, err = s.listTx(ctx, tx, tenant, "", f)
		return err
	})
	return out, err
}

func (s *Store) listTx(ctx context.Context, tx *sql.Tx, tenant, id string, f ledger.Filter) ([]ledger.Entry, error) {
	rows, err := tx.QueryContext(ctx, Q["list_entries"], tenant, id, f.From, f.To, f.Account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ledger.Entry{}
	for rows.Next() {
		var e ledger.Entry
		var p ledger.Posting
		var dr, cr int64
		if err := rows.Scan(&e.ID, &e.Seq, &e.Key, &e.ValueDate, &e.Narration, &e.Source.Kind, &e.Source.ID, &e.Source.Line, &e.ReversalOf, &e.CreatedBy, &e.CreatedAt,
			&p.Account, &dr, &cr, &p.Memo); err != nil {
			return nil, err
		}
		p.Debit, p.Credit = money.Amount(dr), money.Amount(cr)
		if n := len(out); n > 0 && out[n-1].ID == e.ID {
			out[n-1].Postings = append(out[n-1].Postings, p)
			continue
		}
		e.Tenant = tenant
		e.Postings = []ledger.Posting{p}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- ingest.RawStore ----

func (s *Store) Put(ctx context.Context, f ingest.RawFile) (ingest.RawFile, error) {
	f.Size = int64(len(f.Content))
	err := s.tx(ctx, f.Tenant, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, Q["insert_raw"], f.Tenant, f.Source, f.Name, f.SHA256, f.Size, f.Content).Scan(&f.ID, &f.PriorID, &f.ReceivedAt)
	})
	return f, err
}

func (s *Store) GetRaw(ctx context.Context, tenant, id string) (ingest.RawFile, error) {
	f := ingest.RawFile{Tenant: tenant}
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, Q["get_raw"], tenant, id).Scan(&f.ID, &f.Source, &f.Name, &f.SHA256, &f.Size, &f.PriorID, &f.ReceivedAt, &f.Content)
	})
	return f, err
}

// rawStoreAdapter exposes GetRaw under the RawStore method name; Store
// already uses Get for ledger entries.
type rawStoreAdapter struct{ *Store }

func (r rawStoreAdapter) Get(ctx context.Context, tenant, id string) (ingest.RawFile, error) {
	return r.Store.GetRaw(ctx, tenant, id)
}

// RawStore returns the raw-file view of the store.
func (s *Store) RawStore() ingest.RawStore { return rawStoreAdapter{s} }

// ---- ingest.TradeStore ----

func (s *Store) Add(ctx context.Context, evs []ingest.TradeEvent) (int, int, error) {
	if len(evs) == 0 {
		return 0, 0, nil
	}
	added, dup := 0, 0
	tenant := evs[0].Tenant
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		var conflicts []string
		for _, e := range evs {
			if e.Tenant != tenant {
				return errors.New("pgstore: one batch must belong to one tenant")
			}
			res, err := tx.ExecContext(ctx, Q["insert_trade"], e.Tenant, string(e.Origin), e.Exchange, e.Segment, e.TradeDate, e.TradeNo, e.OrderNo, e.UCC, e.Symbol,
				e.InstrumentType, e.Expiry, e.Strike, e.Side, e.Qty, e.Price.String(), e.TradeTime, e.Product, e.AlgoID, e.Terminal, e.FileID, e.Line)
			if err != nil {
				return fmt.Errorf("trade %s: %w", e.TradeNo, err)
			}
			if n, _ := res.RowsAffected(); n == 1 {
				added++
				continue
			}
			ex, err := s.listTradesTx(ctx, tx, tenant, string(e.Origin), "", e.Key())
			if err != nil || len(ex) != 1 {
				return fmt.Errorf("trade %s: reload existing: %v", e.TradeNo, err)
			}
			if !ex[0].SameContent(e) {
				conflicts = append(conflicts, e.TradeNo)
				continue
			}
			dup++
		}
		if len(conflicts) > 0 {
			return fmt.Errorf("%w: %s", ingest.ErrTradeConflict, strings.Join(conflicts, ", "))
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return added, dup, nil
}

func (s *Store) ListTrades(ctx context.Context, tenant string, origin ingest.Origin, date string) ([]ingest.TradeEvent, error) {
	var out []ingest.TradeEvent
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		var err error
		out, err = s.listTradesTx(ctx, tx, tenant, string(origin), date, "")
		return err
	})
	return out, err
}

func (s *Store) listTradesTx(ctx context.Context, tx *sql.Tx, tenant, origin, date, key string) ([]ingest.TradeEvent, error) {
	rows, err := tx.QueryContext(ctx, Q["list_trades"], tenant, origin, date, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ingest.TradeEvent{}
	for rows.Next() {
		e := ingest.TradeEvent{Tenant: tenant}
		var org, price string
		if err := rows.Scan(&org, &e.Exchange, &e.Segment, &e.TradeDate, &e.TradeNo, &e.OrderNo, &e.UCC, &e.Symbol, &e.InstrumentType,
			&e.Expiry, &e.Strike, &e.Side, &e.Qty, &price, &e.TradeTime, &e.Product, &e.AlgoID, &e.Terminal, &e.FileID, &e.Line); err != nil {
			return nil, err
		}
		e.Origin = ingest.Origin(org)
		if e.Price, err = money.ParseDec(price); err != nil {
			return nil, err
		}
		if e.Strike != "" {
			k, err := money.ParseDec(e.Strike)
			if err != nil {
				return nil, err
			}
			e.Strike = k.String()
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// tradeStoreAdapter exposes ListTrades under the TradeStore method name.
type tradeStoreAdapter struct{ *Store }

func (t tradeStoreAdapter) List(ctx context.Context, tenant string, origin ingest.Origin, date string) ([]ingest.TradeEvent, error) {
	return t.Store.ListTrades(ctx, tenant, origin, date)
}

// TradeStore returns the trade view of the store.
func (s *Store) TradeStore() ingest.TradeStore { return tradeStoreAdapter{s} }

// ---- app.BreakStore ----

func (s *Store) SaveRun(ctx context.Context, tenant, runID string, r recon.Result, at time.Time) error {
	return s.tx(ctx, tenant, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, Q["insert_run"], tenant, runID, r.Date, r.ExchangeCount, r.OMSCount, r.Matched, len(r.Breaks), at); err != nil {
			return err
		}
		for _, b := range r.Breaks {
			diffs, _ := json.Marshal(b.Diffs)
			if _, err := tx.ExecContext(ctx, Q["upsert_break"], tenant, b.ID, b.TradeDate, string(b.Type), b.Exchange, b.Segment, b.TradeNo, b.OMSTradeNo, b.UCC,
				b.Instrument, b.Value.String(), string(diffs), b.Suggestion, at, runID); err != nil {
				return fmt.Errorf("break %s: %w", b.ID, err)
			}
		}
		_, err := tx.ExecContext(ctx, Q["clear_breaks"], tenant, r.Date, runID)
		return err
	})
}

func (s *Store) ListBreaks(ctx context.Context, tenant, date, status, id string) ([]app.BreakRecord, error) {
	var out []app.BreakRecord
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, Q["list_breaks"], tenant, date, status, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b app.BreakRecord
			var typ, value, diffs string
			var resolvedAt sql.NullTime
			if err := rows.Scan(&b.ID, &b.TradeDate, &typ, &b.Exchange, &b.Segment, &b.TradeNo, &b.OMSTradeNo, &b.UCC, &b.Instrument, &value,
				&diffs, &b.Suggestion, &b.Status, &b.Resolution, &b.ResolvedBy, &resolvedAt, &b.FirstSeen, &b.LastRun); err != nil {
				return err
			}
			b.Type = recon.BreakType(typ)
			if b.Value, err = money.ParseDec(value); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(diffs), &b.Diffs); err != nil {
				return err
			}
			if resolvedAt.Valid {
				t := resolvedAt.Time
				b.ResolvedAt = &t
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	if out == nil {
		out = []app.BreakRecord{}
	}
	return out, err
}

func (s *Store) Resolve(ctx context.Context, tenant, id, actor, resolution string, at time.Time) (app.BreakRecord, error) {
	var n int64
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, Q["resolve_break"], tenant, id, resolution, actor, at)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	if err != nil {
		return app.BreakRecord{}, err
	}
	bs, err := s.ListBreaks(ctx, tenant, "", "", id)
	if err != nil {
		return app.BreakRecord{}, err
	}
	if len(bs) == 0 {
		return app.BreakRecord{}, fmt.Errorf("%w: break %s", app.ErrNotFound, id)
	}
	if n == 0 {
		return app.BreakRecord{}, fmt.Errorf("%w: break %s is %s", app.ErrState, id, bs[0].Status)
	}
	return bs[0], nil
}

// breakStoreAdapter exposes ListBreaks under the BreakStore method name.
type breakStoreAdapter struct{ *Store }

func (b breakStoreAdapter) List(ctx context.Context, tenant, date, status string) ([]app.BreakRecord, error) {
	return b.Store.ListBreaks(ctx, tenant, date, status, "")
}

// BreakStore returns the break view of the store.
func (s *Store) BreakStore() app.BreakStore { return breakStoreAdapter{s} }

// ---- app.AuditStore ----

func (s *Store) Log(ctx context.Context, e app.AuditEvent) error {
	var detail any
	if e.Detail != nil {
		b, _ := json.Marshal(e.Detail)
		detail = string(b)
	}
	return s.tx(ctx, e.Tenant, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, Q["insert_audit"], e.Tenant, e.At, e.Actor, e.Action, e.Object, detail)
		return err
	})
}

func (s *Store) ListAudit(ctx context.Context, tenant string, limit int) ([]app.AuditEvent, error) {
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	out := []app.AuditEvent{}
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, Q["list_audit"], tenant, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e := app.AuditEvent{Tenant: tenant}
			var detail string
			if err := rows.Scan(&e.At, &e.Actor, &e.Action, &e.Object, &detail); err != nil {
				return err
			}
			if detail != "" {
				_ = json.Unmarshal([]byte(detail), &e.Detail)
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// auditStoreAdapter exposes ListAudit under the AuditStore method name.
type auditStoreAdapter struct{ *Store }

func (a auditStoreAdapter) List(ctx context.Context, tenant string, limit int) ([]app.AuditEvent, error) {
	return a.Store.ListAudit(ctx, tenant, limit)
}

// AuditStore returns the audit view of the store.
func (s *Store) AuditStore() app.AuditStore { return auditStoreAdapter{s} }
