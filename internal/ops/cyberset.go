package ops

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- cyber resilience settings: recovery targets, intraday backups,
// API callers, confidentiality agreements, capacity thresholds ----
//
//   - Recovery targets (CSCRF RC.RP.S2, SEBI 2025/119 para 6.10: RTO 2
//     hours, RPO 15 minutes for critical systems). The firm may set its
//     own, never looser than those for a critical system unless it says
//     why. Restore drills are measured against them.
//   - Intraday backups (backuprpo.go): a backup every N minutes during the
//     working day, so the data lost in a failure is minutes, not a day.
//   - API callers (CSCRF PR.AA.S17: rate limiting and whitelisted API
//     connections). The addresses an API key or a webhook may be called
//     from, and the calls a key may make each minute.
//   - Confidentiality agreements (CSCRF GV.RR.S5): system access only
//     after a confidentiality and integrity agreement is signed, for staff
//     and third parties. With HoldLogins on, a new login cannot sign in
//     until its agreement is recorded; logins made before stay as they are.
//   - Capacity thresholds (capacity.go).

const (
	cCyberSet   = "cyberset"
	cCyberState = "cyberstate"
	cConfAgree  = "confagree" // by user ID
)

// CyberSettings are the firm's settings for these controls.
type CyberSettings struct {
	RTOMin    int    `json:"rto_min"`
	RPOMin    int    `json:"rpo_min"`
	TargetWhy string `json:"target_why,omitempty"` // why the targets are looser than 2 h / 15 min
	// IntradayMinutes: a backup every this many minutes between
	// IntradayFrom and IntradayTo on trading days (0: off).
	IntradayMinutes int    `json:"intraday_minutes"`
	IntradayFrom    string `json:"intraday_from"`
	IntradayTo      string `json:"intraday_to"`
	// APICallers and HookCallers are addresses or ranges (CIDR); empty:
	// any. APIPerMinute is the calls one API key may make in a minute.
	APICallers   []string `json:"api_callers"`
	HookCallers  []string `json:"hook_callers"`
	APIPerMinute int      `json:"api_per_minute"`
	HoldLogins   bool     `json:"hold_logins"`
	// Capacity thresholds: slow answers (95th percentile, ms), busy
	// minutes (requests in a minute), data size (MB), backup time (min).
	SlowMs       int    `json:"slow_ms"`
	BusyPerMin   int    `json:"busy_per_min"`
	DataMB       int    `json:"data_mb"`
	BackupMaxMin int    `json:"backup_max_min"`
	UpdatedAt    string `json:"updated_at,omitempty"`
	UpdatedBy    string `json:"updated_by,omitempty"`
}

// IntradayChoices are the intervals offered.
var IntradayChoices = []int{0, 5, 10, 15, 30, 60, 120, 240}

func (s CyberSettings) withDefaults() CyberSettings {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&s.RTOMin, RTOTargetMin)
	def(&s.RPOMin, RPOTargetMin)
	def(&s.APIPerMinute, 6000)
	def(&s.SlowMs, 2000)
	def(&s.BusyPerMin, 3000)
	def(&s.BackupMaxMin, 30)
	if s.IntradayFrom == "" {
		s.IntradayFrom = "09:00"
	}
	if s.IntradayTo == "" {
		s.IntradayTo = "23:30"
	}
	if s.APICallers == nil {
		s.APICallers = []string{}
	}
	if s.HookCallers == nil {
		s.HookCallers = []string{}
	}
	return s
}

// CyberSettingsOf returns the firm's settings with defaults.
func (o *Ops) CyberSettingsOf(ctx context.Context, tenant string) CyberSettings {
	s, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCyberSet, "main", CyberSettings{})
	return s.withDefaults()
}

func cleanNets(xs []string, what string) ([]string, error) {
	out := []string{}
	for _, n := range xs {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		if _, err := netip.ParsePrefix(n); err != nil {
			if _, err := netip.ParseAddr(n); err != nil {
				return nil, invalid("%s: %q is not an address or a range like 203.0.113.0/24", what, n)
			}
		}
		if !contains(out, n) {
			out = append(out, n)
		}
	}
	if len(out) > 100 {
		return nil, invalid("%s: give at most 100", what)
	}
	return out, nil
}

