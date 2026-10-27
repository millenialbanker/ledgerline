package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"ledgerline/internal/help"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// The made-up vendor in internal/billscan/testdata (no real business).
const testVendorGSTIN, testFirmGSTIN = "27AABCK1234M1ZK", "27AAACL9999Q1Z8"

func TestVendorMaster(t *testing.T) {
	o, ctx := setup(t)
	bad := []Vendor{
		{},
		{Name: "X", GSTIN: "27AABCK1234M1ZA"}, // check digit
		{Name: "X", GSTIN: testVendorGSTIN, PAN: "AAAAA1111A"}, // PAN not in GSTIN
		{Name: "X", IFSC: "HDFC0000123"},                       // IFSC without account
		{Name: "X", Account: "50200012345678", IFSC: "HDFC123"},
		{Name: "X", MSME: "UDYAM-MH-01-0001234"}, // no type
		{Name: "X", TDSSection: "194Z"},
		{Name: "X", Expense: "INCOME:OTHER"},
	}
	for i, v := range bad {
		if _, err := o.SaveVendor(ctx, T, "priya.nair", v); err == nil {
			t.Errorf("bad vendor %d accepted", i)
		}
	}
	if _, err := o.SaveVendor(ctx, T, "vikram.shah", Vendor{Name: "X"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("compliance added a vendor: %v", err)
	}
	v, err := o.SaveVendor(ctx, T, "priya.nair", Vendor{Name: " Kavya  Office Supplies Pvt Ltd", GSTIN: strings.ToLower(testVendorGSTIN),
		Account: "5020 0012 345678", IFSC: "hdfc0000123", TDSSection: "194C", MSME: "UDYAM-MH-19-0012345", MSMEType: "Micro", TermsDays: 60, Expense: "EXPENSE:OFFICE"})
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "V0001" || v.PAN != "AABCK1234M" || v.State != "Maharashtra" || v.Ledger != "PAYABLE:VENDOR:V0001" || v.Account != "50200012345678" || !v.Active {
		t.Fatalf("%+v", v)
	}
	if _, err := o.SaveVendor(ctx, T, "priya.nair", Vendor{Name: "Kavya again", GSTIN: testVendorGSTIN}); err == nil {
		t.Error("second vendor with the same GSTIN")
	}
	// A changed bank account is logged with the last four digits only.
	v.Account = "11112222333344"
	if _, err := o.SaveVendor(ctx, T, "farhan.khan", v); err != nil {
		t.Fatal(err)
	}
	evs, _ := o.App.Audit.List(ctx, T, 500)
	logged := 0
	for _, e := range evs {
		if e.Action == "vendor.bank_changed" {
			logged++
			if !strings.Contains(fmt.Sprint(e.Detail), "…5678 → …3344") || strings.Contains(fmt.Sprint(e.Detail), "11112222") {
				t.Errorf("bank change detail %v", e.Detail)
			}
		}
	}
	if logged != 1 {
		t.Fatalf("bank change logged %d times", logged)
	}
	accts, _ := o.Accounts(ctx, T, "admin", D)
	found := false
	for _, a := range accts {
		found = found || a.Code == "PAYABLE:VENDOR:V0001"
	}
	if !found {
		t.Error("vendor ledger account missing")
	}
}

func TestVendorBillFlow(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	set.FirmGSTIN = testFirmGSTIN
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}

	// Scan a PDF bill: its details are read on the server.
	pdf, err := os.ReadFile("../billscan/testdata/libreoffice.pdf")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := o.ScanBill(ctx, T, "priya.nair", "kavya.pdf", pdf, true)
	if err != nil {
		t.Fatal(err)
	}
	f := sc.Fields
	if sc.Method != "text" || f.VendorGSTIN != testVendorGSTIN || f.Total != money.MustAmount("23600") || f.CGST != money.MustAmount("1800") || f.BankAccount != "50200012345678" || sc.Vendor != "" {
		t.Fatalf("scan %+v", sc)
	}
	if sc.AIOffer || !strings.Contains(sc.AINote, "") {
		t.Errorf("AI offered without a model: %+v", sc)
	}
	// The same file again is flagged.
	if sc2, _ := o.ScanBill(ctx, T, "priya.nair", "again.pdf", pdf, false); !strings.HasPrefix(sc2.Duplicate, sc.ID) {
		t.Errorf("duplicate not flagged: %q", sc2.Duplicate)
	}
	if _, err := o.ScanBill(ctx, T, "priya.nair", "x.txt", []byte("hello there, a text file"), false); err == nil {
		t.Error("text file accepted as a bill")
	}

	// The vendor is created from what was read; scanning again matches it.
	v, err := o.SaveVendor(ctx, T, "priya.nair", Vendor{Name: f.VendorName, GSTIN: f.VendorGSTIN, Address: f.VendorAddress, BankName: f.BankName,
		IFSC: f.BankIFSC, Account: f.BankAccount, TDSSection: "194C", MSME: "UDYAM-MH-19-0012345", MSMEType: "Small", TermsDays: 30, Expense: "EXPENSE:OFFICE"})
	if err != nil {
		t.Fatal(err)
	}
	sc, _ = o.ReadBillWithAI(ctx, T, "priya.nair", sc.ID) // no model: refused, nothing changes
	o.matchVendor(ctx, T, &sc)
	if sc.Vendor != v.ID || sc.VendorMatch != "gstin" || len(sc.Checks) != 0 {
		t.Fatalf("match %+v", sc)
	}
	// A bill showing other bank details is flagged.
	sc3 := BillScanDoc{Fields: f}
	sc3.Fields.BankAccount = "99990000111122"
	o.matchVendor(ctx, T, &sc3)
	if len(sc3.Checks) != 1 || !strings.Contains(sc3.Checks[0], "ending …1122") {
		t.Fatalf("bank change not flagged: %v", sc3.Checks)
	}

	// Book the bill: TDS 194C at 2% (a company).
	bill := VendorBill{Vendor: v.ID, BillNo: f.BillNo, BillDate: f.BillDate, Taxable: f.Taxable, CGST: f.CGST, SGST: f.SGST, ITC: true,
		TDSSection: "194C", TDS: money.MustAmount("400"), Scan: sc.ID, Narration: "Paper and toner"}
	rs := money.MustAmount
	for i, b := range []VendorBill{
		{Vendor: "V9999", BillNo: "1", BillDate: "2026-10-01", Taxable: rs("100")},
		{Vendor: v.ID, BillNo: "", BillDate: "2026-10-01", Taxable: rs("100")},
		{Vendor: v.ID, BillNo: "1", BillDate: "2026-10-01", Taxable: rs("100"), CGST: rs("9"), SGST: rs("8")},
		{Vendor: v.ID, BillNo: "1", BillDate: "2026-10-01", Taxable: rs("100"), CGST: rs("9"), SGST: rs("9"), IGST: rs("18")},
		{Vendor: v.ID, BillNo: "1", BillDate: "2099-10-01", Taxable: rs("100")},
		{Vendor: v.ID, BillNo: "1", BillDate: "2026-10-01", Taxable: rs("100"), TDS: rs("2")},
		{Vendor: v.ID, BillNo: "1", BillDate: "2026-10-01", Taxable: rs("100"), IGST: rs("50")},
		{Vendor: v.ID, BillNo: "1", BillDate: "2026-10-01", Taxable: rs("100"), Expense: "INCOME:X"},
	} {
		if _, err := o.SaveVendorBill(ctx, T, "priya.nair", b); err == nil {
			t.Errorf("bad bill %d accepted", i)
		}
	}
	b, err := o.SaveVendorBill(ctx, T, "priya.nair", bill)
	if err != nil {
		t.Fatal(err)
	}
	if b.Total != money.MustAmount("23600") || b.Payable != money.MustAmount("23200") || b.Status != BillPending || b.ReadBy != "text" || len(b.Edited) != 0 || b.Expense != "EXPENSE:OFFICE" {
		t.Fatalf("bill %+v", b)
	}
	// Small enterprise on 30 days: pay within 30 days (43B(h)).
	if b.MSMEPayBy != addDays(f.BillDate, 30) || b.DueDate != addDays(f.BillDate, 30) {
		t.Errorf("due %s, MSME pay by %s", b.DueDate, b.MSMEPayBy)
	}
	if _, err := o.SaveVendorBill(ctx, T, "priya.nair", VendorBill{Vendor: v.ID, BillNo: strings.ToLower(strings.ReplaceAll(f.BillNo, "/", " ")), BillDate: f.BillDate, Taxable: rs("100")}); err == nil {
		t.Error("the same bill booked twice")
	}
	// The maker cannot post it; the checker can.
	if _, err := o.DecideVoucher(ctx, T, "priya.nair", b.Voucher, "approve", ""); err == nil {
		t.Fatal("maker posted own bill")
	}
	if _, err := o.PayVendorBill(ctx, T, "priya.nair", b.ID, "", "", "", 0); err == nil {
		t.Fatal("paid before posting")
	}
	if _, err := o.DecideVoucher(ctx, T, "farhan.khan", b.Voucher, "approve", ""); err != nil {
		t.Fatal(err)
	}
	bal := func(code string) money.Amount {
		as, _ := o.Accounts(ctx, T, "admin", "2099-12-31")
		for _, a := range as {
			if a.Code == code {
				return a.Balance
			}
		}
		return 0
	}
	if bal("EXPENSE:OFFICE") != money.MustAmount("20000") || bal(InputCGST) != money.MustAmount("1800") || bal("PAYABLE:TDS:194C") != -money.MustAmount("400") || bal(v.Ledger) != -money.MustAmount("23200") {
		t.Fatalf("posting: office %s cgst %s tds %s vendor %s", bal("EXPENSE:OFFICE"), bal(InputCGST), bal("PAYABLE:TDS:194C"), bal(v.Ledger))
	}

	// Pay part, then the rest.
	if _, err := o.PayVendorBill(ctx, T, "priya.nair", b.ID, "", ledger.BankClient, "", 0); err == nil {
		t.Fatal("vendor paid from the client bank account")
	}
	if _, err := o.PayVendorBill(ctx, T, "priya.nair", b.ID, "", "", "UTR1", money.MustAmount("30000")); err == nil {
		t.Fatal("overpaid")
	}
	p, err := o.PayVendorBill(ctx, T, "priya.nair", b.ID, "", "", "UTR1", money.MustAmount("10000"))
	if err != nil || len(p.Payments) != 1 || p.Status != BillPosted {
		t.Fatalf("payment %+v %v", p, err)
	}
	if _, err := o.PayVendorBill(ctx, T, "priya.nair", b.ID, "", "", "UTR2", money.MustAmount("13300")); err == nil {
		t.Fatal("pending payment not counted")
	}
	o.DecideVoucher(ctx, T, "farhan.khan", p.Payments[0].Voucher, "approve", "")
	p, err = o.PayVendorBill(ctx, T, "priya.nair", b.ID, "", "", "UTR2", 0)
	if err != nil || p.Payments[1].Amount != money.MustAmount("13200") {
		t.Fatalf("rest %+v %v", p, err)
	}
	o.DecideVoucher(ctx, T, "farhan.khan", p.Payments[1].Voucher, "approve", "")
	r, err := o.VendorBills(ctx, T, "vikram.shah", "2026-04-01", "2027-03-31")
	if err != nil || len(r.Bills) != 1 || r.Bills[0].Status != BillPaid || r.Unpaid != 0 || r.TDS != money.MustAmount("400") || r.ITC != money.MustAmount("3600") ||
		len(r.TDSBySect) != 1 || r.TDSBySect[0].TDS != money.MustAmount("400") {
		t.Fatalf("register %+v %v", r, err)
	}
	if bal(v.Ledger) != 0 {
		t.Errorf("vendor balance %s after paying", bal(v.Ledger))
	}
	csv, err := o.BillRegisterCSV(ctx, T, "admin", "2026-04-01", "2027-03-31")
	if err != nil || !strings.Contains(string(csv), "AABCK1234M") || !strings.Contains(string(csv), ",Paid,") {
		t.Fatalf("csv %s %v", csv, err)
	}
	vs, _ := o.Vendors(ctx, T, "admin")
	if len(vs) != 1 || vs[0].Bills != 1 || vs[0].Balance != 0 {
		t.Fatalf("vendors %+v", vs)
	}
	if ss, _ := o.BillScans(ctx, T, "priya.nair"); len(ss) != 1 { // the duplicate upload is still waiting
		t.Errorf("waiting scans %d", len(ss))
	}

	// Reverse charge, no ITC, from another state: warnings, not refusals.
	v2, err := o.SaveVendor(ctx, T, "priya.nair", Vendor{Name: "Advocate R. Menon"})
	if err != nil {
		t.Fatal(err)
	}
	b2, err := o.SaveVendorBill(ctx, T, "priya.nair", VendorBill{Vendor: v2.ID, BillNo: "L/7", BillDate: "2026-10-05", Taxable: money.MustAmount("50000"),
		IGST: money.MustAmount("9000"), RCM: true, ITC: true, Expense: "EXPENSE:LEGAL", TDSSection: "194J", TDS: money.MustAmount("5000")})
	if err != nil {
		t.Fatal(err)
	}
	if b2.Total != money.MustAmount("50000") || b2.Payable != money.MustAmount("45000") || !strings.Contains(strings.Join(b2.Warnings, " "), "no GSTIN") {
		t.Fatalf("rcm %+v", b2)
	}
	vch, _ := o.Vouchers(ctx, T, "admin")
	for _, x := range vch {
		if x.No == b2.Voucher {
			got := map[string]string{}
			for _, l := range x.Lines {
				got[l.Account] = l.Debit.String() + "/" + l.Credit.String()
			}
			if got[PayableRCM] != "0.00/9000.00" || got[InputIGST] != "9000.00/0.00" || got["PAYABLE:TDS:194J"] != "0.00/5000.00" || got[v2.Ledger] != "0.00/45000.00" {
				t.Fatalf("rcm lines %v", got)
			}
		}
	}
}

