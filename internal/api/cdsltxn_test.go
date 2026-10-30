package api

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
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

const testEDISKey = "edisedisedisedisedisedisedisedis"

func edisSeal(t *testing.T, v any) string {
	t.Helper()
	b, _ := json.Marshal(v)
	blk, _ := aes.NewCipher([]byte(testEDISKey))
	n := aes.BlockSize - len(b)%aes.BlockSize
	b = append(b, bytes.Repeat([]byte{byte(n)}, n)...)
	out := make([]byte, len(b))
	cipher.NewCBCEncrypter(blk, make([]byte, aes.BlockSize)).CryptBlocks(out, b)
	return base64.StdEncoding.EncodeToString(out)
}

func TestCDSLTxnAPI(t *testing.T) {
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
	if _, err := o.LoadSampleDay(ctx, "demo", "priya.nair", sample.Generate("2026-10-16"), false, func(ctx context.Context, tenant, actor, source, name string, data []byte) error {
		_, err := a.IngestFile(ctx, tenant, actor, source, "", name, data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := o.PutUser(ctx, "demo", "admin", ops.User{ID: "client.bh1001", Name: "Aarav Sharma", Role: ops.RoleClient, Active: true, Clients: []string{"BH1001"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveConnection(ctx, "demo", "admin", "cdsl-api", map[string]string{"dp_id": "12081600", "env": "Mock", "callback_url": "https://bo.example.test", "edis_cm_nse": "M1234", "api_edis": "On"},
		map[string]string{"api_key": testEDISKey}); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	s.cdslTxnRoutes()
	s.cdslTxnRoutes() // registering twice is harmless
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
			json.Unmarshal(b, out)
		}
		if resp.StatusCode >= 300 {
			t.Logf("%s %s → %d %s", method, path, resp.StatusCode, b)
		}
		return resp.StatusCode
	}
	if c := call("GET", "/v1/cdsl/txn", "priya.nair", "", nil); c != 200 {
		t.Fatalf("txn screen %d", c)
	}
	if c := call("GET", "/v1/cdsl/txn", "client.bh1001", "", nil); c != 403 {
		t.Fatalf("client on the staff screen: %d", c)
	}
	var pv ops.PortalEDISView
	if c := call("GET", "/v1/portal/edis", "client.bh1001", "", &pv); c != 200 || !pv.On || len(pv.Holdings) == 0 {
		t.Fatalf("portal %d %+v", c, pv)
	}
	h := pv.Holdings[0]
	var started struct {
		Form ops.CDSLForm `json:"form"`
		Auth ops.EDISAuth `json:"authorisation"`
	}
	body, _ := json.Marshal(ops.EDISRequest{UCC: "BH1001", Lines: []ops.EDISRequestLine{{ISIN: h.ISIN, Qty: 1}}})
	if c := call("POST", "/v1/portal/edis", "client.bh1001", string(body), &started); c != 200 || started.Form.Fields["TransDtls"] == "" {
		t.Fatalf("start %d %+v", c, started)
	}
	post := func(path string, v neturl.Values) (int, string) {
		resp, err := http.PostForm(srv.URL+path, v)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	// Not CDSL's: refused, as a page.
	if c, page := post("/hooks/demo/cdsl/edis/return", neturl.Values{"ReqId": {started.Auth.ReqID}, "transDtls": {"bm90IGNkc2w="}}); c != 400 || !strings.Contains(page, "could not be taken") {
		t.Fatalf("forged %d %s", c, page)
	}
	reply := edisSeal(t, map[string]any{"TransResDtls": map[string]any{"ReqId": started.Auth.ReqID, "ReqType": "D", "ResId": "R1", "ResStatus": "0",
		"RecordResDtls": []any{map[string]any{"TxnReqId": started.Auth.Lines[0].TxnReqID, "TxnId": "T1", "Status": "0"}}}})
	c, page := post("/hooks/demo/cdsl/edis/return", neturl.Values{"ReqId": {started.Auth.ReqID}, "ReqType": {"D"}, "transDtls": {reply}})
	if c != 200 || !strings.Contains(page, "recorded your authorisation") || !strings.Contains(page, "/#pedis") {
		t.Fatalf("return %d %s", c, page)
	}
	var ev ops.EDISView
	if c := call("GET", "/v1/cdsl/edis?date=2026-10-16", "anita.rao", "", &ev); c != 200 || len(ev.Auths) != 1 || ev.Auths[0].Status != "Authorised" {
		t.Fatalf("staff screen %d %+v", c, ev)
	}
	if c := call("POST", "/v1/portal/edis/tpin", "client.bh1001", `{"ucc":"BH1002"}`, nil); c != 404 {
		t.Fatalf("someone else's TPIN: %d", c)
	}
	if c, _ := post("/hooks/demo/cdsl/report/wrongtoken", neturl.Values{"x": {"y"}}); c != 404 {
		t.Fatalf("report hook with a wrong token: %d", c)
	}
}
