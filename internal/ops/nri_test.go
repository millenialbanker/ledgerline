package ops

import (
	"errors"
	"strings"
	"testing"
)

func TestNRI(t *testing.T) {
	o, ctx := setup(t)
	ts, _ := o.exchangeTrades(ctx, T, D)
	// A client with an intraday trade and a delivery sale.
	mis, sell := map[string]bool{}, map[string]bool{}
	for _, x := range ts {
		if x.Segment == "CM" && x.Product == "MIS" {
			mis[x.UCC] = true
		}
		if x.Segment == "CM" && x.Product == "CNC" && x.Side == "S" {
			sell[x.UCC] = true
		}
	}
	ucc := ""
	for u := range mis {
		if sell[u] {
			c, _ := o.Client(ctx, T, u)
			if !contains(c.Segments, "FO") {
				ucc = u
				break
			}
		}
	}
	if ucc == "" {
		for u := range mis {
			if sell[u] {
				ucc = u
				break
			}
		}
	}
	if ucc == "" {
		t.Fatal("no suitable client")
	}
	d := NRIDetails{UCC: ucc, Account: "NRE", Bank: "HDFC Bank", Branch: "Fort, Mumbai", PISAccount: "PIS0012345", Permission: "PIS/2026/77", PermDate: "2026-01-10", Country: "UAE", CPCode: "CP12345"}
	if _, err := o.SetNRI(ctx, T, "priya.nair", d); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker registered NRI: %v", err)
	}
	if _, err := o.SetNRI(ctx, T, "farhan.khan", NRIDetails{UCC: ucc, Account: "XYZ"}); err == nil {
		t.Fatal("bad account type")
	}
	if _, err := o.SetNRI(ctx, T, "farhan.khan", d); err != nil {
		t.Fatal(err)
	}
	if c, _ := o.Client(ctx, T, ucc); c.Type != "NRI" || !contains(c.CSC, "NRI") {
		t.Fatalf("client not NRI: %s %v", c.Type, c.CSC)
	}
	v, err := o.NRI(ctx, T, "vikram.shah", D)
	if err != nil || len(v.Rows) != 1 {
		t.Fatalf("nri: %d %v", len(v.Rows), err)
	}
	intraday := false
	for _, x := range v.Violations {
		intraday = intraday || strings.Contains(x.Rule, "intraday")
	}
	if !intraday {
		t.Fatalf("intraday not caught: %+v", v.Violations)
	}
	als, _ := o.Alerts(ctx, T)
	found := false
	for _, a := range als {
		found = found || (a.UCC == ucc && a.Type == "NRI trading rule")
	}
	if !found {
		t.Fatal("no alert raised")
	}
	f, n, err := o.PISFile(ctx, T, D)
	if err != nil || n == 0 || !strings.Contains(string(f), "PIS0012345") {
		t.Fatalf("PIS file: %d %v", n, err)
	}
	if len(v.TDS) == 0 {
		t.Fatal("no TDS rows for the day's sales")
	}
	for _, x := range v.TDS {
		if x.Gain > 0 && x.Category != CatLTCG && x.TDS == 0 {
			t.Fatalf("no TDS on a short-term gain: %+v", x)
		}
		if x.Category == CatLTCG && x.Exempt == 0 && x.Gain > 0 {
			t.Fatalf("LTCG exemption not applied: %+v", x)
		}
	}
	// Caution list.
	var buy string
	for _, x := range ts {
		if x.UCC == ucc && x.Segment == "CM" && x.Side == "B" {
			buy = x.Symbol
		}
	}
	if buy != "" {
		if _, err := o.LoadFile(ctx, T, "vikram.shah", "NRI-CAUTION", D, "rbi.csv", []byte("ISIN,Symbol,Status\n,"+buy+",ban\n")); err != nil {
			t.Fatal(err)
		}
		v, _ = o.NRI(ctx, T, "vikram.shah", D)
		ban := false
		for _, x := range v.Violations {
			ban = ban || strings.Contains(x.Rule, "ban list")
		}
		if !ban {
			t.Fatal("ban list not applied")
		}
	}
}
