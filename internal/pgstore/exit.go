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
)

// ---- a firm's exit (app.ExitStore, app.Purger) ----
//
// Exit records live in ll.tenant_exit, outside the firm's own tables, so
// they survive the purge (db/migrations/0004_tenant_exit.sql). The purge
// itself is ll.purge_tenant, which checks the record again in the database
// and deletes the firm's rows as the schema owner; the application role
// has no DELETE privilege anywhere.

func init() {
	Q["exit_upsert"] = `INSERT INTO ll.tenant_exit (tenant_id, id, state, requested_by, approved_by, cooling_off_ends, export_downloaded_at, purged_at, certificate_sha256, record, certificate)
VALUES ($1::text, $2::text, $3::text, $4::text, nullif($5::text, ''), $6::timestamptz, $7::timestamptz, $8::timestamptz, nullif($9::text, ''), $10::jsonb, $11::bytea)
ON CONFLICT (tenant_id, id) DO UPDATE SET state = EXCLUDED.state, requested_by = EXCLUDED.requested_by, approved_by = EXCLUDED.approved_by,
  cooling_off_ends = EXCLUDED.cooling_off_ends, export_downloaded_at = EXCLUDED.export_downloaded_at, purged_at = EXCLUDED.purged_at,
  certificate_sha256 = EXCLUDED.certificate_sha256, record = EXCLUDED.record, certificate = EXCLUDED.certificate`
	Q["exit_get"] = `SELECT record::text, coalesce(certificate, ''::bytea) FROM ll.tenant_exit WHERE tenant_id = $1::text AND id = $2::text`
	Q["exit_list"] = `SELECT record::text, coalesce(certificate, ''::bytea) FROM ll.tenant_exit WHERE ($1::text = '' OR tenant_id = $1::text) ORDER BY (record->>'requested_at'), tenant_id, id`
	Q["exit_purge"] = `SELECT ll.purge_tenant(tenant_id, id)::text FROM ll.tenant_exit WHERE tenant_id = $1::text AND id = $2::text`
}

var _ app.ExitStore = (*Store)(nil)
var _ app.Purger = (*Store)(nil)

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func saveExitTx(ctx context.Context, tx *sql.Tx, r app.ExitRecord) error {
	cert := r.Certificate
	r.Certificate = nil
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if cert == nil {
		cert = []byte{}
	}
	_, err = tx.ExecContext(ctx, Q["exit_upsert"], r.Tenant, r.ID, r.State, r.RequestedBy, r.ApprovedBy, nullTime(r.CoolingOffEnds), nullTime(r.ExportDownloadedAt),
		nullTime(r.PurgedAt), r.CertificateSHA256, string(b), cert)
	return err
}

func scanExit(sc interface{ Scan(...any) error }) (app.ExitRecord, error) {
	var body string
	var cert []byte
	if err := sc.Scan(&body, &cert); err != nil {
		return app.ExitRecord{}, err
	}
	var r app.ExitRecord
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return r, err
	}
	if len(cert) > 0 {
		r.Certificate = cert
	}
	return r, nil
}

// SaveExit implements app.ExitStore.
func (s *Store) SaveExit(ctx context.Context, r app.ExitRecord) error {
	if r.Tenant == "" || r.ID == "" {
		return errors.New("pgstore: an exit record needs a tenant and an id")
	}
	return s.tx(ctx, r.Tenant, func(tx *sql.Tx) error {
		err := saveExitTx(ctx, tx, r)
		if err != nil && strings.Contains(err.Error(), "is purged") {
			return fmt.Errorf("%w: exit %s is purged", app.ErrState, r.ID)
		}
		return err
	})
}

// GetExit implements app.ExitStore.
func (s *Store) GetExit(ctx context.Context, tenant, id string) (app.ExitRecord, error) {
	var r app.ExitRecord
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		var err error
		r, err = scanExit(tx.QueryRowContext(ctx, Q["exit_get"], tenant, id))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: %s", app.ErrExitNotFound, id)
	}
	return r, err
}

// ListExits implements app.ExitStore. With no tenant it reads every firm's
// records on a connection with no firm set (the operator's list).
func (s *Store) ListExits(ctx context.Context, tenant string) ([]app.ExitRecord, error) {
	out := []app.ExitRecord{}
	read := func(q interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}) error {
		rows, err := q.QueryContext(ctx, Q["exit_list"], tenant)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanExit(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	}
	if tenant == "" {
		return out, read(s.DB)
	}
	return out, s.tx(ctx, tenant, func(tx *sql.Tx) error { return read(tx) })
}

// PurgeTenant implements app.Purger: the record is checked here and again
// by ll.purge_tenant, the rows are deleted, finish builds the certificate,
// and the purged record is stored, all in one transaction.
func (s *Store) PurgeTenant(ctx context.Context, tenant, exitID string, now time.Time, finish func(map[string]int) (app.ExitRecord, error)) (app.ExitRecord, error) {
	var out app.ExitRecord
	err := s.tx(ctx, tenant, func(tx *sql.Tx) error {
		r, err := scanExit(tx.QueryRowContext(ctx, Q["exit_get"], tenant, exitID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", app.ErrExitNotFound, exitID)
		}
		if err != nil {
			return err
		}
		if err := r.PurgeAllowed(now); err != nil {
			return err
		}
		var counts string
		if err := tx.QueryRowContext(ctx, Q["exit_purge"], tenant, exitID).Scan(&counts); err != nil {
			if strings.Contains(err.Error(), "ready to purge") {
				return fmt.Errorf("%w: the database refused the purge: %v", app.ErrState, err)
			}
			return err
		}
		n := map[string]int{}
		if err := json.Unmarshal([]byte(counts), &n); err != nil {
			return err
		}
		out, err = finish(n)
		if err != nil {
			return err
		}
		if out.Tenant != tenant || out.ID != exitID || out.State != app.ExitPurged {
			return fmt.Errorf("%w: the purge must end with exit %s purged", app.ErrState, exitID)
		}
		return saveExitTx(ctx, tx, out)
	})
	if err != nil {
		return app.ExitRecord{}, err
	}
	return out, nil
}
