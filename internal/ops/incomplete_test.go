package ops

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/ingest"
	"ledgerline/internal/migrate/pkg"
)

// Made-up clients only: inactive clients migrated with the details the
// previous system did not have.

var (
	inCols  = append(append([]string{}, cmCols...), "Inactive", "InactiveSince")
	nomCols = []string{"UCC", "Name", "Relation", "Share", "IDType", "ID", "DOB", "Guardian", "GuardianRelation"}
)

// inRow is an inactive client as an old system keeps one: no mobile, no
// PAN, the bank account masked as the KRA shows it, no segment.
func inRow(ucc, inactive string) []string {
	r := cmRow(ucc, "Test Dormant Client", "", "")
	r[8], r[14] = "", "xxxx1234"                // Segments, Account
	r[16] = "12345678000099" + ucc[len(ucc)-2:] // its own demat account
	return append(r, inactive, map[bool]string{true: "2023-03-31"}[inactive == "Y"])
}

func activeRow(ucc, name, pan, mobile string) []string {
	return append(cmRow(ucc, name, pan, mobile), "N", "")
}

func warnsBy(es []ingest.ParseError) map[string]bool {
	m := map[string]bool{}
	for _, e := range es {
		m[fmt.Sprintf("%d %s", e.Line, strings.ToLower(e.Field))] = true
	}
	return m
}

func TestIncompleteInactiveClientMaster(t *testing.T) {
	o, ctx := migSetup(t)
	data := csvOf(inCols, activeRow("TA001", "Test Client A", "ABCPA1234A", "9876500001"), inRow("TI001", "Y"))
	r, err := o.LoadFile(ctx, MT, "adm.one", "CLIENT-MASTER", "", "cm.csv", data)
	if err != nil {
		t.Fatalf("an inactive client with details missing was rejected: %v %+v", err, r.Errors)
	}
	w := warnsBy(r.Warnings)
	for _, f := range []string{"mobile", "pan", "bank.account", "segments"} {
		if !w["3 "+f] {
			t.Errorf("no warning for %s: %+v", f, r.Warnings)
		}
	}
	for _, e := range r.Warnings {
		if !e.Warning || !strings.HasSuffix(e.Msg, incompleteNote) {
			t.Errorf("warning %+v", e)
		}
	}
	c, _ := o.Client(ctx, MT, "TI001")
	if got := strings.Join(c.Incomplete, ", "); got != "Mobile, PAN, Bank account number, Segments" {
		t.Fatalf("incomplete %q", got)
	}
	if c.Bank.Account != "xxxx1234" || c.Mobile != "" || c.PAN != "" || len(c.Segments) != 0 || !c.Inactive {
		t.Fatalf("not loaded as given: %+v", c)
	}
	if a, _ := o.Client(ctx, MT, "TA001"); len(a.Incomplete) != 0 {
		t.Fatalf("an active client marked incomplete: %v", a.Incomplete)
	}

	// The same row for an active client: rejected as before, nothing stored.
	o2, ctx2 := migSetup(t)
	r, err = o2.LoadFile(ctx2, MT, "adm.one", "CLIENT-MASTER", "", "cm.csv", csvOf(inCols, inRow("TI001", "N")))
	if err == nil {
		t.Fatal("an active client with details missing loaded")
	}
	checkErrs(t, "active", r.Errors, []wantErr{{2, "mobile"}, {2, "pan"}, {2, "bank.account"}, {2, "segments"}})
	if len(r.Warnings) != 0 {
		t.Fatalf("warnings for an active client: %+v", r.Warnings)
	}
	if cs, _ := o2.Clients(ctx2, MT); len(cs) != 0 {
		t.Fatal("stored")
	}

	// Even for an inactive client the UCC, name, stage, flag and dates must be right.
	bad := inRow("TI002", "Y")
	bad[1], bad[20], bad[19] = "X", "Dormant", "31/03/2020" // Name, Status, KYCDate
	bad2 := inRow("TI003", "Q")
	r, err = o2.LoadFile(ctx2, MT, "adm.one", "CLIENT-MASTER", "", "cm.csv", csvOf(inCols, bad, bad2, inRow("TI001", "Y"), inRow("TI001", "Y")))
	if err == nil {
		t.Fatal("loaded")
	}
	checkErrs(t, "inactive", r.Errors, []wantErr{{2, "name"}, {2, "status"}, {2, "kycdate"}, {3, "inactive"}, {3, "mobile"}, {5, "ucc"}})

	// A blank Inactive cell: the client as stored decides.
	again := inRow("TI001", "")
	r, err = o.LoadFile(ctx, MT, "adm.one", "CLIENT-MASTER", "", "cm2.csv", csvOf(inCols, again))
	if err != nil || len(r.Warnings) == 0 {
		t.Fatalf("re-import of a stored inactive client: %v %+v", err, r.Errors)
	}
	// A later import with the details: nothing is missing any more.
	full := append(cmRow("TI001", "Test Dormant Client", "ABCPI1234I", "9876500009"), "Y", "2023-03-31")
	full[16] = "1234567800009901"
	if r, err = o.LoadFile(ctx, MT, "adm.one", "CLIENT-MASTER", "", "cm3.csv", csvOf(inCols, full)); err != nil || len(r.Warnings) != 0 {
		t.Fatalf("complete re-import: %v %+v %+v", err, r.Errors, r.Warnings)
	}
	if c, _ := o.Client(ctx, MT, "TI001"); len(c.Incomplete) != 0 || !c.Inactive {
		t.Fatalf("after a complete re-import: %+v", c.Incomplete)
	}
}

