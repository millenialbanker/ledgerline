package ops

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// Haircut floors and the T-1 close (NSE/INSP/66840 Annexure A).
func TestPledgeHaircutFloors(t *testing.T) {
	for _, c := range []struct {
		what string
		i    Instrument
		want string
	}{
		{"G-sec with no CC haircut", Instrument{ISIN: "IN0020990011", Segment: "CM"}, "10"},
		{"G-sec with the CC's haircut", Instrument{ISIN: "IN0020990011", HaircutPct: money.MustDec("4")}, "4"},
		{"unlisted fund", Instrument{ISIN: "INF000Z01019", Segment: "REF", HaircutPct: money.MustDec("2")}, "10"},
		{"listed fund: at least VaR", Instrument{ISIN: "INF000Z01027", Segment: "CM", HaircutPct: money.MustDec("3"), VaRPct: money.MustDec("8")}, "8"},
		{"share: haircut above VaR", Instrument{ISIN: "INE000Z01011", Segment: "CM", HaircutPct: money.MustDec("12"), VaRPct: money.MustDec("9")}, "12"},
		{"never above 100", Instrument{ISIN: "INE000Z01029", Segment: "CM", HaircutPct: money.MustDec("130")}, "100"},
	} {
		if got := pledgeHaircut(c.i).String(); got != c.want {
			t.Errorf("%s: %s, want %s", c.what, got, c.want)
		}
	}
	cur := newLookup(Market{Instruments: []Instrument{{Exchange: "NSE", Segment: "CM", Symbol: "S", Type: "EQ", ISIN: "INE000Z01011", LTP: money.MustDec("120"), HaircutPct: money.MustDec("10")}}})
	prev := newLookup(Market{Instruments: []Instrument{{Exchange: "NSE", Segment: "CM", Symbol: "S", Type: "EQ", ISIN: "INE000Z01011", LTP: money.MustDec("101"), Close: money.MustDec("100")}}})
	// 10 shares at the T-1 close of 100 less the day's 10%.
	if v, ok := pledgeValueOf(cur, prev, "INE000Z01011", money.DecInt(10)); !ok || v.String() != "900" {
		t.Fatalf("value %s %v", v, ok)
	}
	// No T-1 price: the day's own.
	if v, _ := pledgeValueOf(cur, newLookup(Market{}), "INE000Z01011", money.DecInt(10)); v.String() != "1080" {
		t.Fatalf("value without T-1 %s", v)
	}
}

