package ops

import "sort"

// ---- FATCA and CRS: the code lists ----
//
// Countries are ISO 3166-1 alpha-2 codes: the KRAs check the country of
// birth and each country of tax residency against an ISO 3166 country
// master (KFintech KRA circular KARVY/OPS/INTERM/GENRL/24-07 of 11 June
// 2024).
//
// The reasons for having no TIN (A, B, C) are those of the CBDT Guidance
// Note on FATCA and CRS (24 July 2026): the jurisdiction does not issue
// TINs; the person can get one but has not yet; the jurisdiction's law does
// not require it to be collected.
//
// The Form 166 fields new under the Income-tax Rules 2026 (SEBI circular
// HO/38/14/(18)2026-MIRSD-POD2/I/25560/2026 of 11 November 2026, paras
// 6.1-6.4) are kept with the codes of the OECD CRS XML schema version 3.0,
// which the Form 166 XML is built on: the controlling person's type
// (CRS801-CRS813), the account type (CRS1101-CRS1104) and new or
// pre-existing (CRS1201, CRS1202), as the Austrian Ministry of Finance's
// note on the version 3.0 schema (July 2026) lists them. The Form 166
// schema on the Income Tax reporting portal was not checked: to confirm.

// Countries are ISO 3166-1 alpha-2 codes and English short names.
var Countries = map[string]string{
	"AD": "Andorra", "AE": "United Arab Emirates", "AF": "Afghanistan", "AG": "Antigua and Barbuda", "AI": "Anguilla", "AL": "Albania", "AM": "Armenia", "AO": "Angola",
	"AQ": "Antarctica", "AR": "Argentina", "AS": "American Samoa", "AT": "Austria", "AU": "Australia", "AW": "Aruba", "AX": "Åland Islands", "AZ": "Azerbaijan",
	"BA": "Bosnia and Herzegovina", "BB": "Barbados", "BD": "Bangladesh", "BE": "Belgium", "BF": "Burkina Faso", "BG": "Bulgaria", "BH": "Bahrain", "BI": "Burundi",
	"BJ": "Benin", "BL": "Saint Barthélemy", "BM": "Bermuda", "BN": "Brunei Darussalam", "BO": "Bolivia", "BQ": "Bonaire, Sint Eustatius and Saba", "BR": "Brazil", "BS": "Bahamas",
	"BT": "Bhutan", "BV": "Bouvet Island", "BW": "Botswana", "BY": "Belarus", "BZ": "Belize", "CA": "Canada", "CC": "Cocos (Keeling) Islands", "CD": "Congo, Democratic Republic of the",
	"CF": "Central African Republic", "CG": "Congo", "CH": "Switzerland", "CI": "Côte d'Ivoire", "CK": "Cook Islands", "CL": "Chile", "CM": "Cameroon", "CN": "China",
	"CO": "Colombia", "CR": "Costa Rica", "CU": "Cuba", "CV": "Cabo Verde", "CW": "Curaçao", "CX": "Christmas Island", "CY": "Cyprus", "CZ": "Czechia",
	"DE": "Germany", "DJ": "Djibouti", "DK": "Denmark", "DM": "Dominica", "DO": "Dominican Republic", "DZ": "Algeria", "EC": "Ecuador", "EE": "Estonia",
	"EG": "Egypt", "EH": "Western Sahara", "ER": "Eritrea", "ES": "Spain", "ET": "Ethiopia", "FI": "Finland", "FJ": "Fiji", "FK": "Falkland Islands (Malvinas)",
	"FM": "Micronesia", "FO": "Faroe Islands", "FR": "France", "GA": "Gabon", "GB": "United Kingdom", "GD": "Grenada", "GE": "Georgia", "GF": "French Guiana",
	"GG": "Guernsey", "GH": "Ghana", "GI": "Gibraltar", "GL": "Greenland", "GM": "Gambia", "GN": "Guinea", "GP": "Guadeloupe", "GQ": "Equatorial Guinea",
	"GR": "Greece", "GS": "South Georgia and the South Sandwich Islands", "GT": "Guatemala", "GU": "Guam", "GW": "Guinea-Bissau", "GY": "Guyana", "HK": "Hong Kong", "HM": "Heard Island and McDonald Islands",
	"HN": "Honduras", "HR": "Croatia", "HT": "Haiti", "HU": "Hungary", "ID": "Indonesia", "IE": "Ireland", "IL": "Israel", "IM": "Isle of Man",
	"IN": "India", "IO": "British Indian Ocean Territory", "IQ": "Iraq", "IR": "Iran", "IS": "Iceland", "IT": "Italy", "JE": "Jersey", "JM": "Jamaica",
	"JO": "Jordan", "JP": "Japan", "KE": "Kenya", "KG": "Kyrgyzstan", "KH": "Cambodia", "KI": "Kiribati", "KM": "Comoros", "KN": "Saint Kitts and Nevis",
	"KP": "Korea, Democratic People's Republic of", "KR": "Korea, Republic of", "KW": "Kuwait", "KY": "Cayman Islands", "KZ": "Kazakhstan", "LA": "Lao People's Democratic Republic", "LB": "Lebanon", "LC": "Saint Lucia",
	"LI": "Liechtenstein", "LK": "Sri Lanka", "LR": "Liberia", "LS": "Lesotho", "LT": "Lithuania", "LU": "Luxembourg", "LV": "Latvia", "LY": "Libya",
	"MA": "Morocco", "MC": "Monaco", "MD": "Moldova", "ME": "Montenegro", "MF": "Saint Martin (French part)", "MG": "Madagascar", "MH": "Marshall Islands", "MK": "North Macedonia",
	"ML": "Mali", "MM": "Myanmar", "MN": "Mongolia", "MO": "Macao", "MP": "Northern Mariana Islands", "MQ": "Martinique", "MR": "Mauritania", "MS": "Montserrat",
	"MT": "Malta", "MU": "Mauritius", "MV": "Maldives", "MW": "Malawi", "MX": "Mexico", "MY": "Malaysia", "MZ": "Mozambique", "NA": "Namibia",
	"NC": "New Caledonia", "NE": "Niger", "NF": "Norfolk Island", "NG": "Nigeria", "NI": "Nicaragua", "NL": "Netherlands", "NO": "Norway", "NP": "Nepal",
	"NR": "Nauru", "NU": "Niue", "NZ": "New Zealand", "OM": "Oman", "PA": "Panama", "PE": "Peru", "PF": "French Polynesia", "PG": "Papua New Guinea",
	"PH": "Philippines", "PK": "Pakistan", "PL": "Poland", "PM": "Saint Pierre and Miquelon", "PN": "Pitcairn", "PR": "Puerto Rico", "PS": "Palestine, State of", "PT": "Portugal",
	"PW": "Palau", "PY": "Paraguay", "QA": "Qatar", "RE": "Réunion", "RO": "Romania", "RS": "Serbia", "RU": "Russian Federation", "RW": "Rwanda",
	"SA": "Saudi Arabia", "SB": "Solomon Islands", "SC": "Seychelles", "SD": "Sudan", "SE": "Sweden", "SG": "Singapore", "SH": "Saint Helena, Ascension and Tristan da Cunha", "SI": "Slovenia",
	"SJ": "Svalbard and Jan Mayen", "SK": "Slovakia", "SL": "Sierra Leone", "SM": "San Marino", "SN": "Senegal", "SO": "Somalia", "SR": "Suriname", "SS": "South Sudan",
	"ST": "Sao Tome and Principe", "SV": "El Salvador", "SX": "Sint Maarten (Dutch part)", "SY": "Syrian Arab Republic", "SZ": "Eswatini", "TC": "Turks and Caicos Islands", "TD": "Chad", "TF": "French Southern Territories",
	"TG": "Togo", "TH": "Thailand", "TJ": "Tajikistan", "TK": "Tokelau", "TL": "Timor-Leste", "TM": "Turkmenistan", "TN": "Tunisia", "TO": "Tonga",
	"TR": "Türkiye", "TT": "Trinidad and Tobago", "TV": "Tuvalu", "TW": "Taiwan", "TZ": "Tanzania", "UA": "Ukraine", "UG": "Uganda", "UM": "United States Minor Outlying Islands",
	"US": "United States", "UY": "Uruguay", "UZ": "Uzbekistan", "VA": "Holy See", "VC": "Saint Vincent and the Grenadines", "VE": "Venezuela", "VG": "Virgin Islands (British)", "VI": "Virgin Islands (U.S.)",
	"VN": "Viet Nam", "VU": "Vanuatu", "WF": "Wallis and Futuna", "WS": "Samoa", "YE": "Yemen", "YT": "Mayotte", "ZA": "South Africa", "ZM": "Zambia",
	"ZW": "Zimbabwe",
}

