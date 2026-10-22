package app

import (
	"context"
	"encoding/json"
	"fmt"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
)

// ---- whole-firm snapshots, for backups ----
//
// A Snapshotter reads every row a firm (tenant) has, in every store, from
// one consistent point in time, and writes them back into an empty
// database. Rows are copied as stored: document bodies and file contents
// stay encrypted with the firm's keys, so a backup is no more readable than
// the database it came from. Each backend restores its own format only.

// Record is one stored row: the table it belongs to and the row as JSON.
type Record struct {
	Table string          `json:"t"`
	Row   json.RawMessage `json:"r"`
}

// Snapshotter backs up and restores one firm's data.
type Snapshotter interface {
	// Backend names the storage ("postgres", "memory"); a backup restores
	// only into the backend it was taken from.
	Backend() string
	// Snapshot calls emit for every row of the tenant, parents before
	// children, from a single consistent view.
	Snapshot(ctx context.Context, tenant string, emit func(Record) error) error
	// Restore loads rows into a tenant that has no data, all or nothing,
	// and returns how many rows went into each table.
	Restore(ctx context.Context, tenant string, next func() (Record, bool, error)) (map[string]int, error)
}

// memRaw carries a file's contents, which RawFile leaves out of JSON.
type memRaw struct {
	ingest.RawFile
	Content []byte `json:"content"`
}

// MemSnapshot snapshots the in-memory stores.
type MemSnapshot struct {
	Ledger *ledger.MemStore
	Raw    *ingest.MemRawStore
	Trades *ingest.MemTradeStore
	Breaks *MemBreakStore
	Audit  *MemAuditStore
	Docs   *docs.Mem
}

func (m *MemSnapshot) Backend() string { return "memory" }

func (m *MemSnapshot) Snapshot(_ context.Context, tenant string, emit func(Record) error) error {
	put := func(table string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return emit(Record{Table: table, Row: b})
	}
	for _, f := range m.Raw.ExportTenant(tenant) {
		if err := put("raw_file", memRaw{f, f.Content}); err != nil {
			return err
		}
	}
	for _, e := range m.Trades.ExportTenant(tenant) {
		if err := put("trade_event", e); err != nil {
			return err
		}
	}
	for _, b := range m.Breaks.ExportTenant(tenant) {
		if err := put("recon_break", b); err != nil {
			return err
		}
	}
	for _, e := range m.Ledger.ExportTenant(tenant) {
		if err := put("journal_entry", e); err != nil {
			return err
		}
	}
	for _, e := range m.Audit.ExportTenant(tenant) {
		if err := put("audit_log", e); err != nil {
			return err
		}
	}
	for _, d := range m.Docs.ExportTenant(tenant) {
		if err := put("doc", d); err != nil {
			return err
		}
	}
	return nil
}

func (m *MemSnapshot) Restore(_ context.Context, tenant string, next func() (Record, bool, error)) (map[string]int, error) {
	if len(m.Raw.ExportTenant(tenant))+len(m.Trades.ExportTenant(tenant))+len(m.Breaks.ExportTenant(tenant))+
		len(m.Ledger.ExportTenant(tenant))+len(m.Audit.ExportTenant(tenant))+len(m.Docs.ExportTenant(tenant)) > 0 {
		return nil, fmt.Errorf("%w: firm %s already has data; restore into an empty database", ErrState, tenant)
	}
	var (
		raws   []ingest.RawFile
		trades []ingest.TradeEvent
		breaks []BreakRecord
		ents   []ledger.Entry
		audit  []AuditEvent
		ds     []docs.Doc
		n      = map[string]int{}
	)
	for {
		r, ok, err := next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		var derr error
		switch r.Table {
		case "raw_file":
			var x memRaw
			derr = json.Unmarshal(r.Row, &x)
			x.RawFile.Content = x.Content
			raws = append(raws, x.RawFile)
		case "trade_event":
			var x ingest.TradeEvent
			derr = json.Unmarshal(r.Row, &x)
			trades = append(trades, x)
		case "recon_break":
			var x BreakRecord
			derr = json.Unmarshal(r.Row, &x)
			breaks = append(breaks, x)
		case "journal_entry":
			var x ledger.Entry
			derr = json.Unmarshal(r.Row, &x)
			ents = append(ents, x)
		case "audit_log":
			var x AuditEvent
			derr = json.Unmarshal(r.Row, &x)
			audit = append(audit, x)
		case "doc":
			var x docs.Doc
			derr = json.Unmarshal(r.Row, &x)
			ds = append(ds, x)
		default:
			return nil, fmt.Errorf("restore: table %q is not part of an in-memory backup", r.Table)
		}
		if derr != nil {
			return nil, fmt.Errorf("restore: %s row %d: %w", r.Table, n[r.Table]+1, derr)
		}
		n[r.Table]++
	}
	for _, err := range []error{m.Raw.ImportTenant(tenant, raws), m.Trades.ImportTenant(tenant, trades), m.Breaks.ImportTenant(tenant, breaks),
		m.Ledger.ImportTenant(tenant, ents), m.Audit.ImportTenant(tenant, audit), m.Docs.ImportTenant(tenant, ds)} {
		if err != nil {
			return nil, err
		}
	}
	return n, nil
}
