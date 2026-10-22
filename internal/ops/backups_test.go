package ops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/backup"
	"ledgerline/internal/docs"
	"ledgerline/internal/vault"
)

func TestBackupTiers(t *testing.T) {
	s := DefaultSettings()
	s.Holidays = []string{"2026-12-25", "2027-03-26"} // a Friday, and the Friday before March ends
	cases := map[string][]string{
		"2026-10-14": {"daily"},                        // Wednesday
		"2026-10-16": {"daily", "weekly"},              // Friday
		"2026-12-24": {"daily", "weekly"},              // Thursday before a Friday holiday
		"2026-12-31": {"daily", "quarterly"},           // Thursday; Friday 1-Jan is in the same ISO week
		"2027-03-25": {"daily", "weekly"},              // Thursday; Friday 26th a holiday
		"2027-03-31": {"daily", "quarterly", "yearly"}, // Wednesday: financial year ends
		"2026-06-30": {"daily", "quarterly"},           // Tuesday
		"2026-09-30": {"daily", "quarterly"},           // Wednesday
		"2027-04-30": {"daily", "weekly"},              // Friday, month end (no monthly tier)
		"2027-12-31": {"daily", "weekly", "quarterly"}, // Friday
	}
	for d, want := range cases {
		if got := s.BackupTiers(d); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", d, got, want)
		}
	}
	s.BackupYearEnd = "december"
	if got := s.BackupTiers("2027-12-31"); !reflect.DeepEqual(got, []string{"daily", "weekly", "quarterly", "yearly"}) {
		t.Errorf("calendar year end: %v", got)
	}
	if got := s.BackupTiers("2027-03-31"); contains(got, "yearly") {
		t.Errorf("March is not year end for a calendar year: %v", got)
	}
}

func backupOps(t *testing.T) (*Ops, context.Context, *vault.Vault) {
	o, ctx := setup(t)
	v := vault.New(vault.Ephemeral())
	o.Backups, o.Sealer, o.BackupSync = &backup.Dir{Path: t.TempDir()}, v, true
	return o, ctx, v
}

func TestBackupAfterCloseAndRestore(t *testing.T) {
	o, ctx, v := backupOps(t)
	at := time.Date(2026, 10, 16, 18, 0, 0, 0, ist)
	o.Now = func() time.Time { return at }
	if _, err := o.CloseDay(ctx, T, "farhan.khan", D, "test close"); err != nil {
		t.Fatal(err)
	}
	// Closing the whole day closes equity, then commodity: one backup after each.
	st, err := o.BackupsStatus(ctx, T, "vikram.shah")
	if err != nil || len(st.Backups) != 2 {
		t.Fatalf("status %+v %v", st, err)
	}
	b := st.Backups[0]
	if b.Date != D || b.Part != PartCommodity || !reflect.DeepEqual(b.Tiers, []string{"daily", "weekly"}) || b.Verified == "" || b.Trigger != "after the commodity close" || b.Total == 0 {
		t.Fatalf("backup %+v", b)
	}
	if e := st.Backups[1]; e.Part != PartEquity || e.Trigger != "after the equity close" || e.Total >= b.Total {
		t.Fatalf("equity backup %+v", e)
	}
	if st.LastGood == nil || st.Counts["weekly"] != 2 || len(st.Runs) != 2 || st.Runs[0].Status != "Succeeded" {
		t.Fatalf("status %+v", st)
	}
	for _, p := range st.Problems {
		t.Errorf("problem: %s", p)
	}
	if _, err := o.BackupsStatus(ctx, T, "priya.nair"); err == nil {
		t.Fatal("an Ops maker saw the backups")
	}

	// The backup holds the whole firm: restoring it into empty stores gives
	// back exactly what was there when it was taken.
	path, _ := o.Backups.PathOf(T, b.ID)
	f, _ := os.Open(path)
	defer f.Close()
	var recs []app.Record
	if _, err := backup.Read(f, v, func(r app.Record) error { recs = append(recs, r); return nil }); err != nil {
		t.Fatal(err)
	}
	fresh := app.MemoryStores()
	i := 0
	if _, err := fresh.Snap.Restore(ctx, T, func() (app.Record, bool, error) {
		if i == len(recs) {
			return app.Record{}, false, nil
		}
		i++
		return recs[i-1], true, nil
	}); err != nil {
		t.Fatal(err)
	}
	dump := func(s app.Snapshotter, upTo int) []string {
		var out []string
		_ = s.Snapshot(ctx, T, func(r app.Record) error {
			out = append(out, r.Table+" "+string(r.Row))
			return nil
		})
		if upTo > 0 && upTo < len(out) {
			out = out[:upTo]
		}
		sort.Strings(out)
		return out
	}
	got := dump(fresh.Snap, 0)
	if len(got) != b.Total {
		t.Fatalf("restored %d rows, backup has %d", len(got), b.Total)
	}
	want := map[string]bool{}
	for _, r := range recs {
		want[r.Table+" "+string(r.Row)] = true
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("restored row differs: %.200s", g)
		}
	}
	// Every kind of data is in it.
	tables := map[string]bool{}
	for _, r := range recs {
		tables[r.Table] = true
	}
	for _, tb := range []string{"raw_file", "trade_event", "journal_entry", "audit_log", "doc"} {
		if !tables[tb] {
			t.Errorf("no %s rows in the backup", tb)
		}
	}
	var doc struct {
		Body json.RawMessage `json:"body"`
	}
	for _, r := range recs {
		if r.Table == "doc" {
			json.Unmarshal(r.Row, &doc)
			if !strings.HasPrefix(string(doc.Body), `"llv1:`) {
				t.Fatalf("document stored in clear in the backup: %.80s", doc.Body)
			}
			break
		}
	}

	// A damaged file is caught when checked, and shows as a problem.
	data, _ := os.ReadFile(path)
	data[len(data)/2] ^= 0xff
	os.WriteFile(path, data, 0o600)
	if _, err := o.VerifyBackup(ctx, T, "vikram.shah", b.ID); err == nil {
		t.Fatal("damaged backup verified")
	}
	st, _ = o.BackupsStatus(ctx, T, "vikram.shah")
	if !strings.Contains(strings.Join(st.Problems, " "), "failed its check") {
		t.Fatalf("problems %v", st.Problems)
	}
}

