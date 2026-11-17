package ops

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// All made up.
const (
	fnTestUser   = "LDGADMIN"
	fnTestPass   = "Fs#Admin2026"
	fnTestVendor = "LDG_VC"
	fnTestVKey   = "vk-7f3a9c11e2"
	fnTestBroker = "LDGBRK"
)

// fakeNoren is the Noren WC Admin API as its document describes it: POST
// <base>/<Name>, body jData=<json>&jKey=<token>, every value a string,
// answers {"stat":"Ok"} or {"stat":"Not_Ok","emsg":…}.
type fakeNoren struct {
	t        *testing.T
	mu       sync.Mutex
	pass     string
	logins   int
	tokens   map[string]bool
	expire   bool              // the next request finds its session expired
	down     bool              // the OMS answers "unknown request"
	refuse   map[string]string // actid -> SetLimits refusal
	exists   map[string]bool   // actid already a client
	limits   map[string]map[string]string
	hold     map[string]map[string]string // actid -> isin -> hold_qty
	t1       map[string]map[string]string // actid -> token -> hold_qty
	blocked  map[string]string            // actid -> how
	reqs     map[string][]map[string]any  // name -> jData
	bodies   []string
	lastNum  int
	srcBIDOK bool
}

func newFakeNoren(t *testing.T) *fakeNoren {
	return &fakeNoren{t: t, pass: fnTestPass, tokens: map[string]bool{}, refuse: map[string]string{}, exists: map[string]bool{},
		limits: map[string]map[string]string{}, hold: map[string]map[string]string{}, t1: map[string]map[string]string{},
		blocked: map[string]string{}, reqs: map[string][]map[string]any{}}
}

func (f *fakeNoren) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs[name])
}

func (f *fakeNoren) last(name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	rs := f.reqs[name]
	if len(rs) == 0 {
		return nil
	}
	return rs[len(rs)-1]
}

