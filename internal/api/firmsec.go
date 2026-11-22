package api

import (
	"net/http"

	"ledgerline/internal/ops"
)

// firmSecRoutes: Accounts → Firm securities (ops/firmsec*.go): the firm's
// own investments and stock-in-trade, their register, stock journal,
// valuation, reports, pledges, dividends and the demat check. The
// choices themselves are Firm practices questions (firmpractices.go).
func (s *Server) firmSecRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/firm-securities", s.fsHoldings, false)
	R("POST /v1/firm-securities/kinds", s.fsKind, true)
	R("GET /v1/firm-securities/register", s.fsRegister, false)
	R("POST /v1/firm-securities/sync", s.fsSync, true)
	R("POST /v1/firm-securities/entries", s.fsEntry, true)
	R("POST /v1/firm-securities/entries/{id}/change", s.fsChange, true)
	R("POST /v1/firm-securities/entries/{id}/decide", s.fsDecide, true)
	R("POST /v1/firm-securities/opening/post", s.fsOpening, true)
	R("GET /v1/firm-securities/journals", s.fsJournals, false)
	R("POST /v1/firm-securities/journals", s.fsJournalAdd, true)
	R("POST /v1/firm-securities/journals/{id}/decide", s.fsJournalDecide, true)
	R("GET /v1/firm-securities/valuation", s.fsValuation, false)
	R("POST /v1/firm-securities/valuation", s.fsValuationPropose, true)
	R("POST /v1/firm-securities/valuation/{id}/decide", s.fsValuationDecide, true)
	R("GET /v1/firm-securities/reports", s.fsReportDefs, false)
	R("GET /v1/firm-securities/reports/{kind}", s.fsReport, false)
	R("GET /v1/firm-securities/net-worth", s.fsNetWorth, false)
	R("POST /v1/firm-securities/net-worth", s.fsNetWorthInputs, true)
	R("POST /v1/firm-securities/pledges", s.fsPledge, true)
	R("POST /v1/firm-securities/pledges/{id}/close", s.fsPledgeClose, true)
	R("POST /v1/firm-securities/income", s.fsIncome, true)
	R("POST /v1/firm-securities/income/match", s.fsIncomeMatch, true)
	R("GET /v1/firm-securities/recon", s.fsRecon, false)
	R("POST /v1/firm-securities/demat", s.fsDemat, true)
}

func (s *Server) fsHoldings(w http.ResponseWriter, r *http.Request) {
	d, good := dateParam(w, r.URL.Query().Get("date"), "date", true)
	if !good {
		return
	}
	v, err := s.ops.FirmSecHoldings(r.Context(), tenant(r), actor(r), d)
	ok(w, v, err)
}

