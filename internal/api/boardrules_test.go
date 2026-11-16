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

func TestBoardAutomationRoutes(t *testing.T) {
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
	b, err := o.CreateBoard(ctx, "demo", "priya.nair", ops.BoardInput{Name: "Ops"})
	if err != nil {
		t.Fatal(err)
	}
	base := "/v1/boards/" + b.ID + "/automation"
	todo, done := b.Lists[0].ID, b.Lists[2].ID

	// A firm-wide board: others observe it, so they read but do not change its automation.
	var v ops.BoardAutoView
	if code := call("GET", base, "farhan.khan", "", &v); code != 200 || v.CanEdit || v.Role != "observer" {
		t.Fatalf("observer view %d %+v", code, v)
	}
	rule := `{"kind":"rule","name":"Done means complete","trigger":{"event":"card_moved_in","list":"` + done + `"},"actions":[{"kind":"mark_done"}]}`
	if code := call("POST", base, "farhan.khan", rule, nil); code != 403 {
		t.Fatalf("an observer made a rule: %d", code)
	}
	var made ops.BoardAuto
	if code := call("POST", base, "priya.nair", rule, &made); code != 200 || made.ID == "" || !made.On {
		t.Fatalf("made %d %+v", code, made)
	}
	if code := call("POST", base, "priya.nair", `{"kind":"rule","name":"x","actions":[]}`, nil); code != 422 && code != 400 {
		t.Fatalf("a rule with nothing in it: %d", code)
	}
	made.Name = "Finished"
	body, _ := json.Marshal(made)
	if code := call("PUT", base+"/"+made.ID, "priya.nair", string(body), &made); code != 200 || made.Name != "Finished" {
		t.Fatalf("changed %d %+v", code, made)
	}
	// The rule runs on a move, and the run is in the log.
	c, _ := o.AddCard(ctx, "demo", "priya.nair", b.ID, ops.CardInput{List: todo, Title: "Upload the UCC file"})
	if code := call("POST", "/v1/boards/"+b.ID+"/cards/"+c.ID+"/move", "priya.nair", `{"list":"`+done+`"}`, nil); code != 200 {
		t.Fatalf("move %d", code)
	}
	var runs []ops.BoardAutoRun
	if code := call("GET", base+"/log?auto="+made.ID, "farhan.khan", "", &runs); code != 200 || len(runs) != 1 || runs[0].Status != "done" {
		t.Fatalf("log %d %+v", code, runs)
	}
	// A card button, pressed.
	var btn ops.BoardAuto
	if code := call("POST", base, "priya.nair", `{"kind":"card_button","name":"Back to To do","actions":[{"kind":"move","list":"`+todo+`"},{"kind":"mark_undone"}]}`, &btn); code != 200 {
		t.Fatalf("button %d", code)
	}
	var res ops.BoardAutoResult
	if code := call("POST", base+"/"+btn.ID+"/run", "priya.nair", `{"card":"`+c.ID+`"}`, &res); code != 200 || res.Run == nil || res.Run.Status != "done" {
		t.Fatalf("pressed %d %+v", code, res)
	}
	if code := call("POST", base+"/"+btn.ID+"/run", "farhan.khan", `{"card":"`+c.ID+`"}`, nil); code != 403 {
		t.Fatalf("an observer pressed a button: %d", code)
	}
	// Off, then deleted.
	var off ops.BoardAuto
	if code := call("POST", base+"/"+made.ID+"/on", "priya.nair", `{"on":false}`, &off); code != 200 || off.On {
		t.Fatalf("switched off %d %+v", code, off)
	}
	if code := call("POST", base+"/"+made.ID+"/delete", "priya.nair", `{}`, nil); code != 200 {
		t.Fatalf("delete %d", code)
	}
	if code := call("GET", base, "priya.nair", "", &v); code != 200 || len(v.Autos) != 1 || v.Autos[0].ID != btn.ID {
		t.Fatalf("after delete %d %+v", code, v.Autos)
	}
}
