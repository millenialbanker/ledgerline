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

func TestNSEUCIRoutes(t *testing.T) {
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
	o.Now = func() time.Time { return time.Date(2026, 10, 30, 11, 0, 0, 0, time.FixedZone("IST", 19800)) }
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	s.nseUCIRoutes()
	s.nseUCIRoutes() // wiring it twice is harmless
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	var v ops.NSEUCIView
	if c := ipoCall(t, srv, "GET", "/v1/ucc-api/nse", "anita.rao", nil, &v); c != 200 {
		t.Fatalf("view %d", c)
	}
	if v.Exchange != "NSE" || v.Connection != "Not set" || len(v.Clients) == 0 || v.Counts["Needs fixing"] == 0 {
		t.Fatalf("view %+v", v)
	}
	// The sample clients have no address lines yet: each says what is missing.
	row := v.Clients[0]
	if row.Status != "Needs fixing" || len(row.Problems) == 0 || row.Action != "Add" {
		t.Fatalf("row %+v", row)
	}
	if c := ipoCall(t, srv, "GET", "/v1/ucc-api/nse", "meera.iyer", nil, nil); c != 403 {
		t.Fatalf("branch staff saw the screen: %d", c)
	}
	var res map[string]any
	if c := ipoCall(t, srv, "POST", "/v1/ucc-api/nse/send", "priya.nair", map[string]any{"uccs": []string{}}, &res); c != 409 {
		t.Fatalf("send without a connection: %d %v", c, res)
	}
	var pv ops.NSEUCIPreview
	if c := ipoCall(t, srv, "GET", "/v1/ucc-api/nse/preview/"+row.UCC, "priya.nair", nil, &pv); c != 200 || len(pv.Records) == 0 || len(pv.Problems) == 0 {
		t.Fatalf("preview %d %+v", c, pv)
	}
	if c := ipoCall(t, srv, "POST", "/v1/ucc-api/details/"+row.UCC, "priya.nair", map[string]any{"income_as_on": "30-10-2026"}, nil); c != 422 {
		t.Fatalf("bad date accepted: %d", c)
	}
	var d ops.UCCDetails
	if c := ipoCall(t, srv, "POST", "/v1/ucc-api/details/"+row.UCC, "priya.nair", map[string]any{"address1": "12 Test Lane, Fort", "pin": "400001"}, &d); c != 200 || d.Address1 != "12 Test Lane, Fort" {
		t.Fatalf("details %d %+v", c, d)
	}
	if c := ipoCall(t, srv, "POST", "/v1/ucc-api/details/"+row.UCC, "anita.rao", map[string]any{"pin": "400001"}, nil); c != 403 {
		t.Fatalf("risk manager edited details: %d", c)
	}
	if c := ipoCall(t, srv, "GET", "/v1/ucc-api/details/"+row.UCC, "priya.nair", nil, &d); c != 200 || d.PIN != "400001" {
		t.Fatalf("details read %d %+v", c, d)
	}
}
