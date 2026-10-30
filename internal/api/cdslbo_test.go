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
	netu "net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

// A synthetic API key.
const cdslTestKey = "0123456789abcdef0123456789abcdef"

func cdslTestEncrypt(t *testing.T, plain []byte) string {
	t.Helper()
	blk, _ := aes.NewCipher([]byte(cdslTestKey))
	n := aes.BlockSize - len(plain)%aes.BlockSize
	p := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(n)}, n)...)
	out := make([]byte, len(p))
	cipher.NewCBCEncrypter(blk, make([]byte, 16)).CryptBlocks(out, p)
	return base64.StdEncoding.EncodeToString(out)
}

func TestCDSLBOAPI(t *testing.T) {
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	o.Now = func() time.Time { return time.Date(2026, 10, 30, 10, 0, 0, 0, time.FixedZone("IST", 19800)) }
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveConnection(ctx, "demo", "admin", "cdsl-api", map[string]string{"dp_id": "10900", "env": "Mock", "user": "APIUSER1", "callback_url": "https://bo.example.test", "api_beneficiary": "On"},
		map[string]string{"password": "Synthetic@1", "secret": cdslTestKey}); err != nil {
		t.Fatal(err)
	}
	c, err := o.CreateApplication(ctx, "demo", "priya.nair", ops.Client{Name: "Bhavna Shah", Type: "Individual", PAN: "ABCPB3333B", Mobile: "9876500011", Email: "bhavna@example.test", City: "Pune",
		Segments: []string{"CM"}, Bank: ops.BankAccount{Bank: "Sample Bank", IFSC: "SBIN0001234", Account: "123456789012"}, Demat: ops.Demat{Depository: "CDSL", ID: "1201090000003333"}, RiskDisclosure: true})
	if err != nil {
		t.Fatal(err)
	}
	o.AdvanceStage(ctx, "demo", "priya.nair", c.UCC)
	if _, err := o.AdvanceStage(ctx, "demo", "farhan.khan", c.UCC); err != nil {
		t.Fatal(err)
	}
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	s.cdslBORoutes()
	s.cdslBORoutes() // registering twice is harmless
	srv := httptest.NewServer(s)
	defer srv.Close()
	call := func(method, path, who, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Actor", who)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		return resp.StatusCode, m
	}
	code, m := call("POST", "/v1/cdsl/requests", "priya.nair", `{"kind":"Add beneficiary","ucc":"`+c.UCC+`","buyers":[{"bo_id":"1201090000009999","pan":"ABCPZ1111Z"}]}`)
	if code != 201 || m["status"] != "Waiting for checker" {
		t.Fatalf("create: %d %v", code, m)
	}
	id := m["id"].(string)
	if code, _ := call("POST", "/v1/cdsl/requests/"+id+"/action", "priya.nair", `{"action":"approve"}`); code != 403 {
		t.Fatalf("maker approved: %d", code)
	}
	code, m = call("POST", "/v1/cdsl/requests/"+id+"/action", "farhan.khan", `{"action":"approve"}`)
	link, _ := m["link"].(string)
	if code != 200 || !strings.HasPrefix(link, "https://bo.example.test/hooks/demo/cdsl/go/") {
		t.Fatalf("approve: %d %v", code, m)
	}
	path := strings.TrimPrefix(link, "https://bo.example.test")
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if resp.StatusCode != 200 || !strings.Contains(string(page), `action="https://mockapigt.cdsl.co.in/BSBLWebAPI/v1.0"`) || !strings.Contains(csp, "form-action https://mockapigt.cdsl.co.in;") || !strings.Contains(csp, "script-src 'nonce-") {
		t.Fatalf("go page %d %s\n%s", resp.StatusCode, csp, page)
	}
	reqid := regexp.MustCompile(`name="reqid" value="([0-9]+)"`).FindStringSubmatch(string(page))[1]
	// CDSL posts back through the client's browser.
	ret := strings.Replace(path, "/cdsl/go/", "/cdsl/return/", 1)
	enc := cdslTestEncrypt(t, []byte(`{"reqid":"`+reqid+`","sellerboid":"1201090000003333","resstatus":"0","reserror":"","reserrmsg":"","byrresdtls":[{"buyerboid":"1201090000009999","byrreqstatus":"0","buyerresid":"BYRRES9"}]}`))
	resp, err = http.PostForm(srv.URL+ret, netu.Values{"reqid": {reqid}, "slrbyrresdtls": {enc}})
	if err != nil {
		t.Fatal(err)
	}
	page, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "Beneficiaries added") {
		t.Fatalf("return %d %s", resp.StatusCode, page)
	}
	code, m = call("GET", "/v1/cdsl/requests", "vikram.shah", "")
	rs := m["requests"].([]any)
	if code != 200 || rs[0].(map[string]any)["status"] != "Completed" {
		t.Fatalf("list: %d %v", code, m)
	}
	// Unknown tokens are not found, and say nothing.
	if resp, _ := http.Post(srv.URL+"/hooks/demo/cdsl/bo/nope-nope-nope-nope-nope-nope", "application/json", strings.NewReader(`{}`)); resp.StatusCode != 404 {
		t.Fatalf("bad token: %d", resp.StatusCode)
	}
	if resp, _ := http.Get(srv.URL + "/hooks/demo/cdsl/go/nope-nope-nope-nope-nope-nope"); resp.StatusCode != 404 {
		t.Fatalf("bad link: %d", resp.StatusCode)
	}
	if code, _ := call("GET", "/v1/cdsl/demat-check/"+c.UCC, "anita.rao", ""); code != 200 {
		t.Fatalf("demat check: %d", code)
	}
	if code, _ := call("POST", "/v1/cdsl/demat-check/"+c.UCC, "priya.nair", ""); code != 409 && code != 422 {
		t.Fatalf("demat check with the API off: %d", code)
	}
	// The client's CDSL details for the BO upload files.
	if code, m := call("PUT", "/v1/cdsl/bo-details/"+c.UCC, "priya.nair", `{"holders":[{"name":"Hari Shah","pan":"abcph2222h"}],"mode_of_operation":"2","comm_pref":"1"}`); code != 200 || m["mode_of_operation"] != "2" {
		t.Fatalf("details: %d %v", code, m)
	}
	if code, m := call("GET", "/v1/cdsl/bo-details/"+c.UCC, "vikram.shah", ""); code != 200 || m["holders"].([]any)[0].(map[string]any)["pan"] != "ABCPH2222H" {
		t.Fatalf("details: %d %v", code, m)
	}
	if code, _ := call("PUT", "/v1/cdsl/bo-details/"+c.UCC, "priya.nair", `{"mode_of_operation":"7"}`); code != 422 {
		t.Fatalf("bad mode: %d", code)
	}
	if code, m := call("POST", "/v1/cdsl/delete-holder", "priya.nair", `{"ucc":"`+c.UCC+`","holder":2,"died_on":"2026-10-01"}`); code != 201 || m["kind"] != "Modify account" {
		t.Fatalf("delete holder: %d %v", code, m)
	}
}
