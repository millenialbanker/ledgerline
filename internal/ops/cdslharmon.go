package ops

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// ---- CDSL's harmonised (UDiFF) depository files ----
//
// SEBI's Unified Distilled File Formats put every depository report and
// upload in one CSV layout per kind, named by ISO 20022 tags (UDiFF
// catalogue, depository formats: ISIN master, ISIN rate master, CC
// calendar, corporate action master, member master, statement of holding,
// eDIS, statement of transaction, COD export, transaction upload and its
// response, client master, BO upload and its response, DIS master). The
// layouts are in config/formats (cdsl-*.json) from catalogue V3.0.1.4. This
// file loads the ones that have no older equivalent here and writes the
// harmonised BO upload:
//
//   - client master (cdslclient.go) and the DPE6 freeze master
//     (cdslfreeze.go, not a UDiFF file);
//   - DIS master: slips CDSL has cancelled, blocked or inactivated are
//     marked lost on their book so they cannot be accepted; slips used at
//     CDSL that Ledgerline has not recorded, slips lost here but active
//     there, and slips Ledgerline did not issue go to the work queue;
//   - corporate action master: kept; actions on ISINs clients hold that
//     are not set up in Corporate actions go to the work queue;
//   - eDIS: kept; authorisations CDSL reports that Ledgerline did not
//     request go to the work queue;
//   - BO upload response: accepted lines complete the CDSL request (an
//     account opening takes its BO ID), rejected ones reject it with
//     CDSL's error;
//   - transaction upload response: each uploaded slip is executed
//     (accepted) or failed (rejected, with CDSL's error).
//
// BO upload format. CDSL takes BO setup and modify uploads in the
// fixed-width layout of communiqué CDSL/OPS/SYSTM/2023/404 (08 and 11
// files, cdslbofile.go) and in the harmonised CSV layout (BO_UPLD). The
// catalogue gives no date from which the CSV upload is mandatory (its
// Principles sheet has none, and the Change Log's only applicability
// dates are for the nomination opt-out flag and two freeze reason codes),
// so the BO defaults keep fixed-width unless the DP chooses CSV. Both are
// written from the same records (cdslbomap.go); for CSV each fixed-width
// line becomes a harmonised line by purpose, and CDSL's old code values
// are converted to the catalogue's standard values (cdslcodes.go).

const (
	cCDSLCA   = "cdslca"    // corporate actions from CA_EXP, by event ID and counter
	cCDSLEDIS = "cdsledisx" // eDIS authorisations from EDIS, by CDSL's transaction ID

	cdslFmtFixed = "Fixed-width"
	cdslFmtCSV   = "CSV"
)

// loadCDSLXFile handles CDSL's harmonised files and the freeze master;
// false for other layouts.
func (o *Ops) loadCDSLXFile(ctx context.Context, tenant, actor string, s *xfile.Spec, f xfile.File, rows []xfile.Row, r *XFileResult) bool {
	fail := func(err error) { r.Loaded = "Not loaded: " + strings.TrimPrefix(err.Error(), "invalid: ") }
	switch s.ID {
	case "cdsl-client-master":
		res, err := o.loadCDSLClientMaster(ctx, tenant, actor, f.Name, rows)
		if err != nil {
			fail(err)
			return true
		}
		r.Loaded = fmt.Sprintf("Loaded into the BO accounts: %d of %d accounts matched, %d changed, %d to review", res.Matched, res.Accounts, res.Changed, res.Review)
		r.Added, r.Skipped = res.Matched, res.NotLinked
		if res.NotLinked > 0 {
			r.Note = fmt.Sprintf("%d accounts are not linked to a client (see the work queue).", res.NotLinked)
		}
		if res.Missing > 0 {
			r.Note = strings.TrimSpace(r.Note + fmt.Sprintf(" %d of the DP's accounts in Ledgerline are not in this full file.", res.Missing))
		}
	case "cdsl-freeze-master":
		added, lifted, skipped, err := o.loadCDSLFreezes(ctx, tenant, actor, f.Name, rows)
		if err != nil {
			fail(err)
			return true
		}
		r.Loaded = fmt.Sprintf("Loaded as CDSL freezes: %d new, %d lifted (DIS, eDIS, pledges and pay-outs of frozen securities are blocked)", added, lifted)
		r.Added, r.Skipped = added+lifted, skipped
	case "cdsl-dis-master":
		n, issues := o.loadCDSLDISMaster(ctx, tenant, actor, f.Name, rows)
		r.Loaded, r.Added = fmt.Sprintf("Checked against the DIS books: %d slips matched, %d to review", n, issues), n
	case "cdsl-corporate-action":
		n, held := o.loadCDSLCorpActions(ctx, tenant, actor, f.Name, rows)
		r.Loaded, r.Added = fmt.Sprintf("Kept as CDSL's corporate actions: %d, %d on ISINs clients hold", n, held), n
	case "cdsl-edis":
		n, unknown := o.loadCDSLEDISFile(ctx, tenant, actor, f.Name, rows)
		r.Loaded, r.Added = fmt.Sprintf("Kept as CDSL's eDIS authorisations: %d, %d not requested through Ledgerline", n, unknown), n
	case "cdsl-bo-response":
		done, rejected, unmatched := o.loadCDSLBOResponse(ctx, tenant, actor, f.Name, rows)
		r.Loaded, r.Added, r.Skipped = fmt.Sprintf("Applied to CDSL requests: %d accepted, %d rejected", done, rejected), done+rejected, unmatched
	case "cdsl-txn-response":
		exec, failed, unmatched := o.loadCDSLTxnResponse(ctx, tenant, actor, f.Name, rows)
		r.Loaded, r.Added, r.Skipped = fmt.Sprintf("Applied to uploaded delivery instructions: %d executed, %d failed", exec, failed), exec+failed, unmatched
	default:
		return false
	}
	return true
}

