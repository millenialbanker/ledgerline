package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// ---- NSE eIPO: G-sec, T-bill and SDL non-competitive bids, sovereign gold
// bonds and tender offers (Protocol for WEB API, Initial Public Offering
// System 2.0.0, October 2026) ----
//
// The eIPO host that takes IPO bids (ipoeipo.go) also takes three other
// products, each with its own master and order calls on the same login:
//
//   - NCB (non-competitive bids in G-secs, T-bills and SDLs): GET ncbmaster,
//     POST ncb/add (N new, M modify, D delete), GET ncb/{time}, POST
//     ncb/fetch (by application number) and GET ncb/mismatches/{symbol}.
//     The client's money passes through the member: clearingStatus FP is
//     "fund pending" until the member pays NSCCL (FS), then AL allotted or
//     RJ rejected. Ledgerline blocks the money on the client's ledger when
//     the order is taken, as for an offer for sale (ipoofs.go).
//   - SGB (sovereign gold bonds): GET sgbmaster, POST sgb/add (an array;
//     ER, MR, CR), GET sgb/{time}, POST sgb/fetch (by order number), GET
//     sgb/mismatches/{symbol}, GET sgb/incomplete/{symbol} and POST
//     sgb/incomplete/update (the investor details RBI needs: date of
//     birth, state, bank account, joint holders, guardian). Money as for
//     NCB (FP, FS, FR funds received, RS sent to RBI, RA allotted by RBI).
//   - Tender offers (buy-back, open offer, delisting): GET tendermaster,
//     POST tender/add (ER, MR, CR), GET tender/{time} and POST tender/fetch.
//     The client sells: no money is blocked; demat shares are blocked or
//     moved by the exchange's process (early pay-in), physical shares go to
//     the registrar with the certificates.
//
// None of the three has a callback in the protocol (the callbacks are for
// IPO DP and UPI status), so Ledgerline asks for status by fetch. The calls
// go through nseEIPOHTTP.call, the eIPO login and transport.
//
// Where the protocol's samples and tables disagree Ledgerline takes the
// table and reads replies loosely: keys are matched without case, spaces or
// slashes ("orderStatus " and "physical/DematFlag" in the samples), lists are
// found under any of the names the samples use, and numbers sent as strings
// ("100 ") are read as numbers.

const (
	cEIPOOrder = "eipoorder" // NCB, SGB and tender orders (EIPOOrder)

	// PayableEIPOFunds holds client money taken from the ledger for NCB and
	// SGB applications until it goes to the clearing corporation.
	PayableEIPOFunds = "PAYABLE:EIPO_FUNDS"
)

// Order statuses.
const (
	OrderDraft       = "Draft"   // taken, waiting to go to NSE
	OrderPlaced      = "Placed"  // NSE holds it (entry or modification success)
	OrderRefused     = "Refused" // NSE refused the entry: fix and send again, or cancel
	OrderCancelled   = "Cancelled"
	OrderRejected    = "Rejected" // rejected in clearing or verification, or not placed before the close
	OrderAllotted    = "Allotted" // NCB AL, SGB RA
	OrderAccepted    = "Accepted" // tender: all shares accepted
	OrderPartly      = "Partly accepted"
	OrderNotAccepted = "Not accepted"
)

// OrderStatuses lists the order statuses in lifecycle order.
var OrderStatuses = []string{OrderDraft, OrderPlaced, OrderRefused, OrderCancelled, OrderRejected, OrderAllotted, OrderAccepted, OrderPartly, OrderNotAccepted}

// TenderKinds are the kinds of tender offer.
var TenderKinds = []string{"Buy-back", "Open offer", "Delisting"}

// IPOProduct is the rest of an NCB, SGB or tender offer's master record.
type IPOProduct struct {
	Series    string    `json:"series,omitempty"`
	Kind      string    `json:"kind,omitempty"` // tender: Buy-back, Open offer, Delisting
	ExchType  string    `json:"exch_type,omitempty"`
	FaceValue money.Dec `json:"face_value,omitempty"`
	MinQty    int64     `json:"min_qty,omitempty"` // NCB: rupees of face value; SGB: grams; tender: shares
	MaxQty    int64     `json:"max_qty,omitempty"`
	// CutoffPrice is the master's cut-off price as given (a price for a
	// tender offer; the NCB samples carry a security code there).
	CutoffPrice string `json:"cutoff_price,omitempty"`
	// T1 modification window (NCB, SGB): mismatched orders may be corrected.
	T1From string `json:"t1_from,omitempty"` // yyyy-mm-dd hh:mm
	T1To   string `json:"t1_to,omitempty"`
	// IncompleteTo is the last day (SGB) investor details may be completed.
	IncompleteTo string `json:"incomplete_to,omitempty"`
	AllotDate    string `json:"allot_date,omitempty"`
	RecordDate   string `json:"record_date,omitempty"` // tender: from the letter of offer
	// FundsBuffer (NCB) is the percentage above the face value blocked on
	// the ledger: the price is set in the auction and a dated security can
	// cost more than its face value.
	FundsBuffer money.Dec `json:"funds_buffer_pct,omitempty"`
	IssueSize   int64     `json:"issue_size,omitempty"`
}

// productCheck checks an NCB, SGB or tender issue.
func (iss *IPOIssue) productCheck() error {
	if iss.Product == nil {
		iss.Product = &IPOProduct{}
	}
	p := iss.Product
	if len(iss.Exchanges) == 0 {
		iss.Exchanges = []string{"NSE"}
	}
	if len(iss.Exchanges) != 1 || iss.Exchanges[0] != "NSE" {
		return invalid("%s orders go through NSE's eIPO: the exchange is NSE", iss.Type)
	}
	if iss.LotSize <= 0 {
		iss.LotSize = 1
	}
	p.Series = strings.ToUpper(strings.TrimSpace(p.Series))
	p.Kind = strings.TrimSpace(p.Kind)
	switch {
	case p.Kind != "" && (iss.Type != IssueTender || !contains(TenderKinds, p.Kind)):
		return invalid("the kind of tender offer is %s", strings.Join(TenderKinds, ", "))
	case p.MinQty < 0 || p.MaxQty < 0 || p.MaxQty > 0 && p.MaxQty < p.MinQty:
		return invalid("the maximum quantity must be at least the minimum")
	case iss.PriceLow.Sign() < 0 || iss.PriceHigh.Sign() < 0 || p.FaceValue.Sign() < 0:
		return invalid("prices cannot be negative")
	case p.FundsBuffer.Sign() < 0 || p.FundsBuffer.Cmp(money.DecInt(25)) > 0:
		return invalid("the funds buffer is 0 to 25%% above the face value")
	case p.RecordDate != "" && !validDate(p.RecordDate), p.AllotDate != "" && !validDate(p.AllotDate), p.IncompleteTo != "" && !validDate(p.IncompleteTo):
		return invalid("dates are yyyy-mm-dd")
	}
	switch iss.Type {
	case IssueSGB:
		// The price may not be announced yet: orders wait for it. The
		// online price (₹50 a gram less, for online applications paid
		// digitally) is the low one, the offline the high one.
		if iss.PriceLow.Sign() > 0 && iss.PriceHigh.Sign() > 0 && iss.PriceHigh.Cmp(iss.PriceLow) < 0 {
			return invalid("the offline price cannot be below the online price")
		}
	case IssueTender:
		if iss.PriceLow.Sign() <= 0 && iss.PriceHigh.Sign() <= 0 {
			return invalid("give the offer price (the floor price for a delisting)")
		}
		if iss.PriceLow.Sign() > 0 && iss.PriceHigh.Sign() > 0 && iss.PriceHigh.Cmp(iss.PriceLow) < 0 {
			return invalid("the maximum price cannot be below the minimum")
		}
	}
	if iss.Categories == nil {
		iss.Categories = []IPOCategory{}
	}
	return nil
}

// tenderPrice is the price a tender is made at when the client names none:
// the master's cut-off price, else the top of the range, else the floor.
func (iss IPOIssue) tenderPrice() money.Dec {
	if iss.Product != nil {
		if d, err := money.ParseDec(strings.TrimSpace(iss.Product.CutoffPrice)); err == nil && d.Sign() > 0 {
			return d
		}
	}
	return firstDec(iss.PriceHigh, iss.PriceLow)
}

// TenderCert is one share certificate tendered (physical shares).
type TenderCert struct {
	No   string `json:"no"`
	From string `json:"from"` // distinctive numbers
	To   string `json:"to"`
}

// EIPOOrder is a client's NCB bid, SGB application or tender.
type EIPOOrder struct {
	ID       string `json:"id"`
	Product  string `json:"product"` // NCB, SGB, Tender
	IssueID  string `json:"issue_id"`
	Symbol   string `json:"symbol"`
	Issue    string `json:"issue"`
	UCC      string `json:"ucc"`
	Name     string `json:"name,omitempty"`
	PAN      string `json:"pan"`
	Category string `json:"category,omitempty"` // tender
	// Qty is rupees of face value (NCB), grams (SGB) or shares (tender).
	Qty   int64     `json:"qty"`
	Price money.Dec `json:"price,omitempty"` // SGB issue price, tender price
	// Amount is the money blocked (NCB, SGB) or the tender's value.
	Amount     money.Amount `json:"amount"`
	PriceKind  string       `json:"price_kind,omitempty"` // SGB: Online or Offline
	Physical   bool         `json:"physical,omitempty"`   // SGB certificate; tender physical shares
	Depository string       `json:"depository,omitempty"`
	DPID       string       `json:"dp_id,omitempty"`
	BenID      string       `json:"ben_id,omitempty"`
	Demat      string       `json:"demat,omitempty"` // the BO ID the order names
	Folio      string       `json:"folio,omitempty"`
	Certs      []TenderCert `json:"certificates,omitempty"`
	Status     string       `json:"status"`
	// Pending is the action waiting to go to NSE (New, Modify, Cancel).
	Pending string `json:"pending,omitempty"`
	SentAt  string `json:"sent_at,omitempty"`
	AppNo   string `json:"app_no,omitempty"`
	OrderNo string `json:"order_no,omitempty"`
	// What NSE holds and says.
	ExchQty      int64        `json:"exch_qty,omitempty"`
	ExchPrice    money.Dec    `json:"exch_price,omitempty"`
	PlacedAmount money.Amount `json:"placed_amount,omitempty"` // the amount when NSE last took the order
	OrderStatus  string       `json:"order_status,omitempty"`  // ES, EF, MS, MF, CS, CF
	Reason       string       `json:"reason,omitempty"`
	VerStatus    string       `json:"verification,omitempty"` // Pending, Verified, Failed
	VerReason    string       `json:"verification_reason,omitempty"`
	Clearing     string       `json:"clearing,omitempty"` // FP, FS, RJ, AL, FR, RS, RA
	ClearReason  string       `json:"clearing_reason,omitempty"`
	ExchAmount   money.Amount `json:"exch_amount,omitempty"` // amount payable (NCB) or tender amount NSE reports
	RTAStatus    string       `json:"rta_status,omitempty"`
	RTAReason    string       `json:"rta_reason,omitempty"`
	CustStatus   string       `json:"custodian_status,omitempty"`
	LastAction   string       `json:"last_action,omitempty"`
	Mismatch     string       `json:"mismatch,omitempty"`
	Incomplete   []int        `json:"incomplete,omitempty"` // SGB incomplete codes
	KYCSent      string       `json:"kyc_sent,omitempty"`
	// Money held on the client's ledger (NCB, SGB).
	Funds      money.Amount `json:"funds,omitempty"`
	FundsEntry string       `json:"funds_entry,omitempty"`
	FundsRev   int          `json:"funds_rev,omitempty"`
	ReleasedOn string       `json:"released_on,omitempty"`
	// Tender acceptance.
	AcceptedQty   int64        `json:"accepted_qty,omitempty"`
	AcceptedPrice money.Dec    `json:"accepted_price,omitempty"`
	AcceptedOn    string       `json:"accepted_on,omitempty"`
	Proceeds      money.Amount `json:"proceeds,omitempty"`
	ProceedsEntry string       `json:"proceeds_entry,omitempty"`
	Instruction   string       `json:"instruction,omitempty"`
	Ack           string       `json:"ack,omitempty"`
	By            string       `json:"by,omitempty"`
	Created       string       `json:"created,omitempty"`
	Updated       string       `json:"updated,omitempty"`
	History       []IPOEvent   `json:"history,omitempty"`
}

