package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/xfile"
)

// ---- FATCA and CRS self-certification, sent to the KRA ----
//
// Rules, from the circulars:
//   - SEBI/HO/MIRSD/SECFATF/P/CIR/2024/12 (20 February 2024): FATCA and CRS
//     self-certifications are kept centrally at the KYC registration
//     agencies (KRAs). Intermediaries upload them from 1 July 2024 (para 3);
//     certifications taken before then within 90 days (para 4); the onus is
//     on the intermediary (para 5), which checks that each one is
//     reasonable and updates it when the client reports a change (para 6).
//   - CVL KRA communique CVL/OPS/INTERM/GENRL/24-200 (28 June 2024): the
//     FATCA/CRS data goes with the KYC upload from 1 August 2024.
//   - KFintech KRA circular KARVY/OPS/INTERM/GENRL/24-07 (11 June 2024): the
//     fields Place of Birth, Country of Birth (ISO 3166), "Tax Residency
//     other than India?" (Y or N; Y needs at least one country other than
//     India), Country of Tax Residency1-4, Tax Identification Number1-4 (a
//     9- or 16-character TIN for the US; a reason when the TIN is exempt)
//     and Date of Declaration (dd-mm-yyyy). An updated certification is
//     uploaded as a modification; the KRA keeps the latest one; an Indian
//     tax residency need not be sent.
//   - KFintech KRA circular KARVY/OPS/INTERM/GENRL/24-009 (28 September
//     2024): individuals onboarded before 1 August 2024 (legacy cases) by
//     31 October 2024; the process for non-individuals to be notified.
//   - SEBI HO/38/14/(18)2026-MIRSD-POD2/I/25560/2026 (11 November 2026):
//     FATCA and CRS under section 508 of the Income-tax Act 2025 and Rules
//     238-240, with Form 166 replacing Form 61B. Onboarding captures the
//     self-certification's validity indicator (para 6.1), controlling
//     persons and their roles (6.2), the account's type and whether it is
//     pre-existing or new (6.3) and the Form 166 XML fields (6.4);
//     certifications go to the KRAs (para 7).
//   - CBDT Guidance Note on FATCA and CRS (24 July 2026): what a
//     self-certification holds, the reasons for having no TIN (A, B, C),
//     the indicia that make a declaration doubtful, and the dates that make
//     an account pre-existing (CRS: opened on or before 31 December 2015).
//
// A new client cannot be created without a self-certification (online
// applications have a step for it; an Indian resident individual answers
// it with one choice). The KYC desk changes it by change request, which a
// checker approves; the change goes to the KRA as a modification. Clients
// onboarded before 1 August 2024 whose certification is not at the KRA are
// listed apart, with a bulk file. Ledgerline only holds the Form 166 data:
// it does not build the statement.

const (
	// MaxTaxCountries is how many countries of tax residency the KRA takes
	// (Country of Tax Residency1-4, KARVY/OPS/INTERM/GENRL/24-07).
	MaxTaxCountries = 4
	// FATCAKRAFrom: from this day FATCA/CRS goes with the KYC upload
	// (CVL/OPS/INTERM/GENRL/24-200); clients onboarded before it are legacy
	// cases (KARVY/OPS/INTERM/GENRL/24-009).
	FATCAKRAFrom = "2024-08-01"
	// CRSNewFrom: an account opened from this day is a new account under
	// CRS; one opened earlier is pre-existing (CBDT Guidance Note, 1.4).
	CRSNewFrom = "2016-01-01"
	// maxControllingPersons caps the persons kept for one client.
	maxControllingPersons = 10
	// fatcaKRADays are the working days a certification may wait for the
	// KRA upload (the KYC upload's three working days, SEBI KYC master
	// circular para 84, which the FATCA/CRS data goes with).
	fatcaKRADays = 3
)

// How a self-certification was given.
const (
	FATCAModeESign    = "Online with e-Sign"
	FATCAModePortal   = "Online (client portal)"
	FATCAModeForm     = "Signed form"
	FATCAModeImported = "Imported from the previous system"
)

// FATCAModes are the ways a self-certification is given.
var FATCAModes = []string{FATCAModeESign, FATCAModePortal, FATCAModeForm, FATCAModeImported}

// Where a certification stands at the KRA.
const (
	FATCAKRAUploaded = "Uploaded"
	FATCAKRAAccepted = "Accepted"
	FATCAKRARejected = "Rejected"
)

// DocFATCA is the document kind of a signed self-certification.
const DocFATCA = "FATCA/CRS self-certification"

// ChangeFATCA is the change request kind.
const ChangeFATCA = "FATCA/CRS"

// TaxResidence is one country, other than India, where the person is
// resident for tax, with the TIN or the reason there is none.
type TaxResidence struct {
	Country string `json:"country"` // ISO 3166-1 alpha-2
	TIN     string `json:"tin,omitempty"`
	// TINCountry is the country that issued the TIN, when not Country.
	TINCountry string `json:"tin_country,omitempty"`
	// NoTIN is the reason there is no TIN: A, B or C (NoTINReasons);
	// NoTINWhy explains reason B.
	NoTIN    string `json:"no_tin,omitempty"`
	NoTINWhy string `json:"no_tin_why,omitempty"`
}

// ControllingPerson is a natural person who controls a non-individual
// client (SEBI circular of 11 November 2026, para 6.2).
type ControllingPerson struct {
	Name           string         `json:"name"`
	Type           string         `json:"type"` // CRS801-CRS813
	PAN            string         `json:"pan,omitempty"`
	DOB            string         `json:"dob,omitempty"`
	PlaceOfBirth   string         `json:"place_of_birth,omitempty"`
	CountryOfBirth string         `json:"country_of_birth,omitempty"`
	Nationality    string         `json:"nationality,omitempty"`
	OtherThanIndia string         `json:"other_than_india"` // Y or N
	Residences     []TaxResidence `json:"residences,omitempty"`
	SelfCert       string         `json:"self_cert,omitempty"` // Y: a valid self-certification of the person
}

// FATCAResolved is a reasonableness warning the KYC desk looked into.
type FATCAResolved struct {
	Code string `json:"code"`
	Note string `json:"note"`
	By   string `json:"by"`
	At   string `json:"at"`
}

// FATCADecl is a client's FATCA/CRS self-certification.
type FATCADecl struct {
	// OtherThanIndia is the KRA's "Tax Residency other than India?": N for
	// a tax resident of India only; Y with each other country in
	// Residences (up to MaxTaxCountries).
	OtherThanIndia string         `json:"other_than_india"`
	Residences     []TaxResidence `json:"residences,omitempty"`
	// PlaceOfBirth and CountryOfBirth: of birth for an individual, of
	// incorporation for a non-individual.
	PlaceOfBirth   string `json:"place_of_birth,omitempty"`
	CountryOfBirth string `json:"country_of_birth,omitempty"`
	// Nationality is the citizenship (ISO code).
	Nationality string `json:"nationality,omitempty"`
	// USPerson: Y for a US citizen or a US resident for tax (FATCA).
	USPerson string `json:"us_person,omitempty"`
	// ControllingPersons of a non-individual client.
	ControllingPersons []ControllingPerson `json:"controlling_persons,omitempty"`
	// AccountType (CRS1101-CRS1104) and AccountStatus (CRS1201 new or
	// CRS1202 pre-existing) classify the account for Form 166.
	AccountType   string `json:"account_type,omitempty"`
	AccountStatus string `json:"account_status,omitempty"`
	// SelfCert is the validity indicator: Y when a valid self-certification
	// was obtained, N when not (signed, dated and complete).
	SelfCert   string          `json:"self_cert,omitempty"`
	DeclaredOn string          `json:"declared_on,omitempty"` // date of the self-certification
	Mode       string          `json:"mode,omitempty"`        // FATCAModes
	Resolved   []FATCAResolved `json:"resolved,omitempty"`
	// KRA is where it stands at the KRA: "" not yet sent, Uploaded (in a
	// file marked uploaded), Accepted or Rejected (from the KRA's status
	// file).
	KRA       string `json:"kra,omitempty"`
	KRAOn     string `json:"kra_on,omitempty"`
	KRARef    string `json:"kra_ref,omitempty"`
	KRARemark string `json:"kra_remark,omitempty"`
	By        string `json:"by,omitempty"`
	At        string `json:"at,omitempty"`
}

