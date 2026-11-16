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

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

func TestLedgerlineBoardsAPI(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	if err := o.SeedDev(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
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
	if code, b := call("POST", "/v1/boards/ledgerline", "priya.nair", `{"sources":["work"]}`); code != 403 {
		t.Fatalf("maker set up boards: %d %s", code, b)
	}
	code, b := call("POST", "/v1/boards/ledgerline", "admin", `{"sources":["work","leads"]}`)
	var set struct {
		Boards []ops.LedgerlineSetup `json:"boards"`
	}
	if code != 200 || json.Unmarshal([]byte(b), &set) != nil || len(set.Boards) != 2 || !set.Boards[0].Created {
		t.Fatalf("set up: %d %s", code, b)
	}
	var home ops.LedgerlineBoardsView
	if code, b := call("GET", "/v1/boards/ledgerline", "priya.nair", ""); code != 200 || json.Unmarshal([]byte(b), &home) != nil || len(home.Boards) != 2 || home.CanSetUp {
		t.Fatalf("home: %d %s", code, b)
	}
	// Branch staff reach the link search (it shows them only their own clients),
	// and do not see the Ledgerline boards.
	if code, b := call("GET", "/v1/boards/link-search?q=zz", "meera.iyer", ""); code != 200 || strings.TrimSpace(b) != "[]" {
		t.Fatalf("branch link search: %d %s", code, b)
	}
	if code, b := call("GET", "/v1/boards/ledgerline", "meera.iyer", ""); code != 200 || json.Unmarshal([]byte(b), &home) != nil || len(home.Boards) != 0 {
		t.Fatalf("branch home: %d %s", code, b)
	}
	if code, _ := call("GET", "/v1/boards/link?kind=client&id=NOPE", "priya.nair", ""); code != 404 {
		t.Fatalf("unknown record: %d", code)
	}
	if code, _ := call("GET", "/v1/boards/link?kind=nope&id=x", "priya.nair", ""); code != 422 && code != 400 {
		t.Fatalf("unknown kind: %d", code)
	}
}
