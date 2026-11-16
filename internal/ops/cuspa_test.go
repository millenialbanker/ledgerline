package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

func TestCeilQty(t *testing.T) {
	for _, c := range []struct {
		amount, per string
		want        int64
	}{{"0", "10", 0}, {"-5", "10", 0}, {"10", "10", 1}, {"10.01", "10", 2}, {"2750", "100", 28}, {"99.99", "33.33", 3}, {"1", "0.3", 4}} {
		if got := ceilQty(money.MustDec(c.amount), money.MustDec(c.per)); got != c.want {
			t.Errorf("ceilQty(%s, %s) = %d, want %d", c.amount, c.per, got, c.want)
		}
	}
}

// The 2.25 × (C − B) limit, oldest pledge first, valued at cost when
// there is no price.
func TestCUSPALimits(t *testing.T) {
	line := func(id, date, ucc string, qty int64, cost string) CUSPAItem {
		return CUSPAItem{ID: id, Date: date, UCC: ucc, ISIN: "INE000X0" + id, Symbol: "S" + id, Qty: qty, Value: money.MustDec(cost).MulInt(qty), Status: CUSPAHeld}
	}
	b := cuspaBook{date: "2026-11-02", items: []CUSPAItem{
		line("01", "2026-10-26", "A1", 30, "100"), // older: released first
		line("02", "2026-10-27", "A1", 20, "100"),
		line("03", "2026-10-27", "B1", 10, "50"),
		line("04", "2026-10-27", "C1", 10, "50"),
	}, clients: map[string]Client{}, margin: map[string]money.Dec{"B1": money.MustDec("200")},
		funds: map[string]money.Amount{"A1": money.MustAmount("-1000"), "B1": money.MustAmount("500")}}
	cs, rel := b.limits()
	if len(cs) != 3 {
		t.Fatalf("clients %+v", cs)
	}
	a := cs[0]
	// A = 50 × 100 = 5000; limit 2.25 × (0 − (−1000)) = 2250; excess 2750:
	// 28 shares, all from the older line.
	if a.UCC != "A1" || a.Pledged.String() != "5000" || a.Max.String() != "2250" || a.Excess.String() != "2750" || a.ReleaseAll {
		t.Fatalf("A1 %+v", a)
	}
	if rel["01"] != 28 || rel["02"] != 0 {
		t.Fatalf("release plan %v", rel)
	}
	// B1's ledger covers its margin: everything goes; C1 has neither a
	// debit nor a margin: everything goes too.
	if !cs[1].ReleaseAll || rel["03"] != 10 || !cs[2].ReleaseAll || rel["04"] != 10 {
		t.Fatalf("release all: %+v %v", cs[1:], rel)
	}
	// More excess than the older line holds spills into the next one.
	b.funds["A1"] = money.MustAmount("-100") // limit 225, excess 4775
	_, rel = b.limits()
	if rel["01"] != 30 || rel["02"] != 18 {
		t.Fatalf("spill %v", rel)
	}
}

func TestCUSPAEarlyPayIn(t *testing.T) {
	items := []CUSPAItem{
		{UCC: "A1", ISIN: "X", Invoked: 10, InvokedOn: "2026-11-02"},
		{UCC: "A1", ISIN: "X", Invoked: 5, InvokedOn: "2026-11-02", Sold: 5, SoldOn: "2026-11-02"},
		{UCC: "A1", ISIN: "X", Invoked: 7, InvokedOn: "2026-11-04"},
	}
	if got := cuspaEarlyPayIn(items, "2026-11-02")[holdingID("A1", "X")]; got != 15 {
		t.Fatalf("on the day of sale %d, want 15", got)
	}
	if got := cuspaEarlyPayIn(items, "2026-11-03")[holdingID("A1", "X")]; got != 10 {
		t.Fatalf("the day after %d, want 10", got)
	}
}

