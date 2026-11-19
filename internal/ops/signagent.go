package ops

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/sign"
)

// ---- the USB token signer ----
//
// Many firms keep their Class 3 signing certificate on a USB token (a
// dongle such as ePass2003, ProxKey, mToken, Watchdata or HyperPKI) in a
// computer at the office. Ledgerline runs elsewhere and cannot reach into
// the office, so the computer with the token runs Ledgerline Signer
// (cmd/ledgerline-signer), which connects out to Ledgerline:
//
//	POST /v1/signing/agent/hello  {cert_der_b64, agent_version, host}
//	     → {status: approved | waiting_for_approval | certificate_differs | certificate_invalid}
//	POST /v1/signing/agent/poll   {max, agent_version, host}
//	     → {status, jobs: [{id, digest_b64, hash: "SHA256"}]}   (waits up to ~25 s for work)
//	POST /v1/signing/agent/result {id, signature_b64 | error, agent_version, host}
//
// The signer authenticates with the agent key an Admin makes on Settings →
// Digital signature (shown once; Ledgerline keeps only its SHA-256). The
// key names the firm, so the routes need no firm in the address.
//
// Ledgerline builds the CMS signature itself (sign.SignDigest): the only
// thing that leaves for the office is the SHA-256 of the signed
// attributes, and the token returns a raw signature over it (RSA PKCS #1
// v1.5 with SHA-256, or ECDSA). Every signature is checked against the
// approved certificate before it is used. A document waits for its
// signature in the store (collection signjobs), so any Ledgerline process
// can make the job and any can take the answer; within one process a wake
// signal saves the wait for the next look at the store.
//
// The certificate is fixed by approval: the Admin uploads it in advance,
// or the signer sends it on first connect and an Admin or the Compliance
// officer approves it. A different certificate later (a renewed token)
// waits for the same approval; nothing is signed with it until then.

const (
	cSignAgent      = "signagent"
	cSignJob        = "signjobs"
	signAgentPrefix = "llsign."
	// signJobLife: a job older than this is never handed out (its document
	// has stopped waiting; it is held and sent again later).
	signJobLife = 3 * time.Minute
	// signAgentAway: a signer not heard from for this long is not connected.
	signAgentAway = 2 * time.Minute
	// signAgentOfflineWork: after this long away in working hours, the
	// Admin gets a work item.
	signAgentOfflineWork = 10 * time.Minute
)

// errSignerOffline says documents wait because the USB token signer is not
// connected; dispatch holds them and sends them when it is back.
var errSignerOffline = errors.New("the USB token signer is not connected (keep the office computer on, the token plugged in and Ledgerline Signer running)")

// SignAgent is the firm's USB token signer: its key (as a hash), the
// approved certificate and what the signer last reported.
type SignAgent struct {
	KeyHash string `json:"key_hash"`
	KeyMade string `json:"key_made"`
	KeyBy   string `json:"key_by"`
	// The approved certificate (DER, base64), and who approved it.
	Cert       string    `json:"cert,omitempty"`
	CertInfo   sign.Info `json:"cert_info"`
	ApprovedBy string    `json:"approved_by,omitempty"`
	ApprovedAt string    `json:"approved_at,omitempty"`
	// A certificate the signer sent that waits for approval.
	Pending     string    `json:"pending,omitempty"`
	PendingInfo sign.Info `json:"pending_info"`
	PendingAt   string    `json:"pending_at,omitempty"`
	PendingFrom string    `json:"pending_from,omitempty"`
	// What the signer last reported.
	AgentCert string `json:"agent_cert,omitempty"` // SHA-256 of its certificate
	Seen      string `json:"seen,omitempty"`
	FirstSeen string `json:"first_seen,omitempty"`
	Version   string `json:"version,omitempty"`
	Host      string `json:"host,omitempty"`
	// Stalled is when a document last gave up waiting; a signer not seen
	// since then is treated as away, so the next documents do not wait.
	Stalled string `json:"stalled,omitempty"`
	// The day's count (logged once a day, not per document).
	Day         string `json:"day,omitempty"`
	Signed      int    `json:"signed"`
	Failed      int    `json:"failed"`
	LastError   string `json:"last_error,omitempty"`
	LastErrorAt string `json:"last_error_at,omitempty"`
	// OfflineWork is the key of the open "signer not connected" work item.
	OfflineWork string `json:"offline_work,omitempty"`
}

// SignJob is one digest waiting for the token's signature.
type SignJob struct {
	Tenant    string `json:"tenant"`
	ID        string `json:"id"`
	Digest    string `json:"digest_b64"`
	Hash      string `json:"hash"` // SHA256
	Created   string `json:"created"`
	Status    string `json:"status"` // Waiting, Taken, Signed, Failed, Expired
	Signature string `json:"signature,omitempty"`
	Error     string `json:"error,omitempty"`
	Agent     string `json:"agent,omitempty"` // the computer that took it
	Taken     string `json:"taken,omitempty"`
	Done      string `json:"done,omitempty"`
}

