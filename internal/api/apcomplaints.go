package api

import (
	"io"
	"net/http"
	"strconv"

	"ledgerline/internal/ops"
)

// apComplaintsRoutes: the weekly report of clients mapped to authorised
// persons and the monthly AP count reconciliation (Branches & partners),
// the complaints tables of each business, a complaint's business, and the
// impersonation register with its monthly reports (Investor complaints).
// Registered from exchangeReportingRoutes.
func (s *Server) apComplaintsRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/ap-mapping", s.apMapping, false)
	R("GET /v1/ap-mapping/file", s.apMappingFile, false)
	R("POST /v1/ap-mapping/uploads", s.apMappingUploaded, true)
	R("POST /v1/ap-mapping/taken-as-reported", s.apMappingTaken, true)
	R("GET /v1/ap-mapping/history/{ucc}", s.apMappingHistory, false)
	R("GET /v1/ap-mapping/contacts", s.apContacts, false)
	R("GET /v1/ap-mapping/counts", s.apCounts, false)
	R("POST /v1/ap-mapping/counts", s.apCountsSave, true)
	R("GET /v1/complaints/disclosure/file", s.disclosureFile, false)
	R("POST /v1/tickets/{id}/tag", s.tagTicket, true)
	R("GET /v1/impersonation", s.impCases, false)
	R("POST /v1/impersonation", s.impSave, true)
	R("POST /v1/impersonation/{id}/evidence", s.impEvidenceUp, true)
	R("GET /v1/impersonation/evidence/{file}", s.impEvidence, false)
	R("GET /v1/impersonation/reports", s.impReports, false)
	R("GET /v1/impersonation/reports/file", s.impReportFile, false)
	R("POST /v1/impersonation/reports", s.impReportMark, true)
	R("GET /v1/raia/settings", s.raiaSettings, false)
	R("PUT /v1/raia/settings", s.raiaSettingsSave, true)
}

func (s *Server) apMapping(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.APMappingReport(r.Context(), tenant(r), actor(r), q.Get("week"), q.Get("date"))
	ok(w, v, err)
}

// apMappingFile is an exchange's file for the week (?week=&exchange=).
func (s *Server) apMappingFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name, b, err := s.ops.APMappingFile(r.Context(), tenant(r), actor(r), q.Get("week"), q.Get("exchange"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, name, "text/csv; charset=utf-8", b)
}

func (s *Server) apMappingUploaded(w http.ResponseWriter, r *http.Request) {
	var b ops.APUploadReq
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkAPMappingUploaded(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) apMappingTaken(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Exchange string `json:"exchange"`
		Note     string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.TakeAPMappingAsReported(r.Context(), tenant(r), actor(r), b.Exchange, b.Note)
	ok(w, map[string]int{"clients": n}, err)
}

func (s *Server) apMappingHistory(w http.ResponseWriter, r *http.Request) {
	if !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	v, err := s.ops.ClientAPHistory(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v, err)
}

func (s *Server) apContacts(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.APContactCheck(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) apCounts(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.APCountReconFor(r.Context(), tenant(r), actor(r), r.URL.Query().Get("month"))
	ok(w, v, err)
}

func (s *Server) apCountsSave(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Month    string             `json:"month"`
		Exchange string             `json:"exchange"`
		Lines    []ops.APCountInput `json:"lines"`
		Note     string             `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveAPCountRecon(r.Context(), tenant(r), actor(r), b.Month, b.Exchange, b.Lines, b.Note)
	ok(w, v, err)
}

// disclosureFile is a business's complaints table for the month, as a CSV
// or (format=html) the section to put on the website.
func (s *Server) disclosureFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d, err := s.ops.ComplaintsDisclosureFor(r.Context(), tenant(r), q.Get("business"), q.Get("month"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	name := "ComplaintsData_" + d.Business + "_" + d.Month
	if q.Get("format") == "html" {
		set, _ := s.ops.Settings(r.Context(), tenant(r))
		sendFile(w, name+".html", "text/html; charset=utf-8", d.HTML(set.Firm))
		return
	}
	sendFile(w, name+".csv", "text/csv; charset=utf-8", d.CSV())
}

func (s *Server) tagTicket(w http.ResponseWriter, r *http.Request) {
	var b ops.TicketTag
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.TagTicket(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) impCases(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ImpCases(r.Context(), tenant(r), actor(r))
	ok(w, map[string]any{"cases": v, "where": ops.ImpWhere, "found_by": ops.ImpFoundBy, "takedown_statuses": ops.ImpTakedownStatuses, "alert_by": ops.ImpAlertBy}, err)
}

func (s *Server) impSave(w http.ResponseWriter, r *http.Request) {
	var b ops.ImpCase
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveImpCase(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) impEvidenceUp(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, ops.MaxImpEvidence+1))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	v, err := s.ops.UploadImpEvidence(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.URL.Query().Get("name"), data)
	ok(w, v, err)
}

func (s *Server) impEvidence(w http.ResponseWriter, r *http.Request) {
	f, err := s.ops.ImpEvidence(r.Context(), tenant(r), actor(r), r.PathValue("file"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(f.Content)))
	sendFile(w, f.Name, http.DetectContentType(f.Content), f.Content)
}

func (s *Server) impReports(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ImpReports(r.Context(), tenant(r), actor(r), r.URL.Query().Get("month"))
	ok(w, v, err)
}

func (s *Server) impReportFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name, b, err := s.ops.ImpReportFile(r.Context(), tenant(r), actor(r), q.Get("body"), q.Get("month"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, name, "text/csv; charset=utf-8", b)
}

func (s *Server) impReportMark(w http.ResponseWriter, r *http.Request) {
	var b ops.ImpReportReq
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkImpReport(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) raiaSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RAIASettingsFor(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) raiaSettingsSave(w http.ResponseWriter, r *http.Request) {
	var b ops.RAIASettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveRAIASettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}
