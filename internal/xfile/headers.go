package xfile

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
	"unicode"

	"ledgerline/internal/sheet"
)

// ---- layouts read by their header names ----
//
// Some lists come with no published layout: the circular says what the
// file lists (client codes whose mobile numbers are on DoT's revocation
// list, PANs the KRAs report as deceased), not its columns. Such a layout
// (format "headers") names each column by the header texts it may have:
// its Name and Aliases, compared without case, spaces or punctuation
// ("Mobile No." is "MOBILE_NO"). The header row is looked for in the
// first lines of the file (HeaderScan, 10 by default); columns are taken
// by their header in any order, and other columns are ignored; a Required
// column must be there. The delimiter may be "auto": comma, pipe, tab,
// semicolon or tilde, whichever finds the most columns. An Excel workbook
// (.xlsx or .xls) is read the same way, from the sheet with the best
// header. A file with no header row at all is read by position with
// NoHeader (column keys in order), when the first field of its first line
// fits the first of those columns' pattern.
//
// The header map is the layout's JSON in config/formats: when a real
// file's header turns out different, add its text as an alias there.

const defaultHeaderScan = 10

var autoDelims = []rune{',', '|', '\t', ';', '~'}

var bom = []byte("\xef\xbb\xbf")

func (s *Spec) compileHeaders() error {
	if len(s.Columns) == 0 {
		return fmt.Errorf("%s: a layout read by header names needs its columns", s.ID)
	}
	if len(s.Records) > 0 {
		return fmt.Errorf("%s: a layout read by header names has columns, not records", s.ID)
	}
	if s.Delimiter != "" && s.Delimiter != "auto" && len([]rune(s.Delimiter)) != 1 {
		return fmt.Errorf("%s: delimiter must be one character or auto", s.ID)
	}
	seen := map[string]bool{}
	for i := range s.Columns {
		c := &s.Columns[i]
		if c.Key == "" {
			c.Key = c.Name
		}
		if !validTypes[c.Type] {
			return fmt.Errorf("%s: column %s: unknown type %q", s.ID, c.Name, c.Type)
		}
		if (c.Type == "date" || c.Type == "datetime" || c.Type == "time") && c.Layout == "" {
			return fmt.Errorf("%s: column %s needs a layout", s.ID, c.Name)
		}
		if seen[c.Key] {
			return fmt.Errorf("%s: duplicate column key %s", s.ID, c.Key)
		}
		seen[c.Key] = true
		if normHeader(c.Name) == "" {
			return fmt.Errorf("%s: column %q has no letters or digits to find it by", s.ID, c.Name)
		}
	}
	for i, k := range s.NoHeader {
		if !seen[k] {
			return fmt.Errorf("%s: no_header names %q, which is not a column", s.ID, k)
		}
		if i == 0 && s.column(k).Pattern == "" {
			return fmt.Errorf("%s: the first no_header column needs a pattern", s.ID)
		}
	}
	return nil
}

// column returns a column by key.
func (s *Spec) column(key string) Column {
	for _, c := range s.Columns {
		if c.Key == key {
			return c
		}
	}
	return Column{}
}

// normHeader is a header text as compared: lower case, letters and digits.
func normHeader(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimPrefix(strings.TrimSpace(h), "\ufeff")) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// headerIndex finds the layout's columns in a header row: each column's
// position by key, how many were found, and whether every required one is
// there.
func (s *Spec) headerIndex(fields []string) (map[string]int, int, bool) {
	norm := make([]string, len(fields))
	for i, f := range fields {
		norm[i] = normHeader(f)
	}
	used := make([]bool, len(fields))
	idx := map[string]int{}
	for _, c := range s.Columns {
		for _, n := range append([]string{c.Name}, c.Aliases...) {
			want, at := normHeader(n), -1
			for i, f := range norm {
				if !used[i] && want != "" && f == want {
					at = i
					break
				}
			}
			if at >= 0 {
				idx[c.Key], used[at] = at, true
				break
			}
		}
	}
	ok := len(idx) > 0
	for _, c := range s.Columns {
		if _, has := idx[c.Key]; c.Required && !has {
			ok = false
		}
	}
	return idx, len(idx), ok
}

// headerFound is where a file's header row is and what it has.
type headerFound struct {
	line   int // index of the line (or sheet row)
	delim  rune
	idx    map[string]int
	found  int
	fields []string
}

func (s *Spec) scanLimit() int {
	if s.HeaderScan > 0 {
		return s.HeaderScan
	}
	return defaultHeaderScan
}

