package ops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- corporate bond deals through NSE CBRICS ----
//
// What the back office does with CBRICS (the protocol is in cbrics.go):
//
//   - Participants. Every deal names a CBRICS participant code for each
//     side. A client with no CBRICS account of its own is kept as an
//     unregistered participant: Ledgerline fills it in from the client's
//     record (name, PAN, contact, address, state code, date of birth or
//     incorporation, and the client's active registered bank and demat
//     accounts, the primary as default) and a checker sends it (POST
//     /rest/v1/unreg); NSE approves it. A counterparty is a registered
//     participant (found with participant/find) or an unregistered one the
//     firm keeps the same way. Contact changes, deletion and bank and demat
//     accounts (add, default, status) of an approved participant are
//     proposed by one person and sent by another.
//   - Deals. A maker enters the client's deal (ISIN, the client's side,
//     counterparty, quantity and face value, price and yield, accrued
//     interest, settlement T+0, T+1 or T+2, the deal's date and time); a
//     checker sends it: a client's sale as the seller's reporting
//     (standingInstFlag N), a client's purchase as the buyer's standing
//     instruction (Y), which CBRICS confirms against the seller's report.
//     Ledgerline's deal number goes in sellerRefNo or buyerRefNo. A change
//     before the buyer confirms goes by order/update (CBRICS cancels the old
//     order and gives a new id). A sale reported to a client who buys
//     without a standing instruction is accepted or rejected (order/status)
//     by maker and checker.
//   - Reading. Sell reportings (today's, and earlier days' of open deals),
//     buy instructions and orders for settlement are read every 10 minutes
//     from 08:00 to 20:00, at once after a notification from NSE, and on
//     demand; each is matched to Ledgerline's deal by CBRICS's transaction
//     number, then by Ledgerline's reference, then (a sale reported to the
//     client's standing instruction) by the deal's terms. A deal found at
//     CBRICS for a client and not in Ledgerline is added from it.
//   - Settlement. An order for settlement gives the settlement number and
//     date, the consideration, stamp duty and each side's obligation. The
//     seller's payout bank account (settle/order/updatebank) and the
//     buyer's demat account (settle/order/updatedp) are given from the
//     client's records, for the sides that are unregistered participants
//     the firm keeps: when CBRICS has another account than the primary (or
//     the one chosen), Ledgerline proposes the change and a checker sends
//     it. Each settlement raises a work item for the money (the buyer's
//     funds pay-in, or the seller's delivery and payout), closed when
//     CBRICS reports the pay-in complete.
//   - Books. Settlement is between each participant and NSE Clearing: the
//     buyer pays NSE Clearing from their own bank account (the protocol's
//     payment page or UPI, by the settlement's fund pay-in reference) and
//     NSE Clearing pays the seller's registered account; stamp duty is
//     collected in the settlement. No money passes through the broker, so
//     Ledgerline posts no ledger entries for a CBRICS deal: it keeps each
//     settled deal with its consideration and settlement date.
//   - Reporting time. The protocol sets no time limit; Ledgerline uses the
//     connection's limit (15 minutes by default, SEBI's rule for reporting
//     OTC corporate bond deals) from the deal's time and raises a work item
//     for a deal not reported by then.

const (
	cCBRICSDeal  = "cbricsdeal"  // key: deal ID (CBD-…)
	cCBRICSParty = "cbricsparty" // key: participant code
	cCBRICSState = "cbricsstate" // one document, "state"
)

// CBRICS deal statuses in Ledgerline.
const (
	CBDDraft      = "Draft"                 // entered, waiting for a checker
	CBDReturned   = "Returned"              // the checker sent it back
	CBDRefused    = "Refused"               // CBRICS refused it
	CBDReported   = "Reported"              // a sale reported, waiting for the buyer
	CBDInstructed = "Instruction pending"   // a purchase's standing instruction, not yet matched
	CBDToConfirm  = "Awaiting confirmation" // a sale reported to the client as buyer
	CBDConfirmed  = "Confirmed"
	CBDRejected   = "Rejected by buyer"
	CBDCancelled  = "Cancelled"
	CBDSettling   = "Settling"
	CBDSettled    = "Settled"
	CBDFailed     = "Settlement failed"
)

// CBRICSEvent is one line of a deal's or participant's history.
type CBRICSEvent struct {
	At   string `json:"at"`
	By   string `json:"by"`
	What string `json:"what"`
}

// CBRICSPartyBank is a participant's bank account.
type CBRICSPartyBank struct {
	Bank     string `json:"bank"`
	IFSC     string `json:"ifsc"`
	Account  string `json:"account,omitempty"` // none: an interbank account
	Default  bool   `json:"default,omitempty"`
	Status   string `json:"status,omitempty"`   // A active, S suspended, D deleted
	Workflow string `json:"workflow,omitempty"` // CBRICS's approval code
	Remarks  string `json:"remarks,omitempty"`
}

// CBRICSPartyDP is a participant's demat account.
type CBRICSPartyDP struct {
	Depository string `json:"depository"`      // NSDL or CDSL
	DPID       string `json:"dp_id,omitempty"` // NSDL
	BenID      string `json:"ben_id"`          // 8 digits NSDL, 16 CDSL
	Default    bool   `json:"default,omitempty"`
	Status     string `json:"status,omitempty"`
	Workflow   string `json:"workflow,omitempty"`
	Remarks    string `json:"remarks,omitempty"`
}

// CBRICSParty is a CBRICS participant the firm deals with: a registered
// participant found at CBRICS, or an unregistered one the firm keeps there
// (a client without its own CBRICS account, or a counterparty).
type CBRICSParty struct {
	Code      string            `json:"code"` // CBRICS login ID
	Kind      string            `json:"kind"` // Registered, Unregistered
	Name      string            `json:"name"`
	PAN       string            `json:"pan,omitempty"` // or PAN_EXEMPT
	UCC       string            `json:"ucc,omitempty"` // the firm's client
	CBRICSID  string            `json:"cbrics_id,omitempty"`
	Workflow  string            `json:"workflow,omitempty"`
	State     string            `json:"state"` // Draft, Sent, Approved, Returned, Rejected, Deleted; Active, Inactive, Suspended
	Remarks   string            `json:"remarks,omitempty"`
	PANCheck  string            `json:"pan_check,omitempty"`
	Custodian string            `json:"custodian,omitempty"`
	Contact   string            `json:"contact_person,omitempty"`
	Mobiles   []string          `json:"mobiles,omitempty"`
	Emails    []string          `json:"emails,omitempty"`
	Telephone string            `json:"telephone,omitempty"`
	Fax       string            `json:"fax,omitempty"`
	Address1  string            `json:"address1,omitempty"`
	Address2  string            `json:"address2,omitempty"`
	Address3  string            `json:"address3,omitempty"`
	StateCode string            `json:"state_code,omitempty"`
	RegAddr   string            `json:"reg_address,omitempty"`
	LEI       string            `json:"lei,omitempty"`
	LEIExpiry string            `json:"lei_expiry,omitempty"`
	DOB       string            `json:"dob,omitempty"` // birth or incorporation, YYYY-MM-DD
	Banks     []CBRICSPartyBank `json:"banks,omitempty"`
	DPs       []CBRICSPartyDP   `json:"dps,omitempty"`
	Pending   *CBRICSPartyReq   `json:"pending,omitempty"`
	History   []CBRICSEvent     `json:"history,omitempty"`
	UpdatedAt string            `json:"updated_at,omitempty"`
}

// CBRICSPartyReq is a change to a participant waiting for a checker.
type CBRICSPartyReq struct {
	// Action: add, update (a request returned), contact, delete, add_bank,
	// add_dp, default_bank, default_dp, bank_status, dp_status.
	Action string           `json:"action"`
	Party  *CBRICSParty     `json:"party,omitempty"` // the details proposed (add, update, contact)
	Bank   *CBRICSPartyBank `json:"bank,omitempty"`
	DP     *CBRICSPartyDP   `json:"dp,omitempty"`
	Status string           `json:"status,omitempty"` // A, S or D
	By     string           `json:"by"`
	At     string           `json:"at"`
}

// CBRICSAcctChange is a settlement's account change waiting for a checker:
// the seller's payout bank account, the buyer's demat account, or both.
type CBRICSAcctChange struct {
	Bank    string `json:"bank,omitempty"`
	IFSC    string `json:"ifsc,omitempty"`
	Account string `json:"account,omitempty"`
	BankKey string `json:"bank_key,omitempty"` // input: IFSC-last 4 digits of one of the side's accounts
	Demat   string `json:"demat,omitempty"`    // input and display: the demat account (16 characters)
	DPID    string `json:"dp_id,omitempty"`
	BenID   string `json:"ben_id,omitempty"`
	Why     string `json:"why,omitempty"`
	By      string `json:"by,omitempty"`
	At      string `json:"at,omitempty"`
}

// CBRICSDealSettle is a deal's order for settlement as CBRICS gives it.
type CBRICSDealSettle struct {
	ID            string    `json:"id"`
	SettlementNo  string    `json:"settlement_no"`
	Date          string    `json:"date"`
	Status        string    `json:"status"` // CBRICS's settleStatus
	StatusText    string    `json:"status_text"`
	Source        string    `json:"source,omitempty"`
	Quantity      int64     `json:"quantity"`
	AccrInt       money.Dec `json:"accr_int"`
	Consideration money.Dec `json:"consideration"`
	StampDuty     money.Dec `json:"stamp_duty"`
	StampBearer   string    `json:"stamp_bearer,omitempty"`
	BuyerPayin    money.Dec `json:"buyer_payin"`
	SellerPayout  money.Dec `json:"seller_payout"`
	PayinRef      string    `json:"payin_ref,omitempty"`
	IFSC          string    `json:"ifsc,omitempty"`
	Account       string    `json:"account,omitempty"`
	DPID          string    `json:"dp_id,omitempty"`
	BenID         string    `json:"ben_id,omitempty"`
	SecPayinQty   int64     `json:"sec_payin_qty,omitempty"`
	SecPayinTime  string    `json:"sec_payin_time,omitempty"`
	FundsPayin    money.Dec `json:"funds_payin"`
	FundsTime     string    `json:"funds_payin_time,omitempty"`
	PayoutTime    string    `json:"payout_time,omitempty"`
	UTR           string    `json:"utr,omitempty"`
	Remarks       string    `json:"remarks,omitempty"`
	SettledAt     string    `json:"settled_at,omitempty"`
}

// CBRICSDeal is a client's corporate bond deal reported through CBRICS.
type CBRICSDeal struct {
	ID               string            `json:"id"`
	Source           string            `json:"source"` // Ledgerline (entered here) or CBRICS (found there)
	UCC              string            `json:"ucc"`
	Name             string            `json:"name"`
	Side             string            `json:"side"` // the client's: Buy or Sell
	ClientCode       string            `json:"client_code"`
	Counterparty     string            `json:"counterparty"`
	CounterpartyName string            `json:"counterparty_name,omitempty"`
	ISIN             string            `json:"isin"`
	Description      string            `json:"description,omitempty"`
	DealType         string            `json:"deal_type"` // B brokered, D direct, I inter-scheme
	SendFor          string            `json:"send_for"`  // M mandatory, C optional buyer confirmation
	Settle           int               `json:"settle"`    // 0 same day, 1 T+1, 2 T+2
	Quantity         int64             `json:"quantity"`
	FaceValue        money.Dec         `json:"face_value"` // a bond's
	Value            money.Dec         `json:"value"`      // face value of the deal
	Price            money.Dec         `json:"price"`      // per 100 of face value
	AccrInt          money.Dec         `json:"accr_int"`
	YieldType        string            `json:"yield_type"` // M YTM, C YTC, P YTP
	Yield            money.Dec         `json:"yield"`
	PutCallDate      string            `json:"put_call_date,omitempty"`
	YieldReason      string            `json:"yield_reason,omitempty"` // 1 credit, 2 liquidity: outside CBRICS's yield band
	TradeDate        string            `json:"trade_date"`
	TradeTime        string            `json:"trade_time"` // HH:MM
	Due              string            `json:"due,omitempty"`
	Estimate         money.Dec         `json:"estimate"` // value × price / 100 + accrued interest
	Status           string            `json:"status"`
	Reason           string            `json:"reason,omitempty"`  // a refusal or a return, in words
	Message          string            `json:"message,omitempty"` // CBRICS's last words
	Pending          string            `json:"pending,omitempty"` // Report, Change, Accept, Reject, Account
	PendingBy        string            `json:"pending_by,omitempty"`
	PendingAt        string            `json:"pending_at,omitempty"`
	Change           *CBRICSDeal       `json:"change,omitempty"`
	Account          *CBRICSAcctChange `json:"account,omitempty"`
	MadeBy           string            `json:"made_by,omitempty"`
	ReportedBy       string            `json:"reported_by,omitempty"`
	ReportedAt       string            `json:"reported_at,omitempty"`
	Late             bool              `json:"late,omitempty"`
	LateMinutes      int               `json:"late_minutes,omitempty"`
	OrderID          string            `json:"order_id,omitempty"`
	OrderNo          string            `json:"order_no,omitempty"`
	OldOrders        []string          `json:"old_orders,omitempty"`
	OrderStatus      string            `json:"order_status,omitempty"`
	CounterOrderID   string            `json:"counter_order_id,omitempty"` // the seller's report to a client buying
	CounterOrderNo   string            `json:"counter_order_no,omitempty"`
	CounterStatus    string            `json:"counter_status,omitempty"`
	CBRICSQty        int64             `json:"cbrics_qty,omitempty"`
	Consideration    money.Dec         `json:"consideration"`
	Settlement       *CBRICSDealSettle `json:"settlement,omitempty"`
	Warnings         []string          `json:"warnings,omitempty"`
	History          []CBRICSEvent     `json:"history,omitempty"`
}

func (d *CBRICSDeal) note(at time.Time, by, what string) {
	d.History = append(d.History, CBRICSEvent{At: at.Format(time.RFC3339), By: by, What: what})
}

func (p *CBRICSParty) note(at time.Time, by, what string) {
	p.History = append(p.History, CBRICSEvent{At: at.Format(time.RFC3339), By: by, What: what})
}

// open: CBRICS may still change the deal before settlement.
func (d CBRICSDeal) open() bool {
	switch d.Status {
	case CBDReported, CBDInstructed, CBDToConfirm, CBDConfirmed, CBDSettling:
		return true
	}
	return false
}

// unreported: not yet at CBRICS.
func (d CBRICSDeal) unreported() bool {
	return d.Source != "CBRICS" && d.ReportedAt == "" && (d.Status == CBDDraft || d.Status == CBDReturned || d.Status == CBDRefused)
}

// CBRICSState is the last reading of CBRICS and its notifications.
type CBRICSState struct {
	LastRun    string   `json:"last_run,omitempty"`
	LastOK     string   `json:"last_ok,omitempty"`
	LastError  string   `json:"last_error,omitempty"`
	Refresh    bool     `json:"refresh,omitempty"` // a notification asks for a read
	Notices    int      `json:"notices"`
	LastNotice string   `json:"last_notice,omitempty"`
	Kinds      []string `json:"kinds,omitempty"`     // what the last notification named
	Unmatched  []string `json:"unmatched,omitempty"` // CBRICS transactions of no client of the firm
}

