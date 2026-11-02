package ops

import (
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

func openFileWork(t *testing.T, o *Ops) map[string]WorkItem {
	t.Helper()
	ws, err := docs.List[WorkItem](t.Context(), o.Docs, T, cWorkItem, "")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]WorkItem{}
	for _, w := range ws {
		if w.Process == wFileMissing && w.Status == "Open" {
			out[strings.TrimPrefix(w.Key, wFileMissing+":")] = w
		}
	}
	return out
}

func digests(t *testing.T, o *Ops) []OutboxItem {
	t.Helper()
	xs, _ := docs.List[OutboxItem](t.Context(), o.Docs, T, cOutbox, "")
	var out []OutboxItem
	for _, x := range xs {
		if strings.HasPrefix(x.Ref, "xfile-missing:") {
			out = append(out, x)
		}
	}
	return out
}

func TestMissingFileAlerts(t *testing.T) {
	o, ctx := newOps(t)
	if err := saveClearing(t, o, nil, nil); err != nil { // NCL clears everything: no ICCL files
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 16, 17, 20, 0, 0, ist) // Friday, a business day
	o.Now = func() time.Time { return clock }

	run, err := o.RunFileAlerts(ctx, T, SystemActor, clock)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, it := range run.Alerted {
		got[it.Date+"|"+it.ID] = true
		if it.Party == "ICCL" {
			t.Fatalf("ICCL file chased though ICCL is not chosen: %s", it.ID)
		}
		if it.Intraday || it.Format == "" {
			t.Fatalf("intraday or by-hand file chased: %s", it.ID)
		}
	}
	// BSE bhavcopy 16:30 + 30 minutes is past; the BSE final trade file
	// (17:00) is still within its grace period.
	if !got[D+"|bse-bhav-cm"] || got[D+"|bse-trade-f"] || got[D+"|bse-trade-p"] || got[D+"|bse-stamp"] || !got["2026-10-15|bse-trade-f"] || !got["2026-10-15|bse-stamp"] {
		t.Fatalf("alerted %v", got)
	}
	// The stamp duty file of the 15th is due on the 16th at 15:00.
	work := openFileWork(t, o)
	if _, ok := work["2026-10-15|bse-stamp"]; !ok {
		t.Fatalf("no work item for the 15th's stamp duty file: %v", work)
	}
	w := work["2026-10-16|bse-bhav-cm"]
	if w.Role != RoleMaker || w.View != "xfiles" || !strings.Contains(w.Detail, "BSE") {
		t.Fatalf("work item %+v", w)
	}
	if w := work["2026-10-16|ncl-var"]; w.Role != RoleRisk {
		t.Fatalf("clearing file role %+v", w)
	}
	ds := digests(t, o)
	set, _ := o.Settings(ctx, T)
	if len(ds) != len(set.ManagementEmails) || len(ds) == 0 || !strings.Contains(ds[0].Subject, "expected files missing") {
		t.Fatalf("one digest per recipient: %+v", ds)
	}

	// The checklist shows when each was chased.
	cl, _ := o.DailyFileChecklist(ctx, T, D)
	for _, it := range cl.Items {
		if it.ID == "bse-bhav-cm" && it.AlertedAt != "2026-10-16 17:20" {
			t.Fatalf("alerted at %q", it.AlertedAt)
		}
		if it.ID == "bse-trade-f" && it.AlertedAt != "" {
			t.Fatalf("trade file alerted at %q", it.AlertedAt)
		}
	}

	// A later run alerts only what became late since, in one more digest.
	clock = time.Date(2026, 10, 16, 17, 31, 0, 0, ist)
	run, _ = o.RunFileAlerts(ctx, T, SystemActor, clock)
	if len(run.Alerted) == 0 {
		t.Fatal("trade file not alerted after its grace period")
	}
	for _, it := range run.Alerted {
		if it.ID == "bse-bhav-cm" || it.Date != D {
			t.Fatal("alerted twice")
		}
	}
	if n := len(digests(t, o)); n != 2*len(set.ManagementEmails) {
		t.Fatalf("%d digests", n)
	}
	run, _ = o.RunFileAlerts(ctx, T, SystemActor, clock)
	if len(run.Alerted) != 0 || len(digests(t, o)) != 2*len(set.ManagementEmails) {
		t.Fatalf("a run with nothing new sent mail: %+v", run)
	}

	// The file arriving closes its work item.
	o.noteDailyFile(ctx, T, "priya.nair", "udiff-bhavcopy", "BhavCopy_BSE_CM_0_0_0_20261016_F_0000.CSV", D)
	if _, open := openFileWork(t, o)["2026-10-16|bse-bhav-cm"]; open {
		t.Fatal("work item still open after the file came")
	}
	if _, open := openFileWork(t, o)["2026-10-16|bse-trade-f"]; !open {
		t.Fatal("unrelated item closed")
	}

	// Weekends and holidays are never alerted.
	sat := time.Date(2026, 10, 17, 23, 0, 0, 0, ist)
	if run, _ := o.RunFileAlerts(ctx, T, SystemActor, sat); run.Skipped == "" || len(run.Alerted) != 0 {
		t.Fatalf("Saturday: %+v", run)
	}
	set.Holidays = append(set.Holidays, "2026-10-21")
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	hol := time.Date(2026, 10, 21, 23, 0, 0, 0, ist)
	o.Now = func() time.Time { return hol }
	if run, _ := o.RunFileAlerts(ctx, T, SystemActor, hol); run.Skipped != "not a business day" || len(run.Alerted) != 0 {
		t.Fatalf("holiday: %+v", run)
	}

	// Settings: off, grace and recipients.
	o.Now = func() time.Time { return clock }
	if _, err := o.SaveFileAlertSettings(ctx, T, "priya.nair", FileAlertSettings{Off: true}); err == nil {
		t.Fatal("maker changed the alert rules")
	}
	if _, err := o.SaveFileAlertSettings(ctx, T, "vikram.shah", FileAlertSettings{To: []string{"not an email"}}); err == nil {
		t.Fatal("bad recipient accepted")
	}
	fs, err := o.SaveFileAlertSettings(ctx, T, "vikram.shah", FileAlertSettings{GraceMinutes: 90, To: []string{"compliance", "ops-desk@firm.example"}})
	if err != nil || fs.GraceMinutes != 90 {
		t.Fatalf("%+v %v", fs, err)
	}
	if got := fileAlertRecipients(set, o.FileAlertSettings(ctx, T)); strings.Join(got, ",") != set.ComplianceEmail+",ops-desk@firm.example" {
		t.Fatalf("recipients %v", got)
	}
	if _, err := o.SaveFileAlertSettings(ctx, T, "vikram.shah", FileAlertSettings{Off: true}); err != nil {
		t.Fatal(err)
	}
	mon := time.Date(2026, 10, 19, 23, 0, 0, 0, ist)
	if run, _ := o.RunFileAlerts(ctx, T, SystemActor, mon); run.Skipped == "" || len(run.Alerted) != 0 {
		t.Fatalf("switched off: %+v", run)
	}
	if d := DefaultFileAlertSettings(); d.Off || d.GraceMinutes != 30 {
		t.Fatalf("defaults %+v", d)
	}
}

