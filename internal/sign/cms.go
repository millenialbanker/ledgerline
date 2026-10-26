// Package sign makes and checks the digital signatures on contract notes
// and statements: CMS (PKCS #7) detached signatures as embedded in signed
// PDFs, with the firm's certificate and key. Standard library only.
package sign

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"
)

var (
	oidData                 = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidSignedData           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidContentType          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest        = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningTime          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
	oidSigningCertificateV2 = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 2, 47}
	oidSHA1                 = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}
	oidSHA256               = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidSHA384               = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}
	oidSHA512               = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}
	oidRSA                  = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidRSASHA1              = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 5}
	oidRSASHA256            = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}
	oidRSASHA384            = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 12}
	oidRSASHA512            = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 13}
	oidECDSASHA256          = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
	oidECDSASHA384          = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 3}
)

// Signer holds the firm's signing key and certificate chain.
type Signer struct {
	Key   crypto.Signer
	Cert  *x509.Certificate
	Chain []*x509.Certificate // issuing certificates, signer first excluded
}

// Info describes a signer, for the dispatch register.
type Info struct {
	Name    string `json:"name"`
	Issuer  string `json:"issuer"`
	Serial  string `json:"serial"`
	From    string `json:"valid_from"`
	Until   string `json:"valid_until"`
	SHA256  string `json:"sha256"`
	KeyType string `json:"key_type"`
}

