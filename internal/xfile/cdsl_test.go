package xfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CDSL's sample files of the harmonised (UDiFF) layouts, from CDSL's site
// (dummy data): each is recognised as exactly one layout and reads with no
// issue. The client master and COD samples quote fields line by line (a
// field quoted on one line is bare on the next), which a per-column layout
// cannot rewrite byte for byte; the others round-trip.
func TestCDSLSamples(t *testing.T) {
	l := lib(t)
	want := map[string]string{"CLN_MSTR": "cdsl-client-master", "COD_EXP": "cdsl-transactions", "DIS_MSTR": "cdsl-dis-master", "SOH_EXP": "cdsl-holding", "SOT_EXP": "cdsl-statement-txn"}
	cols := map[string]int{"CLN_MSTR": 186, "COD_EXP": 230, "DIS_MSTR": 20, "SOH_EXP": 44, "SOT_EXP": 42}
	files, _ := filepath.Glob("testdata/cdsl/*.csv")
	if len(files) != len(want) {
		t.Fatalf("%d sample files", len(files))
	}
	for _, p := range files {
		name := filepath.Base(p)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		kind := name[:strings.Index(name[4:], "_")+4]
		n := 0
		for _, s := range l.Specs {
			if s.MatchesName(name) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: %d layouts match the name", name, n)
		}
		s, err := l.Recognise(name, b)
		if err != nil || s.ID != want[kind] {
			t.Errorf("%s recognised as %v (%v)", name, s, err)
			continue
		}
		if len(s.Columns) != cols[kind] || !s.Confirmed {
			t.Errorf("%s: %d columns, confirmed %v", s.ID, len(s.Columns), s.Confirmed)
		}
		tb, err := s.Read(name, b)
		if err != nil || len(tb.Issues) != 0 || len(tb.Rows) == 0 {
			t.Errorf("%s: %v issues %v", name, err, tb.Issues[:min(3, len(tb.Issues))])
		}
		if kind == "CLN_MSTR" || kind == "COD_EXP" {
			continue
		}
		if same, diff, err := s.RoundTrip(name, b); err != nil || !same {
			t.Errorf("%s does not round-trip: %v %s", name, err, diff)
		}
	}
}

// The harmonised layouts follow catalogue V3.0.1.4's field counts, and the
// file names of the catalogue's Format Master are recognised.
func TestCDSLHarmonisedLayouts(t *testing.T) {
	l := lib(t)
	for id, n := range map[string]int{"cdsl-isin-master": 54, "cdsl-closing-price": 10, "cdsl-calendar": 17, "cdsl-corporate-action": 26, "cdsl-member-master": 49,
		"cdsl-holding": 44, "cdsl-edis": 20, "cdsl-statement-txn": 42, "cdsl-transactions": 230, "cdsl-txn-upload": 177, "cdsl-client-master": 186,
		"cdsl-bo-upload": 185, "cdsl-dis-master": 20, "cdsl-bo-response": 9, "cdsl-txn-response": 9} {
		s, ok := l.Get(id)
		if !ok || len(s.Columns) != n || !s.Confirmed || !strings.Contains(s.Source, "V3.0.1.4") {
			t.Errorf("%s: %v %d columns", id, ok, len(s.Columns))
		}
	}
	for name, id := range map[string]string{
		"ISIN_MSTR_041400_1_I_202610160830_001.csv":        "cdsl-isin-master",
		"ISIN_RATE_041400_1_F_202610160830_001.csv":        "cdsl-closing-price",
		"CC_CLND_041400_1_I_202610160830_001.csv":          "cdsl-calendar",
		"CA_EXP_041400_1_I_202610160830_001.csv":           "cdsl-corporate-action",
		"MBR_MSTR_041400_1_F_202610160830_001.csv":         "cdsl-member-master",
		"EDIS_041400_202610161830_001.csv":                 "cdsl-edis",
		"SOH_EXP_041400_7_A_202610160000_999_P_0_0_P0.csv": "cdsl-holding",
		"SOT_EXP_041400_8_I_202610161200_12_C_0_0_P0.csv":  "cdsl-statement-txn",
		"COD_EXP_041400_9_F_202610161200_002_P2.csv":       "cdsl-transactions",
		"CLN_MSTR_041400_9_F_202610161200_002_P0.csv":      "cdsl-client-master",
		"DIS_MSTR_041400_9_I_202610161200_002.csv":         "cdsl-dis-master",
		"BO_RESP_041400_202610161200_001.csv":              "cdsl-bo-response",
		"BO_RESP_041400_202610161200_001_UP.csv":           "cdsl-bo-response",
		"TXN_RESP_041400_202610161200_00012.csv":           "cdsl-txn-response",
		"18DPE6U16102026.001":                              "cdsl-freeze-master",
		"18DPE6U16102026.csv":                              "cdsl-freeze-master",
		"18DPE6U_041400_16102026.txt":                      "cdsl-freeze-master",
		"BO_UPLD_041400_202610161200_001.csv":              "cdsl-bo-upload",
		"TXN_UPLD_041400_202610161200_00001.csv":           "cdsl-txn-upload",
	} {
		var got []string
		for _, s := range l.Specs {
			if s.MatchesName(name) {
				got = append(got, s.ID)
			}
		}
		if len(got) != 1 || got[0] != id {
			t.Errorf("%s matches %v, want %s", name, got, id)
		}
	}
}

