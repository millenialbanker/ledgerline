package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/hdfcapi"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ledger"
	"ledgerline/internal/money"
	"ledgerline/internal/pdf"
)

// ---- the monthly payroll run: maker-checker, books, payment, outputs ----
//
// Payroll makes the month's run (a draft, made again as often as needed),
// submits it, and a second person (Payroll or the HR manager, never the
// maker) approves it. Approval posts the month to the firm's books: salary
// expense and the employer's PF and ESI against salary payable and the
// statutory dues (PF, ESI, professional tax, TDS), with recoveries of
// advances. Salary is the firm's own payment, never a client payout: it is
// paid from the firm's own account by the bank file, or, where the HDFC
// CBX connection has the salary client code, as a CBX batch that waits
// for the firm's own CBX verifier and authorisers. Paid lines move salary
// payable to the bank.

const (
	cPayRun     = "pay_run"
	cSalarySend = "pay_cbx"

	AcctSalaries      = "EXPENSE:SALARIES"
	AcctReimburse     = "EXPENSE:STAFF_REIMBURSEMENTS"
	AcctEmployerPF    = "EXPENSE:EMPLOYER_PF"
	AcctEmployerESI   = "EXPENSE:EMPLOYER_ESI"
	AcctSalaryPayable = "PAYABLE:SALARY"
	AcctPFPayable     = "PAYABLE:PF"
	AcctESIPayable    = "PAYABLE:ESI"
	AcctPTPayable     = "PAYABLE:PROFESSIONAL_TAX"
	AcctTDSSalary     = "PAYABLE:TDS_SALARY"
	AcctStaffAdvances = "RECEIVABLE:STAFF_ADVANCES"
	// AcctStaffRecovered takes other deductions from pay (notice pay
	// recovered, fines the firm's rules allow): income of the firm.
	AcctStaffRecovered = "INCOME:STAFF_RECOVERIES"
)

func init() {
	for k, v := range map[string]string{AcctSalaries: "Salaries and wages", AcctReimburse: "Staff reimbursements", AcctEmployerPF: "Employer's PF, EDLI and admin charges", AcctEmployerESI: "Employer's ESI",
		AcctSalaryPayable: "Salary payable", AcctPFPayable: "PF payable (EPFO)", AcctESIPayable: "ESI payable (ESIC)", AcctPTPayable: "Professional tax payable", AcctTDSSalary: "TDS on salary payable (s.392, earlier 192)", AcctStaffAdvances: "Salary advances and staff loans", AcctStaffRecovered: "Recoveries from staff pay (notice pay)"} {
		systemNames[k] = v
	}
}

func detectDoc(b []byte) string {
	ct := http.DetectContentType(b)
	if _, ok := docTypes[ct]; ok {
		return ct
	}
	return ""
}

func (o *Ops) putRaw(ctx context.Context, tenant, source, name string, data []byte) (ingest.RawFile, error) {
	sum := sha256.Sum256(data)
	return o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: source, Name: name, SHA256: hex.EncodeToString(sum[:]), Content: data})
}

func ledgerLine(acct string, dr, cr money.Amount) ledger.Posting {
	return ledger.Posting{Account: acct, Debit: dr, Credit: cr}
}

func ledgerEntry(tenant, key, date, narr, kind, id, actor string, ps ...ledger.Posting) ledger.Entry {
	var out []ledger.Posting
	for _, p := range ps {
		if p.Debit > 0 || p.Credit > 0 {
			out = append(out, p)
		}
	}
	return ledger.Entry{Tenant: tenant, Key: key, ValueDate: date, Narration: narr, Source: ledger.SourceRef{Kind: kind, ID: id}, Postings: out, CreatedBy: actor}
}

// PayRun is a month's payroll.
type PayRun struct {
	Month      string       `json:"month"`
	Status     string       `json:"status"` // Draft, Submitted, Approved, Paid
	Lines      []PayLine    `json:"lines"`
	Totals     PayLine      `json:"totals"`
	Problems   []string     `json:"problems"`
	MadeBy     string       `json:"made_by"`
	MadeAt     string       `json:"made_at"`
	CheckedBy  string       `json:"checked_by,omitempty"`
	CheckedAt  string       `json:"checked_at,omitempty"`
	Returned   string       `json:"returned,omitempty"` // the checker's reason for sending it back
	Entry      string       `json:"entry,omitempty"`    // the books entry
	PayDate    string       `json:"pay_date"`
	Paid       money.Amount `json:"paid"`
	PayEntries []string     `json:"pay_entries,omitempty"`
	Statutory  string       `json:"statutory"` // the table's date
}

func (r PayRun) line(code string) (PayLine, bool) {
	for _, l := range r.Lines {
		if l.Employee == code {
			return l, true
		}
	}
	return PayLine{}, false
}

func payMonthEnd(month string) string { d := monthDays(month); return d[len(d)-1] }

// MakePayrun works the month out again (a draft or a run sent back).
// Payroll.
func (o *Ops) MakePayrun(ctx context.Context, tenant, actor, month string) (PayRun, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return PayRun{}, err
	}
	if err := o.require(ctx, tenant, actor, "Making the payroll", RolePayroll); err != nil {
		return PayRun{}, err
	}
	mt, err := time.Parse("2006-01", month)
	if err != nil {
		return PayRun{}, invalid("month is YYYY-MM")
	}
	if old, _, err := docs.Get[PayRun](ctx, o.Docs, tenant, cPayRun, month); err == nil && old.Status != "Draft" {
		return old, fmt.Errorf("%w: the %s payroll is %s", ErrState, month, strings.ToLower(old.Status))
	}
	pc := payCtx{month: month, first: month + "-01", last: payMonthEnd(month), mdays: len(monthDays(month)), ps: o.paySettings(ctx, tenant), pol: o.hrPolicy(ctx, tenant)}
	pc.st = o.statutoryOn(ctx, tenant, pc.last)
	pc.set, _ = o.Settings(ctx, tenant)
	pc.leaves, _ = docs.List[LeaveRequest](ctx, o.Docs, tenant, cLeaveReq, "")
	pay := mt.AddDate(0, 1, pc.ps.PayDay-1).Format("2006-01-02")
	r := PayRun{Month: month, Status: "Draft", Lines: []PayLine{}, Problems: []string{}, MadeBy: actor, MadeAt: o.now().Format(time.RFC3339), PayDate: pay, Statutory: firstOf(pc.st.From, "default")}
	es, _ := o.employees(ctx, tenant)
	for _, e := range es {
		l, ok := o.payLine(ctx, tenant, pc, e)
		if !ok {
			continue
		}
		for _, p := range l.Problems {
			r.Problems = append(r.Problems, e.Code+" "+e.Name+": "+p)
		}
		r.Lines = append(r.Lines, l)
	}
	r.Totals = totalOf(r.Lines)
	if err := docs.Upsert(ctx, o.Docs, tenant, cPayRun, month, r, actor); err != nil {
		return r, err
	}
	o.log(ctx, tenant, actor, "payroll.run.made", month, map[string]any{"people": len(r.Lines), "net": r.Totals.Net.String(), "problems": len(r.Problems)})
	return r, nil
}

func totalOf(ls []PayLine) PayLine {
	t := PayLine{Employee: "TOTAL", Name: fmt.Sprintf("%d people", len(ls))}
	for _, l := range ls {
		t.Gross += l.Gross
		t.Reimburse += l.Reimburse
		t.Taxable += l.Taxable
		t.PFWages += l.PFWages
		t.EmpPF += l.EmpPF
		t.ErEPF += l.ErEPF
		t.ErEPS += l.ErEPS
		t.EDLI += l.EDLI
		t.PFAdmin += l.PFAdmin
		t.ESIWages += l.ESIWages
		t.EmpESI += l.EmpESI
		t.ErESI += l.ErESI
		t.PT += l.PT
		t.TDS += l.TDS
		t.Recovery += l.Recovery
		t.Other += l.Other
		t.Net += l.Net
	}
	return t
}

