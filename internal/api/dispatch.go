package api

import (
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

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
	R("POST /v1/signing/check", s.signingCheck, true)
	R("POST /v1/signing/agent", s.signingAgentConnect, true)
	R("POST /v1/signing/agent/certificate", s.signingAgentCert, true)
	R("POST /v1/signing/agent/approve", s.signingAgentApprove, true)
	R("POST /v1/signing/agent/reset-key", s.signingAgentReset, true)
	// Ledgerline Signer next to the firm's USB token: no session; the agent
	// key names the firm (signagent.go).
	s.mux.HandleFunc("POST /v1/signing/agent/hello", s.signAgentCall(s.signAgentHello))
	s.mux.HandleFunc("POST /v1/signing/agent/poll", s.signAgentCall(s.signAgentPoll))
	s.mux.HandleFunc("POST /v1/signing/agent/result", s.signAgentCall(s.signAgentResult))
	R("GET /v1/dispatch", s.dispatchList, false)
	R("GET /v1/dispatch.csv", s.dispatchCSV, false)
	R("GET /v1/dispatch/{id}/file", s.dispatchFile, false)
	R("GET /v1/dispatch/{id}/verify", s.dispatchVerify, false)
	R("POST /v1/dispatch/{id}/bounce", s.dispatchBounce, true)
	R("POST /v1/dispatch/{id}/physical", s.dispatchPhysical, true)
	R("POST /v1/dispatch/{id}/returned", s.dispatchReturned, true)
	R("POST /v1/dispatch/{id}/resend", s.dispatchResend, true)
	R("POST /v1/dispatch/bounces", s.dispatchBounces, true)
	R("POST /v1/dispatch/run", s.dispatchRun, true)
	R("GET /v1/portal/documents/{id}", s.portalDocument, false)
	R("GET /v1/collateral-file", s.collateralFile, false)
	R("GET /v1/nominations", s.nominations, false)
	R("GET /v1/segment-status", s.segmentStatus, false)
	R("POST /v1/clients/{ucc}/segment-status", s.setSegmentStatus, true)
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

func (s *Server) signingCheck(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CheckSigning(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) signingAgentConnect(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Cert     string `json:"cert"` // base64 of the token's .cer/.crt; optional
		Location string `json:"location"`
	}
	if !decode(w, r, &b) {
		return
	}
	data, err := base64.StdEncoding.DecodeString(b.Cert)
	if err != nil {
		writeErr(w, 400, "bad_file", "Send the certificate file as base64 in \"cert\".", nil)
		return
	}
	v, err := s.ops.ConnectSignAgent(r.Context(), tenant(r), actor(r), data, b.Location)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, map[string]any{"key": v.Key, "server": s.publicURL(r), "signing": v.View})
}

func (s *Server) signingAgentCert(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Cert string `json:"cert"`
	}
	if !decode(w, r, &b) {
		return
	}
	data, err := base64.StdEncoding.DecodeString(b.Cert)
	if err != nil || len(data) == 0 {
		writeErr(w, 400, "bad_file", "Send the certificate file as base64 in \"cert\".", nil)
		return
	}
	v, err := s.ops.SetSignAgentCert(r.Context(), tenant(r), actor(r), data)
	ok(w, v, err)
}

