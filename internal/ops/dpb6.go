package ops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- DP controls, round 6 of the compliance gap report ----
//
// The rows closed here (compliance-gaps.md, -appendix.md, -product.md):
//   - DP-0026 (SEBI Master Circular for Depositories 2024/168, 1.10.4): AMC
//     collected in advance for a year is refunded pro rata, in whole
//     quarters, when the account is closed or transferred. Ledgerline
//     reads the safe way: the quarter in which the account closes is
//     used; the quarters after it are refunded. The refund is worked out
//     from what the account was billed for the period (its bill lines),
//     else from its plan; an Ops checker approves it, and it is posted
//     back to the client with GST and a credit note.
//   - DP-0222 (CDSL DPOI 7.5.7) and DP-0328 (CDSL DP2025-411): a death
//     intimation received directly (from a nominee, a joint holder or a
//     legal heir, not on the KRAs' list) is entered here. It opens the same
//     deceased-client case as the KRA's list (deceased.go): debits blocked
//     at once, and a DP freeze request for debits on each of the client's
//     demat accounts with the DP (maker-checker; the DP desk sets it in
//     CDAS with CDSL's holder-deceased reason code). The death
//     certificate's check is recorded: where it was verified (the issuing
//     government site or the registrar, or the original seen) and that the
//     copy was signed and stamped by the person who checked it.
//   - DP-0261 (CDSL DPOI 13.5.3): CDSL's instruction on a BO's KYC
//     deficiency: the BO told by letter, e-mail and phone (dispatch proof
//     and e-mail log kept), 30 days to rectify from the last of them, then
//     a freeze under reason code 20, CDSL informed and the acknowledgement
//     sent.
//   - DP-0299 (CDSL/OPS/DP/POLCY/2026/656 and 665): transfer-cum-closure
//     requests a client starts with an OTP on CDSL's site wait in CDAS for
//     the DP. They are entered (or noted from CDAS) with the date received;
//     the BO ID, the target account and the holdings are checked and the
//     request approved or rejected in CDAS within 2 working days.
//   - DP-0305 (CDSL/OPS/DP/POLCY/2026/734): accounts frozen under reason 27
//     whose holders' KYC the KRA has since validated are listed, and an
//     unfreeze request raised for each (CDSL's prescribed process,
//     CDSL/OPS/DP/SYSTM/2023/525, is followed in CDAS).
//   - DP-0335 (CDSL DP2025-688): the Rights and Obligations given to a
//     joint account's holders carries the clause that a statutory freeze
//     on one holder freezes the whole account (a firm practice records
//     that the firm's document has it; the e-mailed kit says it).
//   - DP-0040 (SEBI master circular 2024/168, 1.14.2-1.14.3): a demat
//     account is opened only with a nomination or a signed opt-out
//     declaration on record: a client with neither cannot be sent to CDSL
//     (cdslbo.go), and the declaration is recorded here.
//   - SEBI-SB1-0150 (SEBI stock broker master circular 2025/90,
//     36.8.1.1-36.8.1.6): each account's PoA or DDPI document is checked
//     against the terms: the accounts it operates and the destination
//     accounts named (the client's own or the broker's settlement
//     accounts only), in the name of the registered entity and not of an
//     employee, not assignable, duly stamped, and requiring the return of
//     securities or money received in error. A maker ticks the terms, a
//     checker approves.
//
// All of it is for a depository participant: nothing is raised when the
// firm profile says the firm is not one (dutyNeed "dp" / "cdsl").

const (
	cB6DP   = "b6dp" // keys: amc|BOID, intim|DC-…, kycdef|KD-…, otpclose|OC-…, poa|BOID
	wB6DP   = "dpb6"
	b6Quart = 4

	cdslReasonKYCDef = "20" // KYC deficiency (CDSL DPOI 13.5.3)
	kycDefDays       = 30
	otpCloseWD       = 2 // working days to approve or reject (CDSL/OPS/DP/POLCY/2026/656)
)

// b6Need reads a firm-profile condition: false only when the profile says
// no (unknown runs, but the callers do not nag about it).
func (o *Ops) b6Need(ctx context.Context, tenant, cond string) bool {
	v, _ := dutyNeed(cond, o.firmFacts(ctx, tenant))
	return v != triNo
}

// b6Sure: the profile says yes.
func (o *Ops) b6Sure(ctx context.Context, tenant, cond string) bool {
	v, _ := dutyNeed(cond, o.firmFacts(ctx, tenant))
	return v == triYes
}

func b6Step(on, by, ref string) *DemiseStep {
	return &DemiseStep{On: on, By: by, At: time.Now().UTC().Format(time.RFC3339), Ref: ref}
}

// ---- DP-0026: pro-rata AMC refund on closure or transfer ----

// DPAMCRefund is the unused annual maintenance of a closed account.
type DPAMCRefund struct {
	ID         string       `json:"id"` // AMC-<BOID>
	UCC        string       `json:"ucc"`
	BOID       string       `json:"bo_id"`
	ClosedOn   string       `json:"closed_on"`
	PeriodFrom string       `json:"period_from"`
	PeriodTo   string       `json:"period_to"`
	Charged    money.Amount `json:"charged"`  // maintenance billed for the period, before GST
	Unused     int          `json:"quarters"` // whole quarters after the quarter of closure
	Taxable    money.Amount `json:"taxable"`
	GST        money.Amount `json:"gst"`
	Total      money.Amount `json:"total"`
	Basis      string       `json:"basis"` // where the charge came from
	Status     string       `json:"status"`
	Checker    string       `json:"checker,omitempty"`
	Decided    string       `json:"decided_at,omitempty"`
	Note       string       `json:"note,omitempty"`
	Entry      string       `json:"entry,omitempty"`
	CreditNote string       `json:"credit_note,omitempty"`
}

const (
	AMCToApprove = "To approve"
	AMCRefunded  = "Refunded"
	AMCNotDue    = "Nothing to refund"
	AMCRejected  = "Not refunded"
)

// amcQuarters is how many whole quarters of a period starting from are
// left after the quarter in which closed falls.
func amcQuarters(from, closed string) int {
	f, err := time.Parse("2006-01-02", from)
	if err != nil {
		return 0
	}
	for i := 0; i < b6Quart; i++ {
		end := f.AddDate(0, 3*(i+1), 0).Format("2006-01-02")
		if closed < end {
			return b6Quart - 1 - i
		}
	}
	return 0
}

// amcCharged is what an account was billed as maintenance for the period
// ending upto: the bill lines that name the period.
func (o *Ops) amcCharged(ctx context.Context, tenant string, b BOAccount, upto string) money.Amount {
	bills, _ := docs.List[DPBill](ctx, o.Docs, tenant, cDPBill, "")
	var sum money.Amount
	for _, x := range bills {
		if x.UCC != b.UCC {
			continue
		}
		for _, l := range x.Lines {
			if l.BOID != "" && normDematID(l.BOID) != normDematID(b.BOID) {
				continue
			}
			if (strings.HasPrefix(l.What, "Annual maintenance") || strings.HasPrefix(l.What, "Corporate account maintenance")) && strings.HasSuffix(l.What, " to "+upto) {
				sum += l.Amount
			}
		}
	}
	return sum
}

