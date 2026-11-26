package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// DP controls (internal/ops/dpdis.go, dpreqtat.go, dpctl.go; web/dpctl.js):
// DIS books against the requisition slip, loss reports, the compliance
// officer's authorisation for dormant accounts, waivers, the call to the
// holder and the countersignature of slips above ₹5 lakh, joint accounts'
// mode of operation, request steps and timelines, the daily
// reconciliation, the blank slip stock and pool pay-outs. Clients' own
// requests to freeze online trading (internal/ops/tradefreeze.go) sit on
// RMS & limits. Registered from opsRoutes.
func (s *Server) dpCtlRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/dp/controls", s.dpCtl, false)
	R("POST /v1/dp/controls/settings", s.dpCtlSettings, true)
	R("POST /v1/dp/books/{id}/lost", s.dpBookLost, true)
	R("POST /v1/dp/books/{id}/authorise", s.dpBookAuthorise, true)
	R("POST /v1/dp/accounts/{ucc}/dis-waiver", s.dpDISWaiver, true)
	R("POST /v1/dp/accounts/{ucc}/joint-operation", s.dpJointOp, true)
	R("POST /v1/dp/dis/{id}/call", s.dpDISCall, true)
	R("POST /v1/dp/dis/{id}/call-approve", s.dpDISCallApprove, true)
	R("POST /v1/dp/dis/{id}/countersign", s.dpDISCountersign, true)
	R("GET /v1/dp/request-timelines", s.dpReqTimelines, false)
	R("POST /v1/dp/requests/{id}/steps", s.dpReqStep, true)
	R("GET /v1/dp/recon", s.dpRecon, false)
	R("POST /v1/dp/recon", s.dpReconSign, true)
	R("POST /v1/dp/stock", s.dpStock, true)
	R("GET /v1/dp/pool-payouts", s.dpPool, false)
	R("GET /v1/rms/freeze-requests", s.tradeFreezes, false)
	R("POST /v1/rms/freeze-requests", s.tradeFreezeNew, true)
	R("POST /v1/rms/freeze-requests/{id}", s.tradeFreezeAct, true)
	R("POST /v1/rms/freeze-requests/{id}/reenable", s.tradeFreezeReenable, true)
}

func (s *Server) dpCtl(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.DPControlsPanel(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) dpCtlSettings(w http.ResponseWriter, r *http.Request) {
	var b ops.DPControls
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveDPControls(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) dpBookLost(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Ref string `json:"ref"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReportDISBookLost(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Ref)
	ok(w, v, err)
}

func (s *Server) dpBookAuthorise(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AuthoriseDISBook(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Note)
	ok(w, v, err)
}

func (s *Server) dpDISWaiver(w http.ResponseWriter, r *http.Request) {
	var b struct {
		BOID string `json:"bo_id"`
		Ref  string `json:"ref"`
	}
	if !decode(w, r, &b) || !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	v, err := s.ops.RecordDISWaiver(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.BOID, b.Ref)
	ok(w, v, err)
}

func (s *Server) dpJointOp(w http.ResponseWriter, r *http.Request) {
	var b struct {
		BOID    string `json:"bo_id"`
		Mode    string `json:"mode"`
		Holders int    `json:"holders"`
		Ref     string `json:"ref"`
	}
	if !decode(w, r, &b) || !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	v, err := s.ops.SetJointOperation(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.BOID, b.Mode, b.Holders, b.Ref)
	ok(w, v, err)
}

func (s *Server) dpDISCall(w http.ResponseWriter, r *http.Request) {
	var b ops.DISCall
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordDISCall(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) dpDISCallApprove(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ApproveDISCall(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) dpDISCountersign(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CountersignDIS(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) dpReqTimelines(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.DPRequestTimelines(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) dpReqStep(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Step string `json:"step"`
		On   string `json:"on"`
		Ref  string `json:"ref"`
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordDPStep(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Step, b.On, b.Ref, b.Note)
	ok(w, v, err)
}

func (s *Server) dpRecon(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.DPReconcile(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) dpReconSign(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string         `json:"date"`
		CDSL map[string]int `json:"cdsl"`
		Note string         `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SignDPRecon(r.Context(), tenant(r), actor(r), b.Date, b.CDSL, b.Note)
	ok(w, v, err)
}

func (s *Server) dpStock(w http.ResponseWriter, r *http.Request) {
	var b ops.DISStockEntry
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordDISStock(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) dpPool(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.PoolPayouts(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
	ok(w, v, err)
}

func (s *Server) tradeFreezes(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.TradeFreezes(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) tradeFreezeNew(w http.ResponseWriter, r *http.Request) {
	var b ops.TradeFreezeIntake
	if !decode(w, r, &b) || !s.canSee(w, r, b.UCC) {
		return
	}
	v, err := s.ops.ReceiveTradeFreeze(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) tradeFreezeAct(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Step string `json:"step"`
		Ref  string `json:"ref"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ActOnTradeFreeze(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Step, b.Ref)
	ok(w, v, err)
}

func (s *Server) tradeFreezeReenable(w http.ResponseWriter, r *http.Request) {
	var b ops.TradeReenable
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReenableTrading(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}
