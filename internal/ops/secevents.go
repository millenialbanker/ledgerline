package ops

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/docs"
	"ledgerline/internal/mail"
)

// ---- security events: one typed stream per firm ----
//
// CERT-In's directions of 28 April 2022 (under section 70B(6) of the IT
// Act) ask every service provider and intermediary to report a cyber
// incident to CERT-In within 6 hours of noticing it, and to keep the logs
// of its ICT systems for 180 days, in India. SEBI's cybersecurity and cyber
// resilience framework (CSCRF, 20 Aug 2024) asks a regulated entity to send
// its logs to its security operations centre (SOC) and to report incidents;
// its cloud framework asks the cloud provider to tell the firm of an
// incident promptly.
//
// Every firm has one stream of security events: sign-ins and their
// failures, lockouts, two-factor resets, new users and role changes, an
// Admin role granted, sign-ins from a new address or from outside the
// firm's networks, large downloads, audit seal failures, malware found in
// an upload, changes to security settings and to connection secrets, and
// Ledgerline support staff's access. Most come from the audit trail: the
// app shows Ops every audit entry (app.App.Observe) and observeAudit picks
// out the actions below, so code that already writes those entries feeds
// the stream without knowing about it. Others are recorded directly with
// SecurityEvent. There is also a stream for the platform itself (the
// operator's: tenant PlatformTenant), with the incidents the provider
// declared.
//
// Detection rules (secRules) turn events into incidents: simple counts in a
// window (wrong passwords for one user, or from one address) and single
// events that always matter (an audit seal that does not match, malware,
// an Admin role granted, a very large download, a sign-in from outside the
// firm's networks). A rule match opens one incident and alerts once; later
// matches of the same rule and subject join that incident while it is not
// closed (secincidents.go). Events and sign-ins are sent to the firm's SIEM
// and can be downloaded (seclogexport.go).

const (
	cSecEvent  = "secevent"  // key: UTC time to the microsecond "|" sequence; sorts by time
	cSecWindow = "secwindow" // the counting rules' recent times, by rule and subject
	cSecKnown  = "secknown"  // per user: addresses signed in from
	cSecSet    = "secset"    // "main": the firm's security settings
	cSecState  = "secstate"  // "main": purge and nightly check progress
	// PlatformTenant holds the operator's own stream and the platform
	// incidents it declared. It is not a firm.
	PlatformTenant = "_platform"
)

// Severities, most serious first.
const (
	SevCritical = "Critical"
	SevHigh     = "High"
	SevMedium   = "Medium"
	SevLow      = "Low"
	SevInfo     = "Info"
)

// Severities lists them for choices on screen.
var Severities = []string{SevCritical, SevHigh, SevMedium, SevLow, SevInfo}

// SecEvent is one security event.
type SecEvent struct {
	ID       string         `json:"id"`
	At       string         `json:"at"`  // RFC 3339 in IST
	Log      string         `json:"log"` // security or signin
	Kind     string         `json:"kind"`
	Severity string         `json:"severity"`
	Actor    string         `json:"actor,omitempty"`   // who acted: a user ID, system:…, support:…
	Subject  string         `json:"subject,omitempty"` // whom or what it is about: a user ID, a connection, a file
	IP       string         `json:"ip,omitempty"`
	Message  string         `json:"message"`
	Detail   map[string]any `json:"detail,omitempty"`
	Incident string         `json:"incident,omitempty"` // the incident it opened or joined
	// Purged: older than the firm's retention period, so the person,
	// address and detail were cleared (kind and time are kept).
	Purged bool `json:"purged,omitempty"`
}

// SecKind describes one kind of event.
type SecKind struct {
	Log      string `json:"log"` // security or signin
	Severity string `json:"severity"`
	Label    string `json:"label"`
}