// dpAMCRefundScan finds closed accounts on a yearly AMC plan paid beyond
// the closure and opens a refund for the checker.
func (o *Ops) dpAMCRefundScan(ctx context.Context, tenant, day string) {
	if !o.b6Need(ctx, tenant, "dp") {
		return
	}
	plans := o.dpPlanMap(ctx, tenant)
	set, _ := o.Settings(ctx, tenant)
	for _, b := range o.boAccounts(ctx, tenant) {
		if b.Status != "Closed" || b.ClosedOn == "" || b.AMCPaidUpto == "" || b.AMCPaidUpto >= "9999" || b.AMCPaidUpto <= b.ClosedOn {
			continue
		}
		p, ok := plans[b.planAt(b.ClosedOn)]
		if !ok || p.AMCBilling != AMCYearly {
			continue
		}
		id := "AMC-" + normDematID(b.BOID)
		if _, _, err := docs.Get[DPAMCRefund](ctx, o.Docs, tenant, cB6DP, "amc|"+b.BOID); err == nil {
			continue
		}
		to, _ := time.Parse("2006-01-02", b.AMCPaidUpto)
		from := to.AddDate(-1, 0, 1).Format("2006-01-02")
		r := DPAMCRefund{ID: id, UCC: b.UCC, BOID: b.BOID, ClosedOn: b.ClosedOn, PeriodFrom: from, PeriodTo: b.AMCPaidUpto, Unused: amcQuarters(from, b.ClosedOn)}
		r.Charged, r.Basis = o.amcCharged(ctx, tenant, b, b.AMCPaidUpto), "the account's DP bill for the period"
		if r.Charged == 0 {
			if v := p.at(from); v != nil {
				r.Charged, r.Basis = v.AMCYear, "the plan's annual maintenance (no bill for the period in Ledgerline)"
			}
		}
		r.Taxable = money.Amount(int64(r.Charged) * int64(r.Unused) / b6Quart)
		r.GST = r.Taxable.Dec().Pct(set.gstRate()).Round(money.UnitPaisa)
		r.Total = r.Taxable + r.GST
		r.Status = AMCToApprove
		if r.Taxable == 0 {
			r.Status = AMCNotDue
		}
		if err := docs.Upsert(ctx, o.Docs, tenant, cB6DP, "amc|"+b.BOID, r, SystemActor); err != nil {
			continue
		}
		o.log(ctx, tenant, SystemActor, "dp.amc_refund_due", id, map[string]any{"ucc": b.UCC, "quarters": r.Unused, "taxable": r.Taxable.String()})
		if r.Status == AMCToApprove {
			o.raiseWork(ctx, tenant, wB6DP, "amc:"+id, "Refund unused AMC to "+b.UCC+" (₹"+r.Total.String()+")",
				fmt.Sprintf("Demat account %s closed on %s with maintenance paid to %s: %d unused quarter(s) of ₹%s are refunded (SEBI Master Circular for Depositories 1.10.4). Check and approve on Depository → Demat accounts & DIS.", cdslMaskBO(b.BOID), fmtDay(b.ClosedOn), fmtDay(b.AMCPaidUpto), r.Unused, r.Charged.String()),
				b.UCC, RoleChecker, "dp", day)
		}
	}
}

// DecideAMCRefund approves (posts) or rejects (with the reason) a refund.
func (o *Ops) DecideAMCRefund(ctx context.Context, tenant, actor, boid string, approve bool, note string) (DPAMCRefund, error) {
	if err := o.require(ctx, tenant, actor, "Approving AMC refunds", RoleChecker); err != nil {
		return DPAMCRefund{}, err
	}
	key := "amc|" + normDematID(boid)
	r, _, err := docs.Get[DPAMCRefund](ctx, o.Docs, tenant, cB6DP, key)
	if err != nil {
		return r, err
	}
	if r.Status != AMCToApprove {
		return r, fmt.Errorf("%w: %s is %s", ErrState, r.ID, strings.ToLower(r.Status))
	}
	note = strings.TrimSpace(note)
	now := o.now()
	if !approve {
		if len(note) < 4 {
			return r, invalid("give the reason the AMC is not refunded")
		}
		r.Status = AMCRejected
	} else {
		day := now.In(ist).Format("2006-01-02")
		desc := fmt.Sprintf("Refund of unused annual maintenance (%d quarter%s, %s to %s), account closed %s", r.Unused, plural(r.Unused), fmtDay(r.PeriodFrom), fmtDay(r.PeriodTo), fmtDay(r.ClosedOn))
		e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "DPAMCREF:" + r.BOID, ValueDate: day, CreatedBy: actor, Narration: desc,
			Source: ledger.SourceRef{Kind: "DP_AMC_REFUND", ID: r.ID}, Postings: []ledger.Posting{
				{Account: IncomeDP, Debit: r.Taxable, Memo: "AMC refunded"},
				{Account: ledger.PayableGST, Debit: r.GST, Memo: "GST on the refund"},
				{Account: ledger.ClientAccount(r.UCC), Credit: r.Total, Memo: desc}}})
		if err != nil {
			return r, err
		}
		set, _ := o.Settings(ctx, tenant)
		c, _ := o.Client(ctx, tenant, r.UCC)
		inv, err := o.issueInvoice(ctx, tenant, actor, Invoice{Date: day, UCC: r.UCC, Name: c.Name, Kind: "CRN", Description: "Credit note: " + desc, SAC: "997152",
			Taxable: r.Taxable, GSTPct: set.gstRate(), GST: r.GST, Total: r.Total, Entry: e.ID})
		if err != nil {
			return r, err
		}
		r.Status, r.Entry, r.CreditNote = AMCRefunded, e.ID, inv.No
	}
	r.Checker, r.Decided, r.Note = actor, now.Format(time.RFC3339), note
	if err := docs.Upsert(ctx, o.Docs, tenant, cB6DP, key, r, actor); err != nil {
		return r, err
	}
	o.closeWork(ctx, tenant, actor, wB6DP, "amc:"+r.ID, r.Status)
	o.log(ctx, tenant, actor, "dp.amc_refund", r.ID, map[string]any{"ucc": r.UCC, "status": r.Status, "total": r.Total.String(), "note": note})
	return r, nil
}

// ---- DP-0222 / DP-0328: a death intimation received directly ----

// DeathIntimation is what was received and how the certificate was
// checked.
type DeathIntimation struct {
	Case        string `json:"case"`
	UCC         string `json:"ucc"`
	Direct      bool   `json:"direct"` // received directly, not on the KRAs' list
	Notifier    string `json:"notifier,omitempty"`
	Relation    string `json:"relation,omitempty"`
	Received    string `json:"received,omitempty"`
	DateOfDeath string `json:"date_of_death,omitempty"`
	Certificate string `json:"certificate,omitempty"` // its number
	// The check of the certificate (DPOI 7.5.7).
	CheckedVia    string   `json:"checked_via,omitempty"` // Government site, Registrar, Original seen
	CheckedWhere  string   `json:"checked_where,omitempty"`
	SignedStamped bool     `json:"signed_stamped,omitempty"`
	CheckedBy     string   `json:"checked_by,omitempty"`
	CheckedOn     string   `json:"checked_on,omitempty"`
	Freezes       []string `json:"freezes,omitempty"` // DP freeze requests raised
	By            string   `json:"by"`
	At            string   `json:"at"`
}

