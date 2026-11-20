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

// The pilot's rule, case by case: bought and sold the same day in the same
// scrip, per exchange and per settlement, earliest trades first by trade
// time (not by trade number), and trades of the same second by trade number
// as a number.
func TestSplitSquareOffRule(t *testing.T) {
	tr := func(exch, no, side string, qty int64, tm, sett string) ingest.TradeEvent {
		return ingest.TradeEvent{Exchange: exch, Segment: "CM", TradeNo: no, UCC: "C1", Symbol: "SCRIPA", Side: side, Qty: qty, Price: money.MustDec("10"), TradeDate: "2026-11-13", TradeTime: tm, Settlement: sett}
	}
	run := func(in ...ingest.TradeEvent) map[string]int64 {
		out, _ := SplitSquareOff(in, "2026-11-13")
		got := map[string]int64{}
		for _, x := range out {
			got[x.Exchange+"/"+x.TradeNo+"/"+x.Product] += x.Qty
		}
		return got
	}
	check := func(name string, got, want map[string]int64) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
			return
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %d, want %d (all %v)", name, k, got[k], v, got)
			}
		}
	}
	// Bought on NSE, sold on BSE: no square-off across exchanges.
	check("exchanges", run(tr("NSE", "1", "B", 100, "09:20:00", "N"), tr("BSE", "2", "S", 100, "09:30:00", "N")),
		map[string]int64{"NSE/1/": 100, "BSE/2/": 100})
	// Bought in the normal settlement, sold in another settlement type the
	// same day: no square-off across settlements.
	check("settlements", run(tr("NSE", "1", "B", 100, "09:20:00", "N"), tr("NSE", "2", "S", 100, "09:30:00", "W")),
		map[string]int64{"NSE/1/": 100, "NSE/2/": 100})
	// Earliest first by trade time: trade 900 at 09:15 comes before trade
	// 100 at 11:00, whatever the trade numbers say.
	check("fifo by time", run(
		tr("NSE", "100", "B", 60, "11:00:00", "N"),
		tr("NSE", "900", "B", 60, "09:15:00", "N"),
		tr("NSE", "500", "S", 80, "12:00:00", "N")),
		map[string]int64{"NSE/900/MIS": 60, "NSE/100/MIS": 20, "NSE/100/": 40, "NSE/500/MIS": 80})
	// The same second: trade 9 before trade 10 (numbers, not text).
	check("same second", run(
		tr("NSE", "10", "B", 50, "10:00:00", "N"),
		tr("NSE", "9", "B", 50, "10:00:00", "N"),
		tr("NSE", "11", "S", 50, "10:05:00", "N")),
		map[string]int64{"NSE/9/MIS": 50, "NSE/10/": 50, "NSE/11/MIS": 50})
	// Both exchanges at once, each squared off on its own.
	check("each exchange", run(
		tr("NSE", "1", "B", 100, "09:20:00", "N"), tr("NSE", "2", "S", 40, "09:30:00", "N"),
		tr("BSE", "1", "B", 30, "09:25:00", "N"), tr("BSE", "2", "S", 70, "09:35:00", "N")),
		map[string]int64{"NSE/1/MIS": 40, "NSE/1/": 60, "NSE/2/MIS": 40, "BSE/1/MIS": 30, "BSE/2/MIS": 30, "BSE/2/": 40})
	if !tradeNoLess("9", "10") || tradeNoLess("10", "9") || !tradeNoLess("0009", "10") || !tradeNoLess("A1", "A2") {
		t.Error("trade number order")
	}
}
