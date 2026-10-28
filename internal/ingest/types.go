// Package ingest turns raw exchange, clearing and OMS files into canonical
// TradeEvents. Every file is kept byte-for-byte with its SHA-256 before it is
// parsed, and every event records the file and line it came from, so any
// number in the back office can be traced to its source.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/money"
)

// Origin says which side of the reconciliation a trade came from.
type Origin string

const (
	FromExchange Origin = "EXCHANGE"
	FromOMS      Origin = "OMS"
)

// TradeEvent is the canonical executed trade.
type TradeEvent struct {
	Tenant         string    `json:"tenant"`
	Origin         Origin    `json:"origin"`
	Exchange       string    `json:"exchange"`
	Segment        string    `json:"segment"` // CM, FO, CD, COM
	TradeNo        string    `json:"trade_no"`
	OrderNo        string    `json:"order_no,omitempty"`
	UCC            string    `json:"ucc"`
	Symbol         string    `json:"symbol"`
	InstrumentType string    `json:"instrument_type"` // EQ, FUT, CE, PE
	Expiry         string    `json:"expiry,omitempty"`
	Strike         string    `json:"strike,omitempty"`
	Side           string    `json:"side"` // B or S
	Qty            int64     `json:"qty"`
	Price          money.Dec `json:"price"`
	TradeDate      string    `json:"trade_date"`
	TradeTime      string    `json:"trade_time,omitempty"`
	Product        string    `json:"product,omitempty"`
	AlgoID         string    `json:"algo_id,omitempty"`
	Terminal       string    `json:"terminal,omitempty"`
	Settlement     string    `json:"settlement,omitempty"` // settlement type as the exchange sends it
	FileID         string    `json:"file_id,omitempty"`
	Line           int       `json:"line,omitempty"`
}

// Key identifies a trade within a tenant and origin.
func (t TradeEvent) Key() string {
	return t.Exchange + "|" + t.Segment + "|" + t.TradeDate + "|" + t.TradeNo
}

// Instrument identifies the contract, e.g. "NIFTY|FUT|2026-10-27|".
func (t TradeEvent) Instrument() string {
	return t.Symbol + "|" + t.InstrumentType + "|" + t.Expiry + "|" + t.Strike
}

// Value is quantity × price (premium for options).
func (t TradeEvent) Value() money.Dec { return t.Price.MulInt(t.Qty) }

// SameContent compares the business fields of two events (not provenance).
func (t TradeEvent) SameContent(o TradeEvent) bool {
	return t.Exchange == o.Exchange && t.Segment == o.Segment && t.TradeNo == o.TradeNo && t.OrderNo == o.OrderNo &&
		t.UCC == o.UCC && t.Instrument() == o.Instrument() && t.Side == o.Side && t.Qty == o.Qty &&
		t.Price.Cmp(o.Price) == 0 && t.TradeDate == o.TradeDate && t.TradeTime == o.TradeTime &&
		t.Product == o.Product && t.AlgoID == o.AlgoID && t.Terminal == o.Terminal
}

