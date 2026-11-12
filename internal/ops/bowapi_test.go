package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// All made up.
const (
	bowTestPass = "Bw#Pass2026x"
	bowTestOTP  = "413007"
	bowTestSess = "1105495721-01086138139"
)

// fakeBOW is BOW's HTTP API as the document describes it: answers are
// records separated by "~" and columns by "|", amounts × 10,000.
type fakeBOW struct {
	t        *testing.T
	mu       sync.Mutex
	twoFA    bool
	logins   int
	otpAsked int
	sessions map[string]bool
	users    [][]string // client master rows: USID, login, back office
	deps     map[string][]*fakeDep
	nextID   int
	edits    []url.Values
	paths    []string
	refuse   map[string]string // login -> EditUserDeposit refusal
}

type fakeDep struct {
	id, level, cat   string
	cash, mult, eff  int64
	mtm, mtmLim, adh string
}

func newFakeBOW(t *testing.T) *fakeBOW {
	return &fakeBOW{t: t, sessions: map[string]bool{}, deps: map[string][]*fakeDep{}, nextID: 97, refuse: map[string]string{},
		users: [][]string{{"16377", "BJ01-T9065", "BJ01-T9065", "Asha", "Rao"}, {"16378", "bj02-T9065", "bj02-T9065", "Vikram", "Shah"}, {"16390", "ZZ99-T9065", "ZZ99-T9065", "Not", "Ours"}}}
}

func (f *fakeBOW) usidOf(login string) string {
	for _, u := range f.users {
		if strings.EqualFold(u[1], login) || strings.EqualFold(u[2], login) {
			return u[0]
		}
	}
	return ""
}