// SignAgentView is the signer's state on the signing screen.
type SignAgentView struct {
	Connected  bool       `json:"connected"`
	Seen       string     `json:"seen,omitempty"`
	FirstSeen  string     `json:"first_seen,omitempty"`
	Version    string     `json:"version,omitempty"`
	Host       string     `json:"host,omitempty"`
	KeyMade    string     `json:"key_made,omitempty"`
	KeyBy      string     `json:"key_by,omitempty"`
	Cert       *sign.Info `json:"cert,omitempty"`
	ApprovedBy string     `json:"approved_by,omitempty"`
	ApprovedAt string     `json:"approved_at,omitempty"`
	// CertMatches: the signer has the approved certificate.
	CertMatches bool   `json:"cert_matches"`
	Warning     string `json:"warning,omitempty"`
	// A certificate waiting for approval, and why it cannot be approved.
	Pending        *sign.Info `json:"pending,omitempty"`
	PendingAt      string     `json:"pending_at,omitempty"`
	PendingFrom    string     `json:"pending_from,omitempty"`
	PendingProblem string     `json:"pending_problem,omitempty"`
	PendingWarning string     `json:"pending_warning,omitempty"`
	SignedToday    int        `json:"signed_today"`
	FailedToday    int        `json:"failed_today"`
	LastError      string     `json:"last_error,omitempty"`
	LastErrorAt    string     `json:"last_error_at,omitempty"`
}

// SignAgentKey is a new agent key, shown once.
type SignAgentKey struct {
	Key  string      `json:"key"`
	View SigningView `json:"signing"`
}

// SignAgentMeta is what the signer says about itself on every call.
type SignAgentMeta struct {
	Version string
	Host    string
}

// SignAgentStatus answers the signer's hello and polls.
type SignAgentStatus struct {
	Status      string     `json:"status"` // approved, waiting_for_approval, certificate_differs, certificate_invalid
	Message     string     `json:"message"`
	Certificate *sign.Info `json:"certificate,omitempty"`
}

// SignJobOut is a job as the signer gets it.
type SignJobOut struct {
	ID     string `json:"id"`
	Digest string `json:"digest_b64"`
	Hash   string `json:"hash"`
}

// SignAgentPollResult is the answer to a poll.
type SignAgentPollResult struct {
	SignAgentStatus
	Jobs []SignJobOut `json:"jobs"`
}

// SigningCheck is the result of signing a test document.
type SigningCheck struct {
	OK     bool        `json:"ok"`
	Signer sign.Info   `json:"signer"`
	Millis int64       `json:"millis"`
	View   SigningView `json:"signing"`
}

// ---- waking waiters in this process ----

// waker hands out channels that are closed by wake: a cheap way for one
// goroutine to tell others in the same process that something changed in
// the store. Waiters still look at the store on a timer, for changes made
// by other processes.
type waker struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

func (w *waker) ch(k string) <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.m == nil {
		w.m = map[string]chan struct{}{}
	}
	c, ok := w.m[k]
	if !ok {
		c = make(chan struct{})
		w.m[k] = c
	}
	return c
}

func (w *waker) wake(k string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if c, ok := w.m[k]; ok {
		close(c)
		delete(w.m, k)
	}
}

func (o *Ops) signWait() time.Duration {
	if o.SignAgentWait > 0 {
		return o.SignAgentWait
	}
	return 60 * time.Second
}

func (o *Ops) signPollWait() time.Duration {
	if o.SignAgentPollWait > 0 {
		return o.SignAgentPollWait
	}
	return 25 * time.Second
}

// ---- keys and certificates ----

// newSignAgentKey makes an agent key: llsign.<firm>.<64 hex digits>.
func newSignAgentKey(tenant string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return signAgentPrefix + tenant + "." + hex.EncodeToString(b)
}

// SignAgentTenant reads the firm from an agent key (without checking it).
func SignAgentTenant(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, signAgentPrefix)
	if !ok || len(key) > 200 {
		return "", false
	}
	i := strings.LastIndex(rest, ".")
	if i <= 0 || len(rest)-i-1 != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(rest[i+1:]); err != nil {
		return "", false
	}
	return rest[:i], true
}

// parseCertFile reads a public certificate, DER or PEM (.cer, .crt).
func parseCertFile(b []byte) (*x509.Certificate, error) {
	if len(b) == 0 || len(b) > 64<<10 {
		return nil, errors.New("upload the token's certificate (.cer or .crt, under 64 KB)")
	}
	if strings.Contains(string(b), "-----BEGIN") {
		for {
			var p *pem.Block
			p, b = pem.Decode(b)
			if p == nil {
				return nil, errors.New("no certificate found in the file")
			}
			if p.Type == "CERTIFICATE" {
				b = p.Bytes
				break
			}
		}
	}
	c, err := x509.ParseCertificate(b)
	if err != nil {
		return nil, fmt.Errorf("the file is not a certificate: %w", err)
	}
	return c, nil
}

// certKey stands for a certificate's key without the private part, so
// sign.Signer.Check can check the certificate alone.
type certKey struct{ pub crypto.PublicKey }

func (k certKey) Public() crypto.PublicKey { return k.pub }
func (k certKey) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("no private key here")
}

// checkAgentCert says whether a certificate can be approved for signing at
// now, and warns when it expires within 30 days.
func checkAgentCert(c *x509.Certificate, now time.Time) (warning string, err error) {
	switch c.PublicKey.(type) {
	case *rsa.PublicKey, *ecdsa.PublicKey:
	default:
		return "", errors.New("the certificate's key is neither RSA nor ECDSA")
	}
	if err := (&sign.Signer{Key: certKey{c.PublicKey}, Cert: c}).Check(now); err != nil {
		return "", err
	}
	if d := c.NotAfter.Sub(now); d < 30*24*time.Hour {
		return fmt.Sprintf("the certificate expires on %s, in %d days: renew the token's certificate in time", c.NotAfter.In(ist).Format("02-Jan-2006"), int(d.Hours()/24)), nil
	}
	return "", nil
}

