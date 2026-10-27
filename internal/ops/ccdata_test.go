package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// xwrite builds a file in a layout from rows of values (made-up data).
func xwrite(t *testing.T, id string, rows ...xfile.Row) []byte {
	t.Helper()
	lib, _ := XFiles()
	s, ok := lib.Get(id)
	if !ok {
		t.Fatalf("no layout %s", id)
	}
	b, err := s.Write(rows)
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return b
}

func xr(rec string, kv ...string) xfile.Row {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return xfile.Row{Record: rec, Values: m}
}

func loadX(t *testing.T, o *Ops, name string, data []byte) XFileResult {
	t.Helper()
	res, err := o.LoadExchangeFile(t.Context(), T, "priya.nair", name, data, true)
	if err != nil || len(res) != 1 {
		t.Fatalf("%s: %+v %v", name, res, err)
	}
	if res[0].Problems > 0 || strings.HasPrefix(res[0].Loaded, "Not loaded") || strings.HasPrefix(res[0].Loaded, "Checked and kept") {
		t.Fatalf("%s: %+v", name, res[0])
	}
	return res[0]
}

func TestClearingDataLoadedAndUsed(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	ucc := cs[0].UCC
	d8 := "16102026"

	// Security master and CDSL closing price value a security the market data lacks.
	loadX(t, o, "C_SEC_"+d8+"_NSE.csv", xwrite(t, "nse-security-master", xr("", "Security Symbol", "FICTCO", "Security Series", "EQ", "Security Description", "Fictional Co Ltd", "Security ISIN", "INE999Q01017", "Security Code", "1", "Settlement Type", "T1")))
	loadX(t, o, "ISIN_RATE_123_4567_F_202610161830_01.csv", xwrite(t, "cdsl-closing-price", xr("", "Src", "CDSL", "ISIN", "INE999Q01017", "Price", "123.45", "ActlDt", D)))
	_, look, _ := o.Market(ctx, T, D)
	if i, ok := look.ByISIN("INE999Q01017"); !ok || i.LTP.String() != "123.45" || i.Symbol != "FICTCO" {
		t.Fatalf("reference price %+v %v", i, ok)
	}
	// VaR file gives a haircut where the market data has none.
	mk, _, _ := o.Market(ctx, T, D)
	var isin string
	for _, i := range mk.Instruments {
		if i.ISIN != "" && i.Segment == "CM" && i.HaircutPct.Sign() == 0 {
			isin = i.ISIN
			break
		}
	}
	if isin != "" {
		loadX(t, o, "C_VAR1_"+d8+"_1.DAT", xwrite(t, "ncl-var", xr("10", "RecordType", "10", "FileDate", d8, "Field3", "1", "RecordCount", "1"),
			xr("20", "RecordType", "20", "Symbol", "X", "Series", "EQ", "ISIN", isin, "SecurityVaR", "10.00", "IndexVaR", "0.00", "VaRMargin", "12.50", "ExtremeLossRate", "3.50", "AdhocMargin", "0.00", "ApplicableMarginRate", "16.00")))
		_, look, _ = o.Market(ctx, T, D)
		if i, _ := look.ByISIN(isin); i.HaircutPct.String() != "16" {
			t.Fatalf("haircut not filled: %+v", i)
		}
	}

	// MG13 → margin reporting upload with the collected amounts.
	loadX(t, o, "C_MG13_90311_"+d8+".lis", xwrite(t, "ncl-span-client", xr("", "Date", "16-Oct-2026", "ClientCode", ucc, "VaRPlusELM", "1000.00", "MinimumMargin", "1000.00", "AdditionalMargin", "0.00", "MTMLoss", "0.00",
		"EODTotalActualMargin", "1000.00", "EODMarginToCollect", "1000.00", "PeakMarginToCollect", "1200.00", "ClientProFlag", "C")))
	name, b, err := o.ExchangeFile(ctx, T, "priya.nair", "ncl-margin-reporting", D, XFileOptions{})
	if err != nil || name != "C_MRG_TM_16102026_01.csv" || !strings.Contains(string(b), ","+ucc+",1000.00,") || strings.Count(string(b), ",") != 11 {
		t.Fatalf("margin reporting %s %q %v", name, b, err)
	}

	// SA04 → reason → short-allocation reporting upload.
	loadX(t, o, "C_SA04_90311_"+d8+".lis", xwrite(t, "ncl-short-allocation", xr("", "Date", "16-Oct-2026", "Indicator", "E", "ClientCode", ucc, "MinimumMarginRequired", "1000.00", "CashCollateral", "500.00",
		"NonCashCollateral", "0.00", "ExcessCollateralOtherSegment", "0.00", "TotalCollateral", "500.00", "ShortAllocation", "500.00")))
	if _, _, err := o.ExchangeFile(ctx, T, "priya.nair", "ncl-short-allocation-reporting", D, XFileOptions{}); err == nil {
		t.Fatal("reporting file written without reasons")
	}
	if _, err := o.SaveSAReason(ctx, T, "priya.nair", SAReason{Date: D, Seg: "CM", UCC: ucc, Code: "01", Amount: money.MustDec("500")}); err == nil {
		t.Fatal("reason 01 without the other clearing corporation")
	}
	if _, err := o.SaveSAReason(ctx, T, "priya.nair", SAReason{Date: D, Seg: "CM", UCC: ucc, Code: "02", Amount: money.MustDec("500")}); err != nil {
		t.Fatal(err)
	}
	_, b, err = o.ExchangeFile(ctx, T, "priya.nair", "ncl-short-allocation-reporting", D, XFileOptions{})
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(b)), ",500.00,02,") {
		t.Fatalf("SA reporting %q %v", b, err)
	}

	// Stamp duty reconciliation and the clearing data screen.
	loadX(t, o, "20261016.txt", xwrite(t, "stamp-duty", xr("", "Sgmt", "CM", "Src", "NCL", "ClctnDt", D, "ClntId", ucc, "TckrSymb", "X", "StmpDtyAmt", "12.34")))
	secs, err := o.ClearingData(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]CCSection{}
	for _, s := range secs {
		by[s.ID] = s
	}
	if s := by["margin"]; !s.Loaded || len(s.Rows) != 1 || s.Rows[0][1] != ucc {
		t.Fatalf("margin section %+v", s)
	}
	if s := by["short_allocation"]; len(s.Rows) != 1 || !strings.Contains(s.Rows[0][7], "02") || s.Diffs != 0 {
		t.Fatalf("SA section %+v", s)
	}
	if s := by["stamp"]; !s.Loaded || len(s.Rows) != 1 || s.Rows[0][3] != "12.34" {
		t.Fatalf("stamp section %+v", s)
	}
	if s := by["obligation"]; s.Loaded {
		t.Fatal("obligation shown without a file")
	}

	// CDSL transactions into the DP register.
	var dm Client
	for _, c := range cs {
		if len(c.Demat.ID) == 16 {
			dm = c
			break
		}
	}
	if dm.UCC != "" {
		before, _ := o.DPTransactions(ctx, T, dm.UCC)
		f := xwrite(t, "cdsl-transactions",
			xr("", "Src", "CDSL", "TxnTyp", "WTR", "TxnID", "1", "LineNb", "1", "ClntId", dm.Demat.ID, "ISIN", "INE999Q01017", "ExecDt", D, "Qty", "10.000", "TxnCd", "DBT"),
			xr("", "Src", "CDSL", "TxnTyp", "NPY", "TxnID", "2", "LineNb", "1", "ClntId", dm.Demat.ID, "ISIN", "INE999Q01017", "ExecDt", D, "Qty", "5.000", "TxnCd", "CRD"),
			xr("", "Src", "CDSL", "TxnTyp", "PLG", "TxnID", "3", "LineNb", "1", "ClntId", dm.Demat.ID, "ISIN", "INE999Q01017", "ExecDt", D, "Qty", "5.000", "TxnCd", "ERMRK"))
		res := loadX(t, o, "COD_EXP_12345_123456_F_202610161830_001_P1.csv", f)
		if res.Added != 2 || res.Skipped != 1 {
			t.Fatalf("cdsl txns %+v", res)
		}
		if again := loadX(t, o, "COD_EXP_12345_123456_F_202610161830_001_P1.csv", f); again.Added != 0 {
			t.Fatalf("duplicates added %+v", again)
		}
		after, _ := o.DPTransactions(ctx, T, dm.UCC)
		if len(after) != len(before)+2 {
			t.Fatalf("register %d -> %d", len(before), len(after))
		}
		// DIS master upload for a book issued this week.
		set, _ := o.Settings(ctx, T)
		set.MemberCodes["CDSL-SENDER"] = "TESTS"
		if err := docs.Upsert(ctx, o.Docs, T, cSettings, "main", set, "test"); err != nil {
			t.Fatal(err)
		}
		if _, err := o.IssueDISBook(ctx, T, "priya.nair", dm.UCC, 10); err != nil {
			t.Fatal(err)
		} else {
			name, b, err := o.ExchangeFile(ctx, T, "priya.nair", "cdsl-txn-upload", o.now().Format("2006-01-02"), XFileOptions{})
			if err != nil || !strings.HasPrefix(name, "TXN_UPLD_"+dm.Demat.ID[2:8]+"_") || !strings.Contains(string(b), ",DMU,") {
				t.Fatalf("DIS master %s %q %v", name, b, err)
			}
		}
	}
}

