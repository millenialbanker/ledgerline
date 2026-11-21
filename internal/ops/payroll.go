package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- payroll (India): statutory tables, salary structures, the monthly
// calculation ----
//
// Everything statutory is in an editable table, effective-dated
// (Statutory): PF and EPS rates and wage ceilings, ESI rates and its
// eligibility threshold, professional tax slabs per state, the income-tax
// slabs of both regimes with the standard deduction, rebate, surcharge and
// cess. The defaults below were checked on 21 November 2026 against the
// sources named on each; a firm changes them on People → Payroll →
// Statutory tables when the law changes, by adding a table from the date
// it applies.
//
// A month's line for a person: earned components (prorated for days not
// in service and loss-of-pay days from attendance and leave), arrears,
// one-off earnings, approved reimbursements and leave encashment; PF on
// PF wages (12% + 12%, EPS 8.33% inside the employer's share, on the wage
// ceiling unless the person contributes on full wages), ESI where wages
// are within the threshold (0.75% + 3.25%, rounded up), professional tax
// by the state of work, TDS on salary by the projected-annual method, and
// recoveries of advances and loans.

const (
	cStatutory   = "pay_statutory"
	cPaySettings = "pay_settings"
	cSalary      = "pay_salary"
	cPayInput    = "pay_input"
	cTaxDecl     = "pay_taxdecl"
	cClaim       = "pay_claim"
	cLoan        = "pay_loan"
)

// PTSlab is one professional tax slab on monthly gross salary.
type PTSlab struct {
	Upto   int64  `json:"upto"`   // ₹ monthly gross up to and including; 0 = and above
	Amount int64  `json:"amount"` // ₹ a month
	Feb    int64  `json:"feb"`    // ₹ in February, when different (Maharashtra: 300)
	Gender string `json:"gender,omitempty"`
}

// TaxSlab is one income-tax slab.
type TaxSlab struct {
	Upto int64  `json:"upto"` // ₹ a year, up to and including; 0 = and above
	Pct  string `json:"pct"`
}

// SurchargeBand is surcharge on income above a level.
type SurchargeBand struct {
	Above int64  `json:"above"`
	Pct   string `json:"pct"`
}

// TaxRegime is one regime's rules.
type TaxRegime struct {
	Slabs          []TaxSlab       `json:"slabs"`
	StdDeduction   int64           `json:"std_deduction"`
	RebateUpto     int64           `json:"rebate_upto"` // total income up to this gets the rebate
	RebateMax      int64           `json:"rebate_max"`
	MarginalRelief bool            `json:"marginal_relief"` // just above the rebate limit, tax is at most the income above it
	Surcharge      []SurchargeBand `json:"surcharge"`
	CessPct        string          `json:"cess_pct"`
	SeniorExempt   int64           `json:"senior_exempt,omitempty"`       // first slab's upper limit at 60 (old regime)
	SuperExempt    int64           `json:"super_senior_exempt,omitempty"` // at 80
}

// Statutory is the statutory table in force from a date.
type Statutory struct {
	From   string `json:"from"`
	Source string `json:"source"`
	// EPF & MP Act 1952 and the EPF, EPS and EDLI schemes (para 26A of the
	// EPF scheme for the wage ceiling); from 21 Nov 2025 the Code on Social
	// Security 2020 (with the Code on Wages 2019 definition of wages).
	PFEmployee  string `json:"pf_employee"`
	PFEmployer  string `json:"pf_employer"`
	EPS         string `json:"eps"`
	EDLI        string `json:"edli"`
	PFAdmin     string `json:"pf_admin"`
	PFCeiling   int64  `json:"pf_ceiling"`
	EPSCeiling  int64  `json:"eps_ceiling"`
	EDLICeiling int64  `json:"edli_ceiling"`
	PFAdminMin  int64  `json:"pf_admin_min"` // a month, per establishment
	EPSUpToAge  int    `json:"eps_up_to_age"`
	// Labour codes: wages are basic, DA and retaining allowance; when the
	// excluded payments (HRA, conveyance, special allowances...) exceed
	// half of the remuneration, the excess counts as wages (Code on Wages
	// 2019 s.2(y) proviso, applied by the Code on Social Security 2020).
	WageRule50 bool `json:"wage_rule_50"`
	// ESI Act 1948; rates from 1 Jul 2019 (G.S.R. 423(E)); wage ceiling
	// ₹21,000 from 1 Jan 2017 (₹25,000 for persons with disability).
	ESIEmployee    string               `json:"esi_employee"`
	ESIEmployer    string               `json:"esi_employer"`
	ESIThreshold   int64                `json:"esi_threshold"`
	ESIDisabled    int64                `json:"esi_threshold_disabled"`
	ESIExemptDaily int64                `json:"esi_exempt_daily"` // average daily wage up to this pays no employee share
	PT             map[string][]PTSlab  `json:"pt"`
	Tax            map[string]TaxRegime `json:"tax"`
	Max80C         int64                `json:"max_80c"`       // Schedule XV / s.123 of the 2025 Act (earlier 80C)
	Max80CCD1B     int64                `json:"max_80ccd1b"`   // additional NPS
	MaxHomeLoan    int64                `json:"max_home_loan"` // self-occupied house interest
	HRAMetroCities []string             `json:"hra_metro_cities"`
	GratuityMax    int64                `json:"gratuity_max"`
}

// DefaultStatutory is the table from 1 April 2026 (tax year 2026-27).
func DefaultStatutory() Statutory {
	return Statutory{
		From: "2026-04-01",
		Source: "Checked 21 Nov 2026. PF: EPF scheme 1952 (12%/12%, EPS 8.33% on up to ₹15,000, EDLI 0.5%, admin 0.5% min ₹500); Code on Social Security 2020 in force 21 Nov 2025. " +
			"ESI: 0.75%/3.25% from 1 Jul 2019, ₹21,000 ceiling. PT: Maharashtra State Tax on Professions Act 1975 schedule (₹7,500/₹10,000 bands, women nil to ₹25,000, ₹300 in February); " +
			"West Bengal State Tax on Professions Act 1979 schedule (₹10,000/15,000/25,000/40,000 bands). Income tax: rates for tax year 2026-27 unchanged from the Finance Act 2025 " +
			"(new regime 0/5/10/15/20/25/30% at ₹4/8/12/16/20/24 lakh, standard deduction ₹75,000, rebate to ₹12 lakh up to ₹60,000; old regime 0/5/20/30% at ₹2.5/5/10 lakh, standard deduction ₹50,000, rebate to ₹5 lakh up to ₹12,500); cess 4%. TDS on salary: s.392 of the Income-tax Act 2025 (earlier s.192); HRA 50% for Mumbai, Delhi, Kolkata, Chennai, Bengaluru, Hyderabad, Pune, Ahmedabad (Income-tax Rules 2026).",
		PFEmployee: "12", PFEmployer: "12", EPS: "8.33", EDLI: "0.5", PFAdmin: "0.5",
		PFCeiling: 15000, EPSCeiling: 15000, EDLICeiling: 15000, PFAdminMin: 500, EPSUpToAge: 58, WageRule50: true,
		ESIEmployee: "0.75", ESIEmployer: "3.25", ESIThreshold: 21000, ESIDisabled: 25000, ESIExemptDaily: 176,
		PT: map[string][]PTSlab{
			"Maharashtra": {{Upto: 7500, Amount: 0, Gender: "M"}, {Upto: 10000, Amount: 175, Gender: "M"}, {Upto: 0, Amount: 200, Feb: 300, Gender: "M"},
				{Upto: 25000, Amount: 0, Gender: "F"}, {Upto: 0, Amount: 200, Feb: 300, Gender: "F"}},
			"West Bengal": {{Upto: 10000, Amount: 0}, {Upto: 15000, Amount: 110}, {Upto: 25000, Amount: 130}, {Upto: 40000, Amount: 150}, {Upto: 0, Amount: 200}},
		},
		Tax: map[string]TaxRegime{
			"new": {Slabs: []TaxSlab{{400000, "0"}, {800000, "5"}, {1200000, "10"}, {1600000, "15"}, {2000000, "20"}, {2400000, "25"}, {0, "30"}},
				StdDeduction: 75000, RebateUpto: 1200000, RebateMax: 60000, MarginalRelief: true, CessPct: "4",
				Surcharge: []SurchargeBand{{5000000, "10"}, {10000000, "15"}, {20000000, "25"}}},
			"old": {Slabs: []TaxSlab{{250000, "0"}, {500000, "5"}, {1000000, "20"}, {0, "30"}},
				StdDeduction: 50000, RebateUpto: 500000, RebateMax: 12500, CessPct: "4", SeniorExempt: 300000, SuperExempt: 500000,
				Surcharge: []SurchargeBand{{5000000, "10"}, {10000000, "15"}, {20000000, "25"}, {50000000, "37"}}},
		},
		Max80C: 150000, Max80CCD1B: 50000, MaxHomeLoan: 200000, GratuityMax: 2000000,
		HRAMetroCities: []string{"Mumbai", "Delhi", "Kolkata", "Chennai", "Bengaluru", "Hyderabad", "Pune", "Ahmedabad"},
	}
}

