package ops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// ---- NSE eIPO API (Protocol for WEB API, Initial Public Offer System 2.0.0) ----
//
// NSE's eIPO host (Production bidcollection.nseindia.com/eipo, UAT
// eipouat.nseindia.com/eipo) takes a member's IPO, SME and debt issue bids
// by API and tells it what happened to them:
//
//   - POST /v3/login with member, loginId and password gives a token sent
//     as "Authorization: Bearer <token>" (protocol 2.0.0, basic security
//     v3); it lapses after an hour idle. Protocol 1.20.x had /v1 with an
//     Access-Token header, which the connection can still choose; on v3
//     Ledgerline sends the token both ways, as 2.0.0's login section still
//     names Access-Token. API access and login without OTP are enabled by
//     msm@nse.co.in.
//   - POST /v3/transactions/add adds, modifies or cancels an application
//     and its bids (each bid line: new, or modify/cancel by its
//     bidReferenceNumber, with the application's last timestamp). The
//     member gives the application number. A UPI bid has upiFlag Y and the
//     UPI ID; an ASBA bid (blocked in the client's bank account) upiFlag N
//     with the bank's and location's codes (ipoeipoasba.go). Neither needs
//     a blocking reference from a member (only a 3-in-1 account bid, prefix
//     T). Ledgerline puts its own bid number in each line's remark, which
//     NSE echoes back.
//   - POST /v3/transactions/fetch is the status of one application.
//   - GET /v3/ipomaster is every open and coming issue: Ledgerline sets
//     new ones up by itself when switched on.
//   - POST /v3/allotment/fetch is the issue's allotment, by application.
//   - NSE calls the member back (appdpstatus, apppaystatus, notification)
//     with an Authorization header of base64(hex(sha256(hex(sha1(
//     password))))) of the callback password the firm registered.
//
// Rate limits (Appendix C): login 2 a second; add 1,000 a second; fetch 25
// a second; allotment fetch 10 a second. Advanced security (v2: requests
// and replies encrypted with AES and RSA, and signed) can be chosen on the
// connection; it lives in the transport (call, and the callback check) in
// ipoeipov2.go, so every call gets it.

const connNSEEIPO = "nse-eipo"

const (
	// Appendix B of protocol 2.0.0 (1.20.7 had eipo.nseindia.com and
	// uat-ipo.nseindia.com).
	eipoLive = "https://bidcollection.nseindia.com/eipo"
	eipoUAT  = "https://eipouat.nseindia.com/eipo"

	eipoV3 = "v3 (Bearer token, protocol 2.0.0)"
	eipoV1 = "v1 (Access-Token, protocol 1.20)"
)

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connNSEEIPO, Name: "NSE eIPO API (public issues)", Group: "Exchanges", Test: "https",
		Purpose: "Sends IPO, SME and NCD bids to NSE's eIPO, takes bid, depository and UPI mandate status from NSE's callback, sets up new issues from NSE's IPO master and reads allotment. Ask msm@nse.co.in for API access (member code, user ID), login without OTP and the callback address to whitelist; for v2 (advanced security) also for NSE's public key for the firm, and send NSE the firm's public key",
		Fields: []ConnField{{Key: "member", Label: "Member code"}, {Key: "login", Label: "eIPO user ID enabled for the API"},
			{Key: "password", Label: "eIPO password", Secret: true},
			{Key: "env", Label: "Environment", Options: []string{"Live", "Mock (UAT)"}},
			{Key: "url", Label: "Address (leave empty for NSE's)", Optional: true, Placeholder: eipoLive},
			{Key: "version", Label: "API version (empty: v3)", Optional: true, Options: []string{eipoV3, eipoV2, eipoV1}},
			{Key: "private_key", Label: "v2 only: the firm's RSA private key (PEM, 2048-bit); its public key is the one given to NSE", Secret: true, Optional: true},
			{Key: "nse_public_key", Label: "v2 only: NSE's public key for the firm (PEM public key or certificate, from NSE)", Secret: true, Optional: true},
			{Key: "asba_location", Label: "ASBA location code (6 characters, from eIPO): used for ASBA bids when a bank has no location code of its own", Optional: true},
			{Key: "callback_token", Label: "Callback token: a long random phrase that goes in the callback address given to NSE", Secret: true, Optional: true},
			{Key: "callback_password", Label: "Callback password registered with NSE (empty: the eIPO password)", Secret: true, Optional: true},
			{Key: "callback_ips", Label: "Addresses NSE calls back from (comma separated; empty allows any)", Optional: true},
			{Key: "send", Label: "Send bids through the API", Options: []string{"Off", "On"}},
			{Key: "master", Label: "Set up new issues from NSE's IPO master", Options: []string{"On", "Off"}, Optional: true},
			{Key: "allotment", Label: "Read allotment from NSE", Options: []string{"On", "Off"}, Optional: true}, fNotes}})
}

// EIPOConfig is the firm's eIPO API connection.
type EIPOConfig struct {
	Base, Member, Login, Password string
	Live                          bool
	Version                       string // "v3" (empty), "v2" or "v1"
	// v2 (advanced security) only: the firm's own key, which signs
	// requests and opens replies, and NSE's public key for the firm, which
	// seals requests and checks callbacks.
	MemberKey *rsa.PrivateKey
	NSEKey    *rsa.PublicKey
}

// path is an API path under the connection's version.
func (c EIPOConfig) path(p string) string {
	switch c.Version {
	case "v1":
		return "/v1" + p
	case "v2":
		return "/v2" + p
	}
	return "/v3" + p
}

// EIPOBidLine is one bid of an application as the API takes it.
type EIPOBidLine struct {
	ActivityType string      `json:"activityType"` // new, modify, cancel
	BidRef       json.Number `json:"bidReferenceNumber,omitempty"`
	Series       string      `json:"series,omitempty"`
	Quantity     int64       `json:"quantity"`
	AtCutOff     bool        `json:"atCutOff"`
	Price        json.Number `json:"price,omitempty"`
	Amount       json.Number `json:"amount,omitempty"`
	Remark       string      `json:"remark,omitempty"` // Ledgerline's bid number, echoed back
	Status       string      `json:"status,omitempty"` // in replies
	ReasonCode   json.Number `json:"reasonCode,omitempty"`
	Reason       string      `json:"reason,omitempty"`
}

