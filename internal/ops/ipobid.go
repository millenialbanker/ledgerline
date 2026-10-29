package ops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- bids: capture, checks, modification and cancellation ----

// IPOChannels are the ways a client's prior instruction may come. Staff
// record which one and its reference (call recording, email, form number);
// the client portal records the client's own action.
var IPOChannels = []string{"Recorded phone line", "Email from the registered address", "Written instruction (signed)", "In person (signed)", "SMS or WhatsApp from the registered number"}

const channelPortal = "Client portal"

// IPOInstruction is one instruction in the consent log. It is written once
// and never changed; the acknowledgement number goes to the client.
type IPOInstruction struct {
	ID       string `json:"id"`
	At       string `json:"at"`       // recorded
	Received string `json:"received"` // when the client gave it
	Bid      string `json:"bid"`
	UCC      string `json:"ucc"`
	Name     string `json:"name,omitempty"`
	Issue    string `json:"issue"`
	Action   string `json:"action"` // Place, Modify, Cancel, Re-send mandate
	Channel  string `json:"channel"`
	Ref      string `json:"ref"`
	Text     string `json:"text"`
	By       string `json:"by"`
	Ack      string `json:"ack"`
}

// IPOInstructionIn is how staff record the client's instruction.
type IPOInstructionIn struct {
	Channel  string `json:"channel"`
	Ref      string `json:"ref"`
	Received string `json:"received"` // when the client gave it: 2026-10-29T10:42 or RFC 3339
	Text     string `json:"text"`
}

// IPOBidInput is a new bid.
type IPOBidInput struct {
	Issue       string           `json:"issue"`
	UCC         string           `json:"ucc"`
	Exchange    string           `json:"exchange"`
	Category    string           `json:"category"`
	Options     []IPOBidOption   `json:"options"`
	UPI         string           `json:"upi"`
	OwnUPI      bool             `json:"own_upi"`  // the client declares the UPI ID is on their own bank account
	Declared    bool             `json:"declared"` // the client declared eligibility for a reservation
	Instruction IPOInstructionIn `json:"instruction"`
}

// IPOModifyInput revises a bid.
type IPOModifyInput struct {
	Options     []IPOBidOption   `json:"options"`
	UPI         string           `json:"upi"`
	OwnUPI      bool             `json:"own_upi"`
	Instruction IPOInstructionIn `json:"instruction"`
}

func individualClient(c Client) bool {
	return c.Type == "" || c.Type == "Individual" || c.Type == "HUF" || c.Type == "NRI"
}

// splitDemat gives the depository, DP ID and client ID of a demat account.
func splitDemat(d Demat) (dep, dpid, ben string, err error) {
	id := strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(d.ID))
	switch {
	case len(id) == 16 && strings.HasPrefix(id, "IN"):
		return "NSDL", id[:8], id[8:], nil
	case len(id) == 16 && strings.Trim(id, "0123456789") == "":
		return "CDSL", id[:8], id[8:], nil
	}
	return "", "", "", invalid("the demat account %q is not a 16-character NSDL (IN…) or CDSL BO ID", d.ID)
}

// ---- category, price and amount ----

// priceBid checks the options against the issue and category and returns
// them with the price filled in, the amount blocked (the largest option),
// the smallest option's amount and the most lots.
func (iss IPOIssue) priceBid(cat IPOCategory, in []IPOBidOption) ([]IPOBidOption, money.Amount, money.Amount, int64, error) {
	if len(in) == 0 || len(in) > 3 || iss.Type == IssueOFS && len(in) > 1 {
		return nil, 0, 0, 0, invalid("give one bid option (up to three in a book-built issue)")
	}
	var out []IPOBidOption
	var hi, lo money.Amount
	var lots int64
	for i, op := range in {
		n := i + 1
		switch {
		case op.Lots <= 0:
			return nil, 0, 0, 0, invalid("option %d: give the number of lots", n)
		case cat.ExactLots > 0 && op.Lots != cat.ExactLots:
			return nil, 0, 0, 0, invalid("%s bids are exactly %d lots", cat.Code, cat.ExactLots)
		case cat.MinLots > 0 && op.Lots < cat.MinLots:
			return nil, 0, 0, 0, invalid("%s bids are at least %d lots", cat.Code, cat.MinLots)
		}
		op.Series = strings.TrimSpace(op.Series)
		switch {
		case iss.Type == IssueNCD:
			op.CutOff, op.Price = false, iss.PriceLow
			if len(iss.Series) > 0 && !contains(iss.Series, op.Series) {
				return nil, 0, 0, 0, invalid("option %d: choose a series of the issue (%s)", n, strings.Join(iss.Series, ", "))
			}
		case op.CutOff:
			if !cat.CutOff {
				if iss.Type == IssueSME {
					return nil, 0, 0, 0, invalid("SME issues have no cut-off price: give a price")
				}
				return nil, 0, 0, 0, invalid("cut-off is only for retail, employee, shareholder and policyholder bids: give a price for %s", cat.Code)
			}
			if iss.PriceHigh.Sign() <= 0 {
				return nil, 0, 0, 0, invalid("the cut-off price is not known yet (for an offer for sale it is set after the non-retail day): give a price")
			}
			op.Price = iss.PriceHigh
		default:
			if op.Price.Cmp(iss.PriceLow) < 0 || iss.Type != IssueOFS && op.Price.Cmp(iss.PriceHigh) > 0 {
				if iss.Type == IssueOFS {
					return nil, 0, 0, 0, invalid("option %d: the price must be at least the floor price ₹%s", n, iss.PriceLow)
				}
				return nil, 0, 0, 0, invalid("option %d: the price must be within the band ₹%s to ₹%s", n, iss.PriceLow, iss.PriceHigh)
			}
			if q, err := op.Price.Quo(iss.Tick); err != nil || q.Round(money.UnitRupee).Dec().Cmp(q) != 0 {
				return nil, 0, 0, 0, invalid("option %d: the price moves in steps of ₹%s", n, iss.Tick)
			}
		}
		eff := money.Max(op.Price.Sub(cat.Discount), money.Dec{})
		a := eff.MulInt(op.Lots * iss.LotSize).Round(money.UnitPaisa)
		if i == 0 || a > hi {
			hi = a
		}
		if i == 0 || a < lo {
			lo = a
		}
		lots = max(lots, op.Lots)
		out = append(out, op)
	}
	switch {
	case cat.Upto > 0 && hi > cat.Upto:
		return nil, 0, 0, 0, invalid("%s bids are up to ₹%s; this bid is ₹%s", cat.Code, formatINR(cat.Upto.Dec()), formatINR(hi.Dec()))
	case cat.Above > 0 && lo <= cat.Above:
		return nil, 0, 0, 0, invalid("%s bids are above ₹%s; this bid is ₹%s", cat.Code, formatINR(cat.Above.Dec()), formatINR(lo.Dec()))
	case cat.MinAmount > 0 && lo < cat.MinAmount:
		return nil, 0, 0, 0, invalid("the minimum application in %s is ₹%s", cat.Code, formatINR(cat.MinAmount.Dec()))
	}
	return out, hi, lo, lots, nil
}

