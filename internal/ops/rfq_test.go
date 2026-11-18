package ops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// The fake RFQ platform answers with the protocol's own samples (RFQ –
// Protocol for Web API 1.1.10), their dates moved to the test's day and the
// samples' JSON typos (a missing comma, "approvalStatus : ") put right.
// The member is DFHI, the responder in the samples.

const (
	rfqTestMember = "DFHI"
	rfqTestPass   = "made-up-pw"
	rfqCBToken    = "made-up-callback-token-0123456789"
	rfqCBKey      = "made-up-signing-key"
)

// rfqDay moves the samples' dates to 18 and 19 Nov 2026.
var rfqDay = strings.NewReplacer("09-Dec-2022", "18-Nov-2026", "10-Dec-2022", "19-Nov-2026")

// POST /rest/v1/negotiation/accept sample response (a responder's direct
// acceptance), for a client of the broker's (respDealType B, BARCLAYS, as in
// the sample request).
const rfqSampleTraded = `{"date": "09-Dec-2022", "acceptedCalcMethod": "O", "tradeNumber": "221209990000055", "initQuoteTime": "21:52:42", "initSettlementType": 0,
 "respAeCode": "DFHI", "respDealType": "B", "respClientCode": "BARCLAYS", "tradeSplits": null, "initSettlementDate": "09-Dec-2022", "initAccruedInterest": null, "acceptedValue": 1,
 "confirmedPriceQuoteTime": null, "initGtdFlag": null, "initYield": 6.0000, "respAccruedInterest": null, "confirmStatus": null, "id": "N22120900000054", "initYieldType": "YTM",
 "respPrice": null, "initLoginId": null, "respQuoteTime": null, "respEndTime": null, "acceptedBySide": "R", "respCalcMethod": null, "initAeCode": "INDBANK",
 "acceptedSettlementDate": "09-Dec-2022", "acceptedPrice": null, "respGtdFlag": null, "acceptedQuantity": 10, "acceptedByLoginId": "NSHARMA", "acceptedAccruedInterest": null,
 "respConsideration": null, "initQuantity": 10, "status": "A", "respSettlementDate": null, "respValue": null, "respLoginId": "NSHARMA", "initPrice": null, "respYieldType": null,
 "acceptedYieldType": "YTM", "acceptedSettlementType": 0, "acceptedConsideration": null, "respSettlementType": null, "respRemarks": null, "initValue": 1, "initCalcMethod": "O",
 "buySell": "S", "acceptedQuoteTime": "21:52:42", "initAuId": null, "rfqNumber": "R22120900000020", "acceptedPutCallDate": null, "acceptedYield": 6.0000, "respYield": null,
 "initConsideration": null, "initEndTime": null, "respQuantity": null, "isin": "INE020B08BH6", "initRemarks": null, "lastActivityTimestamp": "09-Dec-2022 21:03:33"}`

// POST /rest/v1/deal/changeclient sample response, as the counterparty
// (the initiator, INDBANK) proposed it: confirmStatus PC, proposed by I.
// The responder's client is INST*, to be split.
const rfqSampleProposed = `{"date": "09-Dec-2022", "acceptedCalcMethod": "O", "tradeNumber": "221209990000054", "initQuoteTime": "21:52:20", "initSettlementType": 0, "respAeCode": "DFHI",
 "tradeSplits": null, "initSettlementDate": "09-Dec-2022", "initAccruedInterest": null, "acceptedValue": 1.000000, "confirmedPriceQuoteTime": null, "initGtdFlag": null,
 "initYield": 6.0000, "respClientCode": "INST*", "respAccruedInterest": null, "confirmStatus": "PC", "proposedBySide": "I", "respClientRegType": "I", "id": "N22120900000053",
 "initYieldType": "YTM", "respPrice": null, "initLoginId": null, "respQuoteTime": null, "respEndTime": null, "acceptedBySide": "R", "respCalcMethod": null, "initAeCode": "INDBANK",
 "acceptedSettlementDate": "09-Dec-2022", "acceptedPrice": 111.0000, "respGtdFlag": null, "acceptedQuantity": 10, "acceptedByLoginId": "NSHARMA", "acceptedAccruedInterest": 12.00,
 "respConsideration": null, "initQuantity": 10, "respDealType": "B", "status": "A", "respSettlementDate": null, "respValue": null, "initClientRegType": "I", "respLoginId": "NSHARMA",
 "initPrice": null, "initClientCode": "INDBANK", "respYieldType": null, "acceptedYieldType": "YTM", "acceptedSettlementType": 0, "acceptedConsideration": 11100012.00,
 "respSettlementType": null, "respRemarks": null, "initValue": 1.000000, "initCalcMethod": "O", "buySell": "S", "acceptedQuoteTime": "21:52:20", "initAuId": null,
 "rfqNumber": "R22120900000020", "acceptedPutCallDate": "07-Dec-2028", "acceptedYield": 6.0000, "respYield": null, "initConsideration": null, "initEndTime": null,
 "respQuantity": null, "isin": "INE020B08BH6", "initDealType": "D", "initRemarks": null, "lastActivityTimestamp": "09-Dec-2022 21:03:33"}`

