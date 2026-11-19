package ops

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/sign"
)

// testClock is a clock the test moves; safe for the fake signer's goroutine.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) add(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	return c.t
}

// tokenCert makes a made-up token certificate and its key, valid from
// from to until.
func tokenCert(t *testing.T, cn string, key crypto.Signer, from, until time.Time) *x509.Certificate {
	t.Helper()
	tpl := &x509.Certificate{SerialNumber: big.NewInt(from.UnixNano() + int64(len(cn))), Subject: pkix.Name{CommonName: cn, Organization: []string{"Test Firm"}},
		Issuer: pkix.Name{CommonName: "Test CA"}, NotBefore: from, NotAfter: until, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c
}

// fakeSigner stands for Ledgerline Signer: it polls and signs with key
// until stopped.
type fakeSigner struct {
	o      *Ops
	tenant string
	key    string
	priv   crypto.Signer
	cert   *x509.Certificate
	stop   context.CancelFunc
	done   chan struct{}
}

func startSigner(t *testing.T, o *Ops, tenant, agentKey string, priv crypto.Signer, cert *x509.Certificate) *fakeSigner {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeSigner{o: o, tenant: tenant, key: agentKey, priv: priv, cert: cert, stop: cancel, done: make(chan struct{})}
	m := SignAgentMeta{Version: "test-1.0", Host: "OFFICE-PC"}
	st, err := o.SignAgentHello(ctx, tenant, agentKey, m, cert.Raw)
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	if st.Status != "approved" {
		t.Fatalf("hello %+v", st)
	}
	go func() {
		defer close(f.done)
		for ctx.Err() == nil {
			res, err := o.SignAgentPoll(ctx, tenant, agentKey, m, 10)
			if err != nil {
				return
			}
			for _, j := range res.Jobs {
				dg, _ := base64.StdEncoding.DecodeString(j.Digest)
				sig, err := priv.Sign(rand.Reader, dg, crypto.SHA256)
				r := SignAgentResultIn{ID: j.ID, Signature: base64.StdEncoding.EncodeToString(sig)}
				if err != nil {
					r = SignAgentResultIn{ID: j.ID, Error: err.Error()}
				}
				_ = o.SignAgentResult(context.Background(), tenant, agentKey, m, r)
			}
		}
	}()
	return f
}

func (f *fakeSigner) Stop() { f.stop(); <-f.done }

func workItem(ctx context.Context, o *Ops, process, key string) (WorkItem, bool) {
	id := "W-" + strings.NewReplacer(":", "-", "|", "-", " ", "", "/", "-").Replace(process+":"+key)
	w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, id)
	return w, err == nil
}

// agentReady: notes generated, a fixed clock at 11:00 on the sample day,
// short waits, and a made-up RSA token certificate.
func agentReady(t *testing.T) (*Ops, context.Context, *testClock, *rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	o, ctx := notesReady(t)
	clk := &testClock{t: time.Date(2026, 10, 16, 11, 0, 0, 0, ist)}
	o.Now = clk.now
	o.SignAgentWait, o.SignAgentPollWait = 2*time.Second, 100*time.Millisecond
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return o, ctx, clk, k, tokenCert(t, "Test Firm Authorised Signatory", k, clk.now().AddDate(0, 0, -10), clk.now().AddDate(1, 0, 0))
}

