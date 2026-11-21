package ops

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
	"ledgerline/internal/hdfcapi/hdfctest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
)

// Made-up employees; every figure below is worked by hand in the comments.

func rs(s string) money.Amount { return money.MustAmount(s) }

func (r *hrRig) structure(t *testing.T, code string, comps ...SalaryComp) {
	t.Helper()
	if _, err := r.o.SaveSalaryStructure(r.ctx, T, "paul.pay", SalaryStructure{Employee: code, From: "2026-04-01", Components: comps}); err != nil {
		t.Fatal(err)
	}
}

// payrollPeople: three made-up people for April 2026 (30 days).
func payrollPeople(t *testing.T, r *hrRig) (Employee, Employee, Employee) {
	a := r.add(t, Employee{Name: "Ravi Test", JoinedOn: "2025-01-01", Gender: "M", State: "Maharashtra", User: "priya.nair", Email: "ravi@example.invalid", PAN: "AAAPR1111R", UAN: "100000000001", ESIC: "1000000001",
		Bank: SalaryBank{Account: "11110000001", IFSC: "SBIN0000001", Holder: "RAVI TEST"}, EPSMember: true})
	b := r.add(t, Employee{Name: "Sunita Test", JoinedOn: "2024-04-01", Gender: "F", State: "West Bengal", PAN: "AAAPS2222S", UAN: "100000000002",
		Bank: SalaryBank{Account: "50100000000002", IFSC: "HDFC0000002", Holder: "SUNITA TEST"}, EPSMember: true})
	c := r.add(t, Employee{Name: "Kiran Test", JoinedOn: "2024-06-01", Gender: "M", State: "Maharashtra", PAN: "AAAPK3333K", UAN: "100000000003", TaxRegime: "old",
		Bank: SalaryBank{Account: "33330000003", IFSC: "ICIC0000003", Holder: "KIRAN TEST"}, EPSMember: true})
	r.structure(t, a.Code, SalaryComp{Code: "BASIC", Amount: rs("12000"), PFWage: true, ESIWage: true}, SalaryComp{Code: "SPECIAL", Amount: rs("8000"), ESIWage: true})
	r.structure(t, b.Code, SalaryComp{Code: "BASIC", Amount: rs("75000"), PFWage: true}, SalaryComp{Code: "HRA", Amount: rs("30000")}, SalaryComp{Code: "SPECIAL", Amount: rs("45000")})
	r.structure(t, c.Code, SalaryComp{Code: "BASIC", Amount: rs("40000"), PFWage: true}, SalaryComp{Code: "HRA", Amount: rs("20000")}, SalaryComp{Code: "SPECIAL", Amount: rs("20000")})
	return a, b, c
}