// POST /rest/v1/deal/acceptreject sample response's thread, before the
// acceptance: proposed by the initiator for the responder's client BARCLAYS.
const rfqSampleProposed2 = `{"date": "09-Dec-2022", "acceptedCalcMethod": "O", "tradeNumber": "221209990000049", "initQuoteTime": "21:45:39", "initSettlementType": 0, "respAeCode": "DFHI",
 "respDealType": "B", "respClientCode": "BARCLAYS", "tradeSplits": null, "initSettlementDate": "09-Dec-2022", "initAccruedInterest": null, "acceptedValue": 1.000000,
 "confirmedPriceQuoteTime": null, "initGtdFlag": null, "initYield": 6.0000, "respAccruedInterest": null, "confirmStatus": "PC", "proposedBySide": "I", "id": "N22120900000048",
 "initYieldType": "YTM", "respPrice": null, "initLoginId": null, "respQuoteTime": null, "respEndTime": null, "acceptedBySide": "R", "respCalcMethod": null, "initAeCode": "INDBANK",
 "acceptedSettlementDate": "09-Dec-2022", "acceptedPrice": 111.0000, "respGtdFlag": null, "acceptedQuantity": 10, "acceptedByLoginId": "DFHI", "acceptedAccruedInterest": 1.00,
 "respConsideration": null, "initQuantity": 10, "status": "A", "respSettlementDate": null, "respValue": null, "respLoginId": "DFHI", "initPrice": null, "respYieldType": null,
 "acceptedYieldType": "YTM", "acceptedSettlementType": 0, "acceptedConsideration": 11100001.00, "respSettlementType": null, "respRemarks": null, "initValue": 1.000000,
 "initCalcMethod": "O", "buySell": "S", "acceptedQuoteTime": "21:45:39", "initAuId": null, "rfqNumber": "R22120900000020", "acceptedPutCallDate": "07-Dec-2028",
 "acceptedYield": 6.0000, "respYield": null, "initConsideration": null, "initEndTime": null, "respQuantity": null, "isin": "INE020B08BH6", "initRemarks": null,
 "lastActivityTimestamp": "09-Dec-2022 21:03:33"}`

// POST /rest/v1/negotiation/all sample: a thread still negotiating.
const rfqSampleNegotiating = `{"date": "09-Dec-2022", "acceptedCalcMethod": null, "tradeNumber": null, "initQuoteTime": null, "initSettlementType": null, "respAeCode": "DFHI",
 "tradeSplits": null, "id": "N22120900000046", "respQuoteTime": "21:29:07", "respEndTime": "22:00", "initAeCode": "INDBANK", "status": "N", "respSettlementDate": "09-Dec-2022",
 "respValue": 2.000000, "respLoginId": "NSHARMA", "respYieldType": "YTM", "respSettlementType": 0, "buySell": "S", "rfqNumber": "R22120900000020", "respYield": 4.5550,
 "respQuantity": 20, "isin": "INE020B08BH6", "lastActivityTimestamp": "09-Dec-2022 21:03:33"}`

// POST /rest/v1/dealamend/all samples: the modification (proposed by DFHI)
// and the cancellation, here proposed by INDBANK for DFHI to decide, both
// on deals of the fake's.
const rfqSampleAmendMod = `{"id": 123, "rfqNumber": "R22120900000020", "ngId": "N22120900000054", "requestType": "M", "date": "24-Jun-2023", "tradeNumber": "230624990000002",
 "isin": "INE020B08BH6", "description": "RURAL ELECTRIFICATION CORPORATION LIMITED SERIES 169 8.37 BD 07DC28 FVRS10LAC", "buyerAeCode": "INDBANK", "buyerClientCode": "INDBANK",
 "sellerAeCode": "DFHI", "sellerClientCode": "DFHI", "value": 1.00, "quantity": 10, "yieldType": "YTM", "yield": 5.75, "price": 103.455, "accruedInterest": 12345.50,
 "consideration": 10357845.50, "settlementType": 0, "settlementDate": "24-Jun-2023", "modYieldType": "YTM", "modAccruedInterest": 12345.67, "modConsideration": 10357845.67,
 "modSettlementType": 1, "modSettlementDate": "26-Jun-2023", "modReason": "SETTYP", "cancReason": null, "proposerAeCode": "DFHI", "proposerTime": "15:55:45",
 "proposerRemarks": null, "counterAeCode": "INDBANK", "counterTime": null, "counterRemarks": null, "approvalStatus": "PP"}`

const rfqSampleAmendCanc = `{"id": 126, "rfqNumber": "R22120900000020", "ngId": "N22120900000053", "requestType": "C", "date": "24-Jun-2023", "tradeNumber": "230624990000002",
 "isin": "INE020B08BH6", "description": "RURAL ELECTRIFICATION CORPORATION LIMITED SERIES 169 8.37 BD 07DC28 FVRS10LAC", "buyerAeCode": "INDBANK", "buyerClientCode": "INDBANK",
 "sellerAeCode": "DFHI", "sellerClientCode": "DFHI", "value": 1.00, "quantity": 10, "yieldType": "YTM", "yield": 5.75, "price": 103.455, "accruedInterest": 12345.50,
 "consideration": 10357845.50, "settlementType": 0, "settlementDate": "24-Jun-2023", "cancReason": "SHRT", "proposerAeCode": "INDBANK", "proposerTime": "15:55:45",
 "proposerRemarks": null, "counterAeCode": "DFHI", "counterTime": null, "counterRemarks": null, "approvalStatus": "PP"}`

