package ops

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- allotment, unblocking, demat credit, compensation, commission ----
//
// T+1: the basis of allotment (from the registrar: KFintech, MUFG Intime,
// Bigshare and others publish no API, so it is entered, or loaded as a
// file of application numbers and lots). T+2: blocked money not needed is
// released by 4 pm and shares are credited by 6 pm (SEBI T+3 timetable).
// Ledgerline confirms the credit from the depository where the firm is the
// DP (the CDSL transaction file's IPO credits, or the holding file), and
// the release from the sponsor bank's status (110) or as recorded by staff.
// A release later than T+2 earns the client compensation of ₹100 a day or
// 15% a year of the bid amount, whichever is higher, from the bank that
// caused the delay (SEBI circular of 16 March 2021 as carried in the
// General Information Document; for a cancelled bid, from the day the
// cancellation was placed). Ledgerline works out the amount and tracks
// the claim; if the firm itself pays, it is posted to the client.

// IPOAllotResult says what recording an allotment did.
type IPOAllotResult struct {
	Allotted    int `json:"allotted"`
	NotAllotted int `json:"not_allotted"`
	NoMandate   int `json:"mandate_not_accepted"`
	Unchanged   int `json:"unchanged"`
}

// allottable bids are those the basis of allotment covers.
func allottable(a IPOApp) bool {
	return preAllotment[a.Status] && a.Status != BidDraft || a.Status == BidAllotted || a.Status == BidNotAllotted
}

func (o *Ops) postedAllotment(ctx context.Context, tenant string, a IPOApp) bool {
	_, err := o.Docs.Get(ctx, tenant, cExtTrd, strings.ToUpper(a.UCC)+"|IPO-"+a.ID)
	return err == nil
}

// RecordIPOAllotment records the basis of allotment: lots allotted to each
// bid (bids not listed are not allotted). Lots can be corrected until the
// allotment is posted as a purchase.
func (o *Ops) RecordIPOAllotment(ctx context.Context, tenant, actor, issueID, date string, lots map[string]int64) (IPOAllotResult, error) {
	var res IPOAllotResult
	if err := o.require(ctx, tenant, actor, "Recording allotments", RoleMaker, RoleChecker); err != nil {
		return res, err
	}
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return res, err
	}
	if iss.Type == IssueOFS {
		return res, invalid("record an offer for sale's allocation with its price instead")
	}
	if !validDate(date) || date < iss.Close {
		return res, invalid("give the allotment date (on or after the closing date %s)", iss.Close)
	}
	if iss.IssuePrice.Sign() <= 0 {
		return res, invalid("set the final issue price on the issue first")
	}
	bids, err := o.issueBids(ctx, tenant, issueID)
	if err != nil {
		return res, err
	}
	known := map[string]IPOApp{}
	for _, b := range bids {
		known[b.ID] = b
	}
	for id, n := range lots {
		b, ok := known[id]
		switch {
		case !ok:
			return res, invalid("%s is not a bid in %s", id, iss.Name)
		case n < 0 || n > b.Lots:
			return res, invalid("%s: allotted lots must be between 0 and the %d bid", id, b.Lots)
		case n > 0 && b.Mandate != "Accepted":
			return res, invalid("%s: no money was blocked (mandate %s), so nothing can be allotted", id, strings.ToLower(firstOf(b.Mandate, "not accepted")))
		}
	}
	now := o.now()
	cat := map[string]IPOCategory{}
	for _, c := range iss.Categories {
		cat[c.Code] = c
	}
	for _, b := range bids {
		if !allottable(b) {
			continue
		}
		if (b.Status == BidAllotted || b.Status == BidNotAllotted) && o.postedAllotment(ctx, tenant, b) {
			res.Unchanged++
			continue
		}
		n := lots[b.ID]
		var a IPOApp
		_, err := docs.Update(ctx, o.Docs, tenant, cIPO, b.ID, actor, func(x *IPOApp) error {
			x.normalise()
			x.AllotDate, x.Allotted, x.Shares = date, n, n*iss.LotSize
			x.AllotPrice = iss.IssuePrice.Sub(cat[x.Category].Discount)
			switch {
			case x.Mandate != "Accepted":
				x.Allotment = "Rejected · mandate not accepted"
				x.Status = map[bool]string{true: x.Status, false: BidRejected}[x.Status == BidDeclined || x.Status == BidFailed]
			case n > 0:
				x.Status, x.Allotment = BidAllotted, fmt.Sprintf("Allotted %d lot(s)", n)
			default:
				x.Status, x.Allotment = BidNotAllotted, "Not allotted · block to be released"
			}
			x.event(now, actor, "allotment of %s: %s", date, x.Allotment)
			a = *x
			return nil
		})
		if err != nil {
			return res, err
		}
		switch a.Status {
		case BidAllotted:
			res.Allotted++
		case BidNotAllotted:
			res.NotAllotted++
		default:
			res.NoMandate++
		}
		if a.Mandate == "Accepted" {
			o.notify(ctx, tenant, actor, "ipo.allotment", a.UCC, map[string]string{"issue": iss.Name, "bid": a.ID, "lots": fmt.Sprint(a.Allotted), "shares": fmt.Sprint(a.Shares),
				"release": formatINR(ipoToRelease(iss, a).Dec()), "credit_by": fmtDay(iss.Timeline.Credit)})
		}
	}
	o.log(ctx, tenant, actor, "ipo.allotment_recorded", iss.ID, map[string]any{"date": date, "allotted": res.Allotted, "not_allotted": res.NotAllotted})
	return res, nil
}

