package api

import (
	"net/http"
	"sync"

	"ledgerline/internal/ops"
)

// selfInspectRouted makes selfInspectRoutes register once per server.
var selfInspectRouted sync.Map

// selfInspectRoutes: the nightly self-inspection (internal/ops/
// selfinspect.go): the screen, Run now, a past run as it was, a test's
// history, muting a test, working and accepting an exception, and the
// schedule. Head-office staff only; the server checks each role.
func (s *Server) selfInspectRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := selfInspectRouted.LoadOrStore(s, true); done {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/self-inspection", s.selfInspection, false)
	R("POST /v1/self-inspection/run", s.selfInspectionRun, true)
	R("PUT /v1/self-inspection/settings", s.selfInspectionSettings, true)
	R("GET /v1/self-inspection/runs/{id}", s.selfInspectionRunGet, false)
	R("GET /v1/self-inspection/tests/{id}", s.selfInspectionTest, false)
	R("POST /v1/self-inspection/tests/{id}/mute", s.selfInspectionMute, true)
	R("POST /v1/self-inspection/tests/{id}/unmute", s.selfInspectionUnmute, true)
	R("POST /v1/self-inspection/exceptions/{id}/status", s.selfInspectionStatus, true)
	R("POST /v1/self-inspection/exceptions/{id}/accept", s.selfInspectionAccept, true)
}

func (s *Server) selfInspection(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SelfInspection(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) selfInspectionRun(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RunSelfInspection(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, v, err)
}

func (s *Server) selfInspectionSettings(w http.ResponseWriter, r *http.Request) {
	var b ops.SISettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveSelfInspectionSettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) selfInspectionRunGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SelfInspectionRun(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) selfInspectionTest(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SelfInspectionTest(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

type siReason struct {
	Reason string `json:"reason"`
	Until  string `json:"until"`
}

func (s *Server) selfInspectionMute(w http.ResponseWriter, r *http.Request) {
	var b siReason
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MuteSelfInspectionTest(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Reason, b.Until)
	ok(w, v, err)
}

func (s *Server) selfInspectionUnmute(w http.ResponseWriter, r *http.Request) {
	var b siReason
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.UnmuteSelfInspectionTest(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Reason)
	ok(w, v, err)
}

func (s *Server) selfInspectionStatus(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetSelfInspectionException(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Status, b.Note)
	ok(w, v, err)
}

func (s *Server) selfInspectionAccept(w http.ResponseWriter, r *http.Request) {
	var b siReason
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AcceptSelfInspectionRisk(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Reason, b.Until)
	ok(w, v, err)
}