func (s *Spec) delims() []rune {
	if s.Delimiter == "" || s.Delimiter == "auto" {
		return autoDelims
	}
	return []rune{[]rune(s.Delimiter)[0]}
}

func splitWith(line string, d rune) []string {
	r := csv.NewReader(strings.NewReader(line))
	r.Comma, r.LazyQuotes, r.FieldsPerRecord = d, true, -1
	rec, err := r.Read()
	if err != nil {
		return strings.Split(line, string(d))
	}
	return rec
}

func splitLines(data []byte) []string {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n"), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// headerInLines finds the best header row among a text file's first lines.
func (s *Spec) headerInLines(lines []string) (headerFound, bool) {
	var best headerFound
	ok, seen := false, 0
	for i := 0; i < len(lines) && seen < s.scanLimit(); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		seen++
		for _, d := range s.delims() {
			f := splitWith(lines[i], d)
			if idx, found, good := s.headerIndex(f); good && found > best.found {
				best, ok = headerFound{line: i, delim: d, idx: idx, found: found, fields: f}, true
			}
		}
	}
	return best, ok
}

// headerInRows finds the best header row among a sheet's first rows.
func (s *Spec) headerInRows(rows [][]string) (headerFound, bool) {
	var best headerFound
	ok, seen := false, 0
	for i := 0; i < len(rows) && seen < s.scanLimit(); i++ {
		if strings.TrimSpace(strings.Join(rows[i], "")) == "" {
			continue
		}
		seen++
		if idx, found, good := s.headerIndex(rows[i]); good && found > best.found {
			best, ok = headerFound{line: i, idx: idx, found: found, fields: rows[i]}, true
		}
	}
	return best, ok
}

// noHeaderDelim says whether a text file with no header row can be read
// by NoHeader: its first line's first field fits the first column, split
// by the delimiter giving the most fields (at most NoHeader's).
func (s *Spec) noHeaderDelim(lines []string) (rune, bool) {
	if len(s.NoHeader) == 0 {
		return 0, false
	}
	first := s.column(s.NoHeader[0])
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var best rune
		most := 0
		for _, d := range s.delims() {
			f := splitWith(l, d)
			if len(f) > most && len(f) <= len(s.NoHeader) && first.rx != nil && first.rx.MatchString(strings.TrimSpace(f[0])) {
				best, most = d, len(f)
			}
		}
		return best, most > 0
	}
	return 0, false
}

func (s *Spec) noHeaderIssue() Issue {
	var need []string
	for _, c := range s.Columns {
		if c.Required {
			need = append(need, c.Name)
		}
	}
	return Issue{Line: 1, Msg: fmt.Sprintf("no header row with the layout's columns (%s) in the first %d lines; add the file's header texts to the layout %s as aliases", strings.Join(need, ", "), s.scanLimit(), s.ID)}
}

// noteLine says whether a line is a note under or above the table (a
// total, the report's date): a single filled field with spaces or a colon
// in it, in a table of more than one column.
func noteLine(f []string, cols int) bool {
	if cols < 2 {
		return false
	}
	filled, text := 0, ""
	for _, v := range f {
		if strings.TrimSpace(v) != "" {
			filled++
			text = v
		}
	}
	return filled == 1 && strings.ContainsAny(strings.TrimSpace(text), " :")
}

// emitRow checks a data line and hands it on; a line with nothing in the
// layout's columns is left out.
func (s *Spec) emitRow(line int, f []string, idx map[string]int, t *Table, keep func(Row)) {
	r := Row{Line: line, Values: map[string]string{}}
	filled := false
	for _, c := range s.Columns {
		v := ""
		if i, ok := idx[c.Key]; ok && i < len(f) {
			v = strings.TrimSpace(f[i])
		}
		r.Values[c.Key] = v
		filled = filled || v != ""
	}
	if !filled {
		return
	}
	for _, c := range s.Columns {
		if _, ok := idx[c.Key]; !ok {
			continue
		}
		if msg := c.check(r.Values[c.Key]); msg != "" {
			t.Issues = append(t.Issues, Issue{Line: line, Column: c.Name, Msg: msg})
		}
	}
	keep(r)
}

