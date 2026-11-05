package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// ---- CDSL's client master export (CLN_MSTR, harmonised UDiFF layout) ----
//
// CDSL's client master export (UDiFF catalogue: CLIENT_MASTER, 186 fields)
// has one line per purpose of each demat account: the first holder with
// each of its addresses (CORAD, PERAD, BBADD), joint holders, nominees and
// their guardians, POA / DDPI links (POALB), BO-UCC links (BOUCC), CM
// mappings (MPCM) and so on; the lines of one account share the record
// number, and the account-level fields are on the first holder's
// correspondence-address line (DFT elsewhere). An incremental file (I) has
// the accounts that changed, a full file (F) every account of the DP.
//
// For each account linked to a client (by the BO ID on the BO account or
// the client's demat) Ledgerline records what CDSL says on the BO account
// and acts on it:
//
//   - account status (ACT, PAC, CLS, TBC …) with the closure or status
//     change reason and the closure date: a closed account is closed here
//     too, and a pending one marked so; a DP freeze is left alone;
//   - BSDA flag (BSDA, non-BSDA, opted out) and the BO category;
//   - POA / DDPI per demat: the links, their nature (settlement, margin
//     pledge, mutual fund, tender offer) and validity. The account has
//     DDPI (BOAccount.DDPI) when an active link for settlement (or a
//     general-purpose / stock-broker POA) covers the first or all holders
//     and, if the firm's DDPI POA ID is set, is to that master. DDPI is
//     what decides how a sale is delivered: with it, by DDPI; without it,
//     the client authorises by eDIS (TPIN) or gives a slip (live.go,
//     cdsledis.go, cdslepi.go);
//   - nomination flag and nominees, PAN verification, verified mobile and
//     email flags, the clearing members of the BO-UCC links and CM
//     mappings, and CDSL's freeze status (cdslfreeze.go).
//
// The client master's KYC details (name, PAN, date of birth, mobile,
// email, bank, nomination) are never overwritten: where CDSL differs from
// Ledgerline's record, a work item lists the differences for review.
// Aadhaar (UID) is never stored: it is masked in the stored copy of the
// file (cdslMaskUID) and not read. A full file's last part (_P0) also
// lists the DP's accounts Ledgerline has that CDSL's file does not.

const cCDSLCMRun = "cdslcmrun" // the last client master loads, by file

// cdslCMLine are one account's lines.
type cdslCMLine struct {
	bo    string
	rows  []xfile.Row
	first xfile.Row // the account-level line (FH, CORAD)
}

// cdslFileParts reads CLN_MSTR_<DP>_<REQ>_<I/F>_<stamp>_<seq>[_Pn].csv.
var reCDSLExport = regexp.MustCompile(`(?i)^[A-Z_]+?_(\d+)_(\d+)_([A-Z])_(\d{12})_(\d+)(?:_.*?)?(?:_P(\d+))?\.csv`)

func cdslFileParts(name string) (dp, req, kind, part string) {
	m := reCDSLExport.FindStringSubmatch(name)
	if m == nil {
		return "", "", "", ""
	}
	return strings.TrimLeft(m[1], "0"), m[2], strings.ToUpper(m[3]), m[6]
}

// cdslMaskUID masks the Aadhaar (UID) and virtual ID columns of a client
// master file, so the stored copy never holds them: XXXXXXXX and the last
// four digits, as CDSL masks them itself.
func cdslMaskUID(data []byte) []byte {
	clean := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(clean))
	r.FieldsPerRecord, r.LazyQuotes = -1, true
	recs, err := r.ReadAll()
	if err != nil || len(recs) == 0 {
		return data
	}
	cols := map[int]bool{}
	for i, h := range recs[0] {
		if strings.EqualFold(strings.TrimSpace(h), "UID") || strings.EqualFold(strings.TrimSpace(h), "VID") {
			cols[i] = true
		}
	}
	changed := false
	for _, rec := range recs[1:] {
		for i := range cols {
			if i < len(rec) && strings.TrimSpace(rec[i]) != "" {
				if m := maskUID(rec[i]); m != rec[i] {
					rec[i], changed = m, true
				}
			}
		}
	}
	if !changed {
		return data
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.UseCRLF = bytes.Contains(clean, []byte("\r\n"))
	_ = w.WriteAll(recs)
	return b.Bytes()
}