// ImportIPOAllotment reads the basis of allotment as a CSV: ApplicationNo
// (the platform's, or Ledgerline's bid number, or the PAN) and
// AllottedLots.
func (o *Ops) ImportIPOAllotment(ctx context.Context, tenant, actor, issueID, date string, data []byte) (IPOAllotResult, error) {
	if err := o.require(ctx, tenant, actor, "Recording allotments", RoleMaker, RoleChecker); err != nil {
		return IPOAllotResult{}, err
	}
	rows, perrs := parseCSV(data, []string{"ApplicationNo", "AllottedLots"})
	if len(perrs) > 0 {
		return IPOAllotResult{}, invalid("line %d: %s", perrs[0].Line, perrs[0].Msg)
	}
	bids, err := o.issueBids(ctx, tenant, issueID)
	if err != nil {
		return IPOAllotResult{}, err
	}
	lots := map[string]int64{}
	for _, r := range rows {
		key := strings.TrimSpace(r.get("ApplicationNo"))
		n, err := parseInt(r.get("AllottedLots"))
		if err != nil || n < 0 {
			return IPOAllotResult{}, invalid("line %d: AllottedLots must be a whole number", r.line)
		}
		id := ""
		for _, b := range bids {
			if b.ID == key || b.AppNo == key && key != "" || strings.EqualFold(b.PAN, key) && b.live() {
				id = b.ID
				break
			}
		}
		if id == "" {
			return IPOAllotResult{}, invalid("line %d: %s is not a bid in this issue placed through Ledgerline", r.line, key)
		}
		lots[id] = n
	}
	return o.RecordIPOAllotment(ctx, tenant, actor, issueID, date, lots)
}

// PostIssueAllotments records an issue's allotted shares as purchases at
// the issue price (for holdings cost and capital gains).
func (o *Ops) PostIssueAllotments(ctx context.Context, tenant, actor, issueID, date string) (IPOAllotmentResult, error) {
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return IPOAllotmentResult{}, err
	}
	if iss.ISIN == "" || iss.IssuePrice.Sign() <= 0 {
		return IPOAllotmentResult{}, invalid("set the issue's ISIN and final issue price first")
	}
	return o.PostIPOAllotments(ctx, tenant, actor, iss.Name, iss.ISIN, iss.Symbol, firstOf(date, iss.Timeline.Allot), iss.IssuePrice, iss.LotSize)
}

// ---- reconciliation ----

// ipoValue is the money an allotment uses.
func ipoValue(a IPOApp) money.Amount {
	if a.Shares <= 0 || a.AllotPrice.Sign() <= 0 {
		return 0
	}
	return a.AllotPrice.MulInt(a.Shares).Round(money.UnitPaisa)
}

// ipoToRelease is the blocked money that must come back.
func ipoToRelease(iss IPOIssue, a IPOApp) money.Amount {
	if a.Mode == ModeMargin || a.Mandate != "Accepted" {
		return 0
	}
	switch a.Status {
	case BidCancelled, BidNotAllotted:
		return a.Amount
	case BidAllotted, BidUnblocked, BidCredited:
		if r := a.Amount - ipoValue(a); r > 0 {
			return r
		}
	}
	_ = iss
	return 0
}

