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
	Firms []string  // company codes to migrate (empty: all)
	AsOf  time.Time // go-live balances as of this date (zero: the latest date in the newest year)
	// Scope is pkg.ScopeGoLive (empty means go-live) or pkg.ScopeHistory.
	Scope string
	// FYs are the financial years ("2026-27") whose history and documents
	// go in the package. The runner fills it in before calling a Mapper:
	// for a go-live package, the current financial year (the newest
	// company database's) and any earlier years chosen; for a history
	// package, the earlier years chosen. Empty means every year read.
	FYs []string
	// Years is the older way of choosing: how many financial years back
	// from the newest. The runner turns it into FYs when FYs is empty.
	Years       int
	Documents   bool   // archive documents (contract notes, statements) from mail databases
	DormantMode string // "inactive" (default) or "skip": clients with no active segment
	// ClientInterest is "keep" (default: clients' own interest rates come
	// across) or "firm" (every client moves to the firm's rate; their own
	// interest-free and grace-day terms still come across).
	ClientInterest string
}

// IsHistory says whether the plan makes a history package.
func (p Plan) IsHistory() bool { return p.Scope == pkg.ScopeHistory }

// Wants says whether financial year fy's history and documents go in the
// package. A Mapper reading a database (or a mail archive) of a year not
// wanted leaves it out. An empty fy (an archive that spans years) is
// wanted; the Mapper then checks each row's own year.
func (p Plan) Wants(fy string) bool {
	if len(p.FYs) == 0 || fy == "" {
		return true
	}
	for _, f := range p.FYs {
		if f == fy {
			return true
		}
	}
	return false
}

// Preparer is implemented by a Mapper that, for a history package (which
// has no go-live files), must first read the newest company database for
// what its history and document steps rely on, such as which client each
// DP account belongs to. Prepare writes nothing to the package.
type Preparer interface {
	Prepare(ctx context.Context, q Querier, k Kind, plan Plan, p Progress) error
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

var mappers = map[string]Mapper{}

// Register makes a Mapper available to the tool (called from init).
func Register(m Mapper) { mappers[m.Name()] = m }

// Mappers lists the registered mappers.
func Mappers() []Mapper {
	out := make([]Mapper, 0, len(mappers))
	for _, m := range mappers {
		out = append(out, m)
	}
	return out
}

// MapperFor returns the named mapper, or nil.
func MapperFor(name string) Mapper { return mappers[name] }