// nonIndividual says whether a client type is an entity, which names its
// controlling persons.
func nonIndividual(typ string) bool { return typ == "HUF" || typ == "Corporate" }

// IndiaOnlyFATCA is the self-certification of a tax resident of India only,
// a citizen of India, born in India.
func IndiaOnlyFATCA(placeOfBirth, declaredOn string) *FATCADecl {
	return &FATCADecl{OtherThanIndia: "N", PlaceOfBirth: placeOfBirth, CountryOfBirth: "IN", Nationality: "IN", USPerson: "N", SelfCert: "Y", DeclaredOn: declaredOn}
}

func upperTrim(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }
func equalFoldTrim(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
func fatcaYN(s string) string {
	return map[string]string{"Y": "Y", "YES": "Y", "N": "N", "NO": "N"}[upperTrim(s)]
}
func normCountry(s string) string {
	c, ok := countryByName(upperTrim(s))
	return map[bool]string{true: c, false: upperTrim(s)}[ok]
}
func clean(s string) string         { return strings.Join(strings.Fields(s), " ") }
func normTIN(s string) string       { return strings.ToUpper(strings.Join(strings.Fields(s), "")) }
func (d FATCADecl) indiaOnly() bool { return d.OtherThanIndia == "N" }
func (d FATCADecl) foreignCountries() []string {
	var out []string
	for _, r := range d.Residences {
		out = append(out, r.Country)
	}
	return out
}

func normResidences(rs []TaxResidence) []TaxResidence {
	var out []TaxResidence
	for _, r := range rs {
		r.Country, r.TINCountry = normCountry(r.Country), normCountry(r.TINCountry)
		r.TIN, r.NoTIN, r.NoTINWhy = normTIN(r.TIN), upperTrim(r.NoTIN), clean(r.NoTINWhy)
		if r.Country == "" && r.TIN == "" && r.NoTIN == "" {
			continue
		}
		if r.Country == "IN" {
			continue // an Indian tax residency is not sent (KARVY/OPS/INTERM/GENRL/24-07)
		}
		if r.TINCountry == r.Country {
			r.TINCountry = ""
		}
		if r.TIN != "" {
			r.NoTIN, r.NoTINWhy = "", ""
		}
		if r.NoTIN != NoTINNotObtained {
			r.NoTINWhy = ""
		}
		out = append(out, r)
	}
	return out
}

// normalise cleans a declaration in place.
func (d *FATCADecl) normalise() {
	d.OtherThanIndia, d.USPerson, d.SelfCert = fatcaYN(d.OtherThanIndia), fatcaYN(d.USPerson), fatcaYN(d.SelfCert)
	d.PlaceOfBirth, d.CountryOfBirth, d.Nationality = clean(d.PlaceOfBirth), normCountry(d.CountryOfBirth), normCountry(d.Nationality)
	d.AccountType, d.AccountStatus, d.Mode, d.DeclaredOn = upperTrim(d.AccountType), upperTrim(d.AccountStatus), clean(d.Mode), strings.TrimSpace(d.DeclaredOn)
	d.Residences = normResidences(d.Residences)
	if d.OtherThanIndia == "N" {
		d.Residences = nil
	}
	if d.OtherThanIndia == "" && len(d.Residences) > 0 {
		d.OtherThanIndia = "Y"
	}
	if contains(d.foreignCountries(), "US") {
		d.USPerson = "Y" // a US tax resident is a US person
	}
	var cps []ControllingPerson
	for _, p := range d.ControllingPersons {
		p.Name, p.Type, p.PAN, p.DOB = clean(p.Name), upperTrim(p.Type), upperTrim(p.PAN), strings.TrimSpace(p.DOB)
		p.PlaceOfBirth, p.CountryOfBirth, p.Nationality = clean(p.PlaceOfBirth), normCountry(p.CountryOfBirth), normCountry(p.Nationality)
		p.OtherThanIndia, p.SelfCert = fatcaYN(p.OtherThanIndia), fatcaYN(p.SelfCert)
		p.Residences = normResidences(p.Residences)
		if p.OtherThanIndia == "" {
			p.OtherThanIndia = map[bool]string{true: "Y", false: "N"}[len(p.Residences) > 0]
		}
		if p.OtherThanIndia == "N" {
			p.Residences = nil
		}
		if p.SelfCert == "" {
			p.SelfCert = "Y"
		}
		if p.Name == "" && p.Type == "" && p.PAN == "" {
			continue
		}
		cps = append(cps, p)
	}
	d.ControllingPersons = cps
}

// withDefaults fills what a declaration leaves out: valid, a custodial
// account, new or pre-existing from when the account was opened.
func (d *FATCADecl) withDefaults(opened, today string) {
	if d.SelfCert == "" {
		d.SelfCert = "Y"
	}
	if d.DeclaredOn == "" {
		d.DeclaredOn = today
	}
	if d.AccountType == "" {
		d.AccountType = FATCAAccountTypes[0].Code
	}
	if d.AccountStatus == "" {
		d.AccountStatus = accountStatusFor(opened, today)
	}
}

// accountStatusFor classifies an account by the day it was opened (empty:
// opened today): new from 1 January 2016, pre-existing before (CRS).
func accountStatusFor(opened, today string) string {
	if firstOf(opened, today) < CRSNewFrom {
		return AccountPreexisting
	}
	return AccountNew
}

var rePlace = regexp.MustCompile(`^[\p{L}][\p{L}0-9 .,'()/&-]*$`)

// placeProblem checks a place of birth as the KRA does: no special
// characters, not only numbers.
func placeProblem(p string) string {
	if p == "" {
		return ""
	}
	if !rePlace.MatchString(p) {
		return "use letters, and no special characters"
	}
	return ""
}

// tinProblem checks a TIN for its country: a US TIN is 9 digits, or 16
// characters for a GIIN (KARVY/OPS/INTERM/GENRL/24-07: "9/16-digit TIN");
// other countries' TINs are checked by their own formats at the KRA's
// side, so only their characters are checked here.
func tinProblem(country, tin string) string {
	t := strings.NewReplacer("-", "", ".", "", "/", "").Replace(tin)
	if t == "" {
		return ""
	}
	for _, r := range t {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return "a TIN has only letters, digits and - . /"
		}
	}
	if country == "US" && !(len(t) == 9 && allDigits(t)) && len(t) != 16 {
		return "a US TIN has 9 digits (or 16 characters for a GIIN)"
	}
	return ""
}

