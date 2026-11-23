package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- the daily reconciliation of client money transfers ----
//
// Funds may move between the client account and the settlement account,
// and from the client account to the broker's own (proprietary) account
// only for legitimate purposes, such as recovery of brokerage and statutory
// dues; for such transfers the broker "shall maintain daily reconciliation
// statement clearly indicating the amount of funds transferred" (SEBI
// master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90 of 17 June 2025,
// para 15.4.4.2, from SEBI/HO/MIRSD/MIRSD2/CIR/P/2016/95 of 26 September
// 2016, para 2.4.2, which also names the "funds shortfall of debit balance
// clients which has been met by the stock broker"). The exchanges check it
// in inspections and the internal auditor every half year (para 15.4.4.3);
// NSE/INSP/55904 of 9 March 2023 reminded members to keep it daily and
// produce it when asked. No format is prescribed: this is Ledgerline's.
//
// Since the upstreaming of client funds (SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/
// 2023/84 of 8 June 2023, para 3.C.II) clients' money is received only in
// the USCNBA and paid only from the DSCNBA after funds come back from the
// clearing corporation (master circular para 15.3.2), so the statement
// follows the money: receipts into the USCNBA, upstreaming to the clearing
// corporation, funds coming back, payouts from the DSCNBA, transfers to and
// from the own account, and the bank statement lines where they are loaded.
// NSE's FAQs add that money may not move from a USCNBA to a DSCNBA
// (NSE/INSP/57959 of 11 August 2023, FAQ 21) and that nothing may be left
// in the DSCNBA at the end of a day, except on a running account settlement
// day with the bank instructions given by then (NSE/INSP/58146 of 29 August
// 2023, FAQ 1).
//
// A maker prepares the day's statement; a checker who is not the maker
// signs it off. The prepared copy, and a CSV of it, are kept as evidence; a
// statement whose records changed after it was prepared is prepared again
// before it can be signed.

const (
	cFirmXfer   = "firmxfer"   // a transfer between the firm's accounts, by ID (FT-000001)
	cBankLine   = "bankline"   // a bank statement line, by reference and direction
	cMoneyRecon = "moneyrecon" // a day's statement as prepared and signed off, by date
	wMoneyRecon = "money_recon"
)

// Movement groups on the statement.
const (
	mvReceipt  = "receipt"
	mvUpstream = "upstream"
	mvFromCC   = "from_cc"
	mvPayout   = "payout"
	mvToOwn    = "to_own"
	mvFromOwn  = "from_own"
	mvInternal = "internal"
	mvOther    = "other"
)

var moneyGroupNames = map[string]string{
	mvReceipt: "Received from clients", mvUpstream: "Sent to the clearing corporation", mvFromCC: "Received from the clearing corporation",
	mvPayout: "Paid to clients", mvToOwn: "Client money accounts to the own account", mvFromOwn: "Own account to client money accounts",
	mvInternal: "Between the client money accounts", mvOther: "Other movements",
}

var moneyGroupOrder = []string{mvReceipt, mvUpstream, mvFromCC, mvPayout, mvToOwn, mvFromOwn, mvInternal, mvOther}

// ---- bank statement lines ----

// BankLine is one line of a bank statement of one of the firm's accounts,
// credit or debit, as loaded (BANK-STMT) or as a bank's live notice said.
type BankLine struct {
	ID           string       `json:"id"` // reference|CR or reference|DR
	Date         string       `json:"date"`
	Time         string       `json:"time,omitempty"`
	Mode         string       `json:"mode,omitempty"`
	Ref          string       `json:"ref"`
	Party        string       `json:"party,omitempty"`         // remitter or beneficiary
	PartyAccount string       `json:"party_account,omitempty"` // stored in full, shown masked
	Amount       money.Amount `json:"amount"`
	DrCr         string       `json:"dr_cr"`             // CR or DR on the firm's account
	Account      string       `json:"account,omitempty"` // the firm's account the line is on
	Balance      string       `json:"balance,omitempty"` // the account's balance after the line, as the bank gives it
	Source       string       `json:"source"`
	Seq          int          `json:"seq,omitempty"` // the line's place in its file
}

// Public masks the account numbers.
func (b BankLine) Public() BankLine {
	if b.PartyAccount != "VPA" {
		b.PartyAccount = MaskAccount(b.PartyAccount)
	}
	b.Account = MaskAccount(b.Account)
	return b
}

// keepBankLines stores statement lines; a line already stored is left as
// it is.
func (o *Ops) keepBankLines(ctx context.Context, tenant, actor string, ls []BankLine) int {
	n := 0
	for _, l := range ls {
		if l.Ref == "" || l.Amount <= 0 || (l.DrCr != "CR" && l.DrCr != "DR") {
			continue
		}
		l.ID = l.Ref + "|" + l.DrCr
		if _, err := docs.Put(ctx, o.Docs, tenant, cBankLine, l.ID, l, 0, actor); err == nil {
			n++
		}
	}
	return n
}

// ---- transfers between the firm's accounts ----

// MoneyPurpose is why money moves between the firm's accounts.
type MoneyPurpose struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Group   string   `json:"group"`
	From    []string `json:"from"` // the kinds of account it may come from
	To      []string `json:"to"`
	Segment bool     `json:"segment,omitempty"` // a pay-in or pay-out names the exchange segment
	Posts   string   `json:"posts"`             // what is posted in the ledger, in words
}

var clientKinds = []string{BankUSCNBA, BankDSCNBA, BankSettlement}

// MoneyPurposes are the transfers a maker records. The permitted routes
// are SEBI's (master circular para 15.4.4.2) and NSE's FAQs on upstreaming
// (NSE/INSP/57959: FAQ 17, client funds go to the clearing corporation
// through the settlement account; FAQ 30, pay-out may go straight to the
// DSCNBA; FAQ 33, brokerage, statutory levies and charges go to the own
// account; FAQ 42, own funds go to the USCNBA to be upstreamed).
var MoneyPurposes = []MoneyPurpose{
	{"upstream", "Upstream to the clearing corporation", mvUpstream, []string{BankUSCNBA, BankSettlement}, []string{BankCC}, false, "Clearing corporation collateral debited, client bank credited"},
	{"payin", "Funds pay-in to the clearing corporation", mvUpstream, []string{BankSettlement, BankUSCNBA}, []string{BankCC}, true, "The segment's clearing account debited, client bank credited"},
	{"release", "Collateral released by the clearing corporation", mvFromCC, []string{BankCC}, []string{BankDSCNBA, BankSettlement, BankUSCNBA}, false, "Client bank debited, clearing corporation collateral credited"},
	{"payout", "Funds pay-out from the clearing corporation", mvFromCC, []string{BankCC}, []string{BankSettlement, BankDSCNBA}, true, "Client bank debited, the segment's clearing account credited"},
	{"internal", "Between the client money accounts", mvInternal, clientKinds, clientKinds, false, "Nothing: both are the client bank account in the ledger"},
	{"brokerage", "Brokerage and charges debited to clients, to the own account", mvToOwn, clientKinds, []string{BankOwn}, false, "Own bank debited, client bank credited"},
	{"statutory", "Statutory dues debited to clients (STT, stamp duty, SEBI fees, GST, exchange charges), to the own account", mvToOwn, clientKinds, []string{BankOwn}, false, "Own bank debited, client bank credited"},
	{"recovery", "Recovery of a client's debit the firm met from its own funds", mvToOwn, clientKinds, []string{BankOwn}, false, "Own bank debited, client bank credited"},
	{"own_in", "Own funds into a client money account", mvFromOwn, []string{BankOwn}, clientKinds, false, "Client bank debited, own bank credited"},
}

// WithdrawReason is a purpose for which client funds may be taken back
// from the clearing corporation, and the kind of account they go to.
type WithdrawReason struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   string `json:"to"`
}

// WithdrawReasons are the only purposes for which client funds may be
// withdrawn from the clearing corporation (SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/
// 2023/84 of 8 June 2023, para 3.C.IX, forwarded by NSE/INSP/57041): (a)
// to (f) are the client's dues, met through the settlement account, from
// where brokerage and charges go to the own account as usual; (g) and (h)
// are paid to clients, so they come back only to the DSCNBA.
var WithdrawReasons = []WithdrawReason{
	{"unpaid", "(a) The client's unpaid securities or MTF obligation", BankSettlement},
	{"losses", "(b) Losses on selling the client's unpaid securities", BankSettlement},
	{"penalties", "(c) Penalties", BankSettlement},
	{"statutory", "(d) Statutory levies: STT, stamp duty, SEBI turnover fee", BankSettlement},
	{"brokerage", "(e) Brokerage, exchange transaction charges included", BankSettlement},
	{"charges", "(f) Other charges, such as DP charges", BankSettlement},
	{"regulatory", "(g) Amounts due to clients by regulation, such as running account settlement", BankDSCNBA},
	{"withdrawal", "(h) Clients' withdrawal requests", BankDSCNBA},
}

func withdrawReason(id string) (WithdrawReason, bool) {
	for _, r := range WithdrawReasons {
		if r.ID == id {
			return r, true
		}
	}
	return WithdrawReason{}, false
}

func moneyPurpose(id string) (MoneyPurpose, bool) {
	for _, p := range MoneyPurposes {
		if p.ID == id {
			return p, true
		}
	}
	return MoneyPurpose{}, false
}

