// Package kyc is Ledgerline KYC: Aadhaar and PAN from DigiLocker, for
// stockbrokers' account opening, as a service any back office can call.
//
// The investor is sent to DigiLocker, signs in with their Aadhaar-linked
// mobile and consents. The service fetches the e-Aadhaar XML and the PAN
// record issued by the Income Tax Department, checks DigiLocker's
// integrity code (HMAC-SHA256 keyed with the client secret) on each file,
// reads them, and compares them with what the investor typed. With KYC
// completed through Aadhaar from DigiLocker, SEBI's KYC circular
// (SEBI/HO/MIRSD/DOP/CIR/P/2020/73) needs no in-person verification.
//
// Each customer (broker) uses its own DigiLocker requester registration:
// the service is software run for them, not verification on their behalf.
package kyc

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultDigiLocker is DigiLocker's production API (MeriPehchaan).
const DefaultDigiLocker = "https://digilocker.meripehchaan.gov.in/public"

// DigiLocker is one customer's requester registration.
type DigiLocker struct {
	ClientID     string       `json:"client_id"`
	ClientSecret string       `json:"client_secret"`
	RedirectURI  string       `json:"redirect_uri"` // this service's /callback, as registered
	Base         string       `json:"-"`            // API base; DefaultDigiLocker unless testing
	HTTP         *http.Client `json:"-"`
}

func (d DigiLocker) base() string {
	if d.Base != "" {
		return strings.TrimRight(d.Base, "/")
	}
	return DefaultDigiLocker
}

func (d DigiLocker) client() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// PKCE returns a code verifier and its S256 challenge.
func PKCE() (verifier, challenge string) {
	b := make([]byte, 48)
	_, _ = rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(h[:])
}

// AuthorizeURL is where the investor is sent to consent.
func (d DigiLocker) AuthorizeURL(state, challenge string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", d.ClientID)
	q.Set("redirect_uri", d.RedirectURI)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	return d.base() + "/oauth2/1/authorize?" + q.Encode()
}

// Token is DigiLocker's token reply, with the profile it carries.
type Token struct {
	AccessToken  string `json:"access_token"`
	DigiLockerID string `json:"digilockerid"`
	Name         string `json:"name"`
	DOB          string `json:"dob"` // DDMMYYYY
	Gender       string `json:"gender"`
	EAadhaar     string `json:"eaadhaar"` // Y when the account has Aadhaar
	Reference    string `json:"reference_key"`
}

// Exchange trades the authorisation code for a token.
func (d DigiLocker) Exchange(ctx context.Context, code, verifier string) (Token, error) {
	f := url.Values{}
	f.Set("code", code)
	f.Set("grant_type", "authorization_code")
	f.Set("client_id", d.ClientID)
	f.Set("client_secret", d.ClientSecret)
	f.Set("redirect_uri", d.RedirectURI)
	f.Set("code_verifier", verifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.base()+"/oauth2/1/token", strings.NewReader(f.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := d.client().Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("DigiLocker refused the code (HTTP %d)", resp.StatusCode)
	}
	var t Token
	if err := json.Unmarshal(b, &t); err != nil || t.AccessToken == "" {
		return Token{}, errors.New("DigiLocker's token reply could not be read")
	}
	return t, nil
}

// ErrNotFound means DigiLocker has no such document for the investor.
var ErrNotFound = errors.New("not in this DigiLocker account")

// get fetches a document and checks DigiLocker's hmac header.
func (d DigiLocker) get(ctx context.Context, tok, path string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base()+path, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := d.client().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, "", ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, "", fmt.Errorf("DigiLocker returned HTTP %d for %s", resp.StatusCode, path)
	}
	return b, resp.Header.Get("hmac"), nil
}

// checkHMAC verifies DigiLocker's integrity code on a file: base64 of
// HMAC-SHA256 of the content, keyed with the client secret.
func (d DigiLocker) checkHMAC(body []byte, got string) error {
	if got == "" {
		return errors.New("DigiLocker sent the file without its integrity code")
	}
	m := hmac.New(sha256.New, []byte(d.ClientSecret))
	m.Write(body)
	want := base64.StdEncoding.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(strings.TrimSpace(got))) {
		return errors.New("the file's integrity code does not match: it was changed after DigiLocker issued it")
	}
	return nil
}

// EAadhaar fetches the investor's e-Aadhaar XML.
func (d DigiLocker) EAadhaar(ctx context.Context, tok string) ([]byte, error) {
	b, h, err := d.get(ctx, tok, "/oauth2/3/xml/eaadhaar")
	if err != nil {
		return nil, err
	}
	return b, d.checkHMAC(b, h)
}

// IssuedDoc is a document issued into the investor's DigiLocker.
type IssuedDoc struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	URI      string `json:"uri"`
	DocType  string `json:"doctype"`
	Issuer   string `json:"issuer"`
	IssuerID string `json:"issuerid"`
	MIME     any    `json:"mime"`
}

