package ops

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// Made-up data only: the sample day's clients with their details changed
// here, and invented BO IDs, alert numbers and amounts.

var dsPDF = []byte("%PDF-1.4\n% made-up bank statement\n1 0 obj << >> endobj\ntrailer << >>\n%%EOF\n")

func TestSurvImportAndCaseWork(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	cs, _ := o.Clients(ctx, T)
	c := cs[0]
	bo := normDematID(primaryDemat(c).ID)
	if bo == "" {
		t.Fatal("the sample client has no demat account")
	}
	// Roles: an ops maker does not load alerts.
	if _, err := o.ImportSurvAlerts(ctx, T, "priya.nair", "cdsl-surv", "a.csv", []byte("Alert No,BO ID\n1,"+bo+"\n")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker loads: %v", err)
	}
	// A file without headings Ledgerline knows is refused with the names.
	if _, err := o.ImportSurvAlerts(ctx, T, "vikram.shah", "cdsl-surv", "a.csv", []byte("x,y\n1,2\n")); err == nil || !strings.Contains(err.Error(), "Alert ID") {
		t.Fatalf("headless file: %v", err)
	}
	// CDSL's surveillance list, tilde-separated, a title line first.
	file := "CDAS Surveillance alerts\n" +
		"Alert Ref No~Alert Generation Date~Alert Type~BO ID~ISIN~Quantity~Value (Rs)~From Date~To Date~PAN\n" +
		"CDSLA0001~" + D + "~Off-market transfers to unrelated accounts~" + bo + "~INE000A01011~500~1,25,000.50~2026-10-01~2026-10-20~ABCDE1234F\n" +
		"CDSLA0002~" + D + "~Multiple accounts with same mobile (half yearly)~" + bo + "~~~~~~\n" +
		"CDSLA0003~" + D + "~Pledge out of line with income~1234567890123456~~~~~~\n"
	r, err := o.ImportSurvAlerts(ctx, T, "farhan.khan", "cdsl-surv", "alerts.txt", []byte(file))
	if err != nil || r.Download.Added != 3 || r.Download.Rows != 3 || r.Download.Unmatched != 1 || len(r.Unmatched) != 1 {
		t.Fatalf("import %+v %v", r, err)
	}
	if r2, err := o.ImportSurvAlerts(ctx, T, "farhan.khan", "cdsl-surv", "alerts.txt", []byte(file)); err != nil || r2.Download.Duplicates != 3 || r2.Download.Added != 0 {
		t.Fatalf("again %+v %v", r2, err)
	}
	a1, _ := o.caseAlert(ctx, T, "A-cdsl-surv-CDSLA0001")
	if a1.UCC != c.UCC || a1.Area != AreaDP || a1.Source != "CDSL" || a1.Due != addDays(D, 45) || len(a1.Checklist) != 9 || a1.ISIN != "INE000A01011" || a1.Value != "125000.50" || a1.TxnFrom != "2026-10-01" || a1.TxnTo != "2026-10-20" {
		t.Fatalf("alert %+v", a1)
	}
	if strings.Contains(a1.Detail, "ABCDE1234F") || strings.Contains(a1.Detail, bo) {
		t.Fatalf("PAN or BO in full: %s", a1.Detail)
	}
	if a2, _ := o.caseAlert(ctx, T, "A-cdsl-surv-CDSLA0002"); !a2.HalfYearly || a2.Due != addDays(D, 60) {
		t.Fatalf("half-yearly %+v", a2)
	}
	// MCX MRS: the client code is the key; 45 days, MCX's checklist.
	mcx := "Alert ID,Alert Date,Alert Name,Client ID,Commodity\nMRS9,2026-10-30,Client activity surge," + c.UCC + ",GOLD\n"
	if r, err := o.ImportSurvAlerts(ctx, T, "vikram.shah", "mcx-mrs", "mrs.csv", []byte(mcx)); err != nil || r.Download.Added != 1 {
		t.Fatalf("mcx %+v %v", r, err)
	}
	if m, _ := o.caseAlert(ctx, T, "A-mcx-mrs-MRS9"); m.UCC != c.UCC || m.Area != "" || m.Due != "2026-12-14" || len(m.Checklist) != 6 || !strings.HasPrefix(m.Type, "MCX alert") {
		t.Fatalf("mcx alert %+v", m)
	}
	// The log, and a nil download.
	if _, err := o.RecordNilDownload(ctx, T, "vikram.shah", "cdsl-fiu", ""); err != nil {
		t.Fatal(err)
	}
	log, _ := o.SurvDownloads(ctx, T, "vikram.shah")
	if len(log) != 4 || log[0].Kind != "cdsl-fiu" || !log[0].Nil {
		t.Fatalf("log %+v", log)
	}

	// The checklist before closing.
	id := a1.ID
	if _, err := o.ActOnAlert(ctx, T, "vikram.shah", id, "close", "nothing wrong"); err == nil || !strings.Contains(err.Error(), "checklist") {
		t.Fatalf("closed without the checklist: %v", err)
	}
	if _, err := o.SaveAlertChecklist(ctx, T, "vikram.shah", id, []AlertCheck{{Key: "attached", State: "Done"}}); err == nil {
		t.Fatal("documents attached with none")
	}
	if _, err := o.SaveAlertChecklist(ctx, T, "vikram.shah", id, []AlertCheck{{Key: "relationship", State: "Not applicable"}}); err == nil {
		t.Fatal("not applicable without a reason")
	}
	if _, err := o.AddAlertDoc(ctx, T, "vikram.shah", id, "x.exe", []byte("MZ\x90\x00")); err == nil {
		t.Fatal("an executable kept")
	}
	a, err := o.AddAlertDoc(ctx, T, "farhan.khan", id, "gift-deed.pdf", dsPDF)
	if err != nil || len(a.Attachments) != 1 {
		t.Fatalf("doc %+v %v", a.Attachments, err)
	}
	if d, b, err := o.AlertDocFile(ctx, T, "vikram.shah", id, a.Attachments[0].ID); err != nil || string(b) != string(dsPDF) || d.ContentType != "application/pdf" {
		t.Fatalf("doc back %v", err)
	}
	var items []AlertCheck
	for _, x := range a.Checklist {
		items = append(items, AlertCheck{Key: x.Key, State: "Done", Note: "made-up note for " + x.Key})
	}
	if a, err = o.SaveAlertChecklist(ctx, T, "farhan.khan", id, items); err != nil {
		t.Fatal(err)
	}
	if a, err = o.ActOnAlert(ctx, T, "vikram.shah", id, "close", "gift to the client's mother; deed seen"); err != nil || a.ClosedBy != "vikram.shah" || a.ClosedOn != D {
		t.Fatalf("close %+v %v", a, err)
	}
	for _, x := range a.Checklist {
		if x.Key == checkInTime && (x.State != "Done" || !strings.Contains(x.Note, "Answered on "+D)) {
			t.Fatalf("in time %+v", x)
		}
	}
	siWant(t, siOne(t, o, ctx, "alert_checklist", D), SIPass, "closed with the checklist")
	// An alert closed some other way (as before the checklist) is found.
	b := a
	b.ID, b.Checklist = "A-OLD-1", checklistFor(b)
	siPut(t, o, ctx, cAlert, b.ID, b)
	siWant(t, siOne(t, o, ctx, "alert_checklist", D), SIFail, "closed without it")
}

