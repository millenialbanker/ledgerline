package api

import (
	"net/http"
	"sync"
)

var mcxRouted sync.Map // *Server -> true

// mcxRoutes: MCX and MCXCCL data loaded from their files (the files
// themselves are uploaded on Exchange files; their reconciliations are on
// Clearing data).
func (s *Server) mcxRoutes() {
	if s.ops == nil {
		return
	}
	if _, dup := mcxRouted.LoadOrStore(s, true); dup {
		return
	}
	s.route("GET /v1/mcx/contracts", s.mcxContracts, false)
	s.route("GET /v1/mcx/sftp", s.mcxSFTPStatus, false)
	s.route("POST /v1/mcx/sftp/fetch", s.mcxSFTPFetch, true)
	s.route("GET /v1/bse/sftp", s.bseSFTPStatus, false)
	s.route("POST /v1/bse/sftp/fetch", s.bseSFTPFetch, true)
}

// bseSFTPStatus is the BSE SFTP feed's last run and latest files.
func (s *Server) bseSFTPStatus(w http.ResponseWriter, r *http.Request) {
	ok(w, s.ops.BSESFTPStatus(r.Context(), tenant(r)), nil)
}

// bseSFTPFetch fetches new files from BSE's SFTP now.
func (s *Server) bseSFTPFetch(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BSESFTPFetch(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

// mcxContracts is the latest MCX product master (?symbol= for one).
func (s *Server) mcxContracts(w http.ResponseWriter, r *http.Request) {
	m, err := s.ops.MCXContracts(r.Context(), tenant(r), r.URL.Query().Get("symbol"))
	ok(w, m, err)
}

// mcxSFTPStatus is the MCX SFTP feed's last run and latest files.
func (s *Server) mcxSFTPStatus(w http.ResponseWriter, r *http.Request) {
	ok(w, s.ops.MCXSFTPStatus(r.Context(), tenant(r)), nil)
}

// mcxSFTPFetch fetches new files from MCX's SFTP now.
func (s *Server) mcxSFTPFetch(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MCXSFTPFetch(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}