func TestSignAgentEndToEnd(t *testing.T) {
	o, ctx, _, k, cert := agentReady(t)
	if _, err := o.ConnectSignAgent(ctx, T, "priya.nair", nil, ""); err == nil {
		t.Fatal("a maker connected the signer")
	}
	ak, err := o.ConnectSignAgent(ctx, T, "admin", nil, "Mumbai")
	if err != nil || !strings.HasPrefix(ak.Key, "llsign."+T+".") || ak.View.Mode != "agent" || ak.View.Ready {
		t.Fatalf("connect %+v %v", ak, err)
	}
	if a := o.signAgent(ctx, T); a.KeyHash == "" || strings.Contains(a.KeyHash, ak.Key[len(ak.Key)-64:]) {
		t.Fatal("the key is stored, not its hash")
	}
	m := SignAgentMeta{Version: "test-1.0", Host: "OFFICE-PC"}
	// A wrong key, or a key of another firm, is refused.
	bad := newSignAgentKey(T)
	if _, err := o.SignAgentHello(ctx, T, bad, m, cert.Raw); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong key: %v", err)
	}
	if _, err := o.SignAgentPoll(ctx, T, "llsign.beta."+ak.Key[len(ak.Key)-64:], m, 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("key with another firm's name: %v", err)
	}
	// First connect: the certificate waits for approval and gets no work.
	st, err := o.SignAgentHello(ctx, T, ak.Key, m, cert.Raw)
	if err != nil || st.Status != "waiting_for_approval" || st.Certificate == nil {
		t.Fatalf("hello %+v %v", st, err)
	}
	sum := sha256.Sum256([]byte("a document"))
	j, err := o.newSignJob(ctx, T, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	if res, err := o.SignAgentPoll(ctx, T, ak.Key, m, 10); err != nil || len(res.Jobs) != 0 || res.Status != "waiting_for_approval" {
		t.Fatalf("unapproved poll %+v %v", res, err)
	}
	info := sign.CertInfo(cert)
	w, ok := workItem(ctx, o, "signing", "agent-cert-"+info.SHA256[:16])
	if !ok || w.Status != "Open" || w.Role != RoleCompliance {
		t.Fatalf("approval work item %+v %v", w, ok)
	}
	v, _ := o.Signing(ctx, T, "vikram.shah")
	if v.Agent == nil || v.Agent.Pending == nil || v.Agent.Pending.SHA256 != info.SHA256 || v.Ready || !v.Agent.Connected || v.Agent.Host != "OFFICE-PC" {
		t.Fatalf("view %+v %+v", v, v.Agent)
	}
	// Approval: not by a maker; only the certificate shown.
	if _, err := o.ApproveSignAgentCert(ctx, T, "priya.nair", info.SHA256); err == nil {
		t.Fatal("a maker approved the certificate")
	}
	if _, err := o.ApproveSignAgentCert(ctx, T, "vikram.shah", strings.Repeat("AB", 32)); err == nil {
		t.Fatal("approved a different certificate")
	}
	v, err = o.ApproveSignAgentCert(ctx, T, "vikram.shah", info.SHA256)
	if err != nil || v.Agent.Cert == nil || v.Agent.Pending != nil || !v.Agent.CertMatches || v.Agent.ApprovedBy != "vikram.shah" {
		t.Fatalf("approve %+v %v", v.Agent, err)
	}
	if w, _ := workItem(ctx, o, "signing", "agent-cert-"+info.SHA256[:16]); w.Status != "Done" {
		t.Fatalf("approval work item not closed: %+v", w)
	}
	// The job made while the certificate waited is still there; the
	// signer now takes and signs it.
	fs := startSigner(t, o, T, ak.Key, k, cert)
	defer fs.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		cur, _, _ := docs.Get[SignJob](ctx, o.Docs, T, cSignJob, j.ID)
		if cur.Status == "Signed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %+v", cur)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A contract note goes out signed by the token's certificate.
	ns, _ := o.Notes(ctx, T, D)
	d, err := o.sendNote(ctx, T, "priya.nair", ns[0])
	if err != nil || len(d.Files) != 1 || d.Files[0].Signer != "Test Firm Authorised Signatory" {
		t.Fatalf("send %+v %v", d, err)
	}
	_, data, _ := o.DispatchFile(ctx, T, "vikram.shah", d.ID, false)
	ver, whole, err := VerifyPDF(data)
	if err != nil || !whole || ver.Signer.SHA256 != info.SHA256 || strings.Contains(string(data), "TEST SIGNATURE") {
		t.Fatalf("verify %v %v %+v", whole, err, ver.Signer)
	}
	// Check signing signs a probe through the signer.
	if _, err := o.CheckSigning(ctx, T, "priya.nair"); err == nil {
		t.Fatal("a maker ran the check")
	}
	chk, err := o.CheckSigning(ctx, T, "vikram.shah")
	if err != nil || !chk.OK || chk.Signer.SHA256 != info.SHA256 || !chk.View.Ready {
		t.Fatalf("check %+v %v", chk, err)
	}
	// The day's count: the first job, the note and the probe.
	for i := 0; i < 200 && o.signAgent(ctx, T).Signed < 3; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if v, _ := o.Signing(ctx, T, "admin"); v.Agent.SignedToday != 3 || v.Agent.FailedToday != 0 {
		t.Fatalf("count %+v", v.Agent)
	}
	// Another firm's signer sees none of this firm's jobs, and its key
	// does not work here.
	bk, err := o.ConnectSignAgent(ctx, "beta", "admin", cert.Raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SignAgentPoll(ctx, T, bk.Key, m, 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("beta's key on demo: %v", err)
	}
	if _, err := o.SignAgentHello(ctx, "beta", bk.Key, m, cert.Raw); err != nil {
		t.Fatal(err)
	}
	if _, err := o.newSignJob(ctx, T, sum[:]); err != nil {
		t.Fatal(err)
	}
	if res, err := o.SignAgentPoll(ctx, "beta", bk.Key, m, 10); err != nil || res.Status != "approved" || len(res.Jobs) != 0 {
		t.Fatalf("beta saw %+v %v", res, err)
	}
}