func TestCUSPAFramework(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	set.CUSPADays = 6
	if err := o.SaveSettings(ctx, T, "admin", set); err == nil {
		t.Fatal("a payment period beyond 5 trading days was accepted")
	}
	set.CUSPADays = 3
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	set, _ = o.Settings(ctx, T)
	st, _ := o.Obligations(ctx, T, D)
	itc := obl(t, st, "BH1010", "NSE", "ITC")
	if itc.Action != "cuspa" {
		t.Fatalf("ITC buy of BH1010 is %s, want a funds shortfall", itc.Status)
	}
	if _, err := o.SettleAction(ctx, T, "priya.nair", D, itc.Key, "cuspa"); err != nil {
		t.Fatal(err)
	}
	items, _ := docs.List[CUSPAItem](ctx, o.Docs, T, cCUSPA, "")
	if len(items) != 1 {
		t.Fatalf("items %+v", items)
	}
	it := items[0]
	payOut := set.NextSettlementDay(D)
	if it.PayOut != payOut || it.Exchange != "NSE" || it.Qty != itc.NetQty || len(it.Events) != 1 {
		t.Fatalf("line %+v", it)
	}
	day := func(n int) string { return addTradingDays(set, payOut, n) }

	// On the pay-out day: in the payment period; notice possible, not
	// invocation.
	v, err := o.CUSPAAgeing(ctx, T, payOut)
	if err != nil || len(v.Rows) != 1 {
		t.Fatalf("%+v %v", v, err)
	}
	r := v.Rows[0]
	if r.Day != 0 || r.Bucket != "period" || r.PayBy != day(3) || r.InvokeFrom != day(4) || r.LastInvoke != day(5) || r.AutoRelease != day(6) ||
		!contains(r.Can, "release") || !contains(r.Can, "notice") || contains(r.Can, "invoke") || v.Policy.PayDays != 3 {
		t.Fatalf("row %+v", r)
	}
	if r.Name == "" || r.Ledger.Sign() >= 0 {
		t.Fatalf("client and ledger %+v", r)
	}
	if v.Totals["period"] != "1" {
		t.Fatalf("totals %v", v.Totals)
	}

	// Invocation needs notice, the end of the payment period and a checker
	// or risk manager.
	inv := CUSPABulkReq{Date: day(4), Action: "invoke", IDs: []string{it.ID}}
	if _, err := o.CUSPABulk(ctx, T, "priya.nair", inv); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker invoked: %v", err)
	}
	res, err := o.CUSPABulk(ctx, T, "anita.rao", inv)
	if err != nil || res.Done != 0 || !strings.Contains(res.Lines[0].Why, "notice") {
		t.Fatalf("invoked without notice: %+v %v", res, err)
	}
	res, err = o.CUSPABulk(ctx, T, "priya.nair", CUSPABulkReq{Date: day(1), Action: "notice", IDs: []string{it.ID, "CU-99999"}})
	if err != nil || res.Done != 1 || res.Skipped != 1 {
		t.Fatalf("notice %+v %v", res, err)
	}
	ms, _ := o.Messages(ctx, T, "BH1010", 20)
	sentNotice := false
	for _, m := range ms {
		if m.Event == "cuspa.notice" && m.Status == "Sent" && strings.Contains(m.Body, "ITC") {
			sentNotice = true
		}
	}
	if !sentNotice {
		t.Fatalf("no notice in the messages: %+v", ms)
	}
	inv.Date = day(3)
	if res, _ = o.CUSPABulk(ctx, T, "anita.rao", inv); res.Done != 0 || !strings.Contains(res.Lines[0].Why, "until") {
		t.Fatalf("invoked within the payment period: %+v", res)
	}
	inv.Date = day(6)
	if res, _ = o.CUSPABulk(ctx, T, "anita.rao", inv); res.Done != 0 || !strings.Contains(res.Lines[0].Why, "last day") {
		t.Fatalf("invoked after day 5: %+v", res)
	}

	// On day 4 a risk manager invokes as much as covers the debit.
	inv.Date, inv.Preview = day(4), true
	pv, err := o.CUSPABulk(ctx, T, "anita.rao", inv)
	if err != nil || pv.Done != 1 || pv.Lines[0].Qty <= 0 {
		t.Fatalf("preview %+v %v", pv, err)
	}
	if got, _, _ := docs.Get[CUSPAItem](ctx, o.Docs, T, cCUSPA, it.ID); got.Invoked != 0 {
		t.Fatal("the preview invoked")
	}
	b, _ := o.cuspaBook(ctx, T, day(4))
	px, _, _ := b.price(it)
	want := min(it.Qty, ceilQty(b.funds["BH1010"].Dec().Neg(), px))
	if pv.Lines[0].Qty != want {
		t.Fatalf("invoke %d, want %d (debit %s at %s)", pv.Lines[0].Qty, want, b.funds["BH1010"], px)
	}
	inv.Preview = false
	if res, err = o.CUSPABulk(ctx, T, "anita.rao", inv); err != nil || res.Done != 1 {
		t.Fatalf("invoke %+v %v", res, err)
	}
	it, _, _ = docs.Get[CUSPAItem](ctx, o.Docs, T, cCUSPA, it.ID)
	if it.Invoked != want || it.Qty != itc.NetQty-want || it.InvokedOn != day(4) || it.NoticeOn != day(1) {
		t.Fatalf("after invocation %+v", it)
	}
	_, sell, err := o.CUSPAFile(ctx, T, day(4), "sell")
	if err != nil || !strings.Contains(string(sell), "BH1010") || !strings.Contains(string(sell), it.ID) {
		t.Fatalf("sell list %q %v", sell, err)
	}
	_, dep, _ := o.CUSPAFile(ctx, T, day(4), "depository")
	if !strings.Contains(string(dep), "Invoke,"+it.ID) {
		t.Fatalf("depository list %q", dep)
	}

	// The sale is recorded; what is left is still held.
	if res, err = o.CUSPABulk(ctx, T, "priya.nair", CUSPABulkReq{Date: day(4), Action: "sold", IDs: []string{it.ID}}); err != nil || res.Done != 1 {
		t.Fatalf("sold %+v %v", res, err)
	}
	it, _, _ = docs.Get[CUSPAItem](ctx, o.Docs, T, cCUSPA, it.ID)
	wantStatus := CUSPAHeld
	if it.Qty == 0 {
		wantStatus = CUSPASold
	}
	if it.Sold != want || it.SoldOn != day(4) || it.Status != wantStatus {
		t.Fatalf("after the sale %+v", it)
	}

	// After day 6 the daily review records the depository's release: the
	// rest goes to the client's demat, and the debit is left to recover.
	t.Logf("invoked %d of %d", it.Invoked, itc.NetQty)
	if it.Qty > 0 {
		before := map[string]money.Dec{}
		hs, _ := o.Holdings(ctx, T, "BH1010")
		for _, h := range hs {
			before[h.ISIN] = before[h.ISIN].Add(h.Qty)
		}
		left := it.Qty
		sum, _, err := o.cuspaReview(ctx, T, SystemActor, day(7))
		if err != nil || !strings.Contains(sum, "1 released by the depository") {
			t.Fatalf("review %q %v", sum, err)
		}
		it, _, _ = docs.Get[CUSPAItem](ctx, o.Docs, T, cCUSPA, it.ID)
		if it.Qty != 0 || it.Released != left || it.Status != CUSPASold {
			t.Fatalf("after auto-release %+v", it)
		}
		after := map[string]money.Dec{}
		hs, _ = o.Holdings(ctx, T, "BH1010")
		for _, h := range hs {
			after[h.ISIN] = after[h.ISIN].Add(h.Qty)
		}
		if after[it.ISIN].Sub(before[it.ISIN]).Cmp(money.DecInt(left)) != 0 {
			t.Fatalf("holding %s -> %s, want +%d", before[it.ISIN], after[it.ISIN], left)
		}
	}
}

