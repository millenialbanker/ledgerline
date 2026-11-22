package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// ---- Files → Parallel run: the old system's file against Ledgerline's ----
//
// Before going live a broker runs Ledgerline beside its old back office and
// compares the files both make for the same day: the margin reporting file,
// the holding statement, the SEGMON file, the UCC upload, the payout file…
// The pilot's go-live condition is ten business days in a row on which
// every file compared matches.
//
// A comparison takes two files of one layout (any in config/formats) and
// one day. Rows are matched on their identifying columns (client code,
// demat account, account type, ISIN, segment, exchange, settlement, symbol,
// series, expiry, strike, option type, trade and order numbers), found by
// the columns' keys and names; record numbers and fillers are ignored.
// Numbers are compared as numbers (1,000.50 is 1000.5) and text without
// case or extra spaces. A JSON file is read from its first array of
// records, each record's fields as columns. The result: rows only in one
// file, rows that differ and in which column, each column's difference
// count and, for number columns, both files' totals.
//
// Neither file is kept (only its name, row count and SHA-256). Up to 300
// difference lines are kept, with client codes, demat accounts and text
// masked; numbers are shown in full. A day matches when the latest
// comparison of every layout compared that day matches; the streak counts
// business days back from the latest day compared, and a business day with
// no comparison ends it. A comparison that differs raises an Ops checker
// work item, closed by a later comparison of the same layout and day that
// matches.

const (
	cParallel = "parallelrun" // a comparison, by ID (PR-0000001)

	parallelGoal     = 10  // matching days in a row: the pilot's go-live condition
	parallelMaxLines = 300 // difference lines kept per comparison
	parallelMaxRows  = 500000
	wParallel        = "parallel" // work queue process
)

// ParallelColumn is one column's comparison.
type ParallelColumn struct {
	Name    string `json:"name"`
	Key     bool   `json:"key,omitempty"` // an identifying column (rows are matched on it)
	Numeric bool   `json:"numeric,omitempty"`
	Diffs   int    `json:"diffs"`
	// OldTotal and NewTotal are the column's totals in each file (number
	// columns only).
	OldTotal string `json:"old_total,omitempty"`
	NewTotal string `json:"new_total,omitempty"`
}

// ParallelLine is one difference, masked.
type ParallelLine struct {
	Kind   string `json:"kind"` // only in old, only in new, differs
	Key    string `json:"key"`
	Column string `json:"column,omitempty"`
	Old    string `json:"old,omitempty"`
	New    string `json:"new,omitempty"`
}

// ParallelComparison is one comparison, as kept.
type ParallelComparison struct {
	ID          string           `json:"id"`
	Date        string           `json:"date"`
	Format      string           `json:"format"`
	FormatTitle string           `json:"format_title"`
	OldName     string           `json:"old_name"`
	NewName     string           `json:"new_name"`
	OldSHA      string           `json:"old_sha256"`
	NewSHA      string           `json:"new_sha256"`
	OldRows     int              `json:"old_rows"`
	NewRows     int              `json:"new_rows"`
	OldIssues   int              `json:"old_issues"` // lines that do not fit the layout (still compared)
	NewIssues   int              `json:"new_issues"`
	KeyColumns  []string         `json:"key_columns"`
	Ignored     []string         `json:"ignored"` // record numbers and fillers
	Matched     int              `json:"matched"` // rows in both files, the same
	Differing   int              `json:"differing"`
	OnlyOld     int              `json:"only_old"`
	OnlyNew     int              `json:"only_new"`
	Match       bool             `json:"match"`
	Columns     []ParallelColumn `json:"columns"`
	Lines       []ParallelLine   `json:"lines"`
	MoreLines   int              `json:"more_lines"` // differences beyond those kept
	At          string           `json:"at"`
	By          string           `json:"by"`
}

// ParallelDay is one day's comparisons.
type ParallelDay struct {
	Date    string              `json:"date"`
	Match   bool                `json:"match"`
	Formats []ParallelDayFormat `json:"formats"`
}

