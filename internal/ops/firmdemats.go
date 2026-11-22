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
// 2023) is the trading member's own demat accounts: each row one of the
// firm's accounts × ISIN, with the account's type (POOL, CM COLL, OWN, EPI,
// CLNTBENEMTF, TMCOLL, TMCOLL-MTF, CLNT UNPAID, CLNT COLL, MRGN PLEDGE,
// MTF PLEDGE, CLNTBENE) and the member's own client code, PAN and name.
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

const (
	cFirmDematSnap = "firmdematsnap" // key: cdsl (the latest) and cdsl|YYYY-MM-DD

	wHolding = "holding-api" // work queue process of the holding statement
)

// HSAccountTypes are NSE's holding statement account types (NSE/INSP/55380).
var HSAccountTypes = []string{"POOL", "CM COLL", "OWN", "EPI", "CLNTBENEMTF", "TMCOLL", "TMCOLL-MTF", "CLNT UNPAID", "CLNT COLL", "MRGN PLEDGE", "MTF PLEDGE", "CLNTBENE"}

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
		v = strings.ToUpper(strings.Join(strings.Fields(v), " "))
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

// hsSecurityType is the security type of an ISIN. The previous system's
// file was read for its accounts and types only, so its values here are
// to confirm; EQ (what Ledgerline always sent) for every ISIN until then.
func hsSecurityType(isin string) string { return "EQ" }

// firmHoldingStatement works out the day's statement from the firm's
// tagged accounts in that day's CDSL statement of holding, with what stops
// it being sent.
func (o *Ops) firmHoldingStatement(ctx context.Context, tenant string, set Settings, date string) ([]hsItem, []string) {
	var probs []string
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
	snap, _, err := docs.Get[FirmDematSnap](ctx, o.Docs, tenant, cFirmDematSnap, "cdsl|"+date)
	if err != nil {
		probs = append(probs, fmt.Sprintf("CDSL's statement of holding for %s is not loaded (Exchange files): the firm's balances come from it", fmtDay(date)))
		return []hsItem{}, probs
	}
	seen := map[string]bool{}
	items := []hsItem{}
	want := map[string]money.Dec{}
	for _, a := range snap.Accounts {
		seen[a.BOID] = true
		t := set.FirmDemats[a.BOID]
		if t == "" && a.ISINs == 0 {
			continue // holds nothing: nothing to leave out (the self-inspection still asks for its tag)
		}
		if t == "" {
			probs = append(probs, fmt.Sprintf("demat account %s is in the statement of holding, belongs to no client and is not tagged with its holding statement type (%d ISINs held)", MaskAccount(a.BOID), a.ISINs))
			continue
		}
		for _, l := range a.Lines {
			tot, pl := decOr0(l.Total), decOr0(l.Pledged)
			if tot.Sign() <= 0 {
				continue
			}
			want[a.BOID+"|"+l.ISIN] = tot
			items = append(items, hsItem{RecID: fmt.Sprint(len(items) + 1), Dmat: a.BOID, AccountType: t, UCC: strings.ToUpper(strings.TrimSpace(set.FirmUCC)), ClientName: set.Firm, PAN: pan,
				ISIN: l.ISIN, SecurityType: hsSecurityType(l.ISIN), NameOfCommodity: "NA", UnitType: "NA", TotalPldgQty: pl.Fixed(3), FreeBalQty: tot.Sub(pl).Fixed(3), TotalQty: tot.Fixed(3)})
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
	for _, k := range sortedKeys(want) {
		if got[k].Cmp(want[k]) != 0 {
			bo, isin, _ := strings.Cut(k, "|")
			probs = append(probs, fmt.Sprintf("demat account %s, %s: the statement has %s, the statement of holding %s", MaskAccount(bo), isin, fmtQty(got[k]), fmtQty(want[k])))
		}
	}
	return items, probs
}

// holdingStatementFile is the daily holding statement for the exchanges'
// APIs and BSE's BEFS (nseholdingapi.go, bseholding.go): the firm's
// tagged demat accounts, never clients' accounts. It is refused, with a
// work item for the compliance officer, while the check finds a problem.
func (o *Ops) holdingStatementFile(ctx context.Context, tenant string, s *xfile.Spec, set Settings, date string, vars xfile.Vars) (string, []byte, error) {
	items, probs := o.firmHoldingStatement(ctx, tenant, set, date)
	if len(probs) > 0 {
		o.raiseWork(ctx, tenant, wHolding, "blocked-"+date, "Holding statement for "+fmtDay(date)+" not made",
			"The holding statement is not made or sent until this is put right: "+strings.Join(probs, "; ")+".", "", RoleCompliance, "xfiles", date)
		return "", nil, fmt.Errorf("%w: the holding statement for %s is not made: %s", ErrState, fmtDay(date), strings.Join(probs, "; "))
	}
	o.closeWork(ctx, tenant, SystemActor, wHolding, "blocked-"+date, "the holding statement could be made")
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
	Types    []string       `json:"types"`
	Accounts []FirmDematRow `json:"accounts"`
	FirmUCC  string         `json:"firm_ucc"`
	Snapshot string         `json:"snapshot,omitempty"` // the statement of holding they were seen in
	Date     string         `json:"date,omitempty"`
	Problems []string       `json:"problems"` // what stops the latest day's statement
	CanEdit  bool           `json:"can_edit"`
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
	v := FirmDematsView{Types: HSAccountTypes, Accounts: []FirmDematRow{}, FirmUCC: set.FirmUCC, Problems: []string{}, CanEdit: a.Admin}
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
		r.Type = t
	}
	for _, k := range sortedKeys(by) {
		v.Accounts = append(v.Accounts, *by[k])
	}
	if snap.Date != "" {
		_, v.Problems = o.firmHoldingStatement(ctx, tenant, set, snap.Date)
		if v.Problems == nil {
			v.Problems = []string{}
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
		bo, t := normDematID(h.Dmat), strings.ToUpper(strings.Join(strings.Fields(h.AccountType), " "))
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
	_, probs := e.o.firmHoldingStatement(e.ctx, e.tenant, e.set, snap.Date)
	for _, p := range probs {
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
	s.summary = fmt.Sprintf("The statement of holding of %s: %d of the firm's accounts in it (%d tagged), %d tags in Settings; %d problems stop the holding statement.", fmtDay(snap.Date), len(snap.Accounts), tagged, len(e.set.FirmDemats), s.found)
	return s, nil
}
