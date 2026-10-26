package ops

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/pdf"
	"ledgerline/internal/sign"
)

// ---- digital signature on documents sent to clients ----
//
// SEBI requires electronic contract notes to be digitally signed (IT Act),
// and Ledgerline signs every statement it sends the same way. A firm sets
// up one of:
//   - a certificate file (.pfx/.p12 or PEM), usually a Class 3 document
//     signer certificate kept on the server for bulk signing; it is kept
//     encrypted with the firm's key and never shown again;
//   - a signing service: where the certificate stays on a USB token or an
//     HSM, a small service next to it receives the SHA-256 of each document
//     and returns a CMS signature (POST {"sha256","signing_time","document"}
//     → {"cms": base64}). Ledgerline checks every signature it gets back.
// Development servers use a test certificate, marked as not valid.

const cSigning = "signing"

// SigningConfig is the firm's signing set-up.
type SigningConfig struct {
	Mode       string    `json:"mode"` // file, remote, test, off
	File       string    `json:"file,omitempty"`     // base64 of the uploaded file (never returned)
	Password   string    `json:"password,omitempty"` // (never returned)
	RemoteURL  string    `json:"remote_url,omitempty"`
	RemoteKey  string    `json:"remote_key,omitempty"` // (never returned)
	Expect     string    `json:"expect_sha256,omitempty"` // remote: certificate the service must sign with
	Location   string    `json:"location"`
	Info       sign.Info `json:"info"`
	UpdatedBy  string    `json:"updated_by"`
	UpdatedAt  string    `json:"updated_at"`
	Version    int       `json:"version"`
}

