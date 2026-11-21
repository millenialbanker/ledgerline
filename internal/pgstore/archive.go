package pgstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ledgerline/internal/app"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- archived periods (app.PeriodStore) ----
//
// Archive records live in ll.archive_period. The rows of an archived
// period are removed by ll.archive_remove and put back by
// ll.archive_restore and ll.archive_drop_carry, which check the record
// again in the database and run as the schema owner; the application role
// has no DELETE privilege anywhere (db/migrations/0005_archive_period.sql).
// Rows are read and written as stored (row_to_json), like a backup.

func init() {
	Q["arc_upsert"] = `INSERT INTO ll.archive_period (tenant_id, id, state, period_from, period_to, requested_by, approved_by, package_sha256, verified_at,
  carry_entries, restore_requested_by, restore_approved_by, retain_until, record)
VALUES ($1::text, $2::text, $3::text, $4::date, $5::date, $6::text, nullif($7::text, ''), nullif($8::text, ''), $9::timestamptz,
  $10::int, nullif($11::text, ''), nullif($12::text, ''), nullif($13::text, '')::date, $14::jsonb)
ON CONFLICT (tenant_id, id) DO UPDATE SET state = EXCLUDED.state, period_from = EXCLUDED.period_from, period_to = EXCLUDED.period_to,
  requested_by = EXCLUDED.requested_by, approved_by = EXCLUDED.approved_by, package_sha256 = EXCLUDED.package_sha256, verified_at = EXCLUDED.verified_at,
  carry_entries = EXCLUDED.carry_entries, restore_requested_by = EXCLUDED.restore_requested_by, restore_approved_by = EXCLUDED.restore_approved_by,
  retain_until = EXCLUDED.retain_until, record = EXCLUDED.record`
	Q["arc_get"] = `SELECT record::text FROM ll.archive_period WHERE tenant_id = $1::text AND id = $2::text`
	Q["arc_lock"] = `SELECT record::text FROM ll.archive_period WHERE tenant_id = $1::text AND id = $2::text FOR UPDATE`
	Q["arc_list"] = `SELECT record::text FROM ll.archive_period WHERE tenant_id = $1::text ORDER BY id`
	Q["arc_remove"] = `SELECT ll.archive_remove(tenant_id, id, $3::text, ARRAY(SELECT jsonb_array_elements_text($4::jsonb))) FROM ll.archive_period WHERE tenant_id = $1::text AND id = $2::text`
	Q["arc_restore"] = `SELECT ll.archive_restore(tenant_id, id, $3::text, $4::jsonb) FROM ll.archive_period WHERE tenant_id = $1::text AND id = $2::text`
	Q["arc_drop_carry"] = `SELECT ll.archive_drop_carry(tenant_id, id) FROM ll.archive_period WHERE tenant_id = $1::text AND id = $2::text`
	Q["arc_balances"] = `SELECT p.account_code, sum(p.debit_paise) - sum(p.credit_paise)
FROM ll.posting p JOIN ll.journal_entry e ON e.tenant_id = p.tenant_id AND e.id = p.entry_id
WHERE p.tenant_id = $1::text AND ($2::text = '' OR e.value_date <= $2::date)
GROUP BY p.account_code HAVING sum(p.debit_paise) <> sum(p.credit_paise)`

	// The rows of a period, in the order they are restored. $1 tenant,
	// $2 and $3 the period; the JSON lists are of protected and document
	// sources, clients kept live, and the migrated files and records.
	Q["arc_raw"] = `SELECT row_to_json(f)::text, f.id, f.source, to_char((f.received_at AT TIME ZONE 'Asia/Kolkata')::date, 'YYYY-MM-DD'), octet_length(f.content)
FROM ll.raw_file f
WHERE f.tenant_id = $1::text AND (
  (f.source <> 'MIG-ARCHIVE' AND (f.received_at AT TIME ZONE 'Asia/Kolkata')::date BETWEEN $2::date AND $3::date
   AND NOT (f.source = ANY (ARRAY(SELECT jsonb_array_elements_text($4::jsonb))))
   AND (CASE WHEN f.source = ANY (ARRAY(SELECT jsonb_array_elements_text($5::jsonb))) THEN $7::boolean ELSE $6::boolean END)
   AND NOT EXISTS (SELECT 1 FROM ll.trade_event t WHERE t.tenant_id = f.tenant_id AND t.file_id = f.id
     AND NOT ($8::boolean AND t.trade_date BETWEEN $2::date AND $3::date AND NOT (upper(t.ucc) = ANY (ARRAY(SELECT jsonb_array_elements_text($9::jsonb)))))))
  OR (f.source = 'MIG-ARCHIVE' AND f.id = ANY (ARRAY(SELECT jsonb_array_elements_text($10::jsonb)))))
ORDER BY f.id`
	Q["arc_trades"] = `SELECT row_to_json(t)::text, t.origin || '|' || t.exchange || '|' || t.segment || '|' || to_char(t.trade_date, 'YYYY-MM-DD') || '|' || t.trade_no,
  upper(t.ucc), to_char(t.trade_date, 'YYYY-MM-DD')
FROM ll.trade_event t
WHERE t.tenant_id = $1::text AND t.trade_date BETWEEN $2::date AND $3::date AND NOT (upper(t.ucc) = ANY (ARRAY(SELECT jsonb_array_elements_text($4::jsonb))))
ORDER BY t.trade_date, t.origin, t.exchange, t.segment, t.trade_no`
	Q["arc_heads"] = `SELECT e.id, to_char(e.value_date, 'YYYY-MM-DD'), e.source_kind, coalesce(e.reversal_of, ''), array_to_json(array_agg(p.account_code))::text
FROM ll.journal_entry e JOIN ll.posting p ON p.tenant_id = e.tenant_id AND p.entry_id = e.id
WHERE e.tenant_id = $1::text AND e.value_date BETWEEN $2::date AND $3::date
GROUP BY e.id, e.value_date, e.source_kind, e.reversal_of`
	Q["arc_reversals"] = `SELECT id, reversal_of FROM ll.journal_entry WHERE tenant_id = $1::text AND reversal_of IS NOT NULL`
	Q["arc_entries"] = `SELECT row_to_json(e)::text, e.id, to_char(e.value_date, 'YYYY-MM-DD') FROM ll.journal_entry e
WHERE e.tenant_id = $1::text AND e.value_date BETWEEN $2::date AND $3::date ORDER BY e.seq`
	Q["arc_postings"] = `SELECT row_to_json(p)::text, p.entry_id, p.account_code FROM ll.posting p JOIN ll.journal_entry e ON e.tenant_id = p.tenant_id AND e.id = p.entry_id
WHERE p.tenant_id = $1::text AND e.value_date BETWEEN $2::date AND $3::date ORDER BY e.seq, p.line`
	Q["arc_docs"] = `SELECT row_to_json(d)::text, d.coll || '|' || d.id FROM ll.doc d
WHERE d.tenant_id = $1::text AND d.coll = ANY (ll.archive_doc_colls()) AND (d.coll || '|' || d.id) = ANY (ARRAY(SELECT jsonb_array_elements_text($2::jsonb)))
ORDER BY d.coll, d.id`
	Q["arc_dochist"] = `SELECT row_to_json(h)::text, h.seq::text, to_char((h.updated_at AT TIME ZONE 'Asia/Kolkata')::date, 'YYYY-MM-DD'), h.coll || '|' || h.id
FROM ll.doc_history h
WHERE h.tenant_id = $1::text AND (
  ($4::boolean AND (h.updated_at AT TIME ZONE 'Asia/Kolkata')::date BETWEEN $2::date AND $3::date AND NOT (h.coll = ANY (ll.archive_doc_colls()))
   AND NOT (h.coll = 'client' AND upper(h.id) = ANY (ARRAY(SELECT jsonb_array_elements_text($5::jsonb)))))
  OR ((h.coll || '|' || h.id) = ANY (ARRAY(SELECT jsonb_array_elements_text($6::jsonb)))))
ORDER BY h.seq`
}

