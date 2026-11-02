package api

import (
	"net/http"
	"sync"

	"ledgerline/internal/ops"
)

var exchangeAlertsRouted sync.Map // *Server -> registered

// exchangeAlertRoutes: missing daily file alerts (internal/ops/filealerts.go)
// the clients BSE does not permit to trade (bseblocked.go) and the
// order-to-trade ratio alerts (otr.go).
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
	s.route("GET /v1/bse-ucc-blocked", s.bseBlockedList, false)
	s.route("GET /v1/bse-ucc-blocked/{ucc}", s.bseBlockedOne, false)
	s.route("GET /v1/otr", s.otrReport, false)
	s.route("GET /v1/otr/settings", s.otrSettingsGet, false)
	s.route("PUT /v1/otr/settings", s.otrSettingsPut, true)
}

func (s *Server) otrReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.OTRReport(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
	ok(w, v, err)
}

func (s *Server) otrSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.ops.OTRSettings(r.Context(), tenant(r)))
}

func (s *Server) otrSettingsPut(w http.ResponseWriter, r *http.Request) {
	var b ops.OTRSettings
	if !decode(w, r, &b) {
		return
	}
	b.UpdatedBy, b.UpdatedAt = "", ""
	v, err := s.ops.SaveOTRSettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) bseBlockedList(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BSEBlockedList(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) bseBlockedOne(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BSEBlockedFor(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v, err)
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