func residenceProblems(prefix string, rs []TaxResidence, strict bool) []FieldError {
	var fe []FieldError
	add := func(f, m string, a ...any) { fe = append(fe, FieldError{prefix + f, fmt.Sprintf(m, a...)}) }
	if len(rs) > MaxTaxCountries {
		add("residences", "give at most %d countries of tax residency other than India (the KRA takes %d)", MaxTaxCountries, MaxTaxCountries)
	}
	seen := map[string]bool{}
	for i, r := range rs {
		f := fmt.Sprintf("residences.%d.", i)
		switch {
		case r.Country == "":
			add(f+"country", "choose the country of tax residency")
		case Countries[r.Country] == "":
			add(f+"country", "%q is not an ISO 3166 country code", r.Country)
		case seen[r.Country]:
			add(f+"country", "%s is given twice", Countries[r.Country])
		}
		seen[r.Country] = true
		if r.TINCountry != "" && Countries[r.TINCountry] == "" {
			add(f+"tin_country", "%q is not an ISO 3166 country code", r.TINCountry)
		}
		if p := tinProblem(firstOf(r.TINCountry, r.Country), r.TIN); p != "" {
			add(f+"tin", "%s", p)
		}
		if r.NoTIN != "" && NoTINReasons[r.NoTIN] == "" {
			add(f+"no_tin", "the reason for no TIN is A, B or C")
		}
		if strict && r.TIN == "" && r.NoTIN == "" {
			add(f+"tin", "give the TIN for %s, or the reason there is none", firstOf(Countries[r.Country], "this country"))
		}
		if strict && r.NoTIN == NoTINNotObtained && r.NoTINWhy == "" {
			add(f+"no_tin_why", "say why the TIN has not been obtained")
		}
	}
	return fe
}

// problems lists what is wrong with a normalised declaration for a client
// of the given type. Strict (a declaration being made now) also needs the
// TIN or reason for every country, the place and country of birth, and
// the controlling persons' details; a declaration imported from the
// previous system may lack them, and they show as warnings instead.
func (d FATCADecl) problems(typ, today string, strict bool) []FieldError {
	var fe []FieldError
	add := func(f, m string, a ...any) { fe = append(fe, FieldError{"fatca." + f, fmt.Sprintf(m, a...)}) }
	switch d.OtherThanIndia {
	case "N":
	case "Y":
		if len(d.Residences) == 0 {
			add("residences", "give each country other than India where the client is resident for tax, or choose India only")
		}
	default:
		add("other_than_india", "say whether the client is a tax resident of India only (N) or of other countries too (Y)")
	}
	fe = append(fe, prefixed("fatca.", residenceProblems("", d.Residences, strict))...)
	for f, v := range map[string]string{"country_of_birth": d.CountryOfBirth, "nationality": d.Nationality} {
		if v != "" && Countries[v] == "" {
			add(f, "%q is not an ISO 3166 country code", v)
		}
	}
	if p := placeProblem(d.PlaceOfBirth); p != "" {
		add("place_of_birth", "place of birth: %s", p)
	}
	ind := !nonIndividual(typ)
	if strict {
		what := map[bool]string{true: "birth", false: "incorporation"}[ind]
		if d.PlaceOfBirth == "" {
			add("place_of_birth", "give the place of %s", what)
		}
		if d.CountryOfBirth == "" {
			add("country_of_birth", "give the country of %s", what)
		}
		if ind && d.Nationality == "" {
			add("nationality", "give the client's nationality")
		}
	}
	if d.USPerson != "" && d.USPerson != "Y" && d.USPerson != "N" {
		add("us_person", "US person is Y or N")
	}
	if strict && d.USPerson == "" {
		add("us_person", "say whether the client is a US person (a US citizen or resident for tax)")
	}
	if d.USPerson == "Y" && !contains(d.foreignCountries(), "US") && strict {
		add("residences", "a US person is resident in the United States for tax: add the United States with the US TIN")
	}
	if d.SelfCert != "Y" && d.SelfCert != "N" {
		add("self_cert", "the validity indicator is Y (a valid self-certification was obtained) or N")
	}
	if !validDate(d.DeclaredOn) {
		add("declared_on", "give the date of the self-certification (YYYY-MM-DD)")
	} else if d.DeclaredOn > today {
		add("declared_on", "the date of the self-certification cannot be in the future")
	}
	if accountTypeName(d.AccountType) == "" {
		add("account_type", "the account type is one of %s", strings.Join(fatcaAccountTypeCodes(), ", "))
	}
	if FATCAAccountStatuses[d.AccountStatus] == "" {
		add("account_status", "the account is new (%s) or pre-existing (%s)", AccountNew, AccountPreexisting)
	}
	if d.Mode != "" && !contains(FATCAModes, d.Mode) {
		add("mode", "the self-certification is given %s", strings.Join(FATCAModes, ", "))
	}
	if ind && len(d.ControllingPersons) > 0 {
		add("controlling_persons", "an individual has no controlling persons")
	}
	if len(d.ControllingPersons) > maxControllingPersons {
		add("controlling_persons", "give at most %d controlling persons", maxControllingPersons)
	}
	for i, p := range d.ControllingPersons {
		f := fmt.Sprintf("controlling_persons.%d.", i)
		if len(p.Name) < 3 {
			add(f+"name", "give the controlling person's full name")
		}
		if cpTypeName(p.Type) == "" {
			add(f+"type", "choose the controlling person's role (CRS801 to CRS813)")
		}
		if p.PAN != "" && !rePAN.MatchString(p.PAN) {
			add(f+"pan", "PAN must be 5 letters, 4 digits, 1 letter")
		}
		if p.DOB != "" && !validDate(p.DOB) {
			add(f+"dob", "date of birth is YYYY-MM-DD")
		}
		for g, v := range map[string]string{"country_of_birth": p.CountryOfBirth, "nationality": p.Nationality} {
			if v != "" && Countries[v] == "" {
				add(f+g, "%q is not an ISO 3166 country code", v)
			}
		}
		if p.OtherThanIndia == "Y" && len(p.Residences) == 0 {
			add(f+"residences", "give each country other than India where the person is resident for tax")
		}
		fe = append(fe, prefixed("fatca."+f, residenceProblems("", p.Residences, strict))...)
	}
	return fe
}

func prefixed(p string, fe []FieldError) []FieldError {
	for i := range fe {
		fe[i].Field = p + fe[i].Field
	}
	return fe
}

func fatcaAccountTypeCodes() []string {
	var out []string
	for _, t := range FATCAAccountTypes {
		out = append(out, t.Code)
	}
	return out
}

// prepareFATCA normalises a declaration being made now for a client,
// fills its defaults and checks it strictly.
func prepareFATCA(d *FATCADecl, c Client, today string) error {
	d.normalise()
	d.withDefaults(c.KYCDate, today)
	if fe := d.problems(c.Type, today, true); len(fe) > 0 {
		return &ValidationError{fe}
	}
	return nil
}

// ---- reasonableness ----

// FATCAWarning is something in the client's records that does not fit the
// declaration (SEBI/HO/MIRSD/SECFATF/P/CIR/2024/12 para 6; the indicia of
// the CBDT Guidance Note). Resolvable warnings are cleared by a note on
// what was checked; the others by a corrected declaration.
type FATCAWarning struct {
	Code       string         `json:"code"`
	Text       string         `json:"text"`
	Resolvable bool           `json:"resolvable"`
	Resolved   *FATCAResolved `json:"resolved,omitempty"`
}

func indianNationality(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "1", "in", "ind", "india", "indian":
		return true
	}
	return false
}

func indianCountry(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "85", "in", "ind", "india":
		return true
	}
	return false
}