func (a SignAgent) cert() (*x509.Certificate, error) {
	der, err := base64.StdEncoding.DecodeString(a.Cert)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

// verifyAgentSig checks a signature from the token over digest (SHA-256)
// against the approved certificate. ECDSA comes back as ASN.1 DER; a raw
// r‖s (what Windows CNG gives) is accepted and turned into DER.
func verifyAgentSig(c *x509.Certificate, digest, sig []byte) ([]byte, error) {
	switch pub := c.PublicKey.(type) {
	case *rsa.PublicKey:
		if rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, sig) == nil {
			return sig, nil
		}
	case *ecdsa.PublicKey:
		if ecdsa.VerifyASN1(pub, digest, sig) {
			return sig, nil
		}
		n := (pub.Curve.Params().BitSize + 7) / 8
		if len(sig) == 2*n {
			r, s := new(big.Int).SetBytes(sig[:n]), new(big.Int).SetBytes(sig[n:])
			if ecdsa.Verify(pub, digest, r, s) {
				return asn1.Marshal(struct{ R, S *big.Int }{r, s})
			}
		}
	}
	return nil, errors.New("the signature does not match the approved certificate")
}

// agentKey is the token's key as a crypto.Signer: Sign puts a job in the
// store and waits for the signer's answer.
type agentKey struct {
	o      *Ops
	ctx    context.Context
	tenant string
	cert   *x509.Certificate
}

func (k *agentKey) Public() crypto.PublicKey { return k.cert.PublicKey }

func (k *agentKey) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if opts == nil || opts.HashFunc() != crypto.SHA256 || len(digest) != sha256.Size {
		return nil, errors.New("the USB token signer signs SHA-256 digests only")
	}
	return k.o.agentSign(k.ctx, k.tenant, k.cert, digest)
}

func (o *Ops) signAgent(ctx context.Context, tenant string) SignAgent {
	a, _, _ := docs.GetOr(ctx, o.Docs, tenant, cSignAgent, "main", SignAgent{})
	return a
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// signAgentOnline: the signer was heard from lately, and not only before
// a document last gave up waiting for it.
func (o *Ops) signAgentOnline(a SignAgent) bool { return signAgentOnlineAt(a, o.now()) }

func signAgentOnlineAt(a SignAgent, now time.Time) bool {
	seen := parseTime(a.Seen)
	if seen.IsZero() || now.Sub(seen) >= signAgentAway {
		return false
	}
	st := parseTime(a.Stalled)
	return st.IsZero() || st.Before(seen)
}

// agentDocSigner is docSigner for the USB token signer. It fails at once
// when the signer is away, so a batch of documents does not wait a minute
// each; those documents are held and sent when it is back.
func (o *Ops) agentDocSigner(ctx context.Context, tenant string) (func([]byte, time.Time) ([]byte, error), sign.Info, error) {
	a := o.signAgent(ctx, tenant)
	if a.Cert == "" {
		return nil, a.CertInfo, errors.New("the USB token signer's certificate is not approved yet (Settings → Digital signature)")
	}
	cert, err := a.cert()
	if err != nil {
		return nil, a.CertInfo, err
	}
	s := &sign.Signer{Key: &agentKey{o: o, ctx: ctx, tenant: tenant, cert: cert}, Cert: cert}
	if err := s.Check(o.now()); err != nil {
		return nil, a.CertInfo, err
	}
	if !o.signAgentOnline(a) {
		return nil, a.CertInfo, errSignerOffline
	}
	if a.AgentCert != a.CertInfo.SHA256 {
		return nil, a.CertInfo, errors.New("the USB token signer has a different certificate from the approved one; approve it in Settings → Digital signature")
	}
	return s.SignDetached, a.CertInfo, nil
}

// ---- jobs ----

// Job IDs start with the hour (IST), so a look for work lists only the
// last two hours' jobs: 20261119-1503-<random>.
const signJobHour = "20060102-15"

func (o *Ops) newSignJob(ctx context.Context, tenant string, digest []byte) (SignJob, error) {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	now := o.now()
	j := SignJob{Tenant: tenant, ID: now.Format("20060102-1504") + "-" + hex.EncodeToString(b), Digest: base64.StdEncoding.EncodeToString(digest), Hash: "SHA256",
		Created: now.Format(time.RFC3339Nano), Status: "Waiting"}
	if _, err := docs.Put(ctx, o.Docs, tenant, cSignJob, j.ID, j, 0, SystemActor); err != nil {
		return j, err
	}
	o.signWake.wake(tenant + "|jobs")
	return j, nil
}

// agentSign waits for the signer to sign digest, then checks the answer.
func (o *Ops) agentSign(ctx context.Context, tenant string, cert *x509.Certificate, digest []byte) ([]byte, error) {
	j, err := o.newSignJob(ctx, tenant, digest)
	if err != nil {
		return nil, err
	}
	deadline := time.NewTimer(o.signWait())
	defer deadline.Stop()
	look := time.NewTicker(250 * time.Millisecond)
	defer look.Stop()
	for {
		woke := o.signWake.ch(tenant + "|results")
		cur, _, err := docs.Get[SignJob](ctx, o.Docs, tenant, cSignJob, j.ID)
		if err == nil {
			switch cur.Status {
			case "Signed":
				sig, err := base64.StdEncoding.DecodeString(cur.Signature)
				if err != nil {
					return nil, err
				}
				return verifyAgentSig(cert, digest, sig)
			case "Failed":
				return nil, fmt.Errorf("the USB token did not sign: %s", cur.Error)
			case "Expired":
				return nil, errSignerOffline
			}
		}
		select {
		case <-ctx.Done():
			o.expireSignJob(context.WithoutCancel(ctx), tenant, j.ID)
			return nil, ctx.Err()
		case <-deadline.C:
			// Never handed out or answered late: the document is held.
			o.expireSignJob(ctx, tenant, j.ID)
			_, _ = docs.Update(ctx, o.Docs, tenant, cSignAgent, "main", SystemActor, func(a *SignAgent) error {
				a.Stalled = o.now().Format(time.RFC3339Nano)
				return nil
			})
			return nil, errSignerOffline
		case <-woke:
		case <-look.C:
		}
	}
}

func (o *Ops) expireSignJob(ctx context.Context, tenant, id string) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cSignJob, id, SystemActor, func(x *SignJob) error {
		if x.Status != "Waiting" && x.Status != "Taken" {
			return errClaimed
		}
		x.Status, x.Done = "Expired", o.now().Format(time.RFC3339Nano)
		return nil
	})
}

