package kyc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/vault"
)

const testKey = "test-key-0123456789abcdefghij"
const provKey = "provider-key-0123456789abcdef"

type rig struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	hc     *http.Client
	method string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	srv := &Server{Simulate: true, Store: &Store{Dir: t.TempDir(), V: vault.New(vault.Ephemeral())}}
	srv.Cfg.ProviderKeySHA256 = sha(provKey)
	srv.Cfg.Customers = []Customer{{ID: "acme", Name: "Acme Securities", APIKeySHA256: sha(testKey),
		DigiLocker: DigiLocker{ClientID: "ACME1", ClientSecret: "s3cret"}, ReturnHosts: []string{"bo.acme.in"}}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	srv.Cfg.PublicURL = ts.URL
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &rig{t, srv, ts, hc, MethodDigiLocker}
}

func (r *rig) api(method, path, key string, body any) (int, map[string]any) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, r.ts.URL+path, rd)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func (r *rig) create(hint Hint) (id, link string) {
	r.t.Helper()
	code, m := r.api("POST", "/v1/sessions", testKey, map[string]any{"reference": "APP-1", "return_url": "https://bo.acme.in/open/acme?dl=back", "hint": hint, "method": r.method})
	if code != 201 {
		r.t.Fatalf("create: %d %v", code, m)
	}
	return m["id"].(string), m["url"].(string)
}

// walk follows the investor through the start link and practice consent,
// returning where they end up.
func (r *rig) walk(link string, allow bool, tamper func(*url.Values)) *url.URL {
	r.t.Helper()
	resp, err := r.hc.Get(link)
	if err != nil || resp.StatusCode != 302 {
		r.t.Fatalf("start: %v %v", err, resp.Status)
	}
	auth, _ := url.Parse(resp.Header.Get("Location"))
	q := auth.Query()
	if tamper != nil {
		tamper(&q)
	}
	a := "0"
	if allow {
		a = "1"
	}
	resp, err = r.hc.PostForm(r.ts.URL+"/sim/digilocker/public/oauth2/1/consent", url.Values{"q": {q.Encode()}, "allow": {a}})
	if err != nil || resp.StatusCode != 302 {
		r.t.Fatalf("consent: %v %v", err, resp.Status)
	}
	resp, err = r.hc.Get(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != 302 {
		r.t.Fatalf("callback: %v %v", err, resp.Status)
	}
	back, _ := url.Parse(resp.Header.Get("Location"))
	return back
}

func TestFullFlow(t *testing.T) {
	r := newRig(t)
	id, link := r.create(Hint{Name: "Kavya Iyengar", DOB: "1991-04-12", PAN: "abcpi1234k"})
	if code, m := r.api("GET", "/v1/sessions/"+id, testKey, nil); code != 200 || m["status"] != "Pending" {
		t.Fatalf("pending: %d %v", code, m)
	}
	back := r.walk(link, true, nil)
	if back.Host != "bo.acme.in" || back.Query().Get("kyc") != id || back.Query().Get("kyc_status") != "Completed" {
		t.Fatalf("back: %s", back)
	}
	code, m := r.api("GET", "/v1/sessions/"+id, testKey, nil)
	if code != 200 || m["status"] != "Completed" {
		t.Fatalf("read: %d %v", code, m)
	}
	for _, k := range []string{"token_hash", "nonce", "verifier"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s leaked to the API", k)
		}
	}
	a := m["aadhaar"].(map[string]any)
	if a["name"] != "Kavya Iyengar" || a["dob"] != "1991-04-12" || a["pin"] == "" || a["photo"] == nil {
		t.Errorf("aadhaar: %v", a)
	}
	if p := m["pan"].(map[string]any); p["number"] != "ABCPI1234K" {
		t.Errorf("pan: %v", p)
	}
	for _, c := range m["checks"].([]any) {
		if c := c.(map[string]any); c["ok"] != true {
			t.Errorf("check failed: %v", c)
		}
	}
	// The link is single use.
	if resp, _ := r.hc.Get(link); resp.StatusCode != 410 {
		t.Errorf("reused link: %d", resp.StatusCode)
	}
	// Another customer's key cannot read it; a wrong key is refused.
	if code, _ := r.api("GET", "/v1/sessions/"+id, "wrong-key-0123456789abcdefgh", nil); code != 401 {
		t.Errorf("wrong key: %d", code)
	}
	if code, _ := r.api("GET", "/v1/sessions/"+id, "", nil); code != 401 {
		t.Errorf("no key: %d", code)
	}
	// Usage counts.
	if _, u := r.api("GET", "/v1/usage", testKey, nil); u["started"] != 1.0 || u["completed"] != 1.0 {
		t.Errorf("usage: %v", u)
	}
	req, _ := http.NewRequest("GET", r.ts.URL+"/provider/usage?format=csv", nil)
	req.Header.Set("Authorization", "Bearer "+provKey)
	resp, _ := r.hc.Do(req)
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "acme,Acme Securities,"+time.Now().Format("2006-01")+",1,1,0") {
		t.Errorf("provider csv: %s", b)
	}
	if code, _ := r.api("GET", "/provider/usage", testKey, nil); code != 401 {
		t.Errorf("customer key read provider usage: %d", code)
	}
	// Delete removes everything.
	if code, _ := r.api("DELETE", "/v1/sessions/"+id, testKey, nil); code != 200 {
		t.Fatal("delete")
	}
	if code, _ := r.api("GET", "/v1/sessions/"+id, testKey, nil); code != 404 {
		t.Error("still there after delete")
	}
}

