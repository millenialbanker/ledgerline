package api

import (
	"io"
	"net/http"
	"strconv"

	"ledgerline/internal/ops"
)

// inspackRoutes: inspection packs (internal/ops/inspack.go). Compliance
// officer and Admin only; every make and download is in the audit trail.
// Not scoped: branch and partner logins never reach them.
func (s *Server) inspackRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/inspection-packs", s.inspacks, false)
	R("POST /v1/inspection-packs", s.makeInspack, true)
	R("POST /v1/inspection-packs/sample", s.inspackSample, true)
	R("POST /v1/inspection-packs/readiness", s.inspackReadiness, true)
	R("PUT /v1/inspection-packs/settings", s.inspackSettings, true)
	R("GET /v1/inspection-packs/{id}", s.inspack, false)
	R("GET /v1/inspection-packs/{id}/download", s.inspackDownload, false)
	R("GET /v1/inspection-packs/{id}/index", s.inspackIndex, false)
	R("POST /v1/inspection-packs/{id}/masked-copy", s.inspackMasked, true)
	R("POST /v1/inspection-packs/{id}/expire", s.inspackExpire, true)
	R("POST /v1/inspection-templates", s.inspackSaveTemplate, true)
	R("POST /v1/inspection-templates/{id}/delete", s.inspackDeleteTemplate, true)
	R("POST /v1/inspection-docs", s.inspackDocUp, true)
	R("GET /v1/inspection-docs/{id}", s.inspackDoc, false)
	R("POST /v1/inspection-docs/{id}/retire", s.inspackDocRetire, true)
}

func (s *Server) inspacks(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.InspectionPacks(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) inspack(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.InspectionPack(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) makeInspack(w http.ResponseWriter, r *http.Request) {
	var b ops.PackRequest
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MakePack(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) inspackSample(w http.ResponseWriter, r *http.Request) {
	var b struct {
		To     string            `json:"to"`
		Sample ops.PackSampleReq `json:"sample"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PackSamplePreview(r.Context(), tenant(r), actor(r), b.To, b.Sample)
	ok(w, v, err)
}

func (s *Server) inspackReadiness(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Items []ops.PackItem `json:"items"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PackReadiness(r.Context(), tenant(r), actor(r), b.Items)
	ok(w, v, err)
}

func (s *Server) inspackSettings(w http.ResponseWriter, r *http.Request) {
	var b ops.PackSettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SavePackSettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) inspackDownload(w http.ResponseWriter, r *http.Request) {
	p, data, err := s.ops.PackFile(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	sendFile(w, p.FileName, "application/zip", data)
}

func (s *Server) inspackIndex(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.PackIndex(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'self'")
	w.Write(b)
}

func (s *Server) inspackMasked(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MaskedPackCopy(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) inspackExpire(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Why string `json:"why"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ExpirePack(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Why)
	ok(w, v, err)
}

func (s *Server) inspackSaveTemplate(w http.ResponseWriter, r *http.Request) {
	var b ops.PackTemplate
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SavePackTemplate(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) inspackDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	err := s.ops.DeletePackTemplate(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]bool{"deleted": err == nil}, err)
}

func (s *Server) inspackDocUp(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, ops.MaxPackDoc+1))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	q := r.URL.Query()
	v, err := s.ops.AddPackDoc(r.Context(), tenant(r), actor(r), q.Get("kind"), q.Get("title"), q.Get("version"), q.Get("name"), data)
	ok(w, v, err)
}

func (s *Server) inspackDoc(w http.ResponseWriter, r *http.Request) {
	d, data, err := s.ops.PackDocFile(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	sendFile(w, d.Name, d.ContentType, data)
}

func (s *Server) inspackDocRetire(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Why string `json:"why"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RetirePackDoc(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Why)
	ok(w, v, err)
}
