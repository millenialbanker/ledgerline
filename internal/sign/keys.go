package sign

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	_ "crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"unicode/utf16"
)

// Load reads the firm's signing key and certificates from a PKCS #12 file
// (.pfx or .p12, as exported from a DSC) or from PEM (certificates and a
// private key, in any order).
func Load(data []byte, password string) (*Signer, error) {
	if bytes.Contains(data, []byte("-----BEGIN")) {
		return loadPEM(data, password)
	}
	return loadPKCS12(data, password)
}

func loadPEM(data []byte, password string) (*Signer, error) {
	var key crypto.Signer
	var certs []*x509.Certificate
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			break
		}
		switch b.Type {
		case "CERTIFICATE":
			c, err := x509.ParseCertificate(b.Bytes)
			if err != nil {
				return nil, fmt.Errorf("certificate: %w", err)
			}
			certs = append(certs, c)
		case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
			der := b.Bytes
			//lint:ignore SA1019 legacy encrypted PEM keys are still exported by some tools
			if x509.IsEncryptedPEMBlock(b) { //nolint:staticcheck
				d, err := x509.DecryptPEMBlock(b, []byte(password)) //nolint:staticcheck
				if err != nil {
					return nil, errors.New("wrong password for the private key")
				}
				der = d
			}
			k, err := parseKey(der)
			if err != nil {
				return nil, err
			}
			key = k
		case "ENCRYPTED PRIVATE KEY":
			der, err := decryptPKCS8(b.Bytes, password)
			if err != nil {
				return nil, err
			}
			k, err := parseKey(der)
			if err != nil {
				return nil, err
			}
			key = k
		}
	}
	return assemble(key, certs)
}

func parseKey(der []byte) (crypto.Signer, error) {
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if s, ok := k.(crypto.Signer); ok {
			return s, nil
		}
		return nil, errors.New("unsupported private key type")
	}
	if k, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(der); err == nil {
		return k, nil
	}
	return nil, errors.New("could not read the private key")
}

// assemble picks the certificate that matches the key and orders the rest.
func assemble(key crypto.Signer, certs []*x509.Certificate) (*Signer, error) {
	if key == nil {
		return nil, errors.New("no private key found in the file")
	}
	if len(certs) == 0 {
		return nil, errors.New("no certificate found in the file")
	}
	s := &Signer{Key: key}
	for _, c := range certs {
		if publicKeysMatch(key.Public(), c.PublicKey) {
			s.Cert = c
		}
	}
	if s.Cert == nil {
		return nil, errors.New("none of the certificates matches the private key")
	}
	// Chain: walk issuers among the remaining certificates.
	cur := s.Cert
	for i := 0; i < len(certs); i++ {
		var next *x509.Certificate
		for _, c := range certs {
			if c != cur && bytes.Equal(c.RawSubject, cur.RawIssuer) {
				next = c
			}
		}
		if next == nil || bytes.Equal(next.Raw, cur.Raw) {
			break
		}
		s.Chain = append(s.Chain, next)
		cur = next
		if bytes.Equal(cur.RawSubject, cur.RawIssuer) {
			break
		}
	}
	return s, nil
}

// ---- PKCS #12 ----

var (
	oidPKCS7Data          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidPKCS7Encrypted     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 6}
	oidKeyBag             = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 1}
	oidShroudedKeyBag     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 2}
	oidCertBag            = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 3}
	oidX509Cert           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 22, 1}
	oidPBEWithSHA3DES     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 1, 3}
	oidPBEWithSHARC2_40   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 1, 6}
	oidPBEWithSHARC2_128  = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 1, 5}
	oidPBES2              = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}
	oidPBKDF2             = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}
	oidHMACSHA1           = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 7}
	oidHMACSHA256         = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 9}
	oidAES128CBC          = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES192CBC          = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 22}
	oidAES256CBC          = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
	oidDESEDE3CBC         = asn1.ObjectIdentifier{1, 2, 840, 113549, 3, 7}
)

