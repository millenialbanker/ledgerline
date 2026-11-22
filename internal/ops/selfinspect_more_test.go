package ops

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/money"
)

// All data here is made up.

// The holding statement is the firm's tagged demat accounts from CDSL's
// statement of holding, never clients' accounts; an untagged account of no
// client blocks it with a work item, and the self-inspection says why.
func TestSIHoldingAccountTypes(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(D, "23:00"))
	siWant(t, siOne(t, o, ctx, "holding_account_types", D), SISkipped, "nothing loaded")
	cs, _ := o.Clients(ctx, T)
	var c Client
	for _, x := range cs {
		if x.Demat.ID != "" && !x.DPOnly {
			c = x
			break
		}
	}
	h := xheader(t, "cdsl-holding")
	line := func(bo, isin, bal, pl string) string {
		return xline(h, map[string]string{"Src": "CDSL", "BnfclOwnrId": bo, "BnfcryAcctCtgy": "1", "ISIN": isin, "CurBal": bal, "PldgdBal": pl, "StmtDt": D + "T18:00:00"})
	}
	pool, own, coll := "1208169999000001", "1208169999000002", "1208169999000003"
	soh := func(name string, lines ...string) []XFileResult {
		t.Helper()
		res, err := o.LoadExchangeFile(ctx, T, "priya.nair", name, []byte(strings.Join(append([]string{strings.Join(h, ",")}, lines...), "\r\n")+"\r\n"), true)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := soh("SOH_EXP_043200_123_F_202610161831_7_C_0_0_P0.csv", line(c.Demat.ID, "INE000X01010", "25.000", "0.000"), line(pool, "INE000X01010", "40.000", "0.000"),
		line(pool, "INE000Y01010", "2.500", "1.000"), line(own, "INE000X01010", "0.000", "0.000"))
	if !strings.Contains(res[0].Note, "2 demat accounts of the statement of holding (XXXXXXXXXXXX0001, XXXXXXXXXXXX0002) belong to no client and are not tagged") {
		t.Fatalf("load note %+v", res[0])
	}
	s := siOne(t, o, ctx, "holding_account_types", D)
	all := strings.Join(s.examples, " | ")
	if siState(s) != SIFail || s.found != 3 || !strings.Contains(all, "own client code") || !strings.Contains(all, "XXXXXXXXXXXX0001 is in the statement of holding, belongs to no client and is not tagged with its holding statement type (2 ISINs held)") ||
		!strings.Contains(all, "XXXXXXXXXXXX0002 is in the statement of holding, belongs to no client and is not tagged (it holds nothing now") || strings.Contains(all, pool) {
		t.Fatalf("untagged %+v", s)
	}
	// Blocked, with a work item for the compliance officer.
	if _, _, err := o.ExchangeFile(ctx, T, "priya.nair", "holding-statement-api", D, XFileOptions{}); !errors.Is(err, ErrState) {
		t.Fatalf("made while untagged: %v", err)
	}
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-holding-api-blocked-"+D); err != nil || w.Status != "Open" || w.Role != RoleCompliance {
		t.Fatalf("work %+v %v", w, err)
	}

	// Tagging: an Admin, with NSE's types only.
	if _, err := o.SaveFirmDemats(ctx, T, "vikram.shah", map[string]string{pool: "POOL"}, "FIRM01"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance officer tagged: %v", err)
	}
	if _, err := o.SaveFirmDemats(ctx, T, "admin", map[string]string{pool: "POOLS"}, "FIRM01"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown type saved: %v", err)
	}
	v, err := o.SaveFirmDemats(ctx, T, "admin", map[string]string{pool: "pool", own: "OWN", "12345": ""}, "firm01")
	if err != nil || v.FirmUCC != "FIRM01" || len(v.Accounts) != 2 || v.Accounts[0].Type != "POOL" || v.Accounts[0].ISINs != 2 || v.Accounts[0].Qty != "42.5" || len(v.Problems) != 0 || len(v.Types) != 12 {
		t.Fatalf("saved %+v %v", v, err)
	}
	siWant(t, siOne(t, o, ctx, "holding_account_types", D), SIPass, "tagged")
	name, j, err := o.ExchangeFile(ctx, T, "priya.nair", "holding-statement-api", D, XFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var msg struct {
		MemberPan   string   `json:"memberPan"`
		Date        string   `json:"date"`
		HoldingData []hsItem `json:"holdingData"`
	}
	if err := json.Unmarshal(j, &msg); err != nil || !strings.HasPrefix(name, "AAACB1234C_HS_16102026") || msg.MemberPan != "AAACB1234C" || msg.Date != "16-10-2026" || len(msg.HoldingData) != 2 {
		t.Fatalf("statement %s %s %v", name, j, err)
	}
	set, _ := o.Settings(ctx, T)
	for _, r := range msg.HoldingData {
		if r.Dmat != pool || r.AccountType != "POOL" || r.UCC != "FIRM01" || r.PAN != "AAACB1234C" || r.ClientName != set.Firm {
			t.Fatalf("row %+v", r)
		}
	}
	if r := msg.HoldingData[1]; r.ISIN != "INE000Y01010" || r.TotalQty != "2.500" || r.TotalPldgQty != "1.000" || r.FreeBalQty != "1.500" {
		t.Fatalf("quantities %+v", r)
	}
	if bytes.Contains(j, []byte(c.Demat.ID)) || bytes.Contains(j, []byte(c.PAN)) {
		t.Fatal("a client's holding is in the holding statement")
	}
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-holding-api-blocked-"+D); w.Status != "Done" {
		t.Fatalf("work not closed %+v", w)
	}

	// Types from the previous system's holding statement: accounts and types only.
	old := `{"memberPan":"AAACB1234C","holdingData":[{"recId":"1","dmat":"` + coll + `","accountType":"CLNT COLL","ucc":"X","pan":"AAACB1234C","isin":"INE000X01010","totalQty":"1"},{"recId":"2","dmat":"` + own + `","accountType":"OWN"}]}`
	v, found, err := o.FirmDematsFromStatement(ctx, T, "admin", "old_HS.json", []byte(old))
	if err != nil || len(found) != 2 || found["XXXXXXXXXXXX0003"] != "CLNT COLL" || len(v.Accounts) != 3 {
		t.Fatalf("from statement %+v %v %v", v, found, err)
	}
	if _, _, err := o.FirmDematsFromStatement(ctx, T, "priya.nair", "old_HS.json", []byte(old)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker tagged: %v", err)
	}
	// A tagged NSDL account cannot be left out silently.
	if _, err := o.SaveFirmDemats(ctx, T, "admin", map[string]string{pool: "POOL", own: "OWN", "IN30000011112222": "CLNT UNPAID"}, "FIRM01"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.ExchangeFile(ctx, T, "priya.nair", "holding-statement-api", D, XFileOptions{}); err == nil || !strings.Contains(err.Error(), "is with NSDL") {
		t.Fatalf("nsdl %v", err)
	}
	// No statement of holding for a day: nothing is made.
	if _, _, err := o.ExchangeFile(ctx, T, "priya.nair", "holding-statement-api", addDays(D, -1), XFileOptions{}); err == nil || !strings.Contains(err.Error(), "is not loaded") {
		t.Fatalf("no soh %v", err)
	}
}

