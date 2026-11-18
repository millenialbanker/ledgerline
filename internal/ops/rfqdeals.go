package ops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- RFQ deals: reading, confirming for clients, amendments, settlement ----
//
// A deal is read from NSE (negotiation/all) once it is Traded. The firm's
// side of it (initiator or responder, or both) carries the client code on
// the platform; Ledgerline matches it to a client by the RFQ codes it has
// sent before, or by the UCC. Confirming is maker-checker: the maker says
// which client(s) and, when proposing, the price and accrued interest; a
// different checker sends it. A confirmed deal (status C) waits to be
// settled: Ledgerline does not post debt deals to the ledger (settlement is
// through NSE Clearing's DVP, with accrued interest, and stamp duty and
// brokerage on debt are the firm's own decisions), so a work item asks the
// desk to settle and bill it, with the consideration, accrued interest and
// settlement date, and the deal is marked settled when done.

const (
	cRFQDeal   = "rfqdeal"   // negotiation thread id -> RFQDeal
	cRFQAmend  = "rfqamend"  // amendment id -> RFQAmend
	cRFQState  = "rfqstate"  // "main" -> RFQState; "isin:<ISIN>" -> RFQIssue
	cRFQClient = "rfqclient" // UCC -> RFQClientCode
)

// RFQCode is an entry of a code list (Appendix A).
type RFQCode struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// Appendix A: modification and cancellation reasons.
var (
	RFQModReasons  = []RFQCode{{"SETTYP", "Settlement type"}, {"ACCRINT", "Accrued interest and consideration"}, {"YLDTYP", "Yield type"}}
	RFQCancReasons = []RFQCode{{"FATFNGR", "Fat finger error"}, {"CSHFLO", "Cash flow mismatch"}, {"SHRT", "Short sell"}, {"ILLIQ", "Liquidity constraints"}, {"SHTPRD", "Shut period"}}
)

func rfqReasonText(code string) string {
	for _, c := range append(append([]RFQCode{}, RFQModReasons...), RFQCancReasons...) {
		if c.Code == code {
			return c.Text
		}
	}
	return code
}

var (
	rfqNegStatus     = map[string]string{"N": "Negotiating", "R": "Terminated", "A": "Traded", "C": "Consideration confirmed", "E": "Expired"}
	rfqConfirmStatus = map[string]string{"PP": "Proposer checker pending", "PC": "Submitted to counterparty", "PR": "Proposer checker rejected", "CA": "Counterparty checker pending", "CC": "Confirmed", "CR": "Counterparty rejected"}
	rfqAmendStatus   = map[string]string{"PP": "Proposed", "CR": "Rejected by the counter side", "EA": "Approved"}
)

// RFQSide is the firm's side of a deal.
type RFQSide struct {
	Role       string `json:"role"`        // I initiator of the RFQ, R responder
	Side       string `json:"side"`        // B buys, S sells (this side's client)
	DealType   string `json:"deal_type"`   // D direct (the firm's own), B brokered (a client)
	ClientCode string `json:"client_code"` // the client's code on the RFQ platform
	RegType    string `json:"reg_type,omitempty"`
	UCC        string `json:"ucc,omitempty"`
	Name       string `json:"name,omitempty"`
}

// RFQSplit is a trade split of a deal.
type RFQSplit struct {
	Value           money.Dec `json:"value"` // crores
	Qty             int64     `json:"qty"`
	AccruedInterest money.Dec `json:"accrued_interest"`
	Consideration   money.Dec `json:"consideration"`
	ClientCode      string    `json:"client_code,omitempty"` // the actual participant code of the firm's side
	UCC             string    `json:"ucc,omitempty"`
}

// RFQAlloc is a client the maker names for a deal, with its bonds.
type RFQAlloc struct {
	UCC  string `json:"ucc"`
	Code string `json:"code,omitempty"` // the client's code on RFQ (empty: as saved, or the UCC)
	Qty  int64  `json:"qty,omitempty"`
}

// RFQProposal is the maker's confirmation, waiting for a checker.
type RFQProposal struct {
	Action          string     `json:"action"` // propose, accept, reject
	Role            string     `json:"role"`
	Allocs          []RFQAlloc `json:"allocs,omitempty"`
	Price           money.Dec  `json:"price"`
	AccruedInterest money.Dec  `json:"accrued_interest"`
	Consideration   money.Dec  `json:"consideration"`
	CalcMethod      string     `json:"calc_method,omitempty"`
	PutCallDate     string     `json:"put_call_date,omitempty"`
	Remarks         string     `json:"remarks,omitempty"`
	Maker           string     `json:"maker"`
	MadeAt          string     `json:"made_at"`
	Error           string     `json:"error,omitempty"`
	TriedBy         string     `json:"tried_by,omitempty"`
	TriedAt         string     `json:"tried_at,omitempty"`
}

// RFQAmendProposal is the maker's amendment of a confirmed deal, waiting
// for a checker.
type RFQAmendProposal struct {
	Type            string    `json:"type"` // M modification, C cancellation
	YieldType       string    `json:"yield_type,omitempty"`
	AccruedInterest money.Dec `json:"accrued_interest"`
	SettlementType  string    `json:"settlement_type,omitempty"`
	Reason          string    `json:"reason"`
	Remarks         string    `json:"remarks,omitempty"`
	ProposedBySide  string    `json:"proposed_by_side,omitempty"` // B or S
	Maker           string    `json:"maker"`
	MadeAt          string    `json:"made_at"`
	Error           string    `json:"error,omitempty"`
}

// RFQEvent is a line of a deal's history.
type RFQEvent struct {
	At   string `json:"at"`
	By   string `json:"by"`
	What string `json:"what"`
}

// RFQDeal is a deal of the firm's on NSE's RFQ platform.
type RFQDeal struct {
	ID              string            `json:"id"`  // negotiation thread id
	RFQ             string            `json:"rfq"` // RFQ number
	Date            string            `json:"date"`
	TradeNo         string            `json:"trade_no,omitempty"`
	ISIN            string            `json:"isin"`
	Description     string            `json:"description,omitempty"`
	Category        string            `json:"category,omitempty"` // CB, CP, CD, SD, GS
	FaceValue       money.Dec         `json:"face_value"`
	Status          string            `json:"status"`            // N R A C E
	Confirm         string            `json:"confirm,omitempty"` // PP PC PR CA CC CR
	Sides           []RFQSide         `json:"sides"`
	Counterparty    string            `json:"counterparty,omitempty"`
	CounterClient   string            `json:"counter_client,omitempty"`
	Value           money.Dec         `json:"value"` // crores
	Qty             int64             `json:"qty"`
	YieldType       string            `json:"yield_type,omitempty"`
	Yield           money.Dec         `json:"yield"`
	Price           money.Dec         `json:"price"`
	CalcMethod      string            `json:"calc_method,omitempty"`
	PutCallDate     string            `json:"put_call_date,omitempty"`
	AccruedInterest money.Dec         `json:"accrued_interest"`
	Consideration   money.Dec         `json:"consideration"`
	SettlementType  string            `json:"settlement_type,omitempty"` // 0 T+0, 1 T+1
	SettlementDate  string            `json:"settlement_date,omitempty"`
	ProposedBy      string            `json:"proposed_by,omitempty"` // I or R, as NSE gives it
	ProposedByUs    bool              `json:"proposed_by_us,omitempty"`
	ProposedTime    string            `json:"proposed_time,omitempty"`
	ConfirmedTime   string            `json:"confirmed_time,omitempty"`
	LastActivity    string            `json:"last_activity,omitempty"`
	Splits          []RFQSplit        `json:"splits,omitempty"`
	Proposal        *RFQProposal      `json:"proposal,omitempty"`
	Amend           *RFQAmendProposal `json:"amend,omitempty"`
	AmendedBy       string            `json:"amended_by,omitempty"` // an approved modification's id
	Settle          string            `json:"settle,omitempty"`     // To settle, Settled, Cancelled
	SettleNote      string            `json:"settle_note,omitempty"`
	Updated         string            `json:"updated"`
	History         []RFQEvent        `json:"history"`
}

func (d *RFQDeal) event(now time.Time, by, f string, a ...any) {
	d.History = append(d.History, RFQEvent{At: now.Format(time.RFC3339), By: by, What: fmt.Sprintf(f, a...)})
}

// side is the firm's side with the role (the only one when role is "").
func (d RFQDeal) side(role string) (RFQSide, bool) {
	for _, s := range d.Sides {
		if role == "" && len(d.Sides) == 1 || s.Role == role {
			return s, true
		}
	}
	return RFQSide{}, false
}

func (d RFQDeal) hasRole(r string) bool { _, ok := d.side(r); return ok && r != "" }

func otherRole(r string) string { return map[string]string{"I": "R", "R": "I"}[r] }

// proposer is the side that proposed the consideration. NSE's
// proposedBySide is optional: without it, a proposal is the firm's when
// Ledgerline sent it, else the counterparty's.
func (d RFQDeal) proposer() string {
	if d.ProposedBy != "" || len(d.Sides) != 1 {
		return d.ProposedBy
	}
	if d.ProposedByUs {
		return d.Sides[0].Role
	}
	return otherRole(d.Sides[0].Role)
}

// action is what the firm does next on a deal: propose (the consideration
// and client), decide (the counterparty's proposal), or nothing (""), and
// in words.
func (d RFQDeal) action() (string, string) {
	switch {
	case d.Settle == "Cancelled":
		return "", "Cancelled by an approved amendment"
	case d.Status == "C" && d.Settle == "Settled":
		return "", "Settled"
	case d.Status == "C":
		return "settle", "Confirmed: settle it and bill it"
	case d.Status != "A":
		return "", firstOf(rfqNegStatus[d.Status], d.Status)
	case d.Proposal != nil && d.Proposal.Error != "":
		return "", "NSE refused the last send: send again or discard"
	case d.Proposal != nil:
		return "", "Waiting for a checker to send it"
	}
	mine, counterMine := d.hasRole(d.proposer()), d.hasRole(otherRole(d.proposer()))
	switch d.Confirm {
	case "PC":
		if counterMine {
			return "decide", "The counterparty proposed the consideration: accept or reject"
		}
		return "", "Proposed: waiting for the counterparty"
	case "CA":
		if mine && !counterMine {
			return "", "Waiting for the counterparty's checker"
		}
		return "", "Waiting for the firm's checker on NSE's RFQ screen"
	case "PP":
		if mine {
			return "", "Waiting for the firm's checker on NSE's RFQ screen"
		}
		return "", "Waiting for the counterparty's checker"
	case "CC":
		return "", "Confirmed"
	}
	return "propose", "Confirm the client and propose the consideration"
}

