package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/mail"
	"ledgerline/internal/money"
)

// The provider's records, all in one tenant of the store.
const (
	T = "control"

	cStaff    = "staff"
	cSession  = "session"
	cInstall  = "install"
	cFirm     = "firm"
	cUsage    = "usage"
	cCustomer = "customer"
	cInvoice  = "invoice"
	cTicket   = "ticket"
	cOutbox   = "outbox"
	cCommand  = "command"
	cSettings = "settings"
	cSeq      = "seq"
	cLog      = "log"
	cCredit   = "credit"   // credit notes
	cNotice   = "notice"   // notices sent to firms
	cNote     = "cnote"    // notes on a customer (calls, meetings)
	cCanned   = "canned"   // saved support replies
	cAlertAck = "alertack" // alerts someone has looked at
)

var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden")
	ErrInvalid   = errors.New("invalid")
)

func invalid(f string, a ...any) error { return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(f, a...)) }

// Staff roles.
const (
	RoleOwner   = "Owner"   // everything, including staff, prices and settings
	RoleBilling = "Billing" // customers, prices, invoices and AI credit
	RoleSupport = "Support" // support requests; sees customers and installations
)

var Roles = []string{RoleOwner, RoleBilling, RoleSupport}

// Staff is a person at the provider who signs in to Control.
type Staff struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Hash      string `json:"hash"`
	Active    bool   `json:"active"`
	MustReset bool   `json:"must_reset"`
	Created   string `json:"created"`
	LastLogin string `json:"last_login,omitempty"`
}

// Session is a signed-in browser.
type Session struct {
	ID       string `json:"id"` // sha256 of the cookie
	Staff    string `json:"staff"`
	CSRF     string `json:"csrf"`
	Created  string `json:"created"`
	LastSeen string `json:"last_seen"`
	Ended    bool   `json:"ended,omitempty"`
}

// Installation is one Ledgerline server that reports in: the hosted
// server, or a broker's own.
type Installation struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Kind       string      `json:"kind"` // hosted, own
	KeyHash    string      `json:"key_hash"`
	KeyHint    string      `json:"key_hint"`
	Created    string      `json:"created"`
	LastReport string      `json:"last_report,omitempty"`
	LastFrom   string      `json:"last_from,omitempty"`
	Info       InstallInfo `json:"info"`
	Firms      []string    `json:"firms"`
	Revoked    bool        `json:"revoked,omitempty"`
	Reports    int         `json:"reports"`
}

// Firm is a broker firm (a tenant) on an installation.
type Firm struct {
	ID           string     `json:"id"` // installation~tenant
	Installation string     `json:"installation"`
	Tenant       string     `json:"tenant"`
	Customer     string     `json:"customer"`
	FirstSeen    string     `json:"first_seen"`
	LastReport   string     `json:"last_report"`
	Latest       FirmReport `json:"latest"`
}

// UsageMonth is a firm's use in one month: the last report of the month
// (its counts are month to date) and the month's peaks.
type UsageMonth struct {
	Firm      string     `json:"firm"`
	Month     string     `json:"month"`
	Report    FirmReport `json:"report"`
	PeakStaff int        `json:"peak_staff"`
	PeakBots  int        `json:"peak_bots"`
	Edition   string     `json:"edition"` // the highest edition used in the month
}

