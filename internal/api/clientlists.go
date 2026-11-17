package api

import (
	"io"
	"net/http"
	"regexp"

	"ledgerline/internal/ops"
)

var exchangeName = regexp.MustCompile(`^[A-Za-z]{2,6}$`)

// clientListRoutes: the lists checked against the clients. DoT's
// revocation list of mobile numbers (NSE's report, DoT's MNRL, NSDL's
// e-PASS matches and reactivation data; Clients & onboarding → Revoked
// mobiles) and the KRAs' list of deceased clients (Compliance → Deceased
// clients). Registered from exchangeReportingRoutes.
func (s *Server) clientListRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/mnrl", s.mnrlView, false)
	R("POST /v1/mnrl/lists", s.mnrlLoad, true)
	R("POST /v1/mnrl/{id}/status", s.mnrlStatus, true)
	R("GET /v1/deceased", s.deceasedView, false)
	R("POST /v1/deceased/lists", s.deceasedLoad, true)
	R("POST /v1/deceased/cutoff", s.deceasedCutoff, true)
	R("GET /v1/deceased/ucc-list", s.deceasedUCCList, false)
	R("POST /v1/deceased/{id}/steps", s.deceasedStep, true)
}

func (s *Server) mnrlView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MNRL(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

// listBody reads an uploaded list (the file's bytes as the body).
func listBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	data, err := io.ReadAll(io.LimitReader(r.Body, ops.MaxXFileBytes+1))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return nil, false
	}
	return data, true
}

// mnrlLoad loads a revocation list: ?format= (nse-mnrl, dot-mnrl,
// nsdl-mnrl, nsdl-mnrl-reactivation)&name=&date=.
func (s *Server) mnrlLoad(w http.ResponseWriter, r *http.Request) {
	data, okb := listBody(w, r)
	if !okb {
		return
	}
	q := r.URL.Query()
	v, err := s.ops.LoadMNRLList(r.Context(), tenant(r), actor(r), ops.MNRLLoadReq{Format: q.Get("format"), Name: q.Get("name"), Date: q.Get("date")}, data)
	ok(w, v, err)
}

func (s *Server) mnrlStatus(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetMNRLStatus(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Status, b.Note)
	ok(w, v, err)
}

func (s *Server) deceasedView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Demise(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

// deceasedLoad loads <TMcode>_Demise_Clients_by_KRA:
// ?name=&date=&period=YYYY-MM&cutoff=YYYY-MM-DD&circular=.
func (s *Server) deceasedLoad(w http.ResponseWriter, r *http.Request) {
	data, okb := listBody(w, r)
	if !okb {
		return
	}
	q := r.URL.Query()
	v, err := s.ops.LoadDemiseList(r.Context(), tenant(r), actor(r), ops.DemiseLoadReq{Name: q.Get("name"), Date: q.Get("date"), Period: q.Get("period"), Cutoff: q.Get("cutoff"), Circular: q.Get("circular")}, data)
	ok(w, v, err)
}

func (s *Server) deceasedCutoff(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Period   string `json:"period"`
		Cutoff   string `json:"cutoff"`
		Circular string `json:"circular"`
	}
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.SetDemiseCutoff(r.Context(), tenant(r), actor(r), b.Period, b.Cutoff, b.Circular)
	ok(w, map[string]int{"cases": n}, err)
}

func (s *Server) deceasedStep(w http.ResponseWriter, r *http.Request) {
	var b ops.DemiseActionReq
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DemiseAction(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

// deceasedUCCList is the CSV of UCCs to close on an exchange's portal
// (?exchange=; empty: every exchange).
func (s *Server) deceasedUCCList(w http.ResponseWriter, r *http.Request) {
	ex := r.URL.Query().Get("exchange")
	b, err := s.ops.DemiseUCCList(r.Context(), tenant(r), actor(r), ex)
	if err != nil {
		fail(w, err, nil)
		return
	}
	name := "deceased-uccs-to-close.csv"
	if exchangeName.MatchString(ex) {
		name = "deceased-uccs-to-close-" + ex + ".csv"
	}
	sendFile(w, name, "text/csv; charset=utf-8", b)
}
