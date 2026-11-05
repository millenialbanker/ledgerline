package api

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// mcxUCCRouted makes mcxUCCRoutes register once per server.
var mcxUCCRouted sync.Map

// mcxUCCRoutes: registering clients' UCC at MCX by its UCC file (MCX has
// no UCC API): the clients and files, a client's record, the next file,
// and marking a file uploaded. MCX's response file is loaded on Exchange
// files.
func (s *Server) mcxUCCRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := mcxUCCRouted.LoadOrStore(s, true); done {
		return
	}
	s.route("GET /v1/ucc-api/mcx", s.mcxUCCView, false)
	s.route("GET /v1/ucc-api/mcx/preview/{ucc}", s.mcxUCCPreview, false)
	s.route("GET /v1/ucc-api/mcx/batch-file", s.mcxUCCBatch, false)
	s.route("POST /v1/ucc-api/mcx/batch-file/uploaded", s.mcxUCCUploaded, true)
}

func (s *Server) mcxUCCView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MCXUCCView(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) mcxUCCPreview(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MCXUCCPreview(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v, err)
}

func (s *Server) mcxUCCBatch(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.MCXUCCBatchFile(r.Context(), tenant(r), actor(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.NewReplacer(`"`, "", "\\", "").Replace(b.Name)+`"`)
	// Which clients are in the file and which were left out, for the screen.
	w.Header().Set("X-Clients", strings.Join(b.Clients, ","))
	w.Header().Set("X-Left-Out", strconv.Itoa(len(b.Problems)))
	w.Write(b.Content)
}

func (s *Server) mcxUCCUploaded(w http.ResponseWriter, r *http.Request) {
	var in struct {
		File string `json:"file"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := s.ops.MCXUCCMarkUploaded(r.Context(), tenant(r), actor(r), in.File)
	ok(w, v, err)
}
