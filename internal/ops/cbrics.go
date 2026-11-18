package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
)

// ---- NSE CBRICS (corporate bond reporting and settlement) ----
//
// Corporate bond deals struck over the counter are reported to NSE's CBRICS
// (Corporate Bond Reporting and Integrated Clearing and Settlement) and
// settled through it, delivery against payment with NSE Clearing. A broker
// reports its clients' deals there, keeps the clients who are not CBRICS
// participants of their own as "unregistered participants", and gives the
// bank and demat accounts each settlement pays out to. This file is the
// connection and the protocol, "Protocol for WEB API, CBRICS Application",
// version 1.16 (16 February 2026); cbricsdeals.go is what Ledgerline does
// with it.
//
//   - Common API: POST /rest/v1/login (domain = participant code, login,
//     password; newPassword1 when status P says the password expired) gives
//     a loginKey, sent as the "loginKey" header on every other call. A 401
//     means the session is gone (expired, idle, or a second login with the
//     same user): Ledgerline logs in once more and repeats the call. A
//     second login ends the first session, so only one server may use an
//     API user. GET /rest/v1/logout closes it.
//   - Errors come as {"code", "messages"}: each message a string (a business
//     rule) or {"field", "msg"} (a field that failed validation).
//   - Dates are dd-MM-yyyy, times HH:mm, in IST.
//   - Where a section's heading and its method table disagree (order/status
//     and participant/find say POST and PUT, order/buyinstructions GET and
//     POST, unreg/{id} GET and POST), Ledgerline calls with the heading's
//     method and, if CBRICS answers 405 (method not allowed), once with the
//     table's.
//
// Base addresses (Environment Details): testing
// https://bricsonlinereguat.nseindia.com/bondsnew, live
// https://bricsonline.nseindia.com/bondsnew. API access (Enabling API): the
// firm's CBRICS administrator adds a Dealer user under User Management and
// NSE converts it to an API user from the participant code and login ID;
// for notifications the firm gives NSE the callback base address.

const (
	connCBRICS   = "nse-cbrics"
	cbricsLive   = "https://bricsonline.nseindia.com/bondsnew"
	cbricsUAT    = "https://bricsonlinereguat.nseindia.com/bondsnew"
	cbricsDate   = "02-01-2006" // dd-MM-yyyy
	cbricsSpecil = "~!@#$*()_+-:;"
	// cbricsMinutes is Ledgerline's default reporting time limit: the
	// protocol sets none, and SEBI's rule for OTC corporate bond deals is
	// reporting within 15 minutes of the deal.
	cbricsMinutes = 15
)

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connCBRICS, Name: "NSE CBRICS (corporate bonds)", Group: "Exchanges", Test: "https",
		Purpose: "Reports clients' over-the-counter corporate bond deals to NSE's CBRICS, keeps clients without a CBRICS account of their own as unregistered participants, reads deal status and settlements, and gives each settlement the client's bank and demat account. The firm's CBRICS administrator adds a Dealer user and NSE makes it an API user (participant code and login ID); give NSE the callback address for notifications",
		Fields: []ConnField{{Key: "member", Label: "Participant code (the CBRICS domain)"}, {Key: "login", Label: "Login ID of the API user"},
			{Key: "password", Label: "Password", Secret: true},
			{Key: "next_password", Label: "Next password, used when CBRICS says the password has expired (12 to 15 characters with a small letter, a capital letter, a digit and one of ~!@#$*()_+-:;)", Secret: true, Optional: true},
			{Key: "env", Label: "Environment", Options: []string{"Live", "Testing (UAT)"}},
			{Key: "url", Label: "Address (leave empty for NSE's)", Optional: true, Placeholder: cbricsLive},
			{Key: "deal_type", Label: "Deal type reported (empty: Brokered)", Optional: true, Options: []string{"Brokered", "Direct"}},
			{Key: "send_for", Label: "Buyer confirmation of a sale reported (empty: Mandatory)", Optional: true, Options: []string{"Mandatory", "Optional"}},
			{Key: "minutes", Label: "Reporting time limit after the deal, in minutes (empty: 15)", Optional: true, Placeholder: "15"},
			{Key: "callback_key", Label: "Callback key from NSE (checks the token header on NSE's notifications)", Secret: true, Optional: true},
			{Key: "callback_ips", Label: "Addresses NSE calls back from (comma separated; empty allows any)", Optional: true},
			{Key: "auto", Label: "Read deals and settlements every 10 minutes", Optional: true, Options: []string{"On", "Off"}}, fNotes}})
}

