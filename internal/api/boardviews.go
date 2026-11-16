package api

import (
	"net/http"
	"strconv"
	"strings"

	"ledgerline/internal/ops"
)

// Boards: views (internal/ops/boardviews.go, web/boardviews.js): my cards
// and the cards of all boards for the boards home, and a board's
// dashboard tiles and cards over time.

func init() {
	boardRouteHooks = append(boardRouteHooks, func(s *Server, R func(p string, h http.HandlerFunc, m bool)) {
		R("GET /v1/boards/my-cards", s.myCards, false)
		R("GET /v1/board-cards", s.crossCards, false)
		R("PUT /v1/boards/{id}/views/tiles", s.setDashTiles, true)
		R("GET /v1/boards/{id}/views/over-time", s.boardOverTime, false)
	})
}

func (s *Server) myCards(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MyCards(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

// queryList is a repeated query parameter (?board=a&board=b), or one
// given comma-separated.
func queryList(r *http.Request, name string) []string {
	var out []string
	for _, v := range r.URL.Query()[name] {
		for _, x := range strings.Split(v, ",") {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, x)
			}
		}
	}
	return out
}

func (s *Server) crossCards(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := ops.XCardsQuery{Boards: queryList(r, "board"), Lists: queryList(r, "list"), Members: queryList(r, "member"), Labels: queryList(r, "label"), Due: queryList(r, "due"),
		From: q.Get("from"), To: q.Get("to")}
	in.Offset, _ = strconv.Atoi(q.Get("offset"))
	in.Limit, _ = strconv.Atoi(q.Get("limit"))
	v, err := s.ops.CrossCards(r.Context(), tenant(r), actor(r), in)
	ok(w, v, err)
}

func (s *Server) setDashTiles(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Tiles []ops.DashTile `json:"tiles"`
		Reset bool           `json:"reset"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetDashTiles(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Tiles, b.Reset)
	ok(w, map[string]any{"tiles": v}, err)
}

func (s *Server) boardOverTime(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BoardOverTime(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}
