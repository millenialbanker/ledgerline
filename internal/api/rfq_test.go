package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"ledgerline/internal/ops"
)

// TestRFQRoutes: the Bonds (RFQ deals) screen's routes and NSE's
// notification hook (signed with the made-up key; nothing calls NSE).
func TestRFQRoutes(t *testing.T) {
	srv, o := ipoServer(t, time.Date(2026, 11, 18, 10, 0, 0, 0, time.FixedZone("IST", 5*3600+1800)))
	var v ops.RFQView
	if c := ipoCall(t, srv, "GET", "/v1/rfq?date=2026-11-18", "priya.nair", nil, &v); c != 200 || v.Set || len(v.Deals) != 0 || len(v.CancReasons) != 5 {
		t.Fatalf("view %d %+v", c, v)
	}
	if c := ipoCall(t, srv, "POST", "/v1/rfq/read", "priya.nair", nil, nil); c != 409 {
		t.Fatalf("read without a connection: %d", c)
	}
	if c := ipoCall(t, srv, "GET", "/v1/rfq/deals.csv", "priya.nair", nil, nil); c != 400 {
		t.Fatalf("CSV without a date: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/rfq/amendments/1/decide", "priya.nair", map[string]any{"approve": true}, nil); c != 403 {
		t.Fatalf("maker decided an amendment: %d", c)
	}
	if _, err := o.SaveConnection(context.Background(), "demo", "admin", "nse-rfq", map[string]string{"member": "DFHI", "login": "DFHI", "env": "UAT", "url": "https://rfq.example/rfq"},
		map[string]string{"password": "made-up", "callback_token": "tok-made-up-123", "callback_key": "key-made-up"}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"negotiationList":[{"rfqNumber":"R22120900000020","id":"N22120900000054","status":"A"}]}`)
	m := hmac.New(sha256.New, []byte("key-made-up"))
	m.Write(body)
	hook := func(path, sig string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("token", sig)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		var out map[string]any
		_ = json.Unmarshal(b, &out)
		return res.StatusCode, out
	}
	good := hex.EncodeToString(m.Sum(nil))
	if c, _ := hook("/hooks/demo/nse-rfq/wrong/v1/notification", good); c != 403 {
		t.Fatalf("wrong token: %d", c)
	}
	if c, _ := hook("/hooks/demo/nse-rfq/tok-made-up-123/v1/notification", "00"+good[2:]); c != 403 {
		t.Fatalf("wrong signature: %d", c)
	}
	if c, out := hook("/hooks/demo/nse-rfq/tok-made-up-123/v1/notification", good); c != 200 || out["code"] != float64(200) {
		t.Fatalf("notification %d %v", c, out)
	}
	if c := ipoCall(t, srv, "GET", "/v1/rfq", "priya.nair", nil, &v); c != 200 || !v.Set || !v.CallbackOn || !v.CallbackKey || v.State.Callbacks != 1 || !v.State.Pending || v.Callback != "/hooks/demo/nse-rfq/<callback token>" {
		t.Fatalf("after the notification %d %+v", c, v)
	}
}
