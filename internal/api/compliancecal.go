package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// complianceCalendarRoutes: the compliance profile with the exchanges' RAS
// calendar and the system audit cycle, and the holding statement sent to
// BSE on its portal. Registered from exchangeReportingRoutes.
func (s *Server) complianceCalendarRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/compliance/profile", s.compProfile, false)
	R("PUT /v1/compliance/profile", s.saveCompProfile, true)
	R("GET /v1/holding-api/bse", s.bseHoldingView, false)
	R("POST /v1/holding-api/bse", s.bseHoldingUploaded, true)
}

func (s *Server) compProfile(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CompProfileFor(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) saveCompProfile(w http.ResponseWriter, r *http.Request) {
	var b ops.ComplianceProfile
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveCompProfile(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) bseHoldingView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BSEHolding(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) bseHoldingUploaded(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
		Ack  string `json:"ack"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.BSEHoldingUploaded(r.Context(), tenant(r), actor(r), b.Date, b.Ack)
	ok(w, v, err)
}
