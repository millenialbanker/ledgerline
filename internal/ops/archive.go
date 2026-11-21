package ops

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
)

// ---- archiving a past period to cheaper storage, and restoring it ----
//
// A firm's live database grows every day with files, trades, ledger
// entries and the history of its records. Years that are closed are
// rarely looked at, but the rules say they must be kept. Admin → Data &
// exit → Archive moves a closed period (financial years, or dates the firm
// chooses) to archive storage (LEDGERLINE_ARCHIVE_DIR: a separate, cheaper
// disk, or object storage mounted as a folder):
//
//  1. An Admin chooses the period and what to archive and sees a preview:
//     per kind, the rows and bytes it would free, and the estimated
//     monthly saving. Ledgerline refuses the current financial year and a
//     period still needed by open items: open reconciliation breaks,
//     pending payouts or pledge requests, an exit in progress. Clients with
//     an unpaid debit, open pledges or an open complaint, and migrated
//     documents on legal hold, stay live (their rows of the period are not
//     archived).
//  2. A second person (the compliance officer or another Admin) approves.
//  3. Ledgerline writes one archive package: the period's rows exactly as
//     stored (files keep their bytes; records stay encrypted with the
//     firm's keys), an index of the clients and dates it covers, the
//     balances carried forward, and a manifest with every file's SHA-256,
//     signed like the full export (the firm's document-signing certificate,
//     else an Ed25519 key made for it). The ZIP is encrypted with the
//     firm's keys as it is written.
//  4. It reads the package back from archive storage and verifies it: the
//     signature, every file's hash, and every row it is about to remove.
//  5. Only then are the rows removed from the live store (in PostgreSQL by
//     ll.archive_remove, which checks the approval and the verification
//     again), in one transaction with the ledger entries of opening
//     balances carried forward that stand in for the period's entries, so
//     every balance, statement and report of a later period is the same.
//     The store refuses if any balance would change.
//
// A live archive index says which periods are archived, of what, and which
// clients and dates they cover. Screens and reports that reach into an
// archived period say "archived on <date>; restore to see it" instead of
// showing wrong or empty figures; the ledger refuses entries dated in an
// archived period. The audit trail is never archived: its seal chain stays
// whole and verifiable.
//
// Restoring takes a whole archive back (a second person approves): the
// package is verified again, the rows go back with their ids, sequence
// numbers, seals and bytes, the carried-forward entries are removed, and
// the balances are checked to be the same. A restored period can be
// archived again later.
//
// Retention: archive packages are kept at least as long as the rules ask
// (ArchiveSettings.RetainYears, 8 by default) and Ledgerline offers no way
// to delete one. When a firm closes its account its archive packages go
// into the final export and are deleted with the rest of its data.

const (
	cArcIdx  = "archidx"  // "periods": arcIndex; "<archive>|<nn>": arcClients
	cArcFile = "archfile" // <raw file id>: arcFileRef
	cArcSet  = "archset"  // "settings": ArchiveSettings

	arcClientShards = 32
)

// Retention of archived records.
//
// What the rules ask, as best known when this was written (check the
// current text of each before relying on it):
//   - PMLA 2002 section 12 and the PML (Maintenance of Records) Rules 2005,
//     rule 3: records of transactions for five years from the
//     transaction; identity records for five years after the business
//     relationship ends (longer while an investigation is open).
//   - SEBI (Stock Brokers) Regulations 1992, regulation 18 with the third
//     schedule, and the SEBI master circular for stock brokers: books of
//     account, contract notes and client records for at least five years;
//     the exchanges' inspection rules and SEBI's circulars on preserving
//     records have asked for eight years for several of them.
//   - SEBI's cloud framework for regulated entities: records kept with a
//     cloud provider remain the firm's to preserve for the periods the
//     regulations set.
//
// Eight years from the end of the archived period is the safe default; a
// firm may set more. Legal holds (records under investigation or in a
// dispute) are not archived at all.
const ArchiveRetainDefault = 8

// Default storage prices for the saving estimate, in rupees per GB a
// month: a managed database's disk with its backups and standby copy, and
// object storage or a plain disk for archives. The firm sets its own.
const (
	arcLiveCostDefault  = 10.0
	arcStoreCostDefault = 2.0
)

// ErrArchived means what was asked for is in an archived period.
var ErrArchived = errors.New("archived")

// ArchiveSettings are the firm's archive rules.
type ArchiveSettings struct {
	RetainYears      int     `json:"retain_years"`
	LiveCostPerGB    float64 `json:"live_cost_per_gb"`    // ₹ per GB-month, the live database
	ArchiveCostPerGB float64 `json:"archive_cost_per_gb"` // ₹ per GB-month, archive storage
}

func (s ArchiveSettings) norm() ArchiveSettings {
	if s.RetainYears < ArchiveRetainDefault {
		s.RetainYears = ArchiveRetainDefault
	}
	if s.LiveCostPerGB <= 0 {
		s.LiveCostPerGB = arcLiveCostDefault
	}
	if s.ArchiveCostPerGB <= 0 {
		s.ArchiveCostPerGB = arcStoreCostDefault
	}
	return s
}

func (o *Ops) archiveSettings(ctx context.Context, tenant string) ArchiveSettings {
	s, _, _ := docs.GetOr(ctx, o.Docs, tenant, cArcSet, "settings", ArchiveSettings{})
	return s.norm()
}

// SetArchiveSettings changes the retention years and the storage prices
// used for the estimate (Admin). Retention cannot go below the default.
func (o *Ops) SetArchiveSettings(ctx context.Context, tenant, actor string, s ArchiveSettings) (ArchiveSettings, error) {
	if err := o.require(ctx, tenant, actor, "Changing the archive rules", RoleAdmin); err != nil {
		return ArchiveSettings{}, err
	}
	if s.RetainYears < ArchiveRetainDefault || s.RetainYears > 50 {
		return ArchiveSettings{}, invalid("keep archives at least %d years (the record-keeping rules) and at most 50", ArchiveRetainDefault)
	}
	if s.LiveCostPerGB < 0 || s.ArchiveCostPerGB < 0 || s.LiveCostPerGB > 100000 || s.ArchiveCostPerGB > 100000 {
		return ArchiveSettings{}, invalid("give the storage prices in rupees per GB a month")
	}
	s = s.norm()
	if err := docs.Upsert(ctx, o.Docs, tenant, cArcSet, "settings", s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "archive.settings", "settings", map[string]any{"retain_years": s.RetainYears, "live_cost_per_gb": s.LiveCostPerGB, "archive_cost_per_gb": s.ArchiveCostPerGB})
	return s, nil
}