// RFQAmend is an amendment of a confirmed deal.
type RFQAmend struct {
	ID                 string    `json:"id"`
	RFQ                string    `json:"rfq"`
	Deal               string    `json:"deal"` // negotiation thread id
	Type               string    `json:"type"` // M modification, C cancellation
	Date               string    `json:"date"`
	TradeNo            string    `json:"trade_no"`
	ISIN               string    `json:"isin"`
	Description        string    `json:"description,omitempty"`
	BuyerAe            string    `json:"buyer_ae"`
	BuyerClient        string    `json:"buyer_client"`
	SellerAe           string    `json:"seller_ae"`
	SellerClient       string    `json:"seller_client"`
	Value              money.Dec `json:"value"`
	Qty                int64     `json:"qty"`
	YieldType          string    `json:"yield_type"`
	Yield              money.Dec `json:"yield"`
	Price              money.Dec `json:"price"`
	AccruedInterest    money.Dec `json:"accrued_interest"`
	Consideration      money.Dec `json:"consideration"`
	SettlementType     string    `json:"settlement_type"`
	SettlementDate     string    `json:"settlement_date"`
	ModYieldType       string    `json:"mod_yield_type,omitempty"`
	ModAccruedInterest money.Dec `json:"mod_accrued_interest"`
	ModConsideration   money.Dec `json:"mod_consideration"`
	ModSettlementType  string    `json:"mod_settlement_type,omitempty"`
	ModSettlementDate  string    `json:"mod_settlement_date,omitempty"`
	Reason             string    `json:"reason"`
	ReasonText         string    `json:"reason_text"`
	ProposerAe         string    `json:"proposer_ae"`
	ProposerTime       string    `json:"proposer_time,omitempty"`
	ProposerRemarks    string    `json:"proposer_remarks,omitempty"`
	CounterAe          string    `json:"counter_ae"`
	CounterTime        string    `json:"counter_time,omitempty"`
	CounterRemarks     string    `json:"counter_remarks,omitempty"`
	Status             string    `json:"status"` // PP proposed, CR rejected, EA approved
	StatusText         string    `json:"status_text"`
	Ours               string    `json:"ours"` // decide (the counterparty's, for the firm to decide), proposed (the firm's)
	DecidedBy          string    `json:"decided_by,omitempty"`
	DecidedAt          string    `json:"decided_at,omitempty"`
	Applied            bool      `json:"applied,omitempty"`
	Updated            string    `json:"updated"`
}

// RFQState is the connection's last activity.
type RFQState struct {
	LastRead     string      `json:"last_read,omitempty"`
	LastError    string      `json:"last_error,omitempty"`
	LastCallback string      `json:"last_callback,omitempty"`
	Callbacks    int         `json:"callbacks"`
	Pending      bool        `json:"pending,omitempty"` // a notification asked for a read
	TimingsDate  string      `json:"timings_date,omitempty"`
	Open         string      `json:"open,omitempty"` // the day's reading window, HH:MM
	Close        string      `json:"close,omitempty"`
	Timings      []RFQTiming `json:"timings,omitempty"`
	Checked      bool        `json:"checked,omitempty"` // a test signed in
	Broker       bool        `json:"broker,omitempty"`
	BrokerType   string      `json:"broker_type,omitempty"`
}

// RFQTiming is an issue type's windows of the day.
type RFQTiming struct {
	Type       string `json:"type"`
	StartT0    string `json:"start_t0"`
	StartT1    string `json:"start_t1"`
	YieldCutT0 string `json:"yield_cut_t0"`
	YieldCutT1 string `json:"yield_cut_t1"`
	PriceCutT0 string `json:"price_cut_t0"`
	PriceCutT1 string `json:"price_cut_t1"`
}

// RFQIssue is an issue's details from NSE (isins/all).
type RFQIssue struct {
	ISIN, Description, Issuer, Maturity, Category string
	FaceValue, Coupon                             money.Dec
}

// RFQClientCode is a client's code on the RFQ platform, kept when a
// confirmation with it is sent.
type RFQClientCode struct {
	UCC  string `json:"ucc"`
	Code string `json:"code"`
	By   string `json:"by"`
	At   string `json:"at"`
}

func (o *Ops) rfqState(ctx context.Context, tenant string, fn func(*RFQState)) RFQState {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cRFQState, "main", RFQState{})
	if fn != nil {
		fn(&st)
		_ = docs.Upsert(ctx, o.Docs, tenant, cRFQState, "main", st, SystemActor)
	}
	return st
}

// rfqCodes are the saved client codes: by code (upper case) the UCC, and
// by UCC the code.
func (o *Ops) rfqCodes(ctx context.Context, tenant string) (map[string]string, map[string]string) {
	ls, _ := docs.List[RFQClientCode](ctx, o.Docs, tenant, cRFQClient, "")
	byCode, byUCC := map[string]string{}, map[string]string{}
	for _, l := range ls {
		byCode[strings.ToUpper(l.Code)], byUCC[l.UCC] = l.UCC, l.Code
	}
	return byCode, byUCC
}

// rfqApply takes a thread's latest state into the deal; false when the
// firm is on neither side.
func rfqApply(d *RFQDeal, n rfqNeg, member string, byCode map[string]string, cm map[string]Client) bool {
	opp := map[string]string{"B": "S", "S": "B"}
	mk := func(role, side, ae, dt, code, reg string) RFQSide {
		dt, code = strings.ToUpper(strings.TrimSpace(dt)), strings.TrimSpace(code)
		if dt == "" {
			dt = "B"
			if code == "" || strings.EqualFold(code, ae) {
				dt = "D" // a participant's own deal: the client code is its participant code
			}
		}
		if code == "" && dt == "D" {
			code = ae
		}
		s := RFQSide{Role: role, Side: side, DealType: dt, ClientCode: code, RegType: reg}
		if dt == "B" && !strings.HasPrefix(strings.ToUpper(code), "INST") {
			if u := byCode[strings.ToUpper(code)]; u != "" {
				s.UCC = u
			} else if _, ok := cm[code]; ok {
				s.UCC = code
			}
			if c, ok := cm[s.UCC]; ok {
				s.Name = c.Name
			}
		}
		return s
	}
	var sides []RFQSide
	if strings.EqualFold(n.InitAeCode, member) {
		sides = append(sides, mk("I", opp[n.BuySell], n.InitAeCode, n.InitDealType, n.InitClientCode, n.InitClientRegType))
	}
	if strings.EqualFold(n.RespAeCode, member) {
		sides = append(sides, mk("R", n.BuySell, n.RespAeCode, n.RespDealType, n.RespClientCode, n.RespClientRegType))
	}
	if len(sides) == 0 {
		return false
	}
	// A client named before keeps its side when a later reply leaves the
	// client code out.
	for i := range sides {
		if old, ok := d.side(sides[i].Role); ok && sides[i].ClientCode == "" {
			sides[i] = old
		}
	}
	d.Sides = sides
	if len(sides) == 1 {
		if sides[0].Role == "I" {
			d.Counterparty, d.CounterClient = n.RespAeCode, n.RespClientCode
		} else {
			d.Counterparty, d.CounterClient = n.InitAeCode, n.InitClientCode
		}
	} else {
		d.Counterparty, d.CounterClient = member, ""
	}
	d.ID, d.RFQ = firstOf(n.ID, d.ID), firstOf(firstOf(n.RFQNumber, n.NgRFQNumber), d.RFQ)
	d.Date, d.ISIN = firstOf(rfqDate(n.Date), d.Date), firstOf(n.ISIN, d.ISIN)
	d.Status, d.Confirm, d.TradeNo = firstOf(n.Status, d.Status), n.ConfirmStatus, firstOf(n.TradeNumber, d.TradeNo)
	setDec := func(dst *money.Dec, v rfqDec) {
		if v.set {
			*dst = v.v
		}
	}
	setDec(&d.Value, n.AcceptedValue)
	if n.AcceptedQuantity.set {
		d.Qty = n.AcceptedQuantity.int()
	}
	setDec(&d.Yield, n.AcceptedYield)
	setDec(&d.Price, n.AcceptedPrice)
	d.CalcMethod = firstOf(n.AcceptedCalcMethod, d.CalcMethod)
	d.PutCallDate = firstOf(rfqDate(n.AcceptedPutCallDate), d.PutCallDate)
	if d.AmendedBy == "" { // an approved modification decides these
		d.YieldType = firstOf(n.AcceptedYieldType, d.YieldType)
		setDec(&d.AccruedInterest, n.AcceptedAccruedInterest)
		setDec(&d.Consideration, n.AcceptedConsideration)
		d.SettlementType = firstOf(string(n.AcceptedSettlementType), d.SettlementType)
		d.SettlementDate = firstOf(rfqDate(n.AcceptedSettlementDate), d.SettlementDate)
	}
	d.ProposedBy, d.ProposedTime, d.ConfirmedTime = n.ProposedBySide, n.ProposedTime, firstOf(n.ConfirmedPriceQuoteTime, d.ConfirmedTime)
	if d.Confirm == "" || d.Confirm == "PR" || d.Confirm == "CR" {
		d.ProposedByUs = false // no proposal stands
	}
	d.LastActivity = firstOf(n.LastActivityTimestamp, d.LastActivity)
	if n.TradeSplits != nil {
		d.Splits = nil
		for _, s := range n.TradeSplits {
			code := firstOf(s.PropActPartCode, s.CouActPartCode)
			sp := RFQSplit{Value: s.Val.v, Qty: s.Qty.int(), AccruedInterest: s.AccInt.v, Consideration: s.Cons.v, ClientCode: code}
			if u := byCode[strings.ToUpper(code)]; u != "" {
				sp.UCC = u
			} else if _, ok := cm[code]; ok {
				sp.UCC = code
			}
			d.Splits = append(d.Splits, sp)
		}
	}
	return true
}

// ---- reading ----

// RFQSyncResult says what a read did.
type RFQSyncResult struct {
	Threads    int      `json:"threads"`
	Deals      int      `json:"deals"`
	New        int      `json:"new"`
	Confirmed  int      `json:"confirmed"`
	Amendments int      `json:"amendments"`
	Problems   []string `json:"problems,omitempty"`
}

