package ops

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- KYC of persons resident outside India (PROI) ----
//
// SEBI circular HO/38/30/12(3)2026-MIRSD-POD2/I/25175/2026 (review of the
// KYC process for PROI), forwarded by NSE/INSP/77335 (circular 54/2026)
// and CDSL communiqué DP2026-780, in force for individual PROI clients (an
// NRI, an OCI cardholder or a foreign national) onboarded, or whose KYC is
// updated, from 3 December 2026 (NSE's circular reads 4 December; the
// earlier date is used):
//   - para 5.2: seek the PAN and the CKYC identifier, and retrieve the
//     KYC already at the KRA and the CKYCRR, before collecting documents;
//   - paras 5.3-5.5: the PAN verified with the Income Tax database; the
//     passport (and the OCI card for an OCI); an officially valid
//     document for the overseas address; self-attested or certified
//     copies; the e-mail verified by a one-time code, the mobile (Indian
//     or international) where feasible, and when the mobile cannot be
//     verified every communication goes to the verified e-mail (5.5.7);
//   - para 5.5.8: in-person verification with the official's details and
//     a date-time stamp (a), or video IPV with consent, random actions, a
//     live GPS geo-tag and time stamp, VPN/proxy blocking, the client's IP
//     in India or a FATF-compliant country matching the KYC country,
//     liveness and face match with the OVD photo, end-to-end encryption,
//     an authorised official present, and concurrent audit (b);
//   - paras 5.7-5.10: the KYC at the KRA within 3 working days (the KRA
//     upload clock, kycgate.go), attested English translations of
//     documents not in English, the same process for re-KYC.
//
// Ledgerline records each of these on the client (Client.PROI), checks
// them before the client is activated (activationProblems), routes
// messages to the e-mail while the mobile is not verified (messaging.go),
// and reports an NRO account holder to NSE as category 37 (NSE/ISC/71043,
// circular 41/2025: category 11 is NRE only from 22 November 2025).

// PROIFrom is the day the PROI rules apply from.
const PROIFrom = "2026-12-03"

// PROICategories are the kinds of individual PROI client.
var PROICategories = []string{"NRI", "OCI", "Foreign national"}

// PROIIPVModes are the ways the in-person verification is done.
var PROIIPVModes = []string{"Physical", "Video (VIPV)"}

// PROIExtra says, where the KRA or the CKYCRR already has the client's KYC,
// why more was collected (para 5.2 read with the 2026 circular's review of
// the KYC process: additional details only where the record changed, is
// incomplete or not compliant, the OVD's validity lapsed, or verification
// is considered necessary), or that the full KYC was collected because no
// record exists.
var PROIExtra = []string{"Nothing more: the KRA/CKYCRR record is complete", "Details changed", "Record incomplete or not compliant", "OVD validity lapsed", "Verification considered necessary", "No record: full KYC collected"}

// PROIForms are how the KYC form came in: the CERSAI individual template,
// physical, e-signed scan or digital form (para 5.3). A digital form needs
// a wet signature shown during the VIPV and matched.
var PROIForms = []string{"CERSAI template (physical)", "CERSAI template (e-signed scan)", "CERSAI template (digital form)"}

// PROIOSV are the ways the copies were verified (para 5.4): originals seen
// and verified, or the alternatives the circular allows when the client
// cannot produce them. The certifying persons are as the gap report reads
// the circular (to confirm against its para 5.4).
var PROIOSV = []string{"Originals seen and verified", "DigiLocker or issuer e-document", "Aadhaar e-KYC", "Copy certified by a notary", "Copy certified by an overseas bank official", "Copy certified by an Indian embassy or consulate", "Copy certified by a court magistrate or judge"}

