package ops

import (
	"testing"
	"time"

	"ledgerline/internal/control"
)

func TestProviderNoticesAndEdition(t *testing.T) {
	o, ctx := setup(t)
	now := o.now()
	n := &control.Notice{ID: "NTC-00001", Title: "Maintenance tonight", Text: "22:00 to 23:00", Level: "warn", Audience: "all", From: now.Add(-time.Hour).Format(time.RFC3339), Until: now.Add(time.Hour).Format(time.RFC3339)}
	if err := o.ApplyCommand(ctx, control.Command{ID: "CMD-1", Kind: "notice", Tenant: T, Notice: n}); err != nil {
		t.Fatal(err)
	}
	adm := &control.Notice{ID: "NTC-00002", Title: "Payment reminder", Audience: "admins", From: now.Add(-time.Hour).Format(time.RFC3339), Until: now.Add(time.Hour).Format(time.RFC3339)}
	if err := o.ApplyCommand(ctx, control.Command{ID: "CMD-2", Kind: "notice", Tenant: T, Notice: adm}); err != nil {
		t.Fatal(err)
	}
	if ns, _ := o.Notices(ctx, T, "priya.nair"); len(ns) != 1 || ns[0].ID != "NTC-00001" {
		t.Fatalf("maker sees %+v", ns)
	}
	if ns, _ := o.Notices(ctx, T, "admin"); len(ns) != 2 {
		t.Fatalf("admin sees %+v", ns)
	}
	if err := o.ApplyCommand(ctx, control.Command{ID: "CMD-3", Kind: "notice_withdraw", Tenant: T, Ref: "NTC-00001"}); err != nil {
		t.Fatal(err)
	}
	if ns, _ := o.Notices(ctx, T, "priya.nair"); len(ns) != 0 {
		t.Fatalf("withdrawn notice still shown %+v", ns)
	}
	if err := o.ApplyCommand(ctx, control.Command{ID: "CMD-4", Kind: "edition", Tenant: T, Edition: "gold"}); err == nil {
		t.Fatal("bad edition accepted")
	}
	if err := o.ApplyCommand(ctx, control.Command{ID: "CMD-5", Kind: "edition", Tenant: T, Edition: EditionManual}); err != nil {
		t.Fatal(err)
	}
	if ed, _ := o.Edition(ctx, T); ed != EditionManual {
		t.Fatalf("edition %s", ed)
	}
	// After a restart the configured edition is replaced by Control's.
	o.EditionOf = map[string]string{T: EditionFull}
	o.LoadControlEditions(ctx, []string{T})
	if ed, _ := o.Edition(ctx, T); ed != EditionManual {
		t.Fatalf("after restart %s", ed)
	}
	r, err := o.FirmReport(ctx, T)
	if err != nil || len(r.Setup) < 5 {
		t.Fatalf("setup %+v %v", r.Setup, err)
	}
	for _, s := range r.Setup {
		if s.Key == "mail" && s.OK {
			t.Fatal("mail shown as set up")
		}
	}
}