// statutoryOn is the table in force on day.
func (o *Ops) statutoryOn(ctx context.Context, tenant, day string) Statutory {
	ss, _ := docs.List[Statutory](ctx, o.Docs, tenant, cStatutory, "")
	best := DefaultStatutory()
	found := false
	for _, s := range ss {
		if s.From <= day && (!found || s.From > best.From) {
			best, found = s, true
		}
	}
	if !found && day < best.From {
		best.From = "" // before the default's date the default still applies
	}
	return best
}

// StatutoryTables lists the saved tables (the default when none).
func (o *Ops) StatutoryTables(ctx context.Context, tenant, actor string) ([]Statutory, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return nil, err
	}
	if err := o.require(ctx, tenant, actor, "Seeing payroll", RolePayroll, RoleHR); err != nil {
		return nil, err
	}
	ss, err := docs.List[Statutory](ctx, o.Docs, tenant, cStatutory, "")
	if len(ss) == 0 {
		ss = []Statutory{DefaultStatutory()}
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].From > ss[j].From })
	return ss, err
}

// SaveStatutory saves a table effective from its date (a later change is
// a new table from the date it applies; months already approved keep the
// figures they were paid with). Payroll.
func (o *Ops) SaveStatutory(ctx context.Context, tenant, actor string, s Statutory) (Statutory, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return s, err
	}
	if err := o.require(ctx, tenant, actor, "Changing statutory payroll tables", RolePayroll); err != nil {
		return s, err
	}
	if !validDay(s.From) || strings.TrimSpace(s.Source) == "" {
		return s, invalid("give the date the table applies from and its source (notification or circular, and when checked)")
	}
	for _, p := range []string{s.PFEmployee, s.PFEmployer, s.EPS, s.EDLI, s.PFAdmin, s.ESIEmployee, s.ESIEmployer} {
		if d, err := money.ParseDec(p); err != nil || d.Sign() < 0 {
			return s, invalid("rates are percentages like 12 or 0.75")
		}
	}
	for _, r := range []string{"new", "old"} {
		t, ok := s.Tax[r]
		if !ok || len(t.Slabs) == 0 || t.Slabs[len(t.Slabs)-1].Upto != 0 {
			return s, invalid("the %s regime needs slabs ending with one for all income above (upto 0)", r)
		}
	}
	for st, slabs := range s.PT {
		if len(slabs) == 0 {
			return s, invalid("professional tax for %s has no slabs", st)
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cStatutory, s.From, s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "payroll.statutory.saved", s.From, map[string]any{"source": trim(s.Source, 200)})
	return s, nil
}

// PaySettings are the firm's payroll settings.
type PaySettings struct {
	PFCode        string `json:"pf_code"`  // EPFO establishment code
	ESICode       string `json:"esi_code"` // ESIC employer code
	TAN           string `json:"tan"`
	DefaultState  string `json:"default_state"` // state of work when a person has none
	PayDay        int    `json:"pay_day"`       // day of the next month salary is paid (default 1)
	BankAccount   string `json:"bank_account"`  // ledger account salary is paid from (default BANK:OWN)
	DebitAccount  string `json:"debit_account"` // the firm's account number salary is paid from (bank file)
	EncashDivisor int    `json:"encash_divisor"`
	ProofCutoff   int    `json:"proof_cutoff"` // from this month (1 = January) only verified tax proofs count (0: never)
	UpdatedBy     string `json:"updated_by,omitempty"`
}

func (o *Ops) paySettings(ctx context.Context, tenant string) PaySettings {
	p, _, _ := docs.GetOr(ctx, o.Docs, tenant, cPaySettings, "main", PaySettings{})
	if p.PayDay <= 0 {
		p.PayDay = 1
	}
	if p.BankAccount == "" {
		p.BankAccount = "BANK:OWN"
	}
	if p.EncashDivisor <= 0 {
		p.EncashDivisor = 30
	}
	if p.DefaultState == "" {
		set, _ := o.Settings(ctx, tenant)
		p.DefaultState = set.FirmState
	}
	return p
}

// SavePaySettings stores the payroll settings. Payroll.
func (o *Ops) SavePaySettings(ctx context.Context, tenant, actor string, p PaySettings) (PaySettings, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return p, err
	}
	if err := o.require(ctx, tenant, actor, "Changing payroll settings", RolePayroll); err != nil {
		return p, err
	}
	if p.BankAccount != "" && !strings.HasPrefix(strings.ToUpper(p.BankAccount), "BANK:") {
		return p, invalid("salary is paid from a BANK: account in the firm's books")
	}
	if p.ProofCutoff < 0 || p.ProofCutoff > 12 || p.PayDay > 28 {
		return p, invalid("the proof cut-off is a month (1 to 12) and the pay day 1 to 28")
	}
	p.BankAccount, p.UpdatedBy = strings.ToUpper(p.BankAccount), actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cPaySettings, "main", p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "payroll.settings.saved", "", nil)
	return o.paySettings(ctx, tenant), nil
}

// ---- salary structures ----

// SalaryComp is one monthly component.
type SalaryComp struct {
	Code    string       `json:"code"` // BASIC, DA, HRA, SPECIAL, CONV, LTA, ...
	Name    string       `json:"name"`
	Amount  money.Amount `json:"amount"`           // a full month
	PFWage  bool         `json:"pf_wage"`          // counts as wages for PF (basic, DA, retaining allowance)
	ESIWage bool         `json:"esi_wage"`         // counts for ESI
	Exempt  bool         `json:"exempt,omitempty"` // not taxable (rare: most allowances are taxable)
}

// SalaryStructure is a person's pay from a date.
type SalaryStructure struct {
	ID         string       `json:"id"` // code|from
	Employee   string       `json:"employee"`
	From       string       `json:"from"`
	Components []SalaryComp `json:"components"`
	By         string       `json:"by"`
	At         string       `json:"at"`
}

func (s SalaryStructure) gross() money.Amount {
	var t money.Amount
	for _, c := range s.Components {
		t += c.Amount
	}
	return t
}

func (s SalaryStructure) comp(code string) money.Amount {
	var t money.Amount
	for _, c := range s.Components {
		if c.Code == code {
			t += c.Amount
		}
	}
	return t
}

