package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/docs"
)

// ---- NSE OFS Web API (Offer for Sale System, WEB API Protocol v1.3.4) ----
//
// An offer for sale's bids used to be placed by a dealer in the OFS window
// and recorded on the bid. With the API, Ledgerline places them itself:
//
//   - POST /api/auth/token (memberId, userName, password) gives a bearer
//     token with its expiry (epoch milliseconds).
//   - POST /api/order places a bid (series IS non-retail, RS retail, ES
//     employee; CLI, the client code; 100% margin; a market order for a
//     cut-off bid, no price); PUT /api/order changes it by orderId;
//     DELETE /api/order?orderId= cancels it. A reply of "Requested" is an
//     acknowledgement only.
//   - GET /api/query/orders lists the bids RMS approved ("Confirmed",
//     later "Closed"): a bid confirmed there is Accepted.
//
// Production https://eofs.nseindia.com/api, UAT https://eofsuat.nseindia.com/api.

const (
	connNSEOFS = "nse-ofs"
	ofsLive    = "https://eofs.nseindia.com/api"
	ofsUAT     = "https://eofsuat.nseindia.com/api"
)

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connNSEOFS, Name: "NSE OFS API (offers for sale)", Group: "Exchanges", Test: "https",
		Purpose: "Places, changes and cancels clients' offer-for-sale bids in NSE's OFS window, and confirms them from NSE's order book, in place of a dealer. API access from NSE (msm@nse.co.in) for the OFS user ID",
		Fields: []ConnField{{Key: "member", Label: "Member ID"}, {Key: "login", Label: "OFS user ID (up to 5 characters)"}, {Key: "password", Label: "OFS password", Secret: true},
			{Key: "env", Label: "Environment", Options: []string{"Live", "UAT"}},
			{Key: "send", Label: "Place bids through the API", Options: []string{"Off", "On"}},
			{Key: "url", Label: "Address (leave empty for NSE's)", Optional: true, Placeholder: ofsLive}, fNotes}})
}

// OFSConfig is the OFS API connection.
type OFSConfig struct{ Base, Member, Login, Password string }

// OFSOrder is a bid as the OFS API takes and gives it.
type OFSOrder struct {
	OrderID       string      `json:"orderId,omitempty"`
	Symbol        string      `json:"symbol"`
	Series        string      `json:"series"`
	ClientType    string      `json:"clientType"`
	ClientID      string      `json:"clientId"`
	ParticipantID string      `json:"participantId,omitempty"`
	MarginType    string      `json:"marginType"`
	IsMarket      bool        `json:"isMarketOrder"`
	Quantity      int64       `json:"quantity"`
	Price         json.Number `json:"price,omitempty"`
	EchoBack      int64       `json:"apiEchoBack,omitempty"`
	Status        string      `json:"orderStatus,omitempty"`
	MarketOrder   bool        `json:"marketOrder,omitempty"` // in order queries
}

// OFSReply is NSE's answer to a bid: the order, or the error.
type OFSReply struct {
	Order   *OFSOrder `json:"order"`
	Message string    `json:"-"` // the first error, in words
}

// OFSClient talks to NSE's OFS API; replaced in tests.
type OFSClient interface {
	Place(ctx context.Context, c OFSConfig, method string, o OFSOrder) (OFSReply, error)
	Orders(ctx context.Context, c OFSConfig) ([]OFSOrder, error)
}

var ofsClient OFSClient = nseOFSHTTP{client: &http.Client{Timeout: 60 * time.Second}}

type nseOFSHTTP struct{ client *http.Client }

var ofsTokens sync.Map // base|member|login -> eipoToken

func (h nseOFSHTTP) token(ctx context.Context, c OFSConfig, fresh bool) (string, error) {
	k := c.Base + "|" + c.Member + "|" + c.Login
	if t, ok := ofsTokens.Load(k); ok && !fresh && time.Now().Before(t.(eipoToken).exp) {
		return t.(eipoToken).tok, nil
	}
	b, _ := json.Marshal(map[string]string{"memberId": c.Member, "userName": c.Login, "username": c.Login, "password": c.Password})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/auth/token", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach NSE's OFS: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		AccessToken string
		ExpiresAt   int64
		Messages    []json.RawMessage
	}
	if json.Unmarshal(out, &r) != nil || r.AccessToken == "" || resp.StatusCode != 200 {
		return "", fmt.Errorf("NSE's OFS refused the login (%d): %s", resp.StatusCode, trimBody(out))
	}
	exp := time.UnixMilli(r.ExpiresAt).Add(-2 * time.Minute)
	if r.ExpiresAt == 0 || exp.Before(time.Now()) {
		exp = time.Now().Add(10 * time.Minute)
	}
	ofsTokens.Store(k, eipoToken{r.AccessToken, exp})
	return r.AccessToken, nil
}

