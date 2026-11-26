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
	"ledgerline/internal/backup"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
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
	// HR and payroll roles (hrms.go, payroll.go): the HR manager keeps
	// the employee master, joiners and leavers, attendance and leave; the
	// Payroll role runs payroll. Both only matter with the HRMS module on.
	RoleHR      = "HR manager"
	RolePayroll = "Payroll"
)

// Roles lists every role in display order.
var Roles = []string{RoleMaker, RoleChecker, RoleRisk, RoleCompliance, RoleAdmin, RoleBranch, RoleAP, RoleHR, RolePayroll}

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
	// MoreRoles are roles held besides Role; what the person may do is
	// the union of them all (roles.go).
	MoreRoles []string `json:"more_roles,omitempty"`
}

// customRoles reports roles given on the Roles screen: more than one, or
// one that is not built in.
func (u User) customRoles() bool {
	return len(u.MoreRoles) > 0 || (u.Role != RoleClient && !contains(Roles, u.Role))
}

// Scope is how far the person's client list reaches: RoleBranch, RoleAP,
// or "" for every client. Branch staff and authorised persons, and people
// given roles on the Roles screen limited to a branch or an AP (who keep
// a branch or a partner code only then).
func (u User) Scope() string { return u.scope() }

// scope is Scope, for this package.
func (u User) scope() string {
	own := u.customRoles()
	switch {
	case u.Role == RoleBranch || own && u.Branch != "":
		return RoleBranch
	case u.Role == RoleAP || own && u.Partner != "":
		return RoleAP
	}
	return ""
}

