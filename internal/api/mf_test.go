package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

func mfServer(t *testing.T) *httptest.Server {
	t.Helper()
	ops.PasswordIterations = 1000
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	if err := o.SeedDev(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	s.mfRoutes()
	s.mfRoutes() // safe twice, whether or not opsRoutes already calls it
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv
}

func mfCall(t *testing.T, srv *httptest.Server, method, path, as string, body []byte, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Actor", as)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, path, err, b)
		}
	}
	return res.StatusCode
}

const mfNAV = "Scheme Code;ISIN Div Payout/ ISIN Growth;ISIN Div Reinvestment;Scheme Name;Net Asset Value;Date\n\nOpen Ended Schemes(Equity Scheme - Large Cap Fund)\n\nSample Mutual Fund\n\n" +
	"100001;INF000A01011;-;Sample Bluechip Fund - Regular Plan - Growth;52.4100;28-Oct-2026\n100002;INF000A01029;-;Sample Bluechip Fund - Direct Plan - Growth;58.1200;28-Oct-2026\n"

func TestMFRoutes(t *testing.T) {
	srv := mfServer(t)
	var st map[string]any
	if code := mfCall(t, srv, "GET", "/v1/mf/status", "admin", nil, &st); code != 200 || len(st["platforms"].([]any)) != 2 {
		t.Fatalf("status %d %v", code, st)
	}
	set := []byte(`{"route":"Both","arn":"ARN-99999","euins":[{"user":"priya.nair","euin":"E123456"}],"direct_basis":"` + ops.MFDirectBases[0] + `","platforms":["NSE"],"nav_fetch":false}`)
	if code := mfCall(t, srv, "PUT", "/v1/mf/settings", "priya.nair", set, nil); code != 403 {
		t.Fatalf("maker saved MF settings: %d", code)
	}
	var saved ops.MFSettings
	if code := mfCall(t, srv, "PUT", "/v1/mf/settings", "vikram.shah", set, &saved); code != 200 || saved.Route != "Both" || saved.DirectConfirmedBy != "vikram.shah" {
		t.Fatalf("settings %d %+v", code, saved)
	}
	var nav ops.MFNAVState
	if code := mfCall(t, srv, "POST", "/v1/mf/nav?name=NAVAll.txt", "priya.nair", []byte(mfNAV), &nav); code != 200 || nav.Schemes != 2 {
		t.Fatalf("NAV upload %d %+v", code, nav)
	}
	var ss []ops.MFScheme
	if code := mfCall(t, srv, "GET", "/v1/mf/schemes?q=bluechip&plan=Direct", "priya.nair", nil, &ss); code != 200 || len(ss) != 1 {
		t.Fatalf("schemes %d %+v", code, ss)
	}
	codes := "Scheme Code,ISIN\nSBLUE-GR,INF000A01011\n"
	var imp ops.MFImportResult
	if code := mfCall(t, srv, "POST", `/v1/mf/import?kind=scheme-codes&platform=NSE&columns={"code":"Scheme%20Code"}`, "priya.nair", []byte(codes), &imp); code != 200 || imp.Applied != 1 {
		t.Fatalf("import %d %+v", code, imp)
	}
	var e map[string]map[string]string
	if code := mfCall(t, srv, "POST", "/v1/mf/orders", "priya.nair", []byte(`{"ucc":"NOBODY","platform":"NSE","type":"Purchase","isin":"INF000A01011","amount":"1000"}`), &e); code != 404 {
		t.Fatalf("order for an unknown client: %d %v", code, e)
	}
	if code := mfCall(t, srv, "GET", "/v1/mf", "admin", nil, nil); code != 200 {
		t.Fatalf("GET /v1/mf: %d", code)
	}
	for _, p := range []string{"/v1/mf/orders", "/v1/mf/sips", "/v1/mf/mandates", "/v1/mf/units", "/v1/mf/commission?month=2026-10"} {
		if code := mfCall(t, srv, "GET", p, "admin", nil, nil); code != 200 {
			t.Errorf("%s: %d", p, code)
		}
	}
	// The portal pages are open to client logins and are for them only.
	for _, p := range []string{"GET /v1/portal/mf", "POST /v1/portal/mf/orders", "POST /v1/portal/mf/sips", "POST /v1/portal/mf/profile"} {
		if !clientRoutes[p] {
			t.Errorf("%s not open to client logins", p)
		}
	}
	if code := mfCall(t, srv, "GET", "/v1/portal/mf", "admin", nil, &e); code != 403 || !strings.Contains(e["error"]["message"], "client portal") {
		t.Fatalf("staff on the portal page: %d %v", code, e)
	}
}
