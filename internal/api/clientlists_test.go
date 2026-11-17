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

// The sample firm's made-up clients only.
func TestClientListRoutes(t *testing.T) {
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
	o.Now = func() time.Time { return time.Date(2026, 11, 17, 10, 0, 0, 0, time.FixedZone("IST", 19800)) }
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	s.exchangeReportingRoutes()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	cs, _ := o.Clients(ctx, "demo")
	var c ops.Client
	for _, x := range cs {
		if x.Status == "Active" && x.Mobile != "" && !x.DPOnly {
			c = x
			break
		}
	}

	var v ops.MNRLView
	if code := ipoCall(t, srv, "GET", "/v1/mnrl?date=2026-11-17", "priya.nair", nil, &v); code != 200 || len(v.Sources) != 4 || !v.CanWork {
		t.Fatalf("view %d %+v", code, v)
	}
	if code := ipoCall(t, srv, "GET", "/v1/mnrl?date=2026-11-17", "meera.iyer", nil, nil); code != 403 {
		t.Fatalf("branch staff saw the matches: %d", code)
	}
	var l ops.MNRLList
	if code := ipoCall(t, srv, "POST", "/v1/mnrl/lists?format=dot-mnrl&name=mnrl.csv&date=2026-11-17", "priya.nair", []byte("Mobile Number\n"+c.Mobile+"\n"), &l); code != 200 || l.New == 0 {
		t.Fatalf("load %d %+v", code, l)
	}
	ipoCall(t, srv, "GET", "/v1/mnrl?date=2026-11-17", "priya.nair", nil, &v)
	if len(v.Matches) == 0 || strings.Contains(v.Matches[0].Mobile, c.Mobile) {
		t.Fatalf("matches %+v", v.Matches)
	}
	if code := ipoCall(t, srv, "POST", "/v1/mnrl/"+v.Matches[0].ID+"/status", "priya.nair", map[string]string{"status": "Contacted", "note": "Emailed the registered address"}, nil); code != 200 {
		t.Fatalf("status %d", code)
	}
	if code := ipoCall(t, srv, "POST", "/v1/mnrl/lists?format=bse-ucc&name=x.csv", "priya.nair", []byte("x"), nil); code != 422 {
		t.Fatalf("a layout that is not a list: %d", code)
	}

	var dl ops.DemiseList
	if code := ipoCall(t, srv, "POST", "/v1/deceased/lists?name=12345_Demise_Clients_by_KRA.csv&date=2026-11-17&cutoff=2026-11-21&circular=NSE/ISC/77375", "vikram.shah", []byte("PAN\n"+c.PAN+"\n"), &dl); code != 200 || dl.Matched == 0 || dl.Cutoff != "2026-11-21" {
		t.Fatalf("demise %d %+v", code, dl)
	}
	var dv ops.DemiseView
	if code := ipoCall(t, srv, "GET", "/v1/deceased?date=2026-11-17", "vikram.shah", nil, &dv); code != 200 || dv.Open == 0 || !dv.Loaded {
		t.Fatalf("deceased %d %+v", code, dv)
	}
	if code := ipoCall(t, srv, "POST", "/v1/deceased/"+dv.Cases[0].ID+"/steps", "vikram.shah", map[string]string{"action": "nonsense"}, nil); code != 422 {
		t.Fatalf("unknown step %d", code)
	}
	if code := ipoCall(t, srv, "POST", "/v1/deceased/"+dv.Cases[0].ID+"/steps", "vikram.shah", map[string]string{"action": "verify", "note": "Original death certificate seen"}, nil); code != 200 {
		t.Fatalf("verify %d", code)
	}
	var csv []byte
	if code := ipoCall(t, srv, "GET", "/v1/deceased/ucc-list?exchange=NSE", "vikram.shah", nil, &csv); code != 200 || !strings.Contains(string(csv), "Client code") {
		t.Fatalf("csv %d %s", code, csv)
	}
	var n map[string]int
	if code := ipoCall(t, srv, "POST", "/v1/deceased/cutoff", "vikram.shah", map[string]string{"period": "2026-10", "cutoff": "2026-11-20"}, &n); code != 200 || n["cases"] != 1 {
		t.Fatalf("cutoff %d %v", code, n)
	}
}
