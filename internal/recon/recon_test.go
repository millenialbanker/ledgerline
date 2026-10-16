package recon

import (
	"testing"

	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

func tr(no, ucc, sym string, side string, qty int64, px string, tm string) ingest.TradeEvent {
	return ingest.TradeEvent{Origin: ingest.FromExchange, Exchange: "NSE", Segment: "CM", TradeNo: no, UCC: ucc, Symbol: sym, InstrumentType: "EQ", Side: side, Qty: qty, Price: money.MustDec(px), TradeDate: "2026-10-16", TradeTime: tm}
}

func oms(e ingest.TradeEvent, f func(*ingest.TradeEvent)) ingest.TradeEvent {
	e.Origin = ingest.FromOMS
	if f != nil {
		f(&e)
	}
	return e
}

func TestClassification(t *testing.T) {
	ex := []ingest.TradeEvent{
		tr("1", "A", "RELIANCE", "B", 10, "1412", "09:21:14"),
		tr("2", "B", "INFY", "S", 5, "1500", "09:22:00"),
		tr("3", "C", "TCS", "B", 8, "3080", "09:23:00"),
		tr("4", "D", "ITC", "S", 300, "412.2", "09:24:00"),
		tr("5", "E", "LT", "B", 5, "3750.5", "09:25:00"),
		tr("6", "F", "SBIN", "B", 1, "868", "09:26:00"),
	}
	ex[5].AlgoID = "ALG1"
	om := []ingest.TradeEvent{
		oms(ex[0], nil),
		oms(ex[1], func(o *ingest.TradeEvent) { o.Qty = 6; o.Price = money.MustDec("1501") }), // qty wins over price
		oms(ex[2], func(o *ingest.TradeEvent) { o.UCC = "Z" }),
		oms(ex[4], func(o *ingest.TradeEvent) { o.TradeNo = "99"; o.TradeTime = "09:25:01" }), // same trade, other number
		oms(ex[5], func(o *ingest.TradeEvent) { o.AlgoID = "" }),
		oms(tr("7", "G", "HDFCBANK", "B", 1, "1000", "10:00:00"), nil),
	}
	r := Run("2026-10-16", ex, om, Options{})
	if r.Matched != 1 || r.ExchangeCount != 6 || r.OMSCount != 6 {
		t.Fatalf("counts %+v", r)
	}
	want := map[BreakType]int{QtyMismatch: 1, UCCMismatch: 1, MissingInOMS: 1, TradeNoMismatch: 1, AlgoIDMissing: 1, MissingInExchange: 1}
	for k, v := range want {
		if r.ByType[k] != v {
			t.Errorf("%s = %d, want %d (all: %v)", k, r.ByType[k], v, r.ByType)
		}
	}
	for _, b := range r.Breaks {
		if b.Type == QtyMismatch && len(b.Diffs) != 2 {
			t.Errorf("qty break should carry both diffs, got %v", b.Diffs)
		}
		if b.Type == TradeNoMismatch && b.OMSTradeNo != "99" {
			t.Errorf("trade-no break %+v", b)
		}
		if b.Suggestion == "" || b.ID == "" {
			t.Errorf("break missing id or suggestion: %+v", b)
		}
	}
	// Deterministic IDs across runs.
	r2 := Run("2026-10-16", ex, om, Options{})
	for i := range r.Breaks {
		if r.Breaks[i].ID != r2.Breaks[i].ID {
			t.Fatal("break IDs are not stable")
		}
	}
}

func TestToleranceAndWindow(t *testing.T) {
	e := tr("1", "A", "X", "B", 1, "100.00", "09:00:00")
	o := oms(e, func(o *ingest.TradeEvent) { o.Price = money.MustDec("100.04") })
	if r := Run("2026-10-16", []ingest.TradeEvent{e}, []ingest.TradeEvent{o}, Options{PriceTolerance: money.MustDec("0.05")}); r.Matched != 1 {
		t.Fatalf("within tolerance should match: %+v", r.Breaks)
	}
	far := oms(e, func(o *ingest.TradeEvent) { o.TradeNo = "2"; o.TradeTime = "09:00:10" })
	r := Run("2026-10-16", []ingest.TradeEvent{e}, []ingest.TradeEvent{far}, Options{})
	if r.ByType[MissingInOMS] != 1 || r.ByType[MissingInExchange] != 1 {
		t.Fatalf("outside fuzzy window should be two breaks: %v", r.ByType)
	}
}
