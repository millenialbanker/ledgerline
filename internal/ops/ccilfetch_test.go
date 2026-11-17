package ops

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/sheet"
)

// TestCCILFetch: CCIL's prices are fetched from the address the connection
// gives, or from the link on CCIL's page, and loaded as if uploaded; a
// month end with no file goes back to the business day before; the monthly
// task is done by the fetch; the evening fetch runs by itself and a day
// still missing at 23:00 goes to the work queue.
func TestCCILFetch(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	for _, d := range []string{"2026-10-14", "2026-10-15", D, "2026-10-20", "2026-10-21", "2026-10-29", "2026-10-30"} {
		if !set.IsBusinessDay(d) {
			t.Skipf("%s is not a business day in the sample calendar", d)
		}
	}
	wb, err := sheet.WriteXLSX([]sheet.Sheet{{Name: "Sheet1", Rows: [][]string{{"", "Fictional clearing house"}, {}, {"T-BILL"},
		{"Sr. No.", "ISIN No.", "Description", "Coupon", "Maturity Date", "Price * (Rs.)"},
		{"1", "IN002026X011", "91 DTB 05112026", "-", "46331", "99.1234"}, {}, {"G-SEC"},
		{"Sr. No.", "ISIN No.", "Description", "Coupon", "Maturity Date", "Price * (Rs.)"},
		{"1", "IN0020990011", "7.10% GS 2034", "7.1", "49000", "101.25"}}}})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		switch r.URL.Path {
		case "/f/MTMPRICE16102026.xlsx", "/f/MTMPRICE29102026.xlsx", "/f/MTMPRICE20102026.xlsx", "/docs/MTMPRICE14102026.xlsx":
			w.Write(wb)
		case "/f/MTMPRICE15102026.xlsx": // CCIL's site answers with a page
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html><body>The requested resource is not available</body></html>"))
		case "/mtm-prices":
			w.Write([]byte(`<html><body><a class="dl" href="/docs/MTMPRICE14102026.xlsx?download=true">MTMPRICE14102026</a></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	asked := func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[path]
	}
	old := ccilHTTP
	ccilHTTP = srv.Client()
	t.Cleanup(func() { ccilHTTP = old })
	conn := func(vals map[string]string) {
		t.Helper()
		if err := docs.Upsert(ctx, o.Docs, T, cConn, connCCIL, Connection{Provider: connCCIL, Status: "Saved", Values: vals}, "admin"); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := o.CCILFetch(ctx, T, "priya.nair", D, 0); !errors.Is(err, ErrState) {
		t.Fatalf("fetched with no connection: %v", err)
	}
	conn(map[string]string{"file_url": srv.URL + "/f/MTMPRICE{DD}{MM}{YYYY}.xlsx", "fetch": "On"})
	if _, err := o.CCILFetch(ctx, T, "vikram.shah", D, 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance fetched: %v", err)
	}

	// The monthly task fetches the month end: the 30th has no file, so the 29th.
	run, err := o.RunPeriodic(ctx, T, "priya.nair", "ccil_prices", "2026-10", "2026-11-02")
	if err != nil || run.Status != "Done" || !strings.Contains(run.Summary, "Fetched from CCIL: 2 prices") || !strings.Contains(run.Summary, "MTMPRICE29102026") {
		t.Fatalf("monthly task %+v %v", run, err)
	}
	if asked("/f/MTMPRICE30102026.xlsx") != 1 || asked("/f/MTMPRICE29102026.xlsx") != 1 {
		t.Fatalf("addresses asked for: 30th %d, 29th %d", asked("/f/MTMPRICE30102026.xlsx"), asked("/f/MTMPRICE29102026.xlsx"))
	}
	if p, ok := o.gsecPricesOn(ctx, T, "2026-10-31"); !ok || p.Date != "2026-10-29" || p.Prices["IN0020990011"].Price.String() != "101.25" {
		t.Fatalf("month-end prices %+v", p)
	}

	// A day's file by its address.
	r, err := o.CCILFetch(ctx, T, "priya.nair", D, 0)
	if err != nil || r.Date != D || r.Prices != 2 || r.File != "MTMPRICE16102026.xlsx" || !strings.HasSuffix(r.From, "/f/MTMPRICE16102026.xlsx") {
		t.Fatalf("fetch %+v %v", r, err)
	}
	if _, look, _ := o.Market(ctx, T, D); func() bool { i, ok := look.ByISIN("IN0020990011"); return !ok || i.LTP.String() != "101.25" }() {
		t.Fatal("the fetched price does not value the G-sec")
	}
	// A page instead of a workbook is not loaded.
	if _, err := o.CCILFetch(ctx, T, "priya.nair", "2026-10-15", 0); err == nil || !strings.Contains(err.Error(), "no workbook") {
		t.Fatalf("an error page was taken for prices: %v", err)
	}

	// Without an address: the link on CCIL's page.
	conn(map[string]string{"page": srv.URL + "/mtm-prices", "fetch": "On"})
	if r, err = o.CCILFetch(ctx, T, "priya.nair", "2026-10-14", 0); err != nil || r.Date != "2026-10-14" || !strings.Contains(r.From, "/docs/MTMPRICE14102026.xlsx") {
		t.Fatalf("from the page %+v %v", r, err)
	}
	if _, err = o.CCILFetch(ctx, T, "priya.nair", "2026-10-21", 0); err == nil || !strings.Contains(err.Error(), "no link") || !strings.Contains(err.Error(), "Give the file's address") {
		t.Fatalf("no link for the day: %v", err)
	}

	// The evening fetch: the day's prices from 19:00, once.
	conn(map[string]string{"file_url": srv.URL + "/f/MTMPRICE{DD}{MM}{YYYY}.xlsx", "fetch": "On"})
	at := func(d, hm string) time.Time {
		x, _ := time.ParseInLocation("2006-01-02 15:04", d+" "+hm, ist)
		return x
	}
	o.ccilTick(ctx, T, at("2026-10-20", "18:00")) // before 19:00 it looks for the day before
	if asked("/f/MTMPRICE20102026.xlsx") != 0 {
		t.Fatal("fetched the day's prices before the evening")
	}
	o.ccilTick(ctx, T, at("2026-10-20", "19:30"))
	o.ccilTick(ctx, T, at("2026-10-20", "20:30"))
	if p, ok := o.gsecPricesOn(ctx, T, "2026-10-20"); !ok || p.Date != "2026-10-20" || asked("/f/MTMPRICE20102026.xlsx") != 1 {
		t.Fatalf("evening fetch %+v (%d asks)", p, asked("/f/MTMPRICE20102026.xlsx"))
	}
	if st := o.CCILStatus(ctx, T); !st.Set || !st.Fetch || st.State.LastOK != "2026-10-20" || st.State.LastFile != "MTMPRICE20102026.xlsx" {
		t.Fatalf("status %+v", st)
	}
	// A day with no file: tried every half hour, to the work queue at 23:00.
	o.ccilTick(ctx, T, at("2026-10-21", "19:30"))
	o.ccilTick(ctx, T, at("2026-10-21", "19:45")) // within the half hour: not asked again
	if asked("/f/MTMPRICE21102026.xlsx") != 1 || hasOpenWork(t, o, "periodic", "ccilfetch:2026-10-21") {
		t.Fatalf("%d asks; work raised early", asked("/f/MTMPRICE21102026.xlsx"))
	}
	o.ccilTick(ctx, T, at("2026-10-21", "23:05"))
	if asked("/f/MTMPRICE21102026.xlsx") != 2 || !hasOpenWork(t, o, "periodic", "ccilfetch:2026-10-21") {
		t.Fatalf("%d asks; no work item for the missing day", asked("/f/MTMPRICE21102026.xlsx"))
	}
	// Switched off: nothing is fetched.
	conn(map[string]string{"file_url": srv.URL + "/f/MTMPRICE{DD}{MM}{YYYY}.xlsx", "fetch": "Off"})
	o.ccilTick(ctx, T, at("2026-10-22", "20:00"))
	if asked("/f/MTMPRICE22102026.xlsx") != 0 {
		t.Fatal("fetched with the fetch off")
	}
}
