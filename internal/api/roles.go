package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"ledgerline/internal/ops"
	"ledgerline/internal/screens"
)

// ---- roles: the screens each route belongs to, and the Roles screen ----
//
// Someone holding a role the firm made or edited (ops/roles.go) reaches a
// route only when one of its roles may look at (a read) or work on (a
// change) one of the route's screens. The permission checks behind the
// route then decide each action, knowing the screens (ops.WithAreas). An
// unedited built-in role is never stopped here: it reaches what it always
// has, and its checks are by name.

// routeScreens maps a route's path (after /v1/) to the screens it serves.
// The longest key that is the path or a parent of it wins; "GET path" or
// "POST path" keys apply to one method only. A route serving several
// screens is open to anyone with any of them.
var routeScreens = map[string]string{
	// Overview
	"dashboard": "dashboard", "eod": "dashboard settlement",
	"boards": "boards", "board-cards": "boards", "board-collections": "boards", "board-files": "boards", "board-imports": "boards",
	"board-inbox": "boards", "board-notify": "boards", "board-planner": "boards", "board-templates": "boards",
	"chat":      "chat",
	"autopilot": "autopilot", "steps": "steps", "work": "autopilot", "jobs": "jobs", "outbox": "jobs", "rpa": "rpa",
	"periodic": "periodic", "circulars": "circulars", "duties": "circulars", "holidays": "circulars", "inbox": "circulars", "compliance/profile": "circulars kyc",
	// Operations
	"parallel": "parallel", "firm-demats": "settings xfiles selfinspect", "exchange-files": "xfiles", "exchange-files-alerts": "xfiles", "exchange-files-checklist": "xfiles", "mcx/sftp": "xfiles", "mcx/contracts": "xfiles",
	"bse/sftp": "xfiles", "ccil": "xfiles", "nse-extranet": "xfiles", "notis": "xfiles",
	"clearing-data": "ccdata", "ncl-feeds": "ccdata", "ncms": "ccdata", "ncl-margins": "ccdata margin rms", "ncl-margins/key": "connections",
	"files": "files", "sample": "files", "adapters": "files", "audit": "audit files",
	"migration": "migrate", "history-files": "migrate", "history": "clients migrate", "archive": "archive",
	"recon": "recon", "ccm": "recon", "trades": "recon billing",
	"billing": "billing", "journal": "billing ledger books", "charges": "charges billing",
	"settlement": "settlement", "shortages": "settlement", "cuspa": "settlement", "notes": "notes",
	"clients": "clients", "external-trades": "clients", "portal": "clients",
	"clients/{ucc}/risk": "kyc clients", "clients/{ucc}/rekyc": "kyc clients", "clients/{ucc}/mapping": "partners clients",
	"clients/{ucc}/mcx-move": "rms clients", "clients/{ucc}/funds-ledger": "ledger money clients", "clients/{ucc}/ledger-transfer": "ledger clients",
	"clients/{ucc}/whatsapp-consent": "messages clients", "clients/{ucc}/segment-status": "clients uccapi", "clients/{ucc}/documents": "clients statements",
	"applications": "apps", "changes": "changes",
	"ucc-api": "uccapi", "bse-ucc-blocked": "uccapi", "segment-status": "uccapi", "segmon": "uccapi reports", "mnrl": "mnrl",
	"corporate-actions": "corp", "buybacks": "corp",
	"client-reports": "statements", "dispatch": "dispatch", "dispatch.csv": "dispatch",
	"messages": "messages", "templates": "messages", "whatsapp": "messages", "whatsapp/consents": "messages clients", "year-end": "yearend",
	// Risk
	"rms": "rms", "funds-awaiting": "rms", "mcx-moves": "rms",
	"live": "live", "bow": "live", "bow-funds": "live", "finspot-api": "live", "frontoffice": "live",
	"margin": "margin", "payout-holds": "payouthold",
	"collateral": "collateral", "collateral-file": "collateral", "pledges": "collateral",
	"cdsl-pledges": "cdslpledge", "cdsl-epi": "cdslepi", "allocation": "allocation", "deposits": "deposits",
	"debits": "debits", "interest": "debits", "legacy-debts": "legacy", "mtf": "mtf",
	// Accounts
	"ledger": "ledger", "ledger/statement": "ledger clients statements money debits", "ledger/trial-balance": "ledger books", "funds": "ledger",
	"receipts": "money", "payouts": "money", "payouts/hdfc-seq": "money settings", "gateway": "money", "upix": "money", "hdfc-pay": "connections",
	"bank": "bank", "ras": "ras", "money-recon": "clientmoney", "money-transfers": "clientmoney", "firm-banks": "clientmoney settings",
	"vouchers": "books", "accounts": "books", "account-heads": "books", "account-moves": "books", "financials": "books", "gst": "books", "vendor-bills": "books", "vendors": "books", "bill-scans": "books", "invoices": "books",
	"brokerage": "brokerage", "brokerage/client/{ucc}": "brokerage clients", "prepaid": "prepaid",
	// Compliance
	"reports": "reports", "holding-api": "reports", "disclosures": "exdisc", "ai": "aictl", "ai/usage": "aictl settings",
	"compliance": "kyc", "fatca": "kyc", "nominations": "kyc", "sanctions": "kyc",
	"tickets": "helpdesk", "complaints": "helpdesk", "impersonation": "helpdesk", "raia": "helpdesk",
	"nri": "nri", "alerts": "surv", "surveillance": "surv", "surveillance-return": "surv", "fingate": "surv",
	"inactive": "inactive", "deceased": "deceased", "self-inspection": "selfinspect",
	"inspection-packs": "inspack", "inspection-docs": "inspack", "inspection-templates": "inspack",
	"security": "incidents", "security/incidents": "incidents", "security/events": "incidents",
	"security/log-export": "logexport", "security/logs": "logexport", "security/settings": "logexport incidents", "security/keys": "settings",
	// Investments
	"ipo": "ipo", "rfq": "rfq", "cbrics": "cbrics", "stock-sips": "stocksip",
	"mf": "ipo mforders mfsips mfmandates mfunits mfcomm mfsettings", "mf/orders": "mforders", "mf/clients": "mforders", "mf/schemes": "mforders",
	"mf/sips": "mfsips", "mf/mandates": "mfmandates", "mf/units": "mfunits", "mf/commission": "mfcomm",
	"mf/settings": "mfsettings", "mf/status": "mfsettings mforders mfsips", "mf/nav": "mfsettings", "mf/import": "mfsettings",
	// Depository
	"dp": "dp", "cdsl/requests": "cdslreq dp", "cdsl/files": "cdslreq dp", "cdsl/bo-defaults": "cdslreq dp", "cdsl/bo-details": "cdslreq dp",
	"cdsl/delete-holder": "cdslreq dp", "cdsl/demat-check": "cdslreq dp", "cdsl/uploads": "cdslreq dp", "cdsl/freezes": "cdslreq dp",
	"cdsl/edis": "edis", "cdsl/txn": "cdsltxn", "cdsl/destat": "cdsltxn", "cdsl/reports": "cdsltxn",
	// Business
	"insights": "insights", "tax-plan": "insights", "crm": "crm", "leads": "crm",
	"branches": "partners", "partners": "partners", "ap-payouts": "partners", "referrals": "partners", "ap-mapping": "partners",
	"dealers": "dealers", "otr": "dealers",
	// Admin
	"connections": "connections", "backups": "backups", "data-exit": "dataexit", "data-exports": "dataexit", "period-archives": "dataexit",
	"settings": "settings", "firm-profile": "settings", "signing": "settings",
	"users": "users settings", "roles": "roles", "role-changes": "roles", "support-access": "supportaccess", "ui/policy": "layout",
	"firm-practices": "practices", "firm-securities": "firmsec", "go-live": "practices", "finspot-logins": "clients live",
	"mobile/settings": "staffapp clientapp", "mobile/staff-qr": "staffapp", "mobile/test-push": "staffapp",
	// The staff app (/m/): the desktop screen each view mirrors. Its home
	// and approvals list only the items of screens the roles include, and
	// an approval is decided as from the item's screen (ops/mobilestaff.go).
	// HRMS and Payroll (api/hrms.go). "open" routes are every staff
	// login's own records and their team's (the server limits the data);
	// their screens still decide what a role the firm made may do there.
	"modules": "open modules", "PUT modules": "modules",
	"hr/me": "open me", "hr/staff": "open people", "POST hr/staff": "people", "PUT hr/staff/{code}": "people",
	"hr/staff/{code}/reveal": "people", "hr/staff/{code}/user": "people", "hr/staff/{code}/home-dealing": "people hrcomp",
	"hr/staff/{code}/documents": "people", "hr/staff/{code}/certs": "hrcomp people", "hr/staff/{code}/leaving": "hrlife people",
	"hr/staff/{code}/device": "attendance", "hr/staff/{code}/pin": "attendance", "hr/staff/{code}/leave-adjust": "leave",
	"hr/staff/{code}/attendance": "open attendance people", "hr/staff/{code}/balances": "open leave people",
	"hr/org": "open people", "hr/policy": "open hrpolicy", "PUT hr/policy": "hrpolicy",
	"hr/checklists": "hrlife", "hr/leavers": "hrlife", "hr/certificates": "hrcomp", "hr/kmp": "hrcomp people",
	"hr/own-trading": "open hrcomp me", "hr/own-trading/pre-clearance/{id}": "hrcomp",
	"hr/attendance": "open attendance me", "hr/attendance/import": "attendance", "hr/photos": "open attendance",
	"hr/wfh": "open attendance me", "hr/devices": "attendance", "hr/kiosks": "attendance",
	"hr/leave": "open leave me", "hr/leave-year": "leave", "hr/team": "open people leave attendance", "hr/on-leave": "open leave",
	"payroll": "payroll", "payroll/runs/{month}/payslips": "open payroll me", "payroll/form130": "open payroll me",
	"payroll/claims": "open payroll me", "payroll/tax-declarations": "open payroll me", "payroll/tax-declarations/verify": "payroll",
	"m/work": "autopilot", "m/day": "dashboard", "m/alerts": "dashboard autopilot surv", "m/clients": "clients",
}

