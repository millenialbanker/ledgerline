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
	"ledgerline/internal/vault"
)

// TestFirmModuleRoutes: everyone reads Settings → Firm modules; the
// compliance officer changes it, after a preview; /v1/me lists the screens
// it hides.
func TestFirmModuleRoutes(t *testing.T) {
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
	o.Now = func() time.Time { return time.Date(2026, 10, 17, 10, 0, 0, 0, time.FixedZone("IST", 19800)) }
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	var v ops.FirmModulesView
	if c := ipoCall(t, srv, "GET", "/v1/firm-modules", "priya.nair", nil, &v); c != 200 || v.CanEdit || len(v.Segments) == 0 {
		t.Fatalf("read by ops maker: %d %+v", c, v.CanEdit)
	}
	body := map[string]any{"cdsl": "no", "nsdl": "no", "mtf": false}
	if c := ipoCall(t, srv, "PUT", "/v1/firm-modules", "priya.nair", body, nil); c != 403 {
		t.Fatalf("ops maker changed it: %d", c)
	}
	var d ops.ModulesDiff
	if c := ipoCall(t, srv, "POST", "/v1/firm-modules/preview", "vikram.shah", body, &d); c != 200 || len(d.Screens) == 0 {
		t.Fatalf("preview: %d %+v", c, d)
	}
	if c := ipoCall(t, srv, "PUT", "/v1/firm-modules", "vikram.shah", map[string]any{"switches": map[string]string{"dma": "perhaps"}}, nil); c != 422 {
		t.Fatalf("a bad answer: %d", c)
	}
	if c := ipoCall(t, srv, "PUT", "/v1/firm-modules", "vikram.shah", body, &v); c != 200 || !v.CanEdit {
		t.Fatalf("save: %d", c)
	}
	var me struct {
		Hidden map[string]string `json:"hidden_screens"`
	}
	if c := ipoCall(t, srv, "GET", "/v1/me", "priya.nair", nil, &me); c != 200 || me.Hidden["dp"] == "" || me.Hidden["mtf"] == "" {
		t.Fatalf("hidden screens: %d %v", c, me.Hidden)
	}
	if c := ipoCall(t, srv, "POST", "/v1/firm-modules/recount", "vikram.shah", nil, &v); c != 200 || v.CountedAt == "" {
		t.Fatalf("recount: %d %q", c, v.CountedAt)
	}
}
