package ops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- exchange files: the exact files of exchanges, clearing corporations,
// depositories, the trading front end and the bank ----
//
// The layouts are in config/formats (see internal/xfile). A file uploaded
// here is recognised by its name and header, checked field by field, and,
// for the files Ledgerline uses, loaded: trade files as the day's exchange
// trades, the CDSL statement of holding as client holdings, the F&O
// position file as carried positions, STT/CTT files for reconciliation.
// Outgoing files are written in the receiver's layout and name.

var (
	xlibOnce sync.Once
	xlib     *xfile.Library
	xlibErr  error
)

// XFiles returns the file layout library.
func XFiles() (*xfile.Library, error) {
	xlibOnce.Do(func() { xlib, xlibErr = xfile.Load(ledgerline.FS, "config/formats") })
	return xlib, xlibErr
}

// XFormat describes one layout for the Exchange files page.
type XFormat struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Party     string `json:"party"`
	Direction string `json:"direction"`
	When      string `json:"when"`
	Purpose   string `json:"purpose"`
	Linked    string `json:"linked"`
	Confirmed bool   `json:"confirmed"`
	Fields    int    `json:"fields"`
	Writes    bool   `json:"writes"` // Ledgerline can produce it
	Example   string `json:"example"`
}

// xWriters are the outgoing files Ledgerline writes.
var xWriters = map[string]bool{"ncl-allocation": true, "ncl-allocation-api": true, "holding-statement-api": true, "segmon-client-funds": true, "ncl-client-collateral": true}

// ExchangeFormats lists every known layout.
func (o *Ops) ExchangeFormats() ([]XFormat, error) {
	lib, err := XFiles()
	if err != nil {
		return nil, err
	}
	out := []XFormat{}
	for _, s := range lib.Specs {
		n := len(s.Columns)
		for _, r := range s.Records {
			n = max(n, len(r.Columns))
		}
		ex := s.Template
		if len(s.Names) > 0 && ex == "" {
			ex = s.Names[0]
		}
		out = append(out, XFormat{ID: s.ID, Title: s.Title, Party: s.Party, Direction: s.Direction, When: s.When, Purpose: s.Purpose, Linked: s.Linked,
			Confirmed: s.Confirmed, Fields: n, Writes: xWriters[s.ID], Example: ex})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Direction != out[j].Direction {
			return out[i].Direction == "in"
		}
		return out[i].Party+out[i].Title < out[j].Party+out[j].Title
	})
	return out, nil
}

// XFileResult is what happened to one file.
type XFileResult struct {
	Name     string        `json:"name"`
	Format   string        `json:"format"`
	Title    string        `json:"title"`
	Rows     int           `json:"rows"`
	Issues   []xfile.Issue `json:"issues"`
	Problems int           `json:"problems"`
	Loaded   string        `json:"loaded"` // what Ledgerline did with it
	Added    int           `json:"added"`
	Skipped  int           `json:"skipped"`
	Note     string        `json:"note,omitempty"`
}

// MaxXFileBytes caps one upload.
const MaxXFileBytes = 100 << 20

