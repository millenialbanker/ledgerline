package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// bseUCCRouted makes bseUCCRoutes register once per server.
var bseUCCRouted sync.Map

// bseUCCRoutes: registering clients' UCC at BSE through BSE's UCC REST API,
// BSE's PAN verification, the API user's password, and the batch upload
// file for BSE's UCC portal when the API is not used.
func (s *Server) bseUCCRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := bseUCCRouted.LoadOrStore(s, true); done {
		return
	}
	s.route("GET /v1/ucc-api/bse", s.bseUCCView, false)
	s.route("POST /v1/ucc-api/bse/send", s.bseUCCSend, true)
	s.route("POST /v1/ucc-api/bse/pan-status", s.bseUCCPAN, true)
	s.route("GET /v1/ucc-api/bse/preview/{ucc}", s.bseUCCPreview, false)
	s.route("POST /v1/ucc-api/bse/reset-password", s.bseUCCReset, true)
	s.route("GET /v1/ucc-api/bse/batch-file", s.bseUCCBatch, false)
	s.route("POST /v1/ucc-api/bse/batch-file/uploaded", s.bseUCCUploaded, true)
}

// bseDecodeOptional reads a JSON body that may be empty.
func bseDecodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, 400, "bad_json", "Request body is not valid JSON for this endpoint: "+err.Error(), nil)
		return false
	}
	return true
}

func (s *Server) bseUCCView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BSEUCCView(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

type bseUCCList struct {
	UCCs []string `json:"uccs"`
}

func (s *Server) bseUCCSend(w http.ResponseWriter, r *http.Request) {
	var in bseUCCList
	if !bseDecodeOptional(w, r, &in) {
		return
	}
	v, err := s.ops.BSEUCCSend(r.Context(), tenant(r), actor(r), in.UCCs)
	ok(w, v, err)
}

func (s *Server) bseUCCPAN(w http.ResponseWriter, r *http.Request) {
	var in bseUCCList
	if !bseDecodeOptional(w, r, &in) {
		return
	}
	v, err := s.ops.BSEUCCPANStatus(r.Context(), tenant(r), actor(r), in.UCCs)
	ok(w, v, err)
}

func (s *Server) bseUCCPreview(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BSEUCCPreview(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v, err)
}

func (s *Server) bseUCCReset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		NewPassword string `json:"new_password"`
	}
	if !decode(w, r, &in) {
		return
	}
	msg, err := s.ops.BSEUCCResetPassword(r.Context(), tenant(r), actor(r), in.NewPassword)
	ok(w, map[string]string{"message": msg}, err)
}

func (s *Server) bseUCCBatch(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.BSEUCCBatchFile(r.Context(), tenant(r), actor(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.NewReplacer(`"`, "", "\\", "").Replace(b.Name)+`"`)
	// Which clients are in the file and which were left out, for the screen.
	w.Header().Set("X-Clients", strings.Join(b.Clients, ","))
	w.Header().Set("X-Left-Out", strconv.Itoa(len(b.Problems)))
	w.Write(b.Content)
}

func (s *Server) bseUCCUploaded(w http.ResponseWriter, r *http.Request) {
	var in struct {
		File string   `json:"file"`
		UCCs []string `json:"uccs"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := s.ops.BSEUCCMarkUploaded(r.Context(), tenant(r), actor(r), in.File, in.UCCs)
	ok(w, v, err)
}
