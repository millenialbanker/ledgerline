package ops

import (
	"strings"
	"testing"

	"ledgerline/internal/money"
)

func TestLiveRMSAndFiles(t *testing.T) {
	o, ctx := setup(t)
	am, err := o.LiveRisk(ctx, T, D, "11:00:00")
	if err != nil {
		t.Fatal(err)
	}
	eod, err := o.LiveRisk(ctx, T, D, "")
	if err != nil || eod.Trades <= am.Trades || eod.Source != "Exchange" {
		t.Fatalf("trades am %d eod %d %s %v", am.Trades, eod.Trades, eod.Source, err)
	}
	var unbilled money.Dec
	for _, r := range eod.Rows {
		unbilled = unbilled.Add(r.Unbilled)
	}
	if unbilled.Sign() <= 0 {
		t.Fatal("no unbilled charges")
	}
	// A price snapshot moves MTM.
	var fut *PositionView
	for _, ps := range eod.Positions {
		for i := range ps {
			if ps[i].Type == "FUT" && ps[i].Qty != 0 {
				fut = &ps[i]
				break
			}
		}
		if fut != nil {
			break
		}
	}
	if fut == nil {
		t.Fatal("no futures position")
	}
	p := strings.Split(fut.Instrument, "|")
	newPx := fut.LTP.Mul(money.MustDec("0.9")).Fixed(2)
	csv := "Exchange,Segment,Symbol,InstrumentType,Expiry,Strike,LTP,Time\n" + fut.Exchange + "," + fut.Segment + "," + p[0] + "," + p[1] + "," + p[2] + "," + p[3] + "," + newPx + ",14:00:00\n"
	if _, err := o.LoadFile(ctx, T, "priya.nair", "LTP-SNAPSHOT", D, "ltp.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	after, _ := o.LiveRisk(ctx, T, D, "")
	if after.PricesAt != "14:00:00" {
		t.Fatalf("prices at %q", after.PricesAt)
	}
	var m1, m2 money.Dec
	for _, r := range eod.Rows {
		if r.UCC == fut.UCC {
			m1 = r.MTM
		}
	}
	for _, r := range after.Rows {
		if r.UCC == fut.UCC {
			m2 = r.MTM
		}
	}
	if m1.Cmp(m2) == 0 {
		t.Fatalf("MTM unchanged by snapshot: %s", m1)
	}
	if _, n, err := o.LiveLiquidationFile(ctx, T, D, ""); err != nil || (after.Breaches > 0 && n == 0) {
		t.Fatalf("liquidation: %d %v", n, err)
	}
	set, _ := o.Settings(ctx, T)
	next := set.nextBusinessDay(D)
	lf, err := o.LimitFile(ctx, T, next)
	if err != nil || strings.Count(string(lf), "\n") < 10 {
		t.Fatalf("limit file: %v", err)
	}
	hf, err := o.HoldingsFile(ctx, T, next)
	if err != nil || !strings.Contains(string(hf), "T1Qty") {
		t.Fatalf("holdings file: %v", err)
	}
	// Allocation never exceeds what the client has.
	rv, _ := o.Risk(ctx, T, D)
	al, err := o.Allocation(ctx, T, D)
	if err != nil || len(al) == 0 {
		t.Fatalf("allocation: %v", err)
	}
	tot := map[string]money.Dec{}
	for _, a := range al {
		tot[a.UCC] = tot[a.UCC].Add(a.Cash).Add(a.NonCash)
	}
	for _, r := range rv.Rows {
		have := money.Max(r.Funds, money.Dec{}).Add(r.CollUsable)
		if tot[r.UCC].Cmp(have) != 0 && have.Sign() > 0 {
			t.Fatalf("%s allocated %s of %s", r.UCC, tot[r.UCC], have)
		}
	}
	if _, err := o.LoadFile(ctx, T, "priya.nair", "MTF-ELIGIBLE", "", "mtf.csv", []byte("ISIN,Symbol,MarginPct\nINE009A01021,INFY,35\n")); err != nil {
		t.Fatal(err)
	}
	el, _ := o.MTFList(ctx, T, D)
	if len(el) != 1 || el["INE009A01021"].MarginPct.Cmp(money.DecInt(35)) < 0 {
		t.Fatalf("eligible from file: %v", el)
	}
}