// LoadExchangeFile checks an uploaded exchange file (zipped or gzipped as
// received is fine) and, with load, loads the ones Ledgerline uses.
func (o *Ops) LoadExchangeFile(ctx context.Context, tenant, actor, name string, data []byte, load bool) ([]XFileResult, error) {
	if err := o.require(ctx, tenant, actor, "Loading exchange files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > MaxXFileBytes {
		return nil, invalid("files must be between 1 byte and 100 MB")
	}
	lib, err := XFiles()
	if err != nil {
		return nil, err
	}
	files, err := xfile.Unpack(name, data)
	if err != nil {
		return nil, invalid("%v", err)
	}
	var out []XFileResult
	for _, f := range files {
		r := XFileResult{Name: f.Name, Issues: []xfile.Issue{}}
		s, err := lib.Recognise(f.Name, f.Data)
		if err != nil && f.Name != name {
			// a compressed file whose inner name says less than its own
			if s2, err2 := lib.Recognise(strings.TrimSuffix(strings.TrimSuffix(name, ".gz"), ".zip"), f.Data); err2 == nil {
				s, err = s2, nil
			}
		}
		if err != nil {
			r.Note = strings.TrimPrefix(err.Error(), "xfile: ")
			out = append(out, r)
			continue
		}
		r.Format, r.Title = s.ID, s.Title
		t, err := s.Read(f.Name, f.Data)
		if err != nil {
			r.Note = strings.TrimPrefix(err.Error(), "xfile: ")
			out = append(out, r)
			continue
		}
		for _, row := range t.Rows {
			if !row.Blank() {
				r.Rows++
			}
		}
		r.Problems = len(t.Issues)
		if len(t.Issues) > 50 {
			r.Issues = t.Issues[:50]
		} else {
			r.Issues = t.Issues
		}
		sum := sha256.Sum256(f.Data)
		rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "XFILE-" + strings.ToUpper(s.ID), Name: f.Name, SHA256: hex.EncodeToString(sum[:]), Content: f.Data})
		if err != nil {
			return out, err
		}
		detail := map[string]any{"format": s.ID, "rows": r.Rows, "problems": r.Problems, "file": rf.ID}
		switch {
		case !load:
			r.Loaded = "Checked only"
		case r.Problems > 0:
			r.Loaded = "Not loaded: the file does not match its layout"
		default:
			o.loadXFile(ctx, tenant, actor, s, f, t, &r)
		}
		detail["loaded"] = r.Loaded
		o.log(ctx, tenant, actor, "xfile.received", f.Name, detail)
		out = append(out, r)
	}
	return out, nil
}

// loadXFile hands a checked file to the part of Ledgerline that uses it.
func (o *Ops) loadXFile(ctx context.Context, tenant, actor string, s *xfile.Spec, f xfile.File, t *xfile.Table, r *XFileResult) {
	rows := dataRows(t)
	fail := func(err error) { r.Loaded = "Not loaded: " + strings.TrimPrefix(err.Error(), "invalid: ") }
	switch s.ID {
	case "ncl-trade", "mcx-trade":
		src := "MCXCCL-TRADE"
		if s.ID == "ncl-trade" {
			seg := ""
			if len(rows) > 0 {
				seg = strings.ToUpper(rows[0].Get("Sgmt"))
			}
			switch seg {
			case "CM", "FO", "CD":
				src = "NCL-" + seg + "-TRADE"
			default:
				fail(fmt.Errorf("segment %q in the file is not CM, FO or CD", seg))
				return
			}
		}
		res, err := o.App.IngestFile(ctx, tenant, actor, src, "", f.Name, f.Data)
		if err != nil {
			fail(err)
			return
		}
		r.Loaded, r.Added, r.Skipped = "Loaded as exchange trades", res.Added, res.DuplicateRows
	case "cdsl-holding":
		csv, n, skipped, err := o.sohToHoldings(ctx, tenant, rows)
		if err != nil {
			fail(err)
			return
		}
		if n == 0 {
			r.Loaded, r.Skipped = "Nothing to load: no demat account in the file belongs to a client", skipped
			return
		}
		res, err := o.LoadFile(ctx, tenant, actor, "DP-HOLDINGS", "", f.Name+" (holdings)", csv)
		if err != nil {
			fail(err)
			return
		}
		r.Loaded, r.Added, r.Skipped = "Loaded as client demat holdings", res.Added, skipped
		if skipped > 0 {
			r.Note = fmt.Sprintf("%d lines are for demat accounts not linked to a client (or the firm's own accounts) and were left out.", skipped)
		}
	case "ncl-position":
		csv, date, n, err := positionsCSV(rows)
		if err != nil {
			fail(err)
			return
		}
		if n == 0 {
			r.Loaded = "Nothing to load: no open positions"
			return
		}
		res, err := o.LoadFile(ctx, tenant, actor, "POSITIONS", date, f.Name+" (positions)", csv)
		if err != nil {
			fail(err)
			return
		}
		r.Loaded, r.Added = "Loaded as positions carried into "+date, res.Added
	case "stt-ctt":
		csv, date, err := sttCSV(rows)
		if err != nil {
			fail(err)
			return
		}
		res, err := o.LoadFile(ctx, tenant, actor, "STT-STATEMENT", date, f.Name+" (STT)", csv)
		if err != nil {
			fail(err)
			return
		}
		r.Loaded, r.Added = "Loaded for STT reconciliation", res.Added
	default:
		r.Loaded = "Checked and kept; Ledgerline does not use this file yet"
	}
}