// Settings are per-tenant rules and calendar.
type Settings struct {
	Firm            string            `json:"firm"`
	SEBIReg         string            `json:"sebi_reg"`
	MemberCodes     map[string]string `json:"member_codes"`
	ComplianceEmail string            `json:"compliance_email"`
	// The firm's address and phone, and the compliance officer's name and
	// phone, printed on contract notes, statements and letters (SEBI
	// master circular for stock brokers: registered name, address,
	// phone and the compliance officer's name, phone and email on every
	// document to clients).
	FirmAddress       string `json:"firm_address,omitempty"`
	FirmPhone         string `json:"firm_phone,omitempty"`
	ComplianceOfficer string `json:"compliance_officer,omitempty"`
	CompliancePhone   string `json:"compliance_phone,omitempty"`
	// Escalation is the investor grievance escalation matrix (customer
	// care, head of customer care, compliance officer, CEO) that complaint
	// acknowledgements quote (NSE/ISC/73908, Part A 7.2).
	Escalation         []EscalationContact `json:"escalation,omitempty"`
	WarnUtilPct        int64               `json:"warn_util_pct"`
	RASBufferPct       int64               `json:"ras_buffer_pct"`
	PenaltyPassThrough bool                `json:"penalty_pass_through"`
	RASMonthly         string              `json:"ras_monthly"`
	RASQuarterly       string              `json:"ras_quarterly"`
	// CUSPADays: the firm's payment period for unpaid purchases pledged to
	// CUSPA, in trading days after pay-out: 1 to 5 (SEBI's maximum), 5
	// when not set (cuspa.go).
	CUSPADays int `json:"cuspa_days,omitempty"`
	// CUSPAMarginCount counts unpaid securities held in the CUSPA (at the
	// T-1 close less haircut) as margin collected in the margin reporting
	// to the clearing corporation. It is optional (SEBI revised MC para
	// 46.5; NCL-BSE and NSE circulars on CUSPA): no further exposure is
	// ever given on them, whatever this says.
	CUSPAMarginCount bool           `json:"cuspa_margin_count,omitempty"`
	Holidays         []string       `json:"holidays"` // typed in by the firm; exchange calendars add to these
	SnapshotTimes    []string       `json:"snapshot_times"`
	IndexNetLimitCr  int64          `json:"index_net_limit_cr"`
	InactiveDays     int64          `json:"inactive_days"`
	Calendar         []CalendarItem `json:"calendar"`
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
	// ClearingFor is the clearing corporation each exchange segment
	// ("BSE-CM" …) is cleared through: NCL, ICCL, MCCIL, MCXCCL, NCCL (see
	// ClearingSegments), CM:<code> for another clearing member (the firm is
	// only a trading member there, ClearingMemberNames has its name) or
	// NONE. Unset segments take the default (dailyfiles.go).
	ClearingFor         map[string]string `json:"clearing_for,omitempty"`
	ClearingMemberNames map[string]string `json:"clearing_member_names,omitempty"`
	// ClearingBMC is base minimum capital required by each clearing corporation (₹).
	ClearingBMC map[string]string `json:"clearing_bmc,omitempty"`

	// Interest (delayed payment charges) on overdue debit balances.
	InterestPctPA      string   `json:"interest_pct_pa"`      // annual %, default 18
	InterestGraceDays  int64    `json:"interest_grace_days"`  // free days at the start of a debit spell
	InterestMinimum    string   `json:"interest_minimum"`     // ₹; smaller amounts are not charged
	InterestExemptTags []string `json:"interest_exempt_tags"` // client groups not charged
	// InterestPosting: "monthly" (default: simple interest posted at month
	// end) or "daily" (posted every day from InterestDailyFrom, so it earns
	// interest itself; one invoice a month). See interestdaily.go.
	InterestPosting   string `json:"interest_posting,omitempty"`
	InterestDailyFrom string `json:"interest_daily_from,omitempty"`
	GSTPct            string `json:"gst_pct"` // default 18
	// DebitBlockDays: clients in debit this many days are blocked from buying.
	DebitBlockDays int64 `json:"debit_block_days"`

	// GST registration of the firm (place of supply decides IGST vs CGST+SGST).
	FirmGSTIN string `json:"firm_gstin"`
	// FirmPAN is the trading member's PAN for regulatory files (default:
	// from the GSTIN); ClearingMemberPAN the clearing member's when the
	// firm does not clear its own trades.
	FirmPAN           string `json:"firm_pan,omitempty"`
	ClearingMemberPAN string `json:"clearing_member_pan,omitempty"`
	FirmState         string `json:"firm_state"`

	// Partners and referrals.
	TDSCommissionPct string `json:"tds_commission_pct"` // section 194H, default 2
	ReferralPct      string `json:"referral_pct"`       // of referred clients' brokerage, default 10
	ReferralMonths   int64  `json:"referral_months"`    // default 12
	// ReferralAPOnlyFrom is the day from which a referral reward is paid
	// only to a registered authorised person (NSE inspection master
	// circular item 4.21; MCX/INSP/255/2026 para 9.9(a)): a client who
	// refers another is recorded as the source and earns nothing from it.
	// Empty: from DefaultReferralAPOnlyFrom.
	ReferralAPOnlyFrom string `json:"referral_ap_only_from,omitempty"`

	// Money in and out.
	ChequeBounceCharge string `json:"cheque_bounce_charge"` // ₹ before GST, default 350
	GatewaySecret      string `json:"gateway_secret,omitempty"`
	WhatsAppSecret     string `json:"whatsapp_secret,omitempty"`
	// ManagementEmails get scheduled-job results and alerts.
	ManagementEmails   []string `json:"management_emails"`
	PayoutDebitAccount string   `json:"payout_debit_account"` // client bank account payouts are made from
	// MaxClientBanks is how many active bank accounts a client may have
	// registered (0: the fewest bank slots in the UCC record of the
	// exchanges the firm trades on, 3 with MCX, otherwise 5; see
	// clientbanks.go).
	MaxClientBanks int64 `json:"max_client_banks,omitempty"`
	// BankPennyDrop: a new client bank account needs a penny drop (the
	// verification provider in Connections) as well as the bank proof a
	// checker sees; without the provider, or when it cannot be reached,
	// the account is not taken (clientbanks.go).
	BankPennyDrop bool `json:"bank_penny_drop,omitempty"`
	// BankAuthForms is how a client authorises a bank account change that
	// staff raise (clientauth.go): "both" (the default when empty),
	// "confirm" (the client confirms by a code or link sent to the
	// registered mobile and e-mail) or "letter" (the client's signed letter
	// or e-signed request). BankAuthDays is how many days such a request
	// waits for the client's confirmation before it expires (0: 7).
	BankAuthForms string `json:"bank_auth_forms,omitempty"`
	BankAuthDays  int64  `json:"bank_auth_days,omitempty"`
	// MigrationMaxGB is the largest migration package (.llmig) the firm may
	// upload, in GB (0: 100 GB). See migration.go.
	MigrationMaxGB int64 `json:"migration_max_gb,omitempty"`
	// HDFC Bank ENet payout file (hdfcenet.go): the firm's ENet client ID
	// and the file name template (default {hdfc_client_id}{date:0201}.{seq:3}),
	// the sequence (running across days, the default, or daily), the
	// instruction reference prefix (digits; a running number fills the 14),
	// the customer reference suffix (UCC-PAY), the RTGS threshold (₹,
	// default 200000), HDFC-to-HDFC transfers (I) unless turned off, a
	// forced type (N, R or I) and whether a client may appear twice.
	HDFCClientID      string `json:"hdfc_client_id,omitempty"`
	HDFCFileName      string `json:"hdfc_file_name,omitempty"`
	HDFCSeqMode       string `json:"hdfc_seq_mode,omitempty"`
	HDFCInstrPrefix   string `json:"hdfc_instr_prefix,omitempty"`
	HDFCCustRefSuffix string `json:"hdfc_cust_ref_suffix,omitempty"`
	RTGSThreshold     string `json:"rtgs_threshold,omitempty"`
	HDFCNoInternal    bool   `json:"hdfc_no_internal,omitempty"`
	HDFCForceType     string `json:"hdfc_force_type,omitempty"`
	HDFCRepeatClient  bool   `json:"hdfc_repeat_client,omitempty"`

	// Period locking: nothing may be posted on or before LockedUpTo, or
	// before today minus AutoLockDays business days when that is set.
	LockedUpTo   string `json:"locked_up_to"`
	AutoLockDays int64  `json:"auto_lock_days"`

	// LeadIncentive is paid to a lead's owner for each converted lead
	// activated as a client (₹, default 500).
	LeadIncentive string `json:"lead_incentive"`
	// LedgyAI lets Ledgy phrase answers with an AI model (configured on the
	// server). Only the question, with identifiers masked, and the help
	// articles are sent; never client data.
	LedgyAI bool `json:"ledgy_ai,omitempty"`
	// BillAI lets bill scanning send a bill the server cannot read (a scan
	// or photo) to the AI model to read its details. Off by default; the
	// bill leaves the server, so not allowed in India-only mode.
	BillAI bool `json:"bill_ai,omitempty"`
	// BoardAI lets people on a board summarise a card or have checklist
	// items suggested by the AI model (boardai.go): the card's text, with
	// identifiers masked, is sent. Off by default.
	BoardAI bool `json:"board_ai,omitempty"`
	// CircularAI lets the AI model read a circular's text (masked) and
	// propose obligations for the compliance officer to accept
	// (circai.go). Off by default; not allowed in India-only mode.
	CircularAI bool `json:"circular_ai,omitempty"`
	// Deployment is where this firm's Ledgerline runs, for the SaaS
	// disclosure (disclosures.go): "hosted" by Ledgerline (the default),
	// "self" on the broker's own server, or "own_cloud" in the broker's own
	// cloud account; with the hosting provider and region.
	Deployment      string `json:"deployment,omitempty"`
	HostingProvider string `json:"hosting_provider,omitempty"`
	HostingRegion   string `json:"hosting_region,omitempty"`
	// Client allocation to NSE Clearing through its API (nclalloc.go): the
	// key and secret are in Connections. AllocAPILive sends requests;
	// AllocAutoPush sends money that comes in during the day as it is
	// credited, to AllocPushSegment (CM by default).
	AllocAPILive     bool   `json:"alloc_api_live,omitempty"`
	AllocAutoPush    bool   `json:"alloc_auto_push,omitempty"`
	AllocPushSegment string `json:"alloc_push_segment,omitempty"`
	// AllocFileStart is the first number Ledgerline gives its N-MASS
	// allocation files on a day (the .Tnnnn of the name), 1 when not set:
	// higher when the office also uploads its own files that day
	// (allocfileseq.go).
	AllocFileStart int `json:"alloc_file_start,omitempty"`
	// Backups (backups.go): how many each tier keeps, whether the year
	// for yearly backups ends in March or December, and when a day that
	// was not closed is backed up anyway.
	BackupKeep     backup.Keep `json:"backup_keep"`
	BackupYearEnd  string      `json:"backup_year_end"`
	BackupFallback string      `json:"backup_fallback"`
	// BackupFallbackCommodity is when, the morning after, a commodity part
	// not yet closed is backed up anyway (MCX trades until 23:55).
	BackupFallbackCommodity string `json:"backup_fallback_commodity"`
	// Separate commodity ledger (segledger.go), for a firm that is only a
	// trading member on a commodity exchange: each client has an equity
	// ledger (NSE, BSE) and a commodity ledger (MCX), shown to the client
	// as one. Off: one ledger, as always. Money received is put in a ledger
	// by the first instruction that applies: the firm's receiving bank
	// account (BankAccountLedgers: account number -> EQ, COM or ASK), a
	// keyword in the narration (CommodityKeywords, EquityKeywords; the
	// defaults when empty), the client's own default (Client.FundsLedger);
	// otherwise it waits for Risk to choose, and after AllocateAfterMinutes
	// (0: never) goes to AllocateAfterTo (EQ or COM; empty: keeps waiting).
	// PayoutAutoTransfer lets a payout from one ledger take the shortfall
	// from the other ledger by a recorded transfer; otherwise such a payout
	// is refused until someone transfers.
	SeparateCommodityLedger bool              `json:"separate_commodity_ledger,omitempty"`
	BankAccountLedgers      map[string]string `json:"bank_account_ledgers,omitempty"`
	CommodityKeywords       []string          `json:"commodity_keywords,omitempty"`
	EquityKeywords          []string          `json:"equity_keywords,omitempty"`
	AllocateAfterMinutes    int64             `json:"allocate_after_minutes,omitempty"`
	AllocateAfterTo         string            `json:"allocate_after_to,omitempty"`
	PayoutAutoTransfer      bool              `json:"payout_auto_transfer,omitempty"`
	// MCXOnlyBankAccounts are the firm's bank accounts money paid into
	// counts for MCX only, with one ledger per client (mcxearmark.go): it
	// is earmarked as MCX money, never in an equity limit. MCX payouts are
	// made from the first. Not used with the separate commodity ledger
	// (map the account to Commodity in BankAccountLedgers there).
	MCXOnlyBankAccounts []string `json:"mcx_only_bank_accounts,omitempty"`
	// FirmDemats tags each of the firm's own demat accounts (BO ID) with
	// its account type in the exchanges' holding statement (POOL, CM COLL,
	// OWN, EPI, CLNT UNPAID…; NSE/INSP/55380). See firmdemats.go.
	FirmDemats map[string]string `json:"firm_demats,omitempty"`
	// FirmUCC is the member's own client code, as the holding statement
	// reports it on each of the firm's accounts.
	FirmUCC string `json:"firm_ucc,omitempty"`
	// SpecialSessions are days the exchanges trade on a Sunday (a Muhurat
	// or other special session, announced by circular): the holding
	// statement is due for them too (nseholdingapi.go).
	SpecialSessions []string `json:"special_sessions,omitempty"`
	// MCXMoneyApart keeps each client's MCX money as a balance of its own
	// inside the one ledger (mcxearmark.go) for a firm with one bank
	// account for all exchanges: no MCX-only account is needed. Money in
	// is equity money; RMS moves it to MCX money (maker-checker), MCX
	// bills add to or take from it, and MCX payouts go from the usual
	// payout account in the same batch as equity payouts. Off (the
	// default): MCX money is kept apart only with an MCX-only account.
	MCXMoneyApart bool `json:"mcx_money_apart,omitempty"`
	// RiskPolicyClauseSentOn is the date the risk management policy clause
	// allowing a hedge-break margin penalty to be passed to clients was
	// sent to them (NSE/INSP/64315). Empty (the default): no hedge-break
	// penalty is ever passed to a client; from this date + 7 days it may
	// be (penaltypass.go).
	RiskPolicyClauseSentOn string `json:"risk_policy_clause_sent_on,omitempty"`
	// KYCBlockOff switches the KYC trading gate off (kycgate.go). With the
	// gate on (the default), a client whose KYC the KRA has not verified
	// (status not Registered or Validated, including a status never
	// loaded) or who is on an exchange's KYC list is blocked on the
	// trading front ends: fresh orders only for a client onboarded by 31
	// March 2024 (exit allowed, SEBI's FAQ on the KRA validation
	// circular), everything for others and for a client the exchange has
	// made Not Permitted to Trade. The block lifts when the KRA status is
	// loaded as Registered or Validated and no list holds the client.
	// Accounts flagged Inactive, blocked or not active are blocked either
	// way. Switching it off needs the firm's own reason: SEBI does not let
	// an unverified client transact.
	KYCBlockOff bool `json:"kyc_block_off,omitempty"`
	// FinspotLogins is who creates a client's user ID on Finspot
	// (finspotlogin.go): "console" (staff, in Finspot's admin console),
	// "ledgerline" (Ledgerline, by API, or its upload file without the API)
	// or "both" (either: Ledgerline creates one when staff ask, and
	// recognises those made in the console). Empty: as the Finspot API
	// connection's client logins switch says (On: ledgerline, else console).
	FinspotLogins string `json:"finspot_logins,omitempty"`
	// From approved exchange holiday calendars (holidays.go), filled in
	// when settings are loaded; never saved with them.
	exTrading    map[string]string
	exSettlement map[string]string
	exMCX        map[string]string
	exMCXYears   map[string]bool
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

// CalendarItem is one compliance obligation as the calendar shows it.
// (Settings.Calendar, a fixed list of dates, is no longer used: the
// calendar comes from Periodic work and the duties.)
type CalendarItem struct {
	Date       string `json:"date"`
	Obligation string `json:"obligation"`
	Owner      string `json:"owner"`
	Status     string `json:"status,omitempty"`
	View       string `json:"view,omitempty"`
}

// DefaultSettings are used until a tenant saves its own.
func DefaultSettings() Settings {
	return Settings{
		// Penalties are the firm's by default: passing one to a client is
		// allowed only case by case (NSE/INSP/64315, see penaltypass.go).
		Firm: "Your firm", WarnUtilPct: 80, RASBufferPct: 0, PenaltyPassThrough: false,
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
	// KYC runs online account-opening checks; nil means simulated on a
	// development server and manual checking otherwise.
	KYC KYCProvider
	// LedgyAI, when set, phrases Ledgy's answers (see Settings.LedgyAI).
	LedgyAI LedgyWriter
	// Meter prices AI answers and takes them from each firm's credit.
	Meter *Meter
	// AIOff switches every AI call off on this server, for every firm
	// (LEDGERLINE_AI_OFF=1; see aicontrol.go). Firms have their own switch.
	AIOff bool
	// StepEvents, when set, hears every step reminder and escalation
	// (stepremind.go): the hook point for a push to the staff mobile app.
	StepEvents func(ctx context.Context, e StepEvent)
	// EditionOf is each firm's edition (manual, autopilot or full), set by
	// the provider; DefaultEdition applies to firms not listed.
	// Residency is where the server says it is hosted (see residency.go).
	Residency      Residency
	EditionOf      map[string]string
	DefaultEdition string
	edMu           sync.Mutex
	edCache        editionCache
	// boKeyed are the firms whose BO accounts have been re-keyed by BO ID
	// in this server run (demats.go).
	boKeyed sync.Map
	// acctIdx caches each firm's account search index (chart.go).
	acctIdx sync.Map
	// HolidaySources fetch exchange holiday lists (holidays.go); none means
	// lists are uploaded from the circulars only.
	HolidaySources []HolidaySource
	// Backups is where firm backups are written (backups.go); nil means
	// backups are off. Sealer encrypts them with the firms' data keys.
	Backups *backup.Dir
	// NCL talks to NSE Clearing's allocation API (nclalloc.go); nil uses
	// the real service.
	NCL NCLAllocAPI
	// Notis is NSE's trade API (NOTIS); nil uses the real one.
	Notis      NotisAPI
	NCLMargins NCLMarginsAPI // NSE Clearing margins API; nil for the real one
	NCMS       NCMSAPI       // NSE Clearing NCMS API; nil for the real one
	UPIX       UPIXAPI       // NSE Clearing UPIX API; nil for the real one
	// CBRICS sends NSE CBRICS's requests (cbrics.go); nil uses a plain
	// HTTPS client. cbricsKeys are the CBRICS sessions' login keys and
	// cbricsMu keeps one read of CBRICS running at a time.
	CBRICS     CBRICSDoer
	cbricsKeys sync.Map
	cbricsMu   sync.Mutex
	// Mailer sends email (contract notes, statements). Nil: development
	// servers simulate delivery; production servers hold the mail.
	Mailer Mailer
	// ControlKick, when set, asks the Control reporter to report now (a new
	// support request or reply).
	ControlKick chan struct{}
	Sealer      backup.Sealer
	// BackupSync runs the backup after a day's close before replying
	// (tests); otherwise it runs in the background.
	BackupSync bool
	bkMu       sync.Mutex
	bkRunning  map[string]bool
	// WorkDir is the server's working folder; each firm has its own folder
	// in it for uploads in progress (migration packages). Empty means a
	// folder in the system's temporary directory.
	WorkDir string
	// MigrateSync runs migration checks, loads and undos before replying
	// (tests); otherwise they run in the background.
	MigrateSync bool
	migMu       sync.Mutex
	migRun      map[string]bool
	// SignAgentWait is how long a document waits for the USB token signer
	// to sign it (signagent.go); zero means 60 seconds. SignAgentPollWait is
	// how long the signer's poll waits for work; zero means 25 seconds.
	// Tests make both short.
	SignAgentWait     time.Duration
	SignAgentPollWait time.Duration
	signWake          waker
	// FirmKeyClient calls firms' own keys (AWS KMS, a firm's key service;
	// firmkey.go); nil uses a plain HTTPS client. Tests point it at fakes.
	FirmKeyClient *http.Client
	// FCMClient and FCMBase send push notifications (mobilepush.go): nil
	// and "" use a plain HTTPS client and fcm.googleapis.com. Tests point
	// them at a fake. fcmTok keeps each firm's OAuth access token.
	FCMClient *http.Client
	FCMBase   string
	fcmTok    sync.Map
	// ExportSync builds full data exports before replying (tests);
	// otherwise they build in the background (dataexport.go).
	ExportSync bool
	dxMu       sync.Mutex
	dxRunning  map[string]bool
	// BackupCopiesDays is how long copies of backups held outside the
	// server (off-site copies, database snapshots) are kept, from the
	// operator (LEDGERLINE_BACKUP_COPIES_DAYS); the destruction certificate
	// states the date they age out. Zero: to be confirmed by the operator.
	BackupCopiesDays int
	closedMu         sync.Mutex
	closed           map[string]bool // firms purged after closing their account (dataexit.go)
	// ArchiveDir is archive storage (LEDGERLINE_ARCHIVE_DIR): a separate,
	// cheaper disk or object storage mounted as a folder, where archived
	// periods' packages go (archive.go). Empty: a folder in the work
	// folder, which the archive screen warns about. ArchiveSync archives
	// and restores before replying (tests).
	ArchiveDir  string
	ArchiveSync bool
	arcMu       sync.Mutex
	arcRunning  map[string]bool
}

// New builds Ops and installs the client-code-modification hook on the app.
func New(a *app.App) *Ops {
	o := &Ops{App: a, Docs: a.Docs, Now: time.Now}
	// Step reminders and escalations push "step overdue" to the staff app
	// (mobilepush.go). A server that adds another channel calls
	// o.PushStepEvent from its own hook too.
	o.StepEvents = o.PushStepEvent
	a.Adjust = o.applyCCM
	a.BillingHooks = o.billingHooks
	a.Observe = o.observeAudit // security events from the audit trail (secevents.go)
	a.Ledger.Guard = func(ctx context.Context, e ledger.Entry) error { return o.checkLock(ctx, e.Tenant, e.ValueDate) }
	// a stored file moved to an archive says so (archive.go)
	if a.Ingest != nil && a.Ingest.Raw != nil {
		a.Ingest.Raw = arcRaw{RawStore: a.Ingest.Raw, o: o}
	}
	o.installFirmKeys()
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
	if err := notSupport(actor, "Changing users"); err != nil {
		return err
	}
	if !validID.MatchString(u.ID) {
		return invalid("user id must be lower-case letters, digits, dots or dashes")
	}
	if strings.TrimSpace(u.Name) == "" {
		return invalid("user name is required")
	}
	prev, perr := o.user(ctx, tenant, u.ID)
	u.MoreRoles = nil // given only with a second Admin's approval (roles.go)
	ok := u.Role == RoleClient && len(u.Clients) > 0
	for _, r := range Roles {
		ok = ok || r == u.Role
	}
	switch {
	case perr == nil && prev.customRoles():
		// Someone whose roles were given on Users & roles → Roles keeps
		// them; only their name and whether they are active change here.
		if u.Role != prev.Role {
			return invalid("%s's roles are changed on Users & roles → Roles, with a second Admin's approval", u.ID)
		}
		u.MoreRoles, u.Branch, u.Partner = prev.MoreRoles, prev.Branch, prev.Partner
	case !ok:
		if d, _, err := docs.Get[RoleDef](ctx, o.Docs, tenant, cRole, u.Role); err == nil && !d.Retired {
			return invalid("%s is a role the firm made: give it on Users & roles → Roles, where a second Admin approves it", u.Role)
		}
		return invalid("unknown role %q", u.Role)
	case u.Role == RoleBranch:
		if u.Branch == "" {
			return invalid("branch staff need a branch")
		}
		u.Partner = ""
	case u.Role == RoleAP:
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
	// A new user, a changed role or a deactivation is a security event
	// (secevents.go); granting Admin can open an incident.
	switch {
	case perr != nil:
		o.log(ctx, tenant, actor, "user.role_changed", u.ID, map[string]any{"from": "", "to": u.Role, "new_user": true})
	case prev.Role != u.Role:
		o.log(ctx, tenant, actor, "user.role_changed", u.ID, map[string]any{"from": prev.Role, "to": u.Role})
	}
	if perr == nil && prev.Active && !u.Active {
		o.log(ctx, tenant, actor, "user.deactivated", u.ID, nil)
	}
	return nil
}

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,62}$`)

// RoleOf returns the user's role, or an error when users are configured and
// the actor is not one of them. With no users configured every actor is
// treated as Admin (single-user setups).
//
// A Ledgerline support session counts only while the firm's approval is
// open (supportaccess.go), and never as "every actor is Admin". Someone
// holding Admin among several roles is Admin. Someone holding a role the
// firm made or edited (roles.go) gets that role's name, or Branch staff or
// Authorised person when the role reaches only a branch's or an AP's
// clients, so the client filters and the client-level screens apply.
func (o *Ops) RoleOf(ctx context.Context, tenant, actor string) (string, error) {
	a, err := o.Access(ctx, tenant, actor)
	if err != nil {
		return "", err
	}
	if a.Admin {
		return RoleAdmin, nil
	}
	if len(a.Defs) > 0 {
		switch a.Limits().Scope {
		case ScopeBranch:
			return RoleBranch, nil
		case ScopeAP:
			return RoleAP, nil
		}
	}
	return a.Name, nil
}

// require checks the actor may do what: an unedited built-in role by name,
// as listed (Admin always passes); a role the firm edited or made by its
// screen permissions for the screen the request came from (roles.go).
func (o *Ops) require(ctx context.Context, tenant, actor, what string, roles ...string) error {
	a, err := o.Access(ctx, tenant, actor)
	if err != nil {
		return err
	}
	if a.Preview && actionKind(what, roles) != kindView {
		return fmt.Errorf("%w: this is a preview of the %s role; nothing can be changed", ErrForbidden, a.Name)
	}
	if a.Admin {
		return nil
	}
	for _, r := range a.Legacy {
		if contains(roles, r) {
			return nil
		}
	}
	if a.permits(what, roles, areasFrom(ctx)) {
		return nil
	}
	return fmt.Errorf("%w: %s needs %s (you are %s)", ErrForbidden, what, strings.Join(roles, " or "), a.Name)
}

// Settings returns the tenant settings.
func (o *Ops) Settings(ctx context.Context, tenant string) (Settings, error) {
	s, _, err := docs.GetOr(ctx, o.Docs, tenant, cSettings, "main", DefaultSettings())
	if err == nil {
		c := o.exchangeDays(ctx, tenant)
		s.exTrading, s.exSettlement, s.exMCX, s.exMCXYears = c.trading, c.settlement, c.mcx, c.mcxYears
	}
	return s, err
}

// SaveSettings validates and stores settings.
func (o *Ops) SaveSettings(ctx context.Context, tenant, actor string, s Settings) error {
	if err := o.require(ctx, tenant, actor, "Changing settings", RoleAdmin); err != nil {
		return err
	}
	if IsSupportActor(actor) {
		// Even with write approved, support does not weaken sign-in.
		if old, _ := o.Settings(ctx, tenant); s.TwoFactor != old.TwoFactor || s.SessionIdleMinutes != old.SessionIdleMinutes {
			return fmt.Errorf("%w: Ledgerline support cannot change two-factor sign-in or session time-out", ErrForbidden)
		}
	}
	return o.saveSettings(ctx, tenant, actor, s)
}

// saveSettings checks and stores settings for a caller that has already
// checked the actor: SaveSettings, and a firm practice applied when its
// second approver approves (firmpractices.go).
func (o *Ops) saveSettings(ctx context.Context, tenant, actor string, s Settings) error {
	for _, p := range []string{s.FirmPAN, s.ClearingMemberPAN} {
		if p != "" && !rePAN.MatchString(p) {
			return invalid("PAN %s must be 5 letters, 4 digits, 1 letter", p)
		}
	}
	if s.BillAI && o.Residency.IndiaOnly {
		return invalid("reading bills with AI sends them outside India, which India-only mode does not allow")
	}
	if s.CircularAI && o.Residency.IndiaOnly {
		return invalid("reading circulars with AI sends their text outside India, which India-only mode does not allow")
	}
	if err := checkDeployment(&s); err != nil {
		return err
	}
	s.AllocPushSegment = strings.ToUpper(strings.TrimSpace(s.AllocPushSegment))
	if s.AllocPushSegment != "" && s.AllocPushSegment != "CM" && s.AllocPushSegment != "FO" && s.AllocPushSegment != "CD" {
		return invalid("money coming in is allocated to CM, FO or CD")
	}
	if s.AllocFileStart < 0 || s.AllocFileStart > 9000 {
		return invalid("the first allocation file number of the day is 1 to 9000")
	}
	if s.AllocAPILive {
		if _, _, err := o.allocCodes(s); err != nil {
			return err
		}
		if _, ok := o.nclConn(ctx, tenant); !ok {
			return invalid("enter NSE Clearing's consumer key and secret in Settings → Connections (NSE Clearing allocation API) before switching sending on")
		}
	}
	fd, err := checkFirmDemats(s.FirmDemats)
	if err != nil {
		return err
	}
	s.FirmDemats = fd
	ss, err := checkSpecialSessions(s.SpecialSessions)
	if err != nil {
		return err
	}
	s.SpecialSessions = ss
	if s.WarnUtilPct < 1 || s.WarnUtilPct > 100 {
		return invalid("watch threshold must be between 1 and 100")
	}
	if s.RASBufferPct < 0 || s.RASBufferPct > 100 {
		return invalid("RAS buffer must be between 0 and 100")
	}
	if s.CUSPADays < 0 || s.CUSPADays > cuspaInvokeDays {
		return invalid("the payment period for unpaid securities (CUSPA) is 1 to %d trading days after pay-out", cuspaInvokeDays)
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
	switch s.InterestPosting {
	case "", InterestMonthly:
	case InterestDaily:
		if !ledger.ValidDate(s.InterestDailyFrom) {
			return invalid("give the date daily interest posting starts (YYYY-MM-DD); days before it are left to the monthly posting")
		}
	default:
		return invalid("interest posting must be monthly or daily")
	}
	for i, t := range s.InterestExemptTags {
		s.InterestExemptTags[i] = NormaliseTag(t)
	}
	for _, d := range append([]string{s.RASMonthly, s.RASQuarterly, s.LockedUpTo}, s.Holidays...) {
		if d != "" && !ledger.ValidDate(d) {
			return invalid("date %q must be YYYY-MM-DD", d)
		}
	}
	if s.RiskPolicyClauseSentOn = strings.TrimSpace(s.RiskPolicyClauseSentOn); s.RiskPolicyClauseSentOn != "" {
		if !ledger.ValidDate(s.RiskPolicyClauseSentOn) {
			return invalid("the date the risk policy penalty clause was sent to clients must be YYYY-MM-DD")
		}
		if s.RiskPolicyClauseSentOn > o.now().In(ist).Format("2006-01-02") {
			return invalid("the risk policy penalty clause cannot have been sent to clients after today; enter the date it was sent")
		}
	}
	if err := checkClearing(&s); err != nil {
		return err
	}
	if err := checkHDFCSettings(&s); err != nil {
		return err
	}
	if s.MigrationMaxGB < 0 || s.MigrationMaxGB > 2000 {
		return invalid("the largest migration package must be between 1 and 2,000 GB (0 for the default of 100 GB)")
	}
	if s.MaxClientBanks < 0 || s.MaxClientBanks > maxUCCBankSlots {
		return invalid("bank accounts per client must be 1 to %d (no exchange's UCC record holds more), or 0 for the default", maxUCCBankSlots)
	}
	if err := checkBankAuthSettings(&s); err != nil {
		return err
	}
	switch s.FinspotLogins = strings.ToLower(strings.TrimSpace(s.FinspotLogins)); s.FinspotLogins {
	case "", FinspotLoginsConsole, FinspotLoginsLedgerline, FinspotLoginsBoth:
	default:
		return invalid("Finspot client logins are made in the console, by Ledgerline, or both")
	}
	if err := o.checkSegLedger(ctx, tenant, &s); err != nil {
		return err
	}
	if err := o.checkMCXAccounts(ctx, tenant, &s); err != nil {
		return err
	}
	prev, _ := o.Settings(ctx, tenant)
	if err := docs.Upsert(ctx, o.Docs, tenant, cSettings, "main", s, actor); err != nil {
		return err
	}
	o.logClearing(ctx, tenant, actor, prev, s)
	if ch := securitySettingChanges(prev, s); len(ch) > 0 {
		o.log(ctx, tenant, actor, "settings.security_changed", "settings", map[string]any{"changed": ch}) // names only, never a secret
	}
	if prev.SeparateCommodityLedger != s.SeparateCommodityLedger {
		o.log(ctx, tenant, actor, "settings.separate_commodity_ledger", "settings", map[string]any{"on": s.SeparateCommodityLedger})
	}
	if strings.Join(prev.MCXOnlyBankAccounts, ",") != strings.Join(s.MCXOnlyBankAccounts, ",") {
		masked := make([]string, len(s.MCXOnlyBankAccounts))
		for i, a := range s.MCXOnlyBankAccounts {
			masked[i] = MaskAccount(a)
		}
		o.log(ctx, tenant, actor, "settings.mcx_only_bank_accounts", "settings", map[string]any{"accounts": masked})
	}
	if prev.MCXMoneyApart != s.MCXMoneyApart {
		o.log(ctx, tenant, actor, "settings.mcx_money_apart", "settings", map[string]any{"on": s.MCXMoneyApart})
	}
	if prev.RiskPolicyClauseSentOn != s.RiskPolicyClauseSentOn {
		o.log(ctx, tenant, actor, "settings.risk_policy_clause_sent_on", "settings", map[string]any{"from": prev.RiskPolicyClauseSentOn, "to": s.RiskPolicyClauseSentOn})
	}
	o.log(ctx, tenant, actor, "settings.saved", "settings", map[string]any{"warn_util_pct": s.WarnUtilPct, "ras_buffer_pct": s.RASBufferPct, "penalty_pass_through": s.PenaltyPassThrough})
	o.aiMarkState(ctx, tenant) // the AI features' on/off history (aicontrol.go)
	return nil
}

// NextBusinessDay returns the next weekday after date that is not a holiday.
func (s Settings) NextBusinessDay(date string) string {
	return s.stepDay(date, 1, false)
}

// NextSettlementDay is the pay-in day for trades of date: the next
// business day that is not also a settlement (clearing) holiday.
func (s Settings) NextSettlementDay(date string) string {
	return s.stepDay(date, 1, true)
}

// IsHoliday reports whether date is a trading holiday (exchange calendar
// or typed in Settings), and its name.
func (s Settings) IsHoliday(date string) (bool, string) {
	if n, ok := s.exTrading[date]; ok {
		return true, n
	}
	if contains(s.Holidays, date) {
		return true, "Holiday (Settings)"
	}
	return false, ""
}

// TradingHolidays lists every trading holiday known, sorted.
func (s Settings) TradingHolidays() []string {
	m := map[string]bool{}
	for _, h := range s.Holidays {
		m[h] = true
	}
	for d := range s.exTrading {
		m[d] = true
	}
	return sortedKeys(m)
}

func (s Settings) stepDay(date string, by int, settle bool) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return ""
	}
	for i := 0; i < 400; i++ {
		t = t.AddDate(0, 0, by)
		d := t.Format("2006-01-02")
		if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
			continue
		}
		if h, _ := s.IsHoliday(d); h {
			continue
		}
		if _, ok := s.exSettlement[d]; settle && ok {
			continue
		}
		return d
	}
	return ""
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
// gives the position before the day's obligations. It is the client as a
// whole: with a separate commodity ledger (segledger.go) the equity,
// commodity and awaiting-allocation ledgers together; equityFunds and
// clientLedgerFunds give one ledger. A firm with one ledger has only
// CLIENT:<ucc>, so the figures are the same either way.
func (o *Ops) clientFunds(ctx context.Context, tenant, asOf, excludeBillsOn string) (map[string]money.Amount, error) {
	lf, err := o.clientLedgerFunds(ctx, tenant, asOf, excludeBillsOn)
	if err != nil {
		return nil, err
	}
	out := make(map[string]money.Amount, len(lf))
	for u, f := range lf {
		out[u] = f.Combined()
	}
	return out, nil
}

// LedgerFunds is a client's funds in each ledger, credit positive.
type LedgerFunds struct {
	Equity    money.Amount `json:"equity"`
	Commodity money.Amount `json:"commodity"`
	Awaiting  money.Amount `json:"awaiting"`
	hasEquity bool         // an equity posting was seen (clientLedgerFunds)
}

// Combined is the client's money across the ledgers.
func (f LedgerFunds) Combined() money.Amount { return f.Equity + f.Commodity + f.Awaiting }

// Of is one ledger's funds.
func (f LedgerFunds) Of(kind string) money.Amount {
	switch kind {
	case ledger.LedgerCommodity:
		return f.Commodity
	case ledger.LedgerAwaiting:
		return f.Awaiting
	}
	return f.Equity
}

// clientLedgerFunds is clientFunds ledger by ledger.
func (o *Ops) clientLedgerFunds(ctx context.Context, tenant, asOf, excludeBillsOn string) (map[string]LedgerFunds, error) {
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{To: asOf})
	if err != nil {
		return nil, err
	}
	out := map[string]LedgerFunds{}
	for _, e := range es {
		if excludeBillsOn != "" && e.ValueDate == excludeBillsOn && (e.Source.Kind == "BILL" || e.Source.Kind == "REVERSAL") {
			continue
		}
		for _, p := range e.Postings {
			u, kind, ok := ledger.ClientCode(p.Account)
			if !ok {
				continue
			}
			f := out[u]
			switch kind {
			case ledger.LedgerCommodity:
				f.Commodity += p.Credit - p.Debit
			case ledger.LedgerAwaiting:
				f.Awaiting += p.Credit - p.Debit
			default:
				f.Equity += p.Credit - p.Debit
				f.hasEquity = true
			}
			out[u] = f
		}
	}
	return out, nil
}

// equityFunds is clientFunds for the equity ledger alone (CLIENT:<ucc>):
// what the equity front end, NSE Clearing and equity settlement work
// from. With one ledger it is the client's whole balance.
func (o *Ops) equityFunds(ctx context.Context, tenant, asOf, excludeBillsOn string) (map[string]money.Amount, error) {
	lf, err := o.clientLedgerFunds(ctx, tenant, asOf, excludeBillsOn)
	if err != nil {
		return nil, err
	}
	out := make(map[string]money.Amount, len(lf))
	for u, f := range lf {
		if f.hasEquity {
			out[u] = f.Equity
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
