package ops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/migrate/pkg"
)

// ---- migration: propose, approve, load, undo ----
//
// A maker (Admin) proposes loading one firm of a checked package: which
// files, and the date the balances are as of. A different user (Admin or
// Ops checker) approves it with the passphrase, and the load runs: go-live
// files in dependency order through LoadFile (the same strict import as
// the Files page), then the history years, then the documents. Each file
// done is recorded, so a load cut short by a restart carries on from where
// it stopped when someone resumes it with the passphrase. A package (all
// parts of a split package) is loaded into a firm once. Undo, approved by a
// second person, reverses the opening entries and takes the history and
// the archived documents out again, as long as nothing else has been
// posted since.

const (
	cMig     = "migration"
	cMigDone = "migdone"
)

// Migration statuses.
const (
	MigProposed     = "Proposed"
	MigRejected     = "Rejected"
	MigLoading      = "Loading"
	MigInterrupted  = "Interrupted"
	MigFailed       = "Failed"
	MigLoaded       = "Loaded"
	MigUndoProposed = "Undo proposed"
	MigUndoing      = "Undoing"
	MigUndone       = "Undone"
)

// Step kinds.
const (
	StepMaster    = "master"
	StepHistory   = "history"
	StepDocuments = "documents"
)

// Migration is one proposed (and perhaps loaded) migration of a firm.
type Migration struct {
	ID           string    `json:"id"`
	Key          string    `json:"key"`
	Packages     []string  `json:"packages"` // part 1 first
	System       string    `json:"system"`
	Firm         string    `json:"firm"`
	FirmName     string    `json:"firm_name"`
	AsOf         string    `json:"as_of"`
	Files        []string  `json:"files"`
	NotLive      bool      `json:"not_live,omitempty"` // "this firm is not live yet" was confirmed
	Status       string    `json:"status"`
	ProposedBy   string    `json:"proposed_by"`
	ProposedAt   time.Time `json:"proposed_at"`
	Note         string    `json:"note,omitempty"`
	DecidedBy    string    `json:"decided_by,omitempty"`
	DecidedAt    time.Time `json:"decided_at,omitzero"`
	DecisionNote string    `json:"decision_note,omitempty"`
	StartedAt    time.Time `json:"started_at,omitzero"`
	FinishedAt   time.Time `json:"finished_at,omitzero"`
	Error        string    `json:"error,omitempty"`
	BaseSeq      int64     `json:"base_seq"`          // the last ledger entry before the load
	Entries      []string  `json:"entries,omitempty"` // ledger entries the load posted
	Steps        []MigStep `json:"steps"`
	Docs         int       `json:"docs"`
	Recon        *MigRecon `json:"recon,omitempty"`
	Undo         *MigUndo  `json:"undo,omitempty"`
}

// MigStep is one file (or one part's documents) of a load.
type MigStep struct {
	Kind    string         `json:"kind"`
	Package string         `json:"package"`
	Path    string         `json:"path"`
	Source  string         `json:"source,omitempty"`
	FY      string         `json:"fy,omitempty"`
	FYs     []string       `json:"fys,omitempty"` // documents: the years ticked
	Date    string         `json:"date,omitempty"`
	Rows    int            `json:"rows"`
	Status  string         `json:"status"` // Pending, Done, Failed
	Message string         `json:"message,omitempty"`
	RawID   string         `json:"raw_id,omitempty"`
	Resume  int            `json:"resume,omitempty"` // documents: index rows done
	Seq     map[string]int `json:"seq,omitempty"`    // documents: index chunks written per year
	Counts  map[string]int `json:"counts,omitempty"` // documents: archived per year
	Skipped int            `json:"skipped,omitempty"`
}

