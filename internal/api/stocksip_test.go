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
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/sample"
	"ledgerline/internal/vault"
)

func TestStockSIPAPI(t *testing.T) {
	const day = "2026-10-16"
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	ist := time.FixedZone("IST", 19800)
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 11, 0, 0, 0, ist) }
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.LoadSampleDay(ctx, "demo", "priya.nair", sample.Generate(day), false, func(ctx context.Context, tenant, actor, source, name string, data []byte) error {
		_, err := a.IngestFile(ctx, tenant, actor, source, "", name, data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := o.PutUser(ctx, "demo", "admin", ops.User{ID: "client.bh1001", Name: "Aarav Sharma", Role: ops.RoleClient, Active: true, Clients: []string{"BH1001"}}); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	s.stockSIPRoutes()
	s.stockSIPRoutes() // registering twice is harmless
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
		if out != nil {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatalf("%s %s: %v\n%s", method, path, err, b)
			}
		}
		if resp.StatusCode >= 300 {
			t.Logf("%s %s → %d %s", method, path, resp.StatusCode, b)
		}
		return resp.StatusCode
	}
	// Settings are the compliance officer's.
	cfg := ops.DefaultSIPSettings()
	cfg.Enabled = true
	cb, _ := json.Marshal(cfg)
	if c := call("PUT", "/v1/stock-sips/settings", "priya.nair", string(cb), nil); c != 403 {
		t.Fatalf("maker changed SIP settings: %d", c)
	}
	if c := call("PUT", "/v1/stock-sips/settings", "vikram.shah", string(cb), nil); c != 200 {
		t.Fatalf("settings %d", c)
	}
	if c := call("POST", "/v1/stock-sips/lists/eligible?name=list.csv", "vikram.shah", "Symbol,ISIN\nRELIANCE,INE002A01018\nSBIN,INE062A01020\n", nil); c != 200 {
		t.Fatalf("list %d", c)
	}
	// Staff register one; the client registers one in the portal.
	var sip ops.StockSIP
	if c := call("POST", "/v1/stock-sips", "priya.nair", `{"ucc":"BH1001","symbol":"RELIANCE","basis":"qty","qty":36,"frequency":"daily","consent_source":"Signed form","consent_ref":"F-1"}`, &sip); c != 200 || sip.ID == "" {
		t.Fatalf("create %d %+v", c, sip)
	}
	if c := call("POST", "/v1/stock-sips", "client.bh1001", `{"ucc":"BH1001","symbol":"SBIN","basis":"amount","amount":"3000","frequency":"daily","consent":true}`, nil); c != 403 {
		t.Fatalf("client on a staff route: %d", c)
	}
	var pv ops.SIPPreview
	if c := call("POST", "/v1/portal/stock-sips/preview", "client.bh1001", `{"ucc":"BH1001","symbol":"SBIN","basis":"amount","amount":"3000","frequency":"weekly","day":2}`, &pv); c != 200 || pv.ConsentText == "" {
		t.Fatalf("preview %d", c)
	}
	var mine ops.StockSIP
	if c := call("POST", "/v1/portal/stock-sips", "client.bh1001", `{"ucc":"BH1001","symbol":"SBIN","basis":"amount","amount":"3000","frequency":"weekly","day":2,"consent":true,"consent_version":1}`, &mine); c != 200 || mine.Consent.Source != ops.SrcPortal {
		t.Fatalf("portal create %d %+v", c, mine)
	}
	if c := call("POST", "/v1/portal/stock-sips/"+mine.ID+"/action", "client.bh1001", `{"action":"pause"}`, nil); c != 200 {
		t.Fatalf("portal pause %d", c)
	}
	var pview ops.PortalSIPView
	if c := call("GET", "/v1/portal/stock-sips?date="+day, "client.bh1001", "", &pview); c != 200 || len(pview.SIPs) != 2 {
		t.Fatalf("portal view %d %d", c, len(pview.SIPs))
	}
	if c := call("GET", "/v1/stock-sips?date="+day, "client.bh1001", "", nil); c != 403 {
		t.Fatalf("client saw the staff screen: %d", c)
	}
	// The day: run, basket, match.
	var run ops.SIPRunResult
	if c := call("POST", "/v1/stock-sips/run", "priya.nair", `{"date":"`+day+`"}`, &run); c != 200 || run.Counts["Pending"] != 1 {
		t.Fatalf("run %d %+v", c, run)
	}
	var sent ops.SIPSendResult
	if c := call("POST", "/v1/stock-sips/send", "farhan.khan", `{"date":"`+day+`"}`, &sent); c != 200 || sent.Placed != 1 {
		t.Fatalf("send %d %+v", c, sent)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/v1/stock-sips/batches/"+sent.Batch+"/file", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Actor", "farhan.khan")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	file, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(string(file), "TradeDate,ClientCode,Exchange") || !strings.Contains(resp.Header.Get("Content-Disposition"), sent.FileName) {
		t.Fatalf("basket file %d %s", resp.StatusCode, file)
	}
	var mr ops.SIPMatchResult
	if c := call("POST", "/v1/stock-sips/match", "farhan.khan", `{"date":"`+day+`","final":true}`, &mr); c != 200 || mr.Counts["Filled"] != 1 {
		t.Fatalf("match %d %+v", c, mr)
	}
	var screen ops.StockSIPScreen
	if c := call("GET", "/v1/stock-sips?date="+day, "farhan.khan", "", &screen); c != 200 || len(screen.Orders) != 1 || screen.Orders[0].Status != "Filled" {
		t.Fatalf("screen %d %+v", c, screen.Orders)
	}
	// Cashfree's notices need the connection and its signature; the
	// authorisation page answers a bad link plainly.
	if c := call("POST", "/hooks/demo/stocksip/cashfree", "", `{"type":"SUBSCRIPTION_PAYMENT_SUCCESS"}`, nil); c != 403 {
		t.Fatalf("unsigned notice %d", c)
	}
	resp, err = http.Get(srv.URL + "/hooks/demo/stocksip/mandate/nope")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "not valid") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "nonce-") {
		t.Fatalf("mandate page %d %s", resp.StatusCode, page)
	}
}
