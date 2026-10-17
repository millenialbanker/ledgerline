package charges

import (
	"fmt"

	"ledgerline/internal/money"
)

// Brokerage plan modes.
const (
	PlanPct     = "pct"      // percent of the order's turnover (premium for options)
	PlanFlat    = "flat"     // fixed rupees per executed order
	PlanLowerOf = "lower_of" // lower of the flat amount and the percent
	PlanPerLot  = "per_lot"  // rupees per lot (derivatives)
	PlanZero    = "zero"     // no brokerage
)

// BrokerageRule is how a brokerage plan charges one product. It applies to
// an executed order (all its trades together). Min and Max, when set, bound
// the amount per order. On the cash market brokerage never exceeds 2.5% of
// the order's value, the exchange limit.
type BrokerageRule struct {
	Mode   string    `json:"mode"`
	Pct    money.Dec `json:"pct,omitempty"`
	Flat   money.Dec `json:"flat,omitempty"`
	PerLot money.Dec `json:"per_lot,omitempty"`
	Min    money.Dec `json:"min,omitempty"`
	Max    money.Dec `json:"max,omitempty"`
}

// CMBrokerageCapPct is the cash-market ceiling on brokerage.
var CMBrokerageCapPct = money.MustDec("2.5")

// Validate checks the rule is complete and sensible.
func (r BrokerageRule) Validate() error {
	neg := func(name string, d money.Dec) error {
		if d.Sign() < 0 {
			return fmt.Errorf("%s cannot be negative", name)
		}
		return nil
	}
	for _, c := range []struct {
		n string
		d money.Dec
	}{{"percent", r.Pct}, {"flat amount", r.Flat}, {"per-lot amount", r.PerLot}, {"minimum", r.Min}, {"maximum", r.Max}} {
		if err := neg(c.n, c.d); err != nil {
			return err
		}
	}
	switch r.Mode {
	case PlanZero:
	case PlanPct:
		if r.Pct.Sign() == 0 {
			return fmt.Errorf("percent of turnover is required")
		}
	case PlanFlat:
		if r.Flat.Sign() == 0 {
			return fmt.Errorf("the amount per order is required")
		}
	case PlanLowerOf:
		if r.Pct.Sign() == 0 || r.Flat.Sign() == 0 {
			return fmt.Errorf("both the amount per order and the percent are required")
		}
	case PlanPerLot:
		if r.PerLot.Sign() == 0 {
			return fmt.Errorf("the amount per lot is required")
		}
	default:
		return fmt.Errorf("unknown brokerage mode %q", r.Mode)
	}
	if r.Pct.Cmp(money.DecInt(5)) > 0 {
		return fmt.Errorf("percent above 5%% looks like a typing mistake")
	}
	if r.Min.Sign() > 0 && r.Max.Sign() > 0 && r.Min.Cmp(r.Max) > 0 {
		return fmt.Errorf("the minimum is above the maximum")
	}
	return nil
}

// Compute is the brokerage for an order of turnover v, quantity qty and lot
// size lot (1 for equities).
func (r BrokerageRule) Compute(b Bucket, v money.Dec, qty, lot int64) money.Dec {
	var x money.Dec
	switch r.Mode {
	case PlanPct:
		x = v.Pct(r.Pct)
	case PlanFlat:
		x = r.Flat
	case PlanLowerOf:
		x = money.Min(r.Flat, v.Pct(r.Pct))
	case PlanPerLot:
		if lot <= 0 {
			lot = 1
		}
		lots := (qty + lot - 1) / lot
		x = r.PerLot.MulInt(lots)
	case PlanZero:
		return money.Dec{}
	}
	if r.Min.Sign() > 0 {
		x = money.Max(x, r.Min)
	}
	if r.Max.Sign() > 0 {
		x = money.Min(x, r.Max)
	}
	if b == CMDelivery || b == CMIntraday {
		x = money.Min(x, v.Pct(CMBrokerageCapPct))
	}
	return x
}

// Describe is a short human description such as "0.3%, min ₹20".
func (r BrokerageRule) Describe() string {
	s := ""
	switch r.Mode {
	case PlanZero:
		return "Free"
	case PlanPct:
		s = r.Pct.String() + "%"
	case PlanFlat:
		s = "₹" + r.Flat.String() + " per order"
	case PlanLowerOf:
		s = "lower of ₹" + r.Flat.String() + " or " + r.Pct.String() + "%"
	case PlanPerLot:
		s = "₹" + r.PerLot.String() + " per lot"
	}
	if r.Min.Sign() > 0 {
		s += ", min ₹" + r.Min.String()
	}
	if r.Max.Sign() > 0 {
		s += ", max ₹" + r.Max.String()
	}
	return s
}

// ScheduleBrokerage expresses a schedule rule's brokerage as a plan rule.
func ScheduleBrokerage(r Rule) BrokerageRule {
	switch r.BrokerageMode {
	case BrokFlat:
		return BrokerageRule{Mode: PlanFlat, Flat: r.BrokerageFlat}
	case BrokPct:
		return BrokerageRule{Mode: PlanPct, Pct: r.BrokeragePct}
	case BrokMinFlatPct:
		return BrokerageRule{Mode: PlanLowerOf, Flat: r.BrokerageFlat, Pct: r.BrokeragePct}
	}
	return BrokerageRule{Mode: PlanZero}
}

// Buckets lists every charge bucket in display order.
var Buckets = []Bucket{CMDelivery, CMIntraday, FOFut, FOOpt, CDFut, CDOpt, COMFut, COMOpt}

// BucketNames are display names.
var BucketNames = map[Bucket]string{CMDelivery: "Equity delivery", CMIntraday: "Equity intraday", FOFut: "Equity futures", FOOpt: "Equity options",
	CDFut: "Currency futures", CDOpt: "Currency options", COMFut: "Commodity futures", COMOpt: "Commodity options"}