var _ app.PeriodStore = (*Store)(nil)

func keys(m map[string]bool) string {
	l := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			l = append(l, k)
		}
	}
	b, _ := json.Marshal(l)
	return string(b)
}

func saveArchiveTx(ctx context.Context, tx *sql.Tx, r app.ArchiveRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, Q["arc_upsert"], r.Tenant, r.ID, r.State, r.From, r.To, r.RequestedBy, r.ApprovedBy, r.PackageSHA256, nullTime(r.VerifiedAt),
		len(r.CarryEntries), r.RestoreRequestedBy, r.RestoreApprovedBy, r.RetainUntil, string(b))
	if err != nil && strings.Contains(err.Error(), "cannot") {
		return fmt.Errorf("%w: %v", app.ErrState, err)
	}
	return err
}

func scanArchive(sc interface{ Scan(...any) error }) (app.ArchiveRecord, error) {
	var body string
	var r app.ArchiveRecord
	if err := sc.Scan(&body); err != nil {
		return r, err
	}
	return r, json.Unmarshal([]byte(body), &r)
}

// SaveArchive implements app.PeriodStore.
func (s *Store) SaveArchive(ctx context.Context, r app.ArchiveRecord) error {
	if r.Tenant == "" || r.ID == "" {
		return errors.New("pgstore: an archive record needs a tenant and an id")
	}
	return s.tx(ctx, r.Tenant, func(tx *sql.Tx) error { return saveArchiveTx(ctx, tx, r) })
}

