package api

import (
	"io"
	"math"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"

	"ledgerline/internal/ops"
)

// Boards: Trello-style boards, lists and cards (internal/ops/boards.go).
// Branch and partner staff use them too (on their own private boards);
// the server decides what each person sees.

// boardRouteHooks lets the other board files (views, rules, extras,
// Ledgerline boards) add their routes without editing this one.
var boardRouteHooks []func(s *Server, R func(p string, h http.HandlerFunc, m bool))

func (s *Server) boardRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { scopedRoutes[p] = true; s.route(p, h, m) }
	R("GET /v1/boards", s.boards, false)
	R("POST /v1/boards", s.createBoard, true)
	R("GET /v1/boards/search", s.searchCards, false)
	R("GET /v1/boards/notices", s.boardNotices, false)
	R("POST /v1/boards/notices/read", s.readBoardNotices, true)
	R("GET /v1/board-files/{id}", s.boardFile, false)
	R("GET /v1/boards/{id}", s.getBoard, false)
	R("GET /v1/boards/{id}/rev", s.boardRev, false)
	R("PUT /v1/boards/{id}", s.updateBoard, true)
	R("POST /v1/boards/{id}/close", s.closeBoard, true)
	R("POST /v1/boards/{id}/reopen", s.reopenBoard, true)
	R("POST /v1/boards/{id}/delete", s.deleteBoard, true)
	R("POST /v1/boards/{id}/star", s.starBoard, true)
	R("POST /v1/boards/{id}/copy", s.copyBoard, true)
	R("POST /v1/boards/{id}/background", s.boardBackground, true)
	R("GET /v1/boards/{id}/activity", s.boardActivity, false)
	R("POST /v1/boards/{id}/lists", s.addList, true)
	R("PUT /v1/boards/{id}/lists/{list}", s.updateList, true)
	R("POST /v1/boards/{id}/lists/{list}/move-cards", s.moveListCards, true)
	R("POST /v1/boards/{id}/lists/{list}/archive-cards", s.archiveListCards, true)
	R("POST /v1/boards/{id}/lists/{list}/sort", s.sortList, true)
	R("POST /v1/boards/{id}/cards", s.addCard, true)
	R("GET /v1/boards/{id}/cards/{card}", s.getCard, false)
	R("PATCH /v1/boards/{id}/cards/{card}", s.updateCard, true)
	R("POST /v1/boards/{id}/cards/{card}/move", s.moveCard, true)
	R("POST /v1/boards/{id}/cards/{card}/copy", s.copyCard, true)
	R("POST /v1/boards/{id}/cards/{card}/delete", s.deleteCard, true)
	R("POST /v1/boards/{id}/cards/{card}/toggle", s.toggleCardPerson, true)
	R("POST /v1/boards/{id}/cards/{card}/checklists", s.editChecklist, true)
	R("POST /v1/boards/{id}/cards/{card}/comments", s.addComment, true)
	R("PUT /v1/boards/{id}/cards/{card}/comments/{cm}", s.editComment, true)
	R("POST /v1/boards/{id}/cards/{card}/comments/{cm}/delete", s.deleteComment, true)
	R("POST /v1/boards/{id}/cards/{card}/comments/{cm}/react", s.reactComment, true)
	R("POST /v1/boards/{id}/cards/{card}/files", s.attachFile, true)
	R("POST /v1/boards/{id}/cards/{card}/links", s.attachLink, true)
	R("POST /v1/boards/{id}/cards/{card}/attachments/{att}/delete", s.removeAttachment, true)
	for _, h := range boardRouteHooks {
		h(s, R)
	}
}

func (s *Server) boards(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Boards(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) createBoard(w http.ResponseWriter, r *http.Request) {
	var b ops.BoardInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CreateBoard(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) getBoard(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.GetBoard(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.URL.Query().Get("archived") == "1")
	ok(w, v, err)
}

func (s *Server) boardRev(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BoardRev(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]int64{"rev": v}, err)
}

func (s *Server) updateBoard(w http.ResponseWriter, r *http.Request) {
	var b ops.BoardInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.UpdateBoard(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) closeBoard(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SetBoardClosed(r.Context(), tenant(r), actor(r), r.PathValue("id"), true)
	ok(w, v, err)
}

func (s *Server) reopenBoard(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SetBoardClosed(r.Context(), tenant(r), actor(r), r.PathValue("id"), false)
	ok(w, v, err)
}

func (s *Server) deleteBoard(w http.ResponseWriter, r *http.Request) {
	err := s.ops.DeleteBoard(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]bool{"deleted": err == nil}, err)
}

func (s *Server) starBoard(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On bool `json:"on"`
	}
	if !decode(w, r, &b) {
		return
	}
	err := s.ops.StarBoard(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.On)
	ok(w, map[string]bool{"starred": b.On}, err)
}

