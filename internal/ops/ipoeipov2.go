package ops

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
)

// ---- NSE eIPO advanced security (v2) ----
//
// Protocol for WEB API, Initial Public Offer System 2.0.0, "API Security",
// "Advance Security (v2)". Each side has a 2048-bit RSA key pair: NSE makes
// one for each member and gives the member its public key; the member makes
// its own and gives NSE the public key. Every API call then goes under /v2:
//
//   - Encryption: a POST body is encrypted with a fresh AES-256 key
//     (AES/GCM/NoPadding) and base64 encoded; that is the body sent. The AES
//     key is encrypted with the other side's public key
//     (RSA/NONE/OAEPWithSHA1AndMGF1Padding) and base64 encoded into header
//     key1. Replies are encrypted the same way, to the caller's public key.
//   - Integrity: header Sig is base64(SHA-256(the encrypted, encoded body));
//     a GET has no body, so Sig is a random string of at most 50
//     characters. Replies carry Sig too.
//   - Authentication: the caller sends headers member and loginId (NSE
//     sends "NSE" in both) and key3, base64 of Sig "encrypted" with the
//     caller's own private key; the receiver "decrypts" it with the caller's
//     public key and compares it with Sig. Replies carry no key3, member or
//     loginId.
//
// Readings Ledgerline takes where the protocol is silent (questions for
// NSE, listed in the help article's notes):
//
//   - GCM IV: not given. Ledgerline uses a random 12-byte IV per message,
//     sent in front of the ciphertext (IV || ciphertext || 16-byte tag, then
//     base64), with no additional authenticated data. Change eipoGCMIV and
//     eipoAESSeal/eipoAESOpen together if NSE says otherwise.
//   - What key1 holds: the 32 raw AES key bytes (not base64 or hex text).
//   - key3: the protocol says to encrypt the digest with the private key
//     using the RSA transformation in its table, which is OAEP. That is
//     what BouncyCastle's "RSA/NONE/OAEPWithSHA1AndMGF1Padding" does when
//     given a private key: OAEP-encode, then raise to the private exponent.
//     Go's crypto/rsa has no such call, so eipoRSAPrivEncrypt and
//     eipoRSAPubDecrypt below do the RSA steps themselves (with blinding).
//     A standard RSA signature (SHA256withRSA) would be the other reading.
//   - The digest header is named Sig; the table's key3 formula calls it
//     "ipo-key-2", so a reply's key2 header is read when it has no Sig.
//   - A GET also carries a key1 (the table marks key1 required in every
//     request), with a fresh AES key that encrypts nothing.
//   - Login: v2 authenticates every call by its signature and the
//     protocol's list of v2 APIs has no login, so a v2 call carries no
//     token and Ledgerline does not log in for it.
//   - APIs not in that list (login, heartbeat, notifications, mismatches,
//     the allotment report, NCB, tender, SGB, the holiday master) are not
//     offered in v2: on a v2 connection they go by basic security (v3,
//     Bearer token), which NSE hosts for every member, so the password is
//     still needed.
//   - Replies: key3 is not sent by NSE, so a reply is checked by Sig and by
//     decrypting it (GCM fails on any change); if NSE does add key3, it is
//     checked against NSE's public key too.
//   - Callbacks: the protocol's authentication section covers "the entity
//     (Exchange or Member/Bank) which is invoking an API" and names NSE's
//     identifier, so NSE's callbacks to a v2 member are taken as sealed and
//     signed the same way, and Ledgerline's reply is sealed for NSE. The
//     callbacks' Authorization header is still checked when NSE sends it.
//   - The encrypted body is sent with Content-Type application/json, as the
//     general instructions ask of all calls; a reply body that is a JSON
//     string ("...") is read as the base64 inside it.

const (
	eipoV2 = "v2 (advanced security: RSA and AES)"

	eipoGCMIV    = 12 // bytes of IV in front of each GCM ciphertext
	eipoRSABits  = 2048
	eipoNSEIdent = "NSE" // NSE's member and loginId when it calls
)

// eipoV2Covers says whether an API is in the protocol's list of APIs that
// support advanced security: the IPO master, transactions (add, addbulk, by
// time, slice, fetch), allotment by time and fetch (not the allotment
// report), and eForms add and query. p is the path after the version.
func eipoV2Covers(p string) bool {
	p, _, _ = strings.Cut(p, "?")
	switch {
	case p == "/ipomaster", p == "/eforms/add", p == "/eforms/query":
		return true
	case strings.HasPrefix(p, "/transactions/"):
		return true
	case strings.HasPrefix(p, "/allotment/"):
		return !strings.HasPrefix(p, "/allotment/report/")
	}
	return false
}

