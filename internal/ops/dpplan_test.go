package ops

import (
	"strings"
	"testing"

	"ledgerline/internal/money"
)

// A DP account on a tariff plan pays the plan's rates: maintenance a year
// in advance from the paid-up date, transfers on their lines (the firm's
// own trades free unless the plan says), the version in force; accounts on
// no plan pay the firm tariff. Re-loading DP-ACCOUNTS keeps the plans.
func TestDPTariffPlans(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	var dm Client
	for _, c := range cs {
		if len(c.Demat.ID) == 16 && c.Status == "Active" && c.Type != "Corporate" {
			dm = c
			break
		}
	}
	if dm.UCC == "" {
		t.Skip("no CDSL client in the sample")
	}
	load := func(src, data string) []string {
		t.Helper()
		r, err := o.LoadFile(ctx, T, "farhan.khan", src, "", strings.ToLower(src)+".csv", []byte(data))
		if err != nil {
			t.Fatalf("%s: %v %+v", src, err, r.Errors)
		}
		var w []string
		for _, e := range r.Warnings {
			w = append(w, e.Msg)
		}
		return w
	}
	load("DP-ACCOUNTS", "UCC,BOID,Depository,Status,Opened\n"+dm.UCC+","+dm.Demat.ID+",CDSL,Active,2020-01-10\n")
	tf, _ := o.DPTariffNow(ctx, T)
	tf.ChargeFree = []ChargeFreeAccount{{ID: "1203320000000001", Label: "Pool"}}
	if _, err := o.SaveDPTariff(ctx, T, "farhan.khan", tf); err != nil {
		t.Fatal(err)
	}
	w := load("DP-TARIFFS", strings.Join([]string{
		"Plan,From,Code,Name,Pool,AMCBilling,Charge,Flat,Pct,PerUnit,Min,Max,OwnFlat,OwnPct,OwnPerUnit,OwnMin,OwnMax,Mail,SpecialFlat,SpecialPct,SpecialPerUnit,SpecialMin,SpecialMax",
		"B001,2020-08-01,AM,Own broking clients,N,Yearly,,,,,200,,,,,,,,,,,,",
		"B001,2020-08-01,0015,,,,,,,,500,,,,,,,,,,,,",
		"B001,2020-08-01,DCM,,,,,10,,,,,,,,,,,,,,,",
		"B001,2020-08-01,ODTA,,,,,,,,15,,,,,15,,,,,,,",
		"B001,2020-08-01,2201,,,,,,,3,,,,,,,,40,,,,,",
		"B001,2020-08-01,2230,,,,,,0.1,,25,,,,,,,,,,,25,",
		"B001,2026-11-01,AM,,,,,,,,300,,,,,,,,,,,,",
		"B001,2026-11-01,DCM,,,,,10,,,,,,,,,,,,,,,",
		"B001,2026-11-01,ODTA,,,,,,,,15,,,,,15,,,,,,,",
		"B001,2026-11-01,XYZ1,,,,Something old,1,,,,,,,,,,,,,,,",
		"C003,1900-01-01,AM,Uncertain,N,Monthly,,,,,,,,,,,,,,,,,",
	}, "\n")+"\n")
	if len(w) != 1 || !strings.Contains(w[0], "XYZ1") {
		t.Fatalf("tariff warnings %v", w)
	}
	ps, _ := o.DPPlans(ctx, T)
	if len(ps) != 2 || ps[0].Code != "B001" || ps[0].AMCBilling != AMCYearly || len(ps[0].Versions) != 2 || ps[0].Versions[0].AMCYear != 20000 || ps[0].Versions[0].CorporateAMCYear != 50000 || ps[0].Versions[1].AMCYear != 30000 {
		t.Fatalf("plans %+v", ps)
	}
	if l, _ := ps[0].Versions[0].line("2230"); l.Pct.Cmp(money.MustDec("0.1")) != 0 || l.Special == nil || l.Special.Pct.Sign() != 0 || l.Own != nil {
		t.Fatalf("pledge line %+v", l)
	}
	if l, _ := ps[0].Versions[0].line("ODTA"); l.Own == nil || l.Own.Min != 1500 {
		t.Fatalf("off-market line %+v", l)
	}
	load("DP-TARIFF-ACCOUNTS", "BOID,Plan,From,AMCPaidUpto\n"+dm.Demat.ID+",C003,2019-01-01,\n"+dm.Demat.ID+",B001,2026-06-01,2026-11-10\n9999999999999999,B001,2026-06-01,\n")
	if r, err := o.LoadFile(ctx, T, "farhan.khan", "DP-TARIFF-ACCOUNTS", "", "x.csv", []byte("BOID,Plan,From\n"+dm.Demat.ID+",NOPE,2026-06-01\n")); err == nil || len(r.Errors) == 0 {
		t.Fatal("an unknown plan was accepted")
	}
	b, _ := o.boAccount(ctx, T, dm.Demat.ID)
	if b.planAt("2026-11-15") != "B001" || b.planAt("2025-01-01") != "C003" || b.AMCPaidUpto != "2026-11-10" {
		t.Fatalf("account %+v", b)
	}
	day := "2026-11-05"
	f := xwrite(t, "cdsl-transactions",
		// pay-in to the firm's pool: own trade, the plan has no own rate: free
		xr("", "Src", "CDSL", "TxnTyp", "IDO", "TxnID", "91", "LineNb", "1", "ClntId", dm.Demat.ID, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "10.000", "TxnCd", "DBT", "CntrBnfclOwnrId", "1203320000000001"),
		// off-market delivery: ₹15 minimum
		xr("", "Src", "CDSL", "TxnTyp", "WTR", "TxnID", "93", "LineNb", "1", "ClntId", dm.Demat.ID, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "3.000", "TxnCd", "DBT", "CntrBnfclOwnrId", "1208870000000099"),
		// off-market receipt: no charge on the plan
		xr("", "Src", "CDSL", "TxnTyp", "WTR", "TxnID", "94", "LineNb", "1", "ClntId", dm.Demat.ID, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "2.000", "TxnCd", "CRD", "CntrBnfclOwnrId", "1208870000000099"))
	loadX(t, o, "COD_EXP_12345_123456_F_202611051830_001_P1.csv", f)
	dv, _ := o.DP(ctx, T, "2026-11-30")
	for _, r := range dv.Rows {
		if r.BOID == dm.Demat.ID && (r.Plan != "B001" || r.AMC != 30000) {
			t.Fatalf("DP desk row %+v", r)
		}
	}
	if len(dv.Plans) != 2 {
		t.Fatalf("plans on the desk %d", len(dv.Plans))
	}
	bills, err := o.RunDPBilling(ctx, T, "farhan.khan", "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	var bill DPBill
	for _, x := range bills {
		if x.UCC == dm.UCC {
			bill = x
		}
	}
	got := map[string]DPBillLine{}
	for _, l := range bill.Lines {
		got[l.What] = l
	}
	amc, od := got["Annual maintenance, 2026-11-11 to 2027-11-10"], got["Off-market delivery (within the depository)"]
	if amc.Amount != 30000 || od.Count != 1 || od.Amount != 1500 || bill.Free != 1 || len(bill.Lines) != 2 {
		t.Fatalf("bill %+v", bill)
	}
	if b, _ := o.boAccount(ctx, T, dm.Demat.ID); b.AMCPaidUpto != "2027-11-10" {
		t.Fatalf("AMC paid up to %q", b.AMCPaidUpto)
	}
	// re-loading the DP's accounts keeps the plan history
	load("DP-ACCOUNTS", "UCC,BOID,Depository,Status,Opened,DDPI\n"+dm.UCC+","+dm.Demat.ID+",CDSL,Active,2020-01-10,Y\n")
	if b, _ := o.boAccount(ctx, T, dm.Demat.ID); len(b.Plans) != 2 || b.AMCPaidUpto != "2027-11-10" || !b.DDPI {
		t.Fatalf("after DP-ACCOUNTS %+v", b)
	}
	// a checker moves the account to another plan from a date
	if _, err := o.SetDPPlan(ctx, T, "farhan.khan", dm.Demat.ID, "NOPE", "2026-12-01"); err == nil {
		t.Fatal("an unknown plan was set")
	}
	if b, err := o.SetDPPlan(ctx, T, "farhan.khan", dm.Demat.ID, "c003", "2026-12-01"); err != nil || b.planAt("2026-12-15") != "C003" || b.planAt("2026-11-15") != "B001" {
		t.Fatalf("set plan %+v %v", b.Plans, err)
	}
}

// Monthly maintenance is a twelfth of the year's, the corporate amount on
// top for a body corporate; a demat request pays per certificate and the
// courier once.
func TestDPPlanBillMonthly(t *testing.T) {
	p := DPPlan{Code: "P", AMCBilling: AMCMonthly}
	v := DPPlanVersion{From: "2020-01-01", AMCYear: 24000, CorporateAMCYear: 12000, Lines: []DPChargeLine{{Code: "2201", DPRate: DPRate{PerUnit: 300}, Mail: 4000}}}
	r := BORow{BOAccount: BOAccount{BOID: "1234567800000001", Status: "Active"}}
	var lines []DPBillLine
	var free int64
	ts := []DPTxn{{Date: "2026-11-03", Qty: money.MustDec("100"), Kind: "Demat credit", Ref: "DMR-1", Billable: true, Certs: 4},
		{Date: "2026-11-03", Qty: money.MustDec("50"), Kind: "Demat credit", Ref: "DMR-1", Billable: true, Certs: 2}}
	paid := dpPlanBill(p, v, DPTariff{}, nil, r, true, "2026-11", "2026-11-30", ts, func(DPTxn) bool { return true }, func(l DPBillLine) { lines = append(lines, l) }, &free)
	if paid != "" || len(lines) != 2 || lines[0].Amount != 3000 || lines[1].Count != 2 || lines[1].Amount != 300*6+4000 {
		t.Fatalf("lines %+v", lines)
	}
}
