package api

import (
	"context"
	"encoding/json"
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

func TestExchangeAlertRoutes(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 21, 0, 0, 0, time.FixedZone("IST", 19800)) }
	if err := o.SeedDev(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	s.exchangeAlertRoutes()
	s.exchangeAlertRoutes() // registering twice is harmless
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

	// Missing-file alerts.
	c, b := call("GET", "/v1/exchange-files-alerts", "priya.nair", "")
	var fs ops.FileAlertSettings
	if c != 200 || json.Unmarshal([]byte(b), &fs) != nil || fs.Off || fs.GraceMinutes != 30 {
		t.Fatalf("alert settings: %d %s", c, b)
	}
	if c, b := call("PUT", "/v1/exchange-files-alerts", "priya.nair", `{"off":false,"grace_minutes":45,"to":[]}`); c != 403 {
		t.Fatalf("maker saved: %d %s", c, b)
	}
	if c, b := call("PUT", "/v1/exchange-files-alerts", "vikram.shah", `{"off":false,"grace_minutes":45,"to":["compliance"]}`); c != 200 || !strings.Contains(b, `"grace_minutes": 45`) {
		t.Fatalf("save: %d %s", c, b)
	}
	c, b = call("POST", "/v1/exchange-files-alerts/run", "priya.nair", "")
	var run ops.FileAlertRun
	if c != 200 || json.Unmarshal([]byte(b), &run) != nil || len(run.Alerted) == 0 || len(run.Mailed) != 1 {
		t.Fatalf("run: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/exchange-files-checklist?date=2026-10-16", "priya.nair", ""); c != 200 || !strings.Contains(b, `"alerted_at": "2026-10-16 21:00"`) {
		t.Fatalf("checklist: %d %.300s", c, b)
	}

	// BSE not-permitted reasons.
	if c, b := call("GET", "/v1/bse-ucc-blocked", "priya.nair", ""); c != 200 || !strings.Contains(b, `"clients": []`) {
		t.Fatalf("blocked list: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/bse-ucc-blocked/NOSUCH1", "vikram.shah", ""); c != 200 || !strings.Contains(b, `"blocked": false`) {
		t.Fatalf("blocked one: %d %s", c, b)
	}
	if c, _ := call("GET", "/v1/bse-ucc-blocked", "meera.iyer", ""); c != 403 {
		t.Fatalf("branch staff: %d", c)
	}

	// Order-to-trade ratios.
	if c, b := call("GET", "/v1/otr?from=2026-10-01&to=2026-10-16", "vikram.shah", ""); c != 200 || !strings.Contains(b, `"alert_from": 50`) {
		t.Fatalf("otr: %d %s", c, b)
	}
	if c, _ := call("GET", "/v1/otr", "priya.nair", ""); c != 403 {
		t.Fatalf("maker saw otr: %d", c)
	}
	if c, b := call("PUT", "/v1/otr/settings", "vikram.shah", `{"off":false,"alert_from":100,"cool_off_at":500,"count":"all","slabs":[{"from":50,"paise":2},{"from":250,"paise":10}]}`); c != 200 || !strings.Contains(b, `"alert_from": 100`) {
		t.Fatalf("otr settings: %d %s", c, b)
	}
	if c, b := call("PUT", "/v1/otr/settings", "vikram.shah", `{"count":"some"}`); c != 422 {
		t.Fatalf("bad count: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/otr/settings", "anita.rao", ""); c != 200 || !strings.Contains(b, `"alert_from": 100`) {
		t.Fatalf("otr settings get: %d %s", c, b)
	}
}
