package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/billing"
	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

const bkDay = "2026-11-13"

// bkSetup makes clients, the day's market data and a dealer/online channel
// split (terminals 9* are online), intraday by square-off.
func bkSetup(t *testing.T, uccs ...string) (*Ops, func(ts ...ingest.TradeEvent) billing.Output) {
	t.Helper()
	o, ctx := newOps(t)
	for _, u := range uccs {
		docs.Upsert(ctx, o.Docs, T, cClient, u, Client{UCC: u, Name: "Client " + u, Status: "Active", Email: u + "@x.example"}, "t")
	}
	md := "Exchange,Segment,Symbol,Type,Expiry,Strike,ISIN,Company,Lot,LTP,Close,VaRPct,ELMPct,HaircutPct,SPANPct,ExposurePct,Delta,Underlying,Index\n" +
		"NSE,CM,RELIANCE,EQ,,,INE002A01018,Reliance,1,1000,1100,,,,,,,,\n" +
		"NSE,CM,TATAPOWER,EQ,,,INE245A01021,Tata Power,1,400,400,,,,,,,,\n" +
		"NSE,CM,PENNY,EQ,,,INE999Z01011,Penny,1,5,5,,,,,,,,\n" +
		"NSE,FO,NIFTY,CE,2026-11-25,26000,,,75,100,100,,,,10,2,0.5,25900,Y\n" +
		"NSE,FO,NIFTY,FUT,2026-11-25,,,,75,25900,25900,,,,10,2,1,25900,Y\n" +
		"NSE,FO,NIFTY,FUT,2026-12-30,,,,75,26000,26000,,,,10,2,1,25900,Y\n" +
		"NSE,FO,INFY,CE,2026-11-25,1500,,,400,20,20,,,,10,2,0.5,1480,\n" +
		"NSE,FO,INFY,FUT,2026-11-25,,,,400,1480,1480,,,,10,2,1,1480,\n"
	if _, err := o.LoadFile(ctx, T, "farhan.khan", "MARKET-DATA", bkDay, "md.csv", []byte(md)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveBrokerageSetup(ctx, T, "farhan.khan", BrokerageSetup{Channels: []string{"dealer", "online", "mobile"}, DefaultChannel: "dealer",
		Terminals: []TerminalChannel{{"9*", "online"}}, AuctionSettlements: []string{"A"}, IntradayBy: IntradayBySquareOff}); err != nil {
		t.Fatal(err)
	}
	bill := func(ts ...ingest.TradeEvent) billing.Output {
		t.Helper()
		for i := range ts {
			ts[i].Tenant, ts[i].Origin = T, ingest.FromExchange
			if ts[i].TradeDate == "" {
				ts[i].TradeDate = bkDay
			}
			if ts[i].Exchange == "" {
				ts[i].Exchange = "NSE"
			}
		}
		date := ts[0].TradeDate
		m, _, _ := o.Market(ctx, T, date)
		out, err := o.App.BuildBills(ctx, billing.Input{Tenant: T, Date: date, Actor: "t", Trades: ts, SettlementPrices: m.SettlementPrices(), Schedule: o.App.Schedule})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	return o, bill
}

func eq(no, order, ucc, sym, side string, qty int64, px, term string) ingest.TradeEvent {
	return ingest.TradeEvent{Segment: "CM", TradeNo: no, OrderNo: order, UCC: ucc, Symbol: sym, InstrumentType: "EQ", Side: side, Qty: qty, Price: money.MustDec(px), Terminal: term, TradeTime: "10:00:" + no[len(no)-2:]}
}

func fo(no, order, ucc, sym, typ, exp, strike, side string, qty int64, px, term string) ingest.TradeEvent {
	return ingest.TradeEvent{Segment: "FO", TradeNo: no, OrderNo: order, UCC: ucc, Symbol: sym, InstrumentType: typ, Expiry: exp, Strike: strike, Side: side, Qty: qty, Price: money.MustDec(px), Terminal: term}
}

// orderOf finds the line for an order (and product, for split orders).
func orderOf(t *testing.T, out billing.Output, order, product string) billing.OrderBrokerage {
	t.Helper()
	for _, x := range out.Orders {
		if x.Order == order && (product == "" || x.Product == product) {
			return x
		}
	}
	t.Fatalf("no order %s %s in %+v", order, product, out.Orders)
	return billing.OrderBrokerage{}
}

func wantBrok(t *testing.T, x billing.OrderBrokerage, amt, line string) {
	t.Helper()
	if x.Brokerage != money.MustAmount(amt) || x.Line != line {
		t.Errorf("order %s %s: ₹%s by %q, want ₹%s by %q", x.Order, x.Product, x.Brokerage, x.Line, amt, line)
	}
}

// The SharePro example basket: dealer vs online delivery and intraday
// (intraday found by square-off), options with per-lot minimums for index
// and stock options, auction trades, and a scrip-group line.
func TestSpecificBasketRates(t *testing.T) {
	o, bill := bkSetup(t, "SB1", "SB2")
	ctx := t.Context()
	d := money.MustDec
	type R = charges.BrokerageRule
	if _, err := o.SaveScripGroup(ctx, T, "farhan.khan", ScripGroup{Name: "SL00000217", Members: []GroupMember{{Scrip: "INE245A01021", From: "2026-04-01"}}}); err != nil {
		t.Fatal(err)
	}
	basket := map[string]R{
		"CM-DEL|NSE|*|*":                         {Mode: charges.PlanPct, Pct: d("0.20"), MinPerUnit: d("0.02")},
		"CM-DEL|NSE|online|*":                    {Mode: charges.PlanPct, Pct: d("0.08"), MinPerUnit: d("0.01")},
		"CM-INTRA|NSE|*|*":                       {Mode: charges.PlanPct, Pct: d("0.02")},
		"CM-INTRA|NSE|online|*":                  {Mode: charges.PlanPct, Pct: d("0.008")},
		"CM-DEL|NSE|*|auction":                   {Mode: charges.PlanPct, Pct: d("1")},
		"CM-DEL|NSE|online|auction":              {Mode: charges.PlanPct, Pct: d("0.5")},
		"FO-OPT|NSE|*|index":                     {Mode: charges.PlanPct, Pct: d("0.40"), MinPerLot: d("60")},
		"FO-OPT|NSE|*|stock":                     {Mode: charges.PlanPct, Pct: d("0.40"), MinPerLot: d("30")},
		"FO-OPT|NSE|online|index":                {Mode: charges.PlanPct, Pct: d("0.20"), MinPerLot: d("40")},
		"FO-OPT|NSE|online|stock":                {Mode: charges.PlanPct, Pct: d("0.20"), MinPerLot: d("20")},
		"CM-DEL|*|*|*|group:SL00000217":          {Mode: charges.PlanPct, Pct: d("0.002")},
		"CM-INTRA|*|*|*|group:SL00000217":        {Mode: charges.PlanPct, Pct: d("0.002")},
		"FO-OPT|*|*|*|group:NOPE":                {Mode: charges.PlanZero},
		"CM-DEL|NSE|online|*|scrip:INE002A01018": {Mode: charges.PlanPct, Pct: d("0.01")},
	}
	if _, err := o.SavePlan(ctx, T, "farhan.khan", PlanInput{ID: "basket-1", Name: "Basket 1", From: "2026-04-01", Rules: basket}); err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("a line for an unknown scrip group: %v", err)
	}
	delete(basket, "FO-OPT|*|*|*|group:NOPE")
	if _, err := o.SavePlan(ctx, T, "farhan.khan", PlanInput{ID: "basket-1", Name: "Basket 1", From: "2026-04-01", Rules: basket}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"SB1", "SB2"} {
		if _, err := o.Assign(ctx, T, "farhan.khan", "ucc:"+u, "basket-1", "2026-04-01"); err != nil {
			t.Fatal(err)
		}
	}
	out := bill(
		// SB1 (dealer) buys 100 RELIANCE at 1,000 and sells 60 at 1,010: 60
		// a side intraday, 40 delivery.
		eq("101", "O1", "SB1", "RELIANCE", "B", 100, "1000", "1001"),
		eq("102", "O2", "SB1", "RELIANCE", "S", 60, "1010", "1001"),
		// SB2 (online) buys 1,000 PENNY at ₹5: 0.08% = ₹4, 1 paisa a share = ₹10.
		eq("103", "O3", "SB2", "PENNY", "B", 1000, "5", "90001"),
		// SB1 buys 10 PENNY in auction: 1% (dealer).
		func() ingest.TradeEvent {
			x := eq("104", "O4", "SB1", "PENNY", "B", 10, "6", "1001")
			x.Settlement = "A"
			return x
		}(),
		// SB2 buys 100 TATAPOWER (in group SL00000217 by ISIN) at 400: 0.002% = ₹0.80.
		eq("105", "O5", "SB2", "TATAPOWER", "B", 100, "400", "90001"),
		// SB2 online buys 10 RELIANCE: the scrip's own line (by ISIN), 0.01%.
		eq("106", "O6", "SB2", "RELIANCE", "B", 10, "1000", "90001"),
		// SB1 dealer: 2 lots NIFTY CE at ₹100 = ₹15,000; 0.4% = ₹60, min ₹60 a lot = ₹120.
		fo("201", "F1", "SB1", "NIFTY", "CE", "2026-11-25", "26000", "B", 150, "100", "1001"),
		// SB2 online: 1 lot INFY CE at ₹20 = ₹8,000; 0.2% = ₹16, min ₹20 a lot.
		fo("202", "F2", "SB2", "INFY", "CE", "2026-11-25", "1500", "B", 400, "20", "90001"),
		// SB2 online: 1 lot NIFTY CE at ₹400 = ₹30,000; 0.2% = ₹60 over min ₹40.
		fo("203", "F3", "SB2", "NIFTY", "CE", "2026-11-25", "26000", "B", 75, "400", "90001"),
	)
	wantBrok(t, orderOf(t, out, "O1", "CM-INTRA"), "12", "CM-INTRA|NSE|*|*")    // 60 × 1,000 × 0.02%
	wantBrok(t, orderOf(t, out, "O2", "CM-INTRA"), "12.12", "CM-INTRA|NSE|*|*") // 60 × 1,010 × 0.02%
	wantBrok(t, orderOf(t, out, "O1", "CM-DEL"), "80", "CM-DEL|NSE|*|*")        // 40 × 1,000 × 0.2%
	wantBrok(t, orderOf(t, out, "O3", ""), "10", "CM-DEL|NSE|online|*")
	wantBrok(t, orderOf(t, out, "O4", ""), "0.60", "CM-DEL|NSE|*|auction")
	wantBrok(t, orderOf(t, out, "O5", ""), "0.80", "CM-DEL|*|*|*|group:SL00000217")
	wantBrok(t, orderOf(t, out, "O6", ""), "1", "CM-DEL|NSE|online|*|scrip:INE002A01018")
	wantBrok(t, orderOf(t, out, "F1", ""), "120", "FO-OPT|NSE|*|index")
	wantBrok(t, orderOf(t, out, "F2", ""), "20", "FO-OPT|NSE|online|stock")
	wantBrok(t, orderOf(t, out, "F3", ""), "60", "FO-OPT|NSE|online|index")
	if x := orderOf(t, out, "O5", ""); x.Plan != "Basket 1" || !strings.Contains(x.LineName, "group SL00000217") || x.Channel != "online" {
		t.Errorf("rule shown %+v", x)
	}
	// The split trade's brokerage adds up across its two parts (12 + 80).
	if out.TradeBrokerage["NSE|CM|101"] != money.MustAmount("92") {
		t.Errorf("split trade brokerage %s", out.TradeBrokerage["NSE|CM|101"])
	}
	// The same trades with intraday from the product column (none is MIS):
	// all delivery, as before.
	set, _ := o.BrokerageSetup(ctx, T)
	set.IntradayBy = ""
	if _, err := o.SaveBrokerageSetup(ctx, T, "farhan.khan", set); err != nil {
		t.Fatal(err)
	}
	out = bill(eq("101", "O1", "SB1", "RELIANCE", "B", 100, "1000", "1001"), eq("102", "O2", "SB1", "RELIANCE", "S", 60, "1010", "1001"))
	wantBrok(t, orderOf(t, out, "O1", ""), "200", "CM-DEL|NSE|*|*")
	// Preview and quote say which line applies.
	q, ok, err := o.QuoteLine(ctx, T, "SB2", "TATAPOWER", charges.RateQuery{Bucket: charges.CMDelivery, Exchange: "NSE", Channel: "online"}, bkDay)
	if err != nil || !ok || q.Line != "CM-DEL|*|*|*|group:SL00000217" {
		t.Errorf("quote %+v %v %v", q, ok, err)
	}
	q, _, _ = o.QuoteLine(ctx, T, "SB2", "INFY", charges.RateQuery{Bucket: charges.FOOpt, Exchange: "NSE", Channel: "online"}, bkDay)
	if q.Line != "FO-OPT|NSE|online|stock" {
		t.Errorf("quote stock option %+v", q)
	}
}

// Underlying lines (NIFTY options) beat index lines; index and stock
// futures; rollovers; physical delivery of stock options at expiry.
func TestSpecificDerivatives(t *testing.T) {
	o, bill := bkSetup(t, "SD1")
	ctx := t.Context()
	d := money.MustDec
	type R = charges.BrokerageRule
	if _, err := o.SavePlan(ctx, T, "farhan.khan", PlanInput{ID: "fo", Name: "FO", From: "2026-04-01", Rules: map[string]R{
		"FO-FUT|*|*|index":             {Mode: charges.PlanFlat, Flat: d("20")},
		"FO-FUT|*|*|stock":             {Mode: charges.PlanFlat, Flat: d("30")},
		"FO-FUT|*|*|rollover":          {Mode: charges.PlanFlat, Flat: d("5")},
		"FO-OPT|*|*|index":             {Mode: charges.PlanPerLot, PerLot: d("25")},
		"FO-OPT|*|*|*|und:NIFTY":       {Mode: charges.PlanPerLot, PerLot: d("10")},
		"FO-OPT":                       {Mode: charges.PlanPerLot, PerLot: d("15")},
		"FO-OPT|*|*|exercise":          {Mode: charges.PlanPct, Pct: d("0.1")},
		"FO-OPT|*|*|physical":          {Mode: charges.PlanPct, Pct: d("0.25"), Basis: charges.BasisContract},
		"FO-FUT|*|*|expiry":            {Mode: charges.PlanPerLot, PerLot: d("5")},
		"FO-FUT|*|*|physical|und:INFY": {Mode: charges.PlanPerLot, PerLot: d("7")},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Assign(ctx, T, "farhan.khan", "ucc:SD1", "fo", "2026-04-01"); err != nil {
		t.Fatal(err)
	}
	out := bill(
		fo("301", "A1", "SD1", "NIFTY", "FUT", "2026-11-25", "", "B", 75, "25900", "1001"), // index future, no roll
		fo("302", "A2", "SD1", "INFY", "FUT", "2026-11-25", "", "B", 400, "1480", "1001"),  // stock future
		fo("303", "A3", "SD1", "NIFTY", "CE", "2026-11-25", "26000", "B", 150, "100", "1001"),
		fo("304", "A4", "SD1", "INFY", "CE", "2026-11-25", "1500", "B", 400, "20", "1001"),
	)
	wantBrok(t, orderOf(t, out, "A1", ""), "20", "FO-FUT|*|*|index")
	wantBrok(t, orderOf(t, out, "A2", ""), "30", "FO-FUT|*|*|stock")
	wantBrok(t, orderOf(t, out, "A3", ""), "20", "FO-OPT|*|*|*|und:NIFTY") // the underlying beats "index options"
	wantBrok(t, orderOf(t, out, "A4", ""), "15", "FO-OPT")
	// A roll: sell November, buy December the same day: both legs at the rollover line.
	out = bill(
		fo("311", "R1", "SD1", "NIFTY", "FUT", "2026-11-25", "", "S", 75, "25900", "1001"),
		fo("312", "R2", "SD1", "NIFTY", "FUT", "2026-12-30", "", "B", 75, "26000", "1001"),
	)
	wantBrok(t, orderOf(t, out, "R1", ""), "5", "FO-FUT|*|*|rollover")
	wantBrok(t, orderOf(t, out, "R2", ""), "5", "FO-FUT|*|*|rollover")

	// Expiry: INFY (a stock) settles by delivery: the physical lines win
	// over expiry and exercise; NIFTY is an index: exercise.
	day := "2026-11-25"
	md := "Exchange,Segment,Symbol,Type,Expiry,Strike,ISIN,Company,Lot,LTP,Close,VaRPct,ELMPct,HaircutPct,SPANPct,ExposurePct,Delta,Underlying,Index\n" +
		"NSE,FO,NIFTY,CE,2026-11-25,26000,,,75,100,100,,,,10,2,0.5,26100,Y\n" +
		"NSE,FO,INFY,CE,2026-11-25,1500,,,400,20,20,,,,10,2,0.5,1520,\n" +
		"NSE,FO,INFY,FUT,2026-11-25,,,,400,1520,1520,,,,10,2,1,1520,\n"
	if _, err := o.LoadFile(ctx, T, "farhan.khan", "MARKET-DATA", day, "md.csv", []byte(md)); err != nil {
		t.Fatal(err)
	}
	pos := "UCC,Exchange,Segment,Symbol,Type,Expiry,Strike,Qty,AvgPrice\n" +
		"SD1,NSE,FO,NIFTY,CE,2026-11-25,26000,75,80\n" + // exercise: 0.1% × 26,100 × 75 = ₹1,957.50
		"SD1,NSE,FO,INFY,CE,2026-11-25,1500,400,15\n" + // physical, on the strike: 0.25% × 1,500 × 400 = ₹1,500
		"SD1,NSE,FO,INFY,FUT,2026-11-25,,400,1480\n" // physical on INFY: ₹7 a lot
	if _, err := o.LoadFile(ctx, T, "farhan.khan", "POSITIONS", day, "pos.csv", []byte(pos)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SettleDerivatives(ctx, T, "farhan.khan", day); err != nil {
		t.Fatal(err)
	}
	es, _ := o.App.Ledger.Store.List(ctx, T, ledgerFilterDate(day))
	var brok money.Amount
	for _, e := range es {
		if e.Source.Kind == "EXPBRK" && strings.Contains(e.Source.ID, "/SD1/") {
			for _, p := range e.Postings {
				if p.Account == "INCOME:BROKERAGE" {
					brok += p.Credit
				}
			}
		}
	}
	if brok != money.MustAmount("3464.50") {
		t.Fatalf("expiry brokerage %s", brok)
	}
}

// A client's own lines win over the plan's at equal detail; a scrip line
// beats everything less specific; a new plan version applies from its date;
// the market-rate basis; a minimum per contract note.
func TestSpecificOverridesDatesAndMinimums(t *testing.T) {
	o, bill := bkSetup(t, "SO1", "SO2")
	ctx := t.Context()
	d := money.MustDec
	type R = charges.BrokerageRule
	set, _ := o.BrokerageSetup(ctx, T)
	set.IntradayBy = ""
	o.SaveBrokerageSetup(ctx, T, "farhan.khan", set)
	if _, err := o.SavePlan(ctx, T, "farhan.khan", PlanInput{ID: "pp", Name: "P", From: "2026-04-01", Rules: map[string]R{
		"CM-DEL|NSE|*|*":               {Mode: charges.PlanPct, Pct: d("0.20"), MinPerNote: d("25")},
		"CM-DEL|*|*|*|scrip:TATAPOWER": {Mode: charges.PlanPct, Pct: d("0.05")},
		"CM-DEL|*|*|*|scrip:RELIANCE":  {Mode: charges.PlanPct, Pct: d("0.10"), Basis: charges.BasisMarket},
	}}); err != nil {
		t.Fatal(err)
	}
	// From 16-Nov delivery drops to 0.15%.
	if _, err := o.SavePlan(ctx, T, "farhan.khan", PlanInput{ID: "pp", Name: "P", From: "2026-11-16", Rules: map[string]R{
		"CM-DEL|NSE|*|*": {Mode: charges.PlanPct, Pct: d("0.15")},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"SO1", "SO2"} {
		o.Assign(ctx, T, "farhan.khan", "ucc:"+u, "pp", "2026-04-01")
	}
	if _, err := o.SaveOverride(ctx, T, "farhan.khan", OverrideInput{UCC: "SO2", From: "2026-04-01", Rules: map[string]R{"CM-DEL|NSE|*|*": {Mode: charges.PlanPct, Pct: d("0.05")}}}); err != nil {
		t.Fatal(err)
	}
	out := bill(
		eq("401", "B1", "SO1", "PENNY", "B", 10000, "5", "1001"),     // 0.2% of 50,000 = ₹100
		eq("402", "B2", "SO1", "TATAPOWER", "B", 100, "400", "1001"), // scrip line: 0.05% = ₹20
		eq("403", "B3", "SO1", "RELIANCE", "B", 10, "1000", "1001"),  // 0.1% on the close 1,100 × 10 = ₹11
		eq("404", "B4", "SO2", "PENNY", "B", 10000, "5", "1001"),     // client's own 0.05% = ₹25
		eq("405", "B5", "SO2", "TATAPOWER", "B", 100, "400", "1001"), // the plan's scrip line still wins over the client's general line
	)
	wantBrok(t, orderOf(t, out, "B1", ""), "100", "CM-DEL|NSE|*|*")
	wantBrok(t, orderOf(t, out, "B2", ""), "20", "CM-DEL|*|*|*|scrip:TATAPOWER")
	wantBrok(t, orderOf(t, out, "B3", ""), "11", "CM-DEL|*|*|*|scrip:RELIANCE")
	if x := orderOf(t, out, "B4", ""); x.Brokerage != money.MustAmount("25") || x.Plan != "P + client rate" {
		t.Errorf("client override %+v", x)
	}
	wantBrok(t, orderOf(t, out, "B5", ""), "20", "CM-DEL|*|*|*|scrip:TATAPOWER")
	// SO2's note: 25 + 20 = 45, above the ₹25 minimum per note. A small bill
	// is topped up to ₹25.
	out = bill(eq("411", "C1", "SO1", "PENNY", "B", 100, "5", "1001")) // 0.2% of 500 = ₹1
	if x := orderOf(t, out, "minimum per contract note", ""); x.Brokerage != money.MustAmount("24") {
		t.Errorf("note minimum %+v", x)
	}
	if out.Groups[0].Charges.Brokerage != money.MustAmount("25") || out.TradeBrokerage["NSE|CM|411"] != money.MustAmount("25") {
		t.Errorf("note minimum in the bill %+v %v", out.Groups[0].Charges, out.TradeBrokerage)
	}
	// After 16-Nov: 0.15%.
	x := eq("421", "D1", "SO1", "PENNY", "B", 10000, "5", "1001")
	x.TradeDate = "2026-11-16"
	out = bill(x)
	wantBrok(t, orderOf(t, out, "D1", ""), "75", "CM-DEL|NSE|*|*")
}

// SCRIP-GROUPS and cards with the new columns; problems are reported row
// by row and nothing is loaded.
func TestSpecificImport(t *testing.T) {
	o, bill := bkSetup(t, "SI1")
	ctx := t.Context()
	load := func(src, csv string) (ingest.Result, error) {
		return o.LoadFile(ctx, T, "farhan.khan", src, bkDay, strings.ToLower(src)+".csv", []byte(csv))
	}
	groups := "Group,Scrip,From,To,Note\nETF,NIFTYBEES,2026-04-01,,Exchange traded funds\nETF,GOLDBEES,2026-04-01,,\nSL00000217,INE245A01021,2026-04-01,2026-12-31,\n"
	if res, err := load("SCRIP-GROUPS", groups+"bad group!,X,,,\nETF,GOLDBEES,2026-13-01,,\n"); !errors.Is(err, ingest.ErrRejected) || len(res.Errors) != 2 || res.Errors[0].Line != 5 || res.Errors[1].Line != 6 {
		t.Fatalf("bad groups: %v %+v", err, res.Errors)
	}
	if res, err := load("SCRIP-GROUPS", groups); err != nil || res.Added != 2 {
		t.Fatalf("groups %+v %v", res, err)
	}
	if res, _ := load("SCRIP-GROUPS", groups); res.Added != 0 || res.DuplicateRows != 2 {
		t.Fatalf("loaded twice %+v", res)
	}
	head := "Card,Name,From,Product,Exchange,Channel,Case,Scrip,Group,Underlying,Mode,Pct,PerLot,MinPerShare,MinPerLot,MinPerNote,Basis\n"
	good := "B1,Basket 1,2026-04-01,CM-DEL,NSE,,,,,,pct,0.20,,0.02,,,\n" +
		"B1,Basket 1,2026-04-01,CM-DEL,,,,,SL00000217,,pct,0.002,,,,,\n" +
		"B1,Basket 1,2026-04-01,CM-DEL,,online,,RELIANCE,,,pct,0.08,,,,10,market\n" +
		"B1,Basket 1,2026-04-01,FO-OPT,NSE,online,index,,,,pct,0.20,,,40,,\n" +
		"B1,Basket 1,2026-04-01,FO-OPT,,,,,,NIFTY,per_lot,,10,,,,\n"
	bad := "B1,Basket 1,2026-04-01,FO-OPT,,,,RELIANCE,,,pct,0.1,,,,,\n" + // scrip on options: line 7
		"B1,Basket 1,2026-04-01,CM-DEL,,,,,NOGROUP,,pct,0.1,,,,,\n" + // unknown group: line 8
		"B1,Basket 1,2026-04-01,CM-DEL,,,,X,ETF,,pct,0.1,,,,,\n" + // scrip and group: line 9
		"B1,Basket 1,2026-04-01,CM-DEL,,,,,,,pct,0.1,,,,,weekly\n" + // basis: line 10
		"B1,Basket 1,2026-04-01,CM-DEL,,,,,,NIFTY,pct,0.1,,,,,\n" // underlying on cash: line 11
	res, err := load("BROKERAGE-CARDS", head+good+bad)
	if !errors.Is(err, ingest.ErrRejected) || len(res.Errors) != 5 {
		t.Fatalf("bad cards: %v %+v", err, res.Errors)
	}
	for i, e := range res.Errors {
		if e.Line != 7+i {
			t.Errorf("error %d on line %d: %s", i, e.Line, e.Msg)
		}
	}
	if !strings.Contains(res.Errors[1].Msg, "SCRIP-GROUPS") || !strings.Contains(res.Errors[0].Msg, "und:RELIANCE") {
		t.Errorf("messages %+v", res.Errors)
	}
	if ps, _ := o.Plans(ctx, T); func() bool {
		for _, p := range ps {
			if p.ID == "b1" {
				return true
			}
		}
		return false
	}() {
		t.Fatal("a rejected file loaded a card")
	}
	if res, err := load("BROKERAGE-CARDS", head+good); err != nil || res.Added != 1 {
		t.Fatalf("cards %+v %v", res, err)
	}
	ps, _ := o.Plans(ctx, T)
	var p Plan
	for _, x := range ps {
		if x.ID == "b1" {
			p = x
		}
	}
	r := p.Versions[0].Rules
	if len(r) != 5 || r["CM-DEL|*|online|*|scrip:RELIANCE"].Basis != charges.BasisMarket || r["CM-DEL|*|online|*|scrip:RELIANCE"].MinPerNote.Cmp(money.MustDec("10")) != 0 ||
		r["FO-OPT|*|*|*|und:NIFTY"].Mode != charges.PlanPerLot || r["CM-DEL|*|*|*|group:SL00000217"].Pct.Cmp(money.MustDec("0.002")) != 0 {
		t.Fatalf("card lines %+v", r)
	}
	if res, err := load("BROKERAGE-CLIENTS", "UCC,Card,From\nSI1,B1,2026-04-01\n"); err != nil || res.Added != 1 {
		t.Fatalf("assign %+v %v", res, err)
	}
	if res, err := load("BROKERAGE-CLIENT-RATES", "UCC,From,Product,Underlying,Mode,PerLot\nSI1,2026-04-01,FO-OPT,NIFTY,per_lot,8\n"); err != nil || res.Added != 1 {
		t.Fatalf("client rates %+v %v", res, err)
	}
	out := bill(fo("501", "N1", "SI1", "NIFTY", "CE", "2026-11-25", "26000", "B", 150, "100", "1001"))
	if x := orderOf(t, out, "N1", ""); x.Brokerage != money.MustAmount("16") || x.Line != "FO-OPT|*|*|*|und:NIFTY" || x.Plan != "Basket 1 + client rate" {
		t.Errorf("imported client line %+v", x)
	}
	// The group membership ends 31-Dec: from 1-Jan TATAPOWER is not in it.
	q, _, _ := o.QuoteLine(ctx, T, "SI1", "TATAPOWER", charges.RateQuery{Bucket: charges.CMDelivery, Exchange: "NSE"}, "2027-01-04")
	if q.Line != "CM-DEL|NSE|*|*" {
		t.Errorf("after the group ends %+v", q)
	}
}

// Interest by client: own rate, own grace days, no interest, a rate change
// mid-month; monthly and daily posting; narrations show the rate.
func TestClientInterestRates(t *testing.T) {
	o, ctx := newOps(t)
	uccs := []string{"CI1", "CI2", "CI3", "CI4", "CI5"}
	for _, u := range uccs {
		docs.Upsert(ctx, o.Docs, T, cClient, u, Client{UCC: u, Name: "Client " + u, Status: "Active", Email: u + "@x.example"}, "t")
		// ₹1,00,000 billed 30-Sep: overdue from 1-Oct.
		if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "B:" + u, ValueDate: "2026-09-30", Narration: "Bill", Source: ledger.SourceRef{Kind: "BILL", ID: "x" + u}, CreatedBy: "admin",
			Postings: []ledger.Posting{{Account: ledger.ClientAccount(u), Debit: money.MustAmount("100000")}, {Account: ledger.OpeningEquity, Credit: money.MustAmount("100000")}}}); err != nil {
			t.Fatal(err)
		}
	}
	set, _ := o.Settings(ctx, T)
	set.InterestPctPA, set.InterestGraceDays, set.InterestMinimum, set.InterestExemptTags = "24", 0, "0", nil
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	save := func(u string, in InterestTerms) {
		t.Helper()
		if _, err := o.SaveClientInterest(ctx, T, "farhan.khan", ClientInterestInput{UCC: u, InterestTerms: in}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.SaveClientInterest(ctx, T, "priya.nair", ClientInterestInput{UCC: "CI1", InterestTerms: InterestTerms{From: "2026-04-01", RatePct: "15"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker set a client's interest: %v", err)
	}
	save("CI1", InterestTerms{From: "2026-04-01", RatePct: "15"})
	save("CI3", InterestTerms{From: "2026-10-16", RatePct: "17%"})
	five := int64(5)
	save("CI5", InterestTerms{From: "2026-04-01", GraceDays: &five})
	// CI4 from the import: no interest.
	if res, err := o.LoadFile(ctx, T, "farhan.khan", "CLIENT-INTEREST", "", "ci.csv", []byte("UCC,From,Rate,GraceDays,Free,Note\nCI4,2026-04-01,,,Y,Staff\nNOPE,2026-04-01,12,,,\nCI4,2026-04-01,,,maybe,\n")); !errors.Is(err, ingest.ErrRejected) || len(res.Errors) != 2 {
		t.Fatalf("bad interest file %v %+v", err, res.Errors)
	}
	if res, err := o.LoadFile(ctx, T, "farhan.khan", "CLIENT-INTEREST", "", "ci.csv", []byte("UCC,From,Rate,GraceDays,Free,Note\nCI4,2026-04-01,,,Y,Staff\n")); err != nil || res.Added != 1 {
		t.Fatalf("interest file %+v %v", res, err)
	}

	// One day: ₹1,00,000 at 15% = 41.10, at 24% = 65.75.
	iv, err := o.Interest(ctx, T, "2026-10-01", "2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]InterestRow{}
	for _, r := range iv.Rows {
		rows[r.UCC] = r
	}
	if rows["CI1"].Interest != money.MustAmount("41.10") || rows["CI1"].RatePct != "15" || !rows["CI1"].OwnRate {
		t.Errorf("CI1 %+v", rows["CI1"])
	}
	if rows["CI2"].Interest != money.MustAmount("65.75") || rows["CI2"].RatePct != "24" || rows["CI2"].OwnRate {
		t.Errorf("CI2 %+v", rows["CI2"])
	}
	// The month: CI3 at 24% for 15 days and 17% for 16 days; CI4 none; CI5
	// after 5 grace days, 26 days at 24%.
	iv, _ = o.Interest(ctx, T, "2026-10-01", "2026-10-31")
	rows = map[string]InterestRow{}
	for _, r := range iv.Rows {
		rows[r.UCC] = r
	}
	if r := rows["CI3"]; r.Interest != money.MustAmount("1731.51") || r.RatePct != "24% to 15-Oct, 17% from 16-Oct" {
		t.Errorf("CI3 %+v", r)
	}
	if r := rows["CI4"]; r.Status != "No interest (client)" || r.Interest != 0 {
		t.Errorf("CI4 %+v", r)
	}
	if r := rows["CI5"]; r.DebitDays != 26 || r.Interest != money.MustAmount("1709.59") {
		t.Errorf("CI5 %+v", r)
	}
	if _, n, err := o.PostInterest(ctx, T, "farhan.khan", "2026-10-01", "2026-10-31"); err != nil || n < 4 {
		t.Fatalf("posted %d %v", n, err)
	}
	es, _ := o.App.Ledger.Store.List(ctx, T, ledger.Filter{From: "2026-10-31", To: "2026-10-31"})
	nar := map[string]string{}
	for _, e := range es {
		if e.Source.Kind == "INTEREST" {
			nar[strings.Split(e.Source.ID, "/")[2]] = e.Narration
		}
	}
	if !strings.Contains(nar["CI1"], "@ 15% p.a.") || !strings.Contains(nar["CI2"], "@ 24% p.a.") || !strings.Contains(nar["CI3"], "@ 24% p.a. to 15-Oct, 17% p.a. from 16-Oct") || nar["CI4"] != "" {
		t.Errorf("narrations %v", nar)
	}
	// The debtors screen and the client page show the terms.
	dv, _ := o.Debits(ctx, T, "2026-10-20")
	for _, r := range dv.Rows {
		if r.UCC == "CI1" && (r.InterestRate != "15% p.a. (client's own)" || !r.OwnInterest) {
			t.Errorf("debtors row %+v", r)
		}
	}
	cv, err := o.ClientInterestOn(ctx, T, "CI3", "2026-10-10")
	if err != nil || cv.RatePct != "24" || cv.Own {
		t.Errorf("CI3 before %+v %v", cv, err)
	}
	if cv, _ = o.ClientInterestOn(ctx, T, "CI3", "2026-10-20"); cv.RatePct != "17" || !cv.Own || cv.Since != "2026-10-16" || len(cv.History) != 1 {
		t.Errorf("CI3 after %+v", cv)
	}
	// Ending a client's terms: the firm's apply again.
	save("CI1", InterestTerms{From: "2026-11-01"})
	if cv, _ = o.ClientInterestOn(ctx, T, "CI1", "2026-11-02"); cv.RatePct != "24" || cv.Own {
		t.Errorf("CI1 after ending %+v", cv)
	}
}

// Daily posting charges each day at the client's rate in force that day.
func TestClientInterestDaily(t *testing.T) {
	o, ctx := newOps(t)
	for _, u := range []string{"CD1", "CD2"} {
		docs.Upsert(ctx, o.Docs, T, cClient, u, Client{UCC: u, Name: "Client " + u, Status: "Active", Email: u + "@x.example"}, "t")
		if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "B:" + u, ValueDate: "2026-09-30", Narration: "Bill", Source: ledger.SourceRef{Kind: "BILL", ID: "x" + u}, CreatedBy: "admin",
			Postings: []ledger.Posting{{Account: ledger.ClientAccount(u), Debit: money.MustAmount("100000")}, {Account: ledger.OpeningEquity, Credit: money.MustAmount("100000")}}}); err != nil {
			t.Fatal(err)
		}
	}
	set, _ := o.Settings(ctx, T)
	set.InterestPctPA, set.InterestGraceDays, set.InterestMinimum, set.InterestExemptTags = "24", 0, "0", nil
	set.InterestPosting, set.InterestDailyFrom = InterestDaily, "2026-10-01"
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveClientInterest(ctx, T, "farhan.khan", ClientInterestInput{UCC: "CD1", InterestTerms: InterestTerms{From: "2026-04-01", RatePct: "15"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveClientInterest(ctx, T, "farhan.khan", ClientInterestInput{UCC: "CD1", InterestTerms: InterestTerms{From: "2026-10-02", RatePct: "21"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.PostDailyInterest(ctx, T, "farhan.khan", "2026-10-02"); err != nil {
		t.Fatal(err)
	}
	es, _ := o.App.Ledger.Store.List(ctx, T, ledger.Filter{From: "2026-10-01", To: "2026-10-02"})
	got := map[string]string{}
	for _, e := range es {
		if e.Source.Kind != "INTEREST" {
			continue
		}
		u := strings.Split(e.Source.ID, "/")[2]
		for _, p := range e.Postings {
			if p.Account == IncomeInterest {
				got[u+" "+e.ValueDate] = p.Credit.String() + " " + e.Narration[strings.Index(e.Narration, "@"):strings.Index(e.Narration, " (")]
			}
		}
	}
	// CD1 day 1 at 15%: 41.10 (GST 7.40); day 2 at 21% on 1,00,048.50 = 57.56.
	for k, w := range map[string]string{"CD1 2026-10-01": "41.10 @ 15% p.a.", "CD1 2026-10-02": "57.56 @ 21% p.a.", "CD2 2026-10-01": "65.75 @ 24% p.a."} {
		if got[k] != w {
			t.Errorf("%s: %q, want %q", k, got[k], w)
		}
	}
	if n, err := o.IssueDailyInterestInvoices(ctx, T, "farhan.khan", "2026-10"); err != nil || n < 2 {
		t.Fatalf("invoices %d %v", n, err)
	}
	invs, _ := o.Invoices(ctx, T, "CD1")
	if len(invs) != 1 || !strings.Contains(invs[0].Description, "@ 15% p.a. to 01-Oct, 21% p.a. from 02-Oct") {
		t.Fatalf("invoice %+v", invs)
	}
}
