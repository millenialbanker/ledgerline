package ops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// ---- NSE eIPO API (needs NSE's API document) ----
//
// What is public (NSE eIPO FAQ v2.1, July 2025,
// https://nsearchives.nseindia.com/web/sites/default/files/inline-files/eIPO%20-%20FAQ%202.1_0.pdf,
// and circular NSE/IPO/71181 of 7 Nov 2025,
// https://nsearchives.nseindia.com/content/circulars/IPO71181.pdf):
//   - API access: the member emails msm@nse.co.in with its member code and
//     user ID; NSE enables it within a working day. Login without OTP is
//     only for API users, enabled the same way. API users reset passwords
//     like everyone else.
//   - Callback: the member emails its member code, user ID and callback
//     address; NSE whitelists it in 4-5 working days.
//   - The API is listed on the eIPO login page (eipo.nseindia.com); its
//     document is given to members, not published.
//
// Bids are sent with the field names NSE's eIPO bid API uses as relayed in
// public broker documentation (symbol, category, clientName, depository,
// dpId, clientBenId, nonASBA, pan, referenceNumber, allotmentMode, upiFlag,
// upi, timestamp, bids: activityType, quantity, atCutOff, price, amount),
// signed in as NSE's published eIPO query API does (HTTP Basic,
// member^login:password; Web API Document for NSE EIPO Query Server
// v1.0.2) unless the firm enters a key. The endpoint paths and the reply
// layout are in NSE's document: until the firm enters the paths in
// Connections the client refuses to send, and replies and callbacks are
// read leniently by field name. None of this has been run against NSE:
// it is tested only against a fake. Until NSE's document is in hand, bids
// go by file (ipoexchange.go).

const connNSEEIPO = "nse-eipo"

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connNSEEIPO, Name: "NSE eIPO API (public issues)", Group: "Exchanges", Test: "https",
		Purpose: "Sends IPO, SME and NCD bids to NSE's eIPO and takes bid and UPI mandate status from NSE's callback. Ask msm@nse.co.in for API access (member code, user ID), login without OTP and the callback address to whitelist. NSE gives its API document to members: enter the addresses from it here. Until then bids go by file",
		Fields: []ConnField{{Key: "member", Label: "Member code"}, {Key: "login", Label: "eIPO user ID enabled for the API"},
			{Key: "password", Label: "eIPO password", Secret: true}, {Key: "api_key", Label: "API key or token, if NSE's API document asks for one", Secret: true, Optional: true},
			{Key: "env", Label: "Environment", Options: []string{"Live", "Mock (UAT)"}},
			{Key: "url", Label: "API address (from NSE's API document)", Optional: true, Placeholder: "https://eipo.nseindia.com/…"},
			{Key: "bid_path", Label: "Bid endpoint path (from NSE's API document)", Optional: true, Placeholder: "/…"},
			{Key: "inquiry_path", Label: "Bid inquiry endpoint path (from NSE's API document)", Optional: true, Placeholder: "/…"},
			{Key: "callback_token", Label: "Callback token: a long random phrase that goes in the callback address given to NSE", Secret: true, Optional: true},
			{Key: "callback_ips", Label: "Addresses NSE calls back from (comma separated; empty allows any)", Optional: true},
			{Key: "send", Label: "Send bids through the API", Options: []string{"Off", "On"}}, fNotes}})
}

// EIPOConfig is the firm's eIPO API connection.
type EIPOConfig struct {
	Base, Member, Login, Password, APIKey, BidPath, InquiryPath string
	Live                                                        bool
}

// EIPOBidLine is one option of a bid as the API takes it.
type EIPOBidLine struct {
	ActivityType string      `json:"activityType"` // new, modify, cancel
	BidRef       string      `json:"bidReferenceNumber,omitempty"`
	Quantity     int64       `json:"quantity"`
	AtCutOff     bool        `json:"atCutOff"`
	Price        json.Number `json:"price"`
	Amount       json.Number `json:"amount"`
	Series       string      `json:"series,omitempty"`
	Remark       string      `json:"remark,omitempty"`
}

// EIPOBid is one application as the API takes it.
type EIPOBid struct {
	Symbol        string        `json:"symbol"`
	Category      string        `json:"category"`
	SubCategory   string        `json:"subCategory,omitempty"`
	AppNo         string        `json:"applicationNumber,omitempty"`
	ClientName    string        `json:"clientName"`
	Depository    string        `json:"depository"`
	DPID          string        `json:"dpId"`
	ClientBenID   string        `json:"clientBenId"`
	NonASBA       bool          `json:"nonASBA"`
	PAN           string        `json:"pan"`
	Ref           string        `json:"referenceNumber"`
	AllotmentMode string        `json:"allotmentMode"`
	UPIFlag       string        `json:"upiFlag"`
	UPI           string        `json:"upi,omitempty"`
	Timestamp     string        `json:"timestamp"`
	Bids          []EIPOBidLine `json:"bids"`
}

