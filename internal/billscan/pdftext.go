// Package billscan reads vendor bills: the text of computer-made PDFs, and
// the details a back office needs from it (vendor, GSTIN, address, bank,
// bill number and date, taxable value, GST and total).
package billscan

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ErrNoText means the file has no readable text (a scan or a photo).
var ErrNoText = errors.New("billscan: no readable text in the file")

type pdfObj struct {
	dict   string
	stream []byte // decoded
}

var (
	reObj     = regexp.MustCompile(`(\d+)\s+(\d+)\s+obj\b`)
	reRef     = regexp.MustCompile(`(\d+)\s+0\s+R`)
	reFontRef = regexp.MustCompile(`/([^\s/<>\[\]()]+)\s+(\d+)\s+0\s+R`)
)

// parseObjects reads every indirect object, including those packed in
// object streams.
func parseObjects(data []byte) map[int]*pdfObj {
	objs := map[int]*pdfObj{}
	locs := reObj.FindAllSubmatchIndex(data, -1)
	for i, m := range locs {
		num, _ := strconv.Atoi(string(data[m[2]:m[3]]))
		start := m[1]
		end := len(data)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := data[start:end]
		if e := bytes.Index(body, []byte("endobj")); e >= 0 {
			body = body[:e]
		}
		o := &pdfObj{}
		if s := bytes.Index(body, []byte("stream")); s >= 0 && bytes.Contains(body[:s], []byte("<<")) {
			o.dict = string(body[:s])
			raw := body[s+6:]
			raw = bytes.TrimLeft(raw, "\r\n")
			if e := bytes.LastIndex(raw, []byte("endstream")); e >= 0 {
				raw = raw[:e]
			}
			raw = bytes.TrimRight(raw, "\r\n")
			o.stream = decodeStream(o.dict, raw)
		} else {
			o.dict = string(body)
		}
		objs[num] = o
	}
	// Objects packed in object streams.
	for _, o := range objs {
		if !strings.Contains(o.dict, "/ObjStm") || o.stream == nil {
			continue
		}
		n := dictInt(o.dict, "/N")
		first := dictInt(o.dict, "/First")
		if n <= 0 || first <= 0 || first > len(o.stream) {
			continue
		}
		head := strings.Fields(string(o.stream[:first]))
		for i := 0; i+1 < len(head) && i/2 < n; i += 2 {
			num, _ := strconv.Atoi(head[i])
			off, _ := strconv.Atoi(head[i+1])
			end := len(o.stream)
			if i+3 < len(head) {
				if nx, err := strconv.Atoi(head[i+3]); err == nil {
					end = first + nx
				}
			}
			if first+off < end && end <= len(o.stream) {
				if _, ok := objs[num]; !ok {
					objs[num] = &pdfObj{dict: string(o.stream[first+off : end])}
				}
			}
		}
	}
	return objs
}

var reFilter = regexp.MustCompile(`/(ASCIIHexDecode|ASCII85Decode|FlateDecode|AHx|A85|Fl|[A-Za-z0-9]+Decode)\b`)

// decodeStream applies a stream's filters in order. Image filters (JPEG
// and the like) give nil: not text.
func decodeStream(dict string, raw []byte) []byte {
	fi := strings.Index(dict, "/Filter")
	if fi < 0 {
		return raw
	}
	spec := dict[fi+7:]
	if r, _ := dictValue(dict, "/Filter"); r == 0 {
		spec = strings.TrimLeft(spec, " \r\n\t")
		if strings.HasPrefix(spec, "[") {
			if e := strings.Index(spec, "]"); e >= 0 {
				spec = spec[:e]
			}
		} else if m := regexp.MustCompile(`^/[A-Za-z0-9]+`).FindString(spec); m != "" {
			spec = m
		}
	}
	for _, m := range reFilter.FindAllStringSubmatch(spec, -1) {
		switch m[1] {
		case "FlateDecode", "Fl":
			r, err := zlib.NewReader(bytes.NewReader(raw))
			if err != nil {
				return nil
			}
			b, _ := io.ReadAll(io.LimitReader(r, 20<<20))
			if len(b) == 0 {
				return nil
			}
			raw = b
		case "ASCII85Decode", "A85":
			raw = ascii85(raw)
		case "ASCIIHexDecode", "AHx":
			raw = hexBytes(bytes.TrimSuffix(bytes.TrimSpace(raw), []byte(">")))
		default:
			return nil
		}
	}
	return raw
}

