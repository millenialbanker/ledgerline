package charges

import (
	"testing"

	"ledgerline/internal/money"
)

func TestBrokerageRule(t *testing.T) {
	d := money.MustDec
	cases := []struct {
		name string
		r    BrokerageRule
		b    Bucket
		v    string
		qty  int64
		lot  int64
		want string
	}{
		{"pct", BrokerageRule{Mode: PlanPct, Pct: d("0.3")}, CMDelivery, "100000", 100, 1, "300"},
		{"pct with minimum", BrokerageRule{Mode: PlanPct, Pct: d("0.3"), Min: d("20")}, CMDelivery, "5000", 5, 1, "20"},
		{"minimum capped at 2.5% on cash market", BrokerageRule{Mode: PlanPct, Pct: d("0.3"), Min: d("20")}, CMDelivery, "500", 1, 1, "12.5"},
		{"pct with maximum", BrokerageRule{Mode: PlanPct, Pct: d("0.01"), Max: d("15")}, FOFut, "2000000", 75, 75, "15"},
		{"lower of: flat wins", BrokerageRule{Mode: PlanLowerOf, Flat: d("20"), Pct: d("0.03")}, CMIntraday, "200000", 100, 1, "20"},
		{"lower of: percent wins", BrokerageRule{Mode: PlanLowerOf, Flat: d("20"), Pct: d("0.03")}, CMIntraday, "30000", 100, 1, "9"},
		{"per lot, part lot rounds up", BrokerageRule{Mode: PlanPerLot, PerLot: d("5")}, FOOpt, "7000", 130, 65, "10"},
		{"per lot with maximum", BrokerageRule{Mode: PlanPerLot, PerLot: d("5"), Max: d("50")}, FOOpt, "70000", 65 * 20, 65, "50"},
		{"flat", BrokerageRule{Mode: PlanFlat, Flat: d("15")}, FOOpt, "1000", 65, 65, "15"},
		{"zero", BrokerageRule{Mode: PlanZero, Min: d("20")}, CMDelivery, "100000", 1, 1, "0"},
	}
	for _, c := range cases {
		if err := c.r.Validate(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := c.r.Compute(c.b, d(c.v), c.qty, c.lot); got.Cmp(d(c.want)) != 0 {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	for _, bad := range []BrokerageRule{{Mode: PlanPct}, {Mode: PlanLowerOf, Flat: d("20")}, {Mode: "x"}, {Mode: PlanPct, Pct: d("9")}, {Mode: PlanPct, Pct: d("1"), Min: d("50"), Max: d("10")}} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