// claimSignJobs hands out up to max waiting jobs to the signer on host.
func (o *Ops) claimSignJobs(ctx context.Context, tenant, host string, max int) []SignJobOut {
	now := o.now()
	out := []SignJobOut{}
	for _, p := range []string{now.Add(-time.Hour).Format(signJobHour), now.Format(signJobHour)} {
		js, err := docs.List[SignJob](ctx, o.Docs, tenant, cSignJob, p)
		if err != nil {
			continue
		}
		for _, j := range js {
			if j.Status != "Waiting" {
				continue
			}
			if now.Sub(parseTime(j.Created)) > signJobLife {
				o.expireSignJob(ctx, tenant, j.ID)
				continue
			}
			got, err := docs.Update(ctx, o.Docs, tenant, cSignJob, j.ID, SystemActor, func(x *SignJob) error {
				if x.Status != "Waiting" {
					return errClaimed
				}
				x.Status, x.Agent, x.Taken = "Taken", host, now.Format(time.RFC3339Nano)
				return nil
			})
			if err != nil {
				continue
			}
			out = append(out, SignJobOut{ID: got.ID, Digest: got.Digest, Hash: got.Hash})
			if len(out) >= max {
				return out
			}
		}
	}
	return out
}

// ---- the signer's calls ----

// signAgentAuth checks the agent key and records that the signer was seen.
func (o *Ops) signAgentAuth(ctx context.Context, tenant, key string, m SignAgentMeta) (SignAgent, error) {
	if t, ok := SignAgentTenant(key); !ok || t != tenant {
		return SignAgent{}, ErrForbidden
	}
	if o.signingConfig(ctx, tenant).Mode != "agent" {
		return SignAgent{}, fmt.Errorf("%w: the firm does not sign with a USB token signer now", ErrForbidden)
	}
	a, _, err := docs.Get[SignAgent](ctx, o.Docs, tenant, cSignAgent, "main")
	if err != nil || a.KeyHash == "" || subtle.ConstantTimeCompare([]byte(a.KeyHash), []byte(sha(key))) != 1 {
		return SignAgent{}, ErrForbidden
	}
	m.Version, m.Host = clip(strings.TrimSpace(m.Version), 40), clip(strings.TrimSpace(m.Host), 80)
	now := o.now()
	seen, stalled := parseTime(a.Seen), parseTime(a.Stalled)
	if !seen.IsZero() && now.Sub(seen) < 10*time.Second && m.Version == a.Version && m.Host == a.Host && a.OfflineWork == "" && (stalled.IsZero() || stalled.Before(seen)) {
		return a, nil // seen moments ago: no write
	}
	var closeKey string
	back := seen.IsZero() || now.Sub(seen) >= signAgentOfflineWork
	a2, err := docs.Update(ctx, o.Docs, tenant, cSignAgent, "main", SystemActor, func(x *SignAgent) error {
		if x.KeyHash != a.KeyHash {
			return ErrForbidden // the key was reset meanwhile
		}
		x.Seen, x.Version, x.Host = now.Format(time.RFC3339Nano), m.Version, m.Host
		if x.FirstSeen == "" {
			x.FirstSeen = x.Seen
		}
		closeKey, x.OfflineWork = x.OfflineWork, ""
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			return SignAgent{}, err
		}
		return a, nil
	}
	if closeKey != "" {
		o.closeWork(ctx, tenant, SystemActor, "signing", closeKey, "the USB token signer is connected again")
	}
	if back {
		o.log(ctx, tenant, SystemActor, "signing.agent.connected", m.Host, map[string]any{"version": m.Version})
	}
	return a2, nil
}

// signAgentState says what the signer may do with its certificate.
func (o *Ops) signAgentState(a SignAgent) SignAgentStatus {
	switch {
	case a.AgentCert == "":
		return SignAgentStatus{Status: "certificate_invalid", Message: "Send the certificate first (hello)."}
	case a.Cert != "" && a.AgentCert == a.CertInfo.SHA256:
		info := a.CertInfo
		if c, err := a.cert(); err == nil {
			if _, err := checkAgentCert(c, o.now()); err != nil {
				return SignAgentStatus{Status: "certificate_invalid", Message: "The approved certificate cannot sign: " + err.Error() + ".", Certificate: &info}
			}
		}
		return SignAgentStatus{Status: "approved", Message: "Certificate approved: signing documents.", Certificate: &info}
	case a.PendingInfo.SHA256 == a.AgentCert && a.Pending != "":
		info := a.PendingInfo
		if c, err := x509.ParseCertificate(mustB64(a.Pending)); err == nil {
			if _, err := checkAgentCert(c, o.now()); err != nil {
				return SignAgentStatus{Status: "certificate_invalid", Message: "This certificate cannot sign: " + err.Error() + ". Choose another certificate.", Certificate: &info}
			}
		}
		if a.Cert != "" {
			return SignAgentStatus{Status: "certificate_differs", Message: "This certificate differs from the approved one (" + a.CertInfo.Name + ", serial " + a.CertInfo.Serial + "). An Admin or the Compliance officer must approve it in Ledgerline before it signs.", Certificate: &info}
		}
		return SignAgentStatus{Status: "waiting_for_approval", Message: "Waiting for an Admin or the Compliance officer to approve this certificate in Ledgerline (Settings → Digital signature).", Certificate: &info}
	}
	return SignAgentStatus{Status: "certificate_differs", Message: "This certificate differs from the approved one; send hello again."}
}

