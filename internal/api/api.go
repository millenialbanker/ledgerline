// Package api exposes the back office over HTTP/JSON.
//
// Every request is authenticated with "Authorization: Bearer <key>"; the key
// maps to exactly one tenant. Mutating requests also need "X-Actor", the user
// on whose behalf the call is made, which is recorded in the audit log.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"ledgerline/internal/recon"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/ops"
)

const (
	maxFileBytes = 64 << 20
	maxJSONBytes = 1 << 20
)

type ctxKey int

const (
	keyTenant ctxKey = iota
	keyActor
	keyReqID
)

// Server routes requests to the App.
type Server struct {
	app  *app.App
	ops  *ops.Ops
	dev  bool
	keys map[string]string // API key -> tenant
	log  *slog.Logger
	mux  *http.ServeMux
	seq  atomic.Int64

	loginLimit limiter
	// ProviderKey authorises the software provider's own endpoints under
	// /provider/ (adding AI credit, usage for invoicing). Empty disables them.
	ProviderKey string
	// PublicURL is the address browsers use (LEDGERLINE_PUBLIC_URL), for
	// links back from DigiLocker; empty means the request's host.
	PublicURL string
}

// New builds a server. keys maps API keys to tenant IDs.
func New(a *app.App, o *ops.Ops, keys map[string]string, log *slog.Logger, dev bool) *Server {
	s := &Server{app: a, ops: o, dev: dev, keys: keys, log: log, mux: http.NewServeMux(), loginLimit: limiter{max: 30, per: 15 * time.Minute}}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	s.route("GET /v1/adapters", s.adapters, false)
	s.route("POST /v1/files", s.uploadFile, true)
	s.route("GET /v1/trades", s.trades, false)
	s.route("POST /v1/recon/runs", s.reconRun, true)
	s.route("GET /v1/recon/breaks", s.breaks, false)
	s.route("GET /v1/recon/playbooks", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, recon.Playbooks()) }, false)
	s.route("POST /v1/recon/breaks/{id}/resolve", s.resolveBreak, true)
	s.route("POST /v1/billing/runs", s.billingRun, true)
	s.route("POST /v1/charges/quote", s.quote, false)
	s.route("POST /v1/journal", s.postJournal, true)
	s.route("POST /v1/journal/{id}/reverse", s.reverse, true)
	s.route("GET /v1/journal/{id}", s.getEntry, false)
	s.route("GET /v1/ledger/trial-balance", s.trialBalance, false)
	s.route("GET /v1/ledger/statement", s.statement, false)
	s.route("GET /v1/audit", s.audit, false)
	s.mux.HandleFunc("POST /provider/ai/credit", s.provider(s.providerCredit))
	s.mux.HandleFunc("GET /provider/ai/usage", s.provider(s.providerUsage))
	s.opsRoutes()
	s.authRoutes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get("X-Request-ID")
	if id == "" {
		id = fmt.Sprintf("req-%d-%d", time.Now().UnixMilli(), s.seq.Add(1))
	}
	w.Header().Set("X-Request-ID", id)
	start := time.Now()
	rw := &statusWriter{ResponseWriter: w, status: 200}
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("panic", "request_id", id, "panic", p)
			writeErr(rw, http.StatusInternalServerError, "internal", "Unexpected server error. Quote request ID "+id+" when reporting it.", nil)
		}
		s.log.Info("request", "request_id", id, "method", r.Method, "path", r.URL.Path, "status", rw.status, "ms", time.Since(start).Milliseconds())
	}()
	s.mux.ServeHTTP(rw, r.WithContext(context.WithValue(r.Context(), keyReqID, id)))
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) { w.status = c; w.ResponseWriter.WriteHeader(c) }

