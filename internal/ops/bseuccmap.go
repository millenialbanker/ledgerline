package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- BSE UCC: mapping a client to BSE's UCC record ----
//
// The record is the same in the ADD UCC API (AddUCCData_V5, "BSE UCC REST
// API v0.2.2") and the batch upload file (136 pipe-separated fields, the
// "UCC Batch Upload file format"): bseUCCFields lists the fields in the
// specification's numbering with the API's JSON names. The file writes the
// values in that order; the API sends them as JSON in the order of BSE's
// sample request (BATUSER moves next to SERVERIP, which the file lacks).
//
// Where each value comes from in Ledgerline:
//   - client code, PAN, name, type, mobile, email, city, state, bank,
//     demat, segments and their statuses, income band, KYC date, re-KYC,
//     special categories (PEP), inactivity, UPI block: the client record;
//   - the address line and PIN code: the online application that opened
//     the account (eapply), else the client's MF profile;
//   - NRE or NRO, and the F&O custodial participant code: the client's NRI
//     (PIS) details;
//   - nomination: the client's nominees or opt-out.
// Ledgerline keeps no Aadhaar number (only the last four digits of a
// nominee's), no CIN, directors, contact persons or custodian, so a body
// corporate or HUF cannot be registered from here: it is reported as a
// problem to register on BSE's UCC portal.

// bseUCCFields are the 136 fields: JSON key in specification order.
var bseUCCFields = []string{
	"TRANSACTIONCODE", "BATUSER", "CLIENTTYPE", "STATUS", "CATEGORY", "CLIENTCODE", "PANNO", "POLITICALEXPERSON", "ADDRESS1", "PERMNEQUALCORP", // 1-10
	"ADDRESS2", "COUNTRY", "STATE", "CITY", "PINCODE", "TYPEOFSERVICE", "CONTACTDETAILS", "EMAIL", "MOBILENUMBER", "STDCODE", // 11-20
	"PHONENO", "EQ_CPCODE", "EQCMID", "FNOCPCODE", "FNOCMID", "DEPOSITORYNAME1", "DEMANTID1", "DEPOSITORYPARTICIPANT1", "DEPOSITORYNAME2", "DEMANTID2", // 21-30
	"DEPOSITORYPARTICIPANT2", "DEPOSITORYNAME3", "DEMANTID3", "DEPOSITORYPARTICIPANT3", "BANKNAME1", "ACCOUNTNO1", "BANKNAME2", "ACCOUNTNO2", "BANKNAME3", "ACCOUNTNO3", // 31-40
	"CLIENTAGGREMENTDATE", "PROVIDEDETAILS", "INCOME", "INCOMEDATE", "NETWORTH", "NETWORTHDATE", "ISACTIVE", "UPDATEREASON", "FIRSTNAME", "MIDDLENAME", // 41-50
	"LASTNAME", "AADHARCARDNO", "DATEOFBIRTH", "CLIENTNAME", "REGISTRATIONNO", "REGISTERINGAUTHORITY", "DATEOFREGISTRATION", "PLACEOFREGISTRATION", "WHETHERCORPORATE", "CINNO", // 51-60
	"NUMBEROFDIRECTORS", "PARTNERS_KARTAUID", "PARTNERS_COPARCENERUID", "CONTACTPERSONNAME1", "CONTACTPERSONDESIGNATION1", "CONTACTPERSONADDRESS1", "CONTACTPERSONEMAIL1", "CONTACTPERSONPAN1", "CONTACTPERSONMOBILE1", "CONTACTPERSONNAME2", // 61-70
	"CONTACTPERSONDESIGNATION2", "CONTACTPERSONADDRESS2", "CONTACTPERSONEMAIL2", "CONTACTPERSONPAN2", "CONTACTPERSONMOBILE2", "CASH", "EQUITY_DERIVATIVE", "SLB", "CURRENCY", "DEBT", // 71-80
	"ISPOA", "POAFORFUND", "POAFORSECURITY", "DATEOFPOAFORFUND", "DATEOFPOAFORSECURITY", "PERCOUNTRY", "PERSTATE", "PERCITY", "PERPINCODE", "CURRENCYCPCODE", // 81-90
	"CURRENCYCMID", "ENROLLMENTNUMBER", "COMMDERIVATIVES", "OPTEDFORNOMINATION", "CUSTPANNO", "CUSTNAME", "CUSTADDRESS", "CUSTCOUNTRY", "CUSTSTATE", "CUSTCITY", // 91-100
	"CUSTPINCODE", "CUSTMOBILENUMBER", "CUSTSTDCODE", "CUSTPHONENO", "CUSTEMAIL", "EGR", "BENEFICIALOWNACNTNO1", "BENEFICIALOWNACNTNO2", "BENEFICIALOWNACNTNO3", "OPTED_FOR_UPI", // 101-110
	"BANKBRANCHIFSCCODE1", "PRIMARYORSECONDARYBANK1", "BANKBRANCHIFSCCODE2", "PRIMARYORSECONDARYBANK2", "BANKBRANCHIFSCCODE3", "PRIMARYORSECONDARYBANK3", "BANKBRANCHIFSCCODE4", "BANKNAME4", "ACCOUNTNO4", "PRIMARYORSECONDARYBANK4", // 111-120
	"BANKBRANCHIFSCCODE5", "BANKNAME5", "ACCOUNTNO5", "PRIMARYORSECONDARYBANK5", "PRIMARYORSECONDARYDP1", "PRIMARYORSECONDARYDP2", "PRIMARYORSECONDARYDP3", "DEPOSITORYNAME4", "DEMANTID4", "BENEFICIALOWNACNTNO4", // 121-130
	"PRIMARYORSECONDARYDP4", "DEPOSITORYNAME5", "DEMANTID5", "BENEFICIALOWNACNTNO5", "PRIMARYORSECONDARYDP5", "CLIENTNAMEDESCRIPTION", // 131-136
}

