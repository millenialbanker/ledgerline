package ops

import (
	"strings"
	"testing"
)

// The PF ECR and ESI files follow EPFO's ECR 2.0 and ESIC's MC template
// rules (see PFECR and ESIFile).
func TestPayrollFileFormats(t *testing.T) {
	if got := esiName("Anilkumar.T.P. "); got != "ANILKUMAR T P" {
		t.Fatalf("esiName = %q", got)
	}
	r := hrSetup(t)
	o, ctx := r.o, r.ctx
	a, _, _ := payrollPeople(t, r)
	approve := func(month string) {
		t.Helper()
		if _, err := o.MakePayrun(ctx, T, "paul.pay", month); err != nil {
			t.Fatal(err)
		}
		if _, err := o.SubmitPayrun(ctx, T, "paul.pay", month); err != nil {
			t.Fatal(err)
		}
		if _, err := o.ApprovePayrun(ctx, T, "hema.hr", month); err != nil {
			t.Fatal(err)
		}
	}
	// April: Ravi has 1.5 days of loss of pay. ECR NCP days are whole days
	// only (1); ESI days paid round up (28.5 -> 29).
	lop := 1.5
	if _, err := o.SavePayInput(ctx, T, "paul.pay", PayInput{Month: "2026-04", Employee: a.Code, LOP: &lop}); err != nil {
		t.Fatal(err)
	}
	approve("2026-04")
	ecr, err := o.PFECR(ctx, T, "paul.pay", "2026-04")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(ecr)), "\n") {
		f := strings.Split(l, "#~#")
		if len(f) != 11 {
			t.Fatalf("ECR line has %d fields: %s", len(f), l)
		}
		if f[0] == "100000000001" && f[9] != "1" {
			t.Fatalf("NCP days %q, want 1: %s", f[9], l)
		}
	}
	esi, _ := o.ESIFile(ctx, T, "paul.pay", "2026-04")
	if !strings.Contains(string(esi), "\n1000000001,RAVI TEST,29,") {
		t.Fatalf("ESI April:\n%s", esi)
	}
	// Ravi leaves on 14 May: May shows his wages with no reason; June shows
	// 0 days, reason 2 and his last working day.
	if _, err := o.StartLeaving(ctx, T, "hema.hr", a.Code, "2026-05-14", "Resigned"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApproveLeaving(ctx, T, "harsh.hr", a.Code); err != nil {
		t.Fatal(err)
	}
	r.at("2026-06-02 10:00")
	approve("2026-05")
	esi, _ = o.ESIFile(ctx, T, "paul.pay", "2026-05")
	if !strings.Contains(string(esi), "\n1000000001,RAVI TEST,14,") || strings.Contains(string(esi), ",2,14/05/2026") {
		t.Fatalf("ESI May:\n%s", esi)
	}
	r.at("2026-07-02 10:00")
	approve("2026-06")
	esi, _ = o.ESIFile(ctx, T, "paul.pay", "2026-06")
	if !strings.Contains(string(esi), "\n1000000001,RAVI TEST,0,0,2,14/05/2026") {
		t.Fatalf("ESI June:\n%s", esi)
	}
}
