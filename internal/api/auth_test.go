package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"testing"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

func opsServer(t *testing.T) *httptest.Server {
	t.Helper()
	ops.PasswordIterations = 1000
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	if err := o.SeedDev(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true))
	t.Cleanup(srv.Close)
	return srv
}

func TestBrowserSignIn(t *testing.T) {
	srv := opsServer(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	do := func(method, path string, body any, csrf bool) (int, map[string]any) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rd)
		req.Header.Set("Content-Type", "application/json")
		if csrf {
			req.Header.Set("X-Requested-With", "ledgerline")
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	if st, _ := do("GET", "/v1/me", nil, false); st != 401 {
		t.Fatalf("me before sign-in: %d", st)
	}
	if st, _ := do("POST", "/v1/auth/login", map[string]string{"user": "priya.nair", "password": "wrong"}, false); st != 401 {
		t.Fatalf("wrong password: %d", st)
	}
	st, out := do("POST", "/v1/auth/login", map[string]string{"user": "priya.nair", "password": ops.DevPassword}, false)
	if st != 200 {
		t.Fatalf("login: %d %v", st, out)
	}
	if u, _ := url(srv.URL); len(jar.Cookies(u)) != 1 {
		t.Fatal("no session cookie")
	}
	st, out = do("GET", "/v1/me", nil, false)
	if st != 200 || out["actor"] != "priya.nair" || out["role"] != "Ops maker" {
		t.Fatalf("me: %d %v", st, out)
	}
	// Changes need the CSRF header; the actor comes from the session.
	if st, _ := do("POST", "/v1/recon/runs", map[string]string{"date": "2026-10-16"}, false); st != 403 {
		t.Fatalf("change without CSRF header: %d", st)
	}
	if st, out := do("POST", "/v1/settings", nil, true); st == 200 {
		t.Fatalf("unexpected: %v", out)
	}
	// A maker cannot add users; the admin can.
	if st, _ := do("POST", "/v1/users", map[string]string{"id": "new.user", "name": "New", "role": "Ops maker"}, true); st != 403 {
		t.Fatalf("maker added a user: %d", st)
	}
	do("POST", "/v1/auth/logout", nil, true)
	if st, _ := do("GET", "/v1/me", nil, false); st != 401 {
		t.Fatalf("me after sign-out: %d", st)
	}
	do("POST", "/v1/auth/login", map[string]string{"user": "admin", "password": ops.DevPassword}, false)
	st, out = do("POST", "/v1/users", map[string]string{"id": "new.user", "name": "New User", "role": "Ops maker"}, true)
	pw, _ := out["temp_password"].(string)
	if st != 200 || len(pw) < 16 {
		t.Fatalf("create user: %d %v", st, out)
	}
	do("POST", "/v1/auth/logout", nil, true)
	// The new user must change the password before anything else.
	do("POST", "/v1/auth/login", map[string]string{"user": "new.user", "password": pw}, false)
	if st, out := do("GET", "/v1/clients?date=2026-10-16", nil, false); st != 403 || out["error"].(map[string]any)["code"] != "setup_required" {
		t.Fatalf("restricted session: %d %v", st, out)
	}
	if st, out := do("POST", "/v1/auth/password", map[string]string{"current": pw, "new": "a much longer passphrase"}, true); st != 200 {
		t.Fatalf("change password: %d %v", st, out)
	}
	if st, _ := do("GET", "/v1/clients?date=2026-10-16", nil, false); st != 200 {
		t.Fatalf("after password change: %d", st)
	}
	// API keys still work for integrations.
	req, _ := http.NewRequest("GET", srv.URL+"/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 200 {
		t.Fatalf("API key: %d", res.StatusCode)
	}
}

func url(s string) (*neturl.URL, error) { return neturl.Parse(s) }
