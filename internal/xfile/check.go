package xfile

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// ---- checking real files without showing them ----
//
// Check reads a file (zip and gzip layers removed) against the layout it
// is recognised as and summarises what it found by column and kind of
// problem, so a broker's staff can check real files on their own computer
// and share the summary: no field value is ever printed. Quoted values in
// messages are masked (letters A, digits 9), every digit in a message is
// 9, and file names keep their letters but not their digits. Counts,
// record codes, line numbers and the layout's own column names are shown.

// CheckGroup is one kind of problem in one column.
type CheckGroup struct {
	Column string `json:"column"`
	Kind   string `json:"kind"` // the message, masked
	Count  int    `json:"count"`
	Lines  []int  `json:"lines"` // the first few lines it is on
}

// CheckResult is the summary of one file.
type CheckResult struct {
	Name       string         `json:"name"` // digits masked
	Spec       string         `json:"spec,omitempty"`
	Title      string         `json:"title,omitempty"`
	Rows       int            `json:"rows"`
	Records    map[string]int `json:"records,omitempty"` // record code -> rows
	Fields     map[int]int    `json:"fields,omitempty"`  // fields on a line -> lines
	Expected   []string       `json:"expected,omitempty"`
	Header     string         `json:"header,omitempty"`
	Issues     int            `json:"issues"`
	Groups     []CheckGroup   `json:"groups,omitempty"`
	RoundTrip  string         `json:"round_trip,omitempty"`
	Recognised bool           `json:"recognised"`
	Note       string         `json:"note,omitempty"`
}

var reDigits = regexp.MustCompile(`[0-9]`)

// MaskName hides the digits of a file name (member codes, dates).
func MaskName(n string) string { return reDigits.ReplaceAllString(n, "9") }

// Check summarises a file (as found on disk: zipped or gzipped is fine).
func (l *Library) Check(name string, data []byte) []CheckResult {
	files, err := Unpack(name, data)
	if err != nil {
		return []CheckResult{{Name: MaskName(name), Note: MaskMessage(strings.TrimPrefix(err.Error(), "xfile: "))}}
	}
	var out []CheckResult
	for _, f := range files {
		out = append(out, l.check(f, name))
	}
	return out
}

func (l *Library) check(f File, outer string) CheckResult {
	r := CheckResult{Name: MaskName(f.Name)}
	s, err := l.Recognise(f.Name, f.Data)
	if err != nil && f.Name != outer {
		// a compressed file whose inner name says less than its own
		if s2, err2 := l.Recognise(strings.TrimSuffix(strings.TrimSuffix(outer, ".gz"), ".zip"), f.Data); err2 == nil {
			s, err = s2, nil
		}
	}
	if err != nil {
		r.Note = MaskMessage(strings.TrimPrefix(err.Error(), "xfile: "))
		return r
	}
	r.Recognised, r.Spec, r.Title = true, s.ID, s.Title
	t, err := s.Read(f.Name, f.Data)
	if err != nil {
		r.Note = MaskMessage(strings.TrimPrefix(err.Error(), "xfile: "))
		return r
	}
	for _, rec := range s.allRecords() {
		code := rec.Code
		if code == "" {
			code = "detail"
		}
		r.Expected = append(r.Expected, fmt.Sprintf("%s %d", code, len(rec.Columns)))
	}
	if s.Format == "delimited" {
		r.Records, r.Fields = map[string]int{}, map[int]int{}
		lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(string(bytes.TrimPrefix(f.Data, []byte("\xef\xbb\xbf"))), "\r\n", "\n"), "\r", "\n"), "\n")
		start := 0
		if s.TitleLine != "" {
			start++
		}
		if s.Header {
			start++
		}
		for i := start; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) != "" {
				r.Fields[len(splitLine(s, lines[i]))]++
			}
		}
		for _, row := range t.Rows {
			if row.Blank() {
				continue
			}
			code := row.Record
			if code == "" {
				code = "detail"
			}
			r.Records[code]++
		}
		if s.Header {
			r.Header = headerSummary(s, t.Header)
		}
	}
	for _, row := range t.Rows {
		if !row.Blank() {
			r.Rows++
		}
	}
	r.Issues = len(t.Issues)
	by := map[string]*CheckGroup{}
	var keys []string
	for _, is := range t.Issues {
		kind := MaskMessage(is.Msg)
		col := is.Column
		switch {
		case strings.Contains(is.Msg, "fields, expected") || strings.Contains(is.Msg, "no record layout"):
			col, kind = "(line)", "the number of fields does not fit the layout (see fields)"
		case strings.HasPrefix(is.Msg, "header"):
			col, kind = "(header)", "the header differs from the layout (see header)"
		}
		k := col + "\x00" + kind
		g := by[k]
		if g == nil {
			g = &CheckGroup{Column: col, Kind: kind}
			by[k] = g
			keys = append(keys, k)
		}
		g.Count++
		if len(g.Lines) < 5 {
			g.Lines = append(g.Lines, is.Line)
		}
	}
	for _, k := range keys {
		r.Groups = append(r.Groups, *by[k])
	}
	if same, diff, err := s.RoundTrip(f.Name, f.Data); err != nil {
		r.RoundTrip = "could not be written back: " + MaskMessage(err.Error())
	} else if same {
		r.RoundTrip = "written back byte for byte"
	} else {
		r.RoundTrip = "not written back byte for byte: " + MaskMessage(diff)
	}
	return r
}

