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

// ---- client money kept back instead of upstreamed: why ----
//
// Clients' clear credit balances go to the clearing corporation every day.
// Where any is kept in a client bank account instead, the trading member
// reports how much, by reason, in the daily client collateral file
// (SEGMON): one line per reason code with the code in column 52, the
// amount in column 22 (retained by the trading member, all clients
// together) and column 38 (retained with the clearing member), the client
// fields (columns 4 to 9) blank, other numbers 0 and other text NA (NSE
// Clearing NCL/CMPL/61186 of 18 March 2024, circular 03/2024, Annexure 1,
// instructions 2-6). The reasons (the circular's own words, shortened):
//
//	RC01 funds received from clients in the USCNBA after the cut-off
//	RC02 FDRs liened to the clearing corporation, in transit (to be
//	     deposited within the next five settlement days)
//	RC03 MFOS units in transit, pledged by the next settlement day
//	RC04 funds in the DSCNBA for payout instructions sent to the bank and
//	     processed by the next settlement day, or that failed in the banking
//	     system (with the bank's evidence)
//	RC05 funds in the DSCNBA for cheques issued to clients not yet cleared;
//	     a cheque not cleared in five settlement days has its funds
//	     upstreamed on the sixth (instruction 7)
//	RC06 funds the clearing corporation credited back to the settlement
//	     account on account of an OFS
//	RC07 funds in the USCNBA before the cut-off not processed because the
//	     banking system failed (with the bank's evidence)
//	RC08 funds kept overnight in the DSCNBA on a running account
//	     settlement day
//	RC09 retained for anything else
//
// Funds a client pays after the cut-off first reduce the client's debit;
// only what is left over is reported as retained (RC01), and nothing at
// all when they do not cover the debit (NCL/CMPL/76318 of 11 September
// 2026, circular 27/2026, FAQ 5). The NSE inspection master circular
// lists the same reasons (NSE/INSP/73976, Exhibit 29 paras 8-12) and asks
// for every balance kept to lie in the USCNBA, DSCNBA or settlement
// account and be reported by the next settlement day.
//
// What Ledgerline finds by itself:
//
//   - RC01: credits from a client's registered account on the bank
//     statement after the firm's cut-off time (Client money rules), as far
//     as they are left in the client's credit after the debit is met and
//     not allocated to the clearing corporation;
//   - RC04 (RC08 on a running account settlement day): funds released by
//     the clearing corporation for a client's payout and held for it
//     (payout holds), and payouts in a bank batch of the day not yet
//     confirmed paid;
//   - RC05: cheques issued to clients (payoutcheque.go) not yet cleared;
//   - RC02: client-fund FDRs lien-marked to the clearing corporation and
//     not yet deposited with it (deposits.go);
//   - RC09: what is left of the clients' retained credit with no reason,
//     and anything kept past its reason's time limit.
//
// Anything else (MFOS in transit, OFS refunds, a bank's failure, a payout
// that failed with the bank's evidence) is recorded by a person on the
// day, with the evidence.

// RetentionCode is one of NSE Clearing's reasons for keeping client money.
type RetentionCode struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Account  string `json:"account"`  // where such money lies: USCNBA, DSCNBA, Settlement
	Evidence bool   `json:"evidence"` // the bank's evidence is needed
}

// RetentionCodes are NCL/CMPL/61186's codes, in order.
var RetentionCodes = []RetentionCode{
	{"RC01", "Received from clients in the USCNBA after the cut-off time", BankUSCNBA, false},
	{"RC02", "FDRs liened to the clearing corporation, in transit to it (deposited within the next five settlement days)", BankUSCNBA, false},
	{"RC03", "MFOS units in transit, pledged to the clearing corporation by the next settlement day", BankUSCNBA, false},
	{"RC04", "Held in the DSCNBA for payout instructions sent to the bank (processed by the next settlement day) or that failed in the banking system", BankDSCNBA, true},
	{"RC05", "Held in the DSCNBA for cheques issued to clients and not yet cleared (at most five settlement days)", BankDSCNBA, false},
	{"RC06", "Credited back by the clearing corporation to the settlement account on account of an OFS", BankSettlement, false},
	{"RC07", "In the USCNBA before the cut-off but not processed because the banking system failed", BankUSCNBA, true},
	{"RC08", "Kept overnight in the DSCNBA on a running account settlement day", BankDSCNBA, false},
	{"RC09", "Retained for any other reason (not one of the stipulated reasons)", "", false},
}

