package ops

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- Ledgerline support access that the firm sees, approves and ends ----
//
// SEBI's framework for cloud services treats access to a broker's data by
// the provider's staff without the broker's authorisation as a breach, wants
// administrative access tracked through a request, and least privilege; the
// cybersecurity framework (CSCRF) wants the broker to oversee its third
// parties. So a person at Ledgerline never signs in to a firm with a firm
// login or a shared one. Instead:
//
//  1. A support operator (an identity of Ledgerline's own, kept apart from
//     every firm in the SupportNS namespace, with a password and a required
//     two-factor code) asks one firm for access: reason, ticket, areas,
//     read-only or write, and hours (2 by default, 8 at most).
//  2. The firm's Admin, or the role the firm chooses, approves (and may
//     narrow) or refuses it on Admin → Support access. An e-mail and a work
//     item say a request is waiting. A firm may switch on a standing rule
//     that lets read-only requests marked as an outage in at once.
//  3. While approved, the operator opens a session marked as support. It
//     acts as "support:<operator>@<firm>", only in the approved areas, only
//     reads unless write was approved, with PAN, Aadhaar and bank account
//     numbers masked in every answer, and it ends by itself when the hours
//     run out. The firm can end it at any moment; the next request fails.
//  4. Every request, decision, session start and end and every request the
//     support person makes is in the firm's audit trail, which the Support
//     access log reads (and downloads as CSV).
//  5. "Block all Ledgerline support access" refuses new requests and ends
//     open access at once.
//
// The API layer (internal/api/supportaccess.go) enforces the areas, the
// read-only rule and the masking on every request of a support session,
// using SupportGate and SupportMask* below.

const (
	// SupportNS is the namespace of Ledgerline's support operators. It is
	// not a firm: no API key may name it (api.ParseKeys refuses names that
	// start with "_"), so no firm session can be opened in it.
	SupportNS = "_support"
	// RoleSupportOperator is the role of a Ledgerline support operator.
	RoleSupportOperator = "Ledgerline support"
	// SupportActorPrefix starts the actor of every support session.
	SupportActorPrefix = "support:"
	// supportOutageActor approves requests under the firm's standing rule.
	supportOutageActor = "system:support-outage-rule"

	cSupportAccess = "support_access"
	cSupportPolicy = "support_access_policy"

	supportMaxHours     = 8
	supportDefaultHours = 2
	// A request nobody decided on lapses after this long.
	supportPendingFor = 24 * time.Hour
)

// Support access statuses.
const (
	SAPending   = "Pending"
	SAApproved  = "Approved"
	SARefused   = "Refused"
	SAWithdrawn = "Withdrawn"
	SAEnded     = "Ended"
	SAExpired   = "Expired"
)

// SupportArea is a part of Ledgerline a support person may be let into.
type SupportArea struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	About string `json:"about"`
}

// SupportAreas are the areas a request can name. "full" is everything and
// is given only when asked for.
var SupportAreas = []SupportArea{
	{"clients", "Clients and onboarding", "Clients, applications, change requests, KYC, exchange UCC, partners, contract notes and client reports"},
	{"ledger", "Ledger, money and billing", "Ledger, receipts and payouts, bank, running account, billing, brokerage, interest and firm accounts"},
	{"exchange_files", "Exchange files and trades", "Exchange and clearing files, uploads, trades, reconciliation, settlement and day close"},
	{"settings", "Settings and connections", "Settings, connections (secrets stay hidden), scheduled jobs, holidays, backups status and users list"},
	{"full", "Everything", "Every screen. Only when the request asks for it."},
}

// supportAreaPaths maps each area to the API paths in it (a path and what
// is under it). A path in no list is reachable only with "full".
var supportAreaPaths = map[string][]string{
	"clients": {"/v1/clients", "/v1/applications", "/v1/changes", "/v1/ucc-api", "/v1/mnrl", "/v1/bse-ucc-blocked", "/v1/nri", "/v1/fatca",
		"/v1/inactive", "/v1/deceased", "/v1/nominations", "/v1/client-reports", "/v1/tags", "/v1/leads", "/v1/crm", "/v1/referrals",
		"/v1/partners", "/v1/branches", "/v1/dealers", "/v1/ap-mapping", "/v1/notes", "/v1/year-end", "/v1/dispatch", "/v1/dispatch.csv",
		"/v1/whatsapp", "/v1/messages", "/v1/templates", "/v1/archive", "/v1/kyc"},
	"ledger": {"/v1/ledger", "/v1/journal", "/v1/vouchers", "/v1/accounts", "/v1/receipts", "/v1/payouts", "/v1/payout-holds", "/v1/bank",
		"/v1/ras", "/v1/money-recon", "/v1/money-transfers", "/v1/debits", "/v1/interest", "/v1/legacy-debts", "/v1/billing", "/v1/brokerage",
		"/v1/charges", "/v1/prepaid", "/v1/invoices", "/v1/gst", "/v1/financials", "/v1/firm-banks", "/v1/vendor-bills", "/v1/vendors",
		"/v1/bill-scans", "/v1/funds", "/v1/funds-awaiting", "/v1/deposits", "/v1/ap-payouts", "/v1/hdfc-pay", "/v1/gateway", "/v1/upix", "/v1/bow-funds"},
	"exchange_files": {"/v1/files", "/v1/exchange-files", "/v1/exchange-files-alerts", "/v1/exchange-files-checklist", "/v1/file-types",
		"/v1/adapters", "/v1/trades", "/v1/recon", "/v1/settlement", "/v1/clearing-data", "/v1/history-files", "/v1/nse-extranet", "/v1/bse",
		"/v1/mcx", "/v1/mcx-moves", "/v1/ncl-feeds", "/v1/notis", "/v1/external-trades", "/v1/eod", "/v1/shortages", "/v1/allocation",
		"/v1/collateral-file", "/v1/sample"},
	"settings": {"/v1/settings", "/v1/connections", "/v1/jobs", "/v1/holidays", "/v1/edition", "/v1/users", "/v1/backups", "/v1/firm-profile",
		"/v1/ui", "/v1/security", "/v1/compliance/profile"},
}

