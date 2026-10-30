package ops

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
)

// ---- CDSL APIs: the one connection and the shared HTTP helper ----
//
// CDSL offers its DPs a set of APIs (CDSL API Handbook §5). They all go to
// one gateway, mockapigt.cdsl.co.in for testing and apigt.cdsl.co.in live,
// from a server address CDSL has listed (§2, §4). There are two kinds:
//
//   - "Commonweb" APIs (BO setup, BO modify and common uploads, status and
//     response download; §5.4–5.7): a separate CDAS API user signs in with
//     its user ID and password (the password AES-encrypted with the user's
//     secret key) to get an AuthToken; every call then carries the token
//     and an AuthSign header: the SHA-256 of the request body, AES-encrypted
//     with the same secret key. Files carry FileComputeHash, the SHA-256 of
//     the file.
//   - Redirect and form APIs (account transfer, add beneficiary, active
//     demat count, eDIS, pledges; §5.1–5.3, §5.11 on): a form posted with
//     the DP ID, a request ID and one field of JSON AES-256 encrypted (CBC,
//     PKCS#7) with the API key CDSL issues; answers come back encrypted
//     with the same key.
//
// One connection, "cdsl-api", holds the DP ID, the environment, the API
// user, the secrets and one switch per API family. Files in this package
// for the other CDSL APIs read it by ID; when it is missing they behave as
// "not set".
//
// Values keys: dp_id, env (Mock or Live), user, url (an address used in
// place of CDSL's, for a proxy), hash_format, callback_url, and the family
// switches cdslFam*. Secrets: password (the API user's), secret (the API
// user's secret key), api_key (the API key for the redirect and form APIs;
// the secret key is used when it is empty) and iv (an initialisation
// vector, only if CDSL gives one; zero bytes otherwise).
//
// Choices where the handbook is not explicit (each can be changed here
// once CDSL's mock confirms):
//   - "RijndaelManaged" and "AES 256" both read as AES-CBC with PKCS#7
//     padding, and "IV factor should be null" as sixteen zero bytes.
//   - The key is the secret as given when it is 16, 24 or 32 characters,
//     else its base64 or hex decoding of that length.
//   - The SHA-256 that AuthSign encrypts is written as lowercase hex by
//     default; uppercase hex, base64 and the raw 32 bytes are options.
//   - Ciphertext is written in base64, as every sample in the handbook is.
//   - The handbook's encrypted samples are placeholders (the same text
//     appears for different data and no key is given), so they cannot be
//     used as test vectors; the tests check against OpenSSL's output.

const (
	connCDSL     = "cdsl-api"
	cdslMockBase = "https://mockapigt.cdsl.co.in"
	cdslLiveBase = "https://apigt.cdsl.co.in"
	cdslEnvMock  = "Mock"
	cdslEnvLive  = "Live"

	cdslPathAuth     = "AuthWeb/api/CDASAuthorization/APIAuth" // §5.7
	cdslPathStatus   = "Commonweb/api/CDASAPI/Status"          // §5.5–5.7 upload status
	cdslPathDownload = "Commonweb/api/CDASAPI/Download"        // §5.5–5.7 response file

	cdslHashHexLower = "hex lower"
	cdslHashHexUpper = "hex upper"
	cdslHashBase64   = "base64"
	cdslHashRaw      = "raw bytes"
)

// API families, as the handbook's §5 table groups them: one switch each
// (values key, On or Off).
const (
	cdslFamTransfer    = "api_transfer"     // §5.1 account transfer cum closure
	cdslFamBeneficiary = "api_beneficiary"  // §5.2 add beneficiary (off-market)
	cdslFamBOCount     = "api_bocount"      // §5.3 BO active demat account
	cdslFamBO          = "api_bo"           // §5.4–5.6 BO online, setup and modify
	cdslFamUpload      = "api_upload"       // §5.7 common upload
	cdslFamDestat      = "api_destat"       // §5.8 DESTAT
	cdslFamEarlyPayin  = "api_earlypayin"   // §5.9–5.10
	cdslFamEDIS        = "api_edis"         // §5.11–5.15
	cdslFamMargin      = "api_marginpledge" // §5.16–5.18
	cdslFamPledge      = "api_pledge"       // §5.19–5.20
	cdslFamReport      = "api_report"       // §5.21 SFTP report setup
)

