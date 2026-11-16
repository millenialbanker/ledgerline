package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// Board notifications by email (internal/ops/boardnotify.go): each person's
// own settings, under /v1/board-notify so they never meet /v1/boards/{id}.

func init() {
	boardRouteHooks = append(boardRouteHooks, func(s *Server, R func(p string, h http.HandlerFunc, m bool)) {
		R("GET /v1/board-notify", s.boardMail, false)
		R("PUT /v1/board-notify", s.saveBoardMail, true)
		R("POST /v1/board-notify/address", s.boardMailAddress, true)
		R("POST /v1/board-notify/confirm", s.boardMailConfirm, true)
		R("POST /v1/board-notify/pause", s.boardMailPause, true)
		R("POST /v1/board-notify/firm", s.boardMailFirm, true)
	})
}

// noteConsole tells ops the address people reach the console at, for the
// links in board emails: the configured public address, or the one an
// Admin uses (ops decides; others' requests cannot change it).
func (s *Server) noteConsole(r *http.Request) {
	s.ops.NoteConsoleAddress(r.Context(), tenant(r), actor(r), s.publicURL(r), s.PublicURL != "")
}

func (s *Server) boardMail(w http.ResponseWriter, r *http.Request) {
	s.noteConsole(r)
	v, err := s.ops.BoardMailSettings(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) saveBoardMail(w http.ResponseWriter, r *http.Request) {
	var b ops.BoardMailInput
	if !decode(w, r, &b) {
		return
	}
	s.noteConsole(r)
	v, err := s.ops.SaveBoardMail(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) boardMailAddress(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetBoardMailAddress(r.Context(), tenant(r), actor(r), b.Email)
	ok(w, v, err)
}

func (s *Server) boardMailConfirm(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ConfirmBoardMail(r.Context(), tenant(r), actor(r), b.Code)
	ok(w, v, err)
}

func (s *Server) boardMailPause(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Days int `json:"days"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PauseBoardMail(r.Context(), tenant(r), actor(r), b.Days)
	ok(w, v, err)
}

func (s *Server) boardMailFirm(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Off bool `json:"off"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetBoardMailFirm(r.Context(), tenant(r), actor(r), b.Off)
	ok(w, v, err)
}
