package sharepro

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

// The exchanges' UCC details, contact persons of non-individual clients,
// branches, authorised persons and RMs, and DP accounts.

const (
	qUCCDetails = `/*sp:ucc-details*/ SELECT %s FROM DLMAST d%s`
	qKYCDetail  = `/*sp:kyc-detail*/ SELECT %s, ROW_NUMBER() OVER (PARTITION BY KYCCD ORDER BY %s DESC) AS rn FROM KYCDTL`
	qKYCAddr    = `/*sp:kyc-address*/ SELECT %s, ROW_NUMBER() OVER (PARTITION BY KYCCD, %s ORDER BY %s DESC) AS rn FROM KYCADD`
	qPOA        = `/*sp:dp-poa*/ SELECT %s FROM CLPOALIST`
	qFreezeRsn  = `/*sp:freeze-reasons*/ SELECT FREEZERSN AS code, RTRIM(DESCR) AS descr FROM FREEZERSN`
	qContacts   = `/*sp:contacts*/ SELECT %s FROM DLMAST d%s`
	qHUFCoper   = `/*sp:huf-coparceners*/ SELECT %s FROM HUFCOPER`
	qDPAuth     = `/*sp:dp-authorised*/ SELECT %s FROM CLAUTHMAST`
	qBranchesA  = `/*sp:branches*/ SELECT %s FROM BRANCHES ORDER BY BranchId`
	qSubBrokers = `/*sp:sub-brokers*/ SELECT %s FROM SBMAST ORDER BY SBCODE`
	qRMs        = `/*sp:rms*/ SELECT %s FROM RMMAST ORDER BY rmcode`
	qDealers    = `/*sp:dealers*/ SELECT %s FROM DLRMAST ORDER BY DLRCODE`
	qBankNames  = `/*sp:bank-names*/ SELECT RTRIM(BANKCD) AS bankcd, RTRIM(DESCR) AS descr, RTRIM(RTGS) AS ifsc FROM BANKMAST`
	qCheckDP    = `/*sp:check-dp-accounts*/ SELECT COUNT(*) AS n FROM CLLIST WHERE CO IN (%s)%s`
)

var (
	rePIN = regexp.MustCompile(`^[1-9][0-9]{5}$`)
	reCIN = regexp.MustCompile(`^[LU][0-9]{5}[A-Z]{2}[0-9]{4}[A-Z]{3}[0-9]{6}$`)
)

func genderName(g string) string {
	switch strings.ToUpper(strings.TrimSpace(g)) {
	case "M", "MALE":
		return "Male"
	case "F", "FEMALE":
		return "Female"
	case "T", "TG", "TRANSGENDER", "O", "OTHER":
		return "Transgender"
	}
	return ""
}

// ckycOccupation names the CKYC occupation codes of KYCDTL.OCCUP_CODE
// (verified against a real database).
var ckycOccupation = map[string]string{
	"S01": "Private Sector Service", "S02": "Public Sector Service", "S03": "Government Service", "B01": "Business",
	"O01": "Professional", "O02": "Self Employed", "O03": "Retired", "O04": "Housewife", "O05": "Student", "X01": "Not categorised",
}

func ckycOccupationName(code string) string {
	return ckycOccupation[strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code))]
}

func nationality(code, other string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "":
		return ""
	case "01", "1", "I", "IN", "IND", "INDIA", "INDIAN":
		return "Indian"
	case "02", "2":
		return firstNonEmpty(other, "Other")
	}
	return code
}

// occupation: the exchange's code (1 to 9, 99) when SharePro holds one,
// else the word.
func occupation(code, word string) string {
	c := strings.TrimLeft(strings.TrimSpace(code), "0")
	switch c {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "99":
		return c
	}
	return strings.TrimSpace(word)
}

func pepFlag(v string) string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "":
		return ""
	case "Y", "YES", "1", "PEP", "RPEP", "01", "02":
		return "Y"
	}
	return "N"
}

type kycDetail struct {
	gender, marital, father, spouse, citizen, occup string
}

type kycAddr struct {
	a1, a2, a3, city, state, pin, country string
}