// fatcaWarnings checks a declaration against the client's KYC details.
func fatcaWarnings(c Client, d *FATCADecl, ud UCCDetails) []FATCAWarning {
	var ws []FATCAWarning
	w := func(code string, res bool, f string, a ...any) {
		ws = append(ws, FATCAWarning{Code: code, Text: fmt.Sprintf(f, a...), Resolvable: res})
	}
	if d == nil {
		return nil
	}
	if d.SelfCert == "N" {
		w("not-valid", false, "The self-certification is marked not valid: collect a fresh, signed and dated one and record it.")
	}
	if d.indiaOnly() {
		var abroad []string
		if !indianCountry(ud.Country) {
			abroad = append(abroad, "the correspondence address")
		}
		if !indianCountry(ud.PermCountry) {
			abroad = append(abroad, "the permanent address")
		}
		if c.Type == "NRI" {
			abroad = append(abroad, "the client type (NRI)")
		}
		if len(abroad) > 0 {
			w("address-abroad", true, "Declared a tax resident of India only, but %s is outside India. Ask the client to confirm the tax residency, or record a new declaration with the other country.", strings.Join(abroad, " and "))
		}
		if m := strings.TrimSpace(c.Mobile); m != "" && !reMobile.MatchString(strings.TrimPrefix(strings.TrimPrefix(m, "+91"), "0")) {
			w("phone-abroad", true, "Declared a tax resident of India only, but the mobile number on record is not an Indian number.")
		}
		switch {
		case d.Nationality != "" && d.Nationality != "IN":
			w("nationality-abroad", true, "Declared a tax resident of India only, but declared a %s national.", firstOf(Countries[d.Nationality], d.Nationality))
		case !indianNationality(ud.Nationality):
			w("nationality-abroad", true, "Declared a tax resident of India only, but the exchange UCC details give the nationality as %q, not Indian.", ud.Nationality)
		}
	}
	if d.USPerson == "N" && (d.CountryOfBirth == "US" || d.Nationality == "US") {
		w("us-indicia", true, "Born in or a citizen of the United States, but declared not a US person. Keep the client's explanation and, for a former citizen, the certificate of loss of nationality.")
	}
	for i, r := range d.Residences {
		if r.TIN == "" && r.NoTIN == "" {
			w(fmt.Sprintf("no-tin-%d", i+1), false, "No TIN, and no reason for it, for %s: record the TIN or the reason (A, B or C).", firstOf(Countries[r.Country], r.Country))
		}
	}
	if nonIndividual(c.Type) && len(d.ControllingPersons) == 0 {
		w("no-controlling-persons", true, "%s with no controlling persons. Record them (name, role, tax residency and TIN), or note why there are none to report.", map[string]string{"HUF": "An HUF", "Corporate": "A non-individual client"}[c.Type])
	}
	for i, p := range d.ControllingPersons {
		for _, r := range p.Residences {
			if r.TIN == "" && r.NoTIN == "" {
				w(fmt.Sprintf("cp-no-tin-%d", i+1), false, "No TIN, and no reason for it, for controlling person %s in %s.", p.Name, firstOf(Countries[r.Country], r.Country))
				break
			}
		}
	}
	if !nonIndividual(c.Type) && (d.PlaceOfBirth == "" || d.CountryOfBirth == "") {
		w("no-birth", false, "The place or country of birth is missing: the KRA needs both.")
	}
	if d.AccountStatus == "" {
		w("account-status", false, "Whether the account is new or pre-existing is not recorded.")
	}
	for i := range ws {
		for j := range d.Resolved {
			if d.Resolved[j].Code == ws[i].Code && ws[i].Resolvable {
				r := d.Resolved[j]
				ws[i].Resolved = &r
			}
		}
	}
	return ws
}

func openWarnings(ws []FATCAWarning) int {
	n := 0
	for _, w := range ws {
		if w.Resolved == nil {
			n++
		}
	}
	return n
}

// ---- masking ----

// MaskTIN keeps a TIN's last four characters.
func MaskTIN(t string) string {
	if len(t) <= 4 {
		return strings.Repeat("X", len(t))
	}
	return strings.Repeat("X", len(t)-4) + t[len(t)-4:]
}

func maskResidences(rs []TaxResidence) []TaxResidence {
	out := make([]TaxResidence, len(rs))
	for i, r := range rs {
		r.TIN = MaskTIN(r.TIN)
		out[i] = r
	}
	return out
}

// masked is a copy for display: TINs and PANs masked.
func (d *FATCADecl) masked() *FATCADecl {
	if d == nil {
		return nil
	}
	x := *d
	x.Residences = maskResidences(d.Residences)
	x.ControllingPersons = make([]ControllingPerson, len(d.ControllingPersons))
	for i, p := range d.ControllingPersons {
		p.Residences = maskResidences(p.Residences)
		if p.PAN != "" {
			p.PAN = MaskPAN(p.PAN)
		}
		x.ControllingPersons[i] = p
	}
	if len(x.ControllingPersons) == 0 {
		x.ControllingPersons = nil
	}
	return &x
}

// restoreMasked puts back the TINs and PANs a form sent back masked (as
// the screens show them), from the declaration on record: by country, and
// by the controlling person's name. A value that is not the masked form of
// the one on record is new and kept as typed.
func (d *FATCADecl) restoreMasked(old *FATCADecl) {
	if old == nil {
		return
	}
	fix := func(rs, was []TaxResidence) {
		for i := range rs {
			t := normTIN(rs[i].TIN)
			for _, o := range was {
				if o.Country == normCountry(rs[i].Country) && o.TIN != "" && t == MaskTIN(o.TIN) {
					rs[i].TIN = o.TIN
				}
			}
		}
	}
	fix(d.Residences, old.Residences)
	for i := range d.ControllingPersons {
		p := &d.ControllingPersons[i]
		for _, o := range old.ControllingPersons {
			if clean(o.Name) != clean(p.Name) {
				continue
			}
			fix(p.Residences, o.Residences)
			if o.PAN != "" && upperTrim(p.PAN) == MaskPAN(o.PAN) {
				p.PAN = o.PAN
			}
		}
	}
}

// fatcaSummary describes a declaration in a line, TINs masked.
func fatcaSummary(d *FATCADecl) string {
	if d == nil {
		return "No self-certification"
	}
	var parts []string
	if d.indiaOnly() {
		parts = append(parts, "Tax resident of India only")
	} else {
		var cs []string
		for _, r := range d.Residences {
			x := firstOf(Countries[r.Country], r.Country)
			switch {
			case r.TIN != "":
				x += " (TIN " + MaskTIN(r.TIN) + ")"
			case r.NoTIN != "":
				x += " (no TIN: reason " + r.NoTIN + ")"
			}
			cs = append(cs, x)
		}
		parts = append(parts, "Tax resident of "+firstOf(strings.Join(cs, ", "), "other countries"))
	}
	if d.USPerson == "Y" {
		parts = append(parts, "US person")
	}
	if d.CountryOfBirth != "" || d.PlaceOfBirth != "" {
		parts = append(parts, "born "+strings.Trim(d.PlaceOfBirth+", "+firstOf(Countries[d.CountryOfBirth], d.CountryOfBirth), ", "))
	}
	if n := len(d.ControllingPersons); n > 0 {
		parts = append(parts, fmt.Sprintf("%d controlling person%s", n, map[bool]string{true: "", false: "s"}[n == 1]))
	}
	if d.DeclaredOn != "" {
		parts = append(parts, "declared "+fmtDay(d.DeclaredOn))
	}
	if d.SelfCert == "N" {
		parts = append(parts, "not valid")
	}
	return strings.Join(parts, " · ")
}

// fingerprint identifies a declaration's content (what the client
// declared, not where it stands).
func (d *FATCADecl) fingerprint() string {
	if d == nil {
		return ""
	}
	x := *d
	x.Resolved, x.KRA, x.KRAOn, x.KRARef, x.KRARemark, x.By, x.At = nil, "", "", "", "", "", ""
	b, _ := json.Marshal(x)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:16]
}

// ---- the change request ----

func fatcaOld(c Client) map[string]string {
	return map[string]string{"summary": fatcaSummary(c.FATCA.masked()), "fatca_fp": c.FATCA.fingerprint()}
}