// ---- the live index ----

// arcPeriod is one period in the live index. State is Archiving while an
// archive is being written and verified (the period is already closed to
// new entries), Archived once its rows are gone.
type arcPeriod struct {
	ID         string    `json:"id"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Label      string    `json:"label"`
	State      string    `json:"state"`
	Kinds      []string  `json:"kinds"`
	ArchivedAt time.Time `json:"archived_at,omitzero"`
	Clients    int       `json:"clients"`
}

type arcIndex struct {
	Periods []arcPeriod `json:"periods"`
}

// arcClients is one shard of an archive's clients: the first and last
// date of each client's archived rows.
type arcClients struct {
	UCC map[string][2]string `json:"ucc"`
}

// arcFileRef says which archive holds a stored file.
type arcFileRef struct {
	Archive    string    `json:"archive"` // empty once restored
	ArchivedAt time.Time `json:"archived_at"`
	Name       string    `json:"name,omitempty"`
}

func (o *Ops) arcIndex(ctx context.Context, tenant string) arcIndex {
	x, _, _ := docs.GetOr(ctx, o.Docs, tenant, cArcIdx, "periods", arcIndex{})
	return x
}

func (o *Ops) arcIndexPut(ctx context.Context, tenant string, p arcPeriod) error {
	_, err := docs.Update(ctx, o.Docs, tenant, cArcIdx, "periods", SystemActor, func(x *arcIndex) error {
		out := x.Periods[:0]
		for _, q := range x.Periods {
			if q.ID != p.ID {
				out = append(out, q)
			}
		}
		if p.State != "" {
			out = append(out, p)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
		x.Periods = out
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		x := arcIndex{}
		if p.State != "" {
			x.Periods = []arcPeriod{p}
		}
		return docs.Upsert(ctx, o.Docs, tenant, cArcIdx, "periods", x, SystemActor)
	}
	return err
}

func archivedNote(p arcPeriod) string {
	when := "now being archived"
	if p.State == app.ArcArchived {
		when = "archived on " + p.ArchivedAt.In(ist).Format("2 Jan 2006")
	}
	return fmt.Sprintf("%s (%s to %s) was %s (archive %s); restore it on Admin → Data & exit → Archive to see it", firstOf(p.Label, "the period"), arcDay(p.From), arcDay(p.To), when, p.ID)
}

func arcDay(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return d
	}
	return t.Format("2 Jan 2006")
}

func archivedErr(p arcPeriod) error {
	return fmt.Errorf("%w: %w: %s", ErrState, ErrArchived, archivedNote(p))
}

// archivedOn is the archived (or archiving) period a date falls in.
func (o *Ops) archivedOn(ctx context.Context, tenant, date string) (arcPeriod, bool) {
	for _, p := range o.arcIndex(ctx, tenant).Periods {
		if date >= p.From && date <= p.To {
			return p, true
		}
	}
	return arcPeriod{}, false
}

// ArchivedOverlap says (as ErrArchived) when [from, to] reaches into an
// archived period; an empty from means from the beginning. With a client
// code, only periods that hold rows of that client count.
func (o *Ops) ArchivedOverlap(ctx context.Context, tenant, from, to, ucc string) error {
	for _, p := range o.arcIndex(ctx, tenant).Periods {
		if (from == "" || from <= p.To) && (to == "" || to >= p.From) {
			if ucc != "" && !o.arcHasClient(ctx, tenant, p.ID, ucc) {
				continue
			}
			return archivedErr(p)
		}
	}
	return nil
}

// ArchivedPeriodEnd says whether date is the last day of an archived
// period: balances as of that day (and later) are exact, since the
// carried-forward entries are dated it.
func (o *Ops) ArchivedPeriodEnd(ctx context.Context, tenant, date string) bool {
	for _, p := range o.arcIndex(ctx, tenant).Periods {
		if p.To == date && p.State == app.ArcArchived {
			return true
		}
	}
	return false
}

func (o *Ops) arcHasClient(ctx context.Context, tenant, id, ucc string) bool {
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	sh, _, _ := docs.GetOr(ctx, o.Docs, tenant, cArcIdx, fmt.Sprintf("%s|%02d", id, histBucket(ucc, arcClientShards)), arcClients{})
	_, ok := sh.UCC[ucc]
	return ok
}

// arcFrom moves a statement's start past the archived periods it would
// reach into: with no start, the statement starts the day after the last
// archived period (its opening balance carries the archived years in), and
// a start inside an archived period that holds the client's rows is
// refused with ErrArchived.
func (o *Ops) arcFrom(ctx context.Context, tenant, ucc, from, to string) (string, error) {
	auto := from == ""
	for _, p := range o.arcIndex(ctx, tenant).Periods { // in date order
		if (to != "" && to < p.From) || (!auto && from > p.To) {
			continue // the range does not reach the period
		}
		if !o.arcHasClient(ctx, tenant, p.ID, ucc) {
			continue
		}
		if (to != "" && to <= p.To) || !auto {
			// the range ends inside the period, or starts before its end
			// and runs through it: part of it is only in the archive
			return from, archivedErr(p)
		}
		from = addDays(p.To, 1)
	}
	return from, nil
}

// archivedFile says which archive holds a stored file that is no longer
// in the live store.
func (o *Ops) archivedFile(ctx context.Context, tenant, id string) (arcFileRef, bool) {
	ref, _, err := docs.Get[arcFileRef](ctx, o.Docs, tenant, cArcFile, id)
	if err != nil || ref.Archive == "" {
		return ref, false
	}
	return ref, true
}

// arcRaw is the stored-file store as screens see it: a file moved to an
// archive says so instead of "not found".
type arcRaw struct {
	ingest.RawStore
	o *Ops
}

func (r arcRaw) Get(ctx context.Context, tenant, id string) (ingest.RawFile, error) {
	f, err := r.RawStore.Get(ctx, tenant, id)
	if err != nil {
		if ref, ok := r.o.archivedFile(ctx, tenant, id); ok {
			return f, fmt.Errorf("%w: %w: file %s%s was archived on %s (archive %s); restore it on Admin → Data & exit → Archive to see it",
				ErrState, ErrArchived, id, map[bool]string{true: " (" + ref.Name + ")", false: ""}[ref.Name != ""], ref.ArchivedAt.In(ist).Format("2 Jan 2006"), ref.Archive)
		}
	}
	return f, err
}

// ---- choosing a period ----

// ArchiveAsk is what an Admin asks to archive: whole financial years, or a
// period from one date to another, and which kinds.
type ArchiveAsk struct {
	FYs    []string `json:"fys"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	Kinds  []string `json:"kinds"`
	Reason string   `json:"reason"`
}

