package api

import (
	"io"
	"net/http"

	"ledgerline/internal/ops"
)

// compRegRoutes: Compliance → Registers (internal/ops/compregs.go and the
// files it links: taxgst.go, gstnotes.go, apweb.go, dpmisc.go). All under
// /v1/compregs, so they open with the Registers screen's roles; the ops
// layer checks each action's own role.
func (s *Server) compRegRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/compregs", s.compRegs, false)
	R("POST /v1/compregs/records", s.compRegAdd, true)
	R("POST /v1/compregs/records/{id}/withdraw", s.compRegWithdraw, true)
	R("GET /v1/compregs/website", s.compWebsite, false)
	R("POST /v1/compregs/website/published", s.compWebsitePublished, true)
	R("POST /v1/compregs/ap-notice", s.compAPNotice, true)
	R("POST /v1/compregs/prop-notice", s.compPropNotice, true)
	R("POST /v1/compregs/ap-status", s.compAPStatus, true)
	R("GET /v1/compregs/terminal-turnover", s.compTermTurnover, false)
	R("GET /v1/compregs/terminal-turnover.csv", s.compTermTurnoverCSV, false)
	R("GET /v1/compregs/nse-users", s.compNSEUsers, false)
	R("POST /v1/compregs/nse-users", s.compNSEUserSet, true)
	R("GET /v1/compregs/tds-lower.csv", s.compTDSLower, false)
	R("POST /v1/compregs/gstr2b", s.compGSTR2B, true)
	R("GET /v1/compregs/itc", s.compITC, false)
	R("POST /v1/compregs/gst-notes", s.compGSTNotes, true)
	R("GET /v1/compregs/pass-through", s.compPassThrough, false)
	R("POST /v1/compregs/to-be-closed", s.compToBeClosed, true)
	R("POST /v1/compregs/ro-kit", s.compROKit, true)
	R("POST /v1/compregs/cml", s.compCML, true)
	R("POST /v1/compregs/dis/{id}/delay-reason", s.compDISDelay, true)
	R("POST /v1/compregs/tickets/{id}/exchange", s.compTicketExchange, true)
}

func (s *Server) compRegs(w http.ResponseWriter, r *http.Request) {
	ctx, t, a := r.Context(), tenant(r), actor(r)
	regs, err := s.ops.CompRegisters(ctx, t, a)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, map[string]any{"registers": regs, "tds_due": s.ops.TDSCertsDue(ctx, t), "ap_status": s.ops.APStatusList(ctx, t), "to_be_closed": s.ops.ToBeClosedList(ctx, t)})
}

func (s *Server) compRegAdd(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Kind   string            `json:"kind"`
		Values map[string]string `json:"values"`
		Note   string            `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AddCompRecord(r.Context(), tenant(r), actor(r), b.Kind, b.Values, b.Note)
	ok(w, v, err)
}

func (s *Server) compRegWithdraw(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Why string `json:"why"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.WithdrawCompRecord(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Why)
	ok(w, v, err)
}

func (s *Server) compWebsite(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.WebsiteLists(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) compWebsitePublished(w http.ResponseWriter, r *http.Request) {
	var b struct {
		List string `json:"list"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkWebsitePublished(r.Context(), tenant(r), actor(r), b.List)
	ok(w, v, err)
}

func (s *Server) compAPNotice(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Branch    string `json:"branch"`
		OldAP     string `json:"old_ap"`
		NewAP     string `json:"new_ap"`
		Effective string `json:"effective_on"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.NotifyAPChange(r.Context(), tenant(r), actor(r), b.Branch, b.OldAP, b.NewAP, b.Effective)
	ok(w, v, err)
}

func (s *Server) compPropNotice(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Starts string `json:"starts_on"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SendPropTradingNotice(r.Context(), tenant(r), actor(r), b.Starts)
	ok(w, v, err)
}

func (s *Server) compAPStatus(w http.ResponseWriter, r *http.Request) {
	var b ops.APStatus
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetAPStatus(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) compTermTurnover(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.TerminalTurnoverReport(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) compTermTurnoverCSV(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.TerminalTurnoverCSV(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, "terminal_turnover.csv", "text/csv", b)
}

func (s *Server) compNSEUsers(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.NSEUsers(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) compNSEUserSet(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Terminal string `json:"terminal"`
		Type     string `json:"type"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetNSEUserType(r.Context(), tenant(r), actor(r), b.Terminal, b.Type)
	ok(w, v, err)
}

func (s *Server) compTDSLower(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	b, err := s.ops.TDSLowerCSV(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, "tds_lower_deduction.csv", "text/csv", b)
}

// compGSTR2B takes the GSTR-2B file as the body (?period=YYYY-MM&name=).
func (s *Server) compGSTR2B(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, ops.MaxXFileBytes+1))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	q := r.URL.Query()
	v, err := s.ops.LoadGSTR2B(r.Context(), tenant(r), actor(r), q.Get("period"), q.Get("name"), data)
	ok(w, v, err)
}

func (s *Server) compITC(w http.ResponseWriter, r *http.Request) {
	ctx, t, a := r.Context(), tenant(r), actor(r)
	m, err := s.ops.GSTR2BMatch(ctx, t, a)
	if err != nil {
		fail(w, err, nil)
		return
	}
	u, err := s.ops.ITCUnpaid180(ctx, t, a, r.URL.Query().Get("date"))
	ok(w, map[string]any{"match": m, "unpaid_180": u}, err)
}

func (s *Server) compGSTNotes(w http.ResponseWriter, r *http.Request) {
	var b struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.IssueGSTNotes(r.Context(), tenant(r), actor(r), b.From, b.To)
	ok(w, map[string]any{"issued": n}, err)
}

func (s *Server) compPassThrough(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.DPPassThrough(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

// compToBeClosed takes CDSL's list as the body (?date=&name=).
func (s *Server) compToBeClosed(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, ops.MaxXFileBytes+1))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	q := r.URL.Query()
	v, err := s.ops.LoadToBeClosed(r.Context(), tenant(r), actor(r), q.Get("date"), q.Get("name"), data)
	ok(w, v, err)
}

func (s *Server) compROKit(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC string `json:"ucc"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SendROKit(r.Context(), tenant(r), actor(r), b.UCC)
	ok(w, v, err)
}

func (s *Server) compCML(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC     string `json:"ucc"`
		BOID    string `json:"bo_id"`
		Purpose string `json:"purpose"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.IssueAttestedCML(r.Context(), tenant(r), actor(r), b.UCC, b.BOID, b.Purpose)
	ok(w, v, err)
}

func (s *Server) compDISDelay(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordDISDelayReason(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Reason)
	ok(w, v, err)
}

func (s *Server) compTicketExchange(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Exchange string `json:"exchange"`
		Due      string `json:"exchange_due"`
		Claim    string `json:"claim"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetTicketExchange(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Exchange, b.Due, b.Claim)
	ok(w, v, err)
}