func TestSIDealerTerminals(t *testing.T) {
	o, ctx := dealerOps(t, "2026-10-13")
	siWant(t, siOne(t, o, ctx, "dealer_terminals", "2026-10-13"), SISkipped, "no dealers")
	a := testDealer("Asha", "AAAPA1111A", nseTerm("40001", "2026-01-01"))
	a.NISM = NISMCert{CertNo: "NISM-202310140001", ExamDate: "2023-10-15", ValidTill: "2026-10-14"}
	if _, err := o.SaveDealer(ctx, T, "priya.nair", a); err != nil {
		t.Fatal(err)
	}
	dealerClock(o, "2026-10-30")
	if _, _, err := o.App.Trades.Add(ctx, []ingest.TradeEvent{dealerTrade("C1", "NSE", "40001", "2026-10-14", "1", 10, "100")}); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "dealer_terminals", "2026-10-30"), SIPass, "certified dealer")
	if _, _, err := o.App.Trades.Add(ctx, []ingest.TradeEvent{
		dealerTrade("C2", "NSE", "40001", "2026-10-15", "2", 20, "100"), // certificate expired on the 14th
		dealerTrade("C3", "NSE", "99999", "2026-10-15", "3", 3, "100"),  // nobody's terminal
		dealerTrade("C3", "NSE", "99999", "2026-10-16", "4", 3, "100"),
		dealerTrade("C1", "NSE", "", "2026-10-15", "5", 1, "50"), // no terminal in the file: not checked
	}); err != nil {
		t.Fatal(err)
	}
	s := siOne(t, o, ctx, "dealer_terminals", "2026-10-30")
	if siState(s) != SIFail || s.checked != 4 || s.found != 3 || len(s.examples) != 2 || !strings.Contains(s.examples[0], "terminal 99999 · 2 trades") || !strings.Contains(s.examples[1], "NISM certificate expired on 2026-10-14") {
		t.Fatalf("dealers %+v", s)
	}
}

