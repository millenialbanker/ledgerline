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
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

func TestStepsAPI(t *testing.T) {
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
	at := time.Date(2026, 10, 16, 11, 0, 0, 0, time.FixedZone("IST", 19800))
	o.Now = func() time.Time { return at }
	srv := httptest.NewServer(New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false))
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
	settings := `{"custom":[{"name":"Petty cash count","due":"10:00","role":"Ops maker"}],"steps":{"autopilot:billing":{"due":"19:45"}}}`
	if c, b := call("PUT", "/v1/steps/settings", "priya.nair", settings); c != 403 {
		t.Fatalf("maker changed step settings: %d %s", c, b)
	}
	if c, b := call("PUT", "/v1/steps/settings", "admin", settings); c != 200 || !strings.Contains(b, "custom:petty-cash-count") {
		t.Fatalf("save: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/steps/settings", "priya.nair", ""); c != 200 || !strings.Contains(b, `"eff_due": "19:45"`) {
		t.Fatalf("settings: %d %.300s", c, b)
	}
	if c, b := call("POST", "/v1/steps/check", "priya.nair", `{}`); c != 403 {
		t.Fatalf("maker ran the check: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/steps/check", "farhan.khan", `{}`); c != 200 || !strings.Contains(b, "custom:petty-cash-count|2026-10-16") {
		t.Fatalf("check: %d %.300s", c, b)
	}
	if c, b := call("GET", "/v1/steps?date=2026-10-16", "priya.nair", ""); c != 200 || !strings.Contains(b, `"state": "reminded"`) {
		t.Fatalf("steps: %d %.300s", c, b)
	}
	id := `"custom:petty-cash-count|2026-10-16"`
	if c, b := call("POST", "/v1/steps/skip", "priya.nair", `{"id":`+id+`,"reason":""}`); c != 422 {
		t.Fatalf("skipped without a reason: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/steps/done", "priya.nair", `{"id":`+id+`,"note":"Counted at 10:20; register signed"}`); c != 200 || !strings.Contains(b, `"status": "Done by hand"`) {
		t.Fatalf("done: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/steps/confirm", "admin", `{"id":`+id+`,"fingerprint":"x"}`); c == 200 {
		t.Fatalf("confirmed a closed step: %d %s", c, b)
	}
}
