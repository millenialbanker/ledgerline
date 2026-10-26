package ops

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/sign"
)

func notesReady(t *testing.T) (*Ops, context.Context) {
	t.Helper()
	o, ctx := setup(t)
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBilling(ctx, T, "farhan.khan", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	if n, err := o.GenerateNotes(ctx, T, "priya.nair", D); err != nil || n == 0 {
		t.Fatalf("notes %d %v", n, err)
	}
	return o, ctx
}

// testCert makes a signing certificate valid for a year, as PEM.
func testCert(t *testing.T, cn string) ([]byte, *sign.Signer) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn, Organization: []string{"Test Firm"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalPKCS8PrivateKey(k)
	cert, _ := x509.ParseCertificate(der)
	out := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd})...)
	return out, &sign.Signer{Key: k, Cert: cert}
}

func dispatchByStatus(t *testing.T, o *Ops, ctx context.Context) map[string][]Dispatch {
	v, err := o.DispatchRegister(ctx, T, "vikram.shah", DispatchFilter{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]Dispatch{}
	for _, d := range v.Rows {
		out[d.Status] = append(out[d.Status], d)
	}
	return out
}

func TestDispatchSignedNotes(t *testing.T) {
	o, ctx := notesReady(t)
	// One client prefers paper.
	ns, _ := o.Notes(ctx, T, D)
	paper := ns[len(ns)-1].UCC
	if _, err := docs.Update(ctx, o.Docs, T, cClient, paper, "admin", func(c *Client) error { c.PhysicalNotes = true; return nil }); err != nil {
		t.Fatal(err)
	}
	sent, bounced, err := o.SendNotes(ctx, T, "priya.nair", D, "")
	if err != nil || sent == 0 {
		t.Fatalf("sent %d bounced %d %v", sent, bounced, err)
	}
	by := dispatchByStatus(t, o, ctx)
	t.Logf("register: sent %d bounced %d paper %d held %d", len(by["Sent"]), len(by["Bounced"]), len(by["Physical pending"]), len(by["Held"]))
	if len(by["Held"]) != 0 || len(by["Physical pending"]) == 0 {
		t.Fatalf("register %v", by)
	}
	d := by["Sent"][0]
	if !d.Simulated || len(d.Files) != 1 || !d.Files[0].Signed {
		t.Fatalf("dispatch %+v", d)
	}
	// The stored file is a signed PDF that covers the whole document.
	name, data, err := o.DispatchFile(ctx, T, "vikram.shah", d.ID, false)
	if err != nil || !strings.HasSuffix(name, ".pdf") || !strings.HasPrefix(string(data), "%PDF-") {
		t.Fatalf("file %s %v", name, err)
	}
	v, whole, err := VerifyPDF(data)
	if err != nil || !whole || !strings.Contains(v.Signer.Name, "TEST") {
		t.Fatalf("verify %v %v %+v", whole, err, v.Signer)
	}
	chk, err := o.VerifyDispatch(ctx, T, "vikram.shah", d.ID)
	if err != nil || chk["ok"] != true {
		t.Fatalf("verify dispatch %v %v", chk, err)
	}
	// A branch user cannot read the register.
	if _, err := o.DispatchRegister(ctx, T, "meera.iyer", DispatchFilter{}); err == nil {
		t.Fatal("branch user saw the register")
	}
	// The client sees their own document in the portal, and nobody else's.
	if _, _, err := o.DispatchFile(ctx, T, "priya.nair", d.ID, true); err == nil {
		t.Fatal("staff login used the portal route")
	}
	// A later bounce asks for a printed copy; the courier closes it.
	b, err := o.MarkBounced(ctx, T, "priya.nair", d.ID, "552 mailbox full")
	if err != nil || b.Status != "Bounced" {
		t.Fatalf("bounce %v", err)
	}
	if _, err := o.MarkPhysical(ctx, T, "priya.nair", d.ID, "x"); err == nil {
		t.Fatal("courier reference not required")
	}
	p, err := o.MarkPhysical(ctx, T, "priya.nair", d.ID, "BlueDart 12345678")
	if err != nil || p.Status != "Physical sent" {
		t.Fatalf("physical %v", err)
	}
	if _, err := o.Resend(ctx, T, "priya.nair", d.ID); err == nil {
		t.Fatal("resent a delivered document")
	}
	// Bounce report import matches by address.
	if len(by["Sent"]) > 1 {
		e := by["Sent"][1]
		n, err := o.ImportBounces(ctx, T, "priya.nair", []byte("email,date,reason\n"+e.To+","+e.Created[:10]+",550 no such user\n"))
		if err != nil || n != 1 {
			t.Fatalf("import %d %v", n, err)
		}
	}
	// Sending again skips what was delivered.
	sent2, _, _ := o.SendNotes(ctx, T, "priya.nair", D, "")
	if sent2 != 0 {
		t.Fatalf("sent again %d", sent2)
	}
	csv, err := o.DispatchCSV(ctx, T, "vikram.shah", DispatchFilter{Status: "Physical sent"})
	if err != nil || !strings.Contains(string(csv), "BlueDart 12345678") {
		t.Fatalf("csv %s %v", csv, err)
	}
}

func TestDispatchHeldWithoutSignature(t *testing.T) {
	o, ctx := notesReady(t)
	if _, err := o.SetSigningOff(ctx, T, "admin"); err != nil {
		t.Fatal(err)
	}
	sent, _, err := o.SendNotes(ctx, T, "priya.nair", D, "")
	if err != nil || sent != 0 {
		t.Fatalf("sent %d unsigned (%v)", sent, err)
	}
	by := dispatchByStatus(t, o, ctx)
	if len(by["Held"]) == 0 || len(by["Held"][0].Files) != 0 {
		t.Fatalf("not held: %v", by)
	}
	// With a certificate file the held notes go out, signed by the firm.
	file, _ := testCert(t, "Test Firm Compliance Officer")
	if _, err := o.SetSigningFile(ctx, T, "priya.nair", file, "", "Mumbai"); err == nil {
		t.Fatal("maker set the certificate")
	}
	sv, err := o.SetSigningFile(ctx, T, "admin", file, "", "Mumbai")
	if err != nil || !sv.Ready || sv.Test || sv.Info.Name == "" {
		t.Fatalf("set file %+v %v", sv, err)
	}
	d, err := o.Resend(ctx, T, "priya.nair", by["Held"][0].ID)
	if err != nil || d.Status != "Sent" || d.Attempt != 2 {
		t.Fatalf("resend %+v %v", d, err)
	}
	_, data, _ := o.DispatchFile(ctx, T, "vikram.shah", d.ID, false)
	v, whole, err := VerifyPDF(data)
	if err != nil || !whole || v.Signer.Name != "Test Firm Compliance Officer" || strings.Contains(string(data), "TEST SIGNATURE") {
		t.Fatalf("verify %v %v %+v", whole, err, v.Signer)
	}
}

func TestDispatchRemoteSigner(t *testing.T) {
	o, ctx := notesReady(t)
	_, s := testCert(t, "Remote HSM Signer")
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k-123456" {
			w.WriteHeader(401)
			return
		}
		var b struct {
			SHA256 string `json:"sha256"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		dg, _ := hex.DecodeString(b.SHA256)
		cms, err := s.SignDigest(dg, time.Now())
		if err != nil {
			w.WriteHeader(500)
			return
		}
		calls++
		json.NewEncoder(w).Encode(map[string]string{"cms": base64.StdEncoding.EncodeToString(cms)})
	}))
	defer srv.Close()
	if _, err := o.SetSigningRemote(ctx, T, "admin", srv.URL, "k-123456", "Pune"); err == nil {
		t.Fatal("plain http accepted outside development")
	}
	o.DevCodes = true
	if _, err := o.SetSigningRemote(ctx, T, "admin", srv.URL, "wrong", "Pune"); err == nil {
		t.Fatal("bad key accepted")
	}
	sv, err := o.SetSigningRemote(ctx, T, "admin", srv.URL, "k-123456", "Pune")
	if err != nil || sv.Info.Name != "Remote HSM Signer" {
		t.Fatalf("remote %+v %v", sv, err)
	}
	ns, _ := o.Notes(ctx, T, D)
	d, err := o.sendNote(ctx, T, "priya.nair", ns[0])
	if err != nil || len(d.Files) != 1 || d.Files[0].Signer != "Remote HSM Signer" {
		t.Fatalf("send %+v %v", d, err)
	}
	if calls < 2 {
		t.Fatalf("signer called %d times", calls)
	}
}

// fakeSMTP is a small mail server with STARTTLS for the mailer test.
type fakeSMTP struct {
	ln     net.Listener
	mu     sync.Mutex
	mails  []string
	reject string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	pemData, s := testCert(t, "127.0.0.1")
	_ = pemData
	cert := tls.Certificate{Certificate: [][]byte{s.Cert.Raw}, PrivateKey: s.Key}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c, &tls.Config{Certificates: []tls.Certificate{cert}})
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSMTP) serve(c net.Conn, cfg *tls.Config) {
	defer c.Close()
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	say := func(s string) { w.WriteString(s + "\r\n"); w.Flush() }
	say("220 fake ESMTP")
	secure := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			if secure {
				say("250-fake\r\n250 AUTH PLAIN")
			} else {
				say("250-fake\r\n250 STARTTLS")
			}
		case cmd == "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, cfg)
			if tc.Handshake() != nil {
				return
			}
			c, secure = tc, true
			r, w = bufio.NewReader(tc), bufio.NewWriter(tc)
		case strings.HasPrefix(cmd, "AUTH"):
			say("235 ok")
		case strings.HasPrefix(cmd, "MAIL"):
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT"):
			if f.reject != "" && strings.Contains(strings.ToLower(line), f.reject) {
				say("550 5.1.1 no such user")
			} else {
				say("250 ok")
			}
		case cmd == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.mails = append(f.mails, b.String())
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		case cmd == "RSET" || cmd == "NOOP":
			say("250 ok")
		default:
			say("502 no")
		}
	}
}

func TestDispatchSMTP(t *testing.T) {
	o, ctx := notesReady(t)
	f := newFakeSMTP(t)
	_, port, _ := net.SplitHostPort(f.ln.Addr().String())
	m, err := ParseSMTP("smtp://notes%40firm.in:secret@127.0.0.1:" + port + "?from=notes@firm.in")
	if err != nil {
		t.Fatal(err)
	}
	m.InsecureSkipVerify, m.Timeout = true, 10*time.Second
	o.Mailer = m
	ns, _ := o.Notes(ctx, T, D)
	c, _ := o.Client(ctx, T, ns[0].UCC)
	d, err := o.sendNote(ctx, T, "priya.nair", ns[0])
	if err != nil || d.Status != "Sent" || d.Simulated || d.MessageID == "" {
		t.Fatalf("send %+v %v", d, err)
	}
	if len(f.mails) != 1 || !strings.Contains(f.mails[0], "application/pdf") || !strings.Contains(f.mails[0], "To: "+c.Email) {
		t.Fatalf("mail %d", len(f.mails))
	}
	// A 5xx on the recipient is a bounce, with a work item for paper.
	f.reject = strings.ToLower(ns[1].UCC)
	c2, _ := o.Client(ctx, T, ns[1].UCC)
	f.reject = strings.ToLower(c2.Email)
	d2, err := o.sendNote(ctx, T, "priya.nair", ns[1])
	if err != nil || d2.Status != "Bounced" || !strings.Contains(d2.Reason, "550") {
		t.Fatalf("bounce %+v %v", d2, err)
	}
	if _, err := ParseSMTP("smtp://host:25"); err == nil {
		t.Fatal("from address not required")
	}
}

func TestStatementsSent(t *testing.T) {
	o, ctx := notesReady(t)
	r, err := o.SendStatements(ctx, T, "priya.nair", DocMargin, D)
	if err != nil || r.Sent == 0 || len(r.Errors) > 0 {
		t.Fatalf("margin %+v %v", r, err)
	}
	again, _ := o.SendStatements(ctx, T, "priya.nair", DocMargin, D)
	if again.Sent != 0 || again.Skipped == 0 {
		t.Fatalf("margin again %+v", again)
	}
	l, err := o.SendStatements(ctx, T, "priya.nair", DocLedger, D[:7])
	if err != nil || l.Sent == 0 || len(l.Errors) > 0 {
		t.Fatalf("ledger %+v %v", l, err)
	}
	dp, err := o.SendStatements(ctx, T, "priya.nair", DocDP, D[:7])
	if err != nil || len(dp.Errors) > 0 {
		t.Fatalf("dp %+v %v", dp, err)
	}
	t.Logf("margin %+v ledger %+v dp %+v", r, l, dp)
	if _, err := o.SendStatements(ctx, T, "meera.iyer", DocMargin, D); err == nil {
		t.Fatal("branch user sent statements")
	}
	by := dispatchByStatus(t, o, ctx)
	var margin Dispatch
	for _, d := range by["Sent"] {
		if d.Kind == DocMargin {
			margin = d
		}
	}
	_, data, err := o.DispatchFile(ctx, T, "vikram.shah", margin.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, whole, err := VerifyPDF(data); err != nil || !whole {
		t.Fatalf("margin pdf %v", err)
	}
}