// cdslFamilies lists the switches in the order the Connections page shows.
var cdslFamilies = []struct{ Key, Label string }{
	{cdslFamBO, "BO online, BO setup and BO modify (§5.4–5.6)"},
	{cdslFamBOCount, "BO active demat account count (§5.3)"},
	{cdslFamTransfer, "Account transfer cum closure (§5.1)"},
	{cdslFamBeneficiary, "Add beneficiary for off-market (§5.2)"},
	{cdslFamUpload, "Common (transaction) upload (§5.7)"},
	{cdslFamDestat, "DESTAT (§5.8)"},
	{cdslFamEarlyPayin, "Early pay-in (§5.9–5.10)"},
	{cdslFamEDIS, "eDIS (§5.11–5.15)"},
	{cdslFamMargin, "Margin pledge and re-pledge (§5.16–5.18)"},
	{cdslFamPledge, "Normal pledge, unpledge and confiscate (§5.19–5.20)"},
	{cdslFamReport, "SFTP report setup (§5.21)"},
}

func init() {
	f := []ConnField{
		{Key: "dp_id", Label: "DP ID (as CDSL's API forms take it, e.g. 10900)"},
		{Key: "env", Label: "Environment", Options: []string{cdslEnvMock, cdslEnvLive}},
		{Key: "user", Label: "API user ID (the separate CDAS user created for the APIs)", Optional: true},
		{Key: "password", Label: "API user's password", Secret: true, Optional: true},
		{Key: "secret", Label: "API user's secret key (given when the user was created)", Secret: true, Optional: true},
		{Key: "api_key", Label: "API key for eDIS, pledge, transfer and beneficiary APIs (leave empty if the same as the secret key)", Secret: true, Optional: true},
		{Key: "iv", Label: "Initialisation vector (only if CDSL gave one)", Secret: true, Optional: true},
		{Key: "hash_format", Label: "How AuthSign writes the SHA-256 before encrypting", Options: []string{cdslHashHexLower, cdslHashHexUpper, cdslHashBase64, cdslHashRaw}, Optional: true},
		{Key: "callback_url", Label: "Ledgerline's public address for CDSL's answers (leave empty for the server's)", Optional: true, Placeholder: "https://backoffice.firm.in"},
	}
	for _, fam := range cdslFamilies {
		f = append(f, ConnField{Key: fam.Key, Label: "Use: " + fam.Label, Options: []string{"On", "Off"}, Optional: true})
	}
	f = append(f, ConnField{Key: "url", Label: "Address (leave empty for CDSL's gateway)", Optional: true, Placeholder: cdslLiveBase}, fNotes)
	ConnProviders = append(ConnProviders, ConnProvider{ID: connCDSL, Name: "CDSL APIs (DP)", Group: "Depository", Test: "https",
		Purpose: "CDSL's APIs for DPs: open and modify demat accounts, account transfer and closure, off-market beneficiaries, eDIS, pledges, uploads and reports. Ask CDSL's helpdesk for the mock details first; CDSL must list this server's address before any call works. Turn on each API family once it has passed CDSL's mock testing",
		Fields:  f})
}

// cdslConn is the cdsl-api connection, ready to use.
type cdslConn struct {
	DPID, Env, User, Base, HashFormat, Callback string
	c                                           Connection
}

