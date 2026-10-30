package ops

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- CDSL BO upload files: what goes in them ----
//
// A BO setup record is built from the client master, the online
// application, the client's UCC details (address, permanent address,
// gender, father's name, authorised signatories), its CDSL details below
// (joint holders, mode of operation, POA and DDPI links, CDSL's codes for
// the client) and the DP's BO defaults. A BO modify record carries only
// what an approved change request changed, with '@' where a field becomes
// blank.

const cCDSLBODetail = "cdslbodetail"

// CDSLBODetail is what CDSL holds about a demat account that the client
// master does not: joint holders and CDSL's own codes.
type CDSLBODetail struct {
	UCC string `json:"ucc"`
	// Holders are the second and third holders, in order.
	Holders []CDSLJointHolder `json:"holders,omitempty"`
	// ModeOfOperation: 0 sole holder, 1 jointly, 2 anyone or survivor.
	ModeOfOperation string `json:"mode_of_operation,omitempty"`
	// CommPref: 1 first holder, 2 all holders.
	CommPref string `json:"comm_pref,omitempty"`
	// PANVerified: the first holder's PAN is verified and seeded with
	// Aadhaar, as the DP confirms (PAN verification flag 5).
	PANVerified bool `json:"pan_verified,omitempty"`
	// Codes are CDSL's code values for this client, by file field key
	// (occupation, geo, education, language, nationality, staff,
	// staff_code, product, bo_category, sub_status, stmt_cycle, city_seq,
	// state_code, bank_code, family, cas_mode, annual_report…); they take
	// precedence over the BO defaults.
	Codes map[string]string `json:"codes,omitempty"`
	// Guardian of a minor account holder (purpose code 07).
	Guardian *CDSLPerson `json:"guardian,omitempty"`
	// The client's DDPI with the DP (line 06): its registration number,
	// setup date and purposes (S settlement, P margin pledge, M mutual
	// fund, T tender offer). The master ID is the firm's DDPI POA ID.
	DDPIRegNo    string   `json:"ddpi_reg_no,omitempty"`
	DDPIDate     string   `json:"ddpi_date,omitempty"`
	DDPIPurposes []string `json:"ddpi_purposes,omitempty"`
	// Other POA links (line 06).
	POALinks []CDSLPOALink `json:"poa_links,omitempty"`
	// CM accounts mapped to a CM / PMS / DDPI POA master (purpose 21).
	CMMappings []CDSLCMMap `json:"cm_mappings,omitempty"`
	// BO-UCC links (purpose 22); when empty they are made from the
	// client's segments and the firm's member codes.
	UCCLinks []CDSLUCCLink `json:"ucc_links,omitempty"`
	// PMS manager (purpose 23), for a PMS client.
	PMS       *CDSLPerson `json:"pms,omitempty"`
	UpdatedBy string      `json:"updated_by,omitempty"`
	UpdatedAt string      `json:"updated_at,omitempty"`
}

// CDSLJointHolder is a second or third holder.
type CDSLJointHolder struct {
	Name        string `json:"name"`
	Father      string `json:"father,omitempty"`
	PAN         string `json:"pan"`
	Mobile      string `json:"mobile,omitempty"`
	Email       string `json:"email,omitempty"`
	PANVerified bool   `json:"pan_verified,omitempty"`
	PANExempt   string `json:"pan_exempt,omitempty"` // CDSL's exemption code, when PAN-exempt
	DiedOn      string `json:"died_on,omitempty"`    // set when the holder's name is deleted
}

// CDSLPerson is a guardian or PMS manager.
type CDSLPerson struct {
	Name     string `json:"name"`
	Relation string `json:"relation,omitempty"`
	Address  string `json:"address,omitempty"`
	City     string `json:"city,omitempty"`
	State    string `json:"state,omitempty"`
	PIN      string `json:"pin,omitempty"`
	PAN      string `json:"pan,omitempty"`
	Mobile   string `json:"mobile,omitempty"`
	Email    string `json:"email,omitempty"`
	DOB      string `json:"dob,omitempty"`
	SEBIReg  string `json:"sebi_reg,omitempty"` // PMS manager's SEBI registration number
}

// CDSLPOALink is a POA linked to the account.
type CDSLPOALink struct {
	RegNo    string `json:"reg_no"`    // POA registration number (16)
	MasterID string `json:"master_id"` // POA master ID (16 digits)
	Purpose  string `json:"purpose"`   // 0001 first, 0002 second, 0003 third, 0004 all holders
	Flag     string `json:"flag"`      // B, G, S, P, M, T
	Setup    string `json:"setup"`     // YYYY-MM-DD
	From     string `json:"from"`
	To       string `json:"to,omitempty"`
	Operate  string `json:"operate,omitempty"` // Y/N
}

// CDSLCMMap maps a CM account to a POA master (purpose 21).
type CDSLCMMap struct {
	Flag      string `json:"flag"` // 1 map, 2 unmap (modify), 3 map all CM accounts
	ExID      string `json:"ex_id,omitempty"`
	CMID      string `json:"cm_id,omitempty"`
	CMAccount string `json:"cm_account,omitempty"`
}

// CDSLUCCLink is a BO-UCC link (purpose 22).
type CDSLUCCLink struct {
	Flag     string `json:"flag"`    // A add, D delete (modify)
	Consent  string `json:"consent"` // 1 this BO only, 2 every account with the PAN
	Exchange string `json:"exchange"`
	UCC      string `json:"ucc"`
	Segment  string `json:"segment"`
	CMID     string `json:"cm_id"`
	TMCode   string `json:"tm_code"`
}

// CDSLBODetailFor is a client's CDSL details.
func (o *Ops) CDSLBODetailFor(ctx context.Context, tenant, ucc string) CDSLBODetail {
	ucc = strings.ToUpper(ucc)
	d, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCDSLBODetail, ucc, CDSLBODetail{UCC: ucc})
	if d.Codes == nil {
		d.Codes = map[string]string{}
	}
	return d
}

// CDSLBODetailView is a client's CDSL details, for staff.
func (o *Ops) CDSLBODetailView(ctx context.Context, tenant, actor, ucc string) (CDSLBODetail, error) {
	if err := o.require(ctx, tenant, actor, "Viewing CDSL requests", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return CDSLBODetail{}, err
	}
	if _, err := o.Client(ctx, tenant, ucc); err != nil {
		return CDSLBODetail{}, ErrNotFound
	}
	return o.CDSLBODetailFor(ctx, tenant, ucc), nil
}

var reBOCode = regexp.MustCompile(`^[0-9A-Za-z]*$`)

