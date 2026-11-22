package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- the holding statement: the firm's own demat accounts by type ----
//
// The daily holding statement to the exchanges (NSE/INSP/55039 of 28
// December 2022; layout and account types in NSE/INSP/55380 of 25 January
// 2023, kept in NSE's Inspection master circular NSE/INSP/73976) is the
// trading member's own demat accounts: each row one of the firm's accounts
// × ISIN, with the account's type (POOL, CM COLL, OWN, EPI, CLNTBENE-MTF,
// TMCOLL, TMCOLL-MTF, CLNT UNPAID, CLNT COLL, MRGN PLEDGE, MTF PLEDGE,
// CLNTBENE) and the member's own client code, PAN and name.
// It is not the clients' holdings in their own beneficiary accounts. The
// pilot firm's previous system sent exactly that: its own and client
// collateral accounts, nothing from clients' accounts. (Ledgerline's
// first version sent every client's holdings labelled OWN: clients'
// securities reported as the broker's own, and the pool, unpaid and
// collateral accounts left out. This replaces it.)
//
// Inspections and orders: holding statements not matching the member's
// demat accounts (NSE/INSP/65693 of 19 December 2024; NSE/INSP/73503 of
// 27 March 2026, item 4 "DP account tagging not done"); SEBI's order
// against Axis Securities (21 February 2025: holdings left out of the
// holding statement); and a member penalised when its pool and client
// unpaid securities accounts dropped out of the statement after a change
// of back-office software.
//
// So:
//   - Settings.FirmDemats tags each of the firm's demat accounts with its
//     type (an Admin; or taken from the previous system's holding
//     statement file, which carries each account's type: nothing else of
//     that file is kept). Ledgerline never guesses a type.
//   - When CDSL's statement of holding is loaded on Exchange files, the
//     balances of every account in it that belongs to no client, and of
//     every tagged account, are kept by date (account × ISIN: total and
//     pledged). Clients' accounts are not kept here.
//   - The statement is written from that day's balances of the tagged
//     accounts. Before anything is written or sent it is checked: the
//     statement of holding for the day is loaded, every account in it that
//     is no client's is tagged, no tagged NSDL account (whose balances
//     Ledgerline does not load) is left out silently, the member's own
//     client code, PAN and name are set, and per account and ISIN the
//     statement equals the statement of holding. Anything wrong blocks the
//     file and raises a work item for the compliance officer; nothing is
//     dropped silently.
//   - The self-inspection test holding_account_types runs the same check.
//
// Margin pledges (NSE's FAQs on margin collection, 31 July 2020 Q28-29 and
// the clarification of 31 August 2020, now Exhibit 17 of the Inspection
// master circular): securities pledged by clients as margin are not
// reported in the holding statement; securities lying in the member's own
// accounts (POOL, EPI, CUSPA, OWN) and securities whose pledge was invoked
// are. An invoked pledge moves the securities into the member's client
// securities margin pledge account (MRGN PLEDGE) or client securities
// under margin funding account (MTF PLEDGE), so those accounts are
// reported for what they hold. The statement is written only from the
// balances held in the firm's own accounts: what clients pledged to the
// firm stays in the clients' accounts (their pledged balance) and the
// pledge register, and neither is ever read here. A tagged account that
// the client records also name is a client's own account; anything but
// OWN on it blocks the statement, as its pledged balance would be client
// margin collateral.
//
// Account type spelling: the API specification (NSE/INSP/55380 Annexure A,
// row 4.3) prints CLNTBENEMTF; NSE's earlier weekly upload format and the
// Inspection master circular print CLNTBENE-MTF, and Ledgerline uses the
// master circular's. Both spellings (and the weekly format's TM COLL and
// TM COLL-MTF) are read as the same type when tagging. NSE answers 902
// for a type it does not take: the work item then names the other
// spelling.
//
// Security type (row 4.8: EQ, MF, BOND, DEBT, PREF, IDR, WT, COMM) comes
// from CDSL's ISIN master (FinInstrmTp); commodities add the commodity's
// name and unit (rows 4.9 and 4.10).

const (
	cFirmDematSnap = "firmdematsnap" // key: cdsl (the latest) and cdsl|YYYY-MM-DD

	wHolding = "holding-api" // work queue process of the holding statement
)

