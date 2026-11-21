package ops

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/control"
	"ledgerline/internal/dataexport"
	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- the archive package ----
//
// On archive storage a package is <LEDGERLINE_ARCHIVE_DIR>/<firm>/<id>.llar:
// a ZIP encrypted with the firm's keys as it is written (the sealed format
// of the full export, dataexport.SealedWriter). Inside:
//
//	tables/<table>.jsonl   the period's rows exactly as stored, one per line, in
//	                       the order they are restored (raw_file, trade_event,
//	                       journal_entry, posting, doc, doc_history)
//	index/clients.csv      each client in the archive, with its first and last date
//	carried-forward.csv    each account's net movement in the archived entries:
//	                       what the opening balances carried forward hold
//	README.txt
//	manifest.json          every file with its size and SHA-256, the period, rows
//	                       per table, the retention date; signed like the full
//	                       export (manifest.json.p7s or manifest.json.sig)
//
// Rows are as stored: stored files keep their bytes and records stay
// encrypted with the firm's keys, so a package restores only into the same
// kind of storage it came from.

// restoreOrder is the order tables are written and restored in.
var restoreOrder = []string{"raw_file", "trade_event", "journal_entry", "posting", "doc", "doc_history"}

// archiveStorage is the folder archive packages go to, and whether it is a
// folder of its own (LEDGERLINE_ARCHIVE_DIR) rather than the work folder.
func (o *Ops) archiveStorage() (string, bool) {
	if o.ArchiveDir != "" {
		return o.ArchiveDir, true
	}
	w := o.WorkDir
	if w == "" {
		w = filepath.Join(os.TempDir(), "ledgerline-work")
	}
	return filepath.Join(w, "archive"), false
}

func (o *Ops) archivePath(tenant, id string) (string, error) {
	if !reTenantDir.MatchString(tenant) {
		return "", invalid("bad firm id %q", tenant)
	}
	if !strings.HasPrefix(id, "AR-") || strings.ContainsAny(id, `/\.`) {
		return "", fmt.Errorf("%w: no such archive", ErrNotFound)
	}
	d, _ := o.archiveStorage()
	return filepath.Join(d, tenant, id+".llar"), nil
}

type arcBuilt struct {
	refs  []app.RowRef
	net   map[string]money.Amount
	files map[string]string // stored file id -> name
}

// arcRowHead is what the package needs to know of a row, from either
// store's JSON.
type arcRowHead struct {
	ID         json.RawMessage `json:"id"`
	Seq        json.RawMessage `json:"seq"`
	Name       string          `json:"name"`
	Coll       string          `json:"coll"`
	Collection string          `json:"collection"`
	Origin     string          `json:"origin"`
	Exchange   string          `json:"exchange"`
	Segment    string          `json:"segment"`
	TradeDate  string          `json:"trade_date"`
	TradeNo    string          `json:"trade_no"`
	ValueDate  string          `json:"value_date"`
	// a memory entry carries its postings; a PostgreSQL posting row its own
	Postings    []ledger.Posting `json:"postings"`
	EntryID     string           `json:"entry_id"`
	AccountCode string           `json:"account_code"`
	DebitPaise  int64            `json:"debit_paise"`
	CreditPaise int64            `json:"credit_paise"`
}

func rawText(b json.RawMessage) string {
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	return string(bytes.TrimSpace(b))
}

// rowKey is the key a store removes a row by (app.RowRef).
func rowKey(table string, h arcRowHead) string {
	switch table {
	case "raw_file", "journal_entry":
		return rawText(h.ID)
	case "trade_event":
		return h.Origin + "|" + h.Exchange + "|" + h.Segment + "|" + h.TradeDate + "|" + h.TradeNo
	case "doc":
		return firstOf(h.Coll, h.Collection) + "|" + rawText(h.ID)
	case "doc_history":
		return rawText(h.Seq)
	}
	return ""
}