func retentionCode(c string) (RetentionCode, bool) {
	for _, x := range RetentionCodes {
		if x.Code == c {
			return x, true
		}
	}
	return RetentionCode{}, false
}

// retentionSettleDays: an FDR in transit or an uncleared cheque may be
// kept this many settlement days (NCL/CMPL/61186, RC02 and instruction 7).
const retentionSettleDays = 5

// ---- the firm's client money rules ----

const cMoneyRules = "moneyrules"

// MoneyRules are the firm's own times for client money.
type MoneyRules struct {
	// FundsCutoff is the firm's upstreaming cut-off (HH:MM, IST): money a
	// client pays after it cannot reach the clearing corporation that day
	// and is reported as retained under RC01. The clearing member sets it,
	// not earlier than one hour before its own cut-off, which is not
	// earlier than 6 pm; the firm stops taking fresh client money 30
	// minutes before it (SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2023/84 para
	// 3.C.IV-V, NSE/INSP/57041).
	FundsCutoff string `json:"funds_cutoff,omitempty"`
	By          string `json:"by,omitempty"`
	At          string `json:"at,omitempty"`
}

// MoneyRulesFor is the firm's rules.
func (o *Ops) MoneyRulesFor(ctx context.Context, tenant string) MoneyRules {
	r, _, _ := docs.GetOr(ctx, o.Docs, tenant, cMoneyRules, "main", MoneyRules{})
	return r
}

// SaveMoneyRules sets the firm's rules (compliance officer or Admin).
func (o *Ops) SaveMoneyRules(ctx context.Context, tenant, actor string, r MoneyRules) (MoneyRules, error) {
	if err := o.require(ctx, tenant, actor, "Changing the client money rules", RoleCompliance); err != nil {
		return r, err
	}
	r.FundsCutoff = strings.TrimSpace(r.FundsCutoff)
	if r.FundsCutoff != "" && !reHHMM.MatchString(r.FundsCutoff) {
		return r, invalid("the cut-off is a time of day, HH:MM (for example 18:00)")
	}
	r.By, r.At = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cMoneyRules, "main", r, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "money.rules.saved", "main", map[string]any{"funds_cutoff": r.FundsCutoff})
	return r, nil
}

// ---- retention recorded by people ----

const cRetention = "retention"

// RetentionEntry is client money a person records as kept on a day, for a
// reason Ledgerline cannot see by itself.
type RetentionEntry struct {
	ID       string       `json:"id"` // RT-000001
	Date     string       `json:"date"`
	Code     string       `json:"code"`
	Amount   money.Amount `json:"amount"`
	Account  string       `json:"account,omitempty"` // the firm bank account (FB-01) it lies in
	Ref      string       `json:"ref,omitempty"`     // FDR or folio number, bank reference
	Evidence string       `json:"evidence,omitempty"`
	Note     string       `json:"note,omitempty"`
	Status   string       `json:"status"` // Recorded, Withdrawn
	By       string       `json:"by"`
	At       string       `json:"at"`
}

