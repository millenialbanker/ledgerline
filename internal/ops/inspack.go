package ops

import (
	"archive/zip"
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/pdf"
)

// ---- inspection packs ----
//
// An exchange, depository or SEBI inspection (or the firm's internal
// auditor) starts with a data request: a list of records for a period and,
// for some, a sample of clients. The compliance officer picks the
// inspection's template and the period, draws the sample (N clients at
// random with the seed recorded, so the same sample can be drawn again, or
// the list the inspector gave), reviews the items, and Ledgerline builds
// the pack in the background: each item through Ledgerline's own reports
// and exports (the sections, registered with RegisterPackSection), the
// compliance calendar's evidence, circular actions, and the policy and
// audit documents uploaded here. The pack is one ZIP with a cover sheet
// (firm, inspection, period, sample seed, who prepared it and when, and
// the SHA-256 of every file) and an index of every item with its files,
// record counts and period, or why Ledgerline cannot produce it.
//
// A pack holds full client data, because inspectors need it. Only the
// compliance officer and Admin make, list or download packs; every make,
// download and opening of the index goes to the sealed audit trail and the
// pack's own access list. Every file Ledgerline writes carries the
// watermark "Prepared for <inspection>, <date>, by <person>": the first
// row of a CSV, a band across the top of every HTML page and every PDF
// page. Copies of uploaded documents are put in as they were uploaded.
// "Mask personal details" builds a copy for outside advisers: client
// names, codes, PANs, Aadhaar-like numbers, phones, emails and account
// numbers become stable tokens ([CLIENT-3]) and KYC document images are
// left out.
//
// The ZIP is kept in the file store (encrypted with the firm's keys) and
// leaves the download list after the firm's keep period (30 days unless
// changed). The store is append-only: an expired pack is tombstoned
// (Expired) and no longer served; its record, results and access list stay.

const (
	cPack         = "inspack"
	cPackTemplate = "inspacktpl"
	cPackDoc      = "inspackdoc"
	cPackCfg      = "inspackcfg"
	srcPack       = "INSPECTION-PACK"
	srcPackDoc    = "INSPECTION-DOC"

	// PackKeepDays is how long a pack stays on the download list by default.
	PackKeepDays = 30
	// MaxPackDoc is the largest policy or evidence document accepted.
	MaxPackDoc = 20 << 20
)

// Pack states.
const (
	PackQueued      = "Queued"
	PackBuilding    = "Building"
	PackReady       = "Ready"
	PackFailed      = "Failed"
	PackInterrupted = "Interrupted"
)

// PackPeriods are the periods an item can cover.
var PackPeriods = []PackPeriodKind{
	{"period", "The inspection period"},
	{"end", "As at the end of the period"},
	{"current", "The current version"},
}

// PackPeriodKind is one choice of period for an item.
type PackPeriodKind struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// PackItem is one item of a data request.
type PackItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Period  string `json:"period"`            // period, end, current
	Sampled bool   `json:"sampled,omitempty"` // for the sampled clients only
	Section string `json:"section"`           // the Ledgerline report or export that produces it
	Param   string `json:"param,omitempty"`   // the section's option (document kinds, a complaint filter)
	Basis   string `json:"basis,omitempty"`   // where the request comes from
	// ToConfirm is set when the item could not be confirmed against a
	// published data-request list or circular, with what to check.
	ToConfirm string `json:"to_confirm,omitempty"`
	Note      string `json:"note,omitempty"` // for records Ledgerline does not hold: why
}

// PackTemplate is a data request list: one of the standard ones, or the
// firm's own.
type PackTemplate struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"` // joint, exchange, depository, sebi, internal_audit, system_audit, firm
	Authority string     `json:"authority"`
	About     string     `json:"about"`
	Sources   []string   `json:"sources"`
	SampleN   int        `json:"sample_n"`
	Items     []PackItem `json:"items"`
	Standard  bool       `json:"standard,omitempty"`
	Deleted   bool       `json:"deleted,omitempty"`
	By        string     `json:"by,omitempty"`
	At        string     `json:"at,omitempty"`
}

// PackSampleReq asks for a sample of clients.
type PackSampleReq struct {
	Mode  string   `json:"mode"` // random, list, none
	N     int      `json:"n,omitempty"`
	Seed  int64    `json:"seed,omitempty"` // 0: a new seed
	Given []string `json:"given,omitempty"`
}

// PackSample is the sample drawn, with what is needed to draw it again.
type PackSample struct {
	Mode       string   `json:"mode"`
	N          int      `json:"n"`
	Seed       int64    `json:"seed,omitempty"`
	Clients    []string `json:"clients"`
	Population int      `json:"population"`
	PopSHA256  string   `json:"population_sha256,omitempty"`
	PopRule    string   `json:"population_rule,omitempty"`
}

// PackRequest is what the new-pack wizard sends.
type PackRequest struct {
	Template string        `json:"template"`
	Title    string        `json:"title"`
	Ref      string        `json:"ref,omitempty"`
	From     string        `json:"from"`
	To       string        `json:"to"`
	Sample   PackSampleReq `json:"sample"`
	Items    []PackItem    `json:"items,omitempty"` // as reviewed; empty: the template's
	Mask     bool          `json:"mask,omitempty"`
}

// Pack is one inspection pack.
type Pack struct {
	ID           string       `json:"id"`
	Title        string       `json:"title"`
	Ref          string       `json:"ref,omitempty"`
	Template     string       `json:"template,omitempty"`
	TemplateName string       `json:"template_name,omitempty"`
	Kind         string       `json:"kind,omitempty"`
	Authority    string       `json:"authority,omitempty"`
	From         string       `json:"from"`
	To           string       `json:"to"`
	Sample       PackSample   `json:"sample"`
	Items        []PackItem   `json:"items"`
	Mask         bool         `json:"mask,omitempty"`
	CopyOf       string       `json:"copy_of,omitempty"`
	Watermark    string       `json:"watermark"`
	Status       string       `json:"status"`
	Progress     PackProgress `json:"progress"`
	Results      []PackResult `json:"results"`
	Error        string       `json:"error,omitempty"`
	FileID       string       `json:"file_id,omitempty"`
	FileName     string       `json:"file_name,omitempty"`
	Size         int64        `json:"size,omitempty"`
	SHA256       string       `json:"sha256,omitempty"`
	Files        int          `json:"files,omitempty"`
	By           string       `json:"by"`
	ByName       string       `json:"by_name"`
	At           string       `json:"at"`
	BuiltAt      string       `json:"built_at,omitempty"`
	ExpiresAt    string       `json:"expires_at"`
	Expired      bool         `json:"expired,omitempty"`
	ExpiredAt    string       `json:"expired_at,omitempty"`
	ExpiredBy    string       `json:"expired_by,omitempty"`
	ExpiredWhy   string       `json:"expired_why,omitempty"`
	Access       []PackAccess `json:"access"`
}

// PackProgress is how far the build has got.
type PackProgress struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Current string `json:"current,omitempty"`
}

// PackResult is what one item produced.
type PackResult struct {
	No        int            `json:"no"`
	Item      string         `json:"item"`
	Title     string         `json:"title"`
	Period    string         `json:"period"`
	Source    string         `json:"source"`
	Status    string         `json:"status"` // Included, Not available
	Why       string         `json:"why,omitempty"`
	Note      string         `json:"note,omitempty"`
	ToConfirm string         `json:"to_confirm,omitempty"`
	Records   int            `json:"records"`
	Files     []PackFileInfo `json:"files"`
}

// PackFileInfo is one file in the ZIP.
type PackFileInfo struct {
	Path    string `json:"path"`
	Records int    `json:"records"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256"`
}

// PackAccess is one use of a pack, as its access list shows it (the sealed
// audit trail has the same entries).
type PackAccess struct {
	At     string `json:"at"`
	By     string `json:"by"`
	What   string `json:"what"`
	Detail string `json:"detail,omitempty"`
}

// PackSettings are the firm's choices for packs.
type PackSettings struct {
	KeepDays int    `json:"keep_days"`
	By       string `json:"by,omitempty"`
	At       string `json:"at,omitempty"`
}

// PackDoc is a policy or evidence document kept for inspections.
type PackDoc struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	Version     string `json:"version,omitempty"` // date or version of the document
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	FileID      string `json:"file_id"`
	By          string `json:"by"`
	At          string `json:"at"`
	Retired     bool   `json:"retired,omitempty"`
	RetiredBy   string `json:"retired_by,omitempty"`
	RetiredAt   string `json:"retired_at,omitempty"`
	RetiredWhy  string `json:"retired_why,omitempty"`
}

