package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- CDSL eDIS (CDSL API Handbook §5.11–5.15) ----
//
// A client without a DDPI authorises the sale of shares from the demat
// account by TPIN (and OTP) on CDSL's page, not by a slip:
//
//   - Request (§5.11): the client picks the shares on the portal (or a
//     member of staff does it with the client present). Ledgerline builds
//     the encrypted transaction details and the browser posts them to
//     CDSL's eDIS page; the client enters the TPIN and OTP there. CDSL posts
//     the result back to Ledgerline's return address (not signed in: it is
//     taken only if it decrypts under the DP's eDIS key and its request ID
//     matches one sent from here). Each authorisation is recorded per ISIN
//     with CDSL's transaction ID, and the client gets a message.
//   - Inquiry (§5.12): when the redirect back never came (the browser was
//     closed), the tick asks CDSL for the status of the day's requests.
//     CDSL says the inquiry is not for building back-office data, so it is
//     used only for requests left without an answer.
//   - TPIN generation (§5.13): a server call; CDSL sends the TPIN to the
//     mobile and e-mail it has for the account. Clients ask on the portal;
//     staff ask for a client with a note of how the client asked.
//   - TPIN change (§5.15): a redirect to CDSL's change-PIN page with the BO
//     ID and PAN filled in.
//   - Cancel (§5.14): revokes the unused quantity of pre-trade
//     authorisations, at least 20 minutes after they were set up, by a
//     redirect the client completes on CDSL's page; CDSL posts the result
//     back to a second return address.
//
// Live risk (live.go) counts a delivery sell by a client who has no DDPI
// against the eDIS-authorised quantity, not the holding.
//
// Request IDs: ReqId and TxnReqId are 16 digits (the date and a sequence,
// so unique for the DP's lifetime); RevocReqId and RevocReqTxnId 24.

const (
	cEDIS      = "edisauth"  // by ReqId
	cEDISRevoc = "edisrevoc" // by RevocReqId
	cEDISPin   = "edispin"   // TPIN generations, by ReqId

	edisPathVerify    = "EDIS/VerifyDIS"            // §5.11.7 (redirect)
	edisPathInquiry   = "eDISService/GetTranStatus" // §5.12.1
	edisPathPin       = "eDISService/GenerateBOPin" // §5.13.1
	edisPathCancel    = "EDIS/CancelTransaction"    // §5.14.1 sample form (redirect)
	edisPathChangePin = "Home/ChangeBOPin"          // §5.15.1 (redirect)

	edisMaxRecords   = 10 // §5.15.3 ERR032 (the notes say 100; CDSL's error list says 10)
	edisCancelAfter  = 20 * time.Minute
	edisPinPerDay    = 3
	edisInquireAfter = 10 * time.Minute
)

func init() {
	EventVars["edis.authorised"] = []string{"req", "status", "shares", "valid_till", "reason"}
	EventVars["edis.cancelled"] = []string{"req", "shares"}
	t := func(ev, ch, cat, subj, body, dlt string) Template {
		return Template{Event: ev, Channel: ch, Category: cat, Subject: subj, Body: body, DLT: dlt, Active: true}
	}
	extraTemplates = append(extraTemplates,
		t("edis.authorised", ChEmail, "Transactional", "Sale authorisation {req}: {status}", "Dear {name},\n\nCDSL has recorded your eDIS sale authorisation {req}: {status}.\n\nShares: {shares}. Valid until {valid_till}. {reason}\n\nIf you did not authorise this, tell us at once and call CDSL.\n\n{firm}", ""),
		t("edis.authorised", ChSMS, "Transactional", "", "Dear {name}, CDSL eDIS authorisation {req}: {status} for {shares}, valid till {valid_till}. Not you? Call us at once. -{firm}", "1107166000000000071"),
		t("edis.cancelled", ChEmail, "Transactional", "Sale authorisation {req} cancelled", "Dear {name},\n\nThe unused quantity of your eDIS sale authorisation {req} is cancelled at CDSL: {shares}.\n\n{firm}", ""),
		t("edis.cancelled", ChSMS, "Transactional", "", "Dear {name}, your CDSL eDIS authorisation {req} is cancelled for {shares}. -{firm}", "1107166000000000072"),
	)
}

// edisErrors are §5.15.3's codes most often seen, in words.
var edisErrors = map[string]string{
	"ERR002": "the DP is not registered for eDIS", "ERR003": "wrong TPIN", "ERR005": "invalid TPIN", "ERR007": "the client's session timed out",
	"ERR010": "invalid BO ID", "ERR014": "CDSL could not decrypt the request (check the eDIS key)", "ERR016": "invalid CM ID", "ERR027": "TPIN entered wrongly three times",
	"ERR028": "the DP is not registered for eDIS", "ERR029": "CDSL has no eDIS key for the DP", "ERR038": "the account is not a sole-holder account",
	"ERR039": "the account is not active", "ERR040": "the PAN does not match CDSL's", "ERR041": "the account's sub-type cannot use eDIS",
	"ERR047": "the client pressed Cancel", "ERR052": "the session timed out", "ERR056": "CDSL has no mobile number or e-mail for the account",
	"ERR057": "the account is not of this DP", "ERR058": "the client has not entered the TPIN yet", "ERR059": "TPIN cannot be generated for a POA master account",
	"ERR060": "OTP limit reached", "ERR062": "settlement ID not found at CDSL", "ERR063": "settlement date does not match the settlement ID",
	"ERR064": "the CM ID is not mapped to the DP", "ERR065": "neither settlement ID nor date given", "ERR072": "the authorisation is no longer active",
	"ERR073": "not enough unused quantity to cancel", "ERR074": "wrong OTP for the cancellation",
}

func edisWords(code, msg string) string {
	code = strings.TrimSpace(code)
	if w, ok := edisErrors[code]; ok {
		return w + " (" + code + ")"
	}
	return cdslExplain(code, msg)
}

// EDISLine is one ISIN of an authorisation.
type EDISLine struct {
	TxnReqID   string `json:"txn_req_id"`
	ISIN       string `json:"isin"`
	Symbol     string `json:"symbol"`
	Qty        int64  `json:"qty"`
	SettleID   string `json:"settle_id,omitempty"`
	SettleDate string `json:"settle_date,omitempty"` // YYYY-MM-DD
	TxnID      string `json:"txn_id,omitempty"`      // CDSL's, once authorised
	Status     string `json:"status"`                // Awaiting TPIN, Authorised, Failed, Cancelled
	Error      string `json:"error,omitempty"`
	Cancelled  int64  `json:"cancelled,omitempty"`
}