// SaveCyberSettings checks and saves the settings. Compliance and Admin.
func (o *Ops) SaveCyberSettings(ctx context.Context, tenant, actor string, s CyberSettings) (CyberSettings, error) {
	if err := o.require(ctx, tenant, actor, "Changing cyber resilience settings", RoleCompliance); err != nil {
		return CyberSettings{}, err
	}
	rng := func(v, lo, hi int, what string) error {
		if v != 0 && (v < lo || v > hi) {
			return invalid("%s must be between %d and %d", what, lo, hi)
		}
		return nil
	}
	for _, e := range []error{rng(s.RTOMin, 15, 2880, "the RTO (minutes)"), rng(s.RPOMin, 1, 1440, "the RPO (minutes)"), rng(s.APIPerMinute, 30, 20000, "API calls a minute"),
		rng(s.SlowMs, 200, 60000, "the slow answer time (ms)"), rng(s.BusyPerMin, 10, 1000000, "requests a minute"), rng(s.DataMB, 1, 10000000, "the data size (MB)"), rng(s.BackupMaxMin, 1, 600, "the backup time (minutes)")} {
		if e != nil {
			return CyberSettings{}, e
		}
	}
	if !intIn(IntradayChoices, s.IntradayMinutes) {
		return CyberSettings{}, invalid("intraday backups run every 5, 10, 15, 30, 60, 120 or 240 minutes, or are off")
	}
	for _, t := range []string{s.IntradayFrom, s.IntradayTo} {
		if t != "" && !reHHMM.MatchString(t) {
			return CyberSettings{}, invalid("the intraday window is HH:MM to HH:MM")
		}
	}
	if s.IntradayFrom != "" && s.IntradayTo != "" && s.IntradayFrom >= s.IntradayTo {
		return CyberSettings{}, invalid("the intraday window ends after it starts")
	}
	if (s.RTOMin > RTOTargetMin || s.RPOMin > RPOTargetMin) && len(strings.TrimSpace(s.TargetWhy)) < 10 {
		return CyberSettings{}, invalid("targets looser than RTO 2 hours and RPO 15 minutes need a reason (for example: Ledgerline is not a critical system in the firm's CSCRF list)")
	}
	var err error
	if s.APICallers, err = cleanNets(s.APICallers, "API callers"); err != nil {
		return CyberSettings{}, err
	}
	if s.HookCallers, err = cleanNets(s.HookCallers, "webhook callers"); err != nil {
		return CyberSettings{}, err
	}
	s.TargetWhy = trim(strings.TrimSpace(s.TargetWhy), 500)
	s.UpdatedAt, s.UpdatedBy = o.now().Format(time.RFC3339), actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cCyberSet, "main", s, actor); err != nil {
		return CyberSettings{}, err
	}
	s = s.withDefaults()
	o.log(ctx, tenant, actor, "security.cyber_settings", "cyber", map[string]any{"rto_min": s.RTOMin, "rpo_min": s.RPOMin, "intraday_minutes": s.IntradayMinutes,
		"api_callers": len(s.APICallers), "hook_callers": len(s.HookCallers), "api_per_minute": s.APIPerMinute, "hold_logins": s.HoldLogins})
	return s, nil
}

