package sharepro

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

// Documents: every attachment of the mails SharePro sent (contract notes,
// statements, bills) is archived. Verified against a real mail database:
// HTML documents usually have a digitally signed PDF copy (the attachment's
// Signed column, starting %PDF): that copy is stored instead; HTML without
// one is stored as html; PDF, xlsx and jpg as they are. Attachments are
// read one row at a time and the SQL returns only the copy that is kept.
// The archive copy XSSMAIL2 (same layout) is read too, without the
// attachments already read from SSMAIL2.

// MaxAttachment is the largest attachment archived; larger ones are
// reported instead.
var MaxAttachment int64 = 100 << 20

const (
	qMailBare = `/*sp:mail-bare*/ SELECT RTRIM(m.ReportCat) AS cat, COUNT(*) AS n FROM %[1]s m WHERE NOT EXISTS (SELECT 1 FROM %[2]s a WHERE a.slno = m.slno) AND m.[date] >= '%[3]s' AND m.[date] < '%[4]s' GROUP BY RTRIM(m.ReportCat)`
	qMailRows = `/*sp:mail-rows*/ SELECT COUNT(*) AS n FROM %s`
	// The kept copy: the signed PDF when there is one, else the file as sent.
	qAttachments = `/*sp:attachments*/ SELECT m.slno AS slno, a.attnumber AS attnumber, RTRIM(a.filename) AS filename, RTRIM(m.id) AS id, RTRIM(m.idtype) AS idtype, RTRIM(m.ReportCat) AS reportcat, m.[date] AS date, RTRIM(m.[to]) AS sentto, RTRIM(m.subject) AS subject, %[3]s AS mailsigned, %[10]s AS co,` +
		` CASE WHEN %[4]s THEN 1 ELSE 0 END AS use_signed,` +
		` CASE WHEN %[4]s THEN DATALENGTH(a.Signed) ELSE DATALENGTH(a.contents) END AS size,` +
		` CASE WHEN %[4]s THEN (CASE WHEN DATALENGTH(a.Signed) <= %[5]d THEN %[8]s END) ELSE (CASE WHEN DATALENGTH(a.contents) <= %[5]d THEN %[9]s END) END AS data` +
		` FROM %[1]s m JOIN %[2]s a ON a.slno = m.slno WHERE m.[date] >= '%[6]s' AND m.[date] < '%[7]s' ORDER BY m.slno, a.attnumber`
	qScans = `/*sp:scans*/ SELECT s.SrlNo AS srlno, RTRIM(s.Category) AS category, RTRIM(s.IdType) AS idtype, RTRIM(s.Id) AS id, s.[Date] AS date, RTRIM(s.FileType) AS filetype, %[1]s AS catname,` +
		` DATALENGTH(s.Img) AS size, CASE WHEN DATALENGTH(s.Img) <= %[2]d THEN %[6]s END AS data FROM SCANIMAGE s %[3]s WHERE s.[Date] >= '%[4]s' AND s.[Date] < '%[5]s' ORDER BY s.SrlNo`
)

// mailFamilies are the mail tables, each with its archive copy.
var mailFamilies = [][2]string{{"SSMAIL2", "XSSMAIL2"}, {"SSMAIL", "XSSMAIL"}}

func hasMail(s *schema, t string) bool {
	return s.has(t) && s.has(t+"_ATTACHMENT") && s.hasCol(t+"_ATTACHMENT", "CONTENTS") && s.hasCol(t, "SLNO")
}

// mailTables finds the mail and attachment tables of a mail database.
func mailTables(s *schema) (mail, att string) {
	for _, f := range mailFamilies {
		for _, t := range f {
			if hasMail(s, t) {
				return t, t + "_ATTACHMENT"
			}
		}
	}
	return "", ""
}