// HSAccountTypes are NSE's holding statement account types (NSE/INSP/55380
// Annexure A row 4.3, as the Inspection master circular spells them).
var HSAccountTypes = []string{"POOL", "CM COLL", "OWN", "EPI", "CLNTBENE-MTF", "TMCOLL", "TMCOLL-MTF", "CLNT UNPAID", "CLNT COLL", "MRGN PLEDGE", "MTF PLEDGE", "CLNTBENE"}

// hsTypeAliases are other spellings of a type found in NSE's documents and
// in other systems' files.
var hsTypeAliases = map[string]string{"CLNTBENEMTF": "CLNTBENE-MTF", "CLNTBENE MTF": "CLNTBENE-MTF", "TM COLL": "TMCOLL", "TM COLL-MTF": "TMCOLL-MTF", "TMCOLL MTF": "TMCOLL-MTF", "CMCOLL": "CM COLL", "CLNTUNPAID": "CLNT UNPAID", "CLNTCOLL": "CLNT COLL"}

// HSAccountTypeNotes say what each type is for, on the tagging screen.
// The FAQ named is NSE's on margin collection (Exhibit 17 of the
// Inspection master circular).
var HSAccountTypeNotes = map[string]string{
	"POOL":         "Pool account: securities received in pay-out or kept for pay-in.",
	"CM COLL":      "The firm's collateral account for its clearing member or clearing corporation.",
	"OWN":          "The firm's own (proprietary) securities.",
	"EPI":          "Early pay-in account.",
	"CLNTBENE-MTF": "Client beneficiary account for margin trading (MTF).",
	"TMCOLL":       "Client securities taken as collateral into the firm's account (the old title-transfer accounts).",
	"TMCOLL-MTF":   "As TMCOLL, for margin trading (MTF).",
	"CLNT UNPAID":  "Client unpaid securities pledgee account (CUSPA).",
	"CLNT COLL":    "Client collateral account: what it holds is reported, for example securities from an invoked pledge.",
	"MRGN PLEDGE":  "Client securities margin pledge account: only securities invoked into it are reported, never the pledges in its favour (NSE FAQ).",
	"MTF PLEDGE":   "Client securities under margin funding account: only securities invoked into it are reported (NSE FAQ).",
	"CLNTBENE":     "Client beneficiary account held by the firm.",
}

// normHSType is a type in NSE's spelling ("" when empty).
func normHSType(v string) string {
	v = strings.ToUpper(strings.Join(strings.Fields(v), " "))
	if a, ok := hsTypeAliases[v]; ok {
		return a
	}
	return v
}

// FirmDematLine is one ISIN's balance in one of the firm's accounts.
type FirmDematLine struct {
	ISIN    string `json:"isin"`
	Total   string `json:"total"`
	Pledged string `json:"pledged"`
}

// FirmDematSeen is an account of CDSL's statement of holding that is the
// firm's (tagged) or belongs to no client.
type FirmDematSeen struct {
	BOID     string          `json:"boid"`
	Category string          `json:"category,omitempty"` // CDSL's beneficiary account category, as in the file
	Client   string          `json:"client,omitempty"`   // a tagged account the client records also name
	ISINs    int             `json:"isins"`              // ISINs with a balance
	Qty      string          `json:"qty"`                // total of the balances
	Lines    []FirmDematLine `json:"lines"`
}

// FirmDematSnap is a statement of holding's firm accounts.
type FirmDematSnap struct {
	Date     string          `json:"date"` // the statement's date
	File     string          `json:"file"`
	Accounts []FirmDematSeen `json:"accounts"`
	At       string          `json:"at"`
}