// PackDocKinds are the kinds of document kept for inspections.
var PackDocKinds = []string{"Policy", "Board minutes", "CSCRF", "VAPT report", "Cyber audit report", "System audit report", "Internal audit report", "Concurrent audit report", "Action taken report", "Net worth certificate", "Audited accounts", "Other"}

// ---- the section registry ----

// PackSection is one kind of item: the Ledgerline report or export that
// produces it. Other features add theirs with RegisterPackSection.
type PackSection struct {
	ID    string
	Name  string // the report as the screens call it, e.g. "Regulatory reports → Client register"
	About string
	// Build makes the item's files.
	Build func(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error)
	// Ready, when set, says before a build why the item cannot be produced
	// ("" when it can), for the review step.
	Ready func(ctx context.Context, o *Ops, tenant string, it PackItem) string
}

// PackPart is what a section made for one item.
type PackPart struct {
	Files        []PackFile
	Records      int
	NotAvailable string // why the item cannot be produced (then no files)
	Note         string // shown in the index
}

// PackFile is one file a section made.
type PackFile struct {
	Name    string
	Data    []byte
	Records int
}

// PackSectionInfo is a section as the screen lists it.
type PackSectionInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	About string `json:"about"`
}

var (
	packSectionMu    sync.RWMutex
	packSections     = map[string]PackSection{}
	packSectionOrder []string
)

// RegisterPackSection adds a kind of pack item. Call it from an init
// function; an ID registered twice panics.
func RegisterPackSection(s PackSection) {
	packSectionMu.Lock()
	defer packSectionMu.Unlock()
	if s.ID == "" || s.Build == nil {
		panic("RegisterPackSection: a section needs an ID and a Build function")
	}
	if _, dup := packSections[s.ID]; dup {
		panic("RegisterPackSection: section " + s.ID + " registered twice")
	}
	packSections[s.ID] = s
	packSectionOrder = append(packSectionOrder, s.ID)
}

func packSection(id string) (PackSection, bool) {
	packSectionMu.RLock()
	defer packSectionMu.RUnlock()
	s, ok := packSections[id]
	return s, ok
}

// PackSections lists the registered sections in the order they were added.
func PackSections() []PackSectionInfo {
	packSectionMu.RLock()
	defer packSectionMu.RUnlock()
	out := make([]PackSectionInfo, 0, len(packSectionOrder))
	for _, id := range packSectionOrder {
		s := packSections[id]
		out = append(out, PackSectionInfo{ID: s.ID, Name: s.Name, About: s.About})
	}
	return out
}

// ---- the build context ----

// PackBuild is what a section works with while a pack is built.
type PackBuild struct {
	O         *Ops
	Tenant    string
	Actor     string
	From, To  string
	Clients   []string // the sampled clients
	Mask      bool
	Watermark string
	Set       Settings
	Today     string
	masker    *Masker
	clients   map[string]Client
}

// Span is the dates an item covers: the period, its last day, or ("",
// today) for the current version.
func (b *PackBuild) Span(it PackItem) (string, string) {
	switch it.Period {
	case "end":
		return b.To, b.To
	case "current":
		return "", b.Today
	}
	return b.From, b.To
}

// Days are the business days from from to to.
func (b *PackBuild) Days(from, to string) []string {
	var out []string
	for d := from; d != "" && d <= to; d = addDays(d, 1) {
		if b.Set.IsBusinessDay(d) {
			out = append(out, d)
		}
	}
	return out
}

// ClientMap is every client, read once per build.
func (b *PackBuild) ClientMap(ctx context.Context) map[string]Client {
	if b.clients == nil {
		b.clients, _ = b.O.ClientMap(ctx, b.Tenant)
		if b.clients == nil {
			b.clients = map[string]Client{}
		}
	}
	return b.clients
}

// Sampled reports whether a client is in the sample.
func (b *PackBuild) Sampled(ucc string) bool { return contains(b.Clients, ucc) }

// maskCell hides personal details in a masked copy.
func (b *PackBuild) maskCell(head, v string) string {
	if !b.Mask || v == "" {
		return v
	}
	h := strings.ToLower(head)
	switch {
	case h == "dob" || strings.Contains(h, "birth"):
		if len(v) >= 4 {
			return v[:4] + "-XX-XX"
		}
		return "XXXX"
	case strings.Contains(h, "address"):
		return "[ADDRESS]"
	}
	return b.masker.Mask(v)
}

// Text masks one piece of free text in a masked copy.
func (b *PackBuild) Text(s string) string {
	if !b.Mask {
		return s
	}
	return b.masker.Mask(s)
}

// CSV makes a CSV file from rows (header first): the watermark is its
// first row, and a masked copy has the personal details hidden. The
// record count is the rows after the header.
func (b *PackBuild) CSV(name string, rows [][]string) PackFile {
	n := len(rows) - 1
	if n < 0 {
		n = 0
	}
	return b.CSVn(name, rows, n)
}

// CSVn is CSV with the record count given (for reports laid out in
// several tables).
func (b *PackBuild) CSVn(name string, rows [][]string, records int) PackFile {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{b.Watermark})
	var head []string
	for i, r := range rows {
		if i == 0 {
			head = r
		}
		if b.Mask && i > 0 {
			m := make([]string, len(r))
			for j, v := range r {
				h := ""
				if j < len(head) {
					h = head[j]
				}
				m[j] = b.maskCell(h, v)
			}
			r = m
		}
		_ = w.Write(r)
	}
	w.Flush()
	return PackFile{Name: name, Data: buf.Bytes(), Records: records}
}

// PDF renders a report with the watermark across the top of every page.
func (b *PackBuild) PDF(name string, r pdf.Report, records int) (PackFile, error) {
	if b.Mask {
		r = b.maskReport(r)
	}
	d := pdf.Render(r)
	for i := 0; i < d.Pages(); i++ {
		d.SetPage(i)
		d.TextGray(36, 20, 7, true, 0.35, pdf.Fit(b.Watermark, 7, true, d.W-72))
	}
	data, err := d.Bytes(pdf.Meta{Title: r.Title, Author: b.Set.Firm, Subject: b.Watermark, Created: b.O.now()}, nil)
	return PackFile{Name: name, Data: data, Records: records}, err
}

func (b *PackBuild) maskReport(r pdf.Report) pdf.Report {
	m := b.masker.Mask
	ms := func(xs []string) []string {
		out := make([]string, len(xs))
		for i, x := range xs {
			out[i] = m(x)
		}
		return out
	}
	r.Party, r.Notes, r.Banner = ms(r.Party), ms(r.Notes), m(r.Banner)
	r.Meta = ms(r.Meta)
	sum := make([][2]string, len(r.Summary))
	for i, kv := range r.Summary {
		sum[i] = [2]string{kv[0], m(kv[1])}
	}
	r.Summary = sum
	secs := make([]pdf.Table, len(r.Sections))
	for i, t := range r.Sections {
		rows := make([][]string, len(t.Rows))
		for j, row := range t.Rows {
			rows[j] = ms(row)
		}
		t.Rows, t.Totals = rows, ms(t.Totals)
		secs[i] = t
	}
	r.Sections = secs
	return r
}

var reBodyOpen = regexp.MustCompile(`(?i)<body[^>]*>`)

// HTML puts the watermark at the top of a printable page (fixed, so it is
// on every printed page) and masks the text of a masked copy.
func (b *PackBuild) HTML(name string, page []byte, records int) PackFile {
	band := `<div style="position:fixed;top:0;left:0;right:0;z-index:9;background:#fff;color:#16201B;border-bottom:1px solid #16201B;font:600 11px/1.6 system-ui,sans-serif;padding:4px 16px">` + template.HTMLEscapeString(b.Watermark) + `</div><div style="height:26px"></div>`
	s := string(page)
	if b.Mask {
		s = b.masker.Mask(s)
	}
	if loc := reBodyOpen.FindStringIndex(s); loc != nil {
		s = s[:loc[1]] + band + s[loc[1]:]
	} else {
		s = band + s
	}
	return PackFile{Name: name, Data: []byte(s), Records: records}
}

// ---- settings ----

func (o *Ops) packSettings(ctx context.Context, tenant string) PackSettings {
	s, _, _ := docs.GetOr(ctx, o.Docs, tenant, cPackCfg, "main", PackSettings{KeepDays: PackKeepDays})
	if s.KeepDays <= 0 {
		s.KeepDays = PackKeepDays
	}
	return s
}