// SaveCDSLBODetail checks and stores a client's CDSL details.
func (o *Ops) SaveCDSLBODetail(ctx context.Context, tenant, actor, ucc string, d CDSLBODetail) (CDSLBODetail, error) {
	if err := o.require(ctx, tenant, actor, "Changing CDSL account details", RoleMaker, RoleChecker); err != nil {
		return d, err
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return d, invalid("client %s not found", ucc)
	}
	d.UCC = c.UCC
	var probs []string
	if len(d.Holders) > 2 {
		probs = append(probs, "a demat account has at most three holders: give the second and third only")
	}
	for i := range d.Holders {
		h := &d.Holders[i]
		h.Name, h.PAN, h.Mobile, h.Email = strings.Join(strings.Fields(h.Name), " "), strings.ToUpper(strings.TrimSpace(h.PAN)), strings.TrimSpace(h.Mobile), strings.TrimSpace(strings.ToLower(h.Email))
		who := []string{"Second", "Third"}[min(i, 1)] + " holder"
		if len(h.Name) < 3 {
			probs = append(probs, who+": enter the name")
		}
		if h.PANExempt == "" && !rePAN.MatchString(h.PAN) {
			probs = append(probs, who+": the PAN is not in the PAN format")
		}
		if h.Mobile != "" && !reMobile.MatchString(h.Mobile) {
			probs = append(probs, who+": enter a 10-digit Indian mobile number")
		}
		if h.Email != "" && !reEmail.MatchString(h.Email) {
			probs = append(probs, who+": the email is not valid")
		}
	}
	if !contains([]string{"", "0", "1", "2"}, d.ModeOfOperation) {
		probs = append(probs, "mode of operation is 0 (sole), 1 (jointly) or 2 (anyone or survivor)")
	}
	if !contains([]string{"", "1", "2"}, d.CommPref) {
		probs = append(probs, "communication preference is 1 (first holder) or 2 (all holders)")
	}
	for k, v := range d.Codes {
		if !reBOCode.MatchString(v) || len(v) > 12 || len(k) > 30 {
			probs = append(probs, fmt.Sprintf("code %s: %q is not a CDSL code value", k, v))
		}
	}
	if d.DDPIRegNo != "" && len(d.DDPIRegNo) > 16 {
		probs = append(probs, "the DDPI registration number is at most 16 characters")
	}
	if len(probs) > 0 {
		return d, invalid("%s", strings.Join(probs, "; "))
	}
	d.UpdatedBy, d.UpdatedAt = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cCDSLBODetail, d.UCC, d, actor); err != nil {
		return d, err
	}
	o.log(ctx, tenant, actor, "cdsl.bo_detail", d.UCC, map[string]any{"holders": len(d.Holders)})
	return d, nil
}

// boStateCodes are ISO 3166-2 codes of Indian states and union territories.
var boStateCodes = map[string]string{
	"ANDHRA PRADESH": "IN-AP", "ARUNACHAL PRADESH": "IN-AR", "ASSAM": "IN-AS", "BIHAR": "IN-BR", "CHHATTISGARH": "IN-CG", "GOA": "IN-GA", "GUJARAT": "IN-GJ",
	"HARYANA": "IN-HR", "HIMACHAL PRADESH": "IN-HP", "JHARKHAND": "IN-JH", "KARNATAKA": "IN-KA", "KERALA": "IN-KL", "MADHYA PRADESH": "IN-MP", "MAHARASHTRA": "IN-MH",
	"MANIPUR": "IN-MN", "MEGHALAYA": "IN-ML", "MIZORAM": "IN-MZ", "NAGALAND": "IN-NL", "ODISHA": "IN-OD", "ORISSA": "IN-OD", "PUNJAB": "IN-PB", "RAJASTHAN": "IN-RJ",
	"SIKKIM": "IN-SK", "TAMIL NADU": "IN-TN", "TELANGANA": "IN-TS", "TRIPURA": "IN-TR", "UTTAR PRADESH": "IN-UP", "UTTARAKHAND": "IN-UK", "WEST BENGAL": "IN-WB",
	"ANDAMAN AND NICOBAR ISLANDS": "IN-AN", "CHANDIGARH": "IN-CH", "DADRA AND NAGAR HAVELI AND DAMAN AND DIU": "IN-DH", "DELHI": "IN-DL", "NEW DELHI": "IN-DL",
	"JAMMU AND KASHMIR": "IN-JK", "LADAKH": "IN-LA", "LAKSHADWEEP": "IN-LD", "PUDUCHERRY": "IN-PY", "PONDICHERRY": "IN-PY",
}

func boStateCode(state string) string {
	s := strings.ToUpper(strings.Join(strings.Fields(strings.ReplaceAll(state, "&", " AND ")), " "))
	if strings.HasPrefix(s, "IN-") {
		return s
	}
	return boStateCodes[s]
}

// boIncome is CDSL's annual income code for the income band.
func boIncome(band string, individual bool) string {
	switch strings.ReplaceAll(strings.ToUpper(band), " ", "") {
	case "<1L", "BELOW1L":
		return "1"
	case "1-5L":
		return "6"
	case "5-10L":
		return "7"
	case "10-25L":
		return "8"
	case "25L-1CR", ">25L":
		if individual {
			return "9"
		}
		return "10"
	case ">1CR":
		if individual {
			return "9"
		}
		return "11"
	}
	return ""
}

// boCurrency is the numeric currency code the file takes (ISO 4217).
func boCurrency(s string) string {
	if strings.EqualFold(strings.TrimSpace(s), "INR") {
		return "356"
	}
	return s
}

func boDateTime(t time.Time) string { return t.In(ist).Format("02012006150405") }

// boAddr fills a block's address fields.
func boAddr(v map[string]string, addr, city, state, pin, citySeq string) {
	l := boAddress(addr)
	v["addr1"], v["addr2"], v["addr3"] = strings.ToUpper(l[0]), strings.ToUpper(l[1]), strings.ToUpper(l[2])
	v["country"], v["zip"], v["city"] = "IN", pin, strings.ToUpper(city)
	v["state_code"], v["city_seq"] = boStateCode(state), firstOf(citySeq, "00")
}

// boSetupIn is what a BO setup record is made from.
type boSetupIn struct {
	C        Client
	App      *Application
	U        UCCDetails
	D        CDSLBODetail
	Def      map[string]string
	F        map[string]string // the request's own details
	BO       BOAccount
	Serial   int
	Received time.Time
	Members  map[string]string // exchange → the firm's trading member code
	DDPIID   string            // the firm's DDPI POA master ID
	Sig      bool              // a signature image goes with it
	// DeleteFirst: the first holder died on this date (YYYY-MM-DD); the
	// record re-opens the account with the second holder first (§ name
	// deletion of CDSL/OPS/SYSTM/2023/404).
	DeleteFirst string
}