func TestSignAgentRejectsBadSignatures(t *testing.T) {
	o, ctx, _, _, cert := agentReady(t)
	ak, err := o.ConnectSignAgent(ctx, T, "admin", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), "")
	if err != nil || ak.View.Agent.Cert == nil || ak.View.Agent.ApprovedBy != "admin" {
		t.Fatalf("connect with an uploaded certificate %+v %v", ak.View.Agent, err)
	}
	// The signer has the approved certificate but signs with another key.
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	fs := startSigner(t, o, T, ak.Key, other, cert)
	ns, _ := o.Notes(ctx, T, D)
	d, err := o.sendNote(ctx, T, "priya.nair", ns[0])
	fs.Stop()
	if err != nil || d.Status != "Held" || len(d.Files) != 0 || !strings.Contains(d.Reason, "does not match") {
		t.Fatalf("send %+v %v", d, err)
	}
	for i := 0; i < 200 && o.signAgent(ctx, T).Failed < 1; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if v, _ := o.Signing(ctx, T, "admin"); v.Agent.FailedToday != 1 || !strings.Contains(v.Agent.LastError, "does not match") {
		t.Fatalf("failures %+v", v.Agent)
	}
	// A renewed token: a different certificate waits for approval, gets
	// no work, and documents are not signed.
	k2, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	c2 := tokenCert(t, "Test Firm Renewed", k2, o.now().AddDate(0, 0, -1), o.now().AddDate(2, 0, 0))
	m := SignAgentMeta{Version: "test-1.0", Host: "OFFICE-PC"}
	st, err := o.SignAgentHello(ctx, T, ak.Key, m, c2.Raw)
	if err != nil || st.Status != "certificate_differs" {
		t.Fatalf("renewed hello %+v %v", st, err)
	}
	if _, _, err := o.docSigner(ctx, T); err == nil || !strings.Contains(err.Error(), "different certificate") {
		t.Fatalf("signed with an unapproved certificate: %v", err)
	}
	if res, _ := o.SignAgentPoll(ctx, T, ak.Key, m, 10); res.Status != "certificate_differs" || len(res.Jobs) != 0 {
		t.Fatalf("poll %+v", res)
	}
	// Once approved, the ECDSA token signs (raw r‖s from CNG is accepted).
	if _, err := o.ApproveSignAgentCert(ctx, T, "vikram.shah", sign.CertInfo(c2).SHA256); err != nil {
		t.Fatal(err)
	}
	fs = startSigner(t, o, T, ak.Key, rawECDSA{k2}, c2)
	defer fs.Stop()
	d, err = o.sendNote(ctx, T, "priya.nair", ns[0])
	if err != nil || d.Status == "Held" || d.Files[0].Signer != "Test Firm Renewed" {
		t.Fatalf("ecdsa send %+v %v", d, err)
	}
	_, data, _ := o.DispatchFile(ctx, T, "vikram.shah", d.ID, false)
	if _, _, err := VerifyPDF(data); err != nil {
		t.Fatal(err)
	}
}

// rawECDSA signs as Windows CNG does: r‖s, not ASN.1.
type rawECDSA struct{ k *ecdsa.PrivateKey }

