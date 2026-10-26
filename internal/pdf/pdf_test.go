package pdf

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sample() Report {
	var rows [][]string
	for i := 0; i < 140; i++ {
		rows = append(rows, []string{"2026-10-16", "RELIANCE EQ", "Buy", "10", "2,834.50", "₹28,345.00"})
	}
	return Report{Firm: []string{"Bhairav Securities Pvt Ltd (sample)", "SEBI INZ000XXXXXX · NSE 12345 · BSE 6789"}, Title: "Contract note", Meta: []string{"CN/2026-27/000123", "Trade date 16-Oct-2026"},
		Party: []string{"Asha Rao (A1001)", "PAN XXXXX1234F · Mumbai"}, Summary: [][2]string{{"Net payable", "₹1,23,456.78"}, {"Trades", "140"}},
		Sections: []Table{{Title: "Trades", Cols: []Col{{Label: "Date"}, {Label: "Security", Weight: 2.5}, {Label: "B/S"}, {Label: "Qty", Right: true}, {Label: "Price", Right: true}, {Label: "Value", Right: true, Weight: 1.4}}, Rows: rows, Totals: []string{"Total", "", "", "1400", "", "₹39,68,300.00"}}},
		Notes: []string{"This contract note is issued subject to the rules, bye-laws and regulations of the exchanges and SEBI."}, Footer: "Ledgerline", Stamp: []string{"Digitally signed by BHAIRAV SECURITIES", "16-Oct-2026 21:04 IST"}}
}

func TestRenderOpensInPoppler(t *testing.T) {
	d := Render(sample())
	if d.Pages() < 3 {
		t.Fatalf("expected the long table to run over pages, got %d", d.Pages())
	}
	b, err := d.Bytes(Meta{Title: "Contract note", Created: time.Now()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("%PDF-1.7")) {
		t.Fatal("not a PDF")
	}
	if _, err := exec.LookPath("pdftotext"); err == nil {
		dir := t.TempDir()
		f := filepath.Join(dir, "a.pdf")
		os.WriteFile(f, b, 0o600)
		out, err := exec.Command("pdftotext", "-layout", f, "-").CombinedOutput()
		if err != nil {
			t.Fatalf("pdftotext: %v %s", err, out)
		}
		s := string(out)
		for _, want := range []string{"Contract note", "CN/2026-27/000123", "Rs.1,23,456.78", "Page 3 of", "RELIANCE EQ"} {
			if !strings.Contains(s, want) {
				t.Fatalf("missing %q in %s", want, s[:400])
			}
		}
	}
	if _, err := exec.LookPath("qpdf"); err == nil {
		dir := t.TempDir()
		f := filepath.Join(dir, "a.pdf")
		os.WriteFile(f, b, 0o600)
		if out, err := exec.Command("qpdf", "--check", f).CombinedOutput(); err != nil {
			t.Fatalf("qpdf --check: %v %s", err, out)
		}
	}
}

func TestWidthAndWrap(t *testing.T) {
	if w := Width("Hello", 10, false); w < 22 || w > 24 {
		t.Fatalf("width %v", w)
	}
	lines := Wrap("the quick brown fox jumps over the lazy dog", 10, false, 60)
	if len(lines) < 3 {
		t.Fatalf("wrap %v", lines)
	}
	if f := Fit("a very long security name indeed", 8, false, 50); !strings.HasSuffix(f, "...") || Width(f, 8, false) > 50 {
		t.Fatalf("fit %q", f)
	}
}
