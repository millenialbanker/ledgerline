package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- Mutual fund distribution ----
//
// A broker can offer mutual funds to its clients in two ways, and a firm
// may do both:
//
//   - Regular plans, as a distributor: the firm's AMFI registration (ARN),
//     an EUIN for each employee who deals with investors (NISM Series V-A),
//     empanelment with each AMC. The firm earns trail commission only.
//   - Direct plans, as an execution-only platform: SEBI's EOP framework
//     (circular SEBI/HO/IMD/IMD-PoD-1/P/CIR/2023/86). A stockbroker's
//     platform for its own broking clients is exempt from registering as
//     an EOP (para 1.2); the firm's compliance officer records that basis
//     in the MF settings before Direct plans are offered.
//
// Each investor, taken as the family by PAN, uses one route only: the
// first order or SIP fixes it, and only compliance can change it.
//
// Orders, SIPs and client registrations go to an exchange platform (BSE
// StAR MF, NSE MF Invest) through mfPlatform (mfplatform.go). Client money
// never passes through the firm: the investor pays the clearing
// corporation directly, by the platform's payment or authorisation link or
// by a mandate in the clearing corporation's favour, and redemptions are
// paid by the AMC to the investor's bank. Nothing here posts to the client
// ledger, and a trading-account balance can never pay for a purchase.
// Units go to the investor's demat account (or a folio at the RTA); the
// units ledger here is a record of them, built from allotments and RTA
// feeds, not a holding the firm keeps.

const (
	cMFSet    = "mfset"    // firm MF settings: "main"
	cMFScheme = "mfscheme" // key: ISIN
	cMFProf   = "mfprof"   // key: UCC
	cMFRoute  = "mfroute"  // key: PAN
	cMFUnit   = "mfunit"   // key: UCC|sequence
	cMFState  = "mfstate"  // NAV loader and platform polling state
)

// Routes and platforms.
const (
	MFRegular = "Regular"
	MFDirect  = "Direct"
	MFBoth    = "Both"
	MFBSE     = "BSE"
	MFNSE     = "NSE"
	// MFNominationFrom: new single-holder folios need a nomination or an
	// opt-out from this date.
	MFNominationFrom = "2026-09-01"
	// MFMandateMax is the most a mandate may allow per debit (₹1 crore).
	MFMandateMax = money.Amount(1_00_00_000_00)
)

// MFPlatformNames are the exchange platforms, with their clearing
// corporation (the only payee a mandate or payment may name).
var MFPlatformNames = map[string][2]string{
	MFBSE: {"BSE StAR MF", "Indian Clearing Corporation Ltd"},
	MFNSE: {"NSE MF Invest", "NSE Clearing Ltd"},
}

// MFEUIN is an employee's EUIN.
type MFEUIN struct {
	User      string `json:"user"`
	EUIN      string `json:"euin"`
	ValidTill string `json:"valid_till,omitempty"`
	NISM      string `json:"nism,omitempty"` // NISM Series V-A certificate number
}

// MFCutoff is a platform's order cut-off (HH:MM, IST).
type MFCutoff struct {
	Liquid string `json:"liquid"`
	Other  string `json:"other"`
}

// MFSettings are the firm's mutual fund settings.
type MFSettings struct {
	// Route is Regular, Direct or Both; empty means MF is not offered.
	Route        string   `json:"route"`
	ARN          string   `json:"arn,omitempty"`
	ARNValidTill string   `json:"arn_valid_till,omitempty"`
	EUINs        []MFEUIN `json:"euins"`
	// DirectBasis is why the firm may offer Direct plans: "Exempt: own
	// broking clients (EOP circular para 1.2)" or "EOP registered".
	DirectBasis       string `json:"direct_basis,omitempty"`
	DirectConfirmedBy string `json:"direct_confirmed_by,omitempty"`
	DirectConfirmedOn string `json:"direct_confirmed_on,omitempty"`
	// Platforms enabled: BSE, NSE.
	Platforms []string            `json:"platforms"`
	Cutoffs   map[string]MFCutoff `json:"cutoffs,omitempty"`
	// TrailPct is the trail commission expected, % a year of the value
	// held, by ISIN, AMC name or "*" (any other Regular plan).
	TrailPct map[string]string `json:"trail_pct,omitempty"`
	// SIPAlertDays: clients are reminded this many days before an
	// instalment (default 3; 0 turns reminders off).
	SIPAlertDays *int `json:"sip_alert_days,omitempty"`
	// NAVFetch fetches AMFI's NAV file every evening.
	NAVFetch  bool   `json:"nav_fetch"`
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// MFDirectBases are the recorded grounds for offering Direct plans.
var MFDirectBases = []string{"Exempt: own broking clients (EOP circular para 1.2)", "EOP registered"}

// mfDefaultCutoffs: NSE MF Invest's own cut-offs (its FAQ, July 2026);
// BSE StAR MF's are not in the public material read here, so AMFI's
// cut-offs stand until the firm enters BSE's.
var mfDefaultCutoffs = map[string]MFCutoff{MFNSE: {"13:00", "14:30"}, MFBSE: {"13:30", "15:00"}}

func (s MFSettings) alertDays() int {
	if s.SIPAlertDays == nil {
		return 3
	}
	return *s.SIPAlertDays
}

func (s MFSettings) cutoff(platform string, liquid bool) string {
	c, ok := s.Cutoffs[platform]
	if !ok || c.Other == "" {
		c = mfDefaultCutoffs[platform]
	}
	if liquid {
		return c.Liquid
	}
	return c.Other
}

func (s MFSettings) euinOf(user string) (MFEUIN, bool) {
	for _, e := range s.EUINs {
		if e.User == user {
			return e, true
		}
	}
	return MFEUIN{}, false
}

func (s MFSettings) enabled(platform string) bool { return contains(s.Platforms, platform) }

var (
	reARN  = regexp.MustCompile(`^ARN-[0-9]{1,10}$`)
	reEUIN = regexp.MustCompile(`^E[0-9]{6}$`)
	reHHMM = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
)

// MFSettingsGet returns the firm's MF settings.
func (o *Ops) MFSettingsGet(ctx context.Context, tenant string) (MFSettings, error) {
	s, _, err := docs.GetOr(ctx, o.Docs, tenant, cMFSet, "main", MFSettings{EUINs: []MFEUIN{}, Platforms: []string{}})
	if s.EUINs == nil {
		s.EUINs = []MFEUIN{}
	}
	if s.Platforms == nil {
		s.Platforms = []string{}
	}
	return s, err
}

// SaveMFSettings checks and stores the firm's MF settings.
func (o *Ops) SaveMFSettings(ctx context.Context, tenant, actor string, s MFSettings) (MFSettings, error) {
	if err := o.require(ctx, tenant, actor, "Changing mutual fund settings", RoleCompliance); err != nil {
		return s, err
	}
	old, _ := o.MFSettingsGet(ctx, tenant)
	switch s.Route {
	case "", MFRegular, MFDirect, MFBoth:
	default:
		return s, invalid("the route is Regular, Direct or Both")
	}
	s.ARN = strings.ToUpper(strings.TrimSpace(s.ARN))
	if s.Route == MFRegular || s.Route == MFBoth {
		if !reARN.MatchString(s.ARN) {
			return s, invalid("Regular plans need the firm's AMFI registration number (ARN-12345)")
		}
	}
	if s.ARN != "" && !reARN.MatchString(s.ARN) {
		return s, invalid("the ARN is written ARN-12345")
	}
	us, _ := o.Users(ctx, tenant)
	staff := map[string]bool{}
	for _, u := range us {
		staff[u.ID] = u.Role != RoleClient
	}
	seen := map[string]bool{}
	for i, e := range s.EUINs {
		e.EUIN = strings.ToUpper(strings.TrimSpace(e.EUIN))
		if !reEUIN.MatchString(e.EUIN) {
			return s, invalid("EUIN %q is E followed by 6 digits", e.EUIN)
		}
		if len(us) > 0 && !staff[e.User] {
			return s, invalid("%s is not a staff user", e.User)
		}
		if seen[e.User] {
			return s, invalid("%s has two EUINs", e.User)
		}
		seen[e.User] = true
		if e.ValidTill != "" && !validDate(e.ValidTill) {
			return s, invalid("EUIN validity is a date (YYYY-MM-DD)")
		}
		s.EUINs[i] = e
	}
	if s.EUINs == nil {
		s.EUINs = []MFEUIN{}
	}
	if s.Route == MFDirect || s.Route == MFBoth {
		if !contains(MFDirectBases, s.DirectBasis) {
			return s, invalid("Direct plans need the basis recorded: %s", strings.Join(MFDirectBases, ", or "))
		}
		if s.DirectBasis != old.DirectBasis || old.DirectConfirmedBy == "" {
			s.DirectConfirmedBy, s.DirectConfirmedOn = actor, o.now().Format("2006-01-02")
		} else {
			s.DirectConfirmedBy, s.DirectConfirmedOn = old.DirectConfirmedBy, old.DirectConfirmedOn
		}
	} else {
		s.DirectBasis, s.DirectConfirmedBy, s.DirectConfirmedOn = "", "", ""
	}
	ps := []string{}
	for _, p := range s.Platforms {
		p = strings.ToUpper(strings.TrimSpace(p))
		if _, ok := MFPlatformNames[p]; !ok {
			return s, invalid("platform %q is BSE or NSE", p)
		}
		if !contains(ps, p) {
			ps = append(ps, p)
		}
	}
	s.Platforms = ps
	for p, c := range s.Cutoffs {
		if _, ok := MFPlatformNames[p]; !ok || !reHHMM.MatchString(c.Liquid) || !reHHMM.MatchString(c.Other) {
			return s, invalid("cut-offs are given per platform as HH:MM")
		}
	}
	for k, v := range s.TrailPct {
		d, err := money.ParseDec(v)
		if err != nil || d.Sign() < 0 || d.Cmp(money.DecInt(3)) > 0 {
			return s, invalid("trail for %s must be a percentage between 0 and 3", k)
		}
	}
	if s.SIPAlertDays != nil && (*s.SIPAlertDays < 0 || *s.SIPAlertDays > 15) {
		return s, invalid("SIP reminders are sent 0 to 15 days ahead")
	}
	s.UpdatedBy, s.UpdatedAt = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cMFSet, "main", s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "mf.settings.saved", "mf", map[string]any{"route": s.Route, "arn": s.ARN, "platforms": s.Platforms, "direct_basis": s.DirectBasis, "euins": len(s.EUINs)})
	return s, nil
}

