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
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

// The boards' views, Inbox, Planner and calendar feed routes, over HTTP.
func TestBoardPlannerAPI(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	o.Now = func() time.Time { return time.Date(2026, 11, 18, 11, 0, 0, 0, time.FixedZone("IST", 19800)) }
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	b, _ := o.CreateBoard(ctx, "demo", "priya.nair", ops.BoardInput{Name: "Close"})
	o.AddCard(ctx, "demo", "priya.nair", b.ID, ops.CardInput{List: b.Lists[0].ID, Title: "Post interest", Due: "2026-11-19T17:30", Members: []string{"priya.nair"}})
	mb, _ := o.CreateBoard(ctx, "demo", "meera.iyer", ops.BoardInput{Name: "Branch"})
	o.AddCard(ctx, "demo", "meera.iyer", mb.ID, ops.CardInput{List: mb.Lists[0].ID, Title: "Branch visit"})
	srv := httptest.NewServer(New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false))
	defer srv.Close()

	call := func(who, method, path string, body any, out any) int {
		t.Helper()
		var rd io.Reader
		if body != nil {
			j, _ := json.Marshal(body)
			rd = bytes.NewReader(j)
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
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}

	// All boards: branch staff reach the route and see only their own board.
	var xv ops.XCardsView
	if code := call("meera.iyer", "GET", "/v1/board-cards", nil, &xv); code != 200 || xv.Total != 1 || xv.Cards[0].BoardName != "Branch" {
		t.Fatalf("branch staff: %d %+v", code, xv)
	}
	if code := call("priya.nair", "GET", "/v1/board-cards?due=soon,later&limit=1&board="+b.ID, nil, &xv); code != 200 || xv.Total != 1 || xv.Limit != 1 || xv.Cards[0].Title != "Post interest" {
		t.Fatalf("priya filtered: %d %+v", code, xv)
	}
	if code := call("priya.nair", "GET", "/v1/board-cards?from=bad", nil, nil); code != 400 && code != 422 {
		t.Fatalf("a bad date: %d", code)
	}

	// Tiles and cards over time on a board.
	var tv struct{ Tiles []ops.DashTile }
	if code := call("priya.nair", "PUT", "/v1/boards/"+b.ID+"/views/tiles", map[string]any{"tiles": []map[string]string{{"data": "time", "chart": "line"}, {"data": "due", "chart": "pie"}}}, &tv); code != 200 || len(tv.Tiles) != 2 {
		t.Fatalf("tiles: %d %+v", code, tv)
	}
	if code := call("priya.nair", "PUT", "/v1/boards/"+b.ID+"/views/tiles", map[string]any{"tiles": []map[string]string{{"data": "time", "chart": "pie"}}}, nil); code == 200 {
		t.Fatal("a pie of cards over time accepted")
	}
	var ot ops.OverTimeView
	if code := call("priya.nair", "GET", "/v1/boards/"+b.ID+"/views/over-time", nil, &ot); code != 200 || len(ot.Weeks) != 12 || ot.Weeks[11].Added != 1 {
		t.Fatalf("over time: %d %+v", code, ot)
	}
	if code := call("meera.iyer", "GET", "/v1/boards/"+b.ID+"/views/over-time", nil, nil); code != 404 {
		t.Fatalf("branch staff read a head-office board: %d", code)
	}

	// The Inbox: made on first use, private.
	var iv ops.InboxView
	if code := call("priya.nair", "GET", "/v1/board-inbox", nil, &iv); code != 200 || iv.Board == "" {
		t.Fatalf("inbox: %d %+v", code, iv)
	}
	var c ops.Card
	if code := call("priya.nair", "POST", "/v1/board-inbox/cards", map[string]string{"title": "Ring the auditor"}, &c); code != 200 || c.Board != iv.Board {
		t.Fatalf("quick add: %d %+v", code, c)
	}
	if code := call("admin", "GET", "/v1/boards/"+iv.Board, nil, nil); code != 404 {
		t.Fatalf("admin opened priya's inbox: %d", code)
	}

	// The Planner: a block, moved; another person cannot touch it.
	var pb ops.PlanBlock
	if code := call("priya.nair", "POST", "/v1/board-planner/blocks", map[string]any{"board": iv.Board, "card": c.ID, "start": "2026-11-18T15:00"}, &pb); code != 200 || pb.Minutes != 30 {
		t.Fatalf("block: %d %+v", code, pb)
	}
	if code := call("priya.nair", "PATCH", "/v1/board-planner/blocks/"+pb.ID, map[string]any{"start": "2026-11-18T16:00", "minutes": 60}, &pb); code != 200 || pb.Start != "2026-11-18T16:00" || pb.Minutes != 60 {
		t.Fatalf("move: %d %+v", code, pb)
	}
	if code := call("farhan.khan", "PATCH", "/v1/board-planner/blocks/"+pb.ID, map[string]any{"minutes": 15}, nil); code != 404 {
		t.Fatalf("farhan moved priya's block: %d", code)
	}
	var pv ops.PlannerView
	if code := call("priya.nair", "GET", "/v1/board-planner?from=2026-11-16&days=7", nil, &pv); code != 200 || len(pv.Blocks) != 1 || len(pv.Inbox) != 1 {
		t.Fatalf("planner: %d %+v", code, pv)
	}

	// Calendar feeds: off until the firm's Admin switches them on.
	if code := call("priya.nair", "POST", "/v1/board-feeds", map[string]string{"scope": "mine"}, nil); code != 403 {
		t.Fatalf("feed while off: %d", code)
	}
	if code := call("priya.nair", "PUT", "/v1/board-feeds/firm", map[string]bool{"on": true}, nil); code != 403 {
		t.Fatalf("priya switched feeds on: %d", code)
	}
	if code := call("admin", "PUT", "/v1/board-feeds/firm", map[string]bool{"on": true}, nil); code != 200 {
		t.Fatalf("admin switched feeds on: %d", code)
	}
	var fr struct {
		Feed ops.BoardFeed
		Path string
		URL  string
	}
	if code := call("priya.nair", "POST", "/v1/board-feeds", map[string]string{"scope": "mine"}, &fr); code != 200 || !strings.HasPrefix(fr.Path, "/feeds/boards/demo/") || !strings.HasSuffix(fr.Path, ".ics") || !strings.HasSuffix(fr.URL, fr.Path) {
		t.Fatalf("feed: %d %+v", code, fr)
	}
	// The feed is read without a session.
	resp, err := http.Get(srv.URL + fr.Path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// (The Inbox card has no due date, so it is not in the feed.)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/calendar") || !strings.Contains(string(body), "SUMMARY:Post interest\r\n") || strings.Contains(string(body), "Ring the auditor") {
		t.Fatalf("feed read: %d %q\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if code := call("priya.nair", "POST", "/v1/board-feeds/"+fr.Feed.ID+"/revoke", nil, nil); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
	if resp, _ = http.Get(srv.URL + fr.Path); resp.StatusCode != 404 {
		t.Fatalf("revoked feed: %d", resp.StatusCode)
	}
	resp.Body.Close()
	// Guessing addresses is slowed down.
	blocked := false
	for i := 0; i < 40 && !blocked; i++ {
		r, _ := http.Get(srv.URL + "/feeds/boards/demo/" + strings.Repeat("x", 43) + ".ics")
		blocked = r.StatusCode == 429
		r.Body.Close()
	}
	if !blocked {
		t.Fatal("unknown feed addresses are not rate limited")
	}
}
