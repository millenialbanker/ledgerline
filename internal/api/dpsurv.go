package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// Surveillance case work (internal/ops/survcase.go, survimport.go,
// dpsurv.go; web/dpsurv.js): an alert's checklist, documents and adverse
// observation, CDSL's and MCX's alert files with the download log, the
// compliance officer's sample of closures, the thresholds of the DP's own
// rules, and the groups of connected clients. Registered from opsRoutes.
func (s *Server) dpSurvRoutes(R func(string, http.HandlerFunc, bool)) {
	R("PUT /v1/surveillance/alerts/{id}/checklist", s.survChecklist, true)
	R("POST /v1/surveillance/alerts/{id}/docs", s.survDocAdd, true)
	R("GET /v1/surveillance/alerts/{id}/docs/{doc}", s.survDocGet, false)
	R("POST /v1/surveillance/alerts/{id}/adverse", s.survAdverse, true)
	R("POST /v1/surveillance/alerts/{id}/adverse-reported", s.survAdverseReported, true)
	R("POST /v1/surveillance/alerts/{id}/sample", s.survSampleRecord, true)
	R("GET /v1/surveillance/sample", s.survSample, false)
	R("POST /v1/surveillance/import/{kind}", s.survImport, true)
	R("POST /v1/surveillance/downloads/{kind}/nil", s.survNilDownload, true)
	R("GET /v1/surveillance/downloads", s.survDownloads, false)
	R("GET /v1/surveillance/thresholds", s.survThresholds, false)
	R("PUT /v1/surveillance/thresholds", s.survThresholdsPut, true)
	R("GET /v1/surveillance/connected", s.survConnected, false)
}

func (s *Server) survChecklist(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Items []ops.AlertCheck `json:"items"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveAlertChecklist(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Items)
	ok(w, v, err)
}

func (s *Server) survDocAdd(w http.ResponseWriter, r *http.Request) {
	data, okb := readBody(w, r, ops.MaxDocBytes+1)
	if !okb {
		return
	}
	v, err := s.ops.AddAlertDoc(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.URL.Query().Get("name"), data)
	ok(w, v, err)
}

func (s *Server) survDocGet(w http.ResponseWriter, r *http.Request) {
	d, b, err := s.ops.AlertDocFile(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("doc"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, d.Name, d.ContentType, b)
}

func (s *Server) survAdverse(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Observation string `json:"observation"`
		Late        string `json:"late"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkAdverse(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Observation, b.Late)
	ok(w, v, err)
}

func (s *Server) survAdverseReported(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
		Ref  string `json:"ref"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordAdverseReported(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.Ref)
	ok(w, v, err)
}

func (s *Server) survSample(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.AlertSample(r.Context(), tenant(r), actor(r), r.URL.Query().Get("quarter"))
	ok(w, v, err)
}

func (s *Server) survSampleRecord(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Quarter string `json:"quarter"`
		Result  string `json:"result"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordAlertSample(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Quarter, b.Result, b.Note)
	ok(w, v, err)
}

func (s *Server) survImport(w http.ResponseWriter, r *http.Request) {
	data, okb := readBody(w, r, 32<<20)
	if !okb {
		return
	}
	v, err := s.ops.ImportSurvAlerts(r.Context(), tenant(r), actor(r), r.PathValue("kind"), r.URL.Query().Get("name"), data)
	ok(w, v, err)
}

func (s *Server) survNilDownload(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordNilDownload(r.Context(), tenant(r), actor(r), r.PathValue("kind"), b.Note)
	ok(w, v, err)
}

func (s *Server) survDownloads(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SurvDownloads(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) survThresholds(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r, "Seeing surveillance thresholds", ops.RoleCompliance, ops.RoleChecker) {
		return
	}
	ok(w, s.ops.DPSurvConfig(r.Context(), tenant(r)), nil)
}

func (s *Server) survThresholdsPut(w http.ResponseWriter, r *http.Request) {
	var b ops.DPSurvSettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveDPSurvSettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) survConnected(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ConnectedGroups(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}
