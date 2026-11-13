package ops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
)

// ---- migration from another back office: upload and check ----
//
// The migration tool (cmd/ledgerline-migrate) reads the old back office's
// backups at the broker's office and writes one encrypted package (.llmig,
// see package pkg). Here the firm:
//
//  1. uploads it in numbered chunks (resumable; parts of a split package
//     one by one), streamed to the firm's folder on disk;
//  2. checks it: every file's checksum, the manifest, and a dry run of every
//     go-live file through the same validation a normal import uses, in a
//     sandbox, with every error reported at once;
//  3. proposes loading one of its firms (maker), which a second person
//     approves (checker); the load then runs (migload.go);
//  4. reconciles Ledgerline's figures with the old system's (migrecon.go).
//
// Earlier years go to a read-only history store (mighistory.go) and the
// archived documents to the document archive (migarchive.go).
//
// The passphrase is typed on the screen each time the package is opened
// (check, approve, resume). It is never stored or logged; the decrypted
// copy lives in a temporary file only while the package is open.

const (
	cMigPkg = "migpkg"
	cMigErr = "migerr"

	// MigMaxChunk is the largest upload chunk.
	MigMaxChunk  = 16 << 20
	migErrChunk  = 5000
	migDefaultGB = 100
)

// Package statuses.
const (
	MigUploading   = "Uploading"
	MigUploaded    = "Uploaded"
	MigChecking    = "Checking"
	MigChecked     = "Checked"
	MigCheckFailed = "Check failed"
	MigDiscarded   = "Discarded"
)

// MigPackage is one uploaded package (or one part of a split package).
type MigPackage struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Size         int64         `json:"size"`
	ChunkSize    int64         `json:"chunk_size"`
	Chunks       int           `json:"chunks"`
	Have         []int         `json:"have"`
	Status       string        `json:"status"`
	SHA256       string        `json:"sha256,omitempty"`
	UploadedBy   string        `json:"uploaded_by"`
	UploadedAt   time.Time     `json:"uploaded_at"`
	Manifest     *pkg.Manifest `json:"manifest,omitempty"`
	ManifestHash string        `json:"manifest_hash,omitempty"`
	// Key names the migration the package belongs to: "part:<Part.ID>" for
	// a split package, "sha:<manifest hash>" otherwise. A migration is
	// loaded into a firm once.
	Key   string    `json:"key,omitempty"`
	Check *MigCheck `json:"check,omitempty"`
}

// MigCheck is the result of checking a package.
type MigCheck struct {
	At       time.Time      `json:"at"`
	By       string         `json:"by"`
	Firm     string         `json:"firm,omitempty"` // the firm the dry run was for
	Error    string         `json:"error,omitempty"`
	Problems []string       `json:"problems,omitempty"` // damaged files (checksums)
	Files    []MigFileCheck `json:"files"`
	Errors   int            `json:"errors"`
	Warnings int            `json:"warnings"`
	Chunks   int            `json:"error_chunks"`
}

// MigFileCheck is one file's line in the check.
type MigFileCheck struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Firm     string `json:"firm,omitempty"`
	Source   string `json:"source,omitempty"`
	FY       string `json:"fy,omitempty"`
	Rows     int    `json:"rows"`
	Bytes    int64  `json:"bytes"`
	Checked  bool   `json:"checked"` // dry-run for the firm chosen
	Errors   int    `json:"errors"`
	Warnings int    `json:"warnings"`
	Note     string `json:"note,omitempty"`
}

// MigError is one problem found by the check: which file, row and field,
// what is wrong and what to do.
type MigError struct {
	File     string `json:"file"`
	Source   string `json:"source,omitempty"`
	Line     int    `json:"line"`
	Field    string `json:"field,omitempty"`
	Message  string `json:"message"`
	Action   string `json:"action"`
	Severity string `json:"severity"` // Error, Warning
	// The client of a go-live row's problem, from the package's client
	// master (migfix.go), and whether the value is missing there.
	UCC          string `json:"ucc,omitempty"`
	Client       string `json:"client,omitempty"`
	ClientStatus string `json:"client_status,omitempty"`
	Blank        bool   `json:"blank,omitempty"`
}

type migErrDoc struct {
	Errors []MigError `json:"errors"`
}

// Missing lists the chunks not received yet.
func (p MigPackage) Missing() []int {
	have := map[int]bool{}
	for _, n := range p.Have {
		have[n] = true
	}
	out := []int{}
	for i := 0; i < p.Chunks; i++ {
		if !have[i] {
			out = append(out, i)
		}
	}
	return out
}

func (p MigPackage) chunkLen(n int) int64 {
	if n == p.Chunks-1 {
		return p.Size - int64(n)*p.ChunkSize
	}
	return p.ChunkSize
}

var reTenantDir = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
var reMigID = regexp.MustCompile(`^U-[0-9]{14}-[0-9a-f]{8}$`)

// migDir is the firm's migration folder in the work folder.
func (o *Ops) migDir(tenant string) (string, error) {
	if !reTenantDir.MatchString(tenant) {
		return "", invalid("bad firm id %q", tenant)
	}
	w := o.WorkDir
	if w == "" {
		w = filepath.Join(os.TempDir(), "ledgerline-work")
	}
	d := filepath.Join(w, tenant, "migration")
	if err := os.MkdirAll(filepath.Join(d, "tmp"), 0o700); err != nil {
		return "", err
	}
	return d, nil
}

