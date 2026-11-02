package charges

import (
	"os"
	"strings"
	"testing"

	"ledgerline/internal/money"
)

func load(t *testing.T) Schedule {
	t.Helper()
	f, err := os.Open("../../config/charges/sample-schedule.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := LoadSchedule(f)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func amt(s string) money.Amount { return money.MustAmount(s) }

func TestDeliveryBuy(t *testing.T) {
	// 10 RELIANCE @ 1412.00 delivery buy on NSE. Worked by hand:
	// STT 0.1% = 14.12; txn 0.00297% = 0.419364 → 0.42; SEBI 0.0001% = 0.01412 → 0.01;
	// stamp 0.015% = 2.118 → 2.12; GST 18% × (0 + 0.42 + 0.01) = 0.0774 → 0.08. Total 16.75.
	b, err := load(t).Compute(Leg{Bucket: CMDelivery, Exchange: "NSE", Side: "B", Qty: 10, Price: money.MustDec("1412.00"), Date: "2026-10-16"})
	if err != nil {
		t.Fatal(err)
	}
	want := Breakdown{Brokerage: 0, STT: amt("14.12"), ExchTxn: amt("0.42"), SEBIFee: amt("0.01"), Stamp: amt("2.12"), GST: amt("0.08"), Total: amt("16.75")}
	if b.Brokerage != want.Brokerage || b.STT != want.STT || b.ExchTxn != want.ExchTxn || b.SEBIFee != want.SEBIFee || b.Stamp != want.Stamp || b.GST != want.GST || b.Total != want.Total {
		t.Fatalf("got %+v\nwant %+v", b, want)
	}
	if b.Turnover.String() != "14120" {
		t.Fatalf("turnover %s", b.Turnover)
	}
}

func TestFuturesAndOptions(t *testing.T) {
	s := load(t)
	// NIFTY futures buy 65 @ 25810: turnover 16,77,650. Brokerage min(20, 503.30) = 20;
	// txn 29.02; SEBI 1.68; stamp 33.55; GST 18% × 50.70 = 9.13. Total 93.38.
	f, err := s.Compute(Leg{Bucket: FOFut, Exchange: "NSE", Side: "B", Qty: 65, Price: money.MustDec("25810"), Date: "2026-10-16"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Brokerage != amt("20.00") || f.ExchTxn != amt("29.02") || f.SEBIFee != amt("1.68") || f.Stamp != amt("33.55") || f.GST != amt("9.13") || f.Total != amt("93.38") {
		t.Fatalf("futures %+v", f)
	}
	// Option sell: STT 0.15% on premium from 1 Apr 2026 (NSE/FATAX/73524),
	// no stamp on sell side. Premium 14,625 × 0.15% = 21.9375 → 21.94.
	o, _ := s.Compute(Leg{Bucket: FOOpt, Exchange: "NSE", Side: "S", Qty: 130, Price: money.MustDec("112.50"), Date: "2026-10-16"})
	if o.STT != amt("21.94") || o.Stamp != 0 || o.Brokerage != amt("20.00") {
		t.Fatalf("option sell %+v", o)
	}
	// Futures sell: 0.05% from 1 Apr 2026. 16,77,650 × 0.05% = 838.825 → 838.83.
	fs, _ := s.Compute(Leg{Bucket: FOFut, Exchange: "NSE", Side: "S", Qty: 65, Price: money.MustDec("25810"), Date: "2026-10-16"})
	if fs.STT != amt("838.83") || f.STT != 0 {
		t.Fatalf("futures STT sell %s buy %s", fs.STT, f.STT)
	}
	// Trades up to 31 Mar 2026 keep the earlier rates: futures 0.02%
	// (335.53) and option premium 0.1% (14.625 → 14.63).
	fOld, err := s.Compute(Leg{Bucket: FOFut, Exchange: "NSE", Side: "S", Qty: 65, Price: money.MustDec("25810"), Date: "2026-03-31"})
	if err != nil || fOld.STT != amt("335.53") || !strings.HasSuffix(fOld.Rule, "/2024-10-01") {
		t.Fatalf("futures before 1 Apr 2026: %+v %v", fOld, err)
	}
	oOld, _ := s.Compute(Leg{Bucket: FOOpt, Exchange: "NSE", Side: "S", Qty: 130, Price: money.MustDec("112.50"), Date: "2026-03-31"})
	if oOld.STT != amt("14.63") {
		t.Fatalf("option before 1 Apr 2026: %+v", oOld)
	}
	if fNew, _ := s.Compute(Leg{Bucket: FOFut, Exchange: "NSE", Side: "S", Qty: 65, Price: money.MustDec("25810"), Date: "2026-04-01"}); fNew.STT != amt("838.83") {
		t.Fatalf("futures on 1 Apr 2026: %+v", fNew)
	}
	// Intraday small order: percent brokerage below the flat cap.
	i, _ := s.Compute(Leg{Bucket: CMIntraday, Exchange: "NSE", Side: "S", Qty: 10, Price: money.MustDec("400"), Date: "2026-10-16"})
	if i.Brokerage != amt("1.20") {
		t.Fatalf("intraday brokerage %s", i.Brokerage)
	}
}

func TestExchangeOverrideAndDates(t *testing.T) {
	s := load(t)
	nse, _ := s.Compute(Leg{Bucket: CMDelivery, Exchange: "NSE", Side: "S", Qty: 1000, Price: money.MustDec("100"), Date: "2026-10-16"})
	bse, _ := s.Compute(Leg{Bucket: CMDelivery, Exchange: "BSE", Side: "S", Qty: 1000, Price: money.MustDec("100"), Date: "2026-10-16"})
	if nse.ExchTxn != amt("2.97") || bse.ExchTxn != amt("3.75") {
		t.Fatalf("txn NSE %s BSE %s", nse.ExchTxn, bse.ExchTxn)
	}
	if _, err := s.Compute(Leg{Bucket: CMDelivery, Exchange: "NSE", Side: "B", Qty: 1, Price: money.MustDec("1"), Date: "2025-01-01"}); err == nil {
		t.Fatal("trade before schedule start should fail")
	}
	if _, err := s.Compute(Leg{Bucket: COMFut, Exchange: "NSE", Side: "B", Qty: 1, Price: money.MustDec("1"), Date: "2026-10-16"}); err == nil {
		t.Fatal("commodity futures on NSE has no rule in the sample and should fail")
	}
}

func TestRupeeRounding(t *testing.T) {
	s := load(t)
	for i := range s.Rules {
		s.Rules[i].STTRound = "rupee"
	}
	b, _ := s.Compute(Leg{Bucket: CMDelivery, Exchange: "NSE", Side: "B", Qty: 10, Price: money.MustDec("1412"), Date: "2026-10-16"})
	if b.STT != amt("14.00") {
		t.Fatalf("rupee-rounded STT %s", b.STT)
	}
}

func TestValidation(t *testing.T) {
	bad := `{"version":"x","sebi_fee_pct":"0","gst_pct":"18","rules":[
	 {"bucket":"CM-DEL","exchange":"*","effective_from":"2026-01-01","brokerage_mode":"none"},
	 {"bucket":"CM-DEL","exchange":"*","effective_from":"2026-06-01","brokerage_mode":"none"}]}`
	if _, err := LoadSchedule(strings.NewReader(bad)); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlap not caught: %v", err)
	}
	if _, err := LoadSchedule(strings.NewReader(`{"version":"x","typo":1}`)); err == nil {
		t.Fatal("unknown field should be rejected")
	}
	for _, c := range [][3]string{{"CM", "EQ", "CNC"}, {"CM", "EQ", "MIS"}, {"FO", "CE", "NRML"}, {"COM", "FUT", "NRML"}} {
		if _, err := BucketFor(c[0], c[1], c[2]); err != nil {
			t.Errorf("BucketFor%v: %v", c, err)
		}
	}
	if _, err := BucketFor("FO", "EQ", "NRML"); err == nil {
		t.Error("FO equity should not map")
	}
}