func TestSurvAdverseAndClocks(t *testing.T) {
	o, ctx := setup(t)
	set := siClock(o, siAt(D, "11:00"))
	cs, _ := o.Clients(ctx, T)
	c := cs[1]
	// The DP's own observation on transactions of 1-5 Aug.
	if !o.raiseAlert(ctx, T, SystemActor, Alert{Date: "2026-08-10", Source: "Internal", Area: AreaDP, Type: "Made-up own rule", UCC: c.UCC, Severity: "High", TxnFrom: "2026-08-01", TxnTo: "2026-08-05"}, "TST-OWN") {
		t.Fatal("not raised")
	}
	id := "A-TST-OWN"
	a, _ := o.caseAlert(ctx, T, id)
	if a.Due != "2026-09-24" || len(a.Checklist) != 9 {
		t.Fatalf("own alert %+v", a)
	}
	// Undecided after the two months: found.
	siWant(t, siOne(t, o, ctx, "dp_adverse_7", D), SIFail, "own observation past two months")
	if _, err := o.MarkAdverse(ctx, T, "vikram.shah", id, "Transfers to five unrelated accounts after a mobile change; client could not explain.", ""); err == nil {
		t.Fatal("adverse without the checklist")
	}
	var items []AlertCheck
	for _, x := range a.Checklist {
		if x.Key == checkAttached {
			items = append(items, AlertCheck{Key: x.Key, State: "Not applicable", Note: "client gave nothing"})
			continue
		}
		items = append(items, AlertCheck{Key: x.Key, State: "Done", Note: "made-up"})
	}
	if _, err := o.SaveAlertChecklist(ctx, T, "farhan.khan", id, items); err != nil {
		t.Fatal(err)
	}
	if _, err := o.MarkAdverse(ctx, T, "farhan.khan", id, "Transfers to five unrelated accounts after a mobile change.", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("checker decides adverse: %v", err)
	}
	if _, err := o.MarkAdverse(ctx, T, "vikram.shah", id, "Transfers to five unrelated accounts after a mobile change.", ""); err == nil || !strings.Contains(err.Error(), "two months") {
		t.Fatalf("late without a reason: %v", err)
	}
	a, err := o.MarkAdverse(ctx, T, "vikram.shah", id, "Transfers to five unrelated accounts after a mobile change.", "CDSL's file for August was loaded late")
	if err != nil || a.Adverse.Due != addDays(D, 7) || a.Adverse.To != "CDSL" {
		t.Fatalf("adverse %+v %v", a.Adverse, err)
	}
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-survadverse-"+id); err != nil || w.Status != "Open" {
		t.Fatalf("work %+v %v", w, err)
	}
	siWant(t, siOne(t, o, ctx, "dp_adverse_7", D), SIPass, "adverse recorded, not yet due")
	set(siAt(addDays(D, 8), "11:00"))
	siWant(t, siOne(t, o, ctx, "dp_adverse_7", addDays(D, 8)), SIFail, "not reported in 7 days")
	if _, err := o.RecordAdverseReported(ctx, T, "vikram.shah", id, addDays(D, 6), "CDSL-SURV-77"); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "dp_adverse_7", addDays(D, 8)), SIPass, "reported on day 6")
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-survadverse-"+id); w.Status == "Open" {
		t.Fatal("work item left open")
	}
	// Reported counts as disposed of: alerts_45 and the CDSL quarterly status.
	siWant(t, siOne(t, o, ctx, "alerts_45", addDays(D, 8)), SIPass, "reported alert is decided")
	r, err := o.SurvReturnTo(ctx, T, "vikram.shah", "2026-Q3", "CDSL")
	if err != nil || len(r.I) != 1 || r.I[0].New != 1 || r.I[0].End != 1 || r.Due != "2026-10-15" {
		t.Fatalf("CDSL Q3 %+v %v", r, err)
	}
	r, _ = o.SurvReturnTo(ctx, T, "vikram.shah", "2026-Q4", "CDSL")
	if len(r.I) != 1 || r.I[0].Start != 1 || r.I[0].Referred != 1 || len(r.II) != 1 {
		t.Fatalf("CDSL Q4 %+v", r)
	}
	if ex, _ := o.SurvReturnTo(ctx, T, "vikram.shah", "2026-Q4", ""); true {
		for _, f := range ex.I {
			if f.Name == "Made-up own rule" {
				t.Fatalf("a DP alert in the exchanges' report: %+v", ex.I)
			}
		}
	}
	rows, err := o.ReportRows(ctx, T, "2027-01-05", "alerts_cdsl")
	if err != nil || !strings.Contains(string(CSV(rows)), "Reported to CDSL as adverse") {
		t.Fatalf("report %v", err)
	}

	// Downloads: a CDSL participant must keep downloading both lists.
	set(siAt(D, "11:00"))
	siWant(t, siOne(t, o, ctx, "cdsl_alert_downloads", D), SIFail, "never downloaded")
	o.survClocks(ctx, T, D)
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-survdownload-cdsl-fiu-never"); err != nil || w.Status != "Open" {
		t.Fatalf("download work %+v %v", w, err)
	}
	for _, k := range []string{"cdsl-surv", "cdsl-fiu"} {
		if _, err := o.RecordNilDownload(ctx, T, "vikram.shah", k, ""); err != nil {
			t.Fatal(err)
		}
	}
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-survdownload-cdsl-fiu-never"); w.Status == "Open" {
		t.Fatal("download work left open")
	}
	siWant(t, siOne(t, o, ctx, "cdsl_alert_downloads", D), SIPass, "downloaded today")
	set(siAt(addDays(D, 9), "11:00"))
	siWant(t, siOne(t, o, ctx, "cdsl_alert_downloads", addDays(D, 9)), SIFail, "9 days old")
}

