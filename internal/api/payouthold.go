package api

import (
	"net/http"
	"strings"
	"sync"

	"ledgerline/internal/ops"
)

var payoutHoldRouted sync.Map // *Server -> registered

// payoutHoldRoutes: holding back big sellers' sale money from allocation
// and the OMS limit until the clearing corporation's funds pay-out
// (internal/ops/payouthold.go).
func (s *Server) payoutHoldRoutes() {
	if s.ops == nil {
		return
	}
	if _, dup := payoutHoldRouted.LoadOrStore(s, true); dup {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/payout-holds", s.phScreen, false)
	R("GET /v1/payout-holds/settings", s.phSettingsGet, false)
	R("PUT /v1/payout-holds/settings", s.phSettingsPut, true)
	R("POST /v1/payout-holds/apply", s.phApply, true)
	R("POST /v1/payout-holds/funds", s.phFunds, true)
	R("POST /v1/payout-holds/payout", s.phPayout, true)
	R("POST /v1/payout-holds/nmass-done", s.phNMASSDone, true)
	R("POST /v1/payout-holds/holds/{id}/decide", s.phDecide, true)
	R("GET /v1/payout-holds/file", s.phFile, false)
}

func (s *Server) phScreen(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.PayoutHoldScreen(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) phSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.ops.PayoutHoldSettings(r.Context(), tenant(r)))
}

func (s *Server) phSettingsPut(w http.ResponseWriter, r *http.Request) {
	var b ops.PayoutHoldSettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SavePayoutHoldSettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

type phDateBody struct {
	Date string `json:"date"`
}

func (s *Server) phApply(w http.ResponseWriter, r *http.Request) {
	var b phDateBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ApplyPayoutHolds(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, v, err)
}

func (s *Server) phFunds(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date     string            `json:"date"`
		Segments map[string]string `json:"segments"`
		Note     string            `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetPayoutFunds(r.Context(), tenant(r), actor(r), b.Date, b.Segments, b.Note)
	ok(w, v, err)
}

func (s *Server) phPayout(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date        string   `json:"date"`
		Settlements []string `json:"settlements"`
		Note        string   `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PayoutReceived(r.Context(), tenant(r), actor(r), b.Date, b.Settlements, b.Note)
	ok(w, v, err)
}

func (s *Server) phNMASSDone(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string   `json:"date"`
		IDs  []string `json:"ids"`
		Note string   `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PayoutHoldDoneInNMASS(r.Context(), tenant(r), actor(r), b.Date, b.IDs, b.Note)
	ok(w, v, err)
}

func (s *Server) phDecide(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Amount  string `json:"amount"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecidePayoutHold(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Amount, b.Note)
	ok(w, v, err)
}

func (s *Server) phFile(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	name, b, err := s.ops.PayoutHoldFile(r.Context(), tenant(r), actor(r), d, r.URL.Query().Get("kind"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	ct := "text/csv; charset=utf-8"
	if !strings.HasSuffix(name, ".csv") {
		ct = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.NewReplacer(`"`, "", "\\", "").Replace(name)+`"`)
	w.Write(b)
}
