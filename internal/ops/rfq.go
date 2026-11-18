package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- NSE RFQ platform: debt deals (RFQ – Protocol for Web API 1.1.10) ----
//
// Corporate bonds, commercial paper and certificates of deposit are dealt
// on NSE's RFQ platform (part of CBRICS): an RFQ is negotiated, a quote is
// accepted (the thread is "Traded", status A, with a trade number), then the
// consideration is proposed by one side and accepted by the other
// ("Consideration confirmed", status C). For a brokered deal the broker
// names its client at the consideration step. Ledgerline is the back office,
// not the dealing screen: it reads the member's deals, confirms them for the
// firm's clients and decides amendments. The calls it makes:
//
//   - POST /rest/v1/login (domain = participant code, login, password) gives
//     a login key sent as the "loginKey" header on every other call. A 401
//     means the session lapsed (or the same login signed in elsewhere): one
//     fresh login, then the call again (Introduction, Common Error Response).
//   - GET /rest/v1/market/timings: the day's windows by issue type, in
//     milliseconds from midnight; Ledgerline reads deals only while they are
//     open (plus half an hour for confirmations and amendments).
//   - POST /rest/v1/negotiation/all: the member's negotiation threads of a
//     date; a thread Traded (A) or Consideration confirmed (C) is a deal.
//   - POST /rest/v1/deal/propose (consideration and the final client, or
//     trade splits across clients), /deal/acceptreject (accept or reject the
//     counterparty's proposal), /deal/tradesplit and /deal/changeclient (the
//     counter side's splits or client) (Deal Confirmation API).
//   - POST /rest/v1/dealamend/all, /modifycancel, /updatestatus (Deal
//     Amendment API): amendments of confirmed deals.
//   - POST /rest/v1/isins/all (an issue's description and face value),
//     /rfqmaster/calcprice (price and accrued interest from yield) and
//     /debar/all (clients the exchange has debarred) (Other API).
//   - NSE calls POST <callback base>/v1/notification with RFQ, negotiation
//     and amendment records, signed in a "token" header with the hex
//     HmacSHA256 of the body. Ledgerline takes a notification only as a
//     signal to read again through the API (rfqdeals.go).
//
// Posting RFQs, quoting, negotiating, participant groups and limits are the
// dealer's work on NSE's screen and are not here.
//
// Live https://bricsonline.nseindia.com/rfq, test
// https://bricsonlinereguat.nseindia.com/rfq.

const (
	connNSERFQ = "nse-rfq"
	rfqLive    = "https://bricsonline.nseindia.com/rfq"
	rfqUAT     = "https://bricsonlinereguat.nseindia.com/rfq"
)

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connNSERFQ, Name: "NSE RFQ (debt)", Group: "Exchanges", Test: "https",
		Purpose: "Reads the firm's corporate bond, CP and CD deals from NSE's RFQ platform, confirms them for clients (consideration, client, trade splits) and decides amendments. Needs an API-enabled CBRICS user for the firm's participant code, the firm registered on RFQ as a broker, and, for notifications, the callback address given to NSE",
		Fields: []ConnField{{Key: "member", Label: "Participant code (the CBRICS domain)"}, {Key: "login", Label: "API user login ID (a CBRICS user enabled for the API)"},
			{Key: "password", Label: "Password", Secret: true},
			{Key: "env", Label: "Environment", Options: []string{"Live", "UAT"}},
			{Key: "url", Label: "Address (leave empty for NSE's)", Optional: true, Placeholder: rfqLive},
			{Key: "read", Label: "Read deals while the market is open", Options: []string{"On", "Off"}, Optional: true},
			{Key: "every", Label: "Read every (minutes, empty: 5)", Optional: true, Placeholder: "5"},
			{Key: "callback_token", Label: "Callback token: a long random phrase that goes in the callback address given to NSE", Secret: true, Optional: true},
			{Key: "callback_key", Label: "Callback signing key from NSE (checks the token header, HmacSHA256)", Secret: true, Optional: true},
			fNotes}})
}