func mustB64(s string) []byte {
	b, _ := base64.StdEncoding.DecodeString(s)
	return b
}

// SignAgentHello takes the signer's certificate and says whether it may sign.
func (o *Ops) SignAgentHello(ctx context.Context, tenant, key string, m SignAgentMeta, certDER []byte) (SignAgentStatus, error) {
	if _, err := o.signAgentAuth(ctx, tenant, key, m); err != nil {
		return SignAgentStatus{}, err
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return SignAgentStatus{}, invalid("the signer sent no readable certificate")
	}
	now := o.now()
	info := sign.CertInfo(cert)
	_, cerr := checkAgentCert(cert, now)
	newPending := false
	a, err := docs.Update(ctx, o.Docs, tenant, cSignAgent, "main", SystemActor, func(x *SignAgent) error {
		newPending = false
		x.AgentCert = info.SHA256
		if x.Cert != "" && x.CertInfo.SHA256 == info.SHA256 {
			return nil
		}
		if x.Pending == "" || x.PendingInfo.SHA256 != info.SHA256 {
			x.Pending, x.PendingInfo, x.PendingAt = base64.StdEncoding.EncodeToString(cert.Raw), info, now.Format(time.RFC3339)
			x.PendingFrom = "Ledgerline Signer on " + firstOf(clip(m.Host, 80), "the office computer")
			newPending = cerr == nil
		}
		return nil
	})
	if err != nil {
		return SignAgentStatus{}, err
	}
	if newPending {
		o.raiseWork(ctx, tenant, "signing", "agent-cert-"+info.SHA256[:16], "Approve the USB token signer's certificate",
			fmt.Sprintf("%s sent the certificate of %s (serial %s, issued by %s, valid until %s). Check it is the firm's signing certificate and approve it in Settings → Digital signature. Until then nothing is signed with it.",
				a.PendingFrom, info.Name, info.Serial, info.Issuer, info.Until[:10]), "", RoleCompliance, "dispatch", now.Format("2006-01-02"))
		o.log(ctx, tenant, SystemActor, "signing.agent.certificate_sent", info.SHA256, map[string]any{"certificate": info.Name, "serial": info.Serial, "issuer": info.Issuer, "host": a.Host})
	}
	return o.signAgentState(a), nil
}

// SignAgentPoll hands the signer waiting jobs, waiting a while for some.
func (o *Ops) SignAgentPoll(ctx context.Context, tenant, key string, m SignAgentMeta, max int) (SignAgentPollResult, error) {
	a, err := o.signAgentAuth(ctx, tenant, key, m)
	if err != nil {
		return SignAgentPollResult{}, err
	}
	res := SignAgentPollResult{SignAgentStatus: o.signAgentState(a), Jobs: []SignJobOut{}}
	if res.Status != "approved" {
		return res, nil
	}
	if max <= 0 || max > 50 {
		max = 10
	}
	deadline := time.Now().Add(o.signPollWait())
	for {
		woke := o.signWake.ch(tenant + "|jobs")
		if res.Jobs = o.claimSignJobs(ctx, tenant, a.Host, max); len(res.Jobs) > 0 {
			return res, nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			return res, nil
		}
		t := time.NewTimer(min(left, 500*time.Millisecond))
		select {
		case <-ctx.Done():
			t.Stop()
			return res, nil
		case <-woke:
		case <-t.C:
		}
		t.Stop()
	}
}

// SignAgentResultIn is the signer's answer for one job.
type SignAgentResultIn struct {
	ID        string
	Signature string // base64
	Error     string
}

// SignAgentResult takes the signer's signature (or error) for a job.
func (o *Ops) SignAgentResult(ctx context.Context, tenant, key string, m SignAgentMeta, r SignAgentResultIn) error {
	a, err := o.signAgentAuth(ctx, tenant, key, m)
	if err != nil {
		return err
	}
	j, _, err := docs.Get[SignJob](ctx, o.Docs, tenant, cSignJob, r.ID)
	if err != nil || r.ID == "" {
		return fmt.Errorf("%w: no such signing job", ErrNotFound)
	}
	if j.Status != "Taken" {
		return fmt.Errorf("%w: the document stopped waiting for this signature", ErrState)
	}
	failed := clip(strings.TrimSpace(r.Error), 300)
	var sig []byte
	if failed == "" {
		raw, err := base64.StdEncoding.DecodeString(r.Signature)
		cert, cerr := a.cert()
		switch {
		case err != nil || len(raw) == 0:
			failed = "the signer sent no signature"
		case cerr != nil || a.AgentCert != a.CertInfo.SHA256:
			failed = "the signer's certificate is not the approved one"
		default:
			if sig, err = verifyAgentSig(cert, mustB64(j.Digest), raw); err != nil {
				failed = err.Error()
			}
		}
	}
	now := o.now().Format(time.RFC3339Nano)
	if _, err := docs.Update(ctx, o.Docs, tenant, cSignJob, j.ID, SystemActor, func(x *SignJob) error {
		if x.Status != "Taken" {
			return fmt.Errorf("%w: the document stopped waiting for this signature", ErrState)
		}
		x.Done = now
		if failed != "" {
			x.Status, x.Error = "Failed", failed
		} else {
			x.Status, x.Signature = "Signed", base64.StdEncoding.EncodeToString(sig)
		}
		return nil
	}); err != nil {
		return err
	}
	o.countSigned(ctx, tenant, failed)
	o.signWake.wake(tenant + "|results")
	if failed != "" && r.Error == "" {
		return invalid("%s", failed)
	}
	return nil
}