// SecKinds are the kinds of security event. Other code may record any of
// these with SecurityEvent; the label is what the register shows.
var SecKinds = map[string]SecKind{
	"signin.ok":                        {"signin", SevInfo, "Signed in"},
	"signin.failed":                    {"signin", SevLow, "Sign-in failed"},
	"signin.locked":                    {"signin", SevMedium, "User locked after wrong attempts"},
	"signin.rate_limited":              {"signin", SevMedium, "Too many sign-in attempts from one address"},
	"signin.logout":                    {"signin", SevInfo, "Signed out"},
	"signin.new_ip":                    {"signin", SevLow, "Signed in from a new address"},
	"signin.outside_ip_rules":          {"security", SevHigh, "Signed in from outside the firm's networks"},
	"account.created":                  {"security", SevLow, "User added"},
	"account.role_changed":             {"security", SevMedium, "Role changed"},
	"account.role_permissions_changed": {"security", SevMedium, "Role permissions changed"},
	"account.admin_granted":            {"security", SevHigh, "Admin role granted"},
	"account.deactivated":              {"security", SevLow, "User deactivated"},
	"account.password_changed":         {"security", SevInfo, "Password changed"},
	"account.password_reset":           {"security", SevLow, "Password reset by an administrator"},
	"account.2fa_enabled":              {"security", SevInfo, "Two-factor sign-in set up"},
	"account.2fa_reset":                {"security", SevMedium, "Two-factor sign-in reset"},
	"settings.security_changed":        {"security", SevMedium, "Security settings changed"},
	"connection.secret_changed":        {"security", SevMedium, "Connection secret changed"},
	"connection.removed":               {"security", SevLow, "Connection removed"},
	"data.reveal":                      {"security", SevInfo, "Full client identifier seen"},
	"data.download":                    {"security", SevInfo, "Large file downloaded"},
	"data.large_download":              {"security", SevMedium, "Very large download or export"},
	"data.logs_downloaded":             {"security", SevLow, "Security logs downloaded"},
	"audit.seal_failed":                {"security", SevCritical, "Audit trail seal check failed"},
	"audit.seal_ok":                    {"security", SevInfo, "Audit trail seals checked"},
	"malware.found":                    {"security", SevHigh, "Malware found in an upload"},
	"malware.scanner_down":             {"security", SevMedium, "Malware scanner not answering"},
	"support.access":                   {"security", SevMedium, "Ledgerline support staff access"},
	"support.access_unapproved":        {"security", SevHigh, "Ledgerline support access without the firm's approval"},
	"logexport.stuck":                  {"security", SevMedium, "Log export to the SIEM is stuck"},
	"logexport.test":                   {"security", SevInfo, "Log export test"},
	"incident.provider_notice":         {"security", SevHigh, "Incident notice from Ledgerline"},
	"platform.incident":                {"security", SevHigh, "Platform incident declared"},
	"platform.incident_update":         {"security", SevMedium, "Platform incident updated"},
	"mobile.rooted_device":             {"security", SevMedium, "Phone that looks rooted or jailbroken registered"},
	"mobile.unlock_failed":             {"security", SevMedium, "Phone revoked after wrong unlock attempts"},
	"client.fraud_reported":            {"security", SevHigh, "Client reported a fraudulent transaction"},
	"signin.unlocked":                  {"signin", SevLow, "Locked sign-in unlocked"},
}

// SecurityEvent records one event in the firm's security stream, runs the
// detection rules on it and returns it as stored (with the incident it
// opened or joined, if any). It is the one way to add an event:
//
//	o.SecurityEvent(ctx, tenant, SecEvent{Kind: "support.access",
//		Actor: "support:asha", Subject: "client screens",
//		Message: "Opened the firm's console for request SR-000123"})
//
// Kind should be one of SecKinds (its severity and log are filled in when
// left empty); Actor is who acted, Subject whom or what it is about, IP
// the address the request came from. Detail must not hold secrets or full
// client identifiers. Recording never fails the caller's work: problems
// are swallowed and the event is simply not kept.
func (o *Ops) SecurityEvent(ctx context.Context, tenant string, e SecEvent) SecEvent {
	if tenant == "" {
		return e
	}
	k, known := SecKinds[e.Kind]
	if e.Severity == "" {
		e.Severity = firstOf(k.Severity, SevInfo)
	}
	if e.Log == "" {
		e.Log = firstOf(k.Log, "security")
	}
	if e.Message == "" {
		e.Message = firstOf(k.Label, e.Kind)
	}
	if !known && !strings.Contains(e.Kind, ".") {
		e.Kind = "other." + firstOf(e.Kind, "event")
	}
	now := o.now()
	n, err := o.next(ctx, tenant, "secevent", SystemActor)
	if err != nil {
		return e
	}
	e.ID = now.UTC().Format("2006-01-02T15:04:05.000000Z") + "|" + fmt.Sprintf("%08d", n)
	e.At = now.Format(time.RFC3339)
	e.Message, e.Actor, e.Subject, e.IP = trim(e.Message, 500), trim(e.Actor, 120), trim(e.Subject, 200), trim(e.IP, 64)
	if tenant != PlatformTenant {
		if inc := o.secDetect(ctx, tenant, &e, now); inc != "" {
			e.Incident = inc
		}
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cSecEvent, e.ID, e, SystemActor)
	return e
}

// PlatformEvent records an event in the operator's own stream.
func (o *Ops) PlatformEvent(ctx context.Context, e SecEvent) SecEvent {
	return o.SecurityEvent(ctx, PlatformTenant, e)
}

