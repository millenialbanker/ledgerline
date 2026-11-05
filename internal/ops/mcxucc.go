package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// ---- MCX UCC: the upload file, MCX's answer and the State-City master ----
//
// The flow (MCX/TECH/023/2022, MCX UCC File Formats v1.0):
//   - Ledgerline writes MCX_DDMMYYYY_UCC<member>.Mnn with every client that
//     is new or changed at MCX and has no problem (mcxuccmap.go); nn is the
//     day's batch number, 01 to 99 in series. Each file written is kept as
//     a batch: its clients and the hash of each one's record.
//   - Staff upload it on MCX's portal and mark it uploaded: its clients are
//     Sent. MCX answers with MCX_DDMMYYYY_UCC<member>.Enn (section 1.2): one
//     line per client code, with an error code (section 1.3) or blank.
//     Loading it on Exchange files marks each client Accepted or Rejected;
//     a rejected client becomes a work item and goes again once corrected.
//     A file refused as a whole comes back as .Mnn.F<error>: its clients
//     go in the next batch.
//   - Clients already active when Ledgerline first lists them for MCX are
//     taken as registered at MCX before (as for BSE); their later changes
//     go as modifications. Registrations share the collection of BSE's
//     (key "MCX|<UCC>").
//
// Choices where the specification is not explicit:
//   - The file has no add or modify flag; a client code MCX holds is a
//     modification. Add and Modify are shown for staff only.
//   - Batch numbers run per file date: the next is one after the highest
//     batch of that date that was uploaded or answered, so a file
//     downloaded again before upload keeps its number.
//   - Commas inside values become spaces (the specification does not say a
//     field may be quoted).

const (
	cMCXUCCBatch     = "mcxuccbatch"  // key: YYYY-MM-DD|NN
	cMCXStateCity    = "mcxstatecity" // one document: "master"
	mcxStateCitySpec = "mcx-state-city-master"
)

// MCXUCCBatchDoc is one UCC file written for MCX.
type MCXUCCBatchDoc struct {
	ID         string                       `json:"id"`
	Date       string                       `json:"date"`
	Batch      int                          `json:"batch"`
	File       string                       `json:"file"`
	UCCs       []string                     `json:"uccs"`
	Hashes     map[string]string            `json:"hashes"`
	Actions    map[string]string            `json:"actions,omitempty"`
	Summaries  map[string]map[string]string `json:"summaries,omitempty"`
	WrittenAt  string                       `json:"written_at"`
	WrittenBy  string                       `json:"written_by"`
	UploadedAt string                       `json:"uploaded_at,omitempty"`
	UploadedBy string                       `json:"uploaded_by,omitempty"`
	ResponseAt string                       `json:"response_at,omitempty"`
	Response   string                       `json:"response_file,omitempty"`
	Accepted   int                          `json:"accepted,omitempty"`
	Rejected   int                          `json:"rejected,omitempty"`
	FileError  string                       `json:"file_error,omitempty"`
}

func mcxBatchKey(date string, n int) string { return fmt.Sprintf("%s|%02d", date, n) }
func mcxRegKey(ucc string) string           { return "MCX|" + ucc }

// One MCX UCC change at a time across the server.
var mcxUCCMu sync.Mutex

// ---- the State-City master ----

// MCXStateCityMaster is MCX's State-City Code master as last loaded.
type MCXStateCityMaster struct {
	File     string            `json:"file"`
	LoadedAt string            `json:"loaded_at"`
	LoadedBy string            `json:"loaded_by"`
	Columns  []string          `json:"columns"` // the headers read as state, city and code
	Codes    map[string]string `json:"codes"`   // STATE|CITY (normalised) → code
	// CityOnly is by city alone, for a master without a state column; a
	// city with different codes in different states is left out.
	CityOnly map[string]string `json:"city_only,omitempty"`
	Count    int               `json:"count"`
}

// mcxPlace normalises a place name: capitals and digits only, & as AND.
func mcxPlace(s string) string {
	s = strings.ToUpper(strings.ReplaceAll(s, "&", " AND "))
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
}

// mcxStateKey is a state's name as BSE's state list spells it, normalised.
func mcxStateKey(s string) string {
	u := strings.ToUpper(strings.Join(strings.Fields(strings.ReplaceAll(s, "&", " AND ")), " "))
	if st, ok := bseStates[u]; ok {
		u = st
	}
	return mcxPlace(u)
}

// Lookup finds the code of a city in a state.
func (m *MCXStateCityMaster) Lookup(state, city string) (string, bool) {
	if m == nil {
		return "", false
	}
	if c, ok := m.Codes[mcxStateKey(state)+"|"+mcxPlace(city)]; ok && c != "" {
		return c, true
	}
	if c, ok := m.CityOnly[mcxPlace(city)]; ok && c != "" && len(m.Codes) == 0 {
		return c, true
	}
	return "", false
}

