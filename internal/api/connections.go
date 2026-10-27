package api

import "net/http"

// connectionRoutes: the firm's accounts with outside services.
func (s *Server) connectionRoutes() {
	s.route("GET /v1/connections", s.connections, false)
	s.route("PUT /v1/connections/{id}", s.saveConnection, true)
	s.route("POST /v1/connections/{id}/test", s.testConnection, true)
	s.route("POST /v1/connections/{id}/remove", s.removeConnection, true)
}

func (s *Server) connections(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Connections(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) saveConnection(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Values  map[string]string `json:"values"`
		Secrets map[string]string `json:"secrets"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveConnection(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Values, b.Secrets)
	ok(w, v, err)
}

func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.TestConnection(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) removeConnection(w http.ResponseWriter, r *http.Request) {
	err := s.ops.RemoveConnection(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]bool{"ok": err == nil}, err)
}
