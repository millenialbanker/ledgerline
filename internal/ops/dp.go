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

// BOAccount is the DP's record of a client's demat account.
type BOAccount struct {
	UCC        string `json:"ucc"`
	BOID       string `json:"bo_id"`
	Depository string `json:"depository"`
	Status     string `json:"status"`           // Active, Frozen, Closed
	Freeze     string `json:"freeze,omitempty"` // Debit, Credit, Both
	FreezeWhy  string `json:"freeze_reason,omitempty"`
	BSDA       bool   `json:"bsda"`
	// BSDAOptOut: the client chose a regular account although eligible;
	// OtherDemat: the client has a demat account with another DP.
	BSDAOptOut bool   `json:"bsda_opt_out,omitempty"`
	OtherDemat bool   `json:"other_demat,omitempty"`
	DDPI       bool   `json:"ddpi"`
	Opened     string `json:"opened"`
	ClosedOn   string `json:"closed_on,omitempty"`
}

// DPTariff is the firm's DP rate card (₹, before GST).
type DPTariff struct {
	AMCYear         money.Amount `json:"amc_year"`         // regular account, per year
	BSDALowLimit    money.Amount `json:"bsda_nil_upto"`    // BSDA: nil AMC up to this holding value
	BSDALimit       money.Amount `json:"bsda_limit"`       // BSDA ceiling on holding value
	BSDAAMCYear     money.Amount `json:"bsda_amc_year"`    // BSDA above the nil slab
	DebitCharge     money.Amount `json:"debit_charge"`     // per debit instruction per ISIN
	DepositoryShare money.Amount `json:"depository_share"` // of the debit charge, payable to the depository
	PledgeCharge    money.Amount `json:"pledge_charge"`    // per pledge creation or closure per ISIN
	DematCharge     money.Amount `json:"demat_per_cert"`   // per certificate
	OffMarketPct    money.Dec    `json:"offmarket_pct"`    // % of value for off-market transfers
	OffMarketMin    money.Amount `json:"offmarket_min"`
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
		PledgeCharge: 2000, DematCharge: 15000, OffMarketPct: money.MustDec("0.03"), OffMarketMin: 2500, From: "2026-04-01"}
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
	return t, nil
}

// bo returns a client's BO record, created from the client master on first use.
func (o *Ops) bo(ctx context.Context, tenant string, c Client) BOAccount {
	b, _, err := docs.Get[BOAccount](ctx, o.Docs, tenant, cBO, c.UCC)
	if err != nil {
		b = BOAccount{UCC: c.UCC, BOID: c.Demat.ID, Depository: c.Demat.Depository, Status: "Active", Opened: c.KYCDate}
		if c.Status != "Active" {
			b.Status = "Not opened"
		}
	}
	return b
}

// BORow is a BO account with its holdings.
type BORow struct {
	BOAccount
	Name    string       `json:"name"`
	ISINs   int          `json:"isins"`
	Value   money.Dec    `json:"value"`
	Pledged int64        `json:"pledged_qty"`
	BSDAOK  bool         `json:"bsda_eligible"`
	AMC     money.Amount `json:"amc_year"`
	Ledger  money.Amount `json:"ledger"`
}

