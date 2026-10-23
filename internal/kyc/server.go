package kyc

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Customer is a broker (or any back office) using the service, with its
// own API key and its own DigiLocker requester registration.
type Customer struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	APIKeySHA256  string     `json:"api_key_sha256"`
	DigiLocker    DigiLocker `json:"digilocker"`
	ReturnHosts   []string   `json:"return_hosts"` // where investors may be sent back to
	WebhookURL    string     `json:"webhook_url,omitempty"`
	WebhookSecret string     `json:"webhook_secret,omitempty"`
}

// Config is the service's configuration (a JSON file, kept private).
type Config struct {
	PublicURL         string     `json:"public_url"` // how investors reach this service
	ProviderKeySHA256 string     `json:"provider_key_sha256"`
	RetentionHours    int        `json:"retention_hours"` // finished sessions are deleted after this (default 72)
	SessionMinutes    int        `json:"session_minutes"` // an unfinished session expires (default 30)
	Customers         []Customer `json:"customers"`
}

// Server is the HTTP service.
type Server struct {
	Cfg      Config
	Store    *Store
	Base     string // DigiLocker API base for every customer; empty is production
	Simulate bool   // serve a practice DigiLocker at /sim/digilocker and use it
	Now      func() time.Time
	Log      *slog.Logger
	HTTP     *http.Client
	mux      *http.ServeMux
	sim      *Sim
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) customer(id string) (Customer, bool) {
	for _, c := range s.Cfg.Customers {
		if c.ID == id {
			c.DigiLocker.Base = s.dlBase()
			c.DigiLocker.HTTP = s.HTTP
			if c.DigiLocker.RedirectURI == "" {
				c.DigiLocker.RedirectURI = strings.TrimRight(s.Cfg.PublicURL, "/") + "/callback"
			}
			return c, true
		}
	}
	return Customer{}, false
}

func (s *Server) dlBase() string {
	if s.Simulate {
		return strings.TrimRight(s.Cfg.PublicURL, "/") + "/sim/digilocker/public"
	}
	return s.Base
}

// Handler returns the service's routes.
func (s *Server) Handler() http.Handler {
	if s.mux != nil {
		return s.mux
	}
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	m.HandleFunc("POST /v1/sessions", s.auth(s.create))
	m.HandleFunc("GET /v1/sessions/{id}", s.auth(s.read))
	m.HandleFunc("DELETE /v1/sessions/{id}", s.auth(s.remove))
	m.HandleFunc("GET /v1/usage", s.auth(s.usage))
	m.HandleFunc("GET /start/{customer}/{id}", s.start)
	m.HandleFunc("GET /callback", s.callback)
	m.HandleFunc("GET /provider/usage", s.providerUsage)
	if s.Simulate {
		secrets := map[string]string{}
		for _, c := range s.Cfg.Customers {
			secrets[c.DigiLocker.ClientID] = c.DigiLocker.ClientSecret
		}
		s.sim = &Sim{Secrets: secrets, Hint: s.hintForState}
		m.Handle("/sim/digilocker/public/", http.StripPrefix("/sim/digilocker/public", s.sim.Handler()))
	}
	s.mux = m
	return m
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"message": msg}})
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// auth checks the customer's API key (Bearer), comparing hashes in
// constant time with every customer's.
func (s *Server) auth(h func(http.ResponseWriter, *http.Request, Customer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(key) < 24 {
			writeErr(w, 401, "send your API key as: Authorization: Bearer <key>")
			return
		}
		got := sha(key)
		var who *Customer
		for i := range s.Cfg.Customers {
			if subtle.ConstantTimeCompare([]byte(got), []byte(strings.ToLower(s.Cfg.Customers[i].APIKeySHA256))) == 1 {
				who = &s.Cfg.Customers[i]
			}
		}
		if who == nil {
			writeErr(w, 401, "unknown API key")
			return
		}
		c, _ := s.customer(who.ID)
		h(w, r, c)
	}
}

func (s *Server) minutes() time.Duration {
	if s.Cfg.SessionMinutes > 0 {
		return time.Duration(s.Cfg.SessionMinutes) * time.Minute
	}
	return 30 * time.Minute
}

// Retention is how long finished sessions are kept.
func (s *Server) Retention() time.Duration {
	if s.Cfg.RetentionHours > 0 {
		return time.Duration(s.Cfg.RetentionHours) * time.Hour
	}
	return 72 * time.Hour
}