// RFQConfig is the firm's RFQ API login.
type RFQConfig struct{ Base, Member, Login, Password string }

type rfqConn struct {
	cfg            RFQConfig
	cbToken, cbKey string
	read           bool
	every          time.Duration
	live           bool
	usable         bool
	problem        bool // NSE refused the sign-in: no scheduled reads
}

func rfqConnOf(c Connection) rfqConn {
	live := c.Values["env"] != "UAT"
	base := map[bool]string{true: rfqLive, false: rfqUAT}[live]
	if u := strings.TrimRight(strings.TrimSpace(c.Values["url"]), "/"); u != "" {
		base = u
	}
	x := rfqConn{cfg: RFQConfig{Base: base, Member: strings.TrimSpace(c.Values["member"]), Login: strings.TrimSpace(c.Values["login"]), Password: c.Secrets["password"]},
		cbToken: c.Secrets["callback_token"], cbKey: c.Secrets["callback_key"], read: c.Values["read"] != "Off", every: 5 * time.Minute, live: live}
	if n, err := strconv.Atoi(strings.TrimSpace(c.Values["every"])); err == nil && n >= 1 && n <= 60 {
		x.every = time.Duration(n) * time.Minute
	}
	x.usable = x.cfg.Member != "" && x.cfg.Login != "" && x.cfg.Password != ""
	x.problem = c.Status == "Problem"
	return x
}

func (o *Ops) rfqConnection(ctx context.Context, tenant string) (rfqConn, bool) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNSERFQ)
	if err != nil || c.Status == "Not set" {
		return rfqConn{}, false
	}
	return rfqConnOf(c), true
}

// rfqHTTP is replaced in tests by the fake server's client.
var rfqHTTP = &http.Client{Timeout: 60 * time.Second}

// rfqSessions hold each login's key, in memory: base|participant|login.
var rfqSessions sync.Map

// rfqSession is what a login gives.
type rfqSession struct {
	Key        string
	Broker     bool
	BrokerType string // R RFQ broker, O OBPP broker
}

// rfqError is the platform's common error response: the HTTP status and
// its messages (strings, or field errors).
type rfqError struct {
	Status   int
	Messages []string
}

func (e *rfqError) Error() string {
	what := map[int]string{400: "refused it", 401: "says the session is not signed in", 404: "has no such record", 500: "had an error (report it to NSE's support desk)"}[e.Status]
	if what == "" {
		what = "answered"
	}
	return fmt.Sprintf("NSE's RFQ platform %s (%d): %s", what, e.Status, firstOf(strings.Join(e.Messages, "; "), "no reason given"))
}

func rfqMessages(b []byte) []string {
	var r struct{ Messages []json.RawMessage }
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	var out []string
	for _, m := range r.Messages {
		var s string
		if json.Unmarshal(m, &s) == nil {
			out = append(out, s)
			continue
		}
		var f struct{ Field, Msg string }
		if json.Unmarshal(m, &f) == nil && (f.Field != "" || f.Msg != "") {
			out = append(out, strings.TrimPrefix(f.Field+": "+f.Msg, ": "))
		}
	}
	return out
}

func rfqKey(c RFQConfig) string { return c.Base + "|" + c.Member + "|" + c.Login }

// rfqLoginError is NSE refusing the sign-in itself (wrong or expired
// password, user disabled). Scheduled reads stop on it, so a wrong
// password is never tried again by itself: NSE disables a user after
// incorrect password attempts.
type rfqLoginError struct{ msg string }

func (e *rfqLoginError) Error() string { return e.msg }

// rfqSeen marks the connection Problem when NSE refused the sign-in.
func (o *Ops) rfqSeen(ctx context.Context, tenant string, err error) {
	var le *rfqLoginError
	if !errors.As(err, &le) {
		return
	}
	_, _ = docs.Update(ctx, o.Docs, tenant, cConn, connNSERFQ, SystemActor, func(c *Connection) error {
		if c.Status == "Not set" {
			return docs.ErrStale
		}
		c.Status, c.Problem, c.TestedAt, c.TestedBy = "Problem", le.msg+" (reading stopped: save the connection again to restart it)", o.now().Format(time.RFC3339), SystemActor
		return nil
	})
}