func (m *Mapper) Documents(ctx context.Context, q migrate.Querier, k migrate.Kind, plan migrate.Plan, w *pkg.Writer, p migrate.Progress) error {
	if p == nil {
		p = migrate.NopProgress{}
	}
	d := db{q}
	s, err := loadSchema(ctx, d)
	if err != nil {
		return err
	}
	if mt, _ := mailTables(s); mt == "" {
		return fmt.Errorf("sharepro: %s is not a mail database", q.Database())
	}
	m.mu.Lock()
	firms := append([]firmInfo(nil), m.firms...)
	dm := &docRun{d: d, s: s, k: k, w: w, p: p, bo: m.boUCC, tag: docTag(q.Database()),
		rt: &docRouter{fy: k.FY, clientFirm: m.clientFirm, boFirm: m.boFirm, coFirm: map[string]string{}, want: map[string]bool{}, reps: map[string]*report{}, n: map[string]int{}}}
	m.mu.Unlock()
	rt := dm.rt
	for _, f := range firms {
		for _, co := range f.COs {
			rt.coFirm[co] = f.Code
		}
	}
	for _, f := range plan.Firms {
		rt.want[f] = true
	}
	switch {
	case len(plan.Firms) > 0:
		rt.first = plan.Firms[0]
	case len(firms) > 0:
		rt.first = firms[0].Code
	default:
		rt.first = "SP"
	}
	rt.several = len(firms) > 1 && len(plan.Firms) != 1
	rep := rt.rep(rt.first)
	dm.rep = rep
	defer rt.flush(w, p)
	// the years to read: the database's own, or every year it holds
	// within the plan
	from, to := time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	if k.FY != "" {
		from, to = fyStart(k.FY), fyEnd(k.FY).AddDate(0, 0, 1)
	}
	newestFY := newest(firmYears(firms))
	if newestFY == "" {
		newestFY = fyOf(time.Now())
	}
	years := plan.Years
	if years <= 0 {
		years = 8
	}
	if lo := fyStart(newestFY).AddDate(-(years - 1), 0, 0); lo.After(from) {
		from = lo
	}
	if !from.Before(to) {
		rep.infof("Documents", k.FY, "out-of-range", "Mail archive %s is outside the years chosen; not read", k.FY)
		return nil
	}
	dm.from, dm.to = day(from), day(to)
	p.Step("Documents " + firstNonEmpty(k.FY, q.Database()))
	for fi, fam := range mailFamilies {
		var seen map[string]bool
		for i, t := range fam {
			if !hasMail(s, t) {
				continue
			}
			if i == 1 {
				// the archive copy: only when it has rows, skipping what the main table had
				r, err := d.one(ctx, fmt.Sprintf(qMailRows, t+"_ATTACHMENT"))
				if err != nil {
					return err
				}
				if i64(r, "n") == 0 {
					continue
				}
			} else if hasMail(s, fam[1]) {
				seen = map[string]bool{}
			}
			if err := dm.read(ctx, t, fmt.Sprintf("M%d", fi+1), seen, i == 1); err != nil {
				return err
			}
		}
	}
	if dm.dpNoUCC > 0 {
		rep.infof("Documents", "", "dp-no-ucc", "%d DP documents were sent to DP accounts that are not in the newest year's DP master (CLLIST); they are archived without a client code (the DP account is in the source reference)", dm.dpNoUCC)
	}
	if dm.internal > 0 {
		rep.infof("Documents", "", "internal", "%d documents were mailed to branches, groups or sub-brokers (payout reports covering several clients); they are archived as the firm's records, with no client code, so no client sees them", dm.internal)
	}
	if s.has("SCANIMAGE") && s.hasCol("SCANIMAGE", "Img") {
		if _, err := m.scans(ctx, d, s, k, w, rep, rt, dm.tag, dm.from, dm.to); err != nil {
			return err
		}
	}
	if rt.fallback > 0 {
		rep.infof("Documents", "", "first-firm", "%d documents could not be tied to a firm (client or DP account not in the newest company database); they are archived under firm %s", rt.fallback, rt.first)
	}
	if rt.outside > 0 {
		rep.infof("Documents", "", "other-firm", "%d documents belong to firms not chosen for this migration and are left out", rt.outside)
	}
	for _, f := range sortedKeys(rt.n) {
		rt.rep(f).check(pkg.CheckDocuments, fmt.Sprint(rt.n[f]), fmt.Sprintf("Documents archived from %s (%s to %s); %d too large", q.Database(), dm.from, dm.to, dm.big))
	}
	return nil
}

// docRun is one mail database being archived.
type docRun struct {
	d               db
	s               *schema
	k               migrate.Kind
	w               *pkg.Writer
	p               migrate.Progress
	rep             *report
	rt              *docRouter
	bo              map[string]string
	tag, from, to   string
	n, big, dpNoUCC int
	internal        int
}

