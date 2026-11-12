package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/migrate"
	mt "ledgerline/internal/migrate/migratetest"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/migrate/sqlserver"
)

// fakeSQL is a SQL Server that "restores" a .bak whose content names a
// made-up database.
type fakeSQL struct {
	mu      sync.Mutex
	dbs     map[string]*mt.DB // by original name
	temp    map[string]string // temporary name -> original
	dropped []string
	closed  bool
}

func newFakeSQL() *fakeSQL {
	firms := []string{"01"}
	f := &fakeSQL{dbs: map[string]*mt.DB{}, temp: map[string]string{}}
	for _, d := range []*mt.DB{mt.Admin(firms), mt.Company("NC2627", "2026-27", firms, 4), mt.Company("NC2526", "2025-26", firms, 3), mt.Mail("NCMAIL2627", "2026-27", firms, 3, 500)} {
		f.dbs[d.Name] = d
	}
	return f
}

var diskRe = regexp.MustCompile(`DISK = N'((?:[^']|'')*)'`)

func (f *fakeSQL) Rows(ctx context.Context, q string, args ...any) ([]migrate.Row, error) {
	switch {
	case strings.Contains(q, "SERVERPROPERTY"):
		return []migrate.Row{{"version": "16.0.1000.6", "edition": "Express Edition (64-bit)", "engine": int64(4), "datapath": "/var/opt/mssql/data/", "backuppath": "/var/opt/mssql/backup", "dbversion": int64(957)}}, nil
	case strings.Contains(q, "dm_os_volume_stats"):
		return []migrate.Row{{"free": int64(100 << 30)}}, nil
	case strings.HasPrefix(q, "RESTORE HEADERONLY"), strings.HasPrefix(q, "RESTORE FILELISTONLY"):
		p := strings.ReplaceAll(diskRe.FindStringSubmatch(q)[1], "''", "'")
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, errors.New("Cannot open backup device")
		}
		name := strings.TrimSpace(string(b))
		if strings.HasPrefix(q, "RESTORE HEADERONLY") {
			return []migrate.Row{{"backuptype": int64(1), "position": int64(1), "databasename": name, "databaseversion": int64(957), "backupsize": "1000", "backupfinishdate": time.Now().Add(-time.Hour)}}, nil
		}
		return []migrate.Row{{"logicalname": name, "type": "D", "size": "1048576"}, {"logicalname": name + "_log", "type": "L", "size": "1048576"}}, nil
	case strings.Contains(q, "sys.databases"):
		return []migrate.Row{{"name": "NC2627", "bytes": int64(1 << 20)}, {"name": "shadmin", "bytes": int64(1 << 20)}}, nil
	}
	return nil, fmt.Errorf("unexpected %q", q)
}

var restoreRe = regexp.MustCompile(`^RESTORE DATABASE \[([^\]]+)\] FROM DISK = N'((?:[^']|'')*)'`)

func (f *fakeSQL) Exec(ctx context.Context, q string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := restoreRe.FindStringSubmatch(q); m != nil {
		b, _ := os.ReadFile(strings.ReplaceAll(m[2], "''", "'"))
		f.temp[m[1]] = strings.TrimSpace(string(b))
		return nil
	}
	if strings.HasPrefix(q, "IF DB_ID(") {
		f.dropped = append(f.dropped, q)
		return nil
	}
	return fmt.Errorf("unexpected exec %q", q)
}

func (f *fakeSQL) Database(name string) (migrate.Querier, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	orig := name
	if o, ok := f.temp[name]; ok {
		orig = o
	}
	d := f.dbs[orig]
	if d == nil {
		return nil, errors.New("no database " + name)
	}
	return &mt.DB{Name: name, Tables: d.Tables}, nil
}

func (f *fakeSQL) Forget(string) {}
func (f *fakeSQL) Close() error  { f.closed = true; return nil }

type fakeConn struct {
	sql    *fakeSQL
	eps    []sqlserver.Endpoint
	logins []sqlserver.Login
}