// SubmitPayrun sends the draft for checking. Payroll.
func (o *Ops) SubmitPayrun(ctx context.Context, tenant, actor, month string) (PayRun, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return PayRun{}, err
	}
	if err := o.require(ctx, tenant, actor, "Making the payroll", RolePayroll); err != nil {
		return PayRun{}, err
	}
	r, err := docs.Update(ctx, o.Docs, tenant, cPayRun, month, actor, func(r *PayRun) error {
		if r.Status != "Draft" {
			return fmt.Errorf("%w: the payroll is %s", ErrState, strings.ToLower(r.Status))
		}
		for _, l := range r.Lines {
			if l.Net < 0 {
				return invalid("%s: deductions are more than the pay", l.Employee)
			}
		}
		r.Status, r.Returned = "Submitted", ""
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "payroll.run.submitted", month, nil)
		o.raiseWork(ctx, tenant, wHR, "payrun:"+month, "Check and approve the "+month+" payroll", fmt.Sprintf("%d people, net ₹%s. Made by %s.", len(r.Lines), r.Totals.Net, r.MadeBy), "", RolePayroll, "payroll", o.today())
	}
	return r, err
}

// ReturnPayrun sends a submitted run back to the maker with a reason.
func (o *Ops) ReturnPayrun(ctx context.Context, tenant, actor, month, reason string) (PayRun, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return PayRun{}, err
	}
	if err := o.require(ctx, tenant, actor, "Approving the payroll", RolePayroll, RoleHR); err != nil {
		return PayRun{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return PayRun{}, invalid("say what needs changing")
	}
	r, err := docs.Update(ctx, o.Docs, tenant, cPayRun, month, actor, func(r *PayRun) error {
		if r.Status != "Submitted" {
			return fmt.Errorf("%w: the payroll is %s", ErrState, strings.ToLower(r.Status))
		}
		r.Status, r.Returned = "Draft", trim(reason, 300)
		return nil
	})
	if err == nil {
		o.closeWork(ctx, tenant, actor, wHR, "payrun:"+month, "sent back: "+reason)
		o.log(ctx, tenant, actor, "payroll.run.returned", month, map[string]any{"reason": reason})
	}
	return r, err
}

// ApprovePayrun is the checker's approval: a different person from the
// maker. It posts the month to the books, marks claims paid, recoveries
// made and leave encashment paid.
func (o *Ops) ApprovePayrun(ctx context.Context, tenant, actor, month string) (PayRun, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return PayRun{}, err
	}
	if err := o.require(ctx, tenant, actor, "Approving the payroll", RolePayroll, RoleHR); err != nil {
		return PayRun{}, err
	}
	r, _, err := docs.Get[PayRun](ctx, o.Docs, tenant, cPayRun, month)
	if err != nil {
		return r, err
	}
	if r.Status != "Submitted" {
		return r, fmt.Errorf("%w: the payroll is %s", ErrState, strings.ToLower(r.Status))
	}
	if r.MadeBy == actor {
		return r, fmt.Errorf("%w: the maker of the payroll cannot approve it", ErrForbidden)
	}
	if me, ok := o.employeeOf(ctx, tenant, actor); ok {
		if _, mine := r.line(me.Code); mine && len(r.Lines) == 1 {
			return r, fmt.Errorf("%w: you cannot approve a payroll of only your own pay", ErrForbidden)
		}
	}
	e, err := o.postPayrun(ctx, tenant, actor, r)
	if err != nil {
		return r, err
	}
	r.Status, r.CheckedBy, r.CheckedAt, r.Entry = "Approved", actor, o.now().Format(time.RFC3339), e.ID
	if err := docs.Upsert(ctx, o.Docs, tenant, cPayRun, month, r, actor); err != nil {
		return r, err
	}
	for _, l := range r.Lines {
		for _, id := range l.Claims {
			_, _ = docs.Update(ctx, o.Docs, tenant, cClaim, id, actor, func(c *Claim) error { c.Status, c.PaidIn = "Paid", month; return nil })
		}
		for _, id := range l.Encash {
			_, _ = docs.Update(ctx, o.Docs, tenant, cLeaveLedger, id, actor, func(x *LeaveEntry) error { x.PaidIn = month; return nil })
		}
		for _, s := range l.Loans {
			id, amt, _ := strings.Cut(s, ":")
			var p int64
			fmt.Sscan(amt, &p)
			_, _ = docs.Update(ctx, o.Docs, tenant, cLoan, id, actor, func(x *Loan) error {
				x.Recovered += money.Amount(p)
				x.Recoveries = append(x.Recoveries, month+":"+money.Amount(p).String())
				if x.left() <= 0 {
					x.Status = "Closed"
				}
				return nil
			})
		}
	}
	o.closeWork(ctx, tenant, actor, wHR, "payrun:"+month, "approved")
	o.log(ctx, tenant, actor, "payroll.run.approved", month, map[string]any{"maker": r.MadeBy, "entry": e.ID, "net": r.Totals.Net.String()})
	return r, nil
}

// payrunPostings are the month's journal lines.
func payrunPostings(r PayRun) []ledger.Posting {
	t := r.Totals
	salaries := t.Gross - t.Reimburse
	erPF := t.ErEPF + t.ErEPS + t.EDLI + t.PFAdmin
	return []ledger.Posting{
		ledgerLine(AcctSalaries, salaries, 0),
		ledgerLine(AcctReimburse, t.Reimburse, 0),
		ledgerLine(AcctEmployerPF, erPF, 0),
		ledgerLine(AcctEmployerESI, t.ErESI, 0),
		ledgerLine(AcctPFPayable, 0, t.EmpPF+erPF),
		ledgerLine(AcctESIPayable, 0, t.EmpESI+t.ErESI),
		ledgerLine(AcctPTPayable, 0, t.PT),
		ledgerLine(AcctTDSSalary, 0, t.TDS),
		ledgerLine(AcctStaffAdvances, 0, t.Recovery),
		ledgerLine(AcctStaffRecovered, 0, t.Other),
		ledgerLine(AcctSalaryPayable, 0, t.Net),
	}
}

func (o *Ops) postPayrun(ctx context.Context, tenant, actor string, r PayRun) (ledger.Entry, error) {
	e, _, err := o.App.Ledger.Post(ctx, ledgerEntry(tenant, "PAYROLL:"+r.Month, payMonthEnd(r.Month), "Salaries for "+r.Month, "PAYROLL", r.Month, actor, payrunPostings(r)...))
	return e, err
}

// Payrun returns a month's run (Payroll and HR).
func (o *Ops) Payrun(ctx context.Context, tenant, actor, month string) (PayRun, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return PayRun{}, err
	}
	if err := o.require(ctx, tenant, actor, "Seeing payroll", RolePayroll, RoleHR); err != nil {
		return PayRun{}, err
	}
	r, _, err := docs.Get[PayRun](ctx, o.Docs, tenant, cPayRun, month)
	if errors.Is(err, docs.ErrNotFound) {
		return PayRun{Month: month, Status: "Not made", Lines: []PayLine{}, Problems: []string{}}, nil
	}
	return r, err
}

