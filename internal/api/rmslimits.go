package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// rmsLimitRoutes: position limits, index limits, the F&O ban period,
// surveillance measures and their settings (ops/rmslimits.go).
func (s *Server) rmsLimitRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/rms/limits", s.rmsLimits, false)
	R("POST /v1/rms/limits/run", s.rmsLimitsRun, true)
	R("GET /v1/rms/limits/config", s.rmsLimitsConfig, false)
	R("PUT /v1/rms/limits/config", s.rmsLimitsConfigPut, true)
	R("GET /v1/rms/index-cover-report", s.rmsIndexCover, false)
	R("GET /v1/rms/surveillance-file", s.rmsSurvFile, false)
}

func (s *Server) rmsLimits(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.RiskLimits(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) rmsLimitsRun(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RunRiskLimitChecks(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, map[string]any{"date": v.Date, "raised": v.Raised, "view": v.View}, err)
}

func (s *Server) rmsLimitsConfig(w http.ResponseWriter, r *http.Request) {
	ok(w, s.ops.RiskLimitConfig(r.Context(), tenant(r)), nil)
}

func (s *Server) rmsLimitsConfigPut(w http.ResponseWriter, r *http.Request) {
	var c ops.RiskLimitConfig
	if !decode(w, r, &c) {
		return
	}
	v, err := s.ops.SaveRiskLimitConfig(r.Context(), tenant(r), actor(r), c)
	ok(w, v, err)
}

func (s *Server) rmsIndexCover(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	b, _, err := s.ops.IndexCoverReport(r.Context(), tenant(r), actor(r), d)
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, "index-option-cover-"+d+".csv", "text/csv", b)
}

func (s *Server) rmsSurvFile(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	sendFile(w, "surveillance-margins-"+d+".csv", "text/csv", s.ops.SurvMarginFile(r.Context(), tenant(r), d))
}
