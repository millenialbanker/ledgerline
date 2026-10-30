package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"ledgerline/internal/docs"
)

// ---- NSE UCI (UCC) API ----
//
// NSE's UCI API takes the firm's client (UCC) records over HTTPS instead of
// the batch file or the UCI Online portal. Ledgerline maps each client to
// NSE's record, checks it against NSE's field rules first and says in words
// what is wrong, sends the records that are new or changed, reads NSE's
// answer for each client and segment, and asks NSE for each client's PAN
// verification status.
//
// From NSE's "TDD-UCI External interface" v1.9 (11 December 2023, UAT):
//   - One-time API user registration on NSE's api-reg page (member code,
//     password, e-mail, OTP); NSE (uci@nse.co.in) then activates the user.
//   - POST {base}/auth {username, password, apiName: "uci_clientupload"}
//     gives {token}: a JWT valid for 30 minutes, sent as
//     "Authorization: Bearer <token>" on the other calls.
//   - POST {base}/clientupload {"request": [records]}: at most 50 per call.
//     One record per client per segment (ccdSegInd C, F, S, X, D, O); dates
//     DD-MM-YYYY. The answer is {"response": [{clientCode, segment, status,
//     msgCode, msgDesc}]}; 200 is saved. There is no add or modify flag: a
//     record for an existing client code replaces it.
//   - POST {base}/getpanstatus {"request": {memberCode, segment, fromDate,
//     toDate}} (at most 10 days) and POST {base}/getuccpanstatus
//     {"request": {memberCode, ucc: [codes]}} (at most 50) give each UCC's
//     PAN verification (P pending, A approved, I incorrect PAN), Aadhaar
//     seeding and whether it is permitted to trade.
//   - UAT base https://uat.connect2nsccl.com/uciservice/api. The production
//     address is not in the document; www.connect2nsccl.com is assumed and
//     can be changed on the connection.
//   - The document gives no service hours; Ledgerline sends from 08:00 to
//     22:00.

const (
	connNSEUCI   = "nse-uci"
	cUCCReg      = "uccreg"      // key: EXCHANGE|UCC
	cUCCRun      = "uccrun"      // key: EXCHANGE-nnnnn
	cNSEUCIState = "nseucistate" // one document: the login and the last runs

	nseUCILive = "https://www.connect2nsccl.com/uciservice/api"
	nseUCIUAT  = "https://uat.connect2nsccl.com/uciservice/api"
	// nseUCIBatch is NSE's limit of records per call ("50 clients per
	// request"; counted here as records, the safer reading).
	nseUCIBatch = 50
	// nseUCIPerRun caps the records one run sends.
	nseUCIPerRun = 2000
)

// Registration statuses, as shown on the Exchange UCC screen.
const (
	nseUCINotSent  = "Not sent"
	nseUCISent     = "Sent"
	nseUCIAccepted = "Accepted"
	nseUCIRejected = "Rejected"
	nseUCINeedsFix = "Needs fixing"
)

var nseUCIStatuses = []string{nseUCINotSent, nseUCISent, nseUCIAccepted, nseUCIRejected, nseUCINeedsFix}

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connNSEUCI, Name: "NSE UCC API (UCI)", Group: "Exchanges", Test: "https",
		Purpose: "Sends new and changed clients to NSE's UCC database (UCI) and reads back each client's PAN verification status. Register the API user once on NSE's API registration page with the member code, then ask NSE (uci@nse.co.in) to activate it",
		Fields: []ConnField{{Key: "member", Label: "Member code (5 digits)"},
			{Key: "login", Label: "API user name (leave empty when it is the member code)", Optional: true},
			{Key: "password", Label: "API password (set at registration)", Secret: true},
			{Key: "env", Label: "Environment", Options: []string{"Production", "UAT"}},
			{Key: "send", Label: "Send new and changed clients automatically", Options: []string{"Off", "On"}},
			{Key: "url", Label: "Address (leave empty for NSE's)", Optional: true, Placeholder: nseUCILive}, fNotes}})
}

type nseUCIConn struct {
	base, member, login, password string
	live, auto                    bool
}

func parseNSEUCIConn(c Connection) (nseUCIConn, error) {
	x := nseUCIConn{member: strings.TrimSpace(c.Values["member"]), password: c.Secrets["password"], auto: c.Values["send"] == "On"}
	x.login = firstOf(strings.TrimSpace(c.Values["login"]), x.member)
	if len(x.member) != 5 || x.password == "" {
		return x, invalid("the NSE UCC API connection needs the 5-character member code and the API password")
	}
	x.base, x.live = nseUCILive, c.Values["env"] != "UAT"
	if !x.live {
		x.base = nseUCIUAT
	}
	if u := strings.TrimRight(strings.TrimSpace(c.Values["url"]), "/"); u != "" {
		x.base = u
	}
	return x, nil
}

// nseUCIHTTP is replaced in tests.
var nseUCIHTTP = &http.Client{Timeout: 2 * time.Minute}

// One login per server: calls of a firm share its token.
var nseUCIMu sync.Mutex

// NSEUCIState is the login and the last runs.
type NSEUCIState struct {
	Token     string `json:"token,omitempty"`
	TokenExp  string `json:"token_exp,omitempty"`
	LastSend  string `json:"last_send,omitempty"`
	LastPAN   string `json:"last_pan,omitempty"` // day of the last daily PAN status refresh
	LastError string `json:"last_error,omitempty"`
	LastOK    string `json:"last_ok,omitempty"`
}

// NSEUCISegResult is NSE's answer for one segment of a client.
type NSEUCISegResult struct {
	Status string `json:"status"` // Sent, Accepted, Rejected
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
	At     string `json:"at,omitempty"`
}

