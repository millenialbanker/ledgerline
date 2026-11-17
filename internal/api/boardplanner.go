package api

import (
	"net/http"
	"strconv"

	"ledgerline/internal/ops"
)

// Boards: the Inbox and the Planner
// (internal/ops/boardplanner.go, web/boardplanner.js). Calendar feeds were
// taken out: they keep card titles on servers outside India.

func init() {
	boardRouteHooks = append(boardRouteHooks, func(s *Server, R func(p string, h http.HandlerFunc, m bool)) {
		R("GET /v1/board-inbox", s.bpInbox, false)
		R("POST /v1/board-inbox/cards", s.bpInboxAdd, true)
		R("POST /v1/board-inbox/review", s.bpInboxReview, true) // compliance or Admin: read-only, with a reason (audited)
		R("GET /v1/board-planner", s.bpPlanner, false)
		R("POST /v1/board-planner/blocks", s.bpBlockAdd, true)
		R("PATCH /v1/board-planner/blocks/{id}", s.bpBlockEdit, true)
		R("POST /v1/board-planner/blocks/{id}/delete", s.bpBlockDelete, true)
		R("POST /v1/board-planner/blocks/{id}/set-due", s.bpBlockDue, true)
	})
}

func (s *Server) bpInbox(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Inbox(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) bpInboxAdd(w http.ResponseWriter, r *http.Request) {
	var b ops.CardInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AddInboxCard(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) bpInboxReview(w http.ResponseWriter, r *http.Request) {
	var b struct {
		User   string `json:"user"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.OpenInboxForReview(r.Context(), tenant(r), actor(r), b.User, b.Reason)
	ok(w, v, err)
}

func (s *Server) bpPlanner(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	v, err := s.ops.Planner(r.Context(), tenant(r), actor(r), r.URL.Query().Get("from"), days)
	ok(w, v, err)
}

func (s *Server) bpBlockAdd(w http.ResponseWriter, r *http.Request) {
	var b ops.PlanBlockInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CreatePlanBlock(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) bpBlockEdit(w http.ResponseWriter, r *http.Request) {
	var b ops.PlanBlockInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.UpdatePlanBlock(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) bpBlockDelete(w http.ResponseWriter, r *http.Request) {
	err := s.ops.DeletePlanBlock(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]bool{"deleted": err == nil}, err)
}

func (s *Server) bpBlockDue(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PlanSetDue(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}
