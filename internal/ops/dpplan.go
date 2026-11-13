package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
)

// ---- DP tariff plans ----
//
// A firm's DP clients do not all pay the same: regular accounts, BSDA,
// accounts without e-statements, a group of related clients, the firm's own
// pool accounts and closed accounts each have a rate card of their own
// (SharePro calls them baskets). A DPPlan is one rate card, with dated
// versions; each BO account is on one plan at a time and keeps its history
// (BOAccount.Plans). An account on no plan pays the firm's tariff
// (DPTariff), as before plans.
//
// A version has a line per kind of charge, named by the depository's
// transaction type code (dpChargeCodes: DCM a delivery to a clearing
// member, ODTA an off-market delivery within the depository, 2201 a demat
// request …). A line is a flat charge, a percentage of value and a charge
// per unit (certificate), with a minimum and a maximum per instruction, and
// the rate for transfers linked to the firm's own trades (to or from its
// own pool accounts or clearing member, DPTariff.ChargeFree and
// ChargeFreeCMs): no Own rate means those are free.
//
// Annual maintenance is the version's AMCYear (CorporateAMCYear on top for
// a body corporate), billed a twelfth each month, or a year in advance on
// the account's own cycle: the year after BOAccount.AMCPaidUpto (the old
// system's last AMC period, at migration), never back-dated before the
// month billed. A BSDA account keeps the firm's BSDA slabs.

const cDPPlan = "dpplan" // key: plan code

// AMC billing of a plan.
const (
	AMCMonthly = "Monthly" // a twelfth each month
	AMCYearly  = "Yearly"  // a year in advance on the account's cycle
)

// DPRate is what one instruction costs (₹, before GST).
type DPRate struct {
	Flat    money.Amount `json:"flat,omitempty"`     // per instruction
	Pct     money.Dec    `json:"pct"`                // % of the value
	PerUnit money.Amount `json:"per_unit,omitempty"` // per certificate (demat, remat)
	Min     money.Amount `json:"min,omitempty"`
	Max     money.Amount `json:"max,omitempty"` // 0: no maximum
}

// charge is the rate applied to one instruction.
func (r DPRate) charge(value money.Dec, units int64) money.Amount {
	a := r.Flat + r.PerUnit*money.Amount(units)
	if r.Pct.Sign() > 0 && value.Sign() > 0 {
		a += value.Pct(r.Pct).Round(money.UnitPaisa)
	}
	if a < r.Min {
		a = r.Min
	}
	if r.Max > 0 && a > r.Max {
		a = r.Max
	}
	return a
}

func sameRate(a, b DPRate) bool {
	return a.Flat == b.Flat && a.Pct.Cmp(b.Pct) == 0 && a.PerUnit == b.PerUnit && a.Min == b.Min && a.Max == b.Max
}

func (r DPRate) zero() bool {
	return r.Flat == 0 && r.Pct.Sign() == 0 && r.PerUnit == 0 && r.Min == 0 && r.Max == 0
}

// DPChargeLine is one kind of charge on a plan.
type DPChargeLine struct {
	Code string `json:"code"` // the depository's transaction type (dpChargeCodes) or the old system's code
	Name string `json:"name,omitempty"`
	DPRate
	// Own is the rate for transfers to or from the firm's own pool
	// accounts or clearing member; nil: those are free.
	Own *DPRate `json:"own,omitempty"`
	// Mail is a courier charge per request (demat, remat).
	Mail money.Amount `json:"mail,omitempty"`
	// Special is another rate set the old system kept (SharePro's SP
	// rates), kept for the record; Ledgerline does not bill it.
	Special *DPRate `json:"special,omitempty"`
	// Off: not charged; the rates are kept (a pledge event the firm does
	// not charge for, as the old system had it).
	Off bool `json:"off,omitempty"`
}

// DPPlanVersion is a plan's rates from a date.
type DPPlanVersion struct {
	From             string         `json:"from"`
	AMCYear          money.Amount   `json:"amc_year"`
	CorporateAMCYear money.Amount   `json:"corporate_amc_year,omitempty"` // on top of AMCYear, for a body corporate
	Lines            []DPChargeLine `json:"lines"`
}

// line is the version's line for a code.
func (v DPPlanVersion) line(code string) (DPChargeLine, bool) {
	for _, l := range v.Lines {
		if l.Code == code {
			return l, true
		}
	}
	return DPChargeLine{}, false
}