// The freeze master (DPE6) reads the layout of communiqué 2026/621 with one
// or three name fields, the older layout, and a heading line.
func TestCDSLFreezeMasterLayouts(t *testing.T) {
	l := lib(t)
	s, _ := l.Get("cdsl-freeze-master")
	new1 := "4001~1208160000000001~TEST HOLDER ONE~INE000A01011~TESTCO~F~0~0~A~1~2~2~09092026:101500~1~09092026:101500~S~~11~ORDER FROM CBI~REF1~~~~~OPR1~~CBI MUMBAI~ORDER NO 12, CBI, MUMBAI~ATTACHMENT~"
	new3 := "4002~1208160000000002~TEST~M~HOLDER~INE000A01011~TESTCO~P~10.000~10.000~A~1~~3~09092026:101500~1~09092026:101500~S~~1~~~~~~~~~~~~"
	old := "4003~1208160000000003~TEST HOLDER THREE~~~F~0~0~A~3~~3~09092026:101500~1~09092026:101500~S~31122026:000000~17~REQUESTED BY BO~~~~~~OPR1~"
	head := "FZ_ID~FZ_BO_ID~CUST_NAME~FZ_ISIN~ISIN_NAME~FZ_QTY_TYPE~FZ_SETUP_QTY~FZ_ACTUAL_QTY~FZ_STATUS~FROZEN_FOR~FZ_SUB_OPTION~FZ_INITIATED_BY~FZ_INIT_DATE~FZ_ACTIVATION_TYPE~FZ_ACTIVATION_DATE~FZ_TYPE~FZ_EXP_DATE~FZ_REASON_CD~FZ_REASON_DESC~FZ_INT_REF_NBR~FZ_UC_DP~FZ_UCR_DATE~FZ_UC_REMARK~FZ_REMARK~FZ_OPER_ID~FZ_PAR_ID~FZ_AUTHORITY_NAME~FZ_AUTHORITY_DETAILS~FZ_TYPE_OF_ORDER~FZ_RSN_FOR_DELAY"
	data := strings.Join([]string{head, new1, new3, old}, "\r\n") + "\r\n"
	got, err := l.Recognise("18DPE6U09092026.001", []byte(data))
	if err != nil || got.ID != s.ID {
		t.Fatalf("%v %v", got, err)
	}
	tb, err := s.Read("18DPE6U09092026.001", []byte(data))
	if err != nil || len(tb.Issues) != 0 || len(tb.Rows) != 4 {
		t.Fatalf("%v %v %d", err, tb.Issues, len(tb.Rows))
	}
	if tb.Rows[0].Record != "FZ_ID" || tb.Rows[1].Record != "#30" || tb.Rows[2].Record != "#32" || tb.Rows[3].Record != "#26" {
		t.Fatalf("records %q %q %q %q", tb.Rows[0].Record, tb.Rows[1].Record, tb.Rows[2].Record, tb.Rows[3].Record)
	}
	if tb.Rows[1].Get("FZ_AUTHORITY_NAME") != "CBI MUMBAI" || tb.Rows[2].Get("CUST_SRCH_NAME") != "HOLDER" || tb.Rows[3].Get("FZ_EXP_DATE") != "31122026:000000" {
		t.Fatalf("values %v", tb.Rows[1].Values)
	}
}