// supportAlways are open to every support session whatever its areas: who
// am I, the session's own banner and end button, and the help articles.
var supportAlways = map[string]bool{
	"GET /v1/me": true, "GET /v1/me/ui": true, "GET /v1/notices": true,
	"GET /v1/ledgy/suggest": true, "GET /v1/ledgy/articles/{id}": true,
	"GET /v1/support-access/session": true, "POST /v1/support-access/session/end": true,
}

// supportNever are closed to support sessions even with full access and
// write: sign-in and users, revealing identifiers, the support access
// screen itself (support never approves its own access), and development.
var supportNever = []string{"/v1/auth", "/v1/support-access", "/v1/dev"}

// supportNoWrite are read-only to support even when write was approved:
// users and their passwords, connection secrets, the USB token signer,
// browser automations on the firm's computers, migration loads and undos,
// backups, the firm's own tickets to Ledgerline, and AI controls and credit.
var supportNoWrite = []string{"/v1/users", "/v1/connections", "/v1/signing", "/v1/rpa", "/v1/migration", "/v1/backups", "/v1/support",
	"/v1/ai", "/v1/edition"}

func underPath(path, p string) bool { return path == p || strings.HasPrefix(path, p+"/") }

// SupportAreaOf is the area an API path belongs to ("" when it is in none).
func SupportAreaOf(path string) string {
	for _, a := range SupportAreas {
		for _, p := range supportAreaPaths[a.ID] {
			if underPath(path, p) {
				return a.ID
			}
		}
	}
	return ""
}

// SupportAuth is what a support session may do, as sign-in reports it.
type SupportAuth struct {
	Request  string   `json:"request"`
	Operator string   `json:"operator"`
	Name     string   `json:"name"`
	Firm     string   `json:"firm"`
	Ticket   string   `json:"ticket"`
	Areas    []string `json:"areas"`
	Write    bool     `json:"write"`
	Expires  string   `json:"expires"`
}

func (a *SupportAuth) has(area string) bool { return slices.Contains(a.Areas, area) }

// SupportGate says whether a support session may make a request to the
// route pattern ("GET /v1/clients/{ucc}"). nil means yes.
func SupportGate(a *SupportAuth, pattern string) error {
	if a == nil {
		return nil
	}
	if supportAlways[pattern] {
		return nil
	}
	method, path, _ := strings.Cut(pattern, " ")
	for _, p := range supportNever {
		if underPath(path, p) {
			return fmt.Errorf("%w: this is not available in a Ledgerline support session", ErrForbidden)
		}
	}
	if strings.HasSuffix(path, "/reveal") {
		return fmt.Errorf("%w: full PAN and bank account numbers are never shown to Ledgerline support", ErrForbidden)
	}
	write := method != "GET" && method != "HEAD"
	if write && !a.Write {
		return fmt.Errorf("%w: this support session is read-only", ErrForbidden)
	}
	if write {
		for _, p := range supportNoWrite {
			if underPath(path, p) {
				return fmt.Errorf("%w: Ledgerline support may look at this but not change it", ErrForbidden)
			}
		}
	}
	if a.has("full") {
		return nil
	}
	area := SupportAreaOf(path)
	if area == "" || !a.has(area) {
		return fmt.Errorf("%w: this is outside the areas the firm approved for this support session (%s)", ErrForbidden, supportAreaNames(a.Areas))
	}
	return nil
}

func supportAreaNames(ids []string) string {
	var out []string
	for _, id := range ids {
		n := id
		for _, a := range SupportAreas {
			if a.ID == id {
				n = a.Name
			}
		}
		out = append(out, n)
	}
	return strings.Join(out, ", ")
}

// IsSupportActor reports whether an actor is a Ledgerline support session.
func IsSupportActor(actor string) bool { return strings.HasPrefix(actor, SupportActorPrefix) }

func supportActor(operator, tenant string) string {
	return SupportActorPrefix + operator + "@" + tenant
}

// notSupport refuses an action Ledgerline support may never take in a
// firm, whatever was approved (the API layer refuses it too).
func notSupport(actor, what string) error {
	if IsSupportActor(actor) {
		return fmt.Errorf("%w: %s is not open to Ledgerline support", ErrForbidden, what)
	}
	return nil
}

// ---- masking for support ----

var (
	reSupAadhaar = regexp.MustCompile(`\b[2-9][0-9]{3}[ -]?[0-9]{4}[ -]?([0-9]{4})\b`)
	reSupAccount = regexp.MustCompile(`\b[0-9]{9,18}\b`)
)

// SupportMaskText masks PAN, Aadhaar-shaped and bank-account-shaped numbers
// (9 to 18 digits, which also covers demat IDs and mobile numbers) in text
// shown to a support session. It errs towards masking: a long reference
// number is masked too.
func SupportMaskText(s string) string {
	s = reMaskPAN2.ReplaceAllStringFunc(s, func(v string) string { return MaskPAN(strings.ToUpper(v)) })
	s = reSupAadhaar.ReplaceAllString(s, "XXXX XXXX $1")
	return reSupAccount.ReplaceAllStringFunc(s, MaskAccount)
}

// SupportMaskJSON masks every string (and object key) in a JSON document;
// numbers, which are amounts and counts, are left alone. ok is false when
// the body is not JSON.
func SupportMaskJSON(b []byte) (out []byte, ok bool) {
	if len(bytes.TrimSpace(b)) == 0 {
		return b, true
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	out, err := json.Marshal(supportMaskValue(v))
	return append(out, '\n'), err == nil
}

func supportMaskValue(v any) any {
	switch x := v.(type) {
	case string:
		return SupportMaskText(x)
	case []any:
		for i := range x {
			x[i] = supportMaskValue(x[i])
		}
		return x
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[SupportMaskText(k)] = supportMaskValue(e)
		}
		return m
	}
	return v
}

// ---- operators (Ledgerline's own people) ----

// SupportOperator is a Ledgerline support person, for the provider's list.
type SupportOperator struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Active      bool   `json:"active"`
	TOTPEnabled bool   `json:"totp_enabled"`
	LastLogin   string `json:"last_login,omitempty"`
}