// NSEUCIEvent is one entry of a client's registration history.
type NSEUCIEvent struct {
	At     string `json:"at"`
	What   string `json:"what"`
	Ref    string `json:"ref,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// NSEUCIReg is a client's registration at NSE as Ledgerline last knows it.
type NSEUCIReg struct {
	Exchange string `json:"exchange"`
	UCC      string `json:"ucc"`
	Action   string `json:"action"` // of the last send: Add or Modify
	Status   string `json:"status"` // Sent, Accepted, Rejected; Not sent after a failed try
	Reason   string `json:"reason,omitempty"`
	Ref      string `json:"ref,omitempty"`
	SentAt   string `json:"sent_at,omitempty"`
	ResultAt string `json:"result_at,omitempty"`
	// Accepted is set once NSE has saved any record of the client: later
	// sends are modifications.
	Accepted bool `json:"accepted,omitempty"`
	// SegHash is the hash of each segment's record as last sent and
	// answered; an unchanged record is not sent again.
	SegHash map[string]string          `json:"seg_hash,omitempty"`
	Segs    map[string]NSEUCISegResult `json:"segments,omitempty"`
	UPI     string                     `json:"upi,omitempty"` // UPI flag last sent (D only follows Y)
	// PAN verification at NSE.
	PANStatus      string            `json:"pan_status,omitempty"` // P, A, I
	PANStatusAt    string            `json:"pan_status_at,omitempty"`
	PANCheckedAt   string            `json:"pan_checked_at,omitempty"`
	AadhaarSeeding string            `json:"aadhaar_seeding,omitempty"`
	TradeStatus    map[string]string `json:"trade_status,omitempty"` // segment: Permitted to trade / Not permitted to trade
	History        []NSEUCIEvent     `json:"history,omitempty"`
}

func (r *NSEUCIReg) event(at, what, ref, detail string) {
	r.History = append(r.History, NSEUCIEvent{At: at, What: what, Ref: ref, Detail: detail})
	if n := len(r.History); n > 40 {
		r.History = r.History[n-40:]
	}
}

// NSEUCIBatchAudit is one call as sent and answered: the request with
// Aadhaar masked, never the password.
type NSEUCIBatchAudit struct {
	Call     string `json:"call"`
	Records  int    `json:"records"`
	Request  string `json:"request"`
	HTTP     int    `json:"http"`
	Response string `json:"response"`
	Error    string `json:"error,omitempty"`
}

// NSEUCIRun is one send or PAN status run.
type NSEUCIRun struct {
	ID       string             `json:"id"`
	Exchange string             `json:"exchange"`
	Kind     string             `json:"kind"` // send, pan-status
	At       string             `json:"at"`
	By       string             `json:"by"`
	Sent     int                `json:"sent"`
	Accepted int                `json:"accepted"`
	Rejected int                `json:"rejected"`
	Note     string             `json:"note"`
	UCCs     []string           `json:"uccs,omitempty"`
	Batches  []NSEUCIBatchAudit `json:"batches,omitempty"`
}

// ---- the record ----

// nseUCIKeys are every field of NSE's client record (the keys of the
// TDD's request sample; table field 149 spells it CcdPoaFunds, the sample
// ccdPoaFunds, which is used; fields 86, 90 and 94 show ccdBankAcctNo2 as
// the key for banks 3 to 5, the sample ccdBankAcctNo3/4/5, which are used).
var nseUCIKeys = strings.Fields(`ccdMemCd ccdCd ccdName ccdCategory ccdPinCode ccdTelNo ccdAgrDt ccdPanNo ccdBankName ccdBankAcctNo
ccdDeposName ccdBenAcctNo ccdRegNo ccdRegAuth ccdPlcReg ccdDtReg ccdSegInd ccdDob ccdDeposId ccdAddLine1 ccdAddLine2 ccdAddLine3
ccdAddCity ccdAddState ccdAddCountry ccdTelIsd ccdTelStd ccdMobile ccdEmail ccdProofType ccdProofNo ccdIssPlcProof ccdIssDtProof
ccdDir1Name ccdDir2Name ccdDir3Name ccdDir1Desig ccdDir2Desig ccdDir3Desig ccdDir1Pan ccdDir2Pan ccdDir3Pan ccdDir1Add ccdDir2Add
ccdDir3Add ccdDir1TelNo ccdDir2TelNo ccdDir3TelNo ccdDir1Email ccdDir2Email ccdDir3Email ccdIpv ccdUccCd ccdRelationship ccdTvFlag
ccdFacilityType ccdCin ccdCltStatus ccdCltStatusReason ccdGender ccdGuardianName ccdMaritlStatus ccdNationality ccdPermantAddFlg
ccdPermAddLine1 ccdPermAddLine2 ccdPermAddLine3 ccdPermAddCity ccdPermAddState ccdPermAddCountry ccdPermPin ccdOffcIsd ccdOffcStd
ccdTelOffice ccdUid ccdBankName2 ccdBankAcctNo2 ccdBankName3 ccdBankAcctNo3 ccdDeposName2 ccdDeposId2 ccdBenAcctNo2 ccdDeposName3
ccdDeposId3 ccdBenAcctNo3 ccdGrosAnnlRnge ccdGrosAnnlAsdate ccdNetWorth ccdNetWorthAsdate ccdPep ccdPoi ccdOccupation
ccdOccupationDtls ccdBusComDt ccdCpCd ccdClntTyp ccdDinCntrctPerson1 ccdUidCntrctPerson1 ccdDinCntrctPerson2 ccdUidCntrctPerson2
ccdDinCntrctPerson3 ccdUidCntrctPerson3 ccdDir4Name ccdDir4Desig ccdDir4Pan ccdDir4Add ccdDir4TelNo ccdDinCntrctPerson4
ccdUidCntrctPerson4 ccdDir4Email ccdDir5Name ccdDir5Desig ccdDir5Pan ccdDir5Add ccdDir5TelNo ccdDinCntrctPerson5 ccdUidCntrctPerson5
ccdDir5Email ccdNationalityOth ccdPermAddStateOth ccdAddStateOth ccdPoaFunds ccdPoaSecurities ccdCpName ccdCpPan ccdCpEmail
ccdCpStd ccdCpIsd ccdCpTel ccdCpMob ccdCpAdd ccdCpAddCity ccdCpAddState ccdCpAddStateOth ccdCpAddCountry ccdCpAddPin ccdNomFlag
ccdBankIfsc ccdBankIfsc2 ccdBankIfsc3 ccdBankIfsc4 ccdBankIfsc5 ccdPriSecBnk ccdPriSecBnk2 ccdPriSecBnk3 ccdPriSecBnk4 ccdPriSecBnk5
ccdPriSecDp ccdPriSecDp2 ccdPriSecDp3 ccdPriSecDp4 ccdPriSecDp5 ccdAcctType ccdOptForUpi ccdBankName4 ccdBankAcctNo4 ccdBankName5
ccdBankAcctNo5 ccdDeposName4 ccdDeposId4 ccdBenAcctNo4 ccdDeposName5 ccdDeposId5 ccdBenAcctNo5`)

var reNSEUCICode = regexp.MustCompile(`^[A-Z0-9]{1,10}$`)

// nseUCISeg is NSE's segment indicator for Ledgerline's segments.
var nseUCISeg = map[string]string{"CM": "C", "FO": "F", "CD": "X", "COM": "O"}
var nseUCISegName = map[string]string{"C": "cash", "F": "F&O", "X": "currency", "O": "commodity", "S": "SLB", "D": "debt"}

// nseUCIState: Annexure 2 of the TDD.
var nseUCIStateCode = map[string]string{"andamanandnicobarislands": "1", "andamannicobar": "1", "andhrapradesh": "2", "arunachalpradesh": "3", "assam": "4", "bihar": "5",
	"chandigarh": "6", "dadraandnagarhaveli": "7", "damananddiu": "8", "delhi": "9", "newdelhi": "9", "nctofdelhi": "9", "goa": "10", "gujarat": "11", "haryana": "12",
	"himachalpradesh": "13", "jammuandkashmir": "14", "karnataka": "15", "kerala": "16", "lakshadweep": "17", "lakhswadeep": "17", "madhyapradesh": "18", "maharashtra": "19",
	"manipur": "20", "meghalaya": "21", "mizoram": "22", "nagaland": "23", "orissa": "24", "odisha": "24", "pondicherry": "25", "puducherry": "25", "punjab": "26",
	"rajasthan": "27", "sikkim": "28", "tamilnadu": "29", "tripura": "30", "uttarpradesh": "31", "westbengal": "32", "chhattisgarh": "33", "uttaranchal": "34",
	"uttarakhand": "34", "jharkhand": "35", "telangana": "37", "ladakh": "38"}

func nseUCIStateOf(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil {
		for _, v := range nseUCIStateCode {
			if v == strconv.Itoa(n) {
				return v, true
			}
		}
		return "", false
	}
	k := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, strings.ReplaceAll(s, "&", "and"))
	v, ok := nseUCIStateCode[k]
	return v, ok
}

// Category groups (TDD field rules).
var (
	nseUCIIndividual = map[string]bool{"1": true, "11": true, "18": true, "25": true, "26": true, "27": true, "31": true, "36": true}
	// Ledgerline sends these categories; the others need fields (FPI
	// registration, custodians, SLB) it does not keep.
	nseUCISupported = map[string]string{"1": "individual", "2": "partnership firm or LLP", "3": "HUF", "4": "company", "5": "trust or society", "11": "NRI"}
)

func nseUCICategory(c Client, d UCCDetails) string {
	if d.Category != "" {
		return d.Category
	}
	return map[string]string{"Individual": "1", "NRI": "11", "HUF": "3", "Corporate": "4"}[c.Type]
}

func nseUCIDate(d string) string {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(d))
	if err != nil {
		return ""
	}
	return t.Format("02-01-2006")
}

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func digitsOf(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

func sameRune(s string) bool { return s != "" && strings.Trim(s, s[:1]) == "" }

func runes(s string) int { return utf8.RuneCountInString(s) }

// nseUCIEmailProblem checks an e-mail address against the TDD's rules.
func nseUCIEmailProblem(e string) string {
	l := strings.ToLower(e)
	at := strings.Index(e, "@")
	switch {
	case runes(e) < 5 || runes(e) > 60:
		return "must be 5 to 60 characters"
	case strings.Count(e, "@") != 1:
		return "must have exactly one @"
	case !strings.Contains(e, "."):
		return "must have a dot"
	case !(unicode.IsLetter(rune(e[0])) || unicode.IsDigit(rune(e[0]))):
		return "must begin with a letter or digit"
	case strings.ContainsAny(e, "~`#$%^&*()=+{}[]\\|:;\"'<>,?/ ”’"):
		return "has a character NSE does not allow"
	case strings.Contains(l, "noemail") || strings.Contains(l, "notprovided") || strings.Contains(l, "@xyz"):
		return "is a placeholder, not the client's address"
	case strings.HasSuffix(e, "."):
		return "must not end with a dot"
	case at+1 < len(e) && e[at+1] == '.':
		return "must not have a dot right after the @"
	}
	return ""
}

// nseUCIMobileProblem: Indian mobile numbers are 10 digits without the
// country code, not all one digit, not 1234567890.
func nseUCIMobileProblem(m string) string {
	switch {
	case !allDigits(m) || len(m) != 10:
		return "must be the 10-digit Indian mobile number, without +91"
	case sameRune(m) || m == "1234567890":
		return "is not a real number"
	}
	return ""
}

// nseUCIIncome maps Ledgerline's income ranges to NSE's gross annual
// income codes. Code 5 is "more than ₹25 lakh" for individuals and "₹25
// lakh to ₹1 crore" for others; 6 "more than ₹1 crore" is for others.
func nseUCIIncome(income string, individual bool) string {
	switch strings.TrimSpace(income) {
	case "<1 L":
		return "1"
	case "1-5 L":
		return "2"
	case "5-10 L":
		return "3"
	case "10-25 L":
		return "4"
	case "25 L-1 Cr":
		return "5"
	case ">1 Cr":
		if individual {
			return "5"
		}
		return "6"
	}
	if n, err := strconv.Atoi(strings.TrimSpace(income)); err == nil && n >= 1 && n <= 6 {
		return strconv.Itoa(n)
	}
	return ""
}

// nseUCIOccupation maps an occupation to NSE's code. "Salaried" is not
// mapped: NSE asks whether it is public sector, private sector or
// government service, which only the client can say.
func nseUCIOccupation(s string) (code, detail string) {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil && ((n >= 1 && n <= 9) || n == 99) {
		return strconv.Itoa(n), ""
	}
	switch strings.ToLower(s) {
	case "public sector":
		return "1", ""
	case "private sector":
		return "2", ""
	case "government service", "government":
		return "3", ""
	case "business":
		return "4", ""
	case "professional":
		return "5", ""
	case "agriculturist", "agriculture":
		return "6", ""
	case "retired":
		return "7", ""
	case "housewife", "homemaker":
		return "8", ""
	case "student":
		return "9", ""
	case "self-employed", "self employed":
		return "99", "SELF EMPLOYED"
	}
	return "", ""
}

func nseUCIGender(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "m", "male":
		return "M"
	case "f", "female":
		return "F"
	case "u", "transgender", "other": // NSE has no transgender code; U is the closest of its values
		return "U"
	}
	return ""
}

func nseUCIMarital(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "s", "single", "unmarried":
		return "S"
	case "m", "married":
		return "M"
	case "w", "widow", "widower", "widowed":
		return "W"
	case "d", "divorced", "divorce":
		return "D"
	case "na", "other":
		return "NA"
	}
	return ""
}

// splitAddress breaks a one-line address into NSE's three lines of up to
// 255 characters, at commas.
func splitAddress(a string) [3]string {
	var out [3]string
	a = strings.Join(strings.Fields(a), " ")
	if runes(a) <= 255 {
		out[0] = a
		return out
	}
	i := 0
	for _, part := range strings.Split(a, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if out[i] != "" && runes(out[i])+2+runes(part) > 255 && i < 2 {
			i++
		}
		if out[i] != "" {
			out[i] += ", "
		}
		out[i] += part
	}
	return out
}

// nseUCIRecord is one segment's record of a client.
type nseUCIRecord struct {
	Seg  string            // C, F, X, O
	Body map[string]string // every TDD field
	Hash string
}

// nseUCIMapped is a client mapped to NSE's records, with what is wrong.
type nseUCIMapped struct {
	Records  []nseUCIRecord
	Problems []string
}

