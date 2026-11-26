package api

import (
	"net/http"
	"sync"

	"ledgerline/internal/ops"
)

// firmModuleRoutes: Settings → Firm modules (ops/firmmodules.go). Every
// staff member reads it; the compliance officer and an Admin change it.
// The preview says what a change switches on and off before it is saved.
var firmModuleRouted sync.Map

func (s *Server) firmModuleRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := firmModuleRouted.LoadOrStore(s, true); done {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/firm-modules", s.firmModules, false)
	R("PUT /v1/firm-modules", s.saveFirmModules, true)
	R("POST /v1/firm-modules/preview", s.previewFirmModules, false)
	R("POST /v1/firm-modules/recount", s.recountClients, true)
}

func (s *Server) firmModules(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FirmModulesFor(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) saveFirmModules(w http.ResponseWriter, r *http.Request) {
	var b ops.FirmModulesReq
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveFirmModules(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) previewFirmModules(w http.ResponseWriter, r *http.Request) {
	var b ops.FirmModulesReq
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PreviewFirmModules(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) recountClients(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RecountClients(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}
