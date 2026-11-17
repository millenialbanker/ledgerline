package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// Boards: templates, mirror cards, stickers (internal/ops/boardextras.go)
// and the AI on cards (internal/ops/boardai.go). Email-to-board and email
// to a card were taken out (unscanned mail from outside the firm).

func init() {
	boardRouteHooks = append(boardRouteHooks, func(s *Server, R func(p string, h http.HandlerFunc, m bool)) {
		R("GET /v1/board-templates", s.bxTemplates, false)
		R("POST /v1/board-templates/{id}/use", s.bxUseTemplate, true)
		R("GET /v1/boards/{id}/mirror-targets", s.bxMirrorTargets, false)
		R("POST /v1/boards/{id}/cards/{card}/mirror", s.bxMirror, true)
		R("GET /v1/boards/{id}/cards/{card}/mirror", s.bxMirrorSource, false)
		R("PATCH /v1/boards/{id}/cards/{card}/mirror", s.bxMirrorEdit, true)
		R("POST /v1/boards/{id}/cards/{card}/stickers", s.bxSticker, true)
		R("GET /v1/boards/{id}/cards/{card}/ai", s.bxCardAI, false)
		R("POST /v1/boards/{id}/cards/{card}/ai/summary", s.bxCardSummary, true)
		R("POST /v1/boards/{id}/cards/{card}/ai/checklist", s.bxCardChecklist, true)
	})
}

func (s *Server) bxTemplates(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BoardTemplates(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) bxUseTemplate(w http.ResponseWriter, r *http.Request) {
	var b ops.BoardTemplateUse
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.UseBoardTemplate(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) bxMirrorTargets(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MirrorTargets(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) bxMirror(w http.ResponseWriter, r *http.Request) {
	var b ops.CardMove
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MirrorCard(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b)
	ok(w, v, err)
}

func (s *Server) bxMirrorSource(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MirrorSource(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"))
	ok(w, v, err)
}

func (s *Server) bxMirrorEdit(w http.ResponseWriter, r *http.Request) {
	var b ops.MirrorEdit
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.EditMirrorSource(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b)
	ok(w, v, err)
}

func (s *Server) bxSticker(w http.ResponseWriter, r *http.Request) {
	var b ops.StickerOp
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CardSticker(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b)
	ok(w, v, err)
}

func (s *Server) bxCardAI(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CardAI(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"))
	ok(w, v, err)
}

func (s *Server) bxCardSummary(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SummariseCard(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"))
	ok(w, v, err)
}

func (s *Server) bxCardChecklist(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SuggestChecklist(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"))
	ok(w, v, err)
}
