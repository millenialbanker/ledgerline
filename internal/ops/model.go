// Package ops holds the broker operations built on the core: clients and
// onboarding, market data, holdings and pledges, RMS and margin, settlement,
// contract notes, bank reconciliation, running account settlement, reports,
// surveillance, IPO/MF, client code modification and EOD.
//
// Master and workflow data live in the document store; every amount is a
// ledger entry. Every change is written to the audit log.
package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// Collections.
const (
	cUser       = "user"
	cSettings   = "settings"
	cClient     = "client"
	cMarket     = "market"
	cHolding    = "holding"
	cPledge     = "pledge"
	cPosition   = "position"
	cOverride   = "override"
	cSquareOff  = "sqoff"
	cSettle     = "settle"
	cCUSPA      = "cuspa"
	cNote       = "cnote"
	cOutbox     = "outbox"
	cBankTxn    = "banktxn"
	cRASBatch   = "ras_batch"
	cReport     = "report"
	cAlert      = "alert"
	cIPO        = "ipo"
	cMF         = "mf"
	cCCM        = "ccm"
	cEOD        = "eod"
	cSeq        = "seq"
	cSnapshotPx = "snapshot"
	cLot        = "lot"
)

// Roles.
const (
	RoleMaker      = "Ops maker"
	RoleChecker    = "Ops checker"
	RoleRisk       = "Risk manager"
	RoleCompliance = "Compliance officer"
	RoleAdmin      = "Admin"
)

// Roles lists every role in display order.
var Roles = []string{RoleMaker, RoleChecker, RoleRisk, RoleCompliance, RoleAdmin, RoleBranch, RoleAP}

var (
	ErrInvalid   = errors.New("invalid")
	ErrForbidden = errors.New("not permitted for this role")
	ErrNotFound  = docs.ErrNotFound
	ErrState     = app.ErrState
)

func invalid(f string, a ...any) error {
	return fmt.Errorf("%w: "+f, append([]any{ErrInvalid}, a...)...)
}

// User is a person who can act in the back office.
type User struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Active bool   `json:"active"`
	// Branch staff see their branch's clients; authorised persons see
	// their own clients and their downline's.
	Branch  string `json:"branch,omitempty"`
	Partner string `json:"partner,omitempty"`
	// Clients are the accounts a client portal login covers (own first).
	Clients []string `json:"clients,omitempty"`
}

// Settings are per-tenant rules and calendar.
type Settings struct {
	Firm               string            `json:"firm"`
	SEBIReg            string            `json:"sebi_reg"`
	MemberCodes        map[string]string `json:"member_codes"`
	ComplianceEmail    string            `json:"compliance_email"`
	WarnUtilPct        int64             `json:"warn_util_pct"`
	RASBufferPct       int64             `json:"ras_buffer_pct"`
	PenaltyPassThrough bool              `json:"penalty_pass_through"`
	RASMonthly         string            `json:"ras_monthly"`
	RASQuarterly       string            `json:"ras_quarterly"`
	Holidays           []string          `json:"holidays"`
	SnapshotTimes      []string          `json:"snapshot_times"`
	IndexNetLimitCr    int64             `json:"index_net_limit_cr"`
	InactiveDays       int64             `json:"inactive_days"`
	Calendar           []CalendarItem    `json:"calendar"`
	// TwoFactor is "required" (the default when empty) or "optional".
	TwoFactor          string `json:"two_factor"`
	SessionIdleMinutes int64  `json:"session_idle_minutes"`
	// InactiveMonths is the no-activity period after which a trading
	// account is flagged Inactive (24 under the October 2024 circulars).
	InactiveMonths int64 `json:"inactive_months"`
	// InsightCoolOffDays: a client gets at most one non-urgent insight message in this many days (default 3).
	InsightCoolOffDays int64 `json:"insight_cool_off_days"`
	// WABotSessionHours: how long a code given on WhatsApp keeps account information open (default 24).
	WABotSessionHours int64 `json:"wa_bot_session_hours"`

	// Interest (delayed payment charges) on overdue debit balances.
	InterestPctPA      string   `json:"interest_pct_pa"`      // annual %, default 18
	InterestGraceDays  int64    `json:"interest_grace_days"`  // free days at the start of a debit spell
	InterestMinimum    string   `json:"interest_minimum"`     // ₹; smaller amounts are not charged
	InterestExemptTags []string `json:"interest_exempt_tags"` // client groups not charged
	GSTPct             string   `json:"gst_pct"`              // default 18
	// DebitBlockDays: clients in debit this many days are blocked from buying.
	DebitBlockDays int64 `json:"debit_block_days"`

	// GST registration of the firm (place of supply decides IGST vs CGST+SGST).
	FirmGSTIN string `json:"firm_gstin"`
	FirmState string `json:"firm_state"`

	// Partners and referrals.
	TDSCommissionPct string `json:"tds_commission_pct"` // section 194H, default 2
	ReferralPct      string `json:"referral_pct"`       // of referred clients' brokerage, default 10
	ReferralMonths   int64  `json:"referral_months"`    // default 12

	// Money in and out.
	ChequeBounceCharge string `json:"cheque_bounce_charge"` // ₹ before GST, default 350
	GatewaySecret      string `json:"gateway_secret,omitempty"`
	WhatsAppSecret     string `json:"whatsapp_secret,omitempty"`
	// ManagementEmails get scheduled-job results and alerts.
	ManagementEmails   []string `json:"management_emails"`
	PayoutDebitAccount string   `json:"payout_debit_account"` // client bank account payouts are made from

	// Period locking: nothing may be posted on or before LockedUpTo, or
	// before today minus AutoLockDays business days when that is set.
	LockedUpTo   string `json:"locked_up_to"`
	AutoLockDays int64  `json:"auto_lock_days"`

	// LeadIncentive is paid to a lead's owner for each converted lead
	// activated as a client (₹, default 500).
	LeadIncentive string `json:"lead_incentive"`
}

