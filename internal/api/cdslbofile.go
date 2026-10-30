package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// Handlers for CDSL's BO upload files, registered in cdslBORoutes.

func (s *Server) cdslDetailGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CDSLBODetailView(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v, err)
}

func (s *Server) cdslDetailPut(w http.ResponseWriter, r *http.Request) {
	var b ops.CDSLBODetail
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveCDSLBODetail(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b)
	ok(w, v, err)
}

func (s *Server) cdslDeleteHolder(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC    string `json:"ucc"`
		Holder int    `json:"holder"`
		DiedOn string `json:"died_on"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CDSLDeleteHolder(r.Context(), tenant(r), actor(r), b.UCC, b.Holder, b.DiedOn)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) cdslSigFile(w http.ResponseWriter, r *http.Request) {
	name, b, err := s.ops.CDSLSignatureFile(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), r.URL.Query().Get("txn"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, name, "application/zip", b)
}

func (s *Server) cdslClosureFile(w http.ResponseWriter, r *http.Request) {
	name, b, err := s.ops.CDSLClosureFile(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, name, "text/plain; charset=us-ascii", b)
}