func (h nseOFSHTTP) do(ctx context.Context, c OFSConfig, method, path string, body any) ([]byte, int, error) {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	for try := 0; try < 2; try++ {
		tok, err := h.token(ctx, c, try > 0)
		if err != nil {
			return nil, 0, err
		}
		req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(b))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := h.client.Do(req)
		if err != nil {
			return nil, 0, fmt.Errorf("could not reach NSE's OFS: %v", err)
		}
		out, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if resp.StatusCode == 401 && try == 0 {
			continue
		}
		return out, resp.StatusCode, nil
	}
	return nil, 0, fmt.Errorf("NSE's OFS did not take a fresh login")
}

func (h nseOFSHTTP) Place(ctx context.Context, c OFSConfig, method string, o OFSOrder) (OFSReply, error) {
	path := "/order"
	var body any = o
	if method == http.MethodDelete {
		q := url.Values{"orderId": {o.OrderID}}
		if o.EchoBack != 0 {
			q.Set("apiEchoBack", strconv.FormatInt(o.EchoBack, 10))
		}
		path, body = "/order?"+q.Encode(), nil
	}
	out, code, err := h.do(ctx, c, method, path, body)
	if err != nil {
		return OFSReply{}, err
	}
	var r struct {
		Messages []struct {
			ErrorKey  string
			ErrorText string
		}
		Order *OFSOrder
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return OFSReply{}, fmt.Errorf("NSE's OFS answered %d: %s", code, trimBody(out))
	}
	rep := OFSReply{Order: r.Order}
	if len(r.Messages) > 0 {
		rep.Message = firstOf(r.Messages[0].ErrorText, r.Messages[0].ErrorKey)
	} else if code >= 300 {
		rep.Message = fmt.Sprintf("NSE's OFS answered %d", code)
	}
	return rep, nil
}

func (h nseOFSHTTP) Orders(ctx context.Context, c OFSConfig) ([]OFSOrder, error) {
	out, code, err := h.do(ctx, c, http.MethodGet, "/query/orders", nil)
	if err != nil {
		return nil, err
	}
	var os []OFSOrder
	if code != 200 || json.Unmarshal(out, &os) != nil {
		return nil, fmt.Errorf("NSE's OFS order book answered %d: %s", code, trimBody(out))
	}
	return os, nil
}

type ofsConn struct {
	cfg  OFSConfig
	send bool
}

func (o *Ops) ofsConnection(ctx context.Context, tenant string) (ofsConn, error) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNSEOFS)
	if err != nil || c.Status == "Not set" {
		return ofsConn{}, fmt.Errorf("%w: set up the NSE OFS API in Settings → Connections", ErrState)
	}
	base := ofsLive
	if c.Values["env"] == "UAT" {
		base = ofsUAT
	}
	if u := strings.TrimRight(strings.TrimSpace(c.Values["url"]), "/"); u != "" {
		base = u
	}
	x := ofsConn{cfg: OFSConfig{Base: base, Member: strings.TrimSpace(c.Values["member"]), Login: strings.TrimSpace(c.Values["login"]), Password: c.Secrets["password"]}, send: c.Values["send"] == "On"}
	if x.cfg.Member == "" || x.cfg.Login == "" || x.cfg.Password == "" {
		return x, invalid("the NSE OFS connection needs the member ID, OFS user ID and password")
	}
	return x, nil
}

// ofsSeries is the OFS series of a bid's category.
func ofsSeries(c IPOCategory) string {
	switch {
	case c.Declare != "" && strings.Contains(strings.ToLower(c.Declare), "employee"):
		return "ES"
	case c.Day == "T+1" || strings.EqualFold(c.Code, "RETAIL"):
		return "RS"
	}
	return "IS"
}

// OFSSendResult says what a send did.
type OFSSendResult struct {
	Sent      int      `json:"sent"`
	Confirmed int      `json:"confirmed"`
	Refused   int      `json:"refused"`
	Problems  []string `json:"problems,omitempty"`
}

