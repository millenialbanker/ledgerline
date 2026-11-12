package ops

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/migrate/pkg"
)

// ---- the document archive: documents brought from the old system ----
//
// Contract notes, statements, DP bills and the rest, up to eight years of
// them, as the old system sent them. Each document is a stored file (kept
// encrypted, like every file Ledgerline stores) with an index record under
// the client: type, date, financial year, subject, whether it was signed,
// format, size, SHA-256, where it came from, and the date it must be kept
// until. A firm-wide index per financial year, in chunks, lets staff search
// about a million documents without reading every record. Documents cannot
// be deleted before they may be (RetainUntil: from the package, else eight
// years from the document's date) and not at all while on legal hold.
// Every download is in the audit trail. A client sees only their own.

const (
	cArc      = "migarc"   // <ucc or _>|<doc id>: ArchiveDoc
	cArcFY    = "migarcfy" // <mig>|<fy>: arcFY
	cArcChunk = "migarcc"  // <mig>|<fy>|<n>: arcChunk

	arcChunkRows = 2000
	arcKeepYears = 8
)

// ArchiveDoc is one archived document.
type ArchiveDoc struct {
	Key          string    `json:"key"` // <ucc>|<id>
	ID           string    `json:"id"`
	Mig          string    `json:"mig"`
	System       string    `json:"system"`
	UCC          string    `json:"ucc"`
	Type         string    `json:"type"`
	Date         string    `json:"date,omitempty"`
	FY           string    `json:"fy"`
	Subject      string    `json:"subject,omitempty"`
	To           string    `json:"to,omitempty"`
	Signed       bool      `json:"signed"`
	Format       string    `json:"format"`
	Bytes        int64     `json:"bytes"`
	SHA256       string    `json:"sha256"`
	SourceRef    string    `json:"source_ref,omitempty"`
	RetainUntil  string    `json:"retain_until"`
	LegalHold    bool      `json:"legal_hold,omitempty"`
	HoldReason   string    `json:"hold_reason,omitempty"`
	HoldBy       string    `json:"hold_by,omitempty"`
	Raw          string    `json:"raw,omitempty"`
	LoadedAt     time.Time `json:"loaded_at"`
	Removed      bool      `json:"removed,omitempty"` // the migration was undone
	Deleted      bool      `json:"deleted,omitempty"`
	DeletedBy    string    `json:"deleted_by,omitempty"`
	DeletedAt    string    `json:"deleted_at,omitempty"`
	DeleteReason string    `json:"delete_reason,omitempty"`
}

type arcFY struct {
	Mig     string `json:"mig"`
	FY      string `json:"fy"`
	Chunks  int    `json:"chunks"`
	Count   int    `json:"count"`
	Removed bool   `json:"removed,omitempty"`
}

// arcChunk rows: key, type, date, signed (Y/N), ucc.
type arcChunk struct {
	Rows [][]string `json:"rows"`
}

func arcKey(ucc, id string) string {
	if ucc == "" {
		ucc = "_"
	}
	return strings.ToUpper(ucc) + "|" + id
}

// retainUntil is the package's date, else eight years from the document's
// date, else from the end of its financial year, else from today.
func retainUntil(d pkg.DocMeta, fy string, today time.Time) string {
	switch {
	case !d.RetainUntil.IsZero():
		return d.RetainUntil.Format("2006-01-02")
	case !d.Date.IsZero():
		return d.Date.AddDate(arcKeepYears, 0, 0).Format("2006-01-02")
	}
	var y int
	if _, err := fmt.Sscanf(fy, "%d-", &y); err == nil && y > 1900 {
		return fmt.Sprintf("%d-03-31", y+1+arcKeepYears)
	}
	return today.AddDate(arcKeepYears, 0, 0).Format("2006-01-02")
}