// CBRICSDoer sends the HTTP requests; *http.Client is one. Ops.CBRICS
// replaces it in tests.
type CBRICSDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var cbricsDefaultHTTP = &http.Client{Timeout: 60 * time.Second}

func (o *Ops) cbricsHTTP() CBRICSDoer {
	if o.CBRICS != nil {
		return o.CBRICS
	}
	return cbricsDefaultHTTP
}

// CBRICSConfig is the login.
type CBRICSConfig struct{ Base, Member, Login, Password, NextPassword string }

type cbricsConn struct {
	cfg      CBRICSConfig
	live     bool
	dealType string // B brokered, D direct
	sendFor  string // M mandatory, C optional buyer confirmation
	minutes  int
	cbKey    string
	ips      []string
	auto     bool
}

func parseCBRICSConn(c Connection) (cbricsConn, error) {
	x := cbricsConn{cfg: CBRICSConfig{Member: strings.ToUpper(strings.TrimSpace(c.Values["member"])), Login: strings.TrimSpace(c.Values["login"]),
		Password: c.Secrets["password"], NextPassword: c.Secrets["next_password"]},
		live: c.Values["env"] != "Testing (UAT)", dealType: "B", sendFor: "M", minutes: cbricsMinutes, cbKey: c.Secrets["callback_key"], auto: c.Values["auto"] != "Off"}
	x.cfg.Base = map[bool]string{true: cbricsLive, false: cbricsUAT}[x.live]
	if u := strings.TrimRight(strings.TrimSpace(c.Values["url"]), "/"); u != "" {
		x.cfg.Base = u
	}
	if c.Values["deal_type"] == "Direct" {
		x.dealType = "D"
	}
	if c.Values["send_for"] == "Optional" {
		x.sendFor = "C"
	}
	if m := strings.TrimSpace(c.Values["minutes"]); m != "" {
		n, err := strconv.Atoi(m)
		if err != nil || n < 1 || n > 24*60 {
			return x, invalid("the reporting time limit is a number of minutes, 1 to 1440")
		}
		x.minutes = n
	}
	for _, ip := range strings.Split(c.Values["callback_ips"], ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			x.ips = append(x.ips, ip)
		}
	}
	if len(x.cfg.Member) > 10 || len(x.cfg.Login) > 10 {
		return x, invalid("the CBRICS participant code and login ID are up to 10 characters")
	}
	return x, nil
}

// checkCBRICSConn is SaveConnection's check of the CBRICS connection.
func checkCBRICSConn(c Connection) error {
	if _, err := parseCBRICSConn(c); err != nil {
		return err
	}
	if p := c.Secrets["next_password"]; p != "" {
		if msg := cbricsPasswordProblem(p, c.Secrets["password"]); msg != "" {
			return invalid("%s", msg)
		}
	}
	return nil
}

// cbricsPasswordProblem checks a new password against Annexure 1 (Password
// Policy): 12 to 15 characters, at least one each of a-z, A-Z, 0-9 and
// ~!@#$*()_+-:;, and not the current one. CBRICS itself also refuses any
// of the last 5 passwords, and a password lapses after 365 days.
func cbricsPasswordProblem(p, old string) string {
	if n := len(p); n < 12 || n > 15 {
		return "a CBRICS password is 12 to 15 characters long"
	}
	var lo, up, dig, sp bool
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z':
			lo = true
		case r >= 'A' && r <= 'Z':
			up = true
		case r >= '0' && r <= '9':
			dig = true
		case strings.ContainsRune(cbricsSpecil, r):
			sp = true
		}
	}
	if !lo || !up || !dig || !sp {
		return "a CBRICS password needs a small letter, a capital letter, a digit and one of " + cbricsSpecil
	}
	if p == old {
		return "the next CBRICS password must differ from the current one"
	}
	return ""
}

func (o *Ops) cbricsConnection(ctx context.Context, tenant string) (cbricsConn, error) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connCBRICS)
	if err != nil || c.Status == "Not set" {
		return cbricsConn{}, fmt.Errorf("%w: set up NSE CBRICS (corporate bonds) in Settings → Connections", ErrState)
	}
	x, err := parseCBRICSConn(c)
	if err != nil {
		return x, err
	}
	if x.cfg.Member == "" || x.cfg.Login == "" || x.cfg.Password == "" {
		return x, invalid("the NSE CBRICS connection needs the participant code, login ID and password")
	}
	return x, nil
}