// callV2 sends one request with advanced security: the body sealed for
// NSE, signed with the firm's key; the reply checked and opened. A reply
// that fails the check is an error, and nothing in it is returned.
func (h nseEIPOHTTP) callV2(ctx context.Context, c EIPOConfig, method, path string, body any) ([]byte, error) {
	if c.MemberKey == nil || c.NSEKey == nil {
		return nil, fmt.Errorf("the NSE eIPO connection is set to v2 but its keys are missing: enter the firm's RSA private key and NSE's public key in Connections")
	}
	withBody := method != http.MethodGet
	var plain []byte
	if withBody {
		plain = []byte("{}")
		if body != nil {
			plain, _ = json.Marshal(body)
		}
	}
	m, err := eipoSeal(c.NSEKey, c.MemberKey, plain, withBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(m.Body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Ledgerline")
	req.Header.Set("member", c.Member)
	req.Header.Set("loginId", c.Login)
	m.set(req.Header)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach NSE's eIPO: %v", err)
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.Header.Get("Key1") == "" && resp.StatusCode >= 300 {
		// An error NSE did not encrypt: shown, never used as data.
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return nil, fmt.Errorf("NSE's eIPO refused the v2 request (%d): check that NSE has the firm's public key, that NSE's public key here is the one NSE gave, and the member code and user ID: %s", resp.StatusCode, trimBody(out))
		}
		return nil, fmt.Errorf("NSE's eIPO answered %d: %s", resp.StatusCode, trimBody(out))
	}
	reply, err := eipoOpen(c.MemberKey, c.NSEKey, resp.Header, out, true, false)
	if err != nil {
		return nil, fmt.Errorf("NSE's eIPO reply failed the v2 security check (%v): nothing in it was used", err)
	}
	if resp.StatusCode == 403 {
		return nil, fmt.Errorf("NSE's eIPO refused the request (403): a field is longer or of another type than the protocol allows, or the rate limit was reached: %s", trimBody(reply))
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("NSE's eIPO answered %d: %s", resp.StatusCode, trimBody(reply))
	}
	return reply, nil
}

// eipoV2Msg is a sealed message: the body as it goes and its headers.
type eipoV2Msg struct {
	Body            []byte
	Key1, Sig, Key3 string
}

// set puts the message's headers on a request or reply.
func (m eipoV2Msg) set(h http.Header) {
	h.Set("Key1", m.Key1)
	h.Set("Sig", m.Sig)
	if m.Key3 != "" {
		h.Set("Key3", m.Key3)
	}
}

// eipoSeal seals a message for the party whose public key is to: a fresh
// AES-256 key encrypts the body (when it has one) and goes in key1, Sig is
// the digest of the encrypted body (or a random string), and key3 signs
// Sig when signer is given (requests; replies are not signed).
func eipoSeal(to *rsa.PublicKey, signer *rsa.PrivateKey, plain []byte, withBody bool) (eipoV2Msg, error) {
	var m eipoV2Msg
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return m, err
	}
	k1, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, to, key, nil)
	if err != nil {
		return m, fmt.Errorf("NSE's public key cannot encrypt the AES key: %v", err)
	}
	m.Key1 = base64.StdEncoding.EncodeToString(k1)
	if withBody {
		ct, err := eipoAESSeal(key, plain)
		if err != nil {
			return m, err
		}
		m.Body = []byte(base64.StdEncoding.EncodeToString(ct))
		m.Sig = eipoDigest(m.Body)
	} else {
		m.Sig = randomAlnum(32) // the protocol: at most 50 characters
	}
	if signer != nil {
		s, err := eipoRSAPrivEncrypt(signer, []byte(m.Sig))
		if err != nil {
			return m, err
		}
		m.Key3 = base64.StdEncoding.EncodeToString(s)
	}
	return m, nil
}

