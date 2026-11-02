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

// upiCollectRoutes: the firm's validated UPI IDs and SEBI QR code, shown in
// the client portal. Registered from exchangeReportingRoutes.
func (s *Server) upiCollectRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/settings/upi-collect", s.upiCollect, false)
	R("PUT /v1/settings/upi-collect", s.saveUPICollect, true)
}

func (s *Server) upiCollect(w http.ResponseWriter, r *http.Request) {
	ok(w, s.ops.UPICollectConfig(r.Context(), tenant(r)), nil)
}

func (s *Server) saveUPICollect(w http.ResponseWriter, r *http.Request) {
	var b ops.UPICollect
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveUPICollect(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

// surveillanceReturnRoutes: the quarterly surveillance obligation report
// (Formats I to III), referring an alert to the exchange and the major
// surveillance actions of a quarter. Registered from exchangeReportingRoutes.
func (s *Server) surveillanceReturnRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/surveillance-return", s.survReturn, false)
	R("POST /v1/surveillance-return/refer", s.survRefer, true)
	R("POST /v1/surveillance-return/action", s.survAction, true)
}

func (s *Server) survReturn(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SurvReturnFor(r.Context(), tenant(r), actor(r), r.URL.Query().Get("quarter"))
	ok(w, v, err)
}

func (s *Server) survRefer(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ID          string `json:"id"`
		Date        string `json:"date"`
		Observation string `json:"observation"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReferAlert(r.Context(), tenant(r), actor(r), b.ID, b.Date, b.Observation)
	ok(w, v, err)
}

func (s *Server) survAction(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Quarter string `json:"quarter"`
		Text    string `json:"text"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AddSurvAction(r.Context(), tenant(r), actor(r), b.Quarter, b.Text)
	ok(w, v, err)
}
