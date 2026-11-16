package api

import (
	"errors"
	"net/http"

	"ledgerline/internal/ops"
)

// Boards: copying and moving lists, watching lists and boards, collapsing
// lists, turning checklist items into cards, members and joining
// (internal/ops/boardlists.go).

func init() {
	boardRouteHooks = append(boardRouteHooks, func(s *Server, R func(p string, h http.HandlerFunc, m bool)) {
		R("POST /v1/boards/{id}/watch", s.watchBoard, true)
		R("POST /v1/boards/{id}/join", s.joinBoard, true)
		R("PUT /v1/boards/{id}/members", s.setBoardMembers, true)
		R("POST /v1/boards/{id}/lists/{list}/watch", s.watchList, true)
		R("POST /v1/boards/{id}/lists/{list}/collapse", s.collapseList, true)
		R("POST /v1/boards/{id}/lists/{list}/copy", s.copyList, true)
		R("POST /v1/boards/{id}/lists/{list}/move", s.moveList, true)
		R("POST /v1/boards/{id}/cards/{card}/checklists/convert", s.convertCheckItem, true)
	})
}

type boardOn struct {
	On bool `json:"on"`
}

func (s *Server) watchBoard(w http.ResponseWriter, r *http.Request) {
	var b boardOn
	if !decode(w, r, &b) {
		return
	}
	on, err := s.ops.WatchBoard(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.On)
	ok(w, map[string]bool{"watching": on}, err)
}

func (s *Server) joinBoard(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.JoinBoard(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) setBoardMembers(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Members []ops.BoardPerson `json:"members"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Members == nil {
		b.Members = []ops.BoardPerson{}
	}
	v, err := s.ops.SetBoardMembers(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Members)
	ok(w, v, err)
}

func (s *Server) watchList(w http.ResponseWriter, r *http.Request) {
	var b boardOn
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.WatchList(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("list"), b.On)
	ok(w, v, err)
}

func (s *Server) collapseList(w http.ResponseWriter, r *http.Request) {
	var b boardOn
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CollapseList(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("list"), b.On)
	ok(w, map[string][]string{"collapsed": v}, err)
}

func (s *Server) copyList(w http.ResponseWriter, r *http.Request) {
	var b ops.ListCopyInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CopyList(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("list"), b)
	ok(w, v, err)
}

func (s *Server) moveList(w http.ResponseWriter, r *http.Request) {
	var b ops.ListMoveInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MoveList(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("list"), b)
	ok(w, v, err)
}

func (s *Server) convertCheckItem(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Checklist string `json:"checklist"`
		Item      string `json:"item"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, made, err := s.ops.ConvertCheckItem(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b.Checklist, b.Item)
	ok(w, map[string]ops.Card{"card": c, "new": made}, err)
}

// cardMoved is the details of a not-found error for a card that went to
// another board, so the console can follow it.
func cardMoved(err error) any {
	var m *ops.CardMovedError
	if errors.As(err, &m) {
		return map[string]map[string]string{"moved_to": {"board": m.Board, "card": m.Card}}
	}
	return nil
}
