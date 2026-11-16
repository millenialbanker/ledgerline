package api

import "net/http"

// Boards: my cards across boards, for the boards home
// (internal/ops/boardviews.go, web/boardviews.js).

func init() {
	boardRouteHooks = append(boardRouteHooks, func(s *Server, R func(p string, h http.HandlerFunc, m bool)) {
		R("GET /v1/boards/my-cards", s.myCards, false)
	})
}

func (s *Server) myCards(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MyCards(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}