// GetArchive implements app.PeriodStore.
func (s *Store) GetArchive(ctx context.Context, tenant, id string) (app.ArchiveRecord, error) {
	var r app.ArchiveRecord
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		var err error
		r, err = scanArchive(tx.QueryRowContext(ctx, Q["arc_get"], tenant, id))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: %s", app.ErrArchiveNotFound, id)
	}
	return r, err
}

// ListArchives implements app.PeriodStore.
func (s *Store) ListArchives(ctx context.Context, tenant string) ([]app.ArchiveRecord, error) {
	out := []app.ArchiveRecord{}
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, Q["arc_list"], tenant)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanArchive(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// PeriodRows implements app.PeriodStore, from one read-only REPEATABLE READ
// view.
func (s *Store) PeriodRows(ctx context.Context, tenant string, sel app.ArchiveSelection, emit func(app.ArchiveRow) error) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, Q["set_tenant"], tenant); err != nil {
		return err
	}
	excl := keys(sel.ExcludeUCC)
	each := func(q string, args []any, fn func(*sql.Rows) error) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := fn(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	// stored files: by the day received, or the migrated documents listed
	if sel.Kinds[app.ArcFiles] || sel.Kinds[app.ArcDocuments] || (sel.Kinds[app.ArcMigrated] && len(sel.MigratedRaw) > 0) {
		mig := "[]"
		if sel.Kinds[app.ArcMigrated] {
			mig = keys(sel.MigratedRaw)
		}
		err := each(Q["arc_raw"], []any{tenant, sel.From, sel.To, keys(sel.Protected), keys(sel.DocSources), sel.Kinds[app.ArcFiles], sel.Kinds[app.ArcDocuments], sel.Kinds[app.ArcTrades], excl, mig}, func(rows *sql.Rows) error {
			var b, id, src, day string
			var n int64
			if err := rows.Scan(&b, &id, &src, &day, &n); err != nil {
				return err
			}
			kind := app.ArcFiles
			switch {
			case src == "MIG-ARCHIVE":
				kind = app.ArcMigrated
			case sel.DocSources[src]:
				kind = app.ArcDocuments
			}
			return emit(app.ArchiveRow{Record: app.Record{Table: "raw_file", Row: json.RawMessage(b)}, Ref: app.RowRef{Table: "raw_file", Key: id}, Kind: kind, Bytes: n + 200, Date: day})
		})
		if err != nil {
			return fmt.Errorf("archive files: %w", err)
		}
	}
	if sel.Kinds[app.ArcTrades] {
		err := each(Q["arc_trades"], []any{tenant, sel.From, sel.To, excl}, func(rows *sql.Rows) error {
			var b, ref, ucc, day string
			if err := rows.Scan(&b, &ref, &ucc, &day); err != nil {
				return err
			}
			return emit(app.ArchiveRow{Record: app.Record{Table: "trade_event", Row: json.RawMessage(b)}, Ref: app.RowRef{Table: "trade_event", Key: ref}, Kind: app.ArcTrades, Bytes: int64(len(b)), UCC: []string{ucc}, Date: day})
		})
		if err != nil {
			return fmt.Errorf("archive trades: %w", err)
		}
	}
	if sel.Kinds[app.ArcLedger] {
		var heads []app.EntryHead
		err := each(Q["arc_heads"], []any{tenant, sel.From, sel.To}, func(rows *sql.Rows) error {
			var h app.EntryHead
			var accts string
			if err := rows.Scan(&h.ID, &h.ValueDate, &h.SourceKind, &h.ReversalOf, &accts); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(accts), &h.Accounts); err != nil {
				return err
			}
			heads = append(heads, h)
			return nil
		})
		if err != nil {
			return fmt.Errorf("archive entries: %w", err)
		}
		revs := map[string]string{}
		if err := each(Q["arc_reversals"], []any{tenant}, func(rows *sql.Rows) error {
			var id, of string
			if err := rows.Scan(&id, &of); err != nil {
				return err
			}
			revs[id] = of
			return nil
		}); err != nil {
			return err
		}
		take := app.SelectEntries(heads, revs, sel)
		if err := each(Q["arc_entries"], []any{tenant, sel.From, sel.To}, func(rows *sql.Rows) error {
			var b, id, day string
			if err := rows.Scan(&b, &id, &day); err != nil {
				return err
			}
			if !take[id] {
				return nil
			}
			return emit(app.ArchiveRow{Record: app.Record{Table: "journal_entry", Row: json.RawMessage(b)}, Ref: app.RowRef{Table: "journal_entry", Key: id}, Kind: app.ArcLedger, Bytes: int64(len(b)), Date: day})
		}); err != nil {
			return err
		}
		if err := each(Q["arc_postings"], []any{tenant, sel.From, sel.To}, func(rows *sql.Rows) error {
			var b, id, acct string
			if err := rows.Scan(&b, &id, &acct); err != nil {
				return err
			}
			if !take[id] {
				return nil
			}
			row := app.ArchiveRow{Record: app.Record{Table: "posting", Row: json.RawMessage(b)}, Kind: app.ArcLedger, Bytes: int64(len(b))}
			if u, _, ok := ledger.ClientCode(acct); ok {
				row.UCC = []string{u}
			}
			return emit(row)
		}); err != nil {
			return err
		}
	}
	migDocs := "[]"
	if sel.Kinds[app.ArcMigrated] {
		migDocs = keys(sel.MigratedDocs)
		if err := each(Q["arc_docs"], []any{tenant, migDocs}, func(rows *sql.Rows) error {
			var b, ref string
			if err := rows.Scan(&b, &ref); err != nil {
				return err
			}
			return emit(app.ArchiveRow{Record: app.Record{Table: "doc", Row: json.RawMessage(b)}, Ref: app.RowRef{Table: "doc", Key: ref}, Kind: app.ArcMigrated, Bytes: int64(len(b))})
		}); err != nil {
			return fmt.Errorf("archive migrated history: %w", err)
		}
	}
	if sel.Kinds[app.ArcDocHistory] || migDocs != "[]" {
		if err := each(Q["arc_dochist"], []any{tenant, sel.From, sel.To, sel.Kinds[app.ArcDocHistory], excl, migDocs}, func(rows *sql.Rows) error {
			var b, seq, day, ref string
			if err := rows.Scan(&b, &seq, &day, &ref); err != nil {
				return err
			}
			kind := app.ArcDocHistory
			if sel.MigratedDocs[ref] {
				kind = app.ArcMigrated
			}
			return emit(app.ArchiveRow{Record: app.Record{Table: "doc_history", Row: json.RawMessage(b)}, Ref: app.RowRef{Table: "doc_history", Key: seq}, Kind: kind, Bytes: int64(len(b)), Date: day})
		}); err != nil {
			return fmt.Errorf("archive document history: %w", err)
		}
	}
	return tx.Commit()
}