// ParallelDayFormat is a layout's latest comparison of a day.
type ParallelDayFormat struct {
	Format      string `json:"format"`
	FormatTitle string `json:"format_title"`
	ID          string `json:"id"`
	Match       bool   `json:"match"`
	Differences int    `json:"differences"`
	Runs        int    `json:"runs"` // comparisons of the layout that day
}

// ParallelView is the Parallel run screen.
type ParallelView struct {
	Days        []ParallelDay        `json:"days"` // the latest 30 days compared, newest first
	Streak      int                  `json:"streak"`
	StreakTo    string               `json:"streak_to,omitempty"`
	StreakFrom  string               `json:"streak_from,omitempty"`
	Goal        int                  `json:"goal"`
	Recent      []ParallelComparison `json:"recent"` // the latest 50, without their lines
	Formats     []XFormat            `json:"formats"`
	CanCompare  bool                 `json:"can_compare"`
	MaxLines    int                  `json:"max_lines"`
	BusinessDay string               `json:"business_date"`
}

func (o *Ops) parallelSee(ctx context.Context, tenant, actor string) error {
	return o.require(ctx, tenant, actor, "Seeing the parallel run", RoleMaker, RoleChecker, RoleRisk, RoleCompliance)
}

// ParallelRun is the screen: the days compared, the streak and the latest
// comparisons.
func (o *Ops) ParallelRun(ctx context.Context, tenant, actor string) (ParallelView, error) {
	if err := o.parallelSee(ctx, tenant, actor); err != nil {
		return ParallelView{}, err
	}
	v := ParallelView{Days: []ParallelDay{}, Recent: []ParallelComparison{}, Goal: parallelGoal, MaxLines: parallelMaxLines, BusinessDay: o.businessDate(ctx, tenant)}
	v.CanCompare = o.require(ctx, tenant, actor, "Comparing files in the parallel run", RoleMaker, RoleChecker, RoleCompliance) == nil
	fs, err := o.ExchangeFormats()
	if err != nil {
		return v, err
	}
	v.Formats = fs
	all, err := docs.List[ParallelComparison](ctx, o.Docs, tenant, cParallel, "")
	if err != nil {
		return v, err
	}
	set, _ := o.Settings(ctx, tenant)
	days := parallelDays(all)
	v.Streak, v.StreakFrom, v.StreakTo = parallelStreak(set, days)
	for i := len(days) - 1; i >= 0 && len(v.Days) < 30; i-- {
		v.Days = append(v.Days, days[i])
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	for _, c := range all {
		if len(v.Recent) == 50 {
			break
		}
		c.Lines = nil
		v.Recent = append(v.Recent, c)
	}
	return v, nil
}

// parallelDays groups comparisons by day, oldest first: each layout's
// latest comparison of the day decides it.
func parallelDays(all []ParallelComparison) []ParallelDay {
	sort.SliceStable(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	by := map[string]map[string]*ParallelDayFormat{}
	for _, c := range all {
		if by[c.Date] == nil {
			by[c.Date] = map[string]*ParallelDayFormat{}
		}
		f := by[c.Date][c.Format]
		if f == nil {
			f = &ParallelDayFormat{Format: c.Format}
			by[c.Date][c.Format] = f
		}
		f.FormatTitle, f.ID, f.Match, f.Differences = c.FormatTitle, c.ID, c.Match, c.Differing+c.OnlyOld+c.OnlyNew
		f.Runs++
	}
	out := []ParallelDay{}
	for _, d := range sortedKeys(by) {
		day := ParallelDay{Date: d, Match: true, Formats: []ParallelDayFormat{}}
		for _, k := range sortedKeys(by[d]) {
			f := *by[d][k]
			day.Match = day.Match && f.Match
			day.Formats = append(day.Formats, f)
		}
		out = append(out, day)
	}
	return out
}

// parallelStreak counts matching business days back from the latest day
// compared; a business day with no comparison, or one that differs, ends
// it.
func parallelStreak(set Settings, days []ParallelDay) (n int, from, to string) {
	if len(days) == 0 {
		return 0, "", ""
	}
	by := map[string]ParallelDay{}
	for _, d := range days {
		by[d.Date] = d
	}
	d := days[len(days)-1].Date
	for i := 0; i < 400; i++ {
		day, ok := by[d]
		if !ok || !day.Match {
			break
		}
		if n == 0 {
			to = d
		}
		n++
		from = d
		d = set.PrevBusinessDay(d)
	}
	return n, from, to
}

// ParallelComparisonByID is one comparison with its difference lines.
func (o *Ops) ParallelComparisonByID(ctx context.Context, tenant, actor, id string) (ParallelComparison, error) {
	if err := o.parallelSee(ctx, tenant, actor); err != nil {
		return ParallelComparison{}, err
	}
	c, _, err := docs.Get[ParallelComparison](ctx, o.Docs, tenant, cParallel, id)
	return c, err
}

// ParallelCompare compares the old system's file with Ledgerline's file of
// the same layout and day. format may be empty: the layout is then
// recognised from Ledgerline's file (or the old one).
func (o *Ops) ParallelCompare(ctx context.Context, tenant, actor, date, format, oldName string, oldData []byte, newName string, newData []byte) (ParallelComparison, error) {
	if err := o.require(ctx, tenant, actor, "Comparing files in the parallel run", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return ParallelComparison{}, err
	}
	if !validDate(date) {
		return ParallelComparison{}, invalid("give the day both files are for (YYYY-MM-DD)")
	}
	for _, d := range [][]byte{oldData, newData} {
		if len(d) == 0 || len(d) > MaxXFileBytes {
			return ParallelComparison{}, invalid("choose both files: the old system's and Ledgerline's, each up to 100 MB")
		}
	}
	lib, err := XFiles()
	if err != nil {
		return ParallelComparison{}, err
	}
	of, err := parallelUnpack(oldName, oldData)
	if err != nil {
		return ParallelComparison{}, err
	}
	nf, err := parallelUnpack(newName, newData)
	if err != nil {
		return ParallelComparison{}, err
	}
	var s *xfile.Spec
	if format != "" {
		sp, ok := lib.Get(format)
		if !ok {
			return ParallelComparison{}, invalid("unknown layout %q", format)
		}
		s = sp
	} else {
		for _, f := range []xfile.File{nf, of} {
			if sp, err := lib.Recognise(f.Name, f.Data); err == nil {
				s = sp
				break
			}
		}
		if s == nil {
			return ParallelComparison{}, invalid("the files' layout was not recognised from their names: choose the layout")
		}
	}
	ot, err := parallelRead(s, of)
	if err != nil {
		return ParallelComparison{}, invalid("the old system's file: %v", err)
	}
	nt, err := parallelRead(s, nf)
	if err != nil {
		return ParallelComparison{}, invalid("Ledgerline's file: %v", err)
	}
	c := parallelDiff(ot, nt)
	sha := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	c.Date, c.Format, c.FormatTitle = date, s.ID, s.Title
	c.OldName, c.NewName, c.OldSHA, c.NewSHA = clip(oldName, 200), clip(newName, 200), sha(oldData), sha(newData)
	c.At, c.By = o.now().Format(time.RFC3339), actor
	n, err := o.next(ctx, tenant, "parallelrun", actor)
	if err != nil {
		return c, err
	}
	c.ID = fmt.Sprintf("PR-%07d", n)
	if err := docs.Upsert(ctx, o.Docs, tenant, cParallel, c.ID, c, actor); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "parallel.compared", c.ID, map[string]any{"date": date, "format": s.ID, "match": c.Match, "old_rows": c.OldRows, "new_rows": c.NewRows,
		"differing": c.Differing, "only_old": c.OnlyOld, "only_new": c.OnlyNew, "old_sha256": c.OldSHA, "new_sha256": c.NewSHA})
	// One work item per differing comparison (a Done item is never
	// reopened); a later match of the layout and day closes them all.
	key := "diff-" + date + "-" + s.ID + "-"
	if c.Match {
		ws, _ := docs.List[WorkItem](ctx, o.Docs, tenant, cWorkItem, "")
		for _, w := range ws {
			if k := strings.TrimPrefix(w.Key, wParallel+":"); w.Process == wParallel && w.Status == "Open" && strings.HasPrefix(k, key) {
				o.closeWork(ctx, tenant, actor, wParallel, k, "matched in "+c.ID)
			}
		}
	} else {
		o.raiseWork(ctx, tenant, wParallel, key+c.ID, fmt.Sprintf("Parallel run: %s differs for %s", s.Title, fmtDay(date)),
			fmt.Sprintf("%s: %d rows differ, %d only in the old system's file, %d only in Ledgerline's. Find the cause (Ledgerline's data, a setting, or the old system), fix it and compare again.", c.ID, c.Differing, c.OnlyOld, c.OnlyNew),
			"", RoleChecker, "parallel", date)
	}
	return c, nil
}

func parallelUnpack(name string, data []byte) (xfile.File, error) {
	fs, err := xfile.Unpack(firstOf(name, "file"), data)
	if err != nil {
		return xfile.File{}, invalid("%v", err)
	}
	if len(fs) != 1 {
		return xfile.File{}, invalid("%s: compare one file at a time (the archive has %d)", name, len(fs))
	}
	return fs[0], nil
}

// pTable is a file read for comparing: its columns (by key, in order) and
// rows.
type pTable struct {
	cols   []pCol
	rows   []map[string]string
	issues int
}

type pCol struct {
	key, name, typ string
}

// parallelRead reads a file with the layout; a JSON file from its first
// array of records.
func parallelRead(s *xfile.Spec, f xfile.File) (pTable, error) {
	if s.Format == "json" {
		return parallelJSON(f.Data)
	}
	t, err := s.Read(f.Name, f.Data)
	if err != nil {
		return pTable{}, err
	}
	pt := pTable{issues: len(t.Issues)}
	seen := map[string]bool{}
	add := func(cs []xfile.Column) {
		for _, c := range cs {
			k := firstOf(c.Key, c.Name)
			if !seen[k] {
				seen[k] = true
				pt.cols = append(pt.cols, pCol{key: k, name: c.Name, typ: c.Type})
			}
		}
	}
	add(s.Columns)
	for _, r := range s.Records {
		add(r.Columns)
	}
	for _, r := range t.Rows {
		if r.Blank() || r.Record == "(text)" {
			continue
		}
		row := map[string]string{"\x00rec": r.Record}
		for k, v := range r.Values {
			row[k] = v
		}
		pt.rows = append(pt.rows, row)
		if len(pt.rows) > parallelMaxRows {
			return pt, fmt.Errorf("more than %d rows", parallelMaxRows)
		}
	}
	return pt, nil
}

// parallelJSON reads the first array of records in a JSON file.
func parallelJSON(data []byte) (pTable, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return pTable{}, fmt.Errorf("not JSON: %v", err)
	}
	arr := firstRecords(v)
	if arr == nil {
		return pTable{}, fmt.Errorf("no array of records in the file")
	}
	pt := pTable{}
	seen := map[string]bool{}
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		row := map[string]string{}
		for _, k := range sortedKeys(m) {
			if !seen[k] {
				seen[k] = true
				pt.cols = append(pt.cols, pCol{key: k, name: k})
			}
			switch x := m[k].(type) {
			case string:
				row[k] = x
			case nil:
				row[k] = ""
			default:
				b, _ := json.Marshal(x)
				row[k] = string(b)
			}
		}
		pt.rows = append(pt.rows, row)
	}
	return pt, nil
}

