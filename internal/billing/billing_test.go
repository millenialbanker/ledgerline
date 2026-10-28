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

// Rollover and auction orders are priced by their own lines; a rollover is
// both legs of a move to a later expiry on the same day.
func TestCasesPriceOrders(t *testing.T) {
	d := money.MustDec
	rules := map[string]charges.BrokerageRule{
		"FO-FUT":              {Mode: charges.PlanFlat, Flat: d("20")},
		"FO-FUT|*|*|rollover": {Mode: charges.PlanFlat, Flat: d("10")},
		"CM-DEL":              {Mode: charges.PlanPct, Pct: d("0.3")},
		"CM-DEL|*|*|auction":  {Mode: charges.PlanPct, Pct: d("0.5")},
		"CM-DEL|*|online|*":   {Mode: charges.PlanPct, Pct: d("0.1")},
	}
	trade := func(no, ucc, side, expiry, px string, qty int64) ingest.TradeEvent {
		e := ev(ucc, "FO", "FUT", side, qty, px, "NRML")
		e.TradeNo, e.OrderNo, e.Expiry = no, "O"+no, expiry
		return e
	}
	near, far := "2026-10-27", "2026-11-24"
	cash := func(no, settle, term string) ingest.TradeEvent {
		e := ev("R2", "CM", "EQ", "B", 100, "100", "CNC")
		e.TradeNo, e.OrderNo, e.Settlement, e.Terminal = no, "O"+no, settle, term
		return e
	}
	in := Input{Tenant: "t1", Date: "2026-10-16", Actor: "eod", Schedule: schedule(t),
		SettlementPrices: map[string]money.Dec{"NIFTY|FUT|" + near + "|": d("25800"), "NIFTY|FUT|" + far + "|": d("25900")},
		Trades: []ingest.TradeEvent{
			trade("1", "R1", "S", near, "25800", 75), // roll: sell near, buy far
			trade("2", "R1", "B", far, "25900", 75),
			trade("3", "R3", "B", near, "25800", 75), // a plain buy
			cash("4", "N", "1001"),
			cash("5", "A", "1001"),
			cash("6", "N", "9001"),
		},
		Brokerage: func(ucc string, q charges.RateQuery, date string) (charges.BrokerageRule, string, bool) {
			r, _, ok := charges.Lookup(rules, q)
			return r, "card", ok
		},
		Classify: func(t ingest.TradeEvent) (string, []string) {
			ch := "dealer"
			if t.Terminal == "9001" {
				ch = "online"
			}
			if t.Settlement == "A" {
				return ch, []string{charges.CaseAuction}
			}
			return ch, nil
		},
	}
	out, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, g := range out.Groups {
		got[g.UCC+"-"+g.Segment] = g.Charges.Brokerage.String()
	}
	// R1: two rollover legs at ₹10. R3: ₹20. R2: 0.3% + 0.5% + 0.1% of ₹10,000 = 30 + 50 + 10.
	for k, w := range map[string]string{"R1-FO": "20.00", "R3-FO": "20.00", "R2-CM": "90.00"} {
		if got[k] != w {
			t.Errorf("%s brokerage %s, want %s (%v)", k, got[k], w, got)
		}
	}
	if r := Rollovers(in.Trades); !r[TradeKey(in.Trades[0])] || !r[TradeKey(in.Trades[1])] || r[TradeKey(in.Trades[2])] {
		t.Errorf("rollovers %v", r)
	}
}