func maskUID(v string) string {
	d := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, v)
	if len(d) < 4 {
		return strings.Repeat("X", len(v))
	}
	return strings.Repeat("X", 8) + d[len(d)-4:]
}

// cdslDFT is "" for CDSL's DFT / DF (no value).
func cdslDFT(v string) string {
	v = strings.TrimSpace(v)
	if v == "DFT" || v == "DF" {
		return ""
	}
	return v
}

// cdslAccountStatus maps CDSL's account status to the BO account's.
var cdslAccountStatus = map[string]string{"ACT": "Active", "PAC": "Pending activation", "CLS": "Closed", "CAN": "Closed", "REJ": "Not opened"}

// cdslPANVerified are the PAN flags that mean the PAN is verified (or exempt).
var cdslPANVerified = map[string]bool{"PANV": true, "PANVAL": true, "PANATC": true, "PANVLD": true, "AEXMPT": true, "PANVNS": true, "EXMPT": true, "YES": true}

// cdslSettlementPOA are the POA natures that let the firm deliver sales.
var cdslSettlementPOA = map[string]bool{"SET": true, "GNP": true, "STB": true}

// CDSLClientMasterResult is what a client master load did.
type CDSLClientMasterResult struct {
	Accounts  int `json:"accounts"`
	Matched   int `json:"matched"`
	Changed   int `json:"changed"`
	Review    int `json:"review"`
	NotLinked int `json:"not_linked"`
	Missing   int `json:"missing"`
}

// loadCDSLClientMaster reads a CLN_MSTR file.
func (o *Ops) loadCDSLClientMaster(ctx context.Context, tenant, actor, file string, rows []xfile.Row) (CDSLClientMasterResult, error) {
	var res CDSLClientMasterResult
	dp, req, kind, part := cdslFileParts(file)
	// The lines by account, in the file's order.
	var accts []*cdslCMLine
	byBO := map[string]*cdslCMLine{}
	for _, r := range rows {
		bo := normDematID(r.Get("ClntId"))
		if bo == "" {
			continue
		}
		a := byBO[bo]
		if a == nil {
			a = &cdslCMLine{bo: bo}
			byBO[bo] = a
			accts = append(accts, a)
		}
		a.rows = append(a.rows, r)
		if a.first.Values == nil && r.Get("Purpse") == "FH" && (r.Get("PurpCd") == "CORAD" || cdslDFT(r.Get("AcctSts")) != "") {
			a.first = r
		}
	}
	res.Accounts = len(accts)
	// BO ID → client: the BO account's BO ID, else the client's demat.
	ucc := o.boClients(ctx, tenant)
	if bos, err := docs.List[BOAccount](ctx, o.Docs, tenant, cBO, ""); err == nil {
		for _, b := range bos {
			if id := normDematID(b.BOID); id != "" {
				ucc[id] = b.UCC
			}
		}
	}
	cfg := o.CDSLPledgeSettings(ctx, tenant)
	today := o.now().In(ist).Format("2006-01-02")
	now := o.now().Format(time.RFC3339)
	var unlinked []string
	for _, a := range accts {
		if a.first.Values == nil {
			a.first = a.rows[0]
		}
		u := ucc[a.bo]
		if u == "" {
			res.NotLinked++
			unlinked = append(unlinked, cdslMaskBO(a.bo))
			continue
		}
		c, err := o.Client(ctx, tenant, u)
		if err != nil {
			res.NotLinked++
			continue
		}
		res.Matched++
		x := cdslAccountFrom(a, cfg.DDPIPOAID, today)
		x.File, x.FileType, x.At = file, kind, now
		if kind == "F" {
			x.FullReq = req
		}
		b := o.bo(ctx, tenant, c)
		if b.BOID == "" {
			b.BOID, b.Depository = a.bo, "CDSL"
		}
		before := b
		changes := o.cdslApplyAccount(ctx, tenant, &b, x, today)
		if err := docs.Upsert(ctx, o.Docs, tenant, cBO, b.UCC, b, actor); err != nil {
			return res, err
		}
		o.refreshCDSLFreeze(ctx, tenant, actor, a.bo, b.UCC)
		if len(changes) > 0 {
			res.Changed++
			o.log(ctx, tenant, actor, "dp.cdsl_client_master", b.UCC, map[string]any{"file": file, "changes": changes})
			o.cdslAccountWork(ctx, tenant, c, before, b, changes)
		}
		if diffs := cdslKYCDiffs(c, a); len(diffs) > 0 {
			res.Review++
			o.raiseWork(ctx, tenant, "cdslcm", "kyc:"+c.UCC+":"+sha256Hex([]byte(strings.Join(diffs, "|")))[:12], "Review "+c.UCC+": CDSL's client master differs",
				"CDSL's client master ("+file+") differs from Ledgerline's record of "+c.Name+": "+strings.Join(diffs, "; ")+
					". Ledgerline has not changed the client: correct the client (with the client's request and documents) or the account at CDSL (BO modify).", c.UCC, RoleMaker, "clients", "")
		}
	}
	if len(unlinked) > 0 {
		sort.Strings(unlinked)
		o.raiseWork(ctx, tenant, "cdslcm", "unlinked:"+file, fmt.Sprintf("%d CDSL accounts not linked to a client", len(unlinked)),
			fmt.Sprintf("CDSL's client master %s has accounts no client's demat matches: %s. Link each to its client (client master → demat account), or ignore the DP's own accounts.", file, strings.Join(unlinked[:min(20, len(unlinked))], ", ")), "", RoleMaker, "dp", "")
	}
	// A full file's last part: the DP's accounts CDSL no longer lists.
	if kind == "F" && (part == "" || part == "0") {
		res.Missing = o.cdslMissingFromFull(ctx, tenant, file, dp, req)
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cCDSLCMRun, file, map[string]any{"file": file, "type": kind, "request": req, "part": part, "at": now, "result": res}, actor)
	return res, nil
}