// structureOn is the structure in force on day.
func (o *Ops) structureOn(ctx context.Context, tenant, code, day string) (SalaryStructure, bool) {
	ss, _ := docs.List[SalaryStructure](ctx, o.Docs, tenant, cSalary, code+"|")
	var best SalaryStructure
	ok := false
	for _, s := range ss {
		if s.From <= day && (!ok || s.From > best.From) {
			best, ok = s, true
		}
	}
	return best, ok
}

// SaveSalaryStructure stores a person's pay from a date. Payroll and HR.
// Defaults: basic and DA are PF wages; every component counts for ESI.
func (o *Ops) SaveSalaryStructure(ctx context.Context, tenant, actor string, s SalaryStructure) (SalaryStructure, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return s, err
	}
	if err := o.require(ctx, tenant, actor, "Setting salary structures", RolePayroll, RoleHR); err != nil {
		return s, err
	}
	s.Employee = strings.ToUpper(s.Employee)
	if _, err := o.employee(ctx, tenant, s.Employee); err != nil {
		return s, invalid("no employee %s", s.Employee)
	}
	if !validDay(s.From) {
		return s, invalid("give the date the structure applies from")
	}
	if len(s.Components) == 0 {
		return s, invalid("add the components (basic at least)")
	}
	seen := map[string]bool{}
	for i := range s.Components {
		c := &s.Components[i]
		c.Code = strings.ToUpper(strings.TrimSpace(c.Code))
		if c.Code == "" || seen[c.Code] || c.Amount < 0 {
			return s, invalid("each component needs its own code and an amount of zero or more")
		}
		seen[c.Code] = true
		if c.Name == "" {
			c.Name = compNames[c.Code]
			if c.Name == "" {
				c.Name = c.Code
			}
		}
	}
	if !seen["BASIC"] {
		return s, invalid("the structure needs BASIC")
	}
	s.ID, s.By, s.At = s.Employee+"|"+s.From, actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cSalary, s.ID, s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "payroll.structure.saved", s.Employee, map[string]any{"from": s.From, "gross": s.gross().String()})
	return s, nil
}

var compNames = map[string]string{"BASIC": "Basic", "DA": "Dearness allowance", "HRA": "House rent allowance", "SPECIAL": "Special allowance", "CONV": "Conveyance allowance", "LTA": "Leave travel allowance", "MEDICAL": "Medical allowance", "BONUS": "Bonus", "INCENTIVE": "Incentive"}

// StandardStructure suggests a structure from a monthly gross: basic 50%
// (so the labour codes' 50% wage rule is met), HRA 40% of basic, the rest
// special allowance.
func StandardStructure(gross money.Amount) []SalaryComp {
	basic := (gross / 200) * 100 // half, in whole rupees
	hra := (basic * 40 / 100 / 100) * 100
	return []SalaryComp{{Code: "BASIC", Name: "Basic", Amount: basic, PFWage: true, ESIWage: true}, {Code: "HRA", Name: "House rent allowance", Amount: hra, ESIWage: true},
		{Code: "SPECIAL", Name: "Special allowance", Amount: gross - basic - hra, ESIWage: true}}
}

// ---- monthly inputs ----

// PayItem is a one-off earning or deduction, or an arrear of a component.
type PayItem struct {
	Code    string       `json:"code"`
	Name    string       `json:"name"`
	Amount  money.Amount `json:"amount"`
	PFWage  bool         `json:"pf_wage,omitempty"`
	ESIWage bool         `json:"esi_wage,omitempty"`
	Exempt  bool         `json:"exempt,omitempty"`
}

// PayInput is a person's inputs for a month.
type PayInput struct {
	ID         string    `json:"id"` // month|code
	Month      string    `json:"month"`
	Employee   string    `json:"employee"`
	LOP        *float64  `json:"lop,omitempty"` // overrides the loss-of-pay days from attendance
	Arrears    []PayItem `json:"arrears"`
	Earnings   []PayItem `json:"earnings"`   // bonus, incentive, gratuity...
	Deductions []PayItem `json:"deductions"` // notice pay recovery, other
	Hold       bool      `json:"hold"`       // pay held (computed, not paid)
	Note       string    `json:"note,omitempty"`
	By         string    `json:"by"`
}

// SavePayInput stores a person's inputs for a month. Payroll.
func (o *Ops) SavePayInput(ctx context.Context, tenant, actor string, in PayInput) (PayInput, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return in, err
	}
	if err := o.require(ctx, tenant, actor, "Entering payroll inputs", RolePayroll); err != nil {
		return in, err
	}
	in.Employee = strings.ToUpper(in.Employee)
	if _, err := time.Parse("2006-01", in.Month); err != nil {
		return in, invalid("month is YYYY-MM")
	}
	if r, _, err := docs.Get[PayRun](ctx, o.Docs, tenant, cPayRun, in.Month); err == nil && r.Status != "Draft" {
		return in, fmt.Errorf("%w: the %s payroll is %s; inputs can change only while it is a draft", ErrState, in.Month, strings.ToLower(r.Status))
	}
	if in.LOP != nil && (*in.LOP < 0 || *in.LOP > 31) {
		return in, invalid("loss-of-pay days are 0 to 31")
	}
	for _, l := range [][]PayItem{in.Arrears, in.Earnings, in.Deductions} {
		for _, x := range l {
			if x.Amount <= 0 || strings.TrimSpace(x.Name) == "" {
				return in, invalid("each item needs a name and an amount above zero")
			}
		}
	}
	in.ID, in.By = in.Month+"|"+in.Employee, actor
	if err := docs.Upsert(ctx, o.Docs, tenant, cPayInput, in.ID, in, actor); err != nil {
		return in, err
	}
	o.log(ctx, tenant, actor, "payroll.input.saved", in.Employee, map[string]any{"month": in.Month})
	return in, nil
}

// ---- tax declarations ----

// TaxAmounts are what a person declares (or what was verified).
type TaxAmounts struct {
	RentMonthly money.Amount `json:"rent_monthly"`
	Metro       bool         `json:"metro"` // rented house in a city with 50% HRA
	LandlordPAN string       `json:"landlord_pan,omitempty"`
	C80         money.Amount `json:"c80"`       // 80C-type investments (Schedule XV), besides PF
	D80         money.Amount `json:"d80"`       // health insurance
	NPS1B       money.Amount `json:"nps_1b"`    // additional NPS
	HomeLoan    money.Amount `json:"home_loan"` // interest on a self-occupied house
	OtherIncome money.Amount `json:"other_income"`
	PrevIncome  money.Amount `json:"prev_income"` // salary from an earlier employer this year
	PrevTDS     money.Amount `json:"prev_tds"`
	PrevPF      money.Amount `json:"prev_pf"`
	PrevPT      money.Amount `json:"prev_pt"`
}

// TaxDecl is a person's declaration for a financial year (Form 124, earlier
// Form 12BB), with proofs.
type TaxDecl struct {
	ID         string      `json:"id"` // code|fy
	Employee   string      `json:"employee"`
	FY         string      `json:"fy"`
	Regime     string      `json:"regime"` // new or old
	Declared   TaxAmounts  `json:"declared"`
	Verified   *TaxAmounts `json:"verified,omitempty"`
	Proofs     []StaffDoc  `json:"proofs"`
	Status     string      `json:"status"` // Declared, Proofs submitted, Verified
	VerifiedBy string      `json:"verified_by,omitempty"`
	At         string      `json:"at"`
}

