package api

import (
	"fmt"
	"net/http"

	"ledgerline/internal/ops"
)

// firmPracticeRoutes: Settings → Firm practices (ops/firmpractices.go), the
// go-live checklist, and a client's Finspot login (ops/finspotlogin.go).
// Not scoped: branch and partner logins never reach them.
func (s *Server) firmPracticeRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/firm-practices", s.firmPractices, false)
	R("POST /v1/firm-practices/{id}", s.firmPracticeAnswer, true)
	R("POST /v1/firm-practices/{id}/decide", s.firmPracticeDecide, true)
	R("GET /v1/go-live", s.goLive, false)
	R("GET /v1/finspot-logins/{ucc}", s.finspotLogin, false)
	R("POST /v1/finspot-logins/{ucc}/check", s.finspotLoginCheck, true)
	R("POST /v1/finspot-logins/{ucc}/mark", s.finspotLoginMark, true)
	R("POST /v1/finspot-logins/{ucc}/create", s.finspotLoginCreate, true)
}

func (s *Server) firmPractices(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FirmPractices(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

// firmPracticeAnswer: {"option": "on" | … | "other", "other": "…",
// "values": {...}, "note": "…"}.
func (s *Server) firmPracticeAnswer(w http.ResponseWriter, r *http.Request) {
	var b ops.PracticeAnswer
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetFirmPractice(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

// firmPracticeDecide: {"approve": true|false, "note": "…"}; false by the
// Admin who proposed it withdraws the change.
func (s *Server) firmPracticeDecide(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideFirmPractice(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Note)
	ok(w, v, err)
}

func (s *Server) goLive(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.GoLiveChecklist(r.Context(), tenant(r), actor(r))
	ok(w, map[string]any{"items": v}, err)
}

func (s *Server) finspotLogin(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FinspotLoginGet(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v, err)
}

func (s *Server) finspotLoginCheck(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FinspotLoginCheck(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v, err)
}

func (s *Server) finspotLoginMark(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Note string `json:"note"`
	}
	if !bseDecodeOptional(w, r, &b) {
		return
	}
	v, err := s.ops.FinspotLoginMark(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Note)
	ok(w, v, err)
}

func (s *Server) finspotLoginCreate(w http.ResponseWriter, r *http.Request) {
	v, run, err := s.ops.FinspotLoginCreate(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, map[string]any{"login": v, "run": run}, err)
}

// practiceGuard refuses a change, through the general settings or
// brokerage screens, to a setting that is a money-affecting firm practice:
// those go through Settings → Firm practices, where a second person
// approves them.
func practiceGuard(what string, changed bool) error {
	if !changed {
		return nil
	}
	return fmt.Errorf("%w: %s is a firm practice that affects money or client limits: change it in Settings → Firm practices, where the compliance officer or another Admin approves it", ops.ErrState, what)
}
