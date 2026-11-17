package api

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/sample"
	"ledgerline/internal/vault"
)

// The self-inspection's routes, with made-up data (the sample firm).
func TestSelfInspectionRoutes(t *testing.T) {
	ops.PasswordIterations = 1000
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.LoadSampleDay(ctx, "demo", "priya.nair", sample.Generate("2026-10-16"), false, func(ctx context.Context, tenant, actor, source, name string, data []byte) error {
		_, err := a.IngestFile(ctx, tenant, actor, source, "", name, data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 10, 16, 23, 0, 0, 0, time.FixedZone("IST", 19800)) }
	o.Now, a.Now = now, now
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	s.selfInspectRoutes()
	s.selfInspectRoutes() // wiring it twice is harmless
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	if c := ipoCall(t, srv, "GET", "/v1/self-inspection", "meera.iyer", nil, nil); c != 403 {
		t.Fatalf("branch staff opened the self-inspection: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/self-inspection/run", "priya.nair", map[string]string{}, nil); c != 403 {
		t.Fatalf("a maker ran it: %d", c)
	}
	var run ops.SIRun
	if c := ipoCall(t, srv, "POST", "/v1/self-inspection/run", "vikram.shah", map[string]string{"date": "2026-10-16"}, &run); c != 200 || run.ID == "" || run.Failed == 0 {
		t.Fatalf("run %d %+v", c, run.ID)
	}
	var v ops.SIView
	if c := ipoCall(t, srv, "GET", "/v1/self-inspection", "priya.nair", nil, &v); c != 200 || v.Last == nil || v.Last.ID != run.ID || len(v.Tests) == 0 || len(v.Trend) != 1 || v.Can["run"] {
		t.Fatalf("view %d %+v", c, v.Can)
	}
	var got ops.SIRun
	if c := ipoCall(t, srv, "GET", "/v1/self-inspection/runs/"+run.ID, "priya.nair", nil, &got); c != 200 || len(got.Results) != len(run.Results) {
		t.Fatalf("past run %d", c)
	}
	if c := ipoCall(t, srv, "GET", "/v1/self-inspection/runs/SIR-9999999", "priya.nair", nil, nil); c != 404 {
		t.Fatalf("unknown run %d", c)
	}
	var d ops.SITestDetail
	if c := ipoCall(t, srv, "GET", "/v1/self-inspection/tests/kyc_complete", "anita.rao", nil, &d); c != 200 || len(d.Results) != 1 || len(d.Exceptions) != 1 {
		t.Fatalf("test %d %+v", c, d.Results)
	}
	exc := d.Exceptions[0].ID
	if c := ipoCall(t, srv, "POST", "/v1/self-inspection/exceptions/"+exc+"/status", "anita.rao", map[string]string{"status": "fixing"}, nil); c != 403 {
		t.Fatalf("a risk manager worked a compliance exception: %d", c)
	}
	var x ops.SIException
	if c := ipoCall(t, srv, "POST", "/v1/self-inspection/exceptions/"+exc+"/status", "vikram.shah", map[string]string{"status": "fixing"}, &x); c != 200 || x.Status != ops.SIExcFixing {
		t.Fatalf("being fixed %d %+v", c, x.Status)
	}
	acc := map[string]string{"reason": "Made-up reason: KRA re-validation drive", "until": "2026-12-31"}
	if c := ipoCall(t, srv, "POST", "/v1/self-inspection/exceptions/"+exc+"/accept", "admin", acc, nil); c != 403 {
		t.Fatalf("an admin accepted a risk: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/self-inspection/exceptions/"+exc+"/accept", "vikram.shah", map[string]string{"reason": "", "until": "2026-12-31"}, nil); c != 422 {
		t.Fatalf("accepted with no reason: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/self-inspection/exceptions/"+exc+"/accept", "vikram.shah", acc, &x); c != 200 || x.Status != ops.SIExcAccepted {
		t.Fatalf("accepted %d %+v", c, x.Status)
	}
	mute := map[string]string{"reason": "Made-up reason: flagging batch this week", "until": "2026-10-23"}
	if c := ipoCall(t, srv, "POST", "/v1/self-inspection/tests/inactive_accounts/mute", "farhan.khan", mute, nil); c != 403 {
		t.Fatalf("a checker muted a test: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/self-inspection/tests/inactive_accounts/mute", "vikram.shah", mute, nil); c != 200 {
		t.Fatalf("mute %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/self-inspection/tests/inactive_accounts/unmute", "vikram.shah", map[string]string{"reason": "done"}, nil); c != 200 {
		t.Fatalf("unmute %d", c)
	}
	var st ops.SISettings
	if c := ipoCall(t, srv, "PUT", "/v1/self-inspection/settings", "priya.nair", map[string]any{"at": "23:15"}, nil); c != 403 {
		t.Fatalf("a maker changed the schedule: %d", c)
	}
	if c := ipoCall(t, srv, "PUT", "/v1/self-inspection/settings", "vikram.shah", map[string]any{"at": "23:15", "off": false}, &st); c != 200 || st.At != "23:15" {
		t.Fatalf("schedule %d %+v", c, st)
	}
}
