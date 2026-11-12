// Package run is the migration tool's runner: it finds out what each
// database is, chooses which to read, has the Mapper read them, and writes
// the migration package (split into parts when documents make it large),
// with a report.
package run

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

// Input is one database the tool can read.
type Input struct {
	DB      migrate.Database
	Q       migrate.Querier // nil when it could not be restored or opened
	Problem string          // why it cannot be read (restore checks), in plain English
	Live    bool            // a database already on the server, read in place
}

// Found is one row of "What we found".
type Found struct {
	Input
	Kind     migrate.Kind
	Use      bool
	Why      string // why it is skipped
	UserSkip bool   // the user chose Skip
}

// Survey is what the tool found and what it will read.
type Survey struct {
	Mapper   migrate.Mapper
	Found    []Found
	Firms    []pkg.Firm
	Years    int
	Warnings []string
	Now      time.Time

	firmsFrom string // the newest company database Firms was read from
}

// FYStart is the first calendar year of a financial year "2026-27"; 0 if
// fy is not one.
func FYStart(fy string) int {
	a, b, ok := strings.Cut(fy, "-")
	if !ok || len(a) != 4 {
		return 0
	}
	n, err := strconv.Atoi(a)
	if err != nil {
		return 0
	}
	if m, err := strconv.Atoi(b); err != nil || m != (n+1)%100 {
		return 0
	}
	return n
}

// Classify asks the Mapper what each database is and chooses what to read
// for years of history (default 8).
func Classify(ctx context.Context, m migrate.Mapper, inputs []Input, years int, now time.Time) (*Survey, error) {
	s := &Survey{Mapper: m, Now: now}
	for _, in := range inputs {
		f := Found{Input: in}
		if in.Q == nil {
			if f.Problem == "" {
				f.Problem = "The database could not be opened."
			}
		} else {
			k, err := m.Classify(ctx, in.Q)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				f.Problem = "Could not tell what this database is: " + err.Error()
			}
			f.Kind = k
		}
		s.Found = append(s.Found, f)
	}
	s.Select(years)
	if err := s.firms(ctx); err != nil {
		return s, err
	}
	return s, nil
}

// Toggle sets the user's Skip on row i and chooses again.
func (s *Survey) Toggle(ctx context.Context, i int, skip bool) error {
	if i < 0 || i >= len(s.Found) {
		return fmt.Errorf("no row %d", i)
	}
	s.Found[i].UserSkip = skip
	s.Select(s.Years)
	return s.firms(ctx)
}

// SetYears chooses again for a different number of years.
func (s *Survey) SetYears(ctx context.Context, years int) error {
	s.Select(years)
	return s.firms(ctx)
}

func (s *Survey) firms(ctx context.Context) error {
	n := s.Newest()
	if n == nil {
		s.Firms = nil
		s.firmsFrom = ""
		return nil
	}
	if s.firmsFrom == n.DB.Name && s.Firms != nil {
		return nil
	}
	var admin migrate.Querier
	if a := s.Admin(); a != nil {
		admin = a.Q
	}
	firms, err := s.Mapper.Firms(ctx, admin, n.Q)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.Warnings = append(s.Warnings, "Could not list the firms: "+err.Error()+". Every firm will be migrated.")
		s.Firms = []pkg.Firm{}
	} else {
		s.Firms = firms
	}
	s.firmsFrom = n.DB.Name
	return nil
}

// Newest is the newest company database in use.
func (s *Survey) Newest() *Found { return s.newest() }

func (s *Survey) newest() *Found {
	var best *Found
	for i := range s.Found {
		f := &s.Found[i]
		if !f.Use || f.Kind.Role != "company" {
			continue
		}
		if best == nil || FYStart(f.Kind.FY) > FYStart(best.Kind.FY) {
			best = f
		}
	}
	return best
}

// Admin is the admin database in use, if any.
func (s *Survey) Admin() *Found {
	for i := range s.Found {
		if s.Found[i].Use && s.Found[i].Kind.Role == "admin" {
			return &s.Found[i]
		}
	}
	return nil
}