// EIPOBid is one application as the API takes it.
type EIPOBid struct {
	Symbol        string        `json:"symbol"`
	AppNo         string        `json:"applicationNumber"`
	Category      string        `json:"category"`
	ClientName    string        `json:"clientName,omitempty"`
	Depository    string        `json:"depository"`
	DPID          string        `json:"dpId,omitempty"` // NSDL only
	ClientBenID   json.Number   `json:"clientBenId"`
	NonASBA       bool          `json:"nonASBA"`
	PAN           string        `json:"pan"`
	Ref           string        `json:"referenceNumber,omitempty"` // ASBA blocking reference; none for UPI
	AllotmentMode string        `json:"allotmentMode"`
	UPIFlag       string        `json:"upiFlag"`
	UPI           string        `json:"upi,omitempty"`
	BankCode      string        `json:"bankCode,omitempty"`     // ASBA (upiFlag N): NSE's code for the bank
	LocationCode  string        `json:"locationCode,omitempty"` // and for the location
	Timestamp     string        `json:"timestamp,omitempty"`    // the last reply's, for a change
	Bids          []EIPOBidLine `json:"bids"`

	LedgerlineID string `json:"-"` // Ledgerline's bid number
}

// EIPOMaster is one issue of NSE's IPO master.
type EIPOMaster struct {
	Symbol, Name, IssueType, SubType, ISIN, Registrar string
	LotSize, MinBidQuantity                           int64
	FaceValue, MinPrice, MaxPrice, TickSize           json.Number
	BiddingStartDate, BiddingEndDate                  string // dd-MM-yyyy
	DailyStartTime, DailyEndTime                      string
	CategoryDetails                                   []struct{ Code, StartTime, EndTime, LastDayEndTime string }
	SubCategorySettings                               []struct {
		CaCode, SubCatCode, DiscountType  string
		MinValue, MaxValue, DiscountPrice json.Number
		AllowCutOff, AllowUpi             bool
	}
	SeriesDetails []struct{ Code, Desc string }
}

// EIPOAllot is one allotted application.
type EIPOAllot struct {
	Symbol string      `json:"symbol"`
	AppNo  flexStr     `json:"applicationNumber"`
	PAN    string      `json:"panNumber"`
	Qty    int64       `json:"allotedQuantity"`
	Price  json.Number `json:"price"`
}

// flexStr reads a string or a number.
type flexStr string

func (f *flexStr) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flexStr(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return nil
	}
	*f = flexStr(n.String())
	return nil
}

// EIPOClient talks to NSE's eIPO API.
type EIPOClient interface {
	SendBids(ctx context.Context, c EIPOConfig, bids []EIPOBid) ([]IPOStatusUpdate, error)
	Inquire(ctx context.Context, c EIPOConfig, symbol string, appNos []string) ([]IPOStatusUpdate, error)
	Master(ctx context.Context, c EIPOConfig) ([]EIPOMaster, error)
	Allotment(ctx context.Context, c EIPOConfig, symbol string) ([]EIPOAllot, error)
}

// eipoClient is replaced in tests.
var eipoClient EIPOClient = nseEIPOHTTP{client: &http.Client{Timeout: 60 * time.Second}}

type nseEIPOHTTP struct{ client *http.Client }

// eipoTokens hold each login's session token, in memory.
var eipoTokens sync.Map // base|member|login -> eipoToken

type eipoToken struct {
	tok string
	exp time.Time
}

func (h nseEIPOHTTP) login(ctx context.Context, c EIPOConfig, fresh bool) (string, error) {
	if c.Version == "v2" {
		// v2 calls are authenticated by their signature and need no token;
		// the APIs v2 does not cover go by v3, which logs in on /v3/login.
		c.Version = "v3"
	}
	k := c.Base + "|" + c.Member + "|" + c.Login
	if t, ok := eipoTokens.Load(k); ok && !fresh && time.Now().Before(t.(eipoToken).exp) {
		return t.(eipoToken).tok, nil
	}
	b, _ := json.Marshal(map[string]string{"member": c.Member, "loginId": c.Login, "password": c.Password})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+c.path("/login"), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Ledgerline")
	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach NSE's eIPO: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct{ Status, Reason, Token string }
	_ = json.Unmarshal(out, &r)
	switch {
	case strings.EqualFold(r.Status, "passwordExpired"):
		return "", fmt.Errorf("NSE's eIPO says the password has expired: change it on eIPO and save it here")
	case resp.StatusCode != 200 || !strings.EqualFold(r.Status, "success") || r.Token == "":
		return "", fmt.Errorf("NSE's eIPO refused the login (%d %s): check the user ID, password and that API access and login without OTP are enabled", resp.StatusCode, firstOf(r.Reason, trimBody(out)))
	}
	eipoTokens.Store(k, eipoToken{r.Token, time.Now().Add(50 * time.Minute)})
	return r.Token, nil
}

// call sends one request, logging in (again, once) as needed. On a v2
// connection an API the protocol lists for advanced security goes sealed
// and signed (callV2); any other goes by basic security under /v3.
func (h nseEIPOHTTP) call(ctx context.Context, c EIPOConfig, method, path string, body any) ([]byte, error) {
	if c.Version == "v2" {
		api := strings.TrimPrefix(path, "/v2")
		if eipoV2Covers(api) {
			return h.callV2(ctx, c, method, path, body)
		}
		c.Version, path = "v3", "/v3"+api
	}
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	for try := 0; try < 2; try++ {
		tok, err := h.login(ctx, c, try > 0)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Ledgerline")
		req.Header.Set("Access-Token", tok)
		if c.Version != "v1" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := h.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("could not reach NSE's eIPO: %v", err)
		}
		out, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == 401 && try == 0 {
			continue // the session lapsed
		}
		if resp.StatusCode == 403 {
			return nil, fmt.Errorf("NSE's eIPO refused the request (403): a field is longer or of another type than the protocol allows, or the rate limit was reached: %s", trimBody(out))
		}
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("NSE's eIPO answered %d: %s", resp.StatusCode, trimBody(out))
		}
		return out, nil
	}
	return nil, fmt.Errorf("NSE's eIPO did not take a fresh login")
}