// DPPlan is a DP rate card (a SharePro basket).
type DPPlan struct {
	Code string `json:"code"`
	Name string `json:"name"`
	// Pool: the firm's own pool or clearing member accounts.
	Pool       bool            `json:"pool,omitempty"`
	AMCBilling string          `json:"amc_billing"` // AMCMonthly or AMCYearly
	Versions   []DPPlanVersion `json:"versions"`    // oldest first
	Note       string          `json:"note,omitempty"`
}

// at is the version in force on a date (nil: none yet).
func (p DPPlan) at(date string) *DPPlanVersion {
	var v *DPPlanVersion
	for i := range p.Versions {
		if p.Versions[i].From <= date {
			v = &p.Versions[i]
		}
	}
	return v
}

// DPPlanAt is an account's plan from a date.
type DPPlanAt struct {
	Plan string `json:"plan"`
	From string `json:"from"`
}

// planAt is the account's plan on a date ("": the firm's tariff).
func (b BOAccount) planAt(date string) string {
	p := ""
	for _, x := range b.Plans {
		if x.From <= date {
			p = x.Plan
		}
	}
	return p
}

// Pledge events: the depository's pledge transactions a firm charges for,
// by Ledgerline's own codes, as lines of the firm tariff or of a plan. They
// are read from the depository's transaction file (cdslPledgeEvent): the
// one row of a pledge's steps that moves the pledged balance.
const (
	PledgeCreated       = "PLG"  // pledge created (the pledgee accepted)
	MarginPledgeCreated = "PLGM" // margin pledge to the broker created
	MarginRepledge      = "RPLG" // margin pledge re-pledged to the clearing corporation
	PledgeClosed        = "UNP"  // unpledge: the pledge closed, by request or automatically
	MarginPledgeClosed  = "UNPM" // margin unpledge
	PledgeInvoked       = "INV"  // the pledgee invoked the pledge (confiscation)
	MarginPledgeInvoked = "INVM" // the broker invoked a margin pledge
)

// PledgeEvents lists the pledge events in the order the tariff tables show.
var PledgeEvents = []string{PledgeCreated, MarginPledgeCreated, MarginRepledge, PledgeClosed, MarginPledgeClosed, PledgeInvoked, MarginPledgeInvoked}

func isPledgeEvent(code string) bool {
	for _, e := range PledgeEvents {
		if e == code {
			return true
		}
	}
	return false
}

// PledgeEvent is a pledge event's code and name, for the tariff tables.
type PledgeEvent struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func pledgeEventList() []PledgeEvent {
	out := make([]PledgeEvent, 0, len(PledgeEvents))
	for _, e := range PledgeEvents {
		out = append(out, PledgeEvent{e, dpChargeCodes[e]})
	}
	return out
}

// cdslPledgeEvent is the pledge event a row of CDSL's transaction file
// (COD, UDiFF) records, or "". A pledge's maker, checker and pledgee steps
// are rows of their own; only the row that moves the pledged balance is an
// event: TxnCd CPLBL (credit pledge balance) when the pledge is created,
// CRPLBL when a margin pledge is re-pledged, DPLBL (debit pledge balance)
// when it is closed or invoked. TxnTyp PIC or PIV, or an invocation
// status, tells an invocation from a closure; PldgIdr MP (or a margin
// pledge status) a margin pledge from a normal one.
func cdslPledgeEvent(txnTyp, txnCd, pldgIdr, status string) string {
	tp, cd, idr, st := strings.ToUpper(strings.TrimSpace(txnTyp)), strings.ToUpper(strings.TrimSpace(txnCd)), strings.ToUpper(strings.TrimSpace(pldgIdr)), strings.ToUpper(strings.TrimSpace(status))
	margin := idr == "MP" || idr == "M2" || idr == "M3" || strings.HasPrefix(st, "MP") || cdslMarginPledgeStatus[st]
	switch cd {
	case "CPLBL":
		if margin {
			return MarginPledgeCreated
		}
		return PledgeCreated
	case "CRPLBL":
		return MarginRepledge
	case "DPLBL":
		if tp == "PIC" || tp == "PIV" || cdslInvokedStatus[st] {
			if margin {
				return MarginPledgeInvoked
			}
			return PledgeInvoked
		}
		if margin {
			return MarginPledgeClosed
		}
		return PledgeClosed
	}
	return ""
}