func intIn(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// ---- API callers ----

// APIGuard is what the API server enforces for a firm.
type APIGuard struct {
	API       []netip.Prefix
	Hooks     []netip.Prefix
	PerMinute int
}

func prefixes(xs []string) []netip.Prefix {
	var out []netip.Prefix
	for _, x := range xs {
		if p, err := netip.ParsePrefix(x); err == nil {
			out = append(out, p.Masked())
		} else if a, err := netip.ParseAddr(x); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out
}

// APIGuardOf returns the firm's API caller rules.
func (o *Ops) APIGuardOf(ctx context.Context, tenant string) APIGuard {
	s := o.CyberSettingsOf(ctx, tenant)
	return APIGuard{API: prefixes(s.APICallers), Hooks: prefixes(s.HookCallers), PerMinute: s.APIPerMinute}
}

// Allowed reports whether ip may call: always when the list is empty.
func AllowedCaller(list []netip.Prefix, ip string) bool {
	if len(list) == 0 {
		return true
	}
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// NoteRefusedCaller records a call refused by the caller list or the
// rate limit as a security event.
func (o *Ops) NoteRefusedCaller(ctx context.Context, tenant, what, ip, path string) {
	o.SecurityEvent(ctx, tenant, SecEvent{Kind: "api.refused", Severity: SevMedium, Actor: "api", Subject: ip, Message: fmt.Sprintf("%s refused from %s: %s", path, ip, what), Detail: map[string]any{"ip": ip, "path": path, "why": what}})
}

// ---- confidentiality agreements ----

// ConfAgreement is a person's signed confidentiality and integrity
// agreement, or a login waiting for one.
type ConfAgreement struct {
	User    string `json:"user"`
	Kind    string `json:"kind"`            // Employee, Third party
	Party   string `json:"party,omitempty"` // the third party's organisation
	Signed  string `json:"signed,omitempty"`
	Doc     string `json:"doc,omitempty"` // where the signed copy is
	Pending bool   `json:"pending,omitempty"`
	Since   string `json:"since,omitempty"` // held since
	By      string `json:"by,omitempty"`
	At      string `json:"at,omitempty"`
}

// agreementOnCreate holds a new login until its agreement is recorded,
// when the firm has asked for that.
func (o *Ops) agreementOnCreate(ctx context.Context, tenant, actor string, u User) {
	if u.Role == RoleClient || !o.CyberSettingsOf(ctx, tenant).HoldLogins {
		return
	}
	if a, _, err := docs.Get[ConfAgreement](ctx, o.Docs, tenant, cConfAgree, u.ID); err == nil && a.Signed != "" {
		return
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cConfAgree, u.ID, ConfAgreement{User: u.ID, Kind: "Employee", Pending: true, Since: o.now().Format(time.RFC3339)}, actor)
	o.raiseWork(ctx, tenant, "security", "agreement:"+u.ID, "Record "+u.ID+"'s confidentiality agreement",
		"The login "+u.ID+" cannot sign in until a signed confidentiality and integrity agreement is recorded (CSCRF GV.RR.S5; Security → Cyber controls → Agreements).", "", RoleAdmin, "cyber", o.today())
}

// agreementHeld reports whether a login waits for its agreement.
func (o *Ops) agreementHeld(ctx context.Context, tenant, user string) bool {
	a, _, err := docs.Get[ConfAgreement](ctx, o.Docs, tenant, cConfAgree, user)
	return err == nil && a.Pending && a.Signed == ""
}

// RecordAgreement records a signed agreement for a login (an employee or
// a third party's staff) and releases a held login. Admin, HR and
// compliance.
func (o *Ops) RecordAgreement(ctx context.Context, tenant, actor string, a ConfAgreement) (ConfAgreement, error) {
	if err := o.require(ctx, tenant, actor, "Recording confidentiality agreements", RoleCompliance, RoleHR); err != nil {
		return a, err
	}
	a.User = strings.ToLower(strings.TrimSpace(a.User))
	if _, err := o.user(ctx, tenant, a.User); err != nil {
		return a, invalid("no login %s", a.User)
	}
	if a.Kind != "Employee" && a.Kind != "Third party" {
		return a, invalid("the agreement is an Employee's or a Third party's")
	}
	if a.Kind == "Third party" && strings.TrimSpace(a.Party) == "" {
		return a, invalid("name the third party the person works for")
	}
	if !validDay(a.Signed) || a.Signed > o.today() {
		return a, invalid("give the date it was signed (YYYY-MM-DD, not in the future)")
	}
	if strings.TrimSpace(a.Doc) == "" {
		return a, invalid("say where the signed copy is (a document reference)")
	}
	a.Party, a.Doc = trim(strings.TrimSpace(a.Party), 120), trim(strings.TrimSpace(a.Doc), 200)
	a.Pending, a.By, a.At = false, actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cConfAgree, a.User, a, actor); err != nil {
		return a, err
	}
	o.closeWork(ctx, tenant, actor, "security", "agreement:"+a.User, "agreement recorded")
	if e, ok := o.employeeOf(ctx, tenant, a.User); ok {
		o.tickItem(ctx, tenant, actor, e.Code, "join", "nda", "signed "+a.Signed)
	}
	o.log(ctx, tenant, actor, "security.agreement", a.User, map[string]any{"kind": a.Kind, "party": a.Party, "signed": a.Signed})
	return a, nil
}

// agreementFromChecklist: the joining checklist's agreement item was
// ticked, so the employee's login has its agreement.
func (o *Ops) agreementFromChecklist(ctx context.Context, tenant, actor, code, note string) {
	e, err := o.employee(ctx, tenant, code)
	if err != nil || e.User == "" {
		return
	}
	if a, _, err := docs.Get[ConfAgreement](ctx, o.Docs, tenant, cConfAgree, e.User); err == nil && a.Signed != "" {
		return
	}
	a := ConfAgreement{User: e.User, Kind: "Employee", Signed: o.today(), Doc: firstOf(note, "joining checklist"), By: actor, At: o.now().Format(time.RFC3339)}
	_ = docs.Upsert(ctx, o.Docs, tenant, cConfAgree, e.User, a, actor)
	o.closeWork(ctx, tenant, actor, "security", "agreement:"+e.User, "agreement recorded on the joining checklist")
	o.log(ctx, tenant, actor, "security.agreement", e.User, map[string]any{"kind": a.Kind, "signed": a.Signed, "from": "joining checklist"})
}

// AgreementRow is a login and its agreement.
type AgreementRow struct {
	User string `json:"user"`
	Name string `json:"name"`
	Role string `json:"role"`
	ConfAgreement
	Missing bool `json:"missing"`
}

func (o *Ops) agreementRows(ctx context.Context, tenant string) []AgreementRow {
	us, _ := o.Users(ctx, tenant)
	out := []AgreementRow{}
	for _, u := range us {
		if u.Role == RoleClient || !u.Active {
			continue
		}
		a, _, _ := docs.Get[ConfAgreement](ctx, o.Docs, tenant, cConfAgree, u.ID)
		out = append(out, AgreementRow{User: u.ID, Name: u.Name, Role: u.Role, ConfAgreement: a, Missing: a.Signed == ""})
	}
	return out
}
