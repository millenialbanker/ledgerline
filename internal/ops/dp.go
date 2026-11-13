package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
)

// ---- depository participant (DP) operations ----
//
// The firm is also a DP. Each client's demat account (beneficial owner, BO)
// is kept here with its status (active, frozen for debits, credits or both,
// closed) and whether it is a Basic Services Demat Account (BSDA).
//
// Transfers out of a BO need a delivery instruction slip (DIS) from a book
// issued to that BO, or the client's DDPI. Slips are entered by a maker and
// verified by a different checker against the slip register and the free
// balance; instructions above ₹5 lakh need the client's confirmation on
// record. Verified slips go out in the upload batch for the execution date,
// and holdings move when the DP system confirms execution.
//
// DP charges follow the firm's tariff: annual maintenance (nil or reduced
// for BSDA), a charge per debit instruction per ISIN (of which the
// depository's share is payable to it), pledge, demat and off-market
// charges. The monthly DP bill is posted to the client's trading ledger
// with GST and a tax invoice.

const (
	cBO       = "bo"
	cDISBook  = "disbook"
	cDIS      = "dis"
	cDPTxn    = "dptxn"
	cDPTariff = "dptariff"
	cDPBill   = "dpbill"
	cDPReq    = "dpreq"

	IncomeDP          = "INCOME:DP_CHARGES"
	PayableDepository = "PAYABLE:DEPOSITORY"
)

// BOAccount is the DP's record of a client's demat account, stored by BO ID
// (one client may have several; demats.go).
type BOAccount struct {
	UCC        string `json:"ucc"`
	BOID       string `json:"bo_id"`
	Depository string `json:"depository"`
	// JointHolders are the second and third holders of a joint account
	// (the client is the first holder).
	JointHolders []JointHolder `json:"joint_holders,omitempty"`
	// MovedTo marks a record of the time when BO accounts were kept by UCC:
	// it was moved to the key its BO ID gives (demats.go) and is not an
	// account.
	MovedTo   string `json:"moved_to,omitempty"`
	Status    string `json:"status"`           // Active, Frozen, Closed
	Freeze    string `json:"freeze,omitempty"` // Debit, Credit, Both
	FreezeWhy string `json:"freeze_reason,omitempty"`
	BSDA      bool   `json:"bsda"`
	// BSDAOptOut: the client chose a regular account although eligible;
	// OtherDemat: the client has a demat account with another DP.
	BSDAOptOut bool `json:"bsda_opt_out,omitempty"`
	OtherDemat bool `json:"other_demat,omitempty"`
	// DDPI: the client has a DDPI (or a POA for settlement) with the firm
	// on this demat, so sales are delivered without a slip or eDIS; it is
	// set from CDSL's client master (cdslclient.go). Without it a sale
	// needs eDIS (or a DIS).
	DDPI     bool   `json:"ddpi"`
	Opened   string `json:"opened"`
	ClosedOn string `json:"closed_on,omitempty"`
	// CDSL is what CDSL's client master last said about the account
	// (cdslclient.go).
	CDSL *BOCDSL `json:"cdsl,omitempty"`
	// CDSLFreeze is an account-level freeze CDSL reports (Debit, Credit
	// or Both): from the freeze master (DPE6, cdslfreeze.go) or the client
	// master's freeze status. It is kept apart from Freeze, which is the
	// DP's own freeze request, so an unfreeze here cannot lift CDSL's.
	CDSLFreeze    string `json:"cdsl_freeze,omitempty"`
	CDSLFreezeWhy string `json:"cdsl_freeze_reason,omitempty"`
	// Plans is the account's DP tariff plan history (dpplan.go; none: the
	// firm's tariff); AMCPaidUpto the date its annual maintenance is paid
	// up to, for a plan that bills it a year in advance.
	Plans       []DPPlanAt `json:"plans,omitempty"`
	AMCPaidUpto string     `json:"amc_paid_upto,omitempty"`
}

// BOCDSL is the account as CDSL's client master (CLN_MSTR) shows it.
type BOCDSL struct {
	Status       string `json:"status"`                  // CDSL's account status (ACT, PAC, CLS, TBC …)
	StatusText   string `json:"status_text,omitempty"`   // its meaning
	StatusReason string `json:"status_reason,omitempty"` // status change / closure reason (code and meaning)
	ClosedOn     string `json:"closed_on,omitempty"`
	ActivatedAt  string `json:"activated_at,omitempty"`
	Product      string `json:"product,omitempty"`     // product number / client type
	SubType      string `json:"sub_type,omitempty"`    // beneficiary sub type
	Category     string `json:"bo_category,omitempty"` // beneficiary account category
	BSDA         string `json:"bsda,omitempty"`        // BSDA, NBSD or OPTO
	// POAType is the account's POA / DDPI type; POA its links (purpose
	// POALB); POAMappings CDSL's number of POA mappings.
	POAType     string      `json:"poa_type,omitempty"`
	POA         []BOPOALink `json:"poa,omitempty"`
	POAMappings string      `json:"poa_mappings,omitempty"`
	// DDPIBy says which link makes DDPI true (or why none does).
	DDPIBy         string `json:"ddpi_by,omitempty"`
	NoNomination   string `json:"no_nomination,omitempty"` // YES: not opted / opted out; NO: nomination opted
	Nominees       int    `json:"nominees"`
	PANFlag        string `json:"pan_flag,omitempty"`
	PANVerified    bool   `json:"pan_verified"`
	MobileVerified string `json:"mobile_verified,omitempty"` // Yes, No
	EmailVerified  string `json:"email_verified,omitempty"`
	// CMIDs are the clearing members on the BO-UCC links and CM mappings;
	// UCCLinks the BO-UCC links (exchange, UCC, TM code, segment, CM).
	CMIDs    []string `json:"cm_ids,omitempty"`
	UCCLinks []string `json:"ucc_links,omitempty"`
	Freeze   string   `json:"freeze,omitempty"` // FRZ / NFZ with what for (FD, FC, FB)
	File     string   `json:"file"`
	FileType string   `json:"file_type"` // I incremental, F full
	FullReq  string   `json:"full_request,omitempty"`
	At       string   `json:"at"`
}

// BOStatuses are the states of a BO account; FreezeTypes what a freeze
// stops. A freeze needs its type and reason (RaiseDPRequest, the
// DP-ACCOUNTS import).
var (
	BOStatuses  = []string{"Active", "Frozen", "Closed", "Not opened"}
	FreezeTypes = []string{"Debit", "Credit", "Both"}
)

// BOPOALink is one POA or DDPI linked to the account at CDSL.
type BOPOALink struct {
	Type     string `json:"type,omitempty"`      // POA / DDPI type
	ID       string `json:"id,omitempty"`        // POA / DDPI ID
	RegNo    string `json:"reg_no,omitempty"`    // POA registration number (POA ID)
	MasterID string `json:"master_id,omitempty"` // POA master ID
	Nature   string `json:"nature,omitempty"`    // GPA/BPA flag: SET, GNP, MRP, MUF, TEO …
	Holder   string `json:"holder,omitempty"`    // link purpose: FRH, SRH, TRH, ARH
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Operate  string `json:"operate,omitempty"` // POA to operate the account
}

// freezeFor is every freeze on the account together: the DP's own
// (Status Frozen) and CDSL's, as Debit, Credit or Both ("" when none).
func (b BOAccount) freezeFor() string {
	var d, c bool
	add := func(f string) {
		d = d || f == "Debit" || f == "Both"
		c = c || f == "Credit" || f == "Both"
	}
	if b.Status == "Frozen" {
		add(firstOf(b.Freeze, "Both"))
	}
	add(b.CDSLFreeze)
	switch {
	case d && c:
		return "Both"
	case d:
		return "Debit"
	case c:
		return "Credit"
	}
	return ""
}

// debitFrozen says whether nothing may leave the account (DIS, eDIS,
// pledges, early pay-in).
func (b BOAccount) debitFrozen() bool { f := b.freezeFor(); return f == "Debit" || f == "Both" }

// creditFrozen says whether nothing may come in (IPO allotments, pay-outs).
func (b BOAccount) creditFrozen() bool { f := b.freezeFor(); return f == "Credit" || f == "Both" }

// freezeWhy is the reason to show for a frozen account.
func (b BOAccount) freezeWhy() string {
	var why []string
	if b.Status == "Frozen" && b.FreezeWhy != "" {
		why = append(why, b.FreezeWhy)
	}
	if b.CDSLFreeze != "" {
		why = append(why, firstOf(b.CDSLFreezeWhy, "frozen at CDSL"))
	}
	return firstOf(strings.Join(why, "; "), "frozen")
}