// DeathCertVia are the ways a certificate is checked.
var DeathCertVia = []string{"Government site", "Registrar of births and deaths", "Original seen"}

// DeathIntimationReq is a direct intimation as staff enter it.
type DeathIntimationReq struct {
	UCC         string `json:"ucc"`
	Notifier    string `json:"notifier"`
	Relation    string `json:"relation"`
	Received    string `json:"received"`
	DateOfDeath string `json:"date_of_death"`
	Certificate string `json:"certificate"`
	Note        string `json:"note"`
}

// RecordDeathIntimation opens a deceased-client case from an intimation
// received directly: debits blocked at once, and a debit-freeze request on
// each of the client's demat accounts with the DP.
func (o *Ops) RecordDeathIntimation(ctx context.Context, tenant, actor string, in DeathIntimationReq) (DemiseCase, error) {
	if err := o.require(ctx, tenant, actor, "Recording a death intimation", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return DemiseCase{}, err
	}
	c, err := o.Client(ctx, tenant, strings.ToUpper(strings.TrimSpace(in.UCC)))
	if err != nil {
		return DemiseCase{}, invalid("client %s not found", in.UCC)
	}
	if c.Deceased != nil {
		return DemiseCase{}, fmt.Errorf("%w: %s already has deceased case %s", ErrState, c.UCC, c.Deceased.Case)
	}
	now := o.now()
	today := now.In(ist).Format("2006-01-02")
	rec := firstOf(strings.TrimSpace(in.Received), today)
	if !validDate(rec) || rec > today {
		return DemiseCase{}, invalid("give the date the intimation was received (not in the future)")
	}
	if strings.TrimSpace(in.Notifier) == "" || strings.TrimSpace(in.Relation) == "" {
		return DemiseCase{}, invalid("give who sent the intimation and their relation to the holder")
	}
	if strings.TrimSpace(in.Certificate) == "" {
		return DemiseCase{}, invalid("give the death certificate's number: the freeze is set on a valid certificate")
	}
	if d := strings.TrimSpace(in.DateOfDeath); d != "" && (!validDate(d) || d > rec) {
		return DemiseCase{}, invalid("the date of death is YYYY-MM-DD, on or before the intimation")
	}
	set, _ := o.Settings(ctx, tenant)
	period := rec[:7]
	cutoff, circ := demiseCycle(period)
	n, err := o.next(ctx, tenant, "demisecase", actor)
	if err != nil {
		return DemiseCase{}, err
	}
	d := DemiseCase{ID: fmt.Sprintf("DC-%06d", n), UCC: c.UCC, Name: c.Name, PAN: c.PAN, MatchedBy: "Intimation to the firm", List: "Direct intimation",
		Period: period, Cutoff: cutoff, Circular: circ, ReportedOn: rec, Status: DemiseReported, VerifyDue: set.NextBusinessDay(rec),
		TransmissionDue: addDays(rec, demiseTransmissionDays), Blocked: now.Format(time.RFC3339), UCCs: []DemiseUCC{}}
	for _, ex := range demiseExchanges(set, c, o.listUCCRegs(ctx, tenant)[c.UCC]) {
		d.UCCs = append(d.UCCs, DemiseUCC{Exchange: ex, Status: DemiseToClose})
	}
	d.History = []MNRLEvent{{At: now.Format(time.RFC3339), By: actor, Status: DemiseReported, Note: fmt.Sprintf("Intimation from %s (%s) received %s, certificate %s; debits blocked. %s", in.Notifier, in.Relation, fmtDay(rec), in.Certificate, strings.TrimSpace(in.Note))}}
	if err := o.demiseBlock(ctx, tenant, actor, &d, false); err != nil {
		return d, err
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cDemiseCase, d.ID, d, 0, actor); err != nil {
		return d, err
	}
	di := DeathIntimation{Case: d.ID, UCC: c.UCC, Direct: true, Notifier: strings.TrimSpace(in.Notifier), Relation: strings.TrimSpace(in.Relation), Received: rec,
		DateOfDeath: strings.TrimSpace(in.DateOfDeath), Certificate: strings.TrimSpace(in.Certificate), By: actor, At: now.Format(time.RFC3339)}
	if o.b6Need(ctx, tenant, "dp") {
		di.Freezes = o.deceasedFreezes(ctx, tenant, actor, c, d.ID)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cB6DP, "intim|"+d.ID, di, actor); err != nil {
		return d, err
	}
	o.demiseWork(ctx, tenant, actor, d)
	o.deathCertWork(ctx, tenant, d, di)
	o.log(ctx, tenant, actor, "deceased.intimation", d.ID, map[string]any{"ucc": c.UCC, "relation": di.Relation, "received": rec, "freezes": di.Freezes})
	return d, nil
}

// deceasedFreezes raises a debit-freeze request on each of the client's
// open demat accounts with the DP that has none yet.
func (o *Ops) deceasedFreezes(ctx context.Context, tenant, actor string, c Client, caseID string) []string {
	var out []string
	for _, b := range o.dpAccountsOf(ctx, tenant, c) {
		if b.BOID == "" || b.Status == "Closed" || b.Status == "Not opened" || (b.Status == "Frozen" && (b.Freeze == "Debit" || b.Freeze == "Both")) {
			continue
		}
		r, err := o.RaiseDPRequest(ctx, tenant, actor, DPRequest{Kind: "Freeze", UCC: c.UCC, BOID: b.BOID, Detail: map[string]string{"type": "Debit", "by": "DP",
			"reason": "Holder deceased (" + caseID + "): freeze for debits with CDSL's holder-deceased reason code in CDAS (CDSL DPOI 7.5.7, DP2025-411)"}})
		if err == nil {
			out = append(out, r.ID)
		}
	}
	return out
}

// DeathCertCheck is how the certificate was checked.
type DeathCertCheck struct {
	Via           string `json:"via"`
	Where         string `json:"where"`
	SignedStamped bool   `json:"signed_stamped"`
	On            string `json:"on"`
}

// RecordDeathCertCheck records the check of a case's death certificate.
func (o *Ops) RecordDeathCertCheck(ctx context.Context, tenant, actor, caseID string, ck DeathCertCheck) (DeathIntimation, error) {
	if err := o.require(ctx, tenant, actor, "Recording a death certificate check", RoleChecker, RoleCompliance); err != nil {
		return DeathIntimation{}, err
	}
	d, _, err := docs.Get[DemiseCase](ctx, o.Docs, tenant, cDemiseCase, caseID)
	if err != nil {
		return DeathIntimation{}, err
	}
	if !contains(DeathCertVia, ck.Via) {
		return DeathIntimation{}, invalid("the certificate is checked on one of: %s", strings.Join(DeathCertVia, ", "))
	}
	if ck.Via != "Original seen" && strings.TrimSpace(ck.Where) == "" {
		return DeathIntimation{}, invalid("name the site or registrar it was checked with")
	}
	if !ck.SignedStamped {
		return DeathIntimation{}, invalid("the copy kept is signed and stamped by the person who checked it (CDSL DPOI 7.5.7)")
	}
	today := o.now().In(ist).Format("2006-01-02")
	on := firstOf(strings.TrimSpace(ck.On), today)
	if !validDate(on) || on > today {
		return DeathIntimation{}, invalid("give the date it was checked (not in the future)")
	}
	di, _, _ := docs.GetOr(ctx, o.Docs, tenant, cB6DP, "intim|"+caseID, DeathIntimation{Case: caseID, UCC: d.UCC, By: actor, At: o.now().Format(time.RFC3339)})
	di.CheckedVia, di.CheckedWhere, di.SignedStamped, di.CheckedBy, di.CheckedOn = ck.Via, strings.TrimSpace(ck.Where), true, actor, on
	if err := docs.Upsert(ctx, o.Docs, tenant, cB6DP, "intim|"+caseID, di, actor); err != nil {
		return di, err
	}
	o.closeWork(ctx, tenant, actor, wB6DP, "cert:"+caseID, "checked")
	o.log(ctx, tenant, actor, "deceased.certificate_checked", caseID, map[string]any{"via": ck.Via, "where": di.CheckedWhere})
	return di, nil
}

