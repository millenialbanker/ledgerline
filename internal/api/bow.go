package api

import (
	"net/http"
	"sync"

	"ledgerline/internal/ops"
)

// bowRouted makes bowRoutes register once per server.
var bowRouted sync.Map

// bowRoutes: client limits to BOW (BOLTPlusOnWeb) by its API
// (ops/bowapi.go): the panel on Live RMS, pushing now, the OTP sign-in,
// a client's BOW user and BOW's client master, and a push's record.
func (s *Server) bowRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := bowRouted.LoadOrStore(s, true); done {
		return
	}
	s.route("GET /v1/bow", s.bowView, false)
	s.route("GET /v1/bow/runs/{id}", s.bowRun, false)
	s.route("POST /v1/bow/push", s.bowPush, true)
	s.route("POST /v1/bow/sign-in", s.bowSignIn, true)
	s.route("POST /v1/bow/users/{ucc}", s.bowUser, true)
	s.route("POST /v1/bow/master", s.bowMaster, true)
}

func (s *Server) bowView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BOWLimitsView(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
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

func (s *Server) bowRun(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BOWRun(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

// bowPush pushes the clients named ({"uccs": [...], "date": ...}), or every
// client when none is.
func (s *Server) bowPush(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCCs []string `json:"uccs"`
		Date string   `json:"date"`
	}
	if !bseDecodeOptional(w, r, &b) {
		return
	}
	v, err := s.ops.BOWPush(r.Context(), tenant(r), actor(r), b.Date, b.UCCs)
	ok(w, v, err)
}

// bowSignIn: {} asks BOW for the OTP (or signs in, two-factor off);
// {"otp": "…"} finishes the sign-in.
func (s *Server) bowSignIn(w http.ResponseWriter, r *http.Request) {
	var b struct {
		OTP string `json:"otp"`
	}
	if !bseDecodeOptional(w, r, &b) {
		return
	}
	v, err := s.ops.BOWSignIn(r.Context(), tenant(r), actor(r), b.OTP)
	ok(w, v, err)
}

// bowUser sets a client's BOW user by hand ({"usid": ""} clears it).
func (s *Server) bowUser(w http.ResponseWriter, r *http.Request) {
	var b ops.BOWUser
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetBOWUser(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b)
	ok(w, v, err)
}

func (s *Server) bowMaster(w http.ResponseWriter, r *http.Request) {
	total, matched, err := s.ops.BOWRefreshMaster(r.Context(), tenant(r), actor(r))
	ok(w, map[string]int{"users": total, "matched": matched}, err)
}