// GET /rest/v1/market/timings sample.
const rfqSampleTimings = `[{"starttimeT1": 36000000, "type": "CD", "starttimeT0": 36000000, "pricecutoffT0": 77400000, "pricecutoffT1": 77400000, "yieldcutoffT0": 77400000, "yieldcutoffT1": 77400000},
 {"starttimeT1": 36000000, "type": "CP", "starttimeT0": 36000000, "pricecutoffT0": 77400000, "pricecutoffT1": 77400000, "yieldcutoffT0": 77400000, "yieldcutoffT1": 77400000},
 {"starttimeT1": 32400000, "type": "DEFAULT", "starttimeT0": 32400000, "pricecutoffT0": 80100000, "pricecutoffT1": 86100000, "yieldcutoffT0": 79200000, "yieldcutoffT1": 85800000}]`

type fakeRFQ struct {
	mu      sync.Mutex
	key     string
	logins  int
	tries   int // sign-in attempts, refused ones too
	calls   map[string]int
	bodies  map[string][]map[string]any
	threads map[string]map[string]any
	amends  map[string]map[string]any
	debar   string // a PAN NSE has debarred
	expire  bool   // the next call finds the session lapsed
}

func rfqJSON(s string) map[string]any {
	var m map[string]any
	d := json.NewDecoder(strings.NewReader(rfqDay.Replace(s)))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		panic(err)
	}
	return m
}