// SigningView is what the console shows.
type SigningView struct {
	Mode      string    `json:"mode"`
	Info      sign.Info `json:"info"`
	RemoteURL string    `json:"remote_url,omitempty"`
	Location  string    `json:"location"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt string    `json:"updated_at"`
	Ready     bool      `json:"ready"`
	Problem   string    `json:"problem,omitempty"`
	ExpiresIn int       `json:"expires_in_days"`
	Test      bool      `json:"test"`
}

type signerCache struct {
	mu sync.Mutex
	m  map[string]cachedSigner
}

type cachedSigner struct {
	version int
	s       *sign.Signer
}

var signers = signerCache{m: map[string]cachedSigner{}}

func (o *Ops) signingConfig(ctx context.Context, tenant string) SigningConfig {
	c, _, err := docs.Get[SigningConfig](ctx, o.Docs, tenant, cSigning, "main")
	if err != nil || c.Mode == "" {
		if o.DevCodes || o.devFirm(ctx, tenant) {
			return SigningConfig{Mode: "test", Location: "India"}
		}
		return SigningConfig{Mode: "off"}
	}
	return c
}

// Signing shows the set-up.
func (o *Ops) Signing(ctx context.Context, tenant, actor string) (SigningView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the signing certificate", RoleAdmin, RoleCompliance, RoleChecker); err != nil {
		return SigningView{}, err
	}
	c := o.signingConfig(ctx, tenant)
	v := SigningView{Mode: c.Mode, Info: c.Info, RemoteURL: c.RemoteURL, Location: c.Location, UpdatedBy: c.UpdatedBy, UpdatedAt: c.UpdatedAt, Test: c.Mode == "test"}
	_, info, err := o.docSigner(ctx, tenant)
	if err != nil {
		v.Problem = err.Error()
	} else {
		v.Ready, v.Info = true, info
		if t, err := time.Parse(time.RFC3339, info.Until); err == nil {
			v.ExpiresIn = int(t.Sub(o.now()).Hours() / 24)
		}
	}
	return v, nil
}

// SetSigningFile installs a certificate file (.pfx/.p12 or PEM).
func (o *Ops) SetSigningFile(ctx context.Context, tenant, actor string, file []byte, password, location string) (SigningView, error) {
	if err := o.require(ctx, tenant, actor, "Setting the signing certificate", RoleAdmin); err != nil {
		return SigningView{}, err
	}
	if len(file) == 0 || len(file) > 256<<10 {
		return SigningView{}, invalid("upload the certificate file (.pfx, .p12 or .pem, under 256 KB)")
	}
	s, err := sign.Load(file, password)
	if err != nil {
		return SigningView{}, invalid("%s", err.Error())
	}
	if err := s.Check(o.now()); err != nil {
		return SigningView{}, invalid("%s", err.Error())
	}
	return o.saveSigning(ctx, tenant, actor, SigningConfig{Mode: "file", File: base64.StdEncoding.EncodeToString(file), Password: password, Location: strings.TrimSpace(location), Info: sign.CertInfo(s.Cert)})
}

// SetSigningRemote points signing at a signing service.
func (o *Ops) SetSigningRemote(ctx context.Context, tenant, actor, url, key, location string) (SigningView, error) {
	if err := o.require(ctx, tenant, actor, "Setting the signing service", RoleAdmin); err != nil {
		return SigningView{}, err
	}
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(url, "https://") && !(o.DevCodes && strings.HasPrefix(url, "http://")) {
		return SigningView{}, invalid("the signing service must be an https:// address")
	}
	c := SigningConfig{Mode: "remote", RemoteURL: url, RemoteKey: key, Location: strings.TrimSpace(location)}
	// A trial signature fixes which certificate the service signs with.
	probe := []byte("Ledgerline signing check " + o.now().Format(time.RFC3339))
	cms, err := o.remoteSign(ctx, c, probe, "signing-check")
	if err != nil {
		return SigningView{}, invalid("the signing service did not sign a test document: %s", err.Error())
	}
	v, err := sign.Verify(cms, probe)
	if err != nil {
		return SigningView{}, invalid("the signing service returned a signature that does not check: %s", err.Error())
	}
	c.Info, c.Expect = v.Signer, v.Signer.SHA256
	return o.saveSigning(ctx, tenant, actor, c)
}

// SetSigningOff removes the set-up.
func (o *Ops) SetSigningOff(ctx context.Context, tenant, actor string) (SigningView, error) {
	if err := o.require(ctx, tenant, actor, "Removing the signing certificate", RoleAdmin); err != nil {
		return SigningView{}, err
	}
	return o.saveSigning(ctx, tenant, actor, SigningConfig{Mode: "off"})
}

func (o *Ops) saveSigning(ctx context.Context, tenant, actor string, c SigningConfig) (SigningView, error) {
	prev := o.signingConfig(ctx, tenant)
	c.Version = prev.Version + 1
	c.UpdatedBy, c.UpdatedAt = actor, o.now().Format(time.RFC3339)
	if c.Location == "" {
		c.Location = prev.Location
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSigning, "main", c, actor); err != nil {
		return SigningView{}, err
	}
	o.log(ctx, tenant, actor, "signing.set", c.Mode, map[string]any{"certificate": c.Info.Name, "serial": c.Info.Serial, "valid_until": c.Info.Until})
	return o.Signing(ctx, tenant, actor)
}

// docSigner returns the firm's signing function and the certificate it uses.
func (o *Ops) docSigner(ctx context.Context, tenant string) (func([]byte, time.Time) ([]byte, error), sign.Info, error) {
	c := o.signingConfig(ctx, tenant)
	switch c.Mode {
	case "file":
		s, err := o.loadedSigner(tenant, c)
		if err != nil {
			return nil, c.Info, err
		}
		if err := s.Check(o.now()); err != nil {
			return nil, c.Info, err
		}
		return s.SignDetached, sign.CertInfo(s.Cert), nil
	case "remote":
		return func(content []byte, at time.Time) ([]byte, error) {
			cms, err := o.remoteSign(ctx, c, content, "")
			if err != nil {
				return nil, err
			}
			v, err := sign.Verify(cms, content)
			if err != nil {
				return nil, fmt.Errorf("the signing service's signature does not check: %w", err)
			}
			if c.Expect != "" && v.Signer.SHA256 != c.Expect {
				return nil, errors.New("the signing service signed with a different certificate than the one set up")
			}
			return cms, nil
		}, c.Info, nil
	case "test":
		s := testSigner(tenant)
		return s.SignDetached, sign.CertInfo(s.Cert), nil
	}
	return nil, sign.Info{}, errors.New("no digital signature is set up (Settings → Digital signature)")
}

func (o *Ops) loadedSigner(tenant string, c SigningConfig) (*sign.Signer, error) {
	signers.mu.Lock()
	defer signers.mu.Unlock()
	if x, ok := signers.m[tenant]; ok && x.version == c.Version {
		return x.s, nil
	}
	raw, err := base64.StdEncoding.DecodeString(c.File)
	if err != nil {
		return nil, err
	}
	s, err := sign.Load(raw, c.Password)
	if err != nil {
		return nil, err
	}
	signers.m[tenant] = cachedSigner{version: c.Version, s: s}
	return s, nil
}

var (
	testSignerMu sync.Mutex
	testSigners  = map[string]*sign.Signer{}
)

// testSigner is a throwaway certificate for development servers, clearly
// named as not valid for clients.
func testSigner(tenant string) *sign.Signer {
	testSignerMu.Lock()
	defer testSignerMu.Unlock()
	if s, ok := testSigners[tenant]; ok && time.Now().Before(s.Cert.NotAfter.Add(-time.Hour)) {
		return s
	}
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "TEST CERTIFICATE - NOT VALID (" + tenant + ")", Organization: []string{"Ledgerline development server"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(30 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	cert, _ := x509.ParseCertificate(der)
	s := &sign.Signer{Key: k, Cert: cert}
	testSigners[tenant] = s
	return s
}

func (o *Ops) remoteSign(ctx context.Context, c SigningConfig, content []byte, name string) ([]byte, error) {
	sum := sha256.Sum256(content)
	body, _ := json.Marshal(map[string]string{"sha256": hex.EncodeToString(sum[:]), "signing_time": o.now().Format(time.RFC3339), "document": name})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.RemoteURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.RemoteKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.RemoteKey)
	}
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("signing service: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("signing service answered %d", resp.StatusCode)
	}
	var out struct {
		CMS string `json:"cms"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.CMS == "" {
		return nil, errors.New("signing service gave no signature")
	}
	return base64.StdEncoding.DecodeString(out.CMS)
}

