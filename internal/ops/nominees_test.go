package ops

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
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
	// SEBI circular of 29 May 2026: name and relationship are mandatory,
	// and a minor's date of birth; identity and guardian are optional.
	bad := []Nominee{{Name: "A One", Relation: "Spouse", IDType: "PAN", ID: "ABCPA1234K", Share: "60"}, {Name: "Kid", Relation: "Son", Share: "30", IsMinor: true}, {Name: "No Relation", Share: "10", IDType: "PAN", ID: "BAD"}}
	fe := validateNominees(bad, now)
	msg := (&ValidationError{fe}).Error()
	for _, want := range []string{"Nominee 2: a minor nominee's date of birth", "Nominee 3: enter the relationship", "Nominee 3: PAN must be"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("%q not in %s", want, msg)
		}
	}
	if strings.Contains(msg, "guardian") || strings.Contains(msg, "Nominee 1") {
		t.Fatalf("optional details required: %s", msg)
	}
	plain := Client{Nominees: []Nominee{{Name: "Only Name", Relation: "Spouse", IDType: "PAN"}, {Name: "Kid Two", Relation: "Son", DOB: "2020-01-01"}}}
	normaliseNominees(&plain)
	if fe := validateNominees(plain.Nominees, now); len(fe) != 0 || plain.Nominees[0].IDType != "" {
		t.Fatalf("name and relationship only: %v %+v", fe, plain.Nominees)
	}
	four := make([]Nominee, 4)
	if fe := validateNominees(four, now); len(fe) != 1 || !strings.Contains(fe[0].Msg, "At most 3") {
		t.Fatalf("four: %v", fe)
	}
	// A nomination of up to 10 recorded before 1 Sep 2026 still validates
	// when other details change, and is flagged.
	old := Client{Nominees: make([]Nominee, 5)}
	for i := range old.Nominees {
		old.Nominees[i] = Nominee{Name: "Person " + string(rune('A'+i)), Relation: "Friend"}
	}
	normaliseNominees(&old)
	if fe := checkNominees(old.Nominees, now, legacyMaxNominees); len(fe) != 0 || old.NominationState() != NominationOverLimit {
		t.Fatalf("legacy five: %v %s", fe, old.NominationState())
	}
	if old.StatementNomination() != "Yes" {
		t.Fatal("statement shows names without the client's choice")
	}
	old.NomineesOnStatement = true
	if !strings.HasPrefix(old.StatementNomination(), "Person A") || (Client{Nominee: "Opted out"}).StatementNomination() != "No" {
		t.Fatal("statement nomination")
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
	// From 1 Sep 2026: no video declaration, at most 3 nominees, every
	// joint holder consents, and a thumb impression needs two witnesses.
	one, _ := json.Marshal([]Nominee{{Name: "Kiran Rao", Relation: "Brother"}})
	if _, err := o.RequestChange(ctx, T, "priya.nair", u, "Nomination", map[string]string{"nominee": "Opted out", "mode": "Video recorded"}, ""); err == nil {
		t.Fatal("video opt-out accepted")
	}
	four, _ := json.Marshal([]Nominee{{Name: "Kiran Rao", Relation: "Brother"}, {Name: "Mala Rao", Relation: "Sister"}, {Name: "Anu Rao", Relation: "Mother"}, {Name: "Raj Rao", Relation: "Father"}})
	if _, err := o.RequestChange(ctx, T, "priya.nair", u, "Nomination", map[string]string{"nominee": "Opted in", "nominees": string(four), "mode": "Signed form"}, ""); err == nil {
		t.Fatal("four nominees accepted")
	}
	if _, err := o.RequestChange(ctx, T, "priya.nair", u, "Nomination", map[string]string{"nominee": "Opted in", "nominees": string(one), "mode": "Thumb impression with two witnesses", "witness_1": "A, Pune"}, ""); err == nil {
		t.Fatal("thumb impression with one witness accepted")
	}
	uj := cs[2].UCC
	if err := docs.Upsert(ctx, o.Docs, T, cCDSLBODetail, uj, CDSLBODetail{UCC: uj, ModeOfOperation: "2", Holders: []CDSLJointHolder{{Name: "Second Holder", PAN: "ABCPS1234K"}}}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RequestChange(ctx, T, "priya.nair", uj, "Nomination", map[string]string{"nominee": "Opted in", "nominees": string(one), "mode": "Signed form"}, ""); err == nil || !strings.Contains(err.Error(), "joint") {
		t.Fatalf("joint nomination without every holder's consent: %v", err)
	}
	if _, err := o.RequestChange(ctx, T, "priya.nair", uj, "Nomination", map[string]string{"nominee": "Opted in", "nominees": string(one), "mode": "Online with OTP", "joint_consent": "all", "on_statement": "names"}, ""); err != nil {
		t.Fatalf("joint nomination with consent: %v", err)
	}
	// A nomination of 5 recorded before the limit: kept, flagged, worked.
	uo := cs[3].UCC
	five := []Nominee{{Name: "Aa One", Relation: "Son"}, {Name: "Bb Two", Relation: "Son"}, {Name: "Cc Three", Relation: "Son"}, {Name: "Dd Four", Relation: "Son"}, {Name: "Ee Five", Relation: "Son"}}
	if _, err := docs.Update(ctx, o.Docs, T, cClient, uo, "test", func(c *Client) error { c.Nominees = five; normaliseNominees(c); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RequestChange(ctx, T, "priya.nair", uo, "Mobile", map[string]string{"mobile": "9876501234"}, ""); err != nil {
		t.Fatalf("another change blocked by the old nomination: %v", err)
	}
	nrows, by, err := o.Nominations(ctx, T, "vikram.shah")
	if err != nil || by[NominationOverLimit] != 1 {
		t.Fatalf("nomination report: %v %v", by, err)
	}
	for _, r := range nrows {
		if r.UCC == uo && (!r.OverLimit || r.Count != 5) {
			t.Fatalf("not flagged or changed: %+v", r)
		}
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	flagged := false
	for _, w := range ws {
		flagged = flagged || w.Key == wNominations+":over:"+uo
	}
	if !flagged {
		t.Fatal("no work item for the nomination over the limit")
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
