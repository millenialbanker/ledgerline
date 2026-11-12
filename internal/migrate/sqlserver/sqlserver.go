// Package sqlserver connects the migration tool to a SQL Server on this
// computer (or in a local Docker container) and reads databases from it as
// migrate.Querier documents.
//
// Reads are plain READ COMMITTED (the driver's default), never READ
// UNCOMMITTED or NOLOCK, so money totals are consistent. Every query runs in
// a transaction that is always rolled back, and only SELECT-like statements
// are accepted, so nothing is ever written to the old system's databases.
package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	mssql "github.com/microsoft/go-mssqldb"

	"ledgerline/internal/migrate"
)

// Endpoint is one SQL Server instance.
type Endpoint struct {
	Host     string `json:"host"`               // 127.0.0.1, localhost
	Instance string `json:"instance,omitempty"` // SQLEXPRESS; empty for the default instance
	Port     int    `json:"port,omitempty"`     // 0: found through the SQL Browser or shared memory
	Docker   bool   `json:"docker,omitempty"`   // the tool's own container
}

// String is how the server is shown and written in the manifest.
func (e Endpoint) String() string {
	s := e.Host
	if e.Instance != "" {
		s += `\` + e.Instance
	}
	if e.Port > 0 {
		s += "," + strconv.Itoa(e.Port)
	}
	if e.Docker {
		s += " (Docker)"
	}
	return s
}

// Login is how to sign in. An empty User means Windows sign-in.
type Login struct {
	User     string
	Password string
}

// Windows reports whether this is a Windows sign-in.
func (l Login) Windows() bool { return l.User == "" }

// dsn builds the connection string. The password goes only into the URL
// handed to the driver, never into anything logged (see Endpoint.String).
func dsn(e Endpoint, l Login, database string) string {
	host := e.Host
	if e.Port > 0 {
		host = net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
	}
	u := &url.URL{Scheme: "sqlserver", Host: host}
	if e.Instance != "" && e.Port == 0 {
		u.Path = "/" + e.Instance
	}
	if !l.Windows() {
		u.User = url.UserPassword(l.User, l.Password)
	}
	q := url.Values{}
	if database != "" {
		q.Set("database", database)
	}
	q.Set("app name", "Ledgerline migration tool")
	// a local server's certificate is self-signed; the connection never
	// leaves this computer
	q.Set("encrypt", "false")
	q.Set("TrustServerCertificate", "true")
	q.Set("dial timeout", "5")
	q.Set("connection timeout", "0")
	u.RawQuery = q.Encode()
	return u.String()
}

// Server is a connection to one SQL Server.
type Server struct {
	End   Endpoint
	login Login
	open  func(dsn string) (*sql.DB, error)

	mu     sync.Mutex
	master *sql.DB
	dbs    map[string]*sql.DB
}

func openDB(d string) (*sql.DB, error) { return sql.Open("sqlserver", d) }

// Connect signs in to the server and checks the connection.
func Connect(ctx context.Context, e Endpoint, l Login) (*Server, error) {
	s := &Server{End: e, login: l, open: openDB, dbs: map[string]*sql.DB{}}
	db, err := s.open(dsn(e, l, "master"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := db.PingContext(cctx); err != nil {
		db.Close()
		return nil, explainConnect(e, l, err)
	}
	s.master = db
	return s, nil
}

// explainConnect turns a driver error into plain English.
func explainConnect(e Endpoint, l Login, err error) error {
	var me mssql.Error
	if errors.As(err, &me) && me.Number == 18456 {
		if l.Windows() {
			return &LoginError{Msg: "SQL Server on " + e.String() + " did not accept your Windows sign-in. Enter a SQL Server login (such as sa) instead."}
		}
		return &LoginError{Msg: "SQL Server on " + e.String() + " did not accept that login name and password."}
	}
	msg := err.Error()
	if strings.Contains(strings.ToLower(msg), "integrated") || strings.Contains(strings.ToLower(msg), "ntlm") || strings.Contains(strings.ToLower(msg), "sspi") {
		return &LoginError{Msg: "Windows sign-in is not available here. Enter a SQL Server login (such as sa)."}
	}
	return fmt.Errorf("could not connect to SQL Server on %s: %w", e, err)
}

// LoginError means the server is there but would not let us in: ask for a
// SQL login.
type LoginError struct{ Msg string }

func (e *LoginError) Error() string { return e.Msg }

// Close closes every connection.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, db := range s.dbs {
		db.Close()
	}
	s.dbs = map[string]*sql.DB{}
	if s.master != nil {
		return s.master.Close()
	}
	return nil
}

// Exec runs a statement in master (RESTORE, DROP of the tool's own
// temporary databases). It is not reachable through a Querier.
func (s *Server) Exec(ctx context.Context, q string, args ...any) error {
	_, err := s.master.ExecContext(ctx, q, args...)
	return err
}

// Rows runs a query in master and returns every row (small results only:
// server properties, RESTORE HEADERONLY, the database list).
func (s *Server) Rows(ctx context.Context, q string, args ...any) ([]migrate.Row, error) {
	rows, err := s.master.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []migrate.Row
	err = scanRows(rows, func(r migrate.Row) error { out = append(out, r); return nil })
	return out, err
}

// Database returns a read-only Querier on one database.
func (s *Server) Database(name string) (migrate.Querier, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db := s.dbs[name]
	if db == nil {
		var err error
		db, err = s.open(dsn(s.End, s.login, name))
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(4) // a mapper may read inside another read
		s.dbs[name] = db
	}
	return &Querier{db: db, name: name}, nil
}

// Forget closes the connections to a database (before it is dropped).
func (s *Server) Forget(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if db := s.dbs[name]; db != nil {
		db.Close()
		delete(s.dbs, name)
	}
}

// Querier reads one database.
type Querier struct {
	db   *sql.DB
	name string
}

func (q *Querier) Database() string { return q.name }

// Query runs a read-only query. It runs inside a READ COMMITTED transaction
// that is always rolled back.
func (q *Querier) Query(ctx context.Context, stmt string, fn func(migrate.Row) error, args ...any) error {
	if err := ReadOnly(stmt); err != nil {
		return err
	}
	tx, err := q.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, stmt, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanRows(rows, fn)
}

func scanRows(rows *sql.Rows, fn func(migrate.Row) error) error {
	cts, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	names := make([]string, len(cts))
	types := make([]string, len(cts))
	for i, c := range cts {
		names[i] = strings.ToLower(c.Name())
		types[i] = strings.ToUpper(c.DatabaseTypeName())
	}
	vals := make([]any, len(cts))
	ptrs := make([]any, len(cts))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		r := make(migrate.Row, len(cts))
		for i := range vals {
			r[names[i]] = Normalise(types[i], vals[i])
			vals[i] = nil
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Normalise turns a driver value into a Querier value: decimals and money
// as exact strings, GUIDs as text, CHAR columns without their trailing
// spaces, every integer as int64.
func Normalise(dbType string, v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		switch dbType {
		case "DECIMAL", "NUMERIC", "MONEY", "SMALLMONEY":
			return string(x)
		case "UNIQUEIDENTIFIER":
			var g mssql.UniqueIdentifier
			if err := g.Scan(x); err == nil {
				return g.String()
			}
		}
		return append([]byte(nil), x...)
	case string:
		if dbType == "CHAR" || dbType == "NCHAR" {
			return strings.TrimRight(x, " ")
		}
		return x
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case int16:
		return int64(x)
	case int8:
		return int64(x)
	case uint8:
		return int64(x)
	case float32:
		return float64(x)
	}
	return v
}

var (
	reComment = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)
	reString  = regexp.MustCompile(`(?s)N?'(?:[^']|'')*'`)
	reIdent   = regexp.MustCompile(`\[(?:[^\]]|\]\])*\]|"(?:[^"]|"")*"`)
	reWrite   = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|MERGE|DROP|ALTER|CREATE|TRUNCATE|EXEC|EXECUTE|GRANT|DENY|REVOKE|BACKUP|RESTORE|DBCC|SHUTDOWN|KILL|OPENROWSET|OPENQUERY|OPENDATASOURCE|BULK|WRITETEXT|UPDATETEXT|USE)\b`)
	reInto    = regexp.MustCompile(`(?i)\bINTO\s+([^\s#@])`)
	reDirty   = regexp.MustCompile(`(?i)\bNOLOCK\b|\bREADUNCOMMITTED\b|\bREAD\s+UNCOMMITTED\b`)
	reFirst   = regexp.MustCompile(`(?i)^\s*(SELECT|WITH|DECLARE|SET\s+NOCOUNT)\b`)
)

// ErrNotReadOnly is returned for a statement that could change data or
// read uncommitted rows.
var ErrNotReadOnly = errors.New("the migration tool only reads; this statement is not allowed")

// ReadOnly accepts SELECT statements (and WITH, DECLARE of variables, SELECT
// INTO a #temporary table) and refuses anything that writes, runs
// procedures or reads uncommitted data.
func ReadOnly(stmt string) error {
	s := reComment.ReplaceAllString(stmt, " ")
	s = reString.ReplaceAllString(s, "''")
	s = reIdent.ReplaceAllString(s, "[x]")
	if !reFirst.MatchString(s) {
		return ErrNotReadOnly
	}
	if reWrite.MatchString(s) || reInto.MatchString(s) {
		return ErrNotReadOnly
	}
	if reDirty.MatchString(s) {
		return fmt.Errorf("%w (money totals must be read committed: no NOLOCK or READ UNCOMMITTED)", ErrNotReadOnly)
	}
	return nil
}