func TestSIMinorTrading(t *testing.T) {
	o, ctx := newOps(t)
	siClock(o, siAt("2026-10-30", "23:00"))
	m := nseUCITestClient("MN001", "ABCPM1001K")
	m.DOB, m.Segments = "2010-11-15", []string{"CM"} // 15 until 15 Nov 2026
	adult := nseUCITestClient("AD001", "ABCPA1001K")
	nseUCIAddClient(t, o, m, false)
	nseUCIAddClient(t, o, adult, false)
	tr := func(ucc, seg, side, no string) ingest.TradeEvent {
		return ingest.TradeEvent{Tenant: T, Origin: ingest.FromExchange, Exchange: "NSE", Segment: seg, TradeNo: no, UCC: ucc, Symbol: "XTEST", InstrumentType: "EQ", Side: side, Qty: 1, Price: money.MustDec("10"), TradeDate: "2026-10-20"}
	}
	if _, _, err := o.App.Trades.Add(ctx, []ingest.TradeEvent{tr("MN001", "CM", "B", "1"), tr("AD001", "CM", "B", "2"), tr("AD001", "CM", "S", "3"), tr("AD001", "FO", "B", "4")}); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "minor_trading", "2026-10-30"), SIPass, "a minor's delivery buy")
	if _, _, err := o.App.Trades.Add(ctx, []ingest.TradeEvent{tr("MN001", "CM", "S", "5"), tr("MN001", "FO", "B", "6")}); err != nil {
		t.Fatal(err)
	}
	s := siOne(t, o, ctx, "minor_trading", "2026-10-30")
	if s.found != 1 || !strings.Contains(s.examples[0], "derivatives; bought and sold the same scrip the same day") || strings.Contains(s.examples[0], "MN001") {
		t.Fatalf("minor %+v", s)
	}
	if siAgeOn("2010-11-15", "2028-11-14") != 17 || siAgeOn("2010-11-15", "2028-11-15") != 18 || siAgeOn("", "2028-11-15") != -1 {
		t.Fatal("ages")
	}
}