// PROIKYC is a PROI client's onboarding record.
type PROIKYC struct {
	Category string `json:"category"` // NRI, OCI, Foreign national
	Country  string `json:"country"`  // of residence (ISO 3166-1 alpha-2)
	// Retrieved before documents were collected (para 5.2).
	KRALookupOn  string `json:"kra_lookup_on,omitempty"`
	CKYCLookupOn string `json:"ckyc_lookup_on,omitempty"`
	// CKYCGiven is the CKYC identifier the client gave, or "None".
	CKYCGiven     string `json:"ckyc_given,omitempty"`
	DocsOn        string `json:"docs_on,omitempty"`
	PANVerifiedOn string `json:"pan_verified_on,omitempty"`
	// Extra is why more than the KRA/CKYCRR record was collected (PROIExtra).
	Extra string `json:"extra,omitempty"`
	// Form is how the KYC form came in (PROIForms); WetSignMatch: the wet
	// signature shown during the VIPV matched the form (digital form).
	Form         string `json:"form,omitempty"`
	WetSignMatch bool   `json:"wet_sign_match,omitempty"`
	// PoI and PoA are the OVDs (or deemed OVDs) taken as proof of identity
	// and of address; AddressCheck how the overseas address was verified;
	// OSV how the copies were verified (PROIOSV).
	PoI          string `json:"poi,omitempty"`
	PoA          string `json:"poa,omitempty"`
	AddressCheck string `json:"address_check,omitempty"`
	OSV          string `json:"osv,omitempty"`
	// PassportCopy is the passport copy kept (and the OCI card's);
	// PassportCheck whether the passport was verified ("Verified", or why
	// it was not feasible).
	PassportCopy  string `json:"passport_copy,omitempty"`
	PassportCheck string `json:"passport_check,omitempty"`
	// Passport (and OCI card), and the overseas address with its OVD.
	PassportNo      string `json:"passport_no,omitempty"`
	PassportExpiry  string `json:"passport_expiry,omitempty"`
	OCICard         string `json:"oci_card,omitempty"`
	OverseasAddress string `json:"overseas_address,omitempty"`
	OverseasOVD     string `json:"overseas_ovd,omitempty"`
	// Verification of the contacts (para 5.5.7).
	EmailVerified  bool `json:"email_verified"`
	MobileVerified bool `json:"mobile_verified"`
	// In-person verification (para 5.5.8).
	IPV         string    `json:"ipv,omitempty"`
	IPVOfficial string    `json:"ipv_official,omitempty"`
	IPVAt       string    `json:"ipv_at,omitempty"`
	VIPV        *PROIVIPV `json:"vipv,omitempty"`
	// Translations: documents not in English have attested English
	// translations ("Not needed" when all are in English).
	Translations string `json:"translations,omitempty"`
	By           string `json:"by,omitempty"`
	At           string `json:"at,omitempty"`
}

// PROIVIPV is what a video IPV recorded (para 5.5.8(b)).
type PROIVIPV struct {
	Consent         bool   `json:"consent"`
	RandomActions   bool   `json:"random_actions"`
	GeoTag          string `json:"geo_tag,omitempty"` // live GPS co-ordinates
	VPNBlocked      bool   `json:"vpn_blocked"`
	IPCountry       string `json:"ip_country,omitempty"`
	FATFCompliant   bool   `json:"fatf_compliant"` // the IP country is not on FATF's lists
	Liveness        bool   `json:"liveness"`
	FaceMatch       bool   `json:"face_match"`
	Encrypted       bool   `json:"encrypted"`
	OfficialPresent bool   `json:"official_present"`
	AuditedBy       string `json:"audited_by,omitempty"` // concurrent audit
	AuditedOn       string `json:"audited_on,omitempty"`
	// The VIPV conditions of the KYC master circular that apply to every
	// VIPV (onboardx.go): the official's training, the live recording kept
	// with its fingerprint, the random questions, the OVD shown.
	OfficialTrainedOn string   `json:"official_trained_on,omitempty"`
	RecordingRef      string   `json:"recording_ref,omitempty"`
	RecordingSHA256   string   `json:"recording_sha256,omitempty"`
	Questions         []string `json:"questions,omitempty"`
	OVDShown          string   `json:"ovd_shown,omitempty"`
}

var (
	reCountry    = regexp.MustCompile(`^[A-Z]{2}$`)
	reIntlMobile = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
	reCKYC       = regexp.MustCompile(`^[0-9]{14}$`)
)