func (f *fakeBOW) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := r.ParseForm(); err != nil {
		f.t.Errorf("form: %v", err)
	}
	name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	f.paths = append(f.paths, name)
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.URL.RawQuery != "" {
		f.t.Errorf("%s: %s %q %q", name, r.Method, r.Header.Get("Content-Type"), r.URL.RawQuery)
	}
	fv := r.PostForm.Get
	say := func(s string) { fmt.Fprint(w, s) }
	switch name {
	case "LoginServlet":
		f.logins++
		if fv("USLOGINID") != "ADMIN-T9065" || fv("VenderCode") != "1_LDG" || fv("ThickClient") != "Y" || fv("Version") != "LEDGERLINE_1.0" {
			f.t.Errorf("login form %v", r.PostForm)
		}
		if fv("USPassword") != bowTestPass {
			say("1|Invalid User ID or Password~")
			return
		}
		if fv("enable2FA") == "Y" != f.twoFA {
			f.t.Errorf("enable2FA %q", fv("enable2FA"))
		}
		key := bowTestSess
		if f.twoFA {
			key = ""
		} else {
			f.sessions[key] = true
		}
		say("0||2||||||||||||||N|N|N||||||10|N|N||||~45|" + key + "|Sett No : 000000||ADMIN-T9065|3|false~292|8001|1|Test|0.0|^0^0~8001~Y|0~6|9|10|14~")
		return
	case "AuthenticationMode":
		f.otpAsked++
		if fv("verificationMode") != "1" || fv("USLOGINID") != "ADMIN-T9065" {
			f.t.Errorf("otp ask %v", r.PostForm)
		}
		say("0|OTP has been sent to registered mobile 98xxxxxx11 and registered Email Id.~")
		return
	case "AuthenticationLogin":
		if fv("OTP") != bowTestOTP {
			say("1|Invalid OTP~")
			return
		}
		f.sessions[bowTestSess+"-2fa"] = true
		say("0|" + bowTestSess + "-2fa~")
		return
	}
	if !f.sessions[fv("SessionKey")] || fv("ThickClient") != "Y" {
		say("1|Session expired. Please login again~")
		return
	}
	switch name {
	case "ExchangeStatus":
		say("BSE|Equity|3|2026-10-29~MCX|Commodities|3|2026-10-29~")
	case "UserSearchServlet":
		if fv("MAXUSID") != "0" || fv("userstatus") != "1" {
			f.t.Errorf("master %v", r.PostForm)
		}
		s := fmt.Sprintf("0||%d~", len(f.users))
		for _, u := range f.users {
			s += u[0] + "|" + u[3] + "|" + u[4] + "|" + u[1] + "|" + u[2] + "|1|1|2018-08-14 13:42:44.93|N||ADMIN-T9065~"
		}
		s += "45|Admin|User|ADMIN-T9065|ADMIN-T9065|1|3|2018-08-14 13:42:44.93|N||ADMIN-T9065~" // an administrator: not a client
		say(s)
	case "ListUserDeposit":
		usid := f.usidOf(fv("USBackOfficeId"))
		if usid == "" || fv("group") != "Y" || fv("APIDefaultScreenField") != "Y" {
			say("1|No such user~")
			return
		}
		ds := f.deps[usid]
		s := fmt.Sprintf("0||%d|13|~ID|User|Category|Exchange|Market|Product|Cash Deposit|Effective Deposit|Multiplier|AdhocAmount|MTM Multiplier|MTM Limit|USID~HLNO|SLAO~80|80~1|2~", len(ds))
		for _, d := range ds {
			s += fmt.Sprintf("%s|%s|%s||||%s|%s|%s|0.00||| 45~", d.id, fv("USBackOfficeId"), map[string]string{"": "", "1": "Securities", "2": "Commodities"}[d.cat],
				strconv.FormatInt(d.cash, 10)+".00", strconv.FormatInt(d.eff/10000, 10)+".00", fmt.Sprintf("%d.%04d", d.mult/10000, d.mult%10000))
		}
		say(s)
	case "PreEditUserDeposit":
		var owner string
		for u, ds := range f.deps {
			for _, d := range ds {
				if d.id == fv("UDID") {
					owner = u
				}
			}
		}
		if owner == "" {
			say("1|Deposit not found~")
			return
		}
		s := fmt.Sprintf("0||L|L~%d|~UDID|UDSequenceId|UDUSID|UDPFID|UDMarket|UDSEGMENT|UDOrderType|UDDeposit|UDCategory|UDExchange|UDDepositLevel|UDBankTransferAmount~", len(f.deps[owner]))
		for i, d := range f.deps[owner] {
			s += fmt.Sprintf("%s|%d|%s||||,1,2,|%d|%s||%s|~", d.id, 141+i, owner, d.eff, d.cat, d.level)
		}
		say(s + "~")
	case "EditUserDeposit":
		f.edits = append(f.edits, r.PostForm)
		if msg := f.refuse[fv("USLOGINID")]; msg != "" {
			say("1|" + msg + "~")
			return
		}
		n, _ := strconv.Atoi(fv("rowcount"))
		usid := f.usidOf(fv("USLOGINID"))
		if n < 1 || n > 25 || fv("UDDepositLevel0") != "0" || fv("UDCategory0") != "" {
			say("1|User level deposit is mandatory~")
			return
		}
		for i := 0; i < n; i++ {
			k := strconv.Itoa(i)
			num := func(f string) int64 { v, _ := strconv.ParseInt(fv(f+k), 10, 64); return v }
			if fv("UDUSID"+k) != usid && usid != "" {
				f.t.Errorf("row %d USID %q, want %q", i, fv("UDUSID"+k), usid)
			}
			if num("UDDeposit") != num("UDAmount1")*num("UDAmount2")/10000 || fv("UDOrderType"+k) != "1,2" {
				f.t.Errorf("row %d: deposit %d, cash %d × multiplier %d", i, num("UDDeposit"), num("UDAmount1"), num("UDAmount2"))
			}
			id := fv("UDID" + k)
			d := &fakeDep{id: id, level: fv("UDDepositLevel" + k), cat: fv("UDCategory" + k), cash: num("UDAmount1") / 10000, mult: num("UDAmount2"), eff: num("UDDeposit"), mtm: fv("UDMarkToMarketFactor" + k), mtmLim: fv("UDFIELD5" + k), adh: fv("UDADHOCMARGIN" + k)}
			key := fv("UDUSID" + k)
			switch {
			case fv("UDROWSTATE"+k) == "0":
				ds := f.deps[key][:0]
				for _, x := range f.deps[key] {
					if x.id != id {
						ds = append(ds, x)
					}
				}
				f.deps[key] = ds
			case id == "":
				d.id = strconv.Itoa(f.nextID)
				f.nextID++
				f.deps[key] = append(f.deps[key], d)
			default:
				found := false
				for i, x := range f.deps[key] {
					if x.id == id {
						f.deps[key][i], found = d, true
					}
				}
				if !found {
					say("1|Deposit ID not found~")
					return
				}
			}
		}
		say("0|Succesfully Updated~")
	default:
		say("1|Unknown request~")
	}
}