func (s *Server) returnAllowed(c Customer, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return false
	}
	if u.Scheme == "http" && !s.Simulate {
		return false
	}
	if s.Simulate && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1") {
		return true // practice mode: any local back office
	}
	for _, h := range c.ReturnHosts {
		if strings.EqualFold(h, u.Host) {
			return true
		}
	}
	return false
}

// create starts a session: POST /v1/sessions
// {"reference", "return_url", "hint": {"name", "dob", "pan"}}
func (s *Server) create(w http.ResponseWriter, r *http.Request, c Customer) {
	var b struct {
		Reference string `json:"reference"`
		ReturnURL string `json:"return_url"`
		Hint      Hint   `json:"hint"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeErr(w, 400, "the request is not valid JSON")
		return
	}
	if !s.returnAllowed(c, b.ReturnURL) {
		writeErr(w, 400, "return_url must be https on one of your registered hosts")
		return
	}
	if c.DigiLocker.ClientID == "" || c.DigiLocker.ClientSecret == "" {
		writeErr(w, 409, "your DigiLocker registration (client ID and secret) is not set up yet")
		return
	}
	now := s.now()
	tok := randHex(24)
	x := Session{ID: "s" + randHex(12), Customer: c.ID, Reference: strings.TrimSpace(b.Reference), ReturnURL: b.ReturnURL, Hint: b.Hint,
		Status: "Created", Created: now.UTC().Format(time.RFC3339), Expires: now.Add(s.minutes()).UTC().Format(time.RFC3339), TokenHash: sha(tok)}
	x.Hint.PAN = strings.ToUpper(strings.TrimSpace(x.Hint.PAN))
	if err := s.Store.Put(x); err != nil {
		writeErr(w, 500, "the session could not be saved")
		return
	}
	_ = s.Store.Count(c.ID, "started", now)
	writeJSON(w, 201, map[string]string{"id": x.ID, "url": strings.TrimRight(s.Cfg.PublicURL, "/") + "/start/" + c.ID + "/" + x.ID + "?t=" + tok, "expires": x.Expires})
}

func (s *Server) read(w http.ResponseWriter, r *http.Request, c Customer) {
	x, err := s.Store.Get(c.ID, r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, "no such session (finished sessions are deleted after a while)")
		return
	}
	if x.Status != "Completed" && x.Status != "Failed" && s.expired(x) {
		x.Status, x.Error = "Expired", "the investor did not finish in time"
	}
	writeJSON(w, 200, x.Public())
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request, c Customer) {
	if err := s.Store.Delete(c.ID, r.PathValue("id")); err != nil {
		writeErr(w, 404, "no such session")
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request, c Customer) {
	m := r.URL.Query().Get("month")
	if m == "" {
		m = s.now().Format("2006-01")
	}
	u, _ := s.Store.Usage(c.ID, m)
	writeJSON(w, 200, u)
}

// providerUsage is the operator's monthly usage of every customer, for
// invoicing: GET /provider/usage?month=YYYY-MM[&format=csv].
func (s *Server) providerUsage(w http.ResponseWriter, r *http.Request) {
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(key) < 24 || s.Cfg.ProviderKeySHA256 == "" || subtle.ConstantTimeCompare([]byte(sha(key)), []byte(strings.ToLower(s.Cfg.ProviderKeySHA256))) != 1 {
		writeErr(w, 401, "provider key required")
		return
	}
	m := r.URL.Query().Get("month")
	if m == "" {
		m = s.now().Format("2006-01")
	}
	var ids []string
	names := map[string]string{}
	for _, c := range s.Cfg.Customers {
		ids, names[c.ID] = append(ids, c.ID), c.Name
	}
	us := s.Store.AllUsage(ids, m)
	if r.URL.Query().Get("format") == "csv" {
		var b bytes.Buffer
		cw := csv.NewWriter(&b)
		_ = cw.Write([]string{"Customer", "Name", "Month", "Started", "Completed (billable)", "Failed"})
		for _, u := range us {
			_ = cw.Write([]string{u.Customer, names[u.Customer], u.Month, fmt.Sprint(u.Started), fmt.Sprint(u.Completed), fmt.Sprint(u.Failed)})
		}
		cw.Flush()
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="ledgerline-kyc-usage-`+m+`.csv"`)
		w.Write(b.Bytes())
		return
	}
	writeJSON(w, 200, us)
}

func (s *Server) expired(x Session) bool {
	t, err := time.Parse(time.RFC3339, x.Expires)
	return err == nil && s.now().After(t)
}

