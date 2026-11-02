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
	"ledgerline/internal/sample"
	"ledgerline/internal/vault"
)

func TestExchangeReportingRoutes(t *testing.T) {
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
	o.Now = func() time.Time { return time.Date(2026, 10, 17, 10, 0, 0, 0, time.FixedZone("IST", 19800)) }
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	s.exchangeReportingRoutes()
	s.exchangeReportingRoutes() // wiring it twice is harmless
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	var chk ops.SegmonCheck
	if c := ipoCall(t, srv, "GET", "/v1/segmon/check?date=2026-10-16", "priya.nair", nil, &chk); c != 200 || chk.Date != "2026-10-16" || chk.Rows == 0 {
		t.Fatalf("check %d %+v", c, chk)
	}
	if c := ipoCall(t, srv, "GET", "/v1/segmon/check?date=2026-10-16", "meera.iyer", nil, nil); c != 403 {
		t.Fatalf("branch staff ran the check: %d", c)
	}
	var res ops.SegmonResponse
	body := []byte("UCC Code,Error Description\nZZ999,Client not registered\n")
	if c := ipoCall(t, srv, "POST", "/v1/segmon/response?date=2026-10-16&name=resp.csv", "priya.nair", body, &res); c != 200 || res.Rejected != 1 || !res.Unconfirmed {
		t.Fatalf("response %d %+v", c, res)
	}
	var rs []ops.SegmonResponse
	if c := ipoCall(t, srv, "GET", "/v1/segmon/responses?date=2026-10-16", "priya.nair", nil, &rs); c != 200 || len(rs) != 1 {
		t.Fatalf("responses %d %d", c, len(rs))
	}
	var hv ops.NSEHoldingView
	if c := ipoCall(t, srv, "GET", "/v1/holding-api?date=2026-10-16", "priya.nair", nil, &hv); c != 200 || hv.Connection != "Not set" {
		t.Fatalf("holding view %d %+v", c, hv)
	}
	if c := ipoCall(t, srv, "POST", "/v1/holding-api/submit", "priya.nair", map[string]string{"date": "2026-10-16"}, nil); c != 409 {
		t.Fatalf("submit without a connection: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/holding-api/submit", "anita.rao", map[string]string{"date": "2026-10-16"}, nil); c != 403 {
		t.Fatalf("risk manager submitted: %d", c)
	}
}