// allStrings checks the document's rule that every value is a string.
func allStrings(v any) bool {
	switch x := v.(type) {
	case string:
		return true
	case []any:
		for _, e := range x {
			if !allStrings(e) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, e := range x {
			if !allStrings(e) {
				return false
			}
		}
		return true
	}
	return false
}

func (f *fakeNoren) handler(w http.ResponseWriter, r *http.Request) {
	say := func(v any) { b, _ := json.Marshal(v); w.Write(b) }
	no := func(msg string) { say(map[string]string{"stat": "Not_Ok", "emsg": msg}) }
	name := strings.TrimPrefix(r.URL.Path, "/NorenWCAdmin/")
	if r.Method != http.MethodPost || name == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	b, _ := io.ReadAll(r.Body)
	body := string(b)
	if !strings.HasPrefix(body, "jData=") {
		no("Missing 'jData'")
		return
	}
	js, key := strings.TrimPrefix(body, "jData="), ""
	if i := strings.LastIndex(js, "&jKey="); i >= 0 {
		js, key = js[:i], js[i+6:]
	}
	if strings.Contains(js, "&") {
		f.t.Errorf("an & left in jData: %s", js)
	}
	var jd map[string]any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(js, "%26", "&")), &jd); err != nil {
		no("jData is not valid json object")
		return
	}
	if !allStrings(jd) {
		no("One or more input parameters are not in string format")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, body)
	f.reqs[name] = append(f.reqs[name], jd)
	s := func(k string) string { v, _ := jd[k].(string); return v }
	if name == "AdminLogin" {
		f.logins++
		if s("uid") != fnTestUser || s("pwd") != sha256Hex([]byte(f.pass)) || s("vc") != fnTestVendor || s("appkey") != sha256Hex([]byte(fnTestUser+"|"+fnTestVKey)) || s("imei") == "" || s("apkversion") == "" {
			no("Invalid Input : Wrong Password")
			return
		}
		f.lastNum++
		tok := "tok" + strconv.Itoa(f.lastNum) + "9d0c4f2ab77e"
		f.tokens[tok] = true
		say(map[string]string{"stat": "Ok", "susertoken": tok, "brkname": fnTestBroker, "uname": "Admin", "request_time": "08:40:01 29-10-2026"})
		return
	}
	if !f.tokens[key] || s("uid") != fnTestUser {
		w.WriteHeader(http.StatusUnauthorized)
		no("Session Expired : Invalid Session Key")
		return
	}
	if f.expire {
		f.expire = false
		delete(f.tokens, key)
		no("Session Expired : Invalid Session Key")
		return
	}
	if f.down {
		no(`Error Occurred : 1 "unknown request"`)
		return
	}
	act := s("actid")
	needBID := func() bool {
		if s("src_uid") != fnTestUser || s("src_bid") != fnTestBroker {
			no("Invalid Input : src_uid or src_bid")
			return false
		}
		return true
	}
	ok := func() { say(map[string]string{"stat": "Ok", "request_time": "08:41:00 29-10-2026"}) }
	switch name {
	case "SetLimits":
		if s("src_uid") != fnTestUser || jd["src_bid"] != nil {
			no("Invalid Input : src_uid")
			return
		}
		if m := f.refuse[act]; m != "" {
			no(m)
			return
		}
		f.limits[act] = map[string]string{"cash": s("cash"), "brkcollamt": s("brkcollamt"), "daycash": s("daycash")}
		ok()
	case "UpdateAbsHoldingsISIN", "NpoaISINHoldings":
		if !needBID() {
			return
		}
		if name == "UpdateAbsHoldingsISIN" && s("prod") != "C" || name == "NpoaISINHoldings" && jd["prod"] != nil {
			no("Invalid Input : prod")
			return
		}
		if f.hold[act] == nil {
			f.hold[act] = map[string]string{}
		}
		if s("hold_qty") == "0" {
			delete(f.hold[act], s("isin"))
		} else {
			f.hold[act][s("isin")] = s("hold_qty")
		}
		ok()
	case "UpdateBTSTAbsHoldings":
		tks, _ := jd["exch_tks"].([]any)
		if !needBID() || len(tks) == 0 || s("is_collateral") == "" {
			no("Invalid Input : exch_tks or is_collateral")
			return
		}
		tk := tks[0].(map[string]any)["token"].(string)
		if f.t1[act] == nil {
			f.t1[act] = map[string]string{}
		}
		f.t1[act][tk] = s("hold_qty")
		ok()
	case "BlockAcct", "UnBlockAcct", "FreezeAccount", "DeFreezeAccount":
		f.blocked[act] = name + s("type")
		ok()
	case "AddInvestorClient":
		if jd["pwd"] != nil {
			f.t.Errorf("a password was sent: %v", jd)
		}
		for _, k := range []string{"actid", "acct_name", "user_id", "region", "email", "mob_num", "family_id", "pan", "acct_type", "acct_status", "dob", "user_status", "uname"} {
			if s(k) == "" {
				no(k + " is Missing.")
				return
			}
		}
		if f.exists[act] {
			no(`Error Occurred : 2 "invalid input[Client exists ` + act + `]"`)
			return
		}
		f.exists[act] = true
		ok()
	case "ModifyClient", "InvClientSegEnabled", "InvClientProductEnabled", "SetMarginBasket":
		if jd["pwd"] != nil {
			f.t.Errorf("a password was sent: %v", jd)
		}
		ok()
	case "Limits":
		l := f.limits[act]
		say(map[string]string{"stat": "Ok", "actid": act, "cash": l["cash"], "brkcollamt": l["brkcollamt"], "payin": "0.00", "payout": "0.00", "marginused": "0.00"})
	case "Holdings":
		if len(f.hold[act]) == 0 {
			no(`Error Occurred : 5 "no data"`)
			return
		}
		var out []map[string]string
		for isin, q := range f.hold[act] {
			out = append(out, map[string]string{"status": "Ok", "actid": act, "prod": "C", "isin": isin, "hold_qty": q})
		}
		say(out)
	default:
		no(`Error Occurred : 1 "unknown request"`)
	}
}