// SaveTaxDecl stores the signed-in person's declaration (or, for others,
// Payroll's entry).
func (o *Ops) SaveTaxDecl(ctx context.Context, tenant, actor, code string, d TaxDecl) (TaxDecl, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return d, err
	}
	e, self, err := o.selfOrPayroll(ctx, tenant, actor, code)
	if err != nil {
		return d, err
	}
	if d.FY == "" {
		d.FY = finYear(o.today())
	}
	if d.Regime != "new" && d.Regime != "old" {
		return d, invalid("choose the new or the old tax regime")
	}
	if d.Declared.LandlordPAN != "" && !rePAN.MatchString(strings.ToUpper(d.Declared.LandlordPAN)) {
		return d, invalid("the landlord's PAN must be 5 letters, 4 digits, 1 letter")
	}
	id := e.Code + "|" + d.FY
	old, _, err := docs.Get[TaxDecl](ctx, o.Docs, tenant, cTaxDecl, id)
	if err == nil {
		if old.Status == "Verified" && self {
			return d, fmt.Errorf("%w: the declaration is verified; ask payroll to change it", ErrState)
		}
		d.Proofs, d.Verified = old.Proofs, old.Verified
	}
	if d.Proofs == nil {
		d.Proofs = []StaffDoc{}
	}
	d.ID, d.Employee, d.At = id, e.Code, o.now().Format(time.RFC3339)
	d.Declared.LandlordPAN = strings.ToUpper(d.Declared.LandlordPAN)
	d.Status = map[bool]string{true: "Proofs submitted", false: "Declared"}[len(d.Proofs) > 0]
	if err := docs.Upsert(ctx, o.Docs, tenant, cTaxDecl, id, d, actor); err != nil {
		return d, err
	}
	o.log(ctx, tenant, actor, "payroll.tax_decl.saved", e.Code, map[string]any{"fy": d.FY, "regime": d.Regime})
	return d, nil
}

// AddTaxProof adds a proof file to a declaration.
func (o *Ops) AddTaxProof(ctx context.Context, tenant, actor, code, fy, name string, data []byte) (TaxDecl, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return TaxDecl{}, err
	}
	e, _, err := o.selfOrPayroll(ctx, tenant, actor, code)
	if err != nil {
		return TaxDecl{}, err
	}
	sd, err := o.storeFile(ctx, tenant, actor, "TAX-PROOF", "Tax proof", name, data)
	if err != nil {
		return TaxDecl{}, err
	}
	d, err := docs.Update(ctx, o.Docs, tenant, cTaxDecl, e.Code+"|"+firstOf(fy, finYear(o.today())), actor, func(d *TaxDecl) error {
		d.Proofs = append(d.Proofs, sd)
		if d.Status == "Declared" {
			d.Status = "Proofs submitted"
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "payroll.tax_proof.added", e.Code, map[string]any{"doc": sd.ID})
	}
	return d, err
}

// VerifyTaxDecl records the amounts the proofs support. Payroll; not one's own.
func (o *Ops) VerifyTaxDecl(ctx context.Context, tenant, actor, code, fy string, v TaxAmounts) (TaxDecl, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return TaxDecl{}, err
	}
	if err := o.require(ctx, tenant, actor, "Verifying tax proofs", RolePayroll); err != nil {
		return TaxDecl{}, err
	}
	code = strings.ToUpper(code)
	if me, ok := o.employeeOf(ctx, tenant, actor); ok && me.Code == code {
		return TaxDecl{}, fmt.Errorf("%w: someone else verifies your own proofs", ErrForbidden)
	}
	d, err := docs.Update(ctx, o.Docs, tenant, cTaxDecl, code+"|"+firstOf(fy, finYear(o.today())), actor, func(d *TaxDecl) error {
		v.LandlordPAN = strings.ToUpper(v.LandlordPAN)
		d.Verified, d.Status, d.VerifiedBy = &v, "Verified", actor
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "payroll.tax_decl.verified", code, map[string]any{"fy": d.FY})
	}
	return d, err
}

func (o *Ops) selfOrPayroll(ctx context.Context, tenant, actor, code string) (Employee, bool, error) {
	code = strings.ToUpper(code)
	if me, ok := o.employeeOf(ctx, tenant, actor); ok && (code == "" || me.Code == code) {
		return me, true, nil
	}
	if err := o.require(ctx, tenant, actor, "Keeping payroll records for others", RolePayroll); err != nil {
		return Employee{}, false, err
	}
	e, err := o.employee(ctx, tenant, code)
	return e, false, err
}

// storeFile keeps an uploaded PDF or image with the firm's files.
func (o *Ops) storeFile(ctx context.Context, tenant, actor, source, kind, name string, data []byte) (StaffDoc, error) {
	if len(data) == 0 || len(data) > MaxDocBytes {
		return StaffDoc{}, invalid("files must be between 1 byte and 5 MB")
	}
	ct := detectDoc(data)
	if ct == "" {
		return StaffDoc{}, invalid("only PDF, JPEG and PNG files are accepted")
	}
	rf, err := o.putRaw(ctx, tenant, source, name, data)
	if err != nil {
		return StaffDoc{}, err
	}
	n, err := o.next(ctx, tenant, "hr-doc", actor)
	if err != nil {
		return StaffDoc{}, err
	}
	return StaffDoc{ID: fmt.Sprintf("SD-%06d", n), Kind: kind, Name: firstOf(name, kind), ContentType: ct, SHA256: rf.SHA256, FileID: rf.ID, By: actor, At: o.now().Format(time.RFC3339)}, nil
}

// ---- reimbursements, advances and loans ----

// Claim is a reimbursement claim (with the bill).
type Claim struct {
	ID        string       `json:"id"`
	Employee  string       `json:"employee"`
	Name      string       `json:"name"`
	Date      string       `json:"date"`
	Kind      string       `json:"kind"` // Travel, Mobile, Meals, Other...
	Amount    money.Amount `json:"amount"`
	Note      string       `json:"note,omitempty"`
	Bill      *StaffDoc    `json:"bill,omitempty"`
	Status    string       `json:"status"` // Pending, Approved, Rejected, Paid
	DecidedBy string       `json:"decided_by,omitempty"`
	PaidIn    string       `json:"paid_in,omitempty"` // payroll month
}

// ClaimReimbursement is the signed-in person's claim.
func (o *Ops) ClaimReimbursement(ctx context.Context, tenant, actor string, c Claim, bill []byte, billName string) (Claim, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return c, err
	}
	e, ok := o.employeeOf(ctx, tenant, actor)
	if !ok {
		return c, fmt.Errorf("%w: your login is not linked to an employee", ErrForbidden)
	}
	if !validDay(c.Date) || c.Amount <= 0 || strings.TrimSpace(c.Kind) == "" {
		return c, invalid("give the date, the kind of expense and the amount")
	}
	if len(bill) > 0 {
		sd, err := o.storeFile(ctx, tenant, actor, "CLAIM-BILL", "Bill", billName, bill)
		if err != nil {
			return c, err
		}
		c.Bill = &sd
	}
	n, err := o.next(ctx, tenant, "pay-claim", actor)
	if err != nil {
		return c, err
	}
	c.ID, c.Employee, c.Name, c.Status, c.DecidedBy, c.PaidIn = fmt.Sprintf("CL-%05d", n), e.Code, e.Name, "Pending", "", ""
	if err := docs.Upsert(ctx, o.Docs, tenant, cClaim, c.ID, c, actor); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "payroll.claim.made", c.ID, map[string]any{"amount": c.Amount.String(), "kind": c.Kind})
	o.notifyManager(ctx, tenant, e, "claim:"+c.ID, fmt.Sprintf("Reimbursement %s: %s ₹%s (%s)", c.ID, e.Name, c.Amount, c.Kind), "Approve or reject on People → My team.")
	return c, nil
}