func (x *EIPOOrder) event(at time.Time, by, what string, args ...any) {
	x.History = append(x.History, IPOEvent{At: at.Format(time.RFC3339), By: by, What: fmt.Sprintf(what, args...)})
	if len(x.History) > 60 {
		x.History = x.History[len(x.History)-60:]
	}
	x.Updated = at.Format(time.RFC3339)
}

// live reports whether the order still counts (for one order per PAN).
func (x EIPOOrder) live() bool {
	return x.Status != OrderCancelled && x.Status != OrderRejected && x.Status != OrderNotAccepted
}

// open reports whether the order can still be changed or cancelled.
func (x EIPOOrder) open() bool {
	return (x.Status == OrderDraft || x.Status == OrderPlaced || x.Status == OrderRefused) && x.Pending != "Cancel"
}

// final reports whether NSE has nothing more to say about the order.
func (x EIPOOrder) final() bool {
	switch x.Status {
	case OrderCancelled, OrderRejected, OrderAccepted, OrderPartly, OrderNotAccepted:
		return true
	case OrderAllotted:
		return x.Product != IssueSGB || x.Clearing == "RA"
	}
	return false
}

// ---- the API ----

// EIPOProductMaster is one record of the NCB, SGB or tender master.
type EIPOProductMaster struct {
	Product, Symbol, Series, Name, IssueType, ISIN, Registrar  string
	LotSize, MinBidQuantity, MaxQuantity, IssueSize            int64
	FaceValue, MinPrice, MaxPrice, TickSize                    money.Dec
	OnlinePrice, OfflinePrice                                  money.Dec // SGB, when the master gives them by name
	CutoffPrice                                                string
	BiddingStartDate, BiddingEndDate                           string // dd-MM-yyyy
	DailyStartTime, DailyEndTime, LastDayBiddingEndTime        string
	T1ModStartDate, T1ModEndDate, T1ModStartTime, T1ModEndTime string
	AllotmentDate, IncompleteModEndDate                        string
}

// EIPOProductOrder is an order as Ledgerline sends it, whatever the
// product; eipoOrderBody gives each product's JSON.
type EIPOProductOrder struct {
	Product, Symbol, Series, Activity string // Activity: New, Modify, Cancel
	AppNo, OrderNo, Ref               string
	Qty                               int64
	Price                             money.Dec
	Physical                          bool
	PAN, Depository, DPID, BenID      string
	ClientCode, ClientName, Category  string
	Folio                             string
	Certs                             []TenderCert
}

// EIPOOrderUpdate is what NSE says about an order.
type EIPOOrderUpdate struct {
	Ref, AppNo, OrderNo, Symbol      string
	Status, OrderStatus, Reason      string // status: success / failed
	VerStatus, VerReason             string
	Clearing, ClearReason            string
	Amount                           string // totalAmountPayable (NCB) or amount (tender), as given
	Qty                              int64
	Price                            string
	RTAStatus, RTAReason, CustStatus string
	At                               string
	Mismatch                         string // a mismatch's reason
	Source                           string
}

// SGBIncomplete is an SGB application NSE lists as incomplete.
type SGBIncomplete struct {
	Symbol  string `json:"symbol"`
	OrderNo string `json:"order_no"`
	Codes   []int  `json:"codes"`
}

// SGBKYC is the investor detail sent to complete an SGB application
// (sgb/incomplete/update); dates are yyyy-mm-dd here, dd-MM-yyyy to NSE.
type SGBKYC struct {
	Symbol       string `json:"symbol,omitempty"`
	OrderNo      string `json:"order_no,omitempty"`
	DOB          string `json:"dob,omitempty"`
	State        string `json:"state,omitempty"`
	BankIFSC     string `json:"bank_ifsc,omitempty"`
	BankAccount  string `json:"bank_account,omitempty"`
	SecondPAN    string `json:"second_pan,omitempty"`
	SecondDOB    string `json:"second_dob,omitempty"`
	ThirdPAN     string `json:"third_pan,omitempty"`
	ThirdDOB     string `json:"third_dob,omitempty"`
	GuardianName string `json:"guardian_name,omitempty"`
	GuardianRel  string `json:"guardian_relation,omitempty"`
	GuardianDOB  string `json:"guardian_dob,omitempty"`
	GuardianPAN  string `json:"guardian_pan,omitempty"`
	Codes        []int  `json:"codes,omitempty"` // in a view: what NSE says is missing
}

// EIPOProductClient talks to NSE's eIPO for NCB, SGB and tender offers;
// replaced in tests.
type EIPOProductClient interface {
	ProductMaster(ctx context.Context, c EIPOConfig, product string) ([]EIPOProductMaster, error)
	ProductSend(ctx context.Context, c EIPOConfig, product string, orders []EIPOProductOrder) ([]EIPOOrderUpdate, error)
	ProductFetch(ctx context.Context, c EIPOConfig, product, symbol string, keys []string) ([]EIPOOrderUpdate, error)
	ProductMismatches(ctx context.Context, c EIPOConfig, product, symbol string) ([]EIPOOrderUpdate, error)
	SGBIncomplete(ctx context.Context, c EIPOConfig, symbol string) ([]SGBIncomplete, error)
	SGBCompleteDetails(ctx context.Context, c EIPOConfig, k SGBKYC) error
}

var eipoProducts EIPOProductClient = nseEIPOHTTP{client: &http.Client{Timeout: 60 * time.Second}}

// eipoPaths are each product's calls under the API version.
var eipoPaths = map[string]map[string]string{
	IssueNCB:    {"master": "/ncbmaster", "add": "/ncb/add", "fetch": "/ncb/fetch", "mismatches": "/ncb/mismatches/"},
	IssueSGB:    {"master": "/sgbmaster", "add": "/sgb/add", "fetch": "/sgb/fetch", "mismatches": "/sgb/mismatches/"},
	IssueTender: {"master": "/tendermaster", "add": "/tender/add", "fetch": "/tender/fetch"},
}

// eipoRec is one object of a reply, its keys lower case without spaces or
// slashes.
type eipoRec map[string]any

func eipoKey(k string) string {
	return strings.NewReplacer(" ", "", "/", "", "_", "").Replace(strings.ToLower(strings.TrimSpace(k)))
}

