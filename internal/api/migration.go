package api

import (
	"net/http"
	"strconv"
	"strings"

	"ledgerline/internal/ops"
)

// Migration from another back office (ops/migration.go): the package
// upload in chunks, its check, maker-checker load, reconciliation and undo;
// the read-only history of earlier years and the document archive, for
// staff and, the client's own, in the portal.
func (s *Server) migrationRoutes() {
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	// Branch staff reach a client's history from the client screen.
	B := func(p string, h http.HandlerFunc, m bool) { scopedRoutes[p] = true; s.route(p, h, m) }
	C := func(p string, h http.HandlerFunc, m bool) { clientRoutes[p] = true; s.route(p, h, m) }
	R("GET /v1/migration", s.migView, false)
	R("POST /v1/migration/uploads", s.migCreate, true)
	R("PUT /v1/migration/uploads/{id}/chunks/{n}", s.migChunk, true)
	R("POST /v1/migration/uploads/{id}/complete", s.migComplete, true)
	R("GET /v1/migration/packages/{id}", s.migPackage, false)
	R("GET /v1/migration/packages/{id}/errors", s.migErrors, false)
	R("GET /v1/migration/packages/{id}/errors.csv", s.migErrorsCSV, false)
	R("POST /v1/migration/packages/{id}/check", s.migCheck, true)
	R("POST /v1/migration/packages/{id}/discard", s.migDiscard, true)
	R("POST /v1/migration/proposals", s.migPropose, true)
	R("GET /v1/migration/runs/{id}", s.migGet, false)
	R("POST /v1/migration/runs/{id}/decide", s.migDecide, true)
	R("POST /v1/migration/runs/{id}/resume", s.migResume, true)
	R("POST /v1/migration/runs/{id}/reconcile", s.migReconcile, true)
	R("GET /v1/migration/runs/{id}/reconcile.csv", s.migReconCSV, false)
	R("GET /v1/migration/runs/{id}/reconcile.pdf", s.migReconPDF, false)
	R("POST /v1/migration/runs/{id}/undo", s.migUndo, true)
	R("POST /v1/migration/runs/{id}/undo/decide", s.migUndoDecide, true)
	B("GET /v1/history/{ucc}", s.histIndex, false)
	B("GET /v1/history/{ucc}/{fy}/statement", s.histStatement, false)
	B("GET /v1/history/{ucc}/{fy}/{kind}", s.histTable, false)
	R("GET /v1/history-files", s.histFiles, false)
	R("GET /v1/history-files/{fy}/{kind}/csv", s.histExport, false)
	R("GET /v1/archive", s.arcSearch, false)
	R("GET /v1/archive/{ucc}/{id}", s.arcFile, false)
	R("POST /v1/archive/{ucc}/{id}/hold", s.arcHold, true)
	R("POST /v1/archive/{ucc}/{id}/delete", s.arcDelete, true)
	C("GET /v1/portal/history/{ucc}", s.pHistIndex, false)
	C("GET /v1/portal/history/{ucc}/{fy}/statement", s.pHistStatement, false)
	C("GET /v1/portal/history/{ucc}/{fy}/{kind}", s.pHistTable, false)
	C("GET /v1/portal/archive/{ucc}", s.pArchive, false)
	C("GET /v1/portal/archive/{ucc}/{id}", s.pArcFile, false)
}

func (s *Server) migView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MigrationView(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) migCreate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		ChunkSize int64  `json:"chunk_size"`
	}
	if !decode(w, r, &b) {
		return
	}
	p, err := s.ops.CreateMigUpload(r.Context(), tenant(r), actor(r), b.Name, b.Size, b.ChunkSize)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, ops.MigPackageView{MigPackage: p, Missing: p.Chunks})
}

// migChunk streams one chunk (the raw body, at most 16 MB) to disk.
func (s *Server) migChunk(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		writeErr(w, 400, "bad_chunk", "The chunk number must be a whole number from 0.", nil)
		return
	}
	body := http.MaxBytesReader(w, r.Body, ops.MigMaxChunk+1)
	p, err := s.ops.PutMigChunk(r.Context(), tenant(r), actor(r), r.PathValue("id"), n, body, r.Header.Get("X-Chunk-SHA256"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, map[string]any{"id": p.ID, "received": len(p.Have), "chunks": p.Chunks, "missing": len(p.Missing())})
}

func (s *Server) migComplete(w http.ResponseWriter, r *http.Request) {
	p, err := s.ops.CompleteMigUpload(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, map[string]any{"missing": p.Missing()})
		return
	}
	writeJSON(w, 200, ops.MigPackageView{MigPackage: p})
}

func (s *Server) migPackage(w http.ResponseWriter, r *http.Request) {
	p, errs, n, err := s.ops.MigPackageDetail(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	// The chunks still to send, for a browser resuming the upload.
	missing := []int{}
	if p.Status == ops.MigUploading {
		missing = s.ops.MigMissing(r.Context(), tenant(r), r.PathValue("id"))
	}
	writeJSON(w, 200, map[string]any{"package": p, "errors": errs, "error_count": n, "missing": missing})
}

func (s *Server) migErrors(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	off, _ := strconv.Atoi(q.Get("offset"))
	lim, _ := strconv.Atoi(q.Get("limit"))
	if lim <= 0 || lim > 5000 {
		lim = 500
	}
	es, n, err := s.ops.MigErrors(r.Context(), tenant(r), actor(r), r.PathValue("id"), q.Get("file"), off, lim)
	ok(w, map[string]any{"errors": es, "total": n}, err)
}

func (s *Server) migErrorsCSV(w http.ResponseWriter, r *http.Request) {
	csvHead(w, "migration-check-"+r.PathValue("id")+".csv")
	if err := s.ops.MigErrorsCSV(r.Context(), tenant(r), actor(r), r.PathValue("id"), w); err != nil {
		w.Header().Del("Content-Disposition")
		fail(w, err, nil)
	}
}

func (s *Server) migCheck(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Passphrase string `json:"passphrase"`
		Firm       string `json:"firm"`
	}
	if !decode(w, r, &b) {
		return
	}
	p, err := s.ops.CheckMigPackage(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Passphrase, b.Firm)
	b.Passphrase = ""
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 202, ops.MigPackageView{MigPackage: p})
}