func dataRows(t *xfile.Table) []xfile.Row {
	var out []xfile.Row
	for _, r := range t.Rows {
		if !r.Blank() && r.Record != "(text)" {
			out = append(out, r)
		}
	}
	return out
}

func decOr0(s string) money.Dec {
	d, err := money.ParseDec(strings.TrimSpace(s))
	if err != nil {
		return money.Dec{}
	}
	return d
}

// sohToHoldings turns a CDSL statement of holding into DP-HOLDINGS rows,
// matching each demat account to its client.
func (o *Ops) sohToHoldings(ctx context.Context, tenant string, rows []xfile.Row) ([]byte, int, int, error) {
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, 0, 0, err
	}
	byBO := map[string]string{}
	for _, c := range cs {
		if c.Demat.ID != "" {
			byBO[strings.TrimSpace(c.Demat.ID)] = c.UCC
		}
	}
	prev := map[string]Holding{}
	if hs, err := o.Holdings(ctx, tenant, ""); err == nil {
		for _, h := range hs {
			prev[holdingID(h.UCC, h.ISIN)] = h
		}
	}
	type agg struct{ qty, pledged money.Dec }
	sum := map[string]*agg{}
	var keys []string
	skipped := 0
	for _, r := range rows {
		ucc, ok := byBO[strings.TrimSpace(r.Get("BnfclOwnrId"))]
		if !ok {
			skipped++
			continue
		}
		k := ucc + "|" + strings.ToUpper(r.Get("ISIN"))
		a := sum[k]
		if a == nil {
			a = &agg{}
			sum[k] = a
			keys = append(keys, k)
		}
		a.qty = a.qty.Add(decOr0(r.Get("CurBal")))
		a.pledged = a.pledged.Add(decOr0(r.Get("PldgdBal")))
	}
	out := [][]string{{"UCC", "ISIN", "Symbol", "Qty", "Pledged", "AvgCost"}}
	for _, k := range keys {
		ucc, isin, _ := strings.Cut(k, "|")
		p := prev[holdingID(ucc, isin)]
		sym := p.Symbol
		if sym == "" {
			sym = isin
		}
		cost := p.AvgCost.Fixed(2)
		out = append(out, []string{ucc, isin, sym, sum[k].qty.Round(money.UnitRupee).Dec().Fixed(0), sum[k].pledged.Round(money.UnitRupee).Dec().Fixed(0), cost})
	}
	return csvBytesOf(out), len(keys), skipped, nil
}

// positionsCSV turns NSE Clearing's position file into POSITIONS rows
// (positions after exercise and assignment, at the settlement price).
func positionsCSV(rows []xfile.Row) ([]byte, string, int, error) {
	out := [][]string{{"UCC", "Exchange", "Segment", "Symbol", "Type", "Expiry", "Strike", "Qty", "AvgPrice"}}
	date := ""
	n := 0
	for _, r := range rows {
		if d := r.Get("BizDt"); d != "" {
			date = d
		}
		lng, _ := money.ParseDec(nz(r.Get("PstExrcAssgndLngQty")))
		sht, _ := money.ParseDec(nz(r.Get("PstExrcAssgndShrtQty")))
		q := lng.Sub(sht)
		if q.Sign() == 0 {
			continue
		}
		typ := "FUT"
		switch strings.ToUpper(r.Get("FinInstrmTp")) {
		case "IDO", "STO", "OPT", "OPTIDX", "OPTSTK":
			typ = strings.ToUpper(r.Get("OptnTp"))
		}
		strike := ""
		if typ != "FUT" {
			strike = r.Get("StrkPric")
		}
		ex := strings.ToUpper(r.Get("Src"))
		if ex == "" || ex == "NCL" {
			ex = "NSE"
		}
		out = append(out, []string{strings.ToUpper(r.Get("ClntId")), ex, strings.ToUpper(r.Get("Sgmt")), strings.ToUpper(r.Get("TckrSymb")), typ, r.Get("XpryDt"), strike, q.Round(money.UnitRupee).Dec().Fixed(0), nz(r.Get("SttlmPric"))})
		n++
	}
	if date == "" {
		return nil, "", 0, fmt.Errorf("the file has no business date")
	}
	return csvBytesOf(out), date, n, nil
}

