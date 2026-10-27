// Package xfile reads and writes the files a stock broker exchanges with
// the exchanges, clearing corporations, depositories, the trading front end
// and the bank: trade files, margin and obligation files, depository
// statements, collateral allocation, regulatory reports, limit files and
// payout files. Each layout is a spec (config/formats/*.json), so a format
// change in a circular is a spec edit, not new code.
//
// A spec gives the file's name pattern, compression, delimiter, whether it
// has a header row, its line ending and its columns (name, type, decimals,
// date layout). Read checks a file against its spec (exact header names,
// field counts, types) and returns the rows; Write produces a file from
// rows, byte-for-byte in the spec's layout.
package xfile

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Column is one field of a record.
type Column struct {
	Name     string `json:"name"`               // the header text, or the field's name when the file has no header
	Key      string `json:"key,omitempty"`      // the name Ledgerline uses for it (defaults to Name)
	Type     string `json:"type"`               // str, int, dec, date, datetime, time, blank
	Layout   string `json:"layout,omitempty"`   // date/time layout (Go reference time)
	Decimals int    `json:"decimals,omitempty"` // for dec: digits after the point
	Fixed    bool   `json:"fixed,omitempty"`    // for dec: always write all decimals
	Const    string `json:"const,omitempty"`    // a value the field always has (record codes)
	Quote    string `json:"quote,omitempty"`    // "always": in double quotes even when empty; "set": quoted when it has a value
	Required bool   `json:"required,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Record is one kind of line in a file with several (a header record,
// detail records, a trailer).
type Record struct {
	Code    string   `json:"code"`            // value of the first field that identifies it ("" = detail)
	Where   string   `json:"where,omitempty"` // first, last or any (default any)
	Columns []Column `json:"columns"`
}

// Spec is one file layout.
type Spec struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Party       string   `json:"party"`     // NSE Clearing, MCXCCL, CDSL, NSE, BSE, Front end, Bank
	Direction   string   `json:"direction"` // in (received) or out (sent)
	When        string   `json:"when"`      // BOD, Intraday, EOD, On request
	Purpose     string   `json:"purpose"`
	Names       []string `json:"names"`    // regular expressions matching the file name (as received or sent)
	Template    string   `json:"template"` // name of a file Ledgerline writes: {member}, {member_pan}, {date:02012006}, {seq:2}
	Compress    string   `json:"compress"` // none, gzip, zip
	Format      string   `json:"format"`   // delimited, json
	Delimiter   string   `json:"delimiter"`
	Header      bool     `json:"header"`
	TitleLine   string   `json:"title_line,omitempty"` // a fixed first line before the records (e.g. "RMS Limits")
	CRLF        bool     `json:"crlf"`
	CROnly      bool     `json:"cr_only,omitempty"` // lines end in CR alone (CDSL upload files)
	FinalEOL    bool     `json:"final_eol"`
	TextTrailer bool     `json:"text_trailer,omitempty"` // the last line is free text (a total or a note)
	HeaderLine  string   `json:"header_line,omitempty"`  // the header exactly as the file has it, when it is not plain CSV (a comma inside a name)
	Columns     []Column `json:"columns,omitempty"`
	Records     []Record `json:"records,omitempty"`
	JSONShape   any      `json:"json_shape,omitempty"`
	Confirmed   bool     `json:"confirmed"` // every column's meaning is from the published spec or the file's own header
	Linked      string   `json:"linked"`    // what Ledgerline does with it ("" = layout known, not used yet)
	Source      string   `json:"source,omitempty"`
	nameRx      []*regexp.Regexp
}

// Library is the set of known layouts.
type Library struct {
	Specs []*Spec
	byID  map[string]*Spec
}

// Load reads every *.json spec in dir.
func Load(fsys fs.FS, dir string) (*Library, error) {
	ents, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	lib := &Library{byID: map[string]*Spec{}}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var s Spec
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("xfile: %s: %w", e.Name(), err)
		}
		if err := s.compile(); err != nil {
			return nil, fmt.Errorf("xfile: %s: %w", e.Name(), err)
		}
		if lib.byID[s.ID] != nil {
			return nil, fmt.Errorf("xfile: %s: duplicate id %s", e.Name(), s.ID)
		}
		lib.byID[s.ID] = &s
		lib.Specs = append(lib.Specs, &s)
	}
	sort.Slice(lib.Specs, func(i, j int) bool { return lib.Specs[i].ID < lib.Specs[j].ID })
	return lib, nil
}

// Get returns a spec by id.
func (l *Library) Get(id string) (*Spec, bool) { s, ok := l.byID[id]; return s, ok }

var validTypes = map[string]bool{"str": true, "int": true, "dec": true, "date": true, "datetime": true, "time": true, "blank": true}

func (s *Spec) compile() error {
	if s.ID == "" || s.Title == "" {
		return errors.New("spec needs id and title")
	}
	if s.Direction != "in" && s.Direction != "out" {
		return fmt.Errorf("%s: direction must be in or out", s.ID)
	}
	switch s.Compress {
	case "", "none", "gzip", "zip":
	default:
		return fmt.Errorf("%s: compress must be none, gzip or zip", s.ID)
	}
	if s.Format == "" {
		s.Format = "delimited"
	}
	if s.Format == "delimited" {
		if len([]rune(s.Delimiter)) != 1 {
			return fmt.Errorf("%s: delimiter must be one character", s.ID)
		}
		if len(s.Columns) == 0 && len(s.Records) == 0 {
			return fmt.Errorf("%s: no columns", s.ID)
		}
		if len(s.Columns) > 0 && len(s.Records) > 0 {
			return fmt.Errorf("%s: give columns or records, not both", s.ID)
		}
		if s.Header && len(s.Records) > 0 {
			return fmt.Errorf("%s: a header row needs a single record layout", s.ID)
		}
		for _, r := range s.allRecords() {
			seen := map[string]bool{}
			for i := range r.Columns {
				c := &r.Columns[i]
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
			}
		}
	} else if s.Format != "json" {
		return fmt.Errorf("%s: format must be delimited or json", s.ID)
	}
	for _, n := range s.Names {
		rx, err := regexp.Compile("(?i)^(?:" + n + ")$")
		if err != nil {
			return fmt.Errorf("%s: name pattern %q: %w", s.ID, n, err)
		}
		s.nameRx = append(s.nameRx, rx)
	}
	return nil
}

func (s *Spec) allRecords() []*Record {
	if len(s.Records) > 0 {
		out := make([]*Record, len(s.Records))
		for i := range s.Records {
			out[i] = &s.Records[i]
		}
		return out
	}
	return []*Record{{Columns: s.Columns}}
}

// detail is the detail record layout (the only one, or the one without a code).
func (s *Spec) detail() *Record {
	rs := s.allRecords()
	if len(s.Records) == 0 {
		rs[0].Columns = s.Columns
		return rs[0]
	}
	for _, r := range rs {
		if r.Code == "" {
			return r
		}
	}
	return rs[len(rs)-1]
}

// MatchesName reports whether a file name fits the spec.
func (s *Spec) MatchesName(name string) bool {
	base := path.Base(strings.ReplaceAll(name, "\\", "/"))
	for _, rx := range s.nameRx {
		if rx.MatchString(base) {
			return true
		}
	}
	return false
}

// ---- unpacking ----

// File is one file after removing zip and gzip layers.
type File struct {
	Name string
	Data []byte
}

// MaxUnpacked caps what an archive may expand to.
const MaxUnpacked = 200 << 20

// Unpack removes zip and gzip layers (also nested), returning the files inside.
func Unpack(name string, data []byte) ([]File, error) {
	return unpack(name, data, 0)
}

func unpack(name string, data []byte, depth int) ([]File, error) {
	if depth > 3 {
		return []File{{name, data}}, nil
	}
	switch {
	case len(data) > 4 && bytes.HasPrefix(data, []byte("PK\x03\x04")) && !isOOXML(data):
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("xfile: %s: %w", name, err)
		}
		var out []File
		var total int64
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			b, err := io.ReadAll(io.LimitReader(rc, MaxUnpacked-total+1))
			rc.Close()
			if err != nil {
				return nil, err
			}
			total += int64(len(b))
			if total > MaxUnpacked {
				return nil, fmt.Errorf("xfile: %s expands to more than %d MB", name, MaxUnpacked>>20)
			}
			inner, err := unpack(path.Base(f.Name), b, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
		}
		return out, nil
	case len(data) > 2 && data[0] == 0x1f && data[1] == 0x8b:
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("xfile: %s: %w", name, err)
		}
		b, err := io.ReadAll(io.LimitReader(zr, MaxUnpacked+1))
		if err != nil {
			return nil, fmt.Errorf("xfile: %s: %w", name, err)
		}
		if len(b) > MaxUnpacked {
			return nil, fmt.Errorf("xfile: %s expands to more than %d MB", name, MaxUnpacked>>20)
		}
		inner := strings.TrimSuffix(strings.TrimSuffix(name, ".gz"), ".GZ")
		if zr.Name != "" {
			inner = path.Base(zr.Name)
		}
		return unpack(inner, b, depth+1)
	}
	return []File{{name, data}}, nil
}

// isOOXML says whether a zip is an Excel/Word file rather than an archive.
func isOOXML(data []byte) bool {
	return bytes.Contains(data[:min(len(data), 4096)], []byte("[Content_Types].xml"))
}

// ---- recognising ----

// Recognise finds the spec for a file: by name first, then by header row.
func (l *Library) Recognise(name string, data []byte) (*Spec, error) {
	var byName []*Spec
	for _, s := range l.Specs {
		if s.MatchesName(name) {
			byName = append(byName, s)
		}
	}
	if len(byName) == 1 {
		return byName[0], nil
	}
	// Several layouts share a name (NSE Clearing's <member>.txt files) or
	// none matched: decide by the header row.
	first := firstLine(data)
	var cands []*Spec
	pool := byName
	if len(pool) == 0 {
		pool = l.Specs
	}
	for _, s := range pool {
		if s.Format == "delimited" && s.Header && headerMatches(s, first) {
			cands = append(cands, s)
		}
	}
	if len(cands) == 1 {
		return cands[0], nil
	}
	if len(cands) > 1 {
		ids := make([]string, len(cands))
		for i, c := range cands {
			ids[i] = c.ID
		}
		return nil, fmt.Errorf("xfile: %s could be any of %s", name, strings.Join(ids, ", "))
	}
	if len(byName) > 1 {
		ids := make([]string, len(byName))
		for i, c := range byName {
			ids[i] = c.ID
		}
		return nil, fmt.Errorf("xfile: %s matches %s; its header row does not say which", name, strings.Join(ids, ", "))
	}
	return nil, fmt.Errorf("xfile: %s is not a file layout Ledgerline knows", name)
}

func firstLine(b []byte) string {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if i := bytes.IndexAny(b, "\r\n"); i >= 0 {
		b = b[:i]
	}
	return strings.TrimRight(string(b), "\r")
}

func headerMatches(s *Spec, line string) bool {
	if s.HeaderLine != "" {
		return strings.EqualFold(strings.TrimSpace(line), s.HeaderLine)
	}
	f := splitLine(s, line)
	cols := s.detail().Columns
	if len(f) != len(cols) {
		return false
	}
	for i, c := range cols {
		if !strings.EqualFold(strings.TrimSpace(f[i]), c.Name) {
			return false
		}
	}
	return true
}

func splitLine(s *Spec, line string) []string {
	r := csv.NewReader(strings.NewReader(line))
	r.Comma = []rune(s.Delimiter)[0]
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	rec, err := r.Read()
	if err != nil {
		return strings.Split(line, s.Delimiter)
	}
	return rec
}

// ---- reading ----

// blankRecord marks an empty line kept in place (report-style files).
const blankRecord = "(blank)"

// textRecord marks a free-text line kept in place (a trailer).
const textRecord = "(text)"

// Row is one record's values by column key, with the record code and line.
type Row struct {
	Line   int
	Record string
	Values map[string]string
}

// Blank reports whether the row is a kept empty line.
func (r Row) Blank() bool { return r.Record == blankRecord }

// Get returns a field's value.
func (r Row) Get(key string) string { return r.Values[key] }

// Issue is a problem found in a file.
type Issue struct {
	Line   int    `json:"line"`
	Column string `json:"column,omitempty"`
	Msg    string `json:"message"`
}

// Table is a file read against its spec.
type Table struct {
	Spec   *Spec
	Name   string
	Rows   []Row
	Issues []Issue
	CRLF   bool
	CROnly bool
	Header []string
}

// Read parses data (already unpacked) with the spec. Every row is checked;
// problems are listed in Issues (the rows are still returned).
func (s *Spec) Read(name string, data []byte) (*Table, error) {
	t := &Table{Spec: s, Name: name}
	if s.Format == "json" {
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, fmt.Errorf("xfile: %s: not JSON: %w", name, err)
		}
		return t, nil
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	t.CRLF = bytes.Contains(data, []byte("\r\n"))
	t.CROnly = !t.CRLF && bytes.Contains(data, []byte("\r")) && !bytes.Contains(data, []byte("\n"))
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n"), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	start := 0
	if s.TitleLine != "" && len(lines) > 0 && strings.EqualFold(strings.TrimSpace(lines[0]), s.TitleLine) {
		start = 1
	}
	if s.Header {
		if len(lines) == 0 {
			return nil, fmt.Errorf("xfile: %s is empty", name)
		}
		t.Header = splitLine(s, lines[start])
		if !headerMatches(s, lines[start]) {
			t.Issues = append(t.Issues, Issue{Line: start + 1, Msg: headerDiff(s, t.Header)})
		}
		start++
	}
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			t.Rows = append(t.Rows, Row{Line: i + 1, Record: blankRecord})
			continue
		}
		if s.TextTrailer && i == len(lines)-1 && i > start {
			t.Rows = append(t.Rows, Row{Line: i + 1, Record: textRecord, Values: map[string]string{"text": lines[i]}})
			continue
		}
		f := splitLine(s, lines[i])
		rec := s.recordFor(f, i == start, i == len(lines)-1)
		if rec == nil {
			t.Issues = append(t.Issues, Issue{Line: i + 1, Msg: fmt.Sprintf("%d fields; no record layout of this file has that many", len(f))})
			continue
		}
		row := Row{Line: i + 1, Record: rec.Code, Values: map[string]string{}}
		if len(f) != len(rec.Columns) {
			t.Issues = append(t.Issues, Issue{Line: i + 1, Msg: fmt.Sprintf("%d fields, expected %d", len(f), len(rec.Columns))})
		}
		for j, c := range rec.Columns {
			v := ""
			if j < len(f) {
				v = f[j]
			}
			row.Values[c.Key] = v
			if msg := c.check(v); msg != "" {
				t.Issues = append(t.Issues, Issue{Line: i + 1, Column: c.Name, Msg: msg})
			}
		}
		t.Rows = append(t.Rows, row)
	}
	return t, nil
}

func headerDiff(s *Spec, got []string) string {
	cols := s.detail().Columns
	if len(got) != len(cols) {
		return fmt.Sprintf("header has %d columns, the layout %s has %d; is this the current version of the file?", len(got), s.ID, len(cols))
	}
	for i, c := range cols {
		if !strings.EqualFold(strings.TrimSpace(got[i]), c.Name) {
			return fmt.Sprintf("header column %d is %q, the layout expects %q", i+1, got[i], c.Name)
		}
	}
	return "header differs"
}

func (s *Spec) recordFor(f []string, first, last bool) *Record {
	if len(s.Records) == 0 {
		r := s.detail()
		return r
	}
	var byCount *Record
	for i := range s.Records {
		r := &s.Records[i]
		if r.Where == "first" && !first || r.Where == "last" && !last {
			continue
		}
		byFields := r.Code == "" || strings.HasPrefix(r.Code, "#")
		if !byFields && len(f) > 0 && strings.TrimSpace(f[0]) == r.Code && len(f) == len(r.Columns) {
			return r
		}
		if byFields && len(f) == len(r.Columns) && byCount == nil {
			byCount = r
		}
	}
	if byCount != nil {
		return byCount
	}
	for i := range s.Records {
		r := &s.Records[i]
		if (r.Where == "first" && first || r.Where == "last" && last) && len(f) == len(r.Columns) {
			return r
		}
	}
	return nil
}

var (
	reInt = regexp.MustCompile(`^-?\d+$`)
	reDec = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
)

func (c Column) check(v string) string {
	t := strings.TrimSpace(v)
	if t == "" {
		if c.Required {
			return "is empty"
		}
		return ""
	}
	if c.Const != "" && t != c.Const {
		return fmt.Sprintf("is %q, expected %q", t, c.Const)
	}
	switch c.Type {
	case "blank":
		return "" // reserved fields may be used later
	case "int":
		if !reInt.MatchString(t) {
			return fmt.Sprintf("%q is not a whole number", short(t))
		}
	case "dec":
		if !reDec.MatchString(t) {
			return fmt.Sprintf("%q is not a number", short(t))
		}
	case "date", "datetime", "time":
		if _, err := time.Parse(c.Layout, t); err != nil {
			return fmt.Sprintf("%q is not a date in the form %s", short(t), c.Layout)
		}
	}
	return ""
}

func short(s string) string {
	if len(s) > 24 {
		return s[:24] + "…"
	}
	return s
}

// ---- writing ----

// Vars fill a file name template.
type Vars map[string]string

// FileName fills the spec's name template: {key} from vars, {date:layout}
// from vars["date"] (YYYY-MM-DD), {seq:n} as a zero-padded sequence.
func (s *Spec) FileName(v Vars) (string, error) {
	if s.Template == "" {
		return "", fmt.Errorf("xfile: %s has no file name template", s.ID)
	}
	var err error
	re := regexp.MustCompile(`\{([a-z_]+)(?::([^}]+))?\}`)
	out := re.ReplaceAllStringFunc(s.Template, func(m string) string {
		p := re.FindStringSubmatch(m)
		key, arg := p[1], p[2]
		switch key {
		case "date":
			d, e := time.Parse("2006-01-02", v["date"])
			if e != nil {
				err = fmt.Errorf("xfile: %s: date %q", s.ID, v["date"])
				return m
			}
			if arg == "" {
				arg = "02012006"
			}
			return strings.ToUpper(d.Format(arg))
		case "seq":
			n, _ := strconv.Atoi(v["seq"])
			w, _ := strconv.Atoi(arg)
			if w == 0 {
				w = 2
			}
			return fmt.Sprintf("%0*d", w, n)
		}
		val, ok := v[key]
		if !ok || val == "" {
			err = fmt.Errorf("xfile: %s: the file name needs %s", s.ID, key)
		}
		return val
	})
	return out, err
}

// Write produces the file's content (not compressed) from rows. Each row's
// Record picks the layout ("" = the detail record).
func (s *Spec) Write(rows []Row) ([]byte, error) {
	if s.Format != "delimited" {
		return nil, fmt.Errorf("xfile: %s is %s; write it with WriteJSON", s.ID, s.Format)
	}
	var b bytes.Buffer
	eol := "\n"
	if s.CRLF {
		eol = "\r\n"
	}
	if s.CROnly {
		eol = "\r"
	}
	var lines []string
	if s.TitleLine != "" {
		lines = append(lines, s.TitleLine)
	}
	if s.Header {
		cols := s.detail().Columns
		if s.HeaderLine != "" {
			lines = append(lines, s.HeaderLine)
		} else {
			h := make([]string, len(cols))
			for i, c := range cols {
				h[i] = s.quote(c.Name, false)
			}
			lines = append(lines, strings.Join(h, s.Delimiter))
		}
	}
	for n, r := range rows {
		if r.Record == blankRecord {
			lines = append(lines, "")
			continue
		}
		if r.Record == textRecord {
			lines = append(lines, r.Values["text"])
			continue
		}
		rec := s.detail()
		if r.Record != "" || len(s.Records) > 0 {
			rec = nil
			for i := range s.Records {
				if s.Records[i].Code == r.Record {
					rec = &s.Records[i]
				}
			}
			if rec == nil && r.Record == "" {
				rec = s.detail()
			}
			if rec == nil {
				return nil, fmt.Errorf("xfile: %s has no record %q", s.ID, r.Record)
			}
		}
		f := make([]string, len(rec.Columns))
		for i, c := range rec.Columns {
			v, err := c.format(r.Values[c.Key])
			if err != nil {
				return nil, fmt.Errorf("xfile: %s row %d %s: %w", s.ID, n+1, c.Name, err)
			}
			if c.Const != "" && v == "" {
				v = c.Const
			}
			if c.Required && v == "" {
				return nil, fmt.Errorf("xfile: %s row %d: %s is required", s.ID, n+1, c.Name)
			}
			f[i] = s.quote(v, c.Quote == "always" || c.Quote == "set" && v != "")
		}
		lines = append(lines, strings.Join(f, s.Delimiter))
	}
	b.WriteString(strings.Join(lines, eol))
	if s.FinalEOL && len(lines) > 0 {
		b.WriteString(eol)
	}
	return b.Bytes(), nil
}

func (s *Spec) quote(v string, always bool) string {
	if always || strings.ContainsAny(v, s.Delimiter+"\"\r\n") {
		return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
	}
	return v
}

// format writes a value in the column's form: decimals for dec, the date
// layout for dates (values given as YYYY-MM-DD or RFC 3339 are converted).
func (c Column) format(v string) (string, error) {
	if c.Type == "str" || c.Type == "blank" {
		return v, nil // text is written as given, spaces included
	}
	t := strings.TrimSpace(v)
	if t == "" {
		return "", nil
	}
	switch c.Type {
	case "dec":
		if !reDec.MatchString(t) {
			return "", fmt.Errorf("%q is not a number", t)
		}
		if c.Fixed {
			r, ok := parseRat(t)
			if !ok {
				return "", fmt.Errorf("%q is not a number", t)
			}
			return r.FloatString(c.Decimals), nil
		}
		return t, nil
	case "int":
		if !reInt.MatchString(t) {
			return "", fmt.Errorf("%q is not a whole number", t)
		}
	case "date", "datetime", "time":
		if _, err := time.Parse(c.Layout, t); err == nil {
			return t, nil
		}
		for _, l := range []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04:05", "15:04:05"} {
			if d, err := time.Parse(l, t); err == nil {
				out := d.Format(c.Layout)
				if strings.Contains(c.Layout, "Jan") {
					out = strings.ToUpper(out)
				}
				return out, nil
			}
		}
		return "", fmt.Errorf("%q is not a date", t)
	}
	return v, nil
}

// Pack wraps content as the spec says, naming the inner file.
func (s *Spec) Pack(name string, content []byte) (string, []byte, error) {
	switch s.Compress {
	case "gzip":
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		zw.Name = name
		if _, err := zw.Write(content); err != nil {
			return "", nil, err
		}
		if err := zw.Close(); err != nil {
			return "", nil, err
		}
		return name + ".gz", b.Bytes(), nil
	case "zip":
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		w, err := zw.Create(name)
		if err != nil {
			return "", nil, err
		}
		if _, err := w.Write(content); err != nil {
			return "", nil, err
		}
		if err := zw.Close(); err != nil {
			return "", nil, err
		}
		return strings.TrimSuffix(name, path.Ext(name)) + ".zip", b.Bytes(), nil
	}
	return name, content, nil
}

// RoundTrip reads a file and writes it back from what was read. A layout
// that is exactly right reproduces the file byte for byte.
func (s *Spec) RoundTrip(name string, data []byte) (same bool, diff string, err error) {
	t, err := s.Read(name, data)
	if err != nil {
		return false, "", err
	}
	if s.Format == "json" {
		return true, "", nil
	}
	cp := *s
	cp.CRLF, cp.CROnly = t.CRLF, t.CROnly
	clean := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	cp.FinalEOL = bytes.HasSuffix(clean, []byte("\n")) || t.CROnly && bytes.HasSuffix(clean, []byte("\r"))
	if !t.CROnly {
		clean = bytes.TrimSuffix(clean, []byte("\r")) // a final line ending in CR alone
	}
	// Rewrite raw values (no reformatting) to test the structure.
	for i := range cp.Records {
		cp.Records[i].Columns = rawCols(cp.Records[i].Columns)
	}
	cp.Columns = rawCols(cp.Columns)
	out, err := cp.Write(t.Rows)
	if err != nil {
		return false, "", err
	}
	if (s.Header && len(t.Header) > 0) || s.TitleLine != "" {
		// keep the file's own header and title text (case, spacing)
		eol := "\n"
		if cp.CRLF {
			eol = "\r\n"
		}
		if cp.CROnly {
			eol = "\r"
		}
		n := 0
		if s.TitleLine != "" {
			n++
		}
		if s.Header {
			n++
		}
		src := strings.SplitN(string(clean), eol, n+1)
		dst := strings.SplitN(string(out), eol, n+1)
		if len(src) > n && len(dst) > n {
			out = []byte(strings.Join(append(src[:n:n], dst[n]), eol))
		}
	}
	if bytes.Equal(out, clean) {
		return true, "", nil
	}
	sep := "\n"
	if t.CROnly {
		sep = "\r"
	}
	a, b := strings.Split(string(clean), sep), strings.Split(string(out), sep)
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			k := 0
			for k < len(a[i]) && k < len(b[i]) && a[i][k] == b[i][k] {
				k++
			}
			from := max(0, k-20)
			return false, fmt.Sprintf("line %d differs at character %d: file %q, written %q", i+1, k+1, Mask(a[i][from:min(len(a[i]), k+20)]), Mask(b[i][from:min(len(b[i]), k+20)])), nil
		}
	}
	return false, fmt.Sprintf("%d lines read, %d written", len(a), len(b)), nil
}

func rawCols(cs []Column) []Column {
	out := make([]Column, len(cs))
	for i, c := range cs {
		c.Type, c.Fixed, c.Const, c.Required = "str", false, "", false
		out[i] = c
	}
	return out
}

func parseRat(s string) (*big.Rat, bool) { return new(big.Rat).SetString(s) }

// Mask hides values: letters become A and digits 9 (for messages about real files).
func Mask(v string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9':
			return '9'
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			return 'A'
		}
		return r
	}, v)
}
