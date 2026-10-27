package api

import (
	"fmt"
	"net/http"
	"strings"

	"ledgerline/internal/money"
	"ledgerline/internal/ops"
)

// periodicRoutes: weekly, monthly and quarterly work, and the BSDA review.
func (s *Server) periodicRoutes() {
	s.route("GET /v1/periodic", s.periodicList, false)
	s.route("POST /v1/periodic/{id}/run", s.periodicRun, true)
	s.route("GET /v1/periodic/files/{id}", s.periodicFile, false)
	s.route("GET /v1/dp/bsda-reviews", s.bsdaReviews, false)
	s.route("POST /v1/dp/bsda-reviews", s.bsdaReview, true)
	s.route("POST /v1/dp/bsda-reviews/{id}/apply", s.bsdaApply, true)
	s.route("POST /v1/dp/accounts/{ucc}/bsda-choice", s.bsdaChoice, true)
	s.route("POST /v1/ipo/allotments", s.ipoAllotments, true)
}

func (s *Server) ipoAllotments(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Issue   string `json:"issue"`
		ISIN    string `json:"isin"`
		Symbol  string `json:"symbol"`
		Price   string `json:"price"`
		LotSize int64  `json:"lot_size"`
		Date    string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	px, err := money.ParseDec(strings.TrimSpace(b.Price))
	if err != nil {
		fail(w, fmt.Errorf("%w: give the issue price", ops.ErrInvalid), nil)
		return
	}
	v, err := s.ops.PostIPOAllotments(r.Context(), tenant(r), actor(r), b.Issue, b.ISIN, b.Symbol, b.Date, px, b.LotSize)
	ok(w, v, err)
}

func (s *Server) periodicList(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Periodic(r.Context(), tenant(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) periodicRun(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Period string `json:"period"`
		Date   string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RunPeriodic(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Period, b.Date)
	ok(w, v, err)
}

func (s *Server) periodicFile(w http.ResponseWriter, r *http.Request) {
	f, err := s.ops.PeriodicFileData(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	ct := "text/csv; charset=utf-8"
	if !strings.HasSuffix(strings.ToLower(f.Name), ".csv") {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.NewReplacer(`"`, "", "\\", "").Replace(f.Name)+`"`)
	w.Write(f.Content)
}

func (s *Server) bsdaReviews(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BSDAReviews(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) bsdaReview(w http.ResponseWriter, r *http.Request) {
	var b struct {
		AsOf string `json:"as_of"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReviewBSDA(r.Context(), tenant(r), actor(r), b.AsOf)
	ok(w, v, err)
}

func (s *Server) bsdaApply(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ApplyBSDAReview(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) bsdaChoice(w http.ResponseWriter, r *http.Request) {
	var b struct {
		OptOut     bool `json:"opt_out"`
		OtherDemat bool `json:"other_demat"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetBSDAChoice(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.OptOut, b.OtherDemat)
	ok(w, v, err)
}
