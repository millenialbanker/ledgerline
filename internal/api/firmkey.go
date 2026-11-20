package api

import (
	"net/http"
)

// firmKeyRoutes: the Encryption keys panel (Settings): whose key the
// firm's data is under, and, for the Admin, testing the firm's own key,
// moving the firm onto it, rotating it and the cache window (ops/firmkey.go).
func (s *Server) firmKeyRoutes() {
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/security/keys", s.firmKey, false)
	R("POST /v1/security/keys/test", s.firmKeyTest, true)
	R("POST /v1/security/keys/use", s.firmKeyUse, true)
	R("POST /v1/security/keys/rotate", s.firmKeyRotate, true)
	R("PUT /v1/security/keys/cache", s.firmKeyCache, true)
}

func (s *Server) firmKey(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FirmKey(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) firmKeyTest(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Conn string `json:"conn"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.TestFirmKey(r.Context(), tenant(r), actor(r), b.Conn)
	ok(w, v, err)
}

func (s *Server) firmKeyUse(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Conn         string `json:"conn"`
		CacheMinutes int    `json:"cache_minutes"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.UseFirmKey(r.Context(), tenant(r), actor(r), b.Conn, b.CacheMinutes)
	ok(w, v, err)
}

func (s *Server) firmKeyRotate(w http.ResponseWriter, r *http.Request) {
	var b struct{}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RotateFirmKey(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) firmKeyCache(w http.ResponseWriter, r *http.Request) {
	var b struct {
		CacheMinutes int `json:"cache_minutes"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetFirmKeyCache(r.Context(), tenant(r), actor(r), b.CacheMinutes)
	ok(w, v, err)
}