// RawFile is an uploaded file kept exactly as received.
type RawFile struct {
	ID         string    `json:"id"`
	Tenant     string    `json:"tenant"`
	Source     string    `json:"source"`
	Name       string    `json:"name"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	ReceivedAt time.Time `json:"received_at"`
	PriorID    string    `json:"prior_id,omitempty"` // earlier upload with identical bytes
	Content    []byte    `json:"-"`
}

// RawStore keeps raw files. Put always stores a new record (uploads are
// facts) and sets PriorID when identical bytes were seen before.
type RawStore interface {
	Put(ctx context.Context, f RawFile) (RawFile, error)
	Get(ctx context.Context, tenant, id string) (RawFile, error)
}

// ErrTradeConflict means a trade key arrived again with different content.
var ErrTradeConflict = errors.New("ingest: trade already recorded with different details")

// TradeStore persists trade events. Add is atomic: either every event is
// stored (identical repeats count as duplicates) or none is.
type TradeStore interface {
	Add(ctx context.Context, events []TradeEvent) (added, duplicates int, err error)
	List(ctx context.Context, tenant string, origin Origin, date string) ([]TradeEvent, error)
}

// MemRawStore is an in-memory RawStore.
type MemRawStore struct {
	mu    sync.Mutex
	seq   int
	files map[string]RawFile
	bySum map[string]string
	Now   func() time.Time
}

func NewMemRawStore() *MemRawStore {
	return &MemRawStore{files: map[string]RawFile{}, bySum: map[string]string{}, Now: time.Now}
}

func (m *MemRawStore) Put(_ context.Context, f RawFile) (RawFile, error) {
	if f.Tenant == "" || f.SHA256 == "" {
		return RawFile{}, errors.New("ingest: raw file needs tenant and sha256")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	f.ID = fmt.Sprintf("RF-%08d", m.seq)
	f.ReceivedAt = m.Now().UTC()
	if f.Size == 0 {
		f.Size = int64(len(f.Content))
	}
	f.Content = append([]byte(nil), f.Content...)
	sk := f.Tenant + "|" + f.Source + "|" + f.SHA256
	f.PriorID = m.bySum[sk]
	if f.PriorID == "" {
		m.bySum[sk] = f.ID
	}
	m.files[f.Tenant+"|"+f.ID] = f
	return f, nil
}

func (m *MemRawStore) Get(_ context.Context, tenant, id string) (RawFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[tenant+"|"+id]
	if !ok {
		return RawFile{}, fmt.Errorf("ingest: raw file %s not found", id)
	}
	return f, nil
}

// MemTradeStore is an in-memory TradeStore.
type MemTradeStore struct {
	mu     sync.RWMutex
	events map[string]TradeEvent // tenant|origin|key
	order  []string
}

func NewMemTradeStore() *MemTradeStore { return &MemTradeStore{events: map[string]TradeEvent{}} }

func (m *MemTradeStore) Add(_ context.Context, evs []TradeEvent) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var conflicts []string
	seen := map[string]TradeEvent{}
	for _, e := range evs {
		k := e.Tenant + "|" + string(e.Origin) + "|" + e.Key()
		if ex, ok := m.events[k]; ok && !ex.SameContent(e) {
			conflicts = append(conflicts, e.TradeNo)
		} else if ex, ok := seen[k]; ok && !ex.SameContent(e) {
			conflicts = append(conflicts, e.TradeNo)
		}
		seen[k] = e
	}
	if len(conflicts) > 0 {
		return 0, 0, fmt.Errorf("%w: %s", ErrTradeConflict, strings.Join(conflicts, ", "))
	}
	added, dup := 0, 0
	for _, e := range evs {
		k := e.Tenant + "|" + string(e.Origin) + "|" + e.Key()
		if _, ok := m.events[k]; ok {
			dup++
			continue
		}
		m.events[k] = e
		m.order = append(m.order, k)
		added++
	}
	return added, dup, nil
}

func (m *MemTradeStore) List(_ context.Context, tenant string, origin Origin, date string) ([]TradeEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []TradeEvent{}
	for _, k := range m.order {
		e := m.events[k]
		if e.Tenant == tenant && (origin == "" || e.Origin == origin) && (date == "" || e.TradeDate == date) {
			out = append(out, e)
		}
	}
	return out, nil
}

// ExportTenant returns a tenant's stored files in ID order, for backups.
func (m *MemRawStore) ExportTenant(tenant string) []RawFile {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []RawFile{}
	for k, f := range m.files {
		if strings.HasPrefix(k, tenant+"|") {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ImportTenant restores a tenant's files, keeping their IDs. The tenant
// must have no files yet.
func (m *MemRawStore) ImportTenant(tenant string, fs []RawFile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.files {
		if strings.HasPrefix(k, tenant+"|") {
			return fmt.Errorf("ingest: tenant %s already has files", tenant)
		}
	}
	for _, f := range fs {
		if f.Tenant != tenant {
			return fmt.Errorf("ingest: file %s belongs to %s", f.ID, f.Tenant)
		}
		m.files[tenant+"|"+f.ID] = f
		if f.PriorID == "" {
			m.bySum[f.Tenant+"|"+f.Source+"|"+f.SHA256] = f.ID
		}
		var n int
		if _, err := fmt.Sscanf(f.ID, "RF-%d", &n); err == nil && n > m.seq {
			m.seq = n
		}
	}
	return nil
}

// ExportTenant returns a tenant's trades in the order they were added.
func (m *MemTradeStore) ExportTenant(tenant string) []TradeEvent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []TradeEvent{}
	for _, k := range m.order {
		if e := m.events[k]; e.Tenant == tenant {
			out = append(out, e)
		}
	}
	return out
}

// ImportTenant restores a tenant's trades. The tenant must have none yet.
func (m *MemTradeStore) ImportTenant(tenant string, evs []TradeEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.order {
		if m.events[k].Tenant == tenant {
			return fmt.Errorf("ingest: tenant %s already has trades", tenant)
		}
	}
	for _, e := range evs {
		if e.Tenant != tenant {
			return fmt.Errorf("ingest: trade %s belongs to %s", e.TradeNo, e.Tenant)
		}
		k := e.Tenant + "|" + string(e.Origin) + "|" + e.Key()
		m.events[k] = e
		m.order = append(m.order, k)
	}
	return nil
}
