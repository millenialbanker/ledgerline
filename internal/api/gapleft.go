package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// onboardPledgeRoutes: Compliance → KYC trading gate → Onboarding checks
// (internal/ops/onboardx.go), Collateral → Pledges (CDSL) → Controls
// (pledgecontrols.go), the collateral register and the register of client
// code modifications (ccmregister.go). Registered from kycGateRoutes.
func (s *Server) onboardPledgeRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/onboarding-checks", s.onboardChecks, false)
	R("POST /v1/onboarding/{ucc}", s.onboardSave, true)
	R("POST /v1/ucc-controls", s.uccControlsSave, true)
	R("GET /v1/pledge-controls", s.pledgeControls, false)
	R("POST /v1/pledge-controls/letters", s.pledgeLetters, true)
	R("POST /v1/pledge-controls/costs", s.pledgeCosts, true)
	R("POST /v1/pledge-controls/lock-ins", s.pledgeLockIn, true)
	R("GET /v1/collateral-register", s.collateralRegister, false)
	R("GET /v1/ccm/register", s.ccmRegister, false)
	R("POST /v1/ccm/report", s.ccmReport, true)
}

func (s *Server) onboardChecks(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.OnboardChecks(r.Context(), tenant(r), actor(r), r.URL.Query().Get("ucc"))
	ok(w, v, err)
}

func (s *Server) onboardSave(w http.ResponseWriter, r *http.Request) {
	var b ops.ClientOnboarding
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveOnboarding(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b)
	ok(w, v, err)
}

func (s *Server) uccControlsSave(w http.ResponseWriter, r *http.Request) {
	var b ops.UCCControls
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveUCCControls(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) pledgeControls(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PledgeControls(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) pledgeLetters(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SendPledgeLetters(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) pledgeCosts(w http.ResponseWriter, r *http.Request) {
	var b ops.PledgeCosts
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SavePledgeCosts(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) pledgeLockIn(w http.ResponseWriter, r *http.Request) {
	var b ops.LockIn
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordLockIn(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

// collateralRegister: ?from=&to=&format=csv for the file.
func (s *Server) collateralRegister(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("format") == "csv" {
		name, b, err := s.ops.CollateralRegisterCSV(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
		if err != nil {
			ok(w, nil, err)
			return
		}
		sendFile(w, name, "text/csv; charset=utf-8", b)
		return
	}
	v, err := s.ops.CollateralRegister(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
	ok(w, v, err)
}

// ccmRegister: ?from=&to=&format=csv for the file.
func (s *Server) ccmRegister(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("format") == "csv" {
		name, b, err := s.ops.CCMRegisterCSV(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
		if err != nil {
			ok(w, nil, err)
			return
		}
		sendFile(w, name, "text/csv; charset=utf-8", b)
		return
	}
	v, err := s.ops.CCMRegister(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
	ok(w, v, err)
}

func (s *Server) ccmReport(w http.ResponseWriter, r *http.Request) {
	var b struct {
		TradeKey string `json:"trade_key"`
		Code     string `json:"code"`
		On       string `json:"on"`
		Ref      string `json:"ref"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordCCMReport(r.Context(), tenant(r), actor(r), b.TradeKey, b.Code, b.On, b.Ref)
	ok(w, v, err)
}