// SavePackSettings changes how long packs stay on the download list.
func (o *Ops) SavePackSettings(ctx context.Context, tenant, actor string, s PackSettings) (PackSettings, error) {
	if err := o.require(ctx, tenant, actor, "Changing inspection pack settings", RoleCompliance); err != nil {
		return s, err
	}
	if s.KeepDays < 1 || s.KeepDays > 365 {
		return s, invalid("keep packs for 1 to 365 days")
	}
	s.By, s.At = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cPackCfg, "main", s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "inspack.settings", "settings", map[string]any{"keep_days": s.KeepDays})
	return s, nil
}

// ---- templates ----

// PackTemplates lists the standard templates, then the firm's own.
func (o *Ops) PackTemplates(ctx context.Context, tenant string) ([]PackTemplate, error) {
	out := DefaultPackTemplates()
	ts, err := docs.List[PackTemplate](ctx, o.Docs, tenant, cPackTemplate, "")
	sort.SliceStable(ts, func(i, j int) bool { return strings.ToLower(ts[i].Name) < strings.ToLower(ts[j].Name) })
	for _, t := range ts {
		if !t.Deleted {
			out = append(out, t)
		}
	}
	return out, err
}

func (o *Ops) packTemplate(ctx context.Context, tenant, id string) (PackTemplate, error) {
	for _, t := range DefaultPackTemplates() {
		if t.ID == id {
			return t, nil
		}
	}
	t, _, err := docs.Get[PackTemplate](ctx, o.Docs, tenant, cPackTemplate, id)
	if err != nil || t.Deleted {
		return PackTemplate{}, fmt.Errorf("%w: no template %s", ErrNotFound, id)
	}
	return t, nil
}

var packKinds = []string{"joint", "exchange", "depository", "sebi", "internal_audit", "system_audit", "firm"}

// checkPackItems checks and numbers a list of items.
func checkPackItems(items []PackItem) ([]PackItem, error) {
	if len(items) == 0 {
		return nil, invalid("a pack needs at least one item")
	}
	if len(items) > 80 {
		return nil, invalid("a pack can have at most 80 items")
	}
	out := make([]PackItem, 0, len(items))
	for i, it := range items {
		it.Title = strings.TrimSpace(it.Title)
		if it.Title == "" {
			return nil, invalid("item %d needs a title", i+1)
		}
		if len(it.Title) > 160 {
			return nil, invalid("item %d: keep the title under 160 characters", i+1)
		}
		if _, ok := packSection(it.Section); !ok {
			return nil, invalid("item %d (%s): choose which Ledgerline report produces it", i+1, it.Title)
		}
		if it.Period == "" {
			it.Period = "period"
		}
		ok := false
		for _, p := range PackPeriods {
			ok = ok || p.ID == it.Period
		}
		if !ok {
			return nil, invalid("item %d (%s): the period must be the inspection period, its end or the current version", i+1, it.Title)
		}
		it.ID = fmt.Sprintf("I%d", i+1)
		it.Param, it.Basis, it.ToConfirm, it.Note = strings.TrimSpace(it.Param), strings.TrimSpace(it.Basis), strings.TrimSpace(it.ToConfirm), strings.TrimSpace(it.Note)
		out = append(out, it)
	}
	return out, nil
}

// SavePackTemplate adds or changes one of the firm's own templates. The
// standard templates are not changed; a copy is saved instead.
func (o *Ops) SavePackTemplate(ctx context.Context, tenant, actor string, t PackTemplate) (PackTemplate, error) {
	if err := o.require(ctx, tenant, actor, "Changing inspection pack templates", RoleCompliance); err != nil {
		return t, err
	}
	if strings.HasPrefix(t.ID, "std-") {
		return t, invalid("the standard templates are not changed: save a copy under a new name as the firm's own template")
	}
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" || len(t.Name) > 120 {
		return t, invalid("a template needs a name (up to 120 characters)")
	}
	items, err := checkPackItems(t.Items)
	if err != nil {
		return t, err
	}
	t.Items = items
	if !contains(packKinds, t.Kind) {
		t.Kind = "firm"
	}
	if t.SampleN < 0 || t.SampleN > 500 {
		return t, invalid("a sample is 0 to 500 clients")
	}
	if t.ID == "" {
		n, err := o.next(ctx, tenant, "inspacktpl", actor)
		if err != nil {
			return t, err
		}
		t.ID = fmt.Sprintf("TPL-%04d", n)
	} else if old, _, err := docs.Get[PackTemplate](ctx, o.Docs, tenant, cPackTemplate, t.ID); err != nil || old.Deleted {
		return t, fmt.Errorf("%w: no template %s", ErrNotFound, t.ID)
	}
	if t.Sources == nil {
		t.Sources = []string{}
	}
	t.Standard, t.Deleted, t.By, t.At = false, false, actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cPackTemplate, t.ID, t, actor); err != nil {
		return t, err
	}
	o.log(ctx, tenant, actor, "inspack.template.saved", t.ID, map[string]any{"name": t.Name, "items": len(t.Items)})
	return t, nil
}

// DeletePackTemplate takes one of the firm's templates off the list. The
// record stays (tombstoned): packs made from it still name it.
func (o *Ops) DeletePackTemplate(ctx context.Context, tenant, actor, id string) error {
	if err := o.require(ctx, tenant, actor, "Changing inspection pack templates", RoleCompliance); err != nil {
		return err
	}
	if strings.HasPrefix(id, "std-") {
		return invalid("the standard templates cannot be removed")
	}
	_, err := docs.Update(ctx, o.Docs, tenant, cPackTemplate, id, actor, func(t *PackTemplate) error {
		if t.Deleted {
			return fmt.Errorf("%w: no template %s", ErrNotFound, id)
		}
		t.Deleted, t.By, t.At = true, actor, o.now().Format(time.RFC3339)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "inspack.template.deleted", id, nil)
	}
	return err
}

// ---- the sample ----

// packPopulationRule says which clients a random sample is drawn from.
const packPopulationRule = "Clients whose account was opened by the period end (KYC date on or before it), not rejected, in client code order"

// packPopulation is the clients a sample is drawn from, sorted.
func packPopulation(cs []Client, to string) []string {
	var out []string
	for _, c := range cs {
		if c.Status == "Rejected" || (c.KYCDate != "" && c.KYCDate > to) {
			continue
		}
		out = append(out, c.UCC)
	}
	sort.Strings(out)
	return out
}

// splitmix64 is the generator behind the sample. It is written out here,
// not taken from a library, so a seed draws the same clients on every
// version of Ledgerline.
type splitmix64 struct{ s uint64 }

func (g *splitmix64) next() uint64 {
	g.s += 0x9E3779B97F4A7C15
	z := g.s
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// below is a uniform number in [0, n), by rejection.
func (g *splitmix64) below(n uint64) uint64 {
	limit := ^uint64(0) - (^uint64(0) % n)
	for {
		if v := g.next(); v < limit {
			return v % n
		}
	}
}

// drawSample picks n of pop with a Fisher–Yates shuffle from the seed; the
// result is in client code order.
func drawSample(pop []string, n int, seed int64) []string {
	xs := append([]string(nil), pop...)
	if n > len(xs) {
		n = len(xs)
	}
	g := &splitmix64{s: uint64(seed)}
	for i := 0; i < n; i++ {
		j := i + int(g.below(uint64(len(xs)-i)))
		xs[i], xs[j] = xs[j], xs[i]
	}
	out := append([]string(nil), xs[:n]...)
	sort.Strings(out)
	return out
}

// newPackSeed is a 12-digit seed, easy to read out and type.
func newPackSeed() int64 {
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		return time.Now().UnixNano()%900_000_000_000 + 100_000_000_000
	}
	return int64(binary.BigEndian.Uint64(b[:])%900_000_000_000) + 100_000_000_000
}

var rePackUCC = regexp.MustCompile(`[A-Za-z0-9]{3,10}`)