// FirmTransfer is money moved between the firm's accounts, as a maker
// recorded it and a checker approved it.
type FirmTransfer struct {
	ID      string `json:"id"`
	Date    string `json:"date"`
	Purpose string `json:"purpose"`
	From    string `json:"from"` // a register account (FB-01), or CC for the clearing corporation
	To      string `json:"to"`
	Segment string `json:"segment,omitempty"` // NSE-CM …, for a pay-in or pay-out
	// Withdraw is why client funds were taken back from the clearing
	// corporation (WithdrawReasons), for a release.
	Withdraw  string       `json:"withdraw,omitempty"`
	Amount    money.Amount `json:"amount"`
	Ref       string       `json:"ref"` // the bank's reference (UTR)
	Note      string       `json:"note,omitempty"`
	Status    string       `json:"status"` // Pending, Posted, Recorded (nothing to post), Rejected
	Warning   string       `json:"warning,omitempty"`
	Maker     string       `json:"maker"`
	MadeAt    string       `json:"made_at"`
	Checker   string       `json:"checker,omitempty"`
	CheckedAt string       `json:"checked_at,omitempty"`
	Entry     string       `json:"entry,omitempty"`
	Reason    string       `json:"reason,omitempty"`
}

// moneyEnd is one end of a transfer: its kind and its name.
func moneyEnd(bs []FirmBank, id string) (string, string, bool) {
	if id == "CC" {
		return BankCC, "Clearing corporation", true
	}
	b, ok := firmBankByID(bs, id)
	if !ok {
		return "", "", false
	}
	return b.Kind, b.Label(), true
}

// FirmTransfers lists the transfers of a period (newest first).
func (o *Ops) FirmTransfers(ctx context.Context, tenant, actor, from, to string) ([]FirmTransfer, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the client money reconciliation", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return nil, err
	}
	ts, err := docs.List[FirmTransfer](ctx, o.Docs, tenant, cFirmXfer, "")
	out := []FirmTransfer{}
	for _, t := range ts {
		if (from == "" || t.Date >= from) && (to == "" || t.Date <= to) {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, err
}

// RecordFirmTransfer records a transfer for a checker to approve.
func (o *Ops) RecordFirmTransfer(ctx context.Context, tenant, actor string, t FirmTransfer) (FirmTransfer, error) {
	if err := o.require(ctx, tenant, actor, "Recording a transfer between the firm's bank accounts", RoleMaker, RoleChecker); err != nil {
		return FirmTransfer{}, err
	}
	p, ok := moneyPurpose(t.Purpose)
	if !ok {
		return t, invalid("choose why the money moved")
	}
	bs := o.firmBanks(ctx, tenant)
	fk, _, okf := moneyEnd(bs, t.From)
	tk, _, okt := moneyEnd(bs, t.To)
	t.Ref, t.Note, t.Segment = clip(strings.TrimSpace(t.Ref), 40), clip(strings.TrimSpace(t.Note), 300), strings.ToUpper(strings.TrimSpace(t.Segment))
	switch {
	case !ledger.ValidDate(t.Date) || t.Date > o.now().Format("2006-01-02"):
		return t, invalid("give the day the money moved (not a future day)")
	case !okf || !okt:
		return t, invalid("choose both accounts from the firm's bank accounts (Receipts & payouts → Client money reconciliation → Firm bank accounts), or the clearing corporation")
	case t.From == t.To:
		return t, invalid("the money must move between two different accounts")
	case !contains(p.From, fk):
		return t, invalid("%s comes from a %s account, not a %s account", p.Name, strings.Join(p.From, " or "), fk)
	case !contains(p.To, tk):
		return t, invalid("%s goes to a %s account, not a %s account", p.Name, strings.Join(p.To, " or "), tk)
	case t.Amount <= 0:
		return t, invalid("the amount must be above zero")
	case t.Ref == "":
		return t, invalid("give the bank's reference (UTR) of the transfer")
	}
	t.Withdraw = strings.TrimSpace(t.Withdraw)
	if t.Purpose == "release" {
		w, ok := withdrawReason(t.Withdraw)
		switch {
		case !ok:
			return t, invalid("say why the funds were taken back from the clearing corporation: client funds may be withdrawn only for the purposes SEBI lists (SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2023/84, para 3.C.IX)")
		case tk != w.To:
			return t, invalid("funds withdrawn for %s come back to a %s, not a %s", strings.ToLower(strings.TrimSpace(w.Name[3:])), w.To, tk)
		}
	} else {
		t.Withdraw = ""
	}
	if p.Segment {
		if _, ok := clearingSeg(t.Segment); !ok {
			return t, invalid("say which exchange segment the %s is for (NSE-CM, NSE-FO …)", strings.ToLower(p.Name))
		}
	} else {
		t.Segment = ""
	}
	if err := o.checkLock(ctx, tenant, t.Date); err != nil {
		return t, err
	}
	if fk == BankUSCNBA && tk == BankDSCNBA {
		t.Warning = "Money may not move from a USCNBA to a DSCNBA (NSE/INSP/57959, FAQ 21): the day's statement flags it."
	}
	all, _ := docs.List[FirmTransfer](ctx, o.Docs, tenant, cFirmXfer, "")
	for _, x := range all {
		if x.Ref == t.Ref && x.Status != "Rejected" && x.Purpose == t.Purpose {
			return t, invalid("%s %s is already recorded as %s", p.Name, t.Ref, x.ID)
		}
	}
	n, err := o.next(ctx, tenant, "firmxfer", actor)
	if err != nil {
		return t, err
	}
	t.ID, t.Status, t.Maker, t.MadeAt, t.Checker, t.CheckedAt, t.Entry, t.Reason = fmt.Sprintf("FT-%06d", n), "Pending", actor, o.now().Format(time.RFC3339), "", "", "", ""
	if _, err := docs.Put(ctx, o.Docs, tenant, cFirmXfer, t.ID, t, 0, actor); err != nil {
		return t, err
	}
	o.log(ctx, tenant, actor, "money.transfer.recorded", t.ID, map[string]any{"purpose": t.Purpose, "from": t.From, "to": t.To, "amount": t.Amount.String(), "ref": t.Ref})
	return t, nil
}

// postings are what an approved transfer posts in the ledger: nothing for
// a move between client money accounts (one client bank account there).
func (t FirmTransfer) postings() []ledger.Posting {
	a, memo := t.Amount, t.Ref
	seg := func() string {
		x, _ := clearingSeg(t.Segment)
		return ledger.ClearingAccount(x.Exchange, x.Segment)
	}
	switch t.Purpose {
	case "upstream":
		return []ledger.Posting{{Account: ledger.CCCollateral, Debit: a, Memo: memo}, {Account: ledger.BankClient, Credit: a, Memo: memo}}
	case "payin":
		return []ledger.Posting{{Account: seg(), Debit: a, Memo: memo}, {Account: ledger.BankClient, Credit: a, Memo: memo}}
	case "release":
		return []ledger.Posting{{Account: ledger.BankClient, Debit: a, Memo: memo}, {Account: ledger.CCCollateral, Credit: a, Memo: memo}}
	case "payout":
		return []ledger.Posting{{Account: ledger.BankClient, Debit: a, Memo: memo}, {Account: seg(), Credit: a, Memo: memo}}
	case "brokerage", "statutory", "recovery":
		return []ledger.Posting{{Account: ledger.BankOwn, Debit: a, Memo: memo}, {Account: ledger.BankClient, Credit: a, Memo: memo}}
	case "own_in":
		return []ledger.Posting{{Account: ledger.BankClient, Debit: a, Memo: memo}, {Account: ledger.BankOwn, Credit: a, Memo: memo}}
	}
	return nil
}

// DecideFirmTransfer approves (posting it) or rejects a pending transfer.
// The maker cannot approve their own.
func (o *Ops) DecideFirmTransfer(ctx context.Context, tenant, actor, id string, approve bool, reason string) (FirmTransfer, error) {
	if err := o.require(ctx, tenant, actor, "Approving a transfer between the firm's bank accounts", RoleChecker); err != nil {
		return FirmTransfer{}, err
	}
	t, _, err := docs.Get[FirmTransfer](ctx, o.Docs, tenant, cFirmXfer, id)
	if err != nil {
		return t, err
	}
	if t.Status != "Pending" {
		return t, fmt.Errorf("%w: %s is %s", ErrState, id, strings.ToLower(t.Status))
	}
	if t.Maker == actor {
		return t, fmt.Errorf("%w: the maker of a transfer cannot approve it", ErrForbidden)
	}
	status, entry := "Rejected", ""
	if approve {
		p, _ := moneyPurpose(t.Purpose)
		if ps := t.postings(); len(ps) > 0 {
			e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: "XFR:" + t.ID, ValueDate: t.Date, CreatedBy: actor,
				Narration: p.Name + " · " + t.Ref, Source: ledger.SourceRef{Kind: "FIRMXFER", ID: t.ID}, Postings: ps})
			if err != nil {
				return t, err
			}
			status, entry = "Posted", e.ID
		} else {
			status = "Recorded"
		}
	} else if strings.TrimSpace(reason) == "" {
		return t, invalid("give the reason for rejecting")
	}
	t, err = docs.Update(ctx, o.Docs, tenant, cFirmXfer, id, actor, func(x *FirmTransfer) error {
		x.Status, x.Checker, x.CheckedAt, x.Entry, x.Reason = status, actor, o.now().Format(time.RFC3339), entry, clip(strings.TrimSpace(reason), 300)
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "money.transfer."+strings.ToLower(status), id, map[string]any{"purpose": t.Purpose, "amount": t.Amount.String(), "entry": entry, "reason": reason})
	}
	return t, err
}

// ---- the statement ----

// MoneyMove is one movement of money on the statement.
type MoneyMove struct {
	Date     string       `json:"date"`
	From     string       `json:"from"`
	FromKind string       `json:"from_kind"`
	FromID   string       `json:"from_id,omitempty"`
	To       string       `json:"to"`
	ToKind   string       `json:"to_kind"`
	ToID     string       `json:"to_id,omitempty"`
	Amount   money.Amount `json:"amount"`
	Purpose  string       `json:"purpose"`
	Group    string       `json:"group"`
	Ref      string       `json:"ref,omitempty"`
	Entry    string       `json:"entry,omitempty"`
	Source   string       `json:"source"`
	// Assumed: nothing recorded says which of the firm's accounts it went
	// through; the usual one for it is shown.
	Assumed bool     `json:"assumed,omitempty"`
	Flags   []string `json:"flags,omitempty"`
}

