// Package sheet reads and writes spreadsheets without outside libraries:
// Excel workbooks (.xlsx, Office Open XML) and the older binary .xls
// (BIFF8 inside an OLE2 compound file), which clearing corporations, CCIL
// and banks still publish. Cells come back as text: numbers in their
// shortest decimal form and dates as Excel serial numbers (see Date).
package sheet

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"
)

// Sheet is one worksheet: Rows[r][c], ragged, "" for empty cells.
type Sheet struct {
	Name string
	Rows [][]string
}

// Cell returns the text at row r, column c ("" outside the sheet).
func (s Sheet) Cell(r, c int) string {
	if r < 0 || r >= len(s.Rows) || c < 0 || c >= len(s.Rows[r]) {
		return ""
	}
	return s.Rows[r][c]
}

// Read reads a workbook, choosing the format from its content.
func Read(data []byte) ([]Sheet, error) {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return ReadXLSX(data)
	case bytes.HasPrefix(data, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}):
		return ReadXLS(data)
	}
	return nil, errors.New("sheet: not an Excel workbook (.xlsx or .xls)")
}

// IsWorkbook reports whether data looks like an Excel workbook.
func IsWorkbook(data []byte) bool {
	return bytes.HasPrefix(data, []byte("PK\x03\x04")) || bytes.HasPrefix(data, []byte{0xD0, 0xCF, 0x11, 0xE0})
}

// Date converts an Excel serial date (1900 system) to YYYY-MM-DD.
func Date(serial string) (string, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(serial), 64)
	if err != nil || f < 1 || f > 2958465 {
		return "", false
	}
	d := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(math.Floor(f)))
	return d.Format("2006-01-02"), true
}

func num(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func put(rows *[][]string, r, c int, v string) {
	if r < 0 || c < 0 || r > 1<<20 || c > 1<<14 {
		return
	}
	for len(*rows) <= r {
		*rows = append(*rows, nil)
	}
	row := (*rows)[r]
	for len(row) <= c {
		row = append(row, "")
	}
	row[c] = v
	(*rows)[r] = row
}

// ---- .xlsx ----

const maxPart = 256 << 20

func zipRead(z *zip.Reader, name string) ([]byte, error) {
	for _, f := range z.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxPart))
		}
	}
	return nil, fmt.Errorf("sheet: %s missing", name)
}

// ReadXLSX reads an Office Open XML workbook.
func ReadXLSX(data []byte) ([]Sheet, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("sheet: %w", err)
	}
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	b, err := zipRead(z, "xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	if err := xml.Unmarshal(b, &wb); err != nil {
		return nil, fmt.Errorf("sheet: workbook: %w", err)
	}
	var rels struct {
		Rel []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if b, err := zipRead(z, "xl/_rels/workbook.xml.rels"); err == nil {
		_ = xml.Unmarshal(b, &rels)
	}
	target := map[string]string{}
	for _, r := range rels.Rel {
		t := r.Target
		if strings.HasPrefix(t, "/") {
			t = strings.TrimPrefix(t, "/")
		} else {
			t = path.Join("xl", t)
		}
		target[r.ID] = t
	}
	var sst []string
	if b, err := zipRead(z, "xl/sharedStrings.xml"); err == nil {
		var s struct {
			SI []struct {
				T string `xml:"t"`
				R []struct {
					T string `xml:"t"`
				} `xml:"r"`
			} `xml:"si"`
		}
		if err := xml.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("sheet: shared strings: %w", err)
		}
		for _, si := range s.SI {
			t := si.T
			for _, r := range si.R {
				t += r.T
			}
			sst = append(sst, t)
		}
	}
	var out []Sheet
	for i, s := range wb.Sheets {
		name := target[s.RID]
		if name == "" {
			name = fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)
		}
		b, err := zipRead(z, name)
		if err != nil {
			return nil, err
		}
		var ws struct {
			Rows []struct {
				R int `xml:"r,attr"`
				C []struct {
					Ref string `xml:"r,attr"`
					T   string `xml:"t,attr"`
					V   string `xml:"v"`
					IS  struct {
						T string `xml:"t"`
						R []struct {
							T string `xml:"t"`
						} `xml:"r"`
					} `xml:"is"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		if err := xml.Unmarshal(b, &ws); err != nil {
			return nil, fmt.Errorf("sheet: %s: %w", s.Name, err)
		}
		sh := Sheet{Name: s.Name}
		for ri, row := range ws.Rows {
			r := row.R - 1
			if row.R == 0 {
				r = ri
			}
			for ci, c := range row.C {
				col := ci
				if c.Ref != "" {
					col = colIndex(c.Ref)
				}
				v := c.V
				switch c.T {
				case "s":
					if k, err := strconv.Atoi(v); err == nil && k >= 0 && k < len(sst) {
						v = sst[k]
					}
				case "inlineStr":
					v = c.IS.T
					for _, r := range c.IS.R {
						v += r.T
					}
				case "b":
					v = map[string]string{"1": "TRUE", "0": "FALSE"}[v]
				case "", "n":
					if f, err := strconv.ParseFloat(v, 64); err == nil {
						v = num(f)
					}
				}
				put(&sh.Rows, r, col, v)
			}
		}
		out = append(out, sh)
	}
	return out, nil
}

func colIndex(ref string) int {
	n := 0
	for _, ch := range ref {
		if ch < 'A' || ch > 'Z' {
			break
		}
		n = n*26 + int(ch-'A'+1)
	}
	return n - 1
}

func colName(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

// WriteXLSX writes a plain workbook: text cells as text, numbers as
// numbers, the first row of each sheet bold.
func WriteXLSX(sheets []Sheet) ([]byte, error) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	add := func(name, body string) error {
		w, err := z.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, body)
		return err
	}
	esc := func(s string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	ct := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`
	wb := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`
	rels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`
	for i, s := range sheets {
		n := i + 1
		name := s.Name
		if name == "" {
			name = fmt.Sprintf("Sheet%d", n)
		}
		if len(name) > 31 {
			name = name[:31]
		}
		ct += fmt.Sprintf(`<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, n)
		wb += fmt.Sprintf(`<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, esc(name), n, n)
		rels += fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, n, n)
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
		for r, row := range s.Rows {
			fmt.Fprintf(&b, `<row r="%d">`, r+1)
			for c, v := range row {
				if v == "" {
					continue
				}
				ref := colName(c) + strconv.Itoa(r+1)
				style := ""
				if r == 0 {
					style = ` s="1"`
				}
				if _, err := strconv.ParseFloat(v, 64); err == nil && r > 0 && len(v) < 16 && !(len(v) > 1 && v[0] == '0' && v[1] != '.') {
					fmt.Fprintf(&b, `<c r="%s"%s><v>%s</v></c>`, ref, style, v)
				} else {
					fmt.Fprintf(&b, `<c r="%s" t="inlineStr"%s><is><t xml:space="preserve">%s</t></is></c>`, ref, style, esc(v))
				}
			}
			b.WriteString(`</row>`)
		}
		b.WriteString(`</sheetData></worksheet>`)
		if err := add(fmt.Sprintf("xl/worksheets/sheet%d.xml", n), b.String()); err != nil {
			return nil, err
		}
	}
	n := len(sheets) + 1
	rels += fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`, n)
	ct += `</Types>`
	wb += `</sheets></workbook>`
	styles := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs></styleSheet>`
	for _, f := range []struct{ n, b string }{
		{"[Content_Types].xml", ct},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", wb}, {"xl/_rels/workbook.xml.rels", rels}, {"xl/styles.xml", styles},
	} {
		if err := add(f.n, f.b); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
