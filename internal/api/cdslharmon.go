package api

import (
	"net/http"
	"sync"
)

var cdslHarmonRouted sync.Map // *Server -> registered

// cdslHarmonRoutes: CDSL's freezes from the freeze master (DPE6) and the
// BO upload formats (internal/ops/cdslfreeze.go, cdslharmon.go). The
// client master's view of each account comes with the DP desk
// (GET /v1/dp: each row's cdsl, ddpi and cdsl_freeze).
func (s *Server) cdslHarmonRoutes() {
	if s.ops == nil {
		return
	}
	if _, dup := cdslHarmonRouted.LoadOrStore(s, true); dup {
		return
	}
	s.route("GET /v1/cdsl/freezes", s.cdslFreezes, false)
}

func (s *Server) cdslFreezes(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CDSLFreezes(r.Context(), tenant(r), actor(r), r.URL.Query().Get("ucc"))
	ok(w, v, err)
}