// MigUndo is a request to undo a loaded migration.
type MigUndo struct {
	Status     string    `json:"status"` // Proposed, Approved, Rejected, Done
	ProposedBy string    `json:"proposed_by"`
	ProposedAt time.Time `json:"proposed_at"`
	Reason     string    `json:"reason"`
	DecidedBy  string    `json:"decided_by,omitempty"`
	DecidedAt  time.Time `json:"decided_at,omitzero"`
	Note       string    `json:"note,omitempty"`
	Reversed   int       `json:"reversed"`
}

type migDone struct {
	Migration string `json:"migration"`
	Undone    bool   `json:"undone,omitempty"`
}

// MigProposal is what a maker asks to load.
type MigProposal struct {
	Package string   `json:"package"` // any part of the package
	Firm    string   `json:"firm"`
	Files   []string `json:"files"` // paths; empty: every file of the firm
	AsOf    string   `json:"as_of"`
	NotLive bool     `json:"not_live"`
	Note    string   `json:"note"`
}

func stepRank(k string) int {
	return map[string]int{StepMaster: 0, StepHistory: 1, StepDocuments: 2}[k]
}

// migParts returns every part of the package's migration, part 1 first,
// or says which are missing or not checked.
func (o *Ops) migParts(ctx context.Context, tenant, id string) ([]MigPackage, error) {
	p, _, err := docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id)
	if err != nil {
		return nil, fmt.Errorf("%w: no such package", ErrNotFound)
	}
	if p.Manifest == nil || p.Key == "" {
		return nil, fmt.Errorf("%w: check the package first", ErrState)
	}
	if p.Manifest.Part == nil || p.Manifest.Part.Of <= 1 {
		return []MigPackage{p}, nil
	}
	all, err := docs.List[MigPackage](ctx, o.Docs, tenant, cMigPkg, "")
	if err != nil {
		return nil, err
	}
	byIdx := map[int]MigPackage{}
	for _, q := range all {
		if q.Key == p.Key && q.Status != MigDiscarded && q.Manifest != nil && q.Manifest.Part != nil {
			if cur, ok := byIdx[q.Manifest.Part.Index]; !ok || (cur.Status != MigChecked && q.Status == MigChecked) {
				byIdx[q.Manifest.Part.Index] = q
			}
		}
	}
	var out []MigPackage
	var missing []string
	for i := 1; i <= p.Manifest.Part.Of; i++ {
		q, ok := byIdx[i]
		if !ok {
			missing = append(missing, fmt.Sprint(i))
			continue
		}
		out = append(out, q)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: part %s of %d is not uploaded and checked yet", ErrState, strings.Join(missing, ", "), p.Manifest.Part.Of)
	}
	return out, nil
}

// migOwnEntries is every ledger entry posted by a migration of the firm
// (and their reversals by an undo).
func (o *Ops) migOwnEntries(ctx context.Context, tenant string) (map[string]bool, error) {
	ms, err := docs.List[Migration](ctx, o.Docs, tenant, cMig, "")
	if err != nil {
		return nil, err
	}
	own := map[string]bool{}
	for _, m := range ms {
		for _, e := range m.Entries {
			own[e] = true
		}
	}
	return own, nil
}

// migForeign counts ledger entries after seq that no migration posted.
func (o *Ops) migForeign(ctx context.Context, tenant string, after int64) (int, error) {
	own, err := o.migOwnEntries(ctx, tenant)
	if err != nil {
		return 0, err
	}
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range es {
		if e.Seq > after && !own[e.ID] && !own[e.ReversalOf] {
			n++
		}
	}
	return n, nil
}

func (o *Ops) lastSeq(ctx context.Context, tenant string) (int64, error) {
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{})
	var s int64
	for _, e := range es {
		s = max(s, e.Seq)
	}
	return s, err
}

func (o *Ops) migGuardLive(ctx context.Context, tenant string, notLive bool) error {
	n, err := o.migForeign(ctx, tenant, 0)
	if err != nil {
		return err
	}
	if n > 0 && !notLive {
		return fmt.Errorf("%w: this firm already has %s ledger entries that did not come from a migration, so it looks live. Load only into a firm that is not live yet; if it is not (test entries, a trial run), tick \"This firm is not live yet\"", ErrState, groupIN(int64(n)))
	}
	return nil
}

