// Package docs is a versioned, tenant-scoped document store for master and
// workflow data: clients, holdings, settings, settlement status, alerts and
// so on. Money never lives here; every amount is a ledger entry. Each write
// carries the version it read, so two users cannot silently overwrite each
// other (optimistic concurrency), and every change is kept in history.
package docs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrStale means the document changed since it was read.
	ErrStale = errors.New("document changed since it was read; reload and try again")
)

// Doc is one stored document.
type Doc struct {
	Collection string          `json:"collection"`
	ID         string          `json:"id"`
	Version    int64           `json:"version"`
	Body       json.RawMessage `json:"body"`
	UpdatedAt  time.Time       `json:"updated_at"`
	UpdatedBy  string          `json:"updated_by"`
}

// Store persists documents. Put with expect=0 creates (fails if present
// unless upsert is used); expect=n requires the current version to be n.
// Upsert ignores the version.
type Store interface {
	Get(ctx context.Context, tenant, coll, id string) (Doc, error)
	List(ctx context.Context, tenant, coll, prefix string) ([]Doc, error)
	Put(ctx context.Context, tenant, coll, id string, body json.RawMessage, expect int64, upsert bool, actor string) (Doc, error)
}

// Get decodes one document into T.
func Get[T any](ctx context.Context, s Store, tenant, coll, id string) (T, int64, error) {
	var v T
	d, err := s.Get(ctx, tenant, coll, id)
	if err != nil {
		return v, 0, err
	}
	if err := json.Unmarshal(d.Body, &v); err != nil {
		return v, 0, fmt.Errorf("docs: decode %s/%s: %w", coll, id, err)
	}
	return v, d.Version, nil
}

// GetOr returns the document or def when it does not exist.
func GetOr[T any](ctx context.Context, s Store, tenant, coll, id string, def T) (T, int64, error) {
	v, ver, err := Get[T](ctx, s, tenant, coll, id)
	if errors.Is(err, ErrNotFound) {
		return def, 0, nil
	}
	return v, ver, err
}

// List decodes every document in a collection (optionally by ID prefix),
// ordered by ID.
func List[T any](ctx context.Context, s Store, tenant, coll, prefix string) ([]T, error) {
	ds, err := s.List(ctx, tenant, coll, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(ds))
	for _, d := range ds {
		var v T
		if err := json.Unmarshal(d.Body, &v); err != nil {
			return nil, fmt.Errorf("docs: decode %s/%s: %w", coll, d.ID, err)
		}
		out = append(out, v)
	}
	return out, nil
}

// Put encodes and stores v.
func Put[T any](ctx context.Context, s Store, tenant, coll, id string, v T, expect int64, actor string) (int64, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return 0, err
	}
	d, err := s.Put(ctx, tenant, coll, id, b, expect, false, actor)
	return d.Version, err
}

// Upsert stores v regardless of version.
func Upsert[T any](ctx context.Context, s Store, tenant, coll, id string, v T, actor string) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.Put(ctx, tenant, coll, id, b, 0, true, actor)
	return err
}

// Update reads, applies fn and writes back, retrying on a concurrent change.
// fn may return an error to abort.
func Update[T any](ctx context.Context, s Store, tenant, coll, id, actor string, fn func(*T) error) (T, error) {
	for attempt := 0; attempt < 5; attempt++ {
		v, ver, err := Get[T](ctx, s, tenant, coll, id)
		if err != nil {
			return v, err
		}
		if err := fn(&v); err != nil {
			return v, err
		}
		if _, err := Put(ctx, s, tenant, coll, id, v, ver, actor); err != nil {
			if errors.Is(err, ErrStale) {
				continue
			}
			return v, err
		}
		return v, nil
	}
	var zero T
	return zero, ErrStale
}

// Mem is an in-memory Store.
type Mem struct {
	mu   sync.RWMutex
	docs map[string]Doc // tenant|coll|id
	Now  func() time.Time
}

func NewMem() *Mem { return &Mem{docs: map[string]Doc{}, Now: time.Now} }

func key(t, c, id string) string { return t + "\x00" + c + "\x00" + id }

func (m *Mem) Get(_ context.Context, t, c, id string) (Doc, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.docs[key(t, c, id)]
	if !ok {
		return Doc{}, fmt.Errorf("%w: %s/%s", ErrNotFound, c, id)
	}
	return d, nil
}

func (m *Mem) List(_ context.Context, t, c, prefix string) ([]Doc, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p := key(t, c, prefix)
	out := []Doc{}
	for k, d := range m.docs {
		if strings.HasPrefix(k, p) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *Mem) Put(_ context.Context, t, c, id string, body json.RawMessage, expect int64, upsert bool, actor string) (Doc, error) {
	if t == "" || c == "" || id == "" {
		return Doc{}, errors.New("docs: tenant, collection and id are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(t, c, id)
	cur, exists := m.docs[k]
	if !upsert {
		if expect == 0 && exists {
			return Doc{}, fmt.Errorf("%w: %s/%s already exists", ErrStale, c, id)
		}
		if expect > 0 && (!exists || cur.Version != expect) {
			return Doc{}, ErrStale
		}
	}
	d := Doc{Collection: c, ID: id, Version: cur.Version + 1, Body: append(json.RawMessage(nil), body...), UpdatedAt: m.Now().UTC(), UpdatedBy: actor}
	m.docs[k] = d
	return d, nil
}

// ExportTenant returns every document of a tenant, by collection and ID,
// for backups.
func (m *Mem) ExportTenant(tenant string) []Doc {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Doc{}
	for k, d := range m.docs {
		if strings.HasPrefix(k, tenant+"\x00") {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Collection != out[j].Collection {
			return out[i].Collection < out[j].Collection
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ImportTenant restores a tenant's documents with their versions. The
// tenant must have none yet.
func (m *Mem) ImportTenant(tenant string, ds []Doc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.docs {
		if strings.HasPrefix(k, tenant+"\x00") {
			return fmt.Errorf("docs: tenant %s already has documents", tenant)
		}
	}
	for _, d := range ds {
		m.docs[key(tenant, d.Collection, d.ID)] = d
	}
	return nil
}
