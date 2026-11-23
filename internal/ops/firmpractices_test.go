package ops

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

func fpQ(t *testing.T, o *Ops, ctx context.Context, id string) PracticeView {
	t.Helper()
	v, err := o.FirmPractices(ctx, T, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range v.Questions {
		if q.ID == id {
			return q
		}
	}
	t.Fatalf("no question %s", id)
	return PracticeView{}
}

func fpAnswer(t *testing.T, o *Ops, ctx context.Context, id, opt string, vals map[string]string) PracticeView {
	t.Helper()
	q, err := o.SetFirmPractice(ctx, T, "admin", id, PracticeAnswer{Option: opt, Values: vals})
	if err != nil {
		t.Fatalf("%s %s: %v", id, opt, err)
	}
	return q
}

func fpApprove(t *testing.T, o *Ops, ctx context.Context, id string) PracticeView {
	t.Helper()
	q, err := o.DecideFirmPractice(ctx, T, "vikram.shah", id, true, "")
	if err != nil {
		t.Fatalf("approve %s: %v", id, err)
	}
	return q
}

// Each question reads the real setting, and an answer writes it: a change
// made elsewhere shows on the page, and an answer changes the setting.
func TestFirmPracticesMapToSettings(t *testing.T) {
	o, ctx := setup(t)
	v, err := o.FirmPractices(ctx, T, "admin")
	if err != nil || v.Total != len(practiceDefs) || len(v.Questions) != len(practiceDefs) || v.Answered != 0 {
		t.Fatalf("view %d %d %v", v.Total, v.Answered, err)
	}
	if _, err := o.FirmPractices(ctx, T, "priya.nair"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a maker read firm practices: %v", err)
	}
	want := map[string]string{"limit-sharing": "same", "kyc-blocks": "on", "finspot-logins": FinspotLoginsConsole, "finspot-baskets": "off", "cuspa-api": "off",
		"mcx-apart": "off", "bank-auth": BankAuthBoth, "auction-rate": AuctionRateAlways, "dp-pledge": "all", "account-opening": ""}
	money := map[string]bool{"limit-sharing": true, "mcx-apart": true, "bank-auth": true, "auction-rate": true, "dp-pledge": true}
	for _, q := range v.Questions {
		if q.Section == SectionFirmSecurities {
			continue // firmsec_test.go
		}
		if q.Current != want[q.ID] || q.Money != money[q.ID] {
			t.Errorf("%s: current %q money %v", q.ID, q.Current, q.Money)
		}
	}
	for _, x := range fpQ(t, o, ctx, "limit-sharing").Options {
		if x.Available != (x.ID == "same") {
			t.Errorf("limit sharing %s available %v", x.ID, x.Available)
		}
	}
	for _, x := range fpQ(t, o, ctx, "finspot-baskets").Options {
		if x.ID == "on" && (x.Available || !strings.Contains(x.Note, "Connections")) {
			t.Errorf("baskets by API offered without the connection: %+v", x)
		}
	}

	// Changed elsewhere: the page shows it (one source of truth).
	set, _ := o.Settings(ctx, T)
	set.MCXMoneyApart, set.BankAuthForms, set.KYCBlockOff = true, BankAuthLetter, true
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	bs, _ := o.BrokerageSetup(ctx, T)
	bs.AuctionRate = AuctionRateScrip
	if _, err := o.SaveBrokerageSetup(ctx, T, "farhan.khan", bs); err != nil {
		t.Fatal(err)
	}
	for id, w := range map[string]string{"mcx-apart": "on", "bank-auth": BankAuthLetter, "kyc-blocks": "off", "auction-rate": AuctionRateScrip} {
		if q := fpQ(t, o, ctx, id); q.Current != w {
			t.Errorf("%s reads %q, want %q", id, q.Current, w)
		}
	}

	// Not money: applied at once.
	q := fpAnswer(t, o, ctx, "kyc-blocks", "on", nil)
	if set, _ := o.Settings(ctx, T); set.KYCBlockOff || q.Current != "on" || !q.Answered || q.History[0].Status != PracticeApplied || q.History[0].Was != "off" {
		t.Fatalf("kyc on: %+v", q.History)
	}
	fpAnswer(t, o, ctx, "kyc-blocks", "off", nil)
	if set, _ := o.Settings(ctx, T); !set.KYCBlockOff {
		t.Fatal("kyc gate not switched off")
	}

	// Money: each waits, then the approval writes the real setting.
	for _, c := range []struct {
		id, opt string
		vals    map[string]string
		check   func() bool
	}{
		{"mcx-apart", "off", nil, func() bool { s, _ := o.Settings(ctx, T); return !s.MCXMoneyApart }},
		{"bank-auth", BankAuthConfirm, nil, func() bool { s, _ := o.Settings(ctx, T); return s.BankAuthForms == BankAuthConfirm }},
		{"auction-rate", AuctionRateAlways, nil, func() bool { s, _ := o.BrokerageSetup(ctx, T); return s.AuctionRate == "" }},
		{"dp-pledge", "created", nil, func() bool {
			t, _ := o.DPTariffNow(ctx, T)
			for _, l := range t.PledgeCharges {
				if (l.Code == PledgeClosed || l.Code == PledgeInvoked) && pledgeCharged(l) || l.Code == PledgeCreated && !pledgeCharged(l) {
					return false
				}
			}
			return true
		}},
	} {
		before := c.check()
		q := fpAnswer(t, o, ctx, c.id, c.opt, c.vals)
		if q.Pending == nil || q.Pending.Status != PracticeWaiting || c.check() != before || before {
			t.Fatalf("%s: applied before approval (pending %+v)", c.id, q.Pending)
		}
		q = fpApprove(t, o, ctx, c.id)
		if !c.check() || q.Current != c.opt || q.Pending != nil || q.History[0].Status != PracticeApproved || q.History[0].DecidedBy != "vikram.shah" || q.History[0].By != "admin" {
			t.Fatalf("%s after approval: current %q %+v", c.id, q.Current, q.History)
		}
	}
	// Back to charging after a pledge too, with an amount: the closure and
	// invocation lines take it.
	fpAnswer(t, o, ctx, "dp-pledge", "all", map[string]string{"charge": "25"})
	fpApprove(t, o, ctx, "dp-pledge")
	tf, _ := o.DPTariffNow(ctx, T)
	got := map[string]DPChargeLine{}
	for _, l := range tf.PledgeCharges {
		got[l.Code] = l
	}
	if l := got[PledgeInvoked]; !pledgeCharged(l) || l.Flat != 2500 || !pledgeCharged(got[PledgeClosed]) || got[PledgeCreated].Flat != 2500 {
		t.Fatalf("pledge lines %+v", tf.PledgeCharges)
	}
	if q := fpQ(t, o, ctx, "dp-pledge"); q.Current != "all" || q.Values["charge"] != "25.00" {
		t.Fatalf("dp pledge reads %q %v", q.Current, q.Values)
	}

	// Confirming what is in force needs no approval, even for money.
	if q := fpAnswer(t, o, ctx, "mcx-apart", "off", nil); q.Pending != nil || q.History[0].Status != PracticeConfirmed {
		t.Fatalf("confirm %+v", q.History[0])
	}
	// A recorded-only question keeps its answer here.
	q = fpAnswer(t, o, ctx, "account-opening", "own", map[string]string{"fee": "500"})
	if q.Current != "own" || q.Values["fee"] != "500" {
		t.Fatalf("account opening %+v", q)
	}
	if _, err := o.SetFirmPractice(ctx, T, "admin", "account-opening", PracticeAnswer{Option: "own", Values: map[string]string{"fee": "lots"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a fee that is not an amount: %v", err)
	}
	if _, err := o.SetFirmPractice(ctx, T, "admin", "account-opening", PracticeAnswer{Option: "own", Values: map[string]string{"colour": "x"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a field the question does not have: %v", err)
	}
}

// Other records the firm's practice, keeps the behaviour, and gives the
// Admin a work item and a go-live checklist line until an option is chosen.
func TestFirmPracticeOther(t *testing.T) {
	o, ctx := setup(t)
	if _, err := o.SetFirmPractice(ctx, T, "admin", "bank-auth", PracticeAnswer{Option: PracticeOther}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Other without the practice: %v", err)
	}
	q, err := o.SetFirmPractice(ctx, T, "admin", "bank-auth", PracticeAnswer{Option: PracticeOther, Other: "The client signs the change in person at the branch"})
	if err != nil {
		t.Fatal(err)
	}
	set, _ := o.Settings(ctx, T)
	if set.BankAuthForms != "" || q.Current != BankAuthBoth || q.Answer != PracticeOther || q.Pending != nil || q.History[0].Status != PracticeRecorded {
		t.Fatalf("Other changed the behaviour or waits: %+v", q)
	}
	w := fnOpenWork(o, ctx, "practice:bank-auth|1")
	if w == nil || w.Role != RoleAdmin || w.View != "practices" || !strings.Contains(w.Detail, "in person") {
		t.Fatalf("work item %+v", w)
	}
	cl, _ := o.GoLiveChecklist(ctx, T, "admin")
	seen := false
	for _, it := range cl {
		if it.Key == "practice_bank-auth" {
			seen = !it.OK && strings.Contains(it.Detail, "Other")
		}
	}
	if !seen {
		t.Fatalf("checklist %+v", cl)
	}
	// Choosing an option (here the one in force) closes the work item.
	q = fpAnswer(t, o, ctx, "bank-auth", BankAuthBoth, nil)
	if fnOpenWork(o, ctx, "practice:bank-auth|1") != nil || q.Answer != BankAuthBoth || q.Other != "" {
		t.Fatalf("still open after answering: %+v", q)
	}
}

// Who may answer and approve: an Admin answers; a change that affects money
// or limits waits for the compliance officer or another Admin, never the
// proposer; options not built or without their connection are refused.
func TestFirmPracticeApprovalRule(t *testing.T) {
	o, ctx := setup(t)
	for _, who := range []string{"vikram.shah", "farhan.khan", "priya.nair"} {
		if _, err := o.SetFirmPractice(ctx, T, who, "kyc-blocks", PracticeAnswer{Option: "on"}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s answered: %v", who, err)
		}
	}
	if _, err := o.SetFirmPractice(ctx, T, "admin", "limit-sharing", PracticeAnswer{Option: "move"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an option not built yet: %v", err)
	}
	if _, err := o.SetFirmPractice(ctx, T, "admin", "finspot-baskets", PracticeAnswer{Option: "on"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("baskets by API without the connection: %v", err)
	}
	if _, err := o.SetFirmPractice(ctx, T, "admin", "nonsense", PracticeAnswer{Option: "on"}); err == nil {
		t.Fatal("an unknown question")
	}
	off := func() bool { s, _ := o.Settings(ctx, T); return !s.MCXMoneyApart }
	fpAnswer(t, o, ctx, "mcx-apart", "on", nil)
	if !off() {
		t.Fatal("applied before approval")
	}
	if fnOpenWork(o, ctx, "practice:mcx-apart|approve|1") == nil {
		t.Fatal("no work item for the approver")
	}
	if _, err := o.SetFirmPractice(ctx, T, "admin", "mcx-apart", PracticeAnswer{Option: "off"}); !errors.Is(err, ErrState) {
		t.Fatalf("a second change while one waits: %v", err)
	}
	if _, err := o.DecideFirmPractice(ctx, T, "admin", "mcx-apart", true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the proposer approved: %v", err)
	}
	if _, err := o.DecideFirmPractice(ctx, T, "farhan.khan", "mcx-apart", true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an ops checker approved: %v", err)
	}
	if _, err := o.DecideFirmPractice(ctx, T, "vikram.shah", "mcx-apart", false, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rejected without a reason: %v", err)
	}
	q, err := o.DecideFirmPractice(ctx, T, "vikram.shah", "mcx-apart", false, "Wait until RMS has moved the balances")
	if err != nil || q.Pending != nil || q.History[0].Status != PracticeRejected || !off() || q.Answered {
		t.Fatalf("reject %+v %v", q, err)
	}
	if fnOpenWork(o, ctx, "practice:mcx-apart|approve|1") != nil {
		t.Fatal("approval item still open")
	}
	fpAnswer(t, o, ctx, "mcx-apart", "on", nil)
	if q, err := o.DecideFirmPractice(ctx, T, "admin", "mcx-apart", false, ""); err != nil || q.History[0].Status != PracticeWithdrawn || !off() {
		t.Fatalf("withdraw %+v %v", q, err)
	}
	if _, err := o.DecideFirmPractice(ctx, T, "vikram.shah", "mcx-apart", true, ""); !errors.Is(err, ErrState) {
		t.Fatalf("approved with nothing waiting: %v", err)
	}
	fpAnswer(t, o, ctx, "mcx-apart", "on", nil)
	q = fpApprove(t, o, ctx, "mcx-apart")
	if off() || !q.Answered || len(q.History) != 3 || q.History[0].DecidedBy != "vikram.shah" {
		t.Fatalf("approve %+v", q.History)
	}
	// Not money: no approval.
	if q := fpAnswer(t, o, ctx, "kyc-blocks", "on", nil); q.Pending != nil {
		t.Fatal("kyc blocks waited for approval")
	}
	// The go-live checklist lists every question until all are answered.
	n := 0
	cl, _ := o.GoLiveChecklist(ctx, T, "vikram.shah")
	for _, it := range cl {
		if strings.HasPrefix(it.Key, "practice_") && !it.OK {
			n++
		}
	}
	// Eight general questions and where the firm keeps its own securities
	// (the rest of Firm securities waits for that answer).
	if n != 9 || o.practicesOpen(ctx, T) != 9 {
		t.Fatalf("%d open on the checklist, %d open", n, o.practicesOpen(ctx, T))
	}
	for _, d := range practiceDefs {
		if q := fpQ(t, o, ctx, d.ID); !q.Answered {
			fpAnswer(t, o, ctx, d.ID, firstOf(q.Current, d.Options[0].ID), nil)
		}
	}
	cl, _ = o.GoLiveChecklist(ctx, T, "admin")
	for _, it := range cl {
		if strings.HasPrefix(it.Key, "practice") && (!it.OK || it.Key != "practices") {
			t.Fatalf("after every answer: %+v", it)
		}
	}
}

// ---- Finspot logins in each mode, with the fake Noren server ----

func fpFinspot(t *testing.T, o *Ops, ctx context.Context) (*fakeNoren, string, []string) {
	t.Helper()
	f, base := fnFake(t)
	fnSave(t, o, ctx, base, map[string]string{"clients": "On"})
	o.SaveConnection(ctx, T, "admin", connFinspot, map[string]string{"exchange_users": "NSE=9001:100000000001; BSE=9002:1000000000002; NFO=9003:100000000003; BFO=9004:100000000004; CDS=9005:100000000005; BCD=9006:100000000006; MCX=9007:100000000007:31/12/2027", "segment_expiry": "30/06/2027"}, nil)
	cs, _ := o.tradingClientMap(ctx, T)
	var ok []string
	for _, k := range sortedKeys(cs) {
		c, _ := o.Client(ctx, T, k)
		if c.DOB == "" {
			c.DOB = "1984-03-0" + strconv.Itoa(1+len(k)%9)
			_ = docs.Upsert(ctx, o.Docs, T, cClient, k, c, "t")
		}
		if c.Status == "Active" && finspotBlockWhy(c) == "" && c.Email != "" && c.Mobile != "" && c.PAN != "" && len(k) <= 10 && !c.DPOnly {
			ok = append(ok, k)
		}
	}
	if len(ok) < 3 {
		t.Fatalf("only %d clients Finspot can take", len(ok))
	}
	return f, base, ok
}

func fnAdds(f *fakeNoren, act string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, jd := range f.reqs["AddInvestorClient"] {
		if jd["actid"] == act {
			n++
		}
	}
	return n
}

func fnLogin(t *testing.T, o *Ops, ctx context.Context, u string) FinspotLoginView {
	t.Helper()
	v, err := o.FinspotLoginGet(ctx, T, "priya.nair", u)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFinspotLoginsConsole(t *testing.T) {
	o, ctx := setup(t)
	f, _, ok := fpFinspot(t, o, ctx)
	set, _ := o.Settings(ctx, T)
	next := set.nextBusinessDay(D)
	if q := fpQ(t, o, ctx, "finspot-logins"); q.Current != FinspotLoginsLedgerline {
		t.Fatalf("client logins On reads %q", q.Current)
	}
	fpAnswer(t, o, ctx, "finspot-logins", FinspotLoginsConsole, nil)
	if c, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connFinspotAPI); c.Values["clients"] != "Off" || c.Status == "Not set" || c.Secrets["password"] == "" {
		t.Fatalf("connection %v %s", c.Values, c.Status)
	}
	if set, _ := o.Settings(ctx, T); set.FinspotLogins != FinspotLoginsConsole {
		t.Fatalf("setting %q", set.FinspotLogins)
	}
	// Even a push of the client part creates no login.
	run, err := o.finspotAPIPush(ctx, T, SystemActor, next, "Intraday", []string{FNPartClients}, nil, false)
	if err != nil || f.count("AddInvestorClient") != 0 || run.Parts[0].Skipped < len(ok) || !strings.Contains(run.Parts[0].Note, "admin console") {
		t.Fatalf("console mode pushed: %+v %v", run.Parts, err)
	}
	u, v := ok[0], ok[1]
	if _, _, err := o.FinspotLoginCreate(ctx, T, "priya.nair", u); !errors.Is(err, ErrState) {
		t.Fatalf("created in console mode: %v", err)
	}
	// A login made in the console is found by a check; a client Finspot
	// does not know is not recorded.
	f.mu.Lock()
	f.exists[u] = true
	f.mu.Unlock()
	lv, err := o.FinspotLoginCheck(ctx, T, "priya.nair", u)
	if err != nil || !lv.Has || lv.How != FinspotLoginChecked || lv.CheckedAt == "" {
		t.Fatalf("check %+v %v", lv, err)
	}
	lv, err = o.FinspotLoginCheck(ctx, T, "farhan.khan", v)
	if err != nil || lv.Has || !strings.Contains(lv.Note, "did not find") {
		t.Fatalf("check of an unknown client %+v %v", lv, err)
	}
	if _, err := o.FinspotLoginCheck(ctx, T, "vikram.shah", v); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance checked: %v", err)
	}
	// Staff record one made in the console without the API.
	lv, err = o.FinspotLoginMark(ctx, T, "priya.nair", v, "made by the RMS desk")
	if err != nil || !lv.Has || lv.How != FinspotLoginMarked || lv.By != "priya.nair" {
		t.Fatalf("mark %+v %v", lv, err)
	}
	if _, err := o.FinspotLoginMark(ctx, T, "priya.nair", v, ""); !errors.Is(err, ErrState) {
		t.Fatalf("marked twice: %v", err)
	}
}

func TestFinspotLoginsLedgerline(t *testing.T) {
	o, ctx := setup(t)
	f, _, ok := fpFinspot(t, o, ctx)
	set, _ := o.Settings(ctx, T)
	next := set.nextBusinessDay(D)
	fpAnswer(t, o, ctx, "finspot-logins", FinspotLoginsConsole, nil)
	fpAnswer(t, o, ctx, "finspot-logins", FinspotLoginsLedgerline, nil)
	if c, _, _ := docs.Get[Connection](ctx, o.Docs, T, cConn, connFinspotAPI); c.Values["clients"] != "On" {
		t.Fatalf("connection %v", c.Values)
	}
	u, v := ok[0], ok[1] // u made in Finspot's console; v recorded by staff
	f.mu.Lock()
	f.exists[u] = true
	f.mu.Unlock()
	if _, err := o.FinspotLoginMark(ctx, T, "farhan.khan", v, ""); err != nil {
		t.Fatal(err)
	}
	run, err := o.finspotAPIPush(ctx, T, SystemActor, next, "Intraday", []string{FNPartClients}, nil, false)
	if err != nil || run.Parts[0].Pushed == 0 {
		t.Fatalf("push %+v %v", run.Parts, err)
	}
	if lv := fnLogin(t, o, ctx, u); !lv.Has || lv.How != FinspotLoginFound {
		t.Fatalf("console login not recognised: %+v", lv)
	}
	if fnAdds(f, v) != 0 {
		t.Fatal("a login staff recorded was created again")
	}
	created := 0
	for _, k := range ok[2:] {
		if lv := fnLogin(t, o, ctx, k); lv.Has && lv.How == FinspotLoginByAPI {
			created++
		}
	}
	if created == 0 {
		t.Fatal("no login created by API")
	}
	// Never twice: a later push by hand changes, never adds.
	adds := fnAdds(f, u) + fnAdds(f, ok[2])
	if _, err := o.FinspotAPIPush(ctx, T, "anita.rao", next, []string{FNPartClients}, nil); err != nil {
		t.Fatal(err)
	}
	if fnAdds(f, u)+fnAdds(f, ok[2]) != adds {
		t.Fatal("a client with a login was added again")
	}
	if p, _ := o.FinspotAPIPanel(ctx, T, "anita.rao", next); p.LoginMode != FinspotLoginsLedgerline {
		t.Fatalf("panel mode %q", p.LoginMode)
	}
}

func TestFinspotLoginsBoth(t *testing.T) {
	o, ctx := setup(t)
	f, _, ok := fpFinspot(t, o, ctx)
	set, _ := o.Settings(ctx, T)
	next := set.nextBusinessDay(D)
	fpAnswer(t, o, ctx, "finspot-logins", FinspotLoginsBoth, nil)
	// New clients wait: for the console, or for Create Finspot login.
	run, err := o.finspotAPIPush(ctx, T, SystemActor, next, "Intraday", []string{FNPartClients}, nil, false)
	if err != nil || f.count("AddInvestorClient") != 0 || !strings.Contains(run.Parts[0].Note, "Create Finspot login") {
		t.Fatalf("both mode created logins by itself: %+v %v", run.Parts, err)
	}
	u, v, w := ok[0], ok[1], ok[2]
	if lv := fnLogin(t, o, ctx, u); !lv.CanCreate || lv.Has {
		t.Fatalf("create not offered: %+v", lv)
	}
	if _, _, err := o.FinspotLoginCreate(ctx, T, "vikram.shah", u); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance created a login: %v", err)
	}
	lv, _, err := o.FinspotLoginCreate(ctx, T, "priya.nair", u)
	if err != nil || !lv.Has || lv.How != FinspotLoginByAPI || fnAdds(f, u) != 1 || lv.Requested {
		t.Fatalf("create %+v %v (%d adds)", lv, err, fnAdds(f, u))
	}
	if _, _, err := o.FinspotLoginCreate(ctx, T, "priya.nair", u); !errors.Is(err, ErrState) || fnAdds(f, u) != 1 {
		t.Fatalf("created twice: %v", err)
	}
	// Made in the console a moment before: Finspot says it exists, so it
	// is changed and recorded, never duplicated.
	f.mu.Lock()
	f.exists[v] = true
	f.mu.Unlock()
	mods := f.count("ModifyClient")
	lv, _, err = o.FinspotLoginCreate(ctx, T, "priya.nair", v)
	if err != nil || !lv.Has || lv.How != FinspotLoginFound || f.count("ModifyClient") != mods+1 {
		t.Fatalf("console login %+v %v", lv, err)
	}
	// The others still wait.
	o.finspotAPIPush(ctx, T, SystemActor, next, "Intraday", []string{FNPartClients}, nil, false)
	if fnAdds(f, w) != 0 {
		t.Fatal("a client nobody asked for was created")
	}
}
