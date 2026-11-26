package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// Round-6 gap work (internal/ops/dpb6.go, ordercancel.go, survb6.go;
// web/b6dp.js): AMC refunds on closure, death intimations received
// directly and the certificate's check, CDSL KYC deficiency cases, OTP
// transfer-cum-closure requests, reason-27 unfreezes, nomination opt-out
// declarations, PoA/DDPI terms; blocked clients' pending orders; shares
// outstanding, the round-6 trade rules and their thresholds. Registered
// from opsRoutes.
func (s *Server) b6Routes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/dp/b6", s.b6Panel, false)
	R("POST /v1/dp/amc-refunds/{bo}", s.b6AMC, true)
	R("POST /v1/deceased/intimations", s.b6Intimation, true)
	R("POST /v1/deceased/{id}/certificate", s.b6Cert, true)
	R("POST /v1/dp/kyc-deficiencies", s.b6KYCDef, true)
	R("POST /v1/dp/kyc-deficiencies/{id}", s.b6KYCDefStep, true)
	R("POST /v1/dp/otp-closures", s.b6OTP, true)
	R("POST /v1/dp/otp-closures/{id}", s.b6OTPDecide, true)
	R("POST /v1/dp/kyc-unfreezes", s.b6Unfreeze, true)
	R("POST /v1/clients/{ucc}/nomination-optout", s.b6OptOut, true)
	R("POST /v1/dp/poa-checks", s.b6PoA, true)
	R("POST /v1/dp/poa-checks/{bo}/approve", s.b6PoAApprove, true)
	R("GET /v1/rms/order-cancels", s.b6Cancels, false)
	R("POST /v1/rms/order-cancels/{id}", s.b6CancelConfirm, true)
	R("POST /v1/rms/order-cancels/{id}/check", s.b6CancelCheck, true)
	R("GET /v1/surveillance/b6", s.b6Surv, false)
	R("POST /v1/surveillance/b6/settings", s.b6SurvSettings, true)
	R("POST /v1/surveillance/shares-outstanding", s.b6Shares, true)
	R("POST /v1/surveillance/trade-patterns", s.b6Patterns, true)
}

func (s *Server) b6Panel(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.DPB6Panel(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) b6AMC(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideAMCRefund(r.Context(), tenant(r), actor(r), r.PathValue("bo"), b.Approve, b.Note)
	ok(w, v, err)
}

func (s *Server) b6Intimation(w http.ResponseWriter, r *http.Request) {
	var b ops.DeathIntimationReq
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordDeathIntimation(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) b6Cert(w http.ResponseWriter, r *http.Request) {
	var b ops.DeathCertCheck
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordDeathCertCheck(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) b6KYCDef(w http.ResponseWriter, r *http.Request) {
	var b ops.KYCDefReq
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordKYCDeficiency(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) b6KYCDefStep(w http.ResponseWriter, r *http.Request) {
	var b struct{ Step, On, Ref string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.KYCDefStep(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Step, b.On, b.Ref)
	ok(w, v, err)
}

func (s *Server) b6OTP(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC      string `json:"ucc"`
		BOID     string `json:"bo_id"`
		Target   string `json:"target_bo_id"`
		CDSLRef  string `json:"cdsl_ref"`
		Received string `json:"received"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordOTPClosure(r.Context(), tenant(r), actor(r), b.UCC, b.BOID, b.Target, b.CDSLRef, b.Received)
	ok(w, v, err)
}

func (s *Server) b6OTPDecide(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool            `json:"approve"`
		Checks  map[string]bool `json:"checks"`
		Reason  string          `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideOTPClosure(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Checks, b.Reason)
	ok(w, v, err)
}

func (s *Server) b6Unfreeze(w http.ResponseWriter, r *http.Request) {
	n, err := s.ops.RaiseKYCUnfreezes(r.Context(), tenant(r), actor(r))
	ok(w, map[string]int{"raised": n}, err)
}

func (s *Server) b6OptOut(w http.ResponseWriter, r *http.Request) {
	var b ops.NominationDeclReq
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordOptOutDeclaration(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b)
	ok(w, v.Public(), err)
}

func (s *Server) b6PoA(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC   string          `json:"ucc"`
		BOID  string          `json:"bo_id"`
		Ref   string          `json:"ref"`
		Note  string          `json:"note"`
		Terms map[string]bool `json:"terms"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordPoACheck(r.Context(), tenant(r), actor(r), b.UCC, b.BOID, b.Ref, b.Note, b.Terms)
	ok(w, v, err)
}

func (s *Server) b6PoAApprove(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ApprovePoACheck(r.Context(), tenant(r), actor(r), r.PathValue("bo"))
	ok(w, v, err)
}

func (s *Server) b6Cancels(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.OrderCancels(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) b6CancelConfirm(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Ref string `json:"ref"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ConfirmOrderCancel(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Ref)
	ok(w, v, err)
}

func (s *Server) b6CancelCheck(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CheckOrderCancel(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) b6Surv(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.TradeSurvB6Panel(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) b6SurvSettings(w http.ResponseWriter, r *http.Request) {
	var b ops.SurvB6Config
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveSurvB6Settings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) b6Shares(w http.ResponseWriter, r *http.Request) {
	data, okb := readBody(w, r, 32<<20)
	if !okb {
		return
	}
	n, err := s.ops.LoadSharesOutstanding(r.Context(), tenant(r), actor(r), r.URL.Query().Get("source"), data)
	ok(w, map[string]int{"loaded": n}, err)
}

func (s *Server) b6Patterns(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RunTradePatterns(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, v, err)
}
