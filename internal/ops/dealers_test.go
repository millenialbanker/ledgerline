package ops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// Made-up PANs and people only.

func dealerClock(o *Ops, day string) {
	t, _ := time.ParseInLocation("2006-01-02 15:04", day+" 10:00", ist)
	o.Now = func() time.Time { return t }
}

// dealerOps is a development firm with a branch, an RM and an AP.
func dealerOps(t *testing.T, day string) (*Ops, context.Context) {
	t.Helper()
	o, ctx := newOps(t)
	dealerClock(o, day)
	if _, err := o.SaveBranch(ctx, T, "admin", Branch{Code: "Mumbai", Name: "Mumbai head office", City: "Mumbai", Active: true}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Partner{{Code: "RM01", Name: "Test RM", Kind: "Relationship manager", Branch: "Mumbai", Active: true},
		{Code: "AP001", Name: "Test AP", Kind: RoleAP, Branch: "Mumbai", RegNo: "AP0000001", Active: true}} {
		if _, err := o.SavePartner(ctx, T, "admin", p); err != nil {
			t.Fatal(err)
		}
	}
	return o, ctx
}

func testDealer(first, pan string, terms ...DealerTerminal) Dealer {
	return Dealer{FirstName: first, LastName: "Dealer", PAN: pan, DOB: "1990-01-01", Mobile: "9876543210", Email: strings.ToLower(first) + "@example.invalid",
		Relationship: DealerEmployee, Branch: "Mumbai",
		Office:    DealerAddress{Address: "Colaba Causeway", Area: "Colaba", City: "MUMBAI", State: "Maharashtra", Pincode: "400005"},
		Residence: DealerAddress{Address: "Flat 1", Area: "Andheri", City: "MUMBAI", Pincode: "400058"},
		NISM:      NISMCert{CertNo: "NISM-201900012345", ExamDate: "2024-01-31", ValidTill: "2027-01-30"},
		Terminals: terms}
}

func nseTerm(id, from string) DealerTerminal {
	return DealerTerminal{Exchange: "NSE", Segments: []string{"CM", "FO"}, TerminalID: id, ActiveFrom: from}
}

func fieldErr(err error, field string) bool {
	var ve *ValidationError
	if !errors.As(err, &ve) {
		return false
	}
	for _, f := range ve.Fields {
		if f.Field == field {
			return true
		}
	}
	return false
}

func TestDealerCRUDAndValidation(t *testing.T) {
	o, ctx := dealerOps(t, "2026-10-30")
	d := testDealer("Asha", "AAAPA1111A", nseTerm("40001", "2026-10-01"))
	if _, err := o.SaveDealer(ctx, T, "farhan.khan", d); !errors.Is(err, ErrForbidden) {
		t.Fatalf("checker added a dealer: %v", err)
	}
	bad := d
	bad.PAN = "AAAPA1111"
	if _, err := o.SaveDealer(ctx, T, "priya.nair", bad); !fieldErr(err, "pan") {
		t.Fatalf("bad PAN: %v", err)
	}
	bad = d
	bad.NISM = NISMCert{}
	if _, err := o.SaveDealer(ctx, T, "priya.nair", bad); !fieldErr(err, "nism.valid_till") {
		t.Fatalf("active terminal without NISM: %v", err)
	}
	bad = d
	bad.Terminals = []DealerTerminal{{Exchange: "NSE", Segments: []string{"CM"}, TerminalID: "40009", ActiveFrom: "2026-10-01", ClosedOn: "2026-09-01"}}
	if _, err := o.SaveDealer(ctx, T, "priya.nair", bad); !fieldErr(err, "terminals[0].closed_on") {
		t.Fatalf("closed before active: %v", err)
	}
	bad = d
	bad.DOB = "2015-01-01"
	bad.NISM.ValidTill = "2030-01-30"
	if _, err := o.SaveDealer(ctx, T, "priya.nair", bad); !fieldErr(err, "dob") || !fieldErr(err, "nism.valid_till") {
		t.Fatalf("insane dates: %v", err)
	}
	bad = d
	bad.Terminals = []DealerTerminal{{Exchange: "MCX", Segments: []string{"CM"}, TerminalID: "M1", ActiveFrom: "2026-10-01"}}
	if _, err := o.SaveDealer(ctx, T, "priya.nair", bad); !fieldErr(err, "terminals[0].segments") {
		t.Fatalf("MCX cash segment: %v", err)
	}
	a, err := o.SaveDealer(ctx, T, "priya.nair", d)
	if err != nil || a.ID != "DL-0001" || a.Status != DealerActive || a.CreatedBy != "priya.nair" {
		t.Fatalf("create: %+v %v", a, err)
	}
	// The same NSE terminal cannot be active for two dealers; on BSE it can.
	b := testDealer("Bimal", "AAAPB2222B", nseTerm("40001", "2026-10-15"))
	b.Relationship, b.Branch, b.Partner = DealerAP, "", "AP001"
	if _, err := o.SaveDealer(ctx, T, "vikram.shah", b); !fieldErr(err, "terminals[0].terminal_id") {
		t.Fatalf("duplicate terminal: %v", err)
	}
	b.Terminals[0].Exchange = "BSE"
	b.Partner = "RM01"
	if _, err := o.SaveDealer(ctx, T, "vikram.shah", b); !fieldErr(err, "partner") {
		t.Fatalf("an RM is not an AP: %v", err)
	}
	b.Partner = "AP001"
	if b, err = o.SaveDealer(ctx, T, "vikram.shah", b); err != nil || b.ID != "DL-0002" {
		t.Fatalf("AP dealer: %+v %v", b, err)
	}
	dup := testDealer("Asha", "AAAPA1111A")
	if _, err := o.SaveDealer(ctx, T, "priya.nair", dup); !fieldErr(err, "pan") {
		t.Fatalf("same PAN twice: %v", err)
	}
	// Viewing: risk sees the PAN masked; the maker sees it in full.
	if g, err := o.GetDealer(ctx, T, "anita.rao", a.ID); err != nil || g.PAN != MaskPAN("AAAPA1111A") {
		t.Fatalf("risk view: %+v %v", g, err)
	}
	g, err := o.GetDealer(ctx, T, "priya.nair", a.ID)
	if err != nil || g.PAN != "AAAPA1111A" {
		t.Fatalf("maker view: %+v %v", g, err)
	}
	// A change sent back with the masked PAN keeps the PAN; status cannot
	// be changed by saving.
	g.PAN, g.Status, g.Mobile = MaskPAN(g.PAN), DealerLeft, "9123456780"
	g.NISM = NISMCert{CertNo: "NISM-202600054321", ExamDate: "2026-10-20", ValidTill: "2029-10-19"}
	if g, err = o.SaveDealer(ctx, T, "vikram.shah", g); err != nil || g.PAN != "AAAPA1111A" || g.Status != DealerActive || g.Mobile != "9123456780" || len(g.NISMHistory) != 1 {
		t.Fatalf("modify: %+v %v", g, err)
	}
	// Leaving: compliance only.
	if _, err := o.MarkDealerLeft(ctx, T, "priya.nair", a.ID, LeftRequest{LeftOn: "2026-10-29"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker marked left: %v", err)
	}
	if _, err := o.MarkDealerLeft(ctx, T, "vikram.shah", a.ID, LeftRequest{LeftOn: "2026-11-29"}); !fieldErr(err, "left_on") {
		t.Fatalf("left in the future: %v", err)
	}
	l, err := o.MarkDealerLeft(ctx, T, "vikram.shah", a.ID, LeftRequest{LeftOn: "2026-10-29", CloseTerminals: true, Note: "resigned"})
	if err != nil || l.Status != DealerLeft || l.Terminals[0].ClosedOn != "2026-10-30" {
		t.Fatalf("left: %+v %v", l, err)
	}
	if _, err := o.MarkDealerLeft(ctx, T, "admin", a.ID, LeftRequest{LeftOn: "2026-10-29"}); !errors.Is(err, ErrState) {
		t.Fatalf("left twice: %v", err)
	}
	// Once closed, the terminal is free for another dealer.
	c := testDealer("Chitra", "AAAPC3333C", nseTerm("40001", "2026-10-30"))
	if _, err := o.SaveDealer(ctx, T, "priya.nair", c); err != nil {
		t.Fatalf("terminal reused: %v", err)
	}
	v, err := o.Dealers(ctx, T, "farhan.khan", "2026-10-30")
	if err != nil || v.Counts.Active != 2 || v.Counts.Left != 1 || v.Counts.Terminals != 2 || v.CanEdit || v.CanLeave {
		t.Fatalf("view: %+v %v", v.Counts, err)
	}
	for _, r := range v.Dealers {
		if r.PAN != r.PANMasked || strings.Contains(r.PAN, "1111") {
			t.Fatalf("PAN shown in the list: %+v", r)
		}
	}
	// Every change is logged, with the PAN masked.
	evs, _ := o.App.Audit.List(ctx, T, 0)
	n := 0
	for _, e := range evs {
		if strings.HasPrefix(e.Action, "dealer.") {
			n++
			b, _ := json.Marshal(e)
			if strings.Contains(string(b), "AAAPA1111A") || strings.Contains(string(b), "AAAPB2222B") {
				t.Fatalf("PAN in the audit log: %s", b)
			}
		}
	}
	if n < 5 {
		t.Fatalf("%d dealer events logged", n)
	}
}

func dealerWork(t *testing.T, o *Ops, ctx context.Context) map[string]WorkItem {
	t.Helper()
	ws, err := o.WorkItems(ctx, T, "")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]WorkItem{}
	for _, w := range ws {
		if w.Process == wDealers {
			out[strings.TrimPrefix(w.Key, wDealers+":")] = w
		}
	}
	return out
}

func TestDealerNISMAlerts(t *testing.T) {
	o, ctx := dealerOps(t, "2026-10-20")
	d := testDealer("Asha", "AAAPA1111A", nseTerm("40001", "2026-10-01"))
	d.NISM = NISMCert{CertNo: "NISM-202300000001", ExamDate: "2023-12-21", ValidTill: "2026-12-20"}
	d, err := o.SaveDealer(ctx, T, "priya.nair", d)
	if err != nil {
		t.Fatal(err)
	}
	tick := func(day string) map[string]WorkItem {
		t.Helper()
		dealerClock(o, day)
		o.dealersTick(ctx, T, o.Now())
		return dealerWork(t, o, ctx)
	}
	k := func(lv string) string { return d.ID + ":nism-" + lv + ":2026-12-20" }
	if w := tick("2026-10-20"); len(w) != 0 { // 61 days
		t.Fatalf("too early: %v", w)
	}
	w := tick("2026-10-21") // 60 days
	if len(w) != 1 || w[k("60")].Status != "Open" || w[k("60")].Role != RoleCompliance || w[k("60")].View != "dealers" {
		t.Fatalf("60 days: %+v", w)
	}
	// Once a day: a second run the same day does nothing; the next day the
	// same item stays (no new one).
	if w := tick("2026-10-21"); len(w) != 1 {
		t.Fatalf("same day: %v", w)
	}
	if _, err := o.CloseWorkItem(ctx, T, "vikram.shah", w[k("60")].ID, false, "renewal applied for"); err != nil {
		t.Fatal(err)
	}
	if w := tick("2026-10-22"); len(w) != 1 || w[k("60")].Status != "Dismissed" {
		t.Fatalf("dismissed item raised again: %+v", w)
	}
	w = tick("2026-11-20") // 30 days
	if len(w) != 2 || w[k("30")].Status != "Open" {
		t.Fatalf("30 days: %+v", w)
	}
	w = tick("2026-12-13") // 7 days
	if w[k("7")].Status != "Open" || w[k("30")].Status != "Done" {
		t.Fatalf("7 days: %+v", w)
	}
	w = tick("2026-12-20") // the last valid day
	if x := w[k("expiry")]; x.Status != "Open" || !strings.Contains(x.Title, "expires today") || w[k("7")].Status != "Done" {
		t.Fatalf("expiry: %+v", w)
	}
	w = tick("2026-12-21")
	if len(w) != 4 || w[k("expiry")].Status != "Open" {
		t.Fatalf("expired: %+v", w)
	}
	// Renewing closes the expiry item.
	d.NISM = NISMCert{CertNo: "NISM-202600000002", ExamDate: "2026-12-21", ValidTill: "2029-12-20"}
	if _, err := o.SaveDealer(ctx, T, "vikram.shah", d); err != nil {
		t.Fatal(err)
	}
	if w := dealerWork(t, o, ctx); w[k("expiry")].Status != "Done" {
		t.Fatalf("renewal: %+v", w[k("expiry")])
	}
	// A dealer who left with an open terminal.
	if _, err := o.MarkDealerLeft(ctx, T, "vikram.shah", d.ID, LeftRequest{LeftOn: "2026-12-21"}); err != nil {
		t.Fatal(err)
	}
	lk := d.ID + ":left-open:2026-12-21"
	w = tick("2026-12-22")
	if w[lk].Status != "Open" || !strings.Contains(w[lk].Title, "close 1 terminal") {
		t.Fatalf("left with terminals: %+v", w)
	}
	g, _ := o.GetDealer(ctx, T, "priya.nair", d.ID)
	g.Terminals[0].ClosedOn = "2026-12-22"
	if _, err := o.SaveDealer(ctx, T, "priya.nair", g); err != nil {
		t.Fatal(err)
	}
	if w := tick("2026-12-23"); w[lk].Status != "Done" {
		t.Fatalf("closed terminals: %+v", w[lk])
	}
}

func TestDealerFor(t *testing.T) {
	o, ctx := dealerOps(t, "2026-10-30")
	a := testDealer("Asha", "AAAPA1111A", DealerTerminal{Exchange: "NSE", Segments: []string{"CM"}, TerminalID: "40001", ActiveFrom: "2026-01-01", ClosedOn: "2026-06-01"},
		DealerTerminal{Exchange: "BSE", Segments: []string{"CM"}, TerminalID: "555", CTCLID: "4000010001001234", ActiveFrom: "2026-01-01"})
	b := testDealer("Bimal", "AAAPB2222B", nseTerm("40001", "2026-06-01"))
	c := testDealer("Chitra", "AAAPC3333C", nseTerm("333*", "2026-01-01"))
	e := testDealer("Esha", "AAAPE5555E", nseTerm("33399", "2026-01-01"))
	ids := []string{}
	for _, d := range []Dealer{a, b, c, e} {
		x, err := o.SaveDealer(ctx, T, "priya.nair", d)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, x.ID)
	}
	for _, x := range []struct{ ex, term, day, want string }{
		{"NSE", "40001", "2026-03-01", ids[0]},
		{"NSE", "40001", "2026-05-31", ids[0]},
		{"NSE", "40001", "2026-06-01", ids[1]}, // closed on the 1st: the new dealer's from that day
		{"NSE", "40001", "2025-12-31", ""},
		{"nse", " 33345 ", "2026-03-01", ids[2]}, // pattern
		{"NSE", "33399", "2026-03-01", ids[3]},   // exact beats the pattern
		{"BSE", "33345", "2026-03-01", ""},       // patterns are per exchange
		{"BSE", "4000010001001234", "2026-03-01", ids[0]},
		{"BSE", "555", "2026-03-01", ids[0]},
		{"NSE", "", "2026-03-01", ""},
	} {
		d, ok := o.DealerFor(ctx, T, x.ex, x.term, x.day)
		if ok != (x.want != "") || d.ID != x.want {
			t.Errorf("%s %q %s: got %s %v, want %s", x.ex, x.term, x.day, d.ID, ok, x.want)
		}
	}
}

