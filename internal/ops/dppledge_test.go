package ops

import (
	"testing"

	"ledgerline/internal/money"
)

// Which row of a pledge's steps in CDSL's transaction file is the event:
// the combinations of CDSL's UDiFF catalogue (COD mapping sheet).
func TestCDSLPledgeEvent(t *testing.T) {
	for _, c := range []struct{ tp, cd, idr, st, want string }{
		{"PLG", "CPLBL", "NP", "SAPCPB", PledgeCreated},
		{"PLG", "CPLBL", "", "SAPCPB", PledgeCreated},
		{"PLG", "CPLBL", "MP", "MPACPB", MarginPledgeCreated},
		{"PLG", "CPLBL", "", "MPACPB", MarginPledgeCreated},
		{"PLG", "CRPLBL", "MR", "MRSPC", MarginRepledge},
		{"PCC", "DPLBL", "", "AUPLC", PledgeClosed},
		{"PCL", "DPLBL", "NP", "CLDACA", PledgeClosed},
		{"PCC", "DPLBL", "", "PACPB", MarginPledgeClosed},
		{"PCL", "DPLBL", "MP", "MPUAPC", MarginPledgeClosed},
		{"PIC", "DPLBL", "NP", "CONFCT", PledgeInvoked},
		{"PIC", "DPLBL", "MP", "PCADPB", MarginPledgeInvoked},
		{"PLG", "DPLBL", "", "PCADPB", MarginPledgeInvoked},
		{"PLG", "DPLBL", "", "RCADPB", PledgeInvoked}, // the margin re-pledge's pledge: no margin flag on the row
		// the other steps move setup or pledgee balances, or the free balance
		{"PLG", "CPLSBL", "", "SAPC", ""}, {"PLG", "DPSB", "", "SAPDPSB", ""}, {"PLG", "CPLGBL", "", "MPAPEB", ""}, {"PCC", "DPEB", "", "AUCPBO", ""},
		{"PCC", "DRPLBL", "MR", "RACRPB", ""}, {"PLG", "DBT", "", "SAPC", ""}, {"PCC", "CRD", "", "AUPLC", ""}, {"PLG", "SAPGEM", "", "SAPM", ""},
	} {
		if got := cdslPledgeEvent(c.tp, c.cd, c.idr, c.st); got != c.want {
			t.Errorf("%v: %q, want %q", c, got, c.want)
		}
	}
}

