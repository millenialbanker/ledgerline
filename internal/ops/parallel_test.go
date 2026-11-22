package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

// All data here is made up.

func TestParallelCompare(t *testing.T) {
	o, ctx := newOps(t)
	siClock(o, siAt("2026-11-20", "19:00"))
	// NSE Clearing's client collateral file (no header): the old system's
	// and Ledgerline's for the same day; rows in another order, numbers
	// written differently.
	old := "ABC123,1000.50,0,0,1000.50,900\nXYZ999,200,0,0,200,150\nQQQ111,5,0,0,5,5\n"
	neu := "XYZ999,200.00,0,0,200.00,150.00\nABC123,1000.5,0,0,1000.5,950\n"
	c, err := o.ParallelCompare(ctx, T, "priya.nair", "2026-11-20", "", "C_CC90311_01_20261120.csv", []byte(old), "C_CC90311_01_20261120.csv", []byte(neu))
	if err != nil {
		t.Fatal(err)
	}
	if c.Format != "ncl-client-collateral" || c.OldRows != 3 || c.NewRows != 2 || c.Matched != 1 || c.Differing != 1 || c.OnlyOld != 1 || c.OnlyNew != 0 || c.Match {
		t.Fatalf("comparison %+v", c)
	}
	if strings.Join(c.KeyColumns, ",") != "ClientCode" {
		t.Fatalf("keys %v", c.KeyColumns)
	}
	cols := map[string]ParallelColumn{}
	for _, x := range c.Columns {
		cols[x.Name] = x
	}
	if m := cols["Margins"]; m.Diffs != 1 || !m.Numeric || m.OldTotal != "1055" || m.NewTotal != "1100" {
		t.Fatalf("margins %+v", m)
	}
	if cols["CashAllocated"].Diffs != 0 || cols["CashAllocated"].OldTotal != "1205.5" {
		t.Fatalf("cash %+v", cols["CashAllocated"])
	}
	// Client codes are masked; numbers are in full.
	for _, l := range c.Lines {
		if strings.Contains(l.Key, "ABC123") || strings.Contains(l.Key, "QQQ111") {
			t.Fatalf("client code on screen: %+v", l)
		}
	}
	var diff ParallelLine
	for _, l := range c.Lines {
		if l.Kind == "differs" {
			diff = l
		}
	}
	if diff.Column != "Margins" || diff.Old != "900" || diff.New != "950" || diff.Key != "AB**23" {
		t.Fatalf("line %+v", diff)
	}
	// The difference raised a work item for the Ops checker.
	w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-parallel-diff-2026-11-20-ncl-client-collateral-"+c.ID)
	if err != nil || w.Role != RoleChecker || w.Status != "Open" || w.View != "parallel" {
		t.Fatalf("work %+v %v", w, err)
	}

	// Fixed: the same day compared again matches, and the work item closes.
	c2, err := o.ParallelCompare(ctx, T, "farhan.khan", "2026-11-20", "ncl-client-collateral", "old.csv", []byte(old), "new.csv", []byte(old))
	if err != nil || !c2.Match || c2.Matched != 3 {
		t.Fatalf("again %+v %v", c2, err)
	}
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-parallel-diff-2026-11-20-ncl-client-collateral-"+c.ID); w.Status != "Done" {
		t.Fatalf("work not closed: %+v", w)
	}

	// A JSON file is read from its first array of records; record numbers
	// are ignored, the demat account and client code are keys.
	hs := func(qty string) []byte {
		return []byte(`{"memberPan":"AAAAA1111A","date":"20-11-2026","holdingData":[{"recId":"1","dmat":"1208160000012345","accountType":"OWN","ucc":"ABC123","clientName":"Test Person","pan":"AAAPA1234A","isin":"INE000A01011","totalQty":"` + qty + `"},{"recId":"2","dmat":"1208160000099999","accountType":"POOL","ucc":"","clientName":"","pan":"","isin":"INE000A01011","totalQty":"5.000"}]}`)
	}
	c3, err := o.ParallelCompare(ctx, T, "priya.nair", "2026-11-19", "holding-statement-api", "a.json", hs("10.000"), "b.json", hs("12"))
	if err != nil || c3.Differing != 1 || c3.Matched != 1 || strings.Join(c3.Ignored, ",") != "recId" {
		t.Fatalf("json %+v %v", c3, err)
	}
	if l := c3.Lines[0]; l.Column != "totalQty" || l.Old != "10.000" || strings.Contains(l.Key, "1208160000012345") || !strings.Contains(l.Key, "XXXXXXXXXXXX2345") || !strings.Contains(l.Key, "INE000A01011") {
		t.Fatalf("json line %+v", l)
	}
	// Text is masked on screen.
	c4, _ := o.ParallelCompare(ctx, T, "priya.nair", "2026-11-19", "holding-statement-api", "a.json", hs("10.000"), "b.json", []byte(strings.Replace(string(hs("10.000")), "Test Person", "Test Persson", 1)))
	if len(c4.Lines) != 1 || c4.Lines[0].Old != "Te******" || strings.Contains(c4.Lines[0].New, "Persson") {
		t.Fatalf("masked %+v", c4.Lines)
	}

	// Branch staff cannot compare or see.
	if _, err := o.ParallelCompare(ctx, T, "meera.iyer", "2026-11-20", "", "a.csv", []byte(old), "b.csv", []byte(old)); err == nil {
		t.Fatal("branch staff compared")
	}
	if _, err := o.ParallelCompare(ctx, T, "priya.nair", "2026-11-20", "", "a.csv", []byte(old), "b.csv", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("one file: %v", err)
	}
}