// ---- schemes ----

// MFScheme is one scheme option (one ISIN), from AMFI's NAV file and the
// platforms' scheme files.
type MFScheme struct {
	ISIN     string    `json:"isin"`
	AMFICode string    `json:"amfi_code,omitempty"`
	Name     string    `json:"name"`
	AMC      string    `json:"amc,omitempty"`
	Category string    `json:"category,omitempty"`
	Plan     string    `json:"plan"` // Direct or Regular
	NAV      money.Dec `json:"nav"`
	NAVDate  string    `json:"nav_date,omitempty"`
	// MonthEnd keeps the last NAV loaded in each month (YYYY-MM), for
	// trail commission accruals.
	MonthEnd map[string]string `json:"month_end,omitempty"`
	// Codes are each platform's scheme code for orders; NoPurchase,
	// NoRedeem and NoSIP list platforms whose scheme file says the
	// transaction is not allowed.
	Codes       map[string]string `json:"codes,omitempty"`
	NoPurchase  []string          `json:"no_purchase,omitempty"`
	NoRedeem    []string          `json:"no_redeem,omitempty"`
	NoSIP       []string          `json:"no_sip,omitempty"`
	MinPurchase money.Amount      `json:"min_purchase,omitempty"`
	AMCCode     map[string]string `json:"amc_code,omitempty"` // platform -> AMC code (NSE's amc_code for SIPs)
}

// Liquid reports whether the scheme is a liquid or overnight fund (an
// earlier cut-off).
func (s MFScheme) Liquid() bool {
	c := strings.ToLower(s.Category + " " + s.Name)
	return strings.Contains(c, "liquid") || strings.Contains(c, "overnight")
}

func mfPlanOf(name string) string {
	if strings.Contains(strings.ToLower(name), "direct") {
		return MFDirect
	}
	return MFRegular
}