// TestRealFilesLoad loads real files from LEDGERLINE_XFILE_SAMPLES (only on
// the machine that holds them) and prints what happened, without values.
func TestRealFilesLoad(t *testing.T) {
	dir := os.Getenv("LEDGERLINE_XFILE_SAMPLES")
	if dir == "" {
		t.Skip("set LEDGERLINE_XFILE_SAMPLES")
	}
	o, ctx := setup(t)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		name := e.Name()
		if i := strings.Index(name, "__"); i >= 0 {
			name = name[i+2:]
		}
		res, err := o.LoadExchangeFile(ctx, T, "priya.nair", name, b, true)
		if err != nil {
			t.Logf("ERROR %v", err)
			continue
		}
		for _, r := range res {
			if r.Format == "" {
				continue
			}
			t.Logf("%-32s rows=%-5d problems=%d added=%-5d skipped=%-5d %s", r.Format, r.Rows, r.Problems, r.Added, r.Skipped, r.Loaded)
		}
	}
	lib, _ := XFiles()
	for kind, out := range map[string]string{"mg13": "ncl-margin-reporting", "mcxmargin": "mcx-margin-reporting", "sa04": "ncl-short-allocation-reporting"} {
		ds, _ := docs.List[CCData](ctx, o.Docs, T, cCCData, kind+"|")
		for _, d := range ds {
			if (kind == "mg13" || kind == "sa04") && d.Segment != "CM" {
				continue
			}
			if kind == "sa04" {
				for _, r := range d.Rows {
					_, _ = o.SaveSAReason(ctx, T, "priya.nair", SAReason{Date: d.Date, Seg: "CM", UCC: r["ClientCode"], Code: "02", Amount: money.MustDec("1")})
				}
			}
			name, b, err := o.ExchangeFile(ctx, T, "priya.nair", out, d.Date, XFileOptions{})
			s, _ := lib.Get(out)
			tb, rerr := s.Read(name, b)
			issues := -1
			if rerr == nil {
				issues = len(tb.Issues)
			}
			t.Logf("written %-30s %s rows_in=%d lines_out=%d issues=%d err=%v", out, name, len(d.Rows), strings.Count(string(b), "\n"), issues, err)
		}
	}
	secs, _ := o.ClearingData(ctx, T, "2026-09-30")
	for _, s := range secs {
		t.Logf("section %-18s loaded=%v rows=%d differences=%d", s.ID, s.Loaded, len(s.Rows), s.Diffs)
	}
}
