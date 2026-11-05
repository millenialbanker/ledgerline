package ops

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/xfile"
)

// ---- MCX UCC: mapping a client to MCX's UCC record ----
//
// MCX has no UCC API: new and modified client codes go in the UCC file
// (MCX/TECH/023/2022, MCX UCC File Formats v1.0, section 1.1): a control
// record and one 240-field detail record per client, comma-separated, named
// MCX_DDMMYYYY_UCC<member>.Mnn. The file has no add or modify flag: a
// client code MCX already holds is a modification. MCX/S&I/663/2024 made
// the client's e-mail mandatory for every category, with the relationship
// fields (236 to 239) where the mobile or e-mail is not the client's own.
//
// Where each value comes from: the client record (code, name, PAN, DOB,
// mobile, e-mail, bank, demat, income, PEP, segments and inactivity), the
// Exchange UCC details (address lines, gender, father's or spouse's name,
// marital status, occupation, income date, bank account type, whose the
// mobile and e-mail are), the online application, the MF profile (address
// and bank account type when nothing else gives them), and MCX's
// State-City Code master for fields 15 and 22 (mcxucc.go).
//
// Only resident individuals (category 01) are written. Ledgerline keeps no
// Aadhaar (field 28 and the contact persons' UID stay empty), no CIN,
// registration details or contact persons a non-individual needs, and does
// not register NRIs on MCX: those clients are reported as problems, to be
// registered on MCX's UCC portal. Values are never guessed: a client with
// anything missing is listed with the problem and left out of the file.

// The detail record's fields used here (the keys of config/formats/mcx-ucc.json).
const (
	mxCode, mxName, mxCategory, mxPAN, mxGender, mxGuardian, mxMarital, mxNationality, mxNationalityOther = "ClientCode", "ClientName", "Category", "Pan", "Gender", "FatherSHusbandSGuardianSName", "MaritalStatus", "Nationality", "NationalityOther"
	mxEmail, mxAddr1, mxAddr2, mxAddr3, mxCountry, mxStateCity, mxPin, mxPermSame                         = "ClientEmailId", "Address1", "Address2", "Address3", "Country", "StateCityCode", "PinCode", "PermanentSameAsCorrespondence"
	mxPAddr1, mxPAddr2, mxPAddr3, mxPCountry, mxPStateCity, mxPPin                                        = "PermanentAddress1", "PermanentAddress2", "PermanentAddress3", "PermanentCountry", "PermanentStateCityCode", "PermanentPinCode"
	mxMobile, mxDOB, mxUID, mxBank1, mxBranch1, mxAcct1, mxAcctType1                                      = "MobileNumber", "DateOfBirthIncorporation", "UidNumber", "BankName1", "BankBranch1", "BankAccountNumber1", "BankAccountType1"
	mxDep1, mxDepID1, mxBO1, mxOccupation, mxOccDetail, mxIncome, mxIncomeDate, mxPEP                     = "Depository1", "DepositoryId1", "BeneficiaryAccountId1", "Occupation", "OccupationDetails", "IncomeRange", "IncomeAsOnDate", "PoliticallyExposedPerson"
	mxIPV, mxAgreement, mxStatus, mxRemarks, mxCorporate                                                  = "InPersonVerification", "ClientAgreementDate", "ClientStatus", "Remarks", "ClientTypeCorporate"
	mxRelMobile, mxNameMobile, mxRelEmail, mxNameEmail, mxFlag                                            = "RelationshipWithClientMobile", "NameOfAuthorisedContactPersonMobile", "RelationshipWithClientEmail", "NameOfAuthorisedContactPersonEmail", "Flag"
)

// mcxUCCMax is each written field's maximum length (section 1.1).
var mcxUCCMax = map[string]int{mxCode: 10, mxName: 150, mxGuardian: 150, mxEmail: 60, mxAddr1: 255, mxAddr2: 255, mxAddr3: 255, mxStateCity: 50, mxPin: 6,
	mxPAddr1: 255, mxPAddr2: 255, mxPAddr3: 255, mxPStateCity: 50, mxPPin: 6, mxMobile: 10, mxBank1: 60, mxBranch1: 255, mxAcct1: 25, mxBO1: 16, mxOccDetail: 75, mxRemarks: 200}

