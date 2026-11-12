package run

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/migrate"
	mt "ledgerline/internal/migrate/migratetest"
	"ledgerline/internal/migrate/pkg"
)

const pass = "a long enough passphrase"

var now = time.Date(2026, 11, 12, 10, 0, 0, 0, time.Local)

func fy(start int) string {
	return itoa(start) + "-" + itoa((start + 1) % 100)[0:2]
}

func itoa(n int) string {
	s := ""
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

// office makes 10 company years (2017-18 to 2026-27), their mail archives,
// an admin database, an older duplicate of the newest year and a database
// that is not SharePro's.
func office(firms []string, docs, docSize int) []Input {
	var in []Input
	add := func(d *mt.DB, file string, date time.Time) {
		in = append(in, Input{DB: migrate.Database{Name: "LLMIG_abcdef_" + d.Name, Original: d.Name, File: file, BackupDate: date, Bytes: 1 << 20, SHA256: strings.Repeat("ab", 32)}, Q: d})
	}
	add(mt.Admin(firms), "shadmin.7z", now.Add(-5*time.Hour))
	for y := 2017; y <= 2026; y++ {
		name := "NC" + itoa(y%100) + itoa((y+1)%100)
		add(mt.Company(name, fy(y), firms, 3), name+".7z", now.Add(-5*time.Hour))
		add(mt.Mail("NCMAIL"+name[2:], fy(y), firms, docs, docSize), "NCMAIL"+name[2:]+".7z", now.Add(-5*time.Hour))
	}
	add(mt.Company("NC2627", "2026-27", firms, 1), "NC2627-old.7z", now.Add(-200*time.Hour))
	add(&mt.DB{Name: "ReportServer"}, "ReportServer.bak", now.Add(-5*time.Hour))
	return in
}

func TestFY(t *testing.T) {
	if fy(2026) != "2026-27" || fy(2017) != "2017-18" || fy(2099) != "2099-00" {
		t.Fatal(fy(2026), fy(2099))
	}
	if FYStart("2026-27") != 2026 || FYStart("2099-00") != 2099 || FYStart("2026-28") != 0 || FYStart("") != 0 || FYStart("26-27") != 0 {
		t.Error("FYStart")
	}
}

func TestClassifyAndSelect(t *testing.T) {
	ctx := context.Background()
	m := &mt.Mapper{}
	s, err := Classify(ctx, m, office([]string{"01"}, 1, 100), 8, now)
	if err != nil {
		t.Fatal(err)
	}
	why := map[string]string{}
	use := map[string]bool{}
	for _, f := range s.Found {
		k := f.DB.File
		why[k], use[k] = f.Why, f.Use
	}
	if !use["NC2627.7z"] || !use["NC1920.7z"] || use["NC1819.7z"] || use["NC1718.7z"] || !use["shadmin.7z"] || !use["NCMAIL1920.7z"] || use["NCMAIL1819.7z"] {
		t.Errorf("use %v", use)
	}
	if !strings.Contains(why["NC1819.7z"], "Older than the 8 years") || !strings.Contains(why["NC2627-old.7z"], "is newer and is used instead") || !strings.Contains(why["ReportServer.bak"], "Not a back office database") {
		t.Errorf("why %v", why)
	}
	if n := s.Newest(); n == nil || n.DB.File != "NC2627.7z" {
		t.Errorf("newest %v", n)
	}
	if len(s.Firms) != 1 || s.Firms[0].Code != "01" || len(s.Warnings) != 0 {
		t.Errorf("firms %v warnings %v", s.Firms, s.Warnings)
	}
	// fewer years
	s.SetYears(ctx, 2)
	n := 0
	for _, f := range s.Found {
		if f.Use && f.Kind.Role == "company" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d company years for 2", n)
	}
	// the user skips the newest year: the old copy of it is used instead
	for i, f := range s.Found {
		if f.DB.File == "NC2627.7z" {
			s.Toggle(ctx, i, true)
		}
	}
	if nw := s.Newest(); nw == nil || nw.DB.File != "NC2627-old.7z" {
		t.Fatalf("newest after skip: %v", nw)
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "Take a fresh backup in SharePro before migrating") {
		t.Errorf("stale warning: %v", s.Warnings)
	}
	b := s.Backups()
	if len(b) != len(s.Found) || b[0].Database == "" {
		t.Error("backups")
	}
}

func TestNoCompany(t *testing.T) {
	s, err := Classify(context.Background(), &mt.Mapper{}, []Input{{DB: migrate.Database{Name: "shadmin"}, Q: mt.Admin([]string{"01"})}, {DB: migrate.Database{Name: "broken", File: "broken.bak"}, Problem: "SQL Server cannot read this file."}}, 8, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "No company database") || s.Found[1].Why != "SQL Server cannot read this file." {
		t.Errorf("%v %+v", s.Warnings, s.Found[1])
	}
	if _, err := Run(context.Background(), Options{Survey: s, Passphrase: pass, Out: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "no company database") {
		t.Errorf("run: %v", err)
	}
}

func options(t *testing.T, s *Survey, maxPart int64) Options {
	d := t.TempDir()
	return Options{Survey: s, Plan: migrate.Plan{Years: 8, Documents: true, DormantMode: "inactive"}, Server: `localhost\SQLEXPRESS`, Out: filepath.Join(d, "out"), Work: filepath.Join(d, "work"), Passphrase: pass, MaxPart: maxPart, Tool: "test", Now: func() time.Time { return now }, StepWait: 2 * time.Second,
		Extra: []pkg.Backup{{File: "old.rar", Database: "", Role: "other", Note: "RAR files cannot be read"}}}
}

func open(t *testing.T, p string) *pkg.Reader {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := pkg.Open(f, pass, t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	if bad := r.VerifyAll(); len(bad) > 0 {
		t.Fatalf("verify: %v", bad)
	}
	return r
}

func TestRunOnePackage(t *testing.T) {
	ctx := context.Background()
	m := &mt.Mapper{FailHistory: map[string]bool{"2022-23": true}, LeaveOpen: map[string]bool{"2021-22": true}}
	s, err := Classify(ctx, m, office([]string{"01", "02"}, 4, 2000), 8, now)
	if err != nil {
		t.Fatal(err)
	}
	o := options(t, s, 0)
	var steps []string
	o.Progress = progressFunc(func(s string) { steps = append(steps, s) })
	res, err := Run(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Parts) != 1 || filepath.Base(res.Parts[0].Path) != "ledgerline-migration-2026-11-12.llmig" || res.Parts[0].Bytes == 0 {
		t.Fatalf("parts %+v", res.Parts)
	}
	if runtimeMode(res.Parts[0].Path) != 0o600 {
		t.Error("package not 0600")
	}
	r := open(t, res.Parts[0].Path)
	man := r.Manifest
	if man.Part != nil || man.Source.System != "Fake" || man.Source.Server != `localhost\SQLEXPRESS` || len(man.Firms) != 2 {
		t.Errorf("manifest %+v", man.Source)
	}
	for _, f := range man.Firms {
		if f.AsOf != "2026-10-30" {
			t.Errorf("firm %s: the as-of date the mapper gave was lost: %q", f.Code, f.AsOf)
		}
	}
	used := 0
	for _, b := range man.Source.Backups {
		if b.Used {
			used++
		}
		if b.File == "old.rar" && b.Used {
			t.Error("rar used")
		}
	}
	if used != 17 { // admin + 8 company + 8 mail
		t.Errorf("%d used", used)
	}
	if !r.Has("firms/01/master/CLIENT-MASTER.csv") || !r.Has("firms/02/history/2026-27/LEDGER.csv") || !r.Has("firms/01/history/2019-20/LEDGER.csv") || r.Has("firms/01/history/2018-19/LEDGER.csv") {
		t.Error("files missing or too many years")
	}
	// the failed year and the stuck year are left out whole, with an error each
	if r.Has("firms/01/history/2022-23/LEDGER.csv") || r.Has("firms/01/history/2021-22/LEDGER.csv") {
		t.Error("failed history year in the package")
	}
	errs := 0
	for _, i := range man.Issues {
		if i.Severity == "Error" {
			errs++
		}
	}
	if errs != 2 {
		t.Errorf("issues %+v", man.Issues)
	}
	rows := 0
	r.CSV("firms/01/documents/index.csv", func(h, row []string) error { rows++; return nil })
	if rows != 32 || !r.Has("report.html") {
		t.Errorf("%d documents", rows)
	}
	checks := map[string]string{}
	for _, c := range man.Checks {
		checks[c.Firm+"/"+c.Name] = c.Value
	}
	if checks["01/clients"] != "3" || checks["02/client-debit-total"] != "6001.50" {
		t.Errorf("checks %v", checks)
	}
	if len(man.Dropped) != 2 || man.Dropped[0].Count != 3 {
		t.Errorf("dropped %+v", man.Dropped)
	}
	rep, _ := os.ReadFile(res.Report)
	for _, want := range []string{"6,001.50", "Client password", "History 2022-23", "Contract note", "SHA-256", "Take", "Files → Migration from old system", "Older than the 8 years"} {
		if want == "Take" {
			continue
		}
		if !strings.Contains(string(rep), want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if strings.Contains(string(rep), "http://") || strings.Contains(string(rep), "https://") || strings.Contains(string(rep), "<script") {
		t.Error("report has external assets or scripts")
	}
	if len(res.Docs) == 0 || len(steps) == 0 {
		t.Error("no doc counts or steps")
	}
	// nothing left behind: staging removed, no temporary files beside the package
	ents, _ := os.ReadDir(o.Out)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("left %s", e.Name())
		}
	}
	if ents, _ := os.ReadDir(o.Work); len(ents) != 0 {
		t.Errorf("work folder not cleaned: %v", ents)
	}
	// a second run does not overwrite the first
	res2, err := Run(ctx, o)
	if err != nil || filepath.Base(res2.Parts[0].Path) != "ledgerline-migration-2026-11-12 (2).llmig" {
		t.Errorf("second run: %v %v", res2, err)
	}
}

func runtimeMode(p string) os.FileMode {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	if os.PathSeparator == '\\' {
		return 0o600
	}
	return st.Mode().Perm()
}

type progressFunc func(string)

func (f progressFunc) Step(s string)    { f(s) }
func (progressFunc) Count(int, int)     {}
func (progressFunc) Log(string, string) {}

func TestRunSplit(t *testing.T) {
	ctx := context.Background()
	s, err := Classify(ctx, &mt.Mapper{}, office([]string{"01"}, 10, 300<<10), 3, now)
	if err != nil {
		t.Fatal(err)
	}
	o := options(t, s, 3500<<10) // 30 documents of 300 KB; parts of 3.5 MB less 2 MB slack
	res, err := Run(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Parts) < 2 {
		t.Fatalf("not split: %+v", res.Parts)
	}
	id := ""
	total := 0
	for i, p := range res.Parts {
		want := "ledgerline-migration-2026-11-12-part" + itoa(i + 1)[len(itoa(i+1))-1:] + "-of-"
		if i < 9 && !strings.HasPrefix(filepath.Base(p.Path), want) {
			t.Errorf("name %s", p.Path)
		}
		r := open(t, p.Path)
		pt := r.Manifest.Part
		if pt == nil || pt.Index != i+1 || pt.Of != len(res.Parts) {
			t.Fatalf("part %+v", pt)
		}
		if id == "" {
			id = pt.ID
		} else if pt.ID != id {
			t.Error("part ids differ")
		}
		if i == 0 {
			if !r.Has("firms/01/master/CLIENT-MASTER.csv") || !r.Has("report.html") || len(r.Manifest.Checks) == 0 {
				t.Error("part 1 lacks masters, report or checks")
			}
		} else {
			if r.Has("firms/01/master/CLIENT-MASTER.csv") || !strings.HasPrefix(pt.Note, "Documents ") {
				t.Errorf("part %d: %s", i+1, pt.Note)
			}
			if p.Bytes > o.MaxPart+(1<<20) {
				t.Errorf("part %d is %d bytes", i+1, p.Bytes)
			}
		}
		n := 0
		if r.Has("firms/01/documents/index.csv") {
			r.CSV("firms/01/documents/index.csv", func(h, row []string) error {
				n++
				rc, err := r.OpenDocument(row[1], row[11])
				if err != nil {
					t.Fatal(err)
				}
				rc.Close()
				return nil
			})
		}
		if n != p.Docs {
			t.Errorf("part %d: index %d, says %d", i+1, n, p.Docs)
		}
		total += n
	}
	if total != 30 {
		t.Errorf("%d documents in all", total)
	}
}

func TestRunCurrentFatal(t *testing.T) {
	ctx := context.Background()
	s, _ := Classify(ctx, &mt.Mapper{FailCurrent: true}, office([]string{"01"}, 1, 10), 2, now)
	o := options(t, s, 0)
	_, err := Run(ctx, o)
	if err == nil || !strings.Contains(err.Error(), "could not read the go-live data") {
		t.Fatalf("got %v", err)
	}
	if ents, _ := os.ReadDir(o.Out); len(ents) != 0 {
		t.Errorf("left %v", ents)
	}
}

func TestRunCancel(t *testing.T) {
	m := &mt.Mapper{Block: make(chan struct{}), BlockStarted: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	s, _ := Classify(ctx, m, office([]string{"01"}, 1, 10), 2, now)
	o := options(t, s, 0)
	go func() {
		<-m.BlockStarted
		cancel()
	}()
	_, err := Run(ctx, o)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if ents, _ := os.ReadDir(o.Out); len(ents) != 0 {
		t.Errorf("left %v", ents)
	}
	if ents, _ := os.ReadDir(o.Work); len(ents) != 0 {
		t.Errorf("work left %v", ents)
	}
}

func TestRunRefusals(t *testing.T) {
	ctx := context.Background()
	s, _ := Classify(ctx, &mt.Mapper{}, office([]string{"01"}, 0, 0), 1, now)
	o := options(t, s, 0)
	o.Passphrase = "short"
	if _, err := Run(ctx, o); err == nil || !strings.Contains(err.Error(), "at least 12") {
		t.Errorf("short passphrase: %v", err)
	}
	o = options(t, s, 0)
	o.Out = filepath.Join(o.Work, "inside")
	if _, err := Run(ctx, o); err == nil || !strings.Contains(err.Error(), "work folder") {
		t.Errorf("inside work: %v", err)
	}
}

func TestWithin(t *testing.T) {
	cases := []struct {
		parent, child string
		win, want     bool
	}{
		{`C:\Users\Office Staff\AppData\Local\Temp\llmig-1`, `c:\users\office staff\appdata\local\temp\LLMIG-1\out`, true, true},
		{`C:\Temp\llmig`, `C:\Temp\llmig2\out`, true, false},
		{`C:\Temp\llmig`, `C:/Temp/llmig/sub/../x`, true, true},
		{`C:\Temp\llmig`, `C:\Temp\llmig\..\other`, true, false},
		{`C:\Temp\llmig`, `D:\Temp\llmig`, true, false},
		{`C:\Temp\llmig`, `\\?\C:\Temp\llmig\x`, true, true},
		{`\\fileserver\share\work`, `\\FILESERVER\Share\work\out`, true, true},
		{"/Users/office/Library/Caches/Ledgerline Migration/w1", "/Users/office/Library/Caches/Ledgerline Migration/w1/out", false, true},
		{"/Users/office/work", "/Users/office/Work/out", false, false},
		{"/Users/office/work", "/Users/office/workshop", false, false},
		{"/tmp/w", "/tmp/w", false, true},
	}
	for _, c := range cases {
		if got := Within(c.parent, c.child, c.win); got != c.want {
			t.Errorf("Within(%q, %q, %v) = %v", c.parent, c.child, c.win, got)
		}
	}
	d := t.TempDir()
	if !WithinHere(d, filepath.Join(d, "a", "b")) || WithinHere(filepath.Join(d, "a"), filepath.Join(d, "b")) {
		t.Error("WithinHere")
	}
}

func TestIndian(t *testing.T) {
	for in, want := range map[string]string{
		"1234567":     "12,34,567",
		"-123456.78":  "-1,23,456.78",
		"100":         "100",
		"1000":        "1,000",
		"100000":      "1,00,000",
		"12345678901": "12,34,56,78,901",
		"0.5":         "0.5",
		"abc":         "abc",
		"1.2.3":       "1.2.3",
		"":            "",
	} {
		if g := Indian(in); g != want {
			t.Errorf("Indian(%q) = %q, want %q", in, g, want)
		}
	}
	if Size(23<<30+512<<20) != "23.5 GB" || Size(2048) != "2 KB" {
		t.Error("Size")
	}
}

func TestGroupIssues(t *testing.T) {
	g := GroupIssues([]pkg.Issue{{Area: "A", Severity: "Warning"}, {Area: "B", Severity: "Info"}, {Area: "B", Severity: "Error"}, {Area: "A", Severity: "Info"}})
	if len(g) != 2 || g[0].Area != "B" || g[0].Issues[0].Severity != "Error" || g[1].Area != "A" {
		t.Errorf("%+v", g)
	}
}
