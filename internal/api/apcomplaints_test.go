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

// The AP client reporting, the complaints tables by business and the
// impersonation register over the API (made-up clients and cases).
func TestAPComplaintsRoutes(t *testing.T) {
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
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.FixedZone("IST", 19800))
	o.Now = func() time.Time { return now }
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	// An AP and a client mapped to it this week.
	if c := ipoCall(t, srv, "POST", "/v1/partners", "admin", map[string]any{"code": "AP001", "name": "Made-up AP", "kind": "Authorised person", "branch": "Mumbai", "reg_no": "AP0000001", "mobile": "9000000002", "exchange_regs": map[string]string{"MCX": "MCXAP-1"}, "active": true}, nil); c != 200 {
		t.Fatalf("partner %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/clients/BH1006/mapping", "farhan.khan", map[string]string{"branch": "Mumbai", "partner": "AP001"}, nil); c != 200 {
		t.Fatalf("mapping %d", c)
	}
	var v ops.APMappingView
	if c := ipoCall(t, srv, "GET", "/v1/ap-mapping?week=2026-10-18&date=2026-10-14", "priya.nair", nil, &v); c != 200 || len(v.Exchanges) != 3 || len(v.Exchanges[0].Records) != 1 {
		t.Fatalf("report %d %+v", c, v)
	}
	if c := ipoCall(t, srv, "GET", "/v1/ap-mapping", "meera.iyer", nil, nil); c != 403 {
		t.Fatalf("branch staff saw the report: %d", c)
	}
	var file []byte
	if c := ipoCall(t, srv, "GET", "/v1/ap-mapping/file?week=2026-10-18&exchange=NSE", "priya.nair", nil, &file); c != 200 || !strings.Contains(string(file), "AP0000001,BH1006,") {
		t.Fatalf("file %d %s", c, file)
	}
	var hist []ops.APMapChange
	if c := ipoCall(t, srv, "GET", "/v1/ap-mapping/history/BH1006", "priya.nair", nil, &hist); c != 200 || len(hist) != 1 || hist[0].To != "AP001" {
		t.Fatalf("history %d %+v", c, hist)
	}
	now = now.AddDate(0, 0, 8) // Tuesday 20 Oct
	if c := ipoCall(t, srv, "POST", "/v1/ap-mapping/uploads", "priya.nair", map[string]string{"week": "2026-10-18", "exchange": "NSE", "date": "2026-10-20", "ack": "ENIT-1"}, &v); c != 200 || v.Exchanges[0].Status != "Uploaded" {
		t.Fatalf("upload %d %+v", c, v.Exchanges[0])
	}
	if c := ipoCall(t, srv, "POST", "/v1/ap-mapping/uploads", "priya.nair", map[string]string{"week": "2026-10-18", "exchange": "BSE", "date": "2026-10-20"}, nil); c != 422 {
		t.Fatalf("upload with no acknowledgement: %d", c)
	}
	var hits []ops.APContactHit
	if c := ipoCall(t, srv, "GET", "/v1/ap-mapping/contacts", "priya.nair", nil, &hits); c != 200 || len(hits) != 0 {
		t.Fatalf("contacts %d %+v", c, hits)
	}
	var cv ops.APCountView
	if c := ipoCall(t, srv, "GET", "/v1/ap-mapping/counts?month=2026-09", "priya.nair", nil, &cv); c != 200 || len(cv.Exchanges) != 3 {
		t.Fatalf("counts %d %+v", c, cv)
	}

	// A research analyst: its complaints table and an impersonation case.
	if c := ipoCall(t, srv, "PUT", "/v1/firm-profile", "vikram.shah", map[string]any{"registrations": []string{"ra"}}, nil); c != 200 {
		t.Fatalf("firm profile %d", c)
	}
	var tk ops.Ticket
	if c := ipoCall(t, srv, "POST", "/v1/tickets", "priya.nair", map[string]any{"kind": "Complaint", "category": "Research or advice (RA / IA)", "subject": "Made-up", "text": "Made-up", "name": "Made-up Subscriber", "business": "RA", "impersonation": true}, &tk); c != 201 || tk.Business != "RA" || tk.UCC != "" {
		t.Fatalf("ticket %d %+v", c, tk)
	}
	var tl struct {
		Businesses []map[string]string `json:"businesses"`
		Tables     []map[string]string `json:"tables"`
	}
	if c := ipoCall(t, srv, "GET", "/v1/tickets?date=2026-10-20", "priya.nair", nil, &tl); c != 200 || len(tl.Tables) < 2 || len(tl.Businesses) < 2 {
		t.Fatalf("tickets %d %+v", c, tl)
	}
	var ds ops.Disclosure
	if c := ipoCall(t, srv, "GET", "/v1/complaints/disclosure?month=2026-10&business=RA", "priya.nair", nil, &ds); c != 200 || ds.Business != "RA" || ds.Impersonation == nil || *ds.Impersonation != 1 || len(ds.Rows) != 3 {
		t.Fatalf("RA table %d %+v", c, ds)
	}
	if c := ipoCall(t, srv, "GET", "/v1/complaints/disclosure?month=2026-10", "priya.nair", nil, &ds); c != 200 || ds.Business != "Broking" || len(ds.Rows) != 4 {
		t.Fatalf("broker table %d %+v", c, ds)
	}
	var html []byte
	if c := ipoCall(t, srv, "GET", "/v1/complaints/disclosure/file?month=2026-10&business=RA&format=html", "priya.nair", nil, &html); c != 200 || !strings.Contains(string(html), "font-size:12pt") {
		t.Fatalf("html %d %s", c, html)
	}
	if c := ipoCall(t, srv, "POST", "/v1/tickets/"+tk.ID+"/tag", "priya.nair", map[string]any{"business": "Broking"}, nil); c != 422 {
		t.Fatalf("a non-client's complaint made a broker's: %d", c)
	}
	var ic ops.ImpCase
	if c := ipoCall(t, srv, "POST", "/v1/impersonation", "priya.nair", map[string]any{"businesses": []string{"RA"}, "who": "The research desk", "found_on": "2026-10-15", "where": "Social media", "handle": "@madeup", "details": "Fake tips"}, &ic); c != 200 || ic.ID == "" || len(ic.Missing) != 4 {
		t.Fatalf("case %d %+v", c, ic)
	}
	if c := ipoCall(t, srv, "POST", "/v1/impersonation/"+ic.ID+"/evidence?name=fir.pdf", "priya.nair", []byte("%PDF-1.4\nmade-up\n%%EOF"), &ic); c != 200 || len(ic.Evidence) != 1 {
		t.Fatalf("evidence %d %+v", c, ic)
	}
	var ev []byte
	if c := ipoCall(t, srv, "GET", "/v1/impersonation/evidence/"+ic.Evidence[0].ID, "vikram.shah", nil, &ev); c != 200 || !strings.HasPrefix(string(ev), "%PDF") {
		t.Fatalf("evidence back %d", c)
	}
	var list struct {
		Cases []ops.ImpCase `json:"cases"`
		Where []string      `json:"where"`
	}
	if c := ipoCall(t, srv, "GET", "/v1/impersonation", "anita.rao", nil, &list); c != 200 || len(list.Cases) != 1 || len(list.Where) == 0 {
		t.Fatalf("register %d %+v", c, list)
	}
	now = time.Date(2026, 11, 3, 10, 0, 0, 0, time.FixedZone("IST", 19800))
	var rv ops.ImpReportsView
	if c := ipoCall(t, srv, "GET", "/v1/impersonation/reports?month=2026-10", "priya.nair", nil, &rv); c != 200 || len(rv.Bodies) == 0 || rv.Bodies[0].Body != ops.ImpBSERA || len(rv.Bodies[0].Cases) != 1 {
		t.Fatalf("reports %d %+v", c, rv)
	}
	var rep []byte
	if c := ipoCall(t, srv, "GET", "/v1/impersonation/reports/file?body=BSE-RA&month=2026-10", "priya.nair", nil, &rep); c != 200 || !strings.Contains(string(rep), "@madeup") {
		t.Fatalf("report file %d %s", c, rep)
	}
	if c := ipoCall(t, srv, "POST", "/v1/impersonation/reports", "priya.nair", map[string]string{"body": "BSE-RA", "month": "2026-10", "submitted_on": "2026-11-03", "ref": "X"}, nil); c != 403 {
		t.Fatalf("a maker recorded the report: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/impersonation/reports", "vikram.shah", map[string]string{"body": "BSE-RA", "month": "2026-10", "submitted_on": "2026-11-03", "ref": "RAIA-1"}, &rv); c != 200 || rv.Bodies[0].Recorded == nil {
		t.Fatalf("report recorded %d %+v", c, rv.Bodies)
	}
	var rs ops.RAIASettings
	if c := ipoCall(t, srv, "PUT", "/v1/raia/settings", "vikram.shah", map[string]any{"ra_no": "INH000000002", "adjust_impersonation": true}, &rs); c != 200 || rs.RANo != "INH000000002" {
		t.Fatalf("settings %d %+v", c, rs)
	}
	if c := ipoCall(t, srv, "GET", "/v1/raia/settings", "meera.iyer", nil, nil); c != 403 {
		t.Fatalf("branch staff saw the settings: %d", c)
	}
}
