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
	if len(fs) != 2 {
		t.Fatalf("firms %+v", fs)
	}
	sec, met := fs[0], fs[1]
	if sec.Code != "02" || !strings.Contains(sec.Name, "TT SECURITIES") || strings.Join(sec.Segments, ",") != "CDSL-DP,NSE-CM,NSE-FO" || strings.Join(sec.Years, ",") != "2025-26,2026-27" {
		t.Errorf("securities firm %+v", sec)
	}
	if met.Code != "05" || strings.Join(met.Segments, ",") != "MCX-COM" {
		t.Errorf("metals firm %+v", met)
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
	m := New()
	ctx := context.Background()
	co := companyFake("TT2627")
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
	for name := range headers {
		if !ops.IsMasterSource(name) {
			t.Errorf("%s is not a Ledgerline import", name)
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
	if len(cl) != 4 {
		t.Fatalf("clients %d", len(cl))
	}
	a := by["TEST001"]
	if a["Mobile"] != "9800000001" || a["Email"] != "asha@example.com" || a["Segments"] != "CM;FO" || a["IFSC"] != "TEST0000001" || a["DematID"] != "1234567800000001" ||
		a["Risk"] != "Low" || a["Nominee"] != "Opted in" || a["Status"] != "Active" || a["Inactive"] != "" || a["State"] != "" && a["State"] != "WB" || a["KYCDate"] != "2020-02-01" {
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
	if len(bk) != 5 || bk[0]["Primary"] != "Y" || bk[0]["IFSC"] != "TEST0000001" || bk[1]["Status"] != "Inactive" || bk[1]["Primary"] != "N" || bk[4]["Account"] != "12AB" {
		t.Errorf("banks %v", bk)
	}
	if len(issues(r, "Client bank accounts", "IFSC")) == 0 {
		t.Error("bad IFSC not reported")
	}
	// nominees: only the last 4 digits of an Aadhaar
	_, nm := readCSV(t, r, "firms/02/master/NOMINEES.csv")
	if len(nm) != 1 || nm[0]["IDType"] != "AADHAAR" || nm[0]["ID"] != "4321" {
		t.Errorf("nominees %v", nm)
	}
	// segment status history
	_, ss := readCSV(t, r, "firms/02/master/SEGMENT-STATUS.csv")
	got := []string{}
	for _, s := range ss {
		got = append(got, s["UCC"]+":"+s["Segment"]+":"+s["Status"]+":"+s["EffectiveDate"])
	}
	want := "TEST001:CM:Active:2021-01-01,TEST001:FO:Active:2021-01-01,TEST002:CM:Active:2020-01-01,TEST002:CM:Dormant:2025-06-01,TEST003:CM:Active:2019-01-01,TEST003:CM:Closed:2024-01-01,TEST004:CM:Active:2022-01-01"
	if strings.Join(got, ",") != want {
		t.Errorf("segment status\n got %s\nwant %s", strings.Join(got, ","), want)
	}
	// holdings: two DP accounts' rows are one per client and ISIN; the unlinked one is reported
	_, hd := readCSV(t, r, "firms/02/master/DP-HOLDINGS.csv")
	if len(hd) != 1 || hd[0]["Qty"] != "100" || hd[0]["Pledged"] != "10" || hd[0]["Symbol"] != "TESTCO" {
		t.Errorf("holdings %v", hd)
	}
	if len(issues(r, "DP holdings", "no trading code")) != 1 {
		t.Error("unlinked DP account not reported")
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
	if len(kr) != 1 || kr[0]["Status"] != "Registered" || len(ck) != 1 || ck[0]["CKYCNumber"] != "12345678901234" {
		t.Errorf("kra %v ckyc %v", kr, ck)
	}
	// checks come from the server's own totals
	for name, want := range map[string]string{pkg.CheckTBDebit: "5200.00", pkg.CheckTBCredit: "5200.00", pkg.CheckClients: "4", pkg.CheckClientDebit: "1200.00",
		pkg.CheckClientCredit: "300.00", pkg.CheckDPAccounts: "1", pkg.CheckDPQuantity: "100", pkg.CheckBankAccounts: "3", pkg.CheckPositions: "2"} {
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
	if dropped["Client web password"] != 2 || dropped["DP web password"] != 2 || dropped["Aadhaar number (first holder)"] != 2 {
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
	if dr != cr || dr != money.MustAmount("5200") {
		t.Fatalf("trial balance %s / %s: %v", dr, cr, ob)
	}
	c1 := acct["CLIENT:TEST001"]
	if c1["Debit"] != "1200.00" || c1["Since"] != "2026-04-05" {
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
	if len(issues(r, "Opening balances", "do not net to zero")) != 0 {
		t.Error("balanced trial balance reported as unbalanced")
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
	if len(cl) != 2 || cl[0]["UCC"] != "TEST001" || cl[1]["UCC"] != "TEST004" {
		t.Fatalf("clients %v", cl)
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
	errs := issues(r, "Opening balances", "Dormant client left out")
	if len(errs) != 1 || errs[0].Key != "TEST003" || errs[0].Severity != "Error" {
		t.Errorf("skipped client with balance: %+v", errs)
	}
	if check(r, pkg.CheckClients) != "2" {
		t.Errorf("clients check %s", check(r, pkg.CheckClients))
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
	// the guard itself
	for _, q := range []string{"SELECT Web_Pwd FROM DLMAST", "SELECT FHOLDER_ADHAR FROM CLLIST", "SELECT Content FROM SYSTEMLOGFILES", "SELECT PASSWORD FROM SHUSER", "SELECT UID FROM CDNOMREG"} {
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
	h.on("hist-trades", migrate.Row{"co": "02", "stno": "2025081", "sttype": "N", "trdt": d("2025-05-01"), "subcd": "TEST001", "ctrdno": "T1", "ordno": "O1", "itemcd": "SC001", "qty": "-10", "act_rate": "1000.00", "brkg": "3.00", "cntrno": int64(501), "npk": int64(9), "nse_itemcd": "TESTCO", "isin": "INE000A01011"})
	h.on("hist-contract-notes", migrate.Row{"co": "02", "cntrno": int64(501), "trdt": d("2025-05-01"), "subcd": "TEST001"})
	h.on("hist-dp", migrate.Row{"co": "06", "trdt": d("2025-06-01"), "benacnum": "00000001", "isincd": "INE000A01011", "drcr": "D", "qtybooked": "5.000", "trancode": "ODTA", "npk": int64(3), "security": "TESTCO EQ"})
	h.on("hist-hold-date", migrate.Row{"d": d("2026-03-31")})
	h.on("hist-holdings", migrate.Row{"co": "06", "benacnum": "00000001", "isincd": "INE000A01011", "balance": "95.000", "curbal": "90.000", "pldgbal": "5.000", "rate": "12.50"})
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
	for kind, rows := range map[string]int{pkg.HistoryBills: 1, pkg.HistoryTrades: 1, pkg.HistoryContractNotes: 1, pkg.HistoryReceipts: 1, pkg.HistoryDP: 1, pkg.HistoryHoldings: 1, pkg.HistoryDPBills: 0} {
		head, got := readCSV(t, r, "firms/02/history/2025-26/"+kind+".csv")
		if strings.Join(head, ",") != strings.Join(pkg.HistoryColumns[kind], ",") || len(got) != rows {
			t.Errorf("%s: header %v rows %d", kind, head, len(got))
		}
	}
	_, tr := readCSV(t, r, "firms/02/history/2025-26/TRADES.csv")
	if tr[0]["Side"] != "S" || tr[0]["Quantity"] != "10" || tr[0]["Brokerage"] != "30.00" || tr[0]["ISIN"] != "INE000A01011" {
		t.Errorf("trade %v", tr[0])
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
		if strings.Contains(q, "[NPK]") || strings.Contains(q, "t.NPK") || !strings.Contains(q, "NULL AS npk") {
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
	r := build(t, func(w *pkg.Writer) {
		if err := m.Current(ctx, co, ck, migrate.Plan{Firms: []string{"02"}}, w, nil); err != nil {
			t.Fatal(err)
		}
		if err := m.Documents(ctx, ml, k, migrate.Plan{Firms: []string{"02"}, Documents: true}, w, nil); err != nil {
			t.Fatal(err)
		}
	})
	_, idx := readCSV(t, r, "firms/02/documents/index.csv")
	if len(idx) != 3 {
		t.Fatalf("documents %v", idx)
	}
	cn := idx[0]
	if cn["Type"] != pkg.DocContractNote || cn["Signed"] != "Y" || cn["Format"] != "pdf" || cn["UCC"] != "TEST001" || cn["FY"] != "2026-27" || cn["RetainUntil"] != "2034-05-02" || cn["To"] != "asha@example.com" {
		t.Errorf("contract note %v", cn)
	}
	b, err := r.ReadAll(cn["Path"], 1<<20)
	if err != nil {
		b2, err2 := r.OpenDocument(cn["Path"], cn["SHA256"])
		if err2 != nil {
			t.Fatal(err, err2)
		}
		buf := new(bytes.Buffer)
		buf.ReadFrom(b2)
		b2.Close()
		b = buf.Bytes()
	}
	if string(b) != "%PDF-1.4 S" {
		t.Errorf("stored %q, want the signed PDF", b)
	}
	if idx[1]["Type"] != pkg.DocStatement || idx[1]["Signed"] != "N" || idx[1]["Format"] != "pdf" {
		t.Errorf("ledger %v", idx[1])
	}
	if idx[2]["Type"] != pkg.DocDPStatement || idx[2]["UCC"] != "TEST001" || idx[2]["Format"] != "htm" {
		t.Errorf("dp statement %v", idx[2])
	}
	if len(issues(r, "Documents", "larger than")) != 1 || len(issues(r, "Documents", "no attachment")) != 1 {
		t.Errorf("issues %+v", r.Manifest.Issues)
	}
	q := ml.asked("attachments")
	// text columns are read as their bytes, not through the code page
	if len(q) != 1 || !strings.Contains(q[0], "SUBSTRING(a.Signed, 1, 4) = '%PDF'") || !strings.Contains(q[0], fmt.Sprint(MaxAttachment)) ||
		!strings.Contains(q[0], "CAST(CAST(a.contents AS varchar(max)) AS varbinary(max))") {
		t.Errorf("attachment query %v", q)
	}
	if check(r, pkg.CheckDocuments) != "3" {
		t.Errorf("documents check %s", check(r, pkg.CheckDocuments))
	}
}

func TestDocType(t *testing.T) {
	for _, c := range [][4]string{
		{"Contract", "Client", "Contract Note", pkg.DocContractNote},
		{"Margin2Clients", "Client", "", pkg.DocMargin},
		{"", "DP CLIENT", "Transaction statement", pkg.DocDPStatement},
		{"DPBLPRN2", "Client", "DP Bill", pkg.DocDPBill},
		{"Vallan Bills", "Client", "", pkg.DocBill},
		{"RunAcRep2Client", "Client", "", pkg.DocStatement},
		{"Payout Report", "Branchid", "", pkg.DocOther},
	} {
		if got := docType(c[0], strings.ToUpper(c[1]), c[2], ""); got != c[3] {
			t.Errorf("%v: %s", c, got)
		}
	}
}

func TestEntityName(t *testing.T) {
	a := entityName("ABC SECURITIES PVT. LTD. (NSE)")
	for _, s := range []string{"ABC SECURITIES PVT LTD(C - MCX)", "ABC Securities Private Limited (CDSL)", "ABC SECURITIES PVT. LTD"} {
		if entityName(s) != a {
			t.Errorf("%q -> %q, want %q", s, entityName(s), a)
		}
	}
	if s := segFromName("ABC SECURITIES PVT. LTD(C - NSE)", "", false); s.label() != "NSE-CD" {
		t.Errorf("currency %v", s)
	}
}