func (o *Ops) migFile(tenant, id string) (string, error) {
	if !reMigID.MatchString(id) {
		return "", fmt.Errorf("%w: no such package", ErrNotFound)
	}
	d, err := o.migDir(tenant)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, id+".llmig"), nil
}

func (o *Ops) migMaxBytes(ctx context.Context, tenant string) int64 {
	set, _ := o.Settings(ctx, tenant)
	gb := set.MigrationMaxGB
	if gb <= 0 {
		gb = migDefaultGB
	}
	return gb << 30
}

// CreateMigUpload starts the upload of a package of size bytes, sent in
// chunks of chunk bytes (at most 16 MB; 0 for 16 MB).
func (o *Ops) CreateMigUpload(ctx context.Context, tenant, actor, name string, size, chunk int64) (MigPackage, error) {
	if err := o.require(ctx, tenant, actor, "Uploading a migration package", RoleAdmin); err != nil {
		return MigPackage{}, err
	}
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if !strings.HasSuffix(strings.ToLower(name), ".llmig") {
		return MigPackage{}, invalid("choose the .llmig file the migration tool made")
	}
	if max := o.migMaxBytes(ctx, tenant); size <= 0 || size > max {
		return MigPackage{}, invalid("the package is %s bytes; this firm accepts packages up to %d GB (Settings)", groupIN(size), max>>30)
	}
	if chunk == 0 {
		chunk = MigMaxChunk
	}
	if chunk < 1<<10 || chunk > MigMaxChunk {
		return MigPackage{}, invalid("chunks must be between 1 KB and 16 MB")
	}
	var rb [4]byte
	rand.Read(rb[:])
	p := MigPackage{ID: "U-" + o.now().Format("20060102150405") + "-" + hex.EncodeToString(rb[:]), Name: name, Size: size, ChunkSize: chunk,
		Chunks: int((size + chunk - 1) / chunk), Have: []int{}, Status: MigUploading, UploadedBy: actor, UploadedAt: o.now()}
	fn, err := o.migFile(tenant, p.ID)
	if err != nil {
		return p, err
	}
	f, err := os.OpenFile(fn, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return p, err
	}
	err = f.Truncate(size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(fn)
		return p, err
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cMigPkg, p.ID, p, 0, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "migration.upload.started", p.ID, map[string]any{"name": name, "size": size, "chunks": p.Chunks})
	return p, nil
}

// PutMigChunk writes chunk n (from 0) of an upload straight to disk. A
// chunk sent again replaces the earlier copy, so an interrupted upload is
// resumed by sending the missing chunks. sha, if given, is the chunk's
// SHA-256 in hex and is checked.
func (o *Ops) PutMigChunk(ctx context.Context, tenant, actor, id string, n int, body io.Reader, sha string) (MigPackage, error) {
	if err := o.require(ctx, tenant, actor, "Uploading a migration package", RoleAdmin); err != nil {
		return MigPackage{}, err
	}
	p, _, err := docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id)
	if err != nil {
		return p, fmt.Errorf("%w: no such upload", ErrNotFound)
	}
	if p.Status != MigUploading {
		return p, fmt.Errorf("%w: the upload is %s", ErrState, strings.ToLower(p.Status))
	}
	if n < 0 || n >= p.Chunks {
		return p, invalid("chunk %d is outside the upload (0 to %d)", n, p.Chunks-1)
	}
	want := p.chunkLen(n)
	fn, err := o.migFile(tenant, id)
	if err != nil {
		return p, err
	}
	f, err := os.OpenFile(fn, os.O_WRONLY, 0o600)
	if err != nil {
		return p, err
	}
	h := sha256.New()
	got, err := io.Copy(io.MultiWriter(io.NewOffsetWriter(f, int64(n)*p.ChunkSize), h), io.LimitReader(body, want+1))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return p, err
	}
	if got != want {
		return p, invalid("chunk %d must be %d bytes, got %d", n, want, got)
	}
	if sha != "" && !strings.EqualFold(sha, hex.EncodeToString(h.Sum(nil))) {
		return p, invalid("chunk %d arrived damaged (checksum differs); send it again", n)
	}
	o.migMu.Lock()
	defer o.migMu.Unlock()
	return docs.Update(ctx, o.Docs, tenant, cMigPkg, id, actor, func(x *MigPackage) error {
		i := sort.SearchInts(x.Have, n)
		if i == len(x.Have) || x.Have[i] != n {
			x.Have = append(x.Have, 0)
			copy(x.Have[i+1:], x.Have[i:])
			x.Have[i] = n
		}
		return nil
	})
}

// CompleteMigUpload finishes an upload once every chunk is in.
func (o *Ops) CompleteMigUpload(ctx context.Context, tenant, actor, id string) (MigPackage, error) {
	if err := o.require(ctx, tenant, actor, "Uploading a migration package", RoleAdmin); err != nil {
		return MigPackage{}, err
	}
	p, _, err := docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id)
	if err != nil {
		return p, fmt.Errorf("%w: no such upload", ErrNotFound)
	}
	if p.Status != MigUploading {
		return p, nil
	}
	if m := p.Missing(); len(m) > 0 {
		return p, invalid("%d of %d chunks have not arrived yet (first missing: %d)", len(m), p.Chunks, m[0])
	}
	fn, err := o.migFile(tenant, id)
	if err != nil {
		return p, err
	}
	f, err := os.Open(fn)
	if err != nil {
		return p, err
	}
	defer f.Close()
	head := make([]byte, len(pkg.Magic))
	if _, err := io.ReadFull(f, head); err != nil || string(head) != pkg.Magic {
		return p, invalid("this is not a Ledgerline migration package (.llmig)")
	}
	f.Seek(0, io.SeekStart)
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return p, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	p, err = docs.Update(ctx, o.Docs, tenant, cMigPkg, id, actor, func(x *MigPackage) error {
		x.Status, x.SHA256 = MigUploaded, sum
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "migration.uploaded", id, map[string]any{"name": p.Name, "size": p.Size, "sha256": sum})
	}
	return p, err
}