// deathCertWork asks for the certificate check of a case whose client has
// a demat account with the DP.
func (o *Ops) deathCertWork(ctx context.Context, tenant string, d DemiseCase, di DeathIntimation) {
	if di.CheckedOn != "" || !demiseOpen(d) || len(o.dpAccountsOfUCC(ctx, tenant, d.UCC)) == 0 || !o.b6Need(ctx, tenant, "dp") {
		return
	}
	o.raiseWork(ctx, tenant, wB6DP, "cert:"+d.ID, "Check the death certificate of "+d.UCC+" ("+d.ID+")",
		"Verify the certificate on the issuing government site or with the registrar (or see the original), sign and stamp the copy kept, and record the check on Compliance → Deceased clients (CDSL DPOI 7.5.7).",
		d.UCC, RoleCompliance, "deceased", d.ReportedOn)
}

// ---- DP-0261: CDSL's instruction on a KYC deficiency ----

// KYCDefCase is one BO's KYC deficiency CDSL reported.
type KYCDefCase struct {
	ID         string      `json:"id"`
	UCC        string      `json:"ucc"`
	BOID       string      `json:"bo_id"`
	CDSLRef    string      `json:"cdsl_ref"`
	Received   string      `json:"received"`
	Deficiency string      `json:"deficiency"`
	FreezeType string      `json:"freeze_type"` // as CDSL's instruction says
	Letter     *DemiseStep `json:"letter,omitempty"`
	Email      *DemiseStep `json:"email,omitempty"`
	Phone      *DemiseStep `json:"phone,omitempty"`
	Due        string      `json:"due,omitempty"` // 30 days from the last notice
	Rectified  *DemiseStep `json:"rectified,omitempty"`
	FreezeReq  string      `json:"freeze_request,omitempty"`
	Informed   *DemiseStep `json:"cdsl_informed,omitempty"`
	AckSent    *DemiseStep `json:"ack_sent,omitempty"`
	Status     string      `json:"status"`
	By         string      `json:"by"`
}

func (k KYCDefCase) stage(today string) string {
	switch {
	case k.Rectified != nil:
		return "Rectified"
	case k.Letter == nil || k.Email == nil || k.Phone == nil:
		return "Tell the BO"
	case k.FreezeReq == "" && today <= k.Due:
		return "Waiting 30 days"
	case k.FreezeReq == "":
		return "Freeze (reason 20)"
	case k.Informed == nil || k.AckSent == nil:
		return "Inform CDSL"
	}
	return "Done"
}

// KYCDefReq is CDSL's instruction as entered.
type KYCDefReq struct {
	UCC        string `json:"ucc"`
	BOID       string `json:"bo_id"`
	CDSLRef    string `json:"cdsl_ref"`
	Received   string `json:"received"`
	Deficiency string `json:"deficiency"`
	FreezeType string `json:"freeze_type"`
}

// RecordKYCDeficiency enters CDSL's instruction on a BO's KYC deficiency.
func (o *Ops) RecordKYCDeficiency(ctx context.Context, tenant, actor string, in KYCDefReq) (KYCDefCase, error) {
	if err := o.require(ctx, tenant, actor, "CDSL KYC deficiency cases", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return KYCDefCase{}, err
	}
	c, err := o.Client(ctx, tenant, strings.ToUpper(strings.TrimSpace(in.UCC)))
	if err != nil {
		return KYCDefCase{}, invalid("client %s not found", in.UCC)
	}
	b := o.boForRecord(ctx, tenant, c, in.BOID)
	if b.BOID == "" {
		return KYCDefCase{}, invalid("%s has no demat account with the DP", c.UCC)
	}
	today := o.now().In(ist).Format("2006-01-02")
	rec := firstOf(strings.TrimSpace(in.Received), today)
	if !validDate(rec) || rec > today || strings.TrimSpace(in.CDSLRef) == "" || strings.TrimSpace(in.Deficiency) == "" {
		return KYCDefCase{}, invalid("give CDSL's reference, the date received and the deficiency")
	}
	ft := firstOf(strings.TrimSpace(in.FreezeType), "Both")
	if !contains(FreezeTypes, ft) {
		return KYCDefCase{}, invalid("the freeze CDSL asks for is Debit, Credit or Both")
	}
	n, err := o.next(ctx, tenant, "kycdef", actor)
	if err != nil {
		return KYCDefCase{}, err
	}
	k := KYCDefCase{ID: fmt.Sprintf("KD-%05d", n), UCC: c.UCC, BOID: b.BOID, CDSLRef: strings.TrimSpace(in.CDSLRef), Received: rec, Deficiency: strings.TrimSpace(in.Deficiency), FreezeType: ft, By: actor}
	k.Status = k.stage(today)
	if err := docs.Upsert(ctx, o.Docs, tenant, cB6DP, "kycdef|"+k.ID, k, actor); err != nil {
		return k, err
	}
	o.kycDefWork(ctx, tenant, k, today)
	o.log(ctx, tenant, actor, "dp.kyc_deficiency", k.ID, map[string]any{"ucc": c.UCC, "bo": cdslMaskBO(b.BOID), "cdsl_ref": k.CDSLRef})
	return k, nil
}

