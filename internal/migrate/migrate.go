// Package migrate is the engine of the Ledgerline migration tool: it takes
// an old back office's backups and archives, reads them, and writes one
// migration package (see package pkg) for Ledgerline to load at go-live.
//
// The engine knows nothing about any one back office. A Mapper does: it
// says what each database is and turns its tables into Ledgerline's files.
// The SharePro mapper is in package sharepro.
package migrate

import (
	"context"
	"time"

	"ledgerline/internal/migrate/pkg"
)

// Querier runs read-only queries on one database.
type Querier interface {
	// Query runs q and calls fn for each row. Column names are lower case.
	// Values are nil, int64, float64, bool, string, []byte or time.Time
	// (decimals come as string, exactly as stored).
	Query(ctx context.Context, q string, fn func(Row) error, args ...any) error
	// Database is the database's name on the server.
	Database() string
}

// Row is one result row.
type Row map[string]any

// Database is one database the tool can read: restored from a dropped
// backup or already on the server.
type Database struct {
	Name       string    // name on the server
	Original   string    // name inside the backup (or Name)
	File       string    // backup file it came from, if any
	BackupDate time.Time // from the backup header
	Bytes      int64
	SHA256     string
}

// Kind is what a Mapper says a database is.
type Kind struct {
	Role  string // pkg roles: company, admin, mail, other
	FY    string // "2026-27"; empty for admin
	Note  string // shown to the user, e.g. "Mail archive 2025-26"
	Skip  bool   // not used for the migration (Note says why)
	Order int    // lower first; the engine reads admin, then companies newest first, then mail
}

// Plan is what the user chose on the tool's screen.
type Plan struct {
	Firms       []string  // company codes to migrate (empty: all)
	AsOf        time.Time // go-live balances as of this date (zero: the latest date in the newest year)
	Years       int       // how many financial years of history and documents (default 8)
	Documents   bool      // archive documents (contract notes, statements) from mail databases
	DormantMode string    // "inactive" (default) or "skip": clients with no active segment
}

// Mapper turns one back office's databases into a package.
type Mapper interface {
	// Name is the back office's name, e.g. "SharePro".
	Name() string
	// Classify says what a database is, from its tables and contents.
	Classify(ctx context.Context, q Querier) (Kind, error)
	// Firms lists the companies, from the admin database (nil if none was
	// given) and the newest company database.
	Firms(ctx context.Context, admin, company Querier) ([]pkg.Firm, error)
	// Current writes the go-live files (masters, opening balances,
	// holdings, positions, brokerage, interest) for the chosen firms from
	// the newest company database, with the checks Ledgerline will verify.
	Current(ctx context.Context, q Querier, k Kind, plan Plan, w *pkg.Writer, p Progress) error
	// History writes one earlier (or the current) year's read-only history.
	History(ctx context.Context, q Querier, k Kind, plan Plan, w *pkg.Writer, p Progress) error
	// Documents writes the archived documents from a mail database.
	Documents(ctx context.Context, q Querier, k Kind, plan Plan, w *pkg.Writer, p Progress) error
}

// Progress receives what the tool shows while it works.
type Progress interface {
	Step(what string)         // a new step, e.g. "Client master"
	Count(done, total int)    // rows or documents done in this step; total 0 if unknown
	Log(severity, msg string) // Info, Warning, Error
}

// NopProgress ignores progress.
type NopProgress struct{}

func (NopProgress) Step(string)        {}
func (NopProgress) Count(int, int)     {}
func (NopProgress) Log(string, string) {}