// IPOCompensation is ₹100 a day or 15% a year of base, whichever is
// higher, for days late.
func IPOCompensation(base money.Amount, days int) money.Amount {
	if days <= 0 {
		return 0
	}
	flat := money.Amount(int64(days) * 100_00)
	pct := base.Dec().MulInt(15 * int64(days))
	byRate, _ := pct.Quo(money.DecInt(100 * 365))
	return ipoMax(flat, byRate.Round(money.UnitPaisa))
}

// IPOReconRow is one bid in the allotment reconciliation.
type IPOReconRow struct {
	Bid          string       `json:"bid"`
	UCC          string       `json:"ucc"`
	Name         string       `json:"name"`
	Category     string       `json:"category"`
	Mode         string       `json:"mode"`
	Status       string       `json:"status"`
	AppNo        string       `json:"app_no,omitempty"`
	Amount       money.Amount `json:"amount"`
	Lots         int64        `json:"lots"`
	Allotted     int64        `json:"allotted_lots"`
	Shares       int64        `json:"shares"`
	Value        money.Amount `json:"allotted_value"`
	ToRelease    money.Amount `json:"to_release"`
	UnblockBy    string       `json:"unblock_by,omitempty"`
	UnblockedOn  string       `json:"unblocked_on,omitempty"`
	Unblock      string       `json:"unblock"` // Not needed, Due, Done, Done late, Late
	LateDays     int          `json:"late_days,omitempty"`
	Compensation money.Amount `json:"compensation,omitempty"`
	Comp         *IPOComp     `json:"comp,omitempty"`
	CreditBy     string       `json:"credit_by,omitempty"`
	Credited     int64        `json:"credited"`
	CreditSource string       `json:"credit_source,omitempty"`
	Credit       string       `json:"credit"` // Not applicable, ISIN needed, Due, Credited, Short, Late, Not seen (not our DP)
	Posted       bool         `json:"posted"`
	Obligation   string       `json:"obligation,omitempty"`
	Problems     []string     `json:"problems,omitempty"`
}

// IPORecon is an issue's allotment reconciliation.
type IPORecon struct {
	Issue       IPOIssue      `json:"issue"`
	Today       string        `json:"today"`
	Rows        []IPOReconRow `json:"rows"`
	Bids        int           `json:"bids"`
	Allotted    int           `json:"allotted"`
	Shares      int64         `json:"shares"`
	Credited    int           `json:"credited"`
	CreditLate  int           `json:"credit_late"`
	Unblocked   int           `json:"unblocked"`
	LateRelease int           `json:"unblock_late"`
	CompDue     money.Amount  `json:"compensation_due"`
	Commission  IPOCommission `json:"commission"`
	Posted      int           `json:"posted"`
}

// IPOReconciliation compares each bid's allotment with the demat credit
// and the release of the money blocked.
func (o *Ops) IPOReconciliation(ctx context.Context, tenant, actor, issueID string) (IPORecon, error) {
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return IPORecon{}, err
	}
	bids, err := o.issueBids(ctx, tenant, issueID)
	if err != nil {
		return IPORecon{}, err
	}
	now := o.now()
	v := IPORecon{Issue: iss, Today: now.Format("2006-01-02"), Rows: []IPOReconRow{}}
	credits, src := o.ipoCredits(ctx, tenant, iss)
	ourDP := map[string]bool{}
	if bos, _ := docs.List[BOAccount](ctx, o.Docs, tenant, cBO, ""); len(bos) > 0 {
		for _, b := range bos {
			ourDP[b.UCC] = true
		}
	}
	f := o.ClientScope(ctx, tenant, actor)
	set, _ := o.Settings(ctx, tenant)
	for _, a := range bids {
		if a.Status == BidDraft || f != nil && !f(a.UCC) {
			continue
		}
		r := o.reconRow(ctx, tenant, iss, a, v.Today, credits, src, ourDP)
		v.Rows = append(v.Rows, r)
		v.Bids++
		if r.Shares > 0 {
			v.Allotted++
			v.Shares += r.Shares
		}
		switch r.Credit {
		case "Credited":
			v.Credited++
		case "Late", "Short":
			v.CreditLate++
		}
		switch r.Unblock {
		case "Done":
			v.Unblocked++
		case "Done late", "Late":
			v.LateRelease++
		}
		if r.Comp == nil || r.Comp.Status == "Claimed" {
			v.CompDue += r.Compensation
		}
		if r.Posted {
			v.Posted++
		}
	}
	v.Commission = ipoCommission(iss, bids, set)
	if iss.Commission != nil {
		c := *iss.Commission
		v.Commission.ClaimedOn, v.Commission.ClaimEntry, v.Commission.Received, v.Commission.TDS, v.Commission.Receipts, v.Commission.Ref = c.ClaimedOn, c.ClaimEntry, c.Received, c.TDS, c.Receipts, c.Ref
		if c.ClaimedOn != "" {
			v.Commission.Apps, v.Commission.Value, v.Commission.Amount, v.Commission.GST, v.Commission.Total = c.Apps, c.Value, c.Amount, c.GST, c.Total
		}
	}
	return v, nil
}