// parseMCXStateCity reads the master. Its layout is not published with the
// UCC file formats, so it is read leniently: comma, pipe, tab or semicolon
// separated; the header row is the first of the first ten rows naming a
// city column and a code column; the state column is one whose header has
// "state" without "code"; the code column prefers a header with "state",
// "city" and "code", then "city" and "code", then "code" alone. Other
// columns are ignored.
func parseMCXStateCity(data []byte) (MCXStateCityMaster, error) {
	m := MCXStateCityMaster{Codes: map[string]string{}, CityOnly: map[string]string{}}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	first := string(data)
	if i := strings.IndexAny(first, "\r\n"); i >= 0 {
		first = first[:i]
	}
	delim, most := ',', -1
	for _, d := range []rune{',', '|', '\t', ';'} {
		if n := strings.Count(first, string(d)); n > most {
			delim, most = d, n
		}
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma, r.FieldsPerRecord, r.LazyQuotes, r.TrimLeadingSpace = delim, -1, true, true
	rows, err := r.ReadAll()
	if err != nil {
		return m, invalid("the State-City master could not be read: %v", err)
	}
	norm := func(h string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) {
				return unicode.ToLower(r)
			}
			return -1
		}, h)
	}
	hdr, st, ci, co := -1, -1, -1, -1
	for i := 0; i < len(rows) && i < 10 && hdr < 0; i++ {
		s, c, code, rank := -1, -1, -1, 0
		for j, h := range rows[i] {
			n := norm(h)
			hasCode := strings.Contains(n, "code") || n == "cd"
			switch {
			case hasCode && strings.Contains(n, "state") && strings.Contains(n, "city"):
				code, rank = j, 3
			case hasCode && strings.Contains(n, "city") && rank < 2:
				code, rank = j, 2
			case n == "code" && rank < 1:
				code, rank = j, 1
			case !hasCode && strings.Contains(n, "city") && c < 0:
				c = j
			case !hasCode && strings.Contains(n, "state") && s < 0:
				s = j
			}
		}
		if c >= 0 && code >= 0 {
			hdr, st, ci, co = i, s, c, code
		}
	}
	if hdr < 0 {
		return m, invalid("the State-City master has no header row naming a city column and a code column (e.g. State, City, State-City Code)")
	}
	cell := func(row []string, j int) string {
		if j < 0 || j >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[j])
	}
	m.Columns = []string{cell(rows[hdr], st), cell(rows[hdr], ci), cell(rows[hdr], co)}
	conflict := map[string]bool{}
	for _, row := range rows[hdr+1:] {
		city, code := cell(row, ci), cell(row, co)
		if mcxPlace(city) == "" || code == "" {
			continue
		}
		if st >= 0 {
			m.Codes[mcxStateKey(cell(row, st))+"|"+mcxPlace(city)] = code
		}
		k := mcxPlace(city)
		if old, ok := m.CityOnly[k]; ok && old != code {
			conflict[k] = true
		}
		m.CityOnly[k] = code
		m.Count++
	}
	for k := range conflict {
		delete(m.CityOnly, k)
	}
	if m.Count == 0 {
		return m, invalid("the State-City master has no rows with a city and a code")
	}
	return m, nil
}

// loadMCXStateCity keeps a loaded master (replacing the previous one).
func (o *Ops) loadMCXStateCity(ctx context.Context, tenant, actor, name string, data []byte) (int, error) {
	m, err := parseMCXStateCity(data)
	if err != nil {
		return 0, err
	}
	m.File, m.LoadedAt, m.LoadedBy = name, o.now().Format(time.RFC3339), actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cMCXStateCity, "master", m, actor); err != nil {
		return 0, err
	}
	o.log(ctx, tenant, actor, "mcxucc.statecity_loaded", name, map[string]any{"rows": m.Count, "columns": m.Columns})
	return m.Count, nil
}

// ---- the clients ----

func (o *Ops) mcxUCCLoadSources(ctx context.Context, tenant string, set Settings, cs []Client) mcxUCCSources {
	s := mcxUCCSources{bse: o.bseUCCLoadSources(ctx, tenant, set), ucc: o.uccSources(ctx, tenant), mobiles: map[string][]string{}, emails: map[string][]string{}}
	if m, _, err := docs.Get[MCXStateCityMaster](ctx, o.Docs, tenant, cMCXStateCity, "master"); err == nil {
		s.cities = &m
	}
	for _, c := range cs {
		if c.Status == "Closed" || c.Status == "Rejected" {
			continue
		}
		if m := strings.TrimSpace(c.Mobile); m != "" {
			s.mobiles[m] = append(s.mobiles[m], c.UCC)
		}
		if e := strings.ToLower(strings.TrimSpace(c.Email)); e != "" {
			s.emails[e] = append(s.emails[e], c.UCC)
		}
	}
	return s
}

