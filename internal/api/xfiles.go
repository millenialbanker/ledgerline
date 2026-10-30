package api

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"ledgerline/internal/ops"
)

// xfileRoutes: exchange, clearing corporation, depository, front-end and
// bank files in their own layouts.
func (s *Server) xfileRoutes() {
	s.route("GET /v1/exchange-files/formats", s.xfileFormats, false)
	s.route("POST /v1/exchange-files", s.xfileUpload, true)
	s.route("GET /v1/exchange-files/{id}", s.xfileDownload, false)
	s.route("GET /v1/exchange-files-checklist", s.xfileChecklist, false)
}

func (s *Server) xfileChecklist(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.DailyFileChecklist(r.Context(), tenant(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) xfileFormats(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ExchangeFormats()
	ok(w, v, err)
}

func (s *Server) xfileUpload(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, ops.MaxXFileBytes+1))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	q := r.URL.Query()
	v, err := s.ops.LoadExchangeFile(r.Context(), tenant(r), actor(r), q.Get("name"), data, q.Get("load") == "1")
	ok(w, v, err)
}

func (s *Server) xfileDownload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	seq, _ := strconv.Atoi(q.Get("seq"))
	name, b, err := s.ops.ExchangeFile(r.Context(), tenant(r), actor(r), r.PathValue("id"), q.Get("date"), ops.XFileOptions{Exchange: q.Get("exchange"), Segment: q.Get("segment"), Seq: seq})
	if err != nil {
		fail(w, err, nil)
		return
	}
	ct := "text/plain; charset=utf-8"
	switch {
	case strings.HasSuffix(name, ".json"):
		ct = "application/json"
	case strings.HasSuffix(name, ".gz"):
		ct = "application/gzip"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.NewReplacer(`"`, "", "\\", "").Replace(name)+`"`)
	w.Write(b)
}
