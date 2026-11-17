package api

import (
	"net/http"
	"strconv"

	"ledgerline/internal/money"
	"ledgerline/internal/ops"
)

// clientMoneyRoutes: the exchanges' UCC-wise report of each running account
// settlement (Running account), the firm's bank account register, the
// transfers between the firm's accounts and the daily client money
// reconciliation statement. Registered from exchangeReportingRoutes.
func (s *Server) clientMoneyRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/ras/ucc-reports", s.rasUCCList, false)
	R("GET /v1/ras/ucc-reports/{date}", s.rasUCCDetail, false)
	R("POST /v1/ras/ucc-reports/{date}/file", s.rasUCCPrepare, true)
	R("GET /v1/ras/ucc-reports/{date}/file", s.rasUCCFile, false)
	R("POST /v1/ras/ucc-reports/{date}/filed", s.rasUCCFiled, true)
	R("GET /v1/firm-banks", s.firmBanks, false)
	R("POST /v1/firm-banks", s.saveFirmBank, true)
	R("GET /v1/money-transfers", s.moneyTransfers, false)
	R("POST /v1/money-transfers", s.recordMoneyTransfer, true)
	R("POST /v1/money-transfers/{id}/decide", s.decideMoneyTransfer, true)
	R("GET /v1/money-recon", s.moneyRecon, false)
	R("POST /v1/money-recon/{date}/prepare", s.moneyReconPrepare, true)
	R("POST /v1/money-recon/{date}/sign", s.moneyReconSign, true)
	R("POST /v1/money-recon/{date}/return", s.moneyReconReturn, true)
	R("GET /v1/money-recon/{date}/file", s.moneyReconFile, false)
	R("GET /v1/money-recon/{date}/document", s.moneyReconDoc, false)
}

// ---- the UCC-wise settlement report ----

func (s *Server) rasUCCList(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RASUCCReports(r.Context(), tenant(r), actor(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) rasUCCDetail(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RASUCCReport(r.Context(), tenant(r), actor(r), r.PathValue("date"))
	ok(w, v, err)
}

func (s *Server) rasUCCPrepare(w http.ResponseWriter, r *http.Request) {
	var b struct {
		New bool `json:"new"` // a new file (the next sequence number) even when nothing changed
	}
	if r.ContentLength != 0 && !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PrepareRASUCC(r.Context(), tenant(r), actor(r), r.PathValue("date"), b.New)
	ok(w, v, err)
}

func (s *Server) rasUCCFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	seq, _ := strconv.Atoi(q.Get("seq"))
	name, b, err := s.ops.RASUCCFileData(r.Context(), tenant(r), actor(r), r.PathValue("date"), seq, q.Get("csv") == "1")
	if err != nil {
		fail(w, err, nil)
		return
	}
	ct := "application/zip"
	if q.Get("csv") == "1" {
		ct = "text/csv; charset=utf-8"
	}
	sendFile(w, name, ct, b)
}

func (s *Server) rasUCCFiled(w http.ResponseWriter, r *http.Request) {
	var b ops.RASUCCFileReq
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordRASUCC(r.Context(), tenant(r), actor(r), r.PathValue("date"), b)
	ok(w, v, err)
}

// ---- the firm's bank accounts ----

func (s *Server) firmBanks(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FirmBanks(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) saveFirmBank(w http.ResponseWriter, r *http.Request) {
	var b ops.FirmBankReq
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveFirmBank(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

// ---- transfers between the firm's accounts ----

func (s *Server) moneyTransfers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.FirmTransfers(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
	ok(w, map[string]any{"transfers": v, "purposes": ops.MoneyPurposes}, err)
}

func (s *Server) recordMoneyTransfer(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date    string       `json:"date"`
		Purpose string       `json:"purpose"`
		From    string       `json:"from"`
		To      string       `json:"to"`
		Segment string       `json:"segment"`
		Amount  money.Amount `json:"amount"`
		Ref     string       `json:"ref"`
		Note    string       `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordFirmTransfer(r.Context(), tenant(r), actor(r), ops.FirmTransfer{Date: b.Date, Purpose: b.Purpose, From: b.From, To: b.To, Segment: b.Segment, Amount: b.Amount, Ref: b.Ref, Note: b.Note})
	ok(w, v, err)
}

func (s *Server) decideMoneyTransfer(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideFirmTransfer(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Reason)
	ok(w, v, err)
}

// ---- the daily reconciliation statement ----

func (s *Server) moneyRecon(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.MoneyReconFor(r.Context(), tenant(r), actor(r), d)
	ok(w, v, err)
}

type moneyReconNote struct {
	Note   string `json:"note"`
	Reason string `json:"reason"`
}

func (s *Server) moneyReconPrepare(w http.ResponseWriter, r *http.Request) {
	var b moneyReconNote
	if r.ContentLength != 0 && !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PrepareMoneyRecon(r.Context(), tenant(r), actor(r), r.PathValue("date"), b.Note)
	ok(w, v, err)
}

func (s *Server) moneyReconSign(w http.ResponseWriter, r *http.Request) {
	var b moneyReconNote
	if r.ContentLength != 0 && !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SignMoneyRecon(r.Context(), tenant(r), actor(r), r.PathValue("date"), b.Note)
	ok(w, v, err)
}

func (s *Server) moneyReconReturn(w http.ResponseWriter, r *http.Request) {
	var b moneyReconNote
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReturnMoneyRecon(r.Context(), tenant(r), actor(r), r.PathValue("date"), b.Reason)
	ok(w, v, err)
}

func (s *Server) moneyReconFile(w http.ResponseWriter, r *http.Request) {
	name, b, err := s.ops.MoneyReconFile(r.Context(), tenant(r), actor(r), r.PathValue("date"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	sendFile(w, name, "text/csv; charset=utf-8", b)
}

func (s *Server) moneyReconDoc(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.RenderMoneyRecon(r.Context(), tenant(r), actor(r), r.PathValue("date"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Write(b)
}
