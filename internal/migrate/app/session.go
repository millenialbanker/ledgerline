// Package app is the migration tool's user interface: a page served on
// 127.0.0.1 for the browser, and the session behind it that finds SQL
// Server, unpacks and restores the dropped backups, and runs the
// migration. The same session drives the headless mode used for testing.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/archive"
	"ledgerline/internal/migrate/disk"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/migrate/restore"
	"ledgerline/internal/migrate/run"
	"ledgerline/internal/migrate/sqlserver"
)

// Version is the tool's version, shown on the page and in the package.
var Version = "1.0"

// DB is what the session needs from a SQL Server connection.
type DB interface {
	restore.Server
	Database(name string) (migrate.Querier, error)
	Forget(name string)
	Close() error
}

// Connector opens SQL Server connections; tests replace it.
type Connector interface {
	Discover(ctx context.Context) []sqlserver.Endpoint
	Connect(ctx context.Context, e sqlserver.Endpoint, l sqlserver.Login) (DB, error)
}

type realConnector struct{}

func (realConnector) Discover(ctx context.Context) []sqlserver.Endpoint {
	return sqlserver.Discover(ctx)
}

func (realConnector) Connect(ctx context.Context, e sqlserver.Endpoint, l sqlserver.Login) (DB, error) {
	s, err := sqlserver.Connect(ctx, e, l)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// File is one dropped file (uploaded, or given on the command line).
type File struct {
	ID       string `json:"id"`
	Name     string `json:"name"` // as dropped, with its folder for folder drops
	Path     string `json:"-"`    // on this computer
	Size     int64  `json:"size"` // expected size
	Received int64  `json:"received"`
	Local    bool   `json:"local"`  // given on the command line: read where it is
	Status   string `json:"status"` // uploading, ready, password, unpacked, skipped
	Reason   string `json:"reason,omitempty"`
	Wrong    bool   `json:"wrong,omitempty"` // the password given was wrong

	password string
	items    []archive.Item
}

// Server is the SQL Server part of the state.
type Server struct {
	Status    string               `json:"status"` // looking, ready, login, none, docker, starting
	Message   string               `json:"message,omitempty"`
	Warnings  []string             `json:"warnings,omitempty"`
	Label     string               `json:"label,omitempty"`
	Product   string               `json:"product,omitempty"`
	Express   bool                 `json:"express"`
	Docker    bool                 `json:"docker"`
	CanDocker bool                 `json:"can_docker"`
	DockerOK  bool                 `json:"docker_ok"` // Docker checked and ready to start SQL Server
	Endpoints []sqlserver.Endpoint `json:"endpoints,omitempty"`
	Free      int64                `json:"free"`
	Windows   bool                 `json:"windows_signin"`
}

// Job is a long task running in the background.
type Job struct {
	Kind    string     `json:"kind"` // scan, run
	Running bool       `json:"running"`
	Step    string     `json:"step,omitempty"`
	Done    int        `json:"done"`
	Total   int        `json:"total"`
	Steps   []string   `json:"steps,omitempty"`
	Log     []LogEntry `json:"log,omitempty"`
	Error   string     `json:"error,omitempty"`
	Started time.Time  `json:"started"`
}

// LogEntry is one line of the page's log.
type LogEntry struct {
	At       string `json:"at"`
	Severity string `json:"severity"`
	Msg      string `json:"msg"`
}

// Session is one use of the tool.
type Session struct {
	Mapper    migrate.Mapper
	Work      string    // created by NewSession, removed by Close
	Out       string    // default folder for the package
	Log       io.Writer // console log: steps only, never data or secrets
	Connector Connector
	Docker    *sqlserver.Docker
	Now       func() time.Time
	GOOS      string

	mu       sync.Mutex
	files    []*File
	server   Server
	db       DB
	end      sqlserver.Endpoint
	restorer *restore.Restorer
	restored map[string]*run.Input // by .bak path
	live     []migrate.Database
	liveUse  map[string]bool
	survey   *run.Survey
	screen   string
	job      *Job
	cancel   context.CancelFunc
	result   *run.Result
	keep     bool
	nextID   int
	closed   bool
}

// NewSession makes a session with its own work folder inside parent (the
// system's temporary folder when parent is empty).
func NewSession(m migrate.Mapper, parent string) (*Session, error) {
	if parent == "" {
		parent = DefaultWorkParent()
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("cannot use the work folder %s: %w", parent, err)
	}
	w, err := os.MkdirTemp(parent, "llmig-")
	if err != nil {
		return nil, fmt.Errorf("cannot use the work folder %s: %w", parent, err)
	}
	s := &Session{Mapper: m, Work: w, Out: DefaultOut(), Log: io.Discard, Connector: realConnector{}, Now: time.Now, GOOS: runtime.GOOS,
		restored: map[string]*run.Input{}, liveUse: map[string]bool{}, screen: "drop"}
	s.server = Server{Status: "looking", Message: "Looking for SQL Server on this computer…", Windows: sqlserver.WindowsSignIn}
	s.Docker = &sqlserver.Docker{R: sqlserver.ExecRunner{}, Work: w}
	return s, nil
}

// DefaultWorkParent is where work folders go: on a Mac inside the user's
// caches (Docker Desktop, OrbStack and Colima all share the home folder
// with containers), elsewhere the system's temporary folder.
func DefaultWorkParent() string {
	if runtime.GOOS == "darwin" {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, "Library", "Caches", "Ledgerline Migration")
		}
	}
	return os.TempDir()
}

