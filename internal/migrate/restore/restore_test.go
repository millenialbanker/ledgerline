package restore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/migrate"
)

type sqlErr struct{ n int32 }

func (e sqlErr) Error() string         { return "mssql: error " + string(rune('0'+e.n%10)) }
func (e sqlErr) SQLErrorNumber() int32 { return e.n }

// fakeServer answers the statements restore sends.
type fakeServer struct {
	info     migrate.Row
	free     int64
	readable map[string]bool // server paths it can open
	header   []migrate.Row
	files    []migrate.Row
	execs    []string
	queries  []string
	dbs      []migrate.Row
	execErr  error
}

func (f *fakeServer) Rows(ctx context.Context, q string, args ...any) ([]migrate.Row, error) {
	f.queries = append(f.queries, q)
	switch {
	case strings.Contains(q, "SERVERPROPERTY"):
		return []migrate.Row{f.info}, nil
	case strings.Contains(q, "dm_os_volume_stats"):
		return []migrate.Row{{"free": f.free}}, nil
	case strings.HasPrefix(q, "RESTORE HEADERONLY"), strings.HasPrefix(q, "RESTORE FILELISTONLY"):
		path := q[strings.Index(q, "N'")+2:]
		path = path[:strings.Index(path, "'")]
		if !f.readable[path] {
			return nil, sqlErr{3201}
		}
		if strings.HasPrefix(q, "RESTORE HEADERONLY") {
			return f.header, nil
		}
		return f.files, nil
	case strings.Contains(q, "sys.databases"):
		return f.dbs, nil
	}
	return nil, errors.New("unexpected query " + q)
}

func (f *fakeServer) Exec(ctx context.Context, q string, args ...any) error {
	f.execs = append(f.execs, q)
	return f.execErr
}

