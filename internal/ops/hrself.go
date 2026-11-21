package ops

import (
	"context"
	"fmt"
	"strings"

	"ledgerline/internal/docs"
)

// ---- employee self-service ----
//
// Every staff login linked to an employee sees only their own records:
// My profile, My attendance, My leave, My payslips and Form 130 (with the
// Payroll module), My declarations (tax and own trading) and My
// certificates. Managers also see their team (Team). Nothing here takes a
// code from the caller: the employee is always the signed-in person's.

// SelfView is everything a person sees about themselves.
type SelfView struct {
	Linked     bool           `json:"linked"` // the login is linked to an employee
	Profile    EmployeeView   `json:"profile"`
	Attendance AttendanceView `json:"attendance"`
	Leave      LeaveView      `json:"leave"`
	Payslips   []Payslip      `json:"payslips"`
	TaxDecl    *TaxDecl       `json:"tax_declaration,omitempty"`
	Claims     []Claim        `json:"claims"`
	OwnTrading OwnTradingView `json:"own_trading"`
	Manager    bool           `json:"manager"` // has a team
	TeamSize   int            `json:"team_size"`
	Payroll    bool           `json:"payroll"` // the Payroll module is on
	FY         string         `json:"fy"`
	Checklists []HRChecklist  `json:"checklists"` // the person's own joining or leaving checklist
}

// SelfService is the signed-in person's own HR records.
func (o *Ops) SelfService(ctx context.Context, tenant, actor, month string) (SelfView, error) {
	if err := o.needModule(ctx, tenant, ModHRMS); err != nil {
		return SelfView{}, err
	}
	sc, err := o.scopeFor(ctx, tenant, actor)
	if err != nil {
		return SelfView{}, err
	}
	v := SelfView{Payslips: []Payslip{}, Claims: []Claim{}, Checklists: []HRChecklist{}, Payroll: o.ModuleOn(ctx, tenant, ModPayroll), FY: finYear(o.today()), Manager: len(sc.team) > 0, TeamSize: len(sc.team)}
	if sc.self == "" {
		return v, nil
	}
	v.Linked = true
	e, err := o.employee(ctx, tenant, sc.self)
	if err != nil {
		return v, err
	}
	es, _ := o.employees(ctx, tenant)
	v.Profile = o.viewOf(ctx, tenant, e, false, es, o.hrPolicy(ctx, tenant))
	if v.Attendance, err = o.PersonAttendance(ctx, tenant, actor, sc.self, month); err != nil {
		return v, err
	}
	if v.Leave, err = o.MyLeave(ctx, tenant, actor); err != nil {
		return v, err
	}
	if v.OwnTrading, err = o.OwnTrading(ctx, tenant, actor, true); err != nil {
		return v, err
	}
	if v.Payroll {
		v.Payslips, _ = o.MyPayslips(ctx, tenant, actor)
		if d, _, err := docs.Get[TaxDecl](ctx, o.Docs, tenant, cTaxDecl, e.Code+"|"+v.FY); err == nil {
			v.TaxDecl = &d
		}
		cs, _ := docs.List[Claim](ctx, o.Docs, tenant, cClaim, "")
		for i := len(cs) - 1; i >= 0; i-- {
			if cs[i].Employee == e.Code {
				v.Claims = append(v.Claims, cs[i])
			}
		}
	}
	ls, _ := docs.List[HRChecklist](ctx, o.Docs, tenant, cHRList, e.Code+"|")
	for _, c := range ls {
		if c.Status == "Open" {
			v.Checklists = append(v.Checklists, c)
		}
	}
	return v, nil
}

// MyTaxDecl is the signed-in person's declaration for a year.
func (o *Ops) MyTaxDecl(ctx context.Context, tenant, actor, fy string) (TaxDecl, error) {
	if err := o.needModule(ctx, tenant, ModPayroll); err != nil {
		return TaxDecl{}, err
	}
	e, ok := o.employeeOf(ctx, tenant, actor)
	if !ok {
		return TaxDecl{}, fmt.Errorf("%w: your login is not linked to an employee", ErrForbidden)
	}
	d, _, err := docs.Get[TaxDecl](ctx, o.Docs, tenant, cTaxDecl, e.Code+"|"+firstOf(strings.TrimSpace(fy), finYear(o.today())))
	return d, err
}
