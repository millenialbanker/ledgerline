package kyc

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Sim is a practice DigiLocker for demonstrations and tests: the same
// consent page flow and API calls as the real one (PKCE, token, e-Aadhaar
// XML, issued documents, PAN record, integrity codes), answering with the
// investor's own details and a made-up Aadhaar. Never used with real
// credentials.
type Sim struct {
	Secrets map[string]string               // client ID -> secret
	Hint    func(state string) (Hint, bool) // the investor's details, by OAuth state
	mu      sync.Mutex
	codes   map[string]simGrant
	tokens  map[string]simGrant
}

type simGrant struct {
	client, redirect, challenge string
	hint                        Hint
	at                          time.Time
}

// Handler serves the practice DigiLocker under its /public prefix.
func (s *Sim) Handler() http.Handler {
	s.mu.Lock()
	if s.codes == nil {
		s.codes, s.tokens = map[string]simGrant{}, map[string]simGrant{}
	}
	s.mu.Unlock()
	m := http.NewServeMux()
	m.HandleFunc("GET /oauth2/1/authorize", s.authorize)
	m.HandleFunc("POST /oauth2/1/consent", s.consent)
	m.HandleFunc("POST /oauth2/1/token", s.token)
	m.HandleFunc("GET /oauth2/3/xml/eaadhaar", s.eaadhaar)
	m.HandleFunc("GET /oauth2/2/files/issued", s.issued)
	m.HandleFunc("GET /oauth2/1/xml/{uri}", s.panXML)
	return m
}

var simPage = template.Must(template.New("s").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>DigiLocker (practice)</title>
<style>body{font:15px/1.5 system-ui,sans-serif;margin:0;background:#EEF1F8;color:#16201B}main{max-width:440px;margin:8vh auto;padding:24px;background:#fff;border-radius:12px;border:1px solid #d5dbe8}h1{font-size:19px;margin:0}.tag{display:inline-block;background:#FFF1C9;color:#6B4E00;border-radius:6px;padding:2px 8px;font-size:12px;margin:6px 0 14px}ul{padding-left:18px}button{font:inherit;padding:10px 16px;border-radius:8px;border:1px solid #2747A8;background:#2747A8;color:#fff;cursor:pointer}button.no{background:#fff;color:#2747A8;margin-left:8px}@media(prefers-color-scheme:dark){body{background:#0F1320;color:#E6EAE7}main{background:#1A2030;border-color:#2c3550}button.no{background:#1A2030;color:#9DB2F2;border-color:#9DB2F2}}</style></head>
<body><main><h1>DigiLocker</h1><span class="tag">Practice DigiLocker: no real Aadhaar is used</span>
<p><b>{{.Name}}</b> asks to read from your DigiLocker:</p><ul><li>Your Aadhaar (e-Aadhaar XML: name, date of birth, gender, address, photograph)</li><li>Your PAN record from the Income Tax Department</li></ul>
<form method="post" action="consent"><input type="hidden" name="q" value="{{.Q}}"><button name="allow" value="1">Allow</button><button class="no" name="allow" value="0">Deny</button></form></main></body></html>`))

func (s *Sim) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if _, ok := s.Secrets[q.Get("client_id")]; !ok || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("response_type") != "code" {
		http.Error(w, "invalid request: unknown client or missing PKCE", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = simPage.Execute(w, map[string]string{"Name": "Your stockbroker (" + q.Get("client_id") + ")", "Q": q.Encode()})
}

func (s *Sim) consent(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.FormValue("q"))
	if err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	back, _ := url.Parse(q.Get("redirect_uri"))
	if back == nil || back.Host == "" {
		http.Error(w, "bad redirect", 400)
		return
	}
	v := back.Query()
	v.Set("state", q.Get("state"))
	if r.FormValue("allow") != "1" {
		v.Set("error", "access_denied")
		v.Set("error_description", "User denied access")
	} else {
		h, _ := s.Hint(q.Get("state"))
		code := randHex(16)
		s.mu.Lock()
		s.codes[code] = simGrant{client: q.Get("client_id"), redirect: q.Get("redirect_uri"), challenge: q.Get("code_challenge"), hint: h, at: time.Now()}
		s.mu.Unlock()
		v.Set("code", code)
	}
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (s *Sim) token(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	g, ok := s.codes[r.FormValue("code")]
	delete(s.codes, r.FormValue("code"))
	s.mu.Unlock()
	h := sha256.Sum256([]byte(r.FormValue("code_verifier")))
	switch {
	case !ok || time.Since(g.at) > 10*time.Minute:
		http.Error(w, `{"error":"invalid_grant"}`, 400)
		return
	case r.FormValue("client_id") != g.client || r.FormValue("client_secret") != s.Secrets[g.client] || r.FormValue("redirect_uri") != g.redirect:
		http.Error(w, `{"error":"invalid_client"}`, 401)
		return
	case base64.RawURLEncoding.EncodeToString(h[:]) != g.challenge:
		http.Error(w, `{"error":"invalid_grant","error_description":"code_verifier does not match"}`, 400)
		return
	}
	tok := randHex(20)
	s.mu.Lock()
	s.tokens[tok] = g
	s.mu.Unlock()
	dob := ""
	if t, err := time.Parse("2006-01-02", g.hint.DOB); err == nil {
		dob = t.Format("02012006")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "expires_in": 3600, "token_type": "Bearer", "digilockerid": "sim-" + randHex(6),
		"name": g.hint.Name, "dob": dob, "gender": "F", "eaadhaar": "Y", "reference_key": randHex(8)})
}

func (s *Sim) grant(w http.ResponseWriter, r *http.Request) (simGrant, bool) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	g, ok := s.tokens[tok]
	s.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"invalid_token"}`, 401)
	}
	return g, ok
}