// PayrollOverview is the Payroll screen.
type PayrollOverview struct {
	Runs       []PayRun                   `json:"runs"` // without lines
	Settings   PaySettings                `json:"settings"`
	Loans      []Loan                     `json:"loans"`
	Claims     []Claim                    `json:"claims"`
	Decls      []TaxDecl                  `json:"declarations"`
	CBX        bool                       `json:"cbx"` // salary batches by CBX are set up
	Staff      []EmployeeView             `json:"staff"`
	Structures map[string]SalaryStructure `json:"structures"`
}

// Payroll is the overview for Payroll and HR.
func (o *Ops) Payroll(ctx context.Context, tenant, actor string) (PayrollOverview, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return PayrollOverview{}, err
	}
	if err := o.require(ctx, tenant, actor, "Seeing payroll", RolePayroll, RoleHR); err != nil {
		return PayrollOverview{}, err
	}
	v := PayrollOverview{Runs: []PayRun{}, Settings: o.paySettings(ctx, tenant), Loans: []Loan{}, Claims: []Claim{}, Decls: []TaxDecl{}, Staff: []EmployeeView{}, Structures: map[string]SalaryStructure{}}
	rs, _ := docs.List[PayRun](ctx, o.Docs, tenant, cPayRun, "")
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		r.Lines = nil
		v.Runs = append(v.Runs, r)
	}
	v.Loans, _ = docs.List[Loan](ctx, o.Docs, tenant, cLoan, "")
	v.Claims, _ = docs.List[Claim](ctx, o.Docs, tenant, cClaim, "")
	v.Decls, _ = docs.List[TaxDecl](ctx, o.Docs, tenant, cTaxDecl, "")
	if x, err := o.hdfcPay(ctx, tenant); err == nil && x.on && o.salaryClientCode(ctx, tenant) != "" {
		v.CBX = true
	}
	es, _ := o.employees(ctx, tenant)
	pol := o.hrPolicy(ctx, tenant)
	for _, e := range es {
		if e.Status == EmpLeft {
			continue
		}
		v.Staff = append(v.Staff, o.viewOf(ctx, tenant, e, false, es, pol))
		if s, ok := o.structureOn(ctx, tenant, e.Code, "9999-12-31"); ok {
			v.Structures[e.Code] = s
		}
	}
	return v, nil
}

// MarkPayrunPaid records the salary paid by the bank file (or otherwise)
// on a date: salary payable moves to the bank. Payroll or HR, not the
// maker.
func (o *Ops) MarkPayrunPaid(ctx context.Context, tenant, actor, month, date, ref string) (PayRun, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return PayRun{}, err
	}
	if err := o.require(ctx, tenant, actor, "Recording salary paid", RolePayroll, RoleHR); err != nil {
		return PayRun{}, err
	}
	if !validDay(date) || strings.TrimSpace(ref) == "" {
		return PayRun{}, invalid("give the date paid and the bank's reference")
	}
	r, _, err := docs.Get[PayRun](ctx, o.Docs, tenant, cPayRun, month)
	if err != nil {
		return r, err
	}
	if r.Status != "Approved" {
		return r, fmt.Errorf("%w: the payroll is %s", ErrState, strings.ToLower(r.Status))
	}
	if s, ok := o.salarySendOf(ctx, tenant, month); ok && s.State != HSRefused && s.State != HSNotReceived {
		return r, fmt.Errorf("%w: the salary went to HDFC by CBX; its result comes from the bank", ErrState)
	}
	var amt money.Amount
	for _, l := range r.Lines {
		if !l.Hold && l.Net > 0 {
			amt += l.Net
		}
	}
	return o.paySalary(ctx, tenant, actor, r, amt, date, "bank file "+ref)
}

func (o *Ops) paySalary(ctx context.Context, tenant, actor string, r PayRun, amt money.Amount, date, how string) (PayRun, error) {
	ps := o.paySettings(ctx, tenant)
	n := len(r.PayEntries) + 1
	e, _, err := o.App.Ledger.Post(ctx, ledgerEntry(tenant, fmt.Sprintf("SALPAY:%s:%d", r.Month, n), date, "Salaries for "+r.Month+" paid ("+how+")", "PAYROLL-PAY", r.Month, actor,
		ledgerLine(AcctSalaryPayable, amt, 0), ledgerLine(ps.BankAccount, 0, amt)))
	if err != nil {
		return r, err
	}
	return docs.Update(ctx, o.Docs, tenant, cPayRun, r.Month, actor, func(x *PayRun) error {
		x.Paid += amt
		x.PayEntries = append(x.PayEntries, e.ID)
		var due money.Amount
		for _, l := range x.Lines {
			if !l.Hold && l.Net > 0 {
				due += l.Net
			}
		}
		if x.Paid >= due {
			x.Status = "Paid"
		}
		o.log(ctx, tenant, actor, "payroll.run.paid", r.Month, map[string]any{"amount": amt.String(), "how": how, "entry": e.ID})
		return nil
	})
}

// ---- payslips ----

// Payslip is one person's month for the self-service list.
type Payslip struct {
	Month  string       `json:"month"`
	Gross  money.Amount `json:"gross"`
	Net    money.Amount `json:"net"`
	Status string       `json:"status"`
}

// canSeePay: oneself, or Payroll and HR.
func (o *Ops) canSeePay(ctx context.Context, tenant, actor, code string) error {
	if me, ok := o.employeeOf(ctx, tenant, actor); ok && me.Code == strings.ToUpper(code) {
		return nil
	}
	return o.require(ctx, tenant, actor, "Seeing other people's pay", RolePayroll, RoleHR)
}

// MyPayslips lists the signed-in person's payslips (approved months).
func (o *Ops) MyPayslips(ctx context.Context, tenant, actor string) ([]Payslip, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return nil, err
	}
	me, ok := o.employeeOf(ctx, tenant, actor)
	if !ok {
		return nil, fmt.Errorf("%w: your login is not linked to an employee", ErrForbidden)
	}
	rs, _ := docs.List[PayRun](ctx, o.Docs, tenant, cPayRun, "")
	out := []Payslip{}
	for i := len(rs) - 1; i >= 0; i-- {
		r := rs[i]
		if r.Status != "Approved" && r.Status != "Paid" {
			continue
		}
		if l, ok := r.line(me.Code); ok {
			out = append(out, Payslip{Month: r.Month, Gross: l.Gross, Net: l.Net, Status: r.Status})
		}
	}
	return out, nil
}

// PayslipPDF is a person's payslip for an approved month.
func (o *Ops) PayslipPDF(ctx context.Context, tenant, actor, month, code string) ([]byte, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return nil, err
	}
	if err := o.canSeePay(ctx, tenant, actor, code); err != nil {
		return nil, err
	}
	r, _, err := docs.Get[PayRun](ctx, o.Docs, tenant, cPayRun, month)
	if err != nil || (r.Status != "Approved" && r.Status != "Paid") {
		return nil, ErrNotFound
	}
	l, ok := r.line(strings.ToUpper(code))
	if !ok {
		return nil, ErrNotFound
	}
	o.log(ctx, tenant, actor, "payroll.payslip.viewed", l.Employee, map[string]any{"month": month})
	return o.payslipBytes(ctx, tenant, r, l)
}

