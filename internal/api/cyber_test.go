package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"ledgerline/internal/ops"
)

// TestPortalTermsGate: a client's session opens only the terms until they
// are accepted; the unlock-code call answers the same for anyone.
func TestPortalTermsGate(t *testing.T) {
	srv, _, _ := mobileServer(t, true)
	session := func() func(method, path string, body any) (int, map[string]any) {
		jar, _ := cookiejar.New(nil)
		c := &http.Client{Jar: jar}
		return func(method, path string, body any) (int, map[string]any) {
			var rd io.Reader
			if body != nil {
				b, _ := json.Marshal(body)
				rd = bytes.NewReader(b)
			}
			req, _ := http.NewRequest(method, srv.URL+path, rd)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "ledgerline")
			res, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			var out map[string]any
			b, _ := io.ReadAll(res.Body)
			json.Unmarshal(b, &out)
			if out == nil {
				var arr []any
				if json.Unmarshal(b, &arr) == nil {
					out = map[string]any{"list": arr}
				}
			}
			return res.StatusCode, out
		}
	}
	staff := session()
	if st, out := staff("POST", "/v1/auth/login", map[string]string{"user": "farhan.khan", "password": ops.DevPassword}); st != 200 {
		t.Fatalf("staff login %d %v", st, out)
	}
	_, cl := staff("GET", "/v1/clients?date=2026-10-16", nil)
	ucc := ""
	for _, x := range cl["list"].([]any) {
		c := x.(map[string]any)
		if c["status"] == "Active" && strings.Contains(c["email"].(string), "@") {
			ucc = c["ucc"].(string)
			break
		}
	}
	if ucc == "" {
		t.Fatalf("no active client with an e-mail: %v", cl)
	}
	st, out := staff("POST", "/v1/clients/"+ucc+"/portal", map[string]any{})
	if st != 200 {
		t.Fatalf("portal access %d %v", st, out)
	}
	id, pw := out["user"].(string), out["password"].(string)
	cli := session()
	cli("POST", "/v1/auth/login", map[string]string{"user": id, "password": pw})
	if st, out := cli("POST", "/v1/auth/password", map[string]string{"current": pw, "new": "a long client passphrase"}); st != 200 {
		t.Fatalf("password %d %v", st, out)
	}
	if st, out := cli("GET", "/v1/me", nil); st != 200 || out["terms_due"] != true {
		t.Fatalf("me %d %v", st, out)
	}
	if st, out := cli("GET", "/v1/portal?date=2026-10-16", nil); st != 403 || out["error"].(map[string]any)["code"] != "terms_required" {
		t.Fatalf("portal before terms %d %v", st, out)
	}
	_, tm := cli("GET", "/v1/portal/terms", nil)
	if st, out := cli("POST", "/v1/portal/terms", map[string]any{"terms_version": tm["terms_version"], "notice_version": tm["notice_version"]}); st != 200 || out["due"] != false {
		t.Fatalf("accept %d %v", st, out)
	}
	if st, _ := cli("GET", "/v1/portal?date=2026-10-16", nil); st != 200 {
		t.Fatalf("portal after terms %d", st)
	}
	if st, out := cli("GET", "/v1/portal/my-data?ucc="+ucc, nil); st != 200 || out["ucc"] != ucc {
		t.Fatalf("my data %d %v", st, out)
	}
	anon := session()
	_, a := anon("POST", "/v1/auth/unlock-code", map[string]string{"user": "nobody"})
	_, b := anon("POST", "/v1/auth/unlock-code", map[string]string{"user": "farhan.khan"})
	if a["message"] == nil || a["message"] != b["message"] {
		t.Fatalf("unlock-code answers differ: %v %v", a, b)
	}
}
