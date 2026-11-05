package ops

import (
	"os"
	"strings"
	"testing"
)

func checklistParties(t *testing.T, o *Ops) (map[string][]string, FileChecklist) {
	t.Helper()
	cl, err := o.DailyFileChecklist(t.Context(), T, D)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string][]string{} // party -> segments served
	for _, it := range cl.Items {
		by[it.Party] = append(by[it.Party], it.For...)
	}
	return by, cl
}

func saveClearing(t *testing.T, o *Ops, cf, names map[string]string) error {
	t.Helper()
	set, _ := o.Settings(t.Context(), T)
	set.MemberCodes = map[string]string{"NSE": "90311", "BSE": "9999", "MCX": "55555"} // made up
	set.ClearingFor, set.ClearingMemberNames = cf, names
	return o.SaveSettings(t.Context(), T, "admin", set)
}

func TestClearingChoicesFilterChecklist(t *testing.T) {
	o, _ := newOps(t)
	if err := saveClearing(t, o, nil, nil); err != nil {
		t.Fatal(err)
	}
	by, _ := checklistParties(t, o)
	if len(by["NCL"]) == 0 || len(by["ICCL"]) != 0 || len(by["BSE"]) == 0 || len(by["MCXCCL"]) == 0 {
		t.Fatalf("defaults: %v", by)
	}

	// Validation against the table.
	for _, cf := range []map[string]string{{"BSE-CM": "MCXCCL"}, {"MCX-COM": "ICCL"}, {"NSE-COM": "ICCL"}, {"XYZ-CM": "NCL"}, {"BSE-FO": "CM:"}} {
		if err := saveClearing(t, o, cf, nil); err == nil {
			t.Fatalf("%v accepted", cf)
		}
	}
	if err := saveClearing(t, o, map[string]string{"BSE-FO": "CM:1234"}, nil); err == nil {
		t.Fatal("clearing member without a name accepted")
	}

	// NSE-CM at ICCL brings in the ICCL files (for NSE-CM).
	if err := saveClearing(t, o, map[string]string{"NSE-CM": "ICCL"}, nil); err != nil {
		t.Fatal(err)
	}
	by, _ = checklistParties(t, o)
	if !contains(by["ICCL"], "NSE-CM") || contains(by["NCL"], "NSE-CM") || !contains(by["NCL"], "BSE-CM") {
		t.Fatalf("NSE-CM at ICCL: %v", by)
	}

	// Switching BSE-CM from NCL to ICCL moves its clearing files.
	if err := saveClearing(t, o, map[string]string{"NSE-CM": "ICCL", "BSE-CM": "ICCL"}, nil); err != nil {
		t.Fatal(err)
	}
	by, _ = checklistParties(t, o)
	if !contains(by["ICCL"], "BSE-CM") || contains(by["NCL"], "BSE-CM") || !contains(by["BSE"], "BSE-CM") {
		t.Fatalf("BSE-CM at ICCL: %v", by)
	}

	// Everything on ICCL (NSE commodities not traded): no NSE Clearing files.
	all := map[string]string{"NSE-COM": ClearNone}
	for _, k := range []string{"NSE-CM", "NSE-FO", "NSE-CD", "BSE-CM", "BSE-FO", "BSE-CD"} {
		all[k] = "ICCL"
	}
	if err := saveClearing(t, o, all, nil); err != nil {
		t.Fatal(err)
	}
	by, _ = checklistParties(t, o)
	if len(by["NCL"]) != 0 || len(by["ICCL"]) == 0 || len(by["NSE"]) == 0 {
		t.Fatalf("all at ICCL: %v", by)
	}

	// A trading-member-only segment has no clearing-corporation files.
	if err := saveClearing(t, o, map[string]string{"BSE-FO": "CM:1234", "MCX-COM": "cm:777"}, map[string]string{"BSE-FO": "Test Clearing Ltd", "MCX-COM": "Commodity CM Ltd"}); err != nil {
		t.Fatal(err)
	}
	by, cl := checklistParties(t, o)
	for _, p := range []string{"NCL", "ICCL"} {
		if contains(by[p], "BSE-FO") {
			t.Fatalf("BSE-FO still has %s files: %v", p, by)
		}
	}
	if !contains(by["BSE"], "BSE-FO") || !contains(by["MCX"], "MCX-COM") {
		t.Fatalf("trading member only: %v", by)
	}
	// MCXCCL still sends the trading member its own files (NOBL, MCX_MRG,
	// peak margin, NCMR …); only those are listed.
	for _, it := range cl.Items {
		if it.Party == "MCXCCL" && !it.TMLevel {
			t.Fatalf("%s listed for a segment another member clears", it.ID)
		}
	}
	ids := map[string]bool{}
	for _, it := range cl.Items {
		ids[it.ID] = true
	}
	for _, id := range []string{"mcx-nobl", "mcx-mrgfile", "mcx-peak", "mcx-ncmr", "mcx-weballoc", "mcx-collateral", "mcx-collateral-ucc", "mcx-udiff-margin", "mcx-products", "mcx-bhav"} {
		if !ids[id] {
			t.Fatalf("%s not listed for MCX-COM cleared by another member", id)
		}
	}
	if len(cl.Notes) != 2 || !strings.Contains(strings.Join(cl.Notes, " "), "Test Clearing Ltd") || !strings.Contains(strings.Join(cl.Notes, " "), "MCXCCL sends the trading member itself") {
		t.Fatalf("notes %v", cl.Notes)
	}
	set, _ := o.Settings(t.Context(), T)
	if set.ClearedThrough("MCX-COM") != "CM:777" || set.ClearedThrough("NSE-CM") != "NCL" {
		t.Fatalf("stored %v", set.ClearingFor)
	}
}

func TestChecklistTicksReceivedFiles(t *testing.T) {
	o, ctx := newOps(t)
	if err := saveClearing(t, o, nil, nil); err != nil {
		t.Fatal(err)
	}
	name := "MBRSTD001_OTB_CM_9999_161026.CSV"
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	loadX(t, o, name, b)
	_, cl := checklistParties(t, o)
	got := map[string]string{}
	for _, it := range cl.Items {
		got[it.ID] = it.Status
	}
	if got["bse-stamp"] != "Received" || got["bse-trade-f"] != "Missing" || got["iccl-var"] != "" {
		t.Fatalf("%v", got)
	}
	miss, err := o.MissingDailyFiles(ctx, T, "system", D)
	if err != nil || len(miss) == 0 || len(miss) != cl.Missing {
		t.Fatalf("missing %d of %d: %v", len(miss), cl.Missing, err)
	}
}
