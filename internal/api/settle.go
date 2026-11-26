package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// settleRoutes: internal shortages and their noon funding, the pay-out
// reconciliation, securities transfer requests, account closures and the
// allocation's acceptance by 22:00 (internal/ops/nclshortage.go,
// payoutrecon.go, acctclose.go, settlecheck.go). All under /v1/settlement,
// so they open with the Settlement screen's roles.
func (s *Server) settleRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/settlement/internal-shortages", s.internalShortages, false)
	R("GET /v1/settlement/payout-recon", s.payoutRecon, false)
	R("GET /v1/settlement/allocation-accepted", s.allocationAccepted, false)
	R("GET /v1/settlement/transfers", s.secTransfers, false)
	R("POST /v1/settlement/transfers", s.requestSecTransfer, true)
	R("POST /v1/settlement/transfers/{id}/finish", s.finishSecTransfer, true)
	R("GET /v1/settlement/closures", s.closures, false)
	R("POST /v1/settlement/closures", s.requestClosure, true)
	R("POST /v1/settlement/closures/{id}/check", s.checkClosure, true)
	R("POST /v1/settlement/closures/{id}/complete", s.completeClosure, true)
	R("POST /v1/settlement/closures/{id}/reject", s.rejectClosure, true)
}

func (s *Server) internalShortages(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.InternalShortages(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) payoutRecon(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.ReconcilePayout(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) allocationAccepted(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	why := s.ops.AllocationNotAccepted(r.Context(), tenant(r), d)
	if why == nil {
		why = []string{}
	}
	writeJSON(w, 200, map[string]any{"date": d, "accepted": len(why) == 0, "why": why})
}

func (s *Server) secTransfers(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SecTransfers(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) requestSecTransfer(w http.ResponseWriter, r *http.Request) {
	var b ops.SecTransferInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RequestSecTransfer(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) finishSecTransfer(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Done   bool   `json:"done"`
		Ref    string `json:"ref"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.FinishSecTransfer(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Done, b.Ref, b.Reason)
	ok(w, v, err)
}

func (s *Server) closures(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Closures(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) requestClosure(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC      string `json:"ucc"`
		Received string `json:"received"`
		Source   string `json:"source"`
		Note     string `json:"note"`
		From     string `json:"from_email"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RequestClosureFrom(r.Context(), tenant(r), actor(r), b.UCC, b.Received, b.Source, b.Note, b.From)
	ok(w, v, err)
}

func (s *Server) checkClosure(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CheckClosure(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) completeClosure(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CompleteClosure(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) rejectClosure(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RejectClosure(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Reason)
	ok(w, v, err)
}
