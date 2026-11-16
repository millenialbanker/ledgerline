package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

func TestBoardMyCardsAPI(t *testing.T) {
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
	b, err := o.CreateBoard(ctx, "demo", "priya.nair", ops.BoardInput{Name: "Close"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := o.AddCard(ctx, "demo", "priya.nair", b.ID, ops.CardInput{List: b.Lists[0].ID, Title: "Post interest"})
	due, ms := "2026-11-17", []string{"priya.nair"}
	if _, err := o.UpdateCard(ctx, "demo", "priya.nair", b.ID, c.ID, ops.CardPatch{Due: &due, Members: &ms}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false))
	defer srv.Close()
	get := func(who string) (int, ops.MyCardsView) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/v1/boards/my-cards", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Actor", who)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v ops.MyCardsView
		_ = json.NewDecoder(resp.Body).Decode(&v)
		return resp.StatusCode, v
	}
	// The route is not taken for a board called "my-cards".
	if code, v := get("priya.nair"); code != 200 || len(v.Cards) != 1 || v.Cards[0].When != "overdue" || v.Counts["overdue"] != 1 {
		t.Fatalf("priya: %d %+v", code, v)
	}
	// Branch staff may use it (their own private boards only).
	if code, v := get("meera.iyer"); code != 200 || len(v.Cards) != 0 {
		t.Fatalf("branch staff: %d %+v", code, v)
	}
}