func (o *Ops) packSample(ctx context.Context, tenant, to string, r PackSampleReq) (PackSample, error) {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return PackSample{}, err
	}
	switch r.Mode {
	case "", "none":
		return PackSample{Mode: "none", Clients: []string{}}, nil
	case "random":
		if r.N < 1 || r.N > 500 {
			return PackSample{}, invalid("choose how many clients to sample: 1 to 500")
		}
		if r.Seed < 0 || r.Seed > 999_999_999_999_999 {
			return PackSample{}, invalid("the seed is a whole number of up to 15 digits")
		}
		if r.Seed == 0 {
			r.Seed = newPackSeed()
		}
		pop := packPopulation(cs, to)
		if len(pop) == 0 {
			return PackSample{}, invalid("no clients had an account by %s to sample from", fmtDay(to))
		}
		sum := sha256.Sum256([]byte(strings.Join(pop, "\n")))
		s := drawSample(pop, r.N, r.Seed)
		return PackSample{Mode: "random", N: len(s), Seed: r.Seed, Clients: s, Population: len(pop), PopSHA256: hex.EncodeToString(sum[:]), PopRule: packPopulationRule}, nil
	case "list":
		known := map[string]bool{}
		for _, c := range cs {
			known[c.UCC] = true
		}
		var s, missing []string
		seen := map[string]bool{}
		for _, g := range r.Given {
			for _, u := range rePackUCC.FindAllString(strings.ToUpper(g), -1) {
				if seen[u] {
					continue
				}
				seen[u] = true
				if known[u] {
					s = append(s, u)
				} else {
					missing = append(missing, u)
				}
			}
		}
		if len(missing) > 0 {
			return PackSample{}, invalid("not clients of the firm: %s", strings.Join(missing, ", "))
		}
		if len(s) == 0 {
			return PackSample{}, invalid("type the client codes the inspector gave")
		}
		if len(s) > 500 {
			return PackSample{}, invalid("at most 500 clients in a sample")
		}
		sort.Strings(s)
		return PackSample{Mode: "list", N: len(s), Clients: s}, nil
	}
	return PackSample{}, invalid("the sample is random, a list the inspector gave, or none")
}

// PackSamplePreview draws the sample the wizard shows before the build.
func (o *Ops) PackSamplePreview(ctx context.Context, tenant, actor, to string, r PackSampleReq) (PackSample, error) {
	if err := o.require(ctx, tenant, actor, "Making inspection packs", RoleCompliance); err != nil {
		return PackSample{}, err
	}
	if !validDate(to) {
		return PackSample{}, invalid("choose the period first")
	}
	return o.packSample(ctx, tenant, to, r)
}

// ---- making a pack ----

func packWatermark(title, ref string, at time.Time, who string, mask bool) string {
	what := title
	if ref != "" {
		what += " (" + ref + ")"
	}
	s := fmt.Sprintf("Prepared for %s, %s, by %s", what, at.Format("02-Jan-2006"), who)
	if mask {
		s += " · masked copy for outside advisers"
	}
	return s
}

// MakePack checks the request, draws the sample, records the pack and
// builds it in the background.
func (o *Ops) MakePack(ctx context.Context, tenant, actor string, r PackRequest) (Pack, error) {
	if err := o.require(ctx, tenant, actor, "Making inspection packs", RoleCompliance); err != nil {
		return Pack{}, err
	}
	return o.makePack(ctx, tenant, actor, r, nil, "")
}

func (o *Ops) makePack(ctx context.Context, tenant, actor string, r PackRequest, sample *PackSample, copyOf string) (Pack, error) {
	today := o.today()
	if !validDate(r.From) || !validDate(r.To) {
		return Pack{}, invalid("the period needs a start and an end date")
	}
	if r.From > r.To {
		return Pack{}, invalid("the period starts after it ends")
	}
	if r.To > today {
		return Pack{}, invalid("the period cannot end after today")
	}
	if daysBetween(r.From, r.To) > 3*366 {
		return Pack{}, invalid("a pack covers at most three years; make one pack per year for longer")
	}
	var t PackTemplate
	if r.Template != "" {
		var err error
		if t, err = o.packTemplate(ctx, tenant, r.Template); err != nil {
			return Pack{}, err
		}
	}
	items := r.Items
	if len(items) == 0 {
		items = t.Items
	}
	items, err := checkPackItems(items)
	if err != nil {
		return Pack{}, err
	}
	title := strings.TrimSpace(r.Title)
	if title == "" {
		title = firstOf(t.Name, "Inspection")
	}
	if len(title) > 120 || len(r.Ref) > 80 {
		return Pack{}, invalid("keep the inspection's name under 120 characters and its reference under 80")
	}
	var s PackSample
	if sample != nil {
		s = *sample
	} else if s, err = o.packSample(ctx, tenant, r.To, r.Sample); err != nil {
		return Pack{}, err
	}
	for _, it := range items {
		if it.Sampled && len(s.Clients) == 0 {
			return Pack{}, invalid("%q is for sampled clients: draw a sample, or take the item off", it.Title)
		}
	}
	n, err := o.next(ctx, tenant, "inspack", actor)
	if err != nil {
		return Pack{}, err
	}
	who := actor
	if u, err := o.user(ctx, tenant, actor); err == nil && u.Name != "" {
		who = u.Name
	}
	now := o.now()
	keep := o.packSettings(ctx, tenant).KeepDays
	p := Pack{ID: fmt.Sprintf("IP-%05d", n), Title: title, Ref: strings.TrimSpace(r.Ref), Template: t.ID, TemplateName: t.Name, Kind: t.Kind, Authority: t.Authority,
		From: r.From, To: r.To, Sample: s, Items: items, Mask: r.Mask, CopyOf: copyOf, Status: PackQueued, Progress: PackProgress{Total: len(items)},
		Results: []PackResult{}, By: actor, ByName: who, At: now.Format(time.RFC3339), ExpiresAt: now.AddDate(0, 0, keep).Format(time.RFC3339),
		Watermark: packWatermark(title, strings.TrimSpace(r.Ref), now, who, r.Mask)}
	what := "Made"
	if copyOf != "" {
		what = "Made as a masked copy of " + copyOf
	}
	p.Access = []PackAccess{{At: p.At, By: actor, What: what, Detail: fmt.Sprintf("%s to %s · %d items · sample %s", fmtDay(p.From), fmtDay(p.To), len(items), packSampleWords(s))}}
	if _, err := docs.Put(ctx, o.Docs, tenant, cPack, p.ID, p, 0, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "inspack.made", p.ID, map[string]any{"template": t.ID, "title": title, "from": p.From, "to": p.To, "items": len(items),
		"sample": s.Mode, "clients": len(s.Clients), "seed": s.Seed, "masked": r.Mask, "copy_of": copyOf})
	o.packStart(ctx, tenant, p.ID)
	return p, nil
}

func packSampleWords(s PackSample) string {
	switch s.Mode {
	case "random":
		return fmt.Sprintf("%d clients at random, seed %d", len(s.Clients), s.Seed)
	case "list":
		return fmt.Sprintf("%d clients the inspector named", len(s.Clients))
	}
	return "none"
}

// MaskedPackCopy builds a copy of a pack with personal details masked, for
// outside advisers: the same template, period, items and sample.
func (o *Ops) MaskedPackCopy(ctx context.Context, tenant, actor, id string) (Pack, error) {
	if err := o.require(ctx, tenant, actor, "Making inspection packs", RoleCompliance); err != nil {
		return Pack{}, err
	}
	src, _, err := docs.Get[Pack](ctx, o.Docs, tenant, cPack, id)
	if err != nil {
		return Pack{}, fmt.Errorf("%w: no pack %s", ErrNotFound, id)
	}
	if src.Mask {
		return Pack{}, invalid("this pack is already masked")
	}
	r := PackRequest{Template: src.Template, Title: src.Title, Ref: src.Ref, From: src.From, To: src.To, Items: src.Items, Mask: true}
	if r.Template != "" {
		if _, err := o.packTemplate(ctx, tenant, r.Template); err != nil {
			r.Template = "" // the firm's template was removed since: the items are kept on the pack
		}
	}
	sample := src.Sample
	p, err := o.makePack(ctx, tenant, actor, r, &sample, src.ID)
	if err != nil {
		return p, err
	}
	o.packAccess(ctx, tenant, src.ID, PackAccess{By: actor, What: "Masked copy made", Detail: p.ID})
	return p, nil
}

// ---- building ----

var packRunning sync.Map // running builds in this server: ops|tenant|id

func packRunKey(o *Ops, tenant, id string) string { return fmt.Sprintf("%p|%s|%s", o, tenant, id) }

func (o *Ops) packStart(ctx context.Context, tenant, id string) {
	key := packRunKey(o, tenant, id)
	packRunning.Store(key, true)
	go func() {
		defer packRunning.Delete(key)
		o.buildPack(context.WithoutCancel(ctx), tenant, id)
	}()
}

func (o *Ops) setPack(ctx context.Context, tenant, id string, fn func(*Pack)) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cPack, id, SystemActor, func(p *Pack) error { fn(p); return nil })
}

type packEntry struct {
	path string
	data []byte
}