// cdslConnFor reads the connection. ok is false when it is not set.
func (o *Ops) cdslConnFor(ctx context.Context, tenant string) (cdslConn, bool) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connCDSL)
	if err != nil || c.Status == "Not set" || c.Values["dp_id"] == "" {
		return cdslConn{}, false
	}
	x := cdslConn{c: c, DPID: c.Values["dp_id"], Env: firstOf(c.Values["env"], cdslEnvMock), User: c.Values["user"], HashFormat: firstOf(c.Values["hash_format"], cdslHashHexLower)}
	x.Base = cdslMockBase
	if x.Env == cdslEnvLive {
		x.Base = cdslLiveBase
	}
	if u := strings.TrimRight(c.Values["url"], "/"); u != "" {
		x.Base = u
	}
	x.Callback = strings.TrimRight(firstOf(c.Values["callback_url"], os.Getenv("LEDGERLINE_PUBLIC_URL")), "/")
	return x, true
}

// On says whether an API family is switched on.
func (x cdslConn) On(fam string) bool { return x.c.Values[fam] == "On" }

// cdslFamilyOn says whether a family is on for a firm (false when the
// connection is not set).
func (o *Ops) cdslFamilyOn(ctx context.Context, tenant, fam string) bool {
	x, ok := o.cdslConnFor(ctx, tenant)
	return ok && x.On(fam)
}

// URL is an API's address: a path under the gateway, or a full address.
func (x cdslConn) URL(path string) string {
	if strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "http://") {
		return path
	}
	return x.Base + "/" + strings.TrimLeft(path, "/")
}

// apiKey is the key for the redirect and form APIs.
func (x cdslConn) apiKey() ([]byte, error) {
	return cdslAESKey(firstOf(x.c.Secrets["api_key"], x.c.Secrets["secret"]), "API key")
}

// secretKey is the API user's secret key (Commonweb APIs).
func (x cdslConn) secretKey() ([]byte, error) {
	return cdslAESKey(x.c.Secrets["secret"], "secret key")
}

func (x cdslConn) iv() ([]byte, error) { return cdslIV(x.c.Secrets["iv"]) }

// cdslAESKey turns a key as CDSL gives it into AES key bytes.
func cdslAESKey(s, what string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, invalid("the CDSL APIs connection has no %s: enter it in Settings → Connections", what)
	}
	ok := func(n int) bool { return n == 16 || n == 24 || n == 32 }
	if ok(len(s)) {
		return []byte(s), nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && ok(len(b)) {
		return b, nil
	}
	if b, err := hex.DecodeString(s); err == nil && ok(len(b)) {
		return b, nil
	}
	return nil, invalid("the CDSL %s is not an AES key: it should be 32 characters (or 32 bytes in base64 or hex)", what)
}

func cdslIV(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return make([]byte, aes.BlockSize), nil
	}
	if len(s) == aes.BlockSize {
		return []byte(s), nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == aes.BlockSize {
		return b, nil
	}
	if b, err := hex.DecodeString(s); err == nil && len(b) == aes.BlockSize {
		return b, nil
	}
	return nil, invalid("the CDSL initialisation vector should be 16 characters (or 16 bytes in base64 or hex)")
}

// cdslEncrypt is AES-CBC with PKCS#7 padding, in base64.
func cdslEncrypt(key, iv, plain []byte) (string, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	n := aes.BlockSize - len(plain)%aes.BlockSize
	p := append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(n)}, n)...)
	out := make([]byte, len(p))
	cipher.NewCBCEncrypter(blk, iv).CryptBlocks(out, p)
	return base64.StdEncoding.EncodeToString(out), nil
}

var errCDSLDecrypt = errors.New("CDSL's answer could not be decrypted with the key in Connections")

