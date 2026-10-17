package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/ops"
	"ledgerline/internal/sample"
)

// allowed checks the actor's role, writing a 403 when it fails.
func (s *Server) allowed(w http.ResponseWriter, r *http.Request, what string, roles ...string) bool {
	if s.ops == nil {
		return true
	}
	if err := s.ops.Require(r.Context(), tenant(r), actor(r), what, roles...); err != nil {
		fail(w, err, nil)
		return false
	}
	return true
}

func qdate(w http.ResponseWriter, r *http.Request) (string, bool) {
	return dateParam(w, r.URL.Query().Get("date"), "date", true)
}

func (s *Server) opsRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/me", s.me, false)
	R("GET /v1/users", s.users, false)
	R("PUT /v1/users/{id}", s.putUser, true)
	R("GET /v1/settings", s.settings, false)
	R("PUT /v1/settings", s.putSettings, true)
	R("GET /v1/file-types", s.fileTypes, false)
	R("GET /v1/dashboard", s.dashboard, false)
	R("GET /v1/clients", s.clients, false)
	R("POST /v1/clients", s.createClient, true)
	R("GET /v1/clients/{ucc}", s.client, false)
	R("POST /v1/clients/{ucc}/advance", s.advance, true)
	R("POST /v1/clients/{ucc}/block", s.block, true)
	R("GET /v1/market", s.market, false)
	R("GET /v1/collateral", s.collateral, false)
	R("GET /v1/pledges", s.pledges, false)
	R("POST /v1/pledges/{id}/decide", s.decidePledge, true)
	R("GET /v1/rms", s.rms, false)
	R("POST /v1/rms/overrides", s.createOverride, true)
	R("POST /v1/rms/overrides/{id}/decide", s.decideOverride, true)
	R("POST /v1/rms/squareoff", s.squareOff, true)
	R("GET /v1/margin", s.margin, false)
	R("POST /v1/margin/penalties", s.penalties, true)
	R("GET /v1/settlement", s.settlement, false)
	R("POST /v1/settlement/action", s.settleAction, true)
	R("POST /v1/settlement/settle-ready", s.settleReady, true)
	R("POST /v1/cuspa/{id}/release", s.releaseCUSPA, true)
	R("GET /v1/notes", s.notes, false)
	R("POST /v1/notes/generate", s.generateNotes, true)
	R("POST /v1/notes/send", s.sendNotes, true)
	R("GET /v1/notes/{date}/{ucc}/document", s.noteDocument, false)
	R("GET /v1/bank", s.bank, false)
	R("POST /v1/bank/{id}/return", s.returnCredit, true)
	R("POST /v1/bank/{id}/identify", s.identifyCredit, true)
	R("POST /v1/bank/upstream", s.upstream, true)
	R("GET /v1/funds", s.funds, false)
	R("GET /v1/ras", s.ras, false)
	R("POST /v1/ras/run", s.runRAS, true)
	R("GET /v1/ras/payout-file", s.rasFile, false)
	R("GET /v1/reports", s.reports, false)
	R("POST /v1/reports/{code}/{step}", s.reportStep, true)
	R("GET /v1/reports/{code}/download", s.reportDownload, false)
	R("GET /v1/client-reports", s.clientReportDefs, false)
	R("GET /v1/client-reports/{kind}", s.clientReport, false)
	R("POST /v1/billing/correct", s.correctBill, true)
	R("GET /v1/receipts", s.receipts, false)
	R("POST /v1/receipts", s.recordReceipt, true)
	R("POST /v1/receipts/{id}/decide", s.decideReceipt, true)
	R("POST /v1/receipts/{id}/clear", s.clearCheque, true)
	R("POST /v1/gateway/settle", s.settleGateway, true)
	s.mux.HandleFunc("POST /hooks/{tenant}/gateway", s.gatewayHook)
	R("GET /v1/payouts", s.payouts, false)
	R("GET /v1/payouts/withdrawable", s.withdrawable, false)
	R("POST /v1/payouts", s.requestPayout, true)
	R("POST /v1/payouts/{id}/decide", s.decidePayout, true)
	R("POST /v1/payouts/batches", s.payoutBatch, true)
	R("GET /v1/payouts/batches/{id}/file", s.payoutFile, false)
	R("POST /v1/payouts/batches/{id}/confirm", s.confirmPayouts, true)
	R("GET /v1/corporate-actions", s.corpActions, false)
	R("POST /v1/corporate-actions", s.saveCorpAction, true)
	R("POST /v1/corporate-actions/{id}/apply", s.applyCorpAction, true)
	R("POST /v1/corporate-actions/{id}/dividend", s.receiveDividend, true)
	R("GET /v1/buybacks", s.buybacks, false)
	R("POST /v1/buybacks", s.saveBuyBack, true)
	R("POST /v1/buybacks/{id}/tender", s.tenderBuyBack, true)
	R("GET /v1/buybacks/{id}/file", s.buybackFile, false)
	R("POST /v1/buybacks/{id}/settle", s.settleBuyBack, true)
	R("GET /v1/shortages", s.shortages, false)
	R("POST /v1/shortages/{id}/settle", s.settleShortage, true)
	R("GET /v1/accounts", s.accounts, false)
	R("POST /v1/accounts", s.saveAccount, true)
	R("GET /v1/vouchers", s.vouchers, false)
	R("POST /v1/vouchers", s.saveVoucher, true)
	R("POST /v1/vouchers/{no}/{action}", s.decideVoucher, true)
	R("GET /v1/financials", s.financials, false)
	R("GET /v1/gst", s.gst, false)
	R("GET /v1/gst/gstr1/{table}", s.gstr1, false)
	R("GET /v1/invoices/{no}/document", s.invoiceDoc, false)
	R("GET /v1/debits", s.debits, false)
	R("POST /v1/debits/block", s.debitBlock, true)
	R("GET /v1/debits/block-file", s.debitBlockFile, false)
	R("GET /v1/interest", s.interest, false)
	R("POST /v1/interest/post", s.postInterest, true)
	R("GET /v1/invoices", s.invoices, false)
	R("GET /v1/inactive", s.inactive, false)
	R("POST /v1/inactive/flag", s.flagInactive, true)
	R("POST /v1/inactive/reactivations", s.requestReactivation, true)
	R("POST /v1/inactive/reactivations/{id}/decide", s.decideReactivation, true)
	R("POST /v1/clients/{ucc}/contact", s.updateContact, true)
	R("GET /v1/tags", s.tags, false)
	R("POST /v1/clients/tags", s.tagClients, true)
	R("GET /v1/brokerage", s.brokerage, false)
	R("POST /v1/brokerage/plans", s.savePlan, true)
	R("POST /v1/brokerage/assignments", s.assignPlan, true)
	R("GET /v1/brokerage/client/{ucc}", s.clientPlan, false)
	R("GET /v1/alerts", s.alerts, false)
	R("POST /v1/alerts/{id}/{action}", s.alertAction, true)
	R("POST /v1/surveillance/run", s.runRules, true)
	R("GET /v1/ipo", s.ipo, false)
	R("GET /v1/mf", s.mf, false)
	R("GET /v1/ccm", s.ccm, false)
	R("POST /v1/recon/breaks/{id}/ccm", s.raiseCCM, true)
	R("GET /v1/eod", s.eod, false)
	R("POST /v1/eod/close", s.closeDay, true)
	R("GET /v1/journal", s.journal, false)
	R("GET /v1/sample/files", s.sampleFiles, false)
	if s.dev {
		R("POST /v1/dev/sample", s.loadSample, true)
		R("GET /v1/dev/sample", s.sampleStatus, false)
		R("POST /v1/dev/gateway-payment", s.devGatewayPayment, true)
	}
}