func dealerTrade(ucc, ex, term, date, no string, qty int64, px string) ingest.TradeEvent {
	return ingest.TradeEvent{Tenant: T, Origin: ingest.FromExchange, Exchange: ex, Segment: "CM", TradeNo: no, OrderNo: "O" + no, UCC: ucc, Symbol: "XTEST",
		InstrumentType: "EQ", Side: "B", Qty: qty, Price: money.MustDec(px), TradeDate: date, TradeTime: "10:00:00", Product: "CNC", Terminal: term}
}

func TestDealerReportExceptions(t *testing.T) {
	o, ctx := dealerOps(t, "2026-10-13")
	a := testDealer("Asha", "AAAPA1111A", nseTerm("40001", "2026-01-01"))
	a.NISM = NISMCert{CertNo: "NISM-202310140001", ExamDate: "2023-10-15", ValidTill: "2026-10-14"}
	a, err := o.SaveDealer(ctx, T, "priya.nair", a)
	if err != nil {
		t.Fatal(err)
	}
	b := testDealer("Bimal", "AAAPB2222B", nseTerm("333*", "2026-01-01"))
	if b, err = o.SaveDealer(ctx, T, "priya.nair", b); err != nil {
		t.Fatal(err)
	}
	dealerClock(o, "2026-10-30")
	if _, _, err := o.App.Trades.Add(ctx, []ingest.TradeEvent{
		dealerTrade("C1", "NSE", "40001", "2026-10-14", "1", 10, "100"),
		dealerTrade("C2", "NSE", "40001", "2026-10-15", "2", 20, "100"),
		dealerTrade("C1", "NSE", "40001", "2026-10-15", "3", 5, "100"),
		dealerTrade("C3", "NSE", "33310", "2026-10-15", "4", 1, "1000"),
		dealerTrade("C3", "NSE", "99999", "2026-10-15", "5", 3, "100"),
		dealerTrade("C1", "NSE", "", "2026-10-15", "6", 1, "50"),
		dealerTrade("C1", "BSE", "40001", "2026-10-15", "7", 2, "100"),
	}); err != nil {
		t.Fatal(err)
	}
	// Brokerage billed for C1 on NSE-CM on the 15th: ₹30, shared by turnover
	// (dealer Asha ₹500, no dealer ₹50).
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "BILL:test", ValueDate: "2026-10-15", Narration: "bill", Source: ledger.SourceRef{Kind: "BILL", ID: "2026-10-15/C1/NSE-CM"}, CreatedBy: "test",
		Postings: []ledger.Posting{{Account: ledger.ClientAccount("C1"), Debit: 3000}, {Account: ledger.IncomeBrokerage, Credit: 3000}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.DealerBusiness(ctx, T, "anita.rao", "2026-10-15", "2026-10-14"); err == nil {
		t.Fatal("reversed period accepted")
	}
	r, err := o.DealerBusiness(ctx, T, "anita.rao", "2026-10-14", "2026-10-15")
	if err != nil {
		t.Fatal(err)
	}
	if r.Trades != 7 || r.Attributed != 4 || len(r.Rows) != 2 {
		t.Fatalf("report: %+v", r)
	}
	ra, rb := r.Rows[0], r.Rows[1]
	if ra.DealerID != a.ID || ra.Trades != 3 || ra.Clients != 2 || ra.Turnover.Fixed(2) != "3500.00" || rb.DealerID != b.ID || rb.Turnover.Fixed(2) != "1000.00" {
		t.Fatalf("rows: %+v", r.Rows)
	}
	if !r.BrokerageOn || ra.Brokerage != 2727 || r.BrokerageOff != 273 || rb.Brokerage != 0 {
		t.Fatalf("brokerage: %v %v %v", ra.Brokerage, rb.Brokerage, r.BrokerageOff)
	}
	if len(r.Unattributed) != 3 {
		t.Fatalf("unattributed: %+v", r.Unattributed)
	}
	// Only the 15th: Asha's certificate was valid till the 14th.
	if len(r.ExpiredNISM) != 1 {
		t.Fatalf("expired NISM: %+v", r.ExpiredNISM)
	}
	e := r.ExpiredNISM[0]
	if e.Date != "2026-10-15" || e.DealerID != a.ID || e.Trades != 2 || e.Clients != 2 || e.Turnover.Fixed(2) != "2500.00" || e.ValidTill != "2026-10-14" {
		t.Fatalf("expired row: %+v", e)
	}
	// Renewing does not hide the lapse: the old certificate is kept.
	a.NISM = NISMCert{CertNo: "NISM-202610200002", ExamDate: "2026-10-20", ValidTill: "2029-10-19"}
	if _, err := o.SaveDealer(ctx, T, "priya.nair", a); err != nil {
		t.Fatal(err)
	}
	if r, _ = o.DealerBusiness(ctx, T, "anita.rao", "2026-10-14", "2026-10-15"); len(r.ExpiredNISM) != 1 || r.ExpiredNISM[0].Date != "2026-10-15" {
		t.Fatalf("after renewal: %+v", r.ExpiredNISM)
	}
	v, err := o.Dealers(ctx, T, "anita.rao", "2026-10-15")
	if err != nil || v.Counts.Unattributed != 3 {
		t.Fatalf("view: %+v %v", v.Counts, err)
	}
}

func TestBSEDealerFiles(t *testing.T) {
	o, ctx := dealerOps(t, "2026-10-30")
	d := testDealer("Test", "AAAPZ1234C", DealerTerminal{Exchange: "BSE", Segments: []string{"CM"}, TerminalID: "555", LocationID: "4000010001001", BranchID: "5555", Vendor: "08",
		Algo: true, TradingID: "202", ActiveFrom: "2026-10-01"}, nseTerm("40001", "2026-10-01"))
	d.Email = "test.dealer@example.invalid"
	d, err := o.SaveDealer(ctx, T, "priya.nair", d)
	if err != nil {
		t.Fatal(err)
	}
	file := func(kind string, ids ...string) DealerFile {
		t.Helper()
		f, err := o.MakeDealerFile(ctx, T, "priya.nair", kind, ids, "", false)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if strings.HasSuffix(string(f.Content), "\n\n") || !strings.HasSuffix(string(f.Content), "\n") || strings.Contains(string(f.Content), "\r") {
			t.Fatalf("%s must end in exactly one newline: %q", kind, f.Content)
		}
		return f
	}
	if _, err := o.MakeDealerFile(ctx, T, "anita.rao", "bse-dealer", nil, "", false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk made a dealer file: %v", err)
	}
	if _, err := o.MakeDealerFile(ctx, T, "priya.nair", "nse-dealer", nil, "", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("NSE: %v", err)
	}
	reg := "A|1|400001|5555|555|1|08|Colaba Causeway|Colaba|MUMBAI|400005|0|Test||Dealer|test.dealer@example.invalid||9876543210|||0|1990-01-01|Flat 1|Andheri|MUMBAI|400058|0|Y|0|AAAPZ1234C|Y|N|N|N|N|Y|202|"
	if len(strings.Split(reg, "|")) != 38 {
		t.Fatal("golden line has the wrong field count")
	}
	f := file("bse-dealer")
	if string(f.Content) != reg+"\n" || f.Name != "BSE_DEALER_REG_30102026.txt" || f.Rows != 1 {
		t.Fatalf("registration:\n%q\n%q %s", f.Content, reg+"\n", f.Name)
	}
	pan := "A|4000010001001|Test||Dealer|400001|Colaba|MUMBAI|Maharashtra|Colaba Causeway|1990-01-01|AAAPZ1234C\n"
	if f := file("bse-dealer-pan"); string(f.Content) != pan || f.Name != "BSE_DEALER_PAN_30102026.txt" {
		t.Fatalf("PAN file:\n%q\n%q", f.Content, pan)
	}
	nism := "A|AAAPZ1234C|Test||Dealer|EQD|NISM-201900012345|01/31/2024|01/30/2027|\n"
	if f := file("bse-nism", d.ID); string(f.Content) != nism || f.Name != "BSE_NISM_30102026.txt" {
		t.Fatalf("NISM file:\n%q\n%q", f.Content, nism)
	}
	if f := file("bse-nism"); string(f.Content) != "M"+nism[1:] {
		t.Fatalf("NISM again: %q", f.Content)
	}
	// BSE's record ID makes the row a modification; closing with purge asks
	// for permanent deletion.
	d, _ = o.GetDealer(ctx, T, "priya.nair", d.ID)
	d.Terminals[0].ExchangeRecordID = "R77"
	d.NISM = NISMCert{CertNo: "NISM-202600099999", ExamDate: "2026-10-20", ValidTill: "2029-10-19"}
	if d, err = o.SaveDealer(ctx, T, "priya.nair", d); err != nil {
		t.Fatal(err)
	}
	if f := file("bse-dealer"); string(f.Content) != "M"+reg[1:]+"R77\n" {
		t.Fatalf("modify: %q", f.Content)
	}
	if f := file("bse-nism"); string(f.Content) != "RN|AAAPZ1234C|Test||Dealer|EQD|NISM-202600099999|10/20/2026|10/19/2029|\n" {
		t.Fatalf("renewal: %q", f.Content)
	}
	d.Terminals[0].ClosedOn, d.Terminals[0].Purge = "2026-10-30", true
	if _, err = o.SaveDealer(ctx, T, "priya.nair", d); err != nil {
		t.Fatal(err)
	}
	want := "M" + strings.Replace(reg[1:], "|Y|0|AAAPZ1234C|", "|P|0|AAAPZ1234C|", 1) + "R77\n"
	if f := file("bse-dealer"); string(f.Content) != want {
		t.Fatalf("permanent deletion:\n%q\n%q", f.Content, want)
	}
	// A BSE terminal given as a pattern cannot be registered: listed when
	// checking, refused when making.
	x := testDealer("Pat", "AAAPP7777P", DealerTerminal{Exchange: "BSE", Segments: []string{"CM", "FO"}, TerminalID: "77*", ActiveFrom: "2026-10-01"})
	if x, err = o.SaveDealer(ctx, T, "priya.nair", x); err != nil {
		t.Fatal(err)
	}
	c, err := o.MakeDealerFile(ctx, T, "vikram.shah", "bse-dealer", nil, "", true)
	if err != nil || len(c.Problems) != 1 || !strings.Contains(c.Problems[0], "pattern") || c.Content != nil {
		t.Fatalf("check: %+v %v", c, err)
	}
	if _, err := o.MakeDealerFile(ctx, T, "vikram.shah", "bse-dealer", nil, "", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pattern written: %v", err)
	}
}

func TestDealerImportErrors(t *testing.T) {
	o, ctx := dealerOps(t, "2026-10-30")
	head := "PAN,FirstName,LastName,DOB,Relationship,Branch,Partner,NISMCertNo,NISMExamDate,NISMValidTill,Status,LeftOn,Exchange,Segments,Terminal,ActiveFrom,ExchangeRecordID\n"
	file := head +
		"AAAPA1111A,Asha,Rao,1990-01-01,Employee,Mumbai,,NISM-1,2024-01-31,2027-01-30,,,NSE,CM;FO,40001,2026-10-01,\n" + // 2
		"AAAPA1111A,Asha,Rao,,,,,,,,,,BSE,CM,555,2026-10-01,\n" + // 3
		"AAAPB222,Bimal,Shah,1990-01-01,AP,,AP001,NISM-2,2024-01-31,2027-01-30,,,NSE,CM,40002,2026-10-01,\n" + // 4: bad PAN
		"AAAPC3333C,Chitra,Iyer,1990-01-01,Employee,Mumbai,,NISM-3,2024-01-31,2027-01-30,,,NYSE,CM,40003,2026-10-01,\n" + // 5: bad exchange
		"AAAPD4444D,Dev,Nair,1990-01-01,Employee,Mumbai,,NISM-4,2024-01-31,2027-01-30,,,NSE,CM,40001,2026-10-01,\n" + // 6: terminal taken
		"AAAPA1111A,Asha,Kumar,,,,,,,,,,NSE,FO,40011,2026-10-01,\n" + // 7: name differs
		"AAAPE5555E,Esha,Pai,1990-01-01,Employee,Mumbai,,,,,Left,2026-10-01,,,,,\n" // 8: maker cannot mark left
	res, err := o.ImportDealers(ctx, T, "priya.nair", []byte(file))
	if !errors.Is(err, ingest.ErrRejected) {
		t.Fatalf("bad file accepted: %+v %v", res, err)
	}
	got := map[int]string{}
	for _, e := range res.Errors {
		got[e.Line] += e.Field + ":" + e.Msg + "; "
	}
	for line, field := range map[int]string{4: "PAN", 5: "exchange", 6: "Terminal", 7: "LastName", 8: "Status"} {
		if !strings.Contains(got[line], field+":") {
			t.Errorf("line %d: want a %s error, got %q", line, field, got[line])
		}
	}
	if got[2] != "" || got[3] != "" {
		t.Errorf("good lines flagged: %v", got)
	}
	if ds, _ := o.allDealers(ctx, T); len(ds) != 0 {
		t.Fatalf("a rejected file saved %d dealers", len(ds))
	}
	if _, err := o.ImportDealers(ctx, T, "farhan.khan", []byte(head)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("checker imported: %v", err)
	}
	if res, err := o.ImportDealers(ctx, T, "priya.nair", []byte("Name\nx\n")); !errors.Is(err, ingest.ErrRejected) || res.Errors[0].Line != 1 {
		t.Fatalf("missing columns: %+v %v", res, err)
	}
	good := head + "AAAPA1111A,Asha,Rao,1990-01-01,Employee,Mumbai,,NISM-1,2024-01-31,2027-01-30,,,NSE,CM;FO,40001,2026-10-01,\n" +
		"AAAPA1111A,Asha,Rao,,,,,,,,,,BSE,CM,555,2026-10-01,R9\n"
	res, err = o.ImportDealers(ctx, T, "priya.nair", []byte(good))
	if err != nil || res.Added != 1 || res.IDs[0] != "DL-0001" {
		t.Fatalf("good file: %+v %v", res, err)
	}
	// Again without the record ID: matched by PAN, the record ID kept.
	res, err = o.ImportDealers(ctx, T, "vikram.shah", []byte(strings.Replace(good, ",R9\n", ",\n", 1)))
	if err != nil || res.Updated != 1 || res.Added != 0 {
		t.Fatalf("re-import: %+v %v", res, err)
	}
	d, _ := o.GetDealer(ctx, T, "priya.nair", "DL-0001")
	if len(d.Terminals) != 2 || d.Terminals[1].ExchangeRecordID != "R9" || d.Terminals[0].Segments[1] != "FO" {
		t.Fatalf("dealer: %+v", d.Terminals)
	}
	// Export and import back: nothing changes.
	out, err := o.ExportDealersCSV(ctx, T, "priya.nair")
	if err != nil || !strings.HasPrefix(string(out), strings.Join(dealerCSVColumns, ",")) || !strings.Contains(string(out), "AAAPA1111A") {
		t.Fatalf("export: %s %v", out, err)
	}
	if _, err := o.ExportDealersCSV(ctx, T, "anita.rao"); !errors.Is(err, ErrForbidden) {
		t.Fatal("risk exported PANs")
	}
	if res, err = o.ImportDealers(ctx, T, "priya.nair", out); err != nil || res.Updated != 1 {
		t.Fatalf("round trip: %+v %v", res, err)
	}
	// The inspection list masks the PAN, one row per terminal and segment.
	ins, err := o.InspectionCSV(ctx, T, "anita.rao")
	if err != nil || strings.Contains(string(ins), "AAAPA1111A") || !strings.Contains(string(ins), MaskPAN("AAAPA1111A")) || strings.Count(string(ins), "\n") != 4 {
		t.Fatalf("inspection:\n%s %v", ins, err)
	}
}
