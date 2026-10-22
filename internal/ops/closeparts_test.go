package ops

import (
	"strings"
	"testing"
	"time"

	"ledgerline/internal/backup"
	"ledgerline/internal/docs"
	"ledgerline/internal/vault"
)

func TestSplitClose(t *testing.T) {
	o, ctx := setup(t)
	o.Backups, o.Sealer, o.BackupSync = &backup.Dir{Path: t.TempDir()}, vault.New(vault.Ephemeral()), true
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 19, 0, 0, 0, ist) }

	// The sample day trades NSE, BSE and MCX: both parts are needed.
	st, err := o.EOD(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Parts) != 2 || !st.Parts[0].Needed || !st.Parts[1].Needed || st.Closed {
		t.Fatalf("parts %+v", st.Parts)
	}
	for _, p := range st.Parts {
		if len(p.Checks) < 6 {
			t.Fatalf("%s checks %+v", p.ID, p.Checks)
		}
	}
	eqFiles, coFiles := st.Parts[0].Checks[0].Detail, st.Parts[1].Checks[0].Detail
	if eqFiles == coFiles || !strings.Contains(coFiles, "exchange trades") {
		t.Fatalf("files counted the same for both parts: %q %q", eqFiles, coFiles)
	}

	// Contract notes: commodity trades on their own note.
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBilling(ctx, T, "priya.nair", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	if _, err := o.GenerateNotes(ctx, T, "priya.nair", D); err != nil {
		t.Fatal(err)
	}
	ns, _ := o.Notes(ctx, T, D)
	var eq, co *ContractNote
	both := 0
	seen := map[string]int{}
	for i := range ns {
		seen[ns[i].UCC]++
		if ns[i].Part == PartCommodity {
			if co == nil {
				co = &ns[i]
			}
		} else if eq == nil {
			eq = &ns[i]
		}
	}
	for _, n := range seen {
		if n == 2 {
			both++
		}
	}
	if co == nil || eq == nil {
		t.Fatalf("notes: equity %v commodity %v", eq, co)
	}
	if noteSubject(*co) != "Commodity contract note "+co.No {
		t.Fatalf("subject %q", noteSubject(*co))
	}
	html, err := o.RenderNote(ctx, T, D, co.UCC, PartCommodity)
	if err != nil || !strings.Contains(string(html), "MCX-COM") || strings.Contains(string(html), "NSE-CM") {
		t.Fatalf("commodity note shows other trades (%v)", err)
	}
	if html, _ := o.RenderNote(ctx, T, D, eq.UCC, ""); strings.Contains(string(html), "MCX-COM") {
		t.Fatal("equity note shows MCX trades")
	}
	t.Logf("%d notes, %d clients with both", len(ns), both)

	// Close equity in the evening. The day is not closed yet.
	if _, err := o.ClosePart(ctx, T, "priya.nair", D, PartEquity, "x"); err == nil {
		t.Fatal("an Ops maker closed the day")
	}
	if _, err := o.ClosePart(ctx, T, "farhan.khan", D, PartEquity, ""); err == nil || !strings.Contains(err.Error(), "equity check") {
		t.Fatalf("closed with failing checks and no note: %v", err)
	}
	st, err = o.ClosePart(ctx, T, "farhan.khan", D, PartEquity, "test close")
	if err != nil {
		t.Fatal(err)
	}
	if st.Closed || !st.part(PartEquity).Closed || st.part(PartCommodity).Closed {
		t.Fatalf("after equity close: day %v parts %+v", st.Closed, st.Parts)
	}
	next := st.part(PartEquity).NextDay
	carried, _ := o.carried(ctx, T, next)
	for _, c := range carried {
		if PartOf(c.Exchange, c.Segment) != PartEquity {
			t.Fatalf("commodity position rolled with the equity close: %+v", c)
		}
	}
	if len(carried) == 0 || st.part(PartEquity).Rolled != len(carried) {
		t.Fatalf("equity positions rolled %d, carried %d", st.part(PartEquity).Rolled, len(carried))
	}
	if again, _ := o.EOD(ctx, T, D); again.Closed || !again.part(PartEquity).Closed {
		t.Fatal("equity close not kept")
	}
	es, _ := o.Backups.List(T)
	if len(es) != 1 || es[0].Part != PartEquity || es[0].Trigger != "after the equity close" {
		t.Fatalf("backups after equity close %+v", es)
	}

	// Next day: work cannot start until the commodity part is closed too.
	if err := o.DayOpenError(ctx, T, next); err == nil || !strings.Contains(err.Error(), "commodity part of "+D) {
		t.Fatalf("next day guard: %v", err)
	}
	if err := o.partOpenError(ctx, T, next, PartEquity); err != nil {
		t.Fatalf("equity of the next day blocked: %v", err)
	}

	// Close commodity next morning: the day is closed, commodity positions roll.
	o.Now = func() time.Time { return time.Date(2026, 10, 17, 7, 0, 0, 0, ist) }
	st, err = o.ClosePart(ctx, T, "farhan.khan", D, PartCommodity, "test close")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Closed || st.ClosedAt != "07:00" {
		t.Fatalf("day not closed: %+v", st)
	}
	carried, _ = o.carried(ctx, T, next)
	com := 0
	for _, c := range carried {
		if PartOf(c.Exchange, c.Segment) == PartCommodity {
			com++
		}
	}
	if com == 0 || com != st.part(PartCommodity).Rolled {
		t.Fatalf("commodity rolled %d, carried %d", st.part(PartCommodity).Rolled, com)
	}
	if err := o.DayOpenError(ctx, T, next); err != nil {
		t.Fatalf("next day still blocked: %v", err)
	}
	es, _ = o.Backups.List(T)
	if len(es) != 2 || es[0].Part != PartCommodity {
		t.Fatalf("backups %+v", es)
	}
	if _, err := o.ClosePart(ctx, T, "farhan.khan", D, PartCommodity, "x"); err == nil {
		t.Fatal("closed twice")
	}
	if _, err := o.ClosePart(ctx, T, "farhan.khan", D, "currency", "x"); err == nil {
		t.Fatal("unknown part accepted")
	}
	if _, err := o.CloseDay(ctx, T, "farhan.khan", D, "x"); err == nil {
		t.Fatal("closed a closed day")
	}
}