// The intimation after the pledge, no exposure on CUSPA shares, the
// written policy and the extension.
func TestCUSPAIntimationPolicyExtension(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "16:00"))
	set, _ := o.Settings(ctx, T)
	st, _ := o.Obligations(ctx, T, D)
	itc := obl(t, st, "BH1010", "NSE", "ITC")
	riskColl := func() string {
		rv, _ := o.Risk(ctx, T, D)
		for _, r := range rv.Rows {
			if r.UCC == "BH1010" {
				return r.Collateral.String()
			}
		}
		return ""
	}
	before := riskColl()
	if _, err := o.SettleAction(ctx, T, "priya.nair", D, itc.Key, "cuspa"); err != nil {
		t.Fatal(err)
	}
	items, _ := docs.List[CUSPAItem](ctx, o.Docs, T, cCUSPA, "")
	if len(items) != 1 || items[0].IntimatedAt == "" || !strings.HasPrefix(items[0].Intimation, "sent by ") {
		t.Fatalf("intimation %+v", items)
	}
	it := items[0]
	// CUSPA shares give no collateral (no exposure on them).
	if after := riskColl(); after != before {
		t.Fatalf("collateral %s became %s with the CUSPA line", before, after)
	}
	// A line paid out under the framework (from 31 Oct 2026) and not told.
	late := CUSPAItem{ID: "CU-I1", Date: "2026-10-30", UCC: "BH1010", Exchange: "NSE", ISIN: it.ISIN, Symbol: it.Symbol, Qty: 5, Value: money.DecInt(2000), Status: CUSPAHeld, PayOut: "2026-11-02"}
	siPut(t, o, ctx, cCUSPA, late.ID, late)
	next := set.NextBusinessDay(late.PayOut)
	siWant(t, siOne(t, o, ctx, "cuspa_intimation", next), SIFail, "not told")
	// The daily review sends it.
	if _, _, err := o.cuspaReview(ctx, T, SystemActor, late.PayOut); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "cuspa_intimation", next), SIPass, "told by the review")
	siPut(t, o, ctx, cCUSPA, late.ID, CUSPAItem{ID: late.ID, Date: late.Date, UCC: late.UCC, ISIN: late.ISIN, Symbol: late.Symbol, Released: 5, Value: late.Value, Status: CUSPAReleased, PayOut: late.PayOut, IntimatedAt: siAt("2026-11-02", "18:00").Format(time.RFC3339)})

	// The policy.
	siWant(t, siOne(t, o, ctx, "cuspa_policy", D), SIFail, "no policy")
	v, _ := o.CUSPAPolicyStatus(ctx, T)
	in := CUSPAPolicyInput{Text: v.Draft, EffectiveFrom: D, WebsiteURL: "https://example.invalid/unpaid-securities"}
	if _, err := o.SaveCUSPAPolicy(ctx, T, "priya.nair", in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an ops maker recorded the policy: %v", err)
	}
	if _, err := o.SaveCUSPAPolicy(ctx, T, "vikram.shah", CUSPAPolicyInput{Text: "short", EffectiveFrom: D, WebsiteURL: in.WebsiteURL}); err == nil {
		t.Fatal("a policy without its content was taken")
	}
	p, err := o.SaveCUSPAPolicy(ctx, T, "vikram.shah", in)
	if err != nil || p.Version != 1 || p.PayDays != set.cuspaPayDays() {
		t.Fatalf("policy %+v %v", p, err)
	}
	siWant(t, siOne(t, o, ctx, "cuspa_policy", D), SIFail, "not sent")
	if p, err = o.CommunicateCUSPAPolicy(ctx, T, "vikram.shah", D); err != nil || p.CommunicatedOn != D || p.Sent == 0 {
		t.Fatalf("sent %+v %v", p, err)
	}
	if v, _ = o.CUSPAPolicyStatus(ctx, T); v.SentNow != v.Active || len(v.Problems) != 0 {
		t.Fatalf("status %+v", v)
	}
	siWant(t, siOne(t, o, ctx, "cuspa_policy", D), SIPass, "recorded and sent")

	// The extension, from 3 January 2027.
	line := CUSPAItem{ID: "CU-X1", Date: "2027-01-08", UCC: "BH1010", Exchange: "NSE", ISIN: it.ISIN, Symbol: it.Symbol, Qty: 10, Value: money.DecInt(5000), Status: CUSPAHeld, PayOut: "2027-01-11",
		NoticeOn: "2027-01-15", IntimatedAt: siAt("2027-01-11", "18:00").Format(time.RFC3339)}
	siPut(t, o, ctx, cCUSPA, line.ID, line)
	day5 := addTradingDays(set, line.PayOut, cuspaInvokeDays)
	ext := func(date, hm string) CUSPABulkResult {
		t.Helper()
		siClock(o, siAt(date, hm))
		r, err := o.CUSPABulk(ctx, T, "anita.rao", CUSPABulkReq{Date: date, Action: "extend", IDs: []string{line.ID}, Code: "CUSPA02", Note: "trading suspended"})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if _, err := o.CUSPABulk(ctx, T, "anita.rao", CUSPABulkReq{Date: day5, Action: "extend", IDs: []string{line.ID}, Code: "X1", Note: "why"}); err == nil {
		t.Fatal("an extension without a CUSPA reason code was taken")
	}
	if r := ext(set.PrevBusinessDay(day5), "11:00"); r.Done != 0 {
		t.Fatalf("extended before the last day: %+v", r)
	}
	if r := ext(day5, "18:30"); r.Done != 0 || !strings.Contains(r.Lines[0].Why, "6 PM") {
		t.Fatalf("extended after 6 PM: %+v", r)
	}
	if r := ext(day5, "17:00"); r.Done != 1 {
		t.Fatalf("extension: %+v", r)
	}
	x, _, _ := docs.Get[CUSPAItem](ctx, o.Docs, T, cCUSPA, line.ID)
	until := cuspaExtUntil(set, day5)
	if len(x.Extensions) != 1 || x.Extensions[0].Until != until || !x.Extensions[0].NoticeOK || x.dates(set, day5).LastInvoke != until {
		t.Fatalf("extended line %+v", x)
	}
	if _, b, _ := o.CUSPAFile(ctx, T, day5, "depository"); !strings.Contains(string(b), "Extend (by 6 PM)") {
		t.Fatalf("depository list %q", b)
	}
	// Again on the last day of the extension, while the reason lasts.
	if r := ext(until, "10:00"); r.Done != 1 {
		t.Fatalf("second extension: %+v", r)
	}
	siWant(t, siOne(t, o, ctx, "cuspa_extension", until), SIPass, "two extensions in time")
	x, _, _ = docs.Get[CUSPAItem](ctx, o.Docs, T, cCUSPA, line.ID)
	x.Extensions[0].At = siAt(day5, "19:00").Format(time.RFC3339)
	siPut(t, o, ctx, cCUSPA, x.ID, x)
	siWant(t, siOne(t, o, ctx, "cuspa_extension", until), SIFail, "recorded after 6 PM")
}

