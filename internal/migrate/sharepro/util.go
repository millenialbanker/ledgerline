package sharepro

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
)

// ---- reading values ----
//
// The Querier gives lower-case column names; decimals arrive as exact
// strings, char columns trimmed.

func str(r migrate.Row, col string) string {
	switch v := r[col].(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "1"
		}
		return "0"
	case time.Time:
		return v.Format("2006-01-02")
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func upper(r migrate.Row, col string) string { return strings.ToUpper(str(r, col)) }

// dec reads a number; empty or bad values are zero.
func dec(r migrate.Row, col string) money.Dec {
	switch v := r[col].(type) {
	case int64:
		return money.DecInt(v)
	case float64:
		d, err := money.ParseDec(strconv.FormatFloat(v, 'f', -1, 64))
		if err == nil {
			return d
		}
		return money.Dec{}
	}
	s := str(r, col)
	if s == "" {
		return money.Dec{}
	}
	d, err := money.ParseDec(s)
	if err != nil {
		return money.Dec{}
	}
	return d
}

func i64(r migrate.Row, col string) int64 {
	switch v := r[col].(type) {
	case int64:
		return v
	case bool:
		if v {
			return 1
		}
		return 0
	}
	d := dec(r, col)
	a := d.Round(1)
	return a.Paise() / 100
}

// flag reads a bit or Y/N column.
func flag(r migrate.Row, col string) bool {
	switch v := r[col].(type) {
	case bool:
		return v
	case int64:
		return v != 0
	case float64:
		return v != 0
	}
	switch strings.ToUpper(str(r, col)) {
	case "1", "Y", "YES", "TRUE", "T":
		return true
	}
	return false
}

// minDate: SharePro writes 1900-01-01 (and sometimes 1899-12-30) for "no date".
var minDate = time.Date(1901, 1, 1, 0, 0, 0, 0, time.UTC)

func tm(r migrate.Row, col string) time.Time {
	switch v := r[col].(type) {
	case time.Time:
		if v.Before(minDate) {
			return time.Time{}
		}
		return v
	case string:
		s := strings.TrimSpace(v)
		for _, f := range []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05", "20060102"} {
			if t, err := time.Parse(f, s); err == nil {
				if t.Before(minDate) {
					return time.Time{}
				}
				return t
			}
		}
	}
	return time.Time{}
}

func day(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02")
}

func date(r migrate.Row, col string) string { return day(tm(r, col)) }

// rupees renders an amount with two decimals; zero is "".
func rupees(d money.Dec) string {
	if d.IsZero() {
		return ""
	}
	return d.Fixed(2)
}

// rupees0 renders an amount with two decimals, zero as "0.00".
func rupees0(d money.Dec) string { return d.Fixed(2) }

// num renders an exact number without trailing zeros; zero is "".
func num(d money.Dec) string {
	if d.IsZero() {
		return ""
	}
	return d.String()
}

func qty(d money.Dec) string {
	return d.String()
}

// ---- financial years ----

// fyOf is the Indian financial year of a date, e.g. "2026-27".
func fyOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	y := t.Year()
	if t.Month() < time.April {
		y--
	}
	return fmt.Sprintf("%d-%02d", y, (y+1)%100)
}

func fyStart(fy string) time.Time {
	y, err := strconv.Atoi(strings.SplitN(fy, "-", 2)[0])
	if err != nil {
		return time.Time{}
	}
	return time.Date(y, time.April, 1, 0, 0, 0, 0, time.UTC)
}

func fyEnd(fy string) time.Time {
	s := fyStart(fy)
	if s.IsZero() {
		return s
	}
	return s.AddDate(1, 0, -1)
}

// fyFromCode reads SharePro's year code "2627" (or "0304").
func fyFromCode(code string) string {
	code = strings.TrimSpace(code)
	if len(code) != 4 {
		return ""
	}
	a, err1 := strconv.Atoi(code[:2])
	b, err2 := strconv.Atoi(code[2:])
	if err1 != nil || err2 != nil || (a+1)%100 != b {
		return ""
	}
	y := 2000 + a
	if a >= 90 {
		y = 1900 + a
	}
	return fmt.Sprintf("%d-%02d", y, b)
}

var reYearInName = regexp.MustCompile(`[A-Za-z_](\d\d)(\d\d)`)

// fyFromName finds <yy><yy+1> after a letter in a database name: AB2627,
// SP_AB2627202601010900 and XYZ2526 give the year; SSMAIL2026... does not.
func fyFromName(name string) string {
	for _, m := range reYearInName.FindAllStringSubmatchIndex(name, -1) {
		if fy := fyFromCode(name[m[2]:m[5]]); fy != "" {
			return fy
		}
	}
	return ""
}

// yearsBack says whether fy is within n years ending with newest.
func yearsBack(fy, newest string, n int) bool {
	if n <= 0 {
		n = 8
	}
	a, b := fyStart(fy), fyStart(newest)
	if a.IsZero() || b.IsZero() {
		return true
	}
	d := b.Year() - a.Year()
	return d >= 0 && d < n
}

