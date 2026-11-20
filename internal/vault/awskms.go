package vault

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// AWS KMS in the firm's own AWS account, as a KeyProvider. Two calls of
// the AWS KMS API (version 2014-11-01, JSON 1.1 protocol, target
// TrentService): Encrypt to wrap a root and Decrypt to unwrap it, each with
// the encryption context of Bind, so the firm's key policy can allow
// Ledgerline only for its own firm code. Requests are signed with AWS
// Signature Version 4, written here with the standard library.
//
// Credentials are an access key of the firm's (an IAM user allowed to use
// the key), or an access key that may assume a role in the firm's account
// (STS AssumeRole, with an external ID), which is the usual cross-account
// set-up. Temporary credentials from AssumeRole last 15 minutes.

// KMSRegions are the regions allowed: AWS's Indian regions only, so the
// firm's key and every call to it stay in India.
var KMSRegions = []string{"ap-south-1", "ap-south-2"} // Mumbai, Hyderabad

// AWSCredentials sign requests. String masks them, so a stray %v prints no
// secret.
type AWSCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

func (c AWSCredentials) String() string   { return "AWS access key " + MaskID(c.AccessKeyID) }
func (c AWSCredentials) GoString() string { return c.String() }

// MaskID shows the start and end of a key ID or ARN only.
func MaskID(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("•", len(s))
	}
	return s[:4] + "…" + s[len(s)-4:]
}

// AWSKMS is a firm's KMS key.
type AWSKMS struct {
	Region      string // ap-south-1 or ap-south-2
	KeyARN      string // the key new roots are wrapped with
	Creds       AWSCredentials
	RoleARN     string // assumed with Creds when set
	ExternalID  string
	SessionName string // shows in the firm's CloudTrail, e.g. ledgerline-<firm>
	Endpoint    string // empty for https://kms.<region>.amazonaws.com
	STSEndpoint string // empty for https://sts.<region>.amazonaws.com
	Client      *http.Client
	Now         func() time.Time

	mu       sync.Mutex
	tmp      AWSCredentials
	tmpUntil time.Time
}

func (k *AWSKMS) String() string { return "AWS KMS " + k.Region + " " + MaskID(k.KeyARN) }

// ParseKMSKeyARN checks a KMS key or alias ARN in an Indian region and
// returns its region and account.
func ParseKMSKeyARN(arn string) (region, account string, err error) {
	p := strings.SplitN(strings.TrimSpace(arn), ":", 6)
	if len(p) != 6 || p[0] != "arn" || p[1] != "aws" || p[2] != "kms" {
		return "", "", errors.New("the key must be a KMS key ARN, like arn:aws:kms:ap-south-1:111122223333:key/…")
	}
	region, account = p[3], p[4]
	if !allowedRegion(region) {
		return "", "", fmt.Errorf("the key is in %s: only keys in India (%s) are allowed", region, strings.Join(KMSRegions, " or "))
	}
	if len(account) != 12 || strings.Trim(account, "0123456789") != "" {
		return "", "", errors.New("the key ARN's account must be 12 digits")
	}
	res := p[5]
	if !(strings.HasPrefix(res, "key/") && len(res) > 4) && !(strings.HasPrefix(res, "alias/") && len(res) > 6) {
		return "", "", errors.New("the key ARN must end in key/<key ID> or alias/<name>")
	}
	return region, account, nil
}

func allowedRegion(r string) bool {
	for _, x := range KMSRegions {
		if r == x {
			return true
		}
	}
	return false
}

func (k *AWSKMS) now() time.Time {
	if k.Now != nil {
		return k.Now()
	}
	return time.Now()
}