// DiscardMigPackage deletes an uploaded package from the server's disk.
func (o *Ops) DiscardMigPackage(ctx context.Context, tenant, actor, id string) (MigPackage, error) {
	if err := o.require(ctx, tenant, actor, "Uploading a migration package", RoleAdmin); err != nil {
		return MigPackage{}, err
	}
	ms, _ := docs.List[Migration](ctx, o.Docs, tenant, cMig, "")
	for _, m := range ms {
		if contains(m.Packages, id) && (m.Status == MigProposed || m.Status == MigLoading || m.Status == MigInterrupted || m.Status == MigUndoProposed || m.Status == MigUndoing) {
			return MigPackage{}, fmt.Errorf("%w: migration %s uses this package and is %s", ErrState, m.ID, strings.ToLower(m.Status))
		}
	}
	p, err := docs.Update(ctx, o.Docs, tenant, cMigPkg, id, actor, func(x *MigPackage) error {
		if x.Status == MigChecking {
			return fmt.Errorf("%w: the package is being checked", ErrState)
		}
		x.Status = MigDiscarded
		return nil
	})
	if err != nil {
		return p, err
	}
	if fn, err := o.migFile(tenant, id); err == nil {
		os.Remove(fn)
	}
	o.log(ctx, tenant, actor, "migration.discarded", id, map[string]any{"name": p.Name})
	return p, nil
}

// openMig decrypts a package into the firm's temporary folder. The caller
// closes the reader, which removes the decrypted copy.
func (o *Ops) openMig(ctx context.Context, tenant, id, passphrase string) (*pkg.Reader, error) {
	fn, err := o.migFile(tenant, id)
	if err != nil {
		return nil, err
	}
	d, _ := o.migDir(tenant)
	f, err := os.Open(fn)
	if err != nil {
		return nil, fmt.Errorf("the package file is no longer on the server; upload it again: %w", err)
	}
	defer f.Close()
	r, err := pkg.Open(f, passphrase, filepath.Join(d, "tmp"), o.migMaxBytes(ctx, tenant)+o.migMaxBytes(ctx, tenant)/4)
	if errors.Is(err, pkg.ErrPassphrase) {
		return nil, invalid("%v", err)
	}
	return r, err
}

// checkPassphrase answers "wrong passphrase" at once, before a long open.
func (o *Ops) checkPassphrase(tenant, id, passphrase string) error {
	if passphrase == "" {
		return invalid("type the package's passphrase")
	}
	fn, err := o.migFile(tenant, id)
	if err != nil {
		return err
	}
	f, err := os.Open(fn)
	if err != nil {
		return fmt.Errorf("the package file is no longer on the server; upload it again: %w", err)
	}
	defer f.Close()
	if err := pkg.CheckPassphrase(f, passphrase); err != nil {
		return invalid("%v", err)
	}
	return nil
}

// migGo runs fn now (tests, MigrateSync) or in the background. key marks
// the work as running in this server, so a load left "Loading" by a
// restart shows as interrupted.
func (o *Ops) migGo(key string, fn func()) bool {
	o.migMu.Lock()
	if o.migRun == nil {
		o.migRun = map[string]bool{}
	}
	if o.migRun[key] {
		o.migMu.Unlock()
		return false
	}
	o.migRun[key] = true
	o.migMu.Unlock()
	run := func() {
		defer func() {
			o.migMu.Lock()
			delete(o.migRun, key)
			o.migMu.Unlock()
		}()
		fn()
	}
	if o.MigrateSync {
		run()
	} else {
		go run()
	}
	return true
}

func (o *Ops) migRunning(key string) bool {
	o.migMu.Lock()
	defer o.migMu.Unlock()
	return o.migRun[key]
}

// CheckMigPackage opens the package with the passphrase and checks it
// without changing any data: checksums, manifest, and a dry run of the go-
// live files of one firm (firm empty: the first). It runs in the
// background; the package shows Checking until it is done.
func (o *Ops) CheckMigPackage(ctx context.Context, tenant, actor, id, passphrase, firm string) (MigPackage, error) {
	if err := o.require(ctx, tenant, actor, "Checking a migration package", RoleAdmin); err != nil {
		return MigPackage{}, err
	}
	p, _, err := docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id)
	if err != nil {
		return p, fmt.Errorf("%w: no such package", ErrNotFound)
	}
	switch p.Status {
	case MigUploaded, MigChecked, MigCheckFailed:
	case MigChecking:
		return p, fmt.Errorf("%w: the package is being checked", ErrState)
	default:
		return p, fmt.Errorf("%w: the package is %s", ErrState, strings.ToLower(p.Status))
	}
	if err := o.checkPassphrase(tenant, id, passphrase); err != nil {
		return p, err
	}
	p, err = docs.Update(ctx, o.Docs, tenant, cMigPkg, id, actor, func(x *MigPackage) error {
		x.Status = MigChecking
		return nil
	})
	if err != nil {
		return p, err
	}
	bg := context.WithoutCancel(ctx)
	o.migGo("check:"+tenant+":"+id, func() { o.runCheck(bg, tenant, actor, id, passphrase, firm) })
	p, _, err = docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id)
	return p, err
}