func ascii85(b []byte) []byte {
	b = bytes.TrimPrefix(bytes.TrimSpace(b), []byte("<~"))
	if e := bytes.Index(b, []byte("~>")); e >= 0 {
		b = b[:e]
	}
	var out []byte
	var grp [5]byte
	n := 0
	flush := func(k int) {
		v := uint32(0)
		for i := 0; i < 5; i++ {
			c := byte('u')
			if i < k {
				c = grp[i]
			}
			v = v*85 + uint32(c-'!')
		}
		w := []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		out = append(out, w[:k-1]...)
	}
	for _, c := range b {
		switch {
		case c == 'z' && n == 0:
			out = append(out, 0, 0, 0, 0)
		case c >= '!' && c <= 'u':
			grp[n] = c
			n++
			if n == 5 {
				flush(5)
				n = 0
			}
		}
	}
	if n > 1 {
		flush(n)
	}
	return out
}

func dictInt(d, key string) int {
	i := strings.Index(d, key)
	if i < 0 {
		return 0
	}
	f := strings.Fields(d[i+len(key):])
	if len(f) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(strings.Trim(f[0], "/>"))
	return n
}

// dictValue returns what follows key: a reference's object number, or the
// inline text up to its end.
func dictValue(d, key string) (ref int, inline string) {
	i := strings.Index(d, key)
	if i < 0 {
		return 0, ""
	}
	rest := strings.TrimLeft(d[i+len(key):], " \r\n\t")
	if m := regexp.MustCompile(`^(\d+)\s+0\s+R`).FindStringSubmatch(rest); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, ""
	}
	if strings.HasPrefix(rest, "<<") {
		depth := 0
		for j := 0; j+1 < len(rest); j++ {
			switch rest[j : j+2] {
			case "<<":
				depth++
				j++
			case ">>":
				depth--
				j++
				if depth == 0 {
					return 0, rest[:j+1]
				}
			}
		}
	}
	if strings.HasPrefix(rest, "[") {
		if e := strings.Index(rest, "]"); e >= 0 {
			return 0, rest[:e+1]
		}
	}
	return 0, ""
}

// cmap maps character codes to text (a font's ToUnicode).
type cmap struct {
	width int // bytes per code
	m     map[uint32]string
}

var (
	reBfChar  = regexp.MustCompile(`<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>`)
	reBfRange = regexp.MustCompile(`<([0-9A-Fa-f]+)>\s*<([0-9A-Fa-f]+)>\s*(<[0-9A-Fa-f]+>|\[[^\]]*\])`)
)

func utf16hex(h string) string {
	b := make([]byte, len(h)/2)
	for i := range b {
		v, _ := strconv.ParseUint(h[2*i:2*i+2], 16, 8)
		b[i] = byte(v)
	}
	var rs []rune
	for i := 0; i+1 < len(b); i += 2 {
		rs = append(rs, rune(uint16(b[i])<<8|uint16(b[i+1])))
	}
	return string(rs)
}

