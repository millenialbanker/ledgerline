package sharepro

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

// Mapper reads SharePro databases.
//
// SharePro keeps one company database per financial year (<prefix><yy><yy>,
// e.g. XX2627 for 2026-27). Inside it, CO is SharePro's "company": one
// exchange segment (or the DP, PMS, NBFC) of a legal entity, e.g. 02 for
// NSE cash and 03 for NSE F&O of the same firm. The admin database (COLIST,
// ACC_YEAR) lists the COs. A Ledgerline firm is a legal entity, so the COs
// of one entity are grouped into one firm (see groupCOs).
type Mapper struct {
	mu    sync.Mutex
	firms []firmInfo // from Firms, used by the later steps
	// boUCC maps a DP account (BENACNUM and FBENACNUM) to the trading
	// code, from the newest company database (for DP mails).
	boUCC map[string]string
}

// New returns the SharePro mapper.
func New() *Mapper { return &Mapper{} }

func (*Mapper) Name() string { return "SharePro" }

// firmInfo is one Ledgerline firm: a group of SharePro COs.
type firmInfo struct {
	Code   string
	Name   string
	COs    []string
	Seg    map[string]coSeg // CO -> exchange segment
	Years  []string
	Notes  []string
	Active map[string]bool
}

// coSeg is what a CO is: an exchange segment, the DP, or something else.
type coSeg struct {
	Exchange string // NSE, BSE, MCX, NCDEX, CDSL, NSDL
	Segment  string // CM, FO, CD, COM; DP for a depository; "" other
}

func (s coSeg) label() string {
	if s.Exchange == "" {
		return ""
	}
	if s.Segment == "" {
		return s.Exchange
	}
	return s.Exchange + "-" + s.Segment
}

// ---- Classify ----

const (
	qMailDates  = `/*sp:mail-dates*/ SELECT MIN([date]) AS first, MAX([date]) AS last, COUNT(*) AS n FROM %s`
	qJVYears    = `/*sp:jv-years*/ SELECT TOP 1 CASE WHEN MONTH(VRDT) >= 4 THEN YEAR(VRDT) ELSE YEAR(VRDT) - 1 END AS y, COUNT(*) AS n FROM JVFILE GROUP BY CASE WHEN MONTH(VRDT) >= 4 THEN YEAR(VRDT) ELSE YEAR(VRDT) - 1 END ORDER BY COUNT(*) DESC`
	qAdminYears = `/*sp:admin-years*/ SELECT MAX(ACC_YEAR) AS last, MIN(ACC_YEAR) AS first FROM ACC_YEAR`
)

// mailTables finds the mail and attachment tables of a mail database.
func mailTables(s *schema) (mail, att string) {
	for _, t := range []string{"SSMAIL2", "SSMAIL", "XSSMAIL2", "XSSMAIL"} {
		if s.has(t) && s.has(t+"_ATTACHMENT") && s.hasCol(t+"_ATTACHMENT", "CONTENTS") && s.hasCol(t, "SLNO") {
			return t, t + "_ATTACHMENT"
		}
	}
	return "", ""
}

func isAdmin(s *schema) bool { return s.has("COLIST") && s.has("ACC_YEAR") }
func isCompany(s *schema) bool {
	return s.has("DLMAST") && s.has("JVFILE") && (s.has("ACCTOT") || s.has("MLFILE"))
}

