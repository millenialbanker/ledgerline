package api

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

func TestMCXUCCRoutes(t *testing.T) {
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
	ist := time.FixedZone("IST", 19800)
	o.Now = func() time.Time { return time.Date(2026, 10, 30, 11, 0, 0, 0, ist) }
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	s.mcxUCCRoutes()
	s.mcxUCCRoutes() // wiring it twice is harmless
	srv := httptest.NewServer(s)
	defer srv.Close()

	var raw map[string]any
	if code := ipoCall(t, srv, "GET", "/v1/ucc-api/mcx", "priya.nair", nil, &raw); code != 200 {
		t.Fatalf("view %d %v", code, raw)
	}
	for _, k := range []string{"exchange", "connection", "counts", "clients", "runs", "member", "next_batch", "master"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("view lacks %s", k)
		}
	}
	if raw["exchange"] != "MCX" || raw["next_batch"] != float64(1) {
		t.Errorf("view %v %v", raw["exchange"], raw["next_batch"])
	}
	if code := ipoCall(t, srv, "GET", "/v1/ucc-api/mcx", "anita.rao", nil, nil); code != 403 {
		t.Fatalf("risk manager view: %d", code)
	}
	// No State-City master loaded: nothing can go, said in words.
	var file []byte
	if code := ipoCall(t, srv, "GET", "/v1/ucc-api/mcx/batch-file", "priya.nair", nil, &file); code != 409 {
		t.Fatalf("batch file %d %s", code, file)
	}
	if code := ipoCall(t, srv, "POST", "/v1/ucc-api/mcx/batch-file/uploaded", "priya.nair", map[string]string{"file": "not-a-ucc-file.csv"}, nil); code != 422 {
		t.Fatalf("uploaded with a bad name: %d", code)
	}
	if code := ipoCall(t, srv, "POST", "/v1/ucc-api/mcx/batch-file/uploaded", "priya.nair", map[string]string{"file": "MCX_30102026_UCC56204.M01"}, nil); code != 409 {
		t.Fatalf("uploaded a file never written: %d", code)
	}
	if code := ipoCall(t, srv, "POST", "/v1/ucc-api/mcx/batch-file/uploaded", "anita.rao", map[string]string{"file": "MCX_30102026_UCC56204.M01"}, nil); code != 403 {
		t.Fatalf("risk manager uploaded: %d", code)
	}
	if code := ipoCall(t, srv, "GET", "/v1/ucc-api/mcx/preview/NOSUCH", "priya.nair", nil, nil); code != 404 {
		t.Fatalf("preview of no client: %d", code)
	}
	// The UCC API: not set up yet (409), and not for a risk manager.
	if raw["api"] != "Not set" {
		t.Errorf("api %v", raw["api"])
	}
	for _, p := range []string{"/v1/ucc-api/mcx/send", "/v1/ucc-api/mcx/send/MCX01", "/v1/ucc-api/mcx/send-pending", "/v1/ucc-api/mcx/sync"} {
		if code := ipoCall(t, srv, "POST", p, "priya.nair", nil, nil); code != 409 {
			t.Errorf("%s without the connection: %d", p, code)
		}
		if code := ipoCall(t, srv, "POST", p, "anita.rao", nil, nil); code != 403 {
			t.Errorf("%s by a risk manager: %d", p, code)
		}
	}
}
