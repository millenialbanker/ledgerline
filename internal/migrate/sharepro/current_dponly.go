package sharepro

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

// DP-only clients and the clients' demat accounts.
//
// A DP-only client holds a demat account at the firm's DP and has no
// trading account: a CLLIST account whose SUBCODE is blank or is not a
// client in DLMAST. Each such account becomes a client of its own
// (CLIENT-MASTER DPOnly = Y) under the DP client code (BENACNUM, 8 digits),
// with its details from CLLIST (holders, address, bank), CLLIST_DET (KYC
// details) and CDNOMREG (nominees). A closed account still comes across,
// marked inactive: the firm keeps its records, and its history and
// documents attach to it.
//
// The client master's demat account of a trading client is its primary
// account in SharePro's client demat master (CDMAST), which also lists
// accounts at other DPs; without one, its oldest open account at the
// firm's DP.

const (
	qDPDetails   = `/*sp:dp-details*/ SELECT %s FROM CLLIST_DET`
	qDPNominees  = `/*sp:dp-nominees*/ SELECT %s FROM CDNOMREG ORDER BY %s`
	qClientDemat = `/*sp:client-demat*/ SELECT %s FROM CDMAST c%s`
	qCheckDPOnly = `/*sp:check-dp-only-clients*/ SELECT COUNT(*) AS n FROM CLLIST l WHERE %s`
)

var (
	reCDSLBO = regexp.MustCompile(`^[0-9]{16}$`)
	reNSDLBO = regexp.MustCompile(`^IN[0-9]{14}$`)
)

// holderCols are the CLLIST columns of the account's holders, read with
// the account (never the Aadhaar or password columns).
var holderCols = []string{"BRANCHID", "BENTYPE", "BEN1HOLDER", "BEN1FATHUB", "FATHUSNAME", "ITPAN", "BEN2HOLDER", "BEN2PAN", "BEN3HOLDER", "BEN3PAN",
	"DOB", "BOSEX", "BEN1ADD1", "BEN1ADD2", "BEN1ADD3", "BEN1ADD4", "BEN1CITY", "BEN1STATE", "BEN1CNTRY", "BEN1PIN", "StateCd",
	"BEN1FADD1", "BEN1FADD2", "BEN1FADD3", "BEN1FCITY", "BEN1FSTATE", "BEN1FCNTRY", "BEN1FPIN", "CADD1", "CADD2", "CADD3", "CADD4", "CCITY", "CPIN", "CCNTRY",
	"FHOLDER_MOBILE", "FHOLDER_EMAIL", "CUSTEMAIL", "BENBNKNAME", "BENBNKAC", "BENBNKTYPE", "MICRCODE", "BANKCD", "CLRISKPROFILE", "NOMFNAME", "GROUPCD", "BENSTATRSN"}

// dpHolder is what CLLIST says about an account's holders.
type dpHolder struct {
	name1, fath1, pan1, name2, pan2, name3, pan3 string
	dob, sex, benType, branch, group, risk       string
	add                                          [4]string
	city, state, statecd, country, pin           string
	padd                                         [3]string
	pcity, pstate, pcountry, ppin                string
	cadd                                         [4]string
	ccity, cpin, ccountry                        string
	mobile, email                                string
	bankName, bankAc, bankType, micr, bankCd     string
	nominee, statRsn                             string
}