func (r eipoRec) s(names ...string) string {
	for _, n := range names {
		if v, ok := r[eipoKey(n)]; ok && v != nil {
			switch t := v.(type) {
			case map[string]any, []any:
				continue
			default:
				if s := strings.TrimSpace(fmt.Sprint(t)); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func (r eipoRec) i(names ...string) int64 {
	n, _ := parseInt(strings.TrimSpace(r.s(names...)))
	return n
}

func (r eipoRec) d(names ...string) money.Dec {
	d, _ := money.ParseDec(strings.TrimSpace(r.s(names...)))
	return d
}

func (r eipoRec) ints(name string) []int {
	var out []int
	if l, ok := r[eipoKey(name)].([]any); ok {
		for _, v := range l {
			if n, err := parseInt(fmt.Sprint(v)); err == nil {
				out = append(out, int(n))
			}
		}
	}
	return out
}

// eipoRecords reads a reply: the objects under its list (transactions,
// mismatches, notifications, incomplete, data), the objects of a bare list
// (nested lists flattened), or the reply itself; with the reply's own status
// and reason.
func eipoRecords(b []byte) (recs []eipoRec, status, reason string, err error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber() // order numbers are 16 digits
	if err := dec.Decode(&v); err != nil {
		return nil, "", "", fmt.Errorf("NSE's eIPO reply is not JSON: %s", trimBody(b))
	}
	norm := func(m map[string]any) eipoRec {
		r := eipoRec{}
		for k, x := range m {
			r[eipoKey(k)] = x
		}
		return r
	}
	var flat func(any)
	flat = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				flat(e)
			}
		case map[string]any:
			recs = append(recs, norm(t))
		}
	}
	switch t := v.(type) {
	case []any:
		flat(t)
		if len(recs) > 0 {
			status, reason = recs[0].s("status"), recs[0].s("reason")
		}
	case map[string]any:
		top := norm(t)
		status, reason = top.s("status"), top.s("reason")
		listed := false
		for _, k := range []string{"transactions", "mismatches", "notifications", "incomplete", "data", "eforms", "demand"} {
			if l, ok := top[k].([]any); ok {
				flat(l)
				listed = true
			}
		}
		if !listed {
			recs = append(recs, top)
		}
	}
	return recs, status, reason, nil
}

func orderUpdateOf(r eipoRec, source string) EIPOOrderUpdate {
	return EIPOOrderUpdate{Ref: r.s("clientRefNumber", "reference", "referenceNumber"), AppNo: r.s("applicationNumber"), OrderNo: r.s("orderNumber"), Symbol: r.s("symbol"),
		Status: r.s("status"), OrderStatus: strings.ToUpper(r.s("orderStatus")), Reason: firstOf(r.s("rejectionReason"), r.s("reason")),
		VerStatus: strings.ToUpper(r.s("verificationStatus")), VerReason: r.s("verificationReason"), Clearing: strings.ToUpper(r.s("clearingStatus")), ClearReason: r.s("clearingReason"),
		Amount: r.s("totalAmountPayable", "amount"), Qty: r.i("investmentValue", "quantity", "bidQuantity"), Price: r.s("price"),
		RTAStatus: r.s("rtaStatus"), RTAReason: r.s("rtaRejectionReason"), CustStatus: r.s("custodianStatus"), At: r.s("lastActionTime", "entryTime"), Source: source}
}

func (h nseEIPOHTTP) ProductMaster(ctx context.Context, c EIPOConfig, product string) ([]EIPOProductMaster, error) {
	p, ok := eipoPaths[product]
	if !ok {
		return nil, invalid("no %s master", product)
	}
	reply, err := h.call(ctx, c, http.MethodGet, c.path(p["master"]), nil)
	if err != nil {
		return nil, err
	}
	recs, status, reason, err := eipoRecords(reply)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(status, "success") && len(recs) <= 1 {
		if strings.Contains(strings.ToLower(reason), "no record") {
			return nil, nil
		}
		return nil, fmt.Errorf("NSE's %s master: %s", product, firstOf(reason, trimBody(reply)))
	}
	var out []EIPOProductMaster
	for _, r := range recs {
		if r.s("symbol") == "" {
			continue
		}
		out = append(out, EIPOProductMaster{Product: product, Symbol: strings.ToUpper(r.s("symbol")), Series: r.s("series"), Name: r.s("name"), IssueType: r.s("issueType"), ISIN: strings.ToUpper(r.s("isin")), Registrar: r.s("registrar"),
			LotSize: r.i("lotSize"), MinBidQuantity: r.i("minBidQuantity"), MaxQuantity: r.i("maxQuantity"), IssueSize: r.i("issueSize"),
			FaceValue: r.d("faceValue"), MinPrice: r.d("minPrice"), MaxPrice: r.d("maxPrice"), TickSize: r.d("tickSize"), OnlinePrice: r.d("onlinePrice"), OfflinePrice: r.d("offlinePrice"),
			CutoffPrice: r.s("cutoffPrice"), BiddingStartDate: r.s("biddingStartDate"), BiddingEndDate: r.s("biddingEndDate"), DailyStartTime: r.s("dailyStartTime"), DailyEndTime: r.s("dailyEndTime"),
			LastDayBiddingEndTime: r.s("lastDayBiddingEndTime"), T1ModStartDate: r.s("t1ModStartDate"), T1ModEndDate: r.s("t1ModEndDate"), T1ModStartTime: r.s("t1ModStartTime"), T1ModEndTime: r.s("t1ModEndTime"),
			AllotmentDate: r.s("allotmentDate"), IncompleteModEndDate: r.s("incompleteModEndDate")})
	}
	return out, nil
}

// eipoNum is a value for a protocol Number field: a JSON number when it is
// digits, else the text (which NSE then refuses with a reason).
func eipoNum(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.Trim(s, "0123456789") == "" {
		return json.Number(s)
	}
	return s
}

// eipoOrderBody is an order as each product's add call takes it.
func eipoOrderBody(o EIPOProductOrder) any {
	b := map[string]any{"symbol": o.Symbol, "pan": o.PAN}
	put := func(k string, v any) {
		switch t := v.(type) {
		case string:
			if t != "" {
				b[k] = t
			}
		case nil:
		default:
			b[k] = v
		}
	}
	price := func() any {
		if o.Price.Sign() <= 0 {
			return nil
		}
		return json.Number(o.Price.Fixed(2))
	}
	flag := map[bool]string{true: "P", false: "D"}[o.Physical]
	demat := func() {
		if o.Physical {
			return
		}
		put("depository", o.Depository)
		if strings.EqualFold(o.Depository, "NSDL") {
			put("dpId", o.DPID) // ignored for CDSL
		}
		put("clientBenId", eipoNum(o.BenID))
	}
	switch o.Product {
	case IssueNCB:
		// ncb/add: investmentValue in rupees of face value; no price (the
		// auction sets it for non-competitive bids).
		b["investmentValue"] = o.Qty
		b["physicalDematFlag"] = "D"
		b["activityType"] = map[string]string{"New": "N", "Modify": "M", "Cancel": "D"}[o.Activity]
		put("applicationNumber", o.AppNo)
		put("clientRefNumber", o.Ref)
		demat()
		if o.Activity != "New" {
			put("orderNumber", eipoNum(o.OrderNo))
		}
	case IssueSGB:
		b["quantity"] = o.Qty
		put("price", price())
		b["physicalDematFlag"] = flag
		b["activityType"] = map[string]string{"New": "ER", "Modify": "MR", "Cancel": "CR"}[o.Activity]
		put("clientRefNumber", o.Ref)
		if o.Physical {
			put("clientCode", o.ClientCode)
		}
		demat()
		if o.Activity != "New" {
			put("orderNumber", eipoNum(o.OrderNo))
		}
		return []any{b} // sgb/add takes a list
	case IssueTender:
		put("series", o.Series)
		put("applicationNumber", o.AppNo)
		put("category", o.Category)
		b["bidQuantity"] = o.Qty
		put("price", price())
		put("clientName", o.ClientName)
		put("clientCode", o.ClientCode)
		b["physicalDematFlag"] = flag
		b["activityType"] = map[string]string{"New": "ER", "Modify": "MR", "Cancel": "CR"}[o.Activity]
		put("reference", o.Ref) // the table's name; the samples say referenceNumber
		demat()
		if o.Physical {
			put("folioNo", o.Folio)
			for i, c := range o.Certs {
				if i >= 3 {
					break
				}
				n := fmt.Sprint(i + 1)
				put("certificate"+n, c.No)
				put("distinctiveFrom"+n, c.From)
				put("distinctiveTo"+n, c.To)
			}
		}
		if o.Activity != "New" {
			put("orderNumber", eipoNum(o.OrderNo))
		}
	}
	return b
}

func (h nseEIPOHTTP) ProductSend(ctx context.Context, c EIPOConfig, product string, orders []EIPOProductOrder) ([]EIPOOrderUpdate, error) {
	p, ok := eipoPaths[product]
	if !ok {
		return nil, invalid("no %s orders", product)
	}
	var out []EIPOOrderUpdate
	for _, o := range orders {
		reply, err := h.call(ctx, c, http.MethodPost, c.path(p["add"]), eipoOrderBody(o))
		if err != nil {
			return out, err
		}
		recs, status, reason, err := eipoRecords(reply)
		if err != nil {
			return out, err
		}
		u := EIPOOrderUpdate{Status: status, Reason: reason}
		if len(recs) > 0 {
			u = orderUpdateOf(recs[0], "eIPO API")
			u.Status, u.Reason = firstOf(u.Status, status), firstOf(u.Reason, reason)
		}
		u.Ref, u.AppNo, u.Source = firstOf(u.Ref, o.Ref), firstOf(u.AppNo, o.AppNo), "eIPO API"
		if u.OrderNo == "" {
			u.OrderNo = o.OrderNo
		}
		out = append(out, u)
	}
	return out, nil
}

func (h nseEIPOHTTP) ProductFetch(ctx context.Context, c EIPOConfig, product, symbol string, keys []string) ([]EIPOOrderUpdate, error) {
	p, ok := eipoPaths[product]
	if !ok {
		return nil, invalid("no %s orders", product)
	}
	field := "applicationNumber"
	if product == IssueSGB {
		field = "orderNumber" // sgb/fetch is by order number
	}
	var out []EIPOOrderUpdate
	for _, k := range keys {
		reply, err := h.call(ctx, c, http.MethodPost, c.path(p["fetch"]), map[string]string{"symbol": symbol, field: k})
		if err != nil {
			return out, err
		}
		recs, _, _, err := eipoRecords(reply)
		if err != nil {
			continue
		}
		for _, r := range recs {
			if r.s("symbol") == "" && r.s("applicationNumber") == "" && r.s("orderNumber") == "" {
				continue // "No Records Found"
			}
			u := orderUpdateOf(r, "eIPO inquiry")
			if product == IssueSGB && u.OrderNo == "" {
				u.OrderNo = k
			}
			out = append(out, u)
		}
	}
	return out, nil
}

func (h nseEIPOHTTP) ProductMismatches(ctx context.Context, c EIPOConfig, product, symbol string) ([]EIPOOrderUpdate, error) {
	p, ok := eipoPaths[product]
	if !ok || p["mismatches"] == "" {
		return nil, nil
	}
	// The summary lists POST; the reference section a GET with the symbol
	// in the path, which Ledgerline follows.
	reply, err := h.call(ctx, c, http.MethodGet, c.path(p["mismatches"]+url.PathEscape(symbol)), nil)
	if err != nil {
		return nil, err
	}
	recs, _, _, err := eipoRecords(reply)
	if err != nil {
		return nil, err
	}
	var out []EIPOOrderUpdate
	for _, r := range recs {
		if r.s("applicationNumber") == "" && r.s("orderNumber") == "" {
			continue
		}
		out = append(out, EIPOOrderUpdate{AppNo: r.s("applicationNumber"), OrderNo: r.s("orderNumber"), Symbol: r.s("symbol"), Mismatch: firstOf(r.s("reason"), "mismatch"), Source: "eIPO mismatches"})
	}
	return out, nil
}

func (h nseEIPOHTTP) SGBIncomplete(ctx context.Context, c EIPOConfig, symbol string) ([]SGBIncomplete, error) {
	reply, err := h.call(ctx, c, http.MethodGet, c.path("/sgb/incomplete/"+url.PathEscape(symbol)), nil)
	if err != nil {
		return nil, err
	}
	recs, _, _, err := eipoRecords(reply)
	if err != nil {
		return nil, err
	}
	var out []SGBIncomplete
	for _, r := range recs {
		if r.s("orderNumber") == "" {
			continue
		}
		out = append(out, SGBIncomplete{Symbol: r.s("symbol"), OrderNo: r.s("orderNumber"), Codes: r.ints("incompleteCodesList")})
	}
	return out, nil
}

// eipoDDMM is yyyy-mm-dd as NSE's dd-MM-yyyy.
func eipoDDMM(d string) string {
	if t, err := time.Parse("2006-01-02", strings.TrimSpace(d)); err == nil {
		return t.Format("02-01-2006")
	}
	return ""
}

func (h nseEIPOHTTP) SGBCompleteDetails(ctx context.Context, c EIPOConfig, k SGBKYC) error {
	b := map[string]any{"symbol": k.Symbol, "orderNumber": eipoNum(k.OrderNo)}
	for key, v := range map[string]string{"firstHolderDOB": eipoDDMM(k.DOB), "firstHolderState": k.State, "firstHolderBankIfsc": k.BankIFSC, "firstHolderBankAccNo": k.BankAccount,
		"secondHolderPAN": k.SecondPAN, "secondHolderDOB": eipoDDMM(k.SecondDOB), "thirdHolderPAN": k.ThirdPAN, "thirdHolderDOB": eipoDDMM(k.ThirdDOB),
		"guardianFirstName": k.GuardianName, "guardianRelation": k.GuardianRel, "guardianDOB": eipoDDMM(k.GuardianDOB), "guardianPAN": k.GuardianPAN} {
		if v != "" {
			b[key] = v
		}
	}
	reply, err := h.call(ctx, c, http.MethodPost, c.path("/sgb/incomplete/update"), []any{b})
	if err != nil {
		return err
	}
	_, status, reason, err := eipoRecords(reply)
	if err != nil {
		return err
	}
	if !strings.EqualFold(status, "success") {
		return fmt.Errorf("NSE's eIPO did not take the details: %s", firstOf(reason, trimBody(reply)))
	}
	return nil
}

// ---- settings and state ----

// EIPOProductSettings say which products the firm offers: their issues are
// set up from NSE's masters each morning and their orders sent by the tick
// (with the eIPO connection's sending on).
type EIPOProductSettings struct {
	NCB    bool   `json:"ncb"`
	SGB    bool   `json:"sgb"`
	Tender bool   `json:"tender"`
	By     string `json:"by,omitempty"`
	At     string `json:"at,omitempty"`
}

func (s EIPOProductSettings) on(product string) bool {
	return map[string]bool{IssueNCB: s.NCB, IssueSGB: s.SGB, IssueTender: s.Tender}[product]
}

// eipoProdState is what the products' tick has done.
type eipoProdState struct {
	LastMaster string            `json:"last_master,omitempty"`
	LastSend   map[string]string `json:"last_send,omitempty"` // by issue
	LastAsk    map[string]string `json:"last_inquiry,omitempty"`
	LastError  string            `json:"last_error,omitempty"`
	MasterNew  int               `json:"master_new"`
}

func (o *Ops) eipoProductSettings(ctx context.Context, tenant string) EIPOProductSettings {
	s, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIPOState, "products", EIPOProductSettings{})
	return s
}

// SetEIPOProductSettings turns the products on or off.
func (o *Ops) SetEIPOProductSettings(ctx context.Context, tenant, actor string, s EIPOProductSettings) (EIPOProductSettings, error) {
	if err := o.require(ctx, tenant, actor, "Choosing the eIPO products the firm offers", RoleChecker); err != nil {
		return s, err
	}
	s.By, s.At = actor, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cIPOState, "products", s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "ipo.products_set", "products", map[string]any{"ncb": s.NCB, "sgb": s.SGB, "tender": s.Tender})
	return s, nil
}