func (s *Sim) sign(w http.ResponseWriter, client string, body []byte, ct string) {
	m := hmac.New(sha256.New, []byte(s.Secrets[client]))
	m.Write(body)
	w.Header().Set("hmac", base64.StdEncoding.EncodeToString(m.Sum(nil)))
	w.Header().Set("Content-Type", ct)
	w.Write(body)
}

func xmlEsc(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func simPhoto() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 96, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 96; x++ {
			c := color.RGBA{214, 220, 230, 255}
			if (x-48)*(x-48)+(y-46)*(y-46) < 22*22 || (y > 80 && (x-48)*(x-48) < (y-60)*(y-60)) {
				c = color.RGBA{110, 122, 140, 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 80})
	return b.Bytes()
}

func (s *Sim) eaadhaar(w http.ResponseWriter, r *http.Request) {
	g, ok := s.grant(w, r)
	if !ok {
		return
	}
	dob := ""
	if t, err := time.Parse("2006-01-02", g.hint.DOB); err == nil {
		dob = t.Format("02-01-2006")
	}
	x := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Certificate language="99" name="Aadhaar Card" type="ADHAR" number="" issueDate="%s" status="A">
<CertificateData><KycRes code="sim" ret="Y" ts="%s" ttl="%s" txn="sim"><UidData tkn="" uid="xxxxxxxx%s">
<Poi dob="%s" gender="F" name="%s"/>
<Poa co="" country="India" dist="Bengaluru Urban" house="12" lm="Near Metro Station" loc="Indiranagar" pc="560038" po="Indiranagar" state="Karnataka" street="4th Cross" subdist="Bengaluru East" vtc="Bengaluru"/>
<LData/><Pht>%s</Pht></UidData></KycRes></CertificateData>
<Signature xmlns="http://www.w3.org/2000/09/xmldsig#"><SignatureValue>practice-only</SignatureValue></Signature></Certificate>`,
		time.Now().Format("02-01-2006"), time.Now().Format(time.RFC3339), time.Now().Add(24*time.Hour).Format(time.RFC3339), lastDigits(g.hint.PAN),
		dob, xmlEsc(g.hint.Name), base64.StdEncoding.EncodeToString(simPhoto()))
	s.sign(w, g.client, []byte(x), "application/xml")
}

// lastDigits makes a stable made-up Aadhaar ending for the investor.
func lastDigits(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%04d", (int(h[0])<<8|int(h[1]))%10000)
}

func (s *Sim) issued(w http.ResponseWriter, r *http.Request) {
	g, ok := s.grant(w, r)
	if !ok {
		return
	}
	items := []map[string]any{}
	if g.hint.PAN != "" {
		items = append(items, map[string]any{"name": "PAN Verification Record", "type": "file", "size": "", "date": time.Now().Format("02-01-2006"),
			"parent": "", "mime": []string{"application/pdf", "application/xml"}, "uri": "in.gov.pan-PANCR-" + g.hint.PAN, "doctype": "PANCR",
			"description": "PAN Verification Record", "issuerid": "in.gov.pan", "issuer": "Income Tax Department"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "resource": "1"})
}

func (s *Sim) panXML(w http.ResponseWriter, r *http.Request) {
	g, ok := s.grant(w, r)
	if !ok {
		return
	}
	uri := r.PathValue("uri")
	pan, found := strings.CutPrefix(uri, "in.gov.pan-PANCR-")
	if !found || pan != g.hint.PAN {
		http.Error(w, `{"error":"not_found"}`, 404)
		return
	}
	dob := ""
	if t, err := time.Parse("2006-01-02", g.hint.DOB); err == nil {
		dob = t.Format("02-01-2006")
	}
	x := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Certificate language="99" name="PAN Verification Record" type="PANCR" number="%s" prevNumber="" expiryDate="" validFromDate="" issuedAt="Income Tax Department" issueDate="%s" status="A">
<IssuedBy><Organization name="Income Tax Department" code="ITD" tin="" uid="" type="CG"><Address type="" line1="" line2="" house="" landmark="" locality="" vtc="" district="" pin="" state="" country="IN"/></Organization></IssuedBy>
<IssuedTo><Person uid="" title="" name="%s" dob="%s" age="" swd="" swdIndicator="" motherName="" gender="F" maritalStatus="" relationWithHof="" disabilityStatus="" category="" religion="" phone="" email=""><Address type="" line1="" line2="" house="" landmark="" locality="" vtc="" district="" pin="" state="" country="IN"/></Person></IssuedTo>
<CertificateData><PAN verifiedOn="%s"/></CertificateData>
<Signature xmlns="http://www.w3.org/2000/09/xmldsig#"><SignatureValue>practice-only</SignatureValue></Signature></Certificate>`,
		xmlEsc(pan), time.Now().Format("02-01-2006"), xmlEsc(g.hint.Name), dob, time.Now().Format("02-01-2006"))
	s.sign(w, g.client, []byte(x), "application/xml")
}