// archiveDocs stores one part's documents for the ticked years, carrying
// on from where an interrupted run stopped.
func (o *Ops) archiveDocs(ctx context.Context, tenant, actor string, m *Migration, si int, r *pkg.Reader) error {
	st := &m.Steps[si]
	if st.Seq == nil {
		st.Seq = map[string]int{}
	}
	if st.Counts == nil {
		st.Counts = map[string]int{}
	}
	fys := map[string]bool{}
	for _, f := range st.FYs {
		fys[f] = true
	}
	if !r.Has(st.Path) {
		st.Message = "no documents of this firm in this part"
		return nil
	}
	buf := map[string][][]string{}
	pending := 0
	flush := func(row int) error {
		for _, fy := range sortedKeys(buf) {
			rows := buf[fy]
			n := st.Seq[fy]
			if err := docs.Upsert(ctx, o.Docs, tenant, cArcChunk, fmt.Sprintf("%s|%s|%05d", m.ID, fy, n), arcChunk{Rows: rows}, actor); err != nil {
				return err
			}
			st.Seq[fy], st.Counts[fy] = n+1, st.Counts[fy]+len(rows)
			if err := docs.Upsert(ctx, o.Docs, tenant, cArcFY, m.ID+"|"+fy, arcFY{Mig: m.ID, FY: fy, Chunks: st.Seq[fy], Count: st.Counts[fy]}, actor); err != nil {
				return err
			}
		}
		buf, pending = map[string][][]string{}, 0
		st.Resume = row
		m.Docs = 0
		for _, s := range m.Steps {
			for _, c := range s.Counts {
				m.Docs += c
			}
		}
		return o.saveMig(ctx, tenant, actor, m)
	}
	i := -1
	err := r.CSV(st.Path, func(h, row []string) error {
		i++
		if i < st.Resume {
			return nil
		}
		d, probs := docRow(h, row)
		fy := d.FY
		if fy == "" && !d.Date.IsZero() {
			fy, _, _ = fyOf(d.Date.Format("2006-01-02"))
		}
		if fy == "" {
			fy = "unknown"
		}
		if len(probs) > 0 || (len(fys) > 0 && !fys[d.FY] && !fys[fy]) {
			if len(probs) > 0 {
				st.Skipped++
			}
			return nil
		}
		get := func(c string) string {
			for k, x := range h {
				if strings.EqualFold(x, c) && k < len(row) {
					return strings.TrimSpace(row[k])
				}
			}
			return ""
		}
		rc, err := r.OpenDocument(get("Path"), get("SHA256"))
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(rc, 1<<30))
		rc.Close()
		if err != nil {
			return fmt.Errorf("document %s: %w", d.ID, err)
		}
		ext := strings.ToLower(firstOf(strings.TrimPrefix(d.Format, "."), "bin"))
		rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "MIG-ARCHIVE", Name: d.ID + "." + ext, SHA256: get("SHA256"), Size: int64(len(data)), Content: data, ReceivedAt: o.now()})
		if err != nil {
			return err
		}
		date := ""
		if !d.Date.IsZero() {
			date = d.Date.Format("2006-01-02")
		}
		ad := ArchiveDoc{Key: arcKey(d.UCC, d.ID), ID: d.ID, Mig: m.ID, System: m.System, UCC: d.UCC, Type: firstOf(d.Type, pkg.DocOther), Date: date, FY: fy,
			Subject: d.Subject, To: d.To, Signed: d.Signed, Format: ext, Bytes: int64(len(data)), SHA256: get("SHA256"), SourceRef: d.SourceRef,
			RetainUntil: retainUntil(d, fy, o.now()), Raw: rf.ID, LoadedAt: o.now()}
		if err := docs.Upsert(ctx, o.Docs, tenant, cArc, ad.Key, ad, actor); err != nil {
			return err
		}
		buf[fy] = append(buf[fy], []string{ad.Key, ad.Type, ad.Date, yesNo(ad.Signed), ad.UCC})
		if pending++; pending >= arcChunkRows {
			return flush(i + 1)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return flush(i + 1)
}

func yesNo(b bool) string {
	if b {
		return "Y"
	}
	return "N"
}