func manifestHash(m pkg.Manifest) string {
	b, _ := json.Marshal(m)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (o *Ops) runCheck(ctx context.Context, tenant, actor, id, passphrase, firm string) {
	chk := &MigCheck{At: o.now(), By: actor, Files: []MigFileCheck{}}
	var man *pkg.Manifest
	var errs []MigError
	finish := func() {
		status := MigChecked
		if chk.Error != "" || len(chk.Problems) > 0 {
			status = MigCheckFailed
		}
		chk.Chunks = 0
		for i := 0; i*migErrChunk < len(errs) || i == 0; i++ {
			part := errs[min(i*migErrChunk, len(errs)):min((i+1)*migErrChunk, len(errs))]
			if err := docs.Upsert(ctx, o.Docs, tenant, cMigErr, fmt.Sprintf("%s|%05d", id, i), migErrDoc{Errors: part}, actor); err != nil {
				chk.Error = firstOf(chk.Error, err.Error())
			}
			chk.Chunks = i + 1
		}
		for _, e := range errs {
			if e.Severity == "Warning" {
				chk.Warnings++
			} else {
				chk.Errors++
			}
		}
		docs.Update(ctx, o.Docs, tenant, cMigPkg, id, actor, func(x *MigPackage) error {
			x.Status, x.Check = status, chk
			if man != nil {
				x.Manifest, x.ManifestHash = man, manifestHash(*man)
				x.Key = "sha:" + x.ManifestHash
				if man.Part != nil && man.Part.ID != "" {
					x.Key = "part:" + man.Part.ID
				}
			}
			return nil
		})
		o.log(ctx, tenant, actor, "migration.checked", id, map[string]any{"status": status, "firm": chk.Firm, "errors": chk.Errors, "warnings": chk.Warnings, "problems": len(chk.Problems)})
	}
	defer func() {
		if p := recover(); p != nil {
			chk.Error = fmt.Sprintf("the check stopped unexpectedly: %v", p)
			finish()
		}
	}()
	r, err := o.openMig(ctx, tenant, id, passphrase)
	if err != nil {
		chk.Error = strings.TrimPrefix(err.Error(), "invalid: ")
		finish()
		return
	}
	defer r.Close()
	m := r.Manifest
	man = &m
	chk.Problems = r.VerifyAll()
	if firm == "" && len(m.Firms) > 0 {
		firm = m.Firms[0].Code
		for _, f := range m.Files {
			if f.Kind == pkg.KindMaster && f.Firm != "" {
				firm = f.Firm // a part with masters: the firm whose they are
				break
			}
		}
	}
	known := false
	for _, f := range m.Firms {
		known = known || f.Code == firm
	}
	if len(m.Firms) > 0 && !known {
		chk.Error = fmt.Sprintf("the package has no firm %q", firm)
		finish()
		return
	}
	chk.Firm = firm
	errs = o.checkFirm(ctx, tenant, actor, r, firm, chk)
	finish()
}

// checkFirm dry-runs one firm's files and returns every problem found.
func (o *Ops) checkFirm(ctx context.Context, tenant, actor string, r *pkg.Reader, firm string, chk *MigCheck) []MigError {
	m := r.Manifest
	var errs []MigError
	masters := []pkg.File{}
	idx := map[string]int{}
	for _, f := range m.Files {
		fc := MigFileCheck{Path: f.Path, Kind: f.Kind, Firm: f.Firm, Source: f.Source, FY: f.FY, Rows: f.Rows, Bytes: f.Bytes}
		idx[f.Path] = len(chk.Files)
		chk.Files = append(chk.Files, fc)
		if f.Firm == firm && f.Kind == pkg.KindMaster {
			masters = append(masters, f)
		}
	}
	add := func(path string, e MigError) {
		e.File = path
		if e.Severity == "" {
			e.Severity = "Error"
		}
		if e.Action == "" {
			e.Action = migAction(e.Message, e.Field)
		}
		errs = append(errs, e)
		if i, ok := idx[path]; ok {
			if e.Severity == "Warning" {
				chk.Files[i].Warnings++
			} else {
				chk.Files[i].Errors++
			}
		}
	}
	// Go-live files: a dry run in a sandbox, in load order.
	sortMasters(masters)
	asOf := ""
	for _, f := range m.Firms {
		if f.Code == firm {
			asOf = f.AsOf
		}
	}
	if !ledger.ValidDate(asOf) {
		asOf = o.now().Format("2006-01-02")
	}
	var dry []DryFile
	for _, f := range masters {
		if !IsMasterSource(f.Source) {
			add(f.Path, MigError{Source: f.Source, Message: f.Source + " is not a Ledgerline import file", Action: "Make the package again with a newer migration tool, or untick this file"})
			continue
		}
		data, err := r.ReadAll(f.Path, 2<<30)
		if err != nil {
			add(f.Path, MigError{Source: f.Source, Message: err.Error()})
			continue
		}
		dry = append(dry, DryFile{Name: f.Path, Source: f.Source, Date: o.migFileDate(ctx, tenant, f.Source, asOf), Data: data})
	}
	res, err := o.DryRunFiles(ctx, tenant, actor, dry)
	if err != nil {
		add("", MigError{Message: err.Error()})
	}
	rowClients, cm := migRowClients(dry)
	for _, x := range res {
		chk.Files[idx[x.Name]].Checked = true
		chk.Files[idx[x.Name]].Rows = x.Rows
		for _, e := range x.Errors {
			sev := ""
			if e.Warning {
				sev = "Warning"
			}
			me := MigError{Source: x.Source, Line: e.Line, Field: e.Field, Message: e.Msg, Severity: sev}
			tagMigError(&me, rowClients[x.Name], cm)
			add(x.Name, me)
		}
	}
	// One active trading code per individual client and PAN.
	for _, d := range dry {
		if d.Source != "CLIENT-MASTER" {
			continue
		}
		migActiveCodesPerPAN(cm, rowClients[d.Name], func(line int, u, other string) {
			e := MigError{Source: d.Source, Line: line, Field: "PAN", Severity: "Warning",
				Message: fmt.Sprintf("%s: the PAN is also on active client code %s; the exchanges allow one active code per individual client (NSE/ISC/48573)", u, other),
				Action:  "Keep one code active and mark the others inactive in the old system (or in Ledgerline after the load); trade only on the active code"}
			tagMigError(&e, rowClients[d.Name], cm)
			add(d.Name, e)
		})
	}
	// Clients the go-live files bring, for the history and document checks.
	clients := map[string]bool{}
	for _, d := range dry {
		if d.Source == "CLIENT-MASTER" {
			rows, _ := parseCSV(d.Data, []string{"UCC"})
			for _, rw := range rows {
				clients[strings.ToUpper(rw.get("UCC"))] = true
			}
		}
	}
	// History: layouts, dates and amounts.
	for _, f := range m.Files {
		if f.Firm != firm || f.Kind != pkg.KindHistory {
			continue
		}
		chk.Files[idx[f.Path]].Checked = true
		for _, e := range checkHistoryFile(r, f) {
			add(f.Path, e)
		}
	}
	// The document index.
	for _, f := range m.Files {
		if f.Firm != firm || f.Kind != pkg.KindDocIndex {
			continue
		}
		chk.Files[idx[f.Path]].Checked = true
		line := 1
		err := r.CSV(f.Path, func(h, row []string) error {
			line++
			d, problems := docRow(h, row)
			for _, p := range problems {
				add(f.Path, MigError{Line: line, Field: p[0], Message: p[1]})
			}
			if d.UCC != "" && len(clients) > 0 && !clients[strings.ToUpper(d.UCC)] {
				add(f.Path, MigError{Line: line, Field: "UCC", Message: d.UCC + " is not in the client master", Severity: "Warning",
					Action: "The document is archived all the same; it shows under the client once that client is in Ledgerline"})
			}
			return nil
		})
		if err != nil {
			add(f.Path, MigError{Message: err.Error()})
		}
	}
	return errs
}

// migFileDate is the date a go-live file is applied to: balances and lots
// are as at the as-of date; prices and positions are for the next
// business day, the first day on Ledgerline.
func (o *Ops) migFileDate(ctx context.Context, tenant, source, asOf string) string {
	var need bool
	for _, m := range MasterSources() {
		if m.Name == source {
			need = m.NeedDate
		}
	}
	switch {
	case !need:
		return ""
	case source == "OPENING-BALANCES" || source == "HOLDING-LOTS":
		return asOf
	}
	set, _ := o.Settings(ctx, tenant)
	return set.NextBusinessDay(asOf)
}

// migrationOrder is the order go-live files load in: the intake order,
// with the brokerage files where their own descriptions put them: after
// the client master (they name clients), scrip groups before the cards
// that use them, cards before the clients on them.
func migrationOrder() []string {
	var out []string
	for _, s := range intakeOrder {
		out = append(out, s)
		if s == "SEGMENT-STATUS" {
			out = append(out, "SCRIP-GROUPS", "BROKERAGE-CARDS", "BROKERAGE-CLIENTS", "BROKERAGE-CLIENT-RATES", "CLIENT-INTEREST", "TERMINAL-CHANNELS")
		}
	}
	return out
}

func migRank(source string) int {
	for i, s := range migrationOrder() {
		if s == source {
			return i
		}
	}
	return 1000
}

func sortMasters(fs []pkg.File) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := migRank(fs[i].Source), migRank(fs[j].Source)
		if a != b {
			return a < b
		}
		return fs[i].Source < fs[j].Source
	})
}