func balancesTx(ctx context.Context, tx *sql.Tx, tenant, asOf string) (map[string]money.Amount, error) {
	rows, err := tx.QueryContext(ctx, Q["arc_balances"], tenant, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]money.Amount{}
	for rows.Next() {
		var a string
		var v int64
		if err := rows.Scan(&a, &v); err != nil {
			return nil, err
		}
		out[a] = money.Amount(v)
	}
	return out, rows.Err()
}

// The order rows are removed in: children before what they point at.
var removeOrder = []string{"trade_event", "journal_entry", "doc_history", "doc", "raw_file"}

const arcBatch = 5000

// RemovePeriod implements app.PeriodStore in one transaction.
func (s *Store) RemovePeriod(ctx context.Context, tenant, id string, refs []app.RowRef, carry []ledger.Entry, finish func(map[string]int, []ledger.Entry) (app.ArchiveRecord, error)) (app.ArchiveRecord, error) {
	var out app.ArchiveRecord
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		r, err := scanArchive(tx.QueryRowContext(ctx, Q["arc_lock"], tenant, id))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", app.ErrArchiveNotFound, id)
		}
		if err != nil {
			return err
		}
		if err := r.RemoveAllowed(); err != nil {
			return err
		}
		bAll, err := balancesTx(ctx, tx, tenant, "")
		if err != nil {
			return err
		}
		bTo, err := balancesTx(ctx, tx, tenant, r.To)
		if err != nil {
			return err
		}
		by := map[string][]string{}
		for _, x := range refs {
			by[x.Table] = append(by[x.Table], x.Key)
		}
		for t := range by {
			if !contains(removeOrder, t) {
				return fmt.Errorf("%w: table %s cannot be archived", app.ErrState, t)
			}
		}
		n := map[string]int{}
		for _, t := range removeOrder {
			ks := by[t]
			for i := 0; i < len(ks); i += arcBatch {
				b, _ := json.Marshal(ks[i:min(i+arcBatch, len(ks))])
				var c int64
				if err := tx.QueryRowContext(ctx, Q["arc_remove"], tenant, id, t, string(b)).Scan(&c); err != nil {
					if strings.Contains(err.Error(), "archive_remove") {
						return fmt.Errorf("%w: the database refused to archive: %v", app.ErrState, err)
					}
					return err
				}
				n[t] += int(c)
			}
		}
		var stored []ledger.Entry
		for _, c := range carry {
			if c.Source.Kind != app.CarryKind || c.Source.ID != id || c.ValueDate != r.To {
				return fmt.Errorf("%w: %s is not a carried-forward entry of archive %s", app.ErrState, c.Key, id)
			}
			if err := c.Validate(); err != nil {
				return err
			}
			e, err := insertEntryTx(ctx, tx, c)
			if err != nil {
				return err
			}
			stored = append(stored, e)
		}
		aAll, err := balancesTx(ctx, tx, tenant, "")
		if err != nil {
			return err
		}
		aTo, err := balancesTx(ctx, tx, tenant, r.To)
		if err != nil {
			return err
		}
		if a, ok := app.SameBalances(bAll, aAll); !ok {
			return fmt.Errorf("%w: account %s's balance would change; nothing was archived", app.ErrState, a)
		}
		if a, ok := app.SameBalances(bTo, aTo); !ok {
			return fmt.Errorf("%w: account %s's balance on %s would change; nothing was archived", app.ErrState, a, r.To)
		}
		out, err = finish(n, stored)
		if err != nil {
			return err
		}
		if out.Tenant != tenant || out.ID != id || out.State != app.ArcArchived {
			return fmt.Errorf("%w: archiving must end with archive %s archived", app.ErrState, id)
		}
		return saveArchiveTx(ctx, tx, out)
	})
	if err != nil {
		return app.ArchiveRecord{}, err
	}
	return out, nil
}