// isPROI: the client is an individual resident outside India.
func isPROI(c Client) bool { return c.Type == "NRI" || c.PROI != nil }

// proiApplies: the PROI rules apply to the client's onboarding (opened, or
// to be opened, from 3 December 2026).
func proiApplies(c Client, today string) bool {
	if !isPROI(c) {
		return false
	}
	if validDate(c.KYCDate) {
		return c.KYCDate >= PROIFrom
	}
	return today >= PROIFrom
}

// proiProblems lists what a PROI client's onboarding record lacks.
func proiProblems(c Client, today string) []string {
	p := c.PROI
	if p == nil {
		return []string{"no PROI onboarding record (passport, overseas address, KRA and CKYC look-up, IPV)"}
	}
	var out []string
	add := func(f string, x ...any) { out = append(out, fmt.Sprintf(f, x...)) }
	if !contains(PROICategories, p.Category) {
		add("category (NRI, OCI or foreign national) not recorded")
	}
	if !reCountry.MatchString(p.Country) {
		add("country of residence not recorded")
	}
	switch {
	case !validDate(p.KRALookupOn) || !validDate(p.CKYCLookupOn):
		add("KRA and CKYCRR records not looked up")
	case validDate(p.DocsOn) && (p.KRALookupOn > p.DocsOn || p.CKYCLookupOn > p.DocsOn):
		add("KRA and CKYCRR looked up after the documents were collected")
	}
	if !reCKYC.MatchString(c.CKYC) && !reCKYC.MatchString(p.CKYCGiven) && !strings.EqualFold(p.CKYCGiven, "None") {
		add("CKYC identifier not sought (record it, or None)")
	}
	if !validDate(p.PANVerifiedOn) {
		add("PAN not verified with the Income Tax database")
	}
	if strings.TrimSpace(p.PassportNo) == "" || !validDate(p.PassportExpiry) {
		add("passport number and expiry not recorded")
	} else if p.PassportExpiry < today {
		add("passport expired on %s", fmtDay(p.PassportExpiry))
	}
	if p.Category == "OCI" && strings.TrimSpace(p.OCICard) == "" {
		add("OCI card not recorded")
	}
	if strings.TrimSpace(p.OverseasAddress) == "" || strings.TrimSpace(p.OverseasOVD) == "" {
		add("overseas address and the document proving it not recorded")
	}
	if !p.EmailVerified {
		add("e-mail not verified by a one-time code")
	}
	switch p.IPV {
	case "Physical":
		if strings.TrimSpace(p.IPVOfficial) == "" || p.IPVAt == "" {
			add("IPV without the official's details and the date and time")
		}
	case "Video (VIPV)":
		v := p.VIPV
		if v == nil {
			add("video IPV checks not recorded")
			break
		}
		var miss []string
		for _, x := range []struct {
			ok   bool
			name string
		}{{v.Consent, "consent"}, {v.RandomActions, "random actions"}, {strings.TrimSpace(v.GeoTag) != "", "live GPS geo-tag"}, {v.VPNBlocked, "VPN/proxy blocked"},
			{v.IPCountry == "IN" || v.IPCountry == p.Country, "IP from India or the KYC country"}, {v.FATFCompliant, "FATF-compliant country"},
			{v.Liveness, "liveness check"}, {v.FaceMatch, "face match with the OVD"}, {v.Encrypted, "end-to-end encryption"}, {v.OfficialPresent, "authorised official present"},
			{strings.TrimSpace(v.AuditedBy) != "" && validDate(v.AuditedOn), "concurrent audit"},
			{validDate(v.OfficialTrainedOn), "the official's VIPV training"}, {strings.TrimSpace(v.RecordingRef) != "" && reSHA256.MatchString(v.RecordingSHA256), "the recording's reference and fingerprint"},
			{len(v.Questions) > 0, "the random questions asked"}, {strings.TrimSpace(v.OVDShown) != "", "the OVD shown"}} {
			if !x.ok {
				miss = append(miss, x.name)
			}
		}
		if len(miss) > 0 {
			add("video IPV lacks %s", strings.Join(miss, ", "))
		}
		if strings.TrimSpace(p.IPVOfficial) == "" || p.IPVAt == "" {
			add("video IPV without the official's details and the date and time")
		}
	default:
		add("in-person verification (physical or video) not recorded")
	}
	if p.Translations == "" {
		add("translations of documents not in English not recorded (or Not needed)")
	}
	// The review of the PROI KYC process (in force from 3 Dec 2026).
	if !contains(PROIExtra, p.Extra) {
		add("why more than the KRA/CKYCRR record was collected (or that it was not) not recorded")
	}
	if strings.TrimSpace(p.PassportCopy) == "" || strings.TrimSpace(p.PassportCheck) == "" {
		add("the passport copy kept and its verification (or why it was not feasible) not recorded")
	}
	switch {
	case !contains(PROIForms, p.Form):
		add("the KYC form in CERSAI's individual template not recorded")
	case p.Form == "CERSAI template (digital form)" && (p.IPV != "Video (VIPV)" || !p.WetSignMatch):
		add("a digital form needs the wet signature shown in the video IPV and matched")
	}
	if strings.TrimSpace(p.PoI) == "" || strings.TrimSpace(p.PoA) == "" {
		add("the OVDs taken as proof of identity and of address not recorded")
	}
	if strings.TrimSpace(p.AddressCheck) == "" {
		add("how the overseas address was verified (against the OVD, and a source database where feasible) not recorded")
	}
	if !contains(PROIOSV, p.OSV) {
		add("how the copies were verified (originals seen, or an allowed alternative) not recorded")
	}
	return out
}