// RecordRetention records money kept on a day for a reason.
func (o *Ops) RecordRetention(ctx context.Context, tenant, actor string, e RetentionEntry) (RetentionEntry, error) {
	if err := o.require(ctx, tenant, actor, "Recording client money kept back", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return e, err
	}
	e.Code = strings.ToUpper(strings.TrimSpace(e.Code))
	c, ok := retentionCode(e.Code)
	e.Ref, e.Evidence, e.Note = clip(strings.TrimSpace(e.Ref), 60), clip(strings.TrimSpace(e.Evidence), 200), clip(strings.TrimSpace(e.Note), 300)
	switch {
	case !ok || e.Code == "RC09":
		return e, invalid("choose one of the stipulated reasons RC01 to RC08; anything else is RC09, which Ledgerline works out itself from what is left unexplained")
	case !validDate(e.Date) || e.Date > o.now().In(ist).Format("2006-01-02"):
		return e, invalid("give the day the money was kept (not a future day)")
	case e.Amount <= 0:
		return e, invalid("the amount must be above zero")
	case c.Evidence && e.Evidence == "":
		return e, invalid("%s needs the bank's evidence: name the bank's letter, e-mail or statement", e.Code)
	case e.Code != "RC01" && e.Ref == "" && e.Note == "":
		return e, invalid("say what the money is: the FDR, folio or bank reference, or a note")
	}
	if e.Account != "" {
		b, ok := firmBankByID(o.firmBanks(ctx, tenant), e.Account)
		if !ok {
			return e, invalid("choose the account from the firm's bank accounts")
		}
		if c.Account != "" && b.Kind != c.Account {
			return e, invalid("money kept under %s lies in a %s, not a %s", e.Code, c.Account, b.Kind)
		}
	}
	n, err := o.next(ctx, tenant, "retention", actor)
	if err != nil {
		return e, err
	}
	e.ID, e.Status, e.By, e.At = fmt.Sprintf("RT-%06d", n), "Recorded", actor, o.now().Format(time.RFC3339)
	if _, err := docs.Put(ctx, o.Docs, tenant, cRetention, e.ID, e, 0, actor); err != nil {
		return e, err
	}
	o.log(ctx, tenant, actor, "retention.recorded", e.ID, map[string]any{"date": e.Date, "code": e.Code, "amount": e.Amount.String(), "ref": e.Ref})
	return e, nil
}

// WithdrawRetention withdraws an entry recorded in error.
func (o *Ops) WithdrawRetention(ctx context.Context, tenant, actor, id, reason string) (RetentionEntry, error) {
	if err := o.require(ctx, tenant, actor, "Recording client money kept back", RoleMaker, RoleChecker, RoleCompliance); err != nil {
		return RetentionEntry{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return RetentionEntry{}, invalid("say why it is withdrawn")
	}
	e, err := docs.Update(ctx, o.Docs, tenant, cRetention, id, actor, func(x *RetentionEntry) error {
		if x.Status != "Recorded" {
			return fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(x.Status))
		}
		x.Status, x.Note = "Withdrawn", strings.TrimSpace(x.Note+" · withdrawn: "+strings.TrimSpace(reason))
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "retention.withdrawn", id, map[string]any{"reason": reason})
	}
	return e, err
}

// ---- the day's retention, by reason ----

// RetentionLine is one amount kept, and why.
type RetentionLine struct {
	Code   string    `json:"code"`
	Amount money.Dec `json:"amount"`
	What   string    `json:"what"`
	UCC    string    `json:"ucc,omitempty"`
	Ref    string    `json:"ref,omitempty"`
	Source string    `json:"source"` // Ledgerline, or the entry's ID
}

// RetentionTotal is one reason's total for the day.
type RetentionTotal struct {
	RetentionCode
	Amount money.Dec       `json:"amount"`
	Lines  []RetentionLine `json:"lines"`
}

// RetentionReport is the day's client money kept back, by reason.
type RetentionReport struct {
	Date       string           `json:"date"`
	Cutoff     string           `json:"cutoff,omitempty"`
	Settlement bool             `json:"settlement"` // a running account settlement day
	Codes      []RetentionTotal `json:"codes"`      // the codes with an amount
	Total      money.Dec        `json:"total"`
	Stipulated money.Dec        `json:"stipulated"` // RC01 to RC08
	Entries    []RetentionEntry `json:"entries"`
	Warnings   []string         `json:"warnings"`
	CanRecord  bool             `json:"can_record"`
	CanRules   bool             `json:"can_rules"`
	AllCodes   []RetentionCode  `json:"all_codes"`
}

// byCode is a reason's total (zero when none).
func (r RetentionReport) byCode(code string) money.Dec {
	for _, c := range r.Codes {
		if c.Code == code {
			return c.Amount
		}
	}
	return money.Dec{}
}

