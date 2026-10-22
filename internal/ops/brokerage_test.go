package ops

import (
	"testing"

	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// TestBrokeragePlans checks who pays what: client override, then the most
// recently assigned group, then the firm default, then the schedule; plan
// versions by date; and per-order brokerage in the bill and contract note.
func TestBrokeragePlans(t *testing.T) {
	o, ctx := newOps(t)
	d := money.MustDec
	for _, u := range []string{"BH1", "BH2", "BH3", "BH4"} {
		docs.Upsert(ctx, o.Docs, T, cClient, u, Client{UCC: u, Name: "Client " + u, Status: "Active", Email: u + "@x.example"}, "t")
	}
	if _, err := o.TagClients(ctx, T, "priya.nair", []string{"BH2", "BH3"}, []string{"hni", "Active trader"}, nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := o.Client(ctx, T, "BH2"); len(c.Tags) != 2 || c.Tags[0] != "ACTIVE-TRADER" || c.Tags[1] != "HNI" {
		t.Fatalf("tags %v", c.Tags)
	}
	if _, err := o.TagClients(ctx, T, "priya.nair", []string{"BH3"}, nil, []string{"ACTIVE-TRADER"}); err != nil {
		t.Fatal(err)
	}
	type R = charges.BrokerageRule
	mk := func(id string, from string, r R) {
		if _, err := o.SavePlan(ctx, T, "farhan.khan", PlanInput{ID: id, Name: id, From: from, Rules: map[string]R{"CM-DEL": r}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.SavePlan(ctx, T, "priya.nair", PlanInput{ID: "x", Name: "x", From: "2026-01-01"}); err == nil {
		t.Fatal("a maker changed brokerage plans")
	}
	mk("std", "2026-01-01", R{Mode: charges.PlanPct, Pct: d("0.5")})
	mk("std", "2026-10-16", R{Mode: charges.PlanPct, Pct: d("0.4")}) // rate cut from 16-Oct
	mk("hni", "2026-01-01", R{Mode: charges.PlanFlat, Flat: d("25")})
	mk("trader", "2026-01-01", R{Mode: charges.PlanPct, Pct: d("0.1")})
	mk("vip", "2026-01-01", R{Mode: charges.PlanZero})
	assign := func(target, plan, from string) {
		if _, err := o.Assign(ctx, T, "farhan.khan", target, plan, from); err != nil {
			t.Fatalf("%s: %v", target, err)
		}
	}
	assign("default", "std", "2026-01-01")
	assign("tag:HNI", "hni", "2026-02-01")
	assign("tag:ACTIVE-TRADER", "trader", "2026-03-01") // more recent than HNI: wins for BH2
	assign("ucc:BH4", "vip", "2026-10-17")              // from tomorrow
	want := map[string]string{"BH1": "std/Firm default", "BH2": "trader/Group ACTIVE-TRADER", "BH3": "hni/Group HNI", "BH4": "std/Firm default"}
	for u, w := range want {
		ep, _, err := o.ClientBrokerage(ctx, T, u, "2026-10-16")
		if err != nil || ep.PlanID+"/"+ep.Source != w {
			t.Errorf("%s on 16-Oct: %s/%s, want %s (%v)", u, ep.PlanID, ep.Source, w, err)
		}
	}
	if ep, _, _ := o.ClientBrokerage(ctx, T, "BH4", "2026-10-17"); ep.PlanID != "vip" || ep.Source != "Client" {
		t.Errorf("BH4 on 17-Oct: %+v", ep)
	}
	// Removing the client override from a later date falls back to the default.
	assign("ucc:BH4", "", "2026-10-20")
	if ep, _, _ := o.ClientBrokerage(ctx, T, "BH4", "2026-10-20"); ep.PlanID != "std" {
		t.Errorf("BH4 after removal: %+v", ep)
	}
	// Bill: BH1 buys 100 INFY at 1500 in one order filled by two trades on
	// 15-Oct (0.5%) and the same on 16-Oct (0.4%): 750 and 600, not per trade.
	csv := "TradeNo,OrderNo,ClientCode,Symbol,Series,BuySell,Qty,Price,TradeDate,TradeTime,Product,AlgoId,TerminalId\n" +
		"11,901,BH1,INFY,EQ,1,60,1500,15-Oct-2026,10:00:00,CNC,,T1\n" +
		"12,901,BH1,INFY,EQ,1,40,1500,15-Oct-2026,10:00:01,CNC,,T1\n" +
		"13,902,BH1,INFY,EQ,1,60,1500,16-Oct-2026,10:00:00,CNC,,T1\n" +
		"14,902,BH1,INFY,EQ,1,40,1500,16-Oct-2026,10:00:01,CNC,,T1\n" +
		"15,903,BH3,INFY,EQ,1,10,1500,16-Oct-2026,10:00:00,CNC,,T1\n" +
		"16,903,BH3,INFY,EQ,1,10,1500,16-Oct-2026,10:00:02,CNC,,T1\n"
	if _, err := o.App.IngestFile(ctx, T, "t", "NSE-CM-TRADE", "", "t.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	for date, w := range map[string]map[string]string{"2026-10-15": {"BH1": "750"}, "2026-10-16": {"BH1": "600", "BH3": "25"}} {
		res, err := o.App.RunBilling(ctx, T, "farhan.khan", date, nil)
		if err != nil {
			t.Fatal(err)
		}
		es, _ := o.App.Ledger.Store.List(ctx, T, ledgerFilterDate(date))
		got := map[string]money.Amount{}
		for _, e := range es {
			for _, p := range e.Postings {
				if p.Account == "INCOME:BROKERAGE" {
					got[e.Source.ID[11:len(e.Source.ID)-7]] += p.Credit
				}
			}
		}
		for u, x := range w {
			if got[u].Dec().Cmp(d(x)) != 0 {
				t.Errorf("%s %s brokerage %s, want %s (posted %d)", date, u, got[u].Dec(), x, len(res.Posted))
			}
		}
	}
	// The contract note splits the order's 600 by trade value: 360 and 240.
	_, out, err := o.noteData(ctx, T, "2026-10-16", "BH1", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.TradeBrokerage["NSE|CM|13"].Dec().Cmp(d("360")) != 0 || out.TradeBrokerage["NSE|CM|14"].Dec().Cmp(d("240")) != 0 {
		t.Fatalf("note split %v", out.TradeBrokerage)
	}
	// Group reports cover exactly the group.
	r, err := o.ClientReportFor(ctx, T, "tradebook", "TAG:HNI", "2026-10-15", "2026-10-16")
	if err != nil || len(r.Sections[0].Rows) != 2 {
		t.Fatalf("group trade book: %v rows %d", err, len(r.Sections[0].Rows))
	}
}