func parseCMap(s string) *cmap {
	c := &cmap{m: map[uint32]string{}, width: 1}
	sections := func(begin, end string) []string {
		var out []string
		for {
			i := strings.Index(s, begin)
			if i < 0 {
				return out
			}
			j := strings.Index(s[i:], end)
			if j < 0 {
				return out
			}
			out = append(out, s[i+len(begin):i+j])
			s = s[i+j+len(end):]
		}
	}
	all := s
	for _, sec := range sections("beginbfrange", "endbfrange") {
		for _, m := range reBfRange.FindAllStringSubmatch(sec, -1) {
			lo, _ := strconv.ParseUint(m[1], 16, 32)
			hi, _ := strconv.ParseUint(m[2], 16, 32)
			if len(m[1]) >= 4 {
				c.width = 2
			}
			if hi < lo || hi-lo > 65535 {
				continue
			}
			if strings.HasPrefix(m[3], "[") {
				items := regexp.MustCompile(`<([0-9A-Fa-f]+)>`).FindAllStringSubmatch(m[3], -1)
				for k, it := range items {
					c.m[uint32(lo)+uint32(k)] = utf16hex(it[1])
				}
				continue
			}
			dst := strings.Trim(m[3], "<>")
			base, _ := strconv.ParseUint(dst, 16, 32)
			for code := lo; code <= hi; code++ {
				r := rune(base + (code - lo))
				c.m[uint32(code)] = string(r)
			}
		}
	}
	s = all
	for _, sec := range sections("beginbfchar", "endbfchar") {
		for _, m := range reBfChar.FindAllStringSubmatch(sec, -1) {
			code, _ := strconv.ParseUint(m[1], 16, 32)
			if len(m[1]) >= 4 {
				c.width = 2
			}
			c.m[uint32(code)] = utf16hex(m[2])
		}
	}
	return c
}

type frag struct {
	x, y, end, size float64 // device space
	text            string
}

type mat [6]float64

var ident = mat{1, 0, 0, 1, 0, 0}

func (m mat) mul(n mat) mat {
	return mat{
		m[0]*n[0] + m[1]*n[2], m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2], m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4], m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

// ExtractText returns the text of a PDF, line by line in reading order.
func ExtractText(data []byte) (string, error) {
	if !bytes.HasPrefix(bytes.TrimLeft(data, " \r\n\t"), []byte("%PDF")) {
		return "", ErrNoText
	}
	objs := parseObjects(data)
	var lines []string
	pages := 0
	for _, num := range sortedObjNums(objs) {
		o := objs[num]
		if !regexp.MustCompile(`/Type\s*/Page\b`).MatchString(o.dict) || strings.Contains(o.dict, "/Type /Pages") || strings.Contains(o.dict, "/Type/Pages") {
			continue
		}
		pages++
		fonts := pageFonts(objs, o)
		var content []byte
		ref, inline := dictValue(o.dict, "/Contents")
		if ref > 0 {
			if c := objs[ref]; c != nil {
				if c.stream != nil {
					content = c.stream
				} else {
					inline = c.dict // an array of streams
				}
			}
		}
		for _, m := range reRef.FindAllStringSubmatch(inline, -1) {
			n, _ := strconv.Atoi(m[1])
			if c := objs[n]; c != nil {
				content = append(append(content, c.stream...), '\n')
			}
		}
		lines = append(lines, layout(runContent(content, fonts))...)
		if pages >= 20 {
			break
		}
	}
	text := strings.Join(lines, "\n")
	if !readable(text) {
		return text, ErrNoText
	}
	return text, nil
}

func sortedObjNums(objs map[int]*pdfObj) []int {
	ns := make([]int, 0, len(objs))
	for n := range objs {
		ns = append(ns, n)
	}
	sort.Ints(ns)
	return ns
}

// pdfFont is what text reading needs from a font: its ToUnicode map and
// glyph widths (in thousandths of the font size).
type pdfFont struct {
	cm   *cmap
	two  bool // two-byte codes (Type0)
	w    map[uint32]float64
	dw   float64
	base string
}

// read turns a string's bytes into text and its width.
func (f *pdfFont) read(b []byte) (string, float64) {
	if f == nil {
		return latin1(b), 500 * float64(len(b))
	}
	step := 1
	if f.two || (f.cm != nil && f.cm.width == 2) {
		step = 2
	}
	var sb strings.Builder
	width := 0.0
	for i := 0; i+step <= len(b); i += step {
		code := uint32(b[i])
		if step == 2 {
			code = code<<8 | uint32(b[i+1])
		}
		switch {
		case f.cm != nil:
			if t, ok := f.cm.m[code]; ok {
				sb.WriteString(t)
			} else if step == 1 {
				sb.WriteString(latin1([]byte{b[i]}))
			}
		default:
			sb.WriteString(latin1([]byte{b[i]}))
		}
		if w, ok := f.w[code]; ok {
			width += w
		} else {
			width += f.dw
		}
	}
	return sb.String(), width
}

