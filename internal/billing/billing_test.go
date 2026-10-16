package billing

import (
	"errors"
	"os"
	"testing"

	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

func schedule(t *testing.T) charges.Schedule {
	f, err := os.Open("../../config/charges/sample-schedule.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := charges.LoadSchedule(f)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ev(ucc, seg, it, side string, qty int64, px, product string) ingest.TradeEvent {
	e := ingest.TradeEvent{Tenant: "t1", Origin: ingest.FromExchange, Exchange: "NSE", Segment: seg, TradeNo: ucc + side + px, UCC: ucc, Symbol: "NIFTY", InstrumentType: it, Side: side, Qty: qty, Price: money.MustDec(px), TradeDate: "2026-10-16", Product: product}
	if it != "EQ" {
		e.Expiry = "2026-10-27"
	} else {
		e.Symbol = "RELIANCE"
	}
	return e
}

func TestBuildBalancedBills(t *testing.T) {
	in := Input{Tenant: "t1", Date: "2026-10-16", Actor: "eod", Schedule: schedule(t),
		SettlementPrices: map[string]money.Dec{"NIFTY|FUT|2026-10-27|": money.MustDec("25840.50")},
		Trades: []ingest.TradeEvent{
			ev("BH1001", "CM", "EQ", "B", 10, "1412.00", "CNC"),
			ev("BH1001", "FO", "FUT", "B", 65, "25810.00", "NRML"),
		}}
	out, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 2 || len(out.Groups) != 2 {
		t.Fatalf("entries %d groups %d", len(out.Entries), len(out.Groups))
	}
	l := ledger.New(ledger.NewMemStore())
	for _, e := range out.Entries {
		if _, _, err := l.Post(t.Context(), e); err != nil {
			t.Fatalf("post %s: %v", e.Key, err)
		}
	}
	bal, _ := l.Balances(t.Context(), "t1", "")
	// CM: debit 14120 + 16.75 charges. FO: MTM credit (25840.5-25810)×65 = 1982.50, charges 93.38.
	want := money.MustAmount("14136.75") - money.MustAmount("1982.50") + money.MustAmount("93.38")
	if got := bal[ledger.ClientAccount("BH1001")]; got != want {
		t.Fatalf("client balance %s, want %s", got, want)
	}
	if bal[ledger.IncomeBrokerage] != -money.MustAmount("20.00") || bal[ledger.PayableSTT] != -money.MustAmount("14.12") {
		t.Fatalf("income/payables wrong: %v", bal)
	}
	cm := out.Groups[0]
	if cm.Segment != "CM" || cm.Net != -money.MustAmount("14136.75") {
		t.Fatalf("CM group %+v", cm)
	}
}

func TestMissingSettlementPrice(t *testing.T) {
	_, err := Build(Input{Tenant: "t1", Date: "2026-10-16", Actor: "eod", Schedule: schedule(t),
		Trades: []ingest.TradeEvent{ev("BH1001", "FO", "FUT", "B", 65, "25810.00", "NRML")}})
	if !errors.Is(err, ErrMissingSettlementPrice) {
		t.Fatalf("want missing price error, got %v", err)
	}
}

func TestRejectsOMSTrades(t *testing.T) {
	e := ev("BH1001", "CM", "EQ", "B", 1, "1", "CNC")
	e.Origin = ingest.FromOMS
	if _, err := Build(Input{Tenant: "t1", Date: "2026-10-16", Actor: "eod", Schedule: schedule(t), Trades: []ingest.TradeEvent{e}}); err == nil {
		t.Fatal("OMS trades must not be billed")
	}
}

func TestIntradayNetsOut(t *testing.T) {
	out, err := Build(Input{Tenant: "t1", Date: "2026-10-16", Actor: "eod", Schedule: schedule(t), Trades: []ingest.TradeEvent{
		ev("BH1003", "CM", "EQ", "B", 40, "1512.80", "MIS"),
		ev("BH1003", "CM", "EQ", "S", 40, "1519.10", "MIS"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if g := out.Groups[0]; g.TradeNet != money.MustAmount("252.00") || g.Trades != 2 {
		t.Fatalf("intraday group %+v", g)
	}
}