var mcxUCCLabel = map[string]string{mxCode: "The client code", mxName: "The name", mxGuardian: "The father's or spouse's name", mxEmail: "The e-mail address",
	mxAddr1: "Address line 1", mxAddr2: "Address line 2", mxAddr3: "Address line 3", mxPAddr1: "Permanent address line 1", mxPAddr2: "Permanent address line 2",
	mxPAddr3: "Permanent address line 3", mxBank1: "The bank's name", mxBranch1: "The bank branch", mxAcct1: "The bank account number", mxOccDetail: "The occupation details",
	mxRemarks: "The status remarks", mxStateCity: "The State-City code", mxPStateCity: "The permanent State-City code"}

func mcxLabel(k string) string { return firstOf(mcxUCCLabel[k], k) }

var (
	reMCXCode = regexp.MustCompile(`^[A-Za-z0-9 ]{1,10}$`)
	// Names: letters, digits, space and . ' - ( ) & only (fields 2, 6, 237, 239).
	reMCXName = regexp.MustCompile(`^[A-Za-z0-9 .'\-()&]+$`)
	reMCXPin  = regexp.MustCompile(`^[0-9]{6}$`)
)

// mcxUCCRecord is one client's detail record and what is wrong with it.
type mcxUCCRecord struct {
	Values   map[string]string // by the layout's key; absent is empty
	Problems []string
	Summary  map[string]string // what MCX holds that a later change is judged against (status)
}

// Hash identifies the record's content.
func (r mcxUCCRecord) Hash() string {
	b, _ := json.Marshal(r.Values) // map keys are sorted
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Masked is the record for the screen: PAN and bank account masked.
func (r mcxUCCRecord) Masked() map[string]string {
	out := map[string]string{}
	for k, v := range r.Values {
		out[k] = v
	}
	if out[mxAcct1] != "" {
		out[mxAcct1] = MaskAccount(out[mxAcct1])
	}
	if out[mxPAN] != "" {
		out[mxPAN] = MaskPAN(out[mxPAN])
	}
	return out
}

// mcxUCCSources are the records a client's UCC is made from beyond the
// client itself, read once per run: BSE's sources (applications with an
// address, MF profiles, NRI details, the firm's PAN), the Exchange UCC
// details and latest applications, MCX's State-City master, and which
// clients share a mobile number or e-mail address.
type mcxUCCSources struct {
	bse     bseUCCSources
	ucc     uccSources
	cities  *MCXStateCityMaster // nil: not loaded
	mobiles map[string][]string
	emails  map[string][]string
}

// mcxDate is DDMMYYYY.
func mcxDate(d string) string {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(d))
	if err != nil {
		return ""
	}
	return t.Format("02012006")
}

// mcxClean makes a value safe for MCX's comma-separated file: commas
// become spaces (the specification says nothing of quoting, so a quoted
// field is not relied on) and runs of spaces become one.
func mcxClean(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, ",", " ")), " ")
}

// mcxRelation is the relationship code of fields 236 and 238 for an
// individual: 1 self, 2 spouse, 3 dependent children, 4 dependent parent.
func mcxRelation(s string) string {
	switch strings.ToLower(strings.Join(strings.Fields(s), " ")) {
	case "", "1", "self":
		return "1"
	case "2", "spouse", "wife", "husband":
		return "2"
	case "3", "dependent child", "dependent children", "child", "son", "daughter":
		return "3"
	case "4", "dependent parent", "parent", "father", "mother":
		return "4"
	}
	return ""
}

// mcxBankType is field 32's code: 10 savings, 11 current, 99 others.
func mcxBankType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "10", "savings", "saving", "sb", "sa":
		return "10"
	case "11", "current", "cb", "ca":
		return "11"
	case "99", "other", "others":
		return "99"
	}
	return ""
}