// MFSchemes searches the scheme master: q matches name, ISIN or AMFI
// code; plan filters Direct or Regular; platform keeps schemes with a
// code there. At most limit results.
func (o *Ops) MFSchemes(ctx context.Context, tenant, q, plan, platform string, limit int) ([]MFScheme, error) {
	all, err := docs.List[MFScheme](ctx, o.Docs, tenant, cMFScheme, "")
	if err != nil {
		return nil, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	out := []MFScheme{}
	for _, s := range all {
		if plan != "" && s.Plan != plan {
			continue
		}
		if platform != "" && s.Codes[platform] == "" {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(s.Name), q) && !strings.EqualFold(s.ISIN, q) && s.AMFICode != q && !strings.Contains(strings.ToLower(s.AMC), q) {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (o *Ops) mfScheme(ctx context.Context, tenant, isin string) (MFScheme, error) {
	s, _, err := docs.Get[MFScheme](ctx, o.Docs, tenant, cMFScheme, strings.ToUpper(strings.TrimSpace(isin)))
	if errors.Is(err, docs.ErrNotFound) {
		return s, invalid("scheme %s is not in the scheme master: load AMFI's NAV file first", isin)
	}
	return s, err
}

// ---- client profiles ----

// MFNominee is a nominee on mutual fund folios (at most 3).
type MFNominee struct {
	Name     string `json:"name"`
	Relation string `json:"relation"` // the platform's relationship code or name
	Share    string `json:"share"`    // percent; all add to 100
	DOB      string `json:"dob,omitempty"`
	IDType   string `json:"id_type,omitempty"` // 1 PAN, 2 Aadhaar, 3 Driving licence, 4 OCI/Passport
	ID       string `json:"id,omitempty"`
	Guardian string `json:"guardian,omitempty"`
	Mobile   string `json:"mobile,omitempty"`
	Email    string `json:"email,omitempty"`
}

// MFPlatformReg is a client's registration (UCC) on one platform.
type MFPlatformReg struct {
	Status string `json:"status"` // Submitted, Registered, Failed
	At     string `json:"at"`
	By     string `json:"by"`
	Remark string `json:"remark,omitempty"`
}

// MFMandate is a debit mandate for XSIPs (and one-time purchases), always
// in favour of the platform's clearing corporation.
type MFMandate struct {
	ID         string       `json:"id"`
	Platform   string       `json:"platform"`
	PlatformID string       `json:"platform_id,omitempty"` // the platform's mandate number
	UMRN       string       `json:"umrn,omitempty"`
	Type       string       `json:"type"` // E-mandate, Physical NACH
	Limit      money.Amount `json:"limit"`
	Bank       string       `json:"bank"`
	Account    string       `json:"account"` // masked
	IFSC       string       `json:"ifsc"`
	Favour     string       `json:"favour"`
	From       string       `json:"from"`
	Till       string       `json:"till,omitempty"`
	Status     string       `json:"status"` // Pending, Approved, Rejected, Cancelled
	Requested  string       `json:"requested"`
	ExpectedBy string       `json:"expected_by"` // e-mandate 5 working days, physical NACH 20
	Remark     string       `json:"remark,omitempty"`
	By         string       `json:"by"`
}

// MFProfile is what mutual funds need about a client beyond the trading
// account.
type MFProfile struct {
	UCC  string `json:"ucc"`
	Name string `json:"name,omitempty"`
	PAN  string `json:"pan,omitempty"`
	// KRAStatus is the client's KRA status (from the KRA-STATUS file): On
	// hold or Rejected blocks every transaction.
	KRAStatus string `json:"kra_status,omitempty"`
	Mode      string `json:"mode"` // Demat or Physical (folio)
	// For the platforms' client registration.
	TaxStatus   string `json:"tax_status,omitempty"` // platform code: 01 individual, 04 company…
	Occupation  string `json:"occupation,omitempty"` // 01 business … 08 others
	Gender      string `json:"gender,omitempty"`     // M, F, O, T
	Address     string `json:"address,omitempty"`
	Pincode     string `json:"pincode,omitempty"`
	StateCode   string `json:"state_code,omitempty"`   // the platform's state code
	AccountType string `json:"account_type,omitempty"` // SB, CB, NE, NO
	// FATCA self-certification.
	FATCA          bool   `json:"fatca"`
	FATCAOn        string `json:"fatca_on,omitempty"`
	TaxResidentOut bool   `json:"tax_resident_outside_india,omitempty"`
	// Nomination for folios: up to 3 nominees, or an opt-out.
	Nominees   []MFNominee `json:"nominees"`
	NomOptOut  bool        `json:"nomination_opt_out"`
	NomOn      string      `json:"nomination_on,omitempty"`
	NomAuth    string      `json:"nomination_auth,omitempty"` // W wet signature, E eSign, O OTP
	FamilyHead string      `json:"family_head,omitempty"`     // PAN whose MF route this investor shares
	// Family and Route are filled when the profile is read.
	Family    []string                 `json:"family,omitempty"`
	Route     string                   `json:"route,omitempty"`
	Platforms map[string]MFPlatformReg `json:"platforms"`
	Mandates  []MFMandate              `json:"mandates"`
	UpdatedBy string                   `json:"updated_by,omitempty"`
	UpdatedAt string                   `json:"updated_at,omitempty"`
}

// mfProfileFrom starts a profile from the client's account.
func mfProfileFrom(c Client) MFProfile {
	p := MFProfile{UCC: c.UCC, Name: c.Name, PAN: c.PAN, KRAStatus: c.KRAStatus, Mode: "Physical", Platforms: map[string]MFPlatformReg{}, Mandates: []MFMandate{}, Nominees: []MFNominee{}, AccountType: "SB"}
	if c.Demat.ID != "" {
		p.Mode = "Demat"
	}
	switch strings.ToLower(c.Type) {
	case "individual", "":
		p.TaxStatus = "01"
	case "company", "corporate", "body corporate":
		p.TaxStatus = "04"
	}
	for i, n := range c.Nominees {
		if i == 3 {
			break
		}
		p.Nominees = append(p.Nominees, MFNominee{Name: n.Name, Relation: n.Relation, Share: n.Share, DOB: n.DOB, Guardian: n.Guardian, Mobile: n.Mobile, Email: n.Email})
	}
	return p
}

// MFProfile returns a client's MF profile, started from the client's
// account when none is saved, with the family and the route filled in.
func (o *Ops) MFProfile(ctx context.Context, tenant, ucc string) (MFProfile, error) {
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return MFProfile{}, err
	}
	p, _, err := docs.GetOr(ctx, o.Docs, tenant, cMFProf, ucc, mfProfileFrom(c))
	if err != nil {
		return p, err
	}
	p.Name, p.PAN, p.KRAStatus = c.Name, c.PAN, c.KRAStatus
	if p.Platforms == nil {
		p.Platforms = map[string]MFPlatformReg{}
	}
	if p.Mandates == nil {
		p.Mandates = []MFMandate{}
	}
	if p.Nominees == nil {
		p.Nominees = []MFNominee{}
	}
	p.Family, _ = o.mfFamily(ctx, tenant, c, p)
	p.Route, _ = o.mfFamilyRoute(ctx, tenant, p.Family)
	return p, nil
}

// MFProfileUpdate is what staff (or the client, for FATCA and
// nomination) may change on a profile.
type MFProfileUpdate struct {
	Mode           string      `json:"mode"`
	TaxStatus      string      `json:"tax_status"`
	Occupation     string      `json:"occupation"`
	Gender         string      `json:"gender"`
	Address        string      `json:"address"`
	Pincode        string      `json:"pincode"`
	StateCode      string      `json:"state_code"`
	AccountType    string      `json:"account_type"`
	FATCA          bool        `json:"fatca"`
	TaxResidentOut bool        `json:"tax_resident_outside_india"`
	Nominees       []MFNominee `json:"nominees"`
	NomOptOut      bool        `json:"nomination_opt_out"`
	NomAuth        string      `json:"nomination_auth"`
	FamilyHead     string      `json:"family_head"`
}

// SaveMFProfile updates a client's MF profile.
func (o *Ops) SaveMFProfile(ctx context.Context, tenant, actor, ucc string, u MFProfileUpdate) (MFProfile, error) {
	if err := o.require(ctx, tenant, actor, "Updating MF profiles", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return MFProfile{}, err
	}
	return o.saveMFProfile(ctx, tenant, actor, ucc, u)
}

func (o *Ops) saveMFProfile(ctx context.Context, tenant, actor, ucc string, u MFProfileUpdate) (MFProfile, error) {
	p, err := o.MFProfile(ctx, tenant, ucc)
	if err != nil {
		return p, err
	}
	keepMaskedIDs(p, &u)
	if u.Mode != "Demat" && u.Mode != "Physical" {
		return p, invalid("units are held in Demat or Physical (folio) mode")
	}
	if u.Mode == "Demat" {
		if c, _ := o.Client(ctx, tenant, ucc); c.Demat.ID == "" {
			return p, invalid("%s has no demat account on record", p.UCC)
		}
	}
	if u.Gender != "" && !contains([]string{"M", "F", "O", "T"}, u.Gender) {
		return p, invalid("gender is M, F, O or T")
	}
	if u.Occupation != "" && !regexp.MustCompile(`^0[1-8]$`).MatchString(u.Occupation) {
		return p, invalid("occupation is a code 01 to 08")
	}
	if u.TaxStatus != "" && !regexp.MustCompile(`^[0-9]{2}$`).MatchString(u.TaxStatus) {
		return p, invalid("tax status is the platform's two-digit code")
	}
	if u.Pincode != "" && !regexp.MustCompile(`^[1-9][0-9]{5}$`).MatchString(u.Pincode) {
		return p, invalid("PIN code is 6 digits")
	}
	if u.AccountType == "" {
		u.AccountType = "SB"
	}
	if !contains([]string{"SB", "CB", "NE", "NO"}, u.AccountType) {
		return p, invalid("bank account type is SB, CB, NE or NO")
	}
	if err := checkMFNominees(u.Nominees, u.NomOptOut); err != nil {
		return p, err
	}
	if u.NomAuth != "" && !contains([]string{"W", "E", "O"}, u.NomAuth) {
		return p, invalid("nomination is authenticated by W (wet signature), E (eSign) or O (OTP)")
	}
	u.FamilyHead = strings.ToUpper(strings.TrimSpace(u.FamilyHead))
	if u.FamilyHead != "" && !rePAN.MatchString(u.FamilyHead) {
		return p, invalid("the family head is given by PAN")
	}
	today := o.now().Format("2006-01-02")
	if u.FATCA && !p.FATCA {
		p.FATCAOn = today
	}
	if !u.FATCA {
		p.FATCAOn = ""
	}
	if u.NomOptOut != p.NomOptOut || len(u.Nominees) != len(p.Nominees) || p.NomOn == "" {
		p.NomOn = today
	}
	// A new family head can't move an investor whose family already has
	// a route onto a family with a different one.
	if u.FamilyHead != p.FamilyHead && p.Route != "" && u.FamilyHead != "" {
		if r, _, err := docs.Get[MFRouteLock](ctx, o.Docs, tenant, cMFRoute, u.FamilyHead); err == nil && r.Route != p.Route {
			return p, invalid("%s's family invests in %s plans and this investor in %s plans; compliance must change one route first", u.FamilyHead, r.Route, p.Route)
		}
	}
	p.Mode, p.TaxStatus, p.Occupation, p.Gender, p.Address, p.Pincode, p.StateCode, p.AccountType = u.Mode, u.TaxStatus, u.Occupation, u.Gender, strings.TrimSpace(u.Address), u.Pincode, strings.ToUpper(strings.TrimSpace(u.StateCode)), u.AccountType
	p.FATCA, p.TaxResidentOut, p.NomOptOut, p.NomAuth, p.FamilyHead = u.FATCA, u.TaxResidentOut, u.NomOptOut, u.NomAuth, u.FamilyHead
	p.Nominees = u.Nominees
	if p.Nominees == nil || u.NomOptOut {
		p.Nominees = []MFNominee{}
	}
	p.UpdatedBy, p.UpdatedAt = actor, o.now().Format(time.RFC3339)
	if err := o.putMFProfile(ctx, tenant, actor, p); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "mf.profile.saved", p.UCC, map[string]any{"mode": p.Mode, "fatca": p.FATCA, "nominees": len(p.Nominees), "opt_out": p.NomOptOut, "family_head": p.FamilyHead})
	return o.MFProfile(ctx, tenant, p.UCC)
}

// MaskMFProfile hides the PANs and nominee identity numbers of a profile
// for display. Saving a masked number back keeps the one on record.
func MaskMFProfile(p MFProfile) MFProfile {
	p.PAN = MaskPAN(p.PAN)
	fam := make([]string, len(p.Family))
	for i, x := range p.Family {
		fam[i] = MaskPAN(x)
	}
	p.Family = fam
	if p.FamilyHead != "" {
		p.FamilyHead = MaskPAN(p.FamilyHead)
	}
	ns := make([]MFNominee, len(p.Nominees))
	for i, n := range p.Nominees {
		n.ID = MaskAccount(n.ID)
		ns[i] = n
	}
	p.Nominees = ns
	return p
}

// keepMaskedIDs puts back nominee identity numbers (and the family head)
// that come back masked or empty from a screen.
func keepMaskedIDs(old MFProfile, u *MFProfileUpdate) {
	for i, n := range u.Nominees {
		for _, o := range old.Nominees {
			if !strings.EqualFold(strings.TrimSpace(n.Name), o.Name) || o.ID == "" {
				continue
			}
			if n.ID == "" || n.ID == MaskAccount(o.ID) {
				u.Nominees[i].ID = o.ID
				if n.IDType == "" {
					u.Nominees[i].IDType = o.IDType
				}
			}
		}
	}
	if old.FamilyHead != "" && u.FamilyHead == MaskPAN(old.FamilyHead) {
		u.FamilyHead = old.FamilyHead
	}
}

func (o *Ops) putMFProfile(ctx context.Context, tenant, actor string, p MFProfile) error {
	p.Family, p.Route = nil, ""
	return docs.Upsert(ctx, o.Docs, tenant, cMFProf, p.UCC, p, actor)
}

func checkMFNominees(ns []MFNominee, optOut bool) error {
	if optOut {
		if len(ns) > 0 {
			return invalid("an investor who opts out of nomination has no nominees")
		}
		return nil
	}
	if len(ns) > 3 {
		return invalid("mutual fund folios take at most 3 nominees")
	}
	if len(ns) == 0 {
		return nil
	}
	total := money.Dec{}
	for _, n := range ns {
		if strings.TrimSpace(n.Name) == "" || strings.TrimSpace(n.Relation) == "" {
			return invalid("each nominee needs a name and relationship")
		}
		d, err := money.ParseDec(n.Share)
		if err != nil || d.Sign() <= 0 {
			return invalid("each nominee's share is a percentage")
		}
		total = total.Add(d)
		if n.IDType != "" && !contains([]string{"1", "2", "3", "4"}, n.IDType) {
			return invalid("nominee identity type is 1 PAN, 2 Aadhaar, 3 driving licence or 4 OCI/passport")
		}
		if n.DOB != "" && !validDate(n.DOB) {
			return invalid("a nominee's date of birth is YYYY-MM-DD")
		}
	}
	if total.Cmp(money.DecInt(100)) != 0 {
		return invalid("nominee shares add up to %s%%, not 100%%", total.String())
	}
	return nil
}

// ---- one route per family ----

// MFRouteLock is the route (Regular or Direct) of every investor with a
// PAN in one family.
type MFRouteLock struct {
	PAN   string `json:"pan"`
	Route string `json:"route"`
	Since string `json:"since"`
	By    string `json:"by"`
	UCC   string `json:"ucc,omitempty"` // the account whose order fixed it
	Note  string `json:"note,omitempty"`
}

// mfFamily is the PANs of a client's family: the client's own, the family
// head's, and those of accounts linked under one client portal login (a
// declared family, as the exchanges allow), and of accounts that name the
// same family head.
func (o *Ops) mfFamily(ctx context.Context, tenant string, c Client, p MFProfile) ([]string, error) {
	pans := map[string]bool{}
	add := func(s string) {
		if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
			pans[s] = true
		}
	}
	add(c.PAN)
	add(p.FamilyHead)
	us, err := o.Users(ctx, tenant)
	if err != nil {
		return nil, err
	}
	cm, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, err
	}
	for _, u := range us {
		if u.Role != RoleClient || !contains(u.Clients, c.UCC) {
			continue
		}
		for _, x := range u.Clients {
			add(cm[x].PAN)
		}
	}
	if p.FamilyHead != "" || len(pans) > 0 {
		ps, _ := docs.List[MFProfile](ctx, o.Docs, tenant, cMFProf, "")
		for _, q := range ps {
			if q.FamilyHead != "" && (pans[q.FamilyHead] || pans[cm[q.UCC].PAN]) {
				add(cm[q.UCC].PAN)
				add(q.FamilyHead)
			}
		}
	}
	return sortedKeys(pans), nil
}

