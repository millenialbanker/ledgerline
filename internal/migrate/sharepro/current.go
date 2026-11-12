package sharepro

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

// SQL of the go-live step. Select lists are built from the columns the
// database has (schema.sel), so the constants hold the fixed parts.
const (
	qLatest    = `/*sp:latest-date*/ SELECT (SELECT MAX(VRDT) FROM JVFILE) AS jv, (SELECT MAX(TRDT) FROM TRFILE) AS tr`
	qClients   = `/*sp:clients*/ SELECT %s FROM DLMAST`
	qKYC       = `/*sp:kyc*/ SELECT m.KYCCD AS kyccd, m.KYCNO AS kycno, RTRIM(m.STATUS) AS status, m.DT_ACTIVE AS dt_active, m.COMMNCEDT AS commncedt, d.PANNO AS panno FROM KYCMAST m JOIN (SELECT KYCCD, PANNO, ROW_NUMBER() OVER (PARTITION BY KYCCD ORDER BY EFFECTDATE DESC) AS rn FROM KYCDTL) d ON d.KYCCD = m.KYCCD AND d.rn = 1`
	qBanks     = `/*sp:client-banks*/ SELECT %s FROM BKMAST b LEFT JOIN BANKMAST k ON k.BANKCD = b.BANKCD ORDER BY b.SUBCD`
	qNominees  = `/*sp:nominees*/ SELECT %s FROM DLMAST_NOMINEE n ORDER BY n.SUBCD, n.NOMINEENO`
	qSegStatus = `/*sp:segment-status*/ SELECT RTRIM(co) AS co, RTRIM(subcd) AS subcd, RTRIM(status) AS status, effectdate FROM DLMAST_REG2 ORDER BY %s`
	qDPAccts   = `/*sp:dp-accounts*/ SELECT %s FROM CLLIST`
	qLastTrade = `/*sp:last-trade*/ SELECT RTRIM(SUBCD) AS subcd, MAX(TRDT) AS last FROM TRFILE GROUP BY SUBCD`
	qLYTrade   = `/*sp:ly-last-trade*/ SELECT RTRIM(subcd) AS subcd, MAX(trdt) AS last FROM LY_TRADES GROUP BY subcd`
	qRunAc     = `/*sp:running-account*/ SELECT RTRIM(SUBCD) AS subcd, SETTLETYPE AS settletype, ROW_NUMBER() OVER (PARTITION BY SUBCD ORDER BY ACC_YEAR DESC) AS rn FROM DLRUNACMAST`
	qStates    = `/*sp:states*/ SELECT RTRIM(STATECD) AS statecd, RTRIM(DESCR) AS descr FROM STATES`
	// a client's latest group of each type
	qGroups2 = `/*sp:client-groups*/ SELECT RTRIM(Subcd) AS subcd, RTRIM(Type) AS type, RTRIM(Code) AS code, ROW_NUMBER() OVER (PARTITION BY Subcd, Type ORDER BY %s DESC) AS rn FROM DLGROUP`
	// counts of fields never migrated (the values are never read)
	qCountSecret = `/*sp:count-secret*/ SELECT COUNT(CASE WHEN %s IS NOT NULL AND LEN(RTRIM(CAST(%s AS varchar(64)))) > 0 AND RTRIM(CAST(%s AS varchar(64))) <> '0' THEN 1 END) AS n FROM %s`
)

