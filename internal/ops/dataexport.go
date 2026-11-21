package ops

import (
	"bufio"
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/control"
	"ledgerline/internal/dataexport"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/vault"
)

// ---- full export of a firm's data ----
//
// SEBI's framework for the adoption of cloud services by regulated
// entities asks for data portability: the firm can take all of its data
// out, in a usable form, at any time. An Admin asks for "Export all our
// data"; a second person (the compliance officer or another Admin)
// approves; Ledgerline builds one ZIP in the background:
//
//   - collections/<name>.jsonl: every document of every collection,
//     decrypted, one JSON object per line (id, version, when and by whom it
//     last changed, and the document itself);
//   - history/document-versions.jsonl: every earlier version of every
//     document (PostgreSQL keeps them; the in-memory store does not);
//   - tables/<name>.jsonl: the ledger, trades, reconciliation and the other
//     tables, row by row as stored;
//   - files/<kind>/<id>_<name>: every stored file (KYC documents, exchange,
//     depository and bank files, contract notes, reports) with its original
//     bytes, and files/index.jsonl listing them with their SHA-256;
//   - csv/*.csv: the tables people open in a spreadsheet: clients, ledger
//     entries, trades, holdings, contract notes and bills, receipts,
//     payments, DP transactions and DP bills;
//   - audit/audit-trail.jsonl: the audit trail with every seal, and in the
//     export made for closing the account, audit/seal-keys.json, the keys
//     the seals are made with, so the firm can check them without
//     Ledgerline;
//   - README.txt, and manifest.json: every file with its size and SHA-256,
//     the time, the counts, the Ledgerline version; manifest.json is signed
//     (manifest.json.p7s with the firm's document-signing certificate, or
//     manifest.json.sig with an Ed25519 key made for the export, whose
//     public key is shown on the screen).
//
// The ZIP is streamed into a file on the server's disk (the firm's folder in
// the work folder), encrypted with the firm's keys as it is written, so a
// large firm's export is never held in memory. Only an Admin downloads it,
// through a link that works once and expires after 15 minutes; every
// download is in the audit trail. The file is deleted after 7 days.

const (
	cDataExport = "dataexport"
	// DataExportKeepDays is how long an export stays on the server.
	DataExportKeepDays = 7
	// dataExportLinkLife is how long a download link works.
	dataExportLinkLife = 15 * time.Minute
)

// Export states.
const (
	DXWaiting  = "Waiting for approval"
	DXBuilding = "Building"
	DXReady    = "Ready"
	DXFailed   = "Failed"
	DXRejected = "Rejected"
	DXDeleted  = "Deleted"
)

