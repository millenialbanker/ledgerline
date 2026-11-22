package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
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

// All data here is made up.
func TestParallelRoutes(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	o.Now = func() time.Time { return time.Date(2026, 11, 20, 19, 0, 0, 0, time.FixedZone("IST", 19800)) }
	if err := o.SeedDev(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	srv := httptest.NewServer(s)
	defer srv.Close()
	do := func(req *http.Request, who string) (int, map[string]any) {
		t.Helper()
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Actor", who)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&v)
		return resp.StatusCode, v
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, p := range [][2]string{{"old", "ABC123,1,0,0,1,1\n"}, {"new", "ABC123,1,0,0,1,2\n"}} {
		w, _ := mw.CreateFormFile(p[0], "C_CC90311_01_20261120.csv")
		w.Write([]byte(p[1]))
	}
	mw.WriteField("format", "ncl-client-collateral")
	mw.WriteField("date", "2026-11-20")
	mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/parallel/compare", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	code, v := do(req, "priya.nair")
	if code != 200 || v["match"] != false || v["differing"].(float64) != 1 {
		t.Fatalf("compare %d %v", code, v)
	}
	id := v["id"].(string)
	req, _ = http.NewRequest("GET", srv.URL+"/v1/parallel/compare/"+id, nil)
	if code, v := do(req, "farhan.khan"); code != 200 || len(v["lines"].([]any)) != 1 {
		t.Fatalf("one %d %v", code, v)
	}
	req, _ = http.NewRequest("GET", srv.URL+"/v1/parallel", nil)
	if code, v := do(req, "farhan.khan"); code != 200 || v["goal"].(float64) != 10 || v["streak"].(float64) != 0 {
		t.Fatalf("view %d %v", code, v)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/v1/parallel/compare", strings.NewReader("x"))
	if code, _ := do(req, "priya.nair"); code != 400 {
		t.Fatalf("not multipart: %d", code)
	}

	// The firm's demat accounts: seen by head office, tagged by an Admin.
	req, _ = http.NewRequest("GET", srv.URL+"/v1/firm-demats", nil)
	if code, v := do(req, "farhan.khan"); code != 200 || len(v["types"].([]any)) != 12 {
		t.Fatalf("firm demats %d %v", code, v)
	}
	body2 := `{"tags":{"1208169999000001":"POOL"},"firm_ucc":"FIRM01"}`
	req, _ = http.NewRequest("PUT", srv.URL+"/v1/firm-demats", strings.NewReader(body2))
	if code, _ := do(req, "priya.nair"); code != 403 {
		t.Fatalf("maker tagged: %d", code)
	}
	req, _ = http.NewRequest("PUT", srv.URL+"/v1/firm-demats", strings.NewReader(body2))
	if code, v := do(req, "admin"); code != 200 || v["firm_ucc"] != "FIRM01" {
		t.Fatalf("admin tagged: %d %v", code, v)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/v1/firm-demats/from-statement?name=old.json", strings.NewReader(`{"holdingData":[{"dmat":"1208169999000002","accountType":"CLNT COLL"}]}`))
	if code, v := do(req, "admin"); code != 200 || len(v["found"].(map[string]any)) != 1 {
		t.Fatalf("from statement: %d %v", code, v)
	}
}
