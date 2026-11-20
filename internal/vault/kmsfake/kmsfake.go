// Package kmsfake is a stand-in for AWS KMS (with STS AssumeRole) and for a
// firm's own key service, for tests: the network is off, and a test must
// be able to disable a key or revoke access and see what Ledgerline does.
//
// The fake checks what the real services check: every KMS and STS request
// must carry a valid AWS Signature Version 4 (verified here independently
// of the vault's signer, from the headers the request says it signed),
// in the fake's region, from a known access key; encryption context is
// bound into each ciphertext, so a decrypt with a different context fails
// as it would on KMS. It does not import the vault, so the vault's own
// tests can use it.
package kmsfake

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Call is one request the fake KMS answered.
type Call struct {
	Service       string // kms or sts
	Target        string // Encrypt, Decrypt, AssumeRole
	AccessKey     string
	Context       map[string]string
	SignedHeaders string
	SessionToken  bool
	Status        int
}

type key struct {
	aead    cipher.AEAD
	enabled bool
}

type tmpCred struct {
	secret, token string
	until         time.Time
	role          string
}

// KMS is a fake AWS KMS and STS.
type KMS struct {
	Server  *httptest.Server
	Region  string
	Account string
	Now     func() time.Time

	mu      sync.Mutex
	keys    map[string]*key
	creds   map[string]string // access key ID -> secret
	revoked map[string]bool   // access keys (or roles) whose access was revoked
	roles   map[string]string // role ARN -> external ID
	tmp     map[string]tmpCred
	calls   []Call
	n       int
}

// New starts a fake in region for account.
func New(region, account string) *KMS {
	f := &KMS{Region: region, Account: account, keys: map[string]*key{}, creds: map[string]string{}, revoked: map[string]bool{}, roles: map[string]string{}, tmp: map[string]tmpCred{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	return f
}

// Close stops the server.
func (f *KMS) Close() { f.Server.Close() }

// URL is the fake's address (KMS and STS both).
func (f *KMS) URL() string { return f.Server.URL + "/" }

// Client trusts the fake's certificate.
func (f *KMS) Client() *http.Client { return f.Server.Client() }

func (f *KMS) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// AddKey makes an enabled key and returns its ARN.
func (f *KMS) AddKey() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	arn := fmt.Sprintf("arn:aws:kms:%s:%s:key/0000000%d-1111-2222-3333-444455556666", f.Region, f.Account, f.n)
	k := make([]byte, 32)
	rand.Read(k)
	b, _ := aes.NewCipher(k)
	a, _ := cipher.NewGCM(b)
	f.keys[arn] = &key{aead: a, enabled: true}
	return arn
}

// AddCreds allows an access key.
func (f *KMS) AddCreds(id, secret string) {
	f.mu.Lock()
	f.creds[id] = secret
	f.mu.Unlock()
}

// AddRole allows assuming a role with an external ID.
func (f *KMS) AddRole(arn, externalID string) {
	f.mu.Lock()
	f.roles[arn] = externalID
	f.mu.Unlock()
}

// SetEnabled disables or enables a key (DisabledException while off).
func (f *KMS) SetEnabled(arn string, on bool) {
	f.mu.Lock()
	f.keys[arn].enabled = on
	f.mu.Unlock()
}

// Revoke withdraws (or gives back) an access key's or role's use of every
// key, as removing a grant or a key policy statement does
// (AccessDeniedException).
func (f *KMS) Revoke(who string, revoked bool) {
	f.mu.Lock()
	f.revoked[who] = revoked
	f.mu.Unlock()
}

// Calls lists the requests answered so far.
func (f *KMS) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

func (f *KMS) record(c Call) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
}

func kmsErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"__type": "com.amazonaws.kms#" + code, "message": msg})
}

