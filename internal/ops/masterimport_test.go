package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
)

// Made-up offices, partners and clients only.

func csvOf(rows ...[]string) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.WriteAll(rows)
	return b.Bytes()
}

var (
	goodBranches = csvOf([]string{"Code", "Name", "City", "Manager", "Parent", "Kind", "Active"},
		[]string{"PUN", "Test Pune Branch", "Pune", "Test Manager", "WEST", "", ""}, // its parent comes later
		[]string{"WEST", "Test West Zone", "Mumbai", "", "", "zone", "Y"},
		[]string{"OLD", "Test Old Office", "Nashik", "", "WEST", "Branch", "N"})
	goodPartners = csvOf([]string{"Code", "Name", "Kind", "Branch", "Parent", "PAN", "GSTIN", "RegNo", "SharePct", "Bank", "Account", "IFSC", "Email", "Active"},
		[]string{"AP01", "Test Partner One", "Authorised person", "PUN", "", "ABCPE1234E", "", "AP-TEST-1", "40", "Test Bank", "123456789012", "TEST0123456", "ap01@example.com", ""},
		[]string{"AP02", "Test Partner Two", "authorised person", "PUN", "AP01", "", "", "", "25.5", "", "", "", "", "Y"},
		[]string{"RM01", "Test Manager One", "Relationship manager", "PUN", "", "", "", "", "", "", "", "", "", "N"})
)

func goodClients(extra ...[]string) []byte {
	head := append(append([]string{}, cmCols...), "Branch", "AP", "RM", "Groups", "Inactive", "InactiveSince")
	a := append(cmRow("TA001", "Test Client A", "ABCPA1234A", "9876500001"), "PUN", "AP02", "RM01", "hni; staff", "", "")
	b := append(cmRow("TB002", "Test Client B", "ABCPB1234B", "9876500002"), "PUN", "", "", "", "Y", "2025-03-31")
	rows := [][]string{head, a, b}
	return csvOf(append(rows, extra...)...)
}

var (
	goodDetails = csvOf([]string{"UCC", "Address1", "Address2", "City", "State", "PIN", "Country", "Gender", "Guardian", "Marital", "IncomeAsOn", "AgreementDate", "IPV", "PermPIN"},
		[]string{"TA001", "1 Test Road", "Test Nagar", "Pune", "Maharashtra", "411001", "", "M", "Test Parent", "Married", "2025-04-01", "2020-01-02", "y", ""},
		[]string{"TB002", "2 Test Road", "", "Pune", "Maharashtra", "411002", "India", "", "", "", "", "", "", "411003"})
	goodContacts = csvOf([]string{"UCC", "Name", "Designation", "PAN", "Address", "Phone", "Email", "DIN"},
		[]string{"TB002", "Test Director One", "Director", "ABCPF1234F", "", "", "d1@example.com", "00000001"},
		[]string{"TB002", "Test Director Two", "Director", "", "", "", "", ""})
	goodDP = csvOf([]string{"UCC", "BOID", "Depository", "Status", "Opened", "ClosedOn", "BSDA", "Freeze", "FreezeReason", "DDPI"},
		[]string{"TA001", "1234567800000001", "CDSL", "Active", "2020-01-01", "", "N", "", "", "Y"},
		[]string{"TB002", "IN30000011112222", "NSDL", "Frozen", "2020-01-01", "", "", "Debit", "Made-up court order", ""},
		[]string{"TA001", "1234567800000011", "CDSL", "Closed", "2019-01-01", "2021-06-30", "", "", "", ""}) // a second, closed account of TA001
)

func loadAll(t *testing.T, o *Ops, ctx context.Context, files [][2]string, data map[string][]byte) map[string]ingest.Result {
	t.Helper()
	out := map[string]ingest.Result{}
	for _, f := range files {
		r, err := o.LoadFile(ctx, MT, "adm.one", f[0], "", f[1], data[f[0]])
		if err != nil {
			t.Fatalf("%s: %v %v", f[0], err, r.Errors)
		}
		out[f[0]] = r
	}
	return out
}