func TestDayClosedBeforePartsStillCounts(t *testing.T) {
	o, ctx := setup(t)
	// A close recorded before parts existed: the whole day is closed.
	if err := docs.Upsert(ctx, o.Docs, T, cEOD, D, EODState{Date: D, Closed: true, ClosedAt: "20:00", ClosedBy: "farhan.khan"}, "x"); err != nil {
		t.Fatal(err)
	}
	st, _ := o.EOD(ctx, T, D)
	if !st.Closed || !st.part(PartEquity).Closed || !st.part(PartCommodity).Closed {
		t.Fatalf("legacy close %+v", st)
	}
	if err := o.DayOpenError(ctx, T, "2026-10-19"); err != nil {
		t.Fatalf("legacy close blocks the next day: %v", err)
	}
}

func TestCommodityTradingDays(t *testing.T) {
	o, ctx := setup(t)
	// MCX's list: a weekday closed all day, and an equity holiday on which
	// MCX's evening session is open.
	csv := "Date,Description,Remarks\n2026-11-05,MCX closed,\n2026-11-24,Guru Nanak Jayanti,Evening session open\n"
	cal, err := o.UploadHolidays(ctx, T, "priya.nair", 2026, "MCX", "trading", []string{"COM"}, []byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideHolidays(ctx, T, "farhan.khan", cal.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	set, _ := o.Settings(ctx, T)
	set.Holidays = append(set.Holidays, "2026-11-24") // an equity holiday
	if set.IsTradingDay(PartCommodity, "2026-11-05") || !set.IsTradingDay(PartEquity, "2026-11-05") {
		t.Fatal("MCX-only holiday")
	}
	if got := set.NextTradingDay(PartCommodity, "2026-11-04"); got != "2026-11-06" {
		t.Fatalf("next MCX day after 4-Nov %s", got)
	}
	// 24-Nov: equity is shut; MCX trades in the evening, but Settings'
	// holidays still close every part.
	if set.IsTradingDay(PartEquity, "2026-11-24") {
		t.Fatal("equity holiday")
	}
	set.Holidays = nil
	if !set.IsTradingDay(PartCommodity, "2026-11-24") {
		t.Fatal("evening session day is not an MCX trading day")
	}
	// Years with no approved MCX list follow the business days.
	if set.IsTradingDay(PartCommodity, "2027-01-26") != set.IsTradingDay(PartEquity, "2027-01-26") {
		t.Fatal("2027 should follow the equity calendar")
	}
}

func TestAutopilotClosesEachPartOnTime(t *testing.T) {
	o, ctx := setup(t)
	at := time.Date(2026, 10, 16, 19, 0, 0, 0, ist)
	o.Now = func() time.Time { return at }
	for _, st := range []string{"bod", "intraday", "preclose", "eod"} {
		if _, err := o.RunStage(ctx, T, SystemActor, D, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := o.EOD(ctx, T, D)
	if st.part(PartCommodity).Closed {
		t.Fatal("the evening stage closed the commodity part")
	}
	// Whatever the evening left for people is about equity; the commodity
	// close waits for its own stage.
	ws, _ := o.WorkItems(ctx, T, "Open")
	for _, w := range ws {
		if strings.Contains(w.Title, "Commodity close") {
			t.Fatalf("commodity close queued in the evening: %s", w.Title)
		}
	}
	// Next morning the commodity job runs for the last MCX session.
	at = time.Date(2026, 10, 17, 7, 0, 0, 0, ist)
	js, _, err := o.Jobs(ctx, T)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range js {
		if j.ID == "JOB-AP-COM" {
			found = true
			if j.At != "07:00" || j.Params["stage"] != "commodity" {
				t.Fatalf("job %+v", j)
			}
		}
	}
	if !found {
		t.Fatal("no commodity close job")
	}
	run, err := o.RunJob(ctx, T, "admin", "JOB-AP-COM", "")
	if err != nil {
		t.Fatalf("%v %+v", err, run)
	}
	if run.Date != "2026-10-16" {
		t.Fatalf("commodity stage ran for %s", run.Date)
	}
	st, _ = o.EOD(ctx, T, D)
	cp := st.part(PartCommodity)
	if !cp.Closed {
		// Checks may legitimately fail on the sample (open breaks); then the
		// close is queued for a checker instead.
		ws, _ := o.WorkItems(ctx, T, "Open")
		ok := false
		for _, w := range ws {
			if strings.HasPrefix(w.Title, "Commodity close of 2026-10-16") {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("commodity part neither closed nor queued: %+v", cp)
		}
	}
}