func TestSurvSample(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt("2026-10-20", "11:00"))
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("A-S%02d", i)
		siPut(t, o, ctx, cAlert, id, Alert{ID: id, Date: "2026-07-01", Source: "NSE", Type: "Made-up", Status: "Closed", ClosedOn: "2026-08-01", ClosedBy: "vikram.shah", Notes: []string{}})
	}
	v, err := o.AlertSample(ctx, T, "vikram.shah", "2026-Q3")
	if err != nil || v.Closed != 12 || v.Size != 5 || len(v.Sample) != 5 {
		t.Fatalf("sample %+v %v", v, err)
	}
	if again, _ := o.AlertSample(ctx, T, "vikram.shah", "2026-Q3"); again.Sample[0].ID != v.Sample[0].ID {
		t.Fatal("the sample changes between views")
	}
	siWant(t, siOne(t, o, ctx, "alert_sample", "2026-10-20"), SIFail, "unchecked after the 15th")
	out := ""
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("A-S%02d", i)
		in := false
		for _, a := range v.Sample {
			in = in || a.ID == id
		}
		if !in {
			out = id
		}
	}
	if _, err := o.RecordAlertSample(ctx, T, "vikram.shah", out, "2026-Q3", "Agree", ""); err == nil {
		t.Fatal("an alert outside the sample recorded")
	}
	if _, err := o.RecordAlertSample(ctx, T, "vikram.shah", v.Sample[0].ID, "2026-Q3", "Disagree", ""); err == nil {
		t.Fatal("disagree without a reason")
	}
	for i, a := range v.Sample {
		res, note := "Agree", ""
		if i == 0 {
			res, note = "Disagree", "client's explanation not on file"
		}
		if _, err := o.RecordAlertSample(ctx, T, "vikram.shah", a.ID, "2026-Q3", res, note); err != nil {
			t.Fatal(err)
		}
	}
	siWant(t, siOne(t, o, ctx, "alert_sample", "2026-10-20"), SIPass, "all checked")
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-survsample-"+v.Sample[0].ID); err != nil || w.Status != "Open" {
		t.Fatalf("disagree work %+v %v", w, err)
	}
}