// CBRICSError is CBRICS's common error response: the HTTP code and its
// messages in words.
type CBRICSError struct {
	Code     int
	Messages []string
}

func (e *CBRICSError) Error() string {
	return fmt.Sprintf("CBRICS refused it (%d): %s", e.Code, strings.Join(e.Messages, "; "))
}

func cbricsErr(code int, b []byte) error {
	var r struct {
		Code     int
		Messages []json.RawMessage
	}
	e := &CBRICSError{Code: code}
	if json.Unmarshal(b, &r) == nil {
		for _, m := range r.Messages {
			var s string
			if json.Unmarshal(m, &s) == nil {
				e.Messages = append(e.Messages, s)
				continue
			}
			var f struct{ Field, Msg string }
			if json.Unmarshal(m, &f) == nil {
				e.Messages = append(e.Messages, strings.TrimSpace(f.Field+": "+f.Msg))
			}
		}
	}
	if len(e.Messages) == 0 {
		e.Messages = []string{firstOf(trimBody(b), http.StatusText(code))}
	}
	if code == 500 {
		e.Messages = append(e.Messages, "an application error at CBRICS: report it to NSE's support desk")
	}
	return e
}

// cbricsCall is one conversation with CBRICS for a firm.
type cbricsCall struct {
	o      *Ops
	ctx    context.Context
	tenant string
	x      cbricsConn
}

func (o *Ops) cbricsCall(ctx context.Context, tenant string) (*cbricsCall, error) {
	x, err := o.cbricsConnection(ctx, tenant)
	if err != nil {
		return nil, err
	}
	return &cbricsCall{o: o, ctx: ctx, tenant: tenant, x: x}, nil
}

func (c *cbricsCall) keyID() string {
	h := sha256.Sum256([]byte(c.x.cfg.Password))
	return c.tenant + "|" + c.x.cfg.Base + "|" + c.x.cfg.Member + "|" + c.x.cfg.Login + "|" + hex.EncodeToString(h[:8])
}

