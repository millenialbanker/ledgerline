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

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

func TestFinspotAPIRoutes(t *testing.T) {
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
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	s.finspotAPIRoutes()
	s.finspotAPIRoutes() // registering twice is harmless
	srv := httptest.NewServer(s)
	defer srv.Close()
	call := func(method, path, who, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Actor", who)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if c, b := call("GET", "/v1/finspot-api", "anita.rao", ""); c != 409 || !strings.Contains(b, "Connections") {
		t.Fatalf("not set up: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/finspot-api/push", "priya.nair", `{}`); c != 403 {
		t.Fatalf("maker push: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/finspot-api/push", "anita.rao", `{"parts":["limits"]}`); c != 409 {
		t.Fatalf("push without a connection: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/finspot-api/check/NOSUCH", "priya.nair", `{}`); c != 404 && c != 403 {
		t.Fatalf("maker check: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/finspot-api/runs/2026-10-29-0001", "anita.rao", ""); c != 404 {
		t.Fatalf("no such run: %d %s", c, b)
	}
}