func (o *Ops) migAlreadyLoaded(ctx context.Context, tenant, key string) error {
	d, _, err := docs.Get[migDone](ctx, o.Docs, tenant, cMigDone, key)
	if err == nil && !d.Undone {
		return fmt.Errorf("%w: this package was already loaded into this firm (migration %s); a package is loaded once", ErrState, d.Migration)
	}
	return nil
}

// ProposeMigration records a maker's proposal to load one firm of a
// checked package.
func (o *Ops) ProposeMigration(ctx context.Context, tenant, actor string, in MigProposal) (Migration, error) {
	if err := o.require(ctx, tenant, actor, "Proposing a migration load", RoleAdmin); err != nil {
		return Migration{}, err
	}
	parts, err := o.migParts(ctx, tenant, in.Package)
	if err != nil {
		return Migration{}, err
	}
	for _, p := range parts {
		if p.Status != MigChecked {
			return Migration{}, fmt.Errorf("%w: %s is %s; it must be checked without damaged files first", ErrState, p.Name, strings.ToLower(p.Status))
		}
	}
	key, man := parts[0].Key, parts[0].Manifest
	if err := o.migAlreadyLoaded(ctx, tenant, key); err != nil {
		return Migration{}, err
	}
	ms, _ := docs.List[Migration](ctx, o.Docs, tenant, cMig, "")
	for _, m := range ms {
		switch m.Status {
		case MigProposed, MigLoading, MigInterrupted, MigFailed, MigUndoProposed, MigUndoing:
			return Migration{}, fmt.Errorf("%w: migration %s is %s; finish, reject or undo it first", ErrState, m.ID, strings.ToLower(m.Status))
		}
	}
	firm := strings.TrimSpace(in.Firm)
	var f pkg.Firm
	for _, x := range man.Firms {
		if x.Code == firm {
			f = x
		}
	}
	if f.Code == "" {
		return Migration{}, invalid("choose one of the package's firms")
	}
	asOf := firstOf(strings.TrimSpace(in.AsOf), f.AsOf)
	if !ledger.ValidDate(asOf) {
		return Migration{}, invalid("give the date the balances are as of (YYYY-MM-DD)")
	}
	// Every file of the firm in every part, and the ones with errors.
	avail := map[string]pkg.File{}
	bad := map[string]int{}
	for _, p := range parts {
		for _, x := range p.Manifest.Files {
			if x.Firm == firm && (x.Kind == pkg.KindMaster || x.Kind == pkg.KindHistory || x.Kind == pkg.KindDocuments) {
				avail[x.Path] = x
			}
		}
		if p.Check != nil && p.Check.Firm == firm {
			o.eachMigError(ctx, tenant, p, func(e MigError) {
				if e.Severity != "Warning" {
					bad[e.File]++
				}
			})
		}
		hasMaster := false
		for _, x := range p.Manifest.Files {
			hasMaster = hasMaster || (x.Firm == firm && x.Kind == pkg.KindMaster)
		}
		if hasMaster && (p.Check == nil || p.Check.Firm != firm) {
			return Migration{}, fmt.Errorf("%w: %s was not checked for firm %s; check it again for that firm before proposing", ErrState, p.Name, firm)
		}
	}
	files := in.Files
	if len(files) == 0 {
		for p := range avail {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	var problems []string
	for _, p := range files {
		if _, ok := avail[p]; !ok {
			return Migration{}, invalid("%s is not a file of firm %s in this package", p, firm)
		}
		if bad[p] > 0 && avail[p].Kind == pkg.KindMaster {
			problems = append(problems, fmt.Sprintf("%s (%d errors)", p, bad[p]))
		}
	}
	if len(problems) > 0 {
		return Migration{}, invalid("the dry run found errors in %s; correct them and make the package again, or untick those files", strings.Join(problems, ", "))
	}
	if err := o.migGuardLive(ctx, tenant, in.NotLive); err != nil {
		return Migration{}, err
	}
	var rb [2]byte
	rand.Read(rb[:])
	m := Migration{ID: "M-" + o.now().Format("20060102-150405") + "-" + hex.EncodeToString(rb[:]), Key: key, System: man.Source.System, Firm: firm, FirmName: f.Name, AsOf: asOf, Files: files,
		NotLive: in.NotLive, Status: MigProposed, ProposedBy: actor, ProposedAt: o.now(), Note: strings.TrimSpace(in.Note), Steps: []MigStep{}}
	for _, p := range parts {
		m.Packages = append(m.Packages, p.ID)
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cMig, m.ID, m, 0, actor); err != nil {
		return m, err
	}
	o.log(ctx, tenant, actor, "migration.proposed", m.ID, map[string]any{"key": key, "firm": firm, "as_of": asOf, "files": len(files), "not_live": in.NotLive})
	o.raiseWork(ctx, tenant, "migration", m.ID, "Approve the migration of "+firstOf(f.Name, firm), fmt.Sprintf("%s proposed loading %d files from %s as of %s. A second person approves it on Files → Migration.", actor, len(files), m.System, asOf), "", RoleChecker, "migrate", asOf)
	return m, nil
}

// migSteps lays out the load: go-live files in dependency order, then the
// history years, then each part's documents.
func (o *Ops) migSteps(ctx context.Context, tenant string, m Migration, parts []MigPackage) []MigStep {
	ticked := map[string]bool{}
	for _, f := range m.Files {
		ticked[f] = true
	}
	var steps []MigStep
	order := map[string]int{}
	for i, p := range parts {
		order[p.ID] = i
		var masters []pkg.File
		docFY := []string{}
		for _, f := range p.Manifest.Files {
			if f.Firm != m.Firm || !ticked[f.Path] {
				continue
			}
			switch f.Kind {
			case pkg.KindMaster:
				masters = append(masters, f)
			case pkg.KindHistory:
				steps = append(steps, MigStep{Kind: StepHistory, Package: p.ID, Path: f.Path, Source: f.Source, FY: f.FY, Rows: 0, Status: "Pending"})
			case pkg.KindDocuments:
				docFY = append(docFY, f.FY)
			}
		}
		sortMasters(masters)
		for _, f := range masters {
			steps = append(steps, MigStep{Kind: StepMaster, Package: p.ID, Path: f.Path, Source: f.Source, Date: o.migFileDate(ctx, tenant, f.Source, m.AsOf), Status: "Pending"})
		}
		if len(docFY) > 0 {
			sort.Strings(docFY)
			steps = append(steps, MigStep{Kind: StepDocuments, Package: p.ID, Path: "firms/" + m.Firm + "/documents/index.csv", FYs: docFY, Status: "Pending", Seq: map[string]int{}, Counts: map[string]int{}})
		}
	}
	sort.SliceStable(steps, func(i, j int) bool {
		a, b := steps[i], steps[j]
		if stepRank(a.Kind) != stepRank(b.Kind) {
			return stepRank(a.Kind) < stepRank(b.Kind)
		}
		if a.Kind == StepMaster && migRank(a.Source) != migRank(b.Source) {
			return migRank(a.Source) < migRank(b.Source)
		}
		if a.Kind == StepHistory && a.FY != b.FY {
			return a.FY < b.FY
		}
		return order[a.Package] < order[b.Package]
	})
	return steps
}

// DecideMigration approves (and starts) or rejects a proposed load. The
// person who proposed it cannot decide it. Approving needs the package's
// passphrase, which opens it for the load and is then forgotten.
func (o *Ops) DecideMigration(ctx context.Context, tenant, actor, id string, approve bool, passphrase, note string) (Migration, error) {
	if err := o.require(ctx, tenant, actor, "Approving a migration load", RoleChecker); err != nil {
		return Migration{}, err
	}
	m, _, err := docs.Get[Migration](ctx, o.Docs, tenant, cMig, id)
	if err != nil {
		return m, fmt.Errorf("%w: no such migration", ErrNotFound)
	}
	if m.Status != MigProposed {
		return m, fmt.Errorf("%w: migration %s is %s, not proposed", ErrState, id, strings.ToLower(m.Status))
	}
	if m.ProposedBy == actor {
		return m, fmt.Errorf("%w: the person who proposed a migration cannot approve or reject it", ErrForbidden)
	}
	if !approve {
		if strings.TrimSpace(note) == "" {
			return m, invalid("give a reason for rejecting")
		}
		m.Status, m.DecidedBy, m.DecidedAt, m.DecisionNote = MigRejected, actor, o.now(), strings.TrimSpace(note)
		if err := docs.Upsert(ctx, o.Docs, tenant, cMig, id, m, actor); err != nil {
			return m, err
		}
		o.log(ctx, tenant, actor, "migration.rejected", id, map[string]any{"note": m.DecisionNote})
		o.closeWork(ctx, tenant, actor, "migration", id, "rejected")
		return m, nil
	}
	if err := o.migAlreadyLoaded(ctx, tenant, m.Key); err != nil {
		return m, err
	}
	if err := o.migGuardLive(ctx, tenant, m.NotLive); err != nil {
		return m, err
	}
	parts, err := o.migParts(ctx, tenant, m.Packages[0])
	if err != nil {
		return m, err
	}
	for _, p := range parts {
		if err := o.checkPassphrase(tenant, p.ID, passphrase); err != nil {
			return m, err
		}
	}
	m.Packages = nil
	for _, p := range parts {
		m.Packages = append(m.Packages, p.ID)
	}
	m.Steps = o.migSteps(ctx, tenant, m, parts)
	m.Status, m.DecidedBy, m.DecidedAt, m.DecisionNote, m.StartedAt = MigLoading, actor, o.now(), strings.TrimSpace(note), o.now()
	if m.BaseSeq, err = o.lastSeq(ctx, tenant); err != nil {
		return m, err
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cMigDone, m.Key, migDone{Migration: id}, actor); err != nil {
		return m, err
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cMig, id, m, actor); err != nil {
		return m, err
	}
	o.log(ctx, tenant, actor, "migration.approved", id, map[string]any{"steps": len(m.Steps), "proposed_by": m.ProposedBy})
	o.closeWork(ctx, tenant, actor, "migration", id, "approved")
	o.startLoad(ctx, tenant, actor, id, passphrase)
	m, _, err = docs.Get[Migration](ctx, o.Docs, tenant, cMig, id)
	return m, err
}

func (o *Ops) startLoad(ctx context.Context, tenant, actor, id, passphrase string) {
	bg := context.WithoutCancel(ctx)
	o.migGo("load:"+tenant+":"+id, func() { o.runLoad(bg, tenant, actor, id, passphrase) })
}

// ResumeMigration carries on with a load (or an undo) that stopped: the
// server restarted, or a file failed and was put right.
func (o *Ops) ResumeMigration(ctx context.Context, tenant, actor, id, passphrase string) (Migration, error) {
	if err := o.require(ctx, tenant, actor, "Approving a migration load", RoleChecker); err != nil {
		return Migration{}, err
	}
	m, _, err := docs.Get[Migration](ctx, o.Docs, tenant, cMig, id)
	if err != nil {
		return m, fmt.Errorf("%w: no such migration", ErrNotFound)
	}
	if o.migRunning("load:"+tenant+":"+id) || o.migRunning("undo:"+tenant+":"+id) {
		return m, fmt.Errorf("%w: it is running", ErrState)
	}
	switch m.Status {
	case MigUndoing:
		bg := context.WithoutCancel(ctx)
		o.migGo("undo:"+tenant+":"+id, func() { o.runUndo(bg, tenant, actor, id) })
	case MigLoading, MigInterrupted, MigFailed:
		for _, p := range m.Packages {
			if err := o.checkPassphrase(tenant, p, passphrase); err != nil {
				return m, err
			}
		}
		m.Status, m.Error = MigLoading, ""
		if err := docs.Upsert(ctx, o.Docs, tenant, cMig, id, m, actor); err != nil {
			return m, err
		}
		o.log(ctx, tenant, actor, "migration.resumed", id, nil)
		o.startLoad(ctx, tenant, actor, id, passphrase)
	default:
		return m, fmt.Errorf("%w: migration %s is %s", ErrState, id, strings.ToLower(m.Status))
	}
	m, _, err = docs.Get[Migration](ctx, o.Docs, tenant, cMig, id)
	return m, err
}

func (o *Ops) saveMig(ctx context.Context, tenant, actor string, m *Migration) error {
	return docs.Upsert(ctx, o.Docs, tenant, cMig, m.ID, *m, actor)
}

func (o *Ops) runLoad(ctx context.Context, tenant, actor, id, passphrase string) {
	m, _, err := docs.Get[Migration](ctx, o.Docs, tenant, cMig, id)
	if err != nil {
		return
	}
	var r *pkg.Reader
	open := ""
	defer func() {
		if r != nil {
			r.Close()
		}
	}()
	fail := func(i int, err error) {
		msg := strings.TrimPrefix(err.Error(), "invalid: ")
		if i >= 0 {
			m.Steps[i].Status, m.Steps[i].Message = "Failed", msg
			msg = m.Steps[i].Path + ": " + msg
		}
		m.Status, m.Error = MigFailed, msg
		o.saveMig(ctx, tenant, actor, &m)
		o.log(ctx, tenant, actor, "migration.failed", id, map[string]any{"error": msg})
	}
	defer func() {
		if p := recover(); p != nil {
			fail(-1, fmt.Errorf("the load stopped unexpectedly: %v", p))
		}
	}()
	d, _ := o.migDir(tenant)
	for i := range m.Steps {
		st := &m.Steps[i]
		if st.Status == "Done" {
			continue
		}
		if open != st.Package {
			if r != nil {
				r.Close()
				r = nil
			}
			if r, err = o.openMig(ctx, tenant, st.Package, passphrase); err != nil {
				fail(i, err)
				return
			}
			open = st.Package
		}
		switch st.Kind {
		case StepMaster:
			data, err := r.ReadAll(st.Path, 2<<30)
			if err != nil {
				fail(i, err)
				return
			}
			before := map[string]bool{}
			if st.Source == "OPENING-BALANCES" {
				es, _ := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{})
				for _, e := range es {
					before[e.ID] = true
				}
			}
			res, err := o.LoadFile(ctx, tenant, actor, st.Source, st.Date, m.System+" "+filepath.Base(st.Path), data)
			st.RawID = res.File.ID
			if err != nil {
				if len(res.Errors) > 0 {
					e := res.Errors[0]
					err = fmt.Errorf("%v (first: line %d %s %s)", err, e.Line, e.Field, e.Msg)
				}
				fail(i, err)
				return
			}
			st.Rows = res.Added + res.DuplicateRows
			if st.Source == "OPENING-BALANCES" {
				es, _ := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{})
				for _, e := range es {
					if !before[e.ID] && !contains(m.Entries, e.ID) {
						m.Entries = append(m.Entries, e.ID)
					}
				}
			}
		case StepHistory:
			n, err := o.storeHistory(ctx, tenant, actor, &m, r, st, filepath.Join(d, "tmp"))
			if err != nil {
				fail(i, err)
				return
			}
			st.Rows = n
		case StepDocuments:
			if err := o.archiveDocs(ctx, tenant, actor, &m, i, r); err != nil {
				fail(i, err)
				return
			}
		}
		st.Status, st.Message = "Done", ""
		if err := o.saveMig(ctx, tenant, actor, &m); err != nil {
			fail(i, err)
			return
		}
	}
	m.Docs = 0
	for _, st := range m.Steps {
		for _, n := range st.Counts {
			m.Docs += n
		}
	}
	m.Status, m.FinishedAt, m.Error = MigLoaded, o.now(), ""
	if rc, err := o.reconcile(ctx, tenant, actor, &m); err == nil {
		m.Recon = rc
	}
	o.dpAskFillIns(ctx, tenant, actor, &m)
	o.saveMig(ctx, tenant, actor, &m)
	o.log(ctx, tenant, actor, "migration.loaded", id, map[string]any{"firm": m.Firm, "steps": len(m.Steps), "documents": m.Docs, "entries": len(m.Entries)})
}