// cdslDecrypt reverses cdslEncrypt. Spaces and line breaks in the base64
// (as form posts and the handbook's samples have) are ignored.
func cdslDecrypt(key, iv []byte, b64 string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, b64)
	ct, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		if ct, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(clean, "=")); err != nil {
			return nil, errCDSLDecrypt
		}
	}
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return nil, errCDSLDecrypt
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ct))
	cipher.NewCBCDecrypter(blk, iv).CryptBlocks(out, ct)
	n := int(out[len(out)-1])
	if n == 0 || n > aes.BlockSize || n > len(out) {
		return nil, errCDSLDecrypt
	}
	for _, c := range out[len(out)-n:] {
		if int(c) != n {
			return nil, errCDSLDecrypt
		}
	}
	return out[:len(out)-n], nil
}

// seal encrypts JSON with the API key (redirect and form APIs).
func (x cdslConn) seal(v any) (string, error) {
	key, err := x.apiKey()
	if err != nil {
		return "", err
	}
	iv, err := x.iv()
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return cdslEncrypt(key, iv, b)
}

// open decrypts an answer from a redirect or form API into v.
func (x cdslConn) open(s string, v any) error {
	key, err := x.apiKey()
	if err != nil {
		return err
	}
	iv, err := x.iv()
	if err != nil {
		return err
	}
	b, err := cdslDecrypt(key, iv, s)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(cdslCleanJSON(b), v); err != nil {
		return fmt.Errorf("CDSL's answer is not the JSON expected: %v", err)
	}
	return nil
}

// cdslCleanJSON trims what some answers carry around the JSON (a byte order
// mark, NULs).
func cdslCleanJSON(b []byte) []byte {
	b = bytes.TrimPrefix(bytes.TrimSpace(b), []byte("\xef\xbb\xbf"))
	return bytes.Trim(b, "\x00 \r\n\t")
}

// cdslHash is the SHA-256 of b as the AuthSign scheme writes it.
func cdslHash(b []byte, format string) []byte {
	sum := sha256.Sum256(b)
	switch format {
	case cdslHashHexUpper:
		return []byte(strings.ToUpper(hex.EncodeToString(sum[:])))
	case cdslHashBase64:
		return []byte(base64.StdEncoding.EncodeToString(sum[:]))
	case cdslHashRaw:
		return sum[:]
	}
	return []byte(hex.EncodeToString(sum[:]))
}

// AuthSign is the AuthSign header for a Commonweb request body.
func (x cdslConn) AuthSign(body []byte) (string, error) {
	key, err := x.secretKey()
	if err != nil {
		return "", err
	}
	iv, err := x.iv()
	if err != nil {
		return "", err
	}
	return cdslEncrypt(key, iv, cdslHash(body, x.HashFormat))
}

// FileHash is the FileComputeHash (or BOSignComputeHash) header for a file.
func (x cdslConn) FileHash(b []byte) string {
	f := x.HashFormat
	if f == cdslHashRaw {
		f = cdslHashHexLower // a header cannot carry raw bytes
	}
	return string(cdslHash(b, f))
}

// cdslMaskBO keeps the last four digits of a BO ID, for logs.
func cdslMaskBO(id string) string {
	if len(id) <= 4 {
		return id
	}
	return strings.Repeat("•", len(id)-4) + id[len(id)-4:]
}

// ---- HTTP ----

// cdslHTTP talks to CDSL; tests replace it.
var cdslHTTP = &http.Client{Timeout: 60 * time.Second}

// cdslFile is a file sent in a multipart request.
type cdslFile struct {
	Field, Name string
	Data        []byte
}

// cdslRequest is one call to a CDSL API.
type cdslRequest struct {
	Path   string            // under the gateway, or a full address
	Header map[string]string // set exactly as written (CDSL's names are not canonical)
	JSON   []byte            // a JSON body; or
	Form   url.Values        // form fields (with Files: multipart, else urlencoded)
	Files  []cdslFile
}

// cdslResponse is CDSL's answer.
type cdslResponse struct {
	Status int
	Body   []byte
}