func TestImportOfficesPartnersDetailsDP(t *testing.T) {
	o, ctx := migSetup(t)
	data := map[string][]byte{"BRANCHES": goodBranches, "PARTNERS": goodPartners, "CLIENT-MASTER": goodClients(), "UCC-DETAILS": goodDetails, "UCC-CONTACTS": goodContacts, "DP-ACCOUNTS": goodDP}
	order := [][2]string{{"BRANCHES", "b.csv"}, {"PARTNERS", "p.csv"}, {"CLIENT-MASTER", "c.csv"}, {"UCC-DETAILS", "d.csv"}, {"UCC-CONTACTS", "k.csv"}, {"DP-ACCOUNTS", "dp.csv"}}
	// The intake order puts them where they depend on each other.
	ord := strings.Join(intakeOrder, " ")
	for i := 1; i < len(order); i++ {
		if strings.Index(ord, order[i-1][0]) > strings.Index(ord, order[i][0]) {
			t.Fatalf("intake order %v", intakeOrder)
		}
	}
	res := loadAll(t, o, ctx, order, data)
	bs, _ := o.Branches(ctx, MT)
	if len(bs) != 3 {
		t.Fatalf("branches %+v", bs)
	}
	for _, b := range bs {
		if b.Code == "PUN" && (b.Parent != "WEST" || b.Kind != "Branch" || !b.Active) || b.Code == "WEST" && b.Kind != "Zone" || b.Code == "OLD" && b.Active {
			t.Fatalf("branch %+v", b)
		}
	}
	p2, _, _ := docs.Get[Partner](ctx, o.Docs, MT, cPartner, "AP02")
	if p2.Kind != "Authorised person" || p2.Parent != "AP01" || p2.SharePct.String() != "25.5" || !p2.Active {
		t.Fatalf("partner %+v", p2)
	}
	a, _ := o.Client(ctx, MT, "TA001")
	b, _ := o.Client(ctx, MT, "TB002")
	if a.Partner != "AP02" || a.RM != "RM01" || strings.Join(a.Tags, ",") != "HNI,STAFF" || a.Inactive || !b.Inactive || b.InactiveSince != "2025-03-31" {
		t.Fatalf("clients %+v / %+v", a, b)
	}
	d := o.UCCDetailsFor(ctx, MT, "TA001")
	if d.Address1 != "1 Test Road" || d.PIN != "411001" || d.Guardian != "Test Parent" || d.IPV != "Y" || d.UpdatedBy != "adm.one" {
		t.Fatalf("details %+v", d)
	}
	db := o.UCCDetailsFor(ctx, MT, "TB002")
	if len(db.Contacts) != 2 || db.Contacts[0].DIN != "00000001" || db.PermPIN != "411003" {
		t.Fatalf("TB002 details %+v", db)
	}
	bo, _ := o.boAccount(ctx, MT, "IN30000011112222")
	if bo.UCC != "TB002" || bo.Status != "Frozen" || bo.Freeze != "Debit" || bo.BOID != "IN30000011112222" || bo.Depository != "NSDL" {
		t.Fatalf("bo %+v", bo)
	}
	if ba, _ := o.boAccount(ctx, MT, "1234567800000001"); ba.UCC != "TA001" || !ba.DDPI || ba.BSDA {
		t.Fatalf("bo %+v", ba)
	}
	// TA001 has two accounts with the DP, the closed one kept for the record.
	if bs := o.boAccountsOf(ctx, MT, "TA001"); len(bs) != 2 || bs[0].BOID != "1234567800000001" || bs[1].Status != "Closed" {
		t.Fatalf("TA001's accounts %+v", bs)
	}
	if a, _ := o.Client(ctx, MT, "TA001"); a.Demat.ID != "1234567800000001" || len(a.Demats) != 2 || a.Demats[1].Status != DematClosed || !a.Demats[1].OwnDP {
		t.Fatalf("TA001's demats %+v %+v", a.Demat, a.Demats)
	}
	if w := res["DP-ACCOUNTS"].Warnings; len(w) != 1 || w[0].Line != 3 {
		t.Fatalf("DP warnings %+v", w) // TB002's demat differs from the client master
	}
	// A blank cell keeps what is stored; a blank AP keeps the partner;
	// groups add up.
	if _, err := o.LoadFile(ctx, MT, "adm.one", "UCC-DETAILS", "", "d2.csv", csvOf([]string{"UCC", "Address1", "City", "State", "PIN", "Guardian"}, []string{"TA001", "", "", "", "", "New Test Parent"})); err != nil {
		t.Fatal(err)
	}
	if d := o.UCCDetailsFor(ctx, MT, "TA001"); d.Address1 != "1 Test Road" || d.Guardian != "New Test Parent" {
		t.Fatalf("blank cells %+v", d)
	}
	head := append(append([]string{}, cmCols...), "AP", "Groups")
	if _, err := o.LoadFile(ctx, MT, "adm.one", "CLIENT-MASTER", "", "c2.csv", csvOf(head, append(cmRow("TA001", "Test Client A", "ABCPA1234A", "9876500001"), "", "NRI"))); err != nil {
		t.Fatal(err)
	}
	if a, _ := o.Client(ctx, MT, "TA001"); a.Partner != "AP02" || a.RM != "RM01" || strings.Join(a.Tags, ",") != "HNI,STAFF,NRI" {
		t.Fatalf("re-import %+v", a)
	}
	// Loading the same files again changes nothing.
	for _, f := range [][2]string{{"BRANCHES", "b.csv"}, {"PARTNERS", "p.csv"}, {"DP-ACCOUNTS", "dp.csv"}} {
		r, err := o.LoadFile(ctx, MT, "adm.one", f[0], "", f[1], data[f[0]])
		if err != nil || r.Added != 0 {
			t.Fatalf("%s again: %v added %d", f[0], err, r.Added)
		}
	}
	// Columns a file carries only for people reading it are ignored.
	if _, err := o.LoadFile(ctx, MT, "adm.one", "OPENING-BALANCES", "2026-10-30", "ob.csv", csvOf([]string{"Account", "Name", "Debit", "Credit"},
		[]string{"CLIENT:TA001", "Test Client A", "10.00", ""}, []string{"BANK:TESTBANK", "Test Bank", "", "10.00"})); err != nil {
		t.Fatalf("an extra Name column: %v", err)
	}
	// Detected by their columns (and the file name, where columns overlap).
	if s := o.DetectSource("branches.csv", csvOf([]string{"Code", "Name", "Kind"})); s != "BRANCHES" {
		t.Fatalf("branches detected as %s", s)
	}
	if s := o.DetectSource("ap-master.csv", goodPartners); s != "PARTNERS" {
		t.Fatalf("partners detected as %s", s)
	}
	if s := o.DetectSource("x.csv", goodDP); s != "DP-ACCOUNTS" {
		t.Fatalf("dp detected as %s", s)
	}
}