func nz(s string) string {
	if strings.TrimSpace(s) == "" {
		return "0"
	}
	return strings.TrimSpace(s)
}

// sttCSV totals an STT/CTT file by exchange and segment.
func sttCSV(rows []xfile.Row) ([]byte, string, error) {
	type tot struct{ turnover, tax money.Dec }
	by := map[string]*tot{}
	var keys []string
	date := ""
	for _, r := range rows {
		if d := r.Get("TradDt"); d != "" {
			date = d
		}
		ex := strings.ToUpper(r.Get("Src"))
		switch ex {
		case "MCXCCL", "MCX":
			ex = "MCX"
		case "NCL", "":
			ex = "NSE"
		}
		seg := strings.ToUpper(r.Get("Sgmt"))
		if seg == "CO" {
			seg = "COM"
		}
		k := ex + "|" + seg
		t := by[k]
		if t == nil {
			t = &tot{}
			by[k] = t
			keys = append(keys, k)
		}
		for _, c := range []string{"TtlBuyTrfVal", "TtlSellTrfVal", "TaxblSellFutrsVal", "TaxblSellOptnVal", "TaxblExrcVal"} {
			t.turnover = t.turnover.Add(decOr0(r.Get(c)))
		}
		t.tax = t.tax.Add(decOr0(r.Get("TtlTaxs")))
	}
	if date == "" {
		return nil, "", fmt.Errorf("the file has no trade date")
	}
	out := [][]string{{"Exchange", "Segment", "Turnover", "STT"}}
	for _, k := range keys {
		ex, seg, _ := strings.Cut(k, "|")
		out = append(out, []string{ex, seg, by[k].turnover.Fixed(2), by[k].tax.Fixed(2)})
	}
	return csvBytesOf(out), date, nil
}

// ---- outgoing files ----

// firmPAN is the trading member's PAN: set in Settings, else taken from the
// firm's GSTIN.
func firmPAN(set Settings) string {
	if p := strings.ToUpper(strings.TrimSpace(set.FirmPAN)); p != "" {
		return p
	}
	if g := strings.TrimSpace(set.FirmGSTIN); len(g) == 15 {
		return strings.ToUpper(g[2:12])
	}
	return ""
}

// XFileOptions choose what an outgoing file covers.
type XFileOptions struct {
	Exchange string `json:"exchange"` // NSE (default) or MCX, for allocation files
	Segment  string `json:"segment"`  // CM, FO, CD or COM, for the collateral file
	Seq      int    `json:"seq"`      // file number of the day (default 1)
}

// ExchangeFile writes an outgoing file for a date.
func (o *Ops) ExchangeFile(ctx context.Context, tenant, actor, id, date string, opt XFileOptions) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Downloading exchange files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return "", nil, err
	}
	if !validDate(date) {
		return "", nil, invalid("give the date the file is for")
	}
	if !xWriters[id] {
		return "", nil, invalid("Ledgerline does not write %s yet", id)
	}
	lib, err := XFiles()
	if err != nil {
		return "", nil, err
	}
	s, _ := lib.Get(id)
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return "", nil, err
	}
	if opt.Seq <= 0 {
		opt.Seq = 1
	}
	vars := xfile.Vars{"date": date, "seq": fmt.Sprint(opt.Seq)}
	var name string
	var content []byte
	switch id {
	case "ncl-allocation", "ncl-allocation-api":
		name, content, err = o.allocationFile(ctx, tenant, s, set, date, opt, vars)
	case "holding-statement-api":
		name, content, err = o.holdingStatementFile(ctx, tenant, s, set, date, vars)
	case "segmon-client-funds":
		name, content, err = o.segmonFile(ctx, tenant, s, set, date, vars)
	case "ncl-client-collateral":
		return o.ClientCollateralFile(ctx, tenant, actor, date, opt.Segment, false)
	}
	if err != nil {
		return "", nil, err
	}
	o.log(ctx, tenant, actor, "xfile.written", name, map[string]any{"format": id, "date": date, "bytes": len(content)})
	return name, content, nil
}

