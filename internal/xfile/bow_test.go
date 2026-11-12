package xfile

import (
	"strings"
	"testing"
)

// The two BOW (BOLTPlusOnWeb) upload files, in the shapes of the broker's
// real files (made-up clients and figures): no header, CRLF, 20 and 16
// comma-separated fields. They must be recognised by name, read with no
// issues and written back byte for byte.
func TestBOWUploadLayouts(t *testing.T) {
	l := lib(t)
	cases := []struct {
		id, name string
		fields   int
		lines    []string
	}{
		{"bow-fund", "BOW_FUND_09-11-26 08_13_39 AM.csv", 20, []string{
			"A101,,,,0,0,0,0,,1,0,,,,,1,1,0,1,Y",
			"A1022,,,,125000,0,0,0,,1,0,,,,,1,1,0,1,Y",
			"Z9999,,,,-4310,0,0,0,,1,0,,,,,1,1,0,1,Y",
		}},
		{"bow-dp-holding", "BOW_DP_Holding_Combined_Upload_09112026.TXT", 16, []string{
			"A101,TESTSYM,EQ,500001,25,,1043.25,,,0,0.00,,0.00,,,0",
			"A101,TESTSME,SM,,3000,,18.40,,,0,0.00,,0.00,,,0",
			"A1022,,,599999,10,,0.00,,,0,0.00,,0.00,,,0",
			"A1022,TESTBZ,BZ,500002,0,,0.00,,,0,100.00,,512.70,,,0",
			"Z9999,TESTGB,GB,800001,1,,6150.00,,,0,0.00,,0.00,,,0",
		}},
	}
	for _, c := range cases {
		s, ok := l.Get(c.id)
		if !ok {
			t.Fatalf("%s missing", c.id)
		}
		if !s.Confirmed || len(s.Columns) != c.fields || s.Header || !s.CRLF || s.Delimiter != "," || s.TitleLine != "" || !strings.Contains(s.Source, "real BOW upload files") {
			t.Fatalf("%s: confirmed=%v columns=%d header=%v crlf=%v", c.id, s.Confirmed, len(s.Columns), s.Header, s.CRLF)
		}
		data := []byte(strings.Join(c.lines, "\r\n") + "\r\n")
		got, err := l.Recognise(c.name, data)
		if err != nil || got.ID != c.id {
			t.Fatalf("%s recognised as %v: %v", c.name, got, err)
		}
		tb, err := s.Read(c.name, data)
		if err != nil || len(tb.Issues) != 0 || len(tb.Rows) != len(c.lines) {
			t.Fatalf("%s: %v rows %d issues %+v", c.id, err, len(tb.Rows), tb.Issues)
		}
		if same, diff, err := s.RoundTrip(c.name, data); err != nil || !same {
			t.Fatalf("%s round trip: %s %v", c.id, diff, err)
		}
	}
	f, _ := l.Get("bow-fund")
	tb, _ := f.Read("x.csv", []byte("Z9999,,,,-4310,0,0,0,,1,0,,,,,1,1,0,1,Y\r\n"))
	if r := tb.Rows[0]; r.Get("ClientCode") != "Z9999" || r.Get("CashDeposit") != "-4310" {
		t.Fatalf("funds row %v", r.Values)
	}
	// A constant that is not the observed one is an issue.
	if tb, _ := f.Read("x.csv", []byte("Z9999,,,,10,0,0,0,,1,0,,,,,1,1,0,1,N\r\n")); len(tb.Issues) != 1 {
		t.Fatalf("issues %+v", tb.Issues)
	}
	fn, err := f.FileName(Vars{"date": "2026-11-09", "time": "08_13_39 AM"})
	if err != nil || fn != "BOW_FUND_09-11-26 08_13_39 AM.csv" || !f.MatchesName(fn) {
		t.Fatalf("funds file name %q %v", fn, err)
	}
	h, _ := l.Get("bow-dp-holding")
	tb, _ = h.Read("x.TXT", []byte("A1022,TESTBZ,BZ,500002,0,,0.00,,,0,100.00,,512.70,,,0\r\n"))
	if r := tb.Rows[0]; r.Get("Symbol") != "TESTBZ" || r.Get("Series") != "BZ" || r.Get("BSECode") != "500002" || r.Get("PoolQty") != "100.00" || r.Get("PoolAvgPrice") != "512.70" || r.Get("DPCFSHaircutPct") != "0" || r.Get("PoolCFSHaircutPct") != "0" {
		t.Fatalf("holding row %v", r.Values)
	}
	fn, err = h.FileName(Vars{"date": "2026-11-09"})
	if err != nil || fn != "BOW_DP_Holding_Combined_Upload_09112026.TXT" || !h.MatchesName(fn) {
		t.Fatalf("holding file name %q %v", fn, err)
	}
}