// checkFirmDemats normalises and checks the firm's account tags.
func checkFirmDemats(m map[string]string) (map[string]string, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for k, v := range m {
		id := normDematID(k)
		v = normHSType(v)
		if v == "" {
			continue // untagged
		}
		if !reCDSLBO.MatchString(id) && !reNSDLBO.MatchString(id) {
			return nil, invalid("%s is not a demat account (16 digits for CDSL, IN and 14 digits for NSDL)", k)
		}
		if !contains(HSAccountTypes, v) {
			return nil, invalid("%s: %q is not a holding statement account type (%s)", MaskAccount(id), v, strings.Join(HSAccountTypes, ", "))
		}
		out[id] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// sohDate is a statement of holding's date (its StmtDt), or today.
func sohDate(rows []xfile.Row, today string) string {
	for _, r := range rows {
		v := strings.TrimSpace(r.Get("StmtDt"))
		if len(v) >= 10 && validDate(v[:10]) {
			return v[:10]
		}
		if len(v) == 8 {
			if d := v[4:] + "-" + v[2:4] + "-" + v[:2]; validDate(d) {
				return d
			}
		}
	}
	return today
}

// noteFirmDemats keeps the balances of the firm's accounts in a CDSL
// statement of holding: the tagged ones and those of no client. It
// returns the accounts of no client that are not tagged.
func (o *Ops) noteFirmDemats(ctx context.Context, tenant, actor, name string, rows []xfile.Row) (string, []string) {
	set, _ := o.Settings(ctx, tenant)
	byBO := o.boClients(ctx, tenant)
	type agg struct {
		cat, client string
		lines       map[string]*[2]money.Dec
	}
	acc := map[string]*agg{}
	for _, r := range rows {
		bo := normDematID(r.Get("BnfclOwnrId"))
		if bo == "" {
			continue
		}
		ucc, isClient := byBO[bo]
		if isClient && set.FirmDemats[bo] == "" {
			continue // a client's account: not the firm's
		}
		a := acc[bo]
		if a == nil {
			a = &agg{cat: strings.TrimSpace(r.Get("BnfcryAcctCtgy")), client: ucc, lines: map[string]*[2]money.Dec{}}
			acc[bo] = a
		}
		isin := strings.ToUpper(strings.TrimSpace(r.Get("ISIN")))
		l := a.lines[isin]
		if l == nil {
			l = &[2]money.Dec{}
			a.lines[isin] = l
		}
		l[0], l[1] = l[0].Add(decOr0(r.Get("CurBal"))), l[1].Add(decOr0(r.Get("PldgdBal")))
	}
	date := sohDate(rows, o.now().In(ist).Format("2006-01-02"))
	snap := FirmDematSnap{Date: date, File: name, Accounts: []FirmDematSeen{}, At: o.now().Format(time.RFC3339)}
	var untagged []string
	for _, bo := range sortedKeys(acc) {
		a := acc[bo]
		s := FirmDematSeen{BOID: bo, Category: a.cat, Client: a.client, Lines: []FirmDematLine{}}
		var tot money.Dec
		for _, isin := range sortedKeys(a.lines) {
			l := a.lines[isin]
			if l[0].Sign() <= 0 {
				continue
			}
			s.Lines = append(s.Lines, FirmDematLine{ISIN: isin, Total: fmtQty(l[0]), Pledged: fmtQty(l[1])})
			tot = tot.Add(l[0])
		}
		s.ISINs, s.Qty = len(s.Lines), fmtQty(tot)
		snap.Accounts = append(snap.Accounts, s)
		if set.FirmDemats[bo] == "" {
			untagged = append(untagged, MaskAccount(bo))
		}
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cFirmDematSnap, "cdsl|"+date, snap, actor)
	if last, _, err := docs.Get[FirmDematSnap](ctx, o.Docs, tenant, cFirmDematSnap, "cdsl"); err != nil || last.Date <= date {
		_ = docs.Upsert(ctx, o.Docs, tenant, cFirmDematSnap, "cdsl", snap, actor)
	}
	return date, untagged
}

// latestFirmSnapDate is the date of the latest statement of holding loaded
// ("" when none).
func (o *Ops) latestFirmSnapDate(ctx context.Context, tenant string) string {
	s, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFirmDematSnap, "cdsl", FirmDematSnap{})
	return s.Date
}

// ---- writing and checking the statement ----

// hsItem is one row of the holding statement (holding-statement-api).
type hsItem struct {
	RecID           string `json:"recId"`
	Dmat            string `json:"dmat"`
	AccountType     string `json:"accountType"`
	UCC             string `json:"ucc"`
	ClientName      string `json:"clientName"`
	PAN             string `json:"pan"`
	ISIN            string `json:"isin"`
	SecurityType    string `json:"securityType"`
	NameOfCommodity string `json:"nameOfCommodity"`
	UnitType        string `json:"unitType"`
	TotalPldgQty    string `json:"totalPldgQty"`
	FreeBalQty      string `json:"freeBalQty"`
	TotalQty        string `json:"totalQty"`
}

// cdslHSType maps CDSL's security type (ISIN master FinInstrmTp) to the
// holding statement's (NSE/INSP/55380 Annexure A row 4.8). Units of REITs
// and InvITs are listed and settled as equity on the exchanges and have no
// type of their own in NSE's list: they go as EQ (to confirm with NSE).
// AIF, SIF and collective investment scheme units go as MF (units of a
// fund).
var cdslHSType = map[string]string{
	"EQTY": "EQ", "PROSH": "EQ", "REEQT": "EQ", "INVIT": "EQ", "REIT": "EQ", "REINT": "EQ",
	"PRSH": "PREF", "PPRSH": "PREF", "CPRSH": "PREF", "CCPRS": "PREF", "REPRF": "PREF",
	"UNITS": "MF", "MUNIT": "MF", "OEMFT": "MF", "CEMF": "MF", "CEAIF": "MF", "CEAF2": "MF", "CEAF3": "MF", "OEAIF": "MF",
	"CIS": "MF", "OPSIF": "MF", "CLSIF": "MF", "IFSIF": "MF", "SIF": "MF",
	"WRRNT": "WT",
	"IDR":   "IDR",
	"DEBTR": "DEBT", "PTCSI": "DEBT", "PYTC": "DEBT", "CP": "DEBT", "COD": "DEBT", "PRMNT": "DEBT", "SECRT": "DEBT", "REDBT": "DEBT", "ZCZPI": "DEBT", "PSSCH": "DEBT",
	"ZCB": "BOND", "TB": "BOND", "BOCSG": "BOND", "GSEC": "BOND", "CMNGB": "BOND", "SGB": "BOND", "RBIRB": "BOND", "REGRB": "BOND", "BOND": "BOND",
	"DDBON": "BOND", "FLTRB": "BOND", "MNCPB": "BOND",
	"COMDT": "COMM", "COMBN": "COMM", "EGR": "COMM",
}

// hsUnitWords find a commodity's unit (row 4.10) in its name.
var hsUnitWords = []struct{ word, unit string }{
	{"GRAM", "GMS"}, {"GMS", "GMS"}, {"KG", "KG"}, {"QUINTAL", "QUINTAL"}, {"QTL", "QUINTAL"}, {"TONNE", "MT"}, {" MT", "MT"},
	{"BARREL", "BARRELS"}, {"MMBTU", "MMBTU"}, {"BALE", "BALES"}, {"DRUM", "DRUMS"},
}

// hsSecurity is an ISIN's security type in the holding statement, with a
// commodity's name and unit ("NA" for securities), and a note when it is
// not from CDSL's ISIN master.
func (o *Ops) hsSecurity(ctx context.Context, tenant, isin string) (typ, commodity, unit, note string) {
	ref, _, _ := docs.Get[SecRef](ctx, o.Docs, tenant, cSecRef, isin)
	typ = cdslHSType[strings.ToUpper(strings.TrimSpace(ref.Type))]
	if typ == "" {
		switch {
		case strings.HasPrefix(isin, "INF"): // mutual fund units
			typ = "MF"
		case len(isin) > 2 && isin[2] >= '0' && isin[2] <= '9', govtSeries[strings.ToUpper(ref.Series)], strings.EqualFold(ref.Series, "GB"):
			typ = "BOND" // government securities and sovereign gold bonds
		default:
			typ = "EQ"
		}
		why := "not in CDSL's ISIN master"
		if ref.Type != "" {
			why = "CDSL type " + ref.Type + " has no holding statement type"
		}
		note = fmt.Sprintf("%s is reported as %s (%s): load CDSL's ISIN master on Exchange files", isin, typ, why)
	}
	if typ != "COMM" {
		return typ, "NA", "NA", note
	}
	commodity = strings.TrimSpace(firstOf(firstOf(ref.Name, ref.Symbol), isin))
	if len(commodity) > 100 {
		commodity = commodity[:100]
	}
	name := " " + strings.ToUpper(commodity)
	if strings.ToUpper(ref.Type) == "EGR" { // electronic gold receipts are in grams
		unit = "GMS"
	}
	for _, w := range hsUnitWords {
		if unit == "" && strings.Contains(name, w.word) {
			unit = w.unit
		}
	}
	if unit == "" {
		unit = "OTHERS"
		note = strings.TrimSpace(note + " " + isin + " (" + commodity + ") is a commodity whose unit is not in its name: reported with unit OTHERS")
	}
	return typ, commodity, unit, note
}

// holdingSnap is the firm-account balances a data day's statement is made
// from: that day's CDSL statement of holding. A day with no settlement (a
// Saturday, a holiday, a Sunday special session) moves nothing in the
// demat accounts, so without a file of its own it takes the latest one
// since the trading day before it. want is the file that is needed when
// none is found.
func (o *Ops) holdingSnap(ctx context.Context, tenant string, set Settings, date string) (snap FirmDematSnap, want string, ok bool) {
	if s, _, err := docs.Get[FirmDematSnap](ctx, o.Docs, tenant, cFirmDematSnap, "cdsl|"+date); err == nil {
		return s, date, true
	}
	if set.IsBusinessDay(date) {
		return FirmDematSnap{}, date, false
	}
	prev := set.PrevBusinessDay(date)
	for d := addDays(date, -1); d >= prev && d != ""; d = addDays(d, -1) {
		if s, _, err := docs.Get[FirmDematSnap](ctx, o.Docs, tenant, cFirmDematSnap, "cdsl|"+d); err == nil {
			return s, d, true
		}
	}
	return FirmDematSnap{}, prev, false
}

// hsClientAcctOK are the types a tagged account that the client records
// also name may have: the firm's own account opened as a client (for its
// proprietary trades). Any other type on a client's account (collateral,
// pledge, MTF…) would report the client's own holding and pledged balance.
var hsClientAcctOK = map[string]bool{"OWN": true}

// firmHoldingStatement works out the day's statement from the firm's
// tagged accounts in that day's CDSL statement of holding, with what stops
// it being sent and notes that do not.
func (o *Ops) firmHoldingStatement(ctx context.Context, tenant string, set Settings, date string) ([]hsItem, []string, []string) {
	var probs, notes []string
	pan := firmPAN(set)
	if pan == "" {
		probs = append(probs, "the firm's PAN is not set (Settings)")
	}
	if strings.TrimSpace(set.FirmUCC) == "" {
		probs = append(probs, "the firm's own client code for the holding statement is not set (Settings → Firm demat accounts)")
	}
	if strings.TrimSpace(set.Firm) == "" {
		probs = append(probs, "the firm's name is not set (Settings)")
	}
	snap, want, ok := o.holdingSnap(ctx, tenant, set, date)
	if !ok {
		probs = append(probs, fmt.Sprintf("CDSL's statement of holding for %s is not loaded (Exchange files): the firm's balances come from it", fmtDay(want)))
		return []hsItem{}, probs, notes
	}
	seen := map[string]bool{}
	items := []hsItem{}
	want2 := map[string]money.Dec{}
	sec := map[string][3]string{}
	for _, a := range snap.Accounts {
		seen[a.BOID] = true
		t := normHSType(set.FirmDemats[a.BOID])
		if t == "" && a.ISINs == 0 {
			continue // holds nothing: nothing to leave out (the self-inspection still asks for its tag)
		}
		if t == "" {
			probs = append(probs, fmt.Sprintf("demat account %s is in the statement of holding, belongs to no client and is not tagged with its holding statement type (%d ISINs held)", MaskAccount(a.BOID), a.ISINs))
			continue
		}
		if a.Client != "" && !hsClientAcctOK[t] {
			// A client's own account: its pledged balance is the client's
			// margin pledge, which NSE's FAQ says is not reported.
			probs = append(probs, fmt.Sprintf("demat account %s is tagged %s but is client %s's own account: a client's account is not reported, and its pledged balance is margin collateral, which NSE does not take in the holding statement (untag it, or correct the client's demat accounts)", MaskAccount(a.BOID), t, a.Client))
			continue
		}
		for _, l := range a.Lines {
			tot, pl := decOr0(l.Total), decOr0(l.Pledged)
			if tot.Sign() <= 0 {
				continue
			}
			want2[a.BOID+"|"+l.ISIN] = tot
			st, ok := sec[l.ISIN]
			if !ok {
				typ, comm, unit, note := o.hsSecurity(ctx, tenant, l.ISIN)
				st = [3]string{typ, comm, unit}
				sec[l.ISIN] = st
				if note != "" {
					notes = append(notes, note)
				}
			}
			items = append(items, hsItem{RecID: fmt.Sprint(len(items) + 1), Dmat: a.BOID, AccountType: t, UCC: strings.ToUpper(strings.TrimSpace(set.FirmUCC)), ClientName: set.Firm, PAN: pan,
				ISIN: l.ISIN, SecurityType: st[0], NameOfCommodity: st[1], UnitType: st[2], TotalPldgQty: pl.Fixed(3), FreeBalQty: tot.Sub(pl).Fixed(3), TotalQty: tot.Fixed(3)})
		}
	}
	for _, bo := range sortedKeys(set.FirmDemats) {
		if !seen[bo] && reNSDLBO.MatchString(bo) {
			probs = append(probs, fmt.Sprintf("demat account %s (%s) is with NSDL: Ledgerline does not load its balances, so it would be left out", MaskAccount(bo), set.FirmDemats[bo]))
		}
	}
	// The statement equals the statement of holding, account by account
	// and ISIN by ISIN (a guard against a later change here).
	got := map[string]money.Dec{}
	for _, it := range items {
		k := it.Dmat + "|" + it.ISIN
		got[k] = got[k].Add(decOr0(it.TotalQty))
	}
	for _, k := range sortedKeys(want2) {
		if got[k].Cmp(want2[k]) != 0 {
			bo, isin, _ := strings.Cut(k, "|")
			probs = append(probs, fmt.Sprintf("demat account %s, %s: the statement has %s, the statement of holding %s", MaskAccount(bo), isin, fmtQty(got[k]), fmtQty(want2[k])))
		}
	}
	return items, probs, notes
}

// holdingStatementFile is the daily holding statement for the exchanges'
// APIs and BSE's BEFS (nseholdingapi.go, bseholding.go): the firm's
// tagged demat accounts, never clients' accounts. It is refused, with a
// work item for the compliance officer, while the check finds a problem.
func (o *Ops) holdingStatementFile(ctx context.Context, tenant string, s *xfile.Spec, set Settings, date string, vars xfile.Vars) (string, []byte, error) {
	items, probs, notes := o.firmHoldingStatement(ctx, tenant, set, date)
	if len(probs) > 0 {
		o.raiseWork(ctx, tenant, wHolding, "blocked-"+date, "Holding statement for "+fmtDay(date)+" not made",
			"The holding statement is not made or sent until this is put right: "+strings.Join(probs, "; ")+".", "", RoleCompliance, "xfiles", date)
		return "", nil, fmt.Errorf("%w: the holding statement for %s is not made: %s", ErrState, fmtDay(date), strings.Join(probs, "; "))
	}
	o.closeWork(ctx, tenant, SystemActor, wHolding, "blocked-"+date, "the holding statement could be made")
	if len(notes) > 0 {
		o.raiseWork(ctx, tenant, wHolding, "sectype-"+date, "Holding statement for "+fmtDay(date)+": security types to check",
			"The statement is made, but check these before it goes: "+strings.Join(notes, "; ")+".", "", RoleCompliance, "xfiles", date)
	} else {
		o.closeWork(ctx, tenant, SystemActor, wHolding, "sectype-"+date, "every security type is from CDSL's ISIN master")
	}
	d, _ := time.Parse("2006-01-02", date)
	pan := firmPAN(set)
	msg := struct {
		MemberPan      string   `json:"memberPan"`
		FilePeriodDate string   `json:"filePeriodDate"`
		Date           string   `json:"date"`
		HoldingData    []hsItem `json:"holdingData"`
	}{pan, d.Format("02-01-2006"), d.Format("02-01-2006"), items}
	b, err := json.Marshal(msg)
	if err != nil {
		return "", nil, err
	}
	vars["member_pan"] = pan
	name, err := s.FileName(vars)
	if err != nil {
		return "", nil, invalid("%v", err)
	}
	return name, b, nil
}

// ---- the screen ----

// FirmDematRow is one of the firm's demat accounts on the screen.
type FirmDematRow struct {
	BOID   string `json:"boid"`
	Masked string `json:"masked"`
	Type   string `json:"type"` // the tag, empty when untagged
	Seen   bool   `json:"seen"` // in the last CDSL statement of holding
	Client string `json:"client,omitempty"`
	ISINs  int    `json:"isins"`
	Qty    string `json:"qty"`
}

// FirmDematsView is the firm's demat accounts with their tags.
type FirmDematsView struct {
	Types     []string          `json:"types"`
	Accounts  []FirmDematRow    `json:"accounts"`
	FirmUCC   string            `json:"firm_ucc"`
	Snapshot  string            `json:"snapshot,omitempty"` // the statement of holding they were seen in
	Date      string            `json:"date,omitempty"`
	Problems  []string          `json:"problems"` // what stops the latest day's statement
	Notes     []string          `json:"notes"`    // what to check in it (security types)
	TypeNotes map[string]string `json:"type_notes"`
	CanEdit   bool              `json:"can_edit"`
}

// FirmDemats lists the firm's tagged demat accounts and those seen in the
// last CDSL statement of holding that belong to no client.
func (o *Ops) FirmDemats(ctx context.Context, tenant, actor string) (FirmDematsView, error) {
	if err := o.require(ctx, tenant, actor, "Seeing the firm's demat accounts", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return FirmDematsView{}, err
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return FirmDematsView{}, err
	}
	a, _ := o.Access(ctx, tenant, actor)
	v := FirmDematsView{Types: HSAccountTypes, TypeNotes: HSAccountTypeNotes, Accounts: []FirmDematRow{}, FirmUCC: set.FirmUCC, Problems: []string{}, Notes: []string{}, CanEdit: a.Admin}
	snap, _, _ := docs.GetOr(ctx, o.Docs, tenant, cFirmDematSnap, "cdsl", FirmDematSnap{})
	v.Snapshot, v.Date = snap.File, snap.Date
	by := map[string]*FirmDematRow{}
	for _, s := range snap.Accounts {
		by[s.BOID] = &FirmDematRow{BOID: s.BOID, Masked: MaskAccount(s.BOID), Seen: true, Client: s.Client, ISINs: s.ISINs, Qty: s.Qty}
	}
	for bo, t := range set.FirmDemats {
		r := by[bo]
		if r == nil {
			r = &FirmDematRow{BOID: bo, Masked: MaskAccount(bo), Qty: "0"}
			by[bo] = r
		}
		r.Type = normHSType(t)
	}
	for _, k := range sortedKeys(by) {
		v.Accounts = append(v.Accounts, *by[k])
	}
	if snap.Date != "" {
		_, v.Problems, v.Notes = o.firmHoldingStatement(ctx, tenant, set, snap.Date)
		if v.Problems == nil {
			v.Problems = []string{}
		}
		if v.Notes == nil {
			v.Notes = []string{}
		}
	}
	return v, nil
}

// SaveFirmDemats sets the firm's account tags (all of them: an account
// left out or given no type is untagged) and its own client code.
func (o *Ops) SaveFirmDemats(ctx context.Context, tenant, actor string, tags map[string]string, firmUCC string) (FirmDematsView, error) {
	if err := o.require(ctx, tenant, actor, "Tagging the firm's demat accounts", RoleAdmin); err != nil {
		return FirmDematsView{}, err
	}
	fd, err := checkFirmDemats(tags)
	if err != nil {
		return FirmDematsView{}, err
	}
	firmUCC = strings.ToUpper(strings.TrimSpace(firmUCC))
	if firmUCC != "" && !reNSEUCICode.MatchString(firmUCC) {
		return FirmDematsView{}, invalid("the firm's own client code is letters and digits, up to 10")
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return FirmDematsView{}, err
	}
	old := set.FirmDemats
	set.FirmDemats, set.FirmUCC = fd, firmUCC
	if err := o.saveSettings(ctx, tenant, actor, set); err != nil {
		return FirmDematsView{}, err
	}
	masked := map[string]string{}
	for k, v := range fd {
		masked[MaskAccount(k)] = v
	}
	o.log(ctx, tenant, actor, "settings.firm_demats", "settings", map[string]any{"tags": masked, "before": len(old), "after": len(fd), "firm_ucc": firmUCC})
	return o.FirmDemats(ctx, tenant, actor)
}

// FirmDematsFromStatement takes the account types from the previous
// system's holding statement file (the JSON it sent the exchange): each
// account and its type only; nothing else of the file is read or kept.
// The tags are added to the firm's (an account already tagged keeps its
// tag unless the file says otherwise).
func (o *Ops) FirmDematsFromStatement(ctx context.Context, tenant, actor, name string, data []byte) (FirmDematsView, map[string]string, error) {
	if err := o.require(ctx, tenant, actor, "Tagging the firm's demat accounts", RoleAdmin); err != nil {
		return FirmDematsView{}, nil, err
	}
	var msg struct {
		HoldingData []struct {
			Dmat        string `json:"dmat"`
			AccountType string `json:"accountType"`
		} `json:"holdingData"`
	}
	if err := json.Unmarshal(data, &msg); err != nil || len(msg.HoldingData) == 0 {
		return FirmDematsView{}, nil, invalid("%s is not a holding statement file (JSON with holdingData)", name)
	}
	found := map[string]string{}
	for _, h := range msg.HoldingData {
		bo, t := normDematID(h.Dmat), normHSType(h.AccountType)
		if bo == "" || t == "" {
			continue
		}
		if p, ok := found[bo]; ok && p != t {
			return FirmDematsView{}, nil, invalid("account %s has two types in the file (%s and %s)", MaskAccount(bo), p, t)
		}
		found[bo] = t
	}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return FirmDematsView{}, nil, err
	}
	tags := map[string]string{}
	for k, v := range set.FirmDemats {
		tags[k] = v
	}
	for k, v := range found {
		tags[k] = v
	}
	v, err := o.SaveFirmDemats(ctx, tenant, actor, tags, set.FirmUCC)
	masked := map[string]string{}
	for k, t := range found {
		masked[MaskAccount(k)] = t
	}
	return v, masked, err
}

