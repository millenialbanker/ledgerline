package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/money"
)

// Spec describes one version of a delimited trade-file layout. Layouts are
// configuration: when an exchange changes its file format, add a new spec
// version instead of editing the old one, so historical files still parse.
type Spec struct {
	Name           string            `json:"name"`
	Version        string            `json:"version"`
	Origin         Origin            `json:"origin"`
	Exchange       string            `json:"exchange,omitempty"` // fixed value, or map the "exchange" column
	Segment        string            `json:"segment,omitempty"`  // fixed value, or map the "segment" column
	Delimiter      string            `json:"delimiter"`
	Header         bool              `json:"header"`
	SkipLines      int               `json:"skip_lines,omitempty"`
	Strict         bool              `json:"strict"` // reject the whole file if any row fails
	Columns        map[string]string `json:"columns"`
	Constants      map[string]string `json:"constants,omitempty"`
	SideMap        map[string]string `json:"side_map"`
	InstrumentMap  map[string]string `json:"instrument_map,omitempty"`
	ProductMap     map[string]string `json:"product_map,omitempty"`
	DateLayout     string            `json:"date_layout"`
	ExpiryLayout   string            `json:"expiry_layout,omitempty"`
	TimeLayout     string            `json:"time_layout,omitempty"`
	DateTimeLayout string            `json:"datetime_layout,omitempty"`
	Note           string            `json:"note,omitempty"`
}

var knownFields = map[string]bool{
	"trade_no": true, "order_no": true, "ucc": true, "symbol": true, "instrument_type": true, "option_type": true,
	"expiry": true, "strike": true, "side": true, "qty": true, "price": true, "trade_date": true, "trade_time": true,
	"trade_datetime": true, "product": true, "algo_id": true, "terminal": true, "exchange": true, "segment": true,
}

// ParseError is a row-level problem.
type ParseError struct {
	Line  int    `json:"line"`
	Field string `json:"field,omitempty"`
	Msg   string `json:"message"`
}

func (e ParseError) Error() string { return fmt.Sprintf("line %d %s: %s", e.Line, e.Field, e.Msg) }

// ErrRejected means a strict file had row errors and nothing was accepted.
var ErrRejected = errors.New("ingest: file rejected")

// Validate checks a spec is usable.
func (s Spec) Validate() error {
	if s.Name == "" || s.Version == "" {
		return errors.New("ingest: spec needs name and version")
	}
	if s.Origin != FromExchange && s.Origin != FromOMS {
		return fmt.Errorf("ingest: spec %s: origin must be EXCHANGE or OMS", s.Name)
	}
	if len([]rune(s.Delimiter)) != 1 {
		return fmt.Errorf("ingest: spec %s: delimiter must be one character", s.Name)
	}
	for f := range s.Columns {
		if !knownFields[f] {
			return fmt.Errorf("ingest: spec %s: unknown field %q", s.Name, f)
		}
	}
	for f := range s.Constants {
		if !knownFields[f] {
			return fmt.Errorf("ingest: spec %s: unknown constant %q", s.Name, f)
		}
	}
	has := func(f string) bool { _, c := s.Columns[f]; _, k := s.Constants[f]; return c || k }
	for _, f := range []string{"trade_no", "ucc", "symbol", "side", "qty", "price"} {
		if !has(f) {
			return fmt.Errorf("ingest: spec %s: field %q is not mapped", s.Name, f)
		}
	}
	if !has("trade_date") && !has("trade_datetime") {
		return fmt.Errorf("ingest: spec %s: map trade_date or trade_datetime", s.Name)
	}
	if s.Exchange == "" && !has("exchange") {
		return fmt.Errorf("ingest: spec %s: set exchange or map the exchange column", s.Name)
	}
	if s.Segment == "" && !has("segment") {
		return fmt.Errorf("ingest: spec %s: set segment or map the segment column", s.Name)
	}
	if len(s.SideMap) == 0 {
		return fmt.Errorf("ingest: spec %s: side_map is required", s.Name)
	}
	if !s.Header {
		for f, c := range s.Columns {
			if !strings.HasPrefix(c, "#") {
				return fmt.Errorf("ingest: spec %s: file has no header, so column for %q must be #<n>", s.Name, f)
			}
		}
	}
	return nil
}

