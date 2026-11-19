package api

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"testing"
	"time"

	"ledgerline/internal/ops"
)

// TestSignAgentRoutes: Ledgerline Signer's calls with its agent key, and
// the signing screen's routes.
func TestSignAgentRoutes(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	srv, o := ipoServer(t, now)
	o.SignAgentPollWait = 50 * time.Millisecond
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "Test Token Signatory"}, NotBefore: now.AddDate(0, 0, -1), NotAfter: now.AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)

	if c := ipoCall(t, srv, "POST", "/v1/signing/agent", "priya.nair", map[string]string{}, nil); c != 403 {
		t.Fatalf("maker connected: %d", c)
	}
	var conn struct {
		Key     string          `json:"key"`
		Server  string          `json:"server"`
		Signing ops.SigningView `json:"signing"`
	}
	if c := ipoCall(t, srv, "POST", "/v1/signing/agent", "admin", map[string]string{"location": "Mumbai"}, &conn); c != 201 || conn.Key == "" || conn.Signing.Mode != "agent" {
		t.Fatalf("connect %d %+v", c, conn)
	}
	agent := func(path, k string, body any, out any) int {
		t.Helper()
		j, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", srv.URL+"/v1/signing/agent/"+path, bytes.NewReader(j))
		if k != "" {
			req.Header.Set("Authorization", "Bearer "+k)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if out != nil {
			_ = json.Unmarshal(b, out)
		}
		return res.StatusCode
	}
	hello := map[string]string{"cert_der_b64": base64.StdEncoding.EncodeToString(der), "agent_version": "1.0", "host": "OFFICE-PC"}
	if c := agent("hello", "", hello, nil); c != 401 {
		t.Fatalf("no key: %d", c)
	}
	if c := agent("hello", key, hello, nil); c != 401 {
		t.Fatalf("an API key as agent key: %d", c)
	}
	var st ops.SignAgentStatus
	if c := agent("hello", conn.Key, hello, &st); c != 200 || st.Status != "waiting_for_approval" {
		t.Fatalf("hello %d %+v", c, st)
	}
	var poll ops.SignAgentPollResult
	if c := agent("poll", conn.Key, map[string]any{"max": 5, "agent_version": "1.0", "host": "OFFICE-PC"}, &poll); c != 200 || poll.Status != "waiting_for_approval" || len(poll.Jobs) != 0 {
		t.Fatalf("poll %d %+v", c, poll)
	}
	if c := ipoCall(t, srv, "POST", "/v1/signing/agent/approve", "vikram.shah", map[string]string{"sha256": st.Certificate.SHA256}, nil); c != 200 {
		t.Fatalf("approve %d", c)
	}
	// Check signing: the probe is signed by this test acting as the signer.
	done := make(chan ops.SigningCheck, 1)
	go func() {
		var chk ops.SigningCheck
		req, _ := http.NewRequest("POST", srv.URL+"/v1/signing/check", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Actor", "vikram.shah")
		if res, err := http.DefaultClient.Do(req); err == nil {
			_ = json.NewDecoder(res.Body).Decode(&chk)
			res.Body.Close()
		}
		done <- chk
	}()
	var jobs ops.SignAgentPollResult
	for i := 0; i < 100 && len(jobs.Jobs) == 0; i++ {
		if c := agent("poll", conn.Key, map[string]any{"agent_version": "1.0", "host": "OFFICE-PC"}, &jobs); c != 200 || jobs.Status != "approved" {
			t.Fatalf("poll %d %+v", c, jobs)
		}
	}
	if len(jobs.Jobs) != 1 || jobs.Jobs[0].Hash != "SHA256" {
		t.Fatalf("jobs %+v", jobs)
	}
	dg, _ := base64.StdEncoding.DecodeString(jobs.Jobs[0].Digest)
	sig, _ := k.Sign(rand.Reader, dg, crypto.SHA256)
	if c := agent("result", conn.Key, map[string]string{"id": jobs.Jobs[0].ID, "signature_b64": base64.StdEncoding.EncodeToString(sig), "agent_version": "1.0", "host": "OFFICE-PC"}, nil); c != 200 {
		t.Fatalf("result %d", c)
	}
	if chk := <-done; !chk.OK || chk.Signer.Name != "Test Token Signatory" || chk.View.Agent == nil || chk.View.Agent.Host != "OFFICE-PC" {
		t.Fatalf("check %+v", chk)
	}
	// Reset: the old key is refused; wrong keys are limited per address.
	var rs struct {
		Key string `json:"key"`
	}
	if c := ipoCall(t, srv, "POST", "/v1/signing/agent/reset-key", "admin", nil, &rs); c != 200 || rs.Key == "" || rs.Key == conn.Key {
		t.Fatalf("reset %d", c)
	}
	if c := agent("poll", conn.Key, map[string]any{}, nil); c != 401 {
		t.Fatalf("old key %d", c)
	}
	last := 0
	for i := 0; i < 25; i++ {
		last = agent("poll", "llsign.demo."+conn.Key[len(conn.Key)-64:], map[string]any{}, nil)
	}
	if last != 429 {
		t.Fatalf("wrong keys not limited: %d", last)
	}
}