// nseUCIMap maps a client to NSE's records (one per segment) and checks
// each field against the TDD's rules. Problems are in words, for staff.
func nseUCIMap(c Client, d UCCDetails, a *Application, set Settings, member string, now time.Time, reg NSEUCIReg) nseUCIMapped {
	var m nseUCIMapped
	bad := func(f string, x ...any) { m.Problems = append(m.Problems, fmt.Sprintf(f, x...)) }
	app := Application{}
	if a != nil {
		app = *a
	}
	b := map[string]string{}
	for _, k := range nseUCIKeys {
		b[k] = ""
	}
	cat := nseUCICategory(c, d)
	ind := nseUCIIndividual[cat]
	b["ccdMemCd"], b["ccdCd"], b["ccdName"], b["ccdCategory"] = member, c.UCC, c.Name, cat
	if _, ok := nseUCISupported[cat]; !ok {
		bad("Client category: %q is not one Ledgerline sends by API (individual, NRI, HUF, partnership, company, trust); register this client on NSE's UCI Online", firstOf(cat, c.Type))
	}
	if !reNSEUCICode.MatchString(c.UCC) {
		bad("Client code: NSE takes 1 to 10 capital letters or digits")
	}
	if n := runes(c.Name); n < 2 || n > 150 || !hasLetter(c.Name) {
		bad("Name: must be the full name, 2 to 150 characters")
	}
	// Account type: the member's own PAN is a proprietary (OWN) account.
	b["ccdAcctType"] = "CLIENT"
	if set.FirmPAN != "" && c.PAN == set.FirmPAN {
		b["ccdAcctType"] = "OWN"
	}
	b["ccdPanNo"] = c.PAN
	if !rePAN.MatchString(c.PAN) {
		bad("PAN: must be 5 letters, 4 digits and a letter")
	}
	// Bank (field 10 to 13): mandatory for individual and non-individual
	// categories. Commas are not allowed in a bank name and are dropped.
	bank := strings.Join(strings.Fields(strings.ReplaceAll(c.Bank.Bank, ",", " ")), " ")
	b["ccdBankName"], b["ccdBankIfsc"], b["ccdBankAcctNo"], b["ccdPriSecBnk"] = bank, c.Bank.IFSC, c.Bank.Account, "P"
	if n := runes(bank); n < 3 || n > 60 || !hasLetter(bank) {
		bad("Bank name: 3 to 60 characters, not only numbers")
	}
	if !reIFSC.MatchString(c.Bank.IFSC) {
		bad("Bank IFSC: 4 letters, a zero, then 6 letters or digits")
	}
	if acct := c.Bank.Account; len(acct) < 1 || len(acct) > 30 || digitsOf(acct) == "" || strings.Trim(digitsOf(acct), "0") == "" || strings.Trim(acct, "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz") != "" {
		bad("Bank account number: up to 30 letters and digits, with digits that are not all zeros")
	}
	// Demat (fields 14 to 17). For NSDL the 16-character ID is the DP ID
	// (IN + 6) and the 8-digit client ID; for CDSL the 16-digit BO ID, with
	// the depository ID left blank as the TDD asks of the 2nd to 5th demat
	// accounts (it does not say so for the first: the safer reading).
	dp, id := strings.ToUpper(strings.TrimSpace(c.Demat.Depository)), strings.ToUpper(strings.TrimSpace(c.Demat.ID))
	hasCash := false
	for _, s := range c.Segments {
		hasCash = hasCash || s == "CM"
	}
	switch {
	case id == "":
		if hasCash {
			bad("Demat account: NSE needs it for the cash segment")
		}
	case dp == "NSDL" && len(id) == 16 && strings.HasPrefix(id, "IN") && allDigits(id[8:]):
		b["ccdDeposName"], b["ccdDeposId"], b["ccdBenAcctNo"], b["ccdPriSecDp"] = "NSDL", id[:8], id[8:], "P"
	case dp == "CDSL" && len(id) == 16 && allDigits(id) && strings.Trim(id, "0") != "":
		b["ccdDeposName"], b["ccdBenAcctNo"], b["ccdPriSecDp"] = "CDSL", id, "P"
	default:
		bad("Demat account: %s ID must be %s", firstOf(dp, "the depository's"), map[bool]string{true: "IN, 6 characters and 8 digits", false: "16 digits"}[dp == "NSDL"])
	}
	// Date of birth or incorporation.
	b["ccdDob"] = nseUCIDate(c.DOB)
	if t, err := time.Parse("2006-01-02", c.DOB); err != nil || t.After(now) {
		if ind {
			bad("Date of birth: missing or not a date")
		} else {
			bad("Date of incorporation: missing or not a date (enter it as the client's date of birth)")
		}
	}
	// Correspondence address.
	lines := [3]string{d.Address1, d.Address2, d.Address3}
	if lines[0] == "" {
		lines = splitAddress(app.Address)
	}
	city := firstOf(d.City, firstOf(c.City, app.City))
	state := firstOf(d.State, firstOf(app.State, c.State))
	pin := firstOf(d.PIN, app.PIN)
	country := firstOf(d.Country, "85")
	b["ccdAddLine1"], b["ccdAddLine2"], b["ccdAddLine3"], b["ccdAddCity"], b["ccdAddCountry"], b["ccdPinCode"] = lines[0], lines[1], lines[2], city, country, pin
	if n := runes(lines[0]); n < 5 || n > 255 || !hasLetter(lines[0]) {
		bad("Address line 1: missing or not an address (5 to 255 characters); add it under Exchange UCC details")
	}
	for i, l := range lines[1:] {
		if l != "" && (runes(l) < 3 || runes(l) > 255 || !hasLetter(l)) {
			bad("Address line %d: 3 to 255 characters, not only numbers", i+2)
		}
	}
	if n := runes(city); n < 3 || n > 50 || !hasLetter(city) {
		bad("City: 3 to 50 characters")
	}
	if code, ok := nseUCIStateOf(state); ok {
		b["ccdAddState"] = code
	} else {
		bad("State: %q is not one of NSE's states; give the state's name", state)
	}
	countryOK := func(cc string) bool { n, err := strconv.Atoi(cc); return err == nil && n >= 1 && n <= 258 }
	if !countryOK(country) {
		bad("Country: give NSE's country code (India is 85)")
	}
	if country == "85" {
		if len(pin) != 6 || !allDigits(pin) || strings.Trim(pin, "0") == "" {
			bad("PIN code: 6 digits")
		}
	} else if n := len(pin); n > 10 || (pin != "" && !hasLetter(pin) && !allDigits(pin)) {
		bad("PIN code: up to 10 letters and digits")
	}
	// Permanent address: the same unless given.
	b["ccdPermantAddFlg"] = "Y"
	if d.PermAddress1 != "" {
		b["ccdPermantAddFlg"] = "N"
		pc := firstOf(d.PermCountry, "85")
		b["ccdPermAddLine1"], b["ccdPermAddLine2"], b["ccdPermAddLine3"], b["ccdPermAddCity"], b["ccdPermAddCountry"], b["ccdPermPin"] = d.PermAddress1, d.PermAddress2, d.PermAddress3, d.PermCity, pc, d.PermPIN
		if n := runes(d.PermAddress1); n < 5 || n > 255 || !hasLetter(d.PermAddress1) {
			bad("Permanent address line 1: 5 to 255 characters")
		}
		if n := runes(d.PermCity); n < 3 || n > 50 || !hasLetter(d.PermCity) {
			bad("Permanent address city: 3 to 50 characters")
		}
		if code, ok := nseUCIStateOf(d.PermState); ok {
			b["ccdPermAddState"] = code
		} else {
			bad("Permanent address state: %q is not one of NSE's states", d.PermState)
		}
		if !countryOK(pc) {
			bad("Permanent address country: give NSE's country code (India is 85)")
		}
		if pc == "85" && (len(d.PermPIN) != 6 || !allDigits(d.PermPIN) || strings.Trim(d.PermPIN, "0") == "") {
			bad("Permanent address PIN code: 6 digits")
		}
	}
	// Contact.
	b["ccdMobile"], b["ccdEmail"] = c.Mobile, c.Email
	if p := nseUCIMobileProblem(c.Mobile); p != "" && country == "85" {
		bad("Mobile: %s", p)
	} else if country != "85" && (len(c.Mobile) < 5 || len(c.Mobile) > 10 || !allDigits(c.Mobile)) {
		bad("Mobile: 5 to 10 digits")
	}
	if p := nseUCIEmailProblem(c.Email); p != "" {
		bad("E-mail: %s", p)
	}
	b["ccdAgrDt"] = nseUCIDate(d.AgreementDate)
	// In-person verification: the client passed the KYC verification stage
	// before reaching UCC upload (IPV done, or not needed for Aadhaar-based
	// KYC), so Y unless the details say otherwise.
	b["ccdIpv"], b["ccdClntTyp"] = firstOf(d.IPV, "Y"), "N"
	if b["ccdIpv"] != "Y" && b["ccdIpv"] != "N" {
		bad("In-person verification: Y or N")
	}
	// Individuals.
	if ind {
		b["ccdGender"] = nseUCIGender(firstOf(d.Gender, app.Gender))
		if b["ccdGender"] == "" {
			bad("Gender: missing; add it under Exchange UCC details")
		}
		g := firstOf(d.Guardian, app.Father)
		b["ccdGuardianName"] = g
		switch {
		case g == "":
			bad("Father's or spouse's name: missing; add it under Exchange UCC details")
		case runes(g) > 150 || !hasLetter(g):
			bad("Father's or spouse's name: up to 150 characters, not only numbers")
		case strings.Contains(g, ","):
			bad("Father's or spouse's name: NSE does not allow commas")
		}
		b["ccdMaritlStatus"] = nseUCIMarital(firstOf(d.Marital, app.Marital))
		if b["ccdMaritlStatus"] == "" {
			bad("Marital status: missing; add it under Exchange UCC details")
		}
		// Other nationalities (code 2 with the country named) are not sent:
		// NRIs and foreign nationals need fields Ledgerline does not keep.
		b["ccdNationality"] = firstOf(d.Nationality, "1")
		if b["ccdNationality"] != "1" {
			bad("Nationality: Ledgerline sends Indian nationals only by API (code 1)")
		}
		occ, det := nseUCIOccupation(firstOf(d.Occupation, app.Occupation))
		if occ == "99" && d.OccupationDetail != "" {
			det = d.OccupationDetail
		}
		b["ccdOccupation"], b["ccdOccupationDtls"] = occ, det
		switch {
		case occ == "":
			bad("Occupation: %s", map[bool]string{true: "say whether the salaried job is public sector, private sector or government service", false: "missing; add it under Exchange UCC details"}[strings.EqualFold(firstOf(d.Occupation, app.Occupation), "Salaried")])
		case occ == "99" && (runes(det) < 4 || runes(det) > 75):
			bad("Occupation details: 4 to 75 characters for 'Others'")
		}
		// Nomination: 1 opted, 2 opted out.
		b["ccdNomFlag"] = "2"
		if len(c.Nominees) > 0 || c.Nominee == "Opted in" {
			b["ccdNomFlag"] = "1"
		}
	} else if cat != "" {
		if cat == "4" {
			b["ccdClntTyp"] = "Y"
			b["ccdCin"] = d.CIN
			if !(d.CIN == "CIN_EXEMPT" || (len(d.CIN) == 21 && strings.Trim(d.CIN, "0") != "" && strings.Trim(d.CIN, "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ") == "")) {
				bad("CIN: the company's 21-character CIN (or CIN_EXEMPT)")
			}
		}
		nseUCIContacts(b, cat, d.Contacts, bad)
	}
	// Income (retail categories).
	b["ccdGrosAnnlRnge"] = nseUCIIncome(c.Income, ind)
	if b["ccdGrosAnnlRnge"] == "" {
		bad("Gross annual income: missing")
	}
	asOn := d.IncomeAsOn
	for _, x := range []string{c.KYCDate, c.KYCUpdatedOn} {
		if validDate(x) && x > asOn && d.IncomeAsOn == "" {
			asOn = x
		}
	}
	b["ccdGrosAnnlAsdate"] = nseUCIDate(asOn) // DD-MM-YYYY as the sample (the table says DD-MON-YYYY)
	if !validDate(asOn) {
		bad("Income declared on: missing")
	} else if asOn < fmt.Sprintf("%d-01-01", now.Year()-1) {
		bad("Income declared on %s: NSE wants it no older than the previous calendar year; update the client's income", fmtDay(asOn))
	}
	// Politically exposed person.
	b["ccdPep"] = "0"
	for _, s := range c.CSC {
		switch s {
		case "Politically exposed person":
			b["ccdPep"] = "1"
		case "Relative of a PEP":
			if b["ccdPep"] == "0" {
				b["ccdPep"] = "2"
			}
		}
	}
	if d.PEP != "" {
		b["ccdPep"] = d.PEP
	}
	if !contains([]string{"0", "1", "2"}, b["ccdPep"]) {
		bad("Politically exposed: 0, 1 or 2")
	}
	// Power of attorney (since November 2022 a DDPI replaces the POA for
	// securities): N unless the details say a POA was given.
	b["ccdPoaFunds"], b["ccdPoaSecurities"] = firstOf(d.POAFunds, "N"), firstOf(d.POASecurities, "N")
	for _, x := range []string{b["ccdPoaFunds"], b["ccdPoaSecurities"]} {
		if x != "Y" && x != "N" {
			bad("Power of attorney: Y or N")
			break
		}
	}
	// UPI (individuals and HUFs): Y registered, N not, D deregistered (only
	// after Y); NA for other categories.
	b["ccdOptForUpi"] = "NA"
	if cat == "1" || cat == "3" {
		u := d.UPI
		if u == "" {
			u = "N"
			if c.UPIBlock > 0 {
				u = "Y"
			}
		}
		if u == "N" && (reg.UPI == "Y" || reg.UPI == "D") {
			u = "D"
		}
		if u == "D" && reg.UPI != "Y" && reg.UPI != "D" {
			u = "N"
		}
		b["ccdOptForUpi"] = u
		if !contains([]string{"Y", "N", "D"}, u) {
			bad("UPI: Y or N")
		}
	}
	// One record per segment, each with the segment's own status.
	if len(c.Segments) == 0 {
		bad("Segments: the client has none")
	}
	for _, s := range c.Segments {
		segInd, ok := nseUCISeg[s]
		if !ok {
			continue
		}
		st := c.SegmentStatus(s)
		if st == "Not registered" {
			continue
		}
		r := map[string]string{}
		for k, v := range b {
			r[k] = v
		}
		r["ccdSegInd"] = segInd
		r["ccdCltStatus"], r["ccdCltStatusReason"] = "A", ""
		switch {
		case st == SegClosed:
			if reg.SegHash[segInd] == "" {
				continue // never at NSE: nothing to close
			}
			r["ccdCltStatus"], r["ccdCltStatusReason"] = "C", c.SegStatus[s].Reason
		case st == SegSuspended || st == SegDormant:
			r["ccdCltStatus"], r["ccdCltStatusReason"] = "I", c.SegStatus[s].Reason
		case c.Inactive:
			r["ccdCltStatus"], r["ccdCltStatusReason"] = "I", "No activity for the inactivity period"
		}
		if runes(r["ccdCltStatusReason"]) > 200 {
			r["ccdCltStatusReason"] = string([]rune(r["ccdCltStatusReason"])[:200])
		}
		if s == "COM" && (cat == "12" || cat == "22" || cat == "23" || cat == "24") {
			bad("Segment: category %s is not allowed in commodity derivatives", cat)
		}
		j, _ := json.Marshal(r) // map keys are sorted: the hash is stable
		h := sha256.Sum256(j)
		m.Records = append(m.Records, nseUCIRecord{Seg: segInd, Body: r, Hash: hex.EncodeToString(h[:16])})
	}
	return m
}

