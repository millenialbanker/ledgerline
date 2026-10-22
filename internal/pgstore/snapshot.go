package pgstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ledgerline/internal/app"
)

// ---- whole-firm snapshots, for backups ----
//
// Snapshot reads every row of one firm, table by table, inside a single
// read-only REPEATABLE READ transaction, so the backup is one consistent
// point in time even while people keep working. Rows are copied as stored
// (row_to_json), with document bodies and files still encrypted.
//
// Restore writes them back into a database where the firm has no data,
// inside one transaction: all of it or none. It keeps every ID, sequence
// number and audit seal, then moves the database's counters past the
// restored numbers. That needs the database owner's account, not the
// application's (which may only insert, and cannot move counters).

// snapTables lists the firm's tables, parents before children, with the
// order rows are read in.
var snapTables = []struct{ name, order string }{
	{"tenant", "id"},
	{"account", "code"},
	{"charge_schedule", "version"},
	{"raw_file", "id"},
	{"trade_event", "origin, exchange, segment, trade_date, trade_no"},
	{"recon_run", "id"},
	{"recon_break", "id"},
	{"journal_entry", "seq"},
	{"posting", "entry_id, line"},
	{"audit_log", "id"},
	{"doc", "coll, id"},
	{"doc_history", "seq"},
}

func (s *Store) Backend() string { return "postgres" }

// Snapshot implements app.Snapshotter.
func (s *Store) Snapshot(ctx context.Context, tenant string, emit func(app.Record) error) error {
	if tenant == "" {
		return errors.New("pgstore: tenant is required")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, Q["set_tenant"], tenant); err != nil {
		return err
	}
	for _, t := range snapTables {
		col := "tenant_id"
		if t.name == "tenant" {
			col = "id"
		}
		rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT row_to_json(t)::text FROM ll.%s t WHERE %s = $1 ORDER BY %s`, t.name, col, t.order), tenant)
		if err != nil {
			return fmt.Errorf("snapshot %s: %w", t.name, err)
		}
		for rows.Next() {
			var b string
			if err := rows.Scan(&b); err != nil {
				rows.Close()
				return err
			}
			if err := emit(app.Record{Table: t.name, Row: json.RawMessage(b)}); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return tx.Commit()
}

// Restore implements app.Snapshotter.
func (s *Store) Restore(ctx context.Context, tenant string, next func() (app.Record, bool, error)) (map[string]int, error) {
	known := map[string]bool{}
	for _, t := range snapTables {
		known[t.name] = true
	}
	n := map[string]int{}
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		// The firm must be empty: nothing here may be overwritten.
		for _, t := range snapTables[1:] {
			var c int
			if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM ll.%s WHERE tenant_id = $1`, t.name), tenant).Scan(&c); err != nil {
				return fmt.Errorf("restore: %w (restoring needs the database owner's account)", err)
			}
			if c > 0 {
				return fmt.Errorf("%w: firm %s already has %d rows in %s; restore into an empty database", app.ErrState, tenant, c, t.name)
			}
		}
		// The history trigger would copy each restored document into its
		// history a second time (the backup has the history already), so it
		// is off for this transaction only. That needs the table's owner and
		// locks the table until the restore commits or rolls back.
		if _, err := tx.ExecContext(ctx, `ALTER TABLE ll.doc DISABLE TRIGGER doc_history`); err != nil {
			return fmt.Errorf("restore: %w (restoring needs the database owner's account)", err)
		}
		for {
			r, ok, err := next()
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			if !known[r.Table] {
				return fmt.Errorf("restore: unknown table %q", r.Table)
			}
			var head struct {
				Tenant string `json:"tenant_id"`
				ID     any    `json:"id"`
			}
			if err := json.Unmarshal(r.Row, &head); err != nil {
				return fmt.Errorf("restore: %s row %d: %w", r.Table, n[r.Table]+1, err)
			}
			if r.Table == "tenant" {
				if fmt.Sprint(head.ID) != tenant {
					return fmt.Errorf("restore: backup is of firm %v, not %s", head.ID, tenant)
				}
				// The firm's row may already exist (created when the
				// database was set up); keep it.
				if _, err := tx.ExecContext(ctx, `INSERT INTO ll.tenant SELECT * FROM json_populate_record(NULL::ll.tenant, $1::json) ON CONFLICT (id) DO NOTHING`, string(r.Row)); err != nil {
					return fmt.Errorf("restore: firm row: %w", err)
				}
				n[r.Table]++
				continue
			}
			if head.Tenant != tenant {
				return fmt.Errorf("restore: a %s row belongs to firm %q, not %s", r.Table, head.Tenant, tenant)
			}
			over := ""
			if r.Table == "audit_log" || r.Table == "doc_history" {
				over = " OVERRIDING SYSTEM VALUE"
			}
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO ll.%s%s SELECT * FROM json_populate_record(NULL::ll.%s, $1::json)`, r.Table, over, r.Table), string(r.Row)); err != nil {
				return fmt.Errorf("restore: %s row %d: %w", r.Table, n[r.Table]+1, err)
			}
			n[r.Table]++
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE ll.doc ENABLE TRIGGER doc_history`); err != nil {
			return err
		}
		// Move the counters past every restored number, never backwards.
		for _, q := range []string{
			`SELECT setval('ll.journal_seq', greatest((SELECT coalesce(max(seq), 0) FROM ll.journal_entry), (SELECT last_value FROM ll.journal_seq)))`,
			`SELECT setval('ll.raw_file_seq', greatest((SELECT coalesce(max(substring(id from '[0-9]+$')::bigint), 0) FROM ll.raw_file WHERE id ~ '[0-9]+$'), (SELECT last_value FROM ll.raw_file_seq)))`,
			`SELECT setval(pg_get_serial_sequence('ll.audit_log', 'id'), greatest((SELECT coalesce(max(id), 0) FROM ll.audit_log), 1))`,
			`SELECT setval(pg_get_serial_sequence('ll.doc_history', 'seq'), greatest((SELECT coalesce(max(seq), 0) FROM ll.doc_history), 1))`,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				if strings.Contains(err.Error(), "permission denied") {
					return fmt.Errorf("restore: %w (restoring needs the database owner's account)", err)
				}
				return fmt.Errorf("restore: counters: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return n, nil
}