// eipoOpen checks and opens a sealed message: own is the receiver's
// private key (for key1), sender the other side's public key (for key3).
// The signature and digest are checked before anything is decrypted.
// needSig is true for requests (key3 required); for replies key3 is
// checked only if present.
func eipoOpen(own *rsa.PrivateKey, sender *rsa.PublicKey, h http.Header, body []byte, withBody, needSig bool) ([]byte, error) {
	sig := strings.TrimSpace(h.Get("Sig"))
	if k2 := strings.TrimSpace(h.Get("Key2")); sig == "" {
		sig = k2
	} else if k2 != "" && k2 != sig {
		return nil, errors.New("the Sig and key2 headers differ")
	}
	if sig == "" {
		return nil, errors.New("no Sig header")
	}
	if k3 := strings.TrimSpace(h.Get("Key3")); k3 != "" || needSig {
		if k3 == "" {
			return nil, errors.New("no signature (key3 header)")
		}
		raw, err := base64.StdEncoding.DecodeString(k3)
		if err != nil {
			return nil, errors.New("the signature (key3) is not base64")
		}
		got, err := eipoRSAPubDecrypt(sender, raw)
		if err != nil || subtle.ConstantTimeCompare(got, []byte(sig)) != 1 {
			return nil, errors.New("the signature (key3) does not match: it was not made with the key whose public key is held here, or Sig was changed")
		}
	}
	if !withBody {
		return nil, nil
	}
	if !hmac.Equal([]byte(eipoDigest(body)), []byte(sig)) && !hmac.Equal([]byte(eipoDigest(bytes.TrimSpace(body))), []byte(sig)) {
		return nil, errors.New("the body does not match its digest (Sig): it was changed on the way")
	}
	k1, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h.Get("Key1")))
	if err != nil || len(k1) == 0 {
		return nil, errors.New("no AES key (key1 header)")
	}
	key, err := rsa.DecryptOAEP(sha1.New(), nil, own, k1, nil)
	if err != nil || len(key) != 32 {
		return nil, errors.New("the AES key (key1) cannot be decrypted with the firm's private key: it was encrypted for another key")
	}
	text := bytes.TrimSpace(body)
	if len(text) > 0 && text[0] == '"' {
		var s string
		if json.Unmarshal(text, &s) == nil {
			text = []byte(s)
		}
	}
	ct, err := base64.StdEncoding.DecodeString(string(text))
	if err != nil {
		return nil, errors.New("the body is not base64")
	}
	plain, err := eipoAESOpen(key, ct)
	if err != nil {
		return nil, errors.New("the body cannot be decrypted (AES-GCM check failed)")
	}
	return plain, nil
}

// eipoDigest is Sig for a body: base64(SHA-256(body)).
func eipoDigest(b []byte) string {
	s := sha256.Sum256(b)
	return base64.StdEncoding.EncodeToString(s[:])
}

// eipoAESSeal is AES-256-GCM with a random IV in front of the ciphertext.
func eipoAESSeal(key, plain []byte) ([]byte, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCMWithNonceSize(blk, eipoGCMIV)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, eipoGCMIV, eipoGCMIV+len(plain)+g.Overhead())
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	return g.Seal(iv, iv, plain, nil), nil
}

func eipoAESOpen(key, ct []byte) ([]byte, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCMWithNonceSize(blk, eipoGCMIV)
	if err != nil {
		return nil, err
	}
	if len(ct) < eipoGCMIV+g.Overhead() {
		return nil, errors.New("too short")
	}
	return g.Open(nil, ct[:eipoGCMIV], ct[eipoGCMIV:], nil)
}

// eipoRSAPrivEncrypt "encrypts" with a private key as BouncyCastle's
// RSA/NONE/OAEPWithSHA1AndMGF1Padding does: EME-OAEP encoding (SHA-1,
// MGF1-SHA-1, empty label; RFC 8017 section 7.1.1), then the private-key
// operation. The operation is blinded, and its result checked with the
// public exponent before it is used.
func eipoRSAPrivEncrypt(k *rsa.PrivateKey, msg []byte) ([]byte, error) {
	size := (k.N.BitLen() + 7) / 8
	em, err := eipoOAEPEncode(msg, size)
	if err != nil {
		return nil, err
	}
	m := new(big.Int).SetBytes(em)
	e := big.NewInt(int64(k.E))
	var r, ri *big.Int
	for ri == nil {
		if r, err = rand.Int(rand.Reader, k.N); err != nil {
			return nil, err
		}
		if r.Sign() > 0 {
			ri = new(big.Int).ModInverse(r, k.N)
		}
	}
	c := new(big.Int).Exp(r, e, k.N)
	c.Mul(c, m).Mod(c, k.N)
	s := new(big.Int).Exp(c, k.D, k.N)
	s.Mul(s, ri).Mod(s, k.N)
	if new(big.Int).Exp(s, e, k.N).Cmp(m) != 0 {
		return nil, errors.New("the firm's private key gave a wrong result: check the key")
	}
	return s.FillBytes(make([]byte, size)), nil
}