// A day matches when each layout's latest comparison of the day matches;
// the streak counts business days back and stops at a gap.
func TestParallelStreak(t *testing.T) {
	o, ctx := newOps(t)
	siClock(o, siAt("2026-11-20", "19:00"))
	f := []byte("ABC123,1,0,0,1,1\n")
	g := []byte("ABC123,2,0,0,2,1\n")
	cmp := func(date string, same bool) {
		t.Helper()
		n := f
		if !same {
			n = g
		}
		if _, err := o.ParallelCompare(ctx, T, "priya.nair", date, "ncl-client-collateral", "o.csv", f, "n.csv", n); err != nil {
			t.Fatal(err)
		}
	}
	set, _ := o.Settings(ctx, T)
	d := "2026-11-20"
	var days []string
	for i := 0; i < 4; i++ {
		days = append([]string{d}, days...)
		d = set.PrevBusinessDay(d)
	}
	cmp(days[0], true)
	cmp(days[1], false) // differs, then fixed the same day
	cmp(days[1], true)
	cmp(days[2], true)
	cmp(days[3], true)
	v, err := o.ParallelRun(ctx, T, "anita.rao")
	if err != nil || v.Goal != 10 || v.Streak != 4 || v.StreakTo != days[3] || v.StreakFrom != days[0] || len(v.Days) != 4 || !v.Days[0].Match || v.CanCompare {
		t.Fatalf("view streak %d %s..%s days %d can %v %v", v.Streak, v.StreakFrom, v.StreakTo, len(v.Days), v.CanCompare, err)
	}
	if v.Days[2].Formats[0].Runs != 2 {
		t.Fatalf("runs %+v", v.Days[2])
	}
	// A second layout that differs on the last day breaks the streak.
	hs := []byte(`[{"ucc":"A1","isin":"INE000A01011","totalQty":"1"}]`)
	if _, err := o.ParallelCompare(ctx, T, "priya.nair", days[3], "holding-statement-api", "a.json", hs, "b.json", []byte(`[{"ucc":"A1","isin":"INE000A01011","totalQty":"2"}]`)); err != nil {
		t.Fatal(err)
	}
	if v, _ = o.ParallelRun(ctx, T, "priya.nair"); v.Streak != 0 || v.Days[0].Match || !v.CanCompare {
		t.Fatalf("broken streak %d", v.Streak)
	}
	// A business day with no comparison ends the streak.
	cmp(addDays(days[3], 7), true)
	if v, _ = o.ParallelRun(ctx, T, "priya.nair"); v.Streak != 1 {
		t.Fatalf("gap streak %d", v.Streak)
	}
	full, err := o.ParallelComparisonByID(ctx, T, "priya.nair", v.Recent[len(v.Recent)-1].ID)
	if err != nil || full.Date != days[0] {
		t.Fatalf("one %+v %v", full, err)
	}
}
