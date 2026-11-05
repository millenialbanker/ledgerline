package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// ---- CDSL's online freeze master report (DPE6) ----
//
// CDSL reports every freeze on the DP's accounts in the online freeze
// master report (DPE6; layout: communiqué CDSL/SURV/DP/POLCY/2026/621,
// Annexure A, which follows 2026/532 and adds the regulatory authority,
// its details, the type of order and the reason for delay). The file is
// CDSL's unformatted report: one ~ separated line per freeze, no heading.
//
// Each line is kept by freeze ID (with the BO ID) and the account it is
// for. A freeze is active from its activation date until it is unfrozen,
// cancelled or released (a date in the unfreeze / cancel / release field,
// freeze type U, or status U, C or R), or until its expiry date passes;
// a failed freeze (status F) never takes effect. While active it blocks:
//
//   - frozen for debits (or both), the whole account: DIS entry and
//     execution, eDIS, pledges, early pay-in and securities pay-in, and
//     automatic approval of funds payouts (autopilot.go);
//   - frozen for debits, one ISIN in full: DIS, eDIS, pledges and pay-in
//     of that ISIN; for a quantity: that quantity is taken out of the free
//     balance;
//   - frozen for credits (or both): IPO allotment bids and securities
//     pay-outs to the account (or of that ISIN).
//
// The account-level part is kept on the BO account (CDSLFreeze, apart
// from the DP's own freeze requests) and refreshed on every load and by
// the "cdsl_freezes" tick (expiry and future activation). A new freeze and
// a lifted one each go to the work queue for the DP desk.
//
// The communiqué gives no code list for FZ_STATUS; the other codes are
// CDSL's old values in the UDiFF catalogue's Standard Value List.

const cCDSLFreeze = "cdslfreeze" // BOID|freeze ID

// CDSLFreeze is one freeze CDSL reports.
type CDSLFreeze struct {
	ID          string    `json:"id"`
	BOID        string    `json:"bo_id"`
	UCC         string    `json:"ucc,omitempty"`
	Name        string    `json:"name,omitempty"`
	ISIN        string    `json:"isin,omitempty"` // "" for the whole account
	ISINName    string    `json:"isin_name,omitempty"`
	QtyType     string    `json:"qty_type"` // Full, Partial
	SetupQty    money.Dec `json:"setup_qty"`
	ActualQty   money.Dec `json:"actual_qty"`
	Status      string    `json:"status,omitempty"` // CDSL's FZ_STATUS as given
	For         string    `json:"for"`              // Debit, Credit, Both
	SubOption   string    `json:"sub_option,omitempty"`
	InitiatedBy string    `json:"initiated_by,omitempty"`
	InitAt      string    `json:"initiated_at,omitempty"`
	Activation  string    `json:"activation,omitempty"` // Current, Future
	ActiveFrom  string    `json:"active_from,omitempty"`
	Type        string    `json:"type,omitempty"` // Setup, Unfreeze, Permanent, Temporary
	Expiry      string    `json:"expiry,omitempty"`
	ReasonCode  string    `json:"reason_code,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	IntRef      string    `json:"internal_ref,omitempty"`
	LiftedBy    string    `json:"lifted_by_dp,omitempty"`
	LiftedOn    string    `json:"lifted_on,omitempty"`
	LiftRemark  string    `json:"lift_remark,omitempty"`
	Remark      string    `json:"remark,omitempty"`
	Operator    string    `json:"operator,omitempty"`
	ParentISIN  string    `json:"parent_isin,omitempty"`
	Authority   string    `json:"authority,omitempty"`
	AuthDetails string    `json:"authority_details,omitempty"`
	OrderType   string    `json:"order_type,omitempty"`
	DelayReason string    `json:"delay_reason,omitempty"`
	// State is Active, Pending (future activation), Lifted, Failed or
	// Expired, as of the last load.
	State     string `json:"state"`
	Lift      string `json:"lift,omitempty"` // Unfrozen, Cancelled, Released
	File      string `json:"file"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
}

// Full says whether the freeze covers the whole account or the whole ISIN.
func (f CDSLFreeze) Full() bool { return f.ISIN == "" || f.QtyType != "Partial" }