// ---- account codes ----

var reCodeBad = regexp.MustCompile(`[^A-Z0-9_-]+`)

func codePart(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = reCodeBad.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		s = "X"
	}
	return s
}

// ---- issues ----

// report buffers issues, dropped fields and checks: the package Writer is
// locked while a file is open, so they are added once the files are closed.
type report struct {
	firm, fy string
	issues   []pkg.Issue
	dropped  []pkg.Dropped
	checks   []pkg.Check
	counts   map[string]int
	extra    map[string]int
}

const issueCap = 200 // per area and message kind; the rest are counted

func newReport(firm, fy string) *report {
	return &report{firm: firm, fy: fy, counts: map[string]int{}, extra: map[string]int{}}
}

// add records an issue. kind groups similar issues for the cap.
func (r *report) add(sev, area, key, kind, msg, action string) {
	k := area + "|" + kind
	r.counts[k]++
	if r.counts[k] > issueCap {
		r.extra[k]++
		return
	}
	r.issues = append(r.issues, pkg.Issue{Firm: r.firm, FY: r.fy, Severity: sev, Area: area, Key: key, Message: msg, Action: action})
}

func (r *report) errorf(area, key, kind, action, f string, a ...any) {
	r.add("Error", area, key, kind, fmt.Sprintf(f, a...), action)
}
func (r *report) warnf(area, key, kind, action, f string, a ...any) {
	r.add("Warning", area, key, kind, fmt.Sprintf(f, a...), action)
}
func (r *report) infof(area, key, kind, f string, a ...any) {
	r.add("Info", area, key, kind, fmt.Sprintf(f, a...), "")
}

func (r *report) drop(field, reason string, n int) {
	if n <= 0 {
		return
	}
	r.dropped = append(r.dropped, pkg.Dropped{Firm: r.firm, Field: field, Reason: reason, Count: n})
}

func (r *report) check(name, value, detail string) {
	r.checks = append(r.checks, pkg.Check{Firm: r.firm, FY: r.fy, Name: name, Value: value, Detail: detail})
}

// flush adds everything to the package. Call with no file open.
func (r *report) flush(w *pkg.Writer, p migrate.Progress) {
	keys := make([]string, 0, len(r.extra))
	for k := range r.extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		area, _, _ := strings.Cut(k, "|")
		var sev, msg string
		for _, i := range r.issues {
			if i.Area == area {
				sev, msg = i.Severity, i.Message
			}
		}
		_ = msg
		if sev == "" {
			sev = "Warning"
		}
		r.issues = append(r.issues, pkg.Issue{Firm: r.firm, FY: r.fy, Severity: sev, Area: area, Message: fmt.Sprintf("%d more like the above (not listed one by one)", r.extra[k]), Action: "See the old system's exception report for the full list"})
	}
	for _, i := range r.issues {
		w.AddIssue(i)
		if p != nil && i.Severity == "Error" {
			p.Log(i.Severity, i.Area+": "+i.Message)
		}
	}
	for _, d := range r.dropped {
		w.AddDropped(d.Firm, d.Field, d.Reason, d.Count)
	}
	for _, c := range r.checks {
		w.AddCheck(c)
	}
	r.issues, r.dropped, r.checks = nil, nil, nil
	r.counts, r.extra = map[string]int{}, map[string]int{}
}

// ---- query safety ----

// Columns that are never read: client and staff passwords, Aadhaar numbers,
// system log file contents. They may appear only inside COUNT(...) (to
// count them) or RIGHT(...) (the last 4 digits).
var forbiddenCol = regexp.MustCompile(`(?i)\b([A-Z0-9_]*PWD[A-Z0-9_]*|[A-Z0-9_]*PASSWORD[A-Z0-9_]*|[A-Z0-9_]*A{1,2}DHA{1,2}R[A-Z0-9_]*|[A-Z0-9_]*UID[0-9]*|CONTENT|[A-Z0-9_]*TOKEN[A-Z0-9_]*|[A-Z0-9_]*OTP[A-Z0-9_]*)\b`)

// stripAllowed removes COUNT(...) and RIGHT(...) calls, with nested
// brackets, from a query.
func stripAllowed(q string) string {
	up := strings.ToUpper(q)
	var b strings.Builder
	for i := 0; i < len(q); {
		matched := false
		for _, fn := range []string{"COUNT(", "RIGHT("} {
			if strings.HasPrefix(up[i:], fn) && (i == 0 || !isIdent(up[i-1])) {
				depth, j := 0, i+len(fn)-1
				for ; j < len(q); j++ {
					if q[j] == '(' {
						depth++
					} else if q[j] == ')' {
						depth--
						if depth == 0 {
							break
						}
					}
				}
				b.WriteString(" ")
				i = j + 1
				matched = true
				break
			}
		}
		if !matched {
			b.WriteByte(q[i])
			i++
		}
	}
	return b.String()
}

