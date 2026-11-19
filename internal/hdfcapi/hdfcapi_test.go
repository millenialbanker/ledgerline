package hdfcapi_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/hdfcapi"
	"ledgerline/internal/hdfcapi/hdfctest"
)

func key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestKIDIsSHA256OfSPKI(t *testing.T) {
	k := key(t)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	h := sha256.Sum256(der)
	got, err := hdfcapi.KID(&k.PublicKey)
	if err != nil || got != base64.RawURLEncoding.EncodeToString(h[:]) || strings.ContainsAny(got, "=+/") {
		t.Fatalf("kid %q %v", got, err)
	}
}

func TestNestedJWTRoundTrip(t *testing.T) {
	firm, bank := key(t), key(t)
	fk, _ := hdfcapi.KID(&firm.PublicKey)
	bk, _ := hdfcapi.KID(&bank.PublicKey)
	msg := []byte(`{"Bank":"HDFC","amount":"1500.00"}`)
	jws, err := hdfcapi.Sign(msg, firm, fk)
	if err != nil {
		t.Fatal(err)
	}
	hdr, _ := base64.RawURLEncoding.DecodeString(strings.Split(jws, ".")[0])
	if string(hdr) != `{"typ":"JWS","kid":"`+fk+`","alg":"RS256"}` {
		t.Fatalf("JWS header %s", hdr)
	}
	jwe, err := hdfcapi.Encrypt([]byte(jws), &bank.PublicKey, bk)
	if err != nil {
		t.Fatal(err)
	}
	hdr, _ = base64.RawURLEncoding.DecodeString(strings.Split(jwe, ".")[0])
	if string(hdr) != `{"alg":"RSA-OAEP-256","enc":"A256GCM","typ":"JWE","kid":"`+bk+`"}` || len(strings.Split(jwe, ".")) != 5 {
		t.Fatalf("JWE header %s", hdr)
	}
	in, err := hdfcapi.Decrypt(jwe, bank, bk)
	if err != nil || string(in) != jws {
		t.Fatalf("decrypt %v", err)
	}
	out, err := hdfcapi.Verify(string(in), &firm.PublicKey, fk)
	if err != nil || string(out) != string(msg) {
		t.Fatalf("verify %q %v", out, err)
	}

	// Wrong key, wrong kid, changed bytes: all refused.
	other := key(t)
	if _, err := hdfcapi.Decrypt(jwe, other, bk); err == nil {
		t.Fatal("opened with another key")
	}
	if _, err := hdfcapi.Decrypt(jwe, bank, "someone-else"); err == nil || !strings.Contains(err.Error(), "not the firm's key") {
		t.Fatalf("kid not checked: %v", err)
	}
	p := strings.Split(jwe, ".")
	ct, _ := base64.RawURLEncoding.DecodeString(p[3])
	ct[0] ^= 1
	p[3] = base64.RawURLEncoding.EncodeToString(ct)
	if _, err := hdfcapi.Decrypt(strings.Join(p, "."), bank, bk); err == nil {
		t.Fatal("changed ciphertext accepted")
	}
	if _, err := hdfcapi.Verify(jws, &other.PublicKey, fk); err == nil {
		t.Fatal("signature by another key accepted")
	}
	s := strings.Split(jws, ".")
	s[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"Bank":"HDFC","amount":"9999.00"}`))
	if _, err := hdfcapi.Verify(strings.Join(s, "."), &firm.PublicKey, fk); err == nil {
		t.Fatal("changed payload accepted")
	}
	// A JWE with another algorithm is refused before any key use.
	h2 := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RSA1_5","enc":"A128CBC-HS256","kid":"` + bk + `"}`))
	if _, err := hdfcapi.Decrypt(h2+".a.b.c.d", bank, bk); err == nil || !strings.Contains(err.Error(), "not RSA-OAEP-256") {
		t.Fatalf("algorithm not checked: %v", err)
	}

	// For the independent check (python, testdata/josecheck.py): the
	// keys and a message both ways, when asked for.
	if dir := os.Getenv("HDFCJOSE_DUMP"); dir != "" {
		pk := func(k *rsa.PrivateKey) string {
			return base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(k))
		}
		_ = os.WriteFile(dir+"/vectors.json", mustJSON(map[string]string{"firm": pk(firm), "bank": pk(bank), "fkid": fk, "bkid": bk, "jwe": jwe, "msg": string(msg)}), 0o600)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestClientAgainstFakeBank(t *testing.T) {
	firm := key(t)
	b := hdfctest.New(&firm.PublicKey)
	defer b.Close()
	bankKey, err := hdfcapi.BankPublicKey(b.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	c, err := hdfcapi.New(hdfcapi.Config{Base: b.Base(), APIKey: b.APIKey, Secret: b.Secret, Scope: b.Scope, Key: firm, BankKey: bankKey, HTTP: b.Server.Client(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pay := func(ref string, typ string) hdfcapi.Payment {
		return hdfcapi.Payment{Type: typ, BeneCode: "C001", Account: "50100012345678", Amount: "1500.00", Name: "Asha Rao", CustRef: ref, Date: "19/11/2026", IFSC: "ICIC0000056"}
	}
	r, err := c.SendBatch(ctx, "", hdfcapi.BatchRequest{ClientCode: "ABCD", GroupID: "ABCDG", Maker: "APIUSER", Count: "2", FileRef: "000001", Payments: []hdfcapi.Payment{pay("00000000000001", "N"), pay("00000000000002", "I")}})
	if err != nil || !r.Accepted() || r.BatchNum == "" {
		t.Fatalf("send %+v %v", r, err)
	}
	// The same file reference again on the same day is refused by the bank.
	_, err = c.SendBatch(ctx, "", hdfcapi.BatchRequest{ClientCode: "ABCD", GroupID: "ABCDG", Maker: "APIUSER", Count: "1", FileRef: "000001", Payments: []hdfcapi.Payment{pay("00000000000003", "N")}})
	var ae *hdfcapi.APIError
	if !errors.As(err, &ae) || !ae.Sure() || !strings.Contains(ae.Reason, "Duplicate") {
		t.Fatalf("duplicate file reference %v", err)
	}
	q := hdfcapi.InquiryRequest{GroupID: "ABCDG", Maker: "APIUSER", BatchNum: string(r.BatchNum), Date: "19/11/2026", FileRef: "000001"}
	for i, want := range []string{"accepted your request", "under process"} {
		x, err := c.Inquire(ctx, q)
		if err != nil || !strings.Contains(string(x.Message), want) || len(x.Lines) != 0 {
			t.Fatalf("inquiry %d %+v %v", i+1, x, err)
		}
	}
	b.Set("00000000000001", "C", "HDFCN52026111900001", "", "TXSETT")
	b.Set("00000000000002", "R", "", "Account closed", "")
	x, err := c.Inquire(ctx, q)
	if err != nil || len(x.Lines) != 2 || x.FileRef != "1" {
		t.Fatalf("inquiry 3 %+v %v", x, err)
	}
	if l := x.Lines[0]; l.Status != "C" || l.UTR != "HDFCN52026111900001" || l.Amount != "1500.00" {
		t.Fatalf("line 1 %+v", l)
	}
	if l := x.Lines[1]; l.Status != "R" || l.Reason != "Account closed" {
		t.Fatalf("line 2 %+v", l)
	}
	n, err := c.BatchNumber(ctx, hdfcapi.BatchNumberRequest{GroupID: "ABCDG", Maker: "APIUSER", Date: "19/11/2026", FileRef: "000001"})
	if err != nil || n.BatchNum != r.BatchNum {
		t.Fatalf("batch number %+v %v", n, err)
	}
	n, err = c.BatchNumber(ctx, hdfcapi.BatchNumberRequest{GroupID: "ABCDG", Maker: "APIUSER", Date: "19/11/2026", FileRef: "000009"})
	if err != nil || n.BatchNum != "" || !strings.Contains(string(n.Message), "No record") {
		t.Fatalf("unknown file reference %+v %v", n, err)
	}
	// One token for all of it, until it is near its expiry.
	if b.Tokens() != 1 {
		t.Fatalf("%d tokens", b.Tokens())
	}
	now = now.Add(15 * time.Minute)
	if _, err := c.Inquire(ctx, q); err != nil || b.Tokens() != 2 {
		t.Fatalf("token not renewed: %d %v", b.Tokens(), err)
	}

	// A wrong secret is the bank's refusal, with its reason.
	bad, _ := hdfcapi.New(hdfcapi.Config{Base: b.Base(), APIKey: b.APIKey, Secret: "wrong", Scope: b.Scope, Key: firm, BankKey: bankKey, HTTP: b.Server.Client()})
	if _, err := bad.Inquire(ctx, q); !errors.As(err, &ae) || ae.Status != 401 || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("wrong secret %v", err)
	}
	// An answer for another key: the firm's kid is checked.
	other := key(t)
	wrong, _ := hdfcapi.New(hdfcapi.Config{Base: b.Base(), APIKey: b.APIKey, Secret: b.Secret, Scope: b.Scope, Key: other, BankKey: bankKey, HTTP: b.Server.Client()})
	if _, err := wrong.Inquire(ctx, q); err == nil || !errors.As(err, &ae) || !strings.Contains(ae.Reason, "signature") {
		t.Fatalf("a request signed by another key was taken: %v", err)
	}
}

func TestCertificates(t *testing.T) {
	firm := key(t)
	b := hdfctest.New(&firm.PublicKey)
	defer b.Close()
	if _, err := hdfcapi.TLSCertificate(b.CertPEM, firm); err == nil || !strings.Contains(err.Error(), "not for its private key") {
		t.Fatalf("a certificate for another key was paired: %v", err)
	}
	if _, err := hdfcapi.BankPublicKey("nonsense"); err == nil {
		t.Fatal("nonsense read as a certificate")
	}
	if _, err := hdfcapi.ParsePrivateKey(b.CertPEM); err == nil {
		t.Fatal("a certificate read as a private key")
	}
}

// TestIndependentMessage opens a message made by another implementation
// (python's cryptography and PyJWT; testdata/josecheck.py), when given.
func TestIndependentMessage(t *testing.T) {
	path := os.Getenv("HDFCJOSE_IN")
	if path == "" {
		t.Skip("HDFCJOSE_IN not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	_ = json.Unmarshal(raw, &v)
	der := func(s string) *rsa.PrivateKey {
		b, _ := base64.StdEncoding.DecodeString(s)
		k, err := x509.ParsePKCS1PrivateKey(b)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	firm, bank := der(v["firm"]), der(v["bank"])
	jws, err := hdfcapi.Decrypt(v["py_jwe"], firm, v["fkid"])
	if err != nil {
		t.Fatal(err)
	}
	msg, err := hdfcapi.Verify(string(jws), &bank.PublicKey, v["bkid"])
	if err != nil || string(msg) != v["py_msg"] {
		t.Fatalf("%q %v", msg, err)
	}
}
