package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// Synthetic clients only: made-up codes, accounts, IFSCs and emails.

type hdfcCase struct {
	ucc, name, ifsc, acct, bank, branch, email string
	amount                                     money.Amount
	rtgs                                       bool
}

func hdfcSetup(t *testing.T, mut func(*Settings)) (*Ops, context.Context) {
	t.Helper()
	o, ctx := newOps(t)
	docs.Upsert(ctx, o.Docs, T, cAutoPol, "payouts", AutoPolicy{ID: "payouts", Mode: ModeOff}, "test")
	set, _ := o.Settings(ctx, T)
	set.HDFCClientID, set.HDFCInstrPrefix = "TESTCORP01", "9"
	if mut != nil {
		mut(&set)
	}
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	return o, ctx
}

func hdfcClients(t *testing.T, o *Ops, ctx context.Context, cs []hdfcCase) {
	t.Helper()
	for _, c := range cs {
		cl := Client{UCC: c.ucc, Name: c.name, Status: "Active", Tags: []string{}, Email: c.email, Bank: BankAccount{Bank: c.bank, IFSC: c.ifsc, Account: c.acct, Branch: c.branch}}
		if err := docs.Upsert(ctx, o.Docs, T, cClient, c.ucc, cl, "test"); err != nil {
			t.Fatal(err)
		}
	}
}

// hdfcApproved stores approved, unreleased payout requests.
func hdfcApproved(t *testing.T, o *Ops, ctx context.Context, date string, cs []hdfcCase) {
	t.Helper()
	for _, c := range cs {
		n, _ := o.next(ctx, T, "payout", "test")
		p := PayoutRequest{ID: fmt.Sprintf("PO-%06d", n), Date: date, UCC: c.ucc, Name: c.name, Amount: c.amount, Source: "Head office", Status: "Approved", Maker: "priya.nair", Checker: "farhan.khan", ExpiresOn: date, RTGS: c.rtgs}
		if err := docs.Upsert(ctx, o.Docs, T, cPayout, p.ID, p, "test"); err != nil {
			t.Fatal(err)
		}
	}
}

