package api

import (
	"io"
	"mime/multipart"
	"net/http"

	"ledgerline/internal/ops"
)

// parallelRoutes: Files → Parallel run, the old system's files against
// Ledgerline's (ops/parallel.go).
func (s *Server) parallelRoutes() {
	s.route("GET /v1/parallel", s.parallelGet, false)
	s.route("POST /v1/parallel/compare", s.parallelCompare, true)
	s.route("GET /v1/parallel/compare/{id}", s.parallelOne, false)
}

func (s *Server) parallelGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ParallelRun(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) parallelOne(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ParallelComparisonByID(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

// parallelCompare takes a multipart form: the files "old" (the old
// system's) and "new" (Ledgerline's), "format" (a layout id, or empty to
// recognise it) and "date".
func (s *Server) parallelCompare(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2*ops.MaxXFileBytes+(1<<20))
	if err := r.ParseMultipartForm(maxFileBytes); err != nil {
		writeErr(w, 400, "bad_request", "send the two files as multipart/form-data in the fields 'old' and 'new', with 'format' and 'date'", nil)
		return
	}
	read := func(field string) (string, []byte) {
		fs := r.MultipartForm.File[field]
		if len(fs) == 0 {
			return "", nil
		}
		return fs[0].Filename, readPart(fs[0])
	}
	on, od := read("old")
	nn, nd := read("new")
	v, err := s.ops.ParallelCompare(r.Context(), tenant(r), actor(r), r.FormValue("date"), r.FormValue("format"), on, od, nn, nd)
	ok(w, v, err)
}

func readPart(fh *multipart.FileHeader) []byte {
	f, err := fh.Open()
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, ops.MaxXFileBytes+1))
	return b
}
