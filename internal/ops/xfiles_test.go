package ops

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/sheet"
)

// header returns a layout's header row.
func xheader(t *testing.T, id string) []string {
	t.Helper()
	lib, err := XFiles()
	if err != nil {
		t.Fatal(err)
	}
	s, ok := lib.Get(id)
	if !ok {
		t.Fatalf("no layout %s", id)
	}
	var h []string
	for _, c := range s.Columns {
		h = append(h, c.Name)
	}
	return h
}

// xline builds a data line for a layout from named values.
func xline(h []string, v map[string]string) string {
	f := make([]string, len(h))
	for i, n := range h {
		f[i] = v[n]
	}
	return strings.Join(f, ",")
}

func gz(name string, b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Name = name
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func TestExchangeFormatsListed(t *testing.T) {
	o, _ := setup(t)
	fs, err := o.ExchangeFormats()
	if err != nil || len(fs) < 40 {
		t.Fatalf("%d %v", len(fs), err)
	}
	if fs[0].Direction != "in" || fs[len(fs)-1].Direction != "out" {
		t.Error("received files should come first")
	}
}

func TestLoadNCLTradeFile(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	ucc := cs[0].UCC
	h := xheader(t, "ncl-trade")
	row := func(no, side, qty, px string) string {
		return xline(h, map[string]string{"TradDt": D, "BizDt": D, "Sgmt": "CM", "Src": "NSE", "Xchg": "NSE", "FinInstrmTp": "STK", "TckrSymb": "XTEST", "SctySrs": "EQ",
			"ClntId": ucc, "BuySellInd": side, "TradQty": qty, "Pric": px, "UnqTradIdr": no, "OrdrRef": "1" + no, "TradDtTm": D + "T10:15:00", "CtclId": "111111111111111"})
	}
	file := strings.Join([]string{strings.Join(h, ","), row("990001", "B", "10", "101.50"), row("990002", "S", "4", "102.00")}, "\n")
	name := "Trade_NCL_CM_0_CM_90311_" + strings.ReplaceAll(D[8:]+D[5:7]+D[:4], "-", "") + "_F_0000.csv.gz"
	// Checking only does not load.
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", name, gz("x.csv", []byte(file)), false)
	if err != nil || len(res) != 1 || res[0].Format != "ncl-trade" || res[0].Rows != 2 || res[0].Problems != 0 || res[0].Loaded != "Checked only" {
		t.Fatalf("check %+v %v", res, err)
	}
	before, _ := o.App.ExchangeTrades(ctx, T, D)
	res, err = o.LoadExchangeFile(ctx, T, "priya.nair", name, gz("x.csv", []byte(file)), true)
	if err != nil || res[0].Added != 2 || !strings.Contains(res[0].Loaded, "trades") {
		t.Fatalf("load %+v %v", res, err)
	}
	after, _ := o.App.ExchangeTrades(ctx, T, D)
	if len(after) != len(before)+2 {
		t.Fatalf("trades %d -> %d", len(before), len(after))
	}
	// A file that does not match its layout is not loaded.
	bad := strings.Replace(file, "101.50", "1O1.50", 1)
	res, _ = o.LoadExchangeFile(ctx, T, "priya.nair", name, []byte(bad), true)
	if res[0].Problems != 1 || !strings.HasPrefix(res[0].Loaded, "Not loaded") {
		t.Fatalf("bad %+v", res)
	}
	if _, err := o.LoadExchangeFile(ctx, T, "meera.iyer", name, []byte(file), false); err == nil {
		t.Error("branch user loaded exchange files")
	}
	res, _ = o.LoadExchangeFile(ctx, T, "priya.nair", "notes.txt", []byte("a,b\n"), true)
	if res[0].Format != "" || res[0].Note == "" {
		t.Fatalf("unknown file %+v", res)
	}
}

func TestLoadCDSLHoldingAndPositions(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	var c Client
	for _, x := range cs {
		if x.Demat.ID != "" {
			c = x
			break
		}
	}
	if c.UCC == "" {
		t.Skip("no sample client with a demat account")
	}
	h := xheader(t, "cdsl-holding")
	line := func(bo, isin, bal, pl string) string {
		return xline(h, map[string]string{"Src": "CDSL", "BnfclOwnrId": bo, "ISIN": isin, "CurBal": bal, "PldgdBal": pl, "StmtDt": D + "T18:00:00"})
	}
	file := strings.Join([]string{strings.Join(h, ","), line(c.Demat.ID, "INE000X01010", "25.000", "5.000"), line("9999999999999999", "INE000X01010", "1.000", "0.000")}, "\r\n") + "\r\n"
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", "SOH_EXP_043200_123_F_202610161831_7_C_0_0_P0.csv", []byte(file), true)
	if err != nil || res[0].Added != 1 || res[0].Skipped != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	hs, _ := o.Holdings(ctx, T, c.UCC)
	found := false
	for _, x := range hs {
		if x.ISIN == "INE000X01010" && x.Qty == 25 && x.Pledged == 5 {
			found = true
		}
	}
	if !found {
		t.Fatalf("holding not loaded: %+v", hs)
	}

	// F&O position file: net long 50 of a future.
	ph := xheader(t, "ncl-position")
	pl := xline(ph, map[string]string{"Sgmt": "FO", "Src": "NSE", "RptgDt": D, "BizDt": D, "ClntId": c.UCC, "FinInstrmTp": "IDF", "TckrSymb": "NIFTY", "XpryDt": "2026-10-27",
		"PstExrcAssgndLngQty": "75", "PstExrcAssgndShrtQty": "25", "SttlmPric": "25000.00"})
	res, err = o.LoadExchangeFile(ctx, T, "priya.nair", "20261016.txt", []byte(strings.Join(ph, ",")+"\r\n"+pl+"\r\n"), true)
	if err != nil || res[0].Format != "ncl-position" || res[0].Added != 1 {
		t.Fatalf("positions %+v %v", res, err)
	}
	ps, _ := o.carried(ctx, T, D)
	ok := false
	for _, p := range ps {
		if p.UCC == c.UCC && p.Instrument == "NIFTY|FUT|2026-10-27|" && p.Qty == 50 {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("position not loaded: %+v", ps)
	}
}

func TestOutgoingExchangeFiles(t *testing.T) {
	o, ctx := setup(t)
	if _, _, err := o.ExchangeFile(ctx, T, "priya.nair", "bow-fund", D, XFileOptions{}); err == nil {
		t.Error("a file Ledgerline does not write was produced")
	}
	name, b, err := o.ExchangeFile(ctx, T, "priya.nair", "ncl-allocation", D, XFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "90311_ALLOC_16102026.T") {
		t.Errorf("name %s", name)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\r\n")
	if len(lines) == 0 || len(strings.Split(lines[0], ",")) != 15 || !strings.HasPrefix(lines[0], "16-OCT-2026,") {
		t.Fatalf("allocation %q", lines[:1])
	}
	_, j, err := o.ExchangeFile(ctx, T, "priya.nair", "ncl-allocation-api", D, XFileOptions{})
	var msg struct {
		Version string `json:"version"`
		Data    struct {
			MsgID string           `json:"msgId"`
			Req   []map[string]any `json:"allocationRequest"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal(j, &msg) != nil || msg.Version != "1.0" || len(msg.Data.MsgID) != 20 || len(msg.Data.Req) != len(lines) {
		t.Fatalf("api %s %v", j, err)
	}
	name, j, err = o.ExchangeFile(ctx, T, "priya.nair", "holding-statement-api", D, XFileOptions{})
	if err != nil || !strings.HasPrefix(name, "AAACB1234C_HS_16102026_01") || !bytes.Contains(j, []byte(`"memberPan":"AAACB1234C"`)) {
		t.Fatalf("holdings %s %v", name, err)
	}
	name, b, err = o.ExchangeFile(ctx, T, "priya.nair", "segmon-client-funds", D, XFileOptions{})
	if err != nil || name != "AAACB1234C_16102026_01.CSV" {
		t.Fatalf("segmon %s %v", name, err)
	}
	rows := strings.Split(strings.TrimSpace(string(b)), "\r\n")
	if len(rows) < 10 || len(strings.Split(rows[0], ",")) != 65 || !strings.HasPrefix(rows[1], "16-10-2026,AAACB1234C,AAACB1234C,,,") {
		t.Fatalf("segmon %d rows: %q", len(rows), rows[:2])
	}
	// What Ledgerline writes reads back cleanly against its own layout.
	lib, _ := XFiles()
	for _, id := range []string{"segmon-client-funds", "ncl-allocation"} {
		s, _ := lib.Get(id)
		_, data, _ := o.ExchangeFile(ctx, T, "priya.nair", id, D, XFileOptions{})
		tb, err := s.Read(id, data)
		if err != nil || len(tb.Issues) != 0 {
			t.Fatalf("%s reads back with %v %v", id, tb.Issues, err)
		}
	}
}

func TestSettlementMasterCCILAndCUSPAFile(t *testing.T) {
	o, ctx := setup(t)
	h := xheader(t, "ncl-settlement-master")
	line := func(tp, no, td, out string) string {
		return xline(h, map[string]string{"Sgmt": "CM", "Src": "NCL", "TradRegnOrgn": "1", "SttlmTp": tp, "SctiesSttlmTxId": no, "OrgnlTradDtTm": td + "T09:00:00",
			"TradEndDtTm": td + "T15:30:00", "CtdnConfDtTm": td + "T17:00:00", "OblgtnDtTm": td + "T18:00:00", "FndsPayInDtAndTm": out + "T10:30:00",
			"FndsPayOutDtAndTm": out + "T13:30:00", "SctiesPayInDtAndTm": out + "T10:30:00", "SctiesPayOutDtAndTm": out + "T13:30:00"})
	}
	file := strings.Join([]string{strings.Join(h, ","), line("NORMAL1", "2026199", D, "2026-10-19"), line("NORMAL0", "2026299", D, D)}, "\n") + "\n"
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", "SettlementMaster_NCL_CM_0_0_0_20261000_0_0000.csv", []byte(file), true)
	if err != nil || res[0].Format != "ncl-settlement-master" || res[0].Added != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	if st, ok := o.SettlementFor(ctx, T, "CM", "NORMAL1", D); !ok || st.No != "2026199" || st.SecOut != "2026-10-19T13:30:00" {
		t.Fatalf("calendar %+v", st)
	}

	// CCIL prices, as an Excel workbook with section titles.
	wb, _ := sheet.WriteXLSX([]sheet.Sheet{{Name: "Sheet2", Rows: [][]string{{"", "Fictional clearing house"}, {}, {"T-BILL"},
		{"Sr. No.", "ISIN No.", "Description", "Coupon", "Maturity Date", "Price * (Rs.)"},
		{"1", "IN002026X011", "91 DTB 05112026", "-", "46331", "99.1234"}, {}, {"G-SEC"},
		{"Sr. No.", "ISIN No.", "Description", "Coupon", "Maturity Date", "Price * (Rs.)"},
		{"1", "IN0020990011", "7.10% GS 2034", "7.1", "49000", "101.25"}, {"* Prices are indicative"}}}})
	res, err = o.LoadExchangeFile(ctx, T, "priya.nair", "MTMPRICE16102026.xlsx", wb, true)
	if err != nil || res[0].Format != "ccil-gsec-price" || res[0].Added != 2 || res[0].Problems != 0 {
		t.Fatalf("ccil %+v %v", res, err)
	}
	_, look, _ := o.Market(ctx, T, D)
	if i, ok := look.ByISIN("IN0020990011"); !ok || i.LTP.String() != "101.25" {
		t.Fatalf("g-sec price %+v", i)
	}

	// CUSPA / MTF pay-out reporting.
	if _, _, err := o.ExchangeFile(ctx, T, "priya.nair", "ncl-cuspa-mtf-payout", D, XFileOptions{}); err == nil {
		t.Fatal("file written with nothing to report")
	}
	cs, _ := o.Clients(ctx, T)
	_ = docs.Upsert(ctx, o.Docs, T, cCUSPA, "CU-T1", CUSPAItem{ID: "CU-T1", Date: D, UCC: cs[0].UCC, ISIN: "INE000X01010", Symbol: "XTEST", Qty: 40, Status: "Held in CUSPA"}, "test")
	name, b, err := o.ExchangeFile(ctx, T, "priya.nair", "ncl-cuspa-mtf-payout", D, XFileOptions{})
	if err != nil || name != "UNPAIDMTF_M_2026199.T01" || !strings.HasSuffix(string(b), ","+cs[0].UCC+",XTEST,EQ,40,T,\r\n") {
		t.Fatalf("%s %q %v", name, b, err)
	}
}
