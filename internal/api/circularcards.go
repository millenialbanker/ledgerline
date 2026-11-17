package api

import (
	"net/http"
	"sync"

	"ledgerline/internal/ops"
)

// circularCardRoutes: the firm profile the circulars' applicability rules
// read (Settings → Firm profile), reading a circular with AI for proposed
// obligations, and the Calendar & circulars screen's state (AI, the
// Ledgerline areas, the Circulars board). Reviewing, deciding whether a
// circular applies and accepting obligations go through
// POST /v1/circulars/{id} (ops.go). Head-office routes only.
var circularCardRouted sync.Map

func (s *Server) circularCardRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := circularCardRouted.LoadOrStore(s, true); done {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/firm-profile", s.firmProfile, false)
	R("PUT /v1/firm-profile", s.saveFirmProfile, true)
	R("GET /v1/circulars/state", s.circularsState, false)
	R("POST /v1/circulars/{id}/ai", s.readCircularAI, true)
}

func (s *Server) firmProfile(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FirmProfileFor(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) saveFirmProfile(w http.ResponseWriter, r *http.Request) {
	var b ops.FirmProfile
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveFirmProfile(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) circularsState(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CircularsStateFor(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) readCircularAI(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ReadCircularAI(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}