// signPDF renders a report and signs it with the firm's certificate. It
// returns the file and the certificate used.
func (o *Ops) signPDF(ctx context.Context, tenant string, r pdf.Report, title, reason string) ([]byte, sign.Info, error) {
	fn, info, err := o.docSigner(ctx, tenant)
	if err != nil {
		return nil, sign.Info{}, err
	}
	at := o.now()
	c := o.signingConfig(ctx, tenant)
	set, _ := o.Settings(ctx, tenant)
	r.Stamp = []string{"Digitally signed by " + info.Name, "Date: " + at.In(ist).Format("02-Jan-2006 15:04 MST"), "Reason: " + reason, "Certificate " + info.Serial + " issued by " + info.Issuer}
	if c.Mode == "test" {
		r.Banner = strings.TrimSpace(r.Banner + " TEST SIGNATURE: this development server signs with a test certificate. Not valid for clients.")
	}
	d := pdf.Render(r)
	b, err := d.Bytes(pdf.Meta{Title: title, Author: set.Firm, Subject: reason, Created: at}, &pdf.SignatureSpec{
		Name: info.Name, Reason: reason, Location: firstOf(c.Location, "India"), Contact: set.ComplianceEmail, At: at,
		Sign: func(content []byte) ([]byte, error) { return fn(content, at) },
	})
	return b, info, err
}

// VerifyPDF checks the signature on a PDF (one Ledgerline sent, or one
// from the firm's previous back office).
func VerifyPDF(data []byte) (sign.Verified, bool, error) {
	sigs, err := pdf.Signatures(data)
	if err != nil {
		return sign.Verified{}, false, err
	}
	s := sigs[len(sigs)-1]
	v, err := sign.Verify(s.CMS, s.Content)
	return v, s.WholeDoc, err
}

// devFirm is a firm seeded with sample data for development and tests: it
// signs with a test certificate and simulates mail delivery.
func (o *Ops) devFirm(ctx context.Context, tenant string) bool {
	_, _, err := docs.Get[struct{}](ctx, o.Docs, tenant, cSigning, "devfirm")
	return err == nil
}