// countryByName finds a country by its code or its English name, for files
// from other systems that spell the name out.
func countryByName(s string) (string, bool) {
	if _, ok := Countries[s]; ok {
		return s, true
	}
	for k, v := range Countries {
		if equalFoldTrim(v, s) {
			return k, true
		}
	}
	switch upperTrim(s) {
	case "INDIAN", "BHARAT":
		return "IN", true
	case "USA", "U.S.A.", "UNITED STATES OF AMERICA", "AMERICA":
		return "US", true
	case "UK", "U.K.", "GREAT BRITAIN", "BRITAIN", "ENGLAND":
		return "GB", true
	case "UAE", "U.A.E.":
		return "AE", true
	}
	return "", false
}

// The reasons for having no TIN (CBDT Guidance Note on FATCA and CRS, 24
// July 2026).
const (
	NoTINNotIssued   = "A" // the country does not issue TINs to its residents
	NoTINNotObtained = "B" // the person can get a TIN but has not yet (explain why)
	NoTINNotRequired = "C" // the country's law does not require the TIN to be collected
)

// NoTINReasons are the reasons and what they mean.
var NoTINReasons = map[string]string{
	NoTINNotIssued:   "The country does not issue TINs to its residents",
	NoTINNotObtained: "The person can get a TIN but has not yet obtained one",
	NoTINNotRequired: "The country's law does not require the TIN to be collected",
}