var cbricsWorkflow = map[string]string{"100": "Pending with the firm's CBRICS checker", "16": "Returned by the firm's CBRICS checker", "15": "Rejected by the firm's CBRICS checker",
	"0": "Pending with NSE", "10": "Pending with NSE", "1": "Approved", "5": "Rejected by NSE", "6": "Returned by NSE"}

func cbricsWorkflowState(w string) string {
	switch w {
	case "1":
		return "Approved"
	case "6", "16":
		return "Returned"
	case "5", "15":
		return "Rejected"
	case "":
		return ""
	}
	return "Sent"
}

var cbricsSettleWords = map[string]string{"0": "Settlement pending", "1": "Securities pay-in done", "2": "Funds pay-in done", "3": "Pay-in completed",
	"4": "Pay-out done", "5": "Pay-in reversed", "6": "Settle order expired", "7": "Order not settleable", "8": "Settlement cancelled",
	"9": "Documents not received for the unregistered participant"}

func cbricsOrderWords(flag, st string) string {
	if flag == "Y" {
		return map[string]string{"1": CBDInstructed, "2": CBDConfirmed, "3": CBDCancelled}[st]
	}
	return map[string]string{"1": CBDReported, "2": CBDRejected, "7": CBDConfirmed, "8": CBDCancelled}[st]
}

// stamp makes a work item key unique to one request (its time).
func stamp(at string) string {
	return strings.NewReplacer("-", "", ":", "", "T", "", "+", "", "Z", "").Replace(at)
}

func numStr(n json.Number) string { return strings.TrimSpace(n.String()) }

func numInt(n json.Number) int64 {
	v, _ := strconv.ParseInt(strings.Split(numStr(n), ".")[0], 10, 64)
	return v
}

func (o *Ops) cbricsStateDo(ctx context.Context, tenant string, fn func(*CBRICSState)) CBRICSState {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCBRICSState, "state", CBRICSState{})
	fn(&st)
	_ = docs.Upsert(ctx, o.Docs, tenant, cCBRICSState, "state", st, SystemActor)
	return st
}

// ---- participants ----

func normDigits(s string) string {
	return strings.NewReplacer(" ", "", "-", "").Replace(strings.ToUpper(strings.TrimSpace(s)))
}

// cbricsDPOf is a demat account as CBRICS takes it: NSDL with its DP ID and
// 8-digit client ID, CDSL with the 16-digit BO ID as the beneficiary ID.
func cbricsDPOf(id string) (CBRICSPartyDP, bool) {
	id = normDigits(id)
	switch {
	case len(id) == 16 && strings.HasPrefix(id, "IN") && strings.Trim(id[8:], "0123456789") == "":
		return CBRICSPartyDP{Depository: "NSDL", DPID: id[:8], BenID: id[8:]}, true
	case len(id) == 16 && strings.Trim(id, "0123456789") == "":
		return CBRICSPartyDP{Depository: "CDSL", BenID: id}, true
	}
	return CBRICSPartyDP{}, false
}

func (p CBRICSPartyDP) id() string {
	if p.Depository == "NSDL" {
		return p.DPID + p.BenID
	}
	return p.BenID
}

// cbricsClientAccounts are the client's active registered bank accounts
// and demat accounts as CBRICS takes them, the primary (and the pay-out
// demat account) as default.
func cbricsClientAccounts(c Client) ([]CBRICSPartyBank, []CBRICSPartyDP) {
	var bs []CBRICSPartyBank
	pb := primaryBank(c)
	for _, b := range activeBanks(c) {
		bs = append(bs, CBRICSPartyBank{Bank: b.Bank, IFSC: strings.ToUpper(b.IFSC), Account: b.Account, Default: b.Account == pb.Account && strings.EqualFold(b.IFSC, pb.IFSC)})
	}
	var ds []CBRICSPartyDP
	pd := normDigits(payoutDemat(c).ID)
	for _, d := range activeDemats(c) {
		if x, ok := cbricsDPOf(d.ID); ok {
			x.Default = normDigits(d.ID) == pd
			ds = append(ds, x)
		}
	}
	return bs, ds
}

// CBRICSPartyFromClient is an unregistered participant filled in from the
// client's record, for the maker to check and send for approval.
func (o *Ops) CBRICSPartyFromClient(ctx context.Context, tenant, actor, ucc string) (CBRICSParty, error) {
	if err := o.require(ctx, tenant, actor, "Keeping CBRICS participants", RoleMaker, RoleChecker); err != nil {
		return CBRICSParty{}, err
	}
	return o.cbricsPartyFromClient(ctx, tenant, ucc)
}

func (o *Ops) cbricsPartyFromClient(ctx context.Context, tenant, ucc string) (CBRICSParty, error) {
	c, err := o.Client(ctx, tenant, strings.ToUpper(strings.TrimSpace(ucc)))
	if err != nil {
		return CBRICSParty{}, invalid("no client %s", ucc)
	}
	d := o.UCCDetailsFor(ctx, tenant, c.UCC)
	p := CBRICSParty{Code: c.UCC, Kind: "Unregistered", Name: c.Name, PAN: c.PAN, UCC: c.UCC, Contact: c.Name, Telephone: c.Mobile, DOB: c.DOB,
		Address1: d.Address1, Address2: d.Address2, Address3: d.Address3, StateCode: firstOf(cbricsStateCode(d.State), cbricsStateCode(c.State)), State: "Draft"}
	if len(d.Contacts) > 0 && c.Type != "Individual" {
		p.Contact = d.Contacts[0].Name
	}
	if c.Mobile != "" {
		p.Mobiles = []string{c.Mobile}
	}
	if c.Email != "" {
		p.Emails = []string{c.Email}
	}
	reg := []string{firstOf(d.PermAddress1, d.Address1), firstOf(d.PermAddress2, d.Address2), firstOf(d.PermAddress3, d.Address3), firstOf(d.PermCity, firstOf(d.City, c.City)), firstOf(d.PermState, firstOf(d.State, c.State)), firstOf(d.PermPIN, d.PIN)}
	var parts []string
	for _, s := range reg {
		if s = strings.TrimSpace(s); s != "" && !contains(parts, s) {
			parts = append(parts, s)
		}
	}
	p.RegAddr = strings.Join(parts, ", ")
	p.Banks, p.DPs = cbricsClientAccounts(c)
	return p, nil
}

func (p *CBRICSParty) normalise() {
	p.Code = strings.ToUpper(strings.TrimSpace(p.Code))
	p.PAN = strings.ToUpper(strings.TrimSpace(p.PAN))
	p.UCC = strings.ToUpper(strings.TrimSpace(p.UCC))
	p.StateCode = firstOf(cbricsStateCode(p.StateCode), strings.TrimSpace(p.StateCode))
	for _, s := range []*string{&p.Name, &p.Contact, &p.Telephone, &p.Fax, &p.Address1, &p.Address2, &p.Address3, &p.RegAddr, &p.LEI, &p.Custodian} {
		*s = strings.TrimSpace(*s)
	}
	clean := func(xs []string) []string {
		var out []string
		for _, x := range xs {
			if x = strings.TrimSpace(x); x != "" && !contains(out, x) {
				out = append(out, x)
			}
		}
		return out
	}
	p.Mobiles, p.Emails = clean(p.Mobiles), clean(p.Emails)
	for i := range p.Banks {
		p.Banks[i].IFSC = strings.ToUpper(strings.TrimSpace(p.Banks[i].IFSC))
		p.Banks[i].Account = strings.TrimSpace(p.Banks[i].Account)
	}
	for i := range p.DPs {
		x := &p.DPs[i]
		x.Depository, x.DPID, x.BenID = strings.ToUpper(strings.TrimSpace(x.Depository)), normDigits(x.DPID), normDigits(x.BenID)
		if x.Depository == "" {
			if y, ok := cbricsDPOf(x.DPID + x.BenID); ok {
				*x = CBRICSPartyDP{Depository: y.Depository, DPID: y.DPID, BenID: y.BenID, Default: x.Default, Status: x.Status, Workflow: x.Workflow}
			}
		}
	}
}

func cbricsBankProblem(b CBRICSPartyBank) string {
	switch {
	case b.Bank == "" || len(b.Bank) > 100:
		return "each bank account needs the bank's name (up to 100 characters)"
	case !reIFSC.MatchString(b.IFSC):
		return "the IFSC " + b.IFSC + " is not an 11-character IFSC"
	case len(b.Account) > 30:
		return "a bank account number is up to 30 characters"
	}
	return ""
}

func cbricsDPProblem(d CBRICSPartyDP) string {
	switch d.Depository {
	case "NSDL":
		if len(d.DPID) != 8 || !strings.HasPrefix(d.DPID, "IN") || len(d.BenID) != 8 || strings.Trim(d.BenID, "0123456789") != "" {
			return "an NSDL account needs the DP ID (IN and 6 characters) and an 8-digit client ID"
		}
	case "CDSL":
		if len(d.BenID) != 16 || strings.Trim(d.BenID, "0123456789") != "" {
			return "a CDSL account needs the 16-digit BO ID"
		}
	default:
		return "a demat account is NSDL or CDSL"
	}
	return ""
}

// problem checks an unregistered participant against POST /rest/v1/unreg.
func (p CBRICSParty) problem() string {
	switch {
	case p.Code == "" || len(p.Code) > 30:
		return "the participant code is up to 30 characters"
	case p.Name == "" || len(p.Name) > 250:
		return "give the participant's name (up to 250 characters)"
	case p.PAN != "PAN_EXEMPT" && !rePAN.MatchString(p.PAN):
		return "give a valid PAN, or PAN_EXEMPT"
	case p.Contact == "":
		return "give the contact person"
	case len(p.Mobiles) < 1 || len(p.Mobiles) > 3:
		return "give one to three mobile numbers"
	case len(p.Emails) < 1 || len(p.Emails) > 3:
		return "give one to three email addresses"
	case p.Telephone == "":
		return "give a telephone number"
	case cbricsStates[p.StateCode] == "":
		return "choose the registered office or domicile state (Annexure 2 code)"
	case p.RegAddr == "" || len(p.RegAddr) > 500:
		return "give the registered office or domicile address (up to 500 characters)"
	case p.LEI != "" && p.LEIExpiry == "":
		return "give the LEI's expiry date"
	case p.DOB == "":
		return "give the date of birth or incorporation"
	case len(p.Banks) == 0:
		return "give at least one bank account"
	case len(p.DPs) == 0:
		return "give at least one demat account"
	}
	for _, m := range p.Mobiles {
		if len(m) > 15 {
			return "a mobile number is up to 15 characters"
		}
	}
	for _, e := range p.Emails {
		if len(e) > 50 || !reEmail.MatchString(e) {
			return "the email address " + e + " is not valid (up to 50 characters)"
		}
	}
	for _, s := range []string{p.Address1, p.Address2, p.Address3} {
		if len(s) > 100 {
			return "an address line is up to 100 characters"
		}
	}
	if _, err := time.Parse("2006-01-02", p.DOB); err != nil {
		return "the date of birth or incorporation is a date"
	}
	nb, nd := 0, 0
	for _, b := range p.Banks {
		if m := cbricsBankProblem(b); m != "" {
			return m
		}
		if b.Default {
			nb++
		}
	}
	for _, d := range p.DPs {
		if m := cbricsDPProblem(d); m != "" {
			return m
		}
		if d.Default {
			nd++
		}
	}
	if nb != 1 || nd != 1 {
		return "mark one bank account and one demat account as the default for payouts"
	}
	return ""
}

func (b CBRICSPartyBank) acc(code string) CBRICSBankAcc {
	return CBRICSBankAcc{BankName: b.Bank, BankIFSC: b.IFSC, BankAccountNo: b.Account, IsDefault: map[bool]string{true: "Y", false: "N"}[b.Default], ParticipantCode: code, Status: b.Status}
}

func (d CBRICSPartyDP) acc(code string) CBRICSDPAcc {
	return CBRICSDPAcc{DPType: d.Depository, DPID: d.DPID, BenID: flexStr(d.BenID), IsDefault: map[bool]string{true: "Y", false: "N"}[d.Default], ParticipantCode: code, Status: d.Status}
}

func bankOfAcc(a CBRICSBankAcc) CBRICSPartyBank {
	return CBRICSPartyBank{Bank: a.BankName, IFSC: strings.ToUpper(a.BankIFSC), Account: a.BankAccountNo, Default: a.IsDefault == "Y", Status: a.Status, Workflow: numStr(a.WorkflowStatus), Remarks: a.Remarks}
}

func dpOfAcc(a CBRICSDPAcc) CBRICSPartyDP {
	return CBRICSPartyDP{Depository: strings.ToUpper(a.DPType), DPID: strings.ToUpper(a.DPID), BenID: string(a.BenID), Default: a.IsDefault == "Y", Status: a.Status, Workflow: numStr(a.WorkflowStatus), Remarks: a.Remarks}
}

// unreg is the participant as POST /rest/v1/unreg (or, with its id,
// /unreg/update, which also carries each account's status) takes it.
func (p CBRICSParty) unreg(update bool) CBRICSUnreg {
	u := CBRICSUnreg{FirstName: p.Name, PAN: p.PAN, Custodian: p.Custodian, ContactPerson: p.Contact, Mobiles: p.Mobiles, Emails: p.Emails, Telephone: flexStr(p.Telephone),
		Fax: p.Fax, Address: p.Address1, Address2: p.Address2, Address3: p.Address3, StateCode: flexStr(p.StateCode), RegAddress: p.RegAddr, LEI: p.LEI,
		LEIExpiry: cbricsDayOut(p.LEIExpiry), DOBDOI: cbricsDayOut(p.DOB)}
	if update {
		u.ID, u.ActualStatus = json.Number(p.CBRICSID), "4"
	} else {
		u.LoginID = p.Code
	}
	for _, b := range p.Banks {
		a := b.acc("")
		if update {
			a.Status = firstOf(b.Status, "A")
		} else {
			a.Status = ""
		}
		u.Banks = append(u.Banks, a)
	}
	for _, d := range p.DPs {
		a := d.acc("")
		if update {
			a.Status = firstOf(d.Status, "A")
		} else {
			a.Status = ""
		}
		u.DPs = append(u.DPs, a)
	}
	return u
}

// absorb takes CBRICS's reply about the participant.
func (p *CBRICSParty) absorb(u CBRICSUnreg) {
	if id := numStr(u.ID); id != "" {
		p.CBRICSID = id
	}
	if w := numStr(u.WorkflowStatus); w != "" {
		p.Workflow, p.State = w, cbricsWorkflowState(w)
	}
	p.Remarks = firstOf(u.Remarks, p.Remarks)
	if v := numStr(u.PANVerStatus); v != "" {
		p.PANCheck = strings.TrimSpace(firstOf(cbricsWorkflow[v], v) + " " + u.PANVerRemarks)
	}
	if len(u.Banks) > 0 {
		p.Banks = nil
		for _, a := range u.Banks {
			p.Banks = append(p.Banks, bankOfAcc(a))
		}
	}
	if len(u.DPs) > 0 {
		p.DPs = nil
		for _, a := range u.DPs {
			p.DPs = append(p.DPs, dpOfAcc(a))
		}
	}
}

