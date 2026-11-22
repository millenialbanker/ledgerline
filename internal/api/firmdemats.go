package api

import (
	"io"
	"net/http"

	"ledgerline/internal/ops"
)

// firmDematRoutes: the firm's own demat accounts tagged with their holding
// statement types (ops/firmdemats.go).
func (s *Server) firmDematRoutes() {
	s.route("GET /v1/firm-demats", s.firmDematsGet, false)
	s.route("PUT /v1/firm-demats", s.firmDematsPut, true)
	s.route("POST /v1/firm-demats/from-statement", s.firmDematsFromStatement, true)
}

func (s *Server) firmDematsGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FirmDemats(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) firmDematsPut(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Tags    map[string]string `json:"tags"`
		FirmUCC string            `json:"firm_ucc"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveFirmDemats(r.Context(), tenant(r), actor(r), b.Tags, b.FirmUCC)
	ok(w, v, err)
}

// firmDematsFromStatement takes the previous system's holding statement
// file as the body (?name=): its accounts' types only.
func (s *Server) firmDematsFromStatement(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, ops.MaxXFileBytes+1))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	v, found, err := s.ops.FirmDematsFromStatement(r.Context(), tenant(r), actor(r), r.URL.Query().Get("name"), data)
	ok(w, map[string]any{"view": v, "found": found}, err)
}
