package ops

import (
	"encoding/json"
	"strings"
	"testing"

	"ledgerline/internal/billing"
	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// The weekly statement: the week Monday to Saturday, balances as on the
// Saturday, every recent trader (a zero balance too), the open items and
// the clauses (NSE/INSP/73976 item 4.5).
func TestWeeklyStatementWeek(t *testing.T) {
	if p := lastSaturday("2026-10-19"); p != "2026-10-17" {
		t.Fatalf("last Saturday %s", p)
	}
	o, ctx := notesReady(t)
	tk, _ := periodicTask("funds_securities")
	if p, label, due := o.periodFor(ctx, T, tk, "2026-10-19"); p != "2026-10-17" || !strings.HasPrefix(label, "12-Oct-2026") || due != "2026-10-22" {
		t.Fatalf("period %s %s %s", p, label, due)
	}
	if _, err := o.SendStatements(ctx, T, "priya.nair", DocFundsSec, "2026-10-16"); err == nil {
		t.Fatal("a Friday accepted as the week's end")
	}
	// A recent trader with nothing this week still gets one.
	cs, _ := o.Clients(ctx, T)
	var quiet Client
	for _, c := range cs {
		if c.Status == "Active" {
			if due, _ := o.fundsSecuritiesDue(ctx, T, c.UCC, "2026-10-12", "2026-10-17", nil); !due {
				quiet = c
				break
			}
		}
	}
	if quiet.UCC != "" {
		_, _ = docs.Update(ctx, o.Docs, T, cClient, quiet.UCC, "test", func(c *Client) error { c.LastTrade = "2026-09-01"; return nil })
	}
	res, err := o.SendStatements(ctx, T, "priya.nair", DocFundsSec, "2026-10-17")
	if err != nil || res.Sent == 0 || len(res.Errors) > 0 {
		t.Fatalf("weekly %+v %v", res, err)
	}
	if quiet.UCC != "" && !o.delivered(ctx, T, DocFundsSec, "2026-10-12 to 2026-10-17", quiet.UCC) {
		t.Fatalf("zero-balance recent trader %s not sent", quiet.UCC)
	}
	u := siTrader(t, o, ctx).UCC
	rep, err := o.ClientReportFor(ctx, T, "funds_securities", u, "2026-10-12", "2026-10-17")
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]bool{}
	for _, s := range rep.Sections {
		titles[strings.SplitN(s.Title, " (", 2)[0]] = true
	}
	for _, want := range []string{"Balance as on 17-Oct-2026", "Pending obligations", "Uncleared cheques", "Securities with the broker", "Funds movement"} {
		if !titles[want] {
			t.Fatalf("no %q section: %v", want, titles)
		}
	}
	notes := strings.Join(rep.Notes, " ")
	for _, want := range []string{"Daily margin statement", "Running account", "freeze"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("clause %q missing: %s", want, notes)
		}
	}
}

// The RAS statement says disputes are raised within 30 working days.
func TestRASDisputeNotice(t *testing.T) {
	o, ctx := setup(t)
	u := siTrader(t, o, ctx).UCC
	rep, err := o.rasStatement(ctx, T, RASBatch{Date: D, AsOf: D}, RASItem{UCC: u})
	if err != nil || !strings.Contains(strings.Join(rep.Notes, " "), "30 working days") {
		t.Fatalf("%v %v", rep.Notes, err)
	}
}