// mcxUCCMap maps a client to MCX's detail record. last is the summary of
// the record MCX last accepted (nil for none); fileDate (YYYY-MM-DD) is the
// file's date, which dates are checked against.
func mcxUCCMap(c Client, src mcxUCCSources, last map[string]string, fileDate string) mcxUCCRecord {
	v := map[string]string{}
	var probs []string
	bad := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }
	d := src.ucc.details[c.UCC]
	app, ok := src.ucc.apps[c.UCC]
	if !ok {
		app = src.bse.apps[c.UCC]
	}
	mf := src.bse.mf[c.UCC]

	v[mxCode] = c.UCC
	if !reMCXCode.MatchString(c.UCC) || strings.HasPrefix(c.UCC, " ") {
		bad("The client code %q is not one MCX takes: up to 10 letters, digits or spaces", c.UCC)
	}
	name := strings.Join(strings.Fields(c.Name), " ")
	v[mxName] = name
	switch {
	case runes(name) < 2:
		bad("The name is missing")
	case !reMCXName.MatchString(name) || !hasLetter(name):
		bad("The name has a character MCX does not take: only letters, digits, spaces and . ' - ( ) &")
	}
	v[mxPAN] = c.PAN
	if !rePAN.MatchString(c.PAN) {
		bad("The PAN is not 5 letters, 4 digits and a letter")
	} else if c.PAN[3] != 'P' && (c.Type == "Individual" || c.Type == "NRI") {
		bad("The PAN's fourth letter is %q: an individual's PAN has P", c.PAN[3:4])
	}

	// Category: resident individuals only (01).
	individual := false
	cat := strings.TrimSpace(d.Category)
	switch {
	case c.Type == "Individual" && (cat == "" || cat == "1" || cat == "01"):
		v[mxCategory], individual = "01", true
	case c.Type == "NRI":
		v[mxCategory] = "02"
		bad("Ledgerline registers only resident individuals in the MCX UCC file: register this NRI on MCX's UCC portal (category 02 needs the nationality and the NRI's details)")
	case c.Type == "HUF" || cat == "3":
		v[mxCategory] = "16"
		bad("MCX needs the karta as contact person (name, designation, PAN, address, phone) for an HUF, which Ledgerline does not send: register this HUF on MCX's UCC portal")
	case c.Type == "Corporate" || cat == "4":
		v[mxCategory] = "13"
		bad("MCX needs the CIN, two directors as contact persons (name, PAN, DIN, address, phone) and the registration details for a body corporate, which Ledgerline does not hold: register this company on MCX's UCC portal")
	default:
		bad("MCX's client category for %q needs details Ledgerline does not hold (contact persons, registration): register this client on MCX's UCC portal", firstOf(cat, c.Type))
	}
	v[mxCorporate] = "N"

	// Individuals: gender, father's or spouse's name, marital status,
	// nationality, occupation, in-person verification. Blank otherwise.
	if individual {
		v[mxGender] = nseUCIGender(firstOf(d.Gender, app.Gender))
		if v[mxGender] == "" {
			bad("The gender is missing: add it under Exchange UCC details")
		}
		g := strings.Join(strings.Fields(firstOf(d.Guardian, app.Father)), " ")
		v[mxGuardian] = g
		switch {
		case g == "":
			bad("The father's or spouse's name is missing: add it under Exchange UCC details")
		case runes(g) < 2 || !reMCXName.MatchString(g) || !hasLetter(g):
			bad("The father's or spouse's name has a character MCX does not take: only letters, digits, spaces and . ' - ( ) &")
		}
		v[mxMarital] = nseUCIMarital(firstOf(d.Marital, app.Marital))
		if v[mxMarital] == "" {
			bad("The marital status is missing: add it under Exchange UCC details")
		}
		switch strings.ToLower(strings.TrimSpace(d.Nationality)) {
		case "", "1", "indian", "india":
			v[mxNationality] = "1"
		default:
			bad("The nationality is %q: Ledgerline registers Indian nationals on MCX (code 1); register this client on MCX's UCC portal", d.Nationality)
		}
		occWord := firstOf(d.Occupation, app.Occupation)
		occ, det := nseUCIOccupation(occWord)
		if occ == "99" && d.OccupationDetail != "" {
			det = d.OccupationDetail
		}
		det = mcxClean(det)
		switch {
		case occ == "" && strings.EqualFold(strings.TrimSpace(occWord), "Salaried"):
			bad("The occupation is \"Salaried\": MCX asks whether it is public sector, private sector or government service; record it under Exchange UCC details")
		case occ == "":
			bad("The occupation is missing or not one of MCX's: add it under Exchange UCC details")
		case occ == "99" && (runes(det) < 4 || !hasLetter(det)):
			bad("The occupation details (for Others) must be 4 to 75 characters")
		}
		if len(occ) == 1 {
			occ = "0" + occ // MCX's codes are two digits (01 to 09, 99)
		}
		v[mxOccupation] = occ
		if occ == "99" {
			v[mxOccDetail] = det
		}
		// In-person verification: always Y (section 1.1, field 65).
		v[mxIPV] = "Y"
		if d.IPV == "N" {
			bad("In-person verification is recorded as not done: MCX takes Y only")
		}
		if c.DOB == "" {
			bad("The date of birth is missing")
		} else if !validDate(c.DOB) || c.DOB > fileDate {
			bad("The date of birth is not a past date")
		} else {
			v[mxDOB] = mcxDate(c.DOB)
		}
	}

	// E-mail (mandatory for all categories since MCX/S&I/663/2024).
	email := strings.TrimSpace(c.Email)
	v[mxEmail] = email
	if email == "" {
		bad("The e-mail address is missing: MCX needs it for every client")
	} else if p := nseUCIEmailProblem(email); p != "" {
		bad("The e-mail address %s", p) // NSE's rules are MCX's (field 10)
	}
	// Mobile (mandatory for Indian categories).
	v[mxMobile] = c.Mobile
	if p := nseUCIMobileProblem(c.Mobile); p != "" {
		bad("The mobile number %s", p)
	}
	// Whose the mobile and e-mail are: the client's own unless another
	// client shares it, when the details must say how they are related.
	rel := func(shared []string, given, what, key string) {
		r := mcxRelation(given)
		if r == "" {
			bad("Whose the %s is (%q) is not Self, Spouse, Dependent child or Dependent parent: correct it under Exchange UCC details", what, given)
			return
		}
		if strings.TrimSpace(given) == "" {
			var others []string
			for _, u := range shared {
				if u != c.UCC {
					others = append(others, u)
				}
			}
			if len(others) > 0 {
				bad("The %s is also client %s's: MCX takes it as one client's own (Self); record whose it is (self, spouse, dependent child or dependent parent) under Exchange UCC details", what, strings.Join(others, ", "))
				return
			}
		}
		v[key] = r
	}
	if individual {
		if c.Mobile != "" {
			rel(src.mobiles[c.Mobile], d.MobileRelation, "mobile number", mxRelMobile)
		}
		if email != "" {
			rel(src.emails[strings.ToLower(email)], d.EmailRelation, "e-mail address", mxRelEmail)
		}
	}

	// Correspondence address: the details' lines, else the online
	// application's or the MF profile's address split into lines.
	lines := [3]string{d.Address1, d.Address2, d.Address3}
	pin := firstOf(d.PIN, app.PIN)
	if strings.TrimSpace(lines[0]) == "" {
		lines = splitAddress(mcxClean(app.Address))
		if strings.TrimSpace(app.Address) == "" {
			lines = splitAddress(mcxClean(mf.Address))
			pin = firstOf(d.PIN, mf.Pincode)
		}
	}
	for i := range lines {
		lines[i] = mcxClean(lines[i])
	}
	v[mxAddr1], v[mxAddr2], v[mxAddr3] = lines[0], lines[1], lines[2]
	switch {
	case lines[0] == "":
		bad("There is no address on record (Exchange UCC details, the online application or the MF profile): add the client's address")
	case runes(lines[0]) < 5 || !hasLetter(lines[0]):
		bad("Address line 1 must be at least 5 characters, not only numbers")
	}
	for i, l := range lines[1:] {
		if l != "" && (runes(l) < 3 || !hasLetter(l)) {
			bad("Address line %d must be at least 3 characters, not only numbers", i+2)
		}
	}
	country := strings.TrimSpace(d.Country)
	if country != "" && country != "85" && !strings.EqualFold(country, "india") {
		bad("The country is %q: MCX takes countries other than India (85) for foreign categories only", country)
	}
	v[mxCountry] = "85"
	city := firstOf(d.City, firstOf(c.City, app.City))
	state := firstOf(d.State, firstOf(app.State, c.State))
	v[mxStateCity] = src.stateCity(state, city, "", bad)
	v[mxPin] = strings.TrimSpace(pin)
	if !reMCXPin.MatchString(v[mxPin]) || strings.Trim(v[mxPin], "0") == "" {
		bad("The PIN code is missing or not 6 digits")
	}
	// Permanent address: the same unless the details give one.
	v[mxPermSame] = "Y"
	if strings.TrimSpace(d.PermAddress1) != "" {
		v[mxPermSame] = "N"
		v[mxPAddr1], v[mxPAddr2], v[mxPAddr3] = mcxClean(d.PermAddress1), mcxClean(d.PermAddress2), mcxClean(d.PermAddress3)
		if runes(v[mxPAddr1]) < 5 || !hasLetter(v[mxPAddr1]) {
			bad("Permanent address line 1 must be at least 5 characters, not only numbers")
		}
		for i, k := range []string{mxPAddr2, mxPAddr3} {
			if l := v[k]; l != "" && (runes(l) < 3 || !hasLetter(l)) {
				bad("Permanent address line %d must be at least 3 characters, not only numbers", i+2)
			}
		}
		if pc := strings.TrimSpace(d.PermCountry); pc != "" && pc != "85" && !strings.EqualFold(pc, "india") {
			bad("The permanent address country is %q: MCX takes India (85) for this category", pc)
		}
		v[mxPCountry] = "85"
		v[mxPStateCity] = src.stateCity(d.PermState, d.PermCity, "permanent ", bad)
		v[mxPPin] = strings.TrimSpace(d.PermPIN)
		if !reMCXPin.MatchString(v[mxPPin]) || strings.Trim(v[mxPPin], "0") == "" {
			bad("The permanent address PIN code is missing or not 6 digits")
		}
	}
	v[mxUID] = "" // Ledgerline keeps no Aadhaar number

	// Bank: one account. The branch "address" is the branch's name with its
	// IFSC, which identifies the branch (Ledgerline keeps no branch address).
	bank := mcxClean(c.Bank.Bank)
	acct := strings.TrimSpace(c.Bank.Account)
	v[mxBank1], v[mxAcct1] = bank, acct
	switch {
	case runes(bank) < 3 || !hasLetter(bank) || strings.HasPrefix(bank, "-"):
		bad("The bank's name is missing or not a name (3 to 60 characters)")
	}
	switch {
	case acct == "" || strings.HasPrefix(acct, "-") || digitsOf(acct) == "":
		bad("The bank account number is missing or has no digits")
	case strings.Trim(digitsOf(acct), "0") == "":
		bad("The bank account number has no digits other than zeros")
	case strings.Trim(acct, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789") != "":
		bad("The bank account number has characters other than letters and digits")
	}
	if !reIFSC.MatchString(c.Bank.IFSC) {
		bad("The IFSC is not 4 letters, 0 and 6 letters or digits")
	} else {
		v[mxBranch1] = mcxClean(strings.TrimSpace(c.Bank.Branch + " IFSC " + c.Bank.IFSC))
	}
	bt := mcxBankType(d.BankType)
	if d.BankType == "" {
		switch strings.ToUpper(mf.AccountType) {
		case "SB":
			bt = "10"
		case "CB":
			bt = "11"
		}
	}
	v[mxAcctType1] = bt
	if bt == "" {
		bad("The bank account type (savings or current) is not on record: set it under Exchange UCC details")
	}

	// Demat: optional at MCX; written when on record. CDSL: the 16-digit BO
	// ID with the depository ID blank; NSDL: the DP ID (IN + 6) and the
	// 8-digit client ID.
	switch dep, id := strings.ToUpper(strings.TrimSpace(c.Demat.Depository)), strings.ToUpper(strings.TrimSpace(c.Demat.ID)); {
	case id == "":
	case dep == "CDSL" && reBSECDSLBO.MatchString(id):
		v[mxDep1], v[mxBO1] = "2", id
	case dep == "NSDL" && reBSENSDLBO.MatchString(id):
		v[mxDep1], v[mxDepID1], v[mxBO1] = "1", id[:8], id[8:]
	case dep == "CDSL":
		bad("The CDSL demat account must be 16 digits starting with 1")
	case dep == "NSDL":
		bad("The NSDL demat account must be IN, 6 digits (the DP ID) and 8 digits (the client ID)")
	default:
		bad("The depository must be CDSL or NSDL")
	}

	// Income (net worth is not held, so income only), declared within 18
	// months of the file's date.
	v[mxIncome] = nseUCIIncome(c.Income, individual)
	if v[mxIncome] == "" {
		bad("The income range %q is not one MCX takes", c.Income)
	} else {
		v[mxIncome] = "0" + v[mxIncome]
	}
	asOn := d.IncomeAsOn
	if asOn == "" {
		for _, x := range []string{c.KYCDate, c.KYCUpdatedOn, c.ReKYCOn} {
			if validDate(x) && x > asOn {
				asOn = x
			}
		}
	}
	fd, _ := time.Parse("2006-01-02", fileDate)
	switch {
	case !validDate(asOn):
		bad("There is no date for the income declaration (account opening, KYC update or re-KYC)")
	case asOn > fileDate:
		bad("The income declaration date is after the file's date")
	case asOn < fd.AddDate(0, -18, 0).Format("2006-01-02"):
		bad("The income was declared on %s, more than 18 months ago: MCX takes it at most 18 months old; record a fresh income declaration (re-KYC)", fmtDay(asOn))
	default:
		v[mxIncomeDate] = mcxDate(asOn)
	}

	// Politically exposed: 0 not, 1 PEP, 2 related to a PEP.
	v[mxPEP] = "0"
	for _, s := range c.CSC {
		switch s {
		case "Politically exposed person":
			v[mxPEP] = "1"
		case "Relative of a PEP":
			if v[mxPEP] == "0" {
				v[mxPEP] = "2"
			}
		}
	}
	if d.PEP != "" {
		v[mxPEP] = d.PEP
	}
	if !contains([]string{"0", "1", "2"}, v[mxPEP]) {
		bad("Politically exposed must be 0, 1 or 2")
	}

	// The client agreement: the details' date, else the account opening.
	agr := firstOf(d.AgreementDate, c.KYCDate)
	switch {
	case agr == "":
	case !validDate(agr) || agr > fileDate:
		if d.AgreementDate != "" {
			bad("The client agreement date is not a past date")
		}
	default:
		v[mxAgreement] = mcxDate(agr)
	}

	// Status at MCX: active while the commodity segment is open and the
	// account is not inactive; remarks when it changes either way (and for
	// a client first registered inactive).
	v[mxStatus], v[mxRemarks] = "1", ""
	com := c.SegmentStatus("COM")
	switch {
	case com == "Not registered":
		v[mxStatus], v[mxRemarks] = "2", "Commodity segment withdrawn"
	case com == SegClosed || com == SegSuspended || com == SegDormant:
		v[mxStatus], v[mxRemarks] = "2", firstOf(c.SegStatus["COM"].Reason, "Commodity segment "+strings.ToLower(com))
	case c.Inactive:
		v[mxStatus], v[mxRemarks] = "2", "No activity for the inactivity period"
		if c.InactiveSince != "" {
			v[mxRemarks] += " since " + fmtDay(c.InactiveSince)
		}
	}
	if v[mxStatus] == "1" {
		switch {
		case last != nil && last["status"] == "2" && c.ReactivatedOn != "":
			v[mxRemarks] = "Reactivated on " + fmtDay(c.ReactivatedOn)
		case last != nil && last["status"] == "2":
			v[mxRemarks] = "Commodity segment active again"
		}
	} else if last != nil && last["status"] == "2" {
		v[mxRemarks] = "" // no change of status: no remarks needed
	}
	v[mxRemarks] = mcxClean(v[mxRemarks])
	if runes(v[mxRemarks]) > 200 {
		v[mxRemarks] = string([]rune(v[mxRemarks])[:200])
	}
	v[mxFlag] = "E"

	// Lengths and plain printable ASCII (a double quote would need quoting).
	for _, k := range sortedKeys(v) {
		x := v[k]
		if n := mcxUCCMax[k]; n > 0 && runes(x) > n {
			bad("%s is longer than MCX's %d characters", mcxLabel(k), n)
		}
		for _, r := range x {
			if r > 126 || r < 32 || r == '"' {
				bad("%s has a character MCX's file cannot take (%q): use plain English letters", mcxLabel(k), r)
				break
			}
		}
	}
	return mcxUCCRecord{Values: v, Problems: dedupe(probs), Summary: map[string]string{"status": v[mxStatus]}}
}