func (f *KMS) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	service := "kms"
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		service = "sts"
	}
	c := Call{Service: service}
	who, role, status, code, msg := f.verify(r, body, service, &c)
	if status != 0 {
		c.Status = status
		f.record(c)
		if service == "sts" {
			stsErr(w, status, code, msg)
		} else {
			kmsErr(w, status, code, msg)
		}
		return
	}
	if service == "sts" {
		f.assumeRole(w, body, who, &c)
		return
	}
	c.Target = strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "TrentService.")
	var in struct {
		KeyId             string
		Plaintext         []byte
		CiphertextBlob    []byte
		EncryptionContext map[string]string
	}
	if err := json.Unmarshal(body, &in); err != nil {
		c.Status = 400
		f.record(c)
		kmsErr(w, 400, "SerializationException", "bad JSON")
		return
	}
	c.Context = in.EncryptionContext
	fail := func(status int, code, msg string) {
		c.Status = status
		f.record(c)
		kmsErr(w, status, code, msg)
	}
	f.mu.Lock()
	revoked := f.revoked[who] || (role != "" && f.revoked[role])
	f.mu.Unlock()
	aad := contextAAD(in.EncryptionContext)
	switch c.Target {
	case "Encrypt":
		f.mu.Lock()
		k := f.keys[in.KeyId]
		f.mu.Unlock()
		switch {
		case k == nil:
			fail(400, "NotFoundException", "Key '"+in.KeyId+"' does not exist")
		case revoked:
			fail(400, "AccessDeniedException", "User is not authorized to perform: kms:Encrypt")
		case !k.enabled:
			fail(400, "DisabledException", in.KeyId+" is disabled.")
		default:
			nonce := make([]byte, k.aead.NonceSize())
			rand.Read(nonce)
			blob := append([]byte{byte(len(in.KeyId))}, in.KeyId...)
			blob = append(blob, nonce...)
			blob = k.aead.Seal(blob, nonce, in.Plaintext, aad)
			c.Status = 200
			f.record(c)
			json.NewEncoder(w).Encode(map[string]any{"CiphertextBlob": blob, "KeyId": in.KeyId, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"})
		}
	case "Decrypt":
		if len(in.CiphertextBlob) < 1 || len(in.CiphertextBlob) < 1+int(in.CiphertextBlob[0])+12 {
			fail(400, "InvalidCiphertextException", "")
			return
		}
		n := int(in.CiphertextBlob[0])
		arn := string(in.CiphertextBlob[1 : 1+n])
		f.mu.Lock()
		k := f.keys[arn]
		f.mu.Unlock()
		switch {
		case k == nil:
			fail(400, "InvalidCiphertextException", "")
		case in.KeyId != "" && in.KeyId != arn:
			fail(400, "IncorrectKeyException", "The key ID in the request does not identify a KMS key that can perform this operation.")
		case revoked:
			fail(400, "AccessDeniedException", "User is not authorized to perform: kms:Decrypt")
		case !k.enabled:
			fail(400, "DisabledException", arn+" is disabled.")
		default:
			rest := in.CiphertextBlob[1+n:]
			pt, err := k.aead.Open(nil, rest[:12], rest[12:], aad)
			if err != nil {
				fail(400, "InvalidCiphertextException", "")
				return
			}
			c.Status = 200
			f.record(c)
			json.NewEncoder(w).Encode(map[string]any{"Plaintext": pt, "KeyId": arn, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"})
		}
	default:
		fail(400, "UnknownOperationException", c.Target)
	}
}

func contextAAD(m map[string]string) []byte {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var b bytes.Buffer
	for _, k := range ks {
		b.WriteString(k + "\x00" + m[k] + "\x00")
	}
	return b.Bytes()
}

// verify checks the SigV4 signature. It returns the access key (or, for
// temporary credentials, the role behind them).
func (f *KMS) verify(r *http.Request, body []byte, service string, c *Call) (who, role string, status int, code, msg string) {
	auth := r.Header.Get("Authorization")
	amz := r.Header.Get("X-Amz-Date")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || amz == "" {
		return "", "", 400, "MissingAuthenticationToken", "Request is missing Authentication Token"
	}
	if service == "kms" && (r.Header.Get("X-Amz-Target") == "" || r.Header.Get("Content-Type") != "application/x-amz-json-1.1") {
		return "", "", 400, "UnknownOperationException", "missing X-Amz-Target or content type"
	}
	parts := map[string]string{}
	for _, p := range strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		parts[k] = v
	}
	cred := strings.Split(parts["Credential"], "/")
	if len(cred) != 5 || cred[4] != "aws4_request" {
		return "", "", 400, "IncompleteSignature", "bad credential"
	}
	akid, day, region, svc := cred[0], cred[1], cred[2], cred[3]
	c.AccessKey, c.SignedHeaders = akid, parts["SignedHeaders"]
	if region != f.Region || svc != service {
		return "", "", 400, "InvalidSignatureException", "Credential should be scoped to a valid region and service"
	}
	t, err := time.Parse("20060102T150405Z", amz)
	if err != nil || amz[:8] != day || f.now().Sub(t) > 5*time.Minute || t.Sub(f.now()) > 5*time.Minute {
		return "", "", 400, "InvalidSignatureException", "Signature expired or date wrong"
	}
	f.mu.Lock()
	secret, ok := f.creds[akid]
	tc, isTmp := f.tmp[akid]
	f.mu.Unlock()
	if isTmp {
		if r.Header.Get("X-Amz-Security-Token") != tc.token {
			return "", "", 400, "UnrecognizedClientException", "The security token included in the request is invalid."
		}
		if !f.now().Before(tc.until) {
			return "", "", 400, "ExpiredTokenException", "The security token included in the request is expired"
		}
		secret, ok, role = tc.secret, true, tc.role
		c.SessionToken = true
	}
	if !ok {
		return "", "", 400, "UnrecognizedClientException", "The security token included in the request is invalid."
	}
	signed := strings.Split(parts["SignedHeaders"], ";")
	need := []string{"host", "x-amz-date"}
	if service == "kms" {
		need = append(need, "x-amz-target", "content-type")
	}
	if isTmp {
		need = append(need, "x-amz-security-token")
	}
	for _, n := range need {
		if !contains(signed, n) {
			return "", "", 400, "InvalidSignatureException", "header " + n + " is not signed"
		}
	}
	var ch strings.Builder
	for _, h := range signed {
		v := r.Header.Get(h)
		if h == "host" {
			v = r.Host
		}
		ch.WriteString(h + ":" + strings.Join(strings.Fields(v), " ") + "\n")
	}
	q := r.URL.Query()
	var qs []string
	for k, vs := range q {
		for _, v := range vs {
			qs = append(qs, escape(k)+"="+escape(v))
		}
	}
	sort.Strings(qs)
	bh := sha256.Sum256(body)
	creq := r.Method + "\n" + firstOr(r.URL.EscapedPath(), "/") + "\n" + strings.Join(qs, "&") + "\n" + ch.String() + "\n" + parts["SignedHeaders"] + "\n" + hex.EncodeToString(bh[:])
	ch2 := sha256.Sum256([]byte(creq))
	sts := "AWS4-HMAC-SHA256\n" + amz + "\n" + day + "/" + region + "/" + svc + "/aws4_request\n" + hex.EncodeToString(ch2[:])
	k := mac([]byte("AWS4"+secret), day)
	k = mac(k, region)
	k = mac(k, svc)
	k = mac(k, "aws4_request")
	want := hex.EncodeToString(mac(k, sts))
	if !hmac.Equal([]byte(want), []byte(parts["Signature"])) {
		return "", "", 400, "InvalidSignatureException", "The request signature we calculated does not match the signature you provided."
	}
	if role != "" {
		return akid, role, 0, "", ""
	}
	return akid, "", 0, "", ""
}

func (f *KMS) assumeRole(w http.ResponseWriter, body []byte, who string, c *Call) {
	c.Target = "AssumeRole"
	v, _ := url.ParseQuery(string(body))
	arn, ext := v.Get("RoleArn"), v.Get("ExternalId")
	f.mu.Lock()
	want, ok := f.roles[arn]
	revoked := f.revoked[who] || f.revoked[arn]
	f.mu.Unlock()
	if v.Get("Action") != "AssumeRole" || !ok || want != ext || revoked {
		c.Status = 403
		f.record(*c)
		stsErr(w, 403, "AccessDenied", "User is not authorized to perform: sts:AssumeRole on resource: "+arn)
		return
	}
	id := "ASIA" + strings.ToUpper(hex.EncodeToString(randBytes(8)))
	sec, tok := hex.EncodeToString(randBytes(20)), hex.EncodeToString(randBytes(40))
	until := f.now().Add(15 * time.Minute).UTC()
	f.mu.Lock()
	f.tmp[id] = tmpCred{secret: sec, token: tok, until: until, role: arn}
	f.mu.Unlock()
	c.Status = 200
	f.record(*c)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>%s</AccessKeyId><SecretAccessKey>%s</SecretAccessKey><SessionToken>%s</SessionToken><Expiration>%s</Expiration></Credentials><AssumedRoleUser><Arn>%s/%s</Arn></AssumedRoleUser></AssumeRoleResult></AssumeRoleResponse>`,
		id, sec, tok, until.Format(time.RFC3339), arn, v.Get("RoleSessionName"))
}

func stsErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<ErrorResponse><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error></ErrorResponse>`, code, msg)
}

func mac(k []byte, s string) []byte {
	m := hmac.New(sha256.New, k)
	m.Write([]byte(s))
	return m.Sum(nil)
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if x == y {
			return true
		}
	}
	return false
}

func firstOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func escape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// ---- a firm's own key service ----

// KeyService is a fake of the key service a firm runs (see the vault's
// remotekey.go for the protocol).
type KeyService struct {
	Server *httptest.Server
	Secret string
	Now    func() time.Time

	mu      sync.Mutex
	aead    cipher.AEAD
	refused bool
	down    bool
	calls   int
	bad     int // requests refused for a bad signature
}

// NewKeyService starts one with a shared secret.
func NewKeyService(secret string) *KeyService {
	k := make([]byte, 32)
	rand.Read(k)
	b, _ := aes.NewCipher(k)
	a, _ := cipher.NewGCM(b)
	s := &KeyService{Secret: secret, aead: a}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	return s
}

// Close stops it.
func (s *KeyService) Close() { s.Server.Close() }

// Client trusts its certificate.
func (s *KeyService) Client() *http.Client { return s.Server.Client() }

// SetRefused makes it refuse (403) or work again.
func (s *KeyService) SetRefused(on bool) { s.mu.Lock(); s.refused = on; s.mu.Unlock() }

// SetDown makes it fail (503) or work again.
func (s *KeyService) SetDown(on bool) { s.mu.Lock(); s.down = on; s.mu.Unlock() }

// Calls counts requests with a good signature; Bad those without.
func (s *KeyService) Calls() (good, bad int) { s.mu.Lock(); defer s.mu.Unlock(); return s.calls, s.bad }

