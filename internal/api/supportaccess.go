package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ledgerline/internal/ops"
)

// Ledgerline support access (ops/supportaccess.go).
//
// Firm side, in the console: Admin → Support access lists requests, open
// access with End now, the history, the log (and its CSV) and the block
// switch.
//
// Operator side: Ledgerline's support people sign in at /support-desk with
// an operator login of their own (password and a required two-factor
// code, in a namespace that is no firm), ask a firm for access, and once
// the firm approves, start a session in that firm. Ledgerline Control,
// the provider's reporting server, never reaches a firm's data, so the
// operator routes live here, next to the data, on /v1/operator/.
//
// A support session uses the firm's console. Every request it makes goes
// through supportServe: SupportGate decides whether it is in the approved
// areas and (for a change) whether write was approved; the answer is held
// back until PAN, Aadhaar and bank account numbers in it are masked, files
// that cannot be masked are refused; and the request is written to the
// firm's audit trail with the support actor.

const operatorCookie = "ll_operator"

func (s *Server) supportAccessRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/support-access", s.saScreen, false)
	R("PUT /v1/support-access/policy", s.saPolicy, true)
	R("POST /v1/support-access/{id}/approve", s.saApprove, true)
	R("POST /v1/support-access/{id}/refuse", s.saRefuse, true)
	R("POST /v1/support-access/{id}/end", s.saEnd, true)
	R("GET /v1/support-access/log", s.saLog, false)
	R("GET /v1/support-access/log.csv", s.saLogCSV, false)
	R("GET /v1/support-access/session", s.saSession, false)
	R("POST /v1/support-access/session/end", s.saSessionEnd, true)

	s.mux.HandleFunc("POST /v1/operator/login", s.opLogin)
	s.mux.HandleFunc("POST /v1/operator/logout", s.opLogout)
	s.mux.HandleFunc("GET /v1/operator/me", s.operator(s.opMe, true, false))
	s.mux.HandleFunc("POST /v1/operator/password", s.operator(s.opPassword, true, true))
	s.mux.HandleFunc("POST /v1/operator/2fa/setup", s.operator(s.opTOTPSetup, true, true))
	s.mux.HandleFunc("POST /v1/operator/2fa/enable", s.operator(s.opTOTPEnable, true, true))
	s.mux.HandleFunc("GET /v1/operator/requests", s.operator(s.opRequests, false, false))
	s.mux.HandleFunc("POST /v1/operator/requests", s.operator(s.opRequest, false, true))
	s.mux.HandleFunc("POST /v1/operator/requests/{tenant}/{id}/start", s.operator(s.opStart, false, true))
	s.mux.HandleFunc("POST /v1/operator/requests/{tenant}/{id}/end", s.operator(s.opEnd, false, true))

	s.mux.HandleFunc("GET /provider/support/operators", s.provider(s.providerOperators))
	s.mux.HandleFunc("POST /provider/support/operators", s.provider(s.providerSaveOperator))
}

// ---- every request of a support session ----

// supportServe runs a support session's request: refused outside what the
// firm approved, masked on the way out, and recorded either way.
func (s *Server) supportServe(w http.ResponseWriter, r *http.Request, pattern string, sup *ops.SupportAuth, h http.HandlerFunc) {
	ctx := r.Context()
	t := tenant(r)
	where := ops.SupportMaskText(r.URL.RequestURI()) // a search for a PAN is not kept in clear either
	if err := ops.SupportGate(sup, pattern); err != nil {
		s.ops.SupportAction(ctx, t, sup, r.Method, where, http.StatusForbidden, err.Error())
		fail(w, err, nil)
		return
	}
	sw := &supportWriter{h: http.Header{}}
	h(sw, r)
	status, refused := sw.finish(w)
	s.ops.SupportAction(ctx, t, sup, r.Method, where, status, refused)
}

// supportWriter holds an answer back so it can be masked before it goes.
type supportWriter struct {
	h      http.Header
	status int
	buf    bytes.Buffer
}

func (w *supportWriter) Header() http.Header { return w.h }
func (w *supportWriter) WriteHeader(c int) {
	if w.status == 0 {
		w.status = c
	}
}
func (w *supportWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.buf.Write(b)
}