// countSigned adds a document to the day's count; the count of a day is
// logged once, when the next day begins.
func (o *Ops) countSigned(ctx context.Context, tenant, failed string) {
	now := o.now()
	var prev SignAgent
	_, _ = docs.Update(ctx, o.Docs, tenant, cSignAgent, "main", SystemActor, func(a *SignAgent) error {
		prev = rollSignDay(a, now.Format("2006-01-02"))
		if failed == "" {
			a.Signed++
		} else {
			a.Failed++
			a.LastError, a.LastErrorAt = failed, now.Format(time.RFC3339)
		}
		return nil
	})
	o.logSignDay(ctx, tenant, prev)
}

// rollSignDay starts a new day's count and returns the finished day's.
func rollSignDay(a *SignAgent, today string) SignAgent {
	var prev SignAgent
	if a.Day != today {
		if a.Day != "" && a.Signed+a.Failed > 0 {
			prev = SignAgent{Day: a.Day, Signed: a.Signed, Failed: a.Failed}
		}
		a.Day, a.Signed, a.Failed = today, 0, 0
	}
	return prev
}

func (o *Ops) logSignDay(ctx context.Context, tenant string, prev SignAgent) {
	if prev.Day != "" {
		o.log(ctx, tenant, SystemActor, "signing.agent.day", prev.Day, map[string]any{"signed": prev.Signed, "failed": prev.Failed})
	}
}

// ---- the Admin's set-up ----

// ConnectSignAgent switches signing to a USB token signer and makes its
// agent key (shown once). certFile, when given, is the token's public
// certificate, approved by the Admin who uploads it.
func (o *Ops) ConnectSignAgent(ctx context.Context, tenant, actor string, certFile []byte, location string) (SignAgentKey, error) {
	if err := o.require(ctx, tenant, actor, "Connecting the USB token signer", RoleAdmin); err != nil {
		return SignAgentKey{}, err
	}
	var cert *x509.Certificate
	if len(certFile) > 0 {
		c, err := parseCertFile(certFile)
		if err != nil {
			return SignAgentKey{}, invalid("%s", err.Error())
		}
		if _, err := checkAgentCert(c, o.now()); err != nil {
			return SignAgentKey{}, invalid("%s", err.Error())
		}
		cert = c
	}
	key := newSignAgentKey(tenant)
	now := o.now().Format(time.RFC3339)
	a := o.signAgent(ctx, tenant)
	a.KeyHash, a.KeyMade, a.KeyBy = sha(key), now, actor
	a.Seen, a.FirstSeen, a.Version, a.Host, a.AgentCert, a.Stalled = "", "", "", "", "", ""
	if cert != nil {
		approveCert(&a, cert, actor, now)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSignAgent, "main", a, actor); err != nil {
		return SignAgentKey{}, err
	}
	v, err := o.saveSigning(ctx, tenant, actor, SigningConfig{Mode: "agent", Location: strings.TrimSpace(location), Info: a.CertInfo})
	if err != nil {
		return SignAgentKey{}, err
	}
	o.log(ctx, tenant, actor, "signing.agent.connect", "agent", map[string]any{"certificate_uploaded": cert != nil, "certificate": a.CertInfo.Name, "serial": a.CertInfo.Serial})
	return SignAgentKey{Key: key, View: v}, nil
}

func approveCert(a *SignAgent, c *x509.Certificate, actor, now string) {
	info := sign.CertInfo(c)
	a.Cert, a.CertInfo, a.ApprovedBy, a.ApprovedAt = base64.StdEncoding.EncodeToString(c.Raw), info, actor, now
	if a.PendingInfo.SHA256 == info.SHA256 {
		a.Pending, a.PendingInfo, a.PendingAt, a.PendingFrom = "", sign.Info{}, "", ""
	}
}

func (o *Ops) requireAgentMode(ctx context.Context, tenant string) error {
	if o.signingConfig(ctx, tenant).Mode != "agent" {
		return fmt.Errorf("%w: documents are not signed with a USB token signer; connect one first", ErrState)
	}
	return nil
}

// SetSignAgentCert sets the token's certificate from its public file
// (.cer or .crt); the Admin who uploads it approves it.
func (o *Ops) SetSignAgentCert(ctx context.Context, tenant, actor string, certFile []byte) (SigningView, error) {
	if err := o.require(ctx, tenant, actor, "Setting the USB token signer's certificate", RoleAdmin); err != nil {
		return SigningView{}, err
	}
	if err := o.requireAgentMode(ctx, tenant); err != nil {
		return SigningView{}, err
	}
	c, err := parseCertFile(certFile)
	if err != nil {
		return SigningView{}, invalid("%s", err.Error())
	}
	return o.approveAgentCert(ctx, tenant, actor, c, true)
}