// ---- DIS master ----

var reDigitRuns = regexp.MustCompile(`\d+`)

// slipNo reads a DIS serial number (it may carry letters, as "PRA 101").
func slipNo(s string) (int64, bool) {
	ds := reDigitRuns.FindAllString(s, -1)
	if len(ds) == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(ds[len(ds)-1], 10, 64)
	return n, err == nil
}

// cdslDISBlocked are the DIS statuses that mean a slip can no longer be used.
var cdslDISBlocked = map[string]bool{"CAN": true, "DCM": true, "DCR": true, "BLK": true, "IBC": true, "DBM": true}

func (o *Ops) loadCDSLDISMaster(ctx context.Context, tenant, actor, file string, rows []xfile.Row) (matched, issues int) {
	by := o.boClients(ctx, tenant)
	books, _ := docs.List[DISBook](ctx, o.Docs, tenant, cDISBook, "")
	notes := map[string][]string{}
	for _, r := range rows {
		bo := normDematID(r.Get("SrcClntIdOrBnfclOwnrId"))
		ucc := by[bo]
		if ucc == "" {
			continue
		}
		slip, ok := slipNo(r.Get("DISSrlNb"))
		if !ok {
			continue
		}
		st := strings.ToUpper(strings.TrimSpace(r.Get("DISSts")))
		what := firstOf(cdslCode("cdsl-dis-master", "DISSts", st), st)
		var book *DISBook
		for i := range books {
			if books[i].UCC == ucc && slip >= books[i].From && slip <= books[i].To {
				book = &books[i]
			}
		}
		if book == nil {
			if !cdslDISBlocked[st] {
				notes[ucc] = append(notes[ucc], fmt.Sprintf("slip %s is %s at CDSL but was not issued from a book here", r.Get("DISSrlNb"), strings.ToLower(what)))
			}
			continue
		}
		matched++
		used, lost := contains(i64s(book.Used), fmt.Sprint(slip)), contains(i64s(book.Lost), fmt.Sprint(slip))
		switch {
		case cdslDISBlocked[st] && !lost && !used:
			_, _ = docs.Update(ctx, o.Docs, tenant, cDISBook, book.ID, actor, func(x *DISBook) error { x.Lost = append(x.Lost, slip); return nil })
			book.Lost = append(book.Lost, slip)
			reason := firstOf(cdslCode("cdsl-dis-master", "DISCxlFlg", r.Get("DISCxlFlg")), "")
			notes[ucc] = append(notes[ucc], fmt.Sprintf("slip %d is %s at CDSL%s: marked lost here so it cannot be accepted", slip, strings.ToLower(what), map[bool]string{true: " (" + reason + ")", false: ""}[reason != ""]))
		case st == "USE" && !used:
			notes[ucc] = append(notes[ucc], fmt.Sprintf("slip %d is used at CDSL but no instruction on it was entered here: check how it was executed", slip))
		case (st == "ACT" || st == "DSC") && lost:
			notes[ucc] = append(notes[ucc], fmt.Sprintf("slip %d is lost here but active at CDSL: cancel it at CDSL", slip))
		}
	}
	for _, u := range sortedKeys(notes) {
		issues += len(notes[u])
		o.raiseWork(ctx, tenant, "cdsldis", u+":"+file, "DIS master differs for "+u, "CDSL's DIS master "+file+": "+strings.Join(notes[u], "; ")+".", u, RoleMaker, "dp", "")
	}
	return matched, issues
}

// ---- corporate action master ----

// CDSLCorpAction is a corporate action from CDSL's CA_EXP.
type CDSLCorpAction struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	TypeText   string `json:"type_text,omitempty"`
	ISIN       string `json:"isin"`
	CreditISIN string `json:"credit_isin,omitempty"`
	Ratio      string `json:"ratio,omitempty"`
	ExecDate   string `json:"exec_date,omitempty"`
	RecordDate string `json:"record_date,omitempty"`
	PerShare   string `json:"per_share,omitempty"`
	Status     string `json:"status"`
	Remarks    string `json:"remarks,omitempty"`
	File       string `json:"file"`
}