func (c *fakeConn) Discover(ctx context.Context) []sqlserver.Endpoint { return c.eps }
func (c *fakeConn) Connect(ctx context.Context, e sqlserver.Endpoint, l sqlserver.Login) (DB, error) {
	c.logins = append(c.logins, l)
	if l.User == "" && !sqlserver.WindowsSignIn {
		return nil, &sqlserver.LoginError{Msg: "Windows sign-in is not available here. Enter a SQL Server login (such as sa)."}
	}
	if l.User != "" && l.Password != "right" {
		return nil, &sqlserver.LoginError{Msg: "SQL Server did not accept that login name and password."}
	}
	return c.sql, nil
}

type harness struct {
	t   *testing.T
	s   *Session
	u   *UI
	ts  *httptest.Server
	sql *fakeSQL
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	s, err := NewSession(&mt.Mapper{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fs := newFakeSQL()
	s.Connector = &fakeConn{sql: fs, eps: []sqlserver.Endpoint{{Host: "localhost", Instance: "SQLEXPRESS"}}}
	s.Out = t.TempDir()
	u := &UI{S: s, Token: NewToken()}
	ts := httptest.NewServer(u.Handler())
	u.Addr = strings.TrimPrefix(ts.URL, "http://")
	t.Cleanup(func() { ts.Close(); s.Close() })
	return &harness{t: t, s: s, u: u, ts: ts, sql: fs}
}

func (h *harness) req(method, path string, body io.Reader, hdr map[string]string) *http.Response {
	h.t.Helper()
	r, _ := http.NewRequest(method, h.ts.URL+path, body)
	r.Header.Set("X-LL-Token", h.u.Token)
	for k, v := range hdr {
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func (h *harness) post(path string, v any) (int, map[string]any) {
	h.t.Helper()
	b, _ := json.Marshal(v)
	resp := h.req("POST", path, bytes.NewReader(b), nil)
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (h *harness) state() State {
	h.t.Helper()
	resp := h.req("GET", "/api/state", nil, nil)
	defer resp.Body.Close()
	var st State
	json.NewDecoder(resp.Body).Decode(&st)
	return st
}

func (h *harness) upload(batch, id, name string, data []byte, chunk int) {
	h.t.Helper()
	for off := 0; off < len(data); off += chunk {
		end := min(off+chunk, len(data))
		q := url.Values{"batch": {batch}, "id": {id}, "name": {name}, "offset": {fmt.Sprint(off)}, "size": {fmt.Sprint(len(data))}}
		resp := h.req("POST", "/api/upload?"+q.Encode(), bytes.NewReader(data[off:end]), map[string]string{"Content-Type": "application/octet-stream"})
		resp.Body.Close()
		if resp.StatusCode != 200 {
			h.t.Fatalf("upload %s at %d: %d", name, off, resp.StatusCode)
		}
	}
}

func TestGuard(t *testing.T) {
	h := newHarness(t)
	// the page itself with ?t=
	resp, _ := http.Get(h.ts.URL + "/?t=" + h.u.Token)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `content="`+h.u.Token+`"`) || strings.Contains(string(b), "__TOKEN__") {
		t.Fatalf("page: %d", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Error("security headers")
	}
	// static files need the token too
	for _, p := range []string{"/app.js", "/app.css", "/", "/api/state"} {
		resp, _ := http.Get(h.ts.URL + p)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s without token: %d", p, resp.StatusCode)
		}
		resp, _ = http.Get(h.ts.URL + p + "?t=wrong" + h.u.Token[5:])
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s with wrong token: %d", p, resp.StatusCode)
		}
	}
	resp, _ = http.Get(h.ts.URL + "/app.js?t=" + h.u.Token)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("app.js: %d", resp.StatusCode)
	}
	// POST: token in the query is not enough, the header is needed
	r, _ := http.NewRequest("POST", h.ts.URL+"/api/cancel?t="+h.u.Token, nil)
	resp, _ = http.DefaultClient.Do(r)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("POST with query token: %d", resp.StatusCode)
	}
	// another Host (DNS rebinding) or Origin
	resp = h.req("GET", "/api/state", nil, map[string]string{"Origin": "http://evil.example"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin: %d", resp.StatusCode)
	}
	r, _ = http.NewRequest("GET", h.ts.URL+"/api/state", nil)
	r.Host = "evil.example:80"
	r.Header.Set("X-LL-Token", h.u.Token)
	resp, _ = http.DefaultClient.Do(r)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign host: %d", resp.StatusCode)
	}
	// not from this computer
	rec := httptest.NewRecorder()
	rq := httptest.NewRequest("GET", "/api/state", nil)
	rq.RemoteAddr = "192.168.1.20:5000"
	rq.Host = h.u.Addr
	rq.Header.Set("X-LL-Token", h.u.Token)
	h.u.Handler().ServeHTTP(rec, rq)
	if rec.Code != http.StatusForbidden {
		t.Errorf("remote address: %d", rec.Code)
	}
}

func TestListenLoopbackOnly(t *testing.T) {
	u := &UI{S: &Session{}}
	if err := u.Listen(0); err != nil {
		t.Fatal(err)
	}
	defer u.ln.Close()
	if !strings.HasPrefix(u.Addr, "127.0.0.1:") || len(u.Token) != 48 || !strings.HasPrefix(u.URL(), "http://127.0.0.1:") || !strings.Contains(u.URL(), "/?t="+u.Token) {
		t.Errorf("%s %s", u.Addr, u.URL())
	}
}

func TestSafeRel(t *testing.T) {
	ok := map[string]string{
		"NC2627.7z":                       "NC2627.7z",
		"SharePro Backup/2026/NC2627.7z":  filepath.Join("SharePro Backup", "2026", "NC2627.7z"),
		`SharePro Backup\2025\NC2526.bak`: filepath.Join("SharePro Backup", "2025", "NC2526.bak"),
		"Bkp (old)/NC 19-20 final.bak":    filepath.Join("Bkp (old)", "NC 19-20 final.bak"),
	}
	for in, want := range ok {
		if got, err := SafeRel(in); err != nil || got != want {
			t.Errorf("SafeRel(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "../x.bak", "a/../../x.bak", `..\..\Windows\x.bak`, "/etc/x.bak", `\\server\share\x.bak`, `C:\x.bak`, "C:x.bak", "a/./b.bak", "x.bak:stream", "CON", "nul.bak", "trailing.bak.", "trailing ", "a\x00b", "a\x01b", "a/" + strings.Repeat("b", 300)} {
		if _, err := SafeRel(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestUpload(t *testing.T) {
	h := newHarness(t)
	data := bytes.Repeat([]byte("0123456789"), 1000)
	q := func(off int, name string) string {
		return "/api/upload?" + url.Values{"batch": {"batch01"}, "id": {"file0001"}, "name": {name}, "offset": {fmt.Sprint(off)}, "size": {fmt.Sprint(len(data))}}.Encode()
	}
	// traversal refused, nothing written
	resp := h.req("POST", q(0, "../../escape.bak"), bytes.NewReader(data[:10]), nil)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("traversal: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(h.s.Work), "escape.bak")); err == nil {
		t.Fatal("wrote outside the work folder")
	}
	// first chunk
	resp = h.req("POST", q(0, "Backups/NC2627.bak"), bytes.NewReader(data[:4000]), nil)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("chunk 1: %d", resp.StatusCode)
	}
	// a chunk that does not follow on: 409 with what was received
	resp = h.req("POST", q(6000, "Backups/NC2627.bak"), bytes.NewReader(data[6000:]), nil)
	var out struct {
		Value struct{ Received int64 } `json:"value"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || out.Value.Received != 4000 {
		t.Errorf("gap: %d %+v", resp.StatusCode, out)
	}
	st := h.state()
	if len(st.Files) != 1 || st.Files[0].Status != "uploading" || st.Files[0].Received != 4000 {
		t.Fatalf("%+v", st.Files)
	}
	resp = h.req("POST", q(4000, "Backups/NC2627.bak"), bytes.NewReader(data[4000:]), nil)
	resp.Body.Close()
	st = h.state()
	if st.Files[0].Status != "ready" || st.Files[0].Received != int64(len(data)) || st.Files[0].Name != "Backups/NC2627.bak" {
		t.Fatalf("%+v", st.Files)
	}
	p := h.s.files[0].Path
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, data) {
		t.Error("content")
	}
	if rel, err := filepath.Rel(h.s.Work, p); err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("stored at %s", p)
	}
	if fi, _ := os.Stat(p); os.PathSeparator == '/' && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	// the same name again in the batch is refused
	resp = h.req("POST", "/api/upload?"+url.Values{"batch": {"batch01"}, "id": {"file0002"}, "name": {"Backups/NC2627.bak"}, "offset": {"0"}, "size": {"10"}}.Encode(), bytes.NewReader(data[:10]), nil)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("duplicate: %d", resp.StatusCode)
	}
	// bad ids
	resp = h.req("POST", "/api/upload?"+url.Values{"batch": {"../x"}, "id": {"file0003"}, "name": {"a.bak"}, "offset": {"0"}, "size": {"10"}}.Encode(), bytes.NewReader(data[:10]), nil)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("bad batch: %d", resp.StatusCode)
	}
	// RAR noted, not uploaded
	if c, _ := h.post("/api/skipped", map[string]string{"name": "old/NC1920.rar"}); c != 200 {
		t.Error("skipped")
	}
	st = h.state()
	if len(st.Files) != 2 || !strings.Contains(st.Files[1].Reason, "unpack it with WinRAR or 7-Zip first") {
		t.Errorf("%+v", st.Files)
	}
	// remove
	if c, _ := h.post("/api/files/remove", map[string]string{"id": "file0001"}); c != 200 {
		t.Error("remove")
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("removed file still on disk")
	}
}

func waitJob(t *testing.T, h *harness) Job {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		st := h.state()
		if st.Job != nil && !st.Job.Running {
			return *st.Job
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return Job{}
}

func TestWholeFlow(t *testing.T) {
	h := newHarness(t)
	h.s.FindServer(context.Background())
	st := h.state()
	if !sqlserverWindows() {
		if st.Server.Status != "login" {
			t.Fatalf("server %+v", st.Server)
		}
		if c, _ := h.post("/api/server/login", map[string]any{"endpoint": 0, "user": "sa", "password": "wrong"}); c != 400 {
			t.Error("wrong password accepted")
		}
		if c, out := h.post("/api/server/login", map[string]any{"endpoint": 0, "user": "sa", "password": "right"}); c != 200 {
			t.Fatalf("login: %v", out)
		}
	}
	st = h.state()
	if st.Server.Status != "ready" || !st.Server.Express || !strings.Contains(st.Server.Message, "10 GB") {
		t.Fatalf("server %+v", st.Server)
	}
	if strings.Contains(fmt.Sprint(st), "right") {
		t.Error("password in the state")
	}
	h.upload("batchA1", "upNC2627", "NC2627.bak", []byte("NC2627"), 3)
	h.upload("batchA1", "upNC2526", "NC2526.bak", []byte("NC2526"), 100)
	h.upload("batchA1", "upNCMAIL", "NCMAIL2627.bak", []byte("NCMAIL2627"), 100)
	h.upload("batchA1", "upADMIN1", "shadmin.bak", []byte("shadmin"), 100)
	h.upload("batchA1", "upNOTBAK", "notes.zip", []byte("not a zip"), 100)
	if c, out := h.post("/api/scan", nil); c != 200 {
		t.Fatalf("scan: %v", out)
	}
	if j := waitJob(t, h); j.Error != "" {
		t.Fatalf("scan: %s", j.Error)
	}
	st = h.state()
	if st.Screen != "found" || len(st.Found) != 4 {
		t.Fatalf("found %s %+v", st.Screen, st.Found)
	}
	if st.Found[0].Database != "NC2627" && st.Found[0].Database != "shadmin" {
		t.Errorf("order %+v", st.Found)
	}
	if len(st.Firms) != 1 || st.Firms[0].Code != "01" {
		t.Errorf("firms %+v", st.Firms)
	}
	var notZip bool
	for _, f := range st.Files {
		if f.Name == "notes.zip" && f.Status == "skipped" {
			notZip = true
		}
	}
	if !notZip {
		t.Error("bad zip not skipped")
	}
	// skip the older year and use it again
	for _, f := range st.Found {
		if f.Database == "NC2526" {
			h.post("/api/toggle", map[string]any{"index": f.Index, "skip": true})
			h.post("/api/toggle", map[string]any{"index": f.Index, "skip": false})
		}
	}
	h.post("/api/go", map[string]string{"screen": "choose"})
	// refusals
	out := t.TempDir()
	c, res := h.post("/api/start", map[string]any{"years": 8, "documents": true, "out": out, "passphrase": "short", "passphrase2": "short"})
	if c != 400 || !strings.Contains(fmt.Sprint(res["error"]), "at least 12") {
		t.Errorf("short: %v", res)
	}
	c, res = h.post("/api/start", map[string]any{"years": 8, "documents": true, "out": out, "passphrase": "one long passphrase", "passphrase2": "another long passphrase"})
	if c != 400 || !strings.Contains(fmt.Sprint(res["error"]), "not the same") {
		t.Errorf("mismatch: %v", res)
	}
	c, res = h.post("/api/start", map[string]any{"years": 8, "documents": true, "out": filepath.Join(h.s.Work, "x"), "passphrase": "one long passphrase", "passphrase2": "one long passphrase"})
	if c != 400 || !strings.Contains(fmt.Sprint(res["error"]), "work folder") {
		t.Errorf("inside work: %v", res)
	}
	if _, fi := h.post("/api/check-folder", map[string]string{"out": out}); fi["ok"] != true {
		t.Errorf("folder %v", fi)
	}
	c, res = h.post("/api/start", map[string]any{"years": 8, "documents": true, "dormant": "inactive", "out": out, "passphrase": "one long passphrase", "passphrase2": "one long passphrase", "max_part_gb": 4})
	if c != 200 {
		t.Fatalf("start: %v", res)
	}
	if j := waitJob(t, h); j.Error != "" {
		t.Fatalf("run: %s", j.Error)
	}
	st = h.state()
	if st.Screen != "done" || st.Result == nil || len(st.Result.Parts) != 1 {
		t.Fatalf("done: %s %+v", st.Screen, st.Result)
	}
	if strings.Contains(fmt.Sprintf("%+v", st), "one long passphrase") {
		t.Error("passphrase in the state")
	}
	f, err := os.Open(st.Result.Parts[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := pkg.Open(f, "one long passphrase", t.TempDir(), 0)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Has("firms/01/master/CLIENT-MASTER.csv") || !r.Has("firms/01/history/2025-26/LEDGER.csv") || !r.Has("firms/01/documents/index.csv") {
		t.Error("package contents")
	}
	used := map[string]bool{}
	for _, b := range r.Manifest.Source.Backups {
		used[b.File] = b.Used
	}
	if !used["NC2627.bak"] || used["notes.zip"] {
		t.Errorf("manifest backups %v", used)
	}
	// temporary databases dropped after the run
	h.sql.mu.Lock()
	dropped := len(h.sql.dropped)
	h.sql.mu.Unlock()
	if dropped != 4 {
		t.Errorf("%d databases dropped", dropped)
	}
	// finish: work folder removed
	if c, _ := h.post("/api/quit", nil); c != 200 {
		t.Error("quit")
	}
	select {
	case <-h.u.Quit:
	case <-time.After(5 * time.Second):
		t.Fatal("quit not signalled")
	}
	h.s.Close()
	if _, err := os.Stat(h.s.Work); !errors.Is(err, os.ErrNotExist) {
		t.Error("work folder left behind")
	}
}

func sqlserverWindows() bool { return sqlserver.WindowsSignIn }

func TestLiveDatabases(t *testing.T) {
	h := newHarness(t)
	h.s.FindServer(context.Background())
	h.post("/api/server/login", map[string]any{"endpoint": 0, "user": "sa", "password": "right"})
	if c, out := h.post("/api/live/list", nil); c != 200 {
		t.Fatalf("list %v", out)
	}
	if c, _ := h.post("/api/live/use", map[string]any{"names": []string{"NOPE"}}); c != 400 {
		t.Error("unknown database accepted")
	}
	if c, out := h.post("/api/live/use", map[string]any{"names": []string{"NC2627", "shadmin"}}); c != 200 {
		t.Fatalf("use %v", out)
	}
	if j := waitJob(t, h); j.Error != "" {
		t.Fatal(j.Error)
	}
	st := h.state()
	if st.Screen != "found" || len(st.Found) != 2 || !st.Found[0].Live || !strings.HasPrefix(st.Found[0].Date, "Live") {
		t.Fatalf("%+v", st.Found)
	}
	if len(st.Warnings) != 0 {
		t.Errorf("live database warned as stale: %v", st.Warnings)
	}
}

func TestCancelScanAndBack(t *testing.T) {
	h := newHarness(t)
	h.s.FindServer(context.Background())
	h.post("/api/server/login", map[string]any{"endpoint": 0, "user": "sa", "password": "right"})
	h.upload("batchB1", "upNC2627", "NC2627.bak", []byte("NC2627"), 100)
	h.post("/api/scan", nil)
	waitJob(t, h)
	if c, _ := h.post("/api/go", map[string]string{"screen": "drop"}); c != 200 {
		t.Fatal("back")
	}
	st := h.state()
	if st.Screen != "drop" || st.Files[0].Status != "ready" || len(h.sql.dropped) != 1 {
		t.Errorf("after back: %s %+v %d", st.Screen, st.Files, len(h.sql.dropped))
	}
}

func TestParseServer(t *testing.T) {
	for in, want := range map[string]sqlserver.Endpoint{
		`localhost\SQLEXPRESS`: {Host: "localhost", Instance: "SQLEXPRESS"},
		`.\SHAREPRO`:           {Host: "localhost", Instance: "SHAREPRO"},
		`(local)`:              {Host: "localhost"},
		`127.0.0.1,1433`:       {Host: "127.0.0.1", Port: 1433},
		`localhost,50211`:      {Host: "127.0.0.1", Port: 50211},
		`.\SQLEXPRESS,50211`:   {Host: "127.0.0.1", Instance: "SQLEXPRESS", Port: 50211},
	} {
		if got, err := ParseServer(in); err != nil || got != want {
			t.Errorf("ParseServer(%q) = %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{`10.0.0.5\SQLEXPRESS`, "sharepro.example.com", "localhost,abc"} {
		if _, err := ParseServer(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestNoServerMessages(t *testing.T) {
	for goos, want := range map[string]string{"windows": "install the free SQL Server 2022 Express", "darwin": "There is no SQL Server for the Mac", "linux": "Docker"} {
		s, _ := NewSession(&mt.Mapper{}, t.TempDir())
		s.GOOS = goos
		s.Connector = &fakeConn{}
		s.FindServer(context.Background())
		st := s.State()
		if st.Server.Status != "none" || !strings.Contains(st.Server.Message, want) || st.Server.CanDocker != (goos != "windows") {
			t.Errorf("%s: %+v", goos, st.Server)
		}
		s.Close()
	}
}

func TestCancelRun(t *testing.T) {
	h := newHarness(t)
	m := &mt.Mapper{Block: make(chan struct{}), BlockStarted: make(chan struct{})}
	h.s.Mapper = m
	h.s.FindServer(context.Background())
	h.post("/api/server/login", map[string]any{"endpoint": 0, "user": "sa", "password": "right"})
	h.upload("batchC1", "upNC2627", "NC2627.bak", []byte("NC2627"), 100)
	h.upload("batchC1", "upNCMAIL", "NCMAIL2627.bak", []byte("NCMAIL2627"), 100)
	h.post("/api/scan", nil)
	if j := waitJob(t, h); j.Error != "" {
		t.Fatal(j.Error)
	}
	out := t.TempDir()
	if c, res := h.post("/api/start", map[string]any{"years": 8, "documents": true, "out": out, "passphrase": "one long passphrase", "passphrase2": "one long passphrase"}); c != 200 {
		t.Fatal(res)
	}
	<-m.BlockStarted
	if c, _ := h.post("/api/toggle", map[string]any{"index": 0, "skip": true}); c != 400 {
		t.Error("changed the choice while running")
	}
	h.post("/api/cancel", nil)
	j := waitJob(t, h)
	if j.Error != "Cancelled." {
		t.Errorf("job error %q", j.Error)
	}
	if st := h.state(); st.Screen != "drop" {
		t.Errorf("screen %s", st.Screen)
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Errorf("left %v", ents)
	}
	h.sql.mu.Lock()
	defer h.sql.mu.Unlock()
	if len(h.sql.dropped) != 2 {
		t.Errorf("%d temporary databases dropped", len(h.sql.dropped))
	}
}