func (f *fakeBOW) dep(usid, cat string) *fakeDep {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.deps[usid] {
		if d.cat == cat {
			return d
		}
	}
	return nil
}

func (f *fakeBOW) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.paths {
		if p == name {
			n++
		}
	}
	return n
}

// bowSetup is Baljit's books (sepSetup: BJ01 ₹10,000 equity and ₹2,00,000
// commodity, BJ02 ₹50,000 equity) with a fake BOW.
func bowSetup(t *testing.T, twoFA bool) (*Ops, context.Context, *fakeBOW, *time.Time) {
	t.Helper()
	o, ctx := sepSetup(t)
	clock := time.Date(2026, 10, 29, 8, 35, 0, 0, ist)
	o.Now = func() time.Time { return clock }
	f := newFakeBOW(t)
	f.twoFA = twoFA
	srv := httptest.NewTLSServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	old, gap := bowHTTP, bowGap
	bowHTTP, bowGap = srv.Client(), 0
	t.Cleanup(func() { bowHTTP, bowGap = old, gap; bowSessions.Delete(T); bowPending.Delete(T) })
	tf := bowOTPOff
	if twoFA {
		tf = bowOTPOn
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connBOW, map[string]string{"env": "UAT", "url": srv.URL + "/stocks", "user": "ADMIN-T9065", "vendor_code": "1_LDG", "two_factor": tf, "send": "On"},
		map[string]string{"password": bowTestPass}); err != nil {
		t.Fatal(err)
	}
	return o, ctx, f, &clock
}

func bowState(t *testing.T, o *Ops, ctx context.Context, ucc string) BOWClientState {
	t.Helper()
	s, _, err := docs.Get[BOWClientState](ctx, o.Docs, T, cBOWClient, ucc)
	if err != nil {
		t.Fatalf("no BOW state for %s", ucc)
	}
	return s
}

func bowOpenWork(t *testing.T, o *Ops, ctx context.Context, key string) *WorkItem {
	t.Helper()
	ws, _ := o.WorkItems(ctx, T, "Open")
	for i := range ws {
		if ws[i].Key == key {
			return &ws[i]
		}
	}
	return nil
}