// rfqLogin signs in (POST /rest/v1/login) and keeps the login key.
func rfqLogin(ctx context.Context, c RFQConfig) (rfqSession, error) {
	b, _ := json.Marshal(map[string]string{"domain": c.Member, "login": c.Login, "password": c.Password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/rest/v1/login", bytes.NewReader(b))
	if err != nil {
		return rfqSession{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := rfqHTTP.Do(req)
	if err != nil {
		return rfqSession{}, fmt.Errorf("could not reach NSE's RFQ platform: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		Status, Reason, LoginKey, BrokerEnablementType string
		Broker                                         bool
	}
	_ = json.Unmarshal(out, &r)
	switch {
	case strings.EqualFold(r.Status, "P"):
		return rfqSession{}, &rfqLoginError{fmt.Sprintf("NSE's RFQ platform says the password has expired (%s): change it on CBRICS and save the new one here", firstOf(r.Reason, "Change Password"))}
	case resp.StatusCode != 200 || !strings.EqualFold(r.Status, "S") || r.LoginKey == "":
		return rfqSession{}, &rfqLoginError{fmt.Sprintf("NSE's RFQ platform refused the login (%d): %s", resp.StatusCode, firstOf(r.Reason, firstOf(strings.Join(rfqMessages(out), "; "), trimBody(out))))}
	}
	s := rfqSession{Key: r.LoginKey, Broker: r.Broker, BrokerType: r.BrokerEnablementType}
	rfqSessions.Store(rfqKey(c), s)
	return s, nil
}

// rfqCall makes one call with the login key, signing in (again, once) when
// there is no session or the platform says it lapsed. out receives the
// JSON reply.
func rfqCall(ctx context.Context, c RFQConfig, method, path string, body, out any) error {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	for try := 0; try < 2; try++ {
		var s rfqSession
		if v, ok := rfqSessions.Load(rfqKey(c)); ok && try == 0 {
			s = v.(rfqSession)
		} else {
			var err error
			if s, err = rfqLogin(ctx, c); err != nil {
				return err
			}
		}
		var rd io.Reader
		if b != nil {
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
		if err != nil {
			return err
		}
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("loginKey", s.Key)
		resp, err := rfqHTTP.Do(req)
		if err != nil {
			return fmt.Errorf("could not reach NSE's RFQ platform: %v", err)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		if resp.StatusCode == 401 && try == 0 {
			rfqSessions.Delete(rfqKey(c))
			continue // the session lapsed: sign in again, once
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			ms := rfqMessages(data)
			if len(ms) == 0 {
				ms = []string{trimBody(data)}
			}
			return &rfqError{Status: resp.StatusCode, Messages: ms}
		}
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("NSE's RFQ platform answered something Ledgerline cannot read: %s", trimBody(data))
			}
		}
		return nil
	}
	return fmt.Errorf("NSE's RFQ platform did not take a fresh login")
}

// rfqTest signs in afresh and reads the market timings.
func (o *Ops) rfqTest(ctx context.Context, tenant string, c Connection) (string, string) {
	x := rfqConnOf(c)
	if !x.usable {
		return "Problem", "enter the participant code, API user login ID and password"
	}
	s, err := rfqLogin(ctx, x.cfg)
	if err != nil {
		return "Problem", err.Error()
	}
	var ts []rfqTiming
	if err := rfqCall(ctx, x.cfg, http.MethodGet, "/rest/v1/market/timings", nil, &ts); err != nil {
		return "Problem", err.Error()
	}
	o.rfqState(ctx, tenant, func(st *RFQState) { st.Broker, st.BrokerType, st.Checked = s.Broker, s.BrokerType, true })
	if !s.Broker {
		return "Working", "signed in, but NSE does not show the participant as a broker: client deals need the firm registered on RFQ as a broker"
	}
	return "Working", ""
}

// ---- the protocol's records ----

// rfqDec reads a protocol number (or a number in a string, or null).
type rfqDec struct {
	v   money.Dec
	set bool
}

func (d *rfqDec) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := money.ParseDec(s)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return nil // unreadable: left unset
		}
		if v, err = money.ParseDec(strconv.FormatFloat(f, 'f', -1, 64)); err != nil {
			return nil
		}
	}
	d.v, d.set = v, true
	return nil
}

// int is the number as a whole count (bonds).
func (d rfqDec) int() int64 {
	n, _ := strconv.ParseInt(d.v.Fixed(0), 10, 64)
	return n
}

// rfqSplit is a TradeSplit (Deal Confirmation API).
type rfqSplit struct {
	Val             rfqDec `json:"val"`
	Qty             rfqDec `json:"qty"`
	AccInt          rfqDec `json:"accInt"`
	Cons            rfqDec `json:"cons"`
	PropCust        string `json:"propCust"`
	CouCust         string `json:"couCust"`
	PropActPartCode string `json:"propActPartCode"`
	CouActPartCode  string `json:"couActPartCode"`
}

// rfqSplitOut is a TradeSplit as Ledgerline sends it.
type rfqSplitOut struct {
	Val             json.Number `json:"val"`
	Qty             int64       `json:"qty"`
	AccInt          json.Number `json:"accInt"`
	Cons            json.Number `json:"cons"`
	PropActPartCode string      `json:"propActPartCode,omitempty"`
	CouActPartCode  string      `json:"couActPartCode,omitempty"`
}

// rfqNeg is a negotiation thread (Response JSON of POST
// /rest/v1/negotiation/add), the shape every negotiation and deal
// confirmation call answers with.
type rfqNeg struct {
	RFQNumber   string `json:"rfqNumber"`
	NgRFQNumber string `json:"ngRfqNumber"` // the callback sample's name for it
	ID          string `json:"id"`
	Date        string `json:"date"`
	ISIN        string `json:"isin"`
	BuySell     string `json:"buySell"` // with respect to the responder

	InitAeCode        string `json:"initAeCode"`
	InitDealType      string `json:"initDealType"`
	InitClientCode    string `json:"initClientCode"`
	InitClientRegType string `json:"initClientRegType"`
	RespAeCode        string `json:"respAeCode"`
	RespDealType      string `json:"respDealType"`
	RespClientCode    string `json:"respClientCode"`
	RespClientRegType string `json:"respClientRegType"`

	Status      string `json:"status"`
	TradeNumber string `json:"tradeNumber"`

	AcceptedSettlementType  flexStr `json:"acceptedSettlementType"` // a number, or "0" in one sample
	AcceptedSettlementDate  string  `json:"acceptedSettlementDate"`
	AcceptedValue           rfqDec  `json:"acceptedValue"`
	AcceptedQuantity        rfqDec  `json:"acceptedQuantity"`
	AcceptedYieldType       string  `json:"acceptedYieldType"`
	AcceptedYield           rfqDec  `json:"acceptedYield"`
	AcceptedCalcMethod      string  `json:"acceptedCalcMethod"`
	AcceptedPrice           rfqDec  `json:"acceptedPrice"`
	AcceptedPutCallDate     string  `json:"acceptedPutCallDate"`
	AcceptedAccruedInterest rfqDec  `json:"acceptedAccruedInterest"`
	AcceptedConsideration   rfqDec  `json:"acceptedConsideration"`
	AcceptedQuoteTime       string  `json:"acceptedQuoteTime"`
	AcceptedBySide          string  `json:"acceptedBySide"`

	ConfirmStatus           string     `json:"confirmStatus"`
	ProposedBySide          string     `json:"proposedBySide"`
	ProposedTime            string     `json:"proposedTime"`
	ConfirmedPriceQuoteTime string     `json:"confirmedPriceQuoteTime"`
	LastActivityTimestamp   string     `json:"lastActivityTimestamp"`
	TradeSplits             []rfqSplit `json:"tradeSplits"`
}

// rfqAmendWire is a deal amendment (Response JSON of POST
// /rest/v1/dealamend/modifycancel).
type rfqAmendWire struct {
	ID                 flexStr `json:"id"`
	RFQNumber          string  `json:"rfqNumber"`
	NgID               string  `json:"ngId"`
	RequestType        string  `json:"requestType"`
	Date               string  `json:"date"`
	TradeNumber        string  `json:"tradeNumber"`
	ISIN               string  `json:"isin"`
	Description        string  `json:"description"`
	BuyerAeCode        string  `json:"buyerAeCode"`
	BuyerClientCode    string  `json:"buyerClientCode"`
	SellerAeCode       string  `json:"sellerAeCode"`
	SellerClientCode   string  `json:"sellerClientCode"`
	Value              rfqDec  `json:"value"`
	Quantity           rfqDec  `json:"quantity"`
	YieldType          string  `json:"yieldType"`
	Yield              rfqDec  `json:"yield"`
	Price              rfqDec  `json:"price"`
	AccruedInterest    rfqDec  `json:"accruedInterest"`
	Consideration      rfqDec  `json:"consideration"`
	SettlementType     flexStr `json:"settlementType"`
	SettlementDate     string  `json:"settlementDate"`
	ModYieldType       string  `json:"modYieldType"`
	ModAccruedInterest rfqDec  `json:"modAccruedInterest"`
	ModConsideration   rfqDec  `json:"modConsideration"`
	ModSettlementType  flexStr `json:"modSettlementType"`
	ModSettlementDate  string  `json:"modSettlementDate"`
	ModReason          string  `json:"modReason"`
	CancReason         string  `json:"cancReason"`
	ProposerAeCode     string  `json:"proposerAeCode"`
	ProposerTime       string  `json:"proposerTime"`
	ProposerRemarks    string  `json:"proposerRemarks"`
	CounterAeCode      string  `json:"counterAeCode"`
	CounterTime        string  `json:"counterTime"`
	CounterRemarks     string  `json:"counterRemarks"`
	ApprovalStatus     string  `json:"approvalStatus"`
}

// rfqTiming is an issue type's windows (GET /rest/v1/market/timings), in
// milliseconds from midnight. The table names the fields twice over; the
// sample's names are used.
type rfqTiming struct {
	Type          string `json:"type"`
	StarttimeT0   int64  `json:"starttimeT0"`
	StarttimeT1   int64  `json:"starttimeT1"`
	YieldcutoffT0 int64  `json:"yieldcutoffT0"`
	YieldcutoffT1 int64  `json:"yieldcutoffT1"`
	PricecutoffT0 int64  `json:"pricecutoffT0"`
	PricecutoffT1 int64  `json:"pricecutoffT1"`
}

// rfqISIN is an issue of POST /rest/v1/isins/all.
type rfqISIN struct {
	Symbol, Description, Issuer, MaturityDate, Type, IssueCategory, Listed string
	CouponRate, FaceValue                                                  rfqDec
}

// ---- formats ----

// rfqDate reads the protocol's dd-MMM-yyyy as yyyy-mm-dd ("" if unreadable).
func rfqDate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 11 {
		s = s[:11] // a date time
	}
	if t, err := time.Parse("02-Jan-2006", s); err == nil {
		return t.Format("2006-01-02")
	}
	return ""
}

// rfqOutDate writes yyyy-mm-dd as dd-MMM-yyyy.
func rfqOutDate(d string) string {
	if t, err := time.Parse("2006-01-02", d); err == nil {
		return t.Format("02-Jan-2006")
	}
	return d
}

// rfqNum writes a decimal for a request with the protocol's decimal places.
func rfqNum(d money.Dec, places int) json.Number {
	s := d.Fixed(places)
	if strings.Contains(s, ".") {
		s = strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
	}
	return json.Number(s)
}

// rfqHHMM is milliseconds from midnight as HH:MM.
func rfqHHMM(ms int64) string {
	m := ms / 60000
	if m < 0 {
		m = 0
	}
	if m > 23*60+59 {
		m = 23*60 + 59
	}
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}