// nseUCIContacts fills the contact persons of a non-individual: the TDD
// predefines the designation for partnerships (2), HUFs (3), companies (4)
// and trusts (5), and needs the first one (and for 2, 4 and 5 the second).
func nseUCIContacts(b map[string]string, cat string, cs []UCCContact, bad func(string, ...any)) {
	need := map[string]int{"2": 2, "3": 1, "4": 2, "5": 2}[cat]
	desig := func(i int) string {
		switch cat {
		case "2":
			return map[bool]string{true: "PARTNER", false: "PARTNER/POA HOLDER"}[i < 2]
		case "3":
			return map[int]string{0: "KARTA", 1: "COPARCENER /MEMBER"}[min(i, 2)] + map[bool]string{true: "COPARCENER / MEMBER"}[i >= 2]
		case "4":
			return map[bool]string{true: "DIRECTOR", false: "DIRECTOR/EMPLOYEE/OTHERS"}[i < 2]
		case "5":
			return map[bool]string{true: "TRUSTEE / MEMBER", false: "TRUSTEE / MEMBER/POA HOLDER"}[i < 2]
		}
		return ""
	}
	if len(cs) < need {
		bad("Contact persons: NSE needs %d for a %s (name, designation, PAN, address and phone); add them under Exchange UCC details", need, nseUCISupported[cat])
	}
	for i, c := range cs {
		if i == 5 {
			break
		}
		n := strconv.Itoa(i + 1)
		dg := firstOf(desig(i), c.Designation) // predefined for categories 2 to 5
		b["ccdDir"+n+"Name"], b["ccdDir"+n+"Desig"], b["ccdDir"+n+"Pan"], b["ccdDir"+n+"Add"], b["ccdDir"+n+"TelNo"], b["ccdDir"+n+"Email"] = c.Name, dg, c.PAN, c.Address, c.Phone, c.Email
		who := "Contact person " + n
		if l := runes(c.Name); l < 2 || l > 100 || !hasLetter(c.Name) {
			bad("%s: name of 2 to 100 characters", who)
		}
		if l := runes(dg); desig(i) == "" && (l < 5 || l > 25) {
			bad("%s: designation of 5 to 25 characters", who)
		}
		if !rePAN.MatchString(c.PAN) && c.PAN != "FOREIGNDIR" {
			bad("%s: PAN must be 5 letters, 4 digits and a letter (FOREIGNDIR for a foreign director without one)", who)
		}
		if len(strings.Fields(c.Address)) < 3 || runes(c.Address) > 255 || !hasLetter(c.Address) {
			bad("%s: address of at least 3 words", who)
		}
		if ph := strings.TrimSpace(c.Phone); runes(ph) < 5 || runes(ph) > 60 || strings.Trim(digitsOf(ph), "0") == "" {
			bad("%s: phone number of 5 to 60 characters", who)
		}
		if c.Email != "" && nseUCIEmailProblem(c.Email) != "" {
			bad("%s: e-mail %s", who, nseUCIEmailProblem(c.Email))
		}
		if cat == "4" {
			b["ccdDinCntrctPerson"+n] = c.DIN
			if i < 2 && !(c.DIN == "NO_DIN" || (len(c.DIN) == 8 && allDigits(c.DIN))) {
				bad("%s: the director's 8-digit DIN (NO_DIN for a foreign director without one)", who)
			}
		}
	}
}

// nseUCIMask hides Aadhaar numbers in a record for the audit copy and the
// preview; with screen true the PAN and account numbers too.
func nseUCIMask(r map[string]string, screen bool) map[string]string {
	out := map[string]string{}
	for k, v := range r {
		switch {
		case strings.HasPrefix(k, "ccdUid") && v != "":
			out[k] = strings.Repeat("X", max(0, len(v)-4)) + v[max(0, len(v)-4):]
		case screen && v != "" && (k == "ccdPanNo" || strings.HasPrefix(k, "ccdBankAcctNo") || strings.HasPrefix(k, "ccdBenAcctNo")):
			if k == "ccdPanNo" {
				out[k] = MaskPAN(v)
			} else {
				out[k] = MaskAccount(v)
			}
		default:
			out[k] = v
		}
	}
	return out
}

// ---- codes ----