func TestPayrollRunHandWorked(t *testing.T) {
	r := hrSetup(t)
	o, ctx := r.o, r.ctx
	a, b, c := payrollPeople(t, r)
	// Ravi took one day of leave without pay on Wednesday 8 April.
	lr, err := o.ApplyLeave(ctx, T, "priya.nair", LeaveRequest{Type: "LWP", From: "2026-04-08", To: "2026-04-08", Reason: "Personal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideLeave(ctx, T, "hema.hr", lr.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	// Kiran: old regime, rent ₹25,000 a month in a metro, ₹1,00,000 under 80C.
	if _, err := o.SaveTaxDecl(ctx, T, "paul.pay", c.Code, TaxDecl{FY: "2026-27", Regime: "old", Declared: TaxAmounts{RentMonthly: rs("25000"), Metro: true, LandlordPAN: "AAAPL4444L", C80: rs("100000")}}); err != nil {
		t.Fatal(err)
	}
	run, err := o.MakePayrun(ctx, T, "paul.pay", "2026-04")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Lines) != 3 || len(run.Problems) != 0 {
		t.Fatalf("run %d lines, problems %v", len(run.Lines), run.Problems)
	}
	type want struct {
		gross, empPF, erEPF, erEPS, edli, admin, empESI, erESI, pt, tds, net string
		lop                                                                 float64
	}
	for code, w := range map[string]want{
		// Ravi: 29 of 30 days. Basic 12,000×29/30 = 11,600; special
		// 8,000×29/30 = 7,733.33 → 7,733; gross 19,333. PF wages 11,600
		// (special 7,733 is under half of 19,333): 12% = 1,392 each side;
		// EPS 8.33% = 966.28 → 966, so employer EPF 426; EDLI and admin
		// 0.5% = 58. ESI on 19,333: 0.75% = 145.00 (144.9975 up), 3.25% =
		// 628.32 → 629. PT Maharashtra above ₹10,000: 200. Tax: 19,333 +
		// 11 × 20,000 = 2,39,333 a year, under the rebate: 0. Net 19,333 −
		// 1,392 − 145 − 200 = 17,596.
		a.Code: {"19333", "1392", "426", "966", "58", "58", "145", "629", "200", "0", "17596", 1},
		// Sunita: 1,50,000, new regime. PF on the ₹15,000 ceiling: 1,800;
		// EPS 1,249.50 → 1,250, employer EPF 550; EDLI and admin 75. No
		// ESI. PT West Bengal above ₹40,000: 200. Tax: 18,00,000 − 75,000
		// = 17,25,000: 20,000 + 40,000 + 60,000 + 25,000 = 1,45,000, cess
		// 5,800 = 1,50,800; ÷ 12 = 12,566.67 → 12,567. Net 1,50,000 −
		// 1,800 − 200 − 12,567 = 1,35,433.
		b.Code: {"150000", "1800", "550", "1250", "75", "75", "0", "0", "200", "12567", "135433", 0},
		// Kiran: 80,000, old regime. PF as Sunita. PT 200 (₹2,500 a year:
		// 11 × 200 + 300 in February). Year: 9,60,000 − 50,000 standard −
		// 2,500 PT − HRA 2,40,000 (least of 2,40,000 received, 3,00,000 −
		// 48,000 rent over 10% of basic, 50% of 4,80,000 basic) − 80C
		// 1,21,600 (1,00,000 + PF 21,600) = 5,45,900. Tax 12,500 + 20% of
		// 45,900 (9,180) = 21,680; cess 867; 22,547 → 22,550; ÷ 12 =
		// 1,879.17 → 1,879. Net 80,000 − 1,800 − 200 − 1,879 = 76,121.
		c.Code: {"80000", "1800", "550", "1250", "75", "75", "0", "0", "200", "1879", "76121", 0},
	} {
		l, ok := run.line(code)
		if !ok {
			t.Fatalf("no line for %s", code)
		}
		got := want{l.Gross.String(), l.EmpPF.String(), l.ErEPF.String(), l.ErEPS.String(), l.EDLI.String(), l.PFAdmin.String(), l.EmpESI.String(), l.ErESI.String(), l.PT.String(), l.TDS.String(), l.Net.String(), l.LOP}
		for _, p := range []*string{&w.gross, &w.empPF, &w.erEPF, &w.erEPS, &w.edli, &w.admin, &w.empESI, &w.erESI, &w.pt, &w.tds, &w.net} {
			*p = rs(*p).String()
		}
		if got != w {
			t.Errorf("%s:\n got %+v\nwant %+v\ntax %+v", code, got, w, l.Tax)
		}
	}
	if l, _ := run.line(c.Code); l.Tax.HRAExempt != rs("240000") || l.Tax.Taxable != rs("545900") || l.Tax.TaxYear != rs("22550") {
		t.Fatalf("Kiran's tax working %+v", l.Tax)
	}
	// Maker-checker.
	if _, err := o.ApprovePayrun(ctx, T, "paul.pay", "2026-04"); err == nil {
		t.Fatal("approved a draft")
	}
	if _, err := o.SubmitPayrun(ctx, T, "paul.pay", "2026-04"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApprovePayrun(ctx, T, "paul.pay", "2026-04"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the maker approved: %v", err)
	}
	if _, err := o.MakePayrun(ctx, T, "paul.pay", "2026-04"); err == nil {
		t.Fatal("remade a submitted run")
	}
	run, err = o.ApprovePayrun(ctx, T, "hema.hr", "2026-04")
	if err != nil || run.Status != "Approved" || run.Entry == "" {
		t.Fatalf("approve %+v %v", run.Status, err)
	}
	// The books: balanced, with the statutory dues. PF payable = 4,992
	// employees + 1,526 EPF + 3,466 EPS + 208 EDLI + 208 admin = 10,400.
	bal, _ := o.App.Ledger.Balances(ctx, T, "")
	var sum money.Amount
	for _, v := range bal {
		sum += v
	}
	if sum != 0 {
		t.Fatalf("books do not balance: %s", sum)
	}
	for acct, w := range map[string]string{AcctSalaries: "249333", AcctPFPayable: "-10400", AcctESIPayable: "-774", AcctPTPayable: "-600", AcctTDSSalary: "-14446", AcctSalaryPayable: "-229150", AcctEmployerPF: "5408", AcctEmployerESI: "629"} {
		if bal[acct] != rs(w) {
			t.Errorf("%s = %s, want %s", acct, bal[acct], w)
		}
	}
	// ECR: UAN#~#name#~#gross#~#EPF#~#EPS#~#EDLI wages#~#EE#~#EPS#~#ER#~#NCP#~#refund.
	ecr, err := o.PFECR(ctx, T, "paul.pay", "2026-04")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(ecr)), "\n")
	if len(lines) != 3 || lines[1] != "100000000002#~#SUNITA TEST#~#150000#~#15000#~#15000#~#15000#~#1800#~#1250#~#550#~#0#~#0" ||
		lines[0] != "100000000001#~#RAVI TEST#~#19333#~#11600#~#11600#~#11600#~#1392#~#966#~#426#~#1#~#0" {
		t.Fatalf("ECR:\n%s", ecr)
	}
	esi, _ := o.ESIFile(ctx, T, "paul.pay", "2026-04")
	if !strings.Contains(string(esi), "1000000001,RAVI TEST,29,19333,,") {
		t.Fatalf("ESI file:\n%s", esi)
	}
	pt, _ := o.PTSummary(ctx, T, "paul.pay", "2026-04")
	if len(pt) != 2 || pt[0].State != "Maharashtra" || pt[0].Amount != rs("400") || pt[1].Amount != rs("200") {
		t.Fatalf("PT %+v", pt)
	}
	f138, _ := o.Form138CSV(ctx, T, "paul.pay", "2026-27", 1, false)
	if !strings.Contains(string(f138), "AAAPS2222S,Sunita Test,392 (earlier 192),2026-05-01,150000.00,12567.00") {
		t.Fatalf("Form 138:\n%s", f138)
	}
	// Self-service: own payslip only.
	ps, err := o.MyPayslips(ctx, T, "priya.nair")
	if err != nil || len(ps) != 1 || ps[0].Net != rs("17596") {
		t.Fatalf("my payslips %+v %v", ps, err)
	}
	if b, err := o.PayslipPDF(ctx, T, "priya.nair", "2026-04", a.Code); err != nil || !strings.HasPrefix(string(b), "%PDF") {
		t.Fatalf("own payslip %v", err)
	}
	if _, err := o.PayslipPDF(ctx, T, "priya.nair", "2026-04", b.Code); !errors.Is(err, ErrForbidden) {
		t.Fatalf("saw another person's payslip: %v", err)
	}
	if b, err := o.Form130PartB(ctx, T, "paul.pay", c.Code, "2026-27"); err != nil || !strings.HasPrefix(string(b), "%PDF") {
		t.Fatalf("Form 130: %v", err)
	}
	sent, err := o.EmailPayslips(ctx, T, "paul.pay", "2026-04")
	if err != nil || sent[a.Code] != "Sent (simulated)" || !strings.HasPrefix(sent[b.Code], "Not sent") {
		t.Fatalf("emailed %v %v", sent, err)
	}
	// Paid by the bank file: salary payable to the bank.
	if _, err := o.MarkPayrunPaid(ctx, T, "paul.pay", "2026-04", "2026-05-01", "UTRTEST1"); err != nil {
		t.Fatal(err)
	}
	bal, _ = o.App.Ledger.Balances(ctx, T, "")
	if bal[AcctSalaryPayable] != 0 || bal[ledger.BankOwn] != -rs("229150") {
		t.Fatalf("after payment: payable %s, bank %s", bal[AcctSalaryPayable], bal[ledger.BankOwn])
	}
	// May: TDS carries April's deduction (Sunita's year is the same, so
	// 1,50,800 − 12,567 over 11 months = 12,566.64 → 12,567).
	r.at("2026-06-02 10:00")
	may, err := o.MakePayrun(ctx, T, "paul.pay", "2026-05")
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := may.line(b.Code); l.TDS != rs("12567") || l.Tax.PaidBefore != rs("12567") {
		t.Fatalf("May TDS %s (paid before %s)", l.TDS, l.Tax.PaidBefore)
	}
}