// eipoRSAPubDecrypt undoes eipoRSAPrivEncrypt with the public key.
func eipoRSAPubDecrypt(k *rsa.PublicKey, sig []byte) ([]byte, error) {
	size := (k.N.BitLen() + 7) / 8
	if len(sig) == 0 || len(sig) > size {
		return nil, errors.New("wrong length")
	}
	c := new(big.Int).SetBytes(sig)
	if c.Cmp(k.N) >= 0 {
		return nil, errors.New("out of range")
	}
	m := new(big.Int).Exp(c, big.NewInt(int64(k.E)), k.N)
	return eipoOAEPDecode(m.FillBytes(make([]byte, size)))
}

func eipoOAEPEncode(msg []byte, size int) ([]byte, error) {
	hl := sha1.Size
	if len(msg) > size-2*hl-2 {
		return nil, errors.New("message too long for the RSA key")
	}
	em := make([]byte, size)
	seed, db := em[1:1+hl], em[1+hl:]
	lHash := sha1.Sum(nil)
	copy(db, lHash[:])
	db[len(db)-len(msg)-1] = 1
	copy(db[len(db)-len(msg):], msg)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	mgf1XOR(db, seed)
	mgf1XOR(seed, db)
	return em, nil
}

func eipoOAEPDecode(em []byte) ([]byte, error) {
	hl := sha1.Size
	bad := errors.New("not OAEP encoded")
	if len(em) < 2*hl+2 || em[0] != 0 {
		return nil, bad
	}
	seed := append([]byte(nil), em[1:1+hl]...)
	db := append([]byte(nil), em[1+hl:]...)
	mgf1XOR(seed, db)
	mgf1XOR(db, seed)
	lHash := sha1.Sum(nil)
	if !bytes.Equal(db[:hl], lHash[:]) {
		return nil, bad
	}
	rest := db[hl:]
	i := 0
	for i < len(rest) && rest[i] == 0 {
		i++
	}
	if i == len(rest) || rest[i] != 1 {
		return nil, bad
	}
	return rest[i+1:], nil
}

// mgf1XOR xors out with MGF1-SHA-1 of seed.
func mgf1XOR(out, seed []byte) {
	var ctr [4]byte
	done := 0
	for done < len(out) {
		h := sha1.New()
		h.Write(seed)
		h.Write(ctr[:])
		d := h.Sum(nil)
		for i := 0; i < len(d) && done < len(out); i++ {
			out[done] ^= d[i]
			done++
		}
		for i := 3; i >= 0; i-- {
			if ctr[i]++; ctr[i] != 0 {
				break
			}
		}
	}
}

// randomAlnum is a random string of letters and digits.
func randomAlnum(n int) string {
	const set = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		x, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		if err != nil {
			panic(err)
		}
		b[i] = set[x.Int64()]
	}
	return string(b)
}

// ---- the keys ----

// eipoPEMBlocks reads PEM text, also when pasted into a one-line field
// (line breaks lost), or bare base64 of the key.
func eipoPEMBlocks(s string) []*pem.Block {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "-----BEGIN") {
		d, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
		if err != nil || len(d) == 0 {
			return nil
		}
		return []*pem.Block{{Type: "", Bytes: d}}
	}
	rest := []byte(pemLines(s))
	var out []*pem.Block
	for {
		b, r := pem.Decode(rest)
		if b == nil {
			return out
		}
		out = append(out, b)
		rest = r
	}
}

// parseEIPOPrivateKey reads the firm's RSA private key (PKCS#1 or PKCS#8).
func parseEIPOPrivateKey(s string) (*rsa.PrivateKey, error) {
	blocks := eipoPEMBlocks(s)
	if len(blocks) == 0 {
		return nil, invalid("the firm's private key could not be read: paste the whole PEM file, from -----BEGIN PRIVATE KEY----- (or RSA PRIVATE KEY) to the END line")
	}
	for _, b := range blocks {
		switch {
		case b.Type == "ENCRYPTED PRIVATE KEY" || b.Headers["Proc-Type"] != "":
			return nil, invalid("the firm's private key is protected by a passphrase: save it without one and paste that")
		case b.Type == "PUBLIC KEY" || b.Type == "RSA PUBLIC KEY" || b.Type == "CERTIFICATE":
			continue
		}
		var k any
		var err error
		if k, err = x509.ParsePKCS1PrivateKey(b.Bytes); err != nil {
			k, err = x509.ParsePKCS8PrivateKey(b.Bytes)
		}
		if err != nil {
			continue
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, invalid("the firm's private key is not an RSA key: NSE's eIPO v2 uses 2048-bit RSA")
		}
		if n := rk.N.BitLen(); n != eipoRSABits {
			return nil, invalid("the firm's private key is %d-bit: NSE's eIPO v2 uses 2048-bit RSA keys", n)
		}
		return rk, nil
	}
	return nil, invalid("no private key found in what was pasted as the firm's private key (a public key or certificate goes to NSE, not here)")
}