// insertEntryTx appends an entry inside a transaction (the accounts first),
// as Append does.
func insertEntryTx(ctx context.Context, tx *sql.Tx, e ledger.Entry) (ledger.Entry, error) {
	for _, p := range e.Postings {
		t, _ := ledger.TypeOf(p.Account)
		if _, err := tx.ExecContext(ctx, Q["upsert_account"], e.Tenant, p.Account, string(t)); err != nil {
			return e, err
		}
	}
	out := e
	out.Postings = append([]ledger.Posting(nil), e.Postings...)
	if err := tx.QueryRowContext(ctx, Q["insert_entry"], e.Tenant, e.Key, e.ValueDate, e.Narration, e.Source.Kind, e.Source.ID, e.Source.Line, e.ReversalOf, e.Fingerprint(), e.CreatedBy).
		Scan(&out.ID, &out.Seq, &out.CreatedAt); err != nil {
		return e, err
	}
	for i, p := range e.Postings {
		if _, err := tx.ExecContext(ctx, Q["insert_posting"], e.Tenant, out.ID, i+1, p.Account, p.Debit.Paise(), p.Credit.Paise(), p.Memo); err != nil {
			return e, err
		}
	}
	return out, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// RestorePeriod implements app.PeriodStore in one transaction.
func (s *Store) RestorePeriod(ctx context.Context, tenant, id string, next func() (app.Record, bool, error), finish func(map[string]int) (app.ArchiveRecord, error)) (app.ArchiveRecord, error) {
	var out app.ArchiveRecord
	known := map[string]bool{"raw_file": true, "trade_event": true, "journal_entry": true, "posting": true, "doc": true, "doc_history": true}
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		r, err := scanArchive(tx.QueryRowContext(ctx, Q["arc_lock"], tenant, id))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", app.ErrArchiveNotFound, id)
		}
		if err != nil {
			return err
		}
		if err := r.RestoreAllowed(); err != nil {
			return err
		}
		bAll, err := balancesTx(ctx, tx, tenant, "")
		if err != nil {
			return err
		}
		bTo, err := balancesTx(ctx, tx, tenant, r.To)
		if err != nil {
			return err
		}
		n := map[string]int{}
		var batch []json.RawMessage
		table := ""
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			b, _ := json.Marshal(batch)
			var c int64
			if err := tx.QueryRowContext(ctx, Q["arc_restore"], tenant, id, table, string(b)).Scan(&c); err != nil {
				if strings.Contains(err.Error(), "archive_restore") {
					return fmt.Errorf("%w: the database refused to restore: %v", app.ErrState, err)
				}
				return err
			}
			n[table] += int(c)
			batch = batch[:0]
			return nil
		}
		for {
			rec, ok, err := next()
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			if !known[rec.Table] {
				return fmt.Errorf("restore: table %q is not part of an archive", rec.Table)
			}
			if rec.Table != table || len(batch) >= 500 {
				if err := flush(); err != nil {
					return err
				}
				table = rec.Table
			}
			batch = append(batch, rec.Row)
		}
		if err := flush(); err != nil {
			return err
		}
		var c int64
		if err := tx.QueryRowContext(ctx, Q["arc_drop_carry"], tenant, id).Scan(&c); err != nil {
			return fmt.Errorf("%w: %v", app.ErrState, err)
		}
		n["carry_removed"] = int(c)
		aAll, err := balancesTx(ctx, tx, tenant, "")
		if err != nil {
			return err
		}
		aTo, err := balancesTx(ctx, tx, tenant, r.To)
		if err != nil {
			return err
		}
		if a, ok := app.SameBalances(bAll, aAll); !ok {
			return fmt.Errorf("%w: account %s's balance would change; nothing was restored", app.ErrState, a)
		}
		if a, ok := app.SameBalances(bTo, aTo); !ok {
			return fmt.Errorf("%w: account %s's balance on %s would change; nothing was restored", app.ErrState, a, r.To)
		}
		out, err = finish(n)
		if err != nil {
			return err
		}
		if out.Tenant != tenant || out.ID != id || out.State != app.ArcRestored {
			return fmt.Errorf("%w: restoring must end with archive %s restored", app.ErrState, id)
		}
		return saveArchiveTx(ctx, tx, out)
	})
	if err != nil {
		return app.ArchiveRecord{}, err
	}
	return out, nil
}
