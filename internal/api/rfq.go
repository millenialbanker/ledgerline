package api

import (
	"errors"
	"io"
	"net/http"
	"sync"

	"ledgerline/internal/ops"
)

// rfqRouted makes rfqRoutes register once per server.
var rfqRouted sync.Map

// rfqRoutes: debt deals on NSE's RFQ platform (ops/rfq.go, rfqdeals.go):
// the Bonds (RFQ deals) screen, reading now, confirming deals for clients
// (maker, then checker), amendments, settlement, and NSE's notification.
func (s *Server) rfqRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := rfqRouted.LoadOrStore(s, true); done {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/rfq", s.rfqView, false)
	R("GET /v1/rfq/deals.csv", s.rfqCSV, false)
	R("GET /v1/rfq/deals/{id}", s.rfqDeal, false)
	R("POST /v1/rfq/read", s.rfqRead, true)
	R("POST /v1/rfq/deals/{id}/proposal", s.rfqPropose, true)
	R("POST /v1/rfq/deals/{id}/discard", s.rfqDiscard, true)
	R("POST /v1/rfq/deals/{id}/send", s.rfqSend, true)
	R("POST /v1/rfq/deals/{id}/calc-price", s.rfqCalc, true)
	R("POST /v1/rfq/deals/{id}/amend", s.rfqAmend, true)
	R("POST /v1/rfq/deals/{id}/amend/send", s.rfqAmendSend, true)
	R("POST /v1/rfq/deals/{id}/settled", s.rfqSettled, true)
	R("POST /v1/rfq/amendments/{id}/decide", s.rfqDecide, true)
	// NSE calls <callback base>/v1/notification; the base given to NSE is
	// /hooks/<tenant>/nse-rfq/<callback token> (not signed in: the token
	// and the signature in the "token" header admit it).
	s.mux.HandleFunc("POST /hooks/{tenant}/nse-rfq/{token}/v1/notification", s.rfqHook)
}

func (s *Server) rfqView(w http.ResponseWriter, r *http.Request) {
	date, okd := dateParam(w, r.URL.Query().Get("date"), "date", false)
	if !okd {
		return
	}
	v, err := s.ops.RFQDeals(r.Context(), tenant(r), actor(r), date)
	ok(w, v, err)
}

func (s *Server) rfqDeal(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RFQDeal(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) rfqCSV(w http.ResponseWriter, r *http.Request) {
	date, okd := qdate(w, r)
	if !okd {
		return
	}
	b, err := s.ops.RFQDealsCSV(r.Context(), tenant(r), actor(r), date)
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, "rfq_deals_"+date+".csv", "text/csv; charset=utf-8", b)
}

func (s *Server) rfqRead(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
	}
	if !bseDecodeOptional(w, r, &b) {
		return
	}
	v, err := s.ops.RFQSync(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, v, err)
}

func (s *Server) rfqPropose(w http.ResponseWriter, r *http.Request) {
	var b ops.RFQProposalInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RFQPropose(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) rfqDiscard(w http.ResponseWriter, r *http.Request) {
	var b struct {
		What string `json:"what"` // "amend" for the waiting amendment
		Note string `json:"note"`
	}
	if !bseDecodeOptional(w, r, &b) {
		return
	}
	v, err := s.ops.RFQDiscard(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.What, b.Note)
	ok(w, v, err)
}

func (s *Server) rfqSend(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RFQSend(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) rfqCalc(w http.ResponseWriter, r *http.Request) {
	var b struct {
		AIDcc int `json:"ai_dcc"`
	}
	if !bseDecodeOptional(w, r, &b) {
		return
	}
	v, err := s.ops.RFQCalcPrice(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.AIDcc)
	ok(w, v, err)
}

func (s *Server) rfqAmend(w http.ResponseWriter, r *http.Request) {
	var b ops.RFQAmendInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RFQProposeAmend(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) rfqAmendSend(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RFQSendAmend(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) rfqSettled(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RFQMarkSettled(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Note)
	ok(w, v, err)
}

func (s *Server) rfqDecide(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Remarks string `json:"remarks"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RFQDecideAmend(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Remarks)
	ok(w, v, err)
}

// rfqHook receives NSE's notification and answers as the protocol's
// Response Success JSON ({"code": 200}), or a common error response.
func (s *Server) rfqHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSON(w, 400, map[string]any{"code": 400, "messages": []string{"cannot read the body"}})
		return
	}
	if _, err := s.ops.RFQCallback(r.Context(), r.PathValue("tenant"), r.PathValue("token"), r.Header.Get("token"), body); err != nil {
		code := 400
		if errors.Is(err, ops.ErrForbidden) {
			code = 403
		}
		writeJSON(w, code, map[string]any{"code": code, "messages": []string{err.Error()}})
		return
	}
	writeJSON(w, 200, map[string]int{"code": 200})
}