// CDSL's statuses (OrdSttsTo) of margin pledge steps whose code does not
// start with MP, and of invocations (confiscations).
var (
	cdslMarginPledgeStatus = map[string]bool{"PACPB": true, "PACPEB": true, "PCADPB": true, "PCAPEB": true, "PCSPGM": true, "MUAPCP": true}
	cdslInvokedStatus      = map[string]bool{"CONFCT": true, "PINV": true, "PCINV": true, "PCADPB": true, "PCAPEB": true, "RCADPB": true, "RCAPEB": true, "RCARPB": true,
		"RCPEBC": true, "MRCDPEB": true, "MRCDRPB": true}
)

// pledgeCharges bills an account's pledge events in a month on the pledge
// lines given (the firm tariff's or the plan version's): per ISIN per
// instruction. An event with no line, or a line not charged, is free.
func pledgeCharges(lines []DPChargeLine, ts []DPTxn, in func(DPTxn) bool, line func(DPBillLine)) {
	type agg struct {
		n   int64
		amt money.Amount
	}
	by := map[string]*agg{}
	find := func(code string) (DPChargeLine, bool) {
		for _, l := range lines {
			if l.Code == code {
				return l, true
			}
		}
		return DPChargeLine{}, false
	}
	for _, x := range ts {
		if x.Pledge == "" || !in(x) {
			continue
		}
		l, ok := find(x.Pledge)
		if !ok || l.Off {
			continue
		}
		amt := l.DPRate.charge(x.Value.Abs(), 0)
		if amt <= 0 {
			continue
		}
		if by[x.Pledge] == nil {
			by[x.Pledge] = &agg{}
		}
		by[x.Pledge].n++
		by[x.Pledge].amt += amt
	}
	for _, c := range PledgeEvents {
		if a := by[c]; a != nil {
			l, _ := find(c)
			line(DPBillLine{What: firstOf(l.Name, dpChargeCodes[c]) + " (per ISIN)", Count: a.n, Amount: a.amt})
		}
	}
}

// cleanPledgeLines checks a table of pledge charges and keeps the rows
// with a rate or marked not charged, in the events' order.
func cleanPledgeLines(in []DPChargeLine) ([]DPChargeLine, error) {
	by := map[string]DPChargeLine{}
	for _, l := range in {
		l.Code = strings.ToUpper(strings.TrimSpace(l.Code))
		name := dpChargeCodes[l.Code]
		switch {
		case !isPledgeEvent(l.Code):
			return nil, invalid("%q is not a pledge charge: use one of %s", l.Code, strings.Join(PledgeEvents, ", "))
		case l.Flat < 0 || l.Min < 0 || l.Max < 0 || l.Pct.Sign() < 0 || l.PerUnit != 0:
			return nil, invalid("%s: charges cannot be negative", name)
		case l.Pct.Cmp(money.MustDec("100")) > 0:
			return nil, invalid("%s: the %% of value must be from 0 to 100", name)
		case l.Max > 0 && l.Max < l.Min:
			return nil, invalid("%s: the maximum is below the minimum", name)
		}
		if _, dup := by[l.Code]; dup {
			return nil, invalid("%s is in the table twice", name)
		}
		l.Name, l.Own, l.Mail, l.Special = name, nil, 0, nil
		by[l.Code] = l
	}
	out := []DPChargeLine{}
	for _, c := range PledgeEvents {
		if l, ok := by[c]; ok && (!l.DPRate.zero() || l.Off) {
			out = append(out, l)
		}
	}
	return out, nil
}

