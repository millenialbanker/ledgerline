package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

// DefaultMaxPart is the default largest package size.
const DefaultMaxPart = 4 << 30

// MinPassphrase is the shortest passphrase the tool accepts.
const MinPassphrase = 12

// Options is everything a run needs.
type Options struct {
	Survey     *Survey
	Plan       migrate.Plan
	Server     string // shown in the manifest, e.g. localhost\SQLEXPRESS
	Out        string // folder the package is saved in
	Base       string // file name without .llmig (default ledgerline-migration-<date>)
	Work       string // the tool's work folder; the package may not be saved inside it
	Passphrase string
	MaxPart    int64 // largest package size; 0 means DefaultMaxPart, <0 no limit
	Progress   migrate.Progress
	Tool       string       // e.g. "Ledgerline migration tool 1.0"
	Extra      []pkg.Backup // dropped files that could not be used
	Now        func() time.Time
	StepWait   time.Duration // how long to wait for a stuck package file (default 30s)
}

// PartFile is one saved package file.
type PartFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	Note  string `json:"note,omitempty"`
	Docs  int    `json:"docs"`
}

// DocCount is the number of documents of one type in one year.
type DocCount struct {
	Firm  string `json:"firm"`
	FY    string `json:"fy"`
	Type  string `json:"type"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
}

// Result is what a run made.
type Result struct {
	Parts    []PartFile   `json:"parts"`
	Report   string       `json:"report"` // report.html beside the package
	Manifest pkg.Manifest `json:"manifest"`
	Docs     []DocCount   `json:"docs"`
	Steps    []string     `json:"steps"`
}

// ErrStuck means a mapper step left a file in its package open.
var ErrStuck = errors.New("the reader left a file in the package unfinished")

type counter struct {
	w io.Writer
	n int64
}

func (c *counter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

type part struct {
	index int
	tmp   string
	f     *os.File
	cw    *counter
	w     *pkg.Writer
	fys   map[string]bool
	docs  int
	added int64 // bytes handed to the writer, not yet necessarily on disk
}

func (p *part) size() int64 { return max(p.cw.n, p.added) }

type runner struct {
	o      Options
	prog   migrate.Progress
	parts  []*part
	stage  string
	docs   map[string]*DocCount
	steps  []string
	issues []pkg.Issue // the run's own issues (step failures)
	id     string
	base   string
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Run reads the chosen databases and writes the package.
func Run(ctx context.Context, o Options) (res *Result, err error) {
	if o.Survey == nil {
		return nil, errors.New("nothing to migrate")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Progress == nil {
		o.Progress = migrate.NopProgress{}
	}
	if o.MaxPart == 0 {
		o.MaxPart = DefaultMaxPart
	}
	if o.StepWait == 0 {
		o.StepWait = 30 * time.Second
	}
	if o.Plan.Years == 0 {
		o.Plan.Years = 8
	}
	if len([]rune(o.Passphrase)) < MinPassphrase {
		return nil, fmt.Errorf("the passphrase must be at least %d characters", MinPassphrase)
	}
	if o.Out == "" {
		return nil, errors.New("choose where to save the package")
	}
	out, err := filepath.Abs(o.Out)
	if err != nil {
		return nil, err
	}
	o.Out = out
	if o.Work != "" && WithinHere(o.Work, o.Out) {
		return nil, errors.New("the package cannot be saved inside the tool's work folder, which is deleted at the end; choose another folder")
	}
	if err := os.MkdirAll(o.Out, 0o700); err != nil {
		return nil, fmt.Errorf("cannot use the folder %s: %w", o.Out, err)
	}
	newest := o.Survey.Newest()
	if newest == nil {
		return nil, errors.New("no company database to migrate: drop the latest company backup")
	}
	r := &runner{o: o, prog: o.Progress, docs: map[string]*DocCount{}, id: randHex(8)}
	r.base = o.Base
	if r.base == "" {
		r.base = "ledgerline-migration-" + o.Now().Format("2006-01-02")
	}
	stageRoot := o.Work
	if stageRoot == "" {
		stageRoot = os.TempDir()
	}
	if err := os.MkdirAll(stageRoot, 0o700); err != nil {
		return nil, err
	}
	if r.stage, err = os.MkdirTemp(stageRoot, "stage-"); err != nil {
		return nil, err
	}
	defer os.RemoveAll(r.stage)
	defer func() {
		if err != nil {
			r.abort()
		}
	}()

	if _, err := r.newPart(); err != nil {
		return nil, err
	}

	// go-live files: fatal on failure
	r.step("Reading the go-live data from " + newest.DB.Original)
	if err := r.mapperStep(ctx, "Go-live data", newest, func(w *pkg.Writer) error {
		return o.Survey.Mapper.Current(ctx, newest.Q, newest.Kind, o.Plan, w, r.prog)
	}); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("could not read the go-live data (masters and balances) from %s: %w", newest.DB.Original, err)
	}

	used := o.Survey.Used()
	for _, f := range used {
		if f.Kind.Role != "company" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r.step("Reading the history of " + f.Kind.FY + " from " + f.DB.Original)
		f := f
		err := r.mapperStep(ctx, "History "+f.Kind.FY, f, func(w *pkg.Writer) error {
			return o.Survey.Mapper.History(ctx, f.Q, f.Kind, o.Plan, w, r.prog)
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			r.issue(pkg.Issue{FY: f.Kind.FY, Severity: "Error", Area: "History " + f.Kind.FY, Key: f.DB.Original, Message: "The history of " + f.Kind.FY + " could not be read: " + err.Error(), Action: "This year's history is left out of the package. Check the backup of " + f.DB.Original + " (take a fresh one if it is damaged) and run the tool again, or go live without this year's history."})
		}
	}
	if o.Plan.Documents {
		for _, f := range used {
			if f.Kind.Role != "mail" {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			r.step("Reading the archived documents of " + f.Kind.FY + " from " + f.DB.Original)
			f := f
			err := r.mapperStep(ctx, "Documents "+f.Kind.FY, f, func(w *pkg.Writer) error {
				return o.Survey.Mapper.Documents(ctx, f.Q, f.Kind, o.Plan, w, r.prog)
			})
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				r.issue(pkg.Issue{FY: f.Kind.FY, Severity: "Error", Area: "Documents " + f.Kind.FY, Key: f.DB.Original, Message: "The archived documents of " + f.Kind.FY + " could not be read: " + err.Error(), Action: "These documents are left out. Keep the SharePro mail backup " + f.DB.Original + " for the retention period, or fix it and run the tool again."})
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.step("Writing the package")
	return r.finish()
}

func (r *runner) step(s string) {
	r.steps = append(r.steps, s)
	r.prog.Step(s)
	r.prog.Log("Info", s)
}

func (r *runner) issue(i pkg.Issue) {
	r.issues = append(r.issues, i)
	r.parts[0].w.AddIssue(i)
	r.prog.Log(i.Severity, i.Message)
}

func (r *runner) newPart() (*part, error) {
	n := len(r.parts) + 1
	tmp := filepath.Join(r.o.Out, fmt.Sprintf(".%s.part%d.%s.tmp", r.base, n, randHex(3)))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cannot write the package in %s: %w", r.o.Out, err)
	}
	cw := &counter{w: f}
	w, err := pkg.Create(cw, r.o.Passphrase, r.o.Tool)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return nil, err
	}
	m := w.Manifest()
	m.Source = pkg.Source{System: r.o.Survey.Mapper.Name(), Server: r.o.Server, Backups: append(r.o.Survey.Backups(), r.o.Extra...)}
	m.Firms = r.firms()
	p := &part{index: n, tmp: tmp, f: f, cw: cw, w: w, fys: map[string]bool{}}
	r.parts = append(r.parts, p)
	if n > 1 {
		r.prog.Log("Info", fmt.Sprintf("The package is reaching %s; starting part %d", Size(r.o.MaxPart), n))
	}
	return p, nil
}

func (r *runner) firms() []pkg.Firm {
	var out []pkg.Firm
	for _, f := range r.o.Survey.Firms {
		if len(r.o.Plan.Firms) == 0 {
			out = append(out, f)
			continue
		}
		for _, c := range r.o.Plan.Firms {
			if c == f.Code {
				out = append(out, f)
				break
			}
		}
	}
	for i := range out {
		if !r.o.Plan.AsOf.IsZero() && out[i].AsOf == "" {
			out[i].AsOf = r.o.Plan.AsOf.Format("2006-01-02")
		}
	}
	return out
}

func (r *runner) abort() {
	for _, p := range r.parts {
		p.f.Close()
		os.Remove(p.tmp)
	}
	r.parts = nil
}

// withTimeout runs fn but gives up when it does not return in time (a
// writer whose file was left open would wait for ever).
func withTimeout(d time.Duration, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		return ErrStuck
	}
}

// mapperStep runs one Mapper call into a staging package of its own, then
// copies what it wrote into the real package. A step that fails leaves
// nothing half-written behind, and documents can be spread over parts.
func (r *runner) mapperStep(ctx context.Context, area string, f *Found, call func(w *pkg.Writer) error) error {
	sf, err := os.CreateTemp(r.stage, "step-*.llmig")
	if err != nil {
		return err
	}
	sp := sf.Name()
	defer func() {
		sf.Close()
		os.Remove(sp)
	}()
	pass := randHex(16)
	sw, err := pkg.Create(sf, pass, r.o.Tool)
	if err != nil {
		return err
	}
	if err := call(sw); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := withTimeout(r.o.StepWait, sw.Close); err != nil {
		return err
	}
	if _, err := sf.Seek(0, io.SeekStart); err != nil {
		return err
	}
	rd, err := pkg.Open(sf, pass, r.stage, 0)
	if err != nil {
		return err
	}
	defer rd.Close()
	return r.copyStep(ctx, area, rd)
}

func (r *runner) copyStep(ctx context.Context, area string, rd *pkg.Reader) error {
	p1 := r.parts[0]
	m := rd.Manifest
	// files other than documents go into part 1
	for _, f := range m.Files {
		if f.Kind == pkg.KindDocuments || f.Kind == pkg.KindDocIndex || f.Kind == "document" {
			continue
		}
		if err := copyFile(ctx, p1, rd, f); err != nil {
			return err
		}
	}
	// documents: into the current part, rolling over when it is full
	for _, f := range m.Files {
		if f.Kind != pkg.KindDocIndex {
			continue
		}
		var rows [][]string
		if err := rd.CSV(f.Path, func(h, row []string) error {
			rows = append(rows, append([]string(nil), row...))
			return nil
		}); err != nil {
			return err
		}
		r.prog.Step(area + ": packing documents")
		for i, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := r.copyDoc(rd, f.Firm, row); err != nil {
				return err
			}
			r.prog.Count(i+1, len(rows))
		}
	}
	for _, c := range m.Checks {
		p1.w.AddCheck(c)
	}
	for _, i := range m.Issues {
		p1.w.AddIssue(i)
	}
	for _, d := range m.Dropped {
		p1.w.AddDropped(d.Firm, d.Field, d.Reason, d.Count)
	}
	return nil
}

func copyFile(ctx context.Context, p *part, rd *pkg.Reader, f pkg.File) error {
	src, err := rd.Open(f.Path)
	if err != nil {
		return err
	}
	defer src.Close()
	nf := f
	nf.Bytes, nf.SHA256 = 0, ""
	e, err := p.w.Begin(nf, true)
	if err != nil {
		return err
	}
	buf := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			e.Close()
			return err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, err := e.Write(buf[:n]); err != nil {
				e.Close()
				return err
			}
			p.added += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			e.Close()
			return rerr
		}
	}
	return e.Close()
}

// maxDoc is the largest single document the tool will carry.
const maxDoc = 512 << 20

func parseDay(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func (r *runner) copyDoc(rd *pkg.Reader, firm string, row []string) error {
	if len(row) < len(pkg.DocColumns) {
		return errors.New("a document index row is short")
	}
	col := func(name string) string {
		for i, c := range pkg.DocColumns {
			if c == name {
				return row[i]
			}
		}
		return ""
	}
	src, err := rd.OpenDocument(col("Path"), col("SHA256"))
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(src, maxDoc+1))
	src.Close()
	if err != nil {
		return err
	}
	if len(data) > maxDoc {
		return fmt.Errorf("document %s is larger than %s", col("ID"), Size(maxDoc))
	}
	cur := r.parts[len(r.parts)-1]
	// part 1 holds the masters and history whatever their size; documents
	// go on in further parts once the current one is full
	const slack = 2 << 20
	if r.o.MaxPart > 0 && (cur.docs > 0 || cur.index == 1) && cur.size()+int64(len(data))+slack > r.o.MaxPart {
		if cur, err = r.newPart(); err != nil {
			return err
		}
	}
	meta := pkg.DocMeta{ID: col("ID"), UCC: col("UCC"), Type: col("Type"), Date: parseDay(col("Date")), FY: col("FY"), Subject: col("Subject"), To: col("To"), Signed: col("Signed") == "Y", Format: col("Format"), SourceRef: col("SourceRef"), RetainUntil: parseDay(col("RetainUntil"))}
	if err := cur.w.AddDocument(firm, meta, data); err != nil {
		return err
	}
	cur.added += int64(len(data)) + 200
	cur.docs++
	cur.fys[meta.FY] = true
	k := firm + "\x00" + meta.FY + "\x00" + meta.Type
	d := r.docs[k]
	if d == nil {
		d = &DocCount{Firm: firm, FY: meta.FY, Type: meta.Type}
		r.docs[k] = d
	}
	d.Count++
	d.Bytes += int64(len(data))
	return nil
}

func fyRange(fys map[string]bool) string {
	var l []string
	for f := range fys {
		l = append(l, f)
	}
	sort.Strings(l)
	switch len(l) {
	case 0:
		return ""
	case 1:
		return l[0]
	}
	return l[0] + " to " + l[len(l)-1]
}

func (r *runner) finish() (*Result, error) {
	of := len(r.parts)
	res := &Result{Steps: r.steps}
	for _, d := range r.docs {
		res.Docs = append(res.Docs, *d)
	}
	sort.Slice(res.Docs, func(i, j int) bool {
		a, b := res.Docs[i], res.Docs[j]
		if a.Firm != b.Firm {
			return a.Firm < b.Firm
		}
		if a.FY != b.FY {
			return a.FY > b.FY
		}
		return a.Type < b.Type
	})
	names := make([]string, of)
	for i, p := range r.parts {
		if of == 1 {
			names[i] = r.base + ".llmig"
		} else {
			names[i] = fmt.Sprintf("%s-part%d-of-%d.llmig", r.base, p.index, of)
		}
		names[i] = freeName(r.o.Out, names[i])
		note := ""
		if of > 1 {
			if p.index == 1 {
				note = "Masters, balances and history"
				if p.docs > 0 {
					note += "; documents " + fyRange(p.fys)
				}
			} else {
				note = "Documents " + fyRange(p.fys)
			}
			p.w.Manifest().Part = &pkg.Part{ID: r.id, Index: p.index, Of: of, Note: note}
		}
		res.Parts = append(res.Parts, PartFile{Path: filepath.Join(r.o.Out, names[i]), Note: note, Docs: p.docs})
	}
	// the report inside part 1 is written before the sizes are known
	m1 := r.parts[0].w.Manifest()
	res.Manifest = *m1
	html := Report(res, r.o, false)
	if err := r.parts[0].w.AddRaw("report.html", pkg.KindReport, html); err != nil {
		return nil, err
	}
	for _, p := range r.parts {
		if err := withTimeout(2*time.Minute, p.w.Close); err != nil {
			return nil, err
		}
		if err := p.f.Sync(); err != nil {
			return nil, err
		}
		if err := p.f.Close(); err != nil {
			return nil, err
		}
	}
	for i, p := range r.parts {
		if err := os.Rename(p.tmp, res.Parts[i].Path); err != nil {
			return nil, err
		}
		st, err := os.Stat(res.Parts[i].Path)
		if err == nil {
			res.Parts[i].Bytes = st.Size()
		}
	}
	res.Manifest = *r.parts[0].w.Manifest()
	for _, p := range r.parts[1:] {
		res.Manifest.Files = append(res.Manifest.Files, p.w.Manifest().Files...)
	}
	r.parts = nil // saved: nothing to remove now
	res.Report = filepath.Join(r.o.Out, freeName(r.o.Out, r.base+"-report.html"))
	if err := os.WriteFile(res.Report, Report(res, r.o, true), 0o600); err != nil {
		return nil, err
	}
	return res, nil
}

// freeName returns name, or name with " (2)" and so on when the folder
// already has a file of that name.
func freeName(dir, name string) string {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	n := name
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(dir, n)); errors.Is(err, os.ErrNotExist) {
			return n
		}
		n = fmt.Sprintf("%s (%d)%s", stem, i, ext)
	}
}
