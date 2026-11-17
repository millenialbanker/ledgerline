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
	"strings"
	"testing"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

func TestBoardExtrasAPI(t *testing.T) {
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
	srv := httptest.NewServer(New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false))
	defer srv.Close()
	call := func(method, path, who, ctype string, body []byte, out any) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		if who != "" {
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("X-Actor", who)
		}
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
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

	// The template gallery and a board made from one.
	var g []ops.BoardTemplateInfo
	if code := call("GET", "/v1/board-templates", "priya.nair", "", nil, &g); code != 200 || len(g) < 7 {
		t.Fatalf("gallery %d %d", code, len(g))
	}
	var b ops.Board
	if code := call("POST", "/v1/board-templates/bt-onboarding/use", "priya.nair", "application/json", []byte(`{"name":"Onboarding: Mumbai","visibility":"firm","cards":true}`), &b); code != 200 || b.Name != "Onboarding: Mumbai" {
		t.Fatalf("use %d %+v", code, b)
	}

	// Email-to-board was taken out: nothing answers at its old addresses.
	if code := call("POST", "/v1/boards/"+b.ID+"/email", "priya.nair", "application/json", []byte(`{"on":true}`), nil); code != 404 && code != 405 {
		t.Fatalf("email-to-board route still there: %d", code)
	}
	if code := call("POST", "/inbound/boards/demo/"+strings.Repeat("ab", 32), "", "message/rfc822", []byte("From: a@example.com\r\n\r\nhi"), nil); code == 200 {
		t.Fatal("inbound mail was taken")
	}
}