// eachMigArchive calls fn with the key of every document a migration
// archived.
func (o *Ops) eachMigArchive(ctx context.Context, tenant, mig string, fn func(key string) error) error {
	fs, err := docs.List[arcFY](ctx, o.Docs, tenant, cArcFY, mig+"|")
	if err != nil {
		return err
	}
	for _, f := range fs {
		if f.Mig != mig {
			continue
		}
		for c := 0; c < f.Chunks; c++ {
			ch, _, err := docs.Get[arcChunk](ctx, o.Docs, tenant, cArcChunk, fmt.Sprintf("%s|%s|%05d", mig, f.FY, c))
			if err != nil {
				return err
			}
			for _, r := range ch.Rows {
				if err := fn(r[0]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// removeArchive hides a migration's documents (undo).
func (o *Ops) removeArchive(ctx context.Context, tenant, actor, mig string) error {
	err := o.eachMigArchive(ctx, tenant, mig, func(key string) error {
		_, err := docs.Update(ctx, o.Docs, tenant, cArc, key, actor, func(d *ArchiveDoc) error {
			if d.Mig != mig || d.Removed {
				return errAlreadyDone
			}
			d.Removed = true
			return nil
		})
		if err == errAlreadyDone || (err != nil && strings.Contains(err.Error(), "not found")) {
			return nil
		}
		return err
	})
	if err != nil {
		return err
	}
	fs, _ := docs.List[arcFY](ctx, o.Docs, tenant, cArcFY, mig+"|")
	for _, f := range fs {
		if f.Mig == mig && !f.Removed {
			f.Removed = true
			if err := docs.Upsert(ctx, o.Docs, tenant, cArcFY, mig+"|"+f.FY, f, actor); err != nil {
				return err
			}
		}
	}
	return nil
}

// ArchiveQuery searches the archive. Dates are inclusive; Signed is Y, N
// or empty.
type ArchiveQuery struct {
	UCC    string `json:"ucc"`
	Type   string `json:"type"`
	From   string `json:"from"`
	To     string `json:"to"`
	Signed string `json:"signed"`
	Limit  int    `json:"limit"`
}

// ArchiveResult is a page of archived documents.
type ArchiveResult struct {
	Docs  []ArchiveDoc `json:"docs"`
	More  bool         `json:"more"` // there are more than Limit
	Label string       `json:"label,omitempty"`
	Types []string     `json:"types"`
	Total int          `json:"total"` // documents in the archive
}

var arcTypes = []string{pkg.DocContractNote, pkg.DocStatement, pkg.DocDPStatement, pkg.DocDPBill, pkg.DocBill, pkg.DocMargin, pkg.DocKYC, pkg.DocOther}

func (q ArchiveQuery) match(typ, date, signed string) bool {
	if q.Type != "" && !strings.EqualFold(q.Type, typ) {
		return false
	}
	if q.From != "" && (date == "" || date < q.From) {
		return false
	}
	if q.To != "" && (date == "" || date > q.To) {
		return false
	}
	if q.Signed != "" && !strings.EqualFold(q.Signed, signed) {
		return false
	}
	return true
}

func fyOverlaps(fy, from, to string) bool {
	var y int
	if _, err := fmt.Sscanf(fy, "%d-", &y); err != nil {
		return true
	}
	start, end := fmt.Sprintf("%d-04-01", y), fmt.Sprintf("%d-03-31", y+1)
	return (to == "" || start <= to) && (from == "" || end >= from)
}

// SearchArchive finds archived documents: one client's (UCC), or across the
// firm by type, dates and signature, newest first.
func (o *Ops) SearchArchive(ctx context.Context, tenant, actor string, q ArchiveQuery) (ArchiveResult, error) {
	if err := o.require(ctx, tenant, actor, "Searching the document archive", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return ArchiveResult{}, err
	}
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 200
	}
	out := ArchiveResult{Docs: []ArchiveDoc{}, Types: arcTypes}
	fs, err := docs.List[arcFY](ctx, o.Docs, tenant, cArcFY, "")
	if err != nil {
		return out, err
	}
	for _, f := range fs {
		if !f.Removed {
			out.Total += f.Count
		}
	}
	if u := strings.ToUpper(strings.TrimSpace(q.UCC)); u != "" {
		ds, err := docs.List[ArchiveDoc](ctx, o.Docs, tenant, cArc, u+"|")
		if err != nil {
			return out, err
		}
		for _, d := range ds {
			if d.UCC == u && !d.Removed && q.match(d.Type, d.Date, yesNo(d.Signed)) {
				out.Docs = append(out.Docs, d)
			}
		}
	} else {
		// Newest years first; within a year the index order.
		sort.SliceStable(fs, func(i, j int) bool { return fs[i].FY > fs[j].FY })
		var keys []string
	scan:
		for _, f := range fs {
			if f.Removed || !fyOverlaps(f.FY, q.From, q.To) {
				continue
			}
			for c := 0; c < f.Chunks; c++ {
				ch, _, err := docs.Get[arcChunk](ctx, o.Docs, tenant, cArcChunk, fmt.Sprintf("%s|%s|%05d", f.Mig, f.FY, c))
				if err != nil {
					return out, err
				}
				for _, r := range ch.Rows {
					if len(r) >= 4 && q.match(r[1], r[2], r[3]) {
						if len(keys) == q.Limit {
							out.More = true
							break scan
						}
						keys = append(keys, r[0])
					}
				}
			}
		}
		for _, k := range keys {
			if d, _, err := docs.Get[ArchiveDoc](ctx, o.Docs, tenant, cArc, k); err == nil && !d.Removed {
				out.Docs = append(out.Docs, d)
			}
		}
	}
	sort.SliceStable(out.Docs, func(i, j int) bool { return out.Docs[i].Date > out.Docs[j].Date })
	if len(out.Docs) > q.Limit {
		out.Docs, out.More = out.Docs[:q.Limit], true
	}
	if len(out.Docs) > 0 {
		out.Label = HistoryLabel(out.Docs[0].System)
	}
	return out, nil
}

// PortalArchive lists a client's own archived documents.
func (o *Ops) PortalArchive(ctx context.Context, tenant, actor, ucc string) (ArchiveResult, error) {
	if err := o.histAccess(ctx, tenant, actor, ucc, true); err != nil {
		return ArchiveResult{}, err
	}
	u := strings.ToUpper(strings.TrimSpace(ucc))
	out := ArchiveResult{Docs: []ArchiveDoc{}, Types: arcTypes}
	ds, err := docs.List[ArchiveDoc](ctx, o.Docs, tenant, cArc, u+"|")
	if err != nil {
		return out, err
	}
	for _, d := range ds {
		if d.UCC == u && !d.Removed && !d.Deleted && d.Type != pkg.DocKYC {
			d.SourceRef, d.HoldReason, d.HoldBy, d.Raw = "", "", "", ""
			out.Docs = append(out.Docs, d)
		}
	}
	sort.SliceStable(out.Docs, func(i, j int) bool { return out.Docs[i].Date > out.Docs[j].Date })
	out.Total = len(out.Docs)
	if len(out.Docs) > 0 {
		out.Label = HistoryLabel(out.Docs[0].System)
	}
	return out, nil
}

// ArchiveFile returns an archived document for download and records who
// downloaded it. In the portal only the client's own documents are served.
func (o *Ops) ArchiveFile(ctx context.Context, tenant, actor, ucc, id string, portal bool) (string, []byte, error) {
	if portal {
		if err := o.histAccess(ctx, tenant, actor, ucc, true); err != nil {
			return "", nil, fmt.Errorf("%w: no such document", ErrNotFound)
		}
	} else if err := o.require(ctx, tenant, actor, "Searching the document archive", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return "", nil, err
	}
	d, _, err := docs.Get[ArchiveDoc](ctx, o.Docs, tenant, cArc, arcKey(ucc, id))
	if err != nil || d.Removed || d.Deleted || (portal && (!strings.EqualFold(d.UCC, ucc) || d.UCC == "" || d.Type == pkg.DocKYC)) {
		return "", nil, fmt.Errorf("%w: no such document", ErrNotFound)
	}
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, d.Raw)
	if err != nil {
		return "", nil, err
	}
	if sha256Hex(rf.Content) != d.SHA256 {
		return "", nil, fmt.Errorf("the stored document does not match its fingerprint")
	}
	o.log(ctx, tenant, actor, "archive.download", d.Key, map[string]any{"ucc": d.UCC, "type": d.Type, "date": d.Date, "portal": portal})
	name := strings.NewReplacer(" ", "_", "/", "-").Replace(d.Type) + "_" + d.ID + "." + d.Format
	return name, rf.Content, nil
}

// SetArchiveHold puts a document on legal hold (no deletion at all) or
// releases it.
func (o *Ops) SetArchiveHold(ctx context.Context, tenant, actor, ucc, id string, on bool, reason string) (ArchiveDoc, error) {
	if err := o.require(ctx, tenant, actor, "Holding or deleting archived documents", RoleCompliance); err != nil {
		return ArchiveDoc{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return ArchiveDoc{}, invalid("give the reason (the matter, notice or order)")
	}
	d, err := docs.Update(ctx, o.Docs, tenant, cArc, arcKey(ucc, id), actor, func(d *ArchiveDoc) error {
		if d.Removed || d.Deleted {
			return fmt.Errorf("%w: no such document", ErrNotFound)
		}
		d.LegalHold, d.HoldReason, d.HoldBy = on, strings.TrimSpace(reason), actor
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "archive.hold", d.Key, map[string]any{"on": on, "reason": reason})
	}
	return d, err
}

// DeleteArchived deletes an archived document once it may be: never while
// on legal hold, and not before its RetainUntil date. The stored file is
// then no longer served to anyone.
func (o *Ops) DeleteArchived(ctx context.Context, tenant, actor, ucc, id, reason string) (ArchiveDoc, error) {
	if err := o.require(ctx, tenant, actor, "Holding or deleting archived documents", RoleCompliance); err != nil {
		return ArchiveDoc{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return ArchiveDoc{}, invalid("say why the document is deleted")
	}
	today := o.now().Format("2006-01-02")
	d, err := docs.Update(ctx, o.Docs, tenant, cArc, arcKey(ucc, id), actor, func(d *ArchiveDoc) error {
		switch {
		case d.Removed || d.Deleted:
			return fmt.Errorf("%w: no such document", ErrNotFound)
		case d.LegalHold:
			return fmt.Errorf("%w: the document is on legal hold (%s); it cannot be deleted", ErrState, d.HoldReason)
		case today < d.RetainUntil:
			return fmt.Errorf("%w: the document must be kept until %s; it cannot be deleted before then", ErrState, d.RetainUntil)
		}
		d.Deleted, d.DeletedBy, d.DeletedAt, d.DeleteReason = true, actor, today, strings.TrimSpace(reason)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "archive.deleted", d.Key, map[string]any{"reason": reason, "retain_until": d.RetainUntil})
	}
	return d, err
}