// ProposeUndo asks for a loaded migration to be undone. It is allowed only
// while nothing else has been posted to the ledger since the load.
func (o *Ops) ProposeUndo(ctx context.Context, tenant, actor, id, reason string) (Migration, error) {
	if err := o.require(ctx, tenant, actor, "Undoing a migration", RoleAdmin); err != nil {
		return Migration{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return Migration{}, invalid("say why the migration is to be undone")
	}
	m, _, err := docs.Get[Migration](ctx, o.Docs, tenant, cMig, id)
	if err != nil {
		return m, fmt.Errorf("%w: no such migration", ErrNotFound)
	}
	if m.Status != MigLoaded && m.Status != MigFailed {
		return m, fmt.Errorf("%w: migration %s is %s; only a loaded (or failed) migration can be undone", ErrState, id, strings.ToLower(m.Status))
	}
	if err := o.migUndoAllowed(ctx, tenant, m); err != nil {
		return m, err
	}
	m.Status = MigUndoProposed
	m.Undo = &MigUndo{Status: "Proposed", ProposedBy: actor, ProposedAt: o.now(), Reason: strings.TrimSpace(reason)}
	if err := o.saveMig(ctx, tenant, actor, &m); err != nil {
		return m, err
	}
	o.log(ctx, tenant, actor, "migration.undo.proposed", id, map[string]any{"reason": m.Undo.Reason})
	o.raiseWork(ctx, tenant, "migration", id+"-undo", "Approve undoing migration "+id, actor+" asked to undo the migration: "+m.Undo.Reason, "", RoleChecker, "migrate", "")
	return m, nil
}

func (o *Ops) migUndoAllowed(ctx context.Context, tenant string, m Migration) error {
	n, err := o.migForeign(ctx, tenant, m.BaseSeq)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %s ledger entries have been posted since the migration; it can no longer be undone. Correct the balances with journal entries instead", ErrState, groupIN(int64(n)))
	}
	held := 0
	o.eachMigArchive(ctx, tenant, m.ID, func(key string) error {
		if d, _, err := docs.Get[ArchiveDoc](ctx, o.Docs, tenant, cArc, key); err == nil && d.Mig == m.ID && d.LegalHold {
			held++
		}
		return nil
	})
	if held > 0 {
		return fmt.Errorf("%w: %d of the migration's archived documents are on legal hold; release the hold first", ErrState, held)
	}
	return nil
}