// CertInfo describes a certificate.
func CertInfo(c *x509.Certificate) Info {
	sum := sha256.Sum256(c.Raw)
	kt := "unknown"
	switch k := c.PublicKey.(type) {
	case *rsa.PublicKey:
		kt = fmt.Sprintf("RSA %d", k.N.BitLen())
	case *ecdsa.PublicKey:
		kt = "ECDSA " + k.Curve.Params().Name
	}
	return Info{Name: firstNonEmpty(c.Subject.CommonName, c.Subject.String()), Issuer: firstNonEmpty(c.Issuer.CommonName, c.Issuer.String()),
		Serial: fmt.Sprintf("%X", c.SerialNumber), From: c.NotBefore.UTC().Format(time.RFC3339), Until: c.NotAfter.UTC().Format(time.RFC3339),
		SHA256: fmt.Sprintf("%X", sum), KeyType: kt}
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// Check says whether the signer can sign at t.
func (s *Signer) Check(t time.Time) error {
	if s == nil || s.Key == nil || s.Cert == nil {
		return errors.New("no signing certificate is set up")
	}
	if t.Before(s.Cert.NotBefore) {
		return fmt.Errorf("the signing certificate is not valid until %s", s.Cert.NotBefore.Format("02-Jan-2006"))
	}
	if t.After(s.Cert.NotAfter) {
		return fmt.Errorf("the signing certificate expired on %s", s.Cert.NotAfter.Format("02-Jan-2006"))
	}
	if !publicKeysMatch(s.Key.Public(), s.Cert.PublicKey) {
		return errors.New("the private key does not belong to the signing certificate")
	}
	if s.Cert.KeyUsage != 0 && s.Cert.KeyUsage&(x509.KeyUsageDigitalSignature|x509.KeyUsageContentCommitment) == 0 {
		return errors.New("the certificate is not for digital signatures (key usage)")
	}
	return nil
}

func publicKeysMatch(a, b crypto.PublicKey) bool {
	type eq interface{ Equal(crypto.PublicKey) bool }
	if x, ok := a.(eq); ok {
		return x.Equal(b)
	}
	return false
}

// ---- small DER helpers ----

func tlv(tag byte, content []byte) []byte {
	n := len(content)
	var l []byte
	switch {
	case n < 0x80:
		l = []byte{byte(n)}
	case n < 0x100:
		l = []byte{0x81, byte(n)}
	case n < 0x10000:
		l = []byte{0x82, byte(n >> 8), byte(n)}
	case n < 0x1000000:
		l = []byte{0x83, byte(n >> 16), byte(n >> 8), byte(n)}
	default:
		l = []byte{0x84, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
	out := make([]byte, 0, 1+len(l)+n)
	out = append(out, tag)
	out = append(out, l...)
	return append(out, content...)
}

func seq(parts ...[]byte) []byte { return tlv(0x30, bytes.Join(parts, nil)) }

// setOf is a DER SET OF: elements sorted by their encodings.
func setOf(tag byte, parts ...[]byte) []byte {
	ps := append([][]byte(nil), parts...)
	sort.Slice(ps, func(i, j int) bool { return bytes.Compare(ps[i], ps[j]) < 0 })
	return tlv(tag, bytes.Join(ps, nil))
}

func mustDER(v any) []byte {
	b, err := asn1.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func algID(oid asn1.ObjectIdentifier, nullParams bool) []byte {
	if nullParams {
		return seq(mustDER(oid), []byte{0x05, 0x00})
	}
	return seq(mustDER(oid))
}

// SignDetached makes a CMS SignedData over content (not included), with
// SHA-256 and the signed attributes PDF readers expect: content type,
// signing time, message digest and the signing certificate (ESS v2).
func (s *Signer) SignDetached(content []byte, at time.Time) ([]byte, error) {
	digest := sha256.Sum256(content)
	return s.SignDigest(digest[:], at)
}

// SignDigest is SignDetached for a SHA-256 digest computed elsewhere (a
// signing agent next to a USB token receives only the digest).
func (s *Signer) SignDigest(digest []byte, at time.Time) ([]byte, error) {
	if err := s.Check(at); err != nil {
		return nil, err
	}
	if len(digest) != sha256.Size {
		return nil, errors.New("digest must be SHA-256")
	}
	certHash := sha256.Sum256(s.Cert.Raw)
	attr := func(oid asn1.ObjectIdentifier, val []byte) []byte { return seq(mustDER(oid), setOf(0x31, val)) }
	var st []byte
	if y := at.UTC().Year(); y >= 1950 && y < 2050 {
		st = mustDER(at.UTC()) // UTCTime
	} else {
		st, _ = asn1.MarshalWithParams(at.UTC(), "generalized")
	}
	attrs := [][]byte{
		attr(oidContentType, mustDER(oidData)),
		attr(oidSigningTime, st),
		attr(oidMessageDigest, mustDER(digest)),
		attr(oidSigningCertificateV2, seq(seq(seq(mustDER(certHash[:]))))),
	}
	signedSet := setOf(0x31, attrs...)
	h := sha256.Sum256(signedSet)
	var sigAlg []byte
	var sig []byte
	var err error
	switch s.Key.Public().(type) {
	case *rsa.PublicKey:
		sigAlg = algID(oidRSA, true)
		sig, err = s.Key.Sign(rand.Reader, h[:], crypto.SHA256)
	case *ecdsa.PublicKey:
		sigAlg = algID(oidECDSASHA256, false)
		sig, err = s.Key.Sign(rand.Reader, h[:], crypto.SHA256)
	default:
		return nil, errors.New("only RSA and ECDSA keys can sign")
	}
	if err != nil {
		return nil, fmt.Errorf("signing: %w", err)
	}
	sha256Alg := algID(oidSHA256, true)
	sid := seq(s.Cert.RawIssuer, mustDER(s.Cert.SerialNumber))
	signerInfo := seq(
		mustDER(1), sid, sha256Alg,
		append([]byte{0xA0}, signedSet[1:]...), // [0] IMPLICIT SET
		sigAlg, mustDER(sig),
	)
	certs := [][]byte{s.Cert.Raw}
	for _, c := range s.Chain {
		if !bytes.Equal(c.Raw, s.Cert.Raw) {
			certs = append(certs, c.Raw)
		}
	}
	sd := seq(
		mustDER(1),
		setOf(0x31, sha256Alg),
		seq(mustDER(oidData)),
		tlv(0xA0, bytes.Join(certs, nil)),
		setOf(0x31, signerInfo),
	)
	return seq(mustDER(oidSignedData), tlv(0xA0, sd)), nil
}

// ---- verification ----

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,tag:0"`
}

type signedData struct {
	Version          int
	DigestAlgorithms asn1.RawValue
	EncapContentInfo asn1.RawValue
	Certificates     asn1.RawValue `asn1:"optional,tag:0"`
	CRLs             asn1.RawValue `asn1:"optional,tag:1"`
	SignerInfos      asn1.RawValue
}

type algorithmIdentifier struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type issuerAndSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
}

type signerInfo struct {
	Version            int
	SID                asn1.RawValue
	DigestAlgorithm    algorithmIdentifier
	SignedAttrs        asn1.RawValue `asn1:"optional,tag:0"`
	SignatureAlgorithm algorithmIdentifier
	Signature          []byte
	UnsignedAttrs      asn1.RawValue `asn1:"optional,tag:1"`
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue
}

// Verified is the result of checking a signature.
type Verified struct {
	Signer      Info      `json:"signer"`
	SigningTime time.Time `json:"signing_time"`
	Digest      string    `json:"digest_algorithm"`
	Cert        *x509.Certificate `json:"-"`
}

// Verify checks a CMS detached signature over content: the digest matches
// and the signature was made by the certificate's key. It does not decide
// whether the certificate is trusted; callers check the issuer.
func Verify(cms, content []byte) (Verified, error) {
	var ci contentInfo
	if rest, err := asn1.Unmarshal(cms, &ci); err != nil {
		return Verified{}, fmt.Errorf("not a CMS signature: %w", err)
	} else if len(bytes.TrimRight(rest, "\x00")) > 0 {
		return Verified{}, errors.New("unexpected data after the signature")
	}
	if !ci.ContentType.Equal(oidSignedData) {
		return Verified{}, errors.New("not a CMS SignedData")
	}
	var sd signedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return Verified{}, fmt.Errorf("signed data: %w", err)
	}
	var certs []*x509.Certificate
	if len(sd.Certificates.Bytes) > 0 {
		cs, err := x509.ParseCertificates(sd.Certificates.Bytes)
		if err != nil {
			return Verified{}, fmt.Errorf("certificates: %w", err)
		}
		certs = cs
	}
	var sis []signerInfo
	rest := sd.SignerInfos.Bytes
	for len(rest) > 0 {
		var si signerInfo
		r, err := asn1.Unmarshal(rest, &si)
		if err != nil {
			return Verified{}, fmt.Errorf("signer info: %w", err)
		}
		sis = append(sis, si)
		rest = r
	}
	if len(sis) != 1 {
		return Verified{}, fmt.Errorf("expected one signer, found %d", len(sis))
	}
	si := sis[0]
	var ias issuerAndSerial
	if _, err := asn1.Unmarshal(si.SID.FullBytes, &ias); err != nil {
		return Verified{}, errors.New("signer is not identified by issuer and serial")
	}
	var cert *x509.Certificate
	for _, c := range certs {
		if c.SerialNumber.Cmp(ias.Serial) == 0 && bytes.Equal(c.RawIssuer, ias.Issuer.FullBytes) {
			cert = c
		}
	}
	if cert == nil {
		return Verified{}, errors.New("the signer's certificate is not in the signature")
	}
	hash, hname, err := hashFor(si.DigestAlgorithm.Algorithm)
	if err != nil {
		return Verified{}, err
	}
	hf := hash.New()
	hf.Write(content)
	contentDigest := hf.Sum(nil)
	v := Verified{Signer: CertInfo(cert), Digest: hname, Cert: cert}
	toSign := contentDigest
	if len(si.SignedAttrs.Bytes) > 0 {
		var mdOK bool
		ar := si.SignedAttrs.Bytes
		for len(ar) > 0 {
			var a attribute
			r, err := asn1.Unmarshal(ar, &a)
			if err != nil {
				return v, fmt.Errorf("signed attributes: %w", err)
			}
			ar = r
			switch {
			case a.Type.Equal(oidMessageDigest):
				var md []byte
				if _, err := asn1.Unmarshal(a.Values.Bytes, &md); err != nil {
					return v, err
				}
				if !bytes.Equal(md, contentDigest) {
					return v, errors.New("the document was changed after it was signed")
				}
				mdOK = true
			case a.Type.Equal(oidSigningTime):
				var t time.Time
				if _, err := asn1.Unmarshal(a.Values.Bytes, &t); err == nil {
					v.SigningTime = t
				}
			}
		}
		if !mdOK {
			return v, errors.New("the signature has no message digest")
		}
		// The signature covers the attributes encoded as a SET.
		set := append([]byte{0x31}, si.SignedAttrs.FullBytes[1:]...)
		h := hash.New()
		h.Write(set)
		toSign = h.Sum(nil)
	}
	switch pub := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		if err := rsa.VerifyPKCS1v15(pub, hash, toSign, si.Signature); err != nil {
			return v, errors.New("the signature does not match the certificate")
		}
	case *ecdsa.PublicKey:
		if !ecdsa.VerifyASN1(pub, toSign, si.Signature) {
			return v, errors.New("the signature does not match the certificate")
		}
	default:
		return v, errors.New("unsupported key type in the certificate")
	}
	return v, nil
}

func hashFor(oid asn1.ObjectIdentifier) (crypto.Hash, string, error) {
	switch {
	case oid.Equal(oidSHA256), oid.Equal(oidRSASHA256), oid.Equal(oidECDSASHA256):
		return crypto.SHA256, "SHA-256", nil
	case oid.Equal(oidSHA1), oid.Equal(oidRSASHA1):
		return crypto.SHA1, "SHA-1", nil
	case oid.Equal(oidSHA384), oid.Equal(oidRSASHA384), oid.Equal(oidECDSASHA384):
		return crypto.SHA384, "SHA-384", nil
	case oid.Equal(oidSHA512), oid.Equal(oidRSASHA512):
		return crypto.SHA512, "SHA-512", nil
	}
	return 0, "", fmt.Errorf("unsupported digest algorithm %v", oid)
}

var _ = sha1.New // SHA-1 is registered for verifying older documents
