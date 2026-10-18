package ops

import (
	"strings"
	"testing"

	"ledgerline/internal/billing"
	"ledgerline/internal/money"
)

func TestYearEndPack(t *testing.T) {
	o, ctx := setup(t)
	if _, _, err := fyRange("2026-28"); err == nil {
		t.Fatal("bad FY accepted")
	}
	if DefaultFY("2026-06-10") != "2025-26" || DefaultFY("2026-10-16") != "2026-27" || DefaultFY("2027-02-01") != "2026-27" {
		t.Fatal("default FY")
	}
	v, err := o.YearEnds(ctx, T, "2026-27", D, nil)
	if err != nil || !v.Running || v.STT.Sign() <= 0 {
		t.Fatalf("view: %+v %v", v.STT, err)
	}
	// STT per client equals what billing charges for the same trades.
	trades, _ := o.exchangeTrades(ctx, T, D)
	m, _, _ := o.Market(ctx, T, D)
	out, err := o.App.BuildBills(ctx, billing.Input{Tenant: T, Date: D, Actor: "test", Trades: trades, SettlementPrices: m.SettlementPrices(), Schedule: o.App.Schedule})
	if err != nil {
		t.Fatal(err)
	}
	billed := map[string]money.Dec{}
	for _, g := range out.Groups {
		if g.Segment == "CM" || g.Segment == "FO" { // CTT on commodities is not STT
			billed[g.UCC] = billed[g.UCC].Add(g.Charges.STT.Dec())
		}
	}
	checked := 0
	for _, r := range v.Rows {
		if r.STT.Cmp(billed[r.UCC]) != 0 {
			t.Errorf("%s: STT %s, billed %s", r.UCC, r.STT, billed[r.UCC])
		}
		if r.STT.Sign() > 0 {
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no client with STT")
	}
	var u string
	for _, r := range v.Rows {
		if r.STT.Sign() > 0 {
			u = r.UCC
			break
		}
	}
	p, err := o.YearEnd(ctx, T, u, "2026-27", D)
	if err != nil || p.STTTotal.Cmp(billed[u]) != 0 || len(p.Exchanges) == 0 {
		t.Fatalf("pack: %+v %v", p, err)
	}
	b, err := RenderYearEnd(p)
	if err != nil || !strings.Contains(string(b), "FORM No. 10DB") || !strings.Contains(string(b), "still running") {
		t.Fatalf("render: %v", err)
	}
	// Packs go out only after the year ends.
	if _, _, err := o.SendYearEnd(ctx, T, "priya.nair", "2026-27", D, nil); err == nil {
		t.Fatal("sent during the year")
	}
	if n, f, err := o.SendYearEnd(ctx, T, "priya.nair", "2026-27", "2027-04-05", []string{u}); err != nil || n != 1 {
		t.Fatalf("send: %d sent %d failed %v", n, f, err)
	}
	if p, _ = o.YearEnd(ctx, T, u, "2026-27", "2027-04-05"); p.SentAt == "" || p.Running {
		t.Fatalf("after year: sent %q running %v", p.SentAt, p.Running)
	}
	if _, _, err := o.SendYearEnd(ctx, T, "anita.rao", "2026-27", "2027-04-05", nil); err == nil {
		t.Fatal("risk manager sent packs")
	}
}