func TestSTROnCDSLAlert(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	c := cs[0]
	if !o.raiseAlert(ctx, T, SystemActor, Alert{Date: D, Source: "CDSL", Ref: "CDSLF55", FIU: true, Type: "CDSL FIU alert: made-up", UCC: c.UCC, Severity: "High", ISIN: "INE000A01011", Qty: "900", Value: "450000.00", TxnFrom: "2026-10-02", TxnTo: "2026-10-09"}, "TST-FIU") {
		t.Fatal("not raised")
	}
	id := "A-TST-FIU"
	if _, err := o.DecideSTR(ctx, T, "vikram.shah", id, STRInput{Grounds: "Securities received from many unrelated accounts and sold."}); err == nil || !strings.Contains(err.Error(), "analysis") {
		t.Fatalf("STR without the analysis: %v", err)
	}
	if _, err := o.SaveAlertChecklist(ctx, T, "vikram.shah", id, []AlertCheck{
		{Key: "analysis", State: "Done", Note: "nine credits from unrelated accounts in a week, sold the next day"},
		{Key: "bo_explanation", State: "Not applicable", Note: "not asked, to avoid tipping off"},
	}); err != nil {
		t.Fatal(err)
	}
	a, err := o.DecideSTR(ctx, T, "vikram.shah", id, STRInput{Grounds: "Securities received from many unrelated accounts and sold.", Attempted: true})
	if err != nil || a.STR.From != "2026-10-02" || a.STR.To != "2026-10-09" || !a.STR.Attempted {
		t.Fatalf("STR %+v %v", a.STR, err)
	}
	ex, err := o.FINGateUpload(ctx, T, "vikram.shah", []string{id})
	if err != nil {
		t.Fatal(err)
	}
	gs := fgRead(t, ex.Zip)["GS1"]
	if len(gs) != 1 || gs[0]["Attempted Transaction"] != "Yes" {
		t.Fatalf("GS1 %+v", gs)
	}
	for _, want := range []string{"CDSL FIU alert CDSLF55 of " + D, "ISIN INE000A01011", "quantity 900", "value Rs 450000.00", "period 2026-10-02 to 2026-10-09", "KYC: client since", "Analysis: nine credits", "Explanation: not asked"} {
		if !strings.Contains(gs[0]["Narration"], want) {
			t.Fatalf("narration lacks %q: %s", want, gs[0]["Narration"])
		}
	}
	if strCitation(Alert{Source: "NSE", Type: "x"}, c, AlertSTR{}) != "" {
		t.Fatal("an exchange alert without a reference cited")
	}
}

