package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	"ledgerline/internal/sample"
	"ledgerline/internal/vault"
)

func TestCBRICSRoutes(t *testing.T) {
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
	if _, err := o.LoadSampleDay(ctx, "demo", "priya.nair", sample.Generate("2026-10-16"), false, func(ctx context.Context, tenant, actor, source, name string, data []byte) error {
		_, err := a.IngestFile(ctx, tenant, actor, source, "", name, data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	o.Now = func() time.Time { return time.Date(2026, 11, 18, 11, 0, 0, 0, time.FixedZone("IST", 19800)) }
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	s.cbricsRoutes()
	s.cbricsRoutes() // wiring it twice is harmless
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	var v ops.CBRICSView
	if c := ipoCall(t, srv, "GET", "/v1/cbrics", "anita.rao", nil, &v); c != 200 || v.Set || v.Callback != "/hooks/demo/nse-cbrics" || v.Minutes != 15 {
		t.Fatalf("view %d %+v", c, v)
	}
	if c := ipoCall(t, srv, "GET", "/v1/cbrics", "meera.iyer", nil, nil); c != 403 {
		t.Fatalf("branch staff saw the screen: %d", c)
	}
	// Without the connection nothing is entered, and NSE's notifications
	// are refused.
	if c := ipoCall(t, srv, "POST", "/v1/cbrics/deals", "priya.nair", map[string]any{"ucc": "X"}, nil); c != 409 {
		t.Fatalf("deal without a connection: %d", c)
	}
	hook := func(token string, body []byte) (int, map[string]any) {
		req, _ := http.NewRequest("POST", srv.URL+"/hooks/demo/nse-cbrics/v1/notification", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("token", token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	body := []byte(`{"orderList":[{"standingInstFlag":"N","symbol":"INE752E07FF5","id":344,"status":1}]}`)
	if c, _ := hook("whatever", body); c != 403 {
		t.Fatalf("notification without a connection: %d", c)
	}
	if _, err := o.SaveConnection(ctx, "demo", "admin", "nse-cbrics", map[string]string{"member": "PART1", "login": "PART1", "env": "Testing (UAT)", "minutes": "10"},
		map[string]string{"password": "a#12G34h4r5t", "callback_key": "made-up-callback-key"}); err != nil {
		t.Fatal(err)
	}
	m := hmac.New(sha256.New, []byte("made-up-callback-key"))
	m.Write(body)
	if c, _ := hook("", body); c != 403 {
		t.Fatalf("unsigned notification: %d", c)
	}
	if c, out := hook(hex.EncodeToString(m.Sum(nil)), body); c != 200 || out["code"] != float64(200) {
		t.Fatalf("notification %d %v", c, out)
	}
	if c := ipoCall(t, srv, "GET", "/v1/cbrics", "priya.nair", nil, &v); c != 200 || !v.Set || v.Minutes != 10 || !v.KeySet || !v.State.Refresh || v.State.Notices != 1 {
		t.Fatalf("view after the notification %d %+v", c, v)
	}
	// A client's participant, filled in from the client's record.
	cs, _ := o.Clients(ctx, "demo")
	var ucc string
	for _, c := range cs {
		if c.Status == "Active" && c.Email != "" && c.Mobile != "" && c.Bank.IFSC != "" && len(c.Demat.ID) == 16 {
			ucc = c.UCC
			break
		}
	}
	var p ops.CBRICSParty
	if c := ipoCall(t, srv, "GET", "/v1/cbrics/participants/from-client/"+ucc, "priya.nair", nil, &p); c != 200 || p.Code != ucc || len(p.Banks) == 0 {
		t.Fatalf("from the client %d %+v", c, p)
	}
	p.StateCode, p.RegAddr, p.DOB = "27", "1 Made-up Road, Mumbai", "1980-01-01"
	if c := ipoCall(t, srv, "POST", "/v1/cbrics/participants", "priya.nair", p, &p); c != 200 || p.Pending == nil || p.State != "Draft" {
		t.Fatalf("participant saved %d %+v", c, p)
	}
	if c := ipoCall(t, srv, "POST", "/v1/cbrics/participants/"+ucc+"/decide", "priya.nair", map[string]any{"approve": true}, nil); c != 403 {
		t.Fatalf("the maker sent the participant: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/cbrics/participants", "priya.nair", ops.CBRICSParty{Code: "BUYER1", Kind: "Registered", Name: "Participant One"}, nil); c != 200 {
		t.Fatalf("registered participant %d", c)
	}
	// A deal: the maker enters it, cannot send it, and cancels it.
	var d ops.CBRICSDeal
	deal := map[string]any{"ucc": ucc, "side": "Sell", "isin": "INE752E07FF5", "counterparty": "BUYER1", "quantity": 10, "face_value": "100000", "price": "100.512", "accr_int": "0",
		"yield_type": "M", "yield": "10.4512", "settle": 1, "trade_date": "2026-11-18", "trade_time": "10:58"}
	if c := ipoCall(t, srv, "POST", "/v1/cbrics/deals", "priya.nair", deal, &d); c != 200 || d.Status != "Draft" || d.Due != "2026-11-18T11:08:00+05:30" {
		t.Fatalf("deal %d %+v", c, d)
	}
	if c := ipoCall(t, srv, "POST", "/v1/cbrics/deals/"+d.ID+"/decide", "priya.nair", map[string]any{"approve": true}, nil); c != 403 {
		t.Fatalf("the maker reported the deal: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/cbrics/deals/"+d.ID+"/decide", "farhan.khan", map[string]any{"approve": false}, nil); c != 422 {
		t.Fatalf("returned without a reason: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/cbrics/deals/"+d.ID+"/decide", "farhan.khan", map[string]any{"approve": false, "reason": "wrong price"}, &d); c != 200 || d.Status != "Returned" {
		t.Fatalf("returned %d %+v", c, d)
	}
	if c := ipoCall(t, srv, "POST", "/v1/cbrics/deals/"+d.ID+"/cancel", "priya.nair", map[string]any{"reason": "client changed their mind"}, &d); c != 200 || d.Status != "Cancelled" {
		t.Fatalf("cancelled %d %+v", c, d)
	}
	var pr ops.CBRICSProposal
	if c := ipoCall(t, srv, "GET", "/v1/cbrics/propose?ucc="+ucc, "priya.nair", nil, &pr); c != 200 || pr.ClientCode != ucc || pr.TradeTime != "11:00" {
		t.Fatalf("proposal %d %+v", c, pr)
	}
}