// proiEmailOnly says why a message for the client goes to the e-mail
// instead of the mobile ("" when it need not): a PROI client whose mobile
// is not verified (para 5.5.7), or whose mobile is international (Indian
// SMS gateways do not reach it).
func proiEmailOnly(c Client) string {
	if !isPROI(c) {
		return ""
	}
	switch {
	case c.PROI != nil && !c.PROI.MobileVerified:
		return "the client resides outside India and the mobile is not verified: sent to the verified e-mail instead"
	case strings.HasPrefix(strings.TrimSpace(c.Mobile), "+"):
		return "the client's mobile is international: sent to the e-mail instead"
	}
	return ""
}

// SetPROI records a PROI client's onboarding record (Ops maker or
// checker, or Compliance).
func (o *Ops) SetPROI(ctx context.Context, tenant, actor, ucc string, p PROIKYC) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Recording PROI onboarding", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return Client{}, err
	}
	p.Category = strings.TrimSpace(p.Category)
	p.Country = strings.ToUpper(strings.TrimSpace(p.Country))
	p.PassportNo = strings.ToUpper(strings.TrimSpace(p.PassportNo))
	p.CKYCGiven = strings.TrimSpace(p.CKYCGiven)
	if !contains(PROICategories, p.Category) {
		return Client{}, invalid("category must be %s", strings.Join(PROICategories, ", "))
	}
	if !reCountry.MatchString(p.Country) || p.Country == "IN" {
		return Client{}, invalid("country of residence is the two-letter ISO code of a country other than India")
	}
	for _, d := range []struct{ v, name string }{{p.KRALookupOn, "KRA look-up"}, {p.CKYCLookupOn, "CKYCRR look-up"}, {p.DocsOn, "documents collected"}, {p.PANVerifiedOn, "PAN verified"}, {p.PassportExpiry, "passport expiry"}} {
		if d.v != "" && !validDate(d.v) {
			return Client{}, invalid("%s: give the date as YYYY-MM-DD", d.name)
		}
	}
	if p.CKYCGiven != "" && !reCKYC.MatchString(p.CKYCGiven) && !strings.EqualFold(p.CKYCGiven, "None") {
		return Client{}, invalid("the CKYC identifier is 14 digits (or None when the client has none)")
	}
	if p.IPV != "" && !contains(PROIIPVModes, p.IPV) {
		return Client{}, invalid("IPV must be %s", strings.Join(PROIIPVModes, " or "))
	}
	for _, x := range []struct {
		v, name string
		list    []string
	}{{p.Extra, "what was collected", PROIExtra}, {p.Form, "the KYC form", PROIForms}, {p.OSV, "how the copies were verified", PROIOSV}} {
		if x.v != "" && !contains(x.list, x.v) {
			return Client{}, invalid("%s is one of: %s", x.name, strings.Join(x.list, "; "))
		}
	}
	if p.IPVAt != "" {
		if _, err := time.Parse(time.RFC3339, p.IPVAt); err != nil {
			if _, err := time.Parse("2006-01-02T15:04", p.IPVAt); err != nil {
				return Client{}, invalid("the IPV date and time is YYYY-MM-DDTHH:MM")
			}
		}
	}
	if p.IPV != "Video (VIPV)" {
		p.VIPV = nil
	} else if p.VIPV != nil {
		p.VIPV.IPCountry = strings.ToUpper(strings.TrimSpace(p.VIPV.IPCountry))
		p.VIPV.RecordingSHA256 = strings.ToLower(strings.TrimSpace(p.VIPV.RecordingSHA256))
		if p.VIPV.OfficialTrainedOn != "" && !validDate(p.VIPV.OfficialTrainedOn) {
			return Client{}, invalid("the official's training date is YYYY-MM-DD")
		}
	}
	p.By, p.At = actor, o.now().Format(time.RFC3339)
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(x *Client) error {
		if x.Type != "NRI" && x.Type != "Individual" {
			return invalid("the PROI record is for individuals")
		}
		// The screen shows the numbers masked: a masked number sent back
		// keeps the one on record.
		if old := x.PROI; old != nil {
			if strings.Contains(p.PassportNo, "*") {
				p.PassportNo = old.PassportNo
			}
			if strings.Contains(p.OCICard, "*") {
				p.OCICard = old.OCICard
			}
		}
		x.PROI = &p
		return nil
	})
	if err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "proi.recorded", c.UCC, map[string]any{"category": p.Category, "country": p.Country, "ipv": p.IPV})
	return c, nil
}