func TestMismatchIsReported(t *testing.T) {
	r := newRig(t)
	id, link := r.create(Hint{Name: "Kavya Iyengar", DOB: "1991-04-12", PAN: "ABCPI1234K"})
	// The investor's DigiLocker has a different PAN from the one typed.
	r.srv.sim.Hint = func(string) (Hint, bool) {
		return Hint{Name: "Kavya Iyengar", DOB: "1991-04-12", PAN: "ZZZPI9999Z"}, true
	}
	r.walk(link, true, nil)
	_, m := r.api("GET", "/v1/sessions/"+id, testKey, nil)
	bad := false
	for _, c := range m["checks"].([]any) {
		if c := c.(map[string]any); c["name"] == "PAN number" && c["ok"] == false {
			bad = true
		}
	}
	if !bad || strings.Contains(m["checks"].([]any)[0].(map[string]any)["detail"].(string), "ZZZPI9999Z") {
		t.Errorf("PAN mismatch not reported (or not masked): %v", m["checks"])
	}
}

func TestDenyAndBadPKCE(t *testing.T) {
	r := newRig(t)
	id, link := r.create(Hint{Name: "A B"})
	if back := r.walk(link, false, nil); back.Query().Get("kyc_status") != "Failed" {
		t.Fatalf("deny: %s", back)
	}
	if _, m := r.api("GET", "/v1/sessions/"+id, testKey, nil); m["error"] != "consent denied in DigiLocker" {
		t.Errorf("deny: %v", m)
	}
	// A code issued for a different PKCE challenge cannot be exchanged.
	id, link = r.create(Hint{Name: "A B"})
	// Change the first character to a different one: a fixed "x" would
	// leave a challenge that already starts with x unchanged (1 in 64).
	r.walk(link, true, func(q *url.Values) {
		c := q.Get("code_challenge")
		first := "x"
		if c[0] == 'x' {
			first = "y"
		}
		q.Set("code_challenge", first+c[1:])
	})
	if _, m := r.api("GET", "/v1/sessions/"+id, testKey, nil); m["status"] != "Failed" || !strings.Contains(m["error"].(string), "refused the code") {
		t.Errorf("pkce: %v", m)
	}
	// A forged state is refused.
	resp, _ := r.hc.Get(r.ts.URL + "/callback?state=acme." + id + ".forged&code=x")
	if resp.StatusCode != 400 {
		t.Errorf("forged state: %d", resp.StatusCode)
	}
}