// DPView is the DP desk.
type DPView struct {
	Date    string         `json:"date"`
	Rows    []BORow        `json:"rows"`
	Tariff  DPTariff       `json:"tariff"`
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
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return v, err
	}
	hs, _ := o.Holdings(ctx, tenant, "")
	_, look, _ := o.Market(ctx, tenant, date)
	type agg struct {
		n   int
		val money.Dec
		pl  int64
	}
	hv := map[string]*agg{}
	for _, h := range hs {
		a := hv[h.UCC]
		if a == nil {
			a = &agg{}
			hv[h.UCC] = a
		}
		if h.Qty > 0 {
			a.n++
		}
		a.pl += h.Pledged
		if i, ok := look.ByISIN(h.ISIN); ok {
			a.val = a.val.Add(i.LTP.MulInt(h.Qty))
		}
	}
	funds, _ := o.clientFunds(ctx, tenant, date, "")
	for _, u := range sortedKeys(cs) {
		c := cs[u]
		if c.Demat.ID == "" || c.Status != "Active" {
			continue
		}
		b := o.bo(ctx, tenant, c)
		r := BORow{BOAccount: b, Name: c.Name, Ledger: funds[u]}
		if a := hv[u]; a != nil {
			r.ISINs, r.Value, r.Pledged = a.n, a.val, a.pl
		}
		// BSDA: one demat account per PAN, holding value within the limit.
		r.BSDAOK = r.Value.Cmp(t.BSDALimit.Dec()) <= 0
		r.AMC = amcFor(t, b.BSDA, r.Value)
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
	if err := o.require(ctx, tenant, actor, "Changing the account type", RoleChecker); err != nil {
		return BOAccount{}, err
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return BOAccount{}, err
	}
	b := o.bo(ctx, tenant, c)
	if on {
		v, err := o.DP(ctx, tenant, date)
		if err != nil {
			return b, err
		}
		for _, r := range v.Rows {
			if r.UCC == c.UCC && !r.BSDAOK {
				return b, invalid("holdings of ₹%s are above the BSDA limit of ₹%s", formatINR(r.Value), formatINR(v.Tariff.BSDALimit.Dec()))
			}
		}
	}
	b.BSDA = on
	if err := docs.Upsert(ctx, o.Docs, tenant, cBO, b.UCC, b, actor); err != nil {
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
	ID     string  `json:"id"`
	UCC    string  `json:"ucc"`
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
	Qty    int64     `json:"qty"`
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

// IssueDISBook issues a book of n slips to a client.
func (o *Ops) IssueDISBook(ctx context.Context, tenant, actor, ucc string, n int64) (DISBook, error) {
	if err := o.require(ctx, tenant, actor, "Issuing DIS books", RoleMaker, RoleChecker); err != nil {
		return DISBook{}, err
	}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return DISBook{}, err
	}
	if b := o.bo(ctx, tenant, c); b.Status != "Active" {
		return DISBook{}, fmt.Errorf("%w: the demat account is %s", ErrState, strings.ToLower(b.Status))
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
	b := DISBook{ID: fmt.Sprintf("DB-%05d", seq), UCC: c.UCC, From: next, To: next + n - 1, Issued: o.now().Format("2006-01-02"), Used: []int64{}, Lost: []int64{}}
	if err := docs.Upsert(ctx, o.Docs, tenant, cDISBook, b.ID, b, actor); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "dis.book_issued", b.ID, map[string]any{"ucc": c.UCC, "from": b.From, "to": b.To})
	return b, nil
}

func (o *Ops) slipBook(ctx context.Context, tenant, ucc string, slip int64) (DISBook, bool) {
	books, _ := docs.List[DISBook](ctx, o.Docs, tenant, cDISBook, "")
	for _, b := range books {
		if b.UCC == ucc && slip >= b.From && slip <= b.To {
			return b, true
		}
	}
	return DISBook{}, false
}

// freeBalance is what can be moved: holding less pledged and less
// instructions not yet executed.
func (o *Ops) freeBalance(ctx context.Context, tenant, ucc, isin string) int64 {
	h, _, err := docs.Get[Holding](ctx, o.Docs, tenant, cHolding, holdingID(ucc, isin))
	if err != nil {
		return 0
	}
	free := h.Qty - h.Pledged
	ds, _ := docs.List[DIS](ctx, o.Docs, tenant, cDIS, "")
	for _, d := range ds {
		if d.UCC != ucc || (d.Status != "Entered" && d.Status != "Verified" && d.Status != "Uploaded") {
			continue
		}
		for _, l := range d.Lines {
			if l.ISIN == isin {
				free -= l.Qty
			}
		}
	}
	return free
}

// EnterDIS records a slip received from a client.
func (o *Ops) EnterDIS(ctx context.Context, tenant, actor string, d DIS) (DIS, error) {
	if err := o.require(ctx, tenant, actor, "Entering delivery instructions", RoleMaker, RoleChecker, RoleBranch); err != nil {
		return d, err
	}
	c, err := o.Client(ctx, tenant, d.UCC)
	if err != nil {
		return d, invalid("client %s not found", d.UCC)
	}
	b := o.bo(ctx, tenant, c)
	switch {
	case b.Status == "Closed":
		return d, fmt.Errorf("%w: the demat account is closed", ErrState)
	case b.Status == "Frozen" && b.Freeze != "Credit":
		return d, fmt.Errorf("%w: the demat account is frozen for debits (%s)", ErrState, b.FreezeWhy)
	}
	book, ok := o.slipBook(ctx, tenant, c.UCC, d.Slip)
	if !ok {
		return d, invalid("slip %d was not issued to %s", d.Slip, c.UCC)
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
		if l.Qty <= 0 {
			return d, invalid("quantity must be above zero")
		}
		if free := o.freeBalance(ctx, tenant, c.UCC, l.ISIN); l.Qty > free {
			return d, invalid("only %d free in %s (after pledges and pending instructions)", max(free, 0), l.ISIN)
		}
		if h, _, err := docs.Get[Holding](ctx, o.Docs, tenant, cHolding, holdingID(c.UCC, l.ISIN)); err == nil {
			l.Symbol = h.Symbol
		}
		if i, ok := look.ByISIN(l.ISIN); ok {
			l.Value = i.LTP.MulInt(l.Qty)
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
			rows = append(rows, []string{batch, d.ID, fmt.Sprint(d.Slip), d.BOID, d.ToBO, d.Kind, d.ExecDate, l.ISIN, fmt.Sprint(l.Qty), d.Reason, d.Consideration.String()})
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
		for _, l := range d.Lines {
			if _, err := docs.Update(ctx, o.Docs, tenant, cHolding, holdingID(d.UCC, l.ISIN), actor, func(h *Holding) error {
				if h.Qty-h.Pledged < l.Qty {
					return fmt.Errorf("%w: %s no longer has %d free in %s", ErrState, d.UCC, l.Qty, l.ISIN)
				}
				h.Qty -= l.Qty
				return nil
			}); err != nil {
				return d, err
			}
			if err := o.dpTxn(ctx, tenant, actor, DPTxn{Date: date, UCC: d.UCC, ISIN: l.ISIN, Symbol: l.Symbol, Qty: -l.Qty, Kind: d.Kind + " transfer", Ref: d.ID, Value: l.Value, Billable: true, Counterparty: []string{d.ToBO}}); err != nil {
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
	ID       string    `json:"id"`
	Date     string    `json:"date"`
	UCC      string    `json:"ucc"`
	ISIN     string    `json:"isin"`
	Symbol   string    `json:"symbol"`
	Qty      int64     `json:"qty"` // + credit, − debit
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
func (o *Ops) RunDPBilling(ctx context.Context, tenant, actor, month string) ([]DPBill, error) {
	if err := o.require(ctx, tenant, actor, "DP billing", RoleChecker); err != nil {
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
	var out []DPBill
	for _, r := range v.Rows {
		if r.Status == "Closed" || r.Status == "Not opened" {
			continue
		}
		if _, _, err := docs.Get[DPBill](ctx, o.Docs, tenant, cDPBill, month+"|"+r.UCC); err == nil {
			continue
		}
		b := DPBill{ID: month + "|" + r.UCC, Month: month, UCC: r.UCC, Name: r.Name}
		if amc := r.AMC / 12; amc > 0 {
			b.Lines = append(b.Lines, DPBillLine{What: map[bool]string{true: "Annual maintenance (BSDA), one month", false: "Annual maintenance, one month"}[r.BSDA], Count: 1, Amount: amc})
		}
		ts, _ := o.DPTransactions(ctx, tenant, r.UCC)
		var debits, credits, offmkt int64
		var omCharge money.Amount
		var certs int64
		for _, x := range ts {
			if x.Date < month+"-01" || x.Date > end || !x.Billable {
				continue
			}
			if _, ok := t.freeFor(free, x); ok && x.Certs == 0 {
				b.Free++
				continue
			}
			if x.Qty > 0 && x.Certs == 0 && !strings.HasPrefix(x.Kind, "IPO") {
				credits++
			}
			if x.Qty < 0 {
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
			b.Lines = append(b.Lines, DPBillLine{What: "Debit instructions (per ISIN)", Count: debits, Amount: t.DebitCharge * money.Amount(debits), Share: t.DepositoryShare * money.Amount(debits)})
		}
		if credits > 0 && t.CreditCharge > 0 {
			b.Lines = append(b.Lines, DPBillLine{What: "Credit instructions (per ISIN)", Count: credits, Amount: t.CreditCharge * money.Amount(credits)})
		}
		if offmkt > 0 {
			b.Lines = append(b.Lines, DPBillLine{What: "Off-market transfer charge", Count: offmkt, Amount: omCharge})
		}
		if certs > 0 {
			b.Lines = append(b.Lines, DPBillLine{What: "Dematerialisation (per certificate)", Count: certs, Amount: t.DematCharge * money.Amount(certs)})
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
		ps := []ledger.Posting{{Account: ledger.ClientAccount(r.UCC), Debit: b.Total, Memo: desc}}
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
		inv, err := o.issueInvoice(ctx, tenant, actor, Invoice{Date: end, UCC: r.UCC, Name: r.Name, Kind: "Depository", Description: desc, SAC: "997152",
			Taxable: b.Taxable, GSTPct: set.gstRate(), GST: b.GST, Total: b.Total, Entry: e.ID})
		if err != nil {
			return out, err
		}
		b.Invoice, b.Entry = inv.No, e.ID
		if err := docs.Upsert(ctx, o.Docs, tenant, cDPBill, b.ID, b, actor); err != nil {
			return out, err
		}
		out = append(out, b)
	}
	o.log(ctx, tenant, actor, "dp.billed", month, map[string]any{"bills": len(out)})
	return out, nil
}

// ---- requests: demat, freeze, transmission, closure ----

// DPRequest is a demat, freeze, unfreeze, transmission or closure request.
type DPRequest struct {
	ID      string            `json:"id"`
	Kind    string            `json:"kind"` // Demat, Freeze, Unfreeze, Transmission, Closure
	UCC     string            `json:"ucc"`
	Name    string            `json:"name"`
	Detail  map[string]string `json:"detail"`
	Status  string            `json:"status"` // Pending, Sent to RTA, Completed, Rejected
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
	b := o.bo(ctx, tenant, c)
	d := r.Detail
	if d == nil {
		d = map[string]string{}
	}
	switch r.Kind {
	case "Demat":
		var q int64
		var certs int
		fmt.Sscanf(d["qty"], "%d", &q)
		fmt.Sscanf(d["certificates"], "%d", &certs)
		// Units in a mutual fund statement of account (a folio) have no
		// certificates; they go to CDSL by DESTAT (cdsltxn.go).
		if d["isin"] == "" || (q <= 0 && !strings.EqualFold(d["qty"], "all")) || (certs <= 0 && d["folio"] == "") || d["drf"] == "" {
			return r, invalid("give the DRF number, ISIN, quantity and number of certificates (or the folio, for units)")
		}
	case "Freeze":
		if !contains([]string{"Debit", "Credit", "Both"}, d["type"]) || strings.TrimSpace(d["reason"]) == "" {
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
		for _, h := range hs {
			if h.Qty > 0 {
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
	b := o.bo(ctx, tenant, c)
	date := o.businessDate(ctx, tenant)
	status := "Completed"
	switch action {
	case "reject":
		if strings.TrimSpace(note) == "" {
			return r, invalid("give the reason")
		}
		status = "Rejected"
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
			var q int64
			var certs int
			fmt.Sscanf(r.Detail["qty"], "%d", &q)
			fmt.Sscanf(r.Detail["certificates"], "%d", &certs)
			isin := strings.ToUpper(r.Detail["isin"])
			if _, err := upd(ctx, o.Docs, tenant, cHolding, holdingID(c.UCC, isin), actor, &Holding{UCC: c.UCC, ISIN: isin, Symbol: r.Detail["symbol"]}, func(h *Holding) error {
				h.Qty += q
				return nil
			}); err != nil {
				return r, err
			}
			if err := o.dpTxn(ctx, tenant, actor, DPTxn{Date: date, UCC: c.UCC, ISIN: isin, Symbol: r.Detail["symbol"], Qty: q, Kind: "Demat credit", Ref: r.ID, Billable: true, Certs: certs}); err != nil {
				return r, err
			}
		case "Freeze":
			b.Status, b.Freeze, b.FreezeWhy = "Frozen", r.Detail["type"], r.Detail["reason"]
		case "Unfreeze":
			b.Status, b.Freeze, b.FreezeWhy = "Active", "", ""
		case "Transmission":
			to := strings.ToUpper(r.Detail["to_ucc"])
			hs, _ := o.Holdings(ctx, tenant, c.UCC)
			for _, h := range hs {
				if h.Qty <= 0 {
					continue
				}
				if h.Pledged > 0 {
					return r, fmt.Errorf("%w: %s has pledged %s; close the pledge first", ErrState, c.UCC, h.Symbol)
				}
				q := h.Qty
				if _, err := docs.Update(ctx, o.Docs, tenant, cHolding, holdingID(c.UCC, h.ISIN), actor, func(x *Holding) error { x.Qty = 0; return nil }); err != nil {
					return r, err
				}
				if _, err := upd(ctx, o.Docs, tenant, cHolding, holdingID(to, h.ISIN), actor, &Holding{UCC: to, ISIN: h.ISIN, Symbol: h.Symbol, AvgCost: h.AvgCost}, func(x *Holding) error { x.Qty += q; return nil }); err != nil {
					return r, err
				}
				_ = o.dpTxn(ctx, tenant, actor, DPTxn{Date: date, UCC: c.UCC, ISIN: h.ISIN, Symbol: h.Symbol, Qty: -q, Kind: "Transmission out", Ref: r.ID})
				_ = o.dpTxn(ctx, tenant, actor, DPTxn{Date: date, UCC: to, ISIN: h.ISIN, Symbol: h.Symbol, Qty: q, Kind: "Transmission in", Ref: r.ID})
			}
			b.Status, b.Freeze, b.FreezeWhy = "Frozen", "Both", "Transmission completed ("+r.ID+")"
		case "Closure":
			hs, _ := o.Holdings(ctx, tenant, c.UCC)
			for _, h := range hs {
				if h.Qty > 0 {
					return r, fmt.Errorf("%w: %s still holds %s; execute the transfer first", ErrState, c.UCC, h.Symbol)
				}
			}
			b.Status, b.ClosedOn = "Closed", date
		}
		if r.Kind != "Demat" {
			if err := docs.Upsert(ctx, o.Docs, tenant, cBO, b.UCC, b, actor); err != nil {
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