// ipoMode is how the money is blocked: UPI for individuals up to ₹5 lakh
// (SEBI circular 2022/45), ASBA through the client's bank above that or
// for others, and the ledger for an offer for sale.
func ipoMode(iss IPOIssue, c Client, amount money.Amount) string {
	switch {
	case iss.Type == IssueOFS:
		return ModeMargin
	case individualClient(c) && amount <= 5*lakh:
		return ModeUPI
	}
	return ModeASBA
}

// captureErr says why a bid cannot be taken now, if it cannot.
func (iss IPOIssue) captureErr(set Settings, cat IPOCategory, now time.Time) error {
	d, hm := now.Format("2006-01-02"), now.Format("15:04")
	switch {
	case iss.Withdrawn:
		return fmt.Errorf("%w: %s has been withdrawn", ErrState, iss.Name)
	case d < firstOf(iss.PreApply, iss.Open):
		return fmt.Errorf("%w: applications for %s open on %s", ErrState, iss.Name, fmtDay(firstOf(iss.PreApply, iss.Open)))
	case iss.closed(now):
		return fmt.Errorf("%w: bidding in %s closed at %s on %s", ErrState, iss.Name, iss.CloseTime, fmtDay(iss.Close))
	}
	if iss.Type == IssueOFS && cat.Day != "" {
		day := iss.Open
		if cat.Day == "T+1" {
			day = iss.Close
		}
		if d > day || d == day && hm >= iss.CloseTime {
			return fmt.Errorf("%w: %s bids in this offer closed on %s", ErrState, cat.Code, fmtDay(day))
		}
		if d < day && cat.Day == "T" {
			return fmt.Errorf("%w: %s bids are taken on %s", ErrState, cat.Code, fmtDay(day))
		}
	}
	_ = set
	return nil
}

// ---- clients and their UPI IDs ----

// ipoClientErr checks the client may bid in the category.
func (o *Ops) ipoClientErr(ctx context.Context, tenant string, c Client, cat IPOCategory, declared bool, date string) error {
	switch {
	case c.Status != "Active":
		return invalid("%s is not an active client", c.UCC)
	case !rePAN.MatchString(c.PAN):
		return invalid("%s has no valid PAN on record", c.UCC)
	case strings.TrimSpace(c.Demat.ID) == "":
		return invalid("%s has no demat account on record: allotted shares are credited only to the bidder's own demat", c.UCC)
	}
	if d, hit := o.IsDebarredPAN(ctx, tenant, c.PAN, date); hit {
		return fmt.Errorf("%w: %s's PAN is debarred from the securities market (%s order %s)", ErrForbidden, c.UCC, d.Regulator, d.OrderNo)
	}
	if _, _, _, err := splitDemat(c.Demat); err != nil {
		return err
	}
	if bo, _, err := docs.Get[BOAccount](ctx, o.Docs, tenant, cBO, c.UCC); err == nil && strings.EqualFold(normDematID(bo.BOID), normDematID(c.Demat.ID)) {
		switch {
		case bo.Status == "Closed":
			return invalid("%s's demat account is closed", c.UCC)
		case bo.Freeze == "Credit" || bo.Freeze == "Both":
			return invalid("%s's demat account is frozen for credits (%s): shares could not be credited", c.UCC, firstOf(bo.FreezeWhy, "frozen"))
		}
	}
	if cat.Individual && !individualClient(c) {
		return invalid("%s is for individuals; %s is a %s account", cat.Code, c.UCC, c.Type)
	}
	if cat.EligibleISIN != "" {
		h, _, _ := docs.Get[Holding](ctx, o.Docs, tenant, cHolding, holdingID(c.UCC, cat.EligibleISIN))
		if h.Qty <= 0 {
			return invalid("%s needs a holding of %s in the client's demat on the record date; none is on record", cat.Code, cat.EligibleISIN)
		}
	} else if cat.Declare != "" && !declared {
		return invalid("record the client's declaration that they are %s", cat.Declare)
	}
	return nil
}