// start is the investor's link: it sends them to DigiLocker.
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	c, ok := s.customer(r.PathValue("customer"))
	x, err := s.Store.Get(r.PathValue("customer"), r.PathValue("id"))
	if !ok || err != nil || subtle.ConstantTimeCompare([]byte(sha(r.URL.Query().Get("t"))), []byte(x.TokenHash)) != 1 {
		page(w, 404, "This link is not valid", "Go back to the account opening page and try again.", "")
		return
	}
	if x.Status == "Completed" || x.Status == "Failed" || s.expired(x) {
		page(w, 410, "This link has been used or has expired", "Go back to the account opening page and start DigiLocker again.", x.ReturnURL)
		return
	}
	verifier, challenge := PKCE()
	x.Status, x.Nonce, x.Verifier = "Started", randHex(16), verifier
	if err := s.Store.Put(x); err != nil {
		page(w, 500, "Something went wrong", "Please try again in a minute.", x.ReturnURL)
		return
	}
	http.Redirect(w, r, c.DigiLocker.AuthorizeURL(c.ID+"."+x.ID+"."+x.Nonce, challenge), http.StatusFound)
}

func (s *Server) byState(state string) (Customer, Session, error) {
	p := strings.Split(state, ".")
	if len(p) != 3 {
		return Customer{}, Session{}, ErrNoSession
	}
	c, ok := s.customer(p[0])
	if !ok {
		return Customer{}, Session{}, ErrNoSession
	}
	x, err := s.Store.Get(p[0], p[1])
	if err != nil || x.Nonce == "" || subtle.ConstantTimeCompare([]byte(x.Nonce), []byte(p[2])) != 1 {
		return Customer{}, Session{}, ErrNoSession
	}
	return c, x, nil
}

// hintForState lets the practice DigiLocker answer with the investor's
// own name, date of birth and PAN.
func (s *Server) hintForState(state string) (Hint, bool) {
	_, x, err := s.byState(state)
	return x.Hint, err == nil
}

// callback is where DigiLocker sends the investor back.
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, x, err := s.byState(q.Get("state"))
	if err != nil {
		page(w, 400, "This DigiLocker reply is not valid", "Go back to the account opening page and start DigiLocker again.", "")
		return
	}
	if x.Status != "Started" {
		http.Redirect(w, r, backURL(x), http.StatusFound)
		return
	}
	fail := func(msg string) {
		x.Status, x.Error, x.Verifier, x.Completed = "Failed", msg, "", s.now().UTC().Format(time.RFC3339)
		_ = s.Store.Put(x)
		_ = s.Store.Count(c.ID, "failed", s.now())
		s.notify(c, x)
		http.Redirect(w, r, backURL(x), http.StatusFound)
	}
	if e := q.Get("error"); e != "" {
		msg := "consent denied in DigiLocker"
		if e != "access_denied" {
			msg = "DigiLocker returned an error: " + firstNonEmpty(q.Get("error_description"), e)
		}
		fail(msg)
		return
	}
	if s.expired(x) {
		fail("the investor did not finish in time")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := s.fetch(ctx, c, x, q.Get("code"))
	if err != nil {
		fail(err.Error())
		return
	}
	x = res
	x.Status, x.Verifier, x.Completed = "Completed", "", s.now().UTC().Format(time.RFC3339)
	if err := s.Store.Put(x); err != nil {
		fail("the documents could not be saved")
		return
	}
	_ = s.Store.Count(c.ID, "completed", s.now())
	s.notify(c, x)
	if s.Log != nil {
		s.Log.Info("digilocker completed", "customer", c.ID, "session", x.ID, "aadhaar", x.Aadhaar != nil, "pan", x.PAN != nil)
	}
	http.Redirect(w, r, backURL(x), http.StatusFound)
}

