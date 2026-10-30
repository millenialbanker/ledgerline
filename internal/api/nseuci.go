package api

import (
	"net/http"
	"sync"

	"ledgerline/internal/ops"
)

// nseUCIRouted makes nseUCIRoutes register once per server, however it is
// wired.
var nseUCIRouted sync.Map

// nseUCIRoutes: the NSE tab of the Exchange UCC screen (NSE's UCI API):
// the clients and their registration, sending new and changed clients,
// PAN status, a client's records before sending, and the exchange UCC
// details kept per client (shared with the other exchanges' uploads).
func (s *Server) nseUCIRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := nseUCIRouted.LoadOrStore(s, true); done {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/ucc-api/nse", s.nseUCIView, false)
	R("POST /v1/ucc-api/nse/send", s.nseUCISend, true)
	R("POST /v1/ucc-api/nse/pan-status", s.nseUCIPAN, true)
	R("GET /v1/ucc-api/nse/preview/{ucc}", s.nseUCIPreview, false)
	R("GET /v1/ucc-api/details/{ucc}", s.uccDetails, false)
	R("POST /v1/ucc-api/details/{ucc}", s.uccSaveDetails, true)
}

type uccList struct {
	UCCs []string `json:"uccs"`
}

func (s *Server) nseUCIView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.NSEUCI(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) nseUCISend(w http.ResponseWriter, r *http.Request) {
	var b uccList
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.NSEUCISend(r.Context(), tenant(r), actor(r), b.UCCs)
	ok(w, v, err)
}

func (s *Server) nseUCIPAN(w http.ResponseWriter, r *http.Request) {
	var b uccList
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.NSEUCIPANStatus(r.Context(), tenant(r), actor(r), b.UCCs)
	ok(w, v, err)
}

func (s *Server) nseUCIPreview(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.NSEUCIPreview(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v, err)
}

func (s *Server) uccDetails(w http.ResponseWriter, r *http.Request) {
	if _, err := s.ops.NSEUCIPreview(r.Context(), tenant(r), actor(r), r.PathValue("ucc")); err != nil {
		fail(w, err, nil) // the same permission and client checks
		return
	}
	ok(w, s.ops.UCCDetailsFor(r.Context(), tenant(r), r.PathValue("ucc")), nil)
}

func (s *Server) uccSaveDetails(w http.ResponseWriter, r *http.Request) {
	var b ops.UCCDetails
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveUCCDetails(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b)
	ok(w, v, err)
}