// mcxUCCItem is one client and what to do about it at MCX.
type mcxUCCItem struct {
	c       Client
	reg     BSEUCCReg
	exists  bool
	rec     mcxUCCRecord
	hash    string
	pending bool
}

// mcxUCCItems maps every client that is or should be registered at MCX:
// those with the commodity segment. Active clients with no registration
// are left out and returned (baseline: registered before Ledgerline).
func (o *Ops) mcxUCCItems(ctx context.Context, tenant, date string) ([]mcxUCCItem, []Client, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return nil, nil, err
	}
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, nil, err
	}
	regs, err := docs.List[BSEUCCReg](ctx, o.Docs, tenant, cBSEUCCReg, "MCX|")
	if err != nil {
		return nil, nil, err
	}
	byUCC := map[string]BSEUCCReg{}
	for _, r := range regs {
		byUCC[r.UCC] = r
	}
	src := o.mcxUCCLoadSources(ctx, tenant, set, cs)
	var out []mcxUCCItem
	var baseline []Client
	for _, c := range cs {
		r, ok := byUCC[c.UCC]
		if !ok {
			com := c.SegmentStatus("COM")
			if com == "Not registered" || com == SegClosed {
				continue // nothing at MCX, nothing to close there
			}
			switch c.Status {
			case "UCC upload":
				r = BSEUCCReg{Exchange: "MCX", UCC: c.UCC, Action: "Add", Status: UCCNotSent}
			case "Active":
				baseline = append(baseline, c)
				continue
			default:
				continue
			}
		}
		r.Action = "Add"
		if r.AcceptedHash != "" {
			r.Action = "Modify"
		}
		r.Name = c.Name
		it := mcxUCCItem{c: c, reg: r, exists: ok}
		it.rec = mcxUCCMap(c, src, r.Summary, date)
		it.hash = it.rec.Hash()
		switch r.Status {
		case UCCAccepted:
			it.pending = it.hash != r.AcceptedHash
		case UCCRejected, UCCSent:
			it.pending = it.hash != r.Hash
		default: // Not sent, Needs fixing
			it.pending = true
		}
		if c.Status == "Rejected" && r.AcceptedHash == "" {
			it.pending = false
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].c.UCC < out[j].c.UCC })
	return out, baseline, nil
}

// mcxUCCBaseline records clients already active as registered at MCX before.
func (o *Ops) mcxUCCBaseline(ctx context.Context, tenant, date string, cs []Client) {
	if len(cs) == 0 {
		return
	}
	set, _ := o.Settings(ctx, tenant)
	all, _ := o.Clients(ctx, tenant)
	src := o.mcxUCCLoadSources(ctx, tenant, set, all)
	at := o.now().Format(time.RFC3339)
	for _, c := range cs {
		rec := mcxUCCMap(c, src, nil, date)
		h := rec.Hash()
		r := BSEUCCReg{Exchange: "MCX", UCC: c.UCC, Name: c.Name, Action: "Modify", Via: "Before Ledgerline", Hash: h, AcceptedHash: h, Summary: rec.Summary}
		r.event(at, SystemActor, UCCAccepted, "Registered at MCX before Ledgerline wrote its UCC file", "")
		if _, err := docs.Put(ctx, o.Docs, tenant, cBSEUCCReg, mcxRegKey(c.UCC), r, 0, SystemActor); err != nil && !errors.Is(err, docs.ErrStale) {
			continue
		}
	}
	o.log(ctx, tenant, SystemActor, "mcxucc.baseline", "", map[string]any{"clients": len(cs)})
}

func (o *Ops) mcxUCCSaveReg(ctx context.Context, tenant string, r BSEUCCReg) {
	_ = docs.Upsert(ctx, o.Docs, tenant, cBSEUCCReg, mcxRegKey(r.UCC), r, SystemActor)
}

// ---- the file ----

// mcxUCCNextBatch is the next batch number of a date: one after the
// highest uploaded or answered.
func (o *Ops) mcxUCCNextBatch(ctx context.Context, tenant, date string) (int, map[int]MCXUCCBatchDoc) {
	bs, _ := docs.List[MCXUCCBatchDoc](ctx, o.Docs, tenant, cMCXUCCBatch, date+"|")
	by := map[int]MCXUCCBatchDoc{}
	n := 0
	for _, b := range bs {
		by[b.Batch] = b
		if (b.UploadedAt != "" || b.ResponseAt != "") && b.Batch > n {
			n = b.Batch
		}
	}
	return n + 1, by
}