func readHolder(r migrate.Row) *dpHolder {
	h := &dpHolder{name1: str(r, "ben1holder"), fath1: firstNonEmpty(str(r, "ben1fathub"), str(r, "fathusname")), pan1: upper(r, "itpan"),
		name2: str(r, "ben2holder"), pan2: upper(r, "ben2pan"), name3: str(r, "ben3holder"), pan3: upper(r, "ben3pan"),
		dob: date(r, "dob"), sex: upper(r, "bosex"), benType: str(r, "bentype"), branch: upper(r, "branchid"), group: str(r, "groupcd"), risk: str(r, "clriskprofile"),
		city: str(r, "ben1city"), state: str(r, "ben1state"), statecd: upper(r, "statecd"), country: str(r, "ben1cntry"), pin: str(r, "ben1pin"),
		pcity: str(r, "ben1fcity"), pstate: str(r, "ben1fstate"), pcountry: str(r, "ben1fcntry"), ppin: str(r, "ben1fpin"),
		ccity: str(r, "ccity"), cpin: str(r, "cpin"), ccountry: str(r, "ccntry"),
		mobile: str(r, "fholder_mobile"), email: strings.ToLower(firstNonEmpty(str(r, "fholder_email"), str(r, "custemail"))),
		bankName: str(r, "benbnkname"), bankAc: str(r, "benbnkac"), bankType: str(r, "benbnktype"), micr: str(r, "micrcode"), bankCd: upper(r, "bankcd"),
		nominee: str(r, "nomfname"), statRsn: str(r, "benstatrsn")}
	for i := range h.add {
		h.add[i] = str(r, fmt.Sprintf("ben1add%d", i+1))
		h.cadd[i] = str(r, fmt.Sprintf("cadd%d", i+1))
	}
	for i := range h.padd {
		h.padd[i] = str(r, fmt.Sprintf("ben1fadd%d", i+1))
	}
	return h
}

// dpDetail is CLLIST_DET of a DP-only account.
type dpDetail struct {
	sex, marital, nation, nationOth, occup, occupText, income, pep string
	ckyc, kraDesc, ipv, regNo                                      string
	contacts                                                       []contact
}

// cdRec is a trading client's demat account in CDMAST.
type cdRec struct {
	boid, depository string
	primary, closed  bool
}

