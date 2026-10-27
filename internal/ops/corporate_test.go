package ops

import (
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

func obl(t *testing.T, st Settlement, ucc, exch, sym string) Obligation {
	t.Helper()
	for _, ob := range st.Obligations {
		if ob.UCC == ucc && ob.Exchange == exch && ob.Symbol == sym {
			return ob
		}
	}
	t.Fatalf("no obligation %s %s %s", ucc, exch, sym)
	return Obligation{}
}

func TestShortDeliveries(t *testing.T) {
	o, ctx := setup(t)
	st, _ := o.Obligations(ctx, T, D)
	seller, buyer := obl(t, st, "BH1013", "NSE", "BAJFINANCE"), obl(t, st, "BH1011", "NSE", "BAJFINANCE")
	if _, err := o.SettleAction(ctx, T, "priya.nair", D, seller.Key, "report_short"); err != nil {
		t.Fatal(err)
	}
	ss, _ := o.Shortages(ctx, T)
	if len(ss) != 1 || ss[0].Qty != 47 || ss[0].Internal != 47 || ss[0].Auction != 0 || len(ss[0].Buyers) != 1 || ss[0].Buyers[0].UCC != "BH1011" {
		t.Fatalf("shortage = %+v", ss)
	}
	if ss[0].Suggested.Sign() <= 0 {
		t.Fatal("no suggested close-out price")
	}
	st, _ = o.Obligations(ctx, T, D)
	if b := obl(t, st, "BH1011", "NSE", "BAJFINANCE"); b.Status != "Awaiting close-out" || b.Action != "" {
		t.Fatalf("buyer not held: %+v", b)
	}
	before, _ := o.clientFunds(ctx, T, "", "")
	h0, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID("BH1011", buyer.ISIN))
	if _, err := o.SettleShortage(ctx, T, "priya.nair", ss[0].ID, D, money.MustDec("1100")); err == nil {
		t.Fatal("maker settled a shortage")
	}
	sh, err := o.SettleShortage(ctx, T, "farhan.khan", ss[0].ID, D, money.MustDec("1100"))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := o.clientFunds(ctx, T, "", "")
	if d := before["BH1013"] - after["BH1013"]; d.String() != "51700.00" {
		t.Fatalf("seller debited %s", d)
	}
	if d := after["BH1011"] - before["BH1011"]; d.String() != "51700.00" {
		t.Fatalf("buyer credited %s", d)
	}
	h1, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID("BH1011", buyer.ISIN))
	if h1.Qty-h0.Qty != 125 {
		t.Fatalf("buyer received %d shares, want 125", h1.Qty-h0.Qty)
	}
	if !strings.HasPrefix(sh.Status, "Settled") {
		t.Fatal(sh.Status)
	}
	// BSE TCS: the firm's buyer is paid out first, so nothing hides the
	// shortfall and it goes to the exchange auction.
	for _, ob := range st.Obligations {
		if ob.Exchange == "BSE" && ob.Symbol == "TCS" && ob.NetQty > 0 && ob.Action != "" {
			if _, err := o.SettleAction(ctx, T, "priya.nair", D, ob.Key, ob.Action); err != nil {
				t.Fatal(err)
			}
		}
	}
	st, _ = o.Obligations(ctx, T, D)
	tcs := obl(t, st, "BH1007", "BSE", "TCS")
	if _, err := o.SettleAction(ctx, T, "priya.nair", D, tcs.Key, "report_short"); err != nil {
		t.Fatal(err)
	}
	ss, _ = o.Shortages(ctx, T)
	if ss[0].Auction != ss[0].Qty || ss[0].Internal != 0 {
		t.Fatalf("auction shortage = %+v", ss[0])
	}
	if _, err := o.SettleShortage(ctx, T, "farhan.khan", ss[0].ID, D, money.MustDec("3200")); err != nil {
		t.Fatal(err)
	}
	tb, _ := o.App.Ledger.TrialBalance(ctx, T, "")
	if !tb.Balanced {
		t.Fatal("trial balance")
	}
}