// Controlling person types (OECD CRS XML schema v3.0, CtrlgPersonType;
// the roles of SEBI's circular of 11 November 2026, para 6.2).
var ControllingPersonTypes = []struct{ Code, Name string }{
	{"CRS801", "Legal person: controlling ownership interest"},
	{"CRS802", "Legal person: control by other means"},
	{"CRS803", "Legal person: senior managing official"},
	{"CRS804", "Trust: settlor"},
	{"CRS805", "Trust: trustee"},
	{"CRS806", "Trust: protector"},
	{"CRS807", "Trust: beneficiary"},
	{"CRS808", "Trust: other"},
	{"CRS809", "Other legal arrangement: settlor-equivalent"},
	{"CRS810", "Other legal arrangement: trustee-equivalent"},
	{"CRS811", "Other legal arrangement: protector-equivalent"},
	{"CRS812", "Other legal arrangement: beneficiary-equivalent"},
	{"CRS813", "Other legal arrangement: other-equivalent"},
}

// Account types (OECD CRS XML schema v3.0, AccountType). A broker's
// trading and demat account is a custodial account (CBDT Guidance Note,
// para 2.3.1: brokers and DPs are generally custodial institutions).
var FATCAAccountTypes = []struct{ Code, Name string }{
	{"CRS1102", "Custodial account"},
	{"CRS1101", "Depository account"},
	{"CRS1104", "Debt or equity interest in an investment entity"},
	{"CRS1103", "Cash value insurance or annuity contract"},
}

// New or pre-existing account (OECD CRS XML schema v3.0, DDProcedure).
const (
	AccountNew         = "CRS1201"
	AccountPreexisting = "CRS1202"
)

// FATCAAccountStatuses names the two.
var FATCAAccountStatuses = map[string]string{AccountNew: "New account", AccountPreexisting: "Pre-existing account"}

func cpTypeName(code string) string {
	for _, t := range ControllingPersonTypes {
		if t.Code == code {
			return t.Name
		}
	}
	return ""
}

func accountTypeName(code string) string {
	for _, t := range FATCAAccountTypes {
		if t.Code == code {
			return t.Name
		}
	}
	return ""
}

// FATCACodes are the code lists for the screens.
type FATCACodes struct {
	Countries          [][2]string       `json:"countries"` // code, name; India first
	NoTINReasons       map[string]string `json:"no_tin_reasons"`
	ControllingPersons [][2]string       `json:"controlling_person_types"`
	AccountTypes       [][2]string       `json:"account_types"`
	AccountStatuses    map[string]string `json:"account_statuses"`
	Modes              []string          `json:"modes"`
	MaxCountries       int               `json:"max_countries"`
	KRAFrom            string            `json:"kra_from"`
}

// FATCACodeLists returns the code lists.
func FATCACodeLists() FATCACodes {
	cs := [][2]string{{"IN", Countries["IN"]}}
	var rest [][2]string
	for k, v := range Countries {
		if k != "IN" {
			rest = append(rest, [2]string{k, v})
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i][1] < rest[j][1] })
	out := FATCACodes{Countries: append(cs, rest...), NoTINReasons: NoTINReasons, AccountStatuses: FATCAAccountStatuses, Modes: FATCAModes, MaxCountries: MaxTaxCountries, KRAFrom: FATCAKRAFrom}
	for _, t := range ControllingPersonTypes {
		out.ControllingPersons = append(out.ControllingPersons, [2]string{t.Code, t.Name})
	}
	for _, t := range FATCAAccountTypes {
		out.AccountTypes = append(out.AccountTypes, [2]string{t.Code, t.Name})
	}
	return out
}
