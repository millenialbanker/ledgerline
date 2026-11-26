package api

import (
	"strings"
	"testing"
	"time"

	"ledgerline/internal/ops"
)

// Made-up alert numbers and accounts only.
func TestDPSurveillanceRoutes(t *testing.T) {
	now := time.Date(2026, 11, 17, 11, 0, 0, 0, time.UTC)
	srv, _ := ipoServer(t, now)
	file := []byte("Alert No,Alert Date,Alert Type,Client Code\nMRS1,2026-11-16,Client activity surge,NOBODY1\n")
	if c := ipoCall(t, srv, "POST", "/v1/surveillance/import/mcx-mrs?name=mrs.csv", "priya.nair", file, nil); c != 403 {
		t.Fatalf("maker loaded alerts: %d", c)
	}
	var r ops.SurvImportResult
	if c := ipoCall(t, srv, "POST", "/v1/surveillance/import/mcx-mrs?name=mrs.csv", "vikram.shah", file, &r); c != 200 || r.Download.Added != 1 || len(r.Unmatched) != 1 {
		t.Fatalf("import %d %+v", c, r)
	}
	if c := ipoCall(t, srv, "POST", "/v1/surveillance/downloads/cdsl-fiu/nil", "vikram.shah", map[string]string{}, nil); c != 200 {
		t.Fatalf("nil download %d", c)
	}
	var log []ops.SurvDownload
	if c := ipoCall(t, srv, "GET", "/v1/surveillance/downloads", "farhan.khan", nil, &log); c != 200 || len(log) != 2 {
		t.Fatalf("log %d %+v", c, log)
	}
	var alerts []ops.Alert
	ipoCall(t, srv, "GET", "/v1/alerts", "vikram.shah", nil, &alerts)
	id := ""
	for _, a := range alerts {
		if a.Ref == "MRS1" {
			id = a.ID
			if a.Due != "2026-12-31" || len(a.Checklist) == 0 {
				t.Fatalf("alert %+v", a)
			}
		}
	}
	if id == "" {
		t.Fatal("the MCX alert is not listed")
	}
	if c := ipoCall(t, srv, "POST", "/v1/surveillance/alerts/"+id+"/docs?name=s.pdf", "vikram.shah", []byte("%PDF-1.4\n%%EOF\n"), nil); c != 200 {
		t.Fatalf("doc %d", c)
	}
	var a ops.Alert
	if c := ipoCall(t, srv, "PUT", "/v1/surveillance/alerts/"+id+"/checklist", "vikram.shah", map[string]any{"items": []map[string]string{{"key": "attached", "state": "Done"}}}, &a); c != 200 || len(a.Attachments) != 1 {
		t.Fatalf("checklist %d %+v", c, a)
	}
	var b []byte
	if c := ipoCall(t, srv, "GET", "/v1/surveillance/alerts/"+id+"/docs/"+a.Attachments[0].ID, "vikram.shah", nil, &b); c != 200 || !strings.HasPrefix(string(b), "%PDF") {
		t.Fatalf("doc back %d", c)
	}
	var thr ops.DPSurvSettings
	if c := ipoCall(t, srv, "GET", "/v1/surveillance/thresholds", "vikram.shah", nil, &thr); c != 200 || thr.ClusterSize != 20 {
		t.Fatalf("thresholds %d %+v", c, thr)
	}
	if c := ipoCall(t, srv, "PUT", "/v1/surveillance/thresholds", "farhan.khan", map[string]int{"fan_count": 3}, nil); c != 403 {
		t.Fatalf("checker set thresholds: %d", c)
	}
	var run map[string]int
	if c := ipoCall(t, srv, "POST", "/v1/surveillance/run", "vikram.shah", map[string]string{"date": "2026-11-17"}, &run); c != 200 || run["raised"] != run["trading"]+run["dp"] {
		t.Fatalf("run %d %+v", c, run)
	}
	var ret ops.SurvReturn
	if c := ipoCall(t, srv, "GET", "/v1/surveillance-return?for=CDSL", "vikram.shah", nil, &ret); c != 200 || ret.For != "CDSL" {
		t.Fatalf("CDSL return %d %+v", c, ret)
	}
	if c := ipoCall(t, srv, "GET", "/v1/surveillance/connected", "vikram.shah", nil, nil); c != 200 {
		t.Fatalf("connected %d", c)
	}
	if c := ipoCall(t, srv, "GET", "/v1/surveillance/sample", "vikram.shah", nil, nil); c != 200 {
		t.Fatalf("sample %d", c)
	}
}
