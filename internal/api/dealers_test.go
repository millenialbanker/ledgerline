package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
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

func TestDealersAPI(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	o.Now = func() time.Time { return time.Date(2026, 10, 30, 10, 0, 0, 0, time.FixedZone("IST", 19800)) }
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveBranch(ctx, "demo", "admin", ops.Branch{Code: "Mumbai", Name: "Mumbai", City: "Mumbai", Active: true}); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	s.dealerRoutes()
	s.dealerRoutes() // registering twice is harmless
	srv := httptest.NewServer(s)
	defer srv.Close()
	call := func(method, path, who, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Actor", who)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if strings.Contains(resp.Header.Get("Content-Type"), "json") {
			return resp.StatusCode, strings.Join(strings.Fields(string(b)), "") // compact
		}
		return resp.StatusCode, string(b)
	}
	body := `{"first_name":"Asha","last_name":"Dealer","pan":"AAAPA1111A","dob":"1990-01-01","relationship":"Employee","branch":"Mumbai",
	  "office":{"address":"Colaba Causeway","area":"Colaba","city":"MUMBAI","state":"Maharashtra","pincode":"400005"},"residence":{"address":"Flat 1","area":"Andheri","city":"MUMBAI","pincode":"400058"},
	  "nism":{"cert_no":"NISM-1","series":"VIII","exam_date":"2024-01-31","valid_till":"2027-01-30"},
	  "terminals":[{"exchange":"BSE","segments":["CM"],"terminal_id":"555","location_id":"4000010001001","active_from":"2026-10-01"}]}`
	if c, b := call("POST", "/v1/dealers", "farhan.khan", body); c != 403 {
		t.Fatalf("checker created: %d %s", c, b)
	}
	c, b := call("POST", "/v1/dealers", "priya.nair", body)
	var d ops.Dealer
	if c != 201 || json.Unmarshal([]byte(b), &d) != nil || d.ID != "DL-0001" {
		t.Fatalf("create: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/dealers/DL-0001", "anita.rao", ""); c != 200 || strings.Contains(b, "AAAPA1111A") {
		t.Fatalf("get: %d %s", c, b)
	}
	d.Mobile = "9876543210"
	put, _ := json.Marshal(d)
	if c, b := call("PUT", "/v1/dealers/DL-0001", "vikram.shah", string(put)); c != 200 || !strings.Contains(b, "9876543210") {
		t.Fatalf("put: %d %s", c, b)
	}
	c, b = call("GET", "/v1/dealers?date=2026-10-30", "farhan.khan", "")
	var v ops.DealersView
	if c != 200 || json.Unmarshal([]byte(b), &v) != nil || v.Counts.Active != 1 || v.Counts.Terminals != 1 || strings.Contains(b, "AAAPA1111A") {
		t.Fatalf("list: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/dealers/report?from=2026-10-01&to=2026-10-30", "anita.rao", ""); c != 200 || !strings.Contains(b, `"rows"`) {
		t.Fatalf("report: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/dealers/inspection.csv", "anita.rao", ""); c != 200 || !strings.HasPrefix(b, "Exchange,Segment") {
		t.Fatalf("inspection: %d %s", c, b)
	}
	if c, _ := call("GET", "/v1/dealers/export.csv", "anita.rao", ""); c != 403 {
		t.Fatalf("risk exported: %d", c)
	}
	c, exp := call("GET", "/v1/dealers/export.csv", "priya.nair", "")
	if c != 200 || !strings.Contains(exp, "AAAPA1111A") {
		t.Fatalf("export: %d %s", c, exp)
	}
	if c, b := call("POST", "/v1/dealers/import", "priya.nair", "PAN,FirstName,Relationship\nAAAPB222,Bimal,Employee\n"); c != 422 || !strings.Contains(b, `"line":2`) {
		t.Fatalf("import errors: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/dealers/import", "priya.nair", exp); c != 200 || !strings.Contains(b, `"updated":1`) {
		t.Fatalf("import: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/dealers/files/bse-dealer?check=1", "priya.nair", ""); c != 200 || !strings.Contains(b, `"rows":1`) {
		t.Fatalf("check: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/dealers/files/bse-nism?ids=DL-0001", "priya.nair", ""); c != 200 || b != "A|AAAPA1111A|Asha||Dealer|EQD|NISM-1|01/31/2024|01/30/2027|\n" {
		t.Fatalf("nism file: %d %q", c, b)
	}
	if c, _ := call("GET", "/v1/dealers/files/nse-dealer", "priya.nair", ""); c != 422 {
		t.Fatalf("nse: %d", c)
	}
	if c, b := call("POST", "/v1/dealers/DL-0001/left", "priya.nair", `{"left_on":"2026-10-29"}`); c != 403 {
		t.Fatalf("maker left: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/dealers/DL-0001/left", "vikram.shah", `{"left_on":"2026-10-29","close_terminals":true}`); c != 200 || !strings.Contains(b, `"status":"Left"`) {
		t.Fatalf("left: %d %s", c, b)
	}
}
