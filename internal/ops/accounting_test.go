package ops

import (
	"strings"
	"testing"

	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

func TestVouchersStatementsAndGST(t *testing.T) {
	o, ctx := setup(t)
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBilling(ctx, T, "farhan.khan", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	if _, err := o.GenerateNotes(ctx, T, "priya.nair", D); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveAccount(ctx, T, "farhan.khan", Account{Code: "EXPENSE:RENT", Name: "Office rent"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveAccount(ctx, T, "farhan.khan", Account{Code: "EXPENSE:SALARIES", Name: "Salaries", Restricted: true, AllowedUsers: []string{"farhan.khan"}}); err != nil {
		t.Fatal(err)
	}
	// A payment of rent with input GST, from the own bank account.
	rent := Voucher{Type: "Payment", Date: D, Branch: "Bengaluru", Party: "Landlord LLP", Narration: "October rent", Lines: []VoucherLine{
		{Account: "EXPENSE:RENT", Debit: 5000000}, {Account: InputIGST, Debit: 900000}, {Account: ledger.BankOwn, Credit: 5900000}}}
	if _, err := o.SaveVoucher(ctx, T, "priya.nair", Voucher{Type: "Payment", Date: D, Narration: "x", Lines: []VoucherLine{{Account: "EXPENSE:RENT", Debit: 100}, {Account: ledger.BankClient, Credit: 100}}}); err == nil {
		t.Fatal("rent paid from the client bank account")
	}
	if _, err := o.SaveVoucher(ctx, T, "priya.nair", Voucher{Type: "Payment", Date: D, Narration: "client", Lines: []VoucherLine{{Account: "CLIENT:BH1001", Debit: 100}, {Account: ledger.BankOwn, Credit: 100}}}); err == nil {
		t.Fatal("payment voucher to a client accepted")
	}
	if _, err := o.SaveVoucher(ctx, T, "priya.nair", Voucher{Type: "Contra", Date: D, Narration: "raid", Lines: []VoucherLine{{Account: ledger.BankOwn, Debit: 100}, {Account: ledger.BankClient, Credit: 100}}}); err == nil {
		t.Fatal("contra out of the client bank accepted")
	}
	if _, err := o.SaveVoucher(ctx, T, "priya.nair", Voucher{Type: "Payment", Date: D, Narration: "sal", Lines: []VoucherLine{{Account: "EXPENSE:SALARIES", Debit: 100}, {Account: ledger.BankOwn, Credit: 100}}}); err == nil {
		t.Fatal("maker posted to a restricted account")
	}
	v, err := o.SaveVoucher(ctx, T, "priya.nair", rent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v.No, "PV/2026-27/") || v.Status != "Pending" {
		t.Fatalf("voucher %+v", v)
	}
	if _, err := o.DecideVoucher(ctx, T, "priya.nair", v.No, "approve", ""); err == nil {
		t.Fatal("maker approved")
	}
	if v, err = o.DecideVoucher(ctx, T, "farhan.khan", v.No, "approve", ""); err != nil || v.Status != "Posted" {
		t.Fatalf("approve: %v %+v", err, v)
	}
	sal, err := o.SaveVoucher(ctx, T, "farhan.khan", Voucher{Type: "Payment", Date: D, Narration: "October salaries", Lines: []VoucherLine{{Account: "EXPENSE:SALARIES", Debit: 20000000}, {Account: ledger.BankOwn, Credit: 20000000}}})
	if err != nil {
		t.Fatal(err)
	}
	// Restricted: hidden from the maker's voucher list and chart of accounts.
	vs, _ := o.Vouchers(ctx, T, "priya.nair")
	for _, x := range vs {
		if x.No == sal.No {
			t.Fatal("restricted voucher visible to maker")
		}
	}
	as, _ := o.Accounts(ctx, T, "priya.nair", D)
	for _, a := range as {
		if a.Code == "EXPENSE:SALARIES" {
			t.Fatal("restricted account listed for maker")
		}
	}
	// Financials: rent shows; the firm balance sheet balances.
	f, err := o.Financials(ctx, T, "farhan.khan", "2026-04-01", D, "")
	if err != nil {
		t.Fatal(err)
	}
	if !f.Balanced {
		t.Fatalf("balance sheet: assets %s, liabilities %s", f.TotalAssets, f.TotalLiab)
	}
	var rentLine FinLine
	for _, l := range f.Expenses.Lines {
		if l.Code == "EXPENSE:RENT" {
			rentLine = l
		}
	}
	if rentLine.Amount.String() != "50000.00" || f.Income.Total <= 0 {
		t.Fatalf("P&L: rent %+v income %s", rentLine, f.Income.Total)
	}
	fb, _ := o.Financials(ctx, T, "farhan.khan", "2026-04-01", D, "Bengaluru")
	if fb.Expenses.Total.String() != "50000.00" {
		t.Fatalf("Bengaluru expenses %s", fb.Expenses.Total)
	}
	// GST: every bill's GST is on the register; Maharashtra clients pay
	// CGST + SGST, others IGST; ITC from the rent voucher.
	g, err := o.GST(ctx, T, "2026-10-01", "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := o.App.Ledger.Balances(ctx, T, D)
	if tot := g.Out.IGST + g.Out.CGST + g.Out.SGST; tot != -b[ledger.PayableGST] {
		t.Fatalf("register GST %s, ledger GST payable %s", tot, -b[ledger.PayableGST])
	}
	if g.Out.IGST == 0 || g.Out.CGST == 0 || g.Out.CGST != g.Out.SGST && g.Out.CGST+1 != g.Out.SGST && g.Out.CGST > g.Out.SGST+money.Amount(len(g.Lines)) {
		t.Fatalf("split: %+v", g.Out)
	}
	if g.ITC.IGST != 900000 {
		t.Fatalf("ITC %+v", g.ITC)
	}
	if len(g.Docs) == 0 || !strings.HasPrefix(g.Docs[0].Series, "CN/") {
		t.Fatalf("docs %+v", g.Docs)
	}
	csv, _ := o.GSTR1CSV(ctx, T, "2026-10-01", "2026-10-31", "b2cs")
	if !strings.Contains(string(csv), "OE,Maharashtra,18") {
		t.Fatalf("b2cs:\n%s", csv)
	}
	if len(g.B2B) > 0 && g.B2B[0].GSTIN == "" {
		t.Fatal("b2b without GSTIN")
	}
	// Set-off: IGST credit is used against IGST, then CGST, then SGST.
	p, l := setOff(GSTLine{IGST: 1000, CGST: 500, SGST: 500}, GSTLine{IGST: 1800, CGST: 100})
	if p.IGST != 0 || p.CGST != 0 || p.SGST != 200 || l.IGST != 0 || l.CGST != 100 {
		t.Fatalf("set-off pay %+v left %+v", p, l)
	}
	// Cancelling reverses.
	if v, err = o.DecideVoucher(ctx, T, "farhan.khan", v.No, "cancel", "wrong amount"); err != nil || v.Status != "Cancelled" {
		t.Fatalf("cancel: %v", err)
	}
	f, _ = o.Financials(ctx, T, "farhan.khan", "2026-04-01", D, "")
	for _, l := range f.Expenses.Lines {
		if l.Code == "EXPENSE:RENT" && l.Amount != 0 {
			t.Fatal("rent still in P&L after cancel")
		}
	}
}
