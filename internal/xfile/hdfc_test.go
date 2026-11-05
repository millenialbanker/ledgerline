package xfile

import (
	"strings"
	"testing"
)

// The HDFC ENet bulk payout layout: 28 comma-separated fields, no header,
// CRLF. A made-up file (synthetic clients and accounts) must be recognised,
// read with no issues and written back byte for byte.
func TestHDFCENetPayoutRoundTrip(t *testing.T) {
	l := lib(t)
	s, ok := l.Get("hdfc-enet-payout")
	if !ok {
		t.Fatal("hdfc-enet-payout missing")
	}
	if !s.Confirmed || len(s.Columns) != 28 || s.Header || !s.CRLF || s.Delimiter != "," {
		t.Fatalf("spec: confirmed=%v columns=%d header=%v crlf=%v", s.Confirmed, len(s.Columns), s.Header, s.CRLF)
	}
	want := []string{"Transaction Type", "Beneficiary Code", "Beneficiary Account Number", "Instrument Amount", "Beneficiary Name",
		"Drawee Location", "Print Location", "Beneficiary Address 1", "Beneficiary Address 2", "Beneficiary Address 3", "Beneficiary Address 4", "Beneficiary Address 5",
		"Instruction Reference Number", "Customer Reference Number", "Payment Details 1", "Payment Details 2", "Payment Details 3", "Payment Details 4",
		"Payment Details 5", "Payment Details 6", "Payment Details 7", "Cheque Number", "Cheque/Transaction Date", "MICR Number", "IFSC Code",
		"Beneficiary Bank Name", "Beneficiary Bank Branch Name", "Beneficiary Email ID"}
	for i, c := range s.Columns {
		if c.Name != want[i] {
			t.Errorf("field %d is %q, want %q", i+1, c.Name, want[i])
		}
	}
	line := func(typ, code, acct, amt, name, ref, cref, date, ifsc, bank, branch, email string) string {
		f := []string{typ, code, acct, amt, name, "", "", "", "", "", "", "", ref, cref, "", "", "", "", "", "", "", "", date, "", ifsc, bank, branch, email}
		return strings.Join(f, ",")
	}
	data := line("N", "ZX01", "123456789012", "15000.00", "TEST CLIENT ONE", "90000000000001", "ZX01-PAY", "04/11/2026", "SBIN0000001", "STATE BANK OF INDIA", "TEST BRANCH", "one@example.invalid") + "\r\n" +
		line("R", "ZX02", "987654321098", "250000.50", "TEST CLIENT TWO", "90000000000002", "ZX02-PAY", "04/11/2026", "ICIC0000002", "ICICI BANK", "", "two@example.invalid") + "\r\n" +
		line("I", "ZX03", "50100000000003", "1.00", "TEST CLIENT THREE", "90000000000003", "ZX03-PAY", "04/11/2026", "HDFC0000003", "HDFC BANK", "", "three@example.invalid") + "\r\n"
	name := "TESTCORP010411.817"
	got, err := l.Recognise(name, []byte(data))
	if err != nil || got.ID != "hdfc-enet-payout" {
		t.Fatalf("recognised as %v: %v", got, err)
	}
	tb, err := s.Read(name, []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.Issues) != 0 || len(tb.Rows) != 3 {
		t.Fatalf("rows %d issues %+v", len(tb.Rows), tb.Issues)
	}
	if r := tb.Rows[1]; r.Get("txn_type") != "R" || r.Get("amount") != "250000.50" || r.Get("ifsc") != "ICIC0000002" || r.Get("email") != "two@example.invalid" {
		t.Fatalf("row 2: %v", r.Values)
	}
	if same, diff, err := s.RoundTrip(name, []byte(data)); err != nil || !same {
		t.Fatalf("round trip: %s %v", diff, err)
	}
	fn, err := s.FileName(Vars{"hdfc_client_id": "TESTCORP01", "date": "2026-11-04", "seq": "817"})
	if err != nil || fn != "TESTCORP010411.817" || !s.MatchesName(fn) {
		t.Fatalf("file name %q %v", fn, err)
	}
}
