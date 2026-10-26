// Package pdf writes the plain, printable PDFs Ledgerline sends to clients
// (contract notes and statements), optionally with an embedded digital
// signature. It uses the standard Helvetica fonts, so files are small and
// open everywhere. Standard library only.
package pdf

import (
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Page sizes in points.
const (
	A4W = 595.28
	A4H = 841.89
)

// Doc is a document being drawn. Coordinates are in points from the top
// left of the page.
type Doc struct {
	W, H  float64
	pages []*bytes.Buffer
	cur   *bytes.Buffer
}

// New starts a document, A4 portrait or landscape.
func New(landscape bool) *Doc {
	d := &Doc{W: A4W, H: A4H}
	if landscape {
		d.W, d.H = A4H, A4W
	}
	return d
}

// AddPage starts a new page.
func (d *Doc) AddPage() {
	d.cur = &bytes.Buffer{}
	d.pages = append(d.pages, d.cur)
}

// Pages is the number of pages so far.
func (d *Doc) Pages() int { return len(d.pages) }

// SetPage makes an earlier page current (for page numbers drawn at the end).
func (d *Doc) SetPage(i int) { d.cur = d.pages[i] }

func num(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// Text draws s with its baseline at y.
func (d *Doc) Text(x, y, size float64, bold bool, s string) {
	f := "F1"
	if bold {
		f = "F2"
	}
	fmt.Fprintf(d.cur, "BT /%s %s Tf %s %s Td (%s) Tj ET\n", f, num(size), num(x), num(d.H-y), escape(winAnsi(s)))
}

// TextRight draws s ending at x.
func (d *Doc) TextRight(x, y, size float64, bold bool, s string) {
	d.Text(x-Width(s, size, bold), y, size, bold, s)
}

// TextGray draws s in a gray (0 black … 1 white).
func (d *Doc) TextGray(x, y, size float64, bold bool, gray float64, s string) {
	fmt.Fprintf(d.cur, "%s g\n", num(gray))
	d.Text(x, y, size, bold, s)
	fmt.Fprintf(d.cur, "0 g\n")
}

// Line draws a line.
func (d *Doc) Line(x1, y1, x2, y2, width, gray float64) {
	fmt.Fprintf(d.cur, "%s G %s w %s %s m %s %s l S 0 G\n", num(gray), num(width), num(x1), num(d.H-y1), num(x2), num(d.H-y2))
}

// Fill paints a rectangle in a gray.
func (d *Doc) Fill(x, y, w, h, gray float64) {
	fmt.Fprintf(d.cur, "%s g %s %s %s %s re f 0 g\n", num(gray), num(x), num(d.H-y-h), num(w), num(h))
}

// Rect outlines a rectangle.
func (d *Doc) Rect(x, y, w, h, width, gray float64) {
	fmt.Fprintf(d.cur, "%s G %s w %s %s %s %s re S 0 G\n", num(gray), num(width), num(x), num(d.H-y-h), num(w), num(h))
}

// Width is the width of s at size in points.
func Width(s string, size float64, bold bool) float64 {
	w := 0
	tbl := &helv
	if bold {
		tbl = &helvBold
	}
	for _, c := range []byte(winAnsi(s)) {
		if c >= 32 && c < 127 {
			w += int(tbl[c-32])
		} else {
			w += 556
		}
	}
	return float64(w) * size / 1000
}

// Fit cuts s to fit width, ending with "...".
func Fit(s string, size float64, bold bool, width float64) string {
	if Width(s, size, bold) <= width {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && Width(string(r)+"...", size, bold) > width {
		r = r[:len(r)-1]
	}
	return string(r) + "..."
}

// Wrap breaks s into lines no wider than width.
func Wrap(s string, size float64, bold bool, width float64) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, w := range strings.Fields(para) {
			try := w
			if line != "" {
				try = line + " " + w
			}
			if Width(try, size, bold) <= width || line == "" {
				line = try
				continue
			}
			out = append(out, line)
			line = w
		}
		out = append(out, line)
	}
	return out
}

// winAnsi maps text to the fonts' WinAnsi encoding. The rupee sign has no
// glyph in the standard fonts, so it is written "Rs.".
func winAnsi(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '₹':
			b.WriteString("Rs.")
		case r == '–' || r == '—':
			b.WriteByte('-')
		case r == '‘' || r == '’':
			b.WriteByte('\'')
		case r == '“' || r == '”':
			b.WriteByte('"')
		case r == '·' || r == '•':
			b.WriteByte(0xB7)
		case r == '…':
			b.WriteString("...")
		case r == ' ':
			b.WriteByte(' ')
		case r >= 32 && r < 127:
			b.WriteRune(r)
		case r >= 0xA0 && r <= 0xFF:
			b.WriteByte(byte(r))
		case r == '\t':
			b.WriteByte(' ')
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

func escape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`, "\r", "", "\n", " ")
	return r.Replace(s)
}

// Meta is the document information.
type Meta struct {
	Title, Author, Subject string
	Created                time.Time
}

// SignatureSpec asks for a signature field and returns the signature to
// embed for the given bytes (everything except the signature itself).
type SignatureSpec struct {
	Name, Reason, Location, Contact string
	At                              time.Time
	Sign                            func(content []byte) ([]byte, error)
	Reserve                         int // bytes reserved for the signature; default 16384
}

func pdfDate(t time.Time) string {
	_, off := t.Zone()
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	return t.Format("D:20060102150405") + fmt.Sprintf("%s%02d'%02d'", sign, off/3600, (off%3600)/60)
}

// Bytes writes the PDF. With sig, it carries a signature field and the
// signature over every byte but the signature's own placeholder.
func (d *Doc) Bytes(meta Meta, sig *SignatureSpec) ([]byte, error) {
	if len(d.pages) == 0 {
		d.AddPage()
	}
	var buf bytes.Buffer
	offsets := []int{0}
	obj := func(body string) int {
		offsets = append(offsets, buf.Len())
		n := len(offsets) - 1
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
		return n
	}
	buf.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	// Object numbers are fixed up front so pages can refer to the tree.
	np := len(d.pages)
	catalog, pagesN, f1, f2 := 1, 2, 3, 4
	firstPage := 5
	contentN := func(i int) int { return firstPage + np + i }
	info := firstPage + 2*np
	sigField, sigDict := info+1, info+2

	acro := ""
	if sig != nil {
		acro = fmt.Sprintf(" /AcroForm << /Fields [%d 0 R] /SigFlags 3 >>", sigField)
	}
	obj(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R%s >>", pagesN, acro))
	var kids []string
	for i := 0; i < np; i++ {
		kids = append(kids, fmt.Sprintf("%d 0 R", firstPage+i))
	}
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), np))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	for i := 0; i < np; i++ {
		annots := ""
		if sig != nil && i == 0 {
			annots = fmt.Sprintf(" /Annots [%d 0 R]", sigField)
		}
		obj(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %s %s] /Resources << /Font << /F1 %d 0 R /F2 %d 0 R >> >> /Contents %d 0 R%s >>",
			pagesN, num(d.W), num(d.H), f1, f2, contentN(i), annots))
	}
	for i := 0; i < np; i++ {
		var z bytes.Buffer
		w := zlib.NewWriter(&z)
		w.Write(d.pages[i].Bytes())
		w.Close()
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n<< /Length %d /Filter /FlateDecode >>\nstream\n", len(offsets)-1, z.Len())
		buf.Write(z.Bytes())
		buf.WriteString("\nendstream\nendobj\n")
	}
	created := meta.Created
	if created.IsZero() {
		created = time.Now()
	}
	obj(fmt.Sprintf("<< /Title (%s) /Author (%s) /Subject (%s) /Producer (Ledgerline) /CreationDate (%s) >>",
		escape(winAnsi(meta.Title)), escape(winAnsi(meta.Author)), escape(winAnsi(meta.Subject)), pdfDate(created)))
	var contentsAt, contentsEnd, rangeAt int
	reserve := 0
	if sig != nil {
		obj(fmt.Sprintf("<< /Type /Annot /Subtype /Widget /FT /Sig /Rect [0 0 0 0] /F 132 /T (Signature1) /V %d 0 R /P %d 0 R >>", sigDict, firstPage))
		reserve = sig.Reserve
		if reserve == 0 {
			reserve = 16384
		}
		at := sig.At
		if at.IsZero() {
			at = time.Now()
		}
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n<< /Type /Sig /Filter /Adobe.PPKLite /SubFilter /adbe.pkcs7.detached /ByteRange ", len(offsets)-1)
		rangeAt = buf.Len()
		buf.WriteString("[0 0000000000 0000000000 0000000000]")
		buf.WriteString(" /Contents ")
		contentsAt = buf.Len()
		buf.WriteString("<" + strings.Repeat("0", 2*reserve) + ">")
		contentsEnd = buf.Len()
		fmt.Fprintf(&buf, " /M (%s) /Name (%s) /Reason (%s) /Location (%s) /ContactInfo (%s) >>\nendobj\n",
			pdfDate(at), escape(winAnsi(sig.Name)), escape(winAnsi(sig.Reason)), escape(winAnsi(sig.Location)), escape(winAnsi(sig.Contact)))
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, o := range offsets[1:] {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	id := md5.Sum([]byte(fmt.Sprint(meta.Title, created.UnixNano(), buf.Len())))
	h := hex.EncodeToString(id[:])
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R /Info %d 0 R /ID [<%s> <%s>] >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), catalog, info, h, h, xref)
	out := buf.Bytes()
	if sig == nil {
		return out, nil
	}
	// Fill in the byte range, then sign everything outside the placeholder.
	br := fmt.Sprintf("[0 %010d %010d %010d]", contentsAt, contentsEnd, len(out)-contentsEnd)
	copy(out[rangeAt:], br)
	content := append(append([]byte(nil), out[:contentsAt]...), out[contentsEnd:]...)
	cms, err := sig.Sign(content)
	if err != nil {
		return nil, err
	}
	if len(cms) > reserve {
		return nil, fmt.Errorf("signature is %d bytes, more than the %d reserved", len(cms), reserve)
	}
	hexSig := hex.EncodeToString(cms)
	copy(out[contentsAt+1:], hexSig)
	return out, nil
}

var (
	reByteRange = regexp.MustCompile(`/ByteRange\s*\[\s*(\d+)\s+(\d+)\s+(\d+)\s+(\d+)\s*\]`)
)

// Signed is a signature found in a PDF.
type Signed struct {
	CMS      []byte
	Content  []byte // the bytes the signature covers
	WholeDoc bool   // covers the whole file but the signature itself
}

// Signatures finds the signatures in a PDF (ours, or one from another
// back office) so they can be checked.
func Signatures(data []byte) ([]Signed, error) {
	var out []Signed
	for _, m := range reByteRange.FindAllSubmatch(data, -1) {
		var v [4]int
		for i := range v {
			v[i], _ = strconv.Atoi(string(m[i+1]))
		}
		if v[0] != 0 || v[1] <= 0 || v[2] < v[1] || v[2]+v[3] > len(data) || v[1] >= len(data) {
			continue
		}
		hole := data[v[1]:v[2]]
		if len(hole) < 2 || hole[0] != '<' || hole[len(hole)-1] != '>' {
			continue
		}
		raw, err := hex.DecodeString(string(bytes.Join(bytes.Fields(hole[1:len(hole)-1]), nil)))
		if err != nil {
			continue
		}
		raw = derTrim(raw)
		content := append(append([]byte(nil), data[:v[1]]...), data[v[2]:v[2]+v[3]]...)
		out = append(out, Signed{CMS: raw, Content: content, WholeDoc: v[2]+v[3] == len(data) || v[2]+v[3] == len(bytes.TrimRight(data, "\r\n"))})
	}
	if len(out) == 0 {
		return nil, errors.New("no signature in this PDF")
	}
	return out, nil
}

// Helvetica and Helvetica-Bold advance widths for characters 32-126.
var helv = [95]uint16{278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584}

var helvBold = [95]uint16{278, 333, 474, 556, 556, 889, 722, 238, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 333, 333, 584, 584, 584, 611,
	975, 722, 722, 722, 722, 667, 611, 778, 722, 278, 556, 722, 611, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 333, 278, 333, 584, 556,
	333, 556, 611, 556, 611, 556, 333, 611, 611, 278, 278, 556, 278, 889, 611, 611,
	611, 611, 389, 556, 333, 611, 556, 778, 556, 556, 500, 389, 280, 389, 584}

// derTrim cuts the zero padding after a DER value using its own length.
func derTrim(b []byte) []byte {
	if len(b) < 2 {
		return b
	}
	n, h := int(b[1]), 2
	if b[1]&0x80 != 0 {
		k := int(b[1] & 0x7f)
		if k == 0 || k > 4 || len(b) < 2+k {
			return b
		}
		n = 0
		for _, c := range b[2 : 2+k] {
			n = n<<8 | int(c)
		}
		h = 2 + k
	}
	if h+n <= len(b) {
		return b[:h+n]
	}
	return b
}