var nseUCIUploadCodes = map[string]string{
	"200": "saved", "201": "NSE received an empty request", "202": "a mandatory field is missing", "203": "NSE could not read the request",
	"204": "the client is closed at NSE and cannot be changed", "205": "the client is inactive at NSE: send it as active before changing other details",
	"206": "the client is frozen at NSE", "207": "the member code is not valid", "208": "the member is debarred in this segment",
	"209": "the PAN is debarred", "210": "the PAN is already registered for another client in SLB", "211": "NSE could not run its debarment or SLB check: it will be tried again",
	"212": "too many clients in one request", "299": "NSE cannot process it at the moment: it will be tried again", "401": "NSE did not accept the login",
	"422": "NSE could not read the request",
}

var nseUCIPANCodes = map[string]string{
	"200": "found", "201": "the client code is not valid", "202": "NSE has no record of this client yet", "203": "the member code is not valid",
	"204": "NSE received an empty request", "205": "a mandatory field is missing", "206": "too many clients in one request",
	"299": "NSE cannot answer at the moment", "400": "NSE could not read the request", "401": "NSE did not accept the login", "422": "NSE could not read the request",
}

// nseUCIRetry: codes that say nothing about the record; it is sent again.
var nseUCIRetry = map[string]bool{"211": true, "212": true, "299": true, "401": true, "422": true, "201": true, "203": true}

var nseUCIPANText = map[string]string{"A": "Approved", "P": "Pending", "I": "Incorrect PAN"}

func nseUCICodeText(m map[string]string, code, desc string) string {
	t := m[code]
	switch {
	case t == "":
		return firstOf(strings.TrimSpace(desc), "NSE answered "+code)
	case code == "202" && desc != "" && m["200"] == "saved":
		return t + " (" + strings.TrimSpace(desc) + ")"
	}
	return t
}

// ---- calls ----

// nseUCIGet reads a field of an answer whatever its case ("status" and
// "Status" both occur in the TDD's sample).
func nseUCIGet(m map[string]any, name string) string {
	for k, v := range m {
		if strings.EqualFold(k, name) && v != nil {
			return strings.TrimSpace(fmt.Sprint(v))
		}
	}
	return ""
}

func nseUCIList(m map[string]any, name string) []map[string]any {
	for k, v := range m {
		if !strings.EqualFold(k, name) {
			continue
		}
		var out []map[string]any
		if l, ok := v.([]any); ok {
			for _, e := range l {
				if x, ok := e.(map[string]any); ok {
					out = append(out, x)
				}
			}
		}
		return out
	}
	return nil
}

type nseUCICall struct {
	ctx   context.Context
	x     nseUCIConn
	st    *NSEUCIState
	now   time.Time
	audit []NSEUCIBatchAudit
}

func (c *nseUCICall) post(path, token string, body any) (int, []byte, error) {
	j, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.x.base+path, bytes.NewReader(j))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := nseUCIHTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("could not reach NSE's UCC API: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, b, err
}

// login returns the token, logging in when there is none or it has run out.
func (c *nseUCICall) login() (string, error) {
	if exp, err := time.Parse(time.RFC3339, c.st.TokenExp); err == nil && c.now.Before(exp) && c.st.Token != "" {
		return c.st.Token, nil
	}
	code, b, err := c.post("/auth", "", map[string]string{"username": c.x.login, "password": c.x.password, "apiName": "uci_clientupload"})
	if err != nil {
		return "", err
	}
	var r map[string]any
	_ = json.Unmarshal(b, &r)
	tok := nseUCIGet(r, "token")
	if code != 200 || tok == "" {
		msg := firstOf(nseUCIGet(r, "msgDesc"), firstOf(nseUCIGet(r, "message"), firstOf(nseUCIGet(r, "error"), trimBody(b))))
		return "", fmt.Errorf("NSE did not accept the UCC API login (%d): %s; check the member code and password, and that NSE has activated the API user", code, msg)
	}
	c.st.Token, c.st.TokenExp = tok, nseUCITokenExp(tok, c.now).Format(time.RFC3339)
	return tok, nil
}

// nseUCITokenExp: the TDD's tokens last 30 minutes; the JWT's own expiry
// when it is sooner. A minute is kept in hand.
func nseUCITokenExp(tok string, now time.Time) time.Time {
	exp := now.Add(29 * time.Minute)
	if p := strings.Split(tok, "."); len(p) == 3 {
		if raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(p[1], "=")); err == nil {
			var cl struct {
				Exp int64 `json:"exp"`
			}
			if json.Unmarshal(raw, &cl) == nil && cl.Exp > 0 {
				if t := time.Unix(cl.Exp, 0).Add(-time.Minute); t.Before(exp) {
					exp = t
				}
			}
		}
	}
	return exp
}

// call posts with the token, logging in again once if NSE refuses it.
func (c *nseUCICall) call(path string, body any, records int) (int, []byte, error) {
	tok, err := c.login()
	if err != nil {
		c.audit = append(c.audit, NSEUCIBatchAudit{Call: "auth", Error: err.Error()})
		return 0, nil, err
	}
	code, b, err := c.post(path, tok, body)
	if err == nil && code == 401 {
		c.st.Token = ""
		if tok, err = c.login(); err != nil {
			c.audit = append(c.audit, NSEUCIBatchAudit{Call: "auth", Error: err.Error()})
			return code, b, err
		}
		code, b, err = c.post(path, tok, body)
	}
	a := NSEUCIBatchAudit{Call: strings.TrimPrefix(path, "/"), Records: records, HTTP: code, Response: string(b)}
	if len(a.Response) > 64<<10 {
		a.Response = a.Response[:64<<10] + "…"
	}
	if req, ok := body.(map[string]any); ok {
		if recs, ok := req["request"].([]map[string]string); ok {
			masked := make([]map[string]string, len(recs))
			for i, r := range recs {
				masked[i] = nseUCIMask(r, false)
			}
			j, _ := json.Marshal(map[string]any{"request": masked})
			a.Request = string(j)
		} else {
			j, _ := json.Marshal(req)
			a.Request = string(j)
		}
	}
	if err != nil {
		a.Error = err.Error()
	}
	c.audit = append(c.audit, a)
	return code, b, err
}

// ---- the screen and the sends ----

// NSEUCIClientRow is one client on the Exchange UCC screen.
type NSEUCIClientRow struct {
	UCC       string   `json:"ucc"`
	Name      string   `json:"name"`
	PAN       string   `json:"pan"`
	Action    string   `json:"action"`
	Status    string   `json:"status"`
	Reason    string   `json:"reason"`
	Problems  []string `json:"problems"`
	PANStatus string   `json:"pan_status"`
	SentAt    string   `json:"sent_at"`
	ResultAt  string   `json:"result_at"`
	Ref       string   `json:"ref"`
}

// NSEUCIRunRow is a run on the screen.
type NSEUCIRunRow struct {
	ID       string `json:"id"`
	At       string `json:"at"`
	By       string `json:"by"`
	Sent     int    `json:"sent"`
	Accepted int    `json:"accepted"`
	Rejected int    `json:"rejected"`
	Note     string `json:"note"`
}

// NSEUCIView is the NSE tab of the Exchange UCC screen.
type NSEUCIView struct {
	Exchange   string            `json:"exchange"`
	Connection string            `json:"connection"` // Not set, Saved, Working, Problem
	Live       bool              `json:"live"`       // production, not NSE's UAT
	Counts     map[string]int    `json:"counts"`
	Clients    []NSEUCIClientRow `json:"clients"`
	Runs       []NSEUCIRunRow    `json:"runs"`
	Problem    string            `json:"problem,omitempty"`
	Auto       bool              `json:"auto"`
}

// NSEUCIResult is what a send or a PAN status run did.
type NSEUCIResult struct {
	Message  string   `json:"message"`
	Sent     int      `json:"sent"`
	Accepted int      `json:"accepted"`
	Rejected int      `json:"rejected"`
	Problems []string `json:"problems"`
}

// nseUCIItem is a client's state: the records, what is pending and why.
type nseUCIItem struct {
	c       Client
	reg     NSEUCIReg
	hasReg  bool
	mapped  nseUCIMapped
	pending []nseUCIRecord
	row     NSEUCIClientRow
}

func nseUCIInScope(c Client, hasReg bool) bool {
	return hasReg || c.Status == "UCC upload" || c.Status == "Active"
}

// nseUCIItems works out every client's state.
func (o *Ops) nseUCIItems(ctx context.Context, tenant, member string, only map[string]bool) ([]nseUCIItem, error) {
	cs, err := o.Clients(ctx, tenant)
	if err != nil {
		return nil, err
	}
	regs, _ := docs.List[NSEUCIReg](ctx, o.Docs, tenant, cUCCReg, "NSE|")
	byUCC := map[string]NSEUCIReg{}
	for _, r := range regs {
		byUCC[r.UCC] = r
	}
	set, _ := o.Settings(ctx, tenant)
	src := o.uccSources(ctx, tenant)
	now := o.now()
	var out []nseUCIItem
	for _, c := range cs {
		if only != nil && !only[c.UCC] {
			continue
		}
		reg, hasReg := byUCC[c.UCC]
		if !nseUCIInScope(c, hasReg) {
			continue
		}
		var app *Application
		if a, ok := src.apps[c.UCC]; ok {
			app = &a
		}
		it := nseUCIItem{c: c, reg: reg, hasReg: hasReg}
		it.mapped = nseUCIMap(c, src.details[c.UCC], app, set, member, now, reg)
		for _, r := range it.mapped.Records {
			if reg.SegHash[r.Seg] != r.Hash {
				it.pending = append(it.pending, r)
			}
		}
		row := NSEUCIClientRow{UCC: c.UCC, Name: c.Name, PAN: MaskPAN(c.PAN), Action: "Add", Problems: []string{}, SentAt: reg.SentAt, ResultAt: reg.ResultAt, Ref: reg.Ref}
		if reg.Accepted {
			row.Action = "Modify"
		}
		if t := nseUCIPANText[reg.PANStatus]; t != "" {
			row.PANStatus = t
			for seg, s := range reg.TradeStatus {
				if strings.Contains(strings.ToLower(s), "not") {
					row.PANStatus += " · not permitted to trade (" + nseUCISegName[seg] + ")"
					break
				}
			}
		}
		switch {
		case len(it.pending) == 0 && hasReg && reg.Status != nseUCINotSent:
			row.Status, row.Reason = reg.Status, reg.Reason
			if reg.Action != "" {
				row.Action = reg.Action
			}
		case len(it.pending) == 0 && len(it.mapped.Records) == 0:
			row.Status, row.Problems = nseUCINeedsFix, it.mapped.Problems
			if len(row.Problems) == 0 {
				row.Problems = []string{"Segments: no segment to register at NSE"}
			}
			row.Reason = row.Problems[0]
		case len(it.mapped.Problems) > 0:
			row.Status, row.Problems, row.Reason = nseUCINeedsFix, it.mapped.Problems, it.mapped.Problems[0]
		default:
			row.Status = nseUCINotSent
			if hasReg && reg.Status == nseUCINotSent {
				row.Reason = reg.Reason // the last try did not get through
			} else if hasReg {
				row.Reason = "changed since it was last sent"
			}
		}
		it.row = row
		out = append(out, it)
	}
	return out, nil
}

