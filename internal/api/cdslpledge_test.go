package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
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

func TestCDSLPledgeRoutes(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 11, 0, 0, 0, time.FixedZone("IST", 19800)) }
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
	if err := o.PutUser(ctx, "demo", "admin", ops.User{ID: "client.bh1001", Name: "Aarav Sharma", Role: ops.RoleClient, Active: true, Clients: []string{"BH1001"}}); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	s.cdslPledgeRoutes()
	s.cdslPledgeRoutes() // registering twice is harmless
	srv := httptest.NewServer(s)
	defer srv.Close()
	call := func(method, path, who, ctype, body string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if who != "" {
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("X-Actor", who)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	if r, b := call("GET", "/v1/cdsl-pledges?date=2026-10-16", "priya.nair", "", ""); r.StatusCode != 200 || !strings.Contains(b, `"connected": false`) {
		t.Fatalf("screen: %d %s", r.StatusCode, b)
	}
	if r, b := call("GET", "/v1/cdsl-epi?date=2026-10-16", "farhan.khan", "", ""); r.StatusCode != 200 || !strings.Contains(b, `"sells"`) {
		t.Fatalf("epi: %d %s", r.StatusCode, b)
	}
	if r, _ := call("GET", "/v1/cdsl-pledges?date=2026-10-16", "client.bh1001", "", ""); r.StatusCode != 403 {
		t.Fatalf("a client on the staff screen: %d", r.StatusCode)
	}
	if r, b := call("GET", "/v1/portal/pledges?ucc=BH1001", "client.bh1001", "", ""); r.StatusCode != 200 || !strings.Contains(b, `"ucc": "BH1001"`) {
		t.Fatalf("portal: %d %s", r.StatusCode, b)
	}
	if r, _ := call("POST", "/v1/portal/pledges/confirm", "client.bh1001", "application/json", `{"ucc":"BH1001"}`); r.StatusCode < 400 {
		t.Fatalf("nothing to confirm: %d", r.StatusCode)
	}
	// Pages CDSL and the client reach without signing in.
	r, b := call("GET", "/hooks/demo/cdsl/pledge/not-a-real-token-0123456789", "", "", "")
	if r.StatusCode != 400 || !strings.Contains(r.Header.Get("Content-Security-Policy"), "form-action 'none'") || !strings.Contains(b, "not valid") && !strings.Contains(b, "expired") {
		t.Fatalf("bad token: %d %v %s", r.StatusCode, r.Header, b)
	}
	r, b = call("POST", "/hooks/demo/cdsl/pledge-return", "", "application/x-www-form-urlencoded", neturl.Values{"reqid": {"LL1"}, "pledgeresdtls": {"junk"}}.Encode())
	if r.StatusCode != 200 || !strings.Contains(b, "could not match") {
		t.Fatalf("unknown return: %d %s", r.StatusCode, b)
	}
	if r, _ := call("POST", "/hooks/demo/cdsl/epi", "", "application/json", `{"encryptresdtl":"junk"}`); r.StatusCode < 400 {
		t.Fatalf("epi hook without a connection: %d", r.StatusCode)
	}
}
