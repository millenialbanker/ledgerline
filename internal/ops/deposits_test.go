package ops

import (
	"testing"

	"ledgerline/internal/money"
)

func TestClearingDeposits(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	set.ClearingBMC = map[string]string{"NSE Clearing": "5000000"}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveDeposit(ctx, T, "priya.nair", Deposit{CC: "NSE Clearing", Kind: "Cash", Amount: money.MustDec("1000000")}); err == nil {
		t.Fatal("maker recorded a deposit")
	}
	if _, err := o.SaveDeposit(ctx, T, "farhan.khan", Deposit{CC: "NSE Clearing", Kind: "Fixed deposit", Amount: money.MustDec("1000000")}); err == nil {
		t.Fatal("FD without bank, number and maturity")
	}
	fd, err := o.SaveDeposit(ctx, T, "farhan.khan", Deposit{CC: "NSE Clearing", Kind: "Fixed deposit", Issuer: "HDFC Bank", Ref: "FD-778812", Amount: money.MustDec("2000000"), Expiry: addDays(D, 20), Lien: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveDeposit(ctx, T, "farhan.khan", Deposit{CC: "NSE Clearing", Kind: "Securities", Issuer: "Govt of India", Ref: "IN0020230085", Amount: money.MustDec("3000000"), HaircutPct: money.MustDec("10")}); err != nil {
		t.Fatal(err)
	}
	v, err := o.Deposits(ctx, T, D)
	if err != nil || len(v.ByCC) != 1 {
		t.Fatalf("view: %+v %v", v.ByCC, err)
	}
	r := v.ByCC[0]
	// 20 L FD + 30 L securities less 10% = 47 L, against 50 L base capital.
	if r.Value.Cmp(money.MustDec("4700000")) != 0 || r.Short.Cmp(money.MustDec("300000")) != 0 || r.CashPct > 43 || r.Expiring.Cmp(money.MustDec("2000000")) != 0 {
		t.Fatalf("requirement: %+v", r)
	}
	runs, err := o.RunStage(ctx, T, SystemActor, D, "", "deposits")
	if err != nil || runs[0].Queued != 3 { // FD expiring, below base capital, under half cash
		t.Fatalf("queued: %+v %v", runs, err)
	}
	if _, err := o.ReleaseDeposit(ctx, T, "farhan.khan", fd.ID, "Renewed", ""); err == nil {
		t.Fatal("renewed without reference")
	}
	if _, err := o.ReleaseDeposit(ctx, T, "farhan.khan", fd.ID, "Renewed", "Replaced by FD-990011"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ReleaseDeposit(ctx, T, "farhan.khan", fd.ID, "Released", "again"); err == nil {
		t.Fatal("released twice")
	}
}