func (o *Ops) payslipBytes(ctx context.Context, tenant string, r PayRun, l PayLine) ([]byte, error) {
	set, _ := o.Settings(ctx, tenant)
	ps := o.paySettings(ctx, tenant)
	mt, _ := time.Parse("2006-01", r.Month)
	var earn, ded [][]string
	for _, x := range l.Earnings {
		earn = append(earn, []string{x.Name, inrS(x.Amount)})
	}
	for _, x := range l.Deductions {
		ded = append(ded, []string{x.Name, inrS(x.Amount)})
	}
	rep := pdf.Report{
		Firm:  []string{set.Firm, firstOf(set.SEBIReg, "")},
		Title: "Payslip for " + mt.Format("January 2006"),
		Meta:  []string{"Pay date " + fmtDay(r.PayDate)},
		Party: []string{l.Name + " (" + l.Employee + ")", firstOf(l.Department, ""), "PAN " + MaskPAN(l.PAN), "UAN " + maskTail4(l.UAN), "Bank " + MaskAccount(l.Bank.Account)},
		Summary: [][2]string{{"Days in month", fmt.Sprint(l.MonthDays)}, {"Paid days", fmt.Sprintf("%.1f", l.PaidDays)}, {"Loss of pay days", fmt.Sprintf("%.1f", l.LOP)},
			{"Gross earnings", inrS(l.Gross)}, {"Deductions", inrS(l.Gross - l.Net)}, {"Net pay", inrS(l.Net)}, {"Tax regime", l.Regime}},
		Sections: []pdf.Table{
			{Title: "Earnings", Cols: []pdf.Col{{Label: "Component", Weight: 3}, {Label: "₹", Right: true, Weight: 1}}, Rows: earn, Totals: []string{"Gross", inrS(l.Gross)}},
			{Title: "Deductions", Cols: []pdf.Col{{Label: "Deduction", Weight: 3}, {Label: "₹", Right: true, Weight: 1}}, Rows: ded, Totals: []string{"Total", inrS(l.Gross - l.Net)}, Empty: "No deductions."},
			{Title: "Employer's contributions (not part of your pay)", Cols: []pdf.Col{{Label: "Item", Weight: 3}, {Label: "₹", Right: true, Weight: 1}},
				Rows: [][]string{{"PF (EPF)", inrS(l.ErEPF)}, {"Pension (EPS)", inrS(l.ErEPS)}, {"ESI", inrS(l.ErESI)}}},
			{Title: "Income tax for the year (projected)", Cols: []pdf.Col{{Label: "Item", Weight: 3}, {Label: "₹", Right: true, Weight: 1}},
				Rows: [][]string{{"Gross salary for the year", inrS(l.Tax.GrossYear)}, {"Taxable income", inrS(l.Tax.Taxable)}, {"Tax for the year", inrS(l.Tax.TaxYear)}, {"Deducted earlier this year", inrS(l.Tax.PaidBefore)}}},
		},
		Footer: "PF establishment " + firstOf(ps.PFCode, "—") + " · ESI " + firstOf(ps.ESICode, "—") + " · TAN " + firstOf(ps.TAN, "—") + ". A computer-generated payslip; no signature is needed.",
	}
	return pdf.Render(rep).Bytes(pdf.Meta{Title: "Payslip " + r.Month + " " + l.Employee, Author: set.Firm, Created: o.now()}, nil)
}

func inrS(a money.Amount) string { return formatINR(a.Dec()) }

// EmailPayslips mails each person's payslip (approved months) to their
// work email; each is kept in the outbox with what happened. Payroll.
func (o *Ops) EmailPayslips(ctx context.Context, tenant, actor, month string) (map[string]string, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return nil, err
	}
	if err := o.require(ctx, tenant, actor, "Emailing payslips", RolePayroll); err != nil {
		return nil, err
	}
	r, _, err := docs.Get[PayRun](ctx, o.Docs, tenant, cPayRun, month)
	if err != nil || (r.Status != "Approved" && r.Status != "Paid") {
		return nil, fmt.Errorf("%w: payslips go out once the payroll is approved", ErrState)
	}
	out := map[string]string{}
	for _, l := range r.Lines {
		if l.Email == "" {
			out[l.Employee] = "Not sent: no work email"
			continue
		}
		b, err := o.payslipBytes(ctx, tenant, r, l)
		if err != nil {
			out[l.Employee] = "Failed: " + err.Error()
			continue
		}
		out[l.Employee] = o.mailStaff(ctx, tenant, l.Email, "Payslip for "+month, "Your payslip for "+month+" is attached. It is also in Ledgerline under My payslips.", "payslip:"+month+":"+l.Employee,
			[]MailFile{{Name: "payslip-" + month + ".pdf", Type: "application/pdf", Data: b}})
	}
	o.log(ctx, tenant, actor, "payroll.payslips.emailed", month, map[string]any{"count": len(out)})
	return out, nil
}

// ---- files ----

func csvBytes(rows [][]string) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.WriteAll(rows)
	return b.Bytes()
}

func wholeR(a money.Amount) string { return fmt.Sprint(int64(roundR(a.Dec())) / 100) }

// approvedRun is a month's run once approved (files are made only then).
func (o *Ops) approvedRun(ctx context.Context, tenant, actor, month, what string) (PayRun, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return PayRun{}, err
	}
	if err := o.require(ctx, tenant, actor, "Making payroll files", RolePayroll, RoleHR); err != nil {
		return PayRun{}, err
	}
	r, _, err := docs.Get[PayRun](ctx, o.Docs, tenant, cPayRun, month)
	if err != nil {
		return r, err
	}
	if r.Status != "Approved" && r.Status != "Paid" && what != "register" {
		return r, fmt.Errorf("%w: the %s is made from an approved payroll", ErrState, what)
	}
	o.log(ctx, tenant, actor, "payroll.file", month, map[string]any{"file": what})
	return r, nil
}

// SalaryRegisterCSV is the month's salary register.
func (o *Ops) SalaryRegisterCSV(ctx context.Context, tenant, actor, month string) ([]byte, error) {
	r, err := o.approvedRun(ctx, tenant, actor, month, "register")
	if err != nil {
		return nil, err
	}
	codes := []string{}
	seen := map[string]bool{}
	for _, l := range r.Lines {
		for _, x := range l.Earnings {
			if !seen[x.Code] {
				seen[x.Code] = true
				codes = append(codes, x.Code)
			}
		}
	}
	head := append([]string{"Code", "Name", "Department", "Branch", "Days", "LOP", "Paid days"}, codes...)
	head = append(head, "Gross", "PF wages", "Employee PF", "Employer EPF", "Employer EPS", "EDLI", "PF admin", "ESI wages", "Employee ESI", "Employer ESI", "Professional tax", "TDS", "Recoveries", "Other deductions", "Net", "Held")
	rows := [][]string{head}
	for _, l := range append(r.Lines, r.Totals) {
		row := []string{l.Employee, l.Name, l.Department, l.Branch, fmt.Sprint(l.MonthDays), fmt.Sprintf("%.1f", l.LOP), fmt.Sprintf("%.1f", l.PaidDays)}
		for _, c := range codes {
			var a money.Amount
			for _, x := range l.Earnings {
				if x.Code == c {
					a += x.Amount
				}
			}
			row = append(row, a.String())
		}
		row = append(row, l.Gross.String(), l.PFWages.String(), l.EmpPF.String(), l.ErEPF.String(), l.ErEPS.String(), l.EDLI.String(), l.PFAdmin.String(), l.ESIWages.String(), l.EmpESI.String(), l.ErESI.String(), l.PT.String(), l.TDS.String(), l.Recovery.String(), l.Other.String(), l.Net.String(), map[bool]string{true: "Yes"}[l.Hold])
		rows = append(rows, row)
	}
	return csvBytes(rows), nil
}

