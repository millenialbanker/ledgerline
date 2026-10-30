package ops

import (
	"os"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// tradeFileOf builds a standardised trade file (made-up trades) for the
// exchange's layout.
func tradeFileOf(t *testing.T, id, exch, ucc string, trades ...[3]string) []byte {
	t.Helper()
	h := xheader(t, id)
	lines := []string{strings.Join(h, ",")}
	for _, tr := range trades { // trade no, side, qty
		lines = append(lines, xline(h, map[string]string{"TradDt": D, "BizDt": D, "Sgmt": "CM", "Src": exch, "Xchg": exch, "FinInstrmTp": "STK", "TckrSymb": "XTEST", "SctySrs": "A",
			"ClntId": ucc, "BuySellInd": tr[1], "TradQty": tr[2], "Pric": "100.50", "UnqTradIdr": tr[0], "OrdrRef": "9" + tr[0], "TradDtTm": D + "T10:15:00", "CtclId": "111111111111111", "SttlmTp": "DR1"}))
	}
	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

// Provisional trade files feed live risk until the final file of the
// exchange segment arrives; then only the final file's trades count.
func TestProvisionalThenFinalTrades(t *testing.T) {
	o, ctx := newOps(t)
	ucc := "ZZT001" // made up
	d8 := strings.ReplaceAll(D, "-", "")
	load := func(name string, b []byte) XFileResult {
		t.Helper()
		res, err := o.LoadExchangeFile(ctx, T, "priya.nair", name, b, true)
		if err != nil || len(res) != 1 || res[0].Problems > 0 {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		return res[0]
	}
	count := func() (map[string]int, string) {
		t.Helper()
		ts, src, err := o.IntradayTrades(ctx, T, D)
		if err != nil {
			t.Fatal(err)
		}
		n := map[string]int{}
		for _, x := range ts {
			n[x.Exchange+":"+x.TradeNo]++
		}
		return n, src
	}

	// Two provisional BSE files (the second repeats a trade) and one NSE file.
	r := load("Trade_BSE_CM_0_TM_9999_"+d8+"_P_0001.csv", tradeFileOf(t, "bse-trade", "BSE", ucc, [3]string{"770001", "B", "10"}, [3]string{"770002", "S", "5"}))
	if r.Format != "bse-trade" || r.Added != 2 || !strings.Contains(r.Loaded, "provisional") {
		t.Fatalf("P1 %+v", r)
	}
	load("Trade_BSE_CM_0_TM_9999_"+d8+"_P_0002.csv", tradeFileOf(t, "bse-trade", "BSE", ucc, [3]string{"770002", "S", "5"}, [3]string{"770003", "B", "1"}))
	load("Trade_NCL_CM_0_TM_9999_"+d8+"_P_0001.csv", tradeFileOf(t, "ncl-trade", "NSE", ucc, [3]string{"660001", "B", "3"}))
	if ex, _ := o.App.ExchangeTrades(ctx, T, D); len(ex) != 0 {
		t.Fatalf("provisional trades reached the exchange trades: %d", len(ex))
	}
	n, src := count()
	if len(n) != 4 || n["BSE:770002"] != 1 || !strings.Contains(src, "provisional") {
		t.Fatalf("after P: %v %s", n, src)
	}
	if lv, err := o.LiveRisk(ctx, T, D, ""); err != nil || lv.Trades != 4 || !strings.Contains(lv.Source, "provisional") {
		t.Fatalf("live risk after P: %d %s %v", lv.Trades, lv.Source, err)
	}

	// The BSE final file replaces BSE's provisional trades; NSE's stay until its own.
	load("TRADE_BSE_CM_0_TM_9999_"+d8+"_F_0000.csv", tradeFileOf(t, "bse-trade", "BSE", ucc, [3]string{"770001", "B", "10"}, [3]string{"770009", "S", "7"}))
	n, src = count()
	if len(n) != 3 || n["BSE:770001"] != 1 || n["BSE:770009"] != 1 || n["BSE:770002"] != 0 || n["NSE:660001"] != 1 || !strings.Contains(src, "final and provisional") {
		t.Fatalf("after BSE F: %v %s", n, src)
	}
	// A late provisional file is not used.
	if r := load("Trade_BSE_CM_0_TM_9999_"+d8+"_P_0003.csv", tradeFileOf(t, "bse-trade", "BSE", ucc, [3]string{"770004", "B", "1"})); !strings.HasPrefix(r.Loaded, "Not used") {
		t.Fatalf("late P: %+v", r)
	}
	load("Trade_NCL_CM_0_TM_9999_"+d8+"_F_0000.csv", tradeFileOf(t, "ncl-trade", "NSE", ucc, [3]string{"660001", "B", "3"}))
	n, src = count()
	if len(n) != 3 || n["NSE:660001"] != 1 || src != "Exchange" {
		t.Fatalf("after F: %v %s", n, src)
	}
	if lv, _ := o.LiveRisk(ctx, T, D, ""); lv.Trades != 3 || lv.Source != "Exchange" {
		t.Fatalf("live risk after F: %d %s", lv.Trades, lv.Source)
	}
}

// The synthetic MBRSTD file (made-up codes) is read and its summary checked.
func TestBSEStampDutyFixture(t *testing.T) {
	o, ctx := newOps(t)
	name := "MBRSTD001_OTB_CM_9999_161026.CSV"
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	r := loadX(t, o, name, b)
	if r.Format != "bse-stamp-duty" || r.Added != 3 {
		t.Fatalf("%+v", r)
	}
	ds := o.ccData(ctx, T, "bsestamp", D, "")
	if len(ds) != 1 || ds[0].Rows[2]["ClientCode"] != "ZZTEST02X" || ds[0].Rows[1]["SettType"] != "DR1" {
		t.Fatalf("%+v", ds)
	}
	// A summary that disagrees with the details is not loaded.
	bad := strings.Replace(string(b), ",3,4.65\r\n", ",3,4.75\r\n", 1)
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", name, []byte(bad), true)
	if err != nil || len(res) != 1 || !strings.Contains(res[0].Loaded, "does not equal") {
		t.Fatalf("bad total %+v %v", res, err)
	}
	bad = strings.Replace(string(b), ",3,4.65\r\n", ",4,4.65\r\n", 1)
	if res, _ := o.LoadExchangeFile(ctx, T, "priya.nair", name, []byte(bad), true); !strings.Contains(res[0].Loaded, "detail records") {
		t.Fatalf("bad count %+v", res)
	}
}

// BSE stamp duty is reconciled client-wise against the stamp duty billed on
// BSE trades.
func TestBSEStampDutyReconciled(t *testing.T) {
	o, ctx := setup(t)
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBilling(ctx, T, "priya.nair", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	cs, _ := o.Clients(ctx, T)
	var billed, other string
	var amt money.Dec
	for _, c := range cs {
		bt, _ := o.bills(ctx, T, c.UCC, D, D)
		if a := bt.stampBy["BSE"].Abs(); a.Sign() > 0 && billed == "" {
			billed, amt = c.UCC, a
		} else if a.Sign() == 0 && other == "" {
			other = c.UCC
		}
	}
	if billed == "" || other == "" {
		t.Skip("sample day has no BSE stamp duty")
	}
	half := amt.Sub(money.MustDec("0.01"))
	hdr := "RECORD TYPE,TRADE DATE,CM CODE,TOTAL NO. OF RECORDS IN THE DETAILS SECTION,TOTAL STAMP DUTY AMOUNT"
	dh := "RECORD TYPE,TRADE DATE,SETT NO.,SETT TYPE,SCRIP CODE,GROUP NAME,CM CODE,TM CODE/CP CODE,CLIENT CODE/INST,BUY SQOFF QTY,BUY SQOFF AMOUNT,BUY SQOFF STAMP DUTY,SELL SQOFF QTY,SELL SQOFF AMOUNT,SELL SQOFF STAMP DUTY,BUY DELIVERY QTY,BUY DELIVERY AMOUNT,BUY DELIVERY STAMP DUTY,SELL DELIVERY QTY,SELL DELIVERY AMOUNT,SELL DELIVERY STAMP DUTY,TOTAL STAMP DUTY"
	det := func(ucc, scrip string, sd money.Dec) string {
		return "20,16-10-2026,2627145,DR1," + scrip + ",A,9999,9999," + ucc + ",0,0.00,0.00,0,0.00,0.00,1,100.00," + sd.Fixed(2) + ",0,0.00,0.00," + sd.Fixed(2)
	}
	total := amt.Add(money.MustDec("5"))
	file := strings.Join([]string{hdr, "10,16-10-2026,9999,3," + total.Fixed(2), dh,
		det(billed, "599901", half), det(billed, "599902", money.MustDec("0.01")), det(other, "599903", money.MustDec("5"))}, "\r\n") + "\r\n"
	loadX(t, o, "MBRSTD002_OTB_CM_9999_161026.CSV", []byte(file))
	secs, err := o.ClearingData(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secs {
		if s.ID != "stamp" {
			continue
		}
		if !s.Loaded || len(s.Rows) != 2 || s.Diffs != 1 {
			t.Fatalf("stamp section %+v", s)
		}
		for _, r := range s.Rows {
			if r[0] != "BSE CM" {
				t.Fatalf("row %v", r)
			}
			if r[1] == billed && (r[3] != amt.Fixed(2) || r[5] != "0.00") || r[1] == other && r[5] != "5.00" {
				t.Fatalf("row %v (billed %s)", r, amt.Fixed(2))
			}
		}
		return
	}
	t.Fatal("no stamp section")
}

// BSE's common-format bhavcopy sets closing prices for exchange BSE, taking
// the ISIN from the scrip and series mapping by scrip code.
func TestBSEBhavcopy(t *testing.T) {
	o, ctx := setup(t)
	m, _, _ := o.Market(ctx, T, D)
	var eq Instrument
	for _, i := range m.Instruments {
		if i.Exchange == "BSE" && i.Segment == "CM" && i.ISIN != "" {
			eq = i
			break
		}
	}
	if eq.Symbol == "" {
		t.Fatal("no BSE instrument in the sample market")
	}
	loadX(t, o, "BSE_Scrip_Series_Mapping_"+strings.ReplaceAll(D, "-", "")+".csv", xwrite(t, "bse-scrip-series",
		xr("", "Security ISIN", eq.ISIN, "Security BSE Scrip Code", "599911", "Security BSE Group", "A", "Security Description", "Test Co", "Security Applicable Symbol", eq.Symbol, "Security Series", "EQ")))
	h := xheader(t, "udiff-bhavcopy")
	row := func(v map[string]string) string {
		base := map[string]string{"TradDt": D, "BizDt": D, "Src": "BSE", "SsnId": "F1"}
		for k, x := range v {
			base[k] = x
		}
		return xline(h, base)
	}
	d8 := strings.ReplaceAll(D, "-", "")
	cm := strings.Join([]string{strings.Join(h, ","),
		row(map[string]string{"Sgmt": "CM", "FinInstrmTp": "STK", "FinInstrmId": "599911", "TckrSymb": eq.Symbol, "SctySrs": "A", "ClsPric": "123.40", "LastPric": "123.55", "NewBrdLotQty": "1"}),
		row(map[string]string{"Sgmt": "CM", "FinInstrmTp": "STK", "FinInstrmId": "599912", "ISIN": "INE999Z01011", "TckrSymb": "ZZNEWCO", "SctySrs": "B", "ClsPric": "45.00", "NewBrdLotQty": "1", "FinInstrmNm": "ZZ New Co"}),
	}, "\n") + "\n"
	r := loadX(t, o, "BhavCopy_BSE_CM_0_0_0_"+d8+"_F_0000.csv", []byte(cm))
	if r.Format != "udiff-bhavcopy" || r.Added != 2 {
		t.Fatalf("%+v", r)
	}
	fo := strings.Join([]string{strings.Join(h, ","),
		row(map[string]string{"Sgmt": "FO", "FinInstrmTp": "IDF", "FinInstrmId": "1", "TckrSymb": "SENSEX", "XpryDt": "2026-10-29", "ClsPric": "82000.00", "SttlmPric": "82010.50", "UndrlygPric": "81990.00", "NewBrdLotQty": "20"}),
		row(map[string]string{"Sgmt": "FO", "FinInstrmTp": "IDO", "FinInstrmId": "2", "TckrSymb": "SENSEX", "XpryDt": "2026-10-29", "StrkPric": "82000.00", "OptnTp": "CE", "ClsPric": "410.00", "SttlmPric": "412.00", "NewBrdLotQty": "20"}),
	}, "\n") + "\n"
	if r := loadX(t, o, "BhavCopy_BSE_FO_0_0_0_"+d8+"_F_0000.csv", []byte(fo)); r.Added != 2 {
		t.Fatalf("FO %+v", r)
	}
	m, look, _ := o.Market(ctx, T, D)
	if i, ok := look.Get("BSE", "CM", eq.Key()); !ok || i.Close.String() != "123.4" || i.LTP.String() != "123.55" || i.ISIN != eq.ISIN {
		t.Fatalf("BSE close %+v %v", i, ok)
	}
	if i, ok := look.Get("BSE", "CM", "ZZNEWCO|EQ||"); !ok || i.ISIN != "INE999Z01011" || i.Close.String() != "45" {
		t.Fatalf("new scrip %+v %v", i, ok)
	}
	if sp := m.SettlementPrices()["SENSEX|FUT|2026-10-29|"]; sp.String() != "82010.5" {
		t.Fatalf("settlement %v", sp)
	}
	if i, ok := look.Get("BSE", "FO", "SENSEX|CE|2026-10-29|82000"); !ok || i.Close.String() != "412" || i.Lot != 20 {
		t.Fatalf("option %+v %v", i, ok)
	}
	// NSE's prices for the same symbol are untouched.
	if i, ok := look.Get("NSE", "CM", eq.Key()); ok && i.Close.String() == "123.4" {
		t.Fatal("BSE bhavcopy changed NSE's price")
	}
	if ref, _, _ := docs.Get[SecRef](ctx, o.Docs, T, cSecRef, "INE999Z01011"); ref.BSECode != "599912" {
		t.Fatalf("scrip code not learnt: %+v", ref)
	}
}