func TestCorporateActionsAndBuyBack(t *testing.T) {
	o, ctx := setup(t)
	st, _ := o.Obligations(ctx, T, D)
	var infy Holding
	hs, _ := o.Holdings(ctx, T, "BH1001")
	for _, h := range hs {
		if h.Qty-h.Pledged >= 20 {
			infy = h
			break
		}
	}
	if infy.ISIN == "" {
		t.Fatal("sample has no free holding for BH1001")
	}
	sym := infy.Symbol
	h0 := infy
	lots0, _, _, _ := o.equityBook(ctx, T, "BH1001", addDays(D, -1))
	var held int64
	for _, l := range lots0 {
		if l.ISIN == infy.ISIN {
			held += l.Qty
		}
	}
	// Bonus 1:1 with ex-date D.
	if _, err := o.SaveCorpAction(ctx, T, "priya.nair", CorpAction{ISIN: infy.ISIN, Symbol: sym, Type: "Bonus", ExDate: D, RecordDate: D, PayDate: addDays(D, 2), RatioNew: 1, RatioOld: 1}); err != nil {
		t.Fatal(err)
	}
	cas, _ := o.CorpActions(ctx, T)
	var line CorpEntitle
	for _, l := range cas[0].Lines {
		if l.UCC == "BH1001" {
			line = l
		}
	}
	if line.NewShares != h0.Qty {
		t.Fatalf("bonus entitlement %d, want %d", line.NewShares, h0.Qty)
	}
	if _, err := o.ApplyCorpAction(ctx, T, "priya.nair", cas[0].ID); err == nil {
		t.Fatal("maker applied a corporate action")
	}
	if _, err := o.ApplyCorpAction(ctx, T, "farhan.khan", cas[0].ID); err != nil {
		t.Fatal(err)
	}
	h1, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID("BH1001", infy.ISIN))
	if h1.Qty != 2*h0.Qty {
		t.Fatalf("holding after bonus %d, want %d", h1.Qty, 2*h0.Qty)
	}
	// Purchase lots: a nil-cost bonus lot dated the allotment date.
	lots, _, _, _ := o.equityBook(ctx, T, "BH1001", D)
	var bonus *Lot
	for i := range lots {
		if lots[i].ISIN == infy.ISIN && lots[i].Price.IsZero() && lots[i].Date == addDays(D, 2) {
			bonus = &lots[i]
		}
	}
	if held == 0 || bonus == nil || bonus.Qty != held {
		t.Fatalf("bonus lot = %+v, want %d shares", bonus, held)
	}

	// Dividend on shares the broker holds in CUSPA for BH1010.
	itc := obl(t, st, "BH1010", "NSE", "ITC")
	if _, err := o.SettleAction(ctx, T, "priya.nair", D, itc.Key, "cuspa"); err != nil {
		t.Fatal(err)
	}
	dv, err := o.SaveCorpAction(ctx, T, "priya.nair", CorpAction{ISIN: itc.ISIN, Symbol: "ITC", Type: "Dividend", ExDate: addDays(D, 5), RecordDate: addDays(D, 5), DPS: money.MustDec("6.50")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApplyCorpAction(ctx, T, "farhan.khan", dv.ID); err != nil {
		t.Fatal(err)
	}
	owed := money.MustDec("6.50").MulInt(itc.NetQty).Round(money.UnitPaisa)
	if _, err := o.ReceiveDividend(ctx, T, "farhan.khan", dv.ID, D, "NEFT-DIV-1", owed-1); err == nil {
		t.Fatal("short dividend receipt accepted")
	}
	before, _ := o.clientFunds(ctx, T, "", "")
	if _, err := o.ReceiveDividend(ctx, T, "farhan.khan", dv.ID, D, "NEFT-DIV-1", owed+1000); err != nil {
		t.Fatal(err)
	}
	after, _ := o.clientFunds(ctx, T, "", "")
	if after["BH1010"]-before["BH1010"] != owed {
		t.Fatalf("dividend credited %s, want %s", after["BH1010"]-before["BH1010"], owed)
	}
	cas, _ = o.CorpActions(ctx, T)
	for _, c := range cas {
		if c.ID == dv.ID && (c.Recon == nil || c.Recon.Excess != 1000) {
			t.Fatalf("dividend recon = %+v", c.Recon)
		}
	}

	// Buy-back: tender, file, partial acceptance.
	bb, err := o.SaveBuyBack(ctx, T, "priya.nair", BuyBack{ISIN: infy.ISIN, Symbol: sym, Company: "Sample Co", Price: money.MustDec("1800"), Record: D, Open: D, Close: addDays(D, 5)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.TenderBuyBack(ctx, T, "priya.nair", bb.ID, D, "BH1001", h1.Qty+1, false); err == nil {
		t.Fatal("tender above free holding accepted")
	}
	if _, err := o.TenderBuyBack(ctx, T, "priya.nair", bb.ID, D, "BH1001", 20, false); err != nil {
		t.Fatal(err)
	}
	f, _ := o.BuyBackFile(ctx, T, bb.ID)
	if !strings.Contains(string(f), sym+","+infy.ISIN+",BH1001,") || !strings.Contains(string(f), ",20,1800.00,SMALL SHAREHOLDER") {
		t.Fatalf("bid file:\n%s", f)
	}
	before, _ = o.clientFunds(ctx, T, "", "")
	if _, err := o.SettleBuyBack(ctx, T, "farhan.khan", bb.ID, addDays(D, 7), "NSE", map[string]int64{"BH1001": 12}); err != nil {
		t.Fatal(err)
	}
	after, _ = o.clientFunds(ctx, T, "", "")
	if d := after["BH1001"] - before["BH1001"]; d.String() != "21600.00" {
		t.Fatalf("buy-back credit %s", d)
	}
	h2, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID("BH1001", infy.ISIN))
	if h1.Qty-h2.Qty != 12 {
		t.Fatalf("holding after buy-back %d", h2.Qty)
	}
	tb, _ := o.App.Ledger.TrialBalance(ctx, T, "")
	if !tb.Balanced {
		t.Fatal("trial balance")
	}
	_ = ledger.BankClient
}

func TestMergerSymbolChangeRightsAndIPO(t *testing.T) {
	o, ctx := setup(t)
	hs, _ := o.Holdings(ctx, T, "BH1001")
	var h Holding
	for _, x := range hs {
		if x.Qty >= 10 {
			h = x
			break
		}
	}
	if h.ISIN == "" {
		t.Fatal("no holding")
	}
	lots0, _, _, _ := o.equityBook(ctx, T, "BH1001", D)
	var cost0 money.Dec
	var q0 int64
	for _, l := range lots0 {
		if l.ISIN == h.ISIN {
			cost0, q0 = cost0.Add(l.Price.MulInt(l.Qty)), q0+l.Qty
		}
	}
	// Merger: 1 new share for every 2 held; ex-date the next day.
	ex := addDays(D, 1)
	newISIN := "INE999Z01011"
	m, err := o.SaveCorpAction(ctx, T, "priya.nair", CorpAction{ISIN: h.ISIN, Symbol: h.Symbol, Type: "Merger", NewISIN: newISIN, NewSymbol: "NEWCO", ExDate: ex, RecordDate: ex, RatioNew: 1, RatioOld: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SaveCorpAction(ctx, T, "priya.nair", CorpAction{ISIN: h.ISIN, Type: "Merger", NewISIN: h.ISIN, ExDate: ex, RecordDate: ex, RatioNew: 1, RatioOld: 1}); err == nil {
		t.Fatal("merger into the same ISIN accepted")
	}
	if _, err := o.ApplyCorpAction(ctx, T, "farhan.khan", m.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := o.Holdings(ctx, T, "BH1001")
	var old, nw Holding
	for _, x := range after {
		if x.ISIN == h.ISIN {
			old = x
		}
		if x.ISIN == newISIN {
			nw = x
		}
	}
	if old.Qty != 0 || nw.Qty != h.Qty/2 || nw.Symbol != "NEWCO" {
		t.Fatalf("holdings old %+v new %+v", old, nw)
	}
	// Lots: same total cost, half the shares, under the new ISIN, original dates.
	lots1, _, _, _ := o.equityBook(ctx, T, "BH1001", ex)
	var cost1 money.Dec
	var q1 int64
	for _, l := range lots1 {
		if l.ISIN == h.ISIN && l.Qty > 0 {
			t.Fatalf("old ISIN lot left: %+v", l)
		}
		if l.ISIN == newISIN {
			cost1, q1 = cost1.Add(l.Price.MulInt(l.Qty)), q1+l.Qty
		}
	}
	if q1 != q0/2 || cost1.Sub(cost0).Abs().Cmp(money.MustDec("1")) > 0 {
		t.Fatalf("lots %d cost %s want %s", q1, cost1, cost0)
	}
	// Symbol change, one for one.
	sc, err := o.SaveCorpAction(ctx, T, "priya.nair", CorpAction{ISIN: newISIN, Symbol: "NEWCO", Type: "Symbol change", NewSymbol: "NEWCOLTD", ExDate: addDays(ex, 1), RecordDate: addDays(ex, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApplyCorpAction(ctx, T, "farhan.khan", sc.ID); err != nil {
		t.Fatal(err)
	}
	after, _ = o.Holdings(ctx, T, "BH1001")
	for _, x := range after {
		if x.ISIN == newISIN && (x.Symbol != "NEWCOLTD" || x.Qty != h.Qty/2) {
			t.Fatalf("symbol change %+v", x)
		}
	}
	// Rights: 1 entitlement for every 5 held.
	rt, err := o.SaveCorpAction(ctx, T, "priya.nair", CorpAction{ISIN: newISIN, Symbol: "NEWCOLTD", Type: "Rights", NewISIN: "INE999Z20012", ExDate: addDays(ex, 2), RecordDate: addDays(ex, 2), RatioNew: 1, RatioOld: 5})
	if err != nil || rt.NewSymbol != "NEWCOLTD-RE" {
		t.Fatalf("%+v %v", rt, err)
	}
	if _, err := o.ApplyCorpAction(ctx, T, "farhan.khan", rt.ID); err != nil {
		t.Fatal(err)
	}
	after, _ = o.Holdings(ctx, T, "BH1001")
	found := false
	for _, x := range after {
		found = found || (x.ISIN == "INE999Z20012" && x.Qty == h.Qty/2/5)
	}
	if !found && h.Qty/2/5 > 0 {
		t.Fatalf("rights entitlements not credited: %+v", after)
	}
	// IPO allotment posted as a purchase at the issue price.
	_ = docs.Upsert(ctx, o.Docs, T, cIPO, "IPO-T1", IPOApp{ID: "IPO-T1", UCC: "BH1002", Issue: "FICTCO IPO", Lots: 2, Allotment: "Allotted", Allotted: 1}, "test")
	r, err := o.PostIPOAllotments(ctx, T, "priya.nair", "FICTCO IPO", "INE999Y01019", "FICTCO", D, money.MustDec("250"), 60)
	if err != nil || r.Posted != 1 || r.Shares != 60 {
		t.Fatalf("ipo %+v %v", r, err)
	}
	if r2, _ := o.PostIPOAllotments(ctx, T, "priya.nair", "FICTCO IPO", "INE999Y01019", "FICTCO", D, money.MustDec("250"), 60); r2.Posted != 0 || r2.Skipped != 1 {
		t.Fatalf("ipo again %+v", r2)
	}
	ext, _ := o.ExternalTrades(ctx, T, "BH1002")
	if len(ext) == 0 || ext[len(ext)-1].Price.String() != "250" {
		t.Fatalf("ipo purchase %+v", ext)
	}
}