// do makes one call. Errors are in words a DP's staff can act on.
func (x cdslConn) do(ctx context.Context, r cdslRequest) (cdslResponse, error) {
	var body io.Reader
	ctype := ""
	switch {
	case len(r.Files) > 0:
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, vs := range r.Form {
			for _, v := range vs {
				_ = mw.WriteField(k, v)
			}
		}
		for _, f := range r.Files {
			w, err := mw.CreateFormFile(f.Field, f.Name)
			if err != nil {
				return cdslResponse{}, err
			}
			w.Write(f.Data)
		}
		mw.Close()
		body, ctype = &buf, mw.FormDataContentType()
	case r.Form != nil:
		body, ctype = strings.NewReader(r.Form.Encode()), "application/x-www-form-urlencoded"
	default:
		body, ctype = bytes.NewReader(r.JSON), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.URL(r.Path), body)
	if err != nil {
		return cdslResponse{}, err
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Accept", "application/json")
	for k, v := range r.Header {
		req.Header[k] = []string{v}
	}
	resp, err := cdslHTTP.Do(req)
	if err != nil {
		return cdslResponse{}, cdslNetError(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return cdslResponse{}, fmt.Errorf("CDSL's answer was cut off: %v", err)
	}
	out := cdslResponse{Status: resp.StatusCode, Body: b}
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return out, fmt.Errorf("CDSL refused the call (%d): the server's address may not be listed with CDSL, or the API user is not enabled for this API", resp.StatusCode)
	case resp.StatusCode == 404:
		return out, fmt.Errorf("CDSL has no API at %s (404): check the environment and address in Connections", r.Path)
	case resp.StatusCode >= 500:
		return out, fmt.Errorf("CDSL's server answered with an error (%d); try again later", resp.StatusCode)
	}
	return out, nil
}

func cdslNetError(err error) error {
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr):
		return errors.New("CDSL's address could not be found: check the address in Connections")
	case strings.Contains(err.Error(), "certificate"):
		return errors.New("CDSL's certificate was not accepted")
	case strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline"):
		return errors.New("CDSL did not answer in time: CDSL may not have listed this server's address yet")
	}
	return fmt.Errorf("could not reach CDSL: %v", err)
}

// cdslBody reads a Commonweb answer, which may be wrapped in "Body".
func cdslBody(b []byte) map[string]any {
	var m map[string]any
	if json.Unmarshal(cdslCleanJSON(b), &m) != nil {
		var s string // a JSON string holding JSON
		if json.Unmarshal(cdslCleanJSON(b), &s) == nil {
			json.Unmarshal([]byte(s), &m)
		}
	}
	if m == nil {
		return map[string]any{}
	}
	for _, k := range []string{"Body", "body"} {
		if in, ok := m[k].(map[string]any); ok {
			return in
		}
	}
	return m
}

// cdslStr reads a field from an answer, whatever its case and type.
func cdslStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		for mk, v := range m {
			if strings.EqualFold(strings.TrimSpace(mk), k) && v != nil {
				switch t := v.(type) {
				case string:
					return strings.TrimSpace(t)
				case bool:
					if t {
						return "true"
					}
					return "false"
				case float64:
					return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
				}
			}
		}
	}
	return ""
}

// cdslError reads ErrorId and ErrorMessage from an answer's error object
// (AuthResponse, UploadResponse, Response…).
func cdslError(m map[string]any) (string, string) {
	if id := cdslStr(m, "ErrorId"); id != "" {
		return id, cdslStr(m, "ErrorMessage")
	}
	for _, v := range m {
		if in, ok := v.(map[string]any); ok {
			if id := cdslStr(in, "ErrorId"); id != "" || cdslStr(in, "ErrorMessage") != "" {
				return id, cdslStr(in, "ErrorMessage")
			}
		}
	}
	return "", ""
}

// ---- Commonweb sign-in (§5.7 Authentication API) ----

type cdslTok struct {
	token string
	at    time.Time
}

var (
	cdslTokMu sync.Mutex
	cdslToks  = map[string]cdslTok{}
)

// cdslTokenTTL is how long a token is reused. CDSL does not say how long
// it lasts; an answer refusing it signs in again.
const cdslTokenTTL = 15 * time.Minute