// fatcaChange applies a FATCA/CRS change request: the new declaration
// (values "fatca", as JSON), how it was given ("mode") and its date.
func fatcaChange(c *Client, r ChangeRequest) error {
	raw := strings.TrimSpace(r.New["fatca"])
	if raw == "" {
		return invalid("give the new self-certification")
	}
	var d FATCADecl
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return invalid("the self-certification could not be read")
	}
	d.restoreMasked(c.FATCA)
	if m := strings.TrimSpace(r.New["mode"]); m != "" {
		d.Mode = m
	}
	if d.Mode == "" {
		d.Mode = map[bool]string{true: FATCAModePortal, false: FATCAModeForm}[r.Source == "Portal"]
	}
	if r.Source == "Portal" {
		d.Mode = FATCAModePortal
	}
	// Today on Ledgerline's clock: when the request was decided, or raised.
	today := firstOf(r.Decided, r.At)
	if len(today) >= 10 {
		today = today[:10]
	}
	if !validDate(today) {
		today = time.Now().Format("2006-01-02")
	}
	if c.FATCA != nil && d.AccountStatus == "" {
		d.AccountStatus = c.FATCA.AccountStatus
	}
	if err := prepareFATCA(&d, *c, today); err != nil {
		return err
	}
	// A new declaration is sent to the KRA again, and earlier notes on
	// warnings no longer apply.
	d.Resolved, d.KRA, d.KRAOn, d.KRARef, d.KRARemark = nil, "", "", "", ""
	d.By, d.At = r.By, r.Decided
	c.FATCA = &d
	return nil
}

// fatcaChangeProof checks the signed self-certification behind a change
// from staff: a document of that kind on the client's file. From the
// portal, the client's own sign-in and declaration are the affirmation.
func (o *Ops) fatcaChangeProof(ctx context.Context, tenant, ucc, src, doc string) error {
	if src == "Portal" {
		return nil
	}
	ds, _ := o.ClientDocs(ctx, tenant, ucc)
	for _, d := range ds {
		if d.ID == doc && d.Kind == DocFATCA {
			return nil
		}
	}
	return invalid("attach the client's signed and dated self-certification (a %s document)", DocFATCA)
}

// fatcaSummaryOfNew is the change request's readable new value.
func fatcaSummaryOfNew(c Client, values map[string]string) string {
	var d FATCADecl
	if json.Unmarshal([]byte(values["fatca"]), &d) != nil {
		return ""
	}
	d.restoreMasked(c.FATCA)
	d.normalise()
	return fatcaSummary(d.masked())
}

// ResolveFATCAWarning records what the KYC desk checked for a warning.
func (o *Ops) ResolveFATCAWarning(ctx context.Context, tenant, actor, ucc, code, note string) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Resolving FATCA/CRS warnings", RoleChecker, RoleCompliance); err != nil {
		return Client{}, err
	}
	note = clean(note)
	if len(note) < 10 {
		return Client{}, invalid("say what was checked and what it showed (at least a short sentence)")
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, ucc, actor, func(x *Client) error {
		if x.FATCA == nil {
			return fmt.Errorf("%w: %s has no self-certification on record", ErrState, ucc)
		}
		ws := fatcaWarnings(*x, x.FATCA, o.UCCDetailsFor(ctx, tenant, ucc))
		found := false
		for _, w := range ws {
			if w.Code == code {
				found = true
				if !w.Resolvable {
					return invalid("this warning is cleared by recording a corrected self-certification, not by a note")
				}
			}
		}
		if !found {
			return invalid("there is no such warning for %s", ucc)
		}
		d := *x.FATCA
		var keep []FATCAResolved
		for _, r := range d.Resolved {
			if r.Code != code {
				keep = append(keep, r)
			}
		}
		d.Resolved = append(keep, FATCAResolved{Code: code, Note: note, By: actor, At: o.now().Format(time.RFC3339)})
		x.FATCA = &d
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "fatca.warning.resolved", ucc, map[string]any{"code": code, "note": note})
	}
	return c, err
}

// ---- the KYC desk's view ----

// FATCARow is one client on the FATCA/CRS tab.
type FATCARow struct {
	UCC       string         `json:"ucc"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	Status    string         `json:"status"`
	PAN       string         `json:"pan"`
	Opened    string         `json:"opened,omitempty"`
	Legacy    bool           `json:"legacy"` // onboarded before 1 Aug 2024
	Has       bool           `json:"has"`
	Summary   string         `json:"summary"`
	KRA       string         `json:"kra"` // in words
	KRAState  string         `json:"kra_state"`
	Warnings  []FATCAWarning `json:"warnings"`
	Open      int            `json:"open"` // warnings not resolved
	Pending   string         `json:"pending,omitempty"`
	Decl      *FATCADecl     `json:"decl,omitempty"` // masked
	LegacyFor string         `json:"legacy_state,omitempty"`
}

// KRABatch is a KRA upload file made for staff to upload, so that marking
// it uploaded records which certifications went.
type KRABatch struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"` // KYC upload, Legacy FATCA/CRS
	Since      string   `json:"since,omitempty"`
	Name       string   `json:"name"`
	UCCs       []string `json:"uccs"`
	Clients    int      `json:"clients"`
	NoCert     int      `json:"no_cert,omitempty"` // clients in the file without a self-certification
	Made       string   `json:"made"`
	By         string   `json:"by"`
	Uploaded   string   `json:"uploaded,omitempty"`
	Ref        string   `json:"ref,omitempty"`
	UploadedBy string   `json:"uploaded_by,omitempty"`
}

const cKRABatch = "krabatch"

// Kinds of KRA file.
const (
	KRABatchKYC    = "KYC upload"
	KRABatchLegacy = "Legacy FATCA/CRS"
)

// FATCAView is the FATCA/CRS tab of KYC & PMLA.
type FATCAView struct {
	Date    string         `json:"date"`
	Rows    []FATCARow     `json:"rows"`
	Counts  map[string]int `json:"counts"`
	Batches []KRABatch     `json:"batches"`
	Codes   FATCACodes     `json:"codes"`
	Layout  string         `json:"layout"` // which KRA format the columns follow
}

// legacyClient says whether a client was onboarded before FATCA/CRS went
// with the KYC upload (or came from the previous system without a date).
func legacyClient(c Client) bool {
	return c.KYCDate == "" || c.KYCDate < FATCAKRAFrom
}

// fatcaAtKRA says whether a client's certification is at the KRA (or was
// marked uploaded there).
func fatcaAtKRA(d *FATCADecl) bool {
	return d != nil && (d.KRA == FATCAKRAUploaded || d.KRA == FATCAKRAAccepted)
}

func fatcaKRAWords(d *FATCADecl) string {
	switch {
	case d == nil:
		return "Nothing to send"
	case d.KRA == FATCAKRAAccepted:
		return "Accepted by the KRA" + map[bool]string{true: " on " + fmtDay(d.KRAOn), false: ""}[d.KRAOn != ""]
	case d.KRA == FATCAKRAUploaded:
		return "Uploaded " + fmtDay(d.KRAOn) + map[bool]string{true: " · " + d.KRARef, false: ""}[d.KRARef != ""]
	case d.KRA == FATCAKRARejected:
		return "Rejected by the KRA" + map[bool]string{true: ": " + d.KRARemark, false: ""}[d.KRARemark != ""]
	}
	return "Not yet at the KRA"
}