// KYCDefStep records a step: letter, email, phone (with the dispatch
// proof, e-mail log or call note), rectified, freeze (raises the reason-20
// freeze request once the 30 days have run), informed (CDSL told), ack
// (the acknowledgement sent).
func (o *Ops) KYCDefStep(ctx context.Context, tenant, actor, id, step, on, ref string) (KYCDefCase, error) {
	if err := o.require(ctx, tenant, actor, "CDSL KYC deficiency cases", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return KYCDefCase{}, err
	}
	k, _, err := docs.Get[KYCDefCase](ctx, o.Docs, tenant, cB6DP, "kycdef|"+id)
	if err != nil {
		return k, err
	}
	today := o.now().In(ist).Format("2006-01-02")
	on = firstOf(strings.TrimSpace(on), today)
	ref = strings.TrimSpace(ref)
	if !validDate(on) || on > today {
		return k, invalid("give the date it was done (not in the future)")
	}
	if step != "freeze" && ref == "" {
		return k, invalid("give the reference: dispatch proof, e-mail log, call note or CDSL's acknowledgement")
	}
	if k.stage(today) == "Done" || k.stage(today) == "Rectified" {
		return k, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(k.stage(today)))
	}
	s := b6Step(on, actor, ref)
	switch step {
	case "letter":
		k.Letter = s
	case "email":
		k.Email = s
	case "phone":
		k.Phone = s
	case "rectified":
		if k.FreezeReq != "" {
			return k, fmt.Errorf("%w: the freeze was requested (%s): unfreeze it with a DP request once rectified", ErrState, k.FreezeReq)
		}
		k.Rectified = s
	case "freeze":
		if k.Letter == nil || k.Email == nil || k.Phone == nil {
			return k, fmt.Errorf("%w: tell the BO by letter, e-mail and phone first", ErrState)
		}
		if today <= k.Due {
			return k, fmt.Errorf("%w: the BO has until %s to rectify", ErrState, fmtDay(k.Due))
		}
		if k.FreezeReq != "" {
			return k, fmt.Errorf("%w: freeze request %s is raised", ErrState, k.FreezeReq)
		}
		r, err := o.RaiseDPRequest(ctx, tenant, actor, DPRequest{Kind: "Freeze", UCC: k.UCC, BOID: k.BOID, Detail: map[string]string{"type": k.FreezeType, "by": "DP", "reason_code": cdslReasonKYCDef,
			"reason": "KYC deficiency not rectified in 30 days (CDSL " + k.CDSLRef + "; reason code 20, CDSL DPOI 13.5.3)"}})
		if err != nil {
			return k, err
		}
		k.FreezeReq = r.ID
	case "informed":
		if k.FreezeReq == "" {
			return k, fmt.Errorf("%w: raise the freeze first", ErrState)
		}
		k.Informed = s
	case "ack":
		if k.FreezeReq == "" {
			return k, fmt.Errorf("%w: raise the freeze first", ErrState)
		}
		k.AckSent = s
	default:
		return k, invalid("step must be letter, email, phone, rectified, freeze, informed or ack")
	}
	if k.Letter != nil && k.Email != nil && k.Phone != nil {
		last := max(k.Letter.On, k.Email.On, k.Phone.On)
		k.Due = addDays(last, kycDefDays)
	}
	k.Status = k.stage(today)
	if err := docs.Upsert(ctx, o.Docs, tenant, cB6DP, "kycdef|"+k.ID, k, actor); err != nil {
		return k, err
	}
	o.kycDefWork(ctx, tenant, k, today)
	o.log(ctx, tenant, actor, "dp.kyc_deficiency_"+step, k.ID, map[string]any{"ref": ref, "on": on, "stage": k.Status})
	return k, nil
}

func (o *Ops) kycDefWork(ctx context.Context, tenant string, k KYCDefCase, today string) {
	key := "kycdef:" + k.ID
	who := k.UCC + " (" + cdslMaskBO(k.BOID) + ")"
	switch st := k.stage(today); st {
	case "Tell the BO":
		o.raiseWork(ctx, tenant, wB6DP, key, "Tell "+who+" of the KYC deficiency CDSL reported",
			"By letter, e-mail and phone (keep the dispatch proof and the e-mail log); the BO then has 30 days to rectify (CDSL DPOI 13.5.3).", k.UCC, RoleMaker, "dp", k.Received)
	case "Freeze (reason 20)":
		o.raiseWork(ctx, tenant, wB6DP, key+":freeze", "Freeze "+who+" under reason 20: KYC not rectified",
			"The 30 days ran out on "+fmtDay(k.Due)+". Raise the freeze, set it in CDAS, inform CDSL and send the acknowledgement.", k.UCC, RoleMaker, "dp", k.Due)
	case "Rectified", "Done":
		o.closeWork(ctx, tenant, SystemActor, wB6DP, key, st)
		o.closeWork(ctx, tenant, SystemActor, wB6DP, key+":freeze", st)
	default:
		o.closeWork(ctx, tenant, SystemActor, wB6DP, key, st)
	}
}

// ---- DP-0299: transfer-cum-closure requests clients start with an OTP ----