// eipoTxn is an application as NSE's replies give it.
type eipoTxn struct {
	Symbol, Category, PAN, Timestamp, Status, Reason string
	AppNo                                            flexStr `json:"applicationNumber"`
	ReasonCode                                       json.Number
	DPVerStatusFlag, DPVerFailCode, DPVerReason      string
	UPIPaymentStatusFlag                             json.Number
	Bids                                             []EIPOBidLine
}

// update is what a reply says about an application.
func (t eipoTxn) update(source string) IPOStatusUpdate {
	u := IPOStatusUpdate{AppNo: string(t.AppNo), Symbol: t.Symbol, PAN: t.PAN, Status: t.Status, Code: t.ReasonCode.String(), Reason: t.Reason,
		PayStatus: t.UPIPaymentStatusFlag.String(), DPStatus: t.DPVerStatusFlag, DPReason: firstOf(t.DPVerReason, t.DPVerFailCode), At: t.Timestamp, Source: source, Lines: []EIPOBidLine{}}
	for _, l := range t.Bids {
		if u.Ref == "" {
			u.Ref = l.Remark
		}
		if u.Activity == "" {
			u.Activity = l.ActivityType
		}
		if !strings.EqualFold(l.Status, "failed") {
			if !strings.EqualFold(l.ActivityType, "cancel") {
				u.Lines = append(u.Lines, l)
			}
		} else if u.Reason == "" || strings.EqualFold(t.Status, "success") {
			u.Code, u.Reason = l.ReasonCode.String(), l.Reason
		}
	}
	return u
}

func (h nseEIPOHTTP) SendBids(ctx context.Context, c EIPOConfig, bids []EIPOBid) ([]IPOStatusUpdate, error) {
	var out []IPOStatusUpdate
	for _, b := range bids {
		reply, err := h.call(ctx, c, http.MethodPost, c.path("/transactions/add"), b)
		if err != nil {
			return out, err
		}
		var t eipoTxn
		if err := json.Unmarshal(reply, &t); err != nil {
			return out, fmt.Errorf("NSE's eIPO reply is not an application: %s", trimBody(reply))
		}
		u := t.update("eIPO API")
		u.Ref, u.AppNo = firstOf(u.Ref, b.LedgerlineID), firstOf(u.AppNo, b.AppNo)
		if u.Activity == "" && len(b.Bids) > 0 {
			u.Activity = b.Bids[0].ActivityType
		}
		out = append(out, u)
	}
	return out, nil
}

func (h nseEIPOHTTP) Inquire(ctx context.Context, c EIPOConfig, symbol string, appNos []string) ([]IPOStatusUpdate, error) {
	var out []IPOStatusUpdate
	for _, a := range appNos {
		reply, err := h.call(ctx, c, http.MethodPost, c.path("/transactions/fetch"), map[string]string{"symbol": symbol, "applicationNumber": a})
		if err != nil {
			return out, err
		}
		var r struct {
			Status, Reason string
			Transactions   []eipoTxn
		}
		if json.Unmarshal(reply, &r) != nil || !strings.EqualFold(r.Status, "success") {
			continue // "No Records Found"
		}
		for _, t := range r.Transactions {
			u := t.update("eIPO inquiry")
			u.Activity = "" // the latest state, not an answer to one change
			out = append(out, u)
		}
	}
	return out, nil
}

func (h nseEIPOHTTP) Master(ctx context.Context, c EIPOConfig) ([]EIPOMaster, error) {
	reply, err := h.call(ctx, c, http.MethodGet, c.path("/ipomaster"), nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Status, Reason string
		Data           []EIPOMaster
	}
	if err := json.Unmarshal(reply, &r); err != nil || !strings.EqualFold(r.Status, "success") {
		return nil, fmt.Errorf("NSE's IPO master: %s", firstOf(r.Reason, trimBody(reply)))
	}
	return r.Data, nil
}

func (h nseEIPOHTTP) Allotment(ctx context.Context, c EIPOConfig, symbol string) ([]EIPOAllot, error) {
	reply, err := h.call(ctx, c, http.MethodPost, c.path("/allotment/fetch"), map[string]string{"symbol": symbol})
	if err != nil {
		return nil, err
	}
	var r struct {
		Status, Reason string
		Transactions   []EIPOAllot
	}
	if err := json.Unmarshal(reply, &r); err != nil {
		return nil, fmt.Errorf("NSE's allotment reply: %s", trimBody(reply))
	}
	if !strings.EqualFold(r.Status, "success") {
		return nil, nil // not published yet ("No Records Found")
	}
	return r.Transactions, nil
}

// eipoCallbackAuth is the Authorization NSE sends with a callback.
func eipoCallbackAuth(password string) string {
	s1 := sha1.Sum([]byte(password))
	s2 := sha256.Sum256([]byte(hex.EncodeToString(s1[:])))
	return base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(s2[:])))
}