func ok(w http.ResponseWriter, v any, err error) {
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, v)
}

// ---- identity and settings ----

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	us, err := s.ops.Users(r.Context(), tenant(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	set, _ := s.ops.Settings(r.Context(), tenant(r))
	role := ""
	if a := actor(r); a != "" {
		role, _ = s.ops.RoleOf(r.Context(), tenant(r), a)
	}
	var auth any
	if r.Header.Get("Authorization") == "" {
		if _, st, _, err := s.sessionAuth(r); err == nil {
			auth = st
		}
	}
	writeJSON(w, 200, map[string]any{"auth": auth, "tenant": tenant(r), "actor": actor(r), "role": role, "users": us, "roles": ops.Roles, "firm": set.Firm, "dev": s.dev, "member_codes": set.MemberCodes, "holidays": set.Holidays, "sample_rates": s.app.Schedule.Sample, "schedule": s.app.Schedule.Version})
}

func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	us, err := s.ops.UserViews(r.Context(), tenant(r))
	ok(w, us, err)
}

func (s *Server) putUser(w http.ResponseWriter, r *http.Request) {
	var u ops.User
	if !decode(w, r, &u) {
		return
	}
	u.ID = r.PathValue("id")
	if !s.allowed(w, r, "Managing users", ops.RoleAdmin) {
		return
	}
	ok(w, u, s.ops.PutUser(r.Context(), tenant(r), actor(r), u))
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	set, err := s.ops.Settings(r.Context(), tenant(r))
	if set.GatewaySecret != "" {
		set.GatewaySecret = ops.SecretMask
	}
	ok(w, map[string]any{"settings": set, "schedule": s.app.Schedule}, err)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var set ops.Settings
	if !decode(w, r, &set) {
		return
	}
	ok(w, set, s.ops.SaveSettings(r.Context(), tenant(r), actor(r), set))
}