// commonRoutes are open to every staff login whatever their roles: who
// they are, their own sign-in and layout, help, notices, the activity
// feed, and reference lists every screen uses.
var commonRoutes = map[string]bool{"me": true, "auth": true, "ledgy": true, "notices": true, "edition": true, "support": true,
	"file-types": true, "tags": true, "market": true, "dev": true,
	// The activity feed sits beside every screen; it shows only lines
	// about the screens the person's roles include (ops/activity.go).
	"activity": true,
	// The person's own phones and the pushes sent to them; the firm's
	// phones as a whole need an Admin (ops/mobile.go).
	"mobile/devices": true, "mobile/notifications": true,
	// The staff app's home and approvals: filtered by the person's roles.
	"m/home": true, "m/approvals": true}

// screensFor returns the screens a route serves (common: open to all
// staff, with the screens, when it has them, still given to the
// permission checks; nil and false: not mapped, which a gated login
// cannot use).
func screensFor(pattern string) (areas []string, common bool) {
	method, path, _ := strings.Cut(pattern, " ")
	path = strings.TrimPrefix(path, "/v1/")
	for p := path; p != ""; {
		if commonRoutes[p] {
			return nil, true
		}
		v, ok := routeScreens[method+" "+p]
		if !ok {
			v, ok = routeScreens[p]
		}
		if ok {
			if rest, open := strings.CutPrefix(v, "open "); open {
				return strings.Fields(rest), true
			}
			return strings.Fields(v), false
		}
		i := strings.LastIndexAny(p, "/")
		if i < 0 {
			break
		}
		p = p[:i]
	}
	return nil, false
}