// observeAudit picks the security events out of the audit trail. It is
// called for every audit entry, so anything it does not care about returns
// at once.
func (o *Ops) observeAudit(ctx context.Context, a app.AuditEvent) {
	if a.Tenant == "" || a.Tenant == PlatformTenant {
		return
	}
	str := func(k string) string { s, _ := a.Detail[k].(string); return s }
	e := SecEvent{Actor: a.Actor, Subject: a.Object, IP: str("ip")}
	switch a.Action {
	case "auth.login":
		e.Kind, e.Message = "signin.ok", a.Object+" signed in"
		e.Detail = map[string]any{"two_factor": a.Detail["two_factor"]}
		o.SecurityEvent(ctx, a.Tenant, e)
		o.signinChecks(ctx, a.Tenant, a.Object, e.IP)
		return
	case "auth.login_failed":
		e.Kind, e.Message = "signin.failed", fmt.Sprintf("Sign-in failed for %s (%s)", a.Object, firstOf(str("reason"), "wrong details"))
		e.Detail = map[string]any{"reason": str("reason")}
		o.SecurityEvent(ctx, a.Tenant, e)
		if locked, _ := a.Detail["locked"].(bool); locked {
			o.SecurityEvent(ctx, a.Tenant, SecEvent{Kind: "signin.locked", Actor: a.Actor, Subject: a.Object, IP: e.IP, Message: a.Object + " locked after repeated wrong passwords"})
		}
		return
	case "user.unlocked":
		e.Kind, e.Message = "signin.unlocked", fmt.Sprintf("%s's locked sign-in was unlocked (%s)", a.Object, firstOf(str("how"), "out of band"))
	case "auth.logout":
		e.Kind, e.Message = "signin.logout", a.Object+" signed out"
	case "auth.password_changed":
		e.Kind, e.Message = "account.password_changed", a.Object+" changed their password"
	case "auth.2fa_enabled":
		e.Kind, e.Message = "account.2fa_enabled", a.Object+" set up two-factor sign-in"
	case "user.password_reset":
		e.Kind, e.Message = "account.password_reset", a.Actor+" reset the password of "+a.Object
	case "user.2fa_reset":
		e.Kind, e.Message = "account.2fa_reset", a.Actor+" reset the two-factor sign-in of "+a.Object
	case "user.deactivated":
		e.Kind, e.Message = "account.deactivated", a.Actor+" deactivated "+a.Object
	case "user.role_changed":
		from, to := str("from"), str("to")
		if (from == "" || from == RoleClient) && to == RoleClient {
			return // a client's portal login, not staff
		}
		if a.Actor == "bootstrap" {
			return // the firm's first administrator, made when the firm is set up
		}
		e.Detail = map[string]any{"from": from, "to": to}
		switch {
		case to == RoleAdmin:
			e.Kind, e.Message = "account.admin_granted", a.Actor+" gave "+a.Object+" the Admin role"
		case from == "":
			e.Kind, e.Message = "account.created", a.Actor+" added "+a.Object+" as "+to
		default:
			e.Kind, e.Message = "account.role_changed", fmt.Sprintf("%s changed the role of %s from %s to %s", a.Actor, a.Object, from, to)
		}
	case "role.changed":
		// A role's permissions changed (roles.go), after a second Admin approved.
		e.Kind, e.Message = "account.role_permissions_changed", fmt.Sprintf("%s approved a change to the %s role proposed by %s", a.Actor, a.Object, str("proposed_by"))
		e.Detail = map[string]any{"change": str("change")}
	case "settings.security_changed", "security.settings.saved":
		e.Kind, e.Message = "settings.security_changed", a.Actor+" changed security settings"
		e.Detail = map[string]any{"changed": a.Detail["changed"]}
	case "connection.saved":
		ch, _ := a.Detail["secrets_changed"].([]string)
		if xs, ok := a.Detail["secrets_changed"].([]any); ok {
			for _, x := range xs {
				ch = append(ch, fmt.Sprint(x))
			}
		}
		if len(ch) == 0 {
			return
		}
		e.Kind, e.Message = "connection.secret_changed", fmt.Sprintf("%s changed the %s of the %s connection", a.Actor, strings.Join(ch, ", "), a.Object)
		e.Detail = map[string]any{"secrets": ch}
	case "connection.removed":
		e.Kind, e.Message = "connection.removed", a.Actor+" removed the "+a.Object+" connection"
	case "client.reveal":
		e.Kind, e.Message = "data.reveal", fmt.Sprintf("%s saw the full %s of client %s", a.Actor, str("field"), a.Object)
		e.Detail = map[string]any{"field": str("field")}
	case "security.audit.verified":
		if okv, _ := a.Detail["ok"].(bool); okv {
			e.Kind, e.Message = "audit.seal_ok", "Audit trail seals checked: all match"
		} else {
			e.Kind, e.Message = "audit.seal_failed", fmt.Sprintf("Audit trail seal check found %v problem(s): an entry was changed, removed or inserted", a.Detail["problems"])
		}
		e.Subject, e.Detail = "audit trail", map[string]any{"problems": a.Detail["problems"], "sealed": a.Detail["sealed"]}
	case "file.malware_found":
		sig := str("signature")
		e.Kind, e.Message = "malware.found", "Malware found in an uploaded file and refused: "+firstOf(sig, "unknown signature")
		e.Detail = map[string]any{"signature": sig, "source": str("source")}
	case "file.scan_failed":
		e.Kind, e.Message = "malware.scanner_down", "The malware scanner could not be reached; the upload was refused"
		e.Detail = map[string]any{"source": str("source")}
	default:
		return
	}
	o.SecurityEvent(ctx, a.Tenant, e)
}