// DefaultOut is the suggested folder for the package: the desktop, else
// documents, else home.
func DefaultOut() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, d := range []string{"Desktop", "Documents"} {
		p := filepath.Join(h, d)
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return h
}

func (s *Session) logf(format string, a ...any) {
	fmt.Fprintf(s.Log, "%s  %s\n", s.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
}

// ---- SQL Server ----

// FindServer looks for SQL Server on this computer and tries a Windows
// sign-in. Without one, it checks Docker (Mac and Linux).
func (s *Session) FindServer(ctx context.Context) {
	s.mu.Lock()
	s.server = Server{Status: "looking", Message: "Looking for SQL Server on this computer…", Windows: sqlserver.WindowsSignIn}
	s.mu.Unlock()
	eps := s.Connector.Discover(ctx)
	s.mu.Lock()
	s.server.Endpoints = eps
	s.mu.Unlock()
	if len(eps) > 0 {
		var lastErr error
		if sqlserver.WindowsSignIn {
			for _, e := range eps {
				if err := s.Connect(ctx, e, sqlserver.Login{}); err == nil {
					return
				} else {
					lastErr = err
				}
			}
		}
		s.mu.Lock()
		s.server.Status = "login"
		s.server.Message = "SQL Server is on this computer (" + eps[0].String() + "). Enter a SQL Server login to read the backups."
		var le *sqlserver.LoginError
		if errors.As(lastErr, &le) {
			s.server.Message = le.Msg
		}
		s.mu.Unlock()
		return
	}
	s.noServer(ctx)
}

func (s *Session) noServer(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.server.Status = "none"
	s.server.CanDocker = s.GOOS != "windows"
	switch s.GOOS {
	case "windows":
		s.server.Message = "There is no SQL Server on this computer. The best place to run this tool is the SharePro server itself, whose SQL Server can read every backup. Or install the free SQL Server 2022 Express from Microsoft's website (choose Basic installation), then press Try again."
	case "darwin":
		s.server.Message = "There is no SQL Server for the Mac, so the tool runs Microsoft's free SQL Server Express inside Docker."
	default:
		s.server.Message = "No SQL Server was found on this computer. The tool can run Microsoft's free SQL Server Express inside Docker."
	}
}

// CheckDocker checks that Docker is installed, running and big enough.
func (s *Session) CheckDocker(ctx context.Context) error {
	s.mu.Lock()
	d := s.Docker
	s.server.Status = "docker"
	s.server.Message = "Checking Docker…"
	s.server.DockerOK = false
	s.mu.Unlock()
	err := d.Check(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.server.Warnings = d.Warnings
	if err != nil {
		s.server.Message = err.Error()
		return err
	}
	s.server.DockerOK = true
	msg := "Docker is running. Press Start SQL Server to run SQL Server Express in Docker. It is limited to 10 GB per database."
	if !d.HasImage(ctx) {
		msg += " The first time it downloads SQL Server from Microsoft: about 1.5 GB."
	}
	if d.AppleSilicon() {
		msg += " On Apple silicon it runs through Docker's Rosetta emulation."
	}
	s.server.Message = msg
	return nil
}

// StartDocker starts SQL Server in Docker and connects to it.
func (s *Session) StartDocker(ctx context.Context, developer bool) error {
	s.mu.Lock()
	d := s.Docker
	d.Developer = developer
	d.Status = func(m string) {
		s.mu.Lock()
		s.server.Message = m
		s.mu.Unlock()
		s.logf("%s", m)
	}
	s.server.Status = "starting"
	s.mu.Unlock()
	srv, err := d.Start(ctx)
	if err != nil {
		s.mu.Lock()
		s.server.Status = "docker"
		s.server.Message = err.Error()
		s.mu.Unlock()
		return err
	}
	return s.use(ctx, srv, srv.End)
}

// Connect signs in to one server.
func (s *Session) Connect(ctx context.Context, e sqlserver.Endpoint, l sqlserver.Login) error {
	db, err := s.Connector.Connect(ctx, e, l)
	if err != nil {
		s.mu.Lock()
		s.server.Status = "login"
		s.server.Message = err.Error()
		s.mu.Unlock()
		return err
	}
	return s.use(ctx, db, e)
}

func (s *Session) use(ctx context.Context, db DB, e sqlserver.Endpoint) error {
	info, err := restore.ServerInfo(ctx, db)
	if err != nil {
		db.Close()
		s.mu.Lock()
		s.server.Status = "login"
		s.server.Message = "Signed in to " + e.String() + " but could not read its settings: " + err.Error()
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		s.db.Close()
	}
	s.db, s.end = db, e
	docker := e.Docker
	r := &restore.Restorer{S: db, Info: info, Docker: docker, Mac: s.GOOS == "darwin", Forget: db.Forget}
	if docker {
		r.Place = s.dockerPlace
	} else {
		r.Copy = copyInto
	}
	s.restorer = r
	if !docker {
		s.server.Warnings = nil
		// copies left by an earlier run that was closed too early
		if left, err := r.AdoptLeftovers(ctx, 24*time.Hour, s.Now()); err == nil && len(left) > 0 {
			s.server.Warnings = append(s.server.Warnings, fmt.Sprintf("%d temporary copies left on this SQL Server by an earlier run of the tool (named LLMIG_…) are removed when you finish.", len(left)))
		}
	}
	s.server.Status = "ready"
	s.server.Label = e.String()
	s.server.Product = info.Name()
	s.server.Express = info.Express()
	s.server.Docker = docker
	s.server.Free = info.Free
	s.server.Message = "Using " + info.Name() + " on " + e.String() + "."
	if info.Express() {
		s.server.Message += " Express holds up to 10 GB per database."
	}
	s.logf("using %s on %s", info.Name(), e.String())
	return nil
}

// dockerPlace maps a file to the container: files in the work folder are
// mounted; others are copied in first.
func (s *Session) dockerPlace(local string) (string, error) {
	if p, ok := s.Docker.ServerPath(local); ok {
		return p, nil
	}
	dir := filepath.Join(s.Work, "incoming")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, fmt.Sprintf("%d-%s", time.Now().UnixNano(), filepath.Base(local)))
	if err := copyFile(local, dst); err != nil {
		return "", fmt.Errorf("could not copy %s into the work folder for Docker: %w", filepath.Base(local), err)
	}
	p, _ := s.Docker.ServerPath(dst)
	return p, nil
}

// copyInto copies a backup into SQL Server's backup folder on this
// computer when the server cannot read it where it is.
func copyInto(local, dir string) (string, func(), error) {
	dst := filepath.Join(dir, restore.TempName("copy")+"_"+filepath.Base(local))
	if err := copyFile(local, dst); err != nil {
		return "", nil, err
	}
	return dst, func() { os.Remove(dst) }, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// ---- files ----

// AddLocal adds files or folders given on the command line (dropped on
// the program's icon).
func (s *Session) AddLocal(paths []string) {
	files, skipped := archive.Expand(paths)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range files {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		s.nextID++
		s.files = append(s.files, &File{ID: fmt.Sprintf("local%d", s.nextID), Name: p, Path: p, Size: st.Size(), Received: st.Size(), Local: true, Status: "ready"})
	}
	for _, sk := range skipped {
		s.nextID++
		s.files = append(s.files, &File{ID: fmt.Sprintf("local%d", s.nextID), Name: sk.Source, Path: sk.Source, Local: true, Status: "skipped", Reason: sk.Reason})
	}
}

// Skip records a dropped file the page did not upload (RAR and the like).
func (s *Session) Skip(name string) {
	reason := kindReason(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.files = append(s.files, &File{ID: fmt.Sprintf("skip%d", s.nextID), Name: name, Status: "skipped", Reason: reason})
}

func kindReason(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, ".rar"):
		return "RAR files cannot be read by the tool: unpack it with WinRAR or 7-Zip first and drop the .bak inside."
	case strings.HasSuffix(l, ".mdf"), strings.HasSuffix(l, ".ldf"), strings.HasSuffix(l, ".ndf"):
		return "This is a detached database file, not a backup. Attach it in SQL Server Management Studio and choose \"Use databases already on this server\", or take a backup (.bak) of it and drop that."
	}
	return "Not a SQL Server backup (.bak) or an archive of one."
}

// Remove takes a dropped file off the list.
func (s *Session) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != nil && s.job.Running {
		return errors.New("wait until the tool has finished")
	}
	for i, f := range s.files {
		if f.ID == id {
			if !f.Local && f.Path != "" {
				os.Remove(f.Path)
			}
			s.files = append(s.files[:i], s.files[i+1:]...)
			return nil
		}
	}
	return errors.New("no such file")
}