// BankFileCSV is the salary payment file for the firm's bank: one line per
// person paid (held pay is left out).
func (o *Ops) BankFileCSV(ctx context.Context, tenant, actor, month string) ([]byte, error) {
	r, err := o.approvedRun(ctx, tenant, actor, month, "bank file")
	if err != nil {
		return nil, err
	}
	ps := o.paySettings(ctx, tenant)
	rows := [][]string{{"Debit account", "Beneficiary name", "Account number", "IFSC", "Amount", "Payment date", "Narration", "Email"}}
	for _, l := range r.Lines {
		if l.Hold || l.Net <= 0 {
			continue
		}
		rows = append(rows, []string{ps.DebitAccount, firstOf(l.Bank.Holder, l.Name), l.Bank.Account, l.Bank.IFSC, l.Net.String(), r.PayDate, "SALARY " + month + " " + l.Employee, l.Email})
	}
	return csvBytes(rows), nil
}

// PFECR is the EPFO's ECR 2.0 text file: one line per member, fields
// separated by #~#: UAN, name, gross wages, EPF wages, EPS wages, EDLI
// wages, EPF contribution (employee), EPS contribution, EPF-EPS difference
// (employer), NCP days, refund of advances. Amounts in whole rupees.
func (o *Ops) PFECR(ctx context.Context, tenant, actor, month string) ([]byte, error) {
	r, err := o.approvedRun(ctx, tenant, actor, month, "PF ECR")
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, l := range r.Lines {
		if l.EmpPF == 0 && l.PFWages == 0 {
			continue
		}
		if l.UAN == "" {
			return nil, invalid("%s %s has no UAN: add it before making the ECR", l.Employee, l.Name)
		}
		f := []string{l.UAN, strings.ToUpper(l.Name), wholeR(l.Gross - l.Reimburse), wholeR(l.PFWages), wholeR(l.EPSWages), wholeR(l.EDLIWages), wholeR(l.EmpPF), wholeR(l.ErEPS), wholeR(l.ErEPF), fmt.Sprintf("%.0f", l.LOP), "0"}
		b.WriteString(strings.Join(f, "#~#") + "\n")
	}
	return []byte(b.String()), nil
}

// ESIFile is the monthly contribution upload in the columns of ESIC's
// template (IP number, IP name, days paid, total wages, reason code for
// zero days, last working day); paste it into the portal's Excel template.
func (o *Ops) ESIFile(ctx context.Context, tenant, actor, month string) ([]byte, error) {
	r, err := o.approvedRun(ctx, tenant, actor, month, "ESI file")
	if err != nil {
		return nil, err
	}
	rows := [][]string{{"IP Number (10 Digits)", "IP Name (Only alphabets and space)", "No of Days for which wages paid/payable during the month", "Total Monthly Wages", "Reason Code for Zero workings days", "Last Working Day"}}
	es, _ := o.employees(ctx, tenant)
	for _, l := range r.Lines {
		if l.ESIWages == 0 && l.EmpESI == 0 && l.ErESI == 0 {
			continue
		}
		reason, last := "", ""
		if e, ok := find(es, l.Employee); ok && e.LeftOn != "" && e.LeftOn <= payMonthEnd(month) {
			reason, last = "2", slashDate(e.LeftOn) // 2: left service
		}
		if l.PaidDays == 0 && reason == "" {
			reason = "1" // 1: without pay / on leave
		}
		rows = append(rows, []string{l.ESIC, strings.ToUpper(l.Name), fmt.Sprintf("%.0f", l.PaidDays), wholeR(l.ESIWages), reason, last})
	}
	return csvBytes(rows), nil
}

// PTRow is one state's professional tax for a month.
type PTRow struct {
	State  string         `json:"state"`
	Slabs  map[string]int `json:"slabs"` // amount -> people
	People int            `json:"people"`
	Amount money.Amount   `json:"amount"`
}

// PTSummary is the figures for each state's professional tax challan.
func (o *Ops) PTSummary(ctx context.Context, tenant, actor, month string) ([]PTRow, error) {
	r, err := o.approvedRun(ctx, tenant, actor, month, "PT figures")
	if err != nil {
		return nil, err
	}
	by := map[string]*PTRow{}
	for _, l := range r.Lines {
		if l.State == "" {
			continue
		}
		x := by[l.State]
		if x == nil {
			x = &PTRow{State: l.State, Slabs: map[string]int{}}
			by[l.State] = x
		}
		x.Slabs[l.PT.String()]++
		x.People++
		x.Amount += l.PT
	}
	out := []PTRow{}
	for _, k := range sortedKeys(by) {
		out = append(out, *by[k])
	}
	return out, nil
}

// Form138CSV is the data for the quarterly TDS statement on salary (Form
// 138 under the Income-tax Rules 2026, earlier Form 24Q): Annexure I lines
// (each person and month: amount paid, TDS, date paid); for the fourth
// quarter, add=annexure2 gives each person's year (the salary details).
// Load it into the TDS return preparation utility; the FVU file itself is
// not made here.
func (o *Ops) Form138CSV(ctx context.Context, tenant, actor, fy string, quarter int, annexure2 bool) ([]byte, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return nil, err
	}
	if err := o.require(ctx, tenant, actor, "Making payroll files", RolePayroll, RoleHR); err != nil {
		return nil, err
	}
	if quarter < 1 || quarter > 4 {
		return nil, invalid("quarter is 1 to 4")
	}
	months := fyMonths(fy)
	ps := o.paySettings(ctx, tenant)
	if !annexure2 {
		rows := [][]string{{"TAN", "Quarter", "Month", "Employee code", "PAN", "Name", "Section", "Date of payment", "Amount paid", "TDS", "Surcharge and cess included", "Date deducted"}}
		for _, m := range months[(quarter-1)*3 : quarter*3] {
			r, _, err := docs.Get[PayRun](ctx, o.Docs, tenant, cPayRun, m)
			if err != nil || (r.Status != "Approved" && r.Status != "Paid") {
				continue
			}
			for _, l := range r.Lines {
				rows = append(rows, []string{ps.TAN, fmt.Sprintf("Q%d", quarter), m, l.Employee, firstOf(l.PAN, "PANNOTAVBL"), l.Name, "392 (earlier 192)", r.PayDate, (l.Gross - l.Reimburse).String(), l.TDS.String(), "yes", r.PayDate})
			}
		}
		o.log(ctx, tenant, actor, "payroll.file", fy, map[string]any{"file": "Form 138", "quarter": quarter})
		return csvBytes(rows), nil
	}
	rows := [][]string{{"Employee code", "PAN", "Name", "Regime", "Gross salary", "HRA exempt", "Standard deduction", "Professional tax", "Deductions (80C, 80D, NPS, home loan)", "Other income", "Taxable income", "Tax", "Rebate", "Surcharge", "Cess", "Tax for the year", "TDS deducted"}}
	for _, y := range o.yearFigures(ctx, tenant, fy) {
		w := y.Tax
		rows = append(rows, []string{y.Code, firstOf(y.PAN, "PANNOTAVBL"), y.Name, w.Regime, w.GrossYear.String(), w.HRAExempt.String(), w.StdDed.String(), w.PTYear.String(), (w.Ded80C + w.Ded80D + w.DedNPS + w.HomeLoan).String(), w.OtherIncome.String(), w.Taxable.String(), w.SlabTax.String(), w.Rebate.String(), w.Surcharge.String(), w.Cess.String(), w.TaxYear.String(), y.TDS.String()})
	}
	o.log(ctx, tenant, actor, "payroll.file", fy, map[string]any{"file": "Form 138 annexure II"})
	return csvBytes(rows), nil
}

// yearFig is a person's year from the approved runs: the last month's tax
// working (which covers the whole year) and the TDS deducted.
type yearFig struct {
	Code, Name, PAN string
	Tax             TaxWorking
	TDS             money.Amount
	Gross           money.Amount
}

