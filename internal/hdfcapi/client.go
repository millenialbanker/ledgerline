package hdfcapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Addresses of the bank's API gateway. Production needs mutual TLS; the
// bank also lists api1.hdfcbank.com for one-way TLS.
const (
	ProdBase = "https://api.hdfcbank.com/api"
	UATBase  = "https://api.hdfcuat.bank.in/api"
)

// Config is one firm's API registration on the bank's developer portal.
type Config struct {
	Base     string // gateway base, e.g. ProdBase
	TokenURL string // empty: <scheme://host of Base>/auth/oauth/v1/token
	APIKey   string // consumer key (client ID)
	Secret   string // consumer secret
	Scope    string // the application's scope
	AppType  string // only where the bank gives one

	Key      *rsa.PrivateKey // signs requests, opens responses
	BankKey  *rsa.PublicKey  // encrypts requests, checks responses
	TLSCerts []tls.Certificate

	HTTP *http.Client // nil: one is made with TLSCerts and a 60 s timeout
	Now  func() time.Time
}

// APIError is a refusal from the bank's gateway or service.
type APIError struct {
	Status int
	Code   string
	Reason string
	Body   string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("HDFC Bank answered %d", e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Reason != "" {
		s += ": " + e.Reason
	}
	return s
}

// Sure reports whether the bank certainly did not act on the request: the
// gateway refused it (4xx) rather than failing or not answering.
func (e *APIError) Sure() bool { return e.Status >= 400 && e.Status < 500 }

// Client calls the bank. It is safe for concurrent use.
type Client struct {
	cfg            Config
	myKid, bankKid string
	mu             sync.Mutex
	token          string
	tokenExp       time.Time
}

// New checks a Config and makes a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Base == "" || cfg.APIKey == "" || cfg.Secret == "" || cfg.Scope == "" {
		return nil, errors.New("the address, consumer key, consumer secret and scope are needed")
	}
	if cfg.Key == nil || cfg.BankKey == nil {
		return nil, errors.New("the firm's private key and the bank's certificate are needed")
	}
	u, err := url.Parse(cfg.Base)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("the address must start with https://")
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = u.Scheme + "://" + u.Host + "/auth/oauth/v1/token"
	}
	cfg.Base = strings.TrimRight(cfg.Base, "/")
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: cfg.TLSCerts}, ForceAttemptHTTP2: true,
		}}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	c := &Client{cfg: cfg}
	if c.myKid, err = KID(&cfg.Key.PublicKey); err != nil {
		return nil, err
	}
	if c.bankKid, err = KID(cfg.BankKey); err != nil {
		return nil, err
	}
	return c, nil
}

// Kids returns the firm's and the bank's key IDs (for the screen).
func (c *Client) Kids() (mine, bank string) { return c.myKid, c.bankKid }

type tokenReply struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   string `json:"expires_in"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// Token returns an OAuth token, fetched again a minute before it expires.
func (c *Client) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
	if c.token != "" && now.Before(c.tokenExp) {
		return c.token, nil
	}
	q := url.Values{"grant_type": {"client_credentials"}, "scope": {c.cfg.Scope}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.cfg.APIKey, c.cfg.Secret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var t tokenReply
	_ = json.Unmarshal(body, &t)
	if res.StatusCode != 200 || t.AccessToken == "" {
		e := &APIError{Status: res.StatusCode, Code: t.Error, Reason: t.Description, Body: clip(body)}
		if e.Code == "" {
			e.Code, e.Reason = problem(body)
		}
		return "", fmt.Errorf("token: %w", e)
	}
	secs, _ := strconv.Atoi(strings.TrimSpace(t.ExpiresIn))
	if secs <= 0 {
		secs = 900
	}
	c.token, c.tokenExp = t.AccessToken, now.Add(time.Duration(secs)*time.Second-time.Minute)
	return c.token, nil
}

// problem reads the gateway's application/problem+json error.
func problem(body []byte) (string, string) {
	var p struct {
		Title  string `json:"title"`
		Errors []struct {
			Code   string `json:"code"`
			Reason string `json:"reason"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &p) != nil {
		return "", ""
	}
	if len(p.Errors) > 0 {
		return p.Errors[0].Code, p.Errors[0].Reason
	}
	return "", p.Title
}

func clip(b []byte) string {
	if len(b) > 500 {
		b = b[:500]
	}
	return string(b)
}