// bseUCCJSONOrder is the order of BSE's sample request.
var bseUCCJSONOrder = func() []string {
	var out []string
	for _, k := range bseUCCFields {
		switch k {
		case "BATUSER":
			continue
		case "CASH":
			out = append(out, "SERVERIP", "BATUSER")
		}
		out = append(out, k)
	}
	return out
}()

// bseUCCMaxLen is each field's length in the specification.
var bseUCCMaxLen = map[string]int{
	"CLIENTCODE": 10, "PANNO": 10, "ADDRESS1": 150, "ADDRESS2": 150, "COUNTRY": 25, "STATE": 25, "CITY": 25, "PINCODE": 10, "EMAIL": 50, "MOBILENUMBER": 17,
	"FNOCPCODE": 14, "DEPOSITORYPARTICIPANT1": 50, "BANKNAME1": 255, "ACCOUNTNO1": 19, "UPDATEREASON": 100, "CLIENTNAME": 85, "PERCOUNTRY": 25, "PERSTATE": 25,
	"PERCITY": 25, "PERPINCODE": 10, "BENEFICIALOWNACNTNO1": 16, "DEMANTID1": 8, "BANKBRANCHIFSCCODE1": 11,
}

// The free-text fields take letters, digits, spaces and these.
var (
	reBSEText     = regexp.MustCompile(`^[A-Za-z0-9 @#$&*()\-+={}\[\]/\\:",.]*$`)
	reBSECode     = regexp.MustCompile(`^[A-Za-z0-9.*\\/\-]{1,10}$`)
	reBSEPin      = regexp.MustCompile(`^[1-9][0-9]{5}$`)
	reBSECDSLBO   = regexp.MustCompile(`^1[0-9]{15}$`)
	reBSENSDLBO   = regexp.MustCompile(`^IN[0-9]{6}[0-9]{8}$`)
	reBSEBankName = regexp.MustCompile(`[A-Za-z]`)
)

var bseUCCTextFields = []string{"ADDRESS1", "ADDRESS2", "FIRSTNAME", "MIDDLENAME", "LASTNAME", "CLIENTNAME", "UPDATEREASON", "PLACEOFREGISTRATION"}

// bseIncome is the income band's code: individuals have five bands (the
// fifth is above 25 lakh), non-individuals six.
func bseIncome(band string, individual bool) string {
	switch strings.ReplaceAll(strings.ToUpper(band), " ", "") {
	case "<1L", "BELOW1L":
		return "1"
	case "1-5L":
		return "2"
	case "5-10L":
		return "3"
	case "10-25L":
		return "4"
	case "25L-1CR", ">25L":
		return "5"
	case ">1CR":
		if individual {
			return "5"
		}
		return "6"
	}
	return ""
}