func (m *Mapper) Classify(ctx context.Context, q migrate.Querier) (migrate.Kind, error) {
	d := db{q}
	s, err := loadSchema(ctx, d)
	if err != nil {
		return migrate.Kind{}, err
	}
	name := q.Database()
	switch {
	case isAdmin(s):
		note := "SharePro admin database (companies and years)"
		if r, err := d.one(ctx, qAdminYears); err == nil && r != nil {
			if a, b := fyFromCode(str(r, "first")), fyFromCode(str(r, "last")); a != "" && b != "" {
				note += fmt.Sprintf("; years %s to %s", a, b)
			}
		}
		return migrate.Kind{Role: "admin", Note: note, Order: 0}, nil
	case isCompany(s):
		fy := fyFromName(name)
		if fy == "" {
			if r, err := d.one(ctx, qJVYears); err == nil && r != nil && i64(r, "y") > 1990 {
				y := int(i64(r, "y"))
				fy = fmt.Sprintf("%d-%02d", y, (y+1)%100)
			}
		}
		if fy == "" {
			return migrate.Kind{Role: "company", Skip: true, Note: "SharePro company database, but its financial year cannot be told from its name or vouchers", Order: 49}, nil
		}
		return migrate.Kind{Role: "company", FY: fy, Note: "SharePro company database " + fy, Order: companyOrder(fy)}, nil
	}
	if mt, _ := mailTables(s); mt != "" {
		fy := fyFromName(name)
		note := "SharePro mail archive"
		var first, last time.Time
		if r, err := d.one(ctx, fmt.Sprintf(qMailDates, mt)); err == nil && r != nil {
			first, last = tm(r, "first"), tm(r, "last")
			if i64(r, "n") == 0 {
				return migrate.Kind{Role: "mail", FY: fy, Skip: true, Note: note + ": no mails", Order: 99}, nil
			}
		}
		if fy == "" && !first.IsZero() && fyOf(first) == fyOf(last) {
			fy = fyOf(first)
		}
		if fy != "" {
			note += " " + fy
		} else if !first.IsZero() {
			note += fmt.Sprintf(" %s to %s", fyOf(first), fyOf(last))
		}
		o := fy
		if o == "" {
			o = fyOf(last)
		}
		return migrate.Kind{Role: "mail", FY: fy, Note: note, Order: mailOrder(o)}, nil
	}
	return migrate.Kind{Role: "other", Skip: true, Note: "Not a SharePro company, admin or mail database", Order: 100}, nil
}

func clamp(v, lo, hi int) int { return max(lo, min(hi, v)) }

// companyOrder: newest first, 10 to 49.
func companyOrder(fy string) int {
	y := fyStart(fy).Year()
	return 10 + clamp(2059-y, 0, 39)
}

// mailOrder: newest first, 50 to 99.
func mailOrder(fy string) int {
	y := fyStart(fy).Year()
	if y == 0 {
		return 99
	}
	return 50 + clamp(2059-y, 0, 49)
}

// ---- Firms ----

const (
	qAdminCOs   = `/*sp:admin-colist*/ SELECT %s FROM COLIST ORDER BY CO`
	qAdminYearC = `/*sp:admin-acc-year*/ SELECT RTRIM(CO) AS co, RTRIM(ACC_YEAR) AS acc_year FROM ACC_YEAR`
	qCompanyCOs = `/*sp:company-cos*/ SELECT RTRIM(CO) AS co, RTRIM(Segment) AS segment, COUNT(*) AS n FROM TRFILE GROUP BY CO, Segment`
	qJVCOs      = `/*sp:jv-cos*/ SELECT DISTINCT RTRIM(CO) AS co FROM JVFILE`
	qDPCOs      = `/*sp:dp-cos*/ SELECT DISTINCT RTRIM(CO) AS co FROM CLLIST`
)

type coRow struct {
	co, descr, sebi string
	inactive        bool
	dp              bool
}

func (m *Mapper) Firms(ctx context.Context, admin, company migrate.Querier) ([]pkg.Firm, error) {
	var fs []firmInfo
	var err error
	if admin != nil {
		fs, err = firmsFromAdmin(ctx, db{admin})
		if err != nil {
			return nil, err
		}
	}
	if company != nil {
		cs, err := loadSchema(ctx, db{company})
		if err != nil {
			return nil, err
		}
		trade := companySegs(ctx, db{company}, cs)
		if len(fs) == 0 {
			fs = fallbackFirms(ctx, db{company}, cs, trade)
		} else {
			// what the trades say a CO is wins over its name
			for i := range fs {
				for co, s := range trade {
					if _, ok := fs[i].Seg[co]; ok && s.Exchange != "" {
						fs[i].Seg[co] = s
					}
				}
			}
		}
	}
	m.mu.Lock()
	m.firms = fs
	m.mu.Unlock()
	return toPkgFirms(fs), nil
}

func toPkgFirms(fs []firmInfo) []pkg.Firm {
	var out []pkg.Firm
	for _, f := range fs {
		pf := pkg.Firm{Code: f.Code, Name: f.Name, Years: f.Years}
		seen := map[string]bool{}
		for _, co := range f.COs {
			if l := f.Seg[co].label(); l != "" && !seen[l] && (f.Active == nil || f.Active[co]) {
				seen[l] = true
				pf.Segments = append(pf.Segments, l)
			}
		}
		sort.Strings(pf.Segments)
		out = append(out, pf)
	}
	return out
}

