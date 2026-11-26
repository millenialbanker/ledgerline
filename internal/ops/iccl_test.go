package ops

import (
	"fmt"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// icclFirm makes the sample firm clear its own cash and derivatives trades
// on every exchange at ICCL (made-up codes).
func icclFirm(t *testing.T, o *Ops) {
	t.Helper()
	set, _ := o.Settings(t.Context(), T)
	set.MemberCodes = map[string]string{"NSE": "90311", "BSE": "9999", "ICCL": "C777", "MCX": "55555"}
	set.ClearingFor = map[string]string{}
	for _, k := range []string{"NSE-CM", "NSE-FO", "NSE-CD", "BSE-CM", "BSE-FO", "BSE-CD"} {
		set.ClearingFor[k] = "ICCL"
	}
	if err := o.SaveSettings(t.Context(), T, "admin", set); err != nil {
		t.Fatal(err)
	}
}

// A firm that clears at NSE Clearing sees nothing of ICCL: no screen, no
// ICCL file to chase, no files written, no work items.
func TestICCLOffAtNCL(t *testing.T) {
	o, ctx := setup(t)
	v, err := o.ICCLDay(ctx, T, "priya.nair", D)
	if err != nil || v.On || len(v.Margins) != 0 {
		t.Fatalf("NCL firm: %+v %v", v, err)
	}
	if _, _, err := o.ExchangeFile(ctx, T, "priya.nair", "iccl-margin-reporting", D, XFileOptions{}); err == nil {
		t.Fatal("ICCL file written for a firm at NSE Clearing")
	}
	if n := o.RunICCLChecks(ctx, T, D); n != 0 {
		// RunICCLChecks is only called by the tick for an ICCL firm, but
		// even called directly it finds nothing cleared at ICCL.
		t.Fatalf("checks raised %d", n)
	}
	cl, _ := o.DailyFileChecklist(ctx, T, D)
	for _, it := range cl.Items {
		if it.Party == "ICCL" {
			t.Fatalf("ICCL file listed: %s", it.ID)
		}
	}
	if _, err := o.SaveICCLReason(ctx, T, "priya.nair", ICCLReason{Date: D, UCC: "X", Code: "01"}); err == nil {
		t.Fatal("reason saved for an NCL firm")
	}
}

// The layouts ICCL has not published are marked to confirm, with the
// notice they follow.
func TestICCLLayoutsToConfirm(t *testing.T) {
	lib, err := XFiles()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range lib.Specs {
		if !strings.HasPrefix(s.ID, "iccl-") {
			continue
		}
		n++
		if !s.Confirm || s.Confirmed || s.Notice == "" || !strings.Contains(s.Source, s.Notice) {
			t.Errorf("%s: confirm %v confirmed %v notice %q", s.ID, s.Confirm, s.Confirmed, s.Notice)
		}
		if s.Direction == "out" && !xWriters[s.ID] {
			t.Errorf("%s is not written", s.ID)
		}
	}
	if n != 8 {
		t.Fatalf("%d ICCL layouts", n)
	}
}

func TestICCLDayAndFiles(t *testing.T) {
	o, ctx := setup(t)
	icclFirm(t, o)
	v, err := o.ICCLDay(ctx, T, "priya.nair", D)
	if err != nil || !v.On {
		t.Fatalf("%+v %v", v, err)
	}
	if strings.Join(v.Segments, ",") != "CM,FO,CD" || len(v.Layouts) != 8 || v.Codes["cm"] != "C777" || v.Codes["tm"] != "9999" {
		t.Fatalf("segments %v layouts %d codes %v", v.Segments, len(v.Layouts), v.Codes)
	}
	if len(v.Margins) == 0 {
		t.Fatal("no margin rows on the sample day")
	}
	if v.Cutoffs.Funds != "10:30" || v.Cutoffs.AuctionInstr != "07:50" {
		t.Fatalf("cut-offs %+v", v.Cutoffs)
	}
	// Margin reporting: one line per client and segment, with the firm's
	// clearing and trading codes.
	name, b, err := o.ExchangeFile(ctx, T, "priya.nair", "iccl-margin-reporting", D, XFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if name != "ICCL_MRG_C777_16102026.csv" || lines[0] != "Trade Date,Segment,CM Code,TM Code,CP Code,Client Code,EOD Margin Collected,Peak Margin Collected" || len(lines) != len(v.Margins)+1 {
		t.Fatalf("%s\n%s", name, b)
	}
	if !strings.HasPrefix(lines[1], "16-10-2026,") || !strings.Contains(lines[1], ",C777,9999,,") {
		t.Fatalf("line %q", lines[1])
	}
	// The segregation report has every margin row, plus nil-balance
	// clients who traded at ICCL on the day.
	_, b, err = o.ExchangeFile(ctx, T, "priya.nair", "iccl-collateral-segregation", D, XFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(b)), "\n"); got < len(v.Margins) {
		t.Fatalf("segregation %d rows < %d", got, len(v.Margins))
	}
	// The ICCLCOLL file is written only when every allocation keeps the caps.
	if v.AllocIssues == 0 {
		if _, b, err = o.ExchangeFile(ctx, T, "priya.nair", "iccl-collateral-allocation", D, XFileOptions{}); err != nil && !strings.Contains(err.Error(), "no cash") {
			t.Fatal(err)
		} else if err == nil && !strings.Contains(string(b), ",C777,9999,,") {
			t.Fatalf("ICCLCOLL %s", b)
		}
	}
	// Reported margins are kept, and the allocation cap reads them.
	rep, _, _ := docs.Get[icclReported](ctx, o.Docs, T, cICCLReported, D)
	if len(rep.EOD) != len(v.Margins) {
		t.Fatalf("reported %d", len(rep.EOD))
	}
	// A firm that clears only some exchanges' cash market at ICCL gets no
	// ICCL cash files: the collateral would be mixed.
	set, _ := o.Settings(ctx, T)
	set.ClearingFor["NSE-CM"] = "NCL"
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	v, _ = o.ICCLDay(ctx, T, "priya.nair", D)
	if contains(v.Segments, "CM") || len(v.Notes) == 0 || !strings.Contains(v.Notes[0], "cleared at ICCL for some exchanges") {
		t.Fatalf("split: %v %v", v.Segments, v.Notes)
	}
	for _, m := range v.Margins {
		if m.Segment == "CM" {
			t.Fatalf("CM reported to ICCL while split: %+v", m)
		}
	}
}

// ICCL's short allocation file is read by its header names; the reasons
// go back with ICCL's rows unaltered.
func TestICCLShortAllocation(t *testing.T) {
	o, ctx := setup(t)
	icclFirm(t, o)
	// Made-up file: the header is a guess at ICCL's, as the layout reads it.
	data := "Trade Date,Snapshot,Segment,TM Code,Client Code,Margin Required,Total Collateral,Short Allocation,Remarks\n" +
		"16-10-2026,EOD,CM,9999,ZZTEST1,1000.00,400.00,600.00,x\n" +
		"16-10-2026,S1,CM,9999,ZZTEST2,500.00,500.00,0.00,y\n"
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", "SHORTALLOCATION_CM_9999_16102026.csv", []byte(data), true)
	if err != nil || len(res) != 1 || res[0].Format != "iccl-short-allocation" || res[0].Added != 2 {
		t.Fatalf("%+v %v", res, err)
	}
	w, _ := workItem(ctx, o, wICCL, "short|2026-10-16|CM")
	if w.Status != "Open" || !strings.Contains(w.Title, "1 ICCL short") {
		t.Fatalf("work %+v", w)
	}
	if _, err := o.SaveICCLReason(ctx, T, "priya.nair", ICCLReason{Date: D, Snapshot: "EOD", Seg: "CM", UCC: "ZZTEST1", Code: "09", Amount: money.DecInt(600)}); err == nil {
		t.Fatal("code 09 taken")
	}
	if _, err := o.SaveICCLReason(ctx, T, "priya.nair", ICCLReason{Date: D, Snapshot: "S9", Seg: "CM", UCC: "ZZTEST1", Code: "01", Amount: money.DecInt(600)}); err == nil {
		t.Fatal("reason for a row ICCL did not send")
	}
	for _, c := range []string{"01", "08"} {
		if _, err := o.SaveICCLReason(ctx, T, "priya.nair", ICCLReason{Date: D, Snapshot: "EOD", Seg: "CM", UCC: "ZZTEST1", Code: c, Amount: money.DecInt(300)}); err != nil {
			t.Fatal(err)
		}
	}
	if w, _ := workItem(ctx, o, wICCL, "short|2026-10-16|CM"); w.Status != "Done" {
		t.Fatalf("work after reasons %+v", w)
	}
	_, b, err := o.ExchangeFile(ctx, T, "priya.nair", "iccl-short-allocation-reporting", D, XFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := "Trade Date,Snapshot,Segment,TM Code,Client Code,Margin Required,Total Collateral,Short Allocation,Remarks,Reason Code,Collateral Available\n" +
		"16-10-2026,EOD,CM,9999,ZZTEST1,1000.00,400.00,600.00,x,01,300.00\n" +
		"16-10-2026,EOD,CM,9999,ZZTEST1,1000.00,400.00,600.00,x,08,300.00\n"
	if string(b) != want {
		t.Fatalf("got\n%s\nwant\n%s", b, want)
	}
	v, _ := o.ICCLDay(ctx, T, "priya.nair", D)
	if len(v.Short) != 2 || len(v.Short[0].Reasons) != 2 {
		t.Fatalf("short rows %+v", v.Short)
	}
}

// ICCL's ALLOCATIONDIFF raises a work item per client.
func TestICCLAllocationDiff(t *testing.T) {
	o, ctx := setup(t)
	icclFirm(t, o)
	data := "Trade Date|Segment|Client Code|Actual Allocation|Reported Amount|Difference\n16-10-2026|CM|ZZTEST3|100.00|250.00|-150.00\n"
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", "ALLOCATIONDIFF_C777_16102026.csv", []byte(data), true)
	if err != nil || len(res) != 1 || res[0].Format != "iccl-allocation-diff" {
		t.Fatalf("%+v %v", res, err)
	}
	w, _ := workItem(ctx, o, wICCL, "diff|2026-10-16|CM|ZZTEST3")
	if w.Status != "Open" || w.UCC != "ZZTEST3" || !strings.Contains(w.Detail, "₹250") {
		t.Fatalf("%+v", w)
	}
	v, _ := o.ICCLDay(ctx, T, "priya.nair", D)
	if len(v.Diffs) != 1 || v.Diffs[0].Diff.Cmp(money.DecInt(-150)) != 0 {
		t.Fatalf("%+v", v.Diffs)
	}
}

// Pay-in cut-offs are per clearing corporation; ICCL's default to BSECL's.
func TestICCLCutoffs(t *testing.T) {
	var c RiskLimitConfig
	c.fill()
	if got := c.cutoffs("ICCL"); got != (payInCutoffs{"10:30", "10:30", "08:00", "07:50"}) {
		t.Fatalf("ICCL %+v", got)
	}
	if got := c.cutoffs("NCL"); got != (payInCutoffs{"11:00", "10:30", "08:00", "08:00"}) {
		t.Fatalf("NCL %+v", got)
	}
	if f, a := c.latestCutoff(); f != "11:00" || a != "08:00" {
		t.Fatalf("latest %s %s", f, a)
	}
}

// BSECL counts settlement accounts in its limit of 30.
func TestICCLThirtyAccounts(t *testing.T) {
	o, ctx := setup(t)
	icclFirm(t, o)
	for i := 0; i < 31; i++ {
		kind := BankUSCNBA
		if i%2 == 1 {
			kind = BankSettlement
		}
		if _, err := o.SaveFirmBank(ctx, T, "vikram.shah", FirmBankReq{FirmBank: FirmBank{Kind: kind, Bank: "Test Bank", Account: fmt.Sprintf("90000000%04d", i), IFSC: "TEST0000001", Active: true}}); err != nil {
			t.Fatal(err)
		}
	}
	v, err := o.FirmBanks(ctx, T, "vikram.shah")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range v.Warnings {
		found = found || strings.Contains(w, "ICCL allows at most 30")
	}
	if !found {
		t.Fatalf("no ICCL warning: %v", v.Warnings)
	}
}
