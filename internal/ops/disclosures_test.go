package ops

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func field(v DisclosureView, key string) DisclosureField {
	for _, f := range v.Fields {
		if f.Key == key {
			return f
		}
	}
	return DisclosureField{Key: "missing:" + key}
}

func TestDisclosurePeriods(t *testing.T) {
	p := discPeriodsFor(DiscAIML, "half-yearly", "2026-11-02", 3)
	if p[0].ID != "2026-04_2026-09" || p[0].Due != "2026-10-15" || p[0].FY != "2026-27" || p[0].Label != "Apr–Sep 2026" || p[1].ID != "2025-10_2026-03" || p[1].Due != "2026-04-15" || p[2].ID != "2025-04_2025-09" {
		t.Fatalf("half-yearly %+v", p)
	}
	q := discPeriodsFor(DiscAIML, "quarterly", "2026-11-02", 2)
	if q[0].ID != "2026-07_2026-09" || q[0].Due != "2026-10-15" || q[1].ID != "2026-04_2026-06" {
		t.Fatalf("quarterly %+v", q)
	}
	y := discPeriodsFor(DiscAIML, "yearly", "2026-11-02", 1)
	if y[0].ID != "2025-04_2026-03" || y[0].Due != "2026-04-15" {
		t.Fatalf("yearly %+v", y)
	}
	s := discPeriodsFor(DiscSaaS, "", "2026-11-02", 2)
	if s[0].ID != "2026-01_2026-06" || s[0].Due != "2026-07-31" || s[1].ID != "2025-07_2025-12" || s[1].Due != "2026-01-31" || s[1].FY != "2025-26" {
		t.Fatalf("saas %+v", s)
	}
	for _, c := range [][3]string{{DiscAIML, "half-yearly", "04-15,10-15"}, {DiscAIML, "quarterly", "01-15,04-15,07-15,10-15"}, {DiscSaaS, "", "01-31,07-31"}} {
		if r := discDutyRule(c[0], c[1]); r != c[2] {
			t.Errorf("%v: %s", c, r)
		}
	}
}

