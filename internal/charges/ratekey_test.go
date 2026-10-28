package charges

import (
	"testing"

	"ledgerline/internal/money"
)

func TestRateLookupMostSpecificWins(t *testing.T) {
	d := money.MustDec
	rules := map[string]BrokerageRule{
		"FO-OPT":                  {Mode: PlanPerLot, PerLot: d("20")},
		"FO-OPT|BSE|*|*":          {Mode: PlanPerLot, PerLot: d("15")},
		"FO-OPT|*|online|*":       {Mode: PlanPerLot, PerLot: d("10")},
		"FO-OPT|*|*|index":        {Mode: PlanPerLot, PerLot: d("25")},
		"FO-OPT|NSE|online|index": {Mode: PlanPerLot, PerLot: d("12")},
		"FO-OPT|*|*|exercise":     {Mode: PlanPct, Pct: d("0.1")},
	}
	for _, c := range []struct {
		q    RateQuery
		want string
	}{
		{RateQuery{Bucket: FOOpt, Exchange: "NSE", Channel: "dealer"}, "FO-OPT"},
		{RateQuery{Bucket: FOOpt, Exchange: "BSE", Channel: "dealer"}, "FO-OPT|BSE|*|*"},
		{RateQuery{Bucket: FOOpt, Exchange: "BSE", Channel: "online"}, "FO-OPT|*|online|*"}, // channel beats exchange
		{RateQuery{Bucket: FOOpt, Exchange: "BSE", Channel: "online", Cases: []string{CaseIndex}}, "FO-OPT|*|*|index"},
		{RateQuery{Bucket: FOOpt, Exchange: "NSE", Channel: "online", Cases: []string{CaseIndex}}, "FO-OPT|NSE|online|index"},
		{RateQuery{Bucket: FOOpt, Exchange: "NSE", Cases: []string{CaseStock}}, "FO-OPT"},
		{RateQuery{Bucket: FOOpt, Exchange: "NSE", Cases: []string{CaseExercise}, Strict: true}, "FO-OPT|*|*|exercise"},
		{RateQuery{Bucket: FOOpt, Exchange: "NSE", Cases: []string{CaseAssignment}, Strict: true}, ""}, // no line: not charged
	} {
		_, k, ok := Lookup(rules, c.q)
		if k != c.want || ok != (c.want != "") {
			t.Errorf("%+v: got %q, want %q", c.q, k, c.want)
		}
	}
	if _, err := ParseRateKey("CM-DEL|NSE|*|rollover"); err == nil {
		t.Error("rollover accepted for delivery")
	}
	if _, err := ParseRateKey("CM-INTRA|BSE|Online|auction"); err != nil {
		t.Error(err)
	}
	if k, _ := ParseRateKey("cm-del|all||"); k.String() != "CM-DEL" {
		t.Errorf("canonical %q", k.String())
	}
}

func TestPerShareAndLotFloors(t *testing.T) {
	d := money.MustDec
	// 0.3% of ₹5,000 = ₹15; at least 5 paise a share on 1,000 shares = ₹50.
	r := BrokerageRule{Mode: PlanPct, Pct: d("0.3"), MinPerUnit: d("0.05")}
	if got := r.Compute(CMDelivery, d("5000"), 1000, 1); got.Cmp(d("50")) != 0 {
		t.Errorf("per-share floor: %s", got)
	}
	// Per share: 2 paise × 1,000 = ₹20, but ₹25 minimum per order.
	r = BrokerageRule{Mode: PlanPerUnit, PerUnit: d("0.02"), Min: d("25")}
	if got := r.Compute(CMIntraday, d("100000"), 1000, 1); got.Cmp(d("25")) != 0 {
		t.Errorf("per share with order minimum: %s", got)
	}
	// Options: ₹0.05% of premium, at least ₹10 a lot: 3 lots of 75 = ₹30.
	r = BrokerageRule{Mode: PlanPct, Pct: d("0.05"), MinPerLot: d("10")}
	if got := r.Compute(FOOpt, d("9000"), 225, 75); got.Cmp(d("30")) != 0 {
		t.Errorf("per-lot floor: %s", got)
	}
	// The 2.5% cash-market cap still applies over a floor.
	r = BrokerageRule{Mode: PlanPerUnit, PerUnit: d("0.1")}
	if got := r.Compute(CMDelivery, d("100"), 100, 1); got.Cmp(d("2.5")) != 0 {
		t.Errorf("cap: %s", got)
	}
	if err := (BrokerageRule{Mode: PlanPerUnit}).Validate(); err == nil {
		t.Error("per share without an amount accepted")
	}
}
