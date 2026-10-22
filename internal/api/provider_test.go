package api

import (
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

func TestProviderEndpoints(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	if err := o.SeedDev(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	srv := httptest.NewServer(s)
	defer srv.Close()
	call := func(method, path, tok, body string) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	credit := `{"tenant":"demo","amount":"5000","ref":"INV-2026-001"}`
	// Disabled until a provider key is set; a firm's API key never works.
	if c, _ := call("POST", "/provider/ai/credit", key, credit); c != 401 {
		t.Fatalf("no provider key: %d", c)
	}
	s.ProviderKey = "provider-key-0123456789abcdef"
	for _, tok := range []string{"", key, "provider-key-0123456789abcdeX"} {
		if c, _ := call("POST", "/provider/ai/credit", tok, credit); c != 401 {
			t.Fatalf("token %q: %d", tok, c)
		}
	}
	if c, b := call("POST", "/provider/ai/credit", s.ProviderKey, `{"tenant":"nobody","amount":"10","ref":"x"}`); c != 404 {
		t.Fatalf("unknown firm: %d %s", c, b)
	}
	if c, b := call("POST", "/provider/ai/credit", s.ProviderKey, credit); c != 200 || !strings.Contains(b, `"balance": "5000.00"`) {
		t.Fatalf("credit: %d %s", c, b)
	}
	if c, b := call("GET", "/provider/ai/usage?format=csv", s.ProviderKey, ""); c != 200 || !strings.HasPrefix(b, "Firm,Answers") || !strings.Contains(b, "demo,0,0,0.00,0.00,0.00,5000.00,5000.00") {
		t.Fatalf("usage csv: %d %s", c, b)
	}
}