func (s *Server) route(pattern string, h http.HandlerFunc, mutating bool) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		var tenant, actor string
		if r.Header.Get("Authorization") != "" {
			t, ok := s.auth(r)
			if !ok {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeErr(w, http.StatusUnauthorized, "unauthorized", "Send a valid API key as 'Authorization: Bearer <key>'.", nil)
				return
			}
			tenant, actor = t, strings.TrimSpace(r.Header.Get("X-Actor"))
			if strings.Contains(actor, ":") {
				writeErr(w, http.StatusBadRequest, "bad_actor", "X-Actor must be a user ID.", nil)
				return
			}
		} else {
			t, st, _, err := s.sessionAuth(r)
			if err != nil {
				writeErr(w, http.StatusUnauthorized, "signed_out", "Sign in to continue.", nil)
				return
			}
			if st.Restricted() && !setupPath(r.URL.Path) {
				writeErr(w, http.StatusForbidden, "setup_required", "Finish setting up your sign-in (new password or two-factor authentication) first.", st)
				return
			}
			if mutating && r.Header.Get(csrfHeader) != csrfValue {
				writeErr(w, http.StatusForbidden, "csrf", "Missing the "+csrfHeader+" header.", nil)
				return
			}
			tenant, actor = t, st.User
		}
		if mutating && actor == "" {
			writeErr(w, http.StatusBadRequest, "actor_required", "Set the X-Actor header to the user making this change; it is recorded in the audit log.", nil)
			return
		}
		ctx := context.WithValue(r.Context(), keyTenant, tenant)
		ctx = context.WithValue(ctx, keyActor, actor)
		if mutating && s.ops != nil {
			if _, err := s.ops.RoleOf(ctx, tenant, actor); err != nil {
				fail(w, err, nil)
				return
			}
		}
		// Branch staff and authorised persons reach only the client-level
		// screens; everything firm-wide is closed to them.
		if s.ops != nil && !strings.HasPrefix(pattern, "POST /v1/auth/") {
			if role, err := s.ops.RoleOf(ctx, tenant, actor); err == nil && (role == ops.RoleBranch || role == ops.RoleAP) && !scopedRoutes[pattern] {
				writeErr(w, http.StatusForbidden, "forbidden", "This is not available to "+role+" logins.", nil)
				return
			} else if err == nil && role == ops.RoleClient && !clientRoutes[pattern] {
				writeErr(w, http.StatusForbidden, "forbidden", "This is not available in the client portal.", nil)
				return
			}
		}
		h(w, r.WithContext(ctx))
	})
}

func (s *Server) auth(r *http.Request) (string, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return "", false
	}
	for k, t := range s.keys {
		if subtle.ConstantTimeCompare([]byte(k), []byte(tok)) == 1 {
			return t, true
		}
	}
	return "", false
}

func tenant(r *http.Request) string { return r.Context().Value(keyTenant).(string) }
func actor(r *http.Request) string  { return r.Context().Value(keyActor).(string) }

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string, details any) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg, Details: details}})
}

// fail maps domain errors to HTTP statuses.
func fail(w http.ResponseWriter, err error, details any) {
	switch {
	case errors.Is(err, ledger.ErrInvalid):
		writeErr(w, 422, "invalid_entry", err.Error(), details)
	case errors.Is(err, ledger.ErrConflict), errors.Is(err, ingest.ErrTradeConflict):
		writeErr(w, 409, "conflict", err.Error(), details)
	case errors.Is(err, ledger.ErrNotFound), errors.Is(err, app.ErrNotFound):
		writeErr(w, 404, "not_found", err.Error(), details)
	case errors.Is(err, app.ErrState):
		writeErr(w, 409, "invalid_state", err.Error(), details)
	case errors.Is(err, ingest.ErrRejected):
		writeErr(w, 422, "file_rejected", err.Error(), details)
	case errors.Is(err, ops.ErrForbidden):
		writeErr(w, 403, "forbidden", err.Error(), details)
	case errors.Is(err, docs.ErrStale):
		writeErr(w, 409, "stale", err.Error(), details)
	case errors.Is(err, docs.ErrNotFound):
		writeErr(w, 404, "not_found", err.Error(), details)
	case errors.Is(err, ops.ErrNoMarketData):
		writeErr(w, 409, "no_market_data", err.Error(), details)
	case errors.Is(err, ops.ErrInvalid):
		var ve *ops.ValidationError
		if errors.As(err, &ve) {
			writeErr(w, 422, "invalid", "Some fields need attention.", map[string]any{"fields": ve.Fields})
			return
		}
		writeErr(w, 422, "invalid", strings.TrimPrefix(err.Error(), "invalid: "), details)
	default:
		writeErr(w, 400, "bad_request", err.Error(), details)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, 400, "bad_json", "Request body is not valid JSON for this endpoint: "+err.Error(), nil)
		return false
	}
	return true
}