// firstRecords finds the first array of objects, looking at the top level
// first and then into each field in order.
func firstRecords(v any) []any {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if _, ok := e.(map[string]any); ok {
				return x
			}
		}
	case map[string]any:
		keys := sortedKeys(x)
		for _, k := range keys {
			if a, ok := x[k].([]any); ok {
				if r := firstRecords(a); r != nil {
					return r
				}
			}
		}
		for _, k := range keys {
			if m, ok := x[k].(map[string]any); ok {
				if r := firstRecords(m); r != nil {
					return r
				}
			}
		}
	}
	return nil
}

// Identifying columns, record numbers and fillers, by a column's key or
// name compared as letters and digits.
var (
	pKeyNames = map[string]string{
		"ucc": "client", "clientcode": "client", "clientid": "client", "clientcd": "client", "clntid": "client", "clntcd": "client", "ccdcd": "client", "uniqueclientcode": "client", "client": "client", "ucccode": "client", "partycode": "client",
		"dmat": "demat", "demat": "demat", "boid": "demat", "bnfclownrid": "demat", "dematid": "demat", "dpid": "demat", "clientdematid": "demat", "dematacno": "demat", "dematacctno": "demat", "beneficiaryaccount": "demat", "boaccount": "demat", "boaccountno": "demat", "ccdbenacctno": "demat",
		"accounttype": "plain", "accttp": "plain", "acctype": "plain", "isin": "plain", "isincode": "plain", "segment": "plain", "sgmt": "plain", "segmentindicator": "plain", "ccdsegind": "plain",
		"exchange": "plain", "xchg": "plain", "exch": "plain", "settlement": "plain", "settlementno": "plain", "settno": "plain", "sttlmid": "plain", "settlementid": "plain", "settlementtype": "plain", "sttlmtp": "plain",
		"symbol": "plain", "tckrsymb": "plain", "series": "plain", "sctysrs": "plain", "expiry": "plain", "xpirydt": "plain", "expirydate": "plain", "strikeprice": "plain", "strkpric": "plain", "optiontype": "plain", "optntp": "plain",
		"tradeno": "plain", "tradenumber": "plain", "uniqtradid": "plain", "tradeid": "plain", "orderno": "plain", "ordernumber": "plain", "ordrref": "plain", "contract": "plain", "fininstrmid": "plain", "recordtype": "plain",
	}
	reRecNo  = regexp.MustCompile(`^(recid|recordno|recordnumber|recno|lineno|linenumber|srno|slno|serialno|serialnumber|sno|seqno|sequenceno|sequencenumber|rowno)$`)
	reFiller = regexp.MustCompile(`filler|reserved|rsvd|dummy`)
)