// DecideClaim approves or rejects: the person's manager, Payroll or HR.
func (o *Ops) DecideClaim(ctx context.Context, tenant, actor, id string, approve bool) (Claim, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return Claim{}, err
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return Claim{}, err
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cClaim, id, actor, func(c *Claim) error {
		if c.Employee == sc.self || !(sc.team[c.Employee] || sc.role == RolePayroll || sc.role == RoleHR || sc.role == RoleAdmin) {
			return fmt.Errorf("%w: the person's manager or payroll decides", ErrForbidden)
		}
		if c.Status != "Pending" {
			return fmt.Errorf("%w: already %s", ErrState, strings.ToLower(c.Status))
		}
		c.Status, c.DecidedBy = map[bool]string{true: "Approved", false: "Rejected"}[approve], actor
		return nil
	})
	if err == nil {
		o.closeWork(ctx, tenant, actor, wHR, "claim:"+c.ID, strings.ToLower(c.Status))
		o.log(ctx, tenant, actor, "payroll.claim."+strings.ToLower(c.Status), c.ID, nil)
	}
	return c, err
}

// Loan is a salary advance or loan, recovered from salary.
type Loan struct {
	ID         string       `json:"id"`
	Employee   string       `json:"employee"`
	Kind       string       `json:"kind"` // Advance, Loan
	Amount     money.Amount `json:"amount"`
	Instalment money.Amount `json:"instalment"`
	StartMonth string       `json:"start_month"`
	Recovered  money.Amount `json:"recovered"`
	Recoveries []string     `json:"recoveries"` // month:amount
	Status     string       `json:"status"`     // Pending, Active, Closed, Rejected
	MadeBy     string       `json:"made_by"`
	ApprovedBy string       `json:"approved_by,omitempty"`
	PaidOn     string       `json:"paid_on,omitempty"`
	Entry      string       `json:"entry,omitempty"`
}

func (l Loan) left() money.Amount { return l.Amount - l.Recovered }

// GiveAdvance records an advance or loan to be approved. Payroll.
func (o *Ops) GiveAdvance(ctx context.Context, tenant, actor string, l Loan) (Loan, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return l, err
	}
	if err := o.require(ctx, tenant, actor, "Giving salary advances and loans", RolePayroll); err != nil {
		return l, err
	}
	l.Employee = strings.ToUpper(l.Employee)
	if _, err := o.employee(ctx, tenant, l.Employee); err != nil {
		return l, invalid("no employee %s", l.Employee)
	}
	if l.Kind != "Advance" && l.Kind != "Loan" {
		return l, invalid("kind is Advance or Loan")
	}
	if l.Amount <= 0 || l.Instalment <= 0 || l.Instalment > l.Amount {
		return l, invalid("give the amount and a monthly instalment no larger than it")
	}
	if _, err := time.Parse("2006-01", l.StartMonth); err != nil {
		return l, invalid("the first recovery month is YYYY-MM")
	}
	n, err := o.next(ctx, tenant, "pay-loan", actor)
	if err != nil {
		return l, err
	}
	l.ID, l.Status, l.MadeBy, l.Recovered, l.Recoveries = fmt.Sprintf("LN-%05d", n), "Pending", actor, 0, []string{}
	if err := docs.Upsert(ctx, o.Docs, tenant, cLoan, l.ID, l, actor); err != nil {
		return l, err
	}
	o.log(ctx, tenant, actor, "payroll.loan.made", l.ID, map[string]any{"employee": l.Employee, "amount": l.Amount.String()})
	return l, nil
}

// ApproveAdvance is the second person's approval; the firm pays the money
// from its own bank (posted to the books). Payroll or HR, not the maker.
func (o *Ops) ApproveAdvance(ctx context.Context, tenant, actor, id, paidOn string) (Loan, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return Loan{}, err
	}
	if err := o.require(ctx, tenant, actor, "Approving salary advances and loans", RolePayroll, RoleHR); err != nil {
		return Loan{}, err
	}
	if !validDay(paidOn) {
		return Loan{}, invalid("give the date the money was paid")
	}
	ps := o.paySettings(ctx, tenant)
	return docs.Update(ctx, o.Docs, tenant, cLoan, id, actor, func(l *Loan) error {
		if l.Status != "Pending" {
			return fmt.Errorf("%w: %s is %s", ErrState, l.ID, strings.ToLower(l.Status))
		}
		if l.MadeBy == actor {
			return fmt.Errorf("%w: a second person approves an advance", ErrForbidden)
		}
		e, _, err := o.App.Ledger.Post(ctx, ledgerEntry(tenant, "LOAN:"+l.ID, paidOn, fmt.Sprintf("%s to %s (%s)", l.Kind, l.Employee, l.ID), "PAYROLL-LOAN", l.ID, actor,
			ledgerLine(AcctStaffAdvances, l.Amount, 0), ledgerLine(ps.BankAccount, 0, l.Amount)))
		if err != nil {
			return err
		}
		l.Status, l.ApprovedBy, l.PaidOn, l.Entry = "Active", actor, paidOn, e.ID
		o.log(ctx, tenant, actor, "payroll.loan.approved", l.ID, map[string]any{"entry": e.ID})
		return nil
	})
}

// ---- the calculation ----

// PayLine is one person's pay for a month.
type PayLine struct {
	Employee    string       `json:"employee"`
	Name        string       `json:"name"`
	Department  string       `json:"department,omitempty"`
	Branch      string       `json:"branch,omitempty"`
	PAN         string       `json:"pan,omitempty"`
	UAN         string       `json:"uan,omitempty"`
	ESIC        string       `json:"esic,omitempty"`
	State       string       `json:"state,omitempty"`
	Regime      string       `json:"regime"`
	MonthDays   int          `json:"month_days"`
	ServiceDays float64      `json:"service_days"`
	LOP         float64      `json:"lop"`
	PaidDays    float64      `json:"paid_days"`
	Earnings    []PayItem    `json:"earnings"`
	Deductions  []PayItem    `json:"deductions"`
	Gross       money.Amount `json:"gross"`         // every earning
	Reimburse   money.Amount `json:"reimburse"`     // part of gross: not taxable, not wages
	Taxable     money.Amount `json:"taxable"`       // taxable salary this month
	PFWages     money.Amount `json:"pf_wages"`      // EPF wages (after any ceiling)
	PFWagesFull money.Amount `json:"pf_wages_full"` // wages before the ceiling
	EPSWages    money.Amount `json:"eps_wages"`
	EDLIWages   money.Amount `json:"edli_wages"`
	EmpPF       money.Amount `json:"emp_pf"`
	ErEPF       money.Amount `json:"er_epf"` // employer's share to EPF (after EPS)
	ErEPS       money.Amount `json:"er_eps"`
	EDLI        money.Amount `json:"edli"`
	PFAdmin     money.Amount `json:"pf_admin"`
	ESIWages    money.Amount `json:"esi_wages"`
	EmpESI      money.Amount `json:"emp_esi"`
	ErESI       money.Amount `json:"er_esi"`
	PT          money.Amount `json:"pt"`
	TDS         money.Amount `json:"tds"`
	Recovery    money.Amount `json:"recovery"` // advances and loans
	Other       money.Amount `json:"other"`    // other deductions
	Net         money.Amount `json:"net"`
	Hold        bool         `json:"hold,omitempty"`
	Tax         TaxWorking   `json:"tax"`
	Bank        SalaryBank   `json:"bank"`
	Email       string       `json:"email,omitempty"`
	Claims      []string     `json:"claims,omitempty"`
	Loans       []string     `json:"loans,omitempty"`  // id:amount recovered
	Encash      []string     `json:"encash,omitempty"` // leave ledger entries paid
	Problems    []string     `json:"problems"`
}