// DPTariff is the firm's DP rate card (₹, before GST).
type DPTariff struct {
	AMCYear         money.Amount `json:"amc_year"`         // regular account, per year
	BSDALowLimit    money.Amount `json:"bsda_nil_upto"`    // BSDA: nil AMC up to this holding value
	BSDALimit       money.Amount `json:"bsda_limit"`       // BSDA ceiling on holding value
	BSDAAMCYear     money.Amount `json:"bsda_amc_year"`    // BSDA above the nil slab
	DebitCharge     money.Amount `json:"debit_charge"`     // per debit instruction per ISIN
	DepositoryShare money.Amount `json:"depository_share"` // of the debit charge, payable to the depository
	PledgeCharge    money.Amount `json:"pledge_charge"`    // before PledgeCharges: per pledge creation or closure per ISIN
	// PledgeCharges are the charges on pledge events (PledgeEvents), per
	// ISIN per instruction; an event with no line is free.
	PledgeCharges []DPChargeLine `json:"pledge_charges"`
	DematCharge   money.Amount   `json:"demat_per_cert"` // per certificate
	OffMarketPct  money.Dec      `json:"offmarket_pct"`  // % of value for off-market transfers
	OffMarketMin  money.Amount   `json:"offmarket_min"`
	// CreditCharge is per credit (receipt) per ISIN; usually nil.
	CreditCharge money.Amount `json:"credit_charge"`
	// ChargeFree are accounts, such as the firm's own pool (CM) accounts
	// at each depository, to or from which transfers carry no debit or
	// credit charge: deliveries for trades done through the firm.
	ChargeFree []ChargeFreeAccount `json:"charge_free"`
	// ChargeFreeCMs are the firm's own clearing member codes as the
	// depository reports them on settlement transactions: on-market pay-ins
	// go straight from the client to the clearing corporation, so they are
	// recognised by the clearing member, not by a pool account.
	ChargeFreeCMs []string `json:"charge_free_cms"`
	From          string   `json:"from"`
}

// ChargeFreeAccount is a demat account whose transfers are not charged.
type ChargeFreeAccount struct {
	Depository string `json:"depository"` // CDSL or NSDL
	ID         string `json:"id"`         // CDSL BO ID, or NSDL DP ID + client ID
	Label      string `json:"label,omitempty"`
}

// normDematID strips spaces and dashes and upper-cases a demat account ID.
func normDematID(s string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "/", "").Replace(strings.TrimSpace(s)))
}

var (
	reCDSLBO = regexp.MustCompile(`^[0-9]{16}$`)
	reNSDLBO = regexp.MustCompile(`^IN[0-9]{14}$`)
)

// freeSet is the charge-free accounts by normalised ID.
func (t DPTariff) freeSet() map[string]string {
	m := map[string]string{}
	for _, a := range t.ChargeFree {
		m[normDematID(a.ID)] = firstOf(a.Label, a.Depository+" "+a.ID)
	}
	return m
}

// freeFor says whether a transfer went to or came from a charge-free
// account, or settled trades of the firm's own clearing member, and which.
func (t DPTariff) freeFor(free map[string]string, x DPTxn) (string, bool) {
	for _, c := range x.Counterparty {
		if l, ok := free[normDematID(c)]; ok {
			return l, true
		}
	}
	if cm := strings.ToUpper(strings.TrimSpace(x.ClearingMember)); cm != "" {
		for _, c := range t.ChargeFreeCMs {
			if c == cm {
				return "Clearing member " + c, true
			}
		}
	}
	return "", false
}

// CMSeen is a clearing member code found on DP transactions, to help the
// firm pick its own.
type CMSeen struct {
	Code  string   `json:"code"`
	Txns  int      `json:"txns"`
	Kinds []string `json:"kinds"`
	Free  bool     `json:"free"`
}

// DefaultDPTariff is a typical discount-broker DP tariff.
func DefaultDPTariff() DPTariff {
	return DPTariff{AMCYear: 30000, BSDALowLimit: 40000000, BSDALimit: 100000000, BSDAAMCYear: 10000, DebitCharge: 1350, DepositoryShare: 350,
		PledgeCharges: legacyPledgeCharges(2000), DematCharge: 15000, OffMarketPct: money.MustDec("0.03"), OffMarketMin: 2500, From: "2026-04-01"}
}

// legacyPledgeCharges is the pledge table of a tariff saved with one
// pledge charge, "per pledge creation or closure per ISIN".
func legacyPledgeCharges(a money.Amount) []DPChargeLine {
	out := []DPChargeLine{}
	if a <= 0 {
		return out
	}
	for _, e := range []string{PledgeCreated, MarginPledgeCreated, PledgeClosed, MarginPledgeClosed} {
		out = append(out, DPChargeLine{Code: e, Name: dpChargeCodes[e], DPRate: DPRate{Flat: a}})
	}
	return out
}

// DPTariffNow returns the tariff in force.
func (o *Ops) DPTariffNow(ctx context.Context, tenant string) (DPTariff, error) {
	t, _, err := docs.GetOr(ctx, o.Docs, tenant, cDPTariff, "main", DefaultDPTariff())
	if t.ChargeFree == nil {
		t.ChargeFree = []ChargeFreeAccount{}
	}
	if t.ChargeFreeCMs == nil {
		t.ChargeFreeCMs = []string{}
	}
	if t.PledgeCharges == nil {
		t.PledgeCharges = legacyPledgeCharges(t.PledgeCharge)
	}
	return t, err
}

// SaveDPTariff replaces the tariff.
func (o *Ops) SaveDPTariff(ctx context.Context, tenant, actor string, t DPTariff) (DPTariff, error) {
	if err := o.require(ctx, tenant, actor, "Changing the DP tariff", RoleAdmin, RoleChecker); err != nil {
		return t, err
	}
	if t.DepositoryShare > t.DebitCharge {
		return t, invalid("the depository's share cannot be more than the debit charge")
	}
	if t.BSDALowLimit > t.BSDALimit {
		return t, invalid("the BSDA nil-AMC slab must be within the BSDA limit")
	}
	if t.AMCYear < 0 || t.DebitCharge < 0 || t.PledgeCharge < 0 || t.DematCharge < 0 || t.OffMarketPct.Sign() < 0 {
		return t, invalid("charges cannot be negative")
	}
	if !ledger.ValidDate(t.From) {
		return t, invalid("give the date the tariff applies from")
	}
	if t.CreditCharge < 0 {
		return t, invalid("charges cannot be negative")
	}
	if t.PledgeCharges == nil {
		t.PledgeCharges = legacyPledgeCharges(t.PledgeCharge)
	}
	pl, err := cleanPledgeLines(t.PledgeCharges)
	if err != nil {
		return t, err
	}
	t.PledgeCharges, t.PledgeCharge = pl, 0
	seen := map[string]bool{}
	var free []ChargeFreeAccount
	for _, a := range t.ChargeFree {
		a.Depository, a.ID, a.Label = strings.ToUpper(strings.TrimSpace(a.Depository)), normDematID(a.ID), strings.TrimSpace(a.Label)
		if a.ID == "" {
			continue
		}
		if a.Depository == "" {
			a.Depository = map[bool]string{true: "NSDL", false: "CDSL"}[strings.HasPrefix(a.ID, "IN")]
		}
		switch a.Depository {
		case "CDSL":
			if !reCDSLBO.MatchString(a.ID) {
				return t, invalid("CDSL account %s: a CDSL BO ID is 16 digits", a.ID)
			}
		case "NSDL":
			if !reNSDLBO.MatchString(a.ID) {
				return t, invalid("NSDL account %s: give the DP ID and client ID together, IN followed by 14 digits", a.ID)
			}
		default:
			return t, invalid("depository %q: use CDSL or NSDL", a.Depository)
		}
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		free = append(free, a)
	}
	t.ChargeFree = free
	if t.ChargeFree == nil {
		t.ChargeFree = []ChargeFreeAccount{}
	}
	cms := []string{}
	seenCM := map[string]bool{}
	for _, c := range t.ChargeFreeCMs {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c == "" || seenCM[c] {
			continue
		}
		if len(c) > 16 {
			return t, invalid("clearing member code %q is too long", c)
		}
		seenCM[c] = true
		cms = append(cms, c)
	}
	t.ChargeFreeCMs = cms
	if err := docs.Upsert(ctx, o.Docs, tenant, cDPTariff, "main", t, actor); err != nil {
		return t, err
	}
	o.log(ctx, tenant, actor, "dp.tariff", "main", map[string]any{"from": t.From})
	// the firm's tariff, its pledge charges included, is filled in
	o.dpFilledIn(ctx, tenant, actor, pkg.FillDPFirmTariff, "")
	if x, _, err := docs.Get[DPFillIn](ctx, o.Docs, tenant, cDPFillIn, pkg.FillDPPledgeCharges); err == nil && x.Done == "" && len(x.Plans) == 0 {
		o.dpFilledIn(ctx, tenant, actor, pkg.FillDPPledgeCharges, "")
	}
	return t, nil
}