type wantErr struct {
	line  int
	field string
}

func checkErrs(t *testing.T, what string, got []ingest.ParseError, want []wantErr) {
	t.Helper()
	have := map[string]bool{}
	for _, e := range got {
		have[fmt.Sprintf("%d %s", e.Line, strings.ToLower(e.Field))] = true
	}
	for _, w := range want {
		if !have[fmt.Sprintf("%d %s", w.line, strings.ToLower(w.field))] {
			t.Errorf("%s: no error at line %d field %s; got %+v", what, w.line, w.field, got)
		}
	}
}

func TestImportBadRowsAllReported(t *testing.T) {
	o, ctx := migSetup(t)
	loadAll(t, o, ctx, [][2]string{{"BRANCHES", "b.csv"}, {"PARTNERS", "p.csv"}, {"CLIENT-MASTER", "c.csv"}},
		map[string][]byte{"BRANCHES": goodBranches, "PARTNERS": goodPartners, "CLIENT-MASTER": goodClients()})
	cases := []struct {
		src  string
		data []byte
		want []wantErr
	}{
		{"BRANCHES", csvOf([]string{"Code", "Name", "Parent", "Kind", "Active"},
			[]string{"", "No Code", "", "", ""}, []string{"B1", "Test B1", "NOPE", "", ""}, []string{"B2", "Test B2", "", "Planet", ""}, []string{"B3", "Test B3", "", "", "maybe"},
			[]string{"B4", "Test B4", "B5", "", ""}, []string{"B5", "Test B5", "B4", "", ""}, []string{"B3", "Test B3 again", "", "", ""}),
			[]wantErr{{2, "Code"}, {3, "Parent"}, {4, "Kind"}, {5, "Active"}, {6, "Parent"}, {7, "Parent"}, {8, "Code"}}},
		{"PARTNERS", csvOf([]string{"Code", "Name", "Kind", "PAN", "GSTIN", "SharePct", "IFSC", "Account", "Email", "Parent"},
			[]string{"X", "Short Code", "Authorised person", "", "", "", "", "", "", ""}, []string{"AP09", "Test", "Franchisee", "", "", "", "", "", "", ""},
			[]string{"AP10", "Test", "Authorised person", "BAD", "BADGST", "120", "BAD", "12", "nope", ""}, []string{"AP11", "Test", "Authorised person", "", "", "", "", "", "", "AP12"},
			[]string{"AP12", "Test", "Authorised person", "", "", "", "", "", "", "AP11"}),
			[]wantErr{{2, "Code"}, {3, "Kind"}, {4, "PAN"}, {4, "GSTIN"}, {4, "SharePct"}, {4, "IFSC"}, {4, "Account"}, {4, "Email"}, {5, "Parent"}, {6, "Parent"}}},
		{"CLIENT-MASTER", goodClients(append(cmRow("TC003", "Test Client C", "ABCPC1234C", "9876500003"), "", "NOPE", "", "bad group!", "X", "2025-13-01")),
			[]wantErr{{4, "AP"}, {4, "Groups"}, {4, "Inactive"}, {4, "InactiveSince"}}},
		{"UCC-DETAILS", csvOf([]string{"UCC", "Address1", "City", "State", "PIN", "Country", "IncomeAsOn", "AgreementDate"},
			[]string{"TZ999", "1 Road", "Pune", "MH", "411001", "", "", ""}, []string{"TA001", "", "Pune", "MH", "4110", "", "2025-02-30", "someday"},
			[]string{"TA001", "1 Road", "Pune", "MH", "411001", "", "", ""}, []string{"TB002", "1 Road", "London", "LDN", "SW1A1AA", "UK", "", ""}),
			[]wantErr{{2, "UCC"}, {3, "Address1"}, {3, "PIN"}, {3, "IncomeAsOn"}, {3, "AgreementDate"}, {4, "UCC"}}},
		{"UCC-CONTACTS", csvOf([]string{"UCC", "Name", "Designation", "PAN", "Email"},
			[]string{"TZ999", "Test", "Director", "", ""}, []string{"TA001", "", "", "BADPAN", "x"},
			[]string{"TB002", "D1", "Director", "", ""}, []string{"TB002", "D2", "Director", "", ""}, []string{"TB002", "D3", "Director", "", ""},
			[]string{"TB002", "D4", "Director", "", ""}, []string{"TB002", "D5", "Director", "", ""}, []string{"TB002", "D6", "Director", "", ""}),
			[]wantErr{{2, "UCC"}, {3, "Name"}, {3, "Designation"}, {3, "PAN"}, {3, "Email"}, {9, "UCC"}}},
		{"DP-ACCOUNTS", csvOf([]string{"UCC", "BOID", "Depository", "Status", "Opened", "ClosedOn", "BSDA", "Freeze", "FreezeReason", "DDPI"},
			[]string{"TA001", "12345", "CDSL", "Open", "01/01/2020", "", "maybe", "", "", ""}, []string{"TB002", "1234567800000002", "XDSL", "Frozen", "", "", "", "", "", "Q"},
			[]string{"TB002", "1234567800000003", "CDSL", "Active", "", "2025-01-01", "", "Debit", "x", ""}, []string{"TZ999", "1234567800000099", "CDSL", "Active", "", "", "", "", "", ""}),
			[]wantErr{{2, "BOID"}, {2, "Status"}, {2, "Opened"}, {2, "BSDA"}, {3, "Depository"}, {3, "Freeze"}, {3, "DDPI"}, {4, "Freeze"}, {4, "ClosedOn"}, {5, "UCC"}}},
	}
	for _, c := range cases {
		r, err := o.LoadFile(ctx, MT, "adm.one", c.src, "", "bad.csv", c.data)
		if err == nil {
			t.Errorf("%s with bad rows loaded", c.src)
		}
		checkErrs(t, c.src, r.Errors, c.want)
		// The dry run reports the same.
		dr, _ := o.DryRunFiles(ctx, MT, "adm.one", []DryFile{{Name: "bad.csv", Source: c.src, Data: c.data}})
		checkErrs(t, c.src+" dry run", dr[0].Errors, c.want)
	}
	// Nothing of the bad files was stored.
	if bs, _ := o.Branches(ctx, MT); len(bs) != 3 {
		t.Fatalf("branches %d", len(bs))
	}
	if _, _, err := docs.Get[Partner](ctx, o.Docs, MT, cPartner, "AP10"); err == nil {
		t.Fatal("bad partner stored")
	}
}