// headerSummary says how a file's header row compares with the layout,
// naming only the layout's columns, never the file's.
func headerSummary(s *Spec, got []string) string {
	cols := s.detail().Columns
	switch {
	case len(got) == 0:
		return "missing"
	case s.HeaderCheck == "count":
		if s.fits(got, cols) {
			return fmt.Sprintf("%d columns, as the layout (its text is not in the circular, so it is not compared)", len(got))
		}
		return fmt.Sprintf("%d columns, the layout has %d", len(got), len(cols))
	}
	var diff []string
	for i, c := range cols {
		if i < len(got) && !c.named(got[i]) {
			diff = append(diff, fmt.Sprintf("%d (layout: %s)", i+1, c.Name))
		}
	}
	msg := fmt.Sprintf("%d columns, the layout has %d", len(got), len(cols))
	if len(got) == len(cols) {
		msg = fmt.Sprintf("%d columns, as the layout", len(got))
	}
	if len(diff) == 0 {
		return msg + "; names match"
	}
	if len(diff) > 8 {
		diff = append(diff[:8], fmt.Sprintf("and %d more", len(diff)-8))
	}
	return msg + "; names differ at column " + strings.Join(diff, ", ")
}

// WriteCheck prints a summary for people (the masked form only).
func WriteCheck(w io.Writer, r CheckResult) {
	fmt.Fprintln(w, r.Name)
	if !r.Recognised {
		fmt.Fprintf(w, "  not recognised: %s\n\n", r.Note)
		return
	}
	fmt.Fprintf(w, "  layout     %s (%s)\n", r.Spec, r.Title)
	if r.Note != "" {
		fmt.Fprintf(w, "  not read   %s\n\n", r.Note)
		return
	}
	recs := make([]string, 0, len(r.Records))
	for _, k := range sortedKeys(r.Records) {
		if k == "detail" && len(r.Records) == 1 {
			continue
		}
		recs = append(recs, fmt.Sprintf("record %s: %d", k, r.Records[k]))
	}
	rows := fmt.Sprint(r.Rows)
	if len(recs) > 0 {
		rows += " (" + strings.Join(recs, ", ") + ")"
	}
	fmt.Fprintf(w, "  rows       %s\n", rows)
	if len(r.Fields) > 0 {
		var fs []string
		var ns []int
		for n := range r.Fields {
			ns = append(ns, n)
		}
		sort.Ints(ns)
		for _, n := range ns {
			fs = append(fs, fmt.Sprintf("%d fields on %d lines", n, r.Fields[n]))
		}
		fmt.Fprintf(w, "  fields     %s (layout: %s)\n", strings.Join(fs, ", "), strings.Join(r.Expected, ", "))
	}
	if r.Header != "" {
		fmt.Fprintf(w, "  header     %s\n", r.Header)
	}
	if r.Issues == 0 {
		fmt.Fprintln(w, "  issues     none")
	} else {
		fmt.Fprintf(w, "  issues     %d\n", r.Issues)
		for _, g := range r.Groups {
			var ls []string
			for _, l := range g.Lines {
				ls = append(ls, fmt.Sprint(l))
			}
			more := ""
			if g.Count > len(g.Lines) {
				more = ", …"
			}
			fmt.Fprintf(w, "    %-28s %s: %d (lines %s%s)\n", g.Column, g.Kind, g.Count, strings.Join(ls, ", "), more)
		}
	}
	fmt.Fprintf(w, "  round trip %s\n\n", r.RoundTrip)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