// EIPOClient talks to NSE's eIPO API.
type EIPOClient interface {
	SendBids(ctx context.Context, c EIPOConfig, bids []EIPOBid) ([]IPOStatusUpdate, error)
	Inquire(ctx context.Context, c EIPOConfig, symbol string, refs []string) ([]IPOStatusUpdate, error)
}

// eipoClient is replaced in tests.
var eipoClient EIPOClient = nseEIPOHTTP{client: &http.Client{Timeout: 60 * time.Second}}

// ErrEIPOSpec means NSE's API document is needed before the API is used.
var ErrEIPOSpec = errors.New("NSE's eIPO API document is needed: ask msm@nse.co.in for API access and enter the API address and endpoint paths from NSE's document in Settings → Connections (NSE eIPO API); until then send bids by file")

type nseEIPOHTTP struct{ client *http.Client }

func (h nseEIPOHTTP) do(ctx context.Context, c EIPOConfig, path string, body any) ([]byte, error) {
	if c.Base == "" || path == "" {
		return nil, fmt.Errorf("%w: %v", ErrState, ErrEIPOSpec)
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Base, "/")+"/"+strings.TrimLeft(path, "/"), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	} else {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.Member+"^"+c.Login+":"+c.Password)))
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach NSE's eIPO: %v", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("NSE's eIPO refused the login (%d): check the user ID, password and that API access and login without OTP are enabled", resp.StatusCode)
	}
	if resp.StatusCode >= 300 && len(eipoUpdates(out, "")) == 0 {
		return nil, fmt.Errorf("NSE's eIPO answered %d: %s", resp.StatusCode, trimBody(out))
	}
	return out, nil
}

func (h nseEIPOHTTP) SendBids(ctx context.Context, c EIPOConfig, bids []EIPOBid) ([]IPOStatusUpdate, error) {
	var out []IPOStatusUpdate
	for _, b := range bids {
		reply, err := h.do(ctx, c, c.BidPath, b)
		if err != nil {
			return out, err
		}
		ups := eipoUpdates(reply, "eIPO API")
		if len(ups) == 0 {
			// A reply that names no bid speaks for the one sent; with no
			// status in it the bid waits for the callback or an inquiry.
			var top map[string]any
			if json.Unmarshal(reply, &top) == nil {
				s := func(k ...string) string {
					for _, n := range k {
						for kk, v := range top {
							if strings.EqualFold(kk, n) && v != nil {
								return strings.TrimSpace(fmt.Sprint(v))
							}
						}
					}
					return ""
				}
				if st := s("status"); st != "" {
					ups = []IPOStatusUpdate{{Status: st, Code: s("reasonCode", "errorCode"), Reason: s("reason", "message", "errorMessage"), Source: "eIPO API"}}
				}
			}
		}
		for i := range ups {
			if ups[i].Ref == "" {
				ups[i].Ref = b.Ref // the reply is about the bid sent
			}
			if ups[i].Activity == "" && len(b.Bids) > 0 {
				ups[i].Activity = b.Bids[0].ActivityType
			}
		}
		out = append(out, ups...)
	}
	return out, nil
}

func (h nseEIPOHTTP) Inquire(ctx context.Context, c EIPOConfig, symbol string, refs []string) ([]IPOStatusUpdate, error) {
	reply, err := h.do(ctx, c, c.InquiryPath, map[string]any{"symbol": symbol, "referenceNumbers": refs})
	if err != nil {
		return nil, err
	}
	return eipoUpdates(reply, "eIPO inquiry"), nil
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
					Status: get(t, "status", "bidStatus", "orderStatus"), Code: get(t, "reasonCode", "errorCode"), Reason: get(t, "reason", "statusMessage", "rejectReason", "errorMessage", "message"),
					PayStatus: get(t, "upiPaymentStatus", "upiStatus", "payStatus", "paymentStatus", "mandateStatus"), UMN: get(t, "umn"),
					DPStatus: get(t, "dpVerStatusFlag", "dpVerStatus", "dpStatus"), DPReason: get(t, "dpVerReason", "dpVerFailReason"),
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
	cfg    EIPOConfig
	token  string
	ips    []string
	send   bool
	usable bool // the endpoint paths are entered
}