// runPackSection runs one section, turning a panic into an error so one
// item cannot stop the pack.
func runPackSection(ctx context.Context, sec PackSection, b *PackBuild, it PackItem) (part PackPart, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("unexpected error in %s: %v", sec.Name, r)
		}
	}()
	return sec.Build(ctx, b, it)
}

func (o *Ops) buildPack(ctx context.Context, tenant, id string) {
	p, _, err := docs.Get[Pack](ctx, o.Docs, tenant, cPack, id)
	if err != nil {
		return
	}
	fail := func(err error) {
		o.setPack(ctx, tenant, id, func(x *Pack) { x.Status, x.Error = PackFailed, err.Error() })
		o.log(ctx, tenant, SystemActor, "inspack.failed", id, map[string]any{"error": err.Error()})
	}
	defer func() {
		if r := recover(); r != nil {
			fail(fmt.Errorf("unexpected error: %v", r))
		}
	}()
	o.setPack(ctx, tenant, id, func(x *Pack) { x.Status = PackBuilding })
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		fail(err)
		return
	}
	b := &PackBuild{O: o, Tenant: tenant, Actor: p.By, From: p.From, To: p.To, Clients: p.Sample.Clients, Mask: p.Mask, Watermark: p.Watermark, Set: set, Today: o.today()}
	if p.Mask {
		// Client details become tokens; staff names stay (who made and
		// checked a record is part of the evidence).
		b.masker = NewMasker(o.aiMaskLists(ctx, tenant), []string{AIMaskStaffName})
	}
	var entries []packEntry
	results := make([]PackResult, 0, len(p.Items))
	for i, it := range p.Items {
		o.setPack(ctx, tenant, id, func(x *Pack) { x.Progress = PackProgress{Done: i, Total: len(p.Items), Current: it.Title} })
		sec, _ := packSection(it.Section)
		res := PackResult{No: i + 1, Item: it.ID, Title: it.Title, Period: packPeriodWords(b, it), Source: sec.Name, ToConfirm: it.ToConfirm, Files: []PackFileInfo{}}
		if it.Sampled && len(b.Clients) > 0 {
			res.Period += fmt.Sprintf(" · %d sampled clients", len(b.Clients))
		}
		var part PackPart
		if sec.Build == nil {
			err = fmt.Errorf("this version of Ledgerline has no report %q", it.Section)
		} else {
			part, err = runPackSection(ctx, sec, b, it)
		}
		folder := fmt.Sprintf("%02d-%s", i+1, packSlug(it.Title))
		switch {
		case err != nil:
			res.Status, res.Why = "Not available", "Ledgerline could not make it: "+strings.TrimPrefix(err.Error(), "invalid: ")
		case part.NotAvailable != "":
			res.Status, res.Why = "Not available", part.NotAvailable
		default:
			res.Status, res.Records, res.Note = "Included", part.Records, part.Note
			for _, f := range part.Files {
				path := folder + "/" + packFileName(f.Name)
				entries = append(entries, packEntry{path, f.Data})
				res.Files = append(res.Files, PackFileInfo{Path: path, Records: f.Records, Bytes: len(f.Data), SHA256: sha256Hex(f.Data)})
			}
		}
		results = append(results, res)
	}
	o.setPack(ctx, tenant, id, func(x *Pack) {
		x.Progress = PackProgress{Done: len(p.Items), Total: len(p.Items), Current: "Index, cover sheet and checksums"}
	})
	data, count, err := o.packZip(ctx, b, p, results, entries)
	if err != nil {
		fail(err)
		return
	}
	name := fmt.Sprintf("%s %s %s to %s.zip", p.ID, packSlug(p.Title), p.From, p.To)
	if p.Mask {
		name = strings.TrimSuffix(name, ".zip") + " masked.zip"
	}
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: srcPack, Name: name, SHA256: sha256Hex(data), Size: int64(len(data)), Content: data, ReceivedAt: o.now()})
	if err != nil {
		fail(err)
		return
	}
	o.setPack(ctx, tenant, id, func(x *Pack) {
		x.Status, x.Results, x.FileID, x.FileName, x.Size, x.SHA256, x.Files, x.BuiltAt = PackReady, results, rf.ID, name, int64(len(data)), sha256Hex(data), count, o.now().Format(time.RFC3339)
		x.Progress = PackProgress{Done: len(p.Items), Total: len(p.Items)}
	})
	na := 0
	for _, r := range results {
		if r.Status != "Included" {
			na++
		}
	}
	o.log(ctx, tenant, SystemActor, "inspack.built", id, map[string]any{"sha256": sha256Hex(data), "bytes": len(data), "files": count, "not_available": na})
}

func packPeriodWords(b *PackBuild, it PackItem) string {
	switch it.Period {
	case "end":
		return "As at " + fmtDay(b.To)
	case "current":
		return "Current version, " + fmtDay(b.Today)
	}
	return fmtDay(b.From) + " to " + fmtDay(b.To)
}

// packSlug is a short folder or file name from a title.
func packSlug(s string) string {
	s = strings.Trim(reSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 48 {
		s = strings.TrimRight(s[:48], "-")
	}
	if s == "" {
		s = "item"
	}
	return s
}

var reFileName = regexp.MustCompile(`[^A-Za-z0-9._ ()/-]+`)

func packFileName(s string) string {
	s = strings.TrimSpace(reFileName.ReplaceAllString(strings.ReplaceAll(s, "\\", "/"), "_"))
	parts := strings.Split(s, "/")
	for i, p := range parts {
		if p == "" || p == "." || p == ".." {
			parts[i] = "_"
		}
	}
	return strings.Join(parts, "/")
}

// packZip writes the ZIP: the cover sheet, the index (HTML and CSV), the
// checksum list and the items' files.
func (o *Ops) packZip(ctx context.Context, b *PackBuild, p Pack, results []PackResult, entries []packEntry) ([]byte, int, error) {
	idx := packIndexCSV(b, p, results)
	idxHTML, err := packIndexHTML(b, p, results)
	if err != nil {
		return nil, 0, err
	}
	head := []packEntry{{"00-index.html", idxHTML}, {"00-index.csv", idx}}
	all := append(head, entries...)
	var sums bytes.Buffer
	sums.WriteString("# " + b.Watermark + "\n# SHA-256 of every file in the pack but this one and the cover sheet. Check with: sha256sum -c SHA256SUMS.txt\n")
	var infos []PackFileInfo
	for _, e := range all {
		h := sha256Hex(e.data)
		fmt.Fprintf(&sums, "%s  %s\n", h, e.path)
		infos = append(infos, PackFileInfo{Path: e.path, Bytes: len(e.data), SHA256: h})
	}
	sumsB := sums.Bytes()
	infos = append(infos, PackFileInfo{Path: "SHA256SUMS.txt", Bytes: len(sumsB), SHA256: sha256Hex(sumsB)})
	cover, err := o.packCoverHTML(ctx, b, p, results, infos)
	if err != nil {
		return nil, 0, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(path string, data []byte) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: path, Method: zip.Deflate, Modified: o.now()})
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	if err := put("00-cover.html", cover); err != nil {
		return nil, 0, err
	}
	for _, e := range all {
		if err := put(e.path, e.data); err != nil {
			return nil, 0, err
		}
	}
	if err := put("SHA256SUMS.txt", sumsB); err != nil {
		return nil, 0, err
	}
	if err := zw.Close(); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), len(all) + 2, nil
}

func packIndexCSV(b *PackBuild, p Pack, results []PackResult) []byte {
	rows := [][]string{{"No", "Item", "Period", "Produced by", "Status", "Why not available", "To confirm", "Note", "File", "Records", "Bytes", "SHA-256"}}
	for _, r := range results {
		if len(r.Files) == 0 {
			rows = append(rows, []string{fmt.Sprint(r.No), r.Title, r.Period, r.Source, r.Status, r.Why, r.ToConfirm, r.Note, "", fmt.Sprint(r.Records), "", ""})
		}
		for _, f := range r.Files {
			rows = append(rows, []string{fmt.Sprint(r.No), r.Title, r.Period, r.Source, r.Status, r.Why, r.ToConfirm, r.Note, f.Path, fmt.Sprint(f.Records), fmt.Sprint(f.Bytes), f.SHA256})
		}
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{b.Watermark})
	_ = w.WriteAll(rows)
	return buf.Bytes()
}

