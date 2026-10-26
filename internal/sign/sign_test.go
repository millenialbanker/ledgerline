package sign

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// testSigner makes a CA and a document-signing certificate issued by it.
func testSigner(t *testing.T, ec bool) (*Signer, []byte, []byte) {
	t.Helper()
	caKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test CA 2026"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	var key any
	var pub any
	if ec {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		key, pub = k, &k.PublicKey
	} else {
		k, _ := rsa.GenerateKey(rand.Reader, 2048)
		key, pub = k, &k.PublicKey
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(4711), Subject: pkix.Name{CommonName: "BHAIRAV SECURITIES PVT LTD"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, pub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	pemBytes := append(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...)
	pemBytes = append(pemBytes, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})...)
	s, err := Load(pemBytes, "")
	if err != nil {
		t.Fatal(err)
	}
	return s, pemBytes, caDER
}

func TestSignAndVerify(t *testing.T) {
	for _, ec := range []bool{false, true} {
		s, _, _ := testSigner(t, ec)
		if s.Cert.Subject.CommonName != "BHAIRAV SECURITIES PVT LTD" || len(s.Chain) != 1 {
			t.Fatalf("assembled %v chain %d", s.Cert.Subject, len(s.Chain))
		}
		doc := []byte("contract note 2026-10-16 for UCC A1001")
		at := time.Now()
		cms, err := s.SignDetached(doc, at)
		if err != nil {
			t.Fatal(err)
		}
		v, err := Verify(cms, doc)
		if err != nil {
			t.Fatalf("ec=%v verify: %v", ec, err)
		}
		if v.Signer.Name != "BHAIRAV SECURITIES PVT LTD" || v.SigningTime.Unix() != at.Unix() || v.Digest != "SHA-256" {
			t.Fatalf("verified %+v", v)
		}
		if _, err := Verify(cms, append(doc, '!')); err == nil {
			t.Fatal("tampered document verified")
		}
		bad := append([]byte(nil), cms...)
		bad[len(bad)-5] ^= 1
		if _, err := Verify(bad, doc); err == nil {
			t.Fatal("tampered signature verified")
		}
		// OpenSSL agrees.
		if _, err := exec.LookPath("openssl"); err == nil {
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "sig.der"), cms, 0o600)
			os.WriteFile(filepath.Join(dir, "doc"), doc, 0o600)
			out, err := exec.Command("openssl", "cms", "-verify", "-noverify", "-binary", "-inform", "DER", "-in", filepath.Join(dir, "sig.der"), "-content", filepath.Join(dir, "doc"), "-out", os.DevNull).CombinedOutput()
			if err != nil {
				t.Fatalf("openssl rejects the signature: %v %s", err, out)
			}
		}
	}
}

func TestCheckRefusesExpiredAndMismatched(t *testing.T) {
	s, _, _ := testSigner(t, false)
	if err := s.Check(time.Now().Add(72 * time.Hour)); err == nil {
		t.Fatal("expired certificate accepted")
	}
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	s2 := &Signer{Key: other, Cert: s.Cert}
	if err := s2.Check(time.Now()); err == nil {
		t.Fatal("mismatched key accepted")
	}
}

func TestPKCS12FromOpenSSL(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed")
	}
	_, pemBytes, _ := testSigner(t, false)
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pem")
	os.WriteFile(in, pemBytes, 0o600)
	for _, c := range []struct {
		name string
		args []string
	}{
		{"aes", nil},
		{"3des-rc2", []string{"-legacy"}},
		{"3des-3des", []string{"-keypbe", "PBE-SHA1-3DES", "-certpbe", "PBE-SHA1-3DES", "-macalg", "sha1"}},
	} {
		out := filepath.Join(dir, c.name+".pfx")
		args := append([]string{"pkcs12", "-export", "-in", in, "-out", out, "-passout", "pass:Dsc@2026"}, c.args...)
		if msg, err := exec.Command("openssl", args...).CombinedOutput(); err != nil {
			t.Logf("%s: openssl cannot make it here (%v %s)", c.name, err, bytes.TrimSpace(msg))
			continue
		}
		data, _ := os.ReadFile(out)
		s, err := Load(data, "Dsc@2026")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if s.Cert.SerialNumber.Int64() != 4711 || len(s.Chain) != 1 {
			t.Fatalf("%s: wrong certificate", c.name)
		}
		if _, err := Load(data, "wrong"); err == nil {
			t.Fatalf("%s: wrong password accepted", c.name)
		}
	}
}

func TestRC2Vectors(t *testing.T) {
	// RFC 2268 section 5.
	for _, v := range []struct {
		key  string
		bits int
		pt   string
		ct   string
	}{
		{"0000000000000000", 63, "0000000000000000", "ebb773f993278eff"},
		{"ffffffffffffffff", 64, "ffffffffffffffff", "278b27e42e2f0d49"},
		{"3000000000000000", 64, "1000000000000001", "30649edf9be7d2c2"},
		{"88bca90e90875a7f0f79c384627bafb2", 128, "0000000000000000", "2269552ab0f85ca6"},
	} {
		k, _ := hex.DecodeString(v.key)
		pt, _ := hex.DecodeString(v.pt)
		c := newRC2(k, v.bits)
		got := make([]byte, 8)
		c.Encrypt(got, pt)
		if hex.EncodeToString(got) != v.ct {
			t.Fatalf("encrypt %s: %x", v.key, got)
		}
		back := make([]byte, 8)
		c.Decrypt(back, got)
		if !bytes.Equal(back, pt) {
			t.Fatalf("decrypt %s", v.key)
		}
	}
}
