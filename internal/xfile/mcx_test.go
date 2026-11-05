package xfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The MCX and MCXCCL layouts: one fixture per layout built from the spec
// (made-up values), MCXCCL's own sample files (dummy data), the column
// counts of the real files a trading member receives, and an unambiguous
// recognition of every real file name.

// synth makes one line of a record layout with made-up values of each type.
func synth(s *Spec, r *Record) []string {
	day := time.Date(2026, 11, 4, 10, 30, 0, 0, time.UTC)
	f := make([]string, len(r.Columns))
	for i, c := range r.Columns {
		switch {
		case c.Const != "":
			f[i] = c.Const
		case c.Type == "int":
			f[i] = "7"
		case c.Type == "dec":
			f[i] = "12.50"
		case c.Type == "date" || c.Type == "datetime" || c.Type == "time":
			f[i] = strings.ToUpper(day.Format(c.Layout))
		case c.Type == "blank":
			f[i] = ""
		default:
			f[i] = "TEST" + string(rune('A'+i%26))
		}
	}
	return f
}

// synthFile is a whole file: the header (if any) and one line of every record layout.
func synthFile(t *testing.T, s *Spec) string {
	t.Helper()
	var lines []string
	if s.Header {
		var h []string
		for _, c := range s.detail().Columns {
			h = append(h, c.Name)
		}
		lines = append(lines, strings.Join(h, s.Delimiter))
	}
	for _, r := range s.allRecords() {
		lines = append(lines, strings.Join(synth(s, r), s.Delimiter))
	}
	return strings.Join(lines, "\n") + "\n"
}

// The real file names a trading member (made-up code 56845) receives, the
// layout each must be recognised as, and the field count of its first line
// in the real files.
var mcxReal = []struct {
	name, id string
	fields   int
}{
	{"MCX_NOBL_56845_20261104.csv", "mcx-obligation-tm", 13},
	{"MCX_PNOBL_56845_20261104.csv", "mcx-obligation-tm", 13},
	{"MCX_CD_56845_20261104.csv", "mcx-collateral", 11},            // 10 fields and a trailing comma
	{"MCX_CD_56845_UCC_20261104.csv", "mcx-collateral-client", 13}, // 12 fields and a trailing comma
	{"MCX_MARGIN_56845_20261104.csv", "mcx-client-margin", 19},
	{"MCX_MRG_56845_20261104.csv", "mcx-margin-tm", 20},
	{"MCX_PeakMargin56845_20261104_03.csv", "mcx-peak-margin-tm", 12},
	{"MCX_NCMR_56845_20261104.csv", "mcx-ncmr", 31},
	{"MCX_WebAllocationDeallocation56845_20261104.csv", "mcx-web-allocation", 19}, // 18 fields and a trailing comma
	{"MCX_WebAllocationDeallcoation56845_20261104.csv", "mcx-web-allocation", 19},
	{"MCX_CP MASTER_20261104.csv", "mcx-cp-master", 6},
	{"MCX_ProductMaster.csv", "mcx-product-master", 68},
	{"MCX_04112026ORD_1.csv", "mcx-orders", 43},
	{"MCX_04112026ORD.txt", "mcx-orders", 43},
	{"DailyIndexFile04112026.csv", "mcx-daily-index", 7},
	{"MCX_DEBIT-CREDIT NOTE_56845_20261104.csv", "mcx-dc-note", 18},
	{"MCX_EODSAR_56845_20261104.csv", "mcx-eod-short-allocation", 9},
	{"MCX_INTRASAR_56845_20261104.csv", "mcx-intraday-short-allocation", 16},
	{"Margin_MCXCCL_CO_0_TM_56845_20261104_F_0000.csv", "mcxccl-margin", 52},
	{"Position_MCXCCL_CO_0_TM_56845_20261104_F_0000.csv", "mcxccl-position", 46},
	{"Penalty_MCXCCL_CO_0_TM_56845_20261104_F_0000.csv", "mcxccl-penalty", 17},
	{"CTT_MCXCCL_CO_0_TM_56845_20261104_F_0000.csv", "mcxccl-ctt", 49},
	{"Stamp_MCXCCL_CO_0_TM_56845_20261104_F_0000.csv", "mcxccl-stamp", 38},
	{"Trade_MCXCCL_CO_0_TM_56845_20261104_F_0000.csv", "mcx-trade", 46},
	{"Bhavcopy_MCX_CO_0_0_0_20261104_F_0000.csv", "mcx-bhavcopy", 34},
	{"bhavcopy_mcxccl_co_0_0_0_20261104_f_0000.csv", "mcx-bhavcopy", 34},
}

