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

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- archiving a past period: the record and the store side ----
//
// A firm moves a closed period out of the live store to cheaper archive
// storage. Ledgerline (ops/archive.go) writes the period's rows, as stored,
// into an encrypted and signed archive package, reads it back and verifies
// it; only then does the store remove those rows (RemovePeriod), in one
// transaction with the ledger entries of opening balances carried forward
// that stand in for the period's entries, so every later balance stays the
// same. Restoring (RestorePeriod) puts the same rows back and removes the
// carried-forward entries. Both refuse unless the archive's record allows
// it: a second person approved the archive (or the restore), and the
// package was verified. The PostgreSQL store checks the same in the
// database (db/migrations/0005_archive_period.sql). The audit trail is
// never archived, so its seal chain stays whole.

// Kinds of data an archive can hold.
const (
	ArcFiles      = "files"       // stored exchange, clearing, depository and bank files
	ArcDocuments  = "documents"   // contract notes, reports and other documents Ledgerline sent or kept
	ArcTrades     = "trades"      // trade events
	ArcLedger     = "ledger"      // ledger entries and their postings
	ArcDocHistory = "doc_history" // earlier versions of records (kept by PostgreSQL only)
	ArcMigrated   = "migrated"    // history and documents migrated from the old back office
)

// ArchiveKinds lists the kinds in display order.
var ArchiveKinds = []string{ArcFiles, ArcDocuments, ArcTrades, ArcLedger, ArcDocHistory, ArcMigrated}

// CarryKind is the source kind of a carried-forward entry.
const CarryKind = "ARCHIVE-CARRY"

// CarryAccount balances each carried-forward entry when a period's
// accounts are spread over several entries; it nets to nothing.
const CarryAccount = "EQUITY:ARCHIVE_CARRY"

// carryChunk is the most accounts one carried-forward entry holds
// (PostgreSQL numbers postings with a smallint).
const carryChunk = 5000

// Archive states.
const (
	ArcRequested        = "Requested"
	ArcApproved         = "Approved" // a second person approved; the package is being written and verified
	ArcVerified         = "Verified" // the package is written, read back and verified
	ArcArchived         = "Archived" // the rows are gone from the live store
	ArcRejected         = "Rejected"
	ArcFailed           = "Failed"
	ArcRestoreRequested = "Restore requested"
	ArcRestoreApproved  = "Restore approved"
	ArcRestored         = "Restored"
)

// ArchiveStat is the rows and bytes of one kind.
type ArchiveStat struct {
	Rows  int   `json:"rows"`
	Bytes int64 `json:"bytes"`
}

