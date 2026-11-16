package api

import "net/http"

// Ledgerline boards (internal/ops/boardsync.go): setting them up, the
// boards home's counts, and linking any card to a client or record.

func init() {
	boardRouteHooks = append(boardRouteHooks, func(s *Server, R func(p string, h http.HandlerFunc, m bool)) {
		R("GET /v1/boards/ledgerline", s.ledgerlineBoards, false)
		R("POST /v1/boards/ledgerline", s.setUpLedgerlineBoards, true)
		R("GET /v1/boards/link-search", s.boardLinkSearch, false)
		R("GET /v1/boards/link", s.boardLinkRecord, false)
	})
}

func (s *Server) ledgerlineBoards(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.LedgerlineBoards(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) setUpLedgerlineBoards(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Sources []string `json:"sources"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetUpLedgerlineBoards(r.Context(), tenant(r), actor(r), b.Sources)
	ok(w, map[string]any{"boards": v}, err)
}

func (s *Server) boardLinkSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.BoardLinkSearch(r.Context(), tenant(r), actor(r), q.Get("q"), q.Get("kind"))
	ok(w, v, err)
}

func (s *Server) boardLinkRecord(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.BoardLinkRecord(r.Context(), tenant(r), actor(r), q.Get("kind"), q.Get("id"))
	ok(w, v, err)
}