var reNumTok = regexp.MustCompile(`\[|\]|-?[\d.]+`)

// parseW reads a CID font's /W array.
func parseW(s string, into map[uint32]float64) {
	toks := reNumTok.FindAllString(s, -1)
	if len(toks) > 0 && toks[0] == "[" {
		toks = toks[1:]
	}
	for i := 0; i < len(toks); {
		first, err := strconv.ParseFloat(toks[i], 64)
		if err != nil {
			i++
			continue
		}
		if i+1 < len(toks) && toks[i+1] == "[" {
			j, k := i+2, 0
			for ; j < len(toks) && toks[j] != "]"; j++ {
				w, _ := strconv.ParseFloat(toks[j], 64)
				into[uint32(first)+uint32(k)] = w
				k++
			}
			i = j + 1
			continue
		}
		if i+2 < len(toks) {
			last, _ := strconv.ParseFloat(toks[i+1], 64)
			w, _ := strconv.ParseFloat(toks[i+2], 64)
			if last >= first && last-first < 65536 {
				for c := first; c <= last; c++ {
					into[uint32(c)] = w
				}
			}
		}
		i += 3
	}
}

// arrayOrRef returns an array value, following a reference.
func arrayOrRef(objs map[int]*pdfObj, d, key string) string {
	ref, inline := dictValue(d, key)
	if ref > 0 && objs[ref] != nil {
		return objs[ref].dict
	}
	if inline == "" || strings.HasPrefix(inline, "[") {
		// arrays nest: take up to the matching bracket
		i := strings.Index(d, key)
		if i >= 0 {
			rest := strings.TrimLeft(d[i+len(key):], " \r\n\t")
			if strings.HasPrefix(rest, "[") {
				depth := 0
				for j, r := range rest {
					if r == '[' {
						depth++
					} else if r == ']' {
						depth--
						if depth == 0 {
							return rest[:j+1]
						}
					}
				}
			}
		}
	}
	return inline
}

func loadFont(objs map[int]*pdfObj, f *pdfObj) *pdfFont {
	pf := &pdfFont{w: map[uint32]float64{}, dw: 1000}
	if tu, _ := dictValue(f.dict, "/ToUnicode"); tu > 0 && objs[tu] != nil && objs[tu].stream != nil {
		pf.cm = parseCMap(string(objs[tu].stream))
	}
	if strings.Contains(f.dict, "/Type0") {
		pf.two = true
		desc := arrayOrRef(objs, f.dict, "/DescendantFonts")
		if m := reRef.FindStringSubmatch(desc); m != nil {
			n, _ := strconv.Atoi(m[1])
			if d := objs[n]; d != nil {
				if v := dictInt(d.dict, "/DW"); v > 0 {
					pf.dw = float64(v)
				}
				parseW(arrayOrRef(objs, d.dict, "/W"), pf.w)
			}
		}
		return pf
	}
	pf.dw = 520 // standard fonts carry no widths; about Helvetica's average
	if strings.Contains(f.dict, "/Type3") {
		pf.dw = 500
	}
	first := dictInt(f.dict, "/FirstChar")
	ws := arrayOrRef(objs, f.dict, "/Widths")
	for k, t := range reNumTok.FindAllString(ws, -1) {
		if t == "[" || t == "]" {
			continue
		}
		w, _ := strconv.ParseFloat(t, 64)
		pf.w[uint32(first+k-1)] = w
	}
	if strings.Contains(f.dict, "/Type3") {
		// Type3 widths are in glyph space; scale by the font matrix.
		if fm := arrayOrRef(objs, f.dict, "/FontMatrix"); fm != "" {
			if t := reNumTok.FindAllString(fm, -1); len(t) > 1 {
				if a, err := strconv.ParseFloat(t[1], 64); err == nil && a != 0 {
					for k, v := range pf.w {
						pf.w[k] = v * math.Abs(a) * 1000
					}
				}
			}
		}
	}
	return pf
}

