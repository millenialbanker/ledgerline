package qr

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

var sample = strings.Repeat("upi://pay?pa=abc.brk@validhdfc&pn=TEST&cu=INR", 10)

// Version choice and size follow the level M capacities.
func TestVersions(t *testing.T) {
	for n, ver := range map[int]int{1: 1, 14: 1, 15: 2, 26: 2, 42: 3, 62: 4, 84: 5, 106: 6, 122: 7, 152: 8, 180: 9, 213: 10} {
		c, err := Encode([]byte(sample[:n]))
		if err != nil || c.Version != ver || c.Size != ver*4+17 {
			t.Errorf("%d bytes: version %d size %d %v, want version %d", n, c.Version, c.Size, err, ver)
		}
	}
	if _, err := Encode([]byte(strings.Repeat("x", 214))); err != ErrTooLong {
		t.Errorf("214 bytes: %v", err)
	}
	c, _ := Encode([]byte("x"))
	// the finder pattern's corners and centre, and the dark module
	if !c.Dark(0, 0) || !c.Dark(6, 6) || !c.Dark(3, 3) || c.Dark(7, 7) || !c.Dark(8, c.Size-8) {
		t.Error("function patterns")
	}
	if b, err := c.PNG(4); err != nil || len(b) < 50 || string(b[1:4]) != "PNG" {
		t.Errorf("png %v", err)
	}
}

// With QR_OUT set, writes codes of every version for an outside decoder
// to read back (used once with OpenCV while writing the package).
func TestGenForDecoder(t *testing.T) {
	dir := os.Getenv("QR_OUT")
	if dir == "" {
		t.Skip("set QR_OUT to write sample codes")
	}
	for _, n := range []int{1, 10, 14, 20, 26, 40, 60, 62, 84, 100, 106, 120, 140, 154, 180, 200, 213} {
		s := sample[:n]
		c, err := Encode([]byte(s))
		if err != nil {
			t.Fatal(n, err)
		}
		b, _ := c.PNG(6)
		os.WriteFile(fmt.Sprintf("%s/%03d_v%d.png", dir, n, c.Version), b, 0o644)
		os.WriteFile(fmt.Sprintf("%s/%03d_v%d.txt", dir, n, c.Version), []byte(s), 0o644)
	}
}