// signinChecks: a sign-in from an address outside the firm's networks (when
// it has set them) or one the user has not signed in from before.
func (o *Ops) signinChecks(ctx context.Context, tenant, user, ip string) {
	if ip == "" || user == "" {
		return
	}
	set := o.SecuritySettings(ctx, tenant)
	if len(set.AllowedNetworks) > 0 && !inNetworks(ip, set.AllowedNetworks) {
		o.SecurityEvent(ctx, tenant, SecEvent{Kind: "signin.outside_ip_rules", Actor: user, Subject: user, IP: ip,
			Message: fmt.Sprintf("%s signed in from %s, outside the firm's networks", user, ip)})
	}
	now := o.now().Format(time.RFC3339)
	var isNew, hadAny bool
	_, _ = upd(ctx, o.Docs, tenant, cSecKnown, user, SystemActor, &secKnown{}, func(k *secKnown) error {
		if k.IPs == nil {
			k.IPs = map[string]string{}
		}
		hadAny = len(k.IPs) > 0
		_, seen := k.IPs[ip]
		isNew = !seen
		k.IPs[ip] = now
		if len(k.IPs) > 50 { // keep the 50 most recent
			type kv struct{ ip, at string }
			var xs []kv
			for a, t := range k.IPs {
				xs = append(xs, kv{a, t})
			}
			sort.Slice(xs, func(i, j int) bool { return xs[i].at > xs[j].at })
			k.IPs = map[string]string{}
			for _, x := range xs[:50] {
				k.IPs[x.ip] = x.at
			}
		}
		return nil
	})
	if isNew && hadAny {
		o.SecurityEvent(ctx, tenant, SecEvent{Kind: "signin.new_ip", Actor: user, Subject: user, IP: ip, Message: fmt.Sprintf("%s signed in from %s for the first time", user, ip)})
	}
}

type secKnown struct {
	IPs map[string]string `json:"ips"` // address -> last sign-in
}

func inNetworks(ip string, nets []string) bool {
	a := net.ParseIP(ip)
	if a == nil {
		return false
	}
	for _, n := range nets {
		if _, c, err := net.ParseCIDR(n); err == nil && c.Contains(a) {
			return true
		}
		if b := net.ParseIP(n); b != nil && b.Equal(a) {
			return true
		}
	}
	return false
}

// NoteDownload records a download from the console or the API that is
// large (1 MB or more): an event, and above the firm's limit one that the
// "very large download" rule turns into an incident. what is the path.
func (o *Ops) NoteDownload(ctx context.Context, tenant, actor, what string, bytes int64, ip string) {
	if bytes < 1<<20 || tenant == "" {
		return
	}
	set := o.SecuritySettings(ctx, tenant)
	kind := "data.download"
	if bytes >= int64(set.LargeDownloadMB)<<20 {
		kind = "data.large_download"
	}
	o.SecurityEvent(ctx, tenant, SecEvent{Kind: kind, Actor: actor, Subject: trim(what, 200), IP: ip,
		Message: fmt.Sprintf("%s downloaded %.1f MB (%s)", firstOf(actor, "someone"), float64(bytes)/(1<<20), trim(what, 120)),
		Detail:  map[string]any{"bytes": bytes}})
}

// securitySettingChanges names the sign-in and secret settings that
// changed between two saves of the firm's settings.
func securitySettingChanges(a, b Settings) []string {
	var ch []string
	if a.Require2FA() != b.Require2FA() {
		ch = append(ch, "two-factor sign-in "+map[bool]string{true: "required", false: "optional"}[b.Require2FA()])
	}
	if a.SessionIdleMinutes != b.SessionIdleMinutes {
		ch = append(ch, fmt.Sprintf("idle sign-out %d minutes", b.SessionIdleMinutes))
	}
	if a.GatewaySecret != b.GatewaySecret {
		ch = append(ch, "payment gateway secret")
	}
	if a.WhatsAppSecret != b.WhatsAppSecret {
		ch = append(ch, "WhatsApp secret")
	}
	return ch
}