// ipoCredits finds each client's credit of the issue's shares: IPO credits
// in the depository's transaction file where the firm is the DP, else the
// holding file.
func (o *Ops) ipoCredits(ctx context.Context, tenant string, iss IPOIssue) (map[string]int64, map[string]string) {
	qty, src := map[string]int64{}, map[string]string{}
	if iss.ISIN == "" {
		return qty, src
	}
	txns, _ := docs.List[DPTxn](ctx, o.Docs, tenant, cDPTxn, "")
	for _, t := range txns {
		if t.ISIN == iss.ISIN && t.Qty > 0 && t.Date >= iss.Close {
			qty[t.UCC] += t.Qty
			src[t.UCC] = "DP transaction " + t.ID + " (" + t.Kind + ", " + t.Date + ")"
		}
	}
	hs, _ := docs.List[Holding](ctx, o.Docs, tenant, cHolding, "")
	for _, h := range hs {
		if h.ISIN == iss.ISIN && h.Qty > qty[h.UCC] && src[h.UCC] == "" {
			qty[h.UCC] = h.Qty
			src[h.UCC] = "demat holding"
		}
	}
	return qty, src
}

func (o *Ops) reconRow(ctx context.Context, tenant string, iss IPOIssue, a IPOApp, today string, credits map[string]int64, src map[string]string, ourDP map[string]bool) IPOReconRow {
	tl := iss.Timeline
	r := IPOReconRow{Bid: a.ID, UCC: a.UCC, Name: a.Name, Category: a.Category, Mode: a.Mode, Status: a.Status, AppNo: a.AppNo, Amount: a.Amount, Lots: a.Lots,
		Allotted: a.Allotted, Shares: a.Shares, Value: ipoValue(a), UnblockedOn: a.UnblockedOn, Comp: a.Comp, Obligation: a.Obligation, CreditBy: tl.Credit}
	if a.Mode == ModeMargin {
		r.Value = money.Amount(0)
		if a.Shares > 0 {
			r.Value = a.AllotPrice.MulInt(a.Shares).Round(money.UnitPaisa)
		}
	}
	r.ToRelease = ipoToRelease(iss, a)
	r.Unblock = "Not needed"
	if r.ToRelease > 0 {
		r.UnblockBy = tl.Unblock
		end := firstOf(a.UnblockedOn, today)
		switch {
		case a.UnblockedOn != "" && a.UnblockedOn <= tl.Unblock:
			r.Unblock = "Done"
		case a.UnblockedOn != "":
			r.Unblock = "Done late"
		case today > tl.Unblock:
			r.Unblock = "Late"
		default:
			r.Unblock = "Due"
		}
		if end > tl.Unblock {
			start := tl.Unblock
			if a.Status == BidCancelled && a.CancelOn != "" {
				start = a.CancelOn // from the day the cancellation was placed
			}
			r.LateDays = daysBetween(start, end)
			r.Compensation = IPOCompensation(a.Amount, r.LateDays)
		}
	} else if a.Mode == ModeMargin && a.UnblockedOn != "" {
		r.Unblock = "Done"
	}
	switch {
	case a.Shares <= 0:
		r.Credit = "Not applicable"
	case iss.ISIN == "":
		r.Credit = "ISIN needed"
	default:
		r.Credited, r.CreditSource = credits[a.UCC], src[a.UCC]
		switch {
		case r.Credited >= a.Shares:
			r.Credit = "Credited"
		case r.Credited > 0:
			r.Credit = "Short"
		case !ourDP[a.UCC] && today > tl.Credit:
			r.Credit = "Not seen (demat with another DP)"
		case today > tl.Credit:
			r.Credit = "Late"
		default:
			r.Credit = "Due"
		}
	}
	if a.Shares > 0 && a.Mode != ModeMargin {
		r.Posted = o.postedAllotment(ctx, tenant, a)
	}
	if a.DPCheck == "Mismatch" {
		r.Problems = append(r.Problems, "depository check failed")
	}
	if a.Status == BidAccepted && today > tl.Allot {
		r.Problems = append(r.Problems, "allotment not recorded")
	}
	if a.Sent != "" {
		r.Problems = append(r.Problems, "no answer from the exchange to the "+strings.ToLower(a.Sent))
	}
	return r
}

