package main

import (
	"bufio"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/api"
	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/vault"
)

// pemFile writes a made-up RSA certificate and key as PEM, as -pfx reads.
func pemFile(t *testing.T) (string, *x509.Certificate) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "Test Firm Signatory"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	kd, _ := x509.MarshalPKCS8PrivateKey(k)
	p := filepath.Join(t.TempDir(), "signer.pem")
	os.WriteFile(p, append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd})...), 0o600)
	c, _ := x509.ParseCertificate(der)
	return p, c
}

// fakeLedgerline speaks the agent protocol: two jobs, then none.
type fakeLedgerline struct {
	mu      sync.Mutex
	cert    *x509.Certificate
	jobs    map[string][]byte
	given   bool
	results map[string]string // id → "ok" or the error
	bodies  []string
}

func (f *fakeLedgerline) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer llsign.demo.key" {
		w.WriteHeader(401)
		return
	}
	var b struct {
		CertDER string `json:"cert_der_b64"`
		Version string `json:"agent_version"`
		Host    string `json:"host"`
		Max     int    `json:"max"`
		ID      string `json:"id"`
		Sig     string `json:"signature_b64"`
		Error   string `json:"error"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // as Ledgerline does
	if err := dec.Decode(&b); err != nil || b.Version != version || b.Host == "" {
		w.WriteHeader(400)
		return
	}
	switch r.URL.Path {
	case "/v1/signing/agent/hello":
		der, _ := base64.StdEncoding.DecodeString(b.CertDER)
		if c, err := x509.ParseCertificate(der); err != nil || !c.Equal(f.cert) {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "approved", "message": "Certificate approved: signing documents."})
	case "/v1/signing/agent/poll":
		out := []map[string]string{}
		if !f.given {
			f.given = true
			for id, d := range f.jobs {
				out = append(out, map[string]string{"id": id, "digest_b64": base64.StdEncoding.EncodeToString(d), "hash": "SHA256"})
			}
		} else {
			time.Sleep(20 * time.Millisecond)
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "approved", "message": "Certificate approved: signing documents.", "jobs": out})
	case "/v1/signing/agent/result":
		sig, _ := base64.StdEncoding.DecodeString(b.Sig)
		switch {
		case b.Error != "":
			f.results[b.ID] = b.Error
		case rsa.VerifyPKCS1v15(f.cert.PublicKey.(*rsa.PublicKey), crypto.SHA256, f.jobs[b.ID], sig) == nil:
			f.results[b.ID] = "ok"
		default:
			f.results[b.ID] = "bad signature"
		}
		f.bodies = append(f.bodies, b.Sig+b.Error)
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	default:
		w.WriteHeader(404)
	}
}

func (f *fakeLedgerline) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.results) }

// logs collects the console lines.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) add(s string) { l.mu.Lock(); l.lines = append(l.lines, s); l.mu.Unlock() }
func (l *logs) all() string  { l.mu.Lock(); defer l.mu.Unlock(); return strings.Join(l.lines, "\n") }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 500 && !cond(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("timed out")
	}
}

func TestSignerFileBackendWithFakeServer(t *testing.T) {
	path, cert := pemFile(t)
	be, err := openFile(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if ms, err := selfTest(be); err != nil || ms < 0 {
		t.Fatalf("self-test %v", err)
	}
	d1, d2 := sha256.Sum256([]byte("one")), sha256.Sum256([]byte("two"))
	f := &fakeLedgerline{cert: cert, jobs: map[string][]byte{"J1": d1[:], "J2": d2[:]}, results: map[string]string{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	var lg logs
	c := newClient(srv.URL, "llsign.demo.key", "OFFICE-PC", be, lg.add)
	c.retry = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.run(ctx) }()
	waitFor(t, func() bool { return f.count() == 2 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.results["J1"] != "ok" || f.results["J2"] != "ok" {
		t.Fatalf("results %v", f.results)
	}
	out := lg.all()
	if !strings.Contains(out, "Signed 2 documents") || strings.Contains(out, base64.StdEncoding.EncodeToString(d1[:])) {
		t.Fatalf("console:\n%s", out)
	}
	// A wrong key stops the signer with the reason.
	c2 := newClient(srv.URL, "llsign.demo.wrong", "OFFICE-PC", be, lg.add)
	if err := c2.run(context.Background()); !errors.Is(err, errKeyRefused) {
		t.Fatalf("wrong key: %v", err)
	}
	// Plain http only to this computer.
	if _, err := checkServer("http://bo.example.in"); err == nil {
		t.Fatal("plain http accepted")
	}
	if _, err := checkServer("https://bo.example.in/"); err != nil {
		t.Fatal(err)
	}
}

// flakyToken fails to sign until it is opened again, as a pulled-out
// token does.
type flakyToken struct {
	backend
	mu      sync.Mutex
	broken  bool
	reopens int
}

func (f *flakyToken) Sign(r io.Reader, d []byte, o crypto.SignerOpts) ([]byte, error) {
	f.mu.Lock()
	broken := f.broken
	f.mu.Unlock()
	if broken {
		return nil, errors.New("SCARD_W_REMOVED_CARD")
	}
	return f.backend.Sign(r, d, o)
}

func (f *flakyToken) Reopen() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reopens++
	if f.reopens < 2 {
		return errors.New("no token")
	}
	f.broken = false
	return nil
}

func TestSignerTokenPulledOut(t *testing.T) {
	path, cert := pemFile(t)
	be, _ := openFile(path, "")
	tok := &flakyToken{backend: be, broken: true}
	d1, d2 := sha256.Sum256([]byte("one")), sha256.Sum256([]byte("two"))
	f := &fakeLedgerline{cert: cert, jobs: map[string][]byte{"J1": d1[:], "J2": d2[:]}, results: map[string]string{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	var lg logs
	c := newClient(srv.URL, "llsign.demo.key", "OFFICE-PC", tok, lg.add)
	c.retry, c.tokenRetry = 10*time.Millisecond, 10*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.run(ctx) }()
	waitFor(t, func() bool { tok.mu.Lock(); defer tok.mu.Unlock(); return tok.reopens >= 2 })
	cancel()
	<-done
	// Both jobs were answered with the error; the signer kept running and
	// tried the token again.
	if f.count() != 2 || !strings.Contains(f.results["J1"]+f.results["J2"], "REMOVED_CARD") {
		t.Fatalf("results %v", f.results)
	}
	if out := lg.all(); !strings.Contains(out, "2 not signed") || !strings.Contains(out, "available again") {
		t.Fatalf("console:\n%s", out)
	}
}

// TestSignerWithLedgerline runs the signer against Ledgerline's own API:
// set up, approve, and a test document signed through it.
func TestSignerWithLedgerline(t *testing.T) {
	ops.PasswordIterations = 1000
	reg, err := ingest.LoadDir("../../config/adapters")
	if err != nil {
		t.Fatal(err)
	}
	sf, _ := os.Open("../../config/charges/sample-schedule.json")
	sched, _ := charges.LoadSchedule(sf)
	sf.Close()
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	o.SignAgentPollWait = 100 * time.Millisecond
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(a, o, map[string]string{"test-key-0123456789abcdef": "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true))
	defer srv.Close()
	path, cert := pemFile(t)
	ak, err := o.ConnectSignAgent(ctx, "demo", "admin", cert.Raw, "Mumbai")
	if err != nil {
		t.Fatal(err)
	}
	// Set up as an office would, with the key typed in.
	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := setup(cfg, srv.URL, "", path, bufio.NewReader(strings.NewReader(ak.Key+"\n"))); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(cfg)
	if err != nil || c.PFX != path || c.Key == "" || c.Server != srv.URL {
		t.Fatalf("config %+v %v", c, err)
	}
	key, _ := unprotect(c.Key)
	be, _ := openFile(path, "")
	var lg logs
	cl := newClient(c.Server, string(key), "OFFICE-PC", be, lg.add)
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- cl.run(rctx) }()
	waitFor(t, func() bool {
		v, _ := o.Signing(ctx, "demo", "admin")
		return v.Agent != nil && v.Agent.Connected && v.Agent.CertMatches
	})
	chk, err := o.CheckSigning(ctx, "demo", "admin")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err != nil || !chk.OK || chk.Signer.Name != "Test Firm Signatory" || chk.View.Agent.Host != "OFFICE-PC" || chk.View.Agent.Version != version {
		t.Fatalf("check %+v %v\n%s", chk, err, lg.all())
	}
}
