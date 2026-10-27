package sheet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf16"
)

// ---- OLE2 compound file ----

const (
	endOfChain = 0xFFFFFFFE
	freeSect   = 0xFFFFFFFF
)

type cfb struct {
	data    []byte
	ssz     int
	fat     []uint32
	minifat []uint32
	ministr []byte
	cutoff  uint32
	dirs    []cfbDir
}

type cfbDir struct {
	name  string
	typ   byte
	start uint32
	size  uint64
}

func (c *cfb) sector(n uint32) ([]byte, error) {
	off := (int(n) + 1) * c.ssz
	if off < 0 || off+c.ssz > len(c.data) {
		return nil, errors.New("sheet: sector out of range")
	}
	return c.data[off : off+c.ssz], nil
}

func (c *cfb) chain(start uint32, max int) ([]byte, error) {
	var out []byte
	seen := 0
	for s := start; s != endOfChain && s != freeSect; {
		b, err := c.sector(s)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
		if int(s) >= len(c.fat) {
			return nil, errors.New("sheet: broken FAT chain")
		}
		s = c.fat[s]
		if seen++; seen > len(c.fat)+1 || (max > 0 && len(out) >= max+c.ssz) {
			break
		}
	}
	return out, nil
}

func openCFB(data []byte) (*cfb, error) {
	if len(data) < 512 {
		return nil, errors.New("sheet: file too short")
	}
	le := binary.LittleEndian
	shift := le.Uint16(data[30:])
	if shift != 9 && shift != 12 {
		return nil, fmt.Errorf("sheet: unsupported sector size 2^%d", shift)
	}
	c := &cfb{data: data, ssz: 1 << shift, cutoff: le.Uint32(data[56:])}
	nFAT := int(le.Uint32(data[44:]))
	dirStart := le.Uint32(data[48:])
	miniStart, nMini := le.Uint32(data[60:]), int(le.Uint32(data[64:]))
	difStart, nDif := le.Uint32(data[68:]), int(le.Uint32(data[72:]))
	var fatSecs []uint32
	for i := 0; i < 109 && len(fatSecs) < nFAT; i++ {
		fatSecs = append(fatSecs, le.Uint32(data[76+4*i:]))
	}
	for d, i := difStart, 0; i < nDif && d != endOfChain && d != freeSect && len(fatSecs) < nFAT; i++ {
		b, err := c.sector(d)
		if err != nil {
			return nil, err
		}
		per := c.ssz/4 - 1
		for j := 0; j < per && len(fatSecs) < nFAT; j++ {
			fatSecs = append(fatSecs, le.Uint32(b[4*j:]))
		}
		d = le.Uint32(b[4*per:])
	}
	for _, s := range fatSecs {
		b, err := c.sector(s)
		if err != nil {
			return nil, err
		}
		for j := 0; j+4 <= len(b); j += 4 {
			c.fat = append(c.fat, le.Uint32(b[j:]))
		}
	}
	dir, err := c.chain(dirStart, 0)
	if err != nil {
		return nil, err
	}
	for off := 0; off+128 <= len(dir); off += 128 {
		e := dir[off : off+128]
		nl := int(le.Uint16(e[64:]))
		if nl > 64 {
			nl = 64
		}
		var u []uint16
		for i := 0; i+1 < nl-1; i += 2 {
			u = append(u, le.Uint16(e[i:]))
		}
		c.dirs = append(c.dirs, cfbDir{name: string(utf16.Decode(u)), typ: e[66], start: le.Uint32(e[116:]), size: le.Uint64(e[120:]) & 0xFFFFFFFF})
	}
	if nMini > 0 && len(c.dirs) > 0 {
		mf, err := c.chain(miniStart, 0)
		if err != nil {
			return nil, err
		}
		for j := 0; j+4 <= len(mf); j += 4 {
			c.minifat = append(c.minifat, le.Uint32(mf[j:]))
		}
		root := c.dirs[0]
		c.ministr, err = c.chain(root.start, int(root.size))
		if err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *cfb) stream(name string) ([]byte, error) {
	for _, d := range c.dirs {
		if d.typ != 2 || !strings.EqualFold(d.name, name) {
			continue
		}
		if d.size < uint64(c.cutoff) && c.ministr != nil {
			var out []byte
			for s, n := d.start, 0; s != endOfChain && s != freeSect && n <= len(c.minifat); n++ {
				off := int(s) * 64
				if off+64 > len(c.ministr) || int(s) >= len(c.minifat) {
					return nil, errors.New("sheet: broken mini stream")
				}
				out = append(out, c.ministr[off:off+64]...)
				s = c.minifat[s]
			}
			if uint64(len(out)) > d.size {
				out = out[:d.size]
			}
			return out, nil
		}
		b, err := c.chain(d.start, int(d.size))
		if err != nil {
			return nil, err
		}
		if uint64(len(b)) > d.size {
			b = b[:d.size]
		}
		return b, nil
	}
	return nil, fmt.Errorf("sheet: no %s stream", name)
}

// ---- BIFF8 ----

type record struct {
	typ  uint16
	data []byte
}

func records(wb []byte, from int) []record {
	var out []record
	for off := from; off+4 <= len(wb); {
		t := binary.LittleEndian.Uint16(wb[off:])
		n := int(binary.LittleEndian.Uint16(wb[off+2:]))
		if off+4+n > len(wb) {
			break
		}
		out = append(out, record{t, wb[off+4 : off+4+n]})
		off += 4 + n
		if t == 0x000A { // EOF
			break
		}
	}
	return out
}

// strReader reads BIFF8 unicode strings that may run across CONTINUE records.
type strReader struct {
	parts [][]byte
	p, o  int
}

func (r *strReader) left() int {
	if r.p >= len(r.parts) {
		return 0
	}
	return len(r.parts[r.p]) - r.o
}

func (r *strReader) next() bool {
	if r.left() > 0 {
		return true
	}
	for r.p < len(r.parts) && r.left() <= 0 {
		r.p++
		r.o = 0
	}
	return r.p < len(r.parts)
}

func (r *strReader) bytes(n int) []byte {
	var out []byte
	for n > 0 && r.next() {
		k := min(n, r.left())
		out = append(out, r.parts[r.p][r.o:r.o+k]...)
		r.o += k
		n -= k
	}
	return out
}

func (r *strReader) u16() int {
	b := r.bytes(2)
	if len(b) < 2 {
		return 0
	}
	return int(binary.LittleEndian.Uint16(b))
}

func (r *strReader) u32() int {
	b := r.bytes(4)
	if len(b) < 4 {
		return 0
	}
	return int(binary.LittleEndian.Uint32(b))
}

func (r *strReader) str(cch int, flags byte) string {
	var u []uint16
	high := flags&1 == 1
	for cch > 0 {
		if !r.next() {
			break
		}
		if high {
			n := min(cch, r.left()/2)
			if n == 0 { // odd byte at a record end
				r.o = len(r.parts[r.p])
				continue
			}
			for b := r.bytes(2 * n); len(b) >= 2; b = b[2:] {
				u = append(u, binary.LittleEndian.Uint16(b))
			}
			cch -= n
		} else {
			n := min(cch, r.left())
			for _, c := range r.bytes(n) {
				u = append(u, uint16(c))
			}
			cch -= n
		}
		if cch > 0 { // the string continues in the next CONTINUE: a new flags byte
			r.p++
			r.o = 0
			if r.p < len(r.parts) {
				high = r.bytes(1)[0]&1 == 1
			}
		}
	}
	return string(utf16.Decode(u))
}

func (r *strReader) unicode() string {
	cch := r.u16()
	fb := r.bytes(1)
	if len(fb) == 0 {
		return ""
	}
	flags := fb[0]
	runs, ext := 0, 0
	if flags&8 != 0 {
		runs = r.u16()
	}
	if flags&4 != 0 {
		ext = r.u32()
	}
	s := r.str(cch, flags)
	r.bytes(4*runs + ext)
	return s
}

func shortStr(b []byte) string { // BIFF8 LABEL / STRING: cch u16, flags, chars
	if len(b) < 3 {
		return ""
	}
	r := &strReader{parts: [][]byte{b}}
	return r.unicode()
}

func rk(v uint32) float64 {
	var f float64
	if v&2 != 0 {
		f = float64(int32(v) >> 2)
	} else {
		f = math.Float64frombits(uint64(v&0xFFFFFFFC) << 32)
	}
	if v&1 != 0 {
		f /= 100
	}
	return f
}

// ReadXLS reads a BIFF8 (Excel 97–2003) workbook.
func ReadXLS(data []byte) ([]Sheet, error) {
	c, err := openCFB(data)
	if err != nil {
		return nil, err
	}
	wb, err := c.stream("Workbook")
	if err != nil {
		if wb, err = c.stream("Book"); err != nil {
			return nil, err
		}
	}
	le := binary.LittleEndian
	globals := records(wb, 0)
	if len(globals) == 0 || globals[0].typ != 0x0809 || len(globals[0].data) < 2 {
		return nil, errors.New("sheet: not an Excel 5 or later workbook")
	}
	ver := le.Uint16(globals[0].data)
	if ver != 0x0600 && ver != 0x0500 {
		return nil, fmt.Errorf("sheet: unsupported Excel version %#x", ver)
	}
	v8 := ver == 0x0600
	label := func(b []byte) string { // LABEL / STRING text
		if v8 {
			return shortStr(b)
		}
		if len(b) < 2 {
			return ""
		}
		n := int(le.Uint16(b))
		return cp1252(b[2:min(len(b), 2+n)])
	}
	var sst []string
	type bs struct {
		name string
		off  int
	}
	var sheets []bs
	for i, r := range globals {
		switch r.typ {
		case 0x0085: // BOUNDSHEET
			if len(r.data) >= 8 && r.data[5] == 0 { // worksheet
				name := shortStr8(r.data[6:])
				if !v8 {
					name = cp1252(r.data[7:min(len(r.data), 7+int(r.data[6]))])
				}
				sheets = append(sheets, bs{name, int(le.Uint32(r.data))})
			}
		case 0x00FC: // SST
			parts := [][]byte{r.data[8:]}
			for j := i + 1; j < len(globals) && globals[j].typ == 0x003C; j++ {
				parts = append(parts, globals[j].data)
			}
			n := int(le.Uint32(r.data[4:]))
			sr := &strReader{parts: parts}
			for k := 0; k < n && sr.next(); k++ {
				sst = append(sst, sr.unicode())
			}
		}
	}
	var out []Sheet
	for _, s := range sheets {
		sh := Sheet{Name: s.name}
		rs := records(wb, s.off)
		for i, r := range rs {
			d := r.data
			if len(d) < 6 {
				continue
			}
			row, col := int(le.Uint16(d)), int(le.Uint16(d[2:]))
			switch r.typ {
			case 0x00FD: // LABELSST
				if len(d) >= 10 {
					if k := int(le.Uint32(d[6:])); k < len(sst) {
						put(&sh.Rows, row, col, sst[k])
					}
				}
			case 0x0204, 0x00D6: // LABEL, RSTRING
				put(&sh.Rows, row, col, label(d[6:]))
			case 0x0203: // NUMBER
				if len(d) >= 14 {
					put(&sh.Rows, row, col, num(math.Float64frombits(le.Uint64(d[6:]))))
				}
			case 0x027E: // RK
				if len(d) >= 10 {
					put(&sh.Rows, row, col, num(rk(le.Uint32(d[6:]))))
				}
			case 0x00BD: // MULRK
				last := int(le.Uint16(d[len(d)-2:]))
				for k, cc := 0, col; cc <= last && 4+6*k+6 <= len(d)-2; k, cc = k+1, cc+1 {
					put(&sh.Rows, row, cc, num(rk(le.Uint32(d[4+6*k+2:]))))
				}
			case 0x0205: // BOOLERR
				if len(d) >= 8 && d[7] == 0 {
					put(&sh.Rows, row, col, map[byte]string{0: "FALSE", 1: "TRUE"}[d[6]])
				}
			case 0x0006: // FORMULA: cached result
				if len(d) < 14 {
					continue
				}
				if le.Uint16(d[12:]) != 0xFFFF {
					put(&sh.Rows, row, col, num(math.Float64frombits(le.Uint64(d[6:]))))
				} else if d[6] == 0 && i+1 < len(rs) && rs[i+1].typ == 0x0207 {
					put(&sh.Rows, row, col, label(rs[i+1].data))
				} else if d[6] == 1 {
					put(&sh.Rows, row, col, map[byte]string{0: "FALSE", 1: "TRUE"}[d[8]])
				}
			}
		}
		out = append(out, sh)
	}
	return out, nil
}

// shortStr8 reads a sheet name: cch u8, flags, chars.
func shortStr8(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	r := &strReader{parts: [][]byte{b[2:]}}
	return r.str(int(b[0]), b[1])
}

var cp1252hi = [32]rune{'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F, 0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ'}

// cp1252 decodes Windows-1252 text (Excel 5/95 workbooks).
func cp1252(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		if c >= 0x80 && c < 0xA0 {
			r[i] = cp1252hi[c-0x80]
		} else {
			r[i] = rune(c)
		}
	}
	return string(r)
}