// T+1+5: a debit unpaid more than 5 trading days after its pay-in blocks
// buying whatever the firm's own limit.
func TestDebitPayInDays(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "20:00"))
	set, _ := o.Settings(ctx, T)
	set.DebitBlockDays = 60
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	cs, _ := o.Clients(ctx, T)
	var c Client
	for _, x := range cs {
		if x.Status == "Active" && !x.Blocked {
			c = x
			break
		}
	}
	c.UCC, c.Name = "DB501", "Made-up Debtor"
	siPut(t, o, ctx, cClient, c.UCC, c)
	old := addDays(D, -14)
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "DB501-debit", ValueDate: old, Narration: "unpaid purchase", Source: ledger.SourceRef{Kind: "MANUAL", ID: "DB501"}, CreatedBy: "test",
		Postings: []ledger.Posting{{Account: ledger.ClientAccount("DB501"), Debit: money.MustAmount("50000")}, {Account: ledger.BankClient, Credit: money.MustAmount("50000")}}}); err != nil {
		t.Fatal(err)
	}
	v, err := o.Debits(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	var row DebitRow
	for _, r := range v.Rows {
		if r.UCC == "DB501" {
			row = r
		}
	}
	if row.PayInDays <= debitPayInDays || row.Action != "Block" || !strings.Contains(row.BlockWhy, "after its pay-in") {
		t.Fatalf("row %+v", row)
	}
	siWant(t, siOne(t, o, ctx, "debit_day5_block", D), SIFail, "not blocked")
	if _, _, err := o.ApplyDebitBlocks(ctx, T, "anita.rao", D); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "debit_day5_block", D), SIPass, "blocked")
}

