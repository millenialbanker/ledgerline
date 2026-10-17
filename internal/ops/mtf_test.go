package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/money"
)

func TestMTF(t *testing.T) {
	o, ctx := setup(t)
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBilling(ctx, T, "priya.nair", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	// Find a client with a CNC buy in an eligible scrip.
	ts, _ := o.exchangeTrades(ctx, T, D)
	el, err := o.MTFList(ctx, T, D)
	if err != nil || len(el) == 0 {
		t.Fatalf("eligible list: %d %v", len(el), err)
	}
	_, look, _ := o.Market(ctx, T, D)
	var ucc, isin string
	for _, tr := range ts {
		if tr.Segment == "CM" && tr.Product == "CNC" && tr.Side == "B" && tr.Qty >= 4 {
			if i, ok := look.ForTrade(tr); ok {
				if _, ok := el[i.ISIN]; ok {
					ucc, isin = tr.UCC, i.ISIN
					break
				}
			}
		}
	}
	if ucc == "" {
		t.Fatal("no candidate trade")
	}
	if e := el[isin]; e.MarginPct.Cmp(money.MustDec("20")) < 0 {
		t.Fatalf("margin below floor: %s", e.MarginPct)
	}
	if _, err := o.EnrolMTF(ctx, T, "priya.nair", MTFEnrolment{UCC: ucc, Agreed: D, Limit: 100000000}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker enrolled: %v", err)
	}
	if _, err := o.EnrolMTF(ctx, T, "anita.rao", MTFEnrolment{UCC: ucc, Agreed: D, Limit: 100000000}); err != nil {
		t.Fatal(err)
	}
	v, _ := o.MTF(ctx, T, D)
	var c MTFCandidate
	for _, x := range v.Candidates {
		if x.UCC == ucc && x.ISIN == isin {
			c = x
		}
	}
	if c.Qty == 0 {
		t.Fatalf("candidate missing: %+v", v.Candidates)
	}
	before, _ := o.clientFunds(ctx, T, D, "")
	q := c.Qty / 2
	p, err := o.FundMTF(ctx, T, "anita.rao", D, ucc, isin, q)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := o.clientFunds(ctx, T, D, "")
	if after[ucc]-before[ucc] != p.Funded || p.Funded <= 0 || p.Funded >= p.Cost {
		t.Fatalf("funding: %v -> %v funded %v cost %v", before[ucc], after[ucc], p.Funded, p.Cost)
	}
	if _, err := o.FundMTF(ctx, T, "anita.rao", D, ucc, isin, c.Qty); err == nil {
		t.Fatal("funded more than bought")
	}
	// Pledge pending past T+1 → liquidate.
	set, _ := o.Settings(ctx, T)
	t2 := set.nextBusinessDay(set.nextBusinessDay(D))
	v, _ = o.MTF(ctx, T, t2)
	if v.Positions[0].State != "Liquidate" || v.Positions[0].Accrued <= 0 {
		t.Fatalf("unpledged: %+v", v.Positions[0])
	}
	f, n, _ := o.MTFLiquidationFile(ctx, T, t2)
	if n != 1 || !strings.Contains(string(f), p.ID) {
		t.Fatalf("liquidation file: %d", n)
	}
	if _, err := o.MarkMTFPledged(ctx, T, "priya.nair", p.ID, "PSN123"); err != nil {
		t.Fatal(err)
	}
	if v, _ = o.MTF(ctx, T, t2); v.Positions[0].State == "Liquidate" {
		t.Fatalf("still liquidate after pledge: %s", v.Positions[0].Why)
	}
	// Interest and repayment.
	in, err := o.PostMTFInterest(ctx, T, "anita.rao", t2, "")
	if err != nil || in <= 0 {
		t.Fatalf("interest: %v %v", in, err)
	}
	if again, _ := o.PostMTFInterest(ctx, T, "anita.rao", t2, ""); again != 0 {
		t.Fatalf("interest twice: %v", again)
	}
	rep, _ := o.MTFReport(ctx, T, t2)
	if !strings.Contains(string(rep), p.ID[:0]+ucc) || !strings.Contains(string(rep), "TimesNetWorth") {
		t.Fatalf("report: %s", rep)
	}
	funds, _ := o.clientFunds(ctx, T, t2, "")
	if funds[ucc] >= p.Funded {
		cl, err := o.CloseMTF(ctx, T, "anita.rao", t2, p.ID, 0, "client paid")
		if err != nil || cl.Status != "Closed" || cl.Funded != 0 {
			t.Fatalf("close: %+v %v", cl, err)
		}
	} else if _, err := o.CloseMTF(ctx, T, "anita.rao", t2, p.ID, 0, "client paid"); err == nil {
		t.Fatal("repaid without funds")
	}
	tb, _ := o.App.Ledger.TrialBalance(ctx, T, t2)
	if !tb.Balanced {
		t.Fatal("trial balance")
	}
}