// SaveSupportOperator adds a support operator (returning a one-time
// password) or changes one; reset gives a new one-time password, and
// deactivating ends their sessions and every firm access they hold. Only
// the provider's own endpoint calls this (the provider key).
func (o *Ops) SaveSupportOperator(ctx context.Context, by, id, name string, active, reset bool) (SupportOperator, string, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if !validID.MatchString(id) {
		return SupportOperator{}, "", invalid("operator ID must be lower-case letters, digits, dots or dashes")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = id
	}
	// The namespace's own settings: two-factor sign-in is required.
	if _, _, err := docs.Get[Settings](ctx, o.Docs, SupportNS, cSettings, "main"); err != nil {
		s := DefaultSettings()
		s.Firm, s.TwoFactor, s.SessionIdleMinutes = "Ledgerline support", "required", 30
		if err := docs.Upsert(ctx, o.Docs, SupportNS, cSettings, "main", s, by); err != nil {
			return SupportOperator{}, "", err
		}
	}
	_, gerr := o.user(ctx, SupportNS, id)
	isNew := gerr != nil
	u := User{ID: id, Name: name, Role: RoleSupportOperator, Active: active || isNew}
	if err := docs.Upsert(ctx, o.Docs, SupportNS, cUser, id, u, by); err != nil {
		return SupportOperator{}, "", err
	}
	pw := ""
	if isNew || reset {
		pw = TempPassword()
		if err := o.SetPassword(ctx, SupportNS, by, id, pw, true); err != nil {
			return SupportOperator{}, "", err
		}
	}
	if reset || !u.Active {
		_ = o.endSessions(ctx, SupportNS, by, id)
	}
	o.log(ctx, SupportNS, by, "support_operator.saved", id, map[string]any{"active": u.Active, "new": isNew, "reset": reset})
	c, _, _ := o.cred(ctx, SupportNS, id)
	return SupportOperator{ID: id, Name: name, Active: u.Active, TOTPEnabled: c.TOTPEnabled, LastLogin: c.LastLogin}, pw, nil
}

// SupportOperators lists Ledgerline's support operators.
func (o *Ops) SupportOperators(ctx context.Context) ([]SupportOperator, error) {
	us, err := o.Users(ctx, SupportNS)
	out := []SupportOperator{}
	for _, u := range us {
		c, _, _ := o.cred(ctx, SupportNS, u.ID)
		out = append(out, SupportOperator{ID: u.ID, Name: u.Name, Active: u.Active, TOTPEnabled: c.TOTPEnabled, LastLogin: c.LastLogin})
	}
	return out, err
}

func (o *Ops) supportOperator(ctx context.Context, id string) (User, error) {
	u, err := o.user(ctx, SupportNS, id)
	if err != nil || !u.Active || u.Role != RoleSupportOperator {
		return User{}, fmt.Errorf("%w: not an active Ledgerline support operator", ErrForbidden)
	}
	return u, nil
}

// ---- the firm's policy ----

// SupportPolicy is the firm's choice about Ledgerline support access.
type SupportPolicy struct {
	// Blocked refuses every request and ends open access.
	Blocked bool `json:"blocked"`
	// ApproverRole may approve as well as the Admin ("" = Admin only).
	ApproverRole string `json:"approver_role"`
	// OutageReadOnly is the standing rule: a read-only request (not full)
	// that support marks as an outage is let in at once, for at most
	// OutageHours (default 2). Off by default.
	OutageReadOnly bool   `json:"outage_read_only"`
	OutageHours    int    `json:"outage_hours"`
	Changed        string `json:"changed,omitempty"`
	ChangedBy      string `json:"changed_by,omitempty"`
}

// SupportApproverRoles are the roles a firm may let approve.
var SupportApproverRoles = []string{RoleCompliance, RoleChecker, RoleRisk}

func (o *Ops) supportPolicy(ctx context.Context, tenant string) SupportPolicy {
	p, _, _ := docs.GetOr(ctx, o.Docs, tenant, cSupportPolicy, "main", SupportPolicy{})
	if p.OutageHours <= 0 || p.OutageHours > supportMaxHours {
		p.OutageHours = supportDefaultHours
	}
	return p
}

// SetSupportPolicy changes the firm's policy (Admin). Blocking ends every
// open access and refuses every waiting request at once.
func (o *Ops) SetSupportPolicy(ctx context.Context, tenant, actor string, p SupportPolicy) (SupportPolicy, error) {
	if err := notSupport(actor, "Changing support access"); err != nil {
		return SupportPolicy{}, err
	}
	if err := o.require(ctx, tenant, actor, "Changing Ledgerline support access settings"); err != nil {
		return SupportPolicy{}, err
	}
	if p.ApproverRole != "" && p.ApproverRole != RoleAdmin && !slices.Contains(SupportApproverRoles, p.ApproverRole) {
		return SupportPolicy{}, invalid("support access can be approved by the Admin, or also by the %s", strings.Join(SupportApproverRoles, ", "))
	}
	if p.ApproverRole == RoleAdmin {
		p.ApproverRole = ""
	}
	if p.OutageHours < 0 || p.OutageHours > supportMaxHours {
		return SupportPolicy{}, invalid("the outage rule lets support in for 1 to %d hours", supportMaxHours)
	}
	if p.OutageHours == 0 {
		p.OutageHours = supportDefaultHours
	}
	old := o.supportPolicy(ctx, tenant)
	p.Changed, p.ChangedBy = o.now().Format(time.RFC3339), actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cSupportPolicy, "main", p, actor); err != nil {
		return SupportPolicy{}, err
	}
	o.log(ctx, tenant, actor, "support_access.policy", "main", map[string]any{"blocked": p.Blocked, "approver_role": firstOf(p.ApproverRole, RoleAdmin), "outage_read_only": p.OutageReadOnly, "outage_hours": p.OutageHours})
	if p.Blocked && !old.Blocked {
		o.log(ctx, tenant, actor, "support_access.blocked", "main", nil)
		o.securityEvent(ctx, tenant, "support_access.blocked", "info", actor, "main", nil)
		rs, _ := docs.List[SupportAccess](ctx, o.Docs, tenant, cSupportAccess, "")
		for _, r := range rs {
			switch r.Status {
			case SAApproved:
				_, _ = o.endSupportAccess(ctx, tenant, actor, r.ID, "All Ledgerline support access was blocked")
			case SAPending:
				_, _ = o.decide(ctx, tenant, actor, r.ID, false, "All Ledgerline support access was blocked", nil)
			}
		}
	} else if !p.Blocked && old.Blocked {
		o.log(ctx, tenant, actor, "support_access.unblocked", "main", nil)
		o.securityEvent(ctx, tenant, "support_access.unblocked", "info", actor, "main", nil)
	}
	return p, nil
}