// fakeReader reads documents like an AI model would.
type fakeReader struct {
	fakeWriter
	reply string
	calls int
}

func (f *fakeReader) ReadDocument(_ context.Context, data []byte, mime, instruction string) (help.Reply, error) {
	f.calls++
	return help.Reply{Text: f.reply, Provider: f.Provider(), Model: f.Model(), Usage: help.Usage{InputTokens: 1800, OutputTokens: 300}}, nil
}

func TestBillScanAI(t *testing.T) {
	o, ctx := setup(t)
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, make([]byte, 200)...)
	sc, err := o.ScanBill(ctx, T, "priya.nair", "photo.jpg", jpeg, true)
	if err != nil || sc.Method != "none" || sc.AIOffer || !strings.Contains(sc.Note, "scan or photo") || !strings.Contains(sc.AINote, "No AI model") {
		t.Fatalf("no model: %+v %v", sc, err)
	}
	fr := &fakeReader{reply: `{"vendor_name":"Kavya Office Supplies Pvt Ltd","vendor_gstin":"` + testVendorGSTIN + `","bill_no":"KOS/9","bill_date":"2026-10-14","taxable":1000,"cgst":90,"sgst":90,"total":1180}`}
	o.LedgyAI = fr
	o.Meter = meter(t, "2", "")
	if _, err := o.ReadBillWithAI(ctx, T, "priya.nair", sc.ID); err == nil || !strings.Contains(err.Error(), "Settings") {
		t.Fatalf("read with AI while switched off: %v", err)
	}
	set, _ := o.Settings(ctx, T)
	set.BillAI = true
	o.SaveSettings(ctx, T, "admin", set)
	if _, err := o.ReadBillWithAI(ctx, T, "priya.nair", sc.ID); err == nil || !strings.Contains(err.Error(), "credit") {
		t.Fatalf("read with no credit: %v", err)
	}
	o.AddAICredit(ctx, T, "provider", money.MustAmount("100"), "INV-1")
	sc, err = o.ReadBillWithAI(ctx, T, "priya.nair", sc.ID)
	if err != nil || sc.Method != "ai" || sc.Fields.Total != money.MustAmount("1180") || sc.Fields.VendorState != "Maharashtra" || fr.calls != 1 {
		t.Fatalf("ai %+v %v", sc, err)
	}
	if w, _, _ := docsGetWallet(o, T); w.Charged <= 0 {
		t.Error("AI reading not charged")
	}
	// India-only mode never sends a bill out.
	o.Residency.IndiaOnly = true
	if _, err := o.ReadBillWithAI(ctx, T, "priya.nair", sc.ID); err == nil || fr.calls != 1 {
		t.Fatalf("india-only: %v", err)
	}
	o.Residency.IndiaOnly = false
	// A readable PDF is not sent to the model.
	pdf, _ := os.ReadFile("../billscan/testdata/reportlab.pdf")
	if sc, err := o.ScanBill(ctx, T, "priya.nair", "b.pdf", pdf, true); err != nil || sc.Method != "text" || fr.calls != 1 {
		t.Fatalf("pdf went to AI: %+v %v", sc, err)
	}
}