// bseStates are the Indian states and union territories, as BSE's
// state-city master is expected to spell them (BSE's master was not in the
// specification: a name BSE spells differently is rejected by BSE and
// reported back; aliases are mapped here as they are found).
var bseStates = func() map[string]string {
	m := map[string]string{}
	for _, s := range []string{"ANDHRA PRADESH", "ARUNACHAL PRADESH", "ASSAM", "BIHAR", "CHHATTISGARH", "GOA", "GUJARAT", "HARYANA", "HIMACHAL PRADESH", "JHARKHAND",
		"KARNATAKA", "KERALA", "MADHYA PRADESH", "MAHARASHTRA", "MANIPUR", "MEGHALAYA", "MIZORAM", "NAGALAND", "ODISHA", "PUNJAB", "RAJASTHAN", "SIKKIM", "TAMIL NADU",
		"TELANGANA", "TRIPURA", "UTTAR PRADESH", "UTTARAKHAND", "WEST BENGAL", "ANDAMAN AND NICOBAR ISLANDS", "CHANDIGARH", "DADRA AND NAGAR HAVELI AND DAMAN AND DIU",
		"DELHI", "JAMMU AND KASHMIR", "LADAKH", "LAKSHADWEEP", "PUDUCHERRY"} {
		m[s] = s
	}
	for a, s := range map[string]string{"ORISSA": "ODISHA", "PONDICHERRY": "PUDUCHERRY", "NEW DELHI": "DELHI", "NCT OF DELHI": "DELHI", "UTTARANCHAL": "UTTARAKHAND",
		"ANDAMAN & NICOBAR ISLANDS": "ANDAMAN AND NICOBAR ISLANDS", "JAMMU & KASHMIR": "JAMMU AND KASHMIR", "DAMAN AND DIU": "DADRA AND NAGAR HAVELI AND DAMAN AND DIU",
		"DADRA AND NAGAR HAVELI": "DADRA AND NAGAR HAVELI AND DAMAN AND DIU"} {
		m[a] = s
	}
	return m
}()

// bseUCCSources are the records a client's UCC is made from beyond the
// client itself, read once per run.
type bseUCCSources struct {
	apps    map[string]Application
	mf      map[string]MFProfile
	nri     map[string]NRIDetails
	firmPAN string
}

func (o *Ops) bseUCCLoadSources(ctx context.Context, tenant string, set Settings) bseUCCSources {
	s := bseUCCSources{apps: map[string]Application{}, mf: map[string]MFProfile{}, nri: map[string]NRIDetails{}, firmPAN: firmPAN(set)}
	if as, err := docs.List[Application](ctx, o.Docs, tenant, cApplication, ""); err == nil {
		for _, a := range as {
			if a.UCC != "" && strings.TrimSpace(a.Address) != "" {
				s.apps[a.UCC] = a
			}
		}
	}
	if ps, err := docs.List[MFProfile](ctx, o.Docs, tenant, cMFProf, ""); err == nil {
		for _, p := range ps {
			s.mf[p.UCC] = p
		}
	}
	if ns, err := docs.List[NRIDetails](ctx, o.Docs, tenant, cNRI, ""); err == nil {
		for _, n := range ns {
			s.nri[n.UCC] = n
		}
	}
	return s
}

// bseUCCRecord is one client's mapped record and what is wrong with it.
type bseUCCRecord struct {
	Values   map[string]string // by JSON key; "" is sent as null and written empty
	Problems []string
	Summary  map[string]string // the parts whose change needs a reason
}

func bseDate(d string) string {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return ""
	}
	return t.Format("02/01/2006")
}