func TestDPRules(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "11:00"))
	cs, _ := o.Clients(ctx, T)
	var ind []Client
	for _, c := range cs {
		if c.Status != "Closed" && c.PAN != "" {
			ind = append(ind, c)
		}
	}
	if len(ind) < 4 {
		t.Fatal("too few clients")
	}
	a, b := ind[0], ind[1]
	// b gets a's mobile and bank account (made-up).
	if _, err := docs.Update(ctx, o.Docs, T, cClient, b.UCC, "test", func(x *Client) error {
		x.Mobile, x.Bank, x.Banks = a.Mobile, a.Bank, nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RunDPRules(ctx, T, "priya.nair", D); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker runs: %v", err)
	}
	boA := normDematID(primaryDemat(a).ID)
	// Off-market debits from a's account to five accounts, one after a
	// mobile change approved 3 days before, one after 200 quiet days.
	put := func(tx DPTxn) {
		if err := o.dpTxn(ctx, T, "test", tx); err != nil {
			t.Fatal(err)
		}
	}
	put(DPTxn{Date: addDays(D, -230), UCC: a.UCC, BOID: boA, ISIN: "INE000A01011", Qty: money.DecInt(10), Kind: "Demat credit", Ref: "T0"})
	for i := 0; i < 5; i++ {
		put(DPTxn{Date: addDays(D, -5+i), UCC: a.UCC, BOID: boA, ISIN: "INE000A01011", Qty: money.DecInt(-100), Kind: "Off-market transfer", Ref: fmt.Sprint("T", i+1),
			Value: money.DecInt(4_000_000), Counterparty: []string{fmt.Sprintf("12081600000099%02d", i)}})
	}
	siPut(t, o, ctx, cChange, "CR-TST-1", ChangeRequest{ID: "CR-TST-1", UCC: a.UCC, Kind: "Mobile", Status: "Approved", Decided: addDays(D, -8) + "T10:00:00+05:30", New: map[string]string{}, Old: map[string]string{}})
	for i := 2; i <= 3; i++ {
		siPut(t, o, ctx, cChange, fmt.Sprint("CR-TST-", i), ChangeRequest{ID: fmt.Sprint("CR-TST-", i), UCC: a.UCC, Kind: "Email", Status: "Approved", Decided: D[:8] + "01T10:00:00+05:30", New: map[string]string{}, Old: map[string]string{}})
	}
	// A gift instruction with consideration.
	siPut(t, o, ctx, cDIS, "DIS-TST-1", DIS{ID: "DIS-TST-1", UCC: a.UCC, BOID: boA, Kind: "Off-market", ToBO: "1208160000009900", Reason: "Gift", Consideration: money.Amount(5_000_000), ExecDate: D, Status: "Executed", Value: money.DecInt(90000)})
	n, err := o.RunDPRules(ctx, T, "vikram.shah", D)
	if err != nil || n == 0 {
		t.Fatalf("raised %d %v", n, err)
	}
	as, _ := o.Alerts(ctx, T)
	got := map[string]Alert{}
	for _, x := range as {
		got[x.Type] = x
	}
	for _, want := range []string{"Several accounts with the same mobile", "One bank account registered for several PANs", "Off-market transfers from one account to many",
		"Transfer soon after a change of the client's details", "Debit from a dormant demat account", "Frequent changes of the client's details", "Reason code does not fit the off-market instruction"} {
		x, ok := got[want]
		if !ok {
			t.Fatalf("no %q alert in %v", want, sortedKeys(got))
		}
		if x.Due == "" {
			t.Fatalf("%s: no due date", want)
		}
	}
	if m := got["Several accounts with the same mobile"]; !m.HalfYearly || m.Due != addDays(D, 60) || !strings.Contains(m.Detail, b.UCC) || strings.Contains(m.Detail, a.Mobile) {
		t.Fatalf("shared mobile %+v", m)
	}
	if x := got["Transfer soon after a change of the client's details"]; x.ISIN != "INE000A01011" || x.TxnFrom == "" {
		t.Fatalf("transfer after change %+v", x)
	}
	if _, ok := got["Off-market transfers or pledges out of line with income"]; !ok && incomeMid[a.Income] > 0 && incomeMid[a.Income]*1 < 20_000_000 {
		t.Fatalf("₹2 crore off-market against income %s not raised", a.Income)
	}
	// Run again: nothing new.
	if n2, _ := o.RunDPRules(ctx, T, "vikram.shah", D); n2 != 0 {
		t.Fatalf("raised again %d", n2)
	}
	// Connected groups: a and b share mobile and bank.
	gs, err := o.ConnectedGroups(ctx, T, "vikram.shah")
	found := false
	for _, g := range gs {
		found = found || (contains(g.Members, a.UCC) && contains(g.Members, b.UCC))
	}
	if err != nil || !found {
		t.Fatalf("groups %+v %v", gs, err)
	}

	// Thresholds by risk: a high-risk client's limits are tighter.
	s := o.DPSurvConfig(ctx, T)
	if s.scaleCount(10, "High") != 5 || s.scaleCount(10, "low") != 10 || s.scaleCount(2, "High") != 2 {
		t.Fatalf("scale %+v", s.RiskPct)
	}
	if _, err := o.SaveDPSurvSettings(ctx, T, "vikram.shah", DPSurvSettings{RiskPct: map[string]int{"Extreme": 10}}); err == nil {
		t.Fatal("an unknown risk category")
	}
	if v, err := o.SaveDPSurvSettings(ctx, T, "vikram.shah", DPSurvSettings{FanCount: 9}); err != nil || v.FanCount != 9 || v.ClusterSize != 20 {
		t.Fatalf("save %+v %v", v, err)
	}
}