func (k *AWSKMS) client() *http.Client {
	if k.Client != nil {
		return k.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (k *AWSKMS) check(keyARN string) error {
	if !allowedRegion(k.Region) {
		return &ProviderError{Refused: true, Msg: "the region must be " + strings.Join(KMSRegions, " or ")}
	}
	region, _, err := ParseKMSKeyARN(keyARN)
	if err != nil {
		return &ProviderError{Refused: true, Msg: err.Error()}
	}
	if region != k.Region {
		return &ProviderError{Refused: true, Msg: "the key is in " + region + " but the connection's region is " + k.Region}
	}
	if k.Creds.AccessKeyID == "" || k.Creds.SecretAccessKey == "" {
		return &ProviderError{Refused: true, Msg: "the AWS access key is not set"}
	}
	return nil
}

type kmsEncryptIn struct {
	KeyId             string            `json:"KeyId"`
	Plaintext         []byte            `json:"Plaintext"`
	EncryptionContext map[string]string `json:"EncryptionContext"`
}

type kmsDecryptIn struct {
	KeyId             string            `json:"KeyId,omitempty"`
	CiphertextBlob    []byte            `json:"CiphertextBlob"`
	EncryptionContext map[string]string `json:"EncryptionContext"`
}

type kmsOut struct {
	KeyId          string `json:"KeyId"`
	CiphertextBlob []byte `json:"CiphertextBlob"`
	Plaintext      []byte `json:"Plaintext"`
}

// Wrap encrypts root with the firm's key (KMS Encrypt).
func (k *AWSKMS) Wrap(ctx context.Context, root []byte, bind map[string]string) ([]byte, string, error) {
	if err := k.check(k.KeyARN); err != nil {
		return nil, "", err
	}
	var out kmsOut
	if err := k.call(ctx, "Encrypt", kmsEncryptIn{KeyId: k.KeyARN, Plaintext: root, EncryptionContext: bind}, &out); err != nil {
		return nil, "", err
	}
	if len(out.CiphertextBlob) == 0 {
		return nil, "", &ProviderError{Msg: "AWS KMS returned no ciphertext"}
	}
	return out.CiphertextBlob, firstNonEmpty(out.KeyId, k.KeyARN), nil
}

// Unwrap decrypts a wrapped root (KMS Decrypt). keyID, the key that
// wrapped it, is sent too, so KMS refuses a blob made under any other key.
func (k *AWSKMS) Unwrap(ctx context.Context, blob []byte, keyID string, bind map[string]string) ([]byte, error) {
	keyID = firstNonEmpty(keyID, k.KeyARN)
	if err := k.check(keyID); err != nil {
		return nil, err
	}
	var out kmsOut
	if err := k.call(ctx, "Decrypt", kmsDecryptIn{KeyId: keyID, CiphertextBlob: blob, EncryptionContext: bind}, &out); err != nil {
		return nil, err
	}
	if strings.Contains(keyID, ":key/") && out.KeyId != "" && out.KeyId != keyID {
		clear(out.Plaintext)
		return nil, &ProviderError{Refused: true, Msg: "AWS KMS decrypted with a different key than the one recorded"}
	}
	return out.Plaintext, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// kmsRefused are KMS error codes that mean no (as opposed to try again).
var kmsRefused = map[string]bool{
	"AccessDeniedException": true, "DisabledException": true, "KMSInvalidStateException": true,
	"NotFoundException": true, "InvalidCiphertextException": true, "IncorrectKeyException": true,
	"InvalidKeyUsageException": true, "InvalidGrantTokenException": true, "InvalidGrantIdException": true,
	"UnrecognizedClientException": true, "InvalidSignatureException": true, "IncompleteSignature": true,
	"MissingAuthenticationToken": true, "InvalidClientTokenId": true, "ValidationException": true,
}

func (k *AWSKMS) call(ctx context.Context, target string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		creds, err := k.credentials(ctx)
		if err != nil {
			return err
		}
		endpoint := firstNonEmpty(k.Endpoint, "https://kms."+k.Region+".amazonaws.com/")
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return &ProviderError{Refused: true, Msg: "the KMS address is not valid"}
		}
		req.Header.Set("Content-Type", "application/x-amz-json-1.1")
		req.Header.Set("X-Amz-Target", "TrentService."+target)
		SignV4(req, body, creds, k.Region, "kms", k.now())
		resp, err := k.client().Do(req)
		if err != nil {
			return &ProviderError{Msg: "AWS KMS could not be reached: " + netReason(err)}
		}
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			if err := json.Unmarshal(rb, out); err != nil {
				return &ProviderError{Msg: "AWS KMS answered in an unexpected form"}
			}
			return nil
		}
		code, msg := awsJSONError(rb)
		if code == "ExpiredTokenException" && k.RoleARN != "" && attempt == 0 {
			k.mu.Lock()
			k.tmpUntil = time.Time{}
			k.mu.Unlock()
			continue
		}
		return &ProviderError{Refused: kmsRefused[code] || resp.StatusCode == http.StatusForbidden, Code: code, Msg: firstNonEmpty(msg, fmt.Sprintf("AWS KMS answered %d", resp.StatusCode))}
	}
}

// awsJSONError reads a JSON 1.1 error: {"__type": "...#Code", "message": "..."}.
func awsJSONError(b []byte) (code, msg string) {
	var e struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
		Msg2    string `json:"Message"`
	}
	_ = json.Unmarshal(b, &e)
	code = e.Type
	if i := strings.LastIndexByte(code, '#'); i >= 0 {
		code = code[i+1:]
	}
	if i := strings.IndexByte(code, ':'); i >= 0 {
		code = code[:i]
	}
	return code, trimMsg(firstNonEmpty(e.Message, e.Msg2))
}

func trimMsg(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// netReason says why a call failed without the request itself.
func netReason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	switch s := err.Error(); {
	case strings.Contains(s, "deadline") || strings.Contains(s, "timeout"):
		return "no answer in time"
	case strings.Contains(s, "certificate"):
		return "its certificate is not valid"
	default:
		return trimMsg(s)
	}
}

