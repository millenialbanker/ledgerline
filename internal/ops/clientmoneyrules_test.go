package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// All data here is made up: the sample firm and records the tests add.

// NSE/INSP/53820 FAQ 5, row 1: margin 1,00,000 and pledged securities of
// 2,00,000 after haircut cover the 225%, so only the 50% cash margin is
// kept for margin (the pay-in obligation comes on top).
func TestRASCapNetOfCollateral(t *testing.T) {
	m := money.MustDec("100000")
	if c := rasCap(m, money.MustDec("200000")); c.Fixed(2) != "50000.00" {
		t.Fatalf("cap %s", c.Fixed(2))
	}
	if c := rasCap(m, money.MustDec("100000")); c.Fixed(2) != "125000.00" {
		t.Fatalf("cap with less collateral %s", c.Fixed(2))
	}
	if keep, c := rasRetention(m, money.MustDec("200000"), 100); keep.Fixed(2) != "50000.00" || c.Fixed(2) != "50000.00" {
		t.Fatalf("keep %s cap %s", keep.Fixed(2), c.Fixed(2))
	}
	// No collateral: the firm's 50% buffer is below the 225% cap.
	if keep, _ := rasRetention(m, money.Dec{}, 50); keep.Fixed(2) != "150000.00" {
		t.Fatalf("keep %s", keep.Fixed(2))
	}
	if keep, c := rasRetention(money.Dec{}, money.MustDec("5000"), 100); keep.Sign() != 0 || c.Sign() != 0 {
		t.Fatalf("no margin: keep %s", keep.Fixed(2))
	}
}

// A settlement row shows the margin, collateral, cap and the T and T-1
// pay-in, and never keeps more than the cap.
func TestRASRowRetention(t *testing.T) {
	o, ctx := setup(t)
	v, err := o.RAS(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Rows) == 0 {
		t.Fatal("no credit balances in the sample")
	}
	for _, r := range v.Rows {
		if r.Retention.Cmp(r.Cap) > 0 || r.Payout.Cmp(money.Max(r.Credit.Sub(r.Retention), money.Dec{})) != 0 {
			t.Fatalf("%s keeps %s over the cap %s", r.UCC, r.Retention.Fixed(2), r.Cap.Fixed(2))
		}
	}
}

func TestClientJournalRule(t *testing.T) {
	o, ctx := setup(t)
	cs, _ := o.ClientMap(ctx, T)
	us := sortedKeys(cs)
	a, b := ledger.ClientAccount(us[0]), ledger.ClientAccount(us[1])
	jv := func(dr, cr string) error {
		_, err := o.SaveVoucher(ctx, T, "priya.nair", Voucher{Type: "Journal", Date: D, Narration: "test", Lines: []VoucherLine{{Account: dr, Debit: 10000}, {Account: cr, Credit: 10000}}})
		return err
	}
	for _, x := range [][2]string{{a, b}, {a, ledger.BankOwn}, {a, ledger.SuspenseBank}, {a, PayableOFSMargin}, {a, ledger.CCCollateral}} {
		if err := jv(x[0], x[1]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("journal %s → %s allowed: %v", x[0], x[1], err)
		}
	}
	if err := jv(a, b); err == nil || !strings.Contains(err.Error(), "inter-client") {
		t.Fatalf("inter-client message: %v", err)
	}
	// Levying and reversing charges stay allowed.
	if err := jv(a, ledger.IncomeBrokerage); err != nil {
		t.Fatal(err)
	}
	if err := jv(ledger.PayableGST, a); err != nil {
		t.Fatal(err)
	}
	// A journal on no client ledger is not touched by the rule.
	if err := jv("EXPENSE:RENT", ledger.BankOwn); err != nil {
		t.Fatal(err)
	}
	// The self-inspection finds a journal posted another way.
	_ = docs.Upsert(ctx, o.Docs, T, cVoucher, "JV-old", Voucher{No: "JV/old", Type: "Journal", Date: D, Status: "Posted", Total: 5000, Lines: []VoucherLine{{Account: a, Debit: 5000}, {Account: b, Credit: 5000}}}, "test")
	siWant(t, siOne(t, o, ctx, "client_journals", "2026-10-19"), SIFail, "an inter-client journal")
}

