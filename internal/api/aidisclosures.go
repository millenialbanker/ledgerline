package api

import (
	"io"
	"net/http"
	"strconv"
	"sync"

	"ledgerline/internal/ops"
)

// aiDisclosureRouted makes aiDisclosureRoutes register once per server,
// however it is wired.
var aiDisclosureRouted sync.Map

// aiDisclosureRoutes: the AI controls (kill switch, AI inventory, masking,
// AI answer log) and the Exchange disclosures tab (the AI & ML and SaaS
// reports, the details they need, filings and their evidence).
func (s *Server) aiDisclosureRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := aiDisclosureRouted.LoadOrStore(s, true); done {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/ai/controls", s.aiControls, false)
	R("POST /v1/ai/off", s.aiOff, true)
	R("POST /v1/ai/providers/{id}", s.aiProvider, true)
	R("POST /v1/ai/masking", s.aiMasking, true)
	R("POST /v1/ai/log-retention", s.aiLogRetention, true)
	R("GET /v1/ai/log", s.aiLog, false)
	R("GET /v1/ai/log.csv", s.aiLogCSV, false)
	R("GET /v1/ai/inventory", s.aiInventory, false)
	R("POST /v1/ai/tools", s.aiTool, true)
	R("GET /v1/disclosures", s.exDisclosures, false)
	R("GET /v1/disclosures/{kind}", s.exDisclosure, false)
	R("GET /v1/disclosures/{kind}/{period}/csv", s.disclosureCSV, false)
	R("PUT /v1/disclosures/{kind}/{period}/answers", s.disclosureAnswer, true)
	R("POST /v1/disclosures/{kind}/{period}/submit", s.disclosureSubmit, true)
	R("POST /v1/disclosures/evidence", s.disclosureEvidenceUp, true)
	R("GET /v1/disclosures/evidence/{id}", s.disclosureEvidence, false)
	R("PUT /v1/disclosures/profile", s.disclosureProfile, true)
	R("PUT /v1/disclosures/deployment", s.disclosureDeployment, true)
}

func (s *Server) aiControls(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.AIControlsScreen(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) aiOff(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Off    bool   `json:"off"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetAIOff(r.Context(), tenant(r), actor(r), b.Off, b.Reason)
	ok(w, v, err)
}

func (s *Server) aiProvider(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Off    bool   `json:"off"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetAIProviderOff(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Off, b.Reason)
	ok(w, v, err)
}

func (s *Server) aiMasking(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Category string `json:"category"`
		On       bool   `json:"on"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetAIMasking(r.Context(), tenant(r), actor(r), b.Category, b.On)
	ok(w, v, err)
}

func (s *Server) aiLogRetention(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Days int `json:"days"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetAILogRetention(r.Context(), tenant(r), actor(r), b.Days)
	ok(w, v, err)
}

func (s *Server) aiLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.AILog(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
	ok(w, v, err)
}

func (s *Server) aiLogCSV(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	b, err := s.ops.AILogCSV(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, "ai_answer_log.csv", "text/csv; charset=utf-8", b)
}

func (s *Server) aiInventory(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.AIInventory(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) aiTool(w http.ResponseWriter, r *http.Request) {
	var b ops.AISystem
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveAITool(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) exDisclosures(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.Disclosures(r.Context(), tenant(r), actor(r), q.Get("aiml"), q.Get("saas"))
	ok(w, v, err)
}

func (s *Server) exDisclosure(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Disclosure(r.Context(), tenant(r), actor(r), r.PathValue("kind"), r.URL.Query().Get("period"))
	ok(w, v, err)
}

func (s *Server) disclosureCSV(w http.ResponseWriter, r *http.Request) {
	name, b, err := s.ops.DisclosureCSV(r.Context(), tenant(r), actor(r), r.PathValue("kind"), r.PathValue("period"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, name, "text/csv; charset=utf-8", b)
}

func (s *Server) disclosureAnswer(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetDisclosureAnswer(r.Context(), tenant(r), actor(r), r.PathValue("kind"), r.PathValue("period"), b.Key, b.Value)
	ok(w, v, err)
}

func (s *Server) disclosureSubmit(w http.ResponseWriter, r *http.Request) {
	var b ops.DiscSubmission
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkDisclosureSubmitted(r.Context(), tenant(r), actor(r), r.PathValue("kind"), r.PathValue("period"), b)
	ok(w, v, err)
}

func (s *Server) disclosureEvidenceUp(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, ops.MaxDisclosureEvidence+1))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	v, err := s.ops.UploadDisclosureEvidence(r.Context(), tenant(r), actor(r), r.URL.Query().Get("name"), data)
	ok(w, v, err)
}

func (s *Server) disclosureEvidence(w http.ResponseWriter, r *http.Request) {
	f, err := s.ops.DisclosureEvidence(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(f.Content)))
	sendFile(w, f.Name, http.DetectContentType(f.Content), f.Content)
}

func (s *Server) disclosureProfile(w http.ResponseWriter, r *http.Request) {
	var b ops.DisclosureProfile
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveDisclosureProfile(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) disclosureDeployment(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Deployment string `json:"deployment"`
		Provider   string `json:"provider"`
		Region     string `json:"region"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveDeployment(r.Context(), tenant(r), actor(r), b.Deployment, b.Provider, b.Region)
	ok(w, v, err)
}
