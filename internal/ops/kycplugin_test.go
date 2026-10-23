package ops

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"ledgerline/internal/kyc"
	"ledgerline/internal/vault"
)

// TestKYCPluginEndToEnd runs an online application through the real
// Ledgerline KYC plugin and its practice DigiLocker.
func TestKYCPluginEndToEnd(t *testing.T) {
	const key = "firm-key-0123456789abcdefghijkl"
	h := sha256.Sum256([]byte(key))
	srv := &kyc.Server{Simulate: true, Store: &kyc.Store{Dir: t.TempDir(), V: vault.New(vault.Ephemeral())}}
	srv.Cfg.Customers = []kyc.Customer{{ID: "demo", Name: "Demo", APIKeySHA256: hex.EncodeToString(h[:]),
		DigiLocker: kyc.DigiLocker{ClientID: "DEMO", ClientSecret: "secret"}, ReturnHosts: []string{"broker.example"}}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.Handler().ServeHTTP(w, r) }))
	defer ts.Close()
	srv.Cfg.PublicURL = ts.URL

	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	walkDigiLocker = func(t *testing.T, link string) {
		resp, err := hc.Get(link)
		if err != nil || resp.StatusCode != 302 {
			t.Fatalf("start: %v", err)
		}
		auth, _ := url.Parse(resp.Header.Get("Location"))
		resp, err = hc.PostForm(ts.URL+"/sim/digilocker/public/oauth2/1/consent", url.Values{"q": {auth.Query().Encode()}, "allow": {"1"}})
		if err != nil || resp.StatusCode != 302 {
			t.Fatalf("consent: %v", err)
		}
		resp, err = hc.Get(resp.Header.Get("Location"))
		if err != nil || resp.StatusCode != 302 {
			t.Fatalf("callback: %v", err)
		}
		if back, _ := url.Parse(resp.Header.Get("Location")); back.Query().Get("kyc_status") != "Completed" {
			t.Fatalf("back %s", back)
		}
	}
	defer func() { walkDigiLocker = nil }()

	o, ctx := setup(t)
	o.DevCodes = true
	o.SetEdition(T, EditionFull)
	o.KYC = &PluginKYC{URL: ts.URL, Key: key, Base: simKYC{}}
	a, err := apply(t, o, ctx, "9876500041", "ABCPI1241K", true)
	if err != nil {
		t.Fatal(err)
	}
	if a.AddrSource != "DigiLocker (Aadhaar)" || !a.hasDoc("Aadhaar (DigiLocker)") || !a.hasDoc("PAN (DigiLocker)") || !a.hasDoc("Aadhaar photo (DigiLocker)") {
		t.Fatalf("documents not taken from DigiLocker: %+v", a.Docs)
	}
	c, _ := o.Client(ctx, T, a.UCC)
	if c.Status != "Active" || c.ActivatedBy != "system:autopilot" || !a.Activated {
		t.Fatalf("not activated: %s; blockers %v; checks %+v", c.Status, a.blockers(), a.Checks)
	}
	// The plugin deleted the session once Ledgerline had read it.
	if _, err := srv.Store.Get("demo", a.DLSession); err != kyc.ErrNoSession {
		t.Errorf("plugin kept the documents: %v", err)
	}
	// The same, with an Aadhaar offline e-KYC file instead of DigiLocker.
	if ms := o.aadhaarMethods(ctx); len(ms) != 2 || ms[1] != AadhaarOffline {
		t.Fatalf("methods %v", ms)
	}
	walkDigiLocker = func(t *testing.T, link string) {
		resp, err := hc.Get(link)
		if err != nil || resp.StatusCode != 302 {
			t.Fatalf("start: %v", err)
		}
		off, _ := url.Parse(resp.Header.Get("Location"))
		resp, err = hc.Get(ts.URL + "/sim/okyc" + strings.TrimPrefix(off.Path, "/offline") + "?" + off.RawQuery)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("practice file: %v", err)
		}
		zip, _ := io.ReadAll(resp.Body)
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("file", "offline.zip")
		fw.Write(zip)
		mw.WriteField("code", "1234")
		mw.Close()
		resp, err = hc.Post(off.String(), mw.FormDataContentType(), &b)
		if err != nil || resp.StatusCode != 302 {
			t.Fatalf("upload: %v %d", err, resp.StatusCode)
		}
		if back, _ := url.Parse(resp.Header.Get("Location")); back.Query().Get("kyc_status") != "Completed" {
			t.Fatalf("back %s", back)
		}
	}
	applyMethod = AadhaarOffline
	defer func() { applyMethod = AadhaarDigiLocker }()
	b, err := apply(t, o, ctx, "9876500042", "ABCPI1242K", true)
	if err != nil {
		t.Fatal(err)
	}
	if b.AddrSource != "Aadhaar offline e-KYC" || !b.hasDoc("Aadhaar (offline e-KYC)") || !b.hasDoc("Aadhaar photo (offline e-KYC)") || b.hasDoc("PAN (DigiLocker)") || !b.hasDoc("PAN card") {
		t.Fatalf("offline documents: %s %+v", b.AddrSource, b.Docs)
	}
	var chk AppCheck
	for _, c := range b.Checks {
		if c.Name == "Aadhaar (offline e-KYC)" {
			chk = c
		}
	}
	if chk.Status != "Passed" || !strings.Contains(chk.Detail, "mobile is linked") {
		t.Fatalf("offline check %+v", chk)
	}
	c2, _ := o.Client(ctx, T, b.UCC)
	if c2.Status != "Active" || c2.KYCRoute != "Online (Aadhaar offline e-KYC from UIDAI)" {
		t.Fatalf("offline client: %s %s; blockers %v", c2.Status, c2.KYCRoute, b.blockers())
	}

	// Return URLs elsewhere are refused by the plugin.
	if _, _, err := o.KYC.(*PluginKYC).StartDigiLocker(ctx, "x", "https://evil.example/", DigiLockerHint{}); err == nil {
		t.Error("foreign return URL accepted")
	}
}