// canApproveSupport says whether actor may approve, refuse or end access.
func (o *Ops) canApproveSupport(ctx context.Context, tenant, actor string) error {
	if err := notSupport(actor, "Approving support access"); err != nil {
		return err
	}
	p := o.supportPolicy(ctx, tenant)
	roles := []string{}
	if p.ApproverRole != "" {
		roles = append(roles, p.ApproverRole)
	}
	return o.require(ctx, tenant, actor, "Approving Ledgerline support access", roles...)
}

// ---- requests ----

// SupportAccess is one request for access and, once approved, the access.
type SupportAccess struct {
	ID           string   `json:"id"`
	Operator     string   `json:"operator"`
	OperatorName string   `json:"operator_name"`
	Actor        string   `json:"actor"`
	Reason       string   `json:"reason"`
	Ticket       string   `json:"ticket"`
	Areas        []string `json:"areas"`
	Write        bool     `json:"write"`
	Hours        int      `json:"hours"`
	Outage       bool     `json:"outage,omitempty"`
	// What was asked, when the approver narrowed it.
	AskedAreas []string `json:"asked_areas,omitempty"`
	AskedWrite bool     `json:"asked_write,omitempty"`
	AskedHours int      `json:"asked_hours,omitempty"`

	Status    string `json:"status"`
	Requested string `json:"requested"`
	DecidedBy string `json:"decided_by,omitempty"`
	DecidedAt string `json:"decided_at,omitempty"`
	Note      string `json:"note,omitempty"`
	Expires   string `json:"expires,omitempty"` // approved access ends then
	Started   string `json:"started,omitempty"` // first session
	LastStart string `json:"last_start,omitempty"`
	Sessions  int    `json:"sessions,omitempty"`
	Epoch     int    `json:"epoch,omitempty"` // bumped on each start: only the newest session lives
	EndedAt   string `json:"ended_at,omitempty"`
	EndedBy   string `json:"ended_by,omitempty"`
	EndReason string `json:"end_reason,omitempty"`
}

// SupportAccessInput is what a support operator asks for.
type SupportAccessInput struct {
	Tenant string   `json:"tenant"`
	Reason string   `json:"reason"`
	Ticket string   `json:"ticket"`
	Areas  []string `json:"areas"`
	Write  bool     `json:"write"`
	Hours  int      `json:"hours"`
	Outage bool     `json:"outage"`
}

func cleanAreas(in []string) ([]string, error) {
	var out []string
	for _, a := range in {
		a = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(a, " ", "_")))
		if a == "" {
			continue
		}
		ok := false
		for _, x := range SupportAreas {
			ok = ok || x.ID == a
		}
		if !ok {
			return nil, invalid("unknown area %q; choose from clients, ledger, exchange_files, settings or full", a)
		}
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	if slices.Contains(out, "full") {
		return []string{"full"}, nil
	}
	if len(out) == 0 {
		return nil, invalid("choose at least one area")
	}
	sort.Strings(out)
	return out, nil
}

// supportDue marks a request whose time has passed as Expired (in r; the
// caller saves it) and reports whether it did.
func (o *Ops) supportDue(r *SupportAccess, now time.Time) bool {
	switch r.Status {
	case SAPending:
		if t, err := time.Parse(time.RFC3339, r.Requested); err == nil && now.Sub(t) > supportPendingFor {
			r.Status, r.EndedAt, r.EndReason = SAExpired, now.Format(time.RFC3339), "Not decided within 24 hours"
			return true
		}
	case SAApproved:
		if t, err := time.Parse(time.RFC3339, r.Expires); err == nil && !now.Before(t) {
			r.Status, r.EndedAt, r.EndReason = SAExpired, now.Format(time.RFC3339), "The approved hours ran out"
			return true
		}
	}
	return false
}

