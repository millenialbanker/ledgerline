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
// statements, bills) is archived. HTML documents usually have a digitally
// signed PDF copy (the attachment's Signed column): that copy is stored
// instead. Attachments are read one row at a time, and the SQL returns only
// the one copy that is kept, so a large mail database is never held in
// memory.

// MaxAttachment is the largest attachment archived; larger ones are
// reported instead.
var MaxAttachment int64 = 100 << 20

const (
	qMailCount = `/*sp:mail-count*/ SELECT COUNT(*) AS mails, SUM(CASE WHEN NOT EXISTS (SELECT 1 FROM %[2]s a WHERE a.slno = m.slno) THEN 1 ELSE 0 END) AS bare FROM %[1]s m WHERE m.[date] >= '%[3]s' AND m.[date] < '%[4]s'`
	// The kept copy: the signed PDF when there is one, else the file as sent.
	qAttachments = `/*sp:attachments*/ SELECT m.slno AS slno, a.attnumber AS attnumber, RTRIM(a.filename) AS filename, RTRIM(m.id) AS id, RTRIM(m.idtype) AS idtype, RTRIM(m.ReportCat) AS reportcat, m.[date] AS date, RTRIM(m.[to]) AS sentto, RTRIM(m.subject) AS subject, %[3]s AS mailsigned,` +
		` CASE WHEN %[4]s THEN 1 ELSE 0 END AS use_signed,` +
		` CASE WHEN %[4]s THEN DATALENGTH(a.Signed) ELSE DATALENGTH(a.contents) END AS size,` +
		` CASE WHEN %[4]s THEN (CASE WHEN DATALENGTH(a.Signed) <= %[5]d THEN %[8]s END) ELSE (CASE WHEN DATALENGTH(a.contents) <= %[5]d THEN %[9]s END) END AS data` +
		` FROM %[1]s m JOIN %[2]s a ON a.slno = m.slno WHERE m.[date] >= '%[6]s' AND m.[date] < '%[7]s' ORDER BY m.slno, a.attnumber`
	qScans = `/*sp:scans*/ SELECT s.SrlNo AS srlno, RTRIM(s.Category) AS category, RTRIM(s.IdType) AS idtype, RTRIM(s.Id) AS id, s.[Date] AS date, RTRIM(s.FileType) AS filetype, %[1]s AS catname,` +
		` DATALENGTH(s.Img) AS size, CASE WHEN DATALENGTH(s.Img) <= %[2]d THEN %[6]s END AS data FROM SCANIMAGE s %[3]s WHERE s.[Date] >= '%[4]s' AND s.[Date] < '%[5]s' ORDER BY s.SrlNo`
)

