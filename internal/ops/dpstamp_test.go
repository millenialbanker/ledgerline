package ops

import (
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// TestDPStampDuty: the stamp duty CDSL collects through the DP on an
// off-market transfer (StmpDtyInd DP in the transaction export) goes on
// the client's DP bill outside the taxable charges, once per transfer and
// to the transferor; duty the client paid directly is not billed; one that
// arrives after the month was billed goes on the next bill.
func TestDPStampDuty(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	var dm []Client
	for _, c := range cs {
		if len(c.Demat.ID) == 16 && c.Status == "Active" && c.Type != "Corporate" {
			dm = append(dm, c)
		}
	}
	if len(dm) < 2 {
		t.Skip("two CDSL clients needed")
	}
	if _, err := o.LoadFile(ctx, T, "farhan.khan", "DP-ACCOUNTS", "", "dpa.csv", []byte("UCC,BOID,Depository,Status,Opened\n"+
		dm[0].UCC+","+dm[0].Demat.ID+",CDSL,Active,2020-01-10\n"+dm[1].UCC+","+dm[1].Demat.ID+",CDSL,Active,2020-01-10\n")); err != nil {
		t.Fatal(err)
	}
	a, b := dm[0].Demat.ID, dm[1].Demat.ID
	day := "2026-11-06"
	f := xwrite(t, "cdsl-transactions",
		// a gift from a to b: both sides carry the duty; a (the transferor) pays it
		xr("", "Src", "CDSL", "TxnTyp", "WTR", "TxnID", "81", "LineNb", "1", "ClntId", a, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "100.000", "TxnCd", "DBT", "StmpDty", "150.000", "StmpDtyInd", "DP"),
		xr("", "Src", "CDSL", "TxnTyp", "WTR", "TxnID", "81", "LineNb", "2", "ClntId", b, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "100.000", "TxnCd", "CRD", "StmpDty", "150.000", "StmpDtyInd", "DP"),
		// paid by the client directly: nothing to recover
		xr("", "Src", "CDSL", "TxnTyp", "WTR", "TxnID", "82", "LineNb", "1", "ClntId", a, "ISIN", "INE111Q01019", "ExecDt", day, "Qty", "5.000", "TxnCd", "DBT", "StmpDty", "30.000", "StmpDtyInd", "CL"),
		// b's own transfer out
		xr("", "Src", "CDSL", "TxnTyp", "WTR", "TxnID", "83", "LineNb", "1", "ClntId", b, "ISIN", "INE111Q01019", "ExecDt", day, "Qty", "7.000", "TxnCd", "DBT", "StmpDty", "12.345", "StmpDtyInd", "DP"))
	loadX(t, o, "COD_EXP_12345_123456_F_202611061830_001_P1.csv", f)
	if s, _, err := docs.Get[DPStamp](ctx, o.Docs, T, cDPStamp, "81"); err != nil || s.UCC != dm[0].UCC || !s.Debit || s.Amount.Cmp(money.MustDec("150")) != 0 {
		t.Fatalf("stamp 81 %+v %v", s, err)
	}
	if _, _, err := docs.Get[DPStamp](ctx, o.Docs, T, cDPStamp, "82"); err == nil {
		t.Fatal("duty the client paid was kept to bill")
	}

	bills, err := o.RunDPBilling(ctx, T, "farhan.khan", "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]DPBill{}
	for _, x := range bills {
		by[x.UCC] = x
	}
	stampLine := func(x DPBill) (DPBillLine, int) {
		n := 0
		var l DPBillLine
		for _, y := range x.Lines {
			if y.Stamp {
				l = y
				n++
			}
		}
		return l, n
	}
	set, _ := o.Settings(ctx, T)
	check := func(ucc string, want money.Amount) DPBill {
		t.Helper()
		x := by[ucc]
		l, n := stampLine(x)
		if n != 1 || l.Amount != want || l.Count != 1 || x.Stamp != want || !strings.Contains(l.What, "Stamp duty") {
			t.Fatalf("%s: stamp line %+v (%d) on %+v", ucc, l, n, x)
		}
		var taxable money.Amount
		for _, y := range x.Lines {
			if !y.Stamp {
				taxable += y.Amount
			}
		}
		if x.Taxable != taxable || x.GST != x.Taxable.Dec().Pct(set.gstRate()).Round(money.UnitPaisa) || x.Total != x.Taxable+x.GST+x.Stamp {
			t.Fatalf("%s: totals %+v", ucc, x)
		}
		st, err := o.App.Ledger.Statement(ctx, T, ledger.ClientAccount(ucc), "2026-11-01", "2026-11-30")
		if err != nil {
			t.Fatal(err)
		}
		debited := false
		for _, l := range st.Lines {
			debited = debited || (l.Debit == x.Total && strings.Contains(l.Narration+l.Memo, "DP charges"))
		}
		if !debited {
			t.Fatalf("%s: the client is not debited the bill's %v: %+v", ucc, x.Total, st.Lines)
		}
		if x.Taxable > 0 {
			invs, _ := o.Invoices(ctx, T, ucc)
			if len(invs) == 0 || invs[0].No != x.Invoice || invs[0].Total != x.Taxable+x.GST {
				t.Fatalf("%s: invoice %+v for bill %+v", ucc, invs, x)
			}
		}
		return x
	}
	check(dm[0].UCC, 15000)
	check(dm[1].UCC, 1235) // ₹12.345 to the paisa; not the gift b received
	bal, _ := o.App.Ledger.Balances(ctx, T, "2026-11-30")
	if v := bal[PayableDPStamp]; v != 16235 && v != -16235 {
		t.Fatalf("stamp duty payable to the depository %v, want ₹162.35", v)
	}
	if s, _, _ := docs.Get[DPStamp](ctx, o.Docs, T, cDPStamp, "81"); s.Billed != "2026-11|"+dm[0].UCC {
		t.Fatalf("not marked billed %+v", s)
	}

	// A November transfer read after November was billed goes on December's bill.
	late := xwrite(t, "cdsl-transactions",
		xr("", "Src", "CDSL", "TxnTyp", "WTR", "TxnID", "84", "LineNb", "1", "ClntId", a, "ISIN", "INE999Q01017", "ExecDt", "2026-11-28", "Qty", "1.000", "TxnCd", "DBT", "StmpDty", "1.500", "StmpDtyInd", "DP"))
	loadX(t, o, "COD_EXP_12345_123456_F_202611281830_001_P1.csv", late)
	dec, err := o.RunDPBilling(ctx, T, "farhan.khan", "2026-12")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range dec {
		if x.UCC == dm[0].UCC {
			l, n := stampLine(x)
			found = n == 1 && l.Amount == 150
		}
	}
	if !found {
		t.Fatalf("late stamp duty not on the next bill: %+v", dec)
	}
}
