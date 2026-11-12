package xfile

import (
	"strings"
	"testing"
)

// The two BOW (BOLTPlusOnWeb) upload files the broker sends, in the shapes
// of the broker's real files (made-up clients and figures): no header,
// CRLF, 20 (DEPOSIT_UPLOAD without Product) and 16 comma-separated fields,
// and the deposit file's 21-field lines of BOW's sheet. They must be
// recognised by name, read with no issues and written back byte for byte.
func TestBOWUploadLayouts(t *testing.T) {
	l := lib(t)
	cases := []struct {
		id, name string
		fields   int
		lines    []string
	}{
		{"bow-fund", "BOW_FUND_09-11-26 08_13_39 AM.csv", 21, []string{
			"A101,,,,0,0,0,0,,1,0,,,,,1,1,0,1,Y",
			"A1022,,,,125000,0,0,0,,1,0,,,,,1,1,0,1,Y",
			"Z9999,,,,-4310,0,0,0,,1,0,,,,,1,1,0,1,Y",
			"A1022,,,,90000,0,0,0,securities,1,0,,,,,1,1,0,1,Y",
			"A1022,,,,35000,0,0,0,commodities,1,0,,,,,1,1,0,1,Y",
			"Z9999,,BSE,EQ,5000,0,0,0,securities,1,0,10,10,,,6,1,90,6,Y,Delivery",
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
		if !s.Confirmed || len(s.Columns) != c.fields || s.Header || !s.CRLF || s.Delimiter != "," || s.TitleLine != "" || !strings.Contains(s.Source, "real BOW upload files") || !strings.Contains(s.Source, "BOW File_Formats.xlsx") {
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
	if r := tb.Rows[0]; r.Get("ClientID") != "Z9999" || r.Get("TotalDeposit") != "-4310" || r.Get("Factor") != "1" || r.Get("AddIntoEffective") != "1" || r.Get("MTMFactor") != "1" || r.Get("EffectiveAmount") != "0" || r.Get("AddIntoMTM") != "1" || r.Get("AddEntry") != "Y" || r.Get("Product") != "" || r.Fields != 20 {
		t.Fatalf("funds row %v", r.Values)
	}
	// A filler that is not 0 is an issue; so is a line of 19 fields.
	if tb, _ := f.Read("x.csv", []byte("Z9999,,,,10,5,0,0,,1,0,,,,,1,1,0,1,Y\r\n")); len(tb.Issues) != 1 {
		t.Fatalf("issues %+v", tb.Issues)
	}
	if tb, _ := f.Read("x.csv", []byte("Z9999,,,,10,0,0,0,,1,0,,,,,1,1,0,1\r\n")); len(tb.Issues) == 0 {
		t.Fatal("19 fields read")
	}
	fn, err := f.FileName(Vars{"date": "2026-11-09", "time": "08_13_39 AM"})
	if err != nil || fn != "BOW_FUND_09-11-26 08_13_39 AM.csv" || !f.MatchesName(fn) {
		t.Fatalf("funds file name %q %v", fn, err)
	}
	h, _ := l.Get("bow-dp-holding")
	tb, _ = h.Read("x.TXT", []byte("A1022,TESTBZ,BZ,500002,0,,0.00,,,0,100.00,,512.70,,,0\r\n"))
	if r := tb.Rows[0]; r.Get("NSESymbol") != "TESTBZ" || r.Get("Series") != "BZ" || r.Get("BSEScripCode") != "500002" || r.Get("PoolQuantity") != "100.00" || r.Get("PoolAvePrice") != "512.70" || r.Get("DPCFSHaircut") != "0" || r.Get("PoolCFSHaircut") != "0" {
		t.Fatalf("holding row %v", r.Values)
	}
	fn, err = h.FileName(Vars{"date": "2026-11-09"})
	if err != nil || fn != "BOW_DP_Holding_Combined_Upload_09112026.TXT" || !h.MatchesName(fn) {
		t.Fatalf("holding file name %q %v", fn, err)
	}
}