// loadDPOnly links each CLLIST account to its client and makes a client of
// every account with no trading client.
func loadDPOnly(ctx context.Context, d db, s *schema, cm *common) error {
	used := map[string]bool{}
	for u := range cm.clients {
		used[u] = true
	}
	var only []*dpAcct
	for i := range cm.dp {
		a := &cm.dp[i]
		if a.ucc != "" && cm.clients[a.ucc] != nil {
			a.client = a.ucc
			continue
		}
		if a.ucc != "" {
			a.sub, a.ucc = a.ucc, "" // a trading code that is not a client
		}
		a.dpOnly = true
		code, note := dpOnlyCode(a, used)
		a.note = note
		if code == "" {
			continue // reported with the DP accounts
		}
		used[code] = true
		a.client = code
		only = append(only, a)
	}
	if len(only) == 0 {
		return nil
	}
	want := map[string]bool{}
	for _, a := range only {
		want[a.co+"|"+a.ben] = true
	}
	// bank names and IFSCs (BANKMAST.RTGS) of CLLIST.BANKCD
	banks := map[string][2]string{}
	if s.hasCol("CLLIST", "BANKCD") && s.has("BANKMAST") {
		if err := d.query(ctx, qBankNames, func(r migrate.Row) error {
			banks[upper(r, "bankcd")] = [2]string{str(r, "descr"), upper(r, "ifsc")}
			return nil
		}); err != nil {
			return err
		}
	}
	nominees := map[string][]nomRec{}
	if s.has("CDNOMREG") {
		list, miss := s.sel("CDNOMREG", "", "CO", "BENACNUM", "NOMSLNO", "BOREL", "SHAREPR", "FIRSTNM", "MIDNM", "LASTNM", "NOMFNAME", "NOMGRNAME",
			"ADD1", "ADD2", "ADD3", "CITY", "PIN", "MOBILE", "SMobile", "Email", "PEMAIL", "PAN", "NomDob", "NOMACTVDT")
		cm.missing = append(cm.missing, miss...)
		if err := d.query(ctx, fmt.Sprintf(qDPNominees, list, s.order("CDNOMREG", "", "CO", "BENACNUM", "NOMSLNO")), func(r migrate.Row) error {
			k := str(r, "co") + "|" + upper(r, "benacnum")
			if !want[k] {
				return nil
			}
			name := strings.Join(strings.Fields(str(r, "firstnm")+" "+str(r, "midnm")+" "+str(r, "lastnm")), " ")
			n := nomRec{name: firstNonEmpty(name, str(r, "nomfname")), rel: dpRelation(str(r, "borel")), dob: date(r, "nomdob"), guardian: str(r, "nomgrname"),
				mobile: firstNonEmpty(zeroBlank(str(r, "mobile")), str(r, "smobile")), email: strings.ToLower(firstNonEmpty(str(r, "email"), str(r, "pemail"))),
				addr: strings.Join(strings.Fields(strings.Join([]string{str(r, "add1"), str(r, "add2"), str(r, "add3"), str(r, "city"), zeroBlank(str(r, "pin"))}, " ")), " "),
				date: date(r, "nomactvdt")}
			if sh := dec(r, "sharepr"); sh.Sign() > 0 {
				n.share = sh.String()
			}
			switch pan := upper(r, "pan"); {
			case rePAN.MatchString(pan):
				n.idType, n.id = "PAN", pan
			case pan != "":
				cm.dpNomNoPAN++
			}
			nominees[k] = append(nominees[k], n)
			return nil
		}); err != nil {
			return err
		}
	}
	if s.has("CLLIST_DET") {
		cols := []string{"CO", "BENACNUM", "SEX", "MARITALSTATUS", "NATION", "NATIONOTH", "OCCUPN", "OCCUPNALL", "GROSSINCOME", "PEP", "CKYCNO", "KRASTATDES",
			"IPV", "IPV_DATE", "IPVDT", "REGNO"}
		for i := 1; i <= 3; i++ {
			for _, f := range []string{"Name", "Desig", "Din", "Pan", "Add", "City", "Pin", "Phone", "Email"} {
				cols = append(cols, fmt.Sprintf("Cp%d_%s", i, f))
			}
		}
		list, miss := s.sel("CLLIST_DET", "", cols...)
		cm.missing = append(cm.missing, miss...)
		if err := d.query(ctx, fmt.Sprintf(qDPDetails, list), func(r migrate.Row) error {
			k := str(r, "co") + "|" + upper(r, "benacnum")
			if !want[k] {
				return nil
			}
			x := dpDetail{sex: upper(r, "sex"), marital: str(r, "maritalstatus"), nation: str(r, "nation"), nationOth: str(r, "nationoth"), occup: str(r, "occupn"),
				occupText: str(r, "occupnall"), income: str(r, "grossincome"), pep: str(r, "pep"), ckyc: str(r, "ckycno"), kraDesc: str(r, "krastatdes"), regNo: upper(r, "regno")}
			if str(r, "ipv") == "Y" || date(r, "ipv_date") != "" || date(r, "ipvdt") != "" {
				x.ipv = "Y"
			}
			for i := 1; i <= 3; i++ {
				p := fmt.Sprintf("cp%d_", i)
				if n := str(r, p+"name"); n != "" {
					x.contacts = append(x.contacts, contact{name: n, desig: firstNonEmpty(str(r, p+"desig"), "Authorised signatory"), pan: upper(r, p+"pan"), din: str(r, p+"din"),
						addr: strings.Join(strings.Fields(str(r, p+"add")+" "+str(r, p+"city")+" "+str(r, p+"pin")), " "), phone: str(r, p+"phone"), email: strings.ToLower(str(r, p+"email"))})
				}
			}
			cm.dpDet[k] = x
			return nil
		}); err != nil {
			return err
		}
	}
	for _, a := range only {
		h, k := a.h, a.co+"|"+a.ben
		country := strings.ToUpper(firstNonEmpty(h.country, h.ccountry))
		if indiaCountry(country) {
			country = "INDIA"
		}
		cl := &client{ucc: a.client, name: h.name1, pan: h.pan1, dob: h.dob, mobile: h.mobile, email: h.email, city: firstNonEmpty(h.city, h.ccity),
			state: h.state, statecd: h.statecd, country: country, address: firstNonEmpty(h.add[0], h.cadd[0]), pin: firstNonEmpty(h.pin, h.cpin), branch: h.branch,
			group: h.group, risk: h.risk, bank: h.bankName, bankAc: h.bankAc, micr: h.micr, bankType: h.bankType, nominee: h.nominee,
			commence: a.opened, added: a.opened, gender: firstNonEmpty(h.sex, cm.dpDet[k].sex), reg: map[string]bool{}, dpOnly: true, dp: a}
		if strings.TrimLeft(h.benType, "0") == "3" {
			cl.ctype = "NRI" // CDSL product number 3
		}
		if a.isClosed() {
			cl.closeOut = a.closedOn
		}
		if acct := strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(h.bankAc)); acct != "" {
			b := banks[h.bankCd]
			cl.bank = firstNonEmpty(h.bankName, b[0])
			cm.banks[cl.ucc] = append(cm.banks[cl.ucc], bankRec{ucc: cl.ucc, bank: cl.bank, ifsc: b[1], account: acct, typ: h.bankType, micr: h.micr, primary: true})
		}
		for _, n := range nominees[k] {
			n.ucc = cl.ucc
			if n.date == "" {
				n.date = a.opened
			}
			cm.nominees[cl.ucc] = append(cm.nominees[cl.ucc], n)
		}
		cm.clients[cl.ucc] = cl
		cm.order = append(cm.order, cl.ucc)
	}
	return nil
}

