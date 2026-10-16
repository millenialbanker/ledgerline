package money

import (
	"encoding/json"
	"testing"
)

func TestParseAndString(t *testing.T) {
	cases := map[string]string{"1412.00": "1412", "-3.50": "-3.5", ".5": "0.5", "88.4225": "88.4225", "0": "0"}
	for in, want := range cases {
		d, err := ParseDec(in)
		if err != nil {
			t.Fatalf("ParseDec(%q): %v", in, err)
		}
		if got := d.String(); got != want {
			t.Errorf("ParseDec(%q).String() = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "1e5", "1,000", "1/3", "abc", "1 0"} {
		if _, err := ParseDec(bad); err == nil {
			t.Errorf("ParseDec(%q) should fail", bad)
		}
	}
}

func TestExactArithmetic(t *testing.T) {
	// 0.1 + 0.2 must be exactly 0.3, unlike float64.
	if got := MustDec("0.1").Add(MustDec("0.2")); got.Cmp(MustDec("0.3")) != 0 {
		t.Fatalf("0.1+0.2 = %s", got)
	}
	v := MustDec("1412.00").MulInt(10)
	if got := v.Pct(MustDec("0.00297")).String(); got != "0.419364" {
		t.Fatalf("txn charge = %s", got)
	}
}

func TestRound(t *testing.T) {
	cases := []struct {
		in   string
		unit Unit
		want string
	}{
		{"0.419364", UnitPaisa, "0.42"},
		{"0.014120", UnitPaisa, "0.01"},
		{"2.115", UnitPaisa, "2.12"},   // half away from zero
		{"-2.115", UnitPaisa, "-2.12"}, // symmetric for negatives
		{"2.114999", UnitPaisa, "2.11"},
		{"14.12", UnitRupee, "14.00"},
		{"14.50", UnitRupee, "15.00"},
		{"-14.50", UnitRupee, "-15.00"},
	}
	for _, c := range cases {
		if got := MustDec(c.in).Round(c.unit).String(); got != c.want {
			t.Errorf("Round(%s, %d) = %s, want %s", c.in, c.unit, got, c.want)
		}
	}
}

func TestAmount(t *testing.T) {
	a := MustAmount("14136.75")
	if a.Paise() != 1413675 || a.String() != "14136.75" {
		t.Fatalf("amount = %d / %s", a.Paise(), a)
	}
	if (-a).String() != "-14136.75" {
		t.Fatalf("negative = %s", -a)
	}
	if _, err := ParseAmount("1.005"); err == nil {
		t.Fatal("three decimals should be rejected")
	}
	b, _ := json.Marshal(struct{ A Amount }{a})
	if string(b) != `{"A":"14136.75"}` {
		t.Fatalf("json = %s", b)
	}
	var back struct{ A Amount }
	if err := json.Unmarshal([]byte(`{"A":12.5}`), &back); err != nil || back.A != 1250 {
		t.Fatalf("unmarshal number: %v %d", err, back.A)
	}
}

func TestDecJSON(t *testing.T) {
	var v struct{ P Dec }
	if err := json.Unmarshal([]byte(`{"P":"25840.5"}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.P.String() != "25840.5" {
		t.Fatalf("got %s", v.P)
	}
	if err := json.Unmarshal([]byte(`{"P":1e3}`), &v); err == nil {
		t.Fatal("exponent should be rejected")
	}
}