func TestSIKYCBeforeTrade(t *testing.T) {
	o, ctx := newOps(t)
	siClock(o, siAt("2026-10-30", "23:00"))
	c := nseUCITestClient("KY001", "ABCPK1001K")
	c.KRAStatus = "Validated"
	nseUCIAddClient(t, o, c, true)
	if _, _, err := o.App.Trades.Add(ctx, []ingest.TradeEvent{{Tenant: T, Origin: ingest.FromExchange, Exchange: "NSE", Segment: "CM", TradeNo: "1", UCC: "KY001", Symbol: "X", Side: "B", Qty: 1, Price: money.MustDec("1"), TradeDate: "2026-10-20"}}); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "kyc_before_trade", "2026-10-30"), SIPass, "complete KYC")
	c.KRAStatus, c.Income = "On hold", ""
	nseUCIAddClient(t, o, c, false)
	siPut(t, o, ctx, cUCCReg, "NSE|KY001", NSEUCIReg{Exchange: "NSE", UCC: "KY001", PANStatus: "A", AadhaarSeeding: "N"})
	s := siOne(t, o, ctx, "kyc_before_trade", "2026-10-30")
	if s.found != 1 || !strings.Contains(s.examples[0], "KRA on hold; no income range; PAN not linked with Aadhaar (NSE)") {
		t.Fatalf("kyc %+v", s)
	}
}

func TestSISharedContacts(t *testing.T) {
	o, ctx := newOps(t)
	siClock(o, siAt("2026-10-30", "23:00"))
	a, b, c := nseUCITestClient("SC001", "ABCPS1001K"), nseUCITestClient("SC002", "ABCPS1002K"), nseUCITestClient("SC003", "ABCPS1003K")
	b.Mobile, c.Mobile = a.Mobile, "9123400000" // b is a's spouse, declared below
	for _, x := range []Client{a, b, c} {
		nseUCIAddClient(t, o, x, false)
	}
	if _, err := o.SaveUCCDetails(ctx, T, "priya.nair", "SC002", UCCDetails{MobileRelation: "Spouse"}); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "shared_contacts", "2026-10-30"), SIPass, "family declared")
	c.Mobile = a.Mobile // and c, who is not family
	nseUCIAddClient(t, o, c, false)
	s := siOne(t, o, ctx, "shared_contacts", "2026-10-30")
	if s.found != 1 || !strings.Contains(s.examples[0], "Mobile …210 · on 3 clients (SC***, SC***, SC***); 2 not declared as the holder's family") {
		t.Fatalf("shared %+v", s)
	}
}

func TestSIPayoutOneDay(t *testing.T) {
	o, ctx := newOps(t)
	siClock(o, siAt("2026-10-30", "23:00"))
	amt := money.MustAmount("100.00")
	siPut(t, o, ctx, cPOBatch, "PB-1", PayoutBatch{ID: "PB-1", Date: "2026-10-21"})
	siPut(t, o, ctx, cPOBatch, "PB-2", PayoutBatch{ID: "PB-2", Date: "2026-10-26"})
	siPut(t, o, ctx, cPayout, "PO-1", PayoutRequest{ID: "PO-1", Date: "2026-10-20", UCC: "TC001", Amount: amt, Status: "Paid", Batch: "PB-1"})
	siPut(t, o, ctx, cPayout, "PO-2", PayoutRequest{ID: "PO-2", Date: "2026-10-21", UCC: "TC001", Amount: amt, Status: "Rejected"})
	siPut(t, o, ctx, cPayout, "PO-3", PayoutRequest{ID: "PO-3", Date: "2026-10-30", UCC: "TC001", Amount: amt, Status: "Requested"}) // not yet due
	siWant(t, siOne(t, o, ctx, "payout_one_day", "2026-10-30"), SIPass, "paid the next day")
	siPut(t, o, ctx, cPayout, "PO-4", PayoutRequest{ID: "PO-4", Date: "2026-10-21", UCC: "TC002", Amount: amt, Status: "Paid", Batch: "PB-2"})
	siPut(t, o, ctx, cPayout, "PO-5", PayoutRequest{ID: "PO-5", Date: "2026-10-22", UCC: "TC003", Amount: amt, Status: "Approved"})
	s := siOne(t, o, ctx, "payout_one_day", "2026-10-30")
	if s.found != 2 || !strings.Contains(s.examples[0], "PO-4") || !strings.Contains(s.examples[0], "paid on 26-Oct-2026") || !strings.Contains(s.examples[1], "PO-5") || !strings.Contains(s.examples[1], "approved, not paid") {
		t.Fatalf("payouts %+v", s)
	}
}