func (o *Ops) nseUCIConn(ctx context.Context, tenant string) (Connection, nseUCIConn, error) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNSEUCI)
	if err != nil || cn.Status == "Not set" {
		return cn, nseUCIConn{}, fmt.Errorf("%w: set up the NSE UCC API in Settings → Connections", ErrState)
	}
	x, err := parseNSEUCIConn(cn)
	return cn, x, err
}

// NSEUCI is the NSE tab of the Exchange UCC screen.
func (o *Ops) NSEUCI(ctx context.Context, tenant, actor string) (NSEUCIView, error) {
	v := NSEUCIView{Exchange: "NSE", Connection: "Not set", Counts: map[string]int{}, Clients: []NSEUCIClientRow{}, Runs: []NSEUCIRunRow{}}
	if err := o.require(ctx, tenant, actor, "Viewing exchange UCC registrations", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return v, err
	}
	for _, s := range nseUCIStatuses {
		v.Counts[s] = 0
	}
	cn, x, cerr := o.nseUCIConn(ctx, tenant)
	member := x.member
	if cerr == nil {
		v.Connection, v.Live, v.Auto = firstOf(cn.Status, "Saved"), x.live, x.auto
		st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNSEUCIState, "state", NSEUCIState{})
		if st.LastError != "" {
			v.Connection, v.Problem = "Problem", st.LastError
		}
	} else if cn.Status != "" && cn.Status != "Not set" {
		v.Connection, v.Problem = "Problem", cerr.Error()
	}
	if member == "" {
		set, _ := o.Settings(ctx, tenant)
		member = set.MemberCodes["NSE"]
	}
	items, err := o.nseUCIItems(ctx, tenant, member, nil)
	if err != nil {
		return v, err
	}
	order := map[string]int{nseUCINeedsFix: 0, nseUCIRejected: 1, nseUCINotSent: 2, nseUCISent: 3, nseUCIAccepted: 4}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].row, items[j].row
		if order[a.Status] != order[b.Status] {
			return order[a.Status] < order[b.Status]
		}
		return a.UCC < b.UCC
	})
	for _, it := range items {
		v.Counts[it.row.Status]++
		v.Clients = append(v.Clients, it.row)
	}
	runs, _ := docs.List[NSEUCIRun](ctx, o.Docs, tenant, cUCCRun, "NSE-")
	for i := len(runs) - 1; i >= 0 && len(v.Runs) < 30; i-- {
		r := runs[i]
		v.Runs = append(v.Runs, NSEUCIRunRow{ID: r.ID, At: r.At, By: r.By, Sent: r.Sent, Accepted: r.Accepted, Rejected: r.Rejected, Note: r.Note})
	}
	return v, nil
}

// NSEUCIPreview is a client's records as they would go to NSE (PAN,
// account numbers and Aadhaar masked), with the problems.
type NSEUCIPreview struct {
	UCC      string              `json:"ucc"`
	Name     string              `json:"name"`
	Status   string              `json:"status"`
	Action   string              `json:"action"`
	Problems []string            `json:"problems"`
	Records  []map[string]string `json:"records"`
	Pending  []string            `json:"pending"` // segments that would be sent
	Details  UCCDetails          `json:"details"`
	History  []NSEUCIEvent       `json:"history"`
}

// NSEUCIPreview shows a client's mapped records before they are sent.
func (o *Ops) NSEUCIPreview(ctx context.Context, tenant, actor, ucc string) (NSEUCIPreview, error) {
	if err := o.require(ctx, tenant, actor, "Viewing exchange UCC registrations", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return NSEUCIPreview{}, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return NSEUCIPreview{}, err
	}
	_, x, _ := o.nseUCIConn(ctx, tenant)
	member := x.member
	if member == "" {
		set, _ := o.Settings(ctx, tenant)
		member = set.MemberCodes["NSE"]
	}
	items, err := o.nseUCIItems(ctx, tenant, member, map[string]bool{ucc: true})
	if err != nil {
		return NSEUCIPreview{}, err
	}
	p := NSEUCIPreview{UCC: ucc, Name: c.Name, Problems: []string{}, Records: []map[string]string{}, Pending: []string{}, Details: o.UCCDetailsFor(ctx, tenant, ucc), History: []NSEUCIEvent{}}
	if len(items) == 0 {
		p.Status, p.Problems = "Not in scope", []string{"the client is " + c.Status + ": clients go to NSE from the UCC upload stage"}
		return p, nil
	}
	it := items[0]
	p.Status, p.Action, p.Problems = it.row.Status, it.row.Action, it.mapped.Problems
	if p.Problems == nil {
		p.Problems = []string{}
	}
	for _, r := range it.mapped.Records {
		p.Records = append(p.Records, nseUCIMask(r.Body, true))
	}
	for _, r := range it.pending {
		p.Pending = append(p.Pending, r.Seg)
	}
	if it.reg.History != nil {
		p.History = it.reg.History
	}
	return p, nil
}

