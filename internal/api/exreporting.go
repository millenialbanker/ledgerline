package api

import (
	"io"
	"net/http"
	"sync"

	"ledgerline/internal/ops"
)

// exchangeReportingRouted makes exchangeReportingRoutes register once per
// server, however it is wired.
var exchangeReportingRouted sync.Map

// exchangeReportingRoutes: the reports sent to the exchanges and NSE
// Clearing from Exchange files: the SEGMON check before the client funds
// file is made, NSE Clearing's SEGMON response files, and the holding
// statement submitted to NSE by API.
func (s *Server) exchangeReportingRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := exchangeReportingRouted.LoadOrStore(s, true); done {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/segmon/check", s.segmonCheck, false)
	R("POST /v1/segmon/response", s.segmonResponse, true)
	R("GET /v1/segmon/responses", s.segmonResponses, false)
	R("GET /v1/holding-api", s.holdingAPIView, false)
	R("POST /v1/holding-api/submit", s.holdingAPISubmit, true)
	R("POST /v1/holding-api/nil", s.holdingAPINil, true)
	R("PUT /v1/holding-api/special-sessions", s.holdingAPISessions, true)
	s.complianceCalendarRoutes(R)
	s.clientListRoutes(R)
	s.surveillanceReturnRoutes(R)
	s.upiCollectRoutes(R)
	s.apComplaintsRoutes(R)
	s.clientMoneyRoutes(R)
	s.fatcaRoutes(R)
	s.sanctionsRoutes(R)
}

func (s *Server) segmonCheck(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SEGMONCheck(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) segmonResponse(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, ops.MaxXFileBytes+1))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	q := r.URL.Query()
	v, err := s.ops.SEGMONResponseFile(r.Context(), tenant(r), actor(r), q.Get("date"), q.Get("name"), data)
	ok(w, v, err)
}

func (s *Server) segmonResponses(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SEGMONResponses(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) holdingAPIView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.NSEHolding(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) holdingAPISubmit(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.NSEHoldingSubmit(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, v, err)
}

// holdingAPINil records NSE's NIL Holding declaration or bulk nil report
// made on NSE's member portal, with its acknowledgement.
func (s *Server) holdingAPINil(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Kind  string   `json:"kind"`
		Dates []string `json:"dates"`
		Ack   string   `json:"ack"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.NSEHoldingNil(r.Context(), tenant(r), actor(r), b.Kind, b.Dates, b.Ack)
	ok(w, v, err)
}

// holdingAPISessions sets the Sunday special session days.
func (s *Server) holdingAPISessions(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Dates []string `json:"dates"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetSpecialSessions(r.Context(), tenant(r), actor(r), b.Dates)
	ok(w, v, err)
}