// cbricsClientAccountsOnly keeps a client's participant to the client's own
// registered accounts (money and bonds go only to the client's accounts).
func (o *Ops) cbricsClientAccountsOnly(ctx context.Context, tenant string, p CBRICSParty) error {
	if p.UCC == "" {
		return nil
	}
	c, err := o.Client(ctx, tenant, p.UCC)
	if err != nil {
		return invalid("no client %s", p.UCC)
	}
	for _, b := range p.Banks {
		if _, ok := registeredBank(c, b.Account, b.IFSC); !ok && b.Status != "D" {
			return invalid("%s %s is not an active registered bank account of %s", b.IFSC, MaskAccount(b.Account), p.UCC)
		}
	}
	for _, d := range p.DPs {
		if x, ok := dematOf(c, d.id()); (!ok || !x.active()) && d.Status != "D" {
			return invalid("%s is not an active demat account of %s", d.id(), p.UCC)
		}
	}
	return nil
}

// SaveCBRICSParty keeps a participant. A registered one (found at CBRICS)
// is kept with the client it is, if any. An unregistered one not yet
// approved is saved for a checker to send; a client's takes the client's
// active accounts from the client's record each time. For an approved one
// only the contact details change this way (CBRICS's updatecontact).
func (o *Ops) SaveCBRICSParty(ctx context.Context, tenant, actor string, p CBRICSParty) (CBRICSParty, error) {
	if err := o.require(ctx, tenant, actor, "Keeping CBRICS participants", RoleMaker, RoleChecker); err != nil {
		return p, err
	}
	p.normalise()
	now := o.now()
	cur, _, err := docs.Get[CBRICSParty](ctx, o.Docs, tenant, cCBRICSParty, p.Code)
	exists := err == nil
	if p.UCC != "" {
		c, err := o.Client(ctx, tenant, p.UCC)
		if err != nil {
			return p, invalid("no client %s", p.UCC)
		}
		if p.Kind != "Registered" {
			p.Banks, p.DPs = cbricsClientAccounts(c)
		}
		if other := o.cbricsPartyOf(ctx, tenant, p.UCC); other.Code != "" && other.Code != p.Code && other.State != "Deleted" {
			return p, invalid("%s is already participant %s at CBRICS", p.UCC, other.Code)
		}
	}
	if p.Kind == "Registered" {
		if p.Code == "" {
			return p, invalid("give the registered participant's code")
		}
		if exists && cur.Kind != "Registered" {
			return p, invalid("%s is an unregistered participant the firm keeps", p.Code)
		}
		if exists {
			cur.UCC, cur.Name = p.UCC, firstOf(p.Name, cur.Name)
			p = cur
		}
		p.Kind, p.State, p.UpdatedAt = "Registered", firstOf(p.State, "Active"), now.Format(time.RFC3339)
		p.note(now, actor, "kept as a registered participant"+map[bool]string{true: " of client " + p.UCC, false: ""}[p.UCC != ""])
		if err := docs.Upsert(ctx, o.Docs, tenant, cCBRICSParty, p.Code, p, actor); err != nil {
			return p, err
		}
		o.log(ctx, tenant, actor, "cbrics.party.kept", p.Code, map[string]any{"ucc": p.UCC})
		return p, nil
	}
	p.Kind = "Unregistered"
	if m := p.problem(); m != "" {
		return p, invalid("%s", m)
	}
	if err := o.cbricsClientAccountsOnly(ctx, tenant, p); err != nil {
		return p, err
	}
	action := "add"
	switch {
	case !exists || cur.State == "Draft" || cur.State == "Rejected" && cur.CBRICSID == "":
	case cur.Kind == "Registered":
		return p, invalid("%s is a registered participant", p.Code)
	case cur.State == "Returned":
		action = "update"
	case cur.State == "Approved":
		action = "contact"
	default:
		return p, fmt.Errorf("%w: participant %s is %s at CBRICS: wait for NSE", ErrState, p.Code, firstOf(cbricsWorkflow[cur.Workflow], cur.State))
	}
	if exists && cur.Pending != nil {
		return p, fmt.Errorf("%w: participant %s already has a change waiting for a checker", ErrState, p.Code)
	}
	rec := p
	if exists && action != "add" {
		rec = cur
		prop := p
		prop.Pending, prop.History = nil, nil
		prop.CBRICSID, prop.Workflow, prop.State = cur.CBRICSID, cur.Workflow, cur.State
		if action == "contact" {
			prop.Banks, prop.DPs = cur.Banks, cur.DPs // accounts change by their own requests
		}
		rec.Pending = &CBRICSPartyReq{Action: action, Party: &prop, By: actor, At: now.Format(time.RFC3339)}
	} else {
		rec.State, rec.History = "Draft", cur.History
		rec.Pending = &CBRICSPartyReq{Action: "add", By: actor, At: now.Format(time.RFC3339)}
	}
	rec.UpdatedAt = now.Format(time.RFC3339)
	rec.note(now, actor, map[string]string{"add": "entered for CBRICS", "update": "corrected after CBRICS returned it", "contact": "contact details changed"}[action]+": waiting for a checker")
	if err := docs.Upsert(ctx, o.Docs, tenant, cCBRICSParty, rec.Code, rec, actor); err != nil {
		return rec, err
	}
	o.raiseWork(ctx, tenant, "cbrics", "party:"+rec.Code+":"+stamp(rec.Pending.At), "Send CBRICS participant "+rec.Code, fmt.Sprintf("%s (%s) %s: check it and send it to CBRICS.", rec.Code, rec.Name, map[string]string{"add": "is a new unregistered participant", "update": "was corrected after CBRICS returned it", "contact": "has new contact details"}[action]), rec.UCC, RoleChecker, "cbrics", "")
	o.log(ctx, tenant, actor, "cbrics.party.saved", rec.Code, map[string]any{"action": action, "ucc": rec.UCC})
	return rec, nil
}

// CBRICSPartyRequest proposes a change to an approved unregistered
// participant: delete it, add a bank or demat account (a client's from the
// client's record, by bank key or demat ID), make one the default, or
// change an account's status (A active, S suspended, D deleted).
func (o *Ops) CBRICSPartyRequest(ctx context.Context, tenant, actor, code string, r CBRICSPartyReq, bankKey, demat string) (CBRICSParty, error) {
	if err := o.require(ctx, tenant, actor, "Keeping CBRICS participants", RoleMaker, RoleChecker); err != nil {
		return CBRICSParty{}, err
	}
	p, _, err := docs.Get[CBRICSParty](ctx, o.Docs, tenant, cCBRICSParty, strings.ToUpper(code))
	if err != nil || p.Kind != "Unregistered" {
		return p, invalid("no unregistered participant %s", code)
	}
	if p.State != "Approved" {
		return p, fmt.Errorf("%w: %s is not approved at CBRICS yet", ErrState, p.Code)
	}
	if p.Pending != nil {
		return p, fmt.Errorf("%w: participant %s already has a change waiting for a checker", ErrState, p.Code)
	}
	var c Client
	if p.UCC != "" {
		if c, err = o.Client(ctx, tenant, p.UCC); err != nil {
			return p, invalid("no client %s", p.UCC)
		}
	}
	req := CBRICSPartyReq{Action: r.Action, Status: strings.ToUpper(r.Status), By: actor, At: o.now().Format(time.RFC3339)}
	findBank := func() (*CBRICSPartyBank, error) {
		if r.Bank == nil {
			return nil, invalid("choose the bank account")
		}
		// The screen shows a client's account masked (XXXX1234): its last 4
		// digits and the IFSC find it.
		given := strings.TrimLeft(strings.TrimSpace(r.Bank.Account), "X")
		masked := given != strings.TrimSpace(r.Bank.Account)
		for _, b := range p.Banks {
			if strings.EqualFold(b.IFSC, r.Bank.IFSC) && (b.Account == r.Bank.Account || masked && given != "" && strings.HasSuffix(b.Account, given)) {
				return &b, nil
			}
		}
		return nil, invalid("%s has no such bank account at CBRICS", p.Code)
	}
	findDP := func() (*CBRICSPartyDP, error) {
		if r.DP == nil {
			return nil, invalid("choose the demat account")
		}
		want := normDigits(r.DP.DPID + r.DP.BenID)
		for _, d := range p.DPs {
			if d.id() == want {
				return &d, nil
			}
		}
		return nil, invalid("%s has no such demat account at CBRICS", p.Code)
	}
	switch r.Action {
	case "delete":
	case "add_bank":
		var b CBRICSPartyBank
		if p.UCC != "" {
			cb, err := clientBankByKey(c, bankKey)
			if err != nil {
				return p, err
			}
			b = CBRICSPartyBank{Bank: cb.Bank, IFSC: strings.ToUpper(cb.IFSC), Account: cb.Account}
		} else if r.Bank != nil {
			b = *r.Bank
			b.IFSC, b.Account = strings.ToUpper(strings.TrimSpace(b.IFSC)), strings.TrimSpace(b.Account)
		}
		if m := cbricsBankProblem(b); m != "" {
			return p, invalid("%s", m)
		}
		b.Default = r.Bank != nil && r.Bank.Default
		req.Bank = &b
	case "add_dp":
		var d CBRICSPartyDP
		if p.UCC != "" {
			x, ok := dematOf(c, demat)
			if !ok || !x.active() {
				return p, invalid("%s is not an active demat account of %s", demat, p.UCC)
			}
			d, _ = cbricsDPOf(x.ID)
		} else if x, ok := cbricsDPOf(demat); ok {
			d = x
		} else if r.DP != nil {
			d = *r.DP
			d.Depository, d.DPID, d.BenID = strings.ToUpper(d.Depository), normDigits(d.DPID), normDigits(d.BenID)
		}
		if m := cbricsDPProblem(d); m != "" {
			return p, invalid("%s", m)
		}
		d.Default = r.DP != nil && r.DP.Default
		req.DP = &d
	case "default_bank", "bank_status":
		if req.Bank, err = findBank(); err != nil {
			return p, err
		}
	case "default_dp", "dp_status":
		if req.DP, err = findDP(); err != nil {
			return p, err
		}
	default:
		return p, invalid("no such participant change: %s", r.Action)
	}
	if (r.Action == "bank_status" || r.Action == "dp_status") && req.Status != "A" && req.Status != "S" && req.Status != "D" {
		return p, invalid("an account's status is A (active), S (suspended) or D (deleted)")
	}
	p.Pending = &req
	p.note(o.now(), actor, cbricsReqWords(req)+": waiting for a checker")
	if err := docs.Upsert(ctx, o.Docs, tenant, cCBRICSParty, p.Code, p, actor); err != nil {
		return p, err
	}
	o.raiseWork(ctx, tenant, "cbrics", "party:"+p.Code+":"+stamp(req.At), "Send CBRICS participant "+p.Code, fmt.Sprintf("%s (%s): %s. Check it and send it to CBRICS.", p.Code, p.Name, cbricsReqWords(req)), p.UCC, RoleChecker, "cbrics", "")
	o.log(ctx, tenant, actor, "cbrics.party.request", p.Code, map[string]any{"action": r.Action})
	return p, nil
}

func cbricsReqWords(r CBRICSPartyReq) string {
	switch r.Action {
	case "delete":
		return "delete the participant"
	case "add_bank":
		return "add bank account " + r.Bank.IFSC + " " + MaskAccount(r.Bank.Account)
	case "add_dp":
		return "add demat account " + r.DP.id()
	case "default_bank":
		return "make " + r.Bank.IFSC + " " + MaskAccount(r.Bank.Account) + " the default bank account"
	case "default_dp":
		return "make " + r.DP.id() + " the default demat account"
	case "bank_status":
		return "set bank account " + r.Bank.IFSC + " " + MaskAccount(r.Bank.Account) + " to " + r.Status
	case "dp_status":
		return "set demat account " + r.DP.id() + " to " + r.Status
	case "contact":
		return "change the contact details"
	case "update":
		return "send the corrected details"
	}
	return "add the participant"
}

// clientBankByKey finds one of the client's active registered accounts by
// IFSC and the account's last 4 digits ("IFSC-1234"); empty is the primary.
func clientBankByKey(c Client, key string) (BankAccount, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	if key == "" {
		b := primaryBank(c)
		if b.Account == "" {
			return b, invalid("%s has no primary bank account", c.UCC)
		}
		return b, nil
	}
	ifsc, last, _ := strings.Cut(key, "-")
	var hit []BankAccount
	for _, b := range activeBanks(c) {
		if strings.EqualFold(b.IFSC, ifsc) && strings.HasSuffix(b.Account, last) && last != "" {
			hit = append(hit, b)
		}
	}
	if len(hit) != 1 {
		return BankAccount{}, invalid("%s is not one active registered bank account of %s", key, c.UCC)
	}
	return hit[0], nil
}

