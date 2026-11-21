package api

import (
	"net/http"
	"strconv"

	"ledgerline/internal/ops"
)

// activityRoutes: the activity feed beside every screen (ops/activity.go).
// GET /v1/activity reads the week; with after=<cursor> it returns only
// what is newer, for the panel's poll every few seconds. The feed reads
// the audit trail and keeps nothing but each user's panel state.
func (s *Server) activityRoutes() {
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/activity", s.activityFeed, false)
	R("PUT /v1/activity/state", s.activityState, true)
}

func (s *Server) activityFeed(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	num := func(k string) (int64, bool) {
		x := v.Get(k)
		if x == "" {
			return 0, true
		}
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, 400, "bad_"+k, k+" must be a whole number of 0 or more", nil)
			return 0, false
		}
		return n, true
	}
	after, ok1 := num("after")
	before, ok2 := num("before")
	limit, ok3 := num("limit")
	if !ok1 || !ok2 || !ok3 {
		return
	}
	if after > 0 && before > 0 {
		writeErr(w, 400, "bad_cursor", "Send after (newer entries) or before (older entries), not both.", nil)
		return
	}
	f, err := s.ops.Activity(r.Context(), tenant(r), actor(r), ops.ActivityQuery{After: after, Before: before, Limit: int(limit),
		Person: v.Get("person"), Role: v.Get("role"), Area: v.Get("area"), Only: v.Get("only"), Text: v.Get("q")})
	ok(w, f, err)
}

func (s *Server) activityState(w http.ResponseWriter, r *http.Request) {
	var b ops.ActivityStatePatch
	if !decode(w, r, &b) {
		return
	}
	st, err := s.ops.SaveActivityState(r.Context(), tenant(r), actor(r), b)
	ok(w, st, err)
}