// finish masks the held answer and sends it. JSON has every string masked;
// text, CSV and HTML are masked as text; anything else (PDFs, ZIPs,
// images, uploaded files) cannot be masked and is refused.
func (w *supportWriter) finish(out http.ResponseWriter) (int, string) {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	body := w.buf.Bytes()
	ct := strings.ToLower(w.h.Get("Content-Type"))
	if ct == "" && len(body) > 0 {
		ct = strings.ToLower(http.DetectContentType(body))
	}
	switch {
	case len(body) == 0:
	case strings.Contains(ct, "json"):
		if m, ok := ops.SupportMaskJSON(body); ok {
			body = m
		} else {
			body = []byte(ops.SupportMaskText(string(body)))
		}
	case strings.HasPrefix(ct, "text/") || strings.Contains(ct, "csv") || strings.Contains(ct, "xml"):
		body = []byte(ops.SupportMaskText(string(body)))
	default:
		writeErr(out, http.StatusForbidden, "forbidden", "Files can't be opened or downloaded in a Ledgerline support session: PAN, Aadhaar and bank account numbers in them can't be masked.", nil)
		return http.StatusForbidden, "file refused (" + ct + ")"
	}
	for k, v := range w.h {
		if k != "Content-Length" {
			out.Header()[k] = v
		}
	}
	out.WriteHeader(status)
	_, _ = out.Write(body)
	return status, ""
}

func supportOf(r *http.Request) *ops.SupportAuth {
	a, _ := r.Context().Value(keySupport).(*ops.SupportAuth)
	return a
}

// ---- the firm's screen ----

