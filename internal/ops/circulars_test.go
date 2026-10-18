package ops

import (
	"testing"
)

func TestCircularsAndCalendar(t *testing.T) {
	o, ctx := setup(t)
	csv := "Source,Ref,Date,Subject,Department,URL,Text\n" +
		"NSE,NSE/INSP/70001,2026-10-15,Revised framework for peak margin collection,Inspection,https://www.nseindia.com/x,\"Members are required to collect peak margin. This is applicable with effect from 01-Dec-2026. Members shall ensure compliance.\"\n" +
		"BSE,20261016-1,16-Oct-2026,Trading holiday on account of Diwali,Trading Operations,,\"The exchange will remain closed on 9 November 2026.\"\n" +
		"SEBI,SEBI/HO/MIRSD/2026/1,2026-10-14,Cyber Security and Cyber Resilience Framework: clarifications,MIRSD,,\"Regulated entities shall complete VAPT and submit reports.\"\n" +
		"NSE,,2026-10-14,Bad row,,,\n"
	r, err := o.LoadFile(ctx, T, "vikram.shah", "CIRCULARS", "", "circ.csv", []byte(csv))
	if err == nil || r.Added != 0 {
		t.Fatalf("bad row accepted: %+v %v", r, err)
	}
	csv = csv[:len(csv)-len("NSE,,2026-10-14,Bad row,,,\n")]
	if r, err = o.LoadFile(ctx, T, "vikram.shah", "CIRCULARS", "", "circ.csv", []byte(csv)); err != nil || r.Added != 3 {
		t.Fatalf("load: %+v %v", r, err)
	}
	cs, _ := o.Circulars(ctx, T)
	by := map[string]Circular{}
	for _, c := range cs {
		by[c.Ref] = c
	}
	pm := by["NSE/INSP/70001"]
	if pm.Priority != "High" || !contains(pm.Areas, "Risk and margins") || pm.Effective != "2026-12-01" || len(pm.Tasks) == 0 || pm.Tasks[0].Due != "2026-12-01" || pm.Status != "New" {
		t.Fatalf("peak margin triage: %+v", pm)
	}
	if h := by["20261016-1"]; h.Priority != "Information" || h.Status != "Done" || h.Date != "2026-10-16" {
		t.Fatalf("holiday triage: %+v", h)
	}
	if c := by["SEBI/HO/MIRSD/2026/1"]; c.Priority != "High" || !contains(c.Areas, "IT and cyber security") {
		t.Fatalf("cyber triage: %+v", c)
	}
	// Owners finish their actions with evidence; others cannot review.
	if _, err := o.ActOnCircular(ctx, T, "anita.rao", pm.ID, CircularAction{Kind: "review"}); err == nil {
		t.Fatal("risk manager reviewed")
	}
	if _, err := o.ActOnCircular(ctx, T, "vikram.shah", pm.ID, CircularAction{Kind: "review", Note: "Applies to us"}); err != nil {
		t.Fatal(err)
	}
	risk := ""
	for _, x := range pm.Tasks {
		if x.Role == RoleRisk {
			risk = x.ID
		}
	}
	if _, err := o.ActOnCircular(ctx, T, "anita.rao", pm.ID, CircularAction{Kind: "done_task", Task: risk}); err == nil {
		t.Fatal("done without evidence")
	}
	c, err := o.ActOnCircular(ctx, T, "anita.rao", pm.ID, CircularAction{Kind: "done_task", Task: risk, Evidence: "Risk policy v4 updated, board note 12"})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range c.Tasks {
		if x.Status == "Open" {
			if c, err = o.ActOnCircular(ctx, T, "vikram.shah", pm.ID, CircularAction{Kind: "drop_task", Task: x.ID, Note: "Covered by the risk action"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if c.Status != "Done" {
		t.Fatalf("circular not closed: %s", c.Status)
	}

	// Calendar: due dates, done with evidence, queued a week before.
	if d, p := nextDue("D07", "2026-10-16"); d != "2026-11-07" || p != "2026-10" {
		t.Fatalf("monthly: %s %s", d, p)
	}
	if d, _ := nextDue("FRI1Q", "2026-10-16"); d != "2027-01-01" {
		t.Fatalf("first Friday of quarter: %s", d)
	}
	if d, _ := nextDue("05-15,11-15", "2026-10-16"); d != "2026-11-15" {
		t.Fatalf("half-yearly: %s", d)
	}
	ds, _ := o.Duties(ctx, T, D)
	var cd Duty
	for _, x := range ds {
		if x.ID == "OB-COMPLAINTS" {
			cd = x
		}
	}
	if cd.NextDue != "2026-11-07" || cd.Overdue {
		t.Fatalf("complaints duty: %+v", cd)
	}
	if _, err := o.DoneDuty(ctx, T, "vikram.shah", D, "OB-COMPLAINTS", ""); err == nil {
		t.Fatal("done without evidence")
	}
	if _, err := o.DoneDuty(ctx, T, "vikram.shah", D, "OB-COMPLAINTS", "https://firm.example/complaints Sep-2026"); err != nil {
		t.Fatal(err)
	}
	ds, _ = o.Duties(ctx, T, D)
	for _, x := range ds {
		if x.ID == "OB-COMPLAINTS" && (x.NextDue != "2026-12-07" || x.Done[0].Late || x.Done[0].Period != "2026-10") {
			t.Fatalf("after done: %+v", x)
		}
	}
	runs, err := o.RunStage(ctx, T, SystemActor, "2026-10-28", "", "compliance")
	if err != nil || runs[0].Queued == 0 {
		t.Fatalf("queue: %+v %v", runs, err)
	}
}