func fnSave(t *testing.T, o *Ops, ctx context.Context, url string, vals map[string]string) {
	t.Helper()
	v := map[string]string{"env": "UAT", "url": url + "/NorenWCAdmin", "user": fnTestUser, "vendor_code": fnTestVendor, "send": "Off"}
	for k, x := range vals {
		v[k] = x
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connFinspotAPI, v, map[string]string{"password": fnTestPass, "vendor_key": fnTestVKey}); err != nil {
		t.Fatal(err)
	}
}

func fnFake(t *testing.T) (*fakeNoren, string) {
	t.Helper()
	f := newFakeNoren(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	old, gap := finspotHTTP, finspotGap
	finspotHTTP, finspotGap = srv.Client(), 0
	t.Cleanup(func() { finspotHTTP, finspotGap = old, gap; fnSessions.Delete(T) })
	return f, srv.URL
}

func fnOpenWork(o *Ops, ctx context.Context, key string) *WorkItem {
	ws, _ := o.WorkItems(ctx, T, "Open")
	for i := range ws {
		if ws[i].Key == key {
			return &ws[i]
		}
	}
	return nil
}

func TestFinspotAPIConnAndBody(t *testing.T) {
	good := Connection{Values: map[string]string{"url": "https://fs.example/NorenWCAdmin", "user": "A", "vendor_code": "V"}, Secrets: map[string]string{"password": "p", "vendor_key": "k"}}
	x, err := parseFinspotAPIConn(good)
	if err != nil || x.bodAt != "08:40" || x.parallel != 4 || x.notionalField() != "brkcollamt" || x.blocks != fnBlocksFile || x.enabled(FNPartBlocks) || !x.enabled(FNPartT1) || strings.Join(x.access, ",") != "WEB,MOB" {
		t.Fatalf("%+v %v", x, err)
	}
	for k, v := range map[string]string{"url": "http://fs.example/NorenWCAdmin", "bod_at": "09:15", "parallel": "9", "every": "0", "access_types": "TWS"} {
		c := Connection{Values: map[string]string{}, Secrets: good.Secrets}
		for a, b := range good.Values {
			c.Values[a] = b
		}
		c.Values[k] = v
		if _, err := parseFinspotAPIConn(c); err == nil {
			t.Errorf("%s=%s taken", k, v)
		}
	}
	if _, err := parseFinspotAPIConn(Connection{Values: good.Values}); err == nil {
		t.Fatal("no password or vendor key taken")
	}
	b, _ := fnBody(map[string]any{"bank_addr": "NO 17/1, BELLANDUR & MAIN ROAD"}, "k1")
	if b != `jData={"bank_addr":"NO 17/1, BELLANDUR %26 MAIN ROAD"}&jKey=k1` {
		t.Fatalf("body %s", b)
	}
	if fnDate("2026-06-29") != "29-06-2026" || fnDate("") != "" || strings.Join(fnProductList("I,M,C"), ",") != "MIS,NRML,CNC" || fnAcctType(Client{Type: "Corporate"}) != "Institutional" {
		t.Fatal("conversions")
	}
}

// Limits: the start of day, money in during the day, refusals, an expired
// session, the OMS down at EOD/BOD and a refused password.
func TestFinspotAPILimits(t *testing.T) {
	o, ctx := sepSetup(t)
	clock := time.Date(2026, 10, 29, 8, 40, 0, 0, ist)
	o.Now = func() time.Time { return clock }
	f, base := fnFake(t)
	fnSave(t, o, ctx, base, map[string]string{"send": "On", "holdings": "Off"})
	cv, err := o.TestConnection(ctx, T, "admin", connFinspotAPI)
	if err != nil || cv.Status != "Working" || !strings.Contains(cv.Problem, "accepted the admin login") || !strings.Contains(cv.Problem, fnTestBroker) {
		t.Fatalf("test %+v %v", cv, err)
	}
	if _, err := o.FinspotAPIPush(ctx, T, "priya.nair", "", nil, nil); err == nil {
		t.Fatal("a maker pushed to Finspot")
	}
	if _, err := o.FinspotAPIPush(ctx, T, "anita.rao", "", []string{FNPartHoldings}, nil); err == nil {
		t.Fatal("holdings pushed with holdings off")
	}

	// 08:40: the start of day sends every client's limit.
	o.finspotAPITick(ctx, T, clock)
	if f.limits["BJ01"]["cash"] != "10000.00" || f.limits["BJ02"]["cash"] != "50000.00" || f.limits["BJ01"]["brkcollamt"] != "0.00" {
		t.Fatalf("limits %v", f.limits)
	}
	st, _, _ := docs.Get[FinspotAPIClient](ctx, o.Docs, T, cFNClient, "BJ01")
	if st.Status != FNPushed || st.Cash.Cmp(mustDec("10000")) != 0 || st.ActID != "BJ01" {
		t.Fatalf("state %+v", st)
	}
	sd, _, _ := docs.Get[fnStateDoc](ctx, o.Docs, T, cFNState, "state")
	if sd.BOD[FNPartLimits] != "2026-10-29" || f.logins != 1 { // the test's sign-in serves the day
		t.Fatalf("state %+v, %d sign-ins", sd, f.logins)
	}
	n := f.count("SetLimits")

	// 09:10: nothing changed, nothing is sent, and no run is kept.
	clock = clock.Add(30 * time.Minute)
	o.finspotAPITick(ctx, T, clock)
	if f.count("SetLimits") != n {
		t.Fatal("unchanged limits sent again")
	}
	runs, _ := docs.List[FinspotAPIRun](ctx, o.Docs, T, cFNRun, "")
	if len(runs) != 1 {
		t.Fatalf("%d runs kept", len(runs))
	}

	// Equity money for BJ02 queues it; the next tick sends it at once.
	ecmsCredit(t, o, ctx, "S2", "UTR2", sgAcct, "91902033334444", "1000", "NSE FUNDS")
	if q := o.fnQueuedUCCs(ctx, T); len(q) != 1 || q[0] != "BJ02" {
		t.Fatalf("queue %v", q)
	}
	clock = clock.Add(time.Minute)
	o.finspotAPITick(ctx, T, clock)
	if f.count("SetLimits") != n+1 || f.limits["BJ02"]["cash"] != "51000.00" || len(o.fnQueuedUCCs(ctx, T)) != 0 {
		t.Fatalf("queued push: %d, %v", f.count("SetLimits")-n, f.limits["BJ02"])
	}
	// Commodity money is not Finspot's: BJ01 is not sent again.
	ecmsCredit(t, o, ctx, "S1", "UTR1", sgCom, "50100011112222", "5000", "MCX MARGIN")
	clock = clock.Add(6 * time.Minute)
	o.finspotAPITick(ctx, T, clock)
	if f.count("SetLimits") != n+1 {
		t.Fatal("commodity money changed the Finspot limit")
	}

	// The session ends: one new sign-in, and the push goes through.
	f.expire = true
	logins := f.logins
	if run, err := o.FinspotAPIPush(ctx, T, "anita.rao", "", []string{FNPartLimits}, []string{"BJ01"}); err != nil || run.Parts[0].Pushed != 1 || f.logins != logins+1 {
		t.Fatalf("after the session ended: %+v %v (%d sign-ins)", run, err, f.logins-logins)
	}

	// Finspot refuses a client: the state says so, and Risk is told.
	f.refuse["BJ02"] = `Error Occurred : RED:"invalid input"`
	run, err := o.FinspotAPIPush(ctx, T, "anita.rao", "", []string{FNPartLimits}, nil)
	if err != nil || run.Parts[0].Failed != 1 || run.Parts[0].Pushed != 1 || len(run.Failures) != 1 || !strings.Contains(run.Failures[0], "BJ02") {
		t.Fatalf("run %+v %v", run, err)
	}
	if s, _, _ := docs.Get[FinspotAPIClient](ctx, o.Docs, T, cFNClient, "BJ02"); s.Status != FNFailed || !strings.Contains(s.Error, "invalid input") || s.Cash.Cmp(mustDec("51000")) != 0 {
		t.Fatalf("BJ02 %+v", s)
	}
	if w := fnOpenWork(o, ctx, "finspotapi:limits"); w == nil || w.Role != RoleRisk {
		t.Fatalf("work item %+v", w)
	}
	delete(f.refuse, "BJ02")

	// The OMS is down (EOD/BOD): the run stops, the connection stays good.
	f.down = true
	if run, err := o.FinspotAPIPush(ctx, T, "anita.rao", "", nil, nil); err == nil || !strings.Contains(run.Stopped, "not taking requests") {
		t.Fatalf("down: %+v %v", run, err)
	}
	if c, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connFinspotAPI); c.Status == "Problem" {
		t.Fatal("the connection marked Problem while the OMS was down")
	}
	f.down = false

	// A refused password is tried once (four requests at a time never make
	// four attempts), and stops automatic pushes until the connection is saved again.
	logins = f.logins
	f.pass = "changed-on-finspot"
	fnSessions.Delete(T)
	if _, err := o.FinspotAPIPush(ctx, T, "anita.rao", "", nil, nil); err == nil {
		t.Fatal("pushed with a refused password")
	}
	if c, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connFinspotAPI); c.Status != "Problem" || !strings.Contains(c.Problem, "Wrong Password") {
		t.Fatalf("connection %+v", c)
	}
	if f.logins != logins+1 {
		t.Fatalf("%d sign-ins with the refused password, want 1", f.logins-logins)
	}
	logins = f.logins
	ecmsCredit(t, o, ctx, "S3", "UTR3", sgAcct, "91902033334444", "1000", "NSE FUNDS")
	clock = clock.Add(10 * time.Minute)
	o.finspotAPITick(ctx, T, clock)
	if f.logins != logins {
		t.Fatal("the scheduler tried the refused password again")
	}

	// Nothing kept holds the password, its hash, the vendor key, the app key or a session token.
	var kept []string
	rs, _ := docs.List[FinspotAPIRun](ctx, o.Docs, T, cFNRun, "")
	for _, r := range rs {
		b, _ := json.Marshal(r)
		kept = append(kept, string(b))
	}
	ws, _ := o.WorkItems(ctx, T, "")
	b, _ := json.Marshal(ws)
	kept = append(kept, string(b))
	secrets := []string{fnTestPass, sha256Hex([]byte(fnTestPass)), fnTestVKey, sha256Hex([]byte(fnTestUser + "|" + fnTestVKey)), "9d0c4f2ab77e"}
	for _, k := range kept {
		for _, s := range secrets {
			if strings.Contains(k, s) {
				t.Fatalf("a secret (%s…) was kept", s[:4])
			}
		}
	}
}