// SetDPPlanPledge sets a plan's pledge charges from a date: the version
// that starts on it, or a new one copied from the version in force, and
// every later version get the table's pledge lines.
func (o *Ops) SetDPPlanPledge(ctx context.Context, tenant, actor, code, from string, lines []DPChargeLine) (DPPlan, error) {
	if err := o.require(ctx, tenant, actor, "Changing the DP tariff", RoleAdmin, RoleChecker); err != nil {
		return DPPlan{}, err
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	p, _, err := docs.Get[DPPlan](ctx, o.Docs, tenant, cDPPlan, code)
	if err != nil {
		return p, fmt.Errorf("%w: no DP tariff plan %s", ErrNotFound, code)
	}
	if !ledger.ValidDate(from) {
		return p, invalid("give the date the pledge charges apply from (YYYY-MM-DD)")
	}
	pl, err := cleanPledgeLines(lines)
	if err != nil {
		return p, err
	}
	starts := false
	for _, v := range p.Versions {
		starts = starts || v.From == from
	}
	if !starts {
		base := p.at(from)
		if base == nil && len(p.Versions) > 0 {
			base = &p.Versions[0]
		}
		nv := DPPlanVersion{From: from, Lines: []DPChargeLine{}}
		if base != nil {
			nv.AMCYear, nv.CorporateAMCYear = base.AMCYear, base.CorporateAMCYear
			nv.Lines = append(nv.Lines, base.Lines...)
		}
		p.Versions = append(p.Versions, nv)
		sort.Slice(p.Versions, func(i, j int) bool { return p.Versions[i].From < p.Versions[j].From })
	}
	for i := range p.Versions {
		v := &p.Versions[i]
		if v.From < from {
			continue
		}
		keep := []DPChargeLine{}
		for _, l := range v.Lines {
			if !isPledgeEvent(l.Code) {
				keep = append(keep, l)
			}
		}
		v.Lines = append(keep, pl...)
		sort.Slice(v.Lines, func(a, b int) bool { return v.Lines[a].Code < v.Lines[b].Code })
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cDPPlan, code, p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "dp.plan_pledge", code, map[string]any{"from": from, "lines": len(pl)})
	o.dpFilledIn(ctx, tenant, actor, pkg.FillDPPledgeCharges, code)
	return p, nil
}

// dpChargeCodes are the depository transaction types a plan line can name
// (CDSL's codes; NSDL's are taken as their CDSL equivalents), with what
// Ledgerline bills on each.
var dpChargeCodes = map[string]string{
	PledgeCreated: "Pledge created", MarginPledgeCreated: "Margin pledge created", MarginRepledge: "Margin re-pledge (to the clearing corporation)",
	PledgeClosed: "Unpledge (pledge closed)", MarginPledgeClosed: "Margin unpledge", PledgeInvoked: "Pledge invoked", MarginPledgeInvoked: "Margin pledge invoked",
	"DCM": "Delivery to a clearing member (pay-in)", "DCCH": "Delivery to the clearing corporation", "DOWN": "Delivery to an account of this DP",
	"DOTH": "Delivery to another DP's account", "MDEL": "Market delivery", "ODTA": "Off-market delivery (within the depository)",
	"ODTR": "Off-market delivery (to the other depository)", "RCCH": "Receipt from the clearing corporation (pay-out)", "ROWN": "Receipt from an account of this DP",
	"ROTH": "Receipt from another DP's account", "MREC": "Market receipt", "ORTA": "Off-market receipt (within the depository)",
	"ORTR": "Off-market receipt (from the other depository)", "2201": "Dematerialisation", "2205": "Rematerialisation", "2230": "Pledge",
	"2280": "Unpledge", "PLGD": "Pledge set-up", "PLGA": "Pledge acceptance", "PLGR": "Pledge rejection", "UPGC": "Unpledge acceptance",
	"MPGD": "Margin pledge set-up", "MPGA": "Margin pledge acceptance", "MPUS": "Margin unpledge set-up", "MPUA": "Margin unpledge acceptance",
	"IFAL": "Instruction failure", "SETL": "Settlement fee", "CSHD": "CISA account holding", "CSTR": "CISA account transfer",
	"2221": "Lending", "2225": "Borrowing", "2210": "Freeze", "2211": "Safe keeping", "2212": "Lock-in", "2277": "Restatement",
	"3108": "Destatement", "RSTS": "Restat set-up", "RSTA": "Restat acceptance", "DCNF": "Destat confirmation", "0001": "Account opening",
	"0002": "Agreement", "0003": "Account closure", "0006": "Depository charges", "0007": "Courier", "WOST": "POA / DDPI",
	"WST2": "Documentation", "CHSB": "Slip book", "CDIS": "Cheque dishonour", "ADST": "Additional statement", "CHIN": "Change of instruction",
	"CUST": "Custody", "CNFA": "Confiscation approved", "CNFR": "Confiscation rejected", "DREJ": "Demat rejection not collected",
	"0004": "Adjusted minimum charges", "0005": "Adjusted maximum charges",
}

// dpChargeCode is the plan line a DP transaction is billed on ("": none).
func dpChargeCode(x DPTxn) string {
	k := strings.ToLower(x.Kind)
	debit := x.Qty.Sign() < 0
	switch {
	case x.Certs > 0:
		return "2201"
	case strings.HasPrefix(k, "ipo") || strings.HasPrefix(k, "pledge") || strings.HasPrefix(k, "transmission") || strings.HasPrefix(k, "account transfer"):
		return "" // corporate action credits; pledges and transmissions are not instructions billed here
	case strings.HasPrefix(k, "delivery to clearing") || strings.HasPrefix(k, "on-market"):
		if debit {
			return "DCM"
		}
		return "RCCH"
	case strings.HasPrefix(k, "pay-out from clearing"):
		if debit {
			return "DCM"
		}
		return "RCCH"
	case strings.HasPrefix(k, "inter-depository"):
		if debit {
			return "ODTR"
		}
		return "ORTR"
	case strings.HasPrefix(k, "off-market"):
		if debit {
			return "ODTA"
		}
		return "ORTA"
	}
	if debit {
		return "DOTH"
	}
	return "ROTH"
}

// DPPlans lists the plans, by code.
func (o *Ops) DPPlans(ctx context.Context, tenant string) ([]DPPlan, error) {
	ps, err := docs.List[DPPlan](ctx, o.Docs, tenant, cDPPlan, "")
	if err != nil {
		return nil, err
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Code < ps[j].Code })
	return ps, nil
}