// FATCA builds the FATCA/CRS tab.
func (o *Ops) FATCA(ctx context.Context, tenant, date string) (FATCAView, error) {
	v := FATCAView{Date: date, Rows: []FATCARow{}, Counts: map[string]int{}, Batches: []KRABatch{}, Codes: FATCACodeLists(), Layout: kraLayoutNote}
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return v, err
	}
	src := o.uccSources(ctx, tenant)
	pend := map[string]string{}
	crs, _ := docs.List[ChangeRequest](ctx, o.Docs, tenant, cChange, "")
	for _, r := range crs {
		if r.Kind == ChangeFATCA && r.Status == "Pending" {
			pend[r.UCC] = r.ID
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].UCC < cs[j].UCC })
	for _, c := range cs {
		if c.Status == "Closed" || c.Status == "Rejected" {
			continue
		}
		r := FATCARow{UCC: c.UCC, Name: c.Name, Type: c.Type, Status: c.Status, PAN: MaskPAN(c.PAN), Opened: c.KYCDate, Legacy: legacyClient(c), Has: c.FATCA != nil,
			Summary: fatcaSummary(c.FATCA.masked()), KRA: fatcaKRAWords(c.FATCA), KRAState: "none", Pending: pend[c.UCC], Decl: c.FATCA.masked(), Warnings: []FATCAWarning{}}
		if c.FATCA != nil {
			r.KRAState = firstOf(strings.ToLower(c.FATCA.KRA), "not-sent")
			if ws := fatcaWarnings(c, c.FATCA, src.details[c.UCC]); ws != nil {
				r.Warnings = ws
			}
			r.Open = openWarnings(r.Warnings)
		}
		switch {
		case !r.Has:
			v.Counts["No self-certification"]++
		case r.Open > 0:
			v.Counts["Warnings to resolve"]++
		}
		if r.Has && !fatcaAtKRA(c.FATCA) {
			v.Counts["Not at the KRA"]++
		}
		if r.Legacy {
			switch {
			case !r.Has:
				r.LegacyFor = "Collect the self-certification"
			case c.FATCA.KRA == FATCAKRAAccepted:
				r.LegacyFor = "Done"
			case c.FATCA.KRA == FATCAKRAUploaded:
				r.LegacyFor = "Uploaded, waiting for the KRA's status"
			case c.FATCA.SelfCert == "N":
				r.LegacyFor = "Collect a valid self-certification"
			default:
				r.LegacyFor = "Ready for the bulk file"
			}
			if r.LegacyFor != "Done" {
				v.Counts["Legacy to do"]++
			}
		}
		if c.FATCA != nil && c.FATCA.USPerson == "Y" {
			v.Counts["US persons"]++
		}
		if c.FATCA != nil && !c.FATCA.indiaOnly() {
			v.Counts["Foreign tax residents"]++
		}
		v.Rows = append(v.Rows, r)
	}
	bs, _ := docs.List[KRABatch](ctx, o.Docs, tenant, cKRABatch, "")
	sort.Slice(bs, func(i, j int) bool { return bs[i].ID > bs[j].ID })
	if len(bs) > 20 {
		bs = bs[:20]
	}
	for i := range bs {
		bs[i].Clients, bs[i].UCCs = len(bs[i].UCCs), nil // the count is enough on the screen
	}
	v.Batches = append(v.Batches, bs...)
	return v, nil
}

// ---- the KRA files ----

// kraLayoutNote says which KRA format the FATCA/CRS columns follow.
const kraLayoutNote = "The FATCA/CRS columns are named as KFintech KRA's circular KARVY/OPS/INTERM/GENRL/24-07 of 11 June 2024 names its fields (Place of Birth, Country of Birth, Tax Residency other than India?, Country of Tax Residency1-4, Tax Identification Number1-4, Date of Declaration). The TIN exemption reason, TIN issuing country and the fields of SEBI's circular of 11 November 2026 are not named in a KRA format yet: their columns are Ledgerline's names, to confirm with your KRA (layout kra-kyc-upload in config/formats)."

// kraSpec returns a KRA layout.
func kraSpec(id string) (*xfile.Spec, error) {
	lib, err := XFiles()
	if err != nil {
		return nil, err
	}
	s, ok := lib.Get(id)
	if !ok {
		return nil, fmt.Errorf("the KRA layout %s is missing from config/formats", id)
	}
	return s, nil
}

// kraDate writes a date as the KRA's dd-mm-yyyy.
func kraDate(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return ""
	}
	return t.Format("02-01-2006")
}

// kraRow is one client's line of the KRA upload file.
func kraRow(c Client, change string) xfile.Row {
	ipv := "Y"
	if strings.Contains(c.KYCRoute, "DigiLocker") || strings.Contains(c.KYCRoute, "offline e-KYC") {
		ipv = "NA" // not required: Aadhaar via DigiLocker or UIDAI's offline e-KYC
	}
	v := map[string]string{"PAN": c.PAN, "Name": c.Name, "DOB": c.DOB, "Mobile": c.Mobile, "Email": c.Email, "City": c.City, "State": c.State, "KYCMode": c.KYCRoute,
		"IPVDone": ipv, "AccountOpened": c.KYCDate, "Change": change, "UCC": c.UCC}
	if d := c.FATCA; d != nil {
		v["PlaceOfBirth"], v["CountryOfBirth"], v["TaxResidencyOtherThanIndia"], v["DateOfDeclaration"] = d.PlaceOfBirth, d.CountryOfBirth, d.OtherThanIndia, kraDate(d.DeclaredOn)
		for i, r := range d.Residences {
			if i >= MaxTaxCountries {
				break
			}
			n := fmt.Sprint(i + 1)
			v["CountryOfTaxResidency"+n], v["TaxIdentificationNumber"+n], v["TINExemptionReason"+n] = r.Country, r.TIN, r.NoTIN
			if r.TIN != "" {
				v["TINIssuingCountry"+n] = firstOf(r.TINCountry, r.Country)
			}
		}
		v["Nationality"], v["USPerson"], v["SelfCertificationValid"], v["AccountType"], v["AccountStatus"] = d.Nationality, d.USPerson, d.SelfCert, d.AccountType, d.AccountStatus
		v["ControllingPersons"] = fmt.Sprint(len(d.ControllingPersons))
	}
	return xfile.Row{Values: v}
}

// kraUpload builds the KRA upload file of the given clients (nil: new
// accounts and changed details since `since`), with the clients it holds
// and the number without a self-certification.
func (o *Ops) kraUpload(ctx context.Context, tenant, since string, pick func(Client) string) ([]byte, []string, int, error) {
	s, err := kraSpec("kra-kyc-upload")
	if err != nil {
		return nil, nil, 0, err
	}
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, nil, 0, err
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].UCC < cs[j].UCC })
	if pick == nil {
		pick = func(c Client) string {
			switch {
			case c.KYCDate >= since && c.KRAStatus == "":
				return "New"
			case c.KYCUpdatedOn >= since && c.KYCUpdatedOn != "":
				return "Modify"
			}
			return ""
		}
	}
	var rows []xfile.Row
	var in []string
	noCert := 0
	for _, c := range cs {
		ch := pick(c)
		if ch == "" {
			continue
		}
		rows = append(rows, kraRow(c, ch))
		in = append(in, c.UCC)
		if c.FATCA == nil {
			noCert++
		}
	}
	b, err := s.Write(rows)
	return b, in, noCert, err
}

// KRAUploadFile lists clients whose KYC must be uploaded to the KRA: new
// accounts and changed details since `since`, with their FATCA/CRS
// self-certification (layout kra-kyc-upload).
func (o *Ops) KRAUploadFile(ctx context.Context, tenant, since string) ([]byte, error) {
	b, _, _, err := o.kraUpload(ctx, tenant, since, nil)
	return b, err
}

// KRAFile is a KRA file made for download.
type KRAFile struct {
	Name  string
	Data  []byte
	Batch KRABatch
	Note  string // what the person should know (clients the KRA will reject)
}

func (o *Ops) saveKRABatch(ctx context.Context, tenant, actor, kind, since string, uccs []string, noCert int) (KRABatch, error) {
	n, err := o.next(ctx, tenant, "krabatch", actor)
	if err != nil {
		return KRABatch{}, err
	}
	b := KRABatch{ID: fmt.Sprintf("KRA-%06d", n), Kind: kind, Since: since, UCCs: uccs, NoCert: noCert, Made: o.now().Format(time.RFC3339), By: actor}
	if b.UCCs == nil {
		b.UCCs = []string{}
	}
	switch kind {
	case KRABatchLegacy:
		b.Name = fmt.Sprintf("kra_fatca_legacy_%s.csv", b.ID)
	default:
		b.Name = fmt.Sprintf("kra_upload_%s_%s.csv", since, b.ID)
	}
	_, err = docs.Put(ctx, o.Docs, tenant, cKRABatch, b.ID, b, 0, actor)
	return b, err
}

