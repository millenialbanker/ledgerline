package api

import (
	"net/http"

	"ledgerline/internal/money"
	"ledgerline/internal/ops"
)

// icclRoutes: the BSE Clearing (ICCL) day for a firm that clears its own
// trades there (ops/iccl.go). ICCL's files themselves are downloaded from
// the exchange files download route like every other outgoing file.
func (s *Server) icclRoutes() {
	s.route("GET /v1/iccl", s.icclGet, false)
	s.route("POST /v1/iccl/short-reasons", s.icclReason, true)
}

func (s *Server) icclGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ICCLDay(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) icclReason(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date     string    `json:"date"`
		Snapshot string    `json:"snapshot"`
		Segment  string    `json:"segment"`
		UCC      string    `json:"ucc"`
		Code     string    `json:"code"`
		Amount   money.Dec `json:"amount"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveICCLReason(r.Context(), tenant(r), actor(r), ops.ICCLReason{Date: b.Date, Snapshot: b.Snapshot, Seg: b.Segment, UCC: b.UCC, Code: b.Code, Amount: b.Amount})
	ok(w, v, err)
}