// pageFonts maps a page's font names to their fonts.
func pageFonts(objs map[int]*pdfObj, page *pdfObj) map[string]*pdfFont {
	res := ""
	for p, hops := page, 0; p != nil && hops < 10; hops++ {
		ref, inline := dictValue(p.dict, "/Resources")
		if ref > 0 && objs[ref] != nil {
			res = objs[ref].dict
		} else {
			res = inline
		}
		if res != "" {
			break
		}
		pr, _ := dictValue(p.dict, "/Parent")
		p = objs[pr]
	}
	fontDict := ""
	if ref, inline := dictValue(res, "/Font"); ref > 0 && objs[ref] != nil {
		fontDict = objs[ref].dict
	} else {
		fontDict = inline
	}
	out := map[string]*pdfFont{}
	for _, m := range reFontRef.FindAllStringSubmatch(fontDict, -1) {
		n, _ := strconv.Atoi(m[2])
		if f := objs[n]; f != nil {
			out[m[1]] = loadFont(objs, f)
		}
	}
	return out
}

// runContent walks a content stream's text operators and places each
// piece of text on the page.
func runContent(c []byte, fonts map[string]*pdfFont) []frag {
	var out []frag
	var stack []any
	var font *pdfFont
	ctm, tm, tlm := ident, ident, ident
	var saved []mat
	var fs, leading float64 = 10, 0
	// emit places text whose width is in thousandths of the font size.
	emit := func(s string, width float64) {
		w := width / 1000 * fs
		if strings.TrimSpace(s) != "" {
			d := tm.mul(ctm)
			scale := math.Hypot(d[2], d[3])
			if scale == 0 {
				scale = 1
			}
			e := mat{1, 0, 0, 1, w, 0}.mul(tm).mul(ctm)
			out = append(out, frag{x: d[4], y: d[5], end: e[4], size: fs * scale, text: s})
		}
		tm = mat{1, 0, 0, 1, w, 0}.mul(tm)
	}
	str := func(v any) (string, float64) {
		b, _ := v.([]byte)
		return font.read(b)
	}
	num := func(i int) float64 {
		if i < 0 || i >= len(stack) {
			return 0
		}
		f, _ := stack[i].(float64)
		return f
	}
	nums := func(k int) []float64 {
		v := make([]float64, k)
		for j := 0; j < k; j++ {
			v[j] = num(len(stack) - k + j)
		}
		return v
	}
	td := func(x, y float64) {
		tlm = mat{1, 0, 0, 1, x, y}.mul(tlm)
		tm = tlm
	}
	i := 0
	for i < len(c) {
		ch := c[i]
		switch {
		case ch == '%':
			for i < len(c) && c[i] != '\n' && c[i] != '\r' {
				i++
			}
		case ch == '(':
			s, n := readLiteral(c[i:])
			stack = append(stack, s)
			i += n
			continue
		case ch == '<' && i+1 < len(c) && c[i+1] == '<':
			// inline dictionary (marked content properties): skip it
			depth := 0
			for i+1 < len(c) {
				if c[i] == '<' && c[i+1] == '<' {
					depth++
					i += 2
					continue
				}
				if c[i] == '>' && c[i+1] == '>' {
					depth--
					i += 2
					if depth == 0 {
						break
					}
					continue
				}
				i++
			}
			continue
		case ch == '<':
			j := bytes.IndexByte(c[i:], '>')
			if j < 0 {
				return out
			}
			stack = append(stack, hexBytes(c[i+1:i+j]))
			i += j + 1
			continue
		case ch == '[':
			j, arr := i+1, []any{}
			for j < len(c) && c[j] != ']' {
				switch {
				case c[j] == '(':
					s, n := readLiteral(c[j:])
					arr = append(arr, s)
					j += n
				case c[j] == '<':
					k := bytes.IndexByte(c[j:], '>')
					if k < 0 {
						return out
					}
					arr = append(arr, hexBytes(c[j+1:j+k]))
					j += k + 1
				case c[j] == '-' || c[j] == '.' || (c[j] >= '0' && c[j] <= '9'):
					k := j + 1
					for k < len(c) && (c[k] == '.' || (c[k] >= '0' && c[k] <= '9')) {
						k++
					}
					f, _ := strconv.ParseFloat(string(c[j:k]), 64)
					arr = append(arr, f)
					j = k
				default:
					j++
				}
			}
			stack = append(stack, arr)
			i = j + 1
			continue
		case ch == '/' || unicode.IsLetter(rune(ch)) || ch == '\'' || ch == '"' || ch == '*':
			j := i + 1
			for j < len(c) && !isDelim(c[j]) {
				j++
			}
			tok := string(c[i:j])
			i = j
			if tok[0] == '/' {
				stack = append(stack, tok)
				continue
			}
			switch tok {
			case "q":
				saved = append(saved, ctm)
			case "Q":
				if n := len(saved); n > 0 {
					ctm, saved = saved[n-1], saved[:n-1]
				}
			case "cm":
				v := nums(6)
				ctm = mat{v[0], v[1], v[2], v[3], v[4], v[5]}.mul(ctm)
			case "BT":
				tm, tlm = ident, ident
			case "Tf":
				if len(stack) >= 2 {
					if name, ok := stack[len(stack)-2].(string); ok {
						font = fonts[strings.TrimPrefix(name, "/")]
					}
					if f := num(len(stack) - 1); f != 0 {
						fs = math.Abs(f)
					}
				}
			case "TL":
				leading = num(len(stack) - 1)
			case "Td":
				v := nums(2)
				td(v[0], v[1])
			case "TD":
				v := nums(2)
				leading = -v[1]
				td(v[0], v[1])
			case "Tm":
				v := nums(6)
				tlm = mat{v[0], v[1], v[2], v[3], v[4], v[5]}
				tm = tlm
			case "T*":
				td(0, -leading)
			case "Tj":
				if len(stack) > 0 {
					emit(str(stack[len(stack)-1]))
				}
			case "'", "\"":
				td(0, -leading)
				if len(stack) > 0 {
					emit(str(stack[len(stack)-1]))
				}
			case "TJ":
				if len(stack) > 0 {
					if arr, ok := stack[len(stack)-1].([]any); ok {
						var sb strings.Builder
						width := 0.0
						for _, a := range arr {
							switch v := a.(type) {
							case []byte:
								t, w := str(v)
								sb.WriteString(t)
								width += w
							case float64:
								if v < -180 {
									sb.WriteByte(' ')
								}
								width -= v
							}
						}
						emit(sb.String(), width)
					}
				}
			case "BI":
				// inline image: skip to EI
				if e := bytes.Index(c[i:], []byte("EI")); e >= 0 {
					i += e + 2
				}
			}
			stack = stack[:0]
			continue
		case ch == '-' || ch == '.' || ch == '+' || (ch >= '0' && ch <= '9'):
			j := i + 1
			for j < len(c) && (c[j] == '.' || (c[j] >= '0' && c[j] <= '9')) {
				j++
			}
			f, _ := strconv.ParseFloat(string(c[i:j]), 64)
			stack = append(stack, f)
			i = j
			continue
		}
		i++
	}
	return out
}

