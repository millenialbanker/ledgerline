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
	"ledgerline/internal/sample"
	"ledgerline/internal/vault"
)

func TestBSEUCCRoutes(t *testing.T) {
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
	if _, err := o.LoadSampleDay(ctx, "demo", "priya.nair", sample.Generate("2026-10-16"), false, func(ctx context.Context, tenant, actor, source, name string, data []byte) error {
		_, err := a.IngestFile(ctx, tenant, actor, source, "", name, data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ist := time.FixedZone("IST", 19800)
	o.Now = func() time.Time { return time.Date(2026, 10, 30, 11, 0, 0, 0, ist) }
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	s.bseUCCRoutes()
	s.bseUCCRoutes() // wiring it twice is harmless
	srv := httptest.NewServer(s)
	defer srv.Close()

	var v ops.BSEUCCView
	if code := ipoCall(t, srv, "GET", "/v1/ucc-api/bse", "priya.nair", nil, &v); code != 200 || v.Exchange != "BSE" || v.Connection != "Not set" || v.Clients == nil || v.Runs == nil {
		t.Fatalf("view %d %+v", code, v)
	}
	// The sample firm's clients at the UCC upload stage are waiting, most
	// with problems (the sample keeps no addresses or dates of birth).
	if len(v.Clients) == 0 || v.Counts["Needs fixing"] == 0 || len(v.Clients[0].Problems) == 0 {
		t.Fatalf("clients %+v", v.Counts)
	}
	var raw map[string]any
	ipoCall(t, srv, "GET", "/v1/ucc-api/bse", "priya.nair", nil, &raw)
	for _, k := range []string{"exchange", "connection", "live", "counts", "clients", "runs"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("view lacks %s", k)
		}
	}
	row := raw["clients"].([]any)[0].(map[string]any)
	for _, k := range []string{"ucc", "name", "pan", "action", "status", "reason", "problems", "pan_status", "sent_at", "result_at", "ref"} {
		if _, ok := row[k]; !ok {
			t.Errorf("client row lacks %s", k)
		}
	}
	// Sending needs the connection; an empty body means all pending.
	if code := ipoCall(t, srv, "POST", "/v1/ucc-api/bse/send", "priya.nair", nil, nil); code != 409 {
		t.Fatalf("send without a connection: %d", code)
	}
	if code := ipoCall(t, srv, "POST", "/v1/ucc-api/bse/send", "anita.rao", map[string]any{"uccs": []string{}}, nil); code != 403 {
		t.Fatalf("risk manager send: %d", code)
	}
	if code := ipoCall(t, srv, "POST", "/v1/ucc-api/bse/pan-status", "priya.nair", map[string]any{"uccs": []string{"X"}}, nil); code != 409 {
		t.Fatalf("pan status without a connection: %d", code)
	}
	if code := ipoCall(t, srv, "POST", "/v1/ucc-api/bse/reset-password", "priya.nair", map[string]string{"new_password": "New@pass22"}, nil); code != 403 {
		t.Fatalf("maker reset: %d", code)
	}
	var p ops.BSEUCCPreview
	if code := ipoCall(t, srv, "GET", "/v1/ucc-api/bse/preview/"+v.Clients[0].UCC, "priya.nair", nil, &p); code != 200 || p.UCC != v.Clients[0].UCC || len(p.Payload) == 0 {
		t.Fatalf("preview %d %+v", code, p)
	}
	var file []byte
	if code := ipoCall(t, srv, "GET", "/v1/ucc-api/bse/batch-file", "priya.nair", nil, &file); code != 200 {
		t.Fatalf("batch file %d %s", code, file)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(file)), "\r\n") {
		if l != "" && strings.Count(l, "|") != 135 {
			t.Fatalf("line %q", l)
		}
	}
}
