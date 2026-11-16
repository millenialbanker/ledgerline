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

// The card repeater's and board email settings' routes.
func TestBoardRepeatAndNotifyRoutes(t *testing.T) {
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
	call := func(method, path, who, body string, out any) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Actor", who)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if out != nil && res.StatusCode < 300 {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatalf("%s %s: %v %s", method, path, err, b)
			}
		}
		return res.StatusCode
	}
	b, err := o.CreateBoard(ctx, "demo", "priya.nair", ops.BoardInput{Name: "Filings"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := o.AddCard(ctx, "demo", "priya.nair", b.ID, ops.CardInput{List: b.Lists[0].ID, Title: "Monthly surveillance report"})
	cp := "/v1/boards/" + b.ID + "/cards/" + c.ID + "/repeat"
	monthly := `{"every":"monthly","which":"last_working","at":"10:00","due":true,"due_days":5}`

	// Preview, then save; observers read the board's repeats but do not set them.
	var v ops.CardRepeatView
	if code := call("POST", cp+"/preview", "priya.nair", monthly, &v); code != 200 || v.Text != "Repeats every month on the last working day" || len(v.Upcoming) != 3 {
		t.Fatalf("preview %d %+v", code, v)
	}
	if code := call("PUT", cp, "farhan.khan", monthly, nil); code != 403 {
		t.Fatalf("an observer set a repeat: %d", code)
	}
	if code := call("PUT", cp, "priya.nair", `{"every":"monthly","which":"day","day":40,"at":"10:00"}`, nil); code != 422 && code != 400 {
		t.Fatalf("day 40: %d", code)
	}
	if code := call("PUT", cp, "priya.nair", monthly, &v); code != 200 || v.Next == "" || v.Card != c.ID {
		t.Fatalf("saved %d %+v", code, v)
	}
	var rs []ops.CardRepeatView
	if code := call("GET", "/v1/boards/"+b.ID+"/repeats", "farhan.khan", "", &rs); code != 200 || len(rs) != 1 || rs[0].Where == "" {
		t.Fatalf("repeats %d %+v", code, rs)
	}
	if code := call("POST", cp+"/stop", "priya.nair", `{}`, nil); code != 200 {
		t.Fatalf("stop %d", code)
	}
	if code := call("GET", "/v1/boards/"+b.ID+"/repeats", "priya.nair", "", &rs); code != 200 || len(rs) != 0 {
		t.Fatalf("after stop %d %+v", code, rs)
	}
	if code := call("POST", cp+"/stop", "priya.nair", `{}`, nil); code != 404 {
		t.Fatalf("stopped twice: %d", code)
	}

	// Email settings: defaults, an address confirmed with its code, how often, a pause.
	var m ops.BoardMailView
	if code := call("GET", "/v1/board-notify", "farhan.khan", "", &m); code != 200 || m.Every != "daily" || m.At != "09:00" || m.Email != "" || m.CanFirm {
		t.Fatalf("defaults %d %+v", code, m)
	}
	if code := call("POST", "/v1/board-notify/address", "farhan.khan", `{"email":"farhan@bhairav.example"}`, &m); code != 200 || m.Pending != "farhan@bhairav.example" || m.DevCode == "" {
		t.Fatalf("code %d %+v", code, m)
	}
	if code := call("POST", "/v1/board-notify/confirm", "farhan.khan", `{"code":"`+m.DevCode+`"}`, &m); code != 200 || m.Email != "farhan@bhairav.example" {
		t.Fatalf("confirm %d %+v", code, m)
	}
	if code := call("PUT", "/v1/board-notify", "farhan.khan", `{"every":"now","kinds":["mention","comment"]}`, &m); code != 200 || m.Every != "now" || strings.Join(m.Kinds, ",") != "mention,comment" {
		t.Fatalf("saved %d %+v", code, m)
	}
	if code := call("POST", "/v1/board-notify/pause", "farhan.khan", `{"days":7}`, &m); code != 200 || m.PausedUntil == "" {
		t.Fatalf("pause %d %+v", code, m)
	}
	// Each person's own: someone else's settings are untouched.
	if code := call("GET", "/v1/board-notify", "anita.rao", "", &m); code != 200 || m.Email != "" {
		t.Fatalf("another person's %d %+v", code, m)
	}
	// Branch staff have board email too.
	if code := call("GET", "/v1/board-notify", "meera.iyer", "", &m); code != 200 {
		t.Fatalf("branch staff %d", code)
	}
	// The firm's switch is for Admins.
	if code := call("POST", "/v1/board-notify/firm", "farhan.khan", `{"off":true}`, nil); code != 403 {
		t.Fatalf("a checker switched the firm off: %d", code)
	}
	if code := call("POST", "/v1/board-notify/firm", "admin", `{"off":true}`, &m); code != 200 || !m.FirmOff || !m.CanFirm {
		t.Fatalf("firm off %d %+v", code, m)
	}
}