// The NMASS unpaid/MTF flags leave out the firm's own code and UPI-block
// clients.
func TestCUSPAMTFFileExclusions(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	var normal, upi Client
	for _, c := range cs {
		if c.Status != "Active" || c.UPIBlock > 0 {
			continue
		}
		if normal.UCC == "" {
			normal = c
		} else if upi.UCC == "" {
			upi = c
		}
	}
	upi.UPIBlock = money.MustAmount("25000")
	siPut(t, o, ctx, cClient, upi.UCC, upi)
	for i, u := range []string{normal.UCC, upi.UCC, "PRO"} {
		id := "CU-M" + string(rune('1'+i))
		siPut(t, o, ctx, cCUSPA, id, CUSPAItem{ID: id, Date: D, UCC: u, ISIN: "INE000X01010", Symbol: "XTEST", Qty: 40, Status: CUSPAHeld})
	}
	siPut(t, o, ctx, cSettCal, "CM|NORMAL1|2026199", SettlementDay{ID: "CM|NORMAL1|2026199", Segment: "CM", Type: "NORMAL1", No: "2026199", TradeDate: D})
	name, b, err := o.ExchangeFile(ctx, T, "priya.nair", "ncl-cuspa-mtf-payout", D, XFileOptions{})
	if err != nil || !strings.HasPrefix(name, "UNPAIDMTF_M_") {
		t.Fatalf("%s %v", name, err)
	}
	if s := string(b); !strings.Contains(s, ","+normal.UCC+",") || strings.Contains(s, ","+upi.UCC+",") || strings.Contains(s, ",PRO,") {
		t.Fatalf("file %q", s)
	}
	// A trade-for-trade share goes in its own settlement's file.
	siPut(t, o, ctx, cSecRef, "INE000X01010", SecRef{ISIN: "INE000X01010", Symbol: "XTEST", Series: "BE"})
	lib, _ := XFiles()
	sp, _ := lib.Get("ncl-cuspa-mtf-payout")
	set, _ := o.Settings(ctx, T)
	if _, _, err := o.cuspaMTFFile(ctx, T, sp, set, D, map[string]string{"date": D, "seq": "1", "sett_type": "NORMAL1"}); !errors.Is(err, ErrState) {
		t.Fatalf("a T2T purchase in the normal settlement: %v", err)
	}
	if _, _, err := o.cuspaMTFFile(ctx, T, sp, set, D, map[string]string{"date": D, "seq": "1", "sett_type": "TFT1"}); err == nil || errors.Is(err, ErrState) {
		t.Fatalf("TFT1 without its settlement loaded: %v", err)
	}
}

// The collateral and pledge self-inspection tests.
func TestCollateralSelfInspection(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "23:00"))
	set, _ := o.Settings(ctx, T)
	cs, _ := o.Clients(ctx, T)
	u := cs[0].UCC

	// Margin cover at NSE Clearing.
	siWant(t, siOne(t, o, ctx, "client_margin_cover", D), SISkipped, "no client margins")
	siPut(t, o, ctx, cNCLMCli, "FO|"+D+"|"+u, NCLMClient{Segment: "FO", Date: D, UCC: u, CashCollateral: money.MustDec("1000"), NonCash: money.MustDec("500"), Total: money.MustDec("1200")})
	siWant(t, siOne(t, o, ctx, "client_margin_cover", D), SIPass, "covered")
	siPut(t, o, ctx, cNCLMCli, "FO|"+D+"|"+u, NCLMClient{Segment: "FO", Date: D, UCC: u, CashCollateral: money.MustDec("1000"), NonCash: money.MustDec("500"), Total: money.MustDec("2000")})
	siWant(t, siOne(t, o, ctx, "client_margin_cover", D), SIFail, "short")

	// Re-pledge: waits for NSE Clearing's file.
	siWant(t, siOne(t, o, ctx, "repledge_own_client", D), SISkipped, "no re-pledge status file")

	// Pool and unpaid accounts in the statements of holding.
	pool, unpaid := "1208160000000001", "1208160000000002"
	set.FirmDemats = map[string]string{pool: "POOL", unpaid: "CLNT UNPAID"}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	prev := set.PrevBusinessDay(D)
	snap := func(date, poolQty, pledged string) FirmDematSnap {
		return FirmDematSnap{Date: date, Accounts: []FirmDematSeen{{BOID: pool, Lines: []FirmDematLine{{ISIN: "INE000X01010", Total: poolQty, Pledged: "0"}}},
			{BOID: unpaid, Lines: []FirmDematLine{{ISIN: "INE000X01028", Total: "5", Pledged: pledged}}}}}
	}
	siPut(t, o, ctx, cFirmDematSnap, "cdsl|"+prev, snap(prev, "100", "0"))
	siPut(t, o, ctx, cFirmDematSnap, "cdsl|"+D, snap(D, "100", "5"))
	siPut(t, o, ctx, cFirmDematSnap, "cdsl", snap(D, "100", "5"))
	s := siOne(t, o, ctx, "client_secs_pool", D)
	if siState(s) != SIFail || s.found != 2 {
		t.Fatalf("pool: %+v", s)
	}
	siWant(t, siOne(t, o, ctx, "cuspa_no_lender", D), SIFail, "CUSPA account pledged out")

	// Notice and undertaking with an invocation.
	sent := siAt(D, "11:00")
	r := PledgeRelease{ID: "CF-T1", Kind: "CF", UCC: u, Of: "MP", PSN: "9001", Qty: 5, Symbol: "MADEUP", Status: "Done", SentAt: sent.Format(time.RFC3339)}
	siPut(t, o, ctx, cCPLRelease, r.ID, r)
	siWant(t, siOne(t, o, ctx, "pledge_invoke_notice", D), SIFail, "no notice")
	siWant(t, siOne(t, o, ctx, "pledge_undertaking", D), SIFail, "no undertaking")
	r.NoticeAt, r.DueAt = sent.Add(-48*time.Hour).Format(time.RFC3339), sent.Add(-24*time.Hour).Format(time.RFC3339)
	siPut(t, o, ctx, cCPLRelease, r.ID, r)
	siWant(t, siOne(t, o, ctx, "pledge_invoke_notice", D), SIPass, "notice run out before sending")
	if err := o.RecordPledgeUndertaking(ctx, T, "vikram.shah", "CF", r.ID, "UT/2026/0001"); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "pledge_undertaking", D), SIPass, "undertaking kept")
}