// allocationRows are the day's client allocations for NSE Clearing (cash,
// F&O, currency) or MCXCCL (commodities).
func (o *Ops) allocationRows(ctx context.Context, tenant string, set Settings, date, exchange string) ([]map[string]string, error) {
	al, err := o.Allocation(ctx, tenant, date)
	if err != nil {
		return nil, err
	}
	tm := strings.TrimSpace(set.MemberCodes["NSE"])
	cm := firstNonEmptyStr(set.MemberCodes["NCL"], tm)
	if exchange == "MCX" {
		tm = strings.TrimSpace(set.MemberCodes["MCX"])
		cm = firstNonEmptyStr(set.MemberCodes["MCXCCL"], tm)
	}
	if tm == "" {
		return nil, invalid("set the firm's %s member code in Settings first", exchange)
	}
	var out []map[string]string
	for _, a := range al {
		if (exchange == "MCX") != (a.Segment == "COM") {
			continue
		}
		seg := a.Segment
		if seg == "COM" {
			seg = "CO"
		}
		amt := a.Cash.Add(a.NonCash)
		out = append(out, map[string]string{"curDate": date, "segment": seg, "cmCode": cm, "tmCode": tm, "cpCode": "", "cliCode": a.UCC, "accType": "C", "amt": amt.Fixed(2), "action": "U"})
	}
	return out, nil
}

func firstNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return b
}

func (o *Ops) allocationFile(ctx context.Context, tenant string, s *xfile.Spec, set Settings, date string, opt XFileOptions, vars xfile.Vars) (string, []byte, error) {
	ex := strings.ToUpper(firstNonEmptyStr(opt.Exchange, "NSE"))
	if ex != "NSE" && ex != "MCX" {
		return "", nil, invalid("exchange must be NSE or MCX")
	}
	rows, err := o.allocationRows(ctx, tenant, set, date, ex)
	if err != nil {
		return "", nil, err
	}
	vars["member"] = rows0(rows, "tmCode", set.MemberCodes[ex])
	if s.ID == "ncl-allocation" {
		if ex == "MCX" {
			vars["member"] = "M" + vars["member"]
		}
		name, err := s.FileName(vars)
		if err != nil {
			return "", nil, invalid("%v", err)
		}
		xr := make([]xfile.Row, len(rows))
		for i, r := range rows {
			xr[i] = xfile.Row{Values: r}
		}
		b, err := s.Write(xr)
		return name, b, err
	}
	// The API message.
	type req struct {
		CurDate string  `json:"curDate"`
		Segment string  `json:"segment"`
		CMCode  string  `json:"cmCode"`
		TMCode  string  `json:"tmCode"`
		CPCode  string  `json:"cpCode"`
		CliCode string  `json:"cliCode"`
		AccType string  `json:"accType"`
		Amt     float64 `json:"amt"`
		Filler1 string  `json:"filler1"`
		Filler2 string  `json:"filler2"`
		Filler3 string  `json:"filler3"`
		Filler4 string  `json:"filler4"`
		Filler5 string  `json:"filler5"`
		Filler6 string  `json:"filler6"`
		Action  string  `json:"action"`
	}
	d, _ := time.Parse("2006-01-02", date)
	var list []req
	for _, r := range rows {
		amt := decOr0(r["amt"]).Float64()
		list = append(list, req{CurDate: strings.ToUpper(d.Format("02-Jan-2006")), Segment: r["segment"], CMCode: r["cmCode"], TMCode: r["tmCode"], CliCode: r["cliCode"], AccType: r["accType"], Amt: amt, Action: r["action"]})
	}
	if list == nil {
		list = []req{}
	}
	msg := map[string]any{"version": "1.0", "data": map[string]any{"msgId": msgID(), "requestType": "I", "allocationRequest": list}}
	b, err := json.Marshal(msg)
	if err != nil {
		return "", nil, err
	}
	name, err := s.FileName(vars)
	if err != nil {
		return "", nil, invalid("%v", err)
	}
	return name, b, nil
}