func (o *Ops) yearFigures(ctx context.Context, tenant, fy string) []yearFig {
	by := map[string]*yearFig{}
	for _, m := range fyMonths(fy) {
		r, _, err := docs.Get[PayRun](ctx, o.Docs, tenant, cPayRun, m)
		if err != nil || (r.Status != "Approved" && r.Status != "Paid") {
			continue
		}
		for _, l := range r.Lines {
			y := by[l.Employee]
			if y == nil {
				y = &yearFig{Code: l.Employee, Name: l.Name, PAN: l.PAN}
				by[l.Employee] = y
			}
			y.Tax, y.TDS, y.Gross = l.Tax, y.TDS+l.TDS, y.Gross+l.Taxable
		}
	}
	out := []yearFig{}
	for _, k := range sortedKeys(by) {
		out = append(out, *by[k])
	}
	return out
}

// Form130PartB is Part B of the salary TDS certificate (Form 130 under the
// Income-tax Rules 2026, earlier Form 16 Part B): the year's salary, its
// exemptions and deductions and the tax, from the approved runs. Part A
// (the TDS deposited) comes from TRACES. The person themselves, Payroll or HR.
func (o *Ops) Form130PartB(ctx context.Context, tenant, actor, code, fy string) ([]byte, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return nil, err
	}
	if err := o.canSeePay(ctx, tenant, actor, code); err != nil {
		return nil, err
	}
	code = strings.ToUpper(code)
	var y *yearFig
	for _, x := range o.yearFigures(ctx, tenant, fy) {
		if x.Code == code {
			x := x
			y = &x
		}
	}
	if y == nil {
		return nil, ErrNotFound
	}
	set, _ := o.Settings(ctx, tenant)
	ps := o.paySettings(ctx, tenant)
	w := y.Tax
	row := func(k string, a money.Amount) []string { return []string{k, inrS(a)} }
	rep := pdf.Report{
		Firm:  []string{set.Firm, "TAN " + firstOf(ps.TAN, "—"), "PAN " + firstOf(set.FirmPAN, "—")},
		Title: "Form 130, Part B: details of salary paid and tax deducted (tax year " + fy + ")",
		Meta:  []string{"Earlier Form 16 Part B", "Section 392 (earlier 192)"},
		Party: []string{y.Name + " (" + y.Code + ")", "PAN " + firstOf(y.PAN, "not given")},
		Sections: []pdf.Table{{Title: "Computation", Cols: []pdf.Col{{Label: "Item", Weight: 3}, {Label: "₹", Right: true, Weight: 1}}, Rows: [][]string{
			row("Gross salary", w.GrossYear), row("Less: HRA exempt", w.HRAExempt), row("Less: standard deduction", w.StdDed), row("Less: professional tax", map[bool]money.Amount{true: w.PTYear}[w.Regime == "old"]),
			row("Add: other income declared", w.OtherIncome), row("Less: Schedule XV (80C) incl. PF", w.Ded80C), row("Less: health insurance (80D)", w.Ded80D), row("Less: additional NPS", w.DedNPS), row("Less: home loan interest", w.HomeLoan),
			row("Taxable income", w.Taxable), row("Tax on it", w.SlabTax), row("Less: rebate", w.Rebate), row("Add: surcharge", w.Surcharge), row("Add: health and education cess", w.Cess), row("Tax for the year", w.TaxYear), row("TDS deducted", y.TDS)}}},
		Notes:  []string{"Tax regime: " + w.Regime + ".", "Part A of Form 130 (TDS deposited, with challan details) is downloaded from TRACES."},
		Footer: "From the approved payroll in Ledgerline.",
	}
	o.log(ctx, tenant, actor, "payroll.form130.viewed", code, map[string]any{"fy": fy})
	return pdf.Render(rep).Bytes(pdf.Meta{Title: "Form 130 Part B " + fy + " " + code, Author: set.Firm, Created: o.now()}, nil)
}

// ---- full and final settlement ----

// FnF is the settlement for a leaver: amounts that go into the leaving
// month's payroll as earnings and deductions.
type FnF struct {
	Employee       string       `json:"employee"`
	LastDay        string       `json:"last_day"`
	Month          string       `json:"month"`
	ServiceYears   int          `json:"service_years"`
	LeaveDays      float64      `json:"leave_days"`
	LeaveEncash    money.Amount `json:"leave_encash"`
	Gratuity       money.Amount `json:"gratuity"`
	NoticeRecovery money.Amount `json:"notice_recovery"`
	LoansLeft      money.Amount `json:"loans_left"`
	Basis          []string     `json:"basis"`
}

// FullAndFinal works out a leaver's settlement and puts it into the
// leaving month's payroll inputs (where TDS, PF and the books follow).
// Gratuity: 15 days' wages (basic and DA ÷ 26) per completed year, a part
// year over six months counting as a year, after five years' service
// (Payment of Gratuity Act 1972 s.4, now Code on Social Security 2020
// s.53; one year for fixed-term employees), up to the statutory ceiling.
// Payroll.
func (o *Ops) FullAndFinal(ctx context.Context, tenant, actor, code string, noticeRecovery money.Amount) (FnF, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return FnF{}, err
	}
	if err := o.require(ctx, tenant, actor, "Making full and final settlements", RolePayroll); err != nil {
		return FnF{}, err
	}
	e, err := o.employee(ctx, tenant, code)
	if err != nil {
		return FnF{}, err
	}
	if e.LeftOn == "" {
		return FnF{}, invalid("record the leaving (with the last day) first")
	}
	f := FnF{Employee: e.Code, LastDay: e.LeftOn, Month: e.LeftOn[:7], NoticeRecovery: noticeRecovery, Basis: []string{}}
	s, ok := o.structureOn(ctx, tenant, e.Code, e.LeftOn)
	if !ok {
		return f, invalid("%s has no salary structure", e.Code)
	}
	st := o.statutoryOn(ctx, tenant, e.LeftOn)
	ps := o.paySettings(ctx, tenant)
	wage := s.comp("BASIC") + s.comp("DA")
	j, _ := time.Parse("2006-01-02", e.JoinedOn)
	l, _ := time.Parse("2006-01-02", e.LeftOn)
	months := (l.Year()-j.Year())*12 + int(l.Month()) - int(j.Month())
	if l.Day() < j.Day() {
		months--
	}
	f.ServiceYears = months / 12
	if months%12 > 6 {
		f.ServiceYears++
	}
	need := 5
	if e.EmploymentType == "Contract" {
		need = 1
	}
	if months >= need*12 {
		d, _ := wage.Dec().MulInt(15).MulInt(int64(f.ServiceYears)).Quo(money.DecInt(26))
		f.Gratuity = min(roundR(d), rupeesP(st.GratuityMax))
		f.Basis = append(f.Basis, fmt.Sprintf("Gratuity: ₹%s × 15 ÷ 26 × %d years", inrS(wage), f.ServiceYears))
	} else {
		f.Basis = append(f.Basis, fmt.Sprintf("No gratuity: %d months of service, %d years needed", months, need))
	}
	for _, b := range o.balances(ctx, tenant, e, e.LeftOn) {
		lt, _ := leaveType(o.hrPolicy(ctx, tenant), b.Type)
		if lt.Paid && days(lt.EncashMax) > 0 && b.Balance > 0 {
			f.LeaveDays += b.Balance
		}
	}
	if f.LeaveDays > 0 {
		d, _ := wage.Dec().Mul(dec(fmt.Sprintf("%.1f", f.LeaveDays))).Quo(money.DecInt(int64(ps.EncashDivisor)))
		f.LeaveEncash = roundR(d)
		f.Basis = append(f.Basis, fmt.Sprintf("Leave encashment: %.1f days × ₹%s ÷ %d", f.LeaveDays, inrS(wage), ps.EncashDivisor))
	}
	lns, _ := docs.List[Loan](ctx, o.Docs, tenant, cLoan, "")
	for _, x := range lns {
		if x.Employee == e.Code && x.Status == "Active" {
			f.LoansLeft += x.left()
		}
	}
	if f.LoansLeft > 0 {
		f.Basis = append(f.Basis, "Advances and loans left are recovered in full from the last salary")
	}
	in, _, _ := docs.Get[PayInput](ctx, o.Docs, tenant, cPayInput, f.Month+"|"+e.Code)
	in.Month, in.Employee = f.Month, e.Code
	var earn, ded []PayItem
	for _, x := range in.Earnings {
		if x.Code != "GRATUITY" && x.Code != "FNF-LEAVE" {
			earn = append(earn, x)
		}
	}
	for _, x := range in.Deductions {
		if x.Code != "NOTICE" {
			ded = append(ded, x)
		}
	}
	if f.Gratuity > 0 {
		// Exempt up to the ceiling for employees covered by the Act
		// (s.10(10) of the 1961 Act, carried into the 2025 Act).
		earn = append(earn, PayItem{Code: "GRATUITY", Name: "Gratuity", Amount: f.Gratuity, Exempt: true})
	}
	if f.LeaveEncash > 0 {
		earn = append(earn, PayItem{Code: "FNF-LEAVE", Name: fmt.Sprintf("Leave encashment on leaving (%.1f days)", f.LeaveDays), Amount: f.LeaveEncash})
	}
	if noticeRecovery > 0 {
		ded = append(ded, PayItem{Code: "NOTICE", Name: "Notice pay recovery", Amount: noticeRecovery})
	}
	in.Earnings, in.Deductions = earn, ded
	if in.Arrears == nil {
		in.Arrears = []PayItem{}
	}
	if in.Earnings == nil {
		in.Earnings = []PayItem{}
	}
	if in.Deductions == nil {
		in.Deductions = []PayItem{}
	}
	if _, err := o.SavePayInput(ctx, tenant, actor, in); err != nil {
		return f, err
	}
	o.tickItem(ctx, tenant, actor, e.Code, "leave", "fnf", fmt.Sprintf("in the %s payroll", f.Month))
	o.log(ctx, tenant, actor, "payroll.fnf.made", e.Code, map[string]any{"gratuity": f.Gratuity.String(), "leave_encash": f.LeaveEncash.String(), "notice": noticeRecovery.String()})
	return f, nil
}

