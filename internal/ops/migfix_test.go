package ops

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	"ledgerline/internal/migrate/pkg"
)

// The check names each go-live row's client and whether the value is
// missing, warns where one PAN has two active individual codes, and the
// clients-to-fix list gives one client a row with each detail in plain
// words.
func TestMigClientsToFix(t *testing.T) {
	o, ctx := migSetup(t)
	var buf bytes.Buffer
	w, err := pkg.Create(&buf, mPass, "test tool")
	if err != nil {
		t.Fatal(err)
	}
	m := w.Manifest()
	m.Source = pkg.Source{System: "SharePro"}
	m.Firms = []pkg.Firm{{Code: "01", Name: "Test Broking", AsOf: mAsOf}}
	a := cmRow("TA001", "Test Client A", "ABCPA1234A", "")           // mobile missing
	b := cmRow("TB002", "Test Client B", "ABCPB1234B", "9876500002") // account not valid
	b[14] = "12AB"
	c := cmRow("TC003", "Test Client B Again", "ABCPB1234B", "9876500003") // same PAN, active
	d := cmRow("TD004", "Test Client B Old", "ABCPB1234B", "9876500004")   // same PAN, inactive
	cols := append(append([]string{}, cmCols...), "Inactive")
	writeCSV(t, w, "01", "CLIENT-MASTER", [][]string{cols, append(a, "N"), append(b, "N"), append(c, "N"), append(d, "Y")})
	writeCSV(t, w, "01", "NOMINEES", [][]string{{"UCC", "Name", "Relation", "Share", "IDType", "ID", "DOB", "Guardian", "GuardianRelation"},
		{"TA001", "Test Nominee", "Son", "100", "", "", "", "", ""}})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	p := checked(t, o, ctx, buf.Bytes(), "fix.llmig")
	errs, _, _ := o.MigErrors(ctx, MT, "adm.one", p.ID, "", 0, 0)
	var mob, acct *MigError
	pans := 0
	for i, e := range errs {
		switch {
		case e.UCC == "TA001" && e.Field == "mobile":
			mob = &errs[i]
		case e.UCC == "TB002" && strings.HasSuffix(e.Field, "account"):
			acct = &errs[i]
		case e.Field == "PAN" && strings.Contains(e.Message, "one active code per individual client"):
			pans++
		}
	}
	if mob == nil || !mob.Blank || mob.Client != "Test Client A" || mob.ClientStatus != "Active" {
		t.Fatalf("mobile error %+v in %+v", mob, errs)
	}
	if acct == nil || acct.Blank {
		t.Fatalf("account error %+v", acct)
	}
	if pans != 2 { // TB002 and TC003, not the inactive TD004
		t.Fatalf("PAN warnings %d in %+v", pans, errs)
	}
	var out bytes.Buffer
	if err := o.MigClientsToFixCSV(ctx, MT, "adm.one", p.ID, false, &out); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&out).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	by := map[string][]string{}
	for _, r := range rows[1:] {
		by[r[0]] = r
	}
	if rows[0][0] != "Client code" || rows[0][5] != "Details to fix" || len(rows) != 3 {
		t.Fatalf("list %v", rows)
	}
	if r := by["TA001"]; r[1] != "Test Client A" || r[2] != "Active" || !strings.Contains(r[5], "Mobile number missing") || r[7] != "Yes" {
		t.Errorf("TA001 %v", r)
	}
	if r := by["TB002"]; !strings.Contains(r[5], "Primary bank account number: ") || strings.Contains(r[5], "PAN") {
		t.Errorf("TB002 %v", r)
	}
	out.Reset()
	o.MigClientsToFixCSV(ctx, MT, "adm.one", p.ID, true, &out)
	if !strings.Contains(out.String(), "TC003,Test Client B Again,Active,0,1,PAN: the PAN is also on active client code TB002") {
		t.Errorf("with warnings:\n%s", out.String())
	}
}