// secretCols are counted and recorded as dropped, never read.
var secretCols = []struct{ table, col, field, reason string }{
	{"DLMAST", "Web_Pwd", "Client web password", "Never migrated; clients set a new password in Ledgerline"},
	{"CLLIST", "Web_Pwd", "DP web password", "Never migrated; clients set a new password in Ledgerline"},
	{"SBMAST", "Web_Pwd", "Sub-broker web password", "Never migrated; sub-brokers set a new password"},
	{"RMMAST", "Web_Pwd", "Relationship manager web password", "Never migrated; staff set a new password"},
	{"CLLIST", "FHOLDER_ADHAR", "Aadhaar number (first holder)", "Full Aadhaar numbers are never migrated (UIDAI rules); Ledgerline keeps no Aadhaar number for clients"},
	{"CLLIST", "SHOLDER_ADHAR", "Aadhaar number (second holder)", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
	{"CLLIST", "THOLDER_ADHAR", "Aadhaar number (third holder)", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
	{"CDNOMREG", "UID", "DP nominee Aadhaar number", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
	{"CLLIST_DET", "UID", "Aadhaar number (DP account details)", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
	{"CLLIST_DET", "FHADHARNOM", "Aadhaar number (DP nominee 1, account details)", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
	{"CLLIST_DET", "SHADHARNOM", "Aadhaar number (DP nominee 2, account details)", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
	{"CLLIST_DET", "THADHARNOM", "Aadhaar number (DP nominee 3, account details)", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
	{"DLRMAST", "web_Pwd", "Dealer web password", "Never migrated; staff set a new password"},
	{"DLAGREE", "UID", "Aadhaar number (client agreement)", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
	{"DLAGREE", "NomineeAdhar", "Nominee Aadhaar number (client agreement)", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
	{"DLAGREE2", "C_UID1", "Aadhaar number (contact person 1)", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
	{"DLAGREE2", "C_UID2", "Aadhaar number (contact person 2)", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
	{"DLAGREE2", "C_UID3", "Aadhaar number (contact person 3)", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
	{"CLAUTHMAST", "UID", "Aadhaar number (DP authorised person)", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
	{"BOMAST", "FHUID", "Aadhaar number (DP account opening)", "Full Aadhaar numbers are never migrated (UIDAI rules)"},
}

// client is one client as read from SharePro (all firms).
type client struct {
	ucc, name, typ, ctype, pan, dob, mobile, email, city, state, statecd, country string
	branch, sb, rm, group, family, risk, income, bank, bankAc, micr, bankType     string
	nominee, nomineeRel, commence, added, closeOut, gender, occupation, taxStatus string
	cat, kycFlag, custUCC                                                         string
	address, pin                                                                  string
	reg                                                                           map[string]bool // Ledgerline segments registered
	econtract, noInterest                                                         bool
	// dpOnly: a demat account holder of the firm's DP with no trading
	// account (not in DLMAST); dp is that account (current_dponly.go)
	dpOnly bool
	dp     *dpAcct
}

type kycRec struct{ kyccd, kycno, status, active, commence string }

type bankRec struct {
	ucc, bank, ifsc, account, typ, micr, branch, added string
	closed, poa, primary                               bool
}

type nomRec struct {
	ucc, name, rel, share, idType, id, dob, guardian, grel, mobile, email, addr, date string
}

type statusEv struct {
	co, status, on string
}

type dpAcct struct {
	co, dpid, ben, boid, ucc, depository, opened string
	closedOn, benStat, suspFlag, suspRsn, bsda   string
	closed                                       bool
	// client is the Ledgerline client the account belongs to: its trading
	// client (ucc) or, for an account with no trading client, the DP-only
	// client's code (dpOnly). Empty when no code could be made.
	client string
	dpOnly bool
	sub    string    // CLLIST.SUBCODE as given, when it is not a client in DLMAST
	note   string    // why the DP-only client's code is not the DP client code
	h      *dpHolder // the holders' details (go-live step only)
}

// isClosed: BENCLOSEDT set or BENSTAT 3.
func (a dpAcct) isClosed() bool { return a.closed || a.benStat == "3" }

// common is what Current reads once for every firm.
type common struct {
	clients    map[string]*client
	order      []string
	kycByPAN   map[string]kycRec
	banks      map[string][]bankRec
	nominees   map[string][]nomRec
	status     map[string][]statusEv
	dp         []dpAcct
	lastTrade  map[string]string
	runAc      map[string]string
	states     map[string]string
	groups     map[string][]string // client -> group codes (DLMAST.GROUPCD, then DLGROUP)
	runAcOther map[string]int      // DLRUNACMAST.SETTLETYPE values not mapped
	missing    []string

	// DP accounts: BO IDs of every CLLIST account; the trading clients'
	// demat accounts in the client demat master (CDMAST); CLLIST_DET of the
	// DP-only accounts (co|benacnum)
	boids      map[string]bool
	cdmast     map[string][]cdRec
	cdBad      int
	dpDet      map[string]dpDetail
	dpNomNoPAN int // DP nominees whose PAN is not a valid PAN (left out)
}

var errNoFirm = errors.New("sharepro: no firm to migrate (none of the chosen company codes is in this database)")

func (m *Mapper) Current(ctx context.Context, q migrate.Querier, k migrate.Kind, plan migrate.Plan, w *pkg.Writer, p migrate.Progress) error {
	if p == nil {
		p = migrate.NopProgress{}
	}
	d := db{q}
	s, err := loadSchema(ctx, d)
	if err != nil {
		return err
	}
	if !isCompany(s) {
		return fmt.Errorf("sharepro: %s is not a company database", q.Database())
	}
	firms := m.firmsFor(ctx, d, s, plan)
	if len(firms) == 0 {
		return errNoFirm
	}
	asOf := plan.AsOf
	if asOf.IsZero() {
		r, err := d.one(ctx, qLatest)
		if err != nil {
			return err
		}
		asOf = tm(r, "jv")
		if t := tm(r, "tr"); t.After(asOf) {
			asOf = t
		}
		if asOf.IsZero() {
			asOf = fyEnd(k.FY)
		}
	}
	asOf = time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, time.UTC)
	p.Step("Client master")
	cm, err := loadCommon(ctx, d, s)
	if err != nil {
		return err
	}
	// DP account -> client (trading or DP-only), for DP mails and history
	bo := map[string]string{}
	for _, a := range cm.dp {
		if a.client != "" {
			bo[a.ben] = a.client
			bo[a.boid] = a.client
		}
	}
	m.mu.Lock()
	m.boUCC = bo
	m.mu.Unlock()
	primary := firms[0].Code
	// clients are assigned over every firm SharePro has, not only the
	// chosen ones, so a chosen firm never takes another firm's clients
	m.mu.Lock()
	all := append([]firmInfo(nil), m.firms...)
	m.mu.Unlock()
	if len(all) == 0 {
		all = firms
	}
	assigned := assignClients(all, cm)
	// the firm of each client and DP account, over all firms, so documents
	// of firms not chosen can be told apart
	cf, bf := map[string]string{}, map[string]string{}
	for _, f := range all {
		for u := range assigned[f.Code] {
			if _, ok := cf[u]; !ok {
				cf[u] = f.Code
			}
		}
		for _, a := range cm.dp {
			if contains(f.COs, a.co) {
				bf[a.ben], bf[a.boid] = f.Code, f.Code
			}
		}
	}
	m.mu.Lock()
	m.clientFirm, m.boFirm = cf, bf
	m.mu.Unlock()
	for _, f := range firms {
		rep := newReport(f.Code, k.FY)
		for _, n := range f.Notes {
			rep.infof("Firm", f.Code, "firm-note", "%s", n)
		}
		for _, mc := range cm.missing {
			rep.infof("Schema", mc, "missing-col", "Column %s is not in this SharePro database; the field is left empty", mc)
		}
		setAsOf(w, f, asOf)
		c := &cur{m: m, d: d, s: s, k: k, plan: plan, w: w, p: p, f: f, cm: cm, rep: rep, asOf: asOf, primary: f.Code == primary, home: f.Code == all[0].Code, mine: assigned[f.Code]}
		err := c.run(ctx)
		rep.flush(w, p)
		if err != nil {
			return err
		}
	}
	return nil
}

func setAsOf(w *pkg.Writer, f firmInfo, asOf time.Time) {
	man := w.Manifest()
	for i := range man.Firms {
		if man.Firms[i].Code == f.Code {
			man.Firms[i].AsOf = day(asOf)
			return
		}
	}
	pf := toPkgFirms([]firmInfo{f})[0]
	pf.AsOf = day(asOf)
	man.Firms = append(man.Firms, pf)
}

func loadCommon(ctx context.Context, d db, s *schema) (*common, error) {
	cm := &common{clients: map[string]*client{}, kycByPAN: map[string]kycRec{}, banks: map[string][]bankRec{}, nominees: map[string][]nomRec{},
		status: map[string][]statusEv{}, lastTrade: map[string]string{}, runAc: map[string]string{}, states: map[string]string{}, groups: map[string][]string{}, runAcOther: map[string]int{},
		boids: map[string]bool{}, cdmast: map[string][]cdRec{}, dpDet: map[string]dpDetail{}}
	regCols := []string{"REG_NSE", "REG_BSE", "REG_NFO", "REG_BFO", "REG_CNFO", "REG_CBFO", "REG_USFO", "REG_MCX", "REG_NCDEX", "REG_CMFO", "REG_NMCEIL", "REG_ICFO", "REG_KCFO", "REG_NSFO", "REG_DGCX"}
	cols := append([]string{"SUBCD", "DESCR", "CLIENTTYPE", "PAN_NO", "BIRTH_DATE", "MOBILE", "EMAIL1", "CITY", "RES_CITY", "STATE", "STATECD", "COUNTRY",
		"ADD1", "PIN", "BranchID", "SBCODE", "RMCODE", "GROUPCD", "FAMILYCD", "ClRiskProfile", "AnnIncome", "BANK", "BANKACNUM", "MICRNO", "BANKACTYPE",
		"NomineeNm", "NomineeRel", "COMMNCEDT", "ADD_DTTIME", "CloseOutDt", "eContract", "KYCStatus", "TaxStatus", "CLIENTCAT", "GENDER", "OCCUPATION",
		"CUST_UCC", "NoInterest", "DLRCODE"}, regCols...)
	list, miss := s.sel("DLMAST", "", cols...)
	cm.missing = append(cm.missing, miss...)
	err := d.query(ctx, fmt.Sprintf(qClients, list), func(r migrate.Row) error {
		c := &client{ucc: upper(r, "subcd"), name: str(r, "descr"), ctype: str(r, "clienttype"), pan: upper(r, "pan_no"), dob: date(r, "birth_date"),
			mobile: str(r, "mobile"), email: strings.ToLower(str(r, "email1")), city: firstNonEmpty(str(r, "city"), str(r, "res_city")), state: str(r, "state"),
			statecd: str(r, "statecd"), country: upper(r, "country"), address: str(r, "add1"), pin: str(r, "pin"), branch: str(r, "branchid"), sb: str(r, "sbcode"), rm: firstNonEmpty(str(r, "rmcode"), str(r, "dlrcode")),
			group: str(r, "groupcd"), family: str(r, "familycd"), risk: str(r, "clriskprofile"), income: str(r, "annincome"), bank: str(r, "bank"),
			bankAc: str(r, "bankacnum"), micr: str(r, "micrno"), bankType: str(r, "bankactype"), nominee: str(r, "nomineenm"), nomineeRel: str(r, "nomineerel"),
			commence: date(r, "commncedt"), added: date(r, "add_dttime"), closeOut: date(r, "closeoutdt"), econtract: flag(r, "econtract"),
			kycFlag: upper(r, "kycstatus"), taxStatus: upper(r, "taxstatus"), cat: upper(r, "clientcat"), gender: upper(r, "gender"), occupation: str(r, "occupation"),
			custUCC: upper(r, "cust_ucc"), noInterest: flag(r, "nointerest"), reg: map[string]bool{}}
		for _, rc := range regCols {
			if flag(r, strings.ToLower(rc)) {
				c.reg[regSegment(rc)] = true
			}
		}
		if c.ucc == "" {
			return nil
		}
		if _, dup := cm.clients[c.ucc]; dup {
			c.ucc += "\x00dup" // reported later
		}
		cm.clients[c.ucc] = c
		cm.order = append(cm.order, c.ucc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if s.has("KYCMAST") && s.has("KYCDTL") {
		if err := d.query(ctx, qKYC, func(r migrate.Row) error {
			cm.kycByPAN[upper(r, "panno")] = kycRec{kyccd: upper(r, "kyccd"), kycno: str(r, "kycno"), status: upper(r, "status"), active: date(r, "dt_active"), commence: date(r, "commncedt")}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if s.has("BKMAST") {
		list, miss := s.sel("BKMAST", "b", "SUBCD", "TYPE", "ACNO", "ACTYPE", "Closed", "ADD_DTTIME", "POA_RECVD")
		cm.missing = append(cm.missing, miss...)
		kl, miss := s.sel("BANKMAST", "k", "bank=k.DESCR", "ifsc=k.RTGS", "micr=k.MICRNO", "branch=k.BRANCH")
		if !s.has("BANKMAST") {
			kl, miss = "NULL AS bank, NULL AS ifsc, NULL AS micr, NULL AS branch", []string{"BANKMAST"}
		}
		cm.missing = append(cm.missing, miss...)
		q := fmt.Sprintf(qBanks, list+", "+kl)
		if !s.has("BANKMAST") {
			q = strings.Replace(q, " LEFT JOIN BANKMAST k ON k.BANKCD = b.BANKCD", "", 1)
		}
		if err := d.query(ctx, q, func(r migrate.Row) error {
			// BKMAST.TYPE is Primary, Secondary or Pool; a pool account is the
			// firm's own, not the client's
			if strings.EqualFold(str(r, "type"), "POOL") {
				return nil
			}
			b := bankRec{primary: strings.EqualFold(str(r, "type"), "PRIMARY"), ucc: upper(r, "subcd"), bank: str(r, "bank"), ifsc: upper(r, "ifsc"), account: str(r, "acno"), typ: str(r, "actype"), micr: str(r, "micr"),
				branch: str(r, "branch"), added: date(r, "add_dttime"), closed: flag(r, "closed"), poa: flag(r, "poa_recvd")}
			cm.banks[b.ucc] = append(cm.banks[b.ucc], b)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if s.has("DLMAST_NOMINEE") {
		idSafe := "id=CASE WHEN UPPER(n.IDTYPE) LIKE '%AADH%' OR UPPER(n.IDTYPE) LIKE '%ADHAR%' OR UPPER(n.IDTYPE) LIKE '%UID%' THEN RIGHT(RTRIM(n.IDNUMBER), 4) WHEN LEN(REPLACE(RTRIM(n.IDNUMBER), ' ', '')) = 12 AND REPLACE(RTRIM(n.IDNUMBER), ' ', '') NOT LIKE '%[^0-9]%' THEN RIGHT(RTRIM(n.IDNUMBER), 4) ELSE RTRIM(n.IDNUMBER) END"
		if !s.hasCol("DLMAST_NOMINEE", "IDNUMBER") || !s.hasCol("DLMAST_NOMINEE", "IDTYPE") {
			idSafe = "id=NULL"
		}
		list, miss := s.sel("DLMAST_NOMINEE", "n", "SUBCD", "NOMINEENO", "DESCR", "PERCENT", "RELATION", "IDTYPE", idSafe, "ISMINOR", "MINORDOB",
			"GDDESCR", "GDRELATION", "MOBILE", "EMAIL", "ADD1", "CITY", "PINCODE", "Date", "WithDt")
		cm.missing = append(cm.missing, miss...)
		if err := d.query(ctx, fmt.Sprintf(qNominees, list), func(r migrate.Row) error {
			if date(r, "withdt") != "" {
				return nil // withdrawn
			}
			n := nomRec{ucc: upper(r, "subcd"), name: str(r, "descr"), rel: str(r, "relation"), idType: upper(r, "idtype"), id: upper(r, "id"),
				guardian: str(r, "gddescr"), grel: str(r, "gdrelation"), mobile: str(r, "mobile"), email: strings.ToLower(str(r, "email")),
				addr: strings.Join(strings.Fields(strings.Join([]string{str(r, "add1"), str(r, "city"), str(r, "pincode")}, " ")), " "), date: date(r, "date")}
			if sh := dec(r, "percent"); sh.Sign() > 0 {
				n.share = sh.String()
			}
			if flag(r, "isminor") {
				n.dob = date(r, "minordob")
			}
			cm.nominees[n.ucc] = append(cm.nominees[n.ucc], n)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if s.has("DLMAST_REG2") {
		if err := d.query(ctx, fmt.Sprintf(qSegStatus, s.order("DLMAST_REG2", "", "subcd", "effectdate", "ts")), func(r migrate.Row) error {
			u := upper(r, "subcd")
			cm.status[u] = append(cm.status[u], statusEv{co: str(r, "co"), status: upper(r, "status"), on: date(r, "effectdate")})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if s.has("CLLIST") {
		list, miss := s.sel("CLLIST", "", append([]string{"CO", "DPID", "BENACNUM", "FBENACNUM", "SUBCODE", "BENSTAT", "BENCLOSEDT", "ACTIVEDT", "SUSPFLAG", "SUSPRSNCD", "BSDA"}, holderCols...)...)
		cm.missing = append(cm.missing, miss...)
		if err := d.query(ctx, fmt.Sprintf(qDPAccts, list), func(r migrate.Row) error {
			a := dpAcct{co: str(r, "co"), dpid: upper(r, "dpid"), ben: upper(r, "benacnum"), boid: upper(r, "fbenacnum"), ucc: upper(r, "subcode"),
				opened: date(r, "activedt"), closedOn: date(r, "benclosedt"), closed: date(r, "benclosedt") != "", benStat: str(r, "benstat"),
				suspFlag: upper(r, "suspflag"), suspRsn: str(r, "susprsncd"), bsda: upper(r, "bsda"), h: readHolder(r)}
			if a.boid == "" {
				a.boid = a.dpid + a.ben
			}
			a.depository = "CDSL"
			if strings.HasPrefix(a.boid, "IN") {
				a.depository = "NSDL"
			}
			cm.dp = append(cm.dp, a)
			cm.boids[a.boid] = true
			return nil
		}); err != nil {
			return nil, err
		}
		if err := loadDPOnly(ctx, d, s, cm); err != nil {
			return nil, err
		}
	}
	if err := loadClientDemat(ctx, d, s, cm); err != nil {
		return nil, err
	}
	lastTrade := func(q string) error {
		return d.query(ctx, q, func(r migrate.Row) error {
			u, t := upper(r, "subcd"), date(r, "last")
			if t > cm.lastTrade[u] {
				cm.lastTrade[u] = t
			}
			return nil
		})
	}
	if s.has("TRFILE") {
		if err := lastTrade(qLastTrade); err != nil {
			return nil, err
		}
	}
	if s.has("LY_TRADES") {
		if err := lastTrade(qLYTrade); err != nil {
			return nil, err
		}
	}
	if s.hasCol("DLRUNACMAST", "SETTLETYPE") {
		_ = d.query(ctx, qRunAc, func(r migrate.Row) error {
			if i64(r, "rn") == 1 {
				cm.runAc[upper(r, "subcd")] = rasOf(str(r, "settletype"))
				if st := str(r, "settletype"); st != "" && st != "2" && st != "3" {
					cm.runAcOther[st]++
				}
			}
			return nil
		})
	}
	if s.has("STATES") {
		_ = d.query(ctx, qStates, func(r migrate.Row) error {
			cm.states[upper(r, "statecd")] = str(r, "descr")
			return nil
		})
	}
	for _, u := range cm.order {
		if g := codePart(cm.clients[u].group); cm.clients[u].group != "" {
			cm.groups[u] = append(cm.groups[u], g)
		}
	}
	if s.hasCol("DLGROUP", "Code") && s.hasCol("DLGROUP", "Subcd") {
		if err := d.query(ctx, fmt.Sprintf(qGroups2, s.order("DLGROUP", "", "EffectDate")), func(r migrate.Row) error {
			if i64(r, "rn") != 1 || str(r, "code") == "" {
				return nil
			}
			u, g := upper(r, "subcd"), codePart(str(r, "code"))
			for _, x := range cm.groups[u] {
				if x == g {
					return nil
				}
			}
			cm.groups[u] = append(cm.groups[u], g)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return cm, nil
}

// regSegment maps DLMAST's REG_* flags to Ledgerline segments.
func regSegment(col string) string {
	switch col {
	case "REG_NSE", "REG_BSE":
		return "CM"
	case "REG_NFO", "REG_BFO", "REG_NSFO":
		return "FO"
	case "REG_CNFO", "REG_CBFO", "REG_USFO":
		return "CD"
	}
	return "COM"
}

// rasOf reads DLRUNACMAST.SETTLETYPE. Values seen in a real database: 2
// (most clients: quarterly, the default) and 3 (monthly), assumed; 1 is
// not known and is left blank (to be confirmed).
func rasOf(v string) string {
	switch strings.TrimSpace(v) {
	case "2":
		return "Quarterly"
	case "3":
		return "Monthly"
	}
	return ""
}

// assignClients: a client belongs to every firm in whose COs it has a
// segment status or a DP account; clients with neither go to the first
// firm. A DP-only client belongs to the firm whose company holds its DP
// account.
func assignClients(firms []firmInfo, cm *common) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	coFirm := map[string]string{}
	for _, f := range firms {
		out[f.Code] = map[string]bool{}
		for _, co := range f.COs {
			coFirm[co] = f.Code
		}
	}
	if len(firms) == 1 {
		for _, u := range cm.order {
			out[firms[0].Code][u] = true
		}
		return out
	}
	for u, evs := range cm.status {
		for _, e := range evs {
			if f, ok := coFirm[e.co]; ok {
				out[f][u] = true
			}
		}
	}
	for _, a := range cm.dp {
		if f, ok := coFirm[a.co]; ok && a.client != "" {
			out[f][a.client] = true
		}
	}
	for _, u := range cm.order {
		in := false
		for _, f := range firms {
			in = in || out[f.Code][u]
		}
		if !in {
			out[firms[0].Code][u] = true
		}
	}
	return out
}

// cur is the go-live step for one firm.
type cur struct {
	m       *Mapper
	d       db
	s       *schema
	k       migrate.Kind
	plan    migrate.Plan
	w       *pkg.Writer
	p       migrate.Progress
	f       firmInfo
	cm      *common
	rep     *report
	asOf    time.Time
	primary bool // the first chosen firm (counts made once)
	home    bool // the first firm: clients linked to no firm come here
	mine    map[string]bool

	written  map[string]bool // clients in the client master
	skipped  map[string]bool // dormant clients left out (DormantMode skip)
	inactive map[string]string
	// noSegInactive counts clients made inactive because no segment is
	// registered for them at all
	noSegInactive int
	// partnerCodes are the partner codes written to PARTNERS (code → kind);
	// nil when SharePro has no partner masters
	partnerCodes map[string]string
	chart        *chart
	segCache     map[string]segH

	derivClients int
	// demat is each client's demat account in the client master (DematID)
	demat map[string]string
	// DP-only clients written, closed ones among them (Inactive = Y),
	// closed ones left out (dormant mode skip)
	dpOnlyN, dpOnlyClosed, dpOnlySkipped int
	dpOnlyIncome                         int // with an income range code in CLLIST_DET
}

type segH struct {
	rows   [][4]string
	latest map[string]string
}

func (c *cur) run(ctx context.Context) error {
	c.written, c.skipped, c.inactive, c.segCache, c.demat = map[string]bool{}, map[string]bool{}, map[string]string{}, map[string]segH{}, map[string]string{}
	c.countSecrets(ctx)
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		// partners first: the client master's AP and RM codes must be on record
		{"Branches", c.writeBranches},
		{"Authorised persons and RMs", c.writePartners},
		{"Client master", c.writeClients},
		{"Client bank accounts", c.writeBanks},
		{"Nominees", c.writeNominees},
		{"Segment status", c.writeSegStatus},
		{"Opening balances", c.writeOpening},
		{"DP holdings", c.writeHoldings},
		{"Open positions", c.writePositions},
		{"Brokerage", c.writeBrokerage},
		{"Client interest", c.writeInterest},
		{"KRA and CKYC", c.writeKYC},
		{"UCC details", c.writeUCCDetails},
		{"Contact persons", c.writeContacts},
		{"DP accounts", c.writeDPAccounts},
	}
	for _, st := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		c.p.Step(st.name)
		if err := st.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", st.name, err)
		}
	}
	c.rep.infof("Holding lots", "", "no-lots", "SharePro keeps no purchase lots or average cost for DP holdings; HOLDING-LOTS is not written and DP-HOLDINGS has no AvgCost. Load purchase history from the client's broker statements if needed")
	c.rep.infof("Brokerage", "", "no-scrip-groups", "SharePro has no scrip groups for brokerage (DLGROUP holds client groups); SCRIP-GROUPS is not written")
	if len(c.f.dpCOs()) > 0 {
		c.rep.drop("DP tariff assignments (DPCLBASKET; tariffs in DPCLBSKT, DPCHARGE)", "Ledgerline has no import file for DP tariffs; set the DP tariff in Ledgerline", c.count(ctx, "DPCLBASKET"))
	}
	return nil
}

const qCount = `/*sp:count*/ SELECT COUNT(*) AS n FROM %s`

func (c *cur) count(ctx context.Context, table string) int {
	if !c.s.has(table) {
		return 0
	}
	r, err := c.d.one(ctx, fmt.Sprintf(qCount, table))
	if err != nil || r == nil {
		return 0
	}
	return int(i64(r, "n"))
}

func (c *cur) countSecrets(ctx context.Context) {
	if !c.primary {
		return
	}
	for _, sc := range secretCols {
		if !c.s.hasCol(sc.table, sc.col) {
			continue
		}
		r, err := c.d.one(ctx, fmt.Sprintf(qCountSecret, sc.col, sc.col, sc.col, sc.table))
		if err != nil || r == nil {
			continue
		}
		c.rep.drop(sc.field, sc.reason, int(i64(r, "n")))
	}
}

// ---- client master ----

var (
	rePAN    = regexp.MustCompile(`^[A-Z]{5}[0-9]{4}[A-Z]$`)
	reIFSC   = regexp.MustCompile(`^[A-Z]{4}0[A-Z0-9]{6}$`)
	reMobile = regexp.MustCompile(`^[6-9][0-9]{9}$`)
	reEmail  = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	reAcct   = regexp.MustCompile(`^[0-9]{9,18}$`)
	reUCC    = regexp.MustCompile(`^[A-Z0-9]{3,10}$`)
	reDigits = regexp.MustCompile(`[^0-9]`)
)

func cleanMobile(s string) string {
	d := reDigits.ReplaceAllString(s, "")
	switch {
	case len(d) == 12 && strings.HasPrefix(d, "91"):
		d = d[2:]
	case len(d) == 11 && strings.HasPrefix(d, "0"):
		d = d[1:]
	}
	return d
}

// clientType reads the client's type from the PAN's 4th character.
func clientType(c *client) string {
	nri := strings.Contains(c.cat, "NRI") || strings.Contains(strings.ToUpper(c.ctype), "NRI") || (c.country != "" && c.country != "INDIA" && c.country != "IN" && c.country != "IND")
	if len(c.pan) == 10 {
		switch c.pan[3] {
		case 'P':
			if nri {
				return "NRI"
			}
			return "Individual"
		case 'H':
			return "HUF"
		case 'C':
			return "Corporate"
		default:
			return "Corporate" // firms, trusts, AOPs: reported below
		}
	}
	if nri {
		return "NRI"
	}
	return "Individual"
}

// latestStatus is each segment's latest status in this firm's COs, merged
// over the exchanges of a segment (see mergeStatus).
func (c *cur) segHistory(u string) (rows [][4]string, latest map[string]string) {
	if h, ok := c.segCache[u]; ok {
		return h.rows, h.latest
	}
	defer func() { c.segCache[u] = segH{rows, latest} }()
	per := map[string]map[string]string{} // seg -> co -> status
	latest = map[string]string{}
	var unknown []string
	for _, e := range c.cm.status[u] {
		seg := c.f.segOf(e.co)
		if seg == "" || !contains(c.f.COs, e.co) {
			continue
		}
		stt, ok := segStatusName(e.status)
		if !ok {
			unknown = append(unknown, e.status)
			continue
		}
		if e.on == "" {
			continue
		}
		if per[seg] == nil {
			per[seg] = map[string]string{}
		}
		per[seg][e.co] = stt
		merged := mergeStatus(per[seg])
		if merged != latest[seg] {
			rows = append(rows, [4]string{seg, merged, e.on, "SharePro " + e.co + " " + strings.ToLower(e.status)})
			latest[seg] = merged
		}
	}
	for _, x := range unknown {
		c.rep.warnf("Segment status", u, "unknown-status", "Set the segment status by hand after loading", "Status %q in DLMAST_REG2 is not one Ledgerline knows; the row is left out", x)
	}
	return rows, latest
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func segStatusName(s string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "ACTIVE":
		return "Active", true
	case "REACTIVATED", "REACTIVE":
		return "Reactivated", true
	case "SUSPENDED", "SUSPEND":
		return "Suspended", true
	case "DORMANT", "INACTIVE":
		return "Dormant", true
	case "CLOSED", "CLOSE":
		return "Closed", true
	}
	return "", false
}

var statusRank = map[string]int{"Active": 5, "Reactivated": 4, "Dormant": 3, "Suspended": 2, "Closed": 1}

// mergeStatus: a segment traded on two exchanges (NSE and BSE cash) is as
// open as its most open exchange.
func mergeStatus(byCO map[string]string) string {
	best := ""
	for _, s := range byCO {
		if statusRank[s] > statusRank[best] {
			best = s
		}
	}
	return best
}

func isActive(s string) bool { return s == "Active" || s == "Reactivated" }

// dormant says whether a client has no active segment in this firm, and
// since when.
func (c *cur) dormant(u string) (bool, string) {
	rows, latest := c.segHistory(u)
	if len(latest) == 0 {
		// no status history: closed if SharePro has a close-out date
		if cl := c.cm.clients[u]; cl != nil && cl.closeOut != "" {
			return true, cl.closeOut
		}
		return false, ""
	}
	since := ""
	for seg, st := range latest {
		if isActive(st) {
			return false, ""
		}
		for _, r := range rows {
			if r[0] == seg && r[2] > since {
				since = r[2]
			}
		}
	}
	return true, since
}

func (c *cur) clientList() []string {
	var out []string
	for _, u := range c.cm.order {
		if c.mine[u] {
			out = append(out, u)
		}
	}
	return out
}

func (c *cur) writeClients(ctx context.Context) error {
	h := headers[srcClients]
	cw, err := c.w.Master(c.f.Code, srcClients, h)
	if err != nil {
		return err
	}
	own := c.ownAccounts(c.f.COs)
	pans := map[string]string{}
	noCol := map[string]int{}
	n := 0
	otherDP, otherDPOwn, otherDropped := 0, 0, 0
	for _, u := range c.clientList() {
		cl := c.cm.clients[u]
		if strings.HasSuffix(u, "\x00dup") {
			c.rep.errorf("Client master", strings.TrimSuffix(u, "\x00dup"), "dup-ucc", "Merge or renumber the duplicate in SharePro and run the migration again",
				"Client code appears twice in DLMAST; the second record is left out")
			continue
		}
		if cl.dpOnly {
			ok, err := c.writeDPOnlyClient(cw, h, cl, pans)
			if err != nil {
				cw.Close()
				return err
			}
			if ok {
				n++
			}
			continue
		}
		dorm, since := c.dormant(u)
		dm, other, dropped := c.clientDemat(u, own[u])
		if other {
			otherDP++
			if len(own[u]) > 0 {
				otherDPOwn++
			}
		}
		otherDropped += dropped
		c.demat[u] = dm.boid
		v := c.clientRow(cl, dm)
		v["DPOnly"] = "N"
		if !dorm && v["Segments"] == "" {
			// no segment registered at all: the client cannot trade, so it
			// comes across inactive like a dormant one
			dorm, since = true, cl.closeOut
			c.noSegInactive++
		}
		if dorm {
			if c.plan.DormantMode == "skip" {
				c.skipped[u] = true
				continue
			}
			c.inactive[u] = since
		}
		if dorm {
			v["Inactive"], v["InactiveSince"] = "Y", since
		}
		c.validateClient(cl, v, pans)
		for _, x := range []struct{ field, val string }{{"Family", cl.family}} {
			if x.val != "" {
				noCol[x.field]++
			}
		}
		if err := cw.Write(rec(h, v)); err != nil {
			cw.Close()
			return err
		}
		c.written[u] = true
		n++
		if n%500 == 0 {
			c.p.Count(n, 0)
		}
	}
	if err := cw.Close(); err != nil {
		return err
	}
	for _, f := range sortedKeys(noCol) {
		c.rep.drop("Client "+strings.ToLower(f[:1])+f[1:], "Ledgerline's CLIENT-MASTER has no column for it yet; kept in SharePro's records", noCol[f])
	}
	for _, v := range sortedKeys(c.cm.runAcOther) {
		if c.primary {
			c.rep.infof("Client master", "", "ras-"+v, "%d clients have running-account settlement type %s (DLRUNACMAST.SETTLETYPE), whose meaning is not known; RAS is left blank (Ledgerline's default applies). Confirm with SharePro", c.cm.runAcOther[v], v)
		}
	}
	if c.derivClients > 0 {
		c.rep.infof("Client master", "", "income-proof", "%d clients have a derivatives segment: the import records their income proof as received; check it is on file", c.derivClients)
	}
	if k := len(c.skipped) - c.dpOnlySkipped; k > 0 {
		c.rep.infof("Client master", "", "skipped", "%d clients with no active segment were left out (dormant mode: skip)", k)
	}
	if k := len(c.inactive) - c.dpOnlyClosed; k > 0 {
		c.rep.infof("Client master", "", "inactive", "%d clients with no active segment are migrated marked inactive (Inactive = Y)", k)
		if c.noSegInactive > 0 {
			c.rep.infof("Client master", "", "inactive-noseg", "%d of them have no segment registered at all in SharePro", c.noSegInactive)
		}
	}
	if c.dpOnlyN > 0 {
		c.rep.infof("Client master", "", "dp-only", "%d DP-only clients (demat accounts of the firm's DP with no trading account in SharePro) are in the client master with DPOnly = Y, under the DP client code (CLLIST.BENACNUM): no segments, no running-account settlement; %d of them are closed accounts, marked inactive (Inactive = Y, InactiveSince the closing date) and kept for their records",
			c.dpOnlyN, c.dpOnlyClosed)
	}
	if c.dpOnlySkipped > 0 {
		c.rep.infof("Client master", "", "dp-only-skipped", "%d closed DP-only accounts were left out of the client files (dormant mode: skip)", c.dpOnlySkipped)
	}
	if c.dpOnlyIncome > 0 {
		c.rep.infof("Client master", "", "dp-only-income", "%d DP-only clients have an income range code in the DP account details (CLLIST_DET.GROSSINCOME) whose meaning is not confirmed; Income is left blank. Confirm the codes with SharePro", c.dpOnlyIncome)
	}
	if otherDP > 0 {
		c.rep.infof("Client master", "", "other-dp", "%d trading clients' primary demat account in SharePro's client demat master (CDMAST) is at another DP: it is the client master's demat account (DematID) and is not in DP-ACCOUNTS, which holds this DP's own accounts. %d of them also have an account at this DP (in DP-ACCOUNTS, the oldest open one marked primary)", otherDP, otherDPOwn)
	}
	if c.primary && c.cm.cdBad > 0 {
		c.rep.infof("Client master", "", "cdmast-bad", "%d rows of SharePro's client demat master (CDMAST) have no usable BO ID (DPMAST.DPID and CDMAST.CLIENTID do not make 16 digits, or IN and 14); they are not used", c.cm.cdBad)
	}
	c.rep.drop("Further demat accounts at other DPs (CDMAST rows that are not the client's primary)", "Ledgerline's client master holds one demat account per client and DP-ACCOUNTS only this DP's own accounts", otherDropped)
	return c.clientChecks(ctx)
}

func (c *cur) clientRow(cl *client, dm dpAcct) map[string]string {
	segs := map[string]bool{}
	for s := range cl.reg {
		segs[s] = true
	}
	for _, e := range c.cm.status[cl.ucc] {
		if sg := c.f.segOf(e.co); sg != "" && contains(c.f.COs, e.co) {
			segs[sg] = true
		}
	}
	var sl []string
	for _, s := range []string{"CM", "FO", "CD", "COM"} {
		if segs[s] {
			sl = append(sl, s)
		}
	}
	k := c.cm.kycByPAN[cl.pan]
	kycDate := firstNonEmpty(k.active, k.commence, cl.commence, cl.added)
	primary := c.primaryBank(cl)
	nom, nomName, nomDate := c.nomination(cl)
	state := firstNonEmpty(c.cm.states[strings.ToUpper(cl.statecd)], cl.state)
	return map[string]string{
		"UCC": cl.ucc, "Name": cl.name, "Type": clientType(cl), "PAN": cl.pan, "DOB": cl.dob, "Mobile": cleanMobile(cl.mobile), "Email": cl.email,
		"City": cl.city, "Segments": strings.Join(sl, ";"), "Risk": riskOf(cl.risk), "Income": cl.income, "RAS": c.cm.runAc[cl.ucc],
		"Bank": primary.bank, "IFSC": primary.ifsc, "Account": primary.account, "Depository": dm.depository, "DematID": dm.boid,
		"Nominee": nom, "NomineeName": nomName, "KYCDate": kycDate, "Status": "Active", "API": "N", "LastTrade": c.cm.lastTrade[cl.ucc],
		"Branch": cl.branch, "State": state, "ECN": yn(cl.econtract), "NominationDate": nomDate,
		"AP": c.knownPartner(cl.ucc, cl.sb, "AP"), "RM": c.knownPartner(cl.ucc, cl.rm, "RM"), "Groups": strings.Join(c.cm.groups[cl.ucc], ";"),
	}
}

// riskOf maps SharePro's risk profile (DLMAST.ClRiskProfile,
// CLLIST.CLRISKPROFILE) to Ledgerline's.
func riskOf(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "LOW", "L":
		return "Low"
	case "MEDIUM", "M", "MODERATE":
		return "Medium"
	case "HIGH", "H":
		return "High"
	}
	return ""
}

// nomination: "Opted in" and the first nominee's name when the client has
// nominees (or a nominee name in the client master), and the earliest
// declaration date.
func (c *cur) nomination(cl *client) (nom, name, date string) {
	if ns := c.cm.nominees[cl.ucc]; len(ns) > 0 {
		nom, name = "Opted in", ns[0].name
	} else if cl.nominee != "" {
		nom, name = "Opted in", cl.nominee
	}
	for _, nn := range c.cm.nominees[cl.ucc] {
		if nn.date != "" && (date == "" || nn.date < date) {
			date = nn.date
		}
	}
	return nom, name, date
}

// primaryBank: the BKMAST account that is the client master's bank
// account, else the first open one, else the client master's own.
func (c *cur) primaryBank(cl *client) bankRec {
	bs := c.cm.banks[cl.ucc]
	for _, b := range bs {
		if !b.closed && b.primary {
			return b
		}
	}
	acct := reDigits.ReplaceAllString(cl.bankAc, "")
	for _, b := range bs {
		if !b.closed && acct != "" && reDigits.ReplaceAllString(b.account, "") == acct {
			return b
		}
	}
	for _, b := range bs {
		if !b.closed {
			return b
		}
	}
	return bankRec{bank: cl.bank, account: cl.bankAc, micr: cl.micr}
}

// clientErr reports a problem with one client's details: an error for an
// active client, a warning for an inactive one, whose record Ledgerline
// keeps as it is (marked incomplete until the details are completed).
func (c *cur) clientErr(u, area, kind, action, f string, a ...any) {
	if _, in := c.inactive[u]; in {
		c.rep.warnf(area, u, kind+"-inactive", "Kept as in SharePro; complete it in Ledgerline before reactivating the client", f, a...)
		return
	}
	c.rep.errorf(area, u, kind, action, f, a...)
}

func (c *cur) validateClient(cl *client, v map[string]string, pans map[string]string) {
	u := cl.ucc
	fix := "Correct it in SharePro (or in the file) before loading; the client master is loaded only when every row is valid"
	// an inactive client's missing or invalid details do not stop the
	// load: Ledgerline keeps the record as it is, marked incomplete, and
	// asks for the details before the client is reactivated
	soft := v["Inactive"] == "Y"
	errorf := c.rep.errorf
	if soft {
		errorf = func(area, key, kind, _, f string, a ...any) {
			c.rep.warnf(area, key, kind+"-inactive", "Kept as in SharePro; complete it in Ledgerline before reactivating the client", f, a...)
		}
	}
	if !reUCC.MatchString(u) {
		c.rep.errorf("Client master", u, "bad-ucc", fix, "Client code %q is not 3 to 10 letters or digits", u)
	}
	if len(strings.Fields(cl.name)) == 0 || len(cl.name) < 3 {
		c.rep.errorf("Client master", u, "no-name", fix, "Name is missing or too short")
	}
	switch {
	case cl.pan == "":
		errorf("Client master", u, "no-pan", fix, "PAN is missing")
	case !rePAN.MatchString(cl.pan):
		errorf("Client master", u, "bad-pan", fix, "PAN %q is not a valid PAN", cl.pan)
	default:
		if other, dup := pans[cl.pan]; dup {
			c.rep.warnf("Client master", u, "dup-pan", "Check whether the two codes are the same person", "PAN is also on client %s", other)
		} else {
			pans[cl.pan] = u
		}
		if t := cl.pan[3]; t != 'P' && t != 'H' && t != 'C' {
			errorf("Client master", u, "pan-type", "Ledgerline client types are Individual, HUF, Corporate and NRI; decide how to hold this client before loading",
				"PAN holder type %c (firm, trust, AOP, body of individuals...) has no Ledgerline client type", t)
		}
	}
	if !reMobile.MatchString(v["Mobile"]) {
		errorf("Client master", u, "bad-mobile", fix, "Mobile %q is not a 10-digit Indian mobile number", cl.mobile)
	}
	if !reEmail.MatchString(v["Email"]) {
		errorf("Client master", u, "bad-email", fix, "Email %q is not valid", v["Email"])
	}
	if v["City"] == "" {
		errorf("Client master", u, "no-city", fix, "City is missing")
	}
	// a DP-only client trades in no segment
	if v["Segments"] == "" && v["DPOnly"] != "Y" {
		errorf("Client master", u, "no-segment", fix, "No segment is registered (REG_ flags and DLMAST_REG2 are empty for this firm)")
	}
	if strings.TrimSpace(v["Bank"]) == "" {
		errorf("Client master", u, "no-bank", fix, "Bank name is missing")
	}
	if !reIFSC.MatchString(v["IFSC"]) {
		errorf("Client master", u, "bad-ifsc", fix, "IFSC %q is not valid (BANKMAST.RTGS of the client's bank)", v["IFSC"])
	}
	if !reAcct.MatchString(v["Account"]) {
		errorf("Client master", u, "bad-account", fix, "Bank account number %q is not 9 to 18 digits", v["Account"])
	}
	if v["Segments"] != "" && v["Segments"] != "CM" {
		c.derivClients++
	}
	if cl.custUCC != "" && cl.custUCC != u {
		c.rep.infof("Client master", u, "exchange-ucc", "The exchange UCC in SharePro (CUST_UCC) is %s; Ledgerline uses the client code %s", cl.custUCC, u)
	}
}

const (
	// a client belongs to the firm when it has a status or DP account in one of the firm's COs
	qCheckClients = `/*sp:check-clients*/ SELECT COUNT(*) AS n FROM (SELECT DISTINCT RTRIM(d.SUBCD) AS u FROM DLMAST d WHERE %s) x`
)

func (c *cur) clientChecks(ctx context.Context) error {
	cos := inList(c.f.COs)
	var conds []string
	firmLink := ""
	if c.s.has("DLMAST_REG2") {
		firmLink = fmt.Sprintf("EXISTS (SELECT 1 FROM DLMAST_REG2 r WHERE r.subcd = d.SUBCD AND r.co IN (%s))", cos)
	}
	if c.s.has("CLLIST") {
		dp := fmt.Sprintf("EXISTS (SELECT 1 FROM CLLIST l WHERE l.SUBCODE = d.SUBCD AND l.CO IN (%s))", cos)
		if firmLink == "" {
			firmLink = dp
		} else {
			firmLink = "(" + firmLink + " OR " + dp + ")"
		}
	}
	m := c.firmsCount()
	switch {
	case m <= 1 || firmLink == "":
		conds = append(conds, "1 = 1")
	case c.home:
		// clients linked to no firm at all come to the first firm
		var all []string
		c.m.mu.Lock()
		for _, f := range c.m.firms {
			all = append(all, f.COs...)
		}
		c.m.mu.Unlock()
		anyLink := strings.ReplaceAll(firmLink, cos, inList(all))
		conds = append(conds, "("+firmLink+" OR NOT "+anyLink+")")
	default:
		conds = append(conds, firmLink)
	}
	if c.plan.DormantMode == "skip" {
		closed := "d.CloseOutDt > '1901-01-01'"
		if !c.s.hasCol("DLMAST", "CloseOutDt") {
			closed = "1 = 0"
		}
		tsDesc := ""
		if c.s.hasCol("DLMAST_REG2", "ts") {
			tsDesc = ", ts DESC"
		}
		if c.s.has("DLMAST_REG2") {
			conds = append(conds, fmt.Sprintf(`NOT ((EXISTS (SELECT 1 FROM DLMAST_REG2 r WHERE r.subcd = d.SUBCD AND r.co IN (%[1]s)) AND NOT EXISTS (SELECT 1 FROM (SELECT co, status, ROW_NUMBER() OVER (PARTITION BY co ORDER BY effectdate DESC%[3]s) AS rn FROM DLMAST_REG2 r WHERE r.subcd = d.SUBCD AND r.co IN (%[1]s)) l WHERE l.rn = 1 AND UPPER(l.status) IN ('ACTIVE','REACTIVATED'))) OR (NOT EXISTS (SELECT 1 FROM DLMAST_REG2 r WHERE r.subcd = d.SUBCD AND r.co IN (%[1]s)) AND %[2]s))`, inList(c.segCOs()), closed, tsDesc))
		} else {
			conds = append(conds, "NOT ("+closed+")")
		}
	}
	r, err := c.d.one(ctx, fmt.Sprintf(qCheckClients, strings.Join(conds, " AND ")))
	if err != nil {
		return err
	}
	n, detail := i64(r, "n"), "Clients in SharePro's client master (DLMAST) for this firm"
	if c.s.has("CLLIST") {
		// DP-only clients: the firm's CLLIST accounts with no trading client
		dc := []string{c.noTradingSQL("l")}
		switch {
		case m <= 1:
		case c.home:
			var all []string
			c.m.mu.Lock()
			for _, f := range c.m.firms {
				all = append(all, f.COs...)
			}
			c.m.mu.Unlock()
			dc = append(dc, fmt.Sprintf("(l.CO IN (%s) OR l.CO NOT IN (%s))", cos, inList(all)))
		default:
			dc = append(dc, fmt.Sprintf("l.CO IN (%s)", cos))
		}
		if c.plan.DormantMode == "skip" {
			dc = append(dc, "NOT "+c.closedSQL("l"))
		}
		r, err := c.d.one(ctx, fmt.Sprintf(qCheckDPOnly, strings.Join(dc, " AND ")))
		if err != nil {
			return err
		}
		n += i64(r, "n")
		detail += fmt.Sprintf(", and %d DP-only clients (accounts in the DP master CLLIST with no trading client)", i64(r, "n"))
	}
	c.rep.check(pkg.CheckClients, fmt.Sprint(n), detail)
	return nil
}

func (c *cur) segCOs() []string {
	var out []string
	for _, co := range c.f.COs {
		if c.f.segOf(co) != "" {
			out = append(out, co)
		}
	}
	return out
}

func (c *cur) firmsCount() int {
	c.m.mu.Lock()
	defer c.m.mu.Unlock()
	return max(1, len(c.m.firms))
}

// ---- bank accounts ----

func (c *cur) writeBanks(ctx context.Context) error {
	written := 0
	h := headers[srcBanks]
	cw, err := c.w.Master(c.f.Code, srcBanks, h)
	if err != nil {
		return err
	}
	for _, u := range c.clientList() {
		if !c.written[u] {
			continue
		}
		cl := c.cm.clients[u]
		bs := c.cm.banks[u]
		if len(bs) == 0 {
			if cl.bankAc == "" {
				c.rep.warnf("Client bank accounts", u, "no-bank", "Add the bank account in Ledgerline", "The client has no bank account in SharePro")
				continue
			}
			bs = []bankRec{{ucc: u, bank: cl.bank, account: cl.bankAc, micr: cl.micr, typ: cl.bankType}}
			c.rep.warnf("Client bank accounts", u, "master-only", "Add the IFSC in Ledgerline", "Only the client master's bank account (no IFSC) is in SharePro")
		}
		pb := c.primaryBank(cl)
		seen := map[string]bool{}
		for _, b := range bs {
			k := reDigits.ReplaceAllString(b.account, "") + "|" + b.ifsc
			if seen[k] {
				c.rep.warnf("Client bank accounts", u, "dup-bank", "Remove the duplicate in SharePro", "Bank account %s is registered twice; listed once", maskAcct(b.account))
				continue
			}
			seen[k] = true
			acct := strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(b.account))
			if !reAcct.MatchString(acct) {
				c.clientErr(u, "Client bank accounts", "bad-account", "Correct the account number in SharePro", "Bank account %s is not 9 to 18 digits", maskAcct(b.account))
			}
			if !reIFSC.MatchString(b.ifsc) {
				c.clientErr(u, "Client bank accounts", "bad-ifsc", "Correct the bank's IFSC (BANKMAST.RTGS)", "IFSC %q of account %s is not valid", b.ifsc, maskAcct(b.account))
			}
			status := "Active"
			if b.closed {
				status = "Inactive"
			}
			primary := !b.closed && b.account == pb.account && b.ifsc == pb.ifsc
			if err := cw.Write(rec(h, map[string]string{"UCC": u, "Bank": b.bank, "IFSC": b.ifsc, "Account": acct, "Primary": yn(primary), "Status": status,
				"Branch": b.branch, "MICR": b.micr, "Type": bankType(b.typ), "AddedOn": b.added})); err != nil {
				cw.Close()
				return err
			}
			written++
		}
	}
	if err := cw.Close(); err != nil {
		return err
	}
	// The check is the number of accounts listed for Ledgerline (after
	// removing duplicates and adding client-master-only accounts, as above),
	// so the reconciliation shows any account that did not load; SharePro's
	// own row count is given beside it.
	detail := "Client bank accounts listed for Ledgerline from SharePro (BKMAST, the client master and, for DP-only clients, the DP master CLLIST), open and closed, duplicates removed"
	if c.s.has("BKMAST") {
		r, err := c.d.one(ctx, fmt.Sprintf(`/*sp:check-banks*/ SELECT COUNT(*) AS n FROM BKMAST b WHERE RTRIM(b.SUBCD) IN (%s)`, inList(sortedKeys(c.written))))
		if err != nil {
			return err
		}
		detail += fmt.Sprintf("; SharePro's BKMAST has %d rows for these clients, the firm's pool account included", i64(r, "n"))
	}
	c.rep.check(pkg.CheckBankAccounts, fmt.Sprint(written), detail)
	return nil
}

func maskAcct(a string) string {
	a = strings.TrimSpace(a)
	if len(a) <= 4 {
		return a
	}
	return strings.Repeat("x", len(a)-4) + a[len(a)-4:]
}

func bankType(t string) string {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	case "SB", "SA", "SAVING", "SAVINGS", "10", "S":
		return "SB"
	case "CA", "CURRENT", "11", "C", "CB":
		return "CA"
	case "NRE":
		return "NRE"
	case "NRO":
		return "NRO"
	}
	return ""
}

// ---- nominees ----

func nomineeIDType(t string) string {
	t = strings.ToUpper(t)
	switch {
	case strings.Contains(t, "PAN"):
		return "PAN"
	case strings.Contains(t, "AADH"), strings.Contains(t, "ADHAR"), strings.Contains(t, "UID"):
		return "AADHAAR"
	case strings.Contains(t, "DRIV"), t == "DL":
		return "DL"
	case strings.Contains(t, "PASS"):
		return "PASSPORT"
	}
	return ""
}

func (c *cur) writeNominees(ctx context.Context) error {
	h := headers[srcNominees]
	cw, err := c.w.Master(c.f.Code, srcNominees, h)
	if err != nil {
		return err
	}
	for _, u := range c.clientList() {
		if !c.written[u] {
			continue
		}
		ns := c.cm.nominees[u]
		if len(ns) > 3 {
			c.rep.warnf("Nominees", u, "over-3", "Review the nomination: from 1 September 2026 at most 3 nominees", "%d nominees; loaded as they are and flagged for review", len(ns))
		}
		for _, n := range ns {
			it := nomineeIDType(n.idType)
			id := n.id
			if it == "" && id != "" {
				c.rep.infof("Nominees", u, "id-type", "Nominee %s: identity type %q is not PAN, DL, Aadhaar or passport; the identity number is left out", n.name, n.idType)
				id = ""
			}
			if it == "AADHAAR" && len(id) > 4 {
				id = id[len(id)-4:]
			}
			if n.rel == "" {
				c.clientErr(u, "Nominees", "no-relation", "Add the relationship in SharePro", "Nominee %s has no relationship", n.name)
			}
			if err := cw.Write(rec(h, map[string]string{"UCC": u, "Name": n.name, "Relation": n.rel, "Share": n.share, "IDType": it, "ID": id, "DOB": n.dob,
				"Guardian": n.guardian, "GuardianRelation": n.grel, "Mobile": cleanMobile(n.mobile), "Email": n.email, "Address": n.addr, "DeclaredOn": n.date})); err != nil {
				cw.Close()
				return err
			}
		}
	}
	if c.primary && c.cm.dpNomNoPAN > 0 {
		c.rep.infof("Nominees", "", "dp-nominee-pan", "%d DP-only clients' nominees (CDNOMREG) have a PAN that is not a valid PAN; it is left out", c.cm.dpNomNoPAN)
	}
	return cw.Close()
}

// ---- segment status ----

func (c *cur) writeSegStatus(ctx context.Context) error {
	h := headers[srcSegStatus]
	cw, err := c.w.Master(c.f.Code, srcSegStatus, h)
	if err != nil {
		return err
	}
	noSeg := map[string]bool{}
	for _, u := range c.clientList() {
		if !c.written[u] {
			continue
		}
		rows, _ := c.segHistory(u)
		sort.SliceStable(rows, func(i, j int) bool { return rows[i][2] < rows[j][2] })
		for _, r := range rows {
			if err := cw.Write(rec(h, map[string]string{"UCC": u, "Segment": r[0], "Status": r[1], "EffectiveDate": r[2], "Reason": r[3]})); err != nil {
				cw.Close()
				return err
			}
		}
		for _, e := range c.cm.status[u] {
			if contains(c.f.COs, e.co) && c.f.segOf(e.co) == "" {
				noSeg[e.co] = true
			}
		}
	}
	for _, co := range sortedKeys(noSeg) {
		c.rep.infof("Segment status", co, "co-no-segment", "Statuses of SharePro company %s (%s) are not an exchange segment and are not migrated", co, firstNonEmpty(c.f.Seg[co].label(), "unknown segment"))
	}
	return cw.Close()
}

// knownPartner gives a client's AP or RM code as written to PARTNERS. A
// code that is not in SharePro's masters is left blank with a warning, as
// Ledgerline refuses a client pointing at a partner it does not have.
func (c *cur) knownPartner(ucc, raw, prefix string) string {
	code := partnerCode(raw, prefix)
	if code == "" || c.partnerCodes == nil {
		return code
	}
	if c.partnerCodes[code] == "" {
		kind := map[string]string{"AP": "authorised person", "RM": "relationship manager"}[prefix]
		c.rep.warnf("Client master", ucc, "unknown-"+kind, "Add it to the masters in SharePro, or set it in Ledgerline after the load",
			"The client's %s %s is not in SharePro's masters; it is left blank", kind, strings.TrimSpace(raw))
		return ""
	}
	return code
}
