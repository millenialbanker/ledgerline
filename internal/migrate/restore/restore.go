// Package restore restores the old system's backups into temporary
// databases on a SQL Server for the migration tool to read, checks first
// that the server can (version, size limit, disk), and drops them again at
// the end.
//
// Temporary databases are named LLMIG_<6 random>_<original> and are the
// only databases the tool ever creates or drops; Cleanup refuses any other
// name. A database already on the server (SharePro's live server) is read
// in place and never changed.
package restore

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/migrate"
)

// Server is what restore needs from a SQL Server connection
// (sqlserver.Server implements it).
type Server interface {
	Rows(ctx context.Context, q string, args ...any) ([]migrate.Row, error)
	Exec(ctx context.Context, q string, args ...any) error
}

// Info describes the server.
type Info struct {
	Version       string // 16.0.4135.4
	Edition       string // Express Edition (64-bit)
	EngineEdition int    // 4 is Express
	DBVersion     int    // internal database version of master: the newest it can restore
	DataPath      string // InstanceDefaultDataPath
	BackupPath    string // InstanceDefaultBackupPath
	Free          int64  // bytes free on the data disk; -1 unknown
}

// Express says whether the server is SQL Server Express (10 GB a database).
func (i Info) Express() bool { return i.EngineEdition == 4 }

// Name is the product name, e.g. "SQL Server 2019 Express".
func (i Info) Name() string {
	s := "SQL Server " + ReleaseName(i.DBVersion)
	if i.Express() {
		s += " Express"
	} else if strings.Contains(strings.ToLower(i.Edition), "developer") {
		s += " Developer"
	}
	return s
}

// ExpressLimit is the most data SQL Server Express holds in one database.
const ExpressLimit = 10 << 30

// ReleaseName maps an internal database version to a SQL Server release.
func ReleaseName(v int) string {
	switch {
	case v >= 998:
		return "2025"
	case v >= 950:
		return "2022"
	case v >= 895:
		return "2019"
	case v >= 868:
		return "2017"
	case v >= 852:
		return "2016"
	case v >= 782:
		return "2014"
	case v >= 706:
		return "2012"
	case v >= 660:
		return "2008 R2"
	case v >= 655:
		return "2008"
	case v > 0:
		return "2005 or older"
	}
	return "(unknown version)"
}

