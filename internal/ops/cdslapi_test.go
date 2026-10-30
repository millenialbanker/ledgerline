package ops

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

// A synthetic 32-character key, as CDSL gives API keys.
const testCDSLKey = "0123456789abcdef0123456789abcdef"

func TestCDSLAESMatchesOpenSSL(t *testing.T) {
	// openssl enc -aes-256-cbc -K <hex of the key> -iv 00…00 -base64
	// over {"pan":"ABCDE1234F"}: CBC, PKCS#7, zero IV.
	const want = "VQJg0ucgXnxA1ub0qZOIPWnwhAMpNloZoPiPP/0x40s="
	key, err := cdslAESKey(testCDSLKey, "API key")
	if err != nil {
		t.Fatal(err)
	}
	iv, _ := cdslIV("")
	got, err := cdslEncrypt(key, iv, []byte(`{"pan":"ABCDE1234F"}`))
	if err != nil || got != want {
		t.Fatalf("encrypt: %s %v", got, err)
	}
	// The handbook's samples break the base64 over lines.
	plain, err := cdslDecrypt(key, iv, want[:20]+"\n  "+want[20:])
	if err != nil || string(plain) != `{"pan":"ABCDE1234F"}` {
		t.Fatalf("decrypt: %q %v", plain, err)
	}
	other, _ := cdslAESKey("ffffffffffffffffffffffffffffffff", "x")
	if _, err := cdslDecrypt(other, iv, want); err == nil {
		t.Error("a wrong key must not decrypt")
	}
}

func TestCDSLKeyForms(t *testing.T) {
	for _, s := range []string{testCDSLKey, "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", "3031323334353637383961626364656630313233343536373839616263646566"} {
		k, err := cdslAESKey(s, "key")
		if err != nil || string(k) != testCDSLKey {
			t.Errorf("%s: %q %v", s, k, err)
		}
	}
	if _, err := cdslAESKey("short", "key"); err == nil {
		t.Error("a short key must be refused")
	}
	if _, err := cdslIV("123"); err == nil {
		t.Error("a bad IV must be refused")
	}
}

func TestCDSLHashFormats(t *testing.T) {
	// SHA-256("abc"), FIPS 180-2.
	const hx = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if string(cdslHash([]byte("abc"), cdslHashHexLower)) != hx || string(cdslHash([]byte("abc"), cdslHashHexUpper)) != strings.ToUpper(hx) {
		t.Error("hex")
	}
	if string(cdslHash([]byte("abc"), cdslHashBase64)) != "ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0=" {
		t.Error("base64")
	}
	if len(cdslHash([]byte("abc"), cdslHashRaw)) != 32 {
		t.Error("raw")
	}
}

func TestCDSLMaskBO(t *testing.T) {
	if got := cdslMaskBO("1201090000000001"); got != strings.Repeat("•", 12)+"0001" {
		t.Error(got)
	}
}

// fakeCDSLConn saves a cdsl-api connection pointing at srv.
func fakeCDSLConn(t *testing.T, o *Ops, srvURL string, on ...string) {
	t.Helper()
	vals := map[string]string{"dp_id": "10900", "env": cdslEnvMock, "user": "APIUSER1", "url": srvURL, "callback_url": "https://bo.example.test"}
	for _, f := range on {
		vals[f] = "On"
	}
	c := Connection{Provider: connCDSL, Status: "Saved", Values: vals, Secrets: map[string]string{"password": "Synthetic@1", "secret": testCDSLKey, "api_key": testCDSLKey}}
	if err := docs.Upsert(t.Context(), o.Docs, T, cConn, connCDSL, c, "admin"); err != nil {
		t.Fatal(err)
	}
}

func useCDSLServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	old := cdslHTTP
	cdslHTTP = srv.Client()
	t.Cleanup(func() { cdslHTTP = old; srv.Close() })
	cdslTokMu.Lock()
	cdslToks = map[string]cdslTok{}
	cdslTokMu.Unlock()
	return srv
}

func TestCDSLCommonwebSignIn(t *testing.T) {
	o, ctx := newOps(t)
	key, _ := cdslAESKey(testCDSLKey, "k")
	iv, _ := cdslIV("")
	signIns := 0
	srv := useCDSLServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/" + cdslPathAuth:
			signIns++
			var body map[string]string
			json.Unmarshal(b, &body)
			pw, err := cdslDecrypt(key, iv, body["Password"])
			sign, err2 := cdslDecrypt(key, iv, r.Header.Get("AuthSign"))
			if err != nil || string(pw) != "Synthetic@1" || body["UserID"] != "APIUSER1" || body["DPID"] != "10900" || err2 != nil || string(sign) != string(cdslHash(b, cdslHashHexLower)) {
				w.Write([]byte(`{"Body":{"success":false,"AuthResponse":{"ErrorId":"ERR001","ErrorMessage":"Invalid User Id & Password Combination"}}}`))
				return
			}
			w.Write([]byte(`{"Body":{"success":true,"AuthToken":"TOK1"}}`))
		case "/Commonweb/api/CDASAPI/Status":
			sign, _ := cdslDecrypt(key, iv, r.Header.Get("AuthSign"))
			if r.Header.Get("AuthToken") != "TOK1" || string(sign) != string(cdslHash(b, cdslHashHexLower)) {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"success":true,"Status":"Completed"}`))
		}
	})
	fakeCDSLConn(t, o, srv.URL, cdslFamBO)
	x, ok := o.cdslConnFor(ctx, T)
	if !ok || !x.On(cdslFamBO) || x.On(cdslFamEDIS) {
		t.Fatal("connection")
	}
	for i := 0; i < 2; i++ {
		m, err := x.Common(ctx, T, cdslPathStatus, cdslJSON("AcknowledgmentID", "A1", "Signature", ""), nil, nil, nil)
		if err != nil || cdslStr(m, "Status") != "Completed" {
			t.Fatalf("status: %v %v", m, err)
		}
	}
	if signIns != 1 {
		t.Errorf("the token should be reused: %d sign-ins", signIns)
	}
	// A wrong password is reported in words.
	c, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connCDSL)
	c.Secrets["password"] = "wrong"
	docs.Upsert(ctx, o.Docs, T, cConn, connCDSL, c, "admin")
	x, _ = o.cdslConnFor(ctx, T)
	if _, err := x.Token(ctx, T, true); err == nil || !strings.Contains(err.Error(), "Invalid User Id") {
		t.Errorf("wrong password: %v", err)
	}
}