func (c *cbricsCall) post(method, path string, body []byte, loginKey string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(c.ctx, method, c.x.cfg.Base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Ledgerline")
	if loginKey != "" {
		req.Header.Set("loginKey", loginKey)
	}
	resp, err := c.o.cbricsHTTP().Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("could not reach NSE's CBRICS: %v", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return resp.StatusCode, out, err
}

// cbricsLogin is the login reply.
type cbricsLogin struct {
	Status, Reason, LoginKey, OwnerCode, Login, FirstName string
	ServerTime                                            json.Number
}

// login gives the session's login key, logging in when there is none (or
// fresh asks for a new one). A password CBRICS says has expired is changed
// to the next password when one is entered; that then becomes the
// password.
func (c *cbricsCall) login(fresh bool) (string, error) {
	k := c.keyID()
	if v, ok := c.o.cbricsKeys.Load(k); ok && !fresh {
		return v.(string), nil
	}
	body := map[string]string{"domain": c.x.cfg.Member, "login": c.x.cfg.Login, "password": c.x.cfg.Password}
	r, err := c.loginOnce(body)
	if err != nil {
		return "", err
	}
	if r.Status == "P" {
		if c.x.cfg.NextPassword == "" {
			c.o.raiseWork(c.ctx, c.tenant, "cbrics", "password:"+c.o.now().Format("2006-01-02"), "CBRICS password has expired",
				"NSE's CBRICS says the API user's password has expired. Enter the next password in Settings → Connections → NSE CBRICS (12 to 15 characters with a small letter, a capital letter, a digit and one of "+cbricsSpecil+", not one of the last 5); Ledgerline changes it at the next login.",
				"", RoleAdmin, "connections", "")
			return "", fmt.Errorf("%w: CBRICS says the password has expired: enter the next password in Settings → Connections", ErrState)
		}
		body["newPassword1"] = c.x.cfg.NextPassword
		if r, err = c.loginOnce(body); err != nil {
			return "", err
		}
		if r.Status == "S" {
			c.o.cbricsPasswordChanged(c.ctx, c.tenant, c.x.cfg.NextPassword)
			c.x.cfg.Password, c.x.cfg.NextPassword = c.x.cfg.NextPassword, ""
			k = c.keyID()
		}
	}
	switch {
	case r.Status == "S" && r.LoginKey != "":
		c.o.cbricsKeys.Store(k, r.LoginKey)
		return r.LoginKey, nil
	case r.Status == "P":
		return "", fmt.Errorf("%w: CBRICS did not take the next password: %s", ErrState, firstOf(r.Reason, "check it against the password policy"))
	}
	return "", fmt.Errorf("%w: CBRICS refused the login: %s", ErrState, firstOf(r.Reason, "check the participant code, login ID and password"))
}

func (c *cbricsCall) loginOnce(body map[string]string) (cbricsLogin, error) {
	b, _ := json.Marshal(body)
	code, out, err := c.post(http.MethodPost, "/rest/v1/login", b, "")
	if err != nil {
		return cbricsLogin{}, err
	}
	var r cbricsLogin
	if code != 200 || json.Unmarshal(out, &r) != nil {
		if code >= 400 {
			return r, cbricsErr(code, out)
		}
		return r, fmt.Errorf("NSE's CBRICS login answered %d: %s", code, trimBody(out))
	}
	return r, nil
}

// cbricsPasswordChanged makes the next password the password once CBRICS
// took it.
func (o *Ops) cbricsPasswordChanged(ctx context.Context, tenant, pw string) {
	_, _ = docs.Update(ctx, o.Docs, tenant, cConn, connCBRICS, SystemActor, func(c *Connection) error {
		if c.Secrets == nil {
			c.Secrets = map[string]string{}
		}
		c.Secrets["password"] = pw
		delete(c.Secrets, "next_password")
		c.UpdatedAt, c.UpdatedBy = o.now().Format(time.RFC3339), SystemActor
		return nil
	})
	o.closeWork(ctx, tenant, SystemActor, "cbrics", "password:"+o.now().Format("2006-01-02"), "the next password was set at CBRICS")
	o.log(ctx, tenant, SystemActor, "cbrics.password_changed", connCBRICS, nil)
}

// do calls the API, logging in (again, once) as needed. alt is the other
// method a section's table names, tried once on 405.
func (c *cbricsCall) do(method, alt, path string, body, out any) error {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	for try := 0; try < 2; try++ {
		key, err := c.login(try > 0)
		if err != nil {
			return err
		}
		code, reply, err := c.post(method, path, b, key)
		if err != nil {
			return err
		}
		if code == http.StatusMethodNotAllowed && alt != "" {
			if code, reply, err = c.post(alt, path, b, key); err != nil {
				return err
			}
		}
		if code == http.StatusUnauthorized && try == 0 {
			continue // the session lapsed or another login ended it
		}
		if code >= 300 {
			return cbricsErr(code, reply)
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(reply, out); err != nil {
			return fmt.Errorf("NSE's CBRICS answered with something else than the protocol's reply: %s", trimBody(reply))
		}
		return nil
	}
	return fmt.Errorf("NSE's CBRICS did not take a fresh login")
}

// logout closes the session (GET /rest/v1/logout).
func (c *cbricsCall) logout() {
	k := c.keyID()
	v, ok := c.o.cbricsKeys.LoadAndDelete(k)
	if !ok {
		return
	}
	_, _, _ = c.post(http.MethodGet, "/rest/v1/logout", nil, v.(string))
}

// ---- the protocol's structures ----

// CBRICSOrder is a deal reporting by the seller or a buyer standing
// instruction (Order API: POST /rest/v1/order and its replies).
type CBRICSOrder struct {
	ID                json.Number `json:"id,omitempty"`
	OrderNumber       flexStr     `json:"orderNumber,omitempty"`
	StandingInstFlag  string      `json:"standingInstFlag"`
	DealType          string      `json:"dealType"`
	BuyerDealType     string      `json:"buyerDealType,omitempty"`
	Symbol            string      `json:"symbol"`
	BuyParticipant    string      `json:"buyParticipantLoginId"`
	SellParticipant   string      `json:"sellParticipantLoginId"`
	Price             json.Number `json:"price"`
	Value             json.Number `json:"value"`
	AccrInt           json.Number `json:"accrInt"`
	Settle            json.Number `json:"settle"`
	SendFor           string      `json:"sendFor"`
	YieldType         string      `json:"yieldType"`
	Yield             json.Number `json:"yield"`
	TradeDate         string      `json:"tradeDate"`
	TradeTime         string      `json:"tradeTime"`
	SellerRefNo       string      `json:"sellerRefNo,omitempty"`
	BuyerRefNo        string      `json:"buyerRefNo,omitempty"`
	PutCallDate       string      `json:"putCallDate,omitempty"`
	SellerYieldReason string      `json:"sellerYieldViolationReason,omitempty"`
	BuyerYieldReason  string      `json:"buyerYieldViolationReason,omitempty"`
	// In replies.
	Message        string      `json:"message,omitempty"`
	Quantity       json.Number `json:"quantity,omitempty"`
	Consideration  json.Number `json:"consideration,omitempty"`
	BuyBackoffice  string      `json:"buyBackofficeLoginId,omitempty"`
	SellBackoffice string      `json:"sellBackofficeLoginId,omitempty"`
	Status         json.Number `json:"status,omitempty"`
}

// CBRICSSettle is an order for settlement (Settlement API).
type CBRICSSettle struct {
	ID               json.Number `json:"id"`
	OrderNumber      flexStr     `json:"orderNumber"`
	Symbol           string      `json:"symbol"`
	BuyParticipant   string      `json:"buyParticipantLoginId"`
	SellParticipant  string      `json:"sellParticipantLoginId"`
	Price            json.Number `json:"price"`
	YieldType        string      `json:"yieldType"`
	Yield            json.Number `json:"yield"`
	Value            json.Number `json:"value"`
	BuyerRefNo       string      `json:"buyerRefNo"`
	SellerRefNo      string      `json:"sellerRefNo"`
	BuyBackoffice    string      `json:"buyBackofficeLoginId"`
	SellBackoffice   string      `json:"sellBackofficeLoginId"`
	BuyBroker        string      `json:"buyBrokerLoginId"`
	SellBroker       string      `json:"sellBrokerLoginId"`
	Source           json.Number `json:"source"`
	SettleDate       string      `json:"modSettleDate"`
	Quantity         json.Number `json:"modQuantity"`
	AccrInt          json.Number `json:"modAccrInt"`
	Consideration    json.Number `json:"modConsideration"`
	SettlementNo     flexStr     `json:"settlementNo"`
	StampDuty        json.Number `json:"stampDutyAmount"`
	StampDutyBearer  string      `json:"stampDutyBearer"`
	BuyerPayin       json.Number `json:"buyerFundPayinObligation"`
	SellerPayout     json.Number `json:"sellerFundPayoutObligation"`
	FundPayinRefID   string      `json:"fundPayinRefId"`
	SettleStatus     json.Number `json:"settleStatus"`
	SecPayinQuantity json.Number `json:"secPayinQuantity"`
	SecPayinRemarks  string      `json:"secPayinRemarks"`
	SecPayinTime     string      `json:"secPayinTime"`
	FundsPayinAmount json.Number `json:"fundsPayinAmount"`
	FundsPayinRemark string      `json:"fundsPayinRemarks"`
	FundsPayinTime   string      `json:"fundsPayinTime"`
	PayoutRemarks    string      `json:"payoutRemarks"`
	PayoutTime       string      `json:"payoutTime"`
	IFSC             string      `json:"ifscCode"`
	AccountNo        string      `json:"accountNo"`
	UTR              string      `json:"utrNumber"`
	DPID             string      `json:"dpId"`
	BenID            flexStr     `json:"benId"`
}

// CBRICSBankAcc is an unregistered participant's bank account (Bank
// Account Structure; POST /rest/v1/unreg/bankacc).
type CBRICSBankAcc struct {
	BankName        string      `json:"bankName"`
	BankIFSC        string      `json:"bankIFSC"`
	BankAccountNo   string      `json:"bankAccountNo,omitempty"` // none: an interbank account
	IsDefault       string      `json:"isDefault"`
	ParticipantCode string      `json:"participantCode,omitempty"`
	Status          string      `json:"status,omitempty"` // A active, S suspended, D deleted
	WorkflowStatus  json.Number `json:"workflowStatus,omitempty"`
	Remarks         string      `json:"remarks,omitempty"`
}

// CBRICSDPAcc is an unregistered participant's demat account (DPAccount
// Structure; POST /rest/v1/unreg/dpacc).
type CBRICSDPAcc struct {
	DPType          string      `json:"dpType"`         // NSDL or CDSL
	DPID            string      `json:"dpId,omitempty"` // NSDL only
	BenID           flexStr     `json:"benId"`          // 8 digits NSDL, 16 CDSL
	IsDefault       string      `json:"isDefault"`
	ParticipantCode string      `json:"participantCode,omitempty"`
	Status          string      `json:"status,omitempty"`
	WorkflowStatus  json.Number `json:"workflowStatus,omitempty"`
	Remarks         string      `json:"remarks,omitempty"`
}

// CBRICSUnreg is an unregistered participant (POST /rest/v1/unreg, its
// update and replies).
type CBRICSUnreg struct {
	ID            json.Number     `json:"id,omitempty"`
	LoginID       string          `json:"loginId,omitempty"`
	FirstName     string          `json:"firstName"`
	ActualStatus  json.Number     `json:"actualStatus,omitempty"`
	PAN           string          `json:"panNo,omitempty"`
	Custodian     string          `json:"custodian,omitempty"`
	ContactPerson string          `json:"contactPerson"`
	Mobiles       []string        `json:"mobileList"`
	Emails        []string        `json:"emailList"`
	Telephone     flexStr         `json:"telephone"`
	Fax           string          `json:"fax,omitempty"`
	Address       string          `json:"address,omitempty"`
	Address2      string          `json:"address2,omitempty"`
	Address3      string          `json:"address3,omitempty"`
	StateCode     flexStr         `json:"stateCode"`
	RegAddress    string          `json:"regAddress"`
	LEI           string          `json:"leiCode,omitempty"`
	LEIExpiry     string          `json:"expiryDate,omitempty"`
	Banks         []CBRICSBankAcc `json:"bankAccountList,omitempty"`
	DPs           []CBRICSDPAcc   `json:"dpAccountList,omitempty"`
	DOBDOI        string          `json:"dobDoi"`
	// In replies.
	WorkflowStatus json.Number `json:"workflowStatus,omitempty"`
	Remarks        string      `json:"remarks,omitempty"`
	PANVerStatus   json.Number `json:"panVerStatus,omitempty"`
	PANVerRemarks  string      `json:"panVerRemarks,omitempty"`
}

// CBRICSFound is a registered participant or custodian
// (POST /rest/v1/participant/find).
type CBRICSFound struct {
	LoginID      string      `json:"loginId"`
	ActualStatus json.Number `json:"actualStatus"`
	Type         json.Number `json:"type"`
	Custodian    string      `json:"custodian,omitempty"`
	Name         string      `json:"firstName"`
	PAN          string      `json:"panNo,omitempty"`
}

// CBRICSIssue is one issue traded today (POST
// /rest/v1/marketwatch/activeissues).
type CBRICSIssue struct {
	Symbol         string      `json:"symbol"`
	Description    string      `json:"description"`
	Type           string      `json:"type"`
	Issuer         string      `json:"issuer"`
	IssueDate      string      `json:"issueDate"`
	MaturityDate   string      `json:"maturityDate"`
	CouponRate     json.Number `json:"couponRate"`
	CreditRating   string      `json:"creditRating"`
	LastTradePrice json.Number `json:"lastTradePrice"`
	LastTradeValue json.Number `json:"lastTradeValue"`
	LastTradeTime  string      `json:"lastTradeTime"`
	TotTradeValue  json.Number `json:"totTradeValue"`
	TotNoOfTrades  json.Number `json:"totNoOfTrades"`
	AvgYield       json.Number `json:"avgYield"`
	AvgTradePrice  json.Number `json:"avgTradePrice"`
}

// ---- the calls ----

func (c *cbricsCall) order(o CBRICSOrder, update bool) (CBRICSOrder, error) {
	var r CBRICSOrder
	path := "/rest/v1/order"
	if update {
		path += "/update"
	}
	err := c.do(http.MethodPost, "", path, o, &r)
	return r, err
}

// sellReportings lists the sell reportings of a day where the firm is
// buyer, seller or broker: today's by GET, an earlier day's by POST with
// filtEntryDate.
func (c *cbricsCall) sellReportings(day time.Time, today bool) ([]CBRICSOrder, error) {
	var r []CBRICSOrder
	if today {
		return r, c.do(http.MethodGet, "", "/rest/v1/order/sellreportings", nil, &r)
	}
	return r, c.do(http.MethodPost, "", "/rest/v1/order/sellreportings", map[string]string{"filtEntryDate": day.Format(cbricsDate)}, &r)
}

func (c *cbricsCall) buyInstructions() ([]CBRICSOrder, error) {
	var r []CBRICSOrder
	return r, c.do(http.MethodGet, http.MethodPost, "/rest/v1/order/buyinstructions", nil, &r)
}

// orderStatus accepts (7) or rejects (2) a sale reported to the firm's
// client as buyer.
func (c *cbricsCall) orderStatus(id string, status int, yieldReason string) (string, error) {
	b := map[string]any{"id": json.Number(id), "status": status}
	if yieldReason != "" {
		b["buyerYieldViolationReason"] = yieldReason
	}
	var r struct{ Message string }
	err := c.do(http.MethodPost, http.MethodPut, "/rest/v1/order/status", b, &r)
	return r.Message, err
}

func (c *cbricsCall) activeIssues(isin string) ([]CBRICSIssue, error) {
	var r []CBRICSIssue
	b := map[string]any{}
	if isin != "" {
		b["symbol"] = isin
	}
	return r, c.do(http.MethodPost, "", "/rest/v1/marketwatch/activeissues", b, &r)
}

func (c *cbricsCall) find(loginID, pan string) ([]CBRICSFound, error) {
	b := map[string]any{"type": 2}
	if loginID != "" {
		b["loginId"] = loginID
	}
	if pan != "" {
		b["panNo"] = pan
	}
	var r []CBRICSFound
	return r, c.do(http.MethodPost, http.MethodPut, "/rest/v1/participant/find", b, &r)
}

func (c *cbricsCall) unreg(path string, body any) (CBRICSUnreg, error) {
	var r CBRICSUnreg
	return r, c.do(http.MethodPost, "", "/rest/v1/unreg"+path, body, &r)
}

func (c *cbricsCall) unregGet(id string) (CBRICSUnreg, error) {
	var r CBRICSUnreg
	return r, c.do(http.MethodGet, http.MethodPost, "/rest/v1/unreg/"+url.PathEscape(id), nil, &r)
}

func (c *cbricsCall) unregAll(workflow int) ([]CBRICSUnreg, error) {
	var r []CBRICSUnreg
	return r, c.do(http.MethodPost, "", "/rest/v1/unreg/all", map[string]any{"workflowStatus": workflow}, &r)
}

func (c *cbricsCall) unregDelete(id string) (string, error) {
	var r struct{ Message string }
	err := c.do(http.MethodPost, "", "/rest/v1/unreg/final/delete", map[string]any{"id": json.Number(id)}, &r)
	return r.Message, err
}

func (c *cbricsCall) bankAcc(path string, body any) (CBRICSBankAcc, error) {
	var r CBRICSBankAcc
	return r, c.do(http.MethodPost, "", "/rest/v1/unreg/bankacc"+path, body, &r)
}

func (c *cbricsCall) dpAcc(path string, body any) (CBRICSDPAcc, error) {
	var r CBRICSDPAcc
	return r, c.do(http.MethodPost, "", "/rest/v1/unreg/dpacc"+path, body, &r)
}

func (c *cbricsCall) bankAll(code string, workflow int) ([]CBRICSBankAcc, error) {
	var r []CBRICSBankAcc
	return r, c.do(http.MethodPost, "", "/rest/v1/unreg/bankacc/all", map[string]any{"participantCode": code, "workflowStatus": workflow}, &r)
}

func (c *cbricsCall) dpAll(code string, workflow int) ([]CBRICSDPAcc, error) {
	var r []CBRICSDPAcc
	return r, c.do(http.MethodPost, "", "/rest/v1/unreg/dpacc/all", map[string]any{"participantCode": code, "workflowStatus": workflow}, &r)
}

// settleAll lists the orders for settlement whose settlement date is in
// the range (at most 30 days).
func (c *cbricsCall) settleAll(from, to time.Time) ([]CBRICSSettle, error) {
	var r []CBRICSSettle
	return r, c.do(http.MethodPost, "", "/rest/v1/settle/order/all", map[string]string{"filtFromModSettleDate": from.Format(cbricsDate), "filtToModSettleDate": to.Format(cbricsDate)}, &r)
}

// settleBank changes the seller's payout bank account; settleDP the
// buyer's demat account (before settlement starts).
func (c *cbricsCall) settleBank(id, ifsc, account string) (CBRICSSettle, error) {
	var r CBRICSSettle
	b := map[string]any{"id": json.Number(id), "ifscCode": ifsc}
	if account != "" {
		b["accountNo"] = account
	}
	return r, c.do(http.MethodPost, "", "/rest/v1/settle/order/updatebank", b, &r)
}

func (c *cbricsCall) settleDP(id, dpID, benID string) (CBRICSSettle, error) {
	var r CBRICSSettle
	b := map[string]any{"id": json.Number(id), "benId": benID}
	if dpID != "" {
		b["dpId"] = dpID
	}
	return r, c.do(http.MethodPost, "", "/rest/v1/settle/order/updatedp", b, &r)
}

// cbricsTest is TestConnection's check: a login (and logout, so the test
// does not hold the session).
func (o *Ops) cbricsTest(ctx context.Context, tenant string) (string, string) {
	c, err := o.cbricsCall(ctx, tenant)
	if err != nil {
		return "Problem", strings.TrimPrefix(strings.TrimPrefix(err.Error(), "invalid: "), "state: ")
	}
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	c.ctx = cctx
	if _, err := c.login(true); err != nil {
		var ce *CBRICSError
		if errors.As(err, &ce) {
			return "Problem", ce.Error()
		}
		return "Problem", strings.TrimPrefix(err.Error(), "state: ")
	}
	c.logout()
	env := map[bool]string{true: "live", false: "testing"}[c.x.live]
	return "Working", fmt.Sprintf("CBRICS (%s) accepted the login %s of participant %s", env, c.x.cfg.Login, c.x.cfg.Member)
}

// ---- numbers and dates ----

func cbricsDay(s string) string { // dd-MM-yyyy to YYYY-MM-DD
	s = strings.TrimSpace(s)
	if len(s) >= 10 {
		if t, err := time.Parse(cbricsDate, s[:10]); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

func cbricsDayOut(s string) string { // YYYY-MM-DD to dd-MM-yyyy
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Format(cbricsDate)
	}
	return ""
}

// cbricsStates are the state codes of Annexure 2.
var cbricsStates = map[string]string{
	"01": "Jammu and Kashmir", "02": "Himachal Pradesh", "03": "Punjab", "04": "Chandigarh", "05": "Uttarakhand", "06": "Haryana", "07": "Delhi",
	"08": "Rajasthan", "09": "Uttar Pradesh", "10": "Bihar", "11": "Sikkim", "12": "Arunachal Pradesh", "13": "Nagaland", "14": "Manipur",
	"15": "Mizoram", "16": "Tripura", "17": "Meghalaya", "18": "Assam", "19": "West Bengal", "20": "Jharkhand", "21": "Odisha",
	"22": "Chhattisgarh", "23": "Madhya Pradesh", "24": "Gujarat", "25": "Daman & Diu", "26": "Dadra and Nagar Haveli", "27": "Maharashtra",
	"29": "Karnataka", "30": "Goa", "31": "Lakshdweep", "32": "Kerala", "33": "Tamil Nadu", "34": "Pondicherry", "35": "Andaman & Nicobar Islands",
	"36": "Telangana", "37": "Andhra Pradesh", "38": "Ladakh", "98": "Import (not registered in India)",
}

// cbricsStateCode reads a state name (or a code) as an Annexure 2 code.
func cbricsStateCode(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		s = "0" + s
	}
	if _, ok := cbricsStates[s]; ok {
		return s
	}
	n := strings.NewReplacer("&", "and", ".", "", "  ", " ").Replace(strings.ToLower(s))
	alias := map[string]string{"orissa": "21", "puducherry": "34", "lakshadweep": "31", "andaman and nicobar islands": "35", "andaman and nicobar": "35",
		"dadra and nagar haveli and daman and diu": "26", "daman and diu": "25", "new delhi": "07", "nct of delhi": "07", "jammu & kashmir": "01", "uttaranchal": "05"}
	if c, ok := alias[n]; ok {
		return c
	}
	for c, name := range cbricsStates {
		if strings.NewReplacer("&", "and").Replace(strings.ToLower(name)) == n {
			return c
		}
	}
	return ""
}
