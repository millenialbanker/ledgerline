package api

import (
	"net/http"
	"strconv"

	"ledgerline/internal/ops"
)

// Board automation: rules, card and board buttons, scheduled and due-date
// commands (internal/ops/boardrules.go). Everyone on a board reads its
// automation and log; its admins and members change and run it.

func init() {
	boardRouteHooks = append(boardRouteHooks, func(s *Server, R func(p string, h http.HandlerFunc, m bool)) {
		R("GET /v1/boards/{id}/automation", s.boardAutomation, false)
		R("POST /v1/boards/{id}/automation", s.saveBoardAuto, true)
		R("GET /v1/boards/{id}/automation/log", s.boardAutoLog, false)
		R("PUT /v1/boards/{id}/automation/{auto}", s.saveBoardAuto, true)
		R("POST /v1/boards/{id}/automation/{auto}/on", s.boardAutoOn, true)
		R("POST /v1/boards/{id}/automation/{auto}/delete", s.deleteBoardAuto, true)
		R("POST /v1/boards/{id}/automation/{auto}/run", s.runBoardAuto, true)
		// The card repeater (internal/ops/boardrepeat.go).
		R("GET /v1/boards/{id}/repeats", s.boardRepeats, false)
		R("PUT /v1/boards/{id}/cards/{card}/repeat", s.saveCardRepeat, true)
		R("POST /v1/boards/{id}/cards/{card}/repeat/preview", s.previewCardRepeat, true)
		R("POST /v1/boards/{id}/cards/{card}/repeat/stop", s.stopCardRepeat, true)
	})
}

func (s *Server) boardAutomation(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BoardAutomation(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) saveBoardAuto(w http.ResponseWriter, r *http.Request) {
	var b ops.BoardAuto
	if !decode(w, r, &b) {
		return
	}
	b.ID = r.PathValue("auto") // "" on POST: a new one
	v, err := s.ops.SaveBoardAuto(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	if err == nil {
		s.noteConsole(r) // for {cardlink}
	}
	ok(w, v, err)
}

func (s *Server) boardRepeats(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BoardRepeats(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) saveCardRepeat(w http.ResponseWriter, r *http.Request) {
	var b ops.CardRepeat
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveCardRepeat(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b)
	ok(w, v, err)
}

func (s *Server) previewCardRepeat(w http.ResponseWriter, r *http.Request) {
	var b ops.CardRepeat
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PreviewCardRepeat(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b)
	ok(w, v, err)
}

func (s *Server) stopCardRepeat(w http.ResponseWriter, r *http.Request) {
	err := s.ops.StopCardRepeat(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"))
	ok(w, map[string]bool{"stopped": err == nil}, err)
}

func (s *Server) boardAutoOn(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On bool `json:"on"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetBoardAutoOn(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("auto"), b.On)
	ok(w, v, err)
}

func (s *Server) deleteBoardAuto(w http.ResponseWriter, r *http.Request) {
	err := s.ops.DeleteBoardAuto(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("auto"))
	ok(w, map[string]bool{"deleted": err == nil}, err)
}

func (s *Server) runBoardAuto(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Card    string `json:"card"`
		Confirm bool   `json:"confirm"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RunBoardAuto(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("auto"), b.Card, b.Confirm)
	ok(w, v, err)
}

func (s *Server) boardAutoLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	v, err := s.ops.BoardAutoLog(r.Context(), tenant(r), actor(r), r.PathValue("id"), q.Get("auto"), before, limit)
	ok(w, v, err)
}
