package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/ops"
)

// Boards: the Inbox, the Planner and calendar feeds
// (internal/ops/boardplanner.go, web/boardplanner.js).

var (
	// bpFeedLimit caps how often one feed is read (calendars poll every
	// few minutes to hours).
	bpFeedLimit = &limiter{max: 120, per: time.Hour}
	// bpFeedMiss slows down a caller trying feed addresses that do not exist.
	bpFeedMiss = &limiter{max: 30, per: 15 * time.Minute}
)

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
		R("GET /v1/board-feeds", s.bpFeeds, false)
		R("POST /v1/board-feeds", s.bpFeedAdd, true)
		R("POST /v1/board-feeds/{id}/revoke", s.bpFeedRevoke, true)
		R("PUT /v1/board-feeds/firm", s.bpFeedFirm, true)
		R("PUT /v1/boards/{id}/views/feed", s.bpFeedBoard, true)
		// Google Calendar or Outlook reads a feed here. There is no staff
		// session: the long random token in the address is the secret.
		s.mux.HandleFunc("GET /feeds/boards/{tenant}/{file}", s.bpFeedICS)
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

// bpFeedPath is where a feed is read; the full address when the server
// knows its public URL.
func (s *Server) bpFeedURL(r *http.Request, token string) (string, string) {
	p := "/feeds/boards/" + tenant(r) + "/" + token + ".ics"
	return p, s.publicURL(r) + p
}

func (s *Server) bpFeeds(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BoardFeeds(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) bpFeedAdd(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Scope string `json:"scope"`
		Board string `json:"board"`
	}
	if !decode(w, r, &b) {
		return
	}
	f, token, err := s.ops.CreateBoardFeed(r.Context(), tenant(r), actor(r), b.Scope, b.Board)
	if err != nil {
		fail(w, err, nil)
		return
	}
	p, u := s.bpFeedURL(r, token)
	// The address is shown this once: only its hash is kept.
	writeJSON(w, http.StatusOK, map[string]any{"feed": f, "path": p, "url": u, "webcal": "webcal://" + strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")})
}

func (s *Server) bpFeedRevoke(w http.ResponseWriter, r *http.Request) {
	err := s.ops.RevokeBoardFeed(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]bool{"revoked": err == nil}, err)
}

func (s *Server) bpFeedFirm(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On bool `json:"on"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := s.ops.SetFirmFeeds(r.Context(), tenant(r), actor(r), b.On); err != nil {
		fail(w, err, nil)
		return
	}
	v, err := s.ops.BoardFeeds(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) bpFeedBoard(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On bool `json:"on"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetBoardFeed(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.On)
	ok(w, map[string]any{"board": v.ID, "on": b.On}, err)
}

// bpBlocked reports, without counting a hit, whether key is over l's limit.
func bpBlocked(l *limiter, key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, t := range l.hits[key] {
		if time.Since(t) < l.per {
			n++
		}
	}
	return n >= l.max
}

func (s *Server) bpFeedICS(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("X-Content-Type-Options", "nosniff")
	if s.ops == nil {
		http.NotFound(w, r)
		return
	}
	t, file, ip := r.PathValue("tenant"), r.PathValue("file"), clientIP(r)
	token := strings.TrimSuffix(file, ".ics")
	if bpBlocked(bpFeedMiss, ip) {
		h.Set("Retry-After", "900")
		http.Error(w, "Too many requests for calendar feeds that do not exist; try again later.", http.StatusTooManyRequests)
		return
	}
	if !bpFeedLimit.allow(t + "|" + token) {
		h.Set("Retry-After", "600")
		http.Error(w, "This calendar feed was read too often in the last hour; try again later.", http.StatusTooManyRequests)
		return
	}
	body, err := s.ops.BoardFeedICS(r.Context(), t, token, s.publicURL(r))
	if err != nil {
		if errors.Is(err, ops.ErrNotFound) {
			bpFeedMiss.allow(ip)
			http.Error(w, "No calendar feed at this address. It may have been stopped or replaced.", http.StatusNotFound)
			return
		}
		http.Error(w, "The calendar feed could not be made just now.", http.StatusInternalServerError)
		return
	}
	h.Set("Content-Type", "text/calendar; charset=utf-8")
	h.Set("Content-Disposition", `inline; filename="ledgerline-cards.ics"`)
	h.Set("Cache-Control", "private, max-age=300")
	w.Write(body)
}
