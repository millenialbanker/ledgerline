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

// Settings → Firm practices over HTTP: the questionnaire, an answer, the
// approval, and the general settings screen refusing a money-affecting
// practice.
func TestFirmPracticesAPI(t *testing.T) {
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
	if st, m := do("GET", "/v1/firm-practices", "admin", nil); st != 200 || len(m["questions"].([]any)) < 10 {
		t.Fatalf("view %d %v", st, m)
	}
	if st, _ := do("GET", "/v1/firm-practices", "priya.nair", nil); st != 403 {
		t.Fatalf("a maker saw firm practices: %d", st)
	}
	if st, m := do("POST", "/v1/firm-practices/mcx-apart", "admin", map[string]any{"option": "on"}); st != 200 || m["pending"] == nil {
		t.Fatalf("answer %d %v", st, m)
	}
	if st, _ := do("POST", "/v1/firm-practices/mcx-apart/decide", "admin", map[string]any{"approve": true}); st != 403 {
		t.Fatalf("the proposer approved: %d", st)
	}
	if st, m := do("POST", "/v1/firm-practices/mcx-apart/decide", "vikram.shah", map[string]any{"approve": true}); st != 200 || m["current"] != "on" {
		t.Fatalf("approve %d %v", st, m)
	}
	if set, _ := o.Settings(ctx, "demo"); !set.MCXMoneyApart {
		t.Fatal("not applied")
	}
	// The general settings screen cannot change it behind the approval.
	if st, _ := do("PUT", "/v1/settings", "admin", map[string]any{"mcx_money_apart": false}); st != 409 {
		t.Fatalf("settings changed a money practice: %d", st)
	}
	if st, _ := do("PUT", "/v1/settings", "admin", map[string]any{"bank_auth_forms": "letter"}); st != 409 {
		t.Fatalf("settings changed bank authorisation: %d", st)
	}
	if st, _ := do("PUT", "/v1/settings", "admin", map[string]any{"warn_util_pct": 75}); st != 200 {
		t.Fatalf("other settings refused: %d", st)
	}
	bs, _ := o.BrokerageSetup(ctx, "demo")
	bs.AuctionRate = "scrip"
	if st, _ := do("POST", "/v1/brokerage/setup", "farhan.khan", bs); st != 409 {
		t.Fatalf("brokerage setup changed the auction rule: %d", st)
	}
	bs.AuctionRate = ""
	if st, _ := do("POST", "/v1/brokerage/setup", "farhan.khan", bs); st != 200 {
		t.Fatalf("brokerage setup refused: %d", st)
	}
	if st, m := do("GET", "/v1/go-live", "vikram.shah", nil); st != 200 || len(m["items"].([]any)) == 0 {
		t.Fatalf("go-live %d %v", st, m)
	}
}
