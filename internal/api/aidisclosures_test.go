package api

import (
	"context"
	"io"
	"log/slog"
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

func TestAIDisclosureRoutes(t *testing.T) {
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
	o.Now = func() time.Time { return time.Date(2026, 11, 2, 10, 0, 0, 0, time.FixedZone("IST", 19800)) }
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	s.aiDisclosureRoutes()
	s.aiDisclosureRoutes() // wiring it twice is harmless
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	var cv ops.AIControlsView
	if c := ipoCall(t, srv, "GET", "/v1/ai/controls", "vikram.shah", nil, &cv); c != 200 || len(cv.Systems) != 2 || len(cv.Masking) != 8 || cv.Connected {
		t.Fatalf("controls %d %+v", c, cv)
	}
	if c := ipoCall(t, srv, "GET", "/v1/ai/controls", "priya.nair", nil, nil); c != 403 {
		t.Fatalf("maker saw controls: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/ai/off", "vikram.shah", map[string]any{"off": true, "reason": "audit"}, &cv.Controls); c != 200 || !cv.Controls.Off {
		t.Fatalf("off %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/ai/providers/openai", "vikram.shah", map[string]any{"off": true, "reason": "not approved"}, nil); c != 200 {
		t.Fatalf("provider off %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/ai/masking", "vikram.shah", map[string]any{"category": "email", "on": false}, nil); c != 403 {
		t.Fatalf("compliance changed masking: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/ai/masking", "admin", map[string]any{"category": "pan", "on": false}, nil); c != 422 {
		t.Fatalf("pan unmasked: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/ai/log-retention", "vikram.shah", map[string]any{"days": 730}, nil); c != 200 {
		t.Fatalf("retention %d", c)
	}
	var lv ops.AILogView
	if c := ipoCall(t, srv, "GET", "/v1/ai/log?from=2026-10-01&to=2026-11-02", "vikram.shah", nil, &lv); c != 200 || lv.Retention != 730 {
		t.Fatalf("log %d %+v", c, lv)
	}
	var csv []byte
	if c := ipoCall(t, srv, "GET", "/v1/ai/log.csv", "vikram.shah", nil, &csv); c != 200 || !strings.HasPrefix(string(csv), "ID,Time,User") {
		t.Fatalf("log csv %d %s", c, csv)
	}
	tool := map[string]any{"name": "Chat assistant for staff", "purpose": "Drafting replies", "order_execution": "N", "discretionary": "N", "advice": "N", "cyber_security": "N", "active": true}
	if c := ipoCall(t, srv, "POST", "/v1/ai/tools", "vikram.shah", tool, nil); c != 200 {
		t.Fatalf("tool %d", c)
	}
	var inv []ops.AISystem
	if c := ipoCall(t, srv, "GET", "/v1/ai/inventory", "vikram.shah", nil, &inv); c != 200 || len(inv) != 3 {
		t.Fatalf("inventory %d %d", c, len(inv))
	}

	var ds ops.DisclosuresScreen
	if c := ipoCall(t, srv, "GET", "/v1/disclosures", "vikram.shah", nil, &ds); c != 200 || ds.AIML.Period.ID != "2026-04_2026-09" || ds.SaaS.Period.ID != "2026-01_2026-06" || len(ds.AIML.Exchanges) != 3 {
		t.Fatalf("screen %d %+v", c, ds.AIML.Period)
	}
	if ds.AIML.Answer != "Yes" { // the firm's own tool is in use
		t.Fatalf("answer %s", ds.AIML.Answer)
	}
	if c := ipoCall(t, srv, "GET", "/v1/disclosures", "anita.rao", nil, nil); c != 403 {
		t.Fatalf("risk saw disclosures: %d", c)
	}
	var dv ops.DisclosureView
	if c := ipoCall(t, srv, "GET", "/v1/disclosures/saas?period=2025-07_2025-12", "vikram.shah", nil, &dv); c != 200 || dv.Period.Due != "2026-01-31" {
		t.Fatalf("saas %d %+v", c, dv.Period)
	}
	if c := ipoCall(t, srv, "PUT", "/v1/disclosures/aiml/2026-04_2026-09/answers", "vikram.shah", map[string]string{"key": "h.category", "value": "Stock broker and depository participant"}, &dv); c != 200 || dv.Fields[1].Value != "Stock broker and depository participant" {
		t.Fatalf("edit %d %+v", c, dv.Fields[1])
	}
	if c := ipoCall(t, srv, "PUT", "/v1/disclosures/deployment", "vikram.shah", map[string]string{"deployment": "own_cloud", "provider": "Azure", "region": "Central India"}, nil); c != 200 {
		t.Fatalf("deployment %d", c)
	}
	if c := ipoCall(t, srv, "PUT", "/v1/disclosures/profile", "vikram.shah", map[string]any{"aiml_cycle": "weekly"}, nil); c != 422 {
		t.Fatalf("bad cycle %d", c)
	}
	var ev ops.PeriodicFile
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	if c := ipoCall(t, srv, "POST", "/v1/disclosures/evidence?name=ack.png", "vikram.shah", png, &ev); c != 200 || ev.ID == "" {
		t.Fatalf("evidence %d %+v", c, ev)
	}
	sub := map[string]string{"exchange": "MCX", "date": "2026-10-14", "ref": "MCX-ACK-1", "file_id": ev.ID}
	if c := ipoCall(t, srv, "POST", "/v1/disclosures/aiml/2026-04_2026-09/submit", "vikram.shah", sub, &dv); c != 200 || dv.Status != "Partly submitted" {
		t.Fatalf("submit %d %s", c, dv.Status)
	}
	var img []byte
	if c := ipoCall(t, srv, "GET", "/v1/disclosures/evidence/"+ev.ID, "vikram.shah", nil, &img); c != 200 || len(img) != len(png) {
		t.Fatalf("evidence back %d %d", c, len(img))
	}
	if c := ipoCall(t, srv, "GET", "/v1/disclosures/aiml/2026-04_2026-09/csv", "vikram.shah", nil, &csv); c != 200 || !strings.Contains(string(csv), "MCX-ACK-1") {
		t.Fatalf("csv %d %s", c, csv)
	}
}