func (o *Ops) eipoProdState(ctx context.Context, tenant string, fn func(*eipoProdState)) eipoProdState {
	st, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIPOState, "products-state", eipoProdState{})
	if st.LastSend == nil {
		st.LastSend = map[string]string{}
	}
	if st.LastAsk == nil {
		st.LastAsk = map[string]string{}
	}
	if fn != nil {
		fn(&st)
		_ = docs.Upsert(ctx, o.Docs, tenant, cIPOState, "products-state", st, SystemActor)
	}
	return st
}

func (o *Ops) eipoUsable(ctx context.Context, tenant string) (eipoConn, error) {
	conn, ok := o.eipoConnection(ctx, tenant)
	if !ok {
		return conn, fmt.Errorf("%w: set up the NSE eIPO API in Settings → Connections", ErrState)
	}
	if !conn.usable {
		return conn, fmt.Errorf("%w: the NSE eIPO connection needs the member code, API user ID and password", ErrState)
	}
	return conn, nil
}

// ---- issues from the masters ----

// eipoTimes reads a master's dd-MM-yyyy and hh24:mm:ss into the issue.
func eipoProductIssue(m EIPOProductMaster, typ string) IPOIssue {
	iss := IPOIssue{Symbol: m.Symbol, Name: strings.TrimSpace(m.Name), ISIN: m.ISIN, Type: typ, Exchanges: []string{"NSE"}, Registrar: m.Registrar,
		Open: eipoDate(m.BiddingStartDate), Close: eipoDate(m.BiddingEndDate), OpenTime: hhmmOr(eipoHHMM(m.DailyStartTime), ""), DayClose: hhmmOr(eipoHHMM(m.DailyEndTime), ""),
		LotSize: max(m.LotSize, 1), Tick: m.TickSize, Categories: DefaultIPOCategories(typ)}
	iss.CloseTime = iss.DayClose
	if t := strings.Fields(m.LastDayBiddingEndTime); len(t) == 2 {
		iss.CloseTime = hhmmOr(eipoHHMM(t[1]), iss.CloseTime)
	}
	p := &IPOProduct{Series: strings.ToUpper(m.Series), ExchType: m.IssueType, FaceValue: m.FaceValue, MinQty: m.MinBidQuantity, MaxQty: m.MaxQuantity, CutoffPrice: m.CutoffPrice,
		AllotDate: eipoDate(m.AllotmentDate), IncompleteTo: eipoDate(m.IncompleteModEndDate), IssueSize: m.IssueSize}
	if d := eipoDate(m.T1ModStartDate); d != "" {
		p.T1From = strings.TrimSpace(d + " " + eipoHHMM(m.T1ModStartTime))
	}
	if d := eipoDate(m.T1ModEndDate); d != "" {
		p.T1To = strings.TrimSpace(d + " " + firstOf(eipoHHMM(m.T1ModEndTime), "23:59"))
	}
	iss.Product = p
	return iss
}

// eipoHHMM is hh24:mm:ss as HH:MM.
func eipoHHMM(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 5 && s[2] == ':' {
		return s[:5]
	}
	return ""
}

// productIssue makes an issue from a product master record, or false.
func productIssue(m EIPOProductMaster) (IPOIssue, bool) {
	var iss IPOIssue
	switch m.Product {
	case IssueNCB:
		iss = eipoProductIssue(m, IssueNCB)
		iss.PriceLow = m.FaceValue // what a unit of face value is; the auction sets the price
		iss.Name = firstOf(iss.Name, m.Symbol) + " (non-competitive bid)"
	case IssueSGB:
		iss = eipoProductIssue(m, IssueSGB)
		// minPrice is the online price and maxPrice the offline one; some
		// samples name them onlinePrice and offlinePrice.
		iss.PriceLow, iss.PriceHigh = firstDec(m.MinPrice, m.OnlinePrice), firstDec(m.MaxPrice, m.OfflinePrice)
		if iss.PriceLow.Sign() > 0 && iss.PriceHigh.Sign() > 0 && iss.PriceHigh.Cmp(iss.PriceLow) < 0 {
			iss.PriceLow, iss.PriceHigh = iss.PriceHigh, iss.PriceLow
		}
		if iss.Name == "" || strings.EqualFold(iss.Name, "Sovereign Gold Bond") {
			iss.Name = "Sovereign Gold Bond " + m.Symbol
		}
	case IssueTender:
		iss = eipoProductIssue(m, IssueTender)
		iss.PriceLow, iss.PriceHigh = m.MinPrice, m.MaxPrice
		if c, err := money.ParseDec(strings.TrimSpace(m.CutoffPrice)); err == nil && c.Sign() > 0 && iss.PriceHigh.Sign() == 0 {
			iss.PriceHigh = c
		}
		if strings.EqualFold(m.Series, "BB") {
			iss.Product.Kind = "Buy-back" // the protocol's example series for a buy-back
		}
		iss.Name = firstOf(iss.Name, m.Symbol) + " tender offer"
		if iss.Product.Kind != "" {
			iss.Name = strings.TrimSuffix(iss.Name, " tender offer") + " " + strings.ToLower(iss.Product.Kind)
		}
	default:
		return iss, false
	}
	return iss, iss.Open != "" && iss.Close != ""
}

// EIPOSyncProducts sets up the coming and open NCB, SGB and tender issues
// of NSE's masters that Ledgerline does not have (the products the firm
// offers; all three when staff ask), and keeps the dates and prices of
// those it set up in step with NSE. Issues set up by staff are left alone.
func (o *Ops) EIPOSyncProducts(ctx context.Context, tenant, actor string) (int, int, error) {
	if err := o.require(ctx, tenant, actor, "Setting up public issues", RoleMaker, RoleChecker); err != nil {
		return 0, 0, err
	}
	conn, err := o.eipoUsable(ctx, tenant)
	if err != nil {
		return 0, 0, err
	}
	set := o.eipoProductSettings(ctx, tenant)
	now := o.now()
	today := now.In(ist).Format("2006-01-02")
	have, _ := docs.List[IPOIssue](ctx, o.Docs, tenant, cIPOIssue, "")
	added, changed := 0, 0
	var problems []string
	for _, product := range []string{IssueNCB, IssueSGB, IssueTender} {
		if actor == SystemActor && !set.on(product) {
			continue
		}
		ms, err := eipoProducts.ProductMaster(ctx, conn.cfg, product)
		if err != nil {
			problems = append(problems, product+" master: "+err.Error())
			continue
		}
		for _, m := range ms {
			iss, ok := productIssue(m)
			if !ok || iss.Close < today {
				continue
			}
			var old *IPOIssue
			for i := range have {
				if strings.EqualFold(have[i].Symbol, iss.Symbol) && have[i].Type == iss.Type && have[i].Close >= today {
					old = &have[i]
				}
			}
			src := "NSE eIPO " + product + " master"
			switch {
			case old == nil:
				if _, err := o.saveIPOIssue(ctx, tenant, SystemActor, iss, src); err != nil {
					problems = append(problems, iss.Symbol+": "+err.Error())
					continue
				}
				added++
			case old.Source == src && productChanged(*old, iss):
				u := *old
				u.Open, u.Close, u.OpenTime, u.DayClose, u.CloseTime, u.PriceLow, u.PriceHigh, u.LotSize, u.Tick = iss.Open, iss.Close, iss.OpenTime, iss.DayClose, iss.CloseTime, iss.PriceLow, iss.PriceHigh, iss.LotSize, iss.Tick
				kind, rec, buf := "", "", money.Dec{}
				if u.Product != nil {
					kind, rec, buf = u.Product.Kind, u.Product.RecordDate, u.Product.FundsBuffer
				}
				np := *iss.Product
				np.Kind, np.RecordDate, np.FundsBuffer = firstOf(kind, np.Kind), rec, buf // staff's own
				u.Product = &np
				if _, err := o.saveIPOIssue(ctx, tenant, SystemActor, u, src); err != nil {
					problems = append(problems, iss.Symbol+": "+err.Error())
					continue
				}
				changed++
			}
		}
	}
	o.eipoProdState(ctx, tenant, func(s *eipoProdState) {
		s.LastMaster, s.MasterNew, s.LastError = now.Format(time.RFC3339), s.MasterNew+added, ""
		if len(problems) > 0 {
			s.LastError = problems[0]
		}
	})
	o.log(ctx, tenant, actor, "ipo.eipo_product_masters", today, map[string]any{"added": added, "changed": changed, "problems": len(problems)})
	if added+changed == 0 && len(problems) == 3 {
		return 0, 0, fmt.Errorf("NSE's masters could not be read: %s", problems[0])
	}
	return added, changed, nil
}

func productChanged(old, n IPOIssue) bool {
	op, np := old.Product, n.Product
	if op == nil || np == nil {
		return true
	}
	return old.Open != n.Open || old.Close != n.Close || old.PriceLow.Cmp(n.PriceLow) != 0 || old.PriceHigh.Cmp(n.PriceHigh) != 0 || old.LotSize != n.LotSize ||
		op.MinQty != np.MinQty || op.MaxQty != np.MaxQty || op.AllotDate != np.AllotDate || op.T1To != np.T1To || op.IncompleteTo != np.IncompleteTo || op.CutoffPrice != np.CutoffPrice
}

// ---- taking an order ----

// EIPOOrderInput is a client's NCB bid, SGB application or tender.
type EIPOOrderInput struct {
	Issue string `json:"issue"`
	UCC   string `json:"ucc"`
	// Qty: rupees of face value (NCB), grams (SGB) or shares (tender).
	Qty         int64            `json:"qty"`
	Price       money.Dec        `json:"price"`    // tender: the price (delisting); empty: the offer price
	Category    string           `json:"category"` // tender
	Physical    bool             `json:"physical"` // SGB as a certificate; tender of physical shares
	Online      bool             `json:"online"`   // SGB at the online price (applied online, paid digitally)
	Demat       string           `json:"demat"`    // the client's BO ID to use; empty: the primary (tender) or pay-out account
	Folio       string           `json:"folio"`
	Certs       []TenderCert     `json:"certificates"`
	Instruction IPOInstructionIn `json:"instruction"`
}

// eipoClientErr checks the client may place an order: active, a valid PAN,
// a trading account, not debarred.
func (o *Ops) eipoClientErr(ctx context.Context, tenant string, c Client, date string) error {
	switch {
	case c.Status != "Active":
		return invalid("%s is not an active client", c.UCC)
	case !rePAN.MatchString(c.PAN):
		return invalid("%s has no valid PAN on record", c.UCC)
	case c.DPOnly:
		return invalid("%s is a DP-only client with no trading account", c.UCC)
	}
	if d, hit := o.IsDebarredPAN(ctx, tenant, c.PAN, date); hit {
		return fmt.Errorf("%w: %s's PAN is debarred from the securities market (%s order %s)", ErrForbidden, c.UCC, d.Regulator, d.OrderNo)
	}
	return nil
}

// orderDemat picks the demat account: the one named (it must be the
// client's), else the pay-out account (credits) or primary (tenders).
func orderDemat(c Client, id string, credit bool) (Demat, error) {
	if strings.TrimSpace(id) != "" {
		d, ok := dematOf(c, id)
		if !ok {
			return d, invalid("%s is not a demat account of %s", id, c.UCC)
		}
		return d, nil
	}
	d := primaryDemat(c)
	if credit {
		d = payoutDemat(c)
	}
	if strings.TrimSpace(d.ID) == "" {
		return d, invalid("%s has no demat account on record", c.UCC)
	}
	return d, nil
}