// DecideCBRICSParty: a checker other than the maker sends the participant's
// waiting change to CBRICS, or returns it.
func (o *Ops) DecideCBRICSParty(ctx context.Context, tenant, actor, code string, approve bool, reason string) (CBRICSParty, error) {
	if err := o.require(ctx, tenant, actor, "Sending CBRICS participants", RoleChecker); err != nil {
		return CBRICSParty{}, err
	}
	o.cbricsMu.Lock()
	defer o.cbricsMu.Unlock()
	p, _, err := docs.Get[CBRICSParty](ctx, o.Docs, tenant, cCBRICSParty, strings.ToUpper(code))
	if err != nil || p.Pending == nil {
		return p, fmt.Errorf("%w: nothing of participant %s waits for a checker", ErrState, code)
	}
	req := *p.Pending
	if actor == req.By {
		return p, fmt.Errorf("%w: a different person sends the participant to CBRICS (maker and checker)", ErrForbidden)
	}
	now := o.now()
	if !approve {
		p.Pending = nil
		if req.Action == "add" {
			p.State = "Draft"
		}
		p.note(now, actor, "returned to the maker: "+firstOf(reason, "no reason given"))
		_ = docs.Upsert(ctx, o.Docs, tenant, cCBRICSParty, p.Code, p, actor)
		o.closeWork(ctx, tenant, actor, "cbrics", "party:"+p.Code+":"+stamp(req.At), "returned to the maker")
		return p, nil
	}
	c, err := o.cbricsCall(ctx, tenant)
	if err != nil {
		return p, err
	}
	var u CBRICSUnreg
	switch req.Action {
	case "add":
		if m := p.problem(); m != "" {
			return p, invalid("%s", m)
		}
		u, err = c.unreg("", p.unreg(false))
	case "update":
		q := *req.Party
		u, err = c.unreg("/update", q.unreg(true))
		if err == nil {
			hist := p.History
			p, p.History = q, hist
		}
	case "contact":
		q := *req.Party
		body := map[string]any{"id": json.Number(p.CBRICSID), "contactPerson": q.Contact, "mobileList": q.Mobiles, "emailList": q.Emails, "telephone": q.Telephone}
		for k, v := range map[string]string{"fax": q.Fax, "address": q.Address1, "address2": q.Address2, "address3": q.Address3} {
			if v != "" {
				body[k] = v
			}
		}
		u, err = c.unreg("/final/updatecontact", body)
		if err == nil {
			p.Contact, p.Mobiles, p.Emails, p.Telephone, p.Fax, p.Address1, p.Address2, p.Address3 = q.Contact, q.Mobiles, q.Emails, q.Telephone, q.Fax, q.Address1, q.Address2, q.Address3
		}
	case "delete":
		var msg string
		if msg, err = c.unregDelete(p.CBRICSID); err == nil {
			p.State, p.Remarks = "Deleted", msg
		}
	case "add_bank":
		var a CBRICSBankAcc
		if a, err = c.bankAcc("", req.Bank.acc(p.Code)); err == nil {
			p.Banks = append(p.Banks, bankOfAcc(a))
		}
	case "add_dp":
		var a CBRICSDPAcc
		if a, err = c.dpAcc("", req.DP.acc(p.Code)); err == nil {
			p.DPs = append(p.DPs, dpOfAcc(a))
		}
	case "default_bank", "bank_status":
		body := map[string]any{"participantCode": p.Code, "bankIFSC": req.Bank.IFSC}
		if req.Bank.Account != "" {
			body["bankAccountNo"] = req.Bank.Account
		}
		path := "/final/markdefault"
		if req.Action == "bank_status" {
			body["status"], path = req.Status, "/final/updatestatus"
		}
		var a CBRICSBankAcc
		if a, err = c.bankAcc(path, body); err == nil {
			for i, b := range p.Banks {
				same := strings.EqualFold(b.IFSC, req.Bank.IFSC) && b.Account == req.Bank.Account
				if req.Action == "default_bank" {
					p.Banks[i].Default = same
				} else if same {
					p.Banks[i].Status = firstOf(a.Status, req.Status)
				}
			}
		}
	case "default_dp", "dp_status":
		body := map[string]any{"participantCode": p.Code, "dpType": req.DP.Depository, "benId": req.DP.BenID}
		if req.DP.DPID != "" {
			body["dpId"] = req.DP.DPID
		}
		path := "/final/markdefault"
		if req.Action == "dp_status" {
			body["status"], path = req.Status, "/final/updatestatus"
		}
		var a CBRICSDPAcc
		if a, err = c.dpAcc(path, body); err == nil {
			for i, d := range p.DPs {
				same := d.id() == req.DP.id()
				if req.Action == "default_dp" {
					p.DPs[i].Default = same
				} else if same {
					p.DPs[i].Status = firstOf(a.Status, req.Status)
				}
			}
		}
	}
	var ce *CBRICSError
	if err != nil && !errors.As(err, &ce) {
		return p, err // CBRICS did not answer: the change still waits
	}
	p.Pending = nil
	if ce != nil {
		p.Remarks = strings.Join(ce.Messages, "; ")
		p.note(now, actor, cbricsReqWords(req)+": CBRICS refused it: "+p.Remarks)
		o.raiseWork(ctx, tenant, "cbrics", "partyrefused:"+p.Code+":"+stamp(now.Format(time.RFC3339)), "CBRICS refused participant "+p.Code, fmt.Sprintf("CBRICS refused to %s for %s: %s. Correct it and send it again.", cbricsReqWords(req), p.Code, p.Remarks), p.UCC, RoleMaker, "cbrics", "")
	} else {
		if req.Action == "contact" {
			u.Banks, u.DPs = nil, nil // accounts change by their own requests
		}
		if req.Action == "add" || req.Action == "update" || req.Action == "contact" {
			p.absorb(u)
		}
		p.note(now, actor, cbricsReqWords(req)+": sent to CBRICS"+map[bool]string{true: " (" + cbricsWorkflow[p.Workflow] + ")", false: ""}[p.Workflow != ""])
		o.closeWork(ctx, tenant, actor, "cbrics", "party:"+p.Code+":"+stamp(req.At), "sent to CBRICS")
	}
	p.UpdatedAt = now.Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cCBRICSParty, p.Code, p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "cbrics.party.sent", p.Code, map[string]any{"action": req.Action, "refused": ce != nil})
	if ce != nil {
		return p, ce
	}
	return p, nil
}

// CBRICSFind looks up registered participants by code or PAN.
func (o *Ops) CBRICSFind(ctx context.Context, tenant, actor, loginID, pan string) ([]CBRICSFound, error) {
	if err := o.require(ctx, tenant, actor, "Reading NSE CBRICS", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return nil, err
	}
	loginID, pan = strings.ToUpper(strings.TrimSpace(loginID)), strings.ToUpper(strings.TrimSpace(pan))
	if loginID == "" && pan == "" {
		return nil, invalid("give a participant code or a PAN")
	}
	c, err := o.cbricsCall(ctx, tenant)
	if err != nil {
		return nil, err
	}
	fs, err := c.find(loginID, pan)
	if fs == nil {
		fs = []CBRICSFound{}
	}
	return fs, err
}

// CBRICSImportParties takes in the approved unregistered participants the
// firm already has at CBRICS (entered on the CBRICS portal), with their
// approved accounts, linking each to the client with the same PAN.
func (o *Ops) CBRICSImportParties(ctx context.Context, tenant, actor string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Keeping CBRICS participants", RoleMaker, RoleChecker); err != nil {
		return 0, err
	}
	c, err := o.cbricsCall(ctx, tenant)
	if err != nil {
		return 0, err
	}
	us, err := c.unregAll(1)
	if err != nil {
		return 0, err
	}
	byPAN := map[string][]string{}
	if cs, err := o.Clients(ctx, tenant); err == nil {
		for _, cl := range cs {
			if cl.PAN != "" {
				byPAN[cl.PAN] = append(byPAN[cl.PAN], cl.UCC)
			}
		}
	}
	now, n := o.now(), 0
	for _, u := range us {
		code := strings.ToUpper(strings.TrimSpace(u.LoginID))
		if code == "" {
			continue
		}
		if _, _, err := docs.Get[CBRICSParty](ctx, o.Docs, tenant, cCBRICSParty, code); err == nil {
			continue
		}
		p := CBRICSParty{Code: code, Kind: "Unregistered", Name: u.FirstName, PAN: strings.ToUpper(u.PAN), Custodian: u.Custodian, Contact: u.ContactPerson, Mobiles: u.Mobiles, Emails: u.Emails,
			Telephone: string(u.Telephone), Fax: u.Fax, Address1: u.Address, Address2: u.Address2, Address3: u.Address3, StateCode: string(u.StateCode), RegAddr: u.RegAddress,
			LEI: u.LEI, LEIExpiry: cbricsDay(u.LEIExpiry), DOB: cbricsDay(u.DOBDOI)}
		p.absorb(u)
		if ucs := byPAN[p.PAN]; len(ucs) == 1 && o.cbricsPartyOf(ctx, tenant, ucs[0]).Code == "" {
			p.UCC = ucs[0]
		}
		if bs, err := c.bankAll(code, 1); err == nil {
			for _, b := range bs {
				p.Banks = append(p.Banks, bankOfAcc(b))
			}
		}
		if ds, err := c.dpAll(code, 1); err == nil {
			for _, d := range ds {
				p.DPs = append(p.DPs, dpOfAcc(d))
			}
		}
		p.UpdatedAt = now.Format(time.RFC3339)
		p.note(now, actor, "taken in from CBRICS"+map[bool]string{true: " as client " + p.UCC, false: ""}[p.UCC != ""])
		if err := docs.Upsert(ctx, o.Docs, tenant, cCBRICSParty, code, p, actor); err != nil {
			return n, err
		}
		n++
	}
	o.log(ctx, tenant, actor, "cbrics.party.imported", "", map[string]any{"count": n})
	return n, nil
}

// cbricsPartyOf is the participant kept for a client (none: empty).
func (o *Ops) cbricsPartyOf(ctx context.Context, tenant, ucc string) CBRICSParty {
	ps, _ := docs.List[CBRICSParty](ctx, o.Docs, tenant, cCBRICSParty, "")
	for _, p := range ps {
		if p.UCC == strings.ToUpper(ucc) && p.State != "Deleted" {
			return p
		}
	}
	return CBRICSParty{}
}

// cbricsPartiesRefresh reads again each unregistered participant (or
// account) still with CBRICS's checker or NSE.
func (o *Ops) cbricsPartiesRefresh(ctx context.Context, tenant string, c *cbricsCall, now time.Time) (int, error) {
	ps, err := docs.List[CBRICSParty](ctx, o.Docs, tenant, cCBRICSParty, "")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range ps {
		if p.Kind != "Unregistered" || p.CBRICSID == "" || p.State == "Deleted" {
			continue
		}
		waiting := p.State == "Sent"
		for _, b := range p.Banks {
			waiting = waiting || cbricsWorkflowState(b.Workflow) == "Sent"
		}
		for _, d := range p.DPs {
			waiting = waiting || cbricsWorkflowState(d.Workflow) == "Sent"
		}
		if !waiting {
			continue
		}
		u, err := c.unregGet(p.CBRICSID)
		if err != nil {
			return n, err
		}
		before := p.State
		p.absorb(u)
		if p.State != before {
			p.note(now, SystemActor, "CBRICS: "+firstOf(cbricsWorkflow[p.Workflow], p.State)+map[bool]string{true: " (" + p.Remarks + ")", false: ""}[p.Remarks != ""])
		}
		switch p.State {
		case "Returned", "Rejected":
			o.raiseWork(ctx, tenant, "cbrics", "nse:"+p.Code+":"+p.Workflow+":"+now.Format("2006-01-02"), fmt.Sprintf("CBRICS participant %s %s", p.Code, strings.ToLower(p.State)),
				fmt.Sprintf("%s: %s. Remarks: %s.", p.Code, cbricsWorkflow[p.Workflow], firstOf(p.Remarks, "none")), p.UCC, RoleMaker, "cbrics", "")
		}
		p.UpdatedAt = now.Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cCBRICSParty, p.Code, p, SystemActor)
		n++
	}
	return n, nil
}

// ---- deals ----

// CBRICSDealInput is a deal as the maker enters it.
type CBRICSDealInput struct {
	ID           string    `json:"id,omitempty"` // to correct a draft or change a reported deal
	UCC          string    `json:"ucc"`
	Side         string    `json:"side"`
	ISIN         string    `json:"isin"`
	Description  string    `json:"description,omitempty"`
	Counterparty string    `json:"counterparty"`
	Quantity     int64     `json:"quantity"`
	FaceValue    money.Dec `json:"face_value"`
	Price        money.Dec `json:"price"`
	AccrInt      money.Dec `json:"accr_int"`
	YieldType    string    `json:"yield_type"`
	Yield        money.Dec `json:"yield"`
	PutCallDate  string    `json:"put_call_date,omitempty"`
	YieldReason  string    `json:"yield_reason,omitempty"`
	Settle       int       `json:"settle"`
	TradeDate    string    `json:"trade_date"`
	TradeTime    string    `json:"trade_time"`
	DealType     string    `json:"deal_type,omitempty"`
	SendFor      string    `json:"send_for,omitempty"`
}

func places(d money.Dec, n int) bool { return dec(d.Fixed(n)).Cmp(d) == 0 }

