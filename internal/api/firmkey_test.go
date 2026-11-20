package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
	"ledgerline/internal/vault/kmsfake"
)

func TestFirmKeyAPI(t *testing.T) {
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
	var mu sync.Mutex
	now := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	o.Now = clock
	kms := kmsfake.New("ap-south-2", "444455556666")
	defer kms.Close()
	kms.Now = clock
	kms.AddCreds("AKIAFAKEAPITEST00001", "apiTestSecretKey/madeUp+000000000000000")
	arn := kms.AddKey()
	o.FirmKeyClient = kms.Client()
	srv := httptest.NewServer(New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), false))
	defer srv.Close()
	call := func(method, path, who, body string) (int, string) {
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
		return resp.StatusCode, string(b)
	}
	if c, b := call("GET", "/v1/security/keys", "admin", ""); c != 200 || !strings.Contains(b, `"mode": "ledgerline"`) {
		t.Fatalf("view: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/security/keys/use", "priya.nair", `{"conn":"firmkey-aws"}`); c != 403 {
		t.Fatalf("maker moved the key: %d %s", c, b)
	}
	if _, err := o.SaveConnection(ctx, "demo", "admin", "firmkey-aws", map[string]string{"region": "ap-south-2", "key_arn": arn, "access_key_id": "AKIAFAKEAPITEST00001", "url": kms.URL()},
		map[string]string{"secret_access_key": "apiTestSecretKey/madeUp+000000000000000"}); err != nil {
		t.Fatal(err)
	}
	if c, b := call("POST", "/v1/security/keys/test", "admin", `{"conn":"firmkey-aws"}`); c != 200 || !strings.Contains(b, `"status": "Working"`) {
		t.Fatalf("test: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/security/keys/use", "admin", `{"conn":"firmkey-aws","cache_minutes":5}`); c != 200 || !strings.Contains(b, `"mode": "firm"`) || strings.Contains(b, "apiTestSecretKey") {
		t.Fatalf("use: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/security/keys/rotate", "admin", `{}`); c != 200 || !strings.Contains(b, `"current": 2`) {
		t.Fatalf("rotate: %d %s", c, b)
	}
	if c, b := call("GET", "/v1/clients?date=2026-11-20", "admin", ""); c != 200 {
		t.Fatalf("clients: %d %s", c, b)
	}
	kms.SetEnabled(arn, false)
	mu.Lock()
	now = now.Add(6 * time.Minute)
	mu.Unlock()
	c, b := call("GET", "/v1/clients?date=2026-11-20", "admin", "")
	if c != 503 || !strings.Contains(b, "firm_key_unavailable") || !strings.Contains(b, "Your firm's encryption key is not available (revoked or unreachable)") || strings.Contains(b, "arn:aws") {
		t.Fatalf("revoked: %d %s", c, b)
	}
	if c, b := call("POST", "/v1/security/keys/rotate", "admin", `{}`); c != 503 {
		t.Fatalf("mutating while revoked: %d %s", c, b)
	}
}
