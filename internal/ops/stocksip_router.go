package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/xfile"
)

// ---- stock SIPs: handing orders to the trading front end ----
//
// Ledgerline is a back office and does not place orders: SIP orders go to
// the firm's OMS through an OrderRouter.
//
//   - Basket file: a CSV in the layout the firm's OMS takes for basket or
//     bulk orders (ODIN, NEST and others accept them, each in its own
//     layout). The layout is a spec in config/formats; stocksip-basket is
//     a generic one, and a firm whose OMS needs another copies it with its
//     own column names (keys stay the same). The dealer uploads the file.
//   - Symphony XTS: the XTS Interactive API's order entry (login with the
//     app key and secret key for a session token, then one order per SIP
//     instruction), as in Symphony's public SDK and API pages.
//   - Other OMS API: kept as a record of the firm's OMS until its vendor
//     publishes (or gives the firm) an order-entry API; until then SIP
//     orders wait for a basket file.

const (
	connSIPXTS   = "oms-xts"
	connSIPOther = "oms-api-other"
)

func init() {
	ConnProviders = append(ConnProviders,
		ConnProvider{ID: connSIPXTS, Name: "Symphony XTS order entry (stock SIPs)", Group: "Trading", Test: "https",
			Purpose: "Places stock SIP orders through the XTS Interactive API with a dealer login. The address is your broker's or Symphony's XTS server; the app key and secret key are the interactive API's (from the XTS dashboard). Upload the OMS instrument IDs on Stock SIPs → Lists",
			Fields: []ConnField{{Key: "url", Label: "XTS address (e.g. https://<your XTS host>)", Placeholder: "https://…"}, {Key: "api_key", Label: "App key (interactive)", Secret: true},
				{Key: "secret", Label: "Secret key (interactive)", Secret: true}, {Key: "source", Label: "Source (default WEBAPI)", Optional: true, Placeholder: "WEBAPI"}, fNotes}},
		ConnProvider{ID: connSIPOther, Name: "Other OMS order API (stock SIPs)", Group: "Trading", Test: "none",
			Purpose: "A record of the order-entry API the firm's OMS would need for stock SIPs. Not built: the vendor's API is not public. Until it is, SIP orders go by basket file",
			Fields:  []ConnField{fVend("Finspot", "ODIN (63 moons)", "Omnesys NEST", "Greek", "Other"), {Key: "contact", Label: "Vendor contact for the API specification", Optional: true}, fNotes}},
	)
}

// OrderRouter hands SIP orders to the trading front end.
type OrderRouter interface {
	Name() string
	// Route sends the orders. A file router returns the file the dealer
	// uploads; an API router the outcome of each order.
	Route(ctx context.Context, orders []SIPOrder) (RouteResult, error)
}

// RouteResult is what a router did.
type RouteResult struct {
	FileName string                  `json:"file_name,omitempty"`
	File     []byte                  `json:"-"`
	Outcomes map[string]RouteOutcome `json:"outcomes"` // by order ID
}

