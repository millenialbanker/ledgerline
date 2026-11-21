package api

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestActivityAPI(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	ist := time.FixedZone("IST", 19800)
	now := time.Date(2026, 6, 3, 7, 0, 0, 0, ist) // a Wednesday
	o.Now = func() time.Time { return now }
	a.Now = func() time.Time { return now }
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	srv := httptest.NewServer(s)
	defer srv.Close()
	call := func(method, path, who, body string, out any) int {
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
		if out != nil && resp.StatusCode == 200 {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatalf("%s: %v", b, err)
			}
		}
		if resp.StatusCode != 200 {
			t.Logf("%s %s → %d %s", method, path, resp.StatusCode, b)
		}
		return resp.StatusCode
	}
	a.Log(ctx, "demo", ops.SystemActor, "file.ingested", "F-1", map[string]any{"source": "NSE-CM-TRADE", "added": 1204})
	a.Log(ctx, "demo", "farhan.khan", "payout.batch", "PB-12", map[string]any{"items": 14, "total": "320000.00"})
	a.Log(ctx, "demo", "priya.nair", "auth.login", "priya.nair", map[string]any{"ip": "10.1.1.1"})

	var feed ops.ActivityFeed
	if c := call("GET", "/v1/activity", "admin", "", &feed); c != 200 || len(feed.Entries) != 3 || feed.Cursor == 0 || feed.Week.From != "2026-06-01T00:00:00+05:30" {
		t.Fatalf("first answer %d %+v", c, feed)
	}
	if feed.Entries[1].Text != "Farhan Khan released payout batch PB-12 (14 payouts, ₹3.2 lakh)" || feed.Entries[2].Text != "Ledgerline loaded NSE CM trade file (1,204 trades)" {
		t.Fatalf("sentences %+v", feed.Entries)
	}
	if len(feed.Counts) != 3 || len(feed.People) == 0 || len(feed.Areas) != 7 {
		t.Fatalf("counts, people, areas: %+v %+v %+v", feed.Counts, feed.People, feed.Areas)
	}
	// A maker never sees the sign-in.
	var mk ops.ActivityFeed
	if c := call("GET", "/v1/activity", "priya.nair", "", &mk); c != 200 || len(mk.Entries) != 2 {
		t.Fatalf("maker %d %+v", c, mk.Entries)
	}
	// The cursor: only what is newer.
	cur := feed.Cursor
	var poll ops.ActivityFeed
	if c := call("GET", fmt.Sprintf("/v1/activity?after=%d", cur), "admin", "", &poll); c != 200 || len(poll.Entries) != 0 || poll.Cursor != cur {
		t.Fatalf("empty poll %d %+v", c, poll)
	}
	now = now.Add(5 * time.Minute)
	a.Log(ctx, "demo", "anita.rao", "rms.override.approved", "OV-2", map[string]any{"ucc": "BH1001", "amount": "75000"})
	if c := call("GET", fmt.Sprintf("/v1/activity?after=%d", cur), "admin", "", &poll); c != 200 || len(poll.Entries) != 1 || poll.Cursor <= cur ||
		poll.Entries[0].Text != "Anita Rao approved an RMS limit override of ₹75,000 for BH1001" || poll.Counts != nil {
		t.Fatalf("poll %d %+v", c, poll)
	}
	// Filters ride along with the poll.
	var none ops.ActivityFeed
	if c := call("GET", fmt.Sprintf("/v1/activity?after=%d&only=ledgerline", cur), "admin", "", &none); c != 200 || len(none.Entries) != 0 {
		t.Fatalf("filtered poll %d %+v", c, none)
	}
	var pg ops.ActivityFeed
	if c := call("GET", "/v1/activity?limit=1&area=Risk&q=override", "admin", "", &pg); c != 200 || len(pg.Entries) != 1 || pg.Entries[0].Area != "Risk" {
		t.Fatalf("filters %d %+v", c, pg)
	}
	for _, bad := range []string{"?after=-1", "?after=x", "?after=1&before=2", "?only=all", "?area=Moon"} {
		if c := call("GET", "/v1/activity"+bad, "admin", "", nil); c == 200 {
			t.Errorf("%s accepted", bad)
		}
	}
	// The panel state and the marker, per user.
	if c := call("PUT", "/v1/activity/state", "admin", fmt.Sprintf(`{"open":true,"seen":%d}`, cur), nil); c != 200 {
		t.Fatalf("state %d", c)
	}
	if c := call("PUT", "/v1/activity/state", "admin", `{"seen":-4}`, nil); c == 200 {
		t.Fatal("negative seen accepted")
	}
	var again ops.ActivityFeed
	if c := call("GET", "/v1/activity", "admin", "", &again); c != 200 || !again.Open || again.Seen != cur || again.Unseen != 1 {
		t.Fatalf("state back %d open %v seen %d unseen %d", c, again.Open, again.Seen, again.Unseen)
	}
	// Branch staff reach the feed (their own office's lines only).
	var br ops.ActivityFeed
	if c := call("GET", "/v1/activity", "meera.iyer", "", &br); c != 200 || br.AuditLink {
		t.Fatalf("branch %d %+v", c, br)
	}
	for _, e := range br.Entries {
		if e.Actor == "farhan.khan" || e.Kind == "ledgerline" {
			t.Errorf("branch sees %q", e.Text)
		}
	}
	if c := call("PUT", "/v1/activity/state", "meera.iyer", `{"open":true}`, nil); c != 200 {
		t.Fatalf("branch state %d", c)
	}
}