// NSEUCISend sends the clients that are new or changed (all of them, or
// those listed), batch by batch, and records NSE's answer for each.
func (o *Ops) NSEUCISend(ctx context.Context, tenant, actor string, uccs []string) (NSEUCIResult, error) {
	res := NSEUCIResult{Problems: []string{}}
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Sending clients to the exchange UCC", RoleMaker, RoleChecker); err != nil {
			return res, err
		}
	}
	_, x, err := o.nseUCIConn(ctx, tenant)
	if err != nil {
		return res, err
	}
	var only map[string]bool
	if len(uccs) > 0 {
		only = map[string]bool{}
		for _, u := range uccs {
			only[strings.ToUpper(strings.TrimSpace(u))] = true
		}
	}
	nseUCIMu.Lock()
	defer nseUCIMu.Unlock()
	now := o.now()
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNSEUCIState, "state", NSEUCIState{})
	st.LastSend = now.Format(time.RFC3339)
	saveState := func() { _ = docs.Upsert(ctx, o.Docs, tenant, cNSEUCIState, "state", st, SystemActor) }
	items, err := o.nseUCIItems(ctx, tenant, x.member, only)
	if err != nil {
		return res, err
	}
	var send []*nseUCIItem
	n := 0
	for i := range items {
		it := &items[i]
		if len(it.pending) == 0 {
			if only != nil {
				res.Problems = append(res.Problems, it.c.UCC+": nothing new to send ("+strings.ToLower(it.row.Status)+")")
			}
			continue
		}
		if len(it.mapped.Problems) > 0 {
			res.Problems = append(res.Problems, it.c.UCC+": "+strings.Join(it.mapped.Problems, "; "))
			continue
		}
		if n+len(it.pending) > nseUCIPerRun {
			break
		}
		n += len(it.pending)
		send = append(send, it)
	}
	if only != nil {
		for u := range only {
			found := false
			for _, it := range items {
				found = found || it.c.UCC == u
			}
			if !found {
				res.Problems = append(res.Problems, u+": not a client at the UCC upload stage or later")
			}
		}
	}
	if len(send) == 0 {
		saveState()
		res.Message = "Nothing to send to NSE"
		if len(res.Problems) > 0 {
			res.Message += fmt.Sprintf(": %d client(s) need fixing first", len(res.Problems))
		}
		return res, nil
	}
	seq, err := o.next(ctx, tenant, "uccrun-nse", SystemActor)
	if err != nil {
		return res, err
	}
	run := NSEUCIRun{ID: fmt.Sprintf("NSE-%05d", seq), Exchange: "NSE", Kind: "send", At: now.Format(time.RFC3339), By: actor}
	call := &nseUCICall{ctx: ctx, x: x, st: &st, now: now}
	at := now.Format(time.RFC3339)
	// Batches of up to 50 records, a client's segments together.
	var batches [][]*nseUCIItem
	cnt := 0
	for _, it := range send {
		if len(batches) == 0 || cnt+len(it.pending) > nseUCIBatch {
			batches = append(batches, nil)
			cnt = 0
		}
		batches[len(batches)-1] = append(batches[len(batches)-1], it)
		cnt += len(it.pending)
	}
	var callErr error
	type answer struct{ status, code, desc string }
	for _, bt := range batches {
		var recs []map[string]string
		for _, it := range bt {
			for _, r := range it.pending {
				recs = append(recs, r.Body)
			}
			run.UCCs = append(run.UCCs, it.c.UCC)
		}
		code, body, err := call.call("/clientupload", map[string]any{"request": recs}, len(recs))
		answers := map[string]answer{} // UCC|segment
		// batchErr: the call as a whole failed (NSE down, login refused, the
		// request refused without an answer per record). Nothing of it is
		// recorded as sent: the records go again in the next run.
		batchErr := ""
		if err != nil {
			callErr, batchErr = err, err.Error()
		} else {
			var v any
			_ = json.Unmarshal(body, &v)
			var list []map[string]any
			whole := answer{}
			switch t := v.(type) {
			case map[string]any:
				list = nseUCIList(t, "response")
				if list == nil {
					whole = answer{nseUCIGet(t, "status"), firstOf(nseUCIGet(t, "msgCode"), nseUCIGet(t, "statusCode")), nseUCIGet(t, "msgDesc")}
				}
			case []any:
				for _, e := range t {
					if m, ok := e.(map[string]any); ok {
						list = append(list, m)
					}
				}
			}
			for _, e := range list {
				k := strings.ToUpper(nseUCIGet(e, "clientCode")) + "|" + strings.ToUpper(nseUCIGet(e, "segment"))
				answers[k] = answer{nseUCIGet(e, "status"), nseUCIGet(e, "msgCode"), nseUCIGet(e, "msgDesc")}
			}
			if len(list) == 0 && (code != 200 || (whole.code != "" && whole.code != "200")) {
				c := firstOf(whole.code, strconv.Itoa(code))
				batchErr = nseUCICodeText(nseUCIUploadCodes, c, firstOf(whole.desc, trimBody(body)))
				callErr = fmt.Errorf("NSE's UCC API refused the request (%s): %s", c, batchErr)
			}
		}
		for _, it := range bt {
			ucc := it.c.UCC
			reg := it.reg
			reg.Exchange, reg.UCC = "NSE", ucc
			if reg.SegHash == nil {
				reg.SegHash = map[string]string{}
			}
			if reg.Segs == nil {
				reg.Segs = map[string]NSEUCISegResult{}
			}
			action := map[bool]string{true: "Modify", false: "Add"}[reg.Accepted]
			var segsSent []string
			retry := ""
			for _, r := range it.pending {
				if batchErr != "" {
					retry = batchErr
					continue
				}
				a, ok := answers[ucc+"|"+r.Seg]
				switch {
				case !ok:
					// Sent, but NSE's answer says nothing of it: the PAN
					// status check shows whether NSE has it.
					reg.SegHash[r.Seg] = r.Hash
					reg.Segs[r.Seg] = NSEUCISegResult{Status: nseUCISent, Reason: "no answer for this segment in NSE's reply", At: at}
					segsSent = append(segsSent, r.Seg)
				case a.code == "200" || (a.code == "" && strings.HasPrefix(strings.ToUpper(a.status), "SUC")):
					reg.SegHash[r.Seg] = r.Hash
					reg.Segs[r.Seg] = NSEUCISegResult{Status: nseUCIAccepted, Code: firstOf(a.code, "200"), Reason: "saved", At: at}
					reg.Accepted = true
					segsSent = append(segsSent, r.Seg)
				case nseUCIRetry[a.code]:
					retry = nseUCICodeText(nseUCIUploadCodes, a.code, a.desc)
				default:
					reg.SegHash[r.Seg] = r.Hash
					reg.Segs[r.Seg] = NSEUCISegResult{Status: nseUCIRejected, Code: a.code, Reason: nseUCICodeText(nseUCIUploadCodes, a.code, a.desc), At: at}
					segsSent = append(segsSent, r.Seg)
				}
			}
			if len(segsSent) > 0 {
				reg.Action, reg.Ref, reg.SentAt, reg.ResultAt = action, run.ID, at, at
				reg.UPI = it.pending[0].Body["ccdOptForUpi"]
			}
			// The client's status from its segments.
			var rejected []string
			sent := false
			for seg, s := range reg.Segs {
				switch s.Status {
				case nseUCIRejected:
					rejected = append(rejected, nseUCISegName[seg]+": "+s.Reason)
				case nseUCISent:
					sent = true
				}
			}
			sort.Strings(rejected)
			switch {
			case retry != "" && len(segsSent) == 0:
				reg.Status, reg.Reason = nseUCINotSent, retry
			case len(rejected) > 0:
				reg.Status, reg.Reason = nseUCIRejected, strings.Join(rejected, "; ")
			case sent:
				reg.Status, reg.Reason = nseUCISent, "waiting for NSE to confirm"
			default:
				reg.Status, reg.Reason = nseUCIAccepted, ""
			}
			if retry != "" && len(segsSent) > 0 {
				reg.Reason = strings.TrimPrefix(reg.Reason+"; some segments to be tried again: "+retry, "; ")
			}
			if len(segsSent) > 0 {
				res.Sent++
				switch reg.Status {
				case nseUCIAccepted:
					res.Accepted++
				case nseUCIRejected:
					res.Rejected++
				}
				detail := strings.Join(segsSent, ",") + ": " + strings.ToLower(reg.Status)
				if reg.Reason != "" {
					detail += " (" + reg.Reason + ")"
				}
				reg.event(at, action+" sent", run.ID, detail)
			} else {
				reg.event(at, "Not sent", run.ID, reg.Reason)
			}
			_ = docs.Upsert(ctx, o.Docs, tenant, cUCCReg, "NSE|"+ucc, reg, actor)
			if reg.Status == nseUCIRejected {
				o.raiseWork(ctx, tenant, "nseuci", "rejected-"+ucc, "NSE rejected the UCC record of "+it.c.Name,
					fmt.Sprintf("NSE's UCC API did not take %s (%s): %s. Correct the client record or the Exchange UCC details; the corrected record is sent again.", ucc, it.c.Name, reg.Reason),
					ucc, RoleMaker, "uccapi", "")
			} else if reg.Status == nseUCIAccepted {
				o.closeWork(ctx, tenant, actor, "nseuci", "rejected-"+ucc, "accepted by NSE in "+run.ID)
			}
		}
		if callErr != nil {
			break // NSE is down or refused the call: the rest wait for the next run
		}
	}
	run.Batches, run.Sent, run.Accepted, run.Rejected = call.audit, res.Sent, res.Accepted, res.Rejected
	res.Message = fmt.Sprintf("Sent %d client(s) to NSE: %d accepted, %d rejected", res.Sent, res.Accepted, res.Rejected)
	if w := res.Sent - res.Accepted - res.Rejected; w > 0 {
		res.Message += fmt.Sprintf(", %d waiting for NSE to confirm", w)
	}
	run.Note = res.Message
	if callErr != nil {
		run.Note += "; stopped: " + callErr.Error()
		res.Message += "; stopped: " + callErr.Error()
		st.LastError = callErr.Error()
		res.Problems = append(res.Problems, callErr.Error())
	} else {
		st.LastError, st.LastOK = "", at
	}
	saveState()
	_ = docs.Upsert(ctx, o.Docs, tenant, cUCCRun, run.ID, run, actor)
	o.log(ctx, tenant, actor, "uccapi.nse.sent", run.ID, map[string]any{"clients": res.Sent, "accepted": res.Accepted, "rejected": res.Rejected, "uccs": run.UCCs, "stopped": callErr != nil})
	return res, nil
}

// ---- PAN status ----

type nseUCIPANRow struct {
	ucc, seg, pan, panAt, seeding, trade string
}

func nseUCIPANRows(b []byte) (rows []nseUCIPANRow, codes map[string]string) {
	codes = map[string]string{}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil, codes
	}
	var groups []map[string]any
	switch t := v.(type) {
	case map[string]any:
		if l := nseUCIList(t, "response"); l != nil {
			groups = l
		} else {
			groups = []map[string]any{t}
		}
	case []any:
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				groups = append(groups, m)
			}
		}
	}
	for _, g := range groups {
		code := firstOf(nseUCIGet(g, "statusCode"), nseUCIGet(g, "msgCode"))
		data := nseUCIList(g, "uccStatusData")
		for _, d := range data {
			r := nseUCIPANRow{ucc: strings.ToUpper(nseUCIGet(d, "clientCode")), seg: strings.ToUpper(nseUCIGet(d, "segment")), pan: strings.ToUpper(nseUCIGet(d, "panStatus")),
				panAt: nseUCIGet(d, "panStatusDate"), seeding: nseUCIGet(d, "adhrSeedingStatus"), trade: nseUCIGet(d, "exchangeStatus")}
			if r.ucc != "" {
				rows = append(rows, r)
			}
		}
		if len(data) == 0 && code != "" {
			codes[""] = code + " " + nseUCIGet(g, "msgDesc")
		}
	}
	return rows, codes
}

// nseUCIApplyPAN stores NSE's PAN status rows on the registrations.
func (o *Ops) nseUCIApplyPAN(ctx context.Context, tenant, actor string, rows []nseUCIPANRow, at string, res *NSEUCIResult) map[string]bool {
	by := map[string][]nseUCIPANRow{}
	for _, r := range rows {
		by[r.ucc] = append(by[r.ucc], r)
	}
	cs, _ := o.ClientMap(ctx, tenant)
	seen := map[string]bool{}
	for ucc, rs := range by {
		c, ok := cs[ucc]
		if !ok {
			continue
		}
		seen[ucc] = true
		reg, _, _ := docs.GetOr(ctx, o.Docs, tenant, cUCCReg, "NSE|"+ucc, NSEUCIReg{Exchange: "NSE", UCC: ucc})
		if reg.TradeStatus == nil {
			reg.TradeStatus = map[string]string{}
		}
		if reg.Segs == nil {
			reg.Segs = map[string]NSEUCISegResult{}
		}
		worst := ""
		rank := map[string]int{"": 0, "A": 1, "P": 2, "I": 3}
		notPermitted := []string{}
		for _, r := range rs {
			if rank[r.pan] > rank[worst] {
				worst = r.pan
			}
			if r.panAt != "" {
				reg.PANStatusAt = r.panAt
			}
			reg.AadhaarSeeding = firstOf(r.seeding, reg.AadhaarSeeding)
			if r.seg != "" {
				reg.TradeStatus[r.seg] = r.trade
				if strings.Contains(strings.ToLower(r.trade), "not") {
					notPermitted = append(notPermitted, nseUCISegName[r.seg])
				}
				// NSE has the record: a send without an answer is confirmed.
				if s, ok := reg.Segs[r.seg]; ok && s.Status == nseUCISent {
					reg.Segs[r.seg] = NSEUCISegResult{Status: nseUCIAccepted, Code: "200", Reason: "found at NSE", At: at}
				}
			}
		}
		reg.Accepted = true // NSE has the client code: later sends modify it
		if reg.Status == nseUCISent {
			still := false
			for _, s := range reg.Segs {
				still = still || s.Status == nseUCISent
			}
			if !still {
				reg.Status, reg.Reason, reg.ResultAt = nseUCIAccepted, "", at
			}
		}
		prev := reg.PANStatus
		reg.PANStatus, reg.PANCheckedAt = worst, at
		if prev != worst {
			reg.event(at, "PAN status "+firstOf(nseUCIPANText[worst], worst), "", "")
		}
		_ = docs.Upsert(ctx, o.Docs, tenant, cUCCReg, "NSE|"+ucc, reg, actor)
		switch worst {
		case "I":
			res.Rejected++
			o.raiseWork(ctx, tenant, "nseuci", "pan-"+ucc, "NSE: incorrect PAN for "+c.Name,
				fmt.Sprintf("NSE could not verify the PAN of %s (%s) with the Income Tax Department: the PAN, the name or the date of birth does not match the PAN record. Check them against the PAN card and the ITD record and correct the client record; the corrected record is sent to NSE again.", c.Name, ucc),
				ucc, RoleMaker, "uccapi", "")
		case "A":
			res.Accepted++
			o.closeWork(ctx, tenant, actor, "nseuci", "pan-"+ucc, "PAN approved at NSE")
		}
		if len(notPermitted) > 0 {
			sort.Strings(notPermitted)
			o.raiseWork(ctx, tenant, "nseuci", "trade-"+ucc, c.Name+" is not permitted to trade at NSE",
				fmt.Sprintf("NSE shows %s (%s) as not permitted to trade in %s (PAN %s, Aadhaar seeding %s). Find out why with the client (PAN verification, PAN-Aadhaar link) and NSE before the client trades.",
					ucc, c.Name, strings.Join(notPermitted, ", "), strings.ToLower(firstOf(nseUCIPANText[worst], "not known")), firstOf(reg.AadhaarSeeding, "not known")),
				ucc, RoleCompliance, "uccapi", "")
		} else {
			o.closeWork(ctx, tenant, actor, "nseuci", "trade-"+ucc, "permitted to trade at NSE")
		}
	}
	return seen
}