type pfxPdu struct {
	Version  int
	AuthSafe contentInfo
	MacData  macData `asn1:"optional"`
}

type macData struct {
	Mac        digestInfo
	MacSalt    []byte
	Iterations int `asn1:"optional,default:1"`
}

type digestInfo struct {
	Algorithm algorithmIdentifier
	Digest    []byte
}

type encryptedData struct {
	Version              int
	EncryptedContentInfo encryptedContentInfo
}

type encryptedContentInfo struct {
	ContentType                asn1.ObjectIdentifier
	ContentEncryptionAlgorithm algorithmIdentifier
	EncryptedContent           []byte `asn1:"tag:0,optional"`
}

type safeBag struct {
	ID         asn1.ObjectIdentifier
	Value      asn1.RawValue `asn1:"tag:0,explicit"`
	Attributes asn1.RawValue `asn1:"optional"`
}

type certBag struct {
	ID   asn1.ObjectIdentifier
	Data []byte `asn1:"tag:0,explicit"`
}

type encryptedPrivateKeyInfo struct {
	Algorithm     algorithmIdentifier
	EncryptedData []byte
}

type pbeParams struct {
	Salt       []byte
	Iterations int
}

type pbes2Params struct {
	KeyDerivationFunc algorithmIdentifier
	EncryptionScheme  algorithmIdentifier
}

type pbkdf2Params struct {
	Salt       []byte
	Iterations int
	KeyLength  int                 `asn1:"optional"`
	PRF        algorithmIdentifier `asn1:"optional"`
}

func loadPKCS12(data []byte, password string) (*Signer, error) {
	var pfx pfxPdu
	if _, err := asn1.Unmarshal(data, &pfx); err != nil {
		return nil, errors.New("not a .pfx/.p12 or PEM file")
	}
	if pfx.Version != 3 {
		return nil, fmt.Errorf("unsupported PKCS #12 version %d", pfx.Version)
	}
	if !pfx.AuthSafe.ContentType.Equal(oidPKCS7Data) {
		return nil, errors.New("PKCS #12 files signed with a public key are not supported")
	}
	var authSafe []byte
	if _, err := asn1.Unmarshal(pfx.AuthSafe.Content.Bytes, &authSafe); err != nil {
		return nil, fmt.Errorf("PKCS #12 content: %w", err)
	}
	bmp := bmpPassword(password)
	if len(pfx.MacData.Mac.Digest) > 0 {
		if err := checkMAC(pfx.MacData, authSafe, bmp); err != nil {
			return nil, err
		}
	}
	var cis []contentInfo
	if _, err := asn1.Unmarshal(authSafe, &cis); err != nil {
		return nil, fmt.Errorf("PKCS #12 safe: %w", err)
	}
	var key crypto.Signer
	var certs []*x509.Certificate
	for _, ci := range cis {
		var bagsDER []byte
		switch {
		case ci.ContentType.Equal(oidPKCS7Data):
			if _, err := asn1.Unmarshal(ci.Content.Bytes, &bagsDER); err != nil {
				return nil, err
			}
		case ci.ContentType.Equal(oidPKCS7Encrypted):
			var ed encryptedData
			if _, err := asn1.Unmarshal(ci.Content.Bytes, &ed); err != nil {
				return nil, err
			}
			d, err := pbeDecrypt(ed.EncryptedContentInfo.ContentEncryptionAlgorithm, ed.EncryptedContentInfo.EncryptedContent, password, bmp)
			if err != nil {
				return nil, err
			}
			bagsDER = d
		default:
			continue
		}
		var bags []safeBag
		if _, err := asn1.Unmarshal(bagsDER, &bags); err != nil {
			return nil, fmt.Errorf("PKCS #12 bags: %w", err)
		}
		for _, b := range bags {
			switch {
			case b.ID.Equal(oidCertBag):
				var cb certBag
				if _, err := asn1.Unmarshal(b.Value.Bytes, &cb); err != nil {
					return nil, err
				}
				if cb.ID.Equal(oidX509Cert) {
					c, err := x509.ParseCertificate(cb.Data)
					if err != nil {
						return nil, fmt.Errorf("certificate: %w", err)
					}
					certs = append(certs, c)
				}
			case b.ID.Equal(oidKeyBag):
				k, err := parseKey(b.Value.Bytes)
				if err != nil {
					return nil, err
				}
				key = k
			case b.ID.Equal(oidShroudedKeyBag):
				der, err := decryptPKCS8Bytes(b.Value.Bytes, password, bmp)
				if err != nil {
					return nil, err
				}
				k, err := parseKey(der)
				if err != nil {
					return nil, err
				}
				key = k
			}
		}
	}
	return assemble(key, certs)
}

