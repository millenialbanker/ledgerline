package xfile

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"strings"
	"testing"

	"ledgerline"
)

func lib(t *testing.T) *Library {
	t.Helper()
	l, err := Load(ledgerline.FS, "config/formats")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLibraryLoads(t *testing.T) {
	l := lib(t)
	if len(l.Specs) < 40 {
		t.Fatalf("only %d layouts", len(l.Specs))
	}
	in, out := 0, 0
	for _, s := range l.Specs {
		if s.Direction == "in" {
			in++
		} else {
			out++
		}
		if s.Purpose == "" || s.Party == "" || s.When == "" || len(s.Names) == 0 {
			t.Errorf("%s: incomplete description", s.ID)
		}
		if s.Direction == "out" && s.Template == "" {
			t.Errorf("%s: an outgoing file needs a name template", s.ID)
		}
	}
	t.Logf("%d received, %d sent", in, out)
}

// A made-up trade file in the clearing corporation's layout (no real data).
func tradeFile(l *Library) string {
	s, _ := l.Get("ncl-trade")
	var h []string
	for _, c := range s.Columns {
		h = append(h, c.Name)
	}
	row := make([]string, len(h))
	set := func(k, v string) {
		for i, c := range s.Columns {
			if c.Name == k {
				row[i] = v
			}
		}
	}
	set("TradDt", "2026-10-16")
	set("BizDt", "2026-10-16")
	set("Sgmt", "CM")
	set("Src", "NSE")
	set("Xchg", "NSE")
	set("TckrSymb", "TESTCO")
	set("SctySrs", "EQ")
	set("ClntId", "T001")
	set("BuySellInd", "B")
	set("TradQty", "10")
	set("Pric", "101.50")
	set("UnqTradIdr", "900001")
	set("TradDtTm", "2026-10-16T10:15:00")
	return strings.Join(h, ",") + "\n" + strings.Join(row, ",")
}

func TestRecogniseReadRoundTrip(t *testing.T) {
	l := lib(t)
	data := tradeFile(l)
	name := "Trade_NCL_CM_0_CM_12345_20261016_F_0000.csv"
	s, err := l.Recognise(name, []byte(data))
	if err != nil || s.ID != "ncl-trade" {
		t.Fatalf("recognise: %v %v", s, err)
	}
	tb, err := s.Read(name, []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.Rows) != 1 || tb.Rows[0].Get("ClntId") != "T001" || tb.Rows[0].Get("Pric") != "101.50" || len(tb.Issues) != 0 {
		t.Fatalf("read %+v %v", tb.Rows, tb.Issues)
	}
	same, diff, err := s.RoundTrip(name, []byte(data))
	if err != nil || !same {
		t.Fatalf("round trip: %v %s", err, diff)
	}
	// A wrong price and a missing column are reported.
	bad := strings.Replace(data, "101.50", "1O1.50", 1)
	tb, _ = s.Read(name, []byte(bad))
	if len(tb.Issues) != 1 || tb.Issues[0].Column != "Pric" {
		t.Fatalf("issues %v", tb.Issues)
	}
	old := strings.Replace(data, "Rsvd4", "Extra", 1)
	tb, _ = s.Read(name, []byte(old))
	if len(tb.Issues) == 0 || !strings.Contains(tb.Issues[0].Msg, "Rsvd4") {
		t.Fatalf("header change not caught: %v", tb.Issues)
	}
}

func TestSharedNamesDecidedByHeader(t *testing.T) {
	l := lib(t)
	// NSE Clearing sends several <digits>.txt files; the header decides.
	for _, id := range []string{"ncl-position", "stt-ctt", "stamp-duty", "ncl-direct-payout", "nse-cm-obligation"} {
		s, _ := l.Get(id)
		var h []string
		for _, c := range s.Columns {
			h = append(h, c.Name)
		}
		got, err := l.Recognise("20261016.txt", []byte(strings.Join(h, ",")+"\r\n"))
		if err != nil || got.ID != id {
			t.Errorf("%s recognised as %v (%v)", id, got, err)
		}
	}
	if _, err := l.Recognise("notes.txt", []byte("hello,world\n")); err == nil {
		t.Error("unknown file recognised")
	}
}

func TestRecordsVaR(t *testing.T) {
	l := lib(t)
	data := "10,16102026,1,2\r\n20,TESTCO,EQ,INE000X01010,12.50,0.00,12.50,3.50,0.00,16.00\r\n20,OTHER,BE,INE000Y01011,25.00,0.00,25.00,3.50,5.00,33.50\r\n"
	s, err := l.Recognise("C_VAR1_16102026_1.DAT", []byte(data))
	if err != nil || s.ID != "ncl-var" {
		t.Fatalf("%v %v", s, err)
	}
	tb, _ := s.Read("x", []byte(data))
	if len(tb.Rows) != 3 || tb.Rows[0].Record != "10" || tb.Rows[2].Get("ApplicableMarginRate") != "33.50" || len(tb.Issues) != 0 {
		t.Fatalf("%+v %v", tb.Rows, tb.Issues)
	}
	if same, diff, _ := s.RoundTrip("x", []byte(data)); !same {
		t.Fatal(diff)
	}
}

func TestWriteAndPack(t *testing.T) {
	l := lib(t)
	s, _ := l.Get("ncl-allocation")
	out, err := s.Write([]Row{{Values: map[string]string{"curDate": "2026-10-16", "segment": "CM", "cmCode": "12345", "tmCode": "12345", "cliCode": "T001", "accType": "C", "amt": "2500", "action": "A"}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "16-OCT-2026,CM,12345,12345,,T001,C,2500.00,,,,,,,A\r\n" {
		t.Fatalf("%q", out)
	}
	name, err := s.FileName(Vars{"member": "12345", "date": "2026-10-16", "seq": "1"})
	if err != nil || name != "12345_ALLOC_16102026.T0001" {
		t.Fatalf("%s %v", name, err)
	}
	if _, err := s.FileName(Vars{"date": "2026-10-16"}); err == nil {
		t.Error("missing member accepted")
	}
	g, _ := l.Get("ncl-client-collateral")
	n, b, err := g.Pack("C_CC01_12345_16102026.csv", []byte("T001,1.00,0.00,0.00,1.00,1.00\r\n"))
	if err != nil || n != "C_CC01_12345_16102026.csv.gz" {
		t.Fatal(n, err)
	}
	files, err := Unpack(n, b)
	if err != nil || len(files) != 1 || files[0].Name != "C_CC01_12345_16102026.csv" {
		t.Fatalf("%v %v", files, err)
	}
	// Writing refuses a value that is not a number.
	if _, err := s.Write([]Row{{Values: map[string]string{"amt": "lots"}}}); err == nil {
		t.Error("bad amount written")
	}
}

func TestUnpackNested(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Name = "inner.csv"
	zw.Write([]byte("a,b\n"))
	zw.Close()
	var z bytes.Buffer
	w := zip.NewWriter(&z)
	f, _ := w.Create("inner.csv.gz")
	f.Write(gz.Bytes())
	w.Close()
	files, err := Unpack("x.zip", z.Bytes())
	if err != nil || len(files) != 1 || files[0].Name != "inner.csv" || string(files[0].Data) != "a,b\n" {
		t.Fatalf("%v %v", files, err)
	}
}

// TestSamples round-trips real files when LEDGERLINE_XFILE_SAMPLES points
// at a folder of them (run only on the machine that holds them).
func TestSamples(t *testing.T) {
	dir := os.Getenv("LEDGERLINE_XFILE_SAMPLES")
	if dir == "" {
		t.Skip("set LEDGERLINE_XFILE_SAMPLES to check real files")
	}
	l := lib(t)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		b, _ := os.ReadFile(dir + "/" + e.Name())
		files, err := Unpack(e.Name(), b)
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		for _, f := range files {
			s, err := l.Recognise(f.Name, f.Data)
			if err != nil {
				t.Logf("not recognised: %v", err)
				continue
			}
			same, diff, err := s.RoundTrip(f.Name, f.Data)
			issues, first := 0, ""
			if tb, e := s.Read(f.Name, f.Data); e == nil {
				issues = len(tb.Issues)
				if issues > 0 {
					first = fmt.Sprintf("line %d %s: %s", tb.Issues[0].Line, tb.Issues[0].Column, Mask(tb.Issues[0].Msg))
				}
			}
			t.Logf("%-32s same=%v issues=%d %s %s %v", s.ID, same, issues, first, diff, err)
			if !same && os.Getenv("LEDGERLINE_XFILE_DEBUG") == s.ID {
				tb, _ := s.Read(f.Name, f.Data)
				for i, r := range tb.Rows[:min(4, len(tb.Rows))] {
					t.Logf("row %d line %d rec %q fields %d", i, r.Line, r.Record, len(r.Values))
				}
				for i, l := range strings.SplitN(string(f.Data), "\n", 5)[:4] {
					t.Logf("file %d: %s", i+1, Mask(l))
				}
			}
		}
	}
}