// DataExport is one export.
type DataExport struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Final       bool   `json:"final,omitempty"` // made for closing the account
	Exit        string `json:"exit,omitempty"`
	RequestedBy string `json:"requested_by"`
	RequestedAt string `json:"requested_at"`
	Reason      string `json:"reason,omitempty"`
	ApprovedBy  string `json:"approved_by,omitempty"`
	ApprovedAt  string `json:"approved_at,omitempty"`
	RejectedBy  string `json:"rejected_by,omitempty"`
	Note        string `json:"note,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	// Size is the ZIP's size in bytes; Entries the files in it.
	Size           int64        `json:"size,omitempty"`
	Entries        int          `json:"entries,omitempty"`
	Documents      int          `json:"documents,omitempty"`
	Collections    int          `json:"collections,omitempty"`
	StoredFiles    int          `json:"stored_files,omitempty"`
	AuditEntries   int          `json:"audit_entries,omitempty"`
	ManifestSHA256 string       `json:"manifest_sha256,omitempty"`
	SignedWith     string       `json:"signed_with,omitempty"`
	PublicKey      string       `json:"public_key,omitempty"` // Ed25519 signature: the key to check it with
	Error          string       `json:"error,omitempty"`
	KeepUntil      string       `json:"keep_until,omitempty"`
	Downloads      []DXDownload `json:"downloads,omitempty"`
	Links          []dxLink     `json:"links,omitempty"`
}

// DXDownload is one download.
type DXDownload struct {
	By string `json:"by"`
	At string `json:"at"`
}

// dxLink is a download link: only the token's hash is kept.
type dxLink struct {
	Hash    string `json:"hash"`
	By      string `json:"by"`
	Expires string `json:"expires"`
	Used    bool   `json:"used,omitempty"`
}

// exportView hides the links' hashes.
func (x DataExport) view() DataExport {
	x.Links = nil
	return x
}

func newExportID(now time.Time) string {
	b := make([]byte, 3)
	_, _ = crand.Read(b)
	return "DX-" + now.Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// exportSealer is what encrypts export files: the backup sealer, or the
// vault the stores use.
func (o *Ops) exportSealer() dataexport.Sealer {
	if o.Sealer != nil {
		return o.Sealer
	}
	if va := o.vaultAudit(); va != nil {
		return va.V
	}
	return nil
}

// firmWorkDir is the firm's folder in the work folder.
func (o *Ops) firmWorkDir(tenant string) (string, error) {
	if !reTenantDir.MatchString(tenant) {
		return "", invalid("bad firm id %q", tenant)
	}
	w := o.WorkDir
	if w == "" {
		w = filepath.Join(os.TempDir(), "ledgerline-work")
	}
	return filepath.Join(w, tenant), nil
}

func (o *Ops) exportPath(tenant, id string) (string, error) {
	if !strings.HasPrefix(id, "DX-") || strings.ContainsAny(id, `/\.`) {
		return "", fmt.Errorf("%w: no such export", ErrNotFound)
	}
	d, err := o.firmWorkDir(tenant)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "exports", id+".llsx"), nil
}

// RequestDataExport asks for a full export. An Admin asks; a second person
// approves (DecideDataExport).
func (o *Ops) RequestDataExport(ctx context.Context, tenant, actor, reason string) (DataExport, error) {
	if err := o.require(ctx, tenant, actor, "Starting a full data export", RoleAdmin); err != nil {
		return DataExport{}, err
	}
	if o.exportSealer() == nil || o.App.Snap == nil {
		return DataExport{}, fmt.Errorf("%w: full exports need the server's data keys and a store that can be read whole", ErrState)
	}
	xs, _ := docs.List[DataExport](ctx, o.Docs, tenant, cDataExport, "")
	for _, x := range xs {
		if x.State == DXWaiting || x.State == DXBuilding && o.exportRunning(tenant, x.ID) {
			return DataExport{}, fmt.Errorf("%w: export %s is %s; wait for it", ErrState, x.ID, strings.ToLower(x.State))
		}
	}
	now := o.now()
	x := DataExport{ID: newExportID(now), State: DXWaiting, RequestedBy: actor, RequestedAt: now.Format(time.RFC3339), Reason: clip(strings.TrimSpace(reason), 300)}
	if err := docs.Upsert(ctx, o.Docs, tenant, cDataExport, x.ID, x, actor); err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "dataexport.requested", x.ID, map[string]any{"reason": x.Reason})
	o.raiseWork(ctx, tenant, "dataexport", x.ID, "Approve a full export of the firm's data",
		fmt.Sprintf("%s asked for a full export of all the firm's data (%s). A second person, the compliance officer or another Admin, approves it under Admin → Data & exit.", actor, firstOf(x.Reason, "no reason given")),
		"", RoleCompliance, "dataexit", "")
	return x.view(), nil
}

// DecideDataExport approves (starting the build) or rejects an export.
// The person who asked cannot approve it.
func (o *Ops) DecideDataExport(ctx context.Context, tenant, actor, id string, approve bool, note string) (DataExport, error) {
	if err := o.require(ctx, tenant, actor, "Approving a full data export", RoleCompliance); err != nil {
		return DataExport{}, err
	}
	x, _, err := docs.Get[DataExport](ctx, o.Docs, tenant, cDataExport, id)
	if err != nil {
		return x, err
	}
	if x.State != DXWaiting {
		return x.view(), fmt.Errorf("%w: export %s is %s", ErrState, id, strings.ToLower(x.State))
	}
	if x.RequestedBy == actor {
		return x.view(), fmt.Errorf("%w: a second person approves an export, not the one who asked for it", ErrForbidden)
	}
	note = clip(strings.TrimSpace(note), 300)
	if !approve && note == "" {
		return x.view(), invalid("give the reason for rejecting")
	}
	now := o.now().Format(time.RFC3339)
	x, err = docs.Update(ctx, o.Docs, tenant, cDataExport, id, actor, func(v *DataExport) error {
		if v.State != DXWaiting {
			return fmt.Errorf("%w: export %s is %s", ErrState, id, strings.ToLower(v.State))
		}
		if approve {
			v.State, v.ApprovedBy, v.ApprovedAt = DXBuilding, actor, now
		} else {
			v.State, v.RejectedBy = DXRejected, actor
		}
		v.Note = note
		return nil
	})
	if err != nil {
		return x, err
	}
	o.closeWork(ctx, tenant, actor, "dataexport", id, map[bool]string{true: "approved", false: "rejected"}[approve])
	if !approve {
		o.log(ctx, tenant, actor, "dataexport.rejected", id, map[string]any{"note": note})
		return x.view(), nil
	}
	o.log(ctx, tenant, actor, "dataexport.approved", id, map[string]any{"requested_by": x.RequestedBy})
	o.startExport(ctx, tenant, id)
	return x.view(), nil
}

func (o *Ops) exportRunning(tenant, id string) bool {
	o.dxMu.Lock()
	defer o.dxMu.Unlock()
	return o.dxRunning[tenant+"|"+id]
}

// startExport builds an approved export, in the background unless
// ExportSync is set.
func (o *Ops) startExport(ctx context.Context, tenant, id string) {
	o.dxMu.Lock()
	if o.dxRunning == nil {
		o.dxRunning = map[string]bool{}
	}
	if o.dxRunning[tenant+"|"+id] {
		o.dxMu.Unlock()
		return
	}
	o.dxRunning[tenant+"|"+id] = true
	o.dxMu.Unlock()
	run := func(ctx context.Context) {
		defer func() { o.dxMu.Lock(); delete(o.dxRunning, tenant+"|"+id); o.dxMu.Unlock() }()
		o.buildExport(ctx, tenant, id)
	}
	if o.ExportSync {
		run(ctx)
		return
	}
	go run(context.WithoutCancel(ctx))
}

// buildExport writes the export's file and records the result.
func (o *Ops) buildExport(ctx context.Context, tenant, id string) {
	start := o.now()
	x, _ := docs.Update(ctx, o.Docs, tenant, cDataExport, id, SystemActor, func(v *DataExport) error {
		v.State, v.StartedAt, v.Error = DXBuilding, start.Format(time.RFC3339), ""
		return nil
	})
	m, size, err := o.writeExportFile(ctx, tenant, x)
	done := o.now()
	x, uerr := docs.Update(ctx, o.Docs, tenant, cDataExport, id, SystemActor, func(v *DataExport) error {
		v.FinishedAt = done.Format(time.RFC3339)
		if err != nil {
			v.State, v.Error = DXFailed, err.Error()
			return nil
		}
		v.State, v.Size, v.Entries = DXReady, size, len(m.Manifest.Files)+2
		v.ManifestSHA256, v.PublicKey = m.ManifestSHA256, m.Manifest.Signature.PublicKey
		v.SignedWith = m.Manifest.Signature.Signer
		if m.Manifest.Signature.Method == "cms" {
			v.SignedWith = "Digital signature: " + m.Manifest.Signature.Signer
		}
		v.Documents, v.Collections, v.StoredFiles, v.AuditEntries = 0, len(m.Manifest.Collections), m.Manifest.StoredFiles, m.Manifest.Audit.Entries
		for _, n := range m.Manifest.Collections {
			v.Documents += n
		}
		v.KeepUntil = done.AddDate(0, 0, DataExportKeepDays).Format(time.RFC3339)
		return nil
	})
	if uerr != nil {
		return
	}
	if err != nil {
		o.log(ctx, tenant, SystemActor, "dataexport.failed", id, map[string]any{"error": err.Error()})
		o.raiseWork(ctx, tenant, "dataexport", id+":failed", "The full data export failed",
			"Export "+id+" could not be built: "+err.Error()+". Fix the cause and ask for a new export under Admin → Data & exit.", "", RoleAdmin, "dataexit", "")
		return
	}
	o.log(ctx, tenant, SystemActor, "dataexport.ready", id, map[string]any{"bytes": size, "files": x.Entries, "documents": x.Documents, "stored_files": x.StoredFiles,
		"manifest_sha256": x.ManifestSHA256, "signed_with": x.SignedWith, "final": x.Final})
	title := "Download the full data export"
	if x.Final {
		title = "Download the final export before the firm's data is deleted"
		o.exitExportReady(ctx, tenant, x)
	}
	o.raiseWork(ctx, tenant, "dataexport", id+":ready", title,
		fmt.Sprintf("Export %s is ready (%s). An Admin downloads it under Admin → Data & exit; the file is kept until %s.", id, humanBytes(size), x.KeepUntil[:10]),
		"", RoleAdmin, "dataexit", "")
}

type builtExport struct {
	Manifest       dataexport.Manifest
	ManifestSHA256 string
}

// writeExportFile streams the archive into the firm's export folder,
// encrypted. The file gets its final name only once it is complete.
func (o *Ops) writeExportFile(ctx context.Context, tenant string, x DataExport) (builtExport, int64, error) {
	s := o.exportSealer()
	if s == nil || o.App.Snap == nil {
		return builtExport{}, 0, errors.New("full exports need the server's data keys")
	}
	path, err := o.exportPath(tenant, x.ID)
	if err != nil {
		return builtExport{}, 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return builtExport{}, 0, err
	}
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return builtExport{}, 0, err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	sw, err := dataexport.NewSealedWriter(bw, s, tenant, "export:"+x.ID)
	var m builtExport
	if err == nil {
		m, err = o.writeExportArchive(ctx, tenant, x, sw, filepath.Dir(path))
	}
	if err == nil {
		err = sw.Close()
	}
	if err == nil {
		err = bw.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return builtExport{}, 0, err
	}
	return m, sw.Size(), nil
}

// The CSV files made from document collections, in the order written.
var exportCSVs = []struct{ coll, name string }{
	{cClient, "clients"},
	{cHolding, "holdings"},
	{cNote, "contract-notes-and-bills"},
	{cReceipt, "receipts"},
	{cPayout, "payments"},
	{cDPTxn, "dp-transactions"},
	{cDPBill, "dp-bills"},
}

// snapDoc reads a document row from either store's snapshot.
type snapDoc struct {
	Coll       string          `json:"coll"`       // PostgreSQL
	Collection string          `json:"collection"` // memory
	ID         string          `json:"id"`
	Version    int64           `json:"version"`
	Body       json.RawMessage `json:"body"`
	UpdatedAt  string          `json:"updated_at"`
	UpdatedBy  string          `json:"updated_by"`
}

// docLine is one line of a collection file.
type docLine struct {
	ID        string          `json:"id"`
	Version   int64           `json:"version"`
	UpdatedAt string          `json:"updated_at"`
	UpdatedBy string          `json:"updated_by"`
	Doc       json.RawMessage `json:"doc"`
}

// openSnapDoc decrypts a document read from a snapshot.
func (o *Ops) openSnapDoc(tenant string, d snapDoc) (string, json.RawMessage, error) {
	coll := firstOf(d.Coll, d.Collection)
	b := bytes.TrimSpace(d.Body)
	if len(b) > 0 && b[0] == '"' {
		vd, ok := o.Docs.(*vault.Docs)
		if !ok {
			return coll, nil, fmt.Errorf("document %s/%s is encrypted and this server has no data keys", coll, d.ID)
		}
		x, err := vd.Decrypt(tenant, docs.Doc{Collection: coll, ID: d.ID, Body: b})
		if err != nil {
			return coll, nil, err
		}
		b = x.Body
	}
	return coll, b, nil
}

// snapAudit reads an audit row from either store's snapshot.
type snapAudit struct {
	ID       int64           `json:"id"`  // PostgreSQL
	Seq      int64           `json:"seq"` // memory
	At       time.Time       `json:"at"`
	Actor    string          `json:"actor"`
	Action   string          `json:"action"`
	Object   string          `json:"object"`
	Detail   json.RawMessage `json:"detail"`
	Prev     string          `json:"prev"`
	PrevHash string          `json:"prev_hash"`
	Hash     string          `json:"hash"`
}

// auditLine is one line of the exported audit trail: the fields the seal
// covers, the link and the seal.
type auditLine struct {
	Seq    int64           `json:"seq"`
	At     string          `json:"at"`
	Actor  string          `json:"actor"`
	Action string          `json:"action"`
	Object string          `json:"object"`
	Detail json.RawMessage `json:"detail,omitempty"`
	Prev   string          `json:"prev"`
	Hash   string          `json:"hash"`
}

// spool is a scratch file of one collection's documents, encrypted like
// everything else, read back twice to make its CSV (once for the columns,
// once for the rows).
type spool struct {
	path string
	f    *os.File
	bw   *bufio.Writer
	sw   *dataexport.SealedWriter
	n    int
}

func (o *Ops) newSpool(dir, tenant, label string) (*spool, error) {
	f, err := os.CreateTemp(dir, ".spool-*")
	if err != nil {
		return nil, err
	}
	bw := bufio.NewWriterSize(f, 1<<16)
	sw, err := dataexport.NewSealedWriter(bw, o.exportSealer(), tenant, label)
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return &spool{path: f.Name(), f: f, bw: bw, sw: sw}, nil
}

func (s *spool) add(id string, doc json.RawMessage) error {
	b, err := json.Marshal(docLine{ID: id, Doc: doc})
	if err != nil {
		return err
	}
	s.n++
	_, err = s.sw.Write(append(b, '\n'))
	return err
}

func (s *spool) done() error {
	if err := s.sw.Close(); err != nil {
		return err
	}
	if err := s.bw.Flush(); err != nil {
		return err
	}
	return s.f.Close()
}

// each reads the spool's documents back.
func (s *spool) each(o *Ops, tenant, label string, fn func(id string, doc map[string]any) error) error {
	f, err := os.Open(s.path)
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := dataexport.NewSealedReader(f, o.exportSealer(), tenant, label)
	if err != nil {
		return err
	}
	br := bufio.NewReaderSize(r, 1<<16)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var l docLine
			if jerr := json.Unmarshal(line, &l); jerr != nil {
				return jerr
			}
			var m map[string]any
			dec := json.NewDecoder(bytes.NewReader(l.Doc))
			dec.UseNumber()
			if jerr := dec.Decode(&m); jerr != nil {
				m = map[string]any{"value": string(l.Doc)}
			}
			if ferr := fn(l.ID, m); ferr != nil {
				return ferr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// csvCell writes one value: text as it is, numbers and true/false as
// written, anything nested as JSON; never as a spreadsheet formula.
func csvCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return csvSafe(x)
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(x)
		return csvSafe(string(b))
	}
}

// structColumns lists a struct's JSON field names, in order.
func structColumns(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			out = append(out, structColumns(f.Type)...)
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		out = append(out, firstOf(name, f.Name))
	}
	return out
}

// exportWriter is the archive with a buffered writer on its current file.
type exportWriter struct {
	ar  *dataexport.Archive
	bw  *bufio.Writer
	cur string
}

func (e *exportWriter) to(name, kind string) error {
	if e.cur == name && e.bw != nil {
		return nil
	}
	if err := e.flush(); err != nil {
		return err
	}
	w, err := e.ar.Create(name, kind)
	if err != nil {
		return err
	}
	e.bw, e.cur = bufio.NewWriterSize(w, 1<<16), e.ar.Current()
	if e.cur != name {
		e.cur = name // a renamed duplicate keeps receiving this name's lines
	}
	return nil
}

func (e *exportWriter) flush() error {
	if e.bw == nil {
		return nil
	}
	err := e.bw.Flush()
	e.bw = nil
	return err
}

func (e *exportWriter) line(v any) error {
	var b []byte
	switch x := v.(type) {
	case json.RawMessage:
		b = bytes.TrimSpace(x)
	default:
		var err error
		if b, err = json.Marshal(v); err != nil {
			return err
		}
	}
	if _, err := e.bw.Write(b); err != nil {
		return err
	}
	e.ar.Count(1)
	return e.bw.WriteByte('\n')
}

// file writes a whole file as its own entry and returns the path it got.
func (e *exportWriter) file(name, kind string, b []byte, records int) (string, error) {
	if err := e.flush(); err != nil {
		return "", err
	}
	w, err := e.ar.Create(name, kind)
	if err != nil {
		return "", err
	}
	e.cur = ""
	if _, err := w.Write(b); err != nil {
		return "", err
	}
	e.ar.Count(records)
	return e.ar.Current(), nil
}

// csvFile starts a CSV entry and returns its writer.
func (e *exportWriter) csv(name string) (*csv.Writer, error) {
	if err := e.to(name, "csv"); err != nil {
		return nil, err
	}
	e.cur = ""
	return csv.NewWriter(e.bw), nil
}

// writeExportArchive writes the whole archive into w. dir is where scratch
// files go.
func (o *Ops) writeExportArchive(ctx context.Context, tenant string, x DataExport, w io.Writer, dir string) (builtExport, error) {
	set, _ := o.Settings(ctx, tenant)
	now := o.now()
	ew := &exportWriter{ar: dataexport.NewArchive(w, now)}
	m := dataexport.Manifest{ExportID: x.ID, Tenant: tenant, Firm: set.Firm, FirmCode: firmCode(set), Created: now.Format(time.RFC3339), LedgerlineVersion: control.AppVersion,
		Backend: o.App.Snap.Backend(), Final: x.Final, RequestedBy: x.RequestedBy, ApprovedBy: x.ApprovedBy, Collections: map[string]int{}, Tables: map[string]int{}}
	spools := map[string]*spool{}
	defer func() {
		for _, s := range spools {
			s.f.Close()
			os.Remove(s.path)
		}
	}()
	csvColl := map[string]bool{}
	for _, c := range exportCSVs {
		csvColl[c.coll] = true
	}
	type fileIndex struct {
		ID         string `json:"id"`
		Path       string `json:"path"`
		Source     string `json:"source"`
		Name       string `json:"name"`
		SHA256     string `json:"sha256"`
		Size       int64  `json:"size"`
		ReceivedAt string `json:"received_at"`
		PriorID    string `json:"prior_id,omitempty"`
	}
	var index []fileIndex
	ledgerFrom, ledgerTo := "", ""
	tradeDates := map[string]bool{}
	hashVersions := map[string]bool{}
	err := o.App.Snap.Snapshot(ctx, tenant, func(r app.Record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.Tables[r.Table]++
		switch r.Table {
		case "doc", "doc_history":
			var d snapDoc
			if err := json.Unmarshal(r.Row, &d); err != nil {
				return fmt.Errorf("%s row: %w", r.Table, err)
			}
			coll, body, err := o.openSnapDoc(tenant, d)
			if err != nil {
				return err
			}
			if r.Table == "doc_history" {
				if err := ew.to("history/document-versions.jsonl", "history"); err != nil {
					return err
				}
				m.DocumentVersions++
				return ew.line(struct {
					Collection string `json:"collection"`
					docLine
				}{coll, docLine{ID: d.ID, Version: d.Version, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy, Doc: body}})
			}
			if err := ew.to("collections/"+coll+".jsonl", "collection"); err != nil {
				return err
			}
			m.Collections[coll]++
			if csvColl[coll] {
				sp := spools[coll]
				if sp == nil {
					if sp, err = o.newSpool(dir, tenant, "spool:"+x.ID+":"+coll); err != nil {
						return err
					}
					spools[coll] = sp
				}
				if err := sp.add(d.ID, body); err != nil {
					return err
				}
			}
			return ew.line(docLine{ID: d.ID, Version: d.Version, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy, Doc: body})
		case "raw_file":
			var h struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(r.Row, &h); err != nil || h.ID == "" {
				return fmt.Errorf("raw_file row without an id")
			}
			f, err := o.App.Ingest.Raw.Get(ctx, tenant, h.ID)
			if err != nil {
				return fmt.Errorf("file %s: %w", h.ID, err)
			}
			sum := sha256.Sum256(f.Content)
			if got := hex.EncodeToString(sum[:]); f.SHA256 != "" && !strings.EqualFold(got, f.SHA256) {
				return fmt.Errorf("file %s does not match its recorded SHA-256", h.ID)
			}
			name := fmt.Sprintf("files/%s/%s_%s", firstOf(dataexport.CleanPath(f.Source), "other"), f.ID, dataexport.CleanPath(filepath.Base(firstOf(f.Name, "file"))))
			path, err := ew.file(name, "file", f.Content, 0)
			if err != nil {
				return err
			}
			index = append(index, fileIndex{ID: f.ID, Path: path, Source: f.Source, Name: f.Name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(f.Content)),
				ReceivedAt: f.ReceivedAt.UTC().Format(time.RFC3339), PriorID: f.PriorID})
			m.StoredFiles++
			m.StoredBytes += int64(len(f.Content))
			return nil
		case "audit_log":
			var a snapAudit
			if err := json.Unmarshal(r.Row, &a); err != nil {
				return fmt.Errorf("audit row: %w", err)
			}
			if err := ew.to("audit/audit-trail.jsonl", "audit"); err != nil {
				return err
			}
			l := auditLine{Seq: max(a.ID, a.Seq), At: a.At.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), Actor: a.Actor, Action: a.Action, Object: a.Object,
				Prev: firstOf(a.Prev, a.PrevHash), Hash: a.Hash}
			if d := bytes.TrimSpace(a.Detail); len(d) > 0 && string(d) != "null" {
				l.Detail = d
			}
			if v, _, ok := strings.Cut(a.Hash, ":"); ok {
				hashVersions[v] = true
			}
			return ew.line(l)
		default:
			if err := ew.to("tables/"+r.Table+".jsonl", "table"); err != nil {
				return err
			}
			var h struct {
				ValueDate string `json:"value_date"`
				TradeDate string `json:"trade_date"`
			}
			if r.Table == "journal_entry" || r.Table == "trade_event" {
				_ = json.Unmarshal(r.Row, &h)
			}
			if d := h.ValueDate; len(d) >= 10 {
				d = d[:10]
				if ledgerFrom == "" || d < ledgerFrom {
					ledgerFrom = d
				}
				if d > ledgerTo {
					ledgerTo = d
				}
			}
			if len(h.TradeDate) >= 10 {
				tradeDates[h.TradeDate[:10]] = true
			}
			return ew.line(json.RawMessage(r.Row))
		}
	})
	if err != nil {
		return builtExport{}, err
	}
	// The stored files' index.
	if err := ew.to("files/index.jsonl", "index"); err != nil {
		return builtExport{}, err
	}
	for _, f := range index {
		if err := ew.line(f); err != nil {
			return builtExport{}, err
		}
	}
	// CSV files of the collections people open in a spreadsheet: the
	// columns are every field any document of the collection has.
	for _, c := range exportCSVs {
		sp := spools[c.coll]
		if sp == nil {
			continue
		}
		if err := sp.done(); err != nil {
			return builtExport{}, err
		}
		label := "spool:" + x.ID + ":" + c.coll
		cols, seen := []string{}, map[string]bool{}
		if err := sp.each(o, tenant, label, func(_ string, d map[string]any) error {
			for _, k := range sortedKeys(d) {
				if !seen[k] {
					seen[k] = true
					cols = append(cols, k)
				}
			}
			return nil
		}); err != nil {
			return builtExport{}, err
		}
		cw, err := ew.csv("csv/" + c.name + ".csv")
		if err != nil {
			return builtExport{}, err
		}
		_ = cw.Write(append([]string{"id"}, cols...))
		if err := sp.each(o, tenant, label, func(id string, d map[string]any) error {
			row := make([]string, 0, len(cols)+1)
			row = append(row, csvSafe(id))
			for _, k := range cols {
				row = append(row, csvCell(d[k]))
			}
			ew.ar.Count(1)
			return cw.Write(row)
		}); err != nil {
			return builtExport{}, err
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			return builtExport{}, err
		}
	}
	// Ledger entries, one row per posting, a month at a time.
	if ledgerFrom != "" {
		cw, err := ew.csv("csv/ledger-entries.csv")
		if err != nil {
			return builtExport{}, err
		}
		_ = cw.Write([]string{"entry_id", "seq", "value_date", "narration", "source_kind", "source_id", "source_line", "reversal_of", "created_by", "created_at", "line", "account", "debit", "credit", "memo"})
		for mo := ledgerFrom[:7] + "-01"; mo <= ledgerTo; {
			next := addMonths(mo, 1)
			if next <= mo {
				return builtExport{}, fmt.Errorf("ledger dates cannot be read (%s)", mo)
			}
			es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{From: mo, To: addDays(next, -1)})
			mo = next
			if err != nil {
				return builtExport{}, err
			}
			for _, e := range es {
				for i, p := range e.Postings {
					_ = cw.Write([]string{e.ID, strconv.FormatInt(e.Seq, 10), e.ValueDate, csvSafe(e.Narration), e.Source.Kind, csvSafe(e.Source.ID), strconv.Itoa(e.Source.Line), e.ReversalOf,
						csvSafe(e.CreatedBy), e.CreatedAt.UTC().Format(time.RFC3339), strconv.Itoa(i + 1), p.Account, p.Debit.String(), p.Credit.String(), csvSafe(p.Memo)})
					ew.ar.Count(1)
				}
			}
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			return builtExport{}, err
		}
	}
	// Trades, a day at a time.
	if len(tradeDates) > 0 {
		cw, err := ew.csv("csv/trades.csv")
		if err != nil {
			return builtExport{}, err
		}
		cols := structColumns(reflect.TypeOf(ingest.TradeEvent{}))
		_ = cw.Write(cols)
		for _, d := range sortedKeys(tradeDates) {
			evs, err := o.App.Trades.List(ctx, tenant, "", d)
			if err != nil {
				return builtExport{}, err
			}
			for _, e := range evs {
				b, _ := json.Marshal(e)
				var mm map[string]any
				dec := json.NewDecoder(bytes.NewReader(b))
				dec.UseNumber()
				_ = dec.Decode(&mm)
				row := make([]string, len(cols))
				for i, k := range cols {
					row[i] = csvCell(mm[k])
				}
				_ = cw.Write(row)
				ew.ar.Count(1)
			}
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			return builtExport{}, err
		}
	}
	// The audit trail as checked now, and for the final export the keys
	// its seals are made with.
	m.Audit.Entries = m.Tables["audit_log"]
	if va := o.vaultAudit(); va != nil {
		if chk, err := va.VerifyAudit(ctx, tenant); err == nil {
			m.Audit.Checked, m.Audit.OK, m.Audit.Sealed, m.Audit.Unsealed, m.Audit.Problems = true, chk.OK, chk.Sealed, chk.Unsealed, len(chk.Problems)
			m.Audit.LastHash, m.Audit.First, m.Audit.Last = chk.LastHash, chk.First, chk.Last
		}
		if x.Final && len(hashVersions) > 0 {
			keys := map[string]string{}
			for v := range hashVersions {
				n, err := strconv.Atoi(v)
				if err != nil {
					continue
				}
				k, err := va.V.Keys().Key(tenant, "audit", n)
				if err != nil {
					return builtExport{}, fmt.Errorf("audit seal key %d: %w", n, err)
				}
				keys[v] = hex.EncodeToString(k)
			}
			b, _ := json.MarshalIndent(map[string]any{"algorithm": "HMAC-SHA256", "keys": keys,
				"about": "The keys the audit trail's seals are made with, by key version (the number before ':' in each hash). They are given only in the export made for closing the account: once the firm's data is deleted they protect nothing else. See README.txt for how to check a seal."}, "", "  ")
			if _, err := ew.file("audit/seal-keys.json", "keys", append(b, '\n'), len(keys)); err != nil {
				return builtExport{}, err
			}
			m.Audit.SealKeys = true
		}
	}
	// Archived periods are the firm's data too: each archive package goes
	// in as archives/<id>.zip, a signed ZIP of its own (archivepkg.go).
	if n, err := o.exportArchives(ctx, tenant, ew); err != nil {
		return builtExport{}, err
	} else if n > 0 {
		m.Tables["archive_package"] = n
	}
	readme := exportReadme(set, m, x)
	if _, err := ew.file("README.txt", "readme", []byte(readme), 0); err != nil {
		return builtExport{}, err
	}
	if err := ew.flush(); err != nil {
		return builtExport{}, err
	}
	// Sign the manifest with the firm's document-signing certificate when
	// one is set up; otherwise (or if it fails) with a key made for this
	// export.
	var mb []byte
	if fn, info, err := o.docSigner(ctx, tenant); err == nil {
		si := dataexport.SignatureInfo{Method: "cms", File: dataexport.ManifestName + ".p7s", Signer: info.Name, CertificateSHA256: info.SHA256}
		mb, err = ew.ar.Finish(m, si, func(b []byte) ([]byte, error) { return fn(b, now) })
		if err == nil {
			m.Signature = si
		}
	}
	if mb == nil {
		si, fn := dataexport.Ed25519Signer()
		var err error
		if mb, err = ew.ar.Finish(m, si, fn); err != nil {
			return builtExport{}, err
		}
		m.Signature = si
	}
	m.Files = ew.ar.Files()
	return builtExport{Manifest: m, ManifestSHA256: dataexport.ManifestSHA256(mb)}, nil
}

// firmCode is how the certificate and manifest name the firm: its SEBI
// registration and member codes.
func firmCode(set Settings) string {
	var p []string
	if set.SEBIReg != "" {
		p = append(p, "SEBI "+set.SEBIReg)
	}
	for _, k := range sortedKeys(set.MemberCodes) {
		if v := set.MemberCodes[k]; v != "" {
			p = append(p, k+" "+v)
		}
	}
	return strings.Join(p, " · ")
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

func exportReadme(set Settings, m dataexport.Manifest, x DataExport) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	w("FULL EXPORT OF %s FROM LEDGERLINE", strings.ToUpper(firstOf(set.Firm, m.Tenant)))
	w("")
	w("Export %s, made %s by Ledgerline %s (%s storage).", m.ExportID, m.Created, m.LedgerlineVersion, m.Backend)
	w("Asked for by %s, approved by %s.", x.RequestedBy, firstOf(x.ApprovedBy, "-"))
	if m.Final {
		w("This is the final export, made before the firm's account was closed and its data deleted.")
	}
	w("")
	w("WHAT IS IN IT")
	w("")
	w("collections/<name>.jsonl  Every document of every collection, decrypted. One JSON object per line:")
	w("                          id, version, updated_at, updated_by, and doc (the document itself).")
	w("history/document-versions.jsonl  Every earlier version of every document, with its collection")
	w("                          (kept by the PostgreSQL store only).")
	w("tables/<name>.jsonl       The other tables row by row as stored: journal entries and postings (amounts")
	w("                          in paise in PostgreSQL, rupees as text in memory), accounts, trades,")
	w("                          reconciliation runs and breaks, charge schedules.")
	w("files/<kind>/<id>_<name>  Every stored file with its original bytes: KYC documents, exchange,")
	w("                          depository and bank files, contract notes, reports. files/index.jsonl")
	w("                          lists each with its kind, original name, size, SHA-256 and when it came in.")
	w("csv/*.csv                 Spreadsheet copies of the main tables: clients, holdings, contract notes and")
	w("                          bills, receipts, payments, DP transactions, DP bills (one column per field;")
	w("                          nested values as JSON), ledger entries (one row per posting) and trades.")
	w("                          The JSON Lines files are the record; the CSV files are a convenience.")
	w("audit/audit-trail.jsonl   The audit trail, oldest first, with each entry's seal (hash) and the seal")
	w("                          of the entry before it (prev).")
	if m.Audit.SealKeys {
		w("audit/seal-keys.json      The keys the seals are made with (final export only).")
	}
	if n := m.Tables["archive_package"]; n > 0 {
		w("archives/<id>.zip         The %d archived periods, each an archive package as kept in archive storage:", n)
		w("                          the period's rows as stored, with its own signed manifest and README.txt.")
	}
	w("manifest.json             Every file above with its size and SHA-256, the counts, the time and the")
	w("                          Ledgerline version.")
	w("manifest.json.p7s or .sig The manifest's signature.")
	w("")
	w("CHECKING THE EXPORT")
	w("")
	w("1. Check the manifest's signature.")
	w("   manifest.json.p7s is a CMS (PKCS #7) detached signature with the firm's document-signing")
	w("   certificate, for example:  openssl cms -verify -binary -inform DER -in manifest.json.p7s")
	w("   -content manifest.json -noverify -out /dev/null   (add -CAfile to check the certificate chain).")
	w("   manifest.json.sig is an Ed25519 signature over manifest.json's bytes. The public key is in")
	w("   manifest.json (signature.public_key, base64). Compare it with the key Ledgerline showed for this")
	w("   export on the Data & exit screen (and on the destruction certificate when the account was closed).")
	w("2. Check every file: its SHA-256 (sha256sum) and size must match its line in manifest.json.")
	w("   The SHA-256 of manifest.json itself is the export's hash, recorded by Ledgerline.")
	w("3. Check the audit trail. Each entry's hash is \"<key version>:<hex>\", the HMAC-SHA256 over the")
	w("   JSON array [\"ll-audit-v1\", tenant, at, actor, action, object, detail, prev] written without")
	w("   spaces, with detail's keys sorted (null when there is none), at as in the file, and tenant %q.", m.Tenant)
	w("   Each entry's prev is the hash of the entry before it. When the export was made Ledgerline checked")
	if m.Audit.Checked {
		w("   %d sealed entries: %s. The last seal is %s.", m.Audit.Sealed, map[bool]string{true: "all seals matched and every link held", false: fmt.Sprintf("%d problems were found", m.Audit.Problems)}[m.Audit.OK], firstOf(m.Audit.LastHash, "-"))
	} else {
		w("   nothing: this server keeps no seals.")
	}
	if !m.Audit.SealKeys {
		w("   The seal keys are given only in the final export made when the account is closed; until then")
		w("   the chain of links and the signed manifest show the trail is complete and unchanged.")
	}
	w("")
	w("Amounts in documents and CSV files are in rupees unless a field says paise. Dates are YYYY-MM-DD;")
	w("times are RFC 3339 (UTC unless an offset is shown).")
	return b.String()
}

// ---- download ----

// DXLinkView is a download link.
type DXLinkView struct {
	URL     string `json:"url"`
	Expires string `json:"expires"`
}

// DataExportLink makes a download link that works once, for the Admin who
// asks, for 15 minutes.
func (o *Ops) DataExportLink(ctx context.Context, tenant, actor, id string) (DXLinkView, error) {
	if err := o.require(ctx, tenant, actor, "Downloading a full data export", RoleAdmin); err != nil {
		return DXLinkView{}, err
	}
	x, _, err := docs.Get[DataExport](ctx, o.Docs, tenant, cDataExport, id)
	if err != nil {
		return DXLinkView{}, err
	}
	if x.State != DXReady {
		return DXLinkView{}, fmt.Errorf("%w: export %s is %s", ErrState, id, strings.ToLower(x.State))
	}
	tok := make([]byte, 24)
	_, _ = crand.Read(tok)
	t := hex.EncodeToString(tok)
	sum := sha256.Sum256([]byte(t))
	exp := o.now().Add(dataExportLinkLife)
	_, err = docs.Update(ctx, o.Docs, tenant, cDataExport, id, actor, func(v *DataExport) error {
		now := o.now()
		keep := v.Links[:0:0]
		for _, l := range v.Links { // drop spent and expired links
			if e, err := time.Parse(time.RFC3339, l.Expires); err == nil && e.After(now) && !l.Used {
				keep = append(keep, l)
			}
		}
		v.Links = append(keep, dxLink{Hash: hex.EncodeToString(sum[:]), By: actor, Expires: exp.Format(time.RFC3339)})
		return nil
	})
	if err != nil {
		return DXLinkView{}, err
	}
	o.log(ctx, tenant, actor, "dataexport.link", id, map[string]any{"expires": exp.Format(time.RFC3339)})
	return DXLinkView{URL: "/v1/data-exports/" + id + "/download?t=" + t, Expires: exp.Format(time.RFC3339)}, nil
}

// OpenDataExport checks a download link and opens the export for reading
// (decrypted as it is read). The link is spent; the download is recorded.
func (o *Ops) OpenDataExport(ctx context.Context, tenant, actor, id, token string) (DataExport, io.ReadCloser, error) {
	if err := o.require(ctx, tenant, actor, "Downloading a full data export", RoleAdmin); err != nil {
		return DataExport{}, nil, err
	}
	sum := sha256.Sum256([]byte(token))
	h := hex.EncodeToString(sum[:])
	now := o.now()
	x, err := docs.Update(ctx, o.Docs, tenant, cDataExport, id, actor, func(v *DataExport) error {
		if v.State != DXReady {
			return fmt.Errorf("%w: export %s is %s", ErrState, id, strings.ToLower(v.State))
		}
		for i, l := range v.Links {
			if l.Hash != h {
				continue
			}
			e, perr := time.Parse(time.RFC3339, l.Expires)
			if l.Used || perr != nil || !e.After(now) {
				return fmt.Errorf("%w: this download link has expired or was used; make a new one", ErrForbidden)
			}
			if l.By != actor {
				return fmt.Errorf("%w: this download link was made for another Admin", ErrForbidden)
			}
			v.Links[i].Used = true
			v.Downloads = append(v.Downloads, DXDownload{By: actor, At: now.Format(time.RFC3339)})
			return nil
		}
		return fmt.Errorf("%w: not a download link of this export", ErrForbidden)
	})
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			o.log(ctx, tenant, actor, "dataexport.download_refused", id, map[string]any{"reason": err.Error()})
		}
		return DataExport{}, nil, err
	}
	path, err := o.exportPath(tenant, id)
	if err != nil {
		return x, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return x, nil, fmt.Errorf("%w: the export's file is missing from the server: %v", ErrState, err)
	}
	r, err := dataexport.NewSealedReader(f, o.exportSealer(), tenant, "export:"+id)
	if err != nil {
		f.Close()
		return x, nil, err
	}
	o.log(ctx, tenant, actor, "dataexport.downloaded", id, map[string]any{"bytes": x.Size, "manifest_sha256": x.ManifestSHA256, "final": x.Final})
	o.closeWork(ctx, tenant, actor, "dataexport", id+":ready", "downloaded by "+actor)
	if x.Final {
		o.exitExportDownloaded(ctx, tenant, actor, x)
	}
	return x.view(), struct {
		io.Reader
		io.Closer
	}{r, f}, nil
}

// exportHousekeeping deletes export files past their keep date (not the
// final export of an exit still open) and files of exports that failed.
func (o *Ops) exportHousekeeping(ctx context.Context, tenant string, keepFinal string) {
	xs, err := docs.List[DataExport](ctx, o.Docs, tenant, cDataExport, "")
	if err != nil {
		return
	}
	now := o.now()
	for _, x := range xs {
		if x.State != DXReady || x.ID == keepFinal {
			continue
		}
		t, err := time.Parse(time.RFC3339, x.KeepUntil)
		if err != nil || now.Before(t) {
			continue
		}
		if p, err := o.exportPath(tenant, x.ID); err == nil {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		if _, err := docs.Update(ctx, o.Docs, tenant, cDataExport, x.ID, SystemActor, func(v *DataExport) error { v.State, v.Links = DXDeleted, nil; return nil }); err == nil {
			o.log(ctx, tenant, SystemActor, "dataexport.deleted", x.ID, map[string]any{"kept_until": x.KeepUntil})
			o.closeWork(ctx, tenant, SystemActor, "dataexport", x.ID+":ready", "the file was deleted after "+strconv.Itoa(DataExportKeepDays)+" days")
		}
	}
}

// sortExports puts the newest first.
func sortExports(xs []DataExport) {
	sort.Slice(xs, func(i, j int) bool { return xs[i].ID > xs[j].ID })
}