func hdfcBatch(t *testing.T, o *Ops, ctx context.Context, date string, cs []hdfcCase) PayoutBatch {
	t.Helper()
	hdfcClients(t, o, ctx, cs)
	hdfcApproved(t, o, ctx, date, cs)
	b, err := o.CreatePayoutBatch(ctx, T, "farhan.khan", date)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var hdfcMix = []hdfcCase{
	{"ZQ01", "TEST CLIENT ONE", "SBIN0001234", "123456789012", "STATE BANK OF INDIA", "TEST BRANCH", "one@example.invalid", 1500000, false},
	{"ZQ02", "TEST CLIENT TWO", "ICIC0000002", "987654321098", "ICICI BANK", "", "two@example.invalid", 25000000, true},
	{"ZQ03", "TEST CLIENT THREE", "HDFC0000003", "50100000000003", "HDFC BANK", "", "three@example.invalid", 30000000, false},
	{"ZQ04", "TEST, CLIENT\r\nFOUR", "UTIB0000004", "911010000000004", "AXIS BANK, LTD", "", "four@example.invalid", 20000000, false},
	{"ZQ05", "TEST CLIENT FIVE", "PUNB0000005", "0050000000005", "PUNJAB NATIONAL BANK", "", "five@example.invalid", 60000000, false},
}

func TestHDFCENetFileTypesAndBytes(t *testing.T) {
	o, ctx := hdfcSetup(t, func(s *Settings) { s.RTGSThreshold = "500000.00" })
	const d = "2026-11-04"
	if _, err := o.SetHDFCNextSeq(ctx, T, "admin", "main", d, 817); err != nil {
		t.Fatal(err)
	}
	b := hdfcBatch(t, o, ctx, d, hdfcMix)
	f, err := o.MakeHDFCPayoutFile(ctx, T, "priya.nair", b.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "TESTCORP010411.817" || f.Seq != 817 || f.Rows != 5 || f.Total != b.Total {
		t.Fatalf("file %+v", f)
	}
	// I for the HDFC IFSC (above the threshold too); R when marked or from
	// ₹5 lakh (the setting); N otherwise (₹2 lakh is below this firm's threshold).
	if f.Types["N"] != 2 || f.Types["R"] != 2 || f.Types["I"] != 1 {
		t.Fatalf("types %v", f.Types)
	}
	name, content, err := o.DownloadHDFCPayoutFile(ctx, T, "priya.nair", f.ID)
	if err != nil || name != f.Name {
		t.Fatal(name, err)
	}
	// CRLF on every line, the last one too, no bare LF, no header.
	if bytes.Count(content, []byte("\r\n")) != 5 || bytes.Count(content, []byte("\n")) != 5 || !bytes.HasSuffix(content, []byte("\r\n")) {
		t.Fatalf("line endings: %q", content)
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\r\n"), "\r\n")
	want := "N,ZQ01,123456789012,15000.00,TEST CLIENT ONE,,,,,,,,90000000000001,ZQ01-PAY,,,,,,,,,04/11/2026,,SBIN0001234,STATE BANK OF INDIA,TEST BRANCH,one@example.invalid"
	if lines[0] != want {
		t.Fatalf("line 1\n got %s\nwant %s", lines[0], want)
	}
	for i, l := range lines {
		if n := len(strings.Split(l, ",")); n != 28 {
			t.Fatalf("line %d has %d fields", i+1, n)
		}
	}
	for i, typ := range []string{"N", "R", "I", "N", "R"} {
		if !strings.HasPrefix(lines[i], typ+",") {
			t.Errorf("line %d: %s", i+1, lines[i])
		}
	}
	// Commas and line breaks taken out of the name and the bank name.
	if !strings.Contains(lines[3], ",TEST CLIENT FOUR,") || !strings.Contains(lines[3], ",AXIS BANK LTD,") || !strings.Contains(lines[3], ",90000000000004,ZQ04-PAY,") {
		t.Fatalf("line 4: %s", lines[3])
	}
	// The IFSC is given on the I row as well.
	if !strings.Contains(lines[2], ",HDFC0000003,HDFC BANK,") {
		t.Fatalf("line 3: %s", lines[2])
	}
}

func TestHDFCTxnTypeSettings(t *testing.T) {
	set := DefaultSettings()
	it := func(ifsc string, amt money.Amount, mode string) PayoutItem {
		return PayoutItem{IFSC: ifsc, Amount: amt, Mode: mode}
	}
	cases := []struct {
		s    func(*Settings)
		item PayoutItem
		want string
	}{
		{nil, it("HDFC0000001", 100, "NEFT"), "I"},
		{nil, it("SBIN0000001", 19999999, "NEFT"), "N"},
		{nil, it("SBIN0000001", 20000000, "NEFT"), "R"},
		{nil, it("SBIN0000001", 100, "RTGS"), "R"},
		{func(s *Settings) { s.HDFCNoInternal = true }, it("HDFC0000001", 100, "NEFT"), "N"},
		{func(s *Settings) { s.HDFCNoInternal = true }, it("HDFC0000001", 30000000, "NEFT"), "R"},
		{func(s *Settings) { s.HDFCForceType = "N" }, it("HDFC0000001", 30000000, "RTGS"), "N"},
		{func(s *Settings) { s.RTGSThreshold = "1000000" }, it("SBIN0000001", 50000000, "NEFT"), "N"},
	}
	for i, c := range cases {
		s := set
		if c.s != nil {
			c.s(&s)
		}
		if got := hdfcTxnType(s, c.item); got != c.want {
			t.Errorf("case %d: %s, want %s", i, got, c.want)
		}
	}
}

func hdfcProblems(t *testing.T, o *Ops, ctx context.Context, batch string) []string {
	t.Helper()
	v, err := o.HDFCPayoutFile(ctx, T, batch)
	if err != nil {
		t.Fatal(err)
	}
	_, err = o.MakeHDFCPayoutFile(ctx, T, "priya.nair", batch, "")
	var blk *PayoutFileBlocked
	if len(v.Problems) > 0 && (!errors.As(err, &blk) || len(blk.Problems) < len(v.Problems)) {
		t.Fatalf("file not blocked: %v (view %v)", err, v.Problems)
	}
	if len(v.Problems) == 0 && err != nil {
		t.Fatalf("clean batch blocked: %v", err)
	}
	return v.Problems
}

func hasProblem(ps []string, sub string) bool {
	for _, p := range ps {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

func TestHDFCENetChecks(t *testing.T) {
	const d = "2026-11-04"
	one := func(c hdfcCase) []hdfcCase { return []hdfcCase{c} }
	good := hdfcMix[0]
	check := func(name string, cs []hdfcCase, mutSet func(*Settings), tamper func(o *Ops, ctx context.Context, b PayoutBatch), want ...string) {
		t.Run(name, func(t *testing.T) {
			o, ctx := hdfcSetup(t, mutSet)
			b := hdfcBatch(t, o, ctx, d, cs)
			if tamper != nil {
				tamper(o, ctx, b)
			}
			ps := hdfcProblems(t, o, ctx, b.ID)
			if len(want) == 0 && len(ps) > 0 {
				t.Fatalf("unexpected problems %v", ps)
			}
			for _, w := range want {
				if !hasProblem(ps, w) {
					t.Errorf("no problem %q in %v", w, ps)
				}
			}
		})
	}
	check("clean", one(good), nil, nil)
	check("registered account changed", one(good), nil, func(o *Ops, ctx context.Context, b PayoutBatch) {
		docs.Update(ctx, o.Docs, T, cClient, "ZQ01", "test", func(c *Client) error { c.Bank.Account = "111122223333"; return nil })
	}, "not the client's registered bank account")
	bad := good
	bad.ifsc = "SBIN1001234"
	check("IFSC 5th character", one(bad), nil, nil, "IFSC \"SBIN1001234\" is not valid")
	bad = good
	bad.ifsc = "SBIN00012345"
	check("IFSC length", one(bad), nil, nil, "is not valid")
	bad = good
	bad.acct = "12345ABC9012"
	check("account digits", one(bad), nil, nil, "account number must be 9 to 18 digits")
	check("amount zero", one(good), nil, func(o *Ops, ctx context.Context, b PayoutBatch) {
		docs.Update(ctx, o.Docs, T, cPOBatch, b.ID, "test", func(x *PayoutBatch) error { x.Items[0].Amount = 0; return nil })
	}, "amount must be above zero")
	two := []hdfcCase{good, good}
	check("client twice", two, nil, nil, "client is already in this file")
	check("client twice allowed", two, func(s *Settings) { s.HDFCRepeatClient = true }, nil)
	check("total", one(good), nil, func(o *Ops, ctx context.Context, b PayoutBatch) {
		docs.Update(ctx, o.Docs, T, cPOBatch, b.ID, "test", func(x *PayoutBatch) error { x.Total += 100; return nil })
	}, "does not equal the batch's approved total")
	check("item amount differs from approval", one(good), nil, func(o *Ops, ctx context.Context, b PayoutBatch) {
		docs.Update(ctx, o.Docs, T, cPOBatch, b.ID, "test", func(x *PayoutBatch) error { x.Items[0].Amount += 100; x.Total += 100; return nil })
	}, "differs from the approved", "does not equal the batch's approved total")
	long := good
	long.name = "TEST CLIENT WITH A VERY LONG NAME FOR LIMITS"
	check("name length", one(long), nil, nil, "Beneficiary Name is 44 characters; the limit is 35")
	long = good
	long.bank = strings.Repeat("B", 36)
	check("bank name length", one(long), nil, nil, "Beneficiary Bank Name is 36 characters")
	noMail := good
	noMail.email = ""
	check("email", one(noMail), nil, nil, "no valid registered email")
	uni := good
	uni.name = "TEST CLIENT ÉTÉ"
	check("characters", one(uni), nil, nil, "cannot carry")
	check("forced I on another bank", one(good), func(s *Settings) { s.HDFCForceType = "I" }, nil, "needs an HDFC IFSC")
	check("forced R below RTGS minimum", one(good), func(s *Settings) { s.HDFCForceType = "R" }, nil, "RTGS needs at least")
	check("no client ID", one(good), func(s *Settings) { s.HDFCClientID = "" }, nil, "HDFC ENet client ID")
	check("confirmed batch", one(good), nil, func(o *Ops, ctx context.Context, b PayoutBatch) {
		docs.Update(ctx, o.Docs, T, cPOBatch, b.ID, "test", func(x *PayoutBatch) error { x.Status = "Confirmed"; return nil })
	}, "already confirmed")
}

func TestHDFCENetSequence(t *testing.T) {
	const d1, d2 = "2026-11-04", "2026-11-05"
	file := func(o *Ops, ctx context.Context, d, ucc, series string) HDFCFile {
		t.Helper()
		c := hdfcMix[0]
		c.ucc = ucc
		b := hdfcBatch(t, o, ctx, d, []hdfcCase{c})
		f, err := o.MakeHDFCPayoutFile(ctx, T, "priya.nair", b.ID, series)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	// Running (the default): several files a day each take the next
	// number, the date part repeats, and the count carries over the day.
	o, ctx := hdfcSetup(t, nil)
	if s, _ := o.HDFCNextSeq(ctx, T, "main", d1); s.Next != 1 || s.Mode != HDFCSeqRunning || s.Sample != "TESTCORP010411.001" {
		t.Fatalf("fresh sequence %+v", s)
	}
	o.SetHDFCNextSeq(ctx, T, "admin", "main", d1, 998)
	var got []string
	for i, x := range []struct{ d, ucc string }{{d1, "ZS01"}, {d1, "ZS02"}, {d2, "ZS03"}} {
		f := file(o, ctx, x.d, x.ucc, "")
		got = append(got, f.Name)
		_ = i
	}
	if strings.Join(got, " ") != "TESTCORP010411.998 TESTCORP010411.999 TESTCORP010511.001" {
		t.Fatalf("running: %v", got)
	}
	// The second series counts on its own.
	o.SetHDFCNextSeq(ctx, T, "admin", "second", d2, 609)
	if f := file(o, ctx, d2, "ZS04", "second"); f.Name != "TESTCORP010511.609" || f.Series != "second" {
		t.Fatalf("second series %+v", f)
	}
	if f := file(o, ctx, d2, "ZS05", ""); f.Name != "TESTCORP010511.002" {
		t.Fatalf("main after second %s", f.Name)
	}
	if _, err := o.SetHDFCNextSeq(ctx, T, "priya.nair", "main", d2, 5); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker set the sequence: %v", err)
	}
	if _, err := o.SetHDFCNextSeq(ctx, T, "admin", "main", d2, 1000); err == nil {
		t.Fatal("sequence 1000 accepted")
	}
	// Daily: 001 again on the next day.
	o, ctx = hdfcSetup(t, func(s *Settings) { s.HDFCSeqMode = HDFCSeqDaily })
	got = nil
	for _, x := range []struct{ d, ucc string }{{d1, "ZS01"}, {d1, "ZS02"}, {d2, "ZS03"}, {d2, "ZS04"}} {
		got = append(got, file(o, ctx, x.d, x.ucc, "").Name)
	}
	if strings.Join(got, " ") != "TESTCORP010411.001 TESTCORP010411.002 TESTCORP010511.001 TESTCORP010511.002" {
		t.Fatalf("daily: %v", got)
	}
}

func TestHDFCENetNoDuplicateFile(t *testing.T) {
	o, ctx := hdfcSetup(t, nil)
	const d = "2026-11-04"
	b := hdfcBatch(t, o, ctx, d, hdfcMix[:2])
	f1, err := o.MakeHDFCPayoutFile(ctx, T, "priya.nair", b.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	// Asking again gives the same file, not a second one.
	again, err := o.MakeHDFCPayoutFile(ctx, T, "farhan.khan", b.ID, "")
	if err != nil || again.ID != f1.ID || again.Name != f1.Name {
		t.Fatalf("second file made: %+v %v", again, err)
	}
	_, c1, _ := o.DownloadHDFCPayoutFile(ctx, T, "priya.nair", f1.ID)
	_, c2, _ := o.DownloadHDFCPayoutFile(ctx, T, "farhan.khan", f1.ID)
	if !bytes.Equal(c1, c2) {
		t.Fatal("re-download differs")
	}
	v, _ := o.HDFCPayoutFile(ctx, T, b.ID)
	if len(v.Files) != 1 || v.Active != f1.ID || len(v.Files[0].Downloads) != 2 || v.Files[0].Downloads[1].By != "farhan.khan" || v.Files[0].Content != "" {
		t.Fatalf("file record %+v", v.Files)
	}
	// Cancelling needs a checker and a reason; then a new file may be made.
	if _, err := o.CancelHDFCPayoutFile(ctx, T, "priya.nair", f1.ID, "not uploaded"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker cancelled: %v", err)
	}
	if _, err := o.CancelHDFCPayoutFile(ctx, T, "farhan.khan", f1.ID, " "); err == nil {
		t.Fatal("cancelled without a reason")
	}
	if _, err := o.CancelHDFCPayoutFile(ctx, T, "farhan.khan", f1.ID, "rejected by the bank"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.DownloadHDFCPayoutFile(ctx, T, "priya.nair", f1.ID); err == nil {
		t.Fatal("cancelled file downloaded")
	}
	f2, err := o.MakeHDFCPayoutFile(ctx, T, "priya.nair", b.ID, "")
	if err != nil || f2.ID == f1.ID || f2.Seq != f1.Seq+1 {
		t.Fatalf("replacement %+v %v", f2, err)
	}
	// The replacement carries the same instruction references.
	_, c3, _ := o.DownloadHDFCPayoutFile(ctx, T, "priya.nair", f2.ID)
	if !bytes.Equal(c1, c3) {
		t.Fatalf("replacement lines differ:\n%s\n%s", c1, c3)
	}
	// A different batch's payouts get new references, never reused.
	b2 := hdfcBatch(t, o, ctx, "2026-11-05", hdfcMix[2:3])
	f3, err := o.MakeHDFCPayoutFile(ctx, T, "priya.nair", b2.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	_, c4, _ := o.DownloadHDFCPayoutFile(ctx, T, "priya.nair", f3.ID)
	if !strings.Contains(string(c4), ",90000000000003,") {
		t.Fatalf("reference on day 2: %s", c4)
	}
}

func TestHDFCSettingsValidation(t *testing.T) {
	o, ctx := newOps(t)
	for _, mut := range []func(*Settings){
		func(s *Settings) { s.HDFCFileName = "{hdfc_client_id}{date:0201}" },
		func(s *Settings) { s.HDFCFileName = "{member}.{seq:3}" },
		func(s *Settings) { s.HDFCFileName = "A B.{seq:3}" },
		func(s *Settings) { s.HDFCCustRefSuffix = "PAY-1" },
		func(s *Settings) { s.HDFCInstrPrefix = "12A" },
		func(s *Settings) { s.HDFCInstrPrefix = "123456789" },
		func(s *Settings) { s.RTGSThreshold = "100000" },
		func(s *Settings) { s.HDFCSeqMode = "weekly" },
		func(s *Settings) { s.HDFCForceType = "D" },
		func(s *Settings) { s.HDFCClientID = "AB-12" },
	} {
		set, _ := o.Settings(ctx, T)
		mut(&set)
		if err := o.SaveSettings(ctx, T, "admin", set); err == nil {
			t.Errorf("accepted %+v", set)
		}
	}
	set, _ := o.Settings(ctx, T)
	set.HDFCClientID, set.HDFCFileName = "testcorp01", "{hdfc_client_id}12345678.{seq:3}"
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if s, _ := o.HDFCNextSeq(ctx, T, "", "2026-11-04"); s.Sample != "TESTCORP0112345678.001" {
		t.Fatalf("fixed-number template %+v", s)
	}
}