// SetPassword gives the password of a protected archive (empty: skip it).
func (s *Session) SetPassword(id, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files {
		if f.ID == id {
			if password == "" {
				f.Status, f.Reason = "skipped", "Protected by a password, which was not given."
			} else {
				f.password, f.Status, f.Wrong = password, "ready", false
			}
			return nil
		}
	}
	return errors.New("no such file")
}

// ---- jobs ----

// Progress feeds the page from a job.
type progress struct{ s *Session }

func (p progress) Step(what string) {
	p.s.mu.Lock()
	if j := p.s.job; j != nil {
		j.Step, j.Done, j.Total = what, 0, 0
	}
	p.s.mu.Unlock()
}

func (p progress) Count(done, total int) {
	p.s.mu.Lock()
	if j := p.s.job; j != nil {
		j.Done, j.Total = done, total
	}
	p.s.mu.Unlock()
}

func (p progress) Log(sev, msg string) {
	p.s.mu.Lock()
	if j := p.s.job; j != nil {
		j.Log = append(j.Log, LogEntry{At: p.s.Now().Format("15:04:05"), Severity: sev, Msg: msg})
		if len(j.Log) > 500 {
			j.Log = j.Log[len(j.Log)-500:]
		}
		if sev == "Info" && msg == j.Step {
			// the runner logs each of its steps as it starts it
			j.Steps = append(j.Steps, msg)
		}
	}
	p.s.mu.Unlock()
	if sev == "Info" {
		p.s.logf("%s", msg)
	} else {
		p.s.logf("%s noted (see the report)", strings.ToLower(sev))
	}
}

