package api

import (
	"net/http"
	"sync"

	"ledgerline/internal/ops"
)

var exchangeAlertsRouted sync.Map // *Server -> registered

// exchangeAlertRoutes: missing daily file alerts (internal/ops/filealerts.go).
func (s *Server) exchangeAlertRoutes() {
	if s.ops == nil {
		return
	}
	if _, dup := exchangeAlertsRouted.LoadOrStore(s, true); dup {
		return
	}
	s.route("GET /v1/exchange-files-alerts", s.fileAlertsGet, false)
	s.route("PUT /v1/exchange-files-alerts", s.fileAlertsPut, true)
	s.route("POST /v1/exchange-files-alerts/run", s.fileAlertsRun, true)
}

func (s *Server) fileAlertsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.ops.FileAlertSettings(r.Context(), tenant(r)))
}

func (s *Server) fileAlertsPut(w http.ResponseWriter, r *http.Request) {
	var b ops.FileAlertSettings
	if !decode(w, r, &b) {
		return
	}
	b.UpdatedBy, b.UpdatedAt = "", ""
	v, err := s.ops.SaveFileAlertSettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) fileAlertsRun(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CheckMissingFilesNow(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}