func (m *Mapper) Documents(ctx context.Context, q migrate.Querier, k migrate.Kind, plan migrate.Plan, w *pkg.Writer, p migrate.Progress) error {
	if p == nil {
		p = migrate.NopProgress{}
	}
	d := db{q}
	s, err := loadSchema(ctx, d)
	if err != nil {
		return err
	}
	mt, at := mailTables(s)
	if mt == "" {
		return fmt.Errorf("sharepro: %s is not a mail database", q.Database())
	}
	m.mu.Lock()
	firms := append([]firmInfo(nil), m.firms...)
	bo := m.boUCC
	m.mu.Unlock()
	firm := ""
	if len(plan.Firms) > 0 {
		firm = plan.Firms[0]
	} else if len(firms) > 0 {
		firm = firms[0].Code
	}
	if firm == "" {
		firm = "SP"
	}
	rep := newReport(firm, k.FY)
	defer rep.flush(w, p)
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
	fromS, toS := day(from), day(to)
	if r, err := d.one(ctx, fmt.Sprintf(qMailCount, mt, at, fromS, toS)); err == nil && r != nil {
		if n := i64(r, "bare"); n > 0 {
			rep.infof("Documents", "", "no-attachment", "%d of %d mails have no attachment (text only); they are not documents", n, i64(r, "mails"))
		}
	} else if err != nil {
		return err
	}
	p.Step("Documents " + firstNonEmpty(k.FY, q.Database()))
	signedCol := "0"
	if s.hasCol(mt, "signed") {
		signedCol = "CAST(m.signed AS int)"
	}
	useSigned := "1 = 0"
	if s.hasCol(at, "Signed") {
		useSigned = "DATALENGTH(a.Signed) > 4 AND SUBSTRING(a.Signed, 1, 4) = '%PDF'"
	}
	n, bigN := 0, 0
	dpNoUCC := 0
	dbTag := docTag(q.Database())
	signedBytes := s.bytesOf(at, "a.Signed", "Signed")
	if !s.hasCol(at, "Signed") {
		signedBytes = "NULL"
	}
	sq := fmt.Sprintf(qAttachments, mt, at, signedCol, useSigned, MaxAttachment, fromS, toS, signedBytes, s.bytesOf(at, "a.contents", "contents"))
	err = d.query(ctx, sq, func(r migrate.Row) error {
		ref := fmt.Sprintf("%s:slno=%s,attnumber=%s", at, str(r, "slno"), str(r, "attnumber"))
		size := i64(r, "size")
		data := blob(r["data"])
		dt := tm(r, "date")
		if size > MaxAttachment || (data == nil && size > 0) {
			bigN++
			rep.warnf("Documents", str(r, "id"), "too-large", "Export it from SharePro by hand and keep it with the firm's records", "Attachment %s (%s, %d MB) is larger than %d MB and is not archived (%s)", str(r, "filename"), day(dt), size>>20, MaxAttachment>>20, ref)
			return nil
		}
		if len(data) == 0 {
			return nil
		}
		signed := flag(r, "use_signed")
		format := strings.TrimPrefix(strings.ToLower(path.Ext(str(r, "filename"))), ".")
		if signed || bytes.HasPrefix(data, []byte("%PDF")) {
			format = "pdf"
		}
		if format == "" {
			format = "bin"
		}
		idType := strings.ToUpper(str(r, "idtype"))
		ucc := ""
		switch {
		case strings.Contains(idType, "DP"):
			ucc = bo[strings.ToUpper(str(r, "id"))]
			if ucc == "" {
				dpNoUCC++
			}
		case idType == "CLIENT" || idType == "":
			ucc = strings.ToUpper(str(r, "id"))
		}
		meta := pkg.DocMeta{ID: fmt.Sprintf("%s-%s-%s", dbTag, str(r, "slno"), str(r, "attnumber")), UCC: ucc, Type: docType(str(r, "reportcat"), idType, str(r, "subject"), str(r, "filename")),
			Date: dt, FY: firstNonEmpty(k.FY, fyOf(dt)), Subject: str(r, "subject"), To: str(r, "sentto"), Signed: signed || (format == "pdf" && flag(r, "mailsigned")),
			Format: format, SourceRef: ref}
		if !dt.IsZero() {
			meta.RetainUntil = dt.AddDate(8, 0, 0)
		}
		if meta.FY == "" {
			meta.FY = "unknown"
		}
		if err := w.AddDocument(firm, meta, data); err != nil {
			return err
		}
		n++
		if n%200 == 0 {
			p.Count(n, 0)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if dpNoUCC > 0 {
		rep.infof("Documents", "", "dp-no-ucc", "%d DP documents were sent to DP accounts with no trading code; they are archived without a client code (the DP account is in the subject and the source reference)", dpNoUCC)
	}
	if s.has("SCANIMAGE") && s.hasCol("SCANIMAGE", "Img") {
		sn, err := m.scans(ctx, d, s, k, w, rep, firm, docTag(q.Database()), fromS, toS)
		if err != nil {
			return err
		}
		n += sn
	}
	rep.check(pkg.CheckDocuments, fmt.Sprint(n), fmt.Sprintf("Documents archived from %s (%s to %s); %d too large", q.Database(), fromS, toS, bigN))
	return nil
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
func (m *Mapper) scans(ctx context.Context, d db, s *schema, k migrate.Kind, w *pkg.Writer, rep *report, firm, tag, from, to string) (int, error) {
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
		meta := pkg.DocMeta{ID: tag + "-S" + str(r, "srlno"), UCC: ucc, Type: typ, Date: dt, FY: firstNonEmpty(k.FY, fyOf(dt), "unknown"), Subject: firstNonEmpty(str(r, "catname"), str(r, "category")),
			Format: format, SourceRef: ref}
		if !dt.IsZero() {
			meta.RetainUntil = dt.AddDate(8, 0, 0)
		}
		if err := w.AddDocument(firm, meta, data); err != nil {
			return err
		}
		n++
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

// docType reads the document type from the mail's report category,
// subject and file name.
func docType(cat, idType, subject, file string) string {
	t := strings.ToUpper(cat + " " + subject + " " + file)
	dp := strings.Contains(idType, "DP") || strings.Contains(t, "DPBL") || strings.Contains(t, "DP BILL") || strings.Contains(t, "DP STATEMENT") || strings.Contains(t, "TRANSACTION STATEMENT") || strings.Contains(t, "HOLDING STATEMENT")
	switch {
	case dp && (strings.Contains(t, "BILL") || strings.Contains(t, "DPBL")):
		return pkg.DocDPBill
	case dp:
		return pkg.DocDPStatement
	case strings.Contains(t, "CONTRACT"):
		return pkg.DocContractNote
	case strings.Contains(t, "MARGIN") || strings.Contains(t, "MRGN"):
		return pkg.DocMargin
	case strings.Contains(t, "VALLAN") || strings.Contains(t, "BILL"):
		return pkg.DocBill
	case strings.Contains(t, "KYC"):
		return pkg.DocKYC
	case strings.Contains(t, "LEDGER") || strings.Contains(t, "RUNAC") || strings.Contains(t, "RUNNING") || strings.Contains(t, "STATEMENT") ||
		strings.Contains(t, "P/L") || strings.Contains(t, "PROFIT") || strings.Contains(t, "NET POSITION") || strings.Contains(t, "STANDARDUPLOAD") || strings.Contains(t, "HOLDING"):
		return pkg.DocStatement
	}
	return pkg.DocOther
}