// parseEIPOPublicKey reads NSE's public key: a PUBLIC KEY (PKIX), RSA
// PUBLIC KEY (PKCS#1) or a certificate holding it.
func parseEIPOPublicKey(s string) (*rsa.PublicKey, error) {
	blocks := eipoPEMBlocks(s)
	if len(blocks) == 0 {
		return nil, invalid("NSE's public key could not be read: paste the whole PEM file NSE gave, from -----BEGIN PUBLIC KEY----- (or CERTIFICATE) to the END line")
	}
	for _, b := range blocks {
		if strings.Contains(b.Type, "PRIVATE") {
			return nil, invalid("a private key was pasted as NSE's public key: NSE's public key is the one NSE sends")
		}
		var k any
		if c, err := x509.ParseCertificate(b.Bytes); err == nil {
			k = c.PublicKey
		} else if p, err := x509.ParsePKIXPublicKey(b.Bytes); err == nil {
			k = p
		} else if p, err := x509.ParsePKCS1PublicKey(b.Bytes); err == nil {
			k = p
		} else {
			continue
		}
		rk, ok := k.(*rsa.PublicKey)
		if !ok {
			return nil, invalid("NSE's public key is not an RSA key: NSE's eIPO v2 uses 2048-bit RSA")
		}
		if n := rk.N.BitLen(); n != eipoRSABits {
			return nil, invalid("NSE's public key is %d-bit: NSE's eIPO v2 uses 2048-bit RSA keys", n)
		}
		return rk, nil
	}
	return nil, invalid("NSE's public key could not be read: it is not a PEM public key or certificate")
}

// eipoV2Keys reads the connection's v2 keys. need says both must be there
// (the connection is on v2); otherwise only those entered are checked.
func eipoV2Keys(secrets map[string]string, need bool) (*rsa.PrivateKey, *rsa.PublicKey, error) {
	ps, ns := strings.TrimSpace(secrets["private_key"]), strings.TrimSpace(secrets["nse_public_key"])
	if need && ps == "" {
		return nil, nil, invalid("v2 needs the firm's RSA private key: enter it (the firm makes the key pair and gives NSE the public key)")
	}
	if need && ns == "" {
		return nil, nil, invalid("v2 needs NSE's public key: enter the one NSE gave the firm (ask msm@nse.co.in)")
	}
	var priv *rsa.PrivateKey
	var pub *rsa.PublicKey
	var err error
	if ps != "" {
		if priv, err = parseEIPOPrivateKey(ps); err != nil {
			return nil, nil, err
		}
	}
	if ns != "" {
		if pub, err = parseEIPOPublicKey(ns); err != nil {
			return nil, nil, err
		}
	}
	if priv != nil && pub != nil && priv.PublicKey.Equal(pub) {
		return nil, nil, invalid("NSE's public key is the firm's own public key: enter the key NSE gave, and give NSE the firm's")
	}
	return priv, pub, nil
}

// checkEIPOConn checks the eIPO connection's keys on saving.
func checkEIPOConn(c Connection) error {
	_, _, err := eipoV2Keys(c.Secrets, c.Values["version"] == eipoV2)
	return err
}

// EIPOCallbackReply is the reply to a callback as it goes back to NSE. On
// a v2 connection, once the callback token matched, it is sealed for NSE
// (key1 and Sig, no key3: the responder's side of advanced security);
// otherwise it is plain JSON.
func (o *Ops) EIPOCallbackReply(ctx context.Context, tenant, token string, v any) (http.Header, []byte) {
	b, _ := json.Marshal(v)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	conn, ok := o.eipoConnection(ctx, tenant)
	if !ok || conn.token == "" || conn.cfg.Version != "v2" || conn.keyErr != nil || !hmac.Equal([]byte(strings.TrimSpace(token)), []byte(conn.token)) {
		return h, b
	}
	m, err := eipoSeal(conn.cfg.NSEKey, nil, b, true)
	if err != nil {
		return h, b
	}
	m.set(h)
	return h, m.Body
}