// ---- salary by HDFC CBX ----

// SalarySendLine is one person's payment at the bank.
type SalarySendLine struct {
	Employee string       `json:"employee"`
	Ref      string       `json:"ref"`
	Amount   money.Amount `json:"amount"`
	Type     string       `json:"type"`
	Status   string       `json:"status"` // Waiting, Paid, Failed
	UTR      string       `json:"utr,omitempty"`
	Note     string       `json:"note,omitempty"`
	Posted   bool         `json:"posted,omitempty"` // moved from salary payable to the bank in the books
}

// SalarySend is a month's salary batch sent to HDFC by CBX.
type SalarySend struct {
	ID       string           `json:"id"` // the month
	FileRef  string           `json:"file_ref"`
	Day      string           `json:"day"`
	State    string           `json:"state"`
	BatchNum string           `json:"batch_num,omitempty"`
	Message  string           `json:"message,omitempty"`
	SentAt   string           `json:"sent_at"`
	By       string           `json:"by"`
	Total    money.Amount     `json:"total"`
	Lines    []SalarySendLine `json:"lines"`
	Checks   []HDFCCheck      `json:"checks"`
}

func (o *Ops) salarySendOf(ctx context.Context, tenant, month string) (SalarySend, bool) {
	s, _, err := docs.Get[SalarySend](ctx, o.Docs, tenant, cSalarySend, month)
	return s, err == nil
}

func (o *Ops) salaryClientCode(ctx context.Context, tenant string) string {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connHDFCPay)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(c.Values["salary_client_code"])
}

// SendSalaryHDFC sends an approved month's salary to HDFC Bank as a CBX
// batch under the firm's own (salary) CBX client code, never the client
// payout code. The API is only the maker: the payments wait in CBX for
// the firm's own verifier and authorisers. Ledgerline asks a second person
// too: the sender is not the payroll's maker. Payroll or HR.
func (o *Ops) SendSalaryHDFC(ctx context.Context, tenant, actor, month string) (SalarySend, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return SalarySend{}, err
	}
	if err := o.require(ctx, tenant, actor, "Sending salary to the bank", RolePayroll, RoleHR); err != nil {
		return SalarySend{}, err
	}
	r, _, err := docs.Get[PayRun](ctx, o.Docs, tenant, cPayRun, month)
	if err != nil {
		return SalarySend{}, err
	}
	if r.Status != "Approved" {
		return SalarySend{}, fmt.Errorf("%w: the payroll is %s; salary is sent once it is approved", ErrState, strings.ToLower(r.Status))
	}
	x, err := o.hdfcPay(ctx, tenant)
	if err != nil {
		return SalarySend{}, err
	}
	client := o.salaryClientCode(ctx, tenant)
	if !x.on || client == "" {
		return SalarySend{}, invalid("salary by CBX needs the HDFC Bank CBX connection switched on with the CBX client code for the firm's own (salary) payments")
	}
	if client == x.client {
		return SalarySend{}, invalid("the salary client code must be the firm's own account, not the client payout client code")
	}
	if r.MadeBy == actor {
		return SalarySend{}, fmt.Errorf("%w: someone other than the payroll's maker sends the salary", ErrForbidden)
	}
	if prev, ok := o.salarySendOf(ctx, tenant, month); ok && prev.State != HSRefused && prev.State != HSNotReceived {
		return prev, fmt.Errorf("%w: already sent to HDFC (%s)", ErrState, strings.ToLower(prev.State))
	}
	now := o.now()
	today := now.Format("2006-01-02")
	set, _ := o.Settings(ctx, tenant)
	rtgs := money.MustAmount("200000")
	if v, err := money.ParseAmount(set.RTGSThreshold); err == nil && v > 0 {
		rtgs = v
	}
	s := SalarySend{ID: month, Day: today, State: HSSending, SentAt: now.Format(time.RFC3339), By: actor, Lines: []SalarySendLine{}, Checks: []HDFCCheck{}}
	var pays []hdfcapi.Payment
	for i, l := range r.Lines {
		if l.Hold || l.Net <= 0 {
			continue
		}
		if l.Bank.Account == "" || l.Bank.IFSC == "" {
			return SalarySend{}, invalid("%s %s has no salary bank account", l.Employee, l.Name)
		}
		typ := hdfcapi.TypeNEFT
		switch {
		case strings.HasPrefix(l.Bank.IFSC, "HDFC"):
			typ = hdfcapi.TypeInternal
		case l.Net >= rtgs:
			typ = hdfcapi.TypeRTGS
		}
		ref := fmt.Sprintf("SAL%s%03d", strings.ReplaceAll(month, "-", ""), i+1)
		pays = append(pays, hdfcapi.Payment{Type: typ, Account: l.Bank.Account, Amount: l.Net.String(), Name: trim(strings.ToUpper(firstOf(l.Bank.Holder, l.Name)), 35), InstrRef: ref, CustRef: ref,
			Info1: "SALARY " + month, Date: slashDate(today), IFSC: l.Bank.IFSC, BankName: l.Bank.Bank, Email: l.Email})
		s.Lines = append(s.Lines, SalarySendLine{Employee: l.Employee, Ref: ref, Amount: l.Net, Type: typ, Status: "Waiting"})
		s.Total += l.Net
	}
	if len(pays) == 0 {
		return SalarySend{}, invalid("no one to pay")
	}
	if len(pays) > hdfcapi.MaxBatch {
		return SalarySend{}, invalid("the bank takes at most %d payments a batch", hdfcapi.MaxBatch)
	}
	n, err := o.next(ctx, tenant, "hdfc-api-fileref", actor)
	if err != nil {
		return SalarySend{}, err
	}
	s.FileRef = fmt.Sprintf("%06d", n%1000000)
	if err := docs.Upsert(ctx, o.Docs, tenant, cSalarySend, month, s, actor); err != nil {
		return s, err
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	rep, err := x.cl.SendBatch(cctx, "", hdfcapi.BatchRequest{ClientCode: client, GroupID: x.group, Maker: x.maker, Count: fmt.Sprint(len(pays)), FileRef: s.FileRef, Payments: pays})
	cancel()
	var ae *hdfcapi.APIError
	switch {
	case err == nil && rep.Accepted():
		s.State, s.BatchNum, s.Message = HSAccepted, string(rep.BatchNum), "with your CBX verifier and authorisers"
	case err == nil:
		s.State, s.Message = HSRefused, fmt.Sprintf("the bank did not accept the batch (%s %s)", rep.Code, rep.Text)
	case errors.As(err, &ae) && ae.Sure():
		s.State, s.Message = HSRefused, err.Error()
	default:
		s.State, s.Message = HSUnclear, err.Error()
		o.raiseWork(ctx, tenant, wHR, "salary-cbx-unclear:"+month, "Salary "+month+": HDFC's answer not received",
			"The salary batch went to HDFC but no clear answer came back ("+s.Message+"). Check CBX for file reference "+s.FileRef+" before sending again.", "", RolePayroll, "payroll", today)
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSalarySend, month, s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "payroll.cbx_sent", month, map[string]any{"state": s.State, "file_ref": s.FileRef, "batch_num": s.BatchNum, "lines": len(s.Lines), "total": s.Total.String(), "client_code": client})
	return s, nil
}