// RequestSupportAccess records an operator's request to one firm and tells
// the firm, or lets it in at once under the firm's standing outage rule.
func (o *Ops) RequestSupportAccess(ctx context.Context, operator string, in SupportAccessInput) (SupportAccess, error) {
	tenant := strings.ToLower(strings.TrimSpace(in.Tenant))
	op, err := o.supportOperator(ctx, operator)
	if err != nil {
		return SupportAccess{}, err
	}
	actor := supportActor(op.ID, tenant)
	p := o.supportPolicy(ctx, tenant)
	if p.Blocked {
		o.log(ctx, tenant, actor, "support_access.refused_blocked", "", map[string]any{"operator": op.ID, "ticket": trim(in.Ticket, 60)})
		o.securityEvent(ctx, tenant, "support_access.refused_blocked", "warning", actor, "", map[string]any{"operator": op.ID})
		return SupportAccess{}, fmt.Errorf("%w: this firm has blocked all Ledgerline support access", ErrForbidden)
	}
	in.Reason, in.Ticket = strings.TrimSpace(in.Reason), strings.TrimSpace(in.Ticket)
	if len(in.Reason) < 10 {
		return SupportAccess{}, invalid("say why you need access (at least 10 characters); the firm reads it")
	}
	if in.Ticket == "" {
		return SupportAccess{}, invalid("give the support ticket or reference this access is for")
	}
	areas, err := cleanAreas(in.Areas)
	if err != nil {
		return SupportAccess{}, err
	}
	if in.Hours == 0 {
		in.Hours = supportDefaultHours
	}
	if in.Hours < 1 || in.Hours > supportMaxHours {
		return SupportAccess{}, invalid("access is given for 1 to %d hours", supportMaxHours)
	}
	now := o.now()
	rs, _ := docs.List[SupportAccess](ctx, o.Docs, tenant, cSupportAccess, "")
	for _, r := range rs {
		if r.Operator == op.ID && (r.Status == SAPending || r.Status == SAApproved) && !o.supportDue(&r, now) {
			return SupportAccess{}, fmt.Errorf("%w: you already have request %s (%s) at this firm; end or withdraw it first", ErrState, r.ID, strings.ToLower(r.Status))
		}
	}
	n, err := o.next(ctx, tenant, "support_access", actor)
	if err != nil {
		return SupportAccess{}, err
	}
	r := SupportAccess{ID: fmt.Sprintf("SA-%04d", n), Operator: op.ID, OperatorName: op.Name, Actor: actor, Reason: trim(in.Reason, 1000), Ticket: trim(in.Ticket, 60),
		Areas: areas, Write: in.Write, Hours: in.Hours, Outage: in.Outage, Status: SAPending, Requested: now.Format(time.RFC3339)}
	auto := in.Outage && p.OutageReadOnly && !in.Write && !slices.Contains(areas, "full")
	if auto {
		if r.Hours > p.OutageHours {
			r.AskedHours, r.Hours = r.Hours, p.OutageHours
		}
		r.Status, r.DecidedBy, r.DecidedAt = SAApproved, supportOutageActor, now.Format(time.RFC3339)
		r.Expires, r.Note = now.Add(time.Duration(r.Hours)*time.Hour).Format(time.RFC3339), "Let in under the firm's standing read-only rule for outages"
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSupportAccess, r.ID, r, actor); err != nil {
		return SupportAccess{}, err
	}
	detail := map[string]any{"operator": op.ID, "name": op.Name, "reason": r.Reason, "ticket": r.Ticket, "areas": strings.Join(r.Areas, ","), "write": r.Write, "hours": r.Hours, "outage": r.Outage}
	o.log(ctx, tenant, actor, "support_access.requested", r.ID, detail)
	o.securityEvent(ctx, tenant, "support_access.requested", "info", actor, r.ID, detail)
	set, _ := o.Settings(ctx, tenant)
	approver := firstOf(p.ApproverRole, RoleAdmin)
	what := fmt.Sprintf("%s (Ledgerline support) asks for %s access to %s for %d hours. Reason: %s. Ticket: %s.",
		op.Name, map[bool]string{true: "read and write", false: "read-only"}[r.Write], supportAreaNames(r.Areas), r.Hours, r.Reason, r.Ticket)
	if auto {
		o.log(ctx, tenant, supportOutageActor, "support_access.approved", r.ID, map[string]any{"by": "standing outage rule", "hours": r.Hours, "expires": r.Expires})
		o.securityEvent(ctx, tenant, "support_access.approved", "info", supportOutageActor, r.ID, map[string]any{"rule": "outage"})
		o.raiseWork(ctx, tenant, "support_access", r.ID, "Ledgerline support is in under the outage rule ("+r.ID+")",
			what+" It was let in at once under your standing read-only rule for outages. End it on Admin → Support access if you did not expect it.", "", approver, "supportaccess", now.Format("2006-01-02"))
		o.mailSupportAccess(ctx, tenant, set, fmt.Sprintf("[%s] Ledgerline support is in under your outage rule (%s)", set.Firm, r.ID), what+"\n\nIt was let in at once under your standing read-only rule for outages, until "+fmtIST(r.Expires)+". End it at any time on Admin → Support access in Ledgerline.", r.ID)
		return r, nil
	}
	o.raiseWork(ctx, tenant, "support_access", r.ID, "Approve or refuse Ledgerline support access ("+r.ID+")", what+" Approve or refuse it on Admin → Support access.", "", approver, "supportaccess", now.Format("2006-01-02"))
	o.mailSupportAccess(ctx, tenant, set, fmt.Sprintf("[%s] Ledgerline support asks for access (%s)", set.Firm, r.ID), what+"\n\nNothing is open until you approve. Approve or refuse it on Admin → Support access in Ledgerline ("+approver+"). Unanswered, the request lapses in 24 hours.", r.ID)
	return r, nil
}

func fmtIST(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.In(ist).Format("02 Jan 2006 15:04") + " IST"
	}
	return ts
}

// mailSupportAccess tells the firm's management and compliance addresses.
func (o *Ops) mailSupportAccess(ctx context.Context, tenant string, set Settings, subject, text, ref string) {
	to := append([]string{}, set.ManagementEmails...)
	if set.ComplianceEmail != "" {
		to = append(to, set.ComplianceEmail)
	}
	seen := map[string]bool{}
	for _, e := range to {
		if e = strings.TrimSpace(e); e == "" || seen[strings.ToLower(e)] {
			continue
		}
		seen[strings.ToLower(e)] = true
		o.mailStaff(ctx, tenant, e, subject, text, "support-access:"+ref, nil)
	}
}

// SupportNarrow is what an approver may cut down when approving.
type SupportNarrow struct {
	Areas []string `json:"areas"`
	Write *bool    `json:"write"`
	Hours int      `json:"hours"`
}

// DecideSupportAccess approves (optionally narrowing) or refuses a request.
// The Admin may, and the role the firm chose; never support itself.
func (o *Ops) DecideSupportAccess(ctx context.Context, tenant, actor, id string, approve bool, note string, nw *SupportNarrow) (SupportAccess, error) {
	if err := o.canApproveSupport(ctx, tenant, actor); err != nil {
		return SupportAccess{}, err
	}
	if approve && o.supportPolicy(ctx, tenant).Blocked {
		return SupportAccess{}, fmt.Errorf("%w: all Ledgerline support access is blocked; turn the block off first", ErrState)
	}
	return o.decide(ctx, tenant, actor, id, approve, note, nw)
}