// SaveCBRICSDeal records a client's deal for a checker to report. A deal
// already reported (not yet confirmed) can be changed: the change waits for
// a checker, who sends it by order/update.
func (o *Ops) SaveCBRICSDeal(ctx context.Context, tenant, actor string, in CBRICSDealInput) (CBRICSDeal, error) {
	if err := o.require(ctx, tenant, actor, "Entering corporate bond deals", RoleMaker, RoleChecker); err != nil {
		return CBRICSDeal{}, err
	}
	x, err := o.cbricsConnection(ctx, tenant)
	if err != nil {
		return CBRICSDeal{}, err
	}
	now := o.now()
	in.UCC, in.ISIN, in.Counterparty = strings.ToUpper(strings.TrimSpace(in.UCC)), strings.ToUpper(strings.TrimSpace(in.ISIN)), strings.ToUpper(strings.TrimSpace(in.Counterparty))
	in.YieldType, in.DealType, in.SendFor = strings.ToUpper(firstOf(in.YieldType, "M")), strings.ToUpper(firstOf(in.DealType, x.dealType)), strings.ToUpper(firstOf(in.SendFor, x.sendFor))
	cl, err := o.Client(ctx, tenant, in.UCC)
	if err != nil {
		return CBRICSDeal{}, invalid("no client %s", in.UCC)
	}
	cp := o.cbricsPartyOf(ctx, tenant, cl.UCC)
	switch {
	case cl.Status != "Active":
		return CBRICSDeal{}, invalid("%s is %s: only an active client's deal is reported", cl.UCC, strings.ToLower(cl.Status))
	case cp.Code == "":
		return CBRICSDeal{}, invalid("%s has no CBRICS participant code: keep the client as an unregistered participant (or link its registered code) first", cl.UCC)
	case in.Side != "Buy" && in.Side != "Sell":
		return CBRICSDeal{}, invalid("the client's side is Buy or Sell")
	case !reISIN.MatchString(in.ISIN):
		return CBRICSDeal{}, invalid("%q is not an ISIN", in.ISIN)
	case in.Counterparty == "" || in.Counterparty == cp.Code:
		return CBRICSDeal{}, invalid("choose the counterparty (another participant)")
	case in.Quantity <= 0 || in.FaceValue.Sign() <= 0:
		return CBRICSDeal{}, invalid("give the number of bonds and a bond's face value")
	case in.Price.Sign() <= 0 || !places(in.Price, 4):
		return CBRICSDeal{}, invalid("the price (per ₹100 of face value) is above 0, to 4 decimal places")
	case in.Yield.Sign() < 0 || !places(in.Yield, 4):
		return CBRICSDeal{}, invalid("the yield is to 4 decimal places")
	case in.AccrInt.Sign() < 0 || !places(in.AccrInt, 2):
		return CBRICSDeal{}, invalid("the accrued interest is in rupees, to 2 decimal places")
	case !contains([]string{"M", "C", "P"}, in.YieldType):
		return CBRICSDeal{}, invalid("the yield type is M (to maturity), C (to call) or P (to put)")
	case in.YieldType != "M" && in.PutCallDate == "":
		return CBRICSDeal{}, invalid("give the put or call date for a yield to put or call")
	case in.YieldReason != "" && in.YieldReason != "1" && in.YieldReason != "2":
		return CBRICSDeal{}, invalid("the reason for a yield outside CBRICS's band is 1 (credit) or 2 (liquidity)")
	case in.Settle < 0 || in.Settle > 2:
		return CBRICSDeal{}, invalid("settlement is same day (0), T+1 or T+2")
	case !contains([]string{"B", "D", "I"}, in.DealType) || in.DealType == "I" && in.Side == "Buy":
		return CBRICSDeal{}, invalid("the deal type is B (brokered) or D (direct); I (inter-scheme) only for a sale")
	case in.SendFor != "M" && in.SendFor != "C":
		return CBRICSDeal{}, invalid("the buyer's confirmation is M (mandatory) or C (optional)")
	}
	if in.PutCallDate != "" {
		if _, err := time.Parse("2006-01-02", in.PutCallDate); err != nil {
			return CBRICSDeal{}, invalid("the put or call date is a date")
		}
	}
	at, err := time.ParseInLocation("2006-01-02 15:04", in.TradeDate+" "+in.TradeTime, ist)
	if err != nil {
		return CBRICSDeal{}, invalid("give the deal's date and time (HH:MM)")
	}
	if at.After(now.Add(time.Minute)) {
		return CBRICSDeal{}, invalid("the deal's time is in the future")
	}
	if set, _ := o.Settings(ctx, tenant); in.TradeDate < set.PrevBusinessDay(now.Format("2006-01-02")) {
		return CBRICSDeal{}, invalid("CBRICS takes a deal of today, or of the previous trading day until its cut-off")
	}
	other, _, err := docs.Get[CBRICSParty](ctx, o.Docs, tenant, cCBRICSParty, in.Counterparty)
	if err != nil || other.State == "Deleted" {
		return CBRICSDeal{}, invalid("%s is not a participant kept here: find the registered participant, or keep the unregistered one, first", in.Counterparty)
	}
	value := in.FaceValue.MulInt(in.Quantity)
	if !places(value, 2) {
		return CBRICSDeal{}, invalid("the deal's face value comes to more than 2 decimal places")
	}
	d := CBRICSDeal{Source: "Ledgerline", UCC: cl.UCC, Name: cl.Name, Side: in.Side, ClientCode: cp.Code, Counterparty: other.Code, CounterpartyName: other.Name, ISIN: in.ISIN,
		Description: strings.TrimSpace(in.Description), DealType: in.DealType, SendFor: in.SendFor, Settle: in.Settle, Quantity: in.Quantity, FaceValue: in.FaceValue, Value: value,
		Price: in.Price, AccrInt: in.AccrInt, YieldType: in.YieldType, Yield: in.Yield, PutCallDate: in.PutCallDate, YieldReason: in.YieldReason, TradeDate: in.TradeDate,
		TradeTime: at.Format("15:04"), Due: at.Add(time.Duration(x.minutes) * time.Minute).Format(time.RFC3339)}
	d.Estimate = value.Mul(in.Price).Mul(dec("0.01")).Add(in.AccrInt)
	if in.Side == "Sell" {
		if hs, err := o.clientHoldings(ctx, tenant, cl.UCC); err == nil {
			var held money.Dec
			for _, h := range hs {
				if h.ISIN == in.ISIN {
					held = held.Add(h.Free())
				}
			}
			if held.Cmp(money.DecInt(in.Quantity)) < 0 {
				d.Warnings = append(d.Warnings, fmt.Sprintf("Ledgerline shows %s of %s free in the client's demat accounts with the firm", held, in.ISIN))
			}
		}
	}
	if cp.Kind == "Unregistered" && cp.State != "Approved" {
		d.Warnings = append(d.Warnings, "the client's participant "+cp.Code+" is not approved at CBRICS yet")
	}
	o.cbricsMu.Lock()
	defer o.cbricsMu.Unlock()
	if in.ID != "" {
		cur, _, err := docs.Get[CBRICSDeal](ctx, o.Docs, tenant, cCBRICSDeal, in.ID)
		if err != nil || cur.Source != "Ledgerline" {
			return cur, invalid("no deal %s entered here", in.ID)
		}
		if cur.Pending != "" && cur.Pending != "Report" {
			return cur, fmt.Errorf("%w: %s already has a %s waiting for a checker", ErrState, cur.ID, strings.ToLower(cur.Pending))
		}
		switch {
		case cur.unreported():
			d.ID, d.History, d.MadeBy, d.Late, d.Due = cur.ID, cur.History, actor, cur.Late, cur.Due
			if cur.TradeDate != d.TradeDate || cur.TradeTime != d.TradeTime {
				d.Due = at.Add(time.Duration(x.minutes) * time.Minute).Format(time.RFC3339)
			}
		case cur.Status == CBDReported || cur.Status == CBDInstructed:
			if cur.UCC != d.UCC || cur.Side != d.Side {
				return cur, invalid("a reported deal keeps its client and side; cancel it on CBRICS and enter a new one")
			}
			ch := d
			ch.ID = cur.ID
			cur.Change, cur.Pending, cur.PendingBy, cur.PendingAt = &ch, "Change", actor, now.Format(time.RFC3339)
			cur.note(now, actor, "change entered: waiting for a checker")
			if err := docs.Upsert(ctx, o.Docs, tenant, cCBRICSDeal, cur.ID, cur, actor); err != nil {
				return cur, err
			}
			o.raiseWork(ctx, tenant, "cbrics", "approve:"+cur.ID+":"+stamp(cur.PendingAt), "Send the change of corporate bond deal "+cur.ID+" to CBRICS", cbricsDealLine(ch)+". Check it and send it.", cur.UCC, RoleChecker, "cbrics", "")
			return cur, nil
		default:
			return cur, fmt.Errorf("%w: %s is %s and cannot change here", ErrState, cur.ID, strings.ToLower(cur.Status))
		}
	} else {
		n, err := o.next(ctx, tenant, "cbricsdeal", actor)
		if err != nil {
			return d, err
		}
		d.ID, d.MadeBy = fmt.Sprintf("CBD-%05d", n), actor
	}
	d.Status, d.Pending, d.PendingBy, d.PendingAt = CBDDraft, "Report", actor, now.Format(time.RFC3339)
	d.note(now, actor, "entered: waiting for a checker to report it")
	if err := docs.Upsert(ctx, o.Docs, tenant, cCBRICSDeal, d.ID, d, actor); err != nil {
		return d, err
	}
	o.raiseWork(ctx, tenant, "cbrics", "approve:"+d.ID+":"+stamp(d.PendingAt), "Report corporate bond deal "+d.ID+" to CBRICS", cbricsDealLine(d)+". Report by "+cbricsClock(d.Due)+".", d.UCC, RoleChecker, "cbrics", "")
	o.cbricsLate(ctx, tenant, &d, now, true)
	o.log(ctx, tenant, actor, "cbrics.deal.entered", d.ID, map[string]any{"ucc": d.UCC, "isin": d.ISIN, "side": d.Side, "value": d.Value.String()})
	return d, nil
}

func cbricsClock(rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return rfc
	}
	return t.In(ist).Format("15:04 on 02-Jan-2006")
}

func cbricsDealLine(d CBRICSDeal) string {
	verb := map[string]string{"Buy": "buys", "Sell": "sells"}[d.Side]
	to := map[string]string{"Buy": "from", "Sell": "to"}[d.Side]
	return fmt.Sprintf("%s %s %s %d %s (face value ₹%s) %s %s at %s, yield %s%% (%s), T+%d, dealt %s %s", d.UCC, d.Name, verb, d.Quantity, d.ISIN, d.Value.Fixed(2), to,
		firstOf(d.Counterparty+" "+d.CounterpartyName, d.Counterparty), d.Price.Fixed(4), d.Yield.Fixed(4), d.YieldType, d.Settle, fmtDay(d.TradeDate), d.TradeTime)
}

// order is the deal as POST /rest/v1/order takes it: a client's sale as the
// seller's reporting, a purchase as the buyer's standing instruction.
func (d CBRICSDeal) order() CBRICSOrder {
	o := CBRICSOrder{DealType: d.DealType, BuyerDealType: d.DealType, Symbol: d.ISIN, Price: json.Number(d.Price.Fixed(4)), Value: json.Number(d.Value.Fixed(2)),
		AccrInt: json.Number(d.AccrInt.Fixed(2)), Settle: json.Number(strconv.Itoa(d.Settle)), SendFor: d.SendFor, YieldType: d.YieldType, Yield: json.Number(d.Yield.Fixed(4)),
		TradeDate: cbricsDayOut(d.TradeDate), TradeTime: d.TradeTime, PutCallDate: cbricsDayOut(d.PutCallDate)}
	if d.Side == "Sell" {
		o.StandingInstFlag, o.SellParticipant, o.BuyParticipant, o.SellerRefNo, o.SellerYieldReason = "N", d.ClientCode, d.Counterparty, d.ID, d.YieldReason
	} else {
		o.StandingInstFlag, o.BuyParticipant, o.SellParticipant, o.BuyerRefNo, o.BuyerYieldReason = "Y", d.ClientCode, d.Counterparty, d.ID, d.YieldReason
	}
	if d.OrderID != "" {
		o.ID = json.Number(d.OrderID)
	}
	return o
}

// absorbOrder takes CBRICS's reply or record of the deal's own order.
func (d *CBRICSDeal) absorbOrder(r CBRICSOrder) {
	if id := numStr(r.ID); id != "" {
		d.OrderID = id
	}
	if no := string(r.OrderNumber); no != "" {
		d.OrderNo = no
	}
	d.OrderStatus = numStr(r.Status)
	if q := numInt(r.Quantity); q > 0 {
		d.CBRICSQty = q
	}
	if c := numStr(r.Consideration); c != "" {
		d.Consideration = dec(c)
	}
	d.Message = firstOf(r.Message, d.Message)
}

// CancelCBRICSDeal cancels a deal not yet reported. A reported deal is
// cancelled on CBRICS (the protocol has no cancellation).
func (o *Ops) CancelCBRICSDeal(ctx context.Context, tenant, actor, id, reason string) (CBRICSDeal, error) {
	if err := o.require(ctx, tenant, actor, "Entering corporate bond deals", RoleMaker, RoleChecker); err != nil {
		return CBRICSDeal{}, err
	}
	o.cbricsMu.Lock()
	defer o.cbricsMu.Unlock()
	d, _, err := docs.Get[CBRICSDeal](ctx, o.Docs, tenant, cCBRICSDeal, id)
	if err != nil {
		return d, invalid("no deal %s", id)
	}
	if !d.unreported() {
		return d, fmt.Errorf("%w: %s is at CBRICS: it cannot be cancelled by the API; it lapses if the buyer does not accept it, or ask NSE", ErrState, d.ID)
	}
	if strings.TrimSpace(reason) == "" {
		return d, invalid("say why the deal is cancelled")
	}
	now := o.now()
	key := "approve:" + d.ID + ":" + stamp(d.PendingAt)
	d.Status, d.Pending, d.PendingBy, d.PendingAt, d.Reason = CBDCancelled, "", "", "", reason
	d.note(now, actor, "cancelled before reporting: "+reason)
	if err := docs.Upsert(ctx, o.Docs, tenant, cCBRICSDeal, d.ID, d, actor); err != nil {
		return d, err
	}
	o.closeWork(ctx, tenant, actor, "cbrics", key, "cancelled")
	o.closeWork(ctx, tenant, actor, "cbrics", "late:"+d.ID, "cancelled: "+reason)
	o.log(ctx, tenant, actor, "cbrics.deal.cancelled", d.ID, map[string]any{"reason": reason})
	return d, nil
}

// CBRICSRespond proposes the client's answer, as buyer, to a sale reported
// to it: accept or reject (with the yield band reason when CBRICS asks).
func (o *Ops) CBRICSRespond(ctx context.Context, tenant, actor, id string, accept bool, yieldReason string) (CBRICSDeal, error) {
	if err := o.require(ctx, tenant, actor, "Entering corporate bond deals", RoleMaker, RoleChecker); err != nil {
		return CBRICSDeal{}, err
	}
	o.cbricsMu.Lock()
	defer o.cbricsMu.Unlock()
	d, _, err := docs.Get[CBRICSDeal](ctx, o.Docs, tenant, cCBRICSDeal, id)
	if err != nil {
		return d, invalid("no deal %s", id)
	}
	if d.Status != CBDToConfirm || d.CounterOrderID == "" || d.Pending != "" {
		return d, fmt.Errorf("%w: %s does not wait for the client's confirmation", ErrState, d.ID)
	}
	if yieldReason != "" && yieldReason != "1" && yieldReason != "2" {
		return d, invalid("the reason for a yield outside CBRICS's band is 1 (credit) or 2 (liquidity)")
	}
	now := o.now()
	d.Pending, d.PendingBy, d.PendingAt = map[bool]string{true: "Accept", false: "Reject"}[accept], actor, now.Format(time.RFC3339)
	if yieldReason != "" {
		d.YieldReason = yieldReason
	}
	d.note(now, actor, strings.ToLower(d.Pending)+" the sale reported to the client: waiting for a checker")
	if err := docs.Upsert(ctx, o.Docs, tenant, cCBRICSDeal, d.ID, d, actor); err != nil {
		return d, err
	}
	o.closeWork(ctx, tenant, actor, "cbrics", "confirm:"+d.ID, "answer entered")
	o.raiseWork(ctx, tenant, "cbrics", "approve:"+d.ID+":"+stamp(d.PendingAt), fmt.Sprintf("%s the corporate bond sale reported to %s (%s)", d.Pending, d.UCC, d.ID), cbricsDealLine(d)+". Check and send the client's answer.", d.UCC, RoleChecker, "cbrics", "")
	return d, nil
}

// cbricsSides says who is buyer and seller of a deal.
func (d CBRICSDeal) buyer() string {
	if d.Side == "Buy" {
		return d.ClientCode
	}
	return d.Counterparty
}

func (d CBRICSDeal) seller() string {
	if d.Side == "Sell" {
		return d.ClientCode
	}
	return d.Counterparty
}

// cbricsSideAccounts are the accounts of a side the firm keeps at CBRICS
// as an unregistered participant (the client's own registered accounts for
// a client), and the default ones. ok is false for a registered
// participant, who gives its own accounts.
func (o *Ops) cbricsSideAccounts(ctx context.Context, tenant, code string) (banks []CBRICSPartyBank, dps []CBRICSPartyDP, ok bool) {
	p, _, err := docs.Get[CBRICSParty](ctx, o.Docs, tenant, cCBRICSParty, code)
	if err != nil || p.Kind != "Unregistered" {
		return nil, nil, false
	}
	if p.UCC != "" {
		c, err := o.Client(ctx, tenant, p.UCC)
		if err != nil {
			return nil, nil, false
		}
		banks, dps = cbricsClientAccounts(c)
		return banks, dps, true
	}
	for _, b := range p.Banks {
		if (b.Status == "" || b.Status == "A") && cbricsWorkflowState(b.Workflow) != "Sent" && cbricsWorkflowState(b.Workflow) != "Rejected" {
			banks = append(banks, b)
		}
	}
	for _, d := range p.DPs {
		if (d.Status == "" || d.Status == "A") && cbricsWorkflowState(d.Workflow) != "Sent" && cbricsWorkflowState(d.Workflow) != "Rejected" {
			dps = append(dps, d)
		}
	}
	return banks, dps, true
}

func defaultBank(bs []CBRICSPartyBank) (CBRICSPartyBank, bool) {
	for _, b := range bs {
		if b.Default {
			return b, true
		}
	}
	if len(bs) > 0 {
		return bs[0], true
	}
	return CBRICSPartyBank{}, false
}

func defaultDP(ds []CBRICSPartyDP) (CBRICSPartyDP, bool) {
	for _, d := range ds {
		if d.Default {
			return d, true
		}
	}
	if len(ds) > 0 {
		return ds[0], true
	}
	return CBRICSPartyDP{}, false
}

// CBRICSProposeAccount proposes the settlement's accounts: the seller's
// payout bank account (by its key, IFSC-last 4 digits; empty: the
// default) and the buyer's demat account (empty: the default), each for a
// side the firm keeps as an unregistered participant.
func (o *Ops) CBRICSProposeAccount(ctx context.Context, tenant, actor, id string, in CBRICSAcctChange) (CBRICSDeal, error) {
	if err := o.require(ctx, tenant, actor, "Choosing CBRICS settlement accounts", RoleMaker, RoleChecker); err != nil {
		return CBRICSDeal{}, err
	}
	o.cbricsMu.Lock()
	defer o.cbricsMu.Unlock()
	d, _, err := docs.Get[CBRICSDeal](ctx, o.Docs, tenant, cCBRICSDeal, id)
	if err != nil {
		return d, invalid("no deal %s", id)
	}
	ch, err := o.cbricsAcctFor(ctx, tenant, d, in, true)
	if err != nil {
		return d, err
	}
	ch.By, ch.At, ch.Why = actor, o.now().Format(time.RFC3339), strings.TrimSpace(in.Why)
	if err := o.cbricsPutAccount(ctx, tenant, actor, &d, ch); err != nil {
		return d, err
	}
	return d, nil
}