// The go-live files after the client master, for an inactive client the
// same dry run loaded: warnings; for an active client: errors.
func TestIncompleteDryRunLaterFiles(t *testing.T) {
	o, ctx := migSetup(t)
	set, _ := o.Settings(ctx, MT)
	limit := set.maxClientBanks()
	banks := [][]string{{"UCC", "Bank", "IFSC", "Account", "Primary", "Status"},
		{"TA001", "Test Bank", "TEST0123456", "123456789012", "Y", ""},
		{"TA001", "Test Bank", "BADIFSC", "123456789013", "N", ""},  // line 3: an error
		{"TI001", "Test Bank", "TEST0123456", "xxxx1234", "Y", ""},  // line 4: masked
		{"TI001", "Test Bank", "TEST0123456", "xxxx1234", "N", ""},  // line 5: the same again, left out
		{"TI001", "", "", "", "", ""},                               // line 6: nothing, left out
		{"TI001", "Old Bank", "OLDB0000001", "9988", "N", "Closed"}} // line 7: closed, loads inactive
	for i := 0; i < limit; i++ {
		banks = append(banks, []string{"TI001", "Test Bank", "TEST0123456", fmt.Sprintf("22334455%04d", i), "N", ""})
	}
	noms := [][]string{{"UCC", "Name", "Relation", "Share", "IDType", "ID", "DOB", "Guardian", "GuardianRelation", "Email"},
		{"TA001", "Test Nominee", "", "", "", "", "", "", "", ""},       // line 2: an error
		{"TI001", "Test Old Nominee", "", "", "", "", "", "", "", "x@"}, // line 3: no relationship, bad email
		{"TI001", "", "", "", "", "", "", "", "", ""}}                   // line 4: nothing, left out
	det := [][]string{{"UCC", "Address1", "City", "State", "PIN"},
		{"TA001", "1 Test Road", "Pune", "Maharashtra", ""}, // line 2: an error
		{"TI001", "2 Old Test Road", "Pune", "", "4110"}}    // line 3: no state, short PIN
	con := [][]string{{"UCC", "Name", "Designation", "PAN"},
		{"TA001", "Test Director", "", ""},   // line 2: an error
		{"TI001", "Test Partner", "", "BAD"}, // line 3: no designation, bad PAN
		{"TI001", "", "", ""}}                // line 4: nothing, left out
	files := []DryFile{{Name: "cm", Source: "CLIENT-MASTER", Data: csvOf(inCols, activeRow("TA001", "Test Client A", "ABCPA1234A", "9876500001"), inRow("TI001", "Y"))},
		{Name: "banks", Source: "CLIENT-BANKS", Data: csvOf(banks...)}, {Name: "noms", Source: "NOMINEES", Data: csvOf(noms...)},
		{Name: "det", Source: "UCC-DETAILS", Data: csvOf(det...)}, {Name: "con", Source: "UCC-CONTACTS", Data: csvOf(con...)}}
	res, err := o.DryRunFiles(ctx, MT, "adm.one", files)
	if err != nil {
		t.Fatal(err)
	}
	split := func(r DryResult) (hard, warn []ingest.ParseError) {
		for _, e := range r.Errors {
			if e.Warning {
				warn = append(warn, e)
			} else {
				hard = append(hard, e)
			}
		}
		return
	}
	for _, r := range res[1:] {
		hard, warn := split(r)
		if len(warn) == 0 {
			t.Errorf("%s: no warnings for the inactive client", r.Source)
		}
		for _, e := range hard {
			if e.Line != 2 && !(r.Source == "CLIENT-BANKS" && e.Line == 3) || strings.Contains(e.Msg, "TI001") {
				t.Errorf("%s: an error for the inactive client: %+v", r.Source, e)
			}
		}
		if len(hard) == 0 && r.Source != "CLIENT-BANKS" && r.Source != "NOMINEES" {
			t.Errorf("%s: the active client's problem is no error", r.Source)
		}
		for _, e := range warn {
			// an active client's invalid other account loads inactive, with a warning
			// and a nominee relationship missing from an old nomination is asked for, not refused
			if strings.Contains(e.Msg, "TA001") && !(r.Source == "CLIENT-BANKS" && strings.Contains(e.Msg, "inactive account")) && !(r.Source == "NOMINEES" && strings.Contains(e.Msg, "ask the client")) {
				t.Errorf("%s: a warning for the active client: %+v", r.Source, e)
			}
		}
	}
	_, bw := split(res[1])
	txt := fmt.Sprint(bw)
	for _, want := range []string{"is on an earlier row too", "no bank details", "loads as Inactive", "so 1 load as inactive accounts", incompleteNote, inactiveAcctNote} {
		if !strings.Contains(txt, want) {
			t.Errorf("bank warnings lack %q: %s", want, txt)
		}
	}
	_, nw := split(res[2])
	checkErrs(t, "nominee warnings", nw, []wantErr{{3, "nominees.0.relation"}, {3, "nominees.0.email"}, {4, "Name"}})
	_, dw := split(res[3])
	checkErrs(t, "detail warnings", dw, []wantErr{{3, "State"}, {3, "PIN"}})
	_, kw := split(res[4])
	checkErrs(t, "contact warnings", kw, []wantErr{{3, "Designation"}, {3, "PAN"}, {4, "Name"}})
	if cs, _ := o.Clients(ctx, MT); len(cs) != 0 {
		t.Fatal("the dry run stored clients")
	}
}

