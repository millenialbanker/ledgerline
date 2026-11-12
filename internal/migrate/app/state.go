package app

import (
	"path/filepath"
	"runtime"
	"sort"

	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/migrate/run"
)

// FoundRow is one row of "What we found".
type FoundRow struct {
	Index    int    `json:"index"`
	File     string `json:"file"`
	Database string `json:"database"`
	Note     string `json:"note"`
	Role     string `json:"role"`
	FY       string `json:"fy"`
	Date     string `json:"date"`
	Bytes    int64  `json:"bytes"`
	Use      bool   `json:"use"`
	Why      string `json:"why,omitempty"`
	UserSkip bool   `json:"user_skip"`
	Live     bool   `json:"live"`
	Problem  bool   `json:"problem"`
}

// ResultView is the finished run for the page.
type ResultView struct {
	Parts   []run.PartFile   `json:"parts"`
	Report  string           `json:"report"`
	Folder  string           `json:"folder"`
	Checks  []CheckView      `json:"checks"`
	Groups  []run.IssueGroup `json:"groups"`
	Errors  int              `json:"errors"`
	Dropped []pkg.Dropped    `json:"dropped"`
	Docs    []run.DocCount   `json:"docs"`
}

// CheckView is a check with its label.
type CheckView struct {
	pkg.Check
	Label string `json:"label"`
}

// State is everything the page shows.
type State struct {
	Screen    string      `json:"screen"`
	OS        string      `json:"os"`
	Arch      string      `json:"arch"`
	Version   string      `json:"version"`
	Mapper    string      `json:"mapper"`
	Work      string      `json:"work"`
	WorkFree  int64       `json:"work_free"`
	Out       string      `json:"out"`
	Server    Server      `json:"server"`
	Files     []File      `json:"files"`
	Job       *Job        `json:"job,omitempty"`
	Found     []FoundRow  `json:"found,omitempty"`
	Warnings  []string    `json:"warnings,omitempty"`
	Firms     []pkg.Firm  `json:"firms,omitempty"`
	Years     int         `json:"years"`
	Live      []LiveRow   `json:"live,omitempty"`
	Result    *ResultView `json:"result,omitempty"`
	Passwords []File      `json:"passwords,omitempty"`
}

// LiveRow is a database already on the server.
type LiveRow struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Use   bool   `json:"use"`
}

// State is a snapshot for the page. It never holds a password.
func (s *Session) State() State {
	free := s.FreeWork()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{Screen: s.screen, OS: s.GOOS, Arch: runtime.GOARCH, Version: Version, Work: s.Work, WorkFree: free, Out: s.Out, Server: s.server, Years: 8}
	if s.Mapper != nil {
		st.Mapper = s.Mapper.Name()
	}
	for _, f := range s.files {
		c := *f
		c.password, c.items = "", nil
		st.Files = append(st.Files, c)
		if f.Status == "password" {
			st.Passwords = append(st.Passwords, c)
		}
	}
	if s.job != nil {
		j := *s.job
		j.Log = append([]LogEntry(nil), s.job.Log...)
		j.Steps = append([]string(nil), s.job.Steps...)
		st.Job = &j
	}
	for _, d := range s.live {
		st.Live = append(st.Live, LiveRow{Name: d.Name, Bytes: d.Bytes, Use: s.liveUse[d.Name]})
	}
	if sv := s.survey; sv != nil {
		st.Years = sv.Years
		st.Warnings = append([]string(nil), sv.Warnings...)
		st.Firms = sv.Firms
		for i, f := range sv.Found {
			row := FoundRow{Index: i, File: f.DB.File, Database: f.DB.Original, Note: f.Kind.Note, Role: f.Kind.Role, FY: f.Kind.FY, Bytes: f.DB.Bytes, Use: f.Use, Why: f.Why, UserSkip: f.UserSkip, Live: f.Live, Problem: f.Problem != ""}
			if row.Database == "" {
				row.Database = f.DB.Name
			}
			if row.Note == "" && f.Problem != "" {
				row.Note = "Could not be read"
			}
			if !f.DB.BackupDate.IsZero() {
				row.Date = f.DB.BackupDate.Format("02 Jan 2006 15:04")
			} else if f.Live {
				row.Date = "Live, " + s.Now().Format("02 Jan 2006")
			}
			st.Found = append(st.Found, row)
		}
		// newest year first, then the admin and mail databases, skipped last
		sort.SliceStable(st.Found, func(a, b int) bool {
			x, y := st.Found[a], st.Found[b]
			if x.Use != y.Use {
				return x.Use
			}
			return sv.Found[x.Index].Kind.Order < sv.Found[y.Index].Kind.Order
		})
	}
	if r := s.result; r != nil {
		v := &ResultView{Parts: r.Parts, Report: r.Report, Folder: filepath.Dir(r.Report), Groups: run.GroupIssues(r.Manifest.Issues), Dropped: r.Manifest.Dropped, Docs: r.Docs}
		for _, c := range r.Manifest.Checks {
			v.Checks = append(v.Checks, CheckView{Check: c, Label: run.CheckLabel(c.Name)})
		}
		for _, i := range r.Manifest.Issues {
			if i.Severity == "Error" {
				v.Errors++
			}
		}
		st.Result = v
	}
	return st
}
