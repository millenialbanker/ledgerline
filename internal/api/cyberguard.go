package api

import (
	"net/http"
	"strings"
	"time"

	"ledgerline/internal/ops"
)

// ---- API callers: the firm's allow-list and rate limit ----
//
// CSCRF PR.AA.S17 asks for rate limiting and whitelisted API connections.
// A firm lists the addresses its API keys and its webhooks may be called
// from, and the calls a key may make each minute (Security → Cyber
// controls → Settings; ops/cyberset.go). An empty list lets any address
// call, as before. A refused call is a security event. The rules are read
// at most every 30 seconds per firm.

type guardEntry struct {
	at time.Time
	g  ops.APIGuard
}

func (s *Server) guardOf(r *http.Request, tenant string) ops.APIGuard {
	if v, ok := s.guards.Load(tenant); ok {
		if e := v.(guardEntry); time.Since(e.at) < 30*time.Second {
			return e.g
		}
	}
	g := s.ops.APIGuardOf(r.Context(), tenant)
	s.guards.Store(tenant, guardEntry{at: time.Now(), g: g})
	return g
}

// limitOf is the firm's per-minute limiter for one key.
func (s *Server) limitOf(key string, perMin int) *limiter {
	if v, ok := s.keyLimits.Load(key); ok {
		l := v.(*limiter)
		l.mu.Lock()
		l.max = perMin
		l.mu.Unlock()
		return l
	}
	v, _ := s.keyLimits.LoadOrStore(key, &limiter{max: perMin, per: time.Minute})
	return v.(*limiter)
}

// apiCallerOK checks an API-key call against the firm's rules.
func (s *Server) apiCallerOK(w http.ResponseWriter, r *http.Request, tenant string) bool {
	if s.ops == nil {
		return true
	}
	g := s.guardOf(r, tenant)
	ip := clientIP(r)
	if !ops.AllowedCaller(g.API, ip) {
		s.ops.NoteRefusedCaller(r.Context(), tenant, "not on the firm's list of API callers", ip, r.URL.Path)
		writeErr(w, http.StatusForbidden, "caller_not_allowed", "This address may not call the firm's API. The firm sets the allowed addresses under Security → Cyber controls.", nil)
		return false
	}
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !s.limitOf("key|"+tenant+"|"+tokenHint(tok), g.PerMinute).allow("k") {
		s.ops.NoteRefusedCaller(r.Context(), tenant, "rate limit of the API key", ip, r.URL.Path)
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "Too many calls with this API key this minute. Slow down and try again.", nil)
		return false
	}
	return true
}

// hookCallerOK checks a webhook call (/hooks/{tenant}/...).
func (s *Server) hookCallerOK(w http.ResponseWriter, r *http.Request) bool {
	if s.ops == nil {
		return true
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/hooks/")
	if !ok {
		return true
	}
	tenant, _, _ := strings.Cut(rest, "/")
	if !s.knownTenant(tenant) {
		return true // the handler answers for an unknown firm
	}
	g := s.guardOf(r, tenant)
	ip := clientIP(r)
	if !ops.AllowedCaller(g.Hooks, ip) {
		s.ops.NoteRefusedCaller(r.Context(), tenant, "not on the firm's list of webhook callers", ip, r.URL.Path)
		writeErr(w, http.StatusForbidden, "caller_not_allowed", "This address may not call the firm's webhooks.", nil)
		return false
	}
	if !s.limitOf("hook|"+tenant, max(g.PerMinute, 600)).allow("h") {
		w.Header().Set("Retry-After", "60")
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "Too many webhook calls this minute.", nil)
		return false
	}
	return true
}

// tokenHint is a short, non-reversible name for a key.
func tokenHint(tok string) string {
	if len(tok) < 8 {
		return tok
	}
	return tok[:4] + "…" + tok[len(tok)-4:]
}