func pNorm(s string) string { return reNotAlnum.ReplaceAllString(strings.ToLower(s), "") }

// pKind is how a column takes part: key (client, demat or plain), ignored,
// or compared ("").
func pKind(c pCol) string {
	if c.typ == "blank" {
		return "ignore"
	}
	for _, n := range []string{c.key, c.name} {
		x := pNorm(n)
		if reRecNo.MatchString(x) || reFiller.MatchString(x) {
			return "ignore"
		}
	}
	for _, n := range []string{c.key, c.name} {
		if k := pKeyNames[pNorm(n)]; k != "" {
			return k
		}
	}
	return ""
}

// pNum reads a number (commas and spaces allowed); ok is false for text.
func pNum(s string) (*big.Rat, bool) {
	t := strings.NewReplacer(",", "", " ", "").Replace(strings.TrimSpace(s))
	if t == "" || strings.ContainsAny(t, "eE/") {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(t)
	return r, ok
}

func pText(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// pSame compares two values: as numbers when both are, otherwise as text.
func pSame(a, b string) bool {
	if x, ok := pNum(a); ok {
		if y, ok := pNum(b); ok {
			return x.Cmp(y) == 0
		}
	}
	return pText(a) == pText(b)
}

// pMask is a value as shown: numbers in full, client codes, demat accounts
// and other text masked.
func pMask(kind, v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "(blank)"
	}
	switch kind {
	case "client":
		return siMask(strings.ToUpper(v))
	case "demat":
		return MaskAccount(v)
	case "plain":
		return v
	}
	if _, ok := pNum(v); ok {
		return v
	}
	r := []rune(v)
	if len(r) <= 2 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:2]) + strings.Repeat("*", min(len(r)-2, 6))
}

