package pgstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ledgerline/internal/docs"
)

func init() {
	Q["doc_get"] = `SELECT version, body::text, updated_at, updated_by FROM ll.doc WHERE tenant_id = $1::text AND coll = $2::text AND id = $3::text`
	Q["doc_list"] = `SELECT id, version, body::text, updated_at, updated_by FROM ll.doc
WHERE tenant_id = $1::text AND coll = $2::text AND id LIKE replace(replace(replace($3::text, '\', '\\'), '%', '\%'), '_', '\_') || '%'
ORDER BY id`
	Q["doc_insert"] = `INSERT INTO ll.doc (tenant_id, coll, id, version, body, updated_by) VALUES ($1::text, $2::text, $3::text, 1, $4::jsonb, $5::text)
ON CONFLICT DO NOTHING RETURNING version, updated_at`
	Q["doc_update"] = `UPDATE ll.doc SET version = version + 1, body = $4::jsonb, updated_at = now(), updated_by = $5::text
WHERE tenant_id = $1::text AND coll = $2::text AND id = $3::text AND ($6::bigint = 0 OR version = $6::bigint)
RETURNING version, updated_at`
}

type docStore struct{ *Store }

// DocStore returns the document-store view.
func (s *Store) DocStore() docs.Store { return docStore{s} }

func (d docStore) Get(ctx context.Context, tenant, coll, id string) (docs.Doc, error) {
	out := docs.Doc{Collection: coll, ID: id}
	var body string
	err := d.tx(ctx, tenant, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, Q["doc_get"], tenant, coll, id).Scan(&out.Version, &body, &out.UpdatedAt, &out.UpdatedBy)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return docs.Doc{}, fmt.Errorf("%w: %s/%s", docs.ErrNotFound, coll, id)
	}
	out.Body = json.RawMessage(body)
	return out, err
}

func (d docStore) List(ctx context.Context, tenant, coll, prefix string) ([]docs.Doc, error) {
	out := []docs.Doc{}
	err := d.tx(ctx, tenant, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, Q["doc_list"], tenant, coll, prefix)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			x := docs.Doc{Collection: coll}
			var body string
			if err := rows.Scan(&x.ID, &x.Version, &body, &x.UpdatedAt, &x.UpdatedBy); err != nil {
				return err
			}
			x.Body = json.RawMessage(body)
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

func (d docStore) Put(ctx context.Context, tenant, coll, id string, body json.RawMessage, expect int64, upsert bool, actor string) (docs.Doc, error) {
	out := docs.Doc{Collection: coll, ID: id, Body: body, UpdatedBy: actor}
	err := d.tx(ctx, tenant, func(tx *sql.Tx) error {
		if upsert || expect == 0 {
			err := tx.QueryRowContext(ctx, Q["doc_insert"], tenant, coll, id, string(body), actor).Scan(&out.Version, &out.UpdatedAt)
			if err == nil {
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if !upsert {
				return fmt.Errorf("%w: %s/%s already exists", docs.ErrStale, coll, id)
			}
		}
		err := tx.QueryRowContext(ctx, Q["doc_update"], tenant, coll, id, string(body), actor, expect).Scan(&out.Version, &out.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return docs.ErrStale
		}
		if err != nil && strings.Contains(err.Error(), "40001") {
			return docs.ErrStale
		}
		return err
	})
	return out, err
}
