package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	// Return URLs elsewhere are refused by the plugin.
	if _, _, err := o.KYC.(*PluginKYC).StartDigiLocker(ctx, "x", "https://evil.example/", DigiLockerHint{}); err == nil {
		t.Error("foreign return URL accepted")
	}
}