func rows0(rows []map[string]string, k, def string) string {
	if len(rows) > 0 && rows[0][k] != "" {
		return rows[0][k]
	}
	return strings.TrimSpace(def)
}

// msgID is a 20-digit message id.
func msgID() string {
	n, _ := rand.Int(rand.Reader, new(big.Int).Exp(big.NewInt(10), big.NewInt(12), nil))
	return time.Now().Format("20060102") + fmt.Sprintf("%012d", n)
}

// holdingStatementFile is the daily client holding report sent to the
// exchanges through their API.
func (o *Ops) holdingStatementFile(ctx context.Context, tenant string, s *xfile.Spec, set Settings, date string, vars xfile.Vars) (string, []byte, error) {
	pan := firmPAN(set)
	if pan == "" {
		return "", nil, invalid("set the firm's PAN (or GSTIN) in Settings first")
	}
	hs, err := o.Holdings(ctx, tenant, "")
	if err != nil {
		return "", nil, err
	}
	cs, _ := o.ClientMap(ctx, tenant)
	sort.SliceStable(hs, func(i, j int) bool { return holdingID(hs[i].UCC, hs[i].ISIN) < holdingID(hs[j].UCC, hs[j].ISIN) })
	type item struct {
		RecID           string `json:"recId"`
		Dmat            string `json:"dmat"`
		AccountType     string `json:"accountType"`
		UCC             string `json:"ucc"`
		ClientName      string `json:"clientName"`
		PAN             string `json:"pan"`
		ISIN            string `json:"isin"`
		SecurityType    string `json:"securityType"`
		NameOfCommodity string `json:"nameOfCommodity"`
		UnitType        string `json:"unitType"`
		TotalPldgQty    string `json:"totalPldgQty"`
		FreeBalQty      string `json:"freeBalQty"`
		TotalQty        string `json:"totalQty"`
	}
	d, _ := time.Parse("2006-01-02", date)
	items := []item{}
	for _, h := range hs {
		if h.Qty <= 0 {
			continue
		}
		c := cs[h.UCC]
		q3 := func(n int64) string { return fmt.Sprintf("%d.000", n) }
		items = append(items, item{RecID: fmt.Sprint(len(items) + 1), Dmat: c.Demat.ID, AccountType: "OWN", UCC: h.UCC, ClientName: c.Name, PAN: c.PAN, ISIN: h.ISIN,
			SecurityType: "EQ", NameOfCommodity: "NA", UnitType: "NA", TotalPldgQty: q3(h.Pledged), FreeBalQty: q3(h.Qty - h.Pledged), TotalQty: q3(h.Qty)})
	}
	msg := struct {
		MemberPan      string `json:"memberPan"`
		FilePeriodDate string `json:"filePeriodDate"`
		Date           string `json:"date"`
		HoldingData    []item `json:"holdingData"`
	}{pan, d.Format("02-01-2006"), d.Format("02-01-2006"), items}
	b, err := json.Marshal(msg)
	if err != nil {
		return "", nil, err
	}
	vars["member_pan"] = pan
	name, err := s.FileName(vars)
	if err != nil {
		return "", nil, invalid("%v", err)
	}
	return name, b, nil
}

