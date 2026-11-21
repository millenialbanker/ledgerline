package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- HRMS: the firm's own people ----
//
// The employee master for the broker's own staff: head office, branches
// and, when the firm wants, the staff of its authorised persons (Partner
// set). Like client data, every record is encrypted at rest by the vault
// (a separate key per firm); identifiers (PAN, Aadhaar's last four, UAN,
// ESIC number, salary bank account) are masked on screen and seeing one in
// full is recorded with the reason (RevealEmployee). Aadhaar is never
// stored beyond its last four digits (Aadhaar Act 2016 s.29 and the UIDAI's
// rules on storing Aadhaar numbers).
//
// Who sees whom:
//   - Admin and HR managers: everyone.
//   - Payroll and the compliance officer: everyone (payroll for pay, the
//     compliance officer for certificates and the own-trading register).
//   - A manager (any role) whose Ledgerline login is linked to an
//     employee: their direct and indirect reports, without identifiers or
//     pay.
//   - Branch staff who manage people: only the people of their branch
//     (and the offices under it), never another branch's.
//   - Everyone: their own record (self-service, hrself.go).
//
// Everything here needs the HRMS module (modules.go); with it off, every
// call answers ErrModuleOff.

const (
	cEmployee  = "hr_employee"
	cHRPolicy  = "hr_policy"
	cStaffFile = "hr_file"
	wHR        = "hr" // work queue process

	EmpJoining = "Joining"
	EmpActive  = "Active"
	EmpLeaving = "Leaving"
	EmpLeft    = "Left"
)

// EmploymentTypes are the kinds of employment.
var EmploymentTypes = []string{"Permanent", "Probation", "Contract", "Intern", "Consultant", "AP staff"}

// StaffDocKinds are the kinds of staff documents kept.
var StaffDocKinds = []string{"Offer letter", "Appointment letter", "ID proof", "Address proof", "PAN card", "Education", "Experience", "Background check", "Code of conduct", "NISM certificate", "Other certificate", "Relieving letter", "Other"}

// Duties are what a person does that decides the certificates they must
// hold (CertRule) and the joining checklist. Firms add their own.
var Duties = []string{"Dealer (equity cash)", "Dealer (equity derivatives)", "Dealer (currency derivatives)", "Dealer (commodity derivatives)", "Dealer (interest rate derivatives)", "DP operations", "Compliance officer", "Research analyst", "Investment adviser", "Mutual fund distribution", "AP staff (derivatives)", "Back office", "Sales"}

// isDealerDuty: the person deals on exchange terminals.
func isDealerDuty(d string) bool {
	return strings.HasPrefix(d, "Dealer") || d == "AP staff (derivatives)"
}

// Contact is a phone and email; EmergencyContact a person to call.
type EmergencyContact struct {
	Name     string `json:"name"`
	Relation string `json:"relation"`
	Phone    string `json:"phone"`
}

// SalaryBank is the account salary is paid into.
type SalaryBank struct {
	Holder  string `json:"holder"`
	Account string `json:"account"`
	IFSC    string `json:"ifsc"`
	Bank    string `json:"bank"`
}

// StaffDoc is a document on an employee's file, with its expiry if any.
type StaffDoc struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SHA256      string `json:"sha256"`
	FileID      string `json:"file_id"`
	Expiry      string `json:"expiry,omitempty"`
	By          string `json:"by"`
	At          string `json:"at"`
}

// StaffCert is a certificate a person holds (NISM and others).
type StaffCert struct {
	ID        string `json:"id"`
	Cert      string `json:"cert"` // e.g. NISM Series VIII
	Number    string `json:"number"`
	IssuedOn  string `json:"issued_on,omitempty"`
	ValidTill string `json:"valid_till"`
	Verified  bool   `json:"verified"` // checked by HR or compliance (an employee's own entry starts unverified)
	By        string `json:"by,omitempty"`
	At        string `json:"at,omitempty"`
}

// HomeDealing is the record that a dealer's home is a dealing location the
// exchange approved. Dealing from a location the exchange has not approved
// needs the exchange's permission (the exchanges' rules on approved
// locations for CTCL/terminal users); without this record a dealer cannot
// mark attendance from home (hrtime.go).
type HomeDealing struct {
	Approved bool   `json:"approved"`
	Ref      string `json:"ref,omitempty"`  // the exchange's approval reference
	Date     string `json:"date,omitempty"` // date of approval
	By       string `json:"by,omitempty"`
}

// Employee is one person on the firm's staff.
type Employee struct {
	Code           string           `json:"code"`
	Name           string           `json:"name"`
	Department     string           `json:"department"`
	Designation    string           `json:"designation"`
	Branch         string           `json:"branch,omitempty"`  // office code; empty = head office
	Partner        string           `json:"partner,omitempty"` // an authorised person's staff: the AP's code
	Manager        string           `json:"manager,omitempty"` // employee code
	JoinedOn       string           `json:"joined_on"`
	LeftOn         string           `json:"left_on,omitempty"` // last working day
	EmploymentType string           `json:"employment_type"`
	Status         string           `json:"status"`
	Gender         string           `json:"gender,omitempty"` // F, M, O (professional tax slabs differ in some states)
	DOB            string           `json:"dob,omitempty"`
	Email          string           `json:"email,omitempty"`
	Mobile         string           `json:"mobile,omitempty"`
	PersonalEmail  string           `json:"personal_email,omitempty"`
	Address        string           `json:"address,omitempty"`
	Emergency      EmergencyContact `json:"emergency"`
	PAN            string           `json:"pan,omitempty"`
	Aadhaar4       string           `json:"aadhaar4,omitempty"`
	UAN            string           `json:"uan,omitempty"`
	ESIC           string           `json:"esic,omitempty"`
	Bank           SalaryBank       `json:"bank"`
	Docs           []StaffDoc       `json:"docs"`
	Certs          []StaffCert      `json:"certs"`
	Duties         []string         `json:"duties"`
	User           string           `json:"user,omitempty"`   // Ledgerline login
	Dealer         string           `json:"dealer,omitempty"` // dealers register (dealers.go)
	KMP            string           `json:"kmp,omitempty"`    // key managerial personnel: the position, e.g. Principal officer
	Designated     bool             `json:"designated"`       // designated person under the insider trading code
	State          string           `json:"state,omitempty"`  // state of work, for professional tax
	Shift          string           `json:"shift,omitempty"`
	WorkPolicy     string           `json:"work_policy,omitempty"` // hybrid work policy (hrtime.go)
	DeviceID       string           `json:"device_id,omitempty"`   // biometric device user ID (CSV upload)
	HomeDealing    HomeDealing      `json:"home_dealing"`
	TaxRegime      string           `json:"tax_regime,omitempty"` // new (default) or old
	NoPF           bool             `json:"no_pf,omitempty"`      // not a PF member (e.g. excluded employee)
	PFFullWages    bool             `json:"pf_full_wages,omitempty"`
	EPSMember      bool             `json:"eps_member"`
	Disabled       bool             `json:"disabled,omitempty"` // person with disability (ESI threshold)
	Notes          string           `json:"notes,omitempty"`
	CreatedBy      string           `json:"created_by,omitempty"`
	CreatedAt      string           `json:"created_at,omitempty"`
	UpdatedBy      string           `json:"updated_by,omitempty"`
	UpdatedAt      string           `json:"updated_at,omitempty"`
}