// cdslAccountFrom reads one account's lines.
func cdslAccountFrom(a *cdslCMLine, firmPOA, today string) BOCDSL {
	f := a.first
	g := func(k string) string { return cdslDFT(f.Get(k)) }
	x := BOCDSL{Status: g("AcctSts"), Product: g("PrdNb"), SubType: g("BnfcrySubTp"), Category: g("BnfcryAcctCtgy"), BSDA: g("BSDAFlg"),
		POAType: g("POAOrDDPITp"), POAMappings: g("NbOfPOAMppng"), NoNomination: g("NoNmntnFlg"), PANFlag: g("PANVrfyFlg"),
		MobileVerified: g("VrfStsMobNb"), EmailVerified: g("VrfStsEmail"), ClosedOn: anyDate(g("BnAccClsrDt")), ActivatedAt: g("ClntActDtTm")}
	x.StatusText = firstOf(cdslCode("cdsl-client-master", "AcctSts", x.Status), x.Status)
	if r := g("StsChgRsnOrClsrRsnCd"); r != "" {
		x.StatusReason = r
		if m := cdslCode("cdsl-client-master", "StsChgRsnOrClsrRsnCd", r); m != "" {
			x.StatusReason += " " + m
		}
	}
	if t := cdslDFT(f.Get("Rmk")); t != "" && x.StatusReason != "" {
		x.StatusReason += " (" + t + ")"
	}
	x.PANVerified = cdslPANVerified[strings.ToUpper(x.PANFlag)]
	if fz := g("FrzSts"); fz != "" {
		x.Freeze = strings.TrimSpace(fz + " " + g("FrznFor"))
	}
	cms, links := map[string]bool{}, map[string]bool{}
	for _, r := range a.rows {
		v := func(k string) string { return cdslDFT(r.Get(k)) }
		switch r.Get("Purpse") {
		case "NM":
			x.Nominees++
		case "POALB":
			x.POA = append(x.POA, BOPOALink{Type: v("POAOrDDPITp"), ID: v("POAOrDDPIId"), RegNo: v("POAId"), MasterID: v("POAMstrID"), Nature: v("GPABPAFlg"),
				Holder: v("PoaLnkPurpCd"), From: anyDate(v("FrDt")), To: anyDate(v("ToDt")), Operate: v("POAToOprtAcct")})
			if x.POAMappings == "" {
				x.POAMappings = v("NbOfPOAMppng")
			}
		case "BOUCC":
			if cm := v("ClrMmbId"); cm != "" {
				cms[cm] = true
			}
			links[strings.Join(strings.Fields(strings.Join([]string{v("Xchg"), v("UnqClntId"), "TM " + v("Brkr"), v("Sgmt"), "CM " + v("ClrMmbId")}, " ")), " ")] = true
		case "MPCM":
			for _, k := range []string{"ClrMmbId", "CmAcctToMap"} {
				if cm := v(k); cm != "" {
					cms[cm] = true
				}
			}
		}
	}
	x.CMIDs, x.UCCLinks = sortedKeys(cms), sortedKeys(links)
	x.DDPIBy = cdslDDPIBy(x.POA, firmPOA, today)
	return x
}

