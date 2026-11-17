package api

import (
	"net/http"
	"sync"

	"ledgerline/internal/ops"
)

// uiLayoutRouted makes uiLayoutRoutes register once per server.
var uiLayoutRouted sync.Map

// uiLayoutRoutes: the screen layout (ops/uilayout.go). Each user reads and
// changes their own choices; an Admin sets the firm's policy.
func (s *Server) uiLayoutRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := uiLayoutRouted.LoadOrStore(s, true); done {
		return
	}
	s.route("GET /v1/me/ui", s.uiLayoutView, false)
	s.route("PUT /v1/me/ui", s.uiLayoutMine, true)
	s.route("PUT /v1/ui/policy", s.uiLayoutPolicy, true)
}

func (s *Server) uiLayoutView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.UILayout(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) uiLayoutMine(w http.ResponseWriter, r *http.Request) {
	var b ops.UIPrefPatch
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveUIPref(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) uiLayoutPolicy(w http.ResponseWriter, r *http.Request) {
	var b ops.UIPolicy
	if !decode(w, r, &b) {
		return
	}
	if !s.allowed(w, r, "Setting the firm's screen layout", ops.RoleAdmin) {
		return
	}
	v, err := s.ops.SaveUIPolicy(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}