func TestBOWEncoding(t *testing.T) {
	recs := bowRecords("0|Succesfully Updated~\r\n97|A-T1|Securities||||100000.00~")
	if len(recs) != 2 || col(recs[0], 1) != "Succesfully Updated" || col(recs[1], 2) != "Securities" {
		t.Fatalf("records %q", recs)
	}
	if ok, msg := bowStatus(bowRecords("1|Invalid OTP~")); ok || msg != "Invalid OTP" {
		t.Fatalf("status %v %q", ok, msg)
	}
	if bowAmt(mustDec("100000")) != 1000000000 || bowAmt(mustDec("0.01")) != 100 || bowMultAmt(mustDec("1")) != 10000 || bowMultAmt(mustDec("2.5")) != 25000 {
		t.Fatal("scaling")
	}
	x := bowConn{secMult: mustDec("2"), comMult: mustDec("2")}
	f := bowForm(x, BOWUser{USID: "73", LoginID: "Client1-T9072"}, bowRowsFor(x, BOWLimit{Securities: mustDec("100000"), Commodities: mustDec("50000")}))
	want := map[string]string{"rowcount": "3", "USLOGINID": "Client1-T9072", "USBackOfficeId": "Client1-T9072",
		"UDID0": "", "UDUSID0": "73", "UDDepositLevel0": "0", "UDCategory0": "", "UDAmount10": "1500000000", "UDAmount20": "20000", "UDDeposit0": "3000000000", "UDROWSTATE0": "1",
		"UDDepositLevel1": "11", "UDCategory1": "1", "UDExchange1": "", "UDMarket1": "", "UDSEGMENT1": "", "UDAmount11": "1000000000", "UDDeposit1": "2000000000",
		"UDDepositLevel2": "11", "UDCategory2": "2", "UDAmount12": "500000000", "UDDeposit2": "1000000000", "UDADHOCMARGIN2": "0",
		"UDMarkToMarketFactor1": "-1", "UDFIELD51": "-1", "UDFIELD61": "1", "UDFIELD71": "0", "UDOrderType2": "1,2"}
	for k, v := range want {
		if got := f.Get(k); got != v || !f.Has(k) {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// Different multipliers: the user level is the effective sum × 1.
	x.comMult = mustDec("1")
	if f := bowForm(x, BOWUser{USID: "73", LoginID: "L"}, bowRowsFor(x, BOWLimit{Securities: mustDec("100"), Commodities: mustDec("50")})); f.Get("UDAmount10") != "2500000" || f.Get("UDAmount20") != "10000" {
		t.Fatalf("user level %v", f)
	}
	// MTM checked at 1×: the MTM limit is the deposit.
	x.mtm = mustDec("1")
	if f := bowForm(x, BOWUser{USID: "73", LoginID: "L"}, bowRowsFor(x, BOWLimit{Securities: mustDec("100")})); f.Get("UDMarkToMarketFactor1") != "10000" || f.Get("UDFIELD51") != "2000000" {
		t.Fatalf("mtm %v", f)
	}
	cs := map[string]Client{"BJ01": {}, "A-B": {}}
	for in, want := range map[string]string{"BJ01-T9065": "BJ01", "bj01": "BJ01", "A-B-T1": "A-B", "A-B": "A-B", "ZZ-T1": "", "": ""} {
		if got := bowMatch(in, cs); got != want {
			t.Errorf("match %q = %q, want %q", in, got, want)
		}
	}
	if _, err := parseBOWConn(Connection{Values: map[string]string{"user": "A", "vendor_code": "V", "sec_mult": "0"}, Secrets: map[string]string{"password": "p"}}); err == nil {
		t.Fatal("multiplier 0 taken")
	}
	if _, err := parseBOWConn(Connection{Values: map[string]string{"user": "A", "vendor_code": "V", "bod_at": "09:30"}, Secrets: map[string]string{"password": "p"}}); err == nil {
		t.Fatal("start of day after 09:00 taken")
	}
}

func TestBOWPush(t *testing.T) {
	o, ctx, f, clock := bowSetup(t, false)
	cv, err := o.TestConnection(ctx, T, "admin", connBOW)
	if err != nil || cv.Status != "Working" || !strings.Contains(cv.Problem, "exchange status") {
		t.Fatalf("test %+v %v", cv, err)
	}
	if _, err := o.BOWPush(ctx, T, "priya.nair", "", nil); err == nil {
		t.Fatal("a maker pushed limits")
	}

	// Start of day at 08:35: every active client, three rows each.
	o.bowTick(ctx, T, *clock)
	if len(f.edits) != 2 {
		t.Fatalf("%d edits at the start of the day (%v)", len(f.edits), f.paths)
	}
	sec, com, usr := f.dep("16377", "1"), f.dep("16377", "2"), f.dep("16377", "")
	if sec == nil || com == nil || usr == nil || sec.cash != 10000 || com.cash != 200000 || usr.cash != 210000 || sec.mult != 10000 || sec.level != "11" || usr.level != "0" ||
		sec.eff != 100000000 || com.eff != 2000000000 || sec.mtm != "-1" || sec.mtmLim != "-1" || sec.adh != "0" {
		t.Fatalf("BJ01 rows %+v %+v %+v", usr, sec, com)
	}
	if d := f.dep("16378", "2"); d == nil || d.cash != 0 || f.dep("16378", "1").cash != 50000 {
		t.Fatal("BJ02 rows")
	}
	if s := bowState(t, o, ctx, "BJ01"); s.Status != BOWPushed || s.USID != "16377" || s.Securities.Cmp(mustDec("10000")) != 0 || s.Commodities.Cmp(mustDec("200000")) != 0 || !strings.Contains(s.Request, "add Commodities cash ₹200000.00 × 1.0000") {
		t.Fatalf("BJ01 state %+v", s)
	}
	st, _, _ := docs.Get[bowStateDoc](ctx, o.Docs, T, cBOWState, "state")
	if st.BODDate != "2026-10-29" {
		t.Fatalf("start of day not recorded %+v", st)
	}
	n := len(f.edits)

	// 09:10: nothing changed, nothing is sent.
	*clock = clock.Add(35 * time.Minute)
	o.bowTick(ctx, T, *clock)
	if len(f.edits) != n {
		t.Fatalf("unchanged limits pushed again: %d", len(f.edits)-n)
	}

	// ₹5,000 MCX money for BJ01 by e-CMS: the sweep pushes BJ01 only, as a
	// modify of BOW's rows.
	ecmsCredit(t, o, ctx, "S1", "UTR1", sgCom, "50100011112222", "5000", "MCX MARGIN")
	*clock = clock.Add(6 * time.Minute)
	pre := f.count("PreEditUserDeposit")
	o.bowTick(ctx, T, *clock)
	if len(f.edits) != n+1 || f.edits[n].Get("USLOGINID") != "BJ01-T9065" || f.edits[n].Get("UDID0") == "" || f.edits[n].Get("UDID2") != com.id || f.count("PreEditUserDeposit") != pre+1 {
		t.Fatalf("intraday push: %d edits, %v", len(f.edits)-n, f.edits[len(f.edits)-1])
	}
	if d := f.dep("16377", "2"); d.cash != 205000 || d.id != com.id || len(f.deps["16377"]) != 3 {
		t.Fatalf("BJ01 commodities %+v (%d rows)", d, len(f.deps["16377"]))
	}

	// Equity money for BJ02 queues the client; the next tick pushes it at
	// once, before the next sweep.
	ecmsCredit(t, o, ctx, "S2", "UTR2", sgAcct, "91902033334444", "1000", "NSE FUNDS")
	if q := o.bowQueuedUCCs(ctx, T); len(q) != 1 || q[0] != "BJ02" {
		t.Fatalf("queue %v", q)
	}
	*clock = clock.Add(time.Minute)
	o.bowTick(ctx, T, *clock)
	if len(f.edits) != n+2 || f.edits[n+1].Get("USLOGINID") != "bj02-T9065" || f.dep("16378", "1").cash != 51000 {
		t.Fatalf("queued push: %d edits", len(f.edits)-n)
	}
	if q := o.bowQueuedUCCs(ctx, T); len(q) != 0 {
		t.Fatalf("queue after the push %v", q)
	}

	// BOW refuses a client: the state says so and Risk gets a work item.
	f.refuse["bj02-T9065"] = "Deposit exceeds member limit"
	if _, err := o.BOWPush(ctx, T, "anita.rao", "", []string{"BJ02"}); err != nil {
		t.Fatal(err)
	}
	if s := bowState(t, o, ctx, "BJ02"); s.Status != BOWUnchanged {
		t.Fatalf("BJ02 at BOW's values: %+v", s)
	}
	c, _ := o.Client(ctx, T, "BJ02")
	c.Blocked = true
	_ = docs.Upsert(ctx, o.Docs, T, cClient, "BJ02", c, "t")
	run, err := o.BOWPush(ctx, T, "anita.rao", "", []string{"BJ02"})
	if err != nil || run.Failed != 1 {
		t.Fatalf("run %+v %v", run, err)
	}
	if s := bowState(t, o, ctx, "BJ02"); s.Status != BOWFailed || !strings.Contains(s.Error, "Deposit exceeds member limit") {
		t.Fatalf("BJ02 %+v", s)
	}
	if w := bowOpenWork(t, o, ctx, "bowpush:BJ02"); w == nil || w.Role != RoleRisk || w.View != "live" {
		t.Fatalf("work item %+v", w)
	}
	// Taken the next time: blocked means 0 on both; the work item closes.
	delete(f.refuse, "bj02-T9065")
	if _, err := o.BOWPush(ctx, T, "anita.rao", "", []string{"BJ02"}); err != nil {
		t.Fatal(err)
	}
	if f.dep("16378", "1").cash != 0 || f.dep("16378", "2").cash != 0 || f.dep("16378", "").cash != 0 || bowOpenWork(t, o, ctx, "bowpush:BJ02") != nil {
		t.Fatal("blocked client not at 0, or the work item is open")
	}
	if s := bowState(t, o, ctx, "BJ02"); s.Why != "blocked" {
		t.Fatalf("why %+v", s)
	}

	// A client set by hand, and a closed client's rows removed.
	if _, err := o.SetBOWUser(ctx, T, "anita.rao", "BJ02", BOWUser{USID: "16390", LoginID: "ZZ99-T9065"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.BOWPush(ctx, T, "anita.rao", "", []string{"BJ02"}); err != nil {
		t.Fatal(err)
	}
	if last := f.edits[len(f.edits)-1]; last.Get("USLOGINID") != "ZZ99-T9065" || last.Get("UDUSID1") != "16390" || last.Get("UDID1") != "" {
		t.Fatalf("the hand-set user was not used: %v", last)
	}
	if _, err := o.SetBOWUser(ctx, T, "anita.rao", "BJ02", BOWUser{}); err != nil {
		t.Fatal(err)
	}
	c.Status = "Closed"
	_ = docs.Upsert(ctx, o.Docs, T, cClient, "BJ02", c, "t")
	if _, err := o.BOWPush(ctx, T, "anita.rao", "", []string{"BJ02"}); err != nil {
		t.Fatal(err)
	}
	if last := f.edits[len(f.edits)-1]; last.Get("UDROWSTATE0") != "0" || last.Get("UDID0") == "" || len(f.deps["16378"]) != 0 {
		t.Fatalf("closed client not removed: %v %d rows", last, len(f.deps["16378"]))
	}
	if s := bowState(t, o, ctx, "BJ02"); s.Status != BOWRemoved {
		t.Fatalf("BJ02 %+v", s)
	}

	// The screen, and never the password or the session key in a record.
	v, err := o.BOWLimitsView(ctx, T, "anita.rao", "2026-10-29")
	if err != nil || !v.BODDone || !v.Session.SignedIn || len(v.Runs) == 0 || v.MasterSize != 3 {
		t.Fatalf("view %+v %v", v, err)
	}
	var b1 *BOWRow
	for i := range v.Rows {
		if v.Rows[i].UCC == "BJ01" {
			b1 = &v.Rows[i]
		}
	}
	if b1 == nil || b1.Differs || b1.User == nil || b1.Limit.Commodities.Cmp(mustDec("205000")) != 0 {
		t.Fatalf("BJ01 row %+v", b1)
	}
	runs, _ := docs.List[BOWPushRun](ctx, o.Docs, T, cBOWRun, "")
	states, _ := docs.List[BOWClientState](ctx, o.Docs, T, cBOWClient, "")
	cn, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connBOW)
	all, _ := json.Marshal([]any{runs, states, v, cn.Values, cn.Problem})
	for _, secret := range []string{bowTestPass, bowTestSess} {
		if strings.Contains(string(all), secret) {
			t.Fatalf("%q in the records", secret)
		}
	}
	entries, _ := o.App.Audit.List(ctx, T, 100000)
	ab, _ := json.Marshal(entries)
	if strings.Contains(string(ab), bowTestPass) || strings.Contains(string(ab), bowTestSess) {
		t.Fatal("a secret in the audit log")
	}
	if f.logins != 1 {
		t.Fatalf("%d sign-ins for the day", f.logins)
	}
}

func TestBOWOTPAndRefusedPassword(t *testing.T) {
	o, ctx, f, clock := bowSetup(t, true)
	// Start of day without a sign-in: nothing goes, Risk is asked to sign in.
	o.bowTick(ctx, T, *clock)
	if len(f.edits) != 0 || f.logins != 0 {
		t.Fatalf("pushed without a sign-in: %d edits, %d logins", len(f.edits), f.logins)
	}
	if w := bowOpenWork(t, o, ctx, "bowpush:signin"); w == nil || w.Role != RoleRisk {
		t.Fatal("no sign-in work item")
	}
	if _, err := o.BOWSignIn(ctx, T, "anita.rao", bowTestOTP); err == nil {
		t.Fatal("an OTP taken before one was asked")
	}
	s, err := o.BOWSignIn(ctx, T, "anita.rao", "")
	if err != nil || s.SignedIn || s.OTPSentAt == "" || !strings.Contains(s.Message, "OTP has been sent") || f.otpAsked != 1 {
		t.Fatalf("ask OTP %+v %v", s, err)
	}
	if _, err := o.BOWSignIn(ctx, T, "anita.rao", "111111"); err == nil {
		t.Fatal("a wrong OTP taken")
	}
	if s, err = o.BOWSignIn(ctx, T, "anita.rao", bowTestOTP); err != nil || !s.SignedIn {
		t.Fatalf("sign in %+v %v", s, err)
	}
	// The retry waits for the check interval, then goes.
	*clock = clock.Add(time.Minute)
	o.bowTick(ctx, T, *clock)
	if len(f.edits) != 0 {
		t.Fatal("retried before the interval")
	}
	*clock = clock.Add(5 * time.Minute)
	o.bowTick(ctx, T, *clock)
	if len(f.edits) != 2 || bowOpenWork(t, o, ctx, "bowpush:signin") != nil {
		t.Fatalf("%d edits after the sign-in", len(f.edits))
	}

	// A refused password stops automatic pushes: BOW locks a login after
	// three wrong passwords, so it is never tried again on its own.
	o2, ctx2, f2, clock2 := bowSetup(t, false)
	if _, err := o2.SaveConnection(ctx2, T, "admin", connBOW, map[string]string{"env": "UAT", "url": strings.TrimSuffix(bowURL(t, o2, ctx2), "/"), "user": "ADMIN-T9065", "vendor_code": "1_LDG", "two_factor": bowOTPOff, "send": "On"},
		map[string]string{"password": "Wrong#123x"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		o2.bowTick(ctx2, T, *clock2)
		*clock2 = clock2.Add(10 * time.Minute)
	}
	if f2.logins != 1 {
		t.Fatalf("%d sign-ins with a refused password", f2.logins)
	}
	cn, _, _ := docs.Get[Connection](ctx2, o2.Docs, T, cConn, connBOW)
	if cn.Status != "Problem" || !strings.Contains(cn.Problem, "refused the sign-in") {
		t.Fatalf("connection %+v", cn)
	}
}

func bowURL(t *testing.T, o *Ops, ctx context.Context) string {
	t.Helper()
	cn, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connBOW)
	return cn.Values["url"]
}

func TestBOWLimitsBasis(t *testing.T) {
	// One ledger: everything is Securities, and the same figure as
	// Finspot's column 5.
	o, ctx := newOps(t)
	for _, c := range []Client{
		{UCC: "ON01", Name: "One", Status: "Active", Segments: []string{"CM", "COM"}},
		{UCC: "ON02", Name: "Two", Status: "Active", Segments: []string{"CM"}},
		{UCC: "ON03", Name: "Three", Status: "Active", Segments: []string{"CM"}, Inactive: true},
	} {
		if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "t"); err != nil {
			t.Fatal(err)
		}
	}
	csv := "Account,Debit,Credit\nCLIENT:ON01,,70000.00\nCLIENT:ON02,2500.00,\nCLIENT:ON03,,900.00\nEQUITY:OPENING,68400.00,\n"
	if _, err := o.LoadFile(ctx, T, "admin", "OPENING-BALANCES", "2026-10-15", "opening.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	lims, err := o.bowLimits(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	fs, _ := o.finspotFunds(ctx, T, D)
	cs, _ := o.ClientMap(ctx, T)
	checked := 0
	for _, u := range sortedKeys(cs) {
		c, l := cs[u], lims[u]
		if c.Status != "Active" || c.Blocked || c.Inactive || len(c.OpenSegments()) == 0 {
			continue
		}
		if !l.Commodities.IsZero() || l.Securities.Cmp(money.Max(fs[u], money.Dec{})) != 0 {
			t.Errorf("%s: %+v, Finspot %s", u, l, fs[u])
		}
		checked++
	}
	if checked < 2 || lims["ON01"].Securities.Cmp(mustDec("70000")) != 0 || !lims["ON02"].Securities.IsZero() || !lims["ON03"].Securities.IsZero() || lims["ON03"].Why == "" {
		t.Fatalf("%d checked: %+v %+v %+v", checked, lims["ON01"], lims["ON02"], lims["ON03"])
	}
	keys := sortedKeys(lims)
	if !sort.StringsAreSorted(keys) || len(keys) != len(cs) {
		t.Fatal("limits for every client")
	}
}

// One ledger with an MCX-only bank account (mcxearmark.go): MCX money is
// Commodities, the rest Securities, during the day and at the next start
// of day alike, and the BOW commodity funds file says the same.
func TestBOWLimitsEarmark(t *testing.T) {
	o, ctx := emSetup(t, true)
	o.Now = func() time.Time { return time.Date(2026, 10, 29, 11, 0, 0, 0, ist) }
	ecmsCredit(t, o, ctx, "S1", "UTR1", sgAcct, "91902033334444", "3000", "NSE FUNDS")
	want := func(date, ucc, sec, com string) {
		t.Helper()
		lims, err := o.bowLimits(ctx, T, date)
		if err != nil {
			t.Fatal(err)
		}
		if l := lims[ucc]; l.Securities.Cmp(mustDec(sec)) != 0 || l.Commodities.Cmp(mustDec(com)) != 0 {
			t.Fatalf("%s %s: Securities %s Commodities %s, want %s %s", date, ucc, l.Securities, l.Commodities, sec, com)
		}
	}
	want(sgD0, "BJ01", "10000", "200000") // the MCX money came in today
	want(sgD0, "BJ02", "53000", "0")      // equity money today
	next := "2026-10-30"
	want(next, "BJ01", "10000", "200000") // start of the next day
	want(next, "BJ02", "53000", "0")
	fs, _ := o.finspotFunds(ctx, T, next)
	bf, _ := o.bowFunds(ctx, T, next)
	if fs["BJ01"].Cmp(mustDec("10000")) != 0 || bf["BJ01"].Cmp(mustDec("200000")) != 0 {
		t.Fatalf("Finspot %s, BOW funds file %s", fs["BJ01"], bf["BJ01"])
	}
	if b, err := o.BOWFundsFile(ctx, T, next); err != nil || !strings.Contains(string(b), "BJ01") {
		t.Fatalf("BOW funds file %q %v", b, err)
	}
}
