package billing

import (
	"testing"

	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// Square-off: per client, exchange, scrip and settlement, the smaller of
// bought and sold is intraday, from the earliest trades; other segments and
// days are left alone.
func TestSplitSquareOff(t *testing.T) {
	tr := func(no, ucc, sym, side string, qty int64, tm, sett string) ingest.TradeEvent {
		return ingest.TradeEvent{Exchange: "NSE", Segment: "CM", TradeNo: no, UCC: ucc, Symbol: sym, Side: side, Qty: qty, Price: money.MustDec("10"), TradeDate: "2026-11-13", TradeTime: tm, Settlement: sett}
	}
	in := []ingest.TradeEvent{
		tr("1", "A", "X", "B", 50, "09:30", "N"),
		tr("2", "A", "X", "B", 50, "09:40", "N"),
		tr("3", "A", "X", "S", 70, "10:00", "N"),
		tr("4", "A", "X", "S", 10, "11:00", "A"), // another settlement: no buy there
		tr("5", "B", "X", "S", 10, "11:00", "N"), // another client
		{Exchange: "NSE", Segment: "FO", TradeNo: "6", UCC: "A", Symbol: "X", Side: "S", Qty: 75, TradeDate: "2026-11-13"},
	}
	out, split := SplitSquareOff(in, "2026-11-13")
	got := map[string]int64{}
	for _, t := range out {
		got[t.TradeNo+"/"+t.Product] += t.Qty
	}
	want := map[string]int64{"1/MIS": 50, "2/MIS": 20, "2/": 30, "3/MIS": 70, "4/": 10, "5/": 10, "6/": 75}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %d, want %d (all %v)", k, got[k], v, got)
		}
	}
	if !split["NSE|CM|2"] || len(split) != 1 {
		t.Errorf("split %v", split)
	}
}
