package ops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// fakeCBRICS stands in for NSE's CBRICS, speaking protocol 1.16 with its
// sample replies: login with the loginKey header (and the password expiry
// flow), the Order, Unregistered Participants and Settlement APIs, and
// the methods each section's table names where its heading says another
// (it answers 405 to the heading's method there).
type fakeCBRICS struct {
	mu       sync.Mutex
	password string
	expired  bool
	keys     map[string]bool
	logins   int
	drop     bool // the next call finds its session gone (401)
	orders   []map[string]any
	unregs   map[string]map[string]any // by id
	settles  []map[string]any
	calls    []string // "METHOD path"
	bodies   map[string][]map[string]any
	refuse   string // an ISIN CBRICS refuses
}

func newCBRICSFake(t *testing.T) (*httptest.Server, *fakeCBRICS) {
	t.Helper()
	f := &fakeCBRICS{password: "a#12G34h4r5t", keys: map[string]bool{}, unregs: map[string]map[string]any{}, bodies: map[string][]map[string]any{}, refuse: "INE000000017"}
	srv := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return srv, f
}

func (f *fakeCBRICS) reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeCBRICS) orderNo(id int) string { return fmt.Sprintf("2611180000%05d", id) }

func (f *fakeCBRICS) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := strings.CutPrefix(r.URL.Path, "/bondsnew/rest/v1/")
	if !ok || r.Header.Get("Content-Type") != "application/json" {
		f.reply(w, 404, map[string]any{"code": 404, "messages": []string{"Resource does not exist"}})
		return
	}
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.UseNumber()
		_ = dec.Decode(&body)
	}
	f.calls = append(f.calls, r.Method+" "+p)
	f.bodies[p] = append(f.bodies[p], body)
	if p == "login" {
		f.logins++
		if body["domain"] != "PART1" || body["login"] != "PART1" || body["password"] != f.password {
			f.reply(w, 200, map[string]any{"status": "F", "reason": "Invalid Login Id/Password"})
			return
		}
		if f.expired {
			np, _ := body["newPassword1"].(string)
			if np == "" {
				f.reply(w, 200, map[string]any{"status": "P", "reason": "Change Password"})
				return
			}
			f.password, f.expired = np, false
		}
		k := fmt.Sprintf("122f2f48a91c9480%04d", f.logins)
		f.keys = map[string]bool{k: true} // a new login ends the old session
		f.reply(w, 200, map[string]any{"status": "S", "reason": nil, "loginKey": k, "ownerCode": "PART1", "login": "PART1", "firstName": "Amit", "lastLogin": 1525773311206, "serverTime": 1461918333567})
		return
	}
	if f.drop {
		f.drop, f.keys = false, map[string]bool{}
	}
	if !f.keys[r.Header.Get("loginKey")] {
		f.reply(w, 401, map[string]any{"code": 401, "messages": []string{"Session expired"}})
		return
	}
	method := map[string]string{"order/status": "PUT", "participant/find": "PUT", "order/buyinstructions": "POST", "logout": "GET", "order/sellreportings": r.Method}[p]
	if strings.HasPrefix(p, "unreg/") && strings.Trim(strings.TrimPrefix(p, "unreg/"), "0123456789") == "" {
		method = "POST" // GET /rest/v1/unreg/{id}'s table says POST
	}
	if method == "" {
		method = "POST"
	}
	if r.Method != method {
		f.reply(w, 405, map[string]any{"code": 405, "messages": []string{"Method not allowed"}})
		return
	}
	switch {
	case p == "logout":
		f.keys = map[string]bool{}
		f.reply(w, 200, map[string]string{"status": "C"})
	case p == "order" || p == "order/update":
		for _, k := range []string{"dealType", "symbol", "buyParticipantLoginId", "sellParticipantLoginId", "price", "value", "accrInt", "settle", "sendFor", "yieldType", "yield", "tradeDate", "tradeTime"} {
			if body[k] == nil || body[k] == "" {
				f.reply(w, 400, map[string]any{"code": 400, "messages": []map[string]string{{"field": k, "msg": "may not be empty"}}})
				return
			}
		}
		if _, err := time.Parse(cbricsDate, body["tradeDate"].(string)); err != nil {
			f.reply(w, 400, map[string]any{"code": 400, "messages": []map[string]string{{"field": "tradeDate", "msg": "Date format as dd-MM-yyyy"}}})
			return
		}
		if body["symbol"] == f.refuse {
			f.reply(w, 400, map[string]any{"code": 400, "messages": []string{"Yield is not within system level bands. Violation reason is mandatory"}})
			return
		}
		if p == "order/update" {
			found := false
			for _, o := range f.orders {
				if fmt.Sprint(o["id"]) == fmt.Sprint(body["id"]) && o["status"] == json.Number("1") {
					o["status"], found = json.Number("8"), true
				}
			}
			if !found {
				f.reply(w, 400, map[string]any{"code": 400, "messages": []string{"Order cannot be modified"}})
				return
			}
		}
		o := map[string]any{}
		for k, v := range body {
			o[k] = v
		}
		id := 343 + len(f.orders)
		value, _ := money.ParseDec(fmt.Sprint(body["value"]))
		price, _ := money.ParseDec(fmt.Sprint(body["price"]))
		accr, _ := money.ParseDec(fmt.Sprint(body["accrInt"]))
		q, _ := value.Quo(money.DecInt(100000))
		o["id"], o["orderNumber"], o["status"], o["quantity"] = json.Number(fmt.Sprint(id)), f.orderNo(id), json.Number("1"), json.Number(q.Fixed(0))
		o["consideration"] = json.Number(value.Mul(price).Mul(money.MustDec("0.01")).Add(accr).Fixed(2))
		o["message"] = "Sell Deal Reporting saved successfully. Buyer confirmation is optional."
		o["entry"] = "18-11-2026"
		f.orders = append(f.orders, o)
		f.reply(w, 200, o)
	case p == "order/sellreportings":
		day := "18-11-2026"
		if r.Method == "POST" {
			day = fmt.Sprint(body["filtEntryDate"])
		}
		out := []map[string]any{}
		for _, o := range f.orders {
			if o["standingInstFlag"] != "Y" && o["entry"] == day {
				out = append(out, o)
			}
		}
		f.reply(w, 200, out)
	case p == "order/buyinstructions":
		out := []map[string]any{}
		for _, o := range f.orders {
			if o["standingInstFlag"] == "Y" {
				c := map[string]any{}
				for k, v := range o {
					c[k] = v
				}
				c["standingInstFlag"] = "N" // as the protocol's sample shows it
				out = append(out, c)
			}
		}
		f.reply(w, 200, out)
	case p == "order/status":
		for _, o := range f.orders {
			if fmt.Sprint(o["id"]) == fmt.Sprint(body["id"]) {
				o["status"] = body["status"]
				f.reply(w, 200, map[string]any{"id": body["id"], "status": body["status"], "message": "Sell Order Reporting Approved"})
				return
			}
		}
		f.reply(w, 404, map[string]any{"code": 404, "messages": []string{"Resource does not exist"}})
	case p == "marketwatch/activeissues":
		f.reply(w, 200, json.RawMessage(`[{"symbol": "INE752E07FF5","totTradeValue": 3013800.00,"description": "POWER GRID CORPORATION OF INDIA LIMITED 9.33 BD 15DC23","type": "CC","issuer": "POWER GRID CORPORATION OF INDIA LIMITED",
			"avgYield": 8.1519,"avgTradePrice": 101.0034,"couponRate": 9.33,"lastTradePrice": 101,"maturityDate": "15-12-2033","totNoOfTrades": 25,"issueDate": "15-12-2008","lastTradeValue": 125000,"lastTradeTime": "18-11-2026 10:11:10","creditRating": "CRISIL AAA"}]`))
	case p == "participant/find":
		f.reply(w, 200, json.RawMessage(`[{"type": 2,"firstName": "Participant One","loginId": "BUYER1","panNo": "PANNO0001A","actualStatus": 1},{"type": 2,"firstName": "Participant Three","loginId": "PART3","panNo": "PANNO0002A","custodian": "CUST1","actualStatus": 1}]`))
	case p == "unreg" || p == "unreg/update":
		for _, k := range []string{"firstName", "contactPerson", "mobileList", "emailList", "telephone", "stateCode", "regAddress", "bankAccountList", "dpAccountList", "dobDoi"} {
			if body[k] == nil {
				f.reply(w, 400, map[string]any{"code": 400, "messages": []map[string]string{{"field": k, "msg": "may not be null"}}})
				return
			}
		}
		id := fmt.Sprint(body["id"])
		if p == "unreg" {
			id = fmt.Sprint(65092 + len(f.unregs))
		} else if f.unregs[id] == nil {
			f.reply(w, 400, map[string]any{"code": 400, "messages": []string{"Only returned requests can be updated"}})
			return
		}
		u := map[string]any{}
		for k, v := range body {
			u[k] = v
		}
		if p == "unreg/update" {
			u["loginId"] = f.unregs[id]["loginId"]
		}
		u["id"], u["actualStatus"], u["workflowStatus"], u["panVerStatus"] = json.Number(id), 4, json.Number("0"), json.Number("0")
		for _, l := range []string{"bankAccountList", "dpAccountList"} {
			for _, a := range u[l].([]any) {
				a.(map[string]any)["workflowStatus"], a.(map[string]any)["status"] = json.Number("0"), "A"
			}
		}
		f.unregs[id] = u
		f.reply(w, 200, u)
	case p == "unreg/all":
		out := []map[string]any{}
		for _, u := range f.unregs {
			if fmt.Sprint(u["workflowStatus"]) == fmt.Sprint(body["workflowStatus"]) {
				c := map[string]any{}
				for k, v := range u {
					if k != "bankAccountList" && k != "dpAccountList" {
						c[k] = v
					}
				}
				out = append(out, c)
			}
		}
		f.reply(w, 200, out)
	case strings.HasPrefix(p, "unreg/") && f.unregs[strings.TrimPrefix(p, "unreg/")] != nil:
		f.reply(w, 200, f.unregs[strings.TrimPrefix(p, "unreg/")])
	case p == "unreg/final/updatecontact":
		u := f.unregs[fmt.Sprint(body["id"])]
		for k, v := range body {
			if k != "id" {
				u[k] = v
			}
		}
		f.reply(w, 200, u)
	case p == "unreg/final/delete":
		delete(f.unregs, fmt.Sprint(body["id"]))
		f.reply(w, 200, map[string]any{"id": body["id"], "message": "Unregistered Participant deleted successfully."})
	case p == "unreg/bankacc" || p == "unreg/dpacc":
		a := map[string]any{}
		for k, v := range body {
			a[k] = v
		}
		a["workflowStatus"], a["status"] = json.Number("0"), "A"
		l := map[string]string{"unreg/bankacc": "bankAccountList", "unreg/dpacc": "dpAccountList"}[p]
		for _, u := range f.unregs {
			if u["loginId"] == body["participantCode"] {
				u[l] = append(u[l].([]any), a)
			}
		}
		f.reply(w, 200, a)
	case p == "unreg/bankacc/all" || p == "unreg/dpacc/all":
		l := map[string]string{"unreg/bankacc/all": "bankAccountList", "unreg/dpacc/all": "dpAccountList"}[p]
		out := []any{}
		for _, u := range f.unregs {
			if u["loginId"] == body["participantCode"] {
				out = append(out, u[l].([]any)...)
			}
		}
		f.reply(w, 200, out)
	case strings.HasPrefix(p, "unreg/") && strings.Contains(p, "/final/"):
		a := map[string]any{"isDefault": "Y", "workflowStatus": 1}
		for k, v := range body {
			a[k] = v
		}
		f.reply(w, 200, a)
	case p == "settle/order/all":
		from, err1 := time.Parse(cbricsDate, fmt.Sprint(body["filtFromModSettleDate"]))
		to, err2 := time.Parse(cbricsDate, fmt.Sprint(body["filtToModSettleDate"]))
		if err1 != nil || err2 != nil || to.Sub(from) > 30*24*time.Hour {
			f.reply(w, 400, map[string]any{"code": 400, "messages": []string{"The date range cannot be more than 30 days"}})
			return
		}
		f.reply(w, 200, f.settles)
	case p == "settle/order/updatebank" || p == "settle/order/updatedp":
		for _, s := range f.settles {
			if fmt.Sprint(s["id"]) == fmt.Sprint(body["id"]) {
				for k, v := range body {
					if k != "id" {
						s[k] = v
					}
				}
				f.reply(w, 200, s)
				return
			}
		}
		f.reply(w, 400, map[string]any{"code": 400, "messages": []string{"Settle order not found"}})
	default:
		f.reply(w, 404, map[string]any{"code": 404, "messages": []string{"Resource does not exist"}})
	}
}