// cdslJSON writes fields in the given order (the AuthSign hash is over
// the exact bytes sent).
func cdslJSON(kv ...string) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(kv[i])
		v, _ := json.Marshal(kv[i+1])
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// Token signs the API user in, or reuses a recent token.
func (x cdslConn) Token(ctx context.Context, tenant string, fresh bool) (string, error) {
	if x.User == "" || x.c.Secrets["password"] == "" {
		return "", invalid("the CDSL APIs connection needs the API user ID and password")
	}
	ck := tenant + "|" + x.Base + "|" + x.User
	cdslTokMu.Lock()
	t, ok := cdslToks[ck]
	cdslTokMu.Unlock()
	if ok && !fresh && time.Since(t.at) < cdslTokenTTL {
		return t.token, nil
	}
	key, err := x.secretKey()
	if err != nil {
		return "", err
	}
	iv, err := x.iv()
	if err != nil {
		return "", err
	}
	pw, err := cdslEncrypt(key, iv, []byte(x.c.Secrets["password"]))
	if err != nil {
		return "", err
	}
	body := cdslJSON("DPID", x.DPID, "UserID", x.User, "Password", pw)
	sign, err := x.AuthSign(body)
	if err != nil {
		return "", err
	}
	resp, err := x.do(ctx, cdslRequest{Path: cdslPathAuth, JSON: body, Header: map[string]string{"AuthSign": sign}})
	if err != nil {
		return "", err
	}
	m := cdslBody(resp.Body)
	tok := cdslStr(m, "AuthToken", "token")
	if tok == "" || cdslStr(m, "success") == "false" {
		id, msg := cdslError(m)
		return "", fmt.Errorf("CDSL did not sign the API user in: %s", cdslExplain(id, firstOf(msg, "no token in the answer")))
	}
	cdslTokMu.Lock()
	cdslToks[ck] = cdslTok{token: tok, at: time.Now()}
	cdslTokMu.Unlock()
	return tok, nil
}

// Common makes a signed Commonweb call: AuthToken and AuthSign over body
// (the JSON as sent), plus a file when given. Form fields, when given, go
// in a multipart request with the file; the AuthSign is still over body.
func (x cdslConn) Common(ctx context.Context, tenant, path string, body []byte, form url.Values, file *cdslFile, extra map[string]string) (map[string]any, error) {
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := x.Token(ctx, tenant, attempt > 0)
		if err != nil {
			return nil, err
		}
		sign, err := x.AuthSign(body)
		if err != nil {
			return nil, err
		}
		h := map[string]string{"AuthToken": tok, "AuthSign": sign}
		for k, v := range extra {
			h[k] = v
		}
		r := cdslRequest{Path: path, Header: h, JSON: body}
		if file != nil {
			h["FileComputeHash"] = x.FileHash(file.Data)
			r.Form, r.Files = form, []cdslFile{*file}
		} else if form != nil {
			r.Form = form
		}
		resp, err := x.do(ctx, r)
		if resp.Status == 401 && attempt == 0 {
			continue // the token may have expired: sign in again
		}
		if err != nil {
			return nil, err
		}
		m := cdslBody(resp.Body)
		if attempt == 0 && cdslTokenError(m) {
			continue
		}
		return m, nil
	}
	return nil, errors.New("CDSL refused the API user's token twice: check the user ID, password and secret key")
}

func cdslTokenError(m map[string]any) bool {
	_, msg := cdslError(m)
	return strings.Contains(strings.ToLower(msg), "token")
}

// cdslExplain puts CDSL's error in words, keeping its code.
func cdslExplain(id, msg string) string {
	msg = strings.TrimSpace(msg)
	switch {
	case id == "" && msg == "":
		return "no reason given"
	case id == "":
		return msg
	case msg == "":
		return id
	}
	return msg + " (" + id + ")"
}