// Qty is the quantity a partial freeze holds.
func (f CDSLFreeze) Qty() money.Dec {
	if f.ActualQty.Sign() > 0 {
		return f.ActualQty
	}
	return f.SetupQty
}

// Why is the freeze in words, for messages.
func (f CDSLFreeze) Why() string {
	s := "CDSL freeze " + f.ID
	if r := firstOf(f.Reason, f.ReasonCode); r != "" {
		s += ": " + r
	}
	if f.Authority != "" {
		s += " (" + f.Authority + ")"
	}
	return s
}

// blocking says whether the freeze holds on a date.
func (f CDSLFreeze) blocking(today string) bool {
	if f.State != "Active" && f.State != "Pending" {
		return false
	}
	if f.ActiveFrom != "" && f.ActiveFrom[:min(10, len(f.ActiveFrom))] > today {
		return false
	}
	if f.Expiry != "" && f.Expiry[:min(10, len(f.Expiry))] < today {
		return false
	}
	return true
}

func (f CDSLFreeze) debit() bool  { return f.For == "Debit" || f.For == "Both" }
func (f CDSLFreeze) credit() bool { return f.For == "Credit" || f.For == "Both" }

// cdslFreezeCodes are CDSL's old (numeric) values of the freeze codes as
// the UDiFF catalogue's Standard Value List maps them.
var (
	cdslFrozenFor   = map[string]string{"1": "Debit", "2": "Credit", "3": "Both", "FD": "Debit", "FC": "Credit", "FB": "Both", "D": "Debit", "C": "Credit", "B": "Both"}
	cdslFrzBy       = map[string]string{"1": "BO", "2": "CDSL", "3": "DP", "4": "Issuer", "BO": "BO", "CD": "CDSL", "DP": "DP", "IS": "Issuer"}
	cdslFrzSub      = map[string]string{"1": "Lien", "2": "Statutory bodies", "LIEN": "Lien", "STAT": "Statutory bodies"}
	cdslFrzActive   = map[string]string{"1": "Current", "2": "Future", "CRNT": "Current", "FUTR": "Future"}
	cdslFrzType     = map[string]string{"S": "Setup", "U": "Unfreeze", "P": "Permanent", "T": "Temporary", "SET": "Setup", "UNF": "Unfreeze", "PMF": "Permanent", "TMF": "Temporary"}
	cdslFrzReasonCd = map[string]string{"1": "INV", "2": "ITO", "3": "SSO", "4": "DPD", "5": "SJC", "6": "APN", "7": "FHD", "8": "SHD", "9": "THD", "10": "SRO", "11": "CBI", "12": "FIU", "13": "IPV", "14": "ASS",
		"16": "IBB", "17": "RBB", "18": "CAF", "19": "AFN", "20": "KYD", "21": "KYC", "22": "KYV", "23": "EGR", "24": "SCN", "25": "TWC", "26": "PAN", "27": "AKN", "28": "AHD", "29": "BAD", "30": "LEI",
		"94": "NDF", "95": "RGS", "96": "RPA", "97": "AMT", "98": "PNR"}
)

// cdslFreezeReason is a reason code's meaning (from the catalogue's
// standard values for FrzRsnCd, carried in the transaction export's spec).
func cdslFreezeReason(code string) string {
	code = strings.TrimLeft(strings.TrimSpace(code), "0")
	std := firstOf(cdslFrzReasonCd[code], strings.ToUpper(code))
	if m := cdslCode("cdsl-transactions", "FrzRsnCd", std); m != "" {
		return m
	}
	return ""
}

// cdslCode is a standard value's meaning from a spec's code list.
func cdslCode(spec, col, v string) string {
	lib, err := XFiles()
	if err != nil {
		return ""
	}
	s, ok := lib.Get(spec)
	if !ok {
		return ""
	}
	for _, c := range s.Columns {
		if strings.EqualFold(c.Name, col) {
			return c.Codes[strings.TrimSpace(v)]
		}
	}
	return ""
}