func (s *Server) signingAgentApprove(w http.ResponseWriter, r *http.Request) {
	var b struct {
		SHA256 string `json:"sha256"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ApproveSignAgentCert(r.Context(), tenant(r), actor(r), b.SHA256)
	ok(w, v, err)
}

func (s *Server) signingAgentReset(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ResetSignAgentKey(r.Context(), tenant(r), actor(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, map[string]any{"key": v.Key, "server": s.publicURL(r), "signing": v.View})
}

// signAgentCall checks the agent key on Ledgerline Signer's calls. Wrong
// keys are limited per address, and each firm's calls per minute.
func (s *Server) signAgentCall(h func(http.ResponseWriter, *http.Request, string, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.signBad.full(ip) {
			writeErr(w, 429, "too_many", "Too many attempts with a wrong key; wait ten minutes.", nil)
			return
		}
		t, found := ops.SignAgentTenant(key)
		if !found || !s.knownTenant(t) {
			s.signBad.allow(ip)
			writeErr(w, 401, "unauthorized", "This agent key is not valid. Make a new one in Ledgerline (Settings → Digital signature) and set Ledgerline Signer up again.", nil)
			return
		}
		if !s.signLimit.allow(t) {
			writeErr(w, 429, "too_many", "Too many calls; slow down.", nil)
			return
		}
		h(w, r, t, key)
	}
}

// signAgentFail answers a failed call; a wrong or reset key is 401.
func (s *Server) signAgentFail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ops.ErrForbidden) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		s.signBad.allow(ip)
		msg := "This agent key is not valid, or was reset in Ledgerline. Set up Ledgerline Signer again with the new key."
		if strings.Contains(err.Error(), "USB token signer now") {
			msg = "The firm does not sign with a USB token signer now (Settings → Digital signature)."
		}
		writeErr(w, 401, "unauthorized", msg, nil)
		return
	}
	fail(w, err, nil)
}

// signAgentBody is the body of every signer call; each uses its own fields.
type signAgentBody struct {
	CertDER string `json:"cert_der_b64"`
	Version string `json:"agent_version"`
	Host    string `json:"host"`
	Max     int    `json:"max"`
	ID      string `json:"id"`
	Sig     string `json:"signature_b64"`
	Error   string `json:"error"`
}

func (s *Server) signAgentHello(w http.ResponseWriter, r *http.Request, t, key string) {
	var b signAgentBody
	if !decode(w, r, &b) {
		return
	}
	der, err := base64.StdEncoding.DecodeString(b.CertDER)
	if err != nil || len(der) == 0 {
		writeErr(w, 400, "bad_certificate", "Send the token's certificate (DER) as base64 in \"cert_der_b64\".", nil)
		return
	}
	v, err := s.ops.SignAgentHello(r.Context(), t, key, ops.SignAgentMeta{Version: b.Version, Host: b.Host}, der)
	if err != nil {
		s.signAgentFail(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) signAgentPoll(w http.ResponseWriter, r *http.Request, t, key string) {
	var b signAgentBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SignAgentPoll(r.Context(), t, key, ops.SignAgentMeta{Version: b.Version, Host: b.Host}, b.Max)
	if err != nil {
		s.signAgentFail(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) signAgentResult(w http.ResponseWriter, r *http.Request, t, key string) {
	var b signAgentBody
	if !decode(w, r, &b) {
		return
	}
	err := s.ops.SignAgentResult(r.Context(), t, key, ops.SignAgentMeta{Version: b.Version, Host: b.Host}, ops.SignAgentResultIn{ID: b.ID, Signature: b.Sig, Error: b.Error})
	if err != nil {
		s.signAgentFail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
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

func (s *Server) dispatchReturned(w http.ResponseWriter, r *http.Request) {
	var b struct{ Reason string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkReturned(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Reason)
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

// collateralFile is the clearing corporation's client-level collateral file
// for one segment: ?date=&segment=FO|CD|COM|CM&header=1.
func (s *Server) collateralFile(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	q := r.URL.Query()
	name, b, err := s.ops.ClientCollateralFile(r.Context(), tenant(r), actor(r), d, q.Get("segment"), q.Get("header") == "1")
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Write(b)
}

func (s *Server) nominations(w http.ResponseWriter, r *http.Request) {
	rows, by, err := s.ops.Nominations(r.Context(), tenant(r), actor(r))
	ok(w, map[string]any{"rows": rows, "by_state": by, "id_types": ops.NomineeIDTypes, "modes": ops.NominationModes, "max": ops.MaxNominees}, err)
}

func (s *Server) segmentStatus(w http.ResponseWriter, r *http.Request) {
	rows, counts, err := s.ops.SegmentStatusReport(r.Context(), tenant(r), actor(r))
	ok(w, map[string]any{"rows": rows, "counts": counts, "statuses": ops.SegmentStatuses}, err)
}

func (s *Server) setSegmentStatus(w http.ResponseWriter, r *http.Request) {
	var b struct{ Segment, Status, On, Reason string }
	if !decode(w, r, &b) {
		return
	}
	c, err := s.ops.SetSegmentStatus(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Segment, b.Status, b.On, b.Reason)
	ok(w, c, err)
}