// DryFile is one go-live file for DryRunFiles.
type DryFile struct {
	Name   string
	Source string
	Date   string
	Data   []byte
}

// DryResult is what a dry run found in one file.
type DryResult struct {
	Name   string              `json:"name"`
	Source string              `json:"source"`
	Rows   int                 `json:"rows"`
	Errors []ingest.ParseError `json:"errors"` // warnings too (Warning set)
}

// DryRunFiles checks go-live files exactly as LoadFile would and reports
// every error in every file, changing nothing: the files are applied in
// order to a sandbox that reads the firm's data but keeps its writes in
// memory, so a later file is checked against what the earlier ones would
// have loaded (bank accounts against the client master, brokerage cards
// against scrip groups). Rows with errors are applied too where they can
// be, so one bad client does not make every later row for that client an
// error.
func (o *Ops) DryRunFiles(ctx context.Context, tenant, actor string, files []DryFile) ([]DryResult, error) {
	if err := o.require(ctx, tenant, actor, "Loading files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return nil, err
	}
	sb := o.sandbox()
	out := []DryResult{}
	for _, f := range files {
		res := DryResult{Name: f.Name, Source: f.Source, Errors: []ingest.ParseError{}}
		var spec MasterSource
		for _, s := range MasterSources() {
			if s.Name == f.Source {
				spec = s
			}
		}
		switch {
		case spec.Name == "":
			res.Errors = append(res.Errors, ingest.ParseError{Msg: "unknown file type " + f.Source})
		case spec.NeedDate && !ledger.ValidDate(f.Date):
			res.Errors = append(res.Errors, ingest.ParseError{Msg: f.Source + " needs the business date (YYYY-MM-DD)"})
		default:
			n, apply, errs, warns := sb.checkRows(ctx, tenant, actor, spec, f.Date, f.Data, true)
			res.Rows, res.Errors = n, append(res.Errors, errs...)
			res.Errors = append(res.Errors, warns...)
			if apply != nil {
				if err := sandboxApply(apply); err != nil && len(errs) == 0 {
					res.Errors = append(res.Errors, ingest.ParseError{Msg: "the rows are valid, but loading them would fail: " + strings.TrimPrefix(err.Error(), "invalid: ")})
				}
			}
		}
		out = append(out, res)
	}
	return out, nil
}