// fsKind: {"id", "category", "tax", "listed"}: how one of the firm's own
// instrument types is treated.
func (s *Server) fsKind(w http.ResponseWriter, r *http.Request) {
	var b ops.FSKind
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetFSKind(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) fsRegister(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.FirmSecRegister(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
	ok(w, v, err)
}

// fsSync: {"date"}: brings in the day's trades under the firm's own codes.
func (s *Server) fsSync(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.FirmSecSync(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, v, err)
}

func (s *Server) fsEntry(w http.ResponseWriter, r *http.Request) {
	var b ops.FSEntryInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AddFirmSecEntry(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

// fsChange: {"portfolio", "kind", "term"}: a trade-file line before the
// day's close.
func (s *Server) fsChange(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Portfolio string `json:"portfolio"`
		Kind      string `json:"kind"`
		Term      string `json:"term"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.FirmSecChange(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Portfolio, b.Kind, b.Term)
	ok(w, v, err)
}

// fsDecide: {"action": "approve"|"reject"|"cancel", "reason"}.
func (s *Server) fsDecide(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideFirmSecEntry(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Action, b.Reason)
	ok(w, v, err)
}

// fsOpening: {"counter"}: posts the opening lots loaded (FIRM-LOTS).
func (s *Server) fsOpening(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Counter string `json:"counter"`
	}
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.PostFirmSecOpening(r.Context(), tenant(r), actor(r), b.Counter)
	ok(w, map[string]any{"posted": n}, err)
}

func (s *Server) fsJournals(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FirmSecJournals(r.Context(), tenant(r), actor(r))
	ok(w, map[string]any{"journals": v}, err)
}

func (s *Server) fsJournalAdd(w http.ResponseWriter, r *http.Request) {
	var b ops.FSJournalInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AddFirmSecJournal(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

type fsDecision struct {
	Approve bool   `json:"approve"`
	Note    string `json:"note"`
}

func (s *Server) fsJournalDecide(w http.ResponseWriter, r *http.Request) {
	var b fsDecision
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideFirmSecJournal(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Note)
	ok(w, v, err)
}

func (s *Server) fsValuation(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d, good := dateParam(w, q.Get("date"), "date", true)
	if !good {
		return
	}
	v, err := s.ops.FirmSecValuation(r.Context(), tenant(r), actor(r), d, q.Get("portfolio"))
	ok(w, v, err)
}

// fsValuationPropose: {"date", "portfolio"}.
func (s *Server) fsValuationPropose(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date      string `json:"date"`
		Portfolio string `json:"portfolio"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ProposeFirmSecValuation(r.Context(), tenant(r), actor(r), b.Date, b.Portfolio)
	ok(w, v, err)
}

func (s *Server) fsValuationDecide(w http.ResponseWriter, r *http.Request) {
	var b fsDecision
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideFirmSecValuation(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Note)
	ok(w, v, err)
}

func (s *Server) fsReportDefs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"reports": ops.FSReportDefs})
}

// fsReport: ?portfolio=&from=&to=&heads=1&format=csv|html.
func (s *Server) fsReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rep, err := s.ops.FirmSecReport(r.Context(), tenant(r), actor(r), r.PathValue("kind"), q.Get("portfolio"), q.Get("from"), q.Get("to"), q.Get("heads") == "1")
	if err != nil {
		fail(w, err, nil)
		return
	}
	switch q.Get("format") {
	case "csv":
		s.app.Log(r.Context(), tenant(r), actor(r), "firm_securities.report_downloaded", rep.Kind, map[string]any{"period": rep.Period})
		sendFile(w, rep.FileName("csv"), "text/csv; charset=utf-8", rep.CSV())
	case "html":
		b, err := s.ops.RenderClientReport(r.Context(), tenant(r), rep)
		if err != nil {
			fail(w, err, nil)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		w.Write(b)
	default:
		writeJSON(w, 200, rep)
	}
}

func (s *Server) fsNetWorth(w http.ResponseWriter, r *http.Request) {
	d, good := dateParam(w, r.URL.Query().Get("date"), "date", true)
	if !good {
		return
	}
	v, err := s.ops.FirmSecNetWorth(r.Context(), tenant(r), actor(r), d)
	ok(w, v, err)
}

func (s *Server) fsNetWorthInputs(w http.ResponseWriter, r *http.Request) {
	var b ops.FSNetWorthInputs
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveFirmSecNetWorthInputs(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) fsPledge(w http.ResponseWriter, r *http.Request) {
	var b ops.FSPledgeInput
	if !decode(w, r, &b) {
		return
	}
	p, warn, err := s.ops.AddFirmSecPledge(r.Context(), tenant(r), actor(r), b)
	if warn == nil {
		warn = []string{}
	}
	ok(w, map[string]any{"pledge": p, "warnings": warn}, err)
}

// fsPledgeClose: {"action": "release"|"invoke", "date", "price"}.
func (s *Server) fsPledgeClose(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Action string `json:"action"`
		Date   string `json:"date"`
		Price  string `json:"price"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CloseFirmSecPledge(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Action, b.Date, b.Price)
	ok(w, v, err)
}

func (s *Server) fsIncome(w http.ResponseWriter, r *http.Request) {
	var b ops.FSIncomeInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AddFirmSecIncome(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

// fsIncomeMatch: {"bank": "BANK:OWN", "credits": [{date, amount, ref, text}]}.
func (s *Server) fsIncomeMatch(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Bank    string             `json:"bank"`
		Credits []ops.FSBankCredit `json:"credits"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.FirmSecMatchIncome(r.Context(), tenant(r), actor(r), b.Bank, b.Credits)
	ok(w, v, err)
}

func (s *Server) fsRecon(w http.ResponseWriter, r *http.Request) {
	d, good := dateParam(w, r.URL.Query().Get("date"), "date", true)
	if !good {
		return
	}
	v, err := s.ops.FirmSecRecon(r.Context(), tenant(r), actor(r), d)
	ok(w, v, err)
}

func (s *Server) fsDemat(w http.ResponseWriter, r *http.Request) {
	var b ops.FSDematStatement
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveFirmSecDemat(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}