// Customer is who is billed: a broker, with one or more firms.
type Customer struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	LegalName   string       `json:"legal_name"`
	GSTIN       string       `json:"gstin"`
	State       string       `json:"state"` // GST state code, e.g. 27
	Address     string       `json:"address"`
	Contact     string       `json:"contact"`
	Email       string       `json:"email"`
	Phone       string       `json:"phone"`
	Status      string       `json:"status"` // Trial, Active, Suspended, Left
	SetupFee    money.Amount `json:"setup_fee"`
	SetupBilled string       `json:"setup_billed,omitempty"` // invoice that billed it
	DiscountPct string       `json:"discount_pct"`
	BillFrom    string       `json:"bill_from"` // YYYY-MM billing starts
	Notes       string       `json:"notes"`
	Created     string       `json:"created"`

	// Who gets invoices and reminders (Email if empty), and copies.
	BillingEmail string `json:"billing_email,omitempty"`
	CCEmails     string `json:"cc_emails,omitempty"` // comma separated
	TechContact  string `json:"tech_contact,omitempty"`
	TechEmail    string `json:"tech_email,omitempty"`
	// The contract: dates, and prices agreed with this customer that
	// differ from the price book.
	TrialEnds     string                  `json:"trial_ends,omitempty"`     // YYYY-MM-DD
	ContractStart string                  `json:"contract_start,omitempty"` // YYYY-MM-DD
	ContractEnd   string                  `json:"contract_end,omitempty"`   // YYYY-MM-DD, renewal due
	EditionFee    map[string]money.Amount `json:"edition_fee,omitempty"`    // overrides the price book per edition
	IncludedUsers int                     `json:"included_users,omitempty"` // overrides the price book (0 = price book)
	MinMonthly    money.Amount            `json:"min_monthly,omitempty"`    // minimum monthly bill before GST (AI credit not counted)
	TDSPct        string                  `json:"tds_pct,omitempty"`        // TDS the customer deducts, e.g. 10 (194J) or 2
}

var CustomerStatuses = []string{"Trial", "Active", "Suspended", "Left"}

// Slab is one band of the per-client fee: clients who traded in the month,
// up to UpTo (0 = all the rest), at Rate each. Bands are graduated.
type Slab struct {
	UpTo int          `json:"up_to"`
	Rate money.Amount `json:"rate"`
}

// PriceBook is what the provider charges.
type PriceBook struct {
	Edition       map[string]money.Amount `json:"edition"` // monthly fee per firm: manual, autopilot, full
	Slabs         []Slab                  `json:"slabs"`
	IncludedUsers int                     `json:"included_users"` // staff logins included per firm
	ExtraUser     money.Amount            `json:"extra_user"`
	BotComputer   money.Amount            `json:"bot_computer"`
	KYC           money.Amount            `json:"kyc"`       // per online account opening verified
	SetupFee      money.Amount            `json:"setup_fee"` // default for new customers
	GSTPct        string                  `json:"gst_pct"`
	SAC           string                  `json:"sac"`
}