// SyncIPOCredits marks allotted bids Credited once the shares are seen in
// the client's demat. It returns how many it marked.
func (o *Ops) SyncIPOCredits(ctx context.Context, tenant, actor, issueID string) (int, error) {
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return 0, err
	}
	if iss.ISIN == "" || iss.Type == IssueOFS {
		return 0, nil
	}
	bids, err := o.issueBids(ctx, tenant, issueID)
	if err != nil {
		return 0, err
	}
	credits, src := o.ipoCredits(ctx, tenant, iss)
	now := o.now()
	n := 0
	for _, b := range bids {
		if b.Shares <= 0 || b.Status == BidCredited || credits[b.UCC] < b.Shares {
			continue
		}
		_, err := docs.Update(ctx, o.Docs, tenant, cIPO, b.ID, actor, func(x *IPOApp) error {
			x.normalise()
			x.Status, x.CreditedOn, x.CreditedQty = BidCredited, now.Format("2006-01-02"), credits[x.UCC]
			x.event(now, actor, "%d share(s) seen in the demat (%s)", credits[x.UCC], src[x.UCC])
			return nil
		})
		if err != nil {
			return n, err
		}
		n++
	}
	if n > 0 {
		o.log(ctx, tenant, actor, "ipo.credits_confirmed", iss.ID, map[string]any{"bids": n})
	}
	return n, nil
}

// RecordIPOUnblock records that the money blocked for a bid was released
// (from the client, the bank or the sponsor bank's report).
func (o *Ops) RecordIPOUnblock(ctx context.Context, tenant, actor, id, date string, amount money.Amount, note string) (IPOApp, error) {
	if err := o.require(ctx, tenant, actor, "Recording IPO bid status", RoleMaker, RoleChecker); err != nil {
		return IPOApp{}, err
	}
	if !validDate(date) || date > o.now().Format("2006-01-02") {
		return IPOApp{}, invalid("give the date the money was released (not in the future)")
	}
	iss := IPOIssue{}
	a, err := o.ipoBid(ctx, tenant, id)
	if err != nil {
		return a, err
	}
	if a.IssueID != "" {
		iss, _ = o.IPOIssueByID(ctx, tenant, a.IssueID)
	}
	due := ipoToRelease(iss, a)
	if due <= 0 {
		return a, fmt.Errorf("%w: %s has no blocked money to release", ErrState, id)
	}
	if amount <= 0 {
		amount = due
	}
	now := o.now()
	return docs.Update(ctx, o.Docs, tenant, cIPO, id, actor, func(x *IPOApp) error {
		x.normalise()
		x.UnblockedOn, x.Unblocked = date, amount
		if x.Status == BidNotAllotted || x.Status == BidAllotted {
			x.Status = BidUnblocked
		}
		x.event(now, actor, "₹%s released on %s%s", formatINR(amount.Dec()), date, map[bool]string{true: ": " + note, false: ""}[note != ""])
		return nil
	})
}