// ApproveSignAgentCert approves the certificate the signer sent, given by
// its SHA-256 fingerprint as shown (so a certificate that changed while
// the screen was open is not approved by mistake).
func (o *Ops) ApproveSignAgentCert(ctx context.Context, tenant, actor, fingerprint string) (SigningView, error) {
	if err := o.require(ctx, tenant, actor, "Approving the USB token signer's certificate", RoleCompliance); err != nil {
		return SigningView{}, err
	}
	if err := o.requireAgentMode(ctx, tenant); err != nil {
		return SigningView{}, err
	}
	a := o.signAgent(ctx, tenant)
	fp := strings.ToUpper(strings.NewReplacer(":", "", " ", "").Replace(fingerprint))
	if a.Pending == "" || a.PendingInfo.SHA256 != fp {
		return SigningView{}, invalid("that certificate is no longer waiting for approval; open the screen again")
	}
	c, err := x509.ParseCertificate(mustB64(a.Pending))
	if err != nil {
		return SigningView{}, err
	}
	return o.approveAgentCert(ctx, tenant, actor, c, false)
}

func (o *Ops) approveAgentCert(ctx context.Context, tenant, actor string, c *x509.Certificate, uploaded bool) (SigningView, error) {
	if _, err := checkAgentCert(c, o.now()); err != nil {
		return SigningView{}, invalid("%s", err.Error())
	}
	var before sign.Info
	a, err := docs.Update(ctx, o.Docs, tenant, cSignAgent, "main", actor, func(x *SignAgent) error {
		before = x.CertInfo
		approveCert(x, c, actor, o.now().Format(time.RFC3339))
		return nil
	})
	if err != nil {
		return SigningView{}, err
	}
	if _, err := docs.Update(ctx, o.Docs, tenant, cSigning, "main", actor, func(x *SigningConfig) error {
		x.Info, x.UpdatedBy, x.UpdatedAt = a.CertInfo, actor, o.now().Format(time.RFC3339)
		return nil
	}); err != nil {
		return SigningView{}, err
	}
	o.closeWork(ctx, tenant, actor, "signing", "agent-cert-"+a.CertInfo.SHA256[:16], "approved by "+actor)
	o.log(ctx, tenant, actor, "signing.agent.approved", a.CertInfo.SHA256, map[string]any{"certificate": a.CertInfo.Name, "serial": a.CertInfo.Serial, "issuer": a.CertInfo.Issuer,
		"valid_until": a.CertInfo.Until, "uploaded": uploaded, "replaces": before.SHA256})
	return o.Signing(ctx, tenant, actor)
}

// ResetSignAgentKey makes a new agent key; the old one stops working at once.
func (o *Ops) ResetSignAgentKey(ctx context.Context, tenant, actor string) (SignAgentKey, error) {
	if err := o.require(ctx, tenant, actor, "Resetting the USB token signer's key", RoleAdmin); err != nil {
		return SignAgentKey{}, err
	}
	if err := o.requireAgentMode(ctx, tenant); err != nil {
		return SignAgentKey{}, err
	}
	key := newSignAgentKey(tenant)
	if _, err := docs.Update(ctx, o.Docs, tenant, cSignAgent, "main", actor, func(x *SignAgent) error {
		x.KeyHash, x.KeyMade, x.KeyBy = sha(key), o.now().Format(time.RFC3339), actor
		x.Seen, x.AgentCert = "", ""
		return nil
	}); err != nil {
		return SignAgentKey{}, err
	}
	o.log(ctx, tenant, actor, "signing.agent.key_reset", "agent", nil)
	v, err := o.Signing(ctx, tenant, actor)
	return SignAgentKey{Key: key, View: v}, err
}

// CheckSigning signs a test document with the firm's set-up (a file, the
// signing service or the USB token signer) and checks the signature.
func (o *Ops) CheckSigning(ctx context.Context, tenant, actor string) (SigningCheck, error) {
	if err := o.require(ctx, tenant, actor, "Checking the digital signature", RoleCompliance); err != nil {
		return SigningCheck{}, err
	}
	fn, _, err := o.docSigner(ctx, tenant)
	if err != nil {
		return SigningCheck{}, invalid("documents cannot be signed now: %s", err.Error())
	}
	start := time.Now()
	at := o.now()
	probe := []byte("Ledgerline signing check " + at.Format(time.RFC3339Nano))
	cms, err := fn(probe, at)
	if err != nil {
		return SigningCheck{}, invalid("the test document was not signed: %s", err.Error())
	}
	v, err := sign.Verify(cms, probe)
	if err != nil {
		return SigningCheck{}, invalid("the test signature does not check: %s", err.Error())
	}
	res := SigningCheck{OK: true, Signer: v.Signer, Millis: time.Since(start).Milliseconds()}
	o.log(ctx, tenant, actor, "signing.check", o.signingConfig(ctx, tenant).Mode, map[string]any{"certificate": v.Signer.Name, "serial": v.Signer.Serial})
	res.View, err = o.Signing(ctx, tenant, actor)
	return res, err
}

func (o *Ops) signAgentView(ctx context.Context, tenant string) *SignAgentView {
	a := o.signAgent(ctx, tenant)
	now := o.now()
	v := &SignAgentView{Connected: o.signAgentOnline(a), Seen: a.Seen, FirstSeen: a.FirstSeen, Version: a.Version, Host: a.Host, KeyMade: a.KeyMade, KeyBy: a.KeyBy,
		ApprovedBy: a.ApprovedBy, ApprovedAt: a.ApprovedAt, CertMatches: a.Cert != "" && a.AgentCert == a.CertInfo.SHA256, LastError: a.LastError, LastErrorAt: a.LastErrorAt}
	if a.Day == now.Format("2006-01-02") {
		v.SignedToday, v.FailedToday = a.Signed, a.Failed
	}
	if a.Cert != "" {
		info := a.CertInfo
		v.Cert = &info
		if c, err := a.cert(); err == nil {
			if w, err := checkAgentCert(c, now); err != nil {
				v.Warning = err.Error()
			} else {
				v.Warning = w
			}
		}
	}
	if a.Pending != "" {
		info := a.PendingInfo
		v.Pending, v.PendingAt, v.PendingFrom = &info, a.PendingAt, a.PendingFrom
		if c, err := x509.ParseCertificate(mustB64(a.Pending)); err == nil {
			v.PendingWarning, err = checkAgentCert(c, now)
			if err != nil {
				v.PendingProblem = err.Error()
			}
		}
	}
	return v
}