func newFakeRFQ(t *testing.T) (*fakeRFQ, string) {
	t.Helper()
	f := &fakeRFQ{calls: map[string]int{}, bodies: map[string][]map[string]any{}, threads: map[string]map[string]any{}, amends: map[string]map[string]any{}}
	for _, s := range []string{rfqSampleTraded, rfqSampleProposed, rfqSampleProposed2, rfqSampleNegotiating} {
		m := rfqJSON(s)
		f.threads[fmt.Sprint(m["id"])] = m
	}
	for _, s := range []string{rfqSampleAmendMod, rfqSampleAmendCanc} {
		m := rfqJSON(s)
		f.amends[fmt.Sprint(m["id"])] = m
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	old := rfqHTTP
	rfqHTTP = srv.Client()
	t.Cleanup(func() { rfqHTTP = old })
	return f, srv.URL + "/rfq"
}

func (f *fakeRFQ) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	p, ok := strings.CutPrefix(r.URL.Path, "/rfq/rest/v1/")
	if !ok {
		w.WriteHeader(404)
		return
	}
	var body map[string]any
	if r.Method == http.MethodPost {
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(400)
			w.Write([]byte(`{"code":400,"messages":["Content-Type must be application/json"]}`))
			return
		}
		d := json.NewDecoder(r.Body)
		d.UseNumber()
		_ = d.Decode(&body)
	}
	say := func(v any) { json.NewEncoder(w).Encode(v) }
	no := func(field, msg string) {
		w.WriteHeader(400)
		say(map[string]any{"code": 400, "messages": []map[string]string{{"field": field, "msg": msg}}})
	}
	if p == "login" {
		f.tries++
		switch {
		case body["domain"] != rfqTestMember || body["login"] != rfqTestMember:
			w.Write([]byte(`{"domain":"PART1","login":"PART1","status":"F","reason":"Invalid Login Id/Password"}`))
		case body["password"] == "expired-pw":
			w.Write([]byte(`{"domain":"DFHI","login":"DFHI","status":"P","reason":"Change Password"}`))
		case body["password"] != rfqTestPass:
			w.Write([]byte(`{"domain":"DFHI","login":"DFHI","status":"F","reason":"Invalid Login Id/Password"}`))
		default:
			f.logins++
			f.key = fmt.Sprintf("122f2f48a91c94808a9%d", f.logins)
			w.Write([]byte(`{"status":"S","reason":null,"loginKey":"` + f.key + `","domain":"DFHI","login":"DFHI","firstName":"Amit","middleName":null,"lastName":null,"lastLogin":1525773311206,"serverTime":1461918333567,"broker":true,"brokerEnablementType":"R"}`))
		}
		return
	}
	if f.expire || f.key == "" || r.Header.Get("loginKey") != f.key {
		f.expire, f.key = false, ""
		w.WriteHeader(401)
		w.Write([]byte(`{"code":401,"messages":["Session expired"]}`))
		return
	}
	f.calls[p]++
	f.bodies[p] = append(f.bodies[p], body)
	str := func(k string) string { return fmt.Sprint(body[k]) }
	thread := func(id string) map[string]any {
		th := f.threads[id]
		if th == nil {
			w.WriteHeader(404)
			w.Write([]byte(`{"code":404,"messages":["Negotiation not found"]}`))
		}
		return th
	}
	switch p {
	case "market/timings":
		w.Write([]byte(rfqSampleTimings))
	case "negotiation/all":
		ids := make([]string, 0, len(f.threads))
		for id := range f.threads {
			if body["id"] == nil || str("id") == id {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		out := []map[string]any{}
		for _, id := range ids {
			out = append(out, f.threads[id])
		}
		say(out)
	case "deal/propose":
		for _, k := range []string{"ngRfqNumber", "ngId", "participantCode", "dealType", "clientCode", "price", "accruedInterest", "consideration", "calcMethod", "role"} {
			if body[k] == nil || str(k) == "" {
				no(k, "may not be null")
				return
			}
		}
		th := thread(str("ngId"))
		if th == nil {
			return
		}
		th["confirmStatus"], th["proposedBySide"], th["acceptedPrice"], th["acceptedAccruedInterest"], th["acceptedConsideration"] = "PC", nil, body["price"], body["accruedInterest"], body["consideration"]
		th["respDealType"], th["respClientCode"] = body["dealType"], body["clientCode"]
		if body["tradeSplits"] != nil {
			th["tradeSplits"] = body["tradeSplits"]
		}
		say(th)
	case "deal/acceptreject":
		th := thread(str("id"))
		if th == nil {
			return
		}
		if str("confirmStatus") == "PR" {
			th["confirmStatus"] = "CR"
		} else {
			if str("acceptedConsideration") != fmt.Sprint(th["acceptedConsideration"]) && strings.TrimSuffix(str("acceptedConsideration"), ".00") != strings.TrimSuffix(fmt.Sprint(th["acceptedConsideration"]), ".00") {
				w.WriteHeader(400)
				w.Write([]byte(`{"code":400,"messages":["Consideration is not same as proposed"]}`))
				return
			}
			th["confirmStatus"], th["status"], th["confirmedPriceQuoteTime"] = "CC", "C", "21:21:16"
		}
		say(th)
	case "deal/tradesplit":
		th := thread(str("id"))
		if th == nil {
			return
		}
		th["tradeSplits"] = body["tradeSplits"]
		say(th)
	case "deal/changeclient":
		th := thread(str("ngId"))
		if th == nil {
			return
		}
		th["respDealType"], th["respClientCode"], th["respClientRegType"] = body["dealType"], body["clientCode"], "R"
		say(th)
	case "dealamend/all":
		if body["filtFromDate"] == nil || body["filtToDate"] == nil {
			no("filtFromDate", "may not be null")
			return
		}
		ids := []string{}
		for id := range f.amends {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out := []map[string]any{}
		for _, id := range ids {
			out = append(out, f.amends[id])
		}
		say(out)
	case "dealamend/modifycancel":
		m := rfqJSON(rfqSampleAmendCanc)
		m["id"], m["ngId"], m["rfqNumber"], m["requestType"], m["cancReason"] = json.Number("127"), body["ngId"], body["rfqNumber"], body["requestType"], body["cancReason"]
		m["proposerAeCode"], m["counterAeCode"] = "DFHI", "INDBANK"
		f.amends["127"] = m
		say(m)
	case "dealamend/updatestatus":
		a := f.amends[str("id")]
		if a == nil {
			no("id", "Amendment not found")
			return
		}
		a["approvalStatus"], a["counterRemarks"], a["counterTime"] = body["approvalStatus"], body["counterRemarks"], "16:05:00"
		say(a)
	case "isins/all":
		if str("symbol") != "INE020B08BH6" {
			say([]any{})
			return
		}
		w.Write([]byte(`[{"symbol":"INE020B08BH6","description":"RURAL ELECTRIFICATION CORPORATION LIMITED SERIES 169 8.37 BD 07DC28 FVRS10LAC","issuer":"XYZ issuer","maturityDate":"07-Dec-2028","couponRate":8.37,"faceValue":1000000,"type":"BD","issueCategory":"CB","listed":"Y"}]`))
	case "rfqmaster/calcprice":
		w.Write([]byte(`{"calcMethod": "M", "putCallDate": "31-Dec-2027", "price": 115.3200, "accruedInterest": 1234.56}`))
	case "debar/all":
		out := []map[string]any{{"name": "Some Name", "pan": "PANNO3312X", "endDate": "17-May-2023"}, {"name": "Aniket", "pan": "ANKTO1162X", "endDate": "19-May-2023"}}
		if f.debar != "" {
			out = append(out, map[string]any{"name": "A client", "pan": f.debar, "endDate": "31-Dec-2026"})
		}
		say(out)
	default:
		w.WriteHeader(404)
		w.Write([]byte(`{"code":404,"messages":["No such API"]}`))
	}
}

func (f *fakeRFQ) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

func (f *fakeRFQ) n(p string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[p]
}

func (f *fakeRFQ) last(p string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	bs := f.bodies[p]
	if len(bs) == 0 {
		return nil
	}
	return bs[len(bs)-1]
}

func rfqSign(b []byte) string {
	m := hmac.New(sha256.New, []byte(rfqCBKey))
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil))
}

func rfqSave(t *testing.T, o *Ops, ctx context.Context, url, pw string) {
	t.Helper()
	if _, err := o.SaveConnection(ctx, T, "admin", connNSERFQ, map[string]string{"member": rfqTestMember, "login": rfqTestMember, "env": "UAT", "url": url, "read": "On"},
		map[string]string{"password": pw, "callback_token": rfqCBToken, "callback_key": rfqCBKey}); err != nil {
		t.Fatal(err)
	}
}