// bo returns the DP's record of a client's primary demat account (for what
// is about the client as a whole), made from the client master when there
// is none; boFor (demats.go) is any one of the client's accounts.
func (o *Ops) bo(ctx context.Context, tenant string, c Client) BOAccount {
	return o.boFor(ctx, tenant, c, "")
}

// BORow is a BO account with its holdings.
type BORow struct {
	BOAccount
	Name    string       `json:"name"`
	Holders string       `json:"holders"`
	Primary bool         `json:"primary"`
	ISINs   int          `json:"isins"`
	Value   money.Dec    `json:"value"`
	Pledged money.Dec    `json:"pledged_qty"`
	BSDAOK  bool         `json:"bsda_eligible"`
	AMC     money.Amount `json:"amc_year"`
	Ledger  money.Amount `json:"ledger"`
	// Plan is the account's DP tariff plan on the date ("": the firm's
	// tariff).
	Plan string `json:"plan,omitempty"`
}

// DPView is the DP desk.
type DPView struct {
	Date   string   `json:"date"`
	Rows   []BORow  `json:"rows"`
	Tariff DPTariff `json:"tariff"`
	Plans  []DPPlan `json:"plans"`
	// PledgeEvents are the rows of the pledge charges tables.
	PledgeEvents []PledgeEvent `json:"pledge_events"`
	// FillIn is what the old system's backup did not say and the firm is
	// to fill in before DP billing.
	FillIn  []DPFillIn     `json:"fill_in"`
	Counts  map[string]int `json:"counts"`
	DIS     []DIS          `json:"dis"`
	Books   []DISBook      `json:"books"`
	Bills   []DPBill       `json:"bills"`
	Reqs    []DPRequest    `json:"requests"`
	Reasons []string       `json:"reasons"`
	// CMs are the clearing member codes on loaded DP transactions.
	CMs []CMSeen `json:"clearing_members"`
}

func amcFor(t DPTariff, bsda bool, value money.Dec) money.Amount {
	if !bsda {
		return t.AMCYear
	}
	if value.Cmp(t.BSDALowLimit.Dec()) <= 0 {
		return 0
	}
	return t.BSDAAMCYear
}

// DP builds the DP desk for a date.
func (o *Ops) DP(ctx context.Context, tenant, date string) (DPView, error) {
	v := DPView{Date: date, Rows: []BORow{}, Counts: map[string]int{}, Reasons: OffMarketReasons}
	t, err := o.DPTariffNow(ctx, tenant)
	if err != nil {
		return v, err
	}
	v.Tariff = t
	v.PledgeEvents = pledgeEventList()
	v.FillIn = o.DPFillIns(ctx, tenant)
	if v.Plans, _ = o.DPPlans(ctx, tenant); v.Plans == nil {
		v.Plans = []DPPlan{}
	}
	plans := map[string]DPPlan{}
	for _, p := range v.Plans {
		plans[p.Code] = p
	}
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return v, err
	}
	hs, _ := o.Holdings(ctx, tenant, "")
	_, look, _ := o.Market(ctx, tenant, date)
	type agg struct {
		n   int
		val money.Dec
		pl  money.Dec
	}
	// By client and account (holdingIDAt's account part).
	hv := map[string]*agg{}
	for _, h := range hs {
		k := h.UCC + "|" + h.BOID
		a := hv[k]
		if a == nil {
			a = &agg{}
			hv[k] = a
		}
		if h.Qty.Sign() > 0 {
			a.n++
		}
		a.pl = a.pl.Add(h.Pledged)
		if i, ok := look.ByISIN(h.ISIN); ok {
			a.val = a.val.Add(h.Value(i.LTP))
		}
	}
	funds, _ := o.clientFunds(ctx, tenant, date, "")
	idx := o.boIndex(ctx, tenant)
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		// Every one of the client's accounts with the DP (a client opening
		// a trading account keeps them meanwhile); an active client with
		// none on record shows its demat account from the client master.
		bs := idx.of(c)
		if c.Status != "Active" && len(bs) == 0 {
			continue
		}
		if len(bs) == 0 {
			if c.Demat.ID == "" {
				continue
			}
			bs = []BOAccount{o.bo(ctx, tenant, c)}
		}
		open := 0
		for _, d := range demats(c) {
			if d.Status != DematClosed {
				open++
			}
		}
		for _, b := range bs {
			acct := holdingAcct(c, b.BOID)
			r := BORow{BOAccount: b, Name: c.Name, Holders: dematHolderNames(c.Name, b.JointHolders), Primary: acct == "", Ledger: funds[u]}
			if a := hv[u+"|"+acct]; a != nil {
				r.ISINs, r.Value, r.Pledged = a.n, a.val, a.pl
			}
			// BSDA: one demat account per PAN, holding value within the limit.
			r.BSDAOK = r.Value.Cmp(t.BSDALimit.Dec()) <= 0 && open <= 1 && len(b.JointHolders) == 0
			r.AMC = amcFor(t, b.BSDA, r.Value)
			if r.Plan = b.planAt(date); r.Plan != "" {
				if pv := plans[r.Plan].at(date); pv != nil && !b.BSDA {
					r.AMC = pv.AMCYear
					if c.Type == "Corporate" {
						r.AMC += pv.CorporateAMCYear
					}
				}
				v.Counts["On a tariff plan"]++
			}
			v.Counts[b.Status]++
			if b.BSDA {
				v.Counts["BSDA"]++
			}
			if r.BSDAOK && !b.BSDA && b.Status == "Active" {
				v.Counts["Could be BSDA"]++
			}
			if b.BSDA && !r.BSDAOK {
				v.Counts["BSDA over limit"]++
			}
			v.Rows = append(v.Rows, r)
		}
	}
	v.DIS, _ = docs.List[DIS](ctx, o.Docs, tenant, cDIS, "")
	sort.Slice(v.DIS, func(i, j int) bool { return v.DIS[i].ID > v.DIS[j].ID })
	v.Books, _ = docs.List[DISBook](ctx, o.Docs, tenant, cDISBook, "")
	v.CMs = []CMSeen{}
	if ts, err := docs.List[DPTxn](ctx, o.Docs, tenant, cDPTxn, ""); err == nil {
		by := map[string]*CMSeen{}
		kinds := map[string]map[string]bool{}
		for _, x := range ts {
			cm := strings.ToUpper(strings.TrimSpace(x.ClearingMember))
			if cm == "" {
				continue
			}
			if by[cm] == nil {
				by[cm], kinds[cm] = &CMSeen{Code: cm}, map[string]bool{}
			}
			by[cm].Txns++
			kinds[cm][x.Kind] = true
		}
		for _, c := range sortedKeys(by) {
			x := by[c]
			x.Kinds = sortedKeys(kinds[c])
			for _, f := range v.Tariff.ChargeFreeCMs {
				x.Free = x.Free || f == c
			}
			v.CMs = append(v.CMs, *x)
		}
	}
	v.Bills, _ = docs.List[DPBill](ctx, o.Docs, tenant, cDPBill, "")
	sort.Slice(v.Bills, func(i, j int) bool { return v.Bills[i].ID > v.Bills[j].ID })
	v.Reqs, _ = docs.List[DPRequest](ctx, o.Docs, tenant, cDPReq, "")
	sort.Slice(v.Reqs, func(i, j int) bool { return v.Reqs[i].ID > v.Reqs[j].ID })
	for _, d := range v.DIS {
		if d.Status == "Entered" {
			v.Counts["DIS to verify"]++
		}
		if d.Status == "Verified" {
			v.Counts["DIS to upload"]++
		}
	}
	for _, r := range v.Reqs {
		if r.Status == "Pending" || r.Status == "Sent to RTA" {
			v.Counts["Requests open"]++
		}
	}
	for _, x := range []*[]DIS{&v.DIS} {
		if *x == nil {
			*x = []DIS{}
		}
	}
	if v.Books == nil {
		v.Books = []DISBook{}
	}
	if v.Bills == nil {
		v.Bills = []DPBill{}
	}
	if v.Reqs == nil {
		v.Reqs = []DPRequest{}
	}
	return v, nil
}

