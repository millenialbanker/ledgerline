// Package app wires ingestion, reconciliation, billing and the ledger into
// the operations a broker runs each day, and records every action in the
// audit log. Storage is behind interfaces: in-memory for tests and local
// runs, PostgreSQL (internal/pgstore) in production.
package app

import (
	"context"
	"encoding/json"
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
	// Prev and Hash chain the tenant's entries: Hash is a keyed hash over
	// this entry and Prev (the previous entry's Hash), so an edited, removed
	// or inserted entry breaks the chain. Empty on entries written before
	// sealing was switched on.
	Prev string `json:"prev,omitempty"`
	Hash string `json:"hash,omitempty"`
	Seq  int64  `json:"seq,omitempty"`
}

// AuditSealer computes an entry's Hash once Prev is set.
type AuditSealer func(e AuditEvent) (string, error)

// ChainedAudit is implemented by audit stores that can append sealed
// entries: under a lock per tenant, it reads the last Hash, sets Prev, calls
// seal and stores the entry with its Hash. Scan returns entries oldest
// first after a sequence number.
type ChainedAudit interface {
	LogSealed(ctx context.Context, e AuditEvent, seal AuditSealer) error
	Scan(ctx context.Context, tenant string, after int64, limit int) ([]AuditEvent, error)
}

// AuditCanonical is the exact byte string an entry's Hash covers. Time is
// UTC to the microsecond (what PostgreSQL keeps) and the detail is
// normalised (sorted keys, numbers as JSON numbers), so the same bytes are
// produced when the entry is read back from either store.
func AuditCanonical(e AuditEvent) ([]byte, error) {
	var det any
	if len(e.Detail) > 0 {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &det); err != nil {
			return nil, err
		}
	}
	return json.Marshal([]any{"ll-audit-v1", e.Tenant, e.At.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), e.Actor, e.Action, e.Object, det, e.Prev})
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
	Snap     Snapshotter
	Schedule charges.Schedule
	Now      func() time.Time
	// Adjust, when set, rewrites exchange trades before recon and billing,
	// for example to apply client code modifications.
	Adjust func(ctx context.Context, tenant string, evs []ingest.TradeEvent) ([]ingest.TradeEvent, error)
	// BillingHooks, when set, supplies per-client brokerage plans and lot
	// sizes for a billing run.
	BillingHooks func(ctx context.Context, tenant, date string) (BrokerageFunc, func(ingest.TradeEvent) int64, error)
	runSeq       int64
	mu           sync.Mutex
}

// Stores bundles the persistence an App needs.
type Stores struct {
	Ledger ledger.Store
	Raw    ingest.RawStore
	Trades ingest.TradeStore
	Breaks BreakStore
	Audit  AuditStore
	Docs   docs.Store
	// Snap backs up and restores a whole firm (nil: backups unavailable).
	Snap Snapshotter
}

// MemoryStores returns in-memory implementations of every store.
func MemoryStores() Stores {
	l, r, t, b, a, d := ledger.NewMemStore(), ingest.NewMemRawStore(), ingest.NewMemTradeStore(), NewMemBreakStore(), NewMemAuditStore(), docs.NewMem()
	return Stores{Ledger: l, Raw: r, Trades: t, Breaks: b, Audit: a, Docs: d,
		Snap: &MemSnapshot{Ledger: l, Raw: r, Trades: t, Breaks: b, Audit: a, Docs: d}}
}

