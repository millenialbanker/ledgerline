package ledger

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemStore is an in-memory Store for tests, demos and single-node dev.
type MemStore struct {
	mu      sync.RWMutex
	seq     int64
	entries map[string][]Entry // tenant -> entries in append order
	byKey   map[string]int     // tenant|key -> index
	byID    map[string]int     // tenant|id -> index
	Now     func() time.Time
}

func NewMemStore() *MemStore {
	return &MemStore{entries: map[string][]Entry{}, byKey: map[string]int{}, byID: map[string]int{}, Now: time.Now}
}

func clone(e Entry) Entry {
	e.Postings = append([]Posting(nil), e.Postings...)
	return e
}

func (m *MemStore) Append(_ context.Context, e Entry) (Entry, bool, error) {
	if err := e.Validate(); err != nil {
		return Entry{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := e.Tenant + "|" + e.Key
	if i, ok := m.byKey[k]; ok {
		ex := m.entries[e.Tenant][i]
		if ex.Fingerprint() == e.Fingerprint() {
			return clone(ex), false, nil
		}
		return Entry{}, false, fmt.Errorf("%w (key %q, existing %s)", ErrConflict, e.Key, ex.ID)
	}
	if e.ReversalOf != "" {
		if _, ok := m.byID[e.Tenant+"|"+e.ReversalOf]; !ok {
			return Entry{}, false, fmt.Errorf("%w: reversal target %s", ErrNotFound, e.ReversalOf)
		}
	}
	m.seq++
	e.Seq = m.seq
	e.ID = fmt.Sprintf("JE-%010d", m.seq)
	e.CreatedAt = m.Now().UTC()
	e = clone(e)
	m.entries[e.Tenant] = append(m.entries[e.Tenant], e)
	idx := len(m.entries[e.Tenant]) - 1
	m.byKey[k] = idx
	m.byID[e.Tenant+"|"+e.ID] = idx
	return clone(e), true, nil
}

func (m *MemStore) Get(_ context.Context, tenant, id string) (Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	i, ok := m.byID[tenant+"|"+id]
	if !ok {
		return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return clone(m.entries[tenant][i]), nil
}

func (m *MemStore) List(_ context.Context, tenant string, f Filter) ([]Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []Entry{}
	for _, e := range m.entries[tenant] {
		if f.Matches(e) {
			out = append(out, clone(e))
		}
	}
	// Value-date order, then append order, so back-dated entries land correctly.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ValueDate != out[j].ValueDate {
			return out[i].ValueDate < out[j].ValueDate
		}
		return out[i].Seq < out[j].Seq
	})
	return out, nil
}