// ---- the firm's security settings ----

// SecuritySettings are the firm's contacts for alerts, the thresholds of
// the detection rules, its own networks and how long events are kept.
type SecuritySettings struct {
	// Contacts get incident alerts by e-mail (empty: the compliance e-mail
	// and the management e-mails in Settings).
	Contacts []string `json:"contacts"`
	// Wrong sign-ins: FailedUserN for one user, or FailedIPN from one
	// address, within the minutes given, open an incident.
	FailedUserN       int `json:"failed_user_n"`
	FailedUserMinutes int `json:"failed_user_minutes"`
	FailedIPN         int `json:"failed_ip_n"`
	FailedIPMinutes   int `json:"failed_ip_minutes"`
	// LargeDownloadMB: a single download or export this large opens an
	// incident.
	LargeDownloadMB int `json:"large_download_mb"`
	// NoAdminAlert: granting the Admin role does not open an incident (it
	// is still an event).
	NoAdminAlert bool `json:"no_admin_alert,omitempty"`
	// AllowedNetworks are the firm's own addresses (CIDR ranges or single
	// addresses). A sign-in from elsewhere opens an incident; it is not
	// refused. Empty: no check.
	AllowedNetworks []string `json:"allowed_networks"`
	// RetentionDays: how long security events and sign-in records are
	// kept: at least 180 (CERT-In), 730 by default (CSCRF PR.AA.S9: two years).
	RetentionDays int `json:"retention_days"`
	// Sign-in rules (authlock.go): TimedUnlock lets a locked sign-in open
	// again by itself after 15 minutes instead of needing an out-of-band
	// unlock (CSCRF Annexure-G 5-7 asks for out-of-band); PasswordDays is
	// how long a password lasts (90 by default, NSE/INSP/73850 Annex D),
	// NoPasswordExpiry turns expiry off; PasswordHistory is how many past
	// passwords may not be reused (6 by default).
	TimedUnlock      bool `json:"timed_unlock,omitempty"`
	PasswordDays     int  `json:"password_days,omitempty"`
	NoPasswordExpiry bool `json:"no_password_expiry,omitempty"`
	PasswordHistory  int  `json:"password_history,omitempty"`
	// InvestigateHours is how soon an open incident must be taken on
	// (owner or Investigating), by severity (incfollow.go); empty: the
	// defaults.
	InvestigateHours map[string]int `json:"investigate_hours,omitempty"`
	UpdatedAt        string         `json:"updated_at,omitempty"`
	UpdatedBy        string         `json:"updated_by,omitempty"`
}

// MinRetentionDays is CERT-In's minimum for logs (direction (iv)).
const MinRetentionDays = 180

func (s SecuritySettings) withDefaults() SecuritySettings {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&s.FailedUserN, 5)
	def(&s.FailedUserMinutes, 15)
	def(&s.FailedIPN, 10)
	def(&s.FailedIPMinutes, 15)
	def(&s.LargeDownloadMB, 50)
	def(&s.RetentionDays, 730)
	if s.RetentionDays < MinRetentionDays {
		s.RetentionDays = MinRetentionDays
	}
	if s.Contacts == nil {
		s.Contacts = []string{}
	}
	if s.AllowedNetworks == nil {
		s.AllowedNetworks = []string{}
	}
	return s
}

// SecuritySettings returns the firm's settings with defaults filled in.
func (o *Ops) SecuritySettings(ctx context.Context, tenant string) SecuritySettings {
	s, _, _ := docs.GetOr(ctx, o.Docs, tenant, cSecSet, "main", SecuritySettings{})
	return s.withDefaults()
}

