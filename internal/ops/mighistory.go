package ops

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
)

// ---- read-only history of the years before Ledgerline ----
//
// Each history file (one kind for one financial year, pkg.HistoryColumns)
// is kept as it came, in compact form: its rows are grouped by client and
// stored gzipped in chunks of a few thousand rows, with an index of which
// chunks hold each client. A client's year is then a few chunk reads, and a
// firm-wide export reads the chunks one after another, so millions of
// ledger lines need neither one document per line nor the whole year in
// memory. Grouping a large file by client goes through bucket files in the
// firm's temporary folder (by a hash of the client code), each sorted in
// memory on its own. Nothing here can be edited; an undo of the migration
// hides it.

const (
	cHist      = "mighist"  // <mig>|<fy>|<kind>: HistMeta
	cHistChunk = "mighistc" // <mig>|<fy>|<kind>|<n>: gzipped CSV rows
	cHistShard = "mighists" // <mig>|<fy>|<kind>|<bucket>: client -> chunks

	histChunkRows = 2000
)

// HistMeta describes one stored history file.
type HistMeta struct {
	ID       string    `json:"id"`
	Mig      string    `json:"mig"`
	System   string    `json:"system"`
	Firm     string    `json:"firm"`
	FY       string    `json:"fy"`
	Kind     string    `json:"kind"`
	Columns  []string  `json:"columns"`
	Rows     int       `json:"rows"`
	Chunks   int       `json:"chunks"`
	Buckets  int       `json:"buckets"`
	LoadedAt time.Time `json:"loaded_at"`
	Removed  bool      `json:"removed,omitempty"`
}

type histChunk struct {
	GZ []byte `json:"gz"`
}

type histShard struct {
	UCC map[string][2]int `json:"ucc"` // first and last chunk
}

// HistoryLabel is the line every screen of migrated history carries.
func HistoryLabel(system string) string {
	return "Migrated from " + firstOf(system, "the previous back office") + " — read only"
}

// histKeyer finds the client a row belongs to: the UCC column, or on a
// ledger line with no UCC, the code in a CLIENT: account.
func histKeyer(cols []string) func([]string) string {
	u, a := -1, -1
	for i, c := range cols {
		switch strings.ToLower(strings.TrimSpace(c)) {
		case "ucc":
			u = i
		case "account":
			a = i
		}
	}
	return func(r []string) string {
		if u >= 0 && u < len(r) && strings.TrimSpace(r[u]) != "" {
			return strings.ToUpper(strings.TrimSpace(r[u]))
		}
		if a >= 0 && a < len(r) {
			if c, ok := strings.CutPrefix(strings.ToUpper(strings.TrimSpace(r[a])), "CLIENT:"); ok {
				return c
			}
		}
		return ""
	}
}

func histBucket(key string, n int) int {
	h := fnv.New32a()
	h.Write([]byte(key))
	return int(h.Sum32() % uint32(n))
}

func histID(mig, fy, kind string) string { return mig + "|" + fy + "|" + kind }