func (o *Ops) dpPlanMap(ctx context.Context, tenant string) map[string]DPPlan {
	ps, _ := o.DPPlans(ctx, tenant)
	m := map[string]DPPlan{}
	for _, p := range ps {
		m[p.Code] = p
	}
	return m
}

// SetDPPlan puts a BO account on a plan from a date (a checker or admin).
func (o *Ops) SetDPPlan(ctx context.Context, tenant, actor, boid, plan, from string) (BOAccount, error) {
	if err := o.require(ctx, tenant, actor, "Changing a DP account's tariff plan", RoleAdmin, RoleChecker); err != nil {
		return BOAccount{}, err
	}
	b, ok := o.boAccount(ctx, tenant, boid)
	if !ok {
		return BOAccount{}, fmt.Errorf("%w: no DP account %s", ErrNotFound, boid)
	}
	plan = strings.ToUpper(strings.TrimSpace(plan))
	if plan != "" {
		if _, ok := o.dpPlanMap(ctx, tenant)[plan]; !ok {
			return b, invalid("there is no DP tariff plan %s", plan)
		}
	}
	if !ledger.ValidDate(from) {
		return b, invalid("give the date the plan applies from (YYYY-MM-DD)")
	}
	var ps []DPPlanAt
	for _, x := range b.Plans {
		if x.From < from {
			ps = append(ps, x)
		}
	}
	b.Plans = append(ps, DPPlanAt{Plan: plan, From: from})
	if err := docs.Upsert(ctx, o.Docs, tenant, cBO, boKey(b), b, actor); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "dp.plan", normDematID(b.BOID), map[string]any{"plan": plan, "from": from})
	return b, nil
}

// ---- imports ----

func dpPlanSources() []masterSrc {
	return []masterSrc{
		{"DP-TARIFFS", "The DP's tariff plans (migration: the previous back office's tariff baskets), one row per charge of a plan version: Plan (its code), From (the date the version applies from) and Code (the depository's transaction type: DCM, DOWN, DOTH, ODTA, ODTR, RCCH, 2201 demat, 2230 pledge, MPGD margin pledge, IFAL failure, SETL settlement and so on; AM is the annual maintenance and 0015 the corporate maintenance on top; the pledge events PLG pledge created, PLGM margin pledge created, RPLG margin re-pledge, UNP unpledge, UNPM margin unpledge, INV pledge invoked and INVM margin pledge invoked, per ISIN). Optional: Name (the plan's), Pool (Y for the firm's own pool or clearing member accounts), AMCBilling (Monthly or Yearly: a year in advance on each account's cycle), Charge (the line's name), Charged (N: not charged, the rates kept), Flat, Pct (% of value), PerUnit (per certificate), Min and Max (per instruction), OwnFlat, OwnPct, OwnPerUnit, OwnMin and OwnMax (transfers linked to the firm's own trades; blank: free), Mail (courier per request) and SpecialFlat, SpecialPct, SpecialPerUnit, SpecialMin and SpecialMax (another rate set of the old system, kept for the record). The plans in the file replace those stored with the same code", []string{"Plan", "From", "Code"}, false, "Once at go-live, then on change"},
		{"DP-TARIFF-ACCOUNTS", "Which tariff plan each DP account is on (migration), one row per change: BOID, Plan and From are required; optional AMCPaidUpto (the date the account's annual maintenance is paid up to, so a yearly plan bills the next year when it falls due). An account's rows replace its plan history; the plan must be on record (DP-TARIFFS) and the account a BO account of the DP (DP-ACCOUNTS)", []string{"BOID", "Plan", "From"}, false, "Once at go-live, then on change"},
	}
}