// CheckSalaryHDFC asks the bank about the month's batch now: paid lines
// are moved from salary payable to the bank; failed lines go to Payroll.
func (o *Ops) CheckSalaryHDFC(ctx context.Context, tenant, actor, month string) (SalarySend, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return SalarySend{}, err
	}
	if err := o.require(ctx, tenant, actor, "Sending salary to the bank", RolePayroll, RoleHR); err != nil {
		return SalarySend{}, err
	}
	return o.salaryInquire(ctx, tenant, actor, month)
}

func (o *Ops) salaryInquire(ctx context.Context, tenant, actor, month string) (SalarySend, error) {
	s, ok := o.salarySendOf(ctx, tenant, month)
	if !ok {
		return s, ErrNotFound
	}
	if s.State != HSAccepted {
		return s, fmt.Errorf("%w: the batch is %s", ErrState, strings.ToLower(s.State))
	}
	x, err := o.hdfcPay(ctx, tenant)
	if err != nil {
		return s, err
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	rep, err := x.cl.Inquire(cctx, hdfcapi.InquiryRequest{GroupID: x.group, Maker: x.maker, BatchNum: s.BatchNum, Date: slashDate(s.Day), FileRef: s.FileRef})
	cancel()
	ck := HDFCCheck{At: o.now().Format(time.RFC3339), Kind: "inquiry", By: actor}
	var paid money.Amount
	if err != nil {
		ck.Result = err.Error()
	} else {
		ck.Result = firstOf(string(rep.Message), string(rep.Code))
		byRef := map[string]int{}
		for i, l := range s.Lines {
			byRef[l.Ref] = i
		}
		for _, bl := range rep.Lines {
			i, ok := byRef[strings.TrimSpace(string(bl.CustRef))]
			if !ok || s.Lines[i].Status != "Waiting" {
				continue
			}
			ln := &s.Lines[i]
			st, rbi := strings.ToUpper(string(bl.Status)), strings.ToUpper(string(bl.RBIStatus))
			switch {
			case st == "R" || rbi == "TXREJE":
				ln.Status, ln.Note = "Failed", firstOf(firstOf(string(bl.Reason), string(bl.RBIReason)), "rejected")
				o.raiseWork(ctx, tenant, wHR, "salary-failed:"+month+":"+ln.Employee, fmt.Sprintf("Salary %s of %s not paid by HDFC", month, ln.Employee),
					"HDFC rejected the payment ("+ln.Note+"). Correct the bank account and pay it another way; it stays in salary payable.", "", RolePayroll, "payroll", o.today())
			case st == "C":
				utr := strings.TrimSpace(firstOf(string(bl.UTR), string(bl.BankRef)))
				if utr == "" {
					continue
				}
				ln.Status, ln.UTR = "Paid", utr
				paid += ln.Amount
			default:
				ln.Note = firstOf(string(bl.Reason), "with your CBX approvers, or at the bank")
			}
		}
	}
	s.Checks = append(s.Checks, ck)
	done := true
	for _, l := range s.Lines {
		done = done && l.Status != "Waiting"
	}
	if done {
		s.State = HSDone
	}
	if paid > 0 {
		r, _, err := docs.Get[PayRun](ctx, o.Docs, tenant, cPayRun, month)
		if err == nil {
			if _, err := o.paySalary(ctx, tenant, SystemActor, r, paid, o.today(), "HDFC CBX batch "+s.BatchNum); err != nil {
				return s, err
			}
			for i := range s.Lines {
				if s.Lines[i].Status == "Paid" {
					s.Lines[i].Posted = true
				}
			}
		}
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSalarySend, month, s, actor); err != nil {
		return s, err
	}
	o.log(ctx, tenant, actor, "payroll.cbx_inquiry", month, map[string]any{"result": ck.Result, "paid": paid.String(), "state": s.State})
	return s, nil
}

// SalarySendView is a month's CBX batch (Payroll and HR).
func (o *Ops) SalarySendView(ctx context.Context, tenant, actor, month string) (SalarySend, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return SalarySend{}, err
	}
	if err := o.require(ctx, tenant, actor, "Seeing payroll", RolePayroll, RoleHR); err != nil {
		return SalarySend{}, err
	}
	s, ok := o.salarySendOf(ctx, tenant, month)
	if !ok {
		return s, ErrNotFound
	}
	return s, nil
}

// salaryBankTick asks HDFC about accepted salary batches every 30 minutes
// in the day, at most 6 times a day.
func (o *Ops) salaryBankTick(ctx context.Context, tenant string, now time.Time) {
	ss, _ := docs.List[SalarySend](ctx, o.Docs, tenant, cSalarySend, "")
	day := now.In(ist).Format("2006-01-02")
	for _, s := range ss {
		if s.State != HSAccepted {
			continue
		}
		n, last := 0, time.Time{}
		for _, c := range s.Checks {
			if t, err := time.Parse(time.RFC3339, c.At); err == nil {
				if t.In(ist).Format("2006-01-02") == day {
					n++
				}
				if t.After(last) {
					last = t
				}
			}
		}
		sent, _ := time.Parse(time.RFC3339, s.SentAt)
		if n >= hdfcMaxDaily || now.Sub(sent) < 15*time.Minute || now.Sub(last) < 30*time.Minute {
			continue
		}
		_, _ = o.salaryInquire(ctx, tenant, SystemActor, s.ID)
	}
}
