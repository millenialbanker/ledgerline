package sharepro

import (
	"strings"
	"testing"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

// The DP's tariff baskets become plans, one row a charge of a dated
// version (SharePro's "no date" 1900-01-01: from the start) (NSDL codes as CDSL's, the RATE of a demat per certificate, the
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
	want := "B001|2020-08-01|0015 B001|2020-08-01|2201 B001|2020-08-01|AM B001|2020-08-01|DCM B001|2020-08-01|INV B001|2020-08-01|INVM B001|2020-08-01|ODTA " +
		"B001|2020-08-01|PLG B001|2020-08-01|PLGM B001|2020-08-01|UNP B001|2020-08-01|UNPM B001|2026-04-01|AM B001|2026-04-01|ODTR P001|1900-01-01|DCM P001|2019-01-01|DCM Z001|1900-01-01|AM"
	if strings.Join(got, " ") != want {
		t.Fatalf("tariff rows %v", got)
	}
	for k, w := range map[string]map[string]string{
		"B001|2020-08-01|AM":   {"Name": "Standard", "Pool": "N", "AMCBilling": "Yearly", "Flat": "200.00"},
		"B001|2020-08-01|2201": {"PerUnit": "3.00", "Pct": "", "Mail": "40.00", "SpecialPerUnit": "3.00"},
		"B001|2020-08-01|DCM":  {"Pct": "0.04", "Min": "10.00", "OwnPct": "", "OwnMin": ""},
		"B001|2020-08-01|ODTA": {"Pct": "0.02", "Min": "15.00", "OwnMin": "12.00", "SpecialPct": "0.01", "SpecialMin": "15.00"},
		"B001|2026-04-01|ODTR": {"Flat": "25.00"},
		// SharePro charges no pledge creation (DPTRTYPE has no CHARGE_ON for 2230), closures and invocations at 2280's rate
		"B001|2020-08-01|PLG":  {"Pct": "0.1", "Min": "50.00", "Charged": "N", "SpecialPct": "", "OwnMin": ""},
		"B001|2020-08-01|PLGM": {"Pct": "0.1", "Min": "50.00", "Charged": "N"},
		"B001|2020-08-01|UNP":  {"Pct": "0.1", "Min": "50.00", "Charged": "", "OwnMin": ""},
		"B001|2020-08-01|INVM": {"Pct": "0.1", "Min": "50.00", "Charged": ""},
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
	if strings.Join(got, " ") != "1234567800000001|B001|2020-01-01|2026-12-31 1234567800000001|P001|2026-06-01| 1234567800000003|Z001|1900-01-01| 1234567800000009|B001|2021-02-01|9999-01-01" {
		t.Fatalf("account plans %v", got)
	}
	for kind, n := range map[string]int{"Tariff basket E001 has no rates": 1, "1 charges have a different SP rate": 1, "1 tariffs in DPCHARGE are keyed by an account": 1,
		"1 NSDL charge codes": 1, "3 tariff plans (SharePro baskets) with 5 dated versions": 1, `name basket "COO3"`: 1, `name basket "(blank)"`: 1,
		"name basket E001, which has no rates": 1, "1 basket assignments in DPCLBASKET have no effective date": 1, "1 basket assignments are for accounts not migrated": 1,
		"DP accounts have no basket": 1, "1 charges on SharePro's own pledge instruction codes": 1,
		"pledge and margin pledge created (2230) not charged (no CHARGE_ON in DPTRTYPE); pledge closed or invoked (2280) charged": 1} {
		if i := issues(r, "DP tariffs", kind); len(i) != n {
			t.Errorf("%q: %+v", kind, i)
		}
	}
	for name, v := range map[string]string{pkg.CheckDPTariffVersions: "5", pkg.CheckDPTariffAccounts: "3", pkg.CheckDPAMCPaidAhead: "2"} {
		if check(r, name) != v {
			t.Errorf("check %s = %s", name, check(r, name))
		}
	}
	// SharePro has no firm-wide tariff: the firm fills it in; the pledge
	// charges are known (rates and DPTRTYPE)
	if f := r.Manifest.FillIn; len(f) != 1 || f[0].Item != pkg.FillDPFirmTariff || f[0].Firm != "02" || !strings.Contains(f[0].Why, "Accounts on no plan (") {
		t.Errorf("fill in %+v", f)
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
	if f := r.Manifest.FillIn; len(f) != 2 || f[0].Item != pkg.FillDPFirmTariff || f[1].Item != pkg.FillDPPledgeCharges || len(f[1].Plans) != 0 {
		t.Errorf("fill in %+v", f)
	}
}

// What the backup cannot say about pledge charges the firm is asked to
// fill in: no basket has pledge rows, or DPTRTYPE does not say whether they
// are charged.
func TestDPPledgeChargesUnknown(t *testing.T) {
	co := companyFake("TT2627")
	all := co.rows["dp-charges"]
	co.rows["dp-charges"] = func(q string) []migrate.Row {
		var out []migrate.Row
		for _, x := range all(q) {
			if c := x["trancode"]; c != "2230" && c != "2280" {
				out = append(out, x)
			}
		}
		return out
	}
	r, _ := runCurrentOn(t, migrate.Plan{}, co)
	if f := r.Manifest.FillIn; len(f) != 2 || f[0].Item != pkg.FillDPPledgeCharges || strings.Join(f[0].Plans, ",") != "B001,P001,Z001" || !strings.Contains(f[0].Why, "no pledge or unpledge charges") {
		t.Errorf("no pledge rows: fill in %+v", f)
	}
	co = companyFake("TT2627")
	delete(co.tables, "DPTRTYPE")
	r, _ = runCurrentOn(t, migrate.Plan{}, co)
	if f := r.Manifest.FillIn; len(f) != 2 || f[0].Item != pkg.FillDPPledgeCharges || strings.Join(f[0].Plans, ",") != "B001" || !strings.Contains(f[0].Why, "do not say whether pledges are charged") {
		t.Errorf("no DPTRTYPE: fill in %+v", f)
	}
	if len(issues(r, "DP tariffs", "SharePro charges pledges on the depository's rows")) != 0 {
		t.Error("a pledge warning without DPTRTYPE")
	}
}
