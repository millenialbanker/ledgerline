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

func TestUILayoutRoutes(t *testing.T) {
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
	s.uiLayoutRoutes() // registering twice is harmless
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
	if c, b := call("PUT", "/v1/ui/policy", "priya.nair", `{"default":"inbox"}`); c != 403 {
		t.Fatalf("maker sets the firm's layout: %d %s", c, b)
	}
	if c, b := call("PUT", "/v1/ui/policy", "admin", `{"default":"inbox","staff_choose":true}`); c != 200 || !strings.Contains(b, `"default": "inbox"`) {
		t.Fatalf("admin sets the firm's layout: %d %s", c, b)
	}
	// /v1/me carries what applies, so the console draws it at once.
	c, b := call("GET", "/v1/me", "farhan.khan", "")
	var me struct {
		UI ops.UIEffective `json:"ui"`
	}
	if c != 200 || json.Unmarshal([]byte(b), &me) != nil || me.UI.Layout != ops.LayoutInbox || !me.UI.CanChoose {
		t.Fatalf("me: %d %s", c, b)
	}
	if c, b := call("PUT", "/v1/me/ui", "farhan.khan", `{"layout":"grid","density":"compact"}`); c != 200 || !strings.Contains(b, `"source": "you"`) {
		t.Fatalf("own layout: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/me/ui", "farhan.khan", ""); c != 200 || !strings.Contains(b, `"layout": "grid"`) || !strings.Contains(b, `"density": "compact"`) {
		t.Fatalf("read back: %d %s", c, b)
	}
	if c, b := call("PUT", "/v1/me/ui", "farhan.khan", `{"layout":"sideways"}`); c != 400 && c != 422 {
		t.Fatalf("bad layout: %d %s", c, b)
	}
	// Branch staff reach their own choices.
	if c, b := call("PUT", "/v1/me/ui", "meera.iyer", `{"layout":"inbox"}`); c != 200 {
		t.Fatalf("branch staff: %d %s", c, b)
	}
	if c, b := call("PUT", "/v1/me/ui", "meera.iyer", `{"layout":"control"}`); c == 200 {
		t.Fatalf("branch staff on the control room: %d %s", c, b)
	}
}