func (c *cur) stateName(code, text string) string {
	if n := c.cm.states[strings.ToUpper(strings.TrimSpace(code))]; n != "" {
		return n
	}
	if n := c.cm.states[strings.ToUpper(strings.TrimSpace(text))]; n != "" {
		return n
	}
	return firstNonEmpty(text, code)
}

// poaRows reads the DP POA / DDPI register: co|benacnum -> active types.
func (c *cur) poaRows(ctx context.Context) (map[string][]string, error) {
	out := map[string][]string{}
	if !c.s.has("CLPOALIST") {
		return out, nil
	}
	list, _ := c.s.sel("CLPOALIST", "", "co", "benacnum", "PoaType", "PoaDeacDt")
	err := c.d.query(ctx, fmt.Sprintf(qPOA, list), func(r migrate.Row) error {
		if date(r, "poadeacdt") != "" {
			return nil
		}
		k := str(r, "co") + "|" + upper(r, "benacnum")
		out[k] = append(out[k], firstNonEmpty(upper(r, "poatype"), "POA"))
		return nil
	})
	return out, err
}

func (c *cur) writeUCCDetails(ctx context.Context) error {
	area := "UCC details"
	h := headers[srcUCCDetails]
	// KYC register, by KYC code
	kd, ka, kp := map[string]kycDetail{}, map[string]kycAddr{}, map[string]kycAddr{}
	if c.s.has("KYCDTL") {
		list, _ := c.s.sel("KYCDTL", "", "KYCCD", "GENDER", "MARITAL", "FAFNAME", "FAMNAME", "FALNAME", "SPFNAME", "SPMNAME", "SPLNAME", "COCN", "OCCUP_CODE")
		if err := c.d.query(ctx, fmt.Sprintf(qKYCDetail, list, c.s.order("KYCDTL", "", "EFFECTDATE")), func(r migrate.Row) error {
			if i64(r, "rn") == 1 {
				kd[upper(r, "kyccd")] = kycDetail{gender: str(r, "gender"), marital: str(r, "marital"), citizen: str(r, "cocn"), occup: str(r, "occup_code"),
					father: strings.Join(strings.Fields(str(r, "fafname")+" "+str(r, "famname")+" "+str(r, "falname")), " "),
					spouse: strings.Join(strings.Fields(str(r, "spfname")+" "+str(r, "spmname")+" "+str(r, "splname")), " ")}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if c.s.has("KYCADD") {
		part := c.s.order("KYCADD", "", "TYPE", "ADDTYPE")
		list, _ := c.s.sel("KYCADD", "", "KYCCD", "TYPE", "ADDTYPE", "ADD1", "ADD2", "ADD3", "CITY", "STATE", "PIN", "COA")
		if err := c.d.query(ctx, fmt.Sprintf(qKYCAddr, list, part, c.s.order("KYCADD", "", "EFFECTDATE")), func(r migrate.Row) error {
			if i64(r, "rn") != 1 {
				return nil
			}
			a := kycAddr{str(r, "add1"), str(r, "add2"), str(r, "add3"), str(r, "city"), str(r, "state"), str(r, "pin"), str(r, "coa")}
			t := upper(r, "type") + " " + upper(r, "addtype")
			if strings.Contains(t, "PER") || strings.TrimSpace(t) == "P" {
				kp[upper(r, "kyccd")] = a
			} else if _, ok := ka[upper(r, "kyccd")]; !ok {
				ka[upper(r, "kyccd")] = a
			}
			return nil
		}); err != nil {
			return err
		}
	}
	poa, err := c.poaRows(ctx)
	if err != nil {
		return err
	}
	clientPOA := map[string]bool{}
	for _, a := range c.cm.dp {
		if a.ucc != "" && len(poa[a.co+"|"+a.ben]) > 0 {
			clientPOA[a.ucc] = true
		}
	}
	dl, _ := c.s.sel("DLMAST", "d", "SUBCD", "ADD1", "ADD2", "ADD3", "CITY", "PIN", "STATE", "STATECD", "COUNTRY", "RES_ADD1", "RES_ADD2", "RES_ADD3",
		"RES_CITY", "RES_PIN", "GENDER", "FATHERNM", "OCCUPATION", "code_Occupation", "CLIENTCAT", "REG_NO", "COMMNCEDT")
	join := ""
	if c.s.has("DLAGREE") {
		al, _ := c.s.sel("DLAGREE", "a", "KycCd", "PermState", "PermCountry", "CorState", "CorCountry", "MaritalStatus", "Nation", "NationOth",
			"OccupDtl", "OccupDtlOth", "IncomeDt", "PepConn", "IpvDt", "ApplDt")
		dl += ", " + al
		join = " LEFT JOIN DLAGREE a ON a.Subcd = d.SUBCD"
	}
	cw, err := c.w.Master(c.f.Code, srcUCCDetails, h)
	if err != nil {
		return err
	}
	marital := 0
	err = c.d.query(ctx, fmt.Sprintf(qUCCDetails, dl, join), func(r migrate.Row) error {
		u := upper(r, "subcd")
		if !c.written[u] {
			return nil
		}
		cl := c.cm.clients[u]
		kc := firstNonEmpty(upper(r, "kyccd"), c.cm.kycByPAN[cl.pan].kyccd)
		k, a, pa := kd[kc], ka[kc], kp[kc]
		v := map[string]string{"UCC": u, "Category": str(r, "clientcat"),
			"Address1": str(r, "add1"), "Address2": str(r, "add2"), "Address3": str(r, "add3"), "City": str(r, "city"), "PIN": str(r, "pin"),
			"State":        c.stateName(str(r, "statecd"), firstNonEmpty(str(r, "state"), str(r, "corstate"))),
			"Country":      titleCase(firstNonEmpty(str(r, "country"), str(r, "corcountry"))),
			"PermAddress1": str(r, "res_add1"), "PermAddress2": str(r, "res_add2"), "PermAddress3": str(r, "res_add3"), "PermCity": str(r, "res_city"), "PermPIN": str(r, "res_pin"),
			"PermState": c.stateName(str(r, "permstate"), str(r, "permstate")), "PermCountry": titleCase(str(r, "permcountry")),
			"Gender":           genderName(firstNonEmpty(str(r, "gender"), k.gender)),
			"Guardian":         firstNonEmpty(str(r, "fathernm"), k.father, k.spouse),
			"Nationality":      nationality(firstNonEmpty(str(r, "nation"), k.citizen), str(r, "nationoth")),
			"Occupation":       occupation(str(r, "code_occupation"), firstNonEmpty(ckycOccupationName(k.occup), str(r, "occupation"))),
			"OccupationDetail": firstNonEmpty(str(r, "occupdtloth"), str(r, "occupdtl")),
			"IncomeAsOn":       date(r, "incomedt"), "PEP": pepFlag(str(r, "pepconn")),
			"AgreementDate": firstNonEmpty(date(r, "appldt"), date(r, "commncedt")),
			"BankType":      bankType(c.primaryBank(cl).typ),
		}
		if date(r, "ipvdt") != "" {
			v["IPV"] = "Y"
		}
		// SharePro's marital codes (1, 2, 01, 02) are not known: left blank
		if firstNonEmpty(str(r, "maritalstatus"), k.marital) != "" {
			marital++
		}
		if v["OccupationDetail"] == "" && ckycOccupationName(k.occup) != "" && v["Occupation"] == ckycOccupationName(k.occup) {
			v["OccupationDetail"] = k.occup
		}
		if c.s.has("CLPOALIST") {
			v["POASecurities"] = yn(clientPOA[u])
		}
		if c.s.hasCol("BKMAST", "POA_RECVD") {
			v["POAFunds"] = yn(c.bankPOA(u))
		}
		if reg := strings.ToUpper(str(r, "reg_no")); reCIN.MatchString(reg) {
			v["CIN"] = reg
		}
		// the KYC register's addresses where the client master has none
		if v["Address1"] == "" && a.a1 != "" {
			v["Address1"], v["Address2"], v["Address3"], v["City"], v["PIN"] = a.a1, a.a2, a.a3, a.city, a.pin
			v["State"] = c.stateName(a.state, a.state)
		}
		if v["PermAddress1"] == "" && pa.a1 != "" {
			v["PermAddress1"], v["PermAddress2"], v["PermAddress3"], v["PermCity"], v["PermPIN"] = pa.a1, pa.a2, pa.a3, pa.city, pa.pin
			v["PermState"] = c.stateName(pa.state, pa.state)
		}
		// PINs are often typed "700 001" or "700001."
		for _, f := range []string{"PIN", "PermPIN"} {
			v[f] = normPIN(v[f])
		}
		for _, f := range []string{"Address1", "City", "State", "PIN"} {
			if v[f] == "" {
				c.clientErr(u, area, "missing-"+f, "Add it in SharePro (client master or KYC) before loading", "%s is missing", f)
			}
		}
		if v["PIN"] != "" && !rePIN.MatchString(v["PIN"]) {
			c.rep.warnf(area, u, "bad-pin", "Correct the PIN in SharePro", "PIN %q is not 6 digits", v["PIN"])
		}
		return cw.Write(rec(h, v))
	})
	if err != nil {
		cw.Close()
		return err
	}
	if marital > 0 {
		c.rep.infof(area, "", "marital", "Marital status codes not mapped; confirm what 1 and 2 mean in SharePro (%d clients have one); Marital is left blank", marital)
	}
	c.rep.infof(area, "", "relations", "SharePro does not record whose mobile number and email they are (self, spouse, dependent...); MobileRelation and EmailRelation are empty")
	return cw.Close()
}

func (c *cur) bankPOA(u string) bool {
	for _, b := range c.cm.banks[u] {
		if b.poa && !b.closed {
			return true
		}
	}
	return false
}

// ---- contact persons ----

type contact struct{ ucc, name, desig, pan, addr, phone, email, din string }

func (c *cur) writeContacts(ctx context.Context) error {
	area := "Contact persons"
	hasSlots := c.s.hasCol("DLMAST", "CONTACT2") || c.s.hasCol("DLMAST", "C_PAN1")
	if !hasSlots && !c.s.has("HUFCOPER") && !c.s.has("CLAUTHMAST") {
		c.rep.infof(area, "", "no-table", "This SharePro database has no table of directors, partners, karta or authorised signatories; UCC-CONTACTS is not written")
		return nil
	}
	nonInd := func(u string) bool {
		cl := c.cm.clients[u]
		return c.written[u] && cl != nil && clientType(cl) != "Individual" && clientType(cl) != "NRI"
	}
	var out []contact
	if hasSlots {
		cols := []string{"SUBCD", "CONTACT", "DESIGNATN", "CONTACT2", "C_DESIG2", "CONTACT3", "C_DESIG3"}
		for i := 1; i <= 3; i++ {
			cols = append(cols, fmt.Sprintf("C_PAN%d", i), fmt.Sprintf("C_ADD%d", i), fmt.Sprintf("C_PHONE%d", i), fmt.Sprintf("C_EMAIL%d", i))
		}
		dl, _ := c.s.sel("DLMAST", "d", cols...)
		join := ""
		if c.s.has("DLAGREE") {
			var ac []string
			for i := 1; i <= 3; i++ {
				ac = append(ac, fmt.Sprintf("DinCont%d", i), fmt.Sprintf("C_City%d", i), fmt.Sprintf("C_Pin%d", i))
			}
			al, _ := c.s.sel("DLAGREE", "a", ac...)
			dl += ", " + al
			join = " LEFT JOIN DLAGREE a ON a.Subcd = d.SUBCD"
		}
		if err := c.d.query(ctx, fmt.Sprintf(qContacts, dl, join), func(r migrate.Row) error {
			u := upper(r, "subcd")
			if !nonInd(u) {
				return nil
			}
			names := []string{str(r, "contact"), str(r, "contact2"), str(r, "contact3")}
			desigs := []string{str(r, "designatn"), str(r, "c_desig2"), str(r, "c_desig3")}
			for i, n := range names {
				if n == "" {
					continue
				}
				j := fmt.Sprint(i + 1)
				addr := strings.Join(strings.Fields(strings.Join([]string{str(r, "c_add"+j), str(r, "c_city"+j), str(r, "c_pin"+j)}, " ")), " ")
				out = append(out, contact{ucc: u, name: n, desig: firstNonEmpty(desigs[i], "Authorised signatory"), pan: upper(r, "c_pan"+j), addr: addr,
					phone: str(r, "c_phone"+j), email: strings.ToLower(str(r, "c_email"+j)), din: str(r, "dincont"+j)})
			}
			return nil
		}); err != nil {
			return err
		}
	}
	dpUCC := map[string]string{}
	for _, a := range c.cm.dp {
		if a.ucc != "" {
			dpUCC[a.co+"|"+a.ben] = a.ucc
		}
	}
	if c.s.has("HUFCOPER") {
		list, _ := c.s.sel("HUFCOPER", "", "CO", "BenAcNum", "CoPerName", "KRTAREL", "Status")
		if err := c.d.query(ctx, fmt.Sprintf(qHUFCoper, list), func(r migrate.Row) error {
			u := dpUCC[str(r, "co")+"|"+upper(r, "benacnum")]
			if u == "" || !nonInd(u) || str(r, "copername") == "" {
				return nil
			}
			d := "Coparcener"
			if rel := upper(r, "krtarel"); strings.Contains(rel, "KARTA") || rel == "SELF" {
				d = "Karta"
			}
			out = append(out, contact{ucc: u, name: str(r, "copername"), desig: d})
			return nil
		}); err != nil {
			return err
		}
	}
	if c.s.has("CLAUTHMAST") {
		list, _ := c.s.sel("CLAUTHMAST", "", "Co", "BenAcNum", "AuthPerson", "MOBILE", "EMAIL")
		if err := c.d.query(ctx, fmt.Sprintf(qDPAuth, list), func(r migrate.Row) error {
			u := dpUCC[str(r, "co")+"|"+upper(r, "benacnum")]
			if u == "" || !nonInd(u) || str(r, "authperson") == "" {
				return nil
			}
			out = append(out, contact{ucc: u, name: str(r, "authperson"), desig: "Authorised signatory (DP)", phone: str(r, "mobile"), email: strings.ToLower(str(r, "email"))})
			return nil
		}); err != nil {
			return err
		}
	}
	h := headers[srcContacts]
	cw, err := c.w.Master(c.f.Code, srcContacts, h)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, x := range out {
		k := x.ucc + "|" + strings.ToUpper(x.name)
		if seen[k] {
			continue
		}
		seen[k] = true
		if x.pan != "" && !rePAN.MatchString(x.pan) {
			c.rep.warnf(area, x.ucc, "bad-pan", "Correct the contact person's PAN in SharePro", "PAN %q of %s is not a valid PAN", x.pan, x.name)
		}
		if err := cw.Write(rec(h, map[string]string{"UCC": x.ucc, "Name": x.name, "Designation": x.desig, "PAN": x.pan, "Address": x.addr, "Phone": x.phone, "Email": x.email, "DIN": x.din})); err != nil {
			cw.Close()
			return err
		}
	}
	return cw.Close()
}

// ---- branches ----

type branchRec struct {
	code, name, city, manager, parent, kind string
	active                                  bool
}

func branchKind(name string) string {
	n := strings.ToUpper(name)
	switch {
	case strings.Contains(n, "ZONE"):
		return "Zone"
	case strings.Contains(n, "REGION"):
		return "Region"
	case strings.Contains(n, "FRANCHIS"):
		return "Franchise"
	}
	return "Branch"
}

// loadBranches reads BRANCHES (admin database; some company databases
// have it too).
func loadBranches(ctx context.Context, d db) ([]branchRec, error) {
	s, err := loadSchema(ctx, d)
	if err != nil || !s.has("BRANCHES") {
		return nil, err
	}
	list, _ := s.sel("BRANCHES", "", "BranchId", "BranchName", "CITY", "Contact1", "PBranchId", "Status", "Dt_Close")
	var out []branchRec
	err = d.query(ctx, fmt.Sprintf(qBranchesA, list), func(r migrate.Row) error {
		b := branchRec{code: upper(r, "branchid"), name: str(r, "branchname"), city: str(r, "city"), manager: str(r, "contact1"), parent: upper(r, "pbranchid")}
		if b.parent == b.code {
			b.parent = ""
		}
		st := upper(r, "status")
		b.active = date(r, "dt_close") == "" && !strings.HasPrefix(st, "CLOS") && !strings.HasPrefix(st, "INACT") && !strings.HasPrefix(st, "SUSP")
		b.kind = branchKind(b.name)
		if b.code != "" {
			out = append(out, b)
		}
		return nil
	})
	return out, err
}

func (c *cur) writeBranches(ctx context.Context) error {
	area := "Branches"
	c.m.mu.Lock()
	brs := append([]branchRec(nil), c.m.branches...)
	c.m.mu.Unlock()
	if len(brs) == 0 && c.s.has("BRANCHES") {
		var err error
		if brs, err = loadBranches(ctx, c.d); err != nil {
			return err
		}
	}
	have := map[string]bool{}
	for _, b := range brs {
		have[b.code] = true
	}
	used := map[string]bool{}
	for _, u := range c.clientList() {
		if c.written[u] && c.cm.clients[u].branch != "" {
			used[strings.ToUpper(c.cm.clients[u].branch)] = true
		}
	}
	if len(brs) == 0 {
		for _, b := range sortedKeys(used) {
			brs = append(brs, branchRec{code: b, name: b, kind: "Branch", active: true})
		}
		if len(brs) > 0 {
			c.rep.warnf(area, "", "no-master", "Give the admin database, or add the branch names in Ledgerline", "The branch master (BRANCHES, in the admin database) was not given; %d branches are listed by code only", len(brs))
		}
	}
	for _, b := range sortedKeys(used) {
		if len(have) > 0 && !have[b] {
			c.rep.warnf(area, b, "unknown-branch", "Add the branch in SharePro's branch master or in Ledgerline", "Branch %s of the client master is not in the branch master", b)
		}
	}
	h := headers[srcBranches]
	cw, err := c.w.Master(c.f.Code, srcBranches, h)
	if err != nil {
		return err
	}
	for _, b := range brs {
		if err := cw.Write(rec(h, map[string]string{"Code": b.code, "Name": firstNonEmpty(b.name, b.code), "City": b.city, "Manager": b.manager, "Parent": b.parent,
			"Kind": b.kind, "Active": yn(b.active)})); err != nil {
			cw.Close()
			return err
		}
	}
	return cw.Close()
}

// ---- authorised persons and RMs ----

func (c *cur) writePartners(ctx context.Context) error {
	area := "Authorised persons and RMs"
	banks := map[string][2]string{}
	if c.s.has("BANKMAST") {
		_ = c.d.query(ctx, qBankNames, func(r migrate.Row) error {
			banks[upper(r, "bankcd")] = [2]string{str(r, "descr"), upper(r, "ifsc")}
			return nil
		})
	}
	h := headers[srcPartners]
	cw, err := c.w.Master(c.f.Code, srcPartners, h)
	if err != nil {
		return err
	}
	codes := map[string]string{}
	changed := 0
	defer func() {
		if changed > 0 {
			c.rep.infof(area, "", "codes-changed", "%d partner codes were not 3 to 10 letters or digits; they come across tidied (punctuation removed, short codes prefixed AP or RM), and the clients' AP and RM codes the same way", changed)
		}
	}()
	write := func(v map[string]string) error {
		prefix := "AP"
		if v["Kind"] != "Authorised person" {
			prefix = "RM"
		}
		if pc := partnerCode(v["Code"], prefix); pc != v["Code"] {
			changed++
			v["Code"] = pc
		}
		if v["Code"] == "" {
			return nil
		}
		if k, dup := codes[v["Code"]]; dup {
			c.rep.warnf(area, v["Code"], "dup-code", "Give one of them another code in Ledgerline", "Code %s is both a %s and a %s; only the first is written", v["Code"], strings.ToLower(k), strings.ToLower(v["Kind"]))
			return nil
		}
		codes[v["Code"]] = v["Kind"]
		if p := v["PAN"]; p != "" && !rePAN.MatchString(p) {
			c.rep.warnf(area, v["Code"], "bad-pan", "Correct the PAN in SharePro", "PAN %q is not a valid PAN", p)
			v["PAN"] = p
		}
		return cw.Write(rec(h, v))
	}
	if c.s.has("SBMAST") {
		list, _ := c.s.sel("SBMAST", "", "SBCODE", "BRANCHID", "DESCR", "SEBIREGNO", "BANKCD", "ACNO", "PAN_NO", "pan", "GSTIN", "email", "Dt_Close")
		if err := c.d.query(ctx, fmt.Sprintf(qSubBrokers, list), func(r migrate.Row) error {
			b := banks[upper(r, "bankcd")]
			return write(map[string]string{"Code": upper(r, "sbcode"), "Name": str(r, "descr"), "Kind": "Authorised person", "Branch": upper(r, "branchid"),
				"PAN": firstNonEmpty(upper(r, "pan_no"), upper(r, "pan")), "GSTIN": upper(r, "gstin"), "RegNo": str(r, "sebiregno"), "Bank": b[0], "IFSC": b[1],
				"Account": str(r, "acno"), "Email": strings.ToLower(str(r, "email")), "Active": yn(date(r, "dt_close") == "")})
		}); err != nil {
			cw.Close()
			return err
		}
	}
	if c.s.has("RMMAST") {
		list, _ := c.s.sel("RMMAST", "", "rmcode", "descr", "branchid", "email", "pan_no", "Bankcd", "Acno", "Dt_Close")
		if err := c.d.query(ctx, fmt.Sprintf(qRMs, list), func(r migrate.Row) error {
			b := banks[upper(r, "bankcd")]
			return write(map[string]string{"Code": upper(r, "rmcode"), "Name": str(r, "descr"), "Kind": "Relationship manager", "Branch": upper(r, "branchid"),
				"PAN": upper(r, "pan_no"), "Bank": b[0], "IFSC": b[1], "Account": str(r, "acno"), "Email": strings.ToLower(str(r, "email")), "Active": yn(date(r, "dt_close") == "")})
		}); err != nil {
			cw.Close()
			return err
		}
	}
	if c.s.has("DLRMAST") {
		// dealers: the client master's RM is the RM code or, when blank, the dealer code
		list, _ := c.s.sel("DLRMAST", "", "DLRCODE", "DESCR", "BRANCHID", "email", "PAN_NO", "GSTIN", "Bankcd", "Acno", "Dt_Close")
		if err := c.d.query(ctx, fmt.Sprintf(qDealers, list), func(r migrate.Row) error {
			b := banks[upper(r, "bankcd")]
			return write(map[string]string{"Code": upper(r, "dlrcode"), "Name": str(r, "descr"), "Kind": "Relationship manager", "Branch": upper(r, "branchid"),
				"PAN": upper(r, "pan_no"), "GSTIN": upper(r, "gstin"), "Bank": b[0], "IFSC": b[1], "Account": str(r, "acno"), "Email": strings.ToLower(str(r, "email")),
				"Active": yn(date(r, "dt_close") == "")})
		}); err != nil {
			cw.Close()
			return err
		}
	}
	if err := cw.Close(); err != nil {
		return err
	}
	for _, u := range c.clientList() {
		cl := c.cm.clients[u]
		if !c.written[u] {
			continue
		}
		for _, x := range []struct{ code, kind string }{{strings.ToUpper(cl.sb), "authorised person"}, {strings.ToUpper(cl.rm), "relationship manager"}} {
			if x.code != "" && codes[x.code] == "" {
				c.rep.warnf(area, u, "unknown-"+x.kind, "Add it to the master in SharePro or in Ledgerline", "The client's %s %s is not in SharePro's master", x.kind, x.code)
			}
		}
	}
	c.rep.infof(area, "", "share", "SharePro keeps the authorised person's brokerage share per client (DLMAST.SBROK_PER); PARTNERS has no SharePct")
	return nil
}

// ---- DP accounts ----

func dpFreeze(flag string) string {
	switch strings.ToUpper(strings.TrimSpace(flag)) {
	case "D":
		return "Debit"
	case "C":
		return "Credit"
	case "B", "S", "F", "Y":
		return "Both"
	}
	return ""
}

func (c *cur) writeDPAccounts(ctx context.Context) error {
	area := "DP accounts"
	dpc := c.f.dpCOs()
	if len(dpc) == 0 {
		return nil
	}
	reasons := map[string]string{}
	if c.s.has("FREEZERSN") {
		_ = c.d.query(ctx, qFreezeRsn, func(r migrate.Row) error {
			reasons[str(r, "code")] = str(r, "descr")
			return nil
		})
	}
	poa, err := c.poaRows(ctx)
	if err != nil {
		return err
	}
	h := headers[srcDPAccounts]
	cw, err := c.w.Master(c.f.Code, srcDPAccounts, h)
	if err != nil {
		return err
	}
	dpOnly, skipped := 0, map[string]bool{}
	for _, a := range c.cm.dp {
		if !contains(dpc, a.co) {
			continue
		}
		u := a.ucc
		if u != "" && c.skipped[u] {
			skipped[u] = true
			continue
		}
		if u == "" {
			u = a.ben
			dpOnly++
		}
		status, freeze := "Active", dpFreeze(a.suspFlag)
		switch {
		case a.closed || a.benStat == "3":
			status, freeze = "Closed", ""
		case freeze != "" || a.benStat == "2":
			status = "Frozen"
			if freeze == "" {
				freeze = "Both"
			}
		}
		reason := ""
		if freeze != "" && a.suspRsn != "" && a.suspRsn != "0" {
			reason = firstNonEmpty(reasons[a.suspRsn], a.suspRsn)
		}
		ddpi := "N"
		for _, t := range poa[a.co+"|"+a.ben] {
			if strings.Contains(t, "DDPI") {
				ddpi = "Y"
			}
		}
		bsda := ""
		switch a.bsda {
		case "Y", "1":
			bsda = "Y"
		case "N", "0":
			bsda = "N"
		}
		if err := cw.Write(rec(h, map[string]string{"UCC": u, "BOID": a.boid, "Depository": a.depository, "Status": status, "Opened": a.opened, "ClosedOn": a.closedOn,
			"BSDA": bsda, "Freeze": freeze, "FreezeReason": reason, "DDPI": ddpi})); err != nil {
			cw.Close()
			return err
		}
	}
	if err := cw.Close(); err != nil {
		return err
	}
	if dpOnly > 0 {
		c.rep.infof(area, "", "dp-only", "%d DP accounts have no trading code in SharePro (DP-only clients); they are listed under their DP client code (BENACNUM)", dpOnly)
	}
	excl := ""
	if len(skipped) > 0 {
		excl = " AND RTRIM(SUBCODE) NOT IN (" + inList(sortedKeys(skipped)) + ")"
	}
	r, err := c.d.one(ctx, fmt.Sprintf(qCheckDP, inList(dpc), excl))
	if err != nil {
		return err
	}
	c.rep.check(pkg.CheckDPAccounts, fmt.Sprint(i64(r, "n")), "DP accounts in SharePro (CLLIST), open and closed, including DP-only accounts")
	return nil
}

// normPIN takes the digits of a PIN written with spaces, dots or dashes;
// anything that is not then 6 digits is returned as it was, to be reported.
func normPIN(p string) string {
	var d strings.Builder
	for _, r := range p {
		switch {
		case r >= '0' && r <= '9':
			d.WriteRune(r)
		case r == ' ' || r == '.' || r == '-':
		default:
			return strings.TrimSpace(p)
		}
	}
	if d.Len() == 6 {
		return d.String()
	}
	return strings.TrimSpace(p)
}

// partnerCode turns a SharePro sub-broker, RM or dealer code into one
// Ledgerline accepts (3 to 10 letters or digits): punctuation and spaces
// are removed, a code shorter than 3 gets the prefix (AP or RM), a longer
// one keeps its last 10 characters. Placeholders such as "." or "NA" give
// "". The clients' AP and RM codes go through the same function, so they
// still point at the partner.
func partnerCode(raw, prefix string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(raw) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	c := b.String()
	switch c {
	case "", "NA", "NIL", "NONE", "0", "00", "000":
		return ""
	}
	if len(c) < 3 {
		c = prefix + c
	}
	if len(c) > 10 {
		c = c[len(c)-10:]
	}
	return c
}
