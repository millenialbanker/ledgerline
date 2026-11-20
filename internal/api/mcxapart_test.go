package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

// Staff screens show MCX money and equity money when the firm keeps MCX
// money apart with one bank account (no MCX-only account).
func TestMeMCXMoneyApart(t *testing.T) {
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
	srv := httptest.NewServer(s)
	defer srv.Close()
	me := func() map[string]any {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Actor", "priya.nair")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := me(); m["mcx_earmark"] != nil {
		t.Fatalf("MCX money shown apart by default: %v", m["mcx_earmark"])
	}
	set, _ := o.Settings(ctx, "demo")
	set.MCXMoneyApart = true
	if err := o.SaveSettings(ctx, "demo", "admin", set); err != nil {
		t.Fatal(err)
	}
	m := me()
	if m["mcx_earmark"] != true {
		t.Fatalf("mcx_earmark %v", m["mcx_earmark"])
	}
	if acc, ok := m["mcx_accounts"].([]any); !ok || len(acc) != 0 {
		t.Fatalf("mcx_accounts %v", m["mcx_accounts"])
	}
}