// cdslDDPIBy says which link gives the firm a DDPI (or POA) to deliver
// sales: "" when none does.
func cdslDDPIBy(links []BOPOALink, firmPOA, today string) string {
	for _, l := range links {
		if !cdslSettlementPOA[strings.ToUpper(l.Nature)] {
			continue
		}
		if l.Holder != "" && l.Holder != "FRH" && l.Holder != "ARH" {
			continue
		}
		if l.From != "" && l.From > today || l.To != "" && l.To < today {
			continue
		}
		if firmPOA != "" && firmPOA != l.MasterID && firmPOA != l.ID && firmPOA != l.RegNo {
			continue
		}
		return strings.TrimSpace(firstOf(l.Type, "POA/DDPI") + " " + firstOf(l.RegNo, l.ID) + " (" + l.Nature + ", master " + firstOf(l.MasterID, "—") + ")")
	}
	return ""
}

// cdslApplyAccount puts what CDSL says on the BO account and lists what
// changed. KYC details are not touched here (cdslKYCDiffs).
func (o *Ops) cdslApplyAccount(ctx context.Context, tenant string, b *BOAccount, x BOCDSL, today string) []string {
	var ch []string
	if st, ok := cdslAccountStatus[x.Status]; ok && st != b.Status {
		switch {
		case b.Status == "Frozen" && st == "Active":
			// the DP's own freeze stays until it is lifted here
		case st == "Closed":
			ch = append(ch, "status "+b.Status+" → Closed")
			b.Status, b.ClosedOn = "Closed", firstOf(x.ClosedOn, today)
		default:
			ch = append(ch, "status "+b.Status+" → "+st)
			b.Status = st
			if st == "Active" {
				b.ClosedOn = ""
			}
		}
	}
	switch x.BSDA {
	case "BSDA":
		if !b.BSDA {
			ch = append(ch, "BSDA")
		}
		b.BSDA, b.BSDAOptOut = true, false
	case "NBSD", "OPTO":
		if b.BSDA {
			ch = append(ch, "not BSDA")
		}
		if x.BSDA == "OPTO" && !b.BSDAOptOut {
			ch = append(ch, "opted out of BSDA")
		}
		b.BSDA, b.BSDAOptOut = false, x.BSDA == "OPTO"
	}
	ddpi := x.DDPIBy != ""
	if ddpi != b.DDPI {
		ch = append(ch, map[bool]string{true: "DDPI recorded", false: "no DDPI"}[ddpi])
		b.DDPI = ddpi
	}
	if b.CDSL != nil && b.CDSL.Freeze != x.Freeze && (strings.HasPrefix(x.Freeze, "FRZ") || strings.HasPrefix(b.CDSL.Freeze, "FRZ")) {
		ch = append(ch, "freeze status "+firstOf(b.CDSL.Freeze, "none")+" → "+firstOf(x.Freeze, "none"))
	}
	if b.CDSL == nil && strings.HasPrefix(x.Freeze, "FRZ") {
		ch = append(ch, "freeze status "+x.Freeze)
	}
	if b.CDSL != nil && b.CDSL.FullReq != "" && x.FullReq == "" {
		x.FullReq = b.CDSL.FullReq
	}
	b.CDSL = &x
	return ch
}

