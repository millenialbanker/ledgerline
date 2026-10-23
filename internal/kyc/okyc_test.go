package kyc

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// The fixtures are made by testdata/make_fixtures.py with independent
// tools (lxml's canonicaliser, the zip command, a WinZip-AES writer), so
// these tests check this code against other implementations.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var okycAt = time.Date(2026, 1, 15, 12, 0, 0, 0, ist)

func TestOfflineKYC(t *testing.T) {
	certs, err := LoadCerts([]string{"testdata/uidai-test.pem"})
	if err != nil {
		t.Fatal(err)
	}
	if der, err := LoadCerts([]string{"testdata/uidai-test.der"}); err != nil || !der[0].Equal(certs[0]) {
		t.Fatalf("DER: %v", err)
	}
	for _, c := range []struct{ file, code string }{{"okyc-zipcrypto.zip", "1234"}, {"okyc-aes.zip", "5678"}} {
		o, err := VerifyOffline(fixture(t, c.file), c.code, certs, okycAt, "+91 98765 12345", "kavya@mail.example")
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if o.Name != "Kavya Iyer" || o.DOB != "1994-03-02" || o.Gender != "Female" || o.Last4 != "4322" || o.PIN != "560038" ||
			o.District != "Bengaluru Urban" || o.State != "Karnataka" || o.CareOf != "D/O Ramesh Iyer" || o.Source != "Aadhaar offline e-KYC" {
			t.Errorf("%s: %+v", c.file, o.Aadhaar)
		}
		if o.Address != "742, 12th Main & 3rd Cross, HAL 2nd Stage, Bengaluru, Bengaluru North, Indiranagar" {
			t.Errorf("address %q", o.Address)
		}
		if o.MobileMatch != "Matches" || o.EmailMatch != "Matches" || o.PhotoType != "image/jp2" || len(o.Photo) == 0 {
			t.Errorf("%s: mobile %s email %s photo %s", c.file, o.MobileMatch, o.EmailMatch, o.PhotoType)
		}
		if o.Generated != "2026-01-15T10:30:15+05:30" {
			t.Errorf("generated %s", o.Generated)
		}
	}
	// Someone else's mobile.
	o, err := VerifyOffline(fixture(t, "okyc-zipcrypto.zip"), "1234", certs, okycAt, "9999999999", "")
	if err != nil || o.MobileMatch != "Does not match" || o.EmailMatch != "" {
		t.Errorf("other mobile: %v %s %s", err, o.MobileMatch, o.EmailMatch)
	}
	// Last digit 0 hashes once; email compared as typed.
	o, err = VerifyOffline(fixture(t, "okyc-digit0.zip"), "1111", certs, okycAt, "9876512345", "Kavya@Mail.example")
	if err != nil || o.MobileMatch != "Matches" || o.EmailMatch != "Matches" {
		t.Errorf("digit 0: %v %s %s", err, o.MobileMatch, o.EmailMatch)
	}
	for _, c := range []struct {
		file, code string
		want       error
		at         time.Time
	}{
		{"okyc-zipcrypto.zip", "1235", ErrShareCode, okycAt},
		{"okyc-aes.zip", "1234", ErrShareCode, okycAt},
		{"okyc-tampered.zip", "1234", ErrBadSignature, okycAt},
		{"okyc-wrongkey.zip", "1234", ErrBadSignature, okycAt},
	} {
		if _, err := VerifyOffline(fixture(t, c.file), c.code, certs, c.at, "", ""); !errors.Is(err, c.want) {
			t.Errorf("%s/%s: %v, want %v", c.file, c.code, err, c.want)
		}
	}
	if _, err := VerifyOffline(fixture(t, "okyc-zipcrypto.zip"), "1234", certs, okycAt.Add(72*time.Hour), "", ""); err == nil || !strings.Contains(err.Error(), "3 days") {
		t.Errorf("stale file: %v", err)
	}
	if _, err := VerifyOffline(fixture(t, "okyc-zipcrypto.zip"), "1234", nil, okycAt, "", ""); err == nil {
		t.Error("verified without UIDAI's certificate")
	}
	if _, err := VerifyOffline([]byte("not a zip"), "1234", certs, okycAt, "", ""); err == nil {
		t.Error("garbage accepted")
	}
	if _, err := VerifyOffline(fixture(t, "okyc-zipcrypto.zip"), "12a4", certs, okycAt, "", ""); err == nil {
		t.Error("bad share code format")
	}
}

func TestOfflineXMLHardening(t *testing.T) {
	certs, _ := LoadCerts([]string{"testdata/uidai-test.pem"})
	x, err := OpenOfflineZip(fixture(t, "okyc-zipcrypto.zip"), "1234")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyXMLSignature(x, certs); err != nil {
		t.Fatal(err)
	}
	s := string(x)
	for name, bad := range map[string]string{
		"doctype":        strings.Replace(s, "<OfflinePaperlessKyc", `<!DOCTYPE x [<!ENTITY e "x">]><OfflinePaperlessKyc`, 1),
		"two signatures": strings.Replace(s, "</OfflinePaperlessKyc>", s[strings.Index(s, "<Signature"):strings.Index(s, "</Signature>")+12]+"</OfflinePaperlessKyc>", 1),
		"moved name":     strings.Replace(s, `name="Kavya Iyer"`, `name="Kavya  Iyer"`, 1),
		"wrapped":        strings.Replace(s, "<UidData>", "<UidData><Poi name=\"Mallory\"/>", 1),
	} {
		if _, err := VerifyXMLSignature([]byte(bad), certs); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Whitespace and attribute order inside the signed content are canonical: a
	// re-serialisation that only reorders attributes still verifies.
	re := strings.Replace(s, `name="Kavya Iyer" m=`, `m=`, 1)
	re = strings.Replace(re, `gender="F"`, `gender="F" name="Kavya Iyer"`, 1)
	if _, err := VerifyXMLSignature([]byte(re), certs); err != nil {
		t.Errorf("attribute order changed the canonical form: %v", err)
	}
}