func rfqGet(t *testing.T, o *Ops, ctx context.Context, id string) RFQDealView {
	t.Helper()
	d, err := o.RFQDeal(ctx, T, "priya.nair", id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func rfqWorkOpen(o *Ops, ctx context.Context, id string) bool {
	w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, id)
	return err == nil && w.Status == "Open"
}

// rfqF is a field of a request the fake received, as text.
func rfqF(m map[string]any, k string) string { return fmt.Sprint(m[k]) }

func TestRFQDeals(t *testing.T) {
	o, ctx := setup(t)
	at := ipoAt(2026, 11, 18, 10, 0)
	o.Now = func() time.Time { return at }
	f, url := newFakeRFQ(t)
	if _, err := o.RFQSync(ctx, T, "priya.nair", ""); !errors.Is(err, ErrState) {
		t.Fatalf("read without a connection: %v", err)
	}
	rfqSave(t, o, ctx, url, rfqTestPass)
	if v, err := o.TestConnection(ctx, T, "admin", connNSERFQ); err != nil || v.Status != "Working" {
		t.Fatalf("test %+v %v", v, err)
	}

	// Before the market opens nothing is read; at 10:00 the tick reads
	// the deals (the negotiating thread is left out) and the amendments.
	o.rfqTick(ctx, T, ipoAt(2026, 11, 18, 8, 30))
	if f.n("negotiation/all") != 0 {
		t.Fatal("read before the market opened")
	}
	o.rfqTick(ctx, T, at)
	if f.n("negotiation/all") != 1 || f.n("dealamend/all") != 1 {
		t.Fatalf("tick calls %v", f.calls)
	}
	if st := o.rfqState(ctx, T, nil); st.Open != "09:00" || st.Close != "23:59" || len(st.Timings) != 3 || st.Timings[0].PriceCutT0 != "21:30" {
		t.Fatalf("timings %+v", st)
	}
	v, err := o.RFQDeals(ctx, T, "priya.nair", "2026-11-18")
	if err != nil || len(v.Deals) != 3 {
		t.Fatalf("deals %d %v", len(v.Deals), err)
	}
	x := rfqGet(t, o, ctx, "N22120900000054")
	if len(x.Sides) != 1 || x.Sides[0].Role != "R" || x.Sides[0].Side != "S" || x.Sides[0].DealType != "B" || x.Sides[0].ClientCode != "BARCLAYS" || x.Sides[0].UCC != "" ||
		x.Action != "propose" || x.Qty != 10 || x.Value.String() != "1" || x.Yield.String() != "6" || x.SettlementDate != "2026-11-18" || x.Counterparty != "INDBANK" ||
		x.TradeNo != "221209990000055" || !strings.HasPrefix(x.Description, "RURAL ELECTRIFICATION") || x.FaceValue.String() != "1000000" {
		t.Fatalf("traded deal %+v", x)
	}
	if y := rfqGet(t, o, ctx, "N22120900000053"); y.Action != "decide" || y.Consideration.String() != "11100012" || y.PutCallDate != "2028-12-07" {
		t.Fatalf("proposed deal %+v", y)
	}
	if !rfqWorkOpen(o, ctx, "W-rfq-confirm-N22120900000054-new") || !rfqWorkOpen(o, ctx, "W-rfq-confirm-N22120900000053-PC") {
		t.Fatal("no work to confirm the deals")
	}
	var mod, canc RFQAmend
	for _, a := range v.Amendments {
		switch a.ID {
		case "123":
			mod = a
		case "126":
			canc = a
		}
	}
	if mod.Ours != "proposed" || mod.ReasonText != "Settlement type" || mod.ModSettlementDate != "2023-06-26" || canc.Ours != "decide" || canc.Reason != "SHRT" || canc.Status != "PP" {
		t.Fatalf("amendments %+v %+v", mod, canc)
	}
	if !rfqWorkOpen(o, ctx, "W-rfq-amend-126") || rfqWorkOpen(o, ctx, "W-rfq-amend-123") {
		t.Fatal("amendment work items")
	}
	// Within the five minutes nothing is read again.
	o.rfqTick(ctx, T, at.Add(2*time.Minute))
	if f.n("negotiation/all") != 1 {
		t.Fatal("read again too soon")
	}

	// NSE's notification: the address token and the signature must match;
	// a good one has the deals read at the next tick.
	note := []byte(`{"negotiationList":[` + rfqDay.Replace(rfqSampleTraded) + `]}`)
	if _, err := o.RFQCallback(ctx, T, "wrong", rfqSign(note), note); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong token: %v", err)
	}
	if _, err := o.RFQCallback(ctx, T, rfqCBToken, rfqSign([]byte("other")), note); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong signature: %v", err)
	}
	if r, err := o.RFQCallback(ctx, T, rfqCBToken, strings.ToUpper(rfqSign(note)), note); err != nil || r.Negotiations != 1 {
		t.Fatalf("callback %+v %v", r, err)
	}
	o.rfqTick(ctx, T, at.Add(3*time.Minute))
	if f.n("negotiation/all") != 2 || o.rfqState(ctx, T, nil).Pending {
		t.Fatal("the notification did not bring a read")
	}

	// The maker names the client and the price; the consideration is
	// face value × price / 100 + accrued interest (the protocol's sample:
	// 1 crore at 100 with ₹1 accrued is 10000001).
	cs := ipoClients(t, o, ctx, 4)
	in := RFQProposalInput{Action: "propose", Allocs: []RFQAlloc{{UCC: cs[0].UCC}}, Price: "100", AccruedInterest: "1"}
	if _, err := o.RFQPropose(ctx, T, "priya.nair", x.ID, RFQProposalInput{Action: "propose", Allocs: []RFQAlloc{{UCC: "NOSUCH"}}, Price: "100", AccruedInterest: "1"}); err == nil || !strings.Contains(err.Error(), "not a client") {
		t.Fatalf("unknown client: %v", err)
	}
	if _, err := o.RFQPropose(ctx, T, "priya.nair", x.ID, RFQProposalInput{Action: "accept", Allocs: in.Allocs}); !errors.Is(err, ErrState) {
		t.Fatalf("accepting a deal nobody proposed: %v", err)
	}
	if _, err := o.RFQPropose(ctx, T, "priya.nair", x.ID, RFQProposalInput{Action: "propose", Allocs: in.Allocs, Price: "100"}); err == nil {
		t.Fatal("proposed without the accrued interest")
	}
	d, err := o.RFQPropose(ctx, T, "priya.nair", x.ID, in)
	if err != nil || d.Proposal == nil || d.Proposal.Consideration.String() != "10000001" || d.Proposal.CalcMethod != "O" || d.Proposal.Role != "R" {
		t.Fatalf("proposal %+v %v", d.Proposal, err)
	}
	if rfqWorkOpen(o, ctx, "W-rfq-confirm-N22120900000054-new") {
		t.Fatal("the maker's item is still open")
	}
	// Only another checker sends it; NSE's debarred list is checked first.
	if _, err := o.RFQSend(ctx, T, "priya.nair", x.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker sent: %v", err)
	}
	f.mu.Lock()
	f.debar = cs[0].PAN
	f.mu.Unlock()
	if _, err := o.RFQSend(ctx, T, "farhan.khan", x.ID); err == nil || !strings.Contains(err.Error(), "debarred by the exchange") || f.n("deal/propose") != 0 {
		t.Fatalf("debarred client sent: %v", err)
	}
	if d = rfqGet(t, o, ctx, x.ID).RFQDeal; d.Proposal == nil || !strings.Contains(d.Proposal.Error, "debarred") {
		t.Fatalf("refusal not kept %+v", d.Proposal)
	}
	f.mu.Lock()
	f.debar = ""
	f.mu.Unlock()
	if d, err = o.RFQSend(ctx, T, "farhan.khan", x.ID); err != nil {
		t.Fatal(err)
	}
	pb := f.last("deal/propose")
	if rfqF(pb, "ngRfqNumber") != "R22120900000020" || rfqF(pb, "ngId") != x.ID || rfqF(pb, "participantCode") != "DFHI" || rfqF(pb, "dealType") != "B" || rfqF(pb, "clientCode") != cs[0].UCC ||
		rfqF(pb, "price") != "100" || rfqF(pb, "accruedInterest") != "1.00" || rfqF(pb, "consideration") != "10000001.00" || rfqF(pb, "calcMethod") != "O" || rfqF(pb, "role") != "R" || pb["tradeSplits"] != nil {
		t.Fatalf("propose request %v", pb)
	}
	if xv := rfqGet(t, o, ctx, x.ID); xv.Proposal != nil || xv.Confirm != "PC" || xv.Action != "" || !strings.Contains(xv.Needs, "waiting for the counterparty") || xv.Sides[0].UCC != cs[0].UCC {
		t.Fatalf("after proposing %+v", xv)
	}
	// The counterparty accepts on NSE: the next read finds it confirmed,
	// to settle.
	f.mu.Lock()
	f.threads[x.ID]["status"], f.threads[x.ID]["confirmStatus"] = "C", "CC"
	f.mu.Unlock()
	o.rfqTick(ctx, T, at.Add(9*time.Minute))
	x = rfqGet(t, o, ctx, x.ID)
	if x.Status != "C" || x.Settle != "To settle" || x.Action != "settle" || x.Consideration.String() != "10000001" {
		t.Fatalf("confirmed %+v", x)
	}
	w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-rfq-settle-N22120900000054")
	if err != nil || w.Status != "Open" || w.UCC != cs[0].UCC || !strings.Contains(w.Detail, "does not post debt deals") || w.Date != "2026-11-18" {
		t.Fatalf("settle work %+v %v", w, err)
	}

	// The counterparty proposed the other deal: the maker splits it 6 and 4
	// between two clients; the checker's send gives NSE the splits, then
	// accepts the consideration as proposed.
	y := "N22120900000053"
	if _, err := o.RFQPropose(ctx, T, "priya.nair", y, RFQProposalInput{Action: "accept", Allocs: []RFQAlloc{{UCC: cs[1].UCC, Qty: 6}, {UCC: cs[2].UCC, Qty: 3}}}); err == nil || !strings.Contains(err.Error(), "add up to 9") {
		t.Fatalf("splits not adding up: %v", err)
	}
	if _, err := o.RFQPropose(ctx, T, "priya.nair", y, RFQProposalInput{Action: "accept", Allocs: []RFQAlloc{{UCC: cs[1].UCC, Qty: 6}, {UCC: cs[2].UCC, Qty: 4, Code: "RET0002"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RFQSend(ctx, T, "farhan.khan", y); err != nil {
		t.Fatal(err)
	}
	ts := f.last("deal/tradesplit")
	sp, _ := ts["tradeSplits"].([]any)
	if rfqF(ts, "id") != y || len(sp) != 2 {
		t.Fatalf("tradesplit %v", ts)
	}
	s0, s1 := sp[0].(map[string]any), sp[1].(map[string]any)
	if rfqF(s0, "val") != "0.6" || rfqF(s0, "qty") != "6" || rfqF(s0, "accInt") != "7.20" || rfqF(s0, "cons") != "6660007.20" || rfqF(s0, "couActPartCode") != cs[1].UCC ||
		rfqF(s1, "val") != "0.4" || rfqF(s1, "accInt") != "4.80" || rfqF(s1, "cons") != "4440004.80" || rfqF(s1, "couActPartCode") != "RET0002" || s0["propActPartCode"] != nil {
		t.Fatalf("splits %v %v", s0, s1)
	}
	ar := f.last("deal/acceptreject")
	if rfqF(ar, "confirmStatus") != "PC" || rfqF(ar, "acceptedPrice") != "111" || rfqF(ar, "acceptedAccruedInterest") != "12.00" || rfqF(ar, "acceptedConsideration") != "11100012.00" || rfqF(ar, "acceptedPutCallDate") != "07-Dec-2028" {
		t.Fatalf("acceptreject %v", ar)
	}
	yd := rfqGet(t, o, ctx, y)
	if yd.Status != "C" || len(yd.Splits) != 2 || yd.Splits[1].UCC != cs[2].UCC || yd.Splits[1].Consideration.String() != "4440004.8" {
		t.Fatalf("split deal %+v", yd)
	}
	// The third deal goes to one client: NSE is told the client first.
	z := "N22120900000048"
	if _, err := o.RFQPropose(ctx, T, "priya.nair", z, RFQProposalInput{Action: "accept", Allocs: []RFQAlloc{{UCC: cs[3].UCC}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RFQSend(ctx, T, "farhan.khan", z); err != nil {
		t.Fatal(err)
	}
	if cc := f.last("deal/changeclient"); rfqF(cc, "ngId") != z || rfqF(cc, "dealType") != "B" || rfqF(cc, "clientCode") != cs[3].UCC || rfqF(cc, "buySell") != "S" {
		t.Fatalf("changeclient %v", cc)
	}
	if zd := rfqGet(t, o, ctx, z); zd.Status != "C" || zd.Sides[0].UCC != cs[3].UCC || zd.Sides[0].RegType != "R" {
		t.Fatalf("one-client deal %+v", zd)
	}
	csv, err := o.RFQDealsCSV(ctx, T, "priya.nair", "2026-11-18")
	if err != nil || bytes.Count(csv, []byte("\n")) != 5 || !bytes.Contains(csv, []byte(cs[0].UCC)) || !bytes.Contains(csv, []byte("10000001.00")) || !bytes.Contains(csv, []byte("RET0002")) {
		t.Fatalf("csv %s %v", csv, err)
	}

	// The counterparty's cancellation of the split deal: a checker decides,
	// a rejection needs a reason; approved, nothing is left to settle.
	if _, err := o.RFQDecideAmend(ctx, T, "priya.nair", "126", true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker decided: %v", err)
	}
	if _, err := o.RFQDecideAmend(ctx, T, "farhan.khan", "126", false, ""); err == nil {
		t.Fatal("rejected without a reason")
	}
	if _, err := o.RFQDecideAmend(ctx, T, "farhan.khan", "123", true, ""); !errors.Is(err, ErrState) {
		t.Fatalf("decided the firm's own amendment: %v", err)
	}
	a, err := o.RFQDecideAmend(ctx, T, "farhan.khan", "126", true, "")
	if err != nil || a.Status != "EA" || a.DecidedBy != "farhan.khan" {
		t.Fatalf("decide %+v %v", a, err)
	}
	if us := f.last("dealamend/updatestatus"); rfqF(us, "id") != "126" || rfqF(us, "approvalStatus") != "EA" {
		t.Fatalf("updatestatus %v", us)
	}
	if yd := rfqGet(t, o, ctx, y); yd.Settle != "Cancelled" || yd.Action != "" {
		t.Fatalf("cancelled deal %+v", yd)
	}
	if rfqWorkOpen(o, ctx, "W-rfq-amend-126") || rfqWorkOpen(o, ctx, "W-rfq-settle-"+y) {
		t.Fatal("work left open after the cancellation")
	}

	// The firm's own cancellation of a confirmed deal: maker, then checker.
	if _, err := o.RFQProposeAmend(ctx, T, "priya.nair", x.ID, RFQAmendInput{Type: "M", Reason: "SHRT"}); err == nil {
		t.Fatal("a cancellation reason taken for a modification")
	}
	if _, err := o.RFQProposeAmend(ctx, T, "priya.nair", x.ID, RFQAmendInput{Type: "C", Reason: "FATFNGR", Remarks: "wrong client"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RFQSendAmend(ctx, T, "priya.nair", x.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker sent the amendment: %v", err)
	}
	a, err = o.RFQSendAmend(ctx, T, "farhan.khan", x.ID)
	if err != nil || a.ID != "127" || a.Ours != "proposed" || a.Type != "C" {
		t.Fatalf("amendment sent %+v %v", a, err)
	}
	if mc := f.last("dealamend/modifycancel"); rfqF(mc, "rfqNumber") != "R22120900000020" || rfqF(mc, "ngId") != x.ID || rfqF(mc, "requestType") != "C" || rfqF(mc, "cancReason") != "FATFNGR" || rfqF(mc, "proposerRemarks") != "wrong client" || mc["proposedBySide"] != nil {
		t.Fatalf("modifycancel %v", mc)
	}

	// Settled outside Ledgerline: the deal is marked and its item closed.
	if _, err := o.RFQMarkSettled(ctx, T, "priya.nair", z, ""); err == nil {
		t.Fatal("settled without a note")
	}
	if zd, err := o.RFQMarkSettled(ctx, T, "priya.nair", z, "pay-in UTR made-up-001"); err != nil || zd.Settle != "Settled" || rfqWorkOpen(o, ctx, "W-rfq-settle-"+z) {
		t.Fatalf("settled %+v %v", zd, err)
	}

	// NSE's price calculator, at the deal's yield.
	if c, err := o.RFQCalcPrice(ctx, T, "priya.nair", x.ID, 1); err != nil || c.Price.String() != "115.32" || c.AccruedInterest.String() != "1234.56" || c.PutCallDate != "2027-12-31" {
		t.Fatalf("calc %+v %v", c, err)
	}
	if cp := f.last("rfqmaster/calcprice"); rfqF(cp, "isin") != "INE020B08BH6" || rfqF(cp, "settlementDate") != "18-Nov-2026" || rfqF(cp, "yieldType") != "YTM" || rfqF(cp, "yield") != "6" || rfqF(cp, "aiDcc") != "1" {
		t.Fatalf("calcprice %v", cp)
	}

	// A lapsed session is signed in again, once.
	logins := f.loginCount()
	f.mu.Lock()
	f.expire = true
	f.mu.Unlock()
	if _, err := o.RFQSync(ctx, T, "priya.nair", ""); err != nil || f.loginCount() != logins+1 {
		t.Fatalf("re-login %v (%d logins)", err, f.loginCount())
	}
	if _, err := o.RFQSync(ctx, T, "vikram.shah", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance read: %v", err)
	}
}

func TestRFQLogin(t *testing.T) {
	o, ctx := setup(t)
	o.Now = func() time.Time { return ipoAt(2026, 11, 18, 10, 0) }
	f, url := newFakeRFQ(t)
	rfqSave(t, o, ctx, url, "wrong-pw")
	if v, _ := o.TestConnection(ctx, T, "admin", connNSERFQ); v.Status != "Problem" || !strings.Contains(v.Problem, "Invalid Login Id/Password") {
		t.Fatalf("bad password %+v", v)
	}
	rfqSave(t, o, ctx, url, "expired-pw")
	if _, err := o.RFQSync(ctx, T, "priya.nair", ""); err == nil || !strings.Contains(err.Error(), "password has expired") {
		t.Fatalf("expired password: %v", err)
	}
	// NSE refused the sign-in: the connection shows it, and scheduled
	// reads stop until it is saved again.
	if v, _ := o.connView(ctx, T, "admin", connNSERFQ); v.Status != "Problem" || !strings.Contains(v.Problem, "reading stopped") {
		t.Fatalf("connection after a refused sign-in %+v", v)
	}
	f.mu.Lock()
	tries := f.tries
	f.mu.Unlock()
	o.rfqTick(ctx, T, ipoAt(2026, 11, 18, 10, 30))
	f.mu.Lock()
	again := f.tries
	f.mu.Unlock()
	if again != tries {
		t.Fatal("the tick tried the refused password again")
	}
	if f.loginCount() != 0 {
		t.Fatalf("%d logins", f.loginCount())
	}
	// Field errors come back in NSE's words.
	rfqSave(t, o, ctx, url, rfqTestPass)
	c, _ := o.rfqConnection(ctx, T)
	err := rfqCall(ctx, c.cfg, http.MethodPost, "/rest/v1/deal/propose", map[string]any{"ngId": "N1"}, nil)
	var re *rfqError
	if !errors.As(err, &re) || re.Status != 400 || !strings.Contains(err.Error(), "ngRfqNumber: may not be null") {
		t.Fatalf("field error %v", err)
	}
	// A firm without the connection refuses notifications.
	if _, err := o.RFQCallback(ctx, "nobody", rfqCBToken, "", []byte(`{}`)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("callback without a connection: %v", err)
	}
}