// read archives the attachments of one mail table. seen holds the
// attachments already read (the main table), to skip them in the archive
// copy.
func (dm *docRun) read(ctx context.Context, mt, fam string, seen map[string]bool, archive bool) error {
	s, at := dm.s, mt+"_ATTACHMENT"
	if !archive {
		bare := 0
		if err := dm.d.query(ctx, fmt.Sprintf(qMailBare, mt, at, dm.from, dm.to), func(r migrate.Row) error {
			bare += int(i64(r, "n"))
			dm.rep.infof("Documents", str(r, "cat"), "no-attachment", "%d mails of category %q have no attachment (text only); they are not documents", i64(r, "n"), str(r, "cat"))
			return nil
		}); err != nil {
			return err
		}
	}
	signedCol := "0"
	if s.hasCol(mt, "signed") {
		signedCol = "CAST(m.signed AS int)"
	}
	useSigned, signedBytes := "1 = 0", "NULL"
	if s.hasCol(at, "Signed") {
		useSigned = "DATALENGTH(a.Signed) > 4 AND SUBSTRING(a.Signed, 1, 4) = '%PDF'"
		signedBytes = s.bytesOf(at, "a.Signed", "Signed")
	}
	coCol := "NULL"
	if s.hasCol(mt, "CO") {
		coCol = "RTRIM(m.CO)"
	}
	sq := fmt.Sprintf(qAttachments, mt, at, signedCol, useSigned, MaxAttachment, dm.from, dm.to, signedBytes, s.bytesOf(at, "a.contents", "contents"), coCol)
	return dm.d.query(ctx, sq, func(r migrate.Row) error {
		key := str(r, "slno") + "|" + str(r, "attnumber")
		if seen != nil {
			if archive {
				if seen[key] {
					return nil
				}
			} else {
				seen[key] = true
			}
		}
		cat := str(r, "reportcat")
		ref := fmt.Sprintf("%s:slno=%s,attnumber=%s;ReportCat=%s", at, str(r, "slno"), str(r, "attnumber"), cat)
		size := i64(r, "size")
		data := blob(r["data"])
		dt := tm(r, "date")
		if size > MaxAttachment || (data == nil && size > 0) {
			dm.big++
			dm.rep.warnf("Documents", str(r, "id"), "too-large", "Export it from SharePro by hand and keep it with the firm's records", "Attachment %s (%s, %d MB) is larger than %d MB and is not archived (%s)", str(r, "filename"), day(dt), size>>20, MaxAttachment>>20, ref)
			return nil
		}
		if len(data) == 0 {
			return nil
		}
		signed := flag(r, "use_signed")
		format := strings.TrimPrefix(strings.ToLower(path.Ext(str(r, "filename"))), ".")
		switch {
		case signed || bytes.HasPrefix(data, []byte("%PDF")):
			format = "pdf"
		case format == "htm":
			format = "html"
		case format == "":
			format = "bin"
		}
		idType := strings.ToUpper(str(r, "idtype"))
		ucc, dpID := "", ""
		switch idType {
		case "DP CLIENT", "DPCLIENT", "DP":
			// the account's trading client, or the DP-only client
			dpID = strings.ToUpper(str(r, "id"))
			ucc = dm.bo[dpID]
			if ucc == "" {
				dm.dpNoUCC++
			}
		case "CLIENT":
			ucc = strings.ToUpper(str(r, "id"))
		case "BRANCHID", "GROUP", "SUBBROKER", "SUB-BROKER", "BRANCH", "DEALER":
			dm.internal++
		}
		firm, ok := dm.rt.route(str(r, "co"), ucc, dpID)
		if !ok {
			return nil
		}
		typ := docType(cat, idType)
		meta := pkg.DocMeta{ID: fmt.Sprintf("%s-%s-%s-%s", dm.tag, fam, str(r, "slno"), str(r, "attnumber")), UCC: ucc, Type: typ,
			Date: dt, FY: firstNonEmpty(dm.k.FY, fyOf(dt), "unknown"), Subject: str(r, "subject"), To: str(r, "sentto"),
			Signed: signed || (format == "pdf" && flag(r, "mailsigned")), Format: format, SourceRef: ref}
		if !dt.IsZero() {
			meta.RetainUntil = dt.AddDate(8, 0, 0)
		}
		if err := dm.w.AddDocument(firm, meta, data); err != nil {
			return err
		}
		dm.n++
		dm.rt.n[firm]++
		if dm.n%200 == 0 {
			dm.p.Count(dm.n, 0)
		}
		return nil
	})
}

// docType: the mail's report category (verified against a real mail
// database), with DP mails of no category as DP statements.
func docType(cat, idType string) string {
	switch strings.ToUpper(strings.TrimSpace(idType)) {
	case "BRANCHID", "GROUP", "SUBBROKER", "SUB-BROKER", "BRANCH", "DEALER":
		return pkg.DocOther
	}
	switch strings.ToUpper(strings.Join(strings.Fields(cat), " ")) {
	case "CONTRACT":
		return pkg.DocContractNote
	case "MARGIN2CLIENTS", "OBLIGATION MRGN":
		return pkg.DocMargin
	case "STANDARDUPLOAD", "RUNACREP2CLIENT", "A/C LEDGER", "CLIENT P/L", "NET POSITION", "TRADE REGISTER":
		return pkg.DocStatement
	case "VALLAN BILLS":
		return pkg.DocBill
	case "DPBLPRN2":
		return pkg.DocDPBill
	case "REKYC":
		return pkg.DocKYC
	case "":
		if strings.Contains(strings.ToUpper(idType), "DP") {
			return pkg.DocDPStatement
		}
	}
	return pkg.DocOther
}