// MoneyTotal is one group's total.
type MoneyTotal struct {
	Group  string       `json:"group"`
	Name   string       `json:"name"`
	Count  int          `json:"count"`
	Amount money.Amount `json:"amount"`
}

// MoneyCharges are the charges debited to clients' ledgers: what the firm
// may take to its own account.
type MoneyCharges struct {
	Brokerage money.Amount `json:"brokerage"`
	Other     money.Amount `json:"other"`     // interest, DP and other charges
	Statutory money.Amount `json:"statutory"` // STT, stamp duty, SEBI fees, GST, exchange and depository charges
	Total     money.Amount `json:"total"`
}

func (c *MoneyCharges) add(acct string, amt money.Amount) {
	switch {
	case acct == ledger.IncomeBrokerage:
		c.Brokerage += amt
	case strings.HasPrefix(acct, "INCOME:"):
		c.Other += amt
	default:
		c.Statutory += amt
	}
	c.Total += amt
}

// MoneyPool is the backing for moving client money to the own account, to
// the end of the day: charges debited to clients and own funds put in,
// less what was taken to the own account. Below zero, client money was
// taken beyond them.
type MoneyPool struct {
	Charges   money.Amount `json:"charges"`
	OwnIn     money.Amount `json:"own_in"`
	ToOwn     money.Amount `json:"to_own"`
	Available money.Amount `json:"available"`
}

// MoneyAccount is one of the firm's accounts on the day: its book balance
// from Ledgerline's records, and the bank's where its statement is loaded.
type MoneyAccount struct {
	ID         string       `json:"id"`
	Kind       string       `json:"kind"`
	Label      string       `json:"label"`
	Opening    money.Amount `json:"opening"`
	In         money.Amount `json:"in"`
	Out        money.Amount `json:"out"`
	Closing    money.Amount `json:"closing"`
	Statement  string       `json:"statement,omitempty"` // the bank's closing balance of the day, when loaded
	Difference string       `json:"difference,omitempty"`
	Lines      int          `json:"lines"`
}

// MoneyFlag is something on the statement that needs explaining.
type MoneyFlag struct {
	Kind   string       `json:"kind"`
	Text   string       `json:"text"`
	Rule   string       `json:"rule"`
	Amount money.Amount `json:"amount,omitempty"`
}

// MoneyItem is a reconciling item between the bank statement and
// Ledgerline's records.
type MoneyItem struct {
	Side    string       `json:"side"` // In the bank statement only, In Ledgerline only
	Account string       `json:"account"`
	Date    string       `json:"date"`
	Ref     string       `json:"ref"`
	Amount  money.Amount `json:"amount"`
	DrCr    string       `json:"dr_cr"`
	What    string       `json:"what"`
}

// MoneySign is a step of the statement: prepared, signed off, sent back.
type MoneySign struct {
	By   string `json:"by"`
	At   string `json:"at"`
	Note string `json:"note,omitempty"`
}

// MoneyVersion is an earlier prepared copy of a day's statement.
type MoneyVersion struct {
	Hash     string     `json:"hash"`
	Prepared *MoneySign `json:"prepared,omitempty"`
	Signed   *MoneySign `json:"signed,omitempty"`
	FileID   string     `json:"file_id,omitempty"`
}

// MoneyRecon is a day's reconciliation statement.
type MoneyRecon struct {
	Date          string         `json:"date"`
	Settlement    bool           `json:"settlement"` // a running account settlement day
	SettlementWhy string         `json:"settlement_why,omitempty"`
	Register      int            `json:"register"` // accounts in the firm's register
	Moves         []MoneyMove    `json:"moves"`
	Totals        []MoneyTotal   `json:"totals"`
	Charges       MoneyCharges   `json:"charges"`
	Pool          MoneyPool      `json:"pool"`
	Accounts      []MoneyAccount `json:"accounts"`
	Lines         []BankLine     `json:"lines"`
	Unmatched     []MoneyItem    `json:"unmatched"`
	Flags         []MoneyFlag    `json:"flags"`
	Notes         []string       `json:"notes"`
	Hash          string         `json:"hash"`
	Status        string         `json:"status"` // Not prepared, Prepared, Signed off, Sent back
	Prepared      *MoneySign     `json:"prepared,omitempty"`
	Signed        *MoneySign     `json:"signed,omitempty"`
	Returned      *MoneySign     `json:"returned,omitempty"`
	FileID        string         `json:"file_id,omitempty"`
	FileName      string         `json:"file_name,omitempty"`
	History       []MoneyVersion `json:"history,omitempty"`
	Changed       bool           `json:"changed"` // the records changed after it was prepared
	CanPrepare    bool           `json:"can_prepare"`
	CanSign       bool           `json:"can_sign"`
}

// moneyCtx is what classifying the ledger's entries reads.
type moneyCtx struct {
	set     Settings
	reg     []FirmBank
	txns    map[string]BankTxn    // statement credits by reference
	lines   map[string]BankLine   // statement lines by reference|CR/DR
	rcpts   map[string]Receipt    // receipts by ID
	payouts map[string]PayoutItem // payout items by request
	rasFrom map[string]string     // a settlement date's paying account
	xfers   map[string]FirmTransfer
	byID    map[string]ledger.Entry
}

func (o *Ops) moneyCtx(ctx context.Context, tenant string) (*moneyCtx, error) {
	set, err := o.Settings(ctx, tenant)
	if err != nil {
		return nil, err
	}
	m := &moneyCtx{set: set, reg: o.firmBanks(ctx, tenant), txns: map[string]BankTxn{}, lines: map[string]BankLine{}, rcpts: map[string]Receipt{}, payouts: map[string]PayoutItem{}, rasFrom: map[string]string{}, xfers: map[string]FirmTransfer{}, byID: map[string]ledger.Entry{}}
	ts, _ := docs.List[BankTxn](ctx, o.Docs, tenant, cBankTxn, "")
	for _, t := range ts {
		m.txns[t.Ref] = t
	}
	ls, _ := docs.List[BankLine](ctx, o.Docs, tenant, cBankLine, "")
	for _, l := range ls {
		m.lines[l.ID] = l
	}
	rs, _ := docs.List[Receipt](ctx, o.Docs, tenant, cReceipt, "")
	for _, r := range rs {
		m.rcpts[r.ID] = r
	}
	bs, _ := docs.List[PayoutBatch](ctx, o.Docs, tenant, cPOBatch, "")
	for _, b := range bs {
		for _, it := range b.Items {
			m.payouts[it.Request] = it
		}
	}
	rb, _ := docs.List[RASBatch](ctx, o.Docs, tenant, cRASBatch, "")
	for _, b := range rb {
		if b.PaidFrom != "" && m.rasFrom[b.Date] == "" {
			m.rasFrom[b.Date] = b.PaidFrom
		}
	}
	xs, _ := docs.List[FirmTransfer](ctx, o.Docs, tenant, cFirmXfer, "")
	for _, x := range xs {
		m.xfers[x.ID] = x
	}
	return m, nil
}

// end is a firm account as one end of a movement: the register's account
// with that number, the usual account of the kind when no number is
// recorded (assumed), or the number alone when it is not in the register.
func (m *moneyCtx) end(acct, kind, exchange string) (label, k, id string, assumed bool) {
	if acct != "" {
		if b, ok := firmBankByAccount(m.reg, acct); ok {
			return b.Label(), b.Kind, b.ID, false
		}
		return MaskAccount(acct) + " (not in the firm's bank accounts)", "", "", false
	}
	if b, ok := defaultFirmBank(m.reg, kind, exchange); ok {
		return b.Label(), b.Kind, b.ID, true
	}
	return kind + " (not in the firm's bank accounts)", kind, "", true
}

// bankDeltas are an entry's net debits on the bank and clearing accounts.
type bankDeltas struct {
	client, own, cc money.Amount
	ucc             string // the client whose ledger it touches
	clientDr        bool   // the client's ledgers are debited on balance
}

func deltasOf(e ledger.Entry) bankDeltas {
	var d bankDeltas
	var net money.Amount
	for _, p := range e.Postings {
		x := p.Debit - p.Credit
		switch {
		case p.Account == ledger.BankClient:
			d.client += x
		case p.Account == ledger.BankOwn:
			d.own += x
		case strings.HasPrefix(p.Account, "CC:"):
			d.cc += x
		default:
			if u, _, ok := ledger.ClientCode(p.Account); ok {
				if d.ucc == "" {
					d.ucc = u
				}
				net += x
			}
		}
	}
	d.clientDr = net > 0
	return d
}