func str(r migrate.Row, k string) string {
	switch v := r[k].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func num(r migrate.Row, k string) int64 {
	switch v := r[k].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		if i := strings.IndexByte(v, '.'); i >= 0 {
			v = v[:i]
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	case []byte:
		n, _ := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
		return n
	}
	return 0
}

// ServerInfo reads the server's version, edition, folders and free space.
func ServerInfo(ctx context.Context, s Server) (Info, error) {
	rows, err := s.Rows(ctx, `SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)) AS version,
 CAST(SERVERPROPERTY('Edition') AS nvarchar(128)) AS edition,
 CAST(SERVERPROPERTY('EngineEdition') AS int) AS engine,
 CAST(SERVERPROPERTY('InstanceDefaultDataPath') AS nvarchar(512)) AS datapath,
 CAST(SERVERPROPERTY('InstanceDefaultBackupPath') AS nvarchar(512)) AS backuppath,
 CAST(DATABASEPROPERTYEX('master', 'Version') AS int) AS dbversion`)
	if err != nil {
		return Info{}, err
	}
	if len(rows) == 0 {
		return Info{}, errors.New("SQL Server did not say what version it is")
	}
	r := rows[0]
	in := Info{Version: str(r, "version"), Edition: str(r, "edition"), EngineEdition: int(num(r, "engine")), DBVersion: int(num(r, "dbversion")), DataPath: str(r, "datapath"), BackupPath: str(r, "backuppath")}
	if in.DataPath == "" {
		// SQL Server 2008 R2 and older: the folder of master's data file
		if rows, err := s.Rows(ctx, `SELECT physical_name AS p FROM sys.master_files WHERE database_id = 1 AND file_id = 1`); err == nil && len(rows) > 0 {
			in.DataPath = dirOf(str(rows[0], "p"))
		}
	}
	in.Free = freeBytes(ctx, s)
	return in, nil
}

func freeBytes(ctx context.Context, s Server) int64 {
	rows, err := s.Rows(ctx, `SELECT TOP 1 CAST(available_bytes AS bigint) AS free FROM sys.dm_os_volume_stats(1, 1)`)
	if err != nil || len(rows) == 0 {
		return -1
	}
	return num(rows[0], "free")
}

// windowsPath says whether a server path is a Windows one.
func windowsPath(p string) bool {
	return strings.Contains(p, `\`) || (len(p) >= 2 && p[1] == ':')
}

// JoinServer joins a server folder and a file name in the server's style.
func JoinServer(dir, name string) string {
	sep := "/"
	if windowsPath(dir) {
		sep = `\`
	}
	if dir == "" {
		return name
	}
	if strings.HasSuffix(dir, sep) {
		return dir + name
	}
	return dir + sep + name
}

func dirOf(p string) string {
	i := strings.LastIndexAny(p, `\/`)
	if i < 0 {
		return ""
	}
	return p[:i+1]
}

// Lit quotes a T-SQL Unicode string literal.
func Lit(s string) string { return "N'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Ident quotes a T-SQL identifier.
func Ident(s string) string { return "[" + strings.ReplaceAll(s, "]", "]]") + "]" }

// File is one file inside a backup (RESTORE FILELISTONLY).
type File struct {
	Logical  string
	Physical string
	Type     string // D data, L log, F full-text
	Bytes    int64
}

// Backup is what a backup file holds (RESTORE HEADERONLY and FILELISTONLY).
type Backup struct {
	Local      string // the file on this computer
	ServerPath string // the same file as SQL Server sees it
	Position   int    // the full backup set in the file
	Database   string // original database name
	DBVersion  int
	Finished   time.Time
	Bytes      int64 // backup size from the header
	FileBytes  int64 // size of the .bak file
	Files      []File
}

// DataBytes is the size of the restored data files.
func (b *Backup) DataBytes() int64 {
	var n int64
	for _, f := range b.Files {
		if f.Type != "L" {
			n += f.Bytes
		}
	}
	return n
}

// RestoredBytes is the size of every restored file, logs included.
func (b *Backup) RestoredBytes() int64 {
	var n int64
	for _, f := range b.Files {
		n += f.Bytes
	}
	return n
}

// Problem is a check that failed, in plain English with what to do.
type Problem struct {
	File string
	Msg  string
}

func (p *Problem) Error() string { return p.Msg }

// Restorer restores backups into temporary databases.
type Restorer struct {
	S    Server
	Info Info
	// Place says where SQL Server can read a local file (nil: the same
	// path, the server is on this computer). Docker maps the work folder.
	Place func(local string) (string, error)
	// Copy copies a file into the server's backup folder when the server
	// cannot read it where it is (nil: no copying). It returns the server
	// path and a function that removes the copy.
	Copy func(local, backupDir string) (string, func(), error)
	// Mac and Docker shape the messages.
	Docker bool
	Mac    bool
	// Forget closes connections to a database before it is dropped.
	Forget func(name string)

	mu       sync.Mutex
	created  []string
	cleanups []func()
}

// Created lists the temporary databases made so far.
func (r *Restorer) Created() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.created...)
}

// errNo returns the SQL Server error number of err, or 0.
func errNo(err error) int32 {
	var n interface{ SQLErrorNumber() int32 }
	if errors.As(err, &n) {
		return n.SQLErrorNumber()
	}
	return 0
}

// cannotOpen says whether SQL Server could not open the backup file.
func cannotOpen(err error) bool {
	if n := errNo(err); n == 3201 || n == 3203 {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "cannot open backup device") || strings.Contains(s, "operating system error")
}

// Inspect reads the backup's header and file list, copying the file into
// the server's backup folder when the server cannot read it in place.
func (r *Restorer) Inspect(ctx context.Context, local string, fileBytes int64) (*Backup, error) {
	sp := local
	if r.Place != nil {
		var err error
		if sp, err = r.Place(local); err != nil {
			return nil, err
		}
	}
	b := &Backup{Local: local, FileBytes: fileBytes}
	hdr, err := r.S.Rows(ctx, "RESTORE HEADERONLY FROM DISK = "+Lit(sp))
	if err != nil && cannotOpen(err) && r.Copy != nil && r.Info.BackupPath != "" {
		cp, undo, cerr := r.Copy(local, r.Info.BackupPath)
		if cerr == nil {
			r.mu.Lock()
			r.cleanups = append(r.cleanups, undo)
			r.mu.Unlock()
			sp = cp
			hdr, err = r.S.Rows(ctx, "RESTORE HEADERONLY FROM DISK = "+Lit(sp))
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if cannotOpen(err) {
			msg := "SQL Server cannot read this file where it is."
			if r.Docker {
				msg += " Keep the backups inside the tool's work folder, or drop them on the page instead."
			} else {
				msg += " Copy it into a folder SQL Server can read (such as its Backup folder, " + r.Info.BackupPath + "), or run this tool as administrator, then try again."
			}
			return nil, &Problem{File: local, Msg: msg}
		}
		if n := errNo(err); n == 3241 || n == 3013 || strings.Contains(strings.ToLower(err.Error()), "not formatted correctly") || strings.Contains(strings.ToLower(err.Error()), "family") {
			return nil, &Problem{File: local, Msg: "This is not a SQL Server backup that " + r.Info.Name() + " can read. It may be damaged, cut short, or made by a newer SQL Server."}
		}
		return nil, &Problem{File: local, Msg: "SQL Server could not read this backup: " + err.Error()}
	}
	b.ServerPath = sp
	full := -1
	for i, h := range hdr {
		if num(h, "backuptype") == 1 {
			full = i
		}
	}
	if full < 0 {
		return nil, &Problem{File: local, Msg: "This file holds no full backup (only a differential or log backup). Drop the full backup of this database instead."}
	}
	h := hdr[full]
	b.Position = int(num(h, "position"))
	if b.Position == 0 {
		b.Position = full + 1
	}
	b.Database = str(h, "databasename")
	b.DBVersion = int(num(h, "databaseversion"))
	b.Bytes = num(h, "backupsize")
	if t, ok := h["backupfinishdate"].(time.Time); ok {
		b.Finished = t
	}
	fl, err := r.S.Rows(ctx, "RESTORE FILELISTONLY FROM DISK = "+Lit(sp)+" WITH FILE = "+strconv.Itoa(b.Position))
	if err != nil {
		return nil, &Problem{File: local, Msg: "SQL Server could not list the files in this backup: " + err.Error()}
	}
	for _, f := range fl {
		b.Files = append(b.Files, File{Logical: str(f, "logicalname"), Physical: str(f, "physicalname"), Type: strings.ToUpper(str(f, "type")), Bytes: num(f, "size")})
	}
	if len(b.Files) == 0 {
		return nil, &Problem{File: local, Msg: "This backup lists no database files."}
	}
	return b, nil
}

// GB formats bytes as gigabytes with one decimal.
func GB(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/(1<<30)) }

// Check says whether this server can restore b: version, the edition's
// size limit, and free disk (about three times the backup).
func (r *Restorer) Check(ctx context.Context, b *Backup) error {
	if r.Info.DBVersion > 0 && b.DBVersion > r.Info.DBVersion {
		msg := fmt.Sprintf("This backup was made by SQL Server %s; this computer has %s, which cannot read it.", ReleaseName(b.DBVersion), r.Info.Name())
		if r.Docker {
			msg += " Run the tool on the Windows server where SharePro runs."
		} else {
			msg += " Install the free SQL Server 2022 Express, or run the tool on the SharePro server, whose SQL Server made this backup."
		}
		return &Problem{File: b.Local, Msg: msg}
	}
	if r.Info.Express() && b.DataBytes() > ExpressLimit {
		var msg string
		switch {
		case r.Docker && r.Mac:
			msg = "This database (" + GB(b.DataBytes()) + ") is too large for the free SQL Server Express on a Mac. Run the tool on the Windows server where SharePro runs for this backup, or leave it out."
		case r.Docker:
			msg = "This database (" + GB(b.DataBytes()) + ") is too large for the free SQL Server Express on this computer. Run the tool on the Windows server where SharePro runs for this backup, or leave it out."
		default:
			msg = "This backup is " + GB(b.DataBytes()) + "; SQL Server Express can only hold 10 GB. Run the tool on the SharePro server, whose SQL Server can restore it."
		}
		return &Problem{File: b.Local, Msg: msg}
	}
	need := b.RestoredBytes()
	if n := 3 * b.FileBytes; n > need {
		need = n
	}
	free := freeBytes(ctx, r.S)
	if free >= 0 {
		r.Info.Free = free
	}
	if r.Info.Free >= 0 && need > r.Info.Free {
		msg := fmt.Sprintf("SQL Server needs about %s free to restore %s but its disk has %s free.", GB(need), b.Database, GB(r.Info.Free))
		if r.Docker {
			msg += " Give Docker more disk space (Docker Desktop → Settings → Resources → Disk usage limit), or choose fewer years, then try again."
		} else {
			msg += " Free up space on the disk with " + r.Info.DataPath + ", or choose fewer years, then try again."
		}
		return &Problem{File: b.Local, Msg: msg}
	}
	return nil
}

var alnum = regexp.MustCompile(`[^A-Za-z0-9_]`)

// TempName makes the temporary database name LLMIG_<6 random>_<original>.
func TempName(original string) string {
	const set = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		b[i] = set[n.Int64()]
	}
	o := alnum.ReplaceAllString(original, "_")
	if len(o) > 100 {
		o = o[:100]
	}
	return "LLMIG_" + string(b) + "_" + o
}

// IsTemp says whether name is one of the tool's temporary databases.
func IsTemp(name string) bool { return tempRe.MatchString(name) }

var tempRe = regexp.MustCompile(`^LLMIG_[a-z0-9]{6}_[A-Za-z0-9_]*$`)

// RestoreSQL is the RESTORE DATABASE statement for b under name, with each
// file moved into dataPath.
func RestoreSQL(name string, b *Backup, dataPath string) string {
	var sb strings.Builder
	sb.WriteString("RESTORE DATABASE " + Ident(name) + " FROM DISK = " + Lit(b.ServerPath) + " WITH FILE = " + strconv.Itoa(b.Position))
	nd := 0
	for i, f := range b.Files {
		var fn string
		switch f.Type {
		case "L":
			fn = fmt.Sprintf("%s_log%d.ldf", name, i)
		case "F":
			fn = fmt.Sprintf("%s_ft%d", name, i)
		case "S":
			fn = fmt.Sprintf("%s_fs%d", name, i)
		default:
			ext := ".ndf"
			if nd == 0 {
				ext = ".mdf"
			}
			nd++
			fn = fmt.Sprintf("%s_%d%s", name, i, ext)
		}
		sb.WriteString(", MOVE " + Lit(f.Logical) + " TO " + Lit(JoinServer(dataPath, fn)))
	}
	sb.WriteString(", RECOVERY")
	return sb.String()
}

// Restore restores b under a new temporary name and returns the name.
func (r *Restorer) Restore(ctx context.Context, b *Backup) (string, error) {
	name := TempName(b.Database)
	r.mu.Lock()
	r.created = append(r.created, name)
	r.mu.Unlock()
	if err := r.S.Exec(ctx, RestoreSQL(name, b, r.Info.DataPath)); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &Problem{File: b.Local, Msg: "SQL Server could not restore " + b.Database + ": " + err.Error()}
	}
	return name, nil
}

// DropSQL drops one of the tool's temporary databases.
func DropSQL(name string) string {
	return "IF DB_ID(" + Lit(name) + ") IS NOT NULL BEGIN ALTER DATABASE " + Ident(name) + " SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE " + Ident(name) + "; END"
}

// Cleanup drops every temporary database (unless keep) and removes copied
// backup files. It uses its own context so it still runs after a cancel.
func (r *Restorer) Cleanup(keep bool) []error {
	r.mu.Lock()
	created := r.created
	cleanups := r.cleanups
	r.created, r.cleanups = nil, nil
	r.mu.Unlock()
	var errs []error
	if !keep {
		for _, name := range created {
			if !IsTemp(name) {
				errs = append(errs, fmt.Errorf("refusing to drop %s: not a migration tool database", name))
				continue
			}
			if r.Forget != nil {
				r.Forget(name)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			if err := r.S.Exec(ctx, DropSQL(name)); err != nil {
				errs = append(errs, fmt.Errorf("could not remove the temporary database %s: %w (delete it in SQL Server Management Studio)", name, err))
			}
			cancel()
		}
	}
	for _, f := range cleanups {
		f()
	}
	return errs
}

// ListDatabases lists the user databases on the server (for "Use databases
// already on this server"), leaving out the tool's own temporary ones.
func ListDatabases(ctx context.Context, s Server) ([]migrate.Database, error) {
	rows, err := s.Rows(ctx, `SELECT d.name AS name, SUM(CAST(f.size AS bigint)) * 8192 AS bytes
 FROM sys.databases d JOIN sys.master_files f ON f.database_id = d.database_id
 WHERE d.database_id > 4 AND d.state_desc = 'ONLINE' AND d.name NOT LIKE 'LLMIG[_]%'
 GROUP BY d.name`)
	if err != nil {
		return nil, err
	}
	var out []migrate.Database
	for _, r := range rows {
		n := str(r, "name")
		out = append(out, migrate.Database{Name: n, Original: n, Bytes: num(r, "bytes")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