func (s *Server) copyBoard(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name  string `json:"name"`
		Cards bool   `json:"cards"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CopyBoard(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Name, b.Cards)
	ok(w, v, err)
}

func (s *Server) boardBackground(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "Files can be up to 15 MB.", nil)
		return
	}
	id, err := s.ops.UploadBoardFile(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.URL.Query().Get("name"), data)
	if err != nil {
		fail(w, err, nil)
		return
	}
	v, err := s.ops.UpdateBoard(r.Context(), tenant(r), actor(r), r.PathValue("id"), ops.BoardInput{Background: "file:" + id})
	ok(w, v, err)
}

func (s *Server) boardActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	v, err := s.ops.BoardActivityList(r.Context(), tenant(r), actor(r), r.PathValue("id"), q.Get("card"), before, limit)
	ok(w, v, err)
}

func (s *Server) searchCards(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SearchCards(r.Context(), tenant(r), actor(r), r.URL.Query().Get("q"))
	ok(w, v, err)
}

func (s *Server) boardNotices(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ns, unread, err := s.ops.BoardNotices(r.Context(), tenant(r), actor(r), limit)
	ok(w, map[string]any{"notices": ns, "unread": unread}, err)
}

func (s *Server) readBoardNotices(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.ReadBoardNotices(r.Context(), tenant(r), actor(r), b.ID)
	ok(w, map[string]int{"read": n}, err)
}

func (s *Server) boardFile(w http.ResponseWriter, r *http.Request) {
	name, ct, img, data, err := s.ops.BoardFile(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	ascii := strings.Map(func(c rune) rune {
		if c < 32 || c > 126 || c == '"' || c == '\\' {
			return '_'
		}
		return c
	}, name)
	disp := "attachment"
	if img {
		disp = "inline"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", disp+`; filename="`+ascii+`"; filename*=UTF-8''`+neturl.PathEscape(name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(data)
}

func floatOr(p *float64) float64 {
	if p == nil {
		return math.NaN()
	}
	return *p
}

func (s *Server) addList(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string   `json:"name"`
		Pos  *float64 `json:"pos"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AddList(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Name, floatOr(b.Pos))
	ok(w, v, err)
}

func (s *Server) updateList(w http.ResponseWriter, r *http.Request) {
	var b ops.ListInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.UpdateList(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("list"), b)
	ok(w, v, err)
}

func (s *Server) moveListCards(w http.ResponseWriter, r *http.Request) {
	var b struct {
		To string `json:"to"`
	}
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.MoveListCards(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("list"), b.To)
	ok(w, map[string]int{"moved": n}, err)
}

func (s *Server) archiveListCards(w http.ResponseWriter, r *http.Request) {
	n, err := s.ops.ArchiveListCards(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("list"))
	ok(w, map[string]int{"archived": n}, err)
}

func (s *Server) sortList(w http.ResponseWriter, r *http.Request) {
	var b struct {
		By string `json:"by"`
	}
	if !decode(w, r, &b) {
		return
	}
	err := s.ops.SortList(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("list"), b.By)
	ok(w, map[string]string{"sorted_by": b.By}, err)
}

func (s *Server) addCard(w http.ResponseWriter, r *http.Request) {
	var b ops.CardInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AddCard(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) getCard(w http.ResponseWriter, r *http.Request) {
	c, _, err := s.ops.GetCard(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"))
	if err != nil {
		fail(w, err, cardMoved(err)) // a moved card's new place, so old links follow it
		return
	}
	act, err := s.ops.BoardActivityList(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), 0, 200)
	ok(w, map[string]any{"card": c, "activity": act}, err)
}

func (s *Server) updateCard(w http.ResponseWriter, r *http.Request) {
	var b ops.CardPatch
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.UpdateCard(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b)
	ok(w, v, err)
}

func (s *Server) moveCard(w http.ResponseWriter, r *http.Request) {
	var b ops.CardMove
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MoveCard(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b)
	ok(w, v, err)
}

func (s *Server) copyCard(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ops.CardMove
		Title string   `json:"title"`
		Keep  []string `json:"keep"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CopyCard(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b.CardMove, b.Title, b.Keep)
	ok(w, v, err)
}

func (s *Server) deleteCard(w http.ResponseWriter, r *http.Request) {
	err := s.ops.DeleteCard(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"))
	ok(w, map[string]bool{"deleted": err == nil}, err)
}

func (s *Server) toggleCardPerson(w http.ResponseWriter, r *http.Request) {
	var b struct {
		What string `json:"what"` // watch, vote, member
		On   bool   `json:"on"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ToggleCardPerson(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b.What, b.On)
	ok(w, v, err)
}

func (s *Server) editChecklist(w http.ResponseWriter, r *http.Request) {
	var b ops.ChecklistOp
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.EditChecklist(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b)
	ok(w, v, err)
}

func (s *Server) addComment(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AddComment(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b.Text)
	ok(w, v, err)
}

func (s *Server) editComment(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.EditComment(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), r.PathValue("cm"), b.Text, false)
	ok(w, v, err)
}

func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.EditComment(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), r.PathValue("cm"), "", true)
	ok(w, v, err)
}

func (s *Server) reactComment(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Emoji string `json:"emoji"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReactComment(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), r.PathValue("cm"), b.Emoji)
	ok(w, v, err)
}

func (s *Server) attachFile(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "Files can be up to 15 MB.", nil)
		return
	}
	v, err := s.ops.AttachFile(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), r.URL.Query().Get("name"), data)
	ok(w, v, err)
}

func (s *Server) attachLink(w http.ResponseWriter, r *http.Request) {
	var b struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AttachLink(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b.URL, b.Name)
	ok(w, v, err)
}

func (s *Server) removeAttachment(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RemoveAttachment(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), r.PathValue("att"))
	ok(w, v, err)
}