func isIdent(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

var reLiteral = regexp.MustCompile(`'(?:[^']|'')*'`)

// unsafeColumn returns a forbidden column the query reads, or "". String
// literals (LIKE '%AADHAAR%') are not columns.
func unsafeColumn(q string) string {
	return forbiddenCol.FindString(readPart(q))
}

// readPart is the query without COUNT(...), RIGHT(...) and string literals:
// what it can read.
func readPart(q string) string {
	return reLiteral.ReplaceAllString(stripAllowed(q), "''")
}

// db wraps a Querier: every query is checked for forbidden columns first.
type db struct {
	q migrate.Querier
}

func (d db) query(ctx context.Context, q string, fn func(migrate.Row) error, args ...any) error {
	if c := unsafeColumn(q); c != "" {
		return fmt.Errorf("sharepro: refusing a query that reads %s", c)
	}
	return d.q.Query(ctx, q, fn, args...)
}

// one runs a query expected to return one row (nil if none).
func (d db) one(ctx context.Context, q string, args ...any) (migrate.Row, error) {
	var out migrate.Row
	err := d.query(ctx, q, func(r migrate.Row) error {
		if out == nil {
			out = r
		}
		return nil
	}, args...)
	return out, err
}

// ---- schema probe ----

// schema is the set of tables and columns of one database, loaded once.
type schema struct {
	cols  map[string]map[string]bool // upper-case table -> upper-case columns
	types map[string]string          // TABLE.COLUMN -> data type (lower case)
}

const qSchema = `/*sp:schema*/ SELECT TABLE_NAME AS t, COLUMN_NAME AS c, DATA_TYPE AS ty FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = 'dbo'`

func loadSchema(ctx context.Context, d db) (*schema, error) {
	s := &schema{cols: map[string]map[string]bool{}, types: map[string]string{}}
	err := d.query(ctx, qSchema, func(r migrate.Row) error {
		t, c := upper(r, "t"), upper(r, "c")
		if s.cols[t] == nil {
			s.cols[t] = map[string]bool{}
		}
		s.cols[t][c] = true
		s.types[t+"."+c] = strings.ToLower(str(r, "ty"))
		return nil
	})
	return s, err
}

func (s *schema) has(table string) bool { return s.cols[strings.ToUpper(table)] != nil }

func (s *schema) hasCol(table, col string) bool {
	return s.cols[strings.ToUpper(table)][strings.ToUpper(col)]
}

// sel builds a select list for table (with alias a, "" for none): columns
// that exist are selected, missing ones come as NULL so older databases
// still work. missing lists what was not there.
func (s *schema) sel(table, a string, cols ...string) (list string, missing []string) {
	var parts []string
	pre := ""
	if a != "" {
		pre = a + "."
	}
	for _, c := range cols {
		name, expr := c, ""
		if n, e, ok := strings.Cut(c, "="); ok { // "alias=expression" for computed columns
			name, expr = n, e
		}
		// aliases are bracketed: SharePro has columns named like reserved
		// words (PERCENT, KEY, to, read, user, FILE, OF)
		if expr != "" {
			parts = append(parts, expr+" AS ["+name+"]")
			continue
		}
		if s.hasCol(table, c) {
			parts = append(parts, pre+"["+c+"] AS ["+strings.ToLower(c)+"]")
		} else {
			parts = append(parts, "NULL AS ["+strings.ToLower(c)+"]")
			missing = append(missing, table+"."+c)
		}
	}
	return strings.Join(parts, ", "), missing
}

// order is an ORDER BY list of the columns that exist (with alias a).
func (s *schema) order(table, a string, cols ...string) string {
	var out []string
	for _, c := range cols {
		if s.hasCol(table, c) {
			if a != "" {
				out = append(out, a+"."+c)
			} else {
				out = append(out, c)
			}
		}
	}
	if len(out) == 0 {
		return "1"
	}
	return strings.Join(out, ", ")
}

// bytesOf is an expression giving a column's bytes as stored. SharePro
// keeps files in text columns; reading text as text would pass the bytes
// through the code page, so it is cast to varchar(max) (same bytes) and
// then to varbinary(max).
func (s *schema) bytesOf(table, expr, col string) string {
	switch s.types[strings.ToUpper(table)+"."+strings.ToUpper(col)] {
	case "image", "varbinary", "binary":
		return "CAST(" + expr + " AS varbinary(max))"
	case "ntext", "nvarchar":
		return "CAST(CAST(" + expr + " AS nvarchar(max)) AS varbinary(max))"
	}
	return "CAST(CAST(" + expr + " AS varchar(max)) AS varbinary(max))"
}

// inList renders a SQL IN list of string literals ('02','03').
func inList(vals []string) string {
	if len(vals) == 0 {
		return "''"
	}
	q := make([]string, len(vals))
	for i, v := range vals {
		q[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	return strings.Join(q, ",")
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

func yn(b bool) string {
	if b {
		return "Y"
	}
	return "N"
}

func titleCase(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
