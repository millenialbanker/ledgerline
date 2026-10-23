package api

import (
	"context"
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"ledgerline/internal/backup"
	"net/http"
	"sort"
	"strconv"
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
	s.chatRoutes()
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
	R("POST /v1/clients/{ucc}/reveal", s.revealClient, true)
	R("GET /v1/security", s.security, false)
	R("GET /v1/ai/usage", s.aiUsage, false)
	R("GET /v1/backups", s.backups, false)
	R("POST /v1/backups/run", s.backupNow, true)
	R("POST /v1/backups/{id}/verify", s.verifyBackup, true)
	R("PUT /v1/backups/policy", s.backupPolicy, true)
	R("GET /v1/holidays", s.holidays, false)
	R("POST /v1/holidays/check", s.checkHolidays, true)
	R("POST /v1/holidays/upload", s.uploadHolidays, true)
	R("POST /v1/holidays/{id}", s.decideHolidays, true)
	R("GET /v1/me/dashboard", s.dashLayouts, false)
	R("PUT /v1/me/dashboard", s.saveDashLayout, true)
	R("PUT /v1/dashboard/default", s.saveFirmDash, true)
	R("POST /v1/ledgy/ask", s.ledgyAsk, true)
	R("GET /v1/ledgy/suggest", s.ledgySuggest, false)
	R("GET /v1/ledgy/articles/{id}", s.ledgyArticle, false)
	R("POST /v1/ledgy/feedback", s.ledgyFeedback, true)
	R("GET /v1/ledgy/gaps", s.ledgyGaps, false)
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
	R("GET /v1/branches", s.branches, false)
	R("POST /v1/branches", s.saveBranch, true)
	R("GET /v1/partners", s.partners, false)
	R("POST /v1/partners", s.savePartner, true)
	R("POST /v1/clients/{ucc}/mapping", s.mapClient, true)
	R("POST /v1/clients/{ucc}/privacy", s.clientPrivacy, true)
	R("GET /v1/ap-payouts", s.apPayouts, false)
	R("POST /v1/ap-payouts/post", s.postAPPayouts, true)
	R("GET /v1/referrals", s.referrals, false)
	R("POST /v1/referrals/post", s.postReferrals, true)
	R("GET /v1/prepaid", s.prepaid, false)
	R("POST /v1/prepaid/schemes", s.savePrepaidScheme, true)
	R("POST /v1/prepaid/subscriptions", s.buyPrepaid, true)
	R("POST /v1/prepaid/run", s.runPrepaid, true)
	R("GET /v1/compliance", s.compliance, false)
	R("POST /v1/clients/{ucc}/portal", s.portalAccess, true)
	R("GET /v1/portal", s.portal, false)
	R("GET /v1/changes", s.changes, false)
	R("POST /v1/changes", s.requestChange, true)
	R("POST /v1/changes/{id}/decide", s.decideChange, true)
	R("GET /v1/external-trades", s.extTrades, false)
	R("POST /v1/external-trades", s.addExtTrade, true)
	R("POST /v1/external-trades/{ucc}/{id}/delete", s.delExtTrade, true)
	R("GET /v1/tickets", s.tickets, false)
	R("POST /v1/tickets", s.openTicket, true)
	R("POST /v1/tickets/{id}", s.updateTicket, true)
	R("GET /v1/complaints/disclosure", s.disclosure, false)
	R("GET /v1/crm", s.crm, false)
	R("GET /v1/messages", s.messages, false)
	R("GET /v1/templates", s.templates, false)
	R("POST /v1/templates", s.saveTemplate, true)
	R("POST /v1/templates/{id}/test", s.testTemplate, true)
	R("POST /v1/messages/bulk", s.bulkSend, true)
	R("GET /v1/whatsapp/chats", s.waChats, false)
	R("POST /v1/whatsapp/simulate", s.waSimulate, true)
	s.mux.HandleFunc("POST /hooks/{tenant}/whatsapp", s.waHook)
	s.mux.HandleFunc("GET /optin/{tenant}/{token}", s.optinPage)
	s.mux.HandleFunc("POST /optin/{tenant}/{token}", s.optinPage)
	R("GET /v1/whatsapp/consents", s.waConsents, false)
	R("POST /v1/whatsapp/consent-campaign", s.waCampaign, true)
	R("POST /v1/clients/{ucc}/whatsapp-consent", s.waRecordConsent, true)
	R("POST /v1/portal/whatsapp", s.portalWhatsApp, true)
	R("GET /v1/insights", s.insights, false)
	R("GET /v1/year-end", s.yearEnds, false)
	R("GET /v1/circulars", s.circulars, false)
	R("GET /v1/applications", s.applications, false)
	R("POST /v1/applications/{id}/reject", s.rejectApplication, true)
	R("GET /v1/applications/{id}/docs/{n}", s.applicationDoc, false)
	s.onboardRoutes()
	R("GET /v1/deposits", s.deposits, false)
	R("POST /v1/deposits", s.saveDeposit, true)
	R("POST /v1/deposits/{id}/release", s.releaseDeposit, true)
	R("POST /v1/circulars/{id}", s.actCircular, true)
	R("POST /v1/duties", s.saveDuty, true)
	R("POST /v1/duties/{id}/done", s.dutyDone, true)
	R("POST /v1/year-end/{fy}/send", s.sendYearEnd, true)
	R("GET /v1/year-end/{fy}/{ucc}/document", s.yearEndDoc, false)
	R("POST /v1/insights/find", s.findInsights, true)
	R("POST /v1/insights/{id}", s.actInsight, true)
	R("GET /v1/tax-plan", s.taxPlan, false)
	R("GET /v1/portal/insights", s.portalInsights, false)
	R("POST /v1/portal/insights/prefs", s.portalInsightPrefs, true)
	R("POST /v1/portal/buyback/{id}/tender", s.portalTender, true)
	R("GET /v1/dp", s.dpDesk, false)
	R("POST /v1/dp/tariff", s.dpTariff, true)
	R("POST /v1/dp/accounts/{ucc}/bsda", s.dpBSDA, true)
	R("POST /v1/dp/books", s.dpBook, true)
	R("POST /v1/dp/dis", s.dpEnterDIS, true)
	R("POST /v1/dp/dis/batch", s.dpBatch, true)
	R("POST /v1/dp/dis/{id}/verify", s.dpVerify, true)
	R("POST /v1/dp/dis/{id}/execute", s.dpExecute, true)
	R("GET /v1/dp/transactions", s.dpTxns, false)
	R("POST /v1/dp/billing", s.dpBilling, true)
	R("POST /v1/dp/requests", s.dpRequest, true)
	R("POST /v1/dp/requests/{id}", s.dpActRequest, true)
	R("GET /v1/mtf", s.mtf, false)
	R("POST /v1/mtf/config", s.mtfConfig, true)
	R("POST /v1/mtf/enrol", s.mtfEnrol, true)
	R("POST /v1/mtf/fund", s.mtfFund, true)
	R("POST /v1/mtf/calls", s.mtfCalls, true)
	R("POST /v1/mtf/interest", s.mtfInterest, true)
	R("GET /v1/mtf/liquidation", s.mtfLiquidation, false)
	R("GET /v1/mtf/report", s.mtfReport, false)
	R("POST /v1/mtf/{id}/pledge", s.mtfPledge, true)
	R("POST /v1/mtf/{id}/close", s.mtfClose, true)
	R("GET /v1/live", s.live, false)
	R("GET /v1/live/liquidation", s.liveLiquidation, false)
	R("GET /v1/frontoffice/{kind}", s.frontOffice, false)
	R("GET /v1/nri", s.nri, false)
	R("POST /v1/nri", s.setNRI, true)
	R("GET /v1/nri/pis-file", s.pisFile, false)
	R("GET /v1/nri/tds", s.nriTDS, false)
	R("GET /v1/autopilot", s.autopilot, false)
	R("POST /v1/autopilot/policies", s.autoPolicy, true)
	R("POST /v1/autopilot/run", s.autoRun, true)
	R("POST /v1/autopilot/schedule", s.autoSchedule, true)
	R("POST /v1/work/{id}", s.workItem, true)
	R("POST /v1/inbox", s.inbox, true)
	R("POST /v1/changes/{id}/verify", s.verifyChange, true)
	R("GET /v1/jobs", s.jobs, false)
	R("POST /v1/jobs", s.saveJob, true)
	R("POST /v1/jobs/{id}/run", s.runJob, true)
	R("POST /v1/leads", s.saveLead, true)
	R("POST /v1/leads/{id}/act", s.actLead, true)
	R("POST /v1/clients/{ucc}/risk", s.clientRisk, true)
	R("POST /v1/clients/{ucc}/rekyc", s.reKYC, true)
	R("GET /v1/compliance/kra-upload", s.kraUpload, false)
	R("GET /v1/compliance/ckyc-request", s.ckycRequest, false)
	R("GET /v1/compliance/stt", s.sttRecon, false)
	R("GET /v1/clients/{ucc}/documents", s.clientDocs, false)
	R("POST /v1/clients/{ucc}/documents", s.addClientDoc, true)
	R("GET /v1/clients/{ucc}/documents/{id}", s.clientDocFile, false)
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
	R("GET /v1/edition", s.edition, false)
	R("GET /v1/eod", s.eod, false)
	R("POST /v1/eod/close", s.closeDay, true)
	R("GET /v1/journal", s.journal, false)
	R("GET /v1/sample/files", s.sampleFiles, false)
	if s.dev {
		R("POST /v1/dev/sample", s.loadSample, true)
		R("GET /v1/dev/sample", s.sampleStatus, false)
		R("POST /v1/dev/gateway-payment", s.devGatewayPayment, true)
		R("POST /v1/dev/edition", s.devEdition, true)
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
	if role == ops.RoleClient {
		// A client sees only their own login, never the firm's staff list.
		var mine []ops.User
		for _, u := range us {
			if u.ID == actor(r) {
				mine = append(mine, u)
			}
		}
		us = mine
	}
	writeJSON(w, 200, map[string]any{"auth": auth, "tenant": tenant(r), "actor": actor(r), "role": role, "users": us, "roles": ops.Roles, "firm": set.Firm, "dev": s.dev, "member_codes": set.MemberCodes, "holidays": set.TradingHolidays(), "sample_rates": s.app.Schedule.Sample, "schedule": s.app.Schedule.Version, "ledgy_ai": s.ops.LedgyAI != nil, "edition": s.ops.EditionStatus(r.Context(), tenant(r))})
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
	if set.WhatsAppSecret != "" {
		set.WhatsAppSecret = ops.SecretMask
	}
	ok(w, map[string]any{"settings": set, "schedule": s.app.Schedule}, err)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	// Start from the saved settings so fields a screen does not send keep
	// their values.
	set, err := s.ops.Settings(r.Context(), tenant(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
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
	if f := s.scope(r); f != nil && err == nil {
		out := v[:0]
		for _, c := range v {
			if f(c.UCC) {
				out = append(out, c)
			}
		}
		v = out
	}
	if role, _ := s.ops.RoleOf(r.Context(), tenant(r), actor(r)); role == ops.RoleClient && err == nil {
		// The portal needs names for its account picker, nothing more.
		type row struct {
			UCC    string   `json:"ucc"`
			Name   string   `json:"name"`
			Status string   `json:"status"`
			Tags   []string `json:"tags"`
		}
		out := []row{}
		for _, c := range v {
			out = append(out, row{c.UCC, c.Name, c.Status, []string{}})
		}
		ok(w, out, nil)
		return
	}
	ok(w, v, err)
}

func (s *Server) client(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	if !s.canSee(w, r, r.PathValue("ucc")) {
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
	if f := s.scope(r); f != nil && err == nil {
		rows := v.Rows[:0]
		for _, x := range v.Rows {
			if f(x.UCC) {
				rows = append(rows, x)
			}
		}
		v.Rows = rows
		for u := range v.Positions {
			if !f(u) {
				delete(v.Positions, u)
			}
		}
		ovs := v.Overrides[:0]
		for _, x := range v.Overrides {
			if f(x.UCC) {
				ovs = append(ovs, x)
			}
		}
		v.Overrides = ovs
	}
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
	if f := s.scope(r); f != nil && err == nil {
		out := v[:0]
		for _, n := range v {
			if f(n.UCC) {
				out = append(out, n)
			}
		}
		v = out
	}
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
	if !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	b, err := s.ops.RenderNote(r.Context(), tenant(r), r.PathValue("date"), r.PathValue("ucc"), r.URL.Query().Get("part"))
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
	if f := s.scope(r); f != nil {
		u := strings.ToUpper(q.Get("ucc"))
		if u == "ALL" || strings.HasPrefix(u, "TAG:") {
			writeErr(w, 403, "forbidden", "Reports for all clients or a group are not available to this login; choose one client.", nil)
			return
		}
		if !s.canSee(w, r, u) {
			return
		}
	}
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
	var b struct{ Date, Note, Part string }
	if !decode(w, r, &b) {
		return
	}
	if b.Part == "" { // the whole day: every part still open
		v, err := s.ops.CloseDay(r.Context(), tenant(r), actor(r), b.Date, b.Note)
		ok(w, v, err)
		return
	}
	v, err := s.ops.ClosePart(r.Context(), tenant(r), actor(r), b.Date, b.Part, b.Note)
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
		Auto  bool   `json:"autopilot"`
	}
	if !decode(w, r, &b) {
		return
	}
	ctx, t, a := r.Context(), tenant(r), actor(r)
	if b.Auto {
		if b.Date == "" {
			b.Date, _ = s.ops.NextSampleDay(ctx, t)
			if b.Date == "" {
				b.Date = "2026-10-16"
			}
		}
		n, err := s.ops.LoadSampleFor(ctx, t, a, b.Date, s.ingester())
		if err != nil {
			fail(w, err, nil)
			return
		}
		var all []ops.AutoRun
		for _, st := range []string{"bod", "intraday", "preclose", "eod"} {
			runs, err := s.ops.RunStage(ctx, t, ops.SystemActor, b.Date, st, "")
			if err != nil {
				fail(w, err, nil)
				return
			}
			all = append(all, runs...)
		}
		ok(w, map[string]any{"date": b.Date, "files": n, "runs": all}, nil)
		return
	}
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
	if !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
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
	if f := s.scope(r); f != nil && err == nil {
		rows := v.Rows[:0]
		for _, x := range v.Rows {
			if f(x.UCC) {
				rows = append(rows, x)
			}
		}
		v.Rows = rows
	}
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
	if f := s.scope(r); f != nil && err == nil {
		out := rs[:0]
		for _, x := range rs {
			if f(x.UCC) {
				out = append(out, x)
			}
		}
		rs = out
	}
	ok(w, rs, err)
}

func (s *Server) recordReceipt(w http.ResponseWriter, r *http.Request) {
	var b ops.Receipt
	if !decode(w, r, &b) {
		return
	}
	if !s.canSee(w, r, b.UCC) {
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
	if f := s.scope(r); f != nil {
		out := rs[:0]
		for _, x := range rs {
			if f(x.UCC) {
				out = append(out, x)
			}
		}
		ok(w, map[string]any{"requests": out, "batches": []ops.PayoutBatch{}}, nil)
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
	if !s.canSee(w, r, r.URL.Query().Get("ucc")) {
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
	if !s.canSee(w, r, b.UCC) {
		return
	}
	src := "Head office"
	switch role, _ := s.ops.RoleOf(r.Context(), tenant(r), actor(r)); role {
	case ops.RoleBranch, ops.RoleAP:
		src = "Branch"
	case ops.RoleClient:
		src = "Client"
	}
	v, err := s.ops.RequestPayout(r.Context(), tenant(r), actor(r), b.Date, b.UCC, b.Amount, src)
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

// ---- branches, partners, referrals, prepaid ----

func (s *Server) branches(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Branches(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) saveBranch(w http.ResponseWriter, r *http.Request) {
	var b ops.Branch
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveBranch(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) partners(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Partners(r.Context(), tenant(r), d)
	if err == nil {
		if u, uerr := s.ops.UserByID(r.Context(), tenant(r), actor(r)); uerr == nil && (u.Role == ops.RoleAP || u.Role == ops.RoleBranch) {
			out := v[:0]
			keep := map[string]bool{u.Partner: true}
			for changed := true; changed; {
				changed = false
				for _, p := range v {
					if keep[p.Parent] && !keep[p.Code] {
						keep[p.Code], changed = true, true
					}
				}
			}
			for _, p := range v {
				if (u.Role == ops.RoleAP && keep[p.Code]) || (u.Role == ops.RoleBranch && p.Branch == u.Branch) {
					out = append(out, p)
				}
			}
			v = out
		}
	}
	ok(w, v, err)
}

func (s *Server) savePartner(w http.ResponseWriter, r *http.Request) {
	var b ops.Partner
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SavePartner(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) mapClient(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Branch   string `json:"branch"`
		Partner  string `json:"partner"`
		RM       string `json:"rm"`
		Referrer string `json:"referrer"`
		From     string `json:"referred_from"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AssignClient(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Branch, b.Partner, b.RM, b.Referrer, b.From)
	ok(w, v.Public(), err)
}

func (s *Server) clientPrivacy(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Private   bool     `json:"private"`
		VisibleTo []string `json:"visible_to"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetClientPrivacy(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Private, b.VisibleTo)
	ok(w, v.Public(), err)
}

func (s *Server) apPayouts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.APPayouts(r.Context(), tenant(r), q.Get("from"), q.Get("to"))
	ok(w, v, err)
}

func (s *Server) postAPPayouts(w http.ResponseWriter, r *http.Request) {
	var b struct{ From, To string }
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.PostAPPayouts(r.Context(), tenant(r), actor(r), b.From, b.To)
	ok(w, map[string]any{"posted": n}, err)
}

func (s *Server) referrals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.Referrals(r.Context(), tenant(r), q.Get("from"), q.Get("to"))
	ok(w, v, err)
}

func (s *Server) postReferrals(w http.ResponseWriter, r *http.Request) {
	var b struct{ From, To string }
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.PostReferrals(r.Context(), tenant(r), actor(r), b.From, b.To)
	ok(w, map[string]any{"posted": n}, err)
}

func (s *Server) prepaid(w http.ResponseWriter, r *http.Request) {
	sc, err := s.ops.PrepaidSchemes(r.Context(), tenant(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	subs, err := s.ops.PrepaidSubs(r.Context(), tenant(r))
	if f := s.scope(r); f != nil && err == nil {
		out := subs[:0]
		for _, x := range subs {
			if f(x.UCC) {
				out = append(out, x)
			}
		}
		subs = out
	}
	ok(w, map[string]any{"schemes": sc, "subscriptions": subs}, err)
}

func (s *Server) savePrepaidScheme(w http.ResponseWriter, r *http.Request) {
	var b ops.PrepaidScheme
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SavePrepaidScheme(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) buyPrepaid(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date      string `json:"date"`
		UCC       string `json:"ucc"`
		Scheme    string `json:"scheme"`
		AutoRenew bool   `json:"auto_renew"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !s.canSee(w, r, b.UCC) {
		return
	}
	v, err := s.ops.BuyPrepaid(r.Context(), tenant(r), actor(r), b.Date, b.UCC, b.Scheme, b.AutoRenew)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) runPrepaid(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date string }
	if !decode(w, r, &b) {
		return
	}
	a, e, n, err := s.ops.RunPrepaid(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, map[string]any{"adjusted": a, "expired": e, "renewed": n}, err)
}

// ---- compliance desk ----

func (s *Server) compliance(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.PMLA(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) clientRisk(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Risk string   `json:"risk"`
		CSC  []string `json:"csc"`
		Note string   `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetClientRisk(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Risk, b.CSC, b.Note)
	ok(w, v.Public(), err)
}

func (s *Server) reKYC(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date, Mode string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordReKYC(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Date, b.Mode)
	ok(w, v.Public(), err)
}

func (s *Server) kraUpload(w http.ResponseWriter, r *http.Request) {
	since, okd := dateParam(w, r.URL.Query().Get("since"), "since", true)
	if !okd {
		return
	}
	f, err := s.ops.KRAUploadFile(r.Context(), tenant(r), since)
	if err != nil {
		fail(w, err, nil)
		return
	}
	s.app.Log(r.Context(), tenant(r), actor(r), "kra.upload_file", since, nil)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="kra_upload_`+since+`.csv"`)
	w.Write(f)
}

func (s *Server) ckycRequest(w http.ResponseWriter, r *http.Request) {
	f, err := s.ops.CKYCRequestFile(r.Context(), tenant(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ckyc_bulk_request.csv"`)
	w.Write(f)
}

func (s *Server) sttRecon(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.STTReconcile(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) clientDocs(w http.ResponseWriter, r *http.Request) {
	if !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	v, err := s.ops.ClientDocs(r.Context(), tenant(r), r.PathValue("ucc"))
	ok(w, v, err)
}

func (s *Server) addClientDoc(w http.ResponseWriter, r *http.Request) {
	if !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, ops.MaxDocBytes+1))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	q := r.URL.Query()
	v, err := s.ops.AddClientDoc(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), q.Get("kind"), q.Get("name"), data)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) clientDocFile(w http.ResponseWriter, r *http.Request) {
	if !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	d, b, err := s.ops.ClientDocFile(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", d.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", `inline; filename="`+strings.NewReplacer(`"`, "", "\\", "").Replace(d.Name)+`"`)
	w.Write(b)
}

// ---- client portal, help desk, CRM ----

func (s *Server) portalAccess(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Family   []string `json:"family"`
		Declared bool     `json:"declared"`
	}
	if !decode(w, r, &b) {
		return
	}
	id, pw, err := s.ops.PortalAccess(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Family, b.Declared)
	ok(w, map[string]any{"user": id, "password": pw}, err)
}

func (s *Server) portal(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Portal(r.Context(), tenant(r), actor(r), d, r.URL.Query().Get("ucc"))
	ok(w, v, err)
}

func (s *Server) changes(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ChangeRequests(r.Context(), tenant(r))
	if f := s.scope(r); f != nil && err == nil {
		mine := []ops.ChangeRequest{}
		for _, c := range v {
			if f(c.UCC) {
				mine = append(mine, c)
			}
		}
		v = mine
	}
	ok(w, map[string]any{"requests": v, "kinds": ops.ChangeKinds}, err)
}

func (s *Server) requestChange(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC    string            `json:"ucc"`
		Kind   string            `json:"kind"`
		Values map[string]string `json:"values"`
		Doc    string            `json:"doc"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !s.canSee(w, r, b.UCC) {
		return
	}
	v, err := s.ops.RequestChange(r.Context(), tenant(r), actor(r), b.UCC, b.Kind, b.Values, b.Doc)
	if err != nil {
		fail(w, err, nil)
		return
	}
	if !s.dev {
		v.DevCode = ""
	}
	writeJSON(w, 201, v)
}

func (s *Server) decideChange(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideChange(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Reason)
	ok(w, v, err)
}

func (s *Server) extTrades(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("ucc")
	if !s.canSee(w, r, u) {
		return
	}
	v, err := s.ops.ExternalTrades(r.Context(), tenant(r), u)
	ok(w, v, err)
}

func (s *Server) addExtTrade(w http.ResponseWriter, r *http.Request) {
	var b ops.ExternalTrade
	if !decode(w, r, &b) {
		return
	}
	if !s.canSee(w, r, b.UCC) {
		return
	}
	v, err := s.ops.AddExternalTrade(r.Context(), tenant(r), actor(r), b)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) delExtTrade(w http.ResponseWriter, r *http.Request) {
	if !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	err := s.ops.DeleteExternalTrade(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), r.PathValue("id"))
	ok(w, map[string]any{"deleted": err == nil}, err)
}

func (s *Server) tickets(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Tickets(r.Context(), tenant(r), d)
	if f := s.scope(r); f != nil && err == nil {
		mine := []ops.Ticket{}
		for _, t := range v {
			if f(t.UCC) {
				mine = append(mine, t)
			}
		}
		v = mine
	}
	staff := []string{}
	if role, _ := s.ops.RoleOf(r.Context(), tenant(r), actor(r)); role == ops.RoleClient {
		for i := range v {
			v[i].Internal = nil
		}
	} else {
		us, _ := s.ops.Users(r.Context(), tenant(r))
		for _, u := range us {
			if u.Role != ops.RoleClient && u.Active {
				staff = append(staff, u.ID)
			}
		}
	}
	ok(w, map[string]any{"tickets": v, "sources": ops.TicketSources, "categories": ops.TicketCategories, "statuses": ops.TicketStatuses, "staff": staff}, err)
}

func (s *Server) openTicket(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ops.Ticket
		Text string `json:"text"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !s.canSee(w, r, b.UCC) {
		return
	}
	v, err := s.ops.OpenTicket(r.Context(), tenant(r), actor(r), b.Ticket, b.Text)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) updateTicket(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Text     string `json:"text"`
		Internal bool   `json:"internal"`
		Status   string `json:"status"`
		Assignee string `json:"assignee"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.TicketUpdate(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Text, b.Internal, b.Status, b.Assignee)
	if role, _ := s.ops.RoleOf(r.Context(), tenant(r), actor(r)); role == ops.RoleClient {
		v.Internal = nil
	}
	ok(w, v, err)
}

func (s *Server) disclosure(w http.ResponseWriter, r *http.Request) {
	m := r.URL.Query().Get("month")
	v, err := s.ops.ComplaintsDisclosure(r.Context(), tenant(r), m)
	ok(w, v, err)
}

func (s *Server) crm(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if from == "" {
		from = d[:8] + "01"
	}
	if to == "" {
		to = d
	}
	v, err := s.ops.CRM(r.Context(), tenant(r), actor(r), d, from, to)
	ok(w, v, err)
}

func (s *Server) saveLead(w http.ResponseWriter, r *http.Request) {
	var b ops.Lead
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveLead(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) actLead(w http.ResponseWriter, r *http.Request) {
	var b ops.LeadAction
	if !decode(w, r, &b) {
		return
	}
	v, msg, err := s.ops.ActOnLead(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, map[string]any{"lead": v, "message": msg}, err)
}

// ---- messaging, WhatsApp, scheduler ----

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Messages(r.Context(), tenant(r), r.URL.Query().Get("ucc"), 300)
	ok(w, v, err)
}

func (s *Server) templates(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Templates(r.Context(), tenant(r))
	ok(w, map[string]any{"templates": v, "events": ops.EventVars}, err)
}

func (s *Server) saveTemplate(w http.ResponseWriter, r *http.Request) {
	var b ops.Template
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveTemplate(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) testTemplate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC string `json:"ucc"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.TestMessage(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.UCC)
	ok(w, v, err)
}

func (s *Server) bulkSend(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Template string            `json:"template"`
		Target   string            `json:"target"`
		UCCs     []string          `json:"uccs"`
		Data     map[string]string `json:"data"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.BulkSend(r.Context(), tenant(r), actor(r), b.Template, b.Target, b.UCCs, b.Data)
	ok(w, v, err)
}

func (s *Server) waChats(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.WAChats(r.Context(), tenant(r), 100)
	ok(w, v, err)
}

func (s *Server) waSimulate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		From string `json:"from"`
		Text string `json:"text"`
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	if _, err := s.ops.RoleOf(r.Context(), tenant(r), actor(r)); err != nil {
		fail(w, err, nil)
		return
	}
	if _, okd := dateParam(w, b.Date, "date", true); !okd {
		return
	}
	v, err := s.ops.WhatsAppInbound(r.Context(), tenant(r), b.From, b.Text, b.Date)
	ok(w, map[string]any{"reply": v}, err)
}

// waHook receives WhatsApp messages from the provider. It is not signed in:
// the body must be signed with the firm's WhatsApp secret.
func (s *Server) waHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the body", nil)
		return
	}
	v, err := s.ops.WhatsAppWebhook(r.Context(), r.PathValue("tenant"), body, r.Header.Get("X-Signature"))
	ok(w, map[string]any{"reply": v}, err)
}

func (s *Server) portalWhatsApp(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC string `json:"ucc"`
		On  bool   `json:"on"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := s.ops.SetWhatsApp(r.Context(), tenant(r), actor(r), b.UCC, b.On)
	ok(w, map[string]any{"ucc": c.UCC, "whatsapp": c.WhatsApp}, err)
}

func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	js, rs, err := s.ops.Jobs(r.Context(), tenant(r))
	ok(w, map[string]any{"jobs": js, "runs": rs, "kinds": ops.JobKinds}, err)
}

func (s *Server) saveJob(w http.ResponseWriter, r *http.Request) {
	var b ops.Job
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveJob(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) runJob(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RunJob(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date)
	if err != nil && v.ID != "" {
		ok(w, v, nil) // the failure is recorded in the run
		return
	}
	ok(w, v, err)
}

// ---- DP ----

func (s *Server) dpDesk(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.DP(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) dpTariff(w http.ResponseWriter, r *http.Request) {
	var b ops.DPTariff
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveDPTariff(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) dpBSDA(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On   bool   `json:"on"`
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetBSDA(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.On, b.Date)
	ok(w, v, err)
}

func (s *Server) dpBook(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC   string `json:"ucc"`
		Slips int64  `json:"slips"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.IssueDISBook(r.Context(), tenant(r), actor(r), b.UCC, b.Slips)
	ok(w, v, err)
}

func (s *Server) dpEnterDIS(w http.ResponseWriter, r *http.Request) {
	var b ops.DIS
	if !decode(w, r, &b) {
		return
	}
	if !s.canSee(w, r, b.UCC) {
		return
	}
	v, err := s.ops.EnterDIS(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) dpVerify(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve      bool   `json:"approve"`
		Note         string `json:"note"`
		Confirmation string `json:"confirmation"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.VerifyDIS(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Note, b.Confirmation)
	ok(w, v, err)
}

func (s *Server) dpBatch(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	f, n, err := s.ops.DISBatch(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, map[string]any{"file": string(f), "instructions": n}, err)
}

func (s *Server) dpExecute(w http.ResponseWriter, r *http.Request) {
	var b struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
		Date   string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ExecuteDIS(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.OK, b.Reason, b.Date)
	ok(w, v, err)
}

func (s *Server) dpTxns(w http.ResponseWriter, r *http.Request) {
	u := r.URL.Query().Get("ucc")
	if !s.canSee(w, r, u) {
		return
	}
	v, err := s.ops.DPTransactions(r.Context(), tenant(r), u)
	ok(w, v, err)
}

func (s *Server) dpBilling(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Month string `json:"month"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RunDPBilling(r.Context(), tenant(r), actor(r), b.Month)
	ok(w, map[string]any{"bills": v}, err)
}

func (s *Server) dpRequest(w http.ResponseWriter, r *http.Request) {
	var b ops.DPRequest
	if !decode(w, r, &b) {
		return
	}
	if !s.canSee(w, r, b.UCC) {
		return
	}
	v, err := s.ops.RaiseDPRequest(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) dpActRequest(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Action string `json:"action"`
		Note   string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ActOnDPRequest(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Action, b.Note)
	ok(w, v, err)
}

// ---- MTF ----

func (s *Server) mtf(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.MTF(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) mtfConfig(w http.ResponseWriter, r *http.Request) {
	var b ops.MTFConfig
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveMTFConfig(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) mtfEnrol(w http.ResponseWriter, r *http.Request) {
	var b ops.MTFEnrolment
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.EnrolMTF(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) mtfFund(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
		UCC  string `json:"ucc"`
		ISIN string `json:"isin"`
		Qty  int64  `json:"qty"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.FundMTF(r.Context(), tenant(r), actor(r), b.Date, b.UCC, b.ISIN, b.Qty)
	ok(w, v, err)
}

func (s *Server) mtfCalls(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date string }
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.RaiseMTFCalls(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, map[string]any{"raised": n}, err)
}

func (s *Server) mtfInterest(w http.ResponseWriter, r *http.Request) {
	var b struct{ Date string }
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.PostMTFInterest(r.Context(), tenant(r), actor(r), b.Date, "")
	ok(w, map[string]any{"posted": n}, err)
}

func (s *Server) mtfLiquidation(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	f, _, err := s.ops.MTFLiquidationFile(r.Context(), tenant(r), d)
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Write(f)
}

func (s *Server) mtfReport(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	f, err := s.ops.MTFReport(r.Context(), tenant(r), d)
	if err != nil {
		fail(w, err, nil)
		return
	}
	s.app.Log(r.Context(), tenant(r), actor(r), "mtf.report", d, nil)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Write(f)
}

func (s *Server) mtfPledge(w http.ResponseWriter, r *http.Request) {
	var b struct{ Ref string }
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkMTFPledged(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Ref)
	ok(w, v, err)
}

func (s *Server) mtfClose(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
		Qty  int64  `json:"qty"`
		How  string `json:"how"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CloseMTF(r.Context(), tenant(r), actor(r), b.Date, r.PathValue("id"), b.Qty, b.How)
	ok(w, v, err)
}

// ---- live RMS and front-office files ----

func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	u := r.URL.Query().Get("until")
	if u != "" && (len(u) != 8 || u[2] != ':' || u[5] != ':') {
		writeErr(w, 400, "bad_request", "until must be HH:MM:SS", nil)
		return
	}
	v, err := s.ops.LiveRisk(r.Context(), tenant(r), d, u)
	ok(w, v, err)
}

func (s *Server) liveLiquidation(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	f, _, err := s.ops.LiveLiquidationFile(r.Context(), tenant(r), d, r.URL.Query().Get("until"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	s.app.Log(r.Context(), tenant(r), actor(r), "rms.liquidation_file", d, nil)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Write(f)
}

func (s *Server) frontOffice(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	var f []byte
	var err error
	switch r.PathValue("kind") {
	case "limits":
		f, err = s.ops.LimitFile(r.Context(), tenant(r), d)
	case "holdings":
		f, err = s.ops.HoldingsFile(r.Context(), tenant(r), d)
	case "allocation":
		f, err = s.ops.AllocationFile(r.Context(), tenant(r), d)
	default:
		writeErr(w, 404, "not_found", "unknown file", nil)
		return
	}
	if err != nil {
		fail(w, err, nil)
		return
	}
	s.app.Log(r.Context(), tenant(r), actor(r), "frontoffice."+r.PathValue("kind"), d, nil)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Write(f)
}

// ---- NRI ----

func (s *Server) nri(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.NRI(r.Context(), tenant(r), actor(r), d)
	ok(w, v, err)
}

func (s *Server) setNRI(w http.ResponseWriter, r *http.Request) {
	var b ops.NRIDetails
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetNRI(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) pisFile(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	f, _, err := s.ops.PISFile(r.Context(), tenant(r), d)
	if err != nil {
		fail(w, err, nil)
		return
	}
	s.app.Log(r.Context(), tenant(r), actor(r), "nri.pis_file", d, nil)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Write(f)
}

func (s *Server) nriTDS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, okf := dateParam(w, q.Get("from"), "from", true)
	if !okf {
		return
	}
	to, okt := dateParam(w, q.Get("to"), "to", true)
	if !okt {
		return
	}
	v, err := s.ops.NRITDS(r.Context(), tenant(r), from, to)
	ok(w, v, err)
}

// ---- autopilot ----

func (s *Server) autopilot(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Autopilot(r.Context(), tenant(r), d)
	if f := s.scope(r); f != nil && err == nil {
		mine := []ops.WorkItem{}
		for _, x := range v.Work {
			if x.UCC == "" || f(x.UCC) {
				mine = append(mine, x)
			}
		}
		v.Work = mine
	}
	ok(w, v, err)
}

func (s *Server) autoPolicy(w http.ResponseWriter, r *http.Request) {
	var b ops.AutoPolicy
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetAutoPolicy(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) autoRun(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date  string `json:"date"`
		Stage string `json:"stage"`
		Only  string `json:"only"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RunStage(r.Context(), tenant(r), actor(r), b.Date, b.Stage, b.Only)
	ok(w, map[string]any{"runs": v}, err)
}

func (s *Server) autoSchedule(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On bool `json:"on"`
	}
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.SetAutopilotSchedule(r.Context(), tenant(r), actor(r), b.On)
	ok(w, map[string]any{"jobs": n, "on": b.On}, err)
}

func (s *Server) workItem(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Do   bool   `json:"do"`
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CloseWorkItem(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Do, b.Note)
	ok(w, v, err)
}

// inbox takes several files at once (multipart form, field "files") and
// works out what each one is.
func (s *Server) inbox(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxFileBytes); err != nil {
		writeErr(w, 400, "bad_request", "send the files as multipart/form-data in the field 'files'", nil)
		return
	}
	d, okd := dateParam(w, r.URL.Query().Get("date"), "date", true)
	if !okd {
		return
	}
	var files []ops.InFile
	for _, fh := range r.MultipartForm.File["files"] {
		f, err := fh.Open()
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, maxFileBytes))
		f.Close()
		if err == nil {
			files = append(files, ops.InFile{Name: fh.Filename, Data: b})
		}
	}
	if len(files) == 0 {
		writeErr(w, 400, "bad_request", "no files", nil)
		return
	}
	v, err := s.ops.Intake(r.Context(), tenant(r), actor(r), d, files)
	ok(w, map[string]any{"files": v}, err)
}

func (s *Server) verifyChange(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.VerifyChange(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Code)
	ok(w, v, err)
}

// ---- client insights ----

func (s *Server) insights(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Insights(r.Context(), tenant(r), d, s.scope(r))
	ok(w, v, err)
}

func (s *Server) findInsights(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	n, err := s.ops.FindInsights(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, map[string]any{"found": n}, err)
}

func (s *Server) actInsight(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Action  string `json:"action"`
		Outcome string `json:"outcome"`
		Text    string `json:"text"`
	}
	if !decode(w, r, &b) {
		return
	}
	if f := s.scope(r); f != nil {
		in, err := s.ops.InsightByID(r.Context(), tenant(r), r.PathValue("id"))
		if err != nil || !f(in.UCC) {
			ok(w, nil, ops.ErrNotFound)
			return
		}
	}
	v, err := s.ops.ActOnInsight(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Action, b.Outcome, b.Text)
	ok(w, v, err)
}

func (s *Server) taxPlan(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	u := strings.ToUpper(r.URL.Query().Get("ucc"))
	if f := s.scope(r); f != nil && !f(u) {
		ok(w, nil, ops.ErrNotFound)
		return
	}
	if _, err := s.ops.Client(r.Context(), tenant(r), u); err != nil {
		ok(w, nil, err)
		return
	}
	v, err := s.ops.TaxPlan(r.Context(), tenant(r), u, d)
	ok(w, v, err)
}

func (s *Server) portalInsights(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.ClientInsights(r.Context(), tenant(r), actor(r), d, r.URL.Query().Get("ucc"))
	ok(w, v, err)
}

func (s *Server) portalInsightPrefs(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC string `json:"ucc"`
		Off bool   `json:"off"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := s.ops.SetInsightsOff(r.Context(), tenant(r), actor(r), b.UCC, b.Off)
	ok(w, map[string]any{"ucc": c.UCC, "off": c.NoInsights}, err)
}

func (s *Server) portalTender(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC  string `json:"ucc"`
		Qty  int64  `json:"qty"`
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ClientTenderBuyBack(r.Context(), tenant(r), actor(r), b.Date, r.PathValue("id"), b.UCC, b.Qty)
	ok(w, v, err)
}

// ---- WhatsApp consent ----

func (s *Server) waConsents(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Consents(r.Context(), tenant(r), s.scope(r))
	ok(w, v, err)
}

func (s *Server) waRecordConsent(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On     bool   `json:"on"`
		Source string `json:"source"`
		Ref    string `json:"ref"`
	}
	if !decode(w, r, &b) {
		return
	}
	u := strings.ToUpper(r.PathValue("ucc"))
	if f := s.scope(r); f != nil && !f(u) {
		ok(w, nil, ops.ErrNotFound)
		return
	}
	c, err := s.ops.RecordConsent(r.Context(), tenant(r), actor(r), u, b.On, b.Source, b.Ref)
	ok(w, map[string]any{"ucc": c.UCC, "consent": c.WAConsent}, err)
}

func (s *Server) waCampaign(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Channel string `json:"channel"`
		Limit   int    `json:"limit"`
	}
	if !decode(w, r, &b) {
		return
	}
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	v, err := s.ops.ConsentCampaign(r.Context(), tenant(r), actor(r), b.Channel, scheme+"://"+r.Host, b.Limit)
	ok(w, v, err)
}

// optinPage is the public page a consent link opens; answering needs a POST
// so that link scanners which fetch the page do not answer for the client.
func (s *Server) optinPage(w http.ResponseWriter, r *http.Request) {
	t, tok := r.PathValue("tenant"), r.PathValue("token")
	var p ops.ConsentPage
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		p, _ = s.ops.AnswerConsent(r.Context(), t, tok, r.PostForm.Get("answer") == "yes")
	} else {
		p, _, _ = s.ops.OpenConsent(r.Context(), t, tok)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	_ = ops.ConsentHTML.Execute(w, p)
}

// ---- year-end packs ----

func (s *Server) yearEnds(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.YearEnds(r.Context(), tenant(r), r.URL.Query().Get("fy"), d, s.scope(r))
	ok(w, v, err)
}

func (s *Server) sendYearEnd(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date string   `json:"date"`
		UCCs []string `json:"uccs"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Date == "" {
		b.Date = time.Now().Format("2006-01-02")
	}
	n, failed, err := s.ops.SendYearEnd(r.Context(), tenant(r), actor(r), r.PathValue("fy"), b.Date, b.UCCs)
	ok(w, map[string]any{"sent": n, "failed": failed}, err)
}

func (s *Server) yearEndDoc(w http.ResponseWriter, r *http.Request) {
	if !s.canSee(w, r, r.PathValue("ucc")) {
		return
	}
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	p, err := s.ops.YearEnd(r.Context(), tenant(r), r.PathValue("ucc"), r.PathValue("fy"), d)
	if err != nil {
		fail(w, err, nil)
		return
	}
	b, err := ops.RenderYearEnd(p)
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.Write(b)
}

// ---- circulars and the compliance calendar ----

func (s *Server) circulars(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	cs, err := s.ops.Circulars(r.Context(), tenant(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	ds, err := s.ops.Duties(r.Context(), tenant(r), d)
	ok(w, map[string]any{"circulars": cs, "duties": ds}, err)
}

func (s *Server) actCircular(w http.ResponseWriter, r *http.Request) {
	var b ops.CircularAction
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ActOnCircular(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) saveDuty(w http.ResponseWriter, r *http.Request) {
	var b ops.Duty
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveDuty(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) dutyDone(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date     string `json:"date"`
		Evidence string `json:"evidence"`
	}
	if !decode(w, r, &b) {
		return
	}
	if _, okd := dateParam(w, b.Date, "date", false); !okd {
		return
	}
	v, err := s.ops.DoneDuty(r.Context(), tenant(r), actor(r), b.Date, r.PathValue("id"), b.Evidence)
	ok(w, v, err)
}

// ---- clearing deposits ----

func (s *Server) deposits(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.Deposits(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) saveDeposit(w http.ResponseWriter, r *http.Request) {
	var b ops.Deposit
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveDeposit(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) releaseDeposit(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ReleaseDeposit(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Status, b.Note)
	ok(w, v, err)
}

func (s *Server) security(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Security(r.Context(), tenant(r), actor(r), r.URL.Query().Get("verify") == "1")
	ok(w, v, err)
}

func (s *Server) revealClient(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Field  string `json:"field"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RevealClient(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Field, b.Reason)
	ok(w, map[string]string{"value": v}, err)
}

func (s *Server) ledgyAsk(w http.ResponseWriter, r *http.Request) {
	var b struct{ Q, View, Date string }
	if !decode(w, r, &b) {
		return
	}
	if b.Date == "" {
		b.Date = time.Now().In(time.FixedZone("IST", 19800)).Format("2006-01-02")
	}
	v, err := s.ops.Ledgy(r.Context(), tenant(r), actor(r), b.Q, b.View, b.Date)
	ok(w, v, err)
}

func (s *Server) ledgySuggest(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"questions": s.ops.LedgySuggestions(r.URL.Query().Get("view"))})
}

func (s *Server) ledgyArticle(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.LedgyArticle(r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) ledgyFeedback(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Q, View, Article string
		Helpful          bool
	}
	if !decode(w, r, &b) {
		return
	}
	ok(w, map[string]bool{"ok": true}, s.ops.LedgyFeedback(r.Context(), tenant(r), actor(r), b.Q, b.View, b.Article, b.Helpful))
}

func (s *Server) ledgyGaps(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.LedgyGaps(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) edition(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.ops.EditionStatus(r.Context(), tenant(r)))
}

// devEdition switches the firm's edition on a development server, to try
// each one. In production the provider sets editions in configuration.
func (s *Server) devEdition(w http.ResponseWriter, r *http.Request) {
	var b struct{ Edition string }
	if !decode(w, r, &b) {
		return
	}
	if role, _ := s.ops.RoleOf(r.Context(), tenant(r), actor(r)); role != ops.RoleAdmin {
		fail(w, fmt.Errorf("%w: only an Admin can switch the edition", ops.ErrForbidden), nil)
		return
	}
	if err := s.ops.SetEdition(tenant(r), b.Edition); err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, s.ops.EditionStatus(r.Context(), tenant(r)))
}

func (s *Server) backups(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BackupsStatus(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

// backupNow starts a backup and replies at once; the screen shows it
// running and then its result.
func (s *Server) backupNow(w http.ResponseWriter, r *http.Request) {
	err := s.ops.BackupNowAsync(r.Context(), tenant(r), actor(r))
	ok(w, map[string]any{"started": err == nil}, err)
}

func (s *Server) verifyBackup(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.VerifyBackup(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) backupPolicy(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Keep      backup.Keep `json:"keep"`
		YearEnd   string      `json:"year_end"`
		Fallback  string      `json:"fallback"`
		FallbackC string      `json:"fallback_commodity"`
	}
	if !decode(w, r, &b) {
		return
	}
	_, err := s.ops.SetBackupPolicy(r.Context(), tenant(r), actor(r), b.Keep, b.YearEnd, b.Fallback, b.FallbackC)
	ok(w, map[string]any{"saved": err == nil}, err)
}

func (s *Server) holidays(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Holidays(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) checkHolidays(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Year int `json:"year"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CheckHolidays(r.Context(), tenant(r), actor(r), b.Year)
	ok(w, v, err)
}

func (s *Server) uploadHolidays(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Year     int      `json:"year"`
		Exchange string   `json:"exchange"`
		Kind     string   `json:"kind"`
		Segments []string `json:"segments"`
		CSV      string   `json:"csv"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.UploadHolidays(r.Context(), tenant(r), actor(r), b.Year, b.Exchange, b.Kind, b.Segments, []byte(b.CSV))
	ok(w, v, err)
}

func (s *Server) decideHolidays(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.DecideHolidays(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Note)
	ok(w, v, err)
}

func (s *Server) dashLayouts(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.DashboardLayouts(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) saveDashLayout(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Widgets []ops.DashWidget `json:"widgets"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveDashboardLayout(r.Context(), tenant(r), actor(r), b.Widgets)
	ok(w, v, err)
}

func (s *Server) saveFirmDash(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Widgets []ops.DashWidget `json:"widgets"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveFirmDashboard(r.Context(), tenant(r), actor(r), b.Widgets)
	ok(w, v, err)
}

func (s *Server) aiUsage(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.AIUsage(r.Context(), tenant(r), actor(r), r.URL.Query().Get("month"))
	ok(w, v, err)
}

// provider wraps the provider's endpoints: bearer LEDGERLINE_PROVIDER_KEY.
func (s *Server) provider(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.ProviderKey == "" || len(tok) < 24 || subtle.ConstantTimeCompare([]byte(tok), []byte(s.ProviderKey)) != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "Provider endpoints need the provider key.", nil)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		h(w, r)
	}
}

// providerCredit adds prepaid AI credit to a firm after it has paid.
func (s *Server) providerCredit(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Tenant string `json:"tenant"`
		Amount string `json:"amount"`
		Ref    string `json:"ref"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !s.knownTenant(b.Tenant) {
		writeErr(w, http.StatusNotFound, "not_found", "No such firm on this server.", nil)
		return
	}
	amt, err := money.ParseAmount(b.Amount)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid", "amount must be rupees, for example 5000 or 5000.00", nil)
		return
	}
	v, err := s.ops.AddAICredit(r.Context(), b.Tenant, "provider", amt, b.Ref)
	ok(w, v, err)
}

// providerUsage lists every firm's AI use for a month: provider cost,
// charges and margin, as JSON or (format=csv) CSV for invoicing.
func (s *Server) providerUsage(w http.ResponseWriter, r *http.Request) {
	month := r.URL.Query().Get("month")
	ts := s.Tenants()
	sort.Strings(ts)
	rows, err := s.ops.ProviderAIUsage(r.Context(), ts, month)
	if err != nil {
		fail(w, err, nil)
		return
	}
	if r.URL.Query().Get("format") != "csv" {
		writeJSON(w, 200, rows)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ai-usage-`+month+`.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"Firm", "Answers", "Tokens", "Provider cost (INR)", "Charged (INR, before GST)", "Margin (INR)", "Credit added in month (INR)", "Credit balance (INR)"})
	for _, x := range rows {
		cw.Write([]string{x.Tenant, strconv.Itoa(x.Answers), strconv.Itoa(x.Tokens), x.CostINR, x.Charged.String(), x.Margin, x.ToppedUp.String(), x.Balance.String()})
	}
	cw.Flush()
}
