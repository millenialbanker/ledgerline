package api

import (
	"net/http"
	neturl "net/url"

	"ledgerline/internal/ops"
)

// fatcaRoutes: clients' FATCA/CRS self-certifications on KYC & PMLA, the
// KRA files that carry them (the legacy bulk file, the controlling persons
// file, marking a file uploaded) and the warnings the KYC desk resolves.
// Changes go through POST /v1/changes (kind FATCA/CRS). Registered from
// exchangeReportingRoutes.
func (s *Server) fatcaRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/fatca", s.fatcaView, false)
	R("GET /v1/fatca/codes", s.fatcaCodes, false)
	R("POST /v1/fatca/{ucc}/resolve", s.fatcaResolve, true)
	R("GET /v1/compliance/fatca-legacy", s.fatcaLegacyFile, false)
	R("GET /v1/compliance/kra-batches/{id}/controlling-persons", s.kraControllingPersons, false)
	R("POST /v1/compliance/kra-batches/{id}/uploaded", s.kraBatchUploaded, true)
}

func (s *Server) fatcaView(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r, "Viewing FATCA/CRS self-certifications", ops.RoleMaker, ops.RoleChecker, ops.RoleCompliance) {
		return
	}
	date, okd := dateParam(w, r.URL.Query().Get("date"), "date", false)
	if !okd {
		return
	}
	v, err := s.ops.FATCA(r.Context(), tenant(r), date)
	ok(w, v, err)
}

func (s *Server) fatcaCodes(w http.ResponseWriter, r *http.Request) {
	ok(w, ops.FATCACodeLists(), nil)
}

func (s *Server) fatcaResolve(w http.ResponseWriter, r *http.Request) {
	var b struct{ Code, Note string }
	if !decode(w, r, &b) {
		return
	}
	c, err := s.ops.ResolveFATCAWarning(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Code, b.Note)
	ok(w, c.Public(), err)
}

// sendKRAFile sends a KRA file, with what the person should know in X-Note.
func sendKRAFile(w http.ResponseWriter, f ops.KRAFile) {
	if f.Note != "" {
		w.Header().Set("X-Note", neturl.PathEscape(f.Note))
	}
	if f.Batch.ID != "" {
		w.Header().Set("X-Batch", f.Batch.ID)
	}
	sendFile(w, f.Name, "text/csv; charset=utf-8", f.Data)
}

func (s *Server) fatcaLegacyFile(w http.ResponseWriter, r *http.Request) {
	f, err := s.ops.FATCALegacyFile(r.Context(), tenant(r), actor(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendKRAFile(w, f)
}

func (s *Server) kraControllingPersons(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, n, err := s.ops.KRAControllingPersonsFile(r.Context(), tenant(r), actor(r), id)
	if err != nil {
		fail(w, err, nil)
		return
	}
	if n == 0 {
		w.Header().Set("X-Note", neturl.PathEscape("no client in this file named controlling persons"))
	}
	sendFile(w, "kra_fatca_controlling_persons_"+id+".csv", "text/csv; charset=utf-8", b)
}

func (s *Server) kraBatchUploaded(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date, Ref string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkKRABatchUploaded(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.Ref)
	ok(w, v, err)
}