func (s *KeyService) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	ts := r.Header.Get("X-Ledgerline-Timestamp")
	n, _ := strconv.ParseInt(ts, 10, 64)
	m := hmac.New(sha256.New, []byte(s.Secret))
	m.Write([]byte(ts + "\n" + r.URL.Path + "\n"))
	m.Write(body)
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := now.Sub(time.Unix(n, 0)); d > 5*time.Minute || d < -5*time.Minute || !hmac.Equal([]byte(r.Header.Get("X-Ledgerline-Signature")), []byte("v1="+hex.EncodeToString(m.Sum(nil)))) {
		s.bad++
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]string{"error": "bad signature"})
		return
	}
	s.calls++
	if s.down {
		w.WriteHeader(503)
		return
	}
	if s.refused {
		w.WriteHeader(403)
		json.NewEncoder(w).Encode(map[string]string{"error": "key withdrawn by the firm"})
		return
	}
	var in struct {
		Context map[string]string `json:"context"`
		Key     []byte            `json:"key"`
		Wrapped []byte            `json:"wrapped"`
	}
	json.Unmarshal(body, &in)
	aad := contextAAD(in.Context)
	switch r.URL.Path {
	case "/wrap":
		nonce := randBytes(12)
		json.NewEncoder(w).Encode(map[string]any{"wrapped": s.aead.Seal(nonce, nonce, in.Key, aad), "key_id": "firm-hsm-key-1"})
	case "/unwrap":
		if len(in.Wrapped) < 12 {
			w.WriteHeader(400)
			return
		}
		pt, err := s.aead.Open(nil, in.Wrapped[:12], in.Wrapped[12:], aad)
		if err != nil {
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]string{"error": "does not unwrap with this context"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"key": pt})
	default:
		w.WriteHeader(404)
	}
}