func (o *Ops) loadCDSLCorpActions(ctx context.Context, tenant, actor, file string, rows []xfile.Row) (n, held int) {
	hs, _ := o.Holdings(ctx, tenant, "")
	hold := map[string]int{}
	for _, h := range sumHoldings(hs) {
		if h.Qty.Sign() > 0 {
			hold[h.ISIN]++
		}
	}
	have := map[string]bool{}
	if cas, err := o.CorpActions(ctx, tenant); err == nil {
		for _, c := range cas {
			have[c.ISIN+"|"+c.RecordDate] = true
		}
	}
	for _, r := range rows {
		a := CDSLCorpAction{ID: strings.TrimSpace(r.Get("CorpActnEvtId")) + "|" + firstOf(strings.TrimSpace(r.Get("CACntrNb")), "1"), Type: cdslDFT(r.Get("CATyp")),
			ISIN: strings.ToUpper(strings.TrimSpace(r.Get("PrntISIN"))), CreditISIN: strings.ToUpper(strings.TrimSpace(r.Get("CdtISIN"))), Ratio: strings.TrimSpace(r.Get("Rtio")),
			ExecDate: r.Get("ExecDt"), RecordDate: r.Get("RcrdDt"), PerShare: r.Get("DvddIntrstAmtPerScty"), Status: cdslDFT(r.Get("CorpActnSts")), Remarks: strings.TrimSpace(r.Get("Rmks")), File: file}
		if a.ISIN == "" {
			continue
		}
		a.TypeText = cdslCode("cdsl-corporate-action", "CATyp", a.Type)
		if err := docs.Upsert(ctx, o.Docs, tenant, cCDSLCA, a.ID, a, actor); err != nil {
			continue
		}
		n++
		switch a.Status {
		case "CLSD", "DELT", "RCDV", "VCCD", "AUCL":
			continue
		}
		if hold[a.ISIN] > 0 {
			held++
			if !have[a.ISIN+"|"+a.RecordDate] {
				o.raiseWork(ctx, tenant, "cdslca", a.ID, fmt.Sprintf("CDSL corporate action on %s (%s): %d clients hold it", a.ISIN, firstOf(a.TypeText, a.Type), hold[a.ISIN]),
					fmt.Sprintf("CDSL's corporate action %s: %s on %s%s, ratio %s, record date %s, execution %s%s. Set it up in Corporate actions so holdings, lots and dividends follow.",
						a.ID, firstOf(a.TypeText, a.Type), a.ISIN, map[bool]string{true: " → " + a.CreditISIN, false: ""}[a.CreditISIN != "" && a.CreditISIN != a.ISIN], firstOf(a.Ratio, "—"), firstOf(a.RecordDate, "—"), firstOf(a.ExecDate, "—"),
						map[bool]string{true: ", ₹" + a.PerShare + " per security", false: ""}[strings.Trim(a.PerShare, "0.") != ""]), "", RoleMaker, "corporate", "")
			}
		}
	}
	return n, held
}

// ---- eDIS export ----

