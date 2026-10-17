package ops

import (
	"bytes"
	"strings"
	"testing"
)

func TestComplianceDesk(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.ClientMap(ctx, T)
	a, b := cs["BH1005"], cs["BH1006"]
	csv := "PAN,Name,Regulator,OrderNo,OrderDate,Till\n" + a.PAN + ",Someone Else,SEBI,WTM/2026/01,2026-01-10,\n,Mr. " + b.Name + ",UNSC,QDi.999,2020-02-02,\n"
	if _, err := o.LoadFile(ctx, T, "vikram.shah", "DEBARRED-LIST", "", "debarred.csv", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	c, _ := o.Client(ctx, T, "BH1005")
	if !c.Blocked || !strings.Contains(c.BlockedReason, "WTM/2026/01") {
		t.Fatalf("PAN match not blocked: %+v", c)
	}
	als, _ := o.Alerts(ctx, T)
	var hi, med bool
	for _, x := range als {
		hi = hi || (x.UCC == "BH1005" && x.Type == "Debarred entity")
		med = med || (x.UCC == "BH1006" && x.Type == "Possible debarred entity")
	}
	if !hi || !med {
		t.Fatalf("alerts high=%v name=%v", hi, med)
	}
	// Onboarding the debarred PAN is refused.
	nc := Client{Name: "X Y", Type: "Individual", PAN: a.PAN, Mobile: "9876543210", Email: "x@y.in", City: "Pune", Segments: []string{"CM"}, Bank: BankAccount{"HDFC", "HDFC0001234", "123456789012"}, Demat: Demat{"CDSL", "1208160012345678"}, RiskDisclosure: true, IncomeProof: true, Income: "5-10 L", RAS: "Quarterly"}
	if _, err := o.CreateApplication(ctx, T, "priya.nair", nc); err == nil || !strings.Contains(err.Error(), "PAN") {
		t.Fatalf("debarred onboarding: %v", err)
	}
	// KRA and CKYC.
	if _, err := o.LoadFile(ctx, T, "vikram.shah", "KRA-STATUS", "", "kra.csv", []byte("PAN,Status,Remarks\n"+cs["BH1001"].PAN+",Validated,\n"+cs["BH1002"].PAN+",On hold,Address proof unclear\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := o.LoadFile(ctx, T, "vikram.shah", "CKYC-NUMBERS", "", "ckyc.csv", []byte("PAN,CKYCNumber\n"+cs["BH1001"].PAN+",12345678901234\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := o.LoadFile(ctx, T, "vikram.shah", "CKYC-NUMBERS", "", "bad.csv", []byte("PAN,CKYCNumber\nX,12\n")); err == nil {
		t.Fatal("bad CKYC number accepted")
	}
	v, err := o.PMLA(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	row := func(u string) PMLARow {
		for _, r := range v.Rows {
			if r.UCC == u {
				return r
			}
		}
		t.Fatalf("no row %s", u)
		return PMLARow{}
	}
	if r := row("BH1001"); r.KRAStatus != "Validated" || r.CKYC != "12345678901234" || r.ReKYCDue == "" {
		t.Fatalf("BH1001 %+v", r)
	}
	if r := row("BH1005"); r.Suggested != "High" || len(r.Debarred) == 0 {
		t.Fatalf("BH1005 %+v", r)
	}
	// Compliance sets risk and special categories; re-KYC is then due sooner.
	if _, err := o.SetClientRisk(ctx, T, "farhan.khan", "BH1003", "High", []string{"Politically exposed person"}, ""); err == nil {
		t.Fatal("checker set risk")
	}
	if _, err := o.SetClientRisk(ctx, T, "vikram.shah", "BH1003", "High", []string{"Politically exposed person"}, "declared PEP"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordReKYC(ctx, T, "priya.nair", "BH1003", D, "Video KYC"); err != nil {
		t.Fatal(err)
	}
	v, _ = o.PMLA(ctx, T, D)
	if r := row("BH1003"); r.ReKYCDue != "2028-10-16" || r.Suggested != "High" {
		t.Fatalf("BH1003 %+v", r)
	}
	f, _ := o.CKYCRequestFile(ctx, T)
	if bytes.Contains(f, []byte("BH1001")) || !bytes.Contains(f, []byte("BH1002")) {
		t.Fatalf("CKYC request:\n%s", f)
	}
	// Documents: PDFs and images only, stored and returned intact.
	pdf := []byte("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF")
	d, err := o.AddClientDoc(ctx, T, "priya.nair", "BH1001", "PAN card", "pan.pdf", pdf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.AddClientDoc(ctx, T, "priya.nair", "BH1001", "PAN card", "x.html", []byte("<html><script>alert(1)</script>")); err == nil {
		t.Fatal("HTML accepted as a document")
	}
	_, got, err := o.ClientDocFile(ctx, T, "farhan.khan", "BH1001", d.ID)
	if err != nil || !bytes.Equal(got, pdf) {
		t.Fatalf("document round trip: %v", err)
	}
	// STT reconciliation against an exchange statement.
	m, _, _ := o.Market(ctx, T, D)
	if _, err := o.App.RunBilling(ctx, T, "farhan.khan", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	rec, _ := o.STTReconcile(ctx, T, D)
	var nse STTReconRow
	for _, r := range rec.Rows {
		if r.Exchange == "NSE" && r.Segment == "CM" {
			nse = r
		}
	}
	if nse.OurSTT == 0 || nse.Status != "No statement" {
		t.Fatalf("NSE CM recon %+v", nse)
	}
	stmt := "Exchange,Segment,Turnover,STT\nNSE,CM," + nse.OurTurn.String() + "," + (nse.OurSTT + 5000).String() + "\n"
	if _, err := o.LoadFile(ctx, T, "farhan.khan", "STT-STATEMENT", D, "stt.csv", []byte(stmt)); err != nil {
		t.Fatal(err)
	}
	rec, _ = o.STTReconcile(ctx, T, D)
	for _, r := range rec.Rows {
		if r.Exchange == "NSE" && r.Segment == "CM" && (r.Status != "Difference" || r.DiffSTT != -5000) {
			t.Fatalf("NSE CM recon %+v", r)
		}
	}
	for _, code := range []string{"register_securities", "client_register", "ledger_balances", "no_ras_auth", "kyc_registry"} {
		rows, err := o.ReportRows(ctx, T, D, code)
		if err != nil || len(rows) < 1 || (code != "no_ras_auth" && len(rows) < 2) {
			t.Fatalf("%s: %v (%d rows)", code, err, len(rows))
		}
	}
}