// segmonFile is the daily client funds and collateral report (65 columns).
// Mapping: ledger balance (A) and clear balance (B, less uncleared cheques)
// from the client ledger; peak clear balance (C) is the end-of-day clear
// balance (Ledgerline does not yet keep intraday ledger peaks); securities
// pledged as margin are "Approved Securities Non-cash component received";
// each client is reported in each segment they hold an allocation in, with
// the ledger balance and securities on their first segment. Columns for
// collateral Ledgerline does not hold (BG, FDR, commodities) are zero. A
// firm checks the first files against the previous system before using it.
func (o *Ops) segmonFile(ctx context.Context, tenant string, s *xfile.Spec, set Settings, date string, vars xfile.Vars) (string, []byte, error) {
	pan := firmPAN(set)
	if pan == "" {
		return "", nil, invalid("set the firm's PAN (or GSTIN) in Settings first")
	}
	cmPAN := firstNonEmptyStr(set.ClearingMemberPAN, pan)
	funds, err := o.clientFunds(ctx, tenant, date, "")
	if err != nil {
		return "", nil, err
	}
	unc := o.uncleared(ctx, tenant)
	rv, err := o.Risk(ctx, tenant, date)
	if err != nil {
		return "", nil, err
	}
	coll := map[string]money.Dec{}
	for _, r := range rv.Rows {
		coll[r.UCC] = r.Collateral
	}
	al, err := o.Allocation(ctx, tenant, date)
	if err != nil {
		return "", nil, err
	}
	segs := map[string][]string{}
	for _, a := range al {
		seg := a.Segment
		if seg == "COM" {
			continue // commodities are reported to MCX separately
		}
		if !contains(segs[a.UCC], seg) {
			segs[a.UCC] = append(segs[a.UCC], seg)
		}
	}
	cs, _ := o.ClientMap(ctx, tenant)
	uccs := make([]string, 0, len(cs))
	for u, c := range cs {
		if c.Status == "Active" || funds[u] != 0 || len(segs[u]) > 0 {
			uccs = append(uccs, u)
		}
	}
	sort.Strings(uccs)
	cols := s.Columns
	zero := func() map[string]string {
		m := map[string]string{}
		for _, c := range cols {
			if c.Type == "dec" {
				m[c.Key] = "0"
			}
		}
		return m
	}
	byName := func(prefix string) string {
		for _, c := range cols {
			if strings.HasPrefix(c.Name, prefix) {
				return c.Key
			}
		}
		panic("segmon column " + prefix)
	}
	kA, kB, kC := byName("Financial Ledger balance-A"), byName("Financial Ledger balance (clear)-B"), byName("Peak Financial Ledger Balance")
	kSec := byName("Approved Securities Non-cash component received by TM")
	d, _ := time.Parse("2006-01-02", date)
	var rows []xfile.Row
	for _, u := range uccs {
		ss := segs[u]
		if len(ss) == 0 {
			ss = []string{"CM"}
		}
		sort.SliceStable(ss, func(i, j int) bool { return segRank(ss[i]) < segRank(ss[j]) })
		for i, seg := range ss {
			m := zero()
			m["Date"] = d.Format("02-01-2006")
			m["Clearing Member PAN"], m["Trading member PAN"], m["Client PAN"] = cmPAN, pan, cs[u].PAN
			m["Account Type"], m["Segment Indicator"], m["UCC Code"] = "C", seg, u
			m["MTF /Non MTF indicator/Reason Code"] = "NA"
			if seg == "CM" {
				m["MTF /Non MTF indicator/Reason Code"] = "NON MTF"
			}
			m["Unclaimed/Unsettled Client Funds"] = "NA"
			if i == 0 {
				a := funds[u].Dec()
				b := (funds[u] - unc[u]).Dec()
				m[kA], m[kB], m[kC] = a.Fixed(2), b.Fixed(2), b.Fixed(2)
				m[kSec] = coll[u].Fixed(2)
			}
			rows = append(rows, xfile.Row{Values: m})
		}
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, err
	}
	vars["member_pan"] = pan
	name, err := s.FileName(vars)
	if err != nil {
		return "", nil, invalid("%v", err)
	}
	return name, b, nil
}

func segRank(s string) int {
	return map[string]int{"CM": 0, "FO": 1, "CD": 2}[s]
}