// CDSLEDISRecord is an eDIS authorisation from CDSL's EDIS file.
type CDSLEDISRecord struct {
	TxnID    string `json:"txn_id"`
	BOID     string `json:"bo_id"`
	UCC      string `json:"ucc,omitempty"`
	ISIN     string `json:"isin"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Qty      string `json:"qty"`
	Used     string `json:"used"`
	Pending  string `json:"pending"`
	Exchange string `json:"exchange,omitempty"`
	CMID     string `json:"cm_id,omitempty"`
	At       string `json:"at,omitempty"`
	Known    bool   `json:"known"` // requested through Ledgerline
	File     string `json:"file"`
}

func (o *Ops) loadCDSLEDISFile(ctx context.Context, tenant, actor, file string, rows []xfile.Row) (n, unknown int) {
	by := o.boClients(ctx, tenant)
	known := map[string]bool{}
	as, _ := docs.List[EDISAuth](ctx, o.Docs, tenant, cEDIS, "")
	for _, a := range as {
		for _, l := range a.Lines {
			if l.TxnID != "" {
				known[strings.TrimLeft(l.TxnID, "0")] = true
			}
		}
	}
	var odd []string
	for _, r := range rows {
		x := CDSLEDISRecord{TxnID: strings.TrimSpace(r.Get("TxnId")), BOID: normDematID(r.Get("ClntId")), ISIN: strings.ToUpper(r.Get("ISIN")), Type: r.Get("EdisTxnTyp"), Status: r.Get("EdisStatus"),
			Qty: r.Get("EDISTxnQty"), Used: r.Get("EDISExecQty"), Pending: r.Get("EDISPendQty"), Exchange: r.Get("Xchg"), CMID: r.Get("ClrMmbId"), At: r.Get("TxnDt"), File: file}
		if x.TxnID == "" {
			continue
		}
		x.UCC = by[x.BOID]
		x.Known = known[strings.TrimLeft(x.TxnID, "0")]
		if err := docs.Upsert(ctx, o.Docs, tenant, cCDSLEDIS, x.TxnID, x, actor); err != nil {
			continue
		}
		n++
		if !x.Known && x.UCC != "" {
			unknown++
			odd = append(odd, fmt.Sprintf("%s %s %s (%s)", x.UCC, x.ISIN, x.Qty, x.TxnID))
		}
	}
	if len(odd) > 0 {
		o.raiseWork(ctx, tenant, "cdsledis", "unknown:"+file, fmt.Sprintf("%d eDIS authorisations not requested through Ledgerline", len(odd)),
			"CDSL's eDIS file "+file+" has authorisations Ledgerline has no request for: "+strings.Join(odd[:min(20, len(odd))], "; ")+". Check they were the clients' own (another platform, or CDSL's site).", "", RoleCompliance, "edis", "")
	}
	return n, unknown
}

// ---- upload responses ----

// cdslRespGroups gathers a response file's lines by their reference: for
// each, whether any line was rejected, the errors and the BO or
// transaction ID.
type cdslResp struct {
	ok, rejected bool
	errs         []string
	id           string
}

func cdslRespGroups(rows []xfile.Row, idKey string) (map[string]*cdslResp, []string) {
	out := map[string]*cdslResp{}
	var order []string
	for _, r := range rows {
		ref := strings.TrimSpace(r.Get("IntlRefNb"))
		if ref == "" {
			continue
		}
		g := out[ref]
		if g == nil {
			g = &cdslResp{}
			out[ref] = g
			order = append(order, ref)
		}
		switch strings.ToUpper(strings.TrimSpace(r.Get("Status"))) {
		case "A":
			g.ok = true
		case "R":
			g.rejected = true
			e := strings.TrimSpace(strings.Join(strings.Fields(r.Get("ErrCD")+" "+r.Get("ErrDesc")), " "))
			if e != "" && !contains(g.errs, e) {
				g.errs = append(g.errs, e)
			}
		}
		if id := strings.TrimSpace(r.Get(idKey)); id != "" {
			g.id = id
		}
	}
	return out, order
}

// loadCDSLBOResponse applies a BO_RESP file to the CDSL requests whose
// upload it answers (matched by the internal reference sent, the UCC).
func (o *Ops) loadCDSLBOResponse(ctx context.Context, tenant, actor, file string, rows []xfile.Row) (done, rejected, unmatched int) {
	groups, order := cdslRespGroups(rows, "ClntId")
	rs, _ := docs.List[CDSLRequest](ctx, o.Docs, tenant, cCDSLReq, "")
	now := o.now().Format(time.RFC3339)
	for _, ref := range order {
		g := groups[ref]
		var r *CDSLRequest
		for i := range rs {
			x := &rs[i]
			if (x.UCC == strings.ToUpper(ref) || x.ID == ref) && (x.Kind == CDSLOpen || x.Kind == CDSLSetupUpload || x.Kind == CDSLModify) && (x.Status == crSent || x.Status == crApproved || x.Status == crManual) {
				if r == nil || x.ID > r.ID {
					r = x
				}
			}
		}
		if r == nil {
			unmatched++
			continue
		}
		switch {
		case g.rejected:
			r.Status, r.Error = crRejected, "CDSL rejected the upload: "+firstOf(strings.Join(g.errs, "; "), "no reason given")
			r.event(now, actor, r.Error+" ("+file+")")
			o.raiseWork(ctx, tenant, "cdslbo", r.ID, "CDSL rejected "+strings.ToLower(r.Kind)+" "+r.ID, r.UCC+" "+r.Name+": "+r.Error+". Correct the details and upload again.", r.UCC, RoleMaker, "cdslreq", "")
			rejected++
		case g.ok:
			if (r.Kind == CDSLOpen || r.Kind == CDSLSetupUpload) && reCDSLBO.MatchString(g.id) && r.BOID == "" {
				if err := o.cdslApplyBOID(ctx, tenant, actor, r, g.id, nil); err != nil {
					unmatched++
					continue
				}
			}
			r.Status, r.DoneAt, r.Error = crCompleted, now, ""
			r.event(now, actor, "CDSL accepted the upload ("+file+")"+map[bool]string{true: ": BO ID " + cdslMaskBO(g.id), false: ""}[g.id != ""])
			o.closeWork(ctx, tenant, actor, "cdslbo", r.ID, "accepted")
			o.cdslAfterUpload(ctx, tenant, actor, r)
			done++
		default:
			unmatched++
			continue
		}
		_ = o.cdslSave(ctx, tenant, actor, r)
	}
	return done, rejected, unmatched
}

// loadCDSLTxnResponse applies a TXN_RESP file to the uploaded delivery
// instructions (the internal reference is the instruction's ID).
func (o *Ops) loadCDSLTxnResponse(ctx context.Context, tenant, actor, file string, rows []xfile.Row) (exec, failed, unmatched int) {
	groups, order := cdslRespGroups(rows, "TxUnqId")
	for _, ref := range order {
		g := groups[ref]
		d, _, err := docs.Get[DIS](ctx, o.Docs, tenant, cDIS, ref)
		if err != nil || d.Status != "Uploaded" {
			unmatched++
			continue
		}
		switch {
		case g.rejected:
			if _, err := o.ExecuteDIS(ctx, tenant, actor, d.ID, false, "CDSL rejected: "+firstOf(strings.Join(g.errs, "; "), "no reason given"), ""); err == nil {
				failed++
			} else {
				unmatched++
			}
		case g.ok:
			if _, err := o.ExecuteDIS(ctx, tenant, actor, d.ID, true, "", ""); err == nil {
				exec++
			} else {
				unmatched++
				o.raiseWork(ctx, tenant, "cdsl", "response:"+d.ID, "Could not record "+d.ID+" as executed", "CDSL accepted "+d.ID+" ("+file+") but Ledgerline could not record it: "+err.Error()+".", d.UCC, RoleMaker, "dp", "")
			}
		default:
			unmatched++
		}
	}
	return exec, failed, unmatched
}

// ---- the harmonised BO upload (BO_UPLD) ----

// cdslBOUploadFormat is the DP's choice of BO upload layout.
func (o *Ops) cdslBOUploadFormat(ctx context.Context, tenant string) string {
	if strings.EqualFold(strings.TrimSpace(o.CDSLBODefaultsNow(ctx, tenant).Values["bo_upload_format"]), cdslFmtCSV) {
		return cdslFmtCSV
	}
	return cdslFmtFixed
}

// boCSVMap is where a fixed-width field goes in the harmonised upload:
// its ISO tag and the tag whose old codes it is converted by ("" as is).
type boCSVMap struct{ Tag, Codes string }

var (
	boCSVName = map[string]boCSVMap{"name": {"FrstNm", ""}, "middle": {"MddlNm", ""}, "last": {"LastNm", ""}, "title": {"Titl", ""}, "suffix": {"FSfx", ""}, "father": {"ScndHldrNmOfFthr", ""}}
	boCSVAddr = map[string]boCSVMap{"addr1": {"Adr1", ""}, "addr2": {"Adr2", ""}, "addr3": {"Adr3", ""}, "city": {"Adr4", ""}, "country": {"Ctry", "Ctry"}, "zip": {"PstCd", ""},
		"state_code": {"CtrySubDvsnCd", "CtrySubDvsnCd"}, "city_seq": {"CitySeqNb", ""}, "fax": {"FaxNb", ""}}
	boCSVHolder = map[string]boCSVMap{"product": {"PrdNb", "PrdNb"}, "smart_reg": {"SMSFclty", "SMSFclty"}, "isd": {"PrmryISDCd", ""}, "mobile": {"MobNb", ""}, "isd2": {"ScndryISDCd", ""},
		"mobile2": {"PhneNb", ""}, "email2": {"AltrnEmailAdr", ""}, "pan": {"PAN", ""}, "uid_flag": {"AdhrAuthntcnWthUID", "AdhrAuthntcnWthUID"}, "it_circle": {"ITCrcl", ""}, "email": {"EmailAdr", ""},
		"lei": {"LEI", ""}, "pan_exempt": {"PANXmptnCd", "PANXmptnCd"}, "del_flag": {"DelFlg", "DelFlg"}, "del_reason": {"RsnCdDeltn", "RsnCdDeltn"}, "death_date": {"DtOfDeath", ""},
		"open_source": {"AccntOpSrc", "AccntOpSrc"}, "name_reason": {"NmChgRsnCd", "NmChgRsnCd"}}
	boCSVProduct = map[string]boCSVMap{"internal_ref": {"SndrRefNb1", ""}, "dob": {"BirthDt", ""}, "maturity": {"BirthDt", ""}, "sex": {"Gndr", "Gndr"}, "occupation": {"Ocptn", "Ocptn"},
		"geo": {"GeoCd", "GeoCd"}, "education": {"EdctnLvl", "EdctnLvl"}, "income": {"GrssAnlIncmRg", "GrssAnlIncmRg"}, "nationality": {"Ntlty", "Ntlty"}, "nom_opt_out": {"NoNmntnFlg", "NoNmntnFlg"},
		"bonafide": {"OneTmDclrtnFlgForGSECIDT", "OneTmDclrtnFlgForGSECIDT"}, "family": {"FmlyFlgForEmailAdr", "FmlyFlgForEmailAdr"}, "email_stmt": {"FrstClntOptnToRcvElctrncStmtFlg", "FrstClntOptnToRcvElctrncStmtFlg"},
		"cas_mode": {"CASMd", "CASMd"}, "annual_report": {"AnlRptFlg", "AnlRptFlg"}, "pledge_si": {"PldgStgInstrFlg", "PldgStgInstrFlg"}, "email_rta": {"EmailRTADwnldFlg", "EmailRTADwnldFlg"},
		"bsda": {"BSDAFlg", "BSDAFlg"}, "mode_op": {"MdOfOpr", "MdOfOpr"}, "comm_pref": {"ComToBeSentTo", "ComToBeSentTo"}, "bo_category": {"BnfcryAcctCtgy", "BnfcryAcctCtgy"},
		"ifsc": {"INIFSC", ""}, "rbi_ref": {"RBIRefNb", ""}, "rbi_date": {"RBIApprvdDt", ""}, "sebi_reg": {"SEBIRegNb", ""}, "tax_status": {"BnfcryTaxDdctnSts", "BnfcryTaxDdctnSts"},
		"ecs": {"Mndt", "Mndt"}, "div_ccy": {"DvddCcy", ""}, "sub_status": {"BnfcrySubTp", "BnfcrySubTp"}, "cc_id": {"ClrSysId", "ClrSysId"}, "cm_id": {"ClrMmbId", ""},
		"stock_exchange": {"Xchg", "Xchg"}, "conf_waived": {"StgInstr", "StgInstr"}, "trading_id": {"TradgId", ""}, "stmt_cycle": {"BnfclOwnrSttlmCyclCd", "BnfclOwnrSttlmCyclCd"},
		"custodian_email": {"CustdPmsEmailId", ""}, "bank_acct_type": {"BkAcctTp", "BkAcctTp"}, "bank_code": {"MICRCd", ""}, "bank_acct": {"BnfcryBkAcctNb", ""}, "bank_ccy": {"DvddBkCcy", ""}}
	boCSVPOA = map[string]boCSVMap{"poa_id": {"POAId", ""}, "setup_date": {"SetUpDt", ""}, "poa_operate": {"POAToOprtAcct", "POAToOprtAcct"}, "gpa_flag": {"GPABPAFlg", "GPABPAFlg"},
		"eff_from": {"FrDt", ""}, "eff_to": {"ToDt", ""}, "remark": {"Rmk", ""}, "master_id": {"POAMstrID", ""}, "purpose": {"PoaLnkPurpCd", "PoaLnkPurpCd"}, "txn_type": {"AddModDelInd", "AddModDelInd"}}
	boCSVOther = map[string]boCSVMap{"isd": {"PrmryISDCd", ""}, "mobile": {"MobNb", ""}, "dob": {"BirthDt", ""}, "pan": {"PAN", ""}, "uid_flag": {"AdhrAuthntcnWthUID", "AdhrAuthntcnWthUID"},
		"it_circle": {"ITCrcl", ""}, "email": {"EmailAdr", ""}, "nominee_serial": {"SrlNbr", ""}, "relation": {"RltshWthBnfclOwnr", "RltshWthBnfclOwnr"}, "residual": {"RsdlSecFlg", "RsdlSecFlg"},
		"txn_type": {"AddModDelInd", "AddModDelInd"}, "name_reason": {"NmChgRsnCd", "NmChgRsnCd"}}
	boCSVCM  = map[string]boCSVMap{"map_flag": {"MapUMapFlg", "MapUMapFlg"}, "ex_id": {"Xchg", "Xchg"}, "cm_id": {"ClrMmbId", ""}, "cm_account": {"CmAcctToMap", ""}}
	boCSVUCC = map[string]boCSVMap{"link_flag": {"BOUCCLkFlg", "BOUCCLkFlg"}, "consent": {"CnsntInd", "CnsntInd"}, "exchange_id": {"Xchg", "Xchg"}, "ucc": {"UnqClntId", ""}, "segment": {"Sgmt", "Sgmt"},
		"cm_id": {"ClrMmbId", ""}, "tm_code": {"Brkr", ""}}
)

// boStd converts a fixed-width code to the catalogue's standard value: a
// value that already is one stays; an old value is converted.
func boStd(tag, v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", true
	}
	if cdslCode("cdsl-bo-upload", tag, v) != "" {
		return v, true
	}
	m, ok := cdslOldToStd[tag]
	if !ok {
		return v, true // no code list to convert by
	}
	if tag == "AddModDelInd" && v == "A" {
		v = "S"
	}
	for _, k := range []string{v, strings.TrimLeft(v, "0"), fmt.Sprintf("%02s", strings.TrimLeft(v, "0"))} {
		if s, ok := m[k]; ok {
			return s, true
		}
	}
	return v, false
}

// boISO turns a fixed-width date (DDMMYYYY) or date and time
// (DDMMYYYYHHMMSS) into the catalogue's ISO 8601 form.
func boISO(v string) string {
	if t, err := time.Parse("02012006150405", v); err == nil && len(v) == 14 {
		return t.Format("2006-01-02T15:04:05")
	}
	if t, err := time.Parse("02012006", v); err == nil && len(v) == 8 {
		return t.Format("2006-01-02")
	}
	return v
}

// boPctg is a fixed-width share (5 digits, 2 decimals) as a percentage.
func boPctg(v string) string {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return v
	}
	if n%100 == 0 {
		return strconv.Itoa(n / 100)
	}
	return fmt.Sprintf("%d.%02d", n/100, n%100)
}

// boCSVIn is what a harmonised BO upload is written from.
type boCSVIn struct {
	Records  [][]boLine
	Modify   bool
	DPID     string
	Operator string
	Batch    int64
	At       time.Time
}

// cdslBOUploadRows converts fixed-width BO records into harmonised lines.
func cdslBOUploadRows(in boCSVIn) ([]xfile.Row, []string) {
	var rows []xfile.Row
	var probs []string
	txn := map[bool]string{true: "BOMOD", false: "BOSET"}[in.Modify]
	dp := strings.TrimLeft(strings.TrimSpace(in.DPID), "0")
	if len(dp) < 6 {
		dp = strings.Repeat("0", 6-len(dp)) + dp
	}
	for ri, rec := range in.Records {
		common := map[string]string{"CntrlSctiesDpstryPtcpt": dp, "BrnchId": "000000", "BtchId": fmt.Sprint(in.Batch), "SndrId": in.Operator, "SndrDt": in.At.In(ist).Format("2006-01-02"),
			"RcrdNb": fmt.Sprint(ri + 1), "BOTxnTyp": txn}
		var product string
		var panFlags string
		// The account-level line (first holder, correspondence address)
		// takes line 05's fields.
		var acct map[string]string
		var out []map[string]string
		put := func(v map[string]string, l boLine, m map[string]boCSVMap) {
			for k, x := range m {
				val, ok := l.V[k]
				if !ok || val == "" {
					continue
				}
				if val == boClear {
					probs = append(probs, fmt.Sprintf("%s: %s cannot be blanked in the harmonised upload; use the fixed-width file for this change", firstOf(l.Who, "Record"), strings.ToLower(x.Tag)))
					continue
				}
				val = strings.TrimSpace(val)
				if x.Codes != "" {
					std, ok := boStd(x.Codes, val)
					if !ok {
						probs = append(probs, fmt.Sprintf("%s: code %q of %s has no harmonised value in the UDiFF catalogue", firstOf(l.Who, "Record"), val, x.Tag))
					}
					val = std
				}
				switch x.Tag {
				case "BirthDt", "DtOfDeath", "RBIApprvdDt", "SetUpDt", "FrDt", "ToDt":
					val = boISO(val)
				case "PrdNb":
					product = val
				}
				v[x.Tag] = val
			}
		}
		for _, l := range rec {
			fam := l.Layout[1:]
			switch fam {
			case "01":
				common["ClntId"] = l.V["bo_id"]
				common["RcvDt"] = boISO(l.V["request_date"])
			case "02":
				v := map[string]string{"Purpse": "FH"}
				put(v, l, boCSVName)
				put(v, l, boCSVAddr)
				put(v, l, boCSVHolder)
				if v["Adr1"] != "" {
					v["PurpCd"] = "CORAD"
				}
				panFlags = l.V["pan_verify"]
				if len(panFlags) > 0 {
					if s, ok := boStd("PANVrfyFlg", panFlags[:1]); ok && v["PAN"] != "" {
						v["PANVrfyFlg"] = s
					}
				}
				if acct == nil {
					acct = v
					out = append(out, v)
				} else {
					for k, x := range v {
						acct[k] = x
					}
				}
			case "03":
				v := map[string]string{"Purpse": map[int]string{3: "SH", 4: "TH"}[l.No]}
				put(v, l, boCSVName)
				put(v, l, boCSVHolder)
				if i := l.No - 2; i < len(panFlags) && v["PAN"] != "" {
					if s, ok := boStd("PANVrfyFlg", panFlags[i:i+1]); ok {
						v["PANVrfyFlg"] = s
					}
				}
				out = append(out, v)
			case "05":
				if acct == nil {
					acct = map[string]string{"Purpse": "FH"}
					out = append([]map[string]string{acct}, out...)
				}
				put(acct, l, boCSVProduct)
				if acct["BnfcryAcctCtgy"] == "" && l.V["bo_category"] != "" {
					acct["BnfcryAcctCtgy"] = l.V["bo_category"]
				}
			case "06":
				v := map[string]string{"Purpse": "POALB"}
				put(v, l, boCSVPOA)
				out = append(out, v)
			case "07":
				v := map[string]string{"Purpse": "FH", "PurpCd": "PERAD"}
				put(v, l, boCSVName)
				put(v, l, boCSVAddr)
				if p := l.V["pan"]; p != "" {
					v["PAN"] = p
				}
				out = append(out, v)
			case "08":
				p, ok := boStd("Purpse", l.V["purpose"])
				if !ok {
					probs = append(probs, fmt.Sprintf("%s: purpose code %q has no harmonised value", firstOf(l.Who, "Record"), l.V["purpose"]))
				}
				v := map[string]string{"Purpse": p}
				put(v, l, boCSVName)
				put(v, l, boCSVAddr)
				put(v, l, boCSVOther)
				ser := strings.TrimLeft(l.V["nominee_serial"], "0")
				switch p {
				case "NM":
					v["PurpCd"] = map[string]string{"1": "NOMAD", "2": "SENAD", "3": "THNAD"}[ser]
				case "NMG":
					v["PurpCd"] = map[string]string{"1": "MNGAD", "2": "SMNGA", "3": "TMNGA"}[ser]
				case "PMS":
					v["PMSSEBIRegnNb"] = strings.TrimSpace(l.V["user_text2"])
				}
				if s := l.V["share"]; s != "" {
					v["NmneePctgOfShr"] = boPctg(s)
				}
				if t := strings.TrimSpace(l.V["user_text1"]); t != "" {
					v["Rmk"] = t
				}
				out = append(out, v)
			case "21":
				v := map[string]string{"Purpse": "MPCM"}
				put(v, l, boCSVCM)
				out = append(out, v)
			case "22":
				v := map[string]string{"Purpse": "BOUCC"}
				put(v, l, boCSVUCC)
				out = append(out, v)
			}
		}
		for i, v := range out {
			for k, x := range common {
				if _, ok := v[k]; !ok {
					v[k] = x
				}
			}
			if product != "" {
				v["PrdNb"] = product
			}
			v["RcdSRNumber"] = fmt.Sprint(i + 1)
			rows = append(rows, xfile.Row{Values: v})
		}
	}
	return rows, dedupe(probs)
}

// cdslBOUploadCSV writes BO records as a harmonised BO upload: the file
// name (BO_UPLD_<DP ID>_<YYYYMMDDHHMM>_<serial>.csv) and content.
func cdslBOUploadCSV(in boCSVIn, serial int64) (string, []byte, []string) {
	lib, err := XFiles()
	if err != nil {
		return "", nil, []string{err.Error()}
	}
	s, ok := lib.Get("cdsl-bo-upload")
	if !ok {
		return "", nil, []string{"the harmonised BO upload layout is missing"}
	}
	rows, probs := cdslBOUploadRows(in)
	if len(probs) > 0 {
		return "", nil, probs
	}
	b, err := s.Write(rows)
	if err != nil {
		return "", nil, []string{strings.TrimPrefix(err.Error(), "xfile: ")}
	}
	dp := strings.TrimLeft(strings.TrimSpace(in.DPID), "0")
	if len(dp) < 6 {
		dp = strings.Repeat("0", 6-len(dp)) + dp
	}
	name, err := s.FileName(xfile.Vars{"dp_id": dp, "stamp": in.At.In(ist).Format("200601021504"), "seq": fmt.Sprint(serial % 100000)})
	if err != nil {
		return "", nil, []string{err.Error()}
	}
	return name, b, nil
}

// CDSLBOFormats lists the BO upload formats for the settings screen.
func CDSLBOFormats() []string { return []string{cdslFmtFixed, cdslFmtCSV} }

// cdslApplyBOResp reads CDSL's response to a request's upload when it is
// a harmonised BO_RESP file: an accepted account opening takes its BO ID;
// rejected says CDSL rejected the request's lines, with the errors.
func (o *Ops) cdslApplyBOResp(ctx context.Context, tenant, actor string, r *CDSLRequest, name string, data []byte) (rejected bool, why string) {
	lib, err := XFiles()
	if err != nil {
		return false, ""
	}
	files, err := xfile.Unpack(name, data)
	if err != nil {
		return false, ""
	}
	for _, f := range files {
		s, err := lib.Recognise(f.Name, f.Data)
		if err != nil || s.ID != "cdsl-bo-response" {
			continue
		}
		t, err := s.Read(f.Name, f.Data)
		if err != nil {
			continue
		}
		groups, _ := cdslRespGroups(dataRows(t), "ClntId")
		g := groups[r.UCC]
		if g == nil {
			g = groups[r.ID]
		}
		if g == nil {
			continue
		}
		if g.rejected {
			return true, "rejected: " + firstOf(strings.Join(g.errs, "; "), "no reason given")
		}
		if g.ok && (r.Kind == CDSLOpen || r.Kind == CDSLSetupUpload) && r.BOID == "" && reCDSLBO.MatchString(g.id) {
			_ = o.cdslApplyBOID(ctx, tenant, actor, r, g.id, r.Reply)
		}
	}
	return false, ""
}