func isDelim(b byte) bool {
	return b == ' ' || b == '\n' || b == '\r' || b == '\t' || b == '/' || b == '(' || b == '[' || b == '<' || b == ']' || b == '>'
}

func readLiteral(c []byte) ([]byte, int) {
	var out []byte
	depth := 0
	i := 0
	for i < len(c) {
		b := c[i]
		switch {
		case b == '(':
			if depth > 0 {
				out = append(out, b)
			}
			depth++
		case b == ')':
			depth--
			if depth == 0 {
				return out, i + 1
			}
			out = append(out, b)
		case b == '\\' && i+1 < len(c):
			i++
			switch e := c[i]; e {
			case 'n':
				out = append(out, '\n')
			case 'r', 't', 'b', 'f':
				out = append(out, ' ')
			case '\r', '\n':
			default:
				if e >= '0' && e <= '7' {
					v, k := 0, 0
					for k < 3 && i < len(c) && c[i] >= '0' && c[i] <= '7' {
						v = v*8 + int(c[i]-'0')
						i++
						k++
					}
					out = append(out, byte(v))
					continue
				}
				out = append(out, e)
			}
		default:
			out = append(out, b)
		}
		i++
	}
	return out, i
}

func hexBytes(h []byte) []byte {
	h = bytes.Map(func(r rune) rune {
		if unicode.Is(unicode.ASCII_Hex_Digit, r) {
			return r
		}
		return -1
	}, h)
	if len(h)%2 == 1 {
		h = append(h, '0')
	}
	out := make([]byte, len(h)/2)
	for i := range out {
		v, _ := strconv.ParseUint(string(h[2*i:2*i+2]), 16, 8)
		out[i] = byte(v)
	}
	return out
}