// TaxWorking is the year's tax as projected this month.
type TaxWorking struct {
	Regime      string       `json:"regime"`
	GrossYear   money.Amount `json:"gross_year"`
	HRAExempt   money.Amount `json:"hra_exempt"`
	StdDed      money.Amount `json:"std_deduction"`
	PTYear      money.Amount `json:"pt_year"`
	Ded80C      money.Amount `json:"ded_80c"`
	Ded80D      money.Amount `json:"ded_80d"`
	DedNPS      money.Amount `json:"ded_nps"`
	HomeLoan    money.Amount `json:"home_loan"`
	OtherIncome money.Amount `json:"other_income"`
	Taxable     money.Amount `json:"taxable_income"`
	SlabTax     money.Amount `json:"slab_tax"`
	Rebate      money.Amount `json:"rebate"`
	Surcharge   money.Amount `json:"surcharge"`
	Cess        money.Amount `json:"cess"`
	TaxYear     money.Amount `json:"tax_year"`
	PaidBefore  money.Amount `json:"paid_before"` // TDS this year before this month (and an earlier employer's)
	MonthsLeft  int          `json:"months_left"` // including this month
}

func rupeesP(n int64) money.Amount { return money.Amount(n * 100) }

func pctAmt(a money.Amount, pct string) money.Dec {
	d, _ := a.Dec().Mul(dec(pct)).Quo(money.DecInt(100))
	return d
}

func roundR(d money.Dec) money.Amount { return d.Round(money.UnitRupee) }

func ceilR(d money.Dec) money.Amount {
	return d.CeilRupee().Round(money.UnitRupee)
}

// ptFor is the professional tax for a month's gross in a state.
func ptFor(st Statutory, state, gender string, gross money.Amount, month time.Month) money.Amount {
	slabs := st.PT[state]
	g := gender
	if g == "" || g == "O" {
		g = "M"
	}
	has := false
	for _, s := range slabs {
		has = has || s.Gender != ""
	}
	for _, s := range slabs {
		if has && s.Gender != "" && s.Gender != g {
			continue
		}
		if s.Upto == 0 || gross <= rupeesP(s.Upto) {
			if month == time.February && s.Feb > 0 {
				return rupeesP(s.Feb)
			}
			return rupeesP(s.Amount)
		}
	}
	return 0
}

// slabTax is tax on income by slabs.
func slabTax(income money.Amount, slabs []TaxSlab, firstExempt int64) money.Dec {
	t := money.DecInt(0)
	lo := money.Amount(0)
	for i, s := range slabs {
		hi := rupeesP(s.Upto)
		if i == 0 && firstExempt > 0 {
			hi = rupeesP(firstExempt)
		}
		if i > 0 && firstExempt > 0 && s.Upto != 0 && rupeesP(s.Upto) <= rupeesP(firstExempt) {
			continue
		}
		if income <= lo {
			break
		}
		top := income
		if s.Upto != 0 && hi < income {
			top = hi
		}
		if top > lo {
			t = t.Add(pctAmt(top-lo, s.Pct))
		}
		if s.Upto != 0 {
			lo = hi
		}
	}
	return t
}

// taxOn works out a year's tax on taxable income (rounded to ₹10).
func taxOn(r TaxRegime, income money.Amount, age int) (slab, rebate, surcharge, cess, total money.Amount) {
	first := int64(0)
	if age >= 80 && r.SuperExempt > 0 {
		first = r.SuperExempt
	} else if age >= 60 && r.SeniorExempt > 0 {
		first = r.SeniorExempt
	}
	base := slabTax(income, r.Slabs, first)
	slab = roundR(base)
	t := slab
	if r.RebateUpto > 0 && income <= rupeesP(r.RebateUpto) {
		rebate = min(t, rupeesP(r.RebateMax))
	} else if r.MarginalRelief && r.RebateUpto > 0 && t > income-rupeesP(r.RebateUpto) {
		// Just above the limit: tax is at most the income above it.
		rebate = t - (income - rupeesP(r.RebateUpto))
	}
	t -= rebate
	// Surcharge with marginal relief: tax plus surcharge is at most the
	// tax at the band's start plus the income above it.
	for i := len(r.Surcharge) - 1; i >= 0; i-- {
		b := r.Surcharge[i]
		if income > rupeesP(b.Above) {
			s := roundR(pctAmt(t, b.Pct))
			at := rupeesP(b.Above)
			prevPct := "0"
			if i > 0 {
				prevPct = r.Surcharge[i-1].Pct
			}
			atTax := roundR(slabTax(at, r.Slabs, first))
			capTotal := atTax + roundR(pctAmt(atTax, prevPct)) + (income - at)
			if t+s > capTotal {
				s = max(0, capTotal-t)
			}
			surcharge = s
			break
		}
	}
	cess = roundR(pctAmt(t+surcharge, r.CessPct))
	total = t + surcharge + cess
	total = ((total + 500) / 1000) * 1000 // nearest ₹10 (in paise)
	return
}

func ageOn(dob, day string) int {
	b, e1 := time.Parse("2006-01-02", dob)
	d, e2 := time.Parse("2006-01-02", day)
	if e1 != nil || e2 != nil {
		return 0
	}
	a := d.Year() - b.Year()
	if d.YearDay() < b.YearDay() {
		a--
	}
	return a
}

// fyMonths are the months of a financial year, April first.
func fyMonths(fy string) []string {
	var y int
	fmt.Sscanf(fy, "%d", &y)
	out := make([]string, 12)
	for i := range out {
		out[i] = time.Date(y, time.April+time.Month(i), 1, 0, 0, 0, 0, ist).Format("2006-01")
	}
	return out
}

// ytd is what earlier months of the year (approved runs) paid a person.
type ytd struct {
	Taxable, HRA, Basic, PF, PT, TDS money.Amount
}

func (o *Ops) yearToDate(ctx context.Context, tenant, code, month string) ytd {
	var y ytd
	fy := finYear(month + "-01")
	for _, m := range fyMonths(fy) {
		if m >= month {
			break
		}
		r, _, err := docs.Get[PayRun](ctx, o.Docs, tenant, cPayRun, m)
		if err != nil || (r.Status != "Approved" && r.Status != "Paid") {
			continue
		}
		for _, l := range r.Lines {
			if l.Employee != code {
				continue
			}
			y.Taxable += l.Taxable
			y.PF += l.EmpPF
			y.PT += l.PT
			y.TDS += l.TDS
			for _, x := range l.Earnings {
				switch x.Code {
				case "HRA":
					y.HRA += x.Amount
				case "BASIC", "DA":
					y.Basic += x.Amount
				}
			}
		}
	}
	return y
}

// payCtx carries what a run needs.
type payCtx struct {
	st     Statutory
	ps     PaySettings
	pol    HRPolicy
	set    Settings
	leaves []LeaveRequest
	month  string
	first  string
	last   string
	mdays  int
}

// lopDays counts loss-of-pay days in the month from leave without pay and
// absences (marked ones; unmarked days too when the policy says so).
func (o *Ops) lopDays(ctx context.Context, tenant string, pc payCtx, e Employee, from, to string) float64 {
	as, _ := docs.List[Attendance](ctx, o.Docs, tenant, cAttend, e.Code+"|"+pc.month)
	byDay := map[string]Attendance{}
	for _, a := range as {
		byDay[a.Date] = a
	}
	lop := 0.0
	for _, d := range monthDays(pc.month) {
		if d < from || d > to || o.dayKind(pc.pol, pc.set, e, d) != "working" {
			continue
		}
		leave, paid, half := false, true, false
		for _, l := range pc.leaves {
			if l.Employee == e.Code && l.Status == "Approved" && l.From <= d && d <= l.To {
				leave, half = true, l.Half
				if lt, ok := leaveType(pc.pol, l.Type); ok && !lt.Paid {
					paid = false
				}
			}
		}
		a, has := byDay[d]
		switch {
		case leave && !paid:
			if half {
				lop += 0.5
			} else {
				lop++
			}
		case leave && half && has && a.Status == "Absent":
			lop += 0.5
		case leave:
		case has && a.Status == "Absent" && a.Marked != "":
			lop++
		case has && a.Status == "Half day":
			lop += 0.5
		case !has && pc.pol.AbsentIsLOP && d <= o.today():
			lop++
		case has && a.Status == "Absent" && pc.pol.AbsentIsLOP:
			lop++
		}
	}
	return lop
}