func (o *Ops) eipoConnection(ctx context.Context, tenant string) (eipoConn, bool) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNSEEIPO)
	if err != nil || c.Status == "Not set" {
		return eipoConn{}, false
	}
	x := eipoConn{cfg: EIPOConfig{Base: strings.TrimRight(c.Values["url"], "/"), Member: c.Values["member"], Login: c.Values["login"], Password: c.Secrets["password"], APIKey: c.Secrets["api_key"],
		BidPath: c.Values["bid_path"], InquiryPath: c.Values["inquiry_path"], Live: c.Values["env"] != "Mock (UAT)"}, token: c.Secrets["callback_token"], send: c.Values["send"] == "On"}
	for _, ip := range strings.Split(c.Values["callback_ips"], ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			x.ips = append(x.ips, ip)
		}
	}
	x.usable = x.cfg.Base != "" && x.cfg.BidPath != ""
	return x, true
}

// EIPOState is the API's last activity.
type EIPOState struct {
	LastSend     string `json:"last_send,omitempty"`
	LastInquiry  string `json:"last_inquiry,omitempty"`
	LastCallback string `json:"last_callback,omitempty"`
	Callbacks    int    `json:"callbacks"`
	LastError    string `json:"last_error,omitempty"`
}

// EIPOView is the API's state for the screen.
type EIPOView struct {
	Set      bool      `json:"set"`
	Send     bool      `json:"send"`
	Usable   bool      `json:"usable"` // NSE's endpoints are entered
	Callback string    `json:"callback"`
	Token    bool      `json:"callback_token_set"`
	State    EIPOState `json:"state"`
	Needs    string    `json:"needs,omitempty"`
}

// EIPOStatus is the API connection's state.
func (o *Ops) EIPOStatus(ctx context.Context, tenant string) EIPOView {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIPOState, "eipo", EIPOState{})
	c, ok := o.eipoConnection(ctx, tenant)
	v := EIPOView{Set: ok, Send: c.send, Usable: c.usable, Token: c.token != "", State: st, Callback: "/hooks/" + tenant + "/eipo?token=<callback token>"}
	if !c.usable {
		v.Needs = ErrEIPOSpec.Error()
	}
	return v
}

func (o *Ops) eipoState(ctx context.Context, tenant string, fn func(*EIPOState)) {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIPOState, "eipo", EIPOState{})
	fn(&st)
	_ = docs.Upsert(ctx, o.Docs, tenant, cIPOState, "eipo", st, SystemActor)
}