func (s *Server) fileTypes(w http.ResponseWriter, r *http.Request) {
	type ft struct {
		Name     string   `json:"name"`
		Kind     string   `json:"kind"`
		Purpose  string   `json:"purpose"`
		Version  string   `json:"version"`
		Columns  []string `json:"columns,omitempty"`
		NeedDate bool     `json:"needs_date"`
		When     string   `json:"when"`
		Sample   bool     `json:"sample_layout"`
	}
	var out []ft
	for _, sp := range s.app.Ingest.Registry.List() {
		p := "Trade file · " + sp.Exchange + " " + sp.Segment
		if sp.Origin == ingest.FromOMS {
			p = "OMS trade book export"
		}
		out = append(out, ft{sp.Name, "trades", p, sp.Version, nil, false, "EOD (or intraday drop copy)", strings.Contains(sp.Note, "PLACEHOLDER")})
	}
	for _, m := range ops.MasterSources() {
		out = append(out, ft{m.Name, "master", m.Purpose, "1.0", m.Columns, m.NeedDate, m.When, false})
	}
	writeJSON(w, 200, out)
}

// ---- dashboard, clients ----

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Dashboard(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) clients(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.ClientRows(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) client(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.ClientDetail(r.Context(), tenant(r), r.PathValue("ucc"), d)
	ok(w, v, err)
}

func (s *Server) createClient(w http.ResponseWriter, r *http.Request) {
	var c ops.Client
	if !decode(w, r, &c) {
		return
	}
	v, err := s.ops.CreateApplication(r.Context(), tenant(r), actor(r), c)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v.Public())
}

func (s *Server) advance(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.AdvanceStage(r.Context(), tenant(r), actor(r), r.PathValue("ucc"))
	ok(w, v.Public(), err)
}

func (s *Server) block(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Blocked bool   `json:"blocked"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetBlocked(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Blocked, b.Reason)
	ok(w, v.Public(), err)
}

// ---- market, collateral, pledges ----

func (s *Server) market(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	m, _, err := s.ops.Market(r.Context(), tenant(r), d)
	ok(w, m, err)
}

func (s *Server) collateral(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Collateral(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) pledges(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PledgeRequests(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) decidePledge(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Accept bool   `json:"accept"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecidePledge(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Accept, b.Reason)
	ok(w, v, err)
}

// ---- RMS and margin ----

