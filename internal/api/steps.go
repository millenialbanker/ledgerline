package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// stepRoutes: the day's steps, their reminders and the pending list
// (ops/steps.go, ops/stepremind.go). A step's ID holds ":" and "|", so it
// goes in the body.
func (s *Server) stepRoutes() {
	if s.ops == nil {
		return
	}
	s.route("GET /v1/steps", s.steps, false)
	s.route("POST /v1/steps/check", s.stepsCheck, true)
	s.route("GET /v1/steps/settings", s.stepSettings, false)
	s.route("PUT /v1/steps/settings", s.saveStepSettings, true)
	s.route("POST /v1/steps/done", s.stepDone, true)
	s.route("POST /v1/steps/skip", s.stepSkip, true)
	s.route("POST /v1/steps/confirm", s.stepConfirm, true)
}

func (s *Server) steps(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Steps(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) stepsCheck(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CheckStepsNow(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) stepSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.StepSettingsFor(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) saveStepSettings(w http.ResponseWriter, r *http.Request) {
	var b ops.StepSettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveStepSettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

type stepBody struct {
	ID          string `json:"id"`
	Note        string `json:"note"`
	Reason      string `json:"reason"`
	Fingerprint string `json:"fingerprint"`
}

func (s *Server) stepDone(w http.ResponseWriter, r *http.Request) {
	var b stepBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkStepDone(r.Context(), tenant(r), actor(r), b.ID, b.Note)
	ok(w, v, err)
}

func (s *Server) stepSkip(w http.ResponseWriter, r *http.Request) {
	var b stepBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SkipStep(r.Context(), tenant(r), actor(r), b.ID, b.Reason)
	ok(w, v, err)
}

func (s *Server) stepConfirm(w http.ResponseWriter, r *http.Request) {
	var b stepBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ConfirmStep(r.Context(), tenant(r), actor(r), b.ID, b.Fingerprint, b.Note)
	ok(w, v, err)
}
