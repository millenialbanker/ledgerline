package ops

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

// fgRead reads the upload's CSVs: file -> rows of header -> value.
func fgRead(t *testing.T, b []byte) map[string][]map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]map[string]string{}
	for _, f := range zr.File {
		if strings.Contains(f.Name, "/") {
			t.Fatalf("a folder in the zip: %s", f.Name)
		}
		rc, _ := f.Open()
		recs, err := csv.NewReader(rc).ReadAll()
		rc.Close()
		if err != nil || len(recs) < 2 {
			t.Fatalf("%s: %v (%d rows)", f.Name, err, len(recs))
		}
		for _, r := range recs[1:] {
			m := map[string]string{}
			for i, h := range recs[0] {
				m[h] = r[i]
			}
			out[strings.TrimSuffix(f.Name, ".csv")] = append(out[strings.TrimSuffix(f.Name, ".csv")], m)
		}
	}
	return out
}

func TestFINGateSTR(t *testing.T) {
	o, ctx := setup(t)
	trades, _ := o.exchangeTrades(ctx, T, D)
	cm, _ := o.ClientMap(ctx, T)
	ucc, n := "", 0
	for _, tr := range trades {
		if c := cm[tr.UCC]; c.Type == "Individual" && ucc == "" {
			ucc = tr.UCC
		}
	}
	for _, tr := range trades {
		if tr.UCC == ucc {
			n++
		}
	}
	if ucc == "" || n == 0 {
		t.Fatal("no individual client with trades in the sample day")
	}
	if !o.raiseAlert(ctx, T, SystemActor, Alert{Date: D, Source: "Internal", Type: "Turnover inconsistent with income", UCC: ucc, Severity: "High", Detail: "made-up detail"}, "TST:"+ucc) {
		t.Fatal("alert not raised")
	}
	id := "A-TST-" + ucc
	if _, err := o.FINGateUpload(ctx, T, "vikram.shah", nil); err == nil {
		t.Fatal("an upload with no STR decided")
	}
	// Only compliance decides; the decision needs the grounds.
	if _, err := o.DecideSTR(ctx, T, "priya.nair", id, STRInput{Grounds: "x"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker decides: %v", err)
	}
	if _, err := o.DecideSTR(ctx, T, "vikram.shah", id, STRInput{}); err == nil {
		t.Fatal("decided without grounds")
	}
	// Metadata as FINGate gives it (made-up IDs), and the firm's branch.
	for list, csvText := range map[string]string{
		"country":      "Entry ID,Country Name\n101,India\n102,Nepal\n",
		"nationality":  "Entry ID,Nationality\n201,Indian\n",
		"account_type": "Entry ID,Account Type\n301,Savings Account\n302,Trading Account\n",
		"suspicion":    "Entry ID,Suspicious Category\n401,Suspicious Transaction\n",
		"source_alert": "Source of Alert\nTransaction Monitoring\nCustomer Verification\n",
	} {
		if k, err := o.LoadFINGateMeta(ctx, T, "vikram.shah", list, []byte(csvText)); err != nil || k == 0 {
			t.Fatalf("%s: %d %v", list, k, err)
		}
	}
	if _, err := o.LoadFINGateMeta(ctx, T, "vikram.shah", "nonsense", []byte("Entry ID,X\n1,a\n")); err == nil {
		t.Fatal("an unknown list was loaded")
	}
	if _, err := o.SaveFINGateSettings(ctx, T, "vikram.shah", FINGateSettings{BranchCode: "HO01"}); err != nil {
		t.Fatal(err)
	}
	a, err := o.DecideSTR(ctx, T, "vikram.shah", id, STRInput{Grounds: "Trades far above the declared income, funded by a third party.", Suspicion: "Suspicious Transaction"})
	if err != nil || a.Status != "STR to file" || a.STR == nil || a.STR.Ref != "LLSTR000001" || a.STR.From != D || a.STR.Due <= D {
		t.Fatalf("decided %+v %v", a.STR, err)
	}
	w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-aml-str-"+id)
	if err != nil || w.Status != "Open" || w.Role != RoleCompliance || strings.Contains(w.Title+w.Detail, cm[ucc].Name) {
		t.Fatalf("STR work item %+v %v", w, err)
	}
	if _, err := o.ActOnAlert(ctx, T, "vikram.shah", id, "close", "changed my mind"); !errors.Is(err, ErrState) {
		t.Fatalf("closed a decided STR: %v", err)
	}
	// The upload.
	if _, err := o.FINGateUpload(ctx, T, "priya.nair", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker exports: %v", err)
	}
	ex, err := o.FINGateUpload(ctx, T, "vikram.shah", nil)
	if err != nil || len(ex.Reports) != 1 || ex.Reports[0] != "LLSTR000001" || !strings.HasSuffix(ex.Name, ".zip") {
		t.Fatalf("upload %+v %v", ex, err)
	}
	f := fgRead(t, ex.Zip)
	if testing.Verbose() {
		t.Logf("files %v gaps %d", ex.Files, len(ex.Gaps))
		for _, g := range ex.Gaps {
			t.Logf("gap %s %s: %s", g.File, g.Field, g.Why)
		}
		t.Logf("TS7[0] %v", f["TS7"][0])
		t.Logf("Account %v", f["Account_Detail"][0])
	}
	for _, k := range []string{"KC1", "Account_Detail", "Account_Person_Relation", "TS7", "GS1"} {
		if len(f[k]) == 0 {
			t.Fatalf("no %s in %v", k, ex.Files)
		}
	}
	if _, ok := f["KC2"]; ok {
		t.Fatal("KC2 for an individual")
	}
	c := cm[ucc]
	k1 := f["KC1"][0]
	if k1["Report Reference Number"] != "LLSTR000001" || k1["UCIC"] != ucc || k1["PAN"] != c.PAN || k1["First Name"] == "" ||
		k1["Nationality"] != "201" || k1["Primary Address Country"] != "101" || (c.DOB != "" && k1["Date of Birth"] != fgDate(c.DOB)) {
		t.Fatalf("KC1 %v", k1)
	}
	ad := f["Account_Detail"][0]
	if ad["Account Type"] != "302" || ad["Account Number"] != ucc || ad["Branch Code of Account"] != "HO01" || ad["Account Status"] == "" {
		t.Fatalf("Account_Detail %v", ad)
	}
	if apr := f["Account_Person_Relation"][0]; apr["Account Number"] != ucc || apr["Unique Reference Number"] != ucc || apr["Individual / Non-individual"] != "Individual" {
		t.Fatalf("Account_Person_Relation %v", apr)
	}
	if len(f["TS7"]) != n {
		t.Fatalf("TS7 has %d trades, the client made %d", len(f["TS7"]), n)
	}
	for _, r := range f["TS7"] {
		if r["Report Type"] != "STR" || (r["Transaction Type"] != "Buy" && r["Transaction Type"] != "Sell") || r["Trade Date"] != fgDate(D) || r["Amount"] == "" ||
			(r["Trade Type"] == "Options" && r["Call / Put"] == "") || (r["Trade Type"] != "Equity" && r["Expiry Date"] == "") || r["Client ID/Customer ID/UCIC"] != ucc {
			t.Fatalf("TS7 row %v", r)
		}
	}
	gs := f["GS1"][0]
	if gs["GoS Tag 1 - Suspicion Due To"] != "401" || gs["GoS Tag 2 - Source of Alert"] != "Transaction Monitoring" || !strings.Contains(gs["Narration"], "funded by a third party") || gs["KYC Source of Funds"] == "" {
		t.Fatalf("GS1 %v", gs)
	}
	// Uncoded fields are listed, not guessed.
	gaps := map[string]bool{}
	for _, g := range ex.Gaps {
		gaps[g.File+"|"+g.Field] = true
	}
	if !gaps["GS1|GoS Tag 3 - Red Flag Indicator"] || !gaps["KC1|Primary Address State"] || gaps["KC1|Nationality"] {
		t.Fatalf("gaps %+v", ex.Gaps)
	}
	if a, _, _ := docs.Get[Alert](ctx, o.Docs, T, cAlert, id); a.STR.Exported == "" {
		t.Fatal("export not recorded")
	}
	// The filing closes the alert and the work item.
	if _, err := o.ActOnAlert(ctx, T, "vikram.shah", id, "filed", ""); err == nil {
		t.Fatal("filed without the batch ID")
	}
	a, err = o.ActOnAlert(ctx, T, "vikram.shah", id, "filed", "Batch BATCH0001 / report RPT0001")
	if err != nil || a.Status != "STR filed" || a.STR.Filing == "" || a.STR.FiledOn == "" {
		t.Fatalf("filed %+v %v", a, err)
	}
	if w, _, _ := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-aml-str-"+id); w.Status != "Done" {
		t.Fatalf("work item %+v", w)
	}
	if _, err := o.FINGateUpload(ctx, T, "vikram.shah", nil); err == nil {
		t.Fatal("a filed STR is offered again")
	}
	if ex, err := o.FINGateUpload(ctx, T, "vikram.shah", []string{id}); err != nil || len(ex.Reports) != 1 {
		t.Fatalf("a filed STR asked for by ID %v", err)
	}
}
