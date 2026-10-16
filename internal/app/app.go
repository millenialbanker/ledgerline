// Package app wires ingestion, reconciliation, billing and the ledger into
// the operations a broker runs each day, and records every action in the
// audit log. Storage is behind interfaces: in-memory for tests and local
// runs, PostgreSQL (internal/pgstore) in production.
package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/billing"
	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/recon"
)

// Break statuses.
const (
	BreakOpen     = "OPEN"
	BreakResolved = "RESOLVED"
	BreakCleared  = "CLEARED" // disappeared on a later run, e.g. after the OMS was corrected
)

// BreakRecord is a break with its workflow state.
type BreakRecord struct {
	recon.Break
	Status     string     `json:"status"`
	Resolution string     `json:"resolution,omitempty"`
	ResolvedBy string     `json:"resolved_by,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	FirstSeen  time.Time  `json:"first_seen"`
	LastRun    string     `json:"last_run"`
}

// BreakStore keeps breaks across reconciliation runs.
type BreakStore interface {
	// SaveRun records a run: new breaks open, persisting breaks keep their
	// state, and open breaks no longer present become CLEARED.
	SaveRun(ctx context.Context, tenant, runID string, r recon.Result, at time.Time) error
	List(ctx context.Context, tenant, date, status string) ([]BreakRecord, error)
	Resolve(ctx context.Context, tenant, id, actor, resolution string, at time.Time) (BreakRecord, error)
}

// AuditEvent is one recorded action.
type AuditEvent struct {
	Tenant string         `json:"tenant"`
	At     time.Time      `json:"at"`
	Actor  string         `json:"actor"`
	Action string         `json:"action"`
	Object string         `json:"object"`
	Detail map[string]any `json:"detail,omitempty"`
}

// AuditStore is append-only.
type AuditStore interface {
	Log(ctx context.Context, e AuditEvent) error
	List(ctx context.Context, tenant string, limit int) ([]AuditEvent, error)
}

var (
	ErrNotFound = errors.New("not found")
	ErrState    = errors.New("invalid state")
)

// App is the back office core.
type App struct {
	Ledger   *ledger.Ledger
	Ingest   *ingest.Service
	Trades   ingest.TradeStore
	Breaks   BreakStore
	Audit    AuditStore
	Docs     docs.Store
	Schedule charges.Schedule
	Now      func() time.Time
	// Adjust, when set, rewrites exchange trades before recon and billing,
	// for example to apply client code modifications.
	Adjust func(ctx context.Context, tenant string, evs []ingest.TradeEvent) ([]ingest.TradeEvent, error)
	runSeq int64
	mu     sync.Mutex
}

// Stores bundles the persistence an App needs.
type Stores struct {
	Ledger ledger.Store
	Raw    ingest.RawStore
	Trades ingest.TradeStore
	Breaks BreakStore
	Audit  AuditStore
	Docs   docs.Store
}

// MemoryStores returns in-memory implementations of every store.
func MemoryStores() Stores {
	return Stores{Ledger: ledger.NewMemStore(), Raw: ingest.NewMemRawStore(), Trades: ingest.NewMemTradeStore(), Breaks: NewMemBreakStore(), Audit: NewMemAuditStore(), Docs: docs.NewMem()}
}

// New builds an App.
func New(s Stores, reg *ingest.Registry, sched charges.Schedule) *App {
	return &App{
		Ledger: ledger.New(s.Ledger),
		Ingest: &ingest.Service{Raw: s.Raw, Trades: s.Trades, Registry: reg},
		Trades: s.Trades, Breaks: s.Breaks, Audit: s.Audit, Docs: s.Docs, Schedule: sched, Now: time.Now,
	}
}

// Log records an audit event.
func (a *App) Log(ctx context.Context, tenant, actor, action, object string, detail map[string]any) {
	a.log(ctx, tenant, actor, action, object, detail)
}

func (a *App) log(ctx context.Context, tenant, actor, action, object string, detail map[string]any) {
	_ = a.Audit.Log(ctx, AuditEvent{Tenant: tenant, At: a.Now().UTC(), Actor: actor, Action: action, Object: object, Detail: detail})
}

func need(actor string) error {
	if strings.TrimSpace(actor) == "" {
		return errors.New("an actor is required for this action")
	}
	return nil
}

// IngestFile stores and parses one uploaded file.
func (a *App) IngestFile(ctx context.Context, tenant, actor, source, version, name string, data []byte) (ingest.Result, error) {
	if err := need(actor); err != nil {
		return ingest.Result{}, err
	}
	res, err := a.Ingest.Ingest(ctx, tenant, source, version, name, data)
	detail := map[string]any{"source": source, "name": name, "parsed": res.Parsed, "added": res.Added, "duplicates": res.DuplicateRows, "row_errors": len(res.Errors)}
	if err != nil {
		detail["error"] = err.Error()
		a.log(ctx, tenant, actor, "file.rejected", res.File.ID, detail)
		return res, err
	}
	a.log(ctx, tenant, actor, "file.ingested", res.File.ID, detail)
	return res, nil
}

// ReconSummary is a run result plus break workflow counts.
type ReconSummary struct {
	RunID  string       `json:"run_id"`
	Result recon.Result `json:"result"`
	Open   int          `json:"open"`
}

// RunRecon reconciles one trade date.
func (a *App) RunRecon(ctx context.Context, tenant, actor, date string) (ReconSummary, error) {
	if err := need(actor); err != nil {
		return ReconSummary{}, err
	}
	if !ledger.ValidDate(date) {
		return ReconSummary{}, fmt.Errorf("invalid date %q", date)
	}
	ex, err := a.ExchangeTrades(ctx, tenant, date)
	if err != nil {
		return ReconSummary{}, err
	}
	om, err := a.Trades.List(ctx, tenant, ingest.FromOMS, date)
	if err != nil {
		return ReconSummary{}, err
	}
	res := recon.Run(date, ex, om, recon.Options{})
	a.mu.Lock()
	a.runSeq++
	runID := fmt.Sprintf("RUN-%s-%d", strings.ReplaceAll(date, "-", ""), a.Now().UnixNano()/1e6+a.runSeq)
	a.mu.Unlock()
	if err := a.Breaks.SaveRun(ctx, tenant, runID, res, a.Now().UTC()); err != nil {
		return ReconSummary{}, err
	}
	open, err := a.Breaks.List(ctx, tenant, date, BreakOpen)
	if err != nil {
		return ReconSummary{}, err
	}
	a.log(ctx, tenant, actor, "recon.run", runID, map[string]any{"date": date, "matched": res.Matched, "breaks": len(res.Breaks), "open": len(open)})
	return ReconSummary{RunID: runID, Result: res, Open: len(open)}, nil
}

// ResolveBreak closes an open break with a written resolution.
func (a *App) ResolveBreak(ctx context.Context, tenant, actor, id, resolution string) (BreakRecord, error) {
	if err := need(actor); err != nil {
		return BreakRecord{}, err
	}
	if strings.TrimSpace(resolution) == "" {
		return BreakRecord{}, errors.New("a resolution note is required")
	}
	b, err := a.Breaks.Resolve(ctx, tenant, id, actor, resolution, a.Now().UTC())
	if err != nil {
		return BreakRecord{}, err
	}
	a.log(ctx, tenant, actor, "recon.break.resolved", id, map[string]any{"resolution": resolution, "type": b.Type})
	return b, nil
}

// BillingResult reports a billing run.
type BillingResult struct {
	Date      string          `json:"date"`
	Posted    []string        `json:"posted"`   // entry IDs created now
	Existing  []string        `json:"existing"` // already billed with identical content
	Conflicts []string        `json:"conflicts"`
	Reversed  []string        `json:"reversed"` // old bills reversed by a rebill
	Warnings  []string        `json:"warnings"`
	Groups    []billing.Group `json:"groups"`
}

// ExchangeTrades returns a date's exchange trades with Adjust applied.
func (a *App) ExchangeTrades(ctx context.Context, tenant, date string) ([]ingest.TradeEvent, error) {
	ex, err := a.Trades.List(ctx, tenant, ingest.FromExchange, date)
	if err != nil || a.Adjust == nil {
		return ex, err
	}
	return a.Adjust(ctx, tenant, ex)
}

// RunBilling bills a trade date without rebilling; see RunBillingOpts.
func (a *App) RunBilling(ctx context.Context, tenant, actor, date string, prices map[string]money.Dec) (BillingResult, error) {
	return a.RunBillingOpts(ctx, tenant, actor, date, prices, false)
}

// RunBillingOpts bills a trade date from exchange trades. Re-running is safe:
// groups billed with identical content are left alone. A group whose trades
// changed since billing (for example after a client code modification) is a
// conflict; with rebill=true the old bill is reversed and a revised bill is
// posted under a new revision key, and bills for groups that no longer have
// trades are reversed.
func (a *App) RunBillingOpts(ctx context.Context, tenant, actor, date string, prices map[string]money.Dec, rebill bool) (BillingResult, error) {
	if err := need(actor); err != nil {
		return BillingResult{}, err
	}
	trades, err := a.ExchangeTrades(ctx, tenant, date)
	if err != nil {
		return BillingResult{}, err
	}
	if len(trades) == 0 {
		return BillingResult{}, fmt.Errorf("%w: no exchange trades loaded for %s", ErrState, date)
	}
	out, err := billing.Build(billing.Input{Tenant: tenant, Date: date, Actor: actor, Trades: trades, SettlementPrices: prices, Schedule: a.Schedule})
	if err != nil {
		return BillingResult{}, err
	}
	res := BillingResult{Date: date, Posted: []string{}, Existing: []string{}, Conflicts: []string{}, Reversed: []string{}, Warnings: []string{}, Groups: out.Groups}
	if a.Schedule.Sample {
		res.Warnings = append(res.Warnings, "Billed with the SAMPLE charge schedule "+a.Schedule.Version+"; load real rates before go-live.")
	}
	open, _ := a.Breaks.List(ctx, tenant, date, BreakOpen)
	ucc := 0
	for _, b := range open {
		if b.Type == recon.UCCMismatch || b.Type == recon.MissingInOMS {
			ucc++
		}
	}
	if ucc > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d open UCC or missing-in-OMS break(s) for %s; a later client code modification will need a rebill.", ucc, date))
	}

	// Current bills for the date: latest unreversed entry per bill source.
	existing, err := a.Ledger.Store.List(ctx, tenant, ledger.Filter{From: date, To: date})
	if err != nil {
		return res, err
	}
	reversed := map[string]bool{}
	for _, e := range existing {
		if e.ReversalOf != "" {
			reversed[e.ReversalOf] = true
		}
	}
	latest := map[string]ledger.Entry{}
	revisions := map[string]int{}
	for _, e := range existing {
		if e.Source.Kind != "BILL" {
			continue
		}
		revisions[e.Source.ID]++
		if !reversed[e.ID] {
			if cur, ok := latest[e.Source.ID]; !ok || e.Seq > cur.Seq {
				latest[e.Source.ID] = e
			}
		}
	}
	reverse := func(old ledger.Entry, why string) error {
		r, _, err := a.Ledger.Reverse(ctx, tenant, old.ID, actor, why, "")
		if err != nil {
			return err
		}
		res.Reversed = append(res.Reversed, r.ID)
		return nil
	}
	built := map[string]bool{}
	for _, e := range out.Entries {
		built[e.Source.ID] = true
		if cur, ok := latest[e.Source.ID]; ok {
			same := e
			same.Key = cur.Key
			if same.Fingerprint() == cur.Fingerprint() {
				res.Existing = append(res.Existing, cur.ID)
				continue
			}
			if !rebill {
				res.Conflicts = append(res.Conflicts, cur.Key)
				continue
			}
			if err := reverse(cur, "rebill: trades changed after billing"); err != nil {
				return res, err
			}
		}
		if n := revisions[e.Source.ID]; n > 0 {
			e.Key = fmt.Sprintf("%s#r%d", e.Key, n+1)
		}
		stored, created, err := a.Ledger.Post(ctx, e)
		if err != nil {
			return res, fmt.Errorf("posting %s: %w", e.Key, err)
		}
		if created {
			res.Posted = append(res.Posted, stored.ID)
		} else {
			res.Existing = append(res.Existing, stored.ID)
		}
	}
	for src, cur := range latest {
		if built[src] {
			continue
		}
		if !rebill {
			res.Conflicts = append(res.Conflicts, cur.Key)
			continue
		}
		if err := reverse(cur, "rebill: no trades remain for this client segment"); err != nil {
			return res, err
		}
	}
	sort.Strings(res.Conflicts)
	a.log(ctx, tenant, actor, "billing.run", date, map[string]any{"posted": len(res.Posted), "existing": len(res.Existing), "conflicts": len(res.Conflicts), "reversed": len(res.Reversed), "rebill": rebill, "schedule": a.Schedule.Version})
	return res, nil
}

// PostManual posts a user-entered journal entry.
func (a *App) PostManual(ctx context.Context, e ledger.Entry) (ledger.Entry, bool, error) {
	if err := need(e.CreatedBy); err != nil {
		return ledger.Entry{}, false, err
	}
	e.Source = ledger.SourceRef{Kind: "MANUAL", ID: e.CreatedBy}
	stored, created, err := a.Ledger.Post(ctx, e)
	if err == nil && created {
		a.log(ctx, e.Tenant, e.CreatedBy, "journal.posted", stored.ID, map[string]any{"key": e.Key, "narration": e.Narration})
	}
	return stored, created, err
}

// Reverse reverses an entry.
func (a *App) Reverse(ctx context.Context, tenant, actor, id, reason string) (ledger.Entry, bool, error) {
	if err := need(actor); err != nil {
		return ledger.Entry{}, false, err
	}
	e, created, err := a.Ledger.Reverse(ctx, tenant, id, actor, reason, "")
	if err == nil && created {
		a.log(ctx, tenant, actor, "journal.reversed", id, map[string]any{"reversal": e.ID, "reason": reason})
	}
	return e, created, err
}

// MemBreakStore is an in-memory BreakStore.
type MemBreakStore struct {
	mu sync.Mutex
	m  map[string]*BreakRecord // tenant|id
}

func NewMemBreakStore() *MemBreakStore { return &MemBreakStore{m: map[string]*BreakRecord{}} }

func (s *MemBreakStore) SaveRun(_ context.Context, tenant, runID string, r recon.Result, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for _, b := range r.Breaks {
		k := tenant + "|" + b.ID
		seen[k] = true
		if ex, ok := s.m[k]; ok {
			ex.Break, ex.LastRun = b, runID
			if ex.Status == BreakCleared {
				ex.Status = BreakOpen
			}
			continue
		}
		s.m[k] = &BreakRecord{Break: b, Status: BreakOpen, FirstSeen: at, LastRun: runID}
	}
	for k, b := range s.m {
		if strings.HasPrefix(k, tenant+"|") && b.TradeDate == r.Date && !seen[k] && b.Status == BreakOpen {
			b.Status, b.LastRun = BreakCleared, runID
		}
	}
	return nil
}

func (s *MemBreakStore) List(_ context.Context, tenant, date, status string) ([]BreakRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []BreakRecord{}
	for k, b := range s.m {
		if strings.HasPrefix(k, tenant+"|") && (date == "" || b.TradeDate == date) && (status == "" || b.Status == status) {
			out = append(out, *b)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TradeDate != out[j].TradeDate {
			return out[i].TradeDate < out[j].TradeDate
		}
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].TradeNo < out[j].TradeNo
	})
	return out, nil
}

func (s *MemBreakStore) Resolve(_ context.Context, tenant, id, actor, resolution string, at time.Time) (BreakRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[tenant+"|"+id]
	if !ok {
		return BreakRecord{}, fmt.Errorf("%w: break %s", ErrNotFound, id)
	}
	if b.Status != BreakOpen {
		return BreakRecord{}, fmt.Errorf("%w: break %s is %s", ErrState, id, b.Status)
	}
	b.Status, b.Resolution, b.ResolvedBy = BreakResolved, resolution, actor
	t := at
	b.ResolvedAt = &t
	return *b, nil
}

// MemAuditStore is an in-memory AuditStore.
type MemAuditStore struct {
	mu sync.Mutex
	ev []AuditEvent
}

func NewMemAuditStore() *MemAuditStore { return &MemAuditStore{} }

func (s *MemAuditStore) Log(_ context.Context, e AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ev = append(s.ev, e)
	return nil
}

func (s *MemAuditStore) List(_ context.Context, tenant string, limit int) ([]AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []AuditEvent{}
	for i := len(s.ev) - 1; i >= 0 && (limit <= 0 || len(out) < limit); i-- {
		if s.ev[i].Tenant == tenant {
			out = append(out, s.ev[i])
		}
	}
	return out, nil
}