// DecideUndo approves (and runs) or rejects an undo. The person who asked
// for it cannot decide it.
func (o *Ops) DecideUndo(ctx context.Context, tenant, actor, id string, approve bool, note string) (Migration, error) {
	if err := o.require(ctx, tenant, actor, "Approving a migration load", RoleChecker); err != nil {
		return Migration{}, err
	}
	m, _, err := docs.Get[Migration](ctx, o.Docs, tenant, cMig, id)
	if err != nil {
		return m, fmt.Errorf("%w: no such migration", ErrNotFound)
	}
	if m.Status != MigUndoProposed || m.Undo == nil {
		return m, fmt.Errorf("%w: no undo is waiting for migration %s", ErrState, id)
	}
	if m.Undo.ProposedBy == actor {
		return m, fmt.Errorf("%w: the person who asked for an undo cannot approve or reject it", ErrForbidden)
	}
	m.Undo.DecidedBy, m.Undo.DecidedAt, m.Undo.Note = actor, o.now(), strings.TrimSpace(note)
	o.closeWork(ctx, tenant, actor, "migration", id+"-undo", "decided")
	if !approve {
		if m.Undo.Note == "" {
			return m, invalid("give a reason for rejecting")
		}
		m.Undo.Status, m.Status = "Rejected", MigLoaded
		if m.FinishedAt.IsZero() {
			m.Status = MigFailed
		}
		err := o.saveMig(ctx, tenant, actor, &m)
		o.log(ctx, tenant, actor, "migration.undo.rejected", id, map[string]any{"note": m.Undo.Note})
		return m, err
	}
	if err := o.migUndoAllowed(ctx, tenant, m); err != nil {
		return m, err
	}
	m.Undo.Status, m.Status = "Approved", MigUndoing
	if err := o.saveMig(ctx, tenant, actor, &m); err != nil {
		return m, err
	}
	o.log(ctx, tenant, actor, "migration.undo.approved", id, map[string]any{"proposed_by": m.Undo.ProposedBy})
	bg := context.WithoutCancel(ctx)
	o.migGo("undo:"+tenant+":"+id, func() { o.runUndo(bg, tenant, actor, id) })
	m, _, err = docs.Get[Migration](ctx, o.Docs, tenant, cMig, id)
	return m, err
}