// boSetupRecord builds a BO setup record and says what is missing.
func boSetupRecord(in boSetupIn) ([]boLine, []string, []string) {
	c, d, u := in.C, in.D, in.U
	code := func(k string) string { return strings.TrimSpace(firstOf(firstOf(in.F[k], d.Codes[k]), in.Def[k])) }
	var probs, warns []string
	var rec []boLine
	individual := c.Type == "Individual" || c.Type == "NRI" || c.Type == ""

	// Holders in order; on a first holder's death the second becomes first.
	type holder struct {
		name, father, pan, mobile, email, exempt string
		verified                                 bool
	}
	hs := []holder{{c.Name, firstOf(in.F["father"], firstOf(u.Guardian, appFather(in.App))), c.PAN, c.Mobile, c.Email, "", d.PANVerified || appPANPassed(in.App)}}
	for _, h := range d.Holders {
		if h.DiedOn == "" {
			hs = append(hs, holder{h.Name, h.Father, h.PAN, h.Mobile, h.Email, h.PANExempt, h.PANVerified})
		}
	}
	if in.DeleteFirst != "" {
		if len(hs) < 2 {
			return nil, []string{"the first holder's name can be deleted only from a joint account"}, nil
		}
		hs = hs[1:]
	}
	if len(hs) > 3 {
		probs = append(probs, "a demat account has at most three holders")
		hs = hs[:3]
	}

	bo := ""
	if in.DeleteFirst != "" {
		bo = in.BO.BOID
	}
	rec = append(rec, boLine{Layout: "s01", No: 1, Who: "Record", V: map[string]string{"serial": fmt.Sprint(in.Serial), "bo_id": bo, "request_date": boDateTime(in.Received)}})

	// Line 02: the first holder, with the correspondence address.
	addr, city, state, pin := firstOf(in.F["address"], strings.TrimSpace(u.Address1+" "+u.Address2+" "+u.Address3)), firstOf(in.F["city"], u.City), firstOf(in.F["state"], u.State), firstOf(in.F["pin"], u.PIN)
	if in.App != nil {
		addr, city, state, pin = firstOf(addr, in.App.Address), firstOf(city, in.App.City), firstOf(state, in.App.State), firstOf(pin, in.App.PIN)
	}
	city, state = firstOf(city, c.City), firstOf(state, c.State)
	if strings.TrimSpace(addr) == "" {
		probs = append(probs, "Holder: enter the address (house, street, area)")
	}
	if !rePIN.MatchString(pin) {
		probs = append(probs, "Holder: enter the 6-digit PIN code")
	}
	if boStateCode(state) == "" {
		probs = append(probs, fmt.Sprintf("Holder: the state %q is not an Indian state or union territory CDSL knows (ISO 3166-2 code)", state))
	}
	h1 := hs[0]
	first, middle, last := boName(h1.name)
	l2 := map[string]string{"product": code("product"), "name": first, "middle": middle, "last": last, "father": strings.ToUpper(h1.father),
		"isd": firstOf(in.Def["primary_mobil_isd_code"], "91"), "mobile": h1.mobile, "email": h1.email, "pan": h1.pan, "pan_exempt": h1.exempt}
	if code("product") == "" {
		l2["product"] = in.Def["product_number"]
	}
	boAddr(l2, addr, city, state, pin, code("city_seq"))
	verify := ""
	for i, h := range hs {
		switch {
		case h.verified:
			verify += "5"
		case h.exempt != "":
			verify += "4"
		default:
			verify += "0"
			probs = append(probs, fmt.Sprintf("Holder %d: confirm the PAN is verified and linked with Aadhaar (CDSL's PAN verification flag 5) on the client's CDSL details", i+1))
		}
	}
	l2["pan_verify"] = verify + strings.Repeat("0", 4-len(verify))
	l2["sig_flag"] = map[bool]string{true: "Y", false: "N"}[in.Sig]
	l2["open_source"] = map[bool]string{true: "1", false: "2"}[in.App != nil]
	if in.DeleteFirst != "" {
		l2["del_flag"], l2["del_reason"], l2["death_date"] = "100", "06", boDate(in.DeleteFirst)
	}
	rec = append(rec, boLine{Layout: "s02", No: 2, Who: "Holder", V: l2})

	// Lines 03, 04: joint holders.
	for i, h := range hs[1:] {
		f, m, l := boName(h.name)
		v := map[string]string{"name": f, "middle": m, "last": l, "father": strings.ToUpper(h.father), "pan": h.pan, "email": h.email}
		if h.mobile != "" {
			v["isd"], v["mobile"] = firstOf(in.Def["primary_mobil_isd_code"], "91"), h.mobile
		}
		rec = append(rec, boLine{Layout: "s03", No: 3 + i, Who: fmt.Sprintf("Holder %d", i+2), V: v})
	}

	// Line 05: product details.
	gender := firstOf(in.F["gender"], u.Gender)
	if gender == "" && in.App != nil {
		gender = in.App.Gender
	}
	mode, comm := firstOf(d.ModeOfOperation, "0"), firstOf(d.CommPref, "1")
	if len(hs) > 1 {
		if d.ModeOfOperation == "" || d.ModeOfOperation == "0" {
			probs = append(probs, "Account: a joint account needs its mode of operation (1 jointly, 2 anyone or survivor) on the client's CDSL details")
		}
		if d.CommPref == "" {
			probs = append(probs, "Account: a joint account needs its communication preference (1 first holder, 2 all holders)")
		}
		if comm == "2" {
			for i, h := range hs[1:] {
				if h.mobile == "" || h.email == "" {
					probs = append(probs, fmt.Sprintf("Holder %d: communication to all holders needs the holder's mobile and email", i+2))
				}
			}
		}
	}
	income := firstOf(code("income"), boIncome(c.Income, individual))
	if income == "" {
		probs = append(probs, fmt.Sprintf("Account: the income band %q has no CDSL annual income code", c.Income))
	}
	nomOut := ""
	if individual {
		nomOut = map[bool]string{true: "Y", false: "N"}[c.Nominee != "Opted in" || len(c.Nominees) == 0]
	}
	bsda := map[bool]string{true: "Y", false: "N"}[in.BO.BSDA]
	l5 := map[string]string{"internal_ref": c.UCC, "dob": boDate(c.DOB), "sex": map[string]string{"Male": "M", "Female": "F", "M": "M", "F": "F"}[gender],
		"income": income, "nom_opt_out": nomOut, "bonafide": map[bool]string{true: "A", false: "P"}[in.App != nil], "bsda": bsda, "mode_op": mode, "comm_pref": comm,
		"ifsc": c.Bank.IFSC, "bank_acct": c.Bank.Account, "mental": "N"}
	for k, def := range map[string]string{"occupation": "occupation", "geo": "geo", "education": "education", "nationality": "nationality_code", "language": "language",
		"staff": "staff", "staff_code": "staff_code", "family": "family", "email_stmt": "email_statement_flag", "cas_mode": "cas_mode", "annual_report": "annual_report",
		"bo_category": "bo_category", "settlement_flag": "bo_settlement_planning_flag", "sub_status": "bo_sub_status", "stmt_cycle": "bo_statement_cycle_code",
		"bank_acct_type": "divnd_bank_acct_type", "bank_ccy": "divnd_bank_ccy", "bank_code": "divnd_bank_code", "custodian_email": "custodian_email"} {
		if v := firstOf(firstOf(in.F[def], d.Codes[k]), in.Def[def]); v != "" {
			l5[k] = v
		}
	}
	l5["bank_ccy"] = boCurrency(l5["bank_ccy"])
	if l5["bank_code"] == "" {
		probs = append(probs, "Account: enter the bank's 9-digit MICR code (CDSL's dividend bank code)")
	}
	if l5["email_stmt"] == "Y" && h1.email == "" {
		probs = append(probs, "Account: statements by email need the first holder's email")
	}
	rec = append(rec, boLine{Layout: "s05", No: 5, Who: "Account", V: l5})

	// Line 06: DDPI and other POA links.
	if in.BO.DDPI {
		switch {
		case d.DDPIRegNo == "":
			probs = append(probs, "DDPI: the client has a DDPI; enter its registration number and date on the client's CDSL details")
		case in.DDPIID == "":
			probs = append(probs, "DDPI: enter the firm's DDPI POA ID in the CDSL settings")
		default:
			ps := d.DDPIPurposes
			if len(ps) == 0 {
				ps = []string{"S", "P"}
			}
			for _, p := range ps {
				rec = append(rec, boLine{Layout: "s06", No: 6, Who: "DDPI", V: map[string]string{"poa_id": d.DDPIRegNo, "setup_date": boDate(d.DDPIDate), "gpa_flag": p,
					"eff_from": boDate(d.DDPIDate), "master_id": in.DDPIID, "purpose": "0004", "poa_operate": "Y"}})
			}
		}
	}
	for i, p := range d.POALinks {
		rec = append(rec, boLine{Layout: "s06", No: 6, Who: fmt.Sprintf("POA %d", i+1), V: map[string]string{"poa_id": p.RegNo, "setup_date": boDate(p.Setup), "gpa_flag": p.Flag,
			"eff_from": boDate(p.From), "eff_to": boDate(p.To), "master_id": p.MasterID, "purpose": firstOf(p.Purpose, "0004"), "poa_operate": p.Operate}})
	}

	// Line 07: permanent address (purpose 12).
	paddr, pcity, pstate, ppin := strings.TrimSpace(u.PermAddress1+" "+u.PermAddress2+" "+u.PermAddress3), u.PermCity, u.PermState, u.PermPIN
	if paddr == "" {
		paddr, pcity, pstate, ppin = addr, city, state, pin
	}
	l7 := map[string]string{"purpose": "12", "name": first, "middle": middle, "last": last, "father": strings.ToUpper(h1.father)}
	boAddr(l7, paddr, pcity, pstate, ppin, code("city_seq"))
	rec = append(rec, boLine{Layout: "s07", No: 7, Who: "Permanent address", V: l7})

	// Lines 08 onwards.
	n := 8
	add := func(v map[string]string, who string) {
		rec = append(rec, boLine{Layout: "s08", No: n, Who: who, V: v})
		n++
	}
	person := func(purpose string, p CDSLPerson, who string) map[string]string {
		f, m, l := boName(p.Name)
		v := map[string]string{"purpose": purpose, "name": f, "middle": m, "last": l, "dob": boDate(p.DOB), "pan": p.PAN, "email": p.Email}
		if p.Mobile != "" {
			v["isd"], v["mobile"] = "91", p.Mobile
		}
		if p.Address == "" || !rePIN.MatchString(p.PIN) || p.City == "" {
			probs = append(probs, who+": give the address, city and 6-digit PIN code")
		}
		boAddr(v, p.Address, p.City, p.State, p.PIN, "00")
		return v
	}
	if individual {
		if minorOn(c.DOB, in.Received) {
			if d.Guardian == nil {
				probs = append(probs, "Guardian: a minor's account needs the guardian's details on the client's CDSL details")
			} else {
				v := person("07", *d.Guardian, "Guardian")
				v["relation"] = boRelation(d.Guardian.Relation)
				add(v, "Guardian")
			}
			if len(hs) > 1 {
				probs = append(probs, "Account: a minor's account has only the first holder")
			}
		}
		total := 0
		for i, x := range c.Nominees {
			who := fmt.Sprintf("Nominee %d", i+1)
			np := firstOf(in.F[fmt.Sprintf("nominee%d_pin", i+1)], rePINIn.FindString(x.Address))
			nc := firstOf(in.F[fmt.Sprintf("nominee%d_city", i+1)], boCityBefore(x.Address, np))
			ns := firstOf(in.F[fmt.Sprintf("nominee%d_state", i+1)], state)
			p := CDSLPerson{Name: x.Name, Address: strings.TrimSpace(strings.Replace(x.Address, np, "", 1)), City: nc, State: ns, PIN: np, Mobile: x.Mobile, Email: x.Email, DOB: x.DOB}
			if x.IDType == "PAN" {
				p.PAN = x.ID
			}
			v := person("06", p, who)
			share := boShare(x.Share)
			var s int
			fmt.Sscanf(share, "%d", &s)
			total += s
			v["nominee_serial"], v["relation"], v["share"], v["residual"] = fmt.Sprintf("%02d", i+1), boRelation(x.Relation), share, map[bool]string{true: "Y", false: "N"}[i == 0]
			add(v, who)
			if x.Minor(in.Received) {
				gw := fmt.Sprintf("Guardian of nominee %d", i+1)
				g := CDSLPerson{Name: x.Guardian, Address: in.F[fmt.Sprintf("guardian%d_address", i+1)], City: in.F[fmt.Sprintf("guardian%d_city", i+1)], State: state, PIN: in.F[fmt.Sprintf("guardian%d_pin", i+1)]}
				gv := person("08", g, gw)
				gv["nominee_serial"], gv["relation"] = fmt.Sprintf("%02d", i+1), boRelation(x.GuardianRelation)
				add(gv, gw)
			}
		}
		if len(c.Nominees) > 0 && total != 10000 {
			probs = append(probs, fmt.Sprintf("Nominees: the shares add to %d.%02d%%, not 100%%", total/100, total%100))
		}
	} else {
		as := 0
		for _, x := range u.Contacts {
			f, m, l := boName(x.Name)
			if f == "" {
				continue
			}
			as++
			add(map[string]string{"purpose": "18", "name": f, "middle": m, "last": l, "user_text1": strings.ToUpper(trunc(x.Designation, 50))}, fmt.Sprintf("Authorised signatory %d", as))
		}
		if as == 0 {
			probs = append(probs, "Authorised signatory: a non-individual account needs at least one (add the contacts on the client's UCC details)")
		}
	}
	if d.PMS != nil {
		f, m, l := boName(d.PMS.Name)
		add(map[string]string{"purpose": "23", "name": f, "middle": m, "last": l, "user_text2": strings.ToUpper(d.PMS.SEBIReg)}, "PMS manager")
	}
	for i, x := range d.CMMappings {
		rec = append(rec, boLine{Layout: "s21", No: n, Who: fmt.Sprintf("CM account mapping %d", i+1), V: map[string]string{"purpose": "21", "map_flag": x.Flag, "ex_id": x.ExID, "cm_id": x.CMID, "cm_account": x.CMAccount}})
		n++
	}
	links := d.UCCLinks
	if len(links) == 0 {
		var w string
		links, w = boAutoUCCLinks(c, in.Members, in.Def)
		if w != "" {
			warns = append(warns, w)
		}
	}
	for i, x := range links {
		rec = append(rec, boLine{Layout: "s22", No: n, Who: fmt.Sprintf("BO-UCC link %d", i+1), V: map[string]string{"purpose": "22", "link_flag": firstOf(x.Flag, "A"), "consent": firstOf(x.Consent, "1"),
			"exchange_id": x.Exchange, "ucc": x.UCC, "segment": x.Segment, "cm_id": x.CMID, "tm_code": x.TMCode}})
		n++
	}
	if n > 99 {
		probs = append(probs, "the record has more than 99 lines")
	}
	return rec, probs, warns
}