// moves classifies one ledger entry into the movements of money it records.
// Only entries that move the client bank account count.
func (m *moneyCtx) moves(e ledger.Entry) []MoneyMove {
	if e.Source.Kind == "OPENING" {
		return nil // a balance brought in, not a transfer
	}
	d := deltasOf(e)
	if d.client == 0 {
		return nil
	}
	if e.ReversalOf != "" {
		orig, ok := m.byID[e.ReversalOf]
		if !ok {
			return m.generic(e, d)
		}
		var out []MoneyMove
		for _, mv := range m.moves(orig) {
			mv.From, mv.To = mv.To, mv.From
			mv.FromKind, mv.ToKind = mv.ToKind, mv.FromKind
			mv.FromID, mv.ToID = mv.ToID, mv.FromID
			mv.Date, mv.Entry, mv.Purpose, mv.Group, mv.Flags = e.ValueDate, e.ID, "Reversal: "+mv.Purpose, mvOther, nil
			out = append(out, mv)
		}
		return out
	}
	amt := d.client
	if amt < 0 {
		amt = -amt
	}
	mk := func(group, purpose string) MoneyMove {
		return MoneyMove{Date: e.ValueDate, Amount: amt, Group: group, Purpose: purpose, Entry: e.ID, Source: e.Narration}
	}
	client := func(mv *MoneyMove, from bool) {
		name, kind := "Client "+d.ucc, "Client"
		if d.ucc == "" {
			name = "Client"
		}
		if from {
			mv.From, mv.FromKind = name, kind
		} else {
			mv.To, mv.ToKind = name, kind
		}
	}
	firm := func(mv *MoneyMove, from bool, acct, kind, exchange string) {
		l, k, id, as := m.end(acct, kind, exchange)
		if from {
			mv.From, mv.FromKind, mv.FromID = l, k, id
		} else {
			mv.To, mv.ToKind, mv.ToID = l, k, id
		}
		mv.Assumed = mv.Assumed || as
	}
	key, _, _ := strings.Cut(e.Key, ":")
	switch key {
	case "RCPT", "SUSP":
		ref := e.Source.ID
		t := m.txns[ref]
		mv := mk(mvReceipt, "Funds received ("+firstOf(t.Mode, "bank")+")")
		mv.Ref, mv.Source = ref, "Bank credit "+ref
		if key == "SUSP" {
			mv.Purpose, mv.From, mv.FromKind = "Credit parked in suspense until identified or returned", "Remitter (not identified)", "Remitter"
		} else {
			client(&mv, true)
		}
		firm(&mv, false, m.creditAccount(ref, t), BankUSCNBA, earmarkExchange(t.Earmark, t.Ledger))
		return []MoneyMove{mv}
	case "RET":
		ref := e.Source.ID
		t := m.txns[ref]
		mv := mk(mvOther, "Returned to the remitter (not the client's registered account)")
		mv.Ref, mv.Source, mv.To, mv.ToKind = ref, "Bank credit "+ref+" returned", "Remitter", "Remitter"
		firm(&mv, true, m.creditAccount(ref, t), BankUSCNBA, earmarkExchange(t.Earmark, t.Ledger))
		return []MoneyMove{mv}
	case "RCV":
		r := m.rcpts[e.Source.ID]
		mv := mk(mvReceipt, "Funds received ("+firstOf(r.Mode, "receipt")+")")
		mv.Ref, mv.Source = firstOf(r.Ref, e.Source.ID), "Receipt "+e.Source.ID
		client(&mv, true)
		firm(&mv, false, r.ToAccount, BankUSCNBA, earmarkExchange(r.Earmark, r.Ledger))
		return []MoneyMove{mv}
	case "UPS":
		mv := mk(mvUpstream, "Upstream to the clearing corporation")
		mv.Ref, mv.Source, mv.To, mv.ToKind = e.Source.ID, "Upstream "+e.Source.ID, "Clearing corporation", BankCC
		firm(&mv, true, "", BankUSCNBA, "")
		return []MoneyMove{mv}
	case "PAY":
		it := m.payouts[e.Source.ID]
		from := firstOf(it.DebitAccount, m.set.PayoutDebitAccount)
		mv := mk(mvPayout, "Payout on the client's request")
		mv.Ref, mv.Source = firstOf(it.UTR, e.Source.ID), "Payout "+e.Source.ID
		firm(&mv, true, from, BankDSCNBA, "")
		client(&mv, false)
		return []MoneyMove{mv}
	case "RAS":
		date, _, _ := strings.Cut(e.Source.ID, "/")
		from := firstOf(m.rasFrom[date], m.set.PayoutDebitAccount)
		mv := mk(mvPayout, "Running account settlement of "+fmtDay(date))
		mv.Ref, mv.Source = e.Source.ID, "Running account settlement "+date
		firm(&mv, true, from, BankDSCNBA, "")
		client(&mv, false)
		return []MoneyMove{mv}
	case "XFR":
		t, ok := m.xfers[e.Source.ID]
		if !ok {
			return m.generic(e, d)
		}
		return []MoneyMove{m.transferMove(t, e.ID)}
	}
	return m.generic(e, d)
}

// creditAccount is the firm account a bank credit came into: the
// statement line's, else the credit's own (kept with a separate commodity
// ledger or an MCX-only account).
func (m *moneyCtx) creditAccount(ref string, t BankTxn) string {
	if l, ok := m.lines[ref+"|CR"]; ok && l.Account != "" {
		return l.Account
	}
	return t.ToAccount
}

func earmarkExchange(earmark, ledgerKind string) string {
	if earmark == EarmarkMCX || ledgerKind == ledger.LedgerCommodity {
		return "MCX"
	}
	return ""
}

// transferMove is a recorded transfer on the statement.
func (m *moneyCtx) transferMove(t FirmTransfer, entry string) MoneyMove {
	p, _ := moneyPurpose(t.Purpose)
	fk, fl, _ := moneyEnd(m.reg, t.From)
	tk, tl, _ := moneyEnd(m.reg, t.To)
	mv := MoneyMove{Date: t.Date, From: fl, FromKind: fk, To: tl, ToKind: tk, Amount: t.Amount, Purpose: p.Name, Group: p.Group, Ref: t.Ref, Entry: entry, Source: "Transfer " + t.ID}
	if t.From != "CC" {
		mv.FromID = t.From
	}
	if t.To != "CC" {
		mv.ToID = t.To
	}
	if t.Segment != "" {
		mv.Purpose += " (" + t.Segment + ")"
	}
	if w, ok := withdrawReason(t.Withdraw); ok {
		mv.Purpose += ": " + w.Name
	}
	return mv
}

// generic reads an entry Ledgerline has no rule for from what it posts.
func (m *moneyCtx) generic(e ledger.Entry, d bankDeltas) []MoneyMove {
	var out []MoneyMove
	base := MoneyMove{Date: e.ValueDate, Entry: e.ID, Source: e.Narration, Ref: e.Source.ID}
	rest := d.client
	switch {
	case d.own < 0 && d.client > 0: // own money into the client bank account
		a := min(-d.own, d.client)
		mv := base
		mv.Amount, mv.Group, mv.Purpose = a, mvFromOwn, "Own funds into the client bank account"
		l, k, id, _ := m.end("", BankOwn, "")
		mv.From, mv.FromKind, mv.FromID = l, k, id
		l, k, id, as := m.end("", BankUSCNBA, "")
		mv.To, mv.ToKind, mv.ToID, mv.Assumed = l, k, id, as
		out, rest = append(out, mv), d.client-a
	case d.own > 0 && d.client < 0: // client bank to own
		a := min(d.own, -d.client)
		mv := base
		mv.Amount, mv.Group, mv.Purpose = a, mvToOwn, "Client bank account to the own account"
		l, k, id, as := m.end("", BankUSCNBA, "")
		mv.From, mv.FromKind, mv.FromID, mv.Assumed = l, k, id, as
		l, k, id, _ = m.end("", BankOwn, "")
		mv.To, mv.ToKind, mv.ToID = l, k, id
		out, rest = append(out, mv), d.client+a
	}
	if rest == 0 {
		return out
	}
	mv := base
	mv.Amount = rest
	if rest < 0 {
		mv.Amount = -rest
	}
	switch {
	case rest > 0 && d.cc < 0:
		mv.Group, mv.Purpose = mvFromCC, "Received from the clearing corporation"
		mv.From, mv.FromKind = "Clearing corporation", BankCC
		l, k, id, as := m.end("", BankDSCNBA, "")
		mv.To, mv.ToKind, mv.ToID, mv.Assumed = l, k, id, as
	case rest < 0 && d.cc > 0:
		mv.Group, mv.Purpose = mvUpstream, "Sent to the clearing corporation"
		l, k, id, as := m.end("", BankUSCNBA, "")
		mv.From, mv.FromKind, mv.FromID, mv.Assumed = l, k, id, as
		mv.To, mv.ToKind = "Clearing corporation", BankCC
	case rest > 0 && e.Source.Kind == "GATEWAY":
		mv.Group, mv.Purpose = mvReceipt, "Payment gateway's settlement of clients' online payments"
		mv.From, mv.FromKind = "Payment gateway", "Payment gateway"
		l, k, id, as := m.end("", BankUSCNBA, "")
		mv.To, mv.ToKind, mv.ToID, mv.Assumed = l, k, id, as
	case rest > 0 && d.ucc != "" && !d.clientDr:
		mv.Group, mv.Purpose = mvReceipt, "Funds received"
		mv.From, mv.FromKind = "Client "+d.ucc, "Client"
		l, k, id, as := m.end("", BankUSCNBA, "")
		mv.To, mv.ToKind, mv.ToID, mv.Assumed = l, k, id, as
	case rest < 0 && d.ucc != "" && d.clientDr:
		mv.Group, mv.Purpose = mvPayout, "Paid to the client"
		l, k, id, as := m.end("", BankDSCNBA, "")
		mv.From, mv.FromKind, mv.FromID, mv.Assumed = l, k, id, as
		mv.To, mv.ToKind = "Client "+d.ucc, "Client"
	default:
		mv.Group, mv.Purpose = mvOther, "Other movement of the client bank account"
		l, k, id, as := m.end("", BankUSCNBA, "")
		if rest > 0 {
			mv.From, mv.FromKind = "Other", ""
			mv.To, mv.ToKind, mv.ToID, mv.Assumed = l, k, id, as
		} else {
			mv.From, mv.FromKind, mv.FromID, mv.Assumed = l, k, id, as
			mv.To, mv.ToKind = "Other", ""
		}
	}
	return append(out, mv)
}

// chargesOf are the charges an entry debits to clients: what it credits
// to income and payable accounts when it posts to a client's ledger.
func chargesOf(e ledger.Entry, c *MoneyCharges) {
	touches := false
	for _, p := range e.Postings {
		if ledger.IsClientAccount(p.Account) {
			touches = true
		}
	}
	if !touches {
		return
	}
	for _, p := range e.Postings {
		if strings.HasPrefix(p.Account, "INCOME:") || strings.HasPrefix(p.Account, "PAYABLE:") {
			c.add(p.Account, p.Credit-p.Debit)
		}
	}
}

