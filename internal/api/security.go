package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"ledgerline/internal/ops"
)

var securityRouted sync.Map // *Server -> registered

// securityRoutes: security incidents, the CERT-In report step, security
// events and the log export to the firm's SIEM (internal/ops/secevents.go,
// secincidents.go, seclogexport.go), and the provider's platform incidents.
func (s *Server) securityRoutes() {
	if s.ops == nil {
		return
	}
	if _, dup := securityRouted.LoadOrStore(s, true); dup {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/security/incidents", s.secIncidents, false)
	R("POST /v1/security/incidents", s.secRecord, true)
	R("GET /v1/security/incidents/{id}", s.secIncident, false)
	R("POST /v1/security/incidents/{id}/status", s.secStatus, true)
	R("POST /v1/security/incidents/{id}/report", s.secReport, true)
	R("GET /v1/security/events", s.secEvents, false)
	R("GET /v1/security/settings", s.secSettingsGet, false)
	R("PUT /v1/security/settings", s.secSettingsPut, true)
	R("GET /v1/security/log-export", s.secExport, false)
	R("POST /v1/security/log-export/{dest}/test", s.secExportTest, true)
	R("GET /v1/security/logs/download", s.secDownload, false)
	// The provider declares incidents of the platform to the firms.
	s.mux.HandleFunc("GET /provider/incidents", s.provider(s.provIncidents))
	s.mux.HandleFunc("POST /provider/incidents", s.provider(s.provDeclare))
	s.mux.HandleFunc("POST /provider/incidents/{id}", s.provider(s.provUpdate))
}

func (s *Server) secIncidents(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Incidents(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) secIncident(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.IncidentByID(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) secRecord(w http.ResponseWriter, r *http.Request) {
	var b ops.RecordIncidentInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordIncident(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) secStatus(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Status string `json:"status"`
		Note   string `json:"note"`
		Own    bool   `json:"own"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.UpdateIncident(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Status, b.Note, b.Own)
	ok(w, v, err)
}

func (s *Server) secReport(w http.ResponseWriter, r *http.Request) {
	var b struct {
		To        string `json:"to"`
		At        string `json:"at"`
		How       string `json:"how"`
		Reference string `json:"reference"`
		Note      string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReportIncident(r.Context(), tenant(r), actor(r), r.PathValue("id"), ops.IncidentReport{To: b.To, At: b.At, How: b.How, Reference: b.Reference, Note: b.Note})
	ok(w, v, err)
}

func (s *Server) secEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	v, err := s.ops.SecEvents(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"), q.Get("kind"), limit)
	ok(w, v, err)
}

func (s *Server) secSettingsGet(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r, "Seeing security events and incidents", ops.RoleCompliance) {
		return
	}
	writeJSON(w, 200, s.ops.SecuritySettings(r.Context(), tenant(r)))
}

func (s *Server) secSettingsPut(w http.ResponseWriter, r *http.Request) {
	var b ops.SecuritySettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveSecuritySettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) secExport(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.LogExport(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) secExportTest(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.TestLogExport(r.Context(), tenant(r), actor(r), r.PathValue("dest"))
	ok(w, v, err)
}

func (s *Server) secDownload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	b, name, err := s.ops.LogDownload(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"), q.Get("logs") == "events")
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, name, "application/gzip", b)
}

// ---- the provider's platform incidents ----

func providerActor(r *http.Request) string {
	a := strings.TrimSpace(r.Header.Get("X-Actor"))
	if a == "" || len(a) > 80 {
		return "provider"
	}
	return a
}

func (s *Server) provIncidents(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PlatformIncidents(r.Context())
	ok(w, v, err)
}

func (s *Server) provDeclare(w http.ResponseWriter, r *http.Request) {
	var b ops.PlatformIncidentInput
	if !decode(w, r, &b) {
		return
	}
	ts := s.Tenants()
	sort.Strings(ts)
	v, err := s.ops.DeclarePlatformIncident(r.Context(), ts, providerActor(r), b)
	ok(w, v, err)
}

func (s *Server) provUpdate(w http.ResponseWriter, r *http.Request) {
	var b ops.PlatformIncidentInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.UpdatePlatformIncident(r.Context(), providerActor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

// countingWriter counts the bytes of a response, so a large download can
// be recorded as a security event (ops.NoteDownload).
type countingWriter struct {
	http.ResponseWriter
	n int64
}

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	c.n += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the real writer.
func (c *countingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
