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

// ExportTenant returns a tenant's entries in append order, for backups.
func (m *MemStore) ExportTenant(tenant string) []Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Entry, 0, len(m.entries[tenant]))
	for _, e := range m.entries[tenant] {
		out = append(out, clone(e))
	}
	return out
}

// ImportTenant restores a tenant's entries, keeping their IDs and sequence
// numbers. The tenant must have no entries yet.
func (m *MemStore) ImportTenant(tenant string, es []Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries[tenant]) > 0 {
		return fmt.Errorf("ledger: tenant %s already has entries", tenant)
	}
	for _, e := range es {
		if e.Tenant != tenant {
			return fmt.Errorf("ledger: entry %s belongs to %s", e.ID, e.Tenant)
		}
		m.entries[tenant] = append(m.entries[tenant], clone(e))
		i := len(m.entries[tenant]) - 1
		m.byKey[tenant+"|"+e.Key] = i
		m.byID[tenant+"|"+e.ID] = i
		if e.Seq > m.seq {
			m.seq = e.Seq
		}
	}
	return nil
}

// reindex rebuilds a tenant's key and id indexes; m.mu is held.
func (m *MemStore) reindex(tenant string) {
	for k := range m.byKey {
		if len(k) > len(tenant) && k[:len(tenant)+1] == tenant+"|" {
			delete(m.byKey, k)
		}
	}
	for k := range m.byID {
		if len(k) > len(tenant) && k[:len(tenant)+1] == tenant+"|" {
			delete(m.byID, k)
		}
	}
	for i, e := range m.entries[tenant] {
		m.byKey[tenant+"|"+e.Key] = i
		m.byID[tenant+"|"+e.ID] = i
	}
}

// RemoveEntries takes the listed entries out of a tenant's journal and
// returns them. Only app.MemSnapshot's period archive calls it, for an
// archive approved by a second person whose package was verified.
func (m *MemStore) RemoveEntries(tenant string, ids map[string]bool) []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Entry
	keep := m.entries[tenant][:0:0]
	for _, e := range m.entries[tenant] {
		if ids[e.ID] {
			out = append(out, clone(e))
			continue
		}
		keep = append(keep, e)
	}
	m.entries[tenant] = keep
	m.reindex(tenant)
	return out
}

// PutEntries puts entries back into a tenant's journal with their IDs,
// sequence numbers and keys (a restored archive). None may be there yet.
func (m *MemStore) PutEntries(tenant string, es []Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range es {
		if e.Tenant != tenant {
			return fmt.Errorf("ledger: entry %s belongs to %s", e.ID, e.Tenant)
		}
		if _, ok := m.byID[tenant+"|"+e.ID]; ok {
			return fmt.Errorf("ledger: entry %s is already in the journal", e.ID)
		}
		if _, ok := m.byKey[tenant+"|"+e.Key]; ok {
			return fmt.Errorf("ledger: key %q is already used", e.Key)
		}
	}
	for _, e := range es {
		m.entries[tenant] = append(m.entries[tenant], clone(e))
		if e.Seq > m.seq {
			m.seq = e.Seq
		}
	}
	m.reindex(tenant)
	return nil
}

// PurgeTenant removes every entry of a tenant and returns how many entries
// and postings were removed. Only app.MemSnapshot.PurgeTenant calls it, for
// a firm that has closed its account through the approved exit flow.
func (m *MemStore) PurgeTenant(tenant string) (entries, postings int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries[tenant] {
		postings += len(e.Postings)
		delete(m.byKey, tenant+"|"+e.Key)
		delete(m.byID, tenant+"|"+e.ID)
	}
	entries = len(m.entries[tenant])
	delete(m.entries, tenant)
	return entries, postings
}