// SaveSecuritySettings checks and saves the settings. Compliance and Admin.
func (o *Ops) SaveSecuritySettings(ctx context.Context, tenant, actor string, s SecuritySettings) (SecuritySettings, error) {
	if err := o.require(ctx, tenant, actor, "Changing security alert and log settings", RoleCompliance); err != nil {
		return SecuritySettings{}, err
	}
	if s.RetentionDays != 0 && s.RetentionDays < MinRetentionDays {
		return SecuritySettings{}, invalid("security logs must be kept for at least %d days (CERT-In directions of 28 April 2022)", MinRetentionDays)
	}
	if s.RetentionDays > 3650 {
		return SecuritySettings{}, invalid("keep security logs for at most 3,650 days (10 years)")
	}
	rng := func(v int, lo, hi int, what string) error {
		if v != 0 && (v < lo || v > hi) {
			return invalid("%s must be between %d and %d", what, lo, hi)
		}
		return nil
	}
	for _, c := range []error{
		rng(s.FailedUserN, 2, 100, "wrong sign-ins for one user"), rng(s.FailedUserMinutes, 1, 1440, "the minutes for one user"),
		rng(s.FailedIPN, 2, 1000, "wrong sign-ins from one address"), rng(s.FailedIPMinutes, 1, 1440, "the minutes for one address"),
		rng(s.LargeDownloadMB, 1, 100000, "the large download size (MB)"),
		rng(s.PasswordDays, 30, 365, "the days a password lasts"), rng(s.PasswordHistory, 1, 12, "the past passwords not to reuse"),
	} {
		if c != nil {
			return SecuritySettings{}, c
		}
	}
	var cs []string
	for _, c := range s.Contacts {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		if !mail.ValidAddress(c) {
			return SecuritySettings{}, invalid("%q is not an e-mail address", c)
		}
		if !contains(cs, c) {
			cs = append(cs, c)
		}
	}
	if len(cs) > 20 {
		return SecuritySettings{}, invalid("give at most 20 contacts")
	}
	var ns []string
	for _, n := range s.AllowedNetworks {
		if n = strings.TrimSpace(n); n == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(n); err != nil && net.ParseIP(n) == nil {
			return SecuritySettings{}, invalid("%q is not an address or a range like 203.0.113.0/24", n)
		}
		ns = append(ns, n)
	}
	if len(ns) > 100 {
		return SecuritySettings{}, invalid("give at most 100 networks")
	}
	s.Contacts, s.AllowedNetworks = cs, ns
	s.UpdatedAt, s.UpdatedBy = o.now().Format(time.RFC3339), actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cSecSet, "main", s, actor); err != nil {
		return SecuritySettings{}, err
	}
	s = s.withDefaults()
	o.log(ctx, tenant, actor, "security.settings.saved", "security", map[string]any{"changed": []string{"alert and log settings"}, "contacts": len(s.Contacts), "retention_days": s.RetentionDays,
		"failed_user": fmt.Sprintf("%d in %d min", s.FailedUserN, s.FailedUserMinutes), "failed_ip": fmt.Sprintf("%d in %d min", s.FailedIPN, s.FailedIPMinutes), "large_download_mb": s.LargeDownloadMB, "networks": len(s.AllowedNetworks), "admin_alert": !s.NoAdminAlert,
		"timed_unlock": s.TimedUnlock, "password_days": s.passwordDays(), "password_history": s.passwordHistory()})
	return s, nil
}