// fetch exchanges the code and reads the e-Aadhaar and PAN record.
func (s *Server) fetch(ctx context.Context, c Customer, x Session, code string) (Session, error) {
	if code == "" {
		return x, errors.New("DigiLocker sent no authorisation code")
	}
	tok, err := c.DigiLocker.Exchange(ctx, code, x.Verifier)
	if err != nil {
		return x, err
	}
	if tok.EAadhaar != "N" {
		b, err := c.DigiLocker.EAadhaar(ctx, tok.AccessToken)
		switch {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return x, fmt.Errorf("e-Aadhaar: %w", err)
		default:
			a, err := ParseEAadhaar(b)
			if err != nil {
				return x, err
			}
			x.Aadhaar = &a
		}
	}
	docs, err := c.DigiLocker.Issued(ctx, tok.AccessToken)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return x, fmt.Errorf("issued documents: %w", err)
	}
	for _, d := range docs {
		if !strings.EqualFold(d.DocType, "PANCR") {
			continue
		}
		b, err := c.DigiLocker.IssuedXML(ctx, tok.AccessToken, d.URI)
		if err != nil {
			return x, fmt.Errorf("PAN record: %w", err)
		}
		p, err := ParsePAN(b)
		if err != nil {
			return x, err
		}
		x.PAN = &p
		break
	}
	x.Checks = compare(x.Hint, x.Aadhaar, x.PAN)
	return x, nil
}

// compare checks DigiLocker's documents against what the investor typed.
func compare(h Hint, a *Aadhaar, p *PAN) []Check {
	var cs []Check
	add := func(name string, ok bool, detail string) { cs = append(cs, Check{name, ok, detail}) }
	if a == nil {
		add("Aadhaar", false, "no Aadhaar in this DigiLocker account")
	} else {
		if h.Name != "" {
			add("Aadhaar name", NamesMatch(a.Name, h.Name), fmt.Sprintf("Aadhaar %q, given %q", a.Name, h.Name))
		}
		if h.DOB != "" {
			add("Aadhaar date of birth", a.DOB == h.DOB, fmt.Sprintf("Aadhaar %s, given %s", a.DOB, h.DOB))
		}
	}
	if p == nil {
		add("PAN", false, "no PAN record from the Income Tax Department in this DigiLocker account")
	} else {
		if h.PAN != "" {
			add("PAN number", strings.EqualFold(p.Number, h.PAN), fmt.Sprintf("record %s, given %s", mask(p.Number), mask(h.PAN)))
		}
		if h.Name != "" {
			add("PAN name", NamesMatch(p.Name, h.Name), fmt.Sprintf("record %q, given %q", p.Name, h.Name))
		}
		if a != nil {
			add("Aadhaar and PAN are the same person", NamesMatch(a.Name, p.Name) && (p.DOB == "" || a.DOB == "" || a.DOB == p.DOB), fmt.Sprintf("names %q and %q", a.Name, p.Name))
		}
	}
	return cs
}

func mask(pan string) string {
	if len(pan) != 10 {
		return pan
	}
	return pan[:3] + "XX" + pan[5:6] + "XXX" + pan[9:]
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func backURL(x Session) string {
	sep := "?"
	if strings.Contains(x.ReturnURL, "?") {
		sep = "&"
	}
	return x.ReturnURL + sep + "kyc=" + url.QueryEscape(x.ID) + "&kyc_status=" + url.QueryEscape(x.Public().Status)
}

// notify posts the outcome to the customer's webhook, signed with
// HMAC-SHA256 of the body in X-Signature. It does not carry the documents:
// the customer reads them with its API key.
func (s *Server) notify(c Customer, x Session) {
	if c.WebhookURL == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{"id": x.ID, "reference": x.Reference, "status": x.Public().Status})
	m := hmac.New(sha256.New, []byte(c.WebhookSecret))
	m.Write(body)
	sig := hex.EncodeToString(m.Sum(nil))
	go func() {
		req, err := http.NewRequest(http.MethodPost, c.WebhookURL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Signature", sig)
		hc := &http.Client{Timeout: 10 * time.Second}
		if resp, err := hc.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
}

var pageTmpl = template.Must(template.New("p").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.T}}</title>
<style>body{font:15px/1.5 system-ui,sans-serif;margin:0;background:#F4F3EE;color:#16201B}main{max-width:460px;margin:12vh auto;padding:24px;background:#fff;border-radius:12px;border:1px solid #ddd}h1{font-size:19px;margin:0 0 8px}a{color:#2747A8}@media(prefers-color-scheme:dark){body{background:#111613;color:#E6EAE7}main{background:#1A211D;border-color:#2c352f}a{color:#9DB2F2}}</style></head>
<body><main><h1>{{.T}}</h1><p>{{.M}}</p>{{if .B}}<p><a href="{{.B}}">Back to the account opening page</a></p>{{end}}</main></body></html>`))

func page(w http.ResponseWriter, code int, title, msg, back string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = pageTmpl.Execute(w, map[string]string{"T": title, "M": msg, "B": back})
}