func kraNote(noCert int) string {
	if noCert == 0 {
		return ""
	}
	return fmt.Sprintf("%d client%s in the file %s no FATCA/CRS self-certification; the KRA rejects a record without one, so record it on KYC & PMLA → FATCA / CRS", noCert, map[bool]string{true: "", false: "s"}[noCert == 1], map[bool]string{true: "has", false: "have"}[noCert == 1])
}

// KRAUploadBatch makes the KRA upload file for staff to upload and records
// which clients are in it, so that it can be marked uploaded.
func (o *Ops) KRAUploadBatch(ctx context.Context, tenant, actor, since string) (KRAFile, error) {
	if err := o.require(ctx, tenant, actor, "Making KRA upload files", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return KRAFile{}, err
	}
	data, in, noCert, err := o.kraUpload(ctx, tenant, since, nil)
	if err != nil {
		return KRAFile{}, err
	}
	f := KRAFile{Name: "kra_upload_" + since + ".csv", Data: data, Note: kraNote(noCert)}
	if len(in) > 0 {
		if f.Batch, err = o.saveKRABatch(ctx, tenant, actor, KRABatchKYC, since, in, noCert); err != nil {
			return f, err
		}
		f.Name = f.Batch.Name
	} else {
		f.Note = "no client was opened or changed since " + fmtDay(since)
	}
	o.log(ctx, tenant, actor, "kra.upload_file", since, map[string]any{"clients": len(in), "batch": f.Batch.ID, "no_self_certification": noCert})
	return f, nil
}

// FATCALegacyFile makes the bulk file of clients onboarded before 1 August
// 2024 whose self-certification is on record but not at the KRA, as
// modifications (KARVY/OPS/INTERM/GENRL/24-07: an updated certification is
// a modification; KARVY/OPS/INTERM/GENRL/24-009: legacy cases).
func (o *Ops) FATCALegacyFile(ctx context.Context, tenant, actor string) (KRAFile, error) {
	if err := o.require(ctx, tenant, actor, "Making KRA upload files", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return KRAFile{}, err
	}
	data, in, _, err := o.kraUpload(ctx, tenant, "", func(c Client) string {
		if c.Status == "Closed" || c.Status == "Rejected" || !legacyClient(c) || c.FATCA == nil || fatcaAtKRA(c.FATCA) || c.FATCA.SelfCert != "Y" {
			return ""
		}
		return "Modify"
	})
	if err != nil {
		return KRAFile{}, err
	}
	if len(in) == 0 {
		return KRAFile{}, fmt.Errorf("%w: no legacy client has a self-certification waiting for the KRA", ErrState)
	}
	b, err := o.saveKRABatch(ctx, tenant, actor, KRABatchLegacy, "", in, 0)
	if err != nil {
		return KRAFile{}, err
	}
	o.log(ctx, tenant, actor, "kra.fatca_legacy_file", b.ID, map[string]any{"clients": len(in)})
	return KRAFile{Name: b.Name, Data: data, Batch: b}, nil
}