func firmsFromAdmin(ctx context.Context, d db) ([]firmInfo, error) {
	s, err := loadSchema(ctx, d)
	if err != nil {
		return nil, err
	}
	list, _ := s.sel("COLIST", "", "CO", "DESCR", "SEBI_REG", "Inactive", "CDSL_BEN", "NSDL_BEN", "DPID")
	var rows []coRow
	err = d.query(ctx, fmt.Sprintf(qAdminCOs, list), func(r migrate.Row) error {
		rows = append(rows, coRow{co: str(r, "co"), descr: str(r, "descr"), sebi: upper(r, "sebi_reg"), inactive: flag(r, "inactive"),
			dp: flag(r, "cdsl_ben") || flag(r, "nsdl_ben")})
		return nil
	})
	if err != nil {
		return nil, err
	}
	years := map[string][]string{}
	if s.has("ACC_YEAR") {
		err = d.query(ctx, qAdminYearC, func(r migrate.Row) error {
			if fy := fyFromCode(str(r, "acc_year")); fy != "" {
				years[str(r, "co")] = append(years[str(r, "co")], fy)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return groupCOs(rows, years), nil
}

var (
	reParen   = regexp.MustCompile(`\(([^)]*)\)`)
	reNamePun = regexp.MustCompile(`[^A-Z0-9 ]+`)
	reSpaces  = regexp.MustCompile(`\s+`)
)

// entityName is a CO's name without its segment: "ABC SECURITIES PVT.
// LTD. (NSE)" and "ABC SECURITIES PVT LTD(C - MCX)" are both "ABC
// SECURITIES PVT LTD".
func entityName(descr string) string {
	n := strings.ToUpper(reParen.ReplaceAllString(descr, " "))
	n = reNamePun.ReplaceAllString(n, " ")
	n = strings.ReplaceAll(" "+n+" ", " PRIVATE ", " PVT ")
	n = strings.ReplaceAll(n, " LIMITED ", " LTD ")
	return strings.TrimSpace(reSpaces.ReplaceAllString(n, " "))
}

// segFromName reads the segment from a CO's name, e.g. "(NSE)", "(NFO)",
// "(C - NSE)", "(CDSL)".
func segFromName(descr, sebi string, dp bool) coSeg {
	tag := ""
	if m := reParen.FindStringSubmatch(strings.ToUpper(descr)); m != nil {
		tag = strings.ReplaceAll(strings.ReplaceAll(m[1], " ", ""), "-", "")
	}
	if s, ok := segToken(tag); ok {
		return s
	}
	switch {
	case dp || strings.HasPrefix(sebi, "IN-DP") || strings.HasPrefix(sebi, "INDP"):
		if strings.Contains(strings.ToUpper(descr), "NSDL") {
			return coSeg{"NSDL", "DP"}
		}
		return coSeg{"CDSL", "DP"}
	}
	return coSeg{}
}

// segToken reads SharePro's segment names (TRFILE.Segment, CO name tags).
func segToken(t string) (coSeg, bool) {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	case "NSE", "NSECM", "CM":
		return coSeg{"NSE", "CM"}, true
	case "BSE", "BSECM":
		return coSeg{"BSE", "CM"}, true
	case "NFO", "NSEFO", "FO":
		return coSeg{"NSE", "FO"}, true
	case "BFO", "BSEFO":
		return coSeg{"BSE", "FO"}, true
	case "CNSE", "CDS", "NCDS", "CNFO", "NSECD", "CD":
		return coSeg{"NSE", "CD"}, true
	case "CBSE", "BCD", "CBFO", "BSECD":
		return coSeg{"BSE", "CD"}, true
	case "CMCX", "MCXCD", "CMFO", "MCXSX":
		return coSeg{"MCX", "CD"}, true
	case "MCX", "MCFO", "MCXFO", "MCXCOM":
		return coSeg{"MCX", "COM"}, true
	case "NCDEX", "NCFO", "NCDX":
		return coSeg{"NCDEX", "COM"}, true
	case "CDSL":
		return coSeg{"CDSL", "DP"}, true
	case "NSDL":
		return coSeg{"NSDL", "DP"}, true
	}
	return coSeg{}, false
}

// groupCOs makes one firm per legal entity. The firm's code is the
// lowest CO of the group.
func groupCOs(rows []coRow, years map[string][]string) []firmInfo {
	by := map[string]*firmInfo{}
	var order []string
	for _, r := range rows {
		key := entityName(r.descr)
		if key == "" {
			key = "CO " + r.co
		}
		f := by[key]
		if f == nil {
			f = &firmInfo{Code: r.co, Name: key, Seg: map[string]coSeg{}, Active: map[string]bool{}}
			by[key] = f
			order = append(order, key)
		}
		f.COs = append(f.COs, r.co)
		if r.co < f.Code {
			f.Code = r.co
		}
		f.Seg[r.co] = segFromName(r.descr, r.sebi, r.dp)
		f.Active[r.co] = !r.inactive
		// the name as the old system writes it, from an active CO
		if !r.inactive && strings.EqualFold(entityName(r.descr), key) {
			f.Name = strings.TrimSpace(reParen.ReplaceAllString(r.descr, ""))
		}
	}
	var out []firmInfo
	for _, k := range order {
		f := by[k]
		ys := map[string]bool{}
		for _, co := range f.COs {
			for _, y := range years[co] {
				ys[y] = true
			}
		}
		f.Years = sortedKeys(ys)
		sort.Strings(f.COs)
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// companySegs reads which segment each CO trades in, from TRFILE.
func companySegs(ctx context.Context, d db, s *schema) map[string]coSeg {
	out := map[string]coSeg{}
	if !s.hasCol("TRFILE", "SEGMENT") {
		return out
	}
	best := map[string]int64{}
	_ = d.query(ctx, qCompanyCOs, func(r migrate.Row) error {
		if sg, ok := segToken(str(r, "segment")); ok && i64(r, "n") > best[str(r, "co")] {
			best[str(r, "co")] = i64(r, "n")
			out[str(r, "co")] = sg
		}
		return nil
	})
	return out
}

// fallbackFirms: without the admin database every CO in the company
// database is one firm.
func fallbackFirms(ctx context.Context, d db, s *schema, trade map[string]coSeg) []firmInfo {
	cos := map[string]bool{}
	for co := range trade {
		cos[co] = true
	}
	_ = d.query(ctx, qJVCOs, func(r migrate.Row) error {
		if c := str(r, "co"); c != "" {
			cos[c] = true
		}
		return nil
	})
	dp := map[string]bool{}
	if s.has("CLLIST") {
		_ = d.query(ctx, qDPCOs, func(r migrate.Row) error {
			if c := str(r, "co"); c != "" {
				cos[c] = true
				dp[c] = true
			}
			return nil
		})
	}
	if len(cos) == 0 {
		return nil
	}
	f := firmInfo{Seg: map[string]coSeg{}, Active: map[string]bool{}, Notes: []string{"The admin database was not given: every SharePro company (CO) in the company database is treated as one firm"}}
	for _, co := range sortedKeys(cos) {
		f.COs = append(f.COs, co)
		f.Seg[co] = trade[co]
		if dp[co] && f.Seg[co].Exchange == "" {
			f.Seg[co] = coSeg{"CDSL", "DP"}
		}
		f.Active[co] = true
	}
	f.Code = f.COs[0]
	f.Name = "SharePro companies " + strings.Join(f.COs, ", ")
	return []firmInfo{f}
}

// firmsFor returns the firms to migrate: those from Firms (or, if Firms
// was not called, worked out from the company database), narrowed to the
// plan's choice.
func (m *Mapper) firmsFor(ctx context.Context, d db, s *schema, plan migrate.Plan) []firmInfo {
	m.mu.Lock()
	fs := append([]firmInfo(nil), m.firms...)
	m.mu.Unlock()
	if len(fs) == 0 {
		fs = fallbackFirms(ctx, d, s, companySegs(ctx, d, s))
	}
	if len(plan.Firms) == 0 {
		return fs
	}
	want := map[string]bool{}
	for _, f := range plan.Firms {
		want[f] = true
	}
	var out []firmInfo
	for _, f := range fs {
		if want[f.Code] {
			out = append(out, f)
		}
	}
	return out
}

// segOf is the Ledgerline segment (CM, FO, CD, COM) of a CO, or "".
func (f firmInfo) segOf(co string) string {
	s := f.Seg[co].Segment
	if s == "DP" {
		return ""
	}
	return s
}

func (f firmInfo) exchOf(co string) string { return f.Seg[co].Exchange }

func (f firmInfo) dpCOs() []string {
	var out []string
	for _, co := range f.COs {
		if f.Seg[co].Segment == "DP" {
			out = append(out, co)
		}
	}
	return out
}
