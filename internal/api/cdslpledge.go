package api

import (
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"sync"

	"ledgerline/internal/ops"
)

var cdslPledgeRoutesDone sync.Map // *Server -> registered

// cdslPledgeRoutes: CDSL's pledge APIs (margin pledge with the client's
// confirmation at CDSL, re-pledge, normal pledge, unpledge, invocation)
// and early pay-in; the client portal's Pledge shares page; and the
// addresses CDSL itself posts to (the client's return from CDSL's OTP page
// and early pay-in answers), which need no sign-in and are trusted only
// when they decrypt with the firm's API key and match a request sent.
func (s *Server) cdslPledgeRoutes() {
	if s.ops == nil {
		return
	}
	if _, dup := cdslPledgeRoutesDone.LoadOrStore(s, true); dup {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	C := func(p string, h http.HandlerFunc, m bool) { clientRoutes[p] = true; s.route(p, h, m) }
	R("GET /v1/cdsl-pledges", s.cplScreen, false)
	R("GET /v1/cdsl-pledges/settings", s.cplSettingsGet, false)
	R("PUT /v1/cdsl-pledges/settings", s.cplSettingsPut, true)
	R("POST /v1/cdsl-pledges/margin", s.cplMarginRequest, true)
	R("POST /v1/cdsl-pledges/margin/{id}/decide", s.cplMarginDecide, true)
	R("POST /v1/cdsl-pledges/calls/{reqid}/inquire", s.cplInquire, true)
	R("POST /v1/cdsl-pledges/repledges", s.cplRepledge, true)
	R("POST /v1/cdsl-pledges/repledges/{id}/decide", s.cplRepledgeDecide, true)
	R("POST /v1/cdsl-pledges/normal", s.cplNormal, true)
	R("POST /v1/cdsl-pledges/normal/{id}/decide", s.cplNormalDecide, true)
	R("POST /v1/cdsl-pledges/unpledges", s.cplUnpledge, true)
	R("POST /v1/cdsl-pledges/unpledges/{id}/decide", s.cplUnpledgeDecide, true)
	R("POST /v1/cdsl-pledges/invocations", s.cplInvocation, true)
	R("POST /v1/cdsl-pledges/invocations/{id}/decide", s.cplInvocationDecide, true)
	R("POST /v1/cdsl-pledges/invocations/{id}/invoke", s.cplInvokeNow, true)
	R("POST /v1/cdsl-pledges/resolve/{id}", s.cplResolve, true)
	R("GET /v1/cdsl-epi", s.epiScreen, false)
	R("POST /v1/cdsl-epi", s.epiEnter, true)
	R("POST /v1/cdsl-epi/decide", s.epiDecide, true)
	R("POST /v1/cdsl-epi/inquire", s.epiInquire, true)
	C("GET /v1/portal/pledges", s.portalPledges, false)
	C("POST /v1/portal/pledges", s.cplMarginRequest, true)
	C("POST /v1/portal/pledges/confirm", s.portalPledgeLink, true)
	C("POST /v1/portal/pledges/unpledge", s.cplUnpledge, true)
	// Not signed in: the hand-over page is reached by its one-time token;
	// CDSL posts the pledge result and early pay-in answers, encrypted.
	s.mux.HandleFunc("GET /hooks/{tenant}/cdsl/pledge/{token}", s.cplFormPage)
	s.mux.HandleFunc("POST /hooks/{tenant}/cdsl/pledge-return", s.cplReturn)
	s.mux.HandleFunc("POST /hooks/{tenant}/cdsl/epi", s.epiHook)
}

func (s *Server) cplScreen(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.CDSLPledges(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) cplSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.ops.CDSLPledgeSettings(r.Context(), tenant(r)))
}

func (s *Server) cplSettingsPut(w http.ResponseWriter, r *http.Request) {
	var b ops.CDSLPledgeConfig
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveCDSLPledgeSettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) cplMarginRequest(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC   string           `json:"ucc"`
		Kind  string           `json:"kind"`
		MTFID string           `json:"mtf_id"`
		Lines []ops.PledgeLine `json:"lines"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RequestMarginPledge(r.Context(), tenant(r), actor(r), b.UCC, b.Kind, b.MTFID, b.Lines)
	ok(w, v, err)
}

type cplDecision struct {
	Accept bool   `json:"accept"`
	Reason string `json:"reason"`
}

func (s *Server) cplMarginDecide(w http.ResponseWriter, r *http.Request) {
	var b cplDecision
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecidePledge(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Accept, b.Reason)
	ok(w, v, err)
}

func (s *Server) cplInquire(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.InquireMarginPledge(r.Context(), tenant(r), actor(r), r.PathValue("reqid"))
	ok(w, v, err)
}

func (s *Server) cplRepledge(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Lines []ops.RepledgeAsk `json:"lines"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RequestRepledge(r.Context(), tenant(r), actor(r), b.Lines)
	ok(w, v, err)
}