// bseUCCMap maps a client to BSE's record for the action (Add or Modify).
// last is the summary of the record BSE last accepted (nil for none).
func bseUCCMap(c Client, src bseUCCSources, action string, last map[string]string, now time.Time) bseUCCRecord {
	v := map[string]string{}
	var probs []string
	bad := func(f string, a ...any) { probs = append(probs, fmt.Sprintf(f, a...)) }
	today := now.In(ist).Format("2006-01-02")

	v["TRANSACTIONCODE"] = map[string]string{"Add": "N", "Modify": "M"}[action]
	v["CLIENTCODE"] = c.UCC
	if !reBSECode.MatchString(c.UCC) {
		bad("The client code %q is not one BSE takes: up to 10 letters and digits (with . * / \\ - only)", c.UCC)
	}
	v["PANNO"] = c.PAN
	if !rePAN.MatchString(c.PAN) {
		bad("The PAN is not 5 letters, 4 digits and a letter")
	}

	// Client type, status and category.
	individual := c.Type == "Individual" || c.Type == "NRI"
	switch c.Type {
	case "Individual":
		v["CLIENTTYPE"], v["CATEGORY"] = "I", "I"
	case "NRI":
		v["CLIENTTYPE"] = "I"
		switch src.nri[c.UCC].Account {
		case "NRE":
			v["CATEGORY"] = "NRE"
		case "NRO":
			v["CATEGORY"] = "NRO"
		default:
			bad("BSE registers an NRI as NRE or NRO: record the client's NRI (PIS) details first")
		}
		v["FNOCPCODE"] = strings.ToUpper(strings.TrimSpace(src.nri[c.UCC].CPCode))
	case "HUF":
		v["CLIENTTYPE"], v["CATEGORY"] = "NI", "HUF"
		bad("BSE needs two contact persons (the karta and another, with designation, address, PAN and mobile) for an HUF, which Ledgerline does not hold: register this HUF on BSE's UCC portal")
	case "Corporate":
		v["CLIENTTYPE"], v["CATEGORY"] = "NI", "BCO"
		bad("BSE needs the CIN, the date of incorporation, at least two directors (name, DIN, PAN) and two contact persons for a body corporate, which Ledgerline does not hold: register this company on BSE's UCC portal")
	default:
		bad("BSE has no client category for the client type %q", c.Type)
	}
	v["STATUS"] = "CL"
	if src.firmPAN != "" && c.PAN == src.firmPAN {
		v["STATUS"] = "OW" // the firm's own account: BSE checks the PAN is the member's
	}

	// Politically exposed: a PEP, or a relative of one (the safer reading:
	// BSE's single flag is set for both, as KYC treats them alike).
	v["POLITICALEXPERSON"] = "N"
	if contains(c.CSC, "Politically exposed person") || contains(c.CSC, "Relative of a PEP") {
		v["POLITICALEXPERSON"] = "Y"
	}

	// Address: permanent and correspondence the same, in India.
	addr, pin := "", ""
	if a, ok := src.apps[c.UCC]; ok {
		addr, pin = a.Address, a.PIN
	}
	if addr == "" {
		if p, ok := src.mf[c.UCC]; ok && strings.TrimSpace(p.Address) != "" {
			addr, pin = p.Address, p.Pincode
		}
	}
	addr = strings.Join(strings.Fields(addr), " ")
	if addr == "" {
		bad("There is no address on record (from the online application or the MF profile): add the client's address")
	}
	state := strings.ToUpper(strings.Join(strings.Fields(c.State), " "))
	if st, ok := bseStates[state]; ok {
		state = st
	} else if state == "" {
		bad("The client's state is missing")
	} else {
		bad("%q is not an Indian state or union territory as BSE's state master names them", c.State)
	}
	city := strings.ToUpper(strings.TrimSpace(c.City))
	if city == "" {
		bad("The client's city is missing")
	}
	if !reBSEPin.MatchString(pin) {
		bad("The PIN code is missing or not 6 digits")
	}
	v["ADDRESS1"], v["PERMNEQUALCORP"], v["ADDRESS2"] = addr, "Y", addr
	v["COUNTRY"], v["STATE"], v["CITY"], v["PINCODE"] = "INDIA", state, city, pin
	v["PERCOUNTRY"], v["PERSTATE"], v["PERCITY"], v["PERPINCODE"] = "INDIA", state, city, pin

	// Contact: SMS and email, by mobile.
	v["TYPEOFSERVICE"], v["CONTACTDETAILS"] = "3", "2"
	v["EMAIL"], v["MOBILENUMBER"] = c.Email, c.Mobile
	if !reEmail.MatchString(c.Email) {
		bad("The email address is missing or not valid")
	}
	if !regexp.MustCompile(`^[0-9]{10}$`).MatchString(c.Mobile) {
		bad("BSE needs a 10-digit mobile number for a client in India")
	}

	// Depository: one account, primary.
	switch dep := strings.ToUpper(c.Demat.Depository); {
	case c.Demat.ID == "":
		bad("There is no demat account on record")
	case dep == "CDSL" && reBSECDSLBO.MatchString(c.Demat.ID):
		v["DEPOSITORYNAME1"], v["DEPOSITORYPARTICIPANT1"], v["BENEFICIALOWNACNTNO1"] = "CDSL", c.Demat.ID[:8], c.Demat.ID
	case dep == "NSDL" && reBSENSDLBO.MatchString(c.Demat.ID):
		v["DEPOSITORYNAME1"], v["DEMANTID1"], v["DEPOSITORYPARTICIPANT1"], v["BENEFICIALOWNACNTNO1"] = "NSDL", c.Demat.ID[:8], c.Demat.ID[:8], c.Demat.ID[8:]
	case dep == "CDSL":
		bad("The CDSL demat account must be 16 digits starting with 1")
	case dep == "NSDL":
		bad("The NSDL demat account must be IN, 6 digits (the DP ID) and 8 digits (the client ID)")
	default:
		bad("The depository must be CDSL or NSDL")
	}
	if v["DEPOSITORYNAME1"] != "" {
		v["PRIMARYORSECONDARYDP1"] = "P"
	}

	// Bank: one account, primary. BSE refuses commas in the bank's name.
	bank := strings.Join(strings.Fields(strings.ReplaceAll(c.Bank.Bank, ",", " ")), " ")
	acct := strings.TrimSpace(c.Bank.Account)
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, acct)
	switch {
	case bank == "" || !reBSEBankName.MatchString(bank):
		bad("The bank's name is missing")
	case acct == "" || len(acct) > 19 || strings.Trim(acct, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789") != "":
		bad("The bank account number is missing, longer than 19 characters or has characters other than letters and digits")
	case strings.Trim(digits, "0") == "":
		bad("The bank account number has no digits other than zeros")
	case !reIFSC.MatchString(c.Bank.IFSC):
		bad("The IFSC is not 4 letters, 0 and 6 letters or digits")
	}
	v["BANKNAME1"], v["ACCOUNTNO1"], v["BANKBRANCHIFSCCODE1"], v["PRIMARYORSECONDARYBANK1"] = bank, acct, c.Bank.IFSC, "P"

	// Dates.
	if c.KYCDate != "" {
		if c.KYCDate > today {
			bad("The account opening date is in the future")
		} else {
			v["CLIENTAGGREMENTDATE"] = bseDate(c.KYCDate)
		}
	}

	// Income (the networth is not held, so income only). The income date is
	// when it was last declared: at account opening, or the latest re-KYC.
	v["PROVIDEDETAILS"] = "1"
	v["INCOME"] = bseIncome(c.Income, individual)
	if v["INCOME"] == "" {
		bad("The income band %q is not one BSE takes", c.Income)
	}
	inc := c.KYCDate
	if c.ReKYCOn > inc {
		inc = c.ReKYCOn
	}
	switch {
	case !validDate(inc):
		bad("There is no date for the income declaration (account opening or re-KYC)")
	case inc > today:
		bad("The income declaration date is in the future")
	case inc < now.In(ist).AddDate(-1, 0, 0).Format("2006-01-02"):
		bad("The income was declared on %s, more than a year ago: BSE takes income details at most a year old; record a fresh income declaration (re-KYC)", fmtDay(inc))
	default:
		v["INCOMEDATE"] = bseDate(inc)
	}

	// Name: first, middle and last for individuals; the full name always.
	name := strings.ToUpper(strings.Join(strings.Fields(c.Name), " "))
	if individual {
		w := strings.Fields(name)
		switch len(w) {
		case 0:
		case 1:
			v["LASTNAME"] = w[0]
		case 2:
			v["FIRSTNAME"], v["LASTNAME"] = w[0], w[1]
		default:
			v["FIRSTNAME"], v["MIDDLENAME"], v["LASTNAME"] = w[0], strings.Join(w[1:len(w)-1], " "), w[len(w)-1]
		}
		if len(v["FIRSTNAME"])+len(v["MIDDLENAME"])+len(v["LASTNAME"]) > 85 {
			bad("The name is longer than BSE's 85 characters")
		}
		switch {
		case c.DOB == "":
			bad("The date of birth is missing")
		case !validDate(c.DOB) || c.DOB > today:
			bad("The date of birth is not a past date")
		default:
			v["DATEOFBIRTH"] = bseDate(c.DOB)
		}
	}
	if name == "" {
		bad("The name is missing")
	}
	v["CLIENTNAME"] = name
	v["WHETHERCORPORATE"], v["NUMBEROFDIRECTORS"] = "N", "0"

	// Segments: Y for each segment open for trading. A suspended, dormant or
	// closed segment is sent N. The account's own state is "Is Active": N
	// when it is inactive under the exchanges' inactivity rules or has no
	// open segment, C when every segment is closed.
	segKey := map[string]string{"CM": "CASH", "FO": "EQUITY_DERIVATIVE", "CD": "CURRENCY", "COM": "COMMDERIVATIVES"}
	for _, k := range []string{"CASH", "EQUITY_DERIVATIVE", "SLB", "CURRENCY", "DEBT", "COMMDERIVATIVES", "EGR"} {
		v[k] = "N"
	}
	open, closed := 0, 0
	for _, s := range c.Segments {
		if c.SegmentOpen(s) {
			v[segKey[s]] = "Y"
			open++
		} else if c.SegmentStatus(s) == SegClosed {
			closed++
		}
	}
	v["ISACTIVE"] = "Y"
	switch {
	case len(c.Segments) > 0 && closed == len(c.Segments):
		v["ISACTIVE"] = "C"
	case c.Inactive || open == 0:
		v["ISACTIVE"] = "N"
	}
	if open == 0 {
		// BSE needs one segment chosen: an account not active keeps its
		// registered segments, and Is Active says it may not trade.
		for _, s := range c.Segments {
			if c.SegmentStatus(s) != SegClosed || v["ISACTIVE"] == "C" {
				v[segKey[s]] = "Y"
			}
		}
		if len(c.Segments) == 0 {
			bad("Choose at least one segment")
		}
	}

	// Power of attorney is no longer taken (SEBI replaced it with DDPI).
	v["ISPOA"], v["POAFORFUND"], v["POAFORSECURITY"] = "N", "N", "N"

	// Nomination (optional for individuals; sent when the choice is known).
	switch {
	case len(c.Nominees) > 0 || c.Nominee == "Opted in":
		v["OPTEDFORNOMINATION"] = "Y"
	case c.Nominee == "Opted out":
		v["OPTEDFORNOMINATION"] = "N"
	}

	// UPI block for the cash market: applies to individuals, HNI, PMS
	// individuals and HUFs in the cash segment; NA otherwise. D only after Y.
	v["OPTED_FOR_UPI"] = "NA"
	if (v["CATEGORY"] == "I" || v["CATEGORY"] == "HNI" || v["CATEGORY"] == "PMSI" || v["CATEGORY"] == "HUF") && v["CASH"] == "Y" {
		switch {
		case c.UPIBlock > money.Amount(0):
			v["OPTED_FOR_UPI"] = "Y"
		case last["upi"] == "Y" || last["upi"] == "D":
			v["OPTED_FOR_UPI"] = "D"
		default:
			v["OPTED_FOR_UPI"] = "N"
		}
	}

	// What changed since BSE's last record, which BSE wants a reason for.
	sum := map[string]string{"status": v["STATUS"], "category": v["CATEGORY"], "isactive": v["ISACTIVE"], "upi": v["OPTED_FOR_UPI"],
		"segments": v["CASH"] + v["EQUITY_DERIVATIVE"] + v["CURRENCY"] + v["COMMDERIVATIVES"]}
	if action == "Modify" && last != nil {
		var why []string
		if last["category"] != sum["category"] || last["status"] != sum["status"] {
			why = append(why, "Category "+sum["category"])
		}
		if last["isactive"] != sum["isactive"] {
			switch {
			case sum["isactive"] == "C":
				why = append(why, "Account closed")
			case c.Inactive:
				why = append(why, "Inactive under exchange inactivity rules since "+bseDate(c.InactiveSince))
			case sum["isactive"] == "Y" && c.ReactivatedOn != "":
				why = append(why, "Reactivated on "+bseDate(c.ReactivatedOn))
			default:
				why = append(why, "Active "+sum["isactive"])
			}
		}
		if last["segments"] != sum["segments"] {
			var s []string
			for _, h := range segmentChangesOn(c, today) {
				s = append(s, h.Segment+" "+h.To)
			}
			if len(s) == 0 {
				s = append(s, "segments changed")
			}
			why = append(why, "Segment status "+strings.Join(s, " "))
		}
		r := strings.Join(why, ", ")
		if len(r) > 100 {
			r = r[:100]
		}
		v["UPDATEREASON"] = r
	}

	// Lengths, characters and plain ASCII (BSE hashes the request as ASCII).
	for _, k := range bseUCCFields {
		x := v[k]
		if n := bseUCCMaxLen[k]; n > 0 && len(x) > n {
			bad("%s is longer than BSE's %d characters", bseUCCLabel(k), n)
		}
		for _, r := range x {
			if r > 126 || r < 32 {
				bad("%s has a character BSE does not take (%q): use plain English letters", bseUCCLabel(k), r)
				break
			}
		}
	}
	for _, k := range bseUCCTextFields {
		if !reBSEText.MatchString(v[k]) {
			bad("%s has a character BSE does not take: only letters, digits, spaces and @ # $ & * ( ) - + = { } [ ] / \\ : \" , .", bseUCCLabel(k))
		}
	}
	return bseUCCRecord{Values: v, Problems: dedupe(probs), Summary: sum}
}

