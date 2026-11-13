package sharepro

import (
	"strings"
	"testing"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

// The DP's tariff baskets become plans, one row a charge of a dated
// version (NSDL codes as CDSL's, the RATE of a demat per certificate, the
// CM set the own-trade rates, the SP set kept), and each DP account's
// basket history with the date its maintenance is paid up to.
func TestDPTariffs(t *testing.T) {
	r, co := runCurrent(t, migrate.Plan{})
	_, rows := readCSV(t, r, "firms/02/master/DP-TARIFFS.csv")
	var got []string
	by := map[string]map[string]string{}
	for _, x := range rows {
		k := x["Plan"] + "|" + x["From"] + "|" + x["Code"]
		got = append(got, k)
		by[k] = x
	}
	want := "B001|2020-08-01|0015 B001|2020-08-01|2201 B001|2020-08-01|AM B001|2020-08-01|DCM B001|2020-08-01|ODTA B001|2026-04-01|AM B001|2026-04-01|ODTR P001|2019-01-01|DCM"
	if strings.Join(got, " ") != want {
		t.Fatalf("tariff rows %v", got)
	}
	for k, w := range map[string]map[string]string{
		"B001|2020-08-01|AM":   {"Name": "Standard", "Pool": "N", "AMCBilling": "Yearly", "Flat": "200.00"},
		"B001|2020-08-01|2201": {"PerUnit": "3.00", "Pct": "", "Mail": "40.00", "SpecialPerUnit": "3.00"},
		"B001|2020-08-01|DCM":  {"Pct": "0.04", "Min": "10.00", "OwnPct": "", "OwnMin": ""},
		"B001|2020-08-01|ODTA": {"Pct": "0.02", "Min": "15.00", "OwnMin": "12.00", "SpecialPct": "0.01", "SpecialMin": "15.00"},
		"B001|2026-04-01|ODTR": {"Flat": "25.00"},
		"P001|2019-01-01|DCM":  {"Pool": "Y", "AMCBilling": "Monthly", "Flat": "5.00"},
	} {
		for col, v := range w {
			if by[k][col] != v {
				t.Errorf("%s %s = %q, want %q", k, col, by[k][col], v)
			}
		}
	}
	_, as := readCSV(t, r, "firms/02/master/DP-TARIFF-ACCOUNTS.csv")
	got = got[:0]
	for _, x := range as {
		got = append(got, x["BOID"]+"|"+x["Plan"]+"|"+x["From"]+"|"+x["AMCPaidUpto"])
	}
	if strings.Join(got, " ") != "1234567800000001|B001|2020-01-01|2026-12-31 1234567800000001|P001|2026-06-01| 1234567800000009|B001|2021-02-01|9999-01-01" {
		t.Fatalf("account plans %v", got)
	}
	for kind, n := range map[string]int{"Tariff basket E001 has no rates": 1, "1 charges have a different SP rate": 1, "1 tariffs in DPCHARGE are keyed by an account": 1,
		"1 NSDL charge codes": 1, "2 tariff plans (SharePro baskets) with 3 dated versions": 1, `name basket "COO3"`: 1, `name basket "(blank)"`: 1,
		"name basket E001, which has no rates": 1, "1 basket assignments in DPCLBASKET have no effective date": 1, "1 basket assignments are for accounts not migrated": 1,
		"DP accounts have no basket": 1} {
		if i := issues(r, "DP tariffs", kind); len(i) != n {
			t.Errorf("%q: %+v", kind, i)
		}
	}
	for name, v := range map[string]string{pkg.CheckDPTariffVersions: "3", pkg.CheckDPTariffAccounts: "2", pkg.CheckDPAMCPaidAhead: "2"} {
		if check(r, name) != v {
			t.Errorf("check %s = %s", name, check(r, name))
		}
	}
	for _, tag := range []string{"check-dp-tariff-accounts", "check-dp-amc-ahead"} {
		q := co.asked(tag)
		if len(q) != 1 || !strings.Contains(q[0], "FROM CLLIST l WHERE l.CO IN ('06')") || !strings.Contains(q[0], "FROM DPCHARGE ch") {
			t.Errorf("%s %v", tag, q)
		}
	}
}

// With no tariff tables there is nothing to write and no check.
func TestDPTariffsMissingTables(t *testing.T) {
	co := companyFake("TT2627")
	delete(co.tables, "DPCLBSKT")
	r, _ := runCurrentOn(t, migrate.Plan{}, co)
	if r.Has("firms/02/master/DP-TARIFFS.csv") || check(r, pkg.CheckDPTariffVersions) != "<none>" {
		t.Errorf("a check with no tariff tables")
	}
}