func (f *fakeCBRICS) do(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *fakeCBRICS) called(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

func (f *fakeCBRICS) loginCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins
}

func (f *fakeCBRICS) last(path string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	bs := f.bodies[path]
	if len(bs) == 0 {
		return nil
	}
	return bs[len(bs)-1]
}

// cbricsSetup connects a firm to a fake CBRICS.
func cbricsSetup(t *testing.T) (*Ops, context.Context, *fakeCBRICS, string) {
	t.Helper()
	o, ctx := setup(t)
	now := ipoAt(2026, 11, 18, 11, 0)
	o.Now = func() time.Time { return now }
	srv, f := newCBRICSFake(t)
	o.CBRICS = srv.Client()
	vals := map[string]string{"member": "PART1", "login": "PART1", "env": "Testing (UAT)", "url": srv.URL + "/bondsnew", "auto": "On"}
	if _, err := o.SaveConnection(ctx, T, "admin", connCBRICS, vals, map[string]string{"password": "a#12G34h4r5t", "next_password": "short"}); err == nil || !strings.Contains(err.Error(), "12 to 15") {
		t.Fatalf("a next password against the policy was taken: %v", err)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connCBRICS, vals, map[string]string{"password": "a#12G34h4r5t", "callback_key": "made-up-callback-key"}); err != nil {
		t.Fatal(err)
	}
	return o, ctx, f, srv.URL + "/bondsnew"
}