// Scan reads a file in a layout read by its header names. Each data row
// goes to each (or, with each nil, into t.Rows), so a long list can be
// read without keeping it; the header, issues and line endings go in t.
func (s *Spec) Scan(data []byte, t *Table, each func(Row)) error {
	if s.Format != "headers" {
		return fmt.Errorf("xfile: %s is not read by its header names", s.ID)
	}
	keep := func(r Row) {
		if each != nil {
			each(r)
			return
		}
		t.Rows = append(t.Rows, r)
	}
	if sheet.IsWorkbook(data) {
		books, err := sheet.Read(data)
		if err != nil {
			return fmt.Errorf("xfile: %s: %w", t.Name, err)
		}
		var best headerFound
		at := -1
		for i, sh := range books {
			if h, ok := s.headerInRows(sh.Rows); ok && h.found > best.found {
				best, at = h, i
			}
		}
		if at < 0 {
			t.Issues = append(t.Issues, s.noHeaderIssue())
			return nil
		}
		t.Header = best.fields
		rows := books[at].Rows
		for i := best.line + 1; i < len(rows); i++ {
			if !noteLine(rows[i], len(best.fields)) {
				s.emitRow(i+1, rows[i], best.idx, t, keep)
			}
		}
		return nil
	}
	data = bytes.TrimPrefix(data, bom)
	t.CRLF = bytes.Contains(data, []byte("\r\n"))
	lines := splitLines(data)
	if h, ok := s.headerInLines(lines); ok {
		t.Header = h.fields
		for i := h.line + 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "" {
				continue
			}
			if f := splitWith(lines[i], h.delim); !noteLine(f, len(h.fields)) {
				s.emitRow(i+1, f, h.idx, t, keep)
			}
		}
		return nil
	}
	if d, ok := s.noHeaderDelim(lines); ok {
		idx := map[string]int{}
		for i, k := range s.NoHeader {
			idx[k] = i
		}
		for i, l := range lines {
			if strings.TrimSpace(l) != "" {
				s.emitRow(i+1, splitWith(l, d), idx, t, keep)
			}
		}
		return nil
	}
	t.Issues = append(t.Issues, s.noHeaderIssue())
	return nil
}

// HeaderFound says whether a file has a header row the layout can read
// (or, with NoHeader, lines it can read by position); for a text file only
// its first megabyte is looked at.
func (s *Spec) HeaderFound(data []byte) bool {
	if s.Format != "headers" {
		return false
	}
	if !sheet.IsWorkbook(data) && len(data) > 1<<20 {
		data = data[:1<<20]
		if i := bytes.LastIndexByte(data, '\n'); i > 0 {
			data = data[:i]
		}
	}
	return s.headerScore(data) > 0
}

// NoHeaderMessage says what a file without the layout's header row lacks.
func (s *Spec) NoHeaderMessage() string { return s.noHeaderIssue().Msg }

// headerScore is how many of the layout's columns a file's header row has
// (0: no such row, or a required column is missing); a file with no
// header row that NoHeader reads scores 1.
func (s *Spec) headerScore(data []byte) int {
	if sheet.IsWorkbook(data) {
		books, err := sheet.Read(data)
		if err != nil {
			return 0
		}
		best := 0
		for _, sh := range books {
			if h, ok := s.headerInRows(sh.Rows); ok && h.found > best {
				best = h.found
			}
		}
		return best
	}
	lines := splitLines(bytes.TrimPrefix(data, bom))
	if h, ok := s.headerInLines(lines); ok {
		return h.found
	}
	if _, ok := s.noHeaderDelim(lines); ok {
		return 1
	}
	return 0
}

// bestByHeaders decides between layouts read by header names whose file
// name patterns all match: the one whose columns the header row has most
// of. A header alone never recognises such a layout (a "Mobile" column
// says too little), so they are not tried for names that match none. ok
// is false when none of byName is such a layout, or none fits and other
// layouts in byName can still decide by their own header.
func bestByHeaders(name string, byName []*Spec, data []byte) (*Spec, bool, error) {
	var cands []*Spec
	for _, s := range byName {
		if s.Format == "headers" {
			cands = append(cands, s)
		}
	}
	if len(cands) == 0 {
		return nil, false, nil
	}
	var best []*Spec
	top := 0
	for _, s := range cands {
		switch n := s.headerScore(data); {
		case n > top:
			best, top = []*Spec{s}, n
		case n == top && n > 0:
			best = append(best, s)
		}
	}
	ids := func(ss []*Spec) string {
		var out []string
		for _, s := range ss {
			out = append(out, s.ID)
		}
		return strings.Join(out, ", ")
	}
	switch {
	case len(best) == 1:
		return best[0], true, nil
	case len(best) > 1:
		return nil, true, fmt.Errorf("xfile: %s could be any of %s; load it on the screen of its list, choosing the list", name, ids(best))
	case len(cands) == len(byName):
		return nil, true, fmt.Errorf("xfile: %s is named like %s, but its header row has none of their columns", name, ids(cands))
	}
	return nil, false, nil
}
