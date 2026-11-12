package charges

import (
	"testing"

	"ledgerline/internal/money"
)

// Scrip, group and underlying lines: precedence over case, channel and
// exchange; old four-part keys are unchanged.
func TestTargetLines(t *testing.T) {
	d := money.MustDec
	rules := map[string]BrokerageRule{
		"CM-DEL":                          {Mode: PlanPct, Pct: d("0.3")},
		"CM-DEL|NSE|online|*":             {Mode: PlanPct, Pct: d("0.1")},
		"CM-DEL|*|*|auction":              {Mode: PlanPct, Pct: d("1")},
		"CM-DEL|*|*|*|group:ETF":          {Mode: PlanPct, Pct: d("0.01")},
		"CM-DEL|*|*|*|scrip:INE002A01018": {Mode: PlanPct, Pct: d("0.05")},
		"FO-OPT|*|*|index":                {Mode: PlanPerLot, PerLot: d("20")},
		"FO-OPT|*|online|*|und:NIFTY":     {Mode: PlanPerLot, PerLot: d("10")},
	}
	for _, c := range []struct {
		q    RateQuery
		want string
	}{
		{RateQuery{Bucket: CMDelivery, Exchange: "NSE", Channel: "online", Targets: Targets{Scrips: []string{"TCS"}}}, "CM-DEL|NSE|online|*"},
		{RateQuery{Bucket: CMDelivery, Exchange: "NSE", Channel: "online", Targets: Targets{Scrips: []string{"NIFTYBEES"}, Groups: []string{"ETF"}}}, "CM-DEL|*|*|*|group:ETF"},
		{RateQuery{Bucket: CMDelivery, Exchange: "NSE", Channel: "online", Cases: []string{CaseAuction}, Targets: Targets{Scrips: []string{"RELIANCE", "INE002A01018"}, Groups: []string{"ETF"}}}, "CM-DEL|*|*|*|scrip:INE002A01018"},
		{RateQuery{Bucket: CMDelivery, Exchange: "NSE", Cases: []string{CaseAuction}, Targets: Targets{Scrips: []string{"TCS"}}}, "CM-DEL|*|*|auction"},
		{RateQuery{Bucket: FOOpt, Exchange: "NSE", Channel: "online", Cases: []string{CaseIndex}, Targets: Targets{Underlying: "NIFTY"}}, "FO-OPT|*|online|*|und:NIFTY"},
		{RateQuery{Bucket: FOOpt, Exchange: "NSE", Channel: "dealer", Cases: []string{CaseIndex}, Targets: Targets{Underlying: "NIFTY"}}, "FO-OPT|*|*|index"},
		{RateQuery{Bucket: FOOpt, Exchange: "NSE", Cases: []string{CaseExercise}, Strict: true, Targets: Targets{Underlying: "NIFTY"}}, ""},
	} {
		_, k, ok := Lookup(rules, c.q)
		if k != c.want || ok != (c.want != "") {
			t.Errorf("%+v: got %q, want %q", c.q, k, c.want)
		}
	}
	for in, want := range map[string]string{"CM-DEL": "CM-DEL", "CM-DEL|NSE|*|*": "CM-DEL|NSE|*|*", "CM-DEL|*|*|*|*": "CM-DEL", "fo-opt|all|||underlying:nifty": "FO-OPT|*|*|*|und:NIFTY", "CM-DEL|||| scrip:reliance": "CM-DEL|*|*|*|scrip:RELIANCE"} {
		k, err := ParseRateKey(in)
		if err != nil || k.String() != want {
			t.Errorf("%q: %q %v", in, k.String(), err)
		}
	}
	for _, bad := range []string{"CM-DEL|*|*|*|und:NIFTY", "FO-FUT|*|*|*|scrip:INFY", "CM-DEL|*|*|*|group:bad name", "CM-DEL|*|*|*|nifty", "CM-DEL|*|*|physical"} {
		if _, err := ParseRateKey(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	r := BrokerageRule{Mode: PlanPct, Pct: d("0.1"), Basis: BasisMarket, MinPerNote: d("25")}
	if err := r.Validate(); err != nil || r.Describe() != "0.1%, min ₹25 per contract note, on market rate" {
		t.Errorf("%q %v", r.Describe(), err)
	}
	if got := r.ComputeOn(CMDelivery, d("11000"), d("10000"), 10, 1); got.Cmp(d("11")) != 0 {
		t.Errorf("market basis %s", got)
	}
	if (BrokerageRule{Mode: PlanPct, Pct: d("1"), Basis: "weekly"}).Validate() == nil {
		t.Error("unknown basis accepted")
	}
}