// The firm sets what it charges on each pledge event (pledge, margin
// pledge, re-pledge, unpledge, invocation), on its tariff and on each plan;
// a month's bill charges each event per ISIN, and only the pledge events:
// the moves into and out of the pledge are not debits or credits.
func TestDPPledgeCharges(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	var dm []Client
	for _, c := range cs {
		if len(c.Demat.ID) == 16 && c.Status == "Active" && c.Type != "Corporate" {
			dm = append(dm, c)
		}
	}
	if len(dm) < 2 {
		t.Skip("two CDSL clients needed")
	}
	if _, err := o.LoadFile(ctx, T, "farhan.khan", "DP-ACCOUNTS", "", "dpa.csv", []byte("UCC,BOID,Depository,Status,Opened\n"+
		dm[0].UCC+","+dm[0].Demat.ID+",CDSL,Active,2020-01-10\n"+dm[1].UCC+","+dm[1].Demat.ID+",CDSL,Active,2020-01-10\n")); err != nil {
		t.Fatal(err)
	}
	// the tariff: the old single pledge charge reads as creation and closure
	tf, _ := o.DPTariffNow(ctx, T)
	if len(tf.PledgeCharges) != 4 || tf.PledgeCharges[0].Code != PledgeCreated || tf.PledgeCharges[0].Flat != 2000 {
		t.Fatalf("default pledge charges %+v", tf.PledgeCharges)
	}
	tf.AMCYear, tf.DebitCharge, tf.DepositoryShare = 0, 0, 0
	tf.PledgeCharges = []DPChargeLine{{Code: "plg", DPRate: DPRate{Flat: 2000}}, {Code: MarginPledgeCreated, DPRate: DPRate{Pct: money.MustDec("0.1"), Min: 2500}},
		{Code: PledgeClosed, DPRate: DPRate{Flat: 5000}, Off: true}, {Code: MarginPledgeClosed}, {Code: PledgeInvoked, DPRate: DPRate{Flat: 10000, Max: 8000}}}
	if _, err := o.SaveDPTariff(ctx, T, "farhan.khan", tf); err != nil { // a maximum below the flat charge caps it
		t.Fatal(err)
	}
	bad := tf
	bad.PledgeCharges = []DPChargeLine{{Code: "2230", DPRate: DPRate{Flat: 100}}}
	if _, err := o.SaveDPTariff(ctx, T, "farhan.khan", bad); err == nil {
		t.Fatal("a pledge charge on an unknown event was saved")
	}
	bad.PledgeCharges = []DPChargeLine{{Code: PledgeCreated, DPRate: DPRate{Min: 5000, Max: 100}}}
	if _, err := o.SaveDPTariff(ctx, T, "farhan.khan", bad); err == nil {
		t.Fatal("a maximum below the minimum was saved")
	}
	tf, _ = o.DPTariffNow(ctx, T)
	if len(tf.PledgeCharges) != 4 || tf.PledgeCharges[2].Code != PledgeClosed || !tf.PledgeCharges[2].Off || tf.PledgeCharge != 0 {
		t.Fatalf("saved pledge charges %+v (old charge %v)", tf.PledgeCharges, tf.PledgeCharge)
	}
	day := "2026-11-06"
	a, b := dm[0].Demat.ID, dm[1].Demat.ID
	f := xwrite(t, "cdsl-transactions",
		// a pledge: free balance out (not a debit), set-up balance moves, then the pledge balance (the event)
		xr("", "Src", "CDSL", "TxnTyp", "PLG", "TxnID", "71", "LineNb", "1", "ClntId", a, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "10.000", "TxnCd", "DBT", "OrdSttsTo", "SAPC"),
		xr("", "Src", "CDSL", "TxnTyp", "PLG", "TxnID", "71", "LineNb", "2", "ClntId", a, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "10.000", "TxnCd", "CPLSBL", "OrdSttsTo", "SAPC"),
		xr("", "Src", "CDSL", "TxnTyp", "PLG", "TxnID", "71", "LineNb", "3", "ClntId", a, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "10.000", "TxnCd", "CPLBL", "PldgIdr", "NP", "OrdSttsTo", "SAPCPB", "Cnsdrtn", "12000.00"),
		xr("", "Src", "CDSL", "TxnTyp", "PLG", "TxnID", "72", "LineNb", "1", "ClntId", a, "ISIN", "INE111Q01019", "ExecDt", day, "Qty", "5.000", "TxnCd", "CPLBL", "PldgIdr", "NP", "OrdSttsTo", "SAPCPB"),
		// a margin pledge of ₹50,000: 0.1% is ₹50, above the ₹25 minimum
		xr("", "Src", "CDSL", "TxnTyp", "PLG", "TxnID", "73", "LineNb", "1", "ClntId", a, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "40.000", "TxnCd", "CPLBL", "PldgIdr", "MP", "OrdSttsTo", "MPACPB", "Cnsdrtn", "50000.00"),
		// re-pledged to the clearing corporation: no charge set
		xr("", "Src", "CDSL", "TxnTyp", "PLG", "TxnID", "73", "LineNb", "2", "ClntId", a, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "40.000", "TxnCd", "CRPLBL", "PldgIdr", "MR", "OrdSttsTo", "MRSPC"),
		// closed: not charged (the line is off); the shares back to the free balance: not a credit
		xr("", "Src", "CDSL", "TxnTyp", "PCC", "TxnID", "74", "LineNb", "1", "ClntId", a, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "10.000", "TxnCd", "DPLBL", "OrdSttsTo", "AUPLC"),
		xr("", "Src", "CDSL", "TxnTyp", "PCC", "TxnID", "74", "LineNb", "2", "ClntId", a, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "10.000", "TxnCd", "CRD", "OrdSttsTo", "AUPLC"),
		// invoked: ₹100 capped at ₹80
		xr("", "Src", "CDSL", "TxnTyp", "PIC", "TxnID", "75", "LineNb", "1", "ClntId", a, "ISIN", "INE111Q01019", "ExecDt", day, "Qty", "5.000", "TxnCd", "DPLBL", "PldgIdr", "NP", "OrdSttsTo", "CONFCT"),
		// the second client's margin pledge, on a plan
		xr("", "Src", "CDSL", "TxnTyp", "PLG", "TxnID", "76", "LineNb", "1", "ClntId", b, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "40.000", "TxnCd", "CPLBL", "PldgIdr", "MP", "OrdSttsTo", "MPACPB", "Cnsdrtn", "50000.00"),
		xr("", "Src", "CDSL", "TxnTyp", "PCC", "TxnID", "77", "LineNb", "1", "ClntId", b, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "40.000", "TxnCd", "DPLBL", "PldgIdr", "MP", "OrdSttsTo", "PACPB"))
	loadX(t, o, "COD_EXP_12345_123456_F_202611061830_001_P1.csv", f)
	ts, _ := o.DPTransactions(ctx, T, dm[0].UCC)
	events := map[string]int{}
	for _, x := range ts {
		events[x.Pledge]++
		if x.Pledge != "" && (x.Billable || x.Qty.Sign() <= 0) {
			t.Errorf("pledge event %+v", x)
		}
	}
	if events[PledgeCreated] != 2 || events[MarginPledgeCreated] != 1 || events[MarginRepledge] != 1 || events[PledgeClosed] != 1 || events[PledgeInvoked] != 1 || events[""] != 2 {
		t.Fatalf("events %v", events)
	}
	// the second client's account on a plan that charges margin pledges ₹30 and unpledges ₹10
	if _, err := o.LoadFile(ctx, T, "farhan.khan", "DP-TARIFFS", "", "t.csv", []byte("Plan,From,Code,Name,AMCBilling,Flat,Charged\nP1,2020-01-01,AM,Pledgers,Monthly,,\nP1,2020-01-01,PLGM,,,99,N\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := o.LoadFile(ctx, T, "farhan.khan", "DP-TARIFF-ACCOUNTS", "", "ta.csv", []byte("BOID,Plan,From\n"+b+",P1,2020-01-01\n")); err != nil {
		t.Fatal(err)
	}
	ps, _ := o.DPPlans(ctx, T)
	if l, _ := ps[0].Versions[0].line(MarginPledgeCreated); !l.Off || l.Flat != 9900 {
		t.Fatalf("plan line not charged %+v", l)
	}
	if _, err := o.SetDPPlanPledge(ctx, T, "farhan.khan", "P1", "2026-11-01", []DPChargeLine{{Code: "INVX"}}); err == nil {
		t.Fatal("an unknown pledge event on a plan")
	}
	p, err := o.SetDPPlanPledge(ctx, T, "farhan.khan", "p1", "2026-11-01", []DPChargeLine{{Code: MarginPledgeCreated, DPRate: DPRate{Flat: 3000}}, {Code: MarginPledgeClosed, DPRate: DPRate{Flat: 1000}}, {Code: PledgeCreated}})
	if err != nil || len(p.Versions) != 2 || p.Versions[1].From != "2026-11-01" || len(p.Versions[1].Lines) != 2 || len(p.Versions[0].Lines) != 1 {
		t.Fatalf("plan pledge charges %+v %v", p, err)
	}
	bills, err := o.RunDPBilling(ctx, T, "farhan.khan", "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]DPBillLine{}
	for _, x := range bills {
		got[x.UCC] = map[string]DPBillLine{}
		for _, l := range x.Lines {
			got[x.UCC][l.What] = l
		}
	}
	ga, gb := got[dm[0].UCC], got[dm[1].UCC]
	if l := ga["Pledge created (per ISIN)"]; l.Count != 2 || l.Amount != 4000 {
		t.Errorf("pledges %+v", l)
	}
	if l := ga["Margin pledge created (per ISIN)"]; l.Count != 1 || l.Amount != 5000 {
		t.Errorf("margin pledge %+v", l)
	}
	if l := ga["Pledge invoked (per ISIN)"]; l.Count != 1 || l.Amount != 8000 {
		t.Errorf("invocation %+v", l)
	}
	if len(ga) != 3 {
		t.Errorf("firm tariff bill %v", ga)
	}
	if len(gb) != 2 || gb["Margin pledge created (per ISIN)"].Amount != 3000 || gb["Margin unpledge (per ISIN)"].Amount != 1000 {
		t.Errorf("plan bill %v", gb)
	}
}