// storeHistory keeps one history file of the package.
func (o *Ops) storeHistory(ctx context.Context, tenant, actor string, m *Migration, r *pkg.Reader, st *MigStep, tmp string) (int, error) {
	if _, ok := pkg.HistoryColumns[st.Source]; !ok {
		st.Message = "not a kind of history Ledgerline keeps; skipped"
		return 0, nil
	}
	rows, expect := 0, 0
	for _, f := range r.Manifest.Files {
		if f.Path == st.Path {
			expect = f.Rows
		}
	}
	nb := min(max(expect/100000+1, 1), 256)
	dir, err := os.MkdirTemp(tmp, "hist-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	type bucket struct {
		f  *os.File
		bw *bufio.Writer
		cw *csv.Writer
	}
	bs := make([]bucket, nb)
	for i := range bs {
		f, err := os.Create(fmt.Sprintf("%s/%03d.csv", dir, i))
		if err != nil {
			return 0, err
		}
		bw := bufio.NewWriterSize(f, 64<<10)
		bs[i] = bucket{f, bw, csv.NewWriter(bw)}
	}
	closeAll := func() {
		for _, b := range bs {
			b.f.Close()
		}
	}
	var cols []string
	var key func([]string) string
	err = r.CSV(st.Path, func(h, row []string) error {
		if cols == nil {
			cols = append([]string(nil), h...)
			key = histKeyer(cols)
		}
		rows++
		return bs[histBucket(key(row), nb)].cw.Write(row)
	})
	for _, b := range bs {
		b.cw.Flush()
		b.bw.Flush()
	}
	closeAll()
	if err != nil {
		return rows, err
	}
	if cols == nil {
		cols = pkg.HistoryColumns[st.Source]
		key = histKeyer(cols)
	}
	id := histID(m.ID, st.FY, st.Source)
	chunk := 0
	for b := 0; b < nb; b++ {
		f, err := os.Open(fmt.Sprintf("%s/%03d.csv", dir, b))
		if err != nil {
			return rows, err
		}
		cr := csv.NewReader(bufio.NewReaderSize(f, 64<<10))
		cr.FieldsPerRecord = -1
		all, err := cr.ReadAll()
		f.Close()
		if err != nil {
			return rows, err
		}
		sort.SliceStable(all, func(i, j int) bool { return key(all[i]) < key(all[j]) })
		sh := histShard{UCC: map[string][2]int{}}
		for s := 0; s < len(all); s += histChunkRows {
			part := all[s:min(s+histChunkRows, len(all))]
			gz, err := gzipRows(part)
			if err != nil {
				return rows, err
			}
			if err := docs.Upsert(ctx, o.Docs, tenant, cHistChunk, fmt.Sprintf("%s|%06d", id, chunk), histChunk{GZ: gz}, actor); err != nil {
				return rows, err
			}
			for _, rw := range part {
				k := key(rw)
				if v, ok := sh.UCC[k]; ok {
					v[1] = chunk
					sh.UCC[k] = v
				} else {
					sh.UCC[k] = [2]int{chunk, chunk}
				}
			}
			chunk++
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cHistShard, fmt.Sprintf("%s|%03d", id, b), sh, actor); err != nil {
			return rows, err
		}
	}
	meta := HistMeta{ID: id, Mig: m.ID, System: m.System, Firm: m.Firm, FY: st.FY, Kind: st.Source, Columns: cols, Rows: rows, Chunks: chunk, Buckets: nb, LoadedAt: o.now()}
	return rows, docs.Upsert(ctx, o.Docs, tenant, cHist, id, meta, actor)
}

func gzipRows(rows [][]string) ([]byte, error) {
	var b bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	cw := csv.NewWriter(zw)
	if err := cw.WriteAll(rows); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func gunzipRows(b []byte) ([][]string, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	cr := csv.NewReader(zr)
	cr.FieldsPerRecord = -1
	return cr.ReadAll()
}

// histMetas lists the history files in force (not undone).
func (o *Ops) histMetas(ctx context.Context, tenant string) ([]HistMeta, error) {
	ms, err := docs.List[HistMeta](ctx, o.Docs, tenant, cHist, "")
	out := ms[:0]
	for _, m := range ms {
		if !m.Removed {
			out = append(out, m)
		}
	}
	return out, err
}

func (o *Ops) histMeta(ctx context.Context, tenant, fy, kind string) (HistMeta, bool) {
	ms, _ := o.histMetas(ctx, tenant)
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].FY == fy && ms[i].Kind == kind {
			return ms[i], true
		}
	}
	return HistMeta{}, false
}