func appFather(a *Application) string {
	if a == nil {
		return ""
	}
	return a.Father
}

func appPANPassed(a *Application) bool {
	if a == nil {
		return false
	}
	for _, k := range a.Checks {
		if strings.HasPrefix(k.Name, "PAN") && k.Status == "Passed" {
			return true
		}
	}
	return false
}

func minorOn(dob string, on time.Time) bool {
	d, err := time.Parse("2006-01-02", dob)
	return err == nil && d.AddDate(18, 0, 0).After(on)
}

// boCityBefore is the word or words just before the PIN, after the last
// comma.
func boCityBefore(addr, pin string) string {
	if pin == "" {
		return ""
	}
	before := strings.TrimRight(strings.TrimSpace(strings.Split(addr, pin)[0]), " ,-")
	if k := strings.LastIndex(before, ","); k >= 0 {
		return strings.TrimSpace(before[k+1:])
	}
	return ""
}

// boExchangeIDs are CDSL's exchange IDs.
var boExchangeIDs = map[string]string{"BSE": "11", "NSE": "12", "NCDEX": "22", "MCX": "23", "MSEI": "29"}

// boSegments are CDSL's segment codes for the client's segments.
var boSegments = map[string]string{"CM": "1", "FO": "2", "CD": "3", "COM": "5"}