func (s *Server) rms(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Risk(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) createOverride(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date   string    `json:"date"`
		UCC    string    `json:"ucc"`
		Amount money.Dec `json:"amount"`
		Reason string    `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	if _, okd := dateParam(w, b.Date, "date", true); !okd {
		return
	}
	v, err := s.ops.CreateOverride(r.Context(), tenant(r), actor(r), b.Date, b.UCC, b.Amount, b.Reason)
	ok(w, v, err)
}

func (s *Server) decideOverride(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date    string `json:"date"`
		Approve bool   `json:"approve"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideOverride(r.Context(), tenant(r), actor(r), b.Date, r.PathValue("id"), b.Approve)
	ok(w, v, err)
}

func (s *Server) squareOff(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string   `json:"date"`
		UCCs []string `json:"uccs"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SquareOffClients(r.Context(), tenant(r), actor(r), b.Date, b.UCCs)
	ok(w, v, err)
}

func (s *Server) margin(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Margins(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) penalties(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.PostPenalties(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, map[string]int{"posted": n}, err)
}

// ---- settlement ----

func (s *Server) settlement(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Obligations(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) settleAction(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date, Key, Action string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SettleAction(r.Context(), tenant(r), actor(r), b.Date, b.Key, b.Action)
	ok(w, v, err)
}

func (s *Server) settleReady(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date string }
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.SettleAllReady(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, map[string]int{"settled": n}, err)
}

func (s *Server) releaseCUSPA(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ReleaseCUSPA(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

// ---- contract notes ----

func (s *Server) notes(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Notes(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) generateNotes(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date string }
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.GenerateNotes(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, map[string]int{"generated": n}, err)
}

func (s *Server) sendNotes(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date, UCC string }
	if !decode(w, r, &b) {
		return
	}
	sent, bounced, err := s.ops.SendNotes(r.Context(), tenant(r), actor(r), b.Date, b.UCC)
	ok(w, map[string]int{"sent": sent, "bounced": bounced}, err)
}

func (s *Server) noteDocument(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.RenderNote(r.Context(), tenant(r), r.PathValue("date"), r.PathValue("ucc"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Write(b)
}

// ---- bank and funds ----

func (s *Server) bank(w http.ResponseWriter, r *http.Request) {
	ts, err := s.ops.BankTxns(r.Context(), tenant(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	for i := range ts {
		ts[i] = ts[i].Public()
	}
	writeJSON(w, 200, ts)
}

func (s *Server) returnCredit(w http.ResponseWriter, r *http.Request) {
	var b struct{ Note string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReturnCredit(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Note)
	ok(w, v.Public(), err)
}

func (s *Server) identifyCredit(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC           string `json:"ucc"`
		BankConfirmed bool   `json:"bank_confirmed"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.IdentifyCredit(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.UCC, b.BankConfirmed)
	ok(w, v.Public(), err)
}

func (s *Server) upstream(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date   string       `json:"date"`
		Amount money.Amount `json:"amount"`
		Ref    string       `json:"ref"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.Upstream(r.Context(), tenant(r), actor(r), b.Date, b.Amount, b.Ref)
	ok(w, v, err)
}

func (s *Server) funds(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Funds(r.Context(), tenant(r), d)
	ok(w, v, err)
}

// ---- RAS ----

func (s *Server) ras(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.RAS(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) runRAS(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date       string `json:"date"`
		SettleDate string `json:"settle_date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RunRAS(r.Context(), tenant(r), actor(r), b.Date, b.SettleDate)
	if err != nil {
		fail(w, err, nil)
		return
	}
	for i := range v.Items {
		v.Items[i].Account = ops.MaskAccount(v.Items[i].Account)
	}
	writeJSON(w, 201, v)
}

func csvOut(w http.ResponseWriter, name string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Write(ops.CSV(rows))
}

func (s *Server) rasFile(w http.ResponseWriter, r *http.Request) {
	settle := r.URL.Query().Get("settle_date")
	bs, err := s.ops.RASBatches(r.Context(), tenant(r), settle)
	if err != nil {
		fail(w, err, nil)
		return
	}
	rows := [][]string{{"SettlementDate", "UCC", "Name", "Bank", "IFSC", "Account", "Amount"}}
	for _, b := range bs {
		for _, it := range b.Items {
			rows = append(rows, []string{b.Date, it.UCC, it.Name, it.Bank, it.IFSC, it.Account, it.Amount.String()})
		}
	}
	csvOut(w, "ras_payout_"+settle+".csv", rows)
}

// ---- reports ----

func (s *Server) reports(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Reports(r.Context(), tenant(r), d)
	set, _ := s.ops.Settings(r.Context(), tenant(r))
	ok(w, map[string]any{"reports": v, "calendar": set.Calendar}, err)
}

func (s *Server) reportStep(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date, Ack string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReportStep(r.Context(), tenant(r), actor(r), b.Date, r.PathValue("code"), r.PathValue("step"), b.Ack)
	ok(w, v, err)
}

func (s *Server) reportDownload(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	rows, err := s.ops.ReportRows(r.Context(), tenant(r), d, r.PathValue("code"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	s.app.Log(r.Context(), tenant(r), actor(r), "report.downloaded", r.PathValue("code"), map[string]any{"date": d, "rows": len(rows) - 1})
	csvOut(w, r.PathValue("code")+"_"+d+".csv", rows)
}

// ---- client statements and reports ----

func (s *Server) clientReportDefs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, ops.ClientReportDefs)
}

// clientReport returns a client report as JSON (default), CSV or printable
// HTML: ?ucc=&from=&to=&format=json|csv|html.
func (s *Server) clientReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rep, err := s.ops.ClientReportFor(r.Context(), tenant(r), r.PathValue("kind"), q.Get("ucc"), q.Get("from"), q.Get("to"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	switch q.Get("format") {
	case "csv":
		s.app.Log(r.Context(), tenant(r), actor(r), "client_report.downloaded", rep.UCC, map[string]any{"kind": rep.Kind, "period": rep.Period, "format": "csv"})
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+rep.FileName("csv")+`"`)
		w.Write(rep.CSV())
	case "html":
		b, err := s.ops.RenderClientReport(r.Context(), tenant(r), rep)
		if err != nil {
			fail(w, err, nil)
			return
		}
		s.app.Log(r.Context(), tenant(r), actor(r), "client_report.printed", rep.UCC, map[string]any{"kind": rep.Kind, "period": rep.Period})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		w.Write(b)
	default:
		writeJSON(w, 200, rep)
	}
}

// ---- surveillance, IPO, CCM ----

func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Alerts(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) alertAction(w http.ResponseWriter, r *http.Request) {
	var b struct{ Note string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ActOnAlert(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("action"), b.Note)
	ok(w, v, err)
}

func (s *Server) runRules(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date string }
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.RunRules(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, map[string]int{"raised": n}, err)
}

func (s *Server) ipo(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.IPOApps(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) mf(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MFOrders(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) ccm(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CCMs(r.Context(), tenant(r), r.URL.Query().Get("date"))
	ok(w, v, err)
}

func (s *Server) raiseCCM(w http.ResponseWriter, r *http.Request) {
	var b struct{ Reason string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RaiseCCM(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Reason)
	ok(w, v, err)
}

// ---- EOD and journal ----

func (s *Server) eod(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.EOD(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) closeDay(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date, Note string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CloseDay(r.Context(), tenant(r), actor(r), b.Date, b.Note)
	ok(w, v, err)
}

func (s *Server) journal(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	es, err := s.app.Ledger.Store.List(r.Context(), tenant(r), ledger.Filter{From: d, To: d})
	if err == nil {
		vis := s.ops.AccountVisible(r.Context(), tenant(r), actor(r))
		out := es[:0]
		for _, e := range es {
			okE := true
			for _, p := range e.Postings {
				okE = okE && vis(p.Account)
			}
			if okE {
				out = append(out, e)
			}
		}
		es = out
	}
	ok(w, es, err)
}

// ---- sample data ----

func (s *Server) sampleFiles(w http.ResponseWriter, r *http.Request) {
	d := r.URL.Query().Get("date")
	if d == "" {
		d = "2026-10-16"
	}
	day := sample.Generate(d)
	type f struct {
		Source, Name, Date string
		Bytes              int
		Later              bool
	}
	var out []f
	for _, x := range day.Files {
		out = append(out, f{x.Source, x.Name, x.Date, len(x.Data), x.Later})
	}
	writeJSON(w, 200, out)
}

func (s *Server) ingester() ops.TradeIngester {
	return func(ctx context.Context, t, a, source, name string, data []byte) error {
		res, err := s.app.IngestFile(ctx, t, a, source, "", name, data)
		if err != nil && len(res.Errors) > 0 {
			return fmt.Errorf("%w (first: line %d %s)", err, res.Errors[0].Line, res.Errors[0].Msg)
		}
		return err
	}
}

func (s *Server) sampleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.ops.SampleStatus(r.Context(), tenant(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	next, err := s.ops.NextSampleDay(r.Context(), tenant(r))
	ok(w, map[string]any{"start": st.Start, "loaded": st.Loaded, "next": next}, err)
}

// loadSample loads generated files (development only). With run, it also
// works the day end to end and closes it; days > 1 repeats that for the
// following trading days. later loads the IPO allotment results.
func (s *Server) loadSample(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date  string `json:"date"`
		Later bool   `json:"later"`
		Run   bool   `json:"run"`
		Days  int    `json:"days"`
	}
	if !decode(w, r, &b) {
		return
	}
	ctx, t, a := r.Context(), tenant(r), actor(r)
	if b.Later {
		st, err := s.ops.SampleStatus(ctx, t)
		if err != nil || st.Start == "" {
			fail(w, fmt.Errorf("%w: load a sample day first", ops.ErrState), nil)
			return
		}
		n, err := s.ops.LoadSampleDay(ctx, t, a, sample.Generate(st.Start), true, s.ingester())
		ok(w, map[string]any{"files": n, "date": st.Start}, err)
		return
	}
	if b.Date == "" {
		next, err := s.ops.NextSampleDay(ctx, t)
		if err != nil {
			fail(w, err, nil)
			return
		}
		b.Date = next
		if b.Date == "" {
			b.Date = "2026-10-16"
		}
	}
	if !b.Run {
		n, err := s.ops.LoadSampleFor(ctx, t, a, b.Date, s.ingester())
		ok(w, map[string]any{"files": n, "date": b.Date}, err)
		return
	}
	if b.Days < 1 {
		b.Days = 1
	}
	if b.Days > 10 {
		b.Days = 10
	}
	var runs []ops.SampleRun
	d := b.Date
	for i := 0; i < b.Days; i++ {
		run, err := s.ops.RunSampleDay(ctx, t, a, d, s.ingester())
		if err != nil {
			fail(w, fmt.Errorf("%s: %w", d, err), runs)
			return
		}
		runs = append(runs, run)
		d = run.NextDay
	}
	writeJSON(w, 200, map[string]any{"days": runs, "next": d})
}

// ---- groups and brokerage plans ----

func (s *Server) tags(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Tags(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) tagClients(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCCs   []string `json:"uccs"`
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.TagClients(r.Context(), tenant(r), actor(r), b.UCCs, b.Add, b.Remove)
	ok(w, map[string]any{"changed": n}, err)
}

func (s *Server) brokerage(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Brokerage(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) savePlan(w http.ResponseWriter, r *http.Request) {
	var in ops.PlanInput
	if !decode(w, r, &in) {
		return
	}
	p, err := s.ops.SavePlan(r.Context(), tenant(r), actor(r), in)
	ok(w, p, err)
}

func (s *Server) assignPlan(w http.ResponseWriter, r *http.Request) {
	var b struct{ Target, Plan, From string }
	if !decode(w, r, &b) {
		return
	}
	a, err := s.ops.Assign(r.Context(), tenant(r), actor(r), b.Target, b.Plan, b.From)
	ok(w, a, err)
}

func (s *Server) clientPlan(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	ep, desc, err := s.ops.ClientBrokerage(r.Context(), tenant(r), strings.ToUpper(r.PathValue("ucc")), d)
	ok(w, map[string]any{"plan": ep, "rates": desc}, err)
}

// ---- inactive accounts ----

func (s *Server) inactive(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Dormancy(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) flagInactive(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string   `json:"date"`
		UCCs []string `json:"uccs"`
	}
	if !decode(w, r, &b) {
		return
	}
	if _, okd := dateParam(w, b.Date, "date", true); !okd {
		return
	}
	n, err := s.ops.FlagInactive(r.Context(), tenant(r), actor(r), b.Date, b.UCCs)
	ok(w, map[string]any{"flagged": n}, err)
}

func (s *Server) requestReactivation(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
		ops.Reactivation
	}
	if !decode(w, r, &b) {
		return
	}
	if _, okd := dateParam(w, b.Date, "date", true); !okd {
		return
	}
	v, err := s.ops.RequestReactivation(r.Context(), tenant(r), actor(r), b.Date, b.Reactivation)
	ok(w, v, err)
}

func (s *Server) decideReactivation(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date    string `json:"date"`
		Approve bool   `json:"approve"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	if _, okd := dateParam(w, b.Date, "date", true); !okd {
		return
	}
	v, err := s.ops.DecideReactivation(r.Context(), tenant(r), actor(r), b.Date, r.PathValue("id"), b.Approve, b.Reason)
	ok(w, v, err)
}

func (s *Server) updateContact(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date      string `json:"date"`
		Email     string `json:"email"`
		Mobile    string `json:"mobile"`
		City      string `json:"city"`
		KRAStatus string `json:"kra_status"`
	}
	if !decode(w, r, &b) {
		return
	}
	if _, okd := dateParam(w, b.Date, "date", true); !okd {
		return
	}
	c, err := s.ops.UpdateContact(r.Context(), tenant(r), actor(r), b.Date, r.PathValue("ucc"), b.Email, b.Mobile, b.City, b.KRAStatus)
	ok(w, c, err)
}

func (s *Server) correctBill(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date   string `json:"date"`
		UCC    string `json:"ucc"`
		Reason string `json:"reason"`
		Plan   string `json:"plan"`
		Scope  string `json:"scope"` // day or onward
		Send   bool   `json:"send"`
	}
	if !decode(w, r, &b) {
		return
	}
	if _, okd := dateParam(w, b.Date, "date", true); !okd {
		return
	}
	v, err := s.ops.CorrectBill(r.Context(), tenant(r), actor(r), b.Date, b.UCC, b.Reason, b.Plan, b.Scope, b.Send)
	ok(w, v, err)
}

// ---- debits and interest ----

func (s *Server) debits(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Debits(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) debitBlock(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	if _, okd := dateParam(w, b.Date, "date", true); !okd {
		return
	}
	bl, un, err := s.ops.ApplyDebitBlocks(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, map[string]any{"blocked": bl, "unblocked": un}, err)
}

func (s *Server) debitBlockFile(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	f, err := s.ops.DebitBlockFile(r.Context(), tenant(r), d)
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="debit_block_`+d+`.csv"`)
	w.Write(f)
}

func (s *Server) interest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.Interest(r.Context(), tenant(r), q.Get("from"), q.Get("to"))
	ok(w, v, err)
}

func (s *Server) postInterest(w http.ResponseWriter, r *http.Request) {
	var b struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, n, err := s.ops.PostInterest(r.Context(), tenant(r), actor(r), b.From, b.To)
	ok(w, map[string]any{"posted": n, "view": v}, err)
}

func (s *Server) invoices(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Invoices(r.Context(), tenant(r), strings.ToUpper(r.URL.Query().Get("ucc")))
	ok(w, v, err)
}

// ---- receipts, gateway and payouts ----

func (s *Server) receipts(w http.ResponseWriter, r *http.Request) {
	rs, err := s.ops.Receipts(r.Context(), tenant(r))
	ok(w, rs, err)
}

func (s *Server) recordReceipt(w http.ResponseWriter, r *http.Request) {
	var b ops.Receipt
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordReceipt(r.Context(), tenant(r), actor(r), b)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) decideReceipt(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideReceipt(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Note)
	ok(w, v, err)
}

func (s *Server) clearCheque(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date    string `json:"date"`
		Bounced bool   `json:"bounced"`
		Charge  bool   `json:"charge"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ClearCheque(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.Bounced, b.Charge, b.Note)
	ok(w, v, err)
}

func (s *Server) settleGateway(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string       `json:"date"`
		Ref  string       `json:"ref"`
		Fee  money.Amount `json:"fee"`
	}
	if !decode(w, r, &b) {
		return
	}
	n, gross, err := s.ops.SettleGateway(r.Context(), tenant(r), actor(r), b.Date, b.Ref, b.Fee)
	ok(w, map[string]any{"receipts": n, "gross": gross}, err)
}

// gatewayHook receives payment-gateway webhooks. It is not signed in: the
// body must carry a valid HMAC signature made with the firm's gateway secret.
func (s *Server) gatewayHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the body", nil)
		return
	}
	sig := r.Header.Get("X-Razorpay-Signature")
	if sig == "" {
		sig = r.Header.Get("X-Signature")
	}
	rc, created, err := s.ops.GatewayWebhook(r.Context(), r.PathValue("tenant"), body, sig)
	ok(w, map[string]any{"receipt": rc.ID, "created": created}, err)
}

func (s *Server) devGatewayPayment(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC    string       `json:"ucc"`
		Amount money.Amount `json:"amount"`
		Date   string       `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	set, err := s.ops.Settings(r.Context(), tenant(r))
	if err != nil || set.GatewaySecret == "" {
		fail(w, fmt.Errorf("%w: set a gateway secret in Settings first", ops.ErrState), nil)
		return
	}
	ev := ops.GatewayPayment{Event: "payment.captured"}
	e := &ev.Payload.Payment.Entity
	e.ID = fmt.Sprintf("pay_dev%d", time.Now().UnixNano()%1e10)
	e.Amount, e.Method, e.VPA, e.Status = b.Amount.Paise(), "upi", strings.ToLower(b.UCC)+"@okbank", "captured"
	e.Notes = map[string]string{"ucc": b.UCC, "value_date": b.Date}
	body, _ := json.Marshal(ev)
	rc, created, err := s.ops.GatewayWebhook(r.Context(), tenant(r), body, ops.SignGateway(set.GatewaySecret, body))
	ok(w, map[string]any{"receipt": rc, "created": created}, err)
}

func (s *Server) payouts(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	rs, err := s.ops.PayoutRequests(r.Context(), tenant(r), d)
	if err != nil {
		fail(w, err, nil)
		return
	}
	bs, err := s.ops.PayoutBatches(r.Context(), tenant(r))
	for i := range bs {
		for j := range bs[i].Items {
			bs[i].Items[j].Account = ops.MaskAccount(bs[i].Items[j].Account)
		}
	}
	ok(w, map[string]any{"requests": rs, "batches": bs}, err)
}

func (s *Server) withdrawable(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Withdrawable(r.Context(), tenant(r), d, strings.ToUpper(r.URL.Query().Get("ucc")))
	ok(w, v, err)
}

func (s *Server) requestPayout(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date   string       `json:"date"`
		UCC    string       `json:"ucc"`
		Amount money.Amount `json:"amount"`
	}
	if !decode(w, r, &b) {
		return
	}
	if _, okd := dateParam(w, b.Date, "date", true); !okd {
		return
	}
	v, err := s.ops.RequestPayout(r.Context(), tenant(r), actor(r), b.Date, b.UCC, b.Amount, "Head office")
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) decidePayout(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date    string `json:"date"`
		Approve bool   `json:"approve"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecidePayout(r.Context(), tenant(r), actor(r), b.Date, r.PathValue("id"), b.Approve, b.Reason)
	ok(w, v, err)
}

func (s *Server) payoutBatch(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CreatePayoutBatch(r.Context(), tenant(r), actor(r), b.Date)
	if err != nil {
		fail(w, err, nil)
		return
	}
	for j := range v.Items {
		v.Items[j].Account = ops.MaskAccount(v.Items[j].Account)
	}
	writeJSON(w, 201, v)
}

func (s *Server) payoutFile(w http.ResponseWriter, r *http.Request) {
	f, err := s.ops.PayoutFile(r.Context(), tenant(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="payout_`+r.PathValue("id")+`.csv"`)
	w.Write(f)
}

func (s *Server) confirmPayouts(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UTRs   map[string]string `json:"utrs"`
		Failed map[string]string `json:"failed"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ConfirmPayouts(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.UTRs, b.Failed)
	ok(w, v, err)
}

// ---- corporate actions, buy-backs, shortages ----

func (s *Server) corpActions(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CorpActions(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) saveCorpAction(w http.ResponseWriter, r *http.Request) {
	var b ops.CorpAction
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveCorpAction(r.Context(), tenant(r), actor(r), b)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) applyCorpAction(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ApplyCorpAction(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) receiveDividend(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date     string       `json:"date"`
		Ref      string       `json:"ref"`
		Received money.Amount `json:"received"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReceiveDividend(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.Ref, b.Received)
	ok(w, v, err)
}

func (s *Server) buybacks(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BuyBacks(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) saveBuyBack(w http.ResponseWriter, r *http.Request) {
	var b ops.BuyBack
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveBuyBack(r.Context(), tenant(r), actor(r), b)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) tenderBuyBack(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
		UCC  string `json:"ucc"`
		Qty  int64  `json:"qty"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.TenderBuyBack(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.UCC, b.Qty, false)
	ok(w, v, err)
}

func (s *Server) buybackFile(w http.ResponseWriter, r *http.Request) {
	f, err := s.ops.BuyBackFile(r.Context(), tenant(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="buyback_bids_`+r.PathValue("id")+`.csv"`)
	w.Write(f)
}

func (s *Server) settleBuyBack(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date     string           `json:"date"`
		Exchange string           `json:"exchange"`
		Accepted map[string]int64 `json:"accepted"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SettleBuyBack(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.Exchange, b.Accepted)
	ok(w, v, err)
}

func (s *Server) shortages(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Shortages(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) settleShortage(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date  string    `json:"date"`
		Price money.Dec `json:"price"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SettleShortage(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.Price)
	ok(w, v, err)
}

// ---- firm accounts and GST ----

func (s *Server) accounts(w http.ResponseWriter, r *http.Request) {
	d, okd := dateParam(w, r.URL.Query().Get("date"), "date", false)
	if !okd {
		return
	}
	v, err := s.ops.Accounts(r.Context(), tenant(r), actor(r), d)
	ok(w, v, err)
}

func (s *Server) saveAccount(w http.ResponseWriter, r *http.Request) {
	var b ops.Account
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveAccount(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) vouchers(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Vouchers(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) saveVoucher(w http.ResponseWriter, r *http.Request) {
	var b ops.Voucher
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveVoucher(r.Context(), tenant(r), actor(r), b)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) decideVoucher(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideVoucher(r.Context(), tenant(r), actor(r), r.PathValue("no"), r.PathValue("action"), b.Reason)
	ok(w, v, err)
}

func (s *Server) financials(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.Financials(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"), q.Get("branch"))
	ok(w, v, err)
}

func (s *Server) gst(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.GST(r.Context(), tenant(r), q.Get("from"), q.Get("to"))
	ok(w, v, err)
}

func (s *Server) gstr1(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f, err := s.ops.GSTR1CSV(r.Context(), tenant(r), q.Get("from"), q.Get("to"), r.PathValue("table"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="gstr1_`+r.PathValue("table")+`_`+q.Get("from")+`_`+q.Get("to")+`.csv"`)
	w.Write(f)
}

func (s *Server) invoiceDoc(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.RenderInvoice(r.Context(), tenant(r), r.PathValue("no"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Write(b)
}