func amountCell(r row, col string, bad func(l int, f, m string, a ...any)) money.Amount {
	v := strings.ReplaceAll(strings.TrimSpace(r.get(col)), ",", "")
	if v == "" {
		return 0
	}
	a, err := money.ParseAmount(v)
	if err != nil || a < 0 {
		bad(r.line, col, "must be an amount in rupees, 0 or more, got %q", r.get(col))
		return 0
	}
	return a
}

func pctCell(r row, col string, bad func(l int, f, m string, a ...any)) money.Dec {
	v := strings.TrimSpace(r.get(col))
	if v == "" {
		return money.Dec{}
	}
	d, err := money.ParseDec(v)
	if err != nil || d.Sign() < 0 || d.Cmp(money.MustDec("100")) > 0 {
		bad(r.line, col, "must be a percentage from 0 to 100, got %q", v)
		return money.Dec{}
	}
	return d
}

func (o *Ops) prepareDPTariffs(ctx context.Context, tenant, actor string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	bad := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	warn := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...), Warning: true})
	}
	if err := o.require(ctx, tenant, actor, "Changing the DP tariff", RoleAdmin, RoleChecker); err != nil {
		bad(1, "", "%v", err)
		return nil, errs
	}
	plans := map[string]*DPPlan{}
	var order []string
	vers := map[string]map[string]*DPPlanVersion{}
	seen := map[string]int{}
	for _, r := range rows {
		code := strings.ToUpper(strings.TrimSpace(r.get("Plan")))
		from := strings.TrimSpace(r.get("From"))
		lc := strings.ToUpper(strings.TrimSpace(r.get("Code")))
		switch {
		case code == "" || len(code) > 20 || strings.ContainsAny(code, "|/ "):
			bad(r.line, "Plan", "a plan code of up to 20 letters or digits, with no spaces, got %q", r.get("Plan"))
			continue
		case !ledger.ValidDate(from):
			bad(r.line, "From", "must be YYYY-MM-DD, got %q", from)
			continue
		case lc == "":
			bad(r.line, "Code", "give the charge's transaction type code")
			continue
		}
		if l, ok := seen[code+"|"+from+"|"+lc]; ok {
			bad(r.line, "Code", "%s from %s has %s on line %d too", code, from, lc, l)
			continue
		}
		seen[code+"|"+from+"|"+lc] = r.line
		p := plans[code]
		if p == nil {
			p = &DPPlan{Code: code, Name: strings.TrimSpace(r.get("Name")), AMCBilling: AMCMonthly}
			plans[code], vers[code] = p, map[string]*DPPlanVersion{}
			order = append(order, code)
			p.Pool = ynCell(r, "Pool", false, bad)
			switch b := strings.ToLower(strings.TrimSpace(r.get("AMCBilling"))); b {
			case "", "monthly":
			case "yearly", "annual", "annually":
				p.AMCBilling = AMCYearly
			default:
				bad(r.line, "AMCBilling", "must be Monthly or Yearly, got %q", r.get("AMCBilling"))
			}
		} else if n := strings.TrimSpace(r.get("Name")); n != "" && p.Name == "" {
			p.Name = n
		}
		v := vers[code][from]
		if v == nil {
			v = &DPPlanVersion{From: from, Lines: []DPChargeLine{}}
			vers[code][from] = v
		}
		rate := func(prefix string) DPRate {
			return DPRate{Flat: amountCell(r, prefix+"Flat", bad), Pct: pctCell(r, prefix+"Pct", bad), PerUnit: amountCell(r, prefix+"PerUnit", bad),
				Min: amountCell(r, prefix+"Min", bad), Max: amountCell(r, prefix+"Max", bad)}
		}
		main := rate("")
		if main.Max > 0 && main.Max < main.Min {
			bad(r.line, "Max", "the maximum is below the minimum")
		}
		switch lc {
		case "AM":
			v.AMCYear = max(main.Flat, main.Min)
			continue
		case "0015":
			v.CorporateAMCYear = max(main.Flat, main.Min)
			continue
		}
		if _, known := dpChargeCodes[lc]; !known {
			warn(r.line, "Code", "%s is not a depository transaction type Ledgerline bills on; the charge is kept on the plan for the record", lc)
		}
		l := DPChargeLine{Code: lc, Name: firstOf(strings.TrimSpace(r.get("Charge")), dpChargeCodes[lc]), DPRate: main, Mail: amountCell(r, "Mail", bad), Off: !ynCell(r, "Charged", true, bad)}
		if own := rate("Own"); !own.zero() {
			l.Own = &own
		}
		if sp := rate("Special"); !sp.zero() && !sameRate(sp, main) {
			l.Special = &sp
		}
		v.Lines = append(v.Lines, l)
	}
	for _, code := range order {
		p := plans[code]
		for _, from := range sortedKeys(vers[code]) {
			v := vers[code][from]
			sort.Slice(v.Lines, func(i, j int) bool { return v.Lines[i].Code < v.Lines[j].Code })
			p.Versions = append(p.Versions, *v)
		}
		if p.Name == "" {
			p.Name = code
		}
	}
	return func() (int, int, error) {
		n, same := 0, 0
		for _, code := range order {
			p := *plans[code]
			if old, _, err := docs.Get[DPPlan](ctx, o.Docs, tenant, cDPPlan, code); err == nil {
				p.Note = firstOf(p.Note, old.Note)
				if sameJSON(old, p) {
					same++
					continue
				}
			}
			if err := docs.Upsert(ctx, o.Docs, tenant, cDPPlan, code, p, actor); err != nil {
				return n, same, err
			}
			n++
		}
		if n > 0 {
			o.log(ctx, tenant, actor, "dp.plans_loaded", "", map[string]any{"plans": n})
		}
		return n, same, nil
	}, errs
}