// boAutoUCCLinks makes the BO-UCC links from the client's segments and the
// firm's trading member codes, when the DP's clearing member ID is set.
func boAutoUCCLinks(c Client, members, def map[string]string) ([]CDSLUCCLink, string) {
	cm := strings.TrimSpace(def["ucc_cm_id"])
	if cm == "" {
		return nil, "No BO-UCC link written: set the clearing member ID for BO-UCC links in the BO defaults"
	}
	var out []CDSLUCCLink
	for _, ex := range []string{"BSE", "NSE", "MCX"} {
		tm := members[ex]
		if tm == "" {
			continue
		}
		for _, s := range c.Segments {
			if (s == "COM") != (ex == "MCX") {
				continue
			}
			out = append(out, CDSLUCCLink{Flag: "A", Consent: "1", Exchange: boExchangeIDs[ex], UCC: c.UCC, Segment: boSegments[s], CMID: cm, TMCode: tm})
		}
	}
	return out, ""
}

// ---- building the files ----

// boOperator is the operator ID for the header.
func boOperator(def map[string]string, x cdslConn) string {
	op := strings.TrimSpace(def["operator_id"])
	if op == "" {
		op = x.User
	}
	if len(op) > 6 {
		op = op[:6]
	}
	return strings.ToUpper(op)
}

// cdslSetupFile builds a BO setup file for clients.
func (o *Ops) cdslSetupFile(ctx context.Context, tenant, actor string, x cdslConn, uccs []string, f map[string]string, deleteFirst string) (string, []byte, []string, []string, error) {
	def := o.CDSLBODefaultsNow(ctx, tenant).Values
	set, _ := o.Settings(ctx, tenant)
	cfg := o.CDSLPledgeSettings(ctx, tenant)
	file := boFile{Prefix: "08", DPID: x.DPID, Operator: boOperator(def, x)}
	var probs, warns []string
	for i, ucc := range uccs {
		c, err := o.Client(ctx, tenant, ucc)
		if err != nil {
			return "", nil, nil, nil, invalid("client %s not found", ucc)
		}
		in := boSetupIn{C: c, App: o.cdslAppFor(ctx, tenant, c.UCC), U: o.UCCDetailsFor(ctx, tenant, c.UCC), D: o.CDSLBODetailFor(ctx, tenant, c.UCC), Def: def, F: f, BO: o.bo(ctx, tenant, c),
			Serial: i + 1, Received: o.now(), Members: set.MemberCodes, DDPIID: cfg.DDPIPOAID, Sig: o.cdslSignature(ctx, tenant, c.UCC) != nil, DeleteFirst: deleteFirst}
		rec, p, w := boSetupRecord(in)
		for _, s := range p {
			probs = append(probs, prefixUCC(len(uccs), c.UCC, s))
		}
		for _, s := range w {
			warns = append(warns, prefixUCC(len(uccs), c.UCC, s))
		}
		file.Records = append(file.Records, rec)
	}
	if file.Operator == "" {
		probs = append(probs, "set the CDAS operator ID for upload files in the BO defaults")
	}
	if len(probs) > 0 {
		return "", nil, dedupe(probs), warns, nil
	}
	b, p := file.Bytes()
	if len(p) > 0 {
		return "", nil, p, warns, nil
	}
	seq, err := o.next(ctx, tenant, "cdslbofile08", actor)
	if err != nil {
		return "", nil, nil, nil, err
	}
	return boFileName("08", x.DPID, seq), b, nil, warns, nil
}

func prefixUCC(n int, ucc, s string) string {
	if n > 1 {
		return ucc + ": " + s
	}
	return s
}

// boModifyIn is what a BO modify record is made from: the change and the
// client as it now stands.
type boModifyIn struct {
	Ch       ChangeRequest
	C        Client
	BOID     string
	Def      map[string]string
	F        map[string]string
	Received time.Time
}

// boSet sets a modified value: an empty new value clears the field when
// it had one before.
func boSet(v map[string]string, k, old, new string) {
	new = strings.TrimSpace(new)
	switch {
	case new != "":
		v[k] = new
	case strings.TrimSpace(old) != "":
		v[k] = boClear
	}
}

