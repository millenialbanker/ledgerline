package ops

import (
	"testing"

	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

func TestPartnersScopeAndPrepaid(t *testing.T) {
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
	// Hierarchy: AP001 under RM01; RM01's team brokerage includes AP001's.
	ps, _ := o.Partners(ctx, T, D)
	idx := map[string]Partner{}
	for _, p := range ps {
		idx[p.Code] = p
	}
	if idx["AP001"].Clients == 0 || idx["RM01"].Team < idx["AP001"].Brokerage || len(idx["RM01"].Downline) != 1 {
		t.Fatalf("partners %+v", ps)
	}
	if _, err := o.SavePartner(ctx, T, "admin", Partner{Code: "RM01", Name: "x", Kind: "Relationship manager", Parent: "AP001"}); err == nil {
		t.Fatal("loop in hierarchy accepted")
	}
	// AP payout: 40% share, GST (AP has a GSTIN), 2% TDS.
	pays, err := o.APPayouts(ctx, T, D, D)
	if err != nil {
		t.Fatal(err)
	}
	var ap APPayout
	for _, p := range pays {
		if p.Partner == "AP001" {
			ap = p
		}
	}
	want := ap.Brokerage.Dec().Pct(money.MustDec("40")).Round(money.UnitPaisa)
	if ap.Brokerage == 0 || ap.Share != want || ap.GST == 0 || ap.TDS == 0 || ap.Payable != ap.Share+ap.GST-ap.TDS-ap.Withheld {
		t.Fatalf("AP001 payout %+v", ap)
	}
	if n, err := o.PostAPPayouts(ctx, T, "farhan.khan", D, D); err != nil || n < 1 {
		t.Fatalf("post: %d %v", n, err)
	}
	b, _ := o.App.Ledger.Balances(ctx, T, D)
	if -b["AP:AP001"] != ap.Share+ap.GST-ap.TDS || -b[PayableTDS194H] == 0 {
		t.Fatalf("AP ledger %s", -b["AP:AP001"])
	}
	// Referral: BH1001 referred BH1004 and BH1008.
	rs, _ := o.Referrals(ctx, T, D, D)
	if len(rs) != 1 || rs[0].Referrer != "BH1001" || rs[0].Reward == 0 {
		t.Fatalf("referrals %+v", rs)
	}
	before, _ := o.clientFunds(ctx, T, "", "")
	if n, _ := o.PostReferrals(ctx, T, "farhan.khan", D, D); n != 1 {
		t.Fatal("referral not posted")
	}
	after, _ := o.clientFunds(ctx, T, "", "")
	if after["BH1001"]-before["BH1001"] != rs[0].Reward {
		t.Fatal("referrer not credited")
	}
	if n, _ := o.PostReferrals(ctx, T, "farhan.khan", D, D); n != 0 {
		t.Fatal("referral posted twice")
	}
	// Data scope.
	cs, _ := o.ClientMap(ctx, T)
	br := o.ClientScope(ctx, T, "meera.iyer")
	apf := o.ClientScope(ctx, T, "sunrise.ap")
	if br == nil || apf == nil || o.ClientScope(ctx, T, "farhan.khan") != nil {
		t.Fatal("scope functions")
	}
	nb, na := 0, 0
	for u, c := range cs {
		if br(u) != (c.Branch == "Bengaluru") {
			t.Fatalf("branch scope wrong for %s (%s)", u, c.Branch)
		}
		if apf(u) != (c.Partner == "AP001") {
			t.Fatalf("AP scope wrong for %s", u)
		}
		if br(u) {
			nb++
		}
		if apf(u) {
			na++
		}
	}
	if nb == 0 || na == 0 {
		t.Fatalf("scopes empty: %d %d", nb, na)
	}
	// Private client: hidden from the checker, visible to compliance.
	if _, err := o.SetClientPrivacy(ctx, T, "farhan.khan", "BH1002", true, nil); err == nil {
		t.Fatal("checker made a client private")
	}
	if _, err := o.SetClientPrivacy(ctx, T, "vikram.shah", "BH1002", true, []string{"priya.nair"}); err != nil {
		t.Fatal(err)
	}
	if f := o.ClientScope(ctx, T, "farhan.khan"); f == nil || f("BH1002") || !f("BH1001") {
		t.Fatal("private client visible to checker")
	}
	if f := o.ClientScope(ctx, T, "priya.nair"); f == nil || !f("BH1002") {
		t.Fatal("private client hidden from named user")
	}
	if o.ClientScope(ctx, T, "vikram.shah") != nil {
		t.Fatal("compliance should see everything")
	}
	// Prepaid credit: brokerage adjusted against it.
	var rich string
	f, _ := o.clientFunds(ctx, T, D, "")
	for _, u := range sortedKeys(f) {
		if f[u] > 5000000 && cs[u].Status == "Active" {
			if w, _ := o.Withdrawable(ctx, T, D, u); w.Available > 2000000 {
				if br, _ := o.clientBrokerage(ctx, T, D, D); br[u] > 0 {
					rich = u
					break
				}
			}
		}
	}
	if rich == "" {
		t.Fatal("no client to buy a prepaid scheme")
	}
	sub, err := o.BuyPrepaid(ctx, T, "priya.nair", D, rich, "credit-10k", false)
	if err != nil {
		t.Fatal(err)
	}
	if sub.Credit.String() != "10000.00" || sub.GST.String() != "1800.00" {
		t.Fatalf("sub %+v", sub)
	}
	brk, _ := o.clientBrokerage(ctx, T, D, D)
	b0, _ := o.clientFunds(ctx, T, "", "")
	if a, _, _, err := o.RunPrepaid(ctx, T, "farhan.khan", D); err != nil || a != 1 {
		t.Fatalf("prepaid run %d %v", a, err)
	}
	b1, _ := o.clientFunds(ctx, T, "", "")
	gst := brk[rich].Dec().Pct(money.DecInt(18)).Round(money.UnitPaisa)
	if b1[rich]-b0[rich] != brk[rich]+gst {
		t.Fatalf("refund %s want %s", b1[rich]-b0[rich], brk[rich]+gst)
	}
	// GST register stays equal to the ledger after the adjustment.
	g, _ := o.GST(ctx, T, "2026-10-01", "2026-10-31")
	bal, _ := o.App.Ledger.Balances(ctx, T, "")
	if g.Out.IGST+g.Out.CGST+g.Out.SGST != -bal[ledger.PayableGST] {
		t.Fatalf("GST register %s vs ledger %s", g.Out.IGST+g.Out.CGST+g.Out.SGST, -bal[ledger.PayableGST])
	}
	// Expiry lapses the rest.
	if _, e, _, _ := o.RunPrepaid(ctx, T, "farhan.khan", addDays(D, 400)); e != 1 {
		t.Fatal("not expired")
	}
	bal, _ = o.App.Ledger.Balances(ctx, T, "")
	if bal[PayablePrepaid] != 0 {
		t.Fatalf("prepaid liability left %s", bal[PayablePrepaid])
	}
	tb, _ := o.App.Ledger.TrialBalance(ctx, T, "")
	if !tb.Balanced {
		t.Fatal("trial balance")
	}
}