// eipoUpdates reads bid news from a reply or callback, whatever its exact
// layout: every object naming a bid (reference or application number)
// gives its status, reason, UPI status, mandate number and depository
// check by the field names NSE's documents use.
func eipoUpdates(b []byte, source string) []IPOStatusUpdate {
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber() // application numbers are longer than a float holds
	if dec.Decode(&v) != nil {
		return nil
	}
	get := func(m map[string]any, names ...string) string {
		for _, n := range names {
			for k, x := range m {
				if strings.EqualFold(k, n) && x != nil {
					switch t := x.(type) {
					case map[string]any, []any:
						continue
					default:
						return strings.TrimSpace(fmt.Sprint(t))
					}
				}
			}
		}
		return ""
	}
	var out []IPOStatusUpdate
	var walk func(any, map[string]any)
	walk = func(v any, parent map[string]any) {
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				walk(e, parent)
			}
		case map[string]any:
			ref := get(t, "referenceNumber", "refNo", "memberRefNo", "clientReferenceNumber", "reference")
			app := get(t, "applicationNumber", "appNo", "applicationNo")
			if ref == "" && app == "" && parent != nil {
				ref = get(parent, "referenceNumber", "refNo", "memberRefNo", "clientReferenceNumber", "reference")
				app = get(parent, "applicationNumber", "appNo", "applicationNo")
			}
			if ref != "" || app != "" {
				u := IPOStatusUpdate{Ref: ref, AppNo: app, Symbol: get(t, "symbol", "sym"), PAN: get(t, "pan"),
					Status: get(t, "status", "bidStatus", "orderStatus"), Code: get(t, "reasonCode", "errorCode"), Reason: get(t, "reason", "upiPayReason", "statusMessage", "rejectReason", "errorMessage", "message"),
					PayStatus: get(t, "upiPaymentStatusFlag", "upiPaymentStatus", "upiStatus", "payStatus", "paymentStatus", "mandateStatus"), UMN: get(t, "umn"),
					DPStatus: get(t, "dpVerStatusFlag", "dpVerStatus", "dpStatus"), DPReason: get(t, "dpVerReason", "dpVerFailReason", "dpVerFailCode"),
					At: get(t, "timestamp", "time", "modTm", "entTm"), Activity: get(t, "activityType"), Source: source}
				if parent != nil && u.Symbol == "" {
					u.Symbol = get(parent, "symbol", "sym")
				}
				if u.Status != "" || u.PayStatus != "" || u.DPStatus != "" || u.AppNo != "" {
					out = append(out, u)
				}
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				switch t[k].(type) {
				case []any, map[string]any:
					walk(t[k], t)
				}
			}
		}
	}
	walk(v, nil)
	return out
}

type eipoConn struct {
	cfg                 EIPOConfig
	token, cbPassword   string
	asbaLocation        string
	ips                 []string
	keyErr              error // v2: what is wrong with the keys
	send, master, allot bool
	usable              bool // the login is entered
}

func (o *Ops) eipoConnection(ctx context.Context, tenant string) (eipoConn, bool) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNSEEIPO)
	if err != nil || c.Status == "Not set" {
		return eipoConn{}, false
	}
	live := c.Values["env"] != "Mock (UAT)"
	base := map[bool]string{true: eipoLive, false: eipoUAT}[live]
	if u := strings.TrimRight(strings.TrimSpace(c.Values["url"]), "/"); u != "" {
		base = u
	}
	ver := "v3"
	switch c.Values["version"] {
	case eipoV1:
		ver = "v1"
	case eipoV2:
		ver = "v2"
	}
	x := eipoConn{cfg: EIPOConfig{Base: base, Member: strings.TrimSpace(c.Values["member"]), Login: strings.TrimSpace(c.Values["login"]), Password: c.Secrets["password"], Live: live, Version: ver},
		asbaLocation: strings.ToUpper(strings.TrimSpace(c.Values["asba_location"])),
		token:        c.Secrets["callback_token"], cbPassword: firstOf(c.Secrets["callback_password"], c.Secrets["password"]),
		send: c.Values["send"] == "On", master: c.Values["master"] != "Off", allot: c.Values["allotment"] != "Off"}
	for _, ip := range strings.Split(c.Values["callback_ips"], ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			x.ips = append(x.ips, ip)
		}
	}
	if ver == "v2" {
		x.cfg.MemberKey, x.cfg.NSEKey, x.keyErr = eipoV2Keys(c.Secrets, true)
	}
	x.usable = x.cfg.Member != "" && x.cfg.Login != "" && x.cfg.Password != "" && x.keyErr == nil
	return x, true
}