// boModifyRecord builds a BO modify record for an approved change.
func boModifyRecord(in boModifyIn) ([]boLine, []string, []string) {
	ch, n := in.Ch, in.Ch.New
	var probs, warns []string
	rec := []boLine{{Layout: "m01", No: 1, Who: "Record", V: map[string]string{"bo_id": in.BOID, "request_date": boDateTime(in.Received)}}}
	if !reBOIDCDSL.MatchString(in.BOID) {
		probs = append(probs, "the account has no 16-digit CDSL BO ID")
	}
	l2, l5 := map[string]string{}, map[string]string{}
	switch ch.Kind {
	case "Email":
		boSet(l2, "email", ch.Old["email"], n["email"])
	case "Mobile":
		boSet(l2, "mobile", ch.Old["mobile"], n["mobile"])
		if n["mobile"] != "" {
			l2["isd"] = firstOf(in.Def["primary_mobil_isd_code"], "91")
		}
	case "Address":
		if a, ok := n["address"]; ok {
			l := boAddress(a)
			for i, k := range []string{"addr1", "addr2", "addr3"} {
				// a line that is now empty is cleared
				boSet(l2, k, "x", strings.ToUpper(l[i]))
			}
		}
		if c := n["city"]; c != "" {
			l2["city"] = strings.ToUpper(c)
		}
		st := firstOf(n["state"], in.C.State)
		if n["state"] != "" || n["city"] != "" {
			if sc := boStateCode(st); sc != "" {
				l2["state_code"], l2["country"] = sc, "IN"
				l2["city_seq"] = "00"
			} else {
				probs = append(probs, fmt.Sprintf("Holder: the state %q has no ISO 3166-2 code CDSL takes", st))
			}
		}
		if p := n["pin"]; p != "" {
			l2["zip"] = p
		}
		if n["address"] == "" && n["pin"] == "" {
			warns = append(warns, "Only the city and state changed in Ledgerline; check the address lines and PIN code at CDSL")
		}
	case "Bank account":
		l5["ifsc"], l5["bank_acct"] = strings.ToUpper(n["ifsc"]), n["account"]
		if mc := in.F["divnd_bank_code"]; mc != "" {
			l5["bank_code"] = mc
		} else {
			warns = append(warns, "The bank's MICR code (CDSL's dividend bank code) is not changed: give it on the request if the bank changed")
		}
	case "Nomination":
		var old []Nominee
		_ = json.Unmarshal([]byte(ch.Old["nominees"]), &old)
		now := in.C.Nominees
		if in.C.Nominee == "Opted in" && len(now) > 0 {
			l5["nom_opt_out"] = "N"
		} else {
			l5["nom_opt_out"] = "Y"
			now = nil
		}
		no := 8
		for i, x := range now {
			f, m, l := boName(x.Name)
			v := map[string]string{"purpose": "06", "txn_type": map[bool]string{true: "M", false: "S"}[i < len(old)], "name": f, "middle": m, "last": l,
				"nominee_serial": fmt.Sprintf("%02d", i+1), "relation": boRelation(x.Relation), "share": boShare(x.Share), "residual": map[bool]string{true: "Y", false: "N"}[i == 0],
				"dob": boDate(x.DOB), "email": x.Email}
			if x.Mobile != "" {
				v["isd"], v["mobile"] = "91", x.Mobile
			}
			if x.IDType == "PAN" {
				v["pan"] = x.ID
			}
			pin := firstOf(in.F[fmt.Sprintf("nominee%d_pin", i+1)], rePINIn.FindString(x.Address))
			city := firstOf(in.F[fmt.Sprintf("nominee%d_city", i+1)], boCityBefore(x.Address, pin))
			if x.Address == "" || pin == "" || city == "" {
				probs = append(probs, fmt.Sprintf("Nominee %d (%s): CDSL needs the nominee's address with city and PIN code", i+1, x.Name))
			}
			boAddr(v, strings.TrimSpace(strings.Replace(x.Address, pin, "", 1)), city, in.C.State, pin, "00")
			rec = append(rec, boLine{Layout: "m08", No: no, Who: fmt.Sprintf("Nominee %d", i+1), V: v})
			no++
		}
		for i := len(now); i < len(old); i++ {
			f, m, l := boName(old[i].Name)
			rec = append(rec, boLine{Layout: "m08", No: no, Who: fmt.Sprintf("Nominee %d (removed)", i+1), V: map[string]string{"purpose": "06", "txn_type": "D", "name": f, "middle": m, "last": l, "nominee_serial": fmt.Sprintf("%02d", i+1)}})
			no++
		}
	default:
		return nil, []string{"a " + strings.ToLower(ch.Kind) + " change is not one CDSL holds"}, nil
	}
	// Lines in order: 01, 02, 05, then 08 onwards.
	out := []boLine{rec[0]}
	if len(l2) > 0 {
		out = append(out, boLine{Layout: "m02", No: 2, Who: "Holder", V: l2})
	}
	if len(l5) > 0 {
		out = append(out, boLine{Layout: "m05", No: 5, Who: "Account", V: l5})
	}
	out = append(out, rec[1:]...)
	if len(out) == 1 {
		probs = append(probs, "nothing in the change is held by CDSL")
	}
	return out, probs, warns
}

// cdslModifyFile builds the BO modify file for one approved change.
func (o *Ops) cdslModifyFile(ctx context.Context, tenant, actor string, x cdslConn, ch ChangeRequest, f map[string]string) (string, []byte, []string, []string, error) {
	c, err := o.Client(ctx, tenant, ch.UCC)
	if err != nil {
		return "", nil, nil, nil, err
	}
	def := o.CDSLBODefaultsNow(ctx, tenant).Values
	at, err := time.Parse(time.RFC3339, ch.At)
	if err != nil {
		at = o.now()
	}
	rec, probs, warns := boModifyRecord(boModifyIn{Ch: ch, C: c, BOID: o.bo(ctx, tenant, c).BOID, Def: def, F: f, Received: at})
	return o.cdslWriteModify(ctx, tenant, actor, x, def, rec, probs, warns)
}

func (o *Ops) cdslWriteModify(ctx context.Context, tenant, actor string, x cdslConn, def map[string]string, rec []boLine, probs, warns []string) (string, []byte, []string, []string, error) {
	file := boFile{Prefix: "11", DPID: x.DPID, Operator: boOperator(def, x), Records: [][]boLine{rec}}
	if file.Operator == "" {
		probs = append(probs, "set the CDAS operator ID for upload files in the BO defaults")
	}
	if len(probs) > 0 {
		return "", nil, dedupe(probs), warns, nil
	}
	b, p := file.Bytes()
	if len(p) > 0 {
		return "", nil, p, warns, nil
	}
	seq, err := o.next(ctx, tenant, "cdslbofile11", actor)
	if err != nil {
		return "", nil, nil, nil, err
	}
	return boFileName("11", x.DPID, seq), b, nil, warns, nil
}

// ---- deletion of a deceased joint holder's name ----