func cbricsClients(t *testing.T, o *Ops, ctx context.Context, n int) []Client {
	t.Helper()
	cs := ipoClients(t, o, ctx, n)
	for _, c := range cs {
		if _, ok := cbricsDPOf(c.Demat.ID); !ok {
			t.Fatalf("client %s demat %s", c.UCC, c.Demat.ID)
		}
	}
	return cs
}

// cbricsKeep keeps a client as an unregistered participant, approved by NSE.
func cbricsKeep(t *testing.T, o *Ops, ctx context.Context, f *fakeCBRICS, ucc string) CBRICSParty {
	t.Helper()
	p, err := o.CBRICSPartyFromClient(ctx, T, "priya.nair", ucc)
	if err != nil {
		t.Fatal(err)
	}
	p.StateCode, p.RegAddr, p.DOB = firstOf(p.StateCode, "Maharashtra"), firstOf(p.RegAddr, "1 Made-up Road, Mumbai 400001"), firstOf(p.DOB, "1980-01-01")
	if p, err = o.SaveCBRICSParty(ctx, T, "priya.nair", p); err != nil {
		t.Fatal(err)
	}
	if p, err = o.DecideCBRICSParty(ctx, T, "farhan.khan", p.Code, true, ""); err != nil || p.State != "Sent" || p.CBRICSID == "" {
		t.Fatalf("participant sent %+v %v", p, err)
	}
	f.do(func() { f.unregs[p.CBRICSID]["workflowStatus"] = json.Number("1") })
	return p
}

func TestCBRICSProtocol(t *testing.T) {
	o, ctx, f, _ := cbricsSetup(t)
	// The test logs in and out.
	if v, err := o.TestConnection(ctx, T, "admin", connCBRICS); err != nil || v.Status != "Working" || !strings.Contains(v.Problem, "testing") {
		t.Fatalf("test %+v %v", v, err)
	}
	if f.called("GET logout") != 1 {
		t.Fatal("no logout")
	}
	c, err := o.cbricsCall(ctx, T)
	if err != nil {
		t.Fatal(err)
	}
	// participant/find: POST is refused with 405, PUT (the table's) works.
	fs, err := c.find("BUYER1", "")
	if err != nil || len(fs) != 2 || fs[0].LoginID != "BUYER1" || numStr(fs[0].ActualStatus) != "1" || f.called("PUT participant/find") != 1 || f.called("POST participant/find") != 1 {
		t.Fatalf("find %+v %v", fs, err)
	}
	if b := f.last("participant/find"); b["loginId"] != "BUYER1" || fmt.Sprint(b["type"]) != "2" {
		t.Fatalf("find body %v", b)
	}
	// A lapsed session: one fresh login, the call repeated.
	logins := f.loginCount()
	f.do(func() { f.drop = true })
	is, err := c.activeIssues("INE752E07FF5")
	if err != nil || len(is) != 1 || numStr(is[0].AvgYield) != "8.1519" || cbricsDay(is[0].MaturityDate) != "2033-12-15" || f.loginCount() != logins+1 {
		t.Fatalf("after 401 %+v %v (logins %d → %d)", is, err, logins, f.loginCount())
	}
	// A refusal comes back in CBRICS's words, field errors with the field.
	_, err = c.order(CBRICSOrder{Symbol: "INE752E07FF5"}, false)
	var ce *CBRICSError
	if !errors.As(err, &ce) || ce.Code != 400 || !strings.Contains(ce.Messages[0], "dealType: may not be empty") {
		t.Fatalf("field error %v", err)
	}
	// An expired password with no next password: a work item for the admin.
	f.do(func() { f.expired, f.keys = true, map[string]bool{} })
	if _, err := c.activeIssues(""); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired: %v", err)
	}
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-cbrics-password-2026-11-18"); err != nil || w.Status != "Open" || w.Role != RoleAdmin {
		t.Fatalf("password work item %+v %v", w, err)
	}
	// With the next password, Ledgerline changes it at login and keeps it.
	if _, err := o.SaveConnection(ctx, T, "admin", connCBRICS, map[string]string{"member": "PART1", "login": "PART1", "env": "Testing (UAT)", "url": c.x.cfg.Base, "auto": "On"},
		map[string]string{"next_password": "3#Kj1i50u7t1X"}); err != nil {
		t.Fatal(err)
	}
	if c, err = o.cbricsCall(ctx, T); err != nil {
		t.Fatal(err)
	}
	if _, err := c.activeIssues(""); err != nil {
		t.Fatalf("after the password change: %v", err)
	}
	cn, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connCBRICS)
	var pw string
	f.do(func() { pw = f.password })
	if pw != "3#Kj1i50u7t1X" || cn.Secrets["password"] != "3#Kj1i50u7t1X" || cn.Secrets["next_password"] != "" {
		t.Fatalf("password %q, connection %v", pw, cn.Secrets)
	}
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-cbrics-password-2026-11-18"); w.Status != "Done" {
		t.Fatalf("password work item %+v", w)
	}
	if msg := cbricsPasswordProblem("abcdefghijkL1", ""); !strings.Contains(msg, "one of") {
		t.Fatalf("no special character: %q", msg)
	}
	if cbricsStateCode("Maharashtra") != "27" || cbricsStateCode("orissa") != "21" || cbricsStateCode("7") != "07" || cbricsStateCode("Narnia") != "" {
		t.Fatal("state codes")
	}
}

