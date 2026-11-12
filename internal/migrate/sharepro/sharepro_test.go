package sharepro

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
	"ledgerline/internal/money"
	"ledgerline/internal/ops"
)

const pass = "test passphrase 123"

func TestRegistered(t *testing.T) {
	if migrate.MapperFor("SharePro") == nil {
		t.Fatal("SharePro mapper not registered")
	}
}

func TestClassify(t *testing.T) {
	m := New()
	ctx := context.Background()
	other := newFake("RANDOMDB")
	other.table("ORDERS", "ID,QTY")
	noYear := companyFake("BACKOFFICE")
	noYear.on("jv-years", migrate.Row{"y": int64(2024), "n": int64(10)})
	cases := []struct {
		q          *fake
		role, fy   string
		skip       bool
		orderLo    int
		orderHi    int
		noteHasStr string
	}{
		{adminFake(), "admin", "", false, 0, 0, "admin"},
		{companyFake("TT2627"), "company", "2026-27", false, 10, 49, "2026-27"},
		{companyFake("SP_TT2526202601010900"), "company", "2025-26", false, 10, 49, "2025-26"},
		{noYear, "company", "2024-25", false, 10, 49, "2024-25"},
		{mailFake(), "mail", "2026-27", false, 50, 99, "Mail"},
		{other, "other", "", true, 100, 100, "Not a SharePro"},
	}
	for _, c := range cases {
		k, err := m.Classify(ctx, c.q)
		if err != nil {
			t.Fatalf("%s: %v", c.q.name, err)
		}
		if k.Role != c.role || k.FY != c.fy || k.Skip != c.skip || k.Order < c.orderLo || k.Order > c.orderHi || !strings.Contains(strings.ToLower(k.Note), strings.ToLower(c.noteHasStr)) {
			t.Errorf("%s: got %+v", c.q.name, k)
		}
	}
	a, _ := m.Classify(ctx, companyFake("TT2627"))
	b, _ := m.Classify(ctx, companyFake("TT2526"))
	ml, _ := m.Classify(ctx, mailFake())
	if !(a.Order < b.Order && b.Order < ml.Order) {
		t.Errorf("order: newest company %d, older %d, mail %d", a.Order, b.Order, ml.Order)
	}
}

