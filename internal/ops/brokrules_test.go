package ops

import (
	"testing"

	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// auction marks a cash trade as an auction trade (settlement type A in
// bkSetup).
func auction(x ingest.TradeEvent) ingest.TradeEvent { x.Settlement = "A"; return x }

// Auction trades: the auction line wins over any line without the auction
// case, even one for the scrip or its scrip group on the client's card or
// in the client's own lines, and even when the auction line is on a later
// card (the firm default). A scrip's or group's own auction line still
// beats a general auction line. The firm can keep the general order
// ("scrip") instead.
func TestAuctionRateWins(t *testing.T) {
	o, bill := bkSetup(t, "AU1", "AU2")
	ctx := t.Context()
	d := money.MustDec
	type R = charges.BrokerageRule
	if _, err := o.SaveScripGroup(ctx, T, "farhan.khan", ScripGroup{Name: "SPECIAL", Members: []GroupMember{{Scrip: "TATAPOWER", From: "2026-04-01"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SavePlan(ctx, T, "farhan.khan", PlanInput{ID: "card-a", Name: "Card A", From: "2026-04-01", Rules: map[string]R{
		"CM-DEL|NSE|*|*":                   {Mode: charges.PlanPct, Pct: d("0.2")},
		"CM-DEL|*|*|*|scrip:RELIANCE":      {Mode: charges.PlanPct, Pct: d("0.01")},
		"CM-DEL|*|*|*|group:SPECIAL":       {Mode: charges.PlanPct, Pct: d("0.02")},
		"CM-DEL|*|*|auction":               {Mode: charges.PlanPct, Pct: d("1")},
		"CM-DEL|*|*|auction|scrip:PENNY":   {Mode: charges.PlanPct, Pct: d("2")},
		"CM-DEL|*|*|auction|group:SPECIAL": {Mode: charges.PlanPct, Pct: d("1.5")},
	}}); err != nil {
		t.Fatal(err)
	}
	// Card B has special scrip and group rates but no auction line; the
	// firm default has the auction rate.
	if _, err := o.SavePlan(ctx, T, "farhan.khan", PlanInput{ID: "card-b", Name: "Card B", From: "2026-04-01", Rules: map[string]R{
		"CM-DEL":                      {Mode: charges.PlanPct, Pct: d("0.3")},
		"CM-DEL|*|*|*|scrip:RELIANCE": {Mode: charges.PlanPct, Pct: d("0.05")},
		"CM-DEL|*|*|*|group:SPECIAL":  {Mode: charges.PlanPct, Pct: d("0.04")},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SavePlan(ctx, T, "farhan.khan", PlanInput{ID: "firm", Name: "Firm", From: "2026-04-01", Rules: map[string]R{
		"CM-DEL":             {Mode: charges.PlanPct, Pct: d("0.5")},
		"CM-DEL|*|*|auction": {Mode: charges.PlanPct, Pct: d("0.8")},
	}}); err != nil {
		t.Fatal(err)
	}
	for target, plan := range map[string]string{"ucc:AU1": "card-a", "ucc:AU2": "card-b", "default": "firm"} {
		if _, err := o.Assign(ctx, T, "farhan.khan", target, plan, "2026-04-01"); err != nil {
			t.Fatal(err)
		}
	}
	// AU2's own line for RELIANCE (no auction case).
	if _, err := o.SaveOverride(ctx, T, "farhan.khan", OverrideInput{UCC: "AU2", From: "2026-04-01", Rules: map[string]R{
		"CM-DEL|*|*|*|scrip:RELIANCE": {Mode: charges.PlanPct, Pct: d("0.03")},
	}}); err != nil {
		t.Fatal(err)
	}
	trades := func() []ingest.TradeEvent {
		return []ingest.TradeEvent{
			auction(eq("101", "A1", "AU1", "RELIANCE", "B", 10, "1000", "1001")), // scrip line without the case: auction 1% wins
			auction(eq("102", "A2", "AU1", "TATAPOWER", "B", 10, "400", "1001")), // group: the group's auction line, 1.5%
			auction(eq("103", "A3", "AU1", "PENNY", "B", 100, "5", "1001")),      // the scrip's own auction line, 2%
			eq("104", "A4", "AU1", "RELIANCE", "B", 10, "1000", "1001"),          // not auction: the scrip line
			auction(eq("105", "A5", "AU2", "RELIANCE", "B", 10, "1000", "1001")), // card and own scrip lines lose to the firm's auction line
			auction(eq("106", "A6", "AU2", "TATAPOWER", "B", 10, "400", "1001")), // so does the card's group line
			eq("107", "A7", "AU2", "RELIANCE", "B", 10, "1000", "1001"),          // not auction: the client's own line
		}
	}
	out := bill(trades()...)
	wantBrok(t, orderOf(t, out, "A1", ""), "100", "CM-DEL|*|*|auction")
	wantBrok(t, orderOf(t, out, "A2", ""), "60", "CM-DEL|*|*|auction|group:SPECIAL")
	wantBrok(t, orderOf(t, out, "A3", ""), "10", "CM-DEL|*|*|auction|scrip:PENNY")
	wantBrok(t, orderOf(t, out, "A4", ""), "1", "CM-DEL|*|*|*|scrip:RELIANCE")
	wantBrok(t, orderOf(t, out, "A5", ""), "80", "CM-DEL|*|*|auction")
	wantBrok(t, orderOf(t, out, "A6", ""), "32", "CM-DEL|*|*|auction")
	wantBrok(t, orderOf(t, out, "A7", ""), "3", "CM-DEL|*|*|*|scrip:RELIANCE")
	if x := orderOf(t, out, "A5", ""); x.Plan != "Firm" {
		t.Errorf("A5 priced by %q, want the firm default's auction line", x.Plan)
	}
	if x := orderOf(t, out, "A7", ""); x.Plan != "Card B + client rate" {
		t.Errorf("A7 priced by %q", x.Plan)
	}
	// A client's own auction line wins over the card's at equal detail.
	if _, err := o.SaveOverride(ctx, T, "farhan.khan", OverrideInput{UCC: "AU1", From: "2026-04-01", Rules: map[string]R{
		"CM-DEL|*|*|auction": {Mode: charges.PlanPct, Pct: d("0.6")},
	}}); err != nil {
		t.Fatal(err)
	}
	q, ok, err := o.QuoteLine(ctx, T, "AU1", "RELIANCE", charges.RateQuery{Bucket: charges.CMDelivery, Exchange: "NSE", Cases: []string{charges.CaseAuction}}, bkDay)
	if err != nil || !ok || q.Line != "CM-DEL|*|*|auction" || q.Plan != "Card A + client rate" {
		t.Errorf("own auction line: %+v %v %v", q, ok, err)
	}

	// The firm's choice: lines for the scrip or its group first.
	set, _ := o.BrokerageSetup(ctx, T)
	set.AuctionRate = "Scrip"
	if set, err = o.SaveBrokerageSetup(ctx, T, "farhan.khan", set); err != nil || set.AuctionRate != AuctionRateScrip {
		t.Fatalf("%+v %v", set, err)
	}
	out = bill(trades()...)
	wantBrok(t, orderOf(t, out, "A1", ""), "1", "CM-DEL|*|*|*|scrip:RELIANCE")
	wantBrok(t, orderOf(t, out, "A3", ""), "10", "CM-DEL|*|*|auction|scrip:PENNY")
	wantBrok(t, orderOf(t, out, "A5", ""), "3", "CM-DEL|*|*|*|scrip:RELIANCE")
	wantBrok(t, orderOf(t, out, "A6", ""), "1.60", "CM-DEL|*|*|*|group:SPECIAL")
	set.AuctionRate = "sometimes"
	if _, err := o.SaveBrokerageSetup(ctx, T, "farhan.khan", set); err == nil {
		t.Error("unknown auction rate accepted")
	}
	set.AuctionRate = AuctionRateAlways
	if set, err = o.SaveBrokerageSetup(ctx, T, "farhan.khan", set); err != nil || set.AuctionRate != "" {
		t.Errorf("always is the default: %+v %v", set, err)
	}
}

// A client's own rate lines replace only the card line they match, line by
// line, never the whole card for the segment: the card's other lines (a
// channel, an exchange, another product) still apply.
func TestClientLinesReplaceLineByLine(t *testing.T) {
	o, _ := bkSetup(t, "LL1")
	ctx := t.Context()
	d := money.MustDec
	type R = charges.BrokerageRule
	if _, err := o.SavePlan(ctx, T, "farhan.khan", PlanInput{ID: "card", Name: "Card", From: "2026-04-01", Rules: map[string]R{
		"FO-OPT":            {Mode: charges.PlanPerLot, PerLot: d("20")},
		"FO-OPT|*|online|*": {Mode: charges.PlanPerLot, PerLot: d("10")},
		"FO-FUT":            {Mode: charges.PlanFlat, Flat: d("25")},
		"CM-DEL":            {Mode: charges.PlanPct, Pct: d("0.3")},
		"CM-DEL|BSE|*|*":    {Mode: charges.PlanPct, Pct: d("0.25")},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Assign(ctx, T, "farhan.khan", "ucc:LL1", "card", "2026-04-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveOverride(ctx, T, "farhan.khan", OverrideInput{UCC: "LL1", From: "2026-04-01", Rules: map[string]R{
		"FO-OPT": {Mode: charges.PlanPerLot, PerLot: d("15")},
		"CM-DEL": {Mode: charges.PlanPct, Pct: d("0.1")},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		q          charges.RateQuery
		line, plan string
	}{
		{charges.RateQuery{Bucket: charges.FOOpt, Exchange: "NSE", Channel: "dealer"}, "FO-OPT", "Card + client rate"},      // replaced
		{charges.RateQuery{Bucket: charges.FOOpt, Exchange: "NSE", Channel: "online"}, "FO-OPT|*|online|*", "Card"},         // the card's online line stays
		{charges.RateQuery{Bucket: charges.FOFut, Exchange: "NSE", Channel: "dealer"}, "FO-FUT", "Card"},                    // same segment, another line: stays
		{charges.RateQuery{Bucket: charges.CMDelivery, Exchange: "NSE", Channel: "dealer"}, "CM-DEL", "Card + client rate"}, // replaced
		{charges.RateQuery{Bucket: charges.CMDelivery, Exchange: "BSE", Channel: "dealer"}, "CM-DEL|BSE|*|*", "Card"},       // the card's BSE line stays
		{charges.RateQuery{Bucket: charges.CMIntraday, Exchange: "NSE", Channel: "dealer"}, "", ""},                         // no line anywhere: the schedule
	} {
		a, ok, err := o.QuoteLine(ctx, T, "LL1", "", c.q, bkDay)
		if err != nil || ok != (c.line != "") || a.Line != c.line || a.Plan != c.plan {
			t.Errorf("%s %s %s: %q by %q (%v %v), want %q by %q", c.q.Bucket, c.q.Exchange, c.q.Channel, a.Line, a.Plan, ok, err, c.line, c.plan)
		}
	}
	r, _, _, _ := o.QuoteRule(ctx, T, "LL1", charges.RateQuery{Bucket: charges.FOOpt, Exchange: "NSE", Channel: "online"}, bkDay)
	if r.PerLot.Cmp(d("10")) != 0 {
		t.Errorf("online options %s a lot, want the card's 10", r.PerLot)
	}
}