// justifiedIn is what the stipulated reasons allow to lie at the end of
// the day in the DSCNBA (kind DSCNBA), or in the USCNBA and settlement
// accounts together (either of those kinds).
func (r RetentionReport) justifiedIn(kind string) money.Dec {
	in := map[string]bool{BankDSCNBA: kind == BankDSCNBA, BankUSCNBA: kind != BankDSCNBA, BankSettlement: kind != BankDSCNBA}
	var t money.Dec
	for _, c := range r.Codes {
		if c.Code != "RC09" && in[c.Account] {
			t = t.Add(c.Amount)
		}
	}
	return t
}

// segmonRetained is what each client's SEGMON line reports as retained
// by the trading member, explained client by client: money paid after the
// cut-off once the debit is met (RC01), then funds held for the client's
// payout (RC04, or RC08 on a settlement day), and the rest (RC09).
func segmonRetained(all map[string]*segmonClient, rows []segmonRow, settlementDay bool) []RetentionLine {
	ret := map[string]money.Dec{}
	for _, r := range rows {
		if r.reason == "" {
			ret[r.ucc] = ret[r.ucc].Add(r.v[sgRetTMCash])
		}
	}
	var out []RetentionLine
	for _, u := range sortedKeys(ret) {
		left, c := ret[u], all[u]
		if left.Sign() <= 0 || c == nil {
			continue
		}
		take := func(code string, amt money.Dec, what string) {
			x := money.Min(left, money.Max(amt, money.Dec{}))
			if x.Sign() > 0 {
				out = append(out, RetentionLine{Code: code, Amount: x, What: what, UCC: u, Source: "Ledgerline"})
				left = left.Sub(x)
			}
		}
		take("RC01", c.lateSurplus(), "paid after the cut-off, after the client's debit was met")
		hold := "RC04"
		if settlementDay {
			hold = "RC08"
		}
		take(hold, c.held, "released by the clearing corporation for the client's payout")
		take("RC09", left, "the client's clear credit not allocated to the clearing corporation, with no stipulated reason")
	}
	return out
}

// lateSurplus is what a client paid after the cut-off that is left over
// once the client's debit before it is met (NCL/CMPL/76318, FAQ 5): the
// clear credit at the end of the day less the clear credit without the
// late money, never below nil.
func (c *segmonClient) lateSurplus() money.Dec {
	if c.late.Sign() <= 0 {
		return money.Dec{}
	}
	end := money.Max(c.bal.B, money.Dec{})
	before := money.Max(c.bal.B.Sub(c.late), money.Dec{})
	return end.Sub(before)
}

// RetentionFor is the day's client money kept back, by reason.
func (o *Ops) RetentionFor(ctx context.Context, tenant, actor, date string) (RetentionReport, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the client money reconciliation", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return RetentionReport{}, err
	}
	if !validDate(date) {
		return RetentionReport{}, invalid("give the date")
	}
	r, err := o.retentionReport(ctx, tenant, date, nil, nil)
	r.CanRecord = o.require(ctx, tenant, actor, "Recording client money kept back", RoleMaker, RoleChecker, RoleCompliance) == nil
	r.CanRules = o.require(ctx, tenant, actor, "Changing the client money rules", RoleCompliance) == nil
	return r, err
}