// ArchiveKindLine is one kind in a preview or a record.
type ArchiveKindLine struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Rows  int    `json:"rows"`
	Bytes int64  `json:"bytes"`
}

// ArchivePreview is what archiving a period would do.
type ArchivePreview struct {
	From        string            `json:"from"`
	To          string            `json:"to"`
	Label       string            `json:"label"`
	Kinds       []ArchiveKindLine `json:"kinds"`
	Rows        int               `json:"rows"`
	Bytes       int64             `json:"bytes"`
	Saving      float64           `json:"saving_per_month"` // ₹, estimated
	Blockers    []string          `json:"blockers"`
	Kept        []string          `json:"kept"`
	RetainUntil string            `json:"retain_until"`
	Notes       []string          `json:"notes,omitempty"`
}

var arcTitles = map[string]string{
	app.ArcFiles:      "Stored exchange, clearing and bank files",
	app.ArcDocuments:  "Contract notes, reports and other documents",
	app.ArcTrades:     "Trade events",
	app.ArcLedger:     "Ledger entries",
	app.ArcDocHistory: "Document history (earlier versions of records)",
	app.ArcMigrated:   "Migrated history and documents",
}

// Stored-file sources that are documents Ledgerline made or kept; the rest
// are files received (exchange, clearing, depository, bank).
var arcDocSources = map[string]bool{"DISPATCH": true, "REPORT-OUT": true, "PERIODIC-OUT": true, "KRA-UPLOAD-OUT": true, "CHAT": true, "BOARD": true, "RPA-SHOT": true}

// Stored-file sources never archived: a client's KYC documents, vendor
// bills and the evidence kept for disclosures and impersonation cases are
// needed for as long as the client or the case is.
var arcProtected = map[string]bool{"CLIENT-DOC": true, "VENDOR-BILL": true, "DISCLOSURE-EVIDENCE": true, "IMPERSONATION-EVIDENCE": true}

// arcPeriodOf resolves the period asked for.
func arcPeriodOf(in ArchiveAsk) (from, to, label string, err error) {
	if len(in.FYs) > 0 {
		fys := append([]string(nil), in.FYs...)
		sort.Strings(fys)
		var prevEnd string
		for i, fy := range fys {
			var y int
			if _, e := fmt.Sscanf(fy, "%d-", &y); e != nil || fmt.Sprintf("%d-%02d", y, (y+1)%100) != fy {
				return "", "", "", invalid("%q is not a financial year like 2019-20", fy)
			}
			s, e := fmt.Sprintf("%d-04-01", y), fmt.Sprintf("%d-03-31", y+1)
			if i > 0 && s != addDays(prevEnd, 1) {
				return "", "", "", invalid("choose financial years one after another (%s does not follow %s)", fy, fys[i-1])
			}
			if i == 0 {
				from = s
			}
			to, prevEnd = e, e
		}
		label = fys[0]
		if len(fys) > 1 {
			label += " to " + fys[len(fys)-1]
		}
		return from, to, label, nil
	}
	from, to = strings.TrimSpace(in.From), strings.TrimSpace(in.To)
	if !ledger.ValidDate(from) || !ledger.ValidDate(to) {
		return "", "", "", invalid("choose financial years, or a period with a first and last date (YYYY-MM-DD)")
	}
	if to < from {
		return "", "", "", invalid("the period ends before it starts")
	}
	return from, to, arcDay(from) + " to " + arcDay(to), nil
}

