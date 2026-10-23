package api

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"ledgerline/internal/ops"
)

// ---- online account opening: public endpoints ----
//
// These are open without sign-in. An application is identified by the
// secret returned when it starts, sent back in the X-Application header;
// the server keeps only its hash.

const appHeader = "X-Application"

func (s *Server) onboardRoutes() {
	if s.ops == nil {
		return
	}
	h := func(p string, f func(w http.ResponseWriter, r *http.Request, tenant string)) {
		s.mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			t := r.PathValue("tenant")
			known := false
			for _, x := range s.Tenants() {
				known = known || x == t
			}
			if !known {
				writeErr(w, http.StatusNotFound, "not_found", "No such firm.", nil)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			f(w, r, t)
		})
	}
	h("GET /onboard/{tenant}/config", func(w http.ResponseWriter, r *http.Request, t string) {
		writeJSON(w, 200, s.ops.ApplyConfig(r.Context(), t))
	})
	h("POST /onboard/{tenant}/start", func(w http.ResponseWriter, r *http.Request, t string) {
		var b struct{ Mobile, Email, Partner, Lead string }
		if !decode(w, r, &b) {
			return
		}
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		a, secret, dev, err := s.ops.StartApplication(r.Context(), t, b.Mobile, b.Email, b.Partner, b.Lead, ip)
		if !s.dev {
			dev = nil
		}
		ok(w, map[string]any{"application": a.Public(), "secret": secret, "dev_codes": dev}, err)
	})
	h("GET /onboard/{tenant}/state", func(w http.ResponseWriter, r *http.Request, t string) {
		a, err := s.ops.Applicant(r.Context(), t, r.Header.Get(appHeader))
		ok(w, a.Public(), err)
	})
	h("POST /onboard/{tenant}/verify", func(w http.ResponseWriter, r *http.Request, t string) {
		var b struct{ Channel, Code string }
		if !decode(w, r, &b) {
			return
		}
		a, err := s.ops.VerifyAppCode(r.Context(), t, r.Header.Get(appHeader), b.Channel, b.Code)
		ok(w, a.Public(), err)
	})
	h("POST /onboard/{tenant}/step", func(w http.ResponseWriter, r *http.Request, t string) {
		var b ops.AppStep
		if !decode(w, r, &b) {
			return
		}
		a, err := s.ops.SaveAppStep(r.Context(), t, r.Header.Get(appHeader), b)
		ok(w, a.Public(), err)
	})
	// The applicant leaves for DigiLocker (through the KYC plugin) and comes
	// back to /open/<firm>?dl=back, where the page completes the address step.
	h("POST /onboard/{tenant}/digilocker", func(w http.ResponseWriter, r *http.Request, t string) {
		url, err := s.ops.StartDigiLocker(r.Context(), t, r.Header.Get(appHeader), s.publicURL(r)+"/open/"+t+"?dl=back")
		ok(w, map[string]string{"url": url}, err)
	})
	h("POST /onboard/{tenant}/doc", func(w http.ResponseWriter, r *http.Request, t string) {
		data, err := io.ReadAll(io.LimitReader(r.Body, 13<<20))
		if err != nil {
			writeErr(w, 400, "bad_request", "cannot read the file", nil)
			return
		}
		a, err := s.ops.AddAppDoc(r.Context(), t, r.Header.Get(appHeader), r.URL.Query().Get("kind"), strings.TrimSpace(r.URL.Query().Get("name")), data)
		ok(w, a.Public(), err)
	})
	h("POST /onboard/{tenant}/sign", func(w http.ResponseWriter, r *http.Request, t string) {
		var b struct {
			Declarations map[string]bool `json:"declarations"`
			WhatsApp     bool            `json:"whatsapp"`
			Code         string          `json:"code"`
		}
		if !decode(w, r, &b) {
			return
		}
		a, dev, err := s.ops.SignApplication(r.Context(), t, r.Header.Get(appHeader), b.Declarations, b.WhatsApp, b.Code)
		if !s.dev {
			dev = nil
		}
		ok(w, map[string]any{"application": a.Public(), "dev_codes": dev}, err)
	})
	h("GET /onboard/{tenant}/form", func(w http.ResponseWriter, r *http.Request, t string) {
		a, err := s.ops.Applicant(r.Context(), t, r.URL.Query().Get("s"))
		if err != nil {
			fail(w, err, nil)
			return
		}
		b, err := s.ops.AppForm(r.Context(), t, a)
		if err != nil {
			fail(w, err, nil)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Write(b)
	})
}

// ---- staff ----

func (s *Server) applications(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Applications(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) rejectApplication(w http.ResponseWriter, r *http.Request) {
	var b struct{ Reason string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RejectApplication(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Reason)
	ok(w, v.Public(), err)
}

func (s *Server) applicationDoc(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.PathValue("n"))
	if err := s.ops.Require(r.Context(), tenant(r), actor(r), "Viewing applications", ops.RoleMaker, ops.RoleChecker, ops.RoleCompliance); err != nil {
		fail(w, err, nil)
		return
	}
	d, b, err := s.ops.AppDocFile(r.Context(), tenant(r), actor(r), r.PathValue("id"), n)
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", d.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	csp := "default-src 'none'; sandbox"
	if strings.HasPrefix(d.ContentType, "text/html") {
		csp = "default-src 'none'; style-src 'unsafe-inline'; sandbox"
	} else if strings.HasPrefix(d.ContentType, "video/") || strings.HasPrefix(d.ContentType, "image/") {
		csp = "default-src 'none'; media-src 'self'; img-src 'self'"
	}
	w.Header().Set("Content-Security-Policy", csp)
	w.Write(b)
}

// publicURL is where browsers reach this server: LEDGERLINE_PUBLIC_URL, or
// the request's own host.
func (s *Server) publicURL(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