// Issued lists the documents issued to the investor.
func (d DigiLocker) Issued(ctx context.Context, tok string) ([]IssuedDoc, error) {
	b, _, err := d.get(ctx, tok, "/oauth2/2/files/issued")
	if err != nil {
		return nil, err
	}
	var r struct {
		Items []IssuedDoc `json:"items"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, errors.New("DigiLocker's list of issued documents could not be read")
	}
	return r.Items, nil
}

// IssuedXML fetches an issued document's data as XML.
func (d DigiLocker) IssuedXML(ctx context.Context, tok, uri string) ([]byte, error) {
	b, h, err := d.get(ctx, tok, "/oauth2/1/xml/"+url.PathEscape(uri))
	if err != nil {
		return nil, err
	}
	return b, d.checkHMAC(b, h)
}

// ---- reading the documents ----

// Aadhaar is what an e-Aadhaar XML says.
type Aadhaar struct {
	Name     string `json:"name"`
	DOB      string `json:"dob"` // YYYY-MM-DD
	Gender   string `json:"gender"`
	Last4    string `json:"last4"`
	CareOf   string `json:"care_of"`
	Address  string `json:"address"` // house, street, landmark, locality, post office
	District string `json:"district"`
	State    string `json:"state"`
	PIN      string `json:"pin"`
	Photo    []byte `json:"photo,omitempty"`
	XML      []byte `json:"xml,omitempty"`
	SHA256   string `json:"sha256"`
}

// PAN is what a PAN record (doctype PANCR) says.
type PAN struct {
	Number string `json:"number"`
	Name   string `json:"name"`
	DOB    string `json:"dob"`
	XML    []byte `json:"xml,omitempty"`
	SHA256 string `json:"sha256"`
}

func attrs(se xml.StartElement) map[string]string {
	m := map[string]string{}
	for _, a := range se.Attr {
		m[strings.ToLower(a.Name.Local)] = strings.TrimSpace(a.Value)
	}
	return m
}

// isoDate reads DD-MM-YYYY, DD/MM/YYYY, DDMMYYYY or YYYY-MM-DD.
func isoDate(s string) string {
	s = strings.TrimSpace(s)
	for _, l := range []string{"02-01-2006", "02/01/2006", "02012006", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

func genderName(g string) string {
	switch strings.ToUpper(strings.TrimSpace(g)) {
	case "M", "MALE":
		return "Male"
	case "F", "FEMALE":
		return "Female"
	case "T", "TRANSGENDER":
		return "Transgender"
	}
	return ""
}

var reDigits = regexp.MustCompile(`[0-9]{4}$`)

// ParseEAadhaar reads an e-Aadhaar XML: UidData (masked number), Poi
// (name, date of birth, gender), Poa (address) and Pht (photograph).
func ParseEAadhaar(b []byte) (Aadhaar, error) {
	var a Aadhaar
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	inPht := false
	var pht strings.Builder
	seen := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return a, errors.New("the e-Aadhaar XML could not be read")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			m := attrs(t)
			switch strings.ToLower(t.Name.Local) {
			case "uiddata":
				seen = true
				a.Last4 = reDigits.FindString(m["uid"])
			case "poi":
				seen = true
				a.Name, a.DOB, a.Gender = m["name"], isoDate(m["dob"]), genderName(m["gender"])
			case "poa":
				a.CareOf = m["co"]
				var parts []string
				for _, k := range []string{"house", "street", "lm", "loc", "vtc", "subdist", "po"} {
					if v := m[k]; v != "" && !contains(parts, v) {
						parts = append(parts, v)
					}
				}
				a.Address = strings.Join(parts, ", ")
				a.District, a.State, a.PIN = m["dist"], m["state"], m["pc"]
			case "pht":
				inPht = true
			}
		case xml.CharData:
			if inPht {
				pht.Write(t)
			}
		case xml.EndElement:
			if strings.EqualFold(t.Name.Local, "pht") {
				inPht = false
			}
		}
	}
	if !seen || a.Name == "" {
		return a, errors.New("this is not an e-Aadhaar XML (no name or Aadhaar data in it)")
	}
	if p := strings.Join(strings.Fields(pht.String()), ""); p != "" {
		if img, err := base64.StdEncoding.DecodeString(p); err == nil {
			a.Photo = img
		}
	}
	h := sha256.Sum256(b)
	a.XML, a.SHA256 = b, fmt.Sprintf("%x", h)
	return a, nil
}

var rePAN = regexp.MustCompile(`^[A-Z]{5}[0-9]{4}[A-Z]$`)

// ParsePAN reads a PAN record issued by the Income Tax Department: the
// certificate's number, and the person it is issued to.
func ParsePAN(b []byte) (PAN, error) {
	var p PAN
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return p, errors.New("the PAN record could not be read")
		}
		if se, ok := tok.(xml.StartElement); ok {
			m := attrs(se)
			switch strings.ToLower(se.Name.Local) {
			case "certificate":
				if n := strings.ToUpper(m["number"]); rePAN.MatchString(n) {
					p.Number = n
				}
			case "person":
				if p.Name == "" {
					p.Name, p.DOB = m["name"], isoDate(m["dob"])
				}
			}
		}
	}
	if p.Number == "" {
		return p, errors.New("no PAN number in the record")
	}
	h := sha256.Sum256(b)
	p.XML, p.SHA256 = b, fmt.Sprintf("%x", h)
	return p, nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ---- comparing ----

func nameWords(s string) []string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || r > 127 {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// NamesMatch compares names as KYC teams do: the same words in any order,
// initials allowed for middle names.
func NamesMatch(a, b string) bool {
	x, y := nameWords(a), nameWords(b)
	if len(x) == 0 || len(y) == 0 {
		return false
	}
	if len(x) > len(y) {
		x, y = y, x
	}
	used := make([]bool, len(y))
	for _, w := range x {
		found := false
		for i, v := range y {
			if used[i] {
				continue
			}
			if v == w || (len(w) == 1 && strings.HasPrefix(v, w)) || (len(v) == 1 && strings.HasPrefix(w, v)) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return len(x) >= 2 || len(y) == 1
}