// writeArchivePackage writes the period's package to archive storage and
// fills in the record's package fields; it writes the live index of the
// archive's clients too.
func (o *Ops) writeArchivePackage(ctx context.Context, tenant string, r *app.ArchiveRecord, sel app.ArchiveSelection) (arcBuilt, error) {
	b := arcBuilt{net: map[string]money.Amount{}, files: map[string]string{}}
	path, err := o.archivePath(tenant, r.ID)
	if err != nil {
		return b, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return b, err
	}
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return b, err
	}
	defer func() {
		f.Close()
		os.Remove(tmp)
	}()
	bw := bufio.NewWriterSize(f, 1<<20)
	sw, err := dataexport.NewSealedWriter(bw, o.exportSealer(), tenant, "archive:"+r.ID)
	if err != nil {
		return b, err
	}
	now := o.now()
	ew := &exportWriter{ar: dataexport.NewArchive(sw, now)}
	rows := map[string]int{}
	freed := map[string]app.ArchiveStat{}
	clients := map[string][2]string{}
	entryDay := map[string]string{}
	seen := map[string]bool{}
	cur := ""
	add := func(ucc, day string) {
		if ucc == "" {
			return
		}
		c, ok := clients[ucc]
		if !ok {
			c = [2]string{day, day}
		}
		if day != "" && (c[0] == "" || day < c[0]) {
			c[0] = day
		}
		if day > c[1] {
			c[1] = day
		}
		clients[ucc] = c
	}
	err = o.App.Periods.PeriodRows(ctx, tenant, sel, func(row app.ArchiveRow) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if row.Table != cur {
			if seen[row.Table] {
				return fmt.Errorf("the store gave %s rows out of order", row.Table)
			}
			seen[row.Table], cur = true, row.Table
			if err := ew.to("tables/"+row.Table+".jsonl", "table"); err != nil {
				return err
			}
		}
		if err := ew.line(row.Row); err != nil {
			return err
		}
		rows[row.Table]++
		if row.Ref.Table != "" {
			b.refs = append(b.refs, row.Ref)
		}
		s := freed[row.Kind]
		if row.Ref.Table != "" {
			s.Rows++
		}
		s.Bytes += row.Bytes
		freed[row.Kind] = s
		var h arcRowHead
		if row.Table == "raw_file" || row.Table == "journal_entry" || row.Table == "posting" {
			if err := json.Unmarshal(row.Row, &h); err != nil {
				return fmt.Errorf("%s row: %w", row.Table, err)
			}
		}
		day := row.Date
		switch row.Table {
		case "raw_file":
			b.files[rawText(h.ID)] = h.Name
		case "journal_entry":
			entryDay[rawText(h.ID)] = h.ValueDate
			for _, p := range h.Postings {
				b.net[p.Account] += p.Debit - p.Credit
			}
		case "posting":
			b.net[h.AccountCode] += money.Amount(h.DebitPaise - h.CreditPaise)
			day = entryDay[h.EntryID]
		}
		for _, u := range row.UCC {
			add(u, day)
		}
		return nil
	})
	if err != nil {
		return b, err
	}
	// the clients and the balances carried forward
	cw, err := ew.csv("index/clients.csv")
	if err != nil {
		return b, err
	}
	_ = cw.Write([]string{"UCC", "First", "Last"})
	uccs := make([]string, 0, len(clients))
	for u := range clients {
		uccs = append(uccs, u)
	}
	sort.Strings(uccs)
	for _, u := range uccs {
		_ = cw.Write([]string{u, clients[u][0], clients[u][1]})
		ew.ar.Count(1)
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return b, err
	}
	cw, err = ew.csv("carried-forward.csv")
	if err != nil {
		return b, err
	}
	_ = cw.Write([]string{"Account", "NetDebit"})
	accts := make([]string, 0, len(b.net))
	for a, v := range b.net {
		if v != 0 {
			accts = append(accts, a)
		}
	}
	sort.Strings(accts)
	for _, a := range accts {
		_ = cw.Write([]string{a, b.net[a].String()})
		ew.ar.Count(1)
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return b, err
	}
	set, _ := o.Settings(ctx, tenant)
	backend := ""
	if o.App.Snap != nil {
		backend = o.App.Snap.Backend()
	}
	m := dataexport.Manifest{Kind: dataexport.PeriodKind, ExportID: r.ID, Tenant: tenant, Firm: set.Firm, FirmCode: firmCode(set), Created: now.Format(time.RFC3339),
		LedgerlineVersion: control.AppVersion, Backend: backend, RequestedBy: r.RequestedBy, ApprovedBy: r.ApprovedBy, Collections: map[string]int{}, Tables: rows,
		Period: &dataexport.PeriodInfo{ArchiveID: r.ID, From: r.From, To: r.To, Kinds: r.Kinds, Rows: rows, RetainUntil: r.RetainUntil, Clients: len(clients), Backend: backend}}
	if _, err := ew.file("README.txt", "readme", []byte(archiveReadme(set, m, r)), 0); err != nil {
		return b, err
	}
	if err := ew.flush(); err != nil {
		return b, err
	}
	var mb []byte
	if fn, info, err := o.docSigner(ctx, tenant); err == nil {
		si := dataexport.SignatureInfo{Method: "cms", File: dataexport.ManifestName + ".p7s", Signer: info.Name, CertificateSHA256: info.SHA256}
		if mb, err = ew.ar.Finish(m, si, func(x []byte) ([]byte, error) { return fn(x, now) }); err == nil {
			m.Signature = si
		}
	}
	if mb == nil {
		si, fn := dataexport.Ed25519Signer()
		if mb, err = ew.ar.Finish(m, si, fn); err != nil {
			return b, err
		}
		m.Signature = si
	}
	if err := sw.Close(); err != nil {
		return b, err
	}
	if err := bw.Flush(); err != nil {
		return b, err
	}
	if err := f.Sync(); err != nil {
		return b, err
	}
	if err := f.Close(); err != nil {
		return b, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return b, err
	}
	dir, _ := o.archiveStorage()
	rel, _ := filepath.Rel(dir, path)
	r.PackageFile, r.PackageBytes, r.PackageSHA256 = rel, sw.Size(), dataexport.ManifestSHA256(mb)
	if st, err := os.Stat(path); err == nil {
		r.PackageBytes = st.Size() // the file on archive storage, encrypted
	}
	r.SignedWith, r.PublicKey = m.Signature.Signer, m.Signature.PublicKey
	if m.Signature.Method == "cms" {
		r.SignedWith = "Digital signature: " + m.Signature.Signer
	}
	r.Rows, r.Freed, r.Clients = rows, freed, len(clients)
	// The live index of the archive's clients, in shards.
	shards := map[int]*arcClients{}
	for _, u := range uccs {
		n := histBucket(u, arcClientShards)
		if shards[n] == nil {
			shards[n] = &arcClients{UCC: map[string][2]string{}}
		}
		shards[n].UCC[u] = clients[u]
	}
	for n, sh := range shards {
		if err := docs.Upsert(ctx, o.Docs, tenant, cArcIdx, fmt.Sprintf("%s|%02d", r.ID, n), *sh, SystemActor); err != nil {
			return b, err
		}
	}
	return b, nil
}