// lockedUpTo is the last locked date given today.
func (s Settings) lockedUpTo(today string) string {
	l := s.LockedUpTo
	if s.AutoLockDays > 0 {
		d := today
		for i := int64(0); i < s.AutoLockDays; i++ {
			d = s.PrevBusinessDay(d)
		}
		if d > l {
			l = d
		}
	}
	return l
}

// SecretMask stands in for a stored secret when settings are shown.
const SecretMask = "********"

// Require2FA reports whether every user must use two-factor sign-in.
func (s Settings) Require2FA() bool { return s.TwoFactor != "optional" }

// CalendarItem is one compliance obligation.
type CalendarItem struct {
	Date       string `json:"date"`
	Obligation string `json:"obligation"`
	Owner      string `json:"owner"`
}

// DefaultSettings are used until a tenant saves its own.
func DefaultSettings() Settings {
	return Settings{
		Firm: "Your firm", WarnUtilPct: 80, RASBufferPct: 0, PenaltyPassThrough: true,
		SnapshotTimes:   []string{"10:12:41", "11:47:03", "13:05:29", "14:58:16"},
		IndexNetLimitCr: 1500, InactiveDays: 30, MemberCodes: map[string]string{},
	}
}

// Ops is the operations service.
type Ops struct {
	App  *app.App
	Docs docs.Store
	Now  func() time.Time
	// SimulateOMS fills RMS square-off instructions immediately at the last
	// price, as a stand-in for a real OMS connection. Development only.
	SimulateOMS bool
	// DevCodes shows one-time codes in replies. Development only.
	DevCodes bool
}

// New builds Ops and installs the client-code-modification hook on the app.
func New(a *app.App) *Ops {
	o := &Ops{App: a, Docs: a.Docs, Now: time.Now}
	a.Adjust = o.applyCCM
	a.BillingHooks = o.billingHooks
	a.Ledger.Guard = func(ctx context.Context, e ledger.Entry) error { return o.checkLock(ctx, e.Tenant, e.ValueDate) }
	return o
}

func (o *Ops) now() time.Time { return o.Now().In(ist) }

var ist = time.FixedZone("IST", 19800)

func (o *Ops) log(ctx context.Context, tenant, actor, action, object string, detail map[string]any) {
	o.App.Log(ctx, tenant, actor, action, object, detail)
}

// ---- users and settings ----

// Users lists the tenant's users.
func (o *Ops) Users(ctx context.Context, tenant string) ([]User, error) {
	return docs.List[User](ctx, o.Docs, tenant, cUser, "")
}

