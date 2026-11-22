package ops

import (
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// All data here is made up.

// The exchange's UCC download is kept as fingerprints only, and each
// client's fields are compared with Ledgerline's.
func TestUCCFieldsDownload(t *testing.T) {
	o, ctx := newOps(t)
	siClock(o, siAt("2026-11-20", "23:00"))
	a := nseUCITestClient("TC101", "ABCPV1101K")
	b := nseUCITestClient("TC102", "ABCPV1102K")
	nseUCIAddClient(t, o, a, true)
	nseUCIAddClient(t, o, b, true)
	siWant(t, siOne(t, o, ctx, "ucc_fields", "2026-11-20"), SISkipped, "no download yet")

	// Headers as the exchange might spell them, in another order, with a
	// column Ledgerline does not use; client b's mobile and e-mail differ.
	csv := "UCC Code,Mobile No.,Email Id,PAN No,Bank Account Number,IFSC,Address 1,Pin Code,Segment\n" +
		"TC101,+91 98765 43210,CLIENT.TC101@EXAMPLE.COM,abcpv1101k,0050100012345678,hdfc0001234,12 Test Lane Fort,400001,C\n" +
		"TC101,9876543210,client.tc101@example.com,ABCPV1101K,50100012345678,HDFC0001234,12 Test Lane Fort,400001,F\n" +
		"TC102,9123456789,other.person@example.com,ABCPV1102K,50100012345678,HDFC0001234,12 Test Lane Fort,400001,C\n" +
		"ZZ999,9000000001,nobody@example.com,ABCPV9999K,1,HDFC0001234,x,400001,C\n"
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", "NSE_UCC_DOWNLOAD_20261118.csv", []byte(csv), true)
	if err != nil || len(res) != 1 || res[0].Format != "nse-ucc-download" || res[0].Added != 3 || !strings.Contains(res[0].Loaded, "fingerprints") {
		t.Fatalf("load %+v %v", res, err)
	}
	if !strings.Contains(res[0].Note, "mobile") || !strings.Contains(res[0].Note, "address") {
		t.Fatalf("fields %q", res[0].Note)
	}
	// The file itself is not kept: no mobile, e-mail or PAN of it anywhere.
	rf, err := o.App.Ingest.Raw.Get(ctx, T, res[0].FileID)
	if err != nil || strings.Contains(string(rf.Content), "9123456789") || strings.Contains(string(rf.Content), "other.person") {
		t.Fatalf("raw kept %q %v", rf.Content, err)
	}
	recs, _ := docs.List[UCCDownloadRec](ctx, o.Docs, T, cUCCDl, "")
	for _, r := range recs {
		for _, v := range r.Prints {
			if strings.Contains(v, "9123") || strings.Contains(v, "@") {
				t.Fatalf("a value kept as it was: %+v", r)
			}
		}
		if r.Date != "2026-11-18" {
			t.Fatalf("date %+v", r)
		}
	}

	s := siOne(t, o, ctx, "ucc_fields", "2026-11-20")
	siWant(t, s, SIFail, "client b's mobile and e-mail differ")
	if s.checked != 2 || s.found != 1 || !strings.Contains(s.examples[0], "NSE: mobile differs (exchange …789), e-mail differs (exchange o…@example.com)") || strings.Contains(s.examples[0], "TC102") {
		t.Fatalf("found %+v", s)
	}
	if !strings.Contains(s.summary, "mobile 1, e-mail 1") {
		t.Fatalf("summary %q", s.summary)
	}

	// Ledgerline corrected to what the client gave: the next run passes.
	b.Mobile, b.Email = "9123456789", "other.person@example.com"
	nseUCIAddClient(t, o, b, false)
	siWant(t, siOne(t, o, ctx, "ucc_fields", "2026-11-20"), SIPass, "corrected")

	// A field missing at the exchange is a difference too.
	csv2 := "Client Code,Mobile Number,Email\nTC101,,client.tc101@example.com\n"
	if _, err := o.LoadExchangeFile(ctx, T, "priya.nair", "bse_ucc_download_20261119.txt", []byte(csv2), true); err != nil {
		t.Fatal(err)
	}
	s = siOne(t, o, ctx, "ucc_fields", "2026-11-20")
	if s.found != 1 || !strings.Contains(s.examples[0], "BSE: mobile blank at the exchange") {
		t.Fatalf("bse %+v", s)
	}

	// Downloads older than 45 days are ignored.
	siWant(t, siOne(t, o, ctx, "ucc_fields", addDays("2026-11-19", 46)), SISkipped, "old downloads")

	// Branch staff cannot load one.
	res, _ = o.LoadExchangeFile(ctx, T, "meera.iyer", "nse_ucc_download.csv", []byte(csv), true)
	if len(res) == 1 && !strings.HasPrefix(res[0].Loaded, "Not loaded") {
		t.Fatalf("branch staff loaded %+v", res)
	}
}

// What NSE's UCC API accepted is fingerprinted; a client changed since and
// not sent again fails the test.
func TestUCCFieldsNSEAccepted(t *testing.T) {
	o, _, day := nseUCISetup(t)
	ctx := t.Context()
	c := nseUCITestClient("TC201", "ABCPV1201K")
	nseUCIAddClient(t, o, c, true)
	if res, err := o.NSEUCISend(ctx, T, "priya.nair", nil); err != nil || res.Accepted != 1 {
		t.Fatalf("send %+v %v", res, err)
	}
	reg, _, err := docs.Get[NSEUCIReg](ctx, o.Docs, T, cUCCReg, "NSE|TC201")
	if err != nil || len(reg.FieldPrints) != 5 || reg.FieldPrints["mobile"] == "" || reg.FieldPrintsAt == "" {
		t.Fatalf("reg %+v %v", reg, err)
	}
	date := day.Format("2006-01-02")
	siWant(t, siOne(t, o, ctx, "ucc_fields", date), SIPass, "as accepted")

	c.Email = "new.address@example.com"
	nseUCIAddClient(t, o, c, false)
	s := siOne(t, o, ctx, "ucc_fields", date)
	if siState(s) != SIFail || !strings.Contains(s.examples[0], "changed since NSE's UCC API accepted it on 30-Oct-2026 (e-mail), not sent again") {
		t.Fatalf("changed %+v", s)
	}
	*day = day.Add(time.Hour)
	if res, err := o.NSEUCISend(ctx, T, "priya.nair", nil); err != nil || res.Accepted != 1 {
		t.Fatalf("send again %+v %v", res, err)
	}
	siWant(t, siOne(t, o, ctx, "ucc_fields", date), SIPass, "sent again")
}

func TestUCCFieldNorm(t *testing.T) {
	if uccNorm("mobile", "+91-98765 43210") != "9876543210" || uccNorm("bank", "000123", "hdfc0000001") != "123|HDFC0000001" || uccNorm("bank", "", "X") != "" {
		t.Fatal("normalising")
	}
	if uccNorm("address", "12, Test Lane", "", "", "400 001") != "12testlane400001" {
		t.Fatal(uccNorm("address", "12, Test Lane", "", "", "400 001"))
	}
	if uccDlDate("x_18112026.csv", "2026-11-20") != "2026-11-18" || uccDlDate("x_20261118.csv", "2026-11-20") != "2026-11-18" || uccDlDate("x.csv", "2026-11-20") != "2026-11-20" {
		t.Fatal("dates")
	}
	if uccFieldPrint("pan", "") != "" || uccFieldPrint("pan", "A") == uccFieldPrint("mobile", "A") {
		t.Fatal("prints")
	}
}