func archiveReadme(set Settings, m dataexport.Manifest, r *app.ArchiveRecord) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	w("ARCHIVE OF %s FROM LEDGERLINE", strings.ToUpper(firstOf(set.Firm, m.Tenant)))
	w("")
	w("Archive %s: %s (%s to %s), made %s by Ledgerline %s (%s storage).", r.ID, r.Label, r.From, r.To, m.Created, m.LedgerlineVersion, m.Backend)
	w("Asked for by %s, approved by %s. Keep it at least until %s.", r.RequestedBy, firstOf(r.ApprovedBy, "-"), r.RetainUntil)
	w("")
	w("WHAT IS IN IT")
	w("tables/<table>.jsonl  the period's rows exactly as Ledgerline stored them, one JSON object a line.")
	w("                      Stored files keep their bytes and records stay encrypted with the firm's keys,")
	w("                      so Ledgerline can put them back exactly (Admin > Data & exit > Archive > Restore).")
	w("index/clients.csv     each client with archived rows, and the first and last date.")
	w("carried-forward.csv   each account's net movement in the archived ledger entries (debit positive):")
	w("                      the live books hold it as opening balances carried forward on %s.", r.To)
	w("manifest.json         every file with its size and SHA-256; manifest.json.p7s or .sig is its signature.")
	w("")
	w("The audit trail is not in the package: it stays in Ledgerline, whole, with its seals.")
	return b.String()
}

// arcPkg is an archive package opened for reading: decrypted into a
// temporary file in the firm's work folder, verified, and read table by
// table in the order rows are restored.
type arcPkg struct {
	tmp    *os.File
	zr     *zip.Reader
	man    dataexport.Manifest
	tables []*zip.File
	ti     int
	rc     io.ReadCloser
	br     *bufio.Reader
	files  []string
}

func (p *arcPkg) close() {
	if p.rc != nil {
		p.rc.Close()
	}
	if p.tmp != nil {
		p.tmp.Close()
		os.Remove(p.tmp.Name())
	}
}

// next is the next row, as app.PeriodStore's RestorePeriod reads them.
func (p *arcPkg) next() (app.Record, bool, error) {
	for {
		if p.br == nil {
			if p.ti >= len(p.tables) {
				return app.Record{}, false, nil
			}
			rc, err := p.tables[p.ti].Open()
			if err != nil {
				return app.Record{}, false, err
			}
			p.rc, p.br = rc, bufio.NewReaderSize(rc, 1<<20)
		}
		line, err := p.br.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			table := strings.TrimSuffix(strings.TrimPrefix(p.tables[p.ti].Name, "tables/"), ".jsonl")
			if table == "raw_file" {
				var h arcRowHead
				if json.Unmarshal(line, &h) == nil {
					p.files = append(p.files, rawText(h.ID))
				}
			}
			return app.Record{Table: table, Row: json.RawMessage(append([]byte(nil), line...))}, true, nil
		}
		if err == io.EOF {
			p.rc.Close()
			p.rc, p.br = nil, nil
			p.ti++
			continue
		}
		if err != nil {
			return app.Record{}, false, err
		}
	}
}

