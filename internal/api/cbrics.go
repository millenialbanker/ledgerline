package api

import (
	"errors"
	"io"
	"net/http"
	"sync"

	"ledgerline/internal/ops"
)

// cbricsRouted makes cbricsRoutes register once per server, however it is
// wired.
var cbricsRouted sync.Map

// cbricsRoutes: corporate bond deals through NSE CBRICS (the Corporate
// bonds (CBRICS) screen): deals entered, reported, answered and changed by
// maker and checker; participants (registered ones found at CBRICS and
// unregistered ones the firm keeps); settlement accounts; reading CBRICS;
// and NSE's notification callback.
func (s *Server) cbricsRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := cbricsRouted.LoadOrStore(s, true); done {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/cbrics", s.cbricsView, false)
	R("POST /v1/cbrics/sync", s.cbricsSync, true)
	R("GET /v1/cbrics/propose", s.cbricsPropose, false)
	R("POST /v1/cbrics/deals", s.cbricsSaveDeal, true)
	R("POST /v1/cbrics/deals/{id}/decide", s.cbricsDecideDeal, true)
	R("POST /v1/cbrics/deals/{id}/cancel", s.cbricsCancel, true)
	R("POST /v1/cbrics/deals/{id}/respond", s.cbricsRespond, true)
	R("POST /v1/cbrics/deals/{id}/accounts", s.cbricsAccount, true)
	R("POST /v1/cbrics/participants/find", s.cbricsFind, true)
	R("GET /v1/cbrics/participants/from-client/{ucc}", s.cbricsFromClient, false)
	R("POST /v1/cbrics/participants", s.cbricsSaveParty, true)
	R("POST /v1/cbrics/participants/import", s.cbricsImport, true)
	R("POST /v1/cbrics/participants/{code}/request", s.cbricsPartyRequest, true)
	R("POST /v1/cbrics/participants/{code}/decide", s.cbricsDecideParty, true)
	// NSE calls <base>/v1/notification, the base being
	// /hooks/<tenant>/nse-cbrics; the token header (an HMAC of the body
	// under the firm's callback key) admits it.
	s.mux.HandleFunc("POST /hooks/{tenant}/nse-cbrics/v1/notification", s.cbricsHook)
}

func (s *Server) cbricsView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CBRICSStatus(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) cbricsSync(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CBRICSSync(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) cbricsPropose(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.CBRICSPropose(r.Context(), tenant(r), actor(r), q.Get("ucc"), q.Get("isin"))
	ok(w, v, err)
}

func (s *Server) cbricsSaveDeal(w http.ResponseWriter, r *http.Request) {
	var b ops.CBRICSDealInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveCBRICSDeal(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

type cbricsDecision struct {
	Approve bool   `json:"approve"`
	Reason  string `json:"reason"`
}

func (s *Server) cbricsDecideDeal(w http.ResponseWriter, r *http.Request) {
	var b cbricsDecision
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideCBRICSDeal(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Reason)
	ok(w, v, err)
}

func (s *Server) cbricsCancel(w http.ResponseWriter, r *http.Request) {
	var b cbricsDecision
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CancelCBRICSDeal(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Reason)
	ok(w, v, err)
}

func (s *Server) cbricsRespond(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Accept      bool   `json:"accept"`
		YieldReason string `json:"yield_reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CBRICSRespond(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Accept, b.YieldReason)
	ok(w, v, err)
}

func (s *Server) cbricsAccount(w http.ResponseWriter, r *http.Request) {
	var b ops.CBRICSAcctChange
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CBRICSProposeAccount(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) cbricsFind(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Code string `json:"code"`
		PAN  string `json:"pan"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CBRICSFind(r.Context(), tenant(r), actor(r), b.Code, b.PAN)
	ok(w, v, err)
}

func (s *Server) cbricsFromClient(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CBRICSPartyFromClient(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v, err)
}

func (s *Server) cbricsSaveParty(w http.ResponseWriter, r *http.Request) {
	var b ops.CBRICSParty
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveCBRICSParty(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) cbricsImport(w http.ResponseWriter, r *http.Request) {
	n, err := s.ops.CBRICSImportParties(r.Context(), tenant(r), actor(r))
	ok(w, map[string]int{"added": n}, err)
}

func (s *Server) cbricsPartyRequest(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ops.CBRICSPartyReq
		BankKey string `json:"bank_key"`
		Demat   string `json:"demat"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CBRICSPartyRequest(r.Context(), tenant(r), actor(r), r.PathValue("code"), b.CBRICSPartyReq, b.BankKey, b.Demat)
	ok(w, v, err)
}

func (s *Server) cbricsDecideParty(w http.ResponseWriter, r *http.Request) {
	var b cbricsDecision
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideCBRICSParty(r.Context(), tenant(r), actor(r), r.PathValue("code"), b.Approve, b.Reason)
	ok(w, v, err)
}

// cbricsHook takes NSE's notification and answers {"code": 200}, as the
// protocol's Callback API asks.
func (s *Server) cbricsHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSON(w, 400, map[string]any{"code": 400, "messages": []string{"cannot read the body"}})
		return
	}
	if _, err := s.ops.CBRICSNotify(r.Context(), r.PathValue("tenant"), r.Header.Get("token"), r.RemoteAddr, body); err != nil {
		code := 400
		if errors.Is(err, ops.ErrForbidden) {
			code = 403
		}
		writeJSON(w, code, map[string]any{"code": code, "messages": []string{err.Error()}})
		return
	}
	writeJSON(w, 200, map[string]int{"code": 200})
}
