package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ledgerline"
	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
	"ledgerline/internal/vault"
)

// Made-up firm and clients only.

func init() { pkg.SetTestCost() }

const (
	MT    = "mig"
	mPass = "test passphrase 123"
	mAsOf = "2026-10-30"
)

func migSetup(t *testing.T) (*Ops, context.Context) {
	t.Helper()
	ctx := context.Background()
	reg, err := ingest.LoadFS(ledgerline.FS, "config/adapters")
	if err != nil {
		t.Fatal(err)
	}
	f, _ := ledgerline.FS.Open("config/charges/sample-schedule.json")
	sched, err := charges.LoadSchedule(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := New(a)
	o.WorkDir, o.MigrateSync = t.TempDir(), true
	for _, u := range []User{{ID: "adm.one", Name: "Admin One", Role: RoleAdmin, Active: true}, {ID: "adm.two", Name: "Admin Two", Role: RoleAdmin, Active: true},
		{ID: "chk.one", Name: "Checker One", Role: RoleChecker, Active: true}, {ID: "comp.one", Name: "Compliance One", Role: RoleCompliance, Active: true},
		{ID: "mkr.one", Name: "Maker One", Role: RoleMaker, Active: true},
		{ID: "client.a", Name: "Client A", Role: RoleClient, Active: true, Clients: []string{"TA001"}}, {ID: "client.b", Name: "Client B", Role: RoleClient, Active: true, Clients: []string{"TB002"}}} {
		if err := o.PutUser(ctx, MT, "adm.one", u); err != nil {
			t.Fatal(err)
		}
	}
	return o, ctx
}

var cmCols = []string{"UCC", "Name", "Type", "PAN", "DOB", "Mobile", "Email", "City", "Segments", "Risk", "Income", "RAS", "Bank", "IFSC", "Account", "Depository", "DematID", "Nominee", "NomineeName", "KYCDate", "Status", "API", "LastTrade", "UPIBlock"}

func cmRow(ucc, name, pan, mobile string) []string {
	return []string{ucc, name, "Individual", pan, "1980-01-01", mobile, strings.ToLower(ucc) + "@example.com", "Pune", "CM", "Low", "5-10L", "Quarterly", "Test Bank", "TEST0123456", "123456789012", "CDSL", "12345678000000" + ucc[len(ucc)-2:], "", "", "2020-01-01", "Active", "N", "", ""}
}

type mspec struct {
	part     *pkg.Part
	masters  bool
	history  bool
	docs     bool
	bad      bool // client master and banks with errors
	checks   []pkg.Check
	bigB     int // extra ledger lines for TB002
	tbDebitX string
}

func writeCSV(t *testing.T, w *pkg.Writer, firm, src string, rows [][]string) {
	t.Helper()
	c, err := w.Master(firm, src, rows[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows[1:] {
		c.Write(r)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func mkPkg(t *testing.T, s mspec) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := pkg.Create(&buf, mPass, "test tool")
	if err != nil {
		t.Fatal(err)
	}
	m := w.Manifest()
	m.Part = s.part
	m.Source = pkg.Source{System: "SharePro", Backups: []pkg.Backup{{Database: "TEST2627", Role: "company", FY: "2026-27", Used: true}}}
	m.Firms = []pkg.Firm{{Code: "01", Name: "Test Broking", Years: []string{"2024-25", "2025-26", "2026-27"}, AsOf: mAsOf}, {Code: "02", Name: "Other Test Firm"}}
	if s.masters {
		cm := [][]string{cmCols, cmRow("TA001", "Test Client A", "ABCPA1234A", "9876500001"), cmRow("TB002", "Test Client B", "ABCPB1234B", "9876500002")}
		banks := [][]string{{"UCC", "Bank", "IFSC", "Account", "Primary"}, {"TA001", "Test Bank", "TEST0123456", "123456789012", "Y"}, {"TB002", "Test Bank", "TEST0123456", "223456789012", "Y"}}
		if s.bad {
			cm = append(cm, cmRow("TC003", "Test Client C", "BADPAN", "9876500003"), cmRow("TD004", "Test Client D", "ABCPD1234D", "12345"), cmRow("TA001", "Test Client A again", "ABCPA1234A", "9876500001"))
			banks = append(banks, []string{"TZ999", "Test Bank", "TEST0123456", "323456789012", "Y"}, []string{"TB002", "Test Bank", "BADIFSC", "423456789012", "N"})
		}
		writeCSV(t, w, "01", "CLIENT-MASTER", cm)
		writeCSV(t, w, "01", "CLIENT-BANKS", banks)
		writeCSV(t, w, "01", "BROKERAGE-CARDS", [][]string{{"Card", "From", "Product", "Mode", "Pct", "Group"}, {"STD", "2026-04-01", "CM-DEL", "pct", "0.3", ""}, {"STD", "2026-04-01", "CM-DEL", "pct", "0.1", "ETF"}})
		writeCSV(t, w, "01", "SCRIP-GROUPS", [][]string{{"Group", "Scrip"}, {"ETF", "NIFTYBEES"}})
		ob := [][]string{{"Account", "Debit", "Credit"}, {"CLIENT:TA001", "1500.00", ""}, {"CLIENT:TB002", "", "2500.00"}, {"BANK:TESTBANK", "1000.00", ""}}
		if s.bad {
			ob = append(ob, []string{"CLIENT:TA001", "10.00", ""})
		}
		writeCSV(t, w, "01", "OPENING-BALANCES", ob)
		writeCSV(t, w, "01", "BRANCHES", [][]string{{"Code", "Name", "Parent"}, {"PUN", "Test Pune Branch", "WEST"}, {"WEST", "Test West Zone", ""}})
		writeCSV(t, w, "01", "DP-ACCOUNTS", [][]string{{"UCC", "BOID", "Depository", "Status"}, {"TA001", "1234567800000001", "CDSL", "Active"}, {"TB002", "1234567800000002", "CDSL", "Active"}})
		writeCSV(t, w, "01", "DP-HOLDINGS", [][]string{{"UCC", "ISIN", "Symbol", "Qty", "Pledged", "AvgCost"}, {"TA001", "INE000A01010", "TESTCO", "100", "0", "10.50"}, {"TB002", "INE000B01010", "TESTB", "50", "0", "20"}})
		writeCSV(t, w, "01", "DP-TARIFFS", [][]string{{"Plan", "From", "Code", "Name", "AMCBilling", "Flat", "Min"}, {"B001", "2020-08-01", "AM", "Standard", "Yearly", "200", ""},
			{"B001", "2026-04-01", "AM", "Standard", "Yearly", "300", ""}, {"B001", "2026-04-01", "ODTA", "", "", "", "15"}})
		writeCSV(t, w, "01", "DP-TARIFF-ACCOUNTS", [][]string{{"BOID", "Plan", "From", "AMCPaidUpto"}, {"1234567800000001", "B001", "2021-01-01", "2027-03-31"}, {"1234567800000002", "B001", "2026-11-01", ""}})
	}
	if s.history {
		h, _ := w.History("01", "2024-25", pkg.HistoryLedger, pkg.HistoryColumns[pkg.HistoryLedger])
		h.Write([]string{"2024-04-01", "OP", "OB1", "CLIENT:TA001", "Test Client A", "TA001", "", "Opening balance", "200.00", "", "", "", "LED:1"})
		h.Write([]string{"2024-06-10", "BILL", "B1", "CLIENT:TA001", "Test Client A", "TA001", "CM", "Bill", "300.00", "", "", "", "LED:2"})
		h.Write([]string{"2024-06-12", "RCPT", "R1", "CLIENT:TA001", "Test Client A", "TA001", "", "Receipt", "", "100.00", "C1", "", "LED:3"})
		h.Close()
		h, _ = w.History("01", "2025-26", pkg.HistoryLedger, pkg.HistoryColumns[pkg.HistoryLedger])
		h.Write([]string{"2025-05-02", "BILL", "B9", "CLIENT:TA001", "Test Client A", "TA001", "CM", "Bill 2 May", "1000.00", "", "", "", "LED:9"})
		h.Write([]string{"2025-05-03", "JV", "J1", "BANK:TESTBANK", "Test Bank", "", "", "Bank charges", "", "5.00", "", "", "LED:10"})
		for i := 0; i < s.bigB; i++ {
			h.Write([]string{"2025-05-03", "BILL", fmt.Sprintf("BB%d", i), "CLIENT:TB002", "Test Client B", "TB002", "CM", "Bill", "1.00", "", "", "", "LED:B"})
		}
		h.Write([]string{"2025-04-20", "RCPT", "R9", "", "Test Client A", "TA001", "", "Receipt", "", "400.00", "C9", "", "LED:11"})
		h.Close()
		cn, _ := w.History("01", "2025-26", pkg.HistoryContractNotes, pkg.HistoryColumns[pkg.HistoryContractNotes])
		cn.Write([]string{"CN/1", "2025-05-02", "TA001", "NSE", "CM", "2025-05-02", "ta001@example.com", "Y", "CN-1", "CN:1"})
		cn.Write([]string{"CN/2", "2025-05-02", "TB002", "NSE", "CM", "2025-05-02", "tb002@example.com", "Y", "CN-2", "CN:2"})
		cn.Close()
	}
	if s.docs {
		d := time.Date(2025, 5, 2, 0, 0, 0, 0, time.UTC)
		for _, x := range []pkg.DocMeta{{ID: "CN-1", UCC: "TA001", Type: pkg.DocContractNote, Date: d, FY: "2025-26", Signed: true, Format: "pdf"},
			{ID: "CN-2", UCC: "TB002", Type: pkg.DocContractNote, Date: d, FY: "2025-26", Signed: true, Format: "pdf"},
			{ID: "ST-OLD", UCC: "TA001", Type: pkg.DocStatement, Date: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), FY: "2024-25", Format: "pdf", RetainUntil: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}} {
			if err := w.AddDocument("01", x, []byte("%PDF-1.4 made-up document "+x.ID)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, c := range s.checks {
		w.AddCheck(c)
	}
	w.AddDropped("01", "Client web password", "never migrated", 2)
	w.AddIssue(pkg.Issue{Firm: "01", Severity: "Warning", Area: "Client master", Key: "TA001", Message: "made-up warning", Action: "none"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func upload(t *testing.T, o *Ops, ctx context.Context, data []byte, name string) MigPackage {
	t.Helper()
	p, err := o.CreateMigUpload(ctx, MT, "adm.one", name, int64(len(data)), 4096)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < p.Chunks; n++ {
		end := min((n+1)*4096, len(data))
		if _, err := o.PutMigChunk(ctx, MT, "adm.one", p.ID, n, bytes.NewReader(data[n*4096:end]), ""); err != nil {
			t.Fatal(err)
		}
	}
	p, err = o.CompleteMigUpload(ctx, MT, "adm.one", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func checked(t *testing.T, o *Ops, ctx context.Context, data []byte, name string) MigPackage {
	t.Helper()
	p := upload(t, o, ctx, data, name)
	p, err := o.CheckMigPackage(ctx, MT, "adm.one", p.ID, mPass, "01")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != MigChecked {
		t.Fatalf("%s: %s %+v", name, p.Status, p.Check)
	}
	return p
}

func TestMigChunkedUploadResume(t *testing.T) {
	o, ctx := migSetup(t)
	data := mkPkg(t, mspec{masters: true, history: true, bigB: 3000})
	p, err := o.CreateMigUpload(ctx, MT, "adm.one", "office.llmig", int64(len(data)), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.CreateMigUpload(ctx, MT, "chk.one", "office.llmig", 10, 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an Ops checker started an upload: %v", err)
	}
	if _, err := o.CreateMigUpload(ctx, MT, "adm.one", "office.zip", 10, 0); err == nil {
		t.Fatal("accepted a .zip")
	}
	if _, err := o.CreateMigUpload(ctx, MT, "adm.one", "huge.llmig", 101<<30, 0); err == nil {
		t.Fatal("accepted a package over the limit")
	}
	if p.Chunks < 3 {
		t.Fatalf("chunks %d", p.Chunks)
	}
	chunk := func(n int) []byte { return data[n*1024 : min((n+1)*1024, len(data))] }
	// Send every other chunk, out of order, then stop (the connection drops).
	for n := p.Chunks - 1; n >= 0; n -= 2 {
		if _, err := o.PutMigChunk(ctx, MT, "adm.one", p.ID, n, bytes.NewReader(chunk(n)), ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.CompleteMigUpload(ctx, MT, "adm.one", p.ID); err == nil {
		t.Fatal("completed with chunks missing")
	}
	// Resume: the server says what is missing.
	p, _, _ = docs.Get[MigPackage](ctx, o.Docs, MT, cMigPkg, p.ID)
	missing := p.Missing()
	if len(missing) == 0 || len(missing) >= p.Chunks {
		t.Fatalf("missing %v of %d", missing, p.Chunks)
	}
	if _, err := o.PutMigChunk(ctx, MT, "adm.one", p.ID, missing[0], bytes.NewReader(chunk(missing[0])[:100]), ""); err == nil {
		t.Fatal("accepted a short chunk")
	}
	bad := sha256.Sum256([]byte("x"))
	if _, err := o.PutMigChunk(ctx, MT, "adm.one", p.ID, missing[0], bytes.NewReader(chunk(missing[0])), hex.EncodeToString(bad[:])); err == nil {
		t.Fatal("accepted a chunk with the wrong checksum")
	}
	for _, n := range missing {
		sum := sha256.Sum256(chunk(n))
		if _, err := o.PutMigChunk(ctx, MT, "adm.one", p.ID, n, bytes.NewReader(chunk(n)), hex.EncodeToString(sum[:])); err != nil {
			t.Fatal(err)
		}
	}
	p, err = o.CompleteMigUpload(ctx, MT, "adm.one", p.ID)
	if err != nil || p.Status != MigUploaded {
		t.Fatalf("complete: %v %s", err, p.Status)
	}
	whole := sha256.Sum256(data)
	if p.SHA256 != hex.EncodeToString(whole[:]) {
		t.Fatal("the file on disk differs from what was sent")
	}
	// Wrong passphrase: refused at once, nothing changes.
	if _, err := o.CheckMigPackage(ctx, MT, "adm.one", p.ID, "not the passphrase", "01"); err == nil || !strings.Contains(err.Error(), "passphrase is wrong") {
		t.Fatalf("wrong passphrase: %v", err)
	}
	p, err = o.CheckMigPackage(ctx, MT, "adm.one", p.ID, mPass, "01")
	if err != nil || p.Status != MigChecked || p.Check.Errors != 0 {
		t.Fatalf("check: %v %s %+v", err, p.Status, p.Check)
	}
	if p.Manifest.Source.System != "SharePro" || len(p.Manifest.Dropped) != 1 || len(p.Manifest.Issues) != 1 {
		t.Fatalf("manifest %+v", p.Manifest)
	}
	// The passphrase is in no stored document or log.
	all, _ := o.App.Audit.List(ctx, MT, 1000)
	for _, e := range all {
		if strings.Contains(fmt.Sprint(e.Detail), mPass) {
			t.Fatal("passphrase in the audit trail")
		}
	}
	// The decrypted copy is gone after the check.
	d, _ := o.migDir(MT)
	if left, _ := filepath.Glob(d + "/tmp/*"); len(left) > 0 {
		t.Fatalf("plaintext left behind: %v", left)
	}
}

func TestMigDryRunReportsEveryError(t *testing.T) {
	o, ctx := migSetup(t)
	p := upload(t, o, ctx, mkPkg(t, mspec{masters: true, bad: true}), "bad.llmig")
	p, err := o.CheckMigPackage(ctx, MT, "adm.one", p.ID, mPass, "01")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != MigChecked {
		t.Fatalf("status %s %+v", p.Status, p.Check)
	}
	errs, n, err := o.MigErrors(ctx, MT, "adm.one", p.ID, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	type k struct {
		file  string
		line  int
		field string
	}
	got := map[k]bool{}
	for _, e := range errs {
		got[k{e.File[strings.LastIndex(e.File, "/")+1:], e.Line, strings.ToLower(e.Field)}] = true
		if e.Action == "" {
			t.Errorf("no action for %+v", e)
		}
	}
	for _, want := range []k{{"CLIENT-MASTER.csv", 4, "pan"}, {"CLIENT-MASTER.csv", 5, "mobile"}, {"CLIENT-MASTER.csv", 6, "ucc"},
		{"CLIENT-BANKS.csv", 4, "ucc"}, {"OPENING-BALANCES.csv", 0, ""}} {
		if !got[want] {
			t.Errorf("missing error %+v; got %v", want, errs)
		}
	}
	if n != len(errs) || p.Check.Errors < 5 {
		t.Fatalf("count %d / %d / %d", n, len(errs), p.Check.Errors)
	}
	// Nothing was saved.
	if cs, _ := o.Clients(ctx, MT); len(cs) != 0 {
		t.Fatalf("dry run saved %d clients", len(cs))
	}
	if es, _ := o.App.Ledger.Store.List(ctx, MT, ledger.Filter{}); len(es) != 0 {
		t.Fatal("dry run posted to the ledger")
	}
	// A file with errors cannot be proposed.
	if _, err := o.ProposeMigration(ctx, MT, "adm.one", MigProposal{Package: p.ID, Firm: "01"}); err == nil || !strings.Contains(err.Error(), "errors in") {
		t.Fatalf("proposed a file with errors: %v", err)
	}
	var b bytes.Buffer
	if err := o.MigErrorsCSV(ctx, MT, "adm.one", p.ID, &b); err != nil || strings.Count(b.String(), "\n") != n+1 {
		t.Fatalf("csv %v %d lines for %d errors", err, strings.Count(b.String(), "\n"), n)
	}
}

func goodChecks(tbDebit, docs string) []pkg.Check {
	return []pkg.Check{{Firm: "01", Name: pkg.CheckTBDebit, Value: tbDebit}, {Firm: "01", Name: pkg.CheckTBCredit, Value: "2500.00"}, {Firm: "01", Name: pkg.CheckClients, Value: "2"},
		{Firm: "01", Name: pkg.CheckClientDebit, Value: "1,500.00"}, {Firm: "01", Name: pkg.CheckClientCredit, Value: "2500.00"}, {Firm: "01", Name: pkg.CheckDPAccounts, Value: "2"},
		{Firm: "01", Name: pkg.CheckDPQuantity, Value: "150"}, {Firm: "01", Name: pkg.CheckDPTariffVersions, Value: "2"},
		{Firm: "01", Name: pkg.CheckDPTariffAccounts, Value: "1"}, {Firm: "01", Name: pkg.CheckDPAMCPaidAhead, Value: "1"}, {Firm: "01", Name: pkg.CheckBankAccounts, Value: "2"}, {Firm: "01", Name: "brokerage-total", Value: "99"},
		{Firm: "02", Name: pkg.CheckClients, Value: "7"}}
}

func TestMigLoadReconcileHistoryArchiveUndo(t *testing.T) {
	o, ctx := migSetup(t)
	part := func(i int) *pkg.Part {
		return &pkg.Part{ID: "P-TEST-1", Index: i, Of: 2, Note: fmt.Sprintf("part %d", i)}
	}
	p1 := checked(t, o, ctx, mkPkg(t, mspec{part: part(1), masters: true, history: true, bigB: 4500, checks: goodChecks("2500.00", "")}), "office-1.llmig")
	// Part 2 is not here yet.
	if _, err := o.ProposeMigration(ctx, MT, "adm.one", MigProposal{Package: p1.ID, Firm: "01"}); err == nil || !strings.Contains(err.Error(), "part 2 of 2") {
		t.Fatalf("proposed without part 2: %v", err)
	}
	checked(t, o, ctx, mkPkg(t, mspec{part: part(2), docs: true, checks: []pkg.Check{{Firm: "01", Name: pkg.CheckDocuments, Value: "3"}}}), "office-2.llmig")
	v, _ := o.MigrationView(ctx, MT, "adm.one")
	if len(v.Packages) != 2 || len(v.Packages[0].Parts) != 2 || v.Packages[0].Parts[0].Package == "" || v.Packages[0].Parts[1].Package == "" {
		t.Fatalf("parts %+v", v.Packages)
	}
	if _, err := o.ProposeMigration(ctx, MT, "mkr.one", MigProposal{Package: p1.ID, Firm: "01"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an Ops maker proposed: %v", err)
	}
	m, err := o.ProposeMigration(ctx, MT, "adm.one", MigProposal{Package: p1.ID, Firm: "01"})
	if err != nil {
		t.Fatal(err)
	}
	if m.AsOf != mAsOf || len(m.Files) == 0 {
		t.Fatalf("proposal %+v", m)
	}
	// The maker cannot approve; a wrong passphrase starts nothing.
	if _, err := o.DecideMigration(ctx, MT, "adm.one", m.ID, true, mPass, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker approved: %v", err)
	}
	if _, err := o.DecideMigration(ctx, MT, "chk.one", m.ID, true, "wrong wrong wrong", ""); err == nil {
		t.Fatal("approved with a wrong passphrase")
	}
	m, err = o.DecideMigration(ctx, MT, "chk.one", m.ID, true, mPass, "checked against the old trial balance")
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != MigLoaded {
		t.Fatalf("status %s: %s %+v", m.Status, m.Error, m.Steps)
	}
	// Load order: masters by dependency, then history, then documents.
	var order []string
	for _, s := range m.Steps {
		order = append(order, firstOf(s.Source, s.Kind))
		if s.Status != "Done" {
			t.Fatalf("step %+v", s)
		}
	}
	want := "BRANCHES CLIENT-MASTER CLIENT-BANKS SCRIP-GROUPS BROKERAGE-CARDS DP-ACCOUNTS DP-TARIFFS DP-TARIFF-ACCOUNTS OPENING-BALANCES DP-HOLDINGS LEDGER LEDGER CONTRACT-NOTES documents"
	if strings.Join(order, " ") != want {
		t.Fatalf("order %v", order)
	}
	if cs, _ := o.Clients(ctx, MT); len(cs) != 2 {
		t.Fatalf("clients %d", len(cs))
	}
	// Reconciliation: every known check passes; the unknown one is shown.
	rc := m.Recon
	if rc == nil || rc.Failed != 0 || rc.Passed != 12 || rc.Unknown != 1 {
		t.Fatalf("recon %+v", rc)
	}
	for _, r := range rc.Rows {
		if r.Name == pkg.CheckClients && r.Expected == "7" {
			t.Fatal("another firm's check was used")
		}
	}
	// The same package (by Part.ID) cannot be loaded twice.
	again := checked(t, o, ctx, mkPkg(t, mspec{part: part(1), masters: true}), "office-1-again.llmig")
	if _, err := o.ProposeMigration(ctx, MT, "adm.one", MigProposal{Package: again.ID, Firm: "01"}); err == nil || !strings.Contains(err.Error(), "already loaded") {
		t.Fatalf("second load: %v", err)
	}

	// History: the earlier year's ledger with opening and running balance.
	st, err := o.HistoryStatement(ctx, MT, "chk.one", "TA001", "2025-26", false)
	if err != nil {
		t.Fatal(err)
	}
	if st.Opening != money.MustAmount("400.00") || len(st.Lines) != 2 || st.Lines[0].Date != "2025-04-20" || st.Lines[0].Balance != money.MustAmount("0.00") ||
		st.Lines[1].Balance != money.MustAmount("1000.00") || st.Closing != money.MustAmount("1000.00") || !strings.Contains(st.Label, "SharePro") {
		t.Fatalf("statement %+v", st)
	}
	st24, _ := o.HistoryStatement(ctx, MT, "chk.one", "TA001", "2024-25", false)
	if st24.Opening != money.MustAmount("200.00") || st24.Closing != money.MustAmount("400.00") {
		t.Fatalf("2024-25 %+v", st24)
	}
	stB, err := o.HistoryStatement(ctx, MT, "chk.one", "TB002", "2025-26", false)
	if err != nil || len(stB.Lines) != 4500 || stB.Closing != money.MustAmount("4500.00") {
		t.Fatalf("TB002 spread over chunks: %v %d %s", err, len(stB.Lines), stB.Closing)
	}
	idx, _ := o.ClientHistory(ctx, MT, "chk.one", "TA001", false)
	if len(idx.Years) != 2 || idx.Years[0].FY != "2025-26" || idx.Archived != 2 {
		t.Fatalf("index %+v", idx)
	}
	var exp bytes.Buffer
	if err := o.ExportHistory(ctx, MT, "chk.one", "2025-26", pkg.HistoryLedger, &exp); err != nil || strings.Count(exp.String(), "\n") != 4503+2 {
		t.Fatalf("export %v %d lines", err, strings.Count(exp.String(), "\n"))
	}
	// Portal: a client sees their own ledger and notes, and nobody else's.
	if _, err := o.HistoryStatement(ctx, MT, "client.a", "TA001", "2025-26", true); err != nil {
		t.Fatal(err)
	}
	if _, err := o.HistoryStatement(ctx, MT, "client.a", "TB002", "2025-26", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("client A saw B's ledger: %v", err)
	}
	if _, err := o.ClientHistoryTable(ctx, MT, "client.a", "TA001", "2025-26", pkg.HistoryTrades, true); err == nil {
		t.Fatal("portal served a kind it should not")
	}
	tb, err := o.ClientHistoryTable(ctx, MT, "client.a", "TA001", "2025-26", pkg.HistoryContractNotes, true)
	if err != nil || len(tb.Rows) != 1 || contains(tb.Columns, "SourceRef") {
		t.Fatalf("portal notes %v %+v", err, tb)
	}
	if _, err := o.HistoryStatement(ctx, MT, "client.a", "TA001", "2025-26", false); err == nil {
		t.Fatal("a client used the staff view")
	}

	// Archive: search, portal isolation, audited downloads.
	res, err := o.SearchArchive(ctx, MT, "chk.one", ArchiveQuery{Type: pkg.DocContractNote, From: "2025-04-01", To: "2026-03-31", Signed: "Y"})
	if err != nil || len(res.Docs) != 2 || res.Total != 3 {
		t.Fatalf("search %v %+v", err, res)
	}
	if res, _ := o.SearchArchive(ctx, MT, "chk.one", ArchiveQuery{UCC: "TA001"}); len(res.Docs) != 2 {
		t.Fatalf("client search %+v", res)
	}
	pa, err := o.PortalArchive(ctx, MT, "client.a", "TA001")
	if err != nil || len(pa.Docs) != 2 {
		t.Fatalf("portal archive %v %+v", err, pa)
	}
	if _, err := o.PortalArchive(ctx, MT, "client.a", "TB002"); err == nil {
		t.Fatal("client A listed B's documents")
	}
	if _, _, err := o.ArchiveFile(ctx, MT, "client.a", "TB002", "CN-2", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("client A downloaded B's note: %v", err)
	}
	if _, _, err := o.ArchiveFile(ctx, MT, "client.a", "TA001", "CN-2", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("client A got B's note under their own code: %v", err)
	}
	name, data, err := o.ArchiveFile(ctx, MT, "client.a", "TA001", "CN-1", true)
	if err != nil || !strings.Contains(string(data), "CN-1") || !strings.HasSuffix(name, ".pdf") {
		t.Fatalf("own note %v %s", err, name)
	}
	if _, _, err := o.ArchiveFile(ctx, MT, "comp.one", "TB002", "CN-2", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.ArchiveFile(ctx, MT, "client.a", "TB002", "CN-2", false); err == nil {
		t.Fatal("a client used the staff download")
	}
	downloads := map[string]bool{}
	evs, _ := o.App.Audit.List(ctx, MT, 1000)
	for _, e := range evs {
		if e.Action == "archive.download" {
			downloads[e.Actor+" "+e.Object] = true
		}
	}
	if !downloads["client.a TA001|CN-1"] || !downloads["comp.one TB002|CN-2"] || len(downloads) != 2 {
		t.Fatalf("downloads audited: %v", downloads)
	}
	// Retention and legal hold.
	if _, err := o.DeleteArchived(ctx, MT, "comp.one", "TA001", "CN-1", "tidy up"); err == nil || !strings.Contains(err.Error(), "kept until 2033-05-02") {
		t.Fatalf("deleted before RetainUntil: %v", err)
	}
	if _, err := o.DeleteArchived(ctx, MT, "chk.one", "TA001", "ST-OLD", "tidy up"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an Ops checker deleted: %v", err)
	}
	if _, err := o.SetArchiveHold(ctx, MT, "comp.one", "TA001", "ST-OLD", true, "made-up arbitration matter"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.DeleteArchived(ctx, MT, "comp.one", "TA001", "ST-OLD", "past retention"); err == nil || !strings.Contains(err.Error(), "legal hold") {
		t.Fatalf("deleted on hold: %v", err)
	}
	// Undo is refused while a document is on hold.
	if _, err := o.ProposeUndo(ctx, MT, "adm.one", m.ID, "trial run"); err == nil || !strings.Contains(err.Error(), "legal hold") {
		t.Fatalf("undo with a hold: %v", err)
	}
	o.SetArchiveHold(ctx, MT, "comp.one", "TA001", "ST-OLD", false, "matter closed")
	if _, err := o.DeleteArchived(ctx, MT, "comp.one", "TA001", "ST-OLD", "past retention"); err != nil {
		t.Fatal(err)
	}
	if pa, _ := o.PortalArchive(ctx, MT, "client.a", "TA001"); len(pa.Docs) != 1 {
		t.Fatalf("deleted document still listed: %+v", pa.Docs)
	}

	// Reconcile again after someone posts a journal: it fails and lists the
	// account behind the failure. That entry also blocks the undo.
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: MT, Key: "test-jv-1", ValueDate: mAsOf, Narration: "made-up correction", Source: ledger.SourceRef{Kind: "MANUAL", ID: "1"},
		Postings: []ledger.Posting{{Account: "CLIENT:TA001", Debit: money.MustAmount("10.00")}, {Account: "BANK:TESTBANK", Credit: money.MustAmount("10.00")}}, CreatedBy: "adm.two"}); err != nil {
		t.Fatal(err)
	}
	m, err = o.ReconcileMigration(ctx, MT, "chk.one", m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var failed []string
	for _, r := range m.Recon.Rows {
		if r.Status == "Fail" {
			failed = append(failed, r.Name)
			if r.Name == pkg.CheckClientDebit && (len(r.Behind) != 1 || r.Behind[0].Key != "CLIENT:TA001" || r.Behind[0].Ledgerline != "1510.00 Dr" || r.Difference != "10.00") {
				t.Fatalf("behind %+v", r)
			}
		}
	}
	if strings.Join(failed, ",") != "client-debit-total" {
		t.Fatalf("failed %v", failed)
	}
	var csvb bytes.Buffer
	if err := o.MigReconCSV(ctx, MT, "chk.one", m.ID, &csvb); err != nil || !strings.Contains(csvb.String(), "CLIENT:TA001") {
		t.Fatalf("recon csv %v", err)
	}
	if b, err := o.MigReconPDF(ctx, MT, "chk.one", m.ID); err != nil || !bytes.HasPrefix(b, []byte("%PDF")) {
		t.Fatalf("recon pdf %v", err)
	}
	if _, err := o.ProposeUndo(ctx, MT, "adm.one", m.ID, "trial run"); err == nil || !strings.Contains(err.Error(), "posted since") {
		t.Fatalf("undo after a posting: %v", err)
	}
	es, _ := o.App.Ledger.Store.List(ctx, MT, ledger.Filter{})
	for _, e := range es {
		if e.Key == "test-jv-1" {
			o.App.Ledger.Reverse(ctx, MT, e.ID, "adm.two", "made-up", "")
		}
	}
	// The reversal is a posting too: still refused. A fresh firm shows undo.
	if _, err := o.ProposeUndo(ctx, MT, "adm.one", m.ID, "trial run"); err == nil {
		t.Fatal("undo after postings")
	}
}

func TestMigUndoAndLiveGuard(t *testing.T) {
	o, ctx := migSetup(t)
	p := checked(t, o, ctx, mkPkg(t, mspec{masters: true, history: true, docs: true, checks: goodChecks("9999.00", "")}), "office.llmig")
	// A firm with its own ledger entries looks live.
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: MT, Key: "test-live", ValueDate: "2026-10-01", Narration: "made-up", Source: ledger.SourceRef{Kind: "MANUAL", ID: "1"},
		Postings: []ledger.Posting{{Account: "BANK:TESTBANK", Debit: money.MustAmount("1.00")}, {Account: "EQUITY:CAPITAL", Credit: money.MustAmount("1.00")}}, CreatedBy: "adm.two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ProposeMigration(ctx, MT, "adm.one", MigProposal{Package: p.ID, Firm: "01"}); err == nil || !strings.Contains(err.Error(), "not live yet") {
		t.Fatalf("live firm: %v", err)
	}
	m, err := o.ProposeMigration(ctx, MT, "adm.one", MigProposal{Package: p.ID, Firm: "01", NotLive: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.ProposeMigration(ctx, MT, "adm.one", MigProposal{Package: p.ID, Firm: "01", NotLive: true}); err == nil {
		t.Fatal("two proposals at once")
	}
	if m, err = o.DecideMigration(ctx, MT, "adm.two", m.ID, true, mPass, ""); err != nil || m.Status != MigLoaded {
		t.Fatalf("load %v %s %s", err, m.Status, m.Error)
	}
	// The reconciliation fails on the trial balance (the old system's figure
	// here is made up wrong).
	tbFail := false
	for _, r := range m.Recon.Rows {
		if r.Name == pkg.CheckTBDebit {
			tbFail = r.Status == "Fail" && r.Expected == "9999.00" && r.Ledgerline == "2501.00"
		}
	}
	if !tbFail {
		t.Fatalf("recon %+v", m.Recon.Rows)
	}
	// Undo: proposed by one, approved by another.
	m, err = o.ProposeUndo(ctx, MT, "adm.one", m.ID, "wrong as-of date")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideUndo(ctx, MT, "adm.one", m.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("own undo approved: %v", err)
	}
	m, err = o.DecideUndo(ctx, MT, "chk.one", m.ID, true, "")
	if err != nil || m.Status != MigUndone {
		t.Fatalf("undo %v %s %s", err, m.Status, m.Error)
	}
	bal, _ := o.App.Ledger.Balances(ctx, MT, "")
	if bal["CLIENT:TA001"] != 0 || bal["CLIENT:TB002"] != 0 || bal["BANK:TESTBANK"] != money.MustAmount("1.00") {
		t.Fatalf("balances after undo %v", bal)
	}
	if idx, _ := o.ClientHistory(ctx, MT, "chk.one", "TA001", false); len(idx.Years) != 0 || idx.Archived != 0 {
		t.Fatalf("history after undo %+v", idx)
	}
	if _, _, err := o.ArchiveFile(ctx, MT, "chk.one", "TA001", "CN-1", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archive after undo: %v", err)
	}
	// After an undo the package can be loaded again.
	m2, err := o.ProposeMigration(ctx, MT, "adm.one", MigProposal{Package: p.ID, Firm: "01", NotLive: true, AsOf: "2026-10-29"})
	if err != nil {
		t.Fatal(err)
	}
	if m2, err = o.DecideMigration(ctx, MT, "chk.one", m2.ID, true, mPass, ""); err != nil || m2.Status != MigLoaded {
		t.Fatalf("reload %v %s %s", err, m2.Status, m2.Error)
	}
	if idx, _ := o.ClientHistory(ctx, MT, "chk.one", "TA001", false); len(idx.Years) != 2 || idx.Archived != 2 {
		t.Fatalf("history after reload %+v", idx)
	}
}

func TestMigResumeAfterRestart(t *testing.T) {
	o, ctx := migSetup(t)
	p := checked(t, o, ctx, mkPkg(t, mspec{masters: true, history: true, docs: true}), "office.llmig")
	m, err := o.ProposeMigration(ctx, MT, "adm.one", MigProposal{Package: p.ID, Firm: "01"})
	if err != nil {
		t.Fatal(err)
	}
	// Approve, then pretend the server stopped after the first three files:
	// mark the rest pending again and the migration as still loading.
	if m, err = o.DecideMigration(ctx, MT, "chk.one", m.ID, true, mPass, ""); err != nil || m.Status != MigLoaded {
		t.Fatalf("%v %s", err, m.Status)
	}
	for i := 3; i < len(m.Steps); i++ {
		m.Steps[i].Status, m.Steps[i].Resume, m.Steps[i].Seq, m.Steps[i].Counts = "Pending", 0, nil, nil
	}
	m.Status, m.Recon = MigLoading, nil
	o.saveMig(ctx, MT, "chk.one", &m)
	v, _ := o.MigrationView(ctx, MT, "adm.one")
	if v.Migrations[0].Status != MigInterrupted {
		t.Fatalf("not shown as interrupted: %s", v.Migrations[0].Status)
	}
	if _, err := o.ResumeMigration(ctx, MT, "chk.one", m.ID, "wrong passphrase!!"); err == nil {
		t.Fatal("resumed with a wrong passphrase")
	}
	m, err = o.ResumeMigration(ctx, MT, "chk.one", m.ID, mPass)
	if err != nil || m.Status != MigLoaded || m.Docs != 3 {
		t.Fatalf("resume %v %s docs %d", err, m.Status, m.Docs)
	}
	if res, _ := o.SearchArchive(ctx, MT, "chk.one", ArchiveQuery{}); len(res.Docs) != 3 || res.Total != 3 {
		t.Fatalf("archive after resume %+v", res)
	}
}

func TestMigrationOrder(t *testing.T) {
	ord := strings.Join(migrationOrder(), " ")
	for _, pair := range [][2]string{{"CLIENT-MASTER", "CLIENT-BANKS"}, {"CLIENT-MASTER", "BROKERAGE-CLIENTS"}, {"SCRIP-GROUPS", "BROKERAGE-CARDS"},
		{"BROKERAGE-CARDS", "BROKERAGE-CLIENTS"}, {"CLIENT-MASTER", "CLIENT-INTEREST"}, {"CLIENT-MASTER", "OPENING-BALANCES"}, {"DP-HOLDINGS", "HOLDING-LOTS"},
		{"BRANCHES", "PARTNERS"}, {"PARTNERS", "CLIENT-MASTER"}, {"CLIENT-MASTER", "UCC-DETAILS"}, {"UCC-DETAILS", "UCC-CONTACTS"}, {"CLIENT-MASTER", "DP-ACCOUNTS"}, {"DP-ACCOUNTS", "DP-HOLDINGS"},
		{"DP-ACCOUNTS", "DP-TARIFFS"}, {"DP-TARIFFS", "DP-TARIFF-ACCOUNTS"}} {
		if strings.Index(ord, pair[0]) > strings.Index(ord, pair[1]) {
			t.Errorf("%s after %s", pair[0], pair[1])
		}
	}
	if groupIN(123456789) != "12,34,56,789" || groupIN(999) != "999" || groupIN(1000) != "1,000" {
		t.Fatal(groupIN(123456789))
	}
}