// dpOnlyCode is the DP-only client's code: the DP client code (BENACNUM),
// else, when that is taken by a trading client or is not a valid code,
// DP + the code, else the company + the code.
func dpOnlyCode(a *dpAcct, used map[string]bool) (code, note string) {
	ben := strings.ToUpper(strings.TrimSpace(a.ben))
	if ben == "" && len(a.boid) >= 8 {
		ben = a.boid[len(a.boid)-8:]
	}
	why := "is already a client code"
	if !reUCC.MatchString(ben) {
		why = "is not 3 to 10 letters or digits"
	}
	for i, x := range []string{ben, "DP" + ben, a.co + ben} {
		if !reUCC.MatchString(x) || used[x] {
			continue
		}
		if i == 0 {
			return x, ""
		}
		return x, fmt.Sprintf("DP client code %q %s", ben, why)
	}
	return "", fmt.Sprintf("DP client code %q %s, and no other code could be made", ben, why)
}

// dpRelation names CDNOMREG.BOREL, CDSL's relationship codes (the UDiFF
// catalogue's old values) [assumed].
func dpRelation(code string) string {
	if r, ok := cdslRelation[strings.TrimLeft(strings.TrimSpace(code), "0")]; ok {
		return r
	}
	return strings.TrimSpace(code)
}

// cdslRelation: 12 is "not provided", left blank.
var cdslRelation = map[string]string{"1": "Spouse", "2": "Son", "3": "Daughter", "4": "Father", "5": "Mother", "6": "Brother", "7": "Sister", "8": "Grandson",
	"9": "Granddaughter", "10": "Grandfather", "11": "Grandmother", "12": "", "13": "Other"}

func indiaCountry(c string) bool {
	switch strings.ToUpper(strings.TrimSpace(c)) {
	case "", "IN", "IND", "INDIA", "85", "101":
		return true
	}
	return false
}

// dpClientType: from the PAN like a trading client's; without a PAN, from
// CLLIST.BENTYPE (CDSL product number: 1 individual, 3 NRI, 8 HUF, others
// corporate) [assumed].
func dpClientType(cl *client) string {
	if len(cl.pan) == 10 || cl.dp == nil || cl.dp.h == nil {
		return clientType(cl)
	}
	switch strings.TrimLeft(cl.dp.h.benType, "0") {
	case "", "1", "3":
		return clientType(cl)
	case "8":
		return "HUF"
	}
	return "Corporate"
}