func TestImportDryRunChangesNothing(t *testing.T) {
	o, ctx := migSetup(t)
	files := []DryFile{{Name: "b", Source: "BRANCHES", Data: goodBranches}, {Name: "p", Source: "PARTNERS", Data: goodPartners}, {Name: "c", Source: "CLIENT-MASTER", Data: goodClients()},
		{Name: "d", Source: "UCC-DETAILS", Data: goodDetails}, {Name: "k", Source: "UCC-CONTACTS", Data: goodContacts}, {Name: "dp", Source: "DP-ACCOUNTS", Data: goodDP}}
	res, err := o.DryRunFiles(ctx, MT, "adm.one", files)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		for _, e := range r.Errors {
			if !e.Warning {
				t.Errorf("%s: %+v", r.Source, e) // later files see what earlier ones would load
			}
		}
	}
	if n := len(res[5].Errors); n != 1 || !res[5].Errors[0].Warning {
		t.Fatalf("dp warnings %+v", res[5].Errors)
	}
	if bs, _ := o.Branches(ctx, MT); len(bs) != 0 {
		t.Fatal("dry run stored branches")
	}
	for _, c := range []string{cPartner, cClient, cUCCDetails, cBO} {
		if ds, _ := o.Docs.List(ctx, MT, c, ""); len(ds) != 0 {
			t.Fatalf("dry run stored %s", c)
		}
	}
}
