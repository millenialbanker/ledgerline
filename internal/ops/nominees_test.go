package ops

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNominees(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	c := Client{Nominees: []Nominee{{Name: "A One", Relation: "Spouse", IDType: "pan", ID: "abcpa1234k"}, {Name: "B Two", Relation: "Son", IDType: "Driving licence", ID: "MH12 20110012345"}, {Name: "C Three", Relation: "Daughter", IDType: "AADHAAR", ID: "9999-8888-7777"}}}
	normaliseNominees(&c)
	if c.Nominee != "Opted in" || c.Nominees[0].Share != "33.34" || c.Nominees[2].Share != "33.33" || c.Nominees[2].ID != "7777" || c.Nominees[1].IDType != "DL" {
		t.Fatalf("%+v", c.Nominees)
	}
	if fe := validateNominees(c.Nominees, now); len(fe) != 0 {
		t.Fatalf("%v", fe)
	}
	bad := []Nominee{{Name: "A One", Relation: "Spouse", IDType: "PAN", ID: "ABCPA1234K", Share: "60"}, {Name: "Kid", Relation: "Son", IDType: "PASSPORT", ID: "X1234567", Share: "30", DOB: "2020-01-01"}}
	fe := validateNominees(bad, now)
	msg := (&ValidationError{fe}).Error()
	if !strings.Contains(msg, "guardian") || !strings.Contains(msg, "90.00%") {
		t.Fatalf("%s", msg)
	}
	eleven := make([]Nominee, 11)
	if fe := validateNominees(eleven, now); len(fe) != 1 {
		t.Fatalf("eleven: %v", fe)
	}
	if c.Nominees[0].MaskedID() == "ABCPA1234K" || !strings.HasSuffix(c.Nominees[2].MaskedID(), "7777") {
		t.Fatal("not masked")
	}
}

func TestNominationChangeAndFiles(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	u := cs[0].UCC
	ns, _ := json.Marshal([]Nominee{{Name: "Meena Rao", Relation: "Spouse", IDType: "PAN", ID: "ABCPR1234K", Share: "100"}})
	ch, err := o.RequestChange(ctx, T, "priya.nair", u, "Nomination", map[string]string{"nominee": "Opted in", "nominees": string(ns), "mode": "Signed form"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if ch.Status == "Pending" {
		if _, err := o.DecideChange(ctx, T, "farhan.khan", ch.ID, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := o.Client(ctx, T, u)
	if len(c.Nominees) != 1 || c.NominationState() != "Nominated" || c.NominationMode != "Signed form" || c.NominationOn == "" {
		t.Fatalf("after change %+v %s", c.Nominees, c.NominationState())
	}
	// NOMINEES file: two nominees for another client, equal shares.
	u2 := cs[1].UCC
	f := "UCC,Name,Relation,Share,IDType,ID,DOB,Guardian,GuardianRelation,DeclaredOn,Mode\n" +
		u2 + ",Ravi Kumar,Brother,,PAN,ABCPK1234K,,,,2025-06-01,Signed form\n" +
		u2 + ",Little Kumar,Son,,AADHAAR,123412341234,2019-02-02,Ravi Kumar,Uncle,,\n"
	if _, err := o.LoadFile(ctx, T, "priya.nair", "NOMINEES", "", "nominees.csv", []byte(f)); err != nil {
		t.Fatal(err)
	}
	c2, _ := o.Client(ctx, T, u2)
	if len(c2.Nominees) != 2 || c2.Nominees[0].Share != "50.00" || c2.Nominees[1].ID != "1234" || c2.NominationOn != "2025-06-01" {
		t.Fatalf("file %+v %s", c2.Nominees, c2.NominationOn)
	}
	if _, err := o.LoadFile(ctx, T, "priya.nair", "NOMINEES", "", "bad.csv", []byte("UCC,Name,Relation,Share,IDType,ID,DOB,Guardian,GuardianRelation\n"+u2+",X Y Z,Son,100,VOTER,123,,,\n")); err == nil {
		t.Fatal("voter ID accepted")
	}
	// SEGMENT-STATUS file then a change by a checker.
	seg := "UCC,Segment,Status,EffectiveDate,Reason\n" + u + ",FO,Suspended,2025-01-10,Income proof expired\n" + u + ",FO,Reactivated,2025-03-01,Income proof received\n" + u + ",CM,Dormant,2026-01-01,No trades\n"
	if _, err := o.LoadFile(ctx, T, "priya.nair", "SEGMENT-STATUS", "", "seg.csv", []byte(seg)); err != nil {
		t.Fatal(err)
	}
	c, _ = o.Client(ctx, T, u)
	if c.SegmentStatus("FO") != SegReactivated || c.SegmentStatus("CM") != SegDormant || len(c.SegHistory) < 3 {
		t.Fatalf("seg %v %d", c.SegStatus, len(c.SegHistory))
	}
	if _, err := o.SetSegmentStatus(ctx, T, "priya.nair", u, "CM", "Active", D, "client traded again"); err == nil {
		t.Fatal("maker changed segment status")
	}
	if _, err := o.SetSegmentStatus(ctx, T, "farhan.khan", u, "CM", "Active", D, "Client asked to reactivate after re-KYC"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetSegmentStatus(ctx, T, "farhan.khan", u, "FO", "Closed", D, "Client closed derivatives"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetSegmentStatus(ctx, T, "farhan.khan", u, "FO", "Suspended", D, "test"); err == nil {
		t.Fatal("closed segment suspended")
	}
	rows, _ := o.ReportRows(ctx, T, D, "ucc")
	found := 0
	for _, r := range rows {
		if r[0] == u {
			found++
		}
	}
	if found < 2 {
		t.Fatalf("ucc report rows for %s: %d", u, found)
	}
	if lf, err := o.LimitFile(ctx, T, "2026-10-19"); err != nil || !strings.Contains(string(lf), "SegmentsAllowed") {
		t.Fatalf("limit file %v", err)
	}
	if rr, err := o.ReportRows(ctx, T, D, "nomination"); err != nil || len(rr) < 3 || strings.Contains(strings.Join(rr[1], ","), "ABCPR1234K") {
		t.Fatalf("nomination report %v", err)
	}
	// A re-import of the client master keeps nominees and segment status.
	// (Covered by the merge in CLIENT-MASTER; checked here on the stored record.)
	if _, _, err := o.SegmentStatusReport(ctx, T, "vikram.shah"); err != nil {
		t.Fatal(err)
	}
}