func ratText(r *big.Rat) string {
	s := r.FloatString(4)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// parallelDiff compares two tables of one layout.
func parallelDiff(old, neu pTable) ParallelComparison {
	c := ParallelComparison{OldRows: len(old.rows), NewRows: len(neu.rows), OldIssues: old.issues, NewIssues: neu.issues, KeyColumns: []string{}, Ignored: []string{}, Columns: []ParallelColumn{}, Lines: []ParallelLine{}}
	// The columns of both files (a JSON file may have its own).
	cols := append([]pCol(nil), neu.cols...)
	seen := map[string]bool{}
	for _, x := range cols {
		seen[x.key] = true
	}
	for _, x := range old.cols {
		if !seen[x.key] {
			seen[x.key] = true
			cols = append(cols, x)
		}
	}
	kind := map[string]string{}
	var keys, cmp []pCol
	for _, x := range cols {
		k := pKind(x)
		kind[x.key] = k
		switch k {
		case "ignore":
			c.Ignored = append(c.Ignored, x.name)
		case "":
			cmp = append(cmp, x)
		default:
			keys = append(keys, x)
			c.KeyColumns = append(c.KeyColumns, x.name)
		}
	}
	keyOf := func(r map[string]string) (string, string) {
		var raw, shown []string
		if rec := r["\x00rec"]; rec != "" {
			raw, shown = append(raw, rec), append(shown, rec)
		}
		for _, k := range keys {
			v := strings.TrimSpace(r[k.key])
			if n, ok := pNum(v); ok && kind[k.key] == "plain" {
				v = ratText(n)
			}
			raw = append(raw, strings.ToUpper(v))
			if v != "" {
				shown = append(shown, pMask(kind[k.key], v))
			}
		}
		return strings.Join(raw, "|"), strings.Join(shown, " · ")
	}
	index := func(rows []map[string]string) (map[string]map[string]string, []string, map[string]string) {
		by, order, shown, count := map[string]map[string]string{}, []string{}, map[string]string{}, map[string]int{}
		for _, r := range rows {
			k, s := keyOf(r)
			count[k]++
			if count[k] > 1 { // rows with the same key pair up in order
				k = fmt.Sprintf("%s#%d", k, count[k])
				s = fmt.Sprintf("%s (#%d)", s, count[k])
			}
			by[k], shown[k] = r, s
			order = append(order, k)
		}
		return by, order, shown
	}
	ob, oorder, oshown := index(old.rows)
	nb, norder, nshown := index(neu.rows)
	// Columns: difference counts and number totals.
	pc := map[string]*ParallelColumn{}
	numeric := map[string]bool{}
	for _, x := range cols {
		if kind[x.key] == "ignore" {
			continue
		}
		col := &ParallelColumn{Name: x.name, Key: kind[x.key] != ""}
		pc[x.key] = col
		if kind[x.key] != "" {
			continue
		}
		num, any := x.typ == "int" || x.typ == "dec", false
		if x.typ == "" || x.typ == "str" {
			num = true
			for _, rows := range [][]map[string]string{old.rows, neu.rows} {
				for _, r := range rows {
					if v := strings.TrimSpace(r[x.key]); v != "" {
						any = true
						if _, ok := pNum(v); !ok {
							num = false
							break
						}
					}
				}
			}
			num = num && any
		}
		numeric[x.key] = num
		col.Numeric = num
		if num {
			tot := func(rows []map[string]string) string {
				t := new(big.Rat)
				for _, r := range rows {
					if n, ok := pNum(r[x.key]); ok {
						t.Add(t, n)
					}
				}
				return ratText(t)
			}
			col.OldTotal, col.NewTotal = tot(old.rows), tot(neu.rows)
		}
	}
	line := func(l ParallelLine) {
		if len(c.Lines) < parallelMaxLines {
			c.Lines = append(c.Lines, l)
		} else {
			c.MoreLines++
		}
	}
	for _, k := range oorder {
		o := ob[k]
		n, ok := nb[k]
		if !ok {
			c.OnlyOld++
			line(ParallelLine{Kind: "only in old", Key: oshown[k]})
			continue
		}
		differs := false
		for _, x := range cmp {
			if pSame(o[x.key], n[x.key]) {
				continue
			}
			differs = true
			pc[x.key].Diffs++
			line(ParallelLine{Kind: "differs", Key: oshown[k], Column: x.name, Old: pMask("", o[x.key]), New: pMask("", n[x.key])})
		}
		if differs {
			c.Differing++
		} else {
			c.Matched++
		}
	}
	for _, k := range norder {
		if _, ok := ob[k]; !ok {
			c.OnlyNew++
			line(ParallelLine{Kind: "only in new", Key: nshown[k]})
		}
	}
	for _, x := range cols {
		if col := pc[x.key]; col != nil {
			c.Columns = append(c.Columns, *col)
		}
	}
	c.Match = c.Differing == 0 && c.OnlyOld == 0 && c.OnlyNew == 0
	return c
}