// retentionReport works out the day's retention. With all and rows (the
// day's SEGMON lines) given it uses them; otherwise it makes them.
func (o *Ops) retentionReport(ctx context.Context, tenant, date string, all map[string]*segmonClient, rows []segmonRow) (RetentionReport, error) {
	r := RetentionReport{Date: date, Codes: []RetentionTotal{}, Entries: []RetentionEntry{}, Warnings: []string{}, AllCodes: RetentionCodes}
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return r, err
	}
	r.Cutoff = o.MoneyRulesFor(ctx, tenant).FundsCutoff
	r.Settlement, _ = o.rasSettlementDay(ctx, tenant, date)
	if all == nil {
		var uccs []string
		if all, uccs, err = o.segmonData(ctx, tenant, date); err != nil {
			return r, err
		}
		rows = segmonRows(all, uccs)
	}
	lines := segmonRetained(all, rows, r.Settlement)
	if r.Cutoff == "" {
		r.Warnings = append(r.Warnings, "The firm's cut-off for client money is not set (Client money rules): money paid after it cannot be told apart, so none is reported under RC01.")
	}
	warn := func(f string, a ...any) { r.Warnings = append(r.Warnings, fmt.Sprintf(f, a...)) }
	// Payouts sent to the bank and not yet confirmed paid: RC04 until the
	// next settlement day after the batch.
	if bs, err := o.PayoutBatches(ctx, tenant); err == nil {
		for _, b := range bs {
			if b.Date > date {
				continue
			}
			for _, it := range b.Items {
				if it.Status != "Sent" {
					continue
				}
				code, what := "RC04", "payout "+it.Request+" sent to the bank, not yet confirmed paid"
				if date > set.NextBusinessDay(b.Date) {
					code = "RC09"
					warn("Payout %s of %s sent to the bank on %s is still not confirmed paid: record the bank's result, or the bank's evidence of the failure (RC04).", it.Request, siClient(it.UCC), fmtDay(b.Date))
				}
				lines = append(lines, RetentionLine{Code: code, Amount: it.Amount.Dec(), What: what, UCC: it.UCC, Ref: b.ID, Source: "Ledgerline"})
			}
		}
	}
	// Cheques issued to clients and not yet cleared (payoutcheque.go).
	for _, q := range o.payoutCheques(ctx, tenant) {
		if !q.outstandingOn(date) {
			continue
		}
		code := "RC05"
		if n := businessDaysBetween(set, q.IssuedOn, date); n > retentionSettleDays {
			code = "RC09"
			warn("Cheque %s to %s issued on %s is not cleared after %d settlement days: its funds are upstreamed on the sixth settlement day (NCL/CMPL/61186, instruction 7). Stop it and cancel it on Receipts & payouts.", q.ChequeNo, siClient(q.UCC), fmtDay(q.IssuedOn), retentionSettleDays)
		}
		lines = append(lines, RetentionLine{Code: code, Amount: q.Amount.Dec(), What: "cheque " + q.ChequeNo + " issued to the client, not yet cleared", UCC: q.UCC, Ref: q.ID, Source: "Ledgerline"})
	}
	// Client-fund FDRs lien-marked to the clearing corporation and not yet
	// deposited with it (deposits.go).
	if ds, err := docs.List[Deposit](ctx, o.Docs, tenant, cDeposit, ""); err == nil {
		for _, d := range ds {
			if !d.inTransitOn(date) {
				continue
			}
			code := "RC02"
			if n := businessDaysBetween(set, d.Issued, date); n > retentionSettleDays {
				code = "RC09"
				warn("Client-fund FDR %s of %s, liened on %s, is still not deposited with the clearing corporation after %d settlement days.", d.Ref, d.Issuer, fmtDay(d.Issued), retentionSettleDays)
			}
			lines = append(lines, RetentionLine{Code: code, Amount: d.Amount, What: "client-fund FDR in transit to " + d.CC, Ref: d.Ref, Source: "Ledgerline"})
		}
	}
	// What people recorded for the day.
	if es, err := docs.List[RetentionEntry](ctx, o.Docs, tenant, cRetention, ""); err == nil {
		sort.SliceStable(es, func(i, j int) bool { return es[i].ID < es[j].ID })
		for _, e := range es {
			if e.Date != date {
				continue
			}
			r.Entries = append(r.Entries, e)
			if e.Status != "Recorded" {
				continue
			}
			lines = append(lines, RetentionLine{Code: e.Code, Amount: e.Amount.Dec(), What: firstOf(e.Note, "recorded by "+e.By), Ref: e.Ref, Source: e.ID})
		}
	}
	by := map[string]*RetentionTotal{}
	for _, l := range lines {
		t := by[l.Code]
		if t == nil {
			c, _ := retentionCode(l.Code)
			t = &RetentionTotal{RetentionCode: c, Lines: []RetentionLine{}}
			by[l.Code] = t
		}
		t.Amount = t.Amount.Add(l.Amount)
		t.Lines = append(t.Lines, l)
	}
	for _, c := range RetentionCodes {
		if t := by[c.Code]; t != nil && t.Amount.Sign() > 0 {
			t.Amount = paise(t.Amount)
			r.Codes = append(r.Codes, *t)
			r.Total = r.Total.Add(t.Amount)
			if c.Code != "RC09" {
				r.Stipulated = r.Stipulated.Add(t.Amount)
			}
		}
	}
	if x := r.byCode("RC09"); x.Sign() > 0 {
		warn("₹%s of client money is kept with no stipulated reason (RC09): upstream it, or record the reason.", formatINR(x))
	}
	return r, nil
}

