package ops

import (
	"encoding/json"
	"errors"
	"testing"
)

// HDFC e-CMS pushes: accepted only from the bank's addresses, debits and
// other accounts acknowledged and skipped, duplicates answered "Duplicate"
// by Alert Sequence No, every reply a string-only JSON object.
func TestECMSPush(t *testing.T) {
	o, ctx := creditSetup(t)
	if _, err := o.SaveConnection(ctx, T, "admin", connCreditsECMS, map[string]string{"va_prefix": "BALJ"}, nil); err == nil {
		t.Fatal("saved without the account number")
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connCreditsECMS, map[string]string{"accounts": "00321234560198", "addresses": "not-an-ip"}, nil); err == nil {
		t.Fatal("saved a bad address")
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connCreditsECMS, map[string]string{"accounts": "00321234560198", "va_prefix": "BALJ"}, nil); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"GenericCorporateAlertRequest":[{"Alert Sequence No":"000303100109572403173","Virtual Account":"BALJBC01","Account number":"00321234560198","Debit Credit":"Credit","Amount":"5612.22",` +
		`"Remitter Name":"ASHA RAO","Remitter Account":"50100011112222","Remitter Bank":"HDFC Bank","Remitter IFSC":"HDFC0001234","Cheque No":"","User Reference Number":"FT124542","Mnemonic Code":"NEFT",` +
		`"Value Date":"2026-10-29","Transaction Description":"NEFT","Transaction Date":"2026-10-29 08:35"}]}`)
	reply := func(res BankCreditResult) map[string]string {
		t.Helper()
		var m map[string]map[string]string
		if err := json.Unmarshal([]byte(res.Ack), &m); err != nil {
			t.Fatalf("reply %q: %v", res.Ack, err)
		}
		return m["GenericCorporateAlertResponse"]
	}
	// From an address not on the list: refused.
	if _, err := o.BankCreditHookFrom(ctx, T, "ecms", "198.51.100.7", nil, body); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stranger accepted: %v", err)
	}
	res, err := o.BankCreditHookFrom(ctx, T, "ecms", "175.100.161.31", nil, body)
	if err != nil || res.Added != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if r := reply(res); r["errorCode"] != "0" || r["errorMessage"] != "Success" || r["domainReferenceNo"] != "000303100109572403173" {
		t.Fatalf("reply %v", r)
	}
	if c := credit(t, o, ctx, "FT124542"); c.Status != "Matched" || c.UCC != "BC01" || c.Amount.String() != "5612.22" || c.Time != "08:35:00" || c.Source != "HDFC Bank e-CMS (live)" {
		t.Fatalf("credit %+v", c)
	}
	// The bank pushes it again: Duplicate, nothing new.
	res, err = o.BankCreditHookFrom(ctx, T, "ecms", "175.100.161.31", nil, body)
	if err != nil || res.Added != 0 || reply(res)["errorMessage"] != "Duplicate" {
		t.Fatalf("again %+v %v", res, err)
	}
	// A debit and a credit to another account: acknowledged, not recorded.
	other := []byte(`{"GenericCorporateAlertRequest":[{"Alert Sequence No":"9001","Account number":"00321234560198","Debit Credit":"Debit","Amount":"10","User Reference Number":"D1","Transaction Date":"2026-10-29 09:00"},` +
		`{"Alert Sequence No":"9002","Account number":"99999999","Debit Credit":"Credit","Amount":"10","User Reference Number":"C2","Transaction Date":"2026-10-29 09:01"}]}`)
	res, err = o.BankCreditHookFrom(ctx, T, "ecms", "103.120.107.43", nil, other)
	if err != nil || res.Added != 0 || res.Ignored != 2 || reply(res)["errorMessage"] != "Success" || reply(res)["domainReferenceNo"] != "9001" {
		t.Fatalf("other %+v %v", res, err)
	}
	// Garbage: Technical Reject, so the bank pushes again.
	res, err = o.BankCreditHookFrom(ctx, T, "ecms", "103.120.107.43", nil, []byte(`{"x":1}`))
	if err == nil || reply(res)["errorCode"] != "1" || reply(res)["errorMessage"] != "Technical Reject" {
		t.Fatalf("garbage %+v %v", res, err)
	}
}