// setDemat fills the order's demat fields, checking the account can take
// a credit when credit is set.
func (o *Ops) setDemat(ctx context.Context, tenant string, x *EIPOOrder, c Client, id string, credit bool) error {
	d, err := orderDemat(c, id, credit)
	if err != nil {
		return err
	}
	dep, dpid, ben, err := splitDemat(d)
	if err != nil {
		return err
	}
	if bo, ok := o.boAccount(ctx, tenant, d.ID); ok && bo.UCC == c.UCC {
		switch {
		case bo.Status == "Closed":
			return invalid("%s's demat account %s is closed", c.UCC, d.ID)
		case credit && bo.creditFrozen():
			return invalid("%s's demat account is frozen for credits (%s)", c.UCC, bo.freezeWhy())
		}
	}
	x.Depository, x.DPID, x.BenID, x.Demat = dep, dpid, ben, normDematID(d.ID)
	return nil
}

// orderCheck works out the order's price and amount by product.
func (o *Ops) orderCheck(ctx context.Context, tenant string, iss IPOIssue, c Client, x *EIPOOrder, in EIPOOrderInput) error {
	switch iss.Type {
	case IssueNCB:
		return o.ncbCheck(ctx, tenant, iss, c, x, in)
	case IssueSGB:
		return o.sgbCheck(ctx, tenant, iss, c, x, in)
	case IssueTender:
		return o.tenderCheck(ctx, tenant, iss, c, x, in)
	}
	return invalid("%s is not an NCB, SGB or tender offer", iss.Name)
}

// qtyCheck checks a quantity against the issue's lot, minimum and maximum.
func qtyCheck(iss IPOIssue, qty int64, unit string) error {
	p := iss.Product
	if p == nil {
		p = &IPOProduct{}
	}
	switch {
	case qty <= 0:
		return invalid("give the %s", unit)
	case p.MinQty > 0 && qty < p.MinQty:
		return invalid("the minimum is %d %s", p.MinQty, unit)
	case p.MaxQty > 0 && qty > p.MaxQty:
		return invalid("the maximum is %d %s", p.MaxQty, unit)
	case iss.LotSize > 1 && qty%iss.LotSize != 0:
		return invalid("%s go in multiples of %d", unit, iss.LotSize)
	}
	return nil
}

// orderWindow says why an order cannot be taken or changed now: the
// bidding window (and, for a change, the T1 modification window).
func (iss IPOIssue) orderWindow(now time.Time, change bool) error {
	if change && iss.inT1(now) {
		return nil
	}
	return iss.captureErr(Settings{}, IPOCategory{}, now)
}

// inT1 reports whether now is in the T1 modification window (NCB, SGB),
// when mismatched orders may be corrected.
func (iss IPOIssue) inT1(now time.Time) bool {
	p := productOr(iss)
	t := now.In(ist).Format("2006-01-02 15:04")
	return p.T1From != "" && p.T1To != "" && t >= p.T1From && t <= p.T1To
}

// windowOver reports whether bidding and any T1 window are over.
func (iss IPOIssue) windowOver(now time.Time) bool {
	p := productOr(iss)
	return iss.closed(now) && (p.T1To == "" || now.In(ist).Format("2006-01-02 15:04") > p.T1To)
}

// PlaceEIPOOrder takes a client's NCB bid, SGB application or tender on
// the client's prior instruction. NCB and SGB money is checked against
// the client's ledger and held there.
func (o *Ops) PlaceEIPOOrder(ctx context.Context, tenant, actor string, in EIPOOrderInput) (EIPOOrder, error) {
	if err := o.require(ctx, tenant, actor, "Placing NCB, SGB and tender orders for clients", RoleMaker, RoleChecker, RoleBranch, RoleAP); err != nil {
		return EIPOOrder{}, err
	}
	in.UCC = strings.ToUpper(strings.TrimSpace(in.UCC))
	if f := o.ClientScope(ctx, tenant, actor); f != nil && !f(in.UCC) {
		return EIPOOrder{}, ErrNotFound
	}
	now := o.now()
	today := now.Format("2006-01-02")
	iss, err := o.IPOIssueByID(ctx, tenant, strings.TrimSpace(in.Issue))
	if err != nil {
		return EIPOOrder{}, err
	}
	if !eipoProduct(iss.Type) {
		return EIPOOrder{}, invalid("%s is a public issue: take an IPO bid instead", iss.Name)
	}
	if err := iss.orderWindow(now, false); err != nil {
		return EIPOOrder{}, err
	}
	c, err := o.Client(ctx, tenant, in.UCC)
	if err != nil {
		return EIPOOrder{}, invalid("client %s not found", in.UCC)
	}
	if err := o.eipoClientErr(ctx, tenant, c, today); err != nil {
		return EIPOOrder{}, err
	}
	all, _ := docs.List[EIPOOrder](ctx, o.Docs, tenant, cEIPOOrder, "")
	for _, x := range all {
		if x.IssueID == iss.ID && x.live() && strings.EqualFold(x.PAN, c.PAN) {
			return EIPOOrder{}, invalid("PAN %s already has an order in %s (%s, %s): one per PAN; change or cancel that one", MaskPAN(c.PAN), iss.Name, x.ID, x.Status)
		}
	}
	x := EIPOOrder{Product: iss.Type, IssueID: iss.ID, Symbol: iss.Symbol, Issue: iss.Name, UCC: c.UCC, Name: c.Name, PAN: c.PAN, Qty: in.Qty, Status: OrderDraft, Pending: "New",
		By: actor, Created: now.Format(time.RFC3339)}
	if err := o.orderCheck(ctx, tenant, iss, c, &x, in); err != nil {
		return EIPOOrder{}, err
	}
	if x.Product != IssueTender {
		if err := o.eipoFundsOK(ctx, tenant, c.UCC, today, x.Amount, 0); err != nil {
			return EIPOOrder{}, err
		}
	}
	n, err := o.next(ctx, tenant, "eipoorder", actor)
	if err != nil {
		return EIPOOrder{}, err
	}
	x.ID = fmt.Sprintf("%s-%07d", map[string]string{IssueNCB: "NCB", IssueSGB: "SGB", IssueTender: "TND"}[x.Product], n)
	ins, err := o.instruction(ctx, tenant, actor, IPOApp{ID: x.ID, UCC: x.UCC, Name: x.Name, IssueID: iss.ID, Issue: iss.Name}, "Place", in.Instruction, false, orderText(iss, x))
	if err != nil {
		return EIPOOrder{}, err
	}
	x.Instruction, x.Ack = ins.ID, ins.Ack
	if x.Product != IssueTender {
		if err := o.eipoBlock(ctx, tenant, actor, &x, x.Amount, today); err != nil {
			return EIPOOrder{}, err
		}
	}
	x.event(now, actor, "order taken (%s, acknowledgement %s): %s", ins.Channel, ins.Ack, ins.Text)
	if set, _ := o.Settings(ctx, tenant); !iss.liveAt(set, now) {
		x.event(now, actor, "outside bidding hours: goes to NSE in the next session")
	}
	if _, err := docs.Put(ctx, o.Docs, tenant, cEIPOOrder, x.ID, x, 0, actor); err != nil {
		return EIPOOrder{}, err
	}
	o.log(ctx, tenant, actor, "ipo.order_placed", x.ID, map[string]any{"issue": iss.ID, "product": x.Product, "ucc": x.UCC, "qty": x.Qty, "amount": x.Amount.String(), "instruction": ins.ID})
	o.orderAck(ctx, tenant, actor, x, ins, iss)
	return x, nil
}

func orderText(iss IPOIssue, x EIPOOrder) string {
	switch x.Product {
	case IssueNCB:
		return fmt.Sprintf("%s: non-competitive bid for ₹%s face value; ₹%s held from the ledger", iss.Name, formatINR(money.DecInt(x.Qty)), formatINR(x.Amount.Dec()))
	case IssueSGB:
		form := "demat " + x.Depository + " " + x.DPID + x.BenID
		if x.Physical {
			form = "certificate (physical)"
		}
		return fmt.Sprintf("%s: %d gram(s) at ₹%s (%s price), %s; ₹%s held from the ledger", iss.Name, x.Qty, x.Price, strings.ToLower(x.PriceKind), form, formatINR(x.Amount.Dec()))
	}
	from := "demat " + x.Depository + " " + x.DPID + x.BenID
	if x.Physical {
		from = fmt.Sprintf("physical, folio %s, %d certificate(s)", x.Folio, len(x.Certs))
	}
	return fmt.Sprintf("%s: tender %d share(s) at ₹%s, category %s, from %s", iss.Name, x.Qty, x.Price, x.Category, from)
}

func init() {
	EventVars["ipo.order_ack"] = []string{"ack", "action", "issue", "order", "what"}
	extraTemplates = append(extraTemplates, Template{Event: "ipo.order_ack", Channel: ChEmail, Category: "Transactional", Active: true,
		Subject: "{issue}: your instruction {ack}",
		Body:    "Dear {name},\n\nWe have recorded your instruction to {action} order {order}: {what}. Acknowledgement number: {ack}.\n\nIf you did not give this instruction, tell us at once.\n\n{firm}"})
}

func (o *Ops) orderAck(ctx context.Context, tenant, actor string, x EIPOOrder, ins IPOInstruction, iss IPOIssue) {
	o.notify(ctx, tenant, actor, "ipo.order_ack", x.UCC, map[string]string{"ack": ins.Ack, "action": strings.ToLower(ins.Action), "issue": iss.Name, "order": x.ID, "what": orderText(iss, x)})
}

// ---- money on the ledger ----

// eipoFundsOK checks the client's free funds cover the money (counting
// what is already held for the order).
func (o *Ops) eipoFundsOK(ctx context.Context, tenant, ucc, date string, need, held money.Amount) error {
	w, err := o.Withdrawable(ctx, tenant, date, ucc)
	if err != nil {
		return err
	}
	if w.Blocked {
		return invalid("the client's free funds cannot be worked out (%s); the money must be in the client's ledger first", w.BlockedWhy)
	}
	if w.Available+held < need {
		return invalid("%s has ₹%s free and the order needs ₹%s from the ledger: collect the money first", ucc, formatINR((w.Available + held).Dec()), formatINR(need.Dec()))
	}
	return nil
}

// eipoBlock holds the money on the client's ledger, replacing what was
// held for an earlier version of the order (amount 0 releases it).
func (o *Ops) eipoBlock(ctx context.Context, tenant, actor string, x *EIPOOrder, amount money.Amount, date string) error {
	if x.FundsEntry != "" {
		if _, _, err := o.App.Ledger.Reverse(ctx, tenant, x.FundsEntry, actor, x.Product+" order "+x.ID+" changed", date); err != nil {
			return err
		}
		x.FundsEntry, x.Funds = "", 0
	}
	if amount <= 0 {
		return nil
	}
	x.FundsRev++
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: tenant, Key: fmt.Sprintf("EIPOF:%s:%d", x.ID, x.FundsRev), ValueDate: date, CreatedBy: actor,
		Narration: fmt.Sprintf("%s money held: %s order %s", x.Product, x.Issue, x.ID), Source: ledger.SourceRef{Kind: "EIPO-FUNDS", ID: x.ID},
		Postings: []ledger.Posting{{Account: ledger.ClientAccount(x.UCC), Debit: amount, Memo: x.Product + " money held: " + x.Symbol}, {Account: PayableEIPOFunds, Credit: amount, Memo: x.UCC + " " + x.ID}}})
	if err != nil {
		return err
	}
	x.Funds, x.FundsEntry = amount, e.ID
	return nil
}

// eipoRelease gives the money held back to the client's ledger.
func (o *Ops) eipoRelease(ctx context.Context, tenant, actor string, x *EIPOOrder, date, why string) error {
	if x.FundsEntry == "" {
		return nil
	}
	if _, _, err := o.App.Ledger.Reverse(ctx, tenant, x.FundsEntry, actor, x.Product+" money released: "+why, date); err != nil {
		return err
	}
	x.FundsEntry, x.ReleasedOn = "", date
	return nil
}