// histRows returns one client's rows of a history file, in file order.
func (o *Ops) histRows(ctx context.Context, tenant string, hm HistMeta, ucc string) ([][]string, error) {
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	sh, _, err := docs.GetOr(ctx, o.Docs, tenant, cHistShard, fmt.Sprintf("%s|%03d", hm.ID, histBucket(ucc, max(hm.Buckets, 1))), histShard{})
	if err != nil {
		return nil, err
	}
	rg, ok := sh.UCC[ucc]
	if !ok {
		return [][]string{}, nil
	}
	key := histKeyer(hm.Columns)
	out := [][]string{}
	for c := rg[0]; c <= rg[1]; c++ {
		ch, _, err := docs.Get[histChunk](ctx, o.Docs, tenant, cHistChunk, fmt.Sprintf("%s|%06d", hm.ID, c))
		if err != nil {
			return nil, err
		}
		rows, err := gunzipRows(ch.GZ)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if key(r) == ucc {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// histAccess checks that the user may see a client's history: the client
// themselves in the portal, or staff who may see that client.
func (o *Ops) histAccess(ctx context.Context, tenant, actor, ucc string, portal bool) error {
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if ucc == "" {
		return invalid("choose a client")
	}
	if portal {
		accts, err := o.PortalAccounts(ctx, tenant, actor)
		if err != nil {
			return err
		}
		for _, a := range accts {
			if strings.EqualFold(a, ucc) {
				return nil
			}
		}
		return fmt.Errorf("%w: no such account", ErrNotFound)
	}
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return err
	}
	if role == RoleClient {
		return fmt.Errorf("%w: use the client portal", ErrForbidden)
	}
	if f := o.ClientScope(ctx, tenant, actor); f != nil && !f(ucc) {
		return fmt.Errorf("%w: no such client", ErrNotFound)
	}
	return nil
}

// HistoryIndex says which earlier years a client has, and of what.
type HistoryIndex struct {
	UCC      string       `json:"ucc"`
	System   string       `json:"system,omitempty"`
	Label    string       `json:"label,omitempty"`
	Years    []HistYear   `json:"years"`
	Archived int          `json:"archived"` // documents in the archive
	Kinds    []HistKindNm `json:"kinds"`
}

// HistYear is one financial year of history.
type HistYear struct {
	FY    string   `json:"fy"`
	Kinds []string `json:"kinds"`
}

// HistKindNm names a kind of history.
type HistKindNm struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

var histTitles = map[string]string{pkg.HistoryLedger: "Ledger", pkg.HistoryBills: "Bills", pkg.HistoryTrades: "Trades", pkg.HistoryContractNotes: "Contract notes register",
	pkg.HistoryReceipts: "Receipts and payments", pkg.HistoryDP: "DP transactions", pkg.HistoryDPBills: "DP bills", pkg.HistoryHoldings: "Year-end holdings"}

var histKindOrder = []string{pkg.HistoryLedger, pkg.HistoryBills, pkg.HistoryTrades, pkg.HistoryContractNotes, pkg.HistoryReceipts, pkg.HistoryDP, pkg.HistoryDPBills, pkg.HistoryHoldings}

// portalKinds are what a client sees of their own history.
var portalKinds = map[string]bool{pkg.HistoryLedger: true, pkg.HistoryContractNotes: true}

// ClientHistory lists a client's earlier years (portal: their own, with
// the ledger and contract notes only).
func (o *Ops) ClientHistory(ctx context.Context, tenant, actor, ucc string, portal bool) (HistoryIndex, error) {
	if err := o.histAccess(ctx, tenant, actor, ucc, portal); err != nil {
		return HistoryIndex{}, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	out := HistoryIndex{UCC: ucc, Years: []HistYear{}, Kinds: []HistKindNm{}}
	ms, err := o.histMetas(ctx, tenant)
	if err != nil {
		return out, err
	}
	years := map[string]map[string]bool{}
	for _, m := range ms {
		if portal && !portalKinds[m.Kind] {
			continue
		}
		out.System = m.System
		if years[m.FY] == nil {
			years[m.FY] = map[string]bool{}
		}
		years[m.FY][m.Kind] = true
	}
	for _, fy := range sortedKeys(years) {
		y := HistYear{FY: fy}
		for _, k := range histKindOrder {
			if years[fy][k] {
				y.Kinds = append(y.Kinds, k)
			}
		}
		out.Years = append([]HistYear{y}, out.Years...) // newest first
	}
	for _, k := range histKindOrder {
		if !portal || portalKinds[k] {
			out.Kinds = append(out.Kinds, HistKindNm{k, histTitles[k]})
		}
	}
	arc, _ := docs.List[ArchiveDoc](ctx, o.Docs, tenant, cArc, ucc+"|")
	for _, a := range arc {
		if a.UCC == ucc && !a.Removed && !(portal && a.Deleted) {
			out.Archived++
			out.System = firstOf(out.System, a.System)
		}
	}
	if out.System != "" || len(out.Years) > 0 {
		out.Label = HistoryLabel(out.System)
	}
	return out, nil
}

// HistTable is one kind of history for a client and year.
type HistTable struct {
	UCC     string     `json:"ucc"`
	FY      string     `json:"fy"`
	Kind    string     `json:"kind"`
	Title   string     `json:"title"`
	Label   string     `json:"label"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

// ClientHistoryTable returns a client's rows of one kind for one year.
func (o *Ops) ClientHistoryTable(ctx context.Context, tenant, actor, ucc, fy, kind string, portal bool) (HistTable, error) {
	if err := o.histAccess(ctx, tenant, actor, ucc, portal); err != nil {
		return HistTable{}, err
	}
	kind = strings.ToUpper(kind)
	if portal && !portalKinds[kind] {
		return HistTable{}, fmt.Errorf("%w: not available in the portal", ErrNotFound)
	}
	hm, ok := o.histMeta(ctx, tenant, fy, kind)
	if !ok {
		return HistTable{}, fmt.Errorf("%w: no %s history for %s", ErrNotFound, strings.ToLower(firstOf(histTitles[kind], kind)), fy)
	}
	rows, err := o.histRows(ctx, tenant, hm, ucc)
	if err != nil {
		return HistTable{}, err
	}
	cols := hm.Columns
	if portal {
		// The client sees what the old statement showed, not the old
		// system's internal references.
		keep := []int{}
		var nc []string
		for i, c := range cols {
			if c != "SourceRef" && c != "DocumentID" {
				keep = append(keep, i)
				nc = append(nc, c)
			}
		}
		for i, r := range rows {
			x := make([]string, 0, len(keep))
			for _, k := range keep {
				if k < len(r) {
					x = append(x, r[k])
				} else {
					x = append(x, "")
				}
			}
			rows[i] = x
		}
		cols = nc
	}
	return HistTable{UCC: strings.ToUpper(ucc), FY: fy, Kind: kind, Title: firstOf(histTitles[kind], kind), Label: HistoryLabel(hm.System), Columns: cols, Rows: rows}, nil
}

// HistStatement is a client's ledger for an earlier year with its opening
// and running balance (positive is a debit: the client owes).
type HistStatement struct {
	UCC     string         `json:"ucc"`
	FY      string         `json:"fy"`
	Label   string         `json:"label"`
	Opening money.Amount   `json:"opening"`
	Lines   []HistStmtLine `json:"lines"`
	Debit   money.Amount   `json:"debit"`
	Credit  money.Amount   `json:"credit"`
	Closing money.Amount   `json:"closing"`
}

// HistStmtLine is one line of a HistStatement.
type HistStmtLine struct {
	Date        string       `json:"date"`
	VoucherType string       `json:"voucher_type"`
	VoucherNo   string       `json:"voucher_no"`
	Segment     string       `json:"segment,omitempty"`
	Narration   string       `json:"narration"`
	ChequeNo    string       `json:"cheque_no,omitempty"`
	Debit       money.Amount `json:"debit"`
	Credit      money.Amount `json:"credit"`
	Balance     money.Amount `json:"balance"`
}

func histAmount(s string) money.Amount {
	a, err := money.ParseAmount(strings.ReplaceAll(strings.TrimSpace(s), ",", ""))
	if err != nil {
		d, err2 := money.ParseDec(strings.ReplaceAll(strings.TrimSpace(s), ",", ""))
		if err2 != nil {
			return 0
		}
		return d.Round(1)
	}
	return a
}

func isOpeningLine(vtype, narr string) bool {
	v := strings.ToUpper(strings.TrimSpace(vtype))
	switch v {
	case "OP", "OB", "OPENING", "OPBAL", "OPENING BALANCE", "OPN":
		return true
	}
	return strings.Contains(strings.ToLower(narr), "opening balance")
}

// HistoryStatement is a client's ledger statement for an earlier year. The
// opening balance is the year's opening lines in the old system, else the
// closing balance of the year before when that year is here too.
func (o *Ops) HistoryStatement(ctx context.Context, tenant, actor, ucc, fy string, portal bool) (HistStatement, error) {
	if err := o.histAccess(ctx, tenant, actor, ucc, portal); err != nil {
		return HistStatement{}, err
	}
	return o.histStatement(ctx, tenant, strings.ToUpper(strings.TrimSpace(ucc)), fy, 0)
}

func prevFY(fy string) string {
	var y int
	if _, err := fmt.Sscanf(fy, "%d-", &y); err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%02d", y-1, y%100)
}

func (o *Ops) histStatement(ctx context.Context, tenant, ucc, fy string, depth int) (HistStatement, error) {
	hm, ok := o.histMeta(ctx, tenant, fy, pkg.HistoryLedger)
	if !ok {
		return HistStatement{}, fmt.Errorf("%w: no ledger history for %s", ErrNotFound, fy)
	}
	rows, err := o.histRows(ctx, tenant, hm, ucc)
	if err != nil {
		return HistStatement{}, err
	}
	col := map[string]int{}
	for i, c := range hm.Columns {
		col[c] = i
	}
	get := func(r []string, c string) string {
		if i, ok := col[c]; ok && i < len(r) {
			return strings.TrimSpace(r[i])
		}
		return ""
	}
	sort.SliceStable(rows, func(i, j int) bool { return get(rows[i], "Date") < get(rows[j], "Date") })
	st := HistStatement{UCC: ucc, FY: fy, Label: HistoryLabel(hm.System), Lines: []HistStmtLine{}}
	hasOpening := false
	var lines []HistStmtLine
	for _, r := range rows {
		dr, cr := histAmount(get(r, "Debit")), histAmount(get(r, "Credit"))
		if isOpeningLine(get(r, "VoucherType"), get(r, "Narration")) {
			hasOpening = true
			st.Opening += dr - cr
			continue
		}
		lines = append(lines, HistStmtLine{Date: get(r, "Date"), VoucherType: get(r, "VoucherType"), VoucherNo: get(r, "VoucherNo"), Segment: get(r, "Segment"),
			Narration: get(r, "Narration"), ChequeNo: get(r, "ChequeNo"), Debit: dr, Credit: cr})
	}
	if !hasOpening && depth < 12 {
		if p := prevFY(fy); p != "" {
			if prev, err := o.histStatement(ctx, tenant, ucc, p, depth+1); err == nil {
				st.Opening = prev.Closing
			}
		}
	}
	run := st.Opening
	for _, l := range lines {
		run += l.Debit - l.Credit
		l.Balance = run
		st.Debit += l.Debit
		st.Credit += l.Credit
		st.Lines = append(st.Lines, l)
	}
	st.Closing = run
	return st, nil
}

// ExportHistory writes one year's history of one kind, for every client,
// as CSV (the old system's columns), with the read-only label on top.
func (o *Ops) ExportHistory(ctx context.Context, tenant, actor, fy, kind string, w io.Writer) error {
	if err := o.require(ctx, tenant, actor, "Exporting migrated history", RoleChecker, RoleCompliance); err != nil {
		return err
	}
	hm, ok := o.histMeta(ctx, tenant, fy, strings.ToUpper(kind))
	if !ok {
		return fmt.Errorf("%w: no %s history for %s", ErrNotFound, strings.ToLower(kind), fy)
	}
	cw := csv.NewWriter(w)
	cw.Write([]string{HistoryLabel(hm.System) + " · " + firstOf(histTitles[hm.Kind], hm.Kind) + " " + hm.FY})
	cw.Write(hm.Columns)
	for c := 0; c < hm.Chunks; c++ {
		ch, _, err := docs.Get[histChunk](ctx, o.Docs, tenant, cHistChunk, fmt.Sprintf("%s|%06d", hm.ID, c))
		if err != nil {
			return err
		}
		rows, err := gunzipRows(ch.GZ)
		if err != nil {
			return err
		}
		if err := cw.WriteAll(rows); err != nil {
			return err
		}
	}
	o.log(ctx, tenant, actor, "history.exported", hm.ID, map[string]any{"fy": fy, "kind": hm.Kind, "rows": hm.Rows})
	cw.Flush()
	return cw.Error()
}

// HistoryFiles lists the stored history files, for the export list.
func (o *Ops) HistoryFiles(ctx context.Context, tenant, actor string) ([]HistMeta, error) {
	if err := o.require(ctx, tenant, actor, "Exporting migrated history", RoleChecker, RoleCompliance); err != nil {
		return nil, err
	}
	return o.histMetas(ctx, tenant)
}

// removeHistory hides a migration's history (undo).
func (o *Ops) removeHistory(ctx context.Context, tenant, actor, mig string) error {
	ms, err := docs.List[HistMeta](ctx, o.Docs, tenant, cHist, mig+"|")
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.Mig != mig || m.Removed {
			continue
		}
		m.Removed = true
		if err := docs.Upsert(ctx, o.Docs, tenant, cHist, m.ID, m, actor); err != nil {
			return err
		}
	}
	return nil
}
