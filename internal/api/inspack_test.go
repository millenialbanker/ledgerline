package api

import (
	"archive/zip"
	"bytes"
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

func TestInspectionPackRoutes(t *testing.T) {
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
	srv := httptest.NewServer(New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true))
	t.Cleanup(srv.Close)

	var v ops.PacksScreen
	if c := ipoCall(t, srv, "GET", "/v1/inspection-packs", "vikram.shah", nil, &v); c != 200 || len(v.Templates) != 4 || len(v.Sections) < 25 || v.Settings.KeepDays != 30 {
		t.Fatalf("screen %d %d templates %d sections", c, len(v.Templates), len(v.Sections))
	}
	if why := v.Ready["self_inspection|"]; !strings.Contains(why, "not in this version") {
		t.Fatalf("self-inspection readiness %q", why)
	}
	for _, who := range []string{"priya.nair", "anita.rao", "meera.iyer", "sunrise.ap"} {
		if c := ipoCall(t, srv, "GET", "/v1/inspection-packs", who, nil, nil); c != 403 {
			t.Fatalf("%s saw the packs: %d", who, c)
		}
	}
	day := time.Now().In(time.FixedZone("IST", 19800)).Format("2006-01-02")
	var s ops.PackSample
	// A firm with no clients yet has none to sample.
	if c := ipoCall(t, srv, "POST", "/v1/inspection-packs/sample", "vikram.shah", map[string]any{"to": day, "sample": map[string]any{"mode": "random", "n": 3, "seed": 77}}, nil); c != 422 {
		t.Fatalf("sample from no clients: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/inspection-packs/sample", "vikram.shah", map[string]any{"to": day, "sample": map[string]any{"mode": "none"}}, &s); c != 200 || s.Mode != "none" {
		t.Fatalf("no sample %d %+v", c, s)
	}
	req := map[string]any{"title": "BSE inspection", "from": day, "to": day, "items": []map[string]any{{"title": "Nominations", "section": "nomination", "period": "end"}, {"title": "Voice logs", "section": "not_held", "note": "On the voice logger"}}}
	if c := ipoCall(t, srv, "POST", "/v1/inspection-packs", "priya.nair", req, nil); c != 403 {
		t.Fatalf("maker made a pack: %d", c)
	}
	var p ops.Pack
	if c := ipoCall(t, srv, "POST", "/v1/inspection-packs", "vikram.shah", req, &p); c != 200 || p.ID == "" {
		t.Fatalf("make %d %+v", c, p)
	}
	for i := 0; i < 400 && p.Status != ops.PackReady; i++ {
		time.Sleep(20 * time.Millisecond)
		ipoCall(t, srv, "GET", "/v1/inspection-packs/"+p.ID, "vikram.shah", nil, &p)
	}
	if p.Status != ops.PackReady || len(p.Results) != 2 || p.Results[1].Status != "Not available" {
		t.Fatalf("pack %s %+v", p.Status, p.Results)
	}
	var zipb []byte
	if c := ipoCall(t, srv, "GET", "/v1/inspection-packs/"+p.ID+"/download", "priya.nair", nil, &zipb); c != 403 {
		t.Fatalf("maker downloaded: %d", c)
	}
	if c := ipoCall(t, srv, "GET", "/v1/inspection-packs/"+p.ID+"/download", "vikram.shah", nil, &zipb); c != 200 {
		t.Fatalf("download %d %s", c, zipb)
	}
	if zr, err := zip.NewReader(bytes.NewReader(zipb), int64(len(zipb))); err != nil || len(zr.File) < 5 {
		t.Fatalf("zip %v", err)
	}
	var idx []byte
	if c := ipoCall(t, srv, "GET", "/v1/inspection-packs/"+p.ID+"/index", "vikram.shah", nil, &idx); c != 200 || !strings.Contains(string(idx), "Index of the inspection pack") {
		t.Fatalf("index %d", c)
	}
	var m ops.Pack
	if c := ipoCall(t, srv, "POST", "/v1/inspection-packs/"+p.ID+"/masked-copy", "vikram.shah", map[string]any{}, &m); c != 200 || !m.Mask || m.CopyOf != p.ID {
		t.Fatalf("masked copy %d %+v", c, m)
	}
	if c := ipoCall(t, srv, "POST", "/v1/inspection-packs/"+p.ID+"/expire", "vikram.shah", map[string]any{"why": "inspection closed"}, &p); c != 200 || !p.Expired {
		t.Fatalf("expire %d", c)
	}
	if c := ipoCall(t, srv, "GET", "/v1/inspection-packs/"+p.ID+"/download", "vikram.shah", nil, nil); c != 409 {
		t.Fatalf("expired pack served: %d", c)
	}
	var tpl ops.PackTemplate
	if c := ipoCall(t, srv, "POST", "/v1/inspection-templates", "vikram.shah", map[string]any{"name": "Our MCX inspection", "items": req["items"]}, &tpl); c != 200 || tpl.ID != "TPL-0001" {
		t.Fatalf("template %d %+v", c, tpl)
	}
	if c := ipoCall(t, srv, "POST", "/v1/inspection-templates/std-exchange/delete", "vikram.shah", map[string]any{}, nil); c != 422 {
		t.Fatalf("standard template deleted: %d", c)
	}
	var d ops.PackDoc
	if c := ipoCall(t, srv, "POST", "/v1/inspection-docs?kind=VAPT+report&title=VAPT+2026&name=vapt.pdf", "vikram.shah", []byte("%PDF-1.4 made up"), &d); c != 200 || d.ID == "" {
		t.Fatalf("document %d %+v", c, d)
	}
	var db []byte
	if c := ipoCall(t, srv, "GET", "/v1/inspection-docs/"+d.ID, "vikram.shah", nil, &db); c != 200 || !bytes.HasPrefix(db, []byte("%PDF")) {
		t.Fatalf("document file %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/inspection-docs/"+d.ID+"/retire", "vikram.shah", map[string]any{"why": "replaced"}, &d); c != 200 || !d.Retired {
		t.Fatalf("retire %d", c)
	}
	if c := ipoCall(t, srv, "PUT", "/v1/inspection-packs/settings", "vikram.shah", map[string]any{"keep_days": 45}, nil); c != 200 {
		t.Fatalf("settings %d", c)
	}
}