func sandboxApply(apply func() (int, int, error)) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%v", p)
		}
	}()
	_, _, err = apply()
	return err
}

// sandbox is a copy of Ops whose writes stay in memory: documents read
// through to the firm's, the ledger, files and audit start empty.
func (o *Ops) sandbox() *Ops {
	st := app.MemoryStores()
	st.Docs, st.Snap = docs.NewOverlay(o.Docs), nil
	a := app.New(st, o.App.Ingest.Registry, o.App.Schedule)
	a.Now = o.App.Now
	sb := New(a)
	sb.Now, sb.EditionOf, sb.DefaultEdition, sb.Residency, sb.DevCodes = o.Now, o.EditionOf, o.DefaultEdition, o.Residency, o.DevCodes
	return sb
}

// migAction suggests what to do about a problem.
func migAction(msg, field string) string {
	l := strings.ToLower(msg)
	switch {
	case strings.Contains(msg, incompleteNote):
		return "Nothing to do before the load: the inactive client comes across marked incomplete; complete the detail in Ledgerline before reactivating them"
	case strings.Contains(msg, inactiveAcctNote):
		return "Nothing to do: the closed account is kept for the record and never used"
	case strings.Contains(l, "is not a client") || strings.Contains(l, "load the client master first"):
		return "Check the client code; if the client is missing from the old system's client master, add or fix them there and make the package again"
	case strings.Contains(l, "load scrip-groups first") || strings.Contains(l, "no scrip group"):
		return "Add the scrip group to SCRIP-GROUPS (scrip group master in the old system) or correct the group code"
	case strings.Contains(l, "load brokerage-cards first") || strings.Contains(l, "no card"):
		return "Add the card to the old system's brokerage master or correct the card code, then make the package again"
	case strings.Contains(l, "yyyy-mm-dd") || strings.Contains(l, "date"):
		return "Correct the " + firstOf(field, "date") + " in the old system (a real date), then make the package again"
	case strings.Contains(l, "do not balance"):
		return "Compare the old system's trial balance as at the as-of date with this file; a ledger left out of the export is the usual cause"
	case strings.Contains(l, "appears twice"):
		return "Remove the duplicate in the old system (two records with the same code), then make the package again"
	case strings.Contains(l, "pan"):
		return "Correct the PAN in the old system's client master (5 letters, 4 digits, 1 letter)"
	case strings.Contains(l, "ifsc") || strings.Contains(l, "account"):
		return "Correct the bank details in the old system's client bank master"
	case strings.Contains(l, "is required") || strings.Contains(l, "is empty"):
		return "Fill in " + firstOf(field, "the field") + " in the old system, then make the package again"
	case strings.Contains(l, "header") || strings.Contains(l, "column"):
		return "The file's layout is not what Ledgerline expects; make the package again with the current migration tool"
	case strings.Contains(l, "checksum") || strings.Contains(l, "damaged"):
		return "Copy the package again from the office (the file was damaged on the way) and upload it again"
	case strings.Contains(l, "number") || strings.Contains(l, "rupees") || strings.Contains(l, "whole"):
		return "Correct the " + firstOf(field, "amount") + " in the old system, then make the package again"
	}
	return "Correct this record in the old system (or untick the file), then make the package again"
}

// historyDateCol and historyNumCols say what checkHistoryFile checks in
// each kind of history file.
var historyDateCol = map[string]string{pkg.HistoryLedger: "Date", pkg.HistoryBills: "BillDate", pkg.HistoryTrades: "TradeDate", pkg.HistoryContractNotes: "Date",
	pkg.HistoryReceipts: "Date", pkg.HistoryDP: "Date", pkg.HistoryDPBills: "BillDate", pkg.HistoryHoldings: "Date"}
var historyNumCols = map[string][]string{pkg.HistoryLedger: {"Debit", "Credit"}, pkg.HistoryBills: {"NetAmount"}, pkg.HistoryTrades: {"Quantity", "Price"},
	pkg.HistoryReceipts: {"Amount"}, pkg.HistoryDP: {"Quantity"}, pkg.HistoryDPBills: {"Total"}, pkg.HistoryHoldings: {"Total"}}