func newFake() *fakeServer {
	return &fakeServer{
		info: migrate.Row{"version": "15.0.2000.5", "edition": "Express Edition (64-bit)", "engine": int64(4), "datapath": `C:\Program Files\Microsoft SQL Server\MSSQL15.SQLEXPRESS\MSSQL\DATA\`, "backuppath": `C:\Program Files\Microsoft SQL Server\MSSQL15.SQLEXPRESS\MSSQL\Backup`, "dbversion": int64(904)},
		free: 500 << 30,
		header: []migrate.Row{
			{"backuptype": int64(1), "position": int64(1), "databasename": "NC2526", "databaseversion": int64(869), "backupsize": "1000", "backupfinishdate": time.Date(2026, 3, 31, 22, 0, 0, 0, time.UTC)},
			{"backuptype": int64(1), "position": int64(2), "databasename": "NC2627", "databaseversion": int64(904), "backupsize": "2147483648", "backupfinishdate": time.Date(2026, 11, 11, 22, 0, 0, 0, time.UTC)},
			{"backuptype": int64(2), "position": int64(3), "databasename": "NC2627", "databaseversion": int64(904)},
		},
		files: []migrate.Row{
			{"logicalname": "NC2627", "physicalname": `D:\SharePro\Data\NC2627.mdf`, "type": "D", "size": "3221225472"},
			{"logicalname": "NC2627_2", "physicalname": `D:\SharePro\Data\NC2627_2.ndf`, "type": "D", "size": "1073741824"},
			{"logicalname": "NC2627_log", "physicalname": `D:\SharePro\Data\NC2627_log.ldf`, "type": "L", "size": "536870912"},
		},
	}
}

func TestServerInfo(t *testing.T) {
	f := newFake()
	in, err := ServerInfo(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if !in.Express() || in.DBVersion != 904 || in.Free != 500<<30 || in.Name() != "SQL Server 2019 Express" {
		t.Errorf("%+v %s", in, in.Name())
	}
}

func TestInspectCopiesWhenUnreadable(t *testing.T) {
	f := newFake()
	in, _ := ServerInfo(context.Background(), f)
	local := `C:\Users\Office Staff\AppData\Local\Temp\llmig-1\unpacked\NC2627 o'clock.bak`
	copied := ""
	removed := false
	r := &Restorer{S: f, Info: in, Copy: func(l, dir string) (string, func(), error) {
		copied = JoinServer(dir, "LLMIG_copy_NC2627.bak")
		f.readable[copied] = true
		return copied, func() { removed = true }, nil
	}}
	f.readable = map[string]bool{}
	b, err := r.Inspect(context.Background(), local, 2<<30)
	if err != nil {
		t.Fatal(err)
	}
	if copied != `C:\Program Files\Microsoft SQL Server\MSSQL15.SQLEXPRESS\MSSQL\Backup\LLMIG_copy_NC2627.bak` || b.ServerPath != copied {
		t.Errorf("copied to %q, server path %q", copied, b.ServerPath)
	}
	// the last full backup set in the file, not the differential after it
	if b.Position != 2 || b.Database != "NC2627" || b.DBVersion != 904 || !b.Finished.Equal(time.Date(2026, 11, 11, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("%+v", b)
	}
	if b.DataBytes() != 4<<30 || b.RestoredBytes() != 4<<30+512<<20 {
		t.Errorf("sizes %d %d", b.DataBytes(), b.RestoredBytes())
	}
	if !strings.Contains(strings.Join(f.queries, "\n"), "WITH FILE = 2") {
		t.Error("FILELISTONLY not for the chosen set")
	}
	r.Cleanup(false)
	if !removed {
		t.Error("copied backup not removed")
	}
}

func TestInspectUnreadableNoCopy(t *testing.T) {
	f := newFake()
	f.readable = map[string]bool{}
	in, _ := ServerInfo(context.Background(), f)
	r := &Restorer{S: f, Info: in}
	_, err := r.Inspect(context.Background(), "/tmp/x.bak", 10)
	var p *Problem
	if !errors.As(err, &p) || !strings.Contains(p.Msg, "cannot read this file") {
		t.Fatalf("%v", err)
	}
}

func TestChecks(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	f.readable = map[string]bool{"/w/a.bak": true}
	in, _ := ServerInfo(ctx, f)
	r := &Restorer{S: f, Info: in}
	b, err := r.Inspect(ctx, "/w/a.bak", 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Check(ctx, b); err != nil {
		t.Fatalf("fits: %v", err)
	}

	// too new for the server
	b.DBVersion = 957
	err = r.Check(ctx, b)
	if err == nil || !strings.Contains(err.Error(), "made by SQL Server 2022; this computer has SQL Server 2019 Express, which cannot read it") {
		t.Errorf("version: %v", err)
	}
	b.DBVersion = 904

	// Express size limit, three wordings
	b.Files[0].Bytes = 22<<30 + 512<<20
	b.Files[1].Bytes = 1 << 30
	if err := r.Check(ctx, b); err == nil || err.Error() != "This backup is 23.5 GB; SQL Server Express can only hold 10 GB. Run the tool on the SharePro server, whose SQL Server can restore it." {
		t.Errorf("express: %v", err)
	}
	r.Docker, r.Mac = true, true
	if err := r.Check(ctx, b); err == nil || err.Error() != "This database (23.5 GB) is too large for the free SQL Server Express on a Mac. Run the tool on the Windows server where SharePro runs for this backup, or leave it out." {
		t.Errorf("mac: %v", err)
	}
	// Developer edition has no limit
	r.Info.EngineEdition = 3
	r.Info.Edition = "Developer Edition (64-bit)"
	if err := r.Check(ctx, b); err != nil {
		t.Errorf("developer: %v", err)
	}

	// disk: three times the backup file
	f.free = 5 << 30
	b.Files[0].Bytes, b.Files[1].Bytes = 1<<30, 0
	b.FileBytes = 2 << 30
	err = r.Check(ctx, b)
	if err == nil || !strings.Contains(err.Error(), "needs about 6.0 GB free to restore NC2627 but its disk has 5.0 GB free") || !strings.Contains(err.Error(), "Docker Desktop") {
		t.Errorf("disk: %v", err)
	}
}

func TestRestoreSQLAndCleanup(t *testing.T) {
	b := &Backup{ServerPath: `C:\Backup\NC2627 o'clock.bak`, Position: 2, Database: "NC2627", Files: []File{
		{Logical: "NC2627", Type: "D"}, {Logical: "NC2627_2", Type: "D"}, {Logical: "NC]log", Type: "L"}, {Logical: "ft", Type: "F"},
	}}
	got := RestoreSQL("LLMIG_abc123_NC2627", b, `C:\SQL\DATA\`)
	want := `RESTORE DATABASE [LLMIG_abc123_NC2627] FROM DISK = N'C:\Backup\NC2627 o''clock.bak' WITH FILE = 2, ` +
		`MOVE N'NC2627' TO N'C:\SQL\DATA\LLMIG_abc123_NC2627_0.mdf', ` +
		`MOVE N'NC2627_2' TO N'C:\SQL\DATA\LLMIG_abc123_NC2627_1.ndf', ` +
		`MOVE N'NC]log' TO N'C:\SQL\DATA\LLMIG_abc123_NC2627_log2.ldf', ` +
		`MOVE N'ft' TO N'C:\SQL\DATA\LLMIG_abc123_NC2627_ft3', RECOVERY`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if g := RestoreSQL("LLMIG_abc123_X", &Backup{ServerPath: "/work/x.bak", Position: 1, Files: []File{{Logical: "x", Type: "D"}}}, "/var/opt/mssql/data"); !strings.Contains(g, "N'/var/opt/mssql/data/LLMIG_abc123_X_0.mdf'") {
		t.Error(g)
	}

	f := newFake()
	r := &Restorer{S: f, Info: Info{DataPath: `C:\SQL\DATA`}}
	var forgot []string
	r.Forget = func(n string) { forgot = append(forgot, n) }
	name, err := r.Restore(context.Background(), b)
	if err != nil || !IsTemp(name) || !strings.HasPrefix(name, "LLMIG_") || !strings.HasSuffix(name, "_NC2627") {
		t.Fatalf("%s %v", name, err)
	}
	if !strings.HasPrefix(f.execs[0], "RESTORE DATABASE ["+name+"]") {
		t.Error(f.execs[0])
	}
	// kept on request
	if errs := r.Cleanup(true); len(errs) != 0 || len(f.execs) != 1 {
		t.Error("dropped although kept")
	}
	r.created = []string{name, "NC2627"}
	errs := r.Cleanup(false)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "refusing to drop NC2627") {
		t.Errorf("errs %v", errs)
	}
	if len(f.execs) != 2 || f.execs[1] != DropSQL(name) || len(forgot) != 1 {
		t.Errorf("execs %v", f.execs)
	}
	if !strings.Contains(DropSQL(name), "SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE ["+name+"]") {
		t.Error(DropSQL(name))
	}
}

func TestNames(t *testing.T) {
	n := TempName("NC 26-27")
	if !IsTemp(n) || len(n) != len("LLMIG_abcdef_NC_26_27") {
		t.Error(n)
	}
	if TempName("A") == TempName("A") {
		t.Error("not random")
	}
	for _, bad := range []string{"NC2627", "LLMIG_ABCDEF_x", "LLMIG_abc_x", "llmig_abcdef_x", "LLMIG_abcdef_x]; DROP DATABASE y"} {
		if IsTemp(bad) {
			t.Error(bad)
		}
	}
	if JoinServer(`D:\Data`, "a.mdf") != `D:\Data\a.mdf` || JoinServer("/var/opt/mssql/data/", "a.mdf") != "/var/opt/mssql/data/a.mdf" || JoinServer(`C:`, "a") != `C:\a` {
		t.Error("JoinServer")
	}
	if ReleaseName(904) != "2019" || ReleaseName(957) != "2022" || ReleaseName(782) != "2014" || ReleaseName(661) != "2008 R2" {
		t.Error("ReleaseName")
	}
}

func TestListDatabases(t *testing.T) {
	f := newFake()
	f.dbs = []migrate.Row{{"name": "shadmin", "bytes": int64(8 << 20)}, {"name": "NC2627", "bytes": int64(4 << 30)}}
	dbs, err := ListDatabases(context.Background(), f)
	if err != nil || len(dbs) != 2 || dbs[0].Name != "NC2627" || dbs[0].Bytes != 4<<30 {
		t.Fatalf("%v %v", dbs, err)
	}
	if !strings.Contains(f.queries[0], "NOT LIKE 'LLMIG[_]%'") {
		t.Error("temporary databases listed")
	}
}
