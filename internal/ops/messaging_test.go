package ops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

func TestMessagingAndBot(t *testing.T) {
	o, ctx := setup(t)
	c, _ := o.Client(ctx, T, "BH1003")
	r, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: D, UCC: "BH1003", Mode: "NEFT", Ref: "N1234", FromAccount: c.Bank.Account, Amount: 2500000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideReceipt(ctx, T, "farhan.khan", r.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	ms, _ := o.Messages(ctx, T, "BH1003", 0)
	got := map[string]string{}
	for _, m := range ms {
		if m.Event == "receipt.credited" {
			got[m.Channel] = m.Status
			if m.Channel == ChSMS && (!strings.Contains(m.Body, "25,000.00") || m.DLT == "") {
				t.Fatalf("sms: %+v", m)
			}
		}
	}
	if got[ChSMS] != "Sent" || got[ChWhatsApp] != "Not opted in" {
		t.Fatalf("receipt messages: %v", got)
	}

	// Account information needs a code sent to the registered email.
	o.DevCodes = true
	reply, err := o.WhatsAppInbound(ctx, T, "+91"+c.Mobile, "bal", D)
	if err != nil || !strings.Contains(reply, "6-digit code") || strings.Contains(reply, "Available to withdraw") {
		t.Fatalf("bal before code: %q %v", reply, err)
	}
	code := reply[strings.Index(reply, "the code is ")+12:][:6]
	if r2, _ := o.WhatsAppInbound(ctx, T, c.Mobile, "000000", D); !strings.Contains(r2, "not right") && code != "000000" {
		t.Fatalf("wrong code: %q", r2)
	}
	// The right code answers the request that was held back.
	reply, err = o.WhatsAppInbound(ctx, T, c.Mobile, code, D)
	if err != nil || !strings.Contains(reply, "Available to withdraw") || !strings.Contains(reply, "BH1003") {
		t.Fatalf("bal: %q %v", reply, err)
	}
	if c, _ = o.Client(ctx, T, "BH1003"); c.WhatsApp {
		t.Fatal("opted in just by messaging")
	}
	o.WhatsAppInbound(ctx, T, c.Mobile, "YES", D)
	if c, _ = o.Client(ctx, T, "BH1003"); !c.WhatsApp {
		t.Fatal("YES did not opt in")
	}
	for _, cmd := range []string{"HOLD", "LEDGER", "CN", "MARGIN", "what?"} {
		if rep, err := o.WhatsAppInbound(ctx, T, c.Mobile, cmd, D); err != nil || rep == "" {
			t.Fatalf("%s: %q %v", cmd, rep, err)
		}
	}
	if rep, _ := o.WhatsAppInbound(ctx, T, "9000000001", "BAL", D); !strings.Contains(rep, "not registered") {
		t.Fatalf("stranger: %q", rep)
	}
	o.WhatsAppInbound(ctx, T, c.Mobile, "STOP", D)
	if c, _ = o.Client(ctx, T, "BH1003"); c.WhatsApp {
		t.Fatal("STOP did not opt out")
	}
	// Signed webhook.
	body := []byte(`{"from":"` + c.Mobile + `","text":"MENU"}`)
	if _, err := o.WhatsAppWebhook(ctx, T, body, "deadbeef"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bad signature accepted: %v", err)
	}
	mac := hmac.New(sha256.New, []byte("dev-whatsapp-secret"))
	mac.Write(body)
	if rep, err := o.WhatsAppWebhook(ctx, T, body, hex.EncodeToString(mac.Sum(nil))); err != nil || !strings.Contains(rep, "BAL") {
		t.Fatalf("webhook: %q %v", rep, err)
	}

	// Template rules.
	if _, err := o.SaveTemplate(ctx, T, "farhan.khan", Template{Event: "payout.paid", Channel: ChSMS, Category: "Transactional", Body: "Paid {amount}"}); err == nil || !strings.Contains(err.Error(), "DLT") {
		t.Fatalf("SMS without DLT: %v", err)
	}
	if _, err := o.SaveTemplate(ctx, T, "farhan.khan", Template{Event: "payout.paid", Channel: ChEmail, Category: "Transactional", Subject: "x", Body: "Paid {shortfall}"}); err == nil || !strings.Contains(err.Error(), "{shortfall}") {
		t.Fatalf("wrong placeholder: %v", err)
	}
	tp, err := o.SaveTemplate(ctx, T, "farhan.khan", Template{Event: "bulk", Channel: ChEmail, Category: "Service", Subject: "Holiday notice", Body: "Dear {name}, markets are closed on 25-Dec. {firm}", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := o.BulkSend(ctx, T, "farhan.khan", tp.ID, "all", nil, nil)
	if err != nil || res["Sent"] == 0 {
		t.Fatalf("bulk: %v %v", res, err)
	}
	if _, err := o.BulkSend(ctx, T, "priya.nair", tp.ID, "all", nil, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker bulk send: %v", err)
	}
}

func TestScheduler(t *testing.T) {
	o, ctx := setup(t)
	js, _, err := o.Jobs(ctx, T)
	if err != nil || len(js) < 8 || js[0].Active {
		t.Fatalf("seeded jobs: %d %v", len(js), err)
	}
	if _, err := o.SaveJob(ctx, T, "farhan.khan", Job{Name: "x", Kind: "send_notes", At: "10:00", When: "business"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("checker scheduled a job: %v", err)
	}
	if _, err := o.SaveJob(ctx, T, "admin", Job{Name: "x", Kind: "email_report", At: "10:00", When: "business"}); err == nil {
		t.Fatal("email_report without params")
	}
	if _, err := o.SaveJob(ctx, T, "admin", Job{Name: "x", Kind: "send_notes", At: "25:00", When: "business"}); err == nil {
		t.Fatal("bad time")
	}
	notes := js[0]
	notes.Active = true
	if _, err := o.SaveJob(ctx, T, "admin", notes); err != nil {
		t.Fatal(err)
	}
	// Bill the day so that there is something to send.
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBilling(ctx, T, "priya.nair", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	run, err := o.RunJob(ctx, T, "farhan.khan", notes.ID, D)
	if err != nil || run.Status != "Succeeded" || !strings.Contains(run.Summary, "notes generated") || run.Notified == "" {
		t.Fatalf("run: %+v %v", run, err)
	}
	ob, _ := docs.List[OutboxItem](ctx, o.Docs, T, cOutbox, "")
	found := false
	for _, x := range ob {
		found = found || (x.To == "md@bhairav.example" && strings.Contains(x.Subject, "succeeded"))
	}
	if !found {
		t.Fatal("management not told")
	}
	// A failing job is recorded as failed.
	bad, err := o.SaveJob(ctx, T, "admin", Job{Name: "Bad report", Kind: "email_report", At: "06:00", When: "daily", Active: true, Params: map[string]string{"report": "no_such_report", "to": "x@y.in"}})
	if err != nil {
		t.Fatal(err)
	}
	run, err = o.RunJob(ctx, T, "admin", bad.ID, D)
	if err == nil || run.Status != "Failed" {
		t.Fatalf("bad job: %+v %v", run, err)
	}
	// Tick runs due jobs once a day: Monday 19-Oct-2026 at 20:00 IST.
	mon := time.Date(2026, 10, 19, 20, 0, 0, 0, ist)
	n, err := o.Tick(context.Background(), T, mon)
	if err != nil || n != 2 {
		t.Fatalf("tick ran %d: %v", n, err)
	}
	if n, _ := o.Tick(ctx, T, mon.Add(time.Minute)); n != 0 {
		t.Fatalf("ran twice: %d", n)
	}
	sat := time.Date(2026, 10, 24, 20, 0, 0, 0, ist)
	if n, _ := o.Tick(ctx, T, sat); n != 1 { // only the daily job on a Saturday
		t.Fatalf("saturday ran %d", n)
	}
}
