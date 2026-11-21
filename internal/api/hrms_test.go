package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

// HRMS over HTTP: with the module off its routes answer 404 (the firm does
// not see a module it has not switched on), Payroll stays 404 until it is
// on too, only an Admin switches modules, and a person's self-service
// shows only their own record. Made-up people only.
func TestHRMSModuleRoutesAndSelfService(t *testing.T) {
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
	if err := o.PutUser(ctx, "demo", "admin", ops.User{ID: "hema.hr", Name: "Hema (HR)", Role: ops.RoleHR, Active: true}); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	srv := httptest.NewServer(s)
	defer srv.Close()
	do := func(method, path, who string, body any) (int, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Actor", who)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	// Off: every HRMS and Payroll route is 404, the kiosk's too.
	for _, c := range [][3]string{{"GET", "/v1/hr/staff", "admin"}, {"GET", "/v1/hr/me", "priya.nair"}, {"POST", "/v1/hr/attendance/punch", "priya.nair"},
		{"GET", "/v1/hr/leave", "priya.nair"}, {"GET", "/v1/payroll", "admin"}, {"POST", "/kiosk-api/demo/punch", ""}} {
		if st, m := do(c[0], c[1], c[2], map[string]any{}); st != 404 {
			t.Fatalf("%s %s with the module off: %d %v", c[0], c[1], st, m)
		}
	}
	if st, _ := do("GET", "/v1/modules", "priya.nair", nil); st != 200 {
		t.Fatalf("modules view %d", st)
	}
	if st, _ := do("PUT", "/v1/modules", "priya.nair", map[string]any{"hrms": true}); st != 403 {
		t.Fatalf("a maker switched HRMS on: %d", st)
	}
	if st, m := do("PUT", "/v1/modules", "admin", map[string]any{"hrms": true}); st != 200 || m["hrms"] != true || m["payroll"] != false {
		t.Fatalf("switch on %d %v", st, m)
	}
	if st, _ := do("GET", "/v1/payroll", "admin", nil); st != 404 {
		t.Fatalf("payroll without its module: %d", st)
	}
	// Two made-up people, each linked to a login.
	codes := map[string]string{}
	for _, p := range []struct{ name, user string }{{"Asha Test", "priya.nair"}, {"Bimal Test", "farhan.khan"}} {
		st, m := do("POST", "/v1/hr/staff", "hema.hr", map[string]any{"name": p.name, "joined_on": "2025-01-01", "user": p.user, "pan": "AAAPT1234T"})
		if st != 200 {
			t.Fatalf("add %s: %d %v", p.name, st, m)
		}
		codes[p.user] = m["code"].(string)
	}
	// Self-service: own record only, masked.
	st, m := do("GET", "/v1/hr/me", "priya.nair", nil)
	prof, _ := m["profile"].(map[string]any)
	if st != 200 || prof["code"] != codes["priya.nair"] || prof["pan"] != "AAAPTXXXXT" {
		t.Fatalf("self-service %d %v", st, m)
	}
	if st, _ := do("GET", "/v1/hr/staff/"+codes["farhan.khan"], "priya.nair", nil); st != 404 {
		t.Fatalf("saw another person's record: %d", st)
	}
	if st, m := do("GET", "/v1/hr/staff", "priya.nair", nil); st != 200 || len(m["employees"].([]any)) != 1 {
		t.Fatalf("staff list for self %d %v", st, m)
	}
	if st, _ := do("POST", "/v1/hr/staff/"+codes["farhan.khan"]+"/reveal", "priya.nair", map[string]any{"field": "pan", "reason": "looking around"}); st != 403 {
		t.Fatalf("a maker revealed a PAN: %d", st)
	}
	// Switched off again: gone, records kept.
	if st, _ := do("PUT", "/v1/modules", "admin", map[string]any{"hrms": false}); st != 200 {
		t.Fatal("switch off")
	}
	if st, _ := do("GET", "/v1/hr/staff", "admin", nil); st != 404 {
		t.Fatalf("staff after switching off: %d", st)
	}
	if st, _ := do("PUT", "/v1/modules", "admin", map[string]any{"hrms": true, "payroll": true}); st != 200 {
		t.Fatal("switch on with payroll")
	}
	if st, m := do("GET", "/v1/hr/staff", "admin", nil); st != 200 || len(m["employees"].([]any)) != 2 {
		t.Fatalf("records not kept: %d %v", st, m)
	}
	if st, _ := do("GET", "/v1/payroll", "admin", nil); st != 200 {
		t.Fatalf("payroll on: %d", st)
	}
}
