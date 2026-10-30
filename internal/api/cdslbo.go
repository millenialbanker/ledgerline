package api

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"

	"ledgerline/internal/ops"
)

var cdslBORouted sync.Map // *Server -> registered

// cdslBORoutes: CDSL's APIs for the demat account lifecycle (account
// opening, modification and BO setup uploads, active demat count, transfer
// cum closure, add beneficiary; internal/ops/cdslbo.go, cdslcount.go,
// cdslredirect.go), and the addresses CDSL and the client's browser come
// back to, which are not signed in: each carries the request's token.
func (s *Server) cdslBORoutes() {
	if s.ops == nil {
		return
	}
	if _, dup := cdslBORouted.LoadOrStore(s, true); dup {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/cdsl/requests", s.cdslList, false)
	R("POST /v1/cdsl/requests", s.cdslCreate, true)
	R("POST /v1/cdsl/uploads", s.cdslUpload, true)
	R("GET /v1/cdsl/requests/{id}", s.cdslGet, false)
	R("PUT /v1/cdsl/requests/{id}/fields", s.cdslFields, true)
	R("POST /v1/cdsl/requests/{id}/file", s.cdslAttach, true)
	R("GET /v1/cdsl/requests/{id}/file/{which}", s.cdslFile, false)
	R("POST /v1/cdsl/requests/{id}/action", s.cdslAction, true)
	R("GET /v1/cdsl/bo-defaults", s.cdslDefaults, false)
	R("PUT /v1/cdsl/bo-defaults", s.cdslDefaultsPut, true)
	R("GET /v1/cdsl/demat-check/{ucc}", s.cdslCount, false)
	R("POST /v1/cdsl/demat-check/{ucc}", s.cdslCountRun, true)
	// BO upload files (cdslbofile.go): a client's CDSL details, deletion of
	// a deceased holder's name, and the signature and closure files.
	R("GET /v1/cdsl/bo-details/{ucc}", s.cdslDetailGet, false)
	R("PUT /v1/cdsl/bo-details/{ucc}", s.cdslDetailPut, true)
	R("POST /v1/cdsl/delete-holder", s.cdslDeleteHolder, true)
	R("GET /v1/cdsl/files/signature/{ucc}", s.cdslSigFile, false)
	R("GET /v1/cdsl/files/closure/{id}", s.cdslClosureFile, false)
	s.mux.HandleFunc("POST /hooks/{tenant}/cdsl/bo/{token}", s.cdslBOHook)
	s.mux.HandleFunc("GET /hooks/{tenant}/cdsl/go/{token}", s.cdslGo)
	s.mux.HandleFunc("POST /hooks/{tenant}/cdsl/return/{token}", s.cdslReturn)
}

func (s *Server) cdslList(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CDSLRequests(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) cdslGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CDSLRequestGet(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) cdslCreate(w http.ResponseWriter, r *http.Request) {
	var b ops.CDSLNew
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.NewCDSLRequest(r.Context(), tenant(r), actor(r), b)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) cdslFields(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Fields map[string]string `json:"fields"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetCDSLFields(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Fields)
	ok(w, v, err)
}

// cdslUploadBody reads an upload file sent as the request body (?name= gives
// its file name).
func cdslUploadBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 25<<20))
	if err != nil {
		writeErr(w, 413, "too_large", "The file is larger than 25 MB.", nil)
		return nil, false
	}
	if len(data) == 0 {
		writeErr(w, 400, "empty_file", "Send the file as the request body, with ?name= its file name.", nil)
		return nil, false
	}
	return data, true
}

func (s *Server) cdslUpload(w http.ResponseWriter, r *http.Request) {
	data, okb := cdslUploadBody(w, r)
	if !okb {
		return
	}
	v, err := s.ops.NewCDSLUpload(r.Context(), tenant(r), actor(r), r.URL.Query().Get("name"), data)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) cdslAttach(w http.ResponseWriter, r *http.Request) {
	data, okb := cdslUploadBody(w, r)
	if !okb {
		return
	}
	v, err := s.ops.AttachCDSLFile(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.URL.Query().Get("name"), data)
	ok(w, v, err)
}

func (s *Server) cdslFile(w http.ResponseWriter, r *http.Request) {
	name, b, err := s.ops.CDSLRequestFile(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("which"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, name, "application/octet-stream", b)
}

func (s *Server) cdslAction(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Action string `json:"action"`
		Note   string `json:"note"`
		Value  string `json:"value"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ActOnCDSLRequest(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Action, b.Note, b.Value)
	ok(w, v, err)
}

func (s *Server) cdslDefaults(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{"defaults": s.ops.CDSLBODefaultsNow(r.Context(), tenant(r)), "fields": ops.CDSLDefaultFields()}, nil)
}

func (s *Server) cdslDefaultsPut(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Values map[string]string `json:"values"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveCDSLBODefaults(r.Context(), tenant(r), actor(r), b.Values)
	ok(w, v, err)
}

func (s *Server) cdslCount(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CDSLDematCheck(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v, err)
}

func (s *Server) cdslCountRun(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RunCDSLDematCheck(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v, err)
}

// cdslBOHook takes CDSL's call-back for an account opening.
func (s *Server) cdslBOHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the body", nil)
		return
	}
	if err := s.ops.CDSLBOCallback(r.Context(), r.PathValue("tenant"), r.PathValue("token"), body); err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

// cdslPage writes the client's page. Its form may post only to CDSL.
func cdslPage(w http.ResponseWriter, p ops.CDSLPage, code int) {
	var buf bytes.Buffer
	if err := ops.CDSLPageHTML.Execute(&buf, p); err != nil {
		http.Error(w, "page error", 500)
		return
	}
	form := "'none'"
	if p.Action != "" {
		form = p.Action
		if i := strings.Index(p.Action[len("https://"):], "/"); strings.HasPrefix(p.Action, "https://") && i > 0 {
			form = p.Action[:len("https://")+i]
		}
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-"+p.Nonce+"'; form-action "+form+"; frame-ancestors 'none'")
	w.WriteHeader(code)
	w.Write(buf.Bytes())
}

// cdslGo is the client's link: a form that goes on to CDSL.
func (s *Server) cdslGo(w http.ResponseWriter, r *http.Request) {
	p, err := s.ops.CDSLGoPage(r.Context(), r.PathValue("tenant"), r.PathValue("token"))
	code := 200
	if err != nil {
		code = 404
	}
	cdslPage(w, p, code)
}

// cdslReturn is where CDSL posts its answer through the client's browser.
func (s *Server) cdslReturn(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		cdslPage(w, ops.CDSLPage{Title: "Not recognised", Message: "The answer could not be read."}, 400)
		return
	}
	p, err := s.ops.CDSLReturn(r.Context(), r.PathValue("tenant"), r.PathValue("token"), r.PostForm)
	code := 200
	if err != nil {
		code = 400
	}
	cdslPage(w, p, code)
}