// RFQSync reads the firm's deals of a date (default today) and the
// amendments of the week to it from NSE, and keeps them.
func (o *Ops) RFQSync(ctx context.Context, tenant, actor, date string) (RFQSyncResult, error) {
	var res RFQSyncResult
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Reading RFQ deals from NSE", RoleMaker, RoleChecker); err != nil {
			return res, err
		}
	}
	c, ok := o.rfqConnection(ctx, tenant)
	if !ok || !c.usable {
		return res, fmt.Errorf("%w: set up NSE RFQ (debt) in Settings → Connections", ErrState)
	}
	now := o.now()
	if date == "" {
		date = now.Format("2006-01-02")
	}
	if !validDate(date) {
		return res, invalid("give the date as yyyy-mm-dd")
	}
	var ns []rfqNeg
	if err := rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/negotiation/all", map[string]any{"date": rfqOutDate(date)}, &ns); err != nil {
		o.rfqState(ctx, tenant, func(s *RFQState) { s.LastRead, s.LastError = now.Format(time.RFC3339), err.Error() })
		o.rfqSeen(ctx, tenant, err)
		return res, err
	}
	res.Threads = len(ns)
	o.rfqTakeNegs(ctx, tenant, actor, c, ns, &res)
	// Amendments are made on confirmed deals, possibly days after: the
	// last week's are read (dealamend/all needs a date range).
	t, _ := time.Parse("2006-01-02", date)
	var as []rfqAmendWire
	if err := rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/dealamend/all", map[string]any{"filtFromDate": rfqOutDate(t.AddDate(0, 0, -7).Format("2006-01-02")), "filtToDate": rfqOutDate(date)}, &as); err != nil {
		res.Problems = append(res.Problems, "amendments: "+err.Error())
	} else {
		res.Amendments = o.rfqTakeAmends(ctx, tenant, c, as)
	}
	o.rfqIssues(ctx, tenant, c)
	o.rfqState(ctx, tenant, func(s *RFQState) {
		s.LastRead, s.Pending, s.LastError = now.Format(time.RFC3339), false, ""
		if len(res.Problems) > 0 {
			s.LastError = res.Problems[0]
		}
	})
	o.log(ctx, tenant, actor, "rfq.read", date, map[string]any{"threads": res.Threads, "deals": res.Deals, "new": res.New, "confirmed": res.Confirmed, "amendments": res.Amendments})
	return res, nil
}

// rfqTakeNegs keeps the threads that are deals (and every thread already
// kept), and raises or closes their work.
func (o *Ops) rfqTakeNegs(ctx context.Context, tenant, actor string, c rfqConn, ns []rfqNeg, res *RFQSyncResult) {
	byCode, _ := o.rfqCodes(ctx, tenant)
	cm, _ := o.ClientMap(ctx, tenant)
	now := o.now()
	for _, n := range ns {
		if n.ID == "" {
			continue
		}
		old, _, err := docs.Get[RFQDeal](ctx, o.Docs, tenant, cRFQDeal, n.ID)
		isNew := err != nil
		if isNew && n.Status != "A" && n.Status != "C" {
			continue // a negotiation that has not traded is the dealer's business
		}
		d := old
		if !rfqApply(&d, n, c.cfg.Member, byCode, cm) {
			continue
		}
		res.Deals++
		switch {
		case isNew:
			res.New++
			d.event(now, actor, "read from NSE: %s", firstOf(rfqNegStatus[d.Status], d.Status))
		case d.Status != old.Status || d.Confirm != old.Confirm:
			d.event(now, actor, "NSE: %s%s", firstOf(rfqNegStatus[d.Status], d.Status), map[bool]string{true: " · " + firstOf(rfqConfirmStatus[d.Confirm], d.Confirm), false: ""}[d.Confirm != ""])
		}
		if d.Status == "C" && old.Status != "C" {
			res.Confirmed++
		}
		if d.Status == "C" && d.Settle == "" {
			d.Settle = "To settle"
		}
		d.Updated = now.Format(time.RFC3339)
		if err := docs.Upsert(ctx, o.Docs, tenant, cRFQDeal, d.ID, d, actor); err != nil {
			res.Problems = append(res.Problems, d.ID+": "+err.Error())
			continue
		}
		o.rfqWork(ctx, tenant, actor, d)
	}
}

// rfqWork raises or closes a deal's work items: to confirm it, to send a
// waiting confirmation, and to settle it.
func (o *Ops) rfqWork(ctx context.Context, tenant, actor string, d RFQDeal) {
	act, words := d.action()
	label := firstOf(d.TradeNo, d.ID) + " · " + d.ISIN
	// One item a confirmation state, so a deal rejected and to be proposed
	// again asks again.
	key := "confirm|" + d.ID + "|" + firstOf(d.Confirm, "new")
	for _, k := range []string{"new", "PC", "PR", "CR", "CA", "PP"} {
		if k != firstOf(d.Confirm, "new") || d.Proposal != nil || (act != "propose" && act != "decide") {
			o.closeWork(ctx, tenant, actor, "rfq", "confirm|"+d.ID+"|"+k, words)
		}
	}
	if (act == "propose" || act == "decide") && d.Proposal == nil {
		o.raiseWork(ctx, tenant, "rfq", key, "Confirm RFQ deal "+label, fmt.Sprintf("%s. %s %s bonds (₹%s crore) with %s, settling %s. Name the client on Bonds (RFQ deals): a checker then sends it to NSE before the consideration cut-off.",
			words, rfqSideWords(d), money.DecInt(d.Qty).String(), d.Value.String(), firstOf(d.Counterparty, "the counterparty"), fmtRFQDate(firstOf(d.SettlementDate, d.Date))), rfqDealUCC(d), RoleMaker, "rfq", d.Date)
	}
	if d.Status == "C" && d.Settle == "To settle" {
		o.raiseWork(ctx, tenant, "rfq", "settle|"+d.ID, "Settle RFQ deal "+label+" · "+fmtRFQDate(d.SettlementDate),
			fmt.Sprintf("%s %s bonds of %s%s: consideration ₹%s (accrued interest ₹%s), settling %s through NSE Clearing. Ledgerline does not post debt deals to the ledger: arrange the client's funds or securities for the pay-in, record the obligation, brokerage and stamp duty by hand, then mark the deal settled on Bonds (RFQ deals).",
				rfqSideWords(d), money.DecInt(d.Qty).String(), d.ISIN, map[bool]string{true: " (" + d.Description + ")", false: ""}[d.Description != ""], formatINR(d.Consideration), formatINR(d.AccruedInterest), fmtRFQDate(d.SettlementDate)),
			rfqDealUCC(d), RoleMaker, "rfq", firstOf(d.SettlementDate, d.Date))
	} else if d.Settle == "Settled" || d.Settle == "Cancelled" {
		o.closeWork(ctx, tenant, actor, "rfq", "settle|"+d.ID, strings.ToLower(d.Settle))
	}
}

func fmtRFQDate(d string) string {
	if t, err := time.Parse("2006-01-02", d); err == nil {
		return t.Format("02 Jan 2006")
	}
	return d
}

// rfqSideWords is who buys or sells, in words.
func rfqSideWords(d RFQDeal) string {
	var ws []string
	for _, s := range d.Sides {
		who := firstOf(s.UCC, s.ClientCode)
		if s.DealType == "D" {
			who = "The firm (own account)"
		} else if s.UCC == "" {
			who = "Client code " + firstOf(s.ClientCode, "not named")
		}
		ws = append(ws, who+map[string]string{"B": " buys", "S": " sells"}[s.Side])
	}
	return strings.Join(ws, "; ")
}

func rfqDealUCC(d RFQDeal) string {
	if len(d.Sides) == 1 {
		return d.Sides[0].UCC
	}
	return ""
}

// rfqTakeAmends keeps amendments, and applies those approved.
func (o *Ops) rfqTakeAmends(ctx context.Context, tenant string, c rfqConn, as []rfqAmendWire) int {
	n := 0
	for _, w := range as {
		if _, ok := o.rfqKeepAmend(ctx, tenant, c, w, ""); ok {
			n++
		}
	}
	return n
}

// rfqKeepAmend stores one amendment as NSE gives it, works out whether the
// firm decides it, raises or closes the checker's work, and applies it to
// the deal once approved.
func (o *Ops) rfqKeepAmend(ctx context.Context, tenant string, c rfqConn, w rfqAmendWire, decidedBy string) (RFQAmend, bool) {
	id := strings.TrimSpace(string(w.ID))
	if id == "" {
		return RFQAmend{}, false
	}
	old, _, _ := docs.Get[RFQAmend](ctx, o.Docs, tenant, cRFQAmend, id)
	a := RFQAmend{ID: id, RFQ: w.RFQNumber, Deal: w.NgID, Type: w.RequestType, Date: rfqDate(w.Date), TradeNo: w.TradeNumber, ISIN: w.ISIN, Description: strings.Join(strings.Fields(w.Description), " "),
		BuyerAe: w.BuyerAeCode, BuyerClient: w.BuyerClientCode, SellerAe: w.SellerAeCode, SellerClient: w.SellerClientCode,
		Value: w.Value.v, Qty: w.Quantity.int(), YieldType: w.YieldType, Yield: w.Yield.v, Price: w.Price.v, AccruedInterest: w.AccruedInterest.v, Consideration: w.Consideration.v,
		SettlementType: string(w.SettlementType), SettlementDate: rfqDate(w.SettlementDate), ModYieldType: w.ModYieldType, ModAccruedInterest: w.ModAccruedInterest.v, ModConsideration: w.ModConsideration.v,
		ModSettlementType: string(w.ModSettlementType), ModSettlementDate: rfqDate(w.ModSettlementDate), Reason: firstOf(w.ModReason, w.CancReason),
		ProposerAe: w.ProposerAeCode, ProposerTime: w.ProposerTime, ProposerRemarks: w.ProposerRemarks, CounterAe: w.CounterAeCode, CounterTime: w.CounterTime, CounterRemarks: w.CounterRemarks,
		Status: w.ApprovalStatus, DecidedBy: firstOf(decidedBy, old.DecidedBy), DecidedAt: old.DecidedAt, Applied: old.Applied, Updated: o.now().Format(time.RFC3339)}
	if decidedBy != "" {
		a.DecidedAt = a.Updated
	}
	if a.Type == "C" {
		a.Reason = firstOf(w.CancReason, w.ModReason)
	}
	a.ReasonText, a.StatusText = rfqReasonText(a.Reason), firstOf(rfqAmendStatus[a.Status], a.Status)
	// The firm decides when it (or its client on the deal) is the counter
	// side; it proposed when it is the proposer.
	deal, _, derr := docs.Get[RFQDeal](ctx, o.Docs, tenant, cRFQDeal, a.Deal)
	ours := func(code string) bool {
		if strings.EqualFold(code, c.cfg.Member) {
			return true
		}
		for _, s := range deal.Sides {
			if derr == nil && s.ClientCode != "" && strings.EqualFold(code, s.ClientCode) {
				return true
			}
		}
		return false
	}
	switch {
	case ours(a.CounterAe) && !ours(a.ProposerAe):
		a.Ours = "decide"
	case ours(a.ProposerAe):
		a.Ours = "proposed"
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cRFQAmend, a.ID, a, SystemActor); err != nil {
		return a, false
	}
	what := map[string]string{"M": "modification", "C": "cancellation"}[a.Type]
	if a.Ours == "decide" && a.Status == "PP" {
		o.raiseWork(ctx, tenant, "rfq", "amend|"+a.ID, fmt.Sprintf("Decide the RFQ deal %s of %s", firstOf(what, "amendment"), firstOf(a.TradeNo, a.Deal)),
			fmt.Sprintf("%s proposes a %s of deal %s (%s): %s%s. Approve or reject it on Bonds (RFQ deals) → Amendments.", a.ProposerAe, firstOf(what, "change"), firstOf(a.TradeNo, a.Deal), a.ISIN, a.ReasonText,
				map[bool]string{true: ": " + a.ProposerRemarks, false: ""}[a.ProposerRemarks != ""]), rfqDealUCC(deal), RoleChecker, "rfq", a.Date)
	} else {
		o.closeWork(ctx, tenant, SystemActor, "rfq", "amend|"+a.ID, firstOf(a.StatusText, "no longer waiting"))
	}
	if a.Status == "EA" && !a.Applied && derr == nil {
		o.rfqApplyAmend(ctx, tenant, a)
	}
	return a, true
}