// EDISAuth is one eDIS request and what CDSL said.
type EDISAuth struct {
	ReqID       string     `json:"req_id"`
	UCC         string     `json:"ucc"`
	Name        string     `json:"name"`
	BOID        string     `json:"bo_id"`
	ReqType     string     `json:"req_type"` // D pre-trade, E post-trade, F off-market
	Version     string     `json:"version"`
	Exchange    string     `json:"exchange,omitempty"`
	CMID        string     `json:"cm_id,omitempty"`
	Days        int        `json:"days,omitempty"`
	ValidTill   string     `json:"valid_till"`
	CtrBOID     string     `json:"counter_bo,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	ExecDate    string     `json:"exec_date,omitempty"`
	Lines       []EDISLine `json:"lines"`
	Status      string     `json:"status"` // Awaiting TPIN, Authorised, Partly authorised, Failed, Cancelled, No answer
	ResID       string     `json:"res_id,omitempty"`
	ResError    string     `json:"res_error,omitempty"`
	At          string     `json:"at"`
	By          string     `json:"by"`
	Via         string     `json:"via"` // Portal, Staff
	DoneAt      string     `json:"done_at,omitempty"`
	AnsweredBy  string     `json:"answered_by,omitempty"` // Redirect, Inquiry
	Inquiries   int        `json:"inquiries,omitempty"`
	LastInquiry string     `json:"last_inquiry,omitempty"`
}

// EDISRevoc is a cancellation sent to CDSL.
type EDISRevoc struct {
	RevocReqID string            `json:"revoc_req_id"`
	ReqID      string            `json:"req_id"`
	UCC        string            `json:"ucc"`
	Lines      map[string]string `json:"lines"` // RevocReqTxnId -> TxnReqId
	Status     string            `json:"status"`
	Result     string            `json:"result,omitempty"`
	At         string            `json:"at"`
	By         string            `json:"by"`
}

// EDISPin is a TPIN generation asked of CDSL.
type EDISPin struct {
	ReqID  string `json:"req_id"`
	UCC    string `json:"ucc"`
	Status string `json:"status"` // Sent to the client, Refused, Error
	Error  string `json:"error,omitempty"`
	ResID  string `json:"res_id,omitempty"`
	At     string `json:"at"`
	By     string `json:"by"`
	Via    string `json:"via"`
	Note   string `json:"note,omitempty"`
}

// EDISRequestLine is one ISIN asked for.
type EDISRequestLine struct {
	ISIN       string `json:"isin"`
	Qty        int64  `json:"qty"`
	SettleID   string `json:"settle_id"`
	SettleDate string `json:"settle_date"`
}

// EDISRequest is what the client (or staff) asks to authorise.
type EDISRequest struct {
	UCC      string            `json:"ucc"`
	ReqType  string            `json:"req_type"` // D (default), E, F
	Exchange string            `json:"exchange"` // NSE, BSE
	Lines    []EDISRequestLine `json:"lines"`
	CtrBOID  string            `json:"counter_bo"` // F
	Reason   string            `json:"reason"`     // F: an off-market reason
	ExecDate string            `json:"exec_date"`  // F
}

// edisWho checks who may act for a client: the client on the portal, or
// staff.
func (o *Ops) edisWho(ctx context.Context, tenant, actor, ucc string) (string, error) {
	if u, err := o.user(ctx, tenant, actor); err == nil && u.Role == RoleClient {
		if !contains(u.Clients, strings.ToUpper(strings.TrimSpace(ucc))) {
			return "", ErrNotFound
		}
		return "Portal", nil
	}
	if err := o.require(ctx, tenant, actor, "eDIS and TPIN for clients", RoleMaker, RoleChecker, RoleBranch); err != nil {
		return "", err
	}
	return "Staff", nil
}

// edisConn is the connection with eDIS on.
func (o *Ops) edisConn(ctx context.Context, tenant string) (cdslConn, error) {
	x, ok := o.cdxConn(ctx, tenant)
	if !ok || !x.On(cdslFamEDIS) {
		return x, fmt.Errorf("%w: CDSL eDIS is not switched on: the firm sets it up in Settings → Connections → CDSL APIs (DP)", ErrState)
	}
	return x, nil
}

// edisClient is the client and its BO ID, checked for eDIS.
func (o *Ops) edisClient(ctx context.Context, tenant string, x cdslConn, ucc string) (Client, BOAccount, error) {
	c, err := o.Client(ctx, tenant, ucc)
	if err != nil {
		return c, BOAccount{}, invalid("client %s not found", ucc)
	}
	b := o.bo(ctx, tenant, c)
	switch {
	case b.Status == "Closed" || b.Status == "Not opened":
		return c, b, fmt.Errorf("%w: the demat account is %s", ErrState, strings.ToLower(b.Status))
	case b.Status == "Frozen" && b.Freeze != "Credit":
		return c, b, fmt.Errorf("%w: the demat account is frozen for debits", ErrState)
	}
	b.BOID = strings.TrimSpace(b.BOID)
	return c, b, cdxOwnBO(x, b.BOID)
}

func edisDDMMYYYY(d string) string {
	if len(d) != 10 {
		return ""
	}
	return d[8:10] + d[5:7] + d[:4]
}

func (x cdslConn) edisVersion() string { return firstOf(x.c.Values["edis_version"], "1.2") }

func (x cdslConn) edisDays() int {
	n, err := strconv.Atoi(strings.TrimSpace(x.c.Values["edis_days"]))
	if err != nil || n < 1 || n > 99 {
		return 1
	}
	return n
}

// edisReturnBase is Ledgerline's public address for CDSL's redirects.
func edisReturnBase(x cdslConn, base string) (string, error) {
	pub := strings.TrimRight(firstOf(x.Callback, base), "/")
	if pub == "" {
		return "", invalid("set Ledgerline's public address in the CDSL APIs connection so CDSL can send the client back")
	}
	return pub, nil
}

// StartEDIS builds the redirect POST that takes the client to CDSL's eDIS
// page to authorise the sale with the TPIN (§5.11.7).
func (o *Ops) StartEDIS(ctx context.Context, tenant, actor string, in EDISRequest, base string) (CDSLForm, EDISAuth, error) {
	in.UCC = strings.ToUpper(strings.TrimSpace(in.UCC))
	via, err := o.edisWho(ctx, tenant, actor, in.UCC)
	if err != nil {
		return CDSLForm{}, EDISAuth{}, err
	}
	x, err := o.edisConn(ctx, tenant)
	if err != nil {
		return CDSLForm{}, EDISAuth{}, err
	}
	c, b, err := o.edisClient(ctx, tenant, x, in.UCC)
	if err != nil {
		return CDSLForm{}, EDISAuth{}, err
	}
	pub, err := edisReturnBase(x, base)
	if err != nil {
		return CDSLForm{}, EDISAuth{}, err
	}
	set, _ := o.Settings(ctx, tenant)
	today := o.now().Format("2006-01-02")
	a := EDISAuth{UCC: c.UCC, Name: c.Name, BOID: b.BOID, ReqType: firstOf(strings.ToUpper(strings.TrimSpace(in.ReqType)), "D"), Version: x.edisVersion(),
		Status: "Awaiting TPIN", At: o.now().Format(time.RFC3339), By: actor, Via: via}
	switch a.ReqType {
	case "D", "E":
		a.Exchange = strings.ToUpper(firstOf(strings.TrimSpace(in.Exchange), "NSE"))
		if cdxExchangeIDs[a.Exchange] == "" {
			return CDSLForm{}, a, invalid("the exchange is NSE or BSE")
		}
	case "F":
		a.CtrBOID, a.Reason, a.ExecDate = strings.TrimSpace(in.CtrBOID), in.Reason, firstOf(in.ExecDate, today)
		if len(a.CtrBOID) != 16 || strings.Trim(a.CtrBOID, "0123456789") != "" || a.CtrBOID == b.BOID {
			return CDSLForm{}, a, invalid("give the 16-digit CDSL BO ID of the other account")
		}
		if cdxReasonCodes[a.Reason] == "" {
			return CDSLForm{}, a, invalid("choose the reason for the transfer")
		}
		if a.ExecDate < today {
			return CDSLForm{}, a, invalid("the execution date cannot be in the past")
		}
		a.Exchange = strings.ToUpper(firstOf(strings.TrimSpace(in.Exchange), "NSE"))
	default:
		return CDSLForm{}, a, invalid("the request type is D (pre-trade), E (post-trade) or F (off-market)")
	}
	a.CMID = strings.TrimSpace(x.c.Values["edis_cm_"+strings.ToLower(a.Exchange)])
	if a.CMID == "" {
		return CDSLForm{}, a, invalid("enter the firm's %s clearing member ID for eDIS in the CDSL APIs connection", a.Exchange)
	}
	if len(in.Lines) == 0 || len(in.Lines) > edisMaxRecords {
		return CDSLForm{}, a, invalid("authorise 1 to %d ISINs at a time", edisMaxRecords)
	}
	a.ValidTill = today
	if a.ReqType == "D" {
		a.Days = x.edisDays()
		a.ValidTill = addDays(today, a.Days-1)
	}
	seen := map[string]bool{}
	defSettle := set.NextBusinessDay(today) // pay-in of a sale today (T+1)
	for _, l := range in.Lines {
		l.ISIN = strings.ToUpper(strings.TrimSpace(l.ISIN))
		if len(l.ISIN) != 12 || seen[l.ISIN] {
			return CDSLForm{}, a, invalid("give each ISIN once, 12 characters")
		}
		seen[l.ISIN] = true
		if l.Qty <= 0 {
			return CDSLForm{}, a, invalid("quantity must be above zero")
		}
		if free := o.freeBalance(ctx, tenant, c.UCC, l.ISIN); l.Qty > free {
			return CDSLForm{}, a, invalid("only %d free in %s (after pledges and pending instructions)", max(free, 0), l.ISIN)
		}
		el := EDISLine{ISIN: l.ISIN, Qty: l.Qty, Status: "Awaiting TPIN", SettleID: strings.TrimSpace(l.SettleID)}
		if len(el.SettleID) > 13 {
			return CDSLForm{}, a, invalid("a CDSL settlement ID has up to 13 characters")
		}
		if a.ReqType != "F" {
			el.SettleDate = firstOf(strings.TrimSpace(l.SettleDate), defSettle)
			if el.SettleDate < today {
				return CDSLForm{}, a, invalid("the settlement date cannot be in the past")
			}
		}
		if h, _, err := docs.Get[Holding](ctx, o.Docs, tenant, cHolding, holdingID(c.UCC, l.ISIN)); err == nil {
			el.Symbol = h.Symbol
		}
		if el.TxnReqID, err = o.cdxSeqID(ctx, tenant, "edistxn", 16); err != nil {
			return CDSLForm{}, a, err
		}
		a.Lines = append(a.Lines, el)
	}
	if a.ReqID, err = o.cdxSeqID(ctx, tenant, "edisreq", 16); err != nil {
		return CDSLForm{}, a, err
	}
	plain := edisTransDtls(x, a, pub+"/hooks/"+tenant+"/cdsl/edis/return", o.now())
	enc, err := cdxSeal(x, cdxKeyEDIS, plain)
	if err != nil {
		return CDSLForm{}, a, err
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cEDIS, a.ReqID, a, 0, actor); err != nil {
		return CDSLForm{}, a, err
	}
	o.log(ctx, tenant, actor, "edis.requested", a.ReqID, map[string]any{"ucc": a.UCC, "bo": cdslMaskBO(a.BOID), "type": a.ReqType, "isins": len(a.Lines), "via": via})
	return CDSLForm{Action: x.URL(edisPathVerify), Fields: map[string]string{"DPId": x.DPID, "ReqId": a.ReqID, "Version": a.Version, "TransDtls": enc}, Ref: a.ReqID,
		Note: "Enter your CDSL TPIN and the OTP CDSL sends to your registered mobile and e-mail."}, a, nil
}

// edisTransDtls is the TransDtls JSON in the handbook's field order:
// version 1.2 carries the settlement ID and date per ISIN and blank in the
// body; 1.1 in the body (§5.11.7).
func edisTransDtls(x cdslConn, a EDISAuth, ret string, now time.Time) []byte {
	type rec11 struct {
		TxnReqID string `json:"TxnReqId"`
		ISIN     string `json:"ISIN"`
		Quantity string `json:"Quantity"`
	}
	type rec12 struct {
		TxnReqID string `json:"TxnReqId"`
		ISIN     string `json:"ISIN"`
		Quantity string `json:"Quantity"`
		SettleID string `json:"SettleId"`
		ExecDate string `json:"ExecDate"`
	}
	v12 := a.Version == "1.2"
	exec := ""
	if a.ReqType == "F" {
		exec = edisDDMMYYYY(a.ExecDate)
	}
	var recs []any
	settleID, settleDate := "", exec
	for _, l := range a.Lines {
		sd := firstOf(edisDDMMYYYY(l.SettleDate), exec)
		if v12 {
			recs = append(recs, rec12{l.TxnReqID, l.ISIN, cdxQty(l.Qty), l.SettleID, sd})
		} else {
			recs = append(recs, rec11{l.TxnReqID, l.ISIN, cdxQty(l.Qty)})
			settleID, settleDate = firstOf(settleID, l.SettleID), firstOf(settleDate, sd)
		}
	}
	if v12 {
		settleID, settleDate = "", ""
	}
	days := ""
	if a.ReqType == "D" {
		days = strconv.Itoa(a.Days)
	}
	trRsn, ctr := "", ""
	if a.ReqType == "F" {
		trRsn, ctr = cdxReasonCodes[a.Reason], a.CtrBOID
	}
	b, _ := json.Marshal(struct {
		ReqType       string `json:"ReqType"`
		ReqIdentifier string `json:"ReqIdentifier"`
		ReqTime       string `json:"ReqTime"`
		ReturnURL     string `json:"ReturnURL"`
		DPID          string `json:"DPId"`
		BOID          string `json:"BOID"`
		CMID          string `json:"CMID"`
		TMID          string `json:"TMID"`
		ExID          string `json:"ExID"`
		NumOfDays     string `json:"NumOfDays"`
		SettleID      string `json:"SettleId"`
		CtrBOID       string `json:"CtrBOID"`
		TrRsnCode     string `json:"TrRsnCode"`
		ExecDate      string `json:"ExecDate"`
		Filler1       string `json:"Filler1"`
		Filler2       string `json:"Filler2"`
		RecordDtls    []any  `json:"RecordDtls"`
	}{a.ReqType, "S", cdxTime(now), ret, x.DPID, a.BOID, a.CMID, strings.TrimSpace(x.c.Values["edis_tm"]), cdxExchangeIDs[a.Exchange], days, settleID, ctr, trRsn, settleDate, "", "", recs})
	return b
}

// edisApply records CDSL's answer (the redirect's TransResDtls or the
// inquiry's body) on an authorisation. It reports whether the answer was
// final.
func edisApply(a *EDISAuth, m cdxObj) bool {
	recs := m.list("recordresdtls")
	code := m.str("reserror")
	if len(recs) == 0 && (code == "ERR058" || code == "") {
		return false // the client has not entered the TPIN yet
	}
	for _, r := range recs {
		if r.str("errorcode") == "ERR058" {
			return false
		}
	}
	a.ResID, a.ResError = firstOf(m.str("resid"), a.ResID), code
	for _, r := range recs {
		id := r.str("txnreqid")
		for i := range a.Lines {
			l := &a.Lines[i]
			if l.TxnReqID != id {
				continue
			}
			if r.str("status") == "0" {
				l.Status, l.TxnID, l.Error = "Authorised", r.str("txnid"), ""
			} else {
				l.Status, l.Error = "Failed", edisWords(r.str("errorcode"), m.str("remarks"))
			}
		}
	}
	ok := 0
	for i := range a.Lines {
		l := &a.Lines[i]
		if l.Status == "Awaiting TPIN" {
			l.Status, l.Error = "Failed", edisWords(code, firstOf(m.str("remarks"), "no answer for this ISIN"))
		}
		if l.Status == "Authorised" {
			ok++
		}
	}
	switch {
	case ok == len(a.Lines):
		a.Status = "Authorised"
	case ok > 0:
		a.Status = "Partly authorised"
	default:
		a.Status = "Failed"
	}
	return true
}

func edisShares(a EDISAuth, only string) string {
	var s []string
	for _, l := range a.Lines {
		if only == "" || l.Status == only {
			s = append(s, fmt.Sprintf("%s %d", firstOf(l.Symbol, l.ISIN), l.Qty))
		}
	}
	return firstOf(strings.Join(s, ", "), "none")
}

// edisRecord saves an answered authorisation and tells the client.
func (o *Ops) edisRecord(ctx context.Context, tenant string, a EDISAuth, how string) error {
	a.DoneAt, a.AnsweredBy = o.now().Format(time.RFC3339), how
	if err := docs.Upsert(ctx, o.Docs, tenant, cEDIS, a.ReqID, a, SystemActor); err != nil {
		return err
	}
	reason := ""
	for _, l := range a.Lines {
		if l.Status == "Failed" {
			reason = firstOf(reason, "Not authorised: "+firstOf(l.Symbol, l.ISIN)+": "+l.Error+".")
		}
	}
	shares := edisShares(a, "Authorised")
	if a.Status == "Failed" {
		shares = edisShares(a, "")
	}
	o.notify(ctx, tenant, SystemActor, "edis.authorised", a.UCC, map[string]string{"req": a.ReqID, "status": strings.ToLower(a.Status), "shares": shares, "valid_till": fmtDay(a.ValidTill), "reason": reason})
	o.closeWork(ctx, tenant, SystemActor, "cdsl", "edis:"+a.ReqID, a.Status)
	o.log(ctx, tenant, SystemActor, "edis.answered", a.ReqID, map[string]any{"ucc": a.UCC, "status": a.Status, "via": how, "error": a.ResError})
	return nil
}

// EDISReturn takes CDSL's redirect back after the client authorised (or
// not) on CDSL's page (§5.11.8). Not signed in: the details must decrypt
// under the DP's eDIS key, and the request ID inside must match the one
// posted and one sent from here.
func (o *Ops) EDISReturn(ctx context.Context, tenant string, form url.Values) (CDSLReturn, error) {
	set, _ := o.Settings(ctx, tenant)
	out := CDSLReturn{Firm: set.Firm, Title: "Sale authorisation (CDSL eDIS)", Back: "/#pedis", BackFor: "Back"}
	reqid := strings.TrimSpace(firstOf(form.Get("ReqId"), form.Get("reqid")))
	x, ok := o.cdxConn(ctx, tenant)
	a, _, err := docs.Get[EDISAuth](ctx, o.Docs, tenant, cEDIS, reqid)
	if err != nil || reqid == "" || !ok {
		return out, fmt.Errorf("%w: no eDIS request %q was sent from here", ErrNotFound, reqid)
	}
	if a.Via == "Staff" {
		out.Back = "/#edis"
	}
	m, err := cdxOpen(x, cdxKeyEDIS, firstOf(form.Get("transDtls"), firstOf(form.Get("TransDtls"), form.Get("TransResDtls"))))
	if err != nil {
		return out, fmt.Errorf("%w: CDSL's reply could not be verified", ErrInvalid)
	}
	m = m.in("transresdtls")
	if m.str("reqid") != reqid {
		return out, fmt.Errorf("%w: CDSL's reply is for another request", ErrInvalid)
	}
	if a.Status != "Awaiting TPIN" && a.Status != "No answer" {
		out.OK, out.Lines = a.Status == "Authorised" || a.Status == "Partly authorised", []string{"This authorisation was already recorded: " + strings.ToLower(a.Status) + "."}
		return out, nil
	}
	if !edisApply(&a, m) {
		out.Lines = []string{"CDSL has not finished the authorisation. If you closed CDSL's page, start again."}
		return out, nil
	}
	if err := o.edisRecord(ctx, tenant, a, "Redirect"); err != nil {
		return out, err
	}
	return edisReturnPage(out, a), nil
}

func edisReturnPage(out CDSLReturn, a EDISAuth) CDSLReturn {
	out.OK = a.Status == "Authorised" || a.Status == "Partly authorised"
	switch a.Status {
	case "Authorised":
		out.Lines = append(out.Lines, "CDSL recorded your authorisation "+a.ReqID+": "+edisShares(a, "Authorised")+".")
	case "Partly authorised":
		out.Lines = append(out.Lines, "CDSL recorded part of your authorisation "+a.ReqID+": "+edisShares(a, "Authorised")+".")
	default:
		out.Lines = append(out.Lines, "CDSL did not record the authorisation "+a.ReqID+".")
	}
	for _, l := range a.Lines {
		if l.Status == "Failed" {
			out.Lines = append(out.Lines, firstOf(l.Symbol, l.ISIN)+": "+l.Error+".")
		}
	}
	if out.OK && a.ReqType == "D" {
		out.Lines = append(out.Lines, "You can sell these shares until "+fmtDay(a.ValidTill)+".")
	}
	return out
}

// cdxPostForm sends a multipart form with headers to a CDSL server API
// (eDIS inquiry and TPIN generation take form data; §5.11.6).
func cdxPostForm(ctx context.Context, x cdslConn, path string, headers, fields map[string]string) (cdslResponse, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_ = mw.WriteField(k, fields[k])
	}
	_ = mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.URL(path), &buf)
	if err != nil {
		return cdslResponse{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json, text/plain, */*")
	for k, v := range headers {
		req.Header[k] = []string{v} // CDSL's names as written
	}
	resp, err := cdslHTTP.Do(req)
	if err != nil {
		return cdslResponse{}, cdslNetError(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	out := cdslResponse{Status: resp.StatusCode, Body: b}
	if err != nil {
		return out, fmt.Errorf("CDSL's answer was cut off: %v", err)
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return out, fmt.Errorf("CDSL refused the call (%d): the server's address may not be listed with CDSL", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return out, fmt.Errorf("CDSL answered HTTP %d", resp.StatusCode)
	}
	return out, nil
}

// EDISInquire asks CDSL for the status of a request whose redirect never
// came back (§5.12). CDSL answers only for the day's requests.
func (o *Ops) EDISInquire(ctx context.Context, tenant, actor, reqID string) (EDISAuth, error) {
	if err := o.require(ctx, tenant, actor, "Asking CDSL about eDIS authorisations", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return EDISAuth{}, err
	}
	a, _, err := docs.Get[EDISAuth](ctx, o.Docs, tenant, cEDIS, reqID)
	if err != nil {
		return a, err
	}
	if a.Status != "Awaiting TPIN" && a.Status != "No answer" {
		return a, nil
	}
	if a.At[:10] != o.now().Format("2006-01-02") {
		return a, fmt.Errorf("%w: CDSL answers inquiries only for the day's requests", ErrState)
	}
	x, err := o.edisConn(ctx, tenant)
	if err != nil {
		return a, err
	}
	now := o.now()
	body, _ := json.Marshal(struct {
		ReqType string `json:"ReqType"`
		ReqTime string `json:"ReqTime"`
	}{a.ReqType, cdxTime(now)})
	enc, err := cdxSeal(x, cdxKeyEDIS, body)
	if err != nil {
		return a, err
	}
	a.Inquiries, a.LastInquiry = a.Inquiries+1, now.Format(time.RFC3339)
	resp, err := cdxPostForm(ctx, x, edisPathInquiry, map[string]string{"DPID": x.DPID, "ReqId": a.ReqID, "Version": "1.0"}, map[string]string{"EncrypTransDtl": enc})
	if err != nil {
		_ = docs.Upsert(ctx, o.Docs, tenant, cEDIS, a.ReqID, a, actor)
		return a, err
	}
	m, err := cdxOpenReply(x, cdxKeyEDIS, resp.Body)
	if err != nil {
		_ = docs.Upsert(ctx, o.Docs, tenant, cEDIS, a.ReqID, a, actor)
		return a, err
	}
	m = m.in("transresdtls", "body")
	if got := m.str("reqid"); got != "" && got != a.ReqID {
		return a, fmt.Errorf("CDSL's answer is for another request")
	}
	if !edisApply(&a, m) {
		_ = docs.Upsert(ctx, o.Docs, tenant, cEDIS, a.ReqID, a, actor)
		return a, nil
	}
	return a, o.edisRecord(ctx, tenant, a, "Inquiry")
}

// GenerateTPIN asks CDSL to send the client a new TPIN (§5.13). Only for
// genuine requests: the client on the portal, or staff recording how the
// client asked.
func (o *Ops) GenerateTPIN(ctx context.Context, tenant, actor, ucc, note string) (EDISPin, error) {
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	via, err := o.edisWho(ctx, tenant, actor, ucc)
	if err != nil {
		return EDISPin{}, err
	}
	if via == "Staff" && strings.TrimSpace(note) == "" {
		return EDISPin{}, invalid("record how the client asked for a TPIN (call reference, letter, visit)")
	}
	x, err := o.edisConn(ctx, tenant)
	if err != nil {
		return EDISPin{}, err
	}
	c, b, err := o.edisClient(ctx, tenant, x, ucc)
	if err != nil {
		return EDISPin{}, err
	}
	pan := strings.ToUpper(strings.TrimSpace(c.PAN))
	if !rePAN.MatchString(pan) {
		return EDISPin{}, invalid("the client's PAN is not on record")
	}
	today := o.now().Format("2006-01-02")
	ps, _ := docs.List[EDISPin](ctx, o.Docs, tenant, cEDISPin, "")
	n := 0
	for _, p := range ps {
		if p.UCC == c.UCC && strings.HasPrefix(p.At, today) {
			n++
		}
	}
	if n >= edisPinPerDay {
		return EDISPin{}, invalid("a TPIN was already asked for %d times today; try tomorrow or call CDSL", n)
	}
	p := EDISPin{UCC: c.UCC, At: o.now().Format(time.RFC3339), By: actor, Via: via, Note: strings.TrimSpace(note)}
	if p.ReqID, err = o.cdxSeqID(ctx, tenant, "edispin", 16); err != nil {
		return p, err
	}
	// ReqFlag: the field list defines N (generate or regenerate).
	body, _ := json.Marshal(struct {
		BOID    string `json:"BOID"`
		PAN     string `json:"PAN"`
		ReqFlag string `json:"ReqFlag"`
		ReqTime string `json:"ReqTime"`
	}{b.BOID, pan, "N", cdxTime(o.now())})
	enc, err := cdxSeal(x, cdxKeyEDIS, body)
	if err != nil {
		return p, err
	}
	resp, err := cdxPostForm(ctx, x, edisPathPin, map[string]string{"DPId": x.DPID, "ReqId": p.ReqID, "Version": "1.0"}, map[string]string{"EncryptDtls": enc})
	if err != nil {
		p.Status, p.Error = "Error", err.Error()
	} else if m, err := cdxOpenReply(x, cdxKeyEDIS, resp.Body); err != nil {
		p.Status, p.Error = "Error", err.Error()
	} else {
		m = m.in("body")
		p.ResID = m.str("resid")
		if m.str("status") == "00" || m.str("status") == "0" {
			p.Status = "Sent to the client"
		} else {
			p.Status, p.Error = "Refused", edisWords(m.str("errorcode"), "CDSL did not generate the TPIN")
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cEDISPin, p.ReqID, p, actor); err != nil {
		return p, err
	}
	o.log(ctx, tenant, actor, "edis.tpin_generate", p.ReqID, map[string]any{"ucc": p.UCC, "bo": cdslMaskBO(b.BOID), "status": p.Status, "error": p.Error, "via": via})
	if p.Status != "Sent to the client" {
		return p, fmt.Errorf("%w: CDSL did not generate the TPIN: %s", ErrState, p.Error)
	}
	return p, nil
}

// ChangeTPINForm is the redirect to CDSL's change-PIN page with the BO ID
// and PAN filled in (§5.15). CDSL returns nothing.
func (o *Ops) ChangeTPINForm(ctx context.Context, tenant, actor, ucc string) (CDSLForm, error) {
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	via, err := o.edisWho(ctx, tenant, actor, ucc)
	if err != nil {
		return CDSLForm{}, err
	}
	x, err := o.edisConn(ctx, tenant)
	if err != nil {
		return CDSLForm{}, err
	}
	c, b, err := o.edisClient(ctx, tenant, x, ucc)
	if err != nil {
		return CDSLForm{}, err
	}
	body, _ := json.Marshal(struct {
		BOID string `json:"BOID"`
		PAN  string `json:"PAN"`
	}{b.BOID, strings.ToUpper(strings.TrimSpace(c.PAN))})
	enc, err := cdxSeal(x, cdxKeyEDIS, body)
	if err != nil {
		return CDSLForm{}, err
	}
	o.log(ctx, tenant, actor, "edis.tpin_change", c.UCC, map[string]any{"bo": cdslMaskBO(b.BOID), "via": via})
	return CDSLForm{Action: x.URL(edisPathChangePin), Fields: map[string]string{"dpId": x.DPID, "boDtls": enc}, Ref: c.UCC,
		Note: "Change the TPIN on CDSL's page. If the BO ID and PAN are not filled in, enter them there."}, nil
}

// StartEDISCancel builds the redirect that revokes the unused quantity of
// a pre-trade authorisation (§5.14). CDSL allows it 20 minutes after the
// authorisation was set up.
func (o *Ops) StartEDISCancel(ctx context.Context, tenant, actor, reqID, base string) (CDSLForm, error) {
	a, _, err := docs.Get[EDISAuth](ctx, o.Docs, tenant, cEDIS, reqID)
	if err != nil {
		return CDSLForm{}, err
	}
	via, err := o.edisWho(ctx, tenant, actor, a.UCC)
	if err != nil {
		return CDSLForm{}, err
	}
	x, err := o.edisConn(ctx, tenant)
	if err != nil {
		return CDSLForm{}, err
	}
	if a.ReqType != "D" {
		return CDSLForm{}, fmt.Errorf("%w: only pre-trade authorisations can be cancelled", ErrState)
	}
	if a.Status != "Authorised" && a.Status != "Partly authorised" {
		return CDSLForm{}, fmt.Errorf("%w: the authorisation is %s", ErrState, strings.ToLower(a.Status))
	}
	if at, err := time.Parse(time.RFC3339, firstOf(a.DoneAt, a.At)); err == nil && o.now().Sub(at) < edisCancelAfter {
		return CDSLForm{}, fmt.Errorf("%w: CDSL allows a cancellation 20 minutes after the authorisation; try after %s", ErrState, at.Add(edisCancelAfter).In(ist).Format("15:04"))
	}
	pub, err := edisReturnBase(x, base)
	if err != nil {
		return CDSLForm{}, err
	}
	r := EDISRevoc{ReqID: a.ReqID, UCC: a.UCC, Lines: map[string]string{}, Status: "Awaiting OTP", At: o.now().Format(time.RFC3339), By: actor}
	type rec struct {
		RevocReqTxnID string `json:"RevocReqTxnId"`
		ResID         string `json:"ResId"`
		TxnID         string `json:"TxnId"`
		ISIN          string `json:"ISIN"`
		Filler1       string `json:"Filler1"`
	}
	var recs []rec
	for _, l := range a.Lines {
		if l.Status != "Authorised" || l.TxnID == "" || l.Cancelled >= l.Qty {
			continue
		}
		id, err := o.cdxSeqID(ctx, tenant, "edisrevoctxn", 24)
		if err != nil {
			return CDSLForm{}, err
		}
		r.Lines[id] = l.TxnReqID
		recs = append(recs, rec{id, a.ResID, l.TxnID, l.ISIN, ""})
	}
	if len(recs) == 0 {
		return CDSLForm{}, fmt.Errorf("%w: nothing left to cancel", ErrState)
	}
	if r.RevocReqID, err = o.cdxSeqID(ctx, tenant, "edisrevoc", 24); err != nil {
		return CDSLForm{}, err
	}
	plain, _ := json.Marshal(struct {
		DPID         string `json:"DPId"`
		ReqType      string `json:"ReqType"`
		ReturnURL    string `json:"ReturnURL"`
		RevocReqTime string `json:"RevocReqTime"`
		BOID         string `json:"BOID"`
		RecordDtls   []rec  `json:"RecordDtls"`
	}{x.DPID, "D", pub + "/hooks/" + tenant + "/cdsl/edis/cancel-return", cdxTime(o.now()), a.BOID, recs})
	enc, err := cdxSeal(x, cdxKeyEDIS, plain)
	if err != nil {
		return CDSLForm{}, err
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cEDISRevoc, r.RevocReqID, r, 0, actor); err != nil {
		return CDSLForm{}, err
	}
	o.log(ctx, tenant, actor, "edis.cancel_requested", a.ReqID, map[string]any{"revoc": r.RevocReqID, "isins": len(recs), "via": via})
	return CDSLForm{Action: x.URL(edisPathCancel), Fields: map[string]string{"DPId": x.DPID, "Version": "1.0", "RevocReqId": r.RevocReqID, "RevokDtls": enc}, Ref: r.RevocReqID,
		Note: "Confirm the cancellation on CDSL's page with the OTP CDSL sends."}, nil
}

// EDISCancelReturn takes CDSL's post-back of a cancellation (§5.14.3),
// verified like the authorisation's.
func (o *Ops) EDISCancelReturn(ctx context.Context, tenant string, form url.Values) (CDSLReturn, error) {
	set, _ := o.Settings(ctx, tenant)
	out := CDSLReturn{Firm: set.Firm, Title: "Cancel sale authorisation (CDSL eDIS)", Back: "/#pedis", BackFor: "Back"}
	rid := strings.TrimSpace(firstOf(form.Get("RevocReqId"), form.Get("revocreqid")))
	x, ok := o.cdxConn(ctx, tenant)
	r, _, err := docs.Get[EDISRevoc](ctx, o.Docs, tenant, cEDISRevoc, rid)
	if err != nil || rid == "" || !ok {
		return out, fmt.Errorf("%w: no eDIS cancellation %q was sent from here", ErrNotFound, rid)
	}
	m, err := cdxOpen(x, cdxKeyEDIS, firstOf(form.Get("TransResDtls"), form.Get("transResDtls")))
	if err != nil {
		return out, fmt.Errorf("%w: CDSL's reply could not be verified", ErrInvalid)
	}
	m = m.in("transresdtls")
	if m.str("revocreqid") != rid {
		return out, fmt.Errorf("%w: CDSL's reply is for another cancellation", ErrInvalid)
	}
	if r.Status != "Awaiting OTP" {
		out.Lines = []string{"This cancellation was already recorded: " + strings.ToLower(r.Status) + "."}
		return out, nil
	}
	a, _, err := docs.Get[EDISAuth](ctx, o.Docs, tenant, cEDIS, r.ReqID)
	if err != nil {
		return out, err
	}
	var done, failed []string
	for _, rec := range m.list("recordresdtls") {
		txnReq := r.Lines[rec.str("revocreqtxnid")]
		for i := range a.Lines {
			l := &a.Lines[i]
			if l.TxnReqID != txnReq || l.TxnID != rec.str("txnid") {
				continue
			}
			if rec.str("status") != "0" {
				failed = append(failed, firstOf(l.Symbol, l.ISIN)+": "+edisWords(rec.str("errorcode"), rec.str("errormsg")))
				continue
			}
			q, _ := strconv.ParseFloat(rec.str("cancquantity"), 64)
			l.Cancelled = min(l.Qty, l.Cancelled+int64(q))
			if l.Cancelled >= l.Qty || int64(q) == 0 {
				l.Status = "Cancelled"
			}
			done = append(done, fmt.Sprintf("%s %d", firstOf(l.Symbol, l.ISIN), int64(q)))
		}
	}
	live := 0
	for _, l := range a.Lines {
		if l.Status == "Authorised" {
			live++
		}
	}
	if live == 0 && len(done) > 0 {
		a.Status = "Cancelled"
	}
	r.Status, r.Result = "Done", strings.Join(append(done, failed...), "; ")
	if len(done) == 0 {
		r.Status = "Failed"
		if r.Result == "" {
			r.Result = edisWords(m.str("reserror"), m.str("reserrormsg"))
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cEDIS, a.ReqID, a, SystemActor); err != nil {
		return out, err
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cEDISRevoc, r.RevocReqID, r, SystemActor)
	if len(done) > 0 {
		o.notify(ctx, tenant, SystemActor, "edis.cancelled", a.UCC, map[string]string{"req": a.ReqID, "shares": strings.Join(done, ", ")})
		out.OK, out.Lines = true, []string{"CDSL cancelled: " + strings.Join(done, ", ") + "."}
	}
	for _, f := range failed {
		out.Lines = append(out.Lines, "Not cancelled: "+f+".")
	}
	if len(out.Lines) == 0 {
		out.Lines = []string{"CDSL did not cancel the authorisation: " + r.Result + "."}
	}
	o.log(ctx, tenant, SystemActor, "edis.cancel_result", a.ReqID, map[string]any{"revoc": r.RevocReqID, "status": r.Status, "result": r.Result})
	return out, nil
}

// ---- live risk ----

// edisDeliverable is, when eDIS is on, which clients sell through eDIS
// (an active CDSL account at this DP with no DDPI) and the quantity they
// have authorised that is valid on date, by UCC|symbol and UCC|ISIN.
func (o *Ops) edisDeliverable(ctx context.Context, tenant, date string) (map[string]bool, map[string]int64) {
	x, ok := o.cdxConn(ctx, tenant)
	if !ok || !x.On(cdslFamEDIS) {
		return nil, nil
	}
	need := map[string]bool{}
	bos, _ := docs.List[BOAccount](ctx, o.Docs, tenant, cBO, "")
	byUCC := map[string]BOAccount{}
	for _, b := range bos {
		byUCC[b.UCC] = b
	}
	cs, _ := o.ClientMap(ctx, tenant)
	for u, c := range cs {
		b, ok := byUCC[u]
		if !ok { // as o.bo reads an account with no DP record
			b = BOAccount{UCC: u, BOID: c.Demat.ID, Depository: c.Demat.Depository, Status: "Active"}
		}
		if !b.DDPI && b.Status != "Closed" && (b.Depository == "" || strings.EqualFold(b.Depository, "CDSL")) && cdxBODP(b.BOID) == strings.TrimLeft(x.DPID, "0") {
			need[u] = true
		}
	}
	qty := map[string]int64{}
	as, _ := docs.List[EDISAuth](ctx, o.Docs, tenant, cEDIS, "")
	for _, a := range as {
		if (a.Status != "Authorised" && a.Status != "Partly authorised") || a.ReqType == "F" || a.At[:10] > date || a.ValidTill < date {
			continue
		}
		for _, l := range a.Lines {
			if l.Status != "Authorised" {
				continue
			}
			q := l.Qty - l.Cancelled
			if l.Symbol != "" {
				qty[a.UCC+"|"+l.Symbol] += q
			}
			qty[a.UCC+"|"+l.ISIN] += q
		}
	}
	return need, qty
}

// ---- screens ----

// EDISView is the eDIS authorisations tab.
type EDISView struct {
	On        bool        `json:"on"`
	Version   string      `json:"version"`
	Env       string      `json:"environment"`
	Auths     []EDISAuth  `json:"auths"`
	Pins      []EDISPin   `json:"pins"`
	Revocs    []EDISRevoc `json:"revocations"`
	Waiting   int         `json:"waiting"`
	Today     int         `json:"today"`
	Reasons   []string    `json:"reasons"`
	CMMissing []string    `json:"cm_missing,omitempty"`
}

// EDISScreen lists the eDIS authorisations, TPIN requests and
// cancellations.
func (o *Ops) EDISScreen(ctx context.Context, tenant, actor, date string) (EDISView, error) {
	if err := o.require(ctx, tenant, actor, "Viewing eDIS authorisations", RoleMaker, RoleChecker, RoleRisk, RoleCompliance, RoleBranch); err != nil {
		return EDISView{}, err
	}
	v := EDISView{Auths: []EDISAuth{}, Pins: []EDISPin{}, Revocs: []EDISRevoc{}, Reasons: OffMarketReasons}
	if x, ok := o.cdxConn(ctx, tenant); ok {
		v.On, v.Version, v.Env = x.On(cdslFamEDIS), x.edisVersion(), x.Env
		for _, e := range []string{"NSE", "BSE"} {
			if strings.TrimSpace(x.c.Values["edis_cm_"+strings.ToLower(e)]) == "" {
				v.CMMissing = append(v.CMMissing, e)
			}
		}
	}
	as, _ := docs.List[EDISAuth](ctx, o.Docs, tenant, cEDIS, "")
	sort.Slice(as, func(i, j int) bool { return as[i].ReqID > as[j].ReqID })
	for _, a := range as {
		if a.Status == "Awaiting TPIN" {
			v.Waiting++
		}
		if strings.HasPrefix(a.At, date) {
			v.Today++
		}
	}
	if len(as) > 300 {
		as = as[:300]
	}
	v.Auths = append(v.Auths, as...)
	ps, _ := docs.List[EDISPin](ctx, o.Docs, tenant, cEDISPin, "")
	sort.Slice(ps, func(i, j int) bool { return ps[i].ReqID > ps[j].ReqID })
	if len(ps) > 100 {
		ps = ps[:100]
	}
	v.Pins = append(v.Pins, ps...)
	rs, _ := docs.List[EDISRevoc](ctx, o.Docs, tenant, cEDISRevoc, "")
	sort.Slice(rs, func(i, j int) bool { return rs[i].RevocReqID > rs[j].RevocReqID })
	v.Revocs = append(v.Revocs, rs...)
	return v, nil
}

// PortalEDISHolding is a holding the client can authorise.
type PortalEDISHolding struct {
	ISIN       string `json:"isin"`
	Symbol     string `json:"symbol"`
	Free       int64  `json:"free"`
	Authorised int64  `json:"authorised"`
}

// PortalEDISView is the client's eDIS page.
type PortalEDISView struct {
	UCC        string              `json:"ucc"`
	On         bool                `json:"on"`
	DDPI       bool                `json:"ddpi"`
	BOID       string              `json:"bo_id"`
	Problem    string              `json:"problem,omitempty"`
	Holdings   []PortalEDISHolding `json:"holdings"`
	Auths      []EDISAuth          `json:"auths"`
	Exchanges  []string            `json:"exchanges"`
	SettleDate string              `json:"settle_date"`
	ValidDays  int                 `json:"valid_days"`
}

// PortalEDIS is the client's eDIS page: holdings that can be authorised
// and the authorisations given.
func (o *Ops) PortalEDIS(ctx context.Context, tenant, actor, ucc string) (PortalEDISView, error) {
	accts, err := o.PortalAccounts(ctx, tenant, actor)
	if err != nil {
		return PortalEDISView{}, err
	}
	ucc = strings.ToUpper(strings.TrimSpace(ucc))
	if ucc == "" && len(accts) > 0 {
		ucc = accts[0]
	}
	if !contains(accts, ucc) {
		return PortalEDISView{}, ErrNotFound
	}
	v := PortalEDISView{UCC: ucc, Holdings: []PortalEDISHolding{}, Auths: []EDISAuth{}, Exchanges: []string{}}
	x, err := o.edisConn(ctx, tenant)
	if err != nil {
		v.Problem = "Sale authorisation through CDSL is not available yet."
		return v, nil
	}
	v.On, v.ValidDays = true, x.edisDays()
	for _, e := range []string{"NSE", "BSE"} {
		if strings.TrimSpace(x.c.Values["edis_cm_"+strings.ToLower(e)]) != "" {
			v.Exchanges = append(v.Exchanges, e)
		}
	}
	c, b, err := o.edisClient(ctx, tenant, x, ucc)
	if err != nil {
		v.Problem = strings.TrimPrefix(strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "), ErrState.Error()+": ")
	}
	v.DDPI, v.BOID = b.DDPI, cdslMaskBO(b.BOID)
	set, _ := o.Settings(ctx, tenant)
	today := o.now().Format("2006-01-02")
	v.SettleDate = set.NextBusinessDay(today)
	_, auth := o.edisDeliverable(ctx, tenant, today)
	hs, _ := o.Holdings(ctx, tenant, c.UCC)
	for _, h := range hs {
		if f := o.freeBalance(ctx, tenant, c.UCC, h.ISIN); f > 0 {
			v.Holdings = append(v.Holdings, PortalEDISHolding{ISIN: h.ISIN, Symbol: h.Symbol, Free: f, Authorised: auth[c.UCC+"|"+h.ISIN]})
		}
	}
	as, _ := docs.List[EDISAuth](ctx, o.Docs, tenant, cEDIS, "")
	sort.Slice(as, func(i, j int) bool { return as[i].ReqID > as[j].ReqID })
	for _, a := range as {
		if a.UCC == c.UCC && len(v.Auths) < 50 {
			a.BOID = cdslMaskBO(a.BOID)
			v.Auths = append(v.Auths, a)
		}
	}
	return v, nil
}

// ---- the scheduler ----

// edisTick asks CDSL about the day's requests left without an answer (10
// minutes on, up to 3 times) and closes earlier days' as unanswered.
func (o *Ops) edisTick(ctx context.Context, tenant string, now time.Time) {
	today := now.In(ist).Format("2006-01-02")
	as, _ := docs.List[EDISAuth](ctx, o.Docs, tenant, cEDIS, "")
	for _, a := range as {
		if a.Status != "Awaiting TPIN" {
			continue
		}
		at, err := time.Parse(time.RFC3339, a.At)
		if err != nil {
			continue
		}
		if a.At[:10] < today {
			_, _ = docs.Update(ctx, o.Docs, tenant, cEDIS, a.ReqID, SystemActor, func(y *EDISAuth) error {
				if y.Status == "Awaiting TPIN" {
					y.Status = "No answer"
				}
				return nil
			})
			continue
		}
		if now.Sub(at) < edisInquireAfter || a.Inquiries >= 3 {
			continue
		}
		if last, err := time.Parse(time.RFC3339, a.LastInquiry); err == nil && now.Sub(last) < edisInquireAfter {
			continue
		}
		if _, err := o.EDISInquire(ctx, tenant, SystemActor, a.ReqID); err != nil && a.Inquiries >= 2 {
			o.raiseWork(ctx, tenant, "cdsl", "edis:"+a.ReqID, "eDIS authorisation "+a.ReqID+" has no answer from CDSL", a.UCC+": "+err.Error(), a.UCC, RoleMaker, "edis", today)
		}
	}
}
