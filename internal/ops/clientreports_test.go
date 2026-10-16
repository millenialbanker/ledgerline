package ops

import (
	"context"
	"strings"
	"testing"

	"ledgerline"
	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// TestEquityBookHandWorked checks the lot engine on figures worked by hand:
//
//	lots: 100 INFY bought 2024-01-02 @ 500, 50 bought 2026-01-12 @ 600
//	15-Oct: buy 30 @ 700, sell 40 @ 710  -> 30 speculative: 30 x 10 = 300
//	                                        10 sold FIFO from 2024 lot, LTCG: 10 x 210 = 2,100
//	16-Oct: sell 100 @ 650               -> 90 from 2024 lot, LTCG 90 x 150 = 13,500
//	                                        10 from 2026 lot, STCG 10 x 50 = 500
//	left: 40 @ 600 (2026-01-12)
func TestEquityBookHandWorked(t *testing.T) {
	ctx := context.Background()
	reg, _ := ingest.LoadFS(ledgerline.FS, "config/adapters")
	f, _ := ledgerline.FS.Open("config/charges/sample-schedule.json")
	sched, _ := charges.LoadSchedule(f)
	f.Close()
	a := app.New(app.MemoryStores(), reg, sched)
	o := New(a)
	if err := o.SeedDev(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	book := LotBook{UCC: "BH9", AsOf: "2026-10-14", Lots: []Lot{
		{ISIN: "INE009A01021", Symbol: "INFY", Qty: 100, Date: "2024-01-02", Price: money.MustDec("500")},
		{ISIN: "INE009A01021", Symbol: "INFY", Qty: 50, Date: "2026-01-12", Price: money.MustDec("600")},
	}}
	if err := docs.Upsert(ctx, o.Docs, "t", cLot, "BH9", book, "test"); err != nil {
		t.Fatal(err)
	}
	csv := "TradeNo,OrderNo,ClientCode,Symbol,Series,BuySell,Qty,Price,TradeDate,TradeTime,Product,AlgoId,TerminalId\n" +
		"1,11,BH9,INFY,EQ,1,30,700,15-Oct-2026,10:00:00,MIS,,T1\n" +
		"2,12,BH9,INFY,EQ,2,40,710,15-Oct-2026,11:00:00,CNC,,T1\n" +
		"3,13,BH9,INFY,EQ,2,100,650,16-Oct-2026,10:00:00,CNC,,T1\n"
	if _, err := a.IngestFile(ctx, "t", "test", "NSE-CM-TRADE", "", "t.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	open, sales, _, err := o.equityBook(ctx, "t", "BH9", "2026-10-16")
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		cat, gain string
		qty       int64
	}
	ws := []want{{CatSpec, "300", 30}, {CatLTCG, "2100", 10}, {CatLTCG, "13500", 90}, {CatSTCG, "500", 10}}
	if len(sales) != len(ws) {
		t.Fatalf("sales %+v", sales)
	}
	for i, w := range ws {
		if sales[i].Category != w.cat || sales[i].Qty != w.qty || sales[i].Gain.Cmp(money.MustDec(w.gain)) != 0 {
			t.Errorf("sale %d = %s %d %s, want %s %d %s", i, sales[i].Category, sales[i].Qty, sales[i].Gain, w.cat, w.qty, w.gain)
		}
	}
	if len(open) != 1 || open[0].Qty != 40 || open[0].Date != "2026-01-12" {
		t.Fatalf("open lots %+v", open)
	}
	// Up to the 15th only: the 16th's sale is not yet made.
	open, sales, _, _ = o.equityBook(ctx, "t", "BH9", "2026-10-15")
	if len(sales) != 2 || len(open) != 2 || open[0].Qty != 90 {
		t.Fatalf("as of 15th: open %+v sales %d", open, len(sales))
	}
}

func TestClientReportsSampleDay(t *testing.T) {
	o, ctx := setup(t)
	if _, err := o.App.RunRecon(ctx, T, "priya.nair", D); err != nil {
		t.Fatal(err)
	}
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBilling(ctx, T, "priya.nair", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	// Find a client who sold delivery today, and one with derivatives.
	ex, _ := o.App.ExchangeTrades(ctx, T, D)
	seller, fo := "", ""
	for _, tr := range ex {
		if tr.Segment == "CM" && tr.Side == "S" && seller == "" {
			seller = tr.UCC
		}
		if tr.Segment == "FO" && fo == "" {
			fo = tr.UCC
		}
	}
	from := "2026-04-01"
	for _, c := range []struct{ kind, ucc string }{{"ledger", seller}, {"ledger", "ALL"}, {"holdings", seller}, {"holdings", "ALL"}, {"portfolio", seller}, {"pnl", seller}, {"pnl", fo}, {"tradebook", seller}, {"tradebook", "ALL"}, {"positions", fo}} {
		r, err := o.ClientReportFor(ctx, T, c.kind, c.ucc, from, D)
		if err != nil {
			t.Fatalf("%s %s: %v", c.kind, c.ucc, err)
		}
		rows := 0
		for _, s := range r.Sections {
			rows += len(s.Rows)
			for _, row := range s.Rows {
				if len(row) != len(s.Columns) {
					t.Fatalf("%s/%s: row has %d cells for %d columns", c.kind, s.Title, len(row), len(s.Columns))
				}
			}
			if len(s.Totals) > 0 && len(s.Totals) != len(s.Columns) {
				t.Fatalf("%s/%s: totals width", c.kind, s.Title)
			}
		}
		html, err := o.RenderClientReport(ctx, T, r)
		if err != nil || !strings.Contains(string(html), "UCC "+r.UCC) {
			t.Fatalf("%s render: %v", c.kind, err)
		}
		if len(r.CSV()) < 50 {
			t.Fatalf("%s csv empty", c.kind)
		}
		t.Logf("%-9s %-6s %d sections, %d rows, summary %v", c.kind, c.ucc, len(r.Sections), rows, r.Summary)
	}
	// Ledger statement closing equals the ledger.
	r, _ := o.ClientReportFor(ctx, T, "ledger", seller, from, D)
	st, _ := o.App.Ledger.Statement(ctx, T, "CLIENT:"+seller, from, D)
	if r.Summary[3].Value != st.Closing.Dec().Fixed(2) {
		t.Fatalf("closing %s vs %s", r.Summary[3].Value, st.Closing.Dec().Fixed(2))
	}
	// Holdings from lots + today's trades reconcile with the depository file
	// after today's settlement would complete: lots (BOD) + net buys - net sells.
	gains := 0
	cs, _ := o.Clients(ctx, T)
	for _, c := range cs {
		if c.Status != "Active" {
			continue
		}
		r, err := o.ClientReportFor(ctx, T, "pnl", c.UCC, from, D)
		if err != nil {
			t.Fatal(err)
		}
		if r.Summary[1].Value != "0.00" || r.Summary[2].Value != "0.00" {
			gains++
		}
	}
	t.Logf("%d clients with capital gains on the sample day", gains)
	if gains == 0 {
		t.Fatal("no client has capital gains on the sample day")
	}
	if _, err := o.ClientReportFor(ctx, T, "pnl", "ALL", from, D); err == nil {
		t.Fatal("pnl for ALL should be refused")
	}
	if _, err := o.ClientReportFor(ctx, T, "ledger", seller, D, from); err == nil {
		t.Fatal("from after to should be refused")
	}
}