// The running account statement goes to clients settled by retention too,
// and the test checks it was sent.
func TestSIRASStatements(t *testing.T) {
	o, ctx := newOps(t)
	siClock(o, siAt("2026-10-30", "23:00"))
	paid, kept := nseUCITestClient("RS001", "ABCPR1001K"), nseUCITestClient("RS002", "ABCPR1002K")
	nseUCIAddClient(t, o, paid, false)
	nseUCIAddClient(t, o, kept, false)
	siPut(t, o, ctx, cRASBatch, "2026-10-03|1", RASBatch{Date: "2026-10-03", AsOf: "2026-10-02", Items: []RASItem{{UCC: "RS001", Name: paid.Name, Bank: "HDFC", IFSC: "HDFC0001234", Account: "50100012345678", Amount: money.MustAmount("500.00")}},
		Retained: []RASItem{{UCC: "RS002", Name: kept.Name, Credit: money.MustDec("300"), Retained: money.MustDec("300")}}})
	s := siOne(t, o, ctx, "ras_statements", "2026-10-30")
	if s.checked != 2 || s.found != 2 {
		t.Fatalf("none sent %+v", s)
	}
	rep, err := o.RASStatement(ctx, T, "2026-10-03", "RS002")
	if err != nil || !strings.Contains(rep.Sections[0].Rows[2][0], "nothing: the balance was kept") {
		t.Fatalf("retained statement %+v %v", rep.Sections, err)
	}
	res, err := o.SendStatements(ctx, T, "priya.nair", DocRAS, "2026-10-03")
	if err != nil || res.Sent+res.Held+res.Failed != 2 {
		t.Fatalf("send %+v %v", res, err)
	}
	// Delivered on the 6th (the test clock is before the signing
	// certificate's start, so the attempts above were held).
	for _, u := range []string{"RS001", "RS002"} {
		siPut(t, o, ctx, cDispatch, "D-"+u, Dispatch{ID: "D-" + u, Key: DocRAS + "|2026-10-03|" + u, Kind: DocRAS, UCC: u, Status: "Sent", SentAt: "2026-10-06T10:00:00+05:30"})
	}
	siWant(t, siOne(t, o, ctx, "ras_statements", "2026-10-30"), SIPass, "both sent")
}

func TestSIMarginStatements(t *testing.T) {
	o, ctx := setup(t)
	siClock(o, siAt(addDays(D, 3), "23:00"))
	e, _ := o.siEnvFor(ctx, T, addDays(D, 3))
	if rep, err := o.Margins(ctx, T, D); err != nil || len(rep.Rows) == 0 || e.set.NextBusinessDay(D) >= addDays(D, 3) {
		t.Skipf("no margins in the sample day: %v", err)
	}
	s := siOne(t, o, ctx, "margin_statements", addDays(D, 3))
	if siState(s) != SIFail || s.checked == 0 || s.found != s.checked || !strings.Contains(s.examples[0], "not sent") {
		t.Fatalf("not sent %+v", s)
	}
	if _, err := o.SendStatements(ctx, T, "priya.nair", DocMargin, D); err != nil {
		t.Fatal(err)
	}
	// Sent now, after the next trading day: late.
	s = siOne(t, o, ctx, "margin_statements", addDays(D, 3))
	if siState(s) != SIFail || !strings.Contains(s.examples[0], "sent on") {
		t.Fatalf("late %+v", s)
	}
	ds, _ := docs.List[Dispatch](ctx, o.Docs, T, cDispatch, "")
	if len(ds) == 0 {
		t.Fatal("nothing dispatched")
	}
}
