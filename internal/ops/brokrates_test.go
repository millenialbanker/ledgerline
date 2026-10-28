package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// Cards with rate lines by exchange and channel, imported with history for
// clients of any status; a card for one exchange only; a client's own
// rates; terminals mapped to channels; loading the files twice changes
// nothing.
func TestBrokerageImportsAndLines(t *testing.T) {
	o, ctx := newOps(t)
	for u, st := range map[string]string{"RC1": "Active", "RC2": "Suspended", "RC3": "Inactive"} {
		docs.Upsert(ctx, o.Docs, T, cClient, u, Client{UCC: u, Name: "Client " + u, Status: st, Email: u + "@x.example"}, "t")
	}
	load := func(actor, src, csv string) (ingest.Result, error) {
		t.Helper()
		return o.LoadFile(ctx, T, actor, src, "2026-10-16", strings.ToLower(src)+".csv", []byte(csv))
	}
	must := func(src, csv string, added, same int) {
		t.Helper()
		res, err := load("farhan.khan", src, csv)
		if err != nil || res.Added != added || res.DuplicateRows != same {
			t.Fatalf("%s: added %d same %d, want %d/%d: %v %v", src, res.Added, res.DuplicateRows, added, same, err, res.Errors)
		}
	}
	cards := "Card,Name,From,Product,Exchange,Channel,Case,Mode,Pct,Flat,PerLot,PerShare,MinPerShare,MinPerLot,Min,Max\n" +
		"2,Retail 2,2026-04-01,CM-DEL,,,,pct,0.30,,,,0.03,,,\n" +
		"2,Retail 2,2026-04-01,CM-DEL,BSE,,,pct,0.25,,,,,,,\n" +
		"2,Retail 2,2026-04-01,CM-DEL,,online,,pct,0.10,,,,,,,\n" +
		"2,Retail 2,2026-04-01,CM-INTRA,,,,per_share,,,,0.02,,,,\n" +
		"2,Retail 2,2026-04-01,FO-OPT,,,,per_lot,,,20,,,,,\n" +
		"2,Retail 2,2026-04-01,FO-OPT,,,exercise,pct,0.10,,,,,,,\n" +
		"2,Retail 2,2026-10-01,CM-DEL,,,,pct,0.35,,,,0.03,,,\n" +
		"2,Retail 2,2026-10-01,CM-DEL,BSE,,,pct,0.25,,,,,,,\n" +
		"2,Retail 2,2026-10-01,CM-DEL,,online,,pct,0.10,,,,,,,\n" +
		"2,Retail 2,2026-10-01,CM-INTRA,,,,per_share,,,,0.02,,,,\n" +
		"2,Retail 2,2026-10-01,FO-OPT,,,,per_lot,,,20,,,,,\n" +
		"2,Retail 2,2026-10-01,FO-OPT,,,exercise,pct,0.10,,,,,,,\n" +
		"NSE SPL,NSE special,2026-04-01,Equity delivery,NSE,,,pct,0.20,,,,,,,\n"
	if _, err := load("priya.nair", "BROKERAGE-CARDS", cards); !errors.Is(err, ingest.ErrRejected) {
		t.Fatalf("a maker loaded rate cards: %v", err)
	}
	if _, err := load("farhan.khan", "BROKERAGE-CARDS", cards+"2,Retail 2,2026-04-01,CM-DEL,,,rollover,pct,0.1,,,,,,,\n"); err == nil {
		t.Fatal("rollover on delivery accepted")
	}
	must("BROKERAGE-CARDS", cards, 3, 0)
	must("BROKERAGE-CARDS", cards, 0, 3)
	ps, _ := o.Plans(ctx, T)
	found := false
	for _, p := range ps {
		if p.ID == "card-2" && p.Name == "Retail 2" && len(p.Versions) == 2 && len(p.Versions[1].Rules) == 6 {
			found = true
		}
	}
	if !found {
		t.Fatalf("card-2 not imported with two versions: %+v", ps)
	}

	clients := "UCC,Card,From,Segment,Exchange\n" +
		"RC1,Retail 2,2026-04-01,,\n" +
		"RC2,2,2026-04-01,,\n" + // by the old code
		"RC3,card-2,2026-04-01,,\n" + // by the plan ID
		"RC1,NSE special,2026-10-10,CM,NSE\n"
	if _, err := load("farhan.khan", "BROKERAGE-CLIENTS", clients+"NOPE,2,2026-04-01,,\n"); err == nil {
		t.Fatal("unknown client accepted")
	}
	must("BROKERAGE-CLIENTS", clients, 4, 0)
	must("BROKERAGE-CLIENTS", clients, 0, 4)
	must("BROKERAGE-CLIENT-RATES", "UCC,From,Product,Exchange,Channel,Case,Mode,Pct,PerShare\nRC1,2026-10-01,CM-INTRA,,,,per_share,,0.01\n", 1, 0)
	must("TERMINAL-CHANNELS", "Terminal,Channel\n9*,online\n7*,mobile\n", 2, 0)
	setup, _ := o.BrokerageSetup(ctx, T)
	if setup.channelOf("90012") != "online" || setup.channelOf("70001") != "mobile" || setup.channelOf("1001") != "dealer" {
		t.Fatalf("terminal channels %+v", setup)
	}

	q := func(ucc string, b charges.Bucket, exch, ch, date string, cases ...string) string {
		t.Helper()
		r, name, ok, err := o.QuoteRule(ctx, T, ucc, charges.RateQuery{Bucket: b, Exchange: exch, Channel: ch, Cases: cases}, date)
		if err != nil || !ok {
			return "none"
		}
		return name + " " + r.Describe()
	}
	for _, c := range []struct{ got, want string }{
		{q("RC1", charges.CMDelivery, "NSE", "dealer", "2026-10-16"), "NSE special 0.2%"},                    // the NSE card
		{q("RC1", charges.CMDelivery, "NSE", "dealer", "2026-10-05"), "Retail 2 0.35%, min ₹0.03 per share"}, // before it
		{q("RC1", charges.CMDelivery, "BSE", "dealer", "2026-10-16"), "Retail 2 0.25%"},
		{q("RC1", charges.CMDelivery, "BSE", "online", "2026-10-16"), "Retail 2 0.1%"}, // channel beats exchange
		{q("RC1", charges.CMIntraday, "NSE", "", "2026-10-16"), "Retail 2 + client rate ₹0.01 per share"},
		{q("RC2", charges.CMDelivery, "NSE", "", "2026-09-01"), "Retail 2 0.3%, min ₹0.03 per share"}, // suspended, old version
		{q("RC3", charges.FOOpt, "NSE", "", "2026-10-16"), "Retail 2 ₹20 per lot"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	ep, desc, _ := o.ClientBrokerage(ctx, T, "RC1", "2026-10-16")
	if ep.PlanID != "card-2" || len(ep.Segments) != 1 || ep.Segments[0].Scope != "CM-NSE" || !ep.Override || !strings.Contains(desc["CM-DEL"], "BSE") {
		t.Fatalf("client view %+v %v", ep, desc)
	}

	// Billing: RC2 buys 1,000 at ₹5 on NSE through a dealer: 0.35% is
	// ₹17.50 but 3 paise a share is ₹30. RC1 buys 100 at ₹100 on NSE online:
	// the NSE card's 0.2% = ₹20.
	csv := "TradeNo,OrderNo,ClientCode,Symbol,Series,BuySell,Qty,Price,TradeDate,TradeTime,Product,AlgoId,TerminalId\n" +
		"71,801,RC2,INFY,EQ,1,1000,5,16-Oct-2026,10:00:00,CNC,,1001\n" +
		"72,802,RC1,INFY,EQ,1,100,100,16-Oct-2026,10:00:00,CNC,,90012\n"
	if _, err := o.App.IngestFile(ctx, T, "t", "NSE-CM-TRADE", "", "t.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	if _, err := o.App.RunBilling(ctx, T, "farhan.khan", "2026-10-16", nil); err != nil {
		t.Fatal(err)
	}
	es, _ := o.App.Ledger.Store.List(ctx, T, ledgerFilterDate("2026-10-16"))
	got := map[string]money.Amount{}
	for _, e := range es {
		for _, p := range e.Postings {
			if p.Account == "INCOME:BROKERAGE" && strings.Contains(e.Source.ID, "/RC") {
				got[strings.Split(e.Source.ID, "/")[1]] += p.Credit
			}
		}
	}
	if got["RC2"] != money.MustAmount("30") || got["RC1"] != money.MustAmount("20") {
		t.Fatalf("billed brokerage %v", got)
	}
	// A client's own rates end from a date: a row with no product.
	must("BROKERAGE-CLIENT-RATES", "UCC,From,Product,Mode\nRC1,2026-10-20,,\n", 1, 0)
	if g := q("RC1", charges.CMIntraday, "NSE", "", "2026-10-20"); g != "Retail 2 ₹0.02 per share" {
		t.Errorf("after the client rates end: %q", g)
	}
}

func TestClassifyTrades(t *testing.T) {
	s := DefaultBrokerageSetup()
	s.Terminals = []TerminalChannel{{"333*", "online"}}
	s.IndexUnderlyings = []string{"MYIDX"}
	for _, c := range []struct {
		t     ingest.TradeEvent
		ch    string
		cases string
	}{
		{ingest.TradeEvent{Segment: "CM", Settlement: "A", Terminal: "3331"}, "online", "auction"},
		{ingest.TradeEvent{Segment: "CM", Settlement: "N"}, "dealer", ""},
		{ingest.TradeEvent{Segment: "FO", Symbol: "BANKNIFTY"}, "dealer", "index"},
		{ingest.TradeEvent{Segment: "FO", Symbol: "MYIDX"}, "dealer", "index"},
		{ingest.TradeEvent{Segment: "FO", Symbol: "INFY"}, "dealer", "stock"},
	} {
		ch, cs := s.classify(c.t)
		if ch != c.ch || strings.Join(cs, ",") != c.cases {
			t.Errorf("%+v: %s %v", c.t, ch, cs)
		}
	}
}

// At expiry the card's own lines charge futures settled, options exercised
// (long) and assigned (short); with no line, nothing is charged.
func TestExpiryBrokerage(t *testing.T) {
	o, ctx := newOps(t)
	for _, u := range []string{"EX1", "EX2"} {
		docs.Upsert(ctx, o.Docs, T, cClient, u, Client{UCC: u, Name: "Client " + u, Status: "Active", Email: u + "@x.example"}, "t")
	}
	d := money.MustDec
	type R = charges.BrokerageRule
	if _, err := o.SavePlan(ctx, T, "farhan.khan", PlanInput{ID: "fo-card", Name: "FO card", From: "2026-04-01", Rules: map[string]R{
		"FO-FUT":              {Mode: charges.PlanFlat, Flat: d("20")},
		"FO-FUT|*|*|expiry":   {Mode: charges.PlanPerLot, PerLot: d("5")},
		"FO-OPT|*|*|exercise": {Mode: charges.PlanPct, Pct: d("0.1")},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"EX1", "EX2"} {
		if _, err := o.Assign(ctx, T, "farhan.khan", "ucc:"+u, "fo-card", "2026-04-01"); err != nil {
			t.Fatal(err)
		}
	}
	day := "2026-10-27"
	md := "Exchange,Segment,Symbol,Type,Expiry,Strike,ISIN,Company,Lot,LTP,Close,VaRPct,ELMPct,HaircutPct,SPANPct,ExposurePct,Delta,Underlying,Index\n" +
		"NSE,FO,NIFTY,FUT,2026-10-27,,,,75,25800,25800,,,,10,2,1,25800,Y\n" +
		"NSE,FO,NIFTY,CE,2026-10-27,25700,,,75,100,100,,,,10,2,0.9,25800,Y\n"
	if _, err := o.LoadFile(ctx, T, "farhan.khan", "MARKET-DATA", day, "md.csv", []byte(md)); err != nil {
		t.Fatal(err)
	}
	pos := "UCC,Exchange,Segment,Symbol,Type,Expiry,Strike,Qty,AvgPrice\n" +
		"EX1,NSE,FO,NIFTY,FUT,2026-10-27,,150,25700\n" + // 2 lots settle: ₹10
		"EX1,NSE,FO,NIFTY,CE,2026-10-27,25700,75,80\n" + // exercised: 0.1% of 25,800 × 75 = ₹1,935
		"EX2,NSE,FO,NIFTY,CE,2026-10-27,25700,-75,80\n" // assigned: no line, not charged
	if _, err := o.LoadFile(ctx, T, "farhan.khan", "POSITIONS", day, "pos.csv", []byte(pos)); err != nil {
		t.Fatal(err)
	}
	res, err := o.SettleDerivatives(ctx, T, "farhan.khan", day)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Charges) != 1 {
		t.Fatalf("charges %+v", res)
	}
	es, _ := o.App.Ledger.Store.List(ctx, T, ledgerFilterDate(day))
	var brok money.Amount
	for _, e := range es {
		if e.Source.Kind == "EXPBRK" {
			if !strings.Contains(e.Source.ID, "/EX1/") {
				t.Errorf("charged %s", e.Source.ID)
			}
			for _, p := range e.Postings {
				if p.Account == "INCOME:BROKERAGE" {
					brok += p.Credit
				}
			}
		}
	}
	if brok != money.MustAmount("1945") {
		t.Fatalf("expiry brokerage %s", brok)
	}
	if again, _ := o.SettleDerivatives(ctx, T, "farhan.khan", day); len(again.Charges) != 0 {
		t.Fatal("charged twice")
	}
}