// rasSettlementDay says whether a day is a running account settlement day:
// a day of the exchanges' calendar, or a day the firm ran a settlement.
func (o *Ops) rasSettlementDay(ctx context.Context, tenant, date string) (bool, string) {
	for _, r := range o.CompProfile(ctx, tenant).RAS {
		if contains(r.Dates, date) {
			return true, r.Kind + " running account settlement " + r.Period
		}
	}
	if bs, _ := o.RASBatches(ctx, tenant, date); len(bs) > 0 {
		return true, "a running account settlement was run"
	}
	return false, ""
}

// buildMoneyRecon works out a day's statement from Ledgerline's records.
func (o *Ops) buildMoneyRecon(ctx context.Context, tenant, date string) (MoneyRecon, error) {
	r := MoneyRecon{Date: date, Moves: []MoneyMove{}, Totals: []MoneyTotal{}, Accounts: []MoneyAccount{}, Lines: []BankLine{}, Unmatched: []MoneyItem{}, Flags: []MoneyFlag{}, Notes: []string{}, Status: "Not prepared"}
	m, err := o.moneyCtx(ctx, tenant)
	if err != nil {
		return r, err
	}
	r.Register = len(m.reg)
	r.Settlement, r.SettlementWhy = o.rasSettlementDay(ctx, tenant, date)
	es, err := o.App.Ledger.Store.List(ctx, tenant, ledger.Filter{To: date})
	if err != nil {
		return r, err
	}
	for _, e := range es {
		m.byID[e.ID] = e
	}
	// Every movement to the day, for the book balances and the backing of
	// transfers to the own account; the day's are listed.
	type bal struct{ opening, in, out money.Amount }
	bals := map[string]*bal{}
	var charges, today MoneyCharges
	for _, b := range m.reg {
		bals[b.ID] = &bal{opening: b.Opening}
	}
	after := func(id, day string) bool {
		b, ok := firmBankByID(m.reg, id)
		return ok && (b.OpeningOn == "" || day > b.OpeningOn)
	}
	recorded := map[string]bool{} // transfers posted in the ledger
	addMove := func(mv MoneyMove) {
		for _, side := range []struct {
			id  string
			out bool
		}{{mv.FromID, true}, {mv.ToID, false}} {
			x := bals[side.id]
			if side.id == "" || x == nil || !after(side.id, mv.Date) {
				continue
			}
			switch {
			case mv.Date < date && side.out:
				x.opening -= mv.Amount
			case mv.Date < date:
				x.opening += mv.Amount
			case side.out:
				x.out += mv.Amount
			default:
				x.in += mv.Amount
			}
		}
		switch mv.Group {
		case mvToOwn:
			r.Pool.ToOwn += mv.Amount
		case mvFromOwn:
			r.Pool.OwnIn += mv.Amount
		}
		if mv.Date == date {
			r.Moves = append(r.Moves, mv)
		}
	}
	for _, e := range es {
		chargesOf(e, &charges)
		if e.ValueDate == date {
			chargesOf(e, &today)
		}
		if e.Source.Kind == "FIRMXFER" {
			recorded[e.Source.ID] = true
		}
		for _, mv := range m.moves(e) {
			addMove(mv)
		}
	}
	// Transfers between the client money accounts post nothing.
	for _, id := range sortedKeys(m.xfers) {
		t := m.xfers[id]
		if t.Status == "Recorded" && t.Date <= date && !recorded[id] {
			addMove(m.transferMove(t, ""))
		}
	}
	r.Charges = today
	r.Pool.Charges = charges.Total
	r.Pool.Available = r.Pool.Charges + r.Pool.OwnIn - r.Pool.ToOwn
	sort.SliceStable(r.Moves, func(i, j int) bool {
		gi, gj := indexOf(moneyGroupOrder, r.Moves[i].Group), indexOf(moneyGroupOrder, r.Moves[j].Group)
		if gi != gj {
			return gi < gj
		}
		return r.Moves[i].Entry < r.Moves[j].Entry
	})
	// Totals.
	tot := map[string]*MoneyTotal{}
	for _, mv := range r.Moves {
		t := tot[mv.Group]
		if t == nil {
			t = &MoneyTotal{Group: mv.Group, Name: moneyGroupNames[mv.Group]}
			tot[mv.Group] = t
		}
		t.Count++
		t.Amount += mv.Amount
	}
	for _, g := range moneyGroupOrder {
		if t := tot[g]; t != nil {
			r.Totals = append(r.Totals, *t)
		}
	}
	stmt := o.moneyStatementLines(m, &r)
	// Accounts.
	for _, b := range m.reg {
		x := bals[b.ID]
		if !b.Active && x.in == 0 && x.out == 0 && x.opening == 0 {
			continue
		}
		a := MoneyAccount{ID: b.ID, Kind: b.Kind, Label: b.Label(), Opening: x.opening, In: x.in, Out: x.out, Closing: x.opening + x.in - x.out}
		a.Lines, a.Statement = stmt[b.ID].lines, stmt[b.ID].balance
		if a.Statement != "" {
			if sb, err := money.ParseDec(a.Statement); err == nil {
				a.Difference = sb.Sub(a.Closing.Dec()).Fixed(2)
			}
		}
		r.Accounts = append(r.Accounts, a)
	}
	o.moneyFlags(m, &r)
	o.moneyNilFlags(ctx, tenant, &r)
	r.Hash = moneyHash(r)
	return r, nil
}

// moneyNilFlags flags client money accounts that end the day with money
// no stipulated reason keeps there. Client funds are upstreamed every day
// and none kept overnight (SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2023/84 para 3,
// NSE/INSP/57041): what the USCNBA and settlement accounts hold at the end
// of the day is nil but for money received after the cut-off, FDRs and
// MFOS in transit, OFS refunds and a bank's failure; the DSCNBA's is nil
// but for payouts in the bank's hands, uncleared cheques and running
// account settlement days (NCL/CMPL/61186; NSE/INSP/73976 Exhibit 29).
// The DSCNBA's own flag (moneyFlags) stays for the settlement-day rule.
func (o *Ops) moneyNilFlags(ctx context.Context, tenant string, r *MoneyRecon) {
	if r.Register == 0 {
		return
	}
	var rr *RetentionReport
	kept := map[string]money.Dec{}
	for _, a := range r.Accounts {
		if a.Kind != BankUSCNBA && a.Kind != BankSettlement && a.Kind != BankDSCNBA {
			continue
		}
		bal := a.Closing.Dec()
		if a.Statement != "" {
			if sb, err := money.ParseDec(a.Statement); err == nil {
				bal = sb
			}
		}
		if bal.Sign() <= 0 {
			continue
		}
		if rr == nil {
			x, err := o.retentionReport(ctx, tenant, r.Date, nil, nil)
			if err != nil {
				return
			}
			rr = &x
		}
		group := a.Kind
		if group == BankSettlement {
			group = BankUSCNBA
		}
		kept[group] = kept[group].Add(bal)
	}
	// A DSCNBA balance the stipulated reasons explain (payouts with the
	// bank, uncleared cheques) is not a balance left over.
	if bal := kept[BankDSCNBA]; bal.Sign() > 0 && bal.Cmp(rr.justifiedIn(BankDSCNBA)) <= 0 {
		fs := r.Flags[:0]
		for _, f := range r.Flags {
			if f.Kind != "dscnba_balance" || !strings.Contains(f.Text, "not a running account settlement day") {
				fs = append(fs, f)
			}
		}
		r.Flags = fs
	}
	for _, k := range []string{BankUSCNBA, BankDSCNBA} {
		bal := kept[k]
		if bal.Sign() <= 0 {
			continue
		}
		ok := rr.justifiedIn(k)
		over := bal.Sub(ok)
		if over.Round(money.UnitPaisa) <= 0 {
			continue
		}
		what := "The USCNBA and settlement accounts end"
		if k == BankDSCNBA {
			continue // moneyFlags' dscnba_balance flag says it (below)
		}
		r.Flags = append(r.Flags, MoneyFlag{Kind: "not_upstreamed", Text: fmt.Sprintf("%s the day with ₹%s; the stipulated reasons recorded for the day explain ₹%s, so ₹%s was not upstreamed", what, formatINR(bal), formatINR(ok), formatINR(over)),
			Rule: "Client funds are upstreamed to the clearing corporation every day; money is kept only for NSE Clearing's stipulated reasons RC01 to RC08, reported in SEGMON (SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2023/84 para 3; NCL/CMPL/61186)", Amount: over.Round(money.UnitPaisa)})
	}
}

func indexOf(xs []string, x string) int {
	for i, y := range xs {
		if y == x {
			return i
		}
	}
	return len(xs)
}

// stmtInfo is what a day's statement lines say of one of the firm's
// accounts: how many lines, and the balance after the last.
type stmtInfo struct {
	lines   int
	balance string
}