func dateParam(w http.ResponseWriter, v, name string, required bool) (string, bool) {
	if v == "" && !required {
		return "", true
	}
	if !ledger.ValidDate(v) {
		writeErr(w, 400, "bad_date", fmt.Sprintf("%s must be a date in YYYY-MM-DD form", name), nil)
		return "", false
	}
	return v, true
}

// ---- handlers ----

func (s *Server) adapters(w http.ResponseWriter, r *http.Request) {
	type item struct {
		Name, Version, Origin, Exchange, Segment, Note string
	}
	out := []item{}
	for _, sp := range s.app.Ingest.Registry.List() {
		out = append(out, item{sp.Name, sp.Version, string(sp.Origin), sp.Exchange, sp.Segment, sp.Note})
	}
	writeJSON(w, 200, out)
}

func (s *Server) uploadFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source := q.Get("source")
	if source == "" {
		writeErr(w, 400, "source_required", "Pass ?source=<layout name>, for example NSE-CM-TRADE. GET /v1/adapters lists them.", nil)
		return
	}
	name := q.Get("name")
	if name == "" {
		name = source + ".upload"
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFileBytes))
	if err != nil {
		writeErr(w, 413, "too_large", "File exceeds the 64 MB upload limit.", nil)
		return
	}
	if len(data) == 0 {
		writeErr(w, 400, "empty_file", "The request body is empty; send the file bytes as the body.", nil)
		return
	}
	var res ingest.Result
	if s.ops != nil && ops.IsMasterSource(source) {
		res, err = s.ops.LoadFile(r.Context(), tenant(r), actor(r), source, q.Get("date"), name, data)
	} else {
		if s.ops != nil {
			if err := s.ops.Require(r.Context(), tenant(r), actor(r), "Loading files", ops.RoleMaker, ops.RoleChecker, ops.RoleRisk, ops.RoleCompliance); err != nil {
				fail(w, err, nil)
				return
			}
		}
		res, err = s.app.IngestFile(r.Context(), tenant(r), actor(r), source, q.Get("version"), name, data)
	}
	if err != nil {
		fail(w, err, res)
		return
	}
	writeJSON(w, 201, res)
}

func (s *Server) trades(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d, ok := dateParam(w, q.Get("date"), "date", true)
	if !ok {
		return
	}
	origin := ingest.Origin(strings.ToUpper(q.Get("origin")))
	if origin != "" && origin != ingest.FromExchange && origin != ingest.FromOMS {
		writeErr(w, 400, "bad_origin", "origin must be EXCHANGE or OMS", nil)
		return
	}
	evs, err := s.app.Trades.List(r.Context(), tenant(r), origin, d)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, evs)
}

func (s *Server) reconRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Date string `json:"date"`
	}
	if !decode(w, r, &body) {
		return
	}
	if _, ok := dateParam(w, body.Date, "date", true); !ok {
		return
	}
	if !s.allowed(w, r, "Reconciliation", ops.RoleMaker, ops.RoleChecker) {
		return
	}
	sum, err := s.app.RunRecon(r.Context(), tenant(r), actor(r), body.Date)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, sum)
}

func (s *Server) breaks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d, ok := dateParam(w, q.Get("date"), "date", false)
	if !ok {
		return
	}
	st := strings.ToUpper(q.Get("status"))
	if st != "" && st != app.BreakOpen && st != app.BreakResolved && st != app.BreakCleared {
		writeErr(w, 400, "bad_status", "status must be OPEN, RESOLVED or CLEARED", nil)
		return
	}
	bs, err := s.app.Breaks.List(r.Context(), tenant(r), d, st)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, bs)
}