// Parse reads a file in this layout. Row errors are collected; in strict
// mode any error rejects the file and no events are returned.
func (s Spec) Parse(r io.Reader) ([]TradeEvent, []ParseError, error) {
	cr := csv.NewReader(r)
	cr.Comma = []rune(s.Delimiter)[0]
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true
	cr.ReuseRecord = false

	for i := 0; i < s.SkipLines; i++ {
		if _, err := cr.Read(); err != nil {
			return nil, nil, fmt.Errorf("ingest: skipping preamble: %w", err)
		}
	}
	idx := map[string]int{}
	if s.Header {
		h, err := cr.Read()
		if err != nil {
			return nil, nil, fmt.Errorf("ingest: reading header: %w", err)
		}
		for i, name := range h {
			idx[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\ufeff")))] = i
		}
	}
	col := map[string]int{}
	for f, c := range s.Columns {
		if strings.HasPrefix(c, "#") {
			n, err := strconv.Atoi(c[1:])
			if err != nil || n < 1 {
				return nil, nil, fmt.Errorf("ingest: spec %s: bad column index %q", s.Name, c)
			}
			col[f] = n - 1
			continue
		}
		i, ok := idx[strings.ToLower(c)]
		if !ok {
			return nil, nil, fmt.Errorf("ingest: header has no column %q (for %s); is this the right file version?", c, f)
		}
		col[f] = i
	}

	var out []TradeEvent
	var errs []ParseError
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line, _ := cr.FieldPos(0)
		if err != nil {
			errs = append(errs, ParseError{Line: line, Msg: err.Error()})
			continue
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue // blank line
		}
		ev, rowErrs := s.row(rec, col, line)
		if len(rowErrs) > 0 {
			errs = append(errs, rowErrs...)
			continue
		}
		out = append(out, ev)
	}
	if s.Strict && len(errs) > 0 {
		return nil, errs, fmt.Errorf("%w: %d row error(s) in strict layout %s@%s", ErrRejected, len(errs), s.Name, s.Version)
	}
	return out, errs, nil
}