func TestMissingFilesFollowClearingChoice(t *testing.T) {
	o, ctx := newOps(t)
	all := map[string]string{"NSE-COM": ClearNone}
	for _, k := range []string{"NSE-CM", "NSE-FO", "NSE-CD", "BSE-CM", "BSE-FO", "BSE-CD"} {
		all[k] = "ICCL"
	}
	if err := saveClearing(t, o, all, nil); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 16, 23, 50, 0, 0, ist)
	o.Now = func() time.Time { return clock }
	run, err := o.RunFileAlerts(ctx, T, SystemActor, clock)
	if err != nil || len(run.Alerted) == 0 {
		t.Fatalf("%+v %v", run, err)
	}
	for _, it := range run.Alerted {
		if it.Party == "NCL" || it.Party == "ICCL" { // NCL not chosen; ICCL files are checked by hand
			t.Fatalf("chased %s (%s)", it.ID, it.Party)
		}
	}
}

// Files checked by hand say what Ledgerline needs to read them.
func TestHandFilesSayWhatIsNeeded(t *testing.T) {
	for _, f := range DailyFiles {
		if f.Format == "" && !strings.Contains(f.HandNote, "needs") {
			t.Errorf("%s is checked by hand with no note of what is needed", f.ID)
		}
		if f.Format != "" && f.HandNote != "" {
			t.Errorf("%s has a layout and a hand note", f.ID)
		}
	}
}
