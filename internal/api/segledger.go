package api

import (
	"net/http"
	"sync"

	"ledgerline/internal/money"
)

var segLedgerRouted sync.Map // *Server -> true

// segLedgerRoutes: separate equity and commodity ledgers (ops/segledger.go):
// money awaiting allocation, transfers between a client's two ledgers, a
// client's default ledger and the commodity front end's funds.
func (s *Server) segLedgerRoutes() {
	if s.ops == nil {
		return
	}
	if _, dup := segLedgerRouted.LoadOrStore(s, true); dup {
		return
	}
	s.route("GET /v1/funds-awaiting", s.fundsAwaiting, false)
	s.route("POST /v1/funds-awaiting/{id}/allocate", s.allocateFunds, true)
	s.route("POST /v1/clients/{ucc}/ledger-transfer", s.ledgerTransfer, true)
	s.route("POST /v1/clients/{ucc}/funds-ledger", s.clientFundsLedger, true)
	s.route("GET /v1/bow-funds", s.bowFunds, false)
	// One ledger with MCX money earmarked (ops/mcxearmark.go): RMS moves
	// between MCX money and equity money.
	s.route("GET /v1/mcx-moves", s.mcxMoves, false)
	s.route("POST /v1/clients/{ucc}/mcx-move", s.mcxMove, true)
	s.route("POST /v1/mcx-moves/{id}/decide", s.decideMCXMove, true)
}

// mcxMoves lists moves between MCX and equity money (?ucc= for one client).
func (s *Server) mcxMoves(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("ucc")
	if u != "" && !s.canSee(w, r, u) {
		return
	}
	v, err := s.ops.MCXMoves(r.Context(), tenant(r), u)
	if f := s.scope(r); f != nil && err == nil {
		out := v[:0]
		for _, m := range v {
			if f(m.UCC) {
				out = append(out, m)
			}
		}
		v = out
	}
	ok(w, v, err)
}

// mcxMove asks to move a client's money: {"to":"MCX"|"EQ","amount":...}.
func (s *Server) mcxMove(w http.ResponseWriter, r *http.Request) {
	var b struct {
		To     string       `json:"to"`
		Amount money.Amount `json:"amount"`
		Date   string       `json:"date"`
		Note   string       `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	v, err := s.ops.RequestMCXMove(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.To, b.Date, b.Amount, b.Note)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

// decideMCXMove approves or rejects a move: {"approve":true,"reason":""}.
func (s *Server) decideMCXMove(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideMCXMove(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Reason)
	ok(w, v, err)
}

// fundsAwaiting lists money awaiting allocation (?all=1 with allocated).
func (s *Server) fundsAwaiting(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FundsAwaitingList(r.Context(), tenant(r), r.URL.Query().Get("all") != "1")
	if f := s.scope(r); f != nil && err == nil {
		out := v[:0]
		for _, a := range v {
			if f(a.UCC) {
				out = append(out, a)
			}
		}
		v = out
	}
	ok(w, v, err)
}

// allocateFunds puts money awaiting allocation in a ledger: {"ledger":"EQ"|"COM"}.
func (s *Server) allocateFunds(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Ledger string `json:"ledger"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AllocateFunds(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Ledger)
	ok(w, v, err)
}

// ledgerTransfer asks for a transfer between a client's equity and
// commodity ledgers (a journal voucher for a checker).
func (s *Server) ledgerTransfer(w http.ResponseWriter, r *http.Request) {
	var b struct {
		From   string       `json:"from"`
		To     string       `json:"to"`
		Amount money.Amount `json:"amount"`
		Date   string       `json:"date"`
		Note   string       `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	v, err := s.ops.LedgerTransfer(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.From, b.To, b.Date, b.Amount, b.Note)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

// clientFundsLedger sets a client's default ledger: {"ledger":"EQ"|"COM"|""}.
func (s *Server) clientFundsLedger(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Ledger string `json:"ledger"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetClientFundsLedger(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Ledger)
	ok(w, v, err)
}

// bowFunds is the commodity front end's funds file for ?date=.
func (s *Server) bowFunds(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	b, err := s.ops.BOWFundsFile(r.Context(), tenant(r), d)
	if err != nil {
		fail(w, err, nil)
		return
	}
	s.app.Log(r.Context(), tenant(r), actor(r), "frontoffice.bow-funds", d, nil)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="BOW_COMMODITY_FUNDS_`+d+`.csv"`)
	w.Write(b)
}
