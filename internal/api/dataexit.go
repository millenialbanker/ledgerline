package api

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"ledgerline/internal/ops"
)

// dataExitRoutes: full data export and closing the account
// (internal/ops/dataexport.go, dataexit.go). Admin and the compliance
// officer; every step is in the audit trail. Not scoped: branch and
// partner logins never reach them.
func (s *Server) dataExitRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/data-exit", s.dataExit, false)
	R("POST /v1/data-exit/settings", s.dataExitSettings, true)
	R("POST /v1/data-exit/request", s.dataExitRequest, true)
	R("POST /v1/data-exit/{id}/decide", s.dataExitDecide, true)
	R("POST /v1/data-exit/{id}/cancel", s.dataExitCancel, true)
	R("POST /v1/data-exit/{id}/purge", s.dataExitPurge, true)
	R("POST /v1/data-exports", s.dataExportRequest, true)
	R("POST /v1/data-exports/{id}/decide", s.dataExportDecide, true)
	R("POST /v1/data-exports/{id}/link", s.dataExportLink, true)
	R("GET /v1/data-exports/{id}/download", s.dataExportDownload, false)
	// The certificate after the data is gone: no sign-in (the firm's users
	// are deleted), only the secret link the firm was given.
	s.mux.HandleFunc("GET /exit-certificate/{tenant}/{id}", s.exitCertificate)
	// The operator's record of exits, and a certificate issued again.
	s.mux.HandleFunc("GET /provider/exits", s.provider(s.providerExits))
	s.mux.HandleFunc("GET /provider/exits/{tenant}/{id}/certificate", s.provider(s.providerExitCertificate))
}

func (s *Server) dataExit(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.DataExit(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) dataExitSettings(w http.ResponseWriter, r *http.Request) {
	var b struct {
		CoolingOffDays int `json:"cooling_off_days"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetExitSettings(r.Context(), tenant(r), actor(r), b.CoolingOffDays)
	ok(w, v, err)
}

func (s *Server) dataExitRequest(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Confirm string `json:"confirm"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RequestExit(r.Context(), tenant(r), actor(r), b.Confirm, b.Reason)
	ok(w, v, err)
}

func (s *Server) dataExitDecide(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideExit(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Note)
	ok(w, v, err)
}

func (s *Server) dataExitCancel(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CancelExit(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Note)
	ok(w, v, err)
}

func (s *Server) dataExitPurge(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Confirm string `json:"confirm"`
	}
	if !decode(w, r, &b) {
		return
	}
	ctx := ops.WithPublicURL(r.Context(), s.publicURL(r))
	v, err := s.ops.PurgeFirm(ctx, tenant(r), actor(r), r.PathValue("id"), b.Confirm)
	ok(w, v, err)
}

func (s *Server) dataExportRequest(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RequestDataExport(r.Context(), tenant(r), actor(r), b.Reason)
	ok(w, v, err)
}

func (s *Server) dataExportDecide(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideDataExport(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Note)
	ok(w, v, err)
}

func (s *Server) dataExportLink(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.DataExportLink(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

// dataExportDownload streams the export, decrypting as it goes. A large
// export takes longer than the server's usual write timeout, so this
// response gets its own.
func (s *Server) dataExportDownload(w http.ResponseWriter, r *http.Request) {
	x, rc, err := s.ops.OpenDataExport(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.URL.Query().Get("t"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	defer rc.Close()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(12 * time.Hour))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.FormatInt(x.Size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="ledgerline-`+tenant(r)+`-`+x.ID+`.zip"`)
	if _, err := io.Copy(w, rc); err != nil {
		s.log.Error("data export download cut off", "tenant", tenant(r), "export", x.ID, "err", err)
	}
}

func (s *Server) exitCertificate(w http.ResponseWriter, r *http.Request) {
	t, id := r.PathValue("tenant"), r.PathValue("id")
	b, err := s.ops.ExitCertificate(r.Context(), t, id, r.URL.Query().Get("t"))
	if err != nil {
		writeErr(w, 404, "not_found", "No such certificate.", nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	sendFile(w, "destruction-certificate-"+id+".pdf", "application/pdf", b)
}

func (s *Server) providerExits(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ExitRecords(r.Context())
	ok(w, v, err)
}

func (s *Server) providerExitCertificate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := s.ops.OperatorCertificate(r.Context(), r.PathValue("tenant"), id)
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, "destruction-certificate-"+id+".pdf", "application/pdf", b)
}