func TestAIMLDisclosure(t *testing.T) {
	o, ctx, _ := aiFirm(t) // Ledgy AI switched on 20 October 2026
	o.Now = func() time.Time { return time.Date(2026, 11, 2, 10, 0, 0, 0, ist) }
	if _, err := o.Disclosure(ctx, T, "priya.nair", DiscAIML, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker saw disclosures: %v", err)
	}
	// April–September 2026: AI was off the whole period, so the answer is No.
	v, err := o.Disclosure(ctx, T, "vikram.shah", DiscAIML, "")
	if err != nil || v.Period.ID != "2026-04_2026-09" || v.Answer != "No" || field(v, "any").Value != "N" || len(v.Systems) != 0 {
		t.Fatalf("off all period: %s %s %+v %v", v.Period.ID, v.Answer, v.Systems, err)
	}
	if f := field(v, "h.pan"); f.Value != "AAACB1234C" {
		t.Fatalf("pan %+v", f)
	}
	if f := field(v, "h.sebi_reg"); !strings.HasPrefix(f.Value, "INZ000") || field(v, "h.entity").Value == "" || field(v, "h.year").Value != "2026-27" {
		t.Fatalf("header %+v", v.Fields[:6])
	}
	// A member of NSE, BSE and MCX files once, with NSE, which shares it
	// (NSE/INSP/69184, Phase I item 40).
	if len(v.Exchanges) != 1 || v.Exchanges[0].Exchange != "NSE" || v.Status != "Overdue" || !strings.Contains(strings.Join(v.Notes, " "), discSharedNote) {
		t.Fatalf("exchanges %+v %s %v", v.Exchanges, v.Status, v.Notes)
	}

	// October 2026 – March 2027: Ledgy was on, so one entry, answered from the facts.
	o.Now = func() time.Time { return time.Date(2027, 4, 2, 10, 0, 0, 0, ist) }
	v, err = o.Disclosure(ctx, T, "vikram.shah", DiscAIML, "")
	if err != nil || v.Period.ID != "2026-10_2027-03" || v.Answer != "Yes" || len(v.Systems) != 1 || v.Status != "Due" {
		t.Fatalf("on: %s %s %+v %s %v", v.Period.ID, v.Answer, v.Systems, v.Status, err)
	}
	for k, want := range map[string]string{"order_execution": "N", "discretionary": "N", "advice": "N", "cyber": "N", "in_system_audit": "Y"} {
		if f := field(v, "s.ledgy."+k); f.Value != want {
			t.Errorf("%s = %q", k, f.Value)
		}
	}
	if f := field(v, "s.ledgy.implemented"); !strings.Contains(f.Value, "Vendor (Ledgerline)") || !strings.Contains(f.Value, "claude-haiku-5-5") {
		t.Errorf("implemented %q", f.Value)
	}
	if f := field(v, "s.ledgy.safeguards"); !strings.Contains(f.Value, "Kill switch") || !strings.Contains(f.Value, "masked") || !strings.Contains(f.Value, "logged") || !strings.Contains(f.Value, "no autonomous actions") {
		t.Errorf("safeguards %q", f.Value)
	}
	if f := field(v, "s.ledgy.controls"); !strings.Contains(f.Value, "Kill switch") || !strings.Contains(f.Value, "logged") {
		t.Errorf("controls %q", f.Value)
	}

	// The compliance officer edits before filing.
	if _, err := o.SetDisclosureAnswer(ctx, T, "priya.nair", DiscAIML, v.Period.ID, "s.ledgy.in_system_audit", "N"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker edited: %v", err)
	}
	if _, err := o.SetDisclosureAnswer(ctx, T, "vikram.shah", DiscAIML, v.Period.ID, "s.ledgy.in_system_audit", "maybe"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad answer: %v", err)
	}
	v, err = o.SetDisclosureAnswer(ctx, T, "vikram.shah", DiscAIML, v.Period.ID, "s.ledgy.in_system_audit", "na")
	if f := field(v, "s.ledgy.in_system_audit"); err != nil || f.Value != "NA" || !f.Edited || f.Generated != "Y" {
		t.Fatalf("edit %+v %v", f, err)
	}

	// The calendar has one duty, with NSE, due on 15 April.
	up, _ := o.Upcoming(ctx, T, "2027-04-02", 30)
	due := 0
	for _, c := range up {
		if strings.HasPrefix(c.Obligation, "AI & ML systems report (half-yearly) to ") && c.Date == "2027-04-15" {
			due++
		}
	}
	if due != 1 {
		t.Fatalf("calendar: %d AI & ML duties in %+v", due, up)
	}
	ds, _ := o.Duties(ctx, T, "2027-04-02")
	for _, d := range ds {
		if d.ID == "OB-AIML-NSE" && (!d.Active || d.Filing != discSharedNote) {
			t.Fatalf("NSE duty %+v", d)
		}
		if (d.ID == "OB-AIML-BSE" || d.ID == "OB-AIML-MCX") && d.Active {
			t.Fatalf("separate duty %+v", d)
		}
	}

	// Filed on NSE with a screenshot.
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	f, err := o.UploadDisclosureEvidence(ctx, T, "vikram.shah", "nse-ack.png", png)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.UploadDisclosureEvidence(ctx, T, "vikram.shah", "x.exe", []byte("MZ\x90\x00 not a picture")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("exe accepted: %v", err)
	}
	if _, err := o.MarkDisclosureSubmitted(ctx, T, "vikram.shah", DiscAIML, v.Period.ID, DiscSubmission{Exchange: "NSE", Date: "2027-04-05"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no reference accepted: %v", err)
	}
	v, err = o.MarkDisclosureSubmitted(ctx, T, "vikram.shah", DiscAIML, v.Period.ID, DiscSubmission{Exchange: "NSE", Date: "2027-04-05", Ref: "ENIT-77881", FileID: f.ID})
	if err != nil || !v.Locked || v.Status != "Submitted" || v.Exchanges[0].Submitted == nil || v.Exchanges[0].Submitted.FileName != "nse-ack.png" || len(v.Exchanges[0].Submitted.Answers) == 0 {
		t.Fatalf("submitted %+v %v", v.Exchanges, err)
	}
	if _, err := o.MarkDisclosureSubmitted(ctx, T, "vikram.shah", DiscAIML, v.Period.ID, DiscSubmission{Exchange: "NSE", Date: "2027-04-06", Ref: "again"}); !errors.Is(err, ErrState) {
		t.Fatalf("filed twice: %v", err)
	}
	if _, err := o.SetDisclosureAnswer(ctx, T, "vikram.shah", DiscAIML, v.Period.ID, "s.ledgy.claims", "x"); !errors.Is(err, ErrState) {
		t.Fatalf("edited after filing: %v", err)
	}
	up, _ = o.Upcoming(ctx, T, "2027-04-06", 30)
	for _, c := range up {
		if c.Obligation == "AI & ML systems report (half-yearly) to NSE" {
			t.Fatalf("NSE duty still due after filing: %+v", c)
		}
	}
	name, csv, err := o.DisclosureCSV(ctx, T, "vikram.shah", DiscAIML, v.Period.ID)
	if err != nil || !strings.HasPrefix(name, "aiml_disclosure_2026-10_2027-03") || !strings.Contains(string(csv), "ENIT-77881") || !strings.Contains(string(csv), "Safeguards against abnormal behaviour") {
		t.Fatalf("csv %s %s %v", name, csv, err)
	}

	// Filing with BSE and MCX only (no NSE): each exchange separately, and
	// the NSE duty stops; back to all three, it is shared through NSE again.
	p := o.discProfile(ctx, T)
	p.Exchanges = []string{"BSE", "MCX"}
	if _, err := o.SaveDisclosureProfile(ctx, T, "vikram.shah", p); err != nil {
		t.Fatal(err)
	}
	if v, _ = o.Disclosure(ctx, T, "vikram.shah", DiscAIML, v.Period.ID); len(v.Exchanges) != 3 || v.Status != "Due" || strings.Contains(strings.Join(v.Notes, " "), discSharedNote) {
		t.Fatalf("each exchange: %+v %s %v", v.Exchanges, v.Status, v.Notes)
	}
	ds, _ = o.Duties(ctx, T, "2027-04-06")
	active := map[string]bool{}
	for _, d := range ds {
		active[d.ID] = d.Active
	}
	if !active["OB-AIML-BSE"] || !active["OB-AIML-MCX"] || active["OB-AIML-NSE"] || !active["OB-SAAS-BSE"] || active["OB-SAAS-NSE"] {
		t.Fatalf("duties for BSE and MCX: %v", active)
	}
	p.Exchanges = nil
	if _, err := o.SaveDisclosureProfile(ctx, T, "vikram.shah", p); err != nil {
		t.Fatal(err)
	}
	if v, _ = o.Disclosure(ctx, T, "vikram.shah", DiscAIML, v.Period.ID); len(v.Exchanges) != 1 || v.Status != "Submitted" {
		t.Fatalf("shared again: %+v %s", v.Exchanges, v.Status)
	}
	ds, _ = o.Duties(ctx, T, "2027-04-06")
	for _, d := range ds {
		if strings.HasPrefix(d.ID, "OB-AIML-") && d.Active != (d.ID == "OB-AIML-NSE") {
			t.Fatalf("shared again: %+v", d)
		}
	}

	// With AI switched off for a whole later period, the answer is No.
	o.SetAIOff(ctx, T, "vikram.shah", true, "board decision")
	o.Now = func() time.Time { return time.Date(2027, 10, 2, 10, 0, 0, 0, ist) }
	if v, _ = o.Disclosure(ctx, T, "vikram.shah", DiscAIML, ""); v.Period.ID != "2027-04_2027-09" || v.Answer != "Yes" {
		// switched off on 2 April 2027, after the period started: still in use in it
		t.Fatalf("period with AI on at its start: %s %s", v.Period.ID, v.Answer)
	}
	o.Now = func() time.Time { return time.Date(2028, 4, 2, 10, 0, 0, 0, ist) }
	if v, _ = o.Disclosure(ctx, T, "vikram.shah", DiscAIML, ""); v.Period.ID != "2027-10_2028-03" || v.Answer != "No" || len(v.Fields) != 7 {
		t.Fatalf("off all period: %s %s %d", v.Period.ID, v.Answer, len(v.Fields))
	}
}

func TestSaaSDisclosure(t *testing.T) {
	o, ctx, _ := aiFirm(t) // AI on from 20 October 2026
	o.Now = func() time.Time { return time.Date(2027, 2, 2, 10, 0, 0, 0, ist) }
	// Hosted by Ledgerline, region not yet entered.
	v, err := o.Disclosure(ctx, T, "vikram.shah", DiscSaaS, "")
	if err != nil || v.Period.ID != "2026-07_2026-12" || v.Answer != "Yes" || field(v, "h.hosted").Value != "Y" {
		t.Fatalf("hosted: %s %s %v", v.Period.ID, v.Answer, err)
	}
	if f := field(v, "r.ledgerline.gaps"); !strings.Contains(f.Value, "not declared") {
		t.Fatalf("gaps without a region: %q", f.Value)
	}
	if f := field(v, "r.ai-anthropic.provider"); f.Value != "Anthropic PBC" || field(v, "r.ai-anthropic.geo").Value != "USA" {
		t.Fatalf("AI provider row: %+v", f)
	}
	if _, err := o.SaveDeployment(ctx, T, "priya.nair", "hosted", "AWS", "Mumbai (ap-south-1)"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker set deployment: %v", err)
	}
	if _, err := o.SaveDeployment(ctx, T, "vikram.shah", "cloudy", "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad deployment: %v", err)
	}
	if _, err := o.SaveDeployment(ctx, T, "vikram.shah", "hosted", "Amazon Web Services", "AWS Mumbai (ap-south-1)"); err != nil {
		t.Fatal(err)
	}
	v, _ = o.Disclosure(ctx, T, "vikram.shah", DiscSaaS, "")
	if field(v, "r.ledgerline.provider").Value != "Amazon Web Services" || field(v, "r.ledgerline.geo").Value != "AWS Mumbai (ap-south-1)" || field(v, "r.ledgerline.gaps").Value != "None identified" ||
		field(v, "r.ledgerline.nature").Value != "Back-office application" || !strings.Contains(field(v, "r.ledgerline.environment").Value, "client KYC, ledger, holdings, trades") {
		t.Fatalf("ledgerline row: %+v", v.Fields)
	}
	p := o.discProfile(ctx, T)
	p.InfoSecName, p.InfoSecEmail = "Vikram Shah", "infosec@bhairav.example"
	p.OtherSaaS = []SaaSRow{{Service: "Mail", Provider: "Example Mail", Nature: "Email", Environment: "Production", GeoLocation: "India"}}
	if _, err := o.SaveDisclosureProfile(ctx, T, "vikram.shah", p); err != nil {
		t.Fatal(err)
	}
	v, _ = o.Disclosure(ctx, T, "vikram.shah", DiscSaaS, "")
	if !strings.Contains(field(v, "h.contact").Value, "infosec@bhairav.example") || field(v, "r.other-1.service").Value != "Mail" || len(v.Systems) != 3 {
		t.Fatalf("profile: %+v", v.Systems)
	}
	// On the broker's own server with AI off all period: No.
	p.OtherSaaS = nil
	o.SaveDisclosureProfile(ctx, T, "vikram.shah", p)
	o.SaveDeployment(ctx, T, "vikram.shah", "self", "", "")
	v, _ = o.Disclosure(ctx, T, "vikram.shah", DiscSaaS, "2026-01_2026-06")
	if v.Answer != "No" || field(v, "h.hosted").Value != "N" {
		t.Fatalf("self-hosted, AI off: %s %+v", v.Answer, v.Systems)
	}
	// …but Yes for the period AI was on.
	v, _ = o.Disclosure(ctx, T, "vikram.shah", DiscSaaS, "2026-07_2026-12")
	if v.Answer != "Yes" || len(v.Systems) != 1 || !strings.Contains(v.Systems[0], "Anthropic") {
		t.Fatalf("self-hosted, AI on: %s %+v", v.Answer, v.Systems)
	}
	// One SaaS duty, with NSE (shared with BSE and MCX: NSE/INSP/70505,
	// Phase II item 19), due 31 January.
	ds, _ := o.Duties(ctx, T, "2027-02-02")
	n := 0
	for _, d := range ds {
		if strings.HasPrefix(d.ID, "OB-SAAS-") && d.Rule == "01-31,07-31" && d.Active {
			n++
			if d.ID != "OB-SAAS-NSE" || d.Filing != discSharedNote {
				t.Fatalf("SaaS duty %+v", d)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d SaaS duties", n)
	}
	if v, _ = o.Disclosure(ctx, T, "vikram.shah", DiscSaaS, ""); len(v.Exchanges) != 1 || !strings.Contains(strings.Join(v.Notes, " "), "NSE/INSP/70505") {
		t.Fatalf("SaaS filing: %+v %v", v.Exchanges, v.Notes)
	}
}
