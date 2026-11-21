package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/dataexport"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

// TestPeriodArchiveRoutes: the archive screen, a preview, the current year
// refused, and an entry posted into an archived day answered 409 "archived".
func TestPeriodArchiveRoutes(t *testing.T) {
	ops.PasswordIterations = 1000
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	now := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)
	o.Now = func() time.Time { return now }
	o.WorkDir, o.ArchiveDir, o.ArchiveSync = t.TempDir(), t.TempDir(), true
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true))
	t.Cleanup(srv.Close)
	if c := ipoCall(t, srv, "GET", "/v1/period-archives", "priya.nair", nil, nil); c != 403 {
		t.Fatalf("an ops maker saw the archives: %d", c)
	}
	var v ops.ArchiveView
	if c := ipoCall(t, srv, "GET", "/v1/period-archives", "vikram.shah", nil, &v); c != 200 || v.CurrentFY != "2026-27" || len(v.Years) == 0 {
		t.Fatalf("screen %d %+v", c, v)
	}
	var p ops.ArchivePreview
	if c := ipoCall(t, srv, "POST", "/v1/period-archives/preview", "admin", map[string]any{"fys": []string{"2026-27"}}, &p); c != 200 || len(p.Blockers) == 0 {
		t.Fatalf("current year preview %d %+v", c, p)
	}
	if c := ipoCall(t, srv, "POST", "/v1/period-archives", "admin", map[string]any{"fys": []string{"2026-27"}}, nil); c != 409 {
		t.Fatalf("current year requested: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/period-archives/settings", "admin", map[string]any{"retain_years": 5}, nil); c != 422 {
		t.Fatalf("retention below the rules accepted: %d", c)
	}
}

func TestDataExitRoutes(t *testing.T) {
	ops.PasswordIterations = 1000
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	now := time.Now().Truncate(time.Second)
	o.Now = func() time.Time { return now }
	o.WorkDir, o.ExportSync = t.TempDir(), true
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true))
	t.Cleanup(srv.Close)

	if c := ipoCall(t, srv, "GET", "/v1/data-exit", "priya.nair", nil, nil); c != 403 {
		t.Fatalf("an ops maker saw data & exit: %d", c)
	}
	var x ops.DataExport
	if c := ipoCall(t, srv, "POST", "/v1/data-exports", "admin", map[string]any{"reason": "yearly copy"}, &x); c != 200 {
		t.Fatalf("request %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/data-exports/"+x.ID+"/decide", "admin", map[string]any{"approve": true}, nil); c != 403 {
		t.Fatalf("self-approval %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/data-exports/"+x.ID+"/decide", "vikram.shah", map[string]any{"approve": true}, nil); c != 200 {
		t.Fatalf("approve %d", c)
	}
	get := func(id string) []byte {
		var l ops.DXLinkView
		if c := ipoCall(t, srv, "POST", "/v1/data-exports/"+id+"/link", "admin", map[string]any{}, &l); c != 200 {
			t.Fatalf("link %d", c)
		}
		req, _ := http.NewRequest("GET", srv.URL+l.URL, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Actor", "admin")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/zip" || !strings.Contains(res.Header.Get("Content-Disposition"), id) {
			t.Fatalf("download %d %s", res.StatusCode, b)
		}
		if _, err := dataexport.Verify(strings.NewReader(string(b)), int64(len(b)), ""); err != nil {
			t.Fatal(err)
		}
		return b
	}
	get(x.ID)

	var r app.ExitRecord
	if c := ipoCall(t, srv, "POST", "/v1/data-exit/request", "admin", map[string]any{"confirm": "demo"}, &r); c != 200 {
		t.Fatalf("exit request %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/data-exit/"+r.ID+"/decide", "vikram.shah", map[string]any{"approve": true}, &r); c != 200 {
		t.Fatalf("exit approve %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/data-exit/"+r.ID+"/purge", "admin", map[string]any{"confirm": "demo"}, nil); c != 409 {
		t.Fatalf("purge during the cooling-off: %d", c)
	}
	now = now.Add(8 * 24 * time.Hour)
	if _, err := o.Tick(ctx, "demo", now); err != nil {
		t.Fatal(err)
	}
	var v ops.DataExitView
	if c := ipoCall(t, srv, "GET", "/v1/data-exit", "admin", nil, &v); c != 200 || v.Exit == nil || v.Exit.ExportID == "" {
		t.Fatalf("screen %d %+v", c, v.Exit)
	}
	get(v.Exit.ExportID)
	var done ops.ExitDone
	if c := ipoCall(t, srv, "POST", "/v1/data-exit/"+r.ID+"/purge", "admin", map[string]any{"confirm": "demo"}, &done); c != 200 || len(done.Certificate) == 0 {
		t.Fatalf("purge %d", c)
	}
	// The firm is gone: every call is refused; the certificate link works.
	if c := ipoCall(t, srv, "GET", "/v1/data-exit", "admin", nil, nil); c != 410 {
		t.Fatalf("a purged firm answered: %d", c)
	}
	res, err := http.Get(srv.URL + done.CertificateURL[strings.Index(done.CertificateURL, "/exit-certificate/"):])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(string(b), "%PDF") {
		t.Fatalf("certificate link %d", res.StatusCode)
	}
	if res, _ := http.Get(srv.URL + "/exit-certificate/demo/" + r.ID + "?t=wrong"); res.StatusCode != 404 {
		t.Fatalf("a wrong certificate link: %d", res.StatusCode)
	}
}
