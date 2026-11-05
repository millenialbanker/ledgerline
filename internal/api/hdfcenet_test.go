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

func TestHDFCENetRoutes(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	if err := o.SeedDev(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
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
	if c, b := call("GET", "/v1/payouts/hdfc-seq?date=2026-11-04", "priya.nair", ""); c != 200 || !strings.Contains(b, `"main"`) || !strings.Contains(b, `"second"`) {
		t.Fatalf("seq: %d %s", c, b)
	}
	if c, b := call("PUT", "/v1/payouts/hdfc-seq", "priya.nair", `{"series":"main","date":"2026-11-04","next":817}`); c != 403 {
		t.Fatalf("maker set seq: %d %s", c, b)
	}
	if c, b := call("PUT", "/v1/payouts/hdfc-seq", "admin", `{"series":"main","date":"2026-11-04","next":817}`); c != 200 || !strings.Contains(b, `"next": 817`) {
		t.Fatalf("admin set seq: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/payouts/batches/PB-999999/hdfc", "priya.nair", `{}`); c != 404 {
		t.Fatalf("unknown batch: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/payouts/hdfc-files/PB-999999-01/cancel", "farhan.khan", `{"reason":"x"}`); c != 404 {
		t.Fatalf("unknown file: %d %s", c, b)
	}
}
