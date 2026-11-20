package api

import (
	"bytes"
	"compress/gzip"
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

// TestSecurityAPI: the provider declares a platform incident and both
// firms see it in their register; a firm records the CERT-In report, sees
// its events and downloads its logs; staff outside compliance cannot.
func TestSecurityAPI(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	clock := time.Date(2026, 11, 20, 11, 0, 0, 0, time.FixedZone("IST", 19800))
	o.Now, a.Now = func() time.Time { return clock }, func() time.Time { return clock }
	for _, tn := range []string{"demo", "beta"} {
		if err := o.SeedDev(t.Context(), tn); err != nil {
			t.Fatal(err)
		}
	}
	const key2 = "test-key-beta-0123456789"
	s := New(a, o, map[string]string{key: "demo", key2: "beta"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	s.ProviderKey = "provider-key-0123456789abcdef"
	s.securityRoutes() // registering twice is harmless
	srv := httptest.NewServer(s)
	defer srv.Close()
	call := func(method, path, tok, who, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		if who != "" {
			req.Header.Set("X-Actor", who)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	decl := `{"title":"Console unavailable","description":"The console did not answer from 10:40 to 10:55 IST.","severity":"Medium","kind":"Outage"}`
	if c, _ := call("POST", "/provider/incidents", key, "", decl); c != 401 {
		t.Fatalf("firm key declared: %d", c)
	}
	if c, b := call("POST", "/provider/incidents", s.ProviderKey, "ops-lead", decl); c != 200 || !strings.Contains(b, `"PINC-000001"`) || !strings.Contains(b, `"beta"`) {
		t.Fatalf("declare: %d %s", c, b)
	}
	for _, k := range []string{key, key2} {
		if c, b := call("GET", "/v1/security/incidents", k, "vikram.shah", ""); c != 200 || !strings.Contains(b, "Console unavailable") || !strings.Contains(b, "20 Nov 2026 17:00 IST") {
			t.Fatalf("register: %d %s", c, b)
		}
	}
	if c, _ := call("GET", "/v1/security/incidents", key, "priya.nair", ""); c != 403 {
		t.Fatalf("maker saw the register: %d", c)
	}
	if c, b := call("POST", "/v1/security/incidents/PINC-000001/report", key, "vikram.shah", `{"to":"CERT-In","how":"Email","reference":"CERTIn-TEST-42"}`); c != 200 || !strings.Contains(b, `"status": "Reported"`) {
		t.Fatalf("report: %d %s", c, b)
	}
	if c, b := call("PUT", "/v1/security/settings", key, "vikram.shah", `{"retention_days":30}`); c != 422 && c != 400 {
		t.Fatalf("30 days: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/security/events?from=2026-11-20&to=2026-11-20", key, "vikram.shah", ""); c != 200 || !strings.Contains(b, "incident.provider_notice") {
		t.Fatalf("events: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/security/log-export", key, "vikram.shah", ""); c != 200 || !strings.Contains(b, `"Not set"`) {
		t.Fatalf("log export: %d %s", c, b)
	}
	c, b := call("GET", "/v1/security/logs/download?from=2026-11-20&to=2026-11-20", key, "vikram.shah", "")
	if c != 200 {
		t.Fatalf("download: %d %s", c, b)
	}
	zr, err := gzip.NewReader(bytes.NewReader([]byte(b)))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	if !strings.Contains(string(raw), "PINC-000001") {
		t.Fatalf("download content: %s", raw)
	}
	if c, b := call("GET", "/provider/incidents", s.ProviderKey, "", ""); c != 200 || !strings.Contains(b, "Console unavailable") {
		t.Fatalf("provider list: %d %s", c, b)
	}
	if c, b := call("POST", "/provider/incidents/PINC-000001", s.ProviderKey, "ops-lead", `{"status":"Closed","note":"Service restored; cause was a failed disk."}`); c != 200 || !strings.Contains(b, `"status": "Closed"`) {
		t.Fatalf("provider update: %d %s", c, b)
	}
}