// ---- what SEGMON reads for retention ----

// lateReceipts is what each client paid into the firm's account on date
// after the firm's cut-off, from the bank statement credits matched to a
// client's registered account (their time is the bank's). Money paid into
// an MCX-only account or the commodity ledger is not in SEGMON.
func (o *Ops) lateReceipts(ctx context.Context, tenant, date string) map[string]money.Dec {
	out := map[string]money.Dec{}
	cut := o.MoneyRulesFor(ctx, tenant).FundsCutoff
	if cut == "" {
		return out
	}
	ts, _ := docs.List[BankTxn](ctx, o.Docs, tenant, cBankTxn, "")
	for _, t := range ts {
		at := strings.TrimSpace(t.Time)
		if t.Date != date || t.Status != "Matched" || t.UCC == "" || len(at) < 5 || at[:5] <= cut || t.Earmark == EarmarkMCX || t.Ledger == "COM" || t.Ledger == "UNALLOC" {
			continue
		}
		out[t.UCC] = out[t.UCC].Add(t.Amount.Dec())
	}
	return out
}

// ofsMarginHeld is each client's offer-for-sale margin still held (with
// NSE Clearing) at the end of date: what the client's ledger gave to
// PAYABLE:OFS_MARGIN less what came back (ipoofs.go).
func (o *Ops) ofsMarginHeld(ctx context.Context, tenant, date string) map[string]money.Dec {
	out := map[string]money.Dec{}
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{To: date})
	if err != nil {
		return out
	}
	for _, e := range es {
		ofs := false
		for _, p := range e.Postings {
			ofs = ofs || p.Account == PayableOFSMargin
		}
		if !ofs {
			continue
		}
		for _, p := range e.Postings {
			if u, _, ok := ledger.ClientCode(p.Account); ok {
				out[u] = out[u].Add((p.Debit - p.Credit).Dec())
			}
		}
	}
	return out
}

// suspenseClientMoney is the client money parked in the bank suspense
// account to the end of date (unidentified and third-party credits not
// yet returned or identified), credit positive, with the day's clear
// balance and peak. Research and advisory fees parked there are the
// firm's money and left out.
func (o *Ops) suspenseClientMoney(ctx context.Context, tenant, date string) (segBal, error) {
	fee := map[string]bool{}
	ts, _ := docs.List[BankTxn](ctx, o.Docs, tenant, cBankTxn, "")
	for _, t := range ts {
		if t.Channel == ChannelFeeUPI || t.Status == BankFirmFee || t.Status == BankFeeMoved {
			fee["RET:"+t.Ref] = true
		}
	}
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{To: date})
	if err != nil {
		return segBal{}, err
	}
	sort.SliceStable(es, func(i, j int) bool { return es[i].Seq < es[j].Seq })
	var open money.Dec
	var day []money.Dec
	for _, e := range es {
		if strings.HasPrefix(e.Key, "FEE:") || strings.HasPrefix(e.Key, "FEEOUT:") || fee[e.Key] {
			continue
		}
		for _, p := range e.Postings {
			if p.Account != ledger.SuspenseBank {
				continue
			}
			d := (p.Credit - p.Debit).Dec()
			if e.ValueDate < date {
				open = open.Add(d)
			} else {
				day = append(day, d)
			}
		}
	}
	b := segBal{A: open, Peak: open}
	for _, d := range day {
		b.A = b.A.Add(d)
		b.Peak = money.Max(b.Peak, b.A)
	}
	b.B = b.A
	return b, nil
}