// cbricsAcctFor works out an account change for a deal's settlement.
// strict: what is asked must be possible (a person asked); otherwise the
// change carries only what differs from CBRICS's settlement.
func (o *Ops) cbricsAcctFor(ctx context.Context, tenant string, d CBRICSDeal, in CBRICSAcctChange, strict bool) (CBRICSAcctChange, error) {
	var ch CBRICSAcctChange
	s := d.Settlement
	if s == nil || s.ID == "" {
		return ch, fmt.Errorf("%w: %s has no order for settlement at CBRICS yet", ErrState, d.ID)
	}
	if s.Status != "0" {
		return ch, fmt.Errorf("%w: settlement %s has started (%s): its accounts no longer change", ErrState, s.SettlementNo, s.StatusText)
	}
	if d.Pending != "" {
		return ch, fmt.Errorf("%w: %s already has a %s waiting for a checker", ErrState, d.ID, strings.ToLower(d.Pending))
	}
	wantBank, wantDP := in.BankKey != "" || !strict || in.Demat == "", in.Demat != "" || !strict || in.BankKey == ""
	if banks, _, ok := o.cbricsSideAccounts(ctx, tenant, d.seller()); ok && wantBank {
		var b CBRICSPartyBank
		found := false
		if in.BankKey == "" {
			b, found = defaultBank(banks)
		} else {
			ifsc, last, _ := strings.Cut(strings.ToUpper(in.BankKey), "-")
			for _, x := range banks {
				if strings.EqualFold(x.IFSC, ifsc) && last != "" && strings.HasSuffix(x.Account, last) {
					b, found = x, true
				}
			}
		}
		if !found && strict && in.BankKey != "" {
			return ch, invalid("%s is not an account of seller %s kept here", in.BankKey, d.seller())
		}
		if found && (!strings.EqualFold(b.IFSC, s.IFSC) || b.Account != s.Account) {
			ch.Bank, ch.IFSC, ch.Account = b.Bank, b.IFSC, b.Account
		}
	} else if strict && in.BankKey != "" {
		return ch, invalid("seller %s is a registered participant: it gives its own payout account", d.seller())
	}
	if _, dps, ok := o.cbricsSideAccounts(ctx, tenant, d.buyer()); ok && wantDP {
		var x CBRICSPartyDP
		found := false
		if in.Demat == "" {
			x, found = defaultDP(dps)
		} else {
			for _, y := range dps {
				if y.id() == normDigits(in.Demat) {
					x, found = y, true
				}
			}
		}
		if !found && strict && in.Demat != "" {
			return ch, invalid("%s is not a demat account of buyer %s kept here", in.Demat, d.buyer())
		}
		if found && !(x.BenID == s.BenID && (x.Depository != "NSDL" || strings.EqualFold(x.DPID, s.DPID))) {
			ch.Demat, ch.DPID, ch.BenID = x.id(), x.DPID, x.BenID
		}
	} else if strict && in.Demat != "" {
		return ch, invalid("buyer %s is a registered participant: it gives its own demat account", d.buyer())
	}
	if ch.IFSC == "" && ch.BenID == "" && strict {
		return ch, invalid("CBRICS already has these accounts for settlement %s", s.SettlementNo)
	}
	return ch, nil
}

func (o *Ops) cbricsPutAccount(ctx context.Context, tenant, actor string, d *CBRICSDeal, ch CBRICSAcctChange) error {
	d.Account, d.Pending, d.PendingBy, d.PendingAt = &ch, "Account", actor, ch.At
	d.note(o.now(), actor, "settlement accounts proposed ("+cbricsAcctWords(ch)+"): waiting for a checker")
	if err := docs.Upsert(ctx, o.Docs, tenant, cCBRICSDeal, d.ID, *d, actor); err != nil {
		return err
	}
	o.raiseWork(ctx, tenant, "cbrics", "account:"+d.ID+":"+stamp(ch.At), "Send the settlement accounts of "+d.ID+" to CBRICS",
		fmt.Sprintf("Settlement %s of %s (%s): %s, from the records kept here. Check and send them before the settlement starts.", d.Settlement.SettlementNo, d.ID, d.UCC, cbricsAcctWords(ch)), d.UCC, RoleChecker, "cbrics", "")
	return nil
}

func cbricsAcctWords(ch CBRICSAcctChange) string {
	var ws []string
	if ch.IFSC != "" {
		ws = append(ws, "seller's payout to "+strings.TrimSpace(ch.Bank+" "+ch.IFSC+" "+MaskAccount(ch.Account)))
	}
	if ch.BenID != "" {
		ws = append(ws, "buyer's bonds to demat account "+ch.Demat)
	}
	return strings.Join(ws, "; ")
}

// DecideCBRICSDeal: a checker other than the maker sends what waits on the
// deal (its report, a change, the client's answer as buyer, settlement
// accounts) to CBRICS, or returns it.
func (o *Ops) DecideCBRICSDeal(ctx context.Context, tenant, actor, id string, approve bool, reason string) (CBRICSDeal, error) {
	if err := o.require(ctx, tenant, actor, "Sending corporate bond deals to CBRICS", RoleChecker); err != nil {
		return CBRICSDeal{}, err
	}
	o.cbricsMu.Lock()
	defer o.cbricsMu.Unlock()
	d, _, err := docs.Get[CBRICSDeal](ctx, o.Docs, tenant, cCBRICSDeal, id)
	if err != nil || d.Pending == "" {
		return d, fmt.Errorf("%w: nothing of deal %s waits for a checker", ErrState, id)
	}
	if actor == d.PendingBy {
		return d, fmt.Errorf("%w: a different person sends the deal to CBRICS (maker and checker)", ErrForbidden)
	}
	now := o.now()
	what := d.Pending
	workKey := "approve:" + d.ID + ":" + stamp(d.PendingAt)
	if what == "Account" {
		workKey = "account:" + d.ID + ":" + stamp(d.PendingAt)
	}
	if !approve {
		if strings.TrimSpace(reason) == "" {
			return d, invalid("say why it goes back")
		}
		if what == "Report" {
			d.Status = CBDReturned
		}
		d.Pending, d.Change, d.Account, d.Reason = "", nil, nil, reason
		d.note(now, actor, strings.ToLower(what)+" returned: "+reason)
		if err := docs.Upsert(ctx, o.Docs, tenant, cCBRICSDeal, d.ID, d, actor); err != nil {
			return d, err
		}
		o.closeWork(ctx, tenant, actor, "cbrics", workKey, "returned: "+reason)
		if what == "Report" || what == "Change" {
			o.raiseWork(ctx, tenant, "cbrics", "returned:"+d.ID+":"+now.Format("150405"), "Corporate bond deal "+d.ID+" returned", reason, d.UCC, RoleMaker, "cbrics", "")
		}
		return d, nil
	}
	c, err := o.cbricsCall(ctx, tenant)
	if err != nil {
		return d, err
	}
	var ce *CBRICSError
	refused := func(err error) bool { return errors.As(err, &ce) }
	switch what {
	case "Report":
		if p, _, err := docs.Get[CBRICSParty](ctx, o.Docs, tenant, cCBRICSParty, d.ClientCode); err == nil && p.Kind == "Unregistered" && p.State != "Approved" {
			return d, fmt.Errorf("%w: the client's participant %s is not approved at CBRICS yet (%s)", ErrState, p.Code, firstOf(cbricsWorkflow[p.Workflow], p.State))
		}
		r, err := c.order(d.order(), false)
		if err != nil && !refused(err) {
			return d, err
		}
		if ce != nil {
			d.Status, d.Reason = CBDRefused, strings.Join(ce.Messages, "; ")
			d.note(now, actor, "CBRICS refused the report: "+d.Reason)
			o.raiseWork(ctx, tenant, "cbrics", "refused:"+d.ID+":"+now.Format("150405"), "CBRICS refused corporate bond deal "+d.ID, d.Reason+". Correct the deal and send it again.", d.UCC, RoleMaker, "cbrics", "")
		} else {
			d.absorbOrder(r)
			d.Status, d.ReportedBy, d.ReportedAt, d.Reason = firstOf(cbricsOrderWords(r.StandingInstFlag, d.OrderStatus), map[string]string{"Sell": CBDReported, "Buy": CBDInstructed}[d.Side]), actor, now.Format(time.RFC3339), ""
			d.note(now, actor, fmt.Sprintf("reported to CBRICS: transaction %s (order %s)%s", d.OrderNo, d.OrderID, map[bool]string{true: ": " + d.Message, false: ""}[d.Message != ""]))
			o.cbricsReported(ctx, tenant, actor, &d, now)
		}
	case "Change":
		ch := *d.Change
		ch.OrderID = d.OrderID
		r, err := c.order(ch.order(), true)
		if err != nil && !refused(err) {
			return d, err
		}
		if ce != nil {
			d.Reason = strings.Join(ce.Messages, "; ")
			d.note(now, actor, "CBRICS refused the change: "+d.Reason)
		} else {
			old := d.OrderNo
			keep := d
			d = ch
			d.History, d.MadeBy, d.ReportedAt, d.ReportedBy, d.Late, d.LateMinutes, d.Due, d.Source = keep.History, keep.MadeBy, keep.ReportedAt, keep.ReportedBy, keep.Late, keep.LateMinutes, keep.Due, keep.Source
			d.OldOrders = append(keep.OldOrders, old)
			d.OrderID, d.OrderNo = "", ""
			d.absorbOrder(r)
			d.Status = firstOf(cbricsOrderWords(r.StandingInstFlag, d.OrderStatus), keep.Status)
			d.note(now, actor, fmt.Sprintf("change sent to CBRICS: transaction %s replaces %s", d.OrderNo, old))
		}
		d.Change = nil
	case "Accept", "Reject":
		st := map[string]int{"Accept": 7, "Reject": 2}[what]
		msg, err := c.orderStatus(d.CounterOrderID, st, d.YieldReason)
		if err != nil && !refused(err) {
			return d, err
		}
		if ce != nil {
			d.Reason = strings.Join(ce.Messages, "; ")
			d.note(now, actor, "CBRICS refused the client's answer: "+d.Reason)
		} else {
			d.CounterStatus, d.Message = strconv.Itoa(st), msg
			d.Status = map[string]string{"Accept": CBDConfirmed, "Reject": CBDRejected}[what]
			d.ReportedBy, d.ReportedAt = actor, now.Format(time.RFC3339)
			d.note(now, actor, "the client's answer sent to CBRICS: "+firstOf(msg, strings.ToLower(what)+"ed"))
		}
	case "Account":
		ch := *d.Account
		s := *d.Settlement
		var r CBRICSSettle
		if ch.IFSC != "" {
			if r, err = c.settleBank(s.ID, ch.IFSC, ch.Account); err != nil && !refused(err) {
				return d, err
			}
		}
		if ce == nil && ch.BenID != "" {
			if r, err = c.settleDP(s.ID, ch.DPID, ch.BenID); err != nil && !refused(err) {
				return d, err
			}
		}
		if ce != nil {
			d.Reason = strings.Join(ce.Messages, "; ")
			d.note(now, actor, "CBRICS refused the settlement accounts: "+d.Reason)
		} else {
			o.cbricsSettleFields(&d, r)
			d.note(now, actor, "settlement accounts sent to CBRICS: "+cbricsAcctWords(ch))
		}
		d.Account = nil
	}
	d.Pending, d.PendingBy, d.PendingAt = "", "", ""
	if err := docs.Upsert(ctx, o.Docs, tenant, cCBRICSDeal, d.ID, d, actor); err != nil {
		return d, err
	}
	o.closeWork(ctx, tenant, actor, "cbrics", workKey, map[bool]string{true: "CBRICS refused it", false: "sent to CBRICS"}[ce != nil])
	o.log(ctx, tenant, actor, "cbrics.deal.sent", d.ID, map[string]any{"what": what, "refused": ce != nil, "order": d.OrderNo})
	if ce != nil {
		return d, ce
	}
	return d, nil
}

// cbricsReported records the reporting time against the limit.
func (o *Ops) cbricsReported(ctx context.Context, tenant, actor string, d *CBRICSDeal, now time.Time) {
	due, err := time.Parse(time.RFC3339, d.Due)
	if err != nil || !now.After(due) {
		o.closeWork(ctx, tenant, actor, "cbrics", "late:"+d.ID, "reported in time")
		return
	}
	at, _ := time.ParseInLocation("2006-01-02 15:04", d.TradeDate+" "+d.TradeTime, ist)
	d.Late, d.LateMinutes = true, int(now.Sub(at).Minutes())
	d.note(now, actor, fmt.Sprintf("reported %d minutes after the deal, past the limit", d.LateMinutes))
	o.closeWork(ctx, tenant, actor, "cbrics", "late:"+d.ID, fmt.Sprintf("reported at %s, %d minutes after the deal", now.In(ist).Format("15:04"), d.LateMinutes))
}

// cbricsLate raises a work item for a deal not reported by its time limit.
func (o *Ops) cbricsLate(ctx context.Context, tenant string, d *CBRICSDeal, now time.Time, saved bool) {
	due, err := time.Parse(time.RFC3339, d.Due)
	if err != nil || !d.unreported() || !now.After(due) {
		return
	}
	o.raiseWork(ctx, tenant, "cbrics", "late:"+d.ID, "Corporate bond deal "+d.ID+" not reported to CBRICS in time",
		fmt.Sprintf("%s. It was to be reported by %s (the firm's limit after the deal) and is %s. Report it now and record why it was late.", cbricsDealLine(*d), cbricsClock(d.Due), strings.ToLower(d.Status)),
		d.UCC, RoleChecker, "cbrics", "")
	if !d.Late {
		d.Late = true
		if saved {
			_ = docs.Upsert(ctx, o.Docs, tenant, cCBRICSDeal, d.ID, *d, SystemActor)
		}
	}
}

// ---- reading CBRICS ----

// CBRICSSyncResult says what a read found.
type CBRICSSyncResult struct {
	Orders      int      `json:"orders"`
	Matched     int      `json:"matched"`
	Added       int      `json:"added"`
	Settlements int      `json:"settlements"`
	Parties     int      `json:"parties"`
	Unmatched   int      `json:"unmatched"`
	Problems    []string `json:"problems,omitempty"`
}

type cbricsIdx struct {
	deals   map[string]*CBRICSDeal
	byOrder map[string]string // CBRICS transaction number → deal ID
	old     map[string]bool   // transaction numbers a change replaced
	clients map[string]string // participant code → UCC
	parties map[string]CBRICSParty
	dirty   map[string]bool
}