// loadClientDemat reads the trading clients' demat accounts (CDMAST, with
// the DP's ID from DPMAST) [assumed: TYPE Primary / Secondary like BKMAST].
func loadClientDemat(ctx context.Context, d db, s *schema, cm *common) error {
	if !s.hasCol("CDMAST", "SUBCD") || !s.hasCol("CDMAST", "CLIENTID") {
		return nil
	}
	list, miss := s.sel("CDMAST", "c", "SUBCD", "TYPE", "DPCODE", "CLIENTID", "Closed")
	cm.missing = append(cm.missing, miss...)
	join := ""
	if s.hasCol("CDMAST", "DPCODE") && s.hasCol("DPMAST", "DPCODE") && s.hasCol("DPMAST", "DPID") {
		list += ", RTRIM(p.DPID) AS [dpid]"
		join = " LEFT JOIN DPMAST p ON p.DPCODE = c.DPCODE"
	} else {
		list += ", NULL AS [dpid]"
	}
	return d.query(ctx, fmt.Sprintf(qClientDemat, list, join), func(r migrate.Row) error {
		typ := upper(r, "type")
		if typ == "POOL" {
			return nil // the firm's own
		}
		id := strings.ReplaceAll(upper(r, "clientid"), " ", "")
		boid := id
		if !reCDSLBO.MatchString(boid) && !reNSDLBO.MatchString(boid) {
			boid = strings.ReplaceAll(upper(r, "dpid"), " ", "") + id
		}
		if !reCDSLBO.MatchString(boid) && !reNSDLBO.MatchString(boid) {
			cm.cdBad++
			return nil
		}
		x := cdRec{boid: boid, depository: "CDSL", primary: typ == "PRIMARY", closed: flag(r, "closed")}
		if strings.HasPrefix(boid, "IN") {
			x.depository = "NSDL"
		}
		u := upper(r, "subcd")
		cm.cdmast[u] = append(cm.cdmast[u], x)
		return nil
	})
}

// ownAccounts are each client's CLLIST accounts in the given companies,
// in CLLIST order.
func (c *cur) ownAccounts(cos []string) map[string][]*dpAcct {
	out := map[string][]*dpAcct{}
	for i := range c.cm.dp {
		a := &c.cm.dp[i]
		if a.client != "" && contains(cos, a.co) {
			out[a.client] = append(out[a.client], a)
		}
	}
	return out
}

// primaryOwn is the oldest open account (by opening date, then BO ID; an
// account with no opening date last), else the oldest closed one.
func primaryOwn(as []*dpAcct) *dpAcct {
	if len(as) == 0 {
		return nil
	}
	s := append([]*dpAcct(nil), as...)
	sort.SliceStable(s, func(i, j int) bool {
		a, b := s[i], s[j]
		if a.isClosed() != b.isClosed() {
			return !a.isClosed()
		}
		if (a.opened == "") != (b.opened == "") {
			return a.opened != ""
		}
		if a.opened != b.opened {
			return a.opened < b.opened
		}
		return a.boid < b.boid
	})
	return s[0]
}

// clientDemat is a trading client's demat account in the client master:
// its open primary account in CDMAST, else its primary account at the
// firm's DP, else another open CDMAST account. other says the account is
// not at this DP (not in CLLIST); dropped counts the client's further
// CDMAST accounts at other DPs, which Ledgerline has no place for.
func (c *cur) clientDemat(u string, own []*dpAcct) (dm dpAcct, other bool, dropped int) {
	cds := c.cm.cdmast[u]
	var pick *cdRec
	for i := range cds {
		if cds[i].primary && !cds[i].closed {
			pick = &cds[i]
			break
		}
	}
	if pick == nil {
		if p := primaryOwn(own); p != nil {
			dm = dpAcct{boid: p.boid, depository: p.depository}
		} else {
			for i := range cds {
				if !cds[i].closed {
					pick = &cds[i]
					break
				}
			}
		}
	}
	if pick != nil {
		dm = dpAcct{boid: pick.boid, depository: pick.depository}
		other = !c.cm.boids[pick.boid]
	}
	seen := map[string]bool{dm.boid: true}
	for _, x := range cds {
		if !x.closed && !seen[x.boid] && !c.cm.boids[x.boid] {
			dropped++
		}
		seen[x.boid] = true
	}
	return dm, other, dropped
}