func (o *Ops) prepareDPTariffAccounts(ctx context.Context, tenant, actor string, rows []row) (func() (int, int, error), []ingest.ParseError) {
	var errs []ingest.ParseError
	bad := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...)})
	}
	warn := func(l int, f, m string, a ...any) {
		errs = append(errs, ingest.ParseError{Line: l, Field: f, Msg: fmt.Sprintf(m, a...), Warning: true})
	}
	if err := o.require(ctx, tenant, actor, "Changing a DP account's tariff plan", RoleAdmin, RoleChecker); err != nil {
		bad(1, "", "%v", err)
		return nil, errs
	}
	plans := o.dpPlanMap(ctx, tenant)
	type acct struct {
		plans []DPPlanAt
		paid  string
	}
	by := map[string]*acct{}
	var order []string
	seen := map[string]int{}
	for _, r := range rows {
		bo := normDematID(r.get("BOID"))
		plan := strings.ToUpper(strings.TrimSpace(r.get("Plan")))
		from := strings.TrimSpace(r.get("From"))
		if _, ok := o.boAccount(ctx, tenant, bo); !ok {
			warn(r.line, "BOID", "%s is not a BO account of the DP (DP-ACCOUNTS); the row is left out", firstOf(bo, "(blank)"))
			continue
		}
		if _, ok := plans[plan]; !ok {
			bad(r.line, "Plan", "there is no DP tariff plan %q (load DP-TARIFFS first)", plan)
			continue
		}
		if !ledger.ValidDate(from) {
			bad(r.line, "From", "must be YYYY-MM-DD, got %q", from)
			continue
		}
		if l, ok := seen[bo+"|"+from]; ok {
			bad(r.line, "From", "%s already changes plan on %s (line %d)", bo, from, l)
			continue
		}
		seen[bo+"|"+from] = r.line
		a := by[bo]
		if a == nil {
			a = &acct{}
			by[bo] = a
			order = append(order, bo)
		}
		a.plans = append(a.plans, DPPlanAt{Plan: plan, From: from})
		if p := strings.TrimSpace(r.get("AMCPaidUpto")); p != "" {
			if !ledger.ValidDate(p) {
				bad(r.line, "AMCPaidUpto", "must be YYYY-MM-DD, got %q", p)
			} else if p > a.paid {
				a.paid = p
			}
		}
	}
	return func() (int, int, error) {
		n, same := 0, 0
		for _, bo := range order {
			a := by[bo]
			sort.Slice(a.plans, func(i, j int) bool { return a.plans[i].From < a.plans[j].From })
			b, ok := o.boAccount(ctx, tenant, bo)
			if !ok {
				continue
			}
			old := b
			b.Plans = a.plans
			if a.paid != "" {
				b.AMCPaidUpto = a.paid
			}
			if sameJSON(old, b) {
				same++
				continue
			}
			if err := docs.Upsert(ctx, o.Docs, tenant, cBO, boKey(b), b, actor); err != nil {
				return n, same, err
			}
			n++
		}
		if n > 0 {
			o.log(ctx, tenant, actor, "dp.plan_accounts_loaded", "", map[string]any{"accounts": n})
		}
		return n, same, nil
	}, errs
}