// CDSLDeleteHolder raises the request to delete a deceased joint holder's
// name (CDSL/OPS/DP/POLCY/2023/404): the first holder by a BO setup record
// with the survivors in order and deletion flag 100, the second or third by
// a BO modify record with flag 010 or 001. The death must be within 365
// days; older ones go through transmission.
func (o *Ops) CDSLDeleteHolder(ctx context.Context, tenant, actor, ucc string, holder int, diedOn string) (CDSLRequest, error) {
	if err := o.require(ctx, tenant, actor, "Raising CDSL requests", RoleMaker, RoleChecker); err != nil {
		return CDSLRequest{}, err
	}
	x, ok := o.cdslConnFor(ctx, tenant)
	if !ok {
		return CDSLRequest{}, fmt.Errorf("%w: the CDSL APIs connection is not set", ErrState)
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return CDSLRequest{}, invalid("client %s not found", ucc)
	}
	b := o.bo(ctx, tenant, c)
	d := o.CDSLBODetailFor(ctx, tenant, c.UCC)
	died, err := time.ParseInLocation("2006-01-02", diedOn, ist)
	switch {
	case err != nil:
		return CDSLRequest{}, invalid("give the date of death as on the death certificate")
	case died.After(o.now()):
		return CDSLRequest{}, invalid("the date of death is in the future")
	case o.now().Sub(died) > 365*24*time.Hour:
		return CDSLRequest{}, invalid("the holder died more than 365 days ago: CDSL does not delete the name; use transmission instead")
	case holder < 1 || holder > 3:
		return CDSLRequest{}, invalid("holder is 1, 2 or 3")
	case holder-1 > len(d.Holders) || len(d.Holders) == 0:
		return CDSLRequest{}, invalid("the account has no holder %d on its CDSL details", holder)
	case !reBOIDCDSL.MatchString(b.BOID):
		return CDSLRequest{}, invalid("the account has no CDSL BO ID")
	}
	f := map[string]string{"delete_holder": fmt.Sprint(holder), "died_on": diedOn}
	r := CDSLRequest{Kind: CDSLModify, UCC: c.UCC, Name: c.Name, BOID: b.BOID, Source: fmt.Sprintf("deletion of holder %d", holder), Fields: f, Maker: actor}
	if holder == 1 {
		r.Kind = CDSLSetupUpload
	}
	m, err := o.cdslDeleteHolderFile(ctx, tenant, actor, x, c.UCC, holder, diedOn)
	if err != nil {
		return r, err
	}
	o.cdslPutFile(ctx, tenant, actor, &r, fmt.Sprintf("Deletion of holder %d", holder), m.name, m.data, m.probs, m.warns, nil)
	return o.cdslNewRequest(ctx, tenant, actor, r)
}

// cdslAfterUpload applies what a processed upload did to Ledgerline's
// records (a deleted holder leaves the CDSL details).
func (o *Ops) cdslAfterUpload(ctx context.Context, tenant, actor string, r *CDSLRequest) {
	h := r.Fields["delete_holder"]
	if h == "" || r.UCC == "" {
		return
	}
	_, _ = upd(ctx, o.Docs, tenant, cCDSLBODetail, r.UCC, actor, &CDSLBODetail{UCC: r.UCC}, func(d *CDSLBODetail) error {
		switch h {
		case "1":
			// the second holder is now first: the client master takes their
			// details through a change request; here the holders move up
			if len(d.Holders) > 0 {
				d.Holders = d.Holders[1:]
			}
		case "2", "3":
			i := map[string]int{"2": 0, "3": 1}[h]
			if i < len(d.Holders) {
				d.Holders = append(d.Holders[:i], d.Holders[i+1:]...)
			}
		}
		if len(d.Holders) == 0 {
			d.ModeOfOperation, d.CommPref = "0", "1"
		}
		return nil
	})
	if h == "1" {
		o.raiseWork(ctx, tenant, "cdslbo", r.ID+"-holder", "Update "+r.UCC+": the first holder's name was deleted at CDSL",
			"CDSL processed the deletion of the deceased first holder. Change the client master to the surviving holder (name, PAN, contact, bank), and capture the survivors' signatures in CDAS.", r.UCC, RoleMaker, "clients", "")
	}
	r.event(o.now().Format(time.RFC3339), actor, "Holder "+h+" removed from the CDSL details")
}

// ---- signature and closure upload files (DP-5565 Annexures C and D) ----

// CDSLSignatureFile is the BO signature upload (06) for a client's latest
// signature image, as a ZIP with the image named as CDSL asks. txn is S
// (set up), R (replace) or D (delete). It is uploaded in CDAS.
func (o *Ops) CDSLSignatureFile(ctx context.Context, tenant, actor, ucc, txn string) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Raising CDSL requests", RoleMaker, RoleChecker); err != nil {
		return "", nil, err
	}
	x, ok := o.cdslConnFor(ctx, tenant)
	if !ok {
		return "", nil, fmt.Errorf("%w: the CDSL APIs connection is not set (the DP ID)", ErrState)
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return "", nil, invalid("client %s not found", ucc)
	}
	txn = firstOf(strings.ToUpper(txn), "S")
	if !contains([]string{"S", "R", "D"}, txn) {
		return "", nil, invalid("the transaction is S (set up), R (replace) or D (delete)")
	}
	b := o.bo(ctx, tenant, c)
	if !reBOIDCDSL.MatchString(b.BOID) {
		return "", nil, invalid("%s has no CDSL BO ID", c.UCC)
	}
	sig := o.cdslSignature(ctx, tenant, c.UCC)
	if sig == nil {
		return "", nil, invalid("%s has no signature image (JPG or PNG) on file", c.UCC)
	}
	rf, err := o.App.Ingest.Raw.Get(ctx, tenant, sig.FileID)
	if err != nil {
		return "", nil, err
	}
	ext := map[string]string{"image/jpeg": ".jpg", "image/png": ".png"}[sig.ContentType]
	img := "SIG" + strings.TrimPrefix(sig.ID, "DOC-")
	v := map[string]string{"txn_type": txn, "bo_id": b.BOID, "image": img}
	if txn == "R" {
		v["new_image"] = img
	}
	if txn != "S" {
		v["request_date"] = boDateTime(o.now())
	}
	def := o.CDSLBODefaultsNow(ctx, tenant).Values
	file := boFile{Prefix: "06", DPID: x.DPID, Operator: boOperator(def, x), Records: [][]boLine{{{Layout: "sig", Who: "Signature", V: v}}}}
	data, probs := file.Bytes()
	if len(probs) > 0 {
		return "", nil, invalid("%s", strings.Join(probs, "; "))
	}
	seq, err := o.next(ctx, tenant, "cdslbofile06", actor)
	if err != nil {
		return "", nil, err
	}
	name := boFileName("06", x.DPID, seq)
	z, err := boZip(name, data, map[string][]byte{"B" + b.BOID + img + ext: rf.Content})
	if err != nil {
		return "", nil, err
	}
	o.log(ctx, tenant, actor, "cdsl.signature_file", c.UCC, map[string]any{"bo": cdslMaskBO(b.BOID), "txn": txn, "file": name})
	return name + ".zip", z, nil
}