// writeDPOnlyClient writes a DP-only client's CLIENT-MASTER row (false when
// it is left out: a closed account in dormant mode skip).
func (c *cur) writeDPOnlyClient(cw *pkg.CSV, h []string, cl *client, pans map[string]string) (bool, error) {
	u, a := cl.ucc, cl.dp
	v := c.dpOnlyRow(cl)
	if a.isClosed() {
		if c.plan.DormantMode == "skip" {
			c.skipped[u] = true
			c.dpOnlySkipped++
			return false, nil
		}
		c.inactive[u] = cl.closeOut
		v["Inactive"], v["InactiveSince"] = "Y", cl.closeOut
		c.dpOnlyClosed++
	}
	c.dpOnlyN++
	c.demat[u] = a.boid
	if c.cm.dpDet[a.co+"|"+a.ben].income != "" {
		c.dpOnlyIncome++
	}
	if a.note != "" {
		c.rep.warnf("Client master", u, "dp-only-code", "Use this code for the client in Ledgerline, or renumber it there", "DP account %s has no trading client: %s, so the DP-only client's code is %s", a.boid, a.note, u)
	}
	c.validateClient(cl, v, pans)
	if err := cw.Write(rec(h, v)); err != nil {
		return false, err
	}
	c.written[u] = true
	return true, nil
}

func (c *cur) dpOnlyRow(cl *client) map[string]string {
	a := cl.dp
	k := c.cm.kycByPAN[cl.pan]
	pb := c.primaryBank(cl)
	nom, nomName, nomDate := c.nomination(cl)
	return map[string]string{
		"UCC": cl.ucc, "Name": cl.name, "Type": dpClientType(cl), "PAN": cl.pan, "DOB": cl.dob, "Mobile": cleanMobile(cl.mobile), "Email": cl.email,
		"City": cl.city, "Risk": riskOf(cl.risk), "Bank": pb.bank, "IFSC": pb.ifsc, "Account": pb.account, "Depository": a.depository, "DematID": a.boid,
		"Nominee": nom, "NomineeName": nomName, "KYCDate": firstNonEmpty(k.active, k.commence, a.opened), "Status": "Active", "API": "N",
		"Branch": cl.branch, "State": c.stateName(cl.statecd, cl.state), "NominationDate": nomDate, "Groups": strings.Join(c.cm.groups[cl.ucc], ";"), "DPOnly": "Y",
	}
}

// dpOnlyUCC is a DP-only client's UCC-DETAILS row: the first holder's
// address from CLLIST (BEN1ADD…, else CADD…), the permanent or foreign
// address (BEN1FADD…), the KYC details from CLLIST_DET. marital says
// whether CLLIST_DET has a marital status (not mapped, as for trading
// clients).
func (c *cur) dpOnlyUCC(cl *client, poa map[string][]string) (v map[string]string, marital bool) {
	a, h := cl.dp, cl.dp.h
	det := c.cm.dpDet[a.co+"|"+a.ben]
	add, city, pin, country := h.add, h.city, h.pin, h.country
	if add[0] == "" && h.cadd[0] != "" {
		add, city, pin, country = h.cadd, firstNonEmpty(h.ccity, h.city), firstNonEmpty(h.cpin, h.pin), firstNonEmpty(h.ccountry, h.country)
	}
	if indiaCountry(country) && country != "" {
		country = "India"
	}
	v = map[string]string{"UCC": cl.ucc, "Address1": add[0], "Address2": add[1], "Address3": strings.Join(strings.Fields(add[2]+" "+add[3]), " "),
		"City": city, "PIN": pin, "State": c.stateName(h.statecd, h.state), "Country": titleCase(country),
		"PermAddress1": h.padd[0], "PermAddress2": h.padd[1], "PermAddress3": h.padd[2], "PermCity": h.pcity, "PermPIN": h.ppin,
		"PermState": c.stateName(h.pstate, h.pstate), "PermCountry": titleCase(h.pcountry),
		"Gender": genderName(firstNonEmpty(h.sex, det.sex)), "Guardian": h.fath1, "Nationality": nationality(det.nation, det.nationOth),
		"Occupation": occupation(det.occup, det.occupText), "PEP": pepFlag(det.pep), "IPV": det.ipv, "AgreementDate": a.opened,
		"BankType": bankType(c.primaryBank(cl).typ)}
	if v["Occupation"] != "" && v["Occupation"] != strings.TrimSpace(det.occupText) {
		v["OccupationDetail"] = det.occupText
	}
	if c.s.has("CLPOALIST") {
		v["POASecurities"] = yn(len(poa[a.co+"|"+a.ben]) > 0)
	}
	if reCIN.MatchString(det.regNo) {
		v["CIN"] = det.regNo
	}
	return v, det.marital != ""
}