// The daily review releases what is over the 2.25× limit and queues the
// notice; a paid-up client gets everything back.
func TestCUSPAReview(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	st, _ := o.Obligations(ctx, T, D)
	itc := obl(t, st, "BH1010", "NSE", "ITC")
	if _, err := o.SettleAction(ctx, T, "priya.nair", D, itc.Key, "cuspa"); err != nil {
		t.Fatal(err)
	}
	payOut := set.NextSettlementDay(D)
	payBy := addTradingDays(set, payOut, set.cuspaPayDays())
	if _, _, err := o.cuspaReview(ctx, T, SystemActor, payBy); err != nil {
		t.Fatal(err)
	}
	// Whatever is left is within the limit.
	b, _ := o.cuspaBook(ctx, T, payBy)
	cs, rel := b.limits()
	for _, c := range cs {
		if !c.ReleaseAll && c.Excess.Sign() > 0 {
			t.Fatalf("still over the limit after the review: %+v %v", c, rel)
		}
	}
	items, _ := docs.List[CUSPAItem](ctx, o.Docs, T, cCUSPA, "")
	if items[0].Qty > 0 {
		ws, _ := o.WorkItems(ctx, T, "Open")
		found := false
		for _, w := range ws {
			if w.Process == "cuspa" && strings.Contains(w.Title, "notice") && w.Proposed != nil && w.Proposed.Kind == "cuspa.notice" {
				found = true
				// The work queue sends it in one click.
				if err := o.carryOut(ctx, T, "priya.nair", payBy, *w.Proposed); err != nil {
					t.Fatal(err)
				}
			}
		}
		if !found {
			t.Fatalf("no notice in the work queue: %+v", ws)
		}
		it, _, _ := docs.Get[CUSPAItem](ctx, o.Docs, T, cCUSPA, items[0].ID)
		if it.NoticeOn != payBy {
			t.Fatalf("notice not recorded %+v", it)
		}
	}

	// The client pays: the next review releases everything.
	debit := b.funds["BH1010"]
	if debit < 0 {
		rc, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: payBy, UCC: "BH1010", Mode: "NEFT", Ref: "UTRCUSPA1", Amount: -debit})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := o.DecideReceipt(ctx, T, "farhan.khan", rc.ID, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := o.cuspaReview(ctx, T, SystemActor, set.NextBusinessDay(payBy)); err != nil {
		t.Fatal(err)
	}
	it, _, _ := docs.Get[CUSPAItem](ctx, o.Docs, T, cCUSPA, items[0].ID)
	if it.Qty != 0 || it.Status != CUSPAReleased || it.Released != itc.NetQty {
		t.Fatalf("after payment %+v", it)
	}
	_, dep, _ := o.CUSPAFile(ctx, T, set.NextBusinessDay(payBy), "depository")
	if !strings.Contains(string(dep), "Release (unpledge),"+it.ID) {
		t.Fatalf("depository list %q", dep)
	}
}

