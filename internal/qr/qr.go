// Package qr draws QR codes (ISO/IEC 18004) for short texts such as a UPI
// payment link: byte mode, error correction level M, versions 1 to 10
// (up to 213 bytes), the mask chosen by the standard's penalty rules. It
// follows the structure of Project Nayuki's reference implementation.
package qr

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
)

// Level M tables for versions 1 to 10 (index = version): error correction
// codewords per block, and the number of blocks.
var (
	eccPerBlock = [...]int{0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26}
	numBlocks   = [...]int{0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5}
)

const maxVersion = 10

// ErrTooLong is returned for a text that does not fit version 10.
var ErrTooLong = errors.New("qr: text too long")

// Code is a drawn QR code: Size × Size modules, true = dark.
type Code struct {
	Size    int
	Version int
	modules [][]bool
}

// Dark says whether the module at column x, row y is dark.
func (c *Code) Dark(x, y int) bool { return c.modules[y][x] }

// numRawDataModules is how many modules hold data and error correction.
func numRawDataModules(ver int) int {
	r := (16*ver+128)*ver + 64
	if ver >= 2 {
		na := ver/7 + 2
		r -= (25*na-10)*na - 55
		if ver >= 7 {
			r -= 36
		}
	}
	return r
}

func numDataCodewords(ver int) int {
	return numRawDataModules(ver)/8 - eccPerBlock[ver]*numBlocks[ver]
}

// Encode draws text as a QR code in byte mode at level M.
func Encode(text []byte) (*Code, error) {
	ver := 0
	for v := 1; v <= maxVersion; v++ {
		cc := 8
		if v >= 10 {
			cc = 16
		}
		if 4+cc+8*len(text) <= 8*numDataCodewords(v) {
			ver = v
			break
		}
	}
	if ver == 0 {
		return nil, ErrTooLong
	}
	// bits: byte mode (0100), the count, the bytes, terminator, padding
	var bits []bool
	put := func(v, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, (v>>i)&1 == 1)
		}
	}
	put(4, 4)
	if ver >= 10 {
		put(len(text), 16)
	} else {
		put(len(text), 8)
	}
	for _, b := range text {
		put(int(b), 8)
	}
	capBits := 8 * numDataCodewords(ver)
	for i := 0; i < 4 && len(bits) < capBits; i++ {
		bits = append(bits, false)
	}
	for len(bits)%8 != 0 {
		bits = append(bits, false)
	}
	for pad := 0xEC; len(bits) < capBits; pad ^= 0xEC ^ 0x11 {
		put(pad, 8)
	}
	data := make([]byte, len(bits)/8)
	for i, b := range bits {
		if b {
			data[i>>3] |= 1 << (7 - uint(i&7))
		}
	}
	c := &Code{Size: ver*4 + 17, Version: ver}
	fn := c.drawFunctionPatterns()
	c.drawCodewords(addECCAndInterleave(data, ver), fn)
	best, bestPen := 0, -1
	for m := 0; m < 8; m++ {
		c.applyMask(m, fn)
		c.drawFormatBits(m, fn)
		if p := c.penalty(); bestPen < 0 || p < bestPen {
			best, bestPen = m, p
		}
		c.applyMask(m, fn) // undo
	}
	c.applyMask(best, fn)
	c.drawFormatBits(best, fn)
	return c, nil
}

// ---- function patterns ----

func (c *Code) set(fn [][]bool, x, y int, dark bool) {
	c.modules[y][x] = dark
	fn[y][x] = true
}

