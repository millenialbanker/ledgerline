// Package hdfcapi talks to HDFC Bank's corporate APIs that use the bank's
// "JOSE High" security: an OAuth client-credentials token, mutual TLS from
// a whitelisted address, and every request and response body signed and
// encrypted as a nested JWT.
//
// From the bank's Technical Integration Document (JOSE API Spec v2.1) and
// its "JOSE-High: Handling the Request / the Response" recipes:
//
//   - The request JSON is signed as a compact JWS: header typ "JWS", alg
//     RS256, kid = BASE64URL(SHA-256(the firm's public key, X.509
//     SubjectPublicKeyInfo DER)). The payload is the plain request JSON.
//   - The JWS is encrypted as a compact JWE to the bank: alg RSA-OAEP-256,
//     enc A256GCM, typ "JWE", kid made the same way from the bank's public
//     key. The JWE is the request body (Content-Type application/jose).
//   - The response is a JWE to the firm's key (its kid must be the firm's)
//     holding a JWS signed by the bank (its kid must be the bank's), which
//     is checked with the bank's public key.
//
// Only the standard library is used.
package hdfcapi

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var b64 = base64.RawURLEncoding

// KID is the bank's key hint: BASE64URL(SHA-256(public key DER)).
func KID(pub *rsa.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(der)
	return b64.EncodeToString(h[:]), nil
}

type jwsHeader struct {
	Typ string `json:"typ"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
}

type jweHeader struct {
	Alg string `json:"alg"`
	Enc string `json:"enc"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// Sign makes a compact RS256 JWS of payload.
func Sign(payload []byte, priv *rsa.PrivateKey, kid string) (string, error) {
	h, err := json.Marshal(jwsHeader{Typ: "JWS", Kid: kid, Alg: "RS256"})
	if err != nil {
		return "", err
	}
	in := b64.EncodeToString(h) + "." + b64.EncodeToString(payload)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return in + "." + b64.EncodeToString(sig), nil
}

// Verify checks a compact RS256 JWS with pub and returns its payload. The
// header's kid must be wantKid.
func Verify(jws string, pub *rsa.PublicKey, wantKid string) ([]byte, error) {
	p := strings.Split(strings.TrimSpace(jws), ".")
	if len(p) != 3 {
		return nil, errors.New("the signed message does not have three parts")
	}
	hb, err := b64.DecodeString(p[0])
	if err != nil {
		return nil, fmt.Errorf("the signed message's header: %w", err)
	}
	var h jwsHeader
	if err := json.Unmarshal(hb, &h); err != nil {
		return nil, fmt.Errorf("the signed message's header: %w", err)
	}
	if h.Alg != "RS256" {
		return nil, fmt.Errorf("the signed message uses %q, not RS256", h.Alg)
	}
	if wantKid != "" && h.Kid != wantKid {
		return nil, fmt.Errorf("the signed message names key %q, not the bank's key %q", h.Kid, wantKid)
	}
	sig, err := b64.DecodeString(p[2])
	if err != nil {
		return nil, fmt.Errorf("the signature: %w", err)
	}
	sum := sha256.Sum256([]byte(p[0] + "." + p[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return nil, errors.New("the bank's signature does not match")
	}
	body, err := b64.DecodeString(p[1])
	if err != nil {
		return nil, fmt.Errorf("the signed payload: %w", err)
	}
	return body, nil
}

// Encrypt makes a compact JWE (RSA-OAEP-256, A256GCM) of plain for pub.
func Encrypt(plain []byte, pub *rsa.PublicKey, kid string) (string, error) {
	h, err := json.Marshal(jweHeader{Alg: "RSA-OAEP-256", Enc: "A256GCM", Typ: "JWE", Kid: kid})
	if err != nil {
		return "", err
	}
	cek := make([]byte, 32)
	if _, err := rand.Read(cek); err != nil {
		return "", err
	}
	ek, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, cek, nil)
	if err != nil {
		return "", err
	}
	blk, err := aes.NewCipher(cek)
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return "", err
	}
	iv := make([]byte, g.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	eh := b64.EncodeToString(h)
	sealed := g.Seal(nil, iv, plain, []byte(eh))
	ct, tag := sealed[:len(sealed)-g.Overhead()], sealed[len(sealed)-g.Overhead():]
	return strings.Join([]string{eh, b64.EncodeToString(ek), b64.EncodeToString(iv), b64.EncodeToString(ct), b64.EncodeToString(tag)}, "."), nil
}

// Decrypt opens a compact JWE with priv. Only RSA-OAEP-256 with A256GCM is
// accepted, and the header's kid must be wantKid.
func Decrypt(jwe string, priv *rsa.PrivateKey, wantKid string) ([]byte, error) {
	p := strings.Split(strings.TrimSpace(jwe), ".")
	if len(p) != 5 {
		return nil, errors.New("the encrypted message does not have five parts")
	}
	hb, err := b64.DecodeString(p[0])
	if err != nil {
		return nil, fmt.Errorf("the encrypted message's header: %w", err)
	}
	var h jweHeader
	if err := json.Unmarshal(hb, &h); err != nil {
		return nil, fmt.Errorf("the encrypted message's header: %w", err)
	}
	if h.Alg != "RSA-OAEP-256" || h.Enc != "A256GCM" {
		return nil, fmt.Errorf("the encrypted message uses %s/%s, not RSA-OAEP-256/A256GCM", h.Alg, h.Enc)
	}
	if wantKid != "" && h.Kid != wantKid {
		return nil, fmt.Errorf("the encrypted message is for key %q, not the firm's key %q", h.Kid, wantKid)
	}
	var parts [4][]byte
	for i := range parts {
		if parts[i], err = b64.DecodeString(p[i+1]); err != nil {
			return nil, fmt.Errorf("the encrypted message, part %d: %w", i+2, err)
		}
	}
	cek, err := rsa.DecryptOAEP(sha256.New(), nil, priv, parts[0], nil)
	if err != nil || len(cek) != 32 {
		return nil, errors.New("the encrypted message's key could not be opened with the firm's private key")
	}
	blk, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	if len(parts[1]) != g.NonceSize() {
		return nil, errors.New("the encrypted message's IV is not 12 bytes")
	}
	plain, err := g.Open(nil, parts[1], append(parts[2], parts[3]...), []byte(p[0]))
	if err != nil {
		return nil, errors.New("the encrypted message was changed or is not for this key")
	}
	return plain, nil
}