// cdslDate reads CDSL's report dates (DDMMYYYY:HH24MISS, DDMMYYYY, or
// ISO) as YYYY-MM-DD, or YYYY-MM-DDTHH:MM:SS when there is a time.
func cdslDate(s string) string {
	s = strings.TrimSpace(s)
	for _, l := range []string{"02012006:150405", "02012006 150405", "02-01-2006 15:04:05", "2006-01-02T15:04:05", "02/01/2006 15:04:05"} {
		if t, err := time.Parse(l, s); err == nil {
			if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
				return t.Format("2006-01-02")
			}
			return t.Format("2006-01-02T15:04:05")
		}
	}
	for _, l := range []string{"02012006", "02-01-2006", "2006-01-02", "02/01/2006", "02-Jan-2006"} {
		if t, err := time.Parse(l, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

// freezeFromRow reads one DPE6 line.
func freezeFromRow(r xfile.Row, today string) (CDSLFreeze, bool) {
	g := func(k string) string { return strings.TrimSpace(r.Get(k)) }
	f := CDSLFreeze{ID: strings.TrimLeft(g("FZ_ID"), "0"), BOID: normDematID(g("FZ_BO_ID"))}
	if f.ID == "" || f.BOID == "" {
		return f, false
	}
	f.Name = strings.Join(strings.Fields(g("CUST_NAME")+" "+g("BO_MIDDLE_NAME")+" "+g("CUST_SRCH_NAME")), " ")
	f.ISIN, f.ISINName = strings.ToUpper(g("FZ_ISIN")), g("ISIN_NAME")
	f.QtyType = map[string]string{"P": "Partial", "PAR": "Partial"}[strings.ToUpper(g("FZ_QTY_TYPE"))]
	if f.QtyType == "" {
		f.QtyType = "Full"
	}
	f.SetupQty, f.ActualQty = decOr0(g("FZ_SETUP_QTY")), decOr0(g("FZ_ACTUAL_QTY"))
	f.Status = strings.ToUpper(g("FZ_STATUS"))
	f.For = firstOf(cdslFrozenFor[strings.ToUpper(g("FROZEN_FOR"))], "Both") // unknown: the safer reading
	f.SubOption = firstOf(cdslFrzSub[strings.ToUpper(g("FZ_SUB_OPTION"))], g("FZ_SUB_OPTION"))
	f.InitiatedBy = firstOf(cdslFrzBy[strings.ToUpper(g("FZ_INITIATED_BY"))], g("FZ_INITIATED_BY"))
	f.InitAt = cdslDate(g("FZ_INIT_DATE"))
	f.Activation = firstOf(cdslFrzActive[strings.ToUpper(g("FZ_ACTIVATION_TYPE"))], g("FZ_ACTIVATION_TYPE"))
	f.ActiveFrom = cdslDate(g("FZ_ACTIVATION_DATE"))
	f.Type = firstOf(cdslFrzType[strings.ToUpper(g("FZ_TYPE"))], g("FZ_TYPE"))
	f.Expiry = cdslDate(g("FZ_EXP_DATE"))
	f.ReasonCode = g("FZ_REASON_CD")
	f.Reason = firstOf(g("FZ_REASON_DESC"), cdslFreezeReason(f.ReasonCode))
	f.IntRef, f.LiftedBy, f.LiftedOn, f.LiftRemark = g("FZ_INT_REF_NBR"), g("FZ_UC_DP"), cdslDate(g("FZ_UCR_DATE")), g("FZ_UC_REMARK")
	f.Remark, f.Operator, f.ParentISIN = g("FZ_REMARK"), g("FZ_OPER_ID"), g("FZ_PAR_ID")
	f.Authority, f.AuthDetails, f.OrderType, f.DelayReason = g("FZ_AUTHORITY_NAME"), g("FZ_AUTHORITY_DETAILS"), g("FZ_TYPE_OF_ORDER"), g("FZ_RSN_FOR_DELAY")
	switch {
	case f.Status == "F":
		f.State = "Failed"
	case f.Status == "C":
		f.State, f.Lift = "Lifted", "Cancelled"
	case f.Status == "R":
		f.State, f.Lift = "Lifted", "Released"
	case f.Status == "U" || f.Type == "Unfreeze":
		f.State, f.Lift = "Lifted", "Unfrozen"
	case f.LiftedOn != "" && f.LiftedOn[:10] <= today:
		f.State, f.Lift = "Lifted", firstOf(map[bool]string{true: "Unfrozen"}[strings.Contains(strings.ToUpper(f.LiftRemark), "UNFREEZ")], "Released")
	case f.Expiry != "" && f.Expiry[:10] < today:
		f.State = "Expired"
	case f.ActiveFrom != "" && f.ActiveFrom[:10] > today || f.Activation == "Future" && f.ActiveFrom == "":
		f.State = "Pending"
	default:
		f.State = "Active"
	}
	return f, true
}

// loadCDSLFreezes reads a DPE6 file: freezes are recorded, the accounts'
// freezes refreshed, and new and lifted freezes go to the work queue.
func (o *Ops) loadCDSLFreezes(ctx context.Context, tenant, actor, file string, rows []xfile.Row) (added, lifted, skipped int, err error) {
	by := o.boClients(ctx, tenant)
	today := o.now().In(ist).Format("2006-01-02")
	now := o.now().Format(time.RFC3339)
	touched := map[string]bool{}
	for _, r := range rows {
		if r.Record == "FZ_ID" { // a heading line
			continue
		}
		f, ok := freezeFromRow(r, today)
		if !ok {
			skipped++
			continue
		}
		f.UCC = by[f.BOID]
		key := f.BOID + "|" + f.ID
		old, _, gerr := docs.Get[CDSLFreeze](ctx, o.Docs, tenant, cCDSLFreeze, key)
		f.File, f.LastSeen, f.FirstSeen = file, now, now
		if gerr == nil {
			f.FirstSeen = old.FirstSeen
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cCDSLFreeze, key, f, actor); err != nil {
			return added, lifted, skipped, err
		}
		touched[f.BOID] = true
		what := "account"
		if f.ISIN != "" {
			what = f.ISIN + map[bool]string{true: "", false: " (" + f.Qty().String() + " shares)"}[f.Full()]
		}
		who := firstOf(f.UCC, "BO "+cdslMaskBO(f.BOID)+" (not linked to a client)")
		wasOn := gerr == nil && (old.State == "Active" || old.State == "Pending")
		switch {
		case (f.State == "Active" || f.State == "Pending") && !wasOn:
			added++
			o.raiseWork(ctx, tenant, "cdslfreeze", f.ID+":set", "CDSL froze "+who+": "+what+" for "+strings.ToLower(f.For),
				fmt.Sprintf("%s. Initiated by %s on %s%s. %s Ledgerline now blocks %s for this account. Check the order and tell the client where required.",
					f.Why(), firstOf(f.InitiatedBy, "CDSL"), firstOf(f.InitAt, "—"), map[bool]string{true: ", active from " + f.ActiveFrom, false: ""}[f.State == "Pending"],
					strings.TrimSpace(firstOf(f.OrderType, "")+" "+f.AuthDetails), freezeBlocks(f)), f.UCC, RoleMaker, "dp", "")
		case f.State == "Lifted" && (gerr != nil || old.State != "Lifted"):
			lifted++
			o.closeWork(ctx, tenant, actor, "cdslfreeze", f.ID+":set", strings.ToLower(f.Lift)+" at CDSL")
			o.raiseWork(ctx, tenant, "cdslfreeze", f.ID+":lift", "CDSL lifted freeze "+f.ID+" on "+who+" ("+strings.ToLower(f.Lift)+")",
				fmt.Sprintf("%s was %s on %s%s. The block on %s is removed unless another freeze holds it.", f.Why(), strings.ToLower(f.Lift), firstOf(f.LiftedOn, "—"),
					map[bool]string{true: ": " + f.LiftRemark, false: ""}[f.LiftRemark != ""], what), f.UCC, RoleMaker, "dp", "")
		}
	}
	for bo := range touched {
		o.refreshCDSLFreeze(ctx, tenant, actor, bo, by[bo])
	}
	return added, lifted, skipped, nil
}

func freezeBlocks(f CDSLFreeze) string {
	var b []string
	if f.debit() {
		b = append(b, "delivery instructions, eDIS, pledges and pay-in")
		if f.ISIN == "" {
			b = append(b, "automatic funds payouts")
		}
	}
	if f.credit() {
		b = append(b, "pay-outs and allotments into it")
	}
	if f.ISIN != "" {
		return strings.Join(b, " and ") + " of " + f.ISIN
	}
	return strings.Join(b, " and ")
}

// cdslFreezesOf are a BO's freezes.
func (o *Ops) cdslFreezesOf(ctx context.Context, tenant, boid string) []CDSLFreeze {
	boid = normDematID(boid)
	if boid == "" {
		return nil
	}
	fs, _ := docs.List[CDSLFreeze](ctx, o.Docs, tenant, cCDSLFreeze, boid+"|")
	return fs
}

// refreshCDSLFreeze puts the account-level freezes in force (from the
// freeze master, and the client master's freeze status) on the BO account.
func (o *Ops) refreshCDSLFreeze(ctx context.Context, tenant, actor, boid, ucc string) {
	if ucc == "" {
		return
	}
	b, _, err := docs.Get[BOAccount](ctx, o.Docs, tenant, cBO, ucc)
	if err != nil {
		c, cerr := o.Client(ctx, tenant, ucc)
		if cerr != nil {
			return
		}
		b = o.bo(ctx, tenant, c)
	}
	if normDematID(b.BOID) != normDematID(boid) {
		return
	}
	today := o.now().In(ist).Format("2006-01-02")
	var d, c bool
	var why []string
	for _, f := range o.cdslFreezesOf(ctx, tenant, boid) {
		if f.ISIN != "" || !f.blocking(today) {
			continue
		}
		d, c = d || f.debit(), c || f.credit()
		why = append(why, f.Why())
	}
	if x := b.CDSL; x != nil && strings.HasPrefix(x.Freeze, "FRZ") {
		f := firstOf(cdslFrozenFor[strings.TrimSpace(strings.TrimPrefix(x.Freeze, "FRZ"))], "Both")
		d, c = d || f != "Credit", c || f != "Debit"
		why = append(why, "frozen in CDSL's client master")
	}
	sort.Strings(why)
	nf := map[[2]bool]string{{true, true}: "Both", {true, false}: "Debit", {false, true}: "Credit"}[[2]bool{d, c}]
	nw := strings.Join(why, "; ")
	if nf == b.CDSLFreeze && nw == b.CDSLFreezeWhy {
		return
	}
	b.CDSLFreeze, b.CDSLFreezeWhy = nf, nw
	if err := docs.Upsert(ctx, o.Docs, tenant, cBO, b.UCC, b, actor); err == nil {
		o.log(ctx, tenant, actor, "dp.cdsl_freeze", b.UCC, map[string]any{"freeze": nf})
	}
}

// cdslISINFrozen is the freeze in force on one ISIN of a BO for debits
// (or, with credit, for credits): the fullest one when there are several.
func (o *Ops) cdslISINFrozen(ctx context.Context, tenant, boid, isin string, credit bool) (CDSLFreeze, bool) {
	today := o.now().In(ist).Format("2006-01-02")
	var got CDSLFreeze
	ok := false
	isin = strings.ToUpper(strings.TrimSpace(isin))
	for _, f := range o.cdslFreezesOf(ctx, tenant, boid) {
		if f.ISIN != isin || !f.blocking(today) || credit && !f.credit() || !credit && !f.debit() {
			continue
		}
		if !ok || f.Full() && !got.Full() || !got.Full() && !f.Full() && f.Qty().Cmp(got.Qty()) > 0 {
			got, ok = f, true
		}
	}
	return got, ok
}

// cdslFrozenQty is how much of a holding a debit freeze on the ISIN holds
// (all of it for a full freeze).
func (o *Ops) cdslFrozenQty(ctx context.Context, tenant, boid, isin string, held int64) int64 {
	today := o.now().In(ist).Format("2006-01-02")
	var q int64
	for _, f := range o.cdslFreezesOf(ctx, tenant, boid) {
		if f.ISIN != strings.ToUpper(isin) || !f.blocking(today) || !f.debit() {
			continue
		}
		if f.Full() {
			return max(held, 0)
		}
		q += f.Qty().Round(money.UnitRupee).Paise() / 100
	}
	return min(q, max(held, 0))
}

// CDSLFreezeTick refreshes the accounts' CDSL freezes for expiry and
// future activation (scheduler tick "cdsl_freezes", once a day early).
func (o *Ops) CDSLFreezeTick(ctx context.Context, tenant string, now time.Time) {
	fs, _ := docs.List[CDSLFreeze](ctx, o.Docs, tenant, cCDSLFreeze, "")
	today := now.In(ist).Format("2006-01-02")
	seen := map[string]bool{}
	for _, f := range fs {
		state := f.State
		switch {
		case (f.State == "Active" || f.State == "Pending") && f.Expiry != "" && f.Expiry[:10] < today:
			state = "Expired"
		case f.State == "Pending" && f.ActiveFrom != "" && f.ActiveFrom[:10] <= today:
			state = "Active"
		}
		if state != f.State {
			f.State = state
			_ = docs.Upsert(ctx, o.Docs, tenant, cCDSLFreeze, f.BOID+"|"+f.ID, f, SystemActor)
		}
		if !seen[f.BOID] && f.UCC != "" {
			seen[f.BOID] = true
			o.refreshCDSLFreeze(ctx, tenant, SystemActor, f.BOID, f.UCC)
		}
	}
}

// CDSLFreezes lists CDSL's freezes for the DP desk (a BO's, or all),
// those in force first.
func (o *Ops) CDSLFreezes(ctx context.Context, tenant, actor, ucc string) ([]CDSLFreeze, error) {
	if err := o.require(ctx, tenant, actor, "Viewing CDSL freezes", RoleMaker, RoleChecker, RoleCompliance, RoleRisk, RoleBranch); err != nil {
		return nil, err
	}
	var fs []CDSLFreeze
	if ucc = strings.ToUpper(strings.TrimSpace(ucc)); ucc != "" {
		c, err := o.Client(ctx, tenant, ucc)
		if err != nil {
			return nil, err
		}
		fs = o.cdslFreezesOf(ctx, tenant, o.bo(ctx, tenant, c).BOID)
	} else {
		fs, _ = docs.List[CDSLFreeze](ctx, o.Docs, tenant, cCDSLFreeze, "")
	}
	today := o.now().In(ist).Format("2006-01-02")
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i].blocking(today), fs[j].blocking(today)
		if a != b {
			return a
		}
		return fs[i].ID > fs[j].ID
	})
	if fs == nil {
		fs = []CDSLFreeze{}
	}
	return fs, nil
}

