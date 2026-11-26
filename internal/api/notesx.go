package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// Contract note registers and client documents (internal/ops/notesx.go,
// onboardkit.go; web/notesx.js): trades the exchange cancelled, the
// register of rights entitlements, a client's electronic contract note
// authorisation and the account opening kit. Registered from opsRoutes.
func (s *Server) notesXRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/notes/cancellations", s.tradeCancels, false)
	R("POST /v1/notes/cancellations", s.tradeCancelNew, true)
	R("GET /v1/notes/rights-entitlements", s.rightsEnts, false)
	R("POST /v1/notes/rights-entitlements", s.rightsEntSave, true)
	R("POST /v1/clients/{ucc}/ecn", s.clientECN, true)
	R("POST /v1/clients/{ucc}/kit", s.clientKit, true)
}

func (s *Server) tradeCancels(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.TradeCancels(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) tradeCancelNew(w http.ResponseWriter, r *http.Request) {
	var b ops.TradeCancel
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordTradeCancel(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) rightsEnts(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RightsEntitlementList(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) rightsEntSave(w http.ResponseWriter, r *http.Request) {
	var b ops.RightsEntitlement
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveRightsEntitlement(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) clientECN(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On  bool   `json:"on"`
		Doc string `json:"doc"`
	}
	if !decode(w, r, &b) || !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	v, err := s.ops.SetECNAuthorisation(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.On, b.Doc)
	ok(w, v, err)
}

func (s *Server) clientKit(w http.ResponseWriter, r *http.Request) {
	if !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	v, err := s.ops.SendAccountKit(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v, err)
}
