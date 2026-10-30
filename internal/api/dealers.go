package api

import (
	"io"
	"net/http"
	"strings"
	"sync"

	"ledgerline/internal/ops"
)

var dealerRouted sync.Map // *Server -> registered

// dealerRoutes: the dealers and terminals register, its report and
// inspection list, and BSE's dealer upload files (internal/ops/dealers.go).
func (s *Server) dealerRoutes() {
	if s.ops == nil {
		return
	}
	if _, dup := dealerRouted.LoadOrStore(s, true); dup {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/dealers", s.dlList, false)
	R("POST /v1/dealers", s.dlCreate, true)
	R("GET /v1/dealers/export.csv", s.dlExport, false)
	R("GET /v1/dealers/inspection.csv", s.dlInspection, false)
	R("GET /v1/dealers/report", s.dlReport, false)
	R("POST /v1/dealers/import", s.dlImport, true)
	R("GET /v1/dealers/files/{kind}", s.dlFile, false)
	R("GET /v1/dealers/{id}", s.dlGet, false)
	R("PUT /v1/dealers/{id}", s.dlPut, true)
	R("POST /v1/dealers/{id}/left", s.dlLeft, true)
}

func (s *Server) dlList(w http.ResponseWriter, r *http.Request) {
	d := r.URL.Query().Get("date")
	if d != "" {
		var okd bool
		if d, okd = qdate(w, r); !okd {
			return
		}
	}
	v, err := s.ops.Dealers(r.Context(), tenant(r), actor(r), d)
	ok(w, v, err)
}

func (s *Server) dlCreate(w http.ResponseWriter, r *http.Request) {
	var b ops.Dealer
	if !decode(w, r, &b) {
		return
	}
	if strings.TrimSpace(b.ID) != "" {
		writeErr(w, 422, "invalid", "A new dealer has no ID; change an existing one with PUT /v1/dealers/{id}.", nil)
		return
	}
	v, err := s.ops.SaveDealer(r.Context(), tenant(r), actor(r), b)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) dlGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.GetDealer(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) dlPut(w http.ResponseWriter, r *http.Request) {
	var b ops.Dealer
	if !decode(w, r, &b) {
		return
	}
	b.ID = r.PathValue("id")
	v, err := s.ops.SaveDealer(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) dlLeft(w http.ResponseWriter, r *http.Request) {
	var b ops.LeftRequest
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkDealerLeft(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) dlImport(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
	if err != nil {
		writeErr(w, 413, "too_large", "The dealer file is larger than 5 MB.", nil)
		return
	}
	if len(data) == 0 {
		writeErr(w, 400, "empty_file", "Send the CSV file as the request body.", nil)
		return
	}
	res, err := s.ops.ImportDealers(r.Context(), tenant(r), actor(r), data)
	if err != nil {
		fail(w, err, res)
		return
	}
	writeJSON(w, 200, res)
}

func sendFile(w http.ResponseWriter, name, ct string, b []byte) {
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.NewReplacer(`"`, "", "\\", "").Replace(name)+`"`)
	w.Write(b)
}

func (s *Server) dlExport(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.ExportDealersCSV(r.Context(), tenant(r), actor(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, "dealers.csv", "text/csv; charset=utf-8", b)
}

func (s *Server) dlInspection(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.InspectionCSV(r.Context(), tenant(r), actor(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, "terminals_and_users.csv", "text/csv; charset=utf-8", b)
}

func (s *Server) dlReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.DealerBusiness(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
	ok(w, v, err)
}

// dlFile writes BSE's dealer registration, NISM or dealer PAN file for the
// dealers in ?ids= (comma-separated; all when absent). ?check=1 lists what
// the file would hold and any problems instead; ?action= (A, M or RN)
// overrides the NISM file's action.
func (s *Server) dlFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var ids []string
	for _, x := range strings.Split(q.Get("ids"), ",") {
		if x = strings.TrimSpace(x); x != "" {
			ids = append(ids, x)
		}
	}
	check := q.Get("check") == "1" || q.Get("check") == "true"
	f, err := s.ops.MakeDealerFile(r.Context(), tenant(r), actor(r), r.PathValue("kind"), ids, q.Get("action"), check)
	if err != nil {
		fail(w, err, map[string]any{"problems": f.Problems})
		return
	}
	if check {
		writeJSON(w, 200, f)
		return
	}
	sendFile(w, f.Name, "text/plain; charset=utf-8", f.Content)
}