func decryptPKCS8(der []byte, password string) ([]byte, error) {
	return decryptPKCS8Bytes(der, password, bmpPassword(password))
}

func decryptPKCS8Bytes(der []byte, password string, bmp []byte) ([]byte, error) {
	var epki encryptedPrivateKeyInfo
	if _, err := asn1.Unmarshal(der, &epki); err != nil {
		return nil, fmt.Errorf("encrypted key: %w", err)
	}
	return pbeDecrypt(epki.Algorithm, epki.EncryptedData, password, bmp)
}

func bmpPassword(p string) []byte {
	if p == "" {
		return []byte{0, 0}
	}
	u := utf16.Encode([]rune(p))
	out := make([]byte, 0, 2*len(u)+2)
	for _, c := range u {
		out = append(out, byte(c>>8), byte(c))
	}
	return append(out, 0, 0)
}

var errPassword = errors.New("wrong password for the certificate file")

func pbeDecrypt(alg algorithmIdentifier, data []byte, password string, bmp []byte) ([]byte, error) {
	var block cipher.Block
	var iv []byte
	switch {
	case alg.Algorithm.Equal(oidPBEWithSHA3DES), alg.Algorithm.Equal(oidPBEWithSHARC2_40), alg.Algorithm.Equal(oidPBEWithSHARC2_128):
		var p pbeParams
		if _, err := asn1.Unmarshal(alg.Parameters.FullBytes, &p); err != nil {
			return nil, err
		}
		switch {
		case alg.Algorithm.Equal(oidPBEWithSHA3DES):
			k := pkcs12KDF(sha1.New, 1, p.Salt, bmp, p.Iterations, 24)
			b, err := des.NewTripleDESCipher(k)
			if err != nil {
				return nil, err
			}
			block = b
		default:
			n := 5
			if alg.Algorithm.Equal(oidPBEWithSHARC2_128) {
				n = 16
			}
			k := pkcs12KDF(sha1.New, 1, p.Salt, bmp, p.Iterations, n)
			block = newRC2(k, n*8)
		}
		iv = pkcs12KDF(sha1.New, 2, p.Salt, bmp, p.Iterations, 8)
	case alg.Algorithm.Equal(oidPBES2):
		var p pbes2Params
		if _, err := asn1.Unmarshal(alg.Parameters.FullBytes, &p); err != nil {
			return nil, err
		}
		if !p.KeyDerivationFunc.Algorithm.Equal(oidPBKDF2) {
			return nil, errors.New("unsupported key derivation in the certificate file")
		}
		var kp pbkdf2Params
		if _, err := asn1.Unmarshal(p.KeyDerivationFunc.Parameters.FullBytes, &kp); err != nil {
			return nil, err
		}
		prf := sha1.New
		if kp.PRF.Algorithm.Equal(oidHMACSHA256) {
			prf = sha256.New
		} else if len(kp.PRF.Algorithm) > 0 && !kp.PRF.Algorithm.Equal(oidHMACSHA1) {
			return nil, errors.New("unsupported PBKDF2 hash in the certificate file")
		}
		var klen int
		es := p.EncryptionScheme.Algorithm
		switch {
		case es.Equal(oidAES128CBC):
			klen = 16
		case es.Equal(oidAES192CBC):
			klen = 24
		case es.Equal(oidAES256CBC):
			klen = 32
		case es.Equal(oidDESEDE3CBC):
			klen = 24
		default:
			return nil, errors.New("unsupported cipher in the certificate file")
		}
		if _, err := asn1.Unmarshal(p.EncryptionScheme.Parameters.FullBytes, &iv); err != nil {
			return nil, err
		}
		k, err := pbkdf2.Key(prf, password, kp.Salt, kp.Iterations, klen)
		if err != nil {
			return nil, err
		}
		if es.Equal(oidDESEDE3CBC) {
			block, err = des.NewTripleDESCipher(k)
		} else {
			block, err = aes.NewCipher(k)
		}
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported encryption %v in the certificate file", alg.Algorithm)
	}
	bs := block.BlockSize()
	if len(data) == 0 || len(data)%bs != 0 || len(iv) != bs {
		return nil, errPassword
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	pad := int(out[len(out)-1])
	if pad == 0 || pad > bs || pad > len(out) {
		return nil, errPassword
	}
	for _, c := range out[len(out)-pad:] {
		if int(c) != pad {
			return nil, errPassword
		}
	}
	return out[:len(out)-pad], nil
}

func checkMAC(m macData, content, bmp []byte) error {
	var h func() hash.Hash
	switch {
	case m.Mac.Algorithm.Algorithm.Equal(oidSHA1):
		h = sha1.New
	case m.Mac.Algorithm.Algorithm.Equal(oidSHA256):
		h = sha256.New
	default:
		return nil // unknown MAC: rely on decryption to catch a wrong password
	}
	key := pkcs12KDF(h, 3, m.MacSalt, bmp, m.Iterations, h().Size())
	mac := hmac.New(h, key)
	mac.Write(content)
	if !hmac.Equal(mac.Sum(nil), m.Mac.Digest) {
		return errPassword
	}
	return nil
}

// pkcs12KDF is the key derivation of RFC 7292 appendix B.
func pkcs12KDF(h func() hash.Hash, id byte, salt, password []byte, iterations, size int) []byte {
	u := h().Size()
	v := h().BlockSize()
	D := bytes.Repeat([]byte{id}, v)
	fill := func(b []byte) []byte {
		if len(b) == 0 {
			return nil
		}
		n := v * ((len(b) + v - 1) / v)
		out := make([]byte, n)
		for i := range out {
			out[i] = b[i%len(b)]
		}
		return out
	}
	I := append(fill(salt), fill(password)...)
	var out []byte
	for len(out) < size {
		hh := h()
		hh.Write(D)
		hh.Write(I)
		A := hh.Sum(nil)
		for j := 1; j < iterations; j++ {
			hh = h()
			hh.Write(A)
			A = hh.Sum(nil)
		}
		out = append(out, A...)
		B := fill(A)
		one := big.NewInt(1)
		Bn := new(big.Int).SetBytes(B)
		for j := 0; j < len(I)/v; j++ {
			Ij := new(big.Int).SetBytes(I[j*v : (j+1)*v])
			Ij.Add(Ij, Bn)
			Ij.Add(Ij, one)
			b := Ij.Bytes()
			if len(b) > v {
				b = b[len(b)-v:]
			}
			blk := I[j*v : (j+1)*v]
			for k := range blk {
				blk[k] = 0
			}
			copy(blk[v-len(b):], b)
		}
		_ = u
	}
	return out[:size]
}

// keys for RSA/ECDSA are crypto.Signer; keep the imports used.
var _ = []any{(*rsa.PrivateKey)(nil), (*ecdsa.PrivateKey)(nil)}