func (s Spec) row(rec []string, col map[string]int, line int) (TradeEvent, []ParseError) {
	var errs []ParseError
	bad := func(f, m string, a ...any) {
		errs = append(errs, ParseError{Line: line, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	get := func(f string) string {
		if i, ok := col[f]; ok {
			if i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		return s.Constants[f]
	}
	ev := TradeEvent{
		Origin: s.Origin, Exchange: strings.ToUpper(firstNonEmpty(get("exchange"), s.Exchange)),
		Segment: strings.ToUpper(firstNonEmpty(get("segment"), s.Segment)),
		TradeNo: get("trade_no"), OrderNo: get("order_no"), UCC: strings.ToUpper(get("ucc")),
		Symbol: strings.ToUpper(get("symbol")), AlgoID: get("algo_id"), Terminal: get("terminal"), Line: line,
	}
	for f, v := range map[string]string{"trade_no": ev.TradeNo, "ucc": ev.UCC, "symbol": ev.Symbol, "exchange": ev.Exchange, "segment": ev.Segment} {
		if v == "" {
			bad(f, "is empty")
		}
	}
	// Instrument type: map exchange codes (FUTIDX → FUT), then an option type column wins for options.
	it := strings.ToUpper(get("instrument_type"))
	if m, ok := s.InstrumentMap[it]; ok {
		it = m
	}
	if ot := strings.ToUpper(get("option_type")); ot == "CE" || ot == "PE" {
		it = ot
	} else if it == "OPT" {
		bad("option_type", "option trade without CE/PE")
	}
	if it == "" {
		it = "EQ"
	}
	switch it {
	case "EQ", "FUT", "CE", "PE":
		ev.InstrumentType = it
	default:
		bad("instrument_type", "unrecognised value %q; add it to instrument_map", it)
	}
	side := strings.ToUpper(get("side"))
	if m, ok := s.SideMap[side]; ok {
		ev.Side = m
	} else {
		bad("side", "unrecognised value %q", side)
	}
	if q, err := strconv.ParseInt(get("qty"), 10, 64); err != nil || q <= 0 {
		bad("qty", "must be a positive whole number, got %q", get("qty"))
	} else {
		ev.Qty = q
	}
	if p, err := money.ParseDec(get("price")); err != nil || p.Sign() < 0 {
		bad("price", "must be a non-negative decimal, got %q", get("price"))
	} else {
		ev.Price = p
	}
	if dt := get("trade_datetime"); dt != "" && s.DateTimeLayout != "" {
		if t, err := time.Parse(s.DateTimeLayout, dt); err != nil {
			bad("trade_datetime", "does not match %q", s.DateTimeLayout)
		} else {
			ev.TradeDate, ev.TradeTime = t.Format("2006-01-02"), t.Format("15:04:05")
		}
	} else {
		if t, err := time.Parse(s.DateLayout, get("trade_date")); err != nil {
			bad("trade_date", "%q does not match layout %q", get("trade_date"), s.DateLayout)
		} else {
			ev.TradeDate = t.Format("2006-01-02")
		}
		if v := get("trade_time"); v != "" {
			layout := firstNonEmpty(s.TimeLayout, "15:04:05")
			if t, err := time.Parse(layout, v); err != nil {
				bad("trade_time", "%q does not match layout %q", v, layout)
			} else {
				ev.TradeTime = t.Format("15:04:05")
			}
		}
	}
	if v := get("expiry"); v != "" {
		layout := firstNonEmpty(s.ExpiryLayout, s.DateLayout)
		if t, err := time.Parse(layout, v); err != nil {
			bad("expiry", "%q does not match layout %q", v, layout)
		} else {
			ev.Expiry = t.Format("2006-01-02")
		}
	} else if ev.InstrumentType != "EQ" && ev.InstrumentType != "" {
		bad("expiry", "derivative trade without expiry")
	}
	if v := get("strike"); v != "" {
		if k, err := money.ParseDec(v); err != nil {
			bad("strike", "invalid %q", v)
		} else if !k.IsZero() {
			ev.Strike = k.String()
		}
	}
	if (ev.InstrumentType == "CE" || ev.InstrumentType == "PE") && ev.Strike == "" {
		bad("strike", "option trade without strike")
	}
	prod := strings.ToUpper(get("product"))
	if m, ok := s.ProductMap[prod]; ok {
		prod = m
	}
	ev.Product = prod
	return ev, errs
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// Registry holds every known layout version.
type Registry struct{ specs map[string]map[string]Spec }

func NewRegistry() *Registry { return &Registry{specs: map[string]map[string]Spec{}} }

// Add registers a spec version.
func (r *Registry) Add(s Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if r.specs[s.Name] == nil {
		r.specs[s.Name] = map[string]Spec{}
	}
	if _, dup := r.specs[s.Name][s.Version]; dup {
		return fmt.Errorf("ingest: %s@%s registered twice", s.Name, s.Version)
	}
	r.specs[s.Name][s.Version] = s
	return nil
}

// LoadDir registers every *.json spec in dir.
func LoadDir(dir string) (*Registry, error) {
	r := NewRegistry()
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("ingest: no adapter specs in %s", dir)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var s Spec
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return nil, fmt.Errorf("ingest: %s: %w", filepath.Base(p), err)
		}
		if err := r.Add(s); err != nil {
			return nil, fmt.Errorf("ingest: %s: %w", filepath.Base(p), err)
		}
	}
	return r, nil
}

// Get returns a spec; an empty version means the highest version.
func (r *Registry) Get(name, version string) (Spec, error) {
	vs, ok := r.specs[name]
	if !ok {
		return Spec{}, fmt.Errorf("ingest: unknown file source %q", name)
	}
	if version != "" {
		s, ok := vs[version]
		if !ok {
			return Spec{}, fmt.Errorf("ingest: %s has no version %q", name, version)
		}
		return s, nil
	}
	var best string
	for v := range vs {
		if best == "" || compareVersions(v, best) > 0 {
			best = v
		}
	}
	return vs[best], nil
}

// List returns every registered spec, sorted by name and version.
func (r *Registry) List() []Spec {
	var out []Spec
	for _, vs := range r.specs {
		for _, s := range vs {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return compareVersions(out[i].Version, out[j].Version) < 0
	})
	return out
}

// compareVersions compares dotted versions numerically where possible.
func compareVersions(a, b string) int {
	split := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' })
	}
	pa, pb := split(a), split(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		switch {
		case ex == nil && ey == nil && nx != ny:
			if nx < ny {
				return -1
			}
			return 1
		case (ex != nil || ey != nil) && x != y:
			return strings.Compare(x, y)
		}
	}
	return 0
}

// Service stores raw files, parses them and records the trades.
type Service struct {
	Raw      RawStore
	Trades   TradeStore
	Registry *Registry
}

// Result reports what happened to one upload.
type Result struct {
	File          RawFile      `json:"file"`
	Adapter       string       `json:"adapter"`
	Parsed        int          `json:"parsed"`
	Added         int          `json:"added"`
	DuplicateRows int          `json:"duplicate_rows"`
	Errors        []ParseError `json:"errors"`
}

// Ingest keeps the raw bytes, parses them with the named layout (latest
// version when version is empty) and stores the trades. Re-uploading the
// same file is safe: rows already stored count as duplicates.
func (s *Service) Ingest(ctx context.Context, tenant, source, version, name string, data []byte) (Result, error) {
	if tenant == "" {
		return Result{}, errors.New("ingest: tenant is required")
	}
	spec, err := s.Registry.Get(source, version)
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(data)
	rf, err := s.Raw.Put(ctx, RawFile{Tenant: tenant, Source: spec.Name, Name: name, SHA256: hex.EncodeToString(sum[:]), Content: data})
	if err != nil {
		return Result{}, err
	}
	res := Result{File: rf, Adapter: spec.Name + "@" + spec.Version, Errors: []ParseError{}}
	evs, perrs, err := spec.Parse(bytes.NewReader(data))
	if perrs != nil {
		res.Errors = perrs
	}
	if err != nil {
		return res, err
	}
	for i := range evs {
		evs[i].Tenant = tenant
		evs[i].FileID = rf.ID
	}
	res.Parsed = len(evs)
	res.Added, res.DuplicateRows, err = s.Trades.Add(ctx, evs)
	return res, err
}
