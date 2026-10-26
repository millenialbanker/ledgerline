package api

import "net/http"

// noticeRoutes: notices from the provider (Ledgerline Control).
func (s *Server) noticeRoutes() {
	s.route("GET /v1/notices", s.notices, false)
	scopedRoutes["GET /v1/notices"] = true
}

func (s *Server) notices(w http.ResponseWriter, r *http.Request) {
	ns, err := s.ops.Notices(r.Context(), tenant(r), actor(r))
	ok(w, ns, err)
}