// gate applies a gated login's roles to a request: the route must serve
// a screen the roles may open, downloads need "may download", and brokerage
// totals are left out without "may see brokerage totals". It returns the
// writer to use, or nil when it answered the request itself.
func (s *Server) gate(w http.ResponseWriter, r *http.Request, pattern string, mutating bool, acc ops.Access) http.ResponseWriter {
	areas, common := screensFor(pattern)
	if common {
		return w
	}
	if strings.HasPrefix(pattern, "GET /v1/hr/") || strings.HasPrefix(pattern, "POST /v1/hr/") || strings.HasPrefix(pattern, "PUT /v1/hr/") || strings.Contains(pattern, " /v1/payroll") {
		// With the module off its routes answer 404 for everyone, as
		// before roles (ops.ErrModuleOff).
		m := s.ops.Modules(r.Context(), tenant(r))
		if !m.On(ops.ModHRMS) || (strings.Contains(pattern, " /v1/payroll") && !m.On(ops.ModPayroll)) {
			return w
		}
	}
	if areas == nil {
		writeErr(w, 403, "forbidden", "This part of Ledgerline is not yet linked to a screen, so roles the firm made cannot use it. Ask an Admin.", nil)
		return nil
	}
	if !acc.Allows(areas, mutating) {
		what := "look at"
		if mutating {
			what = "change anything on"
		}
		writeErr(w, 403, "forbidden", "Your role does not let you "+what+" "+screens.Name(areas[0])+".", map[string]any{"screens": areas})
		return nil
	}
	lim := acc.Limits()
	if !lim.Brokerage && pattern == "GET /v1/financials" {
		writeErr(w, 403, "forbidden", "Your role does not show brokerage totals, and the firm's profit and loss carries them.", nil)
		return nil
	}
	if !lim.Export {
		w = &noExport{ResponseWriter: w}
	}
	if !lim.Brokerage && r.Method == http.MethodGet {
		w = &noBrokerage{ResponseWriter: w}
	}
	return w
}