// payLine works out a person's month.
func (o *Ops) payLine(ctx context.Context, tenant string, pc payCtx, e Employee) (PayLine, bool) {
	l := PayLine{Employee: e.Code, Name: e.Name, Department: e.Department, Branch: e.Branch, PAN: e.PAN, UAN: e.UAN, ESIC: e.ESIC, MonthDays: pc.mdays, Bank: e.Bank, Email: e.Email,
		Earnings: []PayItem{}, Deductions: []PayItem{}, Problems: []string{}}
	from, to := pc.first, pc.last
	if e.JoinedOn > from {
		from = e.JoinedOn
	}
	if e.LeftOn != "" && e.LeftOn < to {
		to = e.LeftOn
	}
	if from > to {
		return l, false
	}
	s, ok := o.structureOn(ctx, tenant, e.Code, to)
	if !ok {
		l.Problems = append(l.Problems, "no salary structure")
		return l, true
	}
	l.ServiceDays = float64(daysBetween(from, to) + 1)
	in, _, _ := docs.Get[PayInput](ctx, o.Docs, tenant, cPayInput, pc.month+"|"+e.Code)
	if in.LOP != nil {
		l.LOP = *in.LOP
	} else {
		l.LOP = o.lopDays(ctx, tenant, pc, e, from, to)
	}
	l.PaidDays = max(0, l.ServiceDays-l.LOP)
	l.Hold = in.Hold
	l.State = firstOf(e.State, pc.ps.DefaultState)
	month := pc.month
	mt, _ := time.Parse("2006-01", month)
	// Earned components.
	frac := func(a money.Amount) money.Amount {
		d, _ := a.Dec().Mul(dec(fmt.Sprintf("%.1f", l.PaidDays))).Quo(money.DecInt(int64(pc.mdays)))
		return roundR(d)
	}
	var pfWage, esiWage, remun money.Amount
	for _, c := range s.Components {
		a := frac(c.Amount)
		l.Earnings = append(l.Earnings, PayItem{Code: c.Code, Name: c.Name, Amount: a, PFWage: c.PFWage, ESIWage: c.ESIWage, Exempt: c.Exempt})
	}
	for _, x := range in.Arrears {
		x.Name = "Arrears: " + x.Name
		x.Code = "ARR-" + firstOf(x.Code, "PAY")
		l.Earnings = append(l.Earnings, x)
	}
	l.Earnings = append(l.Earnings, in.Earnings...)
	// Leave encashment not yet paid.
	led, _ := docs.List[LeaveEntry](ctx, o.Docs, tenant, cLeaveLedger, e.Code+"|")
	var enc float64
	for _, x := range led {
		if x.Kind == "encash" && x.PaidIn == "" {
			enc += x.Days
			l.Encash = append(l.Encash, x.ID)
		}
	}
	if enc > 0 {
		perDay, _ := (s.comp("BASIC") + s.comp("DA")).Dec().Quo(money.DecInt(int64(pc.ps.EncashDivisor)))
		l.Earnings = append(l.Earnings, PayItem{Code: "LEAVE-ENCASH", Name: fmt.Sprintf("Leave encashment (%.1f days)", enc), Amount: roundR(perDay.Mul(dec(fmt.Sprintf("%.1f", enc))))})
	}
	// Approved claims not yet paid.
	cs, _ := docs.List[Claim](ctx, o.Docs, tenant, cClaim, "")
	for _, c := range cs {
		if c.Employee == e.Code && c.Status == "Approved" {
			l.Reimburse += c.Amount
			l.Claims = append(l.Claims, c.ID)
		}
	}
	if l.Reimburse > 0 {
		l.Earnings = append(l.Earnings, PayItem{Code: "REIMB", Name: "Reimbursements", Amount: l.Reimburse, Exempt: true})
	}
	for _, x := range l.Earnings {
		l.Gross += x.Amount
		if x.Code == "REIMB" {
			continue
		}
		remun += x.Amount
		if !x.Exempt {
			l.Taxable += x.Amount
		}
		if x.PFWage {
			pfWage += x.Amount
		}
		if x.ESIWage {
			esiWage += x.Amount
		}
	}
	st := pc.st
	// PF.
	if !e.NoPF {
		w := pfWage
		if st.WageRule50 && remun > 0 {
			if excl := remun - pfWage; excl*2 > remun {
				w += excl - remun/2
			}
		}
		l.PFWagesFull = w
		l.PFWages = w
		if !e.PFFullWages && w > rupeesP(st.PFCeiling) {
			l.PFWages = rupeesP(st.PFCeiling)
		}
		l.EPSWages = min(w, rupeesP(st.EPSCeiling))
		if !e.EPSMember || (e.DOB != "" && ageOn(e.DOB, pc.first) >= st.EPSUpToAge) {
			l.EPSWages = 0
		}
		l.EDLIWages = min(w, rupeesP(st.EDLICeiling))
		l.EmpPF = roundR(pctAmt(l.PFWages, st.PFEmployee))
		er := roundR(pctAmt(l.PFWages, st.PFEmployer))
		l.ErEPS = roundR(pctAmt(l.EPSWages, st.EPS))
		l.ErEPF = er - l.ErEPS
		l.EDLI = roundR(pctAmt(l.EDLIWages, st.EDLI))
		l.PFAdmin = roundR(pctAmt(l.PFWages, st.PFAdmin))
		if e.UAN == "" {
			l.Problems = append(l.Problems, "no UAN (needed for the PF return)")
		}
	}
	// ESI: covered when the full-month ESI wages of the structure in force
	// at the start of the contribution period (April or October), or on
	// joining, are within the threshold; covered to the period's end.
	ps := esiPeriodStart(month)
	if e.JoinedOn > ps {
		ps = e.JoinedOn
	}
	if s0, ok := o.structureOn(ctx, tenant, e.Code, ps); ok {
		// ESI "wages" are all remuneration (ESI Act s.2(22)): eligibility
		// is on the whole monthly pay; the contribution on the components
		// marked for ESI.
		full := s0.gross()
		limit := st.ESIThreshold
		if e.Disabled {
			limit = st.ESIDisabled
		}
		if full <= rupeesP(limit) {
			l.ESIWages = esiWage
			l.ErESI = ceilR(pctAmt(esiWage, st.ESIEmployer))
			l.EmpESI = ceilR(pctAmt(esiWage, st.ESIEmployee))
			if l.PaidDays > 0 {
				if daily, _ := esiWage.Dec().Quo(dec(fmt.Sprintf("%.1f", l.PaidDays))); daily.Cmp(money.DecInt(st.ESIExemptDaily)) <= 0 {
					l.EmpESI = 0 // low-wage employees pay no employee share
				}
			}
			if e.ESIC == "" {
				l.Problems = append(l.Problems, "no ESIC number (needed for the ESI return)")
			}
		}
	}
	// Professional tax.
	if _, ok := st.PT[l.State]; ok {
		l.PT = ptFor(st, l.State, e.Gender, remun, mt.Month())
	}
	// Recoveries.
	ls, _ := docs.List[Loan](ctx, o.Docs, tenant, cLoan, "")
	for _, x := range ls {
		if x.Employee != e.Code || x.Status != "Active" || x.StartMonth > month {
			continue
		}
		r := min(x.Instalment, x.left())
		if e.LeftOn != "" && e.LeftOn <= pc.last {
			r = x.left() // the last salary recovers what is left
		}
		if r > 0 {
			l.Recovery += r
			l.Loans = append(l.Loans, fmt.Sprintf("%s:%d", x.ID, int64(r)))
			l.Deductions = append(l.Deductions, PayItem{Code: "RECOVERY", Name: x.Kind + " recovery (" + x.ID + ")", Amount: r})
		}
	}
	for _, x := range in.Deductions {
		l.Other += x.Amount
		l.Deductions = append(l.Deductions, x)
	}
	// TDS.
	l.Regime = "new"
	decl, _, derr := docs.Get[TaxDecl](ctx, o.Docs, tenant, cTaxDecl, e.Code+"|"+finYear(pc.first))
	if derr == nil && decl.Regime != "" {
		l.Regime = decl.Regime
	} else if e.TaxRegime != "" {
		l.Regime = e.TaxRegime
	}
	l.Tax = o.tdsFor(ctx, tenant, pc, e, s, l, decl, derr == nil)
	l.TDS = max(0, l.Tax.TaxYear-l.Tax.PaidBefore)
	if l.Tax.MonthsLeft > 0 {
		d, _ := l.TDS.Dec().Quo(money.DecInt(int64(l.Tax.MonthsLeft)))
		l.TDS = roundR(d)
	}
	if l.TDS > 0 && e.PAN == "" {
		// Without a PAN, TDS is at the higher of the average rate and 20%
		// (s.397 of the 2025 Act, earlier 206AA).
		l.TDS = max(l.TDS, roundR(pctAmt(l.Taxable, "20")))
		l.Problems = append(l.Problems, "no PAN: TDS at 20% at least")
	}
	// Deductions and net.
	add := func(code, name string, a money.Amount) {
		if a > 0 {
			l.Deductions = append([]PayItem{{Code: code, Name: name, Amount: a}}, l.Deductions...)
		}
	}
	add("TDS", "Income tax (TDS)", l.TDS)
	add("PT", "Professional tax", l.PT)
	add("ESI", "ESI", l.EmpESI)
	add("PF", "Provident fund", l.EmpPF)
	l.Net = l.Gross - l.EmpPF - l.EmpESI - l.PT - l.TDS - l.Recovery - l.Other
	if l.Net < 0 {
		l.Problems = append(l.Problems, "deductions are more than the pay")
	}
	if l.Net > 0 && (l.Bank.Account == "" || l.Bank.IFSC == "") {
		l.Problems = append(l.Problems, "no salary bank account")
	}
	return l, true
}

