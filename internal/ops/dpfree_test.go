package ops

import (
	"testing"

	"ledgerline/internal/money"
)

// Transfers to or from the firm's own pool accounts (any number, CDSL or
// NSDL) carry no debit or credit charge; all others are charged.
func TestDPChargeFreePools(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	var dm Client
	for _, c := range cs {
		if len(c.Demat.ID) == 16 && c.Status == "Active" {
			dm = c
			break
		}
	}
	if dm.UCC == "" {
		t.Skip("no CDSL client in the sample")
	}
	tf, _ := o.DPTariffNow(ctx, T)
	tf.DebitCharge, tf.DepositoryShare, tf.CreditCharge = 1000, 0, 500
	tf.ChargeFree = []ChargeFreeAccount{{ID: "1203320000000001", Label: "Pool CDSL"}, {Depository: "nsdl", ID: "IN300123 10000001", Label: "Pool NSDL"}}
	bad := tf
	bad.ChargeFree = []ChargeFreeAccount{{Depository: "CDSL", ID: "12345"}}
	if _, err := o.SaveDPTariff(ctx, T, "farhan.khan", bad); err == nil {
		t.Fatal("a short CDSL ID was accepted")
	}
	saved, err := o.SaveDPTariff(ctx, T, "farhan.khan", tf)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.ChargeFree) != 2 || saved.ChargeFree[1].ID != "IN30012310000001" || saved.ChargeFree[1].Depository != "NSDL" || saved.ChargeFree[0].Depository != "CDSL" {
		t.Fatalf("pools %+v", saved.ChargeFree)
	}
	day := "2026-11-05"
	f := xwrite(t, "cdsl-transactions",
		// Pay-in to the CDSL pool for a sale through the firm: free.
		xr("", "Src", "CDSL", "TxnTyp", "IDO", "TxnID", "91", "LineNb", "1", "ClntId", dm.Demat.ID, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "10.000", "TxnCd", "DBT", "CntrBnfclOwnrId", "1203320000000001"),
		// Pay-out from the NSDL pool: free.
		xr("", "Src", "CDSL", "TxnTyp", "IDD", "TxnID", "92", "LineNb", "1", "ClntId", dm.Demat.ID, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "5.000", "TxnCd", "CRD", "OthrDpstryPtcpt", "IN300123", "OthrDpstryClntCd", "10000001"),
		// Delivery to another broker's account and a receipt from one: charged.
		xr("", "Src", "CDSL", "TxnTyp", "WTR", "TxnID", "93", "LineNb", "1", "ClntId", dm.Demat.ID, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "3.000", "TxnCd", "DBT", "CntrBnfclOwnrId", "1208870000000099"),
		xr("", "Src", "CDSL", "TxnTyp", "WTR", "TxnID", "94", "LineNb", "1", "ClntId", dm.Demat.ID, "ISIN", "INE999Q01017", "ExecDt", day, "Qty", "2.000", "TxnCd", "CRD", "CntrBnfclOwnrId", "1208870000000099"))
	loadX(t, o, "COD_EXP_12345_123456_F_202611051830_001_P1.csv", f)
	bills, err := o.RunDPBilling(ctx, T, "farhan.khan", "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	var b DPBill
	for _, x := range bills {
		if x.UCC == dm.UCC {
			b = x
		}
	}
	got := map[string]DPBillLine{}
	for _, l := range b.Lines {
		got[l.What] = l
	}
	d, c := got["Debit instructions (per ISIN)"], got["Credit instructions (per ISIN)"]
	if b.Free != 2 || d.Count != 1 || d.Amount != money.Amount(1000) || c.Count != 1 || c.Amount != money.Amount(500) {
		t.Fatalf("bill %+v", b)
	}
}
