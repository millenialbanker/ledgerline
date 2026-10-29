package api

import (
	"io"
	"net/http"
	"strings"
	"sync"

	"ledgerline/internal/money"
	"ledgerline/internal/ops"
)

// ipoRouted makes ipoRoutes register once per server, however it is wired.
var ipoRouted sync.Map

// ipoRoutes: public issues (IPOs, SME IPOs, NCDs, offers for sale): the
// issue master, bids with the client's instruction, files and the NSE eIPO
// API, the mandate tracker, allotment reconciliation, the client portal
// page, and NSE's eIPO callback. GET /v1/ipo (opsRoutes) and POST
// /v1/ipo/allotments (periodicRoutes) stay where they are.
func (s *Server) ipoRoutes() {
	if s.ops == nil {
		return
	}
	if _, done := ipoRouted.LoadOrStore(s, true); done {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	// Branch staff and authorised persons take bids for their own clients.
	B := func(p string, h http.HandlerFunc, m bool) { scopedRoutes[p] = true; s.route(p, h, m) }
	// The client portal.
	C := func(p string, h http.HandlerFunc, m bool) { clientRoutes[p] = true; s.route(p, h, m) }

	B("GET /v1/ipo/issues", s.ipoIssues, false)
	R("POST /v1/ipo/issues", s.ipoSaveIssue, true)
	R("POST /v1/ipo/issues/import", s.ipoImportIssues, true)
	B("GET /v1/ipo/issues/{id}", s.ipoIssue, false)
	B("GET /v1/ipo/bids", s.ipoBidList, false)
	B("POST /v1/ipo/bids", s.ipoPlace, true)
	B("GET /v1/ipo/bids/{id}", s.ipoBid, false)
	B("POST /v1/ipo/bids/{id}/modify", s.ipoModify, true)
	B("POST /v1/ipo/bids/{id}/cancel", s.ipoCancel, true)
	B("POST /v1/ipo/bids/{id}/resend-mandate", s.ipoResend, true)
	B("POST /v1/ipo/bids/{id}/complaint", s.ipoComplaint, true)
	R("POST /v1/ipo/bids/{id}/status", s.ipoStatus, true)
	R("POST /v1/ipo/bids/{id}/unblock", s.ipoUnblock, true)
	R("POST /v1/ipo/bids/{id}/compensation", s.ipoCompensation, true)
	B("GET /v1/ipo/upi/{ucc}", s.ipoUPIs, false)
	B("POST /v1/ipo/upi/{ucc}", s.ipoUPIAdd, true)
	B("POST /v1/ipo/upi/{ucc}/remove", s.ipoUPIRemove, true)
	B("GET /v1/ipo/instructions", s.ipoInstructions, false)
	R("GET /v1/ipo/instructions.csv", s.ipoInstructionsCSV, false)
	B("GET /v1/ipo/mandates", s.ipoMandates, false)
	R("POST /v1/ipo/issues/{id}/bid-file", s.ipoBidFile, true)
	R("GET /v1/ipo/batches", s.ipoBatches, false)
	R("GET /v1/ipo/batches/{id}/file", s.ipoBatchFile, false)
	R("POST /v1/ipo/responses", s.ipoResponses, true)
	R("POST /v1/ipo/issues/{id}/allotment", s.ipoAllot, true)
	R("POST /v1/ipo/issues/{id}/allotment-file", s.ipoAllotFile, true)
	R("GET /v1/ipo/issues/{id}/reconciliation", s.ipoRecon, false)
	R("POST /v1/ipo/issues/{id}/credits", s.ipoCredits, true)
	R("POST /v1/ipo/issues/{id}/post-allotments", s.ipoPostAllot, true)
	R("POST /v1/ipo/issues/{id}/commission", s.ipoCommission, true)
	R("POST /v1/ipo/issues/{id}/ofs-allocation", s.ipoOFS, true)
	R("GET /v1/ipo/eipo", s.eipoStatus, false)
	R("POST /v1/ipo/issues/{id}/eipo-send", s.eipoSend, true)
	R("POST /v1/ipo/issues/{id}/eipo-inquire", s.eipoInquire, true)
	C("GET /v1/portal/ipo", s.portalIPO, false)
	C("POST /v1/portal/ipo/bids", s.portalIPOBid, true)
	C("POST /v1/portal/ipo/bids/{id}/modify", s.portalIPOModify, true)
	C("POST /v1/portal/ipo/bids/{id}/cancel", s.portalIPOCancel, true)
	// NSE's eIPO calls back here (not signed in: the token in the address
	// registered with NSE, and optionally its listed addresses, admit it).
	s.mux.HandleFunc("POST /hooks/{tenant}/eipo", s.eipoHook)
}

func (s *Server) ipoIssues(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.IPOIssues(r.Context(), tenant(r))
	ok(w, map[string]any{"issues": v, "types": ops.IssueTypes, "defaults": map[string]any{
		ops.IssueMainboard: ops.DefaultIPOCategories(ops.IssueMainboard), ops.IssueSME: ops.DefaultIPOCategories(ops.IssueSME),
		ops.IssueNCD: ops.DefaultIPOCategories(ops.IssueNCD), ops.IssueOFS: ops.DefaultIPOCategories(ops.IssueOFS)}}, err)
}

func (s *Server) ipoSaveIssue(w http.ResponseWriter, r *http.Request) {
	var b ops.IPOIssue
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveIPOIssue(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) ipoImportIssues(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBytes))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	v, err := s.ops.ImportIPOIssues(r.Context(), tenant(r), actor(r), data)
	ok(w, v, err)
}