// ---- billing on a plan ----

// dpPlanBill adds an account's charges for a month on its plan to the
// bill: maintenance as the plan bills it, and each billable transaction on
// its line. It returns the account's new AMC paid-up date ("": unchanged).
func dpPlanBill(p DPPlan, v DPPlanVersion, t DPTariff, free map[string]string, r BORow, corporate bool, month, end string, ts []DPTxn, mine func(DPTxn) bool, line func(DPBillLine), freeCount *int64) string {
	amc := v.AMCYear
	if r.BSDA {
		amc = amcFor(t, true, r.Value)
	}
	corp := money.Amount(0)
	if corporate {
		corp = v.CorporateAMCYear
	}
	paid := ""
	switch p.AMCBilling {
	case AMCYearly:
		upto := r.AMCPaidUpto
		if upto >= "9999" || (upto != "" && upto >= end) {
			break // paid up beyond this month (9999: for life)
		}
		from := month + "-01"
		if upto != "" {
			if d, err := time.Parse("2006-01-02", upto); err == nil {
				if nx := d.AddDate(0, 0, 1).Format("2006-01-02"); nx > from {
					from = nx
				}
			}
		} else if r.Opened > from && r.Opened <= end {
			from = r.Opened
		}
		f, _ := time.Parse("2006-01-02", from)
		to := f.AddDate(1, 0, -1).Format("2006-01-02")
		if amc > 0 {
			line(DPBillLine{What: fmt.Sprintf("Annual maintenance%s, %s to %s", map[bool]string{true: " (BSDA)"}[r.BSDA], from, to), Count: 1, Amount: amc})
		}
		if corp > 0 {
			line(DPBillLine{What: fmt.Sprintf("Corporate account maintenance, %s to %s", from, to), Count: 1, Amount: corp})
		}
		paid = to
	default:
		if a := (amc + corp) / 12; a > 0 {
			line(DPBillLine{What: map[bool]string{true: "Annual maintenance (BSDA), one month", false: "Annual maintenance, one month"}[r.BSDA], Count: 1, Amount: a})
		}
	}
	type agg struct {
		n   int64
		amt money.Amount
	}
	by := map[string]*agg{}
	var codes []string
	mailed := map[string]bool{}
	for _, x := range ts {
		if x.Date < month+"-01" || x.Date > end || !x.Billable || !mine(x) {
			continue
		}
		code := dpChargeCode(x)
		l, ok := v.line(code)
		if code == "" || !ok {
			continue
		}
		rate := l.DPRate
		if _, own := t.freeFor(free, x); own && x.Certs == 0 {
			if l.Own == nil {
				*freeCount++
				continue
			}
			rate = *l.Own
		}
		amt := rate.charge(x.Value.Abs(), int64(x.Certs))
		if l.Mail > 0 && x.Certs > 0 && !mailed[x.Ref] {
			amt += l.Mail
			mailed[x.Ref] = true
		}
		if amt == 0 {
			continue
		}
		if by[code] == nil {
			by[code] = &agg{}
			codes = append(codes, code)
		}
		by[code].n++
		by[code].amt += amt
	}
	for _, c := range codes {
		l, _ := v.line(c)
		line(DPBillLine{What: firstOf(l.Name, firstOf(dpChargeCodes[c], c)), Count: by[c].n, Amount: by[c].amt})
	}
	pledgeCharges(v.Lines, ts, func(x DPTxn) bool { return x.Date >= month+"-01" && x.Date <= end && mine(x) }, line)
	return paid
}
