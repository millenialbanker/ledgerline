package ops

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/ingest"
)

// ---- file intake: recognise and load files without being told what they are ----

// InFile is one file handed to intake.
type InFile struct {
	Name string
	Data []byte
}

// IntakeResult is what happened to one file.
type IntakeResult struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Date    string `json:"date,omitempty"`
	Rows    int    `json:"rows"`
	Status  string `json:"status"` // Loaded, Already loaded, Not recognised, Rejected
	Message string `json:"message,omitempty"`
}

// intakeOrder loads masters before what depends on them.
var intakeOrder = []string{"CLIENT-MASTER", "NOMINEES", "SEGMENT-STATUS", "OPENING-BALANCES", "MARKET-DATA", "DP-HOLDINGS", "HOLDING-LOTS", "POSITIONS", "DEBARRED-LIST", "KRA-STATUS", "CKYC-NUMBERS", "NRI-CAUTION", "MTF-ELIGIBLE"}

var reDateInName = regexp.MustCompile(`(20\d\d)-?(\d\d)-?(\d\d)`)

func firstLine(b []byte) []string {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	if !sc.Scan() {
		return nil
	}
	l := strings.TrimPrefix(sc.Text(), "\ufeff")
	sep := ","
	if strings.Count(l, "|") > strings.Count(l, ",") {
		sep = "|"
	}
	var out []string
	for _, f := range strings.Split(l, sep) {
		out = append(out, strings.ToLower(strings.TrimSpace(strings.Trim(f, `"`))))
	}
	return out
}

// DetectSource works out a file's type from its header (and, for files
// with no header, its name). It returns "" when unsure.
func (o *Ops) DetectSource(name string, data []byte) string {
	head := firstLine(data)
	has := map[string]bool{}
	for _, h := range head {
		has[h] = true
	}
	best, bestN, bestScore := "", 0, -1
	for _, m := range MasterSources() {
		ok := true
		for _, c := range m.Columns {
			if !has[strings.ToLower(c)] {
				ok = false
				break
			}
		}
		if ok && len(m.Columns) > bestN {
			best, bestN = m.Name, len(m.Columns)
		}
	}
	lname := strings.ToLower(name)
	for _, s := range o.App.Ingest.Registry.List() {
		if !s.Header {
			// No header: recognise by name and delimiter.
			key := strings.ToLower(strings.SplitN(s.Name, "-", 2)[0])
			if strings.Contains(lname, key) && len(head) > 1 && strings.Count(string(bytes.SplitN(data, []byte("\n"), 2)[0]), s.Delimiter) >= len(s.Columns)-1 && len(s.Columns) > bestN {
				best, bestN = s.Name, len(s.Columns)
			}
			continue
		}
		ok := true
		for _, col := range s.Columns {
			if !has[strings.ToLower(col)] {
				ok = false
				break
			}
		}
		if !ok || len(s.Columns) < bestN {
			continue
		}
		// Several exchange layouts share columns: prefer the one whose name
		// parts (exchange, segment) the file name carries.
		score := 0
		for _, part := range strings.Split(strings.ToLower(s.Name), "-") {
			if part != "trade" && strings.Contains(lname, part) {
				score++
			}
		}
		if len(s.Columns) > bestN || score > bestScore {
			best, bestN, bestScore = s.Name, len(s.Columns), score
		}
	}
	return best
}

// Intake recognises each file and loads it, masters first, then trade
// files, then the rest. Files already loaded are skipped by their content.
// Afterwards the automations that the files feed run at once.
func (o *Ops) Intake(ctx context.Context, tenant, actor, date string, files []InFile) ([]IntakeResult, error) {
	if err := o.require(ctx, tenant, actor, "Loading files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return nil, err
	}
	type item struct {
		f    InFile
		src  string
		rank int
	}
	var items []item
	var out []IntakeResult
	for _, f := range files {
		src := o.DetectSource(f.Name, f.Data)
		if src == "" {
			out = append(out, IntakeResult{Name: f.Name, Status: "Not recognised", Message: "the columns do not match any file type; load it on the Files page with its type"})
			continue
		}
		rank := 50
		for i, s := range intakeOrder {
			if s == src {
				rank = i
			}
		}
		if !IsMasterSource(src) {
			rank = 30 // trade files after the masters
			if strings.HasPrefix(src, "OMS") {
				rank = 31
			}
		}
		items = append(items, item{f, src, rank})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].rank < items[j].rank })
	loaded := map[string]bool{}
	for _, it := range items {
		d := date
		if it.src == "OPENING-BALANCES" || it.src == "HOLDING-LOTS" {
			// Balances and lots are as at the close of the day before.
			set, _ := o.Settings(ctx, tenant)
			d = set.PrevBusinessDay(date)
		}
		if m := reDateInName.FindStringSubmatch(it.f.Name); m != nil {
			d = m[1] + "-" + m[2] + "-" + m[3]
			if _, err := time.Parse("2006-01-02", d); err != nil {
				d = date
			}
		}
		res := IntakeResult{Name: it.f.Name, Source: it.src, Status: "Loaded"}
		var r ingest.Result
		var err error
		if IsMasterSource(it.src) {
			needs := false
			for _, m := range MasterSources() {
				if m.Name == it.src {
					needs = m.NeedDate
				}
			}
			if needs {
				res.Date = d
			}
			r, err = o.LoadFile(ctx, tenant, actor, it.src, res.Date, it.f.Name, it.f.Data)
		} else {
			r, err = o.App.IngestFile(ctx, tenant, actor, it.src, "", it.f.Name, it.f.Data)
		}
		res.Rows = r.Added
		switch {
		case err != nil:
			res.Status, res.Message = "Rejected", err.Error()
		case r.Added == 0 && r.DuplicateRows > 0:
			res.Status = "Already loaded"
		}
		if r.File.PriorID != "" && r.Added == 0 {
			res.Status = "Already loaded"
		}
		if err == nil {
			loaded[it.src] = true
		}
		out = append(out, res)
	}
	o.log(ctx, tenant, actor, "intake", date, map[string]any{"files": len(files), "recognised": len(items)})
	// Event-driven automations.
	var follow []string
	if loaded["PLEDGE-REQUESTS"] {
		follow = append(follow, "pledges")
	}
	if loaded["BANK-STMT"] {
		follow = append(follow, "third_party", "unidentified")
	}
	for _, id := range follow {
		if _, err := o.RunStage(ctx, tenant, SystemActor, date, "", id); err != nil {
			return out, fmt.Errorf("after intake, %s: %w", id, err)
		}
	}
	return out, nil
}