func arcKindsOf(ks []string) ([]string, error) {
	if len(ks) == 0 {
		return append([]string(nil), app.ArchiveKinds...), nil
	}
	seen := map[string]bool{}
	var out []string
	for _, k := range ks {
		if arcTitles[k] == "" {
			return nil, invalid("unknown kind %q", k)
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	// in display order
	sort.Slice(out, func(i, j int) bool { return arcRank(out[i]) < arcRank(out[j]) })
	return out, nil
}

func arcRank(k string) int {
	for i, x := range app.ArchiveKinds {
		if x == k {
			return i
		}
	}
	return 99
}

// arcCheck is what open items say about a period: reasons it cannot be
// archived at all, and clients whose rows of it stay live.
type arcCheck struct {
	Blockers []string
	Exclude  map[string]bool
	Kept     []string
	Held     map[string]bool // migrated documents on legal hold (archive keys)
}

// archiveChecks applies the rules: never the current financial year, never
// overlapping another archive, nothing needed by open items.
func (o *Ops) archiveChecks(ctx context.Context, tenant, from, to, self string) (arcCheck, error) {
	c := arcCheck{Exclude: map[string]bool{}, Held: map[string]bool{}}
	today := o.now().Format("2006-01-02")
	if fyStart := app.CurrentFYStart(o.now()); to >= fyStart {
		c.Blockers = append(c.Blockers, "The current financial year (from "+arcDay(fyStart)+") cannot be archived; the period must end by "+arcDay(addDays(fyStart, -1))+".")
	}
	if to >= today {
		c.Blockers = append(c.Blockers, "The period must be over.")
	}
	for _, p := range o.arcIndex(ctx, tenant).Periods {
		if p.ID != self && from <= p.To && to >= p.From {
			c.Blockers = append(c.Blockers, "It overlaps archive "+p.ID+" ("+p.Label+").")
		}
	}
	if rs, err := o.App.Periods.ListArchives(ctx, tenant); err == nil {
		for _, r := range rs {
			if r.ID == self || !(from <= r.To && to >= r.From) {
				continue
			}
			switch r.State {
			case app.ArcRequested, app.ArcApproved, app.ArcVerified, app.ArcFailed, app.ArcRestoreRequested, app.ArcRestoreApproved:
				c.Blockers = append(c.Blockers, "Archive "+r.ID+" of an overlapping period is "+strings.ToLower(r.State)+"; finish or reject it first.")
			}
		}
	}
	if x, err := o.openExit(ctx, tenant); err != nil {
		return c, err
	} else if x != nil {
		c.Blockers = append(c.Blockers, "Closing the account is in progress ("+x.ID+"); nothing is archived while it is.")
	}
	if bs, err := o.App.Breaks.List(ctx, tenant, "", app.BreakOpen); err == nil {
		n := 0
		for _, b := range bs {
			if b.TradeDate >= from && b.TradeDate <= to {
				n++
			}
		}
		if n > 0 {
			c.Blockers = append(c.Blockers, fmt.Sprintf("%d reconciliation breaks of the period are open; resolve them first.", n))
		}
	}
	if ps, err := docs.List[PayoutRequest](ctx, o.Docs, tenant, cPayout, ""); err == nil {
		n := 0
		for _, p := range ps {
			if (p.Status == "Requested" || p.Status == "Approved") && p.Date >= from && p.Date <= to {
				n++
			}
		}
		if n > 0 {
			c.Blockers = append(c.Blockers, fmt.Sprintf("%d payout requests of the period are still pending; pay, reject or cancel them first.", n))
		}
	}
	pending, pledged := 0, map[string]bool{}
	if ps, err := docs.List[PledgeRequest](ctx, o.Docs, tenant, cPledge, ""); err == nil {
		for _, p := range ps {
			d := p.Time
			if len(d) >= 10 {
				d = d[:10]
			}
			switch {
			case (p.Status == "Pending" || p.Status == "Awaiting client") && d >= from && d <= to:
				pending++
			case p.Status == "Accepted":
				pledged[strings.ToUpper(p.UCC)] = true
			}
		}
	}
	if pending > 0 {
		c.Blockers = append(c.Blockers, fmt.Sprintf("%d pledge requests of the period are still pending.", pending))
	}
	// Clients whose rows of the period stay live.
	keep := func(set map[string]bool, why string) {
		if len(set) == 0 {
			return
		}
		for u := range set {
			c.Exclude[u] = true
		}
		c.Kept = append(c.Kept, fmt.Sprintf("%d clients %s", len(set), why))
	}
	debit := map[string]bool{}
	if bal, err := o.App.Ledger.Balances(ctx, tenant, ""); err == nil {
		net := map[string]int64{}
		for a, v := range bal {
			if u, _, ok := ledger.ClientCode(a); ok {
				net[u] += int64(v)
			}
		}
		for u, v := range net {
			if v > 0 {
				debit[u] = true
			}
		}
	}
	keep(debit, "with an unpaid debit: their ledger, trades and records of the period stay live")
	keep(pledged, "with open pledges stay live")
	complaints := map[string]bool{}
	if ts, err := docs.List[Ticket](ctx, o.Docs, tenant, cTicket, ""); err == nil {
		for _, t := range ts {
			if t.Kind == KindComplaint && t.Status != "Resolved" && t.Status != "Closed" && t.UCC != "" {
				complaints[strings.ToUpper(t.UCC)] = true
			}
		}
	}
	keep(complaints, "with an open complaint stay live")
	return c, nil
}

// archiveSelection is what the store takes for a period.
func (o *Ops) archiveSelection(ctx context.Context, tenant, from, to string, kinds []string, c arcCheck) (app.ArchiveSelection, []string, error) {
	sel := app.ArchiveSelection{From: from, To: to, Kinds: map[string]bool{}, DocSources: arcDocSources, Protected: arcProtected, ExcludeUCC: c.Exclude,
		MigratedDocs: map[string]bool{}, MigratedRaw: map[string]bool{}}
	for _, k := range kinds {
		sel.Kinds[k] = true
	}
	var notes []string
	if !sel.Kinds[app.ArcMigrated] {
		return sel, notes, nil
	}
	// Migrated history: whole years that lie inside the period.
	hs, err := o.histMetas(ctx, tenant)
	if err != nil {
		return sel, nil, err
	}
	for _, h := range hs {
		if len(h.FY) < 7 || h.Archived != "" {
			continue
		}
		if _, s, e := fyOf(h.FY[:4] + "-06-01"); s < from || e > to {
			continue
		}
		for i := 0; i < h.Chunks; i++ {
			sel.MigratedDocs[cHistChunk+"|"+fmt.Sprintf("%s|%06d", h.ID, i)] = true
		}
		for b := 0; b < h.Buckets; b++ {
			sel.MigratedDocs[cHistShard+"|"+fmt.Sprintf("%s|%03d", h.ID, b)] = true
		}
	}
	// Migrated documents dated in the period (or of a year inside it).
	ds, err := docs.List[ArchiveDoc](ctx, o.Docs, tenant, cArc, "")
	if err != nil {
		return sel, nil, err
	}
	held := 0
	for _, d := range ds {
		if d.Removed || d.Deleted || d.Raw == "" || d.Archived != "" {
			continue
		}
		in := d.Date != "" && d.Date >= from && d.Date <= to
		if d.Date == "" && len(d.FY) >= 7 {
			_, s, e := fyOf(d.FY[:4] + "-06-01")
			in = s >= from && e <= to
		}
		if !in {
			continue
		}
		if d.LegalHold {
			held++
			continue
		}
		if c.Exclude[strings.ToUpper(d.UCC)] {
			continue
		}
		sel.MigratedRaw[d.Raw] = true
	}
	if held > 0 {
		notes = append(notes, fmt.Sprintf("%d migrated documents of the period are on legal hold and stay live", held))
	}
	return sel, notes, nil
}

// arcCostSaving is the estimated monthly saving for bytes leaving the live
// database: their live price, less their archive price once compressed
// (taken as a third of the size).
func arcCostSaving(s ArchiveSettings, bytes int64) float64 {
	gb := float64(bytes) / (1 << 30)
	v := gb*s.LiveCostPerGB - gb/3*s.ArchiveCostPerGB
	return math.Round(v*100) / 100
}

// retainUntil is the period's end plus the retention years.
func arcRetainUntil(to string, years int) string {
	t, err := time.Parse("2006-01-02", to)
	if err != nil {
		return ""
	}
	return t.AddDate(years, 0, 0).Format("2006-01-02")
}

func (o *Ops) archiveAvailable() error {
	if o.App.Periods == nil {
		return fmt.Errorf("%w: archiving is not available on this storage", ErrState)
	}
	if o.exportSealer() == nil {
		return fmt.Errorf("%w: archiving needs the server's data keys", ErrState)
	}
	return nil
}

// PreviewArchive says what archiving a period would do: per kind, the rows
// and bytes it would free, the estimated saving, and what stops it.
func (o *Ops) PreviewArchive(ctx context.Context, tenant, actor string, in ArchiveAsk) (ArchivePreview, error) {
	if err := o.require(ctx, tenant, actor, "Archiving a period", RoleCompliance); err != nil {
		return ArchivePreview{}, err
	}
	if err := o.archiveAvailable(); err != nil {
		return ArchivePreview{}, err
	}
	from, to, label, err := arcPeriodOf(in)
	if err != nil {
		return ArchivePreview{}, err
	}
	kinds, err := arcKindsOf(in.Kinds)
	if err != nil {
		return ArchivePreview{}, err
	}
	return o.previewArchive(ctx, tenant, from, to, label, kinds, "")
}

func (o *Ops) previewArchive(ctx context.Context, tenant, from, to, label string, kinds []string, self string) (ArchivePreview, error) {
	set := o.archiveSettings(ctx, tenant)
	c, err := o.archiveChecks(ctx, tenant, from, to, self)
	if err != nil {
		return ArchivePreview{}, err
	}
	sel, notes, err := o.archiveSelection(ctx, tenant, from, to, kinds, c)
	if err != nil {
		return ArchivePreview{}, err
	}
	p := ArchivePreview{From: from, To: to, Label: label, Blockers: append([]string{}, c.Blockers...), Kept: append(c.Kept, notes...), RetainUntil: arcRetainUntil(to, set.RetainYears)}
	stats := map[string]app.ArchiveStat{}
	err = o.App.Periods.PeriodRows(ctx, tenant, sel, func(r app.ArchiveRow) error {
		s := stats[r.Kind]
		if r.Ref.Table != "" {
			s.Rows++
		}
		s.Bytes += r.Bytes
		stats[r.Kind] = s
		return nil
	})
	if err != nil {
		return p, err
	}
	for _, k := range kinds {
		s := stats[k]
		p.Kinds = append(p.Kinds, ArchiveKindLine{Kind: k, Title: arcTitles[k], Rows: s.Rows, Bytes: s.Bytes})
		p.Rows += s.Rows
		p.Bytes += s.Bytes
	}
	if sel.Kinds[app.ArcDocHistory] && o.App.Snap != nil && o.App.Snap.Backend() == "memory" {
		p.Notes = append(p.Notes, "This server keeps no earlier versions of records, so there is no document history to archive.")
	}
	p.Saving = arcCostSaving(set, p.Bytes)
	if p.Rows == 0 && len(p.Blockers) == 0 {
		p.Blockers = append(p.Blockers, "Nothing of the period would be archived.")
	}
	return p, nil
}

func newArchiveID(now time.Time) string {
	b := make([]byte, 3)
	_, _ = crand.Read(b)
	return "AR-" + now.Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// RequestArchive asks to archive a period (Admin). A second person, the
// compliance officer or another Admin, approves it.
func (o *Ops) RequestArchive(ctx context.Context, tenant, actor string, in ArchiveAsk) (app.ArchiveRecord, error) {
	if err := o.require(ctx, tenant, actor, "Asking to archive a period", RoleAdmin); err != nil {
		return app.ArchiveRecord{}, err
	}
	if err := o.archiveAvailable(); err != nil {
		return app.ArchiveRecord{}, err
	}
	from, to, label, err := arcPeriodOf(in)
	if err != nil {
		return app.ArchiveRecord{}, err
	}
	kinds, err := arcKindsOf(in.Kinds)
	if err != nil {
		return app.ArchiveRecord{}, err
	}
	return o.requestArchive(ctx, tenant, actor, from, to, label, kinds, clip(strings.TrimSpace(in.Reason), 300), "")
}

func (o *Ops) requestArchive(ctx context.Context, tenant, actor, from, to, label string, kinds []string, reason, again string) (app.ArchiveRecord, error) {
	p, err := o.previewArchive(ctx, tenant, from, to, label, kinds, "")
	if err != nil {
		return app.ArchiveRecord{}, err
	}
	if len(p.Blockers) > 0 {
		return app.ArchiveRecord{}, fmt.Errorf("%w: %s cannot be archived: %s", ErrState, label, strings.Join(p.Blockers, " "))
	}
	now := o.now()
	r := app.ArchiveRecord{Tenant: tenant, ID: newArchiveID(now), State: app.ArcRequested, From: from, To: to, Label: label, Kinds: kinds, Reason: reason,
		RequestedBy: actor, RequestedAt: now.UTC(), Preview: map[string]app.ArchiveStat{}, KeptWhy: p.Kept, RetainUntil: p.RetainUntil, Again: again}
	for _, k := range p.Kinds {
		r.Preview[k.Kind] = app.ArchiveStat{Rows: k.Rows, Bytes: k.Bytes}
	}
	if err := o.App.Periods.SaveArchive(ctx, r); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "archive.requested", r.ID, map[string]any{"from": from, "to": to, "kinds": kinds, "rows": p.Rows, "bytes": p.Bytes, "reason": reason, "again": again})
	o.raiseWork(ctx, tenant, "archive", r.ID, "Approve archiving "+label,
		fmt.Sprintf("%s asked to archive %s (%s rows, about %s). A second person, the compliance officer or another Admin, approves it under Admin → Data & exit → Archive.", actor, label, groupIN(int64(p.Rows)), humanBytes(p.Bytes)),
		"", RoleCompliance, "dataexit", "")
	return r, nil
}

func (o *Ops) archiveRecord(ctx context.Context, tenant, id string) (app.ArchiveRecord, error) {
	if o.App.Periods == nil {
		return app.ArchiveRecord{}, fmt.Errorf("%w: archiving is not available on this storage", ErrState)
	}
	r, err := o.App.Periods.GetArchive(ctx, tenant, id)
	if errors.Is(err, app.ErrArchiveNotFound) || (err == nil && r.Tenant != tenant) {
		return r, fmt.Errorf("%w: no such archive", ErrNotFound)
	}
	return r, err
}

// DecideArchive approves (and starts) or rejects an archive. The person who
// asked cannot decide it. A failed archive is started again the same way.
func (o *Ops) DecideArchive(ctx context.Context, tenant, actor, id string, approve bool, note string) (app.ArchiveRecord, error) {
	if err := o.require(ctx, tenant, actor, "Approving an archive", RoleCompliance); err != nil {
		return app.ArchiveRecord{}, err
	}
	r, err := o.archiveRecord(ctx, tenant, id)
	if err != nil {
		return r, err
	}
	if r.State != app.ArcRequested && r.State != app.ArcFailed {
		return r, fmt.Errorf("%w: archive %s is %s", ErrState, id, strings.ToLower(r.State))
	}
	if r.RequestedBy == actor {
		return r, fmt.Errorf("%w: a second person approves an archive, not the one who asked for it", ErrForbidden)
	}
	if r.State == app.ArcFailed && r.ApprovedBy != "" && r.ApprovedBy != actor && approve {
		return r, fmt.Errorf("%w: %s approved this archive; they start it again", ErrForbidden, r.ApprovedBy)
	}
	note = clip(strings.TrimSpace(note), 300)
	if !approve {
		if note == "" {
			return r, invalid("give the reason for rejecting")
		}
		r.State, r.RejectedBy, r.Note = app.ArcRejected, actor, note
		if err := o.App.Periods.SaveArchive(ctx, r); err != nil {
			return r, err
		}
		o.log(ctx, tenant, actor, "archive.rejected", id, map[string]any{"note": note})
		o.closeWork(ctx, tenant, actor, "archive", id, "rejected")
		return r, nil
	}
	c, err := o.archiveChecks(ctx, tenant, r.From, r.To, r.ID)
	if err != nil {
		return r, err
	}
	if len(c.Blockers) > 0 {
		return r, fmt.Errorf("%w: %s cannot be archived now: %s", ErrState, r.Label, strings.Join(c.Blockers, " "))
	}
	role, _ := o.RoleOf(ctx, tenant, actor)
	retry := r.State == app.ArcFailed
	r.State, r.ApprovedBy, r.ApprovedRole, r.ApprovedAt, r.Note, r.Error = app.ArcApproved, actor, role, o.now().UTC(), firstOf(note, r.Note), ""
	if err := o.App.Periods.SaveArchive(ctx, r); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, map[bool]string{true: "archive.retried", false: "archive.approved"}[retry], id, map[string]any{"requested_by": r.RequestedBy, "from": r.From, "to": r.To})
	o.closeWork(ctx, tenant, actor, "archive", id, "approved")
	o.startArchiveJob(ctx, tenant, "archive:"+id, func(ctx context.Context) { o.runArchive(ctx, tenant, id) })
	return o.archiveRecord(ctx, tenant, id)
}

func (o *Ops) startArchiveJob(ctx context.Context, tenant, key string, fn func(context.Context)) {
	o.arcMu.Lock()
	if o.arcRunning == nil {
		o.arcRunning = map[string]bool{}
	}
	if o.arcRunning[tenant+"|"+key] {
		o.arcMu.Unlock()
		return
	}
	o.arcRunning[tenant+"|"+key] = true
	o.arcMu.Unlock()
	run := func(ctx context.Context) {
		defer func() { o.arcMu.Lock(); delete(o.arcRunning, tenant+"|"+key); o.arcMu.Unlock() }()
		fn(ctx)
	}
	if o.ArchiveSync {
		run(ctx)
		return
	}
	go run(context.WithoutCancel(ctx))
}

func (o *Ops) archiveRunning(tenant, key string) bool {
	o.arcMu.Lock()
	defer o.arcMu.Unlock()
	return o.arcRunning[tenant+"|"+key]
}

// runArchive writes, verifies and only then removes an approved period.
func (o *Ops) runArchive(ctx context.Context, tenant, id string) {
	r, err := o.archiveRecord(ctx, tenant, id)
	if err != nil || r.State != app.ArcApproved {
		return
	}
	fail := func(err error) {
		cur, gerr := o.archiveRecord(ctx, tenant, id)
		if gerr == nil {
			r = cur
		}
		r.State, r.Error = app.ArcFailed, err.Error()
		_ = o.App.Periods.SaveArchive(ctx, r)
		_ = o.arcIndexPut(ctx, tenant, arcPeriod{ID: id})
		o.log(ctx, tenant, SystemActor, "archive.failed", id, map[string]any{"error": err.Error()})
		o.raiseWork(ctx, tenant, "archive", id+":failed:"+o.now().Format("20060102150405"), "Archiving "+r.Label+" failed",
			"Archive "+id+" could not be finished: "+err.Error()+". Nothing was removed from the live data. Put the cause right and start it again under Admin → Data & exit → Archive.", "", RoleAdmin, "dataexit", "")
	}
	defer func() {
		if p := recover(); p != nil {
			fail(fmt.Errorf("archiving stopped unexpectedly: %v", p))
		}
	}()
	c, err := o.archiveChecks(ctx, tenant, r.From, r.To, r.ID)
	if err == nil && len(c.Blockers) > 0 {
		err = errors.New(strings.Join(c.Blockers, " "))
	}
	if err != nil {
		fail(err)
		return
	}
	sel, notes, err := o.archiveSelection(ctx, tenant, r.From, r.To, r.Kinds, c)
	if err != nil {
		fail(err)
		return
	}
	r.KeptWhy = append(c.Kept, notes...)
	// From here the period takes no new entries.
	if err := o.arcIndexPut(ctx, tenant, arcPeriod{ID: id, From: r.From, To: r.To, Label: r.Label, State: "Archiving", Kinds: r.Kinds}); err != nil {
		fail(err)
		return
	}
	b, err := o.writeArchivePackage(ctx, tenant, &r, sel)
	if err != nil {
		fail(fmt.Errorf("writing the package: %w", err))
		return
	}
	if err := o.verifyArchivePackage(ctx, tenant, r, b.refs); err != nil {
		fail(fmt.Errorf("the package did not verify, so nothing was removed: %w", err))
		return
	}
	r.State, r.VerifiedAt = app.ArcVerified, o.now().UTC()
	if err := o.App.Periods.SaveArchive(ctx, r); err != nil {
		fail(err)
		return
	}
	o.log(ctx, tenant, SystemActor, "archive.verified", id, map[string]any{"package_sha256": r.PackageSHA256, "bytes": r.PackageBytes, "rows": r.Rows, "signed_with": r.SignedWith})
	carry := app.CarryEntries(tenant, id, r.From, r.To, firstOf(r.ApprovedBy, SystemActor), b.net)
	now := o.now()
	out, err := o.App.Periods.RemovePeriod(ctx, tenant, id, b.refs, carry, func(removed map[string]int, stored []ledger.Entry) (app.ArchiveRecord, error) {
		done := r
		done.State, done.ArchivedAt, done.CarryEntries = app.ArcArchived, now.UTC(), nil
		for _, e := range stored {
			done.CarryEntries = append(done.CarryEntries, e.ID)
		}
		return done, nil
	})
	if err != nil {
		fail(err)
		return
	}
	r = out
	// The live index: the period, its clients, its files, and the migrated
	// history and documents it holds.
	_ = o.arcIndexPut(ctx, tenant, arcPeriod{ID: id, From: r.From, To: r.To, Label: r.Label, State: app.ArcArchived, Kinds: r.Kinds, ArchivedAt: r.ArchivedAt, Clients: r.Clients})
	for fid, name := range b.files {
		_ = docs.Upsert(ctx, o.Docs, tenant, cArcFile, fid, arcFileRef{Archive: id, ArchivedAt: r.ArchivedAt, Name: name}, SystemActor)
	}
	o.markMigratedArchived(ctx, tenant, sel, id)
	o.log(ctx, tenant, SystemActor, "archive.archived", id, map[string]any{"from": r.From, "to": r.To, "rows": r.Rows, "freed": r.Freed, "carry_entries": r.CarryEntries,
		"package_sha256": r.PackageSHA256, "approved_by": r.ApprovedBy, "requested_by": r.RequestedBy, "retain_until": r.RetainUntil})
}

// markMigratedArchived marks the migrated history years and documents an
// archive took.
func (o *Ops) markMigratedArchived(ctx context.Context, tenant string, sel app.ArchiveSelection, id string) {
	if !sel.Kinds[app.ArcMigrated] {
		return
	}
	hs, _ := docs.List[HistMeta](ctx, o.Docs, tenant, cHist, "")
	for _, h := range hs {
		if sel.MigratedDocs[cHistChunk+"|"+fmt.Sprintf("%s|%06d", h.ID, 0)] || sel.MigratedDocs[cHistShard+"|"+fmt.Sprintf("%s|%03d", h.ID, 0)] {
			h.Archived = id
			_ = docs.Upsert(ctx, o.Docs, tenant, cHist, h.ID, h, SystemActor)
		}
	}
	if len(sel.MigratedRaw) == 0 {
		return
	}
	ds, _ := docs.List[ArchiveDoc](ctx, o.Docs, tenant, cArc, "")
	for _, d := range ds {
		if sel.MigratedRaw[d.Raw] {
			d.Archived = id
			_ = docs.Upsert(ctx, o.Docs, tenant, cArc, d.Key, d, SystemActor)
		}
	}
}

// ---- restoring ----

// RequestRestore asks to bring an archived period back into the live data
// (Admin). A second person approves it.
func (o *Ops) RequestRestore(ctx context.Context, tenant, actor, id, reason string) (app.ArchiveRecord, error) {
	if err := o.require(ctx, tenant, actor, "Asking to restore an archive", RoleAdmin); err != nil {
		return app.ArchiveRecord{}, err
	}
	reason = clip(strings.TrimSpace(reason), 300)
	if reason == "" {
		return app.ArchiveRecord{}, invalid("say why the archive is to be restored")
	}
	r, err := o.archiveRecord(ctx, tenant, id)
	if err != nil {
		return r, err
	}
	if r.State != app.ArcArchived {
		return r, fmt.Errorf("%w: archive %s is %s; only an archived period can be restored", ErrState, id, strings.ToLower(r.State))
	}
	if x, err := o.openExit(ctx, tenant); err == nil && x != nil {
		return r, fmt.Errorf("%w: closing the account is in progress; nothing is restored while it is", ErrState)
	}
	r.State, r.RestoreRequestedBy, r.RestoreRequestedAt, r.RestoreReason, r.Error = app.ArcRestoreRequested, actor, o.now().UTC(), reason, ""
	r.RestoreApprovedBy, r.RestoreApprovedAt = "", time.Time{}
	if err := o.App.Periods.SaveArchive(ctx, r); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "archive.restore.requested", id, map[string]any{"reason": reason})
	o.raiseWork(ctx, tenant, "archive", id+":restore:"+o.now().Format("20060102150405"), "Approve restoring "+r.Label,
		actor+" asked to restore archive "+id+" ("+r.Label+"): "+reason+". A second person approves it under Admin → Data & exit → Archive.", "", RoleCompliance, "dataexit", "")
	return r, nil
}