// widen gives a file's first line the real field count (trailing commas).
func widen(data string, n int) string {
	first, rest, _ := strings.Cut(data, "\n")
	for strings.Count(first, ",")+1 < n {
		first += ","
	}
	return first + "\n" + rest
}

func TestMCXRealNamesAndCounts(t *testing.T) {
	l := lib(t)
	for _, x := range mcxReal {
		s, ok := l.Get(x.id)
		if !ok {
			t.Fatalf("%s: no layout %s", x.name, x.id)
		}
		data := widen(synthFile(t, s), x.fields)
		if s.Header {
			// the header has the real count; the data lines too
			first, _, _ := strings.Cut(data, "\n")
			if n := len(splitLine(s, first)); n != x.fields {
				t.Errorf("%s: header has %d fields, real files %d", x.id, n, x.fields)
			}
		} else {
			first, _, _ := strings.Cut(data, "\n")
			if n := len(splitLine(s, first)); n != x.fields {
				t.Errorf("%s: first line has %d fields, real files %d", x.id, n, x.fields)
			}
		}
		got, err := l.Recognise(x.name, []byte(data))
		if err != nil || got.ID != x.id {
			t.Errorf("%s recognised as %v (%v), want %s", x.name, got, err, x.id)
			continue
		}
		// Only this layout claims the name, or its content decides.
		var claim []string
		for _, o := range l.Specs {
			if o.MatchesName(x.name) {
				claim = append(claim, o.ID)
			}
		}
		if len(claim) > 1 && !strings.HasPrefix(x.id, "mcx-obligation") && !strings.HasPrefix(x.id, "mcx-margin-") && !strings.HasPrefix(x.id, "mcx-peak-margin") {
			t.Errorf("%s is claimed by %v", x.name, claim)
		}
		tb, err := got.Read(x.name, []byte(data))
		if err != nil || len(tb.Issues) != 0 || len(tb.Rows) == 0 {
			t.Errorf("%s: read %v, issues %v", x.id, err, tb.Issues)
			continue
		}
		if same, diff, err := got.RoundTrip(x.name, []byte(data)); err != nil || !same {
			t.Errorf("%s: round trip %v %s", x.id, err, diff)
		}
	}
}

// Every new layout reads its own synthetic fixture cleanly and writes it
// back byte for byte; its clearing member twin (same name) does not.
func TestMCXFixtures(t *testing.T) {
	l := lib(t)
	n := 0
	for _, s := range l.Specs {
		if !strings.HasPrefix(s.ID, "mcx") || s.Direction != "in" || s.Format != "delimited" {
			continue
		}
		n++
		if !s.Confirmed || !strings.Contains(s.Source, "MCX") {
			t.Errorf("%s: not confirmed from a circular (%q)", s.ID, s.Source)
		}
		data := synthFile(t, s)
		tb, err := s.Read("x", []byte(data))
		if err != nil || len(tb.Issues) != 0 {
			t.Errorf("%s: %v %v", s.ID, err, tb.Issues)
		}
		if same, diff, err := s.RoundTrip("x", []byte(data)); err != nil || !same {
			t.Errorf("%s: round trip %v %s", s.ID, err, diff)
		}
	}
	if n < 25 {
		t.Fatalf("only %d MCX layouts", n)
	}
	// A clearing member's NOBL, MRG and peak margin files share the
	// trading member's names; the lines decide.
	for _, id := range []string{"mcx-obligation-cm", "mcx-margin-cm", "mcx-peak-margin-cm"} {
		s, _ := l.Get(id)
		name := map[string]string{"mcx-obligation-cm": "MCX_NOBL_1234_20261104.csv", "mcx-margin-cm": "MCX_MRG_1234_20261104.csv", "mcx-peak-margin-cm": "MCX_PeakMargin1234_20261104_01.csv"}[id]
		got, err := l.Recognise(name, []byte(synthFile(t, s)))
		if err != nil || got.ID != id {
			t.Errorf("%s recognised as %v (%v)", id, got, err)
		}
	}
}