// latin1 reads single-byte text (WinAnsi is Latin-1 for what bills use).
func latin1(b []byte) string {
	rs := make([]rune, 0, len(b))
	for _, x := range b {
		switch {
		case x == 0x80:
			rs = append(rs, '€')
		case x >= 0x20 || x == '\n':
			rs = append(rs, rune(x))
		}
	}
	return string(rs)
}

// layout sorts pieces of text into lines, top to bottom and left to
// right, joining letters placed one by one into words and keeping a wide
// gap (two spaces) between columns.
func layout(fs []frag) []string {
	if len(fs) == 0 {
		return nil
	}
	// Device space is y-up unless the page flips it; pages flipped by the
	// CTM end up with y down, which the sort below handles either way by
	// sorting on the y the page shows: higher first.
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].y > fs[j].y })
	var rows [][]frag
	for _, f := range fs {
		tol := math.Max(2, 0.4*f.size)
		if n := len(rows); n > 0 && math.Abs(rows[n-1][0].y-f.y) <= tol {
			rows[n-1] = append(rows[n-1], f)
			continue
		}
		rows = append(rows, []frag{f})
	}
	var lines []string
	for _, r := range rows {
		sort.SliceStable(r, func(i, j int) bool { return r[i].x < r[j].x })
		var sb strings.Builder
		prevEnd := math.Inf(-1)
		for _, f := range r {
			if sb.Len() > 0 {
				gap := f.x - prevEnd
				switch {
				case gap < 0.12*f.size:
				case gap < 1.6*f.size:
					sb.WriteByte(' ')
				default:
					sb.WriteString("  ")
				}
			}
			sb.WriteString(f.text)
			prevEnd = math.Max(prevEnd, f.end)
			if f.text == " " {
				prevEnd = f.x
			}
		}
		if l := strings.Join(strings.FieldsFunc(sb.String(), func(r rune) bool { return r == '\n' }), " "); strings.TrimSpace(l) != "" {
			lines = append(lines, collapse(l))
		}
	}
	return lines
}

// collapse keeps single and double spaces, squeezing longer runs to two.
func collapse(s string) string {
	s = strings.TrimSpace(s)
	for strings.Contains(s, "   ") {
		s = strings.ReplaceAll(s, "   ", "  ")
	}
	return s
}

// readable says whether text looks like words and numbers, not glyph codes.
func readable(s string) bool {
	if len(s) < 20 {
		return false
	}
	good, all := 0, 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		all++
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(".,:/-₹()%&@#'", r) {
			good++
		}
	}
	return all > 0 && float64(good)/float64(all) > 0.85
}