// SetBSDA converts an account to or from a Basic Services Demat Account.
func (o *Ops) SetBSDA(ctx context.Context, tenant, actor, ucc string, on bool, date string) (BOAccount, error) {
	return o.SetBSDAFor(ctx, tenant, actor, ucc, "", on, date)
}

// SetBSDAFor converts one of the client's accounts (blank: the primary).
func (o *Ops) SetBSDAFor(ctx context.Context, tenant, actor, ucc, boid string, on bool, date string) (BOAccount, error) {
	if err := o.require(ctx, tenant, actor, "Changing the account type", RoleChecker); err != nil {
		return BOAccount{}, err
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return BOAccount{}, err
	}
	b := o.boFor(ctx, tenant, c, boid)
	if on {
		v, err := o.DP(ctx, tenant, date)
		if err != nil {
			return b, err
		}
		for _, r := range v.Rows {
			if r.UCC == c.UCC && normDematID(r.BOID) == normDematID(b.BOID) && !r.BSDAOK {
				if len(activeDemats(c)) > 1 {
					return b, invalid("a Basic Services Demat Account must be the client's only demat account; %s has %d", c.UCC, len(activeDemats(c)))
				}
				return b, invalid("holdings of ₹%s are above the BSDA limit of ₹%s", formatINR(r.Value), formatINR(v.Tariff.BSDALimit.Dec()))
			}
		}
	}
	b.BSDA = on
	if err := o.putBO(ctx, tenant, actor, b); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "dp.bsda", b.UCC, map[string]any{"bsda": on})
	return b, nil
}

// ---- delivery instruction slips ----

// OffMarketReasons are the depositories' reason codes for off-market transfers.
var OffMarketReasons = []string{"Gift", "Sale (off-market)", "Transfer to own account", "Transmission", "Donation", "Implementation of government order", "Transfer to family member", "Margin to clearing member", "Others"}

// DISBook is a book of slips issued to a BO.
type DISBook struct {
	ID  string `json:"id"`
	UCC string `json:"ucc"`
	// BOID is the demat account the book was issued for (empty: issued to
	// the client before Ledgerline kept several accounts; any of them).
	BOID   string  `json:"bo_id,omitempty"`
	From   int64   `json:"from"`
	To     int64   `json:"to"`
	Issued string  `json:"issued"`
	Used   []int64 `json:"used"`
	Lost   []int64 `json:"lost"`
}

// DISLine is one ISIN on a slip.
type DISLine struct {
	ISIN   string    `json:"isin"`
	Symbol string    `json:"symbol"`
	Qty    money.Dec `json:"qty"` // exact: units in demat may be fractional
	Value  money.Dec `json:"value"`
}

// DIS is a delivery instruction.
type DIS struct {
	ID   string `json:"id"`
	UCC  string `json:"ucc"`
	Name string `json:"name"`
	BOID string `json:"bo_id"`
	Slip int64  `json:"slip"`
	Kind string `json:"kind"` // Off-market, Inter-depository, On-market
	ToBO string `json:"to_bo"`
	// Settlement and Exchange are an on-market delivery's (to the clearing
	// member's account for a settlement).
	Settlement    string       `json:"settlement,omitempty"`
	Exchange      string       `json:"exchange,omitempty"`
	Reason        string       `json:"reason"`
	Consideration money.Amount `json:"consideration"`
	ExecDate      string       `json:"exec_date"`
	Received      string       `json:"received"`
	Lines         []DISLine    `json:"lines"`
	Value         money.Dec    `json:"value"`
	Confirmed     string       `json:"client_confirmation,omitempty"` // for high-value slips
	Status        string       `json:"status"`                        // Entered, Verified, Rejected, Uploaded, Executed, Failed
	Maker         string       `json:"maker"`
	Checker       string       `json:"checker,omitempty"`
	Note          string       `json:"note,omitempty"`
	Batch         string       `json:"batch,omitempty"`
}

// HighValueDIS needs the client's confirmation on record (₹5 lakh).
var HighValueDIS = money.DecInt(500000)

// IssueDISBook issues a book of n slips for a client's primary account.
func (o *Ops) IssueDISBook(ctx context.Context, tenant, actor, ucc string, n int64) (DISBook, error) {
	return o.IssueDISBookFor(ctx, tenant, actor, ucc, "", n)
}

// IssueDISBookFor issues a book of n slips for one of the client's
// accounts (blank: the primary).
func (o *Ops) IssueDISBookFor(ctx context.Context, tenant, actor, ucc, boid string, n int64) (DISBook, error) {
	if err := o.require(ctx, tenant, actor, "Issuing DIS books", RoleMaker, RoleChecker); err != nil {
		return DISBook{}, err
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return DISBook{}, err
	}
	bo := o.boFor(ctx, tenant, c, boid)
	if bo.Status != "Active" {
		return DISBook{}, fmt.Errorf("%w: the demat account is %s", ErrState, strings.ToLower(bo.Status))
	}
	if n < 1 || n > 100 {
		return DISBook{}, invalid("a book has 1 to 100 slips")
	}
	books, _ := docs.List[DISBook](ctx, o.Docs, tenant, cDISBook, "")
	next := int64(1000001)
	for _, b := range books {
		if b.To >= next {
			next = b.To + 1
		}
	}
	seq, err := o.next(ctx, tenant, "disbook", actor)
	if err != nil {
		return DISBook{}, err
	}
	b := DISBook{ID: fmt.Sprintf("DB-%05d", seq), UCC: c.UCC, BOID: normDematID(bo.BOID), From: next, To: next + n - 1, Issued: o.now().Format("2006-01-02"), Used: []int64{}, Lost: []int64{}}
	if err := docs.Upsert(ctx, o.Docs, tenant, cDISBook, b.ID, b, actor); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "dis.book_issued", b.ID, map[string]any{"ucc": c.UCC, "from": b.From, "to": b.To})
	return b, nil
}

func (o *Ops) slipBook(ctx context.Context, tenant, ucc, boid string, slip int64) (DISBook, bool) {
	books, _ := docs.List[DISBook](ctx, o.Docs, tenant, cDISBook, "")
	for _, b := range books {
		if b.UCC == ucc && (b.BOID == "" || b.BOID == normDematID(boid)) && slip >= b.From && slip <= b.To {
			return b, true
		}
	}
	return DISBook{}, false
}

// freeBalance is what can be moved from the client's primary account.
func (o *Ops) freeBalance(ctx context.Context, tenant, ucc, isin string) money.Dec {
	c, _ := o.Client(ctx, tenant, ucc)
	return o.freeBalanceIn(ctx, tenant, c, "", isin)
}

// freeBalanceIn is what can be moved from one of the client's accounts
// (blank: the primary): holding less pledged and less instructions from
// that account not yet executed.
func (o *Ops) freeBalanceIn(ctx context.Context, tenant string, c Client, boid, isin string) money.Dec {
	acct := holdingAcct(c, boid)
	h, _, err := docs.Get[Holding](ctx, o.Docs, tenant, cHolding, holdingIDAt(c.UCC, isin, acct))
	if err != nil {
		return money.Dec{}
	}
	free := h.Free()
	// A part of the ISIN frozen at CDSL cannot move (cdslfreeze.go).
	bo := o.boFor(ctx, tenant, c, boid)
	free = free.Sub(o.cdslFrozenQty(ctx, tenant, bo.BOID, isin, h.Qty))
	ds, _ := docs.List[DIS](ctx, o.Docs, tenant, cDIS, "")
	for _, d := range ds {
		if d.UCC != c.UCC || holdingAcct(c, d.BOID) != acct || (d.Status != "Entered" && d.Status != "Verified" && d.Status != "Uploaded") {
			continue
		}
		for _, l := range d.Lines {
			if l.ISIN == isin {
				free = free.Sub(l.Qty)
			}
		}
	}
	return free
}