func TestCBRICSDeals(t *testing.T) {
	o, ctx, f, base := cbricsSetup(t)
	cs := cbricsClients(t, o, ctx, 2)
	a, b := cs[0], cs[1]
	// The client's participant, filled in from the client's record with its
	// own accounts, the primary as default; a maker cannot send it.
	p, err := o.CBRICSPartyFromClient(ctx, T, "priya.nair", a.UCC)
	if err != nil || p.Code != a.UCC || p.Name != a.Name || p.PAN != a.PAN || len(p.Banks) == 0 || !p.Banks[0].Default || p.Banks[0].Account != a.Bank.Account || len(p.DPs) == 0 || !p.DPs[0].Default {
		t.Fatalf("from the client %+v %v", p, err)
	}
	p.StateCode, p.RegAddr, p.DOB = "27", "1 Made-up Road, Mumbai 400001", firstOf(p.DOB, "1980-01-01")
	p.Banks = append(p.Banks, CBRICSPartyBank{Bank: "Someone Else's Bank", IFSC: "HDFC0000001", Account: "999999999"})
	if p, err = o.SaveCBRICSParty(ctx, T, "priya.nair", p); err != nil || len(p.Banks) != len(activeBanks(a)) {
		t.Fatalf("only the client's accounts are kept %+v %v", p.Banks, err)
	}
	if _, err := o.DecideCBRICSParty(ctx, T, "priya.nair", a.UCC, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the maker sent it: %v", err)
	}
	if p, err = o.DecideCBRICSParty(ctx, T, "farhan.khan", a.UCC, true, ""); err != nil || p.State != "Sent" || p.CBRICSID != "65092" {
		t.Fatalf("sent %+v %v", p, err)
	}
	ub := f.last("unreg")
	if ub["loginId"] != a.UCC || ub["stateCode"] != "27" || ub["dobDoi"] == nil || len(ub["bankAccountList"].([]any)) != len(p.Banks) || ub["bankAccountList"].([]any)[0].(map[string]any)["isDefault"] != "Y" {
		t.Fatalf("unreg body %v", ub)
	}
	if dp := ub["dpAccountList"].([]any)[0].(map[string]any); dp["dpType"] != "CDSL" || len(fmt.Sprint(dp["benId"])) != 16 || dp["dpId"] != nil {
		t.Fatalf("CDSL account %v", dp)
	}
	// The counterparty: a registered participant found at CBRICS.
	fs, err := o.CBRICSFind(ctx, T, "priya.nair", "buyer1", "")
	if err != nil || len(fs) == 0 {
		t.Fatal(err)
	}
	if _, err := o.SaveCBRICSParty(ctx, T, "priya.nair", CBRICSParty{Code: fs[0].LoginID, Kind: "Registered", Name: fs[0].Name, PAN: fs[0].PAN}); err != nil {
		t.Fatal(err)
	}
	// The deal, ten minutes old: 10 bonds of ₹1 lakh at 100.512.
	in := CBRICSDealInput{UCC: a.UCC, Side: "Sell", ISIN: "INE752E07FF5", Counterparty: "BUYER1", Quantity: 10, FaceValue: money.MustDec("100000"), Price: money.MustDec("100.512"),
		AccrInt: money.MustDec("1234.5"), YieldType: "M", Yield: money.MustDec("10.4512"), Settle: 1, TradeDate: "2026-11-18", TradeTime: "10:50"}
	d, err := o.SaveCBRICSDeal(ctx, T, "priya.nair", in)
	if err != nil || d.Status != CBDDraft || d.Value.String() != "1000000" || d.Estimate.Fixed(2) != "1006354.50" || len(d.Warnings) == 0 {
		t.Fatalf("deal %+v %v", d, err)
	}
	// Not approved at CBRICS yet: the checker cannot report it.
	if _, err := o.DecideCBRICSDeal(ctx, T, "farhan.khan", d.ID, true, ""); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("reported for a participant not approved: %v", err)
	}
	f.do(func() { f.unregs["65092"]["workflowStatus"] = json.Number("1") })
	if res, err := o.CBRICSSync(ctx, T, "anita.rao"); err != nil || res.Parties != 1 {
		t.Fatalf("read %+v %v", res, err)
	}
	if p, _, _ := docs.Get[CBRICSParty](ctx, o.Docs, T, cCBRICSParty, a.UCC); p.State != "Approved" {
		t.Fatalf("participant %+v", p)
	}
	// Twenty minutes after the deal, past the 15-minute limit: a work item.
	later := ipoAt(2026, 11, 18, 11, 10)
	o.Now = func() time.Time { return later }
	o.cbricsTick(ctx, T, later)
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-cbrics-late-"+d.ID); err != nil || w.Status != "Open" || !strings.Contains(w.Title, "not reported") {
		t.Fatalf("late work item %+v %v", w, err)
	}
	if _, err := o.DecideCBRICSDeal(ctx, T, "priya.nair", d.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker reported own deal: %v", err)
	}
	if d, err = o.DecideCBRICSDeal(ctx, T, "farhan.khan", d.ID, true, ""); err != nil || d.Status != CBDReported || d.OrderNo == "" || d.LateMinutes != 20 || d.Consideration.Fixed(2) != "1006354.50" {
		t.Fatalf("reported %+v %v", d, err)
	}
	ob := f.last("order")
	if ob["standingInstFlag"] != "N" || ob["sellParticipantLoginId"] != a.UCC || ob["buyParticipantLoginId"] != "BUYER1" || ob["sellerRefNo"] != d.ID || ob["dealType"] != "B" || ob["sendFor"] != "M" ||
		ob["tradeDate"] != "18-11-2026" || ob["tradeTime"] != "10:50" || fmt.Sprint(ob["price"]) != "100.5120" || fmt.Sprint(ob["value"]) != "1000000.00" || fmt.Sprint(ob["settle"]) != "1" {
		t.Fatalf("order body %v", ob)
	}
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-cbrics-late-"+d.ID); w.Status != "Done" || !strings.Contains(w.Note, "20 minutes") {
		t.Fatalf("late item after reporting %+v", w)
	}
	// The buyer confirms; CBRICS makes the order for settlement, paying out
	// to an account other than the client's primary.
	f.do(func() {
		f.orders[0]["status"] = json.Number("7")
		f.settles = append(f.settles, map[string]any{"id": 1, "orderNumber": d.OrderNo, "symbol": "INE752E07FF5", "buyParticipantLoginId": "BUYER1", "sellParticipantLoginId": a.UCC,
			"settleStatus": 0, "source": 1, "modAccrInt": 1234.50, "modQuantity": 10, "value": 1000000.00, "price": 100.512, "yield": 10.4512, "yieldType": "M", "modConsideration": 1006354.50,
			"stampDutyAmount": 1.00, "stampDutyBearer": "BUYER1", "buyerFundPayinObligation": 1006355.50, "sellerFundPayoutObligation": 1006354.50, "ifscCode": "DBSS0IN0811", "accountNo": "811LIABICSTSUINR",
			"settlementNo": "2611181", "modSettleDate": "19-11-2026", "dpId": "IN500089", "benId": "00000000", "fundPayinRefId": "b4b8a676-cdda-48c8-89f2-776b354364d8"})
	})
	res, err := o.CBRICSSync(ctx, T, "priya.nair")
	if err != nil || res.Settlements != 1 || len(res.Problems) != 0 {
		t.Fatalf("read %+v %v", res, err)
	}
	if f.called("GET order/buyinstructions") == 0 || f.called("POST order/buyinstructions") != f.called("GET order/buyinstructions") || f.called("GET order/sellreportings") == 0 {
		t.Fatal("buy instructions not read with the table's method")
	}
	d, _, _ = docs.Get[CBRICSDeal](ctx, o.Docs, T, cCBRICSDeal, d.ID)
	if d.Status != CBDSettling || d.Settlement == nil || d.Settlement.SettlementNo != "2611181" || d.Settlement.Date != "2026-11-19" || d.Settlement.Consideration.Fixed(2) != "1006354.50" || d.Pending != "Account" ||
		d.Account.IFSC != strings.ToUpper(a.Bank.IFSC) || d.Account.Account != a.Bank.Account {
		t.Fatalf("settling %+v %+v %+v", d, d.Settlement, d.Account)
	}
	mw, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-cbrics-money-"+d.ID)
	if err != nil || mw.Status != "Open" || !strings.Contains(mw.Title, "delivers 10 INE752E07FF5") || !strings.Contains(mw.Detail, "posts nothing to the client's ledger") {
		t.Fatalf("money work item %+v %v", mw, err)
	}
	// The checker sends the client's primary account as the payout account.
	if d, err = o.DecideCBRICSDeal(ctx, T, "farhan.khan", d.ID, true, ""); err != nil || d.Settlement.IFSC != strings.ToUpper(a.Bank.IFSC) {
		t.Fatalf("account sent %+v %v", d.Settlement, err)
	}
	if bb := f.last("settle/order/updatebank"); fmt.Sprint(bb["id"]) != "1" || bb["ifscCode"] != strings.ToUpper(a.Bank.IFSC) || bb["accountNo"] != a.Bank.Account {
		t.Fatalf("updatebank %v", bb)
	}
	// Paid out: the deal is settled with its consideration and date, and the
	// money work item closes. No ledger entries.
	f.do(func() { f.settles[0]["settleStatus"] = 4 })
	if _, err := o.CBRICSSync(ctx, T, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	d, _, _ = docs.Get[CBRICSDeal](ctx, o.Docs, T, cCBRICSDeal, d.ID)
	if d.Status != CBDSettled || d.Settlement.SettledAt == "" || d.Settlement.StatusText != "Pay-out done" {
		t.Fatalf("settled %+v", d.Settlement)
	}
	if mw, _, _ = docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-cbrics-money-"+d.ID); mw.Status != "Done" {
		t.Fatalf("money work item %+v", mw)
	}
	// The screen masks the client's account.
	v, err := o.CBRICSStatus(ctx, T, "vikram.shah")
	if err != nil || len(v.Deals) != 1 || v.Deals[0].Settlement.Account == a.Bank.Account || v.Late != 1 || v.Callback != "/hooks/demo/nse-cbrics" {
		t.Fatalf("view %+v %v", v, err)
	}

	// A purchase from an unregistered counterparty the firm keeps (not a
	// client): a standing instruction; the seller's report does not match
	// it, so the client confirms it.
	cbricsKeep(t, o, ctx, f, b.UCC)
	cp := CBRICSParty{Code: "CPTY1", Name: "Made-up Traders", PAN: "AAAPZ1234C", Contact: "Mr Test", Mobiles: []string{"9876543210"}, Emails: []string{"ops@example.test"}, Telephone: "02200000000",
		StateCode: "27", RegAddr: "2 Made-up Lane, Mumbai", DOB: "2001-04-01", Banks: []CBRICSPartyBank{{Bank: "ABC Bank", IFSC: "ABCD0111111", Account: "123456789", Default: true}},
		DPs: []CBRICSPartyDP{{Depository: "NSDL", DPID: "IN234234", BenID: "29347239", Default: true}}}
	if cp, err = o.SaveCBRICSParty(ctx, T, "priya.nair", cp); err != nil {
		t.Fatal(err)
	}
	if cp, err = o.DecideCBRICSParty(ctx, T, "farhan.khan", "CPTY1", true, ""); err != nil {
		t.Fatal(err)
	}
	f.do(func() { f.unregs[cp.CBRICSID]["workflowStatus"] = json.Number("1") })
	if _, err := o.CBRICSSync(ctx, T, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	bin := CBRICSDealInput{UCC: b.UCC, Side: "Buy", ISIN: "INE020B08BH6", Counterparty: "CPTY1", Quantity: 5, FaceValue: money.MustDec("100000"), Price: money.MustDec("101"),
		YieldType: "M", Yield: money.MustDec("7"), Settle: 1, TradeDate: "2026-11-18", TradeTime: "11:05"}
	bd, err := o.SaveCBRICSDeal(ctx, T, "priya.nair", bin)
	if err != nil {
		t.Fatal(err)
	}
	if bd, err = o.DecideCBRICSDeal(ctx, T, "farhan.khan", bd.ID, true, ""); err != nil || bd.Status != CBDInstructed || bd.LateMinutes != 0 {
		t.Fatalf("instruction %+v %v", bd, err)
	}
	if ob := f.last("order"); ob["standingInstFlag"] != "Y" || ob["buyParticipantLoginId"] != b.UCC || ob["buyerRefNo"] != bd.ID || ob["sellerRefNo"] != nil {
		t.Fatalf("instruction body %v", ob)
	}
	f.do(func() {
		f.orders = append(f.orders, map[string]any{"id": json.Number("901"), "orderNumber": "261118000000901", "standingInstFlag": "N", "dealType": "D", "symbol": "INE020B08BH6",
			"buyParticipantLoginId": b.UCC, "sellParticipantLoginId": "CPTY1", "price": json.Number("101"), "value": json.Number("500000"), "accrInt": json.Number("0"), "settle": json.Number("1"),
			"sendFor": "M", "yieldType": "M", "yield": json.Number("7"), "tradeDate": "18-11-2026", "tradeTime": "11:05", "status": json.Number("1"), "quantity": json.Number("5"), "consideration": json.Number("505000"), "entry": "18-11-2026"})
	})
	if _, err := o.CBRICSSync(ctx, T, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	bd, _, _ = docs.Get[CBRICSDeal](ctx, o.Docs, T, cCBRICSDeal, bd.ID)
	if bd.Status != CBDToConfirm || bd.CounterOrderID != "901" || bd.OrderNo == "" {
		t.Fatalf("to confirm %+v", bd)
	}
	if _, err := o.CBRICSRespond(ctx, T, "priya.nair", bd.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if bd, err = o.DecideCBRICSDeal(ctx, T, "farhan.khan", bd.ID, true, ""); err != nil || bd.Status != CBDConfirmed {
		t.Fatalf("accepted %+v %v", bd, err)
	}
	if sb := f.last("order/status"); fmt.Sprint(sb["id"]) != "901" || fmt.Sprint(sb["status"]) != "7" || f.called("PUT order/status") != 1 {
		t.Fatalf("order/status %v", sb)
	}
	// Its settlement pays CPTY1's default account already; the client's
	// bonds go to another demat account than the client's: Ledgerline
	// proposes the client's, and the money work item has the payment page.
	f.do(func() {
		f.settles = append(f.settles, map[string]any{"id": 2, "orderNumber": "261118000000901", "symbol": "INE020B08BH6", "buyParticipantLoginId": b.UCC, "sellParticipantLoginId": "CPTY1",
			"settleStatus": 0, "source": 1, "modAccrInt": 0, "modQuantity": 5, "value": 500000, "price": 101, "modConsideration": 505000, "stampDutyAmount": 0.75, "stampDutyBearer": b.UCC,
			"buyerFundPayinObligation": 505000.75, "sellerFundPayoutObligation": 505000, "ifscCode": "ABCD0111111", "accountNo": "123456789", "settlementNo": "2611182", "modSettleDate": "19-11-2026",
			"benId": "1208999999999999", "fundPayinRefId": "made-up-payin-ref"})
	})
	if _, err := o.CBRICSSync(ctx, T, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	bd, _, _ = docs.Get[CBRICSDeal](ctx, o.Docs, T, cCBRICSDeal, bd.ID)
	if bd.Pending != "Account" || bd.Account.IFSC != "" || bd.Account.BenID != normDigits(payoutDemat(b).ID) {
		t.Fatalf("demat proposal %+v", bd.Account)
	}
	if mw, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-cbrics-money-"+bd.ID); !strings.Contains(mw.Title, "pays in ₹505000.75") || !strings.Contains(mw.Detail, base+"/rest/pg?fundPayinRefId=made-up-payin-ref") {
		t.Fatalf("buyer's money work item %+v", mw)
	}
	if bd, err = o.DecideCBRICSDeal(ctx, T, "farhan.khan", bd.ID, true, ""); err != nil || bd.Settlement.BenID != normDigits(payoutDemat(b).ID) {
		t.Fatalf("demat sent %+v %v", bd.Settlement, err)
	}
	if db := f.last("settle/order/updatedp"); fmt.Sprint(db["id"]) != "2" || db["benId"] != normDigits(payoutDemat(b).ID) || db["dpId"] != nil || f.last("settle/order/updatebank")["id"] == json.Number("2") {
		t.Fatalf("updatedp %v", db)
	}

	// A deal entered on the CBRICS portal for a client is taken in.
	f.do(func() {
		f.orders = append(f.orders, map[string]any{"id": json.Number("902"), "orderNumber": "261118000000902", "standingInstFlag": "N", "dealType": "B", "symbol": "INE752E07FF5",
			"buyParticipantLoginId": "PART3", "sellParticipantLoginId": a.UCC, "price": json.Number("100"), "value": json.Number("200000"), "accrInt": json.Number("0"), "settle": json.Number("0"),
			"sendFor": "C", "yieldType": "M", "yield": json.Number("9"), "tradeDate": "18-11-2026", "tradeTime": "09:30", "sellerRefNo": "PORTAL-1", "status": json.Number("1"), "quantity": json.Number("2"), "entry": "18-11-2026"})
	})
	if res, err := o.CBRICSSync(ctx, T, "priya.nair"); err != nil || res.Added != 1 {
		t.Fatalf("taken in %+v %v", res, err)
	}
	v, _ = o.CBRICSStatus(ctx, T, "priya.nair")
	var got *CBRICSDeal
	for i := range v.Deals {
		if v.Deals[i].OrderNo == "261118000000902" {
			got = &v.Deals[i]
		}
	}
	if got == nil || got.Source != "CBRICS" || got.UCC != a.UCC || got.Side != "Sell" || got.Status != CBDReported || got.FaceValue.String() != "100000" {
		t.Fatalf("taken in %+v", got)
	}

	// A change before the buyer confirms: CBRICS cancels the old order and
	// gives a new one; the old one is not read back as the deal's.
	in.TradeTime, in.Price = "11:08", money.MustDec("100.25")
	d2, err := o.SaveCBRICSDeal(ctx, T, "priya.nair", in)
	if err != nil {
		t.Fatal(err)
	}
	if d2, err = o.DecideCBRICSDeal(ctx, T, "farhan.khan", d2.ID, true, ""); err != nil || d2.Status != CBDReported {
		t.Fatalf("second deal %+v %v", d2, err)
	}
	in.ID, in.Price = d2.ID, money.MustDec("100.30")
	if d2, err = o.SaveCBRICSDeal(ctx, T, "priya.nair", in); err != nil || d2.Pending != "Change" || d2.Price.String() != "100.25" {
		t.Fatalf("change %+v %v", d2, err)
	}
	oldNo := d2.OrderNo
	if d2, err = o.DecideCBRICSDeal(ctx, T, "farhan.khan", d2.ID, true, ""); err != nil || d2.OrderNo == oldNo || d2.Price.String() != "100.3" || len(d2.OldOrders) != 1 {
		t.Fatalf("changed %+v %v", d2, err)
	}
	if ub := f.last("order/update"); fmt.Sprint(ub["id"]) == "" || fmt.Sprint(ub["price"]) != "100.3000" {
		t.Fatalf("update body %v", ub)
	}
	if _, err := o.CBRICSSync(ctx, T, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	if d2, _, _ = docs.Get[CBRICSDeal](ctx, o.Docs, T, cCBRICSDeal, d2.ID); d2.Status != CBDReported {
		t.Fatalf("after reading the cancelled old order %+v", d2)
	}
	// CBRICS refuses a deal: it is marked so with CBRICS's words for the maker.
	in.ID, in.ISIN = "", "INE000000017"
	d3, err := o.SaveCBRICSDeal(ctx, T, "priya.nair", in)
	if err != nil {
		t.Fatal(err)
	}
	if d3, err = o.DecideCBRICSDeal(ctx, T, "farhan.khan", d3.ID, true, ""); err == nil || d3.Status != CBDRefused || !strings.Contains(d3.Reason, "Violation reason is mandatory") {
		t.Fatalf("refused %+v %v", d3, err)
	}
	if _, err := o.CancelCBRICSDeal(ctx, T, "priya.nair", d3.ID, "entered in error"); err != nil {
		t.Fatal(err)
	}
}

func TestCBRICSParticipantsAndNotify(t *testing.T) {
	o, ctx, f, _ := cbricsSetup(t)
	cs := cbricsClients(t, o, ctx, 2)
	cp := CBRICSParty{Code: "CPTY2", Name: "Made-up Fund", PAN: "PAN_EXEMPT", Contact: "Ms Test", Mobiles: []string{"9876500000"}, Emails: []string{"desk@example.test"}, Telephone: "02200000001",
		StateCode: "24", RegAddr: "3 Made-up Street, Ahmedabad", DOB: "1999-01-01", Banks: []CBRICSPartyBank{{Bank: "ABC Bank", IFSC: "ABCD0111111", Account: "123456780", Default: true}},
		DPs: []CBRICSPartyDP{{Depository: "CDSL", BenID: "1234567891234567", Default: true}}}
	cp.Mobiles = append(cp.Mobiles, "1", "2", "3")
	if _, err := o.SaveCBRICSParty(ctx, T, "priya.nair", cp); err == nil || !strings.Contains(err.Error(), "one to three mobile") {
		t.Fatalf("four mobiles taken: %v", err)
	}
	cp.Mobiles = cp.Mobiles[:1]
	if _, err := o.SaveCBRICSParty(ctx, T, "priya.nair", cp); err != nil {
		t.Fatal(err)
	}
	// The checker returns it; the maker corrects it; it goes.
	if p, err := o.DecideCBRICSParty(ctx, T, "farhan.khan", "CPTY2", false, "check the PAN"); err != nil || p.State != "Draft" || p.Pending != nil {
		t.Fatalf("returned %+v %v", p, err)
	}
	if _, err := o.SaveCBRICSParty(ctx, T, "priya.nair", cp); err != nil {
		t.Fatal(err)
	}
	p, err := o.DecideCBRICSParty(ctx, T, "farhan.khan", "CPTY2", true, "")
	if err != nil {
		t.Fatal(err)
	}
	// NSE returns it; the corrected details go by unreg/update.
	f.do(func() {
		f.unregs[p.CBRICSID]["workflowStatus"], f.unregs[p.CBRICSID]["remarks"] = json.Number("6"), "PAN exemption proof needed"
	})
	if _, err := o.CBRICSSync(ctx, T, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	if p, _, _ = docs.Get[CBRICSParty](ctx, o.Docs, T, cCBRICSParty, "CPTY2"); p.State != "Returned" || p.Remarks != "PAN exemption proof needed" {
		t.Fatalf("returned by NSE %+v", p)
	}
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-cbrics-nse-CPTY2-6-2026-11-18"); w.Status != "Open" || w.Role != RoleMaker || !strings.Contains(w.Detail, "PAN exemption proof") {
		t.Fatalf("returned work item %+v", w)
	}
	cp.PAN = "AAAPZ1234C"
	if p, err = o.SaveCBRICSParty(ctx, T, "priya.nair", cp); err != nil || p.Pending.Action != "update" {
		t.Fatalf("correction %+v %v", p, err)
	}
	if p, err = o.DecideCBRICSParty(ctx, T, "farhan.khan", "CPTY2", true, ""); err != nil || p.PAN != "AAAPZ1234C" || p.State != "Sent" {
		t.Fatalf("update %+v %v", p, err)
	}
	if ub := f.last("unreg/update"); fmt.Sprint(ub["id"]) != p.CBRICSID || fmt.Sprint(ub["actualStatus"]) != "4" || ub["bankAccountList"].([]any)[0].(map[string]any)["status"] != "A" {
		t.Fatalf("update body %v", ub)
	}
	f.do(func() { f.unregs[p.CBRICSID]["workflowStatus"] = json.Number("1") })
	if _, err := o.CBRICSSync(ctx, T, "priya.nair"); err != nil {
		t.Fatal(err)
	}
	// Accounts of an approved participant: add a demat account, make it the
	// default; then a contact change.
	if _, err := o.CBRICSPartyRequest(ctx, T, "priya.nair", "CPTY2", CBRICSPartyReq{Action: "add_dp"}, "", "IN30378610000375"); err != nil {
		t.Fatal(err)
	}
	if p, err = o.DecideCBRICSParty(ctx, T, "farhan.khan", "CPTY2", true, ""); err != nil || len(p.DPs) != 2 {
		t.Fatalf("add dp %+v %v", p.DPs, err)
	}
	if b := f.last("unreg/dpacc"); b["dpType"] != "NSDL" || b["dpId"] != "IN303786" || b["benId"] != "10000375" || b["participantCode"] != "CPTY2" {
		t.Fatalf("dpacc body %v", b)
	}
	if _, err := o.CBRICSPartyRequest(ctx, T, "priya.nair", "CPTY2", CBRICSPartyReq{Action: "default_dp", DP: &CBRICSPartyDP{DPID: "IN303786", BenID: "10000375"}}, "", ""); err != nil {
		t.Fatal(err)
	}
	if p, err = o.DecideCBRICSParty(ctx, T, "farhan.khan", "CPTY2", true, ""); err != nil || !p.DPs[1].Default || p.DPs[0].Default {
		t.Fatalf("default dp %+v %v", p.DPs, err)
	}
	if b := f.last("unreg/dpacc/final/markdefault"); b["benId"] != "10000375" {
		t.Fatalf("markdefault %v", b)
	}
	cp.Contact, cp.Emails = "Mr New", []string{"new@example.test"}
	if p, err = o.SaveCBRICSParty(ctx, T, "priya.nair", cp); err != nil || p.Pending.Action != "contact" || p.Contact == "Mr New" {
		t.Fatalf("contact change %+v %v", p, err)
	}
	if p, err = o.DecideCBRICSParty(ctx, T, "farhan.khan", "CPTY2", true, ""); err != nil || p.Contact != "Mr New" || len(p.DPs) != 2 {
		t.Fatalf("contact sent %+v %v", p, err)
	}
	if b := f.last("unreg/final/updatecontact"); b["contactPerson"] != "Mr New" || fmt.Sprint(b["id"]) != p.CBRICSID {
		t.Fatalf("updatecontact %v", b)
	}
	// Participants made on the CBRICS portal come in, linked by PAN.
	f.do(func() {
		f.unregs["70001"] = map[string]any{"id": json.Number("70001"), "loginId": "PORTALC", "firstName": cs[1].Name, "panNo": cs[1].PAN, "workflowStatus": json.Number("1"), "actualStatus": 4,
			"contactPerson": "x", "mobileList": []any{"9999999999"}, "emailList": []any{"x@example.test"}, "telephone": "1", "stateCode": "27", "regAddress": "x", "dobDoi": "01-01-1990",
			"bankAccountList": []any{map[string]any{"bankName": "X", "bankIFSC": "ABCD0111112", "bankAccountNo": "111", "isDefault": "Y", "status": "A", "workflowStatus": 1}},
			"dpAccountList":   []any{map[string]any{"dpType": "CDSL", "benId": "1208160000000099", "isDefault": "Y", "status": "A", "workflowStatus": 1}}}
	})
	if n, err := o.CBRICSImportParties(ctx, T, "priya.nair"); err != nil || n != 1 {
		t.Fatalf("import %d %v", n, err)
	}
	if ip, _, _ := docs.Get[CBRICSParty](ctx, o.Docs, T, cCBRICSParty, "PORTALC"); ip.UCC != cs[1].UCC || ip.State != "Approved" || len(ip.Banks) != 1 || ip.DOB != "1990-01-01" {
		t.Fatalf("imported %+v", ip)
	}
	// Deletion.
	if _, err := o.CBRICSPartyRequest(ctx, T, "priya.nair", "CPTY2", CBRICSPartyReq{Action: "delete"}, "", ""); err != nil {
		t.Fatal(err)
	}
	if p, err = o.DecideCBRICSParty(ctx, T, "farhan.khan", "CPTY2", true, ""); err != nil || p.State != "Deleted" {
		t.Fatalf("deleted %+v %v", p, err)
	}

	// NSE's notification: signed with the callback key; a wrong token is
	// refused; a good one has CBRICS read at the next tick.
	body := []byte(`{"settleOrderList": [{"symbol": "INE020B08BH6","settlementNo": "2212019","orderNumber": "221218000000367","buyParticipantLoginId": "INDBANK","settleStatus": 0,"id": 1}]}`)
	m := hmac.New(sha256.New, []byte("made-up-callback-key"))
	m.Write(body)
	tok := hex.EncodeToString(m.Sum(nil))
	if _, err := o.CBRICSNotify(ctx, T, strings.Repeat("0", 64), "203.0.113.5:443", body); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong token: %v", err)
	}
	if n, err := o.CBRICSNotify(ctx, T, strings.ToUpper(tok), "203.0.113.5:443", body); err != nil || n != 1 {
		t.Fatalf("notification %d %v", n, err)
	}
	st, _, _ := docs.GetOr(ctx, o.Docs, T, cCBRICSState, "state", CBRICSState{})
	if !st.Refresh || st.Notices != 1 || st.Kinds[0] != "settleOrderList" {
		t.Fatalf("state %+v", st)
	}
	f.do(func() {
		f.settles = append(f.settles, map[string]any{"id": 9, "orderNumber": "221218000000367", "symbol": "INE020B08BH6", "buyParticipantLoginId": "INDBANK", "sellParticipantLoginId": "DFHI", "settleStatus": 0})
	})
	before := f.called("GET order/sellreportings")
	o.cbricsTick(ctx, T, o.now())
	if f.called("GET order/sellreportings") != before+1 {
		t.Fatal("the notification did not bring a read")
	}
	// Within 10 minutes and no notification: no read.
	o.cbricsTick(ctx, T, o.now().Add(time.Minute))
	if f.called("GET order/sellreportings") != before+1 {
		t.Fatal("read again within 10 minutes")
	}
	if st, _, _ = docs.GetOr(ctx, o.Docs, T, cCBRICSState, "state", CBRICSState{}); st.Refresh || len(st.Unmatched) != 1 {
		t.Fatalf("state after the read %+v", st)
	}
}