const packPageStyle = `body{font:12.5px/1.45 -apple-system,system-ui,"Segoe UI",sans-serif;color:#16201B;margin:0 auto;max-width:1120px;padding:52px 28px 32px}
.wm{position:fixed;top:0;left:0;right:0;z-index:2;background:#16201B;color:#fff;font-weight:600;font-size:12px;padding:8px 28px}
h1{font-size:19px;margin:6px 0 2px}h2{font-size:14px;margin:22px 0 6px}
.sub{color:#525D57;margin:0 0 14px}
.meta{display:grid;grid-template-columns:repeat(auto-fit,minmax(220px,1fr));gap:8px 18px;border-top:2px solid #16201B;border-bottom:1px solid #C9CDC3;padding:10px 0;margin-bottom:14px}
.meta div{display:flex;flex-direction:column}.meta small{color:#525D57;font-size:11px}.meta b{font-weight:600}
.sum{display:flex;gap:8px;flex-wrap:wrap;margin:10px 0}.sum span{border:1px solid #C9CDC3;border-radius:6px;padding:6px 10px}.sum b{font-size:15px;margin-right:4px}
table{width:100%;border-collapse:collapse}th,td{padding:5px 6px;border-bottom:1px solid #DADDD5;text-align:left;vertical-align:top}
th{font-size:10.5px;text-transform:uppercase;letter-spacing:.04em;background:#EAECE6}
td.r,th.r{text-align:right;white-space:nowrap}.mono{font-family:ui-monospace,Menlo,monospace;font-size:11px;word-break:break-all}
.st{display:inline-block;border-radius:10px;padding:1px 8px;font-size:11px;font-weight:600;white-space:nowrap}
.ok{background:#DCEFE3;color:#1D6B43}.na{background:#F6DEDA;color:#A3271D}.tc{background:#FBEFD5;color:#7A5300}
.why{color:#A3271D}.note{color:#525D57;font-size:11.5px}.files{margin:2px 0 0;padding:0;list-style:none}.files li{font-size:11.5px}
td.it{min-width:240px}td.fl{min-width:260px}.dir{color:#525D57}
.foot{margin-top:22px;font-size:11px;color:#525D57}
@media print{body{padding:34px 0 0;max-width:none}.wm{background:#fff;color:#16201B;border-bottom:1px solid #16201B;padding:4px 0}thead{display:table-header-group}tr{break-inside:avoid}@page{margin:14mm 12mm}}`

var packIndexTmpl = template.Must(template.New("idx").Funcs(template.FuncMap{"day": fmtDay, "n": func(i int) string { return formatCount(i) }}).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Index · {{.P.Title}}</title><style>` + packPageStyle + `</style></head><body>
<div class="wm">{{.W}}</div>
<h1>Index of the inspection pack</h1><p class="sub">{{.P.Title}}{{if .P.Ref}} · {{.P.Ref}}{{end}} · {{.Firm}}</p>
<div class="meta"><div><small>Pack</small><b>{{.P.ID}}{{if .P.Mask}} (masked copy){{end}}</b></div><div><small>Period</small><b>{{day .P.From}} to {{day .P.To}}</b></div>
<div><small>Template</small><b>{{if .P.TemplateName}}{{.P.TemplateName}}{{else}}Items chosen for this pack{{end}}</b></div><div><small>Sample</small><b>{{.Sample}}</b></div></div>
<div class="sum"><span><b>{{.Included}}</b> included</span><span><b>{{.NA}}</b> not available</span><span><b>{{.TC}}</b> to confirm</span><span><b>{{n .Records}}</b> records</span></div>
<table><thead><tr><th>No.</th><th>Item</th><th>Period</th><th>Produced by</th><th>Files</th><th class="r">Records</th><th>Status</th></tr></thead><tbody>
{{range .R}}{{$f := .Folder}}{{with .R}}<tr><td>{{.No}}</td><td class="it"><b>{{.Title}}</b>{{if .Note}}<div class="note">{{.Note}}</div>{{end}}{{if .ToConfirm}}<div class="note"><span class="st tc">To confirm</span> {{.ToConfirm}}</div>{{end}}</td>
<td>{{.Period}}</td><td>{{.Source}}</td>{{end}}<td class="fl">{{if .Lines}}<div class="mono dir">{{$f}}/</div><ul class="files">{{range .Lines}}<li><span class="mono">{{.Name}}</span>{{if gt .Count 1}} · {{.Count}} files{{end}} · {{n .Records}}</li>{{end}}</ul>{{else}}—{{end}}</td>{{with .R}}
<td class="r">{{if eq .Status "Included"}}{{n .Records}}{{else}}—{{end}}</td>
<td>{{if eq .Status "Included"}}<span class="st ok">Included</span>{{else}}<span class="st na">Not available</span><div class="why">{{.Why}}</div>{{end}}</td></tr>{{end}}
{{end}}</tbody></table>
<p class="foot">Files Ledgerline wrote carry the line “{{.W}}” as the first row (CSV) or across the top of each page (HTML, PDF). Copies of uploaded documents are as they were uploaded. The cover sheet (00-cover.html) lists the SHA-256 of every file; SHA256SUMS.txt is the same list for checking with sha256sum.</p>
</body></html>`))

func formatCount(n int) string {
	s := fmt.Sprint(n)
	if n < 1000 {
		return s
	}
	var out []string
	for len(s) > 3 {
		out = append([]string{s[len(s)-3:]}, out...)
		s = s[:len(s)-3]
	}
	return strings.Join(append([]string{s}, out...), ",")
}

// packIndexRow is an item as the index page shows it: its folder once,
// the files in it, and a line for each subfolder (contract notes of 30
// clients are "notes/ · 68 files"). The CSV index lists every file.
type packIndexRow struct {
	R      PackResult
	Folder string
	Lines  []packIndexLine
}

type packIndexLine struct {
	Name           string
	Count, Records int
}

func packIndexRowOf(r PackResult) packIndexRow {
	row := packIndexRow{R: r}
	at := map[string]int{}
	for _, f := range r.Files {
		folder, rest, _ := strings.Cut(f.Path, "/")
		row.Folder = folder
		name := rest
		if sub, _, ok := strings.Cut(rest, "/"); ok {
			name = sub + "/"
		}
		i, ok := at[name]
		if !ok {
			i = len(row.Lines)
			at[name] = i
			row.Lines = append(row.Lines, packIndexLine{Name: name})
		}
		row.Lines[i].Count++
		row.Lines[i].Records += f.Records
	}
	return row
}

func packIndexHTML(b *PackBuild, p Pack, results []PackResult) ([]byte, error) {
	inc, na, tc, rec := 0, 0, 0, 0
	for _, r := range results {
		if r.Status == "Included" {
			inc++
			rec += r.Records
		} else {
			na++
		}
		if r.ToConfirm != "" {
			tc++
		}
	}
	rows := make([]packIndexRow, len(results))
	for i, r := range results {
		rows[i] = packIndexRowOf(r)
	}
	var buf bytes.Buffer
	err := packIndexTmpl.Execute(&buf, map[string]any{"P": p, "R": rows, "W": b.Watermark, "Firm": b.Set.Firm, "Sample": packSampleWords(p.Sample), "Included": inc, "NA": na, "TC": tc, "Records": rec})
	return buf.Bytes(), err
}

var packCoverTmpl = template.Must(template.New("cover").Funcs(template.FuncMap{"day": fmtDay, "n": func(i int) string { return formatCount(i) }}).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Cover sheet · {{.P.Title}}</title><style>` + packPageStyle + `</style></head><body>
<div class="wm">{{.W}}</div>
<h1>{{.Firm}}</h1><p class="sub">{{.Reg}}</p>
<h2>Inspection pack {{.P.ID}}{{if .P.Mask}} · masked copy{{end}}</h2>
<div class="meta">
<div><small>Inspection</small><b>{{.P.Title}}</b></div>
{{if .P.Ref}}<div><small>Reference</small><b>{{.P.Ref}}</b></div>{{end}}
<div><small>Template</small><b>{{if .P.TemplateName}}{{.P.TemplateName}}{{else}}Items chosen for this pack{{end}}</b>{{if .P.Authority}}<small>{{.P.Authority}}</small>{{end}}</div>
<div><small>Period</small><b>{{day .P.From}} to {{day .P.To}}</b></div>
<div><small>Prepared by</small><b>{{.P.ByName}}</b><small>{{.P.By}}</small></div>
<div><small>Prepared at</small><b>{{.At}}</b><small>request {{.Asked}}</small></div>
</div>
<h2>Sample of clients</h2>
<div class="meta">
<div><small>How chosen</small><b>{{.SampleMode}}</b></div>
{{if eq .P.Sample.Mode "random"}}<div><small>Seed</small><b class="mono">{{.P.Sample.Seed}}</b></div>
<div><small>Drawn from</small><b>{{n .P.Sample.Population}} clients</b><small>{{.P.Sample.PopRule}}</small></div>
<div><small>SHA-256 of the client codes drawn from</small><span class="mono">{{.P.Sample.PopSHA256}}</span></div>{{end}}
<div><small>Clients</small><b>{{len .P.Sample.Clients}}</b></div>
</div>
{{if .Clients}}<p class="mono">{{.Clients}}</p>{{end}}
{{if eq .P.Sample.Mode "random"}}<p class="note">The same seed, period end and client list draw the same clients again: Ledgerline takes the clients in client code order and shuffles them with the SplitMix64 generator started from the seed (Fisher–Yates), keeping the first {{len .P.Sample.Clients}}.</p>{{end}}
{{if .P.CopyOf}}<p class="note">Masked copy of {{.P.CopyOf}}, built again from the same template, period, items and sample. Client names, codes, PANs, Aadhaar-like numbers, phone numbers, email addresses and account numbers are replaced by tokens such as [CLIENT-3], the same token for the same value throughout the pack; dates of birth show the year only. KYC document images are not included.</p>{{end}}
<h2>Contents</h2>
<p>{{.Included}} of {{len .R}} items included, {{.NA}} not available (the index says why), {{n .Files}} files.</p>
<h2>Checksums (SHA-256)</h2>
<table><thead><tr><th>File</th><th class="r">Bytes</th><th>SHA-256</th></tr></thead><tbody>
{{range .Infos}}<tr><td class="mono">{{.Path}}</td><td class="r">{{n .Bytes}}</td><td class="mono">{{.SHA256}}</td></tr>{{end}}
</tbody></table>
<p class="foot">This cover sheet is the one file not in the list. The SHA-256 of the whole ZIP is kept with the pack in Ledgerline (Compliance → Inspection packs) and in its sealed audit trail.</p>
</body></html>`))