// EnterDIS records a slip received from a client. The slip debits the
// demat account it names (BOID; blank: the client's primary account).
func (o *Ops) EnterDIS(ctx context.Context, tenant, actor string, d DIS) (DIS, error) {
	if err := o.require(ctx, tenant, actor, "Entering delivery instructions", RoleMaker, RoleChecker, RoleBranch); err != nil {
		return d, err
	}
	c, err := o.Client(ctx, tenant, d.UCC)
	if err != nil {
		return d, invalid("client %s not found", d.UCC)
	}
	if d.BOID = normDematID(d.BOID); d.BOID != "" {
		if _, ok := dematOf(c, d.BOID); !ok {
			return d, invalid("%s is not a demat account of %s", d.BOID, c.UCC)
		}
	}
	b := o.boFor(ctx, tenant, c, d.BOID)
	switch {
	case b.BOID == "":
		return d, fmt.Errorf("%w: %s has no demat account", ErrState, c.UCC)
	case b.Status == "Closed":
		return d, fmt.Errorf("%w: the demat account is closed", ErrState)
	case b.debitFrozen():
		return d, fmt.Errorf("%w: the demat account is frozen for debits (%s)", ErrState, b.freezeWhy())
	}
	book, ok := o.slipBook(ctx, tenant, c.UCC, b.BOID, d.Slip)
	if !ok {
		return d, invalid("slip %d was not issued to %s for demat account %s", d.Slip, c.UCC, b.BOID)
	}
	if contains(i64s(book.Used), fmt.Sprint(d.Slip)) {
		return d, invalid("slip %d has already been used", d.Slip)
	}
	if contains(i64s(book.Lost), fmt.Sprint(d.Slip)) {
		return d, invalid("slip %d was reported lost and cannot be accepted", d.Slip)
	}
	if !contains([]string{"Off-market", "Inter-depository", "On-market"}, d.Kind) {
		return d, invalid("kind must be Off-market, Inter-depository or On-market")
	}
	d.ToBO = strings.ToUpper(strings.TrimSpace(d.ToBO))
	if d.Kind == "On-market" {
		// A slip delivering to the clearing member for a settlement (clients
		// without DDPI or eDIS); no transfer reason.
		d.Settlement, d.Exchange = strings.TrimSpace(d.Settlement), strings.ToUpper(strings.TrimSpace(d.Exchange))
		if d.ToBO == "" || d.ToBO == b.BOID || d.Settlement == "" || !contains([]string{"NSE", "BSE"}, d.Exchange) {
			return d, invalid("an on-market slip needs the clearing member's account, the exchange (NSE or BSE) and the settlement number")
		}
		d.Reason = "On-market delivery"
	} else {
		d.Settlement, d.Exchange = "", ""
	}
	if d.Kind == "Off-market" && (len(d.ToBO) != 16 || d.ToBO == b.BOID) {
		return d, invalid("give the 16-digit target BO ID of another account")
	}
	if d.Kind == "Inter-depository" && len(d.ToBO) != 16 && !strings.HasPrefix(d.ToBO, "IN") {
		return d, invalid("give the target DP ID and client ID at the other depository")
	}
	if d.Kind != "On-market" && !contains(OffMarketReasons, d.Reason) {
		return d, invalid("choose the reason for the transfer")
	}
	if d.Reason == "Sale (off-market)" && d.Consideration <= 0 {
		return d, invalid("an off-market sale needs the consideration")
	}
	today := o.now().Format("2006-01-02")
	if d.Received == "" {
		d.Received = today
	}
	if !ledger.ValidDate(d.ExecDate) || d.ExecDate < d.Received {
		return d, invalid("execution date must be on or after the date the slip was received")
	}
	if len(d.Lines) == 0 || len(d.Lines) > 5 {
		return d, invalid("a slip carries 1 to 5 ISINs")
	}
	_, look, _ := o.Market(ctx, tenant, today)
	d.Value = money.Dec{}
	for i := range d.Lines {
		l := &d.Lines[i]
		l.ISIN = strings.ToUpper(strings.TrimSpace(l.ISIN))
		if l.Qty.Sign() <= 0 {
			return d, invalid("quantity must be above zero")
		}
		if _, err := parseQty(l.Qty.String()); err != nil {
			return d, invalid("quantity %s", err)
		}
		if fz, ok := o.cdslISINFrozen(ctx, tenant, b.BOID, l.ISIN, false); ok && fz.Full() {
			return d, fmt.Errorf("%w: %s is frozen for debits at CDSL in this account (%s)", ErrState, l.ISIN, fz.Why())
		}
		if free := o.freeBalanceIn(ctx, tenant, c, b.BOID, l.ISIN); l.Qty.Cmp(free) > 0 {
			return d, invalid("only %s free in %s in demat account %s (after pledges and pending instructions)", fmtQty(money.Max(free, money.Dec{})), l.ISIN, b.BOID)
		}
		if h, _, err := docs.Get[Holding](ctx, o.Docs, tenant, cHolding, holdingIDAt(c.UCC, l.ISIN, holdingAcct(c, b.BOID))); err == nil {
			l.Symbol = h.Symbol
		}
		if i, ok := look.ByISIN(l.ISIN); ok {
			l.Value = i.LTP.Mul(l.Qty)
		}
		d.Value = d.Value.Add(l.Value)
	}
	n, err := o.next(ctx, tenant, "dis", actor)
	if err != nil {
		return d, err
	}
	d.ID, d.Name, d.BOID, d.Status, d.Maker = fmt.Sprintf("DIS-%06d", n), c.Name, b.BOID, "Entered", actor
	if _, err := docs.Put(ctx, o.Docs, tenant, cDIS, d.ID, d, 0, actor); err != nil {
		return d, err
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cDISBook, book.ID, actor, func(x *DISBook) error { x.Used = append(x.Used, d.Slip); return nil })
	o.log(ctx, tenant, actor, "dis.entered", d.ID, map[string]any{"ucc": c.UCC, "slip": d.Slip, "value": d.Value.String()})
	return d, nil
}

func i64s(xs []int64) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = fmt.Sprint(x)
	}
	return out
}

// VerifyDIS approves or rejects an entered slip. High-value slips need the
// client's confirmation (how and when it was taken) on record.
func (o *Ops) VerifyDIS(ctx context.Context, tenant, actor, id string, approve bool, note, confirmation string) (DIS, error) {
	if err := o.require(ctx, tenant, actor, "Verifying delivery instructions", RoleChecker); err != nil {
		return DIS{}, err
	}
	d, _, err := docs.Get[DIS](ctx, o.Docs, tenant, cDIS, id)
	if err != nil {
		return d, err
	}
	if d.Status != "Entered" {
		return d, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(d.Status))
	}
	if d.Maker == actor {
		return d, fmt.Errorf("%w: the person who entered a slip cannot verify it", ErrForbidden)
	}
	if !approve && strings.TrimSpace(note) == "" {
		return d, invalid("give the reason for rejecting (signature mismatch, overwriting, …)")
	}
	if approve && d.Value.Cmp(HighValueDIS) > 0 && strings.TrimSpace(confirmation) == "" && d.Confirmed == "" {
		return d, invalid("slips above ₹5 lakh need the client's confirmation on record (call reference or email)")
	}
	d, err = docs.Update(ctx, o.Docs, tenant, cDIS, id, actor, func(x *DIS) error {
		x.Checker, x.Note = actor, note
		if confirmation != "" {
			x.Confirmed = confirmation
		}
		x.Status = map[bool]string{true: "Verified", false: "Rejected"}[approve]
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "dis."+strings.ToLower(d.Status), id, map[string]any{"note": note})
	}
	return d, err
}

// DISBatch builds the upload file of verified slips for an execution date
// and marks them uploaded.
func (o *Ops) DISBatch(ctx context.Context, tenant, actor, date string) ([]byte, int, error) {
	if err := o.require(ctx, tenant, actor, "Uploading delivery instructions", RoleMaker, RoleChecker); err != nil {
		return nil, 0, err
	}
	ds, err := docs.List[DIS](ctx, o.Docs, tenant, cDIS, "")
	if err != nil {
		return nil, 0, err
	}
	batch := "DISB-" + strings.ReplaceAll(date, "-", "") + "-" + o.now().Format("150405")
	rows := [][]string{{"BatchID", "InstructionID", "SlipNo", "SourceBOID", "TargetBOID", "Kind", "ExecutionDate", "ISIN", "Quantity", "Reason", "Consideration"}}
	n := 0
	for _, d := range ds {
		if d.Status != "Verified" || d.ExecDate > date {
			continue
		}
		for _, l := range d.Lines {
			rows = append(rows, []string{batch, d.ID, fmt.Sprint(d.Slip), d.BOID, d.ToBO, d.Kind, d.ExecDate, l.ISIN, fmtQty(l.Qty), d.Reason, d.Consideration.String()})
		}
		if _, err := docs.Update(ctx, o.Docs, tenant, cDIS, d.ID, actor, func(x *DIS) error { x.Status, x.Batch = "Uploaded", batch; return nil }); err != nil {
			return nil, n, err
		}
		n++
	}
	if n == 0 {
		return nil, 0, invalid("no verified instructions to upload for %s", date)
	}
	o.log(ctx, tenant, actor, "dis.uploaded", batch, map[string]any{"instructions": n})
	return csvBytesOf(rows), n, nil
}

