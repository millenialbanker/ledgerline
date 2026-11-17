package ops

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/xfile"
)

// All data here is made up: the sample firm and records added by the tests.

// rasDSCNBA names the sample firm's payout account as its DSCNBA.
func rasDSCNBA(t *testing.T, o *Ops) FirmBank {
	t.Helper()
	ctx := t.Context()
	set, _ := o.Settings(ctx, T)
	b, err := o.SaveFirmBank(ctx, T, siCO, FirmBankReq{FirmBank: FirmBank{Kind: BankDSCNBA, Bank: "Made-up Bank", Account: set.PayoutDebitAccount, IFSC: "MADE0000001", Reported: true, Serves: []string{"NSE", "BSE"}}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func rasUCCOf(t *testing.T, v RASUCCView, date string) RASUCCSettlement {
	t.Helper()
	for _, s := range v.Settlements {
		if s.Date == date {
			return s
		}
	}
	t.Fatalf("no settlement of %s in %+v", date, v.Settlements)
	return RASUCCSettlement{}
}

func rasOpenWork(t *testing.T, o *Ops, key string) *WorkItem {
	t.Helper()
	ws, _ := o.WorkItems(t.Context(), T, "Open")
	for i := range ws {
		if ws[i].Key == key {
			return &ws[i]
		}
	}
	return nil
}

// The UCC-wise report of a settlement: 17 fields, NA bank details for
// clients only retained, a ZIP named PAN_QS_DDMMYYYY_SeqNo whose sequence
// never repeats, and the upload recorded with its reference, late or not.
func TestRASUCCReport(t *testing.T) {
	o, ctx := setup(t)
	move := siClock(o, siAt(D, "18:00"))
	set, _ := o.Settings(ctx, T)
	pan := firmPAN(set)
	if pan != "AAACB1234C" {
		t.Fatalf("pan %q", pan)
	}
	// Before the settlement is run: the calendar date is to report, and
	// cannot be made into a file.
	v, err := o.RASUCCReports(ctx, T, "priya.nair", D)
	if err != nil || v.Exchange != "NSE" || !strings.Contains(v.FiledWith, "NSE only") || len(v.Fields) != 17 || v.TMCode != "90311" {
		t.Fatalf("view %+v %v", v, err)
	}
	s := rasUCCOf(t, v, D)
	if s.Settled || s.Status != "To report" || s.Due != "2026-10-30" || !s.CanNA || !strings.Contains(s.Kind, "onthly") {
		t.Fatalf("before the run %+v", s)
	}
	if _, err := o.PrepareRASUCC(ctx, T, "priya.nair", D, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("file made with no settlement: %v", err)
	}
	if _, err := o.RASUCCReports(ctx, T, "meera.iyer", D); !errors.Is(err, ErrForbidden) {
		t.Fatalf("branch staff saw the reports: %v", err)
	}
	rasDSCNBA(t, o)
	b, err := o.RunRAS(ctx, T, "farhan.khan", D, D)
	if err != nil || len(b.Items) == 0 || b.PaidFrom != set.PayoutDebitAccount || b.PaidFromIFSC != "MADE0000001" {
		t.Fatalf("run %+v %v", b, err)
	}
	if w := rasOpenWork(t, o, wRASUCC+":"+D); w == nil || w.Role != RoleCompliance || !strings.HasPrefix(w.Due, "2026-10-30") {
		t.Fatalf("no reminder: %+v", w)
	}
	v, _ = o.RASUCCReports(ctx, T, "priya.nair", D)
	s = rasUCCOf(t, v, D)
	if !s.Settled || s.Paid != len(b.Items) || s.CanNA || s.Status != "To report" || s.NextFile != pan+"_QS_16102026_01.zip" || len(s.Problems) != 0 {
		t.Fatalf("after the run %+v", s)
	}
	f, err := o.PrepareRASUCC(ctx, T, "priya.nair", D, false)
	if err != nil || f.Seq != 1 || f.Name != pan+"_QS_16102026_01.zip" || f.CSV != pan+"_QS_16102026_01.csv" || f.Rows != len(b.Items) || f.PaidRows != f.Rows {
		t.Fatalf("file %+v %v", f, err)
	}
	name, data, err := o.RASUCCFileData(ctx, T, "priya.nair", D, 0, false)
	if err != nil || name != f.Name {
		t.Fatal(name, err)
	}
	inner, err := xfile.Unpack(name, data)
	if err != nil || len(inner) != 1 || inner[0].Name != f.CSV {
		t.Fatalf("zip %v %v", inner, err)
	}
	lines := strings.Split(strings.TrimRight(string(inner[0].Data), "\r\n"), "\r\n")
	recs, err := csv.NewReader(bytes.NewReader(inner[0].Data)).ReadAll()
	if err != nil || len(lines) != f.Rows+1 || len(recs) != f.Rows+1 || len(recs[0]) != 17 || len(recs[1]) != 17 ||
		!strings.HasPrefix(lines[0], "Date of Settlement,TM Code,TM PAN,Client UCC,Value of amount retained,Value of amount paid,TM Bank account number,TM Bank IFSC,") ||
		recs[0][11] != "If no, reasons for other than electronic Mode" || recs[0][16] != "SMS – Sent Date" {
		t.Fatalf("header %q (%d lines) %v", lines[0], len(lines), err)
	}
	row := lines[1]
	if !strings.HasPrefix(row, "16-10-2026,90311,"+pan+",") || !strings.Contains(row, `,"`+set.PayoutDebitAccount+`",MADE0000001,"`) || !strings.Contains(row, ",Yes,,") {
		t.Fatalf("row %q", row)
	}
	again, _ := o.PrepareRASUCC(ctx, T, "priya.nair", D, false)
	if again.Seq != 1 {
		t.Fatalf("nothing changed, but a new file %+v", again)
	}
	fresh, err := o.PrepareRASUCC(ctx, T, "farhan.khan", D, true)
	if err != nil || fresh.Seq != 2 || fresh.Name != pan+"_QS_16102026_02.zip" {
		t.Fatalf("fresh %+v %v", fresh, err)
	}
	// Recording the upload.
	if _, err := o.RecordRASUCC(ctx, T, "priya.nair", D, RASUCCFileReq{Ref: "ACK-MADEUP-1"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker recorded the upload: %v", err)
	}
	if _, err := o.RecordRASUCC(ctx, T, siCO, D, RASUCCFileReq{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no reference: %v", err)
	}
	if _, err := o.RecordRASUCC(ctx, T, siCO, D, RASUCCFileReq{Ref: "ACK-MADEUP-1", NotApplicable: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not applicable with clients settled: %v", err)
	}
	rec, err := o.RecordRASUCC(ctx, T, siCO, D, RASUCCFileReq{Ref: "ACK-MADEUP-1", On: D})
	if err != nil || len(rec.Uploads) != 1 || rec.Uploads[0].File != fresh.Name || rec.Uploads[0].Late || rec.Uploads[0].By != siCO {
		t.Fatalf("record %+v %v", rec, err)
	}
	if rasOpenWork(t, o, wRASUCC+":"+D) != nil {
		t.Fatal("the reminder is still open")
	}
	ps, _ := o.Periodic(ctx, T, D)
	for _, p := range ps {
		if p.ID == "ras_ucc_report" && (p.Period != "2026-11-06" || p.Status != "Upcoming") {
			t.Fatalf("periodic after the upload %+v", p)
		}
	}
	// The monthly settlement of 6 November: clients with margin are settled
	// by retention alone, with NA bank details.
	move(siAt("2026-11-06", "18:00"))
	b2, err := o.RunRAS(ctx, T, "farhan.khan", D, "2026-11-06")
	if err != nil || len(b2.Retained) == 0 || len(b2.Items) == 0 {
		t.Fatalf("monthly run %d paid, %d retained: %v", len(b2.Items), len(b2.Retained), err)
	}
	if _, err := o.RunRAS(ctx, T, "farhan.khan", D, "2026-11-06"); !errors.Is(err, ErrState) {
		t.Fatalf("second run recorded the retained clients again: %v", err)
	}
	d, err := o.RASUCCReport(ctx, T, "priya.nair", "2026-11-06")
	if err != nil || len(d.Rows) != len(b2.Items)+len(b2.Retained) || len(d.Keys) != 17 {
		t.Fatalf("detail %d rows %v", len(d.Rows), err)
	}
	kept, paid := 0, 0
	for _, r := range d.Rows {
		switch r.Kind {
		case "Retained only":
			kept++
			if r.Values["tm_account"] != "NA" || r.Values["client_ifsc"] != "NA" || r.Values["paid"] != "0.00" || r.Values["retained"] == "0.00" || r.Values["electronic"] != "NA" {
				t.Fatalf("retained-only row %+v", r)
			}
		default:
			paid++
			if !strings.HasPrefix(r.Values["client_account"], "XX") || r.Values["tm_ifsc"] != "MADE0000001" {
				t.Fatalf("paid row not masked or no IFSC %+v", r)
			}
		}
	}
	if kept != len(b2.Retained) || paid != len(b2.Items) {
		t.Fatalf("kept %d paid %d", kept, paid)
	}
	f3, err := o.PrepareRASUCC(ctx, T, "priya.nair", "2026-11-06", false)
	if err != nil || f3.Seq != 1 || f3.RetainedRows != len(b2.Retained) || !strings.Contains(f3.Name, "_QS_06112026_01") {
		t.Fatalf("monthly file %+v %v", f3, err)
	}
	_, csv, _ := o.RASUCCFileData(ctx, T, "priya.nair", "2026-11-06", 0, true)
	if !strings.Contains(string(csv), ",0.00,NA,NA,NA,NA,NA,,") {
		t.Fatalf("no retained-only line with NA: %s", csv)
	}
	// Uploaded late: the penalty and the self-inspection see it.
	move(siAt("2026-11-30", "10:00"))
	due := addTradingDays(set, "2026-11-07", rasUCCDays)
	v, _ = o.RASUCCReports(ctx, T, siCO, "2026-11-30")
	s = rasUCCOf(t, v, "2026-11-06")
	if s.Status != "Overdue" || s.Due != due || s.DaysLate != daysBetween(due, "2026-11-30") || s.Penalty.Sign() <= 0 {
		t.Fatalf("overdue %+v", s)
	}
	rec, err = o.RecordRASUCC(ctx, T, siCO, "2026-11-06", RASUCCFileReq{Ref: "ACK-MADEUP-2"})
	if err != nil || !rec.Uploads[0].Late || rec.Uploads[0].On != "2026-11-30" || rec.Uploads[0].DaysLate != daysBetween(due, "2026-11-30") {
		t.Fatalf("late record %+v %v", rec, err)
	}
	out := siOne(t, o, ctx, "ras_ucc_report", "2026-11-30")
	siWant(t, out, SIFail, "the November settlement reported late")
	if out.found != 1 || !strings.Contains(out.examples[0], "after the due date") {
		t.Fatalf("self-inspection %+v", out)
	}
	// A settlement date with no run: not applicable only if no client had
	// funds to settle. In December every sample client has been idle for 30
	// days, so it is refused.
	move(siAt("2026-12-21", "10:00"))
	v, _ = o.RASUCCReports(ctx, T, siCO, "2026-12-21")
	s = rasUCCOf(t, v, "2026-12-04")
	if s.Settled || !s.CanNA || s.Status != "Overdue" {
		t.Fatalf("december %+v", s)
	}
	if _, err := o.PrepareRASUCC(ctx, T, "priya.nair", "2026-12-04", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("file for an unsettled date: %v", err)
	}
	if _, err := o.RecordRASUCC(ctx, T, siCO, "2026-12-04", RASUCCFileReq{Ref: "NA-MADEUP-3", On: "2026-12-07", NotApplicable: true}); err == nil || !strings.Contains(err.Error(), "credit balance") {
		t.Fatalf("not applicable with clients due: %v", err)
	}
	// A firm with no client in credit records it.
	const empty = "nafirm"
	if err := o.SeedDev(ctx, empty); err != nil {
		t.Fatal(err)
	}
	na, err := o.RecordRASUCC(ctx, empty, siCO, D, RASUCCFileReq{Ref: "NA-MADEUP-4", On: "2026-11-05", NotApplicable: true})
	if err != nil || na.NotApplicable == nil || !na.NotApplicable.Late || na.NotApplicable.Ref != "NA-MADEUP-4" {
		t.Fatalf("not applicable %+v %v", na, err)
	}
	if _, err := o.RecordRASUCC(ctx, empty, siCO, D, RASUCCFileReq{Ref: "ACK-MADEUP-5", File: "made-up.zip"}); !errors.Is(err, ErrState) {
		t.Fatalf("an upload after not applicable: %v", err)
	}
}

// The task of Periodic work makes the file after a settlement and waits for
// the upload.
func TestRASUCCPeriodic(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "18:00"))
	rasDSCNBA(t, o)
	run, err := o.RunPeriodic(ctx, T, SystemActor, "ras_ucc_report", "", D)
	if err != nil || run.Status != "Waiting for the settlement" || run.Period != D {
		t.Fatalf("before the settlement %+v %v", run, err)
	}
	if w := rasOpenWork(t, o, wRASUCC+":"+D); w == nil || !strings.Contains(w.Title, "Report the settlement") {
		t.Fatalf("reminder %+v", w)
	}
	if _, err := o.RunRAS(ctx, T, "farhan.khan", D, D); err != nil {
		t.Fatal(err)
	}
	run, err = o.RunPeriodic(ctx, T, SystemActor, "ras_ucc_report", "", D)
	if err != nil || run.Status != "Waiting for upload" || len(run.Files) != 1 || !strings.HasSuffix(run.Files[0].Name, "_01.zip") {
		t.Fatalf("after the settlement %+v %v", run, err)
	}
	siWant(t, siOne(t, o, ctx, "ras_ucc_report", D), SISkipped, "nothing due yet")
	siWant(t, siOne(t, o, ctx, "ras_ucc_report", "2026-11-02"), SIFail, "not reported by 30 October")
	if _, err := o.RecordRASUCC(ctx, T, siCO, D, RASUCCFileReq{Ref: "ACK-MADEUP-9", On: D}); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "ras_ucc_report", "2026-11-02"), SIPass, "reported on time")
	ps, _ := o.Periodic(ctx, T, D)
	for _, p := range ps {
		if p.ID == "ras_ucc_report" && p.Status == "Overdue" {
			t.Fatalf("still overdue %+v", p)
		}
	}
}