// ArchiveRecord is the record of one archive of a period.
type ArchiveRecord struct {
	Tenant       string    `json:"tenant"`
	ID           string    `json:"id"`
	State        string    `json:"state"`
	From         string    `json:"from"`
	To           string    `json:"to"`
	Label        string    `json:"label,omitempty"`
	Kinds        []string  `json:"kinds"`
	Reason       string    `json:"reason,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	RequestedAt  time.Time `json:"requested_at"`
	ApprovedBy   string    `json:"approved_by,omitempty"`
	ApprovedRole string    `json:"approved_role,omitempty"`
	ApprovedAt   time.Time `json:"approved_at,omitzero"`
	RejectedBy   string    `json:"rejected_by,omitempty"`
	Note         string    `json:"note,omitempty"`
	// Preview is what the period held when it was asked for, per kind.
	Preview map[string]ArchiveStat `json:"preview,omitempty"`
	// Kept counts, per kind, rows of the period left live because open
	// items need them (KeptWhy says which).
	Kept    map[string]int `json:"kept,omitempty"`
	KeptWhy []string       `json:"kept_why,omitempty"`
	// The package: its file in archive storage, size, the SHA-256 of its
	// signed manifest, how it is signed, and rows per table in it.
	PackageFile   string                 `json:"package_file,omitempty"`
	PackageBytes  int64                  `json:"package_bytes,omitempty"`
	PackageSHA256 string                 `json:"package_sha256,omitempty"`
	SignedWith    string                 `json:"signed_with,omitempty"`
	PublicKey     string                 `json:"public_key,omitempty"`
	Rows          map[string]int         `json:"rows,omitempty"`
	Freed         map[string]ArchiveStat `json:"freed,omitempty"` // per kind, what left the live store
	VerifiedAt    time.Time              `json:"verified_at,omitzero"`
	CarryEntries  []string               `json:"carry_entries,omitempty"`
	ArchivedAt    time.Time              `json:"archived_at,omitzero"`
	Clients       int                    `json:"clients,omitempty"`
	RetainUntil   string                 `json:"retain_until,omitempty"`
	Error         string                 `json:"error,omitempty"`
	// The restore.
	RestoreRequestedBy string         `json:"restore_requested_by,omitempty"`
	RestoreRequestedAt time.Time      `json:"restore_requested_at,omitzero"`
	RestoreReason      string         `json:"restore_reason,omitempty"`
	RestoreApprovedBy  string         `json:"restore_approved_by,omitempty"`
	RestoreApprovedAt  time.Time      `json:"restore_approved_at,omitzero"`
	RestoredAt         time.Time      `json:"restored_at,omitzero"`
	Restored           map[string]int `json:"restored,omitempty"`
	// Again names the earlier, restored archive of the same period that
	// this one archives again.
	Again string `json:"again,omitempty"`
}

// RemoveAllowed says whether the period's rows may leave the live store
// under this record.
func (r ArchiveRecord) RemoveAllowed() error {
	switch {
	case r.State != ArcVerified:
		return fmt.Errorf("%w: archive %s is %s, not verified", ErrState, r.ID, r.State)
	case r.RequestedBy == "" || r.ApprovedBy == "" || r.ApprovedBy == r.RequestedBy:
		return fmt.Errorf("%w: archive %s needs a second person's approval", ErrState, r.ID)
	case r.PackageSHA256 == "" || r.VerifiedAt.IsZero():
		return fmt.Errorf("%w: archive %s's package has not been verified", ErrState, r.ID)
	}
	return nil
}

// RestoreAllowed says whether the archived rows may be put back.
func (r ArchiveRecord) RestoreAllowed() error {
	switch {
	case r.State != ArcRestoreApproved:
		return fmt.Errorf("%w: archive %s is %s; its restore is not approved", ErrState, r.ID, r.State)
	case r.RestoreRequestedBy == "" || r.RestoreApprovedBy == "" || r.RestoreApprovedBy == r.RestoreRequestedBy:
		return fmt.Errorf("%w: the restore of archive %s needs a second person's approval", ErrState, r.ID)
	}
	return nil
}

var arcNext = map[string][]string{
	ArcRequested:        {ArcApproved, ArcRejected},
	ArcApproved:         {ArcVerified, ArcFailed},
	ArcVerified:         {ArcArchived, ArcFailed},
	ArcFailed:           {ArcApproved, ArcRejected},
	ArcArchived:         {ArcRestoreRequested},
	ArcRestoreRequested: {ArcRestoreApproved, ArcArchived},
	ArcRestoreApproved:  {ArcRestored, ArcArchived},
}

// ArchiveChange checks a change of record, as the database trigger does:
// the state moves forwards only, and the period and the people cannot
// change once approved.
func ArchiveChange(old, nw ArchiveRecord) error {
	if old.State != nw.State {
		ok := false
		for _, s := range arcNext[old.State] {
			ok = ok || s == nw.State
		}
		if !ok {
			return fmt.Errorf("%w: archive %s is %s; it cannot become %s", ErrState, old.ID, old.State, nw.State)
		}
	}
	if old.State != ArcRequested && old.State != ArcFailed && (old.From != nw.From || old.To != nw.To || old.RequestedBy != nw.RequestedBy || old.ApprovedBy != nw.ApprovedBy) {
		return fmt.Errorf("%w: archive %s's period and people cannot change once approved", ErrState, old.ID)
	}
	return nil
}

// ArchiveSelection says which rows of a period go in an archive.
type ArchiveSelection struct {
	From, To string          // inclusive dates, YYYY-MM-DD
	Kinds    map[string]bool // ArchiveKinds chosen
	// DocSources are the stored-file sources that are documents (the rest
	// are exchange and other files); Protected sources are never archived.
	DocSources map[string]bool
	Protected  map[string]bool
	// ExcludeUCC: clients whose ledger entries, trades and records stay
	// live because open items need them.
	ExcludeUCC map[string]bool
	// The migrated history (collection|id of its stored chunks) and the
	// migrated documents' files (raw file ids) to archive.
	MigratedDocs map[string]bool
	MigratedRaw  map[string]bool
}

// RowRef names a stored row by table and key: journal_entry id,
// trade_event origin|exchange|segment|trade_date|trade_no, raw_file id,
// doc_history seq, doc collection|id.
type RowRef struct {
	Table string `json:"t"`
	Key   string `json:"k"`
}

// ArchiveRow is one row of a period as stored, with the kind it counts
// towards. Ref is empty for a row that goes with another (a posting goes
// with its entry).
type ArchiveRow struct {
	Record
	Ref   RowRef
	Kind  string
	Bytes int64
	UCC   []string
	Date  string
}

// PeriodStore archives and restores periods.
type PeriodStore interface {
	SaveArchive(ctx context.Context, r ArchiveRecord) error
	GetArchive(ctx context.Context, tenant, id string) (ArchiveRecord, error)
	ListArchives(ctx context.Context, tenant string) ([]ArchiveRecord, error)
	// PeriodRows calls emit for every row the selection takes, as stored,
	// from one consistent view: the tables in the order they are restored
	// (raw_file, trade_event, journal_entry, posting, doc, doc_history).
	PeriodRows(ctx context.Context, tenant string, sel ArchiveSelection, emit func(ArchiveRow) error) error
	// RemovePeriod removes exactly the rows refs names and appends the
	// carried-forward entries, all or nothing, only when the record allows
	// it (RemoveAllowed) and every account's balance, overall and as of
	// the period's last day, is the same afterwards. finish gets the rows
	// removed per table and the carried-forward entries as stored, and
	// returns the record to keep (Archived); an error undoes everything.
	RemovePeriod(ctx context.Context, tenant, id string, refs []RowRef, carry []ledger.Entry, finish func(removed map[string]int, carried []ledger.Entry) (ArchiveRecord, error)) (ArchiveRecord, error)
	// RestorePeriod puts archived rows back with their ids, sequence
	// numbers and bytes and removes the carried-forward entries, all or
	// nothing, only when the record allows it (RestoreAllowed) and the
	// balances are the same afterwards.
	RestorePeriod(ctx context.Context, tenant, id string, next func() (Record, bool, error), finish func(restored map[string]int) (ArchiveRecord, error)) (ArchiveRecord, error)
}

// ErrArchiveNotFound is returned for an unknown archive record.
var ErrArchiveNotFound = errors.New("archive record not found")

// IST is the time zone stored files are dated in.
var IST = time.FixedZone("IST", 19800)

// EntryHead is what choosing a period's entries needs to know of one.
type EntryHead struct {
	ID, ValueDate, SourceKind, ReversalOf string
	Accounts                              []string
}

// SelectEntries chooses which of a period's entries (heads, the entries
// dated in it) are archived: not a carried-forward entry, not touching a
// client kept live, and not tied by a reversal to an entry that stays live
// (an entry reversed outside the period, or reversing one), so the
// journal's links stay whole. reversals maps every reversing entry of the
// firm to the entry it reverses.
func SelectEntries(heads []EntryHead, reversals map[string]string, sel ArchiveSelection) map[string]bool {
	in := map[string]bool{}
	for _, h := range heads {
		if h.ValueDate < sel.From || h.ValueDate > sel.To || h.SourceKind == CarryKind {
			continue
		}
		keep := false
		for _, a := range h.Accounts {
			if u, _, ok := ledger.ClientCode(a); ok && sel.ExcludeUCC[u] {
				keep = true
			}
		}
		if !keep {
			in[h.ID] = true
		}
	}
	reversedBy := map[string][]string{}
	for rev, target := range reversals {
		reversedBy[target] = append(reversedBy[target], rev)
	}
	for changed := true; changed; {
		changed = false
		for id := range in {
			drop := false
			if t, ok := reversals[id]; ok && !in[t] {
				drop = true
			}
			for _, r := range reversedBy[id] {
				if !in[r] {
					drop = true
				}
			}
			if drop {
				delete(in, id)
				changed = true
			}
		}
	}
	return in
}

// CarryEntries are the entries of opening balances carried forward that
// stand in for a period's archived entries: net is each account's net
// movement (debit positive) in the entries archived. They are dated the
// period's last day, so every balance from the next day on is unchanged.
// Large periods are spread over several entries, each balanced through
// CarryAccount, which nets to nothing.
func CarryEntries(tenant, archiveID, from, to, actor string, net map[string]money.Amount) []ledger.Entry {
	var accts []string
	for a, v := range net {
		if v != 0 {
			accts = append(accts, a)
		}
	}
	sort.Strings(accts)
	if len(accts) == 0 {
		return nil
	}
	var out []ledger.Entry
	for i := 0; i < len(accts); i += carryChunk {
		part := accts[i:min(i+carryChunk, len(accts))]
		var ps []ledger.Posting
		var sum money.Amount
		for _, a := range part {
			v := net[a]
			sum += v
			if v > 0 {
				ps = append(ps, ledger.Posting{Account: a, Debit: v, Memo: "carried forward"})
			} else {
				ps = append(ps, ledger.Posting{Account: a, Credit: -v, Memo: "carried forward"})
			}
		}
		switch {
		case sum > 0:
			ps = append(ps, ledger.Posting{Account: CarryAccount, Credit: sum, Memo: "balances the carried-forward part"})
		case sum < 0:
			ps = append(ps, ledger.Posting{Account: CarryAccount, Debit: -sum, Memo: "balances the carried-forward part"})
		}
		n := len(out) + 1
		out = append(out, ledger.Entry{Tenant: tenant, Key: fmt.Sprintf("%s:%s:%d", CarryKind, archiveID, n), ValueDate: to,
			Narration: fmt.Sprintf("Opening balances carried forward from the archived period %s to %s (archive %s)", from, to, archiveID),
			Source:    ledger.SourceRef{Kind: CarryKind, ID: archiveID, Line: n}, Postings: ps, CreatedBy: actor})
	}
	return out
}

// Balances sums each account's postings (debit positive) over entries,
// up to asOf (inclusive; empty: all).
func Balances(es []ledger.Entry, asOf string) map[string]money.Amount {
	out := map[string]money.Amount{}
	for _, e := range es {
		if asOf != "" && e.ValueDate > asOf {
			continue
		}
		for _, p := range e.Postings {
			out[p.Account] += p.Debit - p.Credit
		}
	}
	for a, v := range out {
		if v == 0 {
			delete(out, a)
		}
	}
	return out
}

// SameBalances reports the first account whose balance differs.
func SameBalances(a, b map[string]money.Amount) (string, bool) {
	for k, v := range a {
		if b[k] != v {
			return k, false
		}
	}
	for k, v := range b {
		if a[k] != v {
			return k, false
		}
	}
	return "", true
}

// ---- memory ----

// MemArchiveStore keeps archive records in memory.
type MemArchiveStore struct {
	mu sync.Mutex
	m  map[string]ArchiveRecord // tenant|id
}

func NewMemArchiveStore() *MemArchiveStore { return &MemArchiveStore{m: map[string]ArchiveRecord{}} }

func (s *MemArchiveStore) save(r ArchiveRecord) error {
	if r.Tenant == "" || r.ID == "" {
		return errors.New("app: an archive record needs a tenant and an id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.m[r.Tenant+"|"+r.ID]; ok {
		if err := ArchiveChange(old, r); err != nil {
			return err
		}
	}
	s.m[r.Tenant+"|"+r.ID] = r
	return nil
}

func (s *MemArchiveStore) get(tenant, id string) (ArchiveRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.m[tenant+"|"+id]
	if !ok {
		return r, fmt.Errorf("%w: %s", ErrArchiveNotFound, id)
	}
	return r, nil
}

func (s *MemArchiveStore) list(tenant string) []ArchiveRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ArchiveRecord{}
	for _, r := range s.m {
		if r.Tenant == tenant {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// purge removes a firm's records (its exit).
func (s *MemArchiveStore) purge(tenant string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, r := range s.m {
		if r.Tenant == tenant {
			delete(s.m, k)
			n++
		}
	}
	return n
}

func (m *MemSnapshot) archives() *MemArchiveStore {
	m.arcOnce.Do(func() {
		if m.Archives == nil {
			m.Archives = NewMemArchiveStore()
		}
	})
	return m.Archives
}

func (m *MemSnapshot) SaveArchive(_ context.Context, r ArchiveRecord) error {
	return m.archives().save(r)
}
func (m *MemSnapshot) GetArchive(_ context.Context, tenant, id string) (ArchiveRecord, error) {
	return m.archives().get(tenant, id)
}
func (m *MemSnapshot) ListArchives(_ context.Context, tenant string) ([]ArchiveRecord, error) {
	return m.archives().list(tenant), nil
}

func jsonSize(v any) (json.RawMessage, int64, error) {
	b, err := json.Marshal(v)
	return b, int64(len(b)), err
}

// rawKind says which kind a stored file counts towards, or "" when it is
// not archived by date.
func rawKind(f ingest.RawFile, sel ArchiveSelection) string {
	if f.Source == "MIG-ARCHIVE" || sel.Protected[f.Source] {
		return ""
	}
	if sel.DocSources[f.Source] {
		return ArcDocuments
	}
	return ArcFiles
}

// PeriodRows implements PeriodStore for the in-memory stores.
func (m *MemSnapshot) PeriodRows(_ context.Context, tenant string, sel ArchiveSelection, emit func(ArchiveRow) error) error {
	trades := m.Trades.ExportTenant(tenant)
	takeTrade := func(t ingest.TradeEvent) bool {
		return sel.Kinds[ArcTrades] && t.TradeDate >= sel.From && t.TradeDate <= sel.To && !sel.ExcludeUCC[strings.ToUpper(t.UCC)]
	}
	// files a trade that stays live still points at stay too
	needed := map[string]bool{}
	for _, t := range trades {
		if t.FileID != "" && !takeTrade(t) {
			needed[t.FileID] = true
		}
	}
	for _, f := range m.Raw.ExportTenant(tenant) {
		k, day := rawKind(f, sel), f.ReceivedAt.In(IST).Format("2006-01-02")
		take := k != "" && sel.Kinds[k] && day >= sel.From && day <= sel.To && !needed[f.ID]
		if sel.Kinds[ArcMigrated] && sel.MigratedRaw[f.ID] && f.Source == "MIG-ARCHIVE" {
			take, k = true, ArcMigrated
		}
		if !take {
			continue
		}
		b, n, err := jsonSize(memRaw{f, f.Content})
		if err != nil {
			return err
		}
		if err := emit(ArchiveRow{Record: Record{Table: "raw_file", Row: b}, Ref: RowRef{"raw_file", f.ID}, Kind: k, Bytes: n, Date: day}); err != nil {
			return err
		}
	}
	for _, t := range trades {
		if !takeTrade(t) {
			continue
		}
		b, n, err := jsonSize(t)
		if err != nil {
			return err
		}
		if err := emit(ArchiveRow{Record: Record{Table: "trade_event", Row: b}, Ref: RowRef{"trade_event", t.TradeRef()}, Kind: ArcTrades, Bytes: n, UCC: []string{strings.ToUpper(t.UCC)}, Date: t.TradeDate}); err != nil {
			return err
		}
	}
	if sel.Kinds[ArcLedger] {
		es := m.Ledger.ExportTenant(tenant)
		var heads []EntryHead
		revs := map[string]string{}
		for _, e := range es {
			if e.ReversalOf != "" {
				revs[e.ID] = e.ReversalOf
			}
			if e.ValueDate >= sel.From && e.ValueDate <= sel.To {
				h := EntryHead{ID: e.ID, ValueDate: e.ValueDate, SourceKind: e.Source.Kind, ReversalOf: e.ReversalOf}
				for _, p := range e.Postings {
					h.Accounts = append(h.Accounts, p.Account)
				}
				heads = append(heads, h)
			}
		}
		take := SelectEntries(heads, revs, sel)
		for _, e := range es {
			if !take[e.ID] {
				continue
			}
			b, n, err := jsonSize(e)
			if err != nil {
				return err
			}
			var uccs []string
			for _, p := range e.Postings {
				if u, _, ok := ledger.ClientCode(p.Account); ok && !contains(uccs, u) {
					uccs = append(uccs, u)
				}
			}
			if err := emit(ArchiveRow{Record: Record{Table: "journal_entry", Row: b}, Ref: RowRef{"journal_entry", e.ID}, Kind: ArcLedger, Bytes: n, UCC: uccs, Date: e.ValueDate}); err != nil {
				return err
			}
		}
	}
	if sel.Kinds[ArcMigrated] && len(sel.MigratedDocs) > 0 {
		for _, d := range m.Docs.ExportTenant(tenant) {
			if !sel.MigratedDocs[d.Collection+"|"+d.ID] {
				continue
			}
			b, n, err := jsonSize(d)
			if err != nil {
				return err
			}
			if err := emit(ArchiveRow{Record: Record{Table: "doc", Row: b}, Ref: RowRef{"doc", d.Collection + "|" + d.ID}, Kind: ArcMigrated, Bytes: n}); err != nil {
				return err
			}
		}
	}
	// The in-memory store keeps no earlier versions of records, so there
	// is no document history to archive.
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// RemovePeriod implements PeriodStore for the in-memory stores. They have
// no transaction, so what is removed is kept and put back on any error.
func (m *MemSnapshot) RemovePeriod(ctx context.Context, tenant, id string, refs []RowRef, carry []ledger.Entry, finish func(map[string]int, []ledger.Entry) (ArchiveRecord, error)) (ArchiveRecord, error) {
	m.purgeMu.Lock()
	defer m.purgeMu.Unlock()
	r, err := m.archives().get(tenant, id)
	if err != nil {
		return ArchiveRecord{}, err
	}
	if err := r.RemoveAllowed(); err != nil {
		return ArchiveRecord{}, err
	}
	if r.To >= currentFYStart(time.Now()) {
		return ArchiveRecord{}, fmt.Errorf("%w: archive %s reaches into the current financial year", ErrState, id)
	}
	want := map[string]map[string]bool{}
	for _, x := range refs {
		if want[x.Table] == nil {
			want[x.Table] = map[string]bool{}
		}
		if want[x.Table][x.Key] {
			return ArchiveRecord{}, fmt.Errorf("%w: %s %s is listed twice", ErrState, x.Table, x.Key)
		}
		want[x.Table][x.Key] = true
	}
	for t := range want {
		switch t {
		case "raw_file", "trade_event", "journal_entry", "doc":
		default:
			return ArchiveRecord{}, fmt.Errorf("%w: table %s cannot be archived on this storage", ErrState, t)
		}
	}
	all := m.Ledger.ExportTenant(tenant)
	bAll, bTo := Balances(all, ""), Balances(all, r.To)
	// every entry listed must be of the period and not carried forward
	for _, e := range all {
		if want["journal_entry"][e.ID] && (e.ValueDate < r.From || e.ValueDate > r.To || e.Source.Kind == CarryKind) {
			return ArchiveRecord{}, fmt.Errorf("%w: entry %s is not in archive %s's period", ErrState, e.ID, id)
		}
	}
	var (
		trades []ingest.TradeEvent
		ents   []ledger.Entry
		ds     []docs.Doc
		files  []ingest.RawFile
		stored []ledger.Entry
	)
	undo := func() {
		ids := map[string]bool{}
		for _, e := range stored {
			ids[e.ID] = true
		}
		m.Ledger.RemoveEntries(tenant, ids)
		_ = m.Ledger.PutEntries(tenant, ents)
		_ = m.Trades.PutTrades(tenant, trades)
		_ = m.Docs.PutDocs(tenant, ds)
		_ = m.Raw.PutFiles(tenant, files)
	}
	trades = m.Trades.RemoveTrades(tenant, want["trade_event"])
	ents = m.Ledger.RemoveEntries(tenant, want["journal_entry"])
	ds = m.Docs.RemoveDocs(tenant, want["doc"])
	files = m.Raw.RemoveFiles(tenant, want["raw_file"])
	n := map[string]int{"trade_event": len(trades), "journal_entry": len(ents), "doc": len(ds), "raw_file": len(files)}
	for _, e := range ents {
		n["posting"] += len(e.Postings)
	}
	for t, keys := range want {
		if n[t] != len(keys) {
			undo()
			return ArchiveRecord{}, fmt.Errorf("%w: only %d of the %d %s rows listed are stored", ErrState, n[t], len(keys), t)
		}
	}
	for _, t := range trades {
		if t.TradeDate < r.From || t.TradeDate > r.To {
			undo()
			return ArchiveRecord{}, fmt.Errorf("%w: trade %s is not in archive %s's period", ErrState, t.TradeRef(), id)
		}
	}
	for _, c := range carry {
		if c.Source.Kind != CarryKind || c.Source.ID != id || c.ValueDate != r.To {
			undo()
			return ArchiveRecord{}, fmt.Errorf("%w: %s is not a carried-forward entry of archive %s", ErrState, c.Key, id)
		}
		e, _, err := m.Ledger.Append(ctx, c)
		if err != nil {
			undo()
			return ArchiveRecord{}, err
		}
		stored = append(stored, e)
	}
	after := m.Ledger.ExportTenant(tenant)
	if a, ok := SameBalances(bAll, Balances(after, "")); !ok {
		undo()
		return ArchiveRecord{}, fmt.Errorf("%w: account %s's balance would change; nothing was archived", ErrState, a)
	}
	if a, ok := SameBalances(bTo, Balances(after, r.To)); !ok {
		undo()
		return ArchiveRecord{}, fmt.Errorf("%w: account %s's balance on %s would change; nothing was archived", ErrState, a, r.To)
	}
	out, err := finish(n, stored)
	if err == nil {
		if out.Tenant != tenant || out.ID != id || out.State != ArcArchived {
			err = fmt.Errorf("%w: archiving must end with archive %s archived", ErrState, id)
		} else {
			err = m.archives().save(out)
		}
	}
	if err != nil {
		undo()
		return ArchiveRecord{}, err
	}
	return out, nil
}

// RestorePeriod implements PeriodStore for the in-memory stores.
func (m *MemSnapshot) RestorePeriod(ctx context.Context, tenant, id string, next func() (Record, bool, error), finish func(map[string]int) (ArchiveRecord, error)) (ArchiveRecord, error) {
	m.purgeMu.Lock()
	defer m.purgeMu.Unlock()
	r, err := m.archives().get(tenant, id)
	if err != nil {
		return ArchiveRecord{}, err
	}
	if err := r.RestoreAllowed(); err != nil {
		return ArchiveRecord{}, err
	}
	var (
		files  []ingest.RawFile
		trades []ingest.TradeEvent
		ents   []ledger.Entry
		ds     []docs.Doc
		n      = map[string]int{}
	)
	for {
		rec, ok, err := next()
		if err != nil {
			return ArchiveRecord{}, err
		}
		if !ok {
			break
		}
		var derr error
		switch rec.Table {
		case "raw_file":
			var x memRaw
			derr = json.Unmarshal(rec.Row, &x)
			x.RawFile.Content = x.Content
			files = append(files, x.RawFile)
		case "trade_event":
			var x ingest.TradeEvent
			derr = json.Unmarshal(rec.Row, &x)
			trades = append(trades, x)
		case "journal_entry":
			var x ledger.Entry
			derr = json.Unmarshal(rec.Row, &x)
			if derr == nil && (x.ValueDate < r.From || x.ValueDate > r.To) {
				derr = fmt.Errorf("entry %s is not in the archive's period", x.ID)
			}
			ents = append(ents, x)
			n["posting"] += len(x.Postings)
		case "doc":
			var x docs.Doc
			derr = json.Unmarshal(rec.Row, &x)
			ds = append(ds, x)
		default:
			return ArchiveRecord{}, fmt.Errorf("restore: table %q is not part of an in-memory archive", rec.Table)
		}
		if derr != nil {
			return ArchiveRecord{}, fmt.Errorf("restore: %s row %d: %w", rec.Table, n[rec.Table]+1, derr)
		}
		n[rec.Table]++
	}
	all := m.Ledger.ExportTenant(tenant)
	bAll, bTo := Balances(all, ""), Balances(all, r.To)
	carry := map[string]bool{}
	for _, e := range all {
		if e.Source.Kind == CarryKind && e.Source.ID == id {
			carry[e.ID] = true
		}
	}
	if len(carry) != len(r.CarryEntries) {
		return ArchiveRecord{}, fmt.Errorf("%w: archive %s's carried-forward entries are not all there (%d of %d)", ErrState, id, len(carry), len(r.CarryEntries))
	}
	removed := m.Ledger.RemoveEntries(tenant, carry)
	put := 0
	undo := func() {
		ids := map[string]bool{}
		for _, e := range ents {
			ids[e.ID] = true
		}
		if put > 0 {
			m.Ledger.RemoveEntries(tenant, ids)
			fi := map[string]bool{}
			for _, f := range files {
				fi[f.ID] = true
			}
			m.Raw.RemoveFiles(tenant, fi)
			tr := map[string]bool{}
			for _, t := range trades {
				tr[t.TradeRef()] = true
			}
			m.Trades.RemoveTrades(tenant, tr)
			dr := map[string]bool{}
			for _, d := range ds {
				dr[d.Collection+"|"+d.ID] = true
			}
			m.Docs.RemoveDocs(tenant, dr)
		}
		_ = m.Ledger.PutEntries(tenant, removed)
	}
	put = 1
	for _, err := range []error{m.Raw.PutFiles(tenant, files), m.Trades.PutTrades(tenant, trades), m.Ledger.PutEntries(tenant, ents), m.Docs.PutDocs(tenant, ds)} {
		if err != nil {
			undo()
			return ArchiveRecord{}, fmt.Errorf("%w: %v", ErrState, err)
		}
	}
	after := m.Ledger.ExportTenant(tenant)
	if a, ok := SameBalances(bAll, Balances(after, "")); !ok {
		undo()
		return ArchiveRecord{}, fmt.Errorf("%w: account %s's balance would change; nothing was restored", ErrState, a)
	}
	if a, ok := SameBalances(bTo, Balances(after, r.To)); !ok {
		undo()
		return ArchiveRecord{}, fmt.Errorf("%w: account %s's balance on %s would change; nothing was restored", ErrState, a, r.To)
	}
	n["carry_removed"] = len(removed)
	out, err := finish(n)
	if err == nil {
		if out.Tenant != tenant || out.ID != id || out.State != ArcRestored {
			err = fmt.Errorf("%w: restoring must end with archive %s restored", ErrState, id)
		} else {
			err = m.archives().save(out)
		}
	}
	if err != nil {
		undo()
		return ArchiveRecord{}, err
	}
	return out, nil
}

// currentFYStart is the first day of the financial year t falls in (India).
func currentFYStart(t time.Time) string {
	t = t.In(IST)
	y := t.Year()
	if t.Month() < time.April {
		y--
	}
	return fmt.Sprintf("%d-04-01", y)
}

// CurrentFYStart is the first day of the financial year t falls in.
func CurrentFYStart(t time.Time) string { return currentFYStart(t) }
