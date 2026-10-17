package ops

import (
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

func TestReceiptsGatewayAndPayouts(t *testing.T) {
	o, ctx := newOps(t)
	c := Client{UCC: "PY001", Name: "Pay Client", Status: "Active", Tags: []string{}, Bank: BankAccount{Bank: "HDFC Bank", IFSC: "HDFC0001234", Account: "50100011112222"}}
	if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "test"); err != nil {
		t.Fatal(err)
	}
	const d = "2026-10-16"
	bal := func() money.Amount {
		f, _ := o.clientFunds(ctx, T, d, "")
		return f["PY001"]
	}
	// Third-party account refused; cash not accepted.
	if _, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: d, UCC: "PY001", Mode: "Cheque", Ref: "000111", FromAccount: "99999999999", Amount: 100000}); err == nil || !strings.Contains(err.Error(), "third-party") {
		t.Fatalf("third-party accepted: %v", err)
	}
	if _, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: d, UCC: "PY001", Mode: "Cash", Ref: "x", Amount: 100000}); err == nil {
		t.Fatal("cash accepted")
	}
	chq, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: d, UCC: "PY001", Mode: "Cheque", Ref: "000112", DraweeBank: "HDFC Bank", Amount: 5000000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideReceipt(ctx, T, "priya.nair", chq.ID, true, ""); err == nil {
		t.Fatal("maker approved own receipt")
	}
	if chq, err = o.DecideReceipt(ctx, T, "farhan.khan", chq.ID, true, ""); err != nil || chq.Status != "Uncleared" {
		t.Fatalf("approve: %v %+v", err, chq)
	}
	if bal() != 5000000 {
		t.Fatalf("balance after cheque = %s", bal())
	}
	// Uncleared money cannot be withdrawn.
	w, _ := o.Withdrawable(ctx, T, d, "PY001")
	if w.Available != 0 || w.Uncleared != 5000000 {
		t.Fatalf("withdrawable with uncleared cheque = %+v", w)
	}
	// Bounce with charges: credit reversed, ₹350 + GST charged.
	if _, err := o.ClearCheque(ctx, T, "priya.nair", chq.ID, d, true, true, "Funds insufficient"); err != nil {
		t.Fatal(err)
	}
	if bal().String() != "-413.00" {
		t.Fatalf("balance after bounce = %s", bal())
	}
	// Gateway receipt: signature checked, credited once.
	body := []byte(`{"event":"payment.captured","payload":{"payment":{"entity":{"id":"pay_X1","amount":2500000,"method":"upi","vpa":"py@okbank","notes":{"ucc":"py001","value_date":"2026-10-16"}}}}}`)
	if _, _, err := o.GatewayWebhook(ctx, T, body, "bad"); err == nil {
		t.Fatal("unsigned webhook accepted")
	}
	set, _ := o.Settings(ctx, T)
	sig := SignGateway(set.GatewaySecret, body)
	if _, created, err := o.GatewayWebhook(ctx, T, body, sig); err != nil || !created {
		t.Fatalf("webhook: %v", err)
	}
	if _, created, _ := o.GatewayWebhook(ctx, T, body, sig); created {
		t.Fatal("webhook credited twice")
	}
	if bal().String() != "24587.00" {
		t.Fatalf("balance after gateway = %s", bal())
	}
	if n, gross, err := o.SettleGateway(ctx, T, "farhan.khan", d, "setl_1", 5900); err != nil || n != 1 || gross != 2500000 {
		t.Fatalf("settle: %d %s %v", n, gross, err)
	}
	b, _ := o.App.Ledger.Balances(ctx, T, d)
	if b[ReceivableGateway] != 0 {
		t.Fatalf("gateway receivable left: %s", b[ReceivableGateway])
	}
	// Payout: available is the credit (no margin, no interest yet).
	w, _ = o.Withdrawable(ctx, T, d, "PY001")
	if w.Available.String() != "24587.00" {
		t.Fatalf("withdrawable = %+v", w)
	}
	if _, err := o.RequestPayout(ctx, T, "priya.nair", d, "PY001", 3000000, "Head office"); err == nil {
		t.Fatal("payout above available accepted")
	}
	p, err := o.RequestPayout(ctx, T, "priya.nair", d, "PY001", 2000000, "Head office")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecidePayout(ctx, T, "farhan.khan", d, p.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if w, _ = o.Withdrawable(ctx, T, d, "PY001"); w.Available.String() != "4587.00" {
		t.Fatalf("approved payout not reserved: %+v", w)
	}
	batch, err := o.CreatePayoutBatch(ctx, T, "farhan.khan", d)
	if err != nil || len(batch.Items) != 1 || batch.Items[0].Mode != "NEFT" {
		t.Fatalf("batch: %v %+v", err, batch)
	}
	f, _ := o.PayoutFile(ctx, T, batch.ID)
	if !strings.Contains(string(f), "NEFT,50200012345678,Pay Client,50100011112222,HDFC0001234,20000.00") {
		t.Fatalf("payout file:\n%s", f)
	}
	if bal().String() != "4587.00" {
		t.Fatalf("balance after payout = %s", bal())
	}
	// The bank returns it: reversed to the client.
	if _, err := o.ConfirmPayouts(ctx, T, "priya.nair", batch.ID, nil, map[string]string{p.ID: "Account closed"}); err != nil {
		t.Fatal(err)
	}
	if bal().String() != "24587.00" {
		t.Fatalf("balance after failed payout = %s", bal())
	}
	tb, _ := o.App.Ledger.TrialBalance(ctx, T, d)
	if !tb.Balanced {
		t.Fatal("trial balance")
	}
	_ = ledger.BankClient
}