func TestFYFromName(t *testing.T) {
	for in, want := range map[string]string{"AB2627": "2026-27", "SP_AB2627202601010900": "2026-27", "XY0910": "2009-10", "SSMAIL202601010900": "", "ABCMAIL2526": "2025-26"} {
		if got := fyFromName(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestFirms(t *testing.T) {
	m := New()
	fs, err := m.Firms(context.Background(), adminFake(), companyFake("TT2627"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 3 {
		t.Fatalf("firms %+v", fs)
	}
	// "X PVT. LTD.", "X PVT. LTD", "X PVT. LTD (CDSL)", "X PVT. LTD(C - MCX)" are one firm
	sec, met, cr := fs[0], fs[1], fs[2]
	if sec.Code != "02" || sec.Name != "TT SECURITIES PVT. LTD." || strings.Join(sec.Segments, ",") != "CDSL-DP,MCX-CD,NSE-CM,NSE-FO" || strings.Join(sec.Years, ",") != "2025-26,2026-27" {
		t.Errorf("securities firm %+v", sec)
	}
	// "X PVT. LTD (MCX)" and "X PVT. LTD.(SPOT)"
	if met.Code != "05" || strings.Join(met.Segments, ",") != "MCX-COM" {
		t.Errorf("metals firm %+v", met)
	}
	if cr.Code != "09" || !strings.HasSuffix(cr.Name, "(inactive in SharePro)") || len(cr.Segments) != 0 {
		t.Errorf("inactive firm %+v", cr)
	}
	// without the admin database: one firm of every CO
	m2 := New()
	fs, err = m2.Firms(context.Background(), nil, companyFake("TT2627"))
	if err != nil || len(fs) != 1 || fs[0].Code != "02" {
		t.Fatalf("fallback firms %+v %v", fs, err)
	}
}

// build runs a step into a package and opens it.
func build(t *testing.T, fn func(w *pkg.Writer)) *pkg.Reader {
	t.Helper()
	var buf bytes.Buffer
	w, err := pkg.Create(&buf, pass, "test")
	if err != nil {
		t.Fatal(err)
	}
	fn(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := pkg.Open(bytes.NewReader(buf.Bytes()), pass, t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	if bad := r.VerifyAll(); len(bad) > 0 {
		t.Fatal(bad)
	}
	return r
}

func readCSV(t *testing.T, r *pkg.Reader, p string) (head []string, rows []map[string]string) {
	t.Helper()
	err := r.CSV(p, func(h, row []string) error {
		head = h
		m := map[string]string{}
		for i, c := range h {
			if i < len(row) {
				m[c] = row[i]
			}
		}
		rows = append(rows, m)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if head == nil {
		rc, _ := r.Open(p)
		b := new(bytes.Buffer)
		b.ReadFrom(rc)
		rc.Close()
		head = strings.Split(strings.TrimSpace(strings.SplitN(b.String(), "\n", 2)[0]), ",")
	}
	return head, rows
}

func runCurrent(t *testing.T, plan migrate.Plan) (*pkg.Reader, *fake) {
	t.Helper()
	return runCurrentOn(t, plan, companyFake("TT2627"))
}

// runCurrentOn runs the go-live step for firm 02 on a company database.
func runCurrentOn(t *testing.T, plan migrate.Plan, co *fake) (*pkg.Reader, *fake) {
	t.Helper()
	m := New()
	ctx := context.Background()
	if _, err := m.Firms(ctx, adminFake(), co); err != nil {
		t.Fatal(err)
	}
	k, err := m.Classify(ctx, co)
	if err != nil {
		t.Fatal(err)
	}
	plan.Firms = []string{"02"}
	r := build(t, func(w *pkg.Writer) {
		w.Manifest().Firms = []pkg.Firm{{Code: "02", Name: "TT SECURITIES"}}
		if err := m.Current(ctx, co, k, plan, w, nil); err != nil {
			t.Fatal(err)
		}
	})
	return r, co
}

func issues(r *pkg.Reader, area, kind string) []pkg.Issue {
	var out []pkg.Issue
	for _, i := range r.Manifest.Issues {
		if i.Area == area && strings.Contains(i.Message, kind) {
			out = append(out, i)
		}
	}
	return out
}

func check(r *pkg.Reader, name string) string {
	for _, c := range r.Manifest.Checks {
		if c.Name == name {
			return c.Value
		}
	}
	return "<none>"
}

func TestCurrentHeaders(t *testing.T) {
	r, _ := runCurrent(t, migrate.Plan{})
	described := map[string]bool{"BROKERAGE-CLIENT-RATES|Note": true} // read by the import, not named in its description
	for _, src := range ops.MasterSources() {
		want, ok := headers[src.Name]
		if !ok {
			continue
		}
		p := "firms/02/master/" + src.Name + ".csv"
		if !r.Has(p) {
			t.Errorf("%s not written", src.Name)
			continue
		}
		head, _ := readCSV(t, r, p)
		if strings.Join(head, ",") != strings.Join(want, ",") {
			t.Errorf("%s header %v, want %v", src.Name, head, want)
		}
		have := map[string]bool{}
		for _, c := range head {
			have[strings.ToLower(c)] = true
		}
		for _, c := range src.Columns {
			if !have[strings.ToLower(c)] {
				t.Errorf("%s lacks required column %s", src.Name, c)
			}
		}
		req := map[string]bool{}
		for _, c := range src.Columns {
			req[c] = true
		}
		extra := map[string]bool{}
		for _, c := range optionalNotYetRead[src.Name] {
			extra[c] = true
		}
		for _, c := range head {
			if !req[c] && !extra[c] && !described[src.Name+"|"+c] && !strings.Contains(src.Purpose, c) {
				t.Errorf("%s column %s is neither required nor an optional column of the import", src.Name, c)
			}
		}
	}
	// imports the Ledgerline side is adding: compared with MasterSources
	// above once it lists them; until then with their fixed layouts
	for name, want := range headers {
		if ops.IsMasterSource(name) {
			continue
		}
		req, ok := pendingRequired[name]
		if !ok {
			t.Errorf("%s is neither a Ledgerline import nor a pending one", name)
			continue
		}
		p := "firms/02/master/" + name + ".csv"
		if !r.Has(p) {
			t.Errorf("%s not written", name)
			continue
		}
		head, _ := readCSV(t, r, p)
		if strings.Join(head, ",") != strings.Join(want, ",") || strings.Join(head[:len(req)], ",") != strings.Join(req, ",") {
			t.Errorf("%s header %v", name, head)
		}
	}
}

func TestCurrentContents(t *testing.T) {
	r, co := runCurrent(t, migrate.Plan{})
	_, cl := readCSV(t, r, "firms/02/master/CLIENT-MASTER.csv")
	by := map[string]map[string]string{}
	for _, c := range cl {
		by[c["UCC"]] = c
	}
	if len(cl) != 8 { // 5 trading clients and 3 DP-only ones
		t.Fatalf("clients %d", len(cl))
	}
	if x := by["TEST001"]; x["AP"] != "SB01" || x["RM"] != "RM01" || x["Groups"] != "G1;FAM1" || x["RAS"] != "Monthly" {
		t.Errorf("TEST001 AP/RM/groups/RAS %v", x)
	}
	if x := by["TEST002"]; x["RM"] != "D01" || x["RAS"] != "" || by["TEST003"]["RAS"] != "Quarterly" {
		t.Errorf("TEST002 dealer as RM, unknown RAS code %v", x)
	}
	if len(issues(r, "Client master", "settlement type 1")) != 1 {
		t.Error("unknown running-account code not reported")
	}
	if _, ok := by["TEST006"]; ok {
		t.Error("the metals firm's client is in the securities firm")
	}
	a := by["TEST001"]
	if a["Mobile"] != "9800000001" || a["Email"] != "asha@example.com" || a["Segments"] != "CM;FO" || a["IFSC"] != "TEST0000001" || a["DematID"] != "1234567800000001" ||
		a["Risk"] != "Low" || a["Nominee"] != "Opted in" || a["Status"] != "Active" || a["Inactive"] != "" || a["State"] != "West Bengal" || a["KYCDate"] != "2020-02-01" {
		t.Errorf("TEST001 %v", a)
	}
	if by["TEST002"]["Inactive"] != "Y" || by["TEST002"]["InactiveSince"] != "2025-06-01" || by["TEST003"]["Inactive"] != "Y" {
		t.Errorf("dormant rows %v %v", by["TEST002"], by["TEST003"])
	}
	for _, k := range []string{"not a valid PAN", "10-digit", "IFSC", "Email"} {
		if len(issues(r, "Client master", k)) == 0 {
			t.Errorf("no client master issue %q: %+v", k, r.Manifest.Issues)
		}
	}
	for _, i := range issues(r, "Client master", "PAN") {
		if i.Key == "TEST004" && i.Severity != "Error" {
			t.Errorf("bad PAN severity %+v", i)
		}
	}
	if len(issues(r, "Segment status", "WEIRD")) != 1 {
		t.Errorf("unknown segment status not reported once")
	}
	// banks
	_, bk := readCSV(t, r, "firms/02/master/CLIENT-BANKS.csv")
	if len(bk) != 9 || bk[0]["Primary"] != "Y" || bk[0]["IFSC"] != "TEST0000001" || bk[1]["Status"] != "Inactive" || bk[1]["Primary"] != "N" || bk[4]["Account"] != "12AB" {
		t.Errorf("banks %v", bk)
	}
	if len(issues(r, "Client bank accounts", "IFSC")) == 0 {
		t.Error("bad IFSC not reported")
	}
	// nominees: only the last 4 digits of an Aadhaar
	_, nm := readCSV(t, r, "firms/02/master/NOMINEES.csv")
	if len(nm) != 4 || nm[0]["IDType"] != "AADHAAR" || nm[0]["ID"] != "4321" { // and the DP-only clients' 3
		t.Errorf("nominees %v", nm)
	}
	// segment status history
	_, ss := readCSV(t, r, "firms/02/master/SEGMENT-STATUS.csv")
	got := []string{}
	for _, s := range ss {
		got = append(got, s["UCC"]+":"+s["Segment"]+":"+s["Status"]+":"+s["EffectiveDate"])
	}
	want := "TEST001:CM:Active:2021-01-01,TEST001:FO:Active:2021-01-01,TEST002:CM:Active:2020-01-01,TEST002:CM:Dormant:2025-06-01,TEST003:CM:Active:2019-01-01,TEST003:CM:Closed:2024-01-01,TEST004:CM:Active:2022-01-01,TEST005:CM:Active:2023-01-01"
	if strings.Join(got, ",") != want {
		t.Errorf("segment status\n got %s\nwant %s", strings.Join(got, ","), want)
	}
	// holdings: one row per client, DP account and ISIN, quantities exact;
	// an account not in the DP master is reported
	_, hd := readCSV(t, r, "firms/02/master/DP-HOLDINGS.csv")
	hb := map[string]string{}
	for _, x := range hd {
		hb[x["UCC"]+"|"+x["BOID"]+"|"+x["ISIN"]] = x["Qty"] + "/" + x["Pledged"] + "/" + x["Symbol"]
	}
	for k, v := range map[string]string{"TEST001|1234567800000001|INE000A01011": "100/10/TESTCO", "TEST001|1234567800000002|INE000A01011": "20/0/TESTCO",
		"00000009|1234567800000009|INE000A01011": "5/0/TESTCO", "00000009|1234567800000009|INF000A01015": "125.432/25.1/"} {
		if hb[k] != v {
			t.Errorf("holding %s = %q, want %q", k, hb[k], v)
		}
	}
	if len(hd) != 4 {
		t.Errorf("holdings %v", hd)
	}
	if len(issues(r, "DP holdings", "not in SharePro's DP master")) != 1 {
		t.Error("DP account missing from CLLIST not reported")
	}
	if len(issues(r, "DP holdings", "rounded")) != 0 || len(issues(r, "DP holdings", "1 holdings are fractional")) != 1 {
		t.Errorf("fractional holdings: %+v", r.Manifest.Issues)
	}
	// positions: 30 long futures at 100 (FIFO), 50 short puts; the closed call is not there
	_, ps := readCSV(t, r, "firms/02/master/POSITIONS.csv")
	if len(ps) != 2 {
		t.Fatalf("positions %v", ps)
	}
	if p := ps[0]; p["Type"] != "FUT" || p["Qty"] != "30" || p["AvgPrice"] != "100.0000" || p["Symbol"] != "NIFTY" || p["Exchange"] != "NSE" || p["Segment"] != "FO" || p["Expiry"] != "2026-10-29" {
		t.Errorf("future %v", p)
	}
	if p := ps[1]; p["Type"] != "PE" || p["Qty"] != "-50" || p["Strike"] != "21000" {
		t.Errorf("put %v", p)
	}
	if len(issues(r, "Open positions", "call or put")) != 1 {
		t.Error("option of unknown type not reported")
	}
	// brokerage: versions per date, with OTHBRKG minimums on CO 02's lines
	_, br := readCSV(t, r, "firms/02/master/BROKERAGE-CLIENT-RATES.csv")
	var lines []string
	for _, b := range br {
		lines = append(lines, fmt.Sprintf("%s %s %s %s %s %s %s %s %s", b["From"], b["Product"], b["Exchange"], b["Mode"], b["Pct"], b["PerLot"], b["MinPerShare"], b["Min"], b["Scrip"]))
	}
	for _, w := range []string{"2024-04-01 CM-INTRA NSE pct 0.03  0.01 20 ", "2025-04-01 CM-INTRA NSE pct 0.02  0.01 20 ", "2025-04-01 CM-DEL NSE pct 0.1  0.05 20 INE000A01011", "2024-04-01 FO-OPT NSE per_lot  45   "} {
		found := false
		for _, l := range lines {
			found = found || l == w
		}
		if !found {
			t.Errorf("brokerage line %q not in\n%s", w, strings.Join(lines, "\n"))
		}
	}
	if len(issues(r, "Brokerage", "ALLXYZ")) != 1 {
		t.Error("unknown item group not reported")
	}
	// interest
	_, in := readCSV(t, r, "firms/02/master/CLIENT-INTEREST.csv")
	if len(in) != 2 || in[0]["UCC"] != "TEST001" || in[0]["Rate"] != "18" || in[0]["GraceDays"] != "3" || in[1]["UCC"] != "TEST004" || in[1]["Free"] != "Y" {
		t.Errorf("interest %v", in)
	}
	// KRA and CKYC
	_, kr := readCSV(t, r, "firms/02/master/KRA-STATUS.csv")
	_, ck := readCSV(t, r, "firms/02/master/CKYC-NUMBERS.csv")
	if len(kr) != 3 || kr[0]["Status"] != "Registered" || len(ck) != 2 || ck[0]["CKYCNumber"] != "12345678901234" {
		t.Errorf("kra %v ckyc %v", kr, ck)
	}
	// checks come from the server's own totals
	// clients: DLMAST's 5 and the 3 DP-only; DP accounts and quantity: every
	// account in CLLIST; bank accounts: the rows listed for Ledgerline
	for name, want := range map[string]string{pkg.CheckTBDebit: "5595.00", pkg.CheckTBCredit: "5595.00", pkg.CheckOpeningDifference: "150.00", pkg.CheckClients: "8", pkg.CheckClientDebit: "1415.00",
		pkg.CheckClientCredit: "310.00", pkg.CheckDPAccounts: "8", pkg.CheckDPQuantity: "250.432", pkg.CheckBankAccounts: "9", pkg.CheckPositions: "3"} {
		if got := check(r, name); got != want {
			t.Errorf("check %s = %s, want %s", name, got, want)
		}
	}
	if len(co.asked("check-tb")) != 1 {
		t.Error("trial balance check not asked of the server")
	}
	// dropped fields are counted, never read
	dropped := map[string]int{}
	for _, d := range r.Manifest.Dropped {
		dropped[d.Field] = d.Count
	}
	if dropped["Client web password"] != 2 || dropped["DP web password"] != 2 || dropped["Aadhaar number (first holder)"] != 2 || dropped["Aadhaar number (client agreement)"] != 2 ||
		dropped["Dealer web password"] != 2 || dropped["Aadhaar number (contact person 1)"] != 2 || dropped["DP nominee Aadhaar number"] != 2 ||
		dropped["Aadhaar number (DP account details)"] != 2 || dropped["Aadhaar number (DP nominee 1, account details)"] != 2 || dropped["Aadhaar number (DP nominee 3, account details)"] != 2 {
		t.Errorf("dropped %v", dropped)
	}
	if r.Manifest.Firms[0].AsOf != "2026-10-12" {
		t.Errorf("as of %+v", r.Manifest.Firms)
	}
}

func TestTrialBalanceAndSince(t *testing.T) {
	r, _ := runCurrent(t, migrate.Plan{})
	_, ob := readCSV(t, r, "firms/02/master/OPENING-BALANCES.csv")
	var dr, cr money.Amount
	acct := map[string]map[string]string{}
	for _, o := range ob {
		acct[o["Account"]] = o
		if o["Debit"] != "" {
			dr += money.MustAmount(o["Debit"])
		}
		if o["Credit"] != "" {
			cr += money.MustAmount(o["Credit"])
		}
	}
	if dr != cr || dr != money.MustAmount("5595") {
		t.Fatalf("trial balance %s / %s: %v", dr, cr, ob)
	}
	// SharePro's openings do not balance: the difference is one suspense line, with a warning
	if acct[DiffAccount]["Debit"] != "150.00" {
		t.Errorf("difference line %v", acct[DiffAccount])
	}
	if w := issues(r, "Opening balances", "do not balance"); len(w) != 1 || w[0].Severity != "Warning" || !strings.Contains(w[0].Message, "02 -350.00, 03 200.00") {
		t.Errorf("difference warning %+v", w)
	}
	// a client-coded head that is not the client ledger (margin deposits)
	// stays one firm account, with a warning
	if acct["PAYABLE:02-MRG"]["Credit"] != "150.00" {
		t.Errorf("margin head %v", ob)
	}
	if w := issues(r, "Client ledgers", "not merged"); len(w) != 1 || w[0].Severity != "Warning" || !strings.Contains(w[0].Action, "CASH MARGIN DEPOSIT [CLIENTS]") || !strings.Contains(w[0].Message, "1 clients with a balance") {
		t.Errorf("client-coded head warning %+v", w)
	}
	if len(issues(r, "Client ledgers", "merged into CLIENT")) != 2 {
		t.Errorf("client ledger heads not listed: %+v", r.Manifest.Issues)
	}
	c1 := acct["CLIENT:TEST001"]
	// and its two DP accounts' DP ledgers (head 64 of CO 06), 50 and 20
	if c1["Debit"] != "1270.00" || c1["Since"] != "2026-04-05" {
		t.Errorf("TEST001 %v", c1)
	}
	if c := acct["CLIENT:TEST003"]; c["Credit"] != "300.00" || c["Since"] != "" {
		t.Errorf("TEST003 %v", c)
	}
	if _, ok := acct["CLIENT:TEST002"]; ok {
		t.Error("zero balance written")
	}
	for code, kind := range map[string]string{"BANK:02-BANK1": "Debit", "EQUITY:02-CAP": "Credit", "INCOME:02-BRKINC": "Credit"} {
		if acct[code][kind] == "" {
			t.Errorf("%s missing: %v", code, ob)
		}
	}
}

// A debit already due at the year start gets the year's first day, with
// an Info issue counting such debits.
func TestSinceBeforeYear(t *testing.T) {
	m := New()
	ctx := context.Background()
	co := companyFake("TT2627")
	co.on("trial-balance", migrate.Row{"co": "02", "accd": "62", "subcode": "TEST001", "b": "1500.00"}, migrate.Row{"co": "02", "accd": "CAP", "subcode": "", "b": "-1500.00"})
	co.on("client-net", migrate.Row{"co": "02", "accd": "62", "subcode": "TEST001", "dr": "1200.00", "cr": ".00"})
	co.on("client-days", migrate.Row{"co": "02", "accd": "62", "subcode": "TEST001", "vrdt": d("2026-04-05"), "dr": "1200.00", "cr": ".00"})
	if _, err := m.Firms(ctx, adminFake(), co); err != nil {
		t.Fatal(err)
	}
	k, _ := m.Classify(ctx, co)
	r := build(t, func(w *pkg.Writer) {
		if err := m.Current(ctx, co, k, migrate.Plan{Firms: []string{"02"}}, w, nil); err != nil {
			t.Fatal(err)
		}
	})
	_, ob := readCSV(t, r, "firms/02/master/OPENING-BALANCES.csv")
	for _, o := range ob {
		if o["Account"] == "CLIENT:TEST001" && o["Since"] != "2026-04-01" {
			t.Errorf("since %v", o)
		}
	}
	if len(issues(r, "Opening balances", "already due before the year began")) != 1 {
		t.Error("old debits not counted")
	}
	for _, q := range co.queries {
		if strings.Contains(q, "LY_ACCTOT") {
			t.Errorf("LY_ACCTOT is incomplete and must not be used: %s", q)
		}
	}
}

func TestUCCDetailsAndMasters(t *testing.T) {
	r, _ := runCurrent(t, migrate.Plan{})
	_, ud := readCSV(t, r, "firms/02/master/UCC-DETAILS.csv")
	by := map[string]map[string]string{}
	for _, u := range ud {
		by[u["UCC"]] = u
	}
	if len(ud) != 8 { // and the 3 DP-only clients
		t.Fatalf("ucc details %v", ud)
	}
	a := by["TEST001"]
	if a["Address1"] != "1 Test Lane" || a["Address2"] != "Floor 2" || a["State"] != "West Bengal" || a["PIN"] != "700001" || a["Gender"] != "Female" || a["Guardian"] != "Ravi Test" ||
		a["Occupation"] != "2" || a["Marital"] != "" || a["Nationality"] != "Indian" || a["PEP"] != "N" || a["IPV"] != "Y" || a["POASecurities"] != "Y" || a["POAFunds"] != "Y" ||
		a["AgreementDate"] != "2020-01-02" || a["PermAddress1"] != "2 Home Road" || a["PermPIN"] != "700002" || a["PermState"] != "West Bengal" || a["BankType"] != "SB" || a["Country"] != "India" {
		t.Errorf("TEST001 %v", a)
	}
	b := by["TEST002"] // from the KYC register
	if b["Address1"] != "5 Kyc Street" || b["City"] != "HOWRAH" || b["PIN"] != "711101" || b["PermAddress1"] != "6 Home Lane" || b["Gender"] != "Male" ||
		b["Occupation"] != "Private Sector Service" || b["OccupationDetail"] != "S-01" || b["Guardian"] != "Old Test" || b["Marital"] != "" {
		t.Errorf("TEST002 %v", b)
	}
	if by["TEST005"]["CIN"] != "U12345WB2020PTC000001" || by["TEST005"]["Category"] != "Body Corporate" {
		t.Errorf("TEST005 %v", by["TEST005"])
	}
	if e := issues(r, "UCC details", "PIN is missing"); len(e) != 1 || e[0].Key != "TEST004" || e[0].Severity != "Error" {
		t.Errorf("missing PIN %+v", e)
	}
	if len(issues(r, "UCC details", "Marital status codes not mapped")) != 1 {
		t.Error("marital codes not reported")
	}
	// contact persons of the company only
	_, cp := readCSV(t, r, "firms/02/master/UCC-CONTACTS.csv")
	if len(cp) != 2 || cp[0]["UCC"] != "TEST005" || cp[0]["Name"] != "Ravi Director" || cp[0]["Designation"] != "Director" || cp[0]["PAN"] != "AAAPD0001D" ||
		cp[0]["DIN"] != "01234567" || cp[0]["Address"] != "9 Road KOLKATA" || cp[0]["Email"] != "ravi@example.com" || cp[1]["Designation"] != "Authorised signatory" {
		t.Errorf("contacts %v", cp)
	}
	// branches from the admin database
	_, br := readCSV(t, r, "firms/02/master/BRANCHES.csv")
	if len(br) != 3 || br[0]["Code"] != "HO" || br[0]["Manager"] != "Ravi Manager" || br[1]["Kind"] != "Zone" || br[1]["Parent"] != "HO" || br[2]["Active"] != "N" {
		t.Errorf("branches %v", br)
	}
	// authorised persons, RMs and dealers
	_, pt := readCSV(t, r, "firms/02/master/PARTNERS.csv")
	if len(pt) != 3 || pt[0]["Code"] != "SB01" || pt[0]["Kind"] != "Authorised person" || pt[0]["IFSC"] != "TEST0000001" || pt[0]["Bank"] != "TEST BANK" || pt[0]["RegNo"] != "AP0001" ||
		pt[1]["Code"] != "RM01" || pt[1]["Kind"] != "Relationship manager" || pt[2]["Code"] != "D01" || pt[2]["Active"] != "Y" {
		t.Errorf("partners %v", pt)
	}
	if w := issues(r, "Client master", "SB09"); len(w) != 1 || w[0].Key != "TEST002" {
		t.Errorf("unknown AP %+v", w)
	}
	_, cm := readCSV(t, r, "firms/02/master/CLIENT-MASTER.csv")
	for _, c := range cm {
		if c["UCC"] == "TEST002" && c["AP"] != "" {
			t.Errorf("unknown AP written: %v", c["AP"])
		}
	}
	// DP accounts: every account, DP-only ones under the DP-only client's
	// code; one primary per client
	_, dp := readCSV(t, r, "firms/02/master/DP-ACCOUNTS.csv")
	got := map[string]string{}
	for _, x := range dp {
		got[x["BOID"]] = strings.Join([]string{x["UCC"], x["Depository"], x["Status"], x["Freeze"], x["FreezeReason"], x["DDPI"], x["BSDA"], x["ClosedOn"], x["Primary"]}, "|")
	}
	want := map[string]string{
		"1234567800000001": "TEST001|CDSL|Active|||Y|N||Y",
		"1234567800000002": "TEST001|CDSL|Active|||N|||N",
		"1234567800000009": "00000009|CDSL|Active|||N|Y||Y",
		"1234567800000004": "TEST004|CDSL|Frozen|Debit|TEST FREEZE|N|||Y",
		"1234567800000003": "TEST003|CDSL|Closed|||N||2024-01-01|N", // a closed account is no primary
		"1234567800000010": "00000010|CDSL|Closed|||N||2023-03-31|N",
		"1234567800000011": "00000011|CDSL|Frozen|Both|Frozen in SharePro (no reason recorded)|N|||Y",
		"1234567800000012": "TEST005|CDSL|Active|||N|||Y", // by TRADINGID
	}
	if len(dp) != len(want) {
		t.Errorf("DP accounts %v", dp)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("DP account %s = %q, want %q", k, got[k], v)
		}
	}
	if w := issues(r, "DP accounts", "no reason recorded"); len(w) != 1 || w[0].Key != "00000011" || w[0].Severity != "Warning" {
		t.Errorf("frozen account with no reason %+v", w)
	}
	for _, k := range []string{"3 DP accounts have no trading client in SharePro (DP-only clients)", "1 of these accounts have a trading code (CLLIST.SUBCODE) that is not a client",
		"1 clients have more than one account at this DP"} {
		if len(issues(r, "DP accounts", k)) != 1 {
			t.Errorf("DP accounts info %q: %+v", k, r.Manifest.Issues)
		}
	}
	if len(issues(r, "DP accounts", "DP-only")) != 3 { // and the TRADINGID links ("not DP-only clients")
		t.Error("DP-only accounts not counted")
	}
}

func TestFIFO(t *testing.T) {
	f := &fifo{}
	f.apply("2026-04-01", money.MustDec("100"))
	f.apply("2026-04-10", money.MustDec("50"))
	f.apply("2026-04-12", money.MustDec("-120"))
	if f.oldest() != "2026-04-10" || f.lots[0].amt.String() != "30" {
		t.Fatalf("after part payment %+v", f.lots)
	}
	f.apply("2026-04-15", money.MustDec("-50"))
	if f.oldest() != "" || f.credit.String() != "20" {
		t.Fatalf("overpaid %+v %s", f.lots, f.credit)
	}
	f.apply("2026-04-20", money.MustDec("70"))
	if f.oldest() != "2026-04-20" || f.lots[0].amt.String() != "50" {
		t.Fatalf("credit used first %+v", f.lots)
	}
}

func TestDormantSkip(t *testing.T) {
	r, _ := runCurrent(t, migrate.Plan{DormantMode: "skip"})
	_, cl := readCSV(t, r, "firms/02/master/CLIENT-MASTER.csv")
	// the closed DP-only account is left out too
	var codes []string
	for _, c := range cl {
		codes = append(codes, c["UCC"])
	}
	if strings.Join(codes, ",") != "TEST001,TEST004,TEST005,00000009,00000011" {
		t.Fatalf("clients %v", codes)
	}
	if len(issues(r, "Client master", "1 closed DP-only accounts were left out")) != 1 || len(issues(r, "Client master", "2 clients with no active segment were left out")) != 1 {
		t.Errorf("skipped clients not counted: %+v", r.Manifest.Issues)
	}
	// the closed client's credit stays in the trial balance, with an error
	_, ob := readCSV(t, r, "firms/02/master/OPENING-BALANCES.csv")
	found := false
	for _, o := range ob {
		found = found || o["Account"] == "CLIENT:TEST003"
	}
	if !found {
		t.Error("skipped client's balance left out of the trial balance")
	}
	// and the closed DP-only client's advance (its DP ledger)
	errs := issues(r, "Opening balances", "Dormant client left out")
	if len(errs) != 2 || errs[0].Key != "00000010" || errs[1].Key != "TEST003" || errs[0].Severity != "Error" || errs[1].Severity != "Error" {
		t.Errorf("skipped client with balance: %+v", errs)
	}
	if check(r, pkg.CheckClients) != "5" || check(r, pkg.CheckDPAccounts) != "6" {
		t.Errorf("clients check %s, DP accounts %s", check(r, pkg.CheckClients), check(r, pkg.CheckDPAccounts))
	}
	_, dp := readCSV(t, r, "firms/02/master/DP-ACCOUNTS.csv")
	for _, x := range dp {
		if x["UCC"] == "TEST003" || x["UCC"] == "00000010" {
			t.Errorf("skipped client's DP account written: %v", x)
		}
	}
	if len(dp) != 6 {
		t.Errorf("DP accounts %v", dp)
	}
}

// No query reads a password, an Aadhaar number or a log file's contents,
// except to count them or take the last 4 digits.
func TestSecretsNeverSelected(t *testing.T) {
	_, co := runCurrent(t, migrate.Plan{})
	m := New()
	ctx := context.Background()
	h := companyFake("TT2526")
	k, _ := m.Classify(ctx, h)
	build(t, func(w *pkg.Writer) {
		if err := m.History(ctx, h, k, migrate.Plan{}, w, nil); err != nil {
			t.Fatal(err)
		}
	})
	var all []string
	all = append(all, co.queries...)
	all = append(all, h.queries...)
	sawCount := false
	for _, q := range all {
		if c := unsafeColumn(q); c != "" {
			t.Errorf("query reads %s: %s", c, q)
		}
		for _, w := range []string{"WEB_PWD", "ADHAR"} {
			if strings.Contains(strings.ToUpper(q), w) {
				st := strings.ToUpper(readPart(q))
				if strings.Contains(st, w) {
					t.Errorf("%s outside COUNT/RIGHT: %s", w, q)
				}
				sawCount = true
			}
		}
	}
	if !sawCount {
		t.Error("the secret columns were never counted")
	}
	// the DP master, its details and the DP nominations are read, without
	// their Aadhaar columns
	for _, tag := range []string{"dp-accounts", "dp-details", "dp-nominees"} {
		qs := co.asked(tag)
		if len(qs) == 0 {
			t.Errorf("%s not read", tag)
		}
		for _, q := range qs {
			for _, w := range []string{"ADHAR", "[UID]", ".UID", " UID", "WEB_PWD"} {
				if strings.Contains(strings.ToUpper(q), w) {
					t.Errorf("%s reads %s: %s", tag, w, q)
				}
			}
		}
	}
	// the guard itself
	for _, q := range []string{"SELECT Web_Pwd FROM DLMAST", "SELECT FHOLDER_ADHAR FROM CLLIST", "SELECT Content FROM SYSTEMLOGFILES", "SELECT PASSWORD FROM SHUSER", "SELECT UID FROM CDNOMREG",
		"SELECT C_UID1 FROM DLAGREE2", "SELECT NomineeAdhar FROM DLAGREE", "SELECT web_Pwd FROM DLRMAST", "SELECT [UID] AS [uid] FROM CLLIST_DET", "SELECT FHADHARNOM FROM CLLIST_DET",
		"SELECT l.SHADHARNOM FROM CLLIST_DET l", "SELECT THADHARNOM AS x FROM CLLIST_DET", "SELECT n.UID FROM CDNOMREG n"} {
		if unsafeColumn(q) == "" {
			t.Errorf("guard let through %s", q)
		}
		if err := (db{newFake("X")}).query(ctx, q, func(migrate.Row) error { return nil }); err == nil {
			t.Errorf("db.query ran %s", q)
		}
	}
	if unsafeColumn("SELECT COUNT(CASE WHEN LEN(RTRIM(Web_Pwd)) > 0 THEN 1 END) AS n, RIGHT(RTRIM(n.IDNUMBER), 4) AS id, a.contents FROM DLMAST") != "" {
		t.Error("guard refused a count")
	}
}

func TestHistoryStreams(t *testing.T) {
	m := New()
	ctx := context.Background()
	if _, err := m.Firms(ctx, adminFake(), companyFake("TT2627")); err != nil {
		t.Fatal(err)
	}
	h := companyFake("TT2526")
	const n = 30000
	streamed := 0
	h.stream["hist-jv"] = func(q string, fn func(migrate.Row) error) error {
		for i := 0; i < n; i++ {
			amt := fmt.Sprintf("%d.00", 10+i%7)
			acc, sub, dr, cr := "62", "TEST001", amt, ".00"
			if i%2 == 1 {
				acc, sub, dr, cr = "BRKINC", "", ".00", fmt.Sprintf("%d.00", 10+(i-1)%7)
			}
			if err := fn(migrate.Row{"co": "02", "type": "", "tt": "JV", "vrno": fmt.Sprint(i / 2), "vrdt": d("2025-05-01"), "accd": acc, "subcode": sub, "narr": "bill  " + fmt.Sprint(i), "amtdr": dr, "amtcr": cr, "idn": int64(i)}); err != nil {
				return err
			}
			streamed++
		}
		return nil
	}
	h.on("hist-bc", migrate.Row{"co": "02", "type": "", "tt": "B1", "p_r": "R", "vrno": "1", "vrdt": d("2025-05-02"), "accd": "62", "subcode": "TEST001", "narr": "receipt", "amtdr": ".00", "amtcr": "500.00", "cheqno": "123456", "idn": int64(1)})
	h.on("hist-bills", migrate.Row{"co": "02", "stno": "2025081", "sttype": "N", "trdt": d("2025-05-01"), "subcd": "TEST001", "cntrno": int64(501), "turnover": "10000.00", "brokerage": "30.00", "sttax": "10.00", "cgst": "2.70", "sgst": "2.70", "netrecpay": "-10045.40", "npk": int64(7)})
	h.on("hist-trades", migrate.Row{"co": "02", "stno": "2025081", "sttype": "N", "trdt": d("2025-05-01"), "subcd": "TEST001", "ctrdno": "T1", "ordno": "O1", "itemcd": "SC001", "qty": "-10", "act_rate": "1000.00", "brkg": "3.00", "cntrno": int64(501), "npk": int64(9), "nse_itemcd": "TESTCO", "isin": "INE000A01011"},
		migrate.Row{"co": "03", "trdt": d("2025-05-01"), "subcd": "TEST001", "itemcd": "OPT9", "qty": "75", "act_rate": "10.00", "k_prod": "IO", "exp_date": d("2025-05-29"), "cont_rate": "22000.00", "k_srscd": "NIFTY25MAY22000PE", "npk": int64(10)},
		migrate.Row{"co": "03", "trdt": d("2025-05-01"), "subcd": "TEST001", "itemcd": "OPT8", "qty": "75", "act_rate": "10.00", "instr_type": "OPTSTK", "exp_date": d("2025-05-29"), "cont_rate": "500.00", "descr": "TESTCO MAY", "npk": int64(11)})
	h.on("hist-contract-notes", migrate.Row{"co": "02", "cntrno": int64(501), "trdt": d("2025-05-01"), "subcd": "TEST001"})
	h.on("hist-dp", migrate.Row{"co": "06", "trdt": d("2025-06-01"), "benacnum": "00000001", "isincd": "INE000A01011", "drcr": "D", "qtybooked": "5.000", "trancode": "ODTA", "npk": int64(3), "security": "TESTCO EQ"})
	h.on("hist-hold-date", migrate.Row{"d": d("2026-03-31")})
	h.on("hist-holdings", migrate.Row{"co": "06", "benacnum": "00000001", "isincd": "INE000A01011", "balance": "97.000", "curbal": "90.000", "pldgbal": "5.000", "price": "12.50"})
	k, _ := m.Classify(ctx, h)
	r := build(t, func(w *pkg.Writer) {
		if err := m.History(ctx, h, k, migrate.Plan{Firms: []string{"02"}}, w, nil); err != nil {
			t.Fatal(err)
		}
	})
	if streamed != n {
		t.Fatalf("streamed %d", streamed)
	}
	head, led := readCSV(t, r, "firms/02/history/2025-26/LEDGER.csv")
	if strings.Join(head, ",") != strings.Join(pkg.HistoryColumns[pkg.HistoryLedger], ",") {
		t.Errorf("ledger header %v", head)
	}
	if len(led) != n+2 { // the receipt and its bank side
		t.Fatalf("ledger rows %d", len(led))
	}
	if l := led[0]; l["Account"] != "CLIENT:TEST001" || l["UCC"] != "TEST001" || l["Narration"] != "bill 0" || l["Segment"] != "CM" {
		t.Errorf("ledger line %v", l)
	}
	rc, bank := led[n], led[n+1]
	if rc["Credit"] != "500.00" || bank["Account"] != "BANK:02-BANK1" || bank["Debit"] != "500.00" || rc["VoucherType"] != "Receipt" {
		t.Errorf("receipt %v / %v", rc, bank)
	}
	if len(issues(r, "History ledger", "do not balance")) != 0 {
		t.Error("balanced ledger reported unbalanced")
	}
	for kind, rows := range map[string]int{pkg.HistoryBills: 1, pkg.HistoryTrades: 3, pkg.HistoryContractNotes: 1, pkg.HistoryReceipts: 1, pkg.HistoryDP: 1, pkg.HistoryHoldings: 1, pkg.HistoryDPBills: 0} {
		head, got := readCSV(t, r, "firms/02/history/2025-26/"+kind+".csv")
		if strings.Join(head, ",") != strings.Join(pkg.HistoryColumns[kind], ",") || len(got) != rows {
			t.Errorf("%s: header %v rows %d", kind, head, len(got))
		}
	}
	_, tr := readCSV(t, r, "firms/02/history/2025-26/TRADES.csv")
	if tr[0]["Side"] != "S" || tr[0]["Quantity"] != "10" || tr[0]["Brokerage"] != "30.00" || tr[0]["ISIN"] != "INE000A01011" {
		t.Errorf("trade %v", tr[0])
	}
	if tr[1]["OptionType"] != "PE" || tr[1]["Strike"] != "22000" || tr[2]["OptionType"] != "" {
		t.Errorf("option trades %v %v", tr[1], tr[2])
	}
	if len(issues(r, "History", "call or put is not")) != 1 {
		t.Error("option of unknown type not reported")
	}
	_, ye := readCSV(t, r, "firms/02/history/2025-26/DP-HOLDINGS-YEAR-END.csv")
	if y := ye[0]; y["Locked"] != "2" || y["Free"] != "90" || y["Rate"] != "12.5" || y["Value"] != "1212.50" {
		t.Errorf("year-end holding %v", y)
	}
	if q := h.asked("hist-dp-bills"); len(q) != 1 || !strings.Contains(q[0], "IN ('CDSLBILL', 'NSDLBILL', 'DPBILL')") {
		t.Errorf("DP bill query %v", q)
	}
	_, dp := readCSV(t, r, "firms/02/history/2025-26/DP-TRANSACTIONS.csv")
	if dp[0]["Quantity"] != "-5" || dp[0]["UCC"] != "TEST001" || dp[0]["BOID"] != "1234567800000001" {
		t.Errorf("dp txn %v", dp[0])
	}
	_, rp := readCSV(t, r, "firms/02/history/2025-26/RECEIPTS-PAYMENTS.csv")
	if rp[0]["Mode"] != "Cheque" || rp[0]["Amount"] != "500.00" || rp[0]["Bank"] != "TEST BANK A/C" {
		t.Errorf("receipt %v", rp[0])
	}
}

func TestHistoryMissingTables(t *testing.T) {
	m := New()
	ctx := context.Background()
	h := companyFake("TT1516")
	delete(h.tables, "SETTNET")
	delete(h.tables, "DPTRAN")
	h.tables["TRFILE"] = []string{"CO", "TRDT", "SUBCD", "ITEMCD", "QTY", "RATE"} // an older layout
	k, _ := m.Classify(ctx, h)
	r := build(t, func(w *pkg.Writer) {
		if err := m.History(ctx, h, k, migrate.Plan{}, w, nil); err != nil {
			t.Fatal(err)
		}
	})
	if r.Has("firms/02/history/2015-16/BILLS.csv") || r.Has("firms/02/history/2015-16/DP-TRANSACTIONS.csv") {
		t.Error("files written for missing tables")
	}
	if len(issues(r, "History", "no SETTNET")) != 1 {
		t.Errorf("missing table not reported: %+v", r.Manifest.Issues)
	}
	for _, q := range h.asked("hist-trades") {
		if strings.Contains(q, "[NPK]") || strings.Contains(q, "t.NPK") || !strings.Contains(q, "NULL AS [npk]") {
			t.Errorf("missing column selected: %s", q)
		}
	}
}

func TestDocuments(t *testing.T) {
	m := New()
	ctx := context.Background()
	co := companyFake("TT2627")
	if _, err := m.Firms(ctx, adminFake(), co); err != nil {
		t.Fatal(err)
	}
	ck, _ := m.Classify(ctx, co)
	ml := mailFake()
	k, _ := m.Classify(ctx, ml)
	plan := migrate.Plan{Firms: []string{"02", "05"}, Documents: true}
	r := build(t, func(w *pkg.Writer) {
		if err := m.Current(ctx, co, ck, plan, w, nil); err != nil {
			t.Fatal(err)
		}
		if err := m.Documents(ctx, ml, k, plan, w, nil); err != nil {
			t.Fatal(err)
		}
	})
	_, idx := readCSV(t, r, "firms/02/documents/index.csv")
	by := map[string]map[string]string{}
	for _, x := range idx {
		by[strings.SplitN(x["SourceRef"], ";", 2)[0]] = x
	}
	// 1 contract note, 2 ledger, 3 DP statement, 5 payout report (internal),
	// 7 unknown client (first firm), 8 from the archive copy, 9 a DP-only
	// client's DP statement; 1 again in the archive copy is skipped; 4 is
	// too large; 6 is the metals firm's
	if len(idx) != 7 {
		t.Fatalf("documents %v", idx)
	}
	if x := by["SSMAIL2_ATTACHMENT:slno=9,attnumber=1"]; x["Type"] != pkg.DocDPStatement || x["UCC"] != "00000009" {
		t.Errorf("DP-only client's statement %v", x)
	}
	if len(issues(r, "Documents", "DP master")) != 0 {
		t.Errorf("DP documents without a client: %+v", r.Manifest.Issues)
	}
	cn := by["SSMAIL2_ATTACHMENT:slno=1,attnumber=1"]
	if cn["Type"] != pkg.DocContractNote || cn["Signed"] != "Y" || cn["Format"] != "pdf" || cn["UCC"] != "TEST001" || cn["FY"] != "2026-27" || cn["RetainUntil"] != "2034-05-02" ||
		cn["To"] != "asha@example.com" || !strings.HasSuffix(cn["SourceRef"], ";ReportCat=Contract") {
		t.Errorf("contract note %v", cn)
	}
	rc, err := r.OpenDocument(cn["Path"], cn["SHA256"])
	if err != nil {
		t.Fatal(err)
	}
	buf := new(bytes.Buffer)
	buf.ReadFrom(rc)
	rc.Close()
	if buf.String() != "%PDF-1.4 S" {
		t.Errorf("stored %q, want the signed PDF", buf.String())
	}
	if x := by["SSMAIL2_ATTACHMENT:slno=2,attnumber=1"]; x["Type"] != pkg.DocStatement || x["Signed"] != "N" || x["Format"] != "pdf" || x["UCC"] != "TEST001" {
		t.Errorf("ledger %v", x)
	}
	if x := by["SSMAIL2_ATTACHMENT:slno=3,attnumber=1"]; x["Type"] != pkg.DocDPStatement || x["UCC"] != "TEST001" || x["Format"] != "html" {
		t.Errorf("dp statement %v", x)
	}
	if x := by["SSMAIL2_ATTACHMENT:slno=5,attnumber=1"]; x["Type"] != pkg.DocOther || x["UCC"] != "" || x["Subject"] != "Payout report" {
		t.Errorf("internal mail %v", x)
	}
	if x := by["XSSMAIL2_ATTACHMENT:slno=8,attnumber=1"]; x["Type"] != pkg.DocMargin {
		t.Errorf("archive copy %v", x)
	}
	if _, dup := by["XSSMAIL2_ATTACHMENT:slno=1,attnumber=1"]; dup {
		t.Error("archive copy's duplicate archived")
	}
	// the metals firm's client's contract note goes to the metals firm
	_, met := readCSV(t, r, "firms/05/documents/index.csv")
	if len(met) != 1 || met[0]["UCC"] != "TEST006" {
		t.Errorf("metals documents %v", met)
	}
	for _, kind := range []string{"larger than", "no attachment", "could not be tied to a firm", "branches, groups or sub-brokers"} {
		if len(issues(r, "Documents", kind)) != 1 {
			t.Errorf("issue %q: %+v", kind, r.Manifest.Issues)
		}
	}
	q := ml.asked("attachments")
	// text columns are read as their bytes, not through the code page
	if len(q) != 2 || !strings.Contains(q[0], "SUBSTRING(a.Signed, 1, 4) = '%PDF'") || !strings.Contains(q[0], fmt.Sprint(MaxAttachment)) ||
		!strings.Contains(q[0], "CAST(CAST(a.contents AS varchar(max)) AS varbinary(max))") {
		t.Errorf("attachment query %v", q)
	}
	docs := map[string]string{}
	for _, c := range r.Manifest.Checks {
		if c.Name == pkg.CheckDocuments {
			docs[c.Firm] = c.Value
		}
	}
	if docs["02"] != "7" || docs["05"] != "1" {
		t.Errorf("documents checks %v", docs)
	}
}

// Documents of a firm not chosen are left out, not put in the chosen one.
func TestDocumentsOtherFirm(t *testing.T) {
	m := New()
	ctx := context.Background()
	co := companyFake("TT2627")
	if _, err := m.Firms(ctx, adminFake(), co); err != nil {
		t.Fatal(err)
	}
	ck, _ := m.Classify(ctx, co)
	ml := mailFake()
	k, _ := m.Classify(ctx, ml)
	plan := migrate.Plan{Firms: []string{"02"}, Documents: true}
	r := build(t, func(w *pkg.Writer) {
		if err := m.Current(ctx, co, ck, plan, w, nil); err != nil {
			t.Fatal(err)
		}
		if err := m.Documents(ctx, ml, k, plan, w, nil); err != nil {
			t.Fatal(err)
		}
	})
	_, idx := readCSV(t, r, "firms/02/documents/index.csv")
	for _, x := range idx {
		if x["UCC"] == "TEST006" {
			t.Error("the metals firm's document is in the securities firm")
		}
	}
	if len(idx) != 7 || len(issues(r, "Documents", "firms not chosen")) != 1 {
		t.Errorf("documents %d, issues %+v", len(idx), r.Manifest.Issues)
	}
}

func TestDocType(t *testing.T) {
	for _, c := range [][3]string{
		{"Contract", "Client", pkg.DocContractNote},
		{"Margin2Clients", "Client", pkg.DocMargin},
		{"Obligation Mrgn", "Client", pkg.DocMargin},
		{"", "DP CLIENT", pkg.DocDPStatement},
		{"", "Client", pkg.DocOther},
		{"DPBLPRN2", "Client", pkg.DocDPBill},
		{"Vallan Bills", "Client", pkg.DocBill},
		{"RunAcRep2Client", "Client", pkg.DocStatement},
		{"StandardUpload", "Client", pkg.DocStatement},
		{"Trade Register", "Client", pkg.DocStatement},
		{"reKYC", "", pkg.DocKYC},
		{"ClientsAGTS", "Client", pkg.DocOther},
		{"Payout Report", "Branchid", pkg.DocOther},
		{"A/C Ledger", "SubBroker", pkg.DocOther},
	} {
		if got := docType(c[0], c[1]); got != c[2] {
			t.Errorf("%v: %s", c, got)
		}
	}
}

func TestOptType(t *testing.T) {
	for in, want := range map[string]string{"CE": "CE", "NIFTY 29OCT 21000 PE": "PE", "SENSEX24OCT80000CE": "CE", "TESTCO OCT": "", "PE": "PE", "RELIANCE": ""} {
		if got := optType(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestEntityName(t *testing.T) {
	a := entityName("ABC SECURITIES PVT. LTD.")
	for _, v := range []string{"ABC SECURITIES PVT. LTD", "ABC SECURITIES PVT. LTD (CDSL)", "ABC SECURITIES PVT. LTD(C - MCX)", "ABC SECURITIES PVT. LTD.(SPOT)",
		"ABC  Securities  Pvt Ltd (NSE)", "ABC Securities Private Limited (CDSL)"} {
		if entityName(v) != a {
			t.Errorf("%q -> %q, want %q", v, entityName(v), a)
		}
	}
	if entityName("ABC METALS PVT. LTD.(SPOT)") == a {
		t.Error("two companies merged")
	}
	if s := segFromName("ABC SECURITIES PVT. LTD(C - NSE)", "", false); s.label() != "NSE-CD" {
		t.Errorf("currency %v", s)
	}
}

// A client with no segment registered at all cannot trade: it comes across
// inactive, and its missing or invalid details are warnings, not errors.
func TestNoSegmentClientInactive(t *testing.T) {
	m := New()
	ctx := context.Background()
	co := companyFake("TT2627")
	clients := co.rows["clients"]("")
	for _, r := range clients {
		if r["subcd"] == "TEST004" {
			for _, k := range []string{"reg_nse", "reg_bse", "reg_nfo", "reg_mcx"} {
				r[k] = false
			}
		}
	}
	co.on("clients", clients...)
	var st []migrate.Row
	for _, r := range co.rows["segment-status"]("") {
		if r["subcd"] != "TEST004" {
			st = append(st, r)
		}
	}
	co.on("segment-status", st...)
	if _, err := m.Firms(ctx, adminFake(), co); err != nil {
		t.Fatal(err)
	}
	k, err := m.Classify(ctx, co)
	if err != nil {
		t.Fatal(err)
	}
	r := build(t, func(w *pkg.Writer) {
		w.Manifest().Firms = []pkg.Firm{{Code: "02", Name: "TT SECURITIES"}}
		if err := m.Current(ctx, co, k, migrate.Plan{Firms: []string{"02"}}, w, nil); err != nil {
			t.Fatal(err)
		}
	})
	_, cl := readCSV(t, r, "firms/02/master/CLIENT-MASTER.csv")
	found := false
	for _, c := range cl {
		if c["UCC"] == "TEST004" {
			found = true
			if c["Inactive"] != "Y" || c["Segments"] != "" {
				t.Errorf("TEST004 with no segment: %v", c)
			}
		}
	}
	if !found {
		t.Fatal("TEST004 left out")
	}
	for _, i := range r.Manifest.Issues {
		if i.Key == "TEST004" && i.Severity == "Error" {
			t.Errorf("inactive client's detail reported as an error: %+v", i)
		}
	}
	if len(issues(r, "Client master", "no segment registered at all")) != 1 {
		t.Errorf("no-segment clients not counted: %+v", r.Manifest.Issues)
	}
}

func TestNormPIN(t *testing.T) {
	for in, want := range map[string]string{"700 001": "700001", "700001.": "700001", " 700-001 ": "700001", "70001": "70001", "PIN": "PIN", "": ""} {
		if got := normPIN(in); got != want {
			t.Errorf("normPIN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPartnerCode(t *testing.T) {
	for in, want := range map[string]string{"SB01": "SB01", "s/b-7": "SB7", "7": "AP7", ".": "", "na": "", "ABCDEFGHIJKL": "CDEFGHIJKL", " D 01 ": "D01"} {
		if got := partnerCode(in, "AP"); got != want {
			t.Errorf("partnerCode(%q) = %q, want %q", in, got, want)
		}
	}
	if partnerCode("9", "RM") != "RM9" {
		t.Error("RM prefix")
	}
}