// PutUser creates or replaces a user (admin task).
func (o *Ops) PutUser(ctx context.Context, tenant, actor string, u User) error {
	if !validID.MatchString(u.ID) {
		return invalid("user id must be lower-case letters, digits, dots or dashes")
	}
	if strings.TrimSpace(u.Name) == "" {
		return invalid("user name is required")
	}
	ok := u.Role == RoleClient && len(u.Clients) > 0
	for _, r := range Roles {
		ok = ok || r == u.Role
	}
	if !ok {
		return invalid("unknown role %q", u.Role)
	}
	switch u.Role {
	case RoleBranch:
		if u.Branch == "" {
			return invalid("branch staff need a branch")
		}
		u.Partner = ""
	case RoleAP:
		if u.Partner == "" {
			return invalid("an authorised person's login needs the partner code")
		}
		u.Branch = ""
	default:
		u.Branch, u.Partner = "", ""
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cUser, u.ID, u, actor); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "user.saved", u.ID, map[string]any{"role": u.Role, "active": u.Active})
	return nil
}

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}$`)

// RoleOf returns the user's role, or an error when users are configured and
// the actor is not one of them. With no users configured every actor is
// treated as Admin (single-user setups).
func (o *Ops) RoleOf(ctx context.Context, tenant, actor string) (string, error) {
	if actor == SystemActor {
		return RoleAdmin, nil
	}
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return "", err
	}
	if len(us) == 0 {
		return RoleAdmin, nil
	}
	for _, u := range us {
		if u.ID == actor {
			if !u.Active {
				return "", fmt.Errorf("%w: user %s is deactivated", ErrForbidden, actor)
			}
			return u.Role, nil
		}
	}
	return "", fmt.Errorf("%w: %q is not a user of this firm", ErrForbidden, actor)
}

// require checks the actor holds one of the roles (Admin always passes).
func (o *Ops) require(ctx context.Context, tenant, actor, what string, roles ...string) error {
	r, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return err
	}
	if r == RoleAdmin {
		return nil
	}
	for _, x := range roles {
		if r == x {
			return nil
		}
	}
	return fmt.Errorf("%w: %s needs %s (you are %s)", ErrForbidden, what, strings.Join(roles, " or "), r)
}

// Settings returns the tenant settings.
func (o *Ops) Settings(ctx context.Context, tenant string) (Settings, error) {
	s, _, err := docs.GetOr(ctx, o.Docs, tenant, cSettings, "main", DefaultSettings())
	return s, err
}

// SaveSettings validates and stores settings.
func (o *Ops) SaveSettings(ctx context.Context, tenant, actor string, s Settings) error {
	if err := o.require(ctx, tenant, actor, "Changing settings", RoleAdmin); err != nil {
		return err
	}
	if s.WarnUtilPct < 1 || s.WarnUtilPct > 100 {
		return invalid("watch threshold must be between 1 and 100")
	}
	if s.RASBufferPct < 0 || s.RASBufferPct > 100 {
		return invalid("RAS buffer must be between 0 and 100")
	}
	for _, x := range []struct{ name, v string }{{"interest rate", s.InterestPctPA}, {"GST rate", s.GSTPct}, {"interest minimum", s.InterestMinimum}, {"TDS rate", s.TDSCommissionPct}, {"referral reward", s.ReferralPct}, {"interest minimum", s.LeadIncentive}} {
		if x.v == "" {
			continue
		}
		d, err := money.ParseDec(x.v)
		if err != nil || d.Sign() < 0 || d.Cmp(money.DecInt(100)) > 0 && x.name != "interest minimum" {
			return invalid("%s %q must be a number between 0 and 100", x.name, x.v)
		}
	}
	if s.GatewaySecret == SecretMask {
		old, _ := o.Settings(ctx, tenant)
		s.GatewaySecret = old.GatewaySecret
	}
	if s.WhatsAppSecret == SecretMask {
		old, _ := o.Settings(ctx, tenant)
		s.WhatsAppSecret = old.WhatsAppSecret
	}
	for _, e := range s.ManagementEmails {
		if !reEmail.MatchString(e) {
			return invalid("management email %q is not an email address", e)
		}
	}
	if s.InterestGraceDays < 0 || s.DebitBlockDays < 0 || s.AutoLockDays < 0 || s.AutoLockDays > 30 {
		return invalid("day counts must be zero or more (auto lock at most 30)")
	}
	for i, t := range s.InterestExemptTags {
		s.InterestExemptTags[i] = NormaliseTag(t)
	}
	for _, d := range append([]string{s.RASMonthly, s.RASQuarterly, s.LockedUpTo}, s.Holidays...) {
		if d != "" && !ledger.ValidDate(d) {
			return invalid("date %q must be YYYY-MM-DD", d)
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSettings, "main", s, actor); err != nil {
		return err
	}
	o.log(ctx, tenant, actor, "settings.saved", "settings", map[string]any{"warn_util_pct": s.WarnUtilPct, "ras_buffer_pct": s.RASBufferPct, "penalty_pass_through": s.PenaltyPassThrough})
	return nil
}

// NextBusinessDay returns the next weekday after date that is not a holiday.
func (s Settings) NextBusinessDay(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	hol := map[string]bool{}
	for _, h := range s.Holidays {
		hol[h] = true
	}
	for {
		t = t.AddDate(0, 0, 1)
		d := t.Format("2006-01-02")
		if t.Weekday() != time.Saturday && t.Weekday() != time.Sunday && !hol[d] {
			return d
		}
	}
}

// ---- sequences ----

type seqDoc struct {
	N int64 `json:"n"`
}

func (o *Ops) next(ctx context.Context, tenant, name, actor string) (int64, error) {
	if _, _, err := docs.Get[seqDoc](ctx, o.Docs, tenant, cSeq, name); errors.Is(err, docs.ErrNotFound) {
		if _, err := docs.Put(ctx, o.Docs, tenant, cSeq, name, seqDoc{}, 0, actor); err != nil && !errors.Is(err, docs.ErrStale) {
			return 0, err
		}
	}
	v, err := docs.Update(ctx, o.Docs, tenant, cSeq, name, actor, func(s *seqDoc) error { s.N++; return nil })
	return v.N, err
}

// ---- small helpers ----

func dec(s string) money.Dec {
	d, err := money.ParseDec(s)
	if err != nil {
		return money.Dec{}
	}
	return d
}

func daysBetween(a, b string) int {
	ta, e1 := time.Parse("2006-01-02", a)
	tb, e2 := time.Parse("2006-01-02", b)
	if e1 != nil || e2 != nil {
		return 0
	}
	return int(tb.Sub(ta).Hours() / 24)
}

// MaskAccount shows only the last four digits of an account number.
func MaskAccount(a string) string {
	a = strings.TrimSpace(a)
	if len(a) <= 4 {
		return a
	}
	return strings.Repeat("X", len(a)-4) + a[len(a)-4:]
}

// MaskPAN hides the middle of a PAN.
func MaskPAN(p string) string {
	if len(p) != 10 {
		return p
	}
	return p[:5] + "XXXX" + p[9:]
}

func sortedKeys[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// clientFunds returns each client's funds with the broker as of a date:
// positive means the broker holds the client's money (a credit balance).
// With excludeBillsOn set, bills posted on that date are left out, which
// gives the position before the day's obligations.
func (o *Ops) clientFunds(ctx context.Context, tenant, asOf, excludeBillsOn string) (map[string]money.Amount, error) {
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{To: asOf})
	if err != nil {
		return nil, err
	}
	out := map[string]money.Amount{}
	for _, e := range es {
		if excludeBillsOn != "" && e.ValueDate == excludeBillsOn && (e.Source.Kind == "BILL" || e.Source.Kind == "REVERSAL") {
			continue
		}
		for _, p := range e.Postings {
			if u, ok := strings.CutPrefix(p.Account, "CLIENT:"); ok {
				out[u] += p.Credit - p.Debit
			}
		}
	}
	return out, nil
}

// exchangeTrades returns a date's exchange trades with CCMs applied.
func (o *Ops) exchangeTrades(ctx context.Context, tenant, date string) ([]ingest.TradeEvent, error) {
	return o.App.ExchangeTrades(ctx, tenant, date)
}

// Require checks the actor holds one of the roles (Admin always passes).
func (o *Ops) Require(ctx context.Context, tenant, actor, what string, roles ...string) error {
	return o.require(ctx, tenant, actor, what, roles...)
}