// ExecuteDIS records the DP system's response for an uploaded slip: the
// holdings move out, and the transfer is recorded for billing.
func (o *Ops) ExecuteDIS(ctx context.Context, tenant, actor, id string, ok bool, reason, date string) (DIS, error) {
	if err := o.require(ctx, tenant, actor, "Recording DP responses", RoleMaker, RoleChecker); err != nil {
		return DIS{}, err
	}
	d, _, err := docs.Get[DIS](ctx, o.Docs, tenant, cDIS, id)
	if err != nil {
		return d, err
	}
	if d.Status != "Uploaded" {
		return d, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(d.Status))
	}
	if date == "" {
		date = d.ExecDate
	}
	if ok {
		// A freeze CDSL reported after the slip was verified stops it here.
		c, _ := o.Client(ctx, tenant, d.UCC)
		if c.UCC != "" {
			if b := o.boFor(ctx, tenant, c, d.BOID); b.debitFrozen() {
				return d, fmt.Errorf("%w: the demat account is frozen for debits (%s); record the slip as failed", ErrState, b.freezeWhy())
			}
			for _, l := range d.Lines {
				if fz, frozen := o.cdslISINFrozen(ctx, tenant, d.BOID, l.ISIN, false); frozen && fz.Full() {
					return d, fmt.Errorf("%w: %s is frozen for debits at CDSL (%s); record the slip as failed", ErrState, l.ISIN, fz.Why())
				}
			}
		}
		// Out of the account the slip names only.
		acct := holdingAcct(c, d.BOID)
		for _, l := range d.Lines {
			if _, err := docs.Update(ctx, o.Docs, tenant, cHolding, holdingIDAt(d.UCC, l.ISIN, acct), actor, func(h *Holding) error {
				if h.Free().Cmp(l.Qty) < 0 {
					return fmt.Errorf("%w: %s no longer has %s free in %s%s", ErrState, d.UCC, fmtQty(l.Qty), l.ISIN, acctNote(acct))
				}
				h.Qty = h.Qty.Sub(l.Qty)
				return nil
			}); err != nil {
				return d, err
			}
			if err := o.dpTxn(ctx, tenant, actor, DPTxn{Date: date, UCC: d.UCC, BOID: d.BOID, ISIN: l.ISIN, Symbol: l.Symbol, Qty: l.Qty.Neg(), Kind: d.Kind + " transfer", Ref: d.ID, Value: l.Value, Billable: true, Counterparty: []string{d.ToBO}}); err != nil {
				return d, err
			}
		}
	} else if strings.TrimSpace(reason) == "" {
		return d, invalid("give the rejection reason from the DP system")
	}
	d, err = docs.Update(ctx, o.Docs, tenant, cDIS, id, actor, func(x *DIS) error {
		x.Status = map[bool]string{true: "Executed", false: "Failed"}[ok]
		if !ok {
			x.Note = reason
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "dis."+strings.ToLower(d.Status), id, map[string]any{"reason": reason})
	}
	return d, err
}

// ---- DP transactions and billing ----

// DPTxn is a movement in a BO account.
type DPTxn struct {
	ID   string `json:"id"`
	Date string `json:"date"`
	UCC  string `json:"ucc"`
	// BOID is the account moved in (empty: the client's primary account,
	// as before several accounts were kept).
	BOID     string    `json:"bo_id,omitempty"`
	ISIN     string    `json:"isin"`
	Symbol   string    `json:"symbol"`
	Qty      money.Dec `json:"qty"` // + credit, − debit; exact
	Kind     string    `json:"kind"`
	Ref      string    `json:"ref"`
	Value    money.Dec `json:"value"`
	Billable bool      `json:"billable"`
	Certs    int       `json:"certificates,omitempty"`
	// Counterparty lists the other side's account IDs as the depository
	// reports them (BO ID, DP ID + client ID, pool or early pay-in account).
	Counterparty []string `json:"counterparty,omitempty"`
	// ClearingMember is the clearing member of an on-market settlement
	// transaction (pay-in or pay-out), as the depository reports it.
	ClearingMember string `json:"clearing_member,omitempty"`
	// Pledge is a pledge event (PledgeEvents): a pledge created, closed or
	// invoked, billed on the tariff's pledge charges. Qty is the quantity
	// pledged, closed or invoked, not a move of the free balance; Value the
	// pledge value the depository reports.
	Pledge string `json:"pledge,omitempty"`
}

func (o *Ops) dpTxn(ctx context.Context, tenant, actor string, t DPTxn) error {
	n, err := o.next(ctx, tenant, "dptxn", actor)
	if err != nil {
		return err
	}
	t.ID = fmt.Sprintf("DPT-%07d", n)
	return docs.Upsert(ctx, o.Docs, tenant, cDPTxn, t.UCC+"|"+t.ID, t, actor)
}

// DPTransactions lists a client's DP transactions.
func (o *Ops) DPTransactions(ctx context.Context, tenant, ucc string) ([]DPTxn, error) {
	ts, err := docs.List[DPTxn](ctx, o.Docs, tenant, cDPTxn, strings.ToUpper(ucc)+"|")
	if ts == nil {
		ts = []DPTxn{}
	}
	return ts, err
}

// DPBillLine is one charge on a DP bill.
type DPBillLine struct {
	// BOID is the demat account the charge is for: a client with several
	// accounts gets one bill with each account's lines.
	BOID   string       `json:"bo_id,omitempty"`
	What   string       `json:"what"`
	Count  int64        `json:"count"`
	Amount money.Amount `json:"amount"`
	Share  money.Amount `json:"depository_share"`
}

// DPBill is a client's DP bill for a month.
type DPBill struct {
	ID      string       `json:"id"`
	Month   string       `json:"month"`
	UCC     string       `json:"ucc"`
	Name    string       `json:"name"`
	Lines   []DPBillLine `json:"lines"`
	Taxable money.Amount `json:"taxable"`
	GST     money.Amount `json:"gst"`
	Total   money.Amount `json:"total"`
	Invoice string       `json:"invoice"`
	Entry   string       `json:"entry"`
	// Free counts transfers to or from charge-free accounts, not charged.
	Free int64 `json:"free,omitempty"`
}

