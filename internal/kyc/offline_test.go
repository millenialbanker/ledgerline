package kyc

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func (r *rig) upload(link string, zip []byte, code string) *http.Response {
	r.t.Helper()
	u, _ := url.Parse(link)
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, _ := mw.CreateFormFile("file", "offlineaadhaar.zip")
	fw.Write(zip)
	mw.WriteField("code", code)
	mw.Close()
	resp, err := r.hc.Post(r.ts.URL+u.Path+"?"+u.RawQuery, mw.FormDataContentType(), &b)
	if err != nil {
		r.t.Fatal(err)
	}
	return resp
}

func body(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(b)
}

// offlineLink opens the start link and follows it to the upload page.
func (r *rig) offlineLink(link string) string {
	r.t.Helper()
	resp, err := r.hc.Get(link)
	if err != nil || resp.StatusCode != 302 {
		r.t.Fatalf("start: %v %v", err, resp.Status)
	}
	return resp.Header.Get("Location")
}

func TestChooserAndPracticeFile(t *testing.T) {
	r := newRig(t)
	r.method = ""
	_, link := r.create(Hint{Name: "Kavya Iyer", DOB: "1994-03-02", Mobile: "9876512345", Email: "kavya@mail.example"})
	resp, _ := r.hc.Get(link)
	page := body(resp)
	if resp.StatusCode != 200 || !strings.Contains(page, "DigiLocker") || !strings.Contains(page, "offline e-KYC") {
		t.Fatalf("chooser: %d", resp.StatusCode)
	}
	// Choosing the file route.
	resp, _ = r.hc.Get(link + "&via=" + MethodOffline)
	off := resp.Header.Get("Location")
	if resp.StatusCode != 302 || !strings.Contains(off, "/offline/acme/") {
		t.Fatalf("via offline: %d %s", resp.StatusCode, off)
	}
	resp, _ = r.hc.Get(off)
	pg := body(resp)
	if !strings.Contains(pg, "share code") || !strings.Contains(pg, "Use DigiLocker instead") {
		t.Fatal("upload page")
	}
	u, _ := url.Parse(off)
	resp, _ = r.hc.Get(r.ts.URL + "/sim/okyc" + strings.TrimPrefix(u.Path, "/offline") + "?" + u.RawQuery)
	zip, _ := io.ReadAll(resp.Body)
	// The practice file verifies like a real one, with the investor's own mobile and email.
	o, err := VerifyOffline(zip, "1234", r.srv.certs(), time.Now(), "9876512345", "kavya@mail.example")
	if err != nil || o.Name != "Kavya Iyer" || o.MobileMatch != "Matches" || o.EmailMatch != "Matches" {
		t.Fatalf("practice file: %v %+v", err, o.Aadhaar)
	}
	back := r.upload(off, zip, "1234")
	if loc, _ := url.Parse(back.Header.Get("Location")); back.StatusCode != 302 || loc.Query().Get("kyc_status") != "Completed" {
		t.Fatalf("upload: %d %s", back.StatusCode, body(back))
	}
	id := strings.Split(strings.TrimPrefix(u.Path, "/offline/acme/"), "?")[0]
	_, m := r.api("GET", "/v1/sessions/"+id, testKey, nil)
	a := m["aadhaar"].(map[string]any)
	if m["method"] != MethodOffline || a["source"] != "Aadhaar offline e-KYC" || a["mobile_match"] != "Matches" || m["pan"] != nil {
		t.Fatalf("result: %v", m)
	}
	for _, c := range m["checks"].([]any) {
		if c := c.(map[string]any); c["ok"] != true {
			t.Errorf("check: %v", c)
		}
	}
	if _, u := r.api("GET", "/v1/usage", testKey, nil); u["completed"] != 1.0 || u["offline"] != 1.0 {
		t.Errorf("usage: %v", u)
	}
}

func TestOfflineUploadRealFixture(t *testing.T) {
	r := newRig(t)
	r.srv.Simulate = false
	r.srv.Cfg.PublicURL = r.ts.URL
	certs, _ := LoadCerts([]string{"testdata/uidai-test.pem"})
	r.srv.Certs = certs
	r.srv.Now = func() time.Time { return okycAt }
	r.method = MethodOffline
	id, link := r.create(Hint{Name: "Kavya Iyer", DOB: "1994-03-02", Mobile: "8888888888", Email: "someone@else.example"})
	off := r.offlineLink(link)
	// Wrong share codes count, and the page says how many tries are left.
	for i := 1; i <= 4; i++ {
		resp := r.upload(off, fixture(t, "okyc-zipcrypto.zip"), "0000")
		if pg := body(resp); resp.StatusCode != 422 || !strings.Contains(pg, "does not open this file") || !strings.Contains(pg, "left") {
			t.Fatalf("attempt %d: %d", i, resp.StatusCode)
		}
	}
	resp := r.upload(off, fixture(t, "okyc-zipcrypto.zip"), "1234")
	if loc, _ := url.Parse(resp.Header.Get("Location")); loc.Query().Get("kyc_status") != "Completed" {
		t.Fatalf("5th try with the right code: %d", resp.StatusCode)
	}
	_, m := r.api("GET", "/v1/sessions/"+id, testKey, nil)
	var owner map[string]any
	for _, c := range m["checks"].([]any) {
		if c := c.(map[string]any); c["name"] == "Aadhaar belongs to the applicant" {
			owner = c
		}
	}
	if owner == nil || owner["ok"] != false {
		t.Fatalf("mobile/email of someone else not flagged: %v", m["checks"])
	}

	// Five failures end the session.
	_, link = r.create(Hint{Name: "Kavya Iyer"})
	off = r.offlineLink(link)
	for i := 0; i < 5; i++ {
		resp = r.upload(off, fixture(t, "okyc-tampered.zip"), "1234")
	}
	if loc, _ := url.Parse(resp.Header.Get("Location")); loc.Query().Get("kyc_status") != "Failed" {
		t.Fatalf("not failed after 5 tries: %d", resp.StatusCode)
	}
	if resp := r.upload(off, fixture(t, "okyc-zipcrypto.zip"), "1234"); resp.StatusCode != 410 {
		t.Errorf("upload after failure: %d", resp.StatusCode)
	}

	// Without UIDAI's certificate the route is not offered.
	r.srv.Certs = nil
	if code, _ := r.api("POST", "/v1/sessions", testKey, map[string]any{"return_url": "https://bo.acme.in/x", "method": MethodOffline}); code != 409 {
		t.Errorf("no certificate: %d", code)
	}
	if code, _ := r.api("POST", "/v1/sessions", testKey, map[string]any{"return_url": "https://bo.acme.in/x", "method": "fax"}); code != 400 {
		t.Errorf("bad method: %d", code)
	}
}
