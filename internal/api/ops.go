package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

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
