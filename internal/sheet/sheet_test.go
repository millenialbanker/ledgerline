package sheet

import (
	"os"
	"strings"
	"testing"
)

func check(t *testing.T, ss []Sheet) {
	t.Helper()
	if len(ss) != 2 || ss[0].Name != "Sheet2" || ss[1].Name != "Sheet1" {
		t.Fatalf("sheets %d", len(ss))
	}
	s := ss[0]
	want := map[[2]int]string{{1, 1}: "Fictional Clearing House Ltd", {8, 0}: "T-BILL", {9, 5}: "Price * (Rs.)", {10, 1}: "IN002025X123", {10, 4}: "46331", {10, 5}: "99.1234",
		{11, 5}: "97.5", {14, 2}: "7.10% GS 2034 — Ünïcode", {14, 3}: "7.1", {15, 0}: strings.Repeat("x", 300)}
	for k, v := range want {
		if got := s.Cell(k[0], k[1]); got != v {
			t.Errorf("cell %v = %q, want %q", k, got, v)
		}
	}
	if f := s.Cell(16, 0); f != "49001" && f != "" { // formula result (xlsx written by a library has none cached)
		t.Errorf("formula %q", f)
	}
	if d, ok := Date(s.Cell(10, 4)); !ok || d != "2026-11-05" {
		t.Errorf("date %s", d)
	}
	if !strings.HasPrefix(s.Cell(418, 1), "filler string number 399") {
		t.Errorf("last filler %q", s.Cell(418, 1))
	}
}

func TestReadXLS(t *testing.T) {
	b, _ := os.ReadFile("testdata/prices.xls")
	ss, err := Read(b)
	if err != nil {
		t.Fatal(err)
	}
	check(t, ss)
	if ss[0].Cell(16, 0) != "49001" {
		t.Errorf("formula result %q", ss[0].Cell(16, 0))
	}
}

func TestReadXLSX(t *testing.T) {
	b, _ := os.ReadFile("testdata/prices.xlsx")
	ss, err := Read(b)
	if err != nil {
		t.Fatal(err)
	}
	check(t, ss)
}

func TestWriteXLSX(t *testing.T) {
	in := []Sheet{{Name: "Particulars", Rows: [][]string{{"Particulars", "Amount"}, {"Total credit", "1234.50"}, {"UCC", "007"}}}, {Name: "Two"}}
	b, err := WriteXLSX(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Read(b)
	if err != nil || len(out) != 2 || out[0].Cell(1, 1) != "1234.5" || out[0].Cell(2, 1) != "007" || out[0].Name != "Particulars" {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err := Read([]byte("not a workbook")); err == nil {
		t.Fatal("garbage read")
	}
}