// docRouter decides each document's firm: the mail's company (CO) when
// the mail table has one, else the client's firm, else the DP account's,
// else the first firm.
type docRouter struct {
	fy                         string
	first                      string
	several                    bool
	coFirm, clientFirm, boFirm map[string]string
	want                       map[string]bool
	reps                       map[string]*report
	n                          map[string]int
	fallback, outside          int
}

func (rt *docRouter) route(co, ucc, dpID string) (string, bool) {
	f := ""
	switch {
	case co != "" && rt.coFirm[co] != "":
		f = rt.coFirm[co]
	case ucc != "" && rt.clientFirm[ucc] != "":
		f = rt.clientFirm[ucc]
	case dpID != "" && rt.boFirm[dpID] != "":
		f = rt.boFirm[dpID]
	default:
		f = rt.first
		if rt.several {
			rt.fallback++
		}
	}
	if len(rt.want) > 0 && !rt.want[f] {
		rt.outside++
		return "", false
	}
	return f, true
}

func (rt *docRouter) rep(firm string) *report {
	r := rt.reps[firm]
	if r == nil {
		r = newReport(firm, rt.fy)
		rt.reps[firm] = r
	}
	return r
}

func (rt *docRouter) flush(w *pkg.Writer, p migrate.Progress) {
	for _, f := range sortedKeys(rt.reps) {
		rt.reps[f].flush(w, p)
	}
}

func firmYears(fs []firmInfo) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Years...)
	}
	return out
}

// scans archives SCANIMAGE (scanned KYC forms and other images) where the
// mail database has it.
func (m *Mapper) scans(ctx context.Context, d db, s *schema, k migrate.Kind, w *pkg.Writer, rep *report, rt *docRouter, tag, from, to string) (int, error) {
	catCol, join := "NULL", ""
	if s.has("IMAGECAT") {
		catCol, join = "RTRIM(c.Descr)", "LEFT JOIN IMAGECAT c ON RTRIM(c.CatCode) = RTRIM(s.Category)"
	}
	n := 0
	err := d.query(ctx, fmt.Sprintf(qScans, catCol, MaxAttachment, join, from, to, s.bytesOf("SCANIMAGE", "s.Img", "Img")), func(r migrate.Row) error {
		data := blob(r["data"])
		dt := tm(r, "date")
		ref := "SCANIMAGE:SrlNo=" + str(r, "srlno")
		if data == nil {
			if i64(r, "size") > 0 {
				rep.warnf("Documents", str(r, "id"), "too-large", "Export it from SharePro by hand", "Scanned image %s is larger than %d MB and is not archived", ref, MaxAttachment>>20)
			}
			return nil
		}
		format := strings.ToLower(strings.TrimPrefix(str(r, "filetype"), "."))
		if format == "" {
			format = "jpg"
		}
		typ := pkg.DocOther
		if strings.Contains(strings.ToUpper(str(r, "catname")+" "+str(r, "category")), "KYC") {
			typ = pkg.DocKYC
		}
		ucc := ""
		if strings.EqualFold(str(r, "idtype"), "CLIENT") {
			ucc = upper(r, "id")
		}
		firm, ok := rt.route("", ucc, "")
		if !ok {
			return nil
		}
		meta := pkg.DocMeta{ID: tag + "-S" + str(r, "srlno"), UCC: ucc, Type: typ, Date: dt, FY: firstNonEmpty(k.FY, fyOf(dt), "unknown"), Subject: firstNonEmpty(str(r, "catname"), str(r, "category")),
			Format: format, SourceRef: ref}
		if !dt.IsZero() {
			meta.RetainUntil = dt.AddDate(8, 0, 0)
		}
		if err := w.AddDocument(firm, meta, data); err != nil {
			return err
		}
		n++
		rt.n[firm]++
		return nil
	})
	return n, err
}

// docTag makes document IDs unique across mail databases.
func docTag(name string) string {
	t := codePart(name)
	if len(t) > 40 {
		t = t[:40]
	}
	return t
}

func blob(v any) []byte {
	switch x := v.(type) {
	case []byte:
		return x
	case string:
		return []byte(x)
	}
	return nil
}