// stateCity is the State-City code of a state and city from MCX's master.
func (s mcxUCCSources) stateCity(state, city, which string, bad func(string, ...any)) string {
	if strings.TrimSpace(city) == "" {
		bad("The %saddress's city is missing", which)
		return ""
	}
	if s.cities == nil || len(s.cities.Codes)+len(s.cities.CityOnly) == 0 {
		bad("State-City code not found in MCX's master (no master loaded): load the latest master on Exchange files")
		return ""
	}
	if code, ok := s.cities.Lookup(state, city); ok {
		return code
	}
	bad("State-City code not found in MCX's master for %s, %s: load the latest master on Exchange files, or correct the %saddress's city and state", strings.ToUpper(strings.TrimSpace(city)), firstOf(strings.ToUpper(strings.TrimSpace(state)), "no state"), which)
	return ""
}

// mcxUCCRows writes the control record and the detail records.
func mcxUCCRows(s *xfile.Spec, member, date string, batch int, recs []mcxUCCRecord) ([]xfile.Row, error) {
	if len(s.Records) != 2 || len(s.Records[1].Columns) != 240 {
		return nil, fmt.Errorf("the MCX UCC layout is not a control record and 240 fields")
	}
	rows := []xfile.Row{{Record: "", Values: map[string]string{"RecordType": "10", "MemberType": "M", "MemberCode": member, "Date": mcxDate(date),
		"BatchNumber": fmt.Sprintf("%02d", batch), "TotalRecords": strconv.Itoa(len(recs))}}}
	for _, r := range recs {
		m := map[string]string{}
		for _, c := range s.Records[1].Columns {
			m[c.Key] = r.Values[c.Key]
		}
		rows = append(rows, xfile.Row{Record: s.Records[1].Code, Values: m})
	}
	return rows, nil
}