// Holdings and T1: what the files carry, cleared when no longer held,
// tokens for T1, the NPOA call, and reading back.
func TestFinspotAPIHoldings(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	next := set.nextBusinessDay(D)
	f, base := fnFake(t)
	fnSave(t, o, ctx, base, nil)
	rows, err := o.finspotHoldingRows(ctx, T, next, false)
	if err != nil || len(rows) == 0 {
		t.Fatalf("%d rows %v", len(rows), err)
	}
	run, err := o.FinspotAPIPush(ctx, T, "anita.rao", next, []string{FNPartHoldings}, nil)
	if err != nil || run.Parts[0].Pushed != len(rows) || run.Parts[0].Failed != 0 {
		t.Fatalf("run %+v %v", run.Parts, err)
	}
	coll := 0
	for _, r := range rows {
		v := r.Values
		if f.hold[v["AccountId"]][v["ISIN"]] != v["HoldingsQty"] {
			t.Fatalf("%s %s: Finspot has %q, the file %s", v["AccountId"], v["ISIN"], f.hold[v["AccountId"]][v["ISIN"]], v["HoldingsQty"])
		}
		if v["CollateralQty"] != "0" {
			coll++
		}
	}
	got := 0
	for _, jd := range f.reqs["UpdateAbsHoldingsISIN"] {
		if jd["colqty"] != "0" {
			got++
			if jd["col_mode"] != "1" || jd["hair_cut"] == "" {
				t.Fatalf("collateral line %v", jd)
			}
		}
	}
	if coll == 0 || got != coll {
		t.Fatalf("%d collateral lines sent, %d in the file", got, coll)
	}
	cl, _ := o.DailyFileChecklist(ctx, T, next)
	for _, it := range cl.Items {
		if it.ID == "finspot-holding" && it.Status != "Written" {
			t.Fatalf("checklist: holdings %s", it.Status)
		}
	}

	// An ISIN the client no longer holds is sent at 0 and forgotten.
	u := rows[0].Values["AccountId"]
	_, _ = docs.Update(ctx, o.Docs, T, cFNHold, u, "t", func(h *FinspotAPIHolding) error { h.Hold["INE000Z01019"] = 5; return nil })
	f.mu.Lock()
	f.hold[u]["INE000Z01019"] = "5"
	f.mu.Unlock()
	run, err = o.FinspotAPIPush(ctx, T, "anita.rao", next, []string{FNPartHoldings}, []string{u})
	if err != nil || run.Parts[0].Cleared != 1 {
		t.Fatalf("clearing %+v %v", run.Parts, err)
	}
	if _, still := f.hold[u]["INE000Z01019"]; still {
		t.Fatal("the stale holding is still on Finspot")
	}
	if h, _, _ := docs.Get[FinspotAPIHolding](ctx, o.Docs, T, cFNHold, u); h.Hold["INE000Z01019"] != 0 || len(h.Hold) == 0 {
		t.Fatalf("state %v", h.Hold)
	}
	chk, err := o.FinspotAPICheckClient(ctx, T, "anita.rao", u)
	if err != nil || !chk.HoldMatch || len(chk.Holdings) == 0 || !chk.LimitMatch {
		t.Fatalf("check %+v %v", chk, err)
	}
	if _, err := o.FinspotAPICheckClient(ctx, T, "priya.nair", u); err == nil {
		t.Fatal("a maker read Finspot")
	}

	// T1 needs the exchange token: none in the sample's market data.
	t1, _ := o.finspotHoldingRows(ctx, T, next, true)
	if len(t1) == 0 {
		t.Fatal("no T1 lines in the sample")
	}
	run, err = o.FinspotAPIPush(ctx, T, "anita.rao", next, []string{FNPartT1}, nil)
	if err != nil || run.Parts[0].Skipped != len(t1) || run.Parts[0].Pushed != 0 || fnOpenWork(o, ctx, "finspotapi:token") == nil {
		t.Fatalf("T1 without tokens %+v %v", run.Parts, err)
	}
	m, _, _ := docs.Get[Market](ctx, o.Docs, T, cMarket, set.PrevBusinessDay(next))
	for i := range m.Instruments {
		if in := &m.Instruments[i]; in.Segment == "CM" && in.ISIN != "" {
			in.Token = strconv.Itoa(1000 + i)
		}
	}
	if err := docs.Upsert(ctx, o.Docs, T, cMarket, m.Date, m, "t"); err != nil {
		t.Fatal(err)
	}
	run, err = o.FinspotAPIPush(ctx, T, "anita.rao", next, []string{FNPartT1}, nil)
	if err != nil || run.Parts[0].Pushed != len(t1) || fnOpenWork(o, ctx, "finspotapi:token") != nil {
		t.Fatalf("T1 %+v %v", run.Parts, err)
	}
	if jd := f.last("UpdateBTSTAbsHoldings"); jd["is_collateral"] != "false" || jd["prod"] != "C" {
		t.Fatalf("T1 request %v", jd)
	}

	// Clients without a POA by the NPOA call, with their DP account.
	fnSave(t, o, ctx, base, map[string]string{"npoa": fnNPOACall})
	if _, err := o.FinspotAPIPush(ctx, T, "anita.rao", next, []string{FNPartHoldings}, nil); err != nil {
		t.Fatal(err)
	}
	if f.count("NpoaISINHoldings") == 0 {
		t.Fatal("the NPOA call was not used")
	}
	if jd := f.last("NpoaISINHoldings"); jd["dp_acct_num"] == nil || jd["is_collateral"] == nil {
		t.Fatalf("NPOA request %v", jd)
	}
}