// MCXUCCBatch is a UCC file for MCX.
type MCXUCCBatch struct {
	Name     string   `json:"name"`
	Batch    int      `json:"batch"`
	Content  []byte   `json:"-"`
	Clients  []string `json:"clients"`
	Problems []string `json:"problems"`
}

// mcxUCCBatchFile writes the UCC file of every pending client whose record
// has no problems, for a date and batch (0: the next), and keeps the batch.
func (o *Ops) mcxUCCBatchFile(ctx context.Context, tenant, actor string, s *xfile.Spec, set Settings, date string, seq int) (MCXUCCBatch, error) {
	b := MCXUCCBatch{Clients: []string{}, Problems: []string{}}
	member := strings.TrimSpace(set.MemberCodes["MCX"])
	if member == "" {
		return b, invalid("set the firm's MCX member code in Settings first")
	}
	mcxUCCMu.Lock()
	defer mcxUCCMu.Unlock()
	next, batches := o.mcxUCCNextBatch(ctx, tenant, date)
	if seq <= 0 {
		seq = next
	} else if old, ok := batches[seq]; ok && (old.UploadedAt != "" || old.ResponseAt != "") {
		return b, invalid("batch %02d of %s was already uploaded to MCX (%s): the next batch is %02d", seq, fmtDay(date), old.File, next)
	}
	if seq > 99 {
		return b, invalid("MCX takes batches 01 to 99 a day; %s has no batch left", fmtDay(date))
	}
	items, baseline, err := o.mcxUCCItems(ctx, tenant, date)
	if err != nil {
		return b, err
	}
	if len(baseline) > 0 {
		o.mcxUCCBaseline(ctx, tenant, date, baseline)
	}
	doc := MCXUCCBatchDoc{ID: mcxBatchKey(date, seq), Date: date, Batch: seq, Hashes: map[string]string{}, Actions: map[string]string{}, Summaries: map[string]map[string]string{},
		WrittenAt: o.now().Format(time.RFC3339), WrittenBy: actor}
	var recs []mcxUCCRecord
	for _, it := range items {
		if !it.pending {
			continue
		}
		if len(it.rec.Problems) > 0 {
			b.Problems = append(b.Problems, it.c.UCC+": "+strings.Join(it.rec.Problems, "; "))
			continue
		}
		recs = append(recs, it.rec)
		b.Clients = append(b.Clients, it.c.UCC)
		doc.Hashes[it.c.UCC], doc.Actions[it.c.UCC], doc.Summaries[it.c.UCC] = it.hash, it.reg.Action, it.rec.Summary
	}
	if len(recs) == 0 {
		msg := "nothing to send to MCX"
		if len(b.Problems) > 0 {
			msg = fmt.Sprintf("nothing to send to MCX: %d client(s) need fixing first (%s)", len(b.Problems), b.Problems[0])
		}
		return b, fmt.Errorf("%w: %s", ErrState, msg)
	}
	rows, err := mcxUCCRows(s, member, date, seq, recs)
	if err != nil {
		return b, err
	}
	name, err := s.FileName(xfile.Vars{"date": date, "seq": strconv.Itoa(seq), "member": member})
	if err != nil {
		return b, invalid("%v", err)
	}
	content, err := s.Write(rows)
	if err != nil {
		return b, invalid("%v", err)
	}
	doc.File, doc.UCCs = name, b.Clients
	if err := docs.Upsert(ctx, o.Docs, tenant, cMCXUCCBatch, doc.ID, doc, actor); err != nil {
		return b, err
	}
	b.Name, b.Batch, b.Content = name, seq, content
	return b, nil
}

// MCXUCCBatchFile writes today's next UCC file for MCX.
func (o *Ops) MCXUCCBatchFile(ctx context.Context, tenant, actor string) (MCXUCCBatch, error) {
	if err := o.require(ctx, tenant, actor, "Downloading exchange files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return MCXUCCBatch{}, err
	}
	lib, err := XFiles()
	if err != nil {
		return MCXUCCBatch{}, err
	}
	s, ok := lib.Get("mcx-ucc")
	if !ok {
		return MCXUCCBatch{}, fmt.Errorf("the MCX UCC layout is missing")
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return MCXUCCBatch{}, err
	}
	today := o.now().In(ist).Format("2006-01-02")
	b, err := o.mcxUCCBatchFile(ctx, tenant, actor, s, set, today, 0)
	if err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "xfile.written", b.Name, map[string]any{"format": "mcx-ucc", "date": today, "batch": b.Batch, "clients": len(b.Clients), "left_out": len(b.Problems)})
	return b, nil
}