// noExport refuses a response that is a download (sent as an attachment).
type noExport struct {
	http.ResponseWriter
	wrote, blocked bool
}

func (w *noExport) WriteHeader(c int) {
	if w.wrote {
		return
	}
	w.wrote = true
	if strings.HasPrefix(strings.ToLower(w.Header().Get("Content-Disposition")), "attachment") {
		w.blocked = true
		w.Header().Del("Content-Disposition")
		w.Header().Del("Content-Length")
		writeErr(w.ResponseWriter, 403, "forbidden", "Your role may not download files or exports.", nil)
		return
	}
	w.ResponseWriter.WriteHeader(c)
}

func (w *noExport) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.blocked {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

func (w *noExport) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// noBrokerage holds a JSON answer back and drops every "brokerage" field
// from it (a client's or partner's brokerage, a reconciliation's brokerage
// line), for roles that do not see brokerage totals.
type noBrokerage struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (w *noBrokerage) WriteHeader(c int) { w.status = c }
func (w *noBrokerage) Write(b []byte) (int, error) {
	if !strings.Contains(w.Header().Get("Content-Type"), "json") {
		w.flush()
		return w.ResponseWriter.Write(b)
	}
	return w.buf.Write(b)
}
func (w *noBrokerage) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *noBrokerage) flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status != -1 {
		w.ResponseWriter.WriteHeader(w.status)
		w.status = -1
	}
}

// done sends what was held back, without brokerage fields.
func (w *noBrokerage) done() {
	if w.status == -1 {
		return
	}
	var v any
	if json.Unmarshal(w.buf.Bytes(), &v) == nil {
		v = dropKeys(v, brokerageKeys)
		w.Header().Del("Content-Length")
		w.flush()
		enc := json.NewEncoder(w.ResponseWriter)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
		return
	}
	w.flush()
	w.ResponseWriter.Write(w.buf.Bytes())
}

// brokerageKeys are the fields that carry brokerage earned (a partner's,
// a dealer's, a client's, the firm's), not rates.
var brokerageKeys = map[string]bool{"brokerage": true, "team_brokerage": true, "unattributed_brokerage": true}

func dropKeys(v any, keys map[string]bool) any {
	switch x := v.(type) {
	case map[string]any:
		for k := range keys {
			delete(x, k)
		}
		for k, e := range x {
			x[k] = dropKeys(e, keys)
		}
	case []any:
		for i, e := range x {
			x[i] = dropKeys(e, keys)
		}
	}
	return v
}

// ---- the Roles screen ----

func (s *Server) roleRoutes() {
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/roles", s.roles, false)
	R("POST /v1/roles/check", s.checkRole, true)
	R("POST /v1/roles", s.proposeRole, true)
	R("POST /v1/roles/reset", s.resetRole, true)
	R("POST /v1/roles/retire", s.retireRole, true)
	R("POST /v1/users/{id}/roles", s.proposeUserRoles, true)
	R("POST /v1/role-changes/{id}/decide", s.decideRoleChange, true)
	R("POST /v1/role-changes/{id}/withdraw", s.withdrawRoleChange, true)
	R("POST /v1/roles/preview", s.startPreview, true)
	R("POST /v1/roles/preview/end", s.endPreview, true)
}