func (r rawECDSA) Public() crypto.PublicKey { return r.k.Public() }
func (r rawECDSA) Sign(_ io.Reader, digest []byte, _ crypto.SignerOpts) ([]byte, error) {
	a, b, err := ecdsa.Sign(rand.Reader, r.k, digest)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 64)
	a.FillBytes(out[:32])
	b.FillBytes(out[32:])
	return out, nil
}

func TestSignAgentTimeoutExpiryAndKeyReset(t *testing.T) {
	o, ctx, clk, _, cert := agentReady(t)
	o.SignAgentWait = 300 * time.Millisecond
	ak, err := o.ConnectSignAgent(ctx, T, "admin", cert.Raw, "")
	if err != nil {
		t.Fatal(err)
	}
	m := SignAgentMeta{Version: "test-1.0", Host: "OFFICE-PC"}
	ns, _ := o.Notes(ctx, T, D)
	// Never connected: documents are held at once, without waiting.
	start := time.Now()
	d, err := o.sendNote(ctx, T, "priya.nair", ns[0])
	if err != nil || d.Status != "Held" || !strings.Contains(d.Reason, "not connected") || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("offline send %+v %v", d, err)
	}
	if _, ok := workItem(ctx, o, "dispatch", "unsigned"); ok {
		t.Fatal("an offline signer raised the set-up work item")
	}
	// Connected but not answering: the document gives up after the wait,
	// and the next one does not wait again.
	if st, err := o.SignAgentHello(ctx, T, ak.Key, m, cert.Raw); err != nil || st.Status != "approved" {
		t.Fatalf("hello %+v %v", st, err)
	}
	d, err = o.sendNote(ctx, T, "priya.nair", ns[0])
	if err != nil || d.Status != "Held" || !strings.Contains(d.Reason, "not connected") {
		t.Fatalf("timeout send %+v %v", d, err)
	}
	if _, _, err := o.docSigner(ctx, T); !errors.Is(err, errSignerOffline) {
		t.Fatalf("after a timeout: %v", err)
	}
	// The timed-out job is never handed out; neither is a job older than
	// a few minutes.
	sum := sha256.Sum256([]byte("old document"))
	old, _ := o.newSignJob(ctx, T, sum[:])
	clk.add(4 * time.Minute)
	if res, err := o.SignAgentPoll(ctx, T, ak.Key, m, 10); err != nil || res.Status != "approved" || len(res.Jobs) != 0 {
		t.Fatalf("poll handed out %+v %v", res, err)
	}
	if j, _, _ := docs.Get[SignJob](ctx, o.Docs, T, cSignJob, old.ID); j.Status != "Expired" {
		t.Fatalf("old job %+v", j)
	}
	// A late answer for an expired job is refused.
	if err := o.SignAgentResult(ctx, T, ak.Key, m, SignAgentResultIn{ID: old.ID, Signature: "AAAA"}); !errors.Is(err, ErrState) {
		t.Fatalf("late answer: %v", err)
	}
	// Resetting the key: the old one stops working at once.
	if _, err := o.ResetSignAgentKey(ctx, T, "vikram.shah"); err == nil {
		t.Fatal("compliance reset the key")
	}
	nk, err := o.ResetSignAgentKey(ctx, T, "admin")
	if err != nil || nk.Key == ak.Key {
		t.Fatalf("reset %v", err)
	}
	if _, err := o.SignAgentPoll(ctx, T, ak.Key, m, 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("old key after reset: %v", err)
	}
	if st, err := o.SignAgentHello(ctx, T, nk.Key, m, cert.Raw); err != nil || st.Status != "approved" {
		t.Fatalf("new key %+v %v", st, err)
	}
	// Certificates: expired or not yet valid are refused; one expiring
	// within 30 days is accepted with a warning.
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := o.now()
	if _, err := o.SetSignAgentCert(ctx, T, "admin", tokenCert(t, "Expired", k, now.AddDate(-2, 0, 0), now.AddDate(0, 0, -1)).Raw); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired certificate: %v", err)
	}
	if _, err := o.SetSignAgentCert(ctx, T, "admin", tokenCert(t, "Future", k, now.AddDate(0, 0, 2), now.AddDate(1, 0, 0)).Raw); err == nil {
		t.Fatal("a certificate not yet valid was accepted")
	}
	v, err := o.SetSignAgentCert(ctx, T, "admin", tokenCert(t, "Soon", k, now.AddDate(0, 0, -300), now.AddDate(0, 0, 12)).Raw)
	if err != nil || !strings.Contains(v.Agent.Warning, "expires") {
		t.Fatalf("expiring certificate %+v %v", v.Agent, err)
	}
	if _, err := o.SetSignAgentCert(ctx, T, "admin", []byte("not a certificate")); err == nil {
		t.Fatal("junk accepted as a certificate")
	}
}