var reMCXUCCName = regexp.MustCompile(`(?i)^MCX_(\d{2})(\d{2})(\d{4})_UCC(\d+)\.[ME](\d+)(?:\.F(\d+))?$`)

// mcxUCCNameParts reads the date, batch and (for a refused file) the error
// code from an MCX UCC file name.
func mcxUCCNameParts(name string) (date string, batch int, fileErr string, ok bool) {
	base := name[strings.LastIndexAny(name, `/\`)+1:]
	m := reMCXUCCName.FindStringSubmatch(strings.TrimSpace(base))
	if m == nil {
		return "", 0, "", false
	}
	date = m[3] + "-" + m[2] + "-" + m[1]
	batch, _ = strconv.Atoi(m[5])
	return date, batch, m[6], validDate(date)
}

// MCXUCCMarkUploaded records that a UCC file was uploaded on MCX's portal:
// its clients are Sent until MCX's response file is loaded.
func (o *Ops) MCXUCCMarkUploaded(ctx context.Context, tenant, actor, file string) (BSEUCCResult, error) {
	res := BSEUCCResult{Problems: []string{}}
	if err := o.require(ctx, tenant, actor, "Sending UCC to MCX", RoleMaker, RoleChecker); err != nil {
		return res, err
	}
	date, batch, _, ok := mcxUCCNameParts(file)
	if !ok {
		return res, invalid("give the name of the UCC file written for MCX (MCX_DDMMYYYY_UCC<member>.Mnn)")
	}
	mcxUCCMu.Lock()
	defer mcxUCCMu.Unlock()
	b, _, err := docs.Get[MCXUCCBatchDoc](ctx, o.Docs, tenant, cMCXUCCBatch, mcxBatchKey(date, batch))
	if err != nil || !strings.EqualFold(b.File, strings.TrimSpace(file)) {
		return res, fmt.Errorf("%w: %s was not written by Ledgerline (or a later file of batch %02d replaced it)", ErrState, file, batch)
	}
	if b.ResponseAt != "" {
		return res, fmt.Errorf("%w: MCX has already answered %s", ErrState, b.File)
	}
	at := o.now().Format(time.RFC3339)
	for _, u := range b.UCCs {
		r, _, err := docs.GetOr(ctx, o.Docs, tenant, cBSEUCCReg, mcxRegKey(u), BSEUCCReg{Exchange: "MCX", UCC: u})
		if err != nil {
			res.Problems = append(res.Problems, u+": "+err.Error())
			continue
		}
		r.Action = firstOf(b.Actions[u], r.Action)
		r.Hash, r.SentAt, r.Via, r.Ref, r.Problems = b.Hashes[u], at, "File", b.File, nil
		r.event(at, actor, UCCSent, "Uploaded on MCX's portal", r.Ref)
		o.mcxUCCSaveReg(ctx, tenant, r)
		res.Sent++
	}
	b.UploadedAt, b.UploadedBy = at, actor
	_ = docs.Upsert(ctx, o.Docs, tenant, cMCXUCCBatch, b.ID, b, actor)
	res.Message = fmt.Sprintf("%d client(s) of %s marked as uploaded to MCX; load MCX's response file (.E%02d) on Exchange files", res.Sent, b.File, batch)
	o.log(ctx, tenant, actor, "mcxucc.file_uploaded", b.File, map[string]any{"clients": res.Sent})
	return res, nil
}

// ---- MCX's answer ----

// loadMCXUCCFile loads the MCX UCC files: MCX's response, a refused file
// and the State-City master. It reports whether the file was one of them.
func (o *Ops) loadMCXUCCFile(ctx context.Context, tenant, actor string, s *xfile.Spec, f xfile.File, rows []xfile.Row, r *XFileResult) bool {
	fail := func(err error) { r.Loaded = "Not loaded: " + strings.TrimPrefix(err.Error(), "invalid: ") }
	switch s.ID {
	case "mcx-ucc-response":
		acc, rej, skipped, err := o.loadMCXUCCResponse(ctx, tenant, actor, f.Name, rows)
		if err != nil {
			fail(err)
			return true
		}
		r.Loaded, r.Added, r.Skipped = fmt.Sprintf("Loaded as MCX's answer to the UCC file: %d accepted, %d rejected (work items raised)", acc, rej), acc+rej, skipped
		if skipped > 0 {
			r.Note = fmt.Sprintf("%d client code(s) are not clients Ledgerline registers at MCX and were left out.", skipped)
		}
	case "mcx-ucc":
		if _, _, code, ok := mcxUCCNameParts(f.Name); ok && code != "" {
			n, err := o.mcxUCCFileRefused(ctx, tenant, actor, f.Name)
			if err != nil {
				fail(err)
				return true
			}
			r.Loaded, r.Added = "MCX refused the whole file ("+MCXUCCError(code)+"): its clients go in the next batch", n
			return true
		}
		r.Loaded = "Checked only: a UCC file for MCX (written on Exchange UCC)"
	case mcxStateCitySpec:
		n, err := o.loadMCXStateCity(ctx, tenant, actor, f.Name, f.Data)
		if err != nil {
			fail(err)
			return true
		}
		r.Loaded, r.Added = "Loaded as MCX's State-City Code master (used for the MCX UCC file)", n
	default:
		return false
	}
	return true
}

// xLenient says whether a file's own layout check is not used: the
// State-City master is read by its header names, and a refused UCC file
// only by its name.
func xLenient(id, name string) bool {
	if id == mcxStateCitySpec {
		return true
	}
	_, _, code, ok := mcxUCCNameParts(name)
	return id == "mcx-ucc" && ok && code != ""
}

// loadMCXUCCResponse marks the clients of MCX's response file accepted or
// rejected.
func (o *Ops) loadMCXUCCResponse(ctx context.Context, tenant, actor, name string, rows []xfile.Row) (acc, rej, skipped int, err error) {
	date, batch, _, _ := mcxUCCNameParts(name)
	for _, row := range rows {
		if row.Record != "" {
			continue
		}
		if d := strings.TrimSpace(row.Get("Date")); len(d) == 8 {
			if x := d[4:] + "-" + d[2:4] + "-" + d[:2]; validDate(x) {
				date = x
			}
		}
		if n, e := strconv.Atoi(strings.TrimSpace(row.Get("BatchNumber"))); e == nil && n > 0 {
			batch = n
		}
	}
	if date == "" || batch == 0 {
		return 0, 0, 0, invalid("the response has no date and batch number (control record or file name)")
	}
	mcxUCCMu.Lock()
	defer mcxUCCMu.Unlock()
	b, _, berr := docs.Get[MCXUCCBatchDoc](ctx, o.Docs, tenant, cMCXUCCBatch, mcxBatchKey(date, batch))
	known := berr == nil
	cs, _ := o.ClientMap(ctx, tenant)
	at := o.now().Format(time.RFC3339)
	ref := firstOf(b.File, fmt.Sprintf("MCX batch %02d of %s", batch, fmtDay(date)))
	for _, row := range rows {
		if row.Record == "" {
			continue
		}
		u := strings.ToUpper(strings.TrimSpace(row.Get("ClientCode")))
		code := strings.TrimSpace(row.Get("ErrorCode"))
		r, _, gerr := docs.Get[BSEUCCReg](ctx, o.Docs, tenant, cBSEUCCReg, mcxRegKey(u))
		h, inBatch := b.Hashes[u]
		if gerr != nil && !inBatch {
			skipped++
			continue
		}
		if gerr != nil {
			r = BSEUCCReg{Exchange: "MCX", UCC: u, Name: cs[u].Name}
		}
		if inBatch {
			r.Hash, r.Via = h, "File"
			r.Action = firstOf(b.Actions[u], r.Action)
		}
		r.ResultAt, r.Ref, r.Problems = at, ref, nil
		if code == "" {
			r.AcceptedHash = r.Hash
			if s, ok := b.Summaries[u]; ok {
				r.Summary = s
			}
			r.event(at, actor, UCCAccepted, "", ref)
			o.closeWork(ctx, tenant, actor, "mcxucc", "rej:"+u, "accepted by MCX")
			acc++
		} else {
			text := MCXUCCError(code)
			r.event(at, actor, UCCRejected, text, ref)
			who := firstOf(cs[u].Name, u)
			o.raiseWork(ctx, tenant, "mcxucc", "rej:"+u, "MCX rejected the UCC of "+who,
				fmt.Sprintf("%s: MCX refused the client's record in %s: %s. Correct the client's details (or the Exchange UCC details); the corrected record goes in the next MCX UCC file.", u, ref, text), u, RoleMaker, "uccapi", date)
			rej++
		}
		o.mcxUCCSaveReg(ctx, tenant, r)
	}
	if known {
		b.ResponseAt, b.Response, b.Accepted, b.Rejected = at, name, acc, rej
		if b.UploadedAt == "" {
			b.UploadedAt, b.UploadedBy = at, actor // MCX answered it, so it was uploaded
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cMCXUCCBatch, b.ID, b, actor)
	}
	o.log(ctx, tenant, actor, "mcxucc.response", name, map[string]any{"accepted": acc, "rejected": rej, "skipped": skipped, "batch": batch, "date": date})
	return acc, rej, skipped, nil
}

// mcxUCCFileRefused handles a file MCX refused whole (.Mnn.F<error>): not
// the clients' fault, so they are Not sent again and go in the next batch.
func (o *Ops) mcxUCCFileRefused(ctx context.Context, tenant, actor, name string) (int, error) {
	date, batch, code, _ := mcxUCCNameParts(name)
	mcxUCCMu.Lock()
	defer mcxUCCMu.Unlock()
	b, _, err := docs.Get[MCXUCCBatchDoc](ctx, o.Docs, tenant, cMCXUCCBatch, mcxBatchKey(date, batch))
	if err != nil {
		return 0, invalid("batch %02d of %s was not written by Ledgerline", batch, fmtDay(date))
	}
	text := MCXUCCError(code)
	at := o.now().Format(time.RFC3339)
	n := 0
	for _, u := range b.UCCs {
		r, _, err := docs.Get[BSEUCCReg](ctx, o.Docs, tenant, cBSEUCCReg, mcxRegKey(u))
		if err != nil || r.Ref != b.File || r.Status != UCCSent {
			continue
		}
		r.Hash, r.Ref = "", ""
		r.event(at, actor, UCCNotSent, "MCX refused the whole file "+b.File+" ("+text+"); it goes in the next batch", "")
		o.mcxUCCSaveReg(ctx, tenant, r)
		n++
	}
	b.FileError, b.ResponseAt, b.Response = text, at, name
	_ = docs.Upsert(ctx, o.Docs, tenant, cMCXUCCBatch, b.ID, b, actor)
	o.raiseWork(ctx, tenant, "mcxucc", "file:"+b.ID, "MCX refused UCC file "+b.File,
		fmt.Sprintf("MCX refused %s as a whole: %s. Its %d client(s) go in the next UCC file: write it on Exchange UCC (MCX) and upload it.", b.File, text, len(b.UCCs)), "", RoleMaker, "uccapi", date)
	o.log(ctx, tenant, actor, "mcxucc.file_refused", name, map[string]any{"error": code, "clients": n})
	return n, nil
}

// ---- the screen ----

// mcxUCCRowStatus is what the screen shows (as BSE's).
func mcxUCCRowStatus(it mcxUCCItem) (string, string) {
	r := it.reg
	if it.pending && len(it.rec.Problems) > 0 {
		return UCCNeedsFixing, it.rec.Problems[0]
	}
	if it.pending && (r.Status == UCCAccepted || r.Status == UCCRejected || r.Status == UCCSent) {
		return UCCNotSent, "Changed since the record MCX holds (or was sent): goes in the next file"
	}
	return firstOf(r.Status, UCCNotSent), r.Reason
}

// MCXUCCView is GET /v1/ucc-api/mcx: every client registered or to
// register at MCX, and the latest files.
type MCXUCCView struct {
	BSEUCCView
	Member      string `json:"member"`
	NextBatch   int    `json:"next_batch"`
	Master      string `json:"master"` // the State-City master: file and when loaded, "" when not loaded
	MasterCount int    `json:"master_count"`
}

// MCXUCCView lists the clients and the files.
func (o *Ops) MCXUCCView(ctx context.Context, tenant, actor string) (MCXUCCView, error) {
	v := MCXUCCView{BSEUCCView: BSEUCCView{Exchange: "MCX", Connection: "File", Counts: map[string]int{}, Clients: []BSEUCCClientRow{}, Runs: []BSEUCCRunRow{}}}
	if err := o.require(ctx, tenant, actor, "Viewing MCX UCC registrations", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return v, err
	}
	set, _ := o.Settings(ctx, tenant)
	v.Member = strings.TrimSpace(set.MemberCodes["MCX"])
	v.Live = v.Member != ""
	for _, s := range []string{UCCNotSent, UCCSent, UCCAccepted, UCCRejected, UCCNeedsFixing} {
		v.Counts[s] = 0
	}
	today := o.now().In(ist).Format("2006-01-02")
	v.NextBatch, _ = o.mcxUCCNextBatch(ctx, tenant, today)
	if m, _, err := docs.Get[MCXStateCityMaster](ctx, o.Docs, tenant, cMCXStateCity, "master"); err == nil {
		v.Master, v.MasterCount = m.File+" · "+m.LoadedAt, m.Count
	}
	items, _, err := o.mcxUCCItems(ctx, tenant, today)
	if err != nil {
		return v, err
	}
	rank := map[string]int{UCCNeedsFixing: 0, UCCRejected: 1, UCCNotSent: 2, UCCSent: 3, UCCAccepted: 4}
	for _, it := range items {
		st, reason := mcxUCCRowStatus(it)
		v.Counts[st]++
		probs := []string{}
		if it.pending {
			probs = it.rec.Problems
		}
		v.Clients = append(v.Clients, BSEUCCClientRow{UCC: it.c.UCC, Name: it.c.Name, PAN: MaskPAN(it.c.PAN), Action: it.reg.Action, Status: st, Reason: reason, Problems: probs,
			SentAt: it.reg.SentAt, ResultAt: it.reg.ResultAt, Ref: it.reg.Ref})
	}
	sort.SliceStable(v.Clients, func(i, j int) bool { return rank[v.Clients[i].Status] < rank[v.Clients[j].Status] })
	if len(v.Clients) > 1000 {
		v.Clients = v.Clients[:1000]
	}
	bs, _ := docs.List[MCXUCCBatchDoc](ctx, o.Docs, tenant, cMCXUCCBatch, "")
	sort.Slice(bs, func(i, j int) bool { return bs[i].ID > bs[j].ID })
	for i, b := range bs {
		if i == 30 {
			break
		}
		note := "written, not marked uploaded"
		switch {
		case b.FileError != "":
			note = "refused whole by MCX: " + b.FileError
		case b.ResponseAt != "":
			note = "answered by MCX (" + b.Response + ")"
		case b.UploadedAt != "":
			note = "uploaded, waiting for MCX's response file"
		}
		v.Runs = append(v.Runs, BSEUCCRunRow{ID: b.File, At: firstOf(b.UploadedAt, b.WrittenAt), By: firstOf(b.UploadedBy, b.WrittenBy), Sent: len(b.UCCs), Accepted: b.Accepted, Rejected: b.Rejected, Note: note})
	}
	return v, nil
}

// MCXUCCPreview is one client's record as it would go to MCX.
type MCXUCCPreview struct {
	UCC      string              `json:"ucc"`
	Name     string              `json:"name"`
	Action   string              `json:"action"`
	Status   string              `json:"status"`
	Pending  []string            `json:"pending"` // ["COM"] when the record would go in the next file
	Records  []map[string]string `json:"records"` // the one record: "NNN Field" → value, PAN and account masked
	Problems []string            `json:"problems"`
	Details  UCCDetails          `json:"details"`
	History  []NSEUCIEvent       `json:"history"`
	Hash     string              `json:"payload_hash"`
}

// MCXUCCPreview maps one client.
func (o *Ops) MCXUCCPreview(ctx context.Context, tenant, actor, ucc string) (MCXUCCPreview, error) {
	if err := o.require(ctx, tenant, actor, "Viewing MCX UCC registrations", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return MCXUCCPreview{}, err
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return MCXUCCPreview{}, err
	}
	lib, err := XFiles()
	if err != nil {
		return MCXUCCPreview{}, err
	}
	s, _ := lib.Get("mcx-ucc")
	today := o.now().In(ist).Format("2006-01-02")
	items, _, err := o.mcxUCCItems(ctx, tenant, today)
	if err != nil {
		return MCXUCCPreview{}, err
	}
	p := MCXUCCPreview{UCC: c.UCC, Name: c.Name, Action: "Modify", Pending: []string{}, Problems: []string{}, History: []NSEUCIEvent{}, Details: o.UCCDetailsFor(ctx, tenant, c.UCC)}
	var rec mcxUCCRecord
	found := false
	for _, it := range items {
		if it.c.UCC == c.UCC {
			found = true
			rec = it.rec
			p.Status, _ = mcxUCCRowStatus(it)
			p.Action = it.reg.Action
			if it.pending && len(it.rec.Problems) == 0 {
				p.Pending = []string{"COM"}
			}
			for _, h := range it.reg.History {
				p.History = append(p.History, NSEUCIEvent{At: h.At, What: h.Status, Ref: h.Ref, Detail: h.Reason})
			}
		}
	}
	if !found { // not one to register (yet): what a modification would be
		set, _ := o.Settings(ctx, tenant)
		cs, _ := o.Clients(ctx, tenant)
		rec = mcxUCCMap(c, o.mcxUCCLoadSources(ctx, tenant, set, cs), nil, today)
	}
	p.Hash = rec.Hash()
	if rec.Problems != nil {
		p.Problems = rec.Problems
	}
	m := rec.Masked()
	row := map[string]string{}
	if s != nil && len(s.Records) == 2 {
		for i, col := range s.Records[1].Columns {
			if x := m[col.Key]; x != "" {
				row[fmt.Sprintf("%03d %s", i+1, col.Name)] = x
			}
		}
	}
	p.Records = []map[string]string{row}
	return p, nil
}
