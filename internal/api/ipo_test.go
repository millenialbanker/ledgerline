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
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/sample"
	"ledgerline/internal/vault"
)

// ipoServer is a server with the sample firm's clients and the IPO routes.
func ipoServer(t *testing.T, now time.Time) (*httptest.Server, *ops.Ops) {
	t.Helper()
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
	o.Now = func() time.Time { return now }
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	s.ipoRoutes()
	s.ipoRoutes() // wiring it twice is harmless
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv, o
}

func ipoCall(t *testing.T, srv *httptest.Server, method, path, actor string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Actor", actor)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil {
		if p, ok := out.(*[]byte); ok {
			*p = b
		} else if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v\n%s", method, path, err, b)
		}
	}
	return res.StatusCode
}

func TestIPORoutes(t *testing.T) {
	ist := time.FixedZone("IST", 19800)
	srv, o := ipoServer(t, time.Date(2026, 10, 28, 11, 0, 0, 0, ist))
	ctx := context.Background()
	var iss ops.IPOIssue
	body := map[string]any{"symbol": "ROUTECO", "type": "Mainboard", "exchanges": []string{"NSE"}, "open": "2026-10-27", "close": "2026-10-29", "price_low": "95", "price_high": "100", "lot_size": 150}
	if code := ipoCall(t, srv, "POST", "/v1/ipo/issues", "meera.iyer", body, nil); code != 403 {
		t.Fatalf("branch staff set up an issue: %d", code)
	}
	if code := ipoCall(t, srv, "POST", "/v1/ipo/issues", "priya.nair", body, &iss); code != 200 || iss.ID != "ROUTECO-20261027" || iss.Status != "Open" {
		t.Fatalf("issue %d %+v", code, iss)
	}
	var list struct {
		Issues []ops.IPOIssue `json:"issues"`
	}
	if code := ipoCall(t, srv, "GET", "/v1/ipo/issues", "meera.iyer", nil, &list); code != 200 || len(list.Issues) != 1 {
		t.Fatalf("branch staff see issues: %d %+v", code, list)
	}
	// A client with a CDSL demat account.
	cs, _ := o.Clients(ctx, "demo")
	var c ops.Client
	for _, x := range cs {
		if x.Status == "Active" && x.Type == "Individual" && len(x.Demat.ID) == 16 && !strings.HasPrefix(x.Demat.ID, "IN") {
			c = x
			break
		}
	}
	vpa := strings.ToLower(c.UCC) + "@okaxis"
	if code := ipoCall(t, srv, "POST", "/v1/ipo/upi/"+c.UCC, "priya.nair", map[string]string{"vpa": vpa, "evidence": "name match"}, nil); code != 200 {
		t.Fatalf("UPI %d", code)
	}
	var bid ops.IPOApp
	in := map[string]any{"issue": iss.ID, "ucc": c.UCC, "category": "RII", "options": []map[string]any{{"lots": 2, "cut_off": true}}, "upi": vpa,
		"instruction": map[string]string{"channel": ops.IPOChannels[0], "ref": "CALL-9", "received": "2026-10-28T10:50"}}
	if code := ipoCall(t, srv, "POST", "/v1/ipo/bids", "priya.nair", in, &bid); code != 200 || bid.Status != ops.BidDraft {
		t.Fatalf("bid %d %+v", code, bid)
	}
	if code := ipoCall(t, srv, "POST", "/v1/ipo/bids", "priya.nair", in, nil); code != 422 && code != 400 {
		t.Fatalf("second bid on the PAN: %d", code)
	}
	var legacy []ops.IPOApp
	if code := ipoCall(t, srv, "GET", "/v1/ipo", "priya.nair", nil, &legacy); code != 200 || len(legacy) == 0 {
		t.Fatalf("GET /v1/ipo %d", code)
	}
	var batch ops.IPOBatch
	if code := ipoCall(t, srv, "POST", "/v1/ipo/issues/"+iss.ID+"/bid-file", "priya.nair", map[string]string{"exchange": "NSE"}, &batch); code != 200 || batch.ID == "" {
		t.Fatalf("bid file %d %+v", code, batch)
	}
	var file []byte
	if code := ipoCall(t, srv, "GET", "/v1/ipo/batches/"+batch.ID+"/file", "priya.nair", nil, &file); code != 200 || !strings.HasPrefix(string(file), "activityType,symbol,category") || !strings.Contains(string(file), bid.ID) {
		t.Fatalf("file %d %q", code, file)
	}
	resp := []byte("symbol,applicationNumber,referenceNumber,pan,dpId,clientBenId,category,bidId,quantity,price,activityType,status,errorCode,reason,upiStatus,umn,dpVerStatus,dpVerReason,timestamp\r\n" +
		"ROUTECO,2610289999001," + bid.ID + ",,,,RII,1,300,100,N,Success,,,100,UMN9,Y,,\r\n")
	var loaded []ops.XFileResult
	if code := ipoCall(t, srv, "POST", "/v1/ipo/responses?name=NSE_EIPO_RESP_28102026.csv", "priya.nair", resp, &loaded); code != 200 || len(loaded) != 1 || loaded[0].Added != 1 {
		t.Fatalf("response %d %+v", code, loaded)
	}
	var detail ops.IPOBidDetail
	if code := ipoCall(t, srv, "GET", "/v1/ipo/bids/"+bid.ID, "priya.nair", nil, &detail); code != 200 || detail.Bid.Status != ops.BidAccepted || len(detail.Instructions) != 1 {
		t.Fatalf("bid detail %d %+v", code, detail.Bid)
	}
	// The client portal: the client's own page; staff screens are closed.
	user, _, err := o.PortalAccess(ctx, "demo", "farhan.khan", c.UCC, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var pv ops.PortalIPOView
	if code := ipoCall(t, srv, "GET", "/v1/portal/ipo", user, nil, &pv); code != 200 || pv.UCC != c.UCC || len(pv.Issues) != 1 || pv.Issues[0].Bid == nil {
		t.Fatalf("portal %d %+v", code, pv)
	}
	if code := ipoCall(t, srv, "GET", "/v1/ipo/bids", user, nil, nil); code != 403 {
		t.Fatalf("client on a staff route: %d", code)
	}
	// NSE's callback: refused without the token, applied with it.
	if code := ipoCall(t, srv, "POST", "/hooks/demo/eipo?token=nope", "", []byte(`{}`), nil); code != 403 {
		t.Fatalf("callback without a connection: %d", code)
	}
	if _, err := o.SaveConnection(ctx, "demo", "admin", "nse-eipo", map[string]string{"member": "90311", "login": "LLAPI", "env": "Live", "send": "Off"},
		map[string]string{"password": "made-up", "callback_token": "made-up-callback-token-42"}); err != nil {
		t.Fatal(err)
	}
	cb := []byte(`[{"referenceNumber":"` + bid.ID + `","upiPaymentStatus":"110"}]`)
	if code := ipoCall(t, srv, "POST", "/hooks/demo/eipo?token=nope", "", cb, nil); code != 403 {
		t.Fatalf("wrong token: %d", code)
	}
	var cr map[string]any
	if code := ipoCall(t, srv, "POST", "/hooks/demo/eipo?token=made-up-callback-token-42", "", cb, &cr); code != 200 || cr["applied"] != float64(1) {
		t.Fatalf("callback %d %v", code, cr)
	}
	var st ops.EIPOView
	if code := ipoCall(t, srv, "GET", "/v1/ipo/eipo", "priya.nair", nil, &st); code != 200 || st.State.Callbacks != 1 || st.Usable {
		t.Fatalf("eIPO status %d %+v", code, st)
	}
}