// OFSSend places an offer for sale's waiting bids (new, changed, cancelled)
// through NSE's OFS API, then confirms them from NSE's order book.
func (o *Ops) OFSSend(ctx context.Context, tenant, actor, issueID string) (OFSSendResult, error) {
	var res OFSSendResult
	if err := o.require(ctx, tenant, actor, "Sending IPO bids to the exchange", RoleMaker, RoleChecker); err != nil {
		return res, err
	}
	conn, err := o.ofsConnection(ctx, tenant)
	if err != nil {
		return res, err
	}
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return res, err
	}
	if iss.Type != IssueOFS || !contains(iss.Exchanges, "NSE") {
		return res, invalid("%s is not an offer for sale on NSE", iss.Name)
	}
	set, _ := o.Settings(ctx, tenant)
	now := o.now()
	if !iss.liveAt(set, now) {
		return res, fmt.Errorf("%w: bids go to NSE's OFS window only while it is open", ErrState)
	}
	bids, err := o.issueBids(ctx, tenant, iss.ID)
	if err != nil {
		return res, err
	}
	var ups []IPOStatusUpdate
	for _, b := range bids {
		if b.Exchange != "NSE" || b.Pending == "" || b.Mode != ModeMargin {
			continue
		}
		cat, _ := iss.category(b.Category)
		// The OFS window takes non-retail bids on T and retail on T+1.
		day := iss.Open
		if cat.Day == "T+1" {
			day = iss.Close
		}
		if cat.Day != "" && now.In(ist).Format("2006-01-02") != day {
			continue
		}
		ord := OFSOrder{OrderID: b.AppNo, Symbol: iss.Symbol, Series: ofsSeries(cat), ClientType: "CLI", ClientID: b.UCC, MarginType: "1"}
		if n, err := strconv.ParseInt(strings.TrimLeft(strings.TrimPrefix(b.ID, "IPB-"), "0"), 10, 64); err == nil {
			ord.EchoBack = n
		}
		if len(b.Options) > 0 {
			op := b.Options[0]
			ord.Quantity, ord.IsMarket = op.Lots*iss.LotSize, op.CutOff
			if !op.CutOff {
				ord.Price = json.Number(op.Price.Fixed(2))
			}
		}
		method := map[string]string{"New": http.MethodPost, "Modify": http.MethodPut, "Cancel": http.MethodDelete}[b.Pending]
		if method == http.MethodPost {
			ord.OrderID = ""
		} else if ord.OrderID == "" {
			res.Problems = append(res.Problems, b.ID+": no OFS order number to change; place it in the OFS window")
			continue
		}
		rep, err := ofsClient.Place(ctx, conn.cfg, method, ord)
		if err != nil {
			res.Problems = append(res.Problems, b.ID+": "+err.Error())
			break // NSE is not answering: stop for now
		}
		res.Sent++
		u := IPOStatusUpdate{Ref: b.ID, Symbol: iss.Symbol, Activity: b.Pending, Source: "NSE OFS API", At: now.Format(time.RFC3339)}
		switch {
		case rep.Message != "":
			u.Status, u.Reason = "Rejected", rep.Message
			res.Refused++
		case rep.Order != nil && rep.Order.OrderID != "":
			u.AppNo, u.Status = rep.Order.OrderID, "Submitted" // NSE has it ("Requested")
			if b.Pending == "Cancel" {
				u.Status = "Cancelled"
			}
		}
		ups = append(ups, u)
	}
	if len(ups) > 0 {
		if _, _, err := o.ApplyIPOUpdates(ctx, tenant, actor, ups); err != nil {
			return res, err
		}
	}
	n, err := o.ofsConfirm(ctx, tenant, actor, conn, iss)
	res.Confirmed = n
	if err != nil {
		res.Problems = append(res.Problems, err.Error())
	}
	o.log(ctx, tenant, actor, "ipo.ofs_api", iss.ID, map[string]any{"sent": res.Sent, "confirmed": res.Confirmed, "refused": res.Refused})
	return res, nil
}

// ofsConfirm marks bids accepted once NSE's order book shows them confirmed.
func (o *Ops) ofsConfirm(ctx context.Context, tenant, actor string, conn ofsConn, iss IPOIssue) (int, error) {
	orders, err := ofsClient.Orders(ctx, conn.cfg)
	if err != nil {
		return 0, err
	}
	var ups []IPOStatusUpdate
	for _, ord := range orders {
		st := strings.ToUpper(strings.TrimSpace(ord.Status))
		if !strings.EqualFold(ord.Symbol, iss.Symbol) || (st != "CONFIRMED" && st != "CLOSED") || ord.OrderID == "" {
			continue
		}
		ups = append(ups, IPOStatusUpdate{AppNo: ord.OrderID, Symbol: iss.Symbol, Status: "Accepted", Source: "NSE OFS order book"})
	}
	if len(ups) == 0 {
		return 0, nil
	}
	n, _, err := o.ApplyIPOUpdates(ctx, tenant, actor, ups)
	return n, err
}

// ofsTick places waiting OFS bids every two minutes while the window is
// open, when switched on.
func (o *Ops) ofsTick(ctx context.Context, tenant string, iss IPOIssue, set Settings, now time.Time) {
	conn, err := o.ofsConnection(ctx, tenant)
	if err != nil || !conn.send || !iss.liveAt(set, now) {
		return
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIPOState, "ofs:"+iss.ID, EIPOState{})
	if t, err := time.Parse(time.RFC3339, st.LastSend); err == nil && now.Sub(t) < 2*time.Minute {
		return
	}
	res, err := o.OFSSend(ctx, tenant, SystemActor, iss.ID)
	st.LastSend, st.LastError = now.Format(time.RFC3339), ""
	if err != nil {
		st.LastError = err.Error()
	} else if len(res.Problems) > 0 {
		st.LastError = res.Problems[0]
	}
	_ = docs.Upsert(ctx, o.Docs, tenant, cIPOState, "ofs:"+iss.ID, st, SystemActor)
}