func TestBackupFallbackRetryAndPrune(t *testing.T) {
	o, ctx, _ := backupOps(t)
	dir := o.Backups.Path
	// The day is not closed. At 23:00 nothing happens; at 23:30 it is
	// backed up anyway.
	at := time.Date(2026, 10, 16, 23, 0, 0, 0, ist)
	o.Now = func() time.Time { return at }
	_ = o.backupTick(ctx, T, at)
	if es, _ := o.Backups.List(T); len(es) != 0 {
		t.Fatalf("backed up before the fallback time: %d", len(es))
	}
	at = at.Add(30 * time.Minute)
	_ = o.backupTick(ctx, T, at)
	es, _ := o.Backups.List(T)
	if len(es) != 1 || es[0].Trigger != "end of day: the equity part was not closed" || es[0].Part != PartEquity {
		t.Fatalf("fallback %+v", es)
	}
	_ = o.backupTick(ctx, T, at.Add(time.Minute))
	if es, _ := o.Backups.List(T); len(es) != 1 {
		t.Fatalf("fallback ran twice: %d", len(es))
	}

	// The folder becomes unwritable: the close's backup fails, management is
	// told, and it is retried every 15 minutes, four times at most.
	o.Backups.Path = filepath.Join(dir, "missing", "\x00bad")
	at = at.Add(10 * time.Minute)
	if _, err := o.CloseDay(ctx, T, "farhan.khan", D, "test close"); err != nil {
		t.Fatal(err)
	}
	ob, _ := docs.List[OutboxItem](ctx, o.Docs, T, cOutbox, "")
	if !strings.Contains(outboxSubjects(ob), "Backup FAILED for 2026-10-16") {
		t.Fatalf("management not told: %s", outboxSubjects(ob))
	}
	for i := 0; i < 6; i++ {
		at = at.Add(16 * time.Minute)
		_ = o.backupTick(ctx, T, at)
	}
	o.Backups.Path = dir
	st, _ := o.BackupsStatus(ctx, T, "vikram.shah")
	failed := 0
	for _, r := range st.Runs {
		if r.Status == "Failed" {
			failed++
		}
	}
	if failed != 8 || !strings.Contains(strings.Join(st.Problems, " "), "failed four times") { // 4 for each part
		t.Fatalf("failed runs %d, problems %v", failed, st.Problems)
	}

	// Pruning: run many days of backups; each tier keeps its own count.
	_, err := o.SetBackupPolicy(ctx, T, "admin", backup.Keep{Daily: 7, Weekly: 4, Quarterly: 4, Yearly: 5, Manual: 1}, "march", "23:30", "00:30")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetBackupPolicy(ctx, T, "admin", backup.Keep{Daily: 2, Weekly: 4, Quarterly: 4, Yearly: 5, Manual: 1}, "march", "23:30", "00:30"); err == nil {
		t.Fatal("kept 2 dailies")
	}
	set, _ := o.Settings(ctx, T)
	d := "2026-10-19"
	for i := 0; i < 40; i++ {
		at = at.Add(24 * time.Hour)
		if _, err := o.RunBackup(ctx, T, SystemActor, d, PartEquity, "test", set.BackupTiers(d), true); err != nil {
			t.Fatal(err)
		}
		d = set.NextBusinessDay(d)
	}
	es, _ = o.Backups.List(T)
	n := map[string]int{}
	for _, e := range es {
		for _, tr := range e.Tiers {
			n[tr]++
		}
	}
	if n["daily"] < 7 || n["weekly"] < 4 || len(es) > 7+4+2 {
		t.Fatalf("after pruning: %d backups, %v", len(es), n)
	}
	if _, err := o.BackupNow(ctx, T, "farhan.khan"); err == nil {
		t.Fatal("a checker took a manual backup")
	}
	if r, err := o.BackupNow(ctx, T, "admin"); err != nil || !reflect.DeepEqual(r.Tiers, []string{"manual"}) {
		t.Fatalf("manual %+v %v", r, err)
	}
	if st, _ := o.BackupsStatus(ctx, T, "vikram.shah"); strings.Contains(strings.Join(st.Problems, " "), "failed four times") {
		t.Fatalf("a good manual backup did not clear the failures: %v", st.Problems)
	}
}

func outboxSubjects(ob []OutboxItem) string {
	var s []string
	for _, x := range ob {
		s = append(s, x.Subject)
	}
	return strings.Join(s, " | ")
}

func TestBackupsOff(t *testing.T) {
	o, ctx := setup(t)
	if _, err := o.CloseDay(ctx, T, "farhan.khan", D, "test close"); err != nil {
		t.Fatal(err)
	}
	st, err := o.BackupsStatus(ctx, T, "admin")
	if err != nil || st.Configured || len(st.Problems) == 0 || !strings.Contains(st.Problems[0], "LEDGERLINE_BACKUP_DIR") {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := o.BackupNow(ctx, T, "admin"); err == nil {
		t.Fatal("backup without a folder")
	}
}