func esiPeriodStart(month string) string {
	t, _ := time.Parse("2006-01", month)
	if t.Month() >= time.April && t.Month() <= time.September {
		return fmt.Sprintf("%d-04-01", t.Year())
	}
	if t.Month() >= time.October {
		return fmt.Sprintf("%d-10-01", t.Year())
	}
	return fmt.Sprintf("%d-10-01", t.Year()-1)
}

// tdsFor projects the year: what earlier months paid, this month, and the
// rest of the year at the current structure; less exemptions and
// deductions (old regime), the tax on it, less TDS already deducted, over
// the months left.
func (o *Ops) tdsFor(ctx context.Context, tenant string, pc payCtx, e Employee, s SalaryStructure, l PayLine, decl TaxDecl, hasDecl bool) TaxWorking {
	st := pc.st
	r := st.Tax[l.Regime]
	w := TaxWorking{Regime: l.Regime}
	fy := finYear(pc.first)
	months := fyMonths(fy)
	k := 0
	for i, m := range months {
		if m == pc.month {
			k = i
		}
	}
	// Months after this one the person is expected to be paid.
	var rest []string
	for _, m := range months[k+1:] {
		if e.LeftOn != "" && e.LeftOn < m+"-01" {
			break
		}
		rest = append(rest, m)
	}
	w.MonthsLeft = 1 + len(rest)
	y := o.yearToDate(ctx, tenant, e.Code, pc.month)
	amt := decl.Declared
	if decl.Verified != nil {
		amt = *decl.Verified
	} else if hasDecl && pc.ps.ProofCutoff > 0 {
		// From the cut-off month only proved amounts count.
		mt, _ := time.Parse("2006-01", pc.month)
		if c := time.Month(pc.ps.ProofCutoff); mt.Month() >= c && mt.Month() <= time.March || (c > time.March && mt.Month() >= c) {
			amt = TaxAmounts{PrevIncome: amt.PrevIncome, PrevTDS: amt.PrevTDS, PrevPF: amt.PrevPF, PrevPT: amt.PrevPT}
		}
	}
	var fullTaxable, fullHRA, fullBasic money.Amount
	for _, c := range s.Components {
		if !c.Exempt {
			fullTaxable += c.Amount
		}
		switch c.Code {
		case "HRA":
			fullHRA += c.Amount
		case "BASIC", "DA":
			fullBasic += c.Amount
		}
	}
	n := money.Amount(len(rest))
	w.GrossYear = y.Taxable + l.Taxable + fullTaxable*n + amt.PrevIncome
	var curHRA, curBasic money.Amount
	for _, x := range l.Earnings {
		switch x.Code {
		case "HRA":
			curHRA += x.Amount
		case "BASIC", "DA":
			curBasic += x.Amount
		}
	}
	// Professional tax for the year.
	w.PTYear = y.PT + l.PT + amt.PrevPT
	if _, ok := st.PT[l.State]; ok {
		for _, m := range rest {
			mt, _ := time.Parse("2006-01", m)
			w.PTYear += ptFor(st, l.State, e.Gender, s.gross(), mt.Month())
		}
	}
	w.StdDed = min(rupeesP(r.StdDeduction), w.GrossYear)
	inc := w.GrossYear - w.StdDed
	if l.Regime == "old" {
		inc -= w.PTYear
		hra, basic := y.HRA+curHRA+fullHRA*n, y.Basic+curBasic+fullBasic*n
		if amt.RentMonthly > 0 && hra > 0 {
			inService := money.Amount(0)
			for _, m := range months {
				if m+"-01" >= e.JoinedOn[:7]+"-01" && (e.LeftOn == "" || m+"-01" <= e.LeftOn) {
					inService++
				}
			}
			rent := amt.RentMonthly * inService
			pct := "40"
			if amt.Metro {
				pct = "50"
			}
			ex := min(hra, rent-roundR(pctAmt(basic, "10")), roundR(pctAmt(basic, pct)))
			w.HRAExempt = max(0, ex)
		}
		pf := y.PF + l.EmpPF + l.EmpPF*n + amt.PrevPF
		w.Ded80C = min(amt.C80+pf, rupeesP(st.Max80C))
		w.Ded80D = amt.D80
		w.DedNPS = min(amt.NPS1B, rupeesP(st.Max80CCD1B))
		w.HomeLoan = min(amt.HomeLoan, rupeesP(st.MaxHomeLoan))
		inc -= w.HRAExempt + w.Ded80C + w.Ded80D + w.DedNPS + w.HomeLoan
	}
	w.OtherIncome = amt.OtherIncome
	inc += w.OtherIncome
	inc = max(0, inc)
	w.Taxable = ((inc + 500) / 1000) * 1000 // nearest ₹10
	age := 0
	if e.DOB != "" {
		_, end, _ := leaveYear(HRPolicy{LeaveYear: "financial"}, pc.first)
		age = ageOn(e.DOB, end)
	}
	w.SlabTax, w.Rebate, w.Surcharge, w.Cess, w.TaxYear = taxOn(r, w.Taxable, age)
	w.PaidBefore = y.TDS + amt.PrevTDS
	return w
}