// EIPOState is the API's last activity.
type EIPOState struct {
	LastSend      string `json:"last_send,omitempty"`
	LastInquiry   string `json:"last_inquiry,omitempty"`
	LastCallback  string `json:"last_callback,omitempty"`
	Callbacks     int    `json:"callbacks"`
	LastMaster    string `json:"last_master,omitempty"`
	MasterNew     int    `json:"master_new"` // issues set up from the master, in all
	MasterRefresh bool   `json:"master_refresh,omitempty"`
	LastAllot     string `json:"last_allotment,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

// EIPOView is the API's state for the screen.
type EIPOView struct {
	Set      bool      `json:"set"`
	Send     bool      `json:"send"`
	Usable   bool      `json:"usable"`
	Callback string    `json:"callback"`
	Token    bool      `json:"callback_token_set"`
	State    EIPOState `json:"state"`
	Needs    string    `json:"needs,omitempty"`
	Version  string    `json:"version"`
	// ASBA bids by API: the banks' codes and the firm's location code.
	Banks        []EIPOBankCode `json:"banks"`
	ASBALocation string         `json:"asba_location,omitempty"`
}

// EIPOStatus is the API connection's state.
func (o *Ops) EIPOStatus(ctx context.Context, tenant string) EIPOView {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIPOState, "eipo", EIPOState{})
	c, ok := o.eipoConnection(ctx, tenant)
	v := EIPOView{Set: ok, Send: c.send, Usable: c.usable, Token: c.token != "", State: st, Callback: "/hooks/" + tenant + "/eipo/<callback token>",
		Version: firstOf(c.cfg.Version, "v3"), Banks: o.EIPOBankCodes(ctx, tenant), ASBALocation: c.asbaLocation}
	if !c.usable {
		v.Needs = "enter the eIPO member code, API user ID and password"
		if c.keyErr != nil {
			v.Needs = strings.TrimPrefix(c.keyErr.Error(), ErrInvalid.Error()+": ")
		}
	}
	return v
}

func (o *Ops) eipoState(ctx context.Context, tenant string, fn func(*EIPOState)) {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIPOState, "eipo", EIPOState{})
	fn(&st)
	_ = docs.Upsert(ctx, o.Docs, tenant, cIPOState, "eipo", st, SystemActor)
}

// eipoAppNo gives a bid its application number on first sending: the
// member code and a running number, at most 16 characters.
func (o *Ops) eipoAppNo(ctx context.Context, tenant, member string) (string, error) {
	n, err := o.next(ctx, tenant, "eipo-app", SystemActor)
	if err != nil {
		return "", err
	}
	m := strings.ToUpper(member)
	if len(m) > 5 {
		m = m[:5]
	}
	return fmt.Sprintf("%s%0*d", m, 16-len(m), n), nil
}

// eipoLines are the bid lines for an application's waiting action: new
// lines for a new application; for a change, each option against the
// bid NSE holds (modify), extra options new and dropped bids cancelled;
// for a cancellation every bid NSE holds.
func eipoLines(a IPOApp, iss IPOIssue, cat IPOCategory) []EIPOBidLine {
	line := func(op IPOBidOption) EIPOBidLine {
		qty := op.Lots * iss.LotSize
		eff := op.Price.Sub(cat.Discount)
		if op.CutOff {
			eff = iss.PriceHigh.Sub(cat.Discount)
		}
		l := EIPOBidLine{ActivityType: "new", Quantity: qty, AtCutOff: op.CutOff, Amount: json.Number(eff.MulInt(qty).Fixed(2)), Series: op.Series, Remark: a.ID}
		if !op.CutOff {
			l.Price = json.Number(op.Price.Fixed(2))
		}
		return l
	}
	held := a.ExchBids
	var out []EIPOBidLine
	switch a.Pending {
	case "Cancel":
		for _, h := range held {
			out = append(out, EIPOBidLine{ActivityType: "cancel", BidRef: h.BidRef, Quantity: h.Quantity, AtCutOff: h.AtCutOff, Price: h.Price, Amount: h.Amount, Series: h.Series, Remark: a.ID})
		}
	case "Modify":
		for i, op := range a.Options {
			l := line(op)
			if i < len(held) {
				l.ActivityType, l.BidRef = "modify", held[i].BidRef
			}
			out = append(out, l)
		}
		for i := len(a.Options); i < len(held); i++ {
			h := held[i]
			out = append(out, EIPOBidLine{ActivityType: "cancel", BidRef: h.BidRef, Quantity: h.Quantity, AtCutOff: h.AtCutOff, Price: h.Price, Amount: h.Amount, Series: h.Series, Remark: a.ID})
		}
	default:
		for _, op := range a.Options {
			out = append(out, line(op))
		}
	}
	return out
}

// EIPOSend sends an issue's waiting bids to NSE through the API: UPI bids,
// and ASBA bids whose bank has its eIPO codes entered (the others stay for
// the bid file, with a work item asking for the codes).
func (o *Ops) EIPOSend(ctx context.Context, tenant, actor, issueID string) (IPOBatch, error) {
	if err := o.require(ctx, tenant, actor, "Sending IPO bids to the exchange", RoleMaker, RoleChecker); err != nil {
		return IPOBatch{}, err
	}
	conn, ok := o.eipoConnection(ctx, tenant)
	if !ok {
		return IPOBatch{}, fmt.Errorf("%w: set up the NSE eIPO API in Settings → Connections", ErrState)
	}
	if !conn.usable {
		return IPOBatch{}, fmt.Errorf("%w: the NSE eIPO connection needs the member code, API user ID and password", ErrState)
	}
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return IPOBatch{}, err
	}
	if _, err := o.sendable(ctx, tenant, iss, "NSE"); err != nil {
		return IPOBatch{}, err
	}
	bids, err := o.waitingBids(ctx, tenant, iss, "NSE")
	if err != nil {
		return IPOBatch{}, err
	}
	now := o.now()
	var list []EIPOBid
	acts := map[string]string{}
	skipped, asba := 0, false
	cm, _ := o.ClientMap(ctx, tenant)
	codes := o.eipoBankCodeMap(ctx, tenant)
	for _, b := range bids {
		var bankCode, locCode string
		switch b.Mode {
		case ModeUPI:
		case ModeASBA:
			c := cm[b.UCC]
			var ok bool
			if bankCode, locCode, ok = eipoASBACodes(codes, conn, c.Bank.IFSC); !ok {
				o.eipoNeedBank(ctx, tenant, c.Bank, conn, codes)
				skipped++
				continue
			}
			o.closeWork(ctx, tenant, SystemActor, "ipo", "eipo-bank-"+ifscPrefix(c.Bank.IFSC), "codes entered")
		default:
			skipped++
			continue
		}
		// A change to an application NSE holds needs its bid numbers: ask
		// NSE when Ledgerline does not have them (an earlier file upload).
		if b.Pending != "New" && len(b.ExchBids) == 0 && b.AppNo != "" {
			if ups, err := eipoClient.Inquire(ctx, conn.cfg, iss.Symbol, []string{b.AppNo}); err == nil && len(ups) > 0 {
				_, _, _ = o.ApplyIPOUpdates(ctx, tenant, SystemActor, ups)
				b, _ = o.ipoBid(ctx, tenant, b.ID)
			}
		}
		if b.AppNo == "" {
			no, err := o.eipoAppNo(ctx, tenant, conn.cfg.Member)
			if err != nil {
				return IPOBatch{}, err
			}
			// Kept before sending, so a retry sends the same number.
			if _, err := docs.Update(ctx, o.Docs, tenant, cIPO, b.ID, SystemActor, func(x *IPOApp) error { x.AppNo = no; return nil }); err != nil {
				return IPOBatch{}, err
			}
			b.AppNo = no
		}
		cat, _ := iss.category(b.Category)
		code := cat.exch("NSE")
		if cat.SubCode != "" {
			code = cat.SubCode
		}
		e := EIPOBid{Symbol: iss.Symbol, AppNo: b.AppNo, Category: code, ClientName: b.Name, Depository: strings.ToUpper(b.Depository), ClientBenID: json.Number(strings.TrimSpace(b.BenID)),
			PAN: b.PAN, AllotmentMode: "demat", UPIFlag: "Y", UPI: b.UPI, LedgerlineID: b.ID}
		if b.Mode == ModeASBA {
			e.UPIFlag, e.UPI, e.BankCode, e.LocationCode = "N", "", bankCode, locCode
		}
		if strings.EqualFold(b.Depository, "NSDL") {
			e.DPID = b.DPID
		}
		if b.Pending != "New" {
			e.Timestamp = b.ExchTS
		}
		e.Bids = eipoLines(b, iss, cat)
		if len(e.Bids) == 0 {
			continue
		}
		list = append(list, e)
		acts[b.ID] = b.Pending
		asba = asba || (b.Mode == ModeASBA && b.Pending == "New")
	}
	if len(list) == 0 {
		return IPOBatch{}, invalid("no bids are waiting to go to NSE for %s%s", iss.Name, map[bool]string{true: " (ASBA bids whose bank has no eIPO codes yet go by file)", false: ""}[skipped > 0])
	}
	ups, err := eipoClient.SendBids(ctx, conn.cfg, list)
	if err != nil && len(ups) == 0 {
		o.eipoState(ctx, tenant, func(s *EIPOState) { s.LastSend, s.LastError = now.Format(time.RFC3339), err.Error() })
		return IPOBatch{}, err
	}
	b, berr := o.recordBatch(ctx, tenant, actor, IPOBatch{Issue: iss.ID, Exchange: "NSE", Via: "API", Bids: acts, Lines: len(list)}, now)
	if berr != nil {
		return b, berr
	}
	n, unknown, aerr := o.ApplyIPOUpdates(ctx, tenant, actor, ups)
	if aerr != nil {
		return b, aerr
	}
	b.Note = fmt.Sprintf("%d application(s) sent; %d answered", len(list), n)
	if unknown > 0 {
		b.Note += fmt.Sprintf("; %d answer(s) named no bid", unknown)
	}
	if err != nil {
		b.Note += "; stopped: " + err.Error()
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cIPOBatch, b.ID, b, actor)
	o.eipoState(ctx, tenant, func(s *EIPOState) {
		s.LastSend, s.LastError = now.Format(time.RFC3339), ""
		if err != nil {
			s.LastError = err.Error()
		}
	})
	if asba {
		o.asbaFormsWork(ctx, tenant, iss, now)
	}
	o.log(ctx, tenant, actor, "ipo.eipo_sent", b.ID, map[string]any{"issue": iss.ID, "applications": len(list), "answered": n})
	return b, nil
}

// EIPOInquire asks NSE for the status of an issue's applications in flight.
func (o *Ops) EIPOInquire(ctx context.Context, tenant, actor, issueID string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Sending IPO bids to the exchange", RoleMaker, RoleChecker); err != nil {
		return 0, err
	}
	conn, ok := o.eipoConnection(ctx, tenant)
	if !ok || !conn.usable {
		return 0, fmt.Errorf("%w: set up the NSE eIPO API in Settings → Connections", ErrState)
	}
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return 0, err
	}
	bids, _ := o.issueBids(ctx, tenant, iss.ID)
	var apps []string
	for _, b := range bids {
		if b.Exchange == "NSE" && b.AppNo != "" && (b.Sent != "" || b.Status == BidUploaded || b.Status == BidMandatePending) {
			apps = append(apps, b.AppNo)
		}
	}
	if len(apps) == 0 {
		return 0, nil
	}
	now := o.now()
	ups, err := eipoClient.Inquire(ctx, conn.cfg, iss.Symbol, apps)
	o.eipoState(ctx, tenant, func(s *EIPOState) {
		s.LastInquiry = now.Format(time.RFC3339)
		if err != nil {
			s.LastError = err.Error()
		}
	})
	if err != nil && len(ups) == 0 {
		return 0, err
	}
	n, _, aerr := o.ApplyIPOUpdates(ctx, tenant, actor, ups)
	return n, aerr
}

// EIPOCallbackResult says what a callback did.
type EIPOCallbackResult struct {
	Received int `json:"received"`
	Applied  int `json:"applied"`
	Unknown  int `json:"unknown"`
}

// EIPOCallback takes NSE's eIPO callbacks (appdpstatus, apppaystatus,
// notification; kind "" for an older single address) given only the
// Authorization header; see EIPOCallbackHTTP.
func (o *Ops) EIPOCallback(ctx context.Context, tenant, token, kind, auth, remote string, body []byte) (EIPOCallbackResult, error) {
	h := http.Header{}
	if auth != "" {
		h.Set("Authorization", auth)
	}
	return o.EIPOCallbackHTTP(ctx, tenant, token, kind, h, remote, body)
}

// EIPOCallbackHTTP takes NSE's eIPO callbacks with their headers. The
// address carries the firm's callback token; NSE's Authorization header
// must match the callback password; it must come from a listed address
// when addresses are set. On a v2 connection the callback must also be
// sealed for the firm and signed with NSE's key (advanced security): one
// that fails the check is refused and nothing in it is used. The message
// is kept as received (decrypted, for v2).
func (o *Ops) EIPOCallbackHTTP(ctx context.Context, tenant, token, kind string, h http.Header, remote string, body []byte) (EIPOCallbackResult, error) {
	var res EIPOCallbackResult
	auth := h.Get("Authorization")
	conn, ok := o.eipoConnection(ctx, tenant)
	if !ok || conn.token == "" {
		return res, fmt.Errorf("%w: the NSE eIPO callback is not set up", ErrForbidden)
	}
	if !hmac.Equal([]byte(strings.TrimSpace(token)), []byte(conn.token)) {
		return res, fmt.Errorf("%w: the callback token does not match", ErrForbidden)
	}
	v2 := conn.cfg.Version == "v2"
	// In v2 NSE's signature authenticates the callback; the Authorization
	// of the callback section is checked as well when NSE sends it.
	if kind != "" && conn.cbPassword != "" && (!v2 || auth != "") && !hmac.Equal([]byte(strings.TrimSpace(auth)), []byte(eipoCallbackAuth(conn.cbPassword))) {
		return res, fmt.Errorf("%w: the callback's Authorization does not match the callback password", ErrForbidden)
	}
	if len(conn.ips) > 0 {
		host, _, err := net.SplitHostPort(remote)
		if err != nil {
			host = remote
		}
		if !contains(conn.ips, host) {
			return res, fmt.Errorf("%w: callbacks are taken only from the listed addresses", ErrForbidden)
		}
	}
	if v2 {
		if conn.keyErr != nil {
			return res, fmt.Errorf("%w: the NSE eIPO connection is set to v2 but %s", ErrState, strings.TrimPrefix(conn.keyErr.Error(), ErrInvalid.Error()+": "))
		}
		if m := strings.TrimSpace(h.Get("member")); m != "" && !strings.EqualFold(m, eipoNSEIdent) {
			return res, fmt.Errorf("%w: the callback's member header is %q, not NSE", ErrForbidden, m)
		}
		plain, err := eipoOpen(conn.cfg.MemberKey, conn.cfg.NSEKey, h, body, true, true)
		if err != nil {
			return res, fmt.Errorf("%w: the callback failed the v2 security check (%v); nothing in it was used", ErrForbidden, err)
		}
		body = plain
	}
	sum := sha256.Sum256(body)
	if _, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "IPO-EIPO-CALLBACK", Name: "eipo-" + firstOf(kind, "callback") + "-" + o.now().Format("20060102T150405") + ".json", SHA256: hex.EncodeToString(sum[:]), Content: body}); err != nil {
		return res, err
	}
	if kind == "notification" {
		var nt struct {
			Type   json.Number
			Symbol string
		}
		_ = json.Unmarshal(body, &nt)
		res.Received = 1
		o.eipoState(ctx, tenant, func(s *EIPOState) {
			s.LastCallback, s.Callbacks = o.now().Format(time.RFC3339), s.Callbacks+1
			if nt.Type.String() == "1" { // IPO detail modified: read the master again
				s.MasterRefresh = true
			}
		})
		o.log(ctx, tenant, SystemActor, "ipo.eipo_notification", nt.Symbol, map[string]any{"type": nt.Type.String()})
		return res, nil
	}
	ups := eipoUpdates(body, "eIPO callback")
	res.Received = len(ups)
	if len(ups) == 0 {
		return res, invalid("the callback names no application (no application number)")
	}
	n, unknown, err := o.ApplyIPOUpdates(ctx, tenant, SystemActor, ups)
	if err != nil {
		return res, err
	}
	res.Applied, res.Unknown = n, unknown
	o.eipoState(ctx, tenant, func(s *EIPOState) { s.LastCallback, s.Callbacks = o.now().Format(time.RFC3339), s.Callbacks+1 })
	o.log(ctx, tenant, SystemActor, "ipo.eipo_callback", kind, map[string]any{"received": res.Received, "applied": n, "unknown": unknown})
	return res, nil
}

// eipoDate reads NSE's dd-MM-yyyy.
func eipoDate(s string) string {
	if t, err := time.Parse("02-01-2006", strings.TrimSpace(s)); err == nil {
		return t.Format("2006-01-02")
	}
	return ""
}

// eipoIssue makes an issue from a master record, or false for one
// Ledgerline does not bid (rights, REITs, InvITs, call money, SSE).
func eipoIssue(m EIPOMaster) (IPOIssue, bool) {
	iss := IPOIssue{Symbol: m.Symbol, Name: strings.TrimSpace(m.Name), ISIN: m.ISIN, Exchanges: []string{"NSE"}, Registrar: m.Registrar,
		Open: eipoDate(m.BiddingStartDate), Close: eipoDate(m.BiddingEndDate), OpenTime: hhmmOr(m.DailyStartTime, ""), DayClose: hhmmOr(m.DailyEndTime, "")}
	switch strings.ToUpper(m.IssueType) {
	case "EQUITY", "":
		iss.Type = IssueMainboard
		if strings.EqualFold(m.SubType, "SME") {
			iss.Type = IssueSME
		} else if m.SubType != "" {
			return iss, false
		}
	case "DEBT":
		iss.Type = IssueNCD
		if m.SubType != "" {
			return iss, false
		}
	default:
		return iss, false
	}
	dec := func(n json.Number) money.Dec { d, _ := money.ParseDec(n.String()); return d }
	iss.PriceLow, iss.PriceHigh, iss.Tick = dec(m.MinPrice), dec(m.MaxPrice), dec(m.TickSize)
	if iss.Type == IssueNCD || iss.PriceLow.Sign() == 0 {
		iss.PriceLow = firstDec(iss.PriceLow, dec(m.FaceValue))
	}
	iss.LotSize = m.LotSize
	if m.MinBidQuantity > iss.LotSize {
		iss.LotSize = m.MinBidQuantity // Ledgerline bids in lots of the minimum
	}
	for _, s := range m.SeriesDetails {
		iss.Series = append(iss.Series, s.Code)
	}
	for _, c := range m.CategoryDetails {
		if c.LastDayEndTime != "" {
			iss.CloseTime = hhmmOr(c.LastDayEndTime, "")
		}
	}
	rupees := func(n json.Number) money.Amount { a, _ := money.ParseAmount(firstOf(n.String(), "0")); return a }
	for _, sc := range m.SubCategorySettings {
		if !sc.AllowUpi || sc.SubCatCode == "" {
			continue // Ledgerline's bids are UPI bids
		}
		ind := strings.EqualFold(sc.CaCode, "RETAIL") || strings.EqualFold(sc.CaCode, "INDIV") || strings.EqualFold(sc.SubCatCode, "IND")
		c := IPOCategory{Code: strings.ToUpper(sc.SubCatCode), Name: strings.ToUpper(sc.CaCode) + " · " + strings.ToUpper(sc.SubCatCode), ExchCode: map[string]string{"NSE": strings.ToUpper(sc.SubCatCode)},
			MinAmount: rupees(sc.MinValue), Upto: rupees(sc.MaxValue), CutOff: sc.AllowCutOff, Individual: ind, Modify: map[bool]string{true: "any", false: "up"}[ind]}
		if strings.EqualFold(sc.DiscountType, "A") {
			c.Discount = dec(sc.DiscountPrice)
		}
		iss.Categories = append(iss.Categories, c)
	}
	return iss, iss.Open != "" && iss.Close != ""
}

// EIPOSyncMaster sets up the coming and open issues of NSE's IPO master
// that Ledgerline does not have, and keeps the dates and price band of
// those it set up in step with NSE. Issues set up by staff are left alone.
func (o *Ops) EIPOSyncMaster(ctx context.Context, tenant, actor string) (int, int, error) {
	if err := o.require(ctx, tenant, actor, "Setting up public issues", RoleMaker, RoleChecker); err != nil {
		return 0, 0, err
	}
	conn, ok := o.eipoConnection(ctx, tenant)
	if !ok || !conn.usable {
		return 0, 0, fmt.Errorf("%w: set up the NSE eIPO API in Settings → Connections", ErrState)
	}
	ms, err := eipoClient.Master(ctx, conn.cfg)
	now := o.now()
	if err != nil {
		o.eipoState(ctx, tenant, func(s *EIPOState) { s.LastMaster, s.LastError = now.Format(time.RFC3339), err.Error() })
		return 0, 0, err
	}
	have, _ := docs.List[IPOIssue](ctx, o.Docs, tenant, cIPOIssue, "")
	today := now.In(ist).Format("2006-01-02")
	added, changed := 0, 0
	var problems []string
	for _, m := range ms {
		iss, ok := eipoIssue(m)
		if !ok || iss.Close < today {
			continue
		}
		var old *IPOIssue
		for i := range have {
			if strings.EqualFold(have[i].Symbol, iss.Symbol) && have[i].Close >= today && contains(have[i].Exchanges, "NSE") {
				old = &have[i]
			}
		}
		switch {
		case old == nil:
			if _, err := o.saveIPOIssue(ctx, tenant, SystemActor, iss, "NSE eIPO master"); err != nil {
				problems = append(problems, iss.Symbol+": "+err.Error())
				continue
			}
			added++
		case old.Source == "NSE eIPO master" && (old.Open != iss.Open || old.Close != iss.Close || old.PriceLow.Cmp(iss.PriceLow) != 0 || old.PriceHigh.Cmp(iss.PriceHigh) != 0 || old.LotSize != iss.LotSize):
			u := *old
			u.Open, u.Close, u.PriceLow, u.PriceHigh, u.LotSize, u.Tick = iss.Open, iss.Close, iss.PriceLow, iss.PriceHigh, iss.LotSize, iss.Tick
			if _, err := o.saveIPOIssue(ctx, tenant, SystemActor, u, "NSE eIPO master"); err != nil {
				problems = append(problems, iss.Symbol+": "+err.Error())
				continue
			}
			changed++
		}
	}
	o.eipoState(ctx, tenant, func(s *EIPOState) {
		s.LastMaster, s.MasterNew, s.MasterRefresh, s.LastError = now.Format(time.RFC3339), s.MasterNew+added, false, ""
		if len(problems) > 0 {
			s.LastError = "IPO master: " + problems[0]
		}
	})
	o.log(ctx, tenant, actor, "ipo.eipo_master", today, map[string]any{"added": added, "changed": changed, "problems": len(problems)})
	return added, changed, nil
}

// EIPOSyncAllotment records an issue's allotment from NSE once it is
// published: lots for each allotted application, none for the issue's
// other valid bids. The final issue price comes from the allotment when
// the issue has none.
func (o *Ops) EIPOSyncAllotment(ctx context.Context, tenant, actor, issueID string) (IPOAllotResult, error) {
	var res IPOAllotResult
	if err := o.require(ctx, tenant, actor, "Recording allotments", RoleMaker, RoleChecker); err != nil {
		return res, err
	}
	conn, ok := o.eipoConnection(ctx, tenant)
	if !ok || !conn.usable {
		return res, fmt.Errorf("%w: set up the NSE eIPO API in Settings → Connections", ErrState)
	}
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return res, err
	}
	rows, err := eipoClient.Allotment(ctx, conn.cfg, iss.Symbol)
	now := o.now()
	o.eipoState(ctx, tenant, func(s *EIPOState) {
		s.LastAllot = now.Format(time.RFC3339)
		if err != nil {
			s.LastError = err.Error()
		}
	})
	if err != nil || len(rows) == 0 {
		return res, err
	}
	bids, _ := o.issueBids(ctx, tenant, iss.ID)
	byApp := map[string]IPOApp{}
	for _, b := range bids {
		if b.AppNo != "" {
			byApp[b.AppNo] = b
		}
	}
	lots := map[string]int64{}
	var price money.Dec
	for _, r := range rows {
		b, ok := byApp[string(r.AppNo)]
		if !ok || iss.LotSize <= 0 || !allottable(b) || o.postedAllotment(ctx, tenant, b) {
			continue
		}
		lots[b.ID] += r.Qty / iss.LotSize
		if p, err := money.ParseDec(r.Price.String()); err == nil && p.Sign() > 0 {
			price = p
		}
	}
	for _, b := range bids {
		if _, ok := lots[b.ID]; !ok && allottable(b) && !o.postedAllotment(ctx, tenant, b) {
			lots[b.ID] = 0
		}
	}
	if iss.IssuePrice.Sign() <= 0 && price.Sign() > 0 {
		iss.IssuePrice = price
		if _, err := o.saveIPOIssue(ctx, tenant, SystemActor, iss, firstOf(iss.Source, "Staff")); err != nil {
			return res, err
		}
	}
	return o.RecordIPOAllotment(ctx, tenant, actor, iss.ID, now.In(ist).Format("2006-01-02"), lots)
}
