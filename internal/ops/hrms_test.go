package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
)

// Made-up people, offices and numbers only.

type hrRig struct {
	o     *Ops
	ctx   context.Context
	clock time.Time
}

func (r *hrRig) at(s string) {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, ist)
	if err != nil {
		panic(err)
	}
	r.clock = t
}

func hrSetup(t *testing.T) *hrRig {
	t.Helper()
	o, ctx := newOps(t)
	r := &hrRig{o: o, ctx: ctx, clock: time.Date(2026, 5, 4, 10, 0, 0, 0, ist)}
	o.Now = func() time.Time { return r.clock }
	o.WorkDir = t.TempDir()
	for _, u := range []User{{ID: "hema.hr", Name: "Hema (HR)", Role: RoleHR, Active: true}, {ID: "harsh.hr", Name: "Harsh (HR)", Role: RoleHR, Active: true},
		{ID: "paul.pay", Name: "Paul (Payroll)", Role: RolePayroll, Active: true}, {ID: "dev.dealer", Name: "Dev (dealer)", Role: RoleMaker, Active: true},
		{ID: "kavi.blr", Name: "Kavi (Bengaluru)", Role: RoleBranch, Branch: "Bengaluru", Active: true}} {
		if err := o.PutUser(ctx, T, "admin", u); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range []Branch{{Code: "Bengaluru", Name: "Bengaluru branch", Manager: "meera.iyer", Active: true, Kind: "Branch"}, {Code: "Delhi", Name: "Delhi branch", Active: true, Kind: "Branch"}} {
		if err := docs.Upsert(ctx, o.Docs, T, cBranch, b.Code, b, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.SetModules(ctx, T, "admin", ModuleSwitches{HRMS: true, Payroll: true}); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *hrRig) add(t *testing.T, e Employee) Employee {
	t.Helper()
	x, err := r.o.SaveEmployee(r.ctx, T, "hema.hr", e)
	if err != nil {
		t.Fatalf("save %s: %v", e.Name, err)
	}
	return x
}

func workOpen(o *Ops, ctx context.Context, sub string) []WorkItem {
	ws, _ := docs.List[WorkItem](ctx, o.Docs, T, cWorkItem, "")
	var out []WorkItem
	for _, w := range ws {
		if w.Status == "Open" && strings.Contains(w.Key, sub) {
			out = append(out, w)
		}
	}
	return out
}

func TestModuleSwitchAndControl(t *testing.T) {
	o, ctx := newOps(t)
	if _, err := o.Staff(ctx, T, "admin"); !errors.Is(err, ErrModuleOff) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("module off: %v", err)
	}
	if _, err := o.SetModules(ctx, T, "priya.nair", ModuleSwitches{HRMS: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker switched a module: %v", err)
	}
	// Control switches payroll on: HRMS comes with it; the report says so.
	if err := o.applyModule(ctx, T, ModPayroll, true); err != nil {
		t.Fatal(err)
	}
	if m := o.Modules(ctx, T); !m.On(ModPayroll) || !m.HRMS {
		t.Fatalf("payroll on did not bring HRMS: %+v", m)
	}
	if err := o.applyModule(ctx, T, ModHRMS, true); err != nil {
		t.Fatal(err)
	}
	f, err := o.FirmReport(ctx, T)
	if err != nil || strings.Join(f.Modules, ",") != "hrms,payroll" {
		t.Fatalf("report modules %v %v", f.Modules, err)
	}
	if _, err := o.Staff(ctx, T, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := o.applyModule(ctx, T, ModHRMS, false); err != nil {
		t.Fatal(err)
	}
	if m := o.Modules(ctx, T); m.On(ModPayroll) || m.HRMS || m.Payroll {
		t.Fatalf("HRMS off leaves payroll on: %+v", m)
	}
	if err := o.applyModule(ctx, T, "crm", true); err == nil {
		t.Fatal("unknown module accepted")
	}
}

func TestEmployeeMasterMaskingAndScope(t *testing.T) {
	r := hrSetup(t)
	o, ctx := r.o, r.ctx
	if _, err := o.SaveEmployee(ctx, T, "hema.hr", Employee{Name: "X", JoinedOn: "2026-01-01", Aadhaar4: "123412341234"}); err == nil {
		t.Fatal("full Aadhaar accepted")
	}
	boss := r.add(t, Employee{Name: "Meera Test", JoinedOn: "2024-01-01", Branch: "Bengaluru", User: "meera.iyer", Designation: "Branch manager"})
	a := r.add(t, Employee{Name: "Anil Test", JoinedOn: "2025-02-01", Branch: "Bengaluru", Manager: boss.Code, User: "kavi.blr", PAN: "AAAPZ1234Q", Aadhaar4: "4321", UAN: "100000000001", Bank: SalaryBank{Account: "000111222333", IFSC: "SBIN0000001"}})
	d := r.add(t, Employee{Name: "Deepa Test", JoinedOn: "2025-03-01", Branch: "Delhi", User: "dev.dealer"})
	// HR sees everyone, masked.
	v, err := o.GetEmployee(ctx, T, "hema.hr", a.Code)
	if err != nil || v.PAN != "AAAPZXXXXQ" || v.Bank.Account != "XXXXXXXX2333" || v.Aadhaar4 != "XXXX XXXX 4321" || v.UAN != "XXXXXXXX0001" {
		t.Fatalf("masked view %+v %v", v.Employee, err)
	}
	// Reveal: HR, with a reason, recorded.
	if _, err := o.RevealEmployee(ctx, T, "hema.hr", a.Code, "pan", "no"); err == nil {
		t.Fatal("reveal without a reason")
	}
	if p, err := o.RevealEmployee(ctx, T, "hema.hr", a.Code, "pan", "Form 130 for the employee"); err != nil || p != "AAAPZ1234Q" {
		t.Fatalf("reveal %q %v", p, err)
	}
	if _, err := o.RevealEmployee(ctx, T, "priya.nair", a.Code, "pan", "curious about it"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker revealed: %v", err)
	}
	// A masked value sent back keeps the stored one.
	a.PAN = "AAAPZXXXXQ"
	a.Designation = "Dealer"
	if x, err := o.SaveEmployee(ctx, T, "hema.hr", a); err != nil || x.PAN != "AAAPZ1234Q" {
		t.Fatalf("masked PAN overwrote: %v %v", x.PAN, err)
	}
	// Self-service: own record only.
	if _, err := o.GetEmployee(ctx, T, "kavi.blr", d.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("self-service saw another person: %v", err)
	}
	if s, err := o.Staff(ctx, T, "kavi.blr"); err != nil || len(s.Employees) != 1 || s.Employees[0].Code != a.Code {
		t.Fatalf("self list %+v %v", s, err)
	}
	me, err := o.SelfService(ctx, T, "kavi.blr", "")
	if err != nil || me.Profile.Code != a.Code || me.Profile.PAN != "AAAPZXXXXQ" {
		t.Fatalf("self-service %+v %v", me.Profile.Employee, err)
	}
	// The branch manager (named on the office) sees her branch, not Delhi;
	// the team view has no identifiers.
	s, err := o.Staff(ctx, T, "meera.iyer")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]EmployeeView{}
	for _, e := range s.Employees {
		seen[e.Code] = e
	}
	if _, ok := seen[d.Code]; ok || len(seen) != 2 || !seen[a.Code].Team || seen[a.Code].PAN != "" || seen[a.Code].Bank.Account != "" {
		t.Fatalf("branch manager's view: %+v", s.Employees)
	}
	if _, err := o.GetEmployee(ctx, T, "meera.iyer", d.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("branch manager saw Delhi: %v", err)
	}
	org, _ := o.OrgChart(ctx, T, "hema.hr")
	found := false
	for _, n := range org {
		if n.Code == boss.Code && len(n.Reports) == 1 && n.Reports[0].Code == a.Code {
			found = true
		}
	}
	if !found {
		t.Fatalf("org chart %+v", org)
	}
	// No reporting loops.
	boss.Manager = a.Code
	if _, err := o.SaveEmployee(ctx, T, "hema.hr", boss); err == nil {
		t.Fatal("reporting loop accepted")
	}
}

func TestJoinMoveLeaveWithApproval(t *testing.T) {
	r := hrSetup(t)
	o, ctx := r.o, r.ctx
	e := r.add(t, Employee{Name: "Lalit Test", JoinedOn: "2026-05-04", Duties: []string{"Dealer (equity derivatives)"}})
	cs, _ := o.Checklists(ctx, T, "hema.hr", false)
	if len(cs) != 1 || cs[0].Kind != "join" || len(cs[0].Items) != 8 {
		t.Fatalf("joining checklist %+v", cs)
	}
	if len(workOpen(o, ctx, e.Code+":join:")) != 8 {
		t.Fatalf("joining work items %d", len(workOpen(o, ctx, e.Code+":join:")))
	}
	if _, err := o.LinkUser(ctx, T, "hema.hr", e.Code, "dev.dealer"); err != nil {
		t.Fatal(err)
	}
	cs, _ = o.Checklists(ctx, T, "hema.hr", false)
	if !cs[0].Items[0].Done {
		t.Fatal("linking the login did not tick the item")
	}
	// A change of office raises an access review.
	e, _ = o.employee(ctx, T, e.Code)
	e.Branch = "Delhi"
	if _, err := o.SaveEmployee(ctx, T, "hema.hr", e); err != nil {
		t.Fatal(err)
	}
	if len(workOpen(o, ctx, e.Code+":move:")) == 0 {
		t.Fatal("no access review")
	}
	// Leaving: recorded by one HR manager, approved by another.
	r.at("2026-05-20 10:00")
	if _, err := o.StartLeaving(ctx, T, "hema.hr", e.Code, "2026-05-29", "Resigned"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApproveLeaving(ctx, T, "hema.hr", e.Code); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the requester approved: %v", err)
	}
	if n := len(workOpen(o, ctx, e.Code+":leave:2026-05-29:")); n < 7 {
		t.Fatalf("leaving work items %d", n)
	}
	// Not approved by the last day: the login stays and HR is told.
	r.at("2026-05-29 19:00")
	o.leaverTick(ctx, T, r.clock)
	if u, _ := o.user(ctx, T, "dev.dealer"); !u.Active {
		t.Fatal("deactivated without approval")
	}
	if len(workOpen(o, ctx, "leave-unapproved")) != 1 {
		t.Fatal("no reminder for the unapproved leaving")
	}
	// Approved by a second person: switched off at once (past 18:30 on the last day).
	l, err := o.ApproveLeaving(ctx, T, "harsh.hr", e.Code)
	if err != nil || l.Status != "Done" || l.ApprovedBy != "harsh.hr" {
		t.Fatalf("approve %+v %v", l, err)
	}
	if u, _ := o.user(ctx, T, "dev.dealer"); u.Active {
		t.Fatal("login still on after the last day")
	}
	if x, _ := o.employee(ctx, T, e.Code); x.Status != EmpLeft {
		t.Fatalf("status %s", x.Status)
	}
	c, _, _ := docs.Get[HRChecklist](ctx, o.Docs, T, cHRList, listKey(e.Code, "leave", "2026-05-29"))
	if !c.Items[0].Done || c.Items[0].Key != "deactivate" {
		t.Fatalf("deactivate item %+v", c.Items[0])
	}
	if _, err := o.DoCheckItem(ctx, T, "hema.hr", c.ID, "deactivate", false, ""); err == nil {
		t.Fatal("deactivate ticked by hand")
	}
}

func TestLeaverOnLastDayBeforeTime(t *testing.T) {
	r := hrSetup(t)
	o, ctx := r.o, r.ctx
	e := r.add(t, Employee{Name: "Mona Test", JoinedOn: "2025-01-01", User: "priya.nair"})
	if _, err := o.StartLeaving(ctx, T, "hema.hr", e.Code, "2026-05-08", "Retired"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ApproveLeaving(ctx, T, "admin", e.Code); err != nil {
		t.Fatal(err)
	}
	r.at("2026-05-08 12:00")
	o.Tick(ctx, T, r.clock)
	if u, _ := o.user(ctx, T, "priya.nair"); !u.Active {
		t.Fatal("switched off before the time on the last day")
	}
	r.at("2026-05-08 18:31")
	o.Tick(ctx, T, r.clock)
	if u, _ := o.user(ctx, T, "priya.nair"); u.Active {
		t.Fatal("not switched off on the last day")
	}
}

func TestCertificateExpiryRemindersAndTerminalFlag(t *testing.T) {
	r := hrSetup(t)
	o, ctx := r.o, r.ctx
	d := Dealer{ID: "DL-0009", FirstName: "Neel", LastName: "Test", PAN: "AAAPN1111N", Relationship: DealerEmployee, Status: DealerActive,
		NISM: NISMCert{CertNo: "OTHER-1"}, Terminals: []DealerTerminal{{Exchange: "NSE", Segments: []string{"FO"}, TerminalID: "41234", ActiveFrom: "2025-01-01"}}}
	if err := docs.Upsert(ctx, o.Docs, T, cDealer, d.ID, d, "test"); err != nil {
		t.Fatal(err)
	}
	e := r.add(t, Employee{Name: "Neel Test", JoinedOn: "2025-01-01", Duties: []string{"Dealer (equity derivatives)", "DP operations"}, Dealer: d.ID})
	if _, err := o.SaveStaffCert(ctx, T, "vikram.shah", e.Code, StaffCert{Cert: "NISM Series VIII (Equity Derivatives)", Number: "NISM-VIII-TEST-1", ValidTill: "2026-07-01"}); err != nil {
		t.Fatal(err)
	}
	// 58 days before: the 60-day reminder; the DP certificate is missing.
	r.at("2026-05-04 07:00")
	o.hrCertTick(ctx, T, r.clock)
	if len(workOpen(o, ctx, ":cert-60:")) != 1 || len(workOpen(o, ctx, ":cert-missing:NISM Series VI")) != 1 {
		t.Fatalf("60-day: %v / missing %v", workOpen(o, ctx, ":cert-60:"), workOpen(o, ctx, ":cert-missing"))
	}
	// 25 days before: the 30-day reminder replaces it.
	r.at("2026-06-06 07:00")
	o.hrCertTick(ctx, T, r.clock)
	if len(workOpen(o, ctx, ":cert-30:")) != 1 || len(workOpen(o, ctx, ":cert-60:")) != 0 {
		t.Fatal("30-day reminder")
	}
	r.at("2026-06-26 07:00")
	o.hrCertTick(ctx, T, r.clock)
	if len(workOpen(o, ctx, ":cert-7:")) != 1 || len(workOpen(o, ctx, ":cert-30:")) != 0 {
		t.Fatal("7-day reminder")
	}
	// Expired: a work item that flags the terminal.
	r.at("2026-07-02 07:00")
	o.hrCertTick(ctx, T, r.clock)
	ex := workOpen(o, ctx, ":cert-expiry:")
	if len(ex) != 1 || !strings.Contains(ex[0].Title, "terminal flagged") || !strings.Contains(ex[0].Detail, "NSE 41234") {
		t.Fatalf("expiry item %+v", ex)
	}
	reg, _ := o.StaffCertRegister(ctx, T, "vikram.shah")
	flagged := false
	for _, row := range reg.Rows {
		flagged = flagged || (row.Flagged && row.Code == e.Code && strings.Contains(row.Cert, "VIII"))
	}
	if !flagged {
		t.Fatalf("register not flagged: %+v", reg.Rows)
	}
	if v, _ := o.GetEmployee(ctx, T, "hema.hr", e.Code); !v.TerminalFlag {
		t.Fatal("employee not flagged")
	}
	// The rules are the firm's: without the derivatives rule nothing is needed.
	p := o.hrPolicy(ctx, T)
	p.CertRules = []CertRule{{Duty: "DP operations", Cert: "NISM Series VI (Depository Operations)", Basis: "the firm's policy"}}
	if _, err := o.SaveHRPolicy(ctx, T, "vikram.shah", p); err != nil {
		t.Fatal(err)
	}
	p.CertRules[0].Basis = ""
	if _, err := o.SaveHRPolicy(ctx, T, "vikram.shah", p); err == nil {
		t.Fatal("a rule without its basis was accepted")
	}
}

func TestOwnTradingPreClearance(t *testing.T) {
	r := hrSetup(t)
	o, ctx := r.o, r.ctx
	e := r.add(t, Employee{Name: "Omkar Test", JoinedOn: "2025-01-01", User: "priya.nair", Designated: true})
	a, err := o.DeclareAccount(ctx, T, "priya.nair", "", StaffAccount{Relation: "Spouse", Holder: "Spouse Test", Broker: "Other Broker Ltd", Account: "ZZ999", Outside: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(workOpen(o, ctx, "outside-acct")) != 1 {
		t.Fatal("outside account not raised to compliance")
	}
	p := o.hrPolicy(ctx, T)
	p.OwnTrading.Restricted = []string{"RESTRICTCO"}
	if _, err := o.SaveHRPolicy(ctx, T, "vikram.shah", p); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RequestPreClearance(ctx, T, "priya.nair", PreClearance{Account: a.ID, Security: "RESTRICTCO", Side: "Buy", Qty: 10}); err == nil {
		t.Fatal("restricted security pre-cleared")
	}
	pc, err := o.RequestPreClearance(ctx, T, "priya.nair", PreClearance{Account: a.ID, Security: "TESTCO", Side: "Buy", Qty: 100, Value: 2000000_00})
	if err != nil || !pc.Needed {
		t.Fatalf("request %+v %v", pc, err)
	}
	if _, err := o.DecidePreClearance(ctx, T, "priya.nair", pc.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("self approval: %v", err)
	}
	if pc, err = o.DecidePreClearance(ctx, T, "vikram.shah", pc.ID, true, ""); err != nil || pc.ValidTill != "2026-05-11" {
		t.Fatalf("approve %+v %v", pc, err)
	}
	// An opposite trade within the contra period is flagged.
	r.at("2026-06-01 10:00")
	pc2, err := o.RequestPreClearance(ctx, T, "priya.nair", PreClearance{Account: a.ID, Security: "TESTCO", Side: "Sell", Qty: 100})
	if err != nil || len(pc2.Warnings) != 1 || !strings.Contains(pc2.Warnings[0], "Contra") {
		t.Fatalf("contra %+v %v", pc2, err)
	}
	if _, err := o.DeclareHoldings(ctx, T, "priya.nair", "", HoldingsDecl{Kind: "Annual", Nil: true}); err != nil {
		t.Fatal(err)
	}
	v, _ := o.OwnTrading(ctx, T, "vikram.shah", false)
	if len(v.Missing) != 0 || len(v.Accounts) != 1 || v.Accounts[0].Employee != e.Code {
		t.Fatalf("register %+v", v)
	}
}

func TestLeaveAccrualApprovalAndOnLeave(t *testing.T) {
	r := hrSetup(t)
	o, ctx := r.o, r.ctx
	boss := r.add(t, Employee{Name: "Bala Test", JoinedOn: "2024-01-01", User: "farhan.khan"})
	// Joined 10 Feb 2026: CL 8 a year prorated for 11 months = 7.33 → 7.5;
	// EL 1.5 a month for Feb, Mar, Apr (completed months to 4 May) = 4.5.
	e := r.add(t, Employee{Name: "Chitra Test", JoinedOn: "2026-02-10", Manager: boss.Code, User: "priya.nair", Gender: "F"})
	cover := r.add(t, Employee{Name: "Cover Test", JoinedOn: "2024-01-01", User: "anita.rao"})
	bal := map[string]Balance{}
	for _, b := range o.balances(ctx, T, e, "2026-05-04") {
		bal[b.Type] = b
	}
	if bal["CL"].Granted != 7.5 || bal["EL"].Accrued != 4.5 || bal["EL"].Balance != 4.5 || bal["SL"].Granted != 6.5 {
		t.Fatalf("balances %+v", bal)
	}
	// 11 to 15 May 2026 is Mon to Fri: 5 working days (Sat and Sun are weekly offs).
	if _, err := o.ApplyLeave(ctx, T, "priya.nair", LeaveRequest{Type: "EL", From: "2026-05-11", To: "2026-05-17", Backup: cover.Code}); err == nil {
		t.Fatal("5 days of EL on a 4.5 balance")
	}
	lr, err := o.ApplyLeave(ctx, T, "priya.nair", LeaveRequest{Type: "EL", From: "2026-05-11", To: "2026-05-14", Backup: cover.Code, Reason: "Family"})
	if err != nil || lr.Days != 4 {
		t.Fatalf("apply %+v %v", lr, err)
	}
	if len(workOpen(o, ctx, "leave:"+lr.ID)) != 1 || !strings.Contains(workOpen(o, ctx, "leave:"+lr.ID)[0].Title, "for Bala Test") {
		t.Fatal("the manager was not asked")
	}
	if _, err := o.DecideLeave(ctx, T, "priya.nair", lr.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("approved own leave: %v", err)
	}
	if _, err := o.DecideLeave(ctx, T, "anita.rao", lr.ID, true, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a non-manager approved: %v", err)
	}
	if lr, err = o.DecideLeave(ctx, T, "farhan.khan", lr.ID, true, ""); err != nil || lr.Status != "Approved" {
		t.Fatalf("manager approval %+v %v", lr, err)
	}
	ol, err := o.OnLeave(ctx, T, "2026-05-12")
	if err != nil || len(ol) != 1 || ol[0].User != "priya.nair" || ol[0].BackupUser != "anita.rao" {
		t.Fatalf("on leave %+v %v", ol, err)
	}
	if b, ok := o.BackupFor(ctx, T, "priya.nair", "2026-05-12"); !ok || b != "anita.rao" {
		t.Fatal("backup")
	}
	if _, ok := o.BackupFor(ctx, T, "priya.nair", "2026-05-15"); ok {
		t.Fatal("on leave after the leave")
	}
	for _, b := range o.balances(ctx, T, e, "2026-05-31") {
		if b.Type == "EL" && (b.Taken != 4 || b.Balance != 2) {
			t.Fatalf("EL after leave and May's accrual: %+v", b)
		}
	}
	tc, _ := o.Team(ctx, T, "farhan.khan", "2026-05")
	if len(tc.Leave) != 1 || len(tc.People) != 1 {
		t.Fatalf("team calendar %+v", tc)
	}
	// Year end: EL carries up to 30, encashes up to 15, the rest lapses.
	r.at("2027-01-02 10:00")
	rows, err := o.CloseLeaveYear(ctx, T, "hema.hr", "2026-12-31")
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range rows {
		// EL: 1.5 × 11 months (Feb to Dec) = 16.5, less 4 taken = 12.5: all carried.
		if x.Employee == e.Code && x.Type == "EL" && (x.Closing != 12.5 || x.Carried != 12.5) {
			t.Fatalf("EL close %+v", x)
		}
		// CL lapses (no carry, no encashment).
		if x.Employee == e.Code && x.Type == "CL" && (x.Lapsed != 7.5 || x.Carried != 0) {
			t.Fatalf("CL close %+v", x)
		}
	}
	for _, b := range o.balances(ctx, T, e, "2027-01-02") {
		if b.Type == "EL" && b.Carried != 12.5 {
			t.Fatalf("EL carried into 2027: %+v", b)
		}
	}
	if _, err := o.CloseLeaveYear(ctx, T, "hema.hr", "2026-12-31"); err == nil {
		t.Fatal("closed twice")
	}
}

// ---- attendance: phone, kiosk, hybrid ----

func punchSetup(t *testing.T) (*hrRig, Employee) {
	r := hrSetup(t)
	p := r.o.hrPolicy(r.ctx, T)
	p.Sites = []Site{{Branch: "", Lat: 19.0760, Lng: 72.8777, RadiusM: 100, Networks: []string{"10.20.0.0/16"}}}
	p.WorkPolicies = []WorkPolicy{{ID: "HYB", Name: "Hybrid", Modes: []string{"office", "home", "field"}, OfficePerWeek: 3, FixedHomeDays: []string{"Fri"}}}
	p.UseSignInNets = false
	if _, err := r.o.SaveHRPolicy(r.ctx, T, "hema.hr", p); err != nil {
		t.Fatal(err)
	}
	e := r.add(t, Employee{Name: "Pooja Test", JoinedOn: "2025-01-01", User: "priya.nair", Manager: "", WorkPolicy: "HYB"})
	return r, e
}

func TestPhonePunchDeviceBindingAndOffice(t *testing.T) {
	r, e := punchSetup(t)
	o, ctx := r.o, r.ctx
	r.at("2026-05-04 09:20") // Monday
	in := PunchReq{Kind: "in", Mode: "office", DeviceID: "phone-aaaa-1111", Unlocked: true, IP: "203.0.113.9"}
	// Biometric unlock is required by the app.
	in.Unlocked = false
	if _, err := o.PunchAttendance(ctx, T, "priya.nair", in); err == nil {
		t.Fatal("punch without the phone's unlock")
	}
	in.Unlocked = true
	// Off the network and away from the office: refused (the phone is bound anyway).
	if _, err := o.PunchAttendance(ctx, T, "priya.nair", in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("office punch from outside: %v", err)
	}
	// Within 100 m of the office (about 50 m north).
	in.HasLoc, in.Lat, in.Lng = true, 19.07645, 72.8777
	res, err := o.PunchAttendance(ctx, T, "priya.nair", in)
	if err != nil || res.Site != "Head office" {
		t.Fatalf("geofence punch %+v %v", res, err)
	}
	// 300 m away: refused; on the office network: accepted.
	in.Lat = 19.0787
	if _, err := o.PunchAttendance(ctx, T, "priya.nair", in); err == nil {
		t.Fatal("punch 300 m away accepted")
	}
	r.at("2026-05-04 18:40")
	in.IP, in.HasLoc = "10.20.4.5", false
	in.Kind = "out"
	if _, err := o.PunchAttendance(ctx, T, "priya.nair", in); err != nil {
		t.Fatalf("network punch: %v", err)
	}
	// A second phone waits for HR.
	in.DeviceID = "phone-bbbb-2222"
	if _, err := o.PunchAttendance(ctx, T, "priya.nair", in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("new phone accepted: %v", err)
	}
	if len(workOpen(o, ctx, e.Code+":device:")) != 1 {
		t.Fatal("HR not asked about the new phone")
	}
	if _, err := o.ApproveDevice(ctx, T, "priya.nair", e.Code); !errors.Is(err, ErrForbidden) {
		t.Fatalf("self approval of a phone: %v", err)
	}
	if _, err := o.ApproveDevice(ctx, T, "hema.hr", e.Code); err != nil {
		t.Fatal(err)
	}
	if _, err := o.PunchAttendance(ctx, T, "priya.nair", in); err != nil {
		t.Fatalf("approved phone: %v", err)
	}
	if _, err := o.PunchAttendance(ctx, T, "priya.nair", PunchReq{Kind: "in", Mode: "office", DeviceID: "phone-aaaa-1111", Unlocked: true, IP: "10.20.4.5"}); err == nil {
		t.Fatal("the old phone still works")
	}
	// Field punches need the location and a note.
	if _, err := o.PunchAttendance(ctx, T, "priya.nair", PunchReq{Kind: "in", Mode: "field", DeviceID: "phone-bbbb-2222", Unlocked: true}); err == nil {
		t.Fatal("field punch without location")
	}
	a, _, _ := docs.Get[Attendance](ctx, o.Docs, T, cAttend, e.Code+"|2026-05-04")
	if a.Status != "Present" || a.Mode != "office" || a.In != "09:20" {
		t.Fatalf("day %+v", a)
	}
}

func TestHybridHomeRulesAndDealer(t *testing.T) {
	r, e := punchSetup(t)
	o, ctx := r.o, r.ctx
	home := PunchReq{Kind: "in", Mode: "home", DeviceID: "phone-cccc-3333", Unlocked: true}
	r.at("2026-05-05 09:30") // Tuesday: not a home day
	if _, err := o.PunchAttendance(ctx, T, "priya.nair", home); !errors.Is(err, ErrForbidden) {
		t.Fatalf("home on a Tuesday: %v", err)
	}
	r.at("2026-05-08 09:30") // Friday: a fixed home day
	if _, err := o.PunchAttendance(ctx, T, "priya.nair", home); err != nil {
		t.Fatalf("home on Friday: %v", err)
	}
	// An approved work-from-home day.
	r.at("2026-05-11 08:00")
	w, err := o.RequestWFH(ctx, T, "priya.nair", "2026-05-12", "Plumber at home")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecideWFH(ctx, T, "priya.nair", w.ID, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("own WFH approved: %v", err)
	}
	if _, err := o.DecideWFH(ctx, T, "hema.hr", w.ID, true); err != nil {
		t.Fatal(err)
	}
	r.at("2026-05-12 09:30")
	if _, err := o.PunchAttendance(ctx, T, "priya.nair", home); err != nil {
		t.Fatalf("approved WFH day: %v", err)
	}
	// A dealer cannot work from home without an approved home location.
	e.Duties = []string{"Dealer (equity cash)"}
	if _, err := o.SaveEmployee(ctx, T, "hema.hr", e); err != nil {
		t.Fatal(err)
	}
	r.at("2026-05-15 09:30") // Friday
	if _, err := o.PunchAttendance(ctx, T, "priya.nair", home); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "exchange-approved") {
		t.Fatalf("dealer at home: %v", err)
	}
	if _, err := o.RequestWFH(ctx, T, "priya.nair", "2026-05-19", "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("dealer WFH request: %v", err)
	}
	// The rule is the firm's: with it lifted the dealer may work from home
	// on a home day; it is on again below.
	p := o.hrPolicy(ctx, T)
	p.DealersHomeFree = true
	if _, err := o.SaveHRPolicy(ctx, T, "hema.hr", p); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RequestWFH(ctx, T, "priya.nair", "2026-05-19", "x"); err != nil {
		t.Fatalf("dealer WFH with the rule lifted: %v", err)
	}
	p.DealersHomeFree = false
	if _, err := o.SaveHRPolicy(ctx, T, "hema.hr", p); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetHomeDealing(ctx, T, "hema.hr", e.Code, HomeDealing{Approved: true, Ref: "NSE/TEST/1", Date: "2026-05-14"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("HR recorded an exchange approval: %v", err)
	}
	if _, err := o.SetHomeDealing(ctx, T, "vikram.shah", e.Code, HomeDealing{Approved: true, Ref: "NSE/TEST/1", Date: "2026-05-14"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.PunchAttendance(ctx, T, "priya.nair", home); err != nil {
		t.Fatalf("dealer with an approved home: %v", err)
	}
	// The month's report: office days below the policy go to the manager (HR here).
	rows, err := o.WorkReport(ctx, T, "hema.hr", "2026-05", true)
	if err != nil || len(rows) != 1 || rows[0].Summary.Home != 3 || len(rows[0].Summary.Exceptions) == 0 {
		t.Fatalf("report %+v %v", rows, err)
	}
	if len(workOpen(o, ctx, ":work-policy:2026-05")) != 1 {
		t.Fatal("exception not raised")
	}
}

func TestKioskPINLockoutAndPhotoRetention(t *testing.T) {
	r, e := punchSetup(t)
	o, ctx := r.o, r.ctx
	k, code, err := o.CreateKiosk(ctx, T, "hema.hr", "Entrance", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := o.PairKiosk(ctx, T, code, "198.51.100.7"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("paired off the office network: %v", err)
	}
	_, tok, err := o.PairKiosk(ctx, T, code, "10.20.1.1")
	if err != nil || !strings.HasPrefix(tok, k.ID+".") {
		t.Fatalf("pair %q %v", tok, err)
	}
	if _, _, err := o.PairKiosk(ctx, T, code, "10.20.1.1"); err == nil {
		t.Fatal("pairing code used twice")
	}
	if err := o.SetKioskPIN(ctx, T, "priya.nair", "1234"); err == nil {
		t.Fatal("a run of digits as PIN")
	}
	if err := o.SetKioskPIN(ctx, T, "priya.nair", "4071"); err != nil {
		t.Fatal(err)
	}
	r.at("2026-05-04 09:05")
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 64)...)
	res, err := o.KioskPunch(ctx, T, tok, "10.20.1.1", KioskPunchReq{Employee: e.Code, PIN: "4071", Kind: "in", Photo: jpeg})
	if err != nil || res.Name != "Pooja Test" || res.Employee != "" {
		t.Fatalf("kiosk punch %+v %v", res, err)
	}
	if _, err := o.KioskPunch(ctx, T, tok, "198.51.100.7", KioskPunchReq{Employee: e.Code, PIN: "4071", Kind: "out"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("kiosk used off the network")
	}
	if _, err := o.KioskPunch(ctx, T, "K999.bad", "10.20.1.1", KioskPunchReq{Employee: e.Code, PIN: "4071", Kind: "out"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("unregistered kiosk")
	}
	for i := 0; i < PINTries; i++ {
		if _, err := o.KioskPunch(ctx, T, tok, "10.20.1.1", KioskPunchReq{Employee: e.Code, PIN: "9999", Kind: "out"}); err == nil {
			t.Fatal("wrong PIN accepted")
		}
	}
	if _, err := o.KioskPunch(ctx, T, tok, "10.20.1.1", KioskPunchReq{Employee: e.Code, PIN: "4071", Kind: "out"}); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("right PIN after the lock: %v", err)
	}
	if err := o.UnlockKioskPIN(ctx, T, "hema.hr", e.Code); err != nil {
		t.Fatal(err)
	}
	if _, err := o.KioskPunch(ctx, T, tok, "10.20.1.1", KioskPunchReq{Employee: e.Code, PIN: "4071", Kind: "out"}); err != nil {
		t.Fatalf("after unlock: %v", err)
	}
	// The photo: for HR or the manager only, deleted after 90 days.
	a, _, _ := docs.Get[Attendance](ctx, o.Docs, T, cAttend, e.Code+"|2026-05-04")
	ph := a.Punches[0].Selfie
	if ph == "" {
		t.Fatal("no photo kept")
	}
	if _, err := o.PunchPhotoFile(ctx, T, "priya.nair", ph); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the person saw their kiosk photo: %v", err)
	}
	if b, err := o.PunchPhotoFile(ctx, T, "hema.hr", ph); err != nil || len(b) != len(jpeg) {
		t.Fatalf("HR photo %d %v", len(b), err)
	}
	dir, _ := o.photoDir(T)
	if _, err := os.Stat(filepath.Join(dir, ph+".bin")); err != nil {
		t.Fatal(err)
	}
	r.at("2026-08-02 06:00") // 90 days later: kept
	if n := o.photoPurge(ctx, T, r.clock); n != 0 {
		t.Fatalf("purged at 90 days: %d", n)
	}
	r.at("2026-08-03 06:00")
	if n := o.photoPurge(ctx, T, r.clock); n != 1 {
		t.Fatalf("purged %d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, ph+".bin")); !os.IsNotExist(err) {
		t.Fatal("photo file still there")
	}
	if _, err := o.PunchPhotoFile(ctx, T, "hema.hr", ph); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted photo served")
	}
}

func TestAttendanceCSVAndManagerMark(t *testing.T) {
	r := hrSetup(t)
	o, ctx := r.o, r.ctx
	boss := r.add(t, Employee{Name: "Raman Test", JoinedOn: "2024-01-01", User: "farhan.khan"})
	e := r.add(t, Employee{Name: "Sita Test", JoinedOn: "2025-01-01", DeviceID: "77", Manager: boss.Code, User: "priya.nair"})
	csvf := "UserID,DateTime\n77,2026-05-04 09:31:00\n77,2026-05-04 18:40:00\n77,2026-05-05 09:10:00\n77,2026-05-05 12:00:00\n99,2026-05-04 09:00:00\n"
	res, err := o.ImportAttendanceCSV(ctx, T, "hema.hr", []byte(csvf))
	if err != nil || res.Days != 2 || len(res.Unknown) != 1 {
		t.Fatalf("import %+v %v", res, err)
	}
	a, _, _ := docs.Get[Attendance](ctx, o.Docs, T, cAttend, e.Code+"|2026-05-05")
	if a.Status != "Half day" || a.In != "09:10" || a.Out != "12:00" {
		t.Fatalf("half day %+v", a)
	}
	if _, err := o.MarkAttendance(ctx, T, "priya.nair", e.Code, "2026-05-04", "Absent", "", "", "", "self"); !errors.Is(err, ErrForbidden) {
		t.Fatal("marked own attendance")
	}
	if _, err := o.MarkAttendance(ctx, T, "farhan.khan", e.Code, "2026-05-01", "Absent", "", "", "", "no show, no call"); err != nil {
		t.Fatal(err)
	}
}
