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

// TestBoardIOAPI: the Trello import, exports, collections, recently viewed,
// stickers, a card's email address and the AI on cards, over HTTP.
func TestBoardIOAPI(t *testing.T) {
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
	call := func(method, path, who, ctype string, body []byte, out any) (int, http.Header, []byte) {
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
		if out != nil && res.StatusCode == 200 {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatalf("%s %s: %v\n%s", method, path, err, b)
			}
		}
		return res.StatusCode, res.Header, b
	}
	js := "application/json"

	// Import: a preview, then the board.
	raw, err := os.ReadFile("../ops/testdata/trello-board.json")
	if err != nil {
		t.Fatal(err)
	}
	var p ops.TrelloPreview
	if code, _, b := call("POST", "/v1/board-imports/trello/preview", "priya.nair", js, raw, &p); code != 200 || p.Cards != 5 || len(p.Members) != 3 {
		t.Fatalf("preview %d %s", code, b)
	}
	if code, _, _ := call("POST", "/v1/board-imports/trello/preview", "priya.nair", js, []byte(`{"name":"not a board"}`), nil); code != 422 {
		t.Fatalf("not trello %d", code)
	}
	if code, _, _ := call("POST", "/v1/board-imports/trello/preview", "priya.nair", js, bytes.Repeat([]byte(" "), ops.BoardImportMax+10), nil); code != 413 {
		t.Fatalf("too large %d", code)
	}
	env := []byte(`{"name":"From Trello","members":{"6c0000000000000000000002":"farhan.khan"},"trello":` + string(raw) + `}`)
	var res ops.TrelloImportResult
	if code, _, b := call("POST", "/v1/board-imports/trello", "priya.nair", js, env, &res); code != 200 || res.Board.Name != "From Trello" || res.Cards != 5 || res.Board.Visibility != "private" {
		t.Fatalf("import %d %s", code, b)
	}
	bid := res.Board.ID

	// Exports: CSV for members, JSON for admins, with the right headers.
	code, h, body := call("GET", "/v1/boards/"+bid+"/export?format=csv", "farhan.khan", "", nil, nil)
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "text/csv") || !strings.Contains(h.Get("Content-Disposition"), `attachment; filename="From-Trello.csv"`) || !bytes.Contains(body, []byte("Collect bank proof")) {
		t.Fatalf("csv %d %v", code, h)
	}
	if code, _, _ := call("GET", "/v1/boards/"+bid+"/export?format=json", "farhan.khan", "", nil, nil); code != 403 {
		t.Fatalf("member json %d", code)
	}
	var ex ops.BoardExport
	if code, h, _ := call("GET", "/v1/boards/"+bid+"/export?format=json", "priya.nair", "", nil, &ex); code != 200 || ex.Format != "ledgerline-board" || len(ex.Cards) != 5 || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("json %d %+v", code, ex.Format)
	}
	if code, _, _ := call("GET", "/v1/boards/"+bid+"/export?format=xml", "priya.nair", "", nil, nil); code != 400 {
		t.Fatalf("xml %d", code)
	}
	if code, _, _ := call("GET", "/v1/boards/"+bid+"/export?format=csv", "vikram.shah", "", nil, nil); code != 404 {
		t.Fatalf("no access %d", code)
	}
	card := ex.Cards[0]
	var ce ops.CardExport
	if code, h, _ := call("GET", "/v1/boards/"+bid+"/cards/"+card.ID+"/export", "farhan.khan", "", nil, &ce); code != 200 || ce.Card.ID != card.ID || !strings.Contains(h.Get("Content-Disposition"), ".json") {
		t.Fatalf("card json %d", code)
	}
	if code, _, _ := call("POST", "/v1/boards/"+bid+"/printed", "farhan.khan", js, []byte(`{"card":"`+card.ID+`"}`), nil); code != 200 {
		t.Fatalf("printed %d", code)
	}

	// Recently viewed and collections.
	if code, _, _ := call("POST", "/v1/boards/"+bid+"/viewed", "farhan.khan", js, []byte(`{}`), nil); code != 200 {
		t.Fatalf("viewed %d", code)
	}
	var rec []ops.BoardSummary
	if code, _, _ := call("GET", "/v1/boards/recent", "farhan.khan", "", nil, &rec); code != 200 || len(rec) != 1 || rec[0].ID != bid {
		t.Fatalf("recent %d %+v", code, rec)
	}
	var coll ops.BoardCollectionView
	if code, _, _ := call("POST", "/v1/board-collections", "farhan.khan", js, []byte(`{"name":"Settlements"}`), &coll); code != 200 || coll.ID == "" {
		t.Fatalf("collection %d", code)
	}
	if code, _, _ := call("POST", "/v1/board-collections/"+coll.ID+"/boards", "farhan.khan", js, []byte(`{"board":"`+bid+`","on":true}`), nil); code != 403 {
		t.Fatalf("member added the board %d", code)
	}
	if code, _, _ := call("POST", "/v1/board-collections/"+coll.ID+"/boards", "priya.nair", js, []byte(`{"board":"`+bid+`","on":true}`), &coll); code != 200 || len(coll.Boards) != 1 {
		t.Fatalf("admin added the board %d", code)
	}
	if code, _, _ := call("PUT", "/v1/board-collections/"+coll.ID, "priya.nair", js, []byte(`{"name":"Settlement work"}`), &coll); code != 200 || coll.Name != "Settlement work" {
		t.Fatalf("rename %d", code)
	}
	var colls []ops.BoardCollectionView
	if code, _, _ := call("GET", "/v1/board-collections", "meera.iyer", "", nil, &colls); code != 200 || len(colls) != 0 {
		t.Fatalf("branch collections %d %d", code, len(colls))
	}
	if code, _, _ := call("POST", "/v1/board-collections/"+coll.ID+"/delete", "priya.nair", js, []byte(`{}`), nil); code != 403 {
		t.Fatalf("delete by someone else %d", code)
	}
	if code, _, _ := call("POST", "/v1/board-collections/"+coll.ID+"/delete", "farhan.khan", js, []byte(`{}`), nil); code != 200 {
		t.Fatalf("delete %d", code)
	}

	// Stickers.
	var sc ops.Card
	if code, _, _ := call("POST", "/v1/boards/"+bid+"/cards/"+card.ID+"/stickers", "farhan.khan", js, []byte(`{"add":"rocket"}`), &sc); code != 200 || len(sc.Stickers) != 1 {
		t.Fatalf("sticker %d %v", code, sc.Stickers)
	}
	if code, _, _ := call("POST", "/v1/boards/"+bid+"/cards/"+card.ID+"/stickers", "farhan.khan", js, []byte(`{"add":"ghost"}`), nil); code != 422 {
		t.Fatalf("unknown sticker %d", code)
	}

	// A card's address: off until the board's is on; mail goes to the board route.
	var ce2 struct {
		On   bool   `json:"on"`
		Path string `json:"path"`
		Why  string `json:"why"`
	}
	if code, _, _ := call("GET", "/v1/boards/"+bid+"/cards/"+card.ID+"/email", "farhan.khan", "", nil, &ce2); code != 200 || ce2.On || ce2.Why == "" {
		t.Fatalf("card email off %d %+v", code, ce2)
	}
	if code, _, _ := call("POST", "/v1/boards/"+bid+"/email", "priya.nair", js, []byte(`{"on":true}`), nil); code != 200 {
		t.Fatalf("email on %d", code)
	}
	if code, _, _ := call("GET", "/v1/boards/"+bid+"/cards/"+card.ID+"/email", "farhan.khan", "", nil, &ce2); code != 200 || !ce2.On || !strings.HasPrefix(ce2.Path, "/inbound/boards/demo/c") {
		t.Fatalf("card email %d %+v", code, ce2)
	}
	var in struct {
		Card string `json:"card"`
	}
	if code, _, b := call("POST", ce2.Path, "", "message/rfc822", []byte(strings.Replace(bxMail, "%ID%", "card-1", 1)), &in); code != 200 || in.Card != card.ID {
		t.Fatalf("card mail %d %s", code, b)
	}
	if code, _, _ := call("POST", ce2.Path[:len(ce2.Path)-4]+"0000", "", "message/rfc822", []byte(strings.Replace(bxMail, "%ID%", "card-2", 1)), nil); code != 404 {
		t.Fatalf("forged card address %d", code)
	}

	// AI on cards: no model on this server, so off with the reason.
	var st ops.CardAIState
	if code, _, _ := call("GET", "/v1/boards/"+bid+"/cards/"+card.ID+"/ai", "farhan.khan", "", nil, &st); code != 200 || st.On || st.Why == "" {
		t.Fatalf("ai state %d %+v", code, st)
	}
	if code, _, b := call("POST", "/v1/boards/"+bid+"/cards/"+card.ID+"/ai/summary", "farhan.khan", js, []byte(`{}`), nil); code != 422 || !bytes.Contains(b, []byte("No AI model")) {
		t.Fatalf("ai summary %d %s", code, b)
	}
}