func TestFirmBankRules(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	exs := o.reportingExchanges(ctx, T)
	if len(exs) == 0 {
		t.Fatal("no exchanges")
	}
	save := func(b FirmBank) (FirmBank, error) {
		return o.SaveFirmBank(ctx, T, siCO, FirmBankReq{FirmBank: b})
	}
	base := FirmBank{Kind: BankUSCNBA, Bank: "Made-up Bank", Account: "000111222333", IFSC: "MADE0000002"}
	bad := base
	bad.Name = set.Firm + " client account"
	if _, err := save(bad); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "USCNB account") {
		t.Fatalf("a wrong name saved: %v", err)
	}
	bad = base
	bad.Overdraft = true
	if _, err := save(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an overdraft USCNBA saved: %v", err)
	}
	bad = base
	bad.UPIID = "firm@okbank"
	if _, err := save(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a UPI ID that is not @valid saved: %v", err)
	}
	ok := base
	ok.Name, ok.OpenedOn = set.Firm+" – USCNB A/c", "2026-10-12"
	u, err := save(ok)
	if err != nil {
		t.Fatal(err)
	}
	if why := bankNameProblem(set, u); why != "" {
		t.Fatalf("name: %s", why)
	}
	// Reports to each exchange and the undertaking are due 7 working days
	// after opening; the register warns, the self-inspection fails once
	// they are late, and the day's work items are queued.
	its := bankReportItems(set, exs, u)
	if len(its) != len(exs)+1 || its[0].Due != addWorkingDays(set, "2026-10-12", 7) {
		t.Fatalf("items %+v", its)
	}
	v, _ := o.FirmBanks(ctx, T, siCO)
	if !strings.Contains(strings.Join(v.Warnings, "\n"), "@valid UPI ID") {
		t.Fatalf("no UPI warning: %v", v.Warnings)
	}
	late := addWorkingDays(set, its[0].Due, 1)
	siWant(t, siOne(t, o, ctx, "firm_bank_register", late), SIFail, "reports not recorded")
	if _, err := o.RunStage(ctx, T, SystemActor, D, "", "client_money"); err != nil {
		t.Fatal(err)
	}
	w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-client_money-bank-report-"+u.ID+"-opening-"+exs[0])
	if err != nil || w.Status != "Open" {
		t.Fatalf("work item %+v %v", w, err)
	}
	// Reported on time, undertaking given, UPI ID linked: passes.
	u.ReportedOn = map[string]string{}
	for _, x := range exs {
		u.ReportedOn[x] = "2026-10-14"
	}
	u.UndertakingOn, u.UPIID, u.Account = "2026-10-15", "firm.brk@validmade", ""
	if u, err = save(u); err != nil || !u.Reported {
		t.Fatalf("saved %+v %v", u, err)
	}
	siWant(t, siOne(t, o, ctx, "firm_bank_register", late), SIPass, "all recorded")
	if _, err := o.RunStage(ctx, T, SystemActor, D, "", "client_money"); err != nil {
		t.Fatal(err)
	}
	if w, _, _ = docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, w.ID); w.Status == "Open" {
		t.Fatalf("work item still open: %+v", w)
	}
	// Closed: the closure is reported to each exchange within 7 working days.
	u.ClosedOn = "2026-10-20"
	if u, err = save(u); err != nil {
		t.Fatal(err)
	}
	siWant(t, siOne(t, o, ctx, "firm_bank_register", addWorkingDays(set, "2026-10-20", 8)), SIFail, "closure not reported")
}

func TestWithdrawReasons(t *testing.T) {
	o, ctx := setup(t)
	_, dsc, sett, _ := moneyAccounts(t, o)
	rec := func(to, why string) error {
		_, err := o.RecordFirmTransfer(ctx, T, "priya.nair", FirmTransfer{Date: D, Purpose: "release", From: "CC", To: to, Withdraw: why, Amount: 10000, Ref: "UTRW" + to + why})
		return err
	}
	if err := rec(dsc.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("release without a reason: %v", err)
	}
	if err := rec(sett.ID, "withdrawal"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a client's withdrawal to the settlement account: %v", err)
	}
	if err := rec(dsc.ID, "brokerage"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("brokerage to the DSCNBA: %v", err)
	}
	if err := rec(dsc.ID, "withdrawal"); err != nil {
		t.Fatal(err)
	}
	if err := rec(sett.ID, "statutory"); err != nil {
		t.Fatal(err)
	}
}