func (o *Ops) packCoverHTML(ctx context.Context, b *PackBuild, p Pack, results []PackResult, infos []PackFileInfo) ([]byte, error) {
	set := b.Set
	reg := "SEBI registration " + firstOf(set.SEBIReg, "—")
	var ms []string
	for _, k := range sortedKeys(set.MemberCodes) {
		ms = append(ms, k+" "+set.MemberCodes[k])
	}
	if len(ms) > 0 {
		reg += " · Member codes " + strings.Join(ms, ", ")
	}
	inc, na := 0, 0
	for _, r := range results {
		if r.Status == "Included" {
			inc++
		} else {
			na++
		}
	}
	mode := map[string]string{"random": "At random", "list": "The clients the inspector named", "none": "No sample"}[p.Sample.Mode]
	clients := strings.Join(p.Sample.Clients, ", ")
	if p.Mask {
		clients = b.Text(clients)
	}
	asked, _ := time.Parse(time.RFC3339, p.At)
	var buf bytes.Buffer
	err := packCoverTmpl.Execute(&buf, map[string]any{"P": p, "R": results, "W": b.Watermark, "Firm": firstOf(set.Firm, "The firm"), "Reg": reg,
		"At": o.now().Format("02-Jan-2006 15:04 IST"), "Asked": asked.In(ist).Format("02-Jan-2006 15:04"), "SampleMode": mode, "Clients": clients,
		"Included": inc, "NA": na, "Files": len(infos) + 1, "Infos": infos})
	return buf.Bytes(), err
}

// ---- listing, downloading and expiry ----

// PacksScreen is the Inspection packs screen.
type PacksScreen struct {
	Packs     []Pack            `json:"packs"`
	Templates []PackTemplate    `json:"templates"`
	Sections  []PackSectionInfo `json:"sections"`
	Ready     map[string]string `json:"ready"` // section|param -> why it cannot be produced now ("" or absent: it can)
	Documents []PackDoc         `json:"documents"`
	DocKinds  []string          `json:"doc_kinds"`
	Periods   []PackPeriodKind  `json:"periods"`
	Settings  PackSettings      `json:"settings"`
	Today     string            `json:"today"`
}

// InspectionPacks is the screen: packs newest first, templates, sections
// and documents.
func (o *Ops) InspectionPacks(ctx context.Context, tenant, actor string) (PacksScreen, error) {
	if err := o.require(ctx, tenant, actor, "Viewing inspection packs", RoleCompliance); err != nil {
		return PacksScreen{}, err
	}
	ps, err := docs.List[Pack](ctx, o.Docs, tenant, cPack, "")
	if err != nil {
		return PacksScreen{}, err
	}
	for i := range ps {
		o.packCheck(ctx, tenant, &ps[i])
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].ID > ps[j].ID })
	if ps == nil {
		ps = []Pack{}
	}
	ts, err := o.PackTemplates(ctx, tenant)
	if err != nil {
		return PacksScreen{}, err
	}
	ds, err := o.PackDocs(ctx, tenant)
	if err != nil {
		return PacksScreen{}, err
	}
	v := PacksScreen{Packs: ps, Templates: ts, Sections: PackSections(), Ready: map[string]string{}, Documents: ds, DocKinds: PackDocKinds, Periods: PackPeriods, Settings: o.packSettings(ctx, tenant), Today: o.today()}
	seen := map[string]bool{}
	for _, t := range ts {
		for _, it := range t.Items {
			k := it.Section + "|" + it.Param
			if seen[k] {
				continue
			}
			seen[k] = true
			if s, ok := packSection(it.Section); ok && s.Ready != nil {
				if why := s.Ready(ctx, o, tenant, it); why != "" {
					v.Ready[k] = why
				}
			}
		}
	}
	return v, nil
}

// PackReadiness says, for the review step, why each item could not be
// produced now ("" when it can).
func (o *Ops) PackReadiness(ctx context.Context, tenant, actor string, items []PackItem) (map[string]string, error) {
	if err := o.require(ctx, tenant, actor, "Making inspection packs", RoleCompliance); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, it := range items {
		s, ok := packSection(it.Section)
		switch {
		case !ok:
			out[it.Section+"|"+it.Param] = "No such report in Ledgerline"
		case s.Ready != nil:
			if why := s.Ready(ctx, o, tenant, it); why != "" {
				out[it.Section+"|"+it.Param] = why
			}
		}
	}
	return out, nil
}

// InspectionPack is one pack with its progress.
func (o *Ops) InspectionPack(ctx context.Context, tenant, actor, id string) (Pack, error) {
	if err := o.require(ctx, tenant, actor, "Viewing inspection packs", RoleCompliance); err != nil {
		return Pack{}, err
	}
	p, _, err := docs.Get[Pack](ctx, o.Docs, tenant, cPack, id)
	if err != nil {
		return p, fmt.Errorf("%w: no pack %s", ErrNotFound, id)
	}
	o.packCheck(ctx, tenant, &p)
	return p, nil
}

// packCheck expires a pack past its keep date and marks a build this
// server is no longer running as interrupted.
func (o *Ops) packCheck(ctx context.Context, tenant string, p *Pack) {
	now := o.now()
	if !p.Expired && p.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, p.ExpiresAt); err == nil && !now.Before(t) {
			*p = o.expirePack(ctx, tenant, SystemActor, p.ID, fmt.Sprintf("kept for %d days", daysBetween(p.At[:10], p.ExpiresAt[:10])))
			return
		}
	}
	if (p.Status == PackQueued || p.Status == PackBuilding) && !packRunningNow(o, tenant, p.ID) {
		// Wait a moment: the build may be just starting.
		if at, err := time.Parse(time.RFC3339, p.At); err == nil && now.Sub(at) > time.Minute {
			o.setPack(ctx, tenant, p.ID, func(x *Pack) {
				if x.Status == PackQueued || x.Status == PackBuilding {
					x.Status, x.Error = PackInterrupted, "The server stopped while the pack was being built. Make it again."
				}
			})
			p.Status, p.Error = PackInterrupted, "The server stopped while the pack was being built. Make it again."
		}
	}
}

func packRunningNow(o *Ops, tenant, id string) bool {
	_, ok := packRunning.Load(packRunKey(o, tenant, id))
	return ok
}

func (o *Ops) expirePack(ctx context.Context, tenant, actor, id, why string) Pack {
	var changed bool
	p, err := docs.Update(ctx, o.Docs, tenant, cPack, id, actor, func(x *Pack) error {
		changed = false
		if x.Expired {
			return nil
		}
		now := o.now().Format(time.RFC3339)
		x.Expired, x.ExpiredAt, x.ExpiredBy, x.ExpiredWhy, changed = true, now, actor, why, true
		what := "Removed from the download list"
		if actor == SystemActor {
			what = "Expired"
		}
		x.Access = append(x.Access, PackAccess{At: now, By: actor, What: what, Detail: why})
		return nil
	})
	if err == nil && changed {
		o.log(ctx, tenant, actor, "inspack.expired", id, map[string]any{"why": why, "file": p.FileID})
	}
	return p
}