// Dispatch holds documents while the signer is away, the Admin gets a work
// item after 10 minutes, and the documents go out signed once it is back.
func TestSignAgentHoldsAndSendsWhenBack(t *testing.T) {
	o, ctx, clk, k, cert := agentReady(t)
	ak, err := o.ConnectSignAgent(ctx, T, "admin", cert.Raw, "")
	if err != nil {
		t.Fatal(err)
	}
	sent, _, err := o.SendNotes(ctx, T, "priya.nair", D, "")
	if err != nil || sent != 0 {
		t.Fatalf("sent %d while the signer was away (%v)", sent, err)
	}
	by := dispatchByStatus(t, o, ctx)
	held := len(by["Held"])
	if held == 0 || len(by["Held"][0].Files) != 0 {
		t.Fatalf("not held: %v", by)
	}
	ns, _ := o.Notes(ctx, T, D)
	if ns[0].Status != "Held" {
		t.Fatalf("note %+v", ns[0])
	}
	// Ten minutes on in working hours: a work item for the Admin.
	o.signAgentTick(ctx, T, clk.add(5*time.Minute))
	if a := o.signAgent(ctx, T); a.OfflineWork != "" {
		t.Fatal("work item after 5 minutes")
	}
	o.signAgentTick(ctx, T, clk.add(6*time.Minute))
	a := o.signAgent(ctx, T)
	w, ok := workItem(ctx, o, "signing", a.OfflineWork)
	if !ok || w.Status != "Open" || w.Role != RoleAdmin || a.OfflineWork != "agent-offline-2026-10-16-1111" {
		t.Fatalf("offline work item %q %+v", a.OfflineWork, w)
	}
	o.signAgentTick(ctx, T, clk.add(time.Minute))
	if b := o.signAgent(ctx, T); b.OfflineWork != a.OfflineWork {
		t.Fatal("a second work item for the same absence")
	}
	// The signer is back: the work item closes and the held notes go out.
	fs := startSigner(t, o, T, ak.Key, k, cert)
	defer fs.Stop()
	if w, _ := workItem(ctx, o, "signing", a.OfflineWork); w.Status != "Done" {
		t.Fatalf("work item not closed: %+v", w)
	}
	o.signAgentTick(ctx, T, clk.now())
	by = dispatchByStatus(t, o, ctx)
	ns, _ = o.Notes(ctx, T, D)
	for _, n := range ns {
		if n.Status == "Held" {
			t.Fatalf("still held: %+v", n)
		}
	}
	signed := 0
	for _, d := range append(append(by["Sent"], by["Physical pending"]...), by["Bounced"]...) {
		if len(d.Files) == 1 && d.Files[0].Signer == "Test Firm Authorised Signatory" && d.Attempt == 2 {
			signed++
		}
	}
	if signed != held {
		t.Fatalf("held %d, sent signed %d", held, signed)
	}
	// Nothing is sent twice at the next minute.
	before := len(by["Sent"])
	o.signAgentTick(ctx, T, clk.add(5*time.Minute))
	if after := dispatchByStatus(t, o, ctx); len(after["Sent"]) != before {
		t.Fatalf("sent again: %d → %d", before, len(after["Sent"]))
	}
	// The day's count is logged once the next day begins.
	for i := 0; i < 200 && o.signAgent(ctx, T).Signed < held; i++ {
		time.Sleep(10 * time.Millisecond) // the signer's last answer is still being counted
	}
	n := o.signAgent(ctx, T).Signed
	fs.Stop()
	o.signAgentTick(ctx, T, clk.add(24*time.Hour))
	if a := o.signAgent(ctx, T); a.Day != "2026-10-17" || a.Signed != 0 || n < held {
		t.Fatalf("day roll %+v (signed %d)", a, n)
	}
}
