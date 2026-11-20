package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// complianceCalendarRoutes: the compliance profile with the exchanges' RAS
// calendar and the system audit cycle, and the holding statement sent to
// BSE on its portal. Registered from exchangeReportingRoutes.
func (s *Server) complianceCalendarRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/compliance/profile", s.compProfile, false)
	R("PUT /v1/compliance/profile", s.saveCompProfile, true)
	R("GET /v1/duties/sheet", s.dutySheet, false)
	R("POST /v1/duties/{id}/events", s.dutyEvent, true)
	R("POST /v1/duties/{id}/applies", s.dutyApplies, true)
	R("GET /v1/holding-api/bse", s.bseHoldingView, false)
	R("POST /v1/holding-api/bse", s.bseHoldingUploaded, true)
}

func (s *Server) compProfile(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CompProfileFor(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) saveCompProfile(w http.ResponseWriter, r *http.Request) {
	var b ops.ComplianceProfile
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveCompProfile(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

// dutySheet is the month's compliance sheet (?month=YYYY-MM), as JSON or,
// with format=csv, as a file.
func (s *Server) dutySheet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.DutySheetFor(r.Context(), tenant(r), actor(r), q.Get("month"), q.Get("date"))
	if err == nil && q.Get("format") == "csv" {
		sendFile(w, "compliance-sheet-"+v.Month+".csv", "text/csv; charset=utf-8", v.CSV())
		return
	}
	ok(w, v, err)
}

// dutyEvent records that something happened that an event-driven duty
// must answer.
func (s *Server) dutyEvent(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ops.DutyEventReq
		On string `json:"on"` // the business date (today)
	}
	if !decode(w, r, &b) {
		return
	}
	if _, okd := dateParam(w, b.On, "on", false); !okd {
		return
	}
	v, err := s.ops.RecordDutyEvent(r.Context(), tenant(r), actor(r), b.On, r.PathValue("id"), b.DutyEventReq)
	ok(w, v, err)
}

// dutyApplies records the compliance officer's decision that a duty does
// not apply to the firm (with the reason), or takes it back.
func (s *Server) dutyApplies(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ops.DutyAppliesReq
		On string `json:"on"` // the business date (today)
	}
	if !decode(w, r, &b) {
		return
	}
	if _, okd := dateParam(w, b.On, "on", false); !okd {
		return
	}
	v, err := s.ops.SetDutyApplies(r.Context(), tenant(r), actor(r), b.On, r.PathValue("id"), b.DutyAppliesReq)
	ok(w, v, err)
}

func (s *Server) bseHoldingView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BSEHolding(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) bseHoldingUploaded(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
		Ack  string `json:"ack"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.BSEHoldingUploaded(r.Context(), tenant(r), actor(r), b.Date, b.Ack)
	ok(w, v, err)
}

// upiCollectRoutes: the firm's validated UPI IDs and SEBI QR code, shown in
// the client portal. Registered from exchangeReportingRoutes.
func (s *Server) upiCollectRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/settings/upi-collect", s.upiCollect, false)
	R("PUT /v1/settings/upi-collect", s.saveUPICollect, true)
	R("POST /v1/bank/{id}/move-fee", s.moveFeeCredit, true)
}

func (s *Server) upiCollect(w http.ResponseWriter, r *http.Request) {
	ok(w, s.ops.UPICollectFor(r.Context(), tenant(r)), nil)
}

// moveFeeCredit records that a research or advisory fee paid into the
// client bank account was moved to the firm's own account.
func (s *Server) moveFeeCredit(w http.ResponseWriter, r *http.Request) {
	var b struct{ Ref string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MoveFeeCredit(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Ref)
	ok(w, v.Public(), err)
}

func (s *Server) saveUPICollect(w http.ResponseWriter, r *http.Request) {
	var b ops.UPICollect
	if !decode(w, r, &b) {
		return
	}
	if _, err := s.ops.SaveUPICollect(r.Context(), tenant(r), actor(r), b); err != nil {
		fail(w, err, nil)
		return
	}
	ok(w, s.ops.UPICollectFor(r.Context(), tenant(r)), nil)
}

// surveillanceReturnRoutes: the quarterly surveillance obligation report
// (Formats I to III), referring an alert to the exchange and the major
// surveillance actions of a quarter. Registered from exchangeReportingRoutes.
func (s *Server) surveillanceReturnRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/surveillance-return", s.survReturn, false)
	R("POST /v1/surveillance-return/refer", s.survRefer, true)
	R("POST /v1/surveillance-return/action", s.survAction, true)
}

func (s *Server) survReturn(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SurvReturnFor(r.Context(), tenant(r), actor(r), r.URL.Query().Get("quarter"))
	ok(w, v, err)
}

func (s *Server) survRefer(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ID          string `json:"id"`
		Date        string `json:"date"`
		Observation string `json:"observation"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReferAlert(r.Context(), tenant(r), actor(r), b.ID, b.Date, b.Observation)
	ok(w, v, err)
}

func (s *Server) survAction(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Quarter string `json:"quarter"`
		Text    string `json:"text"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AddSurvAction(r.Context(), tenant(r), actor(r), b.Quarter, b.Text)
	ok(w, v, err)
}