// RouteOutcome is one order's hand-off.
type RouteOutcome struct {
	Placed   bool   `json:"placed"`
	OMSOrder string `json:"oms_order,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ---- basket file ----

type basketRouter struct {
	spec *xfile.Spec
	date string
	seq  int
}

func (b *basketRouter) Name() string { return "Basket file (" + b.spec.Title + ")" }

func sipBasketSpec(id string) (*xfile.Spec, error) {
	lib, err := XFiles()
	if err != nil {
		return nil, err
	}
	s, ok := lib.Get(id)
	if !ok || s.Direction != "out" || !strings.HasPrefix(s.ID, "stocksip-basket") || s.Format != "delimited" {
		return nil, invalid("basket layout %q is not in config/formats (an outgoing layout whose id starts stocksip-basket)", id)
	}
	return s, nil
}

// SIPBasketFormats lists the basket layouts available.
func SIPBasketFormats() []string {
	lib, err := XFiles()
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range lib.Specs {
		if s.Direction == "out" && strings.HasPrefix(s.ID, "stocksip-basket") {
			out = append(out, s.ID)
		}
	}
	return out
}

// sipBasketRow is one order as the basket layout's keys.
func sipBasketRow(x SIPOrder) xfile.Row {
	price := ""
	if x.OrderType == "LIMIT" {
		price = x.Limit.Fixed(2)
	}
	return xfile.Row{Values: map[string]string{"date": x.Date, "ucc": x.UCC, "exchange": x.Exchange, "segment": "CM", "symbol": x.Symbol, "series": "EQ", "isin": x.ISIN,
		"side": "BUY", "qty": strconv.FormatInt(x.Qty, 10), "order_type": x.OrderType, "price": price, "trigger": "", "product": "CNC", "validity": "DAY",
		"disclosed_qty": "0", "protection_pct": x.Protect.String(), "algo_id": x.AlgoID, "ref": x.Ref, "name": x.Name}}
}

func (b *basketRouter) Route(ctx context.Context, orders []SIPOrder) (RouteResult, error) {
	rr := RouteResult{Outcomes: map[string]RouteOutcome{}}
	rows := make([]xfile.Row, 0, len(orders))
	for _, x := range orders {
		rows = append(rows, sipBasketRow(x))
		rr.Outcomes[x.ID] = RouteOutcome{Placed: true}
	}
	content, err := b.spec.Write(rows)
	if err != nil {
		return rr, invalid("the basket layout %s cannot be written: %v", b.spec.ID, err)
	}
	name, err := b.spec.FileName(xfile.Vars{"date": b.date, "seq": strconv.Itoa(b.seq)})
	if err != nil {
		return rr, err
	}
	rr.FileName, rr.File = name, content
	return rr, nil
}

// ---- Symphony XTS ----
//
// From Symphony's public XTS API material: the SDK
// (https://github.com/symphonyfintech/xts-pythonclient-api-sdk, Connect.py)
// logs in with POST /interactive/user/session {appKey, secretKey, source}
// and reads result.token, result.userID and result.isInvestorClient; the
// token goes in the Authorization header as it is. Orders are POST
// /interactive/orders with exchangeSegment, exchangeInstrumentID,
// productType, orderType, orderSide, timeInForce, disclosedQuantity,
// orderQuantity, limitPrice, stopPrice, orderUniqueIdentifier and, for a
// dealer login, clientID. The API documentation
// (https://symphonyfintech.com/xts-trading-front-end-api-v2/) gives the
// response envelope {type, code, description, result} and AppOrderID as
// the order's ID in the order entry response. Tested here only against a
// fake XTS server.

// sipXTSHTTP is replaced in tests.
var sipXTSHTTP = &http.Client{Timeout: 20 * time.Second}

type xtsRouter struct {
	base, appKey, secret, source string
	tokens                       map[string]int64 // exchange|symbol -> exchangeInstrumentID
}

func (x *xtsRouter) Name() string { return "Symphony XTS" }

type xtsEnvelope struct {
	Type        string          `json:"type"`
	Code        string          `json:"code"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func (x *xtsRouter) call(ctx context.Context, path, token string, body any) (xtsEnvelope, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.base+path, bytes.NewReader(b))
	if err != nil {
		return xtsEnvelope{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, err := sipXTSHTTP.Do(req)
	if err != nil {
		return xtsEnvelope{}, fmt.Errorf("XTS did not answer: %v", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env xtsEnvelope
	if err := json.Unmarshal(rb, &env); err != nil {
		return env, fmt.Errorf("XTS answered %d: %s", resp.StatusCode, trimBody(rb))
	}
	if resp.StatusCode != 200 || !strings.EqualFold(env.Type, "success") {
		return env, fmt.Errorf("XTS refused (%d %s): %s", resp.StatusCode, env.Code, firstOf(env.Description, trimBody(rb)))
	}
	return env, nil
}

func (x *xtsRouter) login(ctx context.Context) (string, bool, error) {
	env, err := x.call(ctx, "/interactive/user/session", "", map[string]string{"appKey": x.appKey, "secretKey": x.secret, "source": firstOf(x.source, "WEBAPI")})
	if err != nil {
		return "", false, err
	}
	var r struct {
		Token    string `json:"token"`
		UserID   string `json:"userID"`
		Investor bool   `json:"isInvestorClient"`
	}
	if json.Unmarshal(env.Result, &r) != nil || r.Token == "" {
		return "", false, fmt.Errorf("no session token in XTS's reply")
	}
	return r.Token, r.Investor, nil
}

var xtsSegments = map[string]string{"NSE": "NSECM", "BSE": "BSECM"}

func (x *xtsRouter) Route(ctx context.Context, orders []SIPOrder) (RouteResult, error) {
	rr := RouteResult{Outcomes: map[string]RouteOutcome{}}
	token, investor, err := x.login(ctx)
	if err != nil {
		return rr, fmt.Errorf("%w: XTS login: %v", ErrState, err)
	}
	if investor {
		return rr, fmt.Errorf("%w: the XTS login is a client (investor) login; SIP orders for many clients need a dealer login", ErrState)
	}
	for _, o := range orders {
		id, ok := x.tokens[o.Exchange+"|"+o.Symbol]
		if !ok {
			rr.Outcomes[o.ID] = RouteOutcome{Error: "no XTS instrument ID for " + o.Exchange + " " + o.Symbol + " (upload the OMS instrument list)"}
			continue
		}
		limit := 0.0
		if o.OrderType == "LIMIT" {
			limit = o.Limit.Float64()
		}
		body := map[string]any{"exchangeSegment": xtsSegments[o.Exchange], "exchangeInstrumentID": id, "productType": "CNC", "orderType": o.OrderType,
			"orderSide": "BUY", "timeInForce": "DAY", "disclosedQuantity": 0, "orderQuantity": o.Qty, "limitPrice": limit, "stopPrice": 0,
			"orderUniqueIdentifier": o.Ref, "clientID": o.UCC}
		env, err := x.call(ctx, "/interactive/orders", token, body)
		if err != nil {
			rr.Outcomes[o.ID] = RouteOutcome{Error: err.Error()}
			continue
		}
		var r struct {
			AppOrderID json.Number `json:"AppOrderID"`
		}
		d := json.NewDecoder(bytes.NewReader(env.Result))
		d.UseNumber()
		_ = d.Decode(&r)
		rr.Outcomes[o.ID] = RouteOutcome{Placed: true, OMSOrder: r.AppOrderID.String()}
	}
	return rr, nil
}

// ---- other OMS ----

type otherRouter struct{ vendor string }

func (r otherRouter) Name() string { return firstOf(r.vendor, "Other OMS") + " API" }

func (r otherRouter) Route(context.Context, []SIPOrder) (RouteResult, error) {
	return RouteResult{}, fmt.Errorf("%w: %s's order-entry API is not public, so Ledgerline cannot send SIP orders to it; make a basket file instead, and ask the vendor for the API specification", ErrState, firstOf(r.vendor, "the OMS"))
}

// sipRouter is the firm's router as set up.
func (o *Ops) sipRouter(ctx context.Context, tenant string, cfg SIPSettings, date string, seq int) (OrderRouter, error) {
	switch cfg.Router {
	case "xts":
		c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connSIPXTS)
		if err != nil || c.Status == "Not set" || c.Values["url"] == "" || c.Secrets["api_key"] == "" || c.Secrets["secret"] == "" {
			return nil, fmt.Errorf("%w: set up Symphony XTS order entry in Settings → Connections", ErrState)
		}
		r := &xtsRouter{base: strings.TrimRight(c.Values["url"], "/"), appKey: c.Secrets["api_key"], secret: c.Secrets["secret"], source: c.Values["source"], tokens: map[string]int64{}}
		for _, it := range o.sipList(ctx, tenant, "tokens").Items {
			if n, err := strconv.ParseInt(it.Token, 10, 64); err == nil {
				r.tokens[firstOf(it.Exchange, "NSE")+"|"+it.Symbol] = n
			}
		}
		return r, nil
	case "other":
		c, _, _ := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connSIPOther)
		return otherRouter{vendor: c.Values["provider"]}, nil
	}
	s, err := sipBasketSpec(cfg.BasketFormat)
	if err != nil {
		return nil, err
	}
	return &basketRouter{spec: s, date: date, seq: seq}, nil
}

// ---- sending ----

// SIPBatch is one hand-off of orders: a basket file or an API run.
type SIPBatch struct {
	ID        string   `json:"id"`
	Date      string   `json:"date"`
	Router    string   `json:"router"`
	FileName  string   `json:"file_name,omitempty"`
	Content   []byte   `json:"content,omitempty"`
	Orders    []string `json:"orders"`
	Placed    int      `json:"placed"`
	Refused   int      `json:"refused"`
	Status    string   `json:"status"` // Sent, Withdrawn
	CreatedBy string   `json:"created_by"`
	CreatedAt string   `json:"created_at"`
}

// SIPSendResult is what sending did.
type SIPSendResult struct {
	Batch    string `json:"batch"`
	Router   string `json:"router"`
	FileName string `json:"file_name,omitempty"`
	Placed   int    `json:"placed"`
	Refused  int    `json:"refused"`
	Message  string `json:"message"`
}

// SendStockSIPOrders hands the day's waiting SIP orders to the OMS: a
// basket file for the dealer to upload, or the order API.
func (o *Ops) SendStockSIPOrders(ctx context.Context, tenant, actor, date string) (SIPSendResult, error) {
	var res SIPSendResult
	if actor != SystemActor {
		if err := o.require(ctx, tenant, actor, "Sending stock SIP orders to the OMS", RoleMaker, RoleChecker, RoleRisk); err != nil {
			return res, err
		}
	}
	if date != o.now().Format("2006-01-02") {
		return res, invalid("orders are sent to the OMS on their trade date (%s)", o.now().Format("2006-01-02"))
	}
	all, err := docs.List[SIPOrder](ctx, o.Docs, tenant, cSIPOrder, date+"|")
	if err != nil {
		return res, err
	}
	var pend []SIPOrder
	for _, x := range all {
		if x.Status == SIPOrdPending {
			pend = append(pend, x)
		}
	}
	if len(pend) == 0 {
		return res, fmt.Errorf("%w: no SIP orders are waiting to go to the OMS for %s", ErrState, date)
	}
	sort.Slice(pend, func(i, j int) bool { return pend[i].Ref < pend[j].Ref })
	cfg := o.sipSettings(ctx, tenant)
	n, err := o.next(ctx, tenant, "stocksip-batch-"+date, actor)
	if err != nil {
		return res, err
	}
	r, err := o.sipRouter(ctx, tenant, cfg, date, int(n))
	if err != nil {
		return res, err
	}
	rr, err := r.Route(ctx, pend)
	if err != nil {
		o.log(ctx, tenant, actor, "stocksip.send.failed", date, map[string]any{"router": r.Name(), "error": err.Error()})
		return res, err
	}
	now := o.now().Format(time.RFC3339)
	b := SIPBatch{ID: fmt.Sprintf("SB-%s-%02d", strings.ReplaceAll(date, "-", ""), n), Date: date, Router: r.Name(), FileName: rr.FileName, Content: rr.File, Status: "Sent", CreatedBy: actor, CreatedAt: now}
	for _, x := range pend {
		out := rr.Outcomes[x.ID]
		b.Orders = append(b.Orders, x.ID)
		if !out.Placed {
			b.Refused++
			x.Note = "OMS refused: " + out.Error
			x.UpdatedAt = now
			_ = docs.Upsert(ctx, o.Docs, tenant, cSIPOrder, x.ID, x, actor)
			continue
		}
		b.Placed++
		x.Status, x.Router, x.Batch, x.OMSOrder, x.PlacedAt, x.Note, x.UpdatedAt = SIPOrdPlaced, r.Name(), b.ID, out.OMSOrder, now, "", now
		if err := docs.Upsert(ctx, o.Docs, tenant, cSIPOrder, x.ID, x, actor); err != nil {
			return res, err
		}
		price := x.Price.Fixed(2)
		if x.OrderType == "LIMIT" {
			price = x.Limit.Fixed(2) + " (limit)"
		}
		o.sipNotify(ctx, tenant, actor, "stocksip.placed", x.UCC, map[string]string{"scrip": x.Symbol, "qty": strconv.FormatInt(x.Qty, 10), "price": price, "date": fmtDay(x.Date), "ref": x.Ref})
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSIPBatch, b.ID, b, actor); err != nil {
		return res, err
	}
	o.sipOpenDate(ctx, tenant, date, true)
	res = SIPSendResult{Batch: b.ID, Router: b.Router, FileName: b.FileName, Placed: b.Placed, Refused: b.Refused}
	res.Message = fmt.Sprintf("%d orders sent by %s", b.Placed, b.Router)
	if b.FileName != "" {
		res.Message = fmt.Sprintf("basket file %s with %d orders: upload it in the OMS", b.FileName, b.Placed)
	}
	if b.Refused > 0 {
		res.Message += fmt.Sprintf("; %d refused (still waiting: see each order's note)", b.Refused)
	}
	o.log(ctx, tenant, actor, "stocksip.sent", b.ID, map[string]any{"router": b.Router, "placed": b.Placed, "refused": b.Refused, "file": b.FileName})
	return res, nil
}

// StockSIPBatches lists hand-offs, newest first (without file content).
func (o *Ops) StockSIPBatches(ctx context.Context, tenant string) ([]SIPBatch, error) {
	bs, err := docs.List[SIPBatch](ctx, o.Docs, tenant, cSIPBatch, "")
	sort.Slice(bs, func(i, j int) bool { return bs[i].ID > bs[j].ID })
	for i := range bs {
		bs[i].Content = nil
	}
	return bs, err
}

// StockSIPBasket returns a basket file to download again.
func (o *Ops) StockSIPBasket(ctx context.Context, tenant, actor, id string) (string, []byte, error) {
	if err := o.require(ctx, tenant, actor, "Downloading stock SIP basket files", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return "", nil, err
	}
	b, _, err := docs.Get[SIPBatch](ctx, o.Docs, tenant, cSIPBatch, id)
	if err != nil {
		return "", nil, err
	}
	if b.FileName == "" {
		return "", nil, invalid("batch %s went by order API; there is no file", id)
	}
	o.log(ctx, tenant, actor, "stocksip.basket.downloaded", id, map[string]any{"file": b.FileName})
	return b.FileName, b.Content, nil
}

// WithdrawStockSIPBasket takes back a basket file the dealer could not
// upload: its orders without trades wait again.
func (o *Ops) WithdrawStockSIPBasket(ctx context.Context, tenant, actor, id, reason string) (SIPBatch, error) {
	if err := o.require(ctx, tenant, actor, "Withdrawing a stock SIP basket file", RoleMaker, RoleChecker, RoleRisk); err != nil {
		return SIPBatch{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return SIPBatch{}, invalid("say why the file was not uploaded")
	}
	b, _, err := docs.Get[SIPBatch](ctx, o.Docs, tenant, cSIPBatch, id)
	if err != nil {
		return b, err
	}
	if b.FileName == "" || b.Status != "Sent" {
		return b, fmt.Errorf("%w: only a basket file that was sent can be withdrawn", ErrState)
	}
	if b.Date != o.now().Format("2006-01-02") {
		return b, fmt.Errorf("%w: the basket was for %s", ErrState, b.Date)
	}
	n := 0
	for _, oid := range b.Orders {
		x, _, err := docs.Get[SIPOrder](ctx, o.Docs, tenant, cSIPOrder, oid)
		if err != nil || x.Status != SIPOrdPlaced || x.Batch != b.ID {
			continue
		}
		x.Status, x.Batch, x.Router, x.PlacedAt, x.Note, x.UpdatedAt = SIPOrdPending, "", "", "", "basket "+b.ID+" withdrawn: "+reason, o.now().Format(time.RFC3339)
		_ = docs.Upsert(ctx, o.Docs, tenant, cSIPOrder, x.ID, x, actor)
		n++
	}
	b.Status = "Withdrawn"
	if err := docs.Upsert(ctx, o.Docs, tenant, cSIPBatch, b.ID, b, actor); err != nil {
		return b, err
	}
	o.log(ctx, tenant, actor, "stocksip.basket.withdrawn", id, map[string]any{"orders": n, "reason": reason})
	return b, nil
}