func (o *Ops) decide(ctx context.Context, tenant, actor, id string, approve bool, note string, nw *SupportNarrow) (SupportAccess, error) {
	now := o.now()
	lapsed := false
	r, err := upd(ctx, o.Docs, tenant, cSupportAccess, id, actor, nil, func(r *SupportAccess) error {
		if o.supportDue(r, now) {
			lapsed = true
			return nil
		}
		if r.Status != SAPending {
			return fmt.Errorf("%w: request %s is %s", ErrState, r.ID, strings.ToLower(r.Status))
		}
		r.DecidedBy, r.DecidedAt, r.Note = actor, now.Format(time.RFC3339), trim(strings.TrimSpace(note), 500)
		if !approve {
			r.Status = SARefused
			return nil
		}
		if nw != nil {
			if len(nw.Areas) > 0 {
				as, err := cleanAreas(nw.Areas)
				if err != nil {
					return err
				}
				for _, a := range as {
					if !slices.Contains(r.Areas, "full") && !slices.Contains(r.Areas, a) {
						return invalid("you can narrow the areas asked for, not add to them")
					}
				}
				if !slices.Equal(as, r.Areas) {
					r.AskedAreas, r.Areas = r.Areas, as
				}
			}
			if nw.Write != nil && !*nw.Write && r.Write {
				r.AskedWrite, r.Write = true, false
			} else if nw.Write != nil && *nw.Write && !r.Write {
				return invalid("read-only was asked for; write cannot be added")
			}
			if nw.Hours > 0 && nw.Hours < r.Hours {
				r.AskedHours, r.Hours = r.Hours, nw.Hours
			} else if nw.Hours > r.Hours {
				return invalid("you can shorten the hours asked for, not lengthen them")
			}
		}
		r.Status, r.Expires = SAApproved, now.Add(time.Duration(r.Hours)*time.Hour).Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return r, err
	}
	if lapsed { // saved as Expired by upd
		o.supportExpired(ctx, tenant, r)
		return r, fmt.Errorf("%w: request %s lapsed: %s", ErrState, r.ID, strings.ToLower(r.EndReason))
	}
	action := "support_access.refused"
	detail := map[string]any{"note": r.Note, "operator": r.Operator}
	if approve {
		action = "support_access.approved"
		detail["areas"], detail["write"], detail["hours"], detail["expires"] = strings.Join(r.Areas, ","), r.Write, r.Hours, r.Expires
		if r.AskedAreas != nil || r.AskedWrite || r.AskedHours > 0 {
			detail["narrowed"] = true
		}
		o.closeWork(ctx, tenant, actor, "support_access", r.ID, "approved until "+fmtIST(r.Expires))
	} else {
		o.closeWork(ctx, tenant, actor, "support_access", r.ID, "refused")
	}
	o.log(ctx, tenant, actor, action, r.ID, detail)
	o.securityEvent(ctx, tenant, action, "info", actor, r.ID, detail)
	return r, nil
}

// EndSupportAccess ends approved access at once (the firm's End now): the
// support session's next request fails.
func (o *Ops) EndSupportAccess(ctx context.Context, tenant, actor, id string) (SupportAccess, error) {
	if err := o.canApproveSupport(ctx, tenant, actor); err != nil {
		return SupportAccess{}, err
	}
	return o.endSupportAccess(ctx, tenant, actor, id, "Ended by the firm")
}

func (o *Ops) endSupportAccess(ctx context.Context, tenant, actor, id, why string) (SupportAccess, error) {
	now := o.now()
	r, err := upd(ctx, o.Docs, tenant, cSupportAccess, id, actor, nil, func(r *SupportAccess) error {
		if r.Status == SAPending && IsSupportActor(actor) {
			r.Status, r.EndedAt, r.EndedBy, r.EndReason = SAWithdrawn, now.Format(time.RFC3339), actor, why
			return nil
		}
		if r.Status != SAApproved {
			return fmt.Errorf("%w: request %s is %s", ErrState, r.ID, strings.ToLower(r.Status))
		}
		r.Status, r.EndedAt, r.EndedBy, r.EndReason = SAEnded, now.Format(time.RFC3339), actor, why
		return nil
	})
	if err != nil {
		return r, err
	}
	o.closeWork(ctx, tenant, actor, "support_access", r.ID, strings.ToLower(why))
	action := "support_access.ended"
	if r.Status == SAWithdrawn {
		action = "support_access.withdrawn"
	}
	o.log(ctx, tenant, actor, action, r.ID, map[string]any{"reason": why, "operator": r.Operator})
	o.securityEvent(ctx, tenant, action, "info", actor, r.ID, map[string]any{"reason": why})
	return r, nil
}

// EndOwnSupportAccess is the support operator ending (or withdrawing) their own access.
func (o *Ops) EndOwnSupportAccess(ctx context.Context, tenant, operator, id string) (SupportAccess, error) {
	r, _, err := docs.Get[SupportAccess](ctx, o.Docs, tenant, cSupportAccess, id)
	if err != nil || r.Operator != operator {
		return SupportAccess{}, ErrNotFound
	}
	return o.endSupportAccess(ctx, tenant, r.Actor, id, "Ended by Ledgerline support")
}

// StartSupportSession opens a firm session for approved access and returns
// its token (the firm session cookie). Starting again ends the session
// opened before.
func (o *Ops) StartSupportSession(ctx context.Context, tenant, operator, id, ip, agent string) (string, error) {
	op, err := o.supportOperator(ctx, operator)
	if err != nil {
		return "", err
	}
	if o.supportPolicy(ctx, tenant).Blocked {
		return "", fmt.Errorf("%w: this firm has blocked all Ledgerline support access", ErrForbidden)
	}
	now := o.now()
	expired := false
	r, err := upd(ctx, o.Docs, tenant, cSupportAccess, id, supportActor(op.ID, tenant), nil, func(r *SupportAccess) error {
		if r.Operator != op.ID {
			return ErrNotFound
		}
		if o.supportDue(r, now) {
			expired = true
			return nil
		}
		if r.Status != SAApproved {
			return fmt.Errorf("%w: request %s is %s; nothing is open until the firm approves it", ErrForbidden, r.ID, strings.ToLower(r.Status))
		}
		if r.Started == "" {
			r.Started = now.Format(time.RFC3339)
		}
		r.LastStart, r.Sessions, r.Epoch = now.Format(time.RFC3339), r.Sessions+1, r.Epoch+1
		return nil
	})
	if err != nil {
		return "", err
	}
	if expired { // saved as Expired by upd
		o.supportExpired(ctx, tenant, r)
		return "", fmt.Errorf("%w: access %s has ended: %s", ErrForbidden, r.ID, strings.ToLower(r.EndReason))
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	exp, _ := time.Parse(time.RFC3339, r.Expires)
	if m := now.Add(sessionMaxAge); m.Before(exp) {
		exp = m
	}
	s := Session{User: r.Actor, Created: now.Format(time.RFC3339), LastSeen: now.Format(time.RFC3339), Expires: exp.Format(time.RFC3339), Epoch: r.Epoch, IP: ip, Agent: trim(agent, 200), Support: r.ID}
	if _, err := docs.Put(ctx, o.Docs, tenant, cSession, tokenID(tok), s, 0, r.Actor); err != nil {
		return "", err
	}
	detail := map[string]any{"operator": op.ID, "name": op.Name, "ip": ip, "areas": strings.Join(r.Areas, ","), "write": r.Write, "expires": r.Expires, "session": r.Sessions}
	o.log(ctx, tenant, r.Actor, "support_access.session_started", r.ID, detail)
	o.securityEvent(ctx, tenant, "support_access.session_started", "info", r.Actor, r.ID, detail)
	return tok, nil
}

func (o *Ops) supportExpired(ctx context.Context, tenant string, r SupportAccess) {
	o.closeWork(ctx, tenant, SystemActor, "support_access", r.ID, strings.ToLower(r.EndReason))
	o.log(ctx, tenant, SystemActor, "support_access.expired", r.ID, map[string]any{"reason": r.EndReason, "operator": r.Operator})
	o.securityEvent(ctx, tenant, "support_access.expired", "info", SystemActor, r.ID, nil)
}

// supportSessionState checks a support session on every request: the
// access is still approved and within its hours, this is its newest
// session, the firm has not blocked support and the operator is active.
func (o *Ops) supportSessionState(ctx context.Context, tenant string, s Session) (AuthState, error) {
	now := o.now()
	r, _, err := docs.Get[SupportAccess](ctx, o.Docs, tenant, cSupportAccess, s.Support)
	if err != nil || r.Actor != s.User || s.Revoked || r.Status != SAApproved || r.Epoch != s.Epoch {
		return AuthState{}, ErrSessionEnded
	}
	if o.supportDue(&r, now) {
		if _, err := upd(ctx, o.Docs, tenant, cSupportAccess, r.ID, SystemActor, nil, func(x *SupportAccess) error {
			if !o.supportDue(x, now) {
				return ErrState
			}
			return nil
		}); err == nil {
			o.supportExpired(ctx, tenant, r)
		}
		return AuthState{}, ErrSessionEnded
	}
	if o.supportPolicy(ctx, tenant).Blocked {
		return AuthState{}, ErrSessionEnded
	}
	op, err := o.supportOperator(ctx, r.Operator)
	if err != nil {
		return AuthState{}, ErrSessionEnded
	}
	if exp, err := time.Parse(time.RFC3339, s.Expires); err != nil || now.After(exp) {
		return AuthState{}, ErrSessionEnded
	}
	set, _ := o.Settings(ctx, tenant)
	idle := time.Duration(set.SessionIdleMinutes) * time.Minute
	if idle <= 0 {
		idle = 30 * time.Minute
	}
	if seen, err := time.Parse(time.RFC3339, s.LastSeen); err != nil || now.Sub(seen) > idle {
		return AuthState{}, ErrSessionEnded
	}
	return AuthState{User: s.User, TOTPEnabled: true, Support: &SupportAuth{Request: r.ID, Operator: op.ID, Name: op.Name, Firm: set.Firm, Ticket: r.Ticket,
		Areas: append([]string{}, r.Areas...), Write: r.Write, Expires: r.Expires}}, nil
}

// supportRole is the role a support actor acts with while its access is
// open: Admin, held inside the approved areas and read-only rule by
// SupportGate on every request, and never for the actions notSupport
// refuses. Without open access it is refused.
func (o *Ops) supportRole(ctx context.Context, tenant, actor string) (string, error) {
	rs, _ := docs.List[SupportAccess](ctx, o.Docs, tenant, cSupportAccess, "")
	now := o.now()
	for _, r := range rs {
		if r.Actor == actor && r.Status == SAApproved && r.Started != "" && !o.supportDue(&r, now) && !o.supportPolicy(ctx, tenant).Blocked {
			return RoleAdmin, nil
		}
	}
	return "", fmt.Errorf("%w: the Ledgerline support access has ended", ErrForbidden)
}

// SupportAction records one request a support session made, with what
// happened to it (refused says why it was refused). Reads are recorded as
// well as changes: looking at the firm's data is the access.
func (o *Ops) SupportAction(ctx context.Context, tenant string, a *SupportAuth, method, path string, status int, refused string) {
	actor := supportActor(a.Operator, tenant)
	d := map[string]any{"request": a.Request, "method": method, "path": trim(path, 300), "status": status}
	action := "support_access.action"
	if refused != "" {
		action, d["refused"] = "support_access.action_refused", trim(refused, 300)
		o.securityEvent(ctx, tenant, action, "warning", actor, a.Request, d)
	}
	o.log(ctx, tenant, actor, action, a.Request, d)
}

// supportAccessTick lapses waiting requests after 24 hours and closes
// access whose hours ran out, so the screen and the log show it even if
// support never came back.
func (o *Ops) supportAccessTick(ctx context.Context, tenant string, now time.Time) {
	rs, _ := docs.List[SupportAccess](ctx, o.Docs, tenant, cSupportAccess, "")
	for _, r := range rs {
		if r.Status != SAPending && r.Status != SAApproved {
			continue
		}
		x := r
		if !o.supportDue(&x, now.In(ist)) {
			continue
		}
		if _, err := upd(ctx, o.Docs, tenant, cSupportAccess, r.ID, SystemActor, nil, func(y *SupportAccess) error {
			if !o.supportDue(y, now.In(ist)) {
				return ErrState
			}
			return nil
		}); err == nil {
			o.supportExpired(ctx, tenant, x)
		}
	}
}

// ---- the firm's screen ----

// SupportAccessView is Admin → Support access.
type SupportAccessView struct {
	Policy        SupportPolicy   `json:"policy"`
	Pending       []SupportAccess `json:"pending"`
	Active        []SupportAccess `json:"active"`
	History       []SupportAccess `json:"history"`
	Areas         []SupportArea   `json:"areas"`
	ApproverRoles []string        `json:"approver_roles"`
	CanApprove    bool            `json:"can_approve"`
	CanChange     bool            `json:"can_change"`
	MaxHours      int             `json:"max_hours"`
}

func (o *Ops) canSeeSupportAccess(ctx context.Context, tenant, actor string) error {
	if err := notSupport(actor, "The support access screen"); err != nil {
		return err
	}
	if o.canApproveSupport(ctx, tenant, actor) == nil {
		return nil
	}
	return o.require(ctx, tenant, actor, "Viewing Ledgerline support access", RoleCompliance)
}

// CanSeeSupportAccess says whether the console shows the screen to actor.
func (o *Ops) CanSeeSupportAccess(ctx context.Context, tenant, actor string) bool {
	return actor != "" && o.canSeeSupportAccess(ctx, tenant, actor) == nil
}

// SupportAccessScreen lists the firm's requests and access.
func (o *Ops) SupportAccessScreen(ctx context.Context, tenant, actor string) (SupportAccessView, error) {
	if err := o.canSeeSupportAccess(ctx, tenant, actor); err != nil {
		return SupportAccessView{}, err
	}
	rs, err := docs.List[SupportAccess](ctx, o.Docs, tenant, cSupportAccess, "")
	if err != nil {
		return SupportAccessView{}, err
	}
	now := o.now()
	v := SupportAccessView{Policy: o.supportPolicy(ctx, tenant), Pending: []SupportAccess{}, Active: []SupportAccess{}, History: []SupportAccess{},
		Areas: SupportAreas, ApproverRoles: append([]string{RoleAdmin}, SupportApproverRoles...), MaxHours: supportMaxHours}
	v.CanApprove = o.canApproveSupport(ctx, tenant, actor) == nil
	v.CanChange = o.require(ctx, tenant, actor, "Changing Ledgerline support access settings") == nil
	sort.Slice(rs, func(i, j int) bool { return rs[i].Requested > rs[j].Requested })
	for _, r := range rs {
		o.supportDue(&r, now) // shown as it stands now; the tick saves it
		switch r.Status {
		case SAPending:
			v.Pending = append(v.Pending, r)
		case SAApproved:
			v.Active = append(v.Active, r)
		default:
			if len(v.History) < 200 {
				v.History = append(v.History, r)
			}
		}
	}
	return v, nil
}

// SupportLogLine is one line of the Support access log.
type SupportLogLine struct {
	At      string `json:"at"`
	Actor   string `json:"actor"`
	Action  string `json:"action"`
	Request string `json:"request"`
	What    string `json:"what"`
}

var supportLogNames = map[string]string{
	"support_access.requested": "Asked for access", "support_access.approved": "Approved", "support_access.refused": "Refused",
	"support_access.withdrawn": "Withdrawn by support", "support_access.ended": "Ended", "support_access.expired": "Ended: time ran out",
	"support_access.session_started": "Support session started", "support_access.action": "Action by support",
	"support_access.action_refused": "Refused to support", "support_access.refused_blocked": "Request refused: support is blocked",
	"support_access.policy": "Settings changed", "support_access.blocked": "All support access blocked", "support_access.unblocked": "Support access allowed again",
	"support_access.log_downloaded": "Log downloaded", "auth.logout": "Support signed out",
}

// SupportAccessLog reads the firm's audit trail for everything about
// Ledgerline support access and everything support did, newest first.
func (o *Ops) SupportAccessLog(ctx context.Context, tenant, actor string, limit int) ([]SupportLogLine, error) {
	if err := o.canSeeSupportAccess(ctx, tenant, actor); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 5000
	}
	evs, err := o.App.Audit.List(ctx, tenant, 50000)
	if err != nil {
		return nil, err
	}
	out := []SupportLogLine{}
	for _, e := range evs {
		if !strings.HasPrefix(e.Action, "support_access.") && !IsSupportActor(e.Actor) {
			continue
		}
		name := supportLogNames[e.Action]
		if name == "" {
			name = e.Action
		}
		var parts []string
		for _, k := range sortedKeys(e.Detail) {
			parts = append(parts, fmt.Sprintf("%s: %v", k, e.Detail[k]))
		}
		req := e.Object
		if r, ok := e.Detail["request"].(string); ok {
			req = r
		}
		what := name
		if len(parts) > 0 {
			what += " · " + strings.Join(parts, " · ")
		}
		out = append(out, SupportLogLine{At: e.At.In(ist).Format(time.RFC3339), Actor: e.Actor, Action: e.Action, Request: req, What: what})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SupportAccessLogCSV is the log for download.
func (o *Ops) SupportAccessLogCSV(ctx context.Context, tenant, actor string) ([]byte, error) {
	ls, err := o.SupportAccessLog(ctx, tenant, actor, 1<<30)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"Time (IST)", "By", "Event", "Request", "Details"})
	for _, l := range ls {
		_ = w.Write([]string{l.At, l.Actor, l.Action, l.Request, l.What})
	}
	w.Flush()
	o.log(ctx, tenant, actor, "support_access.log_downloaded", "", map[string]any{"lines": len(ls)})
	return b.Bytes(), w.Error()
}

// ---- the operator's own view ----

// OperatorFirm is a firm on this server, as the support desk lists it.
type OperatorFirm struct {
	Tenant  string `json:"tenant"`
	Firm    string `json:"firm"`
	Blocked bool   `json:"blocked"`
}

// OperatorFirms lists the firms an operator may ask (names only; nothing
// of a firm's data is read).
func (o *Ops) OperatorFirms(ctx context.Context, tenants []string) []OperatorFirm {
	out := []OperatorFirm{}
	for _, t := range tenants {
		set, _ := o.Settings(ctx, t)
		out = append(out, OperatorFirm{Tenant: t, Firm: set.Firm, Blocked: o.supportPolicy(ctx, t).Blocked})
	}
	return out
}

// OperatorRequest is one of an operator's requests, with its firm.
type OperatorRequest struct {
	Tenant string `json:"tenant"`
	SupportAccess
}

// OperatorRequests lists an operator's requests at every firm, newest first.
func (o *Ops) OperatorRequests(ctx context.Context, operator string, tenants []string) ([]OperatorRequest, error) {
	if _, err := o.supportOperator(ctx, operator); err != nil {
		return nil, err
	}
	now := o.now()
	out := []OperatorRequest{}
	for _, t := range tenants {
		rs, _ := docs.List[SupportAccess](ctx, o.Docs, t, cSupportAccess, "")
		for _, r := range rs {
			if r.Operator == operator {
				o.supportDue(&r, now)
				out = append(out, OperatorRequest{Tenant: t, SupportAccess: r})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Requested > out[j].Requested })
	return out, nil
}
