package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/ops"
)

// Browser sign-in. The console authenticates with a session cookie set by
// POST /v1/auth/login; integrations keep using an API key plus X-Actor.
//
// The cookie is HttpOnly and SameSite=Strict, and changes made with it must
// carry "X-Requested-With: ledgerline", a header other sites cannot send
// without a CORS preflight this server never grants.

const (
	cookieName = "ll_session"
	csrfHeader = "X-Requested-With"
	csrfValue  = "ledgerline"
)

// sessionAuth resolves the session cookie to tenant and user.
func (s *Server) sessionAuth(r *http.Request) (tenant string, st ops.AuthState, tok string, err error) {
	c, cerr := r.Cookie(cookieName)
	if cerr != nil || s.ops == nil {
		return "", st, "", ops.ErrSessionEnded
	}
	i := strings.LastIndex(c.Value, ".")
	if i <= 0 {
		return "", st, "", ops.ErrSessionEnded
	}
	tenant, tok = c.Value[:i], c.Value[i+1:]
	if !s.knownTenant(tenant) {
		return "", st, "", ops.ErrSessionEnded
	}
	st, err = s.ops.SessionUser(r.Context(), tenant, tok)
	return tenant, st, tok, err
}

func (s *Server) knownTenant(t string) bool {
	for _, x := range s.keys {
		if x == t {
			return true
		}
	}
	return false
}

// Tenants lists the firms this server serves.
func (s *Server) Tenants() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range s.keys {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// setupPaths may be used by a session that still has to change its
// password or set up two-factor sign-in.
func setupPath(p string) bool {
	return p == "/v1/me" || strings.HasPrefix(p, "/v1/auth/")
}

func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// limiter allows a number of attempts per key in a sliding window.
type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	max  int
	per  time.Duration
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.hits == nil {
		l.hits = map[string][]time.Time{}
	}
	keep := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.per {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.max {
		l.hits[key] = keep
		return false
	}
	l.hits[key] = append(keep, now)
	return true
}

func (s *Server) authRoutes() {
	s.mux.HandleFunc("POST /v1/auth/login", s.login)
	s.mux.HandleFunc("POST /v1/auth/logout", s.logout)
	s.route("POST /v1/auth/password", s.changePassword, true)
	s.route("POST /v1/auth/2fa/setup", s.totpSetup, true)
	s.route("POST /v1/auth/2fa/enable", s.totpEnable, true)
	s.route("POST /v1/users", s.createUser, true)
	s.route("POST /v1/users/{id}/reset-password", s.resetPassword, true)
	s.route("POST /v1/users/{id}/reset-2fa", s.resetTOTP, true)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeErr(w, 404, "not_found", "sign-in is not available", nil)
		return
	}
	var b struct {
		Tenant   string `json:"tenant"`
		User     string `json:"user"`
		Password string `json:"password"`
		OTP      string `json:"otp"`
	}
	if !decode(w, r, &b) {
		return
	}
	ip := clientIP(r)
	if !s.loginLimit.allow(ip) {
		writeErr(w, 429, "too_many_attempts", "Too many sign-in attempts from this address. Wait 15 minutes and try again.", nil)
		return
	}
	b.Tenant = strings.ToLower(strings.TrimSpace(b.Tenant))
	if b.Tenant == "" {
		if ts := s.Tenants(); len(ts) == 1 {
			b.Tenant = ts[0]
		}
	}
	if !s.knownTenant(b.Tenant) {
		writeErr(w, 401, "auth_failed", "Unknown firm code.", nil)
		return
	}
	ctx := context.WithValue(r.Context(), keyTenant, b.Tenant)
	tok, st, err := s.ops.Login(ctx, b.Tenant, b.User, b.Password, b.OTP, ip, r.UserAgent())
	switch {
	case errors.Is(err, ops.ErrOTPRequired):
		writeErr(w, 401, "otp_required", err.Error(), nil)
		return
	case errors.Is(err, ops.ErrAuth):
		writeErr(w, 401, "auth_failed", "Wrong user ID, password or code.", nil)
		return
	case err != nil:
		fail(w, err, nil)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: b.Tenant + "." + tok, Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: 12 * 3600})
	writeJSON(w, 200, map[string]any{"tenant": b.Tenant, "auth": st})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if t, _, tok, err := s.sessionAuth(r); err == nil {
		s.ops.Logout(r.Context(), t, tok)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, 200, map[string]any{"signed_out": true})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var b struct{ Current, New string }
	if !decode(w, r, &b) {
		return
	}
	ok(w, map[string]any{"changed": true}, s.ops.ChangePassword(r.Context(), tenant(r), actor(r), b.Current, b.New))
}

func (s *Server) totpSetup(w http.ResponseWriter, r *http.Request) {
	secret, uri, err := s.ops.TOTPSetup(r.Context(), tenant(r), actor(r))
	grouped := ""
	for i := 0; i < len(secret); i += 4 {
		grouped += secret[i:min(i+4, len(secret))] + " "
	}
	ok(w, map[string]any{"secret": strings.TrimSpace(grouped), "uri": uri}, err)
}

func (s *Server) totpEnable(w http.ResponseWriter, r *http.Request) {
	var b struct{ Code string }
	if !decode(w, r, &b) {
		return
	}
	ok(w, map[string]any{"enabled": true}, s.ops.TOTPEnable(r.Context(), tenant(r), actor(r), b.Code))
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var u ops.User
	if !decode(w, r, &u) {
		return
	}
	pw, err := s.ops.CreateUser(r.Context(), tenant(r), actor(r), u)
	ok(w, map[string]any{"user": strings.ToLower(u.ID), "temp_password": pw}, err)
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	pw, err := s.ops.ResetPassword(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]any{"user": r.PathValue("id"), "temp_password": pw}, err)
}

func (s *Server) resetTOTP(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{"user": r.PathValue("id"), "reset": true}, s.ops.ResetTOTP(r.Context(), tenant(r), actor(r), r.PathValue("id")))
}
