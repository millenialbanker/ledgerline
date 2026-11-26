package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// cyberResRoutes: technical glitches (internal/ops/glitches.go) and the
// resilience records of Cyber controls (cyberres.go, cyberset.go,
// backuprpo.go, capacity.go, relevidence.go, sandbox.go), with the
// provider's register of release evidence.
func (s *Server) cyberResRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/security/glitches", s.glitches, false)
	R("POST /v1/security/glitches", s.glitchRecord, true)
	R("POST /v1/security/glitches/{id}", s.glitchUpdate, true)
	R("POST /v1/security/glitches/{id}/report", s.glitchReport, true)
	R("POST /v1/security/glitches/{id}/notice", s.glitchNotice, true)
	R("POST /v1/security/glitches/{id}/exemption", s.glitchExempt, true)
	R("POST /v1/security/glitches/{id}/incident", s.glitchIncident, true)
	R("GET /v1/security/glitches/{id}/draft", s.glitchDraft, false)
	R("GET /v1/security/resilience", s.cyberRes, false)
	R("PUT /v1/security/resilience/settings", s.cyberResSettings, true)
	R("POST /v1/security/agreements", s.cyberAgreement, true)
	R("POST /v1/security/inspections", s.cyberInspection, true)
	R("POST /v1/security/sandbox", s.cyberSandbox, true)
	R("GET /v1/security/evidence-pack", s.cyberEvidence, false)
	// A test environment loads sample days on a production server too.
	if !s.dev {
		R("POST /v1/dev/sample", s.sandboxSample, true)
	}
	s.mux.HandleFunc("GET /provider/release-evidence", s.provider(s.provReleases))
	s.mux.HandleFunc("POST /provider/release-evidence", s.provider(s.provRelease))
}

func (s *Server) glitches(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Glitches(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) glitchRecord(w http.ResponseWriter, r *http.Request) {
	var b ops.GlitchInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordGlitch(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) glitchUpdate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ops.GlitchInput
		Status string `json:"status"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.UpdateGlitch(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.GlitchInput, b.Status)
	ok(w, v, err)
}

func (s *Server) glitchReport(w http.ResponseWriter, r *http.Request) {
	var b ops.GlitchReport
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReportGlitch(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) glitchNotice(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Channel string   `json:"channel"`
		Target  string   `json:"target"`
		UCCs    []string `json:"uccs"`
		Text    string   `json:"text"`
		Status  string   `json:"status"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.NoticeGlitch(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Channel, b.Target, b.UCCs, b.Text, b.Status)
	ok(w, v, err)
}

func (s *Server) glitchExempt(w http.ResponseWriter, r *http.Request) {
	var b ops.GlitchExemption
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.GlitchExempt(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) glitchIncident(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Incident string `json:"incident"`
		Severity string `json:"severity"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.LinkGlitchIncident(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Incident, b.Severity)
	ok(w, v, err)
}

func (s *Server) glitchDraft(w http.ResponseWriter, r *http.Request) {
	b, name, err := s.ops.GlitchDraft(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.URL.Query().Get("annexure"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, name, "text/plain; charset=utf-8", b)
}

func (s *Server) cyberRes(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CyberResView(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) cyberResSettings(w http.ResponseWriter, r *http.Request) {
	var b ops.CyberSettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveCyberSettings(r.Context(), tenant(r), actor(r), b)
	if err == nil {
		s.guards.Delete(tenant(r))
	}
	ok(w, v, err)
}

func (s *Server) cyberAgreement(w http.ResponseWriter, r *http.Request) {
	var b ops.ConfAgreement
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordAgreement(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) cyberInspection(w http.ResponseWriter, r *http.Request) {
	var b ops.InspectionVisit
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveInspectionVisit(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) cyberSandbox(w http.ResponseWriter, r *http.Request) {
	v, pw, err := s.ops.PrepareSandbox(r.Context(), tenant(r), actor(r), s.ingester())
	if err != nil && v.Code == "" {
		fail(w, err, nil)
		return
	}
	out := map[string]any{"sandbox": v, "password": pw}
	if err != nil {
		out["warning"] = err.Error()
	}
	writeJSON(w, 200, out)
}

func (s *Server) cyberEvidence(w http.ResponseWriter, r *http.Request) {
	b, name, err := s.ops.EvidencePack(r.Context(), tenant(r), actor(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, name, "application/json", b)
}

// sandboxSample loads a sample day in a test environment (production
// servers otherwise have no sample data).
func (s *Server) sandboxSample(w http.ResponseWriter, r *http.Request) {
	if !s.ops.IsSandbox(tenant(r)) {
		writeErr(w, http.StatusForbidden, "not_sandbox", "Sample data loads only in a test environment.", nil)
		return
	}
	var b struct {
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	ctx, t, a := r.Context(), tenant(r), actor(r)
	if b.Date == "" {
		b.Date, _ = s.ops.NextSampleDay(ctx, t)
	}
	n, err := s.ops.LoadSampleFor(ctx, t, a, b.Date, s.ingester())
	ok(w, map[string]any{"files": n, "date": b.Date}, err)
}

func (s *Server) provReleases(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.ops.ReleaseEvidenceList(r.Context()))
}

func (s *Server) provRelease(w http.ResponseWriter, r *http.Request) {
	var b ops.ReleaseEvidence
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordReleaseEvidence(r.Context(), providerActor(r), b)
	ok(w, v, err)
}