// ---- the scheduler's minute ----

// signAgentTick, in agent mode: logs the finished day's count, raises a
// work item for the Admin when the signer has been away for 10 minutes in
// working hours (closed when it is back), and sends documents held for a
// signature once the signer is back.
func (o *Ops) signAgentTick(ctx context.Context, tenant string, now time.Time) {
	if o.signingConfig(ctx, tenant).Mode != "agent" {
		return
	}
	now = now.In(ist)
	today, hm := now.Format("2006-01-02"), now.Format("15:04")
	a := o.signAgent(ctx, tenant)
	if a.KeyHash == "" {
		return
	}
	if a.Day != "" && a.Day != today {
		var prev SignAgent
		if x, err := docs.Update(ctx, o.Docs, tenant, cSignAgent, "main", SystemActor, func(x *SignAgent) error { prev = rollSignDay(x, today); return nil }); err == nil {
			a = x
			o.logSignDay(ctx, tenant, prev)
		}
	}
	since := parseTime(firstOf(a.Seen, a.KeyMade))
	away := !since.IsZero() && now.Sub(since) >= signAgentOfflineWork
	set, _ := o.Settings(ctx, tenant)
	working := set.IsBusinessDay(today) && hm >= "09:00" && hm < "20:00"
	switch {
	case away && working && a.OfflineWork == "":
		key := "agent-offline-" + today + "-" + strings.ReplaceAll(hm, ":", "")
		last := "has not connected since its key was made"
		if a.Seen != "" {
			last = "was last seen at " + parseTime(a.Seen).In(ist).Format("15:04 on 02-Jan")
		}
		if _, err := docs.Update(ctx, o.Docs, tenant, cSignAgent, "main", SystemActor, func(x *SignAgent) error {
			if x.OfflineWork != "" || x.Seen != a.Seen {
				return errClaimed // another process raised it, or the signer is back
			}
			x.OfflineWork = key
			return nil
		}); err == nil {
			o.raiseWork(ctx, tenant, "signing", key, "The USB token signer is not connected",
				"Ledgerline Signer "+last+". Contract notes and statements are held until it is back: switch on the office computer with the token, plug the token in and start Ledgerline Signer (enter the PIN). Held documents are then sent by themselves.",
				"", RoleAdmin, "dispatch", today)
		}
	case !away && a.OfflineWork != "" && signAgentOnlineAt(a, now):
		key := a.OfflineWork
		if _, err := docs.Update(ctx, o.Docs, tenant, cSignAgent, "main", SystemActor, func(x *SignAgent) error { x.OfflineWork = ""; return nil }); err == nil {
			o.closeWork(ctx, tenant, SystemActor, "signing", key, "the USB token signer is connected again")
		}
	}
	if signAgentOnlineAt(a, now) && a.Cert != "" && a.AgentCert == a.CertInfo.SHA256 {
		o.retryHeldUnsigned(ctx, tenant, now)
	}
}

// retryHeldUnsigned sends again the documents held for want of a
// signature: the latest attempt of each, up to a week old, waiting longer
// after each failed attempt (2, 4, 8 … 64 minutes). It stops at the first
// that is held again, until the next minute.
func (o *Ops) retryHeldUnsigned(ctx context.Context, tenant string, now time.Time) {
	ds, err := docs.List[Dispatch](ctx, o.Docs, tenant, cDispatch, "")
	if err != nil {
		return
	}
	latest := map[string]Dispatch{}
	for _, d := range ds {
		if cur, ok := latest[d.Key]; !ok || d.ID > cur.ID {
			latest[d.Key] = d
		}
	}
	var due []Dispatch
	for _, d := range latest {
		if d.Status != "Held" || !strings.HasPrefix(d.Reason, "not signed") {
			continue
		}
		t, err := time.Parse(time.RFC3339, d.Created)
		if err != nil {
			continue
		}
		age := now.Sub(t)
		if age > 7*24*time.Hour || age < (2*time.Minute)<<min(max(d.Attempt-1, 0), 5) {
			continue
		}
		due = append(due, d)
	}
	sort.Slice(due, func(i, j int) bool { return due[i].ID < due[j].ID })
	sent := 0
	at := now.In(ist).Format("15:04")
	for i, d := range due {
		if i >= 100 {
			break
		}
		var nd Dispatch
		var err error
		if d.Kind == DocContractNote {
			n, nerr := o.noteByNo(ctx, tenant, d.Ref)
			if nerr != nil {
				continue
			}
			_, nd, err = o.sendNoteRecorded(ctx, tenant, SystemActor, n, at)
		} else {
			nd, err = o.sendAgain(ctx, tenant, SystemActor, d)
		}
		if err != nil {
			continue
		}
		if nd.Status == "Held" {
			break // not signed again: wait for the next minute
		}
		sent++
	}
	if sent > 0 {
		o.log(ctx, tenant, SystemActor, "dispatch.held_sent", "signing", map[string]any{"documents": sent})
	}
}