func (o *Ops) cbricsIndex(ctx context.Context, tenant string) (*cbricsIdx, error) {
	x := &cbricsIdx{deals: map[string]*CBRICSDeal{}, byOrder: map[string]string{}, old: map[string]bool{}, clients: map[string]string{}, parties: map[string]CBRICSParty{}, dirty: map[string]bool{}}
	ds, err := docs.List[CBRICSDeal](ctx, o.Docs, tenant, cCBRICSDeal, "")
	if err != nil {
		return nil, err
	}
	for i := range ds {
		d := &ds[i]
		x.deals[d.ID] = d
		for _, no := range []string{d.OrderNo, d.CounterOrderNo} {
			if no != "" {
				x.byOrder[no] = d.ID
			}
		}
		for _, no := range d.OldOrders {
			x.old[no] = true
		}
	}
	ps, _ := docs.List[CBRICSParty](ctx, o.Docs, tenant, cCBRICSParty, "")
	for _, p := range ps {
		x.parties[p.Code] = p
		if p.UCC != "" && p.State != "Deleted" {
			x.clients[p.Code] = p.UCC
		}
	}
	return x, nil
}

// CBRICSSync reads CBRICS: today's (and open deals' earlier) sell
// reportings, buy instructions, the orders for settlement and the
// participants still waiting for approval, and applies them to the deals.
func (o *Ops) CBRICSSync(ctx context.Context, tenant, actor string) (CBRICSSyncResult, error) {
	var res CBRICSSyncResult
	if err := o.require(ctx, tenant, actor, "Reading NSE CBRICS", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return res, err
	}
	c, err := o.cbricsCall(ctx, tenant)
	if err != nil {
		return res, err
	}
	o.cbricsMu.Lock()
	defer o.cbricsMu.Unlock()
	now := o.now()
	idx, err := o.cbricsIndex(ctx, tenant)
	if err != nil {
		return res, err
	}
	var unmatched []string
	apply := func(rs []CBRICSOrder) {
		for _, r := range rs {
			res.Orders++
			switch o.cbricsApplyOrder(ctx, tenant, idx, r, now) {
			case "matched":
				res.Matched++
			case "added":
				res.Added++
			case "none":
				res.Unmatched++
				unmatched = append(unmatched, string(r.OrderNumber))
			}
		}
	}
	fail := func(what string, err error) bool {
		if err == nil {
			return false
		}
		res.Problems = append(res.Problems, what+": "+err.Error())
		return true
	}
	today := now.Format("2006-01-02")
	rs, err := c.sellReportings(now, true)
	if fail("sell reportings", err) && len(res.Problems) == 1 && !isCBRICSErr(err) {
		o.cbricsStateDo(ctx, tenant, func(s *CBRICSState) { s.LastRun, s.LastError = now.Format(time.RFC3339), err.Error() })
		return res, err // CBRICS is not answering, or the login failed
	}
	apply(rs)
	days := map[string]bool{}
	for _, d := range idx.deals {
		if day := dateOnly(d.ReportedAt); d.open() && day != "" && day < today && day >= now.AddDate(0, 0, -7).Format("2006-01-02") {
			days[day] = true
		}
	}
	var ks []string
	for k := range days {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		t, _ := time.ParseInLocation("2006-01-02", k, ist)
		rs, err := c.sellReportings(t, false)
		if !fail("sell reportings of "+k, err) {
			apply(rs)
		}
	}
	bis, err := c.buyInstructions()
	if !fail("buy instructions", err) {
		// They are the firm's standing instructions as buyer, whatever flag
		// they carry (the protocol's sample shows N, copied from the sell
		// reportings).
		for i := range bis {
			bis[i].StandingInstFlag = "Y"
		}
		apply(bis)
	}
	sts, err := c.settleAll(now.AddDate(0, 0, -20), now.AddDate(0, 0, 7))
	if !fail("orders for settlement", err) {
		x, _ := o.cbricsConnection(ctx, tenant)
		for _, s := range sts {
			res.Settlements++
			if !o.cbricsApplySettle(ctx, tenant, x, idx, s, now) {
				res.Unmatched++
				unmatched = append(unmatched, string(s.OrderNumber))
			}
		}
	}
	n, err := o.cbricsPartiesRefresh(ctx, tenant, c, now)
	res.Parties = n
	fail("participants", err)
	for id := range idx.dirty {
		_ = docs.Upsert(ctx, o.Docs, tenant, cCBRICSDeal, id, *idx.deals[id], SystemActor)
	}
	o.cbricsStateDo(ctx, tenant, func(s *CBRICSState) {
		s.LastRun, s.Refresh, s.LastError = now.Format(time.RFC3339), false, ""
		if len(res.Problems) > 0 {
			s.LastError = res.Problems[0]
		} else {
			s.LastOK = s.LastRun
		}
		if len(unmatched) > 50 {
			unmatched = unmatched[:50]
		}
		s.Unmatched = unmatched
	})
	o.log(ctx, tenant, actor, "cbrics.read", "", map[string]any{"orders": res.Orders, "matched": res.Matched, "added": res.Added, "settlements": res.Settlements, "problems": len(res.Problems)})
	return res, nil
}

func isCBRICSErr(err error) bool {
	var ce *CBRICSError
	return errors.As(err, &ce)
}

// cbricsApplyOrder matches one order record to a deal: "matched",
// "added" (a client's deal found at CBRICS), "old" (replaced by a change)
// or "none" (no client of the firm).
func (o *Ops) cbricsApplyOrder(ctx context.Context, tenant string, idx *cbricsIdx, r CBRICSOrder, now time.Time) string {
	no := string(r.OrderNumber)
	if idx.old[no] {
		return "old"
	}
	st := numStr(r.Status)
	var d *CBRICSDeal
	counter := false
	if id, ok := idx.byOrder[no]; ok && no != "" {
		d = idx.deals[id]
		counter = d.CounterOrderNo == no && d.OrderNo != no
	}
	if d == nil {
		for _, ref := range []string{r.SellerRefNo, r.BuyerRefNo} {
			if x, ok := idx.deals[strings.TrimSpace(ref)]; ok && x.Source == "Ledgerline" {
				own := (r.StandingInstFlag == "Y") == (x.Side == "Buy")
				if own {
					d = x
				}
			}
		}
	}
	if d == nil && r.StandingInstFlag != "Y" {
		// A sale reported to the client's standing instruction: the deal's
		// terms are the instruction's.
		for _, x := range idx.deals {
			if x.Side == "Buy" && x.CounterOrderNo == "" && x.ClientCode == r.BuyParticipant && x.Counterparty == r.SellParticipant && x.ISIN == r.Symbol &&
				x.Value.Cmp(dec(numStr(r.Value))) == 0 && x.Price.Cmp(dec(numStr(r.Price))) == 0 && x.TradeDate == cbricsDay(r.TradeDate) && (x.Status == CBDInstructed || x.Status == CBDConfirmed) {
				d, counter = x, true
				break
			}
		}
	}
	if d == nil {
		ucc, side, code, cp := "", "", "", ""
		if u := idx.clients[r.SellParticipant]; u != "" && r.StandingInstFlag != "Y" {
			ucc, side, code, cp = u, "Sell", r.SellParticipant, r.BuyParticipant
		} else if u := idx.clients[r.BuyParticipant]; u != "" {
			ucc, side, code, cp = u, "Buy", r.BuyParticipant, r.SellParticipant
		} else if u := idx.clients[r.SellParticipant]; u != "" {
			ucc, side, code, cp = u, "Sell", r.SellParticipant, r.BuyParticipant
		}
		if ucc == "" {
			return "none"
		}
		n, err := o.next(ctx, tenant, "cbricsdeal", SystemActor)
		if err != nil {
			return "none"
		}
		cl, _ := o.Client(ctx, tenant, ucc)
		d = &CBRICSDeal{ID: fmt.Sprintf("CBD-%05d", n), Source: "CBRICS", UCC: ucc, Name: cl.Name, Side: side, ClientCode: code, Counterparty: cp, CounterpartyName: idx.parties[cp].Name,
			ISIN: r.Symbol, DealType: firstOf(r.DealType, "B"), SendFor: r.SendFor, Settle: int(numInt(r.Settle)), Value: dec(numStr(r.Value)), Price: dec(numStr(r.Price)),
			AccrInt: dec(numStr(r.AccrInt)), YieldType: r.YieldType, Yield: dec(numStr(r.Yield)), PutCallDate: cbricsDay(r.PutCallDate), TradeDate: cbricsDay(r.TradeDate), TradeTime: r.TradeTime,
			Quantity: numInt(r.Quantity)}
		if d.Quantity > 0 {
			d.FaceValue, _ = d.Value.Quo(money.DecInt(d.Quantity))
		}
		d.Estimate = d.Value.Mul(d.Price).Mul(dec("0.01")).Add(d.AccrInt)
		counter = side == "Buy" && r.StandingInstFlag != "Y"
		d.note(now, SystemActor, "found at CBRICS (transaction "+no+"), not entered here")
		idx.deals[d.ID] = d
		idx.byOrder[no] = d.ID
		idx.dirty[d.ID] = true
		o.cbricsOrderOnto(ctx, tenant, d, r, counter, now)
		return "added"
	}
	if no != "" {
		idx.byOrder[no] = d.ID
	}
	if o.cbricsOrderOnto(ctx, tenant, d, r, counter, now) || st != "" {
		idx.dirty[d.ID] = true
	}
	return "matched"
}

// cbricsOrderOnto applies an order record to the deal; true when something
// changed.
func (o *Ops) cbricsOrderOnto(ctx context.Context, tenant string, d *CBRICSDeal, r CBRICSOrder, counter bool, now time.Time) bool {
	st := numStr(r.Status)
	before := d.Status
	if counter {
		if d.CounterOrderNo == string(r.OrderNumber) && d.CounterStatus == st {
			return false
		}
		d.CounterOrderID, d.CounterOrderNo, d.CounterStatus = numStr(r.ID), string(r.OrderNumber), st
		if c := numStr(r.Consideration); c != "" {
			d.Consideration = dec(c)
		}
		if q := numInt(r.Quantity); q > 0 {
			d.CBRICSQty = q
		}
	} else {
		if d.OrderNo == string(r.OrderNumber) && d.OrderStatus == st && d.OrderID != "" {
			return false
		}
		d.absorbOrder(r)
	}
	if d.Settlement != nil || d.Pending == "Accept" || d.Pending == "Reject" {
		return true // the settlement's status, or the answer waiting, rules
	}
	switch {
	case counter && st == "1":
		d.Status = CBDToConfirm
	case counter:
		d.Status = firstOf(cbricsOrderWords("N", st), d.Status)
	case d.Side == "Buy" && d.CounterOrderNo != "":
		// the seller's report to the instruction rules
	default:
		d.Status = firstOf(cbricsOrderWords(r.StandingInstFlag, st), d.Status)
	}
	if d.ReportedAt == "" {
		// Found at CBRICS before Ledgerline recorded the report (entered
		// there, or CBRICS took it and its reply was lost).
		d.ReportedAt, d.ReportedBy = now.Format(time.RFC3339), firstOf(d.ReportedBy, "CBRICS")
		if d.Pending == "Report" {
			o.closeWork(ctx, tenant, SystemActor, "cbrics", "approve:"+d.ID+":"+stamp(d.PendingAt), "found at CBRICS")
			d.Pending, d.PendingBy, d.PendingAt = "", "", ""
		}
		if d.Source == "Ledgerline" {
			o.cbricsReported(ctx, tenant, SystemActor, d, now)
		}
	}
	if d.Status != before {
		d.note(now, SystemActor, "CBRICS: "+d.Status+" (transaction "+string(r.OrderNumber)+")")
		switch d.Status {
		case CBDToConfirm:
			o.raiseWork(ctx, tenant, "cbrics", "confirm:"+d.ID, "Client "+d.UCC+" to confirm a corporate bond purchase ("+d.ID+")",
				cbricsDealLine(*d)+". The seller reported it to the client: get the client's answer and enter it (accept or reject).", d.UCC, RoleMaker, "cbrics", "")
		case CBDConfirmed, CBDRejected, CBDCancelled:
			o.closeWork(ctx, tenant, SystemActor, "cbrics", "confirm:"+d.ID, "CBRICS: "+d.Status)
		}
	}
	return true
}

func (o *Ops) cbricsSettleFields(d *CBRICSDeal, s CBRICSSettle) {
	if d.Settlement == nil {
		d.Settlement = &CBRICSDealSettle{}
	}
	x := d.Settlement
	if id := numStr(s.ID); id != "" {
		x.ID = id
	}
	if numStr(s.SettleStatus) != "" {
		x.Status = numStr(s.SettleStatus)
	}
	x.StatusText = firstOf(cbricsSettleWords[x.Status], x.Status)
	x.SettlementNo = firstOf(string(s.SettlementNo), x.SettlementNo)
	x.Date = firstOf(cbricsDay(s.SettleDate), x.Date)
	x.Source = firstOf(map[string]string{"1": "NSE CBRICS", "4": "FTRAC", "5": "NSE RFQ"}[numStr(s.Source)], x.Source)
	if q := numInt(s.Quantity); q > 0 {
		x.Quantity = q
	}
	set := func(dst *money.Dec, n json.Number) {
		if v := numStr(n); v != "" {
			*dst = dec(v)
		}
	}
	set(&x.AccrInt, s.AccrInt)
	set(&x.Consideration, s.Consideration)
	set(&x.StampDuty, s.StampDuty)
	set(&x.BuyerPayin, s.BuyerPayin)
	set(&x.SellerPayout, s.SellerPayout)
	set(&x.FundsPayin, s.FundsPayinAmount)
	x.StampBearer = firstOf(s.StampDutyBearer, x.StampBearer)
	x.PayinRef = firstOf(s.FundPayinRefID, x.PayinRef)
	x.IFSC, x.Account = firstOf(s.IFSC, x.IFSC), firstOf(s.AccountNo, x.Account)
	x.DPID, x.BenID = firstOf(s.DPID, x.DPID), firstOf(string(s.BenID), x.BenID)
	if q := numInt(s.SecPayinQuantity); q > 0 {
		x.SecPayinQty = q
	}
	x.SecPayinTime, x.FundsTime, x.PayoutTime = firstOf(s.SecPayinTime, x.SecPayinTime), firstOf(s.FundsPayinTime, x.FundsTime), firstOf(s.PayoutTime, x.PayoutTime)
	x.UTR = firstOf(s.UTR, x.UTR)
	x.Remarks = strings.TrimSpace(strings.Join([]string{s.SecPayinRemarks, s.FundsPayinRemark, s.PayoutRemarks}, " "))
	if x.Consideration.Sign() > 0 {
		d.Consideration = x.Consideration
	}
}