// Vendor is the provider, as printed on invoices.
type Vendor struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	GSTIN    string `json:"gstin"`
	State    string `json:"state"`
	PAN      string `json:"pan"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
	Bank     string `json:"bank"`
	Account  string `json:"account"`
	IFSC     string `json:"ifsc"`
	UPI      string `json:"upi"`
	Prefix   string `json:"prefix"`   // invoice number prefix
	DueDays  int    `json:"due_days"` // payment terms
	Footer   string `json:"footer"`
	SupportH string `json:"support_hours"`
}

// Settings holds the vendor and the price book.
type Settings struct {
	Vendor Vendor    `json:"vendor"`
	Prices PriceBook `json:"prices"`
	Ops    Running   `json:"ops"`
}

// Running is how Control runs day to day.
type Running struct {
	LatestVersion string `json:"latest_version"` // installations below it are flagged
	AutoRemind    bool   `json:"auto_remind"`    // email payment reminders by themselves
	RemindEvery   int    `json:"remind_every"`   // days between reminders (default 7)
	NoticeOverdue bool   `json:"notice_overdue"` // with reminders, also show a notice to the firm's Admins
	SuspendAfter  int    `json:"suspend_after"`  // days overdue before an account is flagged for suspension (0 = never)
}

func defaultSettings() Settings {
	return Settings{
		Vendor: Vendor{Name: "Ledgerline", Prefix: "LL", DueDays: 15, SupportH: "9:00–19:00 IST, trading days"},
		Prices: PriceBook{
			Edition:       map[string]money.Amount{"manual": money.MustAmount("15000"), "autopilot": money.MustAmount("35000"), "full": money.MustAmount("60000")},
			Slabs:         []Slab{{UpTo: 500, Rate: 0}, {UpTo: 5000, Rate: money.MustAmount("10")}, {UpTo: 25000, Rate: money.MustAmount("6")}, {UpTo: 0, Rate: money.MustAmount("3")}},
			IncludedUsers: 10, ExtraUser: money.MustAmount("750"), BotComputer: money.MustAmount("2000"), KYC: money.MustAmount("25"),
			SetupFee: money.MustAmount("50000"), GSTPct: "18", SAC: "997331",
		},
	}
}

// InvoiceLine is one line of an invoice.
type InvoiceLine struct {
	Desc   string       `json:"desc"`
	SAC    string       `json:"sac"`
	Qty    int          `json:"qty"`
	Rate   money.Amount `json:"rate"`
	Amount money.Amount `json:"amount"`
	Kind   string       `json:"kind"` // edition, clients, users, bots, kyc, ai, setup, other
	Firm   string       `json:"firm,omitempty"`
}

// Payment is money received against an invoice. TDS is tax the customer
// deducted and paid to the government on our behalf (it settles the
// invoice like money; claim it against the TDS certificate, Form 16A).
type Payment struct {
	Date    string       `json:"date"`
	Amount  money.Amount `json:"amount"`
	TDS     money.Amount `json:"tds,omitempty"`
	TDSCert string       `json:"tds_cert,omitempty"` // Form 16A number or note once received
	Mode    string       `json:"mode"`
	Ref     string       `json:"ref"`
	By      string       `json:"by"`
}

// Reminder is a payment reminder sent for an invoice.
type Reminder struct {
	At     string `json:"at"`
	By     string `json:"by"`
	To     string `json:"to"`
	Via    string `json:"via"`  // email, notice, email+notice
	Days   int    `json:"days"` // days past due when sent
	Result string `json:"result,omitempty"`
}

// Invoice is a tax invoice to a customer.
type Invoice struct {
	ID        string        `json:"id"`
	Number    string        `json:"number,omitempty"` // given when issued, from the financial year's series
	Customer  string        `json:"customer"`
	Period    string        `json:"period"` // YYYY-MM
	Date      string        `json:"date,omitempty"`
	Due       string        `json:"due,omitempty"`
	Lines     []InvoiceLine `json:"lines"`
	Subtotal  money.Amount  `json:"subtotal"`
	Discount  money.Amount  `json:"discount"`
	Taxable   money.Amount  `json:"taxable"`
	CGST      money.Amount  `json:"cgst"`
	SGST      money.Amount  `json:"sgst"`
	IGST      money.Amount  `json:"igst"`
	Total     money.Amount  `json:"total"`
	Paid      money.Amount  `json:"paid"`     // money and TDS received
	Credited  money.Amount  `json:"credited"` // credit notes against it
	Balance   money.Amount  `json:"balance"`  // still due
	Status    string        `json:"status"`   // Draft, Issued, Part-paid, Paid, Void
	Payments  []Payment     `json:"payments"`
	Bill      Customer      `json:"bill"`   // the customer as at issue
	Vendor    Vendor        `json:"vendor"` // the provider as at issue
	GSTPct    string        `json:"gst_pct"`
	Notes     string        `json:"notes"`
	Created   string        `json:"created"`
	CreatedBy string        `json:"created_by"`
	Commands  []string      `json:"commands,omitempty"` // AI credit top-ups billed
	Emailed   string        `json:"emailed,omitempty"`  // last emailed to the customer
	EmailedTo string        `json:"emailed_to,omitempty"`
	Reminders []Reminder    `json:"reminders,omitempty"`
}

// settle works out what is still due and the status.
func (iv *Invoice) settle() {
	if iv.Status == "Draft" || iv.Status == "Void" {
		iv.Balance = 0
		return
	}
	iv.Balance = iv.Total - iv.Paid - iv.Credited
	switch {
	case iv.Balance <= 0:
		iv.Balance, iv.Status = 0, "Paid"
	case iv.Paid > 0 || iv.Credited > 0:
		iv.Status = "Part-paid"
	default:
		iv.Status = "Issued"
	}
}

// Ticket statuses at the provider.
var TicketStatuses = []string{"New", "Open", "Waiting on customer", "Resolved", "Closed"}

// TicketMsg is one message on a ticket.
type TicketMsg struct {
	ID   string `json:"id"`
	From string `json:"from"` // customer, support, note (internal), status
	Who  string `json:"who"`
	Text string `json:"text"`
	At   string `json:"at"`
}

// Ticket is a support request from a firm.
type Ticket struct {
	ID            string      `json:"id"`
	Installation  string      `json:"installation"`
	Tenant        string      `json:"tenant"`
	Firm          string      `json:"firm"`
	FirmName      string      `json:"firm_name"`
	Customer      string      `json:"customer"`
	LocalID       string      `json:"local_id"`
	Subject       string      `json:"subject"`
	Category      string      `json:"category"`
	Priority      string      `json:"priority"`
	Screen        string      `json:"screen"`
	Version       string      `json:"version"`
	User          string      `json:"user"`
	UserName      string      `json:"user_name"`
	Role          string      `json:"role"`
	Status        string      `json:"status"`
	Assignee      string      `json:"assignee"`
	Messages      []TicketMsg `json:"messages"`
	Created       string      `json:"created"`
	Updated       string      `json:"updated"`
	FirstResponse string      `json:"first_response,omitempty"`
	ResolvedAt    string      `json:"resolved_at,omitempty"`
	RespondBy     string      `json:"respond_by"`
}

// SLA is the first-response target by priority.
var SLA = map[string]time.Duration{"Urgent": time.Hour, "High": 4 * time.Hour, "Normal": 24 * time.Hour, "Low": 72 * time.Hour}

// Outbox is a ticket update waiting for its installation's next report.
type Outbox struct {
	ID           string       `json:"id"`
	Installation string       `json:"installation"`
	Update       TicketUpdate `json:"update"`
	Created      string       `json:"created"`
	Delivered    string       `json:"delivered,omitempty"`
}

// CommandRec is a command sent to an installation.
type CommandRec struct {
	Command
	Installation string `json:"installation"`
	Firm         string `json:"firm"`
	Customer     string `json:"customer"`
	Status       string `json:"status"` // Queued, Done, Failed
	Error        string `json:"error,omitempty"`
	Created      string `json:"created"`
	By           string `json:"by"`
	Sent         string `json:"sent,omitempty"` // first handed to the server
	Done         string `json:"done,omitempty"`
	Invoice      string `json:"invoice,omitempty"`
	Bill         bool   `json:"bill"` // invoice the top-up to the customer
}

// LogEntry is one action in Control.
type LogEntry struct {
	At     string `json:"at"`
	Staff  string `json:"staff"`
	Action string `json:"action"`
	Target string `json:"target"`
	Note   string `json:"note,omitempty"`
}

// Control is the provider's console.
type Control struct {
	Docs docs.Store
	Now  func() time.Time
	// OnlineWithin is how recent a report must be to count as online.
	OnlineWithin time.Duration
	// Mailer sends invoices, statements and reminders (nil: not set up).
	Mailer mail.Sender
	seqMu  sync.Mutex
}

// New makes a Control over a store.
func New(store docs.Store) *Control {
	return &Control{Docs: store, Now: time.Now, OnlineWithin: 35 * time.Minute}
}

var IST = time.FixedZone("IST", 5*3600+1800)

func (c *Control) now() time.Time { return c.Now().In(IST) }
func (c *Control) stamp() string  { return c.now().Format(time.RFC3339) }
func (c *Control) today() string  { return c.now().Format("2006-01-02") }
func (c *Control) month() string  { return c.now().Format("2006-01") }

type seqDoc struct{ N int }

func (c *Control) next(ctx context.Context, name string) (int, error) {
	c.seqMu.Lock()
	defer c.seqMu.Unlock()
	s, err := docs.Update(ctx, c.Docs, T, cSeq, name, "system", func(s *seqDoc) error { s.N++; return nil })
	if errors.Is(err, docs.ErrNotFound) {
		s = seqDoc{N: 1}
		err = docs.Upsert(ctx, c.Docs, T, cSeq, name, s, "system")
	}
	return s.N, err
}

func (c *Control) log(ctx context.Context, staff, action, target, note string) {
	id := c.now().Format("20060102T150405.000000") + "-" + randHex(3)
	_ = docs.Upsert(ctx, c.Docs, T, cLog, id, LogEntry{At: c.stamp(), Staff: staff, Action: action, Target: target, Note: note}, staff)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func firstOf(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func clean(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// Settings returns the vendor and price book.
func (c *Control) Settings(ctx context.Context) (Settings, error) {
	s, _, err := docs.GetOr(ctx, c.Docs, T, cSettings, "settings", defaultSettings())
	return s, err
}

// SaveSettings changes the vendor details and the price book.
func (c *Control) SaveSettings(ctx context.Context, by string, s Settings) (Settings, error) {
	if err := c.need(ctx, by, RoleOwner); err != nil {
		return s, err
	}
	for _, e := range []string{"manual", "autopilot", "full"} {
		if s.Prices.Edition[e] < 0 {
			return s, invalid("the %s fee can't be negative", e)
		}
	}
	if _, err := money.ParseDec(s.Prices.GSTPct); err != nil {
		return s, invalid("GST rate must be a number, e.g. 18")
	}
	for i, sl := range s.Prices.Slabs {
		if sl.Rate < 0 || (sl.UpTo == 0 && i != len(s.Prices.Slabs)-1) || (i > 0 && sl.UpTo != 0 && sl.UpTo <= s.Prices.Slabs[i-1].UpTo) {
			return s, invalid("client slabs must rise, with only the last one open-ended")
		}
	}
	if s.Ops.RemindEvery < 0 || s.Ops.RemindEvery > 60 || s.Ops.SuspendAfter < 0 || s.Ops.SuspendAfter > 365 {
		return s, invalid("reminders every 1 to 60 days; suspension after 0 to 365 days")
	}
	s.Ops.LatestVersion = clean(s.Ops.LatestVersion, 30)
	if s.Vendor.DueDays < 0 || s.Vendor.DueDays > 120 {
		return s, invalid("payment terms must be 0 to 120 days")
	}
	s.Vendor.GSTIN = strings.ToUpper(strings.TrimSpace(s.Vendor.GSTIN))
	if s.Vendor.GSTIN != "" && !validGSTIN(s.Vendor.GSTIN) {
		return s, invalid("the GSTIN doesn't look right (15 characters, e.g. 27ABCDE1234F1Z5)")
	}
	if s.Vendor.GSTIN != "" {
		s.Vendor.State = s.Vendor.GSTIN[:2]
	}
	if err := docs.Upsert(ctx, c.Docs, T, cSettings, "settings", s, by); err != nil {
		return s, err
	}
	c.log(ctx, by, "settings.save", "settings", "")
	return s, nil
}

func validGSTIN(g string) bool {
	if len(g) != 15 {
		return false
	}
	for i, r := range g {
		digit, upper := r >= '0' && r <= '9', r >= 'A' && r <= 'Z'
		switch {
		case i < 2 && !digit, i >= 2 && i < 7 && !upper, i >= 7 && i < 11 && !digit, i == 11 && !upper, !digit && !upper:
			return false
		}
	}
	return true
}