// Loaded incomplete, completed a detail at a time, then reactivated.
func TestIncompleteReactivation(t *testing.T) {
	o, ctx := migSetup(t)
	const day = "2026-11-02"
	o.Now = func() time.Time { return time.Date(2026, 11, 2, 11, 0, 0, 0, ist) }
	load := func(src string, rows ...[]string) ingest.Result {
		t.Helper()
		r, err := o.LoadFile(ctx, MT, "adm.one", src, "", strings.ToLower(src)+".csv", csvOf(rows...))
		if err != nil {
			t.Fatalf("%s: %v %+v", src, err, r.Errors)
		}
		return r
	}
	load("CLIENT-MASTER", inCols, inRow("TI001", "Y"))
	load("CLIENT-BANKS", []string{"UCC", "Bank", "IFSC", "Account", "Primary"}, []string{"TI001", "Test Bank", "TEST0123456", "xxxx1234", "Y"})
	load("NOMINEES", nomCols, []string{"TI001", "Test Old Nominee", "", "", "", "", "", "", ""})
	load("UCC-DETAILS", []string{"UCC", "Address1", "City", "State", "PIN"}, []string{"TI001", "2 Old Test Road", "Pune", "", ""})
	c, _ := o.Client(ctx, MT, "TI001")
	want := "Mobile, PAN, Bank account number, Bank accounts, Segments, Nominee details, PIN, State"
	if got := strings.Join(c.Incomplete, ", "); got != want {
		t.Fatalf("incomplete %q, want %q", got, want)
	}
	req := Reactivation{UCC: "TI001", IPVMode: "VIPV", IPVDate: day, KRAStatus: "Validated"}
	_, err := o.RequestReactivation(ctx, MT, "mkr.one", day, req)
	if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "Complete the client's details first: "+want+". They were missing in the previous system.") {
		t.Fatalf("reactivation of an incomplete client: %v", err)
	}

	// One detail at a time: a wrong value is still refused; the rest stays listed.
	if _, err := o.UpdateContact(ctx, MT, "mkr.one", day, "TI001", "", "12345", "", "Validated"); err == nil {
		t.Fatal("an invalid mobile was saved")
	}
	if _, err := o.UpdateContact(ctx, MT, "mkr.one", day, "TI001", "", "9876500009", "", "Validated"); err != nil {
		t.Fatal(err)
	}
	if c, _ = o.Client(ctx, MT, "TI001"); contains(c.Incomplete, "Mobile") || !contains(c.Incomplete, "PAN") {
		t.Fatalf("after the mobile: %v", c.Incomplete)
	}
	// The bank account by a change request, with its proof.
	doc, err := o.AddClientDoc(ctx, MT, "mkr.one", "TI001", "Bank proof", "cheque.pdf", []byte("%PDF-1.4 made-up cheque"))
	if err != nil {
		t.Fatal(err)
	}
	ch, err := o.RequestChange(ctx, MT, "mkr.one", "TI001", "Bank account", map[string]string{"bank": "Test Bank", "ifsc": "TEST0123456", "account": "334455667788"}, doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideChange(ctx, MT, "chk.one", ch.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	// The nomination.
	ns := `[{"name":"Test New Nominee","relation":"Spouse"}]`
	if ch, err = o.RequestChange(ctx, MT, "mkr.one", "TI001", "Nomination", map[string]string{"nominee": "Opted in", "nominees": ns, "mode": "Signed form"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideChange(ctx, MT, "chk.one", ch.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	// The address in the UCC details.
	if _, err := o.SaveUCCDetails(ctx, MT, "mkr.one", "TI001", UCCDetails{Address1: "2 Old Test Road", City: "Pune", State: "Maharashtra", PIN: "411001"}); err != nil {
		t.Fatal(err)
	}
	c, _ = o.Client(ctx, MT, "TI001")
	if got := strings.Join(c.Incomplete, ", "); got != "PAN, Segments" {
		t.Fatalf("after the edits: %q (banks %+v)", got, c.Banks)
	}
	if _, err := o.RequestReactivation(ctx, MT, "mkr.one", day, req); err == nil || !strings.Contains(err.Error(), "first: PAN, Segments.") {
		t.Fatalf("reactivation with PAN and segments missing: %v", err)
	}
	// PAN and segments with a client master row from the old system,
	// which still has no mobile and the masked account: what was completed
	// here stays.
	row := inRow("TI001", "Y")
	row[3], row[8] = "ABCPI1234I", "CM"
	if r := load("CLIENT-MASTER", inCols, row); len(r.Warnings) != 2 {
		t.Fatalf("warnings %+v", r.Warnings)
	}
	if c, _ = o.Client(ctx, MT, "TI001"); len(c.Incomplete) != 0 || c.Mobile != "9876500009" || c.Bank.Account != "334455667788" || len(activeBanks(c)) != 1 || c.PAN != "ABCPI1234I" {
		t.Fatalf("still incomplete: %v %+v", c.Incomplete, c)
	}
	ra, err := o.RequestReactivation(ctx, MT, "mkr.one", day, req)
	if err != nil {
		t.Fatal(err)
	}
	// Marked incomplete again before the approval: the approval is refused.
	load("UCC-DETAILS", []string{"UCC", "Address1", "City", "State", "PIN"}, []string{"TI001", "", "", "", "123"})
	if _, err := o.DecideReactivation(ctx, MT, "chk.one", day, ra.ID, true, ""); err == nil || !strings.Contains(err.Error(), "Complete the client's details first: PIN.") {
		t.Fatalf("approval of an incomplete client: %v", err)
	}
	if _, err := o.SaveUCCDetails(ctx, MT, "mkr.one", "TI001", UCCDetails{Address1: "2 Old Test Road", City: "Pune", State: "Maharashtra", PIN: "411001"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideReactivation(ctx, MT, "chk.one", day, ra.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if c, _ = o.Client(ctx, MT, "TI001"); c.Inactive || len(c.Incomplete) != 0 {
		t.Fatalf("not reactivated: %+v", c)
	}
}

// A masked account is never paid.
func TestIncompleteNotPaid(t *testing.T) {
	c := Client{UCC: "TI001", Bank: BankAccount{Bank: "Test Bank", IFSC: "TEST0123456", Account: "xxxx1234"}, Incomplete: []string{"Bank account number"}}
	if _, err := payoutBank(c, PayoutRequest{ID: "PO-1", UCC: "TI001"}); err == nil || !strings.Contains(err.Error(), "is incomplete") {
		t.Fatalf("payout to a masked account: %v", err)
	}
	c.Bank.Account, c.Incomplete = "334455667788", nil
	if _, err := payoutBank(c, PayoutRequest{ID: "PO-1", UCC: "TI001"}); err != nil {
		t.Fatal(err)
	}
}

// A package whose only problems are inactive clients' warnings is
// proposed, loaded and reconciled.
func TestIncompleteMigrationLoads(t *testing.T) {
	o, ctx := migSetup(t)
	set, _ := o.Settings(ctx, MT)
	limit := set.maxClientBanks()
	var buf bytes.Buffer
	w, err := pkg.Create(&buf, mPass, "test tool")
	if err != nil {
		t.Fatal(err)
	}
	m := w.Manifest()
	m.Source = pkg.Source{System: "SharePro", Backups: []pkg.Backup{{Database: "TEST2627", Role: "company", FY: "2026-27", Used: true}}}
	m.Firms = []pkg.Firm{{Code: "01", Name: "Test Broking", Years: []string{"2026-27"}, AsOf: mAsOf}}
	writeCSV(t, w, "01", "CLIENT-MASTER", [][]string{inCols, activeRow("TA001", "Test Client A", "ABCPA1234A", "9876500001"), inRow("TI001", "Y")})
	banks := [][]string{{"UCC", "Bank", "IFSC", "Account", "Primary"}, {"TA001", "Test Bank", "TEST0123456", "123456789012", "Y"}, {"TI001", "Test Bank", "TEST0123456", "xxxx1234", "Y"}}
	for i := 0; i < limit; i++ { // one more active account than the limit
		banks = append(banks, []string{"TI001", "Test Bank", "TEST0123456", fmt.Sprintf("22334455%04d", i), "N"})
	}
	writeCSV(t, w, "01", "CLIENT-BANKS", banks)
	writeCSV(t, w, "01", "NOMINEES", [][]string{nomCols, {"TI001", "Test Old Nominee", "", "", "", "", "", "", ""}})
	writeCSV(t, w, "01", "UCC-DETAILS", [][]string{{"UCC", "Address1", "City", "State", "PIN"}, {"TA001", "1 Test Road", "Pune", "Maharashtra", "411001"}, {"TI001", "2 Old Test Road", "Pune", "", ""}})
	writeCSV(t, w, "01", "OPENING-BALANCES", [][]string{{"Account", "Debit", "Credit"}, {"CLIENT:TA001", "1500.00", ""}, {"CLIENT:TI001", "", "2500.00"}, {"BANK:TESTBANK", "1000.00", ""}})
	for _, c := range []pkg.Check{{Firm: "01", Name: pkg.CheckTBDebit, Value: "2500.00"}, {Firm: "01", Name: pkg.CheckTBCredit, Value: "2500.00"}, {Firm: "01", Name: pkg.CheckClients, Value: "2"},
		{Firm: "01", Name: pkg.CheckClientDebit, Value: "1500.00"}, {Firm: "01", Name: pkg.CheckClientCredit, Value: "2500.00"}, {Firm: "01", Name: pkg.CheckBankAccounts, Value: fmt.Sprint(limit + 2)}} {
		w.AddCheck(c)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	p := checked(t, o, ctx, buf.Bytes(), "dormant.llmig")
	if p.Check.Errors != 0 || p.Check.Warnings == 0 {
		t.Fatalf("check %+v", p.Check)
	}
	errs, _, _ := o.MigErrors(ctx, MT, "adm.one", p.ID, "", 0, 0)
	for _, e := range errs {
		if strings.Contains(e.Message, incompleteNote) && !strings.Contains(e.Action, "before reactivating") {
			t.Errorf("action %q for %q", e.Action, e.Message)
		}
	}
	mg, err := o.ProposeMigration(ctx, MT, "adm.one", MigProposal{Package: p.ID, Firm: "01"})
	if err != nil {
		t.Fatalf("a package with only warnings was not proposed: %v", err)
	}
	if mg, err = o.DecideMigration(ctx, MT, "chk.one", mg.ID, true, mPass, ""); err != nil || mg.Status != MigLoaded {
		t.Fatalf("load: %v %s %s %+v", err, mg.Status, mg.Error, mg.Steps)
	}
	if rc := mg.Recon; rc == nil || rc.Failed != 0 || rc.Passed != 6 {
		t.Fatalf("recon %+v", rc)
	}
	c, _ := o.Client(ctx, MT, "TI001")
	if !c.Inactive || !contains(c.Incomplete, "Mobile") || !contains(c.Incomplete, "Bank accounts") || !contains(c.Incomplete, "Nominee details") || !contains(c.Incomplete, "PIN") {
		t.Fatalf("incomplete %v", c.Incomplete)
	}
	over := 0
	for _, b := range c.Banks {
		if b.InactiveWhy == bankOverLimitWhy {
			over++
		}
	}
	if len(activeBanks(c)) != limit || over != 1 {
		t.Fatalf("banks %+v", c.Banks)
	}
	// Its credit balance is not paid to the masked account.
	set.RASMonthly = "2026-11-06"
	if err := o.SaveSettings(ctx, MT, "adm.one", set); err != nil {
		t.Fatal(err)
	}
	v, err := o.RAS(ctx, MT, mAsOf)
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	for _, r := range v.Rows {
		if r.UCC == "TI001" && validDate(r.SettlesOn) {
			ran = true
			if _, err := o.RunRAS(ctx, MT, "chk.one", mAsOf, r.SettlesOn); err == nil || !strings.Contains(err.Error(), "TI001: ₹") || !strings.Contains(err.Error(), "is incomplete") {
				t.Fatalf("RAS for a masked account: %v", err)
			}
		}
	}
	if !ran {
		t.Fatalf("no settlement due for TI001: %+v", v.Rows)
	}
}

// An active client's nominee with an invalid email: the email is left out
// and the nomination loads; a missing relationship marks the client
// incomplete instead of stopping the load.
func TestActiveNomineeLeniency(t *testing.T) {
	o, ctx := migSetup(t)
	cm := csvOf(cmCols, cmRow("TA001", "Test Client A", "ABCPA1234A", "9876500001"))
	if _, err := o.LoadFile(ctx, MT, "adm.one", "CLIENT-MASTER", "", "c.csv", cm); err != nil {
		t.Fatal(err)
	}
	nf := csvOf([]string{"UCC", "Name", "Relation", "Share", "IDType", "ID", "DOB", "Guardian", "GuardianRelation", "Email"},
		[]string{"TA001", "Test Nominee One", "", "60", "", "", "", "", "", "not-an-email"},
		[]string{"TA001", "Test Nominee Two", "Spouse", "40", "", "", "", "", "", "two@example.com"})
	res, err := o.LoadFile(ctx, MT, "adm.one", "NOMINEES", "", "n.csv", nf)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) < 2 {
		t.Errorf("warnings %+v", res.Warnings)
	}
	c, _ := o.Client(ctx, MT, "TA001")
	if len(c.Nominees) != 2 || c.Nominees[0].Email != "" || c.Nominees[1].Email != "two@example.com" {
		t.Fatalf("nominees %+v", c.Nominees)
	}
	if !contains(c.Incomplete, "Nominee details") {
		t.Errorf("incomplete %v", c.Incomplete)
	}
}