// ---- changing and cancelling ----

// EIPOOrderChange revises an order.
type EIPOOrderChange struct {
	Qty         int64            `json:"qty"`
	Price       money.Dec        `json:"price"`
	Online      bool             `json:"online"`
	Instruction IPOInstructionIn `json:"instruction"`
}

func (o *Ops) orderForChange(ctx context.Context, tenant, actor, id string) (EIPOOrder, IPOIssue, Client, error) {
	x, _, err := docs.Get[EIPOOrder](ctx, o.Docs, tenant, cEIPOOrder, id)
	if errors.Is(err, docs.ErrNotFound) {
		return x, IPOIssue{}, Client{}, fmt.Errorf("%w: no order %s", ErrNotFound, id)
	}
	if err != nil {
		return x, IPOIssue{}, Client{}, err
	}
	if f := o.ClientScope(ctx, tenant, actor); f != nil && !f(x.UCC) {
		return x, IPOIssue{}, Client{}, ErrNotFound
	}
	if !x.open() {
		return x, IPOIssue{}, Client{}, fmt.Errorf("%w: %s is %s%s", ErrState, x.ID, x.Status, map[bool]string{true: " (cancellation under way)", false: ""}[x.Pending == "Cancel"])
	}
	iss, err := o.IPOIssueByID(ctx, tenant, x.IssueID)
	if err != nil {
		return x, iss, Client{}, err
	}
	c, err := o.Client(ctx, tenant, x.UCC)
	return x, iss, c, err
}

// ModifyEIPOOrder revises an order on the client's instruction: during
// bidding, or in the T1 window to correct a mismatch. The client's demat
// details are read again, so correcting the client master and modifying
// the order corrects a mismatch.
func (o *Ops) ModifyEIPOOrder(ctx context.Context, tenant, actor, id string, in EIPOOrderChange) (EIPOOrder, error) {
	if err := o.require(ctx, tenant, actor, "Placing NCB, SGB and tender orders for clients", RoleMaker, RoleChecker, RoleBranch, RoleAP); err != nil {
		return EIPOOrder{}, err
	}
	x, iss, c, err := o.orderForChange(ctx, tenant, actor, id)
	if err != nil {
		return x, err
	}
	now := o.now()
	today := now.Format("2006-01-02")
	if err := iss.orderWindow(now, true); err != nil {
		return x, err
	}
	prevQty, prevAmt := x.Qty, x.Amount
	held := x.Funds
	x.Qty = firstPos(in.Qty, x.Qty)
	dm := x.Demat
	if _, ok := dematOf(c, dm); !ok {
		dm = "" // the account was corrected in the client master
	}
	oin := EIPOOrderInput{Qty: x.Qty, Price: in.Price, Category: x.Category, Physical: x.Physical, Online: in.Online, Demat: dm, Folio: x.Folio, Certs: x.Certs}
	if err := o.orderCheck(ctx, tenant, iss, c, &x, oin); err != nil {
		return x, err
	}
	if x.Product != IssueTender {
		if err := o.eipoFundsOK(ctx, tenant, c.UCC, today, x.Amount, held); err != nil {
			return x, err
		}
	}
	ins, err := o.instruction(ctx, tenant, actor, IPOApp{ID: x.ID, UCC: x.UCC, Name: x.Name, IssueID: iss.ID, Issue: iss.Name}, "Modify", in.Instruction, false, orderText(iss, x))
	if err != nil {
		return x, err
	}
	x.Instruction, x.Ack = ins.ID, ins.Ack
	if x.Product != IssueTender {
		if err := o.eipoBlock(ctx, tenant, actor, &x, x.Amount, today); err != nil {
			return x, err
		}
	}
	switch {
	case x.OrderNo != "":
		x.Pending = "Modify"
	case x.Status == OrderRefused:
		x.Pending, x.Status = "New", OrderDraft // sent again as a new order
	}
	x.Mismatch = ""
	x.event(now, actor, "modified (%s, acknowledgement %s): from %d to %d, ₹%s to ₹%s", ins.Channel, ins.Ack, prevQty, x.Qty, formatINR(prevAmt.Dec()), formatINR(x.Amount.Dec()))
	if err := docs.Upsert(ctx, o.Docs, tenant, cEIPOOrder, x.ID, x, actor); err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "ipo.order_modified", x.ID, map[string]any{"qty": x.Qty, "was": prevQty, "instruction": ins.ID})
	o.orderAck(ctx, tenant, actor, x, ins, iss)
	return x, nil
}

func firstPos(a, b int64) int64 {
	if a > 0 {
		return a
	}
	return b
}

// CancelEIPOOrder withdraws an order on the client's instruction. An
// order NSE does not hold is cancelled at once and its money released.
func (o *Ops) CancelEIPOOrder(ctx context.Context, tenant, actor, id string, in IPOInstructionIn) (EIPOOrder, error) {
	if err := o.require(ctx, tenant, actor, "Placing NCB, SGB and tender orders for clients", RoleMaker, RoleChecker, RoleBranch, RoleAP); err != nil {
		return EIPOOrder{}, err
	}
	x, iss, _, err := o.orderForChange(ctx, tenant, actor, id)
	if err != nil {
		return x, err
	}
	now := o.now()
	today := now.Format("2006-01-02")
	held := x.OrderNo != ""
	if held {
		if err := iss.orderWindow(now, false); err != nil {
			return x, fmt.Errorf("%w: orders NSE holds are cancelled during bidding: %v", ErrState, err)
		}
	}
	ins, err := o.instruction(ctx, tenant, actor, IPOApp{ID: x.ID, UCC: x.UCC, Name: x.Name, IssueID: iss.ID, Issue: iss.Name}, "Cancel", in, false, "cancel order "+x.ID+" in "+iss.Name)
	if err != nil {
		return x, err
	}
	x.Instruction, x.Ack = ins.ID, ins.Ack
	if held {
		x.Pending = "Cancel"
	} else {
		x.Status, x.Pending = OrderCancelled, ""
		if err := o.eipoRelease(ctx, tenant, actor, &x, today, "order cancelled"); err != nil {
			return x, err
		}
	}
	x.event(now, actor, "cancellation (%s, acknowledgement %s)%s", ins.Channel, ins.Ack, map[bool]string{true: ": goes to NSE with the next send", false: ""}[held])
	if err := docs.Upsert(ctx, o.Docs, tenant, cEIPOOrder, x.ID, x, actor); err != nil {
		return x, err
	}
	o.log(ctx, tenant, actor, "ipo.order_cancel", x.ID, map[string]any{"instruction": ins.ID, "immediate": !held})
	o.orderAck(ctx, tenant, actor, x, ins, iss)
	return x, nil
}

// ---- sending and status ----