// rfqApplyAmend applies an approved amendment to its deal: a cancellation
// leaves nothing to settle; a modification gives the new accrued interest,
// consideration and settlement.
func (o *Ops) rfqApplyAmend(ctx context.Context, tenant string, a RFQAmend) {
	now := o.now()
	d, err := docs.Update(ctx, o.Docs, tenant, cRFQDeal, a.Deal, SystemActor, func(d *RFQDeal) error {
		switch a.Type {
		case "C":
			d.Settle, d.SettleNote = "Cancelled", "cancelled by amendment "+a.ID+" ("+a.ReasonText+")"
			d.event(now, SystemActor, "cancelled by approved amendment %s: %s", a.ID, a.ReasonText)
		case "M":
			d.AmendedBy = a.ID
			d.YieldType = firstOf(a.ModYieldType, d.YieldType)
			if !a.ModAccruedInterest.IsZero() || !a.ModConsideration.IsZero() {
				d.AccruedInterest, d.Consideration = a.ModAccruedInterest, a.ModConsideration
			}
			d.SettlementType = firstOf(a.ModSettlementType, d.SettlementType)
			d.SettlementDate = firstOf(a.ModSettlementDate, d.SettlementDate)
			d.event(now, SystemActor, "modified by approved amendment %s (%s): accrued interest ₹%s, consideration ₹%s, settling %s", a.ID, a.ReasonText, formatINR(d.AccruedInterest), formatINR(d.Consideration), d.SettlementDate)
		}
		d.Updated = now.Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cRFQAmend, a.ID, SystemActor, func(x *RFQAmend) error { x.Applied = true; return nil })
	o.rfqWork(ctx, tenant, SystemActor, d)
	o.log(ctx, tenant, SystemActor, "rfq.amendment_applied", a.ID, map[string]any{"deal": a.Deal, "type": a.Type, "reason": a.Reason})
}

// rfqIssues fills in the description and face value of deals' ISINs from
// NSE's issue list, once an ISIN (a few a read).
func (o *Ops) rfqIssues(ctx context.Context, tenant string, c rfqConn) {
	ds, _ := docs.List[RFQDeal](ctx, o.Docs, tenant, cRFQDeal, "")
	asked := 0
	for _, d := range ds {
		if d.Description != "" || d.ISIN == "" {
			continue
		}
		is, _, err := docs.Get[RFQIssue](ctx, o.Docs, tenant, cRFQState, "isin:"+d.ISIN)
		if err != nil {
			if asked >= 10 {
				continue
			}
			asked++
			var ls []rfqISIN
			if rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/isins/all", map[string]any{"symbol": d.ISIN}, &ls) != nil {
				continue
			}
			found := false
			for _, l := range ls {
				if strings.EqualFold(l.Symbol, d.ISIN) {
					is = RFQIssue{ISIN: d.ISIN, Description: l.Description, Issuer: l.Issuer, Maturity: rfqDate(l.MaturityDate), Category: l.IssueCategory, FaceValue: l.FaceValue.v, Coupon: l.CouponRate.v}
					found = true
				}
			}
			if !found {
				continue // matured, or not on the list
			}
			_ = docs.Upsert(ctx, o.Docs, tenant, cRFQState, "isin:"+d.ISIN, is, SystemActor)
		}
		_, _ = docs.Update(ctx, o.Docs, tenant, cRFQDeal, d.ID, SystemActor, func(x *RFQDeal) error {
			x.Description, x.Category, x.FaceValue = is.Description, is.Category, is.FaceValue
			return nil
		})
	}
}

// rfqReadOne reads one thread again (negotiation/all by id and RFQ date).
func rfqReadOne(ctx context.Context, c rfqConn, d RFQDeal) (rfqNeg, error) {
	var ns []rfqNeg
	if err := rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/negotiation/all", map[string]any{"id": d.ID, "rfqNumber": d.RFQ, "date": rfqOutDate(d.Date)}, &ns); err != nil {
		return rfqNeg{}, err
	}
	for _, n := range ns {
		if n.ID == d.ID {
			return n, nil
		}
	}
	return rfqNeg{}, fmt.Errorf("%w: NSE no longer lists deal %s", ErrState, d.ID)
}

// ---- the screen ----

// RFQDealView is a deal with what the firm does next.
type RFQDealView struct {
	RFQDeal
	Action     string `json:"action"`
	Needs      string `json:"needs"`
	StatusText string `json:"status_text"`
	Confirmed  string `json:"confirm_text,omitempty"`
}

// RFQView is the Bonds (RFQ deals) screen.
type RFQView struct {
	Date        string            `json:"date"`
	Set         bool              `json:"set"`
	Usable      bool              `json:"usable"`
	Read        bool              `json:"read"`
	Live        bool              `json:"live"`
	Member      string            `json:"member,omitempty"`
	Every       int               `json:"every"`
	Callback    string            `json:"callback"`
	CallbackKey bool              `json:"callback_key"`
	CallbackOn  bool              `json:"callback_on"`
	State       RFQState          `json:"state"`
	Deals       []RFQDealView     `json:"deals"`
	Amendments  []RFQAmend        `json:"amendments"`
	ModReasons  []RFQCode         `json:"mod_reasons"`
	CancReasons []RFQCode         `json:"canc_reasons"`
	Codes       map[string]string `json:"codes"` // UCC -> the saved RFQ client code
}

func (o *Ops) rfqCanView(ctx context.Context, tenant, actor string) error {
	return o.require(ctx, tenant, actor, "Viewing RFQ deals", RoleMaker, RoleChecker, RoleRisk, RoleCompliance)
}

// RFQDeals is the screen for a date: the deals dealt or settling that day,
// and every deal or amendment still waiting for the firm.
func (o *Ops) RFQDeals(ctx context.Context, tenant, actor, date string) (RFQView, error) {
	if err := o.rfqCanView(ctx, tenant, actor); err != nil {
		return RFQView{}, err
	}
	if date == "" {
		date = o.now().Format("2006-01-02")
	}
	c, ok := o.rfqConnection(ctx, tenant)
	_, byUCC := o.rfqCodes(ctx, tenant)
	v := RFQView{Date: date, Set: ok, Usable: c.usable, Read: ok && c.read, Live: c.live, Member: c.cfg.Member, Every: int(c.every / time.Minute),
		Callback: "/hooks/" + tenant + "/nse-rfq/<callback token>", CallbackKey: c.cbKey != "", CallbackOn: c.cbToken != "",
		State: o.rfqState(ctx, tenant, nil), Deals: []RFQDealView{}, Amendments: []RFQAmend{}, ModReasons: RFQModReasons, CancReasons: RFQCancReasons, Codes: byUCC}
	ds, err := docs.List[RFQDeal](ctx, o.Docs, tenant, cRFQDeal, "")
	if err != nil {
		return v, err
	}
	for _, d := range ds {
		act, words := d.action()
		open := act != "" || d.Status == "A" || d.Proposal != nil || d.Amend != nil
		if d.Date != date && d.SettlementDate != date && !open {
			continue
		}
		v.Deals = append(v.Deals, RFQDealView{RFQDeal: d, Action: act, Needs: words, StatusText: firstOf(rfqNegStatus[d.Status], d.Status), Confirmed: rfqConfirmStatus[d.Confirm]})
	}
	sort.Slice(v.Deals, func(i, j int) bool {
		if v.Deals[i].Date != v.Deals[j].Date {
			return v.Deals[i].Date > v.Deals[j].Date
		}
		return v.Deals[i].ID > v.Deals[j].ID
	})
	as, _ := docs.List[RFQAmend](ctx, o.Docs, tenant, cRFQAmend, "")
	t, _ := time.Parse("2006-01-02", date)
	from := t.AddDate(0, 0, -30).Format("2006-01-02")
	for _, a := range as {
		if a.Status == "PP" || (a.Date >= from && a.Date <= date) {
			v.Amendments = append(v.Amendments, a)
		}
	}
	sort.Slice(v.Amendments, func(i, j int) bool { return v.Amendments[i].Updated > v.Amendments[j].Updated })
	return v, nil
}

// RFQDeal returns one deal.
func (o *Ops) RFQDeal(ctx context.Context, tenant, actor, id string) (RFQDealView, error) {
	if err := o.rfqCanView(ctx, tenant, actor); err != nil {
		return RFQDealView{}, err
	}
	d, _, err := docs.Get[RFQDeal](ctx, o.Docs, tenant, cRFQDeal, id)
	if err != nil {
		return RFQDealView{}, err
	}
	act, words := d.action()
	return RFQDealView{RFQDeal: d, Action: act, Needs: words, StatusText: firstOf(rfqNegStatus[d.Status], d.Status), Confirmed: rfqConfirmStatus[d.Confirm]}, nil
}

// ---- confirming for clients ----

// RFQProposalInput is the maker's confirmation of a deal.
type RFQProposalInput struct {
	Action          string     `json:"action"`         // propose, accept, reject
	Role            string     `json:"role,omitempty"` // I or R, when the firm is on both sides
	Allocs          []RFQAlloc `json:"allocs,omitempty"`
	Price           string     `json:"price,omitempty"`
	AccruedInterest string     `json:"accrued_interest,omitempty"`
	Consideration   string     `json:"consideration,omitempty"`
	CalcMethod      string     `json:"calc_method,omitempty"`
	PutCallDate     string     `json:"put_call_date,omitempty"`
	Remarks         string     `json:"remarks,omitempty"`
}

// rfqClientOK checks a client can be named on a deal.
func rfqClientOK(cm map[string]Client, ucc string) (Client, error) {
	c, ok := cm[ucc]
	if !ok {
		return c, invalid("%s is not a client", ucc)
	}
	if c.Status != "Active" {
		return c, invalid("%s is %s: only an active client can be named on a deal", ucc, strings.ToLower(firstOf(c.Status, "not active")))
	}
	if c.Inactive {
		return c, invalid("%s is flagged inactive with the exchanges: reactivate the account first", ucc)
	}
	return c, nil
}

// rfqConsideration is the deal's consideration at a price: face value
// (value in crores) × price / 100, plus accrued interest, to the paisa.
// Face value is the quantity × the issue's face value when known.
func rfqConsideration(d RFQDeal, price, ai money.Dec) money.Dec {
	fv := d.Value.MulInt(10000000)
	if d.FaceValue.Sign() > 0 && d.Qty > 0 {
		fv = d.FaceValue.MulInt(d.Qty)
	}
	c, _ := fv.Mul(price).Quo(money.DecInt(100))
	return money.MustDec(c.Add(ai).Fixed(2))
}