var reVPA = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,255}@[a-z][a-z0-9]{1,63}$`)

func normVPA(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// IPOUPIID is one of a client's UPI IDs for ASBA bids, with how the firm
// knows it is on the client's own bank account.
type IPOUPIID struct {
	VPA      string `json:"vpa"`
	Source   string `json:"source"` // Checked by staff, Declared by the client, Receipt RC-…
	Evidence string `json:"evidence,omitempty"`
	Added    string `json:"added"`
	By       string `json:"by"`
	Removed  string `json:"removed,omitempty"`
}

// IPOUPIs is a client's register of UPI IDs.
type IPOUPIs struct {
	UCC string     `json:"ucc"`
	IDs []IPOUPIID `json:"ids"`
}

// ClientUPIs lists a client's UPI IDs: those recorded for bids and those
// the client has paid the firm from (credited receipts).
func (o *Ops) ClientUPIs(ctx context.Context, tenant, ucc string) ([]IPOUPIID, error) {
	ucc = strings.ToUpper(ucc)
	reg, _, err := docs.GetOr(ctx, o.Docs, tenant, cIPOUPI, ucc, IPOUPIs{UCC: ucc})
	if err != nil {
		return nil, err
	}
	out := []IPOUPIID{}
	seen := map[string]bool{}
	for _, x := range reg.IDs {
		if x.Removed == "" {
			out = append(out, x)
			seen[x.VPA] = true
		}
	}
	rs, _ := docs.List[Receipt](ctx, o.Docs, tenant, cReceipt, "")
	for _, r := range rs {
		v := normVPA(r.FromAccount)
		if r.UCC == ucc && r.Status == "Credited" && strings.Contains(v, "@") && !seen[v] && reVPA.MatchString(v) {
			out = append(out, IPOUPIID{VPA: v, Source: "Receipt " + r.ID, Evidence: "the client paid the firm from it on " + r.Date, Added: r.Date, By: r.Maker})
			seen[v] = true
		}
	}
	return out, nil
}

// upiOwner finds another client who has this UPI ID on record.
func (o *Ops) upiOwner(ctx context.Context, tenant, vpa, ucc, pan string) string {
	regs, _ := docs.List[IPOUPIs](ctx, o.Docs, tenant, cIPOUPI, "")
	cm, _ := o.ClientMap(ctx, tenant)
	for _, r := range regs {
		if r.UCC == ucc {
			continue
		}
		for _, x := range r.IDs {
			if x.VPA == vpa && x.Removed == "" && !strings.EqualFold(cm[r.UCC].PAN, pan) {
				return r.UCC
			}
		}
	}
	return ""
}

// RegisterIPOUPI records a client's UPI ID with the evidence that it is on
// the client's own bank account (a UPI ID on someone else's account is
// rejected by the bank at the mandate).
func (o *Ops) RegisterIPOUPI(ctx context.Context, tenant, actor, ucc, vpa, evidence string) ([]IPOUPIID, error) {
	if err := o.require(ctx, tenant, actor, "Recording clients' UPI IDs", RoleMaker, RoleChecker, RoleBranch, RoleAP); err != nil {
		return nil, err
	}
	if f := o.ClientScope(ctx, tenant, actor); f != nil && !f(ucc) {
		return nil, ErrNotFound
	}
	if strings.TrimSpace(evidence) == "" {
		return nil, invalid("say how the UPI ID was checked to be on the client's own bank account (name check, bank statement, signed form)")
	}
	return o.registerUPI(ctx, tenant, actor, ucc, vpa, "Checked by staff", evidence)
}

func (o *Ops) registerUPI(ctx context.Context, tenant, actor, ucc, vpa, source, evidence string) ([]IPOUPIID, error) {
	ucc, vpa = strings.ToUpper(strings.TrimSpace(ucc)), normVPA(vpa)
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return nil, invalid("client %s not found", ucc)
	}
	if !reVPA.MatchString(vpa) {
		return nil, invalid("%q is not a UPI ID (name@bank)", vpa)
	}
	if other := o.upiOwner(ctx, tenant, vpa, ucc, c.PAN); other != "" {
		return nil, invalid("%s is on record for another client (%s): a third party's UPI ID cannot be used", vpa, other)
	}
	now := o.now().Format(time.RFC3339)
	_, err = docs.Update(ctx, o.Docs, tenant, cIPOUPI, ucc, actor, func(r *IPOUPIs) error {
		for i := range r.IDs {
			if r.IDs[i].VPA == vpa && r.IDs[i].Removed == "" {
				return nil
			}
			_ = i
		}
		r.IDs = append(r.IDs, IPOUPIID{VPA: vpa, Source: source, Evidence: strings.TrimSpace(evidence), Added: now, By: actor})
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		err = docs.Upsert(ctx, o.Docs, tenant, cIPOUPI, ucc, IPOUPIs{UCC: ucc, IDs: []IPOUPIID{{VPA: vpa, Source: source, Evidence: strings.TrimSpace(evidence), Added: now, By: actor}}}, actor)
	}
	if err != nil {
		return nil, err
	}
	o.log(ctx, tenant, actor, "ipo.upi_recorded", ucc, map[string]any{"vpa": vpa, "source": source})
	return o.ClientUPIs(ctx, tenant, ucc)
}

// RemoveIPOUPI stops a UPI ID being used for new bids (it stays on record).
func (o *Ops) RemoveIPOUPI(ctx context.Context, tenant, actor, ucc, vpa string) ([]IPOUPIID, error) {
	if err := o.require(ctx, tenant, actor, "Recording clients' UPI IDs", RoleMaker, RoleChecker, RoleBranch, RoleAP); err != nil {
		return nil, err
	}
	ucc, vpa = strings.ToUpper(ucc), normVPA(vpa)
	if f := o.ClientScope(ctx, tenant, actor); f != nil && !f(ucc) {
		return nil, ErrNotFound
	}
	_, err := docs.Update(ctx, o.Docs, tenant, cIPOUPI, ucc, actor, func(r *IPOUPIs) error {
		for i := range r.IDs {
			if r.IDs[i].VPA == vpa && r.IDs[i].Removed == "" {
				r.IDs[i].Removed = o.now().Format(time.RFC3339)
				return nil
			}
		}
		return invalid("%s is not on record for %s (a UPI ID seen on a receipt cannot be removed)", vpa, ucc)
	})
	if err != nil {
		return nil, err
	}
	o.log(ctx, tenant, actor, "ipo.upi_removed", ucc, map[string]any{"vpa": vpa})
	return o.ClientUPIs(ctx, tenant, ucc)
}

// checkUPI makes sure the UPI ID is the client's own: on record (or, from
// the portal, declared by the client, which records it).
func (o *Ops) checkUPI(ctx context.Context, tenant, actor string, c Client, vpa string, own, byClient bool) (string, error) {
	vpa = normVPA(vpa)
	if vpa == "" {
		return "", invalid("give the client's own UPI ID: individuals bidding up to ₹5 lakh through a broker bid with a UPI mandate")
	}
	if !reVPA.MatchString(vpa) {
		return "", invalid("%q is not a UPI ID (name@bank)", vpa)
	}
	ids, err := o.ClientUPIs(ctx, tenant, c.UCC)
	if err != nil {
		return "", err
	}
	for _, x := range ids {
		if x.VPA == vpa {
			return vpa, nil
		}
	}
	if other := o.upiOwner(ctx, tenant, vpa, c.UCC, c.PAN); other != "" {
		return "", invalid("%s is on record for another client (%s): bids must use the bidder's own UPI ID", vpa, other)
	}
	if byClient && own {
		if _, err := o.registerUPI(ctx, tenant, actor, c.UCC, vpa, "Declared by the client", "client portal declaration: linked to my own bank account"); err != nil {
			return "", err
		}
		return vpa, nil
	}
	if byClient {
		return "", invalid("confirm that %s is linked to your own bank account: a UPI ID on anyone else's account is rejected", vpa)
	}
	return "", invalid("%s is not on record for %s: record it under the client's UPI IDs with how it was checked to be on the client's own bank account", vpa, c.UCC)
}

// panBid finds the PAN's live bid in an issue.
func (o *Ops) panBid(ctx context.Context, tenant string, iss IPOIssue, pan, except string) (IPOApp, bool) {
	all, _ := docs.List[IPOApp](ctx, o.Docs, tenant, cIPO, "")
	var cm map[string]Client
	for _, a := range all {
		a.normalise()
		if a.ID == except || !a.live() || !(a.IssueID == iss.ID || a.IssueID == "" && strings.EqualFold(a.Issue, iss.Name)) {
			continue
		}
		p := a.PAN
		if p == "" {
			if cm == nil {
				cm, _ = o.ClientMap(ctx, tenant)
			}
			p = cm[a.UCC].PAN
		}
		if strings.EqualFold(p, pan) {
			return a, true
		}
	}
	return IPOApp{}, false
}

// ---- the consent log ----

func parseWhen(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(l, s, ist); err == nil {
			return t.In(ist), true
		}
	}
	return time.Time{}, false
}

// instruction checks and writes the client's instruction to the consent log.
func (o *Ops) instruction(ctx context.Context, tenant, actor string, a IPOApp, action string, in IPOInstructionIn, byClient bool, text string) (IPOInstruction, error) {
	now := o.now()
	ins := IPOInstruction{At: now.Format(time.RFC3339), Bid: a.ID, UCC: a.UCC, Name: a.Name, Issue: firstOf(a.IssueID, a.Issue), Action: action, Text: text, By: actor}
	if byClient {
		ins.Channel, ins.Ref, ins.Received = channelPortal, actor, ins.At
	} else {
		ins.Channel, ins.Ref = strings.TrimSpace(in.Channel), strings.TrimSpace(in.Ref)
		if !contains(IPOChannels, ins.Channel) {
			return ins, invalid("record how the client gave the instruction: %s", strings.Join(IPOChannels, ", "))
		}
		if ins.Ref == "" {
			return ins, invalid("give the reference of the client's instruction (call recording ID, email, form number)")
		}
		t, ok := parseWhen(in.Received)
		if !ok {
			return ins, invalid("give when the client gave the instruction (date and time)")
		}
		if t.After(now.Add(time.Minute)) {
			return ins, invalid("the instruction cannot be later than now: a bid needs the client's prior instruction")
		}
		if now.Sub(t) > 7*24*time.Hour {
			return ins, invalid("the instruction is more than a week old; take a fresh one")
		}
		ins.Received = t.Format(time.RFC3339)
		if x := strings.TrimSpace(in.Text); x != "" {
			ins.Text = text + " · client said: " + x
		}
	}
	n, err := o.next(ctx, tenant, "ipoinstr", actor)
	if err != nil {
		return ins, err
	}
	ins.ID = fmt.Sprintf("IPI-%07d", n)
	ins.Ack = fmt.Sprintf("ACK%s%06d", now.Format("060102"), n)
	if _, err := docs.Put(ctx, o.Docs, tenant, cIPOInstr, ins.ID, ins, 0, actor); err != nil {
		return ins, err
	}
	return ins, nil
}

func bidText(iss IPOIssue, a IPOApp) string {
	var ops []string
	for _, op := range a.Options {
		p := "₹" + op.Price.String()
		if op.CutOff {
			p = "cut-off"
		}
		s := fmt.Sprintf("%d lot(s) at %s", op.Lots, p)
		if op.Series != "" {
			s += " series " + op.Series
		}
		ops = append(ops, s)
	}
	pay := "UPI " + a.UPI
	switch a.Mode {
	case ModeASBA:
		pay = "ASBA from the client's own bank account"
	case ModeMargin:
		pay = "margin ₹" + formatINR(a.Margin.Dec()) + " from the ledger"
	}
	return fmt.Sprintf("%s on %s, %s: %s; ₹%s; %s", iss.Name, a.Exchange, a.Category, strings.Join(ops, ", "), formatINR(a.Amount.Dec()), pay)
}

// acknowledge sends the client the acknowledgement of an instruction.
func (o *Ops) acknowledge(ctx context.Context, tenant, actor string, a IPOApp, ins IPOInstruction, iss IPOIssue) {
	o.notify(ctx, tenant, actor, "ipo.bid_ack", a.UCC, map[string]string{"ack": ins.Ack, "action": strings.ToLower(ins.Action), "issue": iss.Name, "bid": a.ID,
		"lots": fmt.Sprint(a.Lots), "amount": formatINR(a.Amount.Dec()), "upi": a.UPI, "close": fmtDay(iss.Close) + " " + iss.CloseTime})
}

// IPOInstructions is the consent log, newest first, optionally for one
// client or bid.
func (o *Ops) IPOInstructions(ctx context.Context, tenant, actor, ucc, bid, from, to string) ([]IPOInstruction, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the IPO consent log", RoleMaker, RoleChecker, RoleCompliance, RoleBranch, RoleAP); err != nil {
		return nil, err
	}
	all, err := docs.List[IPOInstruction](ctx, o.Docs, tenant, cIPOInstr, "")
	if err != nil {
		return nil, err
	}
	f := o.ClientScope(ctx, tenant, actor)
	out := []IPOInstruction{}
	for i := len(all) - 1; i >= 0; i-- {
		x := all[i]
		d := x.At[:min(10, len(x.At))]
		if ucc != "" && !strings.EqualFold(x.UCC, ucc) || bid != "" && x.Bid != bid || from != "" && d < from || to != "" && d > to || f != nil && !f(x.UCC) {
			continue
		}
		out = append(out, x)
	}
	return out, nil
}

// IPOInstructionsCSV is the consent log as a file, for inspection.
func (o *Ops) IPOInstructionsCSV(ctx context.Context, tenant, actor, from, to string) ([]byte, error) {
	xs, err := o.IPOInstructions(ctx, tenant, actor, "", "", from, to)
	if err != nil {
		return nil, err
	}
	rows := [][]string{{"Instruction", "Recorded", "Received", "Bid", "UCC", "Name", "Issue", "Action", "Channel", "Reference", "Instruction text", "By", "Acknowledgement"}}
	for i := len(xs) - 1; i >= 0; i-- {
		x := xs[i]
		rows = append(rows, []string{x.ID, x.At, x.Received, x.Bid, x.UCC, x.Name, x.Issue, x.Action, x.Channel, x.Ref, x.Text, x.By, x.Ack})
	}
	return CSV(rows), nil
}

// ---- placing a bid ----

// PlaceIPOBid takes a bid for a client on the client's prior instruction.
func (o *Ops) PlaceIPOBid(ctx context.Context, tenant, actor string, in IPOBidInput) (IPOApp, error) {
	if err := o.require(ctx, tenant, actor, "Placing IPO bids for clients", RoleMaker, RoleChecker, RoleBranch, RoleAP); err != nil {
		return IPOApp{}, err
	}
	if f := o.ClientScope(ctx, tenant, actor); f != nil && !f(in.UCC) {
		return IPOApp{}, ErrNotFound
	}
	return o.placeIPOBid(ctx, tenant, actor, in, false)
}

// ownAccount is the portal login's own account: only the account holder
// applies (a family login sees family accounts but does not bid for them).
func (o *Ops) ownAccount(ctx context.Context, tenant, actor, ucc string) (string, error) {
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return "", err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if len(accts) == 0 {
		return "", ErrNotFound
	}
	if ucc == "" {
		ucc = accts[0]
	}
	if !contains(accts, ucc) {
		return "", ErrNotFound
	}
	if ucc != accts[0] {
		return "", invalid("only the account holder can apply for %s: ask them to apply from their own login", ucc)
	}
	return ucc, nil
}

// ClientPlaceIPOBid is a client applying from the portal.
func (o *Ops) ClientPlaceIPOBid(ctx context.Context, tenant, actor string, in IPOBidInput) (IPOApp, error) {
	ucc, err := o.ownAccount(ctx, tenant, actor, in.UCC)
	if err != nil {
		return IPOApp{}, err
	}
	in.UCC = ucc
	return o.placeIPOBid(ctx, tenant, actor, in, true)
}

func (o *Ops) placeIPOBid(ctx context.Context, tenant, actor string, in IPOBidInput, byClient bool) (IPOApp, error) {
	now := o.now()
	today := now.Format("2006-01-02")
	set, _ := o.Settings(ctx, tenant)
	iss, err := o.IPOIssueByID(ctx, tenant, strings.TrimSpace(in.Issue))
	if err != nil {
		return IPOApp{}, err
	}
	cat, ok := iss.category(in.Category)
	if !ok {
		var cs []string
		for _, c := range iss.Categories {
			cs = append(cs, c.Code)
		}
		return IPOApp{}, invalid("choose a category of %s: %s", iss.Name, strings.Join(cs, ", "))
	}
	ex := strings.ToUpper(strings.TrimSpace(in.Exchange))
	if ex == "" {
		ex = iss.Exchanges[0]
	}
	if !contains(iss.Exchanges, ex) {
		return IPOApp{}, invalid("%s bids on %s", iss.Name, strings.Join(iss.Exchanges, " and "))
	}
	if err := iss.captureErr(set, cat, now); err != nil {
		return IPOApp{}, err
	}
	c, err := o.Client(ctx, tenant, in.UCC)
	if err != nil {
		return IPOApp{}, invalid("client %s not found", in.UCC)
	}
	if err := o.ipoClientErr(ctx, tenant, c, cat, in.Declared, today); err != nil {
		return IPOApp{}, err
	}
	opts, amount, _, lots, err := iss.priceBid(cat, in.Options)
	if err != nil {
		return IPOApp{}, err
	}
	if other, taken := o.panBid(ctx, tenant, iss, c.PAN, ""); taken {
		return IPOApp{}, invalid("PAN %s already has a bid in %s (%s, %s): one application per PAN; modify or cancel that one", MaskPAN(c.PAN), iss.Name, other.ID, other.Status)
	}
	dep, dpid, ben, _ := splitDemat(c.Demat)
	a := IPOApp{UCC: c.UCC, Name: c.Name, Issue: iss.Name, IssueID: iss.ID, Exchange: ex, Category: cat.Code, Options: opts, Lots: lots, Amount: amount,
		PAN: c.PAN, Depository: dep, DPID: dpid, BenID: ben, Status: BidDraft, Pending: "New", Mandate: "Pending", Allotment: "Awaiting", Date: today,
		Source: map[bool]string{true: channelPortal, false: "Staff"}[byClient], By: actor, Created: now.Format(time.RFC3339)}
	a.Mode = ipoMode(iss, c, amount)
	switch a.Mode {
	case ModeUPI:
		if a.UPI, err = o.checkUPI(ctx, tenant, actor, c, in.UPI, in.OwnUPI, byClient); err != nil {
			return IPOApp{}, err
		}
	case ModeASBA:
		if strings.TrimSpace(c.Bank.Account) == "" || !reIFSC.MatchString(c.Bank.IFSC) {
			return IPOApp{}, invalid("bids above ₹5 lakh (and non-individuals) are blocked in the client's own bank account by the SCSB: %s has no bank account on record", c.UCC)
		}
	case ModeMargin:
		a.Margin = amount
		if err := o.ofsHasFunds(ctx, tenant, c.UCC, today, amount, 0); err != nil {
			return IPOApp{}, err
		}
	}
	n, err := o.next(ctx, tenant, "ipobid", actor)
	if err != nil {
		return IPOApp{}, err
	}
	a.ID = fmt.Sprintf("IPB-%07d", n)
	ins, err := o.instruction(ctx, tenant, actor, a, "Place", in.Instruction, byClient, bidText(iss, a))
	if err != nil {
		return IPOApp{}, err
	}
	a.Instruction, a.Ack = ins.ID, ins.Ack
	if a.Mode == ModeMargin {
		if err := o.ofsBlock(ctx, tenant, actor, &a, iss, today); err != nil {
			return IPOApp{}, err
		}
	}
	a.event(now, actor, "bid taken (%s, acknowledgement %s): %s", ins.Channel, ins.Ack, ins.Text)
	if c.Inactive {
		a.event(now, actor, "note: the client's trading account is flagged inactive")
	}
	if !iss.liveAt(set, now) {
		a.event(now, actor, "outside bidding hours: goes to the exchange in the next session")
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cIPO, a.ID, a, 0, actor); err != nil {
		return IPOApp{}, err
	}
	o.log(ctx, tenant, actor, "ipo.bid_placed", a.ID, map[string]any{"issue": iss.ID, "ucc": a.UCC, "category": a.Category, "amount": a.Amount.String(), "mode": a.Mode, "instruction": ins.ID, "channel": ins.Channel})
	o.acknowledge(ctx, tenant, actor, a, ins, iss)
	return a, nil
}

// ---- modifying and cancelling ----

// activeBid can still be modified or cancelled (before allotment).
func activeBid(a IPOApp) bool {
	switch a.Status {
	case BidDraft, BidUploaded, BidMandatePending, BidAccepted, BidDeclined, BidFailed:
		return a.Pending != "Cancel" && a.Sent != "Cancel"
	}
	return false
}

// ModifyIPOBid revises a bid on the client's instruction.
func (o *Ops) ModifyIPOBid(ctx context.Context, tenant, actor, id string, in IPOModifyInput) (IPOApp, error) {
	if err := o.require(ctx, tenant, actor, "Placing IPO bids for clients", RoleMaker, RoleChecker, RoleBranch, RoleAP); err != nil {
		return IPOApp{}, err
	}
	return o.modifyIPOBid(ctx, tenant, actor, id, in, false)
}

// ClientModifyIPOBid is a client revising their own bid in the portal.
func (o *Ops) ClientModifyIPOBid(ctx context.Context, tenant, actor, id string, in IPOModifyInput) (IPOApp, error) {
	a, err := o.ipoBid(ctx, tenant, id)
	if err != nil {
		return a, err
	}
	if _, err := o.ownAccount(ctx, tenant, actor, a.UCC); err != nil {
		return IPOApp{}, err
	}
	return o.modifyIPOBid(ctx, tenant, actor, id, in, true)
}

func (o *Ops) bidForChange(ctx context.Context, tenant, actor, id string, byClient bool) (IPOApp, IPOIssue, IPOCategory, error) {
	a, err := o.ipoBid(ctx, tenant, id)
	if err != nil {
		return a, IPOIssue{}, IPOCategory{}, err
	}
	if !byClient {
		if f := o.ClientScope(ctx, tenant, actor); f != nil && !f(a.UCC) {
			return a, IPOIssue{}, IPOCategory{}, ErrNotFound
		}
	}
	if a.IssueID == "" {
		return a, IPOIssue{}, IPOCategory{}, fmt.Errorf("%w: %s was placed outside Ledgerline; change it on the exchange platform", ErrState, a.ID)
	}
	iss, err := o.IPOIssueByID(ctx, tenant, a.IssueID)
	if err != nil {
		return a, iss, IPOCategory{}, err
	}
	cat, _ := iss.category(a.Category)
	if !activeBid(a) {
		return a, iss, cat, fmt.Errorf("%w: %s is %s%s", ErrState, a.ID, a.Status, map[bool]string{true: " (cancellation under way)", false: ""}[a.Pending == "Cancel" || a.Sent == "Cancel"])
	}
	return a, iss, cat, nil
}

func (o *Ops) modifyIPOBid(ctx context.Context, tenant, actor, id string, in IPOModifyInput, byClient bool) (IPOApp, error) {
	a, iss, cat, err := o.bidForChange(ctx, tenant, actor, id, byClient)
	if err != nil {
		return a, err
	}
	now := o.now()
	set, _ := o.Settings(ctx, tenant)
	if err := iss.captureErr(set, cat, now); err != nil {
		return a, err
	}
	opts, amount, _, lots, err := iss.priceBid(cat, in.Options)
	if err != nil {
		return a, err
	}
	sent := a.Status != BidDraft || a.Sent != ""
	if sent && cat.Modify == "up" && (amount < a.Amount || lots < a.Lots) {
		if iss.Type == IssueSME {
			return a, invalid("SME bids cannot be revised downward (or cancelled)")
		}
		return a, invalid("%s bids can only be revised upward: no fewer lots, no lower amount", cat.Code)
	}
	c, err := o.Client(ctx, tenant, a.UCC)
	if err != nil {
		return a, err
	}
	mode := ipoMode(iss, c, amount)
	if mode != a.Mode && sent {
		return a, invalid("the revised bid of ₹%s would need %s instead of %s: cancel and bid again", formatINR(amount.Dec()), mode, a.Mode)
	}
	upi := a.UPI
	if mode == ModeUPI && (in.UPI != "" || upi == "") {
		if upi, err = o.checkUPI(ctx, tenant, actor, c, firstOf(in.UPI, upi), in.OwnUPI, byClient); err != nil {
			return a, err
		}
	}
	if mode == ModeMargin {
		if err := o.ofsHasFunds(ctx, tenant, c.UCC, now.Format("2006-01-02"), amount, a.Margin); err != nil {
			return a, err
		}
	}
	prev := IPOApp{Options: a.Options, Amount: a.Amount, Lots: a.Lots, UPI: a.UPI}
	a.Options, a.Amount, a.Lots, a.UPI, a.Mode = opts, amount, lots, upi, mode
	if mode != ModeUPI {
		a.UPI = ""
	}
	ins, err := o.instruction(ctx, tenant, actor, a, "Modify", in.Instruction, byClient, bidText(iss, a))
	if err != nil {
		return a, err
	}
	a.Instruction, a.Ack = ins.ID, ins.Ack
	if mode == ModeMargin {
		a.Margin = amount
		if err := o.ofsBlock(ctx, tenant, actor, &a, iss, now.Format("2006-01-02")); err != nil {
			return a, err
		}
	}
	if sent && a.Pending != "New" {
		a.Pending = "Modify"
	}
	a.event(now, actor, "modified (%s, acknowledgement %s): from %d lot(s) ₹%s to %d lot(s) ₹%s", ins.Channel, ins.Ack, prev.Lots, formatINR(prev.Amount.Dec()), a.Lots, formatINR(a.Amount.Dec()))
	if err := docs.Upsert(ctx, o.Docs, tenant, cIPO, a.ID, a, actor); err != nil {
		return a, err
	}
	o.log(ctx, tenant, actor, "ipo.bid_modified", a.ID, map[string]any{"amount": a.Amount.String(), "was": prev.Amount.String(), "instruction": ins.ID})
	o.acknowledge(ctx, tenant, actor, a, ins, iss)
	return a, nil
}

// CancelIPOBid withdraws a bid on the client's instruction.
func (o *Ops) CancelIPOBid(ctx context.Context, tenant, actor, id string, in IPOInstructionIn) (IPOApp, error) {
	if err := o.require(ctx, tenant, actor, "Placing IPO bids for clients", RoleMaker, RoleChecker, RoleBranch, RoleAP); err != nil {
		return IPOApp{}, err
	}
	return o.cancelIPOBid(ctx, tenant, actor, id, in, false)
}

// ClientCancelIPOBid is a client withdrawing their own bid in the portal.
func (o *Ops) ClientCancelIPOBid(ctx context.Context, tenant, actor, id string) (IPOApp, error) {
	a, err := o.ipoBid(ctx, tenant, id)
	if err != nil {
		return a, err
	}
	if _, err := o.ownAccount(ctx, tenant, actor, a.UCC); err != nil {
		return IPOApp{}, err
	}
	return o.cancelIPOBid(ctx, tenant, actor, id, IPOInstructionIn{}, true)
}

func (o *Ops) cancelIPOBid(ctx context.Context, tenant, actor, id string, in IPOInstructionIn, byClient bool) (IPOApp, error) {
	a, iss, cat, err := o.bidForChange(ctx, tenant, actor, id, byClient)
	if err != nil {
		return a, err
	}
	now := o.now()
	today := now.Format("2006-01-02")
	neverSent := a.Status == BidDraft && a.Sent == "" && a.AppNo == ""
	if !neverSent {
		if cat.Modify != "any" {
			if iss.Type == IssueSME {
				return a, invalid("SME bids cannot be cancelled")
			}
			return a, invalid("%s bids cannot be withdrawn once placed (only bids up to ₹2 lakh can be)", cat.Code)
		}
		if iss.closed(now) {
			return a, fmt.Errorf("%w: bidding in %s closed at %s on %s; bids can no longer be withdrawn", ErrState, iss.Name, iss.CloseTime, fmtDay(iss.Close))
		}
	}
	ins, err := o.instruction(ctx, tenant, actor, a, "Cancel", in, byClient, "cancel bid "+a.ID+" in "+iss.Name)
	if err != nil {
		return a, err
	}
	a.Instruction, a.Ack = ins.ID, ins.Ack
	if neverSent {
		a.Status, a.Pending, a.Mandate, a.Allotment = BidCancelled, "", "Cancelled", "Cancelled before sending"
		if a.MarginEntry != "" {
			if err := o.ofsRelease(ctx, tenant, actor, &a, today, "bid cancelled"); err != nil {
				return a, err
			}
		}
	} else {
		a.Pending = "Cancel"
	}
	a.event(now, actor, "cancellation (%s, acknowledgement %s)%s", ins.Channel, ins.Ack, map[bool]string{true: "", false: ": goes to the exchange with the next file or API call"}[neverSent])
	if err := docs.Upsert(ctx, o.Docs, tenant, cIPO, a.ID, a, actor); err != nil {
		return a, err
	}
	o.log(ctx, tenant, actor, "ipo.bid_cancel", a.ID, map[string]any{"instruction": ins.ID, "immediate": neverSent})
	o.acknowledge(ctx, tenant, actor, a, ins, iss)
	return a, nil
}

// ResendIPOMandate records that a failed or declined UPI mandate was sent
// again on the client's request. NSE allows this once in the issue, after
// the day's bidding closes (eIPO FAQ v2.1: bidding inquiry, resend UPI).
func (o *Ops) ResendIPOMandate(ctx context.Context, tenant, actor, id string, in IPOInstructionIn) (IPOApp, error) {
	if err := o.require(ctx, tenant, actor, "Placing IPO bids for clients", RoleMaker, RoleChecker, RoleBranch, RoleAP); err != nil {
		return IPOApp{}, err
	}
	a, iss, _, err := o.bidForChange(ctx, tenant, actor, id, false)
	if err != nil {
		return a, err
	}
	now := o.now()
	set, _ := o.Settings(ctx, tenant)
	d, hm := now.Format("2006-01-02"), now.Format("15:04")
	switch {
	case a.Mode != ModeUPI:
		return a, invalid("%s is not a UPI bid", a.ID)
	case a.Status != BidFailed && a.Status != BidDeclined:
		return a, fmt.Errorf("%w: only a failed or declined mandate is sent again (%s is %s)", ErrState, a.ID, a.Status)
	case a.Reinit != "":
		return a, fmt.Errorf("%w: the mandate was already sent again on %s; NSE allows it once", ErrState, a.Reinit[:10])
	case d < iss.Open || iss.liveAt(set, now) || d > iss.Close || d == iss.Close && hm >= iss.OpenTime:
		return a, fmt.Errorf("%w: a mandate is sent again after the day's bidding closes (%s), before the last day", ErrState, iss.DayClose)
	}
	ins, err := o.instruction(ctx, tenant, actor, a, "Re-send mandate", in, false, "send the UPI mandate for "+a.ID+" again to "+a.UPI)
	if err != nil {
		return a, err
	}
	a.Instruction, a.Ack = ins.ID, ins.Ack
	a.Reinit, a.Status, a.Mandate, a.PayStatus, a.MandateWhy = now.Format(time.RFC3339), BidMandatePending, "Pending", "", ""
	a.event(now, actor, "UPI mandate sent again (%s, acknowledgement %s)", ins.Channel, ins.Ack)
	if err := docs.Upsert(ctx, o.Docs, tenant, cIPO, a.ID, a, actor); err != nil {
		return a, err
	}
	o.log(ctx, tenant, actor, "ipo.mandate_resent", a.ID, map[string]any{"instruction": ins.ID})
	o.acknowledge(ctx, tenant, actor, a, ins, iss)
	return a, nil
}

// IPOComplaint opens an investor complaint about a bid (21-day SCORES
// timeline) and links it to the bid.
func (o *Ops) IPOComplaint(ctx context.Context, tenant, actor, id, source, ref, text string) (Ticket, error) {
	a, err := o.ipoBid(ctx, tenant, id)
	if err != nil {
		return Ticket{}, err
	}
	if f := o.ClientScope(ctx, tenant, actor); f != nil && !f(a.UCC) {
		return Ticket{}, ErrNotFound
	}
	t, err := o.OpenTicket(ctx, tenant, actor, Ticket{Kind: KindComplaint, UCC: a.UCC, Category: "IPO / mutual funds", Source: firstOf(source, TicketSources[0]), Ref: ref,
		Subject: fmt.Sprintf("%s: bid %s (%s)", a.Issue, a.ID, a.Status)}, text)
	if err != nil {
		return t, err
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cIPO, a.ID, actor, func(x *IPOApp) error {
		x.Tickets = append(x.Tickets, t.ID)
		x.event(o.now(), actor, "complaint %s opened", t.ID)
		return nil
	})
	return t, nil
}

// ---- views ----

// IPOBidsView is the bids screen.
type IPOBidsView struct {
	Issues   []IPOIssue     `json:"issues"`
	Bids     []IPOApp       `json:"bids"`
	Counts   map[string]int `json:"counts"`
	Statuses []string       `json:"statuses"`
	Channels []string       `json:"channels"`
}

// IPOBids lists bids, optionally for one issue and status, newest first.
func (o *Ops) IPOBids(ctx context.Context, tenant, actor, issue, status string) (IPOBidsView, error) {
	v := IPOBidsView{Counts: map[string]int{}, Statuses: BidStatuses, Channels: IPOChannels, Bids: []IPOApp{}}
	var err error
	if v.Issues, err = o.IPOIssues(ctx, tenant); err != nil {
		return v, err
	}
	all, err := o.IPOApps(ctx, tenant)
	if err != nil {
		return v, err
	}
	f := o.ClientScope(ctx, tenant, actor)
	var name string
	for _, i := range v.Issues {
		if i.ID == issue {
			name = i.Name
		}
	}
	for _, a := range all {
		if f != nil && !f(a.UCC) || issue != "" && a.IssueID != issue && !(a.IssueID == "" && a.Issue == name && name != "") {
			continue
		}
		v.Counts[a.Status]++
		if status == "" || a.Status == status {
			v.Bids = append(v.Bids, a)
		}
	}
	sort.SliceStable(v.Bids, func(i, j int) bool {
		return firstOf(v.Bids[i].Created, v.Bids[i].Date) > firstOf(v.Bids[j].Created, v.Bids[j].Date)
	})
	if v.Issues == nil {
		v.Issues = []IPOIssue{}
	}
	return v, nil
}

// IPOBidDetail is one bid with its issue and consent log.
type IPOBidDetail struct {
	Bid          IPOApp           `json:"bid"`
	Issue        *IPOIssue        `json:"issue,omitempty"`
	Instructions []IPOInstruction `json:"instructions"`
	UPIs         []IPOUPIID       `json:"upis"`
}

// IPOBidByID returns a bid for the screen.
func (o *Ops) IPOBidByID(ctx context.Context, tenant, actor, id string) (IPOBidDetail, error) {
	a, err := o.ipoBid(ctx, tenant, id)
	if err != nil {
		return IPOBidDetail{}, err
	}
	if f := o.ClientScope(ctx, tenant, actor); f != nil && !f(a.UCC) {
		return IPOBidDetail{}, ErrNotFound
	}
	d := IPOBidDetail{Bid: a, Instructions: []IPOInstruction{}}
	if a.IssueID != "" {
		if iss, err := o.IPOIssueByID(ctx, tenant, a.IssueID); err == nil {
			d.Issue = &iss
		}
	}
	all, _ := docs.List[IPOInstruction](ctx, o.Docs, tenant, cIPOInstr, "")
	for _, x := range all {
		if x.Bid == a.ID {
			d.Instructions = append(d.Instructions, x)
		}
	}
	d.UPIs, _ = o.ClientUPIs(ctx, tenant, a.UCC)
	return d, nil
}

// PortalIPOCategory is a category a client may bid in, with its lot range
// at the cap price.
type PortalIPOCategory struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	MinLots int64  `json:"min_lots"`
	MaxLots int64  `json:"max_lots,omitempty"` // 0: no limit
	CutOff  bool   `json:"cut_off"`
	Declare string `json:"declare,omitempty"`
}

// PortalIPOIssue is an issue as a client sees it.
type PortalIPOIssue struct {
	IPOIssue
	Choices  []PortalIPOCategory `json:"choices"`
	CanApply bool                `json:"can_apply"`
	Why      string              `json:"why,omitempty"`
	Bid      *IPOApp             `json:"bid,omitempty"`
}

// PortalIPOView is the client portal's public issues page.
type PortalIPOView struct {
	UCC    string           `json:"ucc"`
	Own    bool             `json:"own"`
	Issues []PortalIPOIssue `json:"issues"`
	Bids   []IPOApp         `json:"bids"`
	UPIs   []IPOUPIID       `json:"upis"`
}

// lotRange is the lots a category allows at the cap price.
func (iss IPOIssue) lotRange(cat IPOCategory) (int64, int64) {
	px := money.Max(iss.PriceHigh, iss.PriceLow).Sub(cat.Discount)
	per := px.MulInt(iss.LotSize).Round(money.UnitPaisa)
	lo, hi := max(int64(1), cat.MinLots, cat.ExactLots), int64(0)
	if cat.ExactLots > 0 {
		return cat.ExactLots, cat.ExactLots
	}
	if per <= 0 {
		return lo, hi
	}
	if cat.Above > 0 {
		lo = max(lo, int64(cat.Above)/int64(per)+1)
	}
	if cat.MinAmount > 0 {
		lo = max(lo, (int64(cat.MinAmount)+int64(per)-1)/int64(per))
	}
	if cat.Upto > 0 {
		hi = int64(cat.Upto) / int64(per)
	}
	return lo, hi
}

// PortalIPO lists the issues open (or opening) for a client and their bids.
func (o *Ops) PortalIPO(ctx context.Context, tenant, actor, ucc string) (PortalIPOView, error) {
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return PortalIPOView{}, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if ucc == "" && len(accts) > 0 {
		ucc = accts[0]
	}
	if !contains(accts, ucc) {
		return PortalIPOView{}, ErrNotFound
	}
	v := PortalIPOView{UCC: ucc, Own: ucc == accts[0], Issues: []PortalIPOIssue{}, Bids: []IPOApp{}}
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return v, err
	}
	all, _ := o.IPOApps(ctx, tenant)
	mine := map[string]*IPOApp{}
	for i := range all {
		if all[i].UCC == ucc {
			a := all[i]
			v.Bids = append(v.Bids, a)
			if a.IssueID != "" && a.live() {
				mine[a.IssueID] = &a
			}
		}
	}
	sort.SliceStable(v.Bids, func(i, j int) bool {
		return firstOf(v.Bids[i].Created, v.Bids[i].Date) > firstOf(v.Bids[j].Created, v.Bids[j].Date)
	})
	set, _ := o.Settings(ctx, tenant)
	now := o.now()
	is, _ := o.IPOIssues(ctx, tenant)
	for _, iss := range is {
		if iss.Status != "Open" && iss.Status != "Upcoming" && mine[iss.ID] == nil {
			continue
		}
		p := PortalIPOIssue{IPOIssue: iss, Choices: []PortalIPOCategory{}, Bid: mine[iss.ID]}
		for _, cat := range iss.Categories {
			if cat.Individual && !individualClient(c) {
				continue
			}
			lo, hi := iss.lotRange(cat)
			if hi > 0 && hi < lo {
				continue
			}
			p.Choices = append(p.Choices, PortalIPOCategory{Code: cat.Code, Name: cat.Name, MinLots: lo, MaxLots: hi, CutOff: cat.CutOff, Declare: cat.Declare})
		}
		cerr := iss.captureErr(set, IPOCategory{}, now)
		switch {
		case !v.Own:
			p.Why = "Only the account holder can apply, from their own login."
		case p.Bid != nil:
			p.Why = "You have applied (" + p.Bid.ID + ")."
		case cerr != nil:
			p.Why = strings.TrimPrefix(cerr.Error(), ErrState.Error()+": ")
		case len(p.Choices) == 0:
			p.Why = "No category of this issue is open to this account."
		default:
			p.CanApply = true
		}
		v.Issues = append(v.Issues, p)
	}
	v.UPIs, _ = o.ClientUPIs(ctx, tenant, ucc)
	return v, nil
}