// mfFamilyRoute is the family's route, or "" before its first order. Two
// PANs of one family with different routes (linked after both invested)
// is an error compliance must resolve.
func (o *Ops) mfFamilyRoute(ctx context.Context, tenant string, family []string) (string, error) {
	route := ""
	for _, pan := range family {
		r, _, err := docs.Get[MFRouteLock](ctx, o.Docs, tenant, cMFRoute, pan)
		if err != nil || r.Route == "" {
			continue
		}
		if route != "" && r.Route != route {
			return "", fmt.Errorf("%w: this family invests in both Regular and Direct plans (PAN %s is %s); compliance must settle one route", ErrState, MaskPAN(pan), r.Route)
		}
		route = r.Route
	}
	return route, nil
}

// mfCheckRoute checks a plan against the firm's route and the family's,
// and returns the PANs to lock when the family has none yet.
func (o *Ops) mfCheckRoute(ctx context.Context, tenant string, set MFSettings, p MFProfile, plan string) ([]string, error) {
	switch {
	case set.Route == "":
		return nil, fmt.Errorf("%w: mutual funds are not switched on: compliance chooses the route in MF settings", ErrState)
	case set.Route != MFBoth && set.Route != plan:
		return nil, invalid("the firm offers %s plans only; this scheme is a %s plan", set.Route, plan)
	}
	route, err := o.mfFamilyRoute(ctx, tenant, p.Family)
	if err != nil {
		return nil, err
	}
	if route != "" && route != plan {
		return nil, invalid("%s's family invests through %s plans; one family uses one route (a %s plan cannot be added)", p.Name, route, plan)
	}
	if route == "" {
		return p.Family, nil
	}
	return nil, nil
}

func (o *Ops) mfLockRoute(ctx context.Context, tenant, actor, ucc, plan string, pans []string) {
	for _, pan := range pans {
		if r, _, err := docs.Get[MFRouteLock](ctx, o.Docs, tenant, cMFRoute, pan); err == nil && r.Route != "" {
			continue
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cMFRoute, pan, MFRouteLock{PAN: pan, Route: plan, Since: o.now().Format("2006-01-02"), By: actor, UCC: ucc}, actor)
	}
	if len(pans) > 0 {
		o.log(ctx, tenant, actor, "mf.route.fixed", ucc, map[string]any{"route": plan, "pans": len(pans)})
	}
}

// MFRoutes lists every family route on record.
func (o *Ops) MFRoutes(ctx context.Context, tenant string) ([]MFRouteLock, error) {
	rs, err := docs.List[MFRouteLock](ctx, o.Docs, tenant, cMFRoute, "")
	for i := range rs {
		rs[i].PAN = MaskPAN(rs[i].PAN)
	}
	return rs, err
}

// SetMFRoute changes (or clears, with route "") the route of the family
// of the client ucc, with the reason. Compliance does this when an
// investor moves from Regular to Direct plans or back, after the existing
// holdings are dealt with.
func (o *Ops) SetMFRoute(ctx context.Context, tenant, actor, ucc, route, note string) ([]MFRouteLock, error) {
	if err := o.require(ctx, tenant, actor, "Changing an investor's MF route", RoleCompliance); err != nil {
		return nil, err
	}
	if route != "" && route != MFRegular && route != MFDirect {
		return nil, invalid("the route is Regular or Direct")
	}
	if strings.TrimSpace(note) == "" {
		return nil, invalid("record why the route changes")
	}
	p, err := o.MFProfile(ctx, tenant, ucc)
	if err != nil {
		return nil, err
	}
	var out []MFRouteLock
	for _, pan := range p.Family {
		r := MFRouteLock{PAN: pan, Route: route, Since: o.now().Format("2006-01-02"), By: actor, UCC: p.UCC, Note: note}
		if route == "" {
			r.Route = ""
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cMFRoute, pan, r, actor); err != nil {
			return nil, err
		}
		r.PAN = MaskPAN(pan)
		out = append(out, r)
	}
	o.log(ctx, tenant, actor, "mf.route.changed", p.UCC, map[string]any{"route": route, "note": note, "pans": len(p.Family)})
	return out, nil
}

// ---- orders ----

// MFOrder is a mutual fund order through an exchange platform: placed
// from Ledgerline, or recorded from the platform's order file (MF-ORDERS
// on Other uploads).
type MFOrder struct {
	ID       string       `json:"id"`
	UCC      string       `json:"ucc"`
	Name     string       `json:"name,omitempty"`
	Scheme   string       `json:"scheme"`
	Type     string       `json:"type"` // Purchase, Redemption, Switch (or the file's own words)
	Amount   money.Amount `json:"amount"`
	Platform string       `json:"platform"`
	Status   string       `json:"status"`
	Date     string       `json:"date,omitempty"`
	// Placed from Ledgerline.
	Source       string    `json:"source,omitempty"` // placed, portal (empty: from a file)
	ISIN         string    `json:"isin,omitempty"`
	SchemeCode   string    `json:"scheme_code,omitempty"`
	ToISIN       string    `json:"to_isin,omitempty"`
	ToScheme     string    `json:"to_scheme,omitempty"`
	ToCode       string    `json:"to_code,omitempty"`
	Plan         string    `json:"plan,omitempty"`
	Units        money.Dec `json:"units,omitempty"`
	AllUnits     bool      `json:"all_units,omitempty"`
	Folio        string    `json:"folio,omitempty"`
	Mode         string    `json:"mode,omitempty"`     // Demat, Physical
	BuyType      string    `json:"buy_type,omitempty"` // FRESH, ADDITIONAL
	EUIN         string    `json:"euin,omitempty"`
	ARN          string    `json:"arn,omitempty"`
	MandateID    string    `json:"mandate_id,omitempty"`
	Ref          string    `json:"ref,omitempty"` // our unique reference sent to the platform
	OrderNo      string    `json:"order_no,omitempty"`
	PlatformStat string    `json:"platform_status,omitempty"`
	Remark       string    `json:"remark,omitempty"`
	Payment      string    `json:"payment,omitempty"` // how the investor pays (never through the firm)
	AfterCutoff  bool      `json:"after_cutoff,omitempty"`
	SIP          string    `json:"sip,omitempty"`
	// Allotment.
	NAV          money.Dec `json:"nav,omitempty"`
	Allotted     money.Dec `json:"allotted_units,omitempty"`
	AllottedOn   string    `json:"allotted_on,omitempty"`
	SwitchedOut  money.Dec `json:"switched_out_units,omitempty"`
	By           string    `json:"by,omitempty"`
	At           string    `json:"at,omitempty"`
	CancelledBy  string    `json:"cancelled_by,omitempty"`
	FileStatuses []string  `json:"file_statuses,omitempty"` // statuses seen in the platform's files
}

// Order stages.
const (
	MFPlaced    = "Placed"          // accepted by the platform, awaiting the investor's authorisation and payment
	MFRejected  = "Rejected"        // refused by Ledgerline's checks or the platform
	MFPendRTA   = "Pending at RTA"  // paid / units received, with the RTA
	MFAllotted  = "Allotted"        // units allotted
	MFRedeemed  = "Redeemed"        // redemption processed
	MFCancelled = "Cancelled"       // cancelled by the firm or the platform
	MFPayPend   = "Payment pending" // kept from the MF-ORDERS file
)

