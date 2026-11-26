package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// kycGateRoutes: Compliance → KYC trading gate (internal/ops/kycgate.go,
// kycgate_cdsl.go, proi.go): the clients the KYC rules hold back, the
// exchanges' and CDSL's KYC lists, the demat freezes (CDSL reasons 27 and
// 31) and the PROI onboarding record.
func (s *Server) kycGateRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/kyc-gate", s.kycGateView, false)
	R("POST /v1/kyc-gate/lists", s.kycGateLoad, true)
	R("POST /v1/kyc-gate/clients/{ucc}/lift", s.kycGateLift, true)
	R("POST /v1/kyc-gate/freezes", s.kycGateFreezes, true)
	R("POST /v1/kyc-gate/mnrl/{id}/notice", s.kycGateNotice, true)
	R("POST /v1/kyc-gate/mnrl/{id}/freeze", s.kycGateMNRLFreeze, true)
	R("POST /v1/proi/{ucc}", s.proiSave, true)
	s.onboardPledgeRoutes(R)
}

func (s *Server) kycGateView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.KYCGate(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

// kycGateLoad loads a KYC list (the file's bytes as the body):
// ?list=&name=&date=&from=YYYY-MM-DD&circular=.
func (s *Server) kycGateLoad(w http.ResponseWriter, r *http.Request) {
	data, okb := listBody(w, r)
	if !okb {
		return
	}
	q := r.URL.Query()
	v, err := s.ops.LoadKYCList(r.Context(), tenant(r), actor(r), ops.KYCListLoadReq{List: q.Get("list"), Name: q.Get("name"), Date: q.Get("date"), From: q.Get("from"), Circular: q.Get("circular")}, data)
	ok(w, v, err)
}

func (s *Server) kycGateLift(w http.ResponseWriter, r *http.Request) {
	var b struct {
		List string `json:"list"`
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := s.ops.LiftKYCHold(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.List, b.Note)
	ok(w, c.Public(), err)
}

func (s *Server) kycGateFreezes(w http.ResponseWriter, r *http.Request) {
	n, err := s.ops.RaiseKYCFreezes(r.Context(), tenant(r), actor(r))
	ok(w, map[string]int{"raised": n}, err)
}

func (s *Server) kycGateNotice(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On   string `json:"on"`
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	m, err := s.ops.RecordMNRLNotice(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.On, b.Note)
	ok(w, m, err)
}

func (s *Server) kycGateMNRLFreeze(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RaiseMNRLFreeze(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) proiSave(w http.ResponseWriter, r *http.Request) {
	var b ops.PROIKYC
	if !decode(w, r, &b) {
		return
	}
	c, err := s.ops.SetPROI(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b)
	ok(w, c.Public(), err)
}
