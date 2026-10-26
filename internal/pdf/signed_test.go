package pdf

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/sign"
)

func TestSignedPDF(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "BHAIRAV SECURITIES PVT LTD"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	s := &sign.Signer{Key: key, Cert: cert}
	at := time.Now()
	b, err := Render(sample()).Bytes(Meta{Title: "Contract note"}, &SignatureSpec{Name: "BHAIRAV SECURITIES PVT LTD", Reason: "Contract note", Location: "Mumbai", At: at,
		Sign: func(c []byte) ([]byte, error) { return s.SignDetached(c, at) }})
	if err != nil {
		t.Fatal(err)
	}
	sigs, err := Signatures(b)
	if err != nil || len(sigs) != 1 || !sigs[0].WholeDoc {
		t.Fatalf("signatures %v %+v", err, sigs)
	}
	v, err := sign.Verify(sigs[0].CMS, sigs[0].Content)
	if err != nil || v.Signer.Name != "BHAIRAV SECURITIES PVT LTD" {
		t.Fatalf("verify %v %+v", err, v)
	}
	// One changed byte in the visible content breaks it.
	bad := append([]byte(nil), b...)
	i := strings.Index(string(bad), "/Title (")
	bad[i+8] ^= 0x20
	sg, _ := Signatures(bad)
	if _, err := sign.Verify(sg[0].CMS, sg[0].Content); err == nil {
		t.Fatal("changed PDF still verifies")
	}
	if _, err := exec.LookPath("pdfsig"); err == nil {
		dir := t.TempDir()
		f := filepath.Join(dir, "s.pdf")
		os.WriteFile(f, b, 0o600)
		out, _ := exec.Command("pdfsig", f).CombinedOutput()
		if !strings.Contains(string(out), "Signature Validation: Signature is Valid") || !strings.Contains(string(out), "Total document signed") {
			t.Fatalf("pdfsig: %s", out)
		}
		os.WriteFile(f, bad, 0o600)
		out, _ = exec.Command("pdfsig", f).CombinedOutput()
		if strings.Contains(string(out), "Signature is Valid") {
			t.Fatalf("pdfsig accepts a changed file: %s", out)
		}
	}
	if _, err := exec.LookPath("qpdf"); err == nil {
		dir := t.TempDir()
		f := filepath.Join(dir, "s.pdf")
		os.WriteFile(f, b, 0o600)
		if out, err := exec.Command("qpdf", "--check", f).CombinedOutput(); err != nil {
			t.Fatalf("qpdf: %v %s", err, out)
		}
	}
}
