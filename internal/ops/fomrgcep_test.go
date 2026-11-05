package ops

import (
	"strings"
	"testing"
)

func TestFOMarginReporting(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	ucc := cs[0].UCC
	if _, _, err := o.ExchangeFile(ctx, T, "priya.nair", "ncl-margin-reporting-fo", D, XFileOptions{}); err == nil || !strings.Contains(err.Error(), "F_MG13") {
		t.Fatalf("without the F&O MG13: %v", err)
	}
	loadX(t, o, "F_MG13_90311_16102026.lis", xwrite(t, "ncl-span-client-fo",
		xr("", "Date", "16-Oct-2026", "ClientCode", ucc, "Field3", "5000.00", "Field4", "0.00", "Field5", "0.00", "Field6", "0.00", "Field7", "5000.00", "Field8", "5000.00", "Field9", "6000.00", "ClientProFlag", "C"),
		xr("", "Date", "16-Oct-2026", "ClientCode", "OWN", "Field3", "700.00", "Field4", "0.00", "Field5", "0.00", "Field6", "0.00", "Field7", "700.00", "Field8", "700.00", "Field9", "800.00", "ClientProFlag", "P")))
	name, b, err := o.ExchangeFile(ctx, T, "priya.nair", "ncl-margin-reporting-fo", D, XFileOptions{})
	if err != nil || name != "F_MRG_TM_16102026_01.csv" {
		t.Fatalf("%s %v", name, err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("%q", b)
	}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.Count(l, ",") != 11 {
			t.Fatalf("not 12 fields: %q", l)
		}
		f := strings.Split(l, ",")
		// The MG13 row unaltered, then collected at EOD and at peak.
		if f[1] == "OWN" && (f[10] != "700.00" || f[11] != "700.00") {
			t.Fatalf("pro row %q", l)
		}
		if f[1] == ucc && f[2] != "5000.00" {
			t.Fatalf("client row altered %q", l)
		}
	}
	// The format list says Ledgerline writes it; HDFC ENet and the CDSL BO
	// upload are written on their own screens.
	fs, _ := o.ExchangeFormats()
	for _, f := range fs {
		switch f.ID {
		case "ncl-margin-reporting-fo":
			if !f.Writes || f.WrittenOn != "" {
				t.Errorf("%+v", f)
			}
		case "hdfc-enet-payout", "cdsl-bo-upload":
			if !f.Writes || f.WrittenOn == "" {
				t.Errorf("%+v", f)
			}
		}
	}
}

func TestCEPFeedsEarlyPayin(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.Clients(ctx, T)
	ucc := cs[0].UCC
	if n := len(o.EPIAccepted(ctx, T, D)); n != 0 {
		t.Fatalf("%d before", n)
	}
	b := xwrite(t, "cdsl-early-payin",
		xr("", "RecordType", "10", "FileType", "CEP", "Date", "16102026", "Field4", "1", "MemberCode", "90311", "RecordCount", "3"),
		xr("", "RecordType", "20", "SettlementType", "N", "SettlementNo", "2026199", "Symbol", "RELIANCE", "Series", "EQ", "ClientCode", ucc, "QuantityRequested", "10", "QuantityAllocated", "10"),
		xr("", "RecordType", "20", "SettlementType", "N", "SettlementNo", "2026199", "Symbol", "RELIANCE", "Series", "EQ", "ClientCode", ucc, "QuantityRequested", "5", "QuantityAllocated", "4"),
		xr("", "RecordType", "20", "SettlementType", "N", "SettlementNo", "2026199", "Symbol", "TCS", "Series", "EQ", "ClientCode", ucc, "QuantityRequested", "3", "QuantityAllocated", "0"))
	r := loadX(t, o, "90311_CEP20261016.D01", b)
	if r.Format != "cdsl-early-payin" {
		t.Fatalf("%+v", r)
	}
	es := o.EPIAccepted(ctx, T, D)
	if len(es) != 1 || es[0].Qty != 14 || es[0].Symbol != "RELIANCE" || es[0].UCC != ucc || es[0].Exchange != "NSE" {
		t.Fatalf("%+v", es)
	}
	if got := o.epiCovered(ctx, T, D)[ucc+"|RELIANCE"]; got != 14 {
		t.Fatalf("live risk covered %d", got)
	}
}