// New builds an App.
func New(s Stores, reg *ingest.Registry, sched charges.Schedule) *App {
	return &App{
		Ledger: ledger.New(s.Ledger),
		Ingest: &ingest.Service{Raw: s.Raw, Trades: s.Trades, Registry: reg},
		Trades: s.Trades, Breaks: s.Breaks, Audit: s.Audit, Docs: s.Docs, Snap: s.Snap, Schedule: sched, Now: time.Now,
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
	return a.RunBillingFor(ctx, tenant, actor, date, prices, rebill, "", "")
}

// RunBillingFor bills a date, optionally for one client only (ucc), with a
// reason recorded on any reversal. Rebilling one client leaves every other
// client's bills untouched.
func (a *App) RunBillingFor(ctx context.Context, tenant, actor, date string, prices map[string]money.Dec, rebill bool, ucc, reason string) (BillingResult, error) {
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
	out, err := a.BuildBills(ctx, billing.Input{Tenant: tenant, Date: date, Actor: actor, Trades: trades, SettlementPrices: prices, Schedule: a.Schedule})
	if err != nil {
		return BillingResult{}, err
	}
	mine := func(srcID string) bool { // source ID is date/ucc/EXCH-SEG
		if ucc == "" {
			return true
		}
		p := strings.Split(srcID, "/")
		return len(p) == 3 && p[1] == ucc
	}
	if ucc != "" {
		var es []ledger.Entry
		for _, e := range out.Entries {
			if mine(e.Source.ID) {
				es = append(es, e)
			}
		}
		var gs []billing.Group
		for _, g := range out.Groups {
			if g.UCC == ucc {
				gs = append(gs, g)
			}
		}
		out.Entries, out.Groups = es, gs
	}
	why := func(base string) string {
		if reason != "" {
			return "rebill: " + reason
		}
		return base
	}
	res := BillingResult{Date: date, Posted: []string{}, Existing: []string{}, Conflicts: []string{}, Reversed: []string{}, Warnings: []string{}, Groups: out.Groups}
	if a.Schedule.Sample {
		res.Warnings = append(res.Warnings, "Billed with the SAMPLE charge schedule "+a.Schedule.Version+"; load real rates before go-live.")
	}
	open, _ := a.Breaks.List(ctx, tenant, date, BreakOpen)
	nUCC := 0
	for _, b := range open {
		if b.Type == recon.UCCMismatch || b.Type == recon.MissingInOMS {
			nUCC++
		}
	}
	if nUCC > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d open UCC or missing-in-OMS break(s) for %s; a later client code modification will need a rebill.", nUCC, date))
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
		if e.Source.Kind != "BILL" || !mine(e.Source.ID) {
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
			if err := reverse(cur, why("rebill: trades changed after billing")); err != nil {
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
		if err := reverse(cur, why("rebill: no trades remain for this client segment")); err != nil {
			return res, err
		}
	}
	sort.Strings(res.Conflicts)
	a.log(ctx, tenant, actor, "billing.run", date, map[string]any{"posted": len(res.Posted), "existing": len(res.Existing), "conflicts": len(res.Conflicts), "reversed": len(res.Reversed), "rebill": rebill, "schedule": a.Schedule.Version, "client": ucc, "reason": reason})
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

// ExportTenant returns a tenant's breaks by ID, for backups.
func (s *MemBreakStore) ExportTenant(tenant string) []BreakRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []BreakRecord{}
	for k, b := range s.m {
		if strings.HasPrefix(k, tenant+"|") {
			out = append(out, *b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ImportTenant restores a tenant's breaks. The tenant must have none yet.
func (s *MemBreakStore) ImportTenant(tenant string, bs []BreakRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.m {
		if strings.HasPrefix(k, tenant+"|") {
			return fmt.Errorf("app: tenant %s already has breaks", tenant)
		}
	}
	for i := range bs {
		b := bs[i]
		s.m[tenant+"|"+b.ID] = &b
	}
	return nil
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
	e.Seq = int64(len(s.ev) + 1)
	s.ev = append(s.ev, e)
	return nil
}

func (s *MemAuditStore) LogSealed(_ context.Context, e AuditEvent, seal AuditSealer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.At = e.At.UTC().Truncate(time.Microsecond)
	e.Prev = ""
	for i := len(s.ev) - 1; i >= 0; i-- {
		if s.ev[i].Tenant == e.Tenant && s.ev[i].Hash != "" {
			e.Prev = s.ev[i].Hash
			break
		}
	}
	h, err := seal(e)
	if err != nil {
		return err
	}
	e.Hash, e.Seq = h, int64(len(s.ev)+1)
	s.ev = append(s.ev, e)
	return nil
}

func (s *MemAuditStore) Scan(_ context.Context, tenant string, after int64, limit int) ([]AuditEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []AuditEvent{}
	for _, e := range s.ev {
		if e.Tenant == tenant && e.Seq > after {
			out = append(out, e)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// ExportTenant returns a tenant's audit trail in order, for backups.
func (s *MemAuditStore) ExportTenant(tenant string) []AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []AuditEvent{}
	for _, e := range s.ev {
		if e.Tenant == tenant {
			out = append(out, e)
		}
	}
	return out
}

// ImportTenant restores a tenant's audit trail as it was, seals included.
// The tenant must have no entries yet.
func (s *MemAuditStore) ImportTenant(tenant string, es []AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.ev {
		if e.Tenant == tenant {
			return fmt.Errorf("app: tenant %s already has an audit trail", tenant)
		}
	}
	s.ev = append(s.ev, es...)
	return nil
}

// Tamper changes a stored entry in place, for tests of the seal.
func (s *MemAuditStore) Tamper(fn func(e []AuditEvent)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.ev)
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

// BrokerageFunc resolves a client's brokerage rule for a product.
type BrokerageFunc = func(ucc string, b charges.Bucket, exchange, date string) (charges.BrokerageRule, string, bool)

// BuildBills runs billing.Build with the brokerage plans and lot sizes from
// BillingHooks, so bills and contract notes price brokerage the same way.
func (a *App) BuildBills(ctx context.Context, in billing.Input) (billing.Output, error) {
	if a.BillingHooks != nil {
		b, lot, err := a.BillingHooks(ctx, in.Tenant, in.Date)
		if err != nil {
			return billing.Output{}, err
		}
		in.Brokerage, in.LotSize = b, lot
	}
	return billing.Build(in)
}