// RecordIPOCompensation tracks the claim for late-release compensation:
// Claimed (with the bank), Paid, or Waived. When the firm itself pays, the
// amount is credited to the client's ledger.
func (o *Ops) RecordIPOCompensation(ctx context.Context, tenant, actor, id string, c IPOComp) (IPOApp, error) {
	if err := o.require(ctx, tenant, actor, "Recording IPO compensation", RoleChecker, RoleCompliance); err != nil {
		return IPOApp{}, err
	}
	a, err := o.ipoBid(ctx, tenant, id)
	if err != nil {
		return a, err
	}
	switch {
	case c.Status != "Claimed" && c.Status != "Paid" && c.Status != "Waived":
		return a, invalid("compensation is Claimed, Paid or Waived")
	case c.Party != "SCSB" && c.Party != "Sponsor bank" && c.Party != "Firm":
		return a, invalid("say who pays: SCSB, Sponsor bank or Firm")
	case c.Status == "Paid" && c.Paid <= 0:
		return a, invalid("give the amount paid")
	case c.Status == "Waived" && strings.TrimSpace(c.Note) == "":
		return a, invalid("record why the compensation is waived")
	}
	if a.Comp != nil && a.Comp.Status == "Paid" {
		return a, fmt.Errorf("%w: compensation for %s is already paid", ErrState, id)
	}
	today := o.now().Format("2006-01-02")
	c.By = actor
	if c.Status == "Claimed" {
		c.ClaimedOn = firstOf(c.ClaimedOn, today)
	}
	if c.Status == "Paid" {
		c.PaidOn = firstOf(c.PaidOn, today)
		if a.Comp != nil {
			c.ClaimedOn = firstOf(c.ClaimedOn, a.Comp.ClaimedOn)
		}
		if c.Party == "Firm" {
			e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "IPOCOMP:" + a.ID, ValueDate: c.PaidOn, CreatedBy: actor,
				Narration: fmt.Sprintf("Compensation for late release of IPO funds: %s bid %s", a.Issue, a.ID), Source: ledger.SourceRef{Kind: "IPO-COMPENSATION", ID: a.ID},
				Postings: []ledger.Posting{{Account: ExpenseIPOCompensated, Debit: c.Paid}, {Account: ledger.ClientAccount(a.UCC), Credit: c.Paid, Memo: "IPO compensation " + a.ID}}})
			if err != nil {
				return a, err
			}
			c.Entry = e.ID
		}
	}
	now := o.now()
	a, err = docs.Update(ctx, o.Docs, tenant, cIPO, id, actor, func(x *IPOApp) error {
		x.Comp = &c
		x.event(now, actor, "compensation %s (%s)%s", strings.ToLower(c.Status), c.Party, map[bool]string{true: ": ₹" + formatINR(c.Paid.Dec()), false: ""}[c.Paid > 0])
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "ipo.compensation", id, map[string]any{"status": c.Status, "party": c.Party, "paid": c.Paid.String()})
	}
	return a, err
}

// ---- commission from the issuer ----

func ipoCommission(iss IPOIssue, bids []IPOApp, set Settings) IPOCommission {
	var c IPOCommission
	for _, b := range bids {
		if len(iss.CommCats) > 0 && !contains(iss.CommCats, b.Category) {
			continue
		}
		valid := b.Mandate == "Accepted" && b.Status != BidCancelled && b.Status != BidRejected
		allotted := b.Shares > 0 && (b.Status == BidAllotted || b.Status == BidUnblocked || b.Status == BidCredited)
		if iss.CommOn == "Valid" && valid || iss.CommOn != "Valid" && allotted {
			c.Apps++
		}
		if allotted {
			c.Value += ipoValue(b)
		}
	}
	amt := iss.CommPerApp.Dec().MulInt(int64(c.Apps)).Add(c.Value.Dec().Pct(iss.CommPct))
	c.Amount = amt.Round(money.UnitPaisa)
	gst := money.DecInt(18)
	if set.GSTPct != "" {
		if g, err := money.ParseDec(set.GSTPct); err == nil {
			gst = g
		}
	}
	c.GST = c.Amount.Dec().Pct(gst).Round(money.UnitPaisa)
	c.Total = c.Amount + c.GST
	return c
}