var errBusy = errors.New("the tool is already working; wait or press Cancel")

// startJob runs fn in the background as the session's job.
func (s *Session) startJob(kind string, fn func(ctx context.Context, p progress) error) error {
	s.mu.Lock()
	if s.job != nil && s.job.Running {
		s.mu.Unlock()
		return errBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.job = &Job{Kind: kind, Running: true, Started: s.Now()}
	s.cancel = cancel
	s.mu.Unlock()
	go func() {
		defer cancel()
		err := fn(ctx, progress{s})
		s.mu.Lock()
		s.job.Running = false
		if err != nil {
			if errors.Is(err, context.Canceled) {
				s.job.Error = "Cancelled."
			} else {
				s.job.Error = err.Error()
			}
		}
		s.mu.Unlock()
	}()
	return nil
}

// Cancel stops the running job.
func (s *Session) Cancel() {
	s.mu.Lock()
	c := s.cancel
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

// Wait blocks until the job ends (headless mode and tests).
func (s *Session) Wait() *Job {
	for {
		s.mu.Lock()
		j := s.job
		running := j != nil && j.Running
		var cp Job
		if j != nil {
			cp = *j
		}
		s.mu.Unlock()
		if !running {
			return &cp
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Scan unpacks the dropped files, restores the backups and finds out what
// each database is.
func (s *Session) Scan() error {
	s.mu.Lock()
	if s.restorer == nil && len(s.liveUse) == 0 {
		s.mu.Unlock()
		return errors.New("SQL Server is not ready yet: see the box at the top of the page")
	}
	for _, f := range s.files {
		if f.Status == "uploading" {
			s.mu.Unlock()
			return errors.New("wait until every file has been received")
		}
	}
	s.mu.Unlock()
	return s.startJob("scan", func(ctx context.Context, p progress) error {
		err := s.scan(ctx, p)
		if errors.Is(err, context.Canceled) {
			// cancelled: drop what was restored so far
			s.mu.Lock()
			s.resetLocked()
			s.mu.Unlock()
		}
		return err
	})
}

func (s *Session) scan(ctx context.Context, p progress) error {
	s.mu.Lock()
	var todo []*File
	for _, f := range s.files {
		if f.Status == "ready" {
			todo = append(todo, f)
		}
	}
	s.mu.Unlock()
	// unpack
	for i, f := range todo {
		p.Step(fmt.Sprintf("Unpacking %s (%d of %d)", filepath.Base(f.Name), i+1, len(todo)))
		s.mu.Lock()
		pw := f.password
		s.mu.Unlock()
		items, err := archive.Unpack(ctx, f.Path, archive.Options{Work: s.Work, Password: pw, Progress: func(what string, done, total int64) {
			if total > 0 {
				p.Count(int(done>>20), int(total>>20))
			}
		}})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.mu.Lock()
		var np *archive.NeedPassword
		var se *archive.SkipError
		switch {
		case errors.As(err, &np):
			f.Status, f.Wrong, f.Reason = "password", np.Wrong, np.Error()
		case errors.As(err, &se):
			f.Status, f.Reason = "skipped", se.Reason
		case err != nil:
			f.Status, f.Reason = "skipped", "Could not unpack it: "+err.Error()
		default:
			f.Status, f.items, f.password = "unpacked", items, ""
		}
		s.mu.Unlock()
		if err == nil {
			p.Log("Info", "Unpacked "+filepath.Base(f.Name))
		}
	}
	s.mu.Lock()
	for _, f := range s.files {
		if f.Status == "password" {
			s.mu.Unlock()
			return nil // the page asks for the password
		}
	}
	var items []archive.Item
	for _, f := range s.files {
		if f.Status == "unpacked" {
			items = append(items, f.items...)
		}
	}
	r := s.restorer
	db := s.db
	s.mu.Unlock()
	// restore
	for i, it := range items {
		s.mu.Lock()
		done := s.restored[it.Path] != nil
		s.mu.Unlock()
		if done {
			continue
		}
		label := it.Name
		if it.Inner != "" {
			label += " (" + it.Inner + ")"
		}
		in := &run.Input{DB: migrate.Database{File: it.Name, Original: strings.TrimSuffix(it.Inner, filepath.Ext(it.Inner)), Bytes: it.SourceBytes, SHA256: it.SHA256}}
		if in.DB.Original == "" {
			in.DB.Original = strings.TrimSuffix(it.Name, filepath.Ext(it.Name))
		}
		if r == nil {
			in.Problem = "No SQL Server is ready to restore it."
		} else {
			p.Step(fmt.Sprintf("Reading %s (%d of %d)", label, i+1, len(items)))
			b, err := r.Inspect(ctx, it.Path, it.Bytes)
			if err == nil {
				in.DB.Original, in.DB.BackupDate = b.Database, b.Finished
				err = r.Check(ctx, b)
				if err == nil {
					p.Step(fmt.Sprintf("Restoring %s, %s (%d of %d)", b.Database, restore.GB(b.RestoredBytes()), i+1, len(items)))
					var name string
					name, err = r.Restore(ctx, b)
					if err == nil {
						in.DB.Name = name
						in.Q, err = db.Database(name)
						p.Log("Info", "Restored "+b.Database+" from "+it.Name)
					}
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				in.Problem = err.Error()
				p.Log("Warning", label+": "+err.Error())
			}
		}
		s.mu.Lock()
		s.restored[it.Path] = in
		s.mu.Unlock()
	}
	return s.classify(ctx, p)
}

// inputs lists every database to classify: restored backups and the live
// databases ticked.
func (s *Session) inputs() []run.Input {
	var keys []string
	for k := range s.restored {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []run.Input
	for _, k := range keys {
		out = append(out, *s.restored[k])
	}
	for _, d := range s.live {
		if !s.liveUse[d.Name] {
			continue
		}
		var q migrate.Querier
		in := run.Input{DB: d, Live: true}
		if s.db != nil {
			var err error
			if q, err = s.db.Database(d.Name); err != nil {
				in.Problem = err.Error()
			}
		}
		in.Q = q
		out = append(out, in)
	}
	return out
}

func (s *Session) classify(ctx context.Context, p progress) error {
	p.Step("Finding out what each database is")
	s.mu.Lock()
	ins := s.inputs()
	years := 8
	if s.survey != nil {
		years = s.survey.Years
	}
	s.mu.Unlock()
	if len(ins) == 0 {
		return errors.New("no database could be read: see the reasons beside each file")
	}
	sv, err := run.Classify(ctx, s.Mapper, ins, years, s.Now())
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.survey = sv
	s.screen = "found"
	s.mu.Unlock()
	return nil
}

// ListLive lists the databases on the server ("Use databases already on
// this server").
func (s *Session) ListLive(ctx context.Context) ([]migrate.Database, error) {
	s.mu.Lock()
	db := s.db
	s.mu.Unlock()
	if db == nil {
		return nil, errors.New("SQL Server is not ready yet")
	}
	l, err := restore.ListDatabases(ctx, db)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.live = l
	s.mu.Unlock()
	return l, nil
}

// UseLive reads the named databases in place and classifies everything.
func (s *Session) UseLive(names []string) error {
	s.mu.Lock()
	known := map[string]bool{}
	for _, d := range s.live {
		known[d.Name] = true
	}
	s.liveUse = map[string]bool{}
	for _, n := range names {
		if !known[n] {
			s.mu.Unlock()
			return fmt.Errorf("there is no database %q on the server", n)
		}
		s.liveUse[n] = true
	}
	s.mu.Unlock()
	return s.startJob("scan", func(ctx context.Context, p progress) error { return s.classify(ctx, p) })
}

// Toggle skips (or uses again) row i of "What we found".
func (s *Session) Toggle(ctx context.Context, i int, skip bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != nil && s.job.Running {
		return errBusy
	}
	if s.survey == nil {
		return errors.New("nothing found yet")
	}
	return s.survey.Toggle(ctx, i, skip)
}

// SetYears changes the years of history.
func (s *Session) SetYears(ctx context.Context, n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != nil && s.job.Running {
		return errBusy
	}
	if s.survey == nil {
		return errors.New("nothing found yet")
	}
	return s.survey.SetYears(ctx, n)
}

// Go moves between the screens the user may choose (found, choose).
func (s *Session) Go(screen string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch screen {
	case "found", "choose":
		if s.survey == nil {
			return errors.New("nothing found yet")
		}
		s.screen = screen
	default:
		return errors.New("no such screen")
	}
	return nil
}

// Choice is what the user chose on "Choose".
type Choice struct {
	Firms       []string `json:"firms"`
	Years       int      `json:"years"`
	Documents   bool     `json:"documents"`
	Dormant     string   `json:"dormant"`
	Out         string   `json:"out"`
	Passphrase  string   `json:"passphrase"`
	Passphrase2 string   `json:"passphrase2"`
	MaxPartGB   float64  `json:"max_part_gb"`
	Keep        bool     `json:"keep"`
}

// Validate checks a choice before the run starts, in plain English.
func (s *Session) Validate(c Choice) error {
	if len([]rune(c.Passphrase)) < run.MinPassphrase {
		return fmt.Errorf("the passphrase must be at least %d characters", run.MinPassphrase)
	}
	if c.Passphrase != c.Passphrase2 {
		return errors.New("the two passphrases are not the same")
	}
	if c.Years < 1 || c.Years > 8 {
		return errors.New("choose between 1 and 8 years of history")
	}
	if c.Dormant != "" && c.Dormant != "inactive" && c.Dormant != "skip" {
		return errors.New("choose what to do with dormant clients")
	}
	out := strings.TrimSpace(c.Out)
	if out == "" {
		return errors.New("choose where to save the package")
	}
	if !filepath.IsAbs(out) {
		return errors.New("enter the full path of the folder to save in, such as " + DefaultOut())
	}
	if run.WithinHere(s.Work, out) {
		return errors.New("the package cannot be saved inside the tool's work folder, which is deleted at the end")
	}
	st, err := os.Stat(out)
	if err != nil || !st.IsDir() {
		return errors.New("the folder " + out + " does not exist")
	}
	f, err := os.CreateTemp(out, ".llmig-check-*")
	if err != nil {
		return errors.New("the tool may not write in " + out + "; choose another folder")
	}
	f.Close()
	os.Remove(f.Name())
	if c.MaxPartGB != 0 && (c.MaxPartGB < 0.1 || c.MaxPartGB > 1000) {
		return errors.New("the largest package size must be between 0.1 and 1000 GB")
	}
	return nil
}

// Start validates the choice and starts the migration.
func (s *Session) Start(c Choice) error {
	if err := s.Validate(c); err != nil {
		return err
	}
	s.mu.Lock()
	if s.job != nil && s.job.Running {
		s.mu.Unlock()
		return errBusy
	}
	sv := s.survey
	if sv == nil {
		s.mu.Unlock()
		return errors.New("nothing found yet")
	}
	if sv.Years != c.Years {
		sv.SetYears(context.Background(), c.Years)
	}
	if sv.Newest() == nil {
		s.mu.Unlock()
		return errors.New("no company database to migrate: drop the latest company backup")
	}
	dormant := c.Dormant
	if dormant == "" {
		dormant = "inactive"
	}
	maxPart := int64(c.MaxPartGB * (1 << 30))
	opts := run.Options{
		Survey:     sv,
		Plan:       migrate.Plan{Firms: c.Firms, Years: c.Years, Documents: c.Documents, DormantMode: dormant},
		Server:     s.end.String(),
		Out:        strings.TrimSpace(c.Out),
		Work:       s.Work,
		Passphrase: c.Passphrase,
		MaxPart:    maxPart,
		Tool:       "Ledgerline migration tool " + Version,
		Extra:      s.extra(),
		Now:        s.Now,
	}
	s.keep = c.Keep
	s.screen = "working"
	s.result = nil
	s.mu.Unlock()
	return s.startJob("run", func(ctx context.Context, p progress) error {
		opts.Progress = p
		res, err := run.Run(ctx, opts)
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				s.dropRestoredLocked()
				s.screen = "drop"
			}
			return err
		}
		s.result = res
		s.screen = "done"
		s.dropRestoredLocked()
		s.logf("package saved: %d file(s) in %s", len(res.Parts), opts.Out)
		return nil
	})
}

// extra lists dropped files that could not be used, for the manifest.
func (s *Session) extra() []pkg.Backup {
	var out []pkg.Backup
	for _, f := range s.files {
		if f.Status == "skipped" || f.Status == "password" {
			out = append(out, pkg.Backup{File: filepath.Base(f.Name), Role: "other", Note: f.Reason})
		}
	}
	return out
}

// dropRestoredLocked drops the temporary databases (unless the user kept
// them); the survey goes with them.
func (s *Session) dropRestoredLocked() {
	if s.restorer != nil {
		for _, err := range s.restorer.Cleanup(s.keep) {
			s.logf("%v", err)
		}
	}
	s.restored = map[string]*run.Input{}
	s.survey = nil
}

// Back returns to the first screen, dropping the temporary databases.
func (s *Session) Back() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != nil && s.job.Running {
		return errBusy
	}
	s.resetLocked()
	s.result = nil
	s.job = nil
	return nil
}

// resetLocked drops the temporary databases and unpacked files and goes
// back to the first screen with the dropped files ready again.
func (s *Session) resetLocked() {
	s.dropRestoredLocked()
	for _, f := range s.files {
		if f.Status == "unpacked" {
			f.Status = "ready"
			for _, it := range f.items {
				if it.Extracted {
					os.Remove(it.Path)
				}
			}
			f.items = nil
		}
	}
	s.screen = "drop"
}

// Result is the finished run's result.
func (s *Session) Result() *run.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result
}

// Close drops every temporary database, stops the Docker container and
// deletes the work folder.
func (s *Session) Close() {
	s.Cancel()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		running := s.job != nil && s.job.Running
		s.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.dropRestoredLocked()
	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
	if s.Docker != nil && s.Docker.Running() {
		s.logf("removing the SQL Server container (the downloaded image is kept for next time)")
		s.Docker.Stop(context.Background())
	}
	os.RemoveAll(s.Work)
	s.logf("work folder deleted")
}

// FreeWork is the free space where uploads go.
func (s *Session) FreeWork() int64 {
	n, err := disk.Free(s.Work)
	if err != nil {
		return -1
	}
	return n
}

// Busy says whether a job is running.
func (s *Session) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.job != nil && s.job.Running
}
