package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
)

func TestDP(t *testing.T) {
	o, ctx := setup(t)
	hs, _ := o.Holdings(ctx, T, "BH1001")
	var h Holding
	for _, x := range hs {
		if x.Qty-x.Pledged >= 10 {
			h = x
			break
		}
	}
	if h.ISIN == "" {
		t.Fatal("no free holding")
	}
	book, err := o.IssueDISBook(ctx, T, "priya.nair", "BH1001", 20)
	if err != nil || book.To-book.From != 19 {
		t.Fatalf("book: %+v %v", book, err)
	}
	dis := DIS{UCC: "BH1001", Slip: book.From, Kind: "Off-market", ToBO: "1208160099999999", Reason: "Gift", ExecDate: D, Received: D, Lines: []DISLine{{ISIN: h.ISIN, Qty: 10}}}
	bad := dis
	bad.Slip = book.To + 5
	if _, err := o.EnterDIS(ctx, T, "priya.nair", bad); err == nil || !strings.Contains(err.Error(), "not issued") {
		t.Fatalf("foreign slip: %v", err)
	}
	bad = dis
	bad.Lines = []DISLine{{ISIN: h.ISIN, Qty: h.Qty + 1}}
	if _, err := o.EnterDIS(ctx, T, "priya.nair", bad); err == nil || !strings.Contains(err.Error(), "free") {
		t.Fatalf("over free balance: %v", err)
	}
	d, err := o.EnterDIS(ctx, T, "priya.nair", dis)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.EnterDIS(ctx, T, "priya.nair", dis); err == nil || !strings.Contains(err.Error(), "already been used") {
		t.Fatalf("slip reuse: %v", err)
	}
	if _, err := o.VerifyDIS(ctx, T, "priya.nair", d.ID, true, "", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker verified: %v", err)
	}
	if d.Value.Cmp(HighValueDIS) > 0 {
		if _, err := o.VerifyDIS(ctx, T, "farhan.khan", d.ID, true, "", ""); err == nil {
			t.Fatal("high value without confirmation")
		}
	}
	if d, err = o.VerifyDIS(ctx, T, "farhan.khan", d.ID, true, "", "Call ref C-1"); err != nil || d.Status != "Verified" {
		t.Fatalf("verify: %+v %v", d, err)
	}
	f, n, err := o.DISBatch(ctx, T, "priya.nair", D)
	if err != nil || n != 1 || !strings.Contains(string(f), h.ISIN) {
		t.Fatalf("batch: %d %v", n, err)
	}
	if _, err := o.ExecuteDIS(ctx, T, "priya.nair", d.ID, true, "", D); err != nil {
		t.Fatal(err)
	}
	h2, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID("BH1001", h.ISIN))
	if h2.Qty != h.Qty-10 {
		t.Fatalf("holding %d -> %d", h.Qty, h2.Qty)
	}
	// Billing for the month posts a balanced entry and an invoice.
	bills, err := o.RunDPBilling(ctx, T, "farhan.khan", D[:7])
	if err != nil || len(bills) == 0 {
		t.Fatalf("billing: %d %v", len(bills), err)
	}
	var mine *DPBill
	for i := range bills {
		if bills[i].UCC == "BH1001" {
			mine = &bills[i]
		}
	}
	if mine == nil || len(mine.Lines) < 3 || mine.Invoice == "" || mine.GST == 0 {
		t.Fatalf("bill: %+v", mine)
	}
	if again, _ := o.RunDPBilling(ctx, T, "farhan.khan", D[:7]); len(again) != 0 {
		t.Fatal("billed twice")
	}
	tb, _ := o.App.Ledger.TrialBalance(ctx, T, D)
	if !tb.Balanced {
		t.Fatal("trial balance")
	}
	// Freeze blocks debits; transmission and closure.
	fr, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Freeze", UCC: "BH1002", Detail: map[string]string{"type": "Debit", "reason": "Client request"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.ActOnDPRequest(ctx, T, "farhan.khan", fr.ID, "approve", ""); err != nil {
		t.Fatal(err)
	}
	b2, _ := o.IssueDISBook(ctx, T, "priya.nair", "BH1003", 5)
	_ = b2
	if _, err := o.IssueDISBook(ctx, T, "priya.nair", "BH1002", 5); err == nil {
		t.Fatal("book issued to frozen account")
	}
	tr, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Transmission", UCC: "BH1004", Detail: map[string]string{"to_ucc": "BH1005", "death_certificate": "DC/2026/123"}})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := o.Holdings(ctx, T, "BH1004")
	pledged := false
	for _, x := range before {
		pledged = pledged || x.Pledged > 0
	}
	_, err = o.ActOnDPRequest(ctx, T, "farhan.khan", tr.ID, "approve", "")
	if pledged {
		if err == nil {
			t.Fatal("transmission with pledged shares")
		}
	} else if err != nil {
		t.Fatal(err)
	}
	cl, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Closure", UCC: "BH1006"})
	if err == nil {
		t.Fatalf("closure with holdings accepted: %+v", cl)
	}
	// Demat request credits after the RTA confirms.
	dm, err := o.RaiseDPRequest(ctx, T, "priya.nair", DPRequest{Kind: "Demat", UCC: "BH1003", Detail: map[string]string{"drf": "DRF-77", "isin": "INE999A01015", "symbol": "OLDCO", "qty": "100", "certificates": "2"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.ActOnDPRequest(ctx, T, "farhan.khan", dm.ID, "approve", ""); err == nil {
		t.Fatal("demat confirmed before RTA")
	}
	o.ActOnDPRequest(ctx, T, "priya.nair", dm.ID, "send", "")
	if _, err := o.ActOnDPRequest(ctx, T, "farhan.khan", dm.ID, "approve", ""); err != nil {
		t.Fatal(err)
	}
	if h, _, _ := docs.Get[Holding](ctx, o.Docs, T, cHolding, holdingID("BH1003", "INE999A01015")); h.Qty != 100 {
		t.Fatalf("demat credit: %+v", h)
	}
	_ = ledger.ValidDate
}