// Record counts of the multi-record layouts, from the circulars.
func TestMCXRecordCounts(t *testing.T) {
	l := lib(t)
	want := map[string]map[string]int{
		"mcx-obligation-tm":     {"10": 13, "20": 13},
		"mcx-obligation-cm":     {"10": 14, "20": 9, "30": 15},
		"mcx-margin-tm":         {"10": 20, "30": 19, "40": 22},
		"mcx-margin-cm":         {"10": 22, "20": 16, "30": 19, "40": 24},
		"mcx-collateral":        {"10": 10, "20": 18, "30": 23, "40": 15},
		"mcx-collateral-client": {"10": 12, "20": 20, "30": 25, "40": 15},
	}
	for id, recs := range want {
		s, _ := l.Get(id)
		for _, r := range s.Records {
			if recs[r.Code] != len(r.Columns) {
				t.Errorf("%s record %s: %d fields, circular %d", id, r.Code, len(r.Columns), recs[r.Code])
			}
		}
	}
	for id, n := range map[string]int{"mcx-product-master": 68, "mcx-cp-master": 6, "mcx-peak-margin-tm": 12, "mcx-peak-margin-cm": 11, "mcx-ncmr": 31, "mcx-web-allocation": 18,
		"mcx-orders": 43, "mcx-daily-index": 7, "mcx-dc-note": 18, "mcx-client-margin": 19, "mcxccl-margin": 52, "mcxccl-position": 46, "mcxccl-penalty": 17,
		"mcxccl-ctt": 49, "mcxccl-stamp": 38, "mcx-bhavcopy": 34, "mcx-trade": 46, "mcx-eod-short-allocation": 9, "mcx-intraday-short-allocation": 16} {
		s, _ := l.Get(id)
		if len(s.Columns) != n {
			t.Errorf("%s: %d columns, circular %d", id, len(s.Columns), n)
		}
	}
	if _, ok := l.Get("mcx-margin-other"); ok {
		t.Error("the generic mcx-margin-other layout is retired")
	}
}

// MCXCCL's own sample files (dummy data): each is recognised as its MCXCCL
// layout (not NSE Clearing's, whose headers are the same), reads with no
// issue and round-trips.
func TestMCXCCLSamples(t *testing.T) {
	l := lib(t)
	ents, err := os.ReadDir("testdata/mcxccl")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"CTT": "mcxccl-ctt", "Margin": "mcxccl-margin", "Penalty": "mcxccl-penalty", "Position": "mcxccl-position", "Stamp": "mcxccl-stamp", "Trade": "mcx-trade", "bhavcopy": "mcx-bhavcopy"}
	seen := 0
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join("testdata/mcxccl", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		id := want[strings.SplitN(e.Name(), "_", 2)[0]]
		s, err := l.Recognise(e.Name(), b)
		if err != nil || s.ID != id {
			t.Errorf("%s recognised as %v (%v), want %s", e.Name(), s, err, id)
			continue
		}
		tb, err := s.Read(e.Name(), b)
		if err != nil || len(tb.Issues) != 0 || len(tb.Rows) == 0 {
			t.Errorf("%s: %v %v", e.Name(), err, tb.Issues)
		}
		if same, diff, err := s.RoundTrip(e.Name(), b); err != nil || !same {
			t.Errorf("%s: round trip %v %s", e.Name(), err, diff)
		}
		seen++
	}
	if seen < 10 {
		t.Fatalf("%d samples", seen)
	}
}

// Rows of the UDiFF margin file with a different number of trailing empty
// or reserved fields are read as the layout and written back as they came;
// a value beyond the layout is still an issue.
func TestTrailingReservedFields(t *testing.T) {
	l := lib(t)
	s, _ := l.Get("mcxccl-margin")
	b, err := os.ReadFile("testdata/mcxccl/Margin_MCXCCL_CO_0_TM_88888_20231106_F_0000.csv")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), "\n")
	lines[1] = strings.TrimSuffix(lines[1], ",,,,") // 48 fields: the four dummy fields missing
	lines[2] += ","                                 // 53 fields: a trailing comma
	data := strings.Join(lines, "\n") + "\n"
	tb, _ := s.Read("x", []byte(data))
	if len(tb.Issues) != 0 || tb.Rows[0].Fields != 48 || tb.Rows[1].Fields != 53 {
		t.Fatalf("%v %d %d", tb.Issues, tb.Rows[0].Fields, tb.Rows[1].Fields)
	}
	if same, diff, _ := s.RoundTrip("x", []byte(data)); !same {
		t.Fatal(diff)
	}
	lines[2] += "X"
	tb, _ = s.Read("x", []byte(strings.Join(lines, "\n")+"\n"))
	if len(tb.Issues) != 1 || !strings.Contains(tb.Issues[0].Msg, "53 fields") {
		t.Fatalf("%v", tb.Issues)
	}
	// Remarks is not reserved: a line stopping before it is short.
	lines[2] = strings.Join(strings.Split(lines[1], ",")[:47], ",")
	tb, _ = s.Read("x", []byte(strings.Join(lines[:3], "\n")+"\n"))
	if len(tb.Issues) != 1 {
		t.Fatalf("%v", tb.Issues)
	}
}

func TestMaskMessage(t *testing.T) {
	got := MaskMessage(`line 12: "1O1.50 Rs" is not a number in the form 02012006`)
	if got != `line 99: "9A9.99 AA" is not a number in the form 99999999` {
		t.Fatal(got)
	}
}