func (s *Server) ipoIssue(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.IPOIssueByID(r.Context(), tenant(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) ipoBidList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.IPOBids(r.Context(), tenant(r), actor(r), q.Get("issue"), q.Get("status"))
	ok(w, v, err)
}

func (s *Server) ipoPlace(w http.ResponseWriter, r *http.Request) {
	var b ops.IPOBidInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PlaceIPOBid(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) ipoBid(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.IPOBidByID(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) ipoModify(w http.ResponseWriter, r *http.Request) {
	var b ops.IPOModifyInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ModifyIPOBid(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) ipoCancel(w http.ResponseWriter, r *http.Request) {
	var b ops.IPOInstructionIn
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CancelIPOBid(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) ipoResend(w http.ResponseWriter, r *http.Request) {
	var b ops.IPOInstructionIn
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ResendIPOMandate(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) ipoComplaint(w http.ResponseWriter, r *http.Request) {
	var b struct{ Source, Ref, Text string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.IPOComplaint(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Source, b.Ref, b.Text)
	ok(w, v, err)
}

func (s *Server) ipoStatus(w http.ResponseWriter, r *http.Request) {
	var b ops.IPOStatusUpdate
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordIPOStatus(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) ipoUnblock(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date   string       `json:"date"`
		Amount money.Amount `json:"amount"`
		Note   string       `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordIPOUnblock(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.Amount, b.Note)
	ok(w, v, err)
}

func (s *Server) ipoCompensation(w http.ResponseWriter, r *http.Request) {
	var b ops.IPOComp
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordIPOCompensation(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) ipoUPIs(w http.ResponseWriter, r *http.Request) {
	if !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	v, err := s.ops.ClientUPIs(r.Context(), tenant(r), r.PathValue("ucc"))
	ok(w, v, err)
}

func (s *Server) ipoUPIAdd(w http.ResponseWriter, r *http.Request) {
	var b struct{ VPA, Evidence string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RegisterIPOUPI(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.VPA, b.Evidence)
	ok(w, v, err)
}

func (s *Server) ipoUPIRemove(w http.ResponseWriter, r *http.Request) {
	var b struct{ VPA string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RemoveIPOUPI(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.VPA)
	ok(w, v, err)
}

func (s *Server) ipoInstructions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.IPOInstructions(r.Context(), tenant(r), actor(r), q.Get("ucc"), q.Get("bid"), q.Get("from"), q.Get("to"))
	ok(w, v, err)
}

func (s *Server) ipoInstructionsCSV(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	b, err := s.ops.IPOInstructionsCSV(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ipo_consent_log.csv"`)
	w.Write(b)
}

func (s *Server) ipoMandates(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.IPOMandates(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) ipoBidFile(w http.ResponseWriter, r *http.Request) {
	var b struct{ Exchange string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.IPOBidFile(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Exchange)
	ok(w, v, err)
}

func (s *Server) ipoBatches(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.IPOBatches(r.Context(), tenant(r), r.URL.Query().Get("issue"))
	ok(w, v, err)
}

func (s *Server) ipoBatchFile(w http.ResponseWriter, r *http.Request) {
	name, b, err := s.ops.IPOBatchFile(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.NewReplacer(`"`, "", "\\", "").Replace(name)+`"`)
	w.Write(b)
}

// ipoResponses loads an exchange's response file through the exchange file
// loader (recognised by name and header, kept, and applied to the bids).
func (s *Server) ipoResponses(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, ops.MaxXFileBytes+1))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	v, err := s.ops.LoadExchangeFile(r.Context(), tenant(r), actor(r), r.URL.Query().Get("name"), data, true)
	ok(w, v, err)
}

func (s *Server) ipoAllot(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string           `json:"date"`
		Lots map[string]int64 `json:"lots"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordIPOAllotment(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.Lots)
	ok(w, v, err)
}

func (s *Server) ipoAllotFile(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxFileBytes))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	v, err := s.ops.ImportIPOAllotment(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.URL.Query().Get("date"), data)
	ok(w, v, err)
}

func (s *Server) ipoRecon(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.IPOReconciliation(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) ipoCredits(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r, "Recording allotments", ops.RoleMaker, ops.RoleChecker) {
		return
	}
	n, err := s.ops.SyncIPOCredits(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]int{"credited": n}, err)
}

func (s *Server) ipoPostAllot(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PostIssueAllotments(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date)
	ok(w, v, err)
}

func (s *Server) ipoCommission(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Action   string       `json:"action"` // claim or received
		Date     string       `json:"date"`
		Ref      string       `json:"ref"`
		Received money.Amount `json:"received"`
		TDS      money.Amount `json:"tds"`
	}
	if !decode(w, r, &b) {
		return
	}
	var v ops.IPOIssue
	var err error
	switch b.Action {
	case "claim":
		v, err = s.ops.ClaimIPOCommission(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.Ref)
	case "received":
		v, err = s.ops.ReceiveIPOCommission(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.Received, b.TDS, b.Ref)
	default:
		writeErr(w, 400, "bad_action", "action is claim or received", nil)
		return
	}
	ok(w, v, err)
}

func (s *Server) ipoOFS(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date        string              `json:"date"`
		Allocations []ops.OFSAllocation `json:"allocations"`
		AddTrades   bool                `json:"add_trades"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordOFSAllocation(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.Allocations, b.AddTrades)
	ok(w, v, err)
}

func (s *Server) eipoStatus(w http.ResponseWriter, r *http.Request) {
	ok(w, s.ops.EIPOStatus(r.Context(), tenant(r)), nil)
}

func (s *Server) eipoSend(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.EIPOSend(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) eipoInquire(w http.ResponseWriter, r *http.Request) {
	n, err := s.ops.EIPOInquire(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]int{"updated": n}, err)
}

func (s *Server) portalIPO(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PortalIPO(r.Context(), tenant(r), actor(r), r.URL.Query().Get("ucc"))
	ok(w, v, err)
}

func (s *Server) portalIPOBid(w http.ResponseWriter, r *http.Request) {
	var b ops.IPOBidInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ClientPlaceIPOBid(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) portalIPOModify(w http.ResponseWriter, r *http.Request) {
	var b ops.IPOModifyInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ClientModifyIPOBid(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) portalIPOCancel(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ClientCancelIPOBid(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

// eipoHook receives NSE's eIPO callback.
func (s *Server) eipoHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the body", nil)
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("X-Callback-Token")
	}
	res, err := s.ops.EIPOCallback(r.Context(), r.PathValue("tenant"), token, r.RemoteAddr, body)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, map[string]any{"status": "success", "received": res.Received, "applied": res.Applied})
}