// cdslAccountWork raises what the DP desk should know about a change.
func (o *Ops) cdslAccountWork(ctx context.Context, tenant string, c Client, before, b BOAccount, changes []string) {
	x := b.CDSL
	first := before.CDSL == nil // the first load is not news: only changes after it are
	if before.DDPI != b.DDPI && !first {
		if b.DDPI {
			o.raiseWork(ctx, tenant, "cdslcm", "ddpi:"+c.UCC+":on", c.UCC+" has a DDPI at CDSL: sales go by DDPI",
				"CDSL's client master shows "+x.DDPIBy+" on "+cdslMaskBO(b.BOID)+". Sales are now delivered under the DDPI, without eDIS or a slip.", c.UCC, RoleMaker, "dp", "")
		} else {
			o.closeWork(ctx, tenant, SystemActor, "cdslcm", "ddpi:"+c.UCC+":on", "DDPI no longer at CDSL")
			o.raiseWork(ctx, tenant, "cdslcm", "ddpi:"+c.UCC+":off:"+x.File, c.UCC+" has no DDPI at CDSL: sales need eDIS",
				"CDSL's client master shows no active DDPI (or POA for settlement) for the firm on "+cdslMaskBO(b.BOID)+". The client must authorise each sale by eDIS (TPIN) or give a slip; live risk counts unauthorised sales as short.", c.UCC, RoleMaker, "dp", "")
		}
	}
	if before.Status != b.Status && b.Status == "Closed" {
		o.raiseWork(ctx, tenant, "cdslcm", "closed:"+c.UCC, c.UCC+"'s demat account is closed at CDSL",
			fmt.Sprintf("CDSL's client master shows %s closed%s%s. The account is closed in Ledgerline; check nothing is pending (payouts, pledges, DIS) and update the client.",
				cdslMaskBO(b.BOID), map[bool]string{true: " on " + x.ClosedOn, false: ""}[x.ClosedOn != ""], map[bool]string{true: ": " + x.StatusReason, false: ""}[x.StatusReason != ""]), c.UCC, RoleMaker, "dp", "")
	}
	if (before.BSDA != b.BSDA || before.BSDAOptOut != b.BSDAOptOut) && !first {
		o.raiseWork(ctx, tenant, "cdslcm", "bsda:"+c.UCC+":"+x.BSDA, c.UCC+": BSDA at CDSL is "+firstOf(cdslCode("cdsl-client-master", "BSDAFlg", x.BSDA), x.BSDA),
			"CDSL's client master changed the account type ("+strings.Join(changes, ", ")+"). The DP tariff (AMC) follows the account type from the next bill.", c.UCC, RoleMaker, "dp", "")
	}
}

var reIFSCLike = regexp.MustCompile(`^[A-Z]{4}0[A-Z0-9]{6}$`)