// Select decides Use and Why for every row.
func (s *Survey) Select(years int) {
	if years < 1 || years > 8 {
		years = 8
	}
	s.Years = years
	s.Warnings = nil
	// newer copies first, so a duplicate is the older one
	idx := make([]int, len(s.Found))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return s.Found[idx[a]].DB.BackupDate.After(s.Found[idx[b]].DB.BackupDate)
	})
	newestFY := 0
	for _, i := range idx {
		f := &s.Found[i]
		if f.Problem == "" && !f.UserSkip && !f.Kind.Skip && f.Kind.Role == "company" {
			if y := FYStart(f.Kind.FY); y > newestFY {
				newestFY = y
			}
		}
	}
	first := newestFY - years + 1
	taken := map[string]string{}
	for _, i := range idx {
		f := &s.Found[i]
		f.Use, f.Why = false, ""
		switch {
		case f.Problem != "":
			f.Why = f.Problem
		case f.UserSkip:
			f.Why = "You chose to skip it."
		case f.Kind.Skip:
			f.Why = f.Kind.Note
			if f.Why == "" {
				f.Why = "Not needed for the migration."
			}
		case f.Kind.Role != "company" && f.Kind.Role != "mail" && f.Kind.Role != "admin":
			f.Why = "Not needed for the migration."
		case (f.Kind.Role == "company" || f.Kind.Role == "mail") && f.Kind.FY != "" && FYStart(f.Kind.FY) < first:
			f.Why = fmt.Sprintf("Older than the %d years of history chosen.", years)
		case (f.Kind.Role == "company" || f.Kind.Role == "mail") && f.Kind.FY != "" && FYStart(f.Kind.FY) > newestFY:
			f.Why = "Newer than the newest company database; drop the company backup of " + f.Kind.FY + " too."
		default:
			key := f.Kind.Role + "/" + f.Kind.FY
			if f.Kind.Role == "mail" && f.Kind.FY == "" {
				key = "mail/" + f.DB.Original
			}
			if other, dup := taken[key]; dup {
				f.Why = "Another copy of the same database (" + other + ") is newer and is used instead."
			} else {
				taken[key] = f.DB.Original
				f.Use = true
			}
		}
	}
	n := s.newest()
	switch {
	case n == nil:
		s.Warnings = append(s.Warnings, "No company database was found. Drop the latest backup of the company database (in SharePro named like NC2627).")
	case !n.DB.BackupDate.IsZero() && !n.Live && s.Now.Sub(n.DB.BackupDate) > 48*time.Hour:
		s.Warnings = append(s.Warnings, fmt.Sprintf("The newest company backup (%s) is from %s, more than 2 days ago. Take a fresh backup in SharePro before migrating.", n.DB.Original, n.DB.BackupDate.Format("2 Jan 2006 15:04")))
	}
}

// Used lists the rows in use, in the Mapper's order.
func (s *Survey) Used() []*Found {
	var out []*Found
	for i := range s.Found {
		if s.Found[i].Use {
			out = append(out, &s.Found[i])
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Kind.Order < out[b].Kind.Order })
	return out
}

// Backups is the manifest's list of what was read and skipped.
func (s *Survey) Backups() []pkg.Backup {
	var out []pkg.Backup
	for _, f := range s.Found {
		b := pkg.Backup{File: f.DB.File, Database: f.DB.Original, Role: f.Kind.Role, FY: f.Kind.FY, BackupDate: f.DB.BackupDate, Bytes: f.DB.Bytes, SHA256: f.DB.SHA256, Used: f.Use}
		if b.Database == "" {
			b.Database = f.DB.Name
		}
		if b.Role == "" {
			b.Role = "other"
		}
		if f.Use {
			b.Note = f.Kind.Note
			if f.Live {
				b.Note = strings.TrimSpace(b.Note + " (read in place on the server)")
			}
		} else {
			b.Note = f.Why
		}
		out = append(out, b)
	}
	return out
}