// RFQPropose records the maker's confirmation of a deal for a checker to
// send: the client(s) and, when the firm proposes, the price and accrued
// interest; or accepting or rejecting the counterparty's proposal.
func (o *Ops) RFQPropose(ctx context.Context, tenant, actor, id string, in RFQProposalInput) (RFQDeal, error) {
	if err := o.require(ctx, tenant, actor, "Proposing RFQ deal confirmations", RoleMaker, RoleChecker); err != nil {
		return RFQDeal{}, err
	}
	d, _, err := docs.Get[RFQDeal](ctx, o.Docs, tenant, cRFQDeal, id)
	if err != nil {
		return d, err
	}
	act, words := d.action()
	in.Action = strings.ToLower(strings.TrimSpace(in.Action))
	switch {
	case d.Proposal != nil:
		return d, fmt.Errorf("%w: a confirmation is already waiting for a checker: discard it first", ErrState)
	case act == "propose" && in.Action != "propose", act == "decide" && in.Action != "accept" && in.Action != "reject", act != "propose" && act != "decide":
		return d, fmt.Errorf("%w: this deal is not waiting for the firm to %s it (%s)", ErrState, firstOf(in.Action, "confirm"), words)
	}
	role := strings.ToUpper(strings.TrimSpace(in.Role))
	if len(d.Sides) > 1 && in.Action == "propose" && role != "I" && role != "R" {
		return d, invalid("the firm is on both sides: say which side this is for (I, the RFQ's initiator; R, the responder)")
	}
	if in.Action != "propose" {
		role = otherRole(d.proposer()) // the counter side
	}
	side, ok := d.side(role)
	if !ok {
		return d, invalid("the firm is not side %s of this deal", role)
	}
	p := RFQProposal{Action: in.Action, Role: side.Role, Remarks: strings.TrimSpace(in.Remarks), Maker: actor, MadeAt: o.now().Format(time.RFC3339)}
	if len(p.Remarks) > 100 {
		return d, invalid("remarks are at most 100 characters")
	}
	if in.Action != "reject" {
		cm, _ := o.ClientMap(ctx, tenant)
		_, byUCC := o.rfqCodes(ctx, tenant)
		seen := map[string]bool{}
		var qty int64
		for _, a := range in.Allocs {
			a.UCC, a.Code = strings.ToUpper(strings.TrimSpace(a.UCC)), strings.TrimSpace(a.Code)
			if a.UCC == "" {
				continue
			}
			if seen[a.UCC] {
				return d, invalid("%s is named twice", a.UCC)
			}
			seen[a.UCC] = true
			if _, err := rfqClientOK(cm, a.UCC); err != nil {
				return d, err
			}
			a.Code = firstOf(firstOf(a.Code, byUCC[a.UCC]), a.UCC)
			if len(a.Code) > 30 {
				return d, invalid("an RFQ client code is at most 30 characters")
			}
			if a.Qty < 0 {
				return d, invalid("give %s's bonds as a whole number", a.UCC)
			}
			qty += a.Qty
			p.Allocs = append(p.Allocs, a)
		}
		switch {
		case len(p.Allocs) == 0 && side.DealType != "D":
			return d, invalid("name the client for this deal")
		case len(p.Allocs) == 1 && p.Allocs[0].Qty == 0:
			p.Allocs[0].Qty = d.Qty
		case len(p.Allocs) > 1 && qty != d.Qty:
			return d, invalid("the clients' bonds add up to %d; the deal is for %d", qty, d.Qty)
		case len(p.Allocs) > 1 && d.Qty <= 0:
			return d, invalid("NSE gave no quantity for this deal: it cannot be split")
		}
		for _, a := range p.Allocs {
			if a.Qty <= 0 {
				return d, invalid("give the bonds for each client")
			}
		}
	}
	if in.Action == "propose" {
		dec := func(s, what string, def money.Dec) (money.Dec, error) {
			if strings.TrimSpace(s) == "" {
				return def, nil
			}
			v, err := money.ParseDec(strings.ReplaceAll(strings.TrimSpace(s), ",", ""))
			if err != nil || v.Sign() < 0 {
				return v, invalid("the %s is not a number", what)
			}
			return v, nil
		}
		if p.Price, err = dec(in.Price, "price", d.Price); err != nil {
			return d, err
		}
		if p.Price.Sign() <= 0 {
			return d, invalid("give the price (per ₹100 of face value)")
		}
		if strings.TrimSpace(in.AccruedInterest) == "" {
			return d, invalid("give the accrued interest (0 for none)")
		}
		if p.AccruedInterest, err = dec(in.AccruedInterest, "accrued interest", money.Dec{}); err != nil {
			return d, err
		}
		if p.AccruedInterest, err = money.ParseDec(p.AccruedInterest.Fixed(2)); err != nil {
			return d, err
		}
		if p.Consideration, err = dec(in.Consideration, "consideration", rfqConsideration(d, p.Price, p.AccruedInterest)); err != nil {
			return d, err
		}
		p.Consideration = money.MustDec(p.Consideration.Fixed(2))
		p.CalcMethod = strings.ToUpper(firstOf(strings.TrimSpace(in.CalcMethod), d.CalcMethod))
		if p.CalcMethod != "M" && p.CalcMethod != "O" {
			return d, invalid("choose the calculation method: M (money market) or O (other)")
		}
		p.PutCallDate = firstOf(strings.TrimSpace(in.PutCallDate), d.PutCallDate)
		if p.PutCallDate != "" && !validDate(p.PutCallDate) {
			return d, invalid("give the put or call date as yyyy-mm-dd")
		}
		if (d.YieldType == "YTP" || d.YieldType == "YTC") && p.PutCallDate == "" {
			return d, invalid("the yield is to a put or call: give its date")
		}
	}
	now := o.now()
	d, err = docs.Update(ctx, o.Docs, tenant, cRFQDeal, id, actor, func(x *RFQDeal) error {
		if x.Proposal != nil {
			return fmt.Errorf("%w: a confirmation is already waiting for a checker", ErrState)
		}
		x.Proposal = &p
		x.event(now, actor, "%s for a checker: %s", map[string]string{"propose": "proposed", "accept": "accepting the counterparty's proposal", "reject": "rejecting the counterparty's proposal"}[p.Action], rfqAllocWords(p))
		x.Updated = now.Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return d, err
	}
	o.rfqWork(ctx, tenant, actor, d) // the maker's item is done
	o.raiseWork(ctx, tenant, "rfq", "send|"+d.ID+"|"+now.Format("150405"), "Send the RFQ confirmation of "+firstOf(d.TradeNo, d.ID),
		fmt.Sprintf("%s has %s deal %s (%s): %s. A different checker sends it to NSE on Bonds (RFQ deals).", actor, map[string]string{"propose": "proposed", "accept": "accepted", "reject": "rejected"}[p.Action], firstOf(d.TradeNo, d.ID), d.ISIN, rfqAllocWords(p)),
		rfqDealUCC(d), RoleChecker, "rfq", d.Date)
	o.log(ctx, tenant, actor, "rfq.confirm_proposed", d.ID, map[string]any{"action": p.Action, "clients": len(p.Allocs)})
	return d, nil
}

func rfqAllocWords(p RFQProposal) string {
	if p.Action == "reject" {
		return "reject" + map[bool]string{true: " (" + p.Remarks + ")", false: ""}[p.Remarks != ""]
	}
	var ws []string
	for _, a := range p.Allocs {
		w := fmt.Sprintf("%s %d bonds", a.UCC, a.Qty)
		if a.Code != a.UCC {
			w += " (RFQ code " + a.Code + ")"
		}
		ws = append(ws, w)
	}
	if len(ws) == 0 {
		ws = []string{"the firm's own account"}
	}
	s := strings.Join(ws, ", ")
	if p.Action == "propose" {
		s += fmt.Sprintf(" at %s, accrued interest ₹%s, consideration ₹%s", p.Price.String(), formatINR(p.AccruedInterest), formatINR(p.Consideration))
	}
	return s
}

// RFQDiscard drops a confirmation or amendment waiting for a checker.
func (o *Ops) RFQDiscard(ctx context.Context, tenant, actor, id, what, note string) (RFQDeal, error) {
	if err := o.require(ctx, tenant, actor, "Proposing RFQ deal confirmations", RoleMaker, RoleChecker); err != nil {
		return RFQDeal{}, err
	}
	now := o.now()
	var key string
	d, err := docs.Update(ctx, o.Docs, tenant, cRFQDeal, id, actor, func(x *RFQDeal) error {
		switch {
		case what == "amend" && x.Amend != nil:
			x.Amend = nil
		case what != "amend" && x.Proposal != nil:
			key = "send|" + x.ID + "|" + rfqStamp(x.Proposal.MadeAt)
			x.Proposal = nil
		default:
			return fmt.Errorf("%w: nothing is waiting to be sent", ErrState)
		}
		x.event(now, actor, "discarded the waiting %s%s", map[bool]string{true: "amendment", false: "confirmation"}[what == "amend"], map[bool]string{true: ": " + note, false: ""}[note != ""])
		x.Updated = now.Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return d, err
	}
	if key != "" {
		o.closeWork(ctx, tenant, actor, "rfq", key, "discarded")
	} else {
		o.closeWork(ctx, tenant, actor, "rfq", "amendsend|"+d.ID, "discarded")
	}
	o.rfqWork(ctx, tenant, actor, d)
	return d, nil
}

// rfqStamp is the time part of an RFC 3339 time as hhmmss.
func rfqStamp(at string) string {
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		return t.In(ist).Format("150405")
	}
	return ""
}

// rfqSplits divides a deal across clients by their bonds: value, accrued
// interest and consideration in proportion, the last client taking what
// rounding leaves.
func rfqSplits(d RFQDeal, allocs []RFQAlloc, ai, cons money.Dec, counter bool) []rfqSplitOut {
	var out []rfqSplitOut
	var aiLeft, consLeft = ai, cons
	valLeft := d.Value
	for i, a := range allocs {
		f, _ := money.DecInt(a.Qty).Quo(money.DecInt(d.Qty))
		v, ai1, c1 := money.MustDec(d.Value.Mul(f).Fixed(10)), money.MustDec(ai.Mul(f).Fixed(2)), money.MustDec(cons.Mul(f).Fixed(2))
		if i == len(allocs)-1 {
			v, ai1, c1 = valLeft, aiLeft, consLeft
		}
		valLeft, aiLeft, consLeft = valLeft.Sub(v), aiLeft.Sub(ai1), consLeft.Sub(c1)
		s := rfqSplitOut{Val: rfqNum(v, 10), Qty: a.Qty, AccInt: json.Number(ai1.Fixed(2)), Cons: json.Number(c1.Fixed(2))}
		if counter {
			s.CouActPartCode = a.Code
		} else {
			s.PropActPartCode = a.Code
		}
		out = append(out, s)
	}
	return out
}

// rfqProposeReq is POST /rest/v1/deal/propose's request.
type rfqProposeReq struct {
	NgRFQNumber     string        `json:"ngRfqNumber"`
	NgID            string        `json:"ngId"`
	ParticipantCode string        `json:"participantCode"`
	DealType        string        `json:"dealType"`
	ClientCode      string        `json:"clientCode"`
	Price           json.Number   `json:"price"`
	AccruedInterest json.Number   `json:"accruedInterest"`
	Consideration   json.Number   `json:"consideration"`
	CalcMethod      string        `json:"calcMethod"`
	PutCallDate     string        `json:"putCallDate,omitempty"`
	Remarks         string        `json:"remarks,omitempty"`
	TradeSplits     []rfqSplitOut `json:"tradeSplits,omitempty"`
	Role            string        `json:"role"`
}

// RFQSend sends a waiting confirmation to NSE. The checker is not the
// maker. The thread is read again first so the decision is on NSE's latest
// state, and each client is checked again, also against the exchange's
// debarred list.
func (o *Ops) RFQSend(ctx context.Context, tenant, actor, id string) (RFQDeal, error) {
	if err := o.require(ctx, tenant, actor, "Sending RFQ deal confirmations to NSE", RoleChecker); err != nil {
		return RFQDeal{}, err
	}
	c, ok := o.rfqConnection(ctx, tenant)
	if !ok || !c.usable {
		return RFQDeal{}, fmt.Errorf("%w: set up NSE RFQ (debt) in Settings → Connections", ErrState)
	}
	d, _, err := docs.Get[RFQDeal](ctx, o.Docs, tenant, cRFQDeal, id)
	if err != nil {
		return d, err
	}
	p := d.Proposal
	if p == nil {
		return d, fmt.Errorf("%w: no confirmation is waiting to be sent", ErrState)
	}
	if p.Maker == actor {
		return d, fmt.Errorf("%w: the person who proposed it cannot send it: another checker sends it", ErrForbidden)
	}
	byCode, _ := o.rfqCodes(ctx, tenant)
	cm, _ := o.ClientMap(ctx, tenant)
	now := o.now()
	fail := func(stage string, err error) (RFQDeal, error) {
		o.rfqSeen(ctx, tenant, err)
		x, uerr := docs.Update(ctx, o.Docs, tenant, cRFQDeal, id, actor, func(x *RFQDeal) error {
			*x = d
			if x.Proposal != nil {
				x.Proposal.Error, x.Proposal.TriedBy, x.Proposal.TriedAt = err.Error(), actor, now.Format(time.RFC3339)
			}
			x.event(now, actor, "not sent (%s): %s", stage, err.Error())
			x.Updated = now.Format(time.RFC3339)
			return nil
		})
		if uerr != nil {
			return x, uerr
		}
		var re *rfqError
		if errors.As(err, &re) || !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrState) {
			return x, fmt.Errorf("%w: %v", ErrState, err)
		}
		return x, err
	}
	n, err := rfqReadOne(ctx, c, d)
	if err != nil {
		return fail("reading the deal", err)
	}
	rfqApply(&d, n, c.cfg.Member, byCode, cm)
	keep := *p
	d.Proposal = nil // the deal's own state, without the waiting proposal
	act, words := d.action()
	d.Proposal = p
	if (p.Action == "propose" && act != "propose") || (p.Action != "propose" && act != "decide") {
		return fail("checking", fmt.Errorf("%w: the deal changed on NSE (%s): discard this and look again", ErrState, words))
	}
	side, _ := d.side(p.Role)
	today := now.Format("2006-01-02")
	for _, a := range p.Allocs {
		cl, err := rfqClientOK(cm, a.UCC)
		if err != nil {
			return fail("checking the client", err)
		}
		if cl.PAN == "" {
			continue
		}
		if db, hit := o.IsDebarredPAN(ctx, tenant, cl.PAN, today); hit {
			return fail("checking the debarred list", invalid("%s is on the firm's debarred list (%s %s)", a.UCC, firstOf(db.Regulator, "regulator"), firstOf(db.OrderNo, "order")))
		}
		var ls []struct{ PAN, Name, EndDate string }
		if err := rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/debar/all", map[string]any{"pan": cl.PAN, "filtFromDate": rfqOutDate(today), "filtToDate": rfqOutDate(today)}, &ls); err != nil {
			return fail("checking NSE's debarred clients", err)
		}
		for _, l := range ls {
			if strings.EqualFold(strings.TrimSpace(l.PAN), cl.PAN) && (rfqDate(l.EndDate) == "" || rfqDate(l.EndDate) >= today) {
				return fail("checking NSE's debarred clients", invalid("%s is debarred by the exchange until %s", a.UCC, firstOf(l.EndDate, "further notice")))
			}
		}
	}
	var resp rfqNeg
	var steps []string
	switch p.Action {
	case "propose":
		req := rfqProposeReq{NgRFQNumber: d.RFQ, NgID: d.ID, ParticipantCode: c.cfg.Member, Price: rfqNum(p.Price, 4), AccruedInterest: json.Number(p.AccruedInterest.Fixed(2)),
			Consideration: json.Number(p.Consideration.Fixed(2)), CalcMethod: p.CalcMethod, Remarks: p.Remarks, Role: p.Role}
		if p.PutCallDate != "" {
			req.PutCallDate = rfqOutDate(p.PutCallDate)
		}
		switch len(p.Allocs) {
		case 0: // the firm's own deal
			req.DealType, req.ClientCode = "D", c.cfg.Member
		case 1:
			req.DealType, req.ClientCode = "B", p.Allocs[0].Code
		default:
			// Split across clients: the deal keeps its client code (INST*
			// when the RFQ was entered for later split) and each split names
			// its client as the proposer's actual participant code.
			req.DealType, req.ClientCode = "B", side.ClientCode
			req.TradeSplits = rfqSplits(d, p.Allocs, p.AccruedInterest, p.Consideration, false)
		}
		if err := rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/deal/propose", req, &resp); err != nil {
			return fail("proposing", err)
		}
		steps = append(steps, "proposed")
	case "accept":
		switch {
		case len(p.Allocs) == 1 && (side.DealType != "B" || !strings.EqualFold(side.ClientCode, p.Allocs[0].Code)):
			body := map[string]any{"ngRfqNumber": d.RFQ, "ngId": d.ID, "dealType": "B", "clientCode": p.Allocs[0].Code, "buySell": side.Side}
			if err := rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/deal/changeclient", body, &resp); err != nil {
				return fail("changing the client", err)
			}
			steps = append(steps, "client changed to "+p.Allocs[0].Code)
		case len(p.Allocs) > 1:
			body := map[string]any{"rfqNumber": d.RFQ, "id": d.ID, "tradeSplits": rfqSplits(d, p.Allocs, d.AccruedInterest, d.Consideration, true)}
			if err := rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/deal/tradesplit", body, &resp); err != nil {
				return fail("splitting the trade", err)
			}
			steps = append(steps, fmt.Sprintf("split across %d clients", len(p.Allocs)))
		}
		body := map[string]any{"rfqNumber": d.RFQ, "id": d.ID, "acceptedPrice": rfqNum(d.Price, 4), "acceptedAccruedInterest": json.Number(d.AccruedInterest.Fixed(2)),
			"acceptedConsideration": json.Number(d.Consideration.Fixed(2)), "confirmStatus": "PC"}
		if d.PutCallDate != "" {
			body["acceptedPutCallDate"] = rfqOutDate(d.PutCallDate)
		}
		if err := rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/deal/acceptreject", body, &resp); err != nil {
			if len(steps) > 0 {
				d.event(now, actor, "NSE: %s", strings.Join(steps, ", "))
			}
			return fail("accepting", err)
		}
		steps = append(steps, "accepted")
	case "reject":
		if err := rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/deal/acceptreject", map[string]any{"rfqNumber": d.RFQ, "id": d.ID, "confirmStatus": "PR"}, &resp); err != nil {
			return fail("rejecting", err)
		}
		steps = append(steps, "rejected")
	}
	d.Proposal = nil
	if resp.ID != "" {
		rfqApply(&d, resp, c.cfg.Member, byCode, cm)
	}
	if keep.Action == "propose" && d.Confirm != "" && d.Confirm != "PR" && d.Confirm != "CR" {
		d.ProposedByUs = true
	}
	// The clients named are matched by their codes from now on.
	for _, a := range keep.Allocs {
		_ = docs.Upsert(ctx, o.Docs, tenant, cRFQClient, a.UCC, RFQClientCode{UCC: a.UCC, Code: a.Code, By: actor, At: now.Format(time.RFC3339)}, actor)
		for i := range d.Sides {
			if strings.EqualFold(d.Sides[i].ClientCode, a.Code) && d.Sides[i].UCC == "" {
				d.Sides[i].UCC = a.UCC
				d.Sides[i].Name = cm[a.UCC].Name
			}
		}
		for i := range d.Splits {
			if strings.EqualFold(d.Splits[i].ClientCode, a.Code) && d.Splits[i].UCC == "" {
				d.Splits[i].UCC = a.UCC
			}
		}
	}
	if d.Status == "C" && d.Settle == "" {
		d.Settle = "To settle"
	}
	d.event(now, actor, "sent to NSE (proposed by %s): %s · now %s%s", keep.Maker, strings.Join(steps, ", "), firstOf(rfqNegStatus[d.Status], d.Status),
		map[bool]string{true: " · " + firstOf(rfqConfirmStatus[d.Confirm], d.Confirm), false: ""}[d.Confirm != ""])
	d.Updated = now.Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cRFQDeal, d.ID, d, actor); err != nil {
		return d, err
	}
	o.closeWork(ctx, tenant, actor, "rfq", "send|"+d.ID+"|"+rfqStamp(keep.MadeAt), "sent by "+actor)
	o.rfqWork(ctx, tenant, actor, d)
	o.log(ctx, tenant, actor, "rfq.confirm_sent", d.ID, map[string]any{"action": keep.Action, "maker": keep.Maker, "status": d.Status, "confirm": d.Confirm})
	return d, nil
}

