// Package money provides exact decimal arithmetic for prices and rates (Dec)
// and paise-denominated ledger amounts (Amount).
//
// Floating point is never used. Dec is backed by math/big.Rat, so products
// such as quantity × price × rate are exact until they are rounded, once,
// into an Amount.
package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Dec is an exact, immutable decimal. The zero value is 0.
type Dec struct{ r *big.Rat }

var zeroRat = new(big.Rat)

func (d Dec) rat() *big.Rat {
	if d.r == nil {
		return zeroRat
	}
	return d.r
}

// ParseDec parses a plain decimal such as "1412.05", "-3", ".5".
// Exponents, fractions and thousands separators are rejected.
func ParseDec(s string) (Dec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Dec{}, errors.New("money: empty decimal")
	}
	if strings.ContainsAny(s, "eE/,_ ") {
		return Dec{}, fmt.Errorf("money: invalid decimal %q", s)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return Dec{}, fmt.Errorf("money: invalid decimal %q", s)
	}
	return Dec{r}, nil
}

// MustDec is ParseDec for constants; it panics on invalid input.
func MustDec(s string) Dec {
	d, err := ParseDec(s)
	if err != nil {
		panic(err)
	}
	return d
}

// DecInt returns i as a Dec.
func DecInt(i int64) Dec { return Dec{new(big.Rat).SetInt64(i)} }

func (d Dec) Add(e Dec) Dec      { return Dec{new(big.Rat).Add(d.rat(), e.rat())} }
func (d Dec) Sub(e Dec) Dec      { return Dec{new(big.Rat).Sub(d.rat(), e.rat())} }
func (d Dec) Mul(e Dec) Dec      { return Dec{new(big.Rat).Mul(d.rat(), e.rat())} }
func (d Dec) MulInt(i int64) Dec { return Dec{new(big.Rat).Mul(d.rat(), new(big.Rat).SetInt64(i))} }
func (d Dec) Neg() Dec           { return Dec{new(big.Rat).Neg(d.rat())} }
func (d Dec) Abs() Dec           { return Dec{new(big.Rat).Abs(d.rat())} }
func (d Dec) Cmp(e Dec) int      { return d.rat().Cmp(e.rat()) }
func (d Dec) Sign() int          { return d.rat().Sign() }
func (d Dec) IsZero() bool       { return d.rat().Sign() == 0 }

// Pct returns d × p / 100, e.g. turnover.Pct(0.1) for 0.1% STT.
func (d Dec) Pct(p Dec) Dec {
	x := new(big.Rat).Mul(d.rat(), p.rat())
	return Dec{x.Quo(x, big.NewRat(100, 1))}
}

// Quo returns d / e, or an error when e is zero.
func (d Dec) Quo(e Dec) (Dec, error) {
	if e.IsZero() {
		return Dec{}, errors.New("money: division by zero")
	}
	return Dec{new(big.Rat).Quo(d.rat(), e.rat())}, nil
}

// String renders the exact value when it terminates within 12 decimal places.
func (d Dec) String() string {
	r := d.rat()
	if r.IsInt() {
		return r.Num().String()
	}
	s := r.FloatString(12)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "-0" {
		return "0"
	}
	return s
}

func (d Dec) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Dec) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*d = Dec{}
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	}
	v, err := ParseDec(s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// Unit is a rounding granularity expressed in paise.
type Unit int64

const (
	UnitPaisa Unit = 1
	UnitRupee Unit = 100
)

// ParseUnit maps "paisa" (or "") and "rupee" to a Unit.
func ParseUnit(s string) (Unit, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "paisa", "paise":
		return UnitPaisa, nil
	case "rupee", "rupees":
		return UnitRupee, nil
	}
	return 0, fmt.Errorf("money: unknown rounding unit %q", s)
}

// Round converts a rupee Dec to an Amount, rounding half away from zero to
// the given unit. It panics if the result does not fit in int64 paise
// (about ₹92 lakh crore), which indicates corrupt input.
func (d Dec) Round(u Unit) Amount {
	if u <= 0 {
		u = UnitPaisa
	}
	x := new(big.Rat).Mul(d.rat(), big.NewRat(100, int64(u)))
	n := roundHalfAway(x)
	n.Mul(n, big.NewInt(int64(u)))
	if !n.IsInt64() {
		panic("money: amount overflows int64 paise")
	}
	return Amount(n.Int64())
}

func roundHalfAway(x *big.Rat) *big.Int {
	num := new(big.Int).Set(x.Num())
	den := x.Denom()
	neg := num.Sign() < 0
	if neg {
		num.Neg(num)
	}
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if new(big.Int).Lsh(r, 1).Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if neg {
		q.Neg(q)
	}
	return q
}

// Amount is a ledger amount in paise.
type Amount int64

// Paise returns the amount in paise.
func (a Amount) Paise() int64 { return int64(a) }

// Dec returns the amount in rupees as a Dec.
func (a Amount) Dec() Dec { return Dec{big.NewRat(int64(a), 100)} }

func (a Amount) String() string {
	n := int64(a)
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	return fmt.Sprintf("%s%d.%02d", sign, n/100, n%100)
}

// ParseAmount parses rupees with at most two decimal places.
func ParseAmount(s string) (Amount, error) {
	d, err := ParseDec(s)
	if err != nil {
		return 0, err
	}
	x := new(big.Rat).Mul(d.rat(), big.NewRat(100, 1))
	if !x.IsInt() {
		return 0, fmt.Errorf("money: %q has more than two decimal places", s)
	}
	if !x.Num().IsInt64() {
		return 0, errors.New("money: amount out of range")
	}
	return Amount(x.Num().Int64()), nil
}

// MustAmount is ParseAmount for constants and tests.
func MustAmount(s string) Amount {
	a, err := ParseAmount(s)
	if err != nil {
		panic(err)
	}
	return a
}

func (a Amount) MarshalJSON() ([]byte, error) { return json.Marshal(a.String()) }

func (a *Amount) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*a = 0
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	}
	v, err := ParseAmount(s)
	if err != nil {
		return err
	}
	*a = v
	return nil
}

// Float64 returns an approximation for ratios and display. Never use it for
// amounts that are stored or posted.
func (d Dec) Float64() float64 {
	f, _ := d.rat().Float64()
	return f
}

// Max returns the larger of a and b.
func Max(a, b Dec) Dec {
	if a.Cmp(b) >= 0 {
		return a
	}
	return b
}

// Min returns the smaller of a and b.
func Min(a, b Dec) Dec {
	if a.Cmp(b) <= 0 {
		return a
	}
	return b
}

// Fixed renders d rounded half away from zero to n decimal places.
func (d Dec) Fixed(n int) string {
	x := new(big.Rat).Set(d.rat())
	p := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	x.Mul(x, new(big.Rat).SetInt(p))
	q := roundHalfAway(x)
	r := new(big.Rat).SetFrac(q, p)
	return r.FloatString(n)
}

// CeilRupee rounds d up (towards +infinity) to a whole rupee. Used where an
// amount must be retained in full, such as a running-account retention.
func (d Dec) CeilRupee() Dec {
	r := d.rat()
	q, m := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if m.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return Dec{new(big.Rat).SetInt(q)}
}