func (s *Server) resolveBreak(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Resolution string `json:"resolution"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !s.allowed(w, r, "Resolving breaks", ops.RoleMaker, ops.RoleChecker) {
		return
	}
	b, err := s.app.ResolveBreak(r.Context(), tenant(r), actor(r), r.PathValue("id"), body.Resolution)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) billingRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Date             string               `json:"date"`
		SettlementPrices map[string]money.Dec `json:"settlement_prices"`
		Rebill           bool                 `json:"rebill"`
	}
	if !decode(w, r, &body) {
		return
	}
	if _, ok := dateParam(w, body.Date, "date", true); !ok {
		return
	}
	if s.ops != nil {
		roles := []string{ops.RoleMaker, ops.RoleChecker}
		if body.Rebill {
			roles = []string{ops.RoleChecker}
		}
		if err := s.ops.Require(r.Context(), tenant(r), actor(r), "Billing", roles...); err != nil {
			fail(w, err, nil)
			return
		}
		if err := s.ops.DayOpenError(r.Context(), tenant(r), body.Date); err != nil {
			fail(w, err, nil)
			return
		}
		if len(body.SettlementPrices) == 0 {
			if m, _, err := s.ops.Market(r.Context(), tenant(r), body.Date); err == nil {
				body.SettlementPrices = m.SettlementPrices()
			}
		}
	}
	res, err := s.app.RunBillingOpts(r.Context(), tenant(r), actor(r), body.Date, body.SettlementPrices, body.Rebill)
	if err != nil {
		fail(w, err, res)
		return
	}
	status := 201
	if len(res.Conflicts) > 0 {
		status = 207
	}
	out := struct {
		app.BillingResult
		Derivatives *ops.DerivResult `json:"derivatives,omitempty"`
	}{BillingResult: res}
	if s.ops != nil {
		d, err := s.ops.SettleDerivatives(r.Context(), tenant(r), actor(r), body.Date)
		if err != nil {
			fail(w, err, out)
			return
		}
		out.Derivatives = &d
	}
	writeJSON(w, status, out)
}

func (s *Server) quote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		charges.Leg
		UCC string `json:"ucc"`
		Lot int64  `json:"lot"`
	}
	if !decode(w, r, &body) {
		return
	}
	leg := body.Leg
	if leg.Date == "" {
		leg.Date = time.Now().In(time.FixedZone("IST", 19800)).Format("2006-01-02")
	}
	plan := ""
	var rule charges.BrokerageRule
	if body.UCC != "" && s.ops != nil {
		rl, name, ok, err := s.ops.QuoteRule(r.Context(), tenant(r), body.UCC, leg.Bucket, leg.Date)
		if err != nil {
			fail(w, err, nil)
			return
		}
		if ok {
			rule, plan, leg.NoBrokerage = rl, name, true
		}
	}
	b, err := s.app.Schedule.Compute(leg)
	if err != nil {
		fail(w, err, nil)
		return
	}
	if leg.NoBrokerage {
		brok := rule.Compute(leg.Bucket, leg.Price.MulInt(leg.Qty), leg.Qty, max(body.Lot, 1)).Round(money.UnitPaisa)
		gst := brok.Dec().Pct(s.app.Schedule.GSTPct).Round(money.UnitPaisa)
		b.Brokerage, b.GST, b.Total = brok, b.GST+gst, b.Total+brok+gst
		b.Rule = "Plan " + plan + " · " + rule.Describe() + " · statutory " + b.Rule
	}
	writeJSON(w, 200, map[string]any{"breakdown": b, "plan": plan, "schedule": s.app.Schedule.Version, "sample_rates": s.app.Schedule.Sample})
}

func (s *Server) postJournal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key       string           `json:"key"`
		ValueDate string           `json:"value_date"`
		Narration string           `json:"narration"`
		Postings  []ledger.Posting `json:"postings"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !s.allowed(w, r, "Manual journal entries", ops.RoleMaker, ops.RoleChecker) {
		return
	}
	e, created, err := s.app.PostManual(r.Context(), ledger.Entry{Tenant: tenant(r), Key: body.Key, ValueDate: body.ValueDate, Narration: body.Narration, Postings: body.Postings, CreatedBy: actor(r)})
	if err != nil {
		fail(w, err, nil)
		return
	}
	status := 201
	if !created {
		status = 200
	}
	writeJSON(w, status, e)
}

