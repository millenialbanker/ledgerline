package api

import (
	"encoding/base64"
	"errors"
	"html/template"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"

	"ledgerline/internal/ops"
)

var cdslTxnRoutesDone sync.Map // *Server -> registered

// cdslTxnRoutes: CDSL's transaction APIs (common upload of verified DIS,
// DESTAT), eDIS (authorisations, inquiry, TPIN, cancel) for staff and the
// client portal, report setup, and the pages CDSL sends the browser back
// to.
func (s *Server) cdslTxnRoutes() {
	if s.ops == nil {
		return
	}
	if _, dup := cdslTxnRoutesDone.LoadOrStore(s, true); dup {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	C := func(p string, h http.HandlerFunc, m bool) { clientRoutes[p] = true; s.route(p, h, m) }
	R("GET /v1/cdsl/txn", s.cdslTxnScreen, false)
	R("POST /v1/cdsl/txn/send", s.cdslTxnSend, true)
	R("POST /v1/cdsl/txn/file", s.cdslTxnFile, true)
	R("POST /v1/cdsl/txn/uploads/{id}/check", s.cdslTxnCheck, true)
	R("POST /v1/cdsl/destat/{id}", s.cdslDestatStart, true)
	R("POST /v1/cdsl/reports", s.cdslReportSetup, true)
	R("POST /v1/cdsl/reports/subscriptions", s.cdslReportSub, true)
	R("POST /v1/cdsl/reports/sftp", s.cdslReportSFTP, true)
	R("GET /v1/cdsl/edis", s.edisScreen, false)
	R("POST /v1/cdsl/edis", s.edisStart, true)
	R("POST /v1/cdsl/edis/{id}/inquire", s.edisInquire, true)
	R("POST /v1/cdsl/edis/{id}/cancel", s.edisCancel, true)
	R("POST /v1/cdsl/edis/tpin", s.edisTPIN, true)
	R("POST /v1/cdsl/edis/tpin-change", s.edisTPINChange, true)
	C("GET /v1/portal/edis", s.portalEDIS, false)
	C("POST /v1/portal/edis", s.edisStart, true)
	C("POST /v1/portal/edis/{id}/cancel", s.edisCancel, true)
	C("POST /v1/portal/edis/tpin", s.edisTPIN, true)
	C("POST /v1/portal/edis/tpin-change", s.edisTPINChange, true)
	// Not signed in: CDSL sends the browser back with encrypted details,
	// taken only when they decrypt under the DP's key and match a request
	// sent from here; reports come to an address with a secret token.
	s.mux.HandleFunc("POST /hooks/{tenant}/cdsl/edis/return", s.edisReturnHook)
	s.mux.HandleFunc("POST /hooks/{tenant}/cdsl/edis/cancel-return", s.edisCancelHook)
	s.mux.HandleFunc("POST /hooks/{tenant}/cdsl/destat/return", s.destatReturnHook)
	s.mux.HandleFunc("POST /hooks/{tenant}/cdsl/report/{token}", s.cdslReportHook)
}

func (s *Server) cdslTxnScreen(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CDSLTxnScreen(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

type cdslDateBody struct {
	Date string `json:"date"`
}

func (s *Server) cdslTxnSend(w http.ResponseWriter, r *http.Request) {
	var b cdslDateBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SendDISByAPI(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, v, err)
}

func (s *Server) cdslTxnFile(w http.ResponseWriter, r *http.Request) {
	var b cdslDateBody
	if !decode(w, r, &b) {
		return
	}
	name, f, n, err := s.ops.DISUploadFile(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, map[string]any{"name": name, "file": string(f), "instructions": n}, err)
}

func (s *Server) cdslTxnCheck(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CheckCDSLUpload(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) cdslDestatStart(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.StartDestat(r.Context(), tenant(r), actor(r), r.PathValue("id"), s.publicURL(r))
	ok(w, v, err)
}

func (s *Server) cdslReportSetup(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Module string `json:"module"`
		Report string `json:"report"`
		Param  string `json:"param"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetupCDSLReport(r.Context(), tenant(r), actor(r), b.Module, b.Report, b.Param, s.publicURL(r))
	ok(w, v, err)
}

func (s *Server) cdslReportSub(w http.ResponseWriter, r *http.Request) {
	var b ops.CDSLReportSub
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveCDSLReportSub(r.Context(), tenant(r), actor(r), b, s.publicURL(r))
	ok(w, v, err)
}

func (s *Server) cdslReportSFTP(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FetchCDSLSFTP(r.Context(), tenant(r), actor(r))
	ok(w, map[string]any{"loaded": v}, err)
}

func (s *Server) edisScreen(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.EDISScreen(r.Context(), tenant(r), actor(r), d)
	ok(w, v, err)
}

func (s *Server) portalEDIS(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PortalEDIS(r.Context(), tenant(r), actor(r), r.URL.Query().Get("ucc"))
	ok(w, v, err)
}

func (s *Server) edisStart(w http.ResponseWriter, r *http.Request) {
	var b ops.EDISRequest
	if !decode(w, r, &b) {
		return
	}
	f, a, err := s.ops.StartEDIS(r.Context(), tenant(r), actor(r), b, s.publicURL(r))
	ok(w, map[string]any{"form": f, "authorisation": a}, err)
}

func (s *Server) edisInquire(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.EDISInquire(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) edisCancel(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.StartEDISCancel(r.Context(), tenant(r), actor(r), r.PathValue("id"), s.publicURL(r))
	ok(w, v, err)
}

type edisUCCBody struct {
	UCC  string `json:"ucc"`
	Note string `json:"note"`
}

func (s *Server) edisTPIN(w http.ResponseWriter, r *http.Request) {
	var b edisUCCBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.GenerateTPIN(r.Context(), tenant(r), actor(r), b.UCC, b.Note)
	ok(w, v, err)
}

func (s *Server) edisTPINChange(w http.ResponseWriter, r *http.Request) {
	var b edisUCCBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ChangeTPINForm(r.Context(), tenant(r), actor(r), b.UCC)
	ok(w, v, err)
}

// ---- CDSL's redirects back ----

// cdslHookForm reads CDSL's form post (urlencoded or multipart).
func cdslHookForm(w http.ResponseWriter, r *http.Request) (neturl.Values, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			return nil, err
		}
		return r.PostForm, nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	return r.PostForm, nil
}

var cdslReturnHTML = template.Must(template.New("cdslret").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}}</title>
<style>body{font:15px/1.5 system-ui,sans-serif;background:#f6f7f9;color:#1d2433;margin:0;padding:16px}main{max-width:520px;margin:40px auto;background:#fff;border:1px solid #e3e6eb;border-radius:10px;padding:24px}
h1{font-size:18px;margin:0 0 12px}.ok{color:#14794a}.no{color:#b42318}a{display:inline-block;margin-top:16px;color:#1d4ed8}small{color:#667085}</style></head>
<body><main><small>{{.Firm}}</small><h1 class="{{if .OK}}ok{{else}}no{{end}}">{{.Title}}</h1>{{range .Lines}}<p>{{.}}</p>{{end}}<a href="{{.Back}}">{{.BackFor}}</a></main></body></html>`))

func cdslReturnPage(w http.ResponseWriter, p ops.CDSLReturn, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	if p.Back == "" {
		p.Back, p.BackFor = "/", "Back to Ledgerline"
	}
	if p.Title == "" {
		p.Title = "CDSL"
	}
	if err != nil {
		code := 400
		if errors.Is(err, ops.ErrNotFound) {
			code = 404
		}
		w.WriteHeader(code)
		p.OK, p.Lines = false, []string{"This answer from CDSL could not be taken. If you were authorising on CDSL's page, check the status in Ledgerline before trying again."}
	}
	_ = cdslReturnHTML.Execute(w, p)
}

func (s *Server) edisReturnHook(w http.ResponseWriter, r *http.Request) {
	f, err := cdslHookForm(w, r)
	if err != nil {
		cdslReturnPage(w, ops.CDSLReturn{}, err)
		return
	}
	p, err := s.ops.EDISReturn(r.Context(), r.PathValue("tenant"), f)
	cdslReturnPage(w, p, err)
}

func (s *Server) edisCancelHook(w http.ResponseWriter, r *http.Request) {
	f, err := cdslHookForm(w, r)
	if err != nil {
		cdslReturnPage(w, ops.CDSLReturn{}, err)
		return
	}
	p, err := s.ops.EDISCancelReturn(r.Context(), r.PathValue("tenant"), f)
	cdslReturnPage(w, p, err)
}

func (s *Server) destatReturnHook(w http.ResponseWriter, r *http.Request) {
	f, err := cdslHookForm(w, r)
	if err != nil {
		cdslReturnPage(w, ops.CDSLReturn{}, err)
		return
	}
	p, err := s.ops.DestatReturn(r.Context(), r.PathValue("tenant"), f)
	cdslReturnPage(w, p, err)
}

// cdslReportHook takes what CDSL sends for a report setup: files (multipart),
// form fields or a body.
func (s *Server) cdslReportHook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	var files []ops.CDSLReportFile
	form := neturl.Values{}
	var raw []byte
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "multipart/"):
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeErr(w, 400, "bad_request", "cannot read the upload", nil)
			return
		}
		form = r.MultipartForm.Value
		for _, fhs := range r.MultipartForm.File {
			for _, fh := range fhs {
				f, err := fh.Open()
				if err != nil {
					continue
				}
				b, _ := io.ReadAll(f)
				f.Close()
				files = append(files, ops.CDSLReportFile{Name: fh.Filename, Data: b})
			}
		}
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"):
		if err := r.ParseForm(); err != nil {
			writeErr(w, 400, "bad_request", "cannot read the form", nil)
			return
		}
		form = r.PostForm
	default:
		b, err := io.ReadAll(r.Body)
		if err != nil {
			writeErr(w, 400, "bad_request", "cannot read the body", nil)
			return
		}
		raw = b
		if name := r.URL.Query().Get("filename"); name != "" && len(b) > 0 {
			if d, err := base64.StdEncoding.DecodeString(string(b)); err == nil {
				b = d
			}
			files, raw = []ops.CDSLReportFile{{Name: name, Data: b}}, nil
		}
	}
	res, err := s.ops.CDSLReportCallback(r.Context(), r.PathValue("tenant"), r.PathValue("token"), form, files, raw)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "received", "result": res})
}
