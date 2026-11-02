package ops

import (
	"context"
	"encoding/csv"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

func segPost(t *testing.T, o *Ops, ctx context.Context, key, date, acct, kind string, amt money.Amount) ledger.Entry {
	t.Helper()
	p := []ledger.Posting{{Account: acct, Credit: amt}, {Account: ledger.BankClient, Debit: amt}}
	if amt < 0 {
		p = []ledger.Posting{{Account: acct, Debit: -amt}, {Account: ledger.BankClient, Credit: -amt}}
	}
	e, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: key, ValueDate: date, CreatedBy: "test", Narration: key, Source: ledger.SourceRef{Kind: kind, ID: key}, Postings: p})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// The peak clear balance walks the day's postings in posting order; cheques
// count when they clear, and the day's bills do not lower the peak.
func TestSEGMONPeakAndUncleared(t *testing.T) {
	o, ctx := newOps(t)
	prev := "2026-10-15"
	a, b := "CLIENT:ZZPEAK1", "CLIENT:ZZPEAK2"
	segPost(t, o, ctx, "p1", prev, a, "TEST", 50000)     // opening 500
	segPost(t, o, ctx, "p2", D, a, "TEST", 100000)       // 1500
	segPost(t, o, ctx, "p3", D, a, "TEST", -120000)      // 300
	segPost(t, o, ctx, "p4", D, a, "TEST", 10000)        // 400
	chq := segPost(t, o, ctx, "p5", D, a, "TEST", 30000) // uncleared cheque
	old := segPost(t, o, ctx, "p6", prev, a, "TEST", 25000)
	_ = docs.Upsert(ctx, o.Docs, T, cReceipt, "R1", Receipt{ID: "R1", Date: D, UCC: "ZZPEAK1", Mode: "Cheque", Amount: 30000, Status: "Uncleared", Entry: chq.ID}, "test")
	_ = docs.Upsert(ctx, o.Docs, T, cReceipt, "R2", Receipt{ID: "R2", Date: prev, UCC: "ZZPEAK1", Mode: "Cheque", Amount: 25000, Status: "Credited", ClearedOn: D, Entry: old.ID}, "test")
	segPost(t, o, ctx, "q1", prev, b, "TEST", 50000) // opening 500
	segPost(t, o, ctx, "q2", D, b, "BILL", -200000)  // bill due today: -1500
	segPost(t, o, ctx, "q3", D, b, "TEST", 100000)   // -500 (1500 without the bill)
	bals, err := o.segmonBalances(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	x := bals[a]
	// ledger 500+250+1000-1200+100+300 = 950; clear 500 → 1500 → 300 → 400, then the old cheque clears: 650.
	if x.A.Fixed(2) != "950.00" || x.B.Fixed(2) != "650.00" || x.Peak.Fixed(2) != "1500.00" {
		t.Fatalf("a %s %s %s", x.A.Fixed(2), x.B.Fixed(2), x.Peak.Fixed(2))
	}
	y := bals[b]
	if y.A.Fixed(2) != "-500.00" || y.B.Fixed(2) != "-500.00" || y.Peak.Fixed(2) != "1500.00" {
		t.Fatalf("b %s %s %s", y.A.Fixed(2), y.B.Fixed(2), y.Peak.Fixed(2))
	}
	// A day later the cheque is still uncleared; the next day's peak starts from the clear balance.
	nb, _ := o.segmonBalances(ctx, T, "2026-10-19")
	if z := nb[a]; z.B.Fixed(2) != "650.00" || z.Peak.Fixed(2) != "650.00" {
		t.Fatalf("next day %s %s", z.B.Fixed(2), z.Peak.Fixed(2))
	}
}

// segmonLines reads the file back as rows keyed by UCC|segment|indicator.
func segmonLines(t *testing.T, b []byte) map[string][]string {
	t.Helper()
	recs, err := csv.NewReader(strings.NewReader(string(b))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, r := range recs[1:] {
		if len(r) != 65 {
			t.Fatalf("row of %d fields", len(r))
		}
		out[r[8]+"|"+r[7]+"|"+r[51]] = r
	}
	return out
}

func TestSEGMONColumns(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	cs, _ := o.ClientMap(ctx, T)
	var u string
	for _, k := range sortedKeys(cs) {
		if cs[k].Status == "Active" && rePAN.MatchString(cs[k].PAN) {
			u = k
			break
		}
	}
	_, look, _ := o.Market(ctx, T, D)
	var isin string
	var px money.Dec
	ts, _ := o.exchangeTrades(ctx, T, D)
	sold := map[string]bool{}
	for _, tr := range ts {
		if tr.UCC == u && tr.Side == "S" {
			if i, ok := look.ForTrade(tr); ok {
				sold[i.ISIN] = true
			}
		}
	}
	for _, k := range sortedKeys(look.m) {
		i := look.m[k]
		if i.ISIN != "" && i.LTP.Sign() > 0 && !strings.HasPrefix(i.ISIN, "IN00") && !sold[i.ISIN] {
			isin, px = i.ISIN, i.LTP
			break
		}
	}
	if u == "" || isin == "" {
		t.Fatal("no sample client or security")
	}
	// Early pay-in of 10 shares the client did not sell on the exchange that day: valued at the close.
	_ = docs.Upsert(ctx, o.Docs, T, cEPI, D+"|"+u+"|"+isin, EPIRecord{ID: "E1", Date: D, UCC: u, ISIN: isin, Qty: 10, Status: "Accepted"}, "test")
	// An MTF position: cost 10,000 at 25% margin, 7,500 funded.
	_ = docs.Upsert(ctx, o.Docs, T, cMTFPos, "MTF-T1", MTFPosition{ID: "MTF-T1", UCC: u, ISIN: isin, BuyDate: D, Qty: 1, Cost: 1000000, MarginPct: money.MustDec("25"), Funded: 750000, Status: "Open"}, "test")
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "MTF:T1", ValueDate: D, CreatedBy: "test", Narration: "mtf", Source: ledger.SourceRef{Kind: "MTF", ID: "T1"},
		Postings: []ledger.Posting{{Account: mtfLoan(u), Debit: 750000}, {Account: ledger.ClientAccount(u), Credit: 750000}, {Account: ledger.BankClient, Debit: 750000}, {Account: ledger.BankOwn, Credit: 750000}}}); err != nil {
		t.Fatal(err)
	}
	// Running account settlement on the previous trading day.
	ps := set.PrevBusinessDay(D)
	_ = docs.Upsert(ctx, o.Docs, T, cRASBatch, ps+"|x", RASBatch{Date: ps, Items: []RASItem{{UCC: u, Amount: 123400}}}, "test")

	chk, err := o.SEGMONCheck(ctx, T, "priya.nair", D)
	if err != nil {
		t.Fatal(err)
	}
	if !chk.OK {
		t.Fatalf("errors %+v", chk.Errors[:min(5, len(chk.Errors))])
	}
	if chk.Totals.Ledger.Cmp(chk.Totals.Books) != 0 || chk.Totals.CashNCL.Cmp(chk.Totals.Allocated) != 0 {
		t.Fatalf("totals %+v", chk.Totals)
	}
	_, b, err := o.ExchangeFile(ctx, T, "priya.nair", "segmon-client-funds", D, XFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ls := segmonLines(t, b)
	cm, mtf := ls[u+"|CM|NON MTF"], ls[u+"|CM|MTF"]
	if cm == nil || mtf == nil {
		t.Fatalf("client rows missing")
	}
	epi := px.MulInt(10).Round(money.UnitPaisa).Dec().Fixed(2)
	if cm[19] != epi || cm[62] != "1234.00" {
		t.Fatalf("epi %s want %s, settlement %s", cm[19], epi, cm[62])
	}
	bals, _ := o.segmonBalances(ctx, T, D)
	cl := bals[ledger.ClientAccount(u)]
	mc := money.Max(money.Min(money.MustDec("2500"), cl.B.Add(money.MustDec("2500"))), money.Dec{})
	if cm[64] != mc.Fixed(2) || cm[9] != cl.A.Add(mc).Fixed(2) || cm[11] != cl.Peak.Add(mc).Fixed(2) {
		t.Fatalf("mtf cash %s ledger %s peak %s (books %s, cash %s)", cm[64], cm[9], cm[11], cl.A.Fixed(2), mc.Fixed(2))
	}
	if mtf[9] != money.MustDec("-7500").Sub(mc).Fixed(2) || mtf[64] != "0.00" {
		t.Fatalf("mtf row %s %s", mtf[9], mtf[64])
	}
	// Cash placed with NSE Clearing follows the allocation; retained + placed = received.
	al, _ := o.Allocation(ctx, T, D)
	for _, a := range al {
		r := ls[a.UCC+"|"+a.Segment+"|"+map[bool]string{true: "NON MTF", false: "NA"}[a.Segment == "CM"]]
		if a.Segment == "COM" || r == nil {
			continue
		}
		if r[45] != a.Cash.Fixed(2) || r[29] != r[45] {
			t.Fatalf("%s %s cash ncl %s cm %s want %s", a.UCC, a.Segment, r[45], r[29], a.Cash.Fixed(2))
		}
	}

	// A bad PAN stops the file and is named, masked.
	c := cs[u]
	c.PAN = "ABCDE12345"
	if err := docs.Upsert(ctx, o.Docs, T, cClient, u, c, "test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.ExchangeFile(ctx, T, "priya.nair", "segmon-client-funds", D, XFileOptions{}); err == nil || !strings.Contains(err.Error(), "not made") {
		t.Fatalf("file made with a bad PAN: %v", err)
	}
	chk, _ = o.SEGMONCheck(ctx, T, "priya.nair", D)
	found := false
	for _, e := range chk.Errors {
		if e.UCC == u && e.Column == 6 && strings.Contains(e.Message, "ABCDEXXXX5") && !strings.Contains(e.Message, "ABCDE12345") {
			found = true
		}
	}
	if chk.OK || !found {
		t.Fatalf("check %+v", chk.Errors)
	}
	if _, err := o.SEGMONCheck(ctx, T, "meera.iyer", D); err == nil {
		t.Fatal("branch staff ran the check")
	}
}

func TestSEGMONResponseFile(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.ClientMap(ctx, T)
	us := sortedKeys(cs)
	u1, u2 := us[0], us[1]
	// With a header row.
	f := "UCC Code,Segment Indicator,MTF /Non MTF indicator/Reason Code,Error Code,Error Description\n" +
		u1 + ",CM,NON MTF,E012,Client PAN ABCDE1234F does not match the UCC database\n" +
		u2 + ",FO,NA,,Success\n"
	r, err := o.SEGMONResponseFile(ctx, T, "priya.nair", "", "AAACB1234C_16102026_01_RES.csv", []byte(f))
	if err != nil {
		t.Fatal(err)
	}
	if r.Date != D || r.Lines != 2 || r.Accepted != 1 || r.Rejected != 1 || !r.Unconfirmed {
		t.Fatalf("%+v", r)
	}
	x := r.Rejections[0]
	if x.UCC != u1 || x.Segment != "CM" || x.Code != "E012" || strings.Contains(x.Plain, "ABCDE1234F") || !strings.Contains(x.Plain, "ABCDEXXXXF") {
		t.Fatalf("%+v", x)
	}
	ws, _ := o.WorkItems(ctx, T, "Open")
	found := false
	for _, w := range ws {
		found = found || (w.Process == "segmon" && w.UCC == u1 && strings.Contains(w.Detail, "does not match"))
	}
	if !found {
		t.Fatal("no work item for the rejected line")
	}
	// Without a header: the 65 uploaded fields and the error after them.
	line := make([]string, 67)
	line[7], line[8], line[51], line[65] = "FO", u2, "NA", "1107"
	r, err = o.SEGMONResponseFile(ctx, T, "priya.nair", D, "resp.txt", []byte(strings.Join(line, ",")+"\n"))
	if err != nil || r.Rejected != 1 || r.Rejections[0].UCC != u2 || !strings.Contains(r.Rejections[0].Plain, "error code 1107") {
		t.Fatalf("%+v %v", r, err)
	}
	if ls, _ := o.SEGMONResponses(ctx, T, "priya.nair", D); len(ls) != 2 {
		t.Fatalf("responses %d", len(ls))
	}
}