// RunDPBilling bills every active BO for a month (YYYY-MM): a twelfth of
// the annual maintenance, debit and credit instructions, pledges and demat
// requests. Transfers to or from the tariff's charge-free accounts (the
// firm's own pool accounts) carry no debit or credit charge.
// Bills are posted to the trading ledger with GST and an invoice. A month
// is billed once.
//
// A client with several accounts with the DP gets one bill a month, with
// each account's charges on lines of their own (DPBillLine.BOID): the bill
// is the client's (it is posted to the client's ledger as one entry with
// one tax invoice), as it was with one account, and its key (month|UCC)
// still makes a month billable once.
func (o *Ops) RunDPBilling(ctx context.Context, tenant, actor, month string) ([]DPBill, error) {
	if err := o.require(ctx, tenant, actor, "DP billing", RoleChecker); err != nil {
		return nil, err
	}
	if err := o.dpBillingHeld(ctx, tenant); err != nil {
		return nil, err
	}
	if !ledger.ValidDate(month + "-01") {
		return nil, invalid("month must be YYYY-MM")
	}
	end := monthEnd(month)
	t, err := o.DPTariffNow(ctx, tenant)
	if err != nil {
		return nil, err
	}
	v, err := o.DP(ctx, tenant, end)
	if err != nil {
		return nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	free := t.freeSet()
	cs, _ := o.ClientMap(ctx, tenant)
	plans := o.dpPlanMap(ctx, tenant)
	// The billable accounts, client by client.
	rows := map[string][]BORow{}
	var order []string
	for _, r := range v.Rows {
		if r.Status == "Closed" || r.Status == "Not opened" {
			continue
		}
		if rows[r.UCC] == nil {
			order = append(order, r.UCC)
		}
		rows[r.UCC] = append(rows[r.UCC], r)
	}
	var out []DPBill
	for _, u := range order {
		rs := rows[u]
		if _, _, err := docs.Get[DPBill](ctx, o.Docs, tenant, cDPBill, month+"|"+u); err == nil {
			continue
		}
		c := cs[u]
		b := DPBill{ID: month + "|" + u, Month: month, UCC: u, Name: rs[0].Name}
		ts, _ := o.DPTransactions(ctx, tenant, u)
		several := len(rs) > 1
		paidUpto := map[string]string{} // BO ID: new AMC paid-up date
		for _, r := range rs {
			acct := holdingAcct(c, r.BOID)
			label := func(what string) string {
				if several {
					return what + " · " + cdslMaskBO(r.BOID)
				}
				return what
			}
			line := func(l DPBillLine) {
				if several {
					l.BOID = normDematID(r.BOID)
				}
				l.What = label(l.What)
				b.Lines = append(b.Lines, l)
			}
			// On a tariff plan: the plan's charges (dpplan.go).
			if p, ok := plans[r.planAt(end)]; ok && p.at(end) != nil {
				mine := func(x DPTxn) bool { return holdingAcct(c, x.BOID) == acct }
				if np := dpPlanBill(p, *p.at(end), t, free, r, c.Type == "Corporate", month, end, ts, mine, line, &b.Free); np != "" {
					paidUpto[r.BOID] = np
				}
				continue
			}
			if amc := r.AMC / 12; amc > 0 {
				line(DPBillLine{What: map[bool]string{true: "Annual maintenance (BSDA), one month", false: "Annual maintenance, one month"}[r.BSDA], Count: 1, Amount: amc})
			}
			var debits, credits, offmkt int64
			var omCharge money.Amount
			var certs int64
			for _, x := range ts {
				if x.Date < month+"-01" || x.Date > end || !x.Billable || holdingAcct(c, x.BOID) != acct {
					continue
				}
				if _, ok := t.freeFor(free, x); ok && x.Certs == 0 {
					b.Free++
					continue
				}
				if x.Qty.Sign() > 0 && x.Certs == 0 && !strings.HasPrefix(x.Kind, "IPO") {
					credits++
				}
				if x.Qty.Sign() < 0 {
					debits++
					if strings.HasPrefix(x.Kind, "Off-market") {
						offmkt++
						c := x.Value.Pct(t.OffMarketPct).Round(money.UnitPaisa)
						if c < t.OffMarketMin {
							c = t.OffMarketMin
						}
						omCharge += c
					}
				}
				certs += int64(x.Certs)
			}
			if debits > 0 {
				line(DPBillLine{What: "Debit instructions (per ISIN)", Count: debits, Amount: t.DebitCharge * money.Amount(debits), Share: t.DepositoryShare * money.Amount(debits)})
			}
			if credits > 0 && t.CreditCharge > 0 {
				line(DPBillLine{What: "Credit instructions (per ISIN)", Count: credits, Amount: t.CreditCharge * money.Amount(credits)})
			}
			if offmkt > 0 {
				line(DPBillLine{What: "Off-market transfer charge", Count: offmkt, Amount: omCharge})
			}
			if certs > 0 {
				line(DPBillLine{What: "Dematerialisation (per certificate)", Count: certs, Amount: t.DematCharge * money.Amount(certs)})
			}
			pledgeCharges(t.PledgeCharges, ts, func(x DPTxn) bool {
				return x.Date >= month+"-01" && x.Date <= end && holdingAcct(c, x.BOID) == acct
			}, line)
		}
		if len(b.Lines) == 0 {
			continue
		}
		var share money.Amount
		for _, l := range b.Lines {
			b.Taxable += l.Amount
			share += l.Share
		}
		b.GST = b.Taxable.Dec().Pct(set.gstRate()).Round(money.UnitPaisa)
		b.Total = b.Taxable + b.GST
		desc := "DP charges for " + month
		ps := []ledger.Posting{{Account: ledger.ClientAccount(u), Debit: b.Total, Memo: desc}}
		for _, p := range []ledger.Posting{{Account: IncomeDP, Credit: b.Taxable - share}, {Account: PayableDepository, Credit: share}, {Account: ledger.PayableGST, Credit: b.GST}} {
			if p.Credit > 0 {
				ps = append(ps, p)
			}
		}
		e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "DPBILL:" + b.ID, ValueDate: end, CreatedBy: actor, Narration: desc,
			Source: ledger.SourceRef{Kind: "DP_BILL", ID: b.ID}, Postings: ps})
		if err != nil {
			return out, err
		}
		inv, err := o.issueInvoice(ctx, tenant, actor, Invoice{Date: end, UCC: u, Name: b.Name, Kind: "Depository", Description: desc, SAC: "997152",
			Taxable: b.Taxable, GSTPct: set.gstRate(), GST: b.GST, Total: b.Total, Entry: e.ID})
		if err != nil {
			return out, err
		}
		b.Invoice, b.Entry = inv.No, e.ID
		if err := docs.Upsert(ctx, o.Docs, tenant, cDPBill, b.ID, b, actor); err != nil {
			return out, err
		}
		for bo, p := range paidUpto {
			if a, ok := o.boAccount(ctx, tenant, bo); ok && p > a.AMCPaidUpto {
				a.AMCPaidUpto = p
				if err := docs.Upsert(ctx, o.Docs, tenant, cBO, boKey(a), a, actor); err != nil {
					return out, err
				}
			}
		}
		out = append(out, b)
	}
	o.log(ctx, tenant, actor, "dp.billed", month, map[string]any{"bills": len(out)})
	return out, nil
}

// ---- requests: demat, freeze, transmission, closure ----

// DPRequest is a demat, freeze, unfreeze, transmission or closure request.
type DPRequest struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // Demat, Freeze, Unfreeze, Transmission, Closure
	UCC  string `json:"ucc"`
	Name string `json:"name"`
	// BOID is the demat account the request is for (empty: the client's
	// primary account).
	BOID    string            `json:"bo_id,omitempty"`
	Detail  map[string]string `json:"detail"`
	Status  string            `json:"status"` // Pending, Sent to RTA, Sent to CDSL, Completed, Rejected
	Maker   string            `json:"maker"`
	Checker string            `json:"checker,omitempty"`
	At      string            `json:"at"`
	Note    string            `json:"note,omitempty"`
}

// DPRequestKinds lists the request kinds.
var DPRequestKinds = []string{"Demat", "Freeze", "Unfreeze", "Transmission", "Closure"}

// RaiseDPRequest records a request; a checker completes it.
func (o *Ops) RaiseDPRequest(ctx context.Context, tenant, actor string, r DPRequest) (DPRequest, error) {
	if err := o.require(ctx, tenant, actor, "DP requests", RoleMaker, RoleChecker, RoleCompliance, RoleBranch); err != nil {
		return r, err
	}
	c, err := o.Client(ctx, tenant, r.UCC)
	if err != nil {
		return r, invalid("client %s not found", r.UCC)
	}
	if r.BOID = normDematID(r.BOID); r.BOID != "" {
		if _, ok := dematOf(c, r.BOID); !ok {
			return r, invalid("%s is not a demat account of %s", r.BOID, c.UCC)
		}
	}
	b := o.boFor(ctx, tenant, c, r.BOID)
	r.BOID = b.BOID
	d := r.Detail
	if d == nil {
		d = map[string]string{}
	}
	switch r.Kind {
	case "Demat":
		q, qerr := parseQty(d["qty"])
		var certs int
		fmt.Sscanf(d["certificates"], "%d", &certs)
		// Units in a mutual fund statement of account (a folio) have no
		// certificates (and may be fractional); they go to CDSL by DESTAT
		// (cdsltxn.go).
		if d["isin"] == "" || ((qerr != nil || q.Sign() <= 0) && !strings.EqualFold(d["qty"], "all")) || (certs <= 0 && d["folio"] == "") || d["drf"] == "" {
			return r, invalid("give the DRF number, ISIN, quantity and number of certificates (or the folio, for units)")
		}
	case "Freeze":
		if !contains(FreezeTypes, d["type"]) || strings.TrimSpace(d["reason"]) == "" {
			return r, invalid("give the freeze type (Debit, Credit or Both) and the reason")
		}
	case "Unfreeze":
		if b.Status != "Frozen" {
			return r, fmt.Errorf("%w: the account is not frozen", ErrState)
		}
	case "Transmission":
		if len(d["to_ucc"]) == 0 || d["death_certificate"] == "" {
			return r, invalid("give the claimant's account (UCC) and the death certificate reference")
		}
		if _, err := o.Client(ctx, tenant, d["to_ucc"]); err != nil {
			return r, invalid("claimant account %s not found", d["to_ucc"])
		}
	case "Closure":
		hs, _ := o.Holdings(ctx, tenant, c.UCC)
		held := 0
		acct := holdingAcct(c, b.BOID)
		for _, h := range hs {
			if h.BOID == acct && h.Qty.Sign() > 0 {
				held++
			}
		}
		if held > 0 && d["transfer_to"] == "" {
			return r, invalid("the account still holds %d ISINs: give the BO ID they are to be transferred to, or transfer them first", held)
		}
	default:
		return r, invalid("kind must be one of %s", strings.Join(DPRequestKinds, ", "))
	}
	if b.Status == "Closed" {
		return r, fmt.Errorf("%w: the demat account is closed", ErrState)
	}
	n, err := o.next(ctx, tenant, "dpreq", actor)
	if err != nil {
		return r, err
	}
	r.ID, r.UCC, r.Name, r.Detail, r.Status, r.Maker, r.At = fmt.Sprintf("DPR-%05d", n), c.UCC, c.Name, d, "Pending", actor, o.now().Format(time.RFC3339)
	if _, err := docs.Put(ctx, o.Docs, tenant, cDPReq, r.ID, r, 0, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "dp.request", r.ID, map[string]any{"kind": r.Kind, "ucc": c.UCC})
	return r, nil
}