// DecideRestore approves (and runs) or rejects a restore. The person who
// asked cannot decide it.
func (o *Ops) DecideRestore(ctx context.Context, tenant, actor, id string, approve bool, note string) (app.ArchiveRecord, error) {
	if err := o.require(ctx, tenant, actor, "Approving a restore", RoleCompliance); err != nil {
		return app.ArchiveRecord{}, err
	}
	r, err := o.archiveRecord(ctx, tenant, id)
	if err != nil {
		return r, err
	}
	if r.State != app.ArcRestoreRequested {
		return r, fmt.Errorf("%w: no restore of archive %s is waiting", ErrState, id)
	}
	if r.RestoreRequestedBy == actor {
		return r, fmt.Errorf("%w: a second person approves a restore, not the one who asked for it", ErrForbidden)
	}
	note = clip(strings.TrimSpace(note), 300)
	if !approve {
		if note == "" {
			return r, invalid("give the reason for rejecting")
		}
		r.State, r.Note = app.ArcArchived, note
		if err := o.App.Periods.SaveArchive(ctx, r); err != nil {
			return r, err
		}
		o.log(ctx, tenant, actor, "archive.restore.rejected", id, map[string]any{"note": note})
		return r, nil
	}
	r.State, r.RestoreApprovedBy, r.RestoreApprovedAt = app.ArcRestoreApproved, actor, o.now().UTC()
	if err := o.App.Periods.SaveArchive(ctx, r); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "archive.restore.approved", id, map[string]any{"requested_by": r.RestoreRequestedBy})
	o.startArchiveJob(ctx, tenant, "restore:"+id, func(ctx context.Context) { o.runRestore(ctx, tenant, id) })
	return o.archiveRecord(ctx, tenant, id)
}