// EIPOSend sends an issue's waiting UPI bids to NSE through the API (ASBA
// bids through a bank account go by file).
func (o *Ops) EIPOSend(ctx context.Context, tenant, actor, issueID string) (IPOBatch, error) {
	if err := o.require(ctx, tenant, actor, "Sending IPO bids to the exchange", RoleMaker, RoleChecker); err != nil {
		return IPOBatch{}, err
	}
	conn, ok := o.eipoConnection(ctx, tenant)
	if !ok {
		return IPOBatch{}, fmt.Errorf("%w: set up the NSE eIPO API in Settings → Connections", ErrState)
	}
	if !conn.usable {
		return IPOBatch{}, fmt.Errorf("%w: %v", ErrState, ErrEIPOSpec)
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
	skipped := 0
	for _, b := range bids {
		if b.Mode != ModeUPI {
			skipped++
			continue
		}
		cat, _ := iss.category(b.Category)
		e := EIPOBid{Symbol: iss.Symbol, Category: cat.exch("NSE"), SubCategory: cat.SubCode, AppNo: b.AppNo, ClientName: b.Name, Depository: b.Depository, DPID: b.DPID, ClientBenID: b.BenID,
			PAN: b.PAN, Ref: b.ID, AllotmentMode: "demat", UPIFlag: "Y", UPI: b.UPI, Timestamp: now.Format("02-01-2006 15:04:05")}
		for _, op := range b.Options {
			eff := op.Price.Sub(cat.Discount)
			e.Bids = append(e.Bids, EIPOBidLine{ActivityType: strings.ToLower(b.Pending), Quantity: op.Lots * iss.LotSize, AtCutOff: op.CutOff, Price: json.Number(op.Price.Fixed(2)),
				Amount: json.Number(eff.MulInt(op.Lots * iss.LotSize).Fixed(2)), Series: op.Series})
		}
		list = append(list, e)
		acts[b.ID] = b.Pending
	}
	if len(list) == 0 {
		return IPOBatch{}, invalid("no UPI bids are waiting to go to NSE for %s%s", iss.Name, map[bool]string{true: " (ASBA bids go by file)", false: ""}[skipped > 0])
	}
	ups, err := eipoClient.SendBids(ctx, conn.cfg, list)
	if err != nil {
		o.eipoState(ctx, tenant, func(s *EIPOState) { s.LastSend, s.LastError = now.Format(time.RFC3339), err.Error() })
		return IPOBatch{}, err
	}
	b, err := o.recordBatch(ctx, tenant, actor, IPOBatch{Issue: iss.ID, Exchange: "NSE", Via: "API", Bids: acts, Lines: len(list)}, now)
	if err != nil {
		return b, err
	}
	n, unknown, err := o.ApplyIPOUpdates(ctx, tenant, actor, ups)
	if err != nil {
		return b, err
	}
	b.Note = fmt.Sprintf("%d bid(s) sent; %d answered", len(list), n)
	if unknown > 0 {
		b.Note += fmt.Sprintf("; %d answer(s) named no bid", unknown)
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cIPOBatch, b.ID, b, actor)
	o.eipoState(ctx, tenant, func(s *EIPOState) { s.LastSend, s.LastError = now.Format(time.RFC3339), "" })
	o.log(ctx, tenant, actor, "ipo.eipo_sent", b.ID, map[string]any{"issue": iss.ID, "bids": len(list), "answered": n})
	return b, nil
}

// EIPOInquire asks NSE for the status of an issue's bids in flight.
func (o *Ops) EIPOInquire(ctx context.Context, tenant, actor, issueID string) (int, error) {
	if err := o.require(ctx, tenant, actor, "Sending IPO bids to the exchange", RoleMaker, RoleChecker); err != nil {
		return 0, err
	}
	conn, ok := o.eipoConnection(ctx, tenant)
	if !ok || !conn.usable || conn.cfg.InquiryPath == "" {
		return 0, fmt.Errorf("%w: %v", ErrState, ErrEIPOSpec)
	}
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return 0, err
	}
	bids, _ := o.issueBids(ctx, tenant, iss.ID)
	var refs []string
	for _, b := range bids {
		if b.Exchange == "NSE" && (b.Sent != "" || b.Status == BidUploaded || b.Status == BidMandatePending) {
			refs = append(refs, b.ID)
		}
	}
	if len(refs) == 0 {
		return 0, nil
	}
	now := o.now()
	ups, err := eipoClient.Inquire(ctx, conn.cfg, iss.Symbol, refs)
	o.eipoState(ctx, tenant, func(s *EIPOState) {
		s.LastInquiry = now.Format(time.RFC3339)
		if err != nil {
			s.LastError = err.Error()
		}
	})
	if err != nil {
		return 0, err
	}
	n, _, err := o.ApplyIPOUpdates(ctx, tenant, actor, ups)
	return n, err
}

// EIPOCallbackResult says what a callback did.
type EIPOCallbackResult struct {
	Received int `json:"received"`
	Applied  int `json:"applied"`
	Unknown  int `json:"unknown"`
}

// EIPOCallback takes NSE's eIPO callback with bid and mandate status. NSE
// calls the address the firm had whitelisted; it must carry the firm's
// callback token, and come from a listed address when addresses are set.
// The message is kept as received.
func (o *Ops) EIPOCallback(ctx context.Context, tenant, token, remote string, body []byte) (EIPOCallbackResult, error) {
	var res EIPOCallbackResult
	conn, ok := o.eipoConnection(ctx, tenant)
	if !ok || conn.token == "" {
		return res, fmt.Errorf("%w: the NSE eIPO callback is not set up", ErrForbidden)
	}
	if !hmac.Equal([]byte(strings.TrimSpace(token)), []byte(conn.token)) {
		return res, fmt.Errorf("%w: the callback token does not match", ErrForbidden)
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
	sum := sha256.Sum256(body)
	if _, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "IPO-EIPO-CALLBACK", Name: "eipo-callback-" + o.now().Format("20060102T150405") + ".json", SHA256: hex.EncodeToString(sum[:]), Content: body}); err != nil {
		return res, err
	}
	ups := eipoUpdates(body, "eIPO callback")
	res.Received = len(ups)
	if len(ups) == 0 {
		return res, invalid("the callback names no bid (no reference or application number)")
	}
	n, unknown, err := o.ApplyIPOUpdates(ctx, tenant, SystemActor, ups)
	if err != nil {
		return res, err
	}
	res.Applied, res.Unknown = n, unknown
	o.eipoState(ctx, tenant, func(s *EIPOState) { s.LastCallback, s.Callbacks = o.now().Format(time.RFC3339), s.Callbacks+1 })
	o.log(ctx, tenant, SystemActor, "ipo.eipo_callback", "", map[string]any{"received": res.Received, "applied": n, "unknown": unknown})
	return res, nil
}