// credentials are the ones to sign KMS calls with: the access key, or
// temporary ones from AssumeRole.
func (k *AWSKMS) credentials(ctx context.Context) (AWSCredentials, error) {
	if k.RoleARN == "" {
		return k.Creds, nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.tmp.AccessKeyID != "" && k.now().Before(k.tmpUntil) {
		return k.tmp, nil
	}
	c, until, err := k.assumeRole(ctx)
	if err != nil {
		return AWSCredentials{}, err
	}
	k.tmp, k.tmpUntil = c, until.Add(-time.Minute)
	return c, nil
}

// assumeRole calls STS AssumeRole (Query API, version 2011-06-15) on the
// regional endpoint.
func (k *AWSKMS) assumeRole(ctx context.Context) (AWSCredentials, time.Time, error) {
	if k.Creds.AccessKeyID == "" || k.Creds.SecretAccessKey == "" {
		return AWSCredentials{}, time.Time{}, &ProviderError{Refused: true, Msg: "the AWS access key is not set"}
	}
	f := url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleArn": {k.RoleARN},
		"RoleSessionName": {firstNonEmpty(k.SessionName, "ledgerline")}, "DurationSeconds": {"900"}}
	if k.ExternalID != "" {
		f.Set("ExternalId", k.ExternalID)
	}
	body := []byte(f.Encode())
	endpoint := firstNonEmpty(k.STSEndpoint, "https://sts."+k.Region+".amazonaws.com/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return AWSCredentials{}, time.Time{}, &ProviderError{Refused: true, Msg: "the STS address is not valid"}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	SignV4(req, body, k.Creds, k.Region, "sts", k.now())
	resp, err := k.client().Do(req)
	if err != nil {
		return AWSCredentials{}, time.Time{}, &ProviderError{Msg: "AWS STS could not be reached: " + netReason(err)}
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct{ Code, Message string } `xml:"Error"`
		}
		_ = xml.Unmarshal(rb, &e)
		return AWSCredentials{}, time.Time{}, &ProviderError{Refused: resp.StatusCode < 500, Code: e.Error.Code,
			Msg: "assuming the firm's role: " + firstNonEmpty(trimMsg(e.Error.Message), fmt.Sprintf("AWS STS answered %d", resp.StatusCode))}
	}
	var r struct {
		Creds struct {
			AccessKeyID     string `xml:"AccessKeyId"`
			SecretAccessKey string `xml:"SecretAccessKey"`
			SessionToken    string `xml:"SessionToken"`
			Expiration      string `xml:"Expiration"`
		} `xml:"AssumeRoleResult>Credentials"`
	}
	if err := xml.Unmarshal(rb, &r); err != nil || r.Creds.AccessKeyID == "" {
		return AWSCredentials{}, time.Time{}, &ProviderError{Msg: "AWS STS answered in an unexpected form"}
	}
	until, err := time.Parse(time.RFC3339, r.Creds.Expiration)
	if err != nil {
		until = k.now().Add(10 * time.Minute)
	}
	return AWSCredentials{AccessKeyID: r.Creds.AccessKeyID, SecretAccessKey: r.Creds.SecretAccessKey, SessionToken: r.Creds.SessionToken}, until, nil
}

// ---- AWS Signature Version 4 ----

// SignV4 signs req (whose body is body) for service in region at t, as AWS
// Signature Version 4 describes: a canonical request of the method, path,
// query, the headers signed (host, content-type and every x-amz- header),
// and the hash of the body; a string to sign with the date and credential
// scope; and an HMAC-SHA256 signature with a key derived from the secret,
// date, region and service. It sets X-Amz-Date, X-Amz-Security-Token (for
// temporary credentials) and Authorization.
func SignV4(req *http.Request, body []byte, c AWSCredentials, region, service string, t time.Time) {
	amz := t.UTC().Format("20060102T150405Z")
	day := amz[:8]
	req.Header.Set("X-Amz-Date", amz)
	if c.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.SessionToken)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	vals := map[string]string{"host": host}
	names := []string{"host"}
	for k, v := range req.Header {
		lk := strings.ToLower(k)
		if lk == "content-type" || strings.HasPrefix(lk, "x-amz-") {
			names = append(names, lk)
			vals[lk] = strings.Join(strings.Fields(strings.Join(v, ",")), " ")
		}
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n + ":" + vals[n] + "\n")
	}
	signed := strings.Join(names, ";")
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	creq := strings.Join([]string{req.Method, path, canonicalQuery(req.URL.Query()), ch.String(), signed, hexSHA(body)}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + amz + "\n" + scope + "\n" + hexSHA([]byte(creq))
	sig := hex.EncodeToString(hmacSHA(SigningKeyV4(c.SecretAccessKey, day, region, service), sts))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.AccessKeyID+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

// SigningKeyV4 derives the Signature Version 4 signing key.
func SigningKeyV4(secret, day, region, service string) []byte {
	k := hmacSHA([]byte("AWS4"+secret), day)
	k = hmacSHA(k, region)
	k = hmacSHA(k, service)
	return hmacSHA(k, "aws4_request")
}

func hmacSHA(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

func hexSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func canonicalQuery(q url.Values) string {
	var parts []string
	for k, vs := range q {
		for _, v := range vs {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

// awsEscape is URI encoding as SigV4 wants it: every byte but the
// unreserved characters, with upper-case hex.
func awsEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