// PROIRow is a PROI client on the screen.
type PROIRow struct {
	UCC      string   `json:"ucc"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	KYCDate  string   `json:"kyc_date,omitempty"`
	Applies  bool     `json:"applies"` // onboarded from 3 Dec 2026
	Problems []string `json:"problems"`
	Record   *PROIKYC `json:"record,omitempty"`
	CKYC     string   `json:"ckyc,omitempty"`
	Mobile   string   `json:"mobile,omitempty"` // masked
	EmailOff string   `json:"email_only,omitempty"`
}

func (o *Ops) proiRows(ctx context.Context, tenant string, cs map[string]Client) []PROIRow {
	today := o.now().In(ist).Format("2006-01-02")
	var out []PROIRow
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if !isPROI(c) || c.Status == "Closed" || c.Status == "Rejected" {
			continue
		}
		r := PROIRow{UCC: u, Name: c.Name, Status: c.Status, KYCDate: c.KYCDate, Applies: proiApplies(c, today), Problems: []string{}, Record: c.PROI.masked(), CKYC: c.CKYC, Mobile: maskMobile(c.Mobile), EmailOff: proiEmailOnly(c)}
		if r.Applies {
			r.Problems = append(r.Problems, proiProblems(c, today)...)
		}
		out = append(out, r)
	}
	return out
}

// maskDoc shows a document number's last four characters.
func maskDoc(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= 4 {
		return strings.Repeat("*", len(r))
	}
	return strings.Repeat("*", len(r)-4) + string(r[len(r)-4:])
}

// masked is the record for display: the passport and OCI card numbers
// masked.
func (p *PROIKYC) masked() *PROIKYC {
	if p == nil {
		return nil
	}
	x := *p
	x.PassportNo, x.OCICard = maskDoc(x.PassportNo), maskDoc(x.OCICard)
	if x.VIPV != nil {
		v := *x.VIPV
		x.VIPV = &v
	}
	return &x
}
