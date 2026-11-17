package api

import (
	"context"
	"io"
	"log/slog"
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

func TestCircularCardRoutes(t *testing.T) {
	ops.PasswordIterations = 1000
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
	o.Now = func() time.Time { return time.Date(2026, 10, 17, 10, 0, 0, 0, time.FixedZone("IST", 19800)) }
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	s.circularCardRoutes()
	s.circularCardRoutes() // wiring it twice is harmless
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	// A made-up circular.
	csv := "Source,Ref,Date,Subject,Department,URL,Text\nSEBI,SEBI/HO/MIRSD/2026/990,2026-10-15,Cybersecurity and Cyber Resilience Framework (CSCRF): reporting,ITD,,\"To, All Stock Brokers. Dear Sir / Madam, Regulated entities shall submit the reports.\"\n"
	if _, err := o.LoadFile(ctx, "demo", "vikram.shah", "CIRCULARS", "", "c.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}

	var fp ops.FirmProfileView
	if c := ipoCall(t, srv, "GET", "/v1/firm-profile", "vikram.shah", nil, &fp); c != 200 || len(fp.Facts) == 0 || len(fp.Questions) == 0 {
		t.Fatalf("profile %d %+v", c, fp)
	}
	if c := ipoCall(t, srv, "GET", "/v1/firm-profile", "priya.nair", nil, nil); c != 403 {
		t.Fatalf("maker read the profile: %d", c)
	}
	if c := ipoCall(t, srv, "GET", "/v1/firm-profile", "meera.iyer", nil, nil); c != 403 {
		t.Fatalf("branch read the profile: %d", c)
	}
	if c := ipoCall(t, srv, "PUT", "/v1/firm-profile", "vikram.shah", map[string]any{"cscrf": "nope"}, nil); c != 422 {
		t.Fatalf("bad category: %d", c)
	}
	if c := ipoCall(t, srv, "PUT", "/v1/firm-profile", "vikram.shah", map[string]any{"cscrf": "mid", "cdsl": "no", "nsdl": "no"}, &fp); c != 200 || fp.Profile.CSCRF != "mid" || fp.Profile.By != "vikram.shah" {
		t.Fatalf("save %d %+v", c, fp.Profile)
	}

	var st ops.CircularsState
	if c := ipoCall(t, srv, "GET", "/v1/circulars/state", "vikram.shah", nil, &st); c != 200 || st.AIOn || !strings.Contains(st.AIWhy, "No AI model") || len(st.Areas) == 0 || !st.CanReview {
		t.Fatalf("state %d %+v", c, st)
	}
	if c := ipoCall(t, srv, "GET", "/v1/circulars/state", "priya.nair", nil, &st); c != 200 || st.CanReview {
		t.Fatalf("maker state %d %+v", c, st)
	}
	cs, _ := o.Circulars(ctx, "demo")
	if len(cs) != 1 || cs[0].Applies == nil || cs[0].Applies.Verdict != ops.ApplyYes || !strings.Contains(cs[0].Applies.Reason, "addressed to stock brokers") {
		t.Fatalf("circular %+v", cs[0].Applies)
	}
	id := cs[0].ID
	if c := ipoCall(t, srv, "POST", "/v1/circulars/"+id+"/ai", "vikram.shah", nil, nil); c != 422 {
		t.Fatalf("AI read with no model: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/circulars/"+id+"/ai", "priya.nair", nil, nil); c != 403 {
		t.Fatalf("maker AI read: %d", c)
	}
}
