package vault

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A key service the firm runs itself, as a KeyProvider: for a firm not on
// AWS, its key in a key file or an HSM behind a small HTTPS service. The
// protocol is Ledgerline's own (there is no standard to follow), kept as
// small as possible:
//
//	POST <address>/wrap    {"context": {...}, "key": "<base64>"}
//	                    -> {"wrapped": "<base64>", "key_id": "<the firm's name for its key>"}
//	POST <address>/unwrap  {"context": {...}, "key_id": "...", "wrapped": "<base64>"}
//	                    -> {"key": "<base64>"}
//
// context is the encryption context of Bind (firm code, purpose, root
// version). The service must bind the wrapped key to it (for example as
// the additional data of AES-GCM), so a wrapped key cannot be unwrapped for
// another firm or version.
//
// Each request carries X-Ledgerline-Timestamp (Unix seconds) and
// X-Ledgerline-Signature: "v1=" and the hex HMAC-SHA256, under the shared
// secret, of timestamp, "\n", path, "\n", body. The secret itself is never
// sent. The service should refuse a request more than 5 minutes old or
// with a wrong signature (VerifyRemoteRequest does both). Mutual TLS can be
// used as well or instead: Ledgerline then presents the client certificate
// in the connection.
//
// Answers: 200 with the body above; 401, 403, 404, 410 or 423 mean the
// service refuses (the key is withdrawn: Ledgerline stops serving the
// firm); anything else means it failed and Ledgerline asks again later.

// RemoteKeyService is a firm's own key service.
type RemoteKeyService struct {
	URL    string // https://…, without /wrap or /unwrap
	Secret string // shared secret for the request signature; may be empty with mutual TLS
	Client *http.Client
	Now    func() time.Time
}

func (r *RemoteKeyService) String() string { return "firm key service " + r.URL }

// Wrap asks the service to wrap root.
func (r *RemoteKeyService) Wrap(ctx context.Context, root []byte, bind map[string]string) ([]byte, string, error) {
	var out struct {
		Wrapped []byte `json:"wrapped"`
		KeyID   string `json:"key_id"`
	}
	if err := r.post(ctx, "/wrap", map[string]any{"context": bind, "key": root}, &out); err != nil {
		return nil, "", err
	}
	if len(out.Wrapped) == 0 {
		return nil, "", &ProviderError{Msg: "the firm's key service returned no wrapped key"}
	}
	return out.Wrapped, out.KeyID, nil
}

// Unwrap asks the service to unwrap blob.
func (r *RemoteKeyService) Unwrap(ctx context.Context, blob []byte, keyID string, bind map[string]string) ([]byte, error) {
	var out struct {
		Key []byte `json:"key"`
	}
	if err := r.post(ctx, "/unwrap", map[string]any{"context": bind, "key_id": keyID, "wrapped": blob}, &out); err != nil {
		return nil, err
	}
	return out.Key, nil
}

// RemoteSignature is the X-Ledgerline-Signature value for a request.
func RemoteSignature(secret, ts, path string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "\n" + path + "\n"))
	m.Write(body)
	return "v1=" + hex.EncodeToString(m.Sum(nil))
}

// VerifyRemoteRequest checks a request's signature and age, for a key
// service written in Go (and the tests).
func VerifyRemoteRequest(secret string, r *http.Request, body []byte, now time.Time) error {
	ts := r.Header.Get("X-Ledgerline-Timestamp")
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("no timestamp")
	}
	if d := now.Sub(time.Unix(n, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return errors.New("request too old or from the future")
	}
	if !hmac.Equal([]byte(r.Header.Get("X-Ledgerline-Signature")), []byte(RemoteSignature(secret, ts, r.URL.Path, body))) {
		return errors.New("wrong signature")
	}
	return nil
}

var remoteRefused = map[int]bool{401: true, 403: true, 404: true, 410: true, 423: true}

func (r *RemoteKeyService) post(ctx context.Context, path string, in, out any) error {
	if !strings.HasPrefix(r.URL, "https://") {
		return &ProviderError{Refused: true, Msg: "the key service's address must start with https://"}
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	u := strings.TrimSuffix(r.URL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return &ProviderError{Refused: true, Msg: "the key service's address is not valid"}
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Ledgerline-Timestamp", ts)
	if r.Secret != "" {
		req.Header.Set("X-Ledgerline-Signature", RemoteSignature(r.Secret, ts, req.URL.Path, body))
	}
	c := r.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return &ProviderError{Msg: "the firm's key service could not be reached: " + netReason(err)}
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rb, &e)
		return &ProviderError{Refused: remoteRefused[resp.StatusCode], Code: strconv.Itoa(resp.StatusCode),
			Msg: firstNonEmpty(trimMsg(e.Error), "the firm's key service answered "+resp.Status)}
	}
	if err := json.Unmarshal(rb, out); err != nil {
		return &ProviderError{Msg: "the firm's key service answered in an unexpected form"}
	}
	return nil
}

// RemoteKeyClient is an HTTPS client for a firm's key service: with a
// client certificate for mutual TLS when certPEM and keyPEM are given, and
// trusting only caPEM when it is given (a firm's private CA). base, when
// set, supplies the transport's other settings (tests).
func RemoteKeyClient(certPEM, keyPEM, caPEM string, base *http.Client) (*http.Client, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if base != nil {
		if t, ok := base.Transport.(*http.Transport); ok && t.TLSClientConfig != nil {
			cfg = t.TLSClientConfig.Clone()
		}
	}
	if (certPEM == "") != (keyPEM == "") {
		return nil, errors.New("give both the client certificate and its private key, or neither")
	}
	if certPEM != "" {
		cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
		if err != nil {
			return nil, fmt.Errorf("the client certificate and private key do not load: %v", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	if caPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, errors.New("the key service's CA certificate does not load")
		}
		cfg.RootCAs = pool
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg, Proxy: http.ProxyFromEnvironment}}, nil
}