// The year's AGTS rows and the signed annual global statement.
func TestAnnualGlobalStatement(t *testing.T) {
	for d, want := range map[string]string{"2027-04-01": "2026-27", "2027-03-31": "2025-26", "2026-11-26": "2025-26"} {
		if got := lastEndedFY(d); got != want {
			t.Fatalf("%s: %s, want %s", d, got, want)
		}
	}
	if annualDue("2026-27") != "2027-04-30" {
		t.Fatal(annualDue("2026-27"))
	}
	o, ctx := notesReady(t)
	u := siTrader(t, o, ctx).UCC
	p, err := o.YearEnd(ctx, T, u, "2026-27", D)
	if err != nil || len(p.AGTS) == 0 {
		t.Fatalf("pack %+v %v", p.AGTS, err)
	}
	r := p.AGTS[0]
	if r.Exchange == "" || r.Segment == "" || r.Security == "" || (r.BuyQty == 0 && r.SellQty == 0) {
		t.Fatalf("row %+v", r)
	}
	html, err := RenderYearEnd(p)
	if err != nil || !strings.Contains(string(html), "Annual Global Transaction Statement") || !strings.Contains(string(html), r.Security) {
		t.Fatalf("render %v", err)
	}
	rep, err := o.AnnualStatement(ctx, T, "2026-27", u)
	if err != nil {
		t.Fatal(err)
	}
	last := rep.Sections[len(rep.Sections)-1]
	if last.Title != "Annual Global Transaction Statement" || len(last.Rows) == 0 || len(last.Columns) != 13 {
		t.Fatalf("annual %+v", last)
	}
	d, err := o.sendAnnualStatement(ctx, T, "priya.nair", "2026-27", u)
	if err != nil || d.Kind != DocAnnual || len(d.Files) == 0 {
		t.Fatalf("dispatch %+v %v", d, err)
	}
	// After 30 April the self-inspection wants it for every trader.
	siClock(o, siAt("2027-05-03", "23:00"))
	s := siOne(t, o, ctx, "annual_statement", "2027-05-03")
	if siState(s) == SISkipped {
		t.Fatalf("skipped: %s", s.skip)
	}
}