// MFOrders lists mutual fund orders, newest first. A row from the
// platform's order file whose order number is an order placed here
// updates that order instead of showing twice.
func (o *Ops) MFOrders(ctx context.Context, tenant string) ([]MFOrder, error) {
	all, err := docs.List[MFOrder](ctx, o.Docs, tenant, cMF, "")
	if err != nil {
		return nil, err
	}
	placed := map[string]int{} // the platform's order number -> placed order
	for i, x := range all {
		if x.Source != "" && x.OrderNo != "" {
			placed[x.OrderNo] = i
		}
	}
	merged := map[int]bool{}
	for j, x := range all {
		i, ok := placed[x.ID]
		if !ok || x.Source != "" {
			continue
		}
		p := &all[i]
		if x.Status != "" && !contains(p.FileStatuses, x.Status) {
			p.FileStatuses = append(p.FileStatuses, x.Status)
		}
		if st := mfStage(x.Status); st != "" && p.Status != MFAllotted && p.Status != MFRedeemed && p.Status != MFCancelled {
			p.Status = st
		}
		merged[j] = true
	}
	out := make([]MFOrder, 0, len(all))
	for j, x := range all {
		if !merged[j] {
			out = append(out, x)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].At, out[j].At
		if a == "" {
			a = out[i].Date
		}
		if b == "" {
			b = out[j].Date
		}
		return a > b
	})
	return out, nil
}

// mfStage maps a platform's status words to an order stage, "" when not
// known. NSE MF Invest's statuses are in its FAQ (section 7.50).
func mfStage(s string) string {
	u := strings.ToUpper(strings.TrimSpace(s))
	switch {
	case u == "":
		return ""
	case strings.Contains(u, "ALLOT"), strings.Contains(u, "UNITS TRANSFERRED TO CLIENT"):
		return MFAllotted
	case strings.Contains(u, "UNITS TRANSFERRED TO AMC"), strings.Contains(u, "REDEEM"):
		return MFRedeemed
	case strings.Contains(u, "CANCEL"):
		return MFCancelled
	case strings.Contains(u, "REJECT"), strings.Contains(u, "FAILED"):
		return MFRejected
	case strings.Contains(u, "RTA"), strings.Contains(u, "PROVISIONAL"):
		return MFPendRTA
	case strings.Contains(u, "PAYMENT"), strings.Contains(u, "AUTH"), strings.Contains(u, "2FA"), strings.Contains(u, "PLACED"):
		return MFPlaced
	}
	return ""
}

// MFOrderInput is an order as staff or a client enters it.
type MFOrderInput struct {
	UCC      string `json:"ucc"`
	Platform string `json:"platform"`
	Type     string `json:"type"` // Purchase, Redemption, Switch
	ISIN     string `json:"isin"`
	ToISIN   string `json:"to_isin,omitempty"`
	Amount   string `json:"amount,omitempty"`
	Units    string `json:"units,omitempty"`
	AllUnits bool   `json:"all_units,omitempty"`
	Folio    string `json:"folio,omitempty"`
	// MandateID pays a purchase by the investor's approved mandate (in the
	// clearing corporation's favour); empty means the platform's payment
	// link.
	MandateID string `json:"mandate_id,omitempty"`
}

// PlaceMFOrder places an order for a client, as staff.
func (o *Ops) PlaceMFOrder(ctx context.Context, tenant, actor string, in MFOrderInput) (MFOrder, error) {
	if err := o.require(ctx, tenant, actor, "Placing mutual fund orders", RoleMaker, RoleChecker); err != nil {
		return MFOrder{}, err
	}
	return o.placeMF(ctx, tenant, actor, in, "placed")
}

// PortalMFOrder places an order from the client portal for one of the
// login's own accounts. It is execution-only: no EUIN.
func (o *Ops) PortalMFOrder(ctx context.Context, tenant, actor string, in MFOrderInput) (MFOrder, error) {
	if err := o.portalOwns(ctx, tenant, actor, in.UCC); err != nil {
		return MFOrder{}, err
	}
	if in.Type == "Switch" {
		return MFOrder{}, invalid("switches are placed by the firm; ask us from the Help desk")
	}
	return o.placeMF(ctx, tenant, actor, in, "portal")
}

func (o *Ops) portalOwns(ctx context.Context, tenant, actor, ucc string) error {
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return err
	}
	if !contains(accts, strings.ToUpper(strings.TrimSpace(ucc))) {
		return ErrNotFound
	}
	return nil
}

// mfReady checks what every transaction needs: MF on, the platform
// enabled and connected, the client active and not debarred, KYC not on
// hold, FATCA declared and the client registered on the platform.
func (o *Ops) mfReady(ctx context.Context, tenant, ucc, platform string) (MFSettings, Client, MFProfile, mfConn, error) {
	set, err := o.MFSettingsGet(ctx, tenant)
	if err != nil {
		return set, Client{}, MFProfile{}, mfConn{}, err
	}
	platform = strings.ToUpper(strings.TrimSpace(platform))
	if !set.enabled(platform) {
		return set, Client{}, MFProfile{}, mfConn{}, invalid("platform %q is not enabled in MF settings", platform)
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return set, c, MFProfile{}, mfConn{}, err
	}
	switch {
	case c.Status != "Active":
		return set, c, MFProfile{}, mfConn{}, invalid("%s is not an active client", c.UCC)
	case c.Blocked && strings.HasPrefix(c.BlockedReason, "Debarred"):
		return set, c, MFProfile{}, mfConn{}, invalid("%s is blocked: %s", c.UCC, c.BlockedReason)
	case mfKYCBlocked(c.KRAStatus):
		return set, c, MFProfile{}, mfConn{}, invalid("%s's KYC is %q at the KRA: no mutual fund transaction until it is validated", c.UCC, c.KRAStatus)
	}
	p, err := o.MFProfile(ctx, tenant, c.UCC)
	if err != nil {
		return set, c, p, mfConn{}, err
	}
	if !p.FATCA {
		return set, c, p, mfConn{}, invalid("%s has no FATCA self-certification on record", c.UCC)
	}
	if r := p.Platforms[platform]; r.Status != "Registered" {
		return set, c, p, mfConn{}, invalid("%s is not registered on %s yet (%s)", c.UCC, MFPlatformNames[platform][0], firstOf(r.Status, "not registered"))
	}
	cn, err := o.mfConn(ctx, tenant, platform)
	return set, c, p, cn, err
}

func mfKYCBlocked(s string) bool {
	s = strings.ToLower(strings.ReplaceAll(s, "-", " "))
	return strings.Contains(s, "on hold") || strings.Contains(s, "onhold") || strings.Contains(s, "reject")
}

// mfNeedsNomination: a fresh folio from 1 September 2026 needs nominees
// or an opt-out.
func mfNeedsNomination(p MFProfile, today string) bool {
	return today >= MFNominationFrom && !p.NomOptOut && len(p.Nominees) == 0
}

