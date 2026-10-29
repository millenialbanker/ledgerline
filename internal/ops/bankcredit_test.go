package ops

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

func creditSetup(t *testing.T) (*Ops, context.Context) {
	t.Helper()
	o, ctx := newOps(t)
	for _, c := range []Client{
		{UCC: "BC01", Name: "Asha Rao", Status: "Active", Email: "a@x.example", Bank: BankAccount{Bank: "HDFC Bank", IFSC: "HDFC0001234", Account: "50100011112222"}},
		{UCC: "BC02", Name: "Vikram Shah", Status: "Active", Email: "v@x.example", Bank: BankAccount{Bank: "Axis Bank", IFSC: "UTIB0000123", Account: "91902033334444"}},
	} {
		docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "t")
	}
	return o, ctx
}

func credit(t *testing.T, o *Ops, ctx context.Context, id string) BankTxn {
	t.Helper()
	b, _, err := docs.Get[BankTxn](ctx, o.Docs, T, cBankTxn, id)
	if err != nil {
		t.Fatalf("no credit %s", id)
	}
	return b
}

// Razorpay Smart Collect: a signed virtual_account.credited from the
// client's registered account is a receipt; from any other account it is
// held as third-party funds for the client the virtual account names.
func TestBankCreditRazorpay(t *testing.T) {
	o, ctx := creditSetup(t)
	if _, err := o.BankCreditHook(ctx, T, "razorpay", http.Header{}, []byte(`{}`)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("accepted before set up: %v", err)
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connCreditsRazorpay, map[string]string{"va_prefix": "LLVA"}, map[string]string{"secret": "whsec"}); err != nil {
		t.Fatal(err)
	}
	msg := func(utr, acct string, paise int) []byte {
		return []byte(`{"event":"virtual_account.credited","payload":{"payment":{"entity":{"id":"pay_1","amount":` + strconv.Itoa(paise) + `,"method":"bank_transfer","created_at":1792570000}},` +
			`"virtual_account":{"entity":{"id":"va_1","notes":{},"receivers":[{"account_number":"LLVABC01","ifsc":"RATN0VAAPIS"}]}},` +
			`"bank_transfer":{"entity":{"amount":` + strconv.Itoa(paise) + `,"bank_reference":"` + utr + `","mode":"NEFT","payer_bank_account":{"account_number":"` + acct + `","ifsc":"HDFC0001234","name":"ASHA RAO"}}}}}`)
	}
	h := func(b []byte) http.Header { return http.Header{"X-Razorpay-Signature": {hexHMAC("whsec", b)}} }
	b := msg("UTR0001", "50100011112222", 2500000)
	if _, err := o.BankCreditHook(ctx, T, "razorpay", http.Header{"X-Razorpay-Signature": {"bad"}}, b); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bad signature: %v", err)
	}
	res, err := o.BankCreditHook(ctx, T, "razorpay", h(b), b)
	if err != nil || res.Added != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if c := credit(t, o, ctx, "UTR0001"); c.Status != "Matched" || c.UCC != "BC01" || c.Amount.String() != "25000.00" || c.Mode != "NEFT" || !strings.Contains(c.Source, "live") {
		t.Fatalf("credit %+v", c)
	}
	if res, _ := o.BankCreditHook(ctx, T, "razorpay", h(b), b); res.Duplicate != 1 || res.Added != 0 {
		t.Fatalf("resent %+v", res)
	}
	b = msg("UTR0002", "11112222333344", 100000)
	o.BankCreditHook(ctx, T, "razorpay", h(b), b)
	if c := credit(t, o, ctx, "UTR0002"); c.Status != "Third-party" || c.UCC != "BC01" {
		t.Fatalf("third party %+v", c)
	}
}