func (o *Ops) issueOrders(ctx context.Context, tenant, issueID string) []EIPOOrder {
	all, _ := docs.List[EIPOOrder](ctx, o.Docs, tenant, cEIPOOrder, "")
	var out []EIPOOrder
	for _, x := range all {
		if x.IssueID == issueID {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// EIPOOrderResult says what a send or inquiry did.
type EIPOOrderResult struct {
	Sent     int      `json:"sent"`
	Placed   int      `json:"placed"`
	Refused  int      `json:"refused"`
	Updated  int      `json:"updated"`
	Mismatch int      `json:"mismatches"`
	Missing  int      `json:"incomplete"`
	Problems []string `json:"problems,omitempty"`
}

// EIPOSendOrders sends an issue's waiting orders to NSE.
func (o *Ops) EIPOSendOrders(ctx context.Context, tenant, actor, issueID string) (EIPOOrderResult, error) {
	var res EIPOOrderResult
	if err := o.require(ctx, tenant, actor, "Sending NCB, SGB and tender orders to NSE", RoleMaker, RoleChecker); err != nil {
		return res, err
	}
	conn, err := o.eipoUsable(ctx, tenant)
	if err != nil {
		return res, err
	}
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return res, err
	}
	if !eipoProduct(iss.Type) {
		return res, invalid("%s is a public issue: send its bids from Files & eIPO API", iss.Name)
	}
	now := o.now()
	if set, _ := o.Settings(ctx, tenant); !iss.liveAt(set, now) && !iss.inT1(now) {
		return res, fmt.Errorf("%w: orders go to NSE during bidding hours: %s", ErrState, iss.windowText())
	}
	var list []EIPOProductOrder
	var ids []string
	for _, x := range o.issueOrders(ctx, tenant, iss.ID) {
		if x.Pending == "" || x.Status == OrderCancelled || x.Status == OrderRejected {
			continue
		}
		if x.AppNo == "" && x.Product != IssueSGB { // NSE numbers SGB applications
			no, err := o.eipoAppNo(ctx, tenant, conn.cfg.Member)
			if err != nil {
				return res, err
			}
			if _, err := docs.Update(ctx, o.Docs, tenant, cEIPOOrder, x.ID, SystemActor, func(y *EIPOOrder) error { y.AppNo = no; return nil }); err != nil {
				return res, err
			}
			x.AppNo = no
		}
		series := ""
		if iss.Product != nil {
			series = iss.Product.Series
		}
		list = append(list, EIPOProductOrder{Product: x.Product, Symbol: iss.Symbol, Series: series, Activity: x.Pending, AppNo: x.AppNo, OrderNo: x.OrderNo, Ref: x.ID, Qty: x.Qty, Price: x.Price,
			Physical: x.Physical, PAN: x.PAN, Depository: x.Depository, DPID: x.DPID, BenID: x.BenID, ClientCode: x.UCC, ClientName: x.Name, Category: x.Category, Folio: x.Folio, Certs: x.Certs})
		ids = append(ids, x.ID)
	}
	if len(list) == 0 {
		return res, invalid("no orders are waiting to go to NSE for %s", iss.Name)
	}
	ups, err := eipoProducts.ProductSend(ctx, conn.cfg, iss.Type, list)
	o.eipoProdState(ctx, tenant, func(s *eipoProdState) {
		s.LastSend[iss.ID], s.LastError = now.Format(time.RFC3339), ""
		if err != nil {
			s.LastError = err.Error()
		}
	})
	if err != nil && len(ups) == 0 {
		return res, err
	}
	for i, u := range ups {
		if i >= len(ids) {
			break
		}
		res.Sent++
		act := list[i].Activity
		var after EIPOOrder
		_, uerr := docs.Update(ctx, o.Docs, tenant, cEIPOOrder, ids[i], actor, func(y *EIPOOrder) error {
			y.SentAt = now.Format(time.RFC3339)
			o.applyOrderUpdate(ctx, tenant, actor, y, u, act, now)
			after = *y
			return nil
		})
		if uerr != nil {
			return res, uerr
		}
		switch after.Status {
		case OrderPlaced, OrderCancelled:
			res.Placed++
		case OrderRefused:
			res.Refused++
		}
	}
	if err != nil {
		res.Problems = append(res.Problems, "stopped: "+err.Error())
	}
	o.productWork(ctx, tenant, iss, now)
	if iss.Type == IssueTender && res.Placed > 0 {
		o.tenderSentWork(ctx, tenant, iss, now)
	}
	o.log(ctx, tenant, actor, "ipo.orders_sent", iss.ID, map[string]any{"sent": res.Sent, "placed": res.Placed, "refused": res.Refused})
	return res, nil
}

// orderOK reads NSE's answer to an action: ok, failed, and whether it
// said at all.
func orderOK(u EIPOOrderUpdate) (ok, failed bool) {
	switch u.OrderStatus {
	case "ES", "MS", "CS":
		return true, false
	case "EF", "MF", "CF":
		return false, true
	}
	if strings.EqualFold(u.Status, "failed") {
		return false, true
	}
	// A reply with a rejection reason and no order status (the tender
	// sample) is read, safely, as not taken.
	if strings.EqualFold(u.Status, "success") && u.Reason == "" {
		return true, false
	}
	return false, u.Reason != ""
}

// applyOrderUpdate moves an order on with NSE's news; act is the action
// this answers ("" for an inquiry).
func (o *Ops) applyOrderUpdate(ctx context.Context, tenant, actor string, x *EIPOOrder, u EIPOOrderUpdate, act string, now time.Time) {
	today := now.In(ist).Format("2006-01-02")
	if u.OrderNo != "" && u.OrderNo != "0" {
		x.OrderNo = u.OrderNo
	}
	if u.AppNo != "" && x.AppNo == "" {
		x.AppNo = u.AppNo
	}
	if u.OrderStatus != "" {
		x.OrderStatus = u.OrderStatus
	}
	if u.At != "" {
		x.LastAction = u.At
	}
	ok, failed := orderOK(u)
	if act != "" {
		x.Pending = ""
		switch {
		case ok && act == "Cancel" || u.OrderStatus == "CS":
			x.Status = OrderCancelled
			_ = o.eipoRelease(ctx, tenant, actor, x, today, "cancelled on NSE")
			x.event(now, actor, "cancelled on NSE")
		case ok:
			x.Status, x.ExchQty, x.ExchPrice, x.PlacedAmount, x.Reason = OrderPlaced, x.Qty, x.Price, x.Amount, ""
			x.event(now, actor, "%s accepted by NSE (order %s)", strings.ToLower(act), firstOf(x.OrderNo, "—"))
		case failed && act == "New":
			x.Status, x.Reason = OrderRefused, firstOf(u.Reason, "refused")
			x.event(now, actor, "NSE refused the order: %s", x.Reason)
		case failed && act == "Modify":
			// NSE still holds the earlier order: go back to it.
			x.Reason = firstOf(u.Reason, "modification refused")
			if x.ExchQty > 0 {
				x.Qty, x.Price, x.Amount = x.ExchQty, x.ExchPrice, x.PlacedAmount
				if x.Product != IssueTender {
					_ = o.eipoBlock(ctx, tenant, actor, x, x.Amount, today)
				}
			}
			x.event(now, actor, "NSE refused the modification (%s): the earlier order stands", x.Reason)
		case failed && act == "Cancel":
			x.Reason = firstOf(u.Reason, "cancellation refused")
			x.event(now, actor, "NSE refused the cancellation (%s): the order stands", x.Reason)
		default:
			x.Status = OrderPlaced
			x.event(now, actor, "%s sent to NSE", strings.ToLower(act))
		}
	} else if u.OrderStatus == "CS" && x.Status != OrderCancelled {
		x.Status = OrderCancelled
		_ = o.eipoRelease(ctx, tenant, actor, x, today, "cancelled on NSE")
		x.event(now, actor, "NSE shows the order cancelled")
	}
	if u.VerStatus != "" {
		v := map[string]string{"P": "Pending", "S": "Verified", "F": "Failed"}[u.VerStatus]
		if v != "" && v != x.VerStatus {
			x.VerStatus, x.VerReason = v, u.VerReason
			x.event(now, actor, "depository verification: %s%s", v, map[bool]string{true: " (" + u.VerReason + ")", false: ""}[u.VerReason != ""])
		}
	}
	if u.Clearing != "" && u.Clearing != x.Clearing {
		x.Clearing, x.ClearReason = u.Clearing, u.ClearReason
		x.event(now, actor, "clearing: %s%s", clearingText[u.Clearing], map[bool]string{true: " (" + u.ClearReason + ")", false: ""}[u.ClearReason != ""])
		switch u.Clearing {
		case "RJ":
			if x.Status != OrderCancelled {
				x.Status = OrderRejected
				_ = o.eipoRelease(ctx, tenant, actor, x, today, "rejected in clearing")
			}
		case "AL", "RA":
			x.Status = OrderAllotted
		}
	}
	if u.Amount != "" {
		if a := productExchAmount(x, u.Amount); a > 0 && a != x.ExchAmount {
			x.ExchAmount = a
			if x.Product == IssueNCB && x.FundsEntry != "" && a != x.Funds && x.Status != OrderAllotted {
				// The auction set the price: hold what is payable.
				was := x.Funds
				if o.eipoBlock(ctx, tenant, actor, x, a, today) == nil {
					x.Amount = a
					x.event(now, actor, "amount payable ₹%s: money held changed from ₹%s", formatINR(a.Dec()), formatINR(was.Dec()))
				}
			}
		}
	}
	if u.RTAStatus != "" {
		x.RTAStatus, x.RTAReason = u.RTAStatus, u.RTAReason
	}
	if u.CustStatus != "" {
		x.CustStatus = u.CustStatus
	}
}

// clearingText says what NSE's clearing status codes mean.
var clearingText = map[string]string{"FP": "funds pending (the member pays NSCCL)", "FS": "funds sent to NSCCL", "RJ": "rejected", "AL": "allotted",
	"FR": "funds received", "RS": "sent to RBI", "RA": "allotted by RBI"}

// productExchAmount reads the amount NSE reports. For NCB the samples give
// totalAmountPayable in paise (₹20,000 face value at ₹106 is 2120000); it
// is read as paise when that is a plausible price (50% to 200% of the face
// value), else as rupees, else ignored.
func productExchAmount(x *EIPOOrder, s string) money.Amount {
	d, err := money.ParseDec(strings.TrimSpace(s))
	if err != nil || d.Sign() <= 0 {
		return 0
	}
	if x.Product != IssueNCB {
		return d.Round(money.UnitPaisa)
	}
	face := money.DecInt(x.Qty)
	half, _ := face.Quo(money.DecInt(2))
	plausible := func(a money.Dec) bool {
		return a.Cmp(half) >= 0 && a.Cmp(face.MulInt(2)) <= 0
	}
	if p, _ := d.Quo(money.DecInt(100)); plausible(p) {
		return p.Round(money.UnitPaisa)
	}
	if plausible(d) {
		return d.Round(money.UnitPaisa)
	}
	return 0
}

// EIPOInquireOrders asks NSE about an issue's orders in flight, its
// mismatches (NCB, SGB) and incomplete applications (SGB).
func (o *Ops) EIPOInquireOrders(ctx context.Context, tenant, actor, issueID string) (EIPOOrderResult, error) {
	var res EIPOOrderResult
	if err := o.require(ctx, tenant, actor, "Sending NCB, SGB and tender orders to NSE", RoleMaker, RoleChecker); err != nil {
		return res, err
	}
	conn, err := o.eipoUsable(ctx, tenant)
	if err != nil {
		return res, err
	}
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return res, err
	}
	if !eipoProduct(iss.Type) {
		return res, invalid("%s is a public issue", iss.Name)
	}
	now := o.now()
	orders := o.issueOrders(ctx, tenant, iss.ID)
	var keys []string
	by := map[string]string{} // application or order number -> order
	for _, x := range orders {
		k := x.AppNo
		if x.Product == IssueSGB {
			k = x.OrderNo
		}
		if x.AppNo != "" {
			by["A"+x.AppNo] = x.ID
		}
		if x.OrderNo != "" {
			by["O"+x.OrderNo] = x.ID
		}
		by["R"+x.ID] = x.ID
		if k != "" && !x.final() && x.Status != OrderDraft {
			keys = append(keys, k)
		}
	}
	find := func(u EIPOOrderUpdate) string {
		for _, k := range []string{"R" + u.Ref, "O" + u.OrderNo, "A" + u.AppNo} {
			if id := by[k]; id != "" && len(k) > 1 {
				return id
			}
		}
		return ""
	}
	var ups []EIPOOrderUpdate
	var problems []string
	if len(keys) > 0 {
		got, err := eipoProducts.ProductFetch(ctx, conn.cfg, iss.Type, iss.Symbol, keys)
		ups = append(ups, got...)
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	live := !iss.closed(now) || daysBetween(iss.Close, now.Format("2006-01-02")) <= 3
	if (iss.Type == IssueNCB || iss.Type == IssueSGB) && live && len(orders) > 0 {
		mm, err := eipoProducts.ProductMismatches(ctx, conn.cfg, iss.Type, iss.Symbol)
		if err != nil {
			problems = append(problems, err.Error())
		}
		seen := map[string]string{}
		for _, m := range mm {
			if id := find(m); id != "" {
				seen[id] = m.Mismatch
			}
		}
		for _, x := range orders {
			if x.final() {
				continue
			}
			why, hit := seen[x.ID]
			if hit || x.Mismatch != "" {
				if why != x.Mismatch {
					_, _ = docs.Update(ctx, o.Docs, tenant, cEIPOOrder, x.ID, actor, func(y *EIPOOrder) error {
						y.Mismatch = why
						if why != "" {
							y.event(now, actor, "NSE lists a mismatch: %s", why)
						}
						return nil
					})
				}
				if hit {
					res.Mismatch++
				}
			}
		}
	}
	if iss.Type == IssueSGB && len(orders) > 0 && (iss.Product == nil || iss.Product.IncompleteTo == "" || now.Format("2006-01-02") <= iss.Product.IncompleteTo) {
		inc, err := eipoProducts.SGBIncomplete(ctx, conn.cfg, iss.Symbol)
		if err != nil {
			problems = append(problems, err.Error())
		}
		codes := map[string][]int{}
		for _, m := range inc {
			if id := by["O"+m.OrderNo]; id != "" {
				codes[id] = m.Codes
			}
		}
		for _, x := range orders {
			c := codes[x.ID]
			if fmt.Sprint(c) != fmt.Sprint(x.Incomplete) && (len(c) > 0 || len(x.Incomplete) > 0) {
				_, _ = docs.Update(ctx, o.Docs, tenant, cEIPOOrder, x.ID, actor, func(y *EIPOOrder) error {
					y.Incomplete = c
					if len(c) > 0 {
						y.event(now, actor, "NSE lists the application as incomplete: %s", sgbIncompleteText(c))
					}
					return nil
				})
			}
			if len(c) > 0 {
				res.Missing++
			}
		}
	}
	for _, u := range ups {
		id := find(u)
		if id == "" {
			continue
		}
		changed := false
		_, err := docs.Update(ctx, o.Docs, tenant, cEIPOOrder, id, actor, func(y *EIPOOrder) error {
			before, _ := json.Marshal(y)
			o.applyOrderUpdate(ctx, tenant, actor, y, u, "", now)
			after, _ := json.Marshal(y)
			changed = !bytes.Equal(before, after)
			return nil
		})
		if err != nil {
			return res, err
		}
		if changed {
			res.Updated++
		}
	}
	o.eipoProdState(ctx, tenant, func(s *eipoProdState) {
		s.LastAsk[iss.ID] = now.Format(time.RFC3339)
		if len(problems) > 0 {
			s.LastError = problems[0]
		}
	})
	res.Problems = problems
	o.productWork(ctx, tenant, iss, now)
	return res, nil
}

// productWork raises or closes the issue's work items: orders NSE refused,
// mismatches, incomplete SGB applications, and money to pay in to NSCCL.
func (o *Ops) productWork(ctx context.Context, tenant string, iss IPOIssue, now time.Time) {
	orders := o.issueOrders(ctx, tenant, iss.ID)
	today := now.In(ist).Format("2006-01-02")
	var refused, mism, incomplete, pending []string
	var due money.Amount
	for _, x := range orders {
		line := fmt.Sprintf("%s · %s %s", x.ID, x.UCC, x.Name)
		switch {
		case x.Status == OrderRefused:
			refused = append(refused, line+" · "+x.Reason)
		case x.final():
			continue
		}
		if x.Mismatch != "" || x.VerStatus == "Failed" {
			mism = append(mism, line+" · "+firstOf(x.Mismatch, firstOf(x.VerReason, "depository verification failed")))
		}
		if len(x.Incomplete) > 0 {
			incomplete = append(incomplete, line+" · "+sgbIncompleteText(x.Incomplete))
		}
		if x.Clearing == "FP" && x.Status == OrderPlaced {
			pending = append(pending, fmt.Sprintf("%s · ₹%s", line, formatINR(x.Funds.Dec())))
			due += x.Funds
		}
	}
	key := func(k string) string { return k + "-" + iss.ID }
	work := func(k string, lines []string, title, detail, role string) {
		if len(lines) == 0 {
			o.closeWork(ctx, tenant, SystemActor, "ipo", key(k), "nothing left")
			return
		}
		o.raiseWork(ctx, tenant, "ipo", key(k), title, detail+"\n"+strings.Join(lines, "\n"), "", role, "ipo", today)
	}
	work("eipo-refused", refused, fmt.Sprintf("%s: NSE refused %d order(s)", iss.Name, len(refused)),
		"Correct what NSE says (the client's PAN or demat details in the client master), then modify the order so it goes again, or cancel it on the client's instruction. Money held for a cancelled order goes back to the client's ledger.", RoleMaker)
	work("eipo-mismatch", mism, fmt.Sprintf("%s: %d order(s) with a PAN or demat mismatch", iss.Name, len(mism)),
		"NSE could not match the PAN with the demat account. Correct the client's details and modify the order during bidding or in the T1 modification window, or the order is rejected.", RoleMaker)
	work("eipo-incomplete", incomplete, fmt.Sprintf("%s: %d SGB application(s) missing investor details", iss.Name, len(incomplete)),
		"RBI needs these details. Open each order on Bonds, SGB & tenders, check the details (filled in from the client master where it has them) and send them"+map[bool]string{true: " by " + fmtDay(productOr(iss).IncompleteTo), false: ""}[productOr(iss).IncompleteTo != ""]+".", RoleMaker)
	work("eipo-funds", pending, fmt.Sprintf("%s: pay ₹%s in to NSCCL for %d order(s)", iss.Name, formatINR(due.Dec()), len(pending)),
		"NSE shows these orders as funds pending. The money is held from each client's ledger (PAYABLE:EIPO_FUNDS); pay it in to the clearing corporation as its obligation says, by the pay-in time.", RoleChecker)
}

func productOr(iss IPOIssue) IPOProduct {
	if iss.Product == nil {
		return IPOProduct{}
	}
	return *iss.Product
}

// ---- the products' tick ----

// eipoProductTick, for the products the firm offers: reads NSE's masters
// each morning (08:30) and when NSE says an issue changed; sends waiting
// orders every 5 minutes while bidding is open (with the eIPO connection's
// sending on); asks about orders in flight every 15 minutes until a few
// days after the close (or the allotment); and after the close lapses
// orders never placed, releasing their money, and asks for a tender's
// acceptance.
func (o *Ops) eipoProductTick(ctx context.Context, tenant string, conn eipoConn, set Settings, now time.Time) {
	ps := o.eipoProductSettings(ctx, tenant)
	if !ps.NCB && !ps.SGB && !ps.Tender {
		return
	}
	now = now.In(ist)
	today, hm := now.Format("2006-01-02"), now.Format("15:04")
	st := o.eipoProdState(ctx, tenant, nil)
	ist0, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIPOState, "eipo", EIPOState{})
	if hm >= "08:30" && hm <= "20:00" && (ist0.MasterRefresh || !strings.HasPrefix(laterIST(st.LastMaster), today)) {
		_, _, _ = o.EIPOSyncProducts(ctx, tenant, SystemActor)
	}
	since := func(s string) time.Duration {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return 24 * time.Hour
		}
		return now.Sub(t)
	}
	is, _ := docs.List[IPOIssue](ctx, o.Docs, tenant, cIPOIssue, "")
	for _, iss := range is {
		if !eipoProduct(iss.Type) || !ps.on(iss.Type) || iss.Withdrawn || today < iss.Open {
			continue
		}
		iss.defaults()
		iss.Timeline = iss.timeline(set)
		orders := o.issueOrders(ctx, tenant, iss.ID)
		if len(orders) == 0 {
			continue
		}
		st = o.eipoProdState(ctx, tenant, nil)
		waiting, flying := false, false
		for _, x := range orders {
			waiting = waiting || x.Pending != "" && x.Status != OrderCancelled && x.Status != OrderRejected
			flying = flying || !x.final() && x.Status != OrderDraft && (x.AppNo != "" || x.OrderNo != "")
		}
		if conn.send && waiting && (iss.liveAt(set, now) || iss.inT1(now)) && since(st.LastSend[iss.ID]) >= 5*time.Minute {
			_, _ = o.EIPOSendOrders(ctx, tenant, SystemActor, iss.ID)
		}
		end := firstOf(productOr(iss).AllotDate, iss.Close)
		if flying && daysBetween(end, today) <= 5 && since(st.LastAsk[iss.ID]) >= 15*time.Minute {
			_, _ = o.EIPOInquireOrders(ctx, tenant, SystemActor, iss.ID)
		}
		if iss.windowOver(now) {
			o.lapseOrders(ctx, tenant, iss, now)
			if iss.Type == IssueTender && o.ipoOnce(ctx, tenant, iss.ID, "acceptance:"+iss.Close, now) {
				n := 0
				for _, x := range o.issueOrders(ctx, tenant, iss.ID) {
					if x.Status == OrderPlaced {
						n++
					}
				}
				if n > 0 {
					o.raiseWork(ctx, tenant, "ipo", "tender-accept-"+iss.ID, fmt.Sprintf("%s: record the acceptance of %d tender(s)", iss.Name, n),
						"The offer closed on "+fmtDay(iss.Close)+". When the registrar ("+firstOf(iss.Registrar, "see the offer")+") publishes the basis of acceptance, record each client's accepted shares on Bonds, SGB & tenders: accepted demat shares leave the client's holding and the proceeds are credited to the client's ledger through the clearing corporation. Shares not accepted come back to the client's demat account.", "", RoleChecker, "ipo", today)
				}
			}
		}
	}
}