// RFQCalc is NSE's price and accrued interest at the deal's yield.
type RFQCalc struct {
	Price           money.Dec `json:"price"`
	AccruedInterest money.Dec `json:"accrued_interest"`
	PutCallDate     string    `json:"put_call_date,omitempty"`
	CalcMethod      string    `json:"calc_method"`
}

// RFQCalcPrice asks NSE's calculator (rfqmaster/calcprice) for the price at
// the deal's yield on its settlement date. aiDcc is the accrued interest
// day count: 1 Actual/Actual, 2 30/360 European, 3 30/360 American, 4
// Actual/365.
func (o *Ops) RFQCalcPrice(ctx context.Context, tenant, actor, id string, aiDcc int) (RFQCalc, error) {
	if err := o.require(ctx, tenant, actor, "Proposing RFQ deal confirmations", RoleMaker, RoleChecker); err != nil {
		return RFQCalc{}, err
	}
	c, ok := o.rfqConnection(ctx, tenant)
	if !ok || !c.usable {
		return RFQCalc{}, fmt.Errorf("%w: set up NSE RFQ (debt) in Settings → Connections", ErrState)
	}
	d, _, err := docs.Get[RFQDeal](ctx, o.Docs, tenant, cRFQDeal, id)
	if err != nil {
		return RFQCalc{}, err
	}
	if aiDcc == 0 {
		aiDcc = 1
	}
	if aiDcc < 1 || aiDcc > 4 {
		return RFQCalc{}, invalid("the day count is 1 (Actual/Actual), 2 (30/360 European), 3 (30/360 American) or 4 (Actual/365)")
	}
	if d.SettlementDate == "" || d.YieldType == "" || d.Yield.Sign() <= 0 {
		return RFQCalc{}, fmt.Errorf("%w: NSE has not given this deal's yield and settlement date", ErrState)
	}
	var r struct {
		CalcMethod, PutCallDate string
		AccruedInterest, Price  rfqDec
	}
	if err := rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/rfqmaster/calcprice", map[string]any{"isin": d.ISIN, "settlementDate": rfqOutDate(d.SettlementDate), "yieldType": d.YieldType, "yield": rfqNum(d.Yield, 4), "aiDcc": aiDcc}, &r); err != nil {
		o.rfqSeen(ctx, tenant, err)
		return RFQCalc{}, fmt.Errorf("%w: %v", ErrState, err)
	}
	return RFQCalc{Price: r.Price.v, AccruedInterest: r.AccruedInterest.v, PutCallDate: rfqDate(r.PutCallDate), CalcMethod: r.CalcMethod}, nil
}