func (o *Ops) placeMF(ctx context.Context, tenant, actor string, in MFOrderInput, source string) (MFOrder, error) {
	in.Platform = strings.ToUpper(strings.TrimSpace(in.Platform))
	set, c, p, cn, err := o.mfReady(ctx, tenant, in.UCC, in.Platform)
	if err != nil {
		return MFOrder{}, err
	}
	now := o.now()
	today := now.Format("2006-01-02")
	sc, err := o.mfScheme(ctx, tenant, in.ISIN)
	if err != nil {
		return MFOrder{}, err
	}
	code := sc.Codes[in.Platform]
	if code == "" {
		return MFOrder{}, invalid("%s has no %s scheme code: load the platform's scheme file", sc.Name, in.Platform)
	}
	ord := MFOrder{UCC: c.UCC, Name: c.Name, Scheme: sc.Name, ISIN: sc.ISIN, SchemeCode: code, Plan: sc.Plan, Platform: in.Platform, Date: today,
		Source: source, Mode: p.Mode, Folio: strings.TrimSpace(in.Folio), By: actor, At: now.Format(time.RFC3339)}
	switch in.Type {
	case "Purchase":
		if contains(sc.NoPurchase, in.Platform) {
			return ord, invalid("%s does not take purchases on %s", sc.Name, in.Platform)
		}
		amt, err := money.ParseAmount(strings.ReplaceAll(in.Amount, ",", ""))
		if err != nil || amt <= 0 {
			return ord, invalid("give the amount to invest in rupees")
		}
		if sc.MinPurchase > 0 && amt < sc.MinPurchase {
			return ord, invalid("the minimum purchase in %s is ₹%s", sc.Name, sc.MinPurchase)
		}
		ord.Amount = amt
		ord.BuyType = "FRESH"
		if ord.Folio != "" || o.mfHolds(ctx, tenant, c.UCC, sc) {
			ord.BuyType = "ADDITIONAL"
		}
		if ord.BuyType == "FRESH" && mfNeedsNomination(p, today) {
			return ord, invalid("a new folio from 1 September 2026 needs up to 3 nominees or the investor's opt-out: record it in %s's MF profile", c.UCC)
		}
		ord.Payment = "Platform payment link (to " + MFPlatformNames[in.Platform][1] + ")"
		if in.MandateID != "" {
			m, ok := mfMandateOf(p, in.MandateID)
			if !ok || m.Platform != in.Platform || m.Status != "Approved" {
				return ord, invalid("mandate %s is not an approved %s mandate of %s", in.MandateID, in.Platform, c.UCC)
			}
			if amt > m.Limit {
				return ord, invalid("₹%s is above the mandate's limit of ₹%s", amt, m.Limit)
			}
			ord.MandateID, ord.Payment = m.ID, "Mandate "+firstOf(m.UMRN, m.PlatformID)+" (in favour of "+m.Favour+")"
		}
	case "Redemption", "Switch":
		if in.Type == "Redemption" && contains(sc.NoRedeem, in.Platform) {
			return ord, invalid("%s does not take redemptions on %s", sc.Name, in.Platform)
		}
		if err := o.mfOutQty(ctx, tenant, &ord, p, sc, in); err != nil {
			return ord, err
		}
		if in.Type == "Switch" {
			to, err := o.mfScheme(ctx, tenant, in.ToISIN)
			if err != nil {
				return ord, err
			}
			if to.Codes[in.Platform] == "" {
				return ord, invalid("%s has no %s scheme code", to.Name, in.Platform)
			}
			if to.AMC != "" && sc.AMC != "" && to.AMC != sc.AMC {
				return ord, invalid("a switch stays within one fund house (%s, not %s)", sc.AMC, to.AMC)
			}
			if to.Plan != sc.Plan {
				// Regular to Direct is allowed on the platforms, but one
				// family uses one route here: compliance changes the route
				// and the investor redeems or switches with that in mind.
				return ord, invalid("a switch between Regular and Direct plans changes the family's route: compliance must change the route first")
			}
			ord.ToISIN, ord.ToScheme, ord.ToCode = to.ISIN, to.Name, to.Codes[in.Platform]
		}
	default:
		return ord, invalid("the order type is Purchase, Redemption or Switch")
	}
	ord.Type = in.Type
	lock, err := o.mfCheckRoute(ctx, tenant, set, p, sc.Plan)
	if err != nil {
		return ord, err
	}
	if sc.Plan == MFRegular {
		ord.ARN = set.ARN
		if source != "portal" {
			e, ok := set.euinOf(actor)
			if !ok {
				return ord, invalid("Regular plan orders placed by staff carry the employee's EUIN: add yours in MF settings")
			}
			if e.ValidTill != "" && e.ValidTill < today {
				return ord, invalid("your EUIN %s expired on %s", e.EUIN, e.ValidTill)
			}
			ord.EUIN = e.EUIN
		}
	}
	ord.AfterCutoff = now.Format("15:04") >= set.cutoff(in.Platform, sc.Liquid())
	n, err := o.next(ctx, tenant, "mforder", actor)
	if err != nil {
		return ord, err
	}
	ord.ID = fmt.Sprintf("MFO-%06d", n)
	ord.Ref, err = o.mfRef(ctx, tenant, actor, cn, now)
	if err != nil {
		return ord, err
	}
	req := mfOrderReq{Ref: ord.Ref, ClientCode: c.UCC, SchemeCode: ord.SchemeCode, ToSchemeCode: ord.ToCode, Type: ord.Type, BuyType: ord.BuyType,
		DPTxn: mfDPTxn(c, p), Amount: ord.Amount, Units: ord.Units, AllUnits: ord.AllUnits, Folio: ord.Folio, EUIN: ord.EUIN, Mobile: c.Mobile, Email: c.Email,
		BankAccount: c.Bank.Account, Remarks: ord.ID}
	if m, ok := mfMandateOf(p, ord.MandateID); ok {
		req.MandateID = m.PlatformID
	}
	plat := mfPlatformFor(in.Platform)
	var rep mfReply
	if ord.Type == "Switch" {
		rep, err = plat.Switch(ctx, cn, req)
	} else {
		rep, err = plat.Order(ctx, cn, req)
	}
	ord.OrderNo, ord.Remark, ord.PlatformStat = rep.OrderNo, rep.Remark, rep.Status
	if err != nil || !rep.OK {
		ord.Status = MFRejected
		if err != nil {
			ord.Remark = err.Error()
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cMF, ord.ID, ord, actor)
		o.log(ctx, tenant, actor, "mf.order.rejected", ord.ID, map[string]any{"ucc": ord.UCC, "platform": ord.Platform, "remark": ord.Remark})
		if err != nil && errors.Is(err, errMFNoAPI) {
			return ord, err
		}
		return ord, fmt.Errorf("%w: %s refused the order: %s", ErrState, MFPlatformNames[in.Platform][0], firstOf(ord.Remark, "no reason given"))
	}
	ord.Status = MFPlaced
	if err := docs.Upsert(ctx, o.Docs, tenant, cMF, ord.ID, ord, actor); err != nil {
		return ord, err
	}
	o.mfLockRoute(ctx, tenant, actor, c.UCC, sc.Plan, lock)
	o.log(ctx, tenant, actor, "mf.order.placed", ord.ID, map[string]any{"ucc": ord.UCC, "platform": ord.Platform, "type": ord.Type, "isin": ord.ISIN, "amount": ord.Amount.String(),
		"units": ord.Units.String(), "order_no": ord.OrderNo, "euin": ord.EUIN, "source": source})
	if ord.Type == "Purchase" {
		o.mfNotify(ctx, tenant, actor, "mf.order.placed", c.UCC, map[string]string{"scheme": sc.Name, "amount": formatINR(ord.Amount.Dec()), "platform": MFPlatformNames[in.Platform][0], "order": ord.OrderNo})
	}
	return ord, nil
}

// mfOutQty fills a redemption's or switch's units (or amount), checked
// against the units on record.
func (o *Ops) mfOutQty(ctx context.Context, tenant string, ord *MFOrder, p MFProfile, sc MFScheme, in MFOrderInput) error {
	held := o.mfHeld(ctx, tenant, ord.UCC, sc.ISIN, ord.Folio)
	switch {
	case in.AllUnits:
		ord.AllUnits = true
		if p.Mode == "Demat" {
			// Demat redemptions and switches go by units (NSE FAQ 7.43;
			// switch spec: all_units must be N for demat).
			if held.Sign() <= 0 {
				return invalid("no units of %s on record to redeem in full; give the units", sc.Name)
			}
			ord.AllUnits, ord.Units = false, held
		}
	case strings.TrimSpace(in.Units) != "":
		u, err := money.ParseDec(in.Units)
		if err != nil || u.Sign() <= 0 {
			return invalid("units must be a positive number")
		}
		if held.Sign() > 0 && u.Cmp(held) > 0 {
			return invalid("%s units asked; %s held in %s", u.String(), held.String(), sc.Name)
		}
		ord.Units = u
	case strings.TrimSpace(in.Amount) != "":
		if p.Mode == "Demat" {
			return invalid("demat redemptions and switches are by units, not amount")
		}
		amt, err := money.ParseAmount(strings.ReplaceAll(in.Amount, ",", ""))
		if err != nil || amt <= 0 {
			return invalid("give the amount in rupees")
		}
		ord.Amount = amt
	default:
		return invalid("give the units, the amount or all units")
	}
	if p.Mode == "Physical" && ord.Folio == "" {
		return invalid("a physical (folio) redemption or switch needs the folio number")
	}
	return nil
}

// mfRef is the unique reference of an order: BSE wants
// YYYYMMDD<user ID>NNNNNN, renewed daily; NSE takes up to 25 characters.
func (o *Ops) mfRef(ctx context.Context, tenant, actor string, cn mfConn, now time.Time) (string, error) {
	day := now.Format("20060102")
	n, err := o.next(ctx, tenant, "mfref-"+day, actor)
	if err != nil {
		return "", err
	}
	if cn.Platform == MFBSE {
		return fmt.Sprintf("%s%s%06d", day, cn.User, n), nil
	}
	return fmt.Sprintf("LL%s%06d", day, n), nil
}

func mfDPTxn(c Client, p MFProfile) string {
	if p.Mode != "Demat" {
		return "P"
	}
	if strings.EqualFold(c.Demat.Depository, "NSDL") {
		return "N"
	}
	return "C"
}

func mfMandateOf(p MFProfile, id string) (MFMandate, bool) {
	for _, m := range p.Mandates {
		if m.ID == id && id != "" {
			return m, true
		}
	}
	return MFMandate{}, false
}

// CancelMFOrder cancels an order on the platform (NSE MF Invest's order
// cancellation) or records one cancelled there.
func (o *Ops) CancelMFOrder(ctx context.Context, tenant, actor, id, note string) (MFOrder, error) {
	if err := o.require(ctx, tenant, actor, "Placing mutual fund orders", RoleMaker, RoleChecker); err != nil {
		return MFOrder{}, err
	}
	ord, _, err := docs.Get[MFOrder](ctx, o.Docs, tenant, cMF, id)
	if err != nil {
		return ord, err
	}
	if ord.Status != MFPlaced {
		return ord, fmt.Errorf("%w: only an order awaiting payment or authorisation can be cancelled (this one is %s)", ErrState, ord.Status)
	}
	cn, err := o.mfConn(ctx, tenant, ord.Platform)
	if err != nil {
		return ord, err
	}
	rep, err := mfPlatformFor(ord.Platform).CancelOrder(ctx, cn, ord.UCC, ord.OrderNo, firstOf(note, "cancelled by "+actor))
	if err != nil {
		return ord, err
	}
	if !rep.OK {
		return ord, fmt.Errorf("%w: the platform refused the cancellation: %s", ErrState, rep.Remark)
	}
	ord, err = docs.Update(ctx, o.Docs, tenant, cMF, id, actor, func(x *MFOrder) error {
		x.Status, x.Remark, x.CancelledBy = MFCancelled, firstOf(rep.Remark, note), actor
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "mf.order.cancelled", id, map[string]any{"order_no": ord.OrderNo, "note": note})
	}
	return ord, err
}