func (o *Ops) runUndo(ctx context.Context, tenant, actor, id string) {
	m, _, err := docs.Get[Migration](ctx, o.Docs, tenant, cMig, id)
	if err != nil {
		return
	}
	stop := func(err error) {
		m.Error = "undo: " + err.Error()
		o.saveMig(ctx, tenant, actor, &m)
		o.log(ctx, tenant, actor, "migration.undo.failed", id, map[string]any{"error": err.Error()})
	}
	rev := 0
	for _, e := range m.Entries {
		r, _, err := o.App.Ledger.Reverse(ctx, tenant, e, actor, "undo of migration "+id, "")
		if err != nil {
			stop(err)
			return
		}
		if !contains(m.Entries, r.ID) {
			m.Entries = append(m.Entries, r.ID) // reversals are the migration's too
		}
		rev++
	}
	if err := o.removeHistory(ctx, tenant, actor, id); err != nil {
		stop(err)
		return
	}
	if err := o.removeArchive(ctx, tenant, actor, id); err != nil {
		stop(err)
		return
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cMigDone, m.Key, migDone{Migration: id, Undone: true}, actor); err != nil {
		stop(err)
		return
	}
	m.Undo.Status, m.Undo.Reversed, m.Status, m.Error = "Done", rev, MigUndone, ""
	o.saveMig(ctx, tenant, actor, &m)
	o.log(ctx, tenant, actor, "migration.undone", id, map[string]any{"reversed": rev})
}

// GetMigration returns one migration.
func (o *Ops) GetMigration(ctx context.Context, tenant, actor, id string) (Migration, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the migration", RoleChecker, RoleCompliance); err != nil {
		return Migration{}, err
	}
	m, _, err := docs.Get[Migration](ctx, o.Docs, tenant, cMig, id)
	if err != nil {
		return m, fmt.Errorf("%w: no such migration", ErrNotFound)
	}
	if m.Status == MigLoading && !o.migRunning("load:"+tenant+":"+id) {
		m.Status = MigInterrupted
	}
	return m, nil
}