// NSEUCIPANStatus asks NSE for the PAN status of the clients listed (empty:
// every client sent to NSE whose PAN is not yet approved, or that NSE has
// not confirmed), 50 to a call.
func (o *Ops) NSEUCIPANStatus(ctx context.Context, tenant, actor string, uccs []string) (NSEUCIResult, error) {
	res := NSEUCIResult{Problems: []string{}}
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Checking PAN status at the exchange", RoleMaker, RoleChecker, RoleCompliance); err != nil {
			return res, err
		}
	}
	_, x, err := o.nseUCIConn(ctx, tenant)
	if err != nil {
		return res, err
	}
	if len(uccs) == 0 {
		regs, _ := docs.List[NSEUCIReg](ctx, o.Docs, tenant, cUCCReg, "NSE|")
		for _, r := range regs {
			permitted := true
			for _, t := range r.TradeStatus {
				permitted = permitted && !strings.Contains(strings.ToLower(t), "not")
			}
			if (r.Accepted || r.Status == nseUCISent) && (r.PANStatus != "A" || !permitted || r.Status == nseUCISent) {
				uccs = append(uccs, r.UCC)
			}
		}
	}
	var list []string
	for _, u := range uccs {
		u = strings.ToUpper(strings.TrimSpace(u))
		if reNSEUCICode.MatchString(u) {
			list = append(list, u)
		}
	}
	sort.Strings(list)
	if len(list) == 0 {
		res.Message = "No client to check at NSE"
		return res, nil
	}
	nseUCIMu.Lock()
	defer nseUCIMu.Unlock()
	now := o.now()
	at := now.Format(time.RFC3339)
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNSEUCIState, "state", NSEUCIState{})
	call := &nseUCICall{ctx: ctx, x: x, st: &st, now: now}
	seq, err := o.next(ctx, tenant, "uccrun-nse", SystemActor)
	if err != nil {
		return res, err
	}
	run := NSEUCIRun{ID: fmt.Sprintf("NSE-%05d", seq), Exchange: "NSE", Kind: "pan-status", At: at, By: actor, UCCs: list}
	var callErr error
	found := map[string]bool{}
	for i := 0; i < len(list); i += nseUCIBatch {
		part := list[i:min(i+nseUCIBatch, len(list))]
		code, b, err := call.call("/getuccpanstatus", map[string]any{"request": map[string]any{"memberCode": x.member, "ucc": part}}, len(part))
		if err != nil {
			callErr = err
			break
		}
		rows, codes := nseUCIPANRows(b)
		if code != 200 && len(rows) == 0 {
			c := strings.Fields(codes[""] + " " + strconv.Itoa(code))[0]
			callErr = fmt.Errorf("NSE's PAN status answered %d: %s", code, nseUCICodeText(nseUCIPANCodes, c, trimBody(b)))
			break
		}
		for u := range o.nseUCIApplyPAN(ctx, tenant, actor, rows, at, &res) {
			found[u] = true
		}
	}
	for _, u := range list {
		if !found[u] && callErr == nil {
			res.Problems = append(res.Problems, u+": "+nseUCIPANCodes["202"])
		}
	}
	run.Batches, run.Sent, run.Accepted, run.Rejected = call.audit, len(list), res.Accepted, res.Rejected
	run.Note = fmt.Sprintf("PAN status of %d client(s) asked; %d found at NSE: %d PAN approved, %d incorrect PAN", len(list), len(found), res.Accepted, res.Rejected)
	if callErr != nil {
		run.Note += "; stopped: " + callErr.Error()
		st.LastError = callErr.Error()
		res.Problems = append(res.Problems, callErr.Error())
	} else {
		st.LastError, st.LastOK = "", at
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cNSEUCIState, "state", st, SystemActor)
	_ = docs.Upsert(ctx, o.Docs, tenant, cUCCRun, run.ID, run, actor)
	o.log(ctx, tenant, actor, "uccapi.nse.pan_status", run.ID, map[string]any{"asked": len(list), "found": len(found), "uccs": list})
	res.Message = fmt.Sprintf("PAN status checked for %d client(s): %d found at NSE, %d PAN approved, %d incorrect PAN", len(list), len(found), res.Accepted, res.Rejected)
	if callErr != nil {
		res.Message += "; " + callErr.Error()
	}
	res.Sent = len(list)
	return res, nil
}

// nseUCIPANByDate asks NSE for the PAN status of every UCC created or
// changed in the last 10 days (NSE's longest range), segment by segment.
func (o *Ops) nseUCIPANByDate(ctx context.Context, tenant, actor string, now time.Time) (NSEUCIResult, error) {
	res := NSEUCIResult{Problems: []string{}}
	_, x, err := o.nseUCIConn(ctx, tenant)
	if err != nil {
		return res, err
	}
	segs := map[string]bool{}
	regs, _ := docs.List[NSEUCIReg](ctx, o.Docs, tenant, cUCCReg, "NSE|")
	for _, r := range regs {
		for s := range r.SegHash {
			segs[s] = true
		}
	}
	if len(segs) == 0 {
		return res, nil
	}
	nseUCIMu.Lock()
	defer nseUCIMu.Unlock()
	at := now.Format(time.RFC3339)
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNSEUCIState, "state", NSEUCIState{})
	call := &nseUCICall{ctx: ctx, x: x, st: &st, now: now}
	from, to := now.AddDate(0, 0, -9).Format("02-01-2006"), now.Format("02-01-2006")
	var callErr error
	for _, s := range sortedKeys(segs) {
		code, b, err := call.call("/getpanstatus", map[string]any{"request": map[string]string{"memberCode": x.member, "segment": s, "fromDate": from, "toDate": to}}, 0)
		if err != nil {
			callErr = err
			break
		}
		rows, codes := nseUCIPANRows(b)
		if code != 200 && len(rows) == 0 && !strings.HasPrefix(codes[""], "202") {
			callErr = fmt.Errorf("NSE's PAN status answered %d: %s", code, firstOf(codes[""], trimBody(b)))
			break
		}
		res.Sent += len(o.nseUCIApplyPAN(ctx, tenant, actor, rows, at, &res))
	}
	if callErr != nil {
		st.LastError = callErr.Error()
		res.Problems = append(res.Problems, callErr.Error())
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cNSEUCIState, "state", st, SystemActor)
	if len(call.audit) > 0 {
		if seq, err := o.next(ctx, tenant, "uccrun-nse", SystemActor); err == nil {
			run := NSEUCIRun{ID: fmt.Sprintf("NSE-%05d", seq), Exchange: "NSE", Kind: "pan-status", At: at, By: actor, Sent: res.Sent, Accepted: res.Accepted, Rejected: res.Rejected, Batches: call.audit,
				Note: fmt.Sprintf("Daily PAN status for %s to %s: %d client(s) found", from, to, res.Sent)}
			if callErr != nil {
				run.Note += "; stopped: " + callErr.Error()
			}
			_ = docs.Upsert(ctx, o.Docs, tenant, cUCCRun, run.ID, run, actor)
		}
	}
	return res, callErr
}

// nseUCITick, when the connection's automatic sending is on, sends the new
// and changed clients every 15 minutes from 08:00 to 22:00, and once a day
// refreshes the PAN status: every UCC NSE created in the last 10 days, then
// the clients sent earlier whose PAN is not yet approved.
func (o *Ops) nseUCITick(ctx context.Context, tenant string, now time.Time) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNSEUCI)
	if err != nil || cn.Status == "Not set" || cn.Values["send"] != "On" {
		return
	}
	if hm := now.In(ist).Format("15:04"); hm < "08:00" || hm >= "22:00" {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cNSEUCIState, "state", NSEUCIState{})
	if t, err := time.Parse(time.RFC3339, st.LastSend); err != nil || now.Sub(t) >= 15*time.Minute {
		_, _ = o.NSEUCISend(ctx, tenant, SystemActor, nil)
	}
	day := now.In(ist).Format("2006-01-02")
	if st.LastPAN != day {
		_, _ = o.nseUCIPANByDate(ctx, tenant, SystemActor, now)
		_, _ = o.NSEUCIPANStatus(ctx, tenant, SystemActor, nil)
		_, _ = docs.Update(ctx, o.Docs, tenant, cNSEUCIState, "state", SystemActor, func(s *NSEUCIState) error { s.LastPAN = day; return nil })
	}
}