func (s *Server) reverse(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !s.allowed(w, r, "Reversing entries", ops.RoleChecker) {
		return
	}
	e, created, err := s.app.Reverse(r.Context(), tenant(r), actor(r), r.PathValue("id"), body.Reason)
	if err != nil {
		fail(w, err, nil)
		return
	}
	status := 201
	if !created {
		status = 200
	}
	writeJSON(w, status, e)
}

func (s *Server) getEntry(w http.ResponseWriter, r *http.Request) {
	e, err := s.app.Ledger.Store.Get(r.Context(), tenant(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, e)
}

func (s *Server) trialBalance(w http.ResponseWriter, r *http.Request) {
	d, ok := dateParam(w, r.URL.Query().Get("as_of"), "as_of", false)
	if !ok {
		return
	}
	tb, err := s.app.Ledger.TrialBalance(r.Context(), tenant(r), d)
	if err != nil {
		fail(w, err, nil)
		return
	}
	if s.ops != nil {
		// Restricted accounts are folded into one line for users who may
		// not see them, so the totals still agree.
		vis := s.ops.AccountVisible(r.Context(), tenant(r), actor(r))
		rows := []ledger.TBRow{}
		var hidden ledger.TBRow
		for _, row := range tb.Rows {
			if vis(row.Account) {
				rows = append(rows, row)
				continue
			}
			hidden.Account, hidden.Debit, hidden.Credit = "RESTRICTED", hidden.Debit+row.Debit, hidden.Credit+row.Credit
		}
		if hidden.Account != "" {
			rows = append(rows, hidden)
		}
		tb.Rows = rows
	}
	writeJSON(w, 200, tb)
}

func (s *Server) statement(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	acct := q.Get("account")
	if acct == "" && q.Get("ucc") != "" {
		acct = ledger.ClientAccount(strings.ToUpper(q.Get("ucc")))
	}
	if acct == "" {
		writeErr(w, 400, "account_required", "Pass ?ucc=<client code> or ?account=<account code>.", nil)
		return
	}
	from, ok := dateParam(w, q.Get("from"), "from", false)
	if !ok {
		return
	}
	to, ok := dateParam(w, q.Get("to"), "to", false)
	if !ok {
		return
	}
	if u, isClient := strings.CutPrefix(acct, "CLIENT:"); isClient && !s.canSee(w, r, u) {
		return
	} else if !isClient && s.scope(r) != nil {
		if role, _ := s.ops.RoleOf(r.Context(), tenant(r), actor(r)); role == ops.RoleBranch || role == ops.RoleAP {
			writeErr(w, 403, "forbidden", "Only client ledgers are available to this login.", nil)
			return
		}
	}
	if s.ops != nil && !s.ops.AccountVisible(r.Context(), tenant(r), actor(r))(acct) {
		writeErr(w, 403, "forbidden", "This account is restricted.", nil)
		return
	}
	st, err := s.app.Ledger.Statement(r.Context(), tenant(r), acct, from, to)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	ev, err := s.app.Audit.List(r.Context(), tenant(r), limit)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 200, ev)
}

// ParseKeys parses "key1:tenant1,key2:tenant2".
func ParseKeys(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, t, ok := strings.Cut(part, ":")
		if !ok || len(k) < 16 || t == "" {
			return nil, fmt.Errorf("api key entry %q must be <key of 16+ chars>:<tenant>", part)
		}
		out[k] = t
	}
	return out, nil
}