func TestPayrollModuleOffAndAdvances(t *testing.T) {
	r := hrSetup(t)
	o, ctx := r.o, r.ctx
	if _, err := o.SetModules(ctx, T, "admin", ModuleSwitches{HRMS: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.MakePayrun(ctx, T, "paul.pay", "2026-04"); !errors.Is(err, ErrModuleOff) {
		t.Fatalf("payroll off: %v", err)
	}
	if _, err := o.SetModules(ctx, T, "admin", ModuleSwitches{HRMS: true, Payroll: true}); err != nil {
		t.Fatal(err)
	}
	a, _, _ := payrollPeople(t, r)
	l, err := o.GiveAdvance(ctx, T, "paul.pay", Loan{Employee: a.Code, Kind: "Advance", Amount: rs("3000"), Instalment: rs("1000"), StartMonth: "2026-04"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApproveAdvance(ctx, T, "paul.pay", l.ID, "2026-03-20"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker approved the advance: %v", err)
	}
	if _, err := o.ApproveAdvance(ctx, T, "hema.hr", l.ID, "2026-03-20"); err != nil {
		t.Fatal(err)
	}
	run, err := o.MakePayrun(ctx, T, "paul.pay", "2026-04")
	if err != nil {
		t.Fatal(err)
	}
	if x, _ := run.line(a.Code); x.Recovery != rs("1000") || x.Net != rs("20000")-rs("1440")-rs("150")-rs("200")-rs("1000") {
		t.Fatalf("recovery %s net %s", x.Recovery, x.Net)
	}
	o.SubmitPayrun(ctx, T, "paul.pay", "2026-04")
	if _, err := o.ApprovePayrun(ctx, T, "hema.hr", "2026-04"); err != nil {
		t.Fatal(err)
	}
	bal, _ := o.App.Ledger.Balances(ctx, T, "")
	if bal[AcctStaffAdvances] != rs("2000") {
		t.Fatalf("advance left %s", bal[AcctStaffAdvances])
	}
	ln, _, _ := docs.Get[Loan](ctx, o.Docs, T, cLoan, l.ID)
	if ln.Recovered != rs("1000") || ln.Status != "Active" {
		t.Fatalf("loan %+v", ln)
	}
}

func TestSalaryBatchByCBX(t *testing.T) {
	r := hrSetup(t)
	o, ctx := r.o, r.ctx
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	bank := hdfctest.New(&key.PublicKey)
	bank.Ready = 1
	t.Cleanup(bank.Close)
	old := hdfcPayHTTP
	hdfcPayHTTP = bank.Server.Client()
	t.Cleanup(func() { hdfcPayHTTP = old; hdfcPayClients.Delete(T) })
	priv := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	conn := map[string]string{"send": "On", "mode": "UAT", "url": bank.Base(), "client_code": "TESTCLI", "group_id": "TESTGRP", "maker_id": "APIMAKER", "scope": bank.Scope}
	secrets := map[string]string{"api_key": bank.APIKey, "secret": bank.Secret, "bank_certificate": bank.CertPEM, "private_key": priv}
	if _, err := o.SaveConnection(ctx, T, "admin", connHDFCPay, conn, secrets); err != nil {
		t.Fatal(err)
	}
	a, b, c := payrollPeople(t, r)
	if _, err := o.MakePayrun(ctx, T, "paul.pay", "2026-04"); err != nil {
		t.Fatal(err)
	}
	o.SubmitPayrun(ctx, T, "paul.pay", "2026-04")
	if _, err := o.SendSalaryHDFC(ctx, T, "hema.hr", "2026-04"); !errors.Is(err, ErrState) {
		t.Fatalf("sent before approval: %v", err)
	}
	run, err := o.ApprovePayrun(ctx, T, "hema.hr", "2026-04")
	if err != nil {
		t.Fatal(err)
	}
	// Without the firm's own CBX client code, salary is not sent by API.
	if _, err := o.SendSalaryHDFC(ctx, T, "hema.hr", "2026-04"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("sent without the salary client code: %v", err)
	}
	conn["salary_client_code"] = "TESTSAL"
	if _, err := o.SaveConnection(ctx, T, "admin", connHDFCPay, conn, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SendSalaryHDFC(ctx, T, "paul.pay", "2026-04"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the payroll's maker sent it: %v", err)
	}
	s, err := o.SendSalaryHDFC(ctx, T, "hema.hr", "2026-04")
	if err != nil || s.State != HSAccepted || len(s.Lines) != 3 {
		t.Fatalf("send %+v %v", s, err)
	}
	bs := bank.Batches()
	if len(bs) != 1 || bs[0].Client != "TESTSAL" || len(bs[0].Lines) != 3 {
		t.Fatalf("bank got %+v", bs)
	}
	types := map[string]string{}
	for _, l := range s.Lines {
		types[l.Employee] = l.Type
	}
	if types[b.Code] != "I" || types[a.Code] != "N" || types[c.Code] != "N" {
		t.Fatalf("payment types %v", types)
	}
	// Not a client payout: no payout batch, no client ledger touched.
	if pbs, _ := docs.List[PayoutBatch](ctx, o.Docs, T, cPOBatch, ""); len(pbs) != 0 {
		t.Fatal("salary went through client payouts")
	}
	if _, err := o.SendSalaryHDFC(ctx, T, "hema.hr", "2026-04"); !errors.Is(err, ErrState) {
		t.Fatal("sent twice")
	}
	// The firm's CBX approvers release two; the bank rejects one.
	for _, l := range s.Lines {
		switch l.Employee {
		case c.Code:
			bank.Set(l.Ref, "R", "", "Account closed", "")
		default:
			bank.Set(l.Ref, "C", "UTRSAL"+l.Employee, "", "TXSETT")
		}
	}
	s, err = o.CheckSalaryHDFC(ctx, T, "paul.pay", "2026-04")
	if err != nil || s.State != HSDone {
		t.Fatalf("inquiry %+v %v", s, err)
	}
	la, _ := run.line(a.Code)
	lb, _ := run.line(b.Code)
	lc, _ := run.line(c.Code)
	bal, _ := o.App.Ledger.Balances(ctx, T, "")
	if bal[ledger.BankOwn] != -(la.Net+lb.Net) || bal[AcctSalaryPayable] != -lc.Net {
		t.Fatalf("bank %s payable %s", bal[ledger.BankOwn], bal[AcctSalaryPayable])
	}
	if len(workOpen(o, ctx, "salary-failed:2026-04:"+c.Code)) != 1 {
		t.Fatal("the failed salary was not raised")
	}
	if _, err := o.MarkPayrunPaid(ctx, T, "paul.pay", "2026-04", "2026-05-02", "X"); !errors.Is(err, ErrState) {
		t.Fatalf("marked paid by file after CBX: %v", err)
	}
}
