package api

import (
	"io"
	"net/http"

	"ledgerline/internal/ops"
)

// sanctionsRoutes: KYC & PMLA → Sanctions (internal/ops/sanctions.go): the
// UNSC, UAPA and WMD lists and their versions, screening all clients,
// sanctions cases with the 24-hour clock and the report register, the
// settings, FATF public statements and the EDD they call for. Everything
// here is for Compliance (a case is confidential like an STR). Registered
// from exchangeReportingRoutes.
func (s *Server) sanctionsRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/sanctions", s.sanctionsView, false)
	R("POST /v1/sanctions/lists", s.sanctionsLoad, true)
	R("POST /v1/sanctions/screen", s.sanctionsScreen, true)
	R("POST /v1/sanctions/cases/{id}", s.sanctionsAct, true)
	R("PUT /v1/sanctions/config", s.sanctionsConfig, true)
	R("POST /v1/sanctions/fatf", s.sanctionsFATF, true)
	R("POST /v1/sanctions/edd/{id}", s.sanctionsEDD, true)
}

func (s *Server) sanctionsView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Sanctions(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

// sanctionsLoad takes the list file as the body: ?list=UNSC|UAPA|WMD|SEBI|
// Exchange|Defaulters (the UN's XML is read as UNSC when no list is given),
// &version= the list's own date or version, &name= the file name.
func (s *Server) sanctionsLoad(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFileBytes))
	if err != nil {
		writeErr(w, 413, "too_large", "File exceeds the 64 MB upload limit.", nil)
		return
	}
	if len(data) == 0 {
		writeErr(w, 400, "empty_file", "The request body is empty; send the list file as the body.", nil)
		return
	}
	q := r.URL.Query()
	v, err := s.ops.LoadSanctionsList(r.Context(), tenant(r), actor(r), q.Get("list"), q.Get("version"), q.Get("name"), data)
	ok(w, v, err)
}

func (s *Server) sanctionsScreen(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RunSanctionsScreening(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) sanctionsAct(w http.ResponseWriter, r *http.Request) {
	var b ops.SanctionsAction
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ActOnSanctionsCase(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) sanctionsConfig(w http.ResponseWriter, r *http.Request) {
	var b ops.SanctionsConfig
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveSanctionsConfig(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) sanctionsFATF(w http.ResponseWriter, r *http.Request) {
	var b ops.FATFStatement
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordFATFStatement(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) sanctionsEDD(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Note     string `json:"note"`
		HighRisk bool   `json:"high_risk"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordFATFEDD(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Note, b.HighRisk)
	ok(w, v, err)
}
