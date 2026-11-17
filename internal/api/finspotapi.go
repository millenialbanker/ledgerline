package api

import (
	"net/http"
	"sync"
)

// finspotAPIRouted makes finspotAPIRoutes register once per server.
var finspotAPIRouted sync.Map

// finspotAPIRoutes: Finspot by its Noren admin API (ops/finspotapi.go): the
// panel on Live RMS, pushing now, a push's record and reading a client back.
func (s *Server) finspotAPIRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := finspotAPIRouted.LoadOrStore(s, true); done {
		return
	}
	s.route("GET /v1/finspot-api", s.finspotAPIView, false)
	s.route("GET /v1/finspot-api/runs/{id}", s.finspotAPIRun, false)
	s.route("POST /v1/finspot-api/push", s.finspotAPIPush, true)
	s.route("POST /v1/finspot-api/check/{ucc}", s.finspotAPICheck, true)
}

func (s *Server) finspotAPIView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FinspotAPIPanel(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	if f := s.scope(r); f != nil && err == nil {
		rows := v.Rows[:0]
		for _, x := range v.Rows {
			if f(x.UCC) {
				rows = append(rows, x)
			}
		}
		v.Rows = rows
	}
	ok(w, v, err)
}

func (s *Server) finspotAPIRun(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FinspotAPIRunGet(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

// finspotAPIPush pushes now: {"parts": ["limits", "holdings", …], "uccs":
// [...], "date": ...}; every part the connection sends by API, and every
// client, when none is named.
func (s *Server) finspotAPIPush(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Parts []string `json:"parts"`
		UCCs  []string `json:"uccs"`
		Date  string   `json:"date"`
	}
	if !bseDecodeOptional(w, r, &b) {
		return
	}
	v, err := s.ops.FinspotAPIPush(r.Context(), tenant(r), actor(r), b.Date, b.Parts, b.UCCs)
	ok(w, v, err)
}

func (s *Server) finspotAPICheck(w http.ResponseWriter, r *http.Request) {
	ucc := r.PathValue("ucc")
	if !s.canSee(w, r, ucc) {
		return
	}
	v, err := s.ops.FinspotAPICheckClient(r.Context(), tenant(r), actor(r), ucc)
	ok(w, v, err)
}