func (s *Server) cplRepledgeDecide(w http.ResponseWriter, r *http.Request) {
	var b cplDecision
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideRepledge(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Accept, b.Reason)
	ok(w, v, err)
}

func (s *Server) cplNormal(w http.ResponseWriter, r *http.Request) {
	var b ops.NormalPledge
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RequestNormalPledge(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) cplNormalDecide(w http.ResponseWriter, r *http.Request) {
	var b cplDecision
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideNormalPledge(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Accept, b.Reason)
	ok(w, v, err)
}

func (s *Server) cplUnpledge(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC    string `json:"ucc"`
		PSN    string `json:"psn"`
		Qty    int64  `json:"qty"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RequestUnpledge(r.Context(), tenant(r), actor(r), b.UCC, b.PSN, b.Qty, b.Reason)
	ok(w, v, err)
}

func (s *Server) cplUnpledgeDecide(w http.ResponseWriter, r *http.Request) {
	var b cplDecision
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideUnpledge(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Accept, b.Reason)
	ok(w, v, err)
}

func (s *Server) cplInvocation(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date   string `json:"date"`
		PSN    string `json:"psn"`
		Qty    int64  `json:"qty"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RaiseInvocation(r.Context(), tenant(r), actor(r), b.Date, b.PSN, b.Qty, b.Reason)
	ok(w, v, err)
}

func (s *Server) cplInvocationDecide(w http.ResponseWriter, r *http.Request) {
	var b cplDecision
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideInvocation(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Accept, b.Reason)
	ok(w, v, err)
}

func (s *Server) cplInvokeNow(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.InvokeNow(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) cplResolve(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Done bool   `json:"done"`
		PSN  string `json:"psn"`
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ResolveCDSLRequest(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Done, b.PSN, b.Note)
	ok(w, v, err)
}

func (s *Server) epiScreen(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.EarlyPayins(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) epiEnter(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Lines []ops.EPIInput `json:"lines"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.EnterEPI(r.Context(), tenant(r), actor(r), b.Lines)
	ok(w, v, err)
}

func (s *Server) epiDecide(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IDs    []string `json:"ids"`
		Accept bool     `json:"accept"`
		Reason string   `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideEPI(r.Context(), tenant(r), actor(r), b.IDs, b.Accept, b.Reason)
	ok(w, v, err)
}

func (s *Server) epiInquire(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Seq string `json:"seq"`
	}
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.InquireEPI(r.Context(), tenant(r), actor(r), b.Seq)
	ok(w, map[string]int{"answered": n}, err)
}

func (s *Server) portalPledges(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PortalPledges(r.Context(), tenant(r), actor(r), r.URL.Query().Get("ucc"))
	ok(w, v, err)
}

// portalPledgeLink gives the client the one-time address of the page that
// hands them over to CDSL.
func (s *Server) portalPledgeLink(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC string `json:"ucc"`
	}
	if !decode(w, r, &b) {
		return
	}
	tok, err := s.ops.CDSLPledgeLink(r.Context(), tenant(r), actor(r), b.UCC)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, map[string]string{"url": s.publicURL(r) + "/hooks/" + neturl.PathEscape(tenant(r)) + "/cdsl/pledge/" + tok})
}

// cplPage writes the hand-over or result page. The form may post only to
// the CDSL address it names.
func cplPage(w http.ResponseWriter, p ops.CDSLPledgePage, status int) {
	action := "'none'"
	if u, err := neturl.Parse(p.Action); err == nil && u.Scheme == "https" && u.Host != "" {
		action = "https://" + u.Host
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action "+action+"; frame-ancestors 'none'")
	w.WriteHeader(status)
	_ = ops.CDSLPledgeHTML.Execute(w, p)
}

func (s *Server) cplFormPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.ops.CDSLPledgeFormPage(r.Context(), r.PathValue("tenant"), r.PathValue("token"), s.publicURL(r))
	st := 200
	if err != nil {
		st = 400
	}
	cplPage(w, p, st)
}

func (s *Server) cplReturn(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		writeErr(w, 400, "bad_request", "cannot read the form", nil)
		return
	}
	p, _ := s.ops.CDSLPledgeReturn(r.Context(), r.PathValue("tenant"), s.publicURL(r), r.PostForm)
	cplPage(w, p, 200)
}

func (s *Server) epiHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the body", nil)
		return
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		// Keep the form as it came: open() reads form posts itself.
		body = []byte(strings.TrimSpace(string(body)))
	}
	n, err := s.ops.CDSLEPIHook(r.Context(), r.PathValue("tenant"), body)
	ok(w, map[string]int{"applied": n}, err)
}