// Cashfree virtual accounts: PAYMENT_SUCCESS_WEBHOOK with a VBA transfer,
// signed with the timestamp and body.
func TestBankCreditCashfree(t *testing.T) {
	o, ctx := creditSetup(t)
	o.SaveConnection(ctx, T, "admin", connCreditsCashfree, map[string]string{"va_prefix": "CFX"}, map[string]string{"secret": "cfsecret"})
	b := []byte(`{"type":"PAYMENT_SUCCESS_WEBHOOK","data":{"payment":{"payment_status":"SUCCESS","payment_amount":7500.5,"payment_time":"2026-10-29T11:47:22+05:30",` +
		`"payment_method":{"vba_transfer":{"utr":"CFUTR9","remitter_account":"91902033334444","remitter_ifsc":"UTIB0000123","remitter_name":"VIKRAM SHAH","vaccount_number":"CFXBC02"}}}}}`)
	ts := "1792570000123"
	res, err := o.BankCreditHook(ctx, T, "cashfree", http.Header{"X-Webhook-Timestamp": {ts}, "X-Webhook-Signature": {b64HMAC("cfsecret", []byte(ts), b)}}, b)
	if err != nil || res.Added != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if c := credit(t, o, ctx, "CFUTR9"); c.Status != "Matched" || c.UCC != "BC02" || c.Amount.String() != "7500.50" || c.Date != "2026-10-29" || c.Time != "11:47:22" {
		t.Fatalf("credit %+v", c)
	}
}

// A bank's own format, described in the connection (here as HDFC e-CMS might
// send it): a list of credits, amounts in rupees, a token in a header.
func TestBankCreditMapped(t *testing.T) {
	o, ctx := creditSetup(t)
	vals := map[string]string{"provider": "HDFC Bank e-CMS", "auth": authToken, "header": "X-Api-Token", "va_prefix": "BALJ",
		"path_records": "credits", "path_amount": "amt", "path_utr": "utr", "path_virtual_account": "van", "path_payer_account": "rem.acct",
		"path_payer_ifsc": "rem.ifsc", "path_payer_name": "rem.name", "path_mode": "mode", "path_time": "ts"}
	bad := map[string]string{}
	for k, v := range vals {
		bad[k] = v
	}
	bad["path_amount"] = "amt; DROP"
	if _, err := o.SaveConnection(ctx, T, "admin", connCreditsBank, bad, map[string]string{"secret": "tok"}); err == nil {
		t.Fatal("accepted a bad field path")
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connCreditsBank, vals, map[string]string{"secret": "tok-123"}); err != nil {
		t.Fatal(err)
	}
	b := []byte(`{"credits":[{"amt":"1,20,000.00","utr":"HDFCN26302","van":"BALJBC02","mode":"IMPS","ts":"29-10-2026 22:05:10","rem":{"acct":"91902033334444","ifsc":"UTIB0000123","name":"VIKRAM SHAH"}},` +
		`{"amt":"500","utr":"HDFCN26303","van":"BALJZZ99","mode":"UPI","ts":"29-10-2026 22:06:00","rem":{"acct":"","name":"SOMEONE"}}]}`)
	if _, err := o.BankCreditHook(ctx, T, "bank", http.Header{"X-Api-Token": {"wrong"}}, b); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong token: %v", err)
	}
	res, err := o.BankCreditHook(ctx, T, "bank", http.Header{"X-Api-Token": {"Bearer tok-123"}}, b)
	if err != nil || res.Added != 2 || res.Ack != `{"status":"ok"}` {
		t.Fatalf("%+v %v", res, err)
	}
	if c := credit(t, o, ctx, "HDFCN26302"); c.Status != "Matched" || c.UCC != "BC02" || c.Amount.String() != "120000.00" || c.Source != "HDFC Bank e-CMS (live)" || c.Time != "22:05:10" {
		t.Fatalf("first %+v", c)
	}
	// An unknown virtual account and no payer account: held, unidentified.
	if c := credit(t, o, ctx, "HDFCN26303"); c.Status != "Unidentified" || c.UCC != "" || c.Account != "VPA" {
		t.Fatalf("second %+v", c)
	}
}

func TestCreditWhen(t *testing.T) {
	now := time.Date(2026, 10, 29, 6, 0, 0, 0, time.UTC)
	for in, want := range map[string]string{"": "2026-10-29 11:30:00", "1792570000": time.Unix(1792570000, 0).In(ist).Format("2006-01-02 15:04:05"),
		"2026-10-28T23:59:00Z": "2026-10-29 05:29:00", "2026-10-28 10:00:00": "2026-10-28 10:00:00", "28/10/2026 09:01:02": "2026-10-28 09:01:02"} {
		d, tm := creditWhen(in, now)
		if d+" "+tm != want {
			t.Errorf("%q: %s %s, want %s", in, d, tm, want)
		}
	}
}