// KRAControllingPersonsFile lists the controlling persons of the
// non-individual clients in a KRA file (layout
// kra-fatca-controlling-persons): one line a person.
func (o *Ops) KRAControllingPersonsFile(ctx context.Context, tenant, actor, batch string) ([]byte, int, error) {
	if err := o.require(ctx, tenant, actor, "Making KRA upload files", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return nil, 0, err
	}
	b, _, err := docs.Get[KRABatch](ctx, o.Docs, tenant, cKRABatch, batch)
	if err != nil {
		return nil, 0, err
	}
	s, err := kraSpec("kra-fatca-controlling-persons")
	if err != nil {
		return nil, 0, err
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	var rows []xfile.Row
	for _, u := range b.UCCs {
		c, ok := cs[u]
		if !ok || c.FATCA == nil {
			continue
		}
		for i, p := range c.FATCA.ControllingPersons {
			v := map[string]string{"PAN": c.PAN, "UCC": c.UCC, "Name": c.Name, "Serial": fmt.Sprint(i + 1), "PersonName": p.Name, "PersonType": p.Type, "PersonPAN": p.PAN,
				"PersonDOB": kraDate(p.DOB), "PersonPlaceOfBirth": p.PlaceOfBirth, "PersonCountryOfBirth": p.CountryOfBirth, "PersonNationality": p.Nationality,
				"TaxResidencyOtherThanIndia": p.OtherThanIndia, "SelfCertificationValid": p.SelfCert}
			for j, r := range p.Residences {
				if j >= MaxTaxCountries {
					break
				}
				n := fmt.Sprint(j + 1)
				v["CountryOfTaxResidency"+n], v["TaxIdentificationNumber"+n], v["TINExemptionReason"+n] = r.Country, r.TIN, r.NoTIN
			}
			rows = append(rows, xfile.Row{Values: v})
		}
	}
	data, err := s.Write(rows)
	if err == nil {
		o.log(ctx, tenant, actor, "kra.controlling_persons_file", b.ID, map[string]any{"persons": len(rows)})
	}
	return data, len(rows), err
}

// MarkKRABatchUploaded records that a KRA file was uploaded at the KRA:
// each certification in it (unless it changed since) is then Uploaded.
func (o *Ops) MarkKRABatchUploaded(ctx context.Context, tenant, actor, id, date, ref string) (KRABatch, error) {
	if err := o.require(ctx, tenant, actor, "Marking KRA files uploaded", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return KRABatch{}, err
	}
	ref = clean(ref)
	if !ledger.ValidDate(date) || date > o.now().Format("2006-01-02") {
		return KRABatch{}, invalid("give the day the file was uploaded at the KRA")
	}
	if ref == "" {
		return KRABatch{}, invalid("give the KRA's reference for the upload (batch or acknowledgement number)")
	}
	b, err := docs.Update(ctx, o.Docs, tenant, cKRABatch, id, actor, func(x *KRABatch) error {
		if x.Uploaded != "" {
			return fmt.Errorf("%w: %s was marked uploaded on %s", ErrState, x.ID, fmtDay(x.Uploaded))
		}
		x.Uploaded, x.Ref, x.UploadedBy = date, ref, actor
		return nil
	})
	if err != nil {
		return b, err
	}
	n := 0
	for _, u := range b.UCCs {
		_, err := docs.Update(ctx, o.Docs, tenant, cClient, u, actor, func(c *Client) error {
			// Only the certification that was in the file: one recorded
			// after it was made goes in a later file.
			if c.FATCA == nil || c.FATCA.At > b.Made || c.FATCA.KRA == FATCAKRAAccepted {
				return errSkip
			}
			d := *c.FATCA
			d.KRA, d.KRAOn, d.KRARef, d.KRARemark = FATCAKRAUploaded, date, ref, ""
			c.FATCA = &d
			return nil
		})
		switch {
		case err == nil:
			n++
		case err != errSkip:
			return b, err
		}
	}
	o.log(ctx, tenant, actor, "kra.batch.uploaded", b.ID, map[string]any{"kind": b.Kind, "ref": ref, "date": date, "certifications": n})
	return b, nil
}

var errSkip = fmt.Errorf("skip")

// noteKRAFATCA applies the FATCA column of the KRA status file: Y the
// certification is on record at the KRA, N it is not (with the remark).
func noteKRAFATCA(c *Client, flag, remark, date string) bool {
	if c.FATCA == nil {
		return false
	}
	d := *c.FATCA
	switch fatcaYN(flag) {
	case "Y":
		d.KRA, d.KRARemark = FATCAKRAAccepted, ""
	case "N":
		d.KRA, d.KRARemark = FATCAKRARejected, firstOf(remark, "FATCA/CRS not on record at the KRA")
	default:
		return false
	}
	d.KRAOn = date
	c.FATCA = &d
	return true
}

// ---- onboarding ----

// requireFATCA checks a new client's self-certification and fills its
// defaults (SEBI circular of 11 November 2026, para 6).
func requireFATCA(c *Client, actor, mode, now string) error {
	if c.FATCA == nil {
		return &ValidationError{[]FieldError{{"fatca", "Record the client's FATCA/CRS self-certification: tax residency, place and country of birth, nationality"}}}
	}
	d := *c.FATCA
	if d.Mode == "" {
		d.Mode = mode
	}
	if err := prepareFATCA(&d, *c, now[:10]); err != nil {
		return err
	}
	d.Resolved, d.KRA, d.KRAOn, d.KRARef, d.KRARemark = nil, "", "", "", ""
	d.By, d.At = actor, now
	c.FATCA = &d
	return nil
}

// ---- the legacy import ----

// prepareFATCAImport reads the FATCA-CRS file: self-certifications the
// previous system or the firm's paper files hold, one row a client (rows
// that repeat the UCC add controlling persons). A client who already has
// a self-certification is left as it is: change it by change request.
func (o *Ops) prepareFATCAImport(ctx context.Context, tenant, actor string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	bad := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	cs, _ := o.ClientMap(ctx, tenant)
	today := o.now().Format("2006-01-02")
	decls := map[string]*FATCADecl{}
	lines := map[string]int{}
	var order []string
	for _, r := range rows {
		u := upperTrim(r.get("UCC"))
		if _, ok := cs[u]; !ok {
			bad(r.line, "UCC", "%s is not a client", u)
			continue
		}
		d := decls[u]
		if d == nil {
			d = &FATCADecl{OtherThanIndia: r.get("OtherThanIndia"), PlaceOfBirth: r.get("PlaceOfBirth"), CountryOfBirth: r.get("CountryOfBirth"), Nationality: r.get("Nationality"),
				USPerson: r.get("USPerson"), SelfCert: r.get("SelfCertValid"), DeclaredOn: r.get("DeclaredOn"), AccountType: r.get("AccountType"), AccountStatus: r.get("AccountStatus"),
				Mode: firstOf(r.get("Mode"), FATCAModeImported)}
			// AtKRA Y: the previous system already uploaded it to the KRA.
			switch fatcaYN(r.get("AtKRA")) {
			case "Y":
				d.KRA, d.KRARef = FATCAKRAAccepted, "uploaded by the previous system"
			case "":
				if r.get("AtKRA") != "" {
					bad(r.line, "AtKRA", "is Y (already at the KRA), N or empty")
				}
			}
			for i := 1; i <= MaxTaxCountries; i++ {
				n := fmt.Sprint(i)
				d.Residences = append(d.Residences, TaxResidence{Country: r.get("Country" + n), TIN: r.get("TIN" + n), NoTIN: r.get("NoTINReason" + n), TINCountry: r.get("TINCountry" + n)})
			}
			decls[u] = d
			lines[u] = r.line
			order = append(order, u)
			if fatcaYN(d.OtherThanIndia) == "" {
				bad(r.line, "OtherThanIndia", "is Y (tax resident of other countries too) or N (India only)")
			}
		}
		if name := r.get("CPName"); name != "" {
			p := ControllingPerson{Name: name, Type: r.get("CPType"), PAN: r.get("CPPAN"), DOB: r.get("CPDOB"), CountryOfBirth: r.get("CPCountryOfBirth"), Nationality: r.get("CPNationality"),
				OtherThanIndia: r.get("CPOtherThanIndia"), SelfCert: r.get("CPSelfCertValid")}
			if ct := r.get("CPCountry1"); ct != "" {
				p.Residences = []TaxResidence{{Country: ct, TIN: r.get("CPTIN1"), NoTIN: r.get("CPNoTINReason1")}}
			}
			d.ControllingPersons = append(d.ControllingPersons, p)
		}
	}
	var skip []string
	for _, u := range order {
		c := cs[u]
		d := decls[u]
		d.normalise()
		d.withDefaults(c.KYCDate, today)
		for _, e := range d.problems(c.Type, today, false) {
			bad(lines[u], strings.TrimPrefix(e.Field, "fatca."), "%s: %s", u, e.Msg)
		}
		if c.FATCA != nil {
			skip = append(skip, u)
		}
	}
	return func() (int, int, error) {
		n, dup := 0, 0
		now := o.now().Format(time.RFC3339)
		for _, u := range order {
			if contains(skip, u) {
				dup++
				continue
			}
			d := *decls[u]
			d.By, d.At = actor, now
			if _, err := docs.Update(ctx, o.Docs, tenant, cClient, u, actor, func(x *Client) error {
				if x.FATCA != nil {
					return errSkip
				}
				x.FATCA = &d
				return nil
			}); err != nil {
				if err == errSkip {
					dup++
					continue
				}
				return n, dup, err
			}
			n++
		}
		o.log(ctx, tenant, actor, "fatca.imported", "", map[string]any{"clients": n, "kept": dup})
		return n, dup, nil
	}, errs
}

// ---- self-inspection ----

func siFATCANew(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Clients' FATCA/CRS self-certifications", e.date)
	ds := map[string]UCCDetails{}
	if dd, err := docs.List[UCCDetails](e.ctx, e.o.Docs, e.tenant, cUCCDetails, ""); err == nil {
		for _, d := range dd {
			ds[d.UCC] = d
		}
	}
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if c.Status != "Active" || (legacyClient(c) && c.FATCA == nil) {
			continue // legacy clients without one: fatca_legacy
		}
		s.checked++
		var why []string
		switch {
		case c.FATCA == nil:
			why = append(why, "no self-certification")
		default:
			if n := openWarnings(fatcaWarnings(c, c.FATCA, ds[u])); n > 0 {
				why = append(why, fmt.Sprintf("%d warning%s to resolve", n, map[bool]string{true: "", false: "s"}[n == 1]))
			}
			// The KRA upload of a legacy client's certification is
			// fatca_legacy's.
			if at := c.FATCA.At[:min(10, len(c.FATCA.At))]; !legacyClient(c) && !fatcaAtKRA(c.FATCA) && validDate(at) && businessDaysBetween(e.set, at, e.date) > fatcaKRADays {
				why = append(why, "not at the KRA since "+fmtDay(at))
			}
		}
		if len(why) > 0 {
			s.bad(siClient(u) + " · " + strings.Join(why, "; "))
		}
	}
	s.summary = fmt.Sprintf("%d active clients' self-certifications checked; %d with something to do.", s.checked, s.found)
	return s, nil
}

func siFATCALegacy(e *siEnv) (siOut, error) {
	var s siOut
	s.input("Clients onboarded before 1 Aug 2024", e.date)
	none, waiting := 0, 0
	for _, u := range sortedKeys(e.clients) {
		c := e.clients[u]
		if c.Status != "Active" || !legacyClient(c) {
			continue
		}
		s.checked++
		switch {
		case c.FATCA == nil:
			none++
			s.bad(siClient(u) + " · no self-certification on record")
		case !fatcaAtKRA(c.FATCA):
			waiting++
			s.bad(siClient(u) + " · " + strings.ToLower(fatcaKRAWords(c.FATCA)))
		}
	}
	s.summary = fmt.Sprintf("%d legacy clients checked: %d without a self-certification, %d not yet at the KRA.", s.checked, none, waiting)
	return s, nil
}