func (s *Server) migDiscard(w http.ResponseWriter, r *http.Request) {
	p, err := s.ops.DiscardMigPackage(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, ops.MigPackageView{MigPackage: p}, err)
}

func (s *Server) migPropose(w http.ResponseWriter, r *http.Request) {
	var b ops.MigProposal
	if !decode(w, r, &b) {
		return
	}
	m, err := s.ops.ProposeMigration(r.Context(), tenant(r), actor(r), b)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, m)
}

func (s *Server) migGet(w http.ResponseWriter, r *http.Request) {
	m, err := s.ops.GetMigration(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, m, err)
}

func (s *Server) migDecide(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve    bool   `json:"approve"`
		Passphrase string `json:"passphrase"`
		Note       string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	m, err := s.ops.DecideMigration(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Passphrase, b.Note)
	b.Passphrase = ""
	ok(w, m, err)
}

func (s *Server) migResume(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Passphrase string `json:"passphrase"`
	}
	if !decode(w, r, &b) {
		return
	}
	m, err := s.ops.ResumeMigration(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Passphrase)
	b.Passphrase = ""
	ok(w, m, err)
}

func (s *Server) migReconcile(w http.ResponseWriter, r *http.Request) {
	m, err := s.ops.ReconcileMigration(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, m, err)
}

func (s *Server) migReconCSV(w http.ResponseWriter, r *http.Request) {
	csvHead(w, "migration-reconciliation-"+r.PathValue("id")+".csv")
	if err := s.ops.MigReconCSV(r.Context(), tenant(r), actor(r), r.PathValue("id"), w); err != nil {
		w.Header().Del("Content-Disposition")
		fail(w, err, nil)
	}
}

func (s *Server) migReconPDF(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.MigReconPDF(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	writePDF(w, "migration-reconciliation-"+r.PathValue("id")+".pdf", b)
}

func (s *Server) migUndo(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	m, err := s.ops.ProposeUndo(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Reason)
	ok(w, m, err)
}

func (s *Server) migUndoDecide(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	m, err := s.ops.DecideUndo(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Note)
	ok(w, m, err)
}

// ---- history ----

func (s *Server) histIndex(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ClientHistory(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), false)
	ok(w, v, err)
}

func (s *Server) histStatement(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.HistoryStatement(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), r.PathValue("fy"), false)
	ok(w, v, err)
}

func (s *Server) histTable(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ClientHistoryTable(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), r.PathValue("fy"), r.PathValue("kind"), false)
	ok(w, v, err)
}

func (s *Server) histFiles(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.HistoryFiles(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) histExport(w http.ResponseWriter, r *http.Request) {
	csvHead(w, "history-"+r.PathValue("fy")+"-"+strings.ToLower(r.PathValue("kind"))+".csv")
	if err := s.ops.ExportHistory(r.Context(), tenant(r), actor(r), r.PathValue("fy"), r.PathValue("kind"), w); err != nil {
		w.Header().Del("Content-Disposition")
		fail(w, err, nil)
	}
}

func (s *Server) pHistIndex(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ClientHistory(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), true)
	ok(w, v, err)
}

func (s *Server) pHistStatement(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.HistoryStatement(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), r.PathValue("fy"), true)
	ok(w, v, err)
}

func (s *Server) pHistTable(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ClientHistoryTable(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), r.PathValue("fy"), r.PathValue("kind"), true)
	ok(w, v, err)
}

// ---- archive ----

func (s *Server) arcSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lim, _ := strconv.Atoi(q.Get("limit"))
	v, err := s.ops.SearchArchive(r.Context(), tenant(r), actor(r), ops.ArchiveQuery{UCC: q.Get("ucc"), Type: q.Get("type"), From: q.Get("from"), To: q.Get("to"), Signed: q.Get("signed"), Limit: lim})
	ok(w, v, err)
}

func (s *Server) arcFile(w http.ResponseWriter, r *http.Request) {
	name, b, err := s.ops.ArchiveFile(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), r.PathValue("id"), false)
	writeArchived(w, name, b, err)
}

func (s *Server) pArchive(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PortalArchive(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v, err)
}

func (s *Server) pArcFile(w http.ResponseWriter, r *http.Request) {
	name, b, err := s.ops.ArchiveFile(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), r.PathValue("id"), true)
	writeArchived(w, name, b, err)
}

func writeArchived(w http.ResponseWriter, name string, b []byte, err error) {
	if err != nil {
		fail(w, err, nil)
		return
	}
	ct := "application/octet-stream"
	if strings.HasSuffix(strings.ToLower(name), ".pdf") {
		ct = "application/pdf"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

func (s *Server) arcHold(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On     bool   `json:"on"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetArchiveHold(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), r.PathValue("id"), b.On, b.Reason)
	ok(w, v, err)
}

func (s *Server) arcDelete(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DeleteArchived(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), r.PathValue("id"), b.Reason)
	ok(w, v, err)
}

func csvHead(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
}