// NewTransactionID is an alphanumeric request ID (the bank allows no
// special characters).
func NewTransactionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Call sends req (any JSON value) to path under the gateway base, signed
// and encrypted, and decodes the bank's verified answer into out. txnID
// is the transactionId header (empty: a new one).
func (c *Client) Call(ctx context.Context, path, txnID string, req, out any) error {
	plain, err := json.Marshal(req)
	if err != nil {
		return err
	}
	tok, err := c.Token(ctx)
	if err != nil {
		return err
	}
	jws, err := Sign(plain, c.cfg.Key, c.myKid)
	if err != nil {
		return err
	}
	jwe, err := Encrypt([]byte(jws), c.cfg.BankKey, c.bankKid)
	if err != nil {
		return err
	}
	if txnID == "" {
		txnID = NewTransactionID()
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Base+"/"+strings.TrimLeft(path, "/"), bytes.NewReader([]byte(jwe)))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/jose")
	hr.Header.Set("apikey", c.cfg.APIKey)
	hr.Header.Set("scope", c.cfg.Scope)
	hr.Header.Set("transactionId", txnID)
	hr.Header.Set("Authorization", "Bearer "+tok)
	if c.cfg.AppType != "" {
		hr.Header.Set("appType", c.cfg.AppType)
	}
	res, err := c.cfg.HTTP.Do(hr)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != 200 {
		e := &APIError{Status: res.StatusCode, Body: clip(body)}
		e.Code, e.Reason = problem(body)
		if res.StatusCode == 401 {
			c.mu.Lock()
			c.token = "" // fetched again next time
			c.mu.Unlock()
		}
		return e
	}
	inner, err := Decrypt(string(bytes.TrimSpace(body)), c.cfg.Key, c.myKid)
	if err != nil {
		return fmt.Errorf("the bank's answer: %w", err)
	}
	payload, err := Verify(string(inner), c.cfg.BankKey, c.bankKid)
	if err != nil {
		return fmt.Errorf("the bank's answer: %w", err)
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("the bank's answer is not the expected JSON: %w", err)
	}
	return nil
}

// ---- keys and certificates ----

// ParsePrivateKey reads an RSA private key in PEM (PKCS#1 or PKCS#8).
func ParsePrivateKey(s string) (*rsa.PrivateKey, error) {
	b, _ := pem.Decode([]byte(strings.TrimSpace(s)))
	if b == nil {
		return nil, errors.New("the private key is not in PEM form (-----BEGIN … PRIVATE KEY-----)")
	}
	if k, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the private key could not be read: %v", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the private key must be an RSA key")
	}
	return rk, nil
}

// ParseCertificates reads the PEM certificates in s, in order.
func ParseCertificates(s string) ([]*x509.Certificate, error) {
	rest := []byte(strings.TrimSpace(s))
	var out []*x509.Certificate
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, fmt.Errorf("a certificate could not be read: %v", err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("no certificate found (-----BEGIN CERTIFICATE-----)")
	}
	return out, nil
}

// BankPublicKey reads the bank's public key from its certificate (or a
// PEM public key).
func BankPublicKey(s string) (*rsa.PublicKey, error) {
	if b, _ := pem.Decode([]byte(strings.TrimSpace(s))); b != nil && b.Type == "PUBLIC KEY" {
		k, err := x509.ParsePKIXPublicKey(b.Bytes)
		if err != nil {
			return nil, err
		}
		if rk, ok := k.(*rsa.PublicKey); ok {
			return rk, nil
		}
		return nil, errors.New("the bank's key must be an RSA key")
	}
	cs, err := ParseCertificates(s)
	if err != nil {
		return nil, err
	}
	rk, ok := cs[0].PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("the bank's certificate must hold an RSA key")
	}
	return rk, nil
}

// TLSCertificate pairs the firm's certificate chain with its key for
// mutual TLS. The first certificate must be for key.
func TLSCertificate(chain string, key *rsa.PrivateKey) (tls.Certificate, error) {
	cs, err := ParseCertificates(chain)
	if err != nil {
		return tls.Certificate{}, err
	}
	pub, ok := cs[0].PublicKey.(*rsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return tls.Certificate{}, errors.New("the firm's certificate is not for its private key")
	}
	tc := tls.Certificate{PrivateKey: key, Leaf: cs[0]}
	for _, c := range cs {
		tc.Certificate = append(tc.Certificate, c.Raw)
	}
	return tc, nil
}