func (s *Server) roles(w http.ResponseWriter, r *http.Request) {
	rs, err := s.ops.RoleDefs(r.Context(), tenant(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	cs, err := s.ops.RoleChanges(r.Context(), tenant(r))
	if err != nil {
		fail(w, err, nil)
		return
	}
	us, _ := s.ops.Users(r.Context(), tenant(r))
	var admins []string
	for _, u := range us {
		if a, err := s.ops.Access(r.Context(), tenant(r), u.ID); err == nil && a.Admin && u.Role != ops.RoleClient {
			admins = append(admins, u.ID)
		}
	}
	type person struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Roles   []string `json:"roles"`
		Branch  string   `json:"branch,omitempty"`
		Partner string   `json:"partner,omitempty"`
		Active  bool     `json:"active"`
	}
	var people []person
	for _, u := range us {
		if u.Role == ops.RoleClient {
			continue
		}
		people = append(people, person{u.ID, u.Name, append([]string{u.Role}, u.MoreRoles...), u.Branch, u.Partner, u.Active})
	}
	ok(w, map[string]any{"roles": rs, "changes": cs, "screens": screens.Nav, "admins": admins, "people": people}, nil)
}

type roleBody struct {
	Role ops.RoleDef `json:"role"`
}

func (s *Server) checkRole(w http.ResponseWriter, r *http.Request) {
	var b roleBody
	if !decode(w, r, &b) {
		return
	}
	d, sum, warn, err := s.ops.CheckRole(r.Context(), tenant(r), actor(r), b.Role)
	ok(w, map[string]any{"role": d, "summary": sum, "warnings": warn}, err)
}

func (s *Server) proposeRole(w http.ResponseWriter, r *http.Request) {
	var b roleBody
	if !decode(w, r, &b) {
		return
	}
	c, err := s.ops.ProposeRole(r.Context(), tenant(r), actor(r), b.Role)
	ok(w, c, err)
}

type roleName struct {
	Name string `json:"name"`
}

func (s *Server) resetRole(w http.ResponseWriter, r *http.Request) {
	var b roleName
	if !decode(w, r, &b) {
		return
	}
	c, err := s.ops.ProposeRoleReset(r.Context(), tenant(r), actor(r), b.Name)
	ok(w, c, err)
}

func (s *Server) retireRole(w http.ResponseWriter, r *http.Request) {
	var b roleName
	if !decode(w, r, &b) {
		return
	}
	c, err := s.ops.ProposeRoleRetire(r.Context(), tenant(r), actor(r), b.Name)
	ok(w, c, err)
}

func (s *Server) proposeUserRoles(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Roles   []string `json:"roles"`
		Branch  string   `json:"branch"`
		Partner string   `json:"partner"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := s.ops.ProposeUserRoles(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Roles, b.Branch, b.Partner)
	ok(w, c, err)
}

func (s *Server) decideRoleChange(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	c, err := s.ops.DecideRoleChange(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Approve, b.Note)
	ok(w, c, err)
}

func (s *Server) withdrawRoleChange(w http.ResponseWriter, r *http.Request) {
	c, err := s.ops.WithdrawRoleChange(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, c, err)
}

// startPreview opens "Preview as this role": from then on the console
// sends X-Preview on its requests and sees only what the role would.
func (s *Server) startPreview(w http.ResponseWriter, r *http.Request) {
	var b roleBody
	if !decode(w, r, &b) {
		return
	}
	d, err := s.ops.StartRolePreview(r.Context(), tenant(r), actor(r), b.Role)
	ok(w, d, err)
}

func (s *Server) endPreview(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]bool{"ended": true}, s.ops.EndRolePreview(r.Context(), tenant(r), actor(r)))
}

// accessView is what /v1/me tells the console about a gated login (or a
// preview), so the sidebar, tabs and buttons show only what it may use.
func accessView(a ops.Access) map[string]any {
	return map[string]any{"custom": a.Gated() || a.Preview, "admin": a.Admin, "legacy": a.Legacy, "bases": a.Bases(),
		"screens": a.Screens(), "beyond": a.Beyond(), "limits": a.Limits(), "preview": a.Preview}
}
