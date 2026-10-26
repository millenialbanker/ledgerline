package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// Support requests to Ledgerline's support team (see ops/support.go).

func (s *Server) supportRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { scopedRoutes[p] = true; s.route(p, h, m) }
	R("GET /v1/support", s.supportList, false)
	R("POST /v1/support", s.supportCreate, true)
	R("POST /v1/support/{id}", s.supportReply, true)
}

func (s *Server) supportList(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SupportRequests(r.Context(), tenant(r), actor(r))
	ok(w, map[string]any{"requests": v, "priorities": opsSupportPriorities(), "categories": opsSupportCategories(), "connected": s.ops.ControlKick != nil}, err)
}

func (s *Server) supportCreate(w http.ResponseWriter, r *http.Request) {
	var b struct{ Subject, Text, Priority, Category, Screen string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CreateSupportRequest(r.Context(), tenant(r), actor(r), b.Subject, b.Text, b.Priority, b.Category, b.Screen)
	ok(w, v, err)
}

func (s *Server) supportReply(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Text  string `json:"text"`
		Close bool   `json:"close"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReplySupportRequest(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Text, b.Close)
	ok(w, v, err)
}

func opsSupportPriorities() []string { return ops.SupportPriorities }
func opsSupportCategories() []string { return ops.SupportCategories }