// ---- the self-inspection test ----

func siHoldingTypes(e *siEnv) (siOut, error) {
	exs := memberExchanges(e.set)
	bse, _ := bseHoldingApplies(e.set, e.date)
	if !contains(exs, "NSE") && !bse {
		return siSkip("Not applicable: the firm has no NSE or BSE member code in Settings.")
	}
	snap, _, err := docs.GetOr(e.ctx, e.o.Docs, e.tenant, cFirmDematSnap, "cdsl", FirmDematSnap{})
	if err != nil {
		return siOut{}, err
	}
	if snap.Date == "" {
		if len(e.set.FirmDemats) == 0 {
			return siSkip("Waiting for data: load CDSL's statement of holding on Exchange files (the firm's own accounts are found in it), or tag the firm's demat accounts in Settings.")
		}
		return siSkip("Waiting for data: load CDSL's statement of holding on Exchange files.")
	}
	var s siOut
	s.input("CDSL statement of holding", snap.Date)
	s.checked = len(snap.Accounts) + len(e.set.FirmDemats)
	_, probs, notes := e.o.firmHoldingStatement(e.ctx, e.tenant, e.set, snap.Date)
	for _, p := range append(probs, notes...) {
		s.bad(strings.ToUpper(p[:1]) + p[1:])
	}
	for _, a := range snap.Accounts {
		if a.ISINs == 0 && e.set.FirmDemats[a.BOID] == "" {
			s.bad(fmt.Sprintf("Demat account %s is in the statement of holding, belongs to no client and is not tagged (it holds nothing now: tag it before it does)", MaskAccount(a.BOID)))
		}
	}
	tagged := 0
	for _, a := range snap.Accounts {
		if e.set.FirmDemats[a.BOID] != "" {
			tagged++
		}
	}
	s.summary = fmt.Sprintf("The statement of holding of %s: %d of the firm's accounts in it (%d tagged), %d tags in Settings; %d problems stop the holding statement, %d security types to check.", fmtDay(snap.Date), len(snap.Accounts), tagged, len(e.set.FirmDemats), len(probs), len(notes))
	return s, nil
}