// moneyStatementLines puts the day's bank statement lines on the statement
// and matches them to the movements by reference and amount.
func (o *Ops) moneyStatementLines(m *moneyCtx, r *MoneyRecon) map[string]stmtInfo {
	info := map[string]stmtInfo{}
	var ls []BankLine
	for _, l := range m.lines {
		if l.Date == r.Date {
			ls = append(ls, l)
		}
	}
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].Account != ls[j].Account {
			return ls[i].Account < ls[j].Account
		}
		if ls[i].Time != ls[j].Time {
			return ls[i].Time < ls[j].Time
		}
		return ls[i].Seq < ls[j].Seq
	})
	matched := map[int]bool{}
	onStatement := map[string]bool{} // register accounts with lines loaded for the day
	for _, l := range ls {
		acctID := ""
		if b, ok := firmBankByAccount(m.reg, l.Account); ok {
			acctID = b.ID
		} else if l.Account == "" {
			if b, ok := defaultFirmBank(m.reg, BankUSCNBA, ""); ok && l.DrCr == "CR" {
				acctID = b.ID
			}
		}
		if acctID != "" {
			onStatement[acctID] = true
			x := info[acctID]
			x.lines++
			if l.Balance != "" {
				x.balance = l.Balance
			}
			info[acctID] = x
		}
		found := false
		for i, mv := range r.Moves {
			if matched[i] || mv.Ref != l.Ref || mv.Amount != l.Amount {
				continue
			}
			matched[i], found = true, true
			break
		}
		if !found {
			r.Unmatched = append(r.Unmatched, MoneyItem{Side: "In the bank statement only", Account: firstOf(MaskAccount(l.Account), "client account statement"), Date: l.Date, Ref: l.Ref, Amount: l.Amount, DrCr: l.DrCr, What: firstOf(l.Party, l.Mode)})
		}
		r.Lines = append(r.Lines, l.Public())
	}
	for i, mv := range r.Moves {
		if matched[i] {
			continue
		}
		for _, side := range []struct{ id, dc string }{{mv.FromID, "DR"}, {mv.ToID, "CR"}} {
			if side.id != "" && onStatement[side.id] {
				b, _ := firmBankByID(m.reg, side.id)
				r.Unmatched = append(r.Unmatched, MoneyItem{Side: "In Ledgerline only", Account: b.Label(), Date: mv.Date, Ref: mv.Ref, Amount: mv.Amount, DrCr: side.dc, What: mv.Purpose})
				break
			}
		}
	}
	return info
}

// moneyFlags adds what needs explaining.
func (o *Ops) moneyFlags(m *moneyCtx, r *MoneyRecon) {
	flag := func(i int, kind, text, rule string, amt money.Amount) {
		if i >= 0 {
			r.Moves[i].Flags = append(r.Moves[i].Flags, text)
		}
		r.Flags = append(r.Flags, MoneyFlag{Kind: kind, Text: text, Rule: rule, Amount: amt})
	}
	if len(m.reg) == 0 {
		r.Notes = append(r.Notes, "The firm's bank accounts are not named yet (Receipts & payouts → Client money reconciliation → Firm bank accounts): Ledgerline cannot say which account each movement went through, or check receipts, payments and the DSCNBA's balance.")
	}
	for i, mv := range r.Moves {
		switch {
		case len(m.reg) == 0:
		case mv.Group == mvReceipt && !mv.Assumed && mv.ToKind != BankUSCNBA:
			flag(i, "receipt_outside", fmt.Sprintf("₹%s from %s received in %s, not a USCNBA", formatINR(mv.Amount.Dec()), mv.From, mv.To),
				"Clients' funds are received only in the USCNBA (SEBI master circular 2025/90, para 15.3.2.1; SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2023/84, para 3.C.II)", mv.Amount)
		case mv.Group == mvPayout && !mv.Assumed && mv.FromKind != BankDSCNBA:
			flag(i, "payment_outside", fmt.Sprintf("₹%s to %s paid from %s, not a DSCNBA", formatINR(mv.Amount.Dec()), mv.To, mv.From),
				"Clients are paid only from the DSCNBA (SEBI master circular 2025/90, para 15.3.2.2; NSE/INSP/57959, FAQ 21)", mv.Amount)
		case mv.Group == mvInternal && mv.FromKind == BankUSCNBA && mv.ToKind == BankDSCNBA:
			flag(i, "uscnba_to_dscnba", fmt.Sprintf("₹%s moved from %s to %s", formatINR(mv.Amount.Dec()), mv.From, mv.To),
				"No money may move from a USCNBA to a DSCNBA: it goes to the clearing corporation first (NSE/INSP/57959, FAQs 17 and 21)", mv.Amount)
		}
	}
	if r.Pool.Available < 0 {
		short := -r.Pool.Available
		first := -1
		for i, mv := range r.Moves {
			if mv.Group == mvToOwn {
				if first < 0 {
					first = i
				} else {
					r.Moves[i].Flags = append(r.Moves[i].Flags, "not backed by charges debited to clients")
				}
			}
		}
		if first >= 0 {
			flag(first, "unbacked_own", fmt.Sprintf("₹%s more taken to the own account, to the end of the day, than the brokerage, charges and statutory dues debited to clients and the own funds put in", formatINR(short.Dec())),
				"Client money goes to the own account only for legitimate purposes: brokerage, statutory dues, or a debit balance the firm met (SEBI master circular 2025/90, para 15.4.4.2; NSE/INSP/55904)", short)
		} else {
			r.Notes = append(r.Notes, fmt.Sprintf("Still ₹%s taken to the own account beyond the charges debited to clients, from earlier days: bring it back or explain it.", formatINR(short.Dec())))
		}
	}
	for _, a := range r.Accounts {
		if a.Kind != BankDSCNBA {
			continue
		}
		bal, src := a.Closing.Dec(), "book"
		if a.Statement != "" {
			if sb, err := money.ParseDec(a.Statement); err == nil {
				bal, src = sb, "bank statement"
			}
		}
		switch {
		case bal.Sign() > 0 && !r.Settlement:
			flag(-1, "dscnba_balance", fmt.Sprintf("%s ends the day with ₹%s (%s balance) on a day that is not a running account settlement day", a.Label, formatINR(bal), src),
				"Nothing may be left in the DSCNBA at the end of the day, except on running account settlement days with the bank instructions given by then (NSE/INSP/58146, FAQ 1)", bal.Round(money.UnitPaisa))
		case bal.Sign() < 0:
			flag(-1, "dscnba_balance", fmt.Sprintf("%s ends the day at −₹%s (%s balance): more was paid from it than came in; record the funds that came back from the clearing corporation", a.Label, formatINR(bal.Neg()), src),
				"Clients are paid from the DSCNBA only out of funds that came back from the clearing corporation (SEBI/HO/MIRSD/MIRSD-PoD-1/P/CIR/2023/84, para 3.C.II)", bal.Neg().Round(money.UnitPaisa))
		}
	}
	assumed := 0
	for _, mv := range r.Moves {
		if mv.Assumed {
			assumed++
		}
	}
	if assumed > 0 && len(m.reg) > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%d movement%s show the usual account of their kind: nothing recorded says which account %s through.", assumed, plural(assumed), map[bool]string{true: "it went", false: "they went"}[assumed == 1]))
	}
	if len(r.Lines) == 0 {
		r.Notes = append(r.Notes, "No bank statement lines are loaded for the day (Files → BANK-STMT): the statement is from Ledgerline's records alone.")
	}
}

// moneyHash identifies what a statement says, to tell when its records
// changed after it was prepared.
func moneyHash(r MoneyRecon) string {
	b, _ := json.Marshal(struct {
		Moves     []MoneyMove
		Charges   MoneyCharges
		Pool      MoneyPool
		Accounts  []MoneyAccount
		Unmatched []MoneyItem
		Flags     []MoneyFlag
	}{r.Moves, r.Charges, r.Pool, r.Accounts, r.Unmatched, r.Flags})
	return sha256Hex(b)
}

// MoneyReconFor is a day's statement: the prepared copy when there is one
// (Changed when the records have changed since), else as it stands now.
func (o *Ops) MoneyReconFor(ctx context.Context, tenant, actor, date string) (MoneyRecon, error) {
	if err := o.require(ctx, tenant, actor, "Viewing the client money reconciliation", RoleMaker, RoleChecker, RoleCompliance, RoleRisk); err != nil {
		return MoneyRecon{}, err
	}
	if !validDate(date) {
		return MoneyRecon{}, invalid("give a date (YYYY-MM-DD)")
	}
	live, err := o.buildMoneyRecon(ctx, tenant, date)
	if err != nil {
		return live, err
	}
	out := live
	if rec, _, err := docs.Get[MoneyRecon](ctx, o.Docs, tenant, cMoneyRecon, date); err == nil {
		if rec.Status == "Prepared" || rec.Status == "Signed off" {
			out = rec
			out.Changed = rec.Hash != live.Hash
		} else {
			out.Status, out.Returned, out.History = rec.Status, rec.Returned, rec.History
		}
	}
	out.CanPrepare = o.require(ctx, tenant, actor, "Preparing the client money reconciliation", RoleMaker, RoleChecker) == nil && (out.Status != "Signed off" || out.Changed)
	out.CanSign = o.require(ctx, tenant, actor, "Signing off the client money reconciliation", RoleChecker) == nil && out.Status == "Prepared" && !out.Changed && (out.Prepared == nil || out.Prepared.By != actor)
	return out, nil
}

// PrepareMoneyRecon prepares a day's statement for a checker: the copy and
// its CSV are kept as they are now.
func (o *Ops) PrepareMoneyRecon(ctx context.Context, tenant, actor, date, note string) (MoneyRecon, error) {
	if err := o.require(ctx, tenant, actor, "Preparing the client money reconciliation", RoleMaker, RoleChecker); err != nil {
		return MoneyRecon{}, err
	}
	if !validDate(date) || date > o.now().Format("2006-01-02") {
		return MoneyRecon{}, invalid("give a day that has come (YYYY-MM-DD)")
	}
	r, err := o.buildMoneyRecon(ctx, tenant, date)
	if err != nil {
		return r, err
	}
	prev, _, err := docs.Get[MoneyRecon](ctx, o.Docs, tenant, cMoneyRecon, date)
	if err == nil {
		r.History = prev.History
		switch {
		case prev.Status == "Signed off" && prev.Hash == r.Hash:
			return prev, fmt.Errorf("%w: the statement of %s is signed off and nothing in it has changed", ErrState, fmtDay(date))
		case prev.Status == "Prepared" || prev.Status == "Signed off":
			r.History = append(r.History, MoneyVersion{Hash: prev.Hash, Prepared: prev.Prepared, Signed: prev.Signed, FileID: prev.FileID})
		}
	}
	r.Status, r.Prepared, r.Signed, r.Returned = "Prepared", &MoneySign{By: actor, At: o.now().Format(time.RFC3339), Note: clip(strings.TrimSpace(note), 300)}, nil, nil
	r.FileName = "ClientMoneyReconciliation_" + date + ".csv"
	f, err := o.keepFile(ctx, tenant, r.FileName, MoneyReconCSV(r))
	if err != nil {
		return r, err
	}
	r.FileID = f.ID
	if err := docs.Upsert(ctx, o.Docs, tenant, cMoneyRecon, date, r, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "money.recon.prepared", date, map[string]any{"moves": len(r.Moves), "flags": len(r.Flags), "hash": r.Hash})
	if prev.Status == "Sent back" {
		o.closeWork(ctx, tenant, actor, wMoneyRecon, moneyPrepKey(date, len(prev.History)), "prepared")
	}
	if prev.Status == "Prepared" || prev.Status == "Signed off" {
		o.closeWork(ctx, tenant, actor, wMoneyRecon, moneyWorkKey(date, len(r.History)-1), "prepared again")
	}
	o.moneyReconQueue(ctx, tenant, date, len(r.History))
	return o.MoneyReconFor(ctx, tenant, actor, date)
}