func (c *Code) drawFunctionPatterns() [][]bool {
	n := c.Size
	c.modules = make([][]bool, n)
	fn := make([][]bool, n)
	for i := range c.modules {
		c.modules[i] = make([]bool, n)
		fn[i] = make([]bool, n)
	}
	for i := 0; i < n; i++ { // timing
		c.set(fn, 6, i, i%2 == 0)
		c.set(fn, i, 6, i%2 == 0)
	}
	finder := func(x, y int) {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				d := max(abs(dx), abs(dy))
				if xx, yy := x+dx, y+dy; xx >= 0 && xx < n && yy >= 0 && yy < n {
					c.set(fn, xx, yy, d != 2 && d != 4)
				}
			}
		}
	}
	finder(3, 3)
	finder(n-4, 3)
	finder(3, n-4)
	pos := alignmentPositions(c.Version, n)
	last := len(pos) - 1
	for i, py := range pos {
		for j, px := range pos {
			if (i == 0 && j == 0) || (i == 0 && j == last) || (i == last && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					c.set(fn, px+dx, py+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	c.drawFormatBits(0, fn) // reserves the area; drawn again with the mask
	if c.Version >= 7 {
		rem := c.Version
		for i := 0; i < 12; i++ {
			rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
		}
		bits := c.Version<<12 | rem
		for i := 0; i < 18; i++ {
			bit := (bits>>i)&1 == 1
			a, b := n-11+i%3, i/3
			c.set(fn, a, b, bit)
			c.set(fn, b, a, bit)
		}
	}
	return fn
}

func alignmentPositions(ver, size int) []int {
	if ver == 1 {
		return nil
	}
	na := ver/7 + 2
	step := (ver*8 + na*3 + 5) / (na*4 - 4) * 2
	out := make([]int, na)
	out[0] = 6
	for i, p := na-1, size-7; i >= 1; i, p = i-1, p-step {
		out[i] = p
	}
	return out
}

// drawFormatBits draws level M (00) and the mask, twice.
func (c *Code) drawFormatBits(mask int, fn [][]bool) {
	data := 0<<3 | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := (data<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return (bits>>i)&1 == 1 }
	n := c.Size
	for i := 0; i <= 5; i++ {
		c.set(fn, 8, i, bit(i))
	}
	c.set(fn, 8, 7, bit(6))
	c.set(fn, 8, 8, bit(7))
	c.set(fn, 7, 8, bit(8))
	for i := 9; i < 15; i++ {
		c.set(fn, 14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		c.set(fn, n-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		c.set(fn, 8, n-15+i, bit(i))
	}
	c.set(fn, 8, n-8, true) // the dark module
}

// ---- data ----

func addECCAndInterleave(data []byte, ver int) []byte {
	nb, ecLen := numBlocks[ver], eccPerBlock[ver]
	raw := numRawDataModules(ver) / 8
	numShort := nb - raw%nb
	shortLen := raw / nb
	div := rsDivisor(ecLen)
	var blocks [][]byte
	k := 0
	for i := 0; i < nb; i++ {
		l := shortLen - ecLen
		if i >= numShort {
			l++
		}
		dat := append([]byte(nil), data[k:k+l]...)
		k += l
		ecc := rsRemainder(dat, div)
		if i < numShort {
			dat = append(dat, 0) // placeholder so every block has the same length
		}
		blocks = append(blocks, append(dat, ecc...))
	}
	var out []byte
	for i := range blocks[0] {
		for j, b := range blocks {
			if i != shortLen-ecLen || j >= numShort {
				out = append(out, b[i])
			}
		}
	}
	return out
}

func gfMul(x, y byte) byte {
	var z int
	for i := 7; i >= 0; i-- {
		z = (z << 1) ^ ((z >> 7) * 0x11D)
		z ^= int((y>>uint(i))&1) * int(x)
	}
	return byte(z)
}

func rsDivisor(degree int) []byte {
	r := make([]byte, degree)
	r[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := range r {
			r[j] = gfMul(r[j], root)
			if j+1 < len(r) {
				r[j] ^= r[j+1]
			}
		}
		root = gfMul(root, 0x02)
	}
	return r
}

func rsRemainder(data, div []byte) []byte {
	r := make([]byte, len(div))
	for _, b := range data {
		f := b ^ r[0]
		copy(r, r[1:])
		r[len(r)-1] = 0
		for i := range r {
			r[i] ^= gfMul(div[i], f)
		}
	}
	return r
}

func (c *Code) drawCodewords(data []byte, fn [][]bool) {
	n, i := c.Size, 0
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < n; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = n - 1 - vert
				}
				if !fn[y][x] && i < len(data)*8 {
					c.modules[y][x] = (data[i>>3]>>(7-uint(i&7)))&1 == 1
					i++
				}
			}
		}
	}
}

func (c *Code) applyMask(m int, fn [][]bool) {
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			var inv bool
			switch m {
			case 0:
				inv = (x+y)%2 == 0
			case 1:
				inv = y%2 == 0
			case 2:
				inv = x%3 == 0
			case 3:
				inv = (x+y)%3 == 0
			case 4:
				inv = (x/3+y/2)%2 == 0
			case 5:
				inv = x*y%2+x*y%3 == 0
			case 6:
				inv = (x*y%2+x*y%3)%2 == 0
			case 7:
				inv = ((x+y)%2+x*y%3)%2 == 0
			}
			if inv && !fn[y][x] {
				c.modules[y][x] = !c.modules[y][x]
			}
		}
	}
}

// penalty scores a masked symbol by the standard's four rules.
func (c *Code) penalty() int {
	n, p := c.Size, 0
	line := func(get func(i int) bool) {
		run := 1
		for i := 1; i <= n; i++ {
			if i < n && get(i) == get(i-1) {
				run++
				continue
			}
			if run >= 5 {
				p += 3 + run - 5
			}
			run = 1
		}
		// finder-like 1:1:3:1:1 with four light modules on a side
		pat := []bool{true, false, true, true, true, false, true}
		for i := 0; i+7 <= n; i++ {
			ok := true
			for k, d := range pat {
				ok = ok && get(i+k) == d
			}
			if !ok {
				continue
			}
			light := func(from, to int) bool {
				for k := from; k < to; k++ {
					if k >= 0 && k < n && get(k) {
						return false
					}
				}
				return true
			}
			if light(i-4, i) || light(i+7, i+11) {
				p += 40
			}
		}
	}
	for y := 0; y < n; y++ {
		line(func(i int) bool { return c.modules[y][i] })
	}
	for x := 0; x < n; x++ {
		line(func(i int) bool { return c.modules[i][x] })
	}
	dark := 0
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if c.modules[y][x] {
				dark++
			}
			if x+1 < n && y+1 < n {
				d := c.modules[y][x]
				if d == c.modules[y][x+1] && d == c.modules[y+1][x] && d == c.modules[y+1][x+1] {
					p += 3
				}
			}
		}
	}
	total := n * n
	k := (abs(dark*20-total*10)+total-1)/total - 1
	return p + k*10
}

// PNG draws the code with a 4-module quiet zone, scale pixels a module.
func (c *Code) PNG(scale int) ([]byte, error) {
	if scale < 1 {
		scale = 1
	}
	side := (c.Size + 8) * scale
	img := image.NewPaletted(image.Rect(0, 0, side, side), color.Palette{color.White, color.Black})
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if !c.modules[y][x] {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.SetColorIndex((x+4)*scale+dx, (y+4)*scale+dy, 1)
				}
			}
		}
	}
	var b bytes.Buffer
	err := png.Encode(&b, img)
	return b.Bytes(), err
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
