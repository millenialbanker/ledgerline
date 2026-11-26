package api

import (
	"net/http"
	"strings"

	"ledgerline/internal/money"
	"ledgerline/internal/ops"
)

// cyberRoutes: locked sign-ins, incident follow-ups, cyber resilience
// records, clients' data rights and fraud reports, and the insider trading
// code's and staff controls' records (internal/ops/authlock.go,
// incfollow.go, resilience.go, dataprivacy.go, fraudreport.go, pit.go,
// staffctl.go).
func (s *Server) cyberRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	C := func(p string, h http.HandlerFunc, m bool) { clientRoutes[p] = true; s.route(p, h, m) }
	B := func(p string, h http.HandlerFunc, m bool) { scopedRoutes[p] = true; s.route(p, h, m) }
	// Sign-in: unlock by a code sent to the e-mail on record (no session).
	s.mux.HandleFunc("POST /v1/auth/unlock-code", s.unlockCode)
	s.mux.HandleFunc("POST /v1/auth/unlock", s.unlockWithCode)
	R("POST /v1/users/{id}/unlock", s.unlockUser, true)
	// Incidents.
	R("POST /v1/security/incidents/{id}/not-needed", s.incNotNeeded, true)
	R("POST /v1/security/incidents/{id}/personal-data", s.incPersonalData, true)
	R("POST /v1/security/incidents/{id}/breach-notice", s.incBreachNotice, true)
	R("POST /v1/security/incidents/{id}/disaster", s.incDisaster, true)
	// Cyber controls.
	R("GET /v1/security/cyber", s.cyberControls, false)
	R("POST /v1/security/drills", s.cyberDrill, true)
	R("POST /v1/security/vendors", s.cyberVendor, true)
	R("GET /v1/security/sbom", s.cyberSBOM, false)
	R("POST /v1/security/privileged-review", s.cyberPrivBuild, true)
	R("POST /v1/security/privileged-review/{id}/{user}", s.cyberPrivSign, true)
	// Data protection.
	R("PUT /v1/privacy/settings", s.privacySave, true)
	R("POST /v1/privacy/requests/{id}", s.privacyAnswer, true)
	R("POST /v1/privacy/erase/{ucc}", s.privacyErase, true)
	R("POST /v1/clients/{ucc}/kyc-source", s.kycSource, true)
	B("POST /v1/clients/{ucc}/data-consent", s.dataConsent, true)
	R("POST /v1/fraud-reports/{id}/close", s.fraudClose, true)
	R("GET /v1/fraud-reports", s.fraudList, false)
	// The client portal.
	C("GET /v1/portal/terms", s.portalTerms, false)
	C("POST /v1/portal/terms", s.portalAcceptTerms, true)
	C("GET /v1/portal/my-data", s.portalMyData, false)
	C("POST /v1/portal/data-requests", s.portalDataRequest, true)
	C("POST /v1/portal/data-nominee", s.portalDataNominee, true)
	C("POST /v1/portal/fraud", s.portalFraud, true)
	C("GET /v1/portal/fraud", s.fraudList, false)
	// Insider trading code and staff controls.
	B("POST /v1/hr/own-trading/pre-clearance/{id}/report", s.hrPreClearReport, true)
	B("POST /v1/hr/own-trading/disclosures", s.hrPITDisclose, true)
	R("POST /v1/hr/own-trading/board-approval", s.hrPITBoard, true)
	R("POST /v1/hr/own-trading/upsi", s.hrUPSI, true)
	R("POST /v1/hr/staff/{code}/designated", s.hrDesignated, true)
	R("GET /v1/hr/staff-controls", s.hrStaffControls, false)
	R("POST /v1/hr/staff/{code}/aml-check", s.hrAMLCheck, true)
	R("POST /v1/hr/staff/{code}/co-eligibility", s.hrCOEligibility, true)
}

// termsFree are the client routes open before the terms are accepted.
var termsFree = map[string]bool{"GET /v1/me": true, "GET /v1/portal/terms": true, "POST /v1/portal/terms": true}

func (s *Server) unlockCode(w http.ResponseWriter, r *http.Request) {
	var b struct{ Tenant, User string }
	if !decode(w, r, &b) {
		return
	}
	ip := clientIP(r)
	if !s.loginLimit.allow(ip) {
		writeErr(w, 429, "too_many_attempts", "Too many attempts from this address. Wait 15 minutes and try again.", nil)
		return
	}
	t := strings.ToLower(strings.TrimSpace(b.Tenant))
	if ts := s.Tenants(); t == "" && len(ts) == 1 {
		t = ts[0]
	}
	if !s.knownTenant(t) {
		writeErr(w, 401, "auth_failed", "Unknown firm code.", nil)
		return
	}
	writeJSON(w, 200, map[string]any{"message": s.ops.RequestUnlockCode(r.Context(), t, b.User, ip)})
}