// cbricsApplySettle applies an order for settlement to its deal; false
// when it is of no client of the firm.
func (o *Ops) cbricsApplySettle(ctx context.Context, tenant string, x cbricsConn, idx *cbricsIdx, s CBRICSSettle, now time.Time) bool {
	no := string(s.OrderNumber)
	id := idx.byOrder[no]
	if id == "" {
		// A deal of a client reported elsewhere (FTRAC, NSE RFQ) or not found
		// among the reportings: kept from the settlement.
		ucc, side, code, cp := idx.clients[s.SellParticipant], "Sell", s.SellParticipant, s.BuyParticipant
		if u := idx.clients[s.BuyParticipant]; ucc == "" && u != "" {
			ucc, side, code, cp = u, "Buy", s.BuyParticipant, s.SellParticipant
		}
		if ucc == "" {
			return false
		}
		n, err := o.next(ctx, tenant, "cbricsdeal", SystemActor)
		if err != nil {
			return false
		}
		cl, _ := o.Client(ctx, tenant, ucc)
		d := &CBRICSDeal{ID: fmt.Sprintf("CBD-%05d", n), Source: "CBRICS", UCC: ucc, Name: cl.Name, Side: side, ClientCode: code, Counterparty: cp, CounterpartyName: idx.parties[cp].Name,
			ISIN: s.Symbol, Value: dec(numStr(s.Value)), Price: dec(numStr(s.Price)), YieldType: s.YieldType, Yield: dec(numStr(s.Yield)), OrderNo: no, Quantity: numInt(s.Quantity),
			TradeDate: cbricsDay(s.SettleDate), ReportedAt: now.Format(time.RFC3339), Status: CBDConfirmed}
		d.note(now, SystemActor, "found in CBRICS's settlements (transaction "+no+"), not entered here")
		idx.deals[d.ID], idx.byOrder[no] = d, d.ID
		id = d.ID
	}
	d := idx.deals[id]
	first := d.Settlement == nil || d.Settlement.ID == ""
	before := ""
	if !first {
		before = d.Settlement.Status
	}
	o.cbricsSettleFields(d, s)
	st := d.Settlement
	if !first && st.Status == before {
		return true
	}
	idx.dirty[d.ID] = true
	switch st.Status {
	case "4":
		d.Status = CBDSettled
		st.SettledAt = firstOf(st.SettledAt, now.Format(time.RFC3339))
	case "5", "6", "7", "8", "9":
		d.Status = CBDFailed
	default:
		d.Status = CBDSettling
	}
	d.note(now, SystemActor, fmt.Sprintf("CBRICS settlement %s on %s: %s", st.SettlementNo, fmtDay(st.Date), st.StatusText))
	mk := "money:" + d.ID
	switch {
	case first && st.Status == "0":
		o.raiseWork(ctx, tenant, "cbrics", mk, cbricsMoneyTitle(*d), cbricsMoneyDetail(*d, x), d.UCC, RoleChecker, "cbrics", st.Date+" 23:59")
		// The accounts the settlement pays out to, from the records kept
		// here, when CBRICS has others.
		if d.Pending == "" {
			if ch, err := o.cbricsAcctFor(ctx, tenant, *d, CBRICSAcctChange{}, false); err == nil && (ch.IFSC != "" || ch.BenID != "") {
				ch.By, ch.At, ch.Why = SystemActor, now.Format(time.RFC3339), "CBRICS has other accounts than the records kept here"
				_ = o.cbricsPutAccount(ctx, tenant, SystemActor, d, ch)
			}
		}
	case st.Status == "3" || st.Status == "4":
		o.closeWork(ctx, tenant, SystemActor, "cbrics", mk, "CBRICS: "+st.StatusText)
	case d.Status == CBDFailed:
		o.closeWork(ctx, tenant, SystemActor, "cbrics", mk, "CBRICS: "+st.StatusText)
		o.raiseWork(ctx, tenant, "cbrics", "settlefail:"+d.ID, "CBRICS settlement of "+d.ID+" did not go through", fmt.Sprintf("Settlement %s of %s (%s): %s. %s", st.SettlementNo, d.ID, d.UCC, st.StatusText, firstOf(st.Remarks, "")), d.UCC, RoleChecker, "cbrics", "")
	}
	if st.Status != "0" && d.Pending == "Account" {
		o.closeWork(ctx, tenant, SystemActor, "cbrics", "account:"+d.ID+":"+stamp(d.PendingAt), "the settlement started")
		d.Pending, d.PendingBy, d.PendingAt, d.Account = "", "", "", nil
	}
	return true
}

func cbricsMoneyTitle(d CBRICSDeal) string {
	s := d.Settlement
	if d.Side == "Buy" {
		return fmt.Sprintf("%s pays in ₹%s for CBRICS settlement %s (%s)", d.UCC, s.BuyerPayin.Fixed(2), s.SettlementNo, d.ID)
	}
	return fmt.Sprintf("%s delivers %d %s for CBRICS settlement %s (%s)", d.UCC, s.Quantity, d.ISIN, s.SettlementNo, d.ID)
}

func cbricsMoneyDetail(d CBRICSDeal, x cbricsConn) string {
	s := d.Settlement
	terms := fmt.Sprintf("Consideration ₹%s (accrued interest ₹%s), stamp duty ₹%s borne by %s.", s.Consideration.Fixed(2), s.AccrInt.Fixed(2), s.StampDuty.Fixed(2), firstOf(s.StampBearer, "—"))
	books := " Ledgerline posts nothing to the client's ledger: the money moves between the client's own account and NSE Clearing."
	if d.Side == "Buy" {
		link := ""
		if s.PayinRef != "" && x.cfg.Base != "" {
			link = " through CBRICS's payment page (" + x.cfg.Base + "/rest/pg?fundPayinRefId=" + s.PayinRef + ")"
		}
		return fmt.Sprintf("%s buys %d %s from %s. The client pays NSE Clearing ₹%s by %s from their own bank account%s. %s The bonds go to demat account %s.%s",
			d.UCC, s.Quantity, d.ISIN, d.Counterparty, s.BuyerPayin.Fixed(2), fmtDay(s.Date), link, terms, strings.TrimSpace(s.DPID+" "+s.BenID), books)
	}
	return fmt.Sprintf("%s sells %d %s to %s. The client delivers the bonds for NSE Clearing's securities pay-in by %s; NSE Clearing pays ₹%s to %s %s. %s%s",
		d.UCC, s.Quantity, d.ISIN, d.Counterparty, fmtDay(s.Date), s.SellerPayout.Fixed(2), s.IFSC, MaskAccount(s.Account), terms, books)
}

// ---- views and helpers for the screen ----

// CBRICSView is the screen's feed.
type CBRICSView struct {
	Set      bool              `json:"set"`
	Live     bool              `json:"live"`
	Auto     bool              `json:"auto"`
	Minutes  int               `json:"minutes"`
	Callback string            `json:"callback"`
	KeySet   bool              `json:"callback_key_set"`
	Problem  string            `json:"problem,omitempty"`
	State    CBRICSState       `json:"state"`
	Deals    []CBRICSDeal      `json:"deals"`
	Parties  []CBRICSParty     `json:"parties"`
	Counts   map[string]int    `json:"counts"`
	Late     int               `json:"late"`
	States   map[string]string `json:"state_codes"`
}

// CBRICSStatus is the screen: deals newest first, participants and the
// connection's state. Clients' account numbers are masked.
func (o *Ops) CBRICSStatus(ctx context.Context, tenant, actor string) (CBRICSView, error) {
	v := CBRICSView{Deals: []CBRICSDeal{}, Parties: []CBRICSParty{}, Counts: map[string]int{}, States: cbricsStates, Minutes: cbricsMinutes,
		Callback: "/hooks/" + tenant + "/nse-cbrics"}
	if err := o.require(ctx, tenant, actor, "Reading NSE CBRICS", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return v, err
	}
	if cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connCBRICS); err == nil && cn.Status != "Not set" {
		x, err := parseCBRICSConn(cn)
		v.Set, v.Live, v.Auto, v.Minutes, v.KeySet = true, x.live, x.auto, x.minutes, x.cbKey != ""
		if err != nil {
			v.Problem = err.Error()
		}
	}
	v.State, _, _ = docs.GetOr(ctx, o.Docs, tenant, cCBRICSState, "state", CBRICSState{})
	ds, err := docs.List[CBRICSDeal](ctx, o.Docs, tenant, cCBRICSDeal, "")
	if err != nil {
		return v, err
	}
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].ID > ds[j].ID })
	cut := o.now().AddDate(0, 0, -30).Format("2006-01-02")
	for _, d := range ds {
		v.Counts[d.Status]++
		if d.Late && d.TradeDate >= cut {
			v.Late++
		}
		if d.Settlement != nil {
			s := *d.Settlement
			s.Account = MaskAccount(s.Account)
			d.Settlement = &s
		}
		if d.Account != nil {
			a := *d.Account
			a.Account = MaskAccount(a.Account)
			d.Account = &a
		}
		v.Deals = append(v.Deals, d)
	}
	ps, _ := docs.List[CBRICSParty](ctx, o.Docs, tenant, cCBRICSParty, "")
	for _, p := range ps {
		if p.UCC != "" {
			for i := range p.Banks {
				p.Banks[i].Account = MaskAccount(p.Banks[i].Account)
			}
			if p.Pending != nil && p.Pending.Bank != nil {
				b := *p.Pending.Bank
				b.Account = MaskAccount(b.Account)
				q := *p.Pending
				q.Bank = &b
				p.Pending = &q
			}
		}
		v.Parties = append(v.Parties, p)
	}
	return v, nil
}

// CBRICSProposal fills the deal form from what Ledgerline holds: the
// client's participant code, its free holdings, and (when CBRICS has traded
// the ISIN today) the day's average price and yield and the maturity date.
type CBRICSProposal struct {
	UCC        string            `json:"ucc"`
	Name       string            `json:"name"`
	ClientCode string            `json:"client_code,omitempty"`
	CodeState  string            `json:"code_state,omitempty"`
	Holdings   []Holding         `json:"holdings"`
	Held       money.Dec         `json:"held"`
	Issue      *CBRICSIssue      `json:"issue,omitempty"`
	Price      string            `json:"price,omitempty"`
	Yield      string            `json:"yield,omitempty"`
	Maturity   string            `json:"maturity,omitempty"`
	TradeDate  string            `json:"trade_date"`
	TradeTime  string            `json:"trade_time"`
	Banks      []CBRICSPartyBank `json:"banks"`
	DPs        []CBRICSPartyDP   `json:"dps"`
	Note       string            `json:"note,omitempty"`
}

// CBRICSPropose is the proposal for a client (and ISIN).
func (o *Ops) CBRICSPropose(ctx context.Context, tenant, actor, ucc, isin string) (CBRICSProposal, error) {
	if err := o.require(ctx, tenant, actor, "Entering corporate bond deals", RoleMaker, RoleChecker); err != nil {
		return CBRICSProposal{}, err
	}
	now := o.now()
	p := CBRICSProposal{TradeDate: now.Format("2006-01-02"), TradeTime: now.Format("15:04"), Holdings: []Holding{}, Banks: []CBRICSPartyBank{}, DPs: []CBRICSPartyDP{}}
	isin = strings.ToUpper(strings.TrimSpace(isin))
	if ucc = strings.ToUpper(strings.TrimSpace(ucc)); ucc != "" {
		c, err := o.Client(ctx, tenant, ucc)
		if err != nil {
			return p, invalid("no client %s", ucc)
		}
		p.UCC, p.Name = c.UCC, c.Name
		cp := o.cbricsPartyOf(ctx, tenant, c.UCC)
		p.ClientCode, p.CodeState = cp.Code, cp.State
		if hs, err := o.clientHoldings(ctx, tenant, c.UCC); err == nil {
			for _, h := range sumHoldings(hs) {
				if strings.HasPrefix(h.ISIN, "IN") && h.Free().Sign() > 0 {
					p.Holdings = append(p.Holdings, h)
					if h.ISIN == isin {
						p.Held = h.Free()
					}
				}
			}
		}
		bs, ds := cbricsClientAccounts(c)
		for _, b := range bs {
			b.Account = MaskAccount(b.Account)
			p.Banks = append(p.Banks, b)
		}
		p.DPs = append(p.DPs, ds...)
	}
	if isin != "" {
		if c, err := o.cbricsCall(ctx, tenant); err == nil {
			if is, err := c.activeIssues(isin); err == nil {
				for i := range is {
					if is[i].Symbol == isin {
						p.Issue = &is[i]
						p.Price, p.Yield, p.Maturity = numStr(is[i].AvgTradePrice), numStr(is[i].AvgYield), cbricsDay(is[i].MaturityDate)
					}
				}
				if p.Issue == nil {
					p.Note = "CBRICS shows no trade in " + isin + " today"
				}
			} else {
				p.Note = "CBRICS's market watch: " + err.Error()
			}
		}
	}
	return p, nil
}

// ---- NSE's notifications ----

// CBRICSNotify takes NSE's notification (POST <base>/v1/notification). Its
// "token" header is the hex HmacSHA256 of the body under the key NSE gave
// the firm. The notification is kept as received and the next tick reads
// CBRICS again.
func (o *Ops) CBRICSNotify(ctx context.Context, tenant, token, remote string, body []byte) (int, error) {
	x, err := o.cbricsConnection(ctx, tenant)
	if err != nil || x.cbKey == "" {
		return 0, fmt.Errorf("%w: the NSE CBRICS callback is not set up (no callback key)", ErrForbidden)
	}
	m := hmac.New(sha256.New, []byte(x.cbKey))
	m.Write(body)
	want := hex.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(token))), []byte(want)) {
		return 0, fmt.Errorf("%w: the notification's token does not match the callback key", ErrForbidden)
	}
	if len(x.ips) > 0 {
		host, _, err := net.SplitHostPort(remote)
		if err != nil {
			host = remote
		}
		if !contains(x.ips, host) {
			return 0, fmt.Errorf("%w: notifications are taken only from the listed addresses", ErrForbidden)
		}
	}
	var n map[string][]json.RawMessage
	if err := json.Unmarshal(body, &n); err != nil {
		return 0, invalid("the notification is not the protocol's JSON")
	}
	sum := sha256.Sum256(body)
	now := o.now()
	if _, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "NSE-CBRICS-NOTIFICATION", Name: "cbrics-notification-" + now.Format("20060102T150405") + ".json", SHA256: hex.EncodeToString(sum[:]), Content: body}); err != nil {
		return 0, err
	}
	count := 0
	var kinds []string
	for _, k := range []string{"orderList", "settleOrderList", "unregList", "bankList", "dpList", "payTxnList"} {
		if len(n[k]) > 0 {
			count += len(n[k])
			kinds = append(kinds, k)
		}
	}
	o.cbricsStateDo(ctx, tenant, func(s *CBRICSState) {
		s.Notices++
		s.LastNotice, s.Kinds = now.Format(time.RFC3339), kinds
		s.Refresh = s.Refresh || count > 0
	})
	o.log(ctx, tenant, SystemActor, "cbrics.notified", strings.Join(kinds, ","), map[string]any{"records": count})
	return count, nil
}

// cbricsTick checks reporting time limits every tick, and reads CBRICS
// every 10 minutes from 08:00 to 20:00 (at once after a notification) when
// switched on.
func (o *Ops) cbricsTick(ctx context.Context, tenant string, now time.Time) {
	cn, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connCBRICS)
	if err != nil || cn.Status == "Not set" {
		return
	}
	if ds, err := docs.List[CBRICSDeal](ctx, o.Docs, tenant, cCBRICSDeal, ""); err == nil {
		for i := range ds {
			if ds[i].unreported() && !ds[i].Late {
				o.cbricsLate(ctx, tenant, &ds[i], now, true)
			}
		}
	}
	x, err := parseCBRICSConn(cn)
	if err != nil || !x.auto {
		return
	}
	if hm := now.In(ist).Format("15:04"); hm < "08:00" || hm > "20:00" {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cCBRICSState, "state", CBRICSState{})
	if t, err := time.Parse(time.RFC3339, st.LastRun); err == nil && now.Sub(t) < 10*time.Minute && !st.Refresh {
		return
	}
	_, _ = o.CBRICSSync(ctx, tenant, SystemActor)
}