// ---- allotments and the units ledger ----

// MFUnitTxn is one movement of units in an investor's folio or demat
// account, as reported by the platform or the RTA.
type MFUnitTxn struct {
	ID     string       `json:"id"`
	UCC    string       `json:"ucc"`
	Mode   string       `json:"mode"` // Demat, Physical
	Folio  string       `json:"folio,omitempty"`
	ISIN   string       `json:"isin"`
	Scheme string       `json:"scheme"`
	AMC    string       `json:"amc,omitempty"`
	Date   string       `json:"date"`
	Kind   string       `json:"kind"`  // Purchase, Redemption, Switch in, Switch out, SIP, Opening, RTA
	Units  money.Dec    `json:"units"` // + in, − out
	NAV    money.Dec    `json:"nav,omitempty"`
	Amount money.Amount `json:"amount,omitempty"`
	Order  string       `json:"order,omitempty"`
	Source string       `json:"source"` // allotment, rta file, manual
	By     string       `json:"by"`
}

func (o *Ops) addUnits(ctx context.Context, tenant, actor string, t MFUnitTxn) error {
	n, err := o.next(ctx, tenant, "mfunit", actor)
	if err != nil {
		return err
	}
	t.ID = fmt.Sprintf("%s|%08d", t.UCC, n)
	t.By = actor
	return docs.Upsert(ctx, o.Docs, tenant, cMFUnit, t.ID, t, actor)
}

// MFAllotment is the platform's or RTA's confirmation of an order.
type MFAllotment struct {
	Units    string `json:"units"`               // allotted (purchase, switch in) or redeemed
	OutUnits string `json:"out_units,omitempty"` // switched out, for a switch
	NAV      string `json:"nav"`
	Date     string `json:"date"`
	Folio    string `json:"folio,omitempty"`
	Amount   string `json:"amount,omitempty"` // redemption proceeds (paid by the AMC to the investor's bank)
}

// RecordMFAllotment records an order's allotment (or redemption) and
// moves the units on the units ledger.
func (o *Ops) RecordMFAllotment(ctx context.Context, tenant, actor, id string, a MFAllotment) (MFOrder, error) {
	if err := o.require(ctx, tenant, actor, "Recording MF allotments", RoleMaker, RoleChecker); err != nil {
		return MFOrder{}, err
	}
	return o.recordAllotment(ctx, tenant, actor, id, a, "allotment")
}

func (o *Ops) recordAllotment(ctx context.Context, tenant, actor, id string, a MFAllotment, source string) (MFOrder, error) {
	units, err := money.ParseDec(a.Units)
	if err != nil || units.Sign() <= 0 {
		return MFOrder{}, invalid("units must be a positive number")
	}
	nav, _ := money.ParseDec(firstOf(a.NAV, "0"))
	if a.Date == "" {
		a.Date = o.now().Format("2006-01-02")
	}
	if !validDate(a.Date) {
		return MFOrder{}, invalid("the allotment date is YYYY-MM-DD")
	}
	ord, _, err := docs.Get[MFOrder](ctx, o.Docs, tenant, cMF, id)
	if err != nil {
		return ord, err
	}
	if ord.Status == MFAllotted || ord.Status == MFRedeemed {
		return ord, fmt.Errorf("%w: %s is already %s", ErrState, id, strings.ToLower(ord.Status))
	}
	if ord.Status == MFRejected || ord.Status == MFCancelled {
		return ord, fmt.Errorf("%w: %s was %s", ErrState, id, strings.ToLower(ord.Status))
	}
	sc, _ := o.mfScheme(ctx, tenant, ord.ISIN)
	folio := firstOf(strings.TrimSpace(a.Folio), ord.Folio)
	t := MFUnitTxn{UCC: ord.UCC, Mode: ord.Mode, Folio: folio, ISIN: ord.ISIN, Scheme: ord.Scheme, AMC: sc.AMC, Date: a.Date, NAV: nav, Order: ord.ID, Source: source}
	if ord.Mode == "Demat" {
		t.Folio = ""
	}
	stage := MFAllotted
	switch ord.Type {
	case "Purchase":
		t.Kind, t.Units, t.Amount = "Purchase", units, ord.Amount
		if ord.SIP != "" {
			t.Kind = "SIP"
		}
	case "Redemption":
		stage = MFRedeemed
		t.Kind, t.Units = "Redemption", units.Neg()
		t.Amount, _ = money.ParseAmount(firstOf(a.Amount, "0"))
	case "Switch":
		out, err := money.ParseDec(firstOf(a.OutUnits, ord.Units.String()))
		if err != nil || out.Sign() <= 0 {
			return ord, invalid("a switch needs the units switched out")
		}
		t.Kind, t.Units = "Switch out", out.Neg()
		if err := o.addUnits(ctx, tenant, actor, t); err != nil {
			return ord, err
		}
		to, _ := o.mfScheme(ctx, tenant, ord.ToISIN)
		t = MFUnitTxn{UCC: ord.UCC, Mode: ord.Mode, Folio: t.Folio, ISIN: ord.ToISIN, Scheme: ord.ToScheme, AMC: to.AMC, Date: a.Date, NAV: nav, Order: ord.ID, Source: source, Kind: "Switch in", Units: units}
		ord.SwitchedOut = out
	default:
		return ord, invalid("order %s is a %q order from a file: record its units on the units ledger instead", id, ord.Type)
	}
	if err := o.addUnits(ctx, tenant, actor, t); err != nil {
		return ord, err
	}
	out := ord.SwitchedOut
	ord, err = docs.Update(ctx, o.Docs, tenant, cMF, id, actor, func(x *MFOrder) error {
		x.Status, x.Allotted, x.NAV, x.AllottedOn, x.SwitchedOut = stage, units, nav, a.Date, out
		if folio != "" && x.Mode != "Demat" {
			x.Folio = folio
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "mf.order.allotted", id, map[string]any{"units": units.String(), "nav": nav.String(), "date": a.Date, "source": source})
	}
	return ord, err
}

// MFHolding is an investor's units in one scheme and folio (or demat).
type MFHolding struct {
	UCC     string       `json:"ucc"`
	Name    string       `json:"name,omitempty"`
	Mode    string       `json:"mode"`
	Folio   string       `json:"folio,omitempty"`
	ISIN    string       `json:"isin"`
	Scheme  string       `json:"scheme"`
	AMC     string       `json:"amc,omitempty"`
	Plan    string       `json:"plan,omitempty"`
	Units   money.Dec    `json:"units"`
	NAV     money.Dec    `json:"nav"`
	NAVDate string       `json:"nav_date,omitempty"`
	Value   money.Amount `json:"value"`
	Cost    money.Amount `json:"invested"`
}

// MFUnitsView is the units ledger: holdings and movements.
type MFUnitsView struct {
	Holdings []MFHolding `json:"holdings"`
	Txns     []MFUnitTxn `json:"txns"`
	Value    string      `json:"value"`
}

// MFUnits returns holdings and movements, for one client or all.
func (o *Ops) MFUnits(ctx context.Context, tenant, ucc string) (MFUnitsView, error) {
	prefix := ""
	if ucc != "" {
		prefix = strings.ToUpper(ucc) + "|"
	}
	ts, err := docs.List[MFUnitTxn](ctx, o.Docs, tenant, cMFUnit, prefix)
	if err != nil {
		return MFUnitsView{}, err
	}
	hs, total := o.mfHoldings(ctx, tenant, ts, "")
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].Date > ts[j].Date })
	if ts == nil {
		ts = []MFUnitTxn{}
	}
	return MFUnitsView{Holdings: hs, Txns: ts, Value: total.String()}, nil
}

// mfHoldings adds the movements up to asOf ("" for all) into holdings,
// valued at the latest NAV (or the month-end NAV of asOf's month).
func (o *Ops) mfHoldings(ctx context.Context, tenant string, ts []MFUnitTxn, asOf string) ([]MFHolding, money.Amount) {
	type k struct{ ucc, mode, folio, isin string }
	m := map[k]*MFHolding{}
	var keys []k
	for _, t := range ts {
		if asOf != "" && t.Date > asOf {
			continue
		}
		key := k{t.UCC, t.Mode, t.Folio, t.ISIN}
		h := m[key]
		if h == nil {
			h = &MFHolding{UCC: t.UCC, Mode: t.Mode, Folio: t.Folio, ISIN: t.ISIN, Scheme: t.Scheme, AMC: t.AMC}
			m[key] = h
			keys = append(keys, key)
		}
		h.Units = h.Units.Add(t.Units)
		if t.Units.Sign() > 0 {
			h.Cost += t.Amount
		}
	}
	cm, _ := o.ClientMap(ctx, tenant)
	out := []MFHolding{}
	var total money.Amount
	schemes := map[string]MFScheme{}
	for _, key := range keys {
		h := m[key]
		if h.Units.Sign() <= 0 {
			continue
		}
		sc, ok := schemes[h.ISIN]
		if !ok {
			sc, _, _ = docs.Get[MFScheme](ctx, o.Docs, tenant, cMFScheme, h.ISIN)
			schemes[h.ISIN] = sc
		}
		h.Name, h.Plan, h.NAV, h.NAVDate = cm[h.UCC].Name, sc.Plan, sc.NAV, sc.NAVDate
		if asOf != "" {
			if v, ok := sc.MonthEnd[asOf[:7]]; ok {
				h.NAV, h.NAVDate = dec(v), asOf[:7]
			}
		}
		if h.AMC == "" {
			h.AMC = sc.AMC
		}
		h.Value = h.Units.Mul(h.NAV).Round(money.UnitPaisa)
		total += h.Value
		out = append(out, *h)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UCC != out[j].UCC {
			return out[i].UCC < out[j].UCC
		}
		return out[i].Scheme < out[j].Scheme
	})
	return out, total
}