// FAQ 5 of NCL/CMPL/76318: money paid after the cut-off first meets the
// client's debit; only the surplus is retained under RC01.
func TestSEGMONLateReceiptsDebitFirst(t *testing.T) {
	mk := func(before, late string) (map[string]*segmonClient, []segmonRow) {
		b, l := money.MustDec(before), money.MustDec(late)
		c := &segmonClient{ucc: "ZZ1", late: l, bal: segBal{A: b.Add(l), B: b.Add(l), Peak: b.Add(l)}, alloc: map[string]AllocationRow{}, segs: []string{"CM"}}
		all := map[string]*segmonClient{"ZZ1": c}
		return all, segmonRows(all, []string{"ZZ1"})
	}
	sum := func(ls []RetentionLine) map[string]string {
		out := map[string]string{}
		for _, l := range ls {
			out[l.Code] = l.Amount.Fixed(2)
		}
		return out
	}
	all, rows := mk("-100", "300")
	if got := sum(segmonRetained(all, rows, false)); got["RC01"] != "200.00" || len(got) != 1 {
		t.Fatalf("debit met first: %v", got)
	}
	all, rows = mk("-500", "300")
	if got := segmonRetained(all, rows, false); len(got) != 0 {
		t.Fatalf("debit not covered, yet retained: %v", got)
	}
	all, rows = mk("100", "300")
	if got := sum(segmonRetained(all, rows, false)); got["RC01"] != "300.00" || got["RC09"] != "100.00" {
		t.Fatalf("credit before the late money: %v", got)
	}
	// Funds held for a payout: RC04, RC08 on a settlement day.
	all, rows = mk("100", "0")
	all["ZZ1"].held = money.MustDec("100")
	if got := sum(segmonRetained(all, rows, true)); got["RC08"] != "100.00" || len(got) != 1 {
		t.Fatalf("payout hold on a settlement day: %v", got)
	}
}