// scopedRoutes are the routes open to branch staff and authorised persons.
// Each filters its data to the clients they may see.
var scopedRoutes = map[string]bool{
	"POST /v1/ledgy/ask": true, "GET /v1/ledgy/suggest": true, "GET /v1/ledgy/articles/{id}": true, "POST /v1/ledgy/feedback": true,
	"GET /v1/me": true, "GET /v1/clients": true, "GET /v1/clients/{ucc}": true, "GET /v1/client-reports": true,
	"GET /v1/client-reports/{kind}": true, "GET /v1/ledger/statement": true, "GET /v1/notes": true,
	"GET /v1/notes/{date}/{ucc}/document": true, "GET /v1/receipts": true, "POST /v1/receipts": true, "GET /v1/payouts": true,
	"GET /v1/payouts/withdrawable": true, "POST /v1/payouts": true, "GET /v1/debits": true, "GET /v1/rms": true,
	"GET /v1/tags": true, "GET /v1/brokerage/client/{ucc}": true, "GET /v1/partners": true, "GET /v1/settings": true,
	"GET /v1/prepaid": true, "POST /v1/prepaid/subscriptions": true,
	"GET /v1/clients/{ucc}/documents": true, "POST /v1/clients/{ucc}/documents": true, "GET /v1/clients/{ucc}/documents/{id}": true,
	"GET /v1/changes": true, "POST /v1/changes": true, "GET /v1/tickets": true, "POST /v1/tickets": true, "POST /v1/tickets/{id}": true,
	"GET /v1/external-trades": true, "POST /v1/external-trades": true, "GET /v1/crm": true, "POST /v1/leads": true, "POST /v1/leads/{id}/act": true,
	"GET /v1/autopilot": true, "POST /v1/work/{id}": true,
	"GET /v1/insights": true, "POST /v1/insights/{id}": true, "GET /v1/tax-plan": true,
	"POST /v1/rms/overrides": true, "GET /v1/branches": true,
	"GET /v1/year-end": true, "GET /v1/year-end/{fy}/{ucc}/document": true,
	"GET /v1/whatsapp/consents": true, "POST /v1/clients/{ucc}/whatsapp-consent": true,
}

// clientRoutes are the routes open to a client's portal login, each
// limited to the client's own accounts.
var clientRoutes = map[string]bool{
	"GET /v1/me": true, "GET /v1/portal": true, "GET /v1/clients": true, "GET /v1/client-reports": true, "GET /v1/client-reports/{kind}": true,
	"GET /v1/notes/{date}/{ucc}/document": true, "GET /v1/payouts/withdrawable": true, "POST /v1/payouts": true,
	"POST /v1/changes": true, "GET /v1/tickets": true, "POST /v1/tickets": true, "POST /v1/tickets/{id}": true,
	"GET /v1/external-trades": true, "POST /v1/external-trades": true, "POST /v1/external-trades/{ucc}/{id}/delete": true,
	"GET /v1/clients/{ucc}/documents": true, "POST /v1/clients/{ucc}/documents": true, "GET /v1/clients/{ucc}/documents/{id}": true,
	"POST /v1/portal/whatsapp": true, "POST /v1/changes/{id}/verify": true,
	"GET /v1/year-end/{fy}/{ucc}/document": true,
	"GET /v1/portal/insights":              true, "POST /v1/portal/insights/prefs": true, "POST /v1/portal/buyback/{id}/tender": true,
}

// scope is the client filter for the request's user (nil: every client).
func (s *Server) scope(r *http.Request) func(string) bool {
	if s.ops == nil {
		return nil
	}
	return s.ops.ClientScope(r.Context(), tenant(r), actor(r))
}

// canSee writes a 404 and returns false when the user may not see ucc.
func (s *Server) canSee(w http.ResponseWriter, r *http.Request, ucc string) bool {
	if f := s.scope(r); f != nil && !f(ucc) {
		writeErr(w, http.StatusNotFound, "not_found", "No such client.", nil)
		return false
	}
	return true
}
