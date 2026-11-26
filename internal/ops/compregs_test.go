package ops

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/xfile"
)

// The tax registers and checks: certificates, PAN status, purchases of
// goods, GSTR-2B and the 180-day reversal.
func TestRegistersAndVendorTDS(t *testing.T) {
	o, ctx := setup(t)
	rs := money.MustAmount
	v, err := o.SaveVendor(ctx, T, "priya.nair", Vendor{Name: "Made-up Paper Traders", GSTIN: testVendorGSTIN, TDSSection: "194J", Expense: "EXPENSE:OFFICE"})
	if err != nil {
		t.Fatal(err)
	}
	// A register line is checked against its fields and kind.
	if _, err := o.AddCompRecord(ctx, T, "priya.nair", "ldc", map[string]string{"vendor": v.ID, "section": "194J", "cert_no": "LDC1", "rate": "2", "limit": "100000", "from": "2026-10-10", "to": "2026-10-01"}, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("certificate ending before it starts: %v", err)
	}
	if _, err := o.AddCompRecord(ctx, T, "priya.nair", "nope", nil, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown register: %v", err)
	}
	if _, err := o.AddCompRecord(ctx, T, "anita.rao", "ldc", map[string]string{}, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk recorded a certificate: %v", err)
	}
	ldc, err := o.AddCompRecord(ctx, T, "priya.nair", "ldc", map[string]string{"vendor": strings.ToLower(v.ID), "section": "194j", "cert_no": "LDC1", "rate": "2", "limit": "100000", "from": "2026-10-01", "to": "2027-03-31"}, "")
	if err != nil || ldc.V("section") != "194J" || ldc.V("vendor") != v.ID || ldc.V("limit") != "100000.00" {
		t.Fatalf("certificate %+v %v", ldc, err)
	}
	// Under the certificate, 10% is flagged; 2% is not.
	b1, err := o.SaveVendorBill(ctx, T, "priya.nair", VendorBill{Vendor: v.ID, BillNo: "P-1", BillDate: "2026-10-05", Taxable: rs("50000"), TDSSection: "194J", TDS: rs("5000")})
	if err != nil || !strings.Contains(strings.Join(b1.Warnings, " "), "LDC1 (2%") {
		t.Fatalf("certificate rate not suggested: %v %v", b1.Warnings, err)
	}
	b2, err := o.SaveVendorBill(ctx, T, "priya.nair", VendorBill{Vendor: v.ID, BillNo: "P-2", BillDate: "2026-10-06", Taxable: rs("40000"), TDSSection: "194J", TDS: rs("800")})
	if err != nil || len(b2.Warnings) != 0 {
		t.Fatalf("certificate rate flagged: %v %v", b2.Warnings, err)
	}
	// The next bill goes past the certificate's amount.
	b3, _ := o.SaveVendorBill(ctx, T, "priya.nair", VendorBill{Vendor: v.ID, BillNo: "P-3", BillDate: "2026-10-07", Taxable: rs("20000"), TDSSection: "194J", TDS: rs("400")})
	if !strings.Contains(strings.Join(b3.Warnings, " "), "covers only") {
		t.Fatalf("certificate limit: %v", b3.Warnings)
	}
	// An inoperative PAN: the higher rate.
	if _, err := o.AddCompRecord(ctx, T, "priya.nair", "pan-status", map[string]string{"vendor": v.ID, "status": "Inoperative", "checked_on": "2026-10-08"}, ""); err != nil {
		t.Fatal(err)
	}
	g, _ := o.SaveVendor(ctx, T, "priya.nair", Vendor{Name: "Made-up Goods Co", TDSSection: "194Q", PAN: "AAACM1234Q", Expense: "EXPENSE:OFFICE"})
	if _, err := o.AddCompRecord(ctx, T, "priya.nair", "pan-status", map[string]string{"vendor": g.ID, "status": "Inoperative", "checked_on": "2026-10-08"}, ""); err != nil {
		t.Fatal(err)
	}
	b4, _ := o.SaveVendorBill(ctx, T, "priya.nair", VendorBill{Vendor: g.ID, BillNo: "G-1", BillDate: "2026-10-09", Taxable: rs("10000"), TDSSection: "194Q", TDS: rs("10")})
	if !strings.Contains(strings.Join(b4.Warnings, " "), "higher rate of 5%") {
		t.Fatalf("inoperative PAN for goods: %v", b4.Warnings)
	}
	if noPANRate("194Q") != "5" || tdsRate("194J", "") != "20" || tdsRate("194Q", "") != "5" {
		t.Fatal("no-PAN rates")
	}
	// Purchases of goods: only beyond ₹50 lakh, only above ₹10 crore turnover.
	if _, err := o.AddCompRecord(ctx, T, "priya.nair", "pan-status", map[string]string{"vendor": g.ID, "status": "Operative", "checked_on": "2026-10-10"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetFirmPractice(ctx, T, "admin", "tax-turnover", PracticeAnswer{Option: "above", Values: map[string]string{"fy": "2026-27"}}); err != nil {
		t.Fatal(err)
	}
	b5, _ := o.SaveVendorBill(ctx, T, "priya.nair", VendorBill{Vendor: g.ID, BillNo: "G-2", BillDate: "2026-10-10", Taxable: rs("5000000"), TDSSection: "194Q", TDS: rs("5000")})
	if !strings.Contains(strings.Join(b5.Warnings, " "), "beyond ₹50 lakh") {
		t.Fatalf("goods threshold: %v", b5.Warnings)
	}
	// TDS certificates due: Q3's certificate is due 15 February.
	if c := tdsCertDue("2026-27", "Q3"); c != "2027-02-15" || tdsCertDue("2026-27", "Q4") != "2027-06-15" || quarterOf("2026-10-05") != "Q3" {
		t.Fatalf("certificate due %s", c)
	}
	due := o.TDSCertsDue(ctx, T)
	if len(due) == 0 || due[0].Due != "2027-02-15" || due[0].Issued != "" {
		t.Fatalf("certificates due %+v", due)
	}
	if _, err := o.AddCompRecord(ctx, T, "farhan.khan", "tds-cert", map[string]string{"payee_kind": "Vendor", "payee": due[0].Payee, "fy": "2026-27", "quarter": "Q3", "cert_no": "TRC1", "issued_on": "2027-02-10"}, ""); err != nil {
		t.Fatal(err)
	}
	if d := o.TDSCertsDue(ctx, T); d[0].Issued != "TRC1" {
		t.Fatalf("certificate not matched %+v", d[0])
	}
	csv, err := o.TDSLowerCSV(ctx, T, "farhan.khan", "2026-10-01", "2026-10-31")
	if err != nil || !strings.Contains(string(csv), "LDC1") {
		t.Fatalf("lower deduction CSV %s %v", csv, err)
	}
	// Withdrawn records no longer count.
	if _, err := o.WithdrawCompRecord(ctx, T, "priya.nair", ldc.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("withdrawn without a reason")
	}
	if _, err := o.WithdrawCompRecord(ctx, T, "priya.nair", ldc.ID, "wrong number"); err != nil || len(o.compRecs(ctx, T, "ldc")) != 0 {
		t.Fatalf("withdraw %v", err)
	}

	// GSTR-2B: the portal's JSON; the bill with credit is found, the other not.
	itc, err := o.SaveVendorBill(ctx, T, "priya.nair", VendorBill{Vendor: v.ID, BillNo: "INV/77", BillDate: "2026-04-10", Taxable: rs("1000"), CGST: rs("90"), SGST: rs("90"), ITC: true})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := o.SaveVendorBill(ctx, T, "priya.nair", VendorBill{Vendor: v.ID, BillNo: "INV/78", BillDate: "2026-04-11", Taxable: rs("1000"), CGST: rs("90"), SGST: rs("90"), ITC: true})
	js := `{"data":{"docdata":{"b2b":[{"ctin":"` + testVendorGSTIN + `","trdnm":"Made-up","inv":[{"inum":"inv/77","dt":"10-04-2026","val":1180,"itcavl":"Y","items":[{"txval":1000,"cgst":90,"sgst":90,"igst":0}]}]}]}}}`
	if _, err := o.LoadGSTR2B(ctx, T, "farhan.khan", "2026-04", "2b.json", []byte(js)); err != nil {
		t.Fatal(err)
	}
	m := o.itcMatch(ctx, T)
	st := map[string]string{}
	for _, r := range m.Rows {
		st[r.Bill] = r.Status
	}
	if st[itc.ID] != "In GSTR-2B" || st[other.ID] != "Not in GSTR-2B" {
		t.Fatalf("2B match %v", st)
	}
	rows, err := parseGSTR2BCSV([]byte("GSTR-2B B2B\nGSTIN of supplier,Trade/Legal name,Invoice number,Invoice Date,Taxable Value (₹),Integrated Tax(₹),Central Tax(₹),State/UT Tax(₹),ITC Availability\n" + testVendorGSTIN + ",X,A1,10-04-2026,\"1,000.00\",0,90,90,Yes\n"))
	if err != nil || len(rows) != 1 || rows[0].Taxable != rs("1000") || rows[0].Date != "2026-04-10" {
		t.Fatalf("2B CSV %+v %v", rows, err)
	}
	// Unpaid 180 days after the bill date: the credit on it is reversed.
	u := o.itcUnpaid180(ctx, T, "2026-10-20")
	if len(u) != 2 || u[0].Reverse != rs("180") {
		t.Fatalf("180 days %+v", u)
	}
	if len(o.itcUnpaid180(ctx, T, "2026-10-06")) != 0 {
		t.Fatal("reversal before 180 days")
	}
}

// A bill reversed after its month's GSTR-1 was due becomes a note of the
// correction's month.
func TestGSTLateAdjustments(t *testing.T) {
	at := func(d string) time.Time { tm, _ := time.ParseInLocation("2006-01-02 15:04", d, ist); return tm }
	orig := ledger.Entry{ID: "E1", ValueDate: "2026-09-20", Source: ledger.SourceRef{Kind: "BILL", ID: "2026-09-20/AB001"}, CreatedAt: at("2026-09-20 20:00")}
	early := ledger.Entry{ID: "E2", ValueDate: "2026-09-20", Source: ledger.SourceRef{Kind: "REVERSAL", ID: "E1"}, ReversalOf: "E1", CreatedAt: at("2026-10-05 10:00")}
	rebill := ledger.Entry{ID: "E3", ValueDate: "2026-09-20", Source: ledger.SourceRef{Kind: "BILL", ID: "2026-09-20/AB001"}, CreatedAt: at("2026-10-05 10:01")}
	late := ledger.Entry{ID: "E4", ValueDate: "2026-09-20", Source: ledger.SourceRef{Kind: "REVERSAL", ID: "E3"}, ReversalOf: "E3", CreatedAt: at("2026-11-02 11:00")}
	again := ledger.Entry{ID: "E5", ValueDate: "2026-09-20", Source: ledger.SourceRef{Kind: "BILL", ID: "2026-09-20/AB001"}, CreatedAt: at("2026-11-02 11:01")}
	all := []ledger.Entry{orig, early, rebill, late, again}
	by := map[string]ledger.Entry{}
	for _, e := range all {
		by[e.ID] = e
	}
	adj := gstLateAdjustments(all, by)
	if len(adj) != 2 || adj["E4"] != "2026-09-20/AB001|2026-11-02" || adj["E5"] != adj["E4"] {
		t.Fatalf("late adjustments %v", adj)
	}
	if gstr1Cut("2026-09-20") != "2026-10-10" || gstr1Cut("2026-12-31") != "2027-01-10" {
		t.Fatal("GSTR-1 cut-off")
	}
}

// AP holds, notices, the website's lists, NSE user types.
func TestAPControlsAndWebsite(t *testing.T) {
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
	if _, err := o.SetAPStatus(ctx, T, "vikram.shah", APStatus{Code: "AP001", Hold: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("hold without a reason: %v", err)
	}
	if _, err := o.SetAPStatus(ctx, T, "vikram.shah", APStatus{Code: "AP001", Hold: true, HoldReason: "client complaints of unauthorised trades"}); err != nil {
		t.Fatal(err)
	}
	pays, err := o.APPayouts(ctx, T, D, D)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pays {
		if p.Partner == "AP001" && (p.Payable != 0 || p.Withheld == 0 || !strings.Contains(p.Held, "unauthorised")) {
			t.Fatalf("held payout %+v", p)
		}
	}
	// Cancelled for disciplinary reasons: on the website's second list.
	if _, err := o.SetAPStatus(ctx, T, "vikram.shah", APStatus{Code: "AP001", CancelledOn: D, CancelKind: APCancelDisciplinary, CancelReason: "funds taken from clients"}); err != nil {
		t.Fatal(err)
	}
	l := o.websiteAPs(ctx, T)
	if len(l.Cancelled) != 1 || l.Cancelled[0].Code != "AP001" || !strings.Contains(l.HTML, "funds taken from clients") || !l.Changed {
		t.Fatalf("AP list %+v", l)
	}
	if _, err := o.MarkWebsitePublished(ctx, T, "vikram.shah", "Authorised persons"); err != nil {
		t.Fatal(err)
	}
	if o.websiteAPs(ctx, T).Changed {
		t.Fatal("published list still changed")
	}
	// The USCNBA text changes with the register.
	before := o.websiteUSCNBA(ctx, T)
	if _, err := o.SaveFirmBank(ctx, T, "vikram.shah", FirmBankReq{FirmBank: FirmBank{Kind: BankUSCNBA, Bank: "Made-up Bank", Account: "000555666777", IFSC: "MADE0000009", Reported: true}}); err != nil {
		t.Fatal(err)
	}
	after := o.websiteUSCNBA(ctx, T)
	if after.Version == before.Version || !strings.Contains(after.Text, "000555666777") || !after.Changed || strings.Contains(after.Accounts[len(after.Accounts)-1].Account, "555666") {
		t.Fatalf("USCNBA text %+v", after)
	}
	o.websiteTick(ctx, T, D)
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-website-USCNBAaccounts-"+after.Version); err != nil || w.Role != RoleCompliance {
		t.Fatalf("no website work item: %v", err)
	}
	// Notices go before the change only.
	if _, err := o.NotifyAPChange(ctx, T, "vikram.shah", "HO", "AP001", "", D); !errors.Is(err, ErrInvalid) {
		t.Fatalf("notice on the day of the change: %v", err)
	}
	// NSE user types.
	if _, err := o.SetNSEUserType(ctx, T, "priya.nair", "T1", "Super user"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown type: %v", err)
	}
	x, err := o.SetNSEUserType(ctx, T, "priya.nair", "t1", "Dealer")
	if err != nil || x.Types["T1"] != "Dealer" {
		t.Fatalf("user type %+v %v", x, err)
	}
}

// Receipts by demand draft, closure channels, DIS set-up and TLH, the "to
// be closed" list, and the round's self-inspection tests on a sample day.
func TestReceiptsClosuresDP(t *testing.T) {
	o, ctx := setup(t)
	rs := money.MustAmount
	c := Client{UCC: "DD001", Name: "Draft Client", Status: "Active", Email: "dd@example.invalid", Tags: []string{}, Bank: BankAccount{Bank: "HDFC Bank", IFSC: "HDFC0001234", Account: "50100011112222"}}
	if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: D, UCC: "DD001", Mode: ModeDD, Ref: "DD1", Amount: rs("30000")}); err != nil {
		t.Fatal(err)
	}
	// A second draft the same day takes the client to ₹50,000.
	if _, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: D, UCC: "DD001", Mode: ModeDD, Ref: "DD2", Amount: rs("20000")}); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("draft without the bank's certificate: %v", err)
	}
	if _, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: D, UCC: "DD001", Mode: ModeDD, Ref: "DD2", Amount: rs("20000"), BankCertName: "Draft Client", BankCertAccount: "999900001111"}); err == nil || !strings.Contains(err.Error(), "third-party") {
		t.Fatalf("draft from another account: %v", err)
	}
	r, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: D, UCC: "DD001", Mode: ModeDD, Ref: "DD2", Amount: rs("20000"), BankCertName: "Draft Client", BankCertAccount: "50100011112222"})
	if err != nil {
		t.Fatal(err)
	}
	if r, err = o.DecideReceipt(ctx, T, "farhan.khan", r.ID, true, ""); err != nil || r.Status != "Uncleared" {
		t.Fatalf("a draft is credited once realised: %+v %v", r, err)
	}
	// Closure requests: not by SMS; e-mail only from the registered one.
	if _, err := o.RequestClosureFrom(ctx, T, "priya.nair", "DD001", D, "WhatsApp", "", ""); err == nil || !strings.Contains(err.Error(), "messaging") {
		t.Fatalf("closure by WhatsApp: %v", err)
	}
	if _, err := o.RequestClosureFrom(ctx, T, "priya.nair", "DD001", D, ClosureByEmail, "", "other@example.invalid"); err == nil {
		t.Fatal("closure from another e-mail")
	}
	if x, err := o.RequestClosureFrom(ctx, T, "priya.nair", "DD001", D, ClosureByEmail, "", "DD@example.invalid"); err != nil || x.FromEmail != "dd@example.invalid" {
		t.Fatalf("closure by registered e-mail: %+v %v", x, err)
	}

	// DIS: the set-up date and the delay; reason Others needs words.
	h := firstHolding(t, o, "BH1001")
	book, err := o.IssueDISBook(ctx, T, "priya.nair", "BH1001", 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: "BH1001", Slip: book.From, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Others", ExecDate: D, Received: D, Lines: []DISLine{{ISIN: h.ISIN, Qty: money.DecInt(1)}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reason Others without words: %v", err)
	}
	recv := addDays(o.now().Format("2006-01-02"), -5)
	d, err := o.EnterDIS(ctx, T, "priya.nair", DIS{UCC: "BH1001", Slip: book.From, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Sale (off-market)", Consideration: rs("10000"), ExecDate: o.now().Format("2006-01-02"), Received: recv, Lines: []DISLine{{ISIN: h.ISIN, Qty: money.DecInt(1)}}})
	if err != nil || d.DelayDays != 5 || d.StampEst != rs("1.50") {
		t.Fatalf("slip %+v %v", d, err)
	}
	if _, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-dis-delay-"+d.ID); err != nil {
		t.Fatalf("no work item for the late set-up: %v", err)
	}
	if d, err = o.RecordDISDelayReason(ctx, T, "priya.nair", d.ID, "client's signature needed checking at the branch"); err != nil || d.DelayReason == "" {
		t.Fatal(err)
	}
	// A transmission slip goes to CDSL as TLH.
	tlh := DIS{ID: "DIS-T", UCC: "BH1001", BOID: "1208160000000001", Kind: "Off-market", ToBO: "1208160099999999", Reason: "Transmission", Slip: 7, ExecDate: D, Received: D, Lines: []DISLine{{ISIN: h.ISIN, Qty: money.DecInt(1)}}}
	x := cdslConn{c: Connection{Values: map[string]string{}}, User: "DPUSER01"}
	rows, err := o.cdxDISRows(ctx, T, x, []DIS{tlh}, "B1")
	if err != nil || len(rows) != 1 || rows[0].Values["TrdRsnCd"] != "TLH" {
		t.Fatalf("TLH: %+v %v", rows, err)
	}

	// CDSL's "to be closed" list: no AMC on the account.
	bo := o.boFor(ctx, T, mustClient(t, o, "BH1001"), "")
	if _, err := o.LoadToBeClosed(ctx, T, "farhan.khan", D, "tbc.txt", []byte("SR,BOID\n1,"+bo.BOID+"\n2,9999999999999999\n")); err != nil {
		t.Fatal(err)
	}
	v, err := o.DP(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range v.Rows {
		if normDematID(r.BOID) == normDematID(bo.BOID) && (!r.NoAMC || r.AMC != 0) {
			t.Fatalf("AMC on a 'to be closed' account: %+v", r)
		}
	}
	if l := o.ToBeClosedList(ctx, T); len(l.BOIDs) != 1 {
		t.Fatalf("list %+v", l)
	}
	// Stamp duty not funded: a work item when the status code matches.
	if _, err := o.SetFirmPractice(ctx, T, "admin", "cdsl-stamp-status", PracticeAnswer{Option: "code", Values: map[string]string{"code": "OVRSTMP"}}); err != nil {
		t.Fatal(err)
	}
	o.cdslStampOverdue(ctx, T, xfile.Row{Values: map[string]string{"OrdSttsTo": "ovrstmp", "TxnID": "TX9", "ClntId": bo.BOID, "ISIN": h.ISIN}}, "BH1001")
	if _, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-cdsl-stamp-TX9"); err != nil {
		t.Fatalf("no stamp duty work item: %v", err)
	}

	// The round's tests run on the sample day without errors.
	run, err := o.RunSelfInspection(ctx, T, "vikram.shah", D)
	if err != nil {
		t.Fatal(err)
	}
	mine := map[string]bool{}
	for _, id := range []string{"tds_vendor_rates", "tds_certificates", "stamp_duty_rates", "einvoice_irn", "itc_gstr2b", "itc_180_days", "gst_credit_notes", "transmission_tlh",
		"ap_change_notice", "ap_payout_hold", "terminal_turnover", "nse_user_limits", "website_lists", "prop_trading_notice", "bse_complaints", "complaints_networth",
		"bmc_cash", "segment_deposit", "dd_bank_certificate", "dp_pass_through", "dis_setup_delay", "aop_accounts", "pool_voting", "ro_kit_delivery", "closure_channel", "dlt_templates"} {
		mine[id] = true
	}
	n := 0
	for _, x := range run.Results {
		if !mine[x.Test] {
			continue
		}
		n++
		if x.Status == SIError {
			t.Errorf("%s: %s", x.Test, x.Summary)
		}
		switch x.Test {
		case "stamp_duty_rates":
			if x.Status != SIPass {
				t.Errorf("the sample schedule's stamp duty: %+v", x)
			}
		case "dlt_templates":
			if x.Status != SIFail {
				t.Errorf("sample DLT IDs not flagged: %+v", x)
			}
		}
	}
	if n != len(mine) {
		t.Fatalf("%d of %d tests ran", n, len(mine))
	}
}

func firstHolding(t *testing.T, o *Ops, ucc string) Holding {
	t.Helper()
	hs, _ := o.Holdings(t.Context(), T, ucc)
	for _, h := range hs {
		if h.Free().Cmp(money.DecInt(2)) >= 0 {
			return h
		}
	}
	t.Fatalf("%s has no holding", ucc)
	return Holding{}
}