// ExpirePack takes a pack off the download list now. The record stays.
func (o *Ops) ExpirePack(ctx context.Context, tenant, actor, id, why string) (Pack, error) {
	if err := o.require(ctx, tenant, actor, "Making inspection packs", RoleCompliance); err != nil {
		return Pack{}, err
	}
	if _, _, err := docs.Get[Pack](ctx, o.Docs, tenant, cPack, id); err != nil {
		return Pack{}, fmt.Errorf("%w: no pack %s", ErrNotFound, id)
	}
	why = strings.TrimSpace(why)
	if why == "" {
		why = "no longer needed"
	}
	return o.expirePack(ctx, tenant, actor, id, why), nil
}

func (o *Ops) packAccess(ctx context.Context, tenant, id string, a PackAccess) {
	a.At = o.now().Format(time.RFC3339)
	o.setPack(ctx, tenant, id, func(x *Pack) { x.Access = append(x.Access, a) })
}

// packZipFor checks a pack may be served and returns its ZIP.
func (o *Ops) packZipFor(ctx context.Context, tenant, actor, id string) (Pack, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Downloading inspection packs", RoleCompliance); err != nil {
		return Pack{}, nil, err
	}
	p, _, err := docs.Get[Pack](ctx, o.Docs, tenant, cPack, id)
	if err != nil {
		return p, nil, fmt.Errorf("%w: no pack %s", ErrNotFound, id)
	}
	o.packCheck(ctx, tenant, &p)
	if p.Expired {
		return p, nil, fmt.Errorf("%w: pack %s left the download list on %s (%s); its record stays, the files are no longer served. Make the pack again", ErrState, id, fmtDay(p.ExpiredAt[:10]), p.ExpiredWhy)
	}
	if p.Status != PackReady {
		return p, nil, fmt.Errorf("%w: pack %s is %s", ErrState, id, strings.ToLower(p.Status))
	}
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, p.FileID)
	if err != nil {
		return p, nil, err
	}
	if rf.Source != srcPack || sha256Hex(rf.Content) != p.SHA256 {
		return p, nil, fmt.Errorf("the stored pack %s does not match its checksum; make it again", id)
	}
	return p, rf.Content, nil
}

// PackFile is the ZIP to download. The download is recorded in the sealed
// audit trail and on the pack.
func (o *Ops) PackFile(ctx context.Context, tenant, actor, id string) (Pack, []byte, error) {
	p, data, err := o.packZipFor(ctx, tenant, actor, id)
	if err != nil {
		return p, nil, err
	}
	o.log(ctx, tenant, actor, "inspack.downloaded", id, map[string]any{"sha256": p.SHA256, "bytes": len(data), "masked": p.Mask})
	o.packAccess(ctx, tenant, id, PackAccess{By: actor, What: "Downloaded", Detail: p.FileName})
	return p, data, nil
}

// PackIndex is the pack's index page, read from the ZIP, to look at on
// screen. Opening it is recorded like a download.
func (o *Ops) PackIndex(ctx context.Context, tenant, actor, id string) ([]byte, error) {
	p, data, err := o.packZipFor(ctx, tenant, actor, id)
	if err != nil {
		return nil, err
	}
	b, err := zipFile(data, "00-index.html")
	if err != nil {
		return nil, err
	}
	o.log(ctx, tenant, actor, "inspack.index_viewed", id, map[string]any{"sha256": p.SHA256})
	o.packAccess(ctx, tenant, id, PackAccess{By: actor, What: "Opened the index"})
	return b, nil
}

func zipFile(data []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("%w: %s is not in the pack", ErrNotFound, name)
}

// ---- policy and evidence documents ----

// PackDocs lists the documents kept for inspections, newest first.
func (o *Ops) PackDocs(ctx context.Context, tenant string) ([]PackDoc, error) {
	ds, err := docs.List[PackDoc](ctx, o.Docs, tenant, cPackDoc, "")
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].ID > ds[j].ID })
	if ds == nil {
		ds = []PackDoc{}
	}
	return ds, err
}

var packDocTypes = map[string]string{"application/pdf": ".pdf", "image/png": ".png", "image/jpeg": ".jpg", "application/zip": ".docx", "text/plain; charset=utf-8": ".txt"}

// AddPackDoc keeps a policy, audit report or other evidence document for
// inspection packs.
func (o *Ops) AddPackDoc(ctx context.Context, tenant, actor, kind, title, version, name string, data []byte) (PackDoc, error) {
	if err := o.require(ctx, tenant, actor, "Adding inspection documents", RoleCompliance); err != nil {
		return PackDoc{}, err
	}
	if !contains(PackDocKinds, kind) {
		return PackDoc{}, invalid("the kind of document must be one of %s", strings.Join(PackDocKinds, ", "))
	}
	title = strings.TrimSpace(title)
	if title == "" || len(title) > 160 {
		return PackDoc{}, invalid("give the document a title (up to 160 characters)")
	}
	if len(data) == 0 || len(data) > MaxPackDoc {
		return PackDoc{}, invalid("the file must be between 1 byte and 20 MB")
	}
	ct := http.DetectContentType(data)
	if _, ok := packDocTypes[ct]; !ok {
		return PackDoc{}, invalid("upload a PDF, a picture (PNG or JPEG), a Word or Excel file, or plain text (this looks like %s)", ct)
	}
	name = strings.TrimSpace(strings.NewReplacer("/", "_", "\\", "_").Replace(name))
	if name == "" {
		name = packSlug(title) + packDocTypes[ct]
	}
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: srcPackDoc, Name: name, SHA256: sha256Hex(data), Size: int64(len(data)), Content: data, ReceivedAt: o.now()})
	if err != nil {
		return PackDoc{}, err
	}
	n, err := o.next(ctx, tenant, "inspackdoc", actor)
	if err != nil {
		return PackDoc{}, err
	}
	d := PackDoc{ID: fmt.Sprintf("ID-%05d", n), Kind: kind, Title: title, Version: strings.TrimSpace(version), Name: name, ContentType: ct, Size: int64(len(data)), SHA256: rf.SHA256, FileID: rf.ID, By: actor, At: o.now().Format(time.RFC3339)}
	if _, err := docs.Put(ctx, o.Docs, tenant, cPackDoc, d.ID, d, 0, actor); err != nil {
		return d, err
	}
	o.log(ctx, tenant, actor, "inspack.document.added", d.ID, map[string]any{"kind": kind, "title": title, "sha256": d.SHA256})
	return d, nil
}

// PackDocFile returns a document's bytes; the download is recorded.
func (o *Ops) PackDocFile(ctx context.Context, tenant, actor, id string) (PackDoc, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Viewing inspection packs", RoleCompliance); err != nil {
		return PackDoc{}, nil, err
	}
	d, _, err := docs.Get[PackDoc](ctx, o.Docs, tenant, cPackDoc, id)
	if err != nil {
		return d, nil, fmt.Errorf("%w: no document %s", ErrNotFound, id)
	}
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, d.FileID)
	if err != nil {
		return d, nil, err
	}
	o.log(ctx, tenant, actor, "inspack.document.downloaded", id, map[string]any{"sha256": d.SHA256})
	return d, rf.Content, nil
}

// RetirePackDoc takes a document out of future packs (a newer version
// replaced it). The record and the file stay.
func (o *Ops) RetirePackDoc(ctx context.Context, tenant, actor, id, why string) (PackDoc, error) {
	if err := o.require(ctx, tenant, actor, "Adding inspection documents", RoleCompliance); err != nil {
		return PackDoc{}, err
	}
	why = strings.TrimSpace(why)
	if why == "" {
		return PackDoc{}, invalid("say why the document is retired (for example: replaced by the 2027 version)")
	}
	d, err := docs.Update(ctx, o.Docs, tenant, cPackDoc, id, actor, func(d *PackDoc) error {
		if d.Retired {
			return fmt.Errorf("%w: the document is already retired", ErrState)
		}
		d.Retired, d.RetiredBy, d.RetiredAt, d.RetiredWhy = true, actor, o.now().Format(time.RFC3339), why
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return d, fmt.Errorf("%w: no document %s", ErrNotFound, id)
	}
	if err == nil {
		o.log(ctx, tenant, actor, "inspack.document.retired", id, map[string]any{"why": why})
	}
	return d, err
}
