package ops

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- AMFI's daily NAV file ----
//
// AMFI publishes every scheme's NAV each business evening at
// https://www.amfiindia.com/spages/NAVAll.txt: semicolon-separated lines
// "Scheme Code;ISIN Div Payout/ ISIN Growth;ISIN Div Reinvestment;Scheme
// Name;Net Asset Value;Date" (date as 29-Oct-2026), under heading lines
// naming the category ("Open Ended Schemes(Debt Scheme - Liquid Fund)")
// and the fund house. A missing ISIN is "-" and a missing NAV "N.A.". The
// file could not be opened from here to check against it; the reader is
// lenient (any line with six fields and a numeric scheme code is a
// scheme, anything else a heading). When the evening fetch fails, staff
// upload a copy.

// mfNAVURL is AMFI's file; mfNAVHTTP fetches it. Tests replace both.
var (
	mfNAVURL  = "https://www.amfiindia.com/spages/NAVAll.txt"
	mfNAVHTTP = &http.Client{Timeout: 2 * time.Minute}
)

// MFNAVState is where the NAV feed stands.
type MFNAVState struct {
	NAVDate   string `json:"nav_date"`
	Schemes   int    `json:"schemes"`
	Changed   int    `json:"changed"`
	Source    string `json:"source"` // AMFI or the uploaded file's name
	LastTry   string `json:"last_try,omitempty"`
	LastOK    string `json:"last_ok,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

type amfiRow struct {
	Code, ISIN1, ISIN2, Name, AMC, Category string
	NAV                                     money.Dec
	Date                                    string
}

var reISIN = regexp.MustCompile(`^IN[A-Z0-9]{9}[0-9]$`)

const utf8BOM = "\xef\xbb\xbf"

// parseAMFINAV reads AMFI's file; it returns the latest NAV date in it.
func parseAMFINAV(b []byte) ([]amfiRow, string, error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var rows []amfiRow
	amc, cat, latest := "", "", ""
	header := false
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), utf8BOM))
		if line == "" {
			continue
		}
		f := strings.Split(line, ";")
		if !header {
			if !strings.HasPrefix(strings.ToLower(f[0]), "scheme code") {
				return nil, "", invalid("this is not AMFI's NAV file (the first line should start Scheme Code;…)")
			}
			header = true
			continue
		}
		if len(f) < 6 || strings.Trim(f[0], "0123456789 ") != "" {
			if strings.Contains(line, "Schemes(") || strings.Contains(line, "Scheme(") || strings.Contains(line, "Schemes (") {
				cat = line
			} else if !strings.Contains(line, ";") {
				amc = line
			}
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		nav, err := money.ParseDec(strings.ReplaceAll(f[4], ",", ""))
		if err != nil || nav.Sign() <= 0 {
			continue // N.A.
		}
		t, err := time.Parse("02-Jan-2006", f[5])
		if err != nil {
			continue
		}
		d := t.Format("2006-01-02")
		if d > latest {
			latest = d
		}
		rows = append(rows, amfiRow{Code: f[0], ISIN1: strings.ToUpper(f[1]), ISIN2: strings.ToUpper(f[2]), Name: f[3], AMC: amc, Category: cat, NAV: nav, Date: d})
	}
	if !header {
		return nil, "", invalid("the NAV file is empty")
	}
	if len(rows) == 0 {
		return nil, "", invalid("no NAVs in the file")
	}
	return rows, latest, nil
}

// LoadMFNAV loads AMFI's NAV file (fetched or uploaded) into the scheme
// master.
func (o *Ops) LoadMFNAV(ctx context.Context, tenant, actor, name string, data []byte) (MFNAVState, error) {
	if err := o.require(ctx, tenant, actor, "Loading MF files", RoleMaker, RoleChecker); err != nil {
		return MFNAVState{}, err
	}
	return o.loadNAV(ctx, tenant, actor, firstOf(name, "upload"), data)
}

func (o *Ops) loadNAV(ctx context.Context, tenant, actor, source string, data []byte) (MFNAVState, error) {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cMFState, "nav", MFNAVState{})
	rows, latest, err := parseAMFINAV(data)
	if err != nil {
		return st, err
	}
	have, err := docs.List[MFScheme](ctx, o.Docs, tenant, cMFScheme, "")
	if err != nil {
		return st, err
	}
	cur := make(map[string]MFScheme, len(have))
	for _, s := range have {
		cur[s.ISIN] = s
	}
	changed, n := 0, 0
	for _, r := range rows {
		for _, isin := range []string{r.ISIN1, r.ISIN2} {
			if !reISIN.MatchString(isin) {
				continue
			}
			n++
			s, ok := cur[isin]
			if ok && s.NAVDate > r.Date {
				continue
			}
			if ok && s.NAVDate == r.Date && s.NAV.Cmp(r.NAV) == 0 && s.Name == r.Name {
				continue
			}
			s.ISIN, s.AMFICode, s.Name, s.NAV, s.NAVDate = isin, r.Code, r.Name, r.NAV, r.Date
			if r.AMC != "" {
				s.AMC = r.AMC
			}
			if r.Category != "" {
				s.Category = r.Category
			}
			s.Plan = mfPlanOf(r.Name)
			if s.MonthEnd == nil {
				s.MonthEnd = map[string]string{}
			}
			s.MonthEnd[r.Date[:7]] = r.NAV.String()
			if len(s.MonthEnd) > 36 {
				ks := sortedKeys(s.MonthEnd)
				for _, k := range ks[:len(ks)-36] {
					delete(s.MonthEnd, k)
				}
			}
			if err := docs.Upsert(ctx, o.Docs, tenant, cMFScheme, isin, s, actor); err != nil {
				return st, err
			}
			cur[isin] = s
			changed++
		}
	}
	now := o.now().Format(time.RFC3339)
	st.NAVDate, st.Schemes, st.Changed, st.Source, st.LastOK, st.LastError = latest, n, changed, source, now, ""
	_ = docs.Upsert(ctx, o.Docs, tenant, cMFState, "nav", st, actor)
	o.log(ctx, tenant, actor, "mf.nav.loaded", "nav", map[string]any{"date": latest, "schemes": n, "changed": changed, "source": source})
	return st, nil
}

// FetchMFNAV fetches AMFI's NAV file now and loads it.
func (o *Ops) FetchMFNAV(ctx context.Context, tenant, actor string) (MFNAVState, error) {
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Loading MF files", RoleMaker, RoleChecker); err != nil {
			return MFNAVState{}, err
		}
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cMFState, "nav", MFNAVState{})
	st.LastTry = o.now().Format(time.RFC3339)
	fail := func(err error) (MFNAVState, error) {
		st.LastError = err.Error() + " — upload a copy of the file instead"
		_ = docs.Upsert(ctx, o.Docs, tenant, cMFState, "nav", st, SystemActor)
		return st, fmt.Errorf("%w: %v", ErrState, st.LastError)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mfNAVURL, nil)
	if err != nil {
		return fail(err)
	}
	resp, err := mfNAVHTTP.Do(req)
	if err != nil {
		return fail(fmt.Errorf("AMFI's NAV file could not be fetched: %v", err))
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != 200 {
		return fail(fmt.Errorf("AMFI answered %d", resp.StatusCode))
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cMFState, "nav", st, SystemActor)
	out, err := o.loadNAV(ctx, tenant, actor, "AMFI", b)
	if err != nil {
		return fail(err)
	}
	return out, nil
}

// ---- other MF files: column-mapped imports ----
//
// The platforms' report files (scheme master, order status, allotment and
// redemption statements, mandate status) and the RTAs' mailback feeds
// (KFintech MFSD201/307 transactions, MFSD205-208 brokerage, MFSD211/311
// investor master, MFSD313/314 SIPs; CAMS WBR mailbacks) come as CSV or
// DBF files with layouts given to members and distributors, not published
// in anything read for this. So they load through a mapping: the firm
// says once which column of its file holds each field, and the mapping is
// kept under a name for next time. DBF files are saved as CSV first.
//
// TODO: when a firm shares a sample of each KFintech MFSD and CAMS WBR
// file (or the RTAs publish the layouts), add their mappings here as
// named presets.

// MFImportField is one field an import kind takes.
type MFImportField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Required bool   `json:"required,omitempty"`
}

// MFImportKind is a kind of MF file.
type MFImportKind struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	About  string          `json:"about"`
	Fields []MFImportField `json:"fields"`
	// Platform: the kind needs the platform the file came from.
	Platform bool `json:"platform,omitempty"`
}

func mfF(key, label string, req bool) MFImportField { return MFImportField{key, label, req} }

// MFImportKinds are the files that load through a mapping.
var MFImportKinds = []MFImportKind{
	{ID: "scheme-codes", Name: "Platform scheme master", Platform: true, About: "Each scheme's code on the platform, by ISIN, and what it allows (Y/N). Needed before orders or SIPs in a scheme",
		Fields: []MFImportField{mfF("code", "Scheme code", true), mfF("isin", "ISIN", true), mfF("amc_code", "AMC code (NSE SIPs)", false), mfF("purchase", "Purchase allowed", false),
			mfF("redeem", "Redemption allowed", false), mfF("sip", "SIP allowed", false), mfF("min_purchase", "Minimum purchase", false)}},
	{ID: "order-status", Name: "Order status report", Platform: true, About: "The platform's status of each order placed here",
		Fields: []MFImportField{mfF("order_no", "Order number", true), mfF("status", "Status", true), mfF("remark", "Remarks", false)}},
	{ID: "allotments", Name: "Allotment or redemption statement", Platform: true, About: "Units allotted (or redeemed) against orders placed here; moves the units ledger",
		Fields: []MFImportField{mfF("order_no", "Order number", true), mfF("units", "Units", true), mfF("nav", "NAV", false), mfF("amount", "Amount", false),
			mfF("date", "Allotment date", false), mfF("folio", "Folio", false)}},
	{ID: "mandate-status", Name: "Mandate status report", Platform: true, About: "Registration status of mandates, by the platform's mandate number or UMRN",
		Fields: []MFImportField{mfF("mandate_id", "Mandate number", false), mfF("umrn", "UMRN", false), mfF("status", "Status", true), mfF("remark", "Remarks", false)}},
	{ID: "rta-transactions", Name: "RTA transaction feed (KFintech, CAMS)", About: "Units bought, redeemed or switched in folios, from the RTA's mailback; adds to the units ledger (rows already loaded are skipped)",
		Fields: []MFImportField{mfF("folio", "Folio", true), mfF("isin", "ISIN", true), mfF("date", "Transaction date", true), mfF("units", "Units (negative or with a redemption type for units out)", true),
			mfF("type", "Transaction type", false), mfF("nav", "NAV", false), mfF("amount", "Amount", false), mfF("pan", "PAN", false), mfF("ucc", "Client code", false), mfF("ref", "Transaction number", false)}},
	{ID: "rta-brokerage", Name: "RTA brokerage (trail) statement", About: "Commission the fund houses paid the firm, for reconciliation with the accruals",
		Fields: []MFImportField{mfF("month", "Month or date", true), mfF("isin", "ISIN", false), mfF("scheme", "Scheme name or code", false), mfF("amount", "Brokerage amount", true),
			mfF("folio", "Folio", false), mfF("amc", "Fund house", false), mfF("arn", "ARN", false), mfF("type", "Brokerage type", false)}},
}

// MFImportMapping is a saved mapping of a firm's file columns.
type MFImportMapping struct {
	Name     string            `json:"name"`
	Kind     string            `json:"kind"`
	Platform string            `json:"platform,omitempty"`
	Columns  map[string]string `json:"columns"` // field -> the file's column heading
	By       string            `json:"by,omitempty"`
}

const cMFMap = "mfmap"

// MFImportMappings lists saved mappings.
func (o *Ops) MFImportMappings(ctx context.Context, tenant string) ([]MFImportMapping, error) {
	return docs.List[MFImportMapping](ctx, o.Docs, tenant, cMFMap, "")
}

// MFImportResult is what an import did.
type MFImportResult struct {
	Kind    string   `json:"kind"`
	Rows    int      `json:"rows"`
	Applied int      `json:"applied"`
	Skipped []string `json:"skipped"`
	Columns []string `json:"columns"` // the file's headings
}

// MFImportRequest is one file to import.
type MFImportRequest struct {
	Kind     string            `json:"kind"`
	Platform string            `json:"platform,omitempty"`
	Mapping  string            `json:"mapping,omitempty"` // a saved mapping's name
	Columns  map[string]string `json:"columns,omitempty"`
	SaveAs   string            `json:"save_as,omitempty"`
	Name     string            `json:"name,omitempty"`
}

var normHead = strings.NewReplacer(" ", "", "_", "", "-", "", ".", "")

// readMFTable reads a delimited file (comma, pipe, semicolon or tab, as
// the first line shows).
func readMFTable(data []byte) ([]string, [][]string, error) {
	data = bytes.TrimPrefix(data, []byte(utf8BOM))
	first, _, _ := bytes.Cut(data, []byte("\n"))
	best, sep := -1, ','
	for _, c := range []rune{',', '|', ';', '\t'} {
		if n := bytes.Count(first, []byte(string(c))); n > best {
			best, sep = n, c
		}
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma, r.FieldsPerRecord, r.LazyQuotes, r.TrimLeadingSpace = sep, -1, true, true
	recs, err := r.ReadAll()
	if err != nil {
		return nil, nil, invalid("the file could not be read as a table: %v", err)
	}
	if len(recs) < 2 {
		return nil, nil, invalid("the file has no rows under its headings")
	}
	return recs[0], recs[1:], nil
}

// ImportMFFile loads a mapped MF file.
func (o *Ops) ImportMFFile(ctx context.Context, tenant, actor string, req MFImportRequest, data []byte) (MFImportResult, error) {
	if err := o.require(ctx, tenant, actor, "Loading MF files", RoleMaker, RoleChecker); err != nil {
		return MFImportResult{}, err
	}
	var kind MFImportKind
	for _, k := range MFImportKinds {
		if k.ID == req.Kind {
			kind = k
		}
	}
	if kind.ID == "" {
		return MFImportResult{}, invalid("unknown MF file kind %q", req.Kind)
	}
	cols := req.Columns
	if req.Mapping != "" {
		m, _, err := docs.Get[MFImportMapping](ctx, o.Docs, tenant, cMFMap, req.Mapping)
		if err != nil || m.Kind != kind.ID {
			return MFImportResult{}, invalid("no saved %s mapping called %q", kind.Name, req.Mapping)
		}
		cols = m.Columns
		if req.Platform == "" {
			req.Platform = m.Platform
		}
	}
	req.Platform = strings.ToUpper(req.Platform)
	if _, ok := MFPlatformNames[req.Platform]; kind.Platform && !ok {
		return MFImportResult{}, invalid("say which platform the file is from (BSE or NSE)")
	}
	head, rows, err := readMFTable(data)
	if err != nil {
		return MFImportResult{}, err
	}
	res := MFImportResult{Kind: kind.ID, Rows: len(rows), Columns: head, Skipped: []string{}}
	idx := map[string]int{}
	for _, f := range kind.Fields {
		want := cols[f.Key]
		for i, h := range head {
			hn := strings.ToLower(normHead.Replace(strings.TrimSpace(h)))
			if (want != "" && strings.EqualFold(strings.TrimSpace(h), strings.TrimSpace(want))) || (want == "" && hn == strings.ToLower(normHead.Replace(f.Key))) {
				idx[f.Key] = i
				break
			}
		}
		if _, ok := idx[f.Key]; !ok && f.Required {
			return res, invalid("say which column holds %q (the file's columns: %s)", f.Label, strings.Join(head, ", "))
		}
	}
	get := func(r []string, k string) string {
		if i, ok := idx[k]; ok && i < len(r) {
			return strings.TrimSpace(r[i])
		}
		return ""
	}
	if req.SaveAs != "" {
		used := map[string]string{}
		for k, i := range idx {
			used[k] = head[i]
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cMFMap, req.SaveAs, MFImportMapping{Name: req.SaveAs, Kind: kind.ID, Platform: req.Platform, Columns: used, By: actor}, actor)
	}
	switch kind.ID {
	case "scheme-codes":
		res.Applied, res.Skipped, err = o.importSchemeCodes(ctx, tenant, actor, req.Platform, rows, get)
	case "order-status":
		var rs []mfStatusRow
		for _, r := range rows {
			rs = append(rs, mfStatusRow{OrderNo: get(r, "order_no"), Status: get(r, "status"), Remark: get(r, "remark")})
		}
		res.Applied, err = o.mfApplyStatuses(ctx, tenant, actor, rs)
	case "allotments":
		var rs []mfAllotRow
		for _, r := range rows {
			rs = append(rs, mfAllotRow{OrderNo: get(r, "order_no"), Units: get(r, "units"), NAV: get(r, "nav"), Amount: get(r, "amount"), Date: mfFileDate(get(r, "date")), Folio: get(r, "folio")})
		}
		res.Applied, res.Skipped, err = o.mfApplyAllotments(ctx, tenant, actor, "platform file", rs)
	case "mandate-status":
		var rs []mfMandateRow
		for _, r := range rows {
			rs = append(rs, mfMandateRow{MandateID: get(r, "mandate_id"), UMRN: get(r, "umrn"), Status: get(r, "status"), Remark: get(r, "remark")})
		}
		res.Applied, err = o.mfApplyMandates(ctx, tenant, actor, rs)
	case "rta-transactions":
		res.Applied, res.Skipped, err = o.importRTATxns(ctx, tenant, actor, rows, get)
	case "rta-brokerage":
		res.Applied, res.Skipped, err = o.importRTABrokerage(ctx, tenant, actor, firstOf(req.Name, "file"), rows, get)
	}
	if res.Skipped == nil {
		res.Skipped = []string{}
	}
	if len(res.Skipped) > 50 {
		res.Skipped = append(res.Skipped[:50], fmt.Sprintf("…and %d more", len(res.Skipped)-50))
	}
	o.log(ctx, tenant, actor, "mf.file.imported", kind.ID, map[string]any{"rows": res.Rows, "applied": res.Applied, "skipped": len(res.Skipped), "platform": req.Platform, "name": req.Name})
	return res, err
}

// mfFileDate reads the dates RTA and platform files use.
func mfFileDate(s string) string {
	s = strings.TrimSpace(s)
	for _, f := range []string{"2006-01-02", "02/01/2006", "02-01-2006", "02-Jan-2006", "02-Jan-06", "20060102", "02/01/2006 15:04:05", "2006-01-02 15:04:05", "01/2006", "2006-01", "Jan-2006", "January 2006"} {
		if t, err := time.Parse(f, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

func ynAllowed(s string) bool {
	s = strings.ToUpper(strings.TrimSpace(s))
	return s == "" || s == "Y" || s == "YES" || s == "1" || s == "TRUE"
}

func (o *Ops) importSchemeCodes(ctx context.Context, tenant, actor, platform string, rows [][]string, get func([]string, string) string) (int, []string, error) {
	n := 0
	var skipped []string
	toggle := func(list []string, on bool) []string {
		out := []string{}
		for _, p := range list {
			if p != platform {
				out = append(out, p)
			}
		}
		if !on {
			out = append(out, platform)
		}
		return out
	}
	for i, r := range rows {
		isin, code := strings.ToUpper(get(r, "isin")), get(r, "code")
		if !reISIN.MatchString(isin) || code == "" {
			skipped = append(skipped, fmt.Sprintf("row %d: needs an ISIN and a scheme code", i+2))
			continue
		}
		_, err := docs.Update(ctx, o.Docs, tenant, cMFScheme, isin, actor, func(s *MFScheme) error {
			if s.Codes == nil {
				s.Codes = map[string]string{}
			}
			s.Codes[platform] = code
			if a := get(r, "amc_code"); a != "" {
				if s.AMCCode == nil {
					s.AMCCode = map[string]string{}
				}
				s.AMCCode[platform] = a
			}
			s.NoPurchase = toggle(s.NoPurchase, ynAllowed(get(r, "purchase")))
			s.NoRedeem = toggle(s.NoRedeem, ynAllowed(get(r, "redeem")))
			s.NoSIP = toggle(s.NoSIP, ynAllowed(get(r, "sip")))
			if m, err := money.ParseAmount(strings.ReplaceAll(get(r, "min_purchase"), ",", "")); err == nil && m > 0 {
				s.MinPurchase = m
			}
			return nil
		})
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("row %d: %s is not in AMFI's NAV file (load it first)", i+2, isin))
			continue
		}
		n++
	}
	return n, skipped, nil
}

// importRTATxns adds RTA feed rows to the units ledger, matching the
// folio's client by an earlier movement in the folio, the client code or
// the PAN. A row already loaded (same reference, or same folio, scheme,
// date and units) is skipped.
func (o *Ops) importRTATxns(ctx context.Context, tenant, actor string, rows [][]string, get func([]string, string) string) (int, []string, error) {
	all, err := docs.List[MFUnitTxn](ctx, o.Docs, tenant, cMFUnit, "")
	if err != nil {
		return 0, nil, err
	}
	folioOf := map[string]string{}
	seen := map[string]bool{}
	for _, t := range all {
		if t.Folio != "" {
			folioOf[t.Folio] = t.UCC
		}
		seen[t.Folio+"|"+t.ISIN+"|"+t.Date+"|"+t.Units.String()] = true
		if t.Order != "" {
			seen["ref|"+t.Order] = true
		}
	}
	cs, _ := o.Clients(ctx, tenant)
	byPAN := map[string]string{}
	for _, c := range cs {
		byPAN[c.PAN] = c.UCC
	}
	n := 0
	var skipped []string
	for i, r := range rows {
		folio, isin, date := get(r, "folio"), strings.ToUpper(get(r, "isin")), mfFileDate(get(r, "date"))
		units, err := money.ParseDec(strings.ReplaceAll(get(r, "units"), ",", ""))
		if err != nil || units.IsZero() || date == "" || folio == "" {
			skipped = append(skipped, fmt.Sprintf("row %d: needs the folio, a date and units", i+2))
			continue
		}
		typ := strings.ToUpper(get(r, "type"))
		out := strings.Contains(typ, "RED") || strings.Contains(typ, "SWITCH OUT") || strings.Contains(typ, "SWOUT") || typ == "SO"
		if out && units.Sign() > 0 {
			units = units.Neg()
		}
		ref := get(r, "ref")
		if (ref != "" && seen["ref|RTA:"+ref]) || seen[folio+"|"+isin+"|"+date+"|"+units.String()] {
			continue
		}
		ucc := strings.ToUpper(get(r, "ucc"))
		if ucc == "" {
			ucc = folioOf[folio]
		}
		if ucc == "" {
			ucc = byPAN[strings.ToUpper(get(r, "pan"))]
		}
		if ucc == "" {
			skipped = append(skipped, fmt.Sprintf("row %d: folio %s is not linked to a client (add the client code or PAN column)", i+2, folio))
			continue
		}
		sc, err := o.mfScheme(ctx, tenant, isin)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("row %d: %s is not in the scheme master", i+2, isin))
			continue
		}
		amt, _ := money.ParseAmount(strings.TrimPrefix(strings.ReplaceAll(get(r, "amount"), ",", ""), "-"))
		kind := "RTA"
		switch {
		case out:
			kind = "Redemption"
		case strings.Contains(typ, "SIP"):
			kind = "SIP"
		case strings.Contains(typ, "PUR"):
			kind = "Purchase"
		}
		t := MFUnitTxn{UCC: ucc, Mode: "Physical", Folio: folio, ISIN: isin, Scheme: sc.Name, AMC: sc.AMC, Date: date, Kind: kind, Units: units, NAV: dec(get(r, "nav")), Amount: amt, Source: "rta file"}
		if ref != "" {
			t.Order = "RTA:" + ref
		}
		if err := o.addUnits(ctx, tenant, actor, t); err != nil {
			return n, skipped, err
		}
		seen[folio+"|"+isin+"|"+date+"|"+units.String()] = true
		folioOf[folio] = ucc
		n++
	}
	return n, skipped, nil
}

// ---- trail commission ----

const (
	cMFAccrual = "mfaccrual" // key: month|UCC|ISIN|folio
	cMFBrok    = "mfbrok"    // key: month|source|row
)

// MFAccrual is the trail expected on one holding for a month.
type MFAccrual struct {
	Month    string       `json:"month"` // YYYY-MM
	UCC      string       `json:"ucc"`
	ISIN     string       `json:"isin"`
	Scheme   string       `json:"scheme"`
	AMC      string       `json:"amc,omitempty"`
	Folio    string       `json:"folio,omitempty"`
	Units    money.Dec    `json:"units"`
	NAV      money.Dec    `json:"nav"`
	Value    money.Amount `json:"value"`
	RatePct  string       `json:"rate_pct"`
	Expected money.Amount `json:"expected"`
}

// MFBrokerage is one row of an RTA brokerage statement.
type MFBrokerage struct {
	Month  string       `json:"month"`
	ISIN   string       `json:"isin,omitempty"`
	Scheme string       `json:"scheme,omitempty"`
	AMC    string       `json:"amc,omitempty"`
	Folio  string       `json:"folio,omitempty"`
	ARN    string       `json:"arn,omitempty"`
	Type   string       `json:"type,omitempty"`
	Amount money.Amount `json:"amount"`
	Source string       `json:"source"`
}

func (o *Ops) importRTABrokerage(ctx context.Context, tenant, actor, source string, rows [][]string, get func([]string, string) string) (int, []string, error) {
	schemes, _ := docs.List[MFScheme](ctx, o.Docs, tenant, cMFScheme, "")
	byName := map[string]string{}
	for _, s := range schemes {
		byName[strings.ToLower(s.Name)] = s.ISIN
		for _, c := range s.Codes {
			byName[strings.ToLower(c)] = s.ISIN
		}
	}
	n := 0
	var skipped []string
	for i, r := range rows {
		month := mfFileDate(get(r, "month"))
		amt, err := money.ParseAmount(strings.ReplaceAll(get(r, "amount"), ",", ""))
		if month == "" || err != nil {
			skipped = append(skipped, fmt.Sprintf("row %d: needs the month and the amount", i+2))
			continue
		}
		b := MFBrokerage{Month: month[:7], ISIN: strings.ToUpper(get(r, "isin")), Scheme: get(r, "scheme"), AMC: get(r, "amc"), Folio: get(r, "folio"), ARN: get(r, "arn"),
			Type: get(r, "type"), Amount: amt, Source: source}
		if b.ISIN == "" {
			b.ISIN = byName[strings.ToLower(b.Scheme)]
		}
		key := fmt.Sprintf("%s|%s|%06d", b.Month, source, i+2)
		if err := docs.Upsert(ctx, o.Docs, tenant, cMFBrok, key, b, actor); err != nil {
			return n, skipped, err
		}
		n++
	}
	return n, skipped, nil
}

func (s MFSettings) trailRate(isin, amc string) (money.Dec, string, bool) {
	for _, k := range []string{isin, amc, "*"} {
		if v, ok := s.TrailPct[k]; ok && k != "" {
			return dec(v), v, true
		}
	}
	return money.Dec{}, "", false
}

// AccrueMFTrail works out the trail commission expected for a month on
// Regular plan holdings: units held at the month's end × the month-end
// NAV × the firm's rate ÷ 12. It replaces the month's earlier accruals.
func (o *Ops) AccrueMFTrail(ctx context.Context, tenant, actor, month string) ([]MFAccrual, error) {
	if err := o.require(ctx, tenant, actor, "Working out MF commission", RoleChecker, RoleCompliance); err != nil {
		return nil, err
	}
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return nil, invalid("the month is YYYY-MM")
	}
	end := t.AddDate(0, 1, -1).Format("2006-01-02")
	set, _ := o.MFSettingsGet(ctx, tenant)
	ts, err := docs.List[MFUnitTxn](ctx, o.Docs, tenant, cMFUnit, "")
	if err != nil {
		return nil, err
	}
	hs, _ := o.mfHoldings(ctx, tenant, ts, end)
	old, _ := docs.List[MFAccrual](ctx, o.Docs, tenant, cMFAccrual, month+"|")
	for _, a := range old {
		a.Expected, a.Value, a.Units = 0, 0, money.Dec{}
		_ = docs.Upsert(ctx, o.Docs, tenant, cMFAccrual, month+"|"+a.UCC+"|"+a.ISIN+"|"+a.Folio, a, actor)
	}
	out := []MFAccrual{}
	for _, h := range hs {
		if h.Plan != MFRegular {
			continue
		}
		rate, rs, ok := set.trailRate(h.ISIN, h.AMC)
		if !ok {
			continue
		}
		a := MFAccrual{Month: month, UCC: h.UCC, ISIN: h.ISIN, Scheme: h.Scheme, AMC: h.AMC, Folio: h.Folio, Units: h.Units, NAV: h.NAV, Value: h.Value, RatePct: rs,
			Expected: h.Value.Dec().Pct(rate).Mul(oneTwelfth).Round(money.UnitPaisa)}
		if err := docs.Upsert(ctx, o.Docs, tenant, cMFAccrual, month+"|"+a.UCC+"|"+a.ISIN+"|"+a.Folio, a, actor); err != nil {
			return out, err
		}
		out = append(out, a)
	}
	o.log(ctx, tenant, actor, "mf.trail.accrued", month, map[string]any{"holdings": len(out)})
	return out, nil
}

var oneTwelfth, _ = money.DecInt(1).Quo(money.DecInt(12))

// MFTrailLine is one scheme's commission for a month: expected against
// received.
type MFTrailLine struct {
	ISIN     string       `json:"isin"`
	Scheme   string       `json:"scheme"`
	AMC      string       `json:"amc,omitempty"`
	Expected money.Amount `json:"expected"`
	Received money.Amount `json:"received"`
	Diff     money.Amount `json:"difference"`
	Status   string       `json:"status"` // Matched, Short, Excess, Not received, Not expected
	Holdings int          `json:"holdings"`
}

// MFCommissionView is a month's trail commission.
type MFCommissionView struct {
	Month    string        `json:"month"`
	Lines    []MFTrailLine `json:"lines"`
	Accruals []MFAccrual   `json:"accruals"`
	Expected money.Amount  `json:"expected"`
	Received money.Amount  `json:"received"`
	Unmapped money.Amount  `json:"unmapped"` // received with no scheme recognised
	Files    []string      `json:"files"`
}

// MFCommission reconciles a month's accruals with the RTAs' brokerage
// statements, by scheme. Differences within 2% (at least ₹1) match.
func (o *Ops) MFCommission(ctx context.Context, tenant, month string) (MFCommissionView, error) {
	if month == "" {
		month = o.now().AddDate(0, -1, 0).Format("2006-01")
	}
	v := MFCommissionView{Month: month, Lines: []MFTrailLine{}, Accruals: []MFAccrual{}, Files: []string{}}
	acc, err := docs.List[MFAccrual](ctx, o.Docs, tenant, cMFAccrual, month+"|")
	if err != nil {
		return v, err
	}
	bs, err := docs.List[MFBrokerage](ctx, o.Docs, tenant, cMFBrok, month+"|")
	if err != nil {
		return v, err
	}
	lines := map[string]*MFTrailLine{}
	line := func(isin, scheme, amc string) *MFTrailLine {
		l := lines[isin]
		if l == nil {
			l = &MFTrailLine{ISIN: isin, Scheme: scheme, AMC: amc}
			lines[isin] = l
		}
		return l
	}
	for _, a := range acc {
		if a.Expected == 0 {
			continue
		}
		v.Accruals = append(v.Accruals, a)
		l := line(a.ISIN, a.Scheme, a.AMC)
		l.Expected += a.Expected
		l.Holdings++
		v.Expected += a.Expected
	}
	files := map[string]bool{}
	for _, b := range bs {
		files[b.Source] = true
		v.Received += b.Amount
		if b.ISIN == "" {
			v.Unmapped += b.Amount
			continue
		}
		l := line(b.ISIN, firstOf(b.Scheme, b.ISIN), b.AMC)
		if l.Scheme == b.ISIN && b.Scheme != "" {
			l.Scheme = b.Scheme
		}
		l.Received += b.Amount
	}
	v.Files = sortedKeys(files)
	for _, l := range lines {
		l.Diff = l.Received - l.Expected
		tol := money.Amount(100)
		if t := l.Expected / 50; t > tol {
			tol = t
		}
		switch {
		case l.Expected == 0:
			l.Status = "Not expected"
		case l.Received == 0:
			l.Status = "Not received"
		case l.Diff < -tol:
			l.Status = "Short"
		case l.Diff > tol:
			l.Status = "Excess"
		default:
			l.Status = "Matched"
		}
		v.Lines = append(v.Lines, *l)
	}
	rank := map[string]int{"Short": 0, "Not received": 1, "Excess": 2, "Not expected": 3, "Matched": 4}
	sort.SliceStable(v.Lines, func(i, j int) bool {
		if rank[v.Lines[i].Status] != rank[v.Lines[j].Status] {
			return rank[v.Lines[i].Status] < rank[v.Lines[j].Status]
		}
		return v.Lines[i].Scheme < v.Lines[j].Scheme
	})
	return v, nil
}
