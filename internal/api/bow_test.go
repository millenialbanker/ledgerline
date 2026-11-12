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

func TestBOWAPI(t *testing.T) {
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
	s.bowRoutes()
	s.bowRoutes() // registering twice is harmless
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
	if c, b := call("GET", "/v1/bow", "anita.rao", ""); c != 409 || !strings.Contains(b, "Connections") {
		t.Fatalf("not set up: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/bow/push", "priya.nair", `{}`); c != 403 {
		t.Fatalf("maker push: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/bow/sign-in", "anita.rao", `{"otp":""}`); c != 409 {
		t.Fatalf("sign in without a connection: %d %s", c, b)
	}
	// The BOW upload files need no API connection.
	get := func(path, who string) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Actor", who)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Disposition"), string(b)
	}
	if c, cd, b := get("/v1/frontoffice/bow-funds-file?date=2026-10-16", "anita.rao"); c != 200 || !strings.Contains(cd, `filename="BOW_FUND_16-10-26 `) || (b != "" && !strings.HasSuffix(b, ",1,1,0,1,Y\r\n")) {
		t.Fatalf("funds file: %d %s %q", c, cd, b)
	}
	if c, _, b := get("/v1/frontoffice/bow-funds-file?date=2026-10-16&changed=1", "anita.rao"); c != 200 || b != "" {
		t.Fatalf("changed funds right after the full file: %d %q", c, b)
	}
	if c, _, _ := get("/v1/frontoffice/bow-funds-file?date=2026-10-16", "vikram.shah"); c != 403 {
		t.Fatalf("compliance funds file: %d", c)
	}
	if c, cd, _ := get("/v1/frontoffice/bow-holdings?date=2026-10-16", "priya.nair"); c != 200 || !strings.Contains(cd, `filename="BOW_DP_Holding_Combined_Upload_16102026.TXT"`) {
		t.Fatalf("holdings file: %d %s", c, cd)
	}
	if c, _, _ := get("/v1/frontoffice/bow-nothing?date=2026-10-16", "anita.rao"); c != 404 {
		t.Fatalf("unknown BOW file: %d", c)
	}
}