// moneyWorkKey is the sign-off work item of a version of a day's statement
// (a statement prepared again is a new item).
func moneyWorkKey(date string, version int) string {
	return fmt.Sprintf("%s|%d", date, version+1)
}

// moneyPrepKey is the maker's work item for a statement sent back (n: the
// versions kept by then), so a second sending back is a new item.
func moneyPrepKey(date string, n int) string {
	return fmt.Sprintf("prepare:%s|%d", date, n)
}

// SignMoneyRecon signs off a prepared statement. The checker is not the
// one who prepared it, and the records must not have changed since.
func (o *Ops) SignMoneyRecon(ctx context.Context, tenant, actor, date, note string) (MoneyRecon, error) {
	if err := o.require(ctx, tenant, actor, "Signing off the client money reconciliation", RoleChecker); err != nil {
		return MoneyRecon{}, err
	}
	rec, _, err := docs.Get[MoneyRecon](ctx, o.Docs, tenant, cMoneyRecon, date)
	if err != nil || rec.Status != "Prepared" {
		return rec, fmt.Errorf("%w: the statement of %s is not prepared for sign-off", ErrState, fmtDay(date))
	}
	if rec.Prepared != nil && rec.Prepared.By == actor {
		return rec, fmt.Errorf("%w: the one who prepared the statement cannot sign it off", ErrForbidden)
	}
	live, err := o.buildMoneyRecon(ctx, tenant, date)
	if err != nil {
		return rec, err
	}
	if live.Hash != rec.Hash {
		return rec, fmt.Errorf("%w: the records of %s changed after the statement was prepared: prepare it again, then sign it off", ErrState, fmtDay(date))
	}
	rec.Status, rec.Signed = "Signed off", &MoneySign{By: actor, At: o.now().Format(time.RFC3339), Note: clip(strings.TrimSpace(note), 300)}
	if f, err := o.keepFile(ctx, tenant, rec.FileName, MoneyReconCSV(rec)); err == nil {
		rec.FileID = f.ID // the CSV with the sign-off on it
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cMoneyRecon, date, rec, actor); err != nil {
		return rec, err
	}
	o.log(ctx, tenant, actor, "money.recon.signed", date, map[string]any{"flags": len(rec.Flags), "hash": rec.Hash})
	o.closeWork(ctx, tenant, actor, wMoneyRecon, moneyWorkKey(date, len(rec.History)), "signed off")
	return o.MoneyReconFor(ctx, tenant, actor, date)
}

// ReturnMoneyRecon sends a prepared statement back to the maker, with the
// reason.
func (o *Ops) ReturnMoneyRecon(ctx context.Context, tenant, actor, date, reason string) (MoneyRecon, error) {
	if err := o.require(ctx, tenant, actor, "Signing off the client money reconciliation", RoleChecker); err != nil {
		return MoneyRecon{}, err
	}
	reason = clip(strings.TrimSpace(reason), 300)
	if reason == "" {
		return MoneyRecon{}, invalid("say what is to be done before it is signed off")
	}
	rec, _, err := docs.Get[MoneyRecon](ctx, o.Docs, tenant, cMoneyRecon, date)
	if err != nil || rec.Status != "Prepared" {
		return rec, fmt.Errorf("%w: the statement of %s is not prepared for sign-off", ErrState, fmtDay(date))
	}
	key := moneyWorkKey(date, len(rec.History))
	rec.History = append(rec.History, MoneyVersion{Hash: rec.Hash, Prepared: rec.Prepared, FileID: rec.FileID})
	rec.Status, rec.Returned = "Sent back", &MoneySign{By: actor, At: o.now().Format(time.RFC3339), Note: reason}
	if err := docs.Upsert(ctx, o.Docs, tenant, cMoneyRecon, date, rec, actor); err != nil {
		return rec, err
	}
	o.log(ctx, tenant, actor, "money.recon.returned", date, map[string]any{"reason": reason})
	o.closeWork(ctx, tenant, actor, wMoneyRecon, key, "sent back: "+reason)
	maker := ""
	if rec.Prepared != nil {
		maker = rec.Prepared.By
	}
	o.raiseWork(ctx, tenant, wMoneyRecon, moneyPrepKey(date, len(rec.History)), "Prepare the client money reconciliation of "+fmtDay(date)+" again", fmt.Sprintf("Sent back by %s: %s (prepared by %s).", actor, reason, firstOf(maker, "Ledgerline")), "", RoleMaker, "clientmoney", date)
	return o.MoneyReconFor(ctx, tenant, actor, date)
}

// moneyReconQueue asks a checker to sign off a prepared statement (version
// is how many copies were prepared before it).
func (o *Ops) moneyReconQueue(ctx context.Context, tenant, date string, version int) {
	set, _ := o.Settings(ctx, tenant)
	rc := &runCtx{o: o, ctx: ctx, tenant: tenant, date: o.businessDate(ctx, tenant), process: wMoneyRecon, keys: map[string]bool{}}
	rc.queue(moneyWorkKey(date, version), "Sign off the client money reconciliation of "+fmtDay(date), "Check the day's movements between the clients, the USCNBA, the clearing corporation, the DSCNBA, the settlement and own accounts, explain what is flagged, and sign it off on Client money reconciliation (SEBI master circular 2025/90, para 15.4.4.2).", "", RoleChecker, "clientmoney", set.NextBusinessDay(date)+" 18:00", nil)
}

// MoneyReconFile is the kept CSV of a prepared statement, or the statement
// as it stands when none is prepared.
func (o *Ops) MoneyReconFile(ctx context.Context, tenant, actor, date string) (string, []byte, error) {
	r, err := o.MoneyReconFor(ctx, tenant, actor, date)
	if err != nil {
		return "", nil, err
	}
	o.log(ctx, tenant, actor, "money.recon.download", date, map[string]any{"status": r.Status})
	if r.FileID != "" && !r.Changed {
		if f, err := o.App.Ingest.Raw.Get(ctx, tenant, r.FileID); err == nil {
			return r.FileName, f.Content, nil
		}
	}
	return "ClientMoneyReconciliation_" + date + "_draft.csv", MoneyReconCSV(r), nil
}

// MoneyReconCSV is the statement as a CSV file, in sections.
func MoneyReconCSV(r MoneyRecon) []byte {
	rows := [][]string{{"Daily reconciliation statement of client money transfers", r.Date}, {"Status", r.Status}}
	if r.Settlement {
		rows = append(rows, []string{"Running account settlement day", r.SettlementWhy})
	}
	rows = append(rows, []string{}, []string{"Date", "From", "From kind", "To", "To kind", "Amount (Rs)", "Purpose", "Reference", "Ledger entry", "Source", "Account assumed", "Flags"})
	for _, mv := range r.Moves {
		rows = append(rows, []string{mv.Date, mv.From, mv.FromKind, mv.To, mv.ToKind, mv.Amount.String(), mv.Purpose, mv.Ref, mv.Entry, mv.Source, map[bool]string{true: "Yes", false: ""}[mv.Assumed], strings.Join(mv.Flags, "; ")})
	}
	rows = append(rows, []string{}, []string{"Totals", "Movements", "Amount (Rs)"})
	for _, t := range r.Totals {
		rows = append(rows, []string{t.Name, fmt.Sprint(t.Count), t.Amount.String()})
	}
	rows = append(rows, []string{}, []string{"Charges debited to clients on the day", "Amount (Rs)"},
		[]string{"Brokerage", r.Charges.Brokerage.String()}, []string{"Other charges (interest, DP and others)", r.Charges.Other.String()},
		[]string{"Statutory dues and exchange charges", r.Charges.Statutory.String()}, []string{"Total", r.Charges.Total.String()},
		[]string{}, []string{"Transfers to the own account, to the end of the day", "Amount (Rs)"},
		[]string{"Charges debited to clients", r.Pool.Charges.String()}, []string{"Own funds put into client money accounts", r.Pool.OwnIn.String()},
		[]string{"Taken to the own account", r.Pool.ToOwn.String()}, []string{"Still backed", r.Pool.Available.String()})
	rows = append(rows, []string{}, []string{"Account", "Kind", "Opening (Rs)", "In (Rs)", "Out (Rs)", "Closing, book (Rs)", "Closing, bank statement (Rs)", "Difference (Rs)"})
	for _, a := range r.Accounts {
		rows = append(rows, []string{a.Label, a.Kind, a.Opening.String(), a.In.String(), a.Out.String(), a.Closing.String(), a.Statement, a.Difference})
	}
	if len(r.Unmatched) > 0 {
		rows = append(rows, []string{}, []string{"Reconciling item", "Account", "Date", "Reference", "Dr/Cr", "Amount (Rs)", "What"})
		for _, u := range r.Unmatched {
			rows = append(rows, []string{u.Side, u.Account, u.Date, u.Ref, u.DrCr, u.Amount.String(), u.What})
		}
	}
	rows = append(rows, []string{}, []string{"Flag", "Rule", "Amount (Rs)"})
	if len(r.Flags) == 0 {
		rows = append(rows, []string{"Nothing flagged", "", ""})
	}
	for _, f := range r.Flags {
		rows = append(rows, []string{f.Text, f.Rule, f.Amount.String()})
	}
	for _, n := range r.Notes {
		rows = append(rows, []string{"Note", n})
	}
	rows = append(rows, []string{})
	if r.Prepared != nil {
		rows = append(rows, []string{"Prepared by", r.Prepared.By, r.Prepared.At, r.Prepared.Note})
	}
	if r.Signed != nil {
		rows = append(rows, []string{"Signed off by", r.Signed.By, r.Signed.At, r.Signed.Note})
	}
	rows = append(rows, []string{"Statement hash", r.Hash})
	return CSV(rows)
}