func bseUCCLabel(k string) string {
	if l, ok := map[string]string{"ADDRESS1": "The address", "ADDRESS2": "The address", "FIRSTNAME": "The first name", "MIDDLENAME": "The middle name", "LASTNAME": "The last name",
		"CLIENTNAME": "The name", "UPDATEREASON": "The reason", "CITY": "The city", "STATE": "The state", "EMAIL": "The email address", "BANKNAME1": "The bank's name",
		"CLIENTCODE": "The client code", "PLACEOFREGISTRATION": "The place of registration"}[k]; ok {
		return l
	}
	return k
}

// JSON writes the record as BSE's request: compact, no HTML escaping, the
// sample's field order, empty values as null, DIRECTORDETAILS null.
func (r bseUCCRecord) JSON() []byte {
	var b bytes.Buffer
	b.WriteString(`{"CLIENTDETAILS_V5":[{`)
	for i, k := range bseUCCJSONOrder {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"` + k + `":`)
		if x := r.Values[k]; x == "" {
			b.WriteString("null")
		} else {
			b.Write(jsonString(x))
		}
	}
	b.WriteString(`,"DIRECTORDETAILS":null}]}`)
	return b.Bytes()
}

func jsonString(s string) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// Hash identifies the record's content: the transaction code and reason
// are left out, so an Add BSE accepted and the same data as a Modify are
// the same record.
func (r bseUCCRecord) Hash() string {
	v := map[string]string{}
	for k, x := range r.Values {
		v[k] = x
	}
	v["TRANSACTIONCODE"], v["UPDATEREASON"] = "", ""
	b := bseUCCRecord{Values: v}.JSON()
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Masked is the record for audit and preview: Aadhaar (never filled by
// Ledgerline, masked if it ever is) and bank account numbers show only
// their last four digits.
func (r bseUCCRecord) Masked() bseUCCRecord {
	v := map[string]string{}
	for k, x := range r.Values {
		v[k] = x
	}
	for _, k := range []string{"AADHARCARDNO", "ACCOUNTNO1", "ACCOUNTNO2", "ACCOUNTNO3", "ACCOUNTNO4", "ACCOUNTNO5"} {
		if v[k] != "" {
			v[k] = MaskAccount(v[k])
		}
	}
	return bseUCCRecord{Values: v, Problems: r.Problems, Summary: r.Summary}
}

// bseUCCBatchRows writes records in the batch upload layout (the first,
// general row per client; no director rows, since Ledgerline holds no
// directors and non-individuals are not written).
func bseUCCBatchRows(s *xfile.Spec, recs []bseUCCRecord) ([]xfile.Row, error) {
	if len(s.Columns) != len(bseUCCFields) {
		return nil, fmt.Errorf("the BSE UCC layout has %d fields, not %d", len(s.Columns), len(bseUCCFields))
	}
	var rows []xfile.Row
	for _, r := range recs {
		m := map[string]string{}
		for i, k := range bseUCCFields {
			m[s.Columns[i].Key] = r.Values[k]
		}
		rows = append(rows, xfile.Row{Values: m})
	}
	return rows, nil
}