// ---- amendments ----

// RFQAmendInput is the maker's amendment of a confirmed deal.
type RFQAmendInput struct {
	Type            string `json:"type"` // M or C
	YieldType       string `json:"yield_type,omitempty"`
	AccruedInterest string `json:"accrued_interest,omitempty"`
	SettlementType  string `json:"settlement_type,omitempty"`
	Reason          string `json:"reason"`
	Remarks         string `json:"remarks,omitempty"`
	ProposedBySide  string `json:"proposed_by_side,omitempty"`
}

// RFQProposeAmend records the maker's modification or cancellation of a
// confirmed deal, for a checker to send (dealamend/modifycancel).
func (o *Ops) RFQProposeAmend(ctx context.Context, tenant, actor, id string, in RFQAmendInput) (RFQDeal, error) {
	if err := o.require(ctx, tenant, actor, "Proposing RFQ deal confirmations", RoleMaker, RoleChecker); err != nil {
		return RFQDeal{}, err
	}
	d, _, err := docs.Get[RFQDeal](ctx, o.Docs, tenant, cRFQDeal, id)
	if err != nil {
		return d, err
	}
	if d.Status != "C" || d.Settle == "Cancelled" {
		return d, fmt.Errorf("%w: only a confirmed deal is amended", ErrState)
	}
	if d.Amend != nil {
		return d, fmt.Errorf("%w: an amendment is already waiting for a checker", ErrState)
	}
	p := RFQAmendProposal{Type: strings.ToUpper(strings.TrimSpace(in.Type)), Reason: strings.ToUpper(strings.TrimSpace(in.Reason)), Remarks: strings.TrimSpace(in.Remarks),
		ProposedBySide: strings.ToUpper(strings.TrimSpace(in.ProposedBySide)), Maker: actor, MadeAt: o.now().Format(time.RFC3339)}
	has := func(cs []RFQCode, c string) bool {
		for _, x := range cs {
			if x.Code == c {
				return true
			}
		}
		return false
	}
	switch p.Type {
	case "M":
		if !has(RFQModReasons, p.Reason) {
			return d, invalid("choose the modification reason from the list")
		}
		if len(d.Splits) > 0 {
			return d, invalid("this deal was split: modify it on NSE's RFQ screen, which has the split trades' numbers")
		}
		p.YieldType = strings.ToUpper(firstOf(strings.TrimSpace(in.YieldType), d.YieldType))
		if p.YieldType != "YTM" && p.YieldType != "YTP" && p.YieldType != "YTC" {
			return d, invalid("the yield type is YTM, YTP or YTC")
		}
		ai, err := money.ParseDec(strings.TrimSpace(firstOf(in.AccruedInterest, d.AccruedInterest.String())))
		if err != nil || ai.Sign() < 0 {
			return d, invalid("give the new accrued interest")
		}
		p.AccruedInterest = money.MustDec(ai.Fixed(2))
		p.SettlementType = firstOf(strings.TrimSpace(in.SettlementType), d.SettlementType)
		if p.SettlementType != "0" && p.SettlementType != "1" {
			return d, invalid("the settlement type is 0 (T+0) or 1 (T+1)")
		}
	case "C":
		if !has(RFQCancReasons, p.Reason) {
			return d, invalid("choose the cancellation reason from the list")
		}
	default:
		return d, invalid("say whether it is a modification (M) or a cancellation (C)")
	}
	if len(p.Remarks) > 200 {
		return d, invalid("remarks for the counterparty are at most 200 characters")
	}
	if len(d.Sides) > 1 && p.ProposedBySide != "B" && p.ProposedBySide != "S" {
		return d, invalid("the firm is the broker on both sides: say whether the buyer (B) or the seller (S) asks for it")
	}
	if len(d.Sides) == 1 {
		p.ProposedBySide = ""
	}
	now := o.now()
	d, err = docs.Update(ctx, o.Docs, tenant, cRFQDeal, id, actor, func(x *RFQDeal) error {
		x.Amend = &p
		x.event(now, actor, "%s proposed for a checker: %s", map[string]string{"M": "modification", "C": "cancellation"}[p.Type], rfqReasonText(p.Reason))
		x.Updated = now.Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return d, err
	}
	o.raiseWork(ctx, tenant, "rfq", "amendsend|"+d.ID, "Send the RFQ deal amendment of "+firstOf(d.TradeNo, d.ID),
		fmt.Sprintf("%s proposes to %s deal %s (%s): %s. A different checker sends it to NSE on Bonds (RFQ deals).", actor, map[string]string{"M": "modify", "C": "cancel"}[p.Type], firstOf(d.TradeNo, d.ID), d.ISIN, rfqReasonText(p.Reason)),
		rfqDealUCC(d), RoleChecker, "rfq", now.Format("2006-01-02"))
	o.log(ctx, tenant, actor, "rfq.amend_proposed", d.ID, map[string]any{"type": p.Type, "reason": p.Reason})
	return d, nil
}

// RFQSendAmend sends the waiting amendment to NSE; the checker is not the
// maker.
func (o *Ops) RFQSendAmend(ctx context.Context, tenant, actor, id string) (RFQAmend, error) {
	if err := o.require(ctx, tenant, actor, "Sending RFQ deal confirmations to NSE", RoleChecker); err != nil {
		return RFQAmend{}, err
	}
	c, ok := o.rfqConnection(ctx, tenant)
	if !ok || !c.usable {
		return RFQAmend{}, fmt.Errorf("%w: set up NSE RFQ (debt) in Settings → Connections", ErrState)
	}
	d, _, err := docs.Get[RFQDeal](ctx, o.Docs, tenant, cRFQDeal, id)
	if err != nil {
		return RFQAmend{}, err
	}
	p := d.Amend
	if p == nil {
		return RFQAmend{}, fmt.Errorf("%w: no amendment is waiting to be sent", ErrState)
	}
	if p.Maker == actor {
		return RFQAmend{}, fmt.Errorf("%w: the person who proposed it cannot send it: another checker sends it", ErrForbidden)
	}
	body := map[string]any{"rfqNumber": d.RFQ, "ngId": d.ID, "requestType": p.Type}
	if p.Type == "M" {
		// The table gives modSettlementType as String(1); the sample and the
		// response send a number, which is what goes.
		body["modYieldType"], body["modAccruedInterest"], body["modSettlementType"], body["modReason"] = p.YieldType, json.Number(p.AccruedInterest.Fixed(2)), json.Number(p.SettlementType), p.Reason
	} else {
		body["cancReason"] = p.Reason
	}
	if p.Remarks != "" {
		body["proposerRemarks"] = p.Remarks
	}
	if p.ProposedBySide != "" {
		body["proposedBySide"] = p.ProposedBySide
	}
	now := o.now()
	var w rfqAmendWire
	if err := rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/dealamend/modifycancel", body, &w); err != nil {
		o.rfqSeen(ctx, tenant, err)
		_, _ = docs.Update(ctx, o.Docs, tenant, cRFQDeal, id, actor, func(x *RFQDeal) error {
			if x.Amend != nil {
				x.Amend.Error = err.Error()
			}
			x.event(now, actor, "amendment not sent: %s", err.Error())
			return nil
		})
		return RFQAmend{}, fmt.Errorf("%w: %v", ErrState, err)
	}
	a, _ := o.rfqKeepAmend(ctx, tenant, c, w, "")
	_, _ = docs.Update(ctx, o.Docs, tenant, cRFQDeal, id, actor, func(x *RFQDeal) error {
		x.Amend = nil
		x.event(now, actor, "amendment %s sent to NSE (proposed by %s): %s", a.ID, p.Maker, firstOf(a.StatusText, "proposed"))
		x.Updated = now.Format(time.RFC3339)
		return nil
	})
	o.closeWork(ctx, tenant, actor, "rfq", "amendsend|"+d.ID, "sent by "+actor)
	o.log(ctx, tenant, actor, "rfq.amend_sent", d.ID, map[string]any{"amendment": a.ID, "type": p.Type, "maker": p.Maker})
	return a, nil
}