func TestStaffAndConnectedTrades(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	staff, cl, x := cs[0], cs[1], cs[2]
	siPut(t, o, ctx, cTradeAcct, "TA-1", StaffAccount{ID: "TA-1", Employee: "E001", Holder: "made-up", Relation: "Self", Broker: "this firm", Account: staff.UCC})
	tr := func(u, side, at string, qty int64, px int64) ingest.TradeEvent {
		return ingest.TradeEvent{Exchange: "NSE", Segment: "CM", UCC: u, Symbol: "MADEUP", InstrumentType: "EQ", Side: side, Qty: qty, Price: money.DecInt(px), TradeDate: D, TradeTime: at, TradeNo: u + at}
	}
	e := &dpRuleEnv{o: o, ctx: ctx, tenant: T, actor: SystemActor, date: D, set: defaultDPSurv(), cs: map[string]Client{}}
	for _, c := range cs {
		e.cs[c.UCC] = c
	}
	e.frontRunning([]ingest.TradeEvent{tr(staff.UCC, "B", "10:00:05", 100, 500), tr(cl.UCC, "B", "10:09:00", 3000, 500), tr(x.UCC, "S", "10:01:00", 3000, 500)})
	if e.n != 1 {
		t.Fatalf("front running raised %d", e.n)
	}
	e.n = 0
	e.frontRunning([]ingest.TradeEvent{tr(staff.UCC, "B", "10:00:05", 100, 500), tr(cl.UCC, "B", "10:30:00", 3000, 500)})
	if e.n != 0 {
		t.Fatal("a client order 30 minutes later flagged")
	}
	if tradeSecond("2026-10-30 09:15:07") != 9*3600+15*60+7 || tradeSecond("x") != -1 {
		t.Fatal("trade time")
	}
	g := []sharedGroup{{Kind: "mobile", Members: []string{cl.UCC, x.UCC}}}
	e.circular([]ingest.TradeEvent{tr(cl.UCC, "B", "11:00:00", 1000, 50), tr(x.UCC, "S", "11:00:01", 900, 50)}, g)
	if e.n != 1 {
		t.Fatalf("circular raised %d", e.n)
	}
}