// ---- MCX's UCC error codes (section 1.3) ----

// mcxUCCErrors are MCX's error codes in plain words.
var mcxUCCErrors = func() map[string]string {
	m := map[string]string{
		"001": "the file or a record is not valid", "002": "a future-dated file is not taken", "003": "the control record must start with 10",
		"004": "the member type in the control record is not valid", "005": "the file is not this member's: the member code does not match",
		"006": "the control record's date is missing, not DDMMYYYY or not the file name's date", "007": "this batch is already loaded",
		"008": "the control record's batch number is not the file name's", "009": "the previous batch was not loaded: the batch is out of series",
		"010": "the file has no detail records", "011": "the number of detail records differs from the control record", "012": "the control record is not in the format",
		"013": "the client code starts with a blank", "014": "the client code is longer than 10 characters",
		"015": "the client name is missing or not valid (2 to 150 characters)", "016": "the category is longer than 2 characters",
		"017": "the category is not valid, or may not be modified", "018": "the PAN is a duplicate, or is the member's or a contact person's PAN",
		"019": "the PAN is missing or not valid, or a PAN may not be modified", "020": "the PAN must be 10 characters",
		"021": "the gender is missing or not valid", "022": "the gender must be 1 or 2 characters", "023": "the gender must be blank",
		"024": "the father's, husband's or guardian's name is missing or not valid", "025": "the father's, husband's or guardian's name must be 2 to 150 characters",
		"026": "the father's, husband's or guardian's name must be blank", "027": "the marital status is missing or not valid",
		"028": "the marital status must be 1 or 2 characters", "029": "the marital status must be blank", "030": "the nationality is missing or not valid",
		"031": "the other nationality is missing or not 2 to 50 characters", "032": "the other nationality must be blank",
		"033": "the client's e-mail address is missing or not valid", "034": "the client's e-mail address must be 5 to 60 characters",
		"035": "correspondence address line 1 is missing or not valid", "036": "correspondence address line 2 is not valid", "037": "correspondence address line 3 is not valid",
		"038": "the correspondence country code is missing or not valid", "039": "the correspondence State-City code is missing or not in MCX's master",
		"040": "the correspondence State-City code must be 2 to 50 characters", "041": "the correspondence PIN code is missing or not valid",
		"042": "the PIN code must be 6 to 10 characters", "043": "permanent address same as correspondence is missing or not valid",
		"044": "permanent address line 1 is missing or not valid", "045": "permanent address line 2 is not valid", "046": "permanent address line 3 is not valid",
		"047": "the permanent country code is missing or not valid", "048": "the permanent State-City code is missing or not in MCX's master",
		"049": "the permanent State-City code must be 2 to 50 characters", "050": "the permanent PIN code is missing or not valid",
		"051": "the permanent PIN code must be 6 to 10 characters", "052": "the mobile number is missing or not valid",
		"053": "the date of birth or incorporation is missing or not valid", "054": "the UID is not valid (12 digits)",
		"055": "bank account 1 is incomplete", "056": "bank name 1 is missing or not valid (3 to 60 characters)", "057": "bank branch address 1 is missing or not valid",
		"058": "bank account number 1 is missing or not valid (up to 25 characters)", "059": "bank account type 1 is missing or not valid",
		"060": "bank account 2 is incomplete", "061": "bank name 2 is not valid", "062": "bank branch address 2 is not valid", "063": "bank account number 2 is not valid",
		"064": "bank account type 2 is not valid", "065": "bank account 3 is incomplete", "066": "bank name 3 is not valid", "067": "bank branch address 3 is not valid",
		"068": "bank account number 3 is not valid", "069": "bank account type 3 is not valid",
		"070": "demat account 1 is incomplete", "071": "depository 1 is missing or not valid", "072": "depository ID 1 is missing or longer than 8 characters",
		"073": "depository ID 1 must be blank (CDSL)", "074": "beneficiary account 1 must be 8 to 16 characters",
		"075": "demat account 2 is incomplete", "076": "depository 2 is not valid", "077": "depository ID 2 is longer than 8 characters", "078": "depository ID 2 must be blank (CDSL)",
		"079": "beneficiary account 2 must be 8 to 16 characters", "080": "demat account 3 is incomplete", "081": "depository 3 is not valid",
		"082": "depository ID 3 is longer than 8 characters", "083": "depository ID 3 must be blank (CDSL)", "084": "beneficiary account 3 must be 8 to 16 characters",
		"085": "the CIN or LEI is missing or not valid", "086": "the CIN or LEI is longer than 21 characters", "087": "the place of incorporation is not valid",
		"088": "the occupation is missing or not valid", "089": "the occupation details (for Others) are missing or not 4 to 75 characters",
		"090": "the occupation details must be blank", "091": "the date of commencement of business is not valid or after the file's date",
		"092": "the income range is missing or not valid", "093": "the income date is missing, not valid or after the file's date",
		"094": "the income date is more than 18 months before the file's date", "095": "the net worth is missing or not valid",
		"096": "the net worth date is missing, not valid or after the file's date", "097": "the net worth date is more than a year before the file's date",
		"098": "politically exposed person is missing or not valid", "099": "the CP code is missing or longer than 15 characters",
		"100": "the registration details must be blank", "101": "the registration details are incomplete", "102": "the registration number is missing or not valid",
		"103": "the registering authority is not valid", "104": "the place of registration is not valid", "105": "the date of registration is not valid or after the file's date",
		"106": "in-person verification is missing or not valid", "107": "the client agreement date is not valid or after the file's date",
		"108": "the relationship code is not valid", "109": "the proof details must be blank", "110": "the proof details are incomplete",
		"111": "the proof type is missing or not valid", "112": "the proof number is missing or not valid", "113": "the proof's place of issue is missing or not valid",
		"114": "the proof's place of issue must be 3 to 100 characters", "115": "the proof's place of issue must be blank",
		"116": "the proof's date of issue is missing or not valid", "117": "the proof's date of issue must be blank",
		"118": "the client status is not valid", "119": "the remarks are missing (needed when the status changes)",
		"300": "the entity is debarred", "301": "the member may not add or modify clients (banned from taking fresh clients or disabled)",
		"303": "the record's flag must be E", "304": "the nationality must be blank", "305": "the client code is not valid", "306": "the PAN does not match MCX's record of the client",
		"307": "the client code is in the file twice", "309": "client type corporate is missing or not valid", "310": "the income range or net worth is missing",
		"311": "an inactive client may not be modified, and a client MCX marked inactive may not be made active by the member",
		"313": "the file is larger than MCX takes", "314": "the contact persons must be blank",
		"315": "the relationship with the client for the mobile number is missing or not valid", "316": "the authorised or contact person's name for the mobile number is missing or not valid",
		"317": "the relationship with the client for the e-mail address is missing or not valid", "318": "the authorised or contact person's name for the e-mail address is missing or not valid",
		"319": "the mobile number is already another client's own (Self): record whose it is", "320": "the e-mail address is already another client's own (Self): record whose it is",
	}
	// 120 to 299: the contact persons, nine codes each.
	parts := []string{"details are incomplete", "name is missing or not valid", "designation is missing or not valid", "PAN is missing or not valid",
		"address is missing or not valid", "contact details are missing or not valid", "DIN is missing or not valid", "UID is not valid", "e-mail address is not valid"}
	for n := 1; n <= 20; n++ {
		for i, p := range parts {
			m[fmt.Sprintf("%03d", 120+9*(n-1)+i)] = fmt.Sprintf("contact person %d's %s", n, p)
		}
	}
	return m
}()

// MCXUCCError explains an MCX error code in words.
func MCXUCCError(code string) string {
	code = strings.TrimSpace(code)
	if n, err := strconv.Atoi(code); err == nil {
		code = fmt.Sprintf("%03d", n)
	}
	if t, ok := mcxUCCErrors[code]; ok {
		return "MCX error " + code + ": " + t
	}
	return "MCX error " + code + " (not in MCX's list of UCC error codes)"
}