func (s *Server) unlockWithCode(w http.ResponseWriter, r *http.Request) {
	var b struct{ Tenant, User, Code string }
	if !decode(w, r, &b) {
		return
	}
	ip := clientIP(r)
	if !s.loginLimit.allow(ip) {
		writeErr(w, 429, "too_many_attempts", "Too many attempts from this address. Wait 15 minutes and try again.", nil)
		return
	}
	t := strings.ToLower(strings.TrimSpace(b.Tenant))
	if ts := s.Tenants(); t == "" && len(ts) == 1 {
		t = ts[0]
	}
	if !s.knownTenant(t) {
		writeErr(w, 401, "auth_failed", "Unknown firm code.", nil)
		return
	}
	ok(w, map[string]any{"unlocked": true}, s.ops.UnlockWithCode(r.Context(), t, b.User, b.Code, ip))
}

func (s *Server) unlockUser(w http.ResponseWriter, r *http.Request) {
	var b struct{ How string }
	if !decode(w, r, &b) {
		return
	}
	ok(w, map[string]any{"unlocked": true}, s.ops.UnlockUser(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.How))
}

func (s *Server) incNotNeeded(w http.ResponseWriter, r *http.Request) {
	var b struct{ Stage, Why string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkFollowUpNotNeeded(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Stage, b.Why)
	ok(w, v, err)
}

func (s *Server) incPersonalData(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On   bool   `json:"on"`
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkPersonalData(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.On, b.Note)
	ok(w, v, err)
}

func (s *Server) incBreachNotice(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCCs         []string `json:"uccs"`
		What         string   `json:"what"`
		Consequences string   `json:"consequences"`
		Done         string   `json:"done"`
		Steps        string   `json:"steps"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.NotifyBreachClients(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.UCCs, b.What, b.Consequences, b.Done, b.Steps)
	ok(w, v, err)
}

func (s *Server) incDisaster(w http.ResponseWriter, r *http.Request) {
	var b struct{ Disrupted, Recovered, Note string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DeclareDisaster(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Disrupted, b.Recovered, b.Note)
	ok(w, v, err)
}

func (s *Server) cyberControls(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CyberControlsView(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) cyberDrill(w http.ResponseWriter, r *http.Request) {
	var b ops.RestoreDrill
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordRestoreDrill(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) cyberVendor(w http.ResponseWriter, r *http.Request) {
	var b ops.TechVendor
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveTechVendor(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) cyberSBOM(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.SBOM(r.Context(), tenant(r), actor(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.cyclonedx+json")
	w.Header().Set("Content-Disposition", `attachment; filename="ledgerline-sbom.cdx.json"`)
	w.Write(b)
}

func (s *Server) cyberPrivBuild(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.BuildPrivReview(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, v, err)
}

func (s *Server) cyberPrivSign(w http.ResponseWriter, r *http.Request) {
	var b struct{ Note string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SignPrivReview(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("user"), b.Note)
	ok(w, v, err)
}

func (s *Server) privacySave(w http.ResponseWriter, r *http.Request) {
	var b ops.PrivacySettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SavePrivacySettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) privacyAnswer(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Done   bool   `json:"done"`
		Answer string `json:"answer"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AnswerDataRequest(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Done, b.Answer)
	ok(w, v, err)
}

func (s *Server) privacyErase(w http.ResponseWriter, r *http.Request) {
	var b struct{ Reason string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.EraseClient(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Reason)
	ok(w, map[string]any{"ucc": v.UCC, "erased_on": v.ErasedOn}, err)
}

func (s *Server) kycSource(w http.ResponseWriter, r *http.Request) {
	var b struct{ Source string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetKYCSource(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Source)
	ok(w, map[string]any{"ucc": v.UCC, "kyc_source": v.KYCSource}, err)
}

func (s *Server) dataConsent(w http.ResponseWriter, r *http.Request) {
	if !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	var b struct{ Source, Ref string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordDataConsent(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Source, b.Ref)
	ok(w, map[string]any{"ucc": v.UCC, "data_consent": v.DataConsent}, err)
}

func (s *Server) fraudList(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FraudReports(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) fraudClose(w http.ResponseWriter, r *http.Request) {
	var b struct{ Outcome string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CloseFraudReport(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Outcome)
	ok(w, v, err)
}

func (s *Server) portalTerms(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PortalTermsFor(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) portalAcceptTerms(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Terms  int `json:"terms_version"`
		Notice int `json:"notice_version"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AcceptTerms(r.Context(), tenant(r), actor(r), b.Terms, b.Notice, clientIP(r))
	ok(w, v, err)
}

func (s *Server) portalMyData(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PortalDataSummary(r.Context(), tenant(r), actor(r), r.URL.Query().Get("ucc"))
	ok(w, v, err)
}

func (s *Server) portalDataRequest(w http.ResponseWriter, r *http.Request) {
	var b struct{ UCC, Kind, Detail string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RequestData(r.Context(), tenant(r), actor(r), b.UCC, b.Kind, b.Detail)
	ok(w, v, err)
}

func (s *Server) portalDataNominee(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC string `json:"ucc"`
		ops.DataNominee
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetDataNominee(r.Context(), tenant(r), actor(r), b.UCC, b.DataNominee)
	ok(w, v, err)
}

func (s *Server) portalFraud(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC    string `json:"ucc"`
		Kind   string `json:"kind"`
		When   string `json:"when"`
		Amount string `json:"amount"`
		Ref    string `json:"ref"`
		Detail string `json:"detail"`
		Block  bool   `json:"block"`
	}
	if !decode(w, r, &b) {
		return
	}
	var amt money.Amount
	if strings.TrimSpace(b.Amount) != "" {
		a, err := money.ParseAmount(strings.ReplaceAll(b.Amount, ",", ""))
		if err != nil {
			writeErr(w, 400, "invalid", "The amount is a number of rupees.", nil)
			return
		}
		amt = a
	}
	v, err := s.ops.ReportFraud(r.Context(), tenant(r), actor(r), ops.FraudReport{UCC: b.UCC, Kind: b.Kind, When: b.When, Amount: amt, Ref: b.Ref, Detail: b.Detail, Block: b.Block}, clientIP(r))
	ok(w, v, err)
}

func (s *Server) hrPreClearReport(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Traded    bool   `json:"traded"`
		TradeDate string `json:"trade_date"`
		Qty       int64  `json:"qty"`
		Price     string `json:"price"`
		Reason    string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	var p money.Amount
	if strings.TrimSpace(b.Price) != "" {
		x, err := money.ParseAmount(strings.ReplaceAll(b.Price, ",", ""))
		if err != nil {
			writeErr(w, 400, "invalid", "The price is a number of rupees.", nil)
			return
		}
		p = x
	}
	v, err := s.ops.ReportPreClearance(r.Context(), tenant(r), actor(r), r.PathValue("id"), ops.PreClearReport{Traded: b.Traded, TradeDate: b.TradeDate, Qty: b.Qty, Price: p, Reason: b.Reason})
	ok(w, v, err)
}

func (s *Server) hrPITDisclose(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Code string `json:"code"`
		ops.PITDisclosure
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DeclarePITDetails(r.Context(), tenant(r), actor(r), b.Code, b.PITDisclosure)
	ok(w, v, err)
}

func (s *Server) hrPITBoard(w http.ResponseWriter, r *http.Request) {
	var b struct{ Criteria, Date, Resolution string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordPITBoardApproval(r.Context(), tenant(r), actor(r), b.Criteria, b.Date, b.Resolution)
	ok(w, v, err)
}

func (s *Server) hrUPSI(w http.ResponseWriter, r *http.Request) {
	var b ops.UPSIShare
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordUPSIShare(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) hrDesignated(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On     bool   `json:"on"`
		Ground string `json:"ground"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetDesignated(r.Context(), tenant(r), actor(r), r.PathValue("code"), b.On, b.Ground)
	ok(w, map[string]any{"code": v.Code, "designated": v.Designated, "designated_for": v.DesignatedFor}, err)
}

func (s *Server) hrStaffControls(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.StaffControlsView(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) hrAMLCheck(w http.ResponseWriter, r *http.Request) {
	var b ops.AMLCheck
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordAMLCheck(r.Context(), tenant(r), actor(r), r.PathValue("code"), b)
	ok(w, map[string]any{"code": v.Code, "aml_check": v.AMLCheck}, err)
}

func (s *Server) hrCOEligibility(w http.ResponseWriter, r *http.Request) {
	var b ops.COEligibility
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordCOEligibility(r.Context(), tenant(r), actor(r), r.PathValue("code"), b)
	ok(w, map[string]any{"code": v.Code, "co_eligibility": v.COEligible}, err)
}