// openArchivePackage reads the record's package back from archive storage,
// decrypts it and verifies it: the manifest's signature (with the key
// recorded for it), every file's size and SHA-256, and that it is this
// archive's package.
func (o *Ops) openArchivePackage(ctx context.Context, tenant string, r app.ArchiveRecord) (*arcPkg, error) {
	path, err := o.archivePath(tenant, r.ID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("the archive package is missing from archive storage: %w", err)
	}
	defer f.Close()
	sr, err := dataexport.NewSealedReader(f, o.exportSealer(), tenant, "archive:"+r.ID)
	if err != nil {
		return nil, err
	}
	wd, err := o.firmWorkDir(tenant)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(wd, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(wd, "archive-*.zip")
	if err != nil {
		return nil, err
	}
	p := &arcPkg{tmp: tmp}
	n, err := io.Copy(tmp, sr)
	if err != nil {
		p.close()
		return nil, fmt.Errorf("the archive package does not open: %w", err)
	}
	v, err := dataexport.VerifyKind(tmp, n, r.PublicKey, dataexport.PeriodKind)
	if err != nil {
		p.close()
		return nil, err
	}
	if v.ManifestSHA256 != r.PackageSHA256 || v.Manifest.Period == nil || v.Manifest.Period.ArchiveID != r.ID || v.Manifest.Tenant != tenant {
		p.close()
		return nil, errors.New("the package in archive storage is not the one recorded for this archive")
	}
	p.man = v.Manifest
	if p.zr, err = zip.NewReader(tmp, n); err != nil {
		p.close()
		return nil, err
	}
	by := map[string]*zip.File{}
	for _, zf := range p.zr.File {
		by[zf.Name] = zf
	}
	for _, t := range restoreOrder {
		if zf := by["tables/"+t+".jsonl"]; zf != nil {
			p.tables = append(p.tables, zf)
		}
	}
	return p, nil
}

// verifyArchivePackage reads the package back and checks it holds every
// row about to be removed, and as many rows of each table as were written.
func (o *Ops) verifyArchivePackage(ctx context.Context, tenant string, r app.ArchiveRecord, refs []app.RowRef) error {
	p, err := o.openArchivePackage(ctx, tenant, r)
	if err != nil {
		return err
	}
	defer p.close()
	want := map[app.RowRef]bool{}
	for _, x := range refs {
		want[x] = true
	}
	got := map[string]int{}
	for {
		rec, ok, err := p.next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		got[rec.Table]++
		var h arcRowHead
		if err := json.Unmarshal(rec.Row, &h); err != nil {
			return fmt.Errorf("%s row %d: %w", rec.Table, got[rec.Table], err)
		}
		delete(want, app.RowRef{Table: rec.Table, Key: rowKey(rec.Table, h)})
	}
	for t, n := range r.Rows {
		if got[t] != n {
			return fmt.Errorf("%s: %d rows in the package, %d written", t, got[t], n)
		}
	}
	if len(want) > 0 {
		for x := range want {
			return fmt.Errorf("%d rows to remove are not in the package (first: %s %s)", len(want), x.Table, x.Key)
		}
	}
	return nil
}

// ---- a firm's exit ----

// exportArchives adds every archive package the firm has to its export,
// decrypted (each is a signed ZIP of its own, checked the same way as the
// export).
func (o *Ops) exportArchives(ctx context.Context, tenant string, ew *exportWriter) (int, error) {
	if o.App.Periods == nil {
		return 0, nil
	}
	rs, err := o.App.Periods.ListArchives(ctx, tenant)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rs {
		if r.PackageFile == "" || (r.State != app.ArcArchived && r.State != app.ArcRestoreRequested && r.State != app.ArcRestoreApproved && r.State != app.ArcRestored) {
			continue
		}
		path, err := o.archivePath(tenant, r.ID)
		if err != nil {
			return n, err
		}
		f, err := os.Open(path)
		if err != nil {
			return n, fmt.Errorf("archive %s's package is missing from archive storage: %w", r.ID, err)
		}
		sr, err := dataexport.NewSealedReader(f, o.exportSealer(), tenant, "archive:"+r.ID)
		if err == nil {
			if err = ew.to("archives/"+r.ID+".zip", "archive"); err == nil {
				if _, err = io.Copy(ew.bw, sr); err == nil {
					err = ew.flush()
				}
				ew.cur = ""
			}
		}
		f.Close()
		if err != nil {
			return n, fmt.Errorf("archive %s: %w", r.ID, err)
		}
		n++
	}
	return n, nil
}

// archiveFolder is the firm's folder in archive storage, which its exit
// deletes with the rest of its data.
func (o *Ops) archiveFolder(tenant string) (string, error) {
	if !reTenantDir.MatchString(tenant) {
		return "", invalid("bad firm id %q", tenant)
	}
	d, _ := o.archiveStorage()
	return filepath.Join(d, tenant), nil
}