// boZip packs a data file with its images.
func boZip(name string, data []byte, extra map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		return nil, err
	}
	w.Write(data)
	for k, v := range extra {
		w, err := zw.Create(path.Base(k))
		if err != nil {
			return nil, err
		}
		w.Write(v)
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CDSLClosureFile is the BO closure upload (13) for a DP closure request:
// a setup of the closure, initiated by the client, with the remaining
// balances transferred to the account given (or none). It is uploaded in
// CDAS when the transfer cum closure API is not used.
func (o *Ops) CDSLClosureFile(ctx context.Context, tenant, actor, id string) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Raising CDSL requests", RoleMaker, RoleChecker); err != nil {
		return "", nil, err
	}
	x, ok := o.cdslConnFor(ctx, tenant)
	if !ok {
		return "", nil, fmt.Errorf("%w: the CDSL APIs connection is not set (the DP ID)", ErrState)
	}
	r, _, err := docs.Get[DPRequest](ctx, o.Docs, tenant, cDPReq, id)
	if err != nil || r.Kind != "Closure" {
		return "", nil, invalid("%s is not a closure request", id)
	}
	c, err := o.Client(ctx, tenant, r.UCC)
	if err != nil {
		return "", nil, err
	}
	b := o.bo(ctx, tenant, c)
	at, _ := time.Parse(time.RFC3339, r.At)
	to := strings.TrimSpace(r.Detail["transfer_to"])
	v := map[string]string{"bo_id": b.BOID, "txn_type": "S", "initiated_by": "1", "reason": firstOf(r.Detail["reason_code"], "91"), "balances": "N",
		"remarks": strings.ToUpper(trunc(firstOf(r.Detail["reason"], "Closure at the client's request"), 100)), "int_ref": strings.ReplaceAll(r.ID, "-", ""), "request_date": boDateTime(at)}
	if to != "" {
		v["balances"], v["new_bo_id"] = "T", to
		if r.Detail["reason_code"] == "" && !strings.HasPrefix(to, b.BOID[:min(8, len(b.BOID))]) {
			v["reason"] = "92" // transfer to another DP
		}
	}
	if !reBOIDCDSL.MatchString(b.BOID) {
		return "", nil, invalid("%s has no CDSL BO ID", c.UCC)
	}
	def := o.CDSLBODefaultsNow(ctx, tenant).Values
	file := boFile{Prefix: "13", DPID: x.DPID, Operator: boOperator(def, x), Records: [][]boLine{{{Layout: "clo", Who: "Closure", V: v}}}}
	data, probs := file.Bytes()
	if len(probs) > 0 {
		return "", nil, invalid("%s", strings.Join(probs, "; "))
	}
	seq, err := o.next(ctx, tenant, "cdslbofile13", actor)
	if err != nil {
		return "", nil, err
	}
	name := boFileName("13", x.DPID, seq)
	o.log(ctx, tenant, actor, "cdsl.closure_file", c.UCC, map[string]any{"bo": cdslMaskBO(b.BOID), "request": r.ID, "file": name})
	return name, data, nil
}

// ---- the files on requests ----

// cdslFillModify makes a modification's BO modify file from its change.
func (o *Ops) cdslFillModify(ctx context.Context, tenant, actor string, x cdslConn, r *CDSLRequest, ch ChangeRequest) {
	name, data, probs, warns, err := o.cdslModifyFile(ctx, tenant, actor, x, ch, r.Fields)
	o.cdslPutFile(ctx, tenant, actor, r, "BO modify", name, data, probs, warns, err)
}

// cdslPutFile keeps a file made for a request, or the reasons it was not.
func (o *Ops) cdslPutFile(ctx context.Context, tenant, actor string, r *CDSLRequest, what, name string, data []byte, probs, warns []string, err error) {
	now := o.now().Format(time.RFC3339)
	r.Warnings = warns
	if err == nil && len(probs) == 0 {
		var ref CDSLFileRef
		ref, _, err = o.cdslKeepFile(ctx, tenant, name, data)
		if err == nil {
			r.File, r.Problems, r.Status = &ref, nil, crChecker
			r.event(now, actor, what+" file made: "+name)
			return
		}
	}
	if err != nil {
		probs = append(probs, err.Error())
	}
	r.Problems, r.Status = probs, crNeedsFile
	r.event(now, actor, what+" file not made: "+strings.Join(probs, "; "))
}

// cdslRegenerate makes a request's upload file again from Ledgerline's
// records as they now stand.
func (o *Ops) cdslRegenerate(ctx context.Context, tenant, actor string, x cdslConn, r *CDSLRequest) error {
	if h := r.Fields["delete_holder"]; h != "" {
		hn := 0
		fmt.Sscanf(h, "%d", &hn)
		m, err := o.cdslDeleteHolderFile(ctx, tenant, actor, x, r.UCC, hn, r.Fields["died_on"])
		if err != nil {
			return err
		}
		o.cdslPutFile(ctx, tenant, actor, r, "Holder deletion", m.name, m.data, m.probs, m.warns, nil)
		return nil
	}
	switch r.Kind {
	case CDSLModify:
		if !strings.HasPrefix(r.Source, "CHG-") {
			return invalid("%s was not made from a change request: attach its file", r.ID)
		}
		ch, _, err := docs.Get[ChangeRequest](ctx, o.Docs, tenant, cChange, r.Source)
		if err != nil {
			return err
		}
		o.cdslFillModify(ctx, tenant, actor, x, r, ch)
	case CDSLSetupUpload:
		uccs := strings.Fields(strings.ReplaceAll(r.Fields["uccs"], ",", " "))
		if len(uccs) == 0 && r.UCC != "" {
			uccs = []string{r.UCC}
		}
		if len(uccs) == 0 {
			return invalid("%s was attached by staff: attach its file again", r.ID)
		}
		name, data, probs, warns, err := o.cdslSetupFile(ctx, tenant, actor, x, uccs, r.Fields, "")
		o.cdslPutFile(ctx, tenant, actor, r, "BO setup", name, data, probs, warns, err)
	}
	return nil
}

type boMade struct {
	name         string
	data         []byte
	probs, warns []string
}

// cdslDeleteHolderFile makes the file that deletes a deceased holder's
// name.
func (o *Ops) cdslDeleteHolderFile(ctx context.Context, tenant, actor string, x cdslConn, ucc string, holder int, diedOn string) (boMade, error) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return boMade{}, err
	}
	var m boMade
	if holder == 1 {
		m.name, m.data, m.probs, m.warns, err = o.cdslSetupFile(ctx, tenant, actor, x, []string{c.UCC}, nil, diedOn)
		return m, err
	}
	b := o.bo(ctx, tenant, c)
	v := map[string]string{"del_flag": map[int]string{2: "010", 3: "001"}[holder], "del_reason": "06", "death_date": boDate(diedOn)}
	rec := []boLine{{Layout: "m01", No: 1, Who: "Record", V: map[string]string{"bo_id": b.BOID, "request_date": boDateTime(o.now())}}, {Layout: "m03", No: holder + 1, Who: fmt.Sprintf("Holder %d", holder), V: v}}
	m.name, m.data, m.probs, m.warns, err = o.cdslWriteModify(ctx, tenant, actor, x, o.CDSLBODefaultsNow(ctx, tenant).Values, rec, nil, nil)
	return m, err
}