func TestReturnURLAndSetup(t *testing.T) {
	r := newRig(t)
	r.srv.Simulate = false // production rules: https on a registered host only
	for _, u := range []string{"https://evil.example/x", "http://bo.acme.in/x", "javascript:alert(1)", ""} {
		if code, _ := r.api("POST", "/v1/sessions", testKey, map[string]any{"return_url": u}); code != 400 {
			t.Errorf("%q allowed: %d", u, code)
		}
	}
	if code, _ := r.api("POST", "/v1/sessions", testKey, map[string]any{"return_url": "https://bo.acme.in/open"}); code != 201 {
		t.Errorf("registered host refused: %d", code)
	}
	r.srv.Cfg.Customers[0].DigiLocker.ClientSecret = ""
	if code, _ := r.api("POST", "/v1/sessions", testKey, map[string]any{"return_url": "https://bo.acme.in/open"}); code != 409 {
		t.Errorf("no DigiLocker registration: %d", code)
	}
}

func TestHMACTamper(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("hmac", "AAAA")
		w.Write([]byte("<Certificate/>"))
	}))
	defer ts.Close()
	d := DigiLocker{ClientID: "c", ClientSecret: "s", Base: ts.URL}
	if _, err := d.EAadhaar(context.Background(), "tok"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "integrity") && !strings.Contains(strings.ToLower(err.Error()), "hmac") {
		t.Errorf("tampered body accepted: %v", err)
	}
}

func TestExpiryAndPurge(t *testing.T) {
	r := newRig(t)
	now := time.Now()
	r.srv.Now = func() time.Time { return now }
	id, link := r.create(Hint{})
	now = now.Add(31 * time.Minute)
	if _, m := r.api("GET", "/v1/sessions/"+id, testKey, nil); m["status"] != "Expired" {
		t.Errorf("not expired: %v", m)
	}
	if resp, _ := r.hc.Get(link); resp.StatusCode != 410 {
		t.Errorf("expired link: %d", resp.StatusCode)
	}
	if n, _ := r.srv.Store.Purge(now, r.srv.Retention()); n != 0 {
		t.Error("purged too early")
	}
	if n, _ := r.srv.Store.Purge(now.Add(2*time.Hour), r.srv.Retention()); n != 1 {
		t.Errorf("purge: %d", n)
	}
	// Files on disk are encrypted.
	r.srv.Now = nil
	id, link = r.create(Hint{Name: "Kavya Iyengar", PAN: "ABCPI1234K"})
	r.walk(link, true, nil)
	b, _ := os.ReadFile(filepath.Join(r.srv.Store.Dir, "sessions", "acme", id+".bin"))
	if len(b) == 0 || strings.Contains(string(b), "Iyengar") || strings.Contains(string(b), "ABCPI") {
		t.Error("session stored in the clear")
	}
	if n, _ := r.srv.Store.Purge(time.Now().Add(73*time.Hour), r.srv.Retention()); n != 1 {
		t.Errorf("finished not purged after retention: %d", n)
	}
}

func TestParse(t *testing.T) {
	a, err := ParseEAadhaar([]byte(`<?xml version="1.0"?><Certificate><CertificateData><KycRes><UidData uid="xxxxxxxx4321">
<Poi name="Rahul Sharma" dob="05-11-1988" gender="M"/><Poa co="S/O Anil Sharma" house="12" street="MG Road" loc="Shivaji Nagar" vtc="Pune" dist="Pune" state="Maharashtra" pc="411005"/>
<Pht>/9j/</Pht></UidData></KycRes></CertificateData></Certificate>`))
	if err != nil || a.Name != "Rahul Sharma" || a.DOB != "1988-11-05" || a.Last4 != "4321" || a.PIN != "411005" || a.Gender != "Male" || !strings.Contains(a.Address, "MG Road") {
		t.Errorf("aadhaar: %+v %v", a, err)
	}
	p, err := ParsePAN([]byte(`<Certificate number="ABCPS1234D"><IssuedTo><Person name="RAHUL SHARMA" dob="05-11-1988"/></IssuedTo></Certificate>`))
	if err != nil || p.Number != "ABCPS1234D" || !NamesMatch(p.Name, "Rahul Sharma") {
		t.Errorf("pan: %+v %v", p, err)
	}
	if _, err := ParsePAN([]byte(`<Certificate number="NOTAPAN"/>`)); err == nil {
		t.Error("bad PAN accepted")
	}
}