func checkHistoryFile(r *pkg.Reader, f pkg.File) []MigError {
	var out []MigError
	want, ok := pkg.HistoryColumns[f.Source]
	if !ok {
		return []MigError{{Source: f.Source, Message: f.Source + " is not a kind of history Ledgerline keeps; it is skipped", Severity: "Warning", Action: "Nothing to do"}}
	}
	line := 1
	var col map[string]int
	err := r.CSV(f.Path, func(h, row []string) error {
		line++
		if col == nil {
			col = map[string]int{}
			for i, c := range h {
				col[strings.ToLower(strings.TrimSpace(c))] = i
			}
			for _, c := range want {
				if _, ok := col[strings.ToLower(c)]; !ok {
					out = append(out, MigError{Source: f.Source, Line: 1, Field: c, Message: "header is missing column " + c})
				}
			}
		}
		get := func(c string) string {
			i, ok := col[strings.ToLower(c)]
			if !ok || i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		}
		if dc := historyDateCol[f.Source]; dc != "" {
			if v := get(dc); !ledger.ValidDate(v) {
				out = append(out, MigError{Source: f.Source, Line: line, Field: dc, Message: fmt.Sprintf("must be YYYY-MM-DD, got %q", v)})
			}
		}
		for _, c := range historyNumCols[f.Source] {
			if v := get(c); v != "" {
				if _, err := money.ParseDec(strings.ReplaceAll(v, ",", "")); err != nil {
					out = append(out, MigError{Source: f.Source, Line: line, Field: c, Message: fmt.Sprintf("must be a number, got %q", v)})
				}
			}
		}
		return nil
	})
	if err != nil {
		out = append(out, MigError{Source: f.Source, Line: line, Message: err.Error()})
	}
	return out
}

// docRow reads one row of a document index, with any problems as
// (field, message) pairs.
func docRow(h, row []string) (pkg.DocMeta, [][2]string) {
	var probs [][2]string
	get := func(c string) string {
		for i, x := range h {
			if strings.EqualFold(strings.TrimSpace(x), c) && i < len(row) {
				return strings.TrimSpace(row[i])
			}
		}
		return ""
	}
	d := pkg.DocMeta{ID: get("ID"), UCC: strings.ToUpper(get("UCC")), Type: get("Type"), FY: get("FY"), Subject: get("Subject"), To: get("To"),
		Signed: strings.EqualFold(get("Signed"), "Y"), Format: get("Format"), SourceRef: get("SourceRef")}
	if d.ID == "" {
		probs = append(probs, [2]string{"ID", "is empty"})
	}
	if get("Path") == "" || get("SHA256") == "" {
		probs = append(probs, [2]string{"Path", "the document's path and checksum are required"})
	}
	if v := get("Date"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			probs = append(probs, [2]string{"Date", fmt.Sprintf("must be YYYY-MM-DD, got %q", v)})
		}
		d.Date = t
	}
	if v := get("RetainUntil"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			probs = append(probs, [2]string{"RetainUntil", fmt.Sprintf("must be YYYY-MM-DD, got %q", v)})
		}
		d.RetainUntil = t
	}
	if v := get("Bytes"); v != "" {
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			probs = append(probs, [2]string{"Bytes", "must be a whole number"})
		}
	}
	return d, probs
}

// MigErrors pages through a package's check problems (file: one file only).
func (o *Ops) MigErrors(ctx context.Context, tenant, actor, id, file string, offset, limit int) ([]MigError, int, error) {
	if err := o.require(ctx, tenant, actor, "Checking a migration package", RoleAdmin); err != nil {
		return nil, 0, err
	}
	p, _, err := docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id)
	if err != nil || p.Check == nil {
		return []MigError{}, 0, nil
	}
	out, n := []MigError{}, 0
	err = o.eachMigError(ctx, tenant, p, func(e MigError) {
		if file != "" && e.File != file {
			return
		}
		if n >= offset && (limit <= 0 || len(out) < limit) {
			out = append(out, e)
		}
		n++
	})
	return out, n, err
}

func (o *Ops) eachMigError(ctx context.Context, tenant string, p MigPackage, fn func(MigError)) error {
	if p.Check == nil {
		return nil
	}
	for i := 0; i < p.Check.Chunks; i++ {
		d, _, err := docs.Get[migErrDoc](ctx, o.Docs, tenant, cMigErr, fmt.Sprintf("%s|%05d", p.ID, i))
		if err != nil {
			return err
		}
		for _, e := range d.Errors {
			fn(e)
		}
	}
	return nil
}

// MigErrorsCSV writes every problem the check found, for the firm to work
// through in a spreadsheet.
func (o *Ops) MigErrorsCSV(ctx context.Context, tenant, actor, id string, w io.Writer) error {
	if err := o.require(ctx, tenant, actor, "Checking a migration package", RoleAdmin); err != nil {
		return err
	}
	p, _, err := docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id)
	if err != nil {
		return fmt.Errorf("%w: no such package", ErrNotFound)
	}
	cw := csv.NewWriter(w)
	cw.Write([]string{"File", "Type", "Row", "Field", "Severity", "Problem", "Recommended action"})
	err = o.eachMigError(ctx, tenant, p, func(e MigError) {
		cw.Write([]string{e.File, e.Source, strconv.Itoa(e.Line), e.Field, e.Severity, e.Message, e.Action})
	})
	cw.Flush()
	return err
}