func (s *Server) saScreen(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SupportAccessScreen(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) saPolicy(w http.ResponseWriter, r *http.Request) {
	var p ops.SupportPolicy
	if !decode(w, r, &p) {
		return
	}
	v, err := s.ops.SetSupportPolicy(r.Context(), tenant(r), actor(r), p)
	ok(w, v, err)
}

func (s *Server) saApprove(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Note  string   `json:"note"`
		Areas []string `json:"areas"`
		Write *bool    `json:"write"`
		Hours int      `json:"hours"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideSupportAccess(r.Context(), tenant(r), actor(r), r.PathValue("id"), true, b.Note, &ops.SupportNarrow{Areas: b.Areas, Write: b.Write, Hours: b.Hours})
	ok(w, v, err)
}

func (s *Server) saRefuse(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideSupportAccess(r.Context(), tenant(r), actor(r), r.PathValue("id"), false, b.Note, nil)
	ok(w, v, err)
}

func (s *Server) saEnd(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.EndSupportAccess(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) saLog(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 5000 {
		n = 500
	}
	v, err := s.ops.SupportAccessLog(r.Context(), tenant(r), actor(r), n)
	ok(w, map[string]any{"lines": v}, err)
}

func (s *Server) saLogCSV(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.SupportAccessLogCSV(r.Context(), tenant(r), actor(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, "support-access-log.csv", "text/csv; charset=utf-8", b)
}

// saSession is a support session's own banner: what it may do and until when.
func (s *Server) saSession(w http.ResponseWriter, r *http.Request) {
	a := supportOf(r)
	if a == nil {
		writeJSON(w, 200, map[string]any{"support": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"support": a})
}

// saSessionEnd is the End session button on a support session's banner.
func (s *Server) saSessionEnd(w http.ResponseWriter, r *http.Request) {
	a := supportOf(r)
	if a == nil {
		writeErr(w, http.StatusBadRequest, "not_support", "This is not a Ledgerline support session.", nil)
		return
	}
	_, err := s.ops.EndOwnSupportAccess(r.Context(), tenant(r), a.Operator, a.Request)
	ok(w, map[string]any{"ended": err == nil}, err)
}

// ---- the operator's side ----

func (s *Server) operatorAuth(r *http.Request) (ops.AuthState, string, error) {
	c, err := r.Cookie(operatorCookie)
	if err != nil || s.ops == nil || c.Value == "" {
		return ops.AuthState{}, "", ops.ErrSessionEnded
	}
	st, err := s.ops.SessionUser(r.Context(), ops.SupportNS, c.Value)
	return st, c.Value, err
}

// operator wraps an operator route: a signed-in operator, the sign-in
// set-up finished unless setup is allowed, and the CSRF header on changes.
func (s *Server) operator(h func(http.ResponseWriter, *http.Request, ops.AuthState), setup, mutating bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		st, _, err := s.operatorAuth(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "signed_out", "Sign in to the support desk to continue.", nil)
			return
		}
		if st.Restricted() && !setup {
			writeErr(w, http.StatusForbidden, "setup_required", "Finish setting up your sign-in (new password and two-factor authentication) first.", st)
			return
		}
		if mutating && r.Header.Get(csrfHeader) != csrfValue {
			writeErr(w, http.StatusForbidden, "csrf", "Missing the "+csrfHeader+" header.", nil)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), keyActor, st.User)), st)
	}
}

func (s *Server) opLogin(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeErr(w, 404, "not_found", "sign-in is not available", nil)
		return
	}
	var b struct {
		User     string `json:"user"`
		Password string `json:"password"`
		OTP      string `json:"otp"`
	}
	if !decode(w, r, &b) {
		return
	}
	ip := clientIP(r)
	if !s.loginLimit.allow("operator:" + ip) {
		writeErr(w, 429, "too_many_attempts", "Too many sign-in attempts from this address. Wait 15 minutes and try again.", nil)
		return
	}
	tok, st, err := s.ops.Login(r.Context(), ops.SupportNS, b.User, b.Password, b.OTP, ip, r.UserAgent())
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
	http.SetCookie(w, &http.Cookie{Name: operatorCookie, Value: tok, Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: 12 * 3600})
	writeJSON(w, 200, map[string]any{"auth": st})
}

func (s *Server) opLogout(w http.ResponseWriter, r *http.Request) {
	if _, tok, err := s.operatorAuth(r); err == nil {
		s.ops.Logout(r.Context(), ops.SupportNS, tok)
	}
	http.SetCookie(w, &http.Cookie{Name: operatorCookie, Value: "", Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, 200, map[string]any{"signed_out": true})
}

func (s *Server) opMe(w http.ResponseWriter, r *http.Request, st ops.AuthState) {
	u, _ := s.ops.UserByID(r.Context(), ops.SupportNS, st.User)
	out := map[string]any{"auth": st, "operator": st.User, "name": u.Name, "areas": ops.SupportAreas}
	if !st.Restricted() {
		out["firms"] = s.ops.OperatorFirms(r.Context(), s.Tenants())
	}
	writeJSON(w, 200, out)
}

func (s *Server) opPassword(w http.ResponseWriter, r *http.Request, st ops.AuthState) {
	var b struct{ Current, New string }
	if !decode(w, r, &b) {
		return
	}
	ok(w, map[string]any{"changed": true}, s.ops.ChangePassword(r.Context(), ops.SupportNS, st.User, b.Current, b.New))
}

func (s *Server) opTOTPSetup(w http.ResponseWriter, r *http.Request, st ops.AuthState) {
	secret, uri, err := s.ops.TOTPSetup(r.Context(), ops.SupportNS, st.User)
	ok(w, map[string]any{"secret": secret, "uri": uri}, err)
}

func (s *Server) opTOTPEnable(w http.ResponseWriter, r *http.Request, st ops.AuthState) {
	var b struct{ Code string }
	if !decode(w, r, &b) {
		return
	}
	ok(w, map[string]any{"enabled": true}, s.ops.TOTPEnable(r.Context(), ops.SupportNS, st.User, b.Code))
}

func (s *Server) opRequests(w http.ResponseWriter, r *http.Request, st ops.AuthState) {
	v, err := s.ops.OperatorRequests(r.Context(), st.User, s.Tenants())
	ok(w, map[string]any{"requests": v}, err)
}

func (s *Server) opRequest(w http.ResponseWriter, r *http.Request, st ops.AuthState) {
	var b ops.SupportAccessInput
	if !decode(w, r, &b) {
		return
	}
	if !s.knownTenant(strings.ToLower(strings.TrimSpace(b.Tenant))) {
		writeErr(w, http.StatusNotFound, "not_found", "No such firm on this server.", nil)
		return
	}
	v, err := s.ops.RequestSupportAccess(r.Context(), st.User, b)
	ok(w, v, err)
}

// opStart opens the firm session for approved access: the firm's console
// cookie is set, and the console opens with the support banner.
func (s *Server) opStart(w http.ResponseWriter, r *http.Request, st ops.AuthState) {
	t := r.PathValue("tenant")
	if !s.knownTenant(t) {
		writeErr(w, http.StatusNotFound, "not_found", "No such firm on this server.", nil)
		return
	}
	tok, err := s.ops.StartSupportSession(r.Context(), t, st.User, r.PathValue("id"), clientIP(r), r.UserAgent())
	if err != nil {
		fail(w, err, nil)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: t + "." + tok, Path: "/", HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: 12 * 3600})
	writeJSON(w, 200, map[string]any{"tenant": t, "open": "/"})
}

func (s *Server) opEnd(w http.ResponseWriter, r *http.Request, st ops.AuthState) {
	t := r.PathValue("tenant")
	if !s.knownTenant(t) {
		writeErr(w, http.StatusNotFound, "not_found", "No such firm on this server.", nil)
		return
	}
	v, err := s.ops.EndOwnSupportAccess(r.Context(), t, st.User, r.PathValue("id"))
	ok(w, v, err)
}

// ---- the provider's operator list (provider key) ----

func (s *Server) providerOperators(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SupportOperators(r.Context())
	ok(w, v, err)
}

func (s *Server) providerSaveOperator(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Active bool   `json:"active"`
		Reset  bool   `json:"reset"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, pw, err := s.ops.SaveSupportOperator(r.Context(), "provider", b.ID, b.Name, b.Active, b.Reset)
	ok(w, map[string]any{"operator": v, "temp_password": pw}, err)
}