// runRestore verifies the package again and puts the period back.
func (o *Ops) runRestore(ctx context.Context, tenant, id string) {
	r, err := o.archiveRecord(ctx, tenant, id)
	if err != nil || r.State != app.ArcRestoreApproved {
		return
	}
	fail := func(err error) {
		cur, gerr := o.archiveRecord(ctx, tenant, id)
		if gerr == nil {
			r = cur
		}
		r.State, r.Error = app.ArcArchived, "restore: "+err.Error()
		_ = o.App.Periods.SaveArchive(ctx, r)
		o.log(ctx, tenant, SystemActor, "archive.restore.failed", id, map[string]any{"error": err.Error()})
		o.raiseWork(ctx, tenant, "archive", id+":restorefail:"+o.now().Format("20060102150405"), "Restoring "+r.Label+" failed",
			"Archive "+id+" could not be restored: "+err.Error()+". The period stays archived; nothing changed in the live data.", "", RoleAdmin, "dataexit", "")
	}
	defer func() {
		if p := recover(); p != nil {
			fail(fmt.Errorf("the restore stopped unexpectedly: %v", p))
		}
	}()
	pk, err := o.openArchivePackage(ctx, tenant, r)
	if err != nil {
		fail(err)
		return
	}
	defer pk.close()
	now := o.now()
	out, err := o.App.Periods.RestorePeriod(ctx, tenant, id, pk.next, func(n map[string]int) (app.ArchiveRecord, error) {
		done := r
		done.State, done.RestoredAt, done.Restored = app.ArcRestored, now.UTC(), n
		return done, nil
	})
	if err != nil {
		fail(err)
		return
	}
	r = out
	_ = o.arcIndexPut(ctx, tenant, arcPeriod{ID: id})
	for _, fid := range pk.files {
		if ref, ok := o.archivedFile(ctx, tenant, fid); ok && ref.Archive == id {
			ref.Archive = ""
			_ = docs.Upsert(ctx, o.Docs, tenant, cArcFile, fid, ref, SystemActor)
		}
	}
	o.unmarkMigrated(ctx, tenant, id)
	o.log(ctx, tenant, SystemActor, "archive.restored", id, map[string]any{"rows": r.Restored, "approved_by": r.RestoreApprovedBy, "requested_by": r.RestoreRequestedBy, "package_sha256": r.PackageSHA256})
}

