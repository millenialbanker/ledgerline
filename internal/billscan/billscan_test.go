package billscan

import (
	"errors"
	"os"
	"strings"
	"testing"

	"ledgerline/internal/money"
)

// The test bills are made up: Kavya Office Supplies does not exist and
// the GSTINs were made for the tests (valid check digits, no real firm).
const (
	vendorGSTIN = "27AABCK1234M1ZK"
	firmGSTIN   = "27AAACL9999Q1Z8"
)

func TestValidGSTIN(t *testing.T) {
	for g, want := range map[string]bool{
		vendorGSTIN:       true,
		firmGSTIN:         true,
		"27AABCK1234M1ZA": false, // wrong check digit
		"99AABCK1234M1ZK": false, // no such state
		"27AABCK1234M1Z":  false,
		"":                false,
	} {
		if got := ValidGSTIN(g); got != want {
			t.Errorf("ValidGSTIN(%q) = %v", g, got)
		}
	}
	if GSTINPAN(vendorGSTIN) != "AABCK1234M" || GSTINState(vendorGSTIN) != "Maharashtra" {
		t.Error("PAN or state from GSTIN")
	}
}

func TestReadPDFBills(t *testing.T) {
	for _, n := range []string{"reportlab", "libreoffice", "chromium"} {
		t.Run(n, func(t *testing.T) {
			b, err := os.ReadFile("testdata/" + n + ".pdf")
			if err != nil {
				t.Fatal(err)
			}
			text, err := ExtractText(b)
			if err != nil {
				t.Fatal(err)
			}
			f := Parse(text, firmGSTIN)
			want := map[string]string{
				"vendor_name": "Kavya Office Supplies Pvt Ltd", "vendor_gstin": vendorGSTIN, "vendor_pan": "AABCK1234M",
				"vendor_state": "Maharashtra", "buyer_gstin": firmGSTIN, "bank_name": "HDFC Bank", "bank_ifsc": "HDFC0000123",
				"bank_account": "50200012345678", "bill_no": "KOS/24-25/0187", "bill_date": "2026-10-14",
			}
			got := map[string]string{
				"vendor_name": f.VendorName, "vendor_gstin": f.VendorGSTIN, "vendor_pan": f.VendorPAN, "vendor_state": f.VendorState,
				"buyer_gstin": f.BuyerGSTIN, "bank_name": f.BankName, "bank_ifsc": f.BankIFSC, "bank_account": f.BankAccount,
				"bill_no": f.BillNo, "bill_date": f.BillDate,
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
			if !strings.HasPrefix(f.VendorAddress, "Unit 4, Sunrise Industrial Estate, Andheri East, Mumbai 400093") {
				t.Errorf("address %q", f.VendorAddress)
			}
			if f.Taxable != money.MustAmount("20000") || f.CGST != money.MustAmount("1800") || f.SGST != money.MustAmount("1800") ||
				f.IGST != 0 || f.Total != money.MustAmount("23600") {
				t.Errorf("amounts %s %s %s %s %s", f.Taxable, f.CGST, f.SGST, f.IGST, f.Total)
			}
			if len(f.Warnings) != 0 {
				t.Errorf("warnings %v", f.Warnings)
			}
		})
	}
	b, _ := os.ReadFile("testdata/reportlab.pdf")
	text, _ := ExtractText(b)
	if f := Parse(text, firmGSTIN); f.DueDate != "2026-11-13" {
		t.Errorf("due date %q", f.DueDate)
	}
}

func TestParseTextVariants(t *testing.T) {
	text := `SHREE DATA CENTRE SERVICES LLP
14 Ring Road, Surat 395002
GSTIN : 24AABFS4321K1Z` + gstCheck("24AABFS4321K1Z") + `
Tax Invoice
Bill No. SDC-0091         Dated 05 Sep 2026
Billed to GSTIN 27AABCK1234M1ZA
Colocation charges September          45,000.00
Sub Total                              45,000.00
IGST @ 18%                              8,100.00
Total Amount Rs. 53,100.00
`
	f := Parse(text, firmGSTIN)
	if f.VendorName != "SHREE DATA CENTRE SERVICES LLP" || f.VendorState != "Gujarat" || f.BillNo != "SDC-0091" || f.BillDate != "2026-09-05" {
		t.Errorf("%+v", f)
	}
	if f.Taxable != money.MustAmount("45000") || f.IGST != money.MustAmount("8100") || f.Total != money.MustAmount("53100") {
		t.Errorf("amounts %s %s %s", f.Taxable, f.IGST, f.Total)
	}
	joined := strings.Join(f.Warnings, " | ")
	if !strings.Contains(joined, "check digit") || !strings.Contains(joined, "firm's GSTIN is not on the bill") {
		t.Errorf("warnings %v", f.Warnings)
	}

	// Total and tax only: the taxable value is worked out, and a total that
	// does not add up is flagged.
	f = Parse("Acme Traders\nInvoice No: 7\nCGST 90.00\nSGST 90.00\nGrand Total 1,180.00", "")
	if f.Taxable != money.MustAmount("1000") {
		t.Errorf("taxable %s", f.Taxable)
	}
	f = Parse("Acme Traders\nTaxable Value 1,000.00\nCGST 90.00\nSGST 90.00\nGrand Total 1,500.00", "")
	if !strings.Contains(strings.Join(f.Warnings, " "), "does not make the total") {
		t.Errorf("warnings %v", f.Warnings)
	}
}

func gstCheck(b string) string {
	s := 0
	for i := 0; i < 14; i++ {
		p := strings.IndexByte(gstChars, b[i]) * (1 + i%2)
		s += p/36 + p%36
	}
	return string(gstChars[(36-s%36)%36])
}

func TestNotText(t *testing.T) {
	if _, err := ExtractText([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}); !errors.Is(err, ErrNoText) {
		t.Errorf("jpeg: %v", err)
	}
	if _, err := ExtractText([]byte("%PDF-1.4\n1 0 obj << /Type /Page >> endobj\n%%EOF")); !errors.Is(err, ErrNoText) {
		t.Errorf("empty pdf: %v", err)
	}
}

func TestParseAI(t *testing.T) {
	reply := "```json\n" + `{"vendor_name":"Kavya Office Supplies Pvt Ltd","vendor_gstin":"` + firmGSTIN + `","buyer_gstin":"` + vendorGSTIN + `",
"bank_ifsc":"hdfc0000123","bank_account":"5020 0012 345678","bill_no":"KOS/1","bill_date":"14/10/2026","taxable":20000,"cgst":"1,800.00","sgst":1800,"igst":0,"total":23600}` + "\n```"
	f, err := ParseAI(reply, firmGSTIN)
	if err != nil {
		t.Fatal(err)
	}
	// The model mixed up vendor and buyer: the firm's own GSTIN is swapped out.
	if f.VendorGSTIN != vendorGSTIN || f.BuyerGSTIN != firmGSTIN || f.VendorPAN != "AABCK1234M" {
		t.Errorf("gstin %s buyer %s pan %s", f.VendorGSTIN, f.BuyerGSTIN, f.VendorPAN)
	}
	if f.BankIFSC != "HDFC0000123" || f.BankAccount != "50200012345678" || f.BillDate != "2026-10-14" || f.CGST != money.MustAmount("1800") || f.Source != "ai" {
		t.Errorf("%+v", f)
	}
	if len(f.Warnings) != 0 {
		t.Errorf("warnings %v", f.Warnings)
	}
	if _, err := ParseAI("I cannot read this bill.", ""); err == nil {
		t.Error("non-JSON reply accepted")
	}
}
