package api

import (
	"bytes"
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
)

const key = "test-key-0123456789abcdef"

func server(t *testing.T) *httptest.Server {
	t.Helper()
	reg, err := ingest.LoadDir("../../config/adapters")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, err := charges.LoadSchedule(f)
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(app.MemoryStores(), reg, sched)
	srv := httptest.NewServer(New(a, nil, map[string]string{key: "bhairav", "other-tenant-key-0000": "beta"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false))
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path, k string, body io.Reader, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, body)
	if k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	req.Header.Set("X-Actor", "ops.priya")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, path, err, b)
		}
	}
	return res.StatusCode
}

func upload(t *testing.T, srv *httptest.Server, source, file string) {
	t.Helper()
	b, _ := os.ReadFile("../../testdata/sample/" + file)
	var res map[string]any
	if code := call(t, srv, "POST", "/v1/files?source="+source+"&name="+file, key, bytes.NewReader(b), &res); code != 201 {
		t.Fatalf("upload %s: %d %v", file, code, res)
	}
}

func TestAPIDay(t *testing.T) {
	srv := server(t)
	if code := call(t, srv, "GET", "/v1/trades?date=2026-10-16", "", nil, nil); code != 401 {
		t.Fatalf("no key: %d", code)
	}
	if code := call(t, srv, "GET", "/v1/trades?date=2026-10-16", "wrong-key-wrong-key", nil, nil); code != 401 {
		t.Fatalf("bad key: %d", code)
	}
	for _, f := range [][2]string{{"NSE-CM-TRADE", "nse_cm_trades.csv"}, {"NSE-FO-TRADE", "nse_fo_trades.csv"}, {"MCX-TRADE", "mcx_trades.txt"}, {"OMS-TRADE", "oms_trades.csv"}} {
		upload(t, srv, f[0], f[1])
	}
	var trades []map[string]any
	call(t, srv, "GET", "/v1/trades?date=2026-10-16&origin=EXCHANGE", key, nil, &trades)
	if len(trades) != 17 {
		t.Fatalf("exchange trades %d", len(trades))
	}
	// Other tenant sees nothing.
	call(t, srv, "GET", "/v1/trades?date=2026-10-16", "other-tenant-key-0000", nil, &trades)
	if len(trades) != 0 {
		t.Fatal("tenant isolation broken over API")
	}

	var recon struct {
		Open   int `json:"open"`
		Result struct {
			Matched int `json:"matched"`
		} `json:"result"`
	}
	if code := call(t, srv, "POST", "/v1/recon/runs", key, strings.NewReader(`{"date":"2026-10-16"}`), &recon); code != 201 || recon.Open != 7 || recon.Result.Matched != 11 {
		t.Fatalf("recon %d %+v", code, recon)
	}
	var breaks []struct{ ID, Type, Status string }
	call(t, srv, "GET", "/v1/recon/breaks?date=2026-10-16&status=OPEN", key, nil, &breaks)
	if len(breaks) != 7 {
		t.Fatalf("breaks %d", len(breaks))
	}
	var errBody map[string]map[string]string
	if code := call(t, srv, "POST", "/v1/recon/breaks/"+breaks[0].ID+"/resolve", key, strings.NewReader(`{"resolution":""}`), &errBody); code != 400 {
		t.Fatalf("empty resolution: %d", code)
	}
	if code := call(t, srv, "POST", "/v1/recon/breaks/"+breaks[0].ID+"/resolve", key, strings.NewReader(`{"resolution":"Booked manual trade in OMS"}`), nil); code != 200 {
		t.Fatalf("resolve: %d", code)
	}

	prices, _ := os.ReadFile("../../testdata/sample/settlement_prices.json")
	var bill struct {
		Posted   []string `json:"posted"`
		Warnings []string `json:"warnings"`
	}
	if code := call(t, srv, "POST", "/v1/billing/runs", key, strings.NewReader(`{"date":"2026-10-16","settlement_prices":`+string(prices)+`}`), &bill); code != 201 || len(bill.Posted) == 0 {
		t.Fatalf("billing %d %+v", code, bill)
	}
	var tb struct {
		Balanced   bool   `json:"balanced"`
		TotalDebit string `json:"total_debit"`
	}
	call(t, srv, "GET", "/v1/ledger/trial-balance?as_of=2026-10-16", key, nil, &tb)
	if !tb.Balanced {
		t.Fatalf("trial balance %+v", tb)
	}
	var st struct {
		Closing string `json:"closing"`
	}
	call(t, srv, "GET", "/v1/ledger/statement?ucc=bh1001&from=2026-10-16&to=2026-10-16", key, nil, &st)
	if st.Closing != "17863.72" {
		t.Fatalf("statement closing %s", st.Closing)
	}

	// Manual journal: unbalanced is 422, balanced is 201, retry is 200, reversal is 201.
	bad := `{"key":"RCPT:UTR9","value_date":"2026-10-16","narration":"Funds received","postings":[{"account":"BANK:CLIENT","debit":"5000"},{"account":"CLIENT:BH1001","credit":"4999"}]}`
	if code := call(t, srv, "POST", "/v1/journal", key, strings.NewReader(bad), nil); code != 422 {
		t.Fatalf("unbalanced journal: %d", code)
	}
	good := strings.Replace(bad, `"4999"`, `"5000"`, 1)
	var je struct{ ID string }
	if code := call(t, srv, "POST", "/v1/journal", key, strings.NewReader(good), &je); code != 201 {
		t.Fatalf("journal: %d", code)
	}
	if code := call(t, srv, "POST", "/v1/journal", key, strings.NewReader(good), nil); code != 200 {
		t.Fatalf("journal retry: %d", code)
	}
	if code := call(t, srv, "POST", "/v1/journal/"+je.ID+"/reverse", key, strings.NewReader(`{"reason":"wrong client"}`), nil); code != 201 {
		t.Fatalf("reverse: %d", code)
	}
	var q struct {
		Breakdown struct{ Total string } `json:"breakdown"`
	}
	call(t, srv, "POST", "/v1/charges/quote", key, strings.NewReader(`{"bucket":"CM-DEL","exchange":"NSE","side":"B","qty":10,"price":"1412","date":"2026-10-16"}`), &q)
	if q.Breakdown.Total != "16.75" {
		t.Fatalf("quote %+v", q)
	}
	var audit []map[string]any
	call(t, srv, "GET", "/v1/audit", key, nil, &audit)
	if len(audit) < 9 {
		t.Fatalf("audit %d", len(audit))
	}
}

func TestActorRequired(t *testing.T) {
	srv := server(t)
	req, _ := http.NewRequest("POST", srv.URL+"/v1/recon/runs", strings.NewReader(`{"date":"2026-10-16"}`))
	req.Header.Set("Authorization", "Bearer "+key)
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 400 {
		t.Fatalf("missing actor: %d", res.StatusCode)
	}
}

func TestParseKeys(t *testing.T) {
	if _, err := ParseKeys("short:t1"); err == nil {
		t.Fatal("short key accepted")
	}
	m, err := ParseKeys("0123456789abcdef0:alpha, fedcba98765432100:beta")
	if err != nil || m["0123456789abcdef0"] != "alpha" || len(m) != 2 {
		t.Fatalf("%v %v", m, err)
	}
}