func (o *Ops) unmarkMigrated(ctx context.Context, tenant, id string) {
	hs, _ := docs.List[HistMeta](ctx, o.Docs, tenant, cHist, "")
	for _, h := range hs {
		if h.Archived == id {
			h.Archived = ""
			_ = docs.Upsert(ctx, o.Docs, tenant, cHist, h.ID, h, SystemActor)
		}
	}
	ds, _ := docs.List[ArchiveDoc](ctx, o.Docs, tenant, cArc, "")
	for _, d := range ds {
		if d.Archived == id {
			d.Archived = ""
			_ = docs.Upsert(ctx, o.Docs, tenant, cArc, d.Key, d, SystemActor)
		}
	}
}

// ArchiveAgain asks to archive a restored period again, as it was (Admin;
// a second person approves as for any archive).
func (o *Ops) ArchiveAgain(ctx context.Context, tenant, actor, id string) (app.ArchiveRecord, error) {
	if err := o.require(ctx, tenant, actor, "Asking to archive a period", RoleAdmin); err != nil {
		return app.ArchiveRecord{}, err
	}
	r, err := o.archiveRecord(ctx, tenant, id)
	if err != nil {
		return r, err
	}
	if r.State != app.ArcRestored {
		return r, fmt.Errorf("%w: archive %s is %s; only a restored period is archived again", ErrState, id, strings.ToLower(r.State))
	}
	return o.requestArchive(ctx, tenant, actor, r.From, r.To, r.Label, r.Kinds, "archived again after restore "+id, id)
}

