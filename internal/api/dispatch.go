package api

import (
	"encoding/base64"
	"io"
	"net/http"

	"ledgerline/internal/ops"
)

// Digital signature of documents, and the register of every contract note
// and statement sent to clients (SEBI's proof of delivery).
func (s *Server) dispatchRoutes() {
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/signing", s.signing, false)
	R("POST /v1/signing/file", s.signingFile, true)
	R("POST /v1/signing/remote", s.signingRemote, true)
	R("POST /v1/signing/off", s.signingOff, true)
	R("GET /v1/dispatch", s.dispatchList, false)
	R("GET /v1/dispatch.csv", s.dispatchCSV, false)
	R("GET /v1/dispatch/{id}/file", s.dispatchFile, false)
	R("GET /v1/dispatch/{id}/verify", s.dispatchVerify, false)
	R("POST /v1/dispatch/{id}/bounce", s.dispatchBounce, true)
	R("POST /v1/dispatch/{id}/physical", s.dispatchPhysical, true)
	R("POST /v1/dispatch/{id}/resend", s.dispatchResend, true)
	R("POST /v1/dispatch/bounces", s.dispatchBounces, true)
	R("POST /v1/dispatch/run", s.dispatchRun, true)
	R("GET /v1/portal/documents/{id}", s.portalDocument, false)
	clientRoutes["GET /v1/portal/documents/{id}"] = true
}

func (s *Server) signing(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Signing(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) signingFile(w http.ResponseWriter, r *http.Request) {
	var b struct {
		File     string `json:"file"` // base64 of the .pfx/.p12 or PEM
		Password string `json:"password"`
		Location string `json:"location"`
	}
	if !decode(w, r, &b) {
		return
	}
	data, err := base64.StdEncoding.DecodeString(b.File)
	if err != nil || len(data) == 0 {
		writeErr(w, 400, "bad_file", "Send the certificate file as base64 in \"file\".", nil)
		return
	}
	v, err := s.ops.SetSigningFile(r.Context(), tenant(r), actor(r), data, b.Password, b.Location)
	ok(w, v, err)
}

func (s *Server) signingRemote(w http.ResponseWriter, r *http.Request) {
	var b struct{ URL, Key, Location string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetSigningRemote(r.Context(), tenant(r), actor(r), b.URL, b.Key, b.Location)
	ok(w, v, err)
}

func (s *Server) signingOff(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SetSigningOff(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func dispatchFilter(r *http.Request) ops.DispatchFilter {
	q := r.URL.Query()
	return ops.DispatchFilter{From: q.Get("from"), To: q.Get("to"), Kind: q.Get("kind"), Status: q.Get("status"), UCC: q.Get("ucc")}
}

func (s *Server) dispatchList(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.DispatchRegister(r.Context(), tenant(r), actor(r), dispatchFilter(r))
	ok(w, v, err)
}

func (s *Server) dispatchCSV(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.DispatchCSV(r.Context(), tenant(r), actor(r), dispatchFilter(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="dispatch-register.csv"`)
	w.Write(b)
}

func writePDF(w http.ResponseWriter, name string, b []byte) {
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(b)
}

func (s *Server) dispatchFile(w http.ResponseWriter, r *http.Request) {
	name, b, err := s.ops.DispatchFile(r.Context(), tenant(r), actor(r), r.PathValue("id"), false)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writePDF(w, name, b)
}

func (s *Server) portalDocument(w http.ResponseWriter, r *http.Request) {
	name, b, err := s.ops.DispatchFile(r.Context(), tenant(r), actor(r), r.PathValue("id"), true)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writePDF(w, name, b)
}

func (s *Server) dispatchVerify(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.VerifyDispatch(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) dispatchBounce(w http.ResponseWriter, r *http.Request) {
	var b struct{ Reason string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkBounced(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Reason)
	ok(w, v, err)
}

func (s *Server) dispatchPhysical(w http.ResponseWriter, r *http.Request) {
	var b struct{ Courier string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkPhysical(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Courier)
	ok(w, v, err)
}

func (s *Server) dispatchResend(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Resend(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

// dispatchBounces takes the mail provider's bounce report as CSV in the body.
func (s *Server) dispatchBounces(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFileBytes))
	if err != nil || len(data) == 0 {
		writeErr(w, 400, "empty_file", "Send the bounce report CSV as the body.", nil)
		return
	}
	n, err := s.ops.ImportBounces(r.Context(), tenant(r), actor(r), data)
	ok(w, map[string]int{"bounced": n}, err)
}

func (s *Server) dispatchRun(w http.ResponseWriter, r *http.Request) {
	var b struct{ Kind, Period string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SendStatements(r.Context(), tenant(r), actor(r), b.Kind, b.Period)
	ok(w, v, err)
}
