package sqlserver

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalise(t *testing.T) {
	cases := []struct {
		typ  string
		in   any
		want any
	}{
		{"DECIMAL", []byte("12345678.90"), "12345678.90"},
		{"MONEY", []byte("-0.0100"), "-0.0100"},
		{"NUMERIC", []byte("1"), "1"},
		{"CHAR", "T001      ", "T001"},
		{"NCHAR", "  A  ", "  A"},
		{"VARCHAR", "kept  ", "kept  "},
		{"INT", int64(5), int64(5)},
		{"TINYINT", uint8(7), int64(7)},
		{"REAL", float32(1.5), float64(1.5)},
		{"BIT", true, true},
		{"VARBINARY", []byte{1, 2}, []byte{1, 2}},
		{"INT", nil, nil},
	}
	for _, c := range cases {
		got := Normalise(c.typ, c.in)
		if b, ok := c.want.([]byte); ok {
			if g, ok := got.([]byte); !ok || string(g) != string(b) {
				t.Errorf("%s %v: got %#v", c.typ, c.in, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s %#v: got %#v want %#v", c.typ, c.in, got, c.want)
		}
	}
	// a GUID as SQL Server stores it (mixed endian) comes out as text
	g := Normalise("UNIQUEIDENTIFIER", []byte{0x67, 0x45, 0x23, 0x01, 0xab, 0x89, 0xef, 0xcd, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef})
	if g != "01234567-89AB-CDEF-0123-456789ABCDEF" {
		t.Errorf("guid: %v", g)
	}
	d := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if Normalise("DATETIME", d) != d {
		t.Error("time changed")
	}
}

func TestReadOnly(t *testing.T) {
	ok := []string{
		"SELECT * FROM ClientMaster",
		"  select a, b from t where x = 'DELETE me' -- UPDATE in a comment",
		"WITH x AS (SELECT 1 AS a) SELECT a FROM x",
		"SELECT [update], [Insert Date] FROM t",
		"/* header */ SELECT a INTO #tmp FROM t; SELECT * FROM #tmp",
		"DECLARE @d date = '2026-04-01'; SELECT * FROM t WHERE d >= @d",
		"SET NOCOUNT ON; SELECT 1",
		"SELECT exec_date, created_by FROM t",
	}
	bad := []string{
		"UPDATE t SET a = 1",
		"SELECT 1; DELETE FROM t",
		"SELECT a INTO newtable FROM t",
		"EXEC sp_who",
		"SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED; SELECT 1",
		"SELECT * FROM t WITH (NOLOCK)",
		"SELECT * FROM t (readuncommitted)",
		"DROP TABLE t",
		"USE master; SELECT 1",
		"SELECT * FROM OPENROWSET('x','y','z')",
	}
	for _, q := range ok {
		if err := ReadOnly(q); err != nil {
			t.Errorf("refused %q: %v", q, err)
		}
	}
	for _, q := range bad {
		if err := ReadOnly(q); !errors.Is(err, ErrNotReadOnly) {
			t.Errorf("accepted %q", q)
		}
	}
}

func TestDSN(t *testing.T) {
	d := dsn(Endpoint{Host: "localhost", Instance: "SQLEXPRESS"}, Login{}, "NC2627")
	u, err := url.Parse(d)
	if err != nil {
		t.Fatal(err)
	}
	if u.User != nil || u.Path != "/SQLEXPRESS" || u.Query().Get("database") != "NC2627" {
		t.Errorf("windows sign-in dsn: %s", d)
	}
	d = dsn(Endpoint{Host: "127.0.0.1", Port: 50123, Docker: true}, Login{User: "sa", Password: "p@ss;word"}, "")
	u, _ = url.Parse(d)
	if pw, _ := u.User.Password(); pw != "p@ss;word" || u.Host != "127.0.0.1:50123" || u.Path != "" {
		t.Errorf("sql login dsn: %s", d)
	}
	e := Endpoint{Host: "127.0.0.1", Port: 50123, Docker: true}
	if s := e.String(); strings.Contains(s, "p@ss") || s != "127.0.0.1,50123 (Docker)" {
		t.Errorf("String: %s", s)
	}
	if s := (Endpoint{Host: "localhost", Instance: "SHAREPRO"}).String(); s != `localhost\SHAREPRO` {
		t.Error(s)
	}
}

func TestParseBrowser(t *testing.T) {
	body := "ServerName;OFFICE1;InstanceName;SQLEXPRESS;IsClustered;No;Version;15.0.2000.5;tcp;50211;np;\\\\OFFICE1\\pipe\\MSSQL$SQLEXPRESS\\sql\\query;;" +
		"ServerName;OFFICE1;InstanceName;MSSQLSERVER;IsClustered;No;Version;16.0.1000.6;tcp;1433;;"
	msg := append([]byte{5, byte(len(body)), byte(len(body) >> 8)}, body...)
	in := ParseBrowser(msg)
	if len(in) != 2 || in[0].Name != "SQLEXPRESS" || in[0].TCP != 50211 || in[1].Name != "MSSQLSERVER" || in[1].TCP != 1433 || in[1].Version != "16.0.1000.6" {
		t.Fatalf("%+v", in)
	}
	if ParseBrowser([]byte{4, 0, 0}) != nil || ParseBrowser(nil) != nil {
		t.Error("garbage parsed")
	}
}

func TestRank(t *testing.T) {
	if !(rank(Endpoint{}) < rank(Endpoint{Instance: "SHAREPRO"}) && rank(Endpoint{Instance: "SHAREPRO"}) < rank(Endpoint{Instance: "sqlexpress"})) {
		t.Error("rank order")
	}
}

// ---- Docker ----

type fakeDocker struct {
	mu      sync.Mutex
	found   bool
	info    string
	infoErr error
	image   bool
	running []bool // successive answers to inspect
	logs    string
	calls   [][]string
	envs    [][]string
	pullOut []string
}

func (f *fakeDocker) Found() bool { return f.found }

func (f *fakeDocker) Run(ctx context.Context, env []string, line func(string), args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	f.envs = append(f.envs, env)
	switch args[0] {
	case "info":
		return []byte(f.info), f.infoErr
	case "image":
		if f.image {
			return []byte("sha256:abc"), nil
		}
		return nil, errors.New("no such image")
	case "pull":
		for _, l := range f.pullOut {
			line(l)
		}
		f.image = true
		return nil, nil
	case "run":
		return []byte("cid\n"), nil
	case "port":
		return []byte("127.0.0.1:50999\n"), nil
	case "inspect":
		r := true
		if len(f.running) > 0 {
			r = f.running[0]
			f.running = f.running[1:]
		}
		if r {
			return []byte("true\n"), nil
		}
		return []byte("false\n"), nil
	case "logs":
		return []byte(f.logs), nil
	case "rm":
		return nil, nil
	}
	return nil, errors.New("unexpected " + args[0])
}

func (f *fakeDocker) called(cmd string) []string {
	for _, c := range f.calls {
		if c[0] == cmd {
			return c
		}
	}
	return nil
}

func TestDockerCheck(t *testing.T) {
	ctx := context.Background()
	d := &Docker{R: &fakeDocker{}, GOOS: "darwin"}
	var de *DockerError
	if err := d.Check(ctx); !errors.As(err, &de) || de.Step != "installed" {
		t.Fatalf("not installed: %v", err)
	}
	d.R = &fakeDocker{found: true, infoErr: errors.New("cannot connect")}
	if err := d.Check(ctx); !errors.As(err, &de) || de.Step != "running" || !strings.Contains(de.Msg, "Open Docker Desktop") {
		t.Fatalf("not running: %v", err)
	}
	d.R = &fakeDocker{found: true, info: `{"MemTotal": 1073741824, "Architecture": "aarch64"}`}
	if err := d.Check(ctx); !errors.As(err, &de) || de.Step != "memory" {
		t.Fatalf("1 GB: %v", err)
	}
	d.R = &fakeDocker{found: true, info: `{"MemTotal": 3221225472}`}
	if err := d.Check(ctx); err != nil || len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "3.0 GB") {
		t.Fatalf("3 GB: %v %v", err, d.Warnings)
	}
	d.R = &fakeDocker{found: true, info: `{"MemTotal": 8589934592}`}
	if err := d.Check(ctx); err != nil || len(d.Warnings) != 0 {
		t.Fatalf("8 GB: %v %v", err, d.Warnings)
	}
}

func TestDockerStart(t *testing.T) {
	work := t.TempDir()
	f := &fakeDocker{found: true, pullOut: []string{"2022-latest: Pulling from mssql/server", "a1b2c3d4e5f6: Pulling fs layer", "b1b2c3d4e5f6: Pulling fs layer", "a1b2c3d4e5f6: Pull complete", "b1b2c3d4e5f6: Pull complete", "Status: Downloaded newer image"}}
	var msgs []string
	tries := 0
	d := &Docker{R: f, Work: work, GOOS: "darwin", GOARCH: "arm64", Status: func(m string) { msgs = append(msgs, m) }, Wait: time.Minute}
	d.connect = func(ctx context.Context, e Endpoint, l Login) (*Server, error) {
		tries++
		if e.Host != "127.0.0.1" || e.Port != 50999 || l.User != "sa" || len(l.Password) < 20 {
			t.Errorf("connect to %v as %s", e, l.User)
		}
		return &Server{End: e}, nil
	}
	s, err := d.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !s.End.Docker || tries != 1 {
		t.Errorf("server %v tries %d", s.End, tries)
	}
	run := f.called("run")
	joined := strings.Join(run, " ")
	for _, want := range []string{"--platform linux/amd64", "MSSQL_PID=Express", "-p 127.0.0.1::1433", work + ":/work:ro", Image} {
		if !strings.Contains(joined, want) {
			t.Errorf("docker run lacks %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, d.password) {
		t.Error("password on the docker command line")
	}
	for i, c := range f.calls {
		if c[0] == "run" && (len(f.envs[i]) != 1 || !strings.HasPrefix(f.envs[i][0], "MSSQL_SA_PASSWORD=")) {
			t.Error("password not passed through the environment")
		}
	}
	if f.called("pull") == nil || !strings.Contains(strings.Join(msgs, "\n"), "2 of 2 parts done") {
		t.Errorf("pull progress: %v", msgs)
	}
	if p, ok := d.ServerPath(filepath.Join(work, "unpacked", "NC 2627.bak")); !ok || p != "/work/unpacked/NC 2627.bak" {
		t.Errorf("ServerPath: %s %v", p, ok)
	}
	if _, ok := d.ServerPath(filepath.Join(filepath.Dir(work), "elsewhere.bak")); ok {
		t.Error("a file outside the work folder mapped")
	}
	if err := d.Stop(context.Background()); err != nil || f.called("rm") == nil || d.Running() {
		t.Error("container not removed")
	}
	for _, c := range f.calls {
		if c[0] == "rmi" || (c[0] == "image" && len(c) > 1 && c[1] == "rm") {
			t.Error("the image was removed")
		}
	}
}

func TestDockerRosetta(t *testing.T) {
	f := &fakeDocker{found: true, image: true, running: []bool{false}, logs: "rosetta error: failed to open elf"}
	d := &Docker{R: f, Work: t.TempDir(), GOOS: "darwin", GOARCH: "arm64"}
	d.connect = func(ctx context.Context, e Endpoint, l Login) (*Server, error) { return nil, errors.New("refused") }
	_, err := d.Start(context.Background())
	var de *DockerError
	if !errors.As(err, &de) || de.Step != "rosetta" || !strings.Contains(de.Msg, "Use Rosetta") {
		t.Fatalf("got %v", err)
	}
	if f.called("rm") == nil {
		t.Error("failed container not removed")
	}
	if f.called("pull") != nil {
		t.Error("pulled although the image was there")
	}
}

func TestPassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		p := Password()
		if len(p) != 28 || !strings.ContainsAny(p, "ABCDEFGHJKLMNPQRSTUVWXYZ") || !strings.ContainsAny(p, "23456789") || !strings.ContainsAny(p, "#%+-=_") || seen[p] {
			t.Fatalf("weak or repeated password")
		}
		seen[p] = true
	}
}