// Invoked shares sold under the client's code: the sale's pay-in comes
// from them (early pay-in), the contract note says so, and the daily
// review finds the sale.
func TestCUSPAInvokedSale(t *testing.T) {
	o, ctx := setup(t)
	st, _ := o.Obligations(ctx, T, D)
	var sale Obligation
	for _, ob := range st.Obligations {
		if ob.NetQty < 0 && ob.Status == "Ready for pay-in" && !strings.HasPrefix(ob.ISIN, "UNKNOWN") {
			sale = ob
			break
		}
	}
	if sale.Key == "" {
		t.Fatal("no delivery sale in the sample day")
	}
	q := -sale.NetQty
	it := CUSPAItem{ID: "CU-T9", Date: addDays(D, -7), UCC: sale.UCC, ISIN: sale.ISIN, Symbol: sale.Symbol, Invoked: q, InvokedOn: D, Value: money.DecInt(100 * q), Status: CUSPAInvoked}
	if err := docs.Upsert(ctx, o.Docs, T, cCUSPA, it.ID, it, "test"); err != nil {
		t.Fatal(err)
	}
	st, _ = o.Obligations(ctx, T, D)
	if ob := obl(t, st, sale.UCC, sale.Exchange, sale.Symbol); ob.Status != "Early pay-in (CUSPA)" || ob.Action != "" || ob.FromCUSPA != q {
		t.Fatalf("sale of invoked shares %+v", ob)
	}
	if _, err := o.GenerateNotes(ctx, T, "priya.nair", D); err != nil {
		t.Fatal(err)
	}
	v, err := o.noteView(ctx, T, D, sale.UCC, "")
	if err != nil || len(v.Remarks) != 1 || !strings.Contains(v.Remarks[0], "CUSPA line CU-T9") {
		t.Fatalf("remarks %q %v", v.Remarks, err)
	}
	b, _ := o.cuspaBook(ctx, T, D)
	if n := o.cuspaSpotSales(ctx, T, b); n != 1 {
		t.Fatalf("sales found %d", n)
	}
	got, _, _ := docs.Get[CUSPAItem](ctx, o.Docs, T, cCUSPA, it.ID)
	if got.Sold != q || got.SoldOn != D || got.Status != CUSPASold {
		t.Fatalf("after the review %+v", got)
	}
	// Still the early pay-in on the day of the sale.
	st, _ = o.Obligations(ctx, T, D)
	if ob := obl(t, st, sale.UCC, sale.Exchange, sale.Symbol); ob.FromCUSPA != q {
		t.Fatalf("sale on the day %+v", ob)
	}
}
