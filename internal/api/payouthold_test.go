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

func TestPayoutHoldAPI(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 9, 0, 0, 0, time.FixedZone("IST", 19800)) }
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	s.payoutHoldRoutes()
	s.payoutHoldRoutes() // registering twice is harmless
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
		return resp.StatusCode, string(b)
	}
	if c, b := call("PUT", "/v1/payout-holds/settings", "priya.nair", `{"enabled":true,"threshold_pct":"20","hold_pct":"80","basis":"net","segments":["CM","FO"],"mode":"propose","min_hold":"0","apply_at":"08:45","payout_pattern":"NSE CLEARING"}`); c != 403 {
		t.Fatalf("maker saved rules: %d %s", c, b)
	}
	if c, b := call("PUT", "/v1/payout-holds/settings", "anita.rao", `{"enabled":true,"threshold_pct":"20","hold_pct":"80","basis":"net","segments":["CM","FO"],"mode":"propose","min_hold":"0","apply_at":"08:45","payout_pattern":"NSE CLEARING"}`); c != 200 {
		t.Fatalf("rules: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/payout-holds/funds", "anita.rao", `{"date":"2026-10-16","segments":{"CM":"5000000","FO":"1200000"},"note":"N-MASS screen"}`); c != 200 {
		t.Fatalf("funds: %d %s", c, b)
	}
	c, b := call("GET", "/v1/payout-holds?date=2026-10-16", "anita.rao", "")
	var v ops.PayoutHoldView
	if c != 200 || json.Unmarshal([]byte(b), &v) != nil || v.Threshold.Fixed(2) != "1240000.00" || v.Settings.Mode != "propose" || v.OMSFormat != "generic" {
		t.Fatalf("screen: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/payout-holds/apply", "anita.rao", `{"date":"2026-10-16"}`); c != 200 {
		t.Fatalf("apply: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/payout-holds/file?date=2026-10-16&kind=oms-hold", "anita.rao", ""); c != 200 || !strings.HasPrefix(b, "TradeDate,ClientCode,Segment,Change,NewLimit") {
		t.Fatalf("oms file: %d %s", c, b)
	}
	if c, _ := call("POST", "/v1/payout-holds/payout", "farhan.khan", `{"date":"2026-10-16","settlements":[],"note":"bank portal"}`); c != 200 {
		t.Fatalf("payout: %d", c)
	}
}