// lapseOrders marks orders never placed before the close as rejected and
// releases their money, once the bidding (and T1) window has passed.
func (o *Ops) lapseOrders(ctx context.Context, tenant string, iss IPOIssue, now time.Time) {
	today := now.In(ist).Format("2006-01-02")
	for _, x := range o.issueOrders(ctx, tenant, iss.ID) {
		if (x.Status != OrderDraft && x.Status != OrderRefused) || x.OrderNo != "" {
			continue
		}
		_, _ = docs.Update(ctx, o.Docs, tenant, cEIPOOrder, x.ID, SystemActor, func(y *EIPOOrder) error {
			if (y.Status != OrderDraft && y.Status != OrderRefused) || y.OrderNo != "" {
				return nil
			}
			y.Status, y.Pending = OrderRejected, ""
			y.Reason = firstOf(y.Reason, "not placed before the close")
			if err := o.eipoRelease(ctx, tenant, SystemActor, y, today, "not placed before the close"); err != nil {
				return err
			}
			y.event(now, SystemActor, "not placed on NSE before the close: money held released")
			return nil
		})
	}
	o.productWork(ctx, tenant, iss, now)
}

// ---- views ----

// EIPOProductsView is the Bonds, SGB & tenders tab.
type EIPOProductsView struct {
	Issues   []IPOIssue          `json:"issues"`
	Orders   []EIPOOrder         `json:"orders"`
	Counts   map[string]int      `json:"counts"`
	Statuses []string            `json:"statuses"`
	Channels []string            `json:"channels"`
	Kinds    []string            `json:"kinds"`
	Settings EIPOProductSettings `json:"settings"`
	State    struct {
		LastMaster string `json:"last_master,omitempty"`
		MasterNew  int    `json:"master_new"`
		LastError  string `json:"last_error,omitempty"`
	} `json:"state"`
	Usable bool `json:"usable"`
	Send   bool `json:"send"`
}

// EIPOProducts lists the NCB, SGB and tender issues and orders, optionally
// for one issue and status, newest first.
func (o *Ops) EIPOProducts(ctx context.Context, tenant, actor, issue, status string) (EIPOProductsView, error) {
	v := EIPOProductsView{Issues: []IPOIssue{}, Orders: []EIPOOrder{}, Counts: map[string]int{}, Statuses: OrderStatuses, Channels: IPOChannels, Kinds: TenderKinds,
		Settings: o.eipoProductSettings(ctx, tenant)}
	is, err := o.IPOIssues(ctx, tenant)
	if err != nil {
		return v, err
	}
	for _, i := range is {
		if eipoProduct(i.Type) {
			v.Issues = append(v.Issues, i)
		}
	}
	all, err := docs.List[EIPOOrder](ctx, o.Docs, tenant, cEIPOOrder, "")
	if err != nil {
		return v, err
	}
	f := o.ClientScope(ctx, tenant, actor)
	for _, x := range all {
		if f != nil && !f(x.UCC) || issue != "" && x.IssueID != issue {
			continue
		}
		v.Counts[x.Status]++
		if status == "" || x.Status == status {
			v.Orders = append(v.Orders, x)
		}
	}
	sort.SliceStable(v.Orders, func(i, j int) bool { return v.Orders[i].Created > v.Orders[j].Created })
	st := o.eipoProdState(ctx, tenant, nil)
	v.State.LastMaster, v.State.MasterNew, v.State.LastError = st.LastMaster, st.MasterNew, st.LastError
	if c, ok := o.eipoConnection(ctx, tenant); ok {
		v.Usable, v.Send = c.usable, c.send
	}
	return v, nil
}

// EIPOOrderDetail is one order with its issue, consent log and, for an
// incomplete SGB application, the details to send.
type EIPOOrderDetail struct {
	Order        EIPOOrder        `json:"order"`
	Issue        *IPOIssue        `json:"issue,omitempty"`
	Instructions []IPOInstruction `json:"instructions"`
	KYC          *SGBKYC          `json:"kyc,omitempty"`
}

// EIPOOrderByID returns an order for the screen.
func (o *Ops) EIPOOrderByID(ctx context.Context, tenant, actor, id string) (EIPOOrderDetail, error) {
	x, _, err := docs.Get[EIPOOrder](ctx, o.Docs, tenant, cEIPOOrder, id)
	if errors.Is(err, docs.ErrNotFound) {
		return EIPOOrderDetail{}, fmt.Errorf("%w: no order %s", ErrNotFound, id)
	}
	if err != nil {
		return EIPOOrderDetail{}, err
	}
	if f := o.ClientScope(ctx, tenant, actor); f != nil && !f(x.UCC) {
		return EIPOOrderDetail{}, ErrNotFound
	}
	d := EIPOOrderDetail{Order: x, Instructions: []IPOInstruction{}}
	if iss, err := o.IPOIssueByID(ctx, tenant, x.IssueID); err == nil {
		d.Issue = &iss
	}
	all, _ := docs.List[IPOInstruction](ctx, o.Docs, tenant, cIPOInstr, "")
	for _, ins := range all {
		if ins.Bid == x.ID {
			d.Instructions = append(d.Instructions, ins)
		}
	}
	if x.Product == IssueSGB && len(x.Incomplete) > 0 {
		k := o.sgbKYCFor(ctx, tenant, x)
		d.KYC = &k
	}
	return d, nil
}