// ActOnDPRequest moves a request on: a demat request is sent to the RTA
// and then confirmed (crediting the shares) or rejected; the others are
// approved (and applied) or rejected by a checker who did not raise them.
func (o *Ops) ActOnDPRequest(ctx context.Context, tenant, actor, id, action, note string) (DPRequest, error) {
	roles := []string{RoleChecker}
	if action == "send" {
		roles = append(roles, RoleMaker)
	}
	if err := o.require(ctx, tenant, actor, "Completing DP requests", roles...); err != nil {
		return DPRequest{}, err
	}
	r, _, err := docs.Get[DPRequest](ctx, o.Docs, tenant, cDPReq, id)
	if err != nil {
		return r, err
	}
	if r.Status == "Completed" || r.Status == "Rejected" {
		return r, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(r.Status))
	}
	if r.Maker == actor && action != "send" {
		return r, fmt.Errorf("%w: the person who raised a request cannot approve it", ErrForbidden)
	}
	c, err := o.Client(ctx, tenant, r.UCC)
	if err != nil {
		return r, err
	}
	b := o.boFor(ctx, tenant, c, r.BOID)
	acct := holdingAcct(c, b.BOID)
	date := o.businessDate(ctx, tenant)
	status := "Completed"
	viaCDSL := false
	switch action {
	case "reject":
		if strings.TrimSpace(note) == "" {
			return r, invalid("give the reason")
		}
		status = "Rejected"
		if r.Status == "Sent to CDSL" {
			// stop the CDSL request too (the client's link stops working)
			if cr, ok := o.cdslBySource(ctx, tenant, r.ID); ok && cr.open() {
				cr.Status = crCancelled
				cr.event(o.now().Format(time.RFC3339), actor, "Cancelled with "+r.ID+": "+note)
				_ = o.cdslSave(ctx, tenant, actor, &cr)
			}
		}
	case "send":
		if r.Kind != "Demat" || r.Status != "Pending" {
			return r, fmt.Errorf("%w: only a pending demat request goes to the RTA", ErrState)
		}
		status = "Sent to RTA"
	case "approve":
		switch r.Kind {
		case "Demat":
			if r.Status != "Sent to RTA" {
				return r, fmt.Errorf("%w: send the request to the RTA first", ErrState)
			}
			q, _ := parseQty(r.Detail["qty"])
			var certs int
			fmt.Sscanf(r.Detail["certificates"], "%d", &certs)
			isin := strings.ToUpper(r.Detail["isin"])
			if _, err := upd(ctx, o.Docs, tenant, cHolding, holdingIDAt(c.UCC, isin, acct), actor, &Holding{UCC: c.UCC, ISIN: isin, Symbol: r.Detail["symbol"], BOID: acct}, func(h *Holding) error {
				h.Qty = h.Qty.Add(q)
				return nil
			}); err != nil {
				return r, err
			}
			if err := o.dpTxn(ctx, tenant, actor, DPTxn{Date: date, UCC: c.UCC, BOID: b.BOID, ISIN: isin, Symbol: r.Detail["symbol"], Qty: q, Kind: "Demat credit", Ref: r.ID, Billable: true, Certs: certs}); err != nil {
				return r, err
			}
		case "Freeze":
			b.Status, b.Freeze, b.FreezeWhy = "Frozen", r.Detail["type"], r.Detail["reason"]
		case "Unfreeze":
			b.Status, b.Freeze, b.FreezeWhy = "Active", "", ""
		case "Transmission":
			// The account's holdings go to the claimant's account for
			// pay-outs (its primary unless the claimant chose another).
			to := strings.ToUpper(r.Detail["to_ucc"])
			tc, _ := o.Client(ctx, tenant, to)
			toAcct := holdingAcct(tc, payoutDemat(tc).ID)
			hs, _ := o.Holdings(ctx, tenant, c.UCC)
			for _, h := range hs {
				if h.BOID != acct || h.Qty.Sign() <= 0 {
					continue
				}
				if h.Pledged.Sign() > 0 {
					return r, fmt.Errorf("%w: %s has pledged %s; close the pledge first", ErrState, c.UCC, h.Symbol)
				}
				q := h.Qty
				if _, err := docs.Update(ctx, o.Docs, tenant, cHolding, holdingIDAt(c.UCC, h.ISIN, acct), actor, func(x *Holding) error { x.Qty = money.Dec{}; return nil }); err != nil {
					return r, err
				}
				if _, err := upd(ctx, o.Docs, tenant, cHolding, holdingIDAt(to, h.ISIN, toAcct), actor, &Holding{UCC: to, ISIN: h.ISIN, Symbol: h.Symbol, AvgCost: h.AvgCost, BOID: toAcct}, func(x *Holding) error { x.Qty = x.Qty.Add(q); return nil }); err != nil {
					return r, err
				}
				_ = o.dpTxn(ctx, tenant, actor, DPTxn{Date: date, UCC: c.UCC, BOID: b.BOID, ISIN: h.ISIN, Symbol: h.Symbol, Qty: q.Neg(), Kind: "Transmission out", Ref: r.ID})
				_ = o.dpTxn(ctx, tenant, actor, DPTxn{Date: date, UCC: to, BOID: payoutDemat(tc).ID, ISIN: h.ISIN, Symbol: h.Symbol, Qty: q, Kind: "Transmission in", Ref: r.ID})
			}
			b.Status, b.Freeze, b.FreezeWhy = "Frozen", "Both", "Transmission completed ("+r.ID+")"
		case "Closure":
			// With CDSL's account transfer cum closure API on, CDSL moves
			// the holdings and closes the account once the client confirms
			// with an OTP; the account closes here when CDSL confirms.
			if r.Status == "Sent to CDSL" {
				return r, fmt.Errorf("%w: %s is with CDSL; it completes when CDSL confirms (see CDSL requests)", ErrState, id)
			}
			if took, err := o.cdslStartClosure(ctx, tenant, actor, r, c, b); err != nil {
				return r, err
			} else if took {
				status, viaCDSL = "Sent to CDSL", true
				break
			}
			hs, _ := o.Holdings(ctx, tenant, c.UCC)
			for _, h := range hs {
				if h.BOID == acct && h.Qty.Sign() > 0 {
					return r, fmt.Errorf("%w: %s still holds %s%s; execute the transfer first", ErrState, c.UCC, h.Symbol, acctNote(acct))
				}
			}
			b.Status, b.ClosedOn = "Closed", date
		}
		if r.Kind != "Demat" && !viaCDSL {
			if err := o.putBO(ctx, tenant, actor, b); err != nil {
				return r, err
			}
		}
	default:
		return r, invalid("action must be send, approve or reject")
	}
	r, err = docs.Update(ctx, o.Docs, tenant, cDPReq, id, actor, func(x *DPRequest) error {
		x.Status, x.Note = status, note
		if action != "send" {
			x.Checker = actor
		}
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "dp.request_"+action, id, map[string]any{"kind": r.Kind, "ucc": r.UCC, "note": note})
	}
	return r, err
}