// mfHeld is the units on record of a scheme for a client (one folio, or
// all when folio is "").
func (o *Ops) mfHeld(ctx context.Context, tenant, ucc, isin, folio string) money.Dec {
	ts, _ := docs.List[MFUnitTxn](ctx, o.Docs, tenant, cMFUnit, ucc+"|")
	var u money.Dec
	for _, t := range ts {
		if t.ISIN == isin && (folio == "" || t.Folio == folio) {
			u = u.Add(t.Units)
		}
	}
	return u
}

// mfHolds says whether the client already has a folio with the scheme's
// fund house (an additional purchase rather than a fresh folio).
func (o *Ops) mfHolds(ctx context.Context, tenant, ucc string, sc MFScheme) bool {
	ts, _ := docs.List[MFUnitTxn](ctx, o.Docs, tenant, cMFUnit, ucc+"|")
	for _, t := range ts {
		if t.ISIN == sc.ISIN || (sc.AMC != "" && t.AMC == sc.AMC && t.Folio != "") {
			return true
		}
	}
	return false
}

// AddMFUnits records units from outside an order: an opening balance at
// go-live, or a movement from the RTA's statement.
func (o *Ops) AddMFUnits(ctx context.Context, tenant, actor string, t MFUnitTxn) (MFUnitTxn, error) {
	if err := o.require(ctx, tenant, actor, "Recording MF allotments", RoleMaker, RoleChecker); err != nil {
		return t, err
	}
	t.UCC = strings.ToUpper(strings.TrimSpace(t.UCC))
	c, err := o.Client(ctx, tenant, t.UCC)
	if err != nil {
		return t, err
	}
	sc, err := o.mfScheme(ctx, tenant, t.ISIN)
	if err != nil {
		return t, err
	}
	if t.Units.IsZero() || !validDate(t.Date) {
		return t, invalid("give the units (negative for units out) and the date")
	}
	if !contains([]string{"Opening", "Purchase", "Redemption", "Switch in", "Switch out", "SIP", "Dividend reinvestment", "RTA"}, t.Kind) {
		return t, invalid("unknown movement %q", t.Kind)
	}
	if t.Mode != "Demat" && t.Mode != "Physical" {
		t.Mode = "Physical"
		if c.Demat.ID != "" && t.Folio == "" {
			t.Mode = "Demat"
		}
	}
	if t.Mode == "Physical" && t.Folio == "" {
		return t, invalid("folio units need the folio number")
	}
	t.ISIN, t.Scheme, t.AMC, t.Source = sc.ISIN, sc.Name, sc.AMC, firstOf(t.Source, "manual")
	if err := o.addUnits(ctx, tenant, actor, t); err != nil {
		return t, err
	}
	o.log(ctx, tenant, actor, "mf.units.added", t.UCC, map[string]any{"isin": t.ISIN, "units": t.Units.String(), "kind": t.Kind, "date": t.Date})
	return t, nil
}

// ---- the client portal ----

// PortalMFView is what a client sees about their mutual funds.
type PortalMFView struct {
	UCC      string      `json:"ucc"`
	Profile  MFProfile   `json:"profile"`
	Route    string      `json:"route"`     // the family's route ("" before the first order)
	Offered  string      `json:"offered"`   // the firm's: Regular, Direct, Both
	Ready    []string    `json:"ready"`     // platforms the client can transact on
	Missing  []string    `json:"missing"`   // what stops transactions
	Holdings []MFHolding `json:"holdings"`  // from the units ledger
	Orders   []MFOrder   `json:"orders"`    // newest first
	SIPs     []MFSIP     `json:"sips"`      // all
	Value    string      `json:"value"`     // of holdings
	Cutoffs  string      `json:"cutoffs"`   // for display
	Nominate bool        `json:"nominate"`  // a fresh folio needs a nomination first
	PayNote  string      `json:"pay_note"`  // how money moves
	ARNNote  string      `json:"arn_note"`  // distributor details for Regular plans
	Platform []string    `json:"platforms"` // enabled
}

// PortalMF builds the client's mutual fund page for one of their accounts.
func (o *Ops) PortalMF(ctx context.Context, tenant, actor, ucc string) (PortalMFView, error) {
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return PortalMFView{}, err
	}
	ucc = strings.ToUpper(ucc)
	if ucc == "" && len(accts) > 0 {
		ucc = accts[0]
	}
	if !contains(accts, ucc) {
		return PortalMFView{}, ErrNotFound
	}
	return o.mfClientView(ctx, tenant, ucc)
}

func (o *Ops) mfClientView(ctx context.Context, tenant, ucc string) (PortalMFView, error) {
	set, _ := o.MFSettingsGet(ctx, tenant)
	p, err := o.MFProfile(ctx, tenant, ucc)
	if err != nil {
		return PortalMFView{}, err
	}
	p = MaskMFProfile(p)
	v := PortalMFView{UCC: p.UCC, Profile: p, Route: p.Route, Offered: set.Route, Ready: []string{}, Missing: []string{}, Orders: []MFOrder{}, SIPs: []MFSIP{}, Platform: set.Platforms,
		Nominate: mfNeedsNomination(p, o.now().Format("2006-01-02")),
		PayNote:  "You pay the clearing corporation directly, by the exchange platform's payment link or by your mandate in its favour. Your trading account balance is not used, and the firm never receives your mutual fund money. Redemptions are paid by the fund house to your registered bank account."}
	if set.ARN != "" && (set.Route == MFRegular || set.Route == MFBoth) {
		v.ARNNote = "Regular plans are distributed by the firm (" + set.ARN + "), which is paid commission by the fund house; Direct plans carry no commission."
	}
	c, _ := o.Client(ctx, tenant, ucc)
	if set.Route == "" {
		v.Missing = append(v.Missing, "The firm does not offer mutual funds yet")
	}
	if mfKYCBlocked(c.KRAStatus) {
		v.Missing = append(v.Missing, "Your KYC is "+c.KRAStatus+" at the KRA")
	}
	if !p.FATCA {
		v.Missing = append(v.Missing, "FATCA self-certification")
	}
	for _, pl := range set.Platforms {
		if p.Platforms[pl].Status == "Registered" {
			v.Ready = append(v.Ready, pl)
		}
	}
	if set.Route != "" && len(v.Ready) == 0 {
		v.Missing = append(v.Missing, "Registration with the exchange's mutual fund platform (the firm does this)")
	}
	u, _ := o.MFUnits(ctx, tenant, ucc)
	v.Holdings, v.Value = u.Holdings, u.Value
	os, _ := o.MFOrders(ctx, tenant)
	for _, x := range os {
		if x.UCC == ucc {
			v.Orders = append(v.Orders, x)
		}
	}
	ss, _ := o.MFSIPs(ctx, tenant, ucc)
	v.SIPs = ss
	var cs []string
	for _, pl := range set.Platforms {
		cs = append(cs, fmt.Sprintf("%s: %s for liquid funds, %s for others", MFPlatformNames[pl][0], set.cutoff(pl, true), set.cutoff(pl, false)))
	}
	v.Cutoffs = strings.Join(cs, "; ")
	return v, nil
}

// SavePortalMFProfile lets a client record their own FATCA declaration and
// folio nomination (up to 3 nominees, or opt out). Other details are kept.
func (o *Ops) SavePortalMFProfile(ctx context.Context, tenant, actor, ucc string, fatca bool, nominees []MFNominee, optOut bool) (MFProfile, error) {
	if err := o.portalOwns(ctx, tenant, actor, ucc); err != nil {
		return MFProfile{}, err
	}
	p, err := o.MFProfile(ctx, tenant, ucc)
	if err != nil {
		return p, err
	}
	u := MFProfileUpdate{Mode: p.Mode, TaxStatus: p.TaxStatus, Occupation: p.Occupation, Gender: p.Gender, Address: p.Address, Pincode: p.Pincode, StateCode: p.StateCode,
		AccountType: p.AccountType, FATCA: fatca, TaxResidentOut: p.TaxResidentOut, Nominees: nominees, NomOptOut: optOut, NomAuth: "O", FamilyHead: p.FamilyHead}
	return o.saveMFProfile(ctx, tenant, actor, ucc, u)
}
