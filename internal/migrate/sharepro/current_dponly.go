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

var reCDSLBO = regexp.MustCompile(`^[0-9]{16}$`)

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

// cdRec is a trading client's demat account in CDMAST; bad says why no
// account ID could be made (the row is not used).
type cdRec struct {
	boid, depository, bad string
	primary, closed       bool
}

// shut: the row is closed, or the account is the firm's DP's and closed
// there (CLLIST).
func (x cdRec) shut(cm *common) bool { return x.closed || cm.boClosed[x.boid] }

// cdmastHolders lists, for each BO ID, the trading clients that name it in
// CDMAST and whose PAN is pan's, sorted by code.
func cdmastHolders(cm *common) map[string][]string {
	out := map[string][]string{}
	for _, u := range sortedKeys(cm.cdmast) {
		cl := cm.clients[u]
		if cl == nil || cl.pan == "" {
			continue
		}
		for _, x := range cm.cdmast[u] {
			if x.bad == "" && !contains(out[x.boid], u) {
				out[x.boid] = append(out[x.boid], u)
			}
		}
	}
	return out
}

// loadDPOnly links each CLLIST account to its client and makes a client of
// every account with no trading client.
func loadDPOnly(ctx context.Context, d db, s *schema, cm *common) error {
	used := map[string]bool{}
	for u := range cm.clients {
		used[u] = true
	}
	var only []*dpAcct
	listed := cdmastHolders(cm)
	for i := range cm.dp {
		a := &cm.dp[i]
		if a.ucc != "" && cm.clients[a.ucc] != nil {
			a.client = a.ucc
			continue
		}
		if a.ucc != "" {
			a.sub, a.ucc = a.ucc, "" // a trading code that is not a client
		}
		// no trading code: CLLIST.TRADINGID, when it is a client, links the
		// account to its trading client [verified: set on a few accounts with
		// no SUBCODE]
		if a.tid != "" && cm.clients[a.tid] != nil {
			a.ucc, a.client, a.sub, a.byTID = a.tid, a.tid, "", true
			continue
		}
		// else the trading client that lists the account as its demat
		// account in CDMAST and has the first holder's PAN (the smallest
		// code when the person has several) [verified: most accounts with
		// no SUBCODE, above all closed ones, are listed so]
		if a.h != nil && a.h.pan1 != "" {
			var same []string
			for _, u := range listed[a.boid] {
				if cm.clients[u].pan == a.h.pan1 {
					same = append(same, u)
				}
			}
			if len(same) > 0 {
				a.ucc, a.client, a.sub, a.byCD = same[0], same[0], "", len(same)
				continue
			}
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
// the DP's ID from DPMAST) [verified: TYPE Primary, Secondary, Pool (the
// firm's own, left out); CLIENTID 8 digits; DPCODE looked up in DPMAST].
func loadClientDemat(ctx context.Context, d db, s *schema, cm *common) error {
	if !s.hasCol("CDMAST", "SUBCD") || !s.hasCol("CDMAST", "CLIENTID") {
		return nil
	}
	list, miss := s.sel("CDMAST", "c", "SUBCD", "TYPE", "DPCODE", "CLIENTID", "Closed")
	cm.missing = append(cm.missing, miss...)
	join := ""
	if s.hasCol("CDMAST", "DPCODE") && s.hasCol("DPMAST", "DPCODE") && s.hasCol("DPMAST", "DPID") {
		list += ", RTRIM(p.DPID) AS [dpid]"
		if s.hasCol("DPMAST", "DESCR") {
			list += ", RTRIM(p.DESCR) AS [dpname]"
		} else {
			list += ", NULL AS [dpname]"
		}
		join = " LEFT JOIN DPMAST p ON p.DPCODE = c.DPCODE"
	} else {
		list += ", NULL AS [dpid], NULL AS [dpname]"
	}
	return d.query(ctx, fmt.Sprintf(qClientDemat, list, join), func(r migrate.Row) error {
		typ := upper(r, "type")
		if typ == "POOL" {
			return nil // the firm's own
		}
		x := cdRec{primary: typ == "PRIMARY", closed: flag(r, "closed")}
		x.boid, x.depository, x.bad = cdmastDemat(upper(r, "dpid"), upper(r, "clientid"), upper(r, "dpname"), str(r, "dpcode"))
		u := upper(r, "subcd")
		cm.cdmast[u] = append(cm.cdmast[u], x)
		return nil
	})
}

var (
	reDigits8   = regexp.MustCompile(`^[0-9]{8}$`)
	reNSDLDemat = regexp.MustCompile(`^IN[A-Z0-9]{6}[0-9]{8}$`)
)

// cdmastDemat makes the demat account ID of a CDMAST row: CDSL the DP's ID
// (8 digits) and the client ID (8 digits), 16 digits; NSDL IN, the DP's
// 6-character ID and the 8-digit client ID. bad says why none can be made.
func cdmastDemat(dpid, clientid, dpname, dpcode string) (boid, depository, bad string) {
	dpid, clientid = strings.ReplaceAll(dpid, " ", ""), strings.ReplaceAll(clientid, " ", "")
	switch {
	case reCDSLBO.MatchString(clientid): // the whole BO ID in CLIENTID
		return clientid, "CDSL", ""
	case reNSDLDemat.MatchString(clientid):
		return clientid, "NSDL", ""
	case !reDigits8.MatchString(clientid):
		return "", "", fmt.Sprintf("client ID %q is not 8 digits", clientid)
	case dpid == "":
		return "", "", fmt.Sprintf("DP code %q is not in the DP master (DPMAST)", dpcode)
	case strings.HasPrefix(dpid, "IN"):
		boid, depository = dpid+clientid, "NSDL"
	case len(dpid) == 6 && strings.Contains(dpname, "NSDL"):
		boid, depository = "IN"+dpid+clientid, "NSDL"
	default:
		boid, depository = dpid+clientid, "CDSL"
	}
	if (depository == "CDSL" && !reCDSLBO.MatchString(boid)) || (depository == "NSDL" && !reNSDLDemat.MatchString(boid)) {
		return "", "", fmt.Sprintf("DP ID %q (DP code %q) and client ID %q do not make a %s account ID", dpid, dpcode, clientid, depository)
	}
	return boid, depository, ""
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
// its open primary account in CDMAST, else its oldest open account at the
// firm's DP, else another open CDMAST account, else (all closed) its oldest
// account at the firm's DP. An account closed at the firm's DP (CLLIST)
// counts as closed whatever CDMAST says. other says the account is not at
// this DP (not in CLLIST).
func (c *cur) clientDemat(u string, own []*dpAcct) (dm dpAcct, other bool) {
	var cds []cdRec
	for _, x := range c.cm.cdmast[u] {
		if x.bad == "" {
			cds = append(cds, x)
		}
	}
	// an account closed at the firm's DP is not picked while the client
	// has an open one
	var pick *cdRec
	for i := range cds {
		if cds[i].primary && !cds[i].shut(c.cm) {
			pick = &cds[i]
			break
		}
	}
	if pick == nil {
		if p := primaryOwn(own); p != nil && !p.isClosed() {
			dm = dpAcct{boid: p.boid, depository: p.depository}
		} else {
			for i := range cds {
				if !cds[i].shut(c.cm) {
					pick = &cds[i]
					break
				}
			}
			if pick == nil && p != nil {
				dm = dpAcct{boid: p.boid, depository: p.depository} // all closed: the oldest
			}
		}
	}
	if pick != nil {
		dm = dpAcct{boid: pick.boid, depository: pick.depository}
		other = !c.cm.boids[pick.boid]
	}
	return dm, other
}

// writeClientDemats writes CLIENT-DEMATS: every demat account of each
// migrated client in CDMAST (Pool rows left out). A client's rows of the
// same account are one row; Primary Y is the client master's demat account
// (an open TYPE Primary row; a second open primary is reported); Status
// Inactive for a closed row. A row whose account ID cannot be made is left
// out with a warning (an error for an active client's open primary). An
// account closed at the firm's DP is Inactive here (DP-ACCOUNTS closes it).
func (c *cur) writeClientDemats(ctx context.Context) error {
	area := "Client demat accounts"
	h := headers[srcDemats]
	cw, err := c.w.Master(c.f.Code, srcDemats, h)
	if err != nil {
		return err
	}
	n, clients, dups := 0, 0, 0
	for _, u := range c.clientList() {
		rows := c.cm.cdmast[u]
		if !c.written[u] || len(rows) == 0 {
			continue
		}
		type acct struct {
			dep           string
			primary, open bool
		}
		by, order := map[string]*acct{}, []string(nil)
		primaries := map[string]bool{}
		for _, x := range rows {
			if x.bad != "" {
				_, inactive := c.inactive[u]
				if x.primary && !x.closed && !inactive {
					c.rep.errorf(area, u, "bad-demat", "Correct the client's demat account in SharePro (CDMAST, DPMAST) before loading", "The client's primary demat account (CDMAST) cannot be made into an account ID: %s", x.bad)
				} else {
					c.rep.warnf(area, u, "bad-demat-other", "Add the demat account in Ledgerline if the client still uses it", "A demat account of the client (CDMAST) cannot be made into an account ID and is left out: %s", x.bad)
				}
				continue
			}
			a := by[x.boid]
			if a == nil {
				a = &acct{dep: x.depository}
				by[x.boid] = a
				order = append(order, x.boid)
			} else {
				dups++
			}
			a.open = a.open || !x.shut(c.cm)
			if x.primary && !x.shut(c.cm) {
				a.primary = true
				primaries[x.boid] = true
			}
		}
		if len(primaries) > 1 {
			c.rep.warnf(area, u, "two-primaries", "Keep one primary demat account in Ledgerline", "The client has %d open primary demat accounts in CDMAST; the client master's (%s) is the primary", len(primaries), c.demat[u])
		}
		for _, id := range order {
			a := by[id]
			status := "Active"
			if !a.open {
				status = "Inactive"
			}
			if err := cw.Write(rec(h, map[string]string{"UCC": u, "Depository": a.dep, "DematID": id, "Primary": yn(a.primary && id == c.demat[u]), "Status": status})); err != nil {
				cw.Close()
				return err
			}
			n++
		}
		if len(order) > 0 {
			clients++
		}
	}
	if err := cw.Close(); err != nil {
		return err
	}
	if n > 0 {
		c.rep.infof(area, "", "demats", "%d demat accounts of %d clients from SharePro's client demat master (CDMAST), at this DP and others, are in CLIENT-DEMATS", n, clients)
	}
	if dups > 0 {
		c.rep.infof(area, "", "dup-demats", "%d CDMAST rows repeat an account the client already has (one row per account is written)", dups)
	}
	return nil
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

// isClientSQL: column col of the CLLIST row (alias a) is a client in DLMAST.
func isClientSQL(a, col string) string {
	return fmt.Sprintf("EXISTS (SELECT 1 FROM DLMAST dm WHERE RTRIM(dm.SUBCD) = RTRIM(%s.%s) AND RTRIM(dm.SUBCD) <> '')", a, col)
}

// noCodeSQL: neither SUBCODE nor TRADINGID of the CLLIST account (alias
// a) is a client in DLMAST.
func (c *cur) noCodeSQL(a string) string {
	var not []string
	for _, col := range []string{"SUBCODE", "TRADINGID"} {
		if c.s.hasCol("CLLIST", col) {
			not = append(not, "NOT "+isClientSQL(a, col))
		}
	}
	if len(not) == 0 {
		return "1 = 1"
	}
	return strings.Join(not, " AND ")
}

// noTradingSQL: a CLLIST account (alias a) with no trading client, as the
// reader decides it (neither SUBCODE nor TRADINGID is a client in DLMAST,
// and no trading client of the first holder's PAN lists it in CDMAST).
func (c *cur) noTradingSQL(a string) string {
	out := c.noCodeSQL(a)
	if cd := c.cdmastCodeSQL(a); cd != "" {
		out += " AND " + cd + " IS NULL"
	}
	return out
}

// boidSQL is the CLLIST account's BO ID as the reader makes it.
func boidSQL(a string) string {
	return fmt.Sprintf("CASE WHEN RTRIM(ISNULL(%[1]s.FBENACNUM, '')) <> '' THEN RTRIM(%[1]s.FBENACNUM) ELSE RTRIM(%[1]s.DPID) + RTRIM(%[1]s.BENACNUM) END", a)
}

// cdmastCodeSQL is the trading client that lists the CLLIST account (alias
// a) as its demat account in CDMAST and whose PAN is the first holder's,
// the smallest code when several are (as loadDPOnly picks it); NULL when
// none. Empty when the tables lack the columns.
func (c *cur) cdmastCodeSQL(a string) string {
	for _, tc := range [][2]string{{"CDMAST", "SUBCD"}, {"CDMAST", "CLIENTID"}, {"CLLIST", "ITPAN"}, {"CLLIST", "FBENACNUM"}, {"CLLIST", "DPID"}, {"DLMAST", "PAN_NO"}} {
		if !c.s.hasCol(tc[0], tc[1]) {
			return ""
		}
	}
	id := "REPLACE(RTRIM(cd.CLIENTID), ' ', '')"
	match := id + " = " + boidSQL(a)
	join := ""
	if c.s.hasCol("CDMAST", "DPCODE") && c.s.hasCol("DPMAST", "DPCODE") && c.s.hasCol("DPMAST", "DPID") {
		join = " LEFT JOIN DPMAST cp ON cp.DPCODE = cd.DPCODE"
		match = fmt.Sprintf("(%s OR (LEN(%s) = 8 AND REPLACE(RTRIM(cp.DPID), ' ', '') + %s = %s))", match, id, id, boidSQL(a))
	}
	if c.s.hasCol("CDMAST", "TYPE") {
		match += " AND UPPER(RTRIM(ISNULL(cd.TYPE, ''))) <> 'POOL'"
	}
	return fmt.Sprintf("(SELECT MIN(UPPER(RTRIM(cd.SUBCD))) FROM CDMAST cd%s JOIN DLMAST cdm ON RTRIM(cdm.SUBCD) = RTRIM(cd.SUBCD) AND RTRIM(cdm.SUBCD) <> '' WHERE %s AND UPPER(LTRIM(RTRIM(cdm.PAN_NO))) = UPPER(LTRIM(RTRIM(%s.ITPAN))) AND LTRIM(RTRIM(ISNULL(%s.ITPAN, ''))) <> '')",
		join, match, a, a)
}

// dpOwnerSQL lists each DP client code (BENACNUM) of the firm's DP
// accounts with its client as the trial balance checks key it: 'C|' + the
// trading code (SUBCODE, else TRADINGID, when a client, else the trading
// client of the first holder's PAN listing it in CDMAST), else 'D|' + the
// DP client code (a DP-only client). A code of two clients is left out, as
// the reader leaves it.
func (c *cur) dpOwnerSQL() string {
	u := "'D|' + RTRIM(l.BENACNUM)"
	if cd := c.cdmastCodeSQL("l"); cd != "" {
		u = fmt.Sprintf("CASE WHEN %[1]s IS NOT NULL THEN 'C|' + %[1]s ELSE %[2]s END", cd, u)
	}
	for _, col := range []string{"TRADINGID", "SUBCODE"} { // innermost first
		if c.s.hasCol("CLLIST", col) {
			u = fmt.Sprintf("CASE WHEN %s THEN 'C|' + RTRIM(l.%s) ELSE %s END", isClientSQL("l", col), col, u)
		}
	}
	return fmt.Sprintf("SELECT o.ben, MIN(o.u) AS u FROM (SELECT RTRIM(l.BENACNUM) AS ben, %s AS u FROM CLLIST l WHERE l.CO IN (%s) AND RTRIM(l.BENACNUM) <> '') o GROUP BY o.ben HAVING MIN(o.u) = MAX(o.u)",
		u, inList(c.f.dpCOs()))
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
		if c.s.hasCol("CLLIST", "TRADINGID") {
			// an account linked by TRADINGID (its SUBCODE no client)
			out += fmt.Sprintf(" AND NOT (NOT %s AND ISNULL(RTRIM(%s.TRADINGID), '') IN (%s))", isClientSQL(a, "SUBCODE"), a, inList(tr))
		}
		if cd := c.cdmastCodeSQL(a); cd != "" {
			// an account linked by CDMAST (neither code a client)
			out += fmt.Sprintf(" AND NOT (%s AND ISNULL(%s, '') IN (%s))", c.noCodeSQL(a), cd, inList(tr))
		}
	}
	if c.plan.DormantMode == "skip" {
		out += fmt.Sprintf(" AND NOT (%s AND %s)", c.noTradingSQL(a), c.closedSQL(a))
	}
	return out
}
