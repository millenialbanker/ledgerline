package ops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A fake Cashfree verification API answering as its reference describes.
func fakeCashfree(t *testing.T) (*httptest.Server, *[]string) {
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-client-id") != "CF-ID" || r.Header.Get("x-client-secret") != "CF-SECRET" {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"authentication Failed","code":"request_failed","type":"authentication_error"}`))
			return
		}
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		seen = append(seen, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/bank-account/sync":
			switch b["bank_account"] {
			case "026291800001191":
				w.Write([]byte(`{"reference_id":34,"name_at_bank":"JOHN DOE","bank_name":"YES BANK","city":"MUMBAI","branch":"SANTACRUZ","micr":400532038,"name_match_score":"90.00","name_match_result":"GOOD_PARTIAL_MATCH","account_status":"VALID","account_status_code":"ACCOUNT_IS_VALID"}`))
			case "00011020001772":
				w.Write([]byte(`{"reference_id":35,"account_status":"INVALID","account_status_code":"INVALID_ACCOUNT_FAIL"}`))
			case "1234567890":
				w.Write([]byte(`{"reference_id":36,"name_at_bank":"RAMESH KUMAR","bank_name":"HDFC BANK","name_match_result":"NO_MATCH","account_status":"VALID","account_status_code":"ACCOUNT_IS_VALID"}`))
			default:
				w.WriteHeader(500)
			}
		case "/pan":
			if r.Header.Get("x-api-version") == "" {
				t.Error("PAN call without x-api-version")
			}
			switch b["pan"] {
			case "ABCPV1234D":
				w.Write([]byte(`{"pan":"ABCPV1234D","type":"Individual","reference_id":1,"name_provided":"John Doe","registered_name":"JOHN DOE","valid":true,"message":"PAN verified successfully","name_match_score":"100.00","name_match_result":"DIRECT_MATCH","aadhaar_seeding_status":"Y","pan_status":"VALID","pan_status_desc":"VALID"}`))
			case "ABCPV1234E":
				w.Write([]byte(`{"pan":"ABCPV1234E","valid":true,"registered_name":"JOHN DOE","name_match_result":"DIRECT_MATCH","aadhaar_seeding_status":"R","pan_status":"VALID"}`))
			default:
				w.Write([]byte(`{"pan":"` + b["pan"] + `","valid":false,"message":"Invalid PAN","pan_status":"INVALID","pan_status_desc":"INVALID"}`))
			}
		}
	}))
	old := verifyHTTP
	verifyHTTP = srv.Client()
	t.Cleanup(func() { srv.Close(); verifyHTTP = old })
	return srv, &seen
}

func TestCashfreeVerification(t *testing.T) {
	o, ctx := newOps(t)
	if o.verifier(ctx, T) != nil {
		t.Fatal("a verifier before any connection")
	}
	srv, seen := fakeCashfree(t)
	save := func(secret string) {
		if _, err := o.SaveConnection(ctx, T, "admin", connVerify, map[string]string{"provider": "Cashfree", "client_id": "CF-ID", "mode": verifySandbox, "url": srv.URL}, map[string]string{"api_key": secret}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.SaveConnection(ctx, T, "admin", connVerify, map[string]string{"provider": "Cashfree"}, map[string]string{"api_key": "x"}); err == nil {
		t.Fatal("Cashfree without a client ID")
	}
	save("CF-SECRET")
	v := o.verifier(ctx, T)
	if v == nil || v.Name() != "Cashfree" {
		t.Fatal("no Cashfree verifier")
	}
	c := context.Background()
	for _, tc := range []struct{ acct, name, want, has string }{
		{"026291800001191", "John Doe", "Passed", "name at bank JOHN DOE"},
		{"00011020001772", "John Doe", "Failed", "not found at this bank"},
		{"1234567890", "John Doe", "Check manually", "does not match"},
		{"999", "John Doe", "Check manually", "Could not be checked"},
	} {
		got := v.Bank(c, tc.acct, "YESB0000262", tc.name)
		if got.Status != tc.want || !strings.Contains(got.Detail, tc.has) {
			t.Errorf("bank %s: %s %q", tc.acct, got.Status, got.Detail)
		}
	}
	for _, tc := range []struct{ pan, want, has string }{
		{"ABCPV1234D", "Passed", "Aadhaar linked"},
		{"ABCPV1234E", "Check manually", "not linked to Aadhaar"},
		{"ABCPV9999Z", "Failed", "not valid"},
	} {
		got := v.PAN(c, tc.pan, "John Doe")
		if got.Status != tc.want || !strings.Contains(got.Detail, tc.has) {
			t.Errorf("pan %s: %s %q", tc.pan, got.Status, got.Detail)
		}
	}
	// Refused keys never fail the applicant: a checker looks instead.
	save("WRONG")
	if got := o.verifier(ctx, T).PAN(c, "ABCPV1234D", "John Doe"); got.Status != "Check manually" || !strings.Contains(got.Detail, "refused the keys") {
		t.Fatalf("refused keys: %+v", got)
	}
	if len(*seen) != 7 {
		t.Fatalf("calls %v", *seen)
	}
	// The check is used for the PAN step with the firm's provider.
	save("CF-SECRET")
	if got := o.checkPAN(ctx, T, manualKYC{}, "ABCPV1234D", "John Doe", "1990-01-01"); got.Status != "Passed" || got.Provider != "Cashfree" {
		t.Fatalf("checkPAN %+v", got)
	}
	if got := o.checkBank(ctx, T, nil, "00011020001772", "YESB0000262", "John Doe"); got.Status != "Failed" {
		t.Fatalf("checkBank %+v", got)
	}
}

func TestVerifyNamesMatch(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"JOHN DOE", "Mr. John Doe", true},
		{"R K SHARMA", "Ravi Kumar Sharma", true},
		{"SHARMA RAVI KUMAR", "Ravi Sharma", true},
		{"PRIYA NAIR", "Priya Menon", false},
		{"", "Anyone", false},
	} {
		if got := namesMatch(tc.a, tc.b); got != tc.want {
			t.Errorf("%q vs %q: %v", tc.a, tc.b, got)
		}
	}
}