// dpKRAStatus reads CLLIST_DET.KRASTATDES, the KRA's status as the DP
// recorded it [assumed].
func dpKRAStatus(desc string) string {
	if s := kraStatus(desc); s != "" {
		return s
	}
	d := strings.ToUpper(desc)
	switch {
	case strings.Contains(d, "VALIDATED"):
		return "Validated"
	case strings.Contains(d, "REGISTERED"), strings.Contains(d, "VERIFIED"):
		return "Registered"
	case strings.Contains(d, "HOLD"):
		return "On hold"
	case strings.Contains(d, "REJECT"):
		return "Rejected"
	case strings.Contains(d, "PROCESS"), strings.Contains(d, "PENDING"), strings.Contains(d, "SUBMITTED"):
		return "Under process"
	}
	return ""
}

// ---- SQL for the checks ----

// noTradingSQL: a CLLIST account (alias a) with no trading client, as the
// reader decides it (SUBCODE blank or not a client in DLMAST).
func (c *cur) noTradingSQL(a string) string {
	if !c.s.hasCol("CLLIST", "SUBCODE") {
		return "1 = 1"
	}
	return fmt.Sprintf("NOT EXISTS (SELECT 1 FROM DLMAST d WHERE RTRIM(d.SUBCD) = RTRIM(%[1]s.SUBCODE) AND RTRIM(d.SUBCD) <> '')", a)
}

// closedSQL: a closed CLLIST account (BENCLOSEDT set or BENSTAT 3).
func (c *cur) closedSQL(a string) string {
	var or []string
	if c.s.hasCol("CLLIST", "BENCLOSEDT") {
		or = append(or, a+".BENCLOSEDT >= '1901-01-01'")
	}
	if c.s.hasCol("CLLIST", "BENSTAT") {
		or = append(or, "RTRIM(CAST("+a+".BENSTAT AS varchar(10))) = '3'")
	}
	if len(or) == 0 {
		return "(1 = 0)"
	}
	return "(" + strings.Join(or, " OR ") + ")"
}

// dpExcl leaves out of the DP checks (CLLIST alias a) the accounts of
// clients left out of the client master: dormant trading clients and, in
// dormant mode skip, closed DP-only accounts.
func (c *cur) dpExcl(a string) string {
	var tr []string
	for _, u := range sortedKeys(c.skipped) {
		if cl := c.cm.clients[u]; cl == nil || !cl.dpOnly {
			tr = append(tr, u)
		}
	}
	out := ""
	if len(tr) > 0 && c.s.hasCol("CLLIST", "SUBCODE") {
		out += fmt.Sprintf(" AND ISNULL(RTRIM(%s.SUBCODE), '') NOT IN (%s)", a, inList(tr))
	}
	if c.plan.DormantMode == "skip" {
		out += fmt.Sprintf(" AND NOT (%s AND %s)", c.noTradingSQL(a), c.closedSQL(a))
	}
	return out
}