// ---- the screen ----

// ArchiveView is Admin → Data & exit → Archive.
type ArchiveView struct {
	Available bool                `json:"available"`
	Problem   string              `json:"problem,omitempty"`
	Storage   string              `json:"storage"`
	StorageOK bool                `json:"storage_ok"`
	Settings  ArchiveSettings     `json:"settings"`
	Archives  []app.ArchiveRecord `json:"archives"`
	Index     []arcPeriod         `json:"index"`
	Years     []string            `json:"years"` // financial years that may be archived, newest first
	CurrentFY string              `json:"current_fy"`
	Kinds     []ArchiveKindLine   `json:"kinds"`
	RetainMin int                 `json:"retain_min"`
	Running   []string            `json:"running,omitempty"`
}

// ArchiveScreen lists the archives and the archive rules (Admin and the
// compliance officer).
func (o *Ops) ArchiveScreen(ctx context.Context, tenant, actor string) (ArchiveView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing archives", RoleCompliance); err != nil {
		return ArchiveView{}, err
	}
	v := ArchiveView{Available: true, Settings: o.archiveSettings(ctx, tenant), Archives: []app.ArchiveRecord{}, Index: o.arcIndex(ctx, tenant).Periods, RetainMin: ArchiveRetainDefault}
	if v.Index == nil {
		v.Index = []arcPeriod{}
	}
	if err := o.archiveAvailable(); err != nil {
		v.Available, v.Problem = false, strings.TrimPrefix(err.Error(), "invalid state: ")
	}
	v.Storage, v.StorageOK = o.archiveStorage()
	cur, _, _ := fyOf(o.now().Format("2006-01-02"))
	v.CurrentFY = cur
	var y int
	fmt.Sscanf(cur, "%d-", &y)
	for i := 1; i <= 12; i++ {
		v.Years = append(v.Years, fmt.Sprintf("%d-%02d", y-i, (y-i+1)%100))
	}
	for _, k := range app.ArchiveKinds {
		v.Kinds = append(v.Kinds, ArchiveKindLine{Kind: k, Title: arcTitles[k]})
	}
	if o.App.Periods != nil {
		rs, err := o.App.Periods.ListArchives(ctx, tenant)
		if err != nil {
			return v, err
		}
		sort.Slice(rs, func(i, j int) bool { return rs[i].ID > rs[j].ID })
		v.Archives = rs
		for _, r := range rs {
			if o.archiveRunning(tenant, "archive:"+r.ID) || o.archiveRunning(tenant, "restore:"+r.ID) {
				v.Running = append(v.Running, r.ID)
			}
		}
	}
	return v, nil
}