// RFQDecideAmend approves or rejects the counterparty's amendment
// (dealamend/updatestatus). A rejection says why, for the counterparty.
func (o *Ops) RFQDecideAmend(ctx context.Context, tenant, actor, id string, approve bool, remarks string) (RFQAmend, error) {
	if err := o.require(ctx, tenant, actor, "Deciding RFQ deal amendments", RoleChecker); err != nil {
		return RFQAmend{}, err
	}
	c, ok := o.rfqConnection(ctx, tenant)
	if !ok || !c.usable {
		return RFQAmend{}, fmt.Errorf("%w: set up NSE RFQ (debt) in Settings → Connections", ErrState)
	}
	a, _, err := docs.Get[RFQAmend](ctx, o.Docs, tenant, cRFQAmend, id)
	if err != nil {
		return a, err
	}
	switch {
	case a.Ours != "decide":
		return a, fmt.Errorf("%w: the firm did not receive this amendment to decide", ErrState)
	case a.Status != "PP":
		return a, fmt.Errorf("%w: this amendment is already %s", ErrState, strings.ToLower(a.StatusText))
	}
	remarks = strings.TrimSpace(remarks)
	if !approve && remarks == "" {
		return a, invalid("say why it is rejected: the counterparty sees the remarks")
	}
	if len(remarks) > 200 {
		return a, invalid("remarks are at most 200 characters")
	}
	st := map[bool]string{true: "EA", false: "CR"}[approve]
	body := map[string]any{"id": json.Number(a.ID), "approvalStatus": st, "counterRemarks": nil}
	if remarks != "" {
		body["counterRemarks"] = remarks
	}
	var w rfqAmendWire
	if err := rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/dealamend/updatestatus", body, &w); err != nil {
		o.rfqSeen(ctx, tenant, err)
		return a, fmt.Errorf("%w: %v", ErrState, err)
	}
	if string(w.ID) == "" {
		w.ID = flexStr(a.ID)
	}
	if w.ApprovalStatus == "" {
		w.ApprovalStatus = st
	}
	a2, _ := o.rfqKeepAmend(ctx, tenant, c, w, actor)
	o.closeWork(ctx, tenant, actor, "rfq", "amend|"+a.ID, map[bool]string{true: "approved", false: "rejected"}[approve]+" by "+actor)
	now := o.now()
	_, _ = docs.Update(ctx, o.Docs, tenant, cRFQDeal, a.Deal, actor, func(x *RFQDeal) error {
		x.event(now, actor, "%s the counterparty's %s %s (%s)%s", map[bool]string{true: "approved", false: "rejected"}[approve], map[string]string{"M": "modification", "C": "cancellation"}[a.Type], a.ID, a.ReasonText,
			map[bool]string{true: ": " + remarks, false: ""}[remarks != ""])
		return nil
	})
	o.log(ctx, tenant, actor, "rfq.amend_decided", a.ID, map[string]any{"deal": a.Deal, "status": st})
	return a2, nil
}

// ---- settlement ----

// RFQMarkSettled records that a confirmed deal was settled (and billed)
// outside Ledgerline.
func (o *Ops) RFQMarkSettled(ctx context.Context, tenant, actor, id, note string) (RFQDeal, error) {
	if err := o.require(ctx, tenant, actor, "Recording RFQ deal settlement", RoleMaker, RoleChecker); err != nil {
		return RFQDeal{}, err
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return RFQDeal{}, invalid("say how it was settled (pay-in reference, bill number)")
	}
	now := o.now()
	d, err := docs.Update(ctx, o.Docs, tenant, cRFQDeal, id, actor, func(x *RFQDeal) error {
		if x.Status != "C" || x.Settle != "To settle" {
			return fmt.Errorf("%w: only a confirmed deal waiting to settle is marked settled", ErrState)
		}
		x.Settle, x.SettleNote = "Settled", note
		x.event(now, actor, "settled: %s", note)
		x.Updated = now.Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return d, err
	}
	o.rfqWork(ctx, tenant, actor, d)
	o.log(ctx, tenant, actor, "rfq.settled", d.ID, map[string]any{"note": note})
	return d, nil
}

// RFQDealsCSV lists the confirmed deals settling on a date, for the pay-in
// and billing.
func (o *Ops) RFQDealsCSV(ctx context.Context, tenant, actor, date string) ([]byte, error) {
	if err := o.rfqCanView(ctx, tenant, actor); err != nil {
		return nil, err
	}
	if !validDate(date) {
		return nil, invalid("give the settlement date as yyyy-mm-dd")
	}
	ds, err := docs.List[RFQDeal](ctx, o.Docs, tenant, cRFQDeal, "")
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"Settlement date", "Trade number", "RFQ", "Deal", "ISIN", "Description", "UCC", "Client", "RFQ client code", "Buy/Sell", "Quantity", "Value (crore)", "Yield type", "Yield", "Price", "Accrued interest", "Consideration", "Counterparty", "Settlement"})
	for _, d := range ds {
		if d.Status != "C" || d.SettlementDate != date || d.Settle == "Cancelled" {
			continue
		}
		rows := [][]string{}
		for _, s := range d.Sides {
			if len(d.Splits) > 0 {
				for _, sp := range d.Splits {
					rows = append(rows, []string{sp.UCC, "", sp.ClientCode, s.Side, fmt.Sprint(sp.Qty), sp.Value.String(), sp.AccruedInterest.Fixed(2), sp.Consideration.Fixed(2)})
				}
				continue
			}
			rows = append(rows, []string{s.UCC, s.Name, s.ClientCode, s.Side, fmt.Sprint(d.Qty), d.Value.String(), d.AccruedInterest.Fixed(2), d.Consideration.Fixed(2)})
		}
		for _, r := range rows {
			_ = w.Write([]string{d.SettlementDate, d.TradeNo, d.RFQ, d.ID, d.ISIN, d.Description, r[0], r[1], r[2], r[3], r[4], r[5], d.YieldType, d.Yield.String(), d.Price.String(), r[6], r[7], d.Counterparty, d.Settle})
		}
	}
	w.Flush()
	return b.Bytes(), w.Error()
}

// ---- NSE's callback ----

// RFQCallbackResult says what a notification held.
type RFQCallbackResult struct {
	RFQs         int `json:"rfqs"`
	Negotiations int `json:"negotiations"`
	Amendments   int `json:"amendments"`
}

// RFQCallback takes NSE's notification (POST /v1/notification under the
// callback base given to NSE, which is /hooks/<tenant>/nse-rfq/<callback
// token>). The address must carry the firm's callback token; when the
// signing key is entered, the "token" header must be the hex HmacSHA256 of
// the body under it. The message is kept as received, and the deals are
// read again through the API at the next minute: what NSE's API says is
// what is kept, not the notification's copy.
func (o *Ops) RFQCallback(ctx context.Context, tenant, token, signature string, body []byte) (RFQCallbackResult, error) {
	var res RFQCallbackResult
	c, ok := o.rfqConnection(ctx, tenant)
	if !ok || c.cbToken == "" {
		return res, fmt.Errorf("%w: the NSE RFQ callback is not set up", ErrForbidden)
	}
	if !hmac.Equal([]byte(strings.TrimSpace(token)), []byte(c.cbToken)) {
		return res, fmt.Errorf("%w: the callback token does not match", ErrForbidden)
	}
	if c.cbKey != "" {
		m := hmac.New(sha256.New, []byte(c.cbKey))
		m.Write(body)
		want := hex.EncodeToString(m.Sum(nil))
		if !hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(signature))), []byte(want)) {
			return res, fmt.Errorf("%w: the notification's signature (token header) does not match the signing key", ErrForbidden)
		}
	}
	var n struct {
		RFQList         []json.RawMessage `json:"rfqList"`
		NegotiationList []json.RawMessage `json:"negotiationList"`
		DealAmendList   []json.RawMessage `json:"dealAmendList"`
	}
	if err := json.Unmarshal(body, &n); err != nil {
		return res, invalid("the notification is not JSON")
	}
	res = RFQCallbackResult{RFQs: len(n.RFQList), Negotiations: len(n.NegotiationList), Amendments: len(n.DealAmendList)}
	now := o.now()
	sum := sha256.Sum256(body)
	if _, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "NSE-RFQ-CALLBACK", Name: "rfq-notification-" + now.Format("20060102T150405") + ".json", SHA256: hex.EncodeToString(sum[:]), Content: body}); err != nil {
		return res, err
	}
	o.rfqState(ctx, tenant, func(s *RFQState) {
		s.LastCallback, s.Callbacks = now.Format(time.RFC3339), s.Callbacks+1
		s.Pending = s.Pending || res.Negotiations+res.Amendments > 0
	})
	o.log(ctx, tenant, SystemActor, "rfq.notification", "", map[string]any{"rfqs": res.RFQs, "negotiations": res.Negotiations, "amendments": res.Amendments})
	return res, nil
}

// ---- the tick ----

// rfqWindow is the day's reading window from NSE's market timings, read
// once a day: from the earliest start to half an hour after the latest
// consideration cut-off. Until the timings are read, 09:00 to 20:00.
func (o *Ops) rfqWindow(ctx context.Context, tenant string, c rfqConn, today string) (string, string, error) {
	st := o.rfqState(ctx, tenant, nil)
	if st.TimingsDate == today && st.Open != "" {
		return st.Open, st.Close, nil
	}
	var ts []rfqTiming
	if err := rfqCall(ctx, c.cfg, http.MethodGet, "/rest/v1/market/timings", nil, &ts); err != nil || len(ts) == 0 {
		return "09:00", "20:00", err
	}
	var lo, hi int64 = -1, 0
	var tv []RFQTiming
	for _, t := range ts {
		for _, s := range []int64{t.StarttimeT0, t.StarttimeT1} {
			if s > 0 && (lo < 0 || s < lo) {
				lo = s
			}
		}
		hi = max(hi, t.PricecutoffT0, t.PricecutoffT1)
		tv = append(tv, RFQTiming{Type: t.Type, StartT0: rfqHHMM(t.StarttimeT0), StartT1: rfqHHMM(t.StarttimeT1), YieldCutT0: rfqHHMM(t.YieldcutoffT0), YieldCutT1: rfqHHMM(t.YieldcutoffT1),
			PriceCutT0: rfqHHMM(t.PricecutoffT0), PriceCutT1: rfqHHMM(t.PricecutoffT1)})
	}
	if lo < 0 || hi == 0 {
		return "09:00", "20:00", nil
	}
	open, closeAt := rfqHHMM(lo), rfqHHMM(hi+30*60000)
	o.rfqState(ctx, tenant, func(s *RFQState) { s.TimingsDate, s.Open, s.Close, s.Timings = today, open, closeAt, tv })
	return open, closeAt, nil
}

// rfqTick reads the deals every few minutes on business days while the
// market is open, and at once (the next minute) after a notification. It
// stops once NSE refuses the sign-in, until the connection is saved again.
func (o *Ops) rfqTick(ctx context.Context, tenant string, now time.Time) {
	c, ok := o.rfqConnection(ctx, tenant)
	if !ok || !c.usable || !c.read || c.problem {
		return
	}
	now = now.In(ist)
	today, hm := now.Format("2006-01-02"), now.Format("15:04")
	set, _ := o.Settings(ctx, tenant)
	if !set.IsBusinessDay(today) {
		return
	}
	st := o.rfqState(ctx, tenant, nil)
	open, closeAt, err := o.rfqWindow(ctx, tenant, c, today)
	if err != nil {
		o.rfqSeen(ctx, tenant, err)
		var le *rfqLoginError
		if errors.As(err, &le) {
			return
		}
	}
	if !st.Pending && (hm < open || hm > closeAt) {
		return
	}
	if t, err := time.Parse(time.RFC3339, st.LastRead); err == nil && !st.Pending && now.Sub(t) < c.every {
		return
	}
	_, _ = o.RFQSync(ctx, tenant, SystemActor, today)
}