func (e Employee) dealer() bool {
	for _, d := range e.Duties {
		if isDealerDuty(d) {
			return true
		}
	}
	return false
}

// inService: on the rolls on day.
func (e Employee) inService(day string) bool {
	return e.JoinedOn != "" && e.JoinedOn <= day && (e.LeftOn == "" || day <= e.LeftOn) && e.Status != EmpLeft
}

var (
	reEmpCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{1,19}$`)
	reUAN     = regexp.MustCompile(`^[0-9]{12}$`)
	reESIC    = regexp.MustCompile(`^[0-9]{10}$`)
	reDigits4 = regexp.MustCompile(`^[0-9]{4}$`)
)

// ---- policy: configurable rules ----

// CertRule says who must hold which certificate, by duty. Each rule names
// what it is based on; the firm edits them.
type CertRule struct {
	Duty           string `json:"duty"`
	Cert           string `json:"cert"`
	Basis          string `json:"basis"`
	BlocksTerminal bool   `json:"blocks_terminal"` // an expired certificate flags the person's terminals
}

// DefaultCertRules are the usual requirements for a stock broker and DP.
// They are a starting point; the firm keeps its own list.
var DefaultCertRules = []CertRule{
	// SEBI (Certification of Associated Persons in the Securities Markets)
	// Regulations, 2007, and SEBI's 2010 notification under them requiring
	// associated persons dealing in equity derivatives to hold NISM Series
	// VIII; the exchanges check it for every approved user of a terminal.
	{"Dealer (equity derivatives)", "NISM Series VIII (Equity Derivatives)", "SEBI Certification of Associated Persons Regulations 2007; NISM Series VIII for equity derivatives dealers", true},
	{"AP staff (derivatives)", "NISM Series VIII (Equity Derivatives)", "SEBI Certification of Associated Persons Regulations 2007; the exchanges' AP rules: AP staff dealing in derivatives hold the same certificate", true},
	// The same regulations: NISM Series I for currency derivatives, XVI
	// for commodity derivatives, IV for interest rate derivatives.
	{"Dealer (currency derivatives)", "NISM Series I (Currency Derivatives)", "SEBI Certification of Associated Persons Regulations 2007; NISM Series I", true},
	{"Dealer (commodity derivatives)", "NISM Series XVI (Commodity Derivatives)", "SEBI Certification of Associated Persons Regulations 2007; NISM Series XVI", true},
	{"Dealer (interest rate derivatives)", "NISM Series IV (Interest Rate Derivatives)", "SEBI Certification of Associated Persons Regulations 2007; NISM Series IV", true},
	// Depository participants' staff: NISM Series VI (Depository
	// Operations), under the same regulations.
	{"DP operations", "NISM Series VI (Depository Operations)", "SEBI Certification of Associated Persons Regulations 2007; NISM Series VI for DP staff", false},
	// Compliance officers of intermediaries: NISM Series III-A.
	{"Compliance officer", "NISM Series III-A (Securities Intermediaries Compliance)", "SEBI Certification of Associated Persons Regulations 2007; NISM Series III-A for compliance officers", false},
	// SEBI (Research Analysts) Regulations 2014 reg. 7: NISM Series XV.
	{"Research analyst", "NISM Series XV (Research Analyst)", "SEBI (Research Analysts) Regulations 2014, reg. 7", false},
	// SEBI (Investment Advisers) Regulations 2013 reg. 7: NISM X-A and X-B.
	{"Investment adviser", "NISM Series X-A (Investment Adviser Level 1)", "SEBI (Investment Advisers) Regulations 2013, reg. 7", false},
	{"Investment adviser", "NISM Series X-B (Investment Adviser Level 2)", "SEBI (Investment Advisers) Regulations 2013, reg. 7", false},
	// AMFI registration (ARN/EUIN) needs NISM Series V-A.
	{"Mutual fund distribution", "NISM Series V-A (Mutual Fund Distributors)", "SEBI circulars on mutual fund distributors; AMFI EUIN needs NISM Series V-A", false},
}

// OwnTradingPolicy is the firm's code for its people's own trading. Based
// on the SEBI (Prohibition of Insider Trading) Regulations 2015, reg. 9(1)
// and Schedule C (minimum standards for the code of conduct of
// intermediaries and fiduciaries: designated persons and their immediate
// relatives, pre-clearance above a threshold, trading within 7 trading
// days of pre-clearance, no contra trade for 6 months, disclosures). The
// firm sets its own values; nothing here is fixed.
type OwnTradingPolicy struct {
	Applies           string   `json:"applies"`              // designated (default) or all: who must pre-clear
	PreClearAbove     int64    `json:"pre_clear_above"`      // ₹ value of a trade above which pre-clearance is needed (0: every trade)
	PreClearValidDays int      `json:"pre_clear_valid_days"` // days a pre-clearance stays valid (Schedule C: 7 trading days)
	ContraDays        int      `json:"contra_days"`          // no opposite trade within this many days (Schedule C: 6 months)
	RelativesCovered  bool     `json:"relatives_covered"`    // immediate relatives' accounts are declared and covered
	OutsideAllowed    bool     `json:"outside_allowed"`      // accounts with other brokers allowed when declared
	AnnualDueMonth    int      `json:"annual_due_month"`     // the annual holdings declaration is due by the end of this month (default 4: April)
	Restricted        []string `json:"restricted"`           // securities (ISIN or symbol) on the restricted list: pre-clearance refused
	Basis             string   `json:"basis"`
}

// LeaveType is a kind of leave with its rules.
type LeaveType struct {
	Code       string `json:"code"` // CL, SL, EL, LWP...
	Name       string `json:"name"`
	Paid       bool   `json:"paid"`
	Annual     string `json:"annual"`      // days granted at the start of the leave year (prorated for joiners)
	Monthly    string `json:"monthly"`     // days accrued at the end of each month of service
	MaxBalance string `json:"max_balance"` // cap on the balance (0: none)
	CarryMax   string `json:"carry_max"`   // carried into the next year at most (0: none)
	EncashMax  string `json:"encash_max"`  // encashed at year end at most (0: none); the rest lapses
	Negative   bool   `json:"negative"`    // may be taken without a balance (leave without pay)
	Gender     string `json:"gender,omitempty"`
}

// Shift is working hours and weekly offs.
type Shift struct {
	Code      string   `json:"code"`
	Name      string   `json:"name"`
	Start     string   `json:"start"` // HH:MM
	End       string   `json:"end"`
	GraceMins int      `json:"grace_mins"`
	HalfHours string   `json:"half_hours"` // fewer hours than this is a half day
	WeeklyOff []string `json:"weekly_off"` // Sun, Sat...
}

// HRHoliday is an office holiday, for all offices or some.
type HRHoliday struct {
	Date     string   `json:"date"`
	Name     string   `json:"name"`
	Branches []string `json:"branches,omitempty"`
}

// Site is where an office is, for office punches from a phone: within the
// radius of its coordinates, or from one of its networks.
type Site struct {
	Branch   string   `json:"branch"` // office code; "" for head office
	Lat      float64  `json:"lat"`
	Lng      float64  `json:"lng"`
	RadiusM  int      `json:"radius_m"` // default 100
	Networks []string `json:"networks"`
}

// WorkPolicy is a hybrid work policy: how many office days, which home
// days, and which ways of working are allowed. The firm sets these.
type WorkPolicy struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Modes          []string `json:"modes"` // office, home, field, onduty
	OfficePerWeek  int      `json:"office_per_week"`
	OfficePerMonth int      `json:"office_per_month"`
	FixedHomeDays  []string `json:"fixed_home_days"`   // Mon, Fri...: home allowed on these days
	AnyHomePerWeek int      `json:"any_home_per_week"` // or home on any N days a week
	Departments    []string `json:"departments"`       // applies to these departments (unless a person has their own)
}

// HRPolicy is everything the firm sets for HRMS.
type HRPolicy struct {
	CertRules     []CertRule       `json:"cert_rules"`
	ReminderDays  []int            `json:"reminder_days"` // before a certificate or document expires (default 60, 30, 7)
	OwnTrading    OwnTradingPolicy `json:"own_trading"`
	LeaveTypes    []LeaveType      `json:"leave_types"`
	LeaveYear     string           `json:"leave_year"` // calendar (default) or financial
	Shifts        []Shift          `json:"shifts"`
	Holidays      []HRHoliday      `json:"holidays"`
	ExchangeHols  bool             `json:"exchange_holidays"` // exchange trading holidays are office holidays
	Sites         []Site           `json:"sites"`
	UseSignInNets bool             `json:"use_signin_networks"` // the sign-in "firm's networks" (Security settings) count as office networks
	WorkPolicies  []WorkPolicy     `json:"work_policies"`
	SelfieDays    int              `json:"selfie_days"`   // punch photos kept this many days (default 90)
	DeactivateAt  string           `json:"deactivate_at"` // a leaver's login is switched off at this time on the last day (default 18:30)
	AbsentIsLOP   bool             `json:"absent_is_lop"` // a working day with no attendance and no leave is loss of pay (off: only marked absences)
	UpdatedBy     string           `json:"updated_by,omitempty"`
	UpdatedAt     string           `json:"updated_at,omitempty"`
}

// DefaultHRPolicy is the starting policy.
func DefaultHRPolicy() HRPolicy {
	return HRPolicy{
		CertRules:    append([]CertRule{}, DefaultCertRules...),
		ReminderDays: []int{60, 30, 7},
		OwnTrading: OwnTradingPolicy{Applies: "designated", PreClearAbove: 1000000, PreClearValidDays: 7, ContraDays: 180, RelativesCovered: true, OutsideAllowed: true, AnnualDueMonth: 4, Restricted: []string{},
			Basis: "SEBI (Prohibition of Insider Trading) Regulations 2015, reg. 9(1) and Schedule C; the firm's code of conduct"},
		LeaveTypes: []LeaveType{
			{Code: "CL", Name: "Casual leave", Paid: true, Annual: "8", MaxBalance: "8"},
			{Code: "SL", Name: "Sick leave", Paid: true, Annual: "7", CarryMax: "21", MaxBalance: "28"},
			{Code: "EL", Name: "Earned leave", Paid: true, Monthly: "1.5", CarryMax: "30", EncashMax: "15", MaxBalance: "45"},
			{Code: "LWP", Name: "Leave without pay", Paid: false, Negative: true},
		},
		LeaveYear:     "calendar",
		Shifts:        []Shift{{Code: "GEN", Name: "General", Start: "09:30", End: "18:30", GraceMins: 15, HalfHours: "4.5", WeeklyOff: []string{"Sun", "Sat"}}},
		Holidays:      []HRHoliday{},
		ExchangeHols:  true,
		Sites:         []Site{},
		UseSignInNets: true,
		WorkPolicies:  []WorkPolicy{{ID: "OFFICE", Name: "Office every working day", Modes: []string{"office", "field", "onduty"}}},
		SelfieDays:    90,
		DeactivateAt:  "18:30",
	}
}

// HRPolicy returns the firm's HR policy, with defaults filled in.
func (o *Ops) hrPolicy(ctx context.Context, tenant string) HRPolicy {
	p, _, _ := docs.GetOr(ctx, o.Docs, tenant, cHRPolicy, "main", DefaultHRPolicy())
	d := DefaultHRPolicy()
	if len(p.ReminderDays) == 0 {
		p.ReminderDays = d.ReminderDays
	}
	if p.SelfieDays <= 0 {
		p.SelfieDays = 90
	}
	if p.DeactivateAt == "" {
		p.DeactivateAt = d.DeactivateAt
	}
	if p.OwnTrading.PreClearValidDays <= 0 {
		p.OwnTrading.PreClearValidDays = 7
	}
	if p.OwnTrading.AnnualDueMonth <= 0 {
		p.OwnTrading.AnnualDueMonth = 4
	}
	if len(p.Shifts) == 0 {
		p.Shifts = d.Shifts
	}
	for _, x := range []*[]string{&p.OwnTrading.Restricted} {
		if *x == nil {
			*x = []string{}
		}
	}
	return p
}

// HRPolicyView returns the policy (any staff with the module on may read it).
func (o *Ops) HRPolicy(ctx context.Context, tenant, actor string) (HRPolicy, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return HRPolicy{}, err
	}
	if _, err := o.RoleOf(ctx, tenant, actor); err != nil {
		return HRPolicy{}, err
	}
	return o.hrPolicy(ctx, tenant), nil
}

// SaveHRPolicy checks and stores the policy. HR managers, and the
// compliance officer (who owns the certificate and own-trading rules).
func (o *Ops) SaveHRPolicy(ctx context.Context, tenant, actor string, p HRPolicy) (HRPolicy, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return p, err
	}
	if err := o.require(ctx, tenant, actor, "Changing HR policies", RoleHR, RoleCompliance); err != nil {
		return p, err
	}
	for i, r := range p.CertRules {
		if strings.TrimSpace(r.Duty) == "" || strings.TrimSpace(r.Cert) == "" {
			return p, invalid("certificate rule %d needs a duty and a certificate", i+1)
		}
		if strings.TrimSpace(r.Basis) == "" {
			return p, invalid("certificate rule %d: say what it is based on (regulation, circular or the firm's own policy)", i+1)
		}
	}
	for _, d := range p.ReminderDays {
		if d <= 0 || d > 365 {
			return p, invalid("reminder days must be between 1 and 365")
		}
	}
	seen := map[string]bool{}
	for _, l := range p.LeaveTypes {
		l.Code = strings.ToUpper(strings.TrimSpace(l.Code))
		if l.Code == "" || seen[l.Code] {
			return p, invalid("each leave type needs its own code")
		}
		seen[l.Code] = true
		for _, v := range []string{l.Annual, l.Monthly, l.MaxBalance, l.CarryMax, l.EncashMax} {
			if v != "" {
				if d, err := parseDays(v); err != nil || d < 0 {
					return p, invalid("leave type %s: days must be numbers like 1.5", l.Code)
				}
			}
		}
	}
	for _, s := range p.Shifts {
		if !reHHMM.MatchString(s.Start) || !reHHMM.MatchString(s.End) {
			return p, invalid("shift %s: times are HH:MM", s.Code)
		}
	}
	for _, h := range p.Holidays {
		if !validDay(h.Date) || strings.TrimSpace(h.Name) == "" {
			return p, invalid("each holiday needs a date (YYYY-MM-DD) and a name")
		}
	}
	for _, s := range p.Sites {
		if s.Lat < -90 || s.Lat > 90 || s.Lng < -180 || s.Lng > 180 {
			return p, invalid("office %s: latitude and longitude are out of range", firstOf(s.Branch, "head office"))
		}
		for _, n := range s.Networks {
			if !validNet(n) {
				return p, invalid("office %s: %q is not an address or CIDR range", firstOf(s.Branch, "head office"), n)
			}
		}
	}
	for _, w := range p.WorkPolicies {
		if w.ID == "" {
			return p, invalid("each work policy needs an ID")
		}
		for _, m := range w.Modes {
			if !contains(PunchModes, m) {
				return p, invalid("work policy %s: modes are %s", w.ID, strings.Join(PunchModes, ", "))
			}
		}
	}
	if p.LeaveYear != "" && p.LeaveYear != "calendar" && p.LeaveYear != "financial" {
		return p, invalid("the leave year is calendar or financial")
	}
	if p.DeactivateAt != "" && !reHHMM.MatchString(p.DeactivateAt) {
		return p, invalid("the deactivation time is HH:MM")
	}
	p.UpdatedBy, p.UpdatedAt = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cHRPolicy, "main", p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "hr.policy.saved", "", map[string]any{"cert_rules": len(p.CertRules), "leave_types": len(p.LeaveTypes), "work_policies": len(p.WorkPolicies)})
	return o.hrPolicy(ctx, tenant), nil
}

func validDay(d string) bool { _, err := time.Parse("2006-01-02", d); return err == nil }

// ---- reading and scope ----

func (o *Ops) employees(ctx context.Context, tenant string) ([]Employee, error) {
	es, err := docs.List[Employee](ctx, o.Docs, tenant, cEmployee, "")
	sort.Slice(es, func(i, j int) bool { return es[i].Code < es[j].Code })
	return es, err
}

func (o *Ops) employee(ctx context.Context, tenant, code string) (Employee, error) {
	e, _, err := docs.Get[Employee](ctx, o.Docs, tenant, cEmployee, strings.ToUpper(code))
	return e, err
}

// employeeOf is the employee linked to a login.
func (o *Ops) employeeOf(ctx context.Context, tenant, user string) (Employee, bool) {
	if user == "" {
		return Employee{}, false
	}
	es, _ := o.employees(ctx, tenant)
	for _, e := range es {
		if e.User == user && e.Status != EmpLeft {
			return e, true
		}
	}
	for _, e := range es {
		if e.User == user {
			return e, true
		}
	}
	return Employee{}, false
}

// hrScope is what an actor may see of the staff.
type hrScope struct {
	all  bool            // every employee, with identifiers available for reveal
	pay  bool            // pay and statutory data
	self string          // own employee code
	team map[string]bool // employees the actor manages (codes)
	role string
}

func (s hrScope) sees(code string) bool { return s.all || s.self == code || s.team[code] }

// manages: the actor manages this person (team, not self).
func (s hrScope) manages(code string) bool { return s.all || s.team[code] }

// scopeFor works out whom an actor sees.
func (o *Ops) scopeFor(ctx context.Context, tenant, actor string) (hrScope, error) {
	role, err := o.RoleOf(ctx, tenant, actor)
	if err != nil {
		return hrScope{}, err
	}
	s := hrScope{role: role, team: map[string]bool{}}
	switch role {
	case RoleAdmin, RoleHR:
		s.all, s.pay = true, true
	case RolePayroll:
		s.all, s.pay = true, true
	case RoleCompliance:
		s.all = true
	case RoleClient:
		return s, fmt.Errorf("%w: not for client logins", ErrForbidden)
	}
	me, ok := o.employeeOf(ctx, tenant, actor)
	if !ok {
		return s, nil
	}
	s.self = me.Code
	es, _ := o.employees(ctx, tenant)
	kids := map[string][]string{}
	for _, e := range es {
		if e.Manager != "" {
			kids[e.Manager] = append(kids[e.Manager], e.Code)
		}
	}
	var walk func(string)
	walk = func(c string) {
		for _, k := range kids[c] {
			if !s.team[k] && k != me.Code {
				s.team[k] = true
				walk(k)
			}
		}
	}
	walk(me.Code)
	if role == RoleBranch {
		// Branch staff: a branch manager (someone with reports, or named
		// the office's manager) sees the people of their own office and
		// the offices under it, and nobody outside it.
		u, _ := o.user(ctx, tenant, actor)
		bs, _ := o.Branches(ctx, tenant)
		head := len(s.team) > 0
		for _, b := range bs {
			if b.Code == u.Branch && b.Manager != "" && (strings.EqualFold(b.Manager, actor) || strings.EqualFold(b.Manager, me.Code)) {
				head = true
			}
		}
		offices := map[string]bool{u.Branch: true}
		for _, c := range branchTree(bs)[u.Branch] {
			offices[c] = true
		}
		for k := range s.team {
			if e, ok := find(es, k); !ok || !offices[e.Branch] {
				delete(s.team, k)
			}
		}
		if head && u.Branch != "" {
			for _, e := range es {
				if offices[e.Branch] && e.Code != me.Code {
					s.team[e.Code] = true
				}
			}
		}
	}
	return s, nil
}

func find(es []Employee, code string) (Employee, bool) {
	for _, e := range es {
		if e.Code == code {
			return e, true
		}
	}
	return Employee{}, false
}

// ---- views ----

// EmployeeView is an employee as a screen shows them: identifiers masked,
// and only what the viewer may see.
type EmployeeView struct {
	Employee
	ManagerName  string     `json:"manager_name,omitempty"`
	UserName     string     `json:"user_name,omitempty"`
	Team         bool       `json:"team"` // shown as a team member (no identifiers or pay)
	CertStatus   []CertNeed `json:"cert_status"`
	TerminalFlag bool       `json:"terminal_flag"` // a certificate the terminal needs has run out
	OpenTasks    int        `json:"open_tasks"`
}

func maskTail4(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) <= 4 {
		return s
	}
	return strings.Repeat("X", len(s)-4) + s[len(s)-4:]
}

// masked hides the identifiers; team views also drop contact details
// beyond work email and phone, and documents.
func (e Employee) masked(team bool) Employee {
	e.PAN = MaskPAN(e.PAN)
	e.UAN = maskTail4(e.UAN)
	e.ESIC = maskTail4(e.ESIC)
	e.Bank.Account = MaskAccount(e.Bank.Account)
	if e.Aadhaar4 != "" {
		e.Aadhaar4 = "XXXX XXXX " + e.Aadhaar4
	}
	if team {
		e.PAN, e.UAN, e.ESIC, e.Aadhaar4, e.Bank = "", "", "", "", SalaryBank{}
		e.DOB, e.PersonalEmail, e.Address, e.Emergency = "", "", "", EmergencyContact{}
		e.Docs, e.TaxRegime, e.NoPF, e.PFFullWages = []StaffDoc{}, "", false, false
	}
	if e.Docs == nil {
		e.Docs = []StaffDoc{}
	}
	if e.Certs == nil {
		e.Certs = []StaffCert{}
	}
	if e.Duties == nil {
		e.Duties = []string{}
	}
	return e
}

func (o *Ops) viewOf(ctx context.Context, tenant string, e Employee, team bool, es []Employee, p HRPolicy) EmployeeView {
	v := EmployeeView{Employee: e.masked(team), Team: team}
	if m, ok := find(es, e.Manager); ok {
		v.ManagerName = m.Name
	}
	if e.User != "" {
		if u, err := o.user(ctx, tenant, e.User); err == nil {
			v.UserName = u.Name
		}
	}
	v.CertStatus = certNeeds(e, p, o.today())
	for _, c := range v.CertStatus {
		if c.State == "Expired" || c.State == "Missing" {
			v.TerminalFlag = v.TerminalFlag || (c.BlocksTerminal && e.Status != EmpLeft)
		}
	}
	return v
}

// StaffList is the People screen.
type StaffList struct {
	Employees []EmployeeView `json:"employees"`
	CanEdit   bool           `json:"can_edit"`
	CanPay    bool           `json:"can_pay"`
	All       bool           `json:"all"`
	Self      string         `json:"self,omitempty"`
	Branches  []string       `json:"branches"`
	Partners  []string       `json:"partners"`
	Duties    []string       `json:"duties"`
	Types     []string       `json:"types"`
	DocKinds  []string       `json:"doc_kinds"`
	Users     []User         `json:"users"`
	Counts    map[string]int `json:"counts"`
}

// Staff lists the employees the actor may see.
func (o *Ops) Staff(ctx context.Context, tenant, actor string) (StaffList, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return StaffList{}, err
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return StaffList{}, err
	}
	es, err := o.employees(ctx, tenant)
	if err != nil {
		return StaffList{}, err
	}
	p := o.hrPolicy(ctx, tenant)
	v := StaffList{Employees: []EmployeeView{}, CanEdit: sc.role == RoleAdmin || sc.role == RoleHR, CanPay: sc.pay, All: sc.all, Self: sc.self,
		Duties: append(append([]string{}, Duties...), extraDuties(p)...), Types: EmploymentTypes, DocKinds: StaffDocKinds, Counts: map[string]int{}, Branches: []string{}, Partners: []string{}, Users: []User{}}
	for _, e := range es {
		if !sc.sees(e.Code) {
			continue
		}
		team := !sc.all && e.Code != sc.self
		v.Employees = append(v.Employees, o.viewOf(ctx, tenant, e, team, es, p))
		v.Counts[e.Status]++
	}
	if sc.all {
		bs, _ := o.Branches(ctx, tenant)
		for _, b := range bs {
			v.Branches = append(v.Branches, b.Code)
		}
		ps, _ := docs.List[Partner](ctx, o.Docs, tenant, cPartner, "")
		for _, x := range ps {
			if x.Kind == "Authorised person" {
				v.Partners = append(v.Partners, x.Code)
			}
		}
		if v.CanEdit {
			us, _ := o.Users(ctx, tenant)
			for _, u := range us {
				if u.Role != RoleClient {
					v.Users = append(v.Users, User{ID: u.ID, Name: u.Name, Role: u.Role, Active: u.Active})
				}
			}
		}
	}
	return v, nil
}

func extraDuties(p HRPolicy) []string {
	var out []string
	for _, r := range p.CertRules {
		if !contains(Duties, r.Duty) && !contains(out, r.Duty) {
			out = append(out, r.Duty)
		}
	}
	return out
}

// GetEmployee returns one employee as the actor may see them.
func (o *Ops) GetEmployee(ctx context.Context, tenant, actor, code string) (EmployeeView, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return EmployeeView{}, err
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return EmployeeView{}, err
	}
	code = strings.ToUpper(code)
	if !sc.sees(code) {
		return EmployeeView{}, ErrNotFound
	}
	e, err := o.employee(ctx, tenant, code)
	if err != nil {
		return EmployeeView{}, err
	}
	es, _ := o.employees(ctx, tenant)
	return o.viewOf(ctx, tenant, e, !sc.all && code != sc.self, es, o.hrPolicy(ctx, tenant)), nil
}

// ---- saving ----

// SaveEmployee adds an employee (no code given: one is made) or changes
// one. A new employee raises the joining checklist; a change of role,
// duties or office raises an access review (hrlife.go). Identifiers sent
// masked (as the screen shows them) keep the stored value.
func (o *Ops) SaveEmployee(ctx context.Context, tenant, actor string, e Employee) (Employee, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return e, err
	}
	if err := o.require(ctx, tenant, actor, "Adding or changing employees", RoleHR); err != nil {
		return e, err
	}
	e.Code = strings.ToUpper(strings.TrimSpace(e.Code))
	var old *Employee
	if e.Code != "" {
		if x, err := o.employee(ctx, tenant, e.Code); err == nil {
			old = &x
		}
	} else {
		n, err := o.next(ctx, tenant, "hr-employee", actor)
		if err != nil {
			return e, err
		}
		e.Code = fmt.Sprintf("E%04d", n)
	}
	if old != nil {
		// Masked identifiers come back as they were shown: keep the stored ones.
		keep := func(v *string, stored string) {
			if strings.Contains(*v, "XXXX") || (*v != "" && strings.HasPrefix(*v, "X")) {
				*v = stored
			}
		}
		keep(&e.PAN, old.PAN)
		keep(&e.UAN, old.UAN)
		keep(&e.ESIC, old.ESIC)
		keep(&e.Bank.Account, old.Bank.Account)
		if strings.HasPrefix(e.Aadhaar4, "XXXX") {
			e.Aadhaar4 = old.Aadhaar4
		}
		// Documents, certificates, status and leaving are changed by their own calls.
		e.Docs, e.Certs, e.Status, e.LeftOn = old.Docs, old.Certs, old.Status, old.LeftOn
		e.CreatedBy, e.CreatedAt, e.HomeDealing = old.CreatedBy, old.CreatedAt, old.HomeDealing
	} else {
		e.Status, e.Docs, e.Certs = EmpActive, []StaffDoc{}, []StaffCert{}
		if e.JoinedOn > o.today() {
			e.Status = EmpJoining
		}
		e.CreatedBy, e.CreatedAt = actor, o.now().Format(time.RFC3339)
		e.EPSMember = e.EPSMember || !e.NoPF
	}
	if err := o.checkEmployee(ctx, tenant, &e); err != nil {
		return e, err
	}
	e.UpdatedBy, e.UpdatedAt = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cEmployee, e.Code, e, actor); err != nil {
		return e, err
	}
	o.log(ctx, tenant, actor, map[bool]string{true: "hr.employee.changed", false: "hr.employee.added"}[old != nil], e.Code, map[string]any{"name": e.Name, "branch": e.Branch, "designation": e.Designation})
	if old == nil {
		o.startJoining(ctx, tenant, actor, e)
	} else {
		o.maybeAccessReview(ctx, tenant, actor, *old, e)
	}
	return e, nil
}

func (o *Ops) checkEmployee(ctx context.Context, tenant string, e *Employee) error {
	e.Name = strings.TrimSpace(e.Name)
	e.PAN = strings.ToUpper(strings.TrimSpace(e.PAN))
	e.Bank.IFSC = strings.ToUpper(strings.TrimSpace(e.Bank.IFSC))
	e.Manager = strings.ToUpper(strings.TrimSpace(e.Manager))
	e.TaxRegime = strings.ToLower(strings.TrimSpace(e.TaxRegime))
	switch {
	case !reEmpCode.MatchString(e.Code):
		return invalid("employee code: capital letters, digits and dashes, 2 to 20")
	case e.Name == "":
		return invalid("give the employee's name")
	case !validDay(e.JoinedOn):
		return invalid("give the date of joining (YYYY-MM-DD)")
	case e.EmploymentType != "" && !contains(EmploymentTypes, e.EmploymentType):
		return invalid("employment type is one of %s", strings.Join(EmploymentTypes, ", "))
	case e.PAN != "" && !rePAN.MatchString(e.PAN):
		return invalid("PAN must be 5 letters, 4 digits, 1 letter")
	case e.Aadhaar4 != "" && !reDigits4.MatchString(e.Aadhaar4):
		return invalid("keep only the last 4 digits of Aadhaar; the full number is never stored")
	case e.UAN != "" && !reUAN.MatchString(e.UAN):
		return invalid("UAN is 12 digits")
	case e.ESIC != "" && !reESIC.MatchString(e.ESIC):
		return invalid("ESIC insurance number (IP number) is 10 digits")
	case e.Bank.IFSC != "" && !reIFSC.MatchString(e.Bank.IFSC):
		return invalid("IFSC is 4 letters, 0, then 6 letters or digits")
	case e.Gender != "" && !contains([]string{"F", "M", "O"}, e.Gender):
		return invalid("gender is F, M or O")
	case e.DOB != "" && !validDay(e.DOB):
		return invalid("date of birth is YYYY-MM-DD")
	case e.TaxRegime != "" && e.TaxRegime != "new" && e.TaxRegime != "old":
		return invalid("tax regime is new or old")
	case e.Manager == e.Code:
		return invalid("a person cannot be their own manager")
	}
	if e.EmploymentType == "" {
		e.EmploymentType = "Permanent"
	}
	if e.Duties == nil {
		e.Duties = []string{}
	}
	if e.Manager != "" {
		es, _ := o.employees(ctx, tenant)
		// No loops in the reporting line.
		seen := map[string]bool{e.Code: true}
		for m := e.Manager; m != ""; {
			if seen[m] {
				return invalid("the reporting line loops back to %s", e.Code)
			}
			seen[m] = true
			x, ok := find(es, m)
			if !ok {
				if m == e.Manager {
					return invalid("manager %s is not an employee", e.Manager)
				}
				break
			}
			m = x.Manager
		}
	}
	if e.Branch != "" {
		bs, _ := o.Branches(ctx, tenant)
		ok := false
		for _, b := range bs {
			ok = ok || b.Code == e.Branch
		}
		if !ok {
			return invalid("branch %s is not one of the firm's offices", e.Branch)
		}
	}
	if e.User != "" {
		if _, err := o.user(ctx, tenant, e.User); err != nil {
			return invalid("there is no Ledgerline user %s", e.User)
		}
		es, _ := o.employees(ctx, tenant)
		for _, x := range es {
			if x.User == e.User && x.Code != e.Code && x.Status != EmpLeft {
				return invalid("user %s is already linked to %s", e.User, x.Code)
			}
		}
	}
	if e.Dealer != "" {
		if _, _, err := docs.Get[Dealer](ctx, o.Docs, tenant, cDealer, e.Dealer); err != nil {
			return invalid("dealer %s is not on the dealers register", e.Dealer)
		}
	}
	return nil
}

// LinkUser links an employee to their Ledgerline login (or unlinks with "").
func (o *Ops) LinkUser(ctx context.Context, tenant, actor, code, user string) (Employee, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return Employee{}, err
	}
	if err := o.require(ctx, tenant, actor, "Linking employees to logins", RoleHR); err != nil {
		return Employee{}, err
	}
	e, err := o.employee(ctx, tenant, code)
	if err != nil {
		return e, err
	}
	e.User = strings.TrimSpace(user)
	if err := o.checkEmployee(ctx, tenant, &e); err != nil {
		return e, err
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cEmployee, e.Code, e, actor); err != nil {
		return e, err
	}
	o.log(ctx, tenant, actor, "hr.employee.user_linked", e.Code, map[string]any{"user": e.User})
	if e.User != "" {
		o.tickItem(ctx, tenant, actor, e.Code, "join", "ll-user", "linked to "+e.User)
	}
	return e, nil
}

// SetHomeDealing records (or withdraws) the exchange's approval of a
// dealer's home as a dealing location. Compliance officer.
func (o *Ops) SetHomeDealing(ctx context.Context, tenant, actor, code string, h HomeDealing) (Employee, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return Employee{}, err
	}
	if err := o.require(ctx, tenant, actor, "Recording approved dealing locations", RoleCompliance); err != nil {
		return Employee{}, err
	}
	if h.Approved && (strings.TrimSpace(h.Ref) == "" || !validDay(h.Date)) {
		return Employee{}, invalid("give the exchange's approval reference and date")
	}
	h.By = actor
	e, err := docs.Update(ctx, o.Docs, tenant, cEmployee, strings.ToUpper(code), actor, func(x *Employee) error { x.HomeDealing = h; return nil })
	if err == nil {
		o.log(ctx, tenant, actor, "hr.home_dealing", e.Code, map[string]any{"approved": h.Approved, "ref": h.Ref})
	}
	return e, err
}

// ---- reveal ----

// EmployeeRevealFields are the masked fields that can be seen in full.
var EmployeeRevealFields = map[string]string{"pan": "PAN", "uan": "UAN", "esic": "ESIC number", "account": "salary bank account"}

// RevealEmployee returns one masked identifier in full, for a stated
// reason, and records who saw what and why. HR managers and Payroll.
func (o *Ops) RevealEmployee(ctx context.Context, tenant, actor, code, field, reason string) (string, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return "", err
	}
	if err := notSupport(actor, "Seeing full staff identifiers"); err != nil {
		return "", err
	}
	if err := o.require(ctx, tenant, actor, "Seeing full staff identifiers", RoleHR, RolePayroll); err != nil {
		return "", err
	}
	label, ok := EmployeeRevealFields[field]
	if !ok {
		return "", invalid("field must be pan, uan, esic or account")
	}
	if reason = strings.TrimSpace(reason); len(reason) < 5 {
		return "", invalid("give the reason for seeing the full %s (it is recorded)", label)
	}
	e, err := o.employee(ctx, tenant, code)
	if err != nil {
		return "", err
	}
	v := map[string]string{"pan": e.PAN, "uan": e.UAN, "esic": e.ESIC, "account": e.Bank.Account}[field]
	o.log(ctx, tenant, actor, "hr.employee.reveal", e.Code, map[string]any{"field": label, "reason": trim(reason, 200)})
	return v, nil
}

// ---- org chart ----

// OrgNode is a person with their reports.
type OrgNode struct {
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Designation string    `json:"designation"`
	Department  string    `json:"department"`
	Branch      string    `json:"branch,omitempty"`
	Reports     []OrgNode `json:"reports"`
}

// OrgChart is the reporting tree of the people the actor sees (active and
// joining staff); people whose manager is not visible are roots.
func (o *Ops) OrgChart(ctx context.Context, tenant, actor string) ([]OrgNode, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return nil, err
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	es, err := o.employees(ctx, tenant)
	if err != nil {
		return nil, err
	}
	vis := map[string]Employee{}
	for _, e := range es {
		if sc.sees(e.Code) && e.Status != EmpLeft {
			vis[e.Code] = e
		}
	}
	kids := map[string][]string{}
	var roots []string
	for _, c := range sortedKeys(vis) {
		e := vis[c]
		if _, ok := vis[e.Manager]; ok {
			kids[e.Manager] = append(kids[e.Manager], c)
		} else {
			roots = append(roots, c)
		}
	}
	var build func(string, int) OrgNode
	build = func(c string, depth int) OrgNode {
		e := vis[c]
		n := OrgNode{Code: c, Name: e.Name, Designation: e.Designation, Department: e.Department, Branch: e.Branch, Reports: []OrgNode{}}
		if depth < 50 {
			for _, k := range kids[c] {
				n.Reports = append(n.Reports, build(k, depth+1))
			}
		}
		return n
	}
	out := []OrgNode{}
	for _, r := range roots {
		out = append(out, build(r, 0))
	}
	return out, nil
}

// ---- documents ----

// AddStaffDoc adds a document to an employee's file (encrypted at rest
// with the firm's files). HR managers; anyone may add to their own file.
func (o *Ops) AddStaffDoc(ctx context.Context, tenant, actor, code, kind, name, expiry string, data []byte) (StaffDoc, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return StaffDoc{}, err
	}
	code = strings.ToUpper(code)
	if me, ok := o.employeeOf(ctx, tenant, actor); !ok || me.Code != code {
		if err := o.require(ctx, tenant, actor, "Adding staff documents", RoleHR); err != nil {
			return StaffDoc{}, err
		}
	}
	if !contains(StaffDocKinds, kind) {
		return StaffDoc{}, invalid("document kind is one of %s", strings.Join(StaffDocKinds, ", "))
	}
	if expiry != "" && !validDay(expiry) {
		return StaffDoc{}, invalid("expiry is YYYY-MM-DD")
	}
	if len(data) == 0 || len(data) > MaxDocBytes {
		return StaffDoc{}, invalid("documents must be between 1 byte and 5 MB")
	}
	ct := http.DetectContentType(data)
	if _, ok := docTypes[ct]; !ok {
		return StaffDoc{}, invalid("only PDF, JPEG and PNG files are accepted (this looks like %s)", ct)
	}
	sum := sha256.Sum256(data)
	rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "STAFF-DOC", Name: name, SHA256: hex.EncodeToString(sum[:]), Content: data})
	if err != nil {
		return StaffDoc{}, err
	}
	n, err := o.next(ctx, tenant, "hr-doc", actor)
	if err != nil {
		return StaffDoc{}, err
	}
	d := StaffDoc{ID: fmt.Sprintf("SD-%06d", n), Kind: kind, Name: firstOf(name, kind), ContentType: ct, SHA256: rf.SHA256, FileID: rf.ID, Expiry: expiry, By: actor, At: o.now().Format(time.RFC3339)}
	if _, err := docs.Update(ctx, o.Docs, tenant, cEmployee, code, actor, func(e *Employee) error { e.Docs = append(e.Docs, d); return nil }); err != nil {
		return d, err
	}
	o.log(ctx, tenant, actor, "hr.document.added", code, map[string]any{"doc": d.ID, "kind": kind, "sha256": d.SHA256})
	if kind == "Code of conduct" {
		o.tickItem(ctx, tenant, actor, code, "join", "coc", "signed copy "+d.ID)
	}
	if kind == "Background check" {
		o.tickItem(ctx, tenant, actor, code, "join", "bgcheck", "report "+d.ID)
	}
	return d, nil
}

// StaffDocFile returns a document's bytes (HR, or the person themselves);
// every view is recorded.
func (o *Ops) StaffDocFile(ctx context.Context, tenant, actor, code, id string) (StaffDoc, []byte, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return StaffDoc{}, nil, err
	}
	code = strings.ToUpper(code)
	if me, ok := o.employeeOf(ctx, tenant, actor); !ok || me.Code != code {
		if err := o.require(ctx, tenant, actor, "Seeing staff documents", RoleHR); err != nil {
			return StaffDoc{}, nil, err
		}
	}
	e, err := o.employee(ctx, tenant, code)
	if err != nil {
		return StaffDoc{}, nil, err
	}
	for _, d := range e.Docs {
		if d.ID == id {
			rf, err := o.App.Ingest.Raw.Get(ctx, tenant, d.FileID)
			if err != nil {
				return d, nil, err
			}
			o.log(ctx, tenant, actor, "hr.document.viewed", code, map[string]any{"doc": id})
			return d, rf.Content, nil
		}
	}
	return StaffDoc{}, nil, ErrNotFound
}

// ---- key managerial personnel and designated persons ----

// KMPRow is one line of the KMP and designated persons list.
type KMPRow struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Designation string `json:"designation"`
	KMP         string `json:"kmp,omitempty"`
	Designated  bool   `json:"designated"`
	Status      string `json:"status"`
	JoinedOn    string `json:"joined_on"`
	LeftOn      string `json:"left_on,omitempty"`
	PAN         string `json:"pan"` // masked
}

// KMPList lists key managerial personnel and designated persons (for the
// exchanges' and SEBI's KMP filings and the insider trading code).
// HR, compliance.
func (o *Ops) KMPList(ctx context.Context, tenant, actor string) ([]KMPRow, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return nil, err
	}
	if err := o.require(ctx, tenant, actor, "Seeing key managerial personnel and designated persons", RoleHR, RoleCompliance); err != nil {
		return nil, err
	}
	es, err := o.employees(ctx, tenant)
	out := []KMPRow{}
	for _, e := range es {
		if e.KMP == "" && !e.Designated {
			continue
		}
		out = append(out, KMPRow{Code: e.Code, Name: e.Name, Designation: e.Designation, KMP: e.KMP, Designated: e.Designated, Status: e.Status, JoinedOn: e.JoinedOn, LeftOn: e.LeftOn, PAN: MaskPAN(e.PAN)})
	}
	return out, err
}