// alertContacts are the addresses incident alerts go to.
func (o *Ops) alertContacts(ctx context.Context, tenant string) []string {
	s := o.SecuritySettings(ctx, tenant)
	if len(s.Contacts) > 0 {
		return s.Contacts
	}
	set, _ := o.Settings(ctx, tenant)
	var out []string
	for _, c := range append([]string{set.ComplianceEmail}, set.ManagementEmails...) {
		if c = strings.TrimSpace(c); c != "" && !contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// ---- detection rules ----

// SecRule is one detection rule as the screen explains it.
type SecRule struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	When     string `json:"when"`
	Severity string `json:"severity"`
	On       bool   `json:"on"`
}

// SecRules explains the rules with the firm's thresholds.
func SecRules(s SecuritySettings) []SecRule {
	s = s.withDefaults()
	nets := "Set the firm's own networks to switch this on."
	if len(s.AllowedNetworks) > 0 {
		nets = fmt.Sprintf("Anyone signs in from an address outside the %d network(s) set below.", len(s.AllowedNetworks))
	}
	return []SecRule{
		{"failed-user", "Wrong sign-ins for one user", fmt.Sprintf("%d wrong passwords or codes for one user within %d minutes.", s.FailedUserN, s.FailedUserMinutes), SevMedium, true},
		{"failed-ip", "Wrong sign-ins from one address", fmt.Sprintf("%d wrong sign-ins from one address within %d minutes, or the server refusing an address for too many attempts.", s.FailedIPN, s.FailedIPMinutes), SevMedium, true},
		{"seal", "Audit trail seal failure", "Any check of the audit trail's seals finds an entry changed, removed or inserted.", SevCritical, true},
		{"malware", "Malware in an upload", "The malware scanner finds malware in a file someone uploaded.", SevHigh, true},
		{"admin", "Admin role granted", "Someone is given the Admin role.", SevHigh, !s.NoAdminAlert},
		{"download", "Very large download", fmt.Sprintf("One download or export of %d MB or more.", s.LargeDownloadMB), SevMedium, true},
		{"outside-ip", "Sign-in from outside the firm's networks", nets, SevHigh, len(s.AllowedNetworks) > 0},
		{"support", "Support access without approval", "Ledgerline support staff open the firm's data without the firm's approval.", SevHigh, true},
		{"fraud-report", "Client reports a fraudulent transaction", "A client reports, from the portal or the app, a trade, payout, demat debit, pledge, sign-in or change they did not make.", SevHigh, true},
	}
}

// secDetect runs the rules on an event and opens or joins an incident.
func (o *Ops) secDetect(ctx context.Context, tenant string, e *SecEvent, now time.Time) string {
	var rule, key, title, sev string
	set := o.SecuritySettings(ctx, tenant)
	switch e.Kind {
	case "signin.failed":
		if e.Subject != "" {
			if n := o.secCount(ctx, tenant, "u|"+e.Subject, set.FailedUserMinutes, now); n >= set.FailedUserN {
				rule, key, sev = "failed-user", e.Subject, SevMedium
				title = fmt.Sprintf("%d wrong sign-ins for %s within %d minutes", n, e.Subject, set.FailedUserMinutes)
			}
		}
		if e.IP != "" {
			if n := o.secCount(ctx, tenant, "i|"+e.IP, set.FailedIPMinutes, now); n >= set.FailedIPN && rule == "" {
				rule, key, sev = "failed-ip", e.IP, SevMedium
				title = fmt.Sprintf("%d wrong sign-ins from %s within %d minutes", n, e.IP, set.FailedIPMinutes)
			}
		}
	case "signin.rate_limited":
		rule, key, sev, title = "failed-ip", e.IP, SevMedium, "Too many sign-in attempts from "+e.IP+": the server is refusing it"
	case "audit.seal_failed":
		rule, key, sev, title = "seal", "audit", SevCritical, "The audit trail's seals do not match: an entry was changed, removed or inserted"
	case "malware.found":
		sig, _ := e.Detail["signature"].(string)
		rule, key, sev, title = "malware", firstOf(sig, "unknown"), SevHigh, "Malware found in an upload: "+firstOf(sig, "unknown signature")
	case "account.admin_granted":
		if !set.NoAdminAlert {
			rule, key, sev, title = "admin", e.Subject, SevHigh, e.Subject+" was given the Admin role"
		}
	case "data.large_download":
		rule, key, sev, title = "download", e.Actor, SevMedium, "Very large download by "+firstOf(e.Actor, "someone")
	case "signin.outside_ip_rules":
		rule, key, sev, title = "outside-ip", e.Subject+"|"+e.IP, SevHigh, e.Subject+" signed in from "+e.IP+", outside the firm's networks"
	case "client.fraud_reported":
		rep, _ := e.Detail["report"].(string)
		rule, key, sev, title = "fraud-report", firstOf(rep, e.Subject), SevHigh, e.Message
	case "support.access_unapproved":
		rule, key, sev, title = "support", e.Actor, SevHigh, "Ledgerline support access without the firm's approval ("+firstOf(e.Actor, "support")+")"
	}
	if rule == "" {
		return ""
	}
	id, _ := o.raiseIncident(ctx, tenant, rule, key, title, sev, e, now)
	return id
}

// secCount adds now to a rule's window and returns how many times fall in
// the last minutes.
func (o *Ops) secCount(ctx context.Context, tenant, key string, minutes int, now time.Time) int {
	from := now.Add(-time.Duration(minutes) * time.Minute)
	w, err := upd(ctx, o.Docs, tenant, cSecWindow, key, SystemActor, &secWindow{}, func(w *secWindow) error {
		keep := w.Times[:0]
		for _, s := range w.Times {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil && t.After(from) {
				keep = append(keep, s)
			}
		}
		if len(keep) > 1000 {
			keep = keep[len(keep)-1000:]
		}
		w.Times = append(keep, now.Format(time.RFC3339Nano))
		return nil
	})
	if err != nil {
		return 0
	}
	return len(w.Times)
}

type secWindow struct {
	Times []string `json:"times"`
}

// SecEvents lists the firm's events between two IST dates (inclusive),
// newest first, optionally of one kind or log. Compliance and Admin.
func (o *Ops) SecEvents(ctx context.Context, tenant, actor, from, to, kind string, limit int) ([]SecEvent, error) {
	if err := o.require(ctx, tenant, actor, "Seeing security events and incidents", RoleCompliance); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 5000 {
		limit = 1000
	}
	xs, err := o.secEventsBetween(ctx, tenant, from, to)
	if err != nil {
		return nil, err
	}
	out := []SecEvent{}
	for i := len(xs) - 1; i >= 0 && len(out) < limit; i-- {
		if kind == "" || xs[i].Kind == kind || xs[i].Log == kind || strings.HasPrefix(xs[i].Kind, kind+".") {
			out = append(out, xs[i])
		}
	}
	return out, nil
}

// secEventsBetween returns the events from the start of IST date from to
// the end of IST date to, oldest first.
func (o *Ops) secEventsBetween(ctx context.Context, tenant, from, to string) ([]SecEvent, error) {
	a, err := time.ParseInLocation("2006-01-02", from, ist)
	if err != nil {
		return nil, invalid("give the from date as YYYY-MM-DD")
	}
	b, err := time.ParseInLocation("2006-01-02", to, ist)
	if err != nil {
		return nil, invalid("give the to date as YYYY-MM-DD")
	}
	if b.Before(a) {
		return nil, invalid("the to date is before the from date")
	}
	if b.Sub(a) > 400*24*time.Hour {
		return nil, invalid("choose at most a year at a time")
	}
	end := b.Add(24 * time.Hour)
	lo, hi := a.UTC().Format("2006-01-02T15:04:05.000000Z"), end.UTC().Format("2006-01-02T15:04:05.000000Z")
	var out []SecEvent
	for d := a.UTC().Truncate(24 * time.Hour); d.Before(end); d = d.Add(24 * time.Hour) {
		xs, err := docs.List[SecEvent](ctx, o.Docs, tenant, cSecEvent, d.Format("2006-01-02"))
		if err != nil {
			return nil, err
		}
		for _, x := range xs {
			if x.ID >= lo && x.ID < hi {
				out = append(out, x)
			}
		}
	}
	return out, nil
}

// ---- the daily work: retention and the nightly seal check ----

type secState struct {
	PurgedThrough string `json:"purged_through,omitempty"` // UTC day: events up to it are purged
	SealCheckedOn string `json:"seal_checked_on,omitempty"`
	LastPurge     string `json:"last_purge,omitempty"`
}

// securityTick sends logs to the firm's SIEM, purges events past the
// retention period once a day, and checks the audit trail's seals each
// night after 02:00 IST (an incident at once if one fails).
func (o *Ops) securityTick(ctx context.Context, tenant string, now time.Time) {
	o.logExportTick(ctx, tenant, now)
	o.incidentFollowTick(ctx, tenant, now)
	o.cyberResTick(ctx, tenant, now) // glitches, intraday backups, capacity (cyberres.go)
	now = now.In(ist)
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cSecState, "main", secState{})
	today := now.Format("2006-01-02")
	changed := false
	if st.LastPurge != today {
		o.erasureTick(ctx, tenant, now)
		o.resilienceTick(ctx, tenant, now)
		st.PurgedThrough = o.purgeSecEvents(ctx, tenant, st.PurgedThrough, now)
		st.LastPurge, changed = today, true
	}
	if va := o.vaultAudit(); va != nil && st.SealCheckedOn != today && now.Format("15:04") >= "02:00" {
		st.SealCheckedOn, changed = today, true
		_ = docs.Upsert(ctx, o.Docs, tenant, cSecState, "main", st, SystemActor) // before the check: one a night even if it is slow
		if r, err := va.VerifyAudit(ctx, tenant); err == nil {
			o.log(ctx, tenant, SystemActor, "security.audit.verified", "", map[string]any{"ok": r.OK, "sealed": r.Sealed, "problems": len(r.Problems), "last_hash": r.LastHash, "nightly": true})
		}
		return
	}
	if changed {
		_ = docs.Upsert(ctx, o.Docs, tenant, cSecState, "main", st, SystemActor)
	}
}