// Unidentified receipts under SUSPE1234N on the member's PAN, OFS margin
// in column 46, and a reason line per retention code with blank client
// fields.
func TestSEGMONSuspenseOFSAndReasonLines(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	pan := firmPAN(set)
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "SUSP:ZZREF1", ValueDate: D, CreatedBy: "test", Narration: "unmatched", Source: ledger.SourceRef{Kind: "BANK", ID: "ZZREF1"},
		Postings: []ledger.Posting{{Account: ledger.BankClient, Debit: 77700}, {Account: ledger.SuspenseBank, Credit: 77700}}}); err != nil {
		t.Fatal(err)
	}
	cs, _ := o.ClientMap(ctx, T)
	var u string
	for _, k := range sortedKeys(cs) {
		if cs[k].Status == "Active" && rePAN.MatchString(cs[k].PAN) {
			u = k
			break
		}
	}
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "OFSM:ZZ:1", ValueDate: D, CreatedBy: "test", Narration: "ofs", Source: ledger.SourceRef{Kind: "OFS-MARGIN", ID: "ZZ"},
		Postings: []ledger.Posting{{Account: ledger.ClientAccount(u), Debit: 50000}, {Account: PayableOFSMargin, Credit: 50000}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordRetention(ctx, T, "priya.nair", RetentionEntry{Date: D, Code: "RC07", Amount: 12345, Ref: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("RC07 without the bank's evidence: %v", err)
	}
	if _, err := o.RecordRetention(ctx, T, "priya.nair", RetentionEntry{Date: D, Code: "RC09", Amount: 12345, Note: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("RC09 recorded by hand: %v", err)
	}
	if _, err := o.RecordRetention(ctx, T, "priya.nair", RetentionEntry{Date: D, Code: "RC07", Amount: 12345, Evidence: "bank's e-mail of the day", Ref: "SR-1"}); err != nil {
		t.Fatal(err)
	}
	chk, err := o.SEGMONCheck(ctx, T, "priya.nair", D)
	if err != nil {
		t.Fatal(err)
	}
	if !chk.OK {
		t.Fatalf("errors %+v", chk.Errors[:min(5, len(chk.Errors))])
	}
	// The sample day has unidentified credits of its own; 777 is added.
	sus := chk.Totals.Suspense.Fixed(2)
	if chk.Totals.OFS.Fixed(2) != "500.00" || chk.Totals.Suspense.Cmp(money.MustDec("777")) <= 0 {
		t.Fatalf("totals %+v", chk.Totals)
	}
	_, b, err := o.ExchangeFile(ctx, T, "priya.nair", "segmon-client-funds", D, XFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ls := segmonLines(t, b)
	s := ls[SuspenseUCC+"|CM|NON MTF"]
	if s == nil || s[5] != pan || s[9] != sus || s[29] != sus || s[37] != sus || s[45] != "0.00" || s[21] != "0.00" {
		t.Fatalf("suspense line %v", s)
	}
	r := ls["||RC07"]
	if r == nil || r[21] != "123.45" || r[2] != pan || r[3] != "" || r[5] != "" || r[6] != "" || r[63] != "NA" || r[9] != "0.00" {
		t.Fatalf("reason line %v", r)
	}
	cm := ls[u+"|CM|NON MTF"]
	if cm == nil {
		t.Fatal("client line missing")
	}
	var alloc money.Dec
	al, _ := o.Allocation(ctx, T, D)
	for _, a := range al {
		if a.UCC == u && a.Segment == "CM" {
			alloc = alloc.Add(a.Cash)
		}
	}
	if cm[45] != alloc.Add(money.MustDec("500")).Fixed(2) {
		t.Fatalf("OFS margin not in column 46: %s (allocation %s)", cm[45], alloc.Fixed(2))
	}
	// SUSPE1234N is never a client.
	c := cs[u]
	c.UCC = SuspenseUCC
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "SUSPE1234N") {
		t.Fatalf("SUSPE1234N accepted as a client code: %v", err)
	}
}

func TestPayoutChequeAndRC05(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	cs, _ := o.ClientMap(ctx, T)
	var u string
	for _, k := range sortedKeys(cs) {
		if w, err := o.Withdrawable(ctx, T, D, k); err == nil && w.Available >= 1000000 && cs[k].Deceased == nil {
			u = k
			break
		}
	}
	if u == "" {
		t.Fatal("no client with ₹10,000 free")
	}
	p, err := o.RequestPayout(ctx, T, "priya.nair", D, u, 1000000, "Head office")
	if err != nil {
		t.Fatal(err)
	}
	if p, _, _ = docs.Get[PayoutRequest](ctx, o.Docs, T, cPayout, p.ID); p.Status == "Requested" {
		if _, err := o.DecidePayout(ctx, T, "farhan.khan", D, p.ID, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.IssuePayoutCheque(ctx, T, "farhan.khan", p.ID, "000123", D); !errors.Is(err, ErrState) {
		t.Fatalf("a cheque before the transfer failed: %v", err)
	}
	b, err := o.CreatePayoutBatch(ctx, T, "farhan.khan", D)
	if err != nil {
		t.Fatal(err)
	}
	// Sent and not confirmed: RC04 that day.
	rr, err := o.retentionReport(ctx, T, D, nil, nil)
	if err != nil || rr.byCode("RC04").Sign() <= 0 {
		t.Fatalf("payout with the bank not in RC04: %+v %v", rr.Codes, err)
	}
	if _, err := o.ConfirmPayouts(ctx, T, "priya.nair", b.ID, nil, map[string]string{p.ID: "account frozen"}); err != nil {
		t.Fatal(err)
	}
	q, err := o.IssuePayoutCheque(ctx, T, "farhan.khan", p.ID, "000123", D)
	if err != nil {
		t.Fatal(err)
	}
	rr, _ = o.retentionReport(ctx, T, D, nil, nil)
	if rr.byCode("RC05").Fixed(2) != "10000.00" {
		t.Fatalf("uncleared cheque not in RC05: %+v", rr.Codes)
	}
	// After five settlement days it is upstreamed on the sixth: RC09 and a
	// work item.
	d6 := D
	for i := 0; i < 6; i++ {
		d6 = set.NextBusinessDay(d6)
	}
	rr, _ = o.retentionReport(ctx, T, d6, nil, nil)
	if rr.byCode("RC05").Sign() != 0 || rr.byCode("RC09").Sign() <= 0 {
		t.Fatalf("cheque past five settlement days: %+v", rr.Codes)
	}
	if _, err := o.RunStage(ctx, T, SystemActor, d6, "", "client_money"); err != nil {
		t.Fatal(err)
	}
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-client_money-cheque6-"+q.ID); err != nil || w.Status != "Open" {
		t.Fatalf("no day-6 work item: %+v %v", w, err)
	}
	// Cleared: the settlement date is the day of credit.
	cd := set.NextBusinessDay(D)
	if _, err := o.ClearPayoutCheque(ctx, T, "priya.nair", q.ID, cd); err != nil {
		t.Fatal(err)
	}
	p, _, _ = docs.Get[PayoutRequest](ctx, o.Docs, T, cPayout, p.ID)
	if p.Status != "Paid" || p.SettledOn != cd {
		t.Fatalf("request %+v", p)
	}
	if rr, _ = o.retentionReport(ctx, T, cd, nil, nil); rr.byCode("RC05").Sign() != 0 {
		t.Fatalf("cleared cheque still retained: %+v", rr.Codes)
	}
	if _, err := o.CancelPayoutCheque(ctx, T, "farhan.khan", q.ID, cd, "stopped"); !errors.Is(err, ErrState) {
		t.Fatalf("a cleared cheque cancelled: %v", err)
	}
}

func TestClientFundFDRs(t *testing.T) {
	o, ctx := setup(t)
	usc, dsc, _, _ := moneyAccounts(t, o)
	fd := Deposit{CC: "NSE Clearing", Kind: "Fixed deposit", Issuer: "Made-up Bank", Ref: "FDR-1", Amount: money.MustDec("500000"), Issued: "2026-10-15", Expiry: "2027-10-16",
		ClientFunds: true, FundedFrom: usc.ID, PrincipalProtected: true, PreTerminable: true, Lien: true}
	for name, f := range map[string]func(d *Deposit){
		"from the DSCNBA":      func(d *Deposit) { d.FundedFrom = dsc.ID },
		"over 1 year 1 day":    func(d *Deposit) { d.Expiry = "2027-10-17" },
		"no lien":              func(d *Deposit) { d.Lien = false },
		"used for a facility":  func(d *Deposit) { d.BankFacility = true },
		"not pre-terminable":   func(d *Deposit) { d.PreTerminable = false },
		"a bank guarantee":     func(d *Deposit) { d.Kind = "Bank guarantee" },
		"no issue date at all": func(d *Deposit) { d.Issued = "" },
	} {
		x := fd
		f(&x)
		if _, err := o.SaveDeposit(ctx, T, "farhan.khan", x); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s saved: %v", name, err)
		}
	}
	d, err := o.SaveDeposit(ctx, T, "farhan.khan", fd)
	if err != nil {
		t.Fatal(err)
	}
	// Lien-marked and not yet deposited: in transit (RC02).
	rr, _ := o.retentionReport(ctx, T, D, nil, nil)
	if rr.byCode("RC02").Fixed(2) != "500000.00" {
		t.Fatalf("FDR in transit %+v", rr.Codes)
	}
	siWant(t, siOne(t, o, ctx, "client_fdrs", D), SIPass, "a good client FDR")
	// Not deposited after five settlement days.
	set, _ := o.Settings(ctx, T)
	siWant(t, siOne(t, o, ctx, "client_fdrs", addWorkingDays(set, "2026-10-15", 6)), SIFail, "still in transit")
	d.DepositedOn = "2026-10-16"
	if _, err := o.SaveDeposit(ctx, T, "farhan.khan", d); err != nil {
		t.Fatal(err)
	}
	if rr, _ = o.retentionReport(ctx, T, D, nil, nil); rr.byCode("RC02").Sign() != 0 {
		t.Fatalf("deposited FDR still in transit %+v", rr.Codes)
	}
	// Not the firm's collateral.
	v, _ := o.Deposits(ctx, T, D)
	for _, c := range v.ByCC {
		if c.CC == "NSE Clearing" && c.Value.Cmp(money.MustDec("500000")) >= 0 {
			t.Fatalf("client FDR counted as the firm's collateral: %+v", c)
		}
	}
}

// With the register, money left in the USCNBA without a reason is flagged
// and the nightly test fails; the reason recorded explains it.
func TestClientMoneyNil(t *testing.T) {
	o, ctx := setup(t)
	moneyAccounts(t, o)
	set, _ := o.Settings(ctx, T)
	cs, _ := o.ClientMap(ctx, T)
	u := sortedKeys(cs)[0]
	// A client's money received into the USCNBA and never upstreamed.
	if _, _, err := o.App.Ledger.Post(ctx, ledger.Entry{Tenant: T, Key: "RCPT:ZZNIL", ValueDate: D, CreatedBy: "test", Narration: "Funds received", Source: ledger.SourceRef{Kind: "BANK", ID: "ZZNIL"},
		Postings: []ledger.Posting{{Account: ledger.BankClient, Debit: 4000000}, {Account: ledger.ClientAccount(u), Credit: 4000000}}}); err != nil {
		t.Fatal(err)
	}
	r, err := o.buildMoneyRecon(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	if moneyHas(r, "not_upstreamed") == 0 {
		t.Fatalf("no not_upstreamed flag: %+v", r.Flags)
	}
	siWant(t, siOne(t, o, ctx, "client_money_nil", set.NextBusinessDay(D)), SIFail, "money left in the USCNBA")
}