// OTPClosure is a client's request waiting in CDAS.
type OTPClosure struct {
	ID       string          `json:"id"`
	UCC      string          `json:"ucc"`
	BOID     string          `json:"bo_id"`
	Target   string          `json:"target_bo_id"`
	CDSLRef  string          `json:"cdsl_ref,omitempty"`
	Received string          `json:"received"`
	Due      string          `json:"due"`
	Checks   map[string]bool `json:"checks,omitempty"` // bo, target, holdings
	Decision string          `json:"decision,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	Maker    string          `json:"maker"`
	Checker  string          `json:"checker,omitempty"`
	At       string          `json:"decided_at,omitempty"`
	Status   string          `json:"status"` // Pending, Approved, Rejected
}

// OTPClosureChecks name the checks before deciding.
var OTPClosureChecks = map[string]string{"bo": "The BO ID is the client's", "target": "The target account is valid and open", "holdings": "The holdings to transfer agree with CDAS"}

// RecordOTPClosure enters a request seen in CDAS.
func (o *Ops) RecordOTPClosure(ctx context.Context, tenant, actor, ucc, boid, target, cdslRef, received string) (OTPClosure, error) {
	if err := o.require(ctx, tenant, actor, "OTP transfer-cum-closure requests", RoleMaker, RoleChecker); err != nil {
		return OTPClosure{}, err
	}
	c, err := o.Client(ctx, tenant, strings.ToUpper(strings.TrimSpace(ucc)))
	if err != nil {
		return OTPClosure{}, invalid("client %s not found", ucc)
	}
	b := o.boForRecord(ctx, tenant, c, boid)
	target = normDematID(target)
	if b.BOID == "" || len(target) != 16 {
		return OTPClosure{}, invalid("give the client's demat account and the 16-digit target BO ID")
	}
	today := o.now().In(ist).Format("2006-01-02")
	received = firstOf(strings.TrimSpace(received), today)
	if !validDate(received) || received > today {
		return OTPClosure{}, invalid("give the date CDAS received it (not in the future)")
	}
	set, _ := o.Settings(ctx, tenant)
	n, err := o.next(ctx, tenant, "otpclose", actor)
	if err != nil {
		return OTPClosure{}, err
	}
	r := OTPClosure{ID: fmt.Sprintf("OC-%05d", n), UCC: c.UCC, BOID: b.BOID, Target: target, CDSLRef: strings.TrimSpace(cdslRef), Received: received,
		Due: addWorkingDays(set, received, otpCloseWD), Checks: map[string]bool{}, Maker: actor, Status: "Pending"}
	if err := docs.Upsert(ctx, o.Docs, tenant, cB6DP, "otpclose|"+r.ID, r, actor); err != nil {
		return r, err
	}
	o.workDue(ctx, tenant, wB6DP, "otp:"+r.ID, "Approve or reject "+c.UCC+"'s OTP transfer-cum-closure in CDAS by "+fmtDay(r.Due),
		"Check the BO ID, the target account and the holdings, then approve or reject the request in CDAS within 2 working days of receipt (CDSL/OPS/DP/POLCY/2026/656) and record it on Depository → Demat accounts & DIS.",
		c.UCC, RoleChecker, "dp", received, r.Due+" 17:00")
	o.log(ctx, tenant, actor, "dp.otp_closure_received", r.ID, map[string]any{"ucc": c.UCC, "bo": cdslMaskBO(b.BOID), "target": cdslMaskBO(target), "received": received})
	return r, nil
}

// DecideOTPClosure records the checker's decision (as made in CDAS): the
// three checks confirmed to approve, a reason to reject. A closure the
// account cannot take (a pending demat, a freeze, a pledge) is rejected.
func (o *Ops) DecideOTPClosure(ctx context.Context, tenant, actor, id string, approve bool, checks map[string]bool, reason string) (OTPClosure, error) {
	if err := o.require(ctx, tenant, actor, "Deciding OTP transfer-cum-closure requests", RoleChecker); err != nil {
		return OTPClosure{}, err
	}
	r, _, err := docs.Get[OTPClosure](ctx, o.Docs, tenant, cB6DP, "otpclose|"+id)
	if err != nil {
		return r, err
	}
	if r.Status != "Pending" {
		return r, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(r.Status))
	}
	if r.Maker == actor {
		return r, fmt.Errorf("%w: the person who entered the request does not decide it", ErrForbidden)
	}
	reason = strings.TrimSpace(reason)
	if approve {
		for k, l := range OTPClosureChecks {
			if !checks[k] {
				return r, invalid("confirm: %s", strings.ToLower(l))
			}
		}
		c, err := o.Client(ctx, tenant, r.UCC)
		if err != nil {
			return r, err
		}
		if why := o.closureBlock(ctx, tenant, c, o.boFor(ctx, tenant, c, r.BOID)); why != "" {
			return r, fmt.Errorf("%w: %s; reject the request in CDAS with this reason", ErrState, why)
		}
		r.Status = "Approved"
	} else {
		if len(reason) < 4 {
			return r, invalid("give the reason for rejecting it")
		}
		r.Status = "Rejected"
	}
	r.Checks, r.Reason, r.Checker, r.At, r.Decision = checks, reason, actor, o.now().Format(time.RFC3339), r.Status
	if err := docs.Upsert(ctx, o.Docs, tenant, cB6DP, "otpclose|"+r.ID, r, actor); err != nil {
		return r, err
	}
	o.closeWork(ctx, tenant, actor, wB6DP, "otp:"+r.ID, r.Status)
	o.log(ctx, tenant, actor, "dp.otp_closure_"+strings.ToLower(r.Status), r.ID, map[string]any{"ucc": r.UCC, "reason": reason, "late": o.now().In(ist).Format("2006-01-02") > r.Due})
	return r, nil
}

// ---- DP-0305: accounts frozen under reason 27 whose KYC is now validated ----

// KYCUnfreezeRow is a CDSL account frozen under 27 with every holder's KYC
// now validated by a KRA.
type KYCUnfreezeRow struct {
	UCC     string `json:"ucc"`
	Name    string `json:"name"`
	BOID    string `json:"bo_id"`
	Since   string `json:"since,omitempty"`
	Request string `json:"request,omitempty"` // the unfreeze request raised
}

func (o *Ops) kycUnfreezeReady(ctx context.Context, tenant, date string) []KYCUnfreezeRow {
	cs, err := o.ClientMap(ctx, tenant)
	if err != nil {
		return nil
	}
	byPAN := map[string]Client{}
	for _, c := range cs {
		if c.PAN != "" {
			byPAN[strings.ToUpper(c.PAN)] = c
		}
	}
	rs, _ := docs.List[DPRequest](ctx, o.Docs, tenant, cDPReq, "")
	asked := map[string]string{}
	for _, r := range rs {
		if r.Kind == "Unfreeze" && r.Status != "Rejected" && cdslCodeIs(r.Detail["reason_code"], cdslReasonKYC) {
			asked[normDematID(r.BOID)] = r.ID
		}
	}
	var out []KYCUnfreezeRow
	for _, b := range o.boAccounts(ctx, tenant) {
		c, ok := cs[b.UCC]
		if !ok || b.BOID == "" || b.Status == "Closed" || !strings.EqualFold(firstOf(b.Depository, dematDepository(b.BOID)), "CDSL") {
			continue
		}
		since := ""
		for _, f := range o.cdslFreezesOf(ctx, tenant, b.BOID) {
			if f.ISIN == "" && f.blocking(date) && cdslCodeIs(f.ReasonCode, cdslReasonKYC) {
				since = firstOf(f.ActiveFrom, f.InitAt)
			}
		}
		if since == "" && !(b.Status == "Frozen" && strings.Contains(b.FreezeWhy, "reason 27")) {
			continue
		}
		good := kraVerified(c.KRAStatus)
		for _, j := range b.JointHolders {
			good = good && kraVerified(holderKRA(byPAN, j.PAN, j.KRAStatus))
		}
		if !good {
			continue
		}
		out = append(out, KYCUnfreezeRow{UCC: b.UCC, Name: c.Name, BOID: b.BOID, Since: since, Request: asked[normDematID(b.BOID)]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UCC+out[i].BOID < out[j].UCC+out[j].BOID })
	return out
}

// RaiseKYCUnfreezes raises an unfreeze request (reason 27) for each account
// whose holders' KYC the KRA has validated since the freeze.
func (o *Ops) RaiseKYCUnfreezes(ctx context.Context, tenant, actor string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Raising KYC freeze requests", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return 0, err
	}
	n := 0
	for _, a := range o.kycUnfreezeReady(ctx, tenant, o.now().In(ist).Format("2006-01-02")) {
		if a.Request != "" {
			continue
		}
		if _, err := o.RaiseDPRequest(ctx, tenant, actor, DPRequest{Kind: "Unfreeze", UCC: a.UCC, BOID: a.BOID, Detail: map[string]string{"reason_code": cdslReasonKYC,
			"reason": "KYC validated by the KRA for every holder: unfreeze the reason-27 freeze by CDSL's prescribed process (CDSL/OPS/DP/SYSTM/2023/525)"}}); err != nil {
			return n, err
		}
		n++
	}
	o.log(ctx, tenant, actor, "kycgate.unfreeze_requests", "", map[string]any{"raised": n})
	return n, nil
}

// ---- DP-0040: the signed opt-out declaration ----

// NominationDeclReq is a signed nomination opt-out declaration received
// offline.
type NominationDeclReq struct {
	On       string `json:"on"`
	Mode     string `json:"mode"`
	Ref      string `json:"ref"` // the document's reference (scanned copy, register)
	Witness1 string `json:"witness_1"`
	Witness2 string `json:"witness_2"`
}

// RecordOptOutDeclaration records a client's signed declaration not to
// nominate, so the demat account can be opened.
func (o *Ops) RecordOptOutDeclaration(ctx context.Context, tenant, actor, ucc string, in NominationDeclReq) (Client, error) {
	if err := o.require(ctx, tenant, actor, "Recording a nomination declaration", RoleMaker, RoleChecker); err != nil {
		return Client{}, err
	}
	today := o.now().In(ist).Format("2006-01-02")
	on := firstOf(strings.TrimSpace(in.On), today)
	if !validDate(on) || on > today {
		return Client{}, invalid("give the date the declaration was signed (not in the future)")
	}
	if !contains(NominationModes, in.Mode) {
		return Client{}, invalid("the declaration is made %s", strings.Join(NominationModes, ", "))
	}
	if in.Mode == modeThumb && (strings.TrimSpace(in.Witness1) == "" || strings.TrimSpace(in.Witness2) == "") {
		return Client{}, invalid("a thumb impression needs two witnesses")
	}
	if strings.TrimSpace(in.Ref) == "" {
		return Client{}, invalid("give the reference of the signed declaration kept")
	}
	var prev string
	c, err := docs.Update(ctx, o.Docs, tenant, cClient, strings.ToUpper(ucc), actor, func(c *Client) error {
		if len(c.Nominees) > 0 {
			return fmt.Errorf("%w: %s has nominees on record; a change of nomination goes by a change request", ErrState, c.UCC)
		}
		prev = c.Nominee
		c.Nominee, c.NomineeName, c.NominationOn, c.NominationMode = "Opted out", "", on, in.Mode
		return nil
	})
	if err != nil {
		return c, err
	}
	if _, err := o.registerNomination(ctx, tenant, actor, c, prev, strings.TrimSpace(in.Ref)); err != nil {
		return c, err
	}
	o.log(ctx, tenant, actor, "nomination.optout_declared", c.UCC, map[string]any{"on": on, "mode": in.Mode, "ref": in.Ref})
	return c, nil
}

// noNominationChoice: the client has neither a nomination nor a declared
// opt-out (a client added offline with the old default).
func noNominationChoice(c Client) string {
	switch c.NominationState() {
	case "No declaration":
		return "No nomination and no signed opt-out declaration on record: record the client's nomination, or the signed declaration (Clients → the client → Nomination), before the demat account is opened (SEBI Master Circular for Depositories 1.14.2-1.14.3)"
	case "Nominee details missing":
		return "The client chose to nominate but the nominees' details are not on record"
	}
	return ""
}

// ---- SEBI-SB1-0150: the PoA / DDPI document's terms ----

// PoATerms are the terms checked on a PoA or DDPI document (SEBI stock
// broker master circular 2025/90, 36.8.1.1-36.8.1.6).
var PoATerms = []struct{ Key, Label string }{
	{"accounts", "It names the demat accounts it operates"},
	{"destinations", "Its destination accounts are only the client's own or the broker's settlement accounts"},
	{"entity", "It is in the name of the registered stock broker, not of an employee"},
	{"assign", "It cannot be assigned"},
	{"stamped", "It is duly stamped"},
	{"returns", "It requires securities or money received in error to be returned"},
}

// PoACheck is one account's check of its PoA or DDPI document.
type PoACheck struct {
	BOID      string          `json:"bo_id"`
	UCC       string          `json:"ucc"`
	Terms     map[string]bool `json:"terms"`
	Ref       string          `json:"ref"` // the document's reference
	Note      string          `json:"note,omitempty"`
	Maker     string          `json:"maker"`
	MadeAt    string          `json:"made_at"`
	Checker   string          `json:"checker,omitempty"`
	CheckedAt string          `json:"checked_at,omitempty"`
	Status    string          `json:"status"` // To check, Approved, Not in order
}

func poaPass(t map[string]bool) bool {
	for _, x := range PoATerms {
		if !t[x.Key] {
			return false
		}
	}
	return true
}

// RecordPoACheck: a maker ticks the terms the document meets.
func (o *Ops) RecordPoACheck(ctx context.Context, tenant, actor, ucc, boid, ref, note string, terms map[string]bool) (PoACheck, error) {
	if err := o.require(ctx, tenant, actor, "Checking PoA and DDPI documents", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return PoACheck{}, err
	}
	c, err := o.Client(ctx, tenant, strings.ToUpper(strings.TrimSpace(ucc)))
	if err != nil {
		return PoACheck{}, invalid("client %s not found", ucc)
	}
	b := o.boForRecord(ctx, tenant, c, boid)
	if b.BOID == "" || strings.TrimSpace(ref) == "" {
		return PoACheck{}, invalid("give the demat account and the document's reference")
	}
	p := PoACheck{BOID: b.BOID, UCC: c.UCC, Terms: map[string]bool{}, Ref: strings.TrimSpace(ref), Note: strings.TrimSpace(note), Maker: actor, MadeAt: o.now().Format(time.RFC3339), Status: "To check"}
	for _, x := range PoATerms {
		p.Terms[x.Key] = terms[x.Key]
	}
	if !poaPass(p.Terms) && len(p.Note) < 4 {
		return p, invalid("a term the document does not meet needs a note of what is done about it (a fresh document from the client)")
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cB6DP, "poa|"+b.BOID, p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "dp.poa_terms_checked", b.BOID, map[string]any{"ucc": c.UCC, "pass": poaPass(p.Terms)})
	return p, nil
}

// ApprovePoACheck: a checker who did not tick the terms confirms them.
func (o *Ops) ApprovePoACheck(ctx context.Context, tenant, actor, boid string) (PoACheck, error) {
	if err := o.require(ctx, tenant, actor, "Approving PoA and DDPI checks", RoleChecker, RoleCompliance); err != nil {
		return PoACheck{}, err
	}
	p, _, err := docs.Get[PoACheck](ctx, o.Docs, tenant, cB6DP, "poa|"+normDematID(boid))
	if err != nil {
		return p, err
	}
	if p.Status != "To check" {
		return p, fmt.Errorf("%w: the check is %s", ErrState, strings.ToLower(p.Status))
	}
	if p.Maker == actor {
		return p, fmt.Errorf("%w: the person who ticked the terms does not approve them", ErrForbidden)
	}
	p.Checker, p.CheckedAt = actor, o.now().Format(time.RFC3339)
	p.Status = map[bool]string{true: "Approved", false: "Not in order"}[poaPass(p.Terms)]
	if err := docs.Upsert(ctx, o.Docs, tenant, cB6DP, "poa|"+p.BOID, p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "dp.poa_terms_approved", p.BOID, map[string]any{"status": p.Status})
	return p, nil
}

// poaAccounts are the DP's open accounts with a PoA or DDPI.
func (o *Ops) poaAccounts(ctx context.Context, tenant string) []BOAccount {
	var out []BOAccount
	for _, b := range o.boAccounts(ctx, tenant) {
		if b.BOID == "" || b.Status == "Closed" || b.Status == "Not opened" {
			continue
		}
		if b.DDPI || (b.CDSL != nil && strings.TrimSpace(b.CDSL.POAType) != "") {
			out = append(out, b)
		}
	}
	return out
}

// ---- DP-0335: the joint-holder freeze clause ----

// roJointClause is the clause given to the holders of a joint account
// (CDSL DP2025-688, under the Freezing/Defreezing head of the Rights and
// Obligations, DPOI Annexure 2.7).
const roJointClause = "For a joint account: if a statutory authority orders the freeze of the account of any one holder, the whole joint account is frozen. The other holders must get a specific order from that authority to release their share."

// b6PracticeDefs are kept with the tax and compliance questions
// (compregs.go), so the go-live checklist counts them there.
var b6PracticeDefs = []practiceDef{
	tcQ("ro-joint-freeze", "Joint holders: statutory freeze clause", "Does the firm's Rights and Obligations document, under Freezing/Defreezing, say that a statutory freeze on one joint holder freezes the whole account?",
		"CDSL communique DP2025-688: the clause is added to the Rights and Obligations (DPOI Annexure 2.7) and given to the holders of a joint account at opening.",
		"Ledgerline adds the clause to the Rights and Obligations e-mail to a client with a joint account.",
		[]PracticeOption{fsOpt("included", "Yes: the document has the clause", ""), fsOpt("notyet", "Not yet", "")},
		PracticeField{Key: "version", Label: "Version or date of the document with the clause"}),
}

func init() {
	practiceDefs = append(practiceDefs, b6PracticeDefs...)
	tcPracticeDefs = append(tcPracticeDefs, b6PracticeDefs...)
}

// ---- the daily run ----

// dpB6Daily raises the round-6 DP work: AMC refunds, certificate checks,
// KYC deficiency deadlines, OTP closures past their due date, and
// accounts ready to unfreeze.
func (o *Ops) dpB6Daily(ctx context.Context, tenant, day string) {
	if !o.b6Need(ctx, tenant, "dp") {
		return
	}
	o.dpAMCRefundScan(ctx, tenant, day)
	ks, _ := docs.List[KYCDefCase](ctx, o.Docs, tenant, cB6DP, "kycdef|")
	for _, k := range ks {
		if st := k.stage(day); st != k.Status {
			k.Status = st
			_ = docs.Upsert(ctx, o.Docs, tenant, cB6DP, "kycdef|"+k.ID, k, SystemActor)
		}
		o.kycDefWork(ctx, tenant, k, day)
	}
	if o.b6Need(ctx, tenant, "cdsl") {
		for _, a := range o.kycUnfreezeReady(ctx, tenant, day) {
			key := "unfreeze27:" + a.BOID
			if a.Request != "" {
				o.closeWork(ctx, tenant, SystemActor, wB6DP, key, a.Request)
				continue
			}
			o.raiseWork(ctx, tenant, wB6DP, key, "Unfreeze "+a.UCC+": the KRA has validated the KYC",
				"The account is frozen under reason 27 and every holder's KYC is now KRA-validated. Raise the unfreeze on Compliance → KYC trading gate and follow CDSL's prescribed unfreeze process (CDSL/OPS/DP/SYSTM/2023/525).", a.UCC, RoleMaker, "dp", day)
		}
	}
	cases, _ := docs.List[DemiseCase](ctx, o.Docs, tenant, cDemiseCase, "")
	for _, d := range cases {
		di, _, _ := docs.GetOr(ctx, o.Docs, tenant, cB6DP, "intim|"+d.ID, DeathIntimation{})
		o.deathCertWork(ctx, tenant, d, di)
	}
}

// ---- the panel ----

// DPB6View is the round-6 DP work for the screen.
type DPB6View struct {
	AMC        []DPAMCRefund     `json:"amc_refunds"`
	KYCDef     []KYCDefCase      `json:"kyc_deficiencies"`
	OTP        []OTPClosure      `json:"otp_closures"`
	Unfreeze   []KYCUnfreezeRow  `json:"unfreeze_ready"`
	PoA        []PoACheck        `json:"poa_checks"`
	PoAToCheck []string          `json:"poa_to_check"` // BO IDs with a PoA or DDPI and no approved check
	NoChoice   []string          `json:"no_nomination_choice"`
	Intims     []DeathIntimation `json:"death_intimations"`
	Terms      []struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	} `json:"poa_terms"`
	Checks map[string]string `json:"otp_checks"`
	DP     string            `json:"dp"` // Yes, No, Not known
}

// DPB6Panel lists the round-6 DP work.
func (o *Ops) DPB6Panel(ctx context.Context, tenant, actor string) (DPB6View, error) {
	v := DPB6View{AMC: []DPAMCRefund{}, KYCDef: []KYCDefCase{}, OTP: []OTPClosure{}, Unfreeze: []KYCUnfreezeRow{}, PoA: []PoACheck{}, PoAToCheck: []string{}, NoChoice: []string{}, Intims: []DeathIntimation{}, Checks: OTPClosureChecks}
	if err := o.require(ctx, tenant, actor, "Viewing DP controls", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return v, err
	}
	for _, x := range PoATerms {
		v.Terms = append(v.Terms, struct {
			Key   string `json:"key"`
			Label string `json:"label"`
		}{x.Key, x.Label})
	}
	t, _ := dutyNeed("dp", o.firmFacts(ctx, tenant))
	v.DP = map[tri]string{triYes: "Yes", triNo: "No", triUnknown: "Not known"}[t]
	day := o.now().In(ist).Format("2006-01-02")
	amc, _ := docs.List[DPAMCRefund](ctx, o.Docs, tenant, cB6DP, "amc|")
	v.AMC = append(v.AMC, amc...)
	ks, _ := docs.List[KYCDefCase](ctx, o.Docs, tenant, cB6DP, "kycdef|")
	for _, k := range ks {
		k.Status = k.stage(day)
		v.KYCDef = append(v.KYCDef, k)
	}
	oc, _ := docs.List[OTPClosure](ctx, o.Docs, tenant, cB6DP, "otpclose|")
	v.OTP = append(v.OTP, oc...)
	v.Unfreeze = append(v.Unfreeze, o.kycUnfreezeReady(ctx, tenant, day)...)
	ps, _ := docs.List[PoACheck](ctx, o.Docs, tenant, cB6DP, "poa|")
	v.PoA = append(v.PoA, ps...)
	ok := map[string]bool{}
	for _, p := range ps {
		ok[p.BOID] = p.Status == "Approved"
	}
	for _, b := range o.poaAccounts(ctx, tenant) {
		if !ok[b.BOID] {
			v.PoAToCheck = append(v.PoAToCheck, b.UCC+" "+b.BOID)
		}
	}
	is, _ := docs.List[DeathIntimation](ctx, o.Docs, tenant, cB6DP, "intim|")
	v.Intims = append(v.Intims, is...)
	cs, _ := o.Clients(ctx, tenant)
	for _, c := range cs {
		if c.Status != "Closed" && c.Status != "Rejected" && noNominationChoice(c) != "" && len(o.boAccountsOf(ctx, tenant, c.UCC)) > 0 {
			v.NoChoice = append(v.NoChoice, c.UCC)
		}
	}
	return v, nil
}

// roJointLines is the joint-holder freeze clause for a client with a
// joint account at the DP (none otherwise).
func (o *Ops) roJointLines(ctx context.Context, tenant, ucc string) []string {
	for _, b := range o.boAccountsOf(ctx, tenant, ucc) {
		if len(b.JointHolders) > 0 && b.Status != "Closed" {
			return []string{roJointClause}
		}
	}
	return nil
}

// dpAccountsOf are the client's accounts with the DP: the stored BO
// records, else the client's primary account as the DP's record reads it
// (a firm whose BO records were never loaded).
func (o *Ops) dpAccountsOf(ctx context.Context, tenant string, c Client) []BOAccount {
	if bs := o.boAccountsOf(ctx, tenant, c.UCC); len(bs) > 0 {
		return bs
	}
	if b := o.boForRecord(ctx, tenant, c, ""); b.BOID != "" && b.Status != "Not opened" && strings.EqualFold(firstOf(b.Depository, dematDepository(b.BOID)), "CDSL") {
		return []BOAccount{b}
	}
	return nil
}

func (o *Ops) dpAccountsOfUCC(ctx context.Context, tenant, ucc string) []BOAccount {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return nil
	}
	return o.dpAccountsOf(ctx, tenant, c)
}
