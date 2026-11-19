package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"

	"ledgerline/internal/sign"
)

// backend is where the private key is: the USB token (Windows) or a
// certificate file. Sign takes a SHA-256 digest and returns an RSA PKCS #1
// v1.5 signature, or an ECDSA signature in ASN.1 DER, as crypto.Signer
// does. Reopen gets the key again after the token was pulled out.
type backend interface {
	crypto.Signer
	Certificate() *x509.Certificate
	Reopen() error
	Close()
}

// fileBackend signs with a certificate file (.pfx, .p12 or PEM), read with
// Ledgerline's own loader. For tests, and for firms whose certificate is a
// file on this computer.
type fileBackend struct{ s *sign.Signer }

func openFile(path, password string) (backend, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := sign.Load(b, password)
	if err != nil {
		return nil, err
	}
	return &fileBackend{s: s}, nil
}

func (f *fileBackend) Public() crypto.PublicKey       { return f.s.Key.Public() }
func (f *fileBackend) Certificate() *x509.Certificate { return f.s.Cert }
func (f *fileBackend) Reopen() error                  { return nil }
func (f *fileBackend) Close()                         {}
func (f *fileBackend) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return f.s.Key.Sign(r, digest, opts)
}

// signDigest signs a SHA-256 digest with the backend.
func signDigest(be backend, digest []byte) ([]byte, error) {
	if len(digest) != 32 {
		return nil, errors.New("not a SHA-256 digest")
	}
	return be.Sign(rand.Reader, digest, crypto.SHA256)
}

// verifyRaw checks a signature over digest against the certificate, as
// Ledgerline will.
func verifyRaw(c *x509.Certificate, digest, sig []byte) error {
	switch pub := c.PublicKey.(type) {
	case *rsa.PublicKey:
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, sig); err != nil {
			return errors.New("the signature does not match the certificate")
		}
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(pub, digest, sig) {
			return errors.New("the signature does not match the certificate")
		}
	default:
		return fmt.Errorf("the certificate's key type %T cannot sign documents", pub)
	}
	return nil
}

// ecdsaRawToDER turns the r‖s Windows CNG returns into ASN.1 DER.
func ecdsaRawToDER(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw)%2 != 0 {
		return nil, errors.New("unexpected ECDSA signature length")
	}
	n := len(raw) / 2
	return asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(raw[:n]), new(big.Int).SetBytes(raw[n:])})
}