// DP statements: three returned in a row stop them until the address is
// updated; a closed account's statement is marked and final.
func TestDPStatementReturns(t *testing.T) {
	o, ctx := setup(t)
	if err := o.putBO(ctx, T, "admin", BOAccount{UCC: "BH1003", BOID: "1208160000000777", Depository: "CDSL", Status: "Active", Opened: "2024-01-01"}); err != nil {
		t.Fatal(err)
	}
	var bos []BOAccount
	for _, b := range o.boAccounts(ctx, T) {
		if b.BOID == "1208160000000777" {
			bos = append(bos, b)
		}
	}
	if len(bos) == 0 {
		t.Fatal("no DP account")
	}
	u := bos[0].UCC
	for i, m := range []string{"2026-07", "2026-08", "2026-09"} {
		id := "DSP-R" + string(rune('1'+i))
		siPut(t, o, ctx, cDispatch, id, Dispatch{ID: id, Key: DocDP + "|" + m + "|" + u, Kind: DocDP, Ref: m, Date: monthEnd(m), UCC: u, Channel: "physical", Status: "Physical sent", Courier: "Test courier 1"})
		if _, err := o.MarkReturned(ctx, T, "priya.nair", id, "addressee moved"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.MarkReturned(ctx, T, "priya.nair", "DSP-R1", "again"); err == nil {
		t.Fatal("returned twice")
	}
	if !o.dpStatementStopped(ctx, T, u) {
		t.Fatal("not stopped after three returns")
	}
	res, err := o.SendStatements(ctx, T, "priya.nair", DocDP, "2026-10")
	if err != nil || res.Withheld == 0 {
		t.Fatalf("not withheld %+v %v", res, err)
	}
	ws, _ := docs.List[WorkItem](ctx, o.Docs, T, cWorkItem, "")
	follow := false
	for _, w := range ws {
		follow = follow || strings.HasPrefix(w.Key, "dispatch:address:"+u)
	}
	if !follow {
		t.Fatal("no address follow-up")
	}
	// An approved address change lifts it.
	siPut(t, o, ctx, cChange, "CHG-T1", ChangeRequest{ID: "CHG-T1", UCC: u, Kind: "Address", Status: "Approved", At: "2099-01-01T00:00:00Z", Decided: "2099-01-01T00:00:00Z"})
	if o.dpStatementStopped(ctx, T, u) {
		t.Fatal("still stopped after the address update")
	}
	// The closing statement.
	b := bos[0]
	b.Status, b.ClosedOn = "Closed", "2026-10-10"
	if err := o.putBO(ctx, T, "admin", b); err != nil {
		t.Fatal(err)
	}
	if n := len(o.boAccountsOf(ctx, T, u)); n != 1 {
		t.Fatalf("%d accounts", n)
	}
	rep, err := o.DPStatement(ctx, T, "2026-10", u)
	if err != nil || !strings.Contains(rep.Title, "ACCOUNT CLOSED") || !strings.Contains(strings.Join(rep.Notes, " "), "final statement") {
		t.Fatalf("closing statement %q %v %v", rep.Title, rep.Notes, err)
	}
}

// Every nomination action is acknowledged and entered in the numbered
// register; clients without a choice are reminded.
func TestNominationAcknowledgements(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	u := cs[0].UCC
	ns, _ := json.Marshal([]Nominee{{Name: "Meena Rao", Relation: "Spouse", Share: "100"}})
	ch, err := o.RequestChange(ctx, T, "priya.nair", u, "Nomination", map[string]string{"nominee": "Opted in", "nominees": string(ns), "mode": "Signed form"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if ch.Status == "Pending" {
		if _, err := o.DecideChange(ctx, T, "farhan.khan", ch.ID, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	ms, _ := docs.List[Message](ctx, o.Docs, T, cMessage, "")
	acks := 0
	for _, m := range ms {
		if m.Event == evNominationAk && m.UCC == u {
			acks++
		}
	}
	if acks < 2 {
		t.Fatalf("%d acknowledgements, want one on receipt and one on recording", acks)
	}
	es, _ := o.NominationRegister(ctx, T)
	if len(es) == 0 || es[len(es)-1].UCC != u || !strings.HasPrefix(es[len(es)-1].No, "NOM-") || es[len(es)-1].Ref != ch.ID {
		t.Fatalf("register %+v", es)
	}
	rows, err := o.ReportRows(ctx, T, D, "nomination_register")
	if err != nil || len(rows) < 2 || rows[0][0] != "SerialNo" {
		t.Fatalf("report %v %v", rows, err)
	}
	// Reminders go to clients with no declaration.
	_, _ = docs.Update(ctx, o.Docs, T, cClient, cs[1].UCC, "test", func(c *Client) error {
		c.Status, c.Nominee, c.Nominees, c.NominationOn = "Active", "Opted out", nil, ""
		return nil
	})
	n, err := o.sendNominationReminders(ctx, T, SystemActor, "2026-10-16")
	if err != nil || n == 0 {
		t.Fatalf("reminders %d %v", n, err)
	}
}

// A complaint gets its ticket number and the escalation matrix, and the
// ODR notice once its 21 days pass unresolved.
func TestComplaintNotices(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	set.Escalation = []EscalationContact{{Level: "Customer care", Name: "Help desk", Email: "care@example.invalid", Phone: "022-0000", Hours: "9 to 6"}}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(escalationText(set), "Customer care: Help desk, care@example.invalid") {
		t.Fatal(escalationText(set))
	}
	u := siTrader(t, o, ctx).UCC
	tk, err := o.OpenTicket(ctx, T, "priya.nair", Ticket{UCC: u, Category: TicketCategories[0], Subject: "Funds not received"}, "My payout has not come")
	if err != nil {
		t.Fatal(err)
	}
	ms, _ := docs.List[Message](ctx, o.Docs, T, cMessage, "")
	ok := false
	for _, m := range ms {
		if m.Event == "ticket.opened" && m.UCC == u {
			ok = strings.Contains(m.Body, tk.ID) && strings.Contains(m.Body, "care@example.invalid") && strings.Contains(m.Body, "SMART ODR")
		}
	}
	if !ok {
		t.Fatal("acknowledgement lacks the ticket number, escalation matrix or ODR")
	}
	later := addDays(tk.Due, 1)
	if n := o.odrNotices(ctx, T, tk.Due); n != 0 {
		t.Fatal("notice before the 21 days ran out")
	}
	if n := o.odrNotices(ctx, T, later); n != 1 {
		t.Fatalf("%d notices", n)
	}
	if n := o.odrNotices(ctx, T, addDays(later, 1)); n != 0 {
		t.Fatal("notice sent twice")
	}
	siClock(o, siAt(later, "23:00"))
	siWant(t, siOne(t, o, ctx, "complaints_odr", later), SIPass, "notice sent")
}

// Referral rewards: only a registered AP earns one; client referrers are
// recorded and stop earning from the cut-off.
func TestReferralAPOnly(t *testing.T) {
	o, ctx := setup(t)
	if err := o.seedSampleBrokerage(ctx, T, D); err != nil {
		t.Fatal(err)
	}
	if err := o.seedSamplePartners(ctx, T); err != nil {
		t.Fatal(err)
	}
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBilling(ctx, T, "farhan.khan", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	// Before the cut-off the sample's client referrer earns as before.
	rs, _ := o.Referrals(ctx, T, D, D)
	if len(rs) != 1 || rs[0].Kind != ReferrerClient || rs[0].Reward == 0 {
		t.Fatalf("before %+v", rs)
	}
	set, _ := o.Settings(ctx, T)
	set.ReferralAPOnlyFrom = D
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	rs, _ = o.Referrals(ctx, T, D, D)
	if len(rs) != 1 || rs[0].Reward != 0 || rs[0].Status != "No reward (client referrer)" {
		t.Fatalf("after %+v", rs)
	}
	if n, _ := o.PostReferrals(ctx, T, "farhan.khan", D, D); n != 0 {
		t.Fatal("client referrer paid after the cut-off")
	}
	if n := o.flagClientReferrers(ctx, T, D); n != 1 {
		t.Fatalf("%d client referrers flagged", n)
	}
	// Only a registered AP may be a paid referrer.
	if _, err := o.SavePartner(ctx, T, "admin", Partner{Code: "RMREF", Name: "An RM", Kind: "Relationship manager", Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SavePartner(ctx, T, "admin", Partner{Code: "APREF", Name: "Registered AP", Kind: RoleAP, Active: true, RegNo: "AP0001234", SharePct: money.MustDec("40")}); err != nil {
		t.Fatal(err)
	}
	cs, _ := o.ClientMap(ctx, T)
	var target string
	for _, r := range rs {
		target = r.Referred[0]
	}
	c := cs[target]
	if _, err := o.AssignClient(ctx, T, "farhan.khan", target, c.Branch, c.Partner, c.RM, "RMREF", D); err == nil {
		t.Fatal("an RM accepted as a paid referrer")
	}
	got, err := o.AssignClient(ctx, T, "farhan.khan", target, c.Branch, c.Partner, c.RM, "APREF", D)
	if err != nil || got.ReferrerKind != ReferrerAP {
		t.Fatalf("AP referrer %+v %v", got.ReferrerKind, err)
	}
	rs, _ = o.Referrals(ctx, T, D, D)
	var ap ReferralRow
	for _, r := range rs {
		if r.Referrer == "APREF" {
			ap = r
		}
	}
	if ap.Kind != ReferrerAP || ap.Reward == 0 || ap.TDS != ap.Reward.Dec().Pct(money.DecInt(2)).Round(money.UnitRupee) {
		t.Fatalf("AP reward %+v", ap)
	}
	if n, err := o.PostReferrals(ctx, T, "farhan.khan", D, D); err != nil || n != 1 {
		t.Fatalf("post %d %v", n, err)
	}
	b, _ := o.App.Ledger.Balances(ctx, T, D)
	if -b["AP:APREF"] != ap.Reward-ap.TDS {
		t.Fatalf("AP account %s", -b["AP:APREF"])
	}
	siClock(o, siAt(D, "23:00"))
	siWant(t, siOne(t, o, ctx, "referral_rewards_ap", D), SIPass, "no client paid after the cut-off")
}

// What a contract note says besides the trades.
func TestContractNoteClauses(t *testing.T) {
	set := Settings{FirmState: "Maharashtra", FirmGSTIN: "27AAAAA0000A1Z5"}
	g := []billing.Group{{Exchange: "MCX", Segment: "COM", Charges: charges.Breakdown{GST: 1800}}}
	inter := noteClauses(set, Client{State: "Karnataka"}, "CN1", g)
	if !strings.Contains(inter[0], "SAC 997152") || !strings.Contains(inter[0], "place of supply Karnataka") || !strings.Contains(inter[0], "IGST ₹18.00") || len(inter) != 2 || !strings.Contains(inter[1], "Mumbai") {
		t.Fatalf("inter-state MCX %v", inter)
	}
	intra := noteClauses(set, Client{State: "Maharashtra", GSTIN: "27BBBBB0000B1Z5"}, "CN2", []billing.Group{{Exchange: "NSE", Segment: "CM", Charges: charges.Breakdown{GST: 1800}}})
	if !strings.Contains(intra[0], "CGST ₹9.00 and SGST ₹9.00") || !strings.Contains(intra[0], "27BBBBB0000B1Z5") || len(intra) != 1 {
		t.Fatalf("intra-state %v", intra)
	}
	sum := scripSummary([]ingest.TradeEvent{
		{Exchange: "NSE", Symbol: "ABC", InstrumentType: "EQ", Side: "B", Qty: 10, Price: money.MustDec("100")},
		{Exchange: "NSE", Symbol: "ABC", InstrumentType: "EQ", Side: "B", Qty: 30, Price: money.MustDec("104")},
		{Exchange: "NSE", Symbol: "ABC", InstrumentType: "EQ", Side: "S", Qty: 5, Price: money.MustDec("110")},
	})
	if len(sum) != 1 || sum[0].BuyQty != 40 || sum[0].BuyAvg != "103.0000" || sum[0].SellAvg != "110.0000" {
		t.Fatalf("summary %+v", sum)
	}
}

// BSE's state master spells Kerala as Keralam (BSE notice 20261120-9).
func TestBSEStateKeralam(t *testing.T) {
	if bseStates["KERALA"] != "KERALAM" || bseStates["KERALAM"] != "KERALAM" || bseStates["PONDICHERRY"] != "PUDUCHERRY" {
		t.Fatal(bseStates["KERALA"])
	}
}

// The FIU-IND monthly check: no CTR (no cash), and an NTR worksheet for a
// non-profit client's large receipt.
func TestFIUNTR(t *testing.T) {
	o, ctx := setup(t)
	u := siTrader(t, o, ctx).UCC
	sum, files, err := o.runFIUMonthly(ctx, T, "2026-10", D)
	if err != nil || len(files) != 0 || !strings.Contains(sum, "CTR: none") {
		t.Fatalf("%s %v", sum, err)
	}
	_, _ = docs.Update(ctx, o.Docs, T, cClient, u, "test", func(c *Client) error { c.CSC = append(c.CSC, npoCSC); return nil })
	siPut(t, o, ctx, cReceipt, "RCT-NTR", Receipt{ID: "RCT-NTR", Date: "2026-10-12", UCC: u, Mode: "NEFT", Ref: "UTR1", Amount: money.MustAmount("1500000"), Status: "Credited"})
	sum, files, err = o.runFIUMonthly(ctx, T, "2026-10", D)
	if err != nil || len(files) != 1 || !strings.Contains(sum, "NTR: 1") {
		t.Fatalf("%s %v", sum, err)
	}
	ws, _ := docs.List[WorkItem](ctx, o.Docs, T, cWorkItem, "")
	found := false
	for _, w := range ws {
		found = found || w.Key == "fiu:ntr:2026-10"
	}
	if !found {
		t.Fatal("no NTR work item")
	}
}