// Blocks, client logins and margin baskets, when switched on.
func TestFinspotAPIBlocksClientsBaskets(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	next := set.nextBusinessDay(D)
	f, base := fnFake(t)
	fnSave(t, o, ctx, base, map[string]string{"blocks": fnBlocksAcct, "clients": "On", "baskets": "On"})
	o.SaveConnection(ctx, T, "admin", connFinspot, map[string]string{"exchange_users": "NSE=9001:100000000001; BSE=9002:1000000000002; NFO=9003:100000000003; BFO=9004:100000000004; CDS=9005:100000000005; BCD=9006:100000000006; MCX=9007:100000000007:31/12/2027", "segment_expiry": "30/06/2027"}, nil)
	cs, _ := o.tradingClientMap(ctx, T)
	var u string
	for _, k := range sortedKeys(cs) {
		if finspotBlockWhy(cs[k]) == "" {
			u = k
			break
		}
	}
	c := cs[u]
	c.Blocked, c.BlockedReason = true, "Debit over 5 days"
	_ = docs.Upsert(ctx, o.Docs, T, cClient, u, c, "t")
	run, err := o.FinspotAPIPush(ctx, T, "anita.rao", next, []string{FNPartBlocks}, nil)
	if err != nil || f.blocked[u] != "BlockAcct" || run.Parts[0].Failed != 0 {
		t.Fatalf("block %+v %v %v", run.Parts, err, f.blocked)
	}
	if s, _, _ := docs.Get[FinspotBlockState](ctx, o.Docs, T, cFinspotBlock, u); !s.Blocked {
		t.Fatalf("block state %+v", s)
	}
	n := f.count("BlockAcct")
	if run, _ := o.finspotAPIPush(ctx, T, SystemActor, next, "Intraday", []string{FNPartBlocks}, nil, false); f.count("BlockAcct") != n || run.Parts[0].Pushed != 0 {
		t.Fatal("an unchanged block sent again during the day")
	}
	c.Blocked = false
	_ = docs.Upsert(ctx, o.Docs, T, cClient, u, c, "t")
	o.finspotAPIPush(ctx, T, SystemActor, next, "Intraday", []string{FNPartBlocks}, nil, false)
	if f.blocked[u] != "UnBlockAcct" {
		t.Fatalf("unblock %v", f.blocked[u])
	}
	fnSave(t, o, ctx, base, map[string]string{"blocks": fnBlocksFreeze, "clients": "On", "baskets": "On"})
	c.Blocked = true
	_ = docs.Upsert(ctx, o.Docs, T, cClient, u, c, "t")
	o.finspotAPIPush(ctx, T, SystemActor, next, "Intraday", []string{FNPartBlocks}, nil, false)
	if f.blocked[u] != "FreezeAccount2" {
		t.Fatalf("freeze %v", f.blocked[u])
	}

	// Client logins: new ones added (no password), known ones modified.
	var other string
	for _, k := range sortedKeys(cs) {
		if k != u && cs[k].Status == "Active" {
			other = k
			break
		}
	}
	// A client Finspot cannot take yet (no date of birth) is not tried again
	// during the day until its details change.
	run, _ = o.finspotAPIPush(ctx, T, SystemActor, next, "Intraday", []string{FNPartClients}, nil, false)
	if run.Parts[0].Failed == 0 || f.count("AddInvestorClient") != 0 {
		t.Fatalf("clients without a date of birth: %+v", run.Parts)
	}
	if run, _ = o.finspotAPIPush(ctx, T, SystemActor, next, "Intraday", []string{FNPartClients}, nil, false); run.Parts[0].Failed != 0 || run.Parts[0].Skipped == 0 {
		t.Fatalf("tried again unchanged: %+v", run.Parts)
	}
	f.exists[other] = true // already on Finspot
	// The sample has no dates of birth; KYC always has one.
	for _, k := range sortedKeys(cs) {
		if x, _ := o.Client(ctx, T, k); x.DOB == "" {
			x.DOB = "1984-03-0" + strconv.Itoa(1+len(k)%9)
			_ = docs.Upsert(ctx, o.Docs, T, cClient, k, x, "t")
		}
	}
	cs, _ = o.tradingClientMap(ctx, T)
	run, err = o.FinspotAPIPush(ctx, T, "anita.rao", next, []string{FNPartClients}, nil)
	if err != nil || run.Parts[0].Pushed == 0 || f.count("AddInvestorClient") == 0 {
		t.Fatalf("clients %+v %v %v", run.Parts, err, run.Failures[:min(3, len(run.Failures))])
	}
	if seg := f.last("InvClientSegEnabled"); seg["expirydate"] == nil || seg["exch_uid"] == nil {
		t.Fatalf("segment %v", seg)
	}
	if f.count("ModifyClient") == 0 || f.count("InvClientProductEnabled") == 0 {
		t.Fatalf("an existing client was not modified: %d", f.count("ModifyClient"))
	}
	for _, jd := range f.reqs["AddInvestorClient"] {
		if jd["user_access_type"] == nil || jd["broker_id"] != fnTestBroker || jd["src_uid"] != fnTestUser {
			t.Fatalf("add %v", jd)
		}
	}
	adds := f.count("AddInvestorClient")
	if run, _ := o.finspotAPIPush(ctx, T, SystemActor, next, "Intraday", []string{FNPartClients}, nil, false); run.Parts[0].Pushed != 0 || f.count("AddInvestorClient") != adds {
		t.Fatalf("unchanged clients sent again: %+v", run.Parts)
	}
	c2 := cs[other]
	c2.Email = "new.address@x.example"
	_ = docs.Upsert(ctx, o.Docs, T, cClient, other, c2, "t")
	mods := f.count("ModifyClient")
	o.finspotAPIPush(ctx, T, SystemActor, next, "Intraday", []string{FNPartClients}, []string{other}, false)
	if f.count("ModifyClient") != mods+1 || f.count("AddInvestorClient") != adds || f.last("ModifyClient")["email"] != "new.address@x.example" {
		t.Fatal("a changed client was not modified")
	}

	// Margin baskets go by token.
	m, _, _ := docs.Get[Market](ctx, o.Docs, T, cMarket, set.PrevBusinessDay(next))
	for i := range m.Instruments {
		if in := &m.Instruments[i]; in.Segment == "CM" && in.Exchange == "NSE" {
			in.Token = strconv.Itoa(2000 + i)
		}
	}
	_ = docs.Upsert(ctx, o.Docs, T, cMarket, m.Date, m, "t")
	run, err = o.FinspotAPIPush(ctx, T, "anita.rao", next, []string{FNPartBaskets}, nil)
	if err != nil || run.Parts[0].Pushed == 0 {
		t.Fatalf("baskets %+v %v", run.Parts, err)
	}
	jd := f.reqs["SetMarginBasket"][0]
	toks, _ := jd["token_array"].([]any)
	pcts, _ := jd["margin_perc_array"].([]any)
	if jd["exch"] != "NSE" || jd["basket_name"] != "NSEMIS" || jd["prod"] != "I" || len(toks) == 0 || len(toks) != len(pcts) {
		t.Fatalf("basket %v", jd)
	}
}