// ClaimIPOCommission raises the commission due from the issuer on the
// firm's clients' applications: a receivable, the income and its GST.
func (o *Ops) ClaimIPOCommission(ctx context.Context, tenant, actor, issueID, date, ref string) (IPOIssue, error) {
	if err := o.require(ctx, tenant, actor, "Claiming IPO commission", RoleChecker); err != nil {
		return IPOIssue{}, err
	}
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return iss, err
	}
	if iss.Commission != nil && iss.Commission.ClaimedOn != "" {
		return iss, fmt.Errorf("%w: commission on %s was claimed on %s", ErrState, iss.Name, iss.Commission.ClaimedOn)
	}
	if !validDate(date) {
		return iss, invalid("give the date of the claim")
	}
	bids, _ := o.issueBids(ctx, tenant, issueID)
	set, _ := o.Settings(ctx, tenant)
	c := ipoCommission(iss, bids, set)
	if c.Amount <= 0 {
		return iss, invalid("no commission is due on %s (set the rate on the issue, and record the allotment)", iss.Name)
	}
	ps := []ledger.Posting{{Account: ReceivableIPOComm, Debit: c.Total, Memo: iss.ID}, {Account: IncomeIPOComm, Credit: c.Amount, Memo: iss.Name}}
	if c.GST > 0 {
		ps = append(ps, ledger.Posting{Account: ledger.PayableGST, Credit: c.GST, Memo: "GST on IPO commission " + iss.ID})
	}
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "IPOCOMM:" + iss.ID, ValueDate: date, CreatedBy: actor, Narration: fmt.Sprintf("IPO commission due: %s, %d application(s)", iss.Name, c.Apps),
		Source: ledger.SourceRef{Kind: "IPO-COMMISSION", ID: iss.ID}, Postings: ps})
	if err != nil {
		return iss, err
	}
	c.ClaimedOn, c.ClaimEntry, c.Ref = date, e.ID, strings.TrimSpace(ref)
	if _, err := docs.Update(ctx, o.Docs, tenant, cIPOIssue, iss.ID, actor, func(x *IPOIssue) error { x.Commission = &c; return nil }); err != nil {
		return iss, err
	}
	o.log(ctx, tenant, actor, "ipo.commission_claimed", iss.ID, map[string]any{"applications": c.Apps, "amount": c.Amount.String(), "gst": c.GST.String(), "entry": e.ID})
	return o.IPOIssueByID(ctx, tenant, iss.ID)
}

// ReceiveIPOCommission records commission received in the firm's own bank
// account, with any TDS the payer deducted.
func (o *Ops) ReceiveIPOCommission(ctx context.Context, tenant, actor, issueID, date string, received, tds money.Amount, ref string) (IPOIssue, error) {
	if err := o.require(ctx, tenant, actor, "Claiming IPO commission", RoleChecker); err != nil {
		return IPOIssue{}, err
	}
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return iss, err
	}
	c := iss.Commission
	switch {
	case c == nil || c.ClaimedOn == "":
		return iss, fmt.Errorf("%w: claim the commission first", ErrState)
	case !validDate(date):
		return iss, invalid("give the date the money came in")
	case received <= 0 || tds < 0:
		return iss, invalid("give the amount received (and any TDS deducted)")
	case received+tds > c.Total-c.Received-c.TDS:
		return iss, invalid("₹%s is more than the ₹%s still due", formatINR((received + tds).Dec()), formatINR((c.Total - c.Received - c.TDS).Dec()))
	}
	ps := []ledger.Posting{{Account: ledger.BankOwn, Debit: received, Memo: strings.TrimSpace(ref)}}
	if tds > 0 {
		ps = append(ps, ledger.Posting{Account: ReceivableTDS, Debit: tds, Memo: "TDS on IPO commission " + iss.ID})
	}
	ps = append(ps, ledger.Posting{Account: ReceivableIPOComm, Credit: received + tds, Memo: iss.ID})
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: fmt.Sprintf("IPOCOMMRCV:%s:%d", iss.ID, len(c.Receipts)+1), ValueDate: date, CreatedBy: actor,
		Narration: "IPO commission received: " + iss.Name, Source: ledger.SourceRef{Kind: "IPO-COMMISSION", ID: iss.ID}, Postings: ps})
	if err != nil {
		return iss, err
	}
	if _, err := docs.Update(ctx, o.Docs, tenant, cIPOIssue, iss.ID, actor, func(x *IPOIssue) error {
		x.Commission.Received += received
		x.Commission.TDS += tds
		x.Commission.Receipts = append(x.Commission.Receipts, e.ID)
		return nil
	}); err != nil {
		return iss, err
	}
	o.log(ctx, tenant, actor, "ipo.commission_received", iss.ID, map[string]any{"received": received.String(), "tds": tds.String(), "entry": e.ID})
	return o.IPOIssueByID(ctx, tenant, iss.ID)
}
