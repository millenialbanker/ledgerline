package ops

import (
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

func TestCollateralSection(t *testing.T) {
	r := MarginRow{EOD: money.MustDec("150000"), Funds: money.MustDec("100000"), Collateral: money.MustDec("80000"), Short: money.Dec{}}
	s := collateralSection(r)
	if len(s.Rows) != 4 || s.Rows[0][2] != formatINR(money.MustDec("100000")) || s.Rows[3][2] != formatINR(money.MustDec("50000")) || s.Rows[3][3] != formatINR(money.MustDec("30000")) {
		t.Fatalf("rows %v", s.Rows)
	}
	if s.Rows[1][4] != "None held" || s.Rows[2][4] != "None held" || s.Totals[4] != "Available" {
		t.Fatalf("FDR/BG and status %v %v", s.Rows, s.Totals)
	}
	r.Short = money.MustDec("5000")
	if s := collateralSection(r); !strings.HasPrefix(s.Totals[4], "Due") {
		t.Fatalf("a shortfall %v", s.Totals)
	}
}

func TestPledgeControls(t *testing.T) {
	o, ctx := newOps(t)
	siClock(o, siAt("2026-12-04", "19:00"))
	put := func(coll, id string, v any) {
		t.Helper()
		if err := docs.Upsert(ctx, o.Docs, T, coll, id, v, "t"); err != nil {
			t.Fatal(err)
		}
	}
	u := "PLC01"
	put(cClient, u, Client{UCC: u, Name: "Pledge Test", Type: "Individual", PAN: "ABCPP7777G", Status: "Active", Segments: []string{"CM"}, City: "Pune", Email: "pledge@example.test", Demat: Demat{Depository: "CDSL", ID: "1209000000000009"}})
	put(cPledge, "PL-1", PledgeRequest{ID: "PL-1", UCC: u, ISIN: "INE000A01011", Symbol: "SAMPLE", Qty: 100, Time: "2026-12-04 10:00", Status: "Accepted", PSN: "PSN0001", Via: cplVia, Kind: "MP",
		RequestedBy: "client", DecidedBy: "farhan.khan", DoneAt: "2026-12-04T10:05:00+05:30"})
	put(cPledge, "PL-2", PledgeRequest{ID: "PL-2", UCC: u, ISIN: "INE000A01029", Symbol: "OTHER", Qty: 10, Time: "2026-11-28 10:00", Status: "Pending", Via: cplVia, Kind: "MP", RequestedBy: "client"})
	put(cCPLRelease, "UN-1", PledgeRelease{ID: "UN-1", Kind: "UN", UCC: u, PSN: "PSN0001", ISIN: "INE000A01011", Symbol: "SAMPLE", Qty: 10, Status: "Done", RequestedBy: "client", ApprovedBy: "farhan.khan",
		At: "2026-12-04T11:00:00+05:30", DoneAt: "2026-12-04T11:10:00+05:30", Source: "Client"})

	// The day's reconciliation: received, executed, open.
	rc := o.PledgeDailyRecon(ctx, T, "2026-12-04")
	if rc.Rows[0].Received != 1 || rc.Rows[0].Executed != 1 || rc.Rows[0].Pending != 1 || rc.Rows[3].Executed != 1 || len(rc.Open) != 1 || rc.Open[0].ID != "PL-2" {
		t.Fatalf("recon %+v", rc)
	}
	siWant(t, siOne(t, o, ctx, "pledge_recon", "2026-12-04"), SIFail, "PL-2 open since 28 Nov")
	o.pledgeControlsTick(ctx, T, o.now())
	if w := workFor(t, o, wPledgeCtl, "recon:2026-12-04"); w.Status != "Open" {
		t.Fatalf("no reconciliation work item %+v", w)
	}

	// The set-up letter of PL-1.
	if due := o.pledgeLettersDue(ctx, T); len(due) != 1 || due[0].ID != "PL-1" {
		t.Fatalf("letters due %+v", due)
	}
	siWant(t, siOne(t, o, ctx, "pledge_letters", "2026-12-04"), SIFail, "no letter for PL-1")
	rep, err := o.pledgeLetter(ctx, T, mustPledge(t, o, "PL-1"))
	if err != nil || !strings.Contains(rep.Sections[0].Rows[4][1], "PSN0001") {
		t.Fatalf("letter %+v %v", rep, err)
	}
	res, err := o.SendPledgeLetters(ctx, T, "priya.nair")
	if err != nil || res.Sent+res.Held+res.Paper+res.Bounced != 1 {
		t.Fatalf("send %+v %v", res, err)
	}
	if _, err := o.SendPledgeLetters(ctx, T, "vikram.shah"); err == nil {
		t.Fatal("compliance sent letters")
	}

	// The collateral register: the pledge with its acknowledgement and the
	// return to the client.
	rows, err := o.CollateralRegister(ctx, T, "anita.rao", "2026-12-01", "2026-12-04")
	if err != nil || len(rows) != 2 || !strings.Contains(rows[0].Ack, "OTP") || rows[1].Name != "Pledge Test" {
		t.Fatalf("register %+v %v", rows, err)
	}
	if name, b, err := o.CollateralRegisterCSV(ctx, T, "anita.rao", "2026-12-01", "2026-12-04"); err != nil || !strings.HasPrefix(name, "CollateralRegister_") || !strings.Contains(string(b), "Returned to the client") {
		t.Fatalf("csv %s %v", name, err)
	}

	// Locked-in shares are not invoked.
	put(cLockIn, lockKey(u, "INE000A01011", ""), LockIn{UCC: u, ISIN: "INE000A01011", Qty: 80, Until: "2027-03-31", Source: "Recorded", Date: "2026-12-04"})
	a, ok := o.cplPSN(ctx, T, "PSN0001")
	if !ok || a.Free != 90 {
		t.Fatalf("PSN %+v", a)
	}
	if why := o.lockInWhy(ctx, T, a, 20, "2026-12-04"); !strings.Contains(why, "locked in") {
		t.Fatalf("20 of 90 with 80 locked in: %q", why)
	}
	if why := o.lockInWhy(ctx, T, a, 10, "2026-12-04"); why != "" {
		t.Fatalf("10 free shares: %q", why)
	}
	if why := o.lockInWhy(ctx, T, a, 90, "2027-04-01"); why != "" {
		t.Fatalf("after the lock-in: %q", why)
	}

	// Margin pledge charges at the depository's cost.
	siWant(t, siOne(t, o, ctx, "pledge_charges_at_cost", "2026-12-04"), SISkipped, "no costs recorded")
	if _, err := o.SavePledgeCosts(ctx, T, "farhan.khan", PledgeCosts{Costs: map[string]money.Amount{MarginPledgeCreated: 1000, MarginPledgeClosed: 1000}, Source: "CDSL tariff (made up for the test)"}); err != nil {
		t.Fatal(err)
	}
	if is, _ := o.pledgeChargeIssues(ctx, T, "2026-12-04"); len(is) != 2 || !strings.Contains(is[0].What, "above") {
		t.Fatalf("issues %+v", is)
	}
	siWant(t, siOne(t, o, ctx, "pledge_charges_at_cost", "2026-12-04"), SIFail, "the tariff charges more than CDSL")
	if _, err := o.SavePledgeCosts(ctx, T, "farhan.khan", PledgeCosts{Costs: map[string]money.Amount{"XYZ": 1}, Source: "x"}); err == nil {
		t.Fatal("a cost for an unknown event")
	}
}

func mustPledge(t *testing.T, o *Ops, id string) PledgeRequest {
	t.Helper()
	p, _, err := docs.Get[PledgeRequest](t.Context(), o.Docs, T, cPledge, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The confiscation-cum-early pay-in of sold CUSPA lines by 9:45 a.m. on
// the pay-in day.
func TestCEPCutoff(t *testing.T) {
	o, ctx := newOps(t)
	siClock(o, siAt("2026-12-04", "09:30"))
	put := func(coll, id string, v any) {
		if err := docs.Upsert(ctx, o.Docs, T, coll, id, v, "t"); err != nil {
			t.Fatal(err)
		}
	}
	put(cCUSPA, "CU-1", CUSPAItem{ID: "CU-1", UCC: "X1", Symbol: "AAA", Sold: 5, SoldOn: "2026-12-02", Invoked: 5})
	put(cCUSPA, "CU-2", CUSPAItem{ID: "CU-2", UCC: "X2", Symbol: "BBB", Sold: 5, SoldOn: "2026-12-02", Invoked: 5})
	put(cCUSPA, "CU-3", CUSPAItem{ID: "CU-3", UCC: "X3", Symbol: "CCC", Sold: 5, SoldOn: "2026-12-03", Invoked: 5})
	put(cCPLRelease, "CF-1", PledgeRelease{ID: "CF-1", Kind: "CF", CUSPA: "CU-1", Status: "Done", DoneAt: "2026-12-03T09:40:00+05:30"})
	put(cCPLRelease, "CF-2", PledgeRelease{ID: "CF-2", Kind: "CF", CUSPA: "CU-2", Status: "Done", DoneAt: "2026-12-03T10:15:00+05:30"})
	got := map[string]string{}
	for _, r := range o.cepRows(ctx, T, "2026-12-01", "2026-12-04", o.now()) {
		got[r.CUSPA] = r.State
	}
	if got["CU-1"] != "On time" || got["CU-2"] != "Late" || got["CU-3"] != "Due" {
		t.Fatalf("states %v", got)
	}
	o.pledgeControlsTick(ctx, T, o.now())
	if w := workFor(t, o, wPledgeCtl, "cep:CU-3:2026-12-04"); w.Status != "Open" {
		t.Fatalf("no 9:45 work item %+v", w)
	}
	siWant(t, siOne(t, o, ctx, "cuspa_cep_cutoff", "2026-12-04"), SIFail, "CU-2 confiscated after 9:45")
}

// Client code modifications: the register and the ENIT reason code.
func TestCCMRegister(t *testing.T) {
	o, ctx := newOps(t)
	siClock(o, siAt("2026-12-04", "11:00"))
	c := CCM{TradeKey: "NSE|CM|2026-12-02|T1", Date: "2026-12-02", Exchange: "NSE", Segment: "CM", TradeNo: "T1", From: "AB001", To: "AB002", Reason: "Punching error", By: "priya.nair", At: "10:00:00"}
	if err := docs.Upsert(ctx, o.Docs, T, cCCM, c.TradeKey, c, "t"); err != nil {
		t.Fatal(err)
	}
	o.ccmRaised(ctx, T, c)
	siWant(t, siOne(t, o, ctx, "ccm_enit", "2026-12-04"), SIFail, "not reported on ENIT")
	if _, err := o.RecordCCMReport(ctx, T, "priya.nair", c.TradeKey, "01", "2026-12-02", ""); err == nil {
		t.Fatal("a maker recorded the ENIT code")
	}
	if _, err := o.RecordCCMReport(ctx, T, "farhan.khan", c.TradeKey, "01", "2026-12-02", "ENIT ref 9"); err != nil {
		t.Fatal(err)
	}
	if w := workFor(t, o, "ccm-enit", c.TradeKey); w.Status == "Open" {
		t.Fatal("ENIT work item still open")
	}
	rs, err := o.CCMRegister(ctx, T, "vikram.shah", "2026-12-01", "2026-12-04")
	if err != nil || len(rs) != 1 || rs[0].ENITCode != "01" {
		t.Fatalf("register %+v %v", rs, err)
	}
	siWant(t, siOne(t, o, ctx, "ccm_enit", "2026-12-04"), SIPass, "reported")

	// No inter-depository slip for a day that is not a trading day.
	if why := o.idtHolidayWhy(ctx, T, DIS{Kind: "Inter-depository", ExecDate: "2026-12-05"}); !strings.Contains(why, "not a trading day") {
		t.Fatalf("a Saturday: %q", why)
	}
	if why := o.idtHolidayWhy(ctx, T, DIS{Kind: "Inter-depository", ExecDate: "2026-12-04"}); why != "" {
		t.Fatalf("a Friday: %q", why)
	}
	if why := o.idtHolidayWhy(ctx, T, DIS{Kind: "Off-market", ExecDate: "2026-12-05"}); why != "" {
		t.Fatalf("an off-market slip: %q", why)
	}
}