// Margin pledges only in NSE Clearing's approved securities.
func TestApprovedForMargin(t *testing.T) {
	o, ctx := setup(t)
	if ok, _, have := o.approvedForMargin(ctx, T, "INE000X01010", D); !ok || have {
		t.Fatal("refused with no list loaded")
	}
	siPut(t, o, ctx, cApprovedSec, D, ApprovedSecs{ID: D, Haircut: map[string]money.Dec{"INE000X01010": money.MustDec("12.5")}})
	o.indexDate(ctx, T, cApprovedSec, D, "test")
	if ok, _, have := o.approvedForMargin(ctx, T, "INE000X01010", D); !ok || !have {
		t.Fatal("an approved security refused")
	}
	if ok, _, _ := o.approvedForMargin(ctx, T, "INE000X01028", D); ok {
		t.Fatal("a security not on the list taken")
	}
	if ok, _, _ := o.approvedForMargin(ctx, T, "IN0020990011", D); !ok {
		t.Fatal("a G-sec refused")
	}
	if ok, _, have := o.approvedForMargin(ctx, T, "INE000X01028", addDays(D, 20)); !ok || have {
		t.Fatal("a stale list still refuses")
	}
}

// Tender-offer early pay-ins carry the TMID and stop at the cut-off.
func TestEPITender(t *testing.T) {
	o, ctx := setup(t)
	day := "2026-12-01"
	siPut(t, o, ctx, cIPOIssue, "TO-1", IPOIssue{ID: "TO-1", Name: "Made-up buy-back", Type: IssueTender, Close: day, CloseTime: "17:00", DayClose: "17:00"})
	cfg := o.CDSLPledgeSettings(ctx, T)
	cfg.EPITMCPID = ""
	in := EPIInput{Date: day, Exchange: "NSE", Tender: "TO-1"}
	set, _ := o.Settings(ctx, T)
	_, tmid, warn, err := o.epiTender(ctx, T, cfg, in, siAt(day, "12:00"))
	if want := strings.ToUpper(set.MemberCodes["NSE"]); err != nil || tmid != want || warn != "" {
		t.Fatalf("tmid %q (want %q) %q %v", tmid, want, warn, err)
	}
	if _, _, warn, err = o.epiTender(ctx, T, cfg, in, siAt(day, "16:55")); err != nil || !strings.Contains(warn, "10 minutes") {
		t.Fatalf("near the cut-off %q %v", warn, err)
	}
	if _, _, _, err = o.epiTender(ctx, T, cfg, in, siAt(day, "17:01")); err == nil {
		t.Fatal("taken after the cut-off")
	}
	if _, _, _, err = o.epiTender(ctx, T, cfg, EPIInput{Date: day, Tender: "NOPE"}, siAt(day, "12:00")); err == nil {
		t.Fatal("an unknown offer taken")
	}
}