// cdslKYCDiffs compares CDSL's details of the account with the client's,
// masking what it shows.
func cdslKYCDiffs(c Client, a *cdslCMLine) []string {
	f := a.first
	g := func(k string) string { return cdslDFT(f.Get(k)) }
	var d []string
	norm := func(s string) string {
		ws := strings.Fields(strings.ToUpper(strings.Map(func(r rune) rune {
			if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == ' ' {
				return r
			}
			return ' '
		}, s)))
		sort.Strings(ws)
		return strings.Join(ws, " ")
	}
	if n := strings.TrimSpace(g("FrstNm") + " " + g("MddlNm") + " " + g("LastNm")); n != "" && c.Name != "" && norm(n) != norm(c.Name) {
		d = append(d, fmt.Sprintf("name (CDSL %q, Ledgerline %q)", strings.Join(strings.Fields(n), " "), c.Name))
	}
	if p := strings.ToUpper(g("PAN")); p != "" && c.PAN != "" && p != strings.ToUpper(c.PAN) {
		d = append(d, fmt.Sprintf("PAN (CDSL %s, Ledgerline %s)", MaskPAN(p), MaskPAN(c.PAN)))
	}
	if b := anyDate(g("BirthDt")); b != "" && c.DOB != "" && b != c.DOB {
		d = append(d, fmt.Sprintf("date of birth (CDSL %s, Ledgerline %s)", b, c.DOB))
	}
	last10 := func(s string) string {
		s = strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, s)
		return s[max(0, len(s)-10):]
	}
	if m := last10(g("MobNb")); m != "" && c.Mobile != "" && m != last10(c.Mobile) {
		d = append(d, fmt.Sprintf("mobile (CDSL %s, Ledgerline %s)", maskTail(m), maskTail(c.Mobile)))
	}
	if e := strings.ToLower(g("EmailAdr")); e != "" && c.Email != "" && e != strings.ToLower(strings.TrimSpace(c.Email)) {
		d = append(d, fmt.Sprintf("email (CDSL %s, Ledgerline %s)", maskEmail(e), maskEmail(c.Email)))
	}
	if i := strings.ToUpper(g("INIFSC")); reIFSCLike.MatchString(i) && c.Bank.IFSC != "" && i != strings.ToUpper(c.Bank.IFSC) {
		d = append(d, fmt.Sprintf("bank IFSC (CDSL %s, Ledgerline %s)", i, c.Bank.IFSC))
	}
	if n := g("BnfcryBkAcctNb"); reAcct.MatchString(n) && c.Bank.Account != "" && n != c.Bank.Account {
		d = append(d, fmt.Sprintf("bank account (CDSL %s, Ledgerline %s)", MaskAccount(n), MaskAccount(c.Bank.Account)))
	}
	nominees := 0
	for _, r := range a.rows {
		if r.Get("Purpse") == "NM" {
			nominees++
		}
	}
	switch strings.ToUpper(g("NoNmntnFlg")) {
	case "NO": // nomination opted
		if c.Nominee == "Opted out" && len(c.Nominees) == 0 {
			d = append(d, fmt.Sprintf("nomination (CDSL: opted, %d nominees; Ledgerline: opted out)", nominees))
		} else if len(c.Nominees) > 0 && nominees > 0 && len(c.Nominees) != nominees {
			d = append(d, fmt.Sprintf("nominees (CDSL %d, Ledgerline %d)", nominees, len(c.Nominees)))
		}
	case "YES": // not opted / opted out
		if c.Nominee == "Opted in" || len(c.Nominees) > 0 {
			d = append(d, "nomination (CDSL: opted out or not given; Ledgerline: nominees on record)")
		}
	}
	return d
}

// cdslMissingFromFull raises the DP's accounts that a full client master
// (all its parts loaded) did not list.
func (o *Ops) cdslMissingFromFull(ctx context.Context, tenant, file, dp, req string) int {
	cs, _ := o.ClientMap(ctx, tenant)
	var miss []string
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Demat.ID == "" || !strings.EqualFold(firstOf(c.Demat.Depository, "CDSL"), "CDSL") {
			continue
		}
		b := o.bo(ctx, tenant, c)
		if b.Status == "Closed" || b.Status == "Not opened" || !reCDSLBO.MatchString(normDematID(b.BOID)) || dp != "" && cdxBODP(b.BOID) != dp {
			continue
		}
		if b.CDSL == nil || b.CDSL.FullReq != req {
			miss = append(miss, b.UCC)
		}
	}
	if len(miss) > 0 {
		sort.Strings(miss)
		o.raiseWork(ctx, tenant, "cdslcm", "missing:"+req, fmt.Sprintf("%d demat accounts are not in CDSL's full client master", len(miss)),
			fmt.Sprintf("CDSL's full client master (%s, request %s) does not list these accounts Ledgerline has as open at this DP: %s. Check each account's BO ID and status in CDAS (if the file came in parts, load them all; the _P0 part is the last).", file, req, strings.Join(miss[:min(30, len(miss))], ", ")), "", RoleMaker, "dp", "")
	}
	return len(miss)
}