// settleFrozen says why a settlement pay-in (a debit) or pay-out (a
// credit) of an obligation cannot go to or from the client's demat
// account: a CDSL freeze of the account or of the ISIN ("" when none).
func (o *Ops) settleFrozen(ctx context.Context, tenant string, ob Obligation, action string) string {
	credit := action == "payout"
	if action != "payout" && action != "payin" && action != "unpledge_payin" {
		return ""
	}
	c, err := o.Client(ctx, tenant, ob.UCC)
	if err != nil {
		return ""
	}
	b := o.bo(ctx, tenant, c)
	switch {
	case credit && b.creditFrozen():
		return fmt.Sprintf("%s's demat account is frozen for credits (%s): the pay-out of %s cannot be credited", ob.UCC, b.freezeWhy(), firstOf(ob.Symbol, ob.ISIN))
	case !credit && b.debitFrozen():
		return fmt.Sprintf("%s's demat account is frozen for debits (%s): %s cannot be paid in from it", ob.UCC, b.freezeWhy(), firstOf(ob.Symbol, ob.ISIN))
	}
	if fz, ok := o.cdslISINFrozen(ctx, tenant, b.BOID, ob.ISIN, credit); ok && (credit || fz.Full()) {
		return fmt.Sprintf("%s in %s's demat account is frozen at CDSL (%s)", firstOf(ob.Symbol, ob.ISIN), ob.UCC, fz.Why())
	}
	return ""
}
