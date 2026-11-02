package ops

import (
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

func dutyByID(ds []Duty, id string) Duty {
	for _, d := range ds {
		if d.ID == id {
			return d
		}
	}
	return Duty{}
}

func TestRASCalendarFY2627(t *testing.T) {
	// Every seeded date is the Friday/Saturday pair of the exchanges' calendar.
	cal := RASCalendarFY2627()
	if len(cal) != 16 {
		t.Fatalf("%d entries", len(cal))
	}
	want := map[string]string{"Quarterly|2026-27 Q1": "2026-04-17", "Quarterly|2026-27 Q3": "2026-10-16", "Monthly|2026-05": "2026-05-15", "Monthly|2026-11": "2026-11-06", "Quarterly|2026-27 Q4": "2027-01-01", "Monthly|2027-03": "2027-03-05"}
	for _, r := range cal {
		f, _ := time.Parse("2006-01-02", r.Dates[0])
		s, _ := time.Parse("2006-01-02", r.Dates[1])
		if f.Weekday() != time.Friday || s.Weekday() != time.Saturday || r.RunOn != r.Dates[0] {
			t.Fatalf("not a Friday/Saturday pair: %+v", r)
		}
		if w, ok := want[r.Kind+"|"+r.Period]; ok && w != r.RunOn {
			t.Fatalf("%s %s: %s, want %s", r.Kind, r.Period, r.RunOn, w)
		}
	}
}

func TestRASCalendarDrivesDutiesAndSettings(t *testing.T) {
	o, ctx := setup(t)
	if _, err := o.Duties(ctx, T, "2026-10-01"); err != nil { // the calendar is seeded on 1 October
		t.Fatal(err)
	}
	ds, err := o.Duties(ctx, T, "2026-10-20")
	if err != nil {
		t.Fatal(err)
	}
	q, m := dutyByID(ds, "OB-RAS"), dutyByID(ds, "OB-RAS-M")
	if q.Rule != "RASQ" || q.Task != "ras" || m.Rule != "RASM" {
		t.Fatalf("RAS duties: %+v %+v", q, m)
	}
	// Quarterly clients: Q3 was 16/17 Oct and is unsettled, so overdue; monthly: the same.
	if q.NextDue != "2026-10-17" || q.Period != "2026-27 Q3" || !q.Overdue {
		t.Fatalf("quarterly: %+v", q)
	}
	if m.NextDue != "2026-10-17" || m.Period != "2026-10" {
		t.Fatalf("monthly: %+v", m)
	}
	// Settling on the Friday closes both periods; the next ones are their own dates.
	if err := docs.Upsert(ctx, o.Docs, T, cRASBatch, "2026-10-16", RASBatch{Date: "2026-10-16", Items: []RASItem{}, Entries: []string{}}, "test"); err != nil {
		t.Fatal(err)
	}
	ds, _ = o.Duties(ctx, T, "2026-10-20")
	if q, m = dutyByID(ds, "OB-RAS"), dutyByID(ds, "OB-RAS-M"); q.NextDue != "2027-01-02" || m.NextDue != "2026-11-07" || m.Period != "2026-11" {
		t.Fatalf("after settling: %+v %+v", q, m)
	}
	// Settings follow the calendar.
	o.syncRASSettings(ctx, T, SystemActor, "2026-11-07")
	if s, _ := o.Settings(ctx, T); s.RASMonthly != "2026-12-04" || s.RASQuarterly != "2027-01-01" {
		t.Fatalf("settings: %s %s", s.RASMonthly, s.RASQuarterly)
	}
	if mm, qq := o.RASDates(ctx, T, "2027-03-06"); mm != "" || qq != "" {
		t.Fatalf("past the calendar: %s %s", mm, qq)
	}

	// Compliance and admin keep the list; the Saturday may be the run day.
	p := o.CompProfile(ctx, T)
	if _, err := o.SaveCompProfile(ctx, T, "anita.rao", p); err == nil {
		t.Fatal("risk manager changed the calendar")
	}
	bad := p
	bad.RAS = append(append([]RASDate{}, p.RAS...), RASDate{Kind: RASKindMonthly, Period: "2027-04", Dates: []string{"2027-04-15"}})
	if _, err := o.SaveCompProfile(ctx, T, "vikram.shah", bad); err == nil || !strings.Contains(err.Error(), "Thursday") {
		t.Fatalf("a Thursday accepted: %v", err)
	}
	p.RAS = append(p.RAS, RASDate{Kind: RASKindMonthly, Period: "2027-04", Dates: []string{"2027-04-16", "2027-04-17"}, RunOn: "2027-04-17"})
	if _, err := o.SaveCompProfile(ctx, T, "admin", p); err != nil {
		t.Fatal(err)
	}
	if mm, _ := o.RASDates(ctx, T, "2027-03-06"); mm != "2027-04-17" {
		t.Fatalf("Saturday run: %s", mm)
	}
}

func TestDutiesFilingAndSystemAudit(t *testing.T) {
	o, ctx := setup(t)
	ds, _ := o.Duties(ctx, T, "2026-10-20")
	// Seeded firm: NSE, BSE and MCX member, so common submissions go to NSE only.
	nw := dutyByID(ds, "OB-NETWORTH")
	if !nw.Shared || nw.Filing != "File with NSE (shared with the other exchanges)" || nw.NextDue != "2026-11-29" {
		t.Fatalf("net worth: %+v", nw)
	}
	n := 0
	for _, d := range ds {
		if strings.HasPrefix(d.ID, "OB-NETWORTH") || strings.HasPrefix(d.ID, "OB-ANNUALRET") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("shared duties made per exchange: %d", n)
	}
	if d := dutyByID(ds, "OB-BANKBAL"); d.Title != "Confirm your banks submit daily client bank balances to the exchange" || d.Frequency != "Yearly" {
		t.Fatalf("bank balances duty: %+v", d)
	}
	if d := dutyByID(ds, "OB-CHARTER"); !strings.Contains(d.Title, "2025/22") {
		t.Fatalf("charter: %+v", d)
	}
	if d := dutyByID(ds, "OB-SURV"); d.NextDue != "2026-10-15" && d.NextDue != "2027-01-15" {
		t.Fatalf("surveillance: %+v", d)
	}
	if d := dutyByID(ds, "OB-BSEHOLD"); d.ID != "" {
		t.Fatal("BSE holding duty for an NSE member")
	}
	if d := dutyByID(ds, "OB-SYSAUDIT"); d.NextDue != "2027-06-30" && d.NextDue != "2026-06-30" || d.Warning == "" {
		t.Fatalf("system audit: %+v", d)
	}
	// Type III members: half-yearly, with the April-September milestones.
	p := o.CompProfile(ctx, T)
	p.SysAudit, p.AuditorEmpanelled, p.Auditor, p.MTF = SysAuditHalfYearly, true, "A & Co (NSE empanelled)", true
	if _, err := o.SaveCompProfile(ctx, T, "vikram.shah", p); err != nil {
		t.Fatal(err)
	}
	ds, _ = o.Duties(ctx, T, "2026-10-20")
	if d := dutyByID(ds, "OB-SYSAUDIT-PLAN"); d.Frequency != "Half-yearly" || d.Warning != "" {
		t.Fatalf("plan: %+v", d)
	}
	for id, want := range map[string]string{"OB-SYSAUDIT": "2026-11-30", "OB-SYSAUDIT-ATR": "2027-02-28", "OB-NETWORTH": "2026-11-14"} {
		if d := dutyByID(ds, id); d.NextDue != want {
			t.Fatalf("%s: %+v", id, d)
		}
	}
	if d := dutyByID(ds, "OB-SYSAUDIT-PLAN"); d.NextDue != "2026-11-05" && d.NextDue != "2026-06-05" {
		t.Fatalf("plan: %+v", d)
	}
}

func TestBSEHoldingForBSEOnlyMember(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	if ok, why := bseHoldingApplies(set, "2026-10-20"); ok || !strings.Contains(why, "NSE/INSP/70505") {
		t.Fatalf("NSE and BSE member: %v %s", ok, why)
	}
	if ok, _ := bseHoldingApplies(set, "2025-10-14"); !ok {
		t.Fatal("before NSE shared it, BSE took its own")
	}
	set.MemberCodes = map[string]string{"BSE": "6127"}
	if err := docs.Upsert(ctx, o.Docs, T, cSettings, "main", set, "test"); err != nil {
		t.Fatal(err)
	}
	ds, _ := o.Duties(ctx, T, "2026-10-20") // Tuesday: tracked from the day before
	d := dutyByID(ds, "OB-BSEHOLD")
	if d.ID == "" || d.NextDue != "2026-10-20" || d.Period != "2026-10-19" {
		t.Fatalf("BSE duty: %+v", d)
	}
	if _, err := o.BSEHoldingUploaded(ctx, T, "vikram.shah", "2026-10-19", "BEFS-76"); err != nil {
		t.Fatal(err)
	}
	ds, _ = o.Duties(ctx, T, "2026-10-20")
	if d = dutyByID(ds, "OB-BSEHOLD"); d.NextDue != "2026-10-21" || d.Period != "2026-10-20" {
		t.Fatalf("BSE duty after the 19th: %+v", d)
	}
	// The next morning the 20th is pending and chased.
	if n := o.bseHoldingChase(ctx, T, "2026-10-21"); n != 1 {
		t.Fatalf("chased %d", n)
	}
	if _, err := o.BSEHoldingUploaded(ctx, T, "vikram.shah", "2026-10-18", "ACK1"); err == nil {
		t.Fatal("Sunday recorded")
	}
	if _, err := o.BSEHoldingUploaded(ctx, T, "vikram.shah", "2026-10-20", ""); err == nil {
		t.Fatal("recorded without acknowledgement")
	}
	if _, err := o.BSEHoldingUploaded(ctx, T, "vikram.shah", "2026-10-20", "BEFS-77"); err != nil {
		t.Fatal(err)
	}
	if n := o.bseHoldingChase(ctx, T, "2026-10-21"); n != 0 {
		t.Fatalf("still chased %d", n)
	}
	o.Now = func() time.Time { return time.Date(2026, 10, 21, 9, 0, 0, 0, ist) }
	v, err := o.BSEHolding(ctx, T, "vikram.shah", "2026-10-20")
	if err != nil || !v.Applies || v.API != BSEHoldingAPIStatus || len(v.Filings) != 2 || v.Filings[0].Ack != "BEFS-77" {
		t.Fatalf("view: %+v %v", v, err)
	}
}
