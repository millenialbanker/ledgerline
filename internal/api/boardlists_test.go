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

// The routes for copying and moving lists, watching, collapsing, turning
// checklist items into cards, members and joining. Made-up data only.
func TestBoardListRoutes(t *testing.T) {
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
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatalf("%s %s: %v %s", method, path, err, b)
			}
		}
		return res.StatusCode
	}
	b, _ := o.CreateBoard(ctx, "demo", "admin", ops.BoardInput{Name: "Firm notices"})
	b2, _ := o.CreateBoard(ctx, "demo", "priya.nair", ops.BoardInput{Name: "Ops"})
	base, todo := "/v1/boards/"+b.ID, b.Lists[0].ID
	c, _ := o.AddCard(ctx, "demo", "admin", b.ID, ops.CardInput{List: todo, Title: "Holiday list"})

	// Watching and collapsing: anyone who can see the board.
	var w map[string]bool
	if code := call("POST", base+"/watch", "priya.nair", `{"on":true}`, &w); code != 200 || !w["watching"] {
		t.Fatalf("watch board %d %v", code, w)
	}
	var l ops.BoardList
	if code := call("POST", base+"/lists/"+todo+"/watch", "priya.nair", `{"on":true}`, &l); code != 200 || len(l.Watchers) != 1 {
		t.Fatalf("watch list %d %+v", code, l)
	}
	var col map[string][]string
	if code := call("POST", base+"/lists/"+todo+"/collapse", "priya.nair", `{"on":true}`, &col); code != 200 || len(col["collapsed"]) != 1 {
		t.Fatalf("collapse %d %v", code, col)
	}
	var v ops.BoardView
	if code := call("GET", base, "priya.nair", "", &v); code != 200 || len(v.Collapsed) != 1 || len(v.Board.Watchers) != 1 || v.CanJoin {
		t.Fatalf("view %d %+v", code, v.Collapsed)
	}

	// Joining, once the admin lets anyone at head office join.
	if code := call("POST", base+"/join", "priya.nair", `{}`, nil); code != 403 {
		t.Fatalf("joined without the setting: %d", code)
	}
	if code := call("PUT", base, "admin", `{"perms":{"comments":"observers","invite":"members","self_join":true}}`, nil); code != 200 {
		t.Fatalf("perms %d", code)
	}
	var jb ops.Board
	if code := call("POST", base+"/join", "priya.nair", `{}`, &jb); code != 200 || len(jb.Members) != 2 {
		t.Fatalf("join %d %+v", code, jb.Members)
	}
	// Members, now that all members may add them.
	ms := `{"members":[{"user":"admin","role":"admin"},{"user":"priya.nair","role":"member"},{"user":"farhan.khan","role":"observer"}]}`
	var mb ops.Board
	if code := call("PUT", base+"/members", "priya.nair", ms, &mb); code != 200 || len(mb.Members) != 3 {
		t.Fatalf("members %d %+v", code, mb.Members)
	}
	if code := call("PUT", base+"/members", "farhan.khan", ms, nil); code != 403 {
		t.Fatalf("an observer set members: %d", code)
	}

	// A checklist item becomes a card.
	c, _ = o.EditChecklist(ctx, "demo", "admin", b.ID, c.ID, ops.ChecklistOp{Op: "add_list", Name: "Steps"})
	c, _ = o.EditChecklist(ctx, "demo", "admin", b.ID, c.ID, ops.ChecklistOp{Op: "add_item", Checklist: c.Checklists[0].ID, Name: "Ask the exchanges"})
	var cv map[string]ops.Card
	body := `{"checklist":"` + c.Checklists[0].ID + `","item":"` + c.Checklists[0].Items[0].ID + `"}`
	if code := call("POST", base+"/cards/"+c.ID+"/checklists/convert", "priya.nair", body, &cv); code != 200 || cv["new"].Title != "Ask the exchanges" || len(cv["card"].Checklists[0].Items) != 0 {
		t.Fatalf("convert %d %+v", code, cv)
	}

	// Copy the list on this board, then move it to another.
	var cp ops.ListCopy
	if code := call("POST", base+"/lists/"+todo+"/copy", "priya.nair", `{"name":"To do 2027"}`, &cp); code != 200 || cp.Cards != 2 || cp.List.Name != "To do 2027" {
		t.Fatalf("copy %d %+v", code, cp)
	}
	if code := call("POST", base+"/lists/"+todo+"/move", "priya.nair", `{"board":"`+b2.ID+`"}`, &cp); code != 200 || cp.Cards != 2 || cp.Board != b2.ID {
		t.Fatalf("move %d %+v", code, cp)
	}
	// An old link to a moved card says where it went.
	var e struct {
		Error struct {
			Details struct {
				MovedTo struct{ Board, Card string } `json:"moved_to"`
			} `json:"details"`
		} `json:"error"`
	}
	if code := call("GET", base+"/cards/"+c.ID, "priya.nair", "", &e); code != 404 || e.Error.Details.MovedTo.Board != b2.ID || e.Error.Details.MovedTo.Card == "" {
		t.Fatalf("old link %d %+v", code, e)
	}
	if code := call("GET", "/v1/boards/"+b2.ID+"/cards/"+e.Error.Details.MovedTo.Card, "priya.nair", "", nil); code != 200 {
		t.Fatalf("followed the old link: %d", code)
	}
}