// groupINDec is groupIN for an exact quantity, with its decimals when it
// has any (12,34,567.993).
func groupINDec(q money.Dec) string {
	s := q.String()
	ip, frac, ok := strings.Cut(s, ".")
	n, _ := strconv.ParseInt(ip, 10, 64)
	out := groupIN(n)
	if n == 0 && strings.HasPrefix(ip, "-") {
		out = "-0"
	}
	if ok {
		out += "." + frac
	}
	return out
}

// groupIN writes n with Indian digit grouping (12,34,567).
func groupIN(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		if head != "" {
			parts = append([]string{head}, parts...)
		}
		s = strings.Join(parts, ",") + "," + tail
	}
	if neg {
		s = "-" + s
	}
	return s
}

// MigView is the migration screen.
type MigView struct {
	Packages   []MigPackageView `json:"packages"`
	Migrations []Migration      `json:"migrations"`
	MaxGB      int64            `json:"max_gb"`
	ChunkSize  int64            `json:"chunk_size"`
	Order      []string         `json:"order"`
	CanRun     bool             `json:"can_run"`     // upload, check, propose
	CanApprove bool             `json:"can_approve"` // approve loads and undos
}

// MigPackageView is a package with what the screen needs.
type MigPackageView struct {
	MigPackage
	Missing  int  `json:"missing"`
	Checking bool `json:"checking"`
	// Parts of the same split package present on the server.
	Parts []MigPartView `json:"parts,omitempty"`
}

// MigPartView is one part of a split package.
type MigPartView struct {
	Index   int    `json:"index"`
	Package string `json:"package,omitempty"` // empty: not uploaded yet
	Status  string `json:"status"`
	Note    string `json:"note,omitempty"`
}

// MigrationView lists the firm's packages and migrations.
func (o *Ops) MigrationView(ctx context.Context, tenant, actor string) (MigView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the migration", RoleChecker, RoleCompliance); err != nil {
		return MigView{}, err
	}
	ps, err := docs.List[MigPackage](ctx, o.Docs, tenant, cMigPkg, "")
	if err != nil {
		return MigView{}, err
	}
	ms, err := docs.List[Migration](ctx, o.Docs, tenant, cMig, "")
	if err != nil {
		return MigView{}, err
	}
	role, _ := o.RoleOf(ctx, tenant, actor)
	v := MigView{Packages: []MigPackageView{}, Migrations: []Migration{}, MaxGB: o.migMaxBytes(ctx, tenant) >> 30, ChunkSize: MigMaxChunk, Order: migrationOrder(),
		CanRun: role == RoleAdmin, CanApprove: role == RoleAdmin || role == RoleChecker}
	byKey := map[string][]MigPackage{}
	for _, p := range ps {
		if p.Status != MigDiscarded && p.Key != "" {
			byKey[p.Key] = append(byKey[p.Key], p)
		}
	}
	for i := len(ps) - 1; i >= 0; i-- {
		p := ps[i]
		if p.Status == MigDiscarded {
			continue
		}
		pv := MigPackageView{MigPackage: p, Missing: len(p.Missing()), Checking: o.migRunning("check:" + tenant + ":" + p.ID)}
		if p.Status == MigChecking && !pv.Checking {
			pv.Status = MigCheckFailed
			if pv.Check == nil {
				pv.Check = &MigCheck{Error: "the check was interrupted (the server restarted); check the package again"}
			}
		}
		pv.Have = nil // the screen needs the count only
		if p.Manifest != nil && p.Manifest.Part != nil && p.Manifest.Part.Of > 1 {
			for k := 1; k <= p.Manifest.Part.Of; k++ {
				pt := MigPartView{Index: k, Status: "Not uploaded"}
				for _, q := range byKey[p.Key] {
					if q.Manifest != nil && q.Manifest.Part != nil && q.Manifest.Part.Index == k {
						pt.Package, pt.Status, pt.Note = q.ID, q.Status, q.Manifest.Part.Note
					}
				}
				pv.Parts = append(pv.Parts, pt)
			}
		}
		v.Packages = append(v.Packages, pv)
	}
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		if m.Status == MigLoading && !o.migRunning("load:"+tenant+":"+m.ID) {
			m.Status = MigInterrupted
		}
		if m.Status == MigUndoing && !o.migRunning("undo:"+tenant+":"+m.ID) {
			m.Status = MigInterrupted
		}
		v.Migrations = append(v.Migrations, m)
	}
	return v, nil
}

// MigPackageDetail is one package with the first page of its problems.
func (o *Ops) MigPackageDetail(ctx context.Context, tenant, actor, id string) (MigPackageView, []MigError, int, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the migration", RoleChecker, RoleCompliance); err != nil {
		return MigPackageView{}, nil, 0, err
	}
	p, _, err := docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id)
	if err != nil {
		return MigPackageView{}, nil, 0, fmt.Errorf("%w: no such package", ErrNotFound)
	}
	pv := MigPackageView{MigPackage: p, Missing: len(p.Missing()), Checking: o.migRunning("check:" + tenant + ":" + id)}
	pv.Have = nil
	out, n := []MigError{}, 0
	o.eachMigError(ctx, tenant, p, func(e MigError) {
		if len(out) < 2000 {
			out = append(out, e)
		}
		n++
	})
	return pv, out, n, nil
}

// MigMissing lists the chunks of an upload still to be sent.
func (o *Ops) MigMissing(ctx context.Context, tenant, id string) []int {
	p, _, err := docs.Get[MigPackage](ctx, o.Docs, tenant, cMigPkg, id)
	if err != nil {
		return []int{}
	}
	return p.Missing()
}