// purgeSecEvents clears the person, address and detail of events older
// than the retention period (the store keeps no deleted rows, so the kind
// and time stay). It returns the last UTC day done.
func (o *Ops) purgeSecEvents(ctx context.Context, tenant, through string, now time.Time) string {
	days := o.SecuritySettings(ctx, tenant).RetentionDays
	cutoff := now.UTC().Add(-time.Duration(days) * 24 * time.Hour).Truncate(24 * time.Hour) // events before this day go
	last := cutoff.Add(-24 * time.Hour).Format("2006-01-02")
	if through >= last {
		return through
	}
	purge := func(xs []SecEvent) {
		for _, x := range xs {
			if x.Purged || x.ID >= cutoff.Format("2006-01-02") {
				continue
			}
			_ = docs.Upsert(ctx, o.Docs, tenant, cSecEvent, x.ID, SecEvent{ID: x.ID, At: x.At, Log: x.Log, Kind: x.Kind, Severity: x.Severity, Message: SecKinds[x.Kind].Label, Incident: x.Incident, Purged: true}, SystemActor)
		}
	}
	if through == "" {
		xs, _ := docs.List[SecEvent](ctx, o.Docs, tenant, cSecEvent, "")
		purge(xs)
		return last
	}
	d, err := time.Parse("2006-01-02", through)
	if err != nil {
		return through
	}
	for d = d.Add(24 * time.Hour); !d.After(cutoff.Add(-24 * time.Hour)); d = d.Add(24 * time.Hour) {
		xs, _ := docs.List[SecEvent](ctx, o.Docs, tenant, cSecEvent, d.Format("2006-01-02"))
		purge(xs)
	}
	return last
}