var moneyReconTmpl = template.Must(template.New("mr").Funcs(template.FuncMap{"inr": func(a money.Amount) string { return formatINR(a.Dec()) }, "day": fmtDay}).Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Client money reconciliation {{.R.Date}}</title>
<style>body{font:13px/1.45 system-ui,sans-serif;color:#1d1d1b;max-width:1100px;margin:24px auto;padding:0 16px}h1{font-size:19px;margin:0 0 4px}h2{font-size:15px;margin:22px 0 6px}table{width:100%;border-collapse:collapse;margin:6px 0}td,th{border-bottom:1px solid #ddd;padding:5px 6px;text-align:left;vertical-align:top}td.r,th.r{text-align:right}.muted{color:#666;font-size:12px}.flag{color:#a4161a}.top{display:flex;justify-content:space-between;gap:16px;border-bottom:2px solid #1d1d1b;padding-bottom:10px}.sign{display:flex;gap:40px;margin-top:28px}.sign div{flex:1;border-top:1px solid #1d1d1b;padding-top:6px}@media print{body{margin:0}}</style></head><body>
<div class="top"><div><h1>Daily reconciliation statement of client money transfers</h1><div class="muted">{{.Firm}} · {{day .R.Date}}{{if .R.Settlement}} · running account settlement day{{end}} · {{.R.Status}}</div></div><div class="muted" style="text-align:right">SEBI master circular SEBI/HO/MIRSD/MIRSD-PoD/P/CIR/2025/90, para 15.4.4.2</div></div>
<h2>Movements</h2><table><tr><th>From</th><th>To</th><th class="r">Amount (₹)</th><th>Purpose</th><th>Reference</th><th>Entry</th></tr>
{{range .R.Moves}}<tr><td>{{.From}}</td><td>{{.To}}{{if .Assumed}} <span class="muted">(usual account)</span>{{end}}</td><td class="r">{{inr .Amount}}</td><td>{{.Purpose}}{{range .Flags}}<div class="flag">{{.}}</div>{{end}}</td><td>{{.Ref}}</td><td class="muted">{{.Entry}}</td></tr>{{else}}<tr><td colspan="6" class="muted">No money moved.</td></tr>{{end}}</table>
<h2>Totals</h2><table>{{range .R.Totals}}<tr><td>{{.Name}}</td><td class="r">{{.Count}}</td><td class="r">{{inr .Amount}}</td></tr>{{end}}</table>
<h2>Transfers to the own account</h2><table><tr><td>Charges debited to clients on the day (brokerage {{inr .R.Charges.Brokerage}}, other {{inr .R.Charges.Other}}, statutory {{inr .R.Charges.Statutory}})</td><td class="r">{{inr .R.Charges.Total}}</td></tr>
<tr><td>To the end of the day: charges debited to clients</td><td class="r">{{inr .R.Pool.Charges}}</td></tr><tr><td>Own funds put into client money accounts</td><td class="r">{{inr .R.Pool.OwnIn}}</td></tr><tr><td>Taken to the own account</td><td class="r">{{inr .R.Pool.ToOwn}}</td></tr><tr><th>Still backed</th><th class="r">{{inr .R.Pool.Available}}</th></tr></table>
<h2>Accounts</h2><table><tr><th>Account</th><th class="r">Opening</th><th class="r">In</th><th class="r">Out</th><th class="r">Closing (book)</th><th class="r">Bank statement</th></tr>{{range .R.Accounts}}<tr><td>{{.Label}}</td><td class="r">{{inr .Opening}}</td><td class="r">{{inr .In}}</td><td class="r">{{inr .Out}}</td><td class="r">{{inr .Closing}}</td><td class="r">{{.Statement}}</td></tr>{{else}}<tr><td colspan="6" class="muted">The firm's bank accounts are not named yet.</td></tr>{{end}}</table>
{{if .R.Unmatched}}<h2>Reconciling items</h2><table>{{range .R.Unmatched}}<tr><td>{{.Side}}</td><td>{{.Account}}</td><td>{{.Ref}}</td><td>{{.DrCr}}</td><td class="r">{{inr .Amount}}</td><td>{{.What}}</td></tr>{{end}}</table>{{end}}
<h2>Flags</h2>{{range .R.Flags}}<p class="flag"><b>{{.Text}}</b><br><span class="muted">{{.Rule}}</span></p>{{else}}<p>Nothing flagged.</p>{{end}}
{{range .R.Notes}}<p class="muted">{{.}}</p>{{end}}
<div class="sign"><div>Prepared by {{if .R.Prepared}}{{.R.Prepared.By}} · {{.R.Prepared.At}}{{end}}</div><div>Signed off by {{if .R.Signed}}{{.R.Signed.By}} · {{.R.Signed.At}}{{end}}</div></div>
<p class="muted">Statement hash {{.R.Hash}}</p></body></html>`))

// RenderMoneyRecon is the statement as a page to print.
func (o *Ops) RenderMoneyRecon(ctx context.Context, tenant, actor, date string) ([]byte, error) {
	r, err := o.MoneyReconFor(ctx, tenant, actor, date)
	if err != nil {
		return nil, err
	}
	set, _ := o.Settings(ctx, tenant)
	var buf bytes.Buffer
	err = moneyReconTmpl.Execute(&buf, map[string]any{"R": r, "Firm": set.Firm})
	return buf.Bytes(), err
}

// ---- Periodic work: client_money_recon ----

func (o *Ops) moneyReconDone(ctx context.Context, tenant, period string) (bool, string) {
	rec, _, err := docs.Get[MoneyRecon](ctx, o.Docs, tenant, cMoneyRecon, period)
	if err != nil {
		return false, ""
	}
	switch rec.Status {
	case "Signed off":
		return true, fmt.Sprintf("Signed off by %s; %d flagged", rec.Signed.By, len(rec.Flags))
	case "Prepared":
		return false, "Prepared; waiting for a checker to sign it off"
	case "Sent back":
		return false, "Sent back: " + rec.Returned.Note
	}
	return false, ""
}

// moneyReconRun prepares the day's statement (as Ledgerline) when no one
// has, or when its records changed since, and asks a checker to sign it
// off.
func (o *Ops) moneyReconRun(ctx context.Context, tenant, actor, period string) (string, []PeriodicFile, error) {
	rec, _, err := docs.Get[MoneyRecon](ctx, o.Docs, tenant, cMoneyRecon, period)
	switch {
	case err == nil && rec.Status == "Signed off":
		return fmt.Sprintf("Signed off by %s", rec.Signed.By), nil, nil
	case err == nil && rec.Status == "Sent back":
		// The maker answers the checker: it is not prepared again for them.
		why := ""
		if rec.Returned != nil {
			why = fmt.Sprintf(" by %s: %s", rec.Returned.By, rec.Returned.Note)
		}
		return "", nil, &pendingErr{"Sent back" + why + "; waiting for the maker to prepare it again", "Sent back"}
	}
	need := err != nil
	if !need && rec.Status == "Prepared" {
		if live, err := o.buildMoneyRecon(ctx, tenant, period); err == nil && live.Hash != rec.Hash {
			need = true
		}
	}
	if need {
		r, err := o.PrepareMoneyRecon(ctx, tenant, actor, period, "Prepared by Periodic work")
		if err != nil && !errors.Is(err, ErrState) {
			return "", nil, err
		}
		rec = r
	} else {
		o.moneyReconQueue(ctx, tenant, period, len(rec.History))
	}
	return "", []PeriodicFile{{ID: rec.FileID, Name: rec.FileName}}, &pendingErr{fmt.Sprintf("Prepared: %d movements, %d flagged; waiting for a checker to sign it off", len(rec.Moves), len(rec.Flags)), "Waiting for sign-off"}
}

// ---- self-inspection: client_money_recon ----

// siMoneyRecon checks that each business day's statement of the last two
// weeks before the date was signed off.
func siMoneyRecon(e *siEnv) (siOut, error) {
	since := siFirstTrade(e)
	if since == "" {
		return siSkip("Nothing to check: no trades in Ledgerline yet.")
	}
	var s siOut
	d := e.date
	for i := 0; i < 10; i++ {
		d = e.set.PrevBusinessDay(d)
		if d == "" || d < since {
			break
		}
		s.checked++
		s.input("Client money reconciliation", d)
		rec, _, err := docs.Get[MoneyRecon](e.ctx, e.o.Docs, e.tenant, cMoneyRecon, d)
		switch {
		case err != nil:
			s.bad(fmt.Sprintf("Statement of %s · not prepared", fmtDay(d)))
		case rec.Status == "Prepared":
			s.bad(fmt.Sprintf("Statement of %s · prepared by %s, not signed off", fmtDay(d), rec.Prepared.By))
		case rec.Status == "Sent back":
			s.bad(fmt.Sprintf("Statement of %s · sent back, not prepared again", fmtDay(d)))
		case rec.Status != "Signed off":
			s.bad(fmt.Sprintf("Statement of %s · %s", fmtDay(d), strings.ToLower(rec.Status)))
		}
	}
	if s.checked == 0 {
		return siSkip("Nothing to check yet: the first business day in Ledgerline is today.", s.inputs...)
	}
	s.summary = fmt.Sprintf("%d business days checked; %d statements not signed off.", s.checked, s.found)
	return s, nil
}
